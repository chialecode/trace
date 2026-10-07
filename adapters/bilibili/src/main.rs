//! Bilibili 平台适配器（插件协议 0.1，stdio 帧）。
//!
//! 进程模型：单 reader 线程从 stdin 分发帧（宿主请求 → 工作队列；
//! proxy.fetch 响应 → 挂起表），4 个 worker 串行处理并写 stdout。
//! 所有网络访问经宿主受限代理（proxy.fetch），Cookie 永不进入本进程。

mod api;
mod handlers;
mod wbi;

use serde_json::Value;
use std::collections::HashMap;
use std::io::{BufReader, Stdout};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};
use trace_contracts::plugin::{PluginError, PluginRequest, PluginResponse, methods};
use trace_contracts::wire;
use trace_domain::PLUGIN_PROTOCOL_VERSION;
use trace_domain::errors::FailureClass;

/// stdout 共享写端（帧级原子写入）。
pub struct OutSink(pub Mutex<Stdout>);

impl OutSink {
    pub fn send(&self, value: &impl serde::Serialize) -> Result<(), String> {
        let mut w = self.0.lock().map_err(|_| "out sink poisoned".to_string())?;
        wire::write_frame(&mut *w, value).map_err(|e| format!("write frame: {e}"))
    }
}

/// proxy.fetch 挂起表：request_id → 响应通道。
pub type PendingMap =
    HashMap<String, std::sync::mpsc::Sender<Result<api::ProxyReply, PluginError>>>;

/// 适配器共享状态：写端、proxy.fetch 挂起表、WBI 缓存、请求计数。
pub struct AdapterState {
    pub out: Arc<OutSink>,
    pub pending: Arc<Mutex<PendingMap>>,
    pub wbi: Arc<Mutex<wbi::WbiCache>>,
    counter: AtomicU64,
}

impl AdapterState {
    pub fn new_request_id(&self) -> String {
        format!(
            "adapter-{}-{}",
            std::process::id(),
            self.counter.fetch_add(1, Ordering::Relaxed)
        )
    }
}

fn main() {
    let out = Arc::new(OutSink(Mutex::new(std::io::stdout())));
    let state = Arc::new(AdapterState {
        out: out.clone(),
        pending: Arc::new(Mutex::new(HashMap::new())),
        wbi: Arc::new(Mutex::new(wbi::WbiCache::default())),
        counter: AtomicU64::new(0),
    });
    let (tx, rx) = std::sync::mpsc::channel::<PluginRequest>();
    let rx = Arc::new(Mutex::new(rx));

    // reader：stdin 帧分发（宿主请求 / 代理响应）。
    let pending = state.pending.clone();
    let reader = std::thread::Builder::new()
        .name("adapter-reader".into())
        .spawn(move || {
            let stdin = std::io::stdin();
            let mut r = BufReader::new(stdin.lock());
            while let Ok(msg) = wire::read_frame::<_, Value>(&mut r) {
                if msg.get("method").and_then(|m| m.as_str()).is_some() {
                    match serde_json::from_value::<PluginRequest>(msg) {
                        Ok(req) => {
                            if tx.send(req).is_err() {
                                break;
                            }
                        }
                        Err(_) => continue,
                    }
                } else if let Ok(resp) = serde_json::from_value::<PluginResponse>(msg)
                    && let Some(sender) = pending.lock().unwrap().remove(&resp.request_id)
                {
                    if resp.ok {
                        let _ = sender.send(Ok(api::ProxyReply {
                            result: resp.result.unwrap_or(Value::Null),
                        }));
                    } else if let Some(err) = resp.error {
                        let _ = sender.send(Err(err));
                    } else {
                        let _ = sender.send(Err(PluginError {
                            class: FailureClass::Unknown,
                            message: "proxy response without result".into(),
                            retry_after_ms: None,
                            details: Value::Null,
                        }));
                    }
                }
            }
        })
        .expect("spawn reader");

    // workers。
    let mut workers = Vec::new();
    for n in 0..4 {
        let state = state.clone();
        let rx = rx.clone();
        workers.push(
            std::thread::Builder::new()
                .name(format!("adapter-worker-{n}"))
                .spawn(move || {
                    loop {
                        let req = {
                            let guard = match rx.lock() {
                                Ok(g) => g,
                                Err(_) => return,
                            };
                            match guard.recv() {
                                Ok(req) => req,
                                Err(_) => return, // 队列关闭。
                            }
                        };
                        let started = Instant::now();
                        let deadline = Duration::from_millis(req.deadline_ms.unwrap_or(30_000));
                        let resp = if req.protocol_version != PLUGIN_PROTOCOL_VERSION {
                            PluginResponse {
                                request_id: req.request_id.clone(),
                                ok: false,
                                result: None,
                                error: Some(PluginError {
                                    class: FailureClass::Unsupported,
                                    message: format!(
                                        "protocol mismatch: {} vs {PLUGIN_PROTOCOL_VERSION}",
                                        req.protocol_version
                                    ),
                                    retry_after_ms: None,
                                    details: Value::Null,
                                }),
                            }
                        } else {
                            let (result, error) =
                                match handlers::dispatch(&state, &req, deadline, started) {
                                    Ok(v) => (Some(v), None),
                                    Err(e) => (None, Some(e)),
                                };
                            PluginResponse {
                                request_id: req.request_id.clone(),
                                ok: error.is_none(),
                                result,
                                error,
                            }
                        };
                        if let Err(e) = state.out.send(&resp) {
                            eprintln!("adapter: fatal: {e}");
                            std::process::exit(3);
                        }
                    }
                })
                .expect("spawn worker"),
        );
    }

    let _ = reader.join();
    for w in workers {
        let _ = w.join();
    }
    let _ = methods::HELLO;
}
