//! The offers a source preview shows and the checks that a download matches what was offered.
use crate::error::CoreError;
use crate::source_acquisition::digest;
use serde_json::{Value, json};
use trace_contracts::plugin::{LiveStream, MediaCandidate};

fn conflict() -> CoreError {
    CoreError::VersionConflict("source options changed; preview again".into())
}

pub fn offer(candidate: &MediaCandidate) -> Value {
    let spec = offer_spec(&candidate.claimed_spec);
    let mut value = json!({
        "attachment_id": candidate.resource.source_attachment_id,
        "role": candidate.resource.role,
        "group": candidate.resource.group_key,
        "required": candidate.resource.required,
        "representation": candidate.representation,
        "spec": spec,
        "kind": candidate.kind,
        "bytes": candidate.expected_size,
        "sha256": candidate.expected_sha256,
        "segments": candidate.segments.iter().map(|s|json!({"bytes":s.expected_size, "sha256":s.expected_sha256})).collect::<Vec<_>>(),
    });
    value["id"] = json!(digest(&value));
    value["title"] = json!(candidate.resource.title);
    value["position"] = json!(candidate.resource.position);
    value
}
pub fn offer_spec(claim: &Value) -> Value {
    // Safe metadata normalizes numeric *_id fields to strings. Preview and
    // execution must fingerprint that same representation, including DASH IDs.
    let claim = trace_contracts::content::safe_metadata(claim);
    let mut spec = crate::pipeline::acquire::acquisition_spec(&claim);
    for key in ["bandwidth", "quality_id", "container"] {
        if let Some(v) = claim.get(key).filter(|v| !v.is_null()) {
            spec[key] = v.clone();
        }
    }
    spec
}
pub fn live_offer(stream: &LiveStream) -> Value {
    let mut value = json!({"role":"live","attachment_id":"live","required":true,"protocol":stream.protocol,"quality":stream.quality,"audio_only":stream.audio_only});
    value["id"] = json!(digest(&value));
    value
}
pub fn live_meets_minimum(measured: &Value) -> bool {
    measured["width"]
        .as_u64()
        .zip(measured["height"].as_u64())
        .is_some_and(|(w, h)| w.min(h) >= 720)
        && measured["codec"].as_str().is_some()
}
pub fn validate_measured(candidate: &MediaCandidate, measured: &Value) -> Result<(), CoreError> {
    if !matches!(
        candidate.resource.role.as_str(),
        "video" | "video_segment" | "audio" | "motion"
    ) {
        return Ok(());
    }
    let claim = &candidate.claimed_spec;
    if matches!(
        candidate.resource.role.as_str(),
        "video" | "video_segment" | "motion"
    ) {
        for key in ["width", "height"] {
            if let Some(wanted) = claim[key].as_u64().filter(|n| *n > 0)
                && measured[key].as_u64() != Some(wanted)
            {
                return Err(conflict());
            }
        }
        if measured["codec"].as_str().is_none() {
            return Err(conflict());
        }
        let family = |codec: &str| {
            if codec.starts_with("avc") || codec == "h264" {
                "h264"
            } else if codec.starts_with("hev") || codec.starts_with("hvc") || codec == "hevc" {
                "hevc"
            } else if codec.starts_with("av01") || codec == "av1" {
                "av1"
            } else {
                "unknown"
            }
        };
        if let Some(codec) = claim["codec"].as_str().or_else(|| claim["codecs"].as_str())
            && family(codec) != "unknown"
            && family(codec) != family(measured["codec"].as_str().unwrap_or(""))
        {
            return Err(conflict());
        }
        if let Some(wanted) = trace_contracts::content::source_fps(claim)
            && measured["fps"]
                .as_f64()
                .is_none_or(|fps| fps < wanted * 0.97)
        {
            return Err(conflict());
        }
    }
    if (candidate.resource.role == "audio" || candidate.kind == "muxed" && claim["silent"] != true)
        && measured["audio_tracks"].as_u64().unwrap_or(0) == 0
    {
        return Err(conflict());
    }
    Ok(())
}
