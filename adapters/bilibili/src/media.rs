//! 媒体候选：playurl → 清晰度/音轨候选、静音与混流证据。

use crate::detail::{player_info, player_subtitles};
use crate::sign::signed_get;
use crate::{AdapterState, api, wbi};
use serde_json::{Value, json};
use std::time::{Duration, Instant};
use trace_contracts::plugin::{
    MediaCandidate, ResolveMediaParams, ResolveMediaResult, ResourceIdentityDto, UrlSet,
};
use trace_domain::errors::FailureClass;

/// Request source maxima; availability still depends on the supplied session.
const REQUESTED_QN: u64 = 127;
const FNVAL: u64 = 4048;

pub(crate) fn resolve_media(
    state: &AdapterState,
    p: &ResolveMediaParams,
    deadline: Duration,
    started: Instant,
) -> Result<ResolveMediaResult, trace_contracts::plugin::PluginError> {
    // 同页 video/audio 共享一次 playurl 调用（cid → 响应）。
    let mut cache: Vec<(String, Value)> = Vec::new();
    let mut player_cache: Vec<(String, Value)> = Vec::new();
    let mut candidates: Vec<MediaCandidate> = Vec::new();
    let mut notes = json!({
        "quality_requested": REQUESTED_QN,
        "fnval": FNVAL,
        "wbi": wbi::ALGO_VERSION,
        "silent_or_muxed": [],
        "rejected_streams": [],
    });
    for resource in &p.resources {
        if resource.role == "cover" {
            let data = signed_get(
                state,
                api::VIEW_URL,
                &[("bvid".into(), p.native_entry_key.clone())],
                true,
                deadline,
                started,
            )?;
            if let Some(pic) = data["pic"].as_str().filter(|s| !s.is_empty()) {
                candidates.push(MediaCandidate {
                    resource: resource.clone(),
                    representation: "cover-v1".into(),
                    kind: if pic.split('?').next().unwrap_or(pic).ends_with(".png") {
                        "image_png".into()
                    } else {
                        "image_jpeg".into()
                    },
                    segments: vec![url_set(pic, &Value::Null)],
                    public_headers: api::public_headers(),
                    expected_size: None,
                    expected_sha256: None,
                    claimed_spec: json!({}),
                });
            }
            continue;
        }
        let Some((bvid, cid)) = parse_sai(&resource.source_attachment_id) else {
            continue;
        };
        if resource.role == "subtitle" {
            let data = match player_cache.iter().find(|(c, _)| c == &cid) {
                Some((_, v)) => v.clone(),
                None => {
                    let v = player_info(state, &bvid, &cid, deadline, started)?;
                    player_cache.push((cid.clone(), v.clone()));
                    v
                }
            };
            candidates.extend(
                player_subtitles(&data, &bvid, &cid)
                    .1
                    .into_iter()
                    .filter(|c| c.resource.source_attachment_id == resource.source_attachment_id),
            );
            continue;
        }
        let data = match cache.iter().find(|(c, _)| c == &cid) {
            Some((_, v)) => v.clone(),
            None => {
                let params = vec![
                    ("bvid".to_string(), bvid.clone()),
                    ("cid".to_string(), cid.clone()),
                    ("qn".to_string(), REQUESTED_QN.to_string()),
                    ("fnval".to_string(), FNVAL.to_string()),
                    ("fourk".to_string(), "1".to_string()),
                ];
                let v = signed_get(state, api::PLAYURL_URL, &params, true, deadline, started)?;
                cache.push((cid.clone(), v.clone()));
                v
            }
        };
        match resource.role.as_str() {
            "video" if p.quality_hint["all_variants"] == true => {
                candidates.extend(all_video_candidates(&data, resource)?)
            }
            "audio" if p.quality_hint["all_variants"] == true => {
                let audio = all_audio_candidates(&data, resource);
                if audio.is_empty() && has_silent_or_muxed_evidence(&data) {
                    notes["silent_or_muxed"]
                        .as_array_mut()
                        .unwrap()
                        .push(json!(cid));
                }
                candidates.extend(audio);
            }
            "video" => candidates.extend(map_video_candidates_for(
                &data,
                resource,
                p.quality_hint["prefer_fps"] == true,
            )?),
            "audio" => match map_audio_candidate(&data, resource) {
                Some(c) => candidates.push(c),
                None if has_silent_or_muxed_evidence(&data) => notes["silent_or_muxed"]
                    .as_array_mut()
                    .expect("array")
                    .push(json!(cid)),
                None => {}
            },
            _ => {}
        }
    }
    Ok(ResolveMediaResult { candidates, notes })
}

// Missing candidate data alone cannot waive required audio.
fn has_silent_or_muxed_evidence(data: &Value) -> bool {
    data.get("durl")
        .and_then(Value::as_array)
        .is_some_and(|a| !a.is_empty())
        || data
            .pointer("/dash/audio")
            .and_then(Value::as_array)
            .is_some_and(|a| a.is_empty())
}

/// "{bvid}:{cid}:{page}:{kind}" → (bvid, cid)。
fn parse_sai(sai: &str) -> Option<(String, String)> {
    let parts: Vec<&str> = sai.split(':').collect();
    if parts.len() == 4 {
        Some((parts[0].to_string(), parts[1].to_string()))
    } else {
        None
    }
}

fn url_set(primary: &str, backups: &Value) -> UrlSet {
    UrlSet {
        primary: primary.to_string(),
        backups: backups
            .as_array()
            .map(|a| {
                a.iter()
                    .filter_map(|u| u.as_str())
                    .map(str::to_string)
                    .collect()
            })
            .unwrap_or_default(),
        expected_size: None,
        expected_sha256: None,
    }
}

/// 视频/混流候选：DASH 优先（AVC 优先按带宽），否则 durl 全段。
#[cfg(test)]
fn map_video_candidates(
    data: &Value,
    resource: &ResourceIdentityDto,
) -> Result<Vec<MediaCandidate>, trace_contracts::plugin::PluginError> {
    map_video_candidates_for(data, resource, false)
}

/// Builds the error of a stream mapping; the resource id travels as detail.
fn stream_error(
    resource: &ResourceIdentityDto,
    class: FailureClass,
    msg: &str,
) -> trace_contracts::plugin::PluginError {
    trace_contracts::plugin::PluginError {
        class,
        message: msg.to_string(),
        retry_after_ms: None,
        details: json!({"cid": resource.source_attachment_id}),
    }
}

fn map_video_candidates_for(
    data: &Value,
    resource: &ResourceIdentityDto,
    prefer_fps: bool,
) -> Result<Vec<MediaCandidate>, trace_contracts::plugin::PluginError> {
    if let Some(dash) = data.get("dash") {
        return dash_video_candidate(dash, resource, prefer_fps);
    }
    if let Some(durl) = data.get("durl").and_then(|d| d.as_array()) {
        return durl_candidate(data, durl, resource);
    }
    Err(stream_error(
        resource,
        FailureClass::SourceUnavailable,
        "no dash or durl streams (empty flow)",
    ))
}

fn dash_video_candidate(
    dash: &Value,
    resource: &ResourceIdentityDto,
    prefer_fps: bool,
) -> Result<Vec<MediaCandidate>, trace_contracts::plugin::PluginError> {
    let fail = |class, msg: &str| stream_error(resource, class, msg);
    let videos = dash
        .get("video")
        .and_then(|v| v.as_array())
        .ok_or_else(|| fail(FailureClass::SchemaChanged, "dash missing video array"))?;
    if videos.is_empty() {
        return Err(fail(
            FailureClass::SourceUnavailable,
            "dash video list empty",
        ));
    }
    // Source quality preference first; codec compatibility breaks ties.
    let mut streams: Vec<&Value> = videos.iter().collect();
    streams.sort_by_key(|s| {
        let codec = s.get("codecs").and_then(|c| c.as_str()).unwrap_or("");
        let avc = codec.starts_with("avc") as i64;
        let bw = s.get("bandwidth").and_then(|b| b.as_i64()).unwrap_or(0);
        (
            std::cmp::Reverse(trace_contracts::content::source_rank(s, prefer_fps)),
            std::cmp::Reverse(avc),
            std::cmp::Reverse(bw),
        )
    });
    let best = streams[0];
    let codec = best
        .get("codecs")
        .and_then(|c| c.as_str())
        .unwrap_or("unknown");
    let id = best.get("id").and_then(|i| i.as_i64()).unwrap_or(0);
    let w = best.get("width").and_then(|b| b.as_i64()).unwrap_or(0);
    let h = best.get("height").and_then(|b| b.as_i64()).unwrap_or(0);
    let base = best
        .get("base_url")
        .and_then(|u| u.as_str())
        .or_else(|| best.get("url").and_then(|u| u.as_str()))
        .unwrap_or("");
    if base.is_empty() {
        return Err(fail(
            FailureClass::SchemaChanged,
            "dash video missing base_url",
        ));
    }
    let representation = format!(
        "dash-{id}-{}-{}x{}",
        codec.split('.').next().unwrap_or("codec"),
        w,
        h
    );
    Ok(vec![MediaCandidate {
        resource: resource.clone(),
        representation,
        kind: "dash_video".into(),
        segments: vec![url_set(
            base,
            best.get("backup_url").unwrap_or(&Value::Null),
        )],
        public_headers: api::public_headers(),
        expected_size: None,
        expected_sha256: None,
        claimed_spec: json!({
            "codec": codec,
            "width": w,
            "height": h,
            "frame_rate": best.get("frame_rate"),
            "bandwidth": best.get("bandwidth"),
            "quality_id": id,
            "container": "m4s",
        }),
    }])
}

fn durl_candidate(
    data: &Value,
    durl: &[Value],
    resource: &ResourceIdentityDto,
) -> Result<Vec<MediaCandidate>, trace_contracts::plugin::PluginError> {
    let fail = |class, msg: &str| stream_error(resource, class, msg);
    if durl.is_empty() {
        return Err(fail(FailureClass::SourceUnavailable, "durl list empty"));
    }
    // durl 数组必须全段按序（不能只取 [0]）。
    let mut segments = Vec::new();
    let mut total = Some(0i64);
    for seg in durl {
        let primary = seg
            .get("url")
            .and_then(|u| u.as_str())
            .map(str::to_string)
            .or_else(|| {
                seg.get("url")
                    .and_then(|u| u.as_array())
                    .and_then(|a| a.first())
                    .and_then(|u| u.as_str())
                    .map(str::to_string)
            })
            .unwrap_or_default();
        if primary.is_empty() {
            return Err(fail(
                FailureClass::SchemaChanged,
                "durl segment missing url",
            ));
        }
        let size = seg.get("size").and_then(|s| s.as_i64()).filter(|n| *n > 0);
        total = total.zip(size).and_then(|(sum, n)| sum.checked_add(n));
        let mut urls = url_set(&primary, seg.get("backup_url").unwrap_or(&Value::Null));
        urls.expected_size = size;
        segments.push(urls);
    }
    let qn = data.get("quality").and_then(|q| q.as_i64()).unwrap_or(0);
    Ok(vec![MediaCandidate {
        resource: resource.clone(),
        representation: format!("durl-qn{qn}-segments{len}", len = segments.len()),
        kind: "durl_segment".into(),
        segments,
        public_headers: api::public_headers(),
        expected_size: total,
        expected_sha256: None,
        claimed_spec: json!({"container": "flv360+", "quality": qn, "segments": durl.len()}),
    }])
}

fn all_video_candidates(
    data: &Value,
    r: &ResourceIdentityDto,
) -> Result<Vec<MediaCandidate>, trace_contracts::plugin::PluginError> {
    let Some(streams) = data.pointer("/dash/video").and_then(Value::as_array) else {
        return map_video_candidates_for(data, r, false);
    };
    if streams.len() > 128 {
        return Err(trace_site_support::error(
            FailureClass::InvalidPayload,
            "video offer budget",
        ));
    }
    let mut result = Vec::new();
    for stream in streams {
        let mut one = data.clone();
        one["dash"]["video"] = json!([stream]);
        result.extend(map_video_candidates_for(&one, r, false)?);
    }
    result.sort_by_key(|c| {
        std::cmp::Reverse(trace_contracts::content::source_rank(
            &c.claimed_spec,
            false,
        ))
    });
    Ok(result)
}
fn all_audio_candidates(data: &Value, r: &ResourceIdentityDto) -> Vec<MediaCandidate> {
    let mut result = Vec::new();
    if let Some(streams) = data.pointer("/dash/audio").and_then(Value::as_array) {
        for stream in streams.iter().take(32) {
            let mut one = data.clone();
            one["dash"]["audio"] = json!([stream]);
            if let Some(c) = map_audio_candidate(&one, r) {
                result.push(c);
            }
        }
    }
    result
}

/// 音频候选：DASH 音轨按 AAC 优先取最高带宽；无声/混流返回 None。
fn map_audio_candidate(data: &Value, resource: &ResourceIdentityDto) -> Option<MediaCandidate> {
    let audios = data.pointer("/dash/audio")?.as_array()?;
    if audios.is_empty() {
        return None;
    }
    let mut streams: Vec<&Value> = audios.iter().collect();
    streams.sort_by_key(|s| {
        let codec = s.get("codecs").and_then(|c| c.as_str()).unwrap_or("");
        let aac = codec.contains("mp4a") as i64;
        let bw = s.get("bandwidth").and_then(|b| b.as_i64()).unwrap_or(0);
        (std::cmp::Reverse(aac), std::cmp::Reverse(bw))
    });
    let best = streams[0];
    let codec = best
        .get("codecs")
        .and_then(|c| c.as_str())
        .unwrap_or("unknown");
    let id = best.get("id").and_then(|i| i.as_i64()).unwrap_or(0);
    let base = best
        .get("base_url")
        .and_then(|u| u.as_str())
        .or_else(|| best.get("url").and_then(|u| u.as_str()))?;
    if base.is_empty() {
        return None;
    }
    Some(MediaCandidate {
        resource: resource.clone(),
        representation: format!(
            "dash-audio-{id}-{}",
            codec.split('.').next().unwrap_or("codec")
        ),
        kind: "dash_audio".into(),
        segments: vec![url_set(
            base,
            best.get("backup_url").unwrap_or(&Value::Null),
        )],
        public_headers: api::public_headers(),
        expected_size: None,
        expected_sha256: None,
        claimed_spec: json!({
            "codec": codec,
            "bandwidth": best.get("bandwidth"),
            "quality_id": id,
            "container": "m4s",
        }),
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn resources_for(bvid: &str, cid: i64, page: i64) -> Vec<ResourceIdentityDto> {
        vec![
            ResourceIdentityDto {
                role: "video".into(),
                source_attachment_id: format!("{bvid}:{cid}:{page}:video"),
                position: page,
                title: None,
                group_key: Some(cid.to_string()),
                required: true,
            },
            ResourceIdentityDto {
                role: "audio".into(),
                source_attachment_id: format!("{bvid}:{cid}:{page}:audio"),
                position: page,
                title: None,
                group_key: Some(cid.to_string()),
                required: false,
            },
        ]
    }

    #[test]
    fn playurl_dash_prefers_avc_and_maps_audio() {
        let env: Value = serde_json::from_str(include_str!(
            "../../../tests/fixtures/bilibili/playurl_dash.json"
        ))
        .unwrap();
        let data = env["data"].clone();
        let res = resources_for("BV1fakeBVfake1", 1001, 1);
        let mut cands = map_video_candidates(&data, &res[0]).unwrap();
        assert_eq!(cands.len(), 1);
        let c = cands.remove(0);
        // AVC 优先于更高带宽的 HEVC（兼容优先，不承诺最高质量）。
        assert!(c.representation.starts_with("dash-80-avc1-"));
        assert_eq!(c.kind, "dash_video");
        assert_eq!(c.segments.len(), 1);
        assert!(c.segments[0].primary.starts_with("https://"));
        assert!(!c.segments[0].backups.is_empty());
        assert_eq!(c.claimed_spec["container"], "m4s");
        let audio = map_audio_candidate(&data, &res[1]).unwrap();
        assert_eq!(audio.kind, "dash_audio");
        // 同为 AAC 时取最高带宽（30280 = 132k）。
        assert!(audio.representation.starts_with("dash-audio-30280-mp4a"));
    }

    #[test]
    fn playurl_durl_requires_all_segments() {
        let env: Value = serde_json::from_str(include_str!(
            "../../../tests/fixtures/bilibili/playurl_durl.json"
        ))
        .unwrap();
        let data = env["data"].clone();
        let res = resources_for("BV1fakeBVfake3", 3001, 1);
        let mut cands = map_video_candidates(&data, &res[0]).unwrap();
        assert_eq!(cands.len(), 1);
        let c = cands.remove(0);
        assert_eq!(c.kind, "durl_segment");
        assert_eq!(c.segments.len(), 2, "durl segments must all be listed");
        assert_eq!(c.expected_size, Some(4_000_000));
        assert_eq!(
            c.segments[0].expected_size,
            data["durl"][0]["size"].as_i64()
        );
        assert_eq!(
            c.segments[1].expected_size,
            data["durl"][1]["size"].as_i64()
        );
        let mut partial = data.clone();
        partial["durl"][1]["size"] = Value::Null;
        let partial = map_video_candidates(&partial, &res[0]).unwrap().remove(0);
        assert_eq!(
            partial.expected_size, None,
            "an unknown segment makes the aggregate unknown"
        );
        assert_eq!(
            partial.segments[0].expected_size,
            c.segments[0].expected_size
        );
        assert_eq!(partial.segments[1].expected_size, None);
        // 混流：无独立音频候选。
        assert!(map_audio_candidate(&data, &res[1]).is_none());
    }

    #[test]
    fn playurl_silent_dash_yields_no_audio_candidate() {
        let env: Value = serde_json::from_str(include_str!(
            "../../../tests/fixtures/bilibili/playurl_dash_silent.json"
        ))
        .unwrap();
        let data = env["data"].clone();
        let res = resources_for("BV1fakeBVfake4", 4001, 1);
        assert!(map_audio_candidate(&data, &res[1]).is_none());
        assert!(map_video_candidates(&data, &res[0]).is_ok());
    }

    #[test]
    fn playurl_empty_flow_is_source_unavailable() {
        let data = json!({"quality": 80, "dash": {"video": [], "audio": []}});
        let res = resources_for("BV1x", 1, 1);
        let e = map_video_candidates(&data, &res[0]).unwrap_err();
        assert_eq!(e.class, FailureClass::SourceUnavailable);
    }
    #[test]
    fn audio_waiver_requires_explicit_silent_or_muxed_evidence() {
        assert!(!has_silent_or_muxed_evidence(&json!({})));
        assert!(!has_silent_or_muxed_evidence(&json!({"dash":{"video":[]}})));
        assert!(!has_silent_or_muxed_evidence(
            &json!({"dash":{"audio":[{"id":30280}]}})
        ));
        assert!(has_silent_or_muxed_evidence(&json!({"dash":{"audio":[]}})));
        assert!(has_silent_or_muxed_evidence(
            &json!({"durl":[{"url":"https://cdn.invalid/a.flv"}]})
        ));
    }
}
