//! Credential metadata and the jobs that wait on a credential. Secrets live in the vault only.
use crate::error::CoreError;
use rusqlite::Connection;
use serde_json::{Value, json};

pub fn list(c: &Connection, platform: Option<&str>) -> Result<Vec<Value>, CoreError> {
    let mut stmt = c.prepare(
        "SELECT credential_id, platform, subject, status, capability, validated_at, created_at
         FROM credentials_state WHERE (?1 IS NULL OR platform=?1)",
    )?;
    let rows = stmt
        .query_map([platform], |r| {
            Ok(json!({
                "credential_id": r.get::<_, String>(0)?,
                "platform": r.get::<_, String>(1)?,
                "subject": r.get::<_, Option<String>>(2)?,
                "status": ({let status=r.get::<_,String>(3)?;if status=="authenticated"{"valid".to_owned()}else{status}}),
                "capability": super::json_column(r.get::<_, String>(4)),
                "validated_at": r.get::<_, Option<String>>(5)?,
                "created_at": r.get::<_, String>(6)?,
            }))
        })?
        .collect::<Result<Vec<_>, _>>()?;
    Ok(rows)
}

pub fn insert(
    c: &Connection,
    id: &str,
    platform: &str,
    subject: Option<&str>,
    now: &str,
) -> Result<(), CoreError> {
    c.execute(
        "INSERT INTO credentials_state (credential_id, platform, subject, status, capability, created_at, updated_at, vault_ref)
         VALUES (?1,?2,?3,'unknown','{}',?4,?4,?1)",
        rusqlite::params![id, platform, subject, now],
    )?;
    Ok(())
}

pub fn platform_of(c: &Connection, id: &str) -> Result<String, CoreError> {
    Ok(c.query_row(
        "SELECT platform FROM credentials_state WHERE credential_id=?1",
        [id],
        |r| r.get::<_, String>(0),
    )?)
}

pub fn vault_ref_of(c: &Connection, id: &str) -> Result<String, CoreError> {
    Ok(c.query_row(
        "SELECT vault_ref FROM credentials_state WHERE credential_id=?1",
        [id],
        |r| r.get(0),
    )?)
}

/// Platform, current vault reference and generation.
pub fn binding_of(c: &Connection, id: &str) -> Result<(String, String, i64), CoreError> {
    Ok(c.query_row(
        "SELECT platform,vault_ref,generation FROM credentials_state WHERE credential_id=?1",
        [id],
        |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)),
    )?)
}

/// Vault reference and generation for a probe.
pub fn reference_and_generation(c: &Connection, id: &str) -> Result<(String, i64), CoreError> {
    Ok(c.query_row(
        "SELECT vault_ref,generation FROM credentials_state WHERE credential_id=?1",
        [id],
        |r| Ok((r.get(0)?, r.get(1)?)),
    )?)
}

/// Generation and status, when the credential still exists.
pub fn generation_and_status(c: &Connection, id: &str) -> Option<(i64, String)> {
    c.query_row(
        "SELECT generation,status FROM credentials_state WHERE credential_id=?1",
        [id],
        |r| Ok((r.get::<_, i64>(0)?, r.get::<_, String>(1)?)),
    )
    .ok()
}

/// Swaps in a new vault entry and resets validation; only if nobody changed it meanwhile.
pub fn swap_secret(
    c: &Connection,
    id: &str,
    new_ref: &str,
    expected_generation: i64,
    now: &str,
) -> Result<bool, CoreError> {
    let n = c.execute(
        "UPDATE credentials_state SET vault_ref=?2,generation=generation+1,status='unknown',validated_at=NULL,capability='{}',updated_at=?3 WHERE credential_id=?1 AND generation=?4",
        rusqlite::params![id, new_ref, now, expected_generation],
    )?;
    Ok(n == 1)
}

/// Pending probes of the credential are obsolete once its secret changes.
pub fn cancel_probe_jobs(c: &Connection, id: &str) -> Result<(), CoreError> {
    c.execute(
        "UPDATE jobs SET state='cancelled',target_generation=target_generation+1,lease_owner=NULL,lease_expires_at=NULL WHERE job_type='credential_probe' AND subject_id=?1",
        [id],
    )?;
    Ok(())
}

/// Work of subscriptions using the credential waits for authentication again.
pub fn park_subscription_work(c: &Connection, id: &str, now: &str) -> Result<(), CoreError> {
    c.execute(
        "UPDATE jobs SET state='waiting_auth',target_generation=target_generation+1,lease_owner=NULL,lease_expires_at=NULL,updated_at=?2 WHERE json_extract(payload,'$.subscription_id') IN (SELECT subscription_id FROM subscriptions WHERE credential_id=?1) AND state NOT IN ('cancelled','paused','succeeded','waiting_user')",
        rusqlite::params![id, now],
    )?;
    Ok(())
}

/// Revoking stops probes and parks the dependent work.
pub fn park_for_revocation(c: &Connection, id: &str) -> Result<(), CoreError> {
    c.execute(
        "UPDATE jobs SET state=CASE WHEN job_type='credential_probe' THEN 'cancelled' ELSE 'waiting_auth' END,target_generation=target_generation+1,lease_owner=NULL,lease_expires_at=NULL WHERE (subject_id=?1 AND job_type='credential_probe' OR json_extract(payload,'$.subscription_id') IN (SELECT subscription_id FROM subscriptions WHERE credential_id=?1)) AND state NOT IN ('succeeded','cancelled','paused','waiting_user')",
        [id],
    )?;
    Ok(())
}

pub fn delete(c: &Connection, id: &str) -> Result<(), CoreError> {
    c.execute("DELETE FROM credentials_state WHERE credential_id=?1", [id])?;
    Ok(())
}

/// After revocation, queued or running work of dependent subscriptions waits for authentication.
pub fn park_active_work(c: &Connection, id: &str, now: &str) -> Result<(), CoreError> {
    c.execute(
        "UPDATE jobs SET state='waiting_auth',target_generation=target_generation+1,lease_owner=NULL,lease_expires_at=NULL,updated_at=?1 WHERE state IN ('queued','leased','running','waiting_retry','waiting_auth') AND (json_extract(payload,'$.subscription_id') IN (SELECT subscription_id FROM subscriptions WHERE credential_id=?2))",
        rusqlite::params![now, id],
    )?;
    Ok(())
}

/// Schedules the next probe of a credential generation (a valid one rarely, others soon).
pub fn schedule_probe(
    c: &Connection,
    id: &str,
    generation: i64,
    platform: &str,
    next_run_at: &str,
    now: &str,
) -> Result<(), CoreError> {
    c.execute(
        "INSERT INTO jobs(job_id,job_key,job_type,subject_id,payload,priority,state,next_run_at,created_at,updated_at) VALUES(?1,?2,'credential_probe',?3,?4,2,'queued',?5,?6,?6) ON CONFLICT(job_key) DO UPDATE SET state='queued',next_run_at=excluded.next_run_at WHERE jobs.state NOT IN ('running','leased','cancelled','paused')",
        rusqlite::params![
            format!("job-{}", uuid::Uuid::new_v4()),
            format!("credential-probe:{id}:{generation}"),
            id,
            json!({"credential_id":id,"credential_generation":generation,"platform":platform}).to_string(),
            next_run_at,
            now
        ],
    )?;
    Ok(())
}

/// A probe job finished with a valid credential: run again later.
pub fn reschedule_probe_job(
    c: &Connection,
    job_id: &str,
    next_run_at: &str,
) -> Result<(), CoreError> {
    c.execute(
        "UPDATE jobs SET state='queued',next_run_at=?2,lease_owner=NULL,lease_expires_at=NULL,attempts=0 WHERE job_id=?1",
        rusqlite::params![job_id, next_run_at],
    )?;
    Ok(())
}

pub fn job_attempts(c: &Connection, job_id: &str) -> Result<u32, CoreError> {
    Ok(
        c.query_row("SELECT attempts FROM jobs WHERE job_id=?1", [job_id], |r| {
            r.get::<_, u32>(0)
        })?,
    )
}

/// A probe job could not decide: retry later and keep a class-only error.
pub fn retry_probe_job_later(
    c: &Connection,
    job_id: &str,
    next_run_at: &str,
    last_error: &str,
) -> Result<(), CoreError> {
    c.execute(
        "UPDATE jobs SET state='waiting_retry',next_run_at=?2,lease_owner=NULL,lease_expires_at=NULL,last_error=?3 WHERE job_id=?1",
        rusqlite::params![job_id, next_run_at, last_error],
    )?;
    Ok(())
}

/// Stores a probe verdict for exactly this generation; false when the credential moved on.
pub fn record_probe_result(
    c: &Connection,
    id: &str,
    status: &str,
    capability_json: &str,
    generation: i64,
    now: &str,
) -> Result<(), CoreError> {
    c.execute(
        "UPDATE credentials_state SET status=?2, capability=?3, validated_at=?4, updated_at=?4 WHERE credential_id=?1 AND generation=?5",
        rusqlite::params![id, status, capability_json, now, generation],
    )
    .and_then(|n| {
        if n == 1 {
            Ok(n)
        } else {
            Err(rusqlite::Error::QueryReturnedNoRows)
        }
    })?;
    Ok(())
}

/// A rejected credential parks the work that depends on it.
pub fn park_unfinished_subscription_work(c: &Connection, id: &str) -> Result<(), CoreError> {
    c.execute(
        "UPDATE jobs SET state='waiting_auth',target_generation=target_generation+1,lease_owner=NULL,lease_expires_at=NULL WHERE json_extract(payload,'$.subscription_id') IN (SELECT subscription_id FROM subscriptions WHERE credential_id=?1) AND state NOT IN ('succeeded','failed','cancelled','paused','waiting_user')",
        [id],
    )?;
    Ok(())
}

/// The generation is still the valid one.
pub fn is_valid_generation(c: &Connection, id: &str, generation: i64) -> Result<bool, CoreError> {
    Ok(c.query_row(
        "SELECT EXISTS(SELECT 1 FROM credentials_state WHERE credential_id=?1 AND generation=?2 AND status='valid')",
        rusqlite::params![id, generation],
        |r| r.get(0),
    )?)
}

/// A valid credential wakes the enabled subscriptions that waited for authentication.
pub fn wake_waiting_auth(c: &Connection, id: &str, now: &str) -> Result<(), CoreError> {
    c.execute(
        "UPDATE jobs SET state='queued', next_run_at=?1, updated_at=?1
         WHERE state='waiting_auth'
           AND json_extract(payload,'$.subscription_id') IN (
             SELECT s.subscription_id FROM subscriptions s
             WHERE s.credential_id=?2 AND s.state='enabled' AND s.acquisition_kind='subscription'
               AND NOT EXISTS(SELECT 1 FROM jobs p WHERE json_extract(p.last_error,'$.class')='permission_denied'
                 AND json_extract(p.payload,'$.subscription_id')=s.subscription_id)
               AND NOT EXISTS(SELECT 1 FROM job_consumers w
                 WHERE w.subscription_id=s.subscription_id AND w.requested>w.completed
                   AND w.state='waiting_user'
                   AND json_extract(w.last_error,'$.class')='permission_denied'))",
        rusqlite::params![now, id],
    )?;
    Ok(())
}

/// Finished media jobs whose saved quality fell short are repeated with the new credential.
pub fn requeue_media_below_target(c: &Connection, id: &str, now: &str) -> Result<(), CoreError> {
    c.execute(
        "UPDATE jobs SET state='queued',target_generation=target_generation+1,next_run_at=?1,updated_at=?1 WHERE job_type='media' AND state='succeeded' AND json_extract(payload,'$.subscription_id') IN (SELECT subscription_id FROM subscriptions s WHERE credential_id=?2 AND state='enabled' AND acquisition_kind='subscription' AND NOT EXISTS(SELECT 1 FROM jobs p WHERE json_extract(p.last_error,'$.class')='permission_denied' AND json_extract(p.payload,'$.subscription_id')=s.subscription_id) AND NOT EXISTS(SELECT 1 FROM job_consumers w WHERE w.subscription_id=s.subscription_id AND w.requested>w.completed AND w.state='waiting_user' AND json_extract(w.last_error,'$.class')='permission_denied')) AND EXISTS(SELECT 1 FROM entry_revisions r JOIN media_items m ON m.revision_id=r.revision_id WHERE r.entry_id=jobs.subject_id AND (json_extract(m.acquisition_evidence,'$.capability_decreased')=1 OR json_extract(m.acquisition_evidence,'$.below_target')=1 OR (json_extract(m.acquisition_evidence,'$.requested_quality.min_height') IS NOT NULL AND NOT EXISTS(SELECT 1 FROM asset_selections x JOIN media_variants v ON v.variant_id=x.variant_id JOIN replicas p ON p.blob_id=v.blob_id AND p.state='verified' WHERE x.media_id=m.media_id AND json_extract(v.measured_spec,'$.height')>=json_extract(m.acquisition_evidence,'$.requested_quality.min_height'))) OR (json_extract(m.acquisition_evidence,'$.requested_quality.min_fps') IS NOT NULL AND NOT EXISTS(SELECT 1 FROM asset_selections x JOIN media_variants v ON v.variant_id=x.variant_id JOIN replicas p ON p.blob_id=v.blob_id AND p.state='verified' WHERE x.media_id=m.media_id AND json_extract(v.measured_spec,'$.fps')>=json_extract(m.acquisition_evidence,'$.requested_quality.min_fps')))))",
        rusqlite::params![now, id],
    )?;
    Ok(())
}
