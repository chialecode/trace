//! Subscriptions: configuration rows and the scan jobs that follow them.
use crate::error::CoreError;
use rusqlite::{Connection, OptionalExtension};
use serde_json::{Value, json};
use trace_domain::jobs::{ScanMode, ScanPayload};

/// Everything a new subscription row stores.
pub struct NewSubscription<'a> {
    pub id: &'a str,
    pub account_id: &'a str,
    pub content_type: &'a str,
    pub range_json: &'a str,
    pub credential_id: Option<&'a str>,
    pub interval_seconds: i64,
    pub capture_chat: bool,
    pub capture_subtitles: bool,
    pub auto_download: bool,
    pub quality_json: &'a str,
    pub now: &'a str,
}

/// A valid credential of the account's platform may be attached to a new subscription.
pub fn credential_fits_account(
    c: &Connection,
    credential_id: &str,
    account_id: &str,
) -> Result<bool, CoreError> {
    Ok(c.query_row(
        "SELECT EXISTS(SELECT 1 FROM credentials_state c JOIN source_accounts a ON a.namespace=c.platform WHERE c.credential_id=?1 AND a.account_id=?2 AND c.status IN ('valid','authenticated'))",
        rusqlite::params![credential_id, account_id],
        |r| r.get(0),
    )?)
}

/// Same rule for an existing subscription, resolved through its account.
pub fn credential_fits_subscription(
    c: &Connection,
    credential_id: &str,
    subscription_id: &str,
) -> Result<bool, CoreError> {
    Ok(c.query_row(
        "SELECT EXISTS(SELECT 1 FROM credentials_state c JOIN source_accounts a ON a.namespace=c.platform JOIN subscriptions s ON s.account_id=a.account_id WHERE c.credential_id=?1 AND s.subscription_id=?2 AND c.status IN ('valid','authenticated'))",
        rusqlite::params![credential_id, subscription_id],
        |r| r.get(0),
    )?)
}

pub fn insert(c: &Connection, s: &NewSubscription) -> Result<(), CoreError> {
    c.execute(
        "INSERT INTO subscriptions (subscription_id, account_id, content_type, range_json,
         credential_id, config_version, state, check_interval_seconds, created_at, updated_at)
         VALUES (?1,?2,?3,?4,?5,1,'enabled',?6,?7,?7)",
        rusqlite::params![
            s.id,
            s.account_id,
            s.content_type,
            s.range_json,
            s.credential_id,
            s.interval_seconds,
            s.now
        ],
    )?;
    c.execute(
        "UPDATE subscriptions SET capture_chat=?2,capture_subtitles=?3,quality_json=?4,auto_download=?5 WHERE subscription_id=?1",
        rusqlite::params![
            s.id,
            s.capture_chat,
            s.capture_subtitles,
            s.quality_json,
            s.auto_download
        ],
    )?;
    Ok(())
}

/// Creates the recurring scan job of one lane. Deep checks start a week from now.
pub fn insert_scan_lane(
    c: &Connection,
    subscription_id: &str,
    lane: ScanMode,
    now: &str,
) -> Result<(), CoreError> {
    c.execute(
        "INSERT INTO jobs(job_id,job_key,job_type,subject_id,payload,priority,state,created_at,updated_at) VALUES(?1,?2,'scan',?3,?4,?5,'queued',?6,?6)",
        rusqlite::params![
            format!("job-{}", uuid::Uuid::new_v4()),
            lane.job_key(subscription_id),
            subscription_id,
            ScanPayload::new(subscription_id, lane).to_json_text(),
            lane.priority(),
            now
        ],
    )?;
    if lane == ScanMode::Deep {
        c.execute(
            "UPDATE jobs SET next_run_at=strftime('%Y-%m-%dT%H:%M:%SZ','now','+7 days') WHERE job_key=?1",
            [ScanMode::Deep.job_key(subscription_id)],
        )?;
    }
    Ok(())
}

pub fn config_version(c: &Connection, id: &str) -> Result<i64, CoreError> {
    Ok(c.query_row(
        "SELECT config_version FROM subscriptions WHERE subscription_id=?1",
        [id],
        |r| r.get(0),
    )?)
}

/// Replaces (or clears, with `None`) the credential and bumps the configuration version.
pub fn set_credential(
    c: &Connection,
    id: &str,
    credential_id: Option<&str>,
    now: &str,
) -> Result<(), CoreError> {
    c.execute(
        "UPDATE subscriptions SET credential_id=?2,config_version=config_version+1,updated_at=?3 WHERE subscription_id=?1",
        rusqlite::params![id, credential_id, now],
    )?;
    Ok(())
}

/// A credential change restarts all work of the subscription under the new generation.
pub fn requeue_work_after_credential_change(
    c: &Connection,
    id: &str,
    now: &str,
) -> Result<(), CoreError> {
    c.execute(
        "UPDATE jobs SET state=CASE WHEN state='paused' THEN 'paused' ELSE 'queued' END,target_generation=target_generation+1,lease_owner=NULL,lease_expires_at=NULL,next_run_at=?2,updated_at=?2 WHERE json_extract(payload,'$.subscription_id')=?1 AND state NOT IN ('cancelled','waiting_user') AND job_type IN ('scan','observe','media','record','account_profile','live_auxiliary') AND NOT EXISTS(SELECT 1 FROM job_consumers w WHERE w.subscription_id=?1 AND w.requested>w.completed AND w.state='waiting_user' AND json_extract(w.last_error,'$.class')='permission_denied') AND NOT EXISTS(SELECT 1 FROM jobs p WHERE json_extract(p.last_error,'$.class')='permission_denied' AND json_extract(p.payload,'$.subscription_id')=?1)",
        rusqlite::params![id, now],
    )?;
    Ok(())
}

pub fn set_quality(
    c: &Connection,
    id: &str,
    quality_json: &str,
    now: &str,
) -> Result<(), CoreError> {
    c.execute(
        "UPDATE subscriptions SET quality_json=?2,config_version=config_version+1,updated_at=?3 WHERE subscription_id=?1",
        rusqlite::params![id, quality_json, now],
    )?;
    Ok(())
}

/// A quality change re-evaluates media and recordings only.
pub fn requeue_media_work(c: &Connection, id: &str, now: &str) -> Result<(), CoreError> {
    c.execute(
        "UPDATE jobs SET state=CASE WHEN state='paused' THEN 'paused' ELSE 'queued' END,target_generation=target_generation+1,lease_owner=NULL,lease_expires_at=NULL,next_run_at=?2 WHERE json_extract(payload,'$.subscription_id')=?1 AND job_type IN ('media','record') AND state NOT IN ('cancelled','waiting_user') AND NOT EXISTS(SELECT 1 FROM job_consumers w WHERE w.subscription_id=?1 AND w.requested>w.completed AND w.state='waiting_user' AND json_extract(w.last_error,'$.class')='permission_denied') AND NOT EXISTS(SELECT 1 FROM jobs p WHERE json_extract(p.last_error,'$.class')='permission_denied' AND json_extract(p.payload,'$.subscription_id')=?1)",
        rusqlite::params![id, now],
    )?;
    Ok(())
}

/// A new range discards the old scan progress and restarts every scan lane.
pub fn set_range(c: &Connection, id: &str, range_json: &str, now: &str) -> Result<(), CoreError> {
    c.execute(
        "UPDATE subscriptions SET range_json=?2, config_version=config_version+1, updated_at=?3 WHERE subscription_id=?1",
        rusqlite::params![id, range_json, now],
    )?;
    c.execute(
        "DELETE FROM scan_checkpoints WHERE subscription_id=?1",
        [id],
    )?;
    c.execute(
        "DELETE FROM scan_cursor_visits WHERE subscription_id=?1",
        [id],
    )?;
    if crate::jobs::consumers::subscription_refused(c, id)? {
        return Ok(());
    }
    c.execute(
        "UPDATE jobs SET target_generation=target_generation+1,lease_owner=NULL,lease_expires_at=NULL,state='queued',next_run_at=?2,payload=json_remove(payload,'$.cursor') WHERE job_type='scan' AND subject_id=?1 AND state!='waiting_user'",
        rusqlite::params![id, now],
    )?;
    Ok(())
}

/// Whether the (new) range of a non-live subscription reaches into history.
pub fn wants_history(c: &Connection, id: &str) -> Result<bool, CoreError> {
    Ok(c.query_row(
        "SELECT content_type!='live' AND json_extract(range_json,'$.kind')!='from_now' FROM subscriptions WHERE subscription_id=?1",
        [id],
        |r| r.get(0),
    )?)
}

pub fn ensure_history_lane(c: &Connection, id: &str, now: &str) -> Result<(), CoreError> {
    if crate::jobs::consumers::subscription_refused(c, id)? {
        return Ok(());
    }
    c.execute(
        "INSERT OR IGNORE INTO jobs(job_id,job_key,job_type,subject_id,payload,priority,state,created_at,updated_at) VALUES(?1,?2,'scan',?3,?4,2,'queued',?5,?5)",
        rusqlite::params![
            format!("job-{}", uuid::Uuid::new_v4()),
            ScanMode::History.job_key(id),
            id,
            ScanPayload::new(id, ScanMode::History).to_json_text(),
            now
        ],
    )?;
    Ok(())
}

pub fn cancel_history_lane(c: &Connection, id: &str) -> Result<(), CoreError> {
    c.execute(
        "UPDATE jobs SET state='cancelled' WHERE job_key=?1 AND state!='waiting_user'",
        [ScanMode::History.job_key(id)],
    )?;
    Ok(())
}

pub fn set_interval(c: &Connection, id: &str, seconds: i64, now: &str) -> Result<(), CoreError> {
    c.execute(
        "UPDATE subscriptions SET check_interval_seconds=?2, config_version=config_version+1, updated_at=?3 WHERE subscription_id=?1",
        rusqlite::params![id, seconds, now],
    )?;
    Ok(())
}

pub fn is_live(c: &Connection, id: &str) -> Result<bool, CoreError> {
    Ok(c.query_row(
        "SELECT content_type='live' FROM subscriptions WHERE subscription_id=?1",
        [id],
        |r| r.get(0),
    )?)
}

pub fn set_capture_subtitles(
    c: &Connection,
    id: &str,
    capture: bool,
    now: &str,
) -> Result<(), CoreError> {
    c.execute(
        "UPDATE subscriptions SET capture_subtitles=?2,config_version=config_version+1,updated_at=?3 WHERE subscription_id=?1",
        rusqlite::params![id, capture, now],
    )?;
    Ok(())
}

/// Chat capture also starts or pauses the live auxiliary jobs of the subscription.
pub fn set_capture_chat(
    c: &Connection,
    id: &str,
    capture: bool,
    now: &str,
) -> Result<(), CoreError> {
    c.execute(
        "UPDATE subscriptions SET capture_chat=?2,config_version=config_version+1,updated_at=?3 WHERE subscription_id=?1",
        rusqlite::params![id, capture, now],
    )?;
    c.execute(
        "UPDATE jobs SET state=?2,target_generation=target_generation+1,lease_owner=NULL,lease_expires_at=NULL,next_run_at=?3 WHERE job_type='live_auxiliary' AND json_extract(payload,'$.subscription_id')=?1 AND state!='cancelled'",
        rusqlite::params![id, if capture { "queued" } else { "paused" }, now],
    )?;
    Ok(())
}

/// Changes only what later discoveries do; saved and undownloaded media stay as they are.
pub fn set_auto_download(
    c: &Connection,
    id: &str,
    enabled: bool,
    now: &str,
) -> Result<(), CoreError> {
    c.execute(
        "UPDATE subscriptions SET auto_download=?2,config_version=config_version+1,updated_at=?3 WHERE subscription_id=?1",
        rusqlite::params![id, enabled, now],
    )?;
    Ok(())
}

/// Whether a subscription downloads the videos it discovers; `None` for an unknown id.
pub fn auto_download(c: &Connection, id: &str) -> Result<Option<bool>, CoreError> {
    Ok(c.query_row(
        "SELECT auto_download FROM subscriptions WHERE subscription_id=?1",
        [id],
        |r| r.get(0),
    )
    .optional()?)
}

pub fn credential_generation(c: &Connection, credential_id: &str) -> Result<i64, CoreError> {
    Ok(c.query_row(
        "SELECT generation FROM credentials_state WHERE credential_id=?1",
        [credential_id],
        |r| r.get(0),
    )?)
}

/// Queues an immediate incremental scan; returns the job id. A running or paused scan is left alone.
pub fn request_check_now(c: &Connection, id: &str, now: &str) -> Result<String, CoreError> {
    if crate::jobs::consumers::subscription_refused(c, id)? {
        return Err(CoreError::VersionConflict(
            "the subscription was refused; explicitly retry the stopped job first".into(),
        ));
    }
    let key = ScanMode::Incremental.job_key(id);
    c.execute(
        "INSERT INTO jobs(job_id,job_key,job_type,subject_id,payload,priority,state,created_at,updated_at) VALUES(?1,?2,'scan',?3,?4,0,'queued',?5,?5) ON CONFLICT(job_key) DO UPDATE SET next_run_at=excluded.updated_at,state='queued' WHERE jobs.state NOT IN ('running','leased','paused','cancelled','waiting_user')",
        rusqlite::params![
            format!("job-{}", uuid::Uuid::new_v4()),
            key,
            id,
            ScanPayload::new(id, ScanMode::Incremental).to_json_text(),
            now
        ],
    )?;
    Ok(
        c.query_row("SELECT job_id FROM jobs WHERE job_key=?1", [key], |r| {
            r.get::<_, String>(0)
        })?,
    )
}

/// Subscriptions of a creator and/or account with schedule and coverage.
pub fn list(
    c: &Connection,
    creator_id: Option<&str>,
    account_id: Option<&str>,
) -> Result<Vec<Value>, CoreError> {
    let mut stmt = c.prepare(
        "SELECT s.subscription_id, s.account_id, s.content_type, s.range_json, s.state,
                s.config_version, s.credential_id, s.updated_at, s.capture_chat, s.capture_subtitles,s.quality_json,s.check_interval_seconds,
                (SELECT MIN(j.next_run_at) FROM jobs j WHERE j.subject_id=s.subscription_id AND j.job_type='scan' AND j.state NOT IN ('cancelled','paused')),
                (SELECT json_group_array(json_object('scan_type',p.scan_type,'coverage',json(p.coverage))) FROM scan_checkpoints p WHERE p.subscription_id=s.subscription_id),
                s.auto_download
         FROM subscriptions s
         WHERE s.acquisition_kind='subscription' AND (?1 IS NULL OR EXISTS (
            SELECT 1 FROM account_bindings b WHERE b.account_id=s.account_id AND b.creator_id=?1)) AND (?2 IS NULL OR s.account_id=?2)",
    )?;
    let rows = stmt
        .query_map(rusqlite::params![creator_id, account_id], |r| {
            Ok(json!({
                "subscription_id": r.get::<_, String>(0)?,
                "account_id": r.get::<_, String>(1)?,
                "content_type": r.get::<_, String>(2)?,
                "range": super::json_column(r.get::<_, String>(3)),
                "state": r.get::<_, String>(4)?,
                "config_version": r.get::<_, i64>(5)?,
                "credential_id": r.get::<_, Option<String>>(6)?,
                "updated_at": r.get::<_, String>(7)?,
                "capture_chat": r.get::<_, bool>(8)?,
                "capture_subtitles": r.get::<_, bool>(9)?,
                "check_interval_seconds": r.get::<_, i64>(11)?,
                "next_check_at": r.get::<_, Option<String>>(12)?,
                "coverage": super::json_column(r.get::<_, String>(13)),
                "quality": super::json_column(r.get::<_, String>(10)),
                "auto_download": r.get::<_, bool>(14)?,
            }))
        })?
        .collect::<Result<Vec<_>, _>>()?;
    Ok(rows)
}
