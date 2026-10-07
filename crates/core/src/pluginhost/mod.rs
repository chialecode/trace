//! 插件宿主：平台适配器进程的启停、协议与取消（docs/design.md §8）。
//!
//! - 一个适配器进程服务同平台多账号，空闲退出；不按创作者常驻。
//! - 双向帧：适配器经 `proxy.fetch` 使用核心受限 HTTP 代理；Cookie 由宿主
//!   按平台解析并仅在授权域附加，永远不进入适配器进程（INV-10）。
//! - 协议帧上限 1 MiB；单页 100 条；请求带截止时间。

use crate::config::Profile;
use crate::error::CoreError;
use crate::transport::{ConstrainedClient, host_rules};
use base64::Engine as _;
use serde_json::Value;
use std::collections::HashMap;
use std::path::PathBuf;
use std::sync::Arc;
use std::time::Duration;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::sync::{Mutex, oneshot};
use tokio_util::sync::CancellationToken;
use trace_contracts::plugin::{
    AdapterHello, PluginError, PluginRequest, PluginResponse, ProxyFetchParams, ProxyFetchResult,
};
use trace_contracts::wire;
use trace_domain::states::PluginState;

/// 适配器注册信息。
#[derive(Debug, Clone)]
pub struct AdapterSpec {
    pub plugin_id: String,
    pub platform: String,
    pub binary_path: PathBuf,
}

/// 平台 → Cookie 的短期解析（宿主内部解密，仅内存）。
pub type CredentialLookup = Arc<dyn Fn(&str) -> Option<String> + Send + Sync>;

/// request_id → 等待中的适配器响应。
type PendingCalls = HashMap<String, oneshot::Sender<Result<Value, PluginError>>>;

// Unit tests can answer the production executable's metadata proxy messages.
// This type and the injection point do not exist in any shipped library build.
#[cfg(test)]
type ProxyFixture = Arc<dyn Fn(&Value) -> Result<ProxyFetchResult, PluginError> + Send + Sync>;

struct SessionInner {
    stdin: Arc<Mutex<tokio::process::ChildStdin>>,
    pending: Arc<std::sync::Mutex<PendingCalls>>,
    hello: Arc<Mutex<AdapterHello>>,
    cancel: CancellationToken,
    exited: CancellationToken,
    last_used: std::sync::Mutex<std::time::Instant>,
}

pub struct PluginHost {
    proxy_counts: Arc<[std::sync::atomic::AtomicU64; 3]>,
    profile: Profile,
    http: Arc<ConstrainedClient>,
    sessions: Mutex<HashMap<String, Arc<SessionInner>>>,
    startup: Mutex<()>,
    closed: std::sync::atomic::AtomicBool,
    credential_lookup: std::sync::RwLock<CredentialLookup>,
    specs: Vec<AdapterSpec>,
    disabled: std::sync::RwLock<std::collections::HashSet<String>>,
    #[cfg(test)]
    fixture: std::sync::RwLock<Option<ProxyFixture>>,
}

impl PluginHost {
    pub fn new(
        profile: Profile,
        http: Arc<ConstrainedClient>,
        specs: Vec<AdapterSpec>,
        credential_lookup: CredentialLookup,
    ) -> Self {
        Self {
            proxy_counts: Arc::new(Default::default()),
            profile,
            http,
            sessions: Mutex::new(HashMap::new()),
            startup: Mutex::new(()),
            closed: std::sync::atomic::AtomicBool::new(false),
            credential_lookup: std::sync::RwLock::new(credential_lookup),
            specs,
            disabled: Default::default(),
            #[cfg(test)]
            fixture: Default::default(),
        }
    }

    pub fn proxy_counts(&self) -> [u64; 3] {
        self.proxy_counts
            .each_ref()
            .map(|n| n.load(std::sync::atomic::Ordering::Relaxed))
    }

    #[cfg(test)]
    pub(crate) fn set_proxy_fixture(&self, fixture: ProxyFixture) {
        *self.fixture.write().unwrap() = Some(fixture);
    }

    /// 运行时注入/更新平台 Cookie 查询（凭证导入后调用）。
    pub fn set_credential_lookup(&self, lookup: CredentialLookup) {
        *self.credential_lookup.write().unwrap() = lookup;
    }

    pub fn spec_for_platform(&self, platform: &str) -> Option<AdapterSpec> {
        if self.disabled.read().unwrap().contains(platform) {
            return None;
        }
        self.specs.iter().find(|s| s.platform == platform).cloned()
    }

    pub fn set_enabled(&self, platform: &str, enabled: bool) {
        let mut disabled = self.disabled.write().unwrap();
        if enabled {
            disabled.remove(platform);
        } else {
            disabled.insert(platform.to_owned());
        }
    }

    pub async fn session_state(&self, plugin_id: &str) -> PluginState {
        if self.disabled.read().unwrap().contains(plugin_id) {
            return PluginState::Disabled;
        }
        let sessions = self.sessions.lock().await;
        if sessions
            .keys()
            .any(|k| k == plugin_id || k.starts_with(&format!("{plugin_id}/")))
        {
            PluginState::Ready
        } else {
            PluginState::Installed
        }
    }

    /// 启动或复用会话并握手；协议不兼容拒绝（INV-07 保持旧档案可读）。
    async fn ensure_session(
        &self,
        platform: &str,
        scope: &str,
    ) -> Result<Arc<SessionInner>, CoreError> {
        let _startup = self.startup.lock().await;
        if self.closed.load(std::sync::atomic::Ordering::SeqCst) {
            return Err(CoreError::Cancelled);
        }
        let spec = self
            .spec_for_platform(platform)
            .ok_or_else(|| CoreError::PluginUnavailable(format!("no adapter for {platform}")))?;
        {
            let mut sessions = self.sessions.lock().await;
            sessions.retain(|_, s| !s.cancel.is_cancelled());
            if let Some(existing) = sessions.get(scope) {
                return Ok(existing.clone());
            }
            if sessions.len() >= 16 {
                let idle = sessions
                    .iter()
                    .find(|(_, s)| Arc::strong_count(s) == 1)
                    .map(|(k, _)| k.clone());
                if let Some(key) = idle {
                    if let Some(s) = sessions.remove(&key) {
                        s.cancel.cancel();
                    }
                } else {
                    return Err(CoreError::Paused("adapter session budget occupied".into()));
                }
            }
        }
        let mut child = tokio::process::Command::new(&spec.binary_path)
            .stdin(std::process::Stdio::piped())
            .stdout(std::process::Stdio::piped())
            .stderr(std::process::Stdio::piped())
            .creation_flags(0x0800_0000) // CREATE_NO_WINDOW：隐式启动无终端窗口
            .kill_on_drop(true)
            .spawn()
            .map_err(|e| CoreError::PluginUnavailable(format!("spawn adapter: {e}")))?;
        let stdin = child
            .stdin
            .take()
            .ok_or_else(|| CoreError::PluginUnavailable("no stdin".into()))?;
        let mut stdout = child
            .stdout
            .take()
            .ok_or_else(|| CoreError::PluginUnavailable("no stdout".into()))?;
        let stderr = child.stderr.take();
        let cancel = CancellationToken::new();
        let inner = Arc::new(SessionInner {
            stdin: Arc::new(Mutex::new(stdin)),
            pending: Arc::new(std::sync::Mutex::new(HashMap::new())),
            hello: Arc::new(Mutex::new(AdapterHello {
                protocol_version: trace_domain::PLUGIN_PROTOCOL_VERSION.into(),
                adapter_version: "0".into(),
                platform: platform.into(),
                capabilities: vec![],
                limits: Default::default(),
            })),
            cancel: cancel.clone(),
            exited: CancellationToken::new(),
            last_used: std::sync::Mutex::new(std::time::Instant::now()),
        });

        // 响应/代理请求处理循环。
        let pending = inner.pending.clone();
        let stdin_for_proxy = inner.stdin.clone();
        let http = self.http.clone();
        let rules = host_rules(&self.profile);
        let cred_lookup = {
            let guard = self.credential_lookup.read().unwrap();
            let lookup = guard.clone();
            let scope = scope.to_owned();
            Arc::new(move |_: &str| lookup(&scope)) as CredentialLookup
        };
        let platform_owned = platform.to_string();
        let proxy_counts = self.proxy_counts.clone();
        let exited = inner.exited.clone();
        #[cfg(test)]
        let fixture = self.fixture.read().unwrap().clone();
        tokio::spawn(async move {
            let mut proxy_tasks = tokio::task::JoinSet::new();
            let mut chunks = trace_contracts::plugin_chunks::Decoder::default();
            loop {
                // read_exact consumes bytes before yielding and is not cancel
                // safe. Keep this future alive when a proxy finishes mid-frame.
                let reading = read_frame_async(&mut stdout);
                tokio::pin!(reading);
                let frame = loop {
                    tokio::select! {
                        _=proxy_tasks.join_next(),if !proxy_tasks.is_empty()=>{},
                        frame = &mut reading => break Some(frame),
                        _ = cancel.cancelled() => break None,
                    }
                };
                let Some(frame) = frame else {
                    break;
                };
                match frame {
                    Ok(bytes) => {
                        let msg: Value = match serde_json::from_slice(&bytes) {
                            Ok(v) => v,
                            Err(_) => continue,
                        };
                        let msg = match chunks.push(msg) {
                            Ok(Some(v)) => v,
                            Ok(None) => continue,
                            Err(_) => break,
                        };
                        let is_response = msg.get("ok").is_some() && msg.get("method").is_none();
                        if is_response {
                            let Ok(resp) = serde_json::from_value::<PluginResponse>(msg) else {
                                continue;
                            };
                            if let Some(tx) = pending.lock().unwrap().remove(&resp.request_id) {
                                let _ = tx.send(response_to_result(resp));
                            }
                        } else if matches!(
                            msg.get("method").and_then(|m| m.as_str()),
                            Some("proxy.fetch" | "proxy.fetch_large")
                        ) {
                            let Ok(req) = serde_json::from_value::<PluginRequest>(msg) else {
                                continue;
                            };
                            let http = http.clone();
                            let rules = rules.clone();
                            let cred_lookup = cred_lookup.clone();
                            let platform_owned = platform_owned.clone();
                            let stdin_for_proxy = stdin_for_proxy.clone();
                            let token = cancel.clone();
                            #[cfg(test)]
                            let fixture = fixture.clone();
                            if proxy_tasks.len() >= 32 {
                                break;
                            }
                            let occupied = proxy_tasks.len() >= 8;
                            let proxy_counts = proxy_counts.clone();
                            proxy_counts[0].fetch_add(1, std::sync::atomic::Ordering::Relaxed);
                            // Keep reading responses while one upstream is slow. The
                            // bounded pool cannot stall unrelated in-flight calls.
                            proxy_tasks.spawn(async move {
                                    let large=req.method=="proxy.fetch_large";
                                    let fetch = async {
                                        // The ordinary adapter default is 512 KiB, not
                                        // the legacy protocol's permitted request cap.
                                        // Older peers may ask for 4 MiB and return a
                                        // small body. Preserve them while bounding
                                        // all responses and keeping ordinary frames.
                                        let max=trace_contracts::plugin_chunks::METADATA_MAX;
                                        if req.params["max_bytes"].as_u64().is_none_or(|n|n==0||n>max as u64){return Err(PluginError{class:trace_domain::errors::FailureClass::InvalidPayload,message:"proxy byte budget".into(),retry_after_ms:None,details:Value::Null});}
                                        #[cfg(test)]
                                        if let Some(fixture)=fixture { return fixture(&req.params); }
                                        handle_proxy_fetch(
                                        &http,
                                        &rules,
                                        &cred_lookup,
                                        &platform_owned,
                                        &req.params,
                                    ).await
                                    };
                                    let resp = if occupied {Err(PluginError{class:trace_domain::errors::FailureClass::RateLimited,message:"proxy concurrency budget".into(),retry_after_ms:Some(1000),details:Value::Null})}else{tokio::select!{_ = token.cancelled()=>return, value=fetch=>value}};
                                    let out = PluginResponse {
                                        request_id: req.request_id.clone(),
                                        ok: resp.is_ok(),
                                        result: resp
                                            .as_ref()
                                            .ok()
                                            .and_then(|r| serde_json::to_value(r).ok()),
                                        error: resp.err(),
                                    };
                                    proxy_counts[1].fetch_add(1,std::sync::atomic::Ordering::Relaxed);
                                    let frame=if large {trace_contracts::plugin_chunks::encode(&out)} else {wire::encode_frame(&out)};
                                    let frame=frame.or_else(|_|wire::encode_frame(&PluginResponse{request_id:out.request_id,ok:false,result:None,error:Some(PluginError{class:trace_domain::errors::FailureClass::InvalidPayload,message:"proxy response exceeds negotiated frame budget".into(),retry_after_ms:None,details:Value::Null})}));
                                    if let Ok(frame)=frame && send_frame(stdin_for_proxy,frame,token).await.is_ok(){proxy_counts[2].fetch_add(1,std::sync::atomic::Ordering::Relaxed);}

                                    });
                        }
                    }
                    Err(_) => break,
                }
            }
            cancel.cancel();
            proxy_tasks.abort_all();
            pending.lock().unwrap().clear();
            let _ = child.start_kill();
            let _ = tokio::time::timeout(Duration::from_secs(2), child.wait()).await;
            exited.cancel();
        });

        // stderr 是适配器诊断通道：计数而非转发，避免敏感内容混入日志。
        if let Some(stderr) = stderr {
            tokio::spawn(async move {
                let mut err = stderr;
                let mut lines = 0u64;
                let mut buf = vec![0u8; 4096];
                loop {
                    match err.read(&mut buf).await {
                        Ok(0) | Err(_) => break,
                        Ok(_) => {
                            lines += 1;
                            if lines <= 3 {
                                tracing::debug!("adapter stderr activity (not echoed)");
                            }
                        }
                    }
                }
            });
        }

        {
            let mut sessions = self.sessions.lock().await;
            if self.closed.load(std::sync::atomic::Ordering::SeqCst) {
                inner.cancel.cancel();
                return Err(CoreError::Cancelled);
            }
            sessions.insert(scope.to_owned(), inner.clone());
        }

        // 握手。
        let hello_v = match Self::call_session(
            &inner,
            trace_contracts::plugin::methods::HELLO.to_owned(),
            serde_json::json!({"protocol_version":trace_domain::PLUGIN_PROTOCOL_VERSION,"platform":platform}),
            Some(10_000),
        )
        .await
        {
            Ok(value) => value,
            Err(error) => {
                inner.cancel.cancel();
                self.sessions.lock().await.remove(scope);
                return Err(error);
            }
        };
        let hello: AdapterHello = match serde_json::from_value(hello_v) {
            Ok(h) => h,
            Err(_) => {
                self.kill_session(scope).await;
                return Err(CoreError::PluginUnavailable("invalid adapter hello".into()));
            }
        };
        if hello.protocol_version != trace_domain::PLUGIN_PROTOCOL_VERSION
            || hello.platform != platform
        {
            self.kill_session(scope).await;
            return Err(CoreError::PluginUnavailable(format!(
                "protocol mismatch: adapter {} vs host {}",
                hello.protocol_version,
                trace_domain::PLUGIN_PROTOCOL_VERSION
            )));
        }
        *inner.hello.lock().await = hello;
        Ok(inner)
    }

    pub async fn adapter_hello(&self, platform: &str) -> Option<AdapterHello> {
        let spec = self.spec_for_platform(platform)?;
        let sessions = self.sessions.lock().await;
        sessions
            .get(&spec.plugin_id)
            .and_then(|s| s.hello.try_lock().ok().map(|h| h.clone()))
    }

    /// 调用适配器方法，带截止时间；连接断开/超时分别归位。
    /// 递归（call → ensure_session → call）需要 boxing。
    pub fn call(
        &self,
        platform: &str,
        method: &str,
        params: Value,
        deadline_ms: Option<u64>,
    ) -> std::pin::Pin<Box<dyn std::future::Future<Output = Result<Value, CoreError>> + Send + '_>>
    {
        let platform = platform.to_string();
        let method = method.to_string();
        Box::pin(self.call_inner(platform.clone(), platform, method, params, deadline_ms))
    }

    pub async fn call_scoped(
        &self,
        platform: &str,
        credential: Option<&str>,
        method: &str,
        params: Value,
        deadline_ms: Option<u64>,
    ) -> Result<Value, CoreError> {
        let scope = credential
            .map(|id| format!("{platform}/{id}"))
            .unwrap_or_else(|| platform.to_owned());
        self.call_inner(
            platform.to_owned(),
            scope,
            method.to_owned(),
            params,
            deadline_ms,
        )
        .await
    }

    async fn call_inner(
        &self,
        platform: String,
        scope: String,
        method: String,
        params: Value,
        deadline_ms: Option<u64>,
    ) -> Result<Value, CoreError> {
        let _spec = self
            .spec_for_platform(&platform)
            .ok_or_else(|| CoreError::PluginUnavailable(format!("no adapter for {platform}")))?;
        let session = self.ensure_session(&platform, &scope).await?;
        Self::call_session(&session, method, params, deadline_ms).await
    }

    async fn call_session(
        session: &SessionInner,
        method: String,
        params: Value,
        deadline_ms: Option<u64>,
    ) -> Result<Value, CoreError> {
        *session.last_used.lock().unwrap() = std::time::Instant::now();
        let request_id = format!("host-{}", uuid::Uuid::new_v4());
        let req = PluginRequest {
            protocol_version: trace_domain::PLUGIN_PROTOCOL_VERSION.into(),
            request_id: request_id.clone(),
            deadline_ms,
            method,
            params,
        };
        let frame = wire::encode_frame(&req)
            .map_err(|_| CoreError::InvalidInput("adapter request frame budget".into()))?;
        let (tx, rx) = oneshot::channel();
        {
            let mut pending = session.pending.lock().unwrap();
            if pending.len() >= 32 {
                return Err(CoreError::Paused("adapter request budget occupied".into()));
            }
            pending.insert(request_id.clone(), tx);
        }
        struct PendingGuard<'a> {
            session: &'a SessionInner,
            id: String,
        }
        impl Drop for PendingGuard<'_> {
            fn drop(&mut self) {
                self.session.pending.lock().unwrap().remove(&self.id);
                *self.session.last_used.lock().unwrap() = std::time::Instant::now();
            }
        }
        let _pending = PendingGuard {
            session,
            id: request_id,
        };
        let exchange = async {
            send_frame(session.stdin.clone(), frame, session.cancel.clone()).await?;
            match rx.await {
                Ok(Ok(v)) => Ok(v),
                Ok(Err(e)) => Err(plugin_error_to_core(e)),
                Err(_) => Err(CoreError::PluginUnavailable(
                    "adapter response channel closed".into(),
                )),
            }
        };
        let timeout = Duration::from_millis(
            deadline_ms
                .unwrap_or(30_000)
                .min(120_000)
                .saturating_add(2_000),
        );
        tokio::select! {
            value=tokio::time::timeout(timeout,exchange)=>value.unwrap_or_else(|_|Err(CoreError::Network("adapter deadline exceeded".into()))),
            _=session.cancel.cancelled()=>Err(CoreError::PluginUnavailable("adapter stopped".into())),
        }
    }

    pub async fn reap_idle(&self) {
        let Ok(_startup) = self.startup.try_lock() else {
            return;
        };
        self.sessions.lock().await.retain(|_, s| {
            let idle = Arc::strong_count(s) == 1
                && s.pending.lock().unwrap().is_empty()
                && s.last_used.lock().unwrap().elapsed() > Duration::from_secs(120);
            if idle {
                s.cancel.cancel();
            }
            !idle && !s.cancel.is_cancelled()
        });
    }

    pub async fn kill_session(&self, plugin_id: &str) {
        let mut sessions = self.sessions.lock().await;
        let keys = sessions
            .keys()
            .filter(|k| k.as_str() == plugin_id || k.starts_with(&format!("{plugin_id}/")))
            .cloned()
            .collect::<Vec<_>>();
        let removed = keys
            .into_iter()
            .filter_map(|k| sessions.remove(&k))
            .collect::<Vec<_>>();
        drop(sessions);
        for s in &removed {
            s.cancel.cancel();
        }
        let _ = tokio::time::timeout(Duration::from_secs(3), async {
            for s in removed {
                s.exited.cancelled().await;
            }
        })
        .await;
    }

    pub async fn shutdown_all(&self) {
        self.closed.store(true, std::sync::atomic::Ordering::SeqCst);
        let sessions: Vec<_> = self.sessions.lock().await.drain().map(|(_, s)| s).collect();
        for s in &sessions {
            s.cancel.cancel();
        }
        let _ = tokio::time::timeout(Duration::from_secs(3), async {
            for s in sessions {
                s.exited.cancelled().await;
            }
        })
        .await;
    }
}

// An abandoned caller cannot abandon a partially written stdio frame. The
// dedicated send task either finishes the frame or invalidates the session.
async fn send_frame(
    stdin: Arc<Mutex<tokio::process::ChildStdin>>,
    frame: Vec<u8>,
    cancel: CancellationToken,
) -> Result<(), CoreError> {
    tokio::spawn(async move {
        let write = async {
            let mut s = stdin.lock().await;
            s.write_all(&frame).await?;
            s.flush().await
        };
        let result = tokio::select! {
            _=cancel.cancelled()=>Err(CoreError::Cancelled),
            result=tokio::time::timeout(Duration::from_secs(25),write)=>match result {
                Ok(Ok(()))=>Ok(()),
                _=>Err(CoreError::PluginUnavailable("adapter frame write failed".into())),
            }
        };
        if result.is_err() {
            cancel.cancel();
        }
        result
    })
    .await
    .map_err(|_| CoreError::PluginUnavailable("adapter writer stopped".into()))?
}

async fn handle_proxy_fetch(
    http: &ConstrainedClient,
    rules: &crate::transport::HostRules,
    cred_lookup: &CredentialLookup,
    platform: &str,
    params: &Value,
) -> Result<ProxyFetchResult, PluginError> {
    let params: ProxyFetchParams =
        serde_json::from_value(params.clone()).map_err(|e| PluginError {
            class: trace_domain::errors::FailureClass::InvalidPayload,
            message: format!("bad proxy params: {e}"),
            retry_after_ms: None,
            details: Value::Null,
        })?;
    let scoped_rules = crate::transport::platform_rules(rules, platform);
    let cookie = if params.with_credential {
        (cred_lookup)(platform)
    } else {
        None
    };
    let outcome = http
        .fetch_metadata(
            &scoped_rules,
            &params.method,
            &params.url,
            Some(&params.headers),
            cookie.as_deref(),
            params.max_bytes,
        )
        .await
        .map_err(|e| PluginError {
            class: e.failure_class(),
            message: format!("{:?}", e.failure_class()),
            retry_after_ms: None,
            details: Value::Null,
        })?;
    Ok(ProxyFetchResult {
        status: outcome.status,
        headers: serde_json::json!({}),
        body_b64: base64::engine::general_purpose::STANDARD.encode(&outcome.body),
        final_url: outcome.final_url,
    })
}

fn plugin_error_to_core(e: PluginError) -> CoreError {
    if e.class == trace_domain::errors::FailureClass::SourceUnavailable
        && e.details.get("source_state").and_then(Value::as_str) == Some("offline")
    {
        // Whitelist a source state, never relay the adapter's message/details.
        return CoreError::SourceUnavailable("room is not live".into());
    }
    CoreError::Adapter(e.class)
}

fn response_to_result(resp: PluginResponse) -> Result<Value, PluginError> {
    if resp.ok {
        Ok(resp.result.unwrap_or(Value::Null))
    } else {
        Err(resp.error.unwrap_or(PluginError {
            class: trace_domain::errors::FailureClass::Unknown,
            message: "adapter error".into(),
            retry_after_ms: None,
            details: Value::Null,
        }))
    }
}

async fn read_frame_async(reader: &mut (impl AsyncReadExt + Unpin)) -> std::io::Result<Vec<u8>> {
    let mut len_buf = [0u8; 4];
    reader.read_exact(&mut len_buf).await?;
    let len = u32::from_le_bytes(len_buf);
    if len > trace_domain::MAX_FRAME_BYTES {
        return Err(std::io::Error::new(
            std::io::ErrorKind::InvalidData,
            "frame too large",
        ));
    }
    let mut buf = vec![0u8; len as usize];
    reader.read_exact(&mut buf).await?;
    Ok(buf)
}

/// 便捷构造：平台适配器二进制路径解析（发行包目录或开发目标目录）。
pub fn default_adapter_specs(profile: &Profile) -> Vec<AdapterSpec> {
    let dir = profile
        .adapter_dir
        .clone()
        .unwrap_or_else(|| PathBuf::from("."));
    let exe = |name: &str| {
        dir.join(if cfg!(windows) {
            format!("{name}.exe")
        } else {
            name.to_string()
        })
    };
    vec![
        AdapterSpec {
            plugin_id: "bilibili".into(),
            platform: "bilibili".into(),
            binary_path: exe("trace-adapter-bilibili"),
        },
        AdapterSpec {
            plugin_id: "douyin".into(),
            platform: "douyin".into(),
            binary_path: exe("trace-adapter-douyin"),
        },
        // 合成异构平台只在开发/测试构建注册，发行包没有此能力。
        #[cfg(debug_assertions)]
        AdapterSpec {
            plugin_id: "fake".into(),
            platform: "fake".into(),
            binary_path: exe("trace-fake-adapter"),
        },
    ]
}

#[cfg(test)]
mod m1_tests {
    use super::*;
    use serde_json::json;
    use tokio::io::{AsyncReadExt, AsyncWriteExt};

    #[test]
    fn c14_offline_source_errors_keep_safe_state_without_adapter_payload() {
        use trace_domain::errors::FailureClass;
        for (class, state, offline) in [
            (FailureClass::SourceUnavailable, "offline", true),
            (
                FailureClass::SourceUnavailable,
                "fixture-private-state",
                false,
            ),
            (FailureClass::PermissionDenied, "offline", false),
        ] {
            let error = plugin_error_to_core(PluginError {
                class,
                message: "fixture-private-message".into(),
                retry_after_ms: None,
                details: json!({"source_state":state,"raw":"fixture-private-payload"}),
            });
            assert_eq!(matches!(error, CoreError::SourceUnavailable(_)), offline);
            let api = error.to_api_error();
            assert!(
                !serde_json::to_string(&api)
                    .unwrap()
                    .contains("fixture-private")
            );
            if offline {
                assert!(api.user_action.unwrap().contains("未开播"));
            } else {
                assert_eq!(error.failure_class(), class);
            }
        }
    }

    fn host() -> Arc<PluginHost> {
        let binary =
            PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../target/debug/m1_host_probe.exe");
        assert!(binary.is_file(), "build the workspace before process tests");
        Arc::new(PluginHost::new(
            Profile {
                test_allowed_hosts: vec!["127.0.0.1".into()],
                ..Default::default()
            },
            Arc::new(ConstrainedClient::build().unwrap()),
            ["bilibili", "douyin"]
                .into_iter()
                .map(|p| AdapterSpec {
                    platform: p.into(),
                    plugin_id: p.into(),
                    binary_path: binary.clone(),
                })
                .collect(),
            Arc::new(|scope| match scope {
                "bilibili/one" => Some("session=one".into()),
                "bilibili/two" => Some("session=two".into()),
                "douyin/one" => Some("session=other-site".into()),
                _ => None,
            }),
        ))
    }
    #[tokio::test]
    async fn c06_credential_scopes_and_cross_origin_redirect_strip_headers() {
        let host = host();
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let base = format!("http://{}", listener.local_addr().unwrap());
        let received = Arc::new(std::sync::Mutex::new(Vec::new()));
        let captures = received.clone();
        let server = tokio::spawn(async move {
            loop {
                let (mut socket, _) = listener.accept().await.unwrap();
                let captures = captures.clone();
                tokio::spawn(async move {
                    let mut req = [0; 4096];
                    let n = socket.read(&mut req).await.unwrap();
                    captures
                        .lock()
                        .unwrap()
                        .push(String::from_utf8_lossy(&req[..n]).to_string());
                    let _ = socket
                        .write_all(
                            b"HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}",
                        )
                        .await;
                });
            }
        });
        for (platform, credential, expected) in [
            ("bilibili", Some("one"), Some("session=one")),
            ("bilibili", Some("two"), Some("session=two")),
            ("douyin", Some("one"), Some("session=other-site")),
            ("bilibili", None, None),
        ] {
            host.call_scoped(
                platform,
                credential,
                "probe",
                json!({"url":base}),
                Some(5000),
            )
            .await
            .unwrap();
            let raw = received
                .lock()
                .unwrap()
                .last()
                .unwrap()
                .to_ascii_lowercase();
            let cookie = raw.lines().find(|l| l.starts_with("cookie:"));
            assert_eq!(cookie.map(|v| v[7..].trim()), expected);
        }
        let redirect = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let origin = format!("http://{}", redirect.local_addr().unwrap());
        let destination = base.clone();
        let redir = tokio::spawn(async move {
            let (mut s, _) = redirect.accept().await.unwrap();
            let mut req = [0; 4096];
            let n = s.read(&mut req).await.unwrap();
            assert!(String::from_utf8_lossy(&req[..n]).contains("session=one"));
            s.write_all(format!("HTTP/1.1 302 Found\r\nLocation: {destination}/next\r\nContent-Length: 0\r\nConnection: close\r\n\r\n").as_bytes()).await.unwrap();
        });
        host.call_scoped(
            "bilibili",
            Some("one"),
            "probe",
            json!({"url":origin}),
            Some(5000),
        )
        .await
        .unwrap();
        redir.await.unwrap();
        assert!(
            !received
                .lock()
                .unwrap()
                .last()
                .unwrap()
                .to_ascii_lowercase()
                .contains("cookie:")
        );
        host.shutdown_all().await;
        server.abort();
    }
    #[tokio::test]
    async fn c05_plugin_process_isolation_and_cancelled_request_cleanup() {
        let host = host();
        host.call("bilibili", "pid", json!({}), None).await.unwrap();
        host.call("douyin", "pid", json!({}), None).await.unwrap();
        let session = host.sessions.lock().await["bilibili"].clone();
        for _ in 0..36 {
            let request = host.call("bilibili", "hold", json!({}), Some(1000));
            let _ = tokio::time::timeout(Duration::from_millis(5), request).await;
            assert_eq!(
                session.pending.lock().unwrap().len(),
                0,
                "dropped request must release its pending slot"
            );
        }
        host.set_enabled("bilibili", false);
        host.kill_session("bilibili").await;
        assert!(session.exited.is_cancelled());
        assert!(host.call("bilibili", "pid", json!({}), None).await.is_err());
        assert!(host.call("douyin", "pid", json!({}), None).await.is_ok());
        let start = std::time::Instant::now();
        host.shutdown_all().await;
        assert!(start.elapsed() < Duration::from_secs(4));
        assert!(host.sessions.lock().await.is_empty());
        assert!(host.call("douyin", "pid", json!({}), None).await.is_err());
    }
    #[tokio::test]
    async fn c05_slow_proxy_does_not_block_same_scope_responses_and_idle_exit() {
        let host = host();
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let base = format!("http://{}", listener.local_addr().unwrap());
        let started = Arc::new(tokio::sync::Notify::new());
        let notice = started.clone();
        let server = tokio::spawn(async move {
            loop {
                let (mut socket, _) = listener.accept().await.unwrap();
                let notice = notice.clone();
                tokio::spawn(async move {
                    let mut req = [0; 4096];
                    let n = socket.read(&mut req).await.unwrap();
                    if String::from_utf8_lossy(&req[..n]).contains("/slow ") {
                        notice.notify_one();
                        tokio::time::sleep(Duration::from_secs(2)).await;
                    }
                    let _ = socket
                        .write_all(
                            b"HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}",
                        )
                        .await;
                });
            }
        });
        let h = host.clone();
        let slow_url = format!("{base}/slow");
        let slow = tokio::spawn(async move {
            h.call("bilibili", "probe", json!({"url":slow_url}), Some(5000))
                .await
        });
        tokio::time::timeout(Duration::from_secs(5), started.notified())
            .await
            .unwrap();
        tokio::time::timeout(
            Duration::from_millis(900),
            host.call(
                "bilibili",
                "probe",
                json!({"url":format!("{base}/fast")}),
                Some(5000),
            ),
        )
        .await
        .expect("fast reply blocked behind slow upstream")
        .unwrap();
        slow.await.unwrap().unwrap();
        let exited = {
            let sessions = host.sessions.lock().await;
            let s = &sessions["bilibili"];
            *s.last_used.lock().unwrap() = std::time::Instant::now() - Duration::from_secs(121);
            s.exited.clone()
        };
        host.reap_idle().await;
        tokio::time::timeout(Duration::from_secs(3), exited.cancelled())
            .await
            .unwrap();
        assert!(host.sessions.lock().await.is_empty());
        assert!(host.call("bilibili", "pid", json!({}), None).await.is_ok());
        host.shutdown_all().await;
        server.abort();
    }
    #[test]
    fn c06_failure_class_survives_without_untrusted_error_text() {
        for class in [
            trace_domain::errors::FailureClass::SchemaChanged,
            trace_domain::errors::FailureClass::PermissionDenied,
            trace_domain::errors::FailureClass::RateLimited,
        ] {
            let error = plugin_error_to_core(PluginError {
                class,
                message: "private-sentinel".into(),
                retry_after_ms: None,
                details: json!({"secret":"private-sentinel"}),
            });
            assert_eq!(error.failure_class(), class);
            assert!(!error.to_string().contains("sentinel"));
            assert!(
                !serde_json::to_string(&error.to_api_error())
                    .unwrap()
                    .contains("sentinel")
            );
        }
    }
    #[tokio::test]
    async fn c05_proxy_completion_cannot_cancel_partial_frame_read() {
        let binary =
            PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../target/debug/m1_frame_probe.exe");
        assert!(binary.is_file(), "build workspace fixture binaries first");
        let host = PluginHost::new(
            Profile {
                test_allowed_hosts: vec!["127.0.0.1".into()],
                ..Default::default()
            },
            Arc::new(ConstrainedClient::build().unwrap()),
            vec![AdapterSpec {
                platform: "bilibili".into(),
                plugin_id: "bilibili".into(),
                binary_path: binary,
            }],
            Arc::new(|_| None),
        );
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let url = format!("http://{}", listener.local_addr().unwrap());
        let server = tokio::spawn(async move {
            for _ in 0..3 {
                let (mut socket, _) = listener.accept().await.unwrap();
                let mut req = [0; 4096];
                let mut received = 0;
                loop {
                    let count = socket.read(&mut req[received..]).await.unwrap();
                    assert!(count > 0, "request ended before headers");
                    received += count;
                    if req[..received].windows(4).any(|w| w == b"\r\n\r\n") {
                        break;
                    }
                    assert!(
                        received < req.len(),
                        "request exceeds fixture header budget"
                    );
                }
                socket
                    .write_all(
                        b"HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}",
                    )
                    .await
                    .unwrap();
            }
        });
        for _ in 0..3 {
            let value = host
                .call("bilibili", "race", json!({"url":url}), Some(5000))
                .await
                .unwrap();
            assert_eq!(value["payload"].as_str().unwrap(), "中".repeat(65536));
        }
        server.await.unwrap();
        host.shutdown_all().await;
    }
}
