//! Scheduling of login-completion upgrade work and the per-attachment candidate ranking.

use super::*;

/// Rank per attachment before expanding ordered segments. Required audio and
/// other attachments are independent groups and cannot disappear in this step.
pub fn highest_candidates(
    candidates: &mut Vec<trace_contracts::plugin::MediaCandidate>,
    prefer_fps: bool,
) {
    let mut best = std::collections::BTreeMap::new();
    for (i, c) in candidates.iter().enumerate() {
        let rank = (
            trace_contracts::content::source_rank(&c.claimed_spec, prefer_fps),
            c.claimed_spec["bitrate"]
                .as_i64()
                .or_else(|| c.claimed_spec["bit_rate"].as_i64())
                .unwrap_or(0),
        );
        let key = (
            c.resource.source_attachment_id.clone(),
            c.resource.role.clone(),
        );
        if best.get(&key).is_none_or(|(r, _)| rank > *r) {
            best.insert(key, (rank, i));
        }
    }
    let selected = best
        .into_values()
        .map(|(_, i)| i)
        .collect::<std::collections::BTreeSet<_>>();
    let mut i = 0;
    candidates.retain(|_| {
        let keep = selected.contains(&i);
        i += 1;
        keep
    });
}

const INSERT_JOBS: &str = "INSERT OR IGNORE INTO jobs(job_id,job_key,job_type,subject_id,payload,priority,state,created_at,updated_at) VALUES(?1,?2,'auth_completion',?3,?4,3,'queued',?5,?5)";

pub fn schedule(tx: &Transaction<'_>, credential: &str, generation: i64) -> Result<(), CoreError> {
    let subscriptions = tx
        .prepare(
            "SELECT subscription_id,config_version FROM subscriptions
             WHERE credential_id=?1 AND content_type!='live'
               AND acquisition_kind='subscription'",
        )?
        .query_map([credential], |r| {
            Ok((r.get::<_, String>(0)?, r.get::<_, i64>(1)?))
        })?
        .collect::<Result<Vec<_>, _>>()?;
    for (sub, version) in subscriptions {
        if crate::jobs::consumers::subscription_refused(tx, &sub)? {
            continue;
        }
        let bound: i64 = tx.query_row(
            "SELECT COALESCE(MAX(rowid),0) FROM entry_subscriptions WHERE subscription_id=?1",
            [&sub],
            |r| r.get(0),
        )?;
        if bound == 0 {
            continue;
        }
        let key = format!("auth-completion:{credential}:{generation}:{sub}:{version}");
        tx.execute(
            INSERT_JOBS,
            rusqlite::params![
                format!("job-{}", uuid::Uuid::new_v4()),
                key,
                credential,
                json!({
                    "subscription_id": sub,
                    "credential_id": credential,
                    "credential_generation": generation,
                    "upper_bound": bound,
                    "after": 0,
                })
                .to_string(),
                jobs::now()
            ],
        )?;
    }
    Ok(())
}

const SELECT_ENTRY_SUBSCRIPTIONS: &str = "SELECT d.rowid,e.entry_id,r.revision_id FROM entry_subscriptions d JOIN entries e ON e.entry_id=d.entry_id JOIN current_entry_revisions r ON r.entry_id=e.entry_id WHERE d.subscription_id=?1 AND d.rowid>?2 AND d.rowid<=?3 AND e.native_kind!='live' AND e.lifecycle!='deleted' AND e.media_suppressed=0 AND EXISTS(SELECT 1 FROM media_items m WHERE m.revision_id=r.revision_id) AND (NOT EXISTS(SELECT 1 FROM acquisition_contexts x WHERE x.entry_id=e.entry_id) OR EXISTS(SELECT 1 FROM acquisition_contexts x WHERE x.entry_id=e.entry_id AND x.auth_state IN ('anonymous','expired','unknown'))) ORDER BY d.rowid LIMIT 16";
const INSERT_JOBS_2: &str = "INSERT OR IGNORE INTO jobs(job_id,job_key,job_type,subject_id,payload,priority,state,created_at,updated_at) VALUES(?1,?2,'observe',?3,?4,3,'queued',?5,?5)";

const UPDATE_JOBS: &str = "UPDATE jobs SET state='queued',payload=json_set(payload,'$.after',?2),next_run_at=?3,lease_owner=NULL,lease_expires_at=NULL WHERE job_id=?1";

pub async fn run(core: &Core, job: &ClaimedJob) -> Result<(), CoreError> {
    let guard = job.clone();
    core.db
        .write(move |tx| {
            guard.check(tx)?;
            let sub = guard.payload["subscription_id"].as_str();
            let after = guard.payload["after"].as_i64().unwrap_or(0);
            let bound = guard.payload["upper_bound"].as_i64().unwrap_or(0);
            let rows = tx
                .prepare(SELECT_ENTRY_SUBSCRIPTIONS)?
                .query_map(rusqlite::params![sub, after, bound], |r| {
                    Ok((
                        r.get::<_, i64>(0)?,
                        r.get::<_, String>(1)?,
                        r.get::<_, String>(2)?,
                    ))
                })?
                .collect::<Result<Vec<_>, _>>()?;
            for (_, entry, revision) in &rows {
                let key = format!("completion-observe:{}:{entry}", guard.job_id);
                tx.execute(
                    INSERT_JOBS_2,
                    rusqlite::params![
                        format!("job-{}", uuid::Uuid::new_v4()),
                        key,
                        entry,
                        json!({
                            "entry_id": entry,
                            "subscription_id": sub,
                            "completion": true,
                            "completion_parent": guard.job_id,
                            "expected_revision_id": revision,
                            "credential_id": guard.payload["credential_id"],
                            "credential_generation": guard.payload["credential_generation"],
                        })
                        .to_string(),
                        jobs::now()
                    ],
                )?;
            }
            if rows.len() == 16 {
                let next = (time::OffsetDateTime::now_utc() + time::Duration::seconds(2))
                    .format(&time::format_description::well_known::Rfc3339)
                    .map_err(|_| CoreError::Internal("clock".into()))?;
                tx.execute(
                    UPDATE_JOBS,
                    rusqlite::params![guard.job_id, rows.last().unwrap().0, next],
                )?;
            }
            Ok(())
        })
        .await
}

pub fn schedule_media(
    tx: &Transaction<'_>,
    job: &ClaimedJob,
    payload: Value,
    entry: &str,
    revision: &str,
    group: &str,
) -> Result<(), CoreError> {
    let mut payload = payload;
    for key in [
        "completion",
        "completion_parent",
        "expected_revision_id",
        "credential_id",
        "credential_generation",
    ] {
        payload[key] = job.payload[key].clone();
    }
    let key = format!("completion-media:{}:{revision}:{group}", job.job_id);
    tx.execute("INSERT OR IGNORE INTO jobs(job_id,job_key,job_type,subject_id,payload,priority,state,created_at,updated_at) VALUES(?1,?2,'media',?3,?4,3,'queued',?5,?5)",rusqlite::params![format!("job-{}",uuid::Uuid::new_v4()),key,entry,payload.to_string(),jobs::now()])?;
    Ok(())
}

pub(super) fn policy(c: &Connection) -> Result<Option<String>, CoreError> {
    Ok(c.query_row(
        "SELECT value FROM meta WHERE key='quality_policy'",
        [],
        |r| r.get(0),
    )
    .optional()?)
}
