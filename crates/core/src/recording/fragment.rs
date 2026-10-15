//! A recorded fragment from its first byte to its seal: reservation, quality admission and
//! the explicit gap rows that say what was not captured.
use super::invalid;
use crate::core::Core;
use crate::error::CoreError;
use crate::jobs::{self, ClaimedJob};
use crate::objects::{CommitInfo, ReceiveParams, StagedWriter};
use serde_json::{Value, json};
use trace_domain::ids::OperationId;

const START_RESERVE_BYTES: usize = 1024 * 1024;

/// Provenance label of a fragment: raw captures stay separate from playable media.
pub(super) fn representation(original: bool) -> &'static str {
    if original {
        "recorded-original"
    } else {
        "recorded-source"
    }
}

/// Fragment state once sealed: raw captures are provenance only (`source`).
pub(super) fn sealed_state(original: bool) -> &'static str {
    if original { "source" } else { "sealed" }
}

pub(super) struct Fragment {
    pub id: String,
    pub media: String,
    pub op: OperationId,
    pub writer: StagedWriter,
}

pub(super) fn disk_budget(core: &Core, bytes: usize) -> Result<(), CoreError> {
    core.objects.budget.check(&core.live_profile(), bytes)
}

const SOURCE_SEQUENCE_TAKEN: &str = "SELECT EXISTS(SELECT 1 FROM live_fragments WHERE session_id=?1 AND source_sequence=?2 AND state IN ('receiving','sealed','gap','source'))";
const INSERT_FRAGMENT: &str = "INSERT INTO live_fragments(fragment_id,session_id,sequence,source_sequence,media_id,state,start_ms,captured_at,retry_of) VALUES(?1,?2,?3,?4,?5,'receiving',?6,strftime('%Y-%m-%dT%H:%M:%SZ','now'),(SELECT fragment_id FROM live_fragments WHERE session_id=?2 AND source_sequence=?4 AND state='interrupted' ORDER BY sequence DESC LIMIT 1))";
const INSERT_RECORDING_ITEM: &str = "INSERT INTO media_items(media_id,revision_id,role,position,source_attachment_id,required) VALUES(?1,?2,'recording',?3,?4,1)";

/// Reserves the fragment's rows and starts receiving into it. `None` when the source
/// sequence is already taken, which makes a replay of the same segment a no-op.
pub(super) async fn begin(
    core: &Core,
    job: &ClaimedJob,
    source: String,
    mime: &str,
    start: i64,
) -> Result<Option<Fragment>, CoreError> {
    disk_budget(core, START_RESERVE_BYTES)?;
    let id = format!("frag-{}", uuid::Uuid::new_v4());
    let media = format!("med-{}", uuid::Uuid::new_v4());
    let created = reserve_rows(core, job, &id, &media, &source, start).await?;
    if !created {
        return Ok(None);
    }
    let op = OperationId::new(format!("live-{id}"));
    let params = ReceiveParams {
        job_id: Some(job.job_id.clone()),
        subject_kind: "live_fragment".into(),
        subject_id: media.clone(),
        generation: job.generation,
        mime: Some(mime.into()),
        expected_size: None,
        expected_sha256: None,
        source_validator: None,
    };
    let writer = core.objects.start_receiving(&core.db, params, &op).await?;
    Ok(Some(Fragment {
        id,
        media,
        op,
        writer,
    }))
}

async fn reserve_rows(
    core: &Core,
    job: &ClaimedJob,
    fragment: &str,
    media: &str,
    source: &str,
    start: i64,
) -> Result<bool, CoreError> {
    let (j, fragment, media, source) = (
        job.clone(),
        fragment.to_owned(),
        media.to_owned(),
        source.to_owned(),
    );
    core.db
        .write(move |tx| {
            j.check(tx)?;
            let taken: bool = tx.query_row(
                SOURCE_SEQUENCE_TAKEN,
                rusqlite::params![j.subject_id, source],
                |r| r.get(0),
            )?;
            if taken {
                return Ok(false);
            }
            let revision: String = tx.query_row(
                "SELECT r.revision_id FROM live_sessions l JOIN entry_revisions r ON r.entry_id=l.entry_id WHERE l.session_id=?1",
                [&j.subject_id],
                |r| r.get(0),
            )?;
            let sequence: i64 = tx.query_row(
                "SELECT COALESCE(MAX(sequence)+1,0) FROM live_fragments WHERE session_id=?1",
                [&j.subject_id],
                |r| r.get(0),
            )?;
            tx.execute(
                INSERT_RECORDING_ITEM,
                rusqlite::params![media, revision, sequence, fragment],
            )?;
            tx.execute(
                INSERT_FRAGMENT,
                rusqlite::params![fragment, j.subject_id, sequence, source, media, start],
            )?;
            tx.execute(
                "UPDATE live_fragments SET context_id=?2 WHERE fragment_id=?1",
                rusqlite::params![fragment, j.payload["acquisition_context_id"].as_str()],
            )?;
            Ok(true)
        })
        .await
}

/// Seals the fragment: validates and measures what was received, admits it against the
/// requested quality, commits it and marks the row. A failure after the bytes are
/// installed leaves them for guarded recovery rather than dropping them.
pub(super) async fn finish(
    core: &Core,
    job: &ClaimedJob,
    f: Fragment,
    end: i64,
    reason: Option<&str>,
    mut spec: Value,
) -> Result<(), CoreError> {
    spec["requested_quality"] =
        serde_json::to_value(crate::quality::requirement(core, job).await?)?;
    spec["direct_source"] = json!(job.payload["source_request_id"].is_string());
    spec["captured_until"] = json!(jobs::now());
    let _sealing = core.recording_recovery.lock().await;
    record_end(core, &f.id, end, reason, &spec).await?;
    let (_, sha) = core
        .objects
        .finish_and_validate(&core.db, &f.op, f.writer)
        .await?;
    core.objects.install_object(&core.db, &f.op, &sha).await?;
    let original = spec["original_capture"] == true;
    let measured = if original {
        json!({"original_capture":true,"container_validation":"not_applicable_raw_capture"})
    } else {
        validate_fragment(core, &sha).await?
    };
    if !admit_quality(core, &f.id, &f.op, &spec, &measured).await? {
        return Err(CoreError::Adapter(
            trace_domain::errors::FailureClass::CapabilityLost,
        ));
    }
    // A revoked lease leaves OBJECT_READY for guarded recovery, not a late reference.
    let info = CommitInfo {
        job: Some(job.clone()),
        media_id: f.media,
        source_representation: representation(original).into(),
        claimed_spec: spec,
    };
    core.objects
        .commit(&core.db, &f.op, &info, Some(measured))
        .await?;
    mark_sealed(core, job, &f.id, end, reason, original).await
}

async fn record_end(
    core: &Core,
    fragment: &str,
    end: i64,
    reason: Option<&str>,
    spec: &Value,
) -> Result<(), CoreError> {
    let (fragment, reason) = (fragment.to_owned(), reason.map(str::to_owned));
    let evidence = trace_contracts::content::safe_metadata(spec).to_string();
    core.db
        .write(move |tx| {
            tx.execute(
                "UPDATE live_fragments SET end_ms=?2,reason=?3,evidence=?4 WHERE fragment_id=?1 AND state='receiving'",
                rusqlite::params![fragment, end, reason, evidence],
            )?;
            Ok(())
        })
        .await
}

/// A sealed retry supersedes the interrupted fragment of the same source sequence.
async fn mark_sealed(
    core: &Core,
    job: &ClaimedJob,
    fragment: &str,
    end: i64,
    reason: Option<&str>,
    original: bool,
) -> Result<(), CoreError> {
    let (j, fragment, reason) = (job.clone(), fragment.to_owned(), reason.map(str::to_owned));
    core.db
        .write(move |tx| {
            j.check(tx)?;
            tx.execute(
                "UPDATE live_fragments SET state=?4,end_ms=?2,reason=?3 WHERE fragment_id=?1",
                rusqlite::params![fragment, end, reason, sealed_state(original)],
            )?;
            tx.execute(
                "UPDATE live_fragments SET state='superseded',reason='recovered_by_retry' WHERE session_id=?1 AND state='interrupted' AND source_sequence=(SELECT source_sequence FROM live_fragments WHERE fragment_id=?2)",
                rusqlite::params![j.subject_id, fragment],
            )?;
            tx.execute(
                "UPDATE media_items SET required=0 WHERE media_id IN (SELECT media_id FROM live_fragments WHERE session_id=?1 AND state='superseded')",
                [&j.subject_id],
            )?;
            crate::objects::outbox_emit(
                tx,
                "live.fragment.sealed",
                &j.subject_id,
                &json!({"fragment_id": fragment}),
            )?;
            Ok(())
        })
        .await
}

/// Raw captures remain separate provenance, never accepted playback media. The
/// measured derivative alone is subject to admission; rejected bytes stay in
/// quarantine with an explicit gap and cannot be promoted by crash recovery.
pub(super) async fn admit_quality(
    core: &Core,
    fragment: &str,
    op: &OperationId,
    evidence: &Value,
    measured: &Value,
) -> Result<bool, CoreError> {
    let p: trace_contracts::control::QualityRequirement =
        serde_json::from_value(evidence["requested_quality"].clone()).unwrap_or_default();
    if evidence["original_capture"] == true {
        return Ok(true);
    }
    let has_video = measured["codec"].as_str().is_some();
    let auxiliary = evidence["track"]
        .as_str()
        .is_some_and(|s| s.starts_with("audio") || s.starts_with("subtitle"));
    let matches = (evidence["direct_source"] != true
        || auxiliary
        || crate::source_acquisition::live_meets_minimum(measured))
        && (auxiliary
            || has_video && crate::quality::meets(&p, measured)
            || p.min_height.is_none() && p.min_fps.is_none());
    record_actual_quality(core, fragment, &p, measured, matches).await?;
    if !p.strict || matches {
        return Ok(true);
    }
    core.objects.quarantine(&core.db, op).await?;
    let id = fragment.to_owned();
    core.db
        .write(move |tx| {
            tx.execute(
                "UPDATE live_fragments SET state='gap',reason='strict_quality_unrecorded',discontinuity=1 WHERE fragment_id=?1",
                [&id],
            )?;
            crate::objects::outbox_emit(
                tx,
                "live.quality_gap",
                &id,
                &json!({"reason": "strict_quality_unrecorded"}),
            )?;
            Ok(())
        })
        .await?;
    Ok(false)
}

async fn record_actual_quality(
    core: &Core,
    fragment: &str,
    requested: &trace_contracts::control::QualityRequirement,
    measured: &Value,
    meets: bool,
) -> Result<(), CoreError> {
    let id = fragment.to_owned();
    let measured = measured.to_string();
    let requested = serde_json::to_value(requested)?.to_string();
    core.db
        .write(move |tx| {
            tx.execute(
                "UPDATE media_items SET acquisition_evidence=json_set(COALESCE(acquisition_evidence,'{}'),'$.requested_quality',json(?2),'$.actual_quality',json(?3),'$.below_target',?4) WHERE media_id=(SELECT media_id FROM live_fragments WHERE fragment_id=?1)",
                rusqlite::params![id, requested, measured, !meets],
            )?;
            Ok(())
        })
        .await
}

/// Records an explicit discontinuity so a hole in the recording is never silent.
pub(super) async fn gap(
    core: &Core,
    job: &ClaimedJob,
    source: String,
    reason: &str,
) -> Result<(), CoreError> {
    let (j, reason) = (job.clone(), reason.to_owned());
    core.db
        .write(move |tx| {
            j.check(tx)?;
            tx.execute(
                "INSERT OR IGNORE INTO live_fragments(fragment_id,session_id,sequence,source_sequence,state,reason,discontinuity,context_id) VALUES(?1,?2,COALESCE((SELECT MAX(sequence)+1 FROM live_fragments WHERE session_id=?2),0),?3,'gap',?4,1,?5)",
                rusqlite::params![
                    format!("gap-{}", uuid::Uuid::new_v4()),
                    j.subject_id,
                    source,
                    reason,
                    j.payload["acquisition_context_id"].as_str()
                ],
            )?;
            Ok(())
        })
        .await
}

/// The received bytes must hold a decodable stream before they count as media.
pub(super) async fn validate_fragment(core: &Core, sha: &str) -> Result<Value, CoreError> {
    let spec = core
        .media_backend
        .probe(
            &core.objects.object_path(sha)?,
            &tokio_util::sync::CancellationToken::new(),
        )
        .await?;
    if spec.codec.is_none() && spec.audio_codec.is_none() && spec.subtitle_tracks.unwrap_or(0) == 0
    {
        return Err(invalid("recording fragment has no decodable stream"));
    }
    serde_json::to_value(spec).map_err(CoreError::from)
}
