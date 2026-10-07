//! Stable work is shared; pending requests, retry budgets and scopes are not.
use crate::{
    error::CoreError,
    jobs::{self, ClaimedJob},
};
use rusqlite::{Connection, OptionalExtension, Transaction};
use serde_json::Value;

pub fn discover(tx: &Transaction<'_>, entry: &str, subscription: &str) -> Result<(), CoreError> {
    tx.execute("INSERT INTO entry_subscriptions VALUES(?1,?2,?3,?3) ON CONFLICT(entry_id,subscription_id) DO UPDATE SET last_discovered_at=excluded.last_discovered_at",rusqlite::params![entry,subscription,jobs::now()])?;
    Ok(())
}

pub fn request(
    tx: &Transaction<'_>,
    key: &str,
    kind: &str,
    entry: &str,
    payload: Value,
    subscription: &str,
) -> Result<String, CoreError> {
    let ts = jobs::now();
    tx.execute("INSERT OR IGNORE INTO jobs(job_id,job_key,job_type,subject_id,payload,priority,state,created_at,updated_at) VALUES(?1,?2,?3,?4,?5,1,'queued',?6,?6)",rusqlite::params![format!("job-{}",uuid::Uuid::new_v4()),key,kind,entry,payload.to_string(),ts])?;
    let id: String = tx.query_row("SELECT job_id FROM jobs WHERE job_key=?1", [key], |r| {
        r.get(0)
    })?;
    tx.execute("INSERT INTO job_consumers(job_id,subscription_id,requested_at) VALUES(?1,?2,?3) ON CONFLICT(job_id,subscription_id) DO UPDATE SET requested=job_consumers.requested+1,state='queued',attempts=0,next_run_at=NULL,last_error=NULL,requested_at=excluded.requested_at WHERE job_consumers.completed=job_consumers.requested AND job_consumers.state='succeeded'",rusqlite::params![id,subscription,ts])?;
    reconcile(tx, &id)?;
    Ok(id)
}

pub fn is_shared(c: &Connection, id: &str) -> Result<bool, CoreError> {
    Ok(c.query_row(
        "SELECT EXISTS(SELECT 1 FROM job_consumers WHERE job_id=?1)",
        [id],
        |r| r.get(0),
    )?)
}

/// Choose only pending, enabled consumers. Failed/auth-blocked work does not
/// starve another consumer; explicit cancellation remains on the logical job.
type PendingConsumer = (String, i64, String, i64, Option<String>, Option<String>);
fn next(c: &Connection, id: &str) -> Result<Option<PendingConsumer>, CoreError> {
    Ok(c.query_row("SELECT w.subscription_id,w.requested,w.state,w.attempts,w.next_run_at,w.last_error FROM job_consumers w JOIN subscriptions s ON s.subscription_id=w.subscription_id WHERE w.job_id=?1 AND w.requested>w.completed AND s.state='enabled' AND w.state IN ('queued','waiting_retry','waiting_storage') ORDER BY CASE WHEN w.next_run_at IS NULL OR w.next_run_at<=?2 THEN 0 ELSE 1 END,w.next_run_at,w.requested_at,w.subscription_id LIMIT 1",rusqlite::params![id,jobs::now()],|r|Ok((r.get(0)?,r.get(1)?,r.get(2)?,r.get(3)?,r.get(4)?,r.get(5)?))).optional()?)
}

pub fn reconcile(tx: &Transaction<'_>, id: &str) -> Result<(), CoreError> {
    if !is_shared(tx, id)? {
        return Ok(());
    }
    let (state, raw): (String, String) = tx.query_row(
        "SELECT state,payload FROM jobs WHERE job_id=?1",
        [id],
        |r| Ok((r.get(0)?, r.get(1)?)),
    )?;
    if state == "cancelled" {
        return Ok(());
    }
    let mut payload: Value = serde_json::from_str(&raw)?;
    if matches!(state.as_str(), "running" | "leased") {
        let current: bool = tx.query_row("SELECT EXISTS(SELECT 1 FROM subscriptions s WHERE s.subscription_id=?1 AND s.state='enabled' AND s.config_version=?2 AND s.credential_id IS ?3 AND (SELECT generation FROM credentials_state WHERE credential_id=s.credential_id) IS ?4)",rusqlite::params![payload["subscription_id"].as_str(),payload["execution_scope"]["subscription_version"].as_i64(),payload["execution_scope"]["credential_id"].as_str(),payload["execution_scope"]["credential_generation"].as_i64()],|r|r.get(0))?;
        if current {
            return Ok(());
        }
    }
    if let Some((sub, request, state, attempts, at, error)) = next(tx, id)? {
        let disabled: bool = tx.query_row("SELECT EXISTS(SELECT 1 FROM subscriptions s JOIN source_accounts a ON a.account_id=s.account_id JOIN plugins_state p ON p.platform=a.namespace WHERE s.subscription_id=?1 AND p.enabled=0)",[&sub],|r|r.get(0))?;
        let same = payload["subscription_id"] == sub
            && payload["consumer_request"] == request
            && !matches!(state.as_str(), "succeeded" | "paused");
        let preserve_generation =
            same && matches!(
                state.as_str(),
                "queued" | "waiting_retry" | "waiting_storage"
            ) && !matches!(
                tx.query_row("SELECT state FROM jobs WHERE job_id=?1", [id], |r| r
                    .get::<_, String>(0))?
                    .as_str(),
                "leased" | "running" | "succeeded" | "paused"
            );
        payload["subscription_id"] = Value::String(sub);
        payload["consumer_request"] = Value::from(request);
        payload.as_object_mut().unwrap().remove("execution_scope");
        tx.execute("UPDATE jobs SET payload=?2,state=?3,target_generation=target_generation+?8,attempts=?4,next_run_at=?5,last_error=?6,lease_owner=NULL,lease_expires_at=NULL,updated_at=?7 WHERE job_id=?1",rusqlite::params![id,payload.to_string(),if disabled {"waiting_plugin"} else {&state},attempts,at,error,jobs::now(),i64::from(!preserve_generation)])?;
    } else {
        let state: String = tx.query_row("SELECT CASE WHEN NOT EXISTS(SELECT 1 FROM job_consumers WHERE job_id=?1 AND requested>completed) THEN 'succeeded' WHEN NOT EXISTS(SELECT 1 FROM job_consumers w JOIN subscriptions s ON s.subscription_id=w.subscription_id WHERE w.job_id=?1 AND requested>completed AND s.state='enabled') THEN 'paused' ELSE COALESCE((SELECT state FROM job_consumers WHERE job_id=?1 AND requested>completed ORDER BY CASE state WHEN 'waiting_auth' THEN 0 ELSE 1 END LIMIT 1),'paused') END",[id],|r|r.get(0))?;
        tx.execute("UPDATE jobs SET state=?2,target_generation=target_generation+CASE WHEN state='succeeded' AND ?2='succeeded' THEN 0 ELSE 1 END,lease_owner=NULL,lease_expires_at=NULL,updated_at=?3 WHERE job_id=?1",rusqlite::params![id,state,jobs::now()])?;
    }
    Ok(())
}

#[cfg(test)]
#[path = "consumer_tests.rs"]
mod tests;

pub fn subscription_changed(
    tx: &Transaction<'_>,
    sub: &str,
    new_request: bool,
) -> Result<(), CoreError> {
    if new_request {
        tx.execute("UPDATE job_consumers SET requested=requested+1,state='queued',attempts=0,next_run_at=NULL,last_error=NULL,requested_at=?2 WHERE subscription_id=?1 AND state!='cancelled'",rusqlite::params![sub,jobs::now()])?;
    }
    let ids = tx
        .prepare("SELECT job_id FROM job_consumers WHERE subscription_id=?1")?
        .query_map([sub], |r| r.get::<_, String>(0))?
        .collect::<Result<Vec<_>, _>>()?;
    for id in ids {
        reconcile(tx, &id)?;
    }
    Ok(())
}

pub fn finish(tx: &Transaction<'_>, job: &ClaimedJob, success: bool) -> Result<(), CoreError> {
    if !is_shared(tx, &job.job_id)? {
        return Ok(());
    }
    let sub = job.payload["subscription_id"].as_str();
    if success {
        tx.execute("UPDATE job_consumers SET completed=?3,state=CASE WHEN requested=?3 THEN 'succeeded' ELSE 'queued' END WHERE job_id=?1 AND subscription_id=?2",rusqlite::params![job.job_id,sub,job.payload["consumer_request"].as_i64()])?;
    } else {
        tx.execute("UPDATE job_consumers SET state=(SELECT state FROM jobs WHERE job_id=?1),attempts=(SELECT attempts FROM jobs WHERE job_id=?1),next_run_at=(SELECT next_run_at FROM jobs WHERE job_id=?1),last_error=(SELECT last_error FROM jobs WHERE job_id=?1) WHERE job_id=?1 AND subscription_id=?2",rusqlite::params![job.job_id,sub])?;
    }
    reconcile(tx, &job.job_id)
}

pub fn retry(tx: &Transaction<'_>, id: &str) -> Result<(), CoreError> {
    tx.execute("UPDATE job_consumers SET state='queued',requested=MAX(requested,completed+1),attempts=0,next_run_at=NULL,last_error=NULL WHERE job_id=?1",[id])?;
    reconcile(tx, id)
}

pub fn credential_changed(
    tx: &Transaction<'_>,
    credential: &str,
    valid: bool,
) -> Result<(), CoreError> {
    let subscriptions = tx
        .prepare("SELECT subscription_id FROM subscriptions WHERE credential_id=?1")?
        .query_map([credential], |r| r.get::<_, String>(0))?
        .collect::<Result<Vec<_>, _>>()?;
    for sub in subscriptions {
        if valid {
            tx.execute("UPDATE job_consumers SET state='queued',next_run_at=NULL WHERE subscription_id=?1 AND state='waiting_auth'",[&sub])?;
        } else {
            tx.execute("UPDATE job_consumers SET state='waiting_auth' WHERE subscription_id=?1 AND requested>completed AND state NOT IN ('cancelled','failed')",[&sub])?;
        }
        subscription_changed(tx, &sub, false)?;
    }
    Ok(())
}
