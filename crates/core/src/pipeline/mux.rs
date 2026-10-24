//! Optional playback derivative: video and audio copied into one MP4 without re-encoding.
//! The original tracks stay referenced and unchanged; the derivative is a rebuildable cache.
use super::acquire::uncommitted_operation;
use crate::core::Core;
use crate::error::CoreError;
use crate::jobs::ClaimedJob;
use crate::objects::{CommitInfo, ReceiveParams};
use serde_json::{Value, json};
use std::io::Read;
use std::path::Path;
use trace_domain::ids::OperationId;

const REPRESENTATION: &str = "playback-mux-v1";
const COPY_BUFFER_BYTES: usize = 256 * 1024;
/// Container overhead assumed when estimating the derivative's size.
const MUX_OVERHEAD_BYTES: i64 = 1024 * 1024;

/// A selected, verified original track of the group.
struct Track {
    media_id: String,
    blob_id: String,
}

pub(crate) async fn create_playback_mux(
    core: &Core,
    job: &ClaimedJob,
    revision: &str,
    group: &str,
) -> Result<(), CoreError> {
    if core.media_backend.ffmpeg.is_none() {
        return Ok(());
    }
    let _cache = core.cache_build.lock().await;
    if !core
        .db
        .read(|c| crate::cache::derived_allowed(c, 0))
        .await?
    {
        return Ok(());
    }
    if cache_suppressed(core, revision).await? {
        return Ok(());
    }
    let Some((video, audio)) = original_tracks(core, revision, group).await? else {
        return Ok(());
    };
    let pair = json!({"derived_from": [video.blob_id, audio.blob_id]});
    if existing_mux(core, &video.media_id, &pair).await? {
        return keep_cached(core, job, &video.media_id, &pair).await;
    }
    build(core, job, &video, &audio, pair).await
}

async fn cache_suppressed(core: &Core, revision: &str) -> Result<bool, CoreError> {
    let revision = revision.to_owned();
    core.db
        .read(move |c| {
            Ok(c.query_row(
                "SELECT e.cache_suppressed!=0 OR e.media_suppressed!=0 OR e.lifecycle='deleted' FROM entries e JOIN entry_revisions r ON r.entry_id=e.entry_id WHERE r.revision_id=?1",
                [revision],
                |r| r.get(0),
            )?)
        })
        .await
}

/// The group's selected video and audio, when both exist with verified replicas.
async fn original_tracks(
    core: &Core,
    revision: &str,
    group: &str,
) -> Result<Option<(Track, Track)>, CoreError> {
    let (revision, group) = (revision.to_owned(), group.to_owned());
    let tracks: Vec<(String, String, String)> = core
        .db
        .read(move |c| {
            Ok(c.prepare("SELECT mi.media_id,mi.role,mv.blob_id FROM media_items mi JOIN asset_selections selected ON selected.media_id=mi.media_id JOIN media_variants mv ON mv.variant_id=selected.variant_id JOIN replicas rp ON rp.blob_id=mv.blob_id AND rp.state='verified' WHERE mi.revision_id=?1 AND mi.group_key=?2 AND mi.role IN ('video','audio') AND mv.measured_spec IS NOT NULL AND mv.approval_state IN ('automatic','accepted','kept') AND mv.source_representation!='playback-mux-v1' ORDER BY mi.role DESC")?
                .query_map(rusqlite::params![revision, group], |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)))?
                .collect::<Result<_, _>>()?)
        })
        .await?;
    let pick = |role: &str| {
        tracks.iter().find(|t| t.1 == role).map(|t| Track {
            media_id: t.0.clone(),
            blob_id: t.2.clone(),
        })
    };
    Ok(pick("video").zip(pick("audio")))
}

async fn existing_mux(core: &Core, media_id: &str, pair: &Value) -> Result<bool, CoreError> {
    let (media_id, pair) = (media_id.to_owned(), pair.to_string());
    core.db
        .read(move |c| {
            Ok(c.query_row(
                "SELECT EXISTS(SELECT 1 FROM media_variants WHERE media_id=?1 AND source_representation='playback-mux-v1' AND claimed_spec=?2 AND EXISTS(SELECT 1 FROM replicas rp WHERE rp.blob_id=media_variants.blob_id AND rp.state='verified'))",
                rusqlite::params![media_id, pair],
                |r| r.get(0),
            )?)
        })
        .await
}

/// A derivative that survived a cache clear is retained again instead of rebuilt.
async fn keep_cached(
    core: &Core,
    job: &ClaimedJob,
    media_id: &str,
    pair: &Value,
) -> Result<(), CoreError> {
    let (media_id, pair, guard) = (media_id.to_owned(), pair.to_string(), job.clone());
    core.db
        .write(move |tx| {
            guard.check(tx)?;
            if crate::cache::derived_allowed(tx, 0)? {
                tx.execute(
                    "UPDATE variant_retention SET retained=1,eligible_at=NULL,reason='cache_reused' WHERE variant_id IN (SELECT variant_id FROM media_variants WHERE media_id=?1 AND source_representation='playback-mux-v1' AND claimed_spec=?2) AND reason='cache_cleared'",
                    rusqlite::params![media_id, pair],
                )?;
            }
            Ok(())
        })
        .await
}

fn operation_for(video: &Track, generation: i64, pair: &Value) -> OperationId {
    use sha2::Digest;
    let digest = sha2::Sha256::digest(pair.to_string().as_bytes())
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect::<String>();
    OperationId::new(format!(
        "mz-{}-playback-{generation}-{}",
        video.media_id,
        &digest[..16]
    ))
}

async fn derived_allowed(core: &Core, bytes: i64) -> Result<bool, CoreError> {
    core.db
        .read(move |c| crate::cache::derived_allowed(c, bytes))
        .await
}

async fn build(
    core: &Core,
    job: &ClaimedJob,
    video: &Track,
    audio: &Track,
    pair: Value,
) -> Result<(), CoreError> {
    let original = operation_for(video, job.generation, &pair);
    let op = uncommitted_operation(core, &original).await?;
    let info = CommitInfo {
        job: Some(job.clone()),
        media_id: video.media_id.clone(),
        source_representation: REPRESENTATION.into(),
        claimed_spec: pair,
    };
    if core.objects.ready(&core.db, &op, job).await? {
        core.objects.commit(&core.db, &op, &info, None).await?;
        return Ok(());
    }
    let inputs = vec![
        core.objects
            .object_path(crate::paths::blob_sha(&video.blob_id)?)?,
        core.objects
            .object_path(crate::paths::blob_sha(&audio.blob_id)?)?,
    ];
    let estimate = inputs.iter().try_fold(MUX_OVERHEAD_BYTES, |total, path| {
        std::fs::metadata(path).map(|m| total.saturating_add((m.len() as i64).saturating_mul(2)))
    })?;
    if !derived_allowed(core, estimate).await? {
        return Ok(());
    }
    core.objects.ensure_dirs()?;
    let output = core
        .objects
        .staging_dir()
        .join(format!("{}.mp4", op.as_str()));
    core.media_backend
        .mux_stream_copy(&inputs, output.clone(), &core.cancel)
        .await?;
    let size = output.metadata()?.len() as i64;
    if !derived_allowed(core, size).await? {
        std::fs::remove_file(&output)?;
        return Ok(());
    }
    let spec = core.media_backend.probe(&output, &core.cancel).await?;
    if spec.audio_tracks.unwrap_or(0) < 1 || spec.width.is_none() {
        return Err(CoreError::InvalidInput("mux missing tracks".into()));
    }
    store_output(core, job, &op, &info, &output).await?;
    core.objects
        .commit(&core.db, &op, &info, Some(serde_json::to_value(spec)?))
        .await?;
    std::fs::remove_file(output)?;
    Ok(())
}

/// Streams the muxed file into the object store under the operation.
async fn store_output(
    core: &Core,
    job: &ClaimedJob,
    op: &OperationId,
    info: &CommitInfo,
    output: &Path,
) -> Result<(), CoreError> {
    let mut writer = core
        .objects
        .start_receiving(
            &core.db,
            ReceiveParams {
                job_id: Some(job.job_id.clone()),
                subject_kind: "media_variant".into(),
                subject_id: info.media_id.clone(),
                generation: job.generation,
                mime: Some("video/mp4".into()),
                expected_size: Some(output.metadata()?.len() as i64),
                expected_sha256: None,
                source_validator: None,
            },
            op,
        )
        .await?;
    writer.reset().await?;
    let mut file = std::fs::File::open(output)?;
    let mut buffer = vec![0; COPY_BUFFER_BYTES];
    loop {
        let n = file.read(&mut buffer)?;
        if n == 0 {
            break;
        }
        writer.write_chunk(&core.db, op, &buffer[..n]).await?;
    }
    drop(file);
    let (_, sha) = core
        .objects
        .finish_and_validate(&core.db, op, writer)
        .await?;
    core.objects.install_object(&core.db, op, &sha).await?;
    Ok(())
}
