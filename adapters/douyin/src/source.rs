use serde_json::{Value, json};
use trace_contracts::{
    plugin::{PluginError, ResolveMediaResult},
    source::ResolvedSource,
};
use trace_domain::errors::FailureClass;
use trace_site_support::{error, id, runtime::Client};
fn invalid() -> PluginError {
    error(
        FailureClass::InvalidPayload,
        "invalid Douyin video or live link",
    )
}
fn numeric(s: &str) -> bool {
    !s.is_empty() && s.len() <= 32 && s.bytes().all(|b| b.is_ascii_digit())
}
pub fn resolve(client: &Client, p: &Value) -> Result<ResolvedSource, PluginError> {
    let mut url =
        url::Url::parse(p["input"].as_str().ok_or_else(invalid)?).map_err(|_| invalid())?;
    if url.scheme() != "https"
        || !url.username().is_empty()
        || url.password().is_some()
        || url.port().is_some()
    {
        return Err(invalid());
    }
    if url.host_str() == Some("v.douyin.com") {
        let response = client.fetch(
            "HEAD",
            url.as_str(),
            json!({"user-agent":crate::protocol::UA}),
            false,
        )?;
        url = url::Url::parse(&response.final_url).map_err(|_| invalid())?;
    }
    let parts = url.path().trim_matches('/').split('/').collect::<Vec<_>>();
    if url.host_str() == Some("live.douyin.com") && parts.len() == 1 && numeric(parts[0]) {
        return live(client, parts[0]);
    }
    if !matches!(url.host_str(), Some("www.douyin.com" | "www.iesdouyin.com"))
        || parts.len() != 2
        || parts[0] != "video"
        || !numeric(parts[1])
    {
        return Err(invalid());
    }
    let key = parts[1];
    let data = crate::protocol::api(client, "/aweme/v1/web/aweme/detail/", &[("aweme_id", key)])?;
    let value = &data["aweme_detail"];
    let (detail, _) = crate::protocol::detail_for(value, key, false)?;
    if detail.native_kind != "video" {
        return Err(error(FailureClass::Unsupported, "link is not a video"));
    }
    let candidates = crate::protocol::all_candidates(value, key)?;
    Ok(ResolvedSource {
        kind: "video".into(),
        canonical_url: format!("https://www.douyin.com/video/{key}"),
        native_account_key: value["author"]["sec_uid"]
            .as_str()
            .ok_or_else(invalid)?
            .into(),
        display_name: value["author"]["nickname"].as_str().map(str::to_owned),
        source_duration_seconds: value["video"]["duration"].as_f64().map(|n| n / 1000.0),
        detail: Some(detail),
        media: ResolveMediaResult {
            candidates,
            notes: json!({}),
        },
        live: None,
    })
}
fn live(client: &Client, alias: &str) -> Result<ResolvedSource, PluginError> {
    let mut url = url::Url::parse("https://live.douyin.com/webcast/room/web/enter/").unwrap();
    url.query_pairs_mut().extend_pairs([
        ("aid", "6383"),
        ("app_name", "douyin_web"),
        ("live_id", "1"),
        ("device_platform", "web"),
        ("language", "zh-CN"),
        ("enter_from", "web_live"),
        ("cookie_enabled", "true"),
        ("screen_width", "1920"),
        ("screen_height", "1080"),
        ("browser_language", "zh-CN"),
        ("browser_platform", "Win32"),
        ("browser_name", "Chrome"),
        ("browser_version", "130.0.0.0"),
        ("web_rid", alias),
        ("room_id_str", ""),
        ("enter_source", ""),
        ("is_need_double_stream", "false"),
    ]);
    let value = client.json_large(
        url.as_str(),
        json!({"user-agent":crate::protocol::UA,"referer":"https://live.douyin.com/"}),
        true,
    )?;
    crate::protocol::envelope(value.clone())?;
    let room = value.pointer("/data/data/0").ok_or_else(invalid)?;
    // An offline response can omit both owner and room identity. Do not turn
    // that known source state into a schema error or trust the viewer instead.
    if room["status"].as_i64() == Some(4) {
        return Err(PluginError {
            class: FailureClass::SourceUnavailable,
            message: "room is not live".into(),
            retry_after_ms: None,
            details: json!({"source_state":"offline"}),
        });
    }
    let mut snapshot = crate::live::map(room)?;
    let account = room["owner"]["sec_uid"]
        .as_str()
        .filter(|s| !s.is_empty())
        .map(str::to_owned)
        // The envelope's user can be the viewer. Only the room's owner is
        // identity evidence; a missing sec_uid falls back to that owner's uid.
        .or_else(|| id(&room["owner"]["id_str"]).filter(|s| numeric(s) && s != "0"))
        .ok_or_else(invalid)?;
    snapshot.metadata["display_room_key"] = json!(alias);
    snapshot.metadata["native_account_key"] = json!(account);
    Ok(ResolvedSource {
        kind: "live".into(),
        canonical_url: format!("https://live.douyin.com/{alias}"),
        native_account_key: account,
        display_name: room["owner"]["nickname"].as_str().map(str::to_owned),
        source_duration_seconds: None,
        detail: None,
        media: ResolveMediaResult {
            candidates: vec![],
            notes: json!({}),
        },
        live: Some(snapshot),
    })
}
