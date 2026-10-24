//! Fetching videos a subscription left undownloaded, and observing entries again without
//! touching the subscription's scan position.
use crate::core::Core;
use crate::error::CoreError;
use crate::{jobs, store};
use rusqlite::Transaction;
use serde_json::json;
use time::format_description::well_known::Rfc3339;
use time::{Date, Duration, OffsetDateTime};
use trace_contracts::media_sync::{
    MAX_ENTRIES, MediaSyncItem, MediaSyncReport, RecheckArgs, RecheckItem, RecheckReport,
};

const DEFAULT_LIMIT: u32 = 100;

fn item(entry: &str, state: &str, groups: i64, message: Option<&str>) -> MediaSyncItem {
    MediaSyncItem {
        entry_id: entry.to_owned(),
        state: state.into(),
        groups,
        message: message.map(str::to_owned),
    }
}

fn user_message(e: &CoreError) -> String {
    match e {
        CoreError::InvalidInput(m)
        | CoreError::NotFound(m)
        | CoreError::VersionConflict(m)
        | CoreError::Unsupported(m) => m.clone(),
        _ => "内部错误，详见日志".into(),
    }
}

/// Queues the undownloaded videos of one entry and retries the media work that failed.
/// Every media group is one job whatever the number of requests, so selecting the same entry
/// again queues nothing new.
fn sync_entry(tx: &Transaction<'_>, entry: &str) -> Result<MediaSyncItem, CoreError> {
    let deferred = store::media_sync::deferred_groups(tx, entry)?;
    let failed = store::media_sync::failed_media_jobs(tx, entry)?;
    if deferred.is_empty() && failed.is_empty() {
        return Ok(if store::media_sync::active_media_jobs(tx, entry)? > 0 {
            item(entry, "already_queued", 0, None)
        } else {
            item(entry, "nothing", 0, Some("没有按订阅设置未下载的视频"))
        });
    }
    let mut queued = 0;
    if !deferred.is_empty() {
        // The subscription that found the entry also brings its credential and quality choice.
        let (_, _, _, subscription) = store::archive::recheck_target(tx, entry).map_err(|_| {
            CoreError::InvalidInput("没有可用的订阅来取回视频（订阅已停用或内容已排除）".into())
        })?;
        for group in &deferred {
            store::media_sync::mark_wanted(tx, &group.revision_id, &group.group)?;
            let payload = json!({
                "entry_id": entry,
                "revision_id": group.revision_id,
                "group": group.group,
                "account_id": group.account_id,
                "subscription_id": subscription,
            });
            jobs::consumers::discover(tx, entry, &subscription)?;
            jobs::consumers::request(
                tx,
                &format!("media:{}:{}", group.revision_id, group.group),
                "media",
                entry,
                payload,
                &subscription,
            )?;
            queued += 1;
        }
    }
    for job in failed {
        if store::jobs::retry_stopped(tx, &job, &jobs::now())? > 0 {
            jobs::consumers::retry(tx, &job)?;
            queued += 1;
        }
    }
    Ok(item(entry, "queued", queued, None))
}

/// Queues the undownloaded videos of the given entries (up to 200 per request).
pub async fn sync(core: &Core, entry_ids: &[String]) -> Result<MediaSyncReport, CoreError> {
    if entry_ids.is_empty() || entry_ids.len() > MAX_ENTRIES {
        return Err(CoreError::InvalidInput(format!(
            "一次请求需要 1 到 {MAX_ENTRIES} 个条目"
        )));
    }
    let mut seen = std::collections::HashSet::new();
    let mut items = Vec::new();
    for id in entry_ids.iter().filter(|id| seen.insert(id.as_str())) {
        let entry = id.clone();
        let result = core.db.write(move |tx| sync_entry(tx, &entry)).await;
        items.push(match result {
            Ok(item) => item,
            Err(e) => item(id, "failed", 0, Some(&user_message(&e))),
        });
    }
    Ok(MediaSyncReport { items })
}

/// Asks the subscription that covers the entry to observe it again. This is an observation
/// of one entry: the scan position of the subscription is not read or written.
pub(super) fn recheck_entry(tx: &Transaction<'_>, entry: &str) -> Result<String, CoreError> {
    let (namespace, kind, key, subscription) = store::archive::recheck_target(tx, entry)?;
    let job_key = format!("observe:{namespace}:{kind}:{key}");
    jobs::consumers::discover(tx, entry, &subscription)?;
    let payload = json!({"entry_id": entry, "subscription_id": subscription});
    let id = jobs::consumers::request(tx, &job_key, "observe", entry, payload, &subscription)?;
    let state = store::jobs::state_of(tx, &id)?;
    if state == "cancelled" {
        return Err(CoreError::VersionConflict(
            "explicitly cancelled work needs an explicit job retry".into(),
        ));
    }
    if !matches!(state.as_str(), "running" | "leased") {
        jobs::consumers::retry(tx, &id)?;
    }
    Ok(id)
}

/// A publication-time bound as an RFC 3339 instant. A date alone starts its day; as the upper
/// bound it includes that whole day.
fn bound(text: &str, upper: bool) -> Result<String, CoreError> {
    let bad = || CoreError::InvalidInput("时间格式应为 YYYY-MM-DD 或 RFC 3339".into());
    let instant = if let Ok(t) = OffsetDateTime::parse(text, &Rfc3339) {
        t
    } else {
        let day = Date::parse(
            text,
            time::macros::format_description!("[year]-[month]-[day]"),
        )
        .map_err(|_| bad())?;
        let start = day.midnight().assume_utc();
        if upper {
            start + Duration::days(1)
        } else {
            start
        }
    };
    instant.format(&Rfc3339).map_err(|_| bad())
}

enum Mode {
    Entries(Vec<String>),
    Range {
        account: Option<String>,
        from: Option<String>,
        to: Option<String>,
    },
    Failed(Option<String>),
}

fn mode_of(a: &RecheckArgs) -> Result<Mode, CoreError> {
    let range = a.from.is_some() || a.to.is_some();
    let chosen = [!a.entry_ids.is_empty(), range, a.failed]
        .iter()
        .filter(|m| **m)
        .count();
    if chosen != 1 {
        return Err(CoreError::InvalidInput(
            "补查需要且只能指定：条目列表、时间段或失败项目".into(),
        ));
    }
    if !a.entry_ids.is_empty() {
        if a.entry_ids.len() > MAX_ENTRIES {
            return Err(CoreError::InvalidInput(format!(
                "一次最多补查 {MAX_ENTRIES} 个条目"
            )));
        }
        return Ok(Mode::Entries(a.entry_ids.clone()));
    }
    if a.failed {
        return Ok(Mode::Failed(a.account_id.clone()));
    }
    Ok(Mode::Range {
        account: a.account_id.clone(),
        from: a.from.as_deref().map(|t| bound(t, false)).transpose()?,
        to: a.to.as_deref().map(|t| bound(t, true)).transpose()?,
    })
}

async fn recheck_one(core: &Core, entry: String) -> RecheckItem {
    let id = entry.clone();
    match core.db.write(move |tx| recheck_entry(tx, &id)).await {
        Ok(_) => RecheckItem {
            entry_id: entry,
            state: "queued".into(),
            message: None,
        },
        Err(e) => RecheckItem {
            entry_id: entry,
            state: "skipped".into(),
            message: Some(match e {
                CoreError::Db(_) => "没有可用的订阅来补查这条内容".into(),
                other => user_message(&other),
            }),
        },
    }
}

/// Observes entries again — listed, published in a period, or whose work failed. The scan
/// position of every subscription stays where it is.
pub async fn recheck(core: &Core, args: RecheckArgs) -> Result<RecheckReport, CoreError> {
    let mode = mode_of(&args)?;
    let limit = args
        .limit
        .unwrap_or(DEFAULT_LIMIT)
        .clamp(1, MAX_ENTRIES as u32);
    let (mut truncated, mut items) = (false, Vec::new());
    match mode {
        Mode::Entries(ids) => {
            let mut seen = std::collections::HashSet::new();
            for id in ids.into_iter().filter(|id| seen.insert(id.clone())) {
                items.push(recheck_one(core, id).await);
            }
        }
        Mode::Range { account, from, to } => {
            let cap = i64::from(limit);
            let mut ids = core
                .db
                .read(move |c| {
                    store::media_sync::entries_in_range(
                        c,
                        account.as_deref(),
                        from.as_deref(),
                        to.as_deref(),
                        cap,
                    )
                })
                .await?;
            truncated = ids.len() as i64 > cap;
            ids.truncate(limit as usize);
            for id in ids {
                items.push(recheck_one(core, id).await);
            }
        }
        Mode::Failed(account) => {
            let cap = i64::from(limit);
            let mut failed = core
                .db
                .read(move |c| store::media_sync::failed_jobs(c, account.as_deref(), cap))
                .await?;
            truncated = failed.len() as i64 > cap;
            failed.truncate(limit as usize);
            for (job, entry) in failed {
                let retried = core
                    .db
                    .write(move |tx| {
                        let n = store::jobs::retry_stopped(tx, &job, &jobs::now())?;
                        if n > 0 {
                            jobs::consumers::retry(tx, &job)?;
                        }
                        Ok(n)
                    })
                    .await?;
                items.push(RecheckItem {
                    entry_id: entry,
                    state: if retried > 0 { "retried" } else { "skipped" }.into(),
                    message: None,
                });
            }
        }
    }
    let queued = items.iter().filter(|i| i.state != "skipped").count() as i64;
    Ok(RecheckReport {
        queued,
        skipped: items.len() as i64 - queued,
        truncated,
        items,
    })
}
