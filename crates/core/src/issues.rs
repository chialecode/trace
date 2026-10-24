//! Durable, coalesced health issues; the daemon owns delivery independently of GUI.
use crate::{core::Core, error::CoreError, jobs::ClaimedJob};
use rusqlite::{OptionalExtension, Transaction};
use serde_json::{Value, json};
use trace_domain::errors::FailureClass;

fn notify(tx: &Transaction<'_>, key: &str, event: &str) -> Result<(), CoreError> {
    tx.execute("INSERT INTO issue_notifications(issue_key,event,updated_at) VALUES(?1,?2,?3) ON CONFLICT(issue_key) DO UPDATE SET event=excluded.event,state='pending',attempts=0,last_error=NULL,updated_at=excluded.updated_at,version=issue_notifications.version+1",rusqlite::params![key,event,crate::jobs::now()])?;
    crate::objects::outbox_emit(tx, "health.changed", key, &json!({"event":event}))?;
    Ok(())
}

const SELECT_SUBSCRIPTIONS: &str = "SELECT a.namespace,s.credential_id FROM subscriptions s JOIN source_accounts a ON a.account_id=s.account_id WHERE s.subscription_id=?1";

pub fn record(
    tx: &Transaction<'_>,
    job: &ClaimedJob,
    class: FailureClass,
) -> Result<(), CoreError> {
    if !matches!(
        class,
        FailureClass::AuthRequired
            | FailureClass::PermissionDenied
            | FailureClass::CapabilityLost
            | FailureClass::StorageFull
    ) {
        return Ok(());
    }
    let sub = job.payload["subscription_id"].as_str();
    let (platform, credential): (String, Option<String>) = tx
        .query_row(SELECT_SUBSCRIPTIONS, [sub], |r| Ok((r.get(0)?, r.get(1)?)))
        .optional()?
        .unwrap_or((
            job.payload["platform"]
                .as_str()
                .unwrap_or("local")
                .to_owned(),
            job.payload["credential_id"].as_str().map(str::to_owned),
        ));
    let category = serde_json::to_value(class)?
        .as_str()
        .unwrap_or("unknown")
        .to_owned();
    let key = format!(
        "{category}:{platform}:{}",
        credential.as_deref().unwrap_or("guest")
    );
    let active: bool = tx.query_row(
        "SELECT EXISTS(SELECT 1 FROM issues WHERE issue_key=?1 AND resolved_at IS NULL)",
        [&key],
        |r| r.get(0),
    )?;
    let before: i64 = tx.query_row(
        "SELECT count(*) FROM issue_links WHERE issue_key=?1 AND active=1",
        [&key],
        |r| r.get(0),
    )?;
    let detail = json!({"class":class,"platform":platform,"credential_id":credential});
    tx.execute("INSERT INTO issues(issue_key,category,severity,detail,user_action,first_seen_at,last_seen_at) VALUES(?1,?2,'warning',?3,?4,?5,?5) ON CONFLICT(issue_key) DO UPDATE SET detail=excluded.detail,last_seen_at=excluded.last_seen_at,resolved_at=NULL",rusqlite::params![key,category,detail.to_string(),if class==FailureClass::StorageFull{"storage"}else{"accounts"},crate::jobs::now()])?;
    tx.execute("INSERT INTO issue_links(issue_key,job_id,subscription_id) VALUES(?1,?2,?3) ON CONFLICT(issue_key,job_id) DO UPDATE SET active=1",rusqlite::params![key,job.job_id,sub])?;
    let after: i64 = tx.query_row(
        "SELECT count(*) FROM issue_links WHERE issue_key=?1 AND active=1",
        [&key],
        |r| r.get(0),
    )?;
    // Notify first appearance/recovery recurrence or a doubling of impact.
    if !active || after > before && after >= 2 && (after as u64).is_power_of_two() {
        notify(tx, &key, if active { "expanded" } else { "opened" })?;
    }
    Ok(())
}

pub fn resolved_job(tx: &Transaction<'_>, job: &str) -> Result<(), CoreError> {
    let keys = tx
        .prepare("SELECT issue_key FROM issue_links WHERE job_id=?1 AND active=1")?
        .query_map([job], |r| r.get::<_, String>(0))?
        .collect::<Result<Vec<_>, _>>()?;
    tx.execute("UPDATE issue_links SET active=0 WHERE job_id=?1", [job])?;
    for key in keys {
        let active: bool = tx.query_row(
            "SELECT EXISTS(SELECT 1 FROM issue_links WHERE issue_key=?1 AND active=1)",
            [&key],
            |r| r.get(0),
        )?;
        if !active {
            tx.execute(
                "UPDATE issues SET resolved_at=?2,last_seen_at=?2 WHERE issue_key=?1",
                rusqlite::params![key, crate::jobs::now()],
            )?;
            notify(tx, &key, "resolved")?;
        }
    }
    Ok(())
}

const SELECT_ISSUE_LINKS: &str = "SELECT i.issue_key,i.category,i.detail,i.first_seen_at,i.last_seen_at,i.resolved_at,n.state,n.last_error,(SELECT count(*) FROM issue_links l WHERE l.issue_key=i.issue_key AND active=1) FROM issues i LEFT JOIN issue_notifications n ON n.issue_key=i.issue_key ORDER BY (i.resolved_at IS NULL) DESC,i.last_seen_at DESC LIMIT 100";

pub async fn listing(core: &Core) -> Result<Value, CoreError> {
    core.db
        .read(|c| {
            let items = c
                .prepare(SELECT_ISSUE_LINKS)?
                .query_map([], |r| {
                    Ok(json!({
                        "issue_key": r.get::<_, String>(0)?,
                        "category": r.get::<_, String>(1)?,
                        "detail": r.get::<_, String>(2)?.parse::<Value>().unwrap_or_default(),
                        "first_seen_at": r.get::<_, String>(3)?,
                        "last_seen_at": r.get::<_, String>(4)?,
                        "resolved_at": r.get::<_, Option<String>>(5)?,
                        "notification_state": r.get::<_, Option<String>>(6)?,
                        "notification_error": r.get::<_, Option<String>>(7)?,
                        "affected_jobs": r.get::<_, i64>(8)?,
                    }))
                })?
                .collect::<Result<Vec<_>, _>>()?;
            Ok(json!({"items":items}))
        })
        .await
}
const SELECT_ISSUE_LINKS_2: &str = "SELECT l.job_id,l.subscription_id,l.active,j.state FROM issue_links l LEFT JOIN jobs j ON j.job_id=l.job_id WHERE l.issue_key=?1 AND l.job_id>?2 ORDER BY l.job_id LIMIT 101";

pub async fn refs(core: &Core, key: String, after: String) -> Result<Value, CoreError> {
    core.db
        .read(move |c| {
            let mut items = c
                .prepare(SELECT_ISSUE_LINKS_2)?
                .query_map(rusqlite::params![key, after], |r| {
                    Ok(json!({
                        "job_id": r.get::<_, String>(0)?,
                        "subscription_id": r.get::<_, Option<String>>(1)?,
                        "active": r.get::<_, bool>(2)?,
                        "state": r.get::<_, Option<String>>(3)?,
                    }))
                })?
                .collect::<Result<Vec<_>, _>>()?;
            let more = items.len() > 100;
            items.truncate(100);
            let next = if more {
                items.last().map(|v| v["job_id"].clone())
            } else {
                None
            };
            Ok(json!({"items":items,"next":next}))
        })
        .await
}

const SELECT_ISSUE_NOTIFICATIONS: &str = "SELECT issue_key,event,version FROM issue_notifications WHERE state='pending' ORDER BY updated_at LIMIT 4";
const UPDATE_ISSUE_NOTIFICATIONS: &str = "UPDATE issue_notifications SET state=?2,attempts=attempts+1,last_error=?3 WHERE issue_key=?1 AND version=?4";

/// Delivery failure is retained and retried on a later explicit retry/new event,
/// avoiding repeated OS prompts for an unchanged problem.
pub async fn deliver_pending(core: &Core) -> Result<(), CoreError> {
    let rows = core
        .db
        .read(|c| {
            Ok(c.prepare(SELECT_ISSUE_NOTIFICATIONS)?
                .query_map([], |r| {
                    Ok((
                        r.get::<_, String>(0)?,
                        r.get::<_, String>(1)?,
                        r.get::<_, i64>(2)?,
                    ))
                })?
                .collect::<Result<Vec<_>, _>>()?)
        })
        .await?;
    for (key, event, version) in rows {
        let profile = core.profile.clone();
        let delivered = tokio::task::spawn_blocking(move || {
            crate::windows_integration::notify(&profile, &event)
        })
        .await
        .map_err(|_| CoreError::Internal("notification worker".into()))?;
        core.db
            .write(move |tx| {
                tx.execute(
                    UPDATE_ISSUE_NOTIFICATIONS,
                    rusqlite::params![
                        key,
                        if delivered.is_ok() {
                            "submitted"
                        } else {
                            "failed"
                        },
                        delivered.err().map(|_| "system_notification_unavailable"),
                        version
                    ],
                )?;
                Ok(())
            })
            .await?;
    }
    Ok(())
}
