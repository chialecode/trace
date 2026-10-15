//! Audio cards use the documented web song identity and stream envelope.
use crate::{AdapterState, api};
use serde_json::{Value, json};
use std::time::{Duration, Instant};
use trace_contracts::{
    content::{Block, Document},
    plugin::*,
};
use trace_domain::errors::FailureClass;
use trace_site_support::{candidate, error, id, resource};

pub fn observe(
    state: &AdapterState,
    key: &str,
    deadline: Duration,
    started: Instant,
) -> Result<(ObservedDetail, Vec<MediaCandidate>), PluginError> {
    if key.is_empty() || !key.bytes().all(|b| b.is_ascii_digit()) {
        return Err(error(
            FailureClass::InvalidPayload,
            "invalid audio identity",
        ));
    }
    let fetch = |path: &str| -> Result<Value, PluginError> {
        let url = format!("https://www.bilibili.com/audio/music-service-c/web/{path}?sid={key}");
        api::envelope(&api::get_json(state, &url, true, deadline, started)?).map(|e| e.data)
    };
    let info = fetch("song/info")?;
    let stream = fetch("url")?;
    if id(&info["id"]).as_deref() != Some(key) || id(&stream["sid"]).as_deref() != Some(key) {
        return Err(error(
            FailureClass::SchemaChanged,
            "audio identity mismatch",
        ));
    }
    if stream["type"] == -1 {
        return Err(error(
            FailureClass::PermissionDenied,
            "audio preview is not a full song",
        ));
    }
    if stream["type"] != 1 {
        return Err(error(
            FailureClass::SchemaChanged,
            "unknown audio stream type",
        ));
    }
    let mut audio = resource(&format!("au:{key}"), "music", 0, true);
    audio.title = info["title"].as_str().map(str::to_owned);
    let urls = stream["cdns"]
        .as_array()
        .ok_or_else(|| error(FailureClass::SchemaChanged, "audio URLs missing"))?
        .iter()
        .filter_map(Value::as_str)
        .map(str::to_owned)
        .collect();
    let mut plan = candidate(audio.clone(), urls, "audio", json!({"source_type":1}))
        .ok_or_else(|| error(FailureClass::SchemaChanged, "audio URLs empty"))?;
    plan.expected_size = stream["size"].as_i64().filter(|n| *n > 0);
    plan.segments[0].expected_size = plan.expected_size;
    let mut resources = vec![audio];
    let mut plans = vec![plan];
    let mut blocks = vec![Block::Resource {
        attachment_id: format!("au:{key}"),
        caption: info["title"].as_str().unwrap_or("").into(),
    }];
    if let Some(raw) = info["cover"].as_str() {
        let url = if raw.starts_with("http://") {
            raw.replacen("http://", "https://", 1)
        } else {
            raw.into()
        };
        let cover = resource(&format!("au:{key}:cover"), "cover", 1, false);
        if let Some(plan) = candidate(cover.clone(), vec![url], "image", json!({})) {
            resources.push(cover);
            plans.push(plan);
        }
    }
    let body = info["intro"].as_str().unwrap_or("").to_owned();
    if !body.is_empty() {
        blocks.insert(
            0,
            Block::Text {
                text: body.clone(),
                style: "paragraph".into(),
            },
        );
    }
    Ok((
        ObservedDetail {
            native_entry_key: key.into(),
            native_kind: "music".into(),
            title: info["title"].as_str().unwrap_or("").into(),
            normalized_body: body,
            published_at: None,
            published_precision: "unknown".into(),
            access_state: "available".into(),
            completeness: "full".into(),
            document: Document {
                version: 1,
                blocks,
                omissions: vec![],
            },
            metadata: trace_contracts::content::safe_metadata(&info),
            stats: Value::Null,
            source_version_evidence: json!({"source":"audio.song.info + audio.web.url","native_id":key,"stream_type":1}),
            resources,
        },
        plans,
    ))
}
