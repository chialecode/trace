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

struct SessionInner {
    stdin: Arc<Mutex<tokio::process::ChildStdin>>,
    pending: Arc<Mutex<PendingCalls>>,
    hello: Arc<Mutex<AdapterHello>>,
    cancel: CancellationToken,
}

pub struct PluginHost {
    profile: Profile,
    http: Arc<ConstrainedClient>,
    sessions: Mutex<HashMap<String, Arc<SessionInner>>>,
    credential_lookup: std::sync::RwLock<CredentialLookup>,
    specs: Vec<AdapterSpec>,
}

impl PluginHost {
    pub fn new(
        profile: Profile,
        http: Arc<ConstrainedClient>,
        specs: Vec<AdapterSpec>,
        credential_lookup: CredentialLookup,
    ) -> Self {
        Self {
            profile,
            http,
            sessions: Mutex::new(HashMap::new()),
            credential_lookup: std::sync::RwLock::new(credential_lookup),
            specs,
        }
    }

    /// 运行时注入/更新平台 Cookie 查询（凭证导入后调用）。
    pub fn set_credential_lookup(&self, lookup: CredentialLookup) {
        *self.credential_lookup.write().unwrap() = lookup;
    }

    pub fn spec_for_platform(&self, platform: &str) -> Option<AdapterSpec> {
        self.specs.iter().find(|s| s.platform == platform).cloned()
    }

    pub async fn session_state(&self, plugin_id: &str) -> PluginState {
        let sessions = self.sessions.lock().await;
        if sessions.contains_key(plugin_id) {
            PluginState::Ready
        } else {
            PluginState::Installed
        }
    }

    /// 启动或复用会话并握手；协议不兼容拒绝（INV-07 保持旧档案可读）。
    async fn ensure_session(&self, platform: &str) -> Result<Arc<SessionInner>, CoreError> {
        let spec = self
            .spec_for_platform(platform)
            .ok_or_else(|| CoreError::PluginUnavailable(format!("no adapter for {platform}")))?;
        if let Some(existing) = self.sessions.lock().await.get(&spec.plugin_id) {
            return Ok(existing.clone());
        }
        let mut child = tokio::process::Command::new(&spec.binary_path)
            .stdin(std::process::Stdio::piped())
            .stdout(std::process::Stdio::piped())
            .stderr(std::process::Stdio::piped())
            .creation_flags(0x0800_0000) // CREATE_NO_WINDOW：隐式启动无终端窗口
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
            pending: Arc::new(Mutex::new(HashMap::new())),
            hello: Arc::new(Mutex::new(AdapterHello {
                protocol_version: trace_domain::PLUGIN_PROTOCOL_VERSION.into(),
                adapter_version: "0".into(),
                platform: platform.into(),
                capabilities: vec![],
                limits: Default::default(),
            })),
            cancel: cancel.clone(),
        });

        // 响应/代理请求处理循环。
        let pending = inner.pending.clone();
        let stdin_for_proxy = inner.stdin.clone();
        let http = self.http.clone();
        let rules = host_rules(&self.profile);
        let cred_lookup = {
            let guard = self.credential_lookup.read().unwrap();
            guard.clone()
        };
        let platform_owned = platform.to_string();
        tokio::spawn(async move {
            loop {
                tokio::select! {
                    frame = read_frame_async(&mut stdout) => {
                        match frame {
                            Ok(bytes) => {
                                let msg: Value = match serde_json::from_slice(&bytes) {
                                    Ok(v) => v,
                                    Err(_) => continue,
                                };
                                let is_response =
                                    msg.get("ok").is_some() && msg.get("method").is_none();
                                if is_response {
                                    let Ok(resp) = serde_json::from_value::<PluginResponse>(msg) else {
                                        continue;
                                    };
                                    if let Some(tx) =
                                        pending.lock().await.remove(&resp.request_id)
                                    {
                                        let _ = tx.send(response_to_result(resp));
                                    }
                                } else if msg.get("method").and_then(|m| m.as_str())
                                    == Some(trace_contracts::plugin::methods::PROXY_FETCH)
                                {
                                    let Ok(req) = serde_json::from_value::<PluginRequest>(msg)
                                    else {
                                        continue;
                                    };
                                    let resp = handle_proxy_fetch(
                                        &http,
                                        &rules,
                                        &cred_lookup,
                                        &platform_owned,
                                        &req.params,
                                    )
                                    .await;
                                    let out = PluginResponse {
                                        request_id: req.request_id.clone(),
                                        ok: resp.is_ok(),
                                        result: resp
                                            .as_ref()
                                            .ok()
                                            .and_then(|r| serde_json::to_value(r).ok()),
                                        error: resp.err(),
                                    };
                                    if let Ok(frame) = wire::encode_frame(&out) {
                                        let mut s = stdin_for_proxy.lock().await;
                                        let _ = s.write_all(&frame).await;
                                        let _ = s.flush().await;
                                    }
                                }
                            }
                            Err(_) => break,
                        }
                    }
                    _ = cancel.cancelled() => break,
                }
            }
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

        self.sessions
            .lock()
            .await
            .insert(spec.plugin_id.clone(), inner.clone());

        // 握手。
        let hello_v = self
            .call(
                platform,
                trace_contracts::plugin::methods::HELLO,
                serde_json::json!({ "protocol_version": trace_domain::PLUGIN_PROTOCOL_VERSION }),
                Some(10_000),
            )
            .await?;
        let hello: AdapterHello = serde_json::from_value(hello_v)
            .map_err(|e| CoreError::PluginUnavailable(format!("bad hello: {e}")))?;
        if hello.protocol_version != trace_domain::PLUGIN_PROTOCOL_VERSION {
            self.kill_session(&spec.plugin_id).await;
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
        Box::pin(self.call_inner(platform, method, params, deadline_ms))
    }

    async fn call_inner(
        &self,
        platform: String,
        method: String,
        params: Value,
        deadline_ms: Option<u64>,
    ) -> Result<Value, CoreError> {
        let _spec = self
            .spec_for_platform(&platform)
            .ok_or_else(|| CoreError::PluginUnavailable(format!("no adapter for {platform}")))?;
        let session = self.ensure_session(&platform).await?;
        let request_id = format!("host-{}", uuid::Uuid::new_v4());
        let req = PluginRequest {
            protocol_version: trace_domain::PLUGIN_PROTOCOL_VERSION.into(),
            request_id: request_id.clone(),
            deadline_ms,
            method,
            params,
        };
        let (tx, rx) = oneshot::channel();
        session.pending.lock().await.insert(request_id.clone(), tx);
        let frame = wire::encode_frame(&req).map_err(|e| CoreError::Internal(format!("{e}")))?;
        {
            let mut stdin = session.stdin.lock().await;
            stdin
                .write_all(&frame)
                .await
                .map_err(|_| CoreError::PluginUnavailable("adapter stdin closed".into()))?;
            stdin.flush().await.map_err(CoreError::Io)?;
        }
        let timeout = Duration::from_millis(deadline_ms.unwrap_or(30_000) + 2_000);
        let result = tokio::time::timeout(timeout, rx).await;
        match result {
            Ok(Ok(Ok(value))) => Ok(value),
            Ok(Ok(Err(plugin_err))) => Err(plugin_error_to_core(plugin_err)),
            Ok(Err(_)) | Err(_) => {
                session.pending.lock().await.remove(&request_id);
                Err(CoreError::Network("adapter deadline exceeded".into()))
            }
        }
    }

    pub async fn kill_session(&self, plugin_id: &str) {
        if let Some(s) = self.sessions.lock().await.remove(plugin_id) {
            s.cancel.cancel();
        }
    }

    pub async fn shutdown_all(&self) {
        let ids: Vec<String> = self.sessions.lock().await.keys().cloned().collect();
        for id in ids {
            self.kill_session(&id).await;
        }
    }
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
    let cookie = if params.with_credential {
        (cred_lookup)(platform)
    } else {
        None
    };
    let outcome = http
        .fetch_metadata(
            rules,
            &params.method,
            &params.url,
            Some(&params.headers),
            cookie.as_deref(),
            params.max_bytes,
        )
        .await
        .map_err(|e| PluginError {
            class: e.failure_class(),
            message: e.to_string(),
            retry_after_ms: None,
            details: Value::Null,
        })?;
    Ok(ProxyFetchResult {
        status: outcome.status,
        headers: serde_json::json!({}),
        body_b64: base64::engine::general_purpose::STANDARD.encode(&outcome.body),
        final_url: outcome.final_host,
    })
}

fn plugin_error_to_core(e: PluginError) -> CoreError {
    use trace_domain::errors::FailureClass::*;
    match e.class {
        AuthRequired => CoreError::AuthRequired(e.message),
        RateLimited => CoreError::RateLimited(e.message),
        InvalidPayload => CoreError::InvalidInput(e.message),
        _ => CoreError::Network(e.message),
    }
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
        // 合成异构平台（仅测试构建存在），证明核心不绑定单站点形状。
        AdapterSpec {
            plugin_id: "fake".into(),
            platform: "fake".into(),
            binary_path: exe("trace-fake-adapter"),
        },
    ]
}
