//! Duplicate and similarity views (stage plan §11.5, C14).
//!
//! Exact duplicates come from the SHA-256 identity of objects. Similarity is an explicit,
//! bounded perceptual signature: an image is decoded and reduced to an 8x8 average hash; a
//! video uses one frame from the existing FFmpeg backend. A missing decoder, a damaged file or
//! a frame the tool cannot produce is `incomparable`, never a fabricated candidate. Nothing
//! here deletes, merges, moves or rewrites an object.
use crate::core::Core;
use crate::error::CoreError;
use crate::quality::comparison;
use crate::store;
use serde_json::{Value, json};
use std::path::Path;
use std::sync::Arc;

const HASH_BITS: u32 = 64;
/// At most this many candidates are hashed for one request.
const MAX_HASHED: usize = 60;
/// Two hashes this close are shown as a candidate; the user decides what to do.
const SIMILAR_DISTANCE: u32 = 12;
pub const HASH_KIND: &str = "ahash64";
pub const HASH_ALGORITHM: &str = "trace-ahash64-v1";

fn blob_hex(blob_id: &str) -> &str {
    blob_id.strip_prefix("sha256:").unwrap_or(blob_id)
}

fn ahash64(image: &image::DynamicImage) -> String {
    let luma = image.to_luma8();
    let small = image::imageops::resize(&luma, 8, 8, image::imageops::FilterType::Triangle);
    let pixels: Vec<u8> = small.pixels().map(|p| p.0[0]).collect();
    let mean: u32 = pixels.iter().map(|p| u32::from(*p)).sum::<u32>() / HASH_BITS;
    let mut bits = 0u64;
    for (index, pixel) in pixels.iter().enumerate() {
        if u32::from(*pixel) > mean {
            bits |= 1u64 << index;
        }
    }
    format!("{bits:016x}")
}

fn decode_image(path: &Path) -> Result<image::DynamicImage, String> {
    let mut reader = image::ImageReader::open(path)
        .map_err(|e| format!("open: {e}"))?
        .with_guessed_format()
        .map_err(|e| format!("format: {e}"))?;
    let mut limits = image::Limits::default();
    limits.max_image_width = Some(8192);
    limits.max_image_height = Some(8192);
    limits.max_alloc = Some(256 * 1024 * 1024);
    reader.limits(limits);
    reader.decode().map_err(|e| format!("decode: {e}"))
}

fn video_frame(core: &Core, path: &Path) -> Result<image::DynamicImage, String> {
    let ffmpeg = core
        .media_backend
        .ffmpeg
        .as_ref()
        .ok_or_else(|| "ffmpeg unavailable".to_owned())?;
    let output = std::process::Command::new(ffmpeg)
        .args([
            "-v",
            "error",
            "-ss",
            "1",
            "-i",
            &path.to_string_lossy(),
            "-frames:v",
            "1",
            "-f",
            "image2pipe",
            "-vcodec",
            "png",
            "-",
        ])
        .output()
        .map_err(|e| format!("ffmpeg: {e}"))?;
    if !output.status.success() {
        return Err("no frame".into());
    }
    if output.stdout.len() > 8 * 1024 * 1024 {
        return Err("frame too large".into());
    }
    image::load_from_memory_with_format(&output.stdout, image::ImageFormat::Png)
        .map_err(|e| format!("decode frame: {e}"))
}

/// The signature of a blob, reusing the stored one; `Err` explains why it is incomparable.
async fn signature(core: &Core, blob_id: &str, role: &str) -> Result<String, String> {
    let cached = core
        .db
        .read({
            let blob = blob_id.to_owned();
            move |c| store::resources::signature(c, &blob, HASH_KIND)
        })
        .await
        .map_err(|e| e.to_string())?;
    if let Some(value) = cached {
        return Ok(value);
    }
    let location = core
        .db
        .read({
            let blob = blob_id.to_owned();
            move |c| store::resources::binary_path(c, &blob)
        })
        .await
        .map_err(|e| e.to_string())?
        .ok_or_else(|| "no readable copy".to_owned())?;
    let path = if location.1 {
        core.objects
            .object_path(blob_hex(blob_id))
            .map_err(|e| e.to_string())?
    } else {
        location.0
    };
    let image = if role.starts_with("video") || role == "motion" {
        video_frame(core, &path)?
    } else {
        decode_image(&path)?
    };
    let value = ahash64(&image);
    let (blob, value2) = (blob_id.to_owned(), value.clone());
    core.db
        .write(move |tx| {
            store::resources::save_signature(
                tx,
                &blob,
                HASH_KIND,
                HASH_ALGORITHM,
                &value2,
                &crate::jobs::now(),
            )
        })
        .await
        .map_err(|e| e.to_string())?;
    Ok(value)
}

fn distance(left: &str, right: &str) -> Option<u32> {
    let a = u64::from_str_radix(left, 16).ok()?;
    let b = u64::from_str_radix(right, 16).ok()?;
    Some((a ^ b).count_ones())
}

fn specs_comparable(left: Option<&Value>, right: Option<&Value>) -> Result<(), &'static str> {
    let (Some(left), Some(right)) = (left, right) else {
        return Ok(());
    };
    let parse =
        |v: &Value| serde_json::from_value::<trace_domain::entities::MediaSpec>(v.clone()).ok();
    let (Some(left), Some(right)) = (parse(left), parse(right)) else {
        return Ok(());
    };
    if let (Some(a), Some(b)) = (left.duration_ms, right.duration_ms)
        && a > 0
        && b > 0
        && (a - b).abs() > 500.max(a / 20)
    {
        return Err("duration differs");
    }
    if let (Some(aw), Some(ah), Some(bw), Some(bh)) =
        (left.width, left.height, right.width, right.height)
        && aw > 0
        && ah > 0
        && bw > 0
        && bh > 0
    {
        let ar = (aw as f64) / (ah as f64);
        let br = (bw as f64) / (bh as f64);
        if (ar - br).abs() / ar.max(br) > 0.15 {
            return Err("aspect ratio differs");
        }
    }
    if left.audio_tracks.is_some()
        && right.audio_tracks.is_some()
        && left.audio_tracks != right.audio_tracks
    {
        return Err("audio tracks differ");
    }
    Ok(())
}

/// The first image/video representation of an entry, or `None` for an entry with no media.
fn target_of(media: &[store::resources::MediaRef]) -> Option<&store::resources::MediaRef> {
    media
        .iter()
        .find(|m| m.role.starts_with("image") || m.role.starts_with("video") || m.role == "motion")
}

pub async fn similar(core: &Arc<Core>, entry_id: &str, limit: i64) -> Result<Value, CoreError> {
    let media = {
        let entry = entry_id.to_owned();
        core.db
            .read(move |c| store::resources::entry_media(c, &entry))
            .await?
    };
    let Some(target) = target_of(&media) else {
        return Ok(json!({
            "state": "incomparable",
            "reason": "the entry has no image or video representation",
            "items": [],
        }));
    };
    let target_measured = target
        .measured
        .as_deref()
        .and_then(|m| serde_json::from_str::<Value>(m).ok());
    let target_signature = match signature(core, &target.blob_id, &target.role).await {
        Ok(value) => value,
        Err(reason) => {
            return Ok(json!({
                "state": "incomparable",
                "reason": reason,
                "target": {"entry_id": target.entry_id, "media_id": target.media_id, "role": target.role},
                "items": [],
            }));
        }
    };
    let candidates = {
        let (role, entry, blob, limit) = (
            target.role.clone(),
            entry_id.to_owned(),
            target.blob_id.clone(),
            limit.clamp(1, MAX_HASHED as i64),
        );
        core.db
            .read(move |c| store::resources::candidates(c, &role, &entry, &blob, limit))
            .await?
    };
    let mut items = Vec::new();
    let mut incomparable = 0;
    for candidate in candidates {
        let measured = candidate
            .measured
            .as_deref()
            .and_then(|m| serde_json::from_str::<Value>(m).ok());
        if specs_comparable(target_measured.as_ref(), measured.as_ref()).is_err() {
            continue;
        }
        let Ok(candidate_signature) = signature(core, &candidate.blob_id, &candidate.role).await
        else {
            incomparable += 1;
            continue;
        };
        let Some(bits) = distance(&target_signature, &candidate_signature) else {
            incomparable += 1;
            continue;
        };
        if bits > SIMILAR_DISTANCE {
            continue;
        }
        items.push(json!({
            "entry_id": candidate.entry_id,
            "media_id": candidate.media_id,
            "variant_id": candidate.variant_id,
            "blob_id": candidate.blob_id,
            "role": candidate.role,
            "title": candidate.entry_title,
            "namespace": candidate.namespace,
            "published_at": candidate.published_at,
            "distance": bits,
            "score": 1.0 - f64::from(bits) / f64::from(HASH_BITS),
            "signature": HASH_KIND,
        }));
    }
    Ok(json!({
        "state": "ok",
        "algorithm": HASH_ALGORITHM,
        "target": {
            "entry_id": target.entry_id,
            "media_id": target.media_id,
            "role": target.role,
            "signature": target_signature,
        },
        "incomparable": incomparable,
        "items": items,
    }))
}

pub async fn compare(core: &Arc<Core>, left: &str, right: &str) -> Result<Value, CoreError> {
    if left == right {
        return Err(CoreError::InvalidInput(
            "compare two different entries".into(),
        ));
    }
    let (left_side, right_side) = {
        let (l, r) = (left.to_owned(), right.to_owned());
        core.db
            .read(move |c| {
                Ok((
                    store::resources::side(c, &l)?,
                    store::resources::side(c, &r)?,
                ))
            })
            .await?
    };
    let existing = {
        let (l, r) = (left.to_owned(), right.to_owned());
        core.db
            .read(move |c| store::resources::decision(c, "entry", &l, "entry", &r))
            .await?
    };
    let (prefer_fps, _) = crate::quality::policy(core).await?;
    let old = left_side["media"][0]["measured"].clone();
    let new = right_side["media"][0]["measured"].clone();
    let quality = comparison(&old, &new, prefer_fps);
    let left_blob = left_side["media"][0]["blob_id"].clone();
    let exact = !left_blob.is_null() && left_blob == right_side["media"][0]["blob_id"];
    Ok(json!({
        "left": left_side,
        "right": right_side,
        "exact_duplicate": exact,
        "quality": quality,
        "decision": existing,
    }))
}

pub async fn decide(
    core: &Arc<Core>,
    left: &str,
    right: &str,
    decision: &str,
) -> Result<Value, CoreError> {
    if !matches!(
        decision,
        "not_duplicate" | "keep_both" | "different_versions"
    ) {
        return Err(CoreError::InvalidInput("comparison decision".into()));
    }
    let (l, r, d) = (left.to_owned(), right.to_owned(), decision.to_owned());
    let record = core
        .db
        .write(move |tx| {
            // Both sides must exist; a decision is never recorded against a missing entry.
            let exists = |entry: &str| -> Result<bool, CoreError> {
                Ok(tx.query_row(
                    "SELECT EXISTS(SELECT 1 FROM entries WHERE entry_id=?1 AND lifecycle!='deleted')",
                    [entry],
                    |r| r.get(0),
                )?)
            };
            if !exists(&l)? || !exists(&r)? {
                return Err(CoreError::NotFound("entry".into()));
            }
            let id = format!("cmp-{}", uuid::Uuid::new_v4());
            let now = crate::jobs::now();
            store::resources::save_decision(
                tx,
                &id,
                "entry",
                &l,
                "entry",
                &r,
                &d,
                &json!({"scope": "user"}),
                &now,
            )?;
            store::management::record(
                tx,
                "comparison_decide",
                &id,
                &json!({"left": l, "right": r, "decision": d}),
                &now,
            )?;
            Ok(json!({"comparison_id": id, "left": l, "right": r, "decision": d}))
        })
        .await?;
    Ok(record)
}
