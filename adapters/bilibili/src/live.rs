use crate::{AdapterState, api};
use serde_json::{Value, json};
use std::time::{Duration, Instant};
use trace_contracts::plugin::{LiveSnapshot, LiveStream, PluginError};
use trace_domain::errors::FailureClass;
use trace_site_support::{error, id};
fn invalid() -> PluginError {
    error(
        FailureClass::SchemaChanged,
        "invalid Bilibili live response",
    )
}

pub fn auxiliary(
    s: &AdapterState,
    p: &Value,
    d: Duration,
    t: Instant,
) -> Result<trace_contracts::plugin::LiveAuxiliary, PluginError> {
    let room = p["room_key"]
        .as_str()
        .filter(|s| !s.is_empty() && s.len() <= 32 && s.bytes().all(|b| b.is_ascii_digit()))
        .ok_or_else(invalid)?;
    let data = get(
        s,
        "/xlive/web-room/v1/dM/gethistory",
        &[("roomid", room)],
        d,
        t,
    )?;
    auxiliary_map(&data)
}
pub fn auxiliary_map(data: &Value) -> Result<trace_contracts::plugin::LiveAuxiliary, PluginError> {
    use trace_contracts::plugin::{LiveAuxiliary, LiveMessage};
    let mut rows = data["room"]
        .as_array()
        .ok_or_else(invalid)?
        .iter()
        .collect::<Vec<_>>();
    if let Some(admin) = data.get("admin") {
        rows.extend(admin.as_array().ok_or_else(invalid)?)
    }
    if rows.len() > 100 {
        return Err(invalid());
    }
    let mut messages = Vec::new();
    for r in &rows {
        let text = r["text"]
            .as_str()
            .filter(|s| s.len() <= 4096)
            .ok_or_else(invalid)?;
        let source_id = id(&r["id_str"])
            .or_else(|| id(&r["id"]))
            .unwrap_or_else(|| {
                use sha2::{Digest, Sha256};
                format!(
                    "history-{}",
                    Sha256::digest(
                        serde_json::to_vec(&json!([r["timeline"], r["uid"], text])).unwrap()
                    )
                    .iter()
                    .map(|b| format!("{b:02x}"))
                    .collect::<String>()
                )
            });
        messages.push(LiveMessage {
            source_id,
            author: r["nickname"].as_str().map(str::to_owned),
            text: text.into(),
            source_time: r["timeline"].as_str().map(str::to_owned),
            kind: "chat".into(),
        });
    }
    Ok(LiveAuxiliary {
        messages,
        coverage: "sampled_history".into(),
        source_count: rows.len() as u32,
        omitted: 0,
    })
}
fn get(
    s: &AdapterState,
    path: &str,
    params: &[(&str, &str)],
    d: Duration,
    t: Instant,
) -> Result<Value, PluginError> {
    let mut u = url::Url::parse(&format!("https://api.live.bilibili.com{path}")).unwrap();
    u.query_pairs_mut().extend_pairs(params.iter().copied());
    api::envelope(&api::get_json(s, u.as_str(), true, d, t)?).map(|e| e.data)
}
pub fn probe(
    s: &AdapterState,
    p: &Value,
    d: Duration,
    t: Instant,
) -> Result<LiveSnapshot, PluginError> {
    let account = p["native_account_key"].as_str().ok_or_else(invalid)?;
    let a = get(s, "/room/v1/Room/getRoomInfoOld", &[("mid", account)], d, t)?;
    if a["roomStatus"] == 0 {
        return Ok(LiveSnapshot {
            room_key: format!("account:{account}"),
            state: "offline".into(),
            source_session: None,
            title: String::new(),
            metadata: json!({"room_exists":false}),
            streams: vec![],
        });
    }
    let room = id(&a["roomid"]).ok_or_else(invalid)?;
    let room = get(s, "/room/v1/Room/room_init", &[("id", &room)], d, t)?;
    let key = id(&room["room_id"]).ok_or_else(invalid)?;
    let streams = if room["live_status"] == 1 {
        get(
            s,
            "/xlive/web-room/v2/index/getRoomPlayInfo",
            &[
                ("room_id", &key),
                ("protocol", "0,1"),
                ("format", "0,1,2"),
                ("codec", "0,1"),
                ("qn", "10000"),
                ("platform", "web"),
                ("ptype", "8"),
            ],
            d,
            t,
        )?
    } else {
        Value::Null
    };
    map(&room, &a, &streams)
}
pub fn map(room: &Value, info: &Value, play: &Value) -> Result<LiveSnapshot, PluginError> {
    let state = match room["live_status"].as_u64() {
        Some(1) => "live",
        Some(0 | 2) => "offline",
        _ => return Err(invalid()),
    };
    let mut streams = Vec::new();
    if state == "live" {
        for stream in play
            .pointer("/playurl_info/playurl/stream")
            .and_then(Value::as_array)
            .ok_or_else(invalid)?
        {
            for format in stream["format"].as_array().ok_or_else(invalid)? {
                for codec in format["codec"].as_array().ok_or_else(invalid)? {
                    for host in codec["url_info"].as_array().ok_or_else(invalid)? {
                        let protocol = match format["format_name"].as_str() {
                            Some("flv") => "flv",
                            Some("ts" | "fmp4") => "hls",
                            _ => continue,
                        };
                        streams.push(LiveStream {
                            protocol: protocol.into(),
                            url: format!(
                                "{}{}{}",
                                host["host"].as_str().ok_or_else(invalid)?,
                                codec["base_url"].as_str().ok_or_else(invalid)?,
                                host["extra"].as_str().ok_or_else(invalid)?
                            ),
                            quality: id(&codec["current_qn"]).unwrap_or_else(|| "unknown".into()),
                            audio_only: info["broadcast_type"] == 1,
                            public_headers: json!({"referer":"https://live.bilibili.com/"}),
                        });
                    }
                }
            }
        }
        if streams.is_empty() {
            return Err(error(
                FailureClass::SourceUnavailable,
                "live room has no supported stream",
            ));
        }
    }
    Ok(LiveSnapshot {
        room_key: id(&room["room_id"]).ok_or_else(invalid)?,
        state: state.into(),
        source_session: room["live_time"]
            .as_i64()
            .filter(|n| *n > 0)
            .map(|n| n.to_string()),
        title: info["title"].as_str().unwrap_or("").into(),
        metadata: trace_contracts::content::safe_metadata(&json!({"room":room,"info":info})),
        streams,
    })
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn c04_bilibili_live_auxiliary_is_bounded_history_with_stable_identity() {
        let data = json!({"room":[{"id_str":"90071992547409931","text":"普通消息","nickname":"甲","timeline":"2026-10-07 12:00:00"}],"admin":[{"text":"管理员消息","uid":2,"nickname":"乙","timeline":"2026-10-07 12:00:01"}]});
        let a = auxiliary_map(&data).unwrap();
        let b = auxiliary_map(&data).unwrap();
        assert_eq!(a.messages.len(), 2);
        assert_eq!(a.coverage, "sampled_history");
        assert_eq!(a.messages[1].source_id, b.messages[1].source_id);
        assert_eq!(a.messages[0].source_id, "90071992547409931");
        assert!(auxiliary_map(&json!({"room":null})).is_err());
    }
    #[test]
    fn c04_live_is_distinct_from_replay_unknown_and_missing_stream() {
        assert!(map(&json!({"room_id":1}), &json!({}), &Value::Null).is_err());
        assert_eq!(
            map(
                &json!({"room_id":1,"live_status":2}),
                &json!({}),
                &Value::Null
            )
            .unwrap()
            .state,
            "offline"
        );
        assert!(
            map(
                &json!({"room_id":1,"live_status":1}),
                &json!({}),
                &Value::Null
            )
            .is_err()
        );
        let p = json!({"playurl_info":{"playurl":{"stream":[{"format":[{"format_name":"flv","codec":[{"base_url":"/a.flv","current_qn":10000,"url_info":[{"host":"https://cdn.bilivideo.com","extra":"?sign=secret"}]}]}]}]}}});
        let r = map(
            &json!({"room_id":"90071992547409931","live_status":1,"live_time":123}),
            &json!({"title":"直播"}),
            &p,
        )
        .unwrap();
        assert_eq!(r.streams.len(), 1);
        assert_eq!(r.source_session.as_deref(), Some("123"));
        assert!(!r.metadata.to_string().contains("secret"));
    }
}
