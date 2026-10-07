//! 合成异构平台适配器（测试专用，生产不打包）。
//!
//! 与 Bilibili 刻意不同形：数字条目键、游标分页、muxed mp4、
//! 字段名不同——证明核心领域/契约不绑定单站点形状。
//! 数据源为 e2e 本地 HTTP 服务器，地址经 FAKE_BASE_URL 注入。

use serde_json::{Value, json};
use std::io::{BufReader, Stdout};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex};
use trace_contracts::plugin::{
    AdapterHello, AdapterLimits, EnumeratedEntry, EnumeratedPage, MediaCandidate, PluginError,
    PluginRequest, PluginResponse, ProxyFetchParams, ProxyFetchResult, ResolveMediaResult,
    ResourceIdentityDto, UrlSet, methods,
};
use trace_contracts::wire;
use trace_domain::PLUGIN_PROTOCOL_VERSION;
use trace_domain::errors::FailureClass;

struct Out(Mutex<Stdout>);

impl Out {
    fn send(&self, v: &impl serde::Serialize) {
        let mut w = self.0.lock().unwrap();
        wire::write_frame(&mut *w, v).expect("write frame");
    }
}

struct State {
    out: Arc<Out>,
    counter: AtomicU64,
}

impl State {
    fn new_id(&self) -> String {
        format!(
            "fake-{}-{}",
            std::process::id(),
            self.counter.fetch_add(1, Ordering::Relaxed)
        )
    }
}

fn plain_err(class: FailureClass, msg: String) -> PluginError {
    PluginError {
        class,
        message: msg,
        retry_after_ms: None,
        details: Value::Null,
    }
}

/// 同步 proxy.fetch：reader 由主循环持有（stdin 锁不可重入），
/// 单线程串行处理，宿主不会在处理期间并发发新请求。
fn proxy_get(
    state: &State,
    reader: &mut impl std::io::Read,
    url: &str,
    max_bytes: u32,
) -> Result<Value, PluginError> {
    let request_id = state.new_id();
    let params = ProxyFetchParams {
        method: "GET".into(),
        url: url.into(),
        headers: json!({"user-agent": "trace-fake-adapter/0.1"}),
        max_bytes,
        with_credential: true,
    };
    state.out.send(&PluginRequest {
        protocol_version: PLUGIN_PROTOCOL_VERSION.into(),
        request_id: request_id.clone(),
        deadline_ms: Some(10_000),
        method: methods::PROXY_FETCH.into(),
        params: serde_json::to_value(&params).unwrap_or_default(),
    });
    loop {
        let msg: Value = wire::read_frame(reader).map_err(|_| {
            plain_err(
                FailureClass::PluginCrash,
                "stdin closed while awaiting proxy response".into(),
            )
        })?;
        let resp: PluginResponse = serde_json::from_value(msg).map_err(|_| {
            plain_err(
                FailureClass::PluginCrash,
                "unrecognized frame during proxy wait".into(),
            )
        })?;
        if resp.request_id != request_id {
            continue;
        }
        return if resp.ok {
            Ok(resp.result.unwrap_or(Value::Null))
        } else {
            Err(resp.error.unwrap_or_else(|| {
                plain_err(FailureClass::Unknown, "proxy error without details".into())
            }))
        };
    }
}

fn get_json(
    state: &State,
    reader: &mut impl std::io::Read,
    path: &str,
) -> Result<Value, PluginError> {
    let base = std::env::var("FAKE_BASE_URL").unwrap_or_default();
    let resp = proxy_get(state, reader, &format!("{base}{path}"), 4 * 1024 * 1024)?;
    let result: ProxyFetchResult = serde_json::from_value(resp).map_err(|e| {
        plain_err(
            FailureClass::InvalidPayload,
            format!("bad proxy result: {e}"),
        )
    })?;
    if result.status != 200 {
        return Err(plain_err(
            if result.status == 404 {
                FailureClass::SourceUnavailable
            } else {
                FailureClass::TransientNetwork
            },
            format!("fake api status {}", result.status),
        ));
    }
    use base64::Engine;
    let body = base64::engine::general_purpose::STANDARD
        .decode(&result.body_b64)
        .map_err(|e| {
            plain_err(
                FailureClass::InvalidPayload,
                format!("fake body not base64: {e}"),
            )
        })?;
    serde_json::from_slice(&body).map_err(|e| {
        plain_err(
            FailureClass::SchemaChanged,
            format!("fake body not json: {e}"),
        )
    })
}

fn fake_rfc3339(epoch: i64) -> String {
    use time::format_description::well_known::Rfc3339;
    time::OffsetDateTime::from_unix_timestamp(epoch)
        .map(|t| t.format(&Rfc3339).unwrap_or_default())
        .unwrap_or_default()
}

fn poster_hash(u: &str) -> String {
    u.rsplit('/').next().unwrap_or("poster").to_string()
}

fn handle(
    state: &State,
    reader: &mut impl std::io::Read,
    req: &PluginRequest,
) -> Result<Value, PluginError> {
    match req.method.as_str() {
        methods::HELLO => serde_json::to_value(AdapterHello {
            protocol_version: PLUGIN_PROTOCOL_VERSION.into(),
            adapter_version: env!("CARGO_PKG_VERSION").into(),
            platform: "fake".into(),
            capabilities: vec![
                methods::ENUMERATE.into(),
                methods::OBSERVE.into(),
                methods::RESOLVE_MEDIA.into(),
            ],
            limits: AdapterLimits {
                max_page_items: 20,
                supports_cancel: false,
            },
        })
        .map_err(|e| {
            plain_err(
                FailureClass::InvalidPayload,
                format!("serialize hello: {e}"),
            )
        }),
        methods::ENUMERATE => {
            let cursor = req
                .params
                .get("cursor")
                .and_then(|c| c.as_str())
                .unwrap_or("");
            let page = get_json(
                state,
                reader,
                &if cursor.is_empty() {
                    "/fake/api/list".to_string()
                } else {
                    format!("/fake/api/list?cursor={cursor}")
                },
            )?;
            let entries = page
                .get("entries")
                .and_then(|e| e.as_array())
                .ok_or_else(|| {
                    plain_err(
                        FailureClass::SchemaChanged,
                        "fake list missing entries".into(),
                    )
                })?;
            let items = entries
                .iter()
                .map(|e| EnumeratedEntry {
                    native_entry_key: e["id"].as_str().unwrap_or("").to_string(),
                    native_kind: "clip".into(),
                    title: e["heading"].as_str().unwrap_or("").to_string(),
                    published_at: e["epoch"].as_i64().map(fake_rfc3339),
                    published_precision: "source".into(),
                    pinned: false,
                    summary_body: e["blurb"].as_str().map(str::to_string),
                    stats: e.get("stats").cloned().unwrap_or(Value::Null),
                })
                .collect();
            serde_json::to_value(EnumeratedPage {
                next_cursor: page["next"].as_str().map(str::to_string),
                has_more: page["next"].as_str().is_some(),
                items,
                coverage: json!({"shape": "fake-cursor"}),
            })
            .map_err(|e| plain_err(FailureClass::InvalidPayload, format!("serialize page: {e}")))
        }
        methods::OBSERVE => {
            let key = req.params["native_entry_key"].as_str().unwrap_or("");
            let d = get_json(state, reader, &format!("/fake/api/detail?id={key}"))?;
            let mut resources = Vec::new();
            if let Some(poster) = d["poster"].as_str() {
                resources.push(ResourceIdentityDto {
                    role: "poster".into(),
                    source_attachment_id: format!("{key}/poster:{}", poster_hash(poster)),
                    position: 0,
                    title: None,
                    group_key: None,
                    required: false,
                });
            }
            for part in d["parts"].as_array().unwrap_or(&Vec::new()) {
                let part_id = part["part_id"].as_str().unwrap_or("");
                resources.push(ResourceIdentityDto {
                    role: "audio".into(),
                    source_attachment_id: format!("{key}/{part_id}/audio"),
                    position: part["position"].as_i64().unwrap_or(0),
                    title: None,
                    group_key: Some(part_id.to_string()),
                    required: true,
                });
                resources.push(ResourceIdentityDto {
                    role: "video".into(),
                    source_attachment_id: format!("{key}/{part_id}"),
                    position: part["position"].as_i64().unwrap_or(0),
                    title: part["label"].as_str().map(str::to_string),
                    group_key: Some(part_id.to_string()),
                    required: true,
                });
            }
            serde_json::to_value(json!({
                "native_entry_key": key,
                "native_kind": "clip",
                "title": d["heading"],
                "normalized_body": d["body"].as_str().unwrap_or(""),
                "published_at": d["epoch"].as_i64().map(fake_rfc3339),
                "published_precision": "source",
                "access_state": "available",
                "source_version_evidence": {"fake_rev": d["rev"], "observed_at": fake_rfc3339(0)},
                "resources": resources,
                "completeness": "full",
                "stats": d["stats"],
            }))
            .map_err(|e| {
                plain_err(
                    FailureClass::InvalidPayload,
                    format!("serialize observe: {e}"),
                )
            })
        }
        methods::RESOLVE_MEDIA => {
            let key = req.params["native_entry_key"].as_str().unwrap_or("");
            let mut candidates = Vec::new();
            let mut ordered = req.params["resources"]
                .as_array()
                .cloned()
                .unwrap_or_default();
            ordered.sort_by_key(|r| r["role"].as_str() != Some("video"));
            for resource in &ordered {
                let Ok(res) = serde_json::from_value::<ResourceIdentityDto>(resource.clone())
                else {
                    continue;
                };
                if res.role != "video" && res.role != "audio" {
                    continue;
                }
                let (entry, part) = res
                    .source_attachment_id
                    .split_once('/')
                    .unwrap_or((key, "p1"));
                let stream = get_json(
                    state,
                    reader,
                    &format!("/fake/api/stream?id={entry}&part={part}"),
                )?;
                let segs = stream["segments"].as_array().cloned().unwrap_or_default();
                let mut segments = Vec::new();
                for s in &segs {
                    segments.push(UrlSet {
                        primary: s["u"].as_str().unwrap_or("").to_string(),
                        backups: vec![],
                        expected_size: s["size"].as_i64(),
                        expected_sha256: s["sha256"].as_str().map(str::to_owned),
                    });
                }
                candidates.push(MediaCandidate {
                    resource: res.clone(),
                    representation: format!("muxed-{entry}-{part}"),
                    kind: "muxed".into(),
                    segments,
                    public_headers: json!({"user-agent": "trace-fake-adapter/0.1"}),
                    expected_size: stream["total_size"].as_i64().or_else(|| {
                        segs.iter()
                            .try_fold(0i64, |sum, s| sum.checked_add(s["size"].as_i64()?))
                    }),
                    expected_sha256: stream["sha256"].as_str().map(str::to_owned),
                    claimed_spec: json!({"container": "mp4", "shape": "fake"}),
                });
            }
            serde_json::to_value(ResolveMediaResult {
                candidates,
                notes: json!({"shape": "fake"}),
            })
            .map_err(|e| {
                plain_err(
                    FailureClass::InvalidPayload,
                    format!("serialize media: {e}"),
                )
            })
        }
        methods::PROBE_AUTH => Ok(json!({"state":"valid","capability":{"synthetic":true}})),
        methods::CANCEL | methods::SHUTDOWN => Ok(json!({"ack": true})),
        other => Err(plain_err(
            FailureClass::Unsupported,
            format!("fake adapter unsupported method {other}"),
        )),
    }
}

fn main() {
    let out = Arc::new(Out(Mutex::new(std::io::stdout())));
    let state = State {
        out,
        counter: AtomicU64::new(0),
    };
    let mut reader = BufReader::new(std::io::stdin().lock());
    while let Ok(msg) = wire::read_frame::<_, Value>(&mut reader) {
        let Ok(req) = serde_json::from_value::<PluginRequest>(msg) else {
            break;
        };
        let resp = if req.protocol_version != PLUGIN_PROTOCOL_VERSION {
            PluginResponse {
                request_id: req.request_id,
                ok: false,
                result: None,
                error: Some(plain_err(
                    FailureClass::Unsupported,
                    "protocol mismatch".into(),
                )),
            }
        } else {
            match handle(&state, &mut reader, &req) {
                Ok(v) => PluginResponse {
                    request_id: req.request_id,
                    ok: true,
                    result: Some(v),
                    error: None,
                },
                Err(e) => PluginResponse {
                    request_id: req.request_id,
                    ok: false,
                    result: None,
                    error: Some(e),
                },
            }
        };
        state.out.send(&resp);
    }
}
