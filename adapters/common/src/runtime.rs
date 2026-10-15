//! Shared bounded stdio transport. Adapters have no HTTP client or vault access.
use base64::Engine;
use serde_json::Value;
use std::io::Write;
use std::{
    collections::HashMap,
    sync::{
        Arc, Mutex,
        atomic::{AtomicU64, Ordering},
    },
    time::Duration,
};
use trace_contracts::{
    plugin::{PluginError, PluginRequest, PluginResponse, ProxyFetchParams, ProxyFetchResult},
    wire,
};
use trace_domain::errors::FailureClass;

type Reply = Result<Value, PluginError>;
type Pending = HashMap<String, std::sync::mpsc::SyncSender<Reply>>;
pub struct Client {
    output: Mutex<std::io::Stdout>,
    pending: Mutex<Pending>,
    serial: AtomicU64,
}
impl Client {
    fn send(&self, v: &impl serde::Serialize) -> Result<(), PluginError> {
        let bytes = trace_contracts::plugin_chunks::encode(v)
            .map_err(|_| crate::error(FailureClass::InvalidPayload, "plugin message budget"))?;
        let mut out = self.output.lock().unwrap();
        out.write_all(&bytes)
            .and_then(|()| out.flush())
            .map_err(|_| crate::error(FailureClass::PluginCrash, "stdio write failed"))
    }
    pub fn get(
        &self,
        url: &str,
        headers: Value,
        credential: bool,
    ) -> Result<ProxyFetchResult, PluginError> {
        self.fetch("GET", url, headers, credential)
    }
    pub fn fetch(
        &self,
        method: &str,
        url: &str,
        headers: Value,
        credential: bool,
    ) -> Result<ProxyFetchResult, PluginError> {
        self.fetch_bounded(method, url, headers, credential, false)
    }
    fn fetch_bounded(
        &self,
        method: &str,
        url: &str,
        headers: Value,
        credential: bool,
        large: bool,
    ) -> Result<ProxyFetchResult, PluginError> {
        let id = format!("proxy-{}", self.serial.fetch_add(1, Ordering::Relaxed));
        let (tx, rx) = std::sync::mpsc::sync_channel(1);
        self.pending.lock().unwrap().insert(id.clone(), tx);
        let params = ProxyFetchParams {
            method: method.into(),
            url: url.into(),
            headers,
            max_bytes: if large {
                trace_contracts::plugin_chunks::METADATA_MAX
            } else {
                512 * 1024
            },
            with_credential: credential,
        };
        let req = PluginRequest {
            protocol_version: trace_domain::PLUGIN_PROTOCOL_VERSION.into(),
            request_id: id.clone(),
            deadline_ms: Some(25_000),
            method: if large {
                "proxy.fetch_large"
            } else {
                "proxy.fetch"
            }
            .into(),
            params: serde_json::to_value(params).unwrap(),
        };
        let result = self.send(&req).and_then(|()| {
            rx.recv_timeout(Duration::from_secs(25)).map_err(|_| {
                crate::error(FailureClass::TransientNetwork, "proxy deadline exceeded")
            })?
        });
        self.pending.lock().unwrap().remove(&id);
        serde_json::from_value(result?)
            .map_err(|_| crate::error(FailureClass::InvalidPayload, "invalid proxy response"))
    }
    pub fn json(&self, url: &str, headers: Value, credential: bool) -> Result<Value, PluginError> {
        let result = self.get(url, headers, credential)?;
        self.decode_json(result)
    }
    pub fn json_large(
        &self,
        url: &str,
        headers: Value,
        credential: bool,
    ) -> Result<Value, PluginError> {
        let result = self.fetch_bounded("GET", url, headers, credential, true)?;
        self.decode_json(result)
    }
    fn decode_json(&self, result: ProxyFetchResult) -> Result<Value, PluginError> {
        if result.status != 200 {
            return Err(crate::error(
                match result.status {
                    401 | 403 => FailureClass::PermissionDenied,
                    412 | 429 => FailureClass::RateLimited,
                    500..=599 => FailureClass::TransientNetwork,
                    _ => FailureClass::SourceUnavailable,
                },
                "site HTTP failure",
            ));
        }
        let bytes = base64::engine::general_purpose::STANDARD
            .decode(result.body_b64)
            .map_err(|_| crate::error(FailureClass::InvalidPayload, "invalid proxy body"))?;
        serde_json::from_slice(&bytes).map_err(|_| {
            crate::error(
                FailureClass::SchemaChanged,
                "empty or non-JSON site response",
            )
        })
    }
}

pub fn serve(dispatch: fn(&Client, &PluginRequest) -> Reply) {
    let client = Arc::new(Client {
        output: Mutex::new(std::io::stdout()),
        pending: Mutex::new(HashMap::new()),
        serial: AtomicU64::new(1),
    });
    let (tx, rx) = std::sync::mpsc::sync_channel::<PluginRequest>(32);
    let rx = Arc::new(Mutex::new(rx));
    for _ in 0..4 {
        let client = client.clone();
        let rx = rx.clone();
        std::thread::spawn(move || {
            loop {
                let Ok(req) = rx.lock().unwrap().recv() else {
                    break;
                };
                let result = if req.protocol_version != trace_domain::PLUGIN_PROTOCOL_VERSION {
                    Err(crate::error(FailureClass::Unsupported, "protocol mismatch"))
                } else {
                    dispatch(&client, &req)
                };
                let (result, error) = match result {
                    Ok(v) => (Some(v), None),
                    Err(e) => (None, Some(e)),
                };
                let _ = client.send(&PluginResponse {
                    request_id: req.request_id,
                    ok: error.is_none(),
                    result,
                    error,
                });
            }
        });
    }
    let mut input = std::io::BufReader::new(std::io::stdin());
    let mut chunks = trace_contracts::plugin_chunks::Decoder::default();
    while let Ok(value) = wire::read_frame::<_, Value>(&mut input) {
        let value = match chunks.push(value) {
            Ok(Some(v)) => v,
            Ok(None) => continue,
            Err(_) => break,
        };
        if value.get("method").is_some() {
            if let Ok(req) = serde_json::from_value::<PluginRequest>(value) {
                if matches!(req.method.as_str(), "shutdown" | "cancel") {
                    let _ = client.send(&PluginResponse {
                        request_id: req.request_id,
                        ok: true,
                        result: Some(serde_json::json!({"ack":true})),
                        error: None,
                    });
                    if req.method == "shutdown" {
                        break;
                    }
                } else if let Err(std::sync::mpsc::TrySendError::Full(req)) = tx.try_send(req) {
                    let _ = client.send(&PluginResponse {
                        request_id: req.request_id,
                        ok: false,
                        result: None,
                        error: Some(crate::error(
                            FailureClass::RateLimited,
                            "adapter queue full",
                        )),
                    });
                }
            }
        } else if let Ok(resp) = serde_json::from_value::<PluginResponse>(value)
            && let Some(tx) = client.pending.lock().unwrap().remove(&resp.request_id)
        {
            let _ = tx.send(if resp.ok {
                Ok(resp.result.unwrap_or_default())
            } else {
                Err(resp
                    .error
                    .unwrap_or_else(|| crate::error(FailureClass::Unknown, "proxy failure")))
            });
        }
    }
}
