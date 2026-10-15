//! Acquiring one media candidate: operation identity, download, structure checks and commit.
use crate::core::Core;
use crate::error::CoreError;
use crate::jobs::ClaimedJob;
use crate::objects::{CommitInfo, ReceiveParams};
use crate::transport::HostRules;
use serde_json::Value;
use trace_contracts::plugin::MediaCandidate;
use trace_domain::ids::OperationId;

/// Repair receipts an interrupted object may need before the budget is exhausted.
const REPAIR_BUDGET: u32 = 32;
const VERIFY_BUFFER_BYTES: usize = 256 * 1024;

/// The fields of a claimed specification that identify a representation.
pub(crate) fn acquisition_spec(v: &Value) -> Value {
    let keys = [
        "width",
        "height",
        "fps",
        "frame_rate",
        "codec",
        "codecs",
        "bit_rate",
        "bitrate",
        "quality",
        "audio_quality",
        "sample_rate",
        "channels",
    ];
    Value::Object(
        keys.into_iter()
            .filter_map(|key| {
                v.get(key)
                    .filter(|value| !value.is_null())
                    .map(|value| (key.to_owned(), value.clone()))
            })
            .collect(),
    )
}

pub(super) fn mime_for(kind: &str) -> &'static str {
    match kind {
        "image" | "cover" => "image/*",
        "audio" => "audio/*",
        "subtitle_json" => "application/vnd.trace.subtitle+json",
        "dash_audio" => "audio/mp4",
        "image_jpeg" => "image/jpeg",
        "image_png" => "image/png",
        "durl_segment" => "video/x-flv",
        _ => "video/mp4",
    }
}

/// URLs may rotate without changing an operation, but a new source specification
/// or integrity claim must never relabel an older PREPARED object on recovery.
fn acquisition_digest(candidate: &MediaCandidate) -> String {
    let identity = serde_json::json!({
        "representation": candidate.representation,
        "spec": acquisition_spec(&candidate.claimed_spec),
        "size": candidate.expected_size,
        "sha256": candidate.expected_sha256,
    });
    crate::config::short_hash(&identity.to_string())
}

pub(crate) fn acquisition_operation(
    media: &str,
    job_id: &str,
    generation: i64,
    candidate: &MediaCandidate,
) -> OperationId {
    OperationId::new(format!(
        "mz-{media}-{job_id}-{generation}-{}",
        acquisition_digest(candidate)
    ))
}

/// The operation this job owns for the candidate. Schema-5 prepared operations remain
/// resumable by their original owner; the old spelling never aliases another task's bytes.
pub(super) async fn owned_operation(
    core: &Core,
    job: &ClaimedJob,
    media: &str,
    candidate: &MediaCandidate,
) -> Result<OperationId, CoreError> {
    let modern = acquisition_operation(media, &job.job_id, job.generation, candidate);
    let legacy = OperationId::new(format!(
        "mz-{media}-{}-{}",
        job.generation,
        acquisition_digest(candidate)
    ));
    let (current, previous, owner, subject, generation) = (
        modern.as_str().to_owned(),
        legacy.as_str().to_owned(),
        job.job_id.clone(),
        media.to_owned(),
        job.generation,
    );
    let use_legacy = core
        .db
        .read(move |c| {
            Ok(c.query_row(
                "SELECT NOT EXISTS(SELECT 1 FROM materializations WHERE operation_id=?1) AND EXISTS(SELECT 1 FROM materializations WHERE operation_id=?2 AND job_id=?3 AND subject_id=?4 AND generation=?5)",
                rusqlite::params![current, previous, owner, subject, generation],
                |r| r.get::<_, bool>(0),
            )?)
        })
        .await?;
    Ok(if use_legacy { legacy } else { modern })
}

/// A repaired/rebuilt object gets a new receipt while preserving committed history.
pub(super) async fn uncommitted_operation(
    core: &Core,
    original: &OperationId,
) -> Result<OperationId, CoreError> {
    let mut operation = original.clone();
    for repair in 1..=REPAIR_BUDGET {
        let id = operation.as_str().to_owned();
        let committed: bool = core
            .db
            .read(move |c| {
                Ok(c.query_row(
                    "SELECT EXISTS(SELECT 1 FROM materializations WHERE operation_id=?1 AND stage='COMMITTED')",
                    [id],
                    |r| r.get(0),
                )?)
            })
            .await?;
        if !committed {
            return Ok(operation);
        }
        operation = OperationId::new(format!("{}-repair-{repair}", original.as_str()));
    }
    Err(CoreError::InvalidInput(
        "materialization repair budget".into(),
    ))
}

/// Empties the materialization so the next try writes from the first byte.
async fn restart(
    core: &Core,
    writer: &mut crate::objects::StagedWriter,
    op_id: &OperationId,
) -> Result<(), CoreError> {
    writer.reset().await?;
    let op = op_id.as_str().to_owned();
    core.db
        .write(move |tx| {
            tx.execute(
                "UPDATE materializations SET received_bytes=0 WHERE operation_id=?1",
                [op],
            )?;
            Ok(())
        })
        .await
}

/// Takes the video from the media tool when the site refused the address its page gave: the
/// tool downloads the file into a directory of its own and it is received here like any other.
/// The candidate it becomes promises no size or digest (the tool picks the rendition) and says
/// where it came from. `None` when the tool is not there to be asked.
pub(super) async fn download_by_tool(
    core: &Core,
    job: &ClaimedJob,
    (platform, native_key): (&str, &str),
    candidate: &MediaCandidate,
    media_id: &str,
) -> Result<Option<(OperationId, MediaCandidate, String)>, CoreError> {
    use tokio::io::AsyncReadExt;
    let Some(fetched) = crate::media_tools::fetch_replacement(
        core,
        platform,
        native_key,
        candidate.expected_size,
        job.payload["acquisition_context_id"].as_str(),
    )
    .await?
    else {
        return Ok(None);
    };
    let mut by_tool = candidate.clone();
    by_tool.representation = format!("{}+media-tool", candidate.representation);
    by_tool.expected_size = None;
    by_tool.expected_sha256 = None;
    for segment in &mut by_tool.segments {
        segment.expected_size = None;
        segment.expected_sha256 = None;
    }
    by_tool.claimed_spec["acquired_by"] = Value::String("media_tool".into());
    let original = owned_operation(core, job, media_id, &by_tool).await?;
    let op_id = uncommitted_operation(core, &original).await?;
    let params = ReceiveParams {
        job_id: Some(job.job_id.clone()),
        subject_kind: "media_variant".into(),
        subject_id: media_id.to_owned(),
        generation: job.generation,
        mime: Some(mime_for(&by_tool.kind).to_string()),
        expected_size: None,
        expected_sha256: None,
        source_validator: None,
    };
    let mut writer = core
        .objects
        .start_receiving(&core.db, params, &op_id)
        .await?;
    restart(core, &mut writer, &op_id).await?;
    let mut file = tokio::fs::File::open(&fetched.path).await?;
    let mut buffer = vec![0u8; VERIFY_BUFFER_BYTES];
    loop {
        let n = file.read(&mut buffer).await?;
        if n == 0 {
            break;
        }
        writer.write_chunk(&core.db, &op_id, &buffer[..n]).await?;
    }
    core.http.received_media_for(
        usize::try_from(fetched.size).unwrap_or(usize::MAX),
        job.payload["acquisition_context_id"].as_str(),
    )?;
    let (_, sha) = core
        .objects
        .finish_and_validate(&core.db, &op_id, writer)
        .await?;
    core.objects.install_object(&core.db, &op_id, &sha).await?;
    Ok(Some((op_id, by_tool, sha)))
}

/// Downloads the candidate's only segment into a fresh materialization and installs it.
/// Each URL is tried in turn; without a strong validator every try restarts from zero
/// rather than appending unproven bytes.
pub(super) async fn download(
    core: &Core,
    job: &ClaimedJob,
    rules: &HostRules,
    candidate: &MediaCandidate,
    media_id: &str,
    op_id: &OperationId,
) -> Result<String, CoreError> {
    let params = ReceiveParams {
        job_id: Some(job.job_id.clone()),
        subject_kind: "media_variant".into(),
        subject_id: media_id.to_owned(),
        generation: job.generation,
        mime: Some(mime_for(&candidate.kind).to_string()),
        expected_size: candidate.expected_size,
        expected_sha256: candidate.expected_sha256.clone(),
        source_validator: None,
    };
    let mut writer = core
        .objects
        .start_receiving(&core.db, params, op_id)
        .await?;
    let segment = &candidate.segments[0];
    let mut last_error = None;
    let mut downloaded = false;
    for url in std::iter::once(&segment.primary).chain(segment.backups.iter()) {
        restart(core, &mut writer, op_id).await?;
        let ranged = candidate
            .expected_size
            .and_then(|n| u64::try_from(n).ok())
            .filter(|n| *n > 0)
            .zip(crate::media_tools::ranged_chunk(url));
        let cookie = core.media_cookie(job, rules, url).await?;
        let received = match ranged {
            Some((total, chunk)) => {
                core.http
                    .download_media_ranged_with(
                        rules,
                        (url, cookie.as_deref()),
                        Some(&candidate.public_headers),
                        (total, chunk),
                        (&mut writer, &core.db, op_id),
                        &core.cancel,
                        job.payload["acquisition_context_id"].as_str(),
                    )
                    .await
            }
            None => core
                .http
                .download_media_with(
                    rules,
                    (url, cookie.as_deref()),
                    Some(&candidate.public_headers),
                    (0, None),
                    (&mut writer, &core.db, op_id),
                    &core.cancel,
                    candidate.expected_size,
                    job.payload["acquisition_context_id"].as_str(),
                )
                .await
                .map(|(received, _)| received),
        };
        match received {
            Ok(_) => {
                downloaded = true;
                break;
            }
            Err(e) => last_error = Some(e),
        }
    }
    if !downloaded {
        return Err(last_error.unwrap_or_else(|| CoreError::Network("no media URL".into())));
    }
    let (_, sha) = core
        .objects
        .finish_and_validate(&core.db, op_id, writer)
        .await?;
    core.objects.install_object(&core.db, op_id, &sha).await?;
    Ok(sha)
}

/// The size and digest of the ordered whole must match what the adapter promised.
pub(super) async fn verify_files(
    core: &Core,
    candidate: &MediaCandidate,
    hashes: &[String],
) -> Result<(), CoreError> {
    use sha2::{Digest, Sha256};
    use tokio::io::AsyncReadExt;
    let mut size = 0u64;
    let mut digest = Sha256::new();
    let mut buffer = vec![0u8; VERIFY_BUFFER_BYTES];
    for sha in hashes {
        let path = core.objects.object_path(sha)?;
        size = size
            .checked_add(tokio::fs::metadata(&path).await?.len())
            .ok_or_else(|| CoreError::InvalidInput("segment size overflow".into()))?;
        if candidate.expected_sha256.is_some() {
            let mut file = tokio::fs::File::open(path).await?;
            loop {
                let n = file.read(&mut buffer).await?;
                if n == 0 {
                    break;
                }
                digest.update(&buffer[..n]);
            }
        }
    }
    if candidate
        .expected_size
        .is_some_and(|n| n < 0 || n as u64 != size)
    {
        return Err(CoreError::InvalidInput(
            "segment group size mismatch".into(),
        ));
    }
    if let Some(expected) = &candidate.expected_sha256 {
        crate::paths::sha256(expected)?;
        let actual = digest
            .finalize()
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect::<String>();
        if &actual != expected {
            return Err(CoreError::InvalidInput(
                "segment group digest mismatch".into(),
            ));
        }
    }
    Ok(())
}

/// Audio and images must prove they contain what their role promises.
fn require_structure(
    candidate: &MediaCandidate,
    measured: &Result<trace_domain::entities::MediaSpec, CoreError>,
) -> Result<(), CoreError> {
    if !matches!(candidate.kind.as_str(), "audio" | "image" | "cover") {
        return Ok(());
    }
    let spec = measured
        .as_ref()
        .map_err(|_| CoreError::InvalidInput("resource structure could not be verified".into()))?;
    let valid = if candidate.kind == "audio" {
        spec.audio_tracks.is_some_and(|n| n > 0)
    } else {
        spec.width.is_some_and(|n| n > 0) && spec.height.is_some_and(|n| n > 0)
    };
    if !valid {
        return Err(CoreError::InvalidInput(
            "required resource tracks missing".into(),
        ));
    }
    Ok(())
}

/// Measures the installed file, enforces the quality and request rules, then commits it.
pub(crate) async fn commit_candidate(
    core: &Core,
    job: &ClaimedJob,
    candidate: &MediaCandidate,
    media_id: &str,
    op_id: &OperationId,
    sha: &str,
) -> Result<(), CoreError> {
    let path = core.objects.object_path(sha)?;
    let measured = core.media_backend.probe(&path, &core.cancel).await;
    require_structure(candidate, &measured)?;
    let measured_json = measured
        .as_ref()
        .ok()
        .and_then(|spec| serde_json::to_value(spec).ok());
    if job.payload["source_request_id"].is_string() {
        crate::source_acquisition::validate_measured(
            candidate,
            &measured_json.clone().unwrap_or_default(),
        )?;
    }
    crate::quality::enforce_measured(
        core,
        job,
        &candidate.resource.role,
        &measured_json.clone().unwrap_or_default(),
    )
    .await?;
    let info = CommitInfo {
        job: Some(job.clone()),
        media_id: media_id.to_owned(),
        source_representation: candidate.representation.clone(),
        claimed_spec: candidate.claimed_spec.clone(),
    };
    // A superseded commit is already represented by a newer variant; either outcome is done.
    core.objects
        .commit(&core.db, op_id, &info, measured_json)
        .await?;
    Ok(())
}
