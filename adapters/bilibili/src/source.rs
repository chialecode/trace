use crate::{AdapterState, api, detail, media, sign};
use serde_json::{Value, json};
use std::time::{Duration, Instant};
use trace_contracts::{
    plugin::{PluginError, ResolveMediaParams, ResolveMediaResult},
    source::ResolvedSource,
};
use trace_domain::errors::FailureClass;
use trace_site_support::{error, id};

fn invalid() -> PluginError {
    error(
        FailureClass::InvalidPayload,
        "invalid Bilibili video or live link",
    )
}
pub fn resolve(
    s: &AdapterState,
    p: &Value,
    d: Duration,
    t: Instant,
) -> Result<ResolvedSource, PluginError> {
    let input = p["input"].as_str().ok_or_else(invalid)?;
    let mut url = url::Url::parse(input).map_err(|_| invalid())?;
    if url.scheme() != "https"
        || !url.username().is_empty()
        || url.password().is_some()
        || url.port().is_some()
    {
        return Err(invalid());
    }
    if url.host_str() == Some("b23.tv") {
        let reply = api::proxy_head(s, url.as_str(), d, t)?;
        url = url::Url::parse(&reply.final_url).map_err(|_| invalid())?;
    }
    let parts = url.path().trim_matches('/').split('/').collect::<Vec<_>>();
    if url.host_str() == Some("live.bilibili.com") && parts.len() == 1 && numeric(parts[0]) {
        return live(s, parts[0], d, t);
    }
    if !matches!(
        url.host_str(),
        Some("www.bilibili.com" | "bilibili.com" | "m.bilibili.com")
    ) || parts.len() != 2
        || parts[0] != "video"
    {
        return Err(invalid());
    }
    let key = parts[1];
    let aid = key.strip_prefix("av").filter(|s| numeric(s));
    if aid.is_none()
        && !(key.starts_with("BV")
            && key.len() == 12
            && key.bytes().all(|b| b.is_ascii_alphanumeric()))
    {
        return Err(invalid());
    }
    let data = sign::signed_get(
        s,
        api::VIEW_URL,
        &[(
            if aid.is_some() { "aid" } else { "bvid" }.into(),
            aid.unwrap_or(key).into(),
        )],
        true,
        d,
        t,
    )?;
    let bvid = data["bvid"].as_str().ok_or_else(invalid)?;
    if aid.is_none() && bvid != key || aid.is_some() && id(&data["aid"]).as_deref() != aid {
        return Err(invalid());
    }
    let mut detail = detail::map_view(&data, bvid)?;
    if detail
        .resources
        .iter()
        .filter(|r| r.role == "video")
        .count()
        > 16
    {
        return Err(error(FailureClass::Unsupported, "direct video part budget"));
    }
    let media = media::resolve_media(
        s,
        &ResolveMediaParams {
            native_entry_key: bvid.into(),
            native_kind: "video".into(),
            resources: detail.resources.clone(),
            quality_hint: json!({"all_variants":true}),
        },
        d,
        t,
    )?;
    for r in &mut detail.resources {
        if r.role == "audio"
            && media.notes["silent_or_muxed"]
                .as_array()
                .is_some_and(|a| a.iter().any(|v| v.as_str() == r.group_key.as_deref()))
        {
            r.required = false;
        }
    }
    Ok(ResolvedSource {
        kind: "video".into(),
        canonical_url: format!("https://www.bilibili.com/video/{bvid}"),
        native_account_key: id(&data["owner"]["mid"]).ok_or_else(invalid)?,
        display_name: data["owner"]["name"].as_str().map(str::to_owned),
        source_duration_seconds: data["duration"].as_f64(),
        detail: Some(detail),
        media,
        live: None,
    })
}
fn numeric(s: &str) -> bool {
    !s.is_empty() && s.len() <= 32 && s.bytes().all(|b| b.is_ascii_digit())
}
fn live(
    s: &AdapterState,
    alias: &str,
    d: Duration,
    t: Instant,
) -> Result<ResolvedSource, PluginError> {
    let info = sign::signed_get(
        s,
        "https://api.live.bilibili.com/xlive/web-room/v1/index/getInfoByRoom",
        &[
            ("room_id".into(), alias.into()),
            ("web_location".into(), "444.8".into()),
        ],
        true,
        d,
        t,
    )?;
    let room = &info["room_info"];
    let room_key = id(&room["room_id"]).ok_or_else(invalid)?;
    let account = id(&room["uid"])
        .or_else(|| id(&info["anchor_info"]["base_info"]["uid"]))
        .ok_or_else(invalid)?;
    let play = if room["live_status"] == 1 {
        sign::signed_get(
            s,
            "https://api.live.bilibili.com/xlive/web-room/v2/index/getRoomPlayInfo",
            &[
                ("room_id".into(), room_key.clone()),
                ("no_playurl".into(), "0".into()),
                ("mask".into(), "1".into()),
                ("qn".into(), "10000".into()),
                ("platform".into(), "web".into()),
                ("protocol".into(), "0,1".into()),
                ("format".into(), "0,1,2".into()),
                ("codec".into(), "0,1,2".into()),
                ("web_location".into(), "444.8".into()),
            ],
            true,
            d,
            t,
        )?
    } else {
        Value::Null
    };
    let mut snapshot = crate::live::map(room, room, &play)?;
    snapshot.metadata["display_room_key"] = json!(alias);
    snapshot.metadata["native_account_key"] = json!(account);
    // Distinguish encodings of the same quality without using expiring URLs.
    if let Some(streams) = play
        .pointer("/playurl_info/playurl/stream")
        .and_then(Value::as_array)
    {
        let mut index = 0;
        for stream in streams {
            for format in stream["format"].as_array().into_iter().flatten() {
                for codec in format["codec"].as_array().into_iter().flatten() {
                    if !matches!(format["format_name"].as_str(), Some("flv" | "ts" | "fmp4")) {
                        continue;
                    }
                    for _ in codec["url_info"].as_array().into_iter().flatten() {
                        if let Some(target) = snapshot.streams.get_mut(index) {
                            target.quality = format!(
                                "{}-{}",
                                target.quality,
                                codec["codec_name"].as_str().unwrap_or("unknown")
                            );
                            target.public_headers = json!({"referer":"https://live.bilibili.com/","user-agent":api::UA});
                        }
                        index += 1;
                    }
                }
            }
        }
    }
    Ok(ResolvedSource {
        kind: "live".into(),
        canonical_url: format!("https://live.bilibili.com/{room_key}"),
        native_account_key: account,
        display_name: info["anchor_info"]["base_info"]["uname"]
            .as_str()
            .map(str::to_owned),
        source_duration_seconds: None,
        detail: None,
        media: ResolveMediaResult {
            candidates: vec![],
            notes: json!({}),
        },
        live: Some(snapshot),
    })
}
