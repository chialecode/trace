//! What a subscription scan reads and writes besides the entries it finds.
use crate::error::CoreError;
use rusqlite::Connection;

/// The subscription row a scan works from, joined with its account.
pub struct Target {
    pub account_key: String,
    pub namespace: String,
    pub state: String,
    pub content_type: String,
    pub range_json: String,
    pub created_at: String,
}

pub fn target(c: &Connection, subscription_id: &str) -> Result<Target, CoreError> {
    Ok(c.query_row(
        "SELECT a.native_account_key, a.namespace, s.state, s.content_type, s.range_json, s.created_at
         FROM subscriptions s JOIN source_accounts a ON a.account_id=s.account_id
         WHERE s.subscription_id=?1",
        [subscription_id],
        |r| {
            Ok(Target {
                account_key: r.get(0)?,
                namespace: r.get(1)?,
                state: r.get(2)?,
                content_type: r.get(3)?,
                range_json: r.get(4)?,
                created_at: r.get(5)?,
            })
        },
    )?)
}

/// The stored resume point of one scan type, if any.
pub fn checkpoint_cursor(c: &Connection, subscription_id: &str, scan_type: &str) -> Option<String> {
    c.query_row(
        "SELECT cursor FROM scan_checkpoints WHERE subscription_id=?1 AND scan_type=?2",
        rusqlite::params![subscription_id, scan_type],
        |r| r.get(0),
    )
    .ok()
}

/// A history scan has started once its checkpoint exists.
pub fn history_started(c: &Connection, subscription_id: &str) -> Result<bool, CoreError> {
    Ok(c.query_row(
        "SELECT EXISTS(SELECT 1 FROM scan_checkpoints WHERE subscription_id=?1 AND scan_type='history')",
        [subscription_id],
        |r| r.get(0),
    )?)
}

/// Seconds between checks; the default applies when the subscription is gone.
pub fn check_interval_seconds(c: &Connection, subscription_id: &str) -> i64 {
    c.query_row(
        "SELECT check_interval_seconds FROM subscriptions WHERE subscription_id=?1",
        [subscription_id],
        |r| r.get::<_, i64>(0),
    )
    .unwrap_or(crate::config::DEFAULT_CHECK_INTERVAL_SECONDS)
}

/// A scan without its plugin waits instead of failing (C19).
pub fn wait_for_plugin(c: &Connection, job_id: &str, now: &str) -> Result<(), CoreError> {
    c.execute(
        "UPDATE jobs SET state='waiting_plugin', updated_at=?2 WHERE job_id=?1",
        rusqlite::params![job_id, now],
    )?;
    Ok(())
}

/// Queues the next page or round of the single active scan job and remembers its cursor.
pub fn schedule_next_run(
    c: &Connection,
    job_id: &str,
    next_run_at: &str,
    cursor: Option<&str>,
) -> Result<(), CoreError> {
    c.execute(
        "UPDATE jobs SET state='queued', next_run_at=?2, updated_at=?2,
         payload=json_set(payload, '$.cursor', ?3)
         WHERE job_id=?1",
        rusqlite::params![job_id, next_run_at, cursor],
    )?;
    Ok(())
}

/// Saves the page cursor and coverage of a scan type with the page's entries (C01).
pub fn save_checkpoint(
    c: &Connection,
    subscription_id: &str,
    scan_type: &str,
    cursor: Option<&str>,
    coverage_json: &str,
    now: &str,
) -> Result<(), CoreError> {
    c.execute(
        "INSERT INTO scan_checkpoints (subscription_id, scan_type, cursor, coverage, generation, updated_at)
         VALUES (?1,?2,?3,?4,1,?5)
         ON CONFLICT(subscription_id, scan_type) DO UPDATE SET cursor=excluded.cursor, coverage=excluded.coverage, updated_at=excluded.updated_at",
        rusqlite::params![subscription_id, scan_type, cursor, coverage_json, now],
    )?;
    Ok(())
}

/// Keeps the cursor in the job payload so a restarted job resumes at the same page.
pub fn remember_cursor(
    c: &Connection,
    job_id: &str,
    cursor: Option<&str>,
) -> Result<(), CoreError> {
    c.execute(
        "UPDATE jobs SET payload=json_set(payload,'$.cursor',?2) WHERE job_id=?1",
        rusqlite::params![job_id, cursor],
    )?;
    Ok(())
}
