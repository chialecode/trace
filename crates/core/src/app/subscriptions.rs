//! Subscriptions: create, reconfigure, run now and list.
use super::{parse, unsupported};
use crate::core::Core;
use crate::error::CoreError;
use crate::store::subscriptions::{self as repo, NewSubscription};
use crate::{jobs, store};
use rusqlite::{Connection, Transaction};
use serde_json::{Value, json};
use std::sync::Arc;
use trace_contracts::control as ctl;
use trace_contracts::control::Request;
use trace_contracts::control::commands as cmd;
use trace_domain::jobs::ScanMode;

const CONTENT_TYPES: [&str; 5] = ["video", "dynamic", "image", "article", "live"];
const INTERVAL_SECONDS: std::ops::RangeInclusive<i64> = 5..=86400;
const LIVE_DEFAULT_INTERVAL_SECONDS: i64 = 30;

pub async fn route(core: &Arc<Core>, req: &Request) -> Result<Value, CoreError> {
    let args = req.args.clone();
    match req.command.as_str() {
        cmd::SUBSCRIPTION_CREATE => create(core, parse(args)?).await,
        cmd::SUBSCRIPTION_UPDATE => update(core, parse(args)?, req.if_revision).await,
        cmd::SUBSCRIPTION_CHECK_NOW => check_now(core, parse(args)?).await,
        cmd::SUBSCRIPTION_LIST => list(core, parse(args)?).await,
        other => Err(unsupported(other)),
    }
}

fn check_interval(seconds: Option<i64>) -> Result<(), CoreError> {
    if seconds.is_some_and(|n| !INTERVAL_SECONDS.contains(&n)) {
        return Err(CoreError::InvalidInput(
            "subscription interval outside 5..86400 seconds".into(),
        ));
    }
    Ok(())
}

/// The credential must be valid and belong to the subscription's platform.
fn incompatible_credential() -> CoreError {
    CoreError::InvalidInput(
        "credential must be valid and belong to the subscription platform".into(),
    )
}

/// Validated pieces of a new subscription that the transaction stores as text.
struct Prepared {
    quality_json: String,
    range: Value,
    interval_seconds: i64,
}

fn prepare(a: &ctl::SubscriptionCreateArgs) -> Result<Prepared, CoreError> {
    crate::quality::validate_requirement(&a.quality)?;
    if a.content_type.is_empty()
        || a.content_type.len() > 32
        || !a
            .content_type
            .bytes()
            .all(|b| b.is_ascii_lowercase() || b == b'_')
    {
        return Err(CoreError::InvalidInput("unsupported content type".into()));
    }
    check_interval(a.check_interval_seconds)?;
    let range = if a.range.is_null() {
        json!({"kind": "from_now"})
    } else {
        a.range.clone()
    };
    crate::scan_range::Range::parse(&range, &jobs::now())?;
    let live = a.content_type == "live";
    if (a.capture_chat || a.capture_subtitles) && !live {
        return Err(CoreError::InvalidInput(
            "chat capture requires live subscription".into(),
        ));
    }
    let default_interval = if live {
        LIVE_DEFAULT_INTERVAL_SECONDS
    } else {
        crate::config::DEFAULT_CHECK_INTERVAL_SECONDS
    };
    Ok(Prepared {
        quality_json: serde_json::to_string(&a.quality)?,
        range,
        interval_seconds: a.check_interval_seconds.unwrap_or(default_interval),
    })
}

async fn create(core: &Arc<Core>, a: ctl::SubscriptionCreateArgs) -> Result<Value, CoreError> {
    let prepared = prepare(&a)?;
    let id = format!("sub-{}", uuid::Uuid::new_v4());
    let new_id = id.clone();
    let now = jobs::now();
    core.db
        .write(move |tx| {
            if let Some(credential) = &a.credential_id
                && !repo::credential_fits_account(tx, credential, &a.account_id)?
            {
                return Err(incompatible_credential());
            }
            require_lane(tx, &a.account_id, &a.content_type)?;
            let range_json = prepared.range.to_string();
            repo::insert(
                tx,
                &NewSubscription {
                    id: &new_id,
                    account_id: &a.account_id,
                    content_type: &a.content_type,
                    range_json: &range_json,
                    credential_id: a.credential_id.as_deref(),
                    interval_seconds: prepared.interval_seconds,
                    capture_chat: a.capture_chat,
                    capture_subtitles: a.capture_subtitles,
                    auto_download: a.auto_download,
                    quality_json: &prepared.quality_json,
                    now: &now,
                },
            )?;
            let from_now = prepared.range["kind"] == "from_now";
            for &lane in ScanMode::lanes(a.content_type == "live", from_now) {
                repo::insert_scan_lane(tx, &new_id, lane, &now)?;
            }
            Ok(())
        })
        .await?;
    Ok(json!({"subscription_id": id}))
}

/// The lane must be one the account's site offers: a declared subscription rule's lane, or
/// for the two sites that predate rule declarations the original lanes.
fn require_lane(tx: &Connection, account_id: &str, lane: &str) -> Result<(), CoreError> {
    let namespace: String = tx
        .query_row(
            "SELECT namespace FROM source_accounts WHERE account_id=?1",
            [account_id],
            |r| r.get(0),
        )
        .map_err(|_| CoreError::NotFound("account".into()))?;
    let site = namespace.split(':').next().unwrap_or_default();
    let declared = crate::catalog::lanes(tx, site)?;
    let legacy = matches!(site, "bilibili" | "douyin") || declared.is_empty();
    if declared.iter().any(|l| l == lane) || legacy && CONTENT_TYPES.contains(&lane) {
        Ok(())
    } else {
        Err(CoreError::InvalidInput("unsupported content type".into()))
    }
}

/// What one update asks for; only the present fields change.
struct Changes {
    credential_id: Option<String>,
    quality_json: Option<String>,
    range_json: Option<String>,
    enabled: Option<bool>,
    interval_seconds: Option<i64>,
    capture_chat: Option<bool>,
    capture_subtitles: Option<bool>,
    auto_download: Option<bool>,
}

fn changes_of(a: &ctl::SubscriptionUpdateArgs) -> Result<Changes, CoreError> {
    if let Some(quality) = &a.quality {
        crate::quality::validate_requirement(quality)?;
    }
    check_interval(a.check_interval_seconds)?;
    if let Some(range) = &a.range {
        crate::scan_range::Range::parse(range, &jobs::now())?;
    }
    Ok(Changes {
        credential_id: a.credential_id.clone(),
        quality_json: a.quality.as_ref().map(serde_json::to_string).transpose()?,
        range_json: a.range.as_ref().map(Value::to_string),
        enabled: a.enabled,
        interval_seconds: a.check_interval_seconds,
        capture_chat: a.capture_chat,
        capture_subtitles: a.capture_subtitles,
        auto_download: a.auto_download,
    })
}

/// An empty credential id clears the credential.
fn change_credential(tx: &Connection, id: &str, credential: &str) -> Result<(), CoreError> {
    if !credential.is_empty() && !repo::credential_fits_subscription(tx, credential, id)? {
        return Err(incompatible_credential());
    }
    let new = (!credential.is_empty()).then_some(credential);
    repo::set_credential(tx, id, new, &jobs::now())?;
    repo::requeue_work_after_credential_change(tx, id, &jobs::now())
}

/// A new range restarts scanning; history is scanned only when the range reaches into it.
fn change_range(tx: &Connection, id: &str, range_json: &str) -> Result<(), CoreError> {
    repo::set_range(tx, id, range_json, &jobs::now())?;
    if repo::wants_history(tx, id)? {
        repo::ensure_history_lane(tx, id, &jobs::now())
    } else {
        repo::cancel_history_lane(tx, id)
    }
}

fn require_live(tx: &Connection, id: &str, what: &str) -> Result<(), CoreError> {
    if !repo::is_live(tx, id)? {
        return Err(CoreError::InvalidInput(format!(
            "{what} capture requires live subscription"
        )));
    }
    Ok(())
}

fn apply(tx: &Transaction<'_>, id: &str, c: &Changes) -> Result<(), CoreError> {
    if let Some(credential) = &c.credential_id {
        change_credential(tx, id, credential)?;
    }
    if let Some(quality) = &c.quality_json {
        repo::set_quality(tx, id, quality, &jobs::now())?;
        repo::requeue_media_work(tx, id, &jobs::now())?;
    }
    if let Some(range) = &c.range_json {
        change_range(tx, id, range)?;
    }
    if let Some(enabled) = c.enabled {
        jobs::set_subscription_enabled(tx, id, enabled)?;
    }
    if let Some(seconds) = c.interval_seconds {
        repo::set_interval(tx, id, seconds, &jobs::now())?;
    }
    if let Some(enabled) = c.auto_download {
        repo::set_auto_download(tx, id, enabled, &jobs::now())?;
    }
    if let Some(capture) = c.capture_subtitles {
        require_live(tx, id, "subtitle")?;
        repo::set_capture_subtitles(tx, id, capture, &jobs::now())?;
    }
    if let Some(capture) = c.capture_chat {
        require_live(tx, id, "chat")?;
        repo::set_capture_chat(tx, id, capture, &jobs::now())?;
    }
    Ok(())
}

async fn update(
    core: &Arc<Core>,
    a: ctl::SubscriptionUpdateArgs,
    expected_revision: Option<i64>,
) -> Result<Value, CoreError> {
    let changes = changes_of(&a)?;
    let id = a.subscription_id;
    // Identity-relevant changes make the consumers re-evaluate the subscription's work.
    let reconfigures = changes.credential_id.is_some()
        || changes.quality_json.is_some()
        || changes.range_json.is_some();
    core.db
        .write(move |tx| {
            if let Some(expect) = expected_revision
                && repo::config_version(tx, &id)? != expect
            {
                return Err(CoreError::VersionConflict(format!("subscription {id}")));
            }
            apply(tx, &id, &changes)?;
            jobs::consumers::subscription_changed(tx, &id, reconfigures)?;
            if let Some(credential) = changes.credential_id.filter(|v| !v.is_empty()) {
                let generation = repo::credential_generation(tx, &credential)?;
                crate::upgrades::schedule(tx, &credential, generation)?;
            }
            Ok(())
        })
        .await?;
    Ok(json!({"ok": true}))
}

async fn check_now(core: &Arc<Core>, a: ctl::SubscriptionCheckNowArgs) -> Result<Value, CoreError> {
    let job_id = core
        .db
        .write(move |tx| repo::request_check_now(tx, &a.subscription_id, &jobs::now()))
        .await?;
    Ok(json!({"job_id": job_id}))
}

async fn list(core: &Arc<Core>, mut a: ctl::SubscriptionListArgs) -> Result<Value, CoreError> {
    a.creator_id = super::resolve_creator(core, a.creator_id).await?;
    let items = core
        .db
        .read(move |c| {
            store::subscriptions::list(c, a.creator_id.as_deref(), a.account_id.as_deref())
        })
        .await?;
    Ok(json!({ "items": items }))
}
