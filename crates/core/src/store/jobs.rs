//! Job rows as the control commands see them.
use crate::error::CoreError;
use rusqlite::Connection;
use trace_contracts::control::JobDto;

const JOB_COLUMNS: &str =
    "job_id, job_key, job_type, subject_id, state, attempts, target_generation,
                priority, next_run_at, last_error, created_at, updated_at";

fn job_dto(r: &rusqlite::Row<'_>) -> rusqlite::Result<JobDto> {
    Ok(JobDto {
        job_id: r.get(0)?,
        job_key: r.get(1)?,
        job_type: r.get(2)?,
        subject_id: r.get(3)?,
        state: r.get(4)?,
        attempts: r.get(5)?,
        target_generation: r.get(6)?,
        priority: r.get(7)?,
        next_run_at: r.get(8)?,
        last_error: r
            .get::<_, Option<String>>(9)?
            .and_then(|s| serde_json::from_str(&s).ok()),
        created_at: r.get(10)?,
        updated_at: r.get(11)?,
    })
}

/// Pausing the daemon puts running work back in the queue under a new generation.
pub fn requeue_running(c: &Connection, now: &str) -> Result<(), CoreError> {
    c.execute(
        "UPDATE jobs SET state='queued',target_generation=target_generation+1,lease_owner=NULL,lease_expires_at=NULL,next_run_at=?1,updated_at=?1 WHERE state IN ('leased','running')",
        [now],
    )?;
    Ok(())
}

/// Requeues one stopped job; returns how many rows changed (0 when it was not stopped).
pub fn retry_stopped(c: &Connection, job_id: &str, now: &str) -> Result<usize, CoreError> {
    Ok(c.execute(
        "UPDATE jobs SET state='queued',target_generation=target_generation+1,attempts=0,lease_owner=NULL,lease_expires_at=NULL,next_run_at=?2,last_error=NULL,updated_at=?2 WHERE job_id=?1 AND state IN ('failed','waiting_retry','waiting_storage','waiting_auth','waiting_plugin','waiting_user')",
        rusqlite::params![job_id, now],
    )?)
}

pub fn list(
    c: &Connection,
    state: Option<&str>,
    job_type: Option<&str>,
    limit: i64,
) -> Result<Vec<JobDto>, CoreError> {
    let mut stmt = c.prepare(&format!(
        "SELECT {JOB_COLUMNS}
         FROM jobs WHERE (?1 IS NULL OR state=?1) AND (?2 IS NULL OR job_type=?2)
         ORDER BY updated_at DESC LIMIT ?3"
    ))?;
    let rows = stmt
        .query_map(rusqlite::params![state, job_type, limit], job_dto)?
        .collect::<Result<Vec<_>, _>>()?;
    Ok(rows)
}

pub fn get(c: &Connection, job_id: &str) -> Result<Option<JobDto>, CoreError> {
    let mut stmt = c.prepare(&format!("SELECT {JOB_COLUMNS} FROM jobs WHERE job_id=?1"))?;
    let rows = stmt
        .query_map([job_id], job_dto)?
        .collect::<Result<Vec<_>, _>>()?;
    Ok(rows.into_iter().next())
}

/// Explicit retry keeps the execution history and re-queues a finished or waiting job.
pub fn retry(c: &Connection, job_id: &str, now: &str) -> Result<usize, CoreError> {
    Ok(c.execute(
        "UPDATE jobs SET state='queued', target_generation=target_generation+1, attempts=0, lease_owner=NULL, lease_expires_at=NULL, next_run_at=?2, last_error=NULL, updated_at=?2
         WHERE job_id=?1 AND state IN ('succeeded','failed','cancelled','waiting_auth','waiting_user','waiting_plugin','waiting_retry','waiting_storage')",
        rusqlite::params![job_id, now],
    )?)
}

/// State of one job.
pub fn state_of(c: &Connection, job_id: &str) -> Result<String, CoreError> {
    Ok(
        c.query_row("SELECT state FROM jobs WHERE job_id=?1", [job_id], |r| {
            r.get(0)
        })?,
    )
}
