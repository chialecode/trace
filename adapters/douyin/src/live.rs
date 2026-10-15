use serde_json::{Value, json};
use trace_contracts::plugin::{LiveSnapshot, LiveStream, PluginError};
use trace_domain::errors::FailureClass;
use trace_site_support::{error, id, runtime::Client};
fn invalid() -> PluginError {
    error(FailureClass::SchemaChanged, "invalid Douyin live response")
}
pub fn auxiliary(
    client: &Client,
    p: &Value,
) -> Result<trace_contracts::plugin::LiveAuxiliary, PluginError> {
    // This state is process-local; PluginHost starts a separate process per
    // credential scope. A visitor device ID is never treated as login evidence.
    static VISITOR: std::sync::Mutex<Option<String>> = std::sync::Mutex::new(None);
    let room = p["room_key"]
        .as_str()
        .filter(|s| !s.is_empty() && s.len() <= 32 && s.bytes().all(|b| b.is_ascii_digit()))
        .ok_or_else(invalid)?;
    let cached = VISITOR.lock().unwrap().clone();
    let visitor = if let Some(id) = cached {
        id
    } else {
        let v = super::protocol::api(client, "/aweme/v1/web/query/user/", &[])?;
        let id = id(&v["id"])
            .filter(|s| s.len() <= 32 && s.bytes().all(|b| b.is_ascii_digit()))
            .ok_or_else(|| {
                error(
                    FailureClass::AuthRequired,
                    "live visitor identity unavailable",
                )
            })?;
        *VISITOR.lock().unwrap() = Some(id.clone());
        id
    };
    let mut url = url::Url::parse("https://live.douyin.com/webcast/im/fetch/").unwrap();
    url.query_pairs_mut().extend_pairs([
        ("app_name", "douyin_web"),
        ("version_code", "180800"),
        ("device_platform", "web"),
        ("aid", "6383"),
        ("live_id", "1"),
        ("did_rule", "3"),
        ("endpoint", "live_pc"),
        ("support_wrds", "1"),
        ("identity", "audience"),
        ("need_persist_msg_count", "15"),
        ("resp_content_type", "json"),
        ("fetch_rule", "1"),
        ("last_rtt", "0"),
        ("cursor", ""),
        ("internal_ext", ""),
        ("room_id", room),
        ("user_unique_id", visitor.as_str()),
        ("host", "https://live.douyin.com"),
        ("cookie_enabled", "true"),
        ("browser_platform", "Win32"),
        ("browser_language", "zh-CN"),
        ("browser_name", "Mozilla"),
        ("browser_version", super::protocol::UA),
        ("browser_online", "true"),
    ]);
    let now = (time::OffsetDateTime::now_utc().unix_timestamp_nanos() / 1_000_000) as i64;
    let sig = crate::signature::sign_at(url.query().unwrap_or(""), super::protocol::UA, now);
    url.query_pairs_mut().append_pair("a_bogus", &sig);
    let v = client.json(
        url.as_str(),
        json!({"referer":"https://live.douyin.com/","user-agent":super::protocol::UA}),
        true,
    )?;
    auxiliary_map(&super::protocol::envelope(v)?, room)
}
pub fn auxiliary_map(
    v: &Value,
    room: &str,
) -> Result<trace_contracts::plugin::LiveAuxiliary, PluginError> {
    use trace_contracts::plugin::{LiveAuxiliary, LiveMessage};
    let rows = v["data"].as_array().ok_or_else(invalid)?;
    if rows.len() > 100 {
        return Err(invalid());
    }
    let mut messages = Vec::new();
    let mut omitted = 0;
    for r in rows {
        if id(&r["common"]["room_id"])
            .as_deref()
            .is_some_and(|key| key != room)
        {
            return Err(invalid());
        }
        let method = r["common"]["method"].as_str().unwrap_or("");
        if method != "WebcastChatMessage" {
            omitted += 1;
            continue;
        }
        let text = r["content"]
            .as_str()
            .filter(|s| s.len() <= 4096)
            .ok_or_else(invalid)?;
        let source_id = id(&r["common"]["msg_id"]).ok_or_else(invalid)?;
        messages.push(LiveMessage {
            source_id,
            author: r["user"]["nickname"].as_str().map(str::to_owned),
            text: text.into(),
            source_time: id(&r["common"]["create_time"]),
            kind: "chat".into(),
        });
    }
    Ok(LiveAuxiliary {
        messages,
        coverage: "sampled_history".into(),
        source_count: rows.len() as u32,
        omitted,
    })
}
pub fn probe(client: &Client, p: &Value) -> Result<LiveSnapshot, PluginError> {
    let key = p["native_account_key"].as_str().ok_or_else(invalid)?;
    let mut u = url::Url::parse("https://webcast.amemv.com/webcast/room/reflow/info/").unwrap();
    u.query_pairs_mut()
        .append_pair("sec_user_id", key)
        .append_pair("type_id", "0")
        .append_pair("live_id", "1")
        .append_pair("app_id", "1128");
    let v = client.json(u.as_str(), json!({"user-agent":super::protocol::UA}), true)?;
    super::protocol::envelope(v.clone())?;
    map(&v["data"]["room"])
}
pub fn map(room: &Value) -> Result<LiveSnapshot, PluginError> {
    let state = match room["status"].as_u64() {
        Some(2) => "live",
        Some(4) => "offline",
        _ => return Err(invalid()),
    };
    let key = id(&room["id_str"])
        .or_else(|| id(&room["id"]))
        .ok_or_else(invalid)?;
    // Closed rooms can retain empty or expired transport fields. They are not
    // live alternatives and must not turn a known offline state into a parser
    // or credential error.
    if state == "offline" {
        return Ok(LiveSnapshot {
            room_key: key.clone(),
            state: state.into(),
            source_session: Some(key),
            title: room["title"].as_str().unwrap_or("").into(),
            metadata: trace_contracts::content::safe_metadata(room),
            streams: vec![],
        });
    }
    let mut streams = Vec::new();
    let source = &room["stream_url"];
    for (field, protocol) in [("flv_pull_url", "flv"), ("hls_pull_url_map", "hls")] {
        if let Some(map) = source[field].as_object() {
            for (q, u) in map {
                if let Some(u) = u.as_str() {
                    streams.push(LiveStream{protocol:protocol.into(),url:u.into(),quality:q.clone(),audio_only:room["room_layout"]==1,public_headers:json!({"referer":"https://live.douyin.com/","user-agent":super::protocol::UA})});
                }
            }
        }
    }
    if let Some(u) = source["hls_pull_url"].as_str().filter(|s| !s.is_empty()) {
        streams.push(LiveStream {
            protocol: "hls".into(),
            url: u.into(),
            quality: "default".into(),
            audio_only: room["room_layout"] == 1,
            public_headers: json!({"referer":"https://live.douyin.com/"}),
        });
    }
    if let Some(encoded) = source
        .pointer("/live_core_sdk_data/pull_data/stream_data")
        .and_then(Value::as_str)
    {
        let data: Value = serde_json::from_str(encoded).map_err(|_| invalid())?;
        if let Some(map) = data["data"].as_object() {
            for (q, v) in map {
                for proto in ["flv", "hls"] {
                    if let Some(u) = v["main"][proto].as_str().filter(|s| !s.is_empty()) {
                        streams.push(LiveStream {
                            protocol: proto.into(),
                            url: u.into(),
                            quality: q.clone(),
                            audio_only: room["room_layout"] == 1 || q == "ao",
                            public_headers: json!({"referer":"https://live.douyin.com/"}),
                        });
                    }
                }
            }
        }
    }
    if state == "live" && streams.is_empty() {
        return Err(error(
            FailureClass::SourceUnavailable,
            "live room has no supported stream",
        ));
    }
    for stream in &mut streams {
        stream.url = normalize_media_url(&stream.url)?;
        stream.public_headers["user-agent"] = json!(super::protocol::UA);
    }
    Ok(LiveSnapshot {
        room_key: key.clone(),
        state: state.into(),
        source_session: Some(key),
        title: room["title"].as_str().unwrap_or("").into(),
        metadata: trace_contracts::content::safe_metadata(room),
        streams,
    })
}
/// Only the CDN family verified to serve the identical resource over TLS.
fn normalize_media_url(raw: &str) -> Result<String, PluginError> {
    let mut u = url::Url::parse(raw).map_err(|_| invalid())?;
    if u.scheme() == "http"
        && u.host_str()
            .is_some_and(|h| h == "douyinliving.com" || h.ends_with(".douyinliving.com"))
        && u.port().is_none()
    {
        u.set_scheme("https").map_err(|_| invalid())?;
    }
    if u.scheme() != "https" {
        return Err(error(
            FailureClass::Unsupported,
            "live media requires HTTPS",
        ));
    }
    Ok(u.into())
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn c14_https_normalization_is_exact_and_limited_to_verified_cdn() {
        let raw = "http://pull-flv-fixture.douyinliving.com/live/a.flv?signature=unchanged%2Bbytes";
        assert_eq!(
            normalize_media_url(raw).unwrap(),
            raw.replacen("http:", "https:", 1)
        );
        for raw in [
            "http://douyinliving.com.evil.invalid/a.flv",
            "http://127.0.0.1/a.flv",
            "http://unknown.example/a.flv",
            "http://cdn.douyinliving.com:8080/a.flv",
        ] {
            assert!(normalize_media_url(raw).is_err());
        }
    }
    #[test]
    fn c04_douyin_live_auxiliary_preserves_id_and_rejects_cross_room() {
        let data = json!({"status_code":0,"data":[{"common":{"method":"WebcastChatMessage","msg_id":"90071992547409931","room_id":"7","create_time":123},"user":{"nickname":"甲","token":"secret"},"content":"公开消息"},{"common":{"method":"WebcastGiftMessage","room_id":"7"},"content":"礼物"}],"internal_ext":"device-secret"});
        let page = auxiliary_map(&data, "7").unwrap();
        assert_eq!(page.messages.len(), 1);
        assert_eq!(page.omitted, 1);
        assert_eq!(page.messages[0].source_id, "90071992547409931");
        assert_eq!(page.coverage, "sampled_history");
        assert!(!serde_json::to_string(&page).unwrap().contains("secret"));
        assert!(auxiliary_map(&data, "8").is_err());
        assert!(auxiliary_map(&json!({"data":null}), "7").is_err());
    }
    #[test]
    fn c04_douyin_audio_live_and_ended_are_distinct() {
        let v = json!({"id_str":"90071992547409931","status":2,"room_layout":1,"stream_url":{"flv_pull_url":{"FULL_HD1":"https://cdn.douyincdn.com/1.flv?sign=secret"},"hls_pull_url":"https://cdn.douyincdn.com/1.m3u8"}});
        let r = map(&v).unwrap();
        assert_eq!(r.streams.len(), 2);
        assert!(r.streams[0].audio_only);
        assert!(!r.metadata.to_string().contains("secret"));
        assert_eq!(r.source_session.as_deref(), Some("90071992547409931"));
        assert!(map(&json!({"id":1,"status":0})).is_err());
        assert_eq!(map(&json!({"id":1,"status":4})).unwrap().state, "offline");
    }
}
