//! Stage 6: the business commit of an installed object.
//!
//! One short transaction rechecks the generation and cancellation, creates the references
//! and queues the outbox events (INV-11): a cancelled or deleted target never comes back.

use super::*;

const SELECT_MATERIALIZATIONS_3: &str =
    "SELECT generation FROM materializations WHERE operation_id=?1";
const SELECT_MEDIA_ITEMS: &str = "SELECT count(*) FROM media_items m JOIN entry_revisions r ON r.revision_id=m.revision_id JOIN entries e ON e.entry_id=r.entry_id WHERE m.media_id=?1 AND e.lifecycle!='deleted' AND e.media_suppressed=0";
const UPDATE_MATERIALIZATIONS_2: &str =
    "UPDATE materializations SET stage='SUPERSEDED', updated_at=?2 WHERE operation_id=?1";
const UPDATE_JOBS: &str = "UPDATE jobs SET state='cancelled', updated_at=?2 WHERE job_id=?1";
const SELECT_ENTRIES: &str = "SELECT e.cache_suppressed!=0 FROM entries e JOIN entry_revisions r ON r.entry_id=e.entry_id JOIN media_items m ON m.revision_id=r.revision_id WHERE m.media_id=?1";
const UPDATE_MATERIALIZATIONS_3: &str =
    "UPDATE materializations SET stage='SUPERSEDED' WHERE operation_id=?1";
const INSERT_BLOBS: &str =
    "INSERT OR IGNORE INTO blobs (blob_id, size, mime, first_seen_at) VALUES (?1,?2,?3,?4)";
const UPDATE_MEDIA_VARIANTS: &str =
    "UPDATE media_variants SET approval_state='pending' WHERE variant_id=?1";
const UPDATE_MATERIALIZATIONS_4: &str = "UPDATE materializations SET stage='COMMITTED', blob_id=?2, updated_at=?3 WHERE operation_id=?1";
const SELECT_MATERIALIZATIONS_4: &str =
    "SELECT stage, received_sha256, received_bytes, mime, job_id, subject_id
                     FROM materializations WHERE operation_id=?1";
const INSERT_REPLICAS: &str = "INSERT INTO replicas (replica_id, blob_id, node_id, store_id, locator, managed, state, verified_at)
                     VALUES (?1,?2,'local','primary',?3,1,'verified',?4)
                     ON CONFLICT(blob_id,store_id,locator) DO UPDATE SET state='verified',managed=1,verified_at=excluded.verified_at";
const INSERT_MEDIA_VARIANTS: &str = "INSERT INTO media_variants (variant_id, media_id, source_representation, blob_id, claimed_spec, measured_spec, created_at)
                     VALUES (?1,?2,?3,?4,?5,?6,?7)";
const INSERT_ASSET_SELECTIONS: &str = "INSERT OR IGNORE INTO asset_selections (media_id, variant_id, policy_version, reason, updated_at)
                     VALUES (?1,?2,'v1','first verified variant',?3)";

/// The materialization row a commit works on.
struct Materialization {
    stage: String,
    blob_sha: String,
    size: i64,
    mime: Option<String>,
    job_id: Option<String>,
    subject_id: String,
}

impl ObjectStore {
    /// 阶段 6：业务提交（短事务重检代次/取消 → 建立引用 → Outbox → COMMITTED）。
    pub async fn commit(
        &self,
        db: &DbHandle,
        op_id: &OperationId,
        info: &CommitInfo,
        measured_spec: Option<serde_json::Value>,
    ) -> Result<CommitDisposition, CoreError> {
        let info = info.clone();
        let op = op_id.as_str().to_string();
        db.write(move |tx| commit_in(tx, &op, &info, measured_spec.as_ref()))
            .await
    }
}

fn commit_in(
    tx: &rusqlite::Transaction<'_>,
    op: &str,
    info: &CommitInfo,
    measured: Option<&serde_json::Value>,
) -> Result<CommitDisposition, CoreError> {
    let row = load_materialization(tx, op, info)?;
    if row.stage == "COMMITTED" {
        return Ok(CommitDisposition::Committed);
    }
    if row.stage != "OBJECT_READY" {
        return Err(CoreError::Internal(format!(
            "cannot commit from stage {}",
            row.stage
        )));
    }
    if superseded(tx, op, info, &row)? {
        return Ok(CommitDisposition::Superseded);
    }
    record_variant(tx, op, info, measured, &row)?;
    Ok(CommitDisposition::Committed)
}

/// Loads the row and checks it still belongs to this job, generation and media item.
fn load_materialization(
    tx: &rusqlite::Transaction<'_>,
    op: &str,
    info: &CommitInfo,
) -> Result<Materialization, CoreError> {
    let row = tx.query_row(SELECT_MATERIALIZATIONS_4, [op], |r| {
        Ok(Materialization {
            stage: r.get(0)?,
            blob_sha: r.get(1)?,
            size: r.get(2)?,
            mime: r.get(3)?,
            job_id: r.get(4)?,
            subject_id: r.get(5)?,
        })
    })?;
    if let Some(job) = &info.job {
        let generation: i64 = tx.query_row(SELECT_MATERIALIZATIONS_3, [op], |r| r.get(0))?;
        if row.job_id.as_deref() != Some(job.job_id.as_str()) || generation != job.generation {
            return Err(CoreError::Cancelled);
        }
        if row.stage != "COMMITTED" {
            job.check(tx)?;
        }
    } else if row.job_id.is_some() {
        return Err(CoreError::Cancelled);
    }
    if row.subject_id != info.media_id {
        return Err(CoreError::Cancelled);
    }
    Ok(row)
}

/// A commit whose media item is gone (cancel/delete never resurrects it) or whose derived
/// output is no longer allowed ends as superseded.
fn superseded(
    tx: &rusqlite::Transaction<'_>,
    op: &str,
    info: &CommitInfo,
    row: &Materialization,
) -> Result<bool, CoreError> {
    let media_exists: i64 = tx.query_row(SELECT_MEDIA_ITEMS, [&info.media_id], |r| r.get(0))?;
    if media_exists == 0 {
        tx.execute(UPDATE_MATERIALIZATIONS_2, rusqlite::params![op, now()])?;
        if let Some(job) = &row.job_id {
            tx.execute(UPDATE_JOBS, rusqlite::params![job, now()])?;
        }
        return Ok(true);
    }
    if info.source_representation == "playback-mux-v1" {
        let suppressed: bool = tx.query_row(SELECT_ENTRIES, [&info.media_id], |r| r.get(0))?;
        if suppressed || !crate::cache::derived_allowed(tx, row.size)? {
            tx.execute(UPDATE_MATERIALIZATIONS_3, [op])?;
            return Ok(true);
        }
    }
    Ok(false)
}

/// Registers the blob, replica and variant, selects the variant and queues the events.
fn record_variant(
    tx: &rusqlite::Transaction<'_>,
    op: &str,
    info: &CommitInfo,
    measured: Option<&serde_json::Value>,
    row: &Materialization,
) -> Result<(), CoreError> {
    let blob_id = format!("sha256:{}", row.blob_sha);
    let ts = now();
    tx.execute(
        INSERT_BLOBS,
        rusqlite::params![blob_id, row.size, row.mime, ts],
    )?;
    // replica：对象已通过安装校验 → verified。
    let replica_id = format!("repl-{}", uuid::Uuid::new_v4());
    tx.execute(
        INSERT_REPLICAS,
        rusqlite::params![replica_id, blob_id, row.blob_sha, ts],
    )?;
    let variant_id = format!("var-{}", uuid::Uuid::new_v4());
    tx.execute(
        INSERT_MEDIA_VARIANTS,
        rusqlite::params![
            variant_id,
            info.media_id,
            info.source_representation,
            blob_id,
            info.claimed_spec.to_string(),
            measured.map(|m| m.to_string()),
            ts
        ],
    )?;
    crate::acquisitions::commit(
        tx,
        info.job.as_ref(),
        &info.media_id,
        &variant_id,
        measured.unwrap_or(&serde_json::Value::Null),
    )?;
    select_variant(tx, info, &variant_id, &ts)?;
    tx.execute(
        UPDATE_MATERIALIZATIONS_4,
        rusqlite::params![op, blob_id, ts],
    )?;
    outbox_emit(
        tx,
        "media.variant.added",
        &info.media_id,
        &serde_json::json!({
            "media_id": info.media_id,
            "variant_id": variant_id,
            "blob_id": blob_id,
            "size": row.size,
        }),
    )?;
    outbox_emit(
        tx,
        "entry.media_changed",
        &row.subject_id,
        &serde_json::json!({"media_id": info.media_id}),
    )
}

/// An upgrade candidate waits for approval; any other first variant is selected at once.
fn select_variant(
    tx: &rusqlite::Transaction<'_>,
    info: &CommitInfo,
    variant_id: &str,
    ts: &str,
) -> Result<(), CoreError> {
    let candidate = info
        .job
        .as_ref()
        .and_then(|job| job.payload["upgrade_candidate_id"].as_str());
    if let Some(candidate) = candidate {
        tx.execute(UPDATE_MEDIA_VARIANTS, [variant_id])?;
        crate::upgrades::variant(tx, candidate, &info.media_id, variant_id)?;
    } else {
        tx.execute(
            INSERT_ASSET_SELECTIONS,
            rusqlite::params![info.media_id, variant_id, ts],
        )?;
    }
    Ok(())
}
