//! Blob facts the media commands need before they hand out a copy or a lease.
use crate::error::CoreError;
use rusqlite::Connection;

const VERIFIED: &str =
    "EXISTS(SELECT 1 FROM replicas r WHERE r.blob_id=b.blob_id AND r.state='verified')";

/// Media type of a blob that has a verified replica.
pub fn verified_mime(c: &Connection, blob_id: &str) -> Result<String, CoreError> {
    Ok(c.query_row(
        &format!("SELECT mime FROM blobs b WHERE blob_id=?1 AND {VERIFIED}"),
        [blob_id],
        |r| r.get(0),
    )?)
}

/// Size and media type of a blob that has a verified replica.
pub fn verified_size_and_mime(
    c: &Connection,
    blob_id: &str,
) -> Result<(i64, Option<String>), CoreError> {
    Ok(c.query_row(
        &format!("SELECT size, mime FROM blobs b WHERE blob_id=?1 AND {VERIFIED}"),
        [blob_id],
        |r| Ok((r.get(0)?, r.get(1)?)),
    )?)
}

/// One resource of a media group as recorded for a revision.
pub struct Item {
    pub media_id: String,
    pub role: String,
    pub position: i64,
    pub attachment_id: String,
    pub required: bool,
}

const GROUP_FILTER: &str =
    "revision_id=?1 AND (group_key=?2 OR (group_key IS NULL AND source_attachment_id=?2))";

pub fn group_items(c: &Connection, revision: &str, group: &str) -> Result<Vec<Item>, CoreError> {
    let mut stmt = c.prepare(&format!(
        "SELECT media_id, role, position, source_attachment_id, required FROM media_items WHERE {GROUP_FILTER}"
    ))?;
    let rows = stmt
        .query_map(rusqlite::params![revision, group], |r| {
            Ok(Item {
                media_id: r.get(0)?,
                role: r.get(1)?,
                position: r.get(2)?,
                attachment_id: r.get(3)?,
                required: r.get::<_, i64>(4)? != 0,
            })
        })?
        .collect::<Result<Vec<_>, _>>()?;
    Ok(rows)
}

/// Required resources of the group that still have no verified replica.
pub fn missing_required(c: &Connection, revision: &str, group: &str) -> Result<i64, CoreError> {
    Ok(c.query_row(
        &format!("SELECT count(*) FROM media_items mi WHERE {GROUP_FILTER} AND required=1 AND NOT EXISTS(SELECT 1 FROM media_variants mv JOIN replicas r ON r.blob_id=mv.blob_id AND r.state='verified' WHERE mv.media_id=mi.media_id)"),
        rusqlite::params![revision, group],
        |r| r.get(0),
    )?)
}

/// Remembers what quality was requested so later maintenance can judge the result.
pub fn record_requested_quality(
    c: &Connection,
    revision: &str,
    requested_json: &str,
) -> Result<(), CoreError> {
    c.execute(
        "UPDATE media_items SET acquisition_evidence=json_set(COALESCE(acquisition_evidence,'{}'),'$.requested_quality',json(?2)) WHERE revision_id=?1",
        rusqlite::params![revision, requested_json],
    )?;
    Ok(())
}

/// Explicit evidence may waive an independent audio resource of a group.
pub fn set_audio_requirement(
    c: &Connection,
    revision: &str,
    group: &str,
    required: bool,
    evidence_json: &str,
) -> Result<(), CoreError> {
    c.execute(
        "UPDATE media_items SET required=?3,acquisition_evidence=?4 WHERE revision_id=?1 AND group_key=?2 AND role='audio'",
        rusqlite::params![revision, group, required, evidence_json],
    )?;
    Ok(())
}

/// A multi-segment resource keeps its own row (no longer required) and gains one row per segment.
pub fn split_into_segments(
    c: &Connection,
    original_media: &str,
    evidence_json: &str,
    revision: &str,
    group: &str,
    segments: &[(String, String, i64)],
) -> Result<(), CoreError> {
    c.execute(
        "UPDATE media_items SET required=0,acquisition_evidence=?2 WHERE media_id=?1",
        rusqlite::params![original_media, evidence_json],
    )?;
    for (media_id, attachment_id, index) in segments {
        c.execute(
            "INSERT OR IGNORE INTO media_items(media_id,revision_id,role,position,source_attachment_id,group_key,required) VALUES(?1,?2,'video_segment',?3,?4,?5,1)",
            rusqlite::params![media_id, revision, index, attachment_id, group],
        )?;
    }
    Ok(())
}

/// A variant already stored for the resource, with whether its provenance matches this batch.
pub struct StoredVariant {
    pub variant_id: String,
    pub blob_id: String,
    pub claimed_spec: String,
    pub same_provenance: bool,
}

/// What authenticated the batch that wants the variant.
pub struct Provenance<'a> {
    pub auth_state: &'a str,
    pub credential_id: Option<&'a str>,
    pub credential_generation: Option<i64>,
    pub job_id: &'a str,
    pub job_generation: i64,
}

/// Variants of the resource a retry may reuse instead of downloading again (C07). A legacy
/// committed operation can be resumed without creating a duplicate variant; its missing
/// acquisition proof stays unknown and cannot satisfy a newly authenticated consumer.
pub fn stored_variants(
    c: &Connection,
    media_id: &str,
    representation: &str,
    by: &Provenance<'_>,
) -> Result<Vec<StoredVariant>, CoreError> {
    let proof = "EXISTS(SELECT 1 FROM resource_acquisitions a JOIN acquisition_contexts x ON x.context_id=a.context_id WHERE a.variant_id=mv.variant_id AND x.auth_state=?3 AND x.credential_id IS ?4 AND x.credential_generation IS ?5)";
    let own = "EXISTS(SELECT 1 FROM resource_acquisitions a JOIN acquisition_contexts x ON x.context_id=a.context_id WHERE a.variant_id=mv.variant_id AND x.job_id=?6 AND x.job_generation=?7 AND x.auth_state=?3 AND x.credential_id IS ?4 AND x.credential_generation IS ?5)";
    let sql = format!(
        "SELECT mv.variant_id,mv.blob_id,mv.claimed_spec,{proof} FROM media_variants mv WHERE mv.media_id=?1 AND mv.source_representation=?2 AND (mv.approval_state IN ('automatic','accepted','kept') OR (mv.approval_state='pending' AND {own})) AND NOT EXISTS(SELECT 1 FROM deleted_variants d WHERE d.variant_id=mv.variant_id) AND ({proof} OR (?3='anonymous' AND NOT EXISTS(SELECT 1 FROM resource_acquisitions a JOIN acquisition_contexts x ON x.context_id=a.context_id WHERE a.variant_id=mv.variant_id AND x.auth_state!='unknown') AND EXISTS(SELECT 1 FROM materializations mz WHERE mz.job_id=?6 AND mz.generation=?7 AND mz.subject_id=mv.media_id AND mz.blob_id=mv.blob_id AND mz.stage='COMMITTED'))) AND EXISTS(SELECT 1 FROM replicas r WHERE r.blob_id=mv.blob_id AND r.state='verified') ORDER BY mv.created_at DESC"
    );
    let rows = c
        .prepare(&sql)?
        .query_map(
            rusqlite::params![
                media_id,
                representation,
                by.auth_state,
                by.credential_id,
                by.credential_generation,
                by.job_id,
                by.job_generation
            ],
            |r| {
                Ok(StoredVariant {
                    variant_id: r.get(0)?,
                    blob_id: r.get(1)?,
                    claimed_spec: r.get(2)?,
                    same_provenance: r.get(3)?,
                })
            },
        )?
        .collect::<Result<Vec<_>, _>>()?;
    Ok(rows)
}

/// The hash of a prepared object, once its receipt says it is complete.
pub fn received_sha256(c: &Connection, operation_id: &str) -> Result<String, CoreError> {
    Ok(c.query_row(
        "SELECT received_sha256 FROM materializations WHERE operation_id=?1",
        [operation_id],
        |r| r.get(0),
    )?)
}
