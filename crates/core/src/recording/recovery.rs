//! Finishing fragments a crash or a lease loss left half-sealed. Recovery never restarts a
//! capture; it only decides whether bytes already on disk may count as media.
use super::fragment::{admit_quality, representation, sealed_state, validate_fragment};
use super::invalid;
use crate::core::Core;
use crate::error::CoreError;
use crate::jobs::ClaimedJob;
use crate::objects::CommitInfo;
use serde_json::{Value, json};
use trace_domain::ids::OperationId;

const CLOSED_PAGE: i64 = 64;

/// A fragment that was still receiving, with the state of its materialization.
struct Pending {
    fragment: String,
    media: String,
    operation: String,
    stage: String,
    sha: Option<String>,
    evidence: String,
}

async fn mark_interrupted(
    core: &Core,
    guard: Option<&ClaimedJob>,
    fragment: &str,
    reason: &str,
) -> Result<(), CoreError> {
    let (guard, fragment, reason) = (guard.cloned(), fragment.to_owned(), reason.to_owned());
    core.db
        .write(move |tx| {
            if let Some(job) = &guard {
                job.check(tx)?;
            }
            tx.execute(
                "UPDATE live_fragments SET state='interrupted',reason=?2 WHERE fragment_id=?1",
                [fragment, reason],
            )?;
            Ok(())
        })
        .await
}

/// Measurements of a recovered fragment; raw captures are provenance and are not probed.
/// `None` when the container is invalid, in which case the object is quarantined and the
/// fragment marked interrupted by the caller.
async fn measure(
    core: &Core,
    original: bool,
    sha: Result<&str, CoreError>,
) -> Result<Option<Value>, CoreError> {
    if original {
        return Ok(Some(json!({"original_capture": true})));
    }
    Ok(validate_fragment(core, sha?).await.ok())
}

/// Takes the fragments this job's session left receiving back to a sealed or interrupted state.
pub(super) async fn recover_pending(core: &Core, job: &ClaimedJob) -> Result<(), CoreError> {
    let _sealing = core.recording_recovery.lock().await;
    let orphan_job = job.clone();
    core.db
        .write(move |tx| {
            orphan_job.check(tx)?;
            tx.execute(
                "UPDATE live_fragments SET state='interrupted',reason='interrupted_before_receiving' WHERE session_id=?1 AND state='receiving' AND NOT EXISTS(SELECT 1 FROM materializations m WHERE m.subject_id=live_fragments.media_id)",
                [orphan_job.subject_id],
            )?;
            Ok(())
        })
        .await?;
    for pending in load_pending(core, job).await? {
        recover_one(core, job, pending).await?;
    }
    Ok(())
}

async fn load_pending(core: &Core, job: &ClaimedJob) -> Result<Vec<Pending>, CoreError> {
    let j = job.clone();
    core.db
        .read(move |c| {
            j.check(c)?;
            let mut stmt = c.prepare("SELECT f.fragment_id,f.media_id,m.operation_id,m.stage,m.received_sha256,f.evidence FROM live_fragments f JOIN materializations m ON m.subject_id=f.media_id WHERE f.session_id=?1 AND f.state='receiving'")?;
            let rows = stmt
                .query_map([j.subject_id], |r| {
                    Ok(Pending {
                        fragment: r.get(0)?,
                        media: r.get(1)?,
                        operation: r.get(2)?,
                        stage: r.get(3)?,
                        sha: r.get(4)?,
                        evidence: r.get(5)?,
                    })
                })?
                .collect::<Result<Vec<_>, _>>()?;
            Ok(rows)
        })
        .await
}

async fn recover_one(core: &Core, job: &ClaimedJob, p: Pending) -> Result<(), CoreError> {
    if matches!(p.stage.as_str(), "PREPARED" | "OBJECT_READY" | "COMMITTED") {
        return recover_window(core, job, p).await;
    }
    mark_interrupted(core, Some(job), &p.fragment, "unsealed_after_restart").await
}

/// The bytes were sealed, or nearly: install, measure, admit and commit them under this job.
async fn recover_window(core: &Core, job: &ClaimedJob, p: Pending) -> Result<(), CoreError> {
    let evidence: Value = serde_json::from_str(&p.evidence)?;
    let original = evidence["original_capture"] == true;
    let sha = || {
        p.sha
            .as_deref()
            .ok_or_else(|| invalid("sealed hash missing"))
    };
    let op = OperationId::new(p.operation.clone());
    if p.stage == "PREPARED" {
        core.objects.install_object(&core.db, &op, sha()?).await?;
    }
    let (j, operation) = (job.clone(), p.operation.clone());
    core.db
        .write(move |tx| {
            j.check(tx)?;
            tx.execute(
                "UPDATE materializations SET job_id=?2,generation=?3 WHERE operation_id=?1",
                rusqlite::params![operation, j.job_id, j.generation],
            )?;
            Ok(())
        })
        .await?;
    let Some(measured) = measure(core, original, sha()).await? else {
        core.objects.quarantine(&core.db, &op).await?;
        return mark_interrupted(core, Some(job), &p.fragment, "recovery_container_invalid").await;
    };
    if !admit_quality(core, &p.fragment, &op, &evidence, &measured).await? {
        return Ok(());
    }
    let info = CommitInfo {
        job: Some(job.clone()),
        media_id: p.media.clone(),
        source_representation: representation(original).into(),
        claimed_spec: evidence,
    };
    core.objects
        .commit(&core.db, &op, &info, Some(measured))
        .await?;
    let (j, fragment) = (job.clone(), p.fragment);
    core.db
        .write(move |tx| {
            j.check(tx)?;
            tx.execute(
                "UPDATE live_fragments SET state=?2,reason='recovered_commit_window' WHERE fragment_id=?1",
                rusqlite::params![fragment, sealed_state(original)],
            )?;
            Ok(())
        })
        .await
}

/// A closed capture whose recording worker no longer holds a live lease.
struct Closed {
    row: i64,
    fragment: String,
    session: String,
    media: String,
    operation: String,
    stage: String,
    sha: String,
    evidence: String,
}

/// Finish only durable, already closed captures whose recording worker no longer
/// owns a live lease. This local recovery never restarts capture or revives a job.
/// A cancelled subscription still retains the bytes captured before cancellation.
pub async fn recover_closed(core: &Core) -> Result<usize, CoreError> {
    let _sealing = core.recording_recovery.lock().await;
    let mut after = 0i64;
    let mut recovered = 0;
    loop {
        let rows = load_closed(core, after).await?;
        let Some(last) = rows.last() else {
            break;
        };
        after = last.row;
        for closed in rows {
            if recover_closed_one(core, closed).await? {
                recovered += 1;
            }
        }
    }
    Ok(recovered)
}

async fn load_closed(core: &Core, after: i64) -> Result<Vec<Closed>, CoreError> {
    core.db
        .read(move |c| {
            let mut stmt = c.prepare("SELECT f.rowid,f.fragment_id,f.session_id,f.media_id,m.operation_id,m.stage,COALESCE(m.received_sha256,''),f.evidence FROM live_fragments f JOIN materializations m ON m.subject_id=f.media_id LEFT JOIN jobs j ON j.job_id=m.job_id WHERE f.rowid>?1 AND f.state='receiving' AND f.end_ms IS NOT NULL AND m.subject_kind='live_fragment' AND m.stage IN ('PREPARED','OBJECT_READY','COMMITTED') AND COALESCE(j.state,'') NOT IN ('leased','running') ORDER BY f.rowid LIMIT ?2")?;
            let rows = stmt
                .query_map(rusqlite::params![after, CLOSED_PAGE], |r| {
                    Ok(Closed {
                        row: r.get(0)?,
                        fragment: r.get(1)?,
                        session: r.get(2)?,
                        media: r.get(3)?,
                        operation: r.get(4)?,
                        stage: r.get(5)?,
                        sha: r.get(6)?,
                        evidence: r.get(7)?,
                    })
                })?
                .collect::<Result<Vec<_>, _>>()?;
            Ok(rows)
        })
        .await
}

/// Whether the fragment was sealed by this recovery.
async fn recover_closed_one(core: &Core, c: Closed) -> Result<bool, CoreError> {
    let mut evidence: Value = serde_json::from_str(&c.evidence)?;
    let original = evidence["original_capture"] == true;
    if c.stage != "COMMITTED" && !commit_closed(core, &c, &evidence, original).await? {
        return Ok(false);
    }
    evidence["commit_recovery"] = json!("closed_capture_after_lease_release");
    let (fragment, session) = (c.fragment, c.session);
    core.db
        .write(move |tx| {
            tx.execute(
                "UPDATE live_fragments SET state=?2,evidence=?3 WHERE fragment_id=?1 AND state='receiving'",
                rusqlite::params![fragment, sealed_state(original), evidence.to_string()],
            )?;
            crate::objects::outbox_emit(
                tx,
                "live.fragment.sealed",
                &session,
                &json!({"fragment_id": fragment, "recovered": true}),
            )?;
            Ok(())
        })
        .await?;
    Ok(true)
}

/// Verifies and commits the closed capture's object. `false` when it must stay as it is:
/// the object or container is invalid, quality is not admitted, or another worker adopted it.
async fn commit_closed(
    core: &Core,
    c: &Closed,
    evidence: &Value,
    original: bool,
) -> Result<bool, CoreError> {
    let op = OperationId::new(c.operation.clone());
    // Verify the entire object even for OBJECT_READY. No old "verified" flag is evidence
    // that disk bytes have survived interruption.
    if core
        .objects
        .install_object(&core.db, &op, &c.sha)
        .await
        .is_err()
    {
        core.objects.quarantine(&core.db, &op).await?;
        mark_interrupted(core, None, &c.fragment, "recovery_object_invalid").await?;
        return Ok(false);
    }
    let Some(measured) = measure(core, original, Ok(&c.sha)).await? else {
        core.objects.quarantine(&core.db, &op).await?;
        mark_interrupted(core, None, &c.fragment, "recovery_container_invalid").await?;
        return Ok(false);
    };
    if !admit_quality(core, &c.fragment, &op, evidence, &measured).await? {
        return Ok(false);
    }
    if !adopt_object(core, &c.operation).await? {
        return Ok(false);
    }
    let info = CommitInfo {
        job: None,
        media_id: c.media.clone(),
        source_representation: representation(original).into(),
        claimed_spec: evidence.clone(),
    };
    core.objects
        .commit(&core.db, &op, &info, Some(measured))
        .await?;
    Ok(true)
}

/// Releases the object from its finished job so recovery can commit it; only one wins.
async fn adopt_object(core: &Core, operation: &str) -> Result<bool, CoreError> {
    let id = operation.to_owned();
    core.db
        .write(move |tx| {
            let changed = tx.execute(
                "UPDATE materializations SET job_id=NULL WHERE operation_id=?1 AND subject_kind='live_fragment' AND stage='OBJECT_READY' AND NOT EXISTS(SELECT 1 FROM jobs j WHERE j.job_id=materializations.job_id AND j.state IN ('leased','running')) AND EXISTS(SELECT 1 FROM live_fragments f JOIN media_items mi ON mi.media_id=f.media_id WHERE f.media_id=materializations.subject_id AND f.state='receiving' AND f.end_ms IS NOT NULL)",
                [id],
            )?;
            Ok(changed == 1)
        })
        .await
}
