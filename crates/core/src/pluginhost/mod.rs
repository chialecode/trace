//! 插件宿主：平台适配器进程的启停、协议与取消（docs/design.md §8）。
//!
//! - 一个适配器进程服务同平台多账号，空闲退出；不按创作者常驻。
//! - 双向帧：适配器经 `proxy.fetch` 使用核心受限 HTTP 代理；Cookie 由宿主
//!   按平台解析并仅在授权域附加，永远不进入适配器进程（INV-10）。
//! - 协议帧上限 1 MiB；单页 100 条；请求带截止时间。
//! - 进程与帧读写见 `session`，元数据代理见 `proxy`。

pub(crate) mod fault_log;
mod proxy;
mod session;

use crate::config::Profile;
use crate::error::CoreError;
use crate::transport::{ConstrainedClient, host_rules};
use serde_json::Value;
use session::{Launched, Reader, SessionInner, send_frame};
use std::collections::HashMap;
use std::path::PathBuf;
use std::sync::Arc;
use std::time::Duration;
use tokio::sync::{Mutex, oneshot};
use trace_contracts::plugin::{AdapterHello, PluginError, PluginRequest};
use trace_contracts::wire;
use trace_domain::PLUGIN_PROTOCOL_VERSION;
use trace_domain::errors::FailureClass;
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
type ProxyFixture = Arc<
    dyn Fn(&Value) -> Result<trace_contracts::plugin::ProxyFetchResult, PluginError> + Send + Sync,
>;

/// Adapter sessions alive at once; an idle one is evicted for a new scope.
const SESSION_LIMIT: usize = 16;
/// Requests waiting for an answer from one adapter.
const PENDING_LIMIT: usize = 32;
const HELLO_DEADLINE_MS: u64 = 10_000;
const DEFAULT_DEADLINE_MS: u64 = 30_000;
const MAX_DEADLINE_MS: u64 = 120_000;
/// Time allowed beyond the adapter's own deadline for the answer to travel back.
const DEADLINE_SLACK_MS: u64 = 2_000;
const IDLE_EXIT: Duration = Duration::from_secs(120);
const KILL_GRACE: Duration = Duration::from_secs(3);

pub struct PluginHost {
    proxy_counts: Arc<[std::sync::atomic::AtomicU64; 3]>,
    profile: Profile,
    http: Arc<ConstrainedClient>,
    sessions: Mutex<HashMap<String, Arc<SessionInner>>>,
    startup: Mutex<()>,
    closed: std::sync::atomic::AtomicBool,
    credential_lookup: std::sync::RwLock<CredentialLookup>,
    session_jar: Arc<crate::session_jar::SessionJar>,
    specs: std::sync::RwLock<Vec<AdapterSpec>>,
    disabled: std::sync::RwLock<std::collections::HashSet<String>>,
    #[cfg(test)]
    fixture: std::sync::RwLock<Option<ProxyFixture>>,
}

fn is_closed(flag: &std::sync::atomic::AtomicBool) -> bool {
    flag.load(std::sync::atomic::Ordering::SeqCst)
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
            session_jar: Arc::default(),
            specs: std::sync::RwLock::new(specs),
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

    /// The session Cookies the proxy has taken from the sites that need them for media.
    pub fn session_jar(&self) -> &Arc<crate::session_jar::SessionJar> {
        &self.session_jar
    }

    pub fn spec_for_platform(&self, platform: &str) -> Option<AdapterSpec> {
        if self.disabled.read().unwrap().contains(platform) {
            return None;
        }
        self.specs
            .read()
            .unwrap()
            .iter()
            .find(|s| s.platform == platform)
            .cloned()
    }

    /// Points a platform at an installed release and ends its current session, so the next call
    /// starts the new binary. The old release stays on disk for a rollback.
    pub async fn replace_spec(&self, spec: AdapterSpec) {
        {
            let mut specs = self.specs.write().unwrap();
            if let Some(existing) = specs.iter_mut().find(|s| s.platform == spec.platform) {
                *existing = spec.clone();
            } else {
                specs.push(spec.clone());
            }
        }
        self.kill_session(&spec.platform).await;
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
        if is_closed(&self.closed) {
            return Err(CoreError::Cancelled);
        }
        let spec = self
            .spec_for_platform(platform)
            .ok_or_else(|| CoreError::PluginUnavailable(format!("no adapter for {platform}")))?;
        if let Some(existing) = self.reusable_session(scope).await? {
            return Ok(existing);
        }
        let inner = self.start_session(&spec, platform, scope)?;
        self.register_session(scope, &inner).await?;
        self.handshake(&inner, scope, platform).await?;
        Ok(inner)
    }

    /// The live session of the scope, or room for a new one: at the limit an idle session
    /// is evicted, and with none idle the new scope waits.
    async fn reusable_session(&self, scope: &str) -> Result<Option<Arc<SessionInner>>, CoreError> {
        let mut sessions = self.sessions.lock().await;
        sessions.retain(|_, s| !s.cancel.is_cancelled());
        if let Some(existing) = sessions.get(scope) {
            return Ok(Some(existing.clone()));
        }
        if sessions.len() >= SESSION_LIMIT {
            let idle = sessions
                .iter()
                .find(|(_, s)| Arc::strong_count(s) == 1)
                .map(|(k, _)| k.clone());
            let Some(key) = idle else {
                return Err(CoreError::Paused("adapter session budget occupied".into()));
            };
            if let Some(s) = sessions.remove(&key) {
                s.cancel.cancel();
            }
        }
        Ok(None)
    }

    /// Starts the adapter process and the tasks that read and drain it.
    fn start_session(
        &self,
        spec: &AdapterSpec,
        platform: &str,
        scope: &str,
    ) -> Result<Arc<SessionInner>, CoreError> {
        let Launched {
            child,
            stdin,
            stdout,
            stderr,
        } = session::launch(spec)?;
        let inner = Arc::new(SessionInner::new(stdin, platform));
        let reader = Reader {
            stdout,
            child,
            pending: inner.pending.clone(),
            cancel: inner.cancel.clone(),
            exited: inner.exited.clone(),
            proxy: self.proxy_context(&inner, platform, scope),
        };
        tokio::spawn(reader.run());
        if let Some(stderr) = stderr {
            tokio::spawn(session::drain_stderr(stderr));
        }
        Ok(inner)
    }

    /// What the session's proxy requests run with. Credentials resolve for the session's
    /// own scope only, whatever platform name the adapter asks for.
    fn proxy_context(
        &self,
        inner: &SessionInner,
        platform: &str,
        scope: &str,
    ) -> proxy::ProxyContext {
        let lookup = self.credential_lookup.read().unwrap().clone();
        let scope = scope.to_owned();
        let session = proxy::Session {
            jar: self.session_jar.clone(),
            scope: scope.clone(),
        };
        proxy::ProxyContext {
            http: self.http.clone(),
            rules: host_rules(&self.profile),
            credentials: Arc::new(move |_: &str| lookup(&scope)),
            session,
            platform: platform.to_owned(),
            counts: self.proxy_counts.clone(),
            stdin: inner.stdin.clone(),
            cancel: inner.cancel.clone(),
            #[cfg(test)]
            fixture: self.fixture.read().unwrap().clone(),
        }
    }

    async fn register_session(
        &self,
        scope: &str,
        inner: &Arc<SessionInner>,
    ) -> Result<(), CoreError> {
        let mut sessions = self.sessions.lock().await;
        if is_closed(&self.closed) {
            inner.cancel.cancel();
            return Err(CoreError::Cancelled);
        }
        sessions.insert(scope.to_owned(), inner.clone());
        Ok(())
    }

    /// The adapter must speak this protocol for this platform, or it is stopped.
    async fn handshake(
        &self,
        inner: &SessionInner,
        scope: &str,
        platform: &str,
    ) -> Result<(), CoreError> {
        let params = serde_json::json!({
            "protocol_version": PLUGIN_PROTOCOL_VERSION,
            "platform": platform,
        });
        let method = trace_contracts::plugin::methods::HELLO.to_owned();
        let reply =
            Self::call_session(inner, method, params, Value::Null, Some(HELLO_DEADLINE_MS)).await;
        let value = match reply {
            Ok(value) => value,
            Err(error) => {
                inner.cancel.cancel();
                self.sessions.lock().await.remove(scope);
                return Err(error);
            }
        };
        let Ok(hello) = serde_json::from_value::<AdapterHello>(value) else {
            self.kill_session(scope).await;
            return Err(CoreError::PluginUnavailable("invalid adapter hello".into()));
        };
        if hello.protocol_version != PLUGIN_PROTOCOL_VERSION || hello.platform != platform {
            self.kill_session(scope).await;
            return Err(CoreError::PluginUnavailable(format!(
                "protocol mismatch: adapter {} vs host {PLUGIN_PROTOCOL_VERSION}",
                hello.protocol_version,
            )));
        }
        *inner.hello.lock().await = hello;
        Ok(())
    }

    /// The adapter's handshake, starting its process when none is running.
    pub async fn hello(&self, platform: &str) -> Result<AdapterHello, CoreError> {
        let session = self.ensure_session(platform, platform).await?;
        let hello = session.hello.lock().await.clone();
        Ok(hello)
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
        Box::pin(self.call_inner(
            platform.clone(),
            platform,
            method,
            params,
            deadline_ms,
            None,
        ))
    }

    pub async fn call_scoped(
        &self,
        platform: &str,
        credential: Option<&str>,
        method: &str,
        params: Value,
        deadline_ms: Option<u64>,
    ) -> Result<Value, CoreError> {
        self.call_scoped_with_evidence(platform, credential, method, params, deadline_ms, None)
            .await
    }

    #[allow(clippy::too_many_arguments)]
    pub async fn call_scoped_with_evidence(
        &self,
        platform: &str,
        credential: Option<&str>,
        method: &str,
        params: Value,
        deadline_ms: Option<u64>,
        evidence_id: Option<&str>,
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
            evidence_id.map(str::to_owned),
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
        evidence_id: Option<String>,
    ) -> Result<Value, CoreError> {
        let _spec = self
            .spec_for_platform(&platform)
            .ok_or_else(|| CoreError::PluginUnavailable(format!("no adapter for {platform}")))?;
        let session = self.ensure_session(&platform, &scope).await?;
        let cookie = {
            let lookup = self.credential_lookup.read().unwrap();
            lookup(&scope)
        };
        let context = access_context(&platform, cookie.as_deref(), evidence_id.as_deref());
        Self::call_session(&session, method, params, context, deadline_ms).await
    }

    async fn call_session(
        session: &SessionInner,
        method: String,
        params: Value,
        context: Value,
        deadline_ms: Option<u64>,
    ) -> Result<Value, CoreError> {
        *session.last_used.lock().unwrap() = std::time::Instant::now();
        let request_id = format!("host-{}", uuid::Uuid::new_v4());
        let method_name = method.clone();
        let req = PluginRequest {
            protocol_version: PLUGIN_PROTOCOL_VERSION.into(),
            request_id: request_id.clone(),
            deadline_ms,
            method,
            params,
            context,
        };
        let frame = wire::encode_frame(&req)
            .map_err(|_| CoreError::InvalidInput("adapter request frame budget".into()))?;
        let (tx, rx) = oneshot::channel();
        {
            let mut pending = session.pending.lock().unwrap();
            if pending.len() >= PENDING_LIMIT {
                return Err(CoreError::Paused("adapter request budget occupied".into()));
            }
            pending.insert(request_id.clone(), tx);
        }
        let _pending = PendingGuard {
            session,
            id: request_id,
        };
        let exchange = async {
            send_frame(session.stdin.clone(), frame, session.cancel.clone()).await?;
            match rx.await {
                Ok(Ok(v)) => Ok(v),
                Ok(Err(e)) => {
                    fault_log::record(&method_name, &e);
                    Err(plugin_error_to_core(e))
                }
                Err(_) => Err(CoreError::PluginUnavailable(
                    "adapter response channel closed".into(),
                )),
            }
        };
        let timeout = Duration::from_millis(
            deadline_ms
                .unwrap_or(DEFAULT_DEADLINE_MS)
                .min(MAX_DEADLINE_MS)
                .saturating_add(DEADLINE_SLACK_MS),
        );
        tokio::select! {
            value = tokio::time::timeout(timeout, exchange) => value.unwrap_or_else(|_| {
                Err(CoreError::Network("adapter deadline exceeded".into()))
            }),
            _ = session.cancel.cancelled() => {
                Err(CoreError::PluginUnavailable("adapter stopped".into()))
            },
        }
    }

    pub async fn reap_idle(&self) {
        let Ok(_startup) = self.startup.try_lock() else {
            return;
        };
        self.sessions.lock().await.retain(|_, s| {
            let idle = Arc::strong_count(s) == 1
                && s.pending.lock().unwrap().is_empty()
                && s.last_used.lock().unwrap().elapsed() > IDLE_EXIT;
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
        wait_exited(removed).await;
    }

    pub async fn shutdown_all(&self) {
        self.closed.store(true, std::sync::atomic::Ordering::SeqCst);
        let sessions: Vec<_> = self.sessions.lock().await.drain().map(|(_, s)| s).collect();
        for s in &sessions {
            s.cancel.cancel();
        }
        wait_exited(sessions).await;
    }
}

/// Gives cancelled sessions a moment to let their processes go.
async fn wait_exited(sessions: Vec<Arc<SessionInner>>) {
    let _ = tokio::time::timeout(KILL_GRACE, async {
        for s in sessions {
            s.exited.cancelled().await;
        }
    })
    .await;
}

/// Frees the request's pending slot even when the caller is dropped mid-wait.
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

fn plugin_error_to_core(e: PluginError) -> CoreError {
    if e.class == FailureClass::SourceUnavailable
        && e.details.get("source_state").and_then(Value::as_str) == Some("offline")
    {
        // Whitelist a source state, never relay the adapter's message/details.
        return CoreError::SourceUnavailable("room is not live".into());
    }
    CoreError::Adapter(e.class)
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
        AdapterSpec {
            plugin_id: "weibo".into(),
            platform: "weibo".into(),
            binary_path: exe("trace-adapter-weibo"),
        },
        AdapterSpec {
            plugin_id: "youtube".into(),
            platform: "youtube".into(),
            binary_path: exe("trace-adapter-youtube"),
        },
        AdapterSpec {
            plugin_id: "tiktok".into(),
            platform: "tiktok".into(),
            binary_path: exe("trace-adapter-tiktok"),
        },
        AdapterSpec {
            plugin_id: "xiaohongshu".into(),
            platform: "xiaohongshu".into(),
            binary_path: exe("trace-adapter-xiaohongshu"),
        },
        AdapterSpec {
            plugin_id: "instagram".into(),
            platform: "instagram".into(),
            binary_path: exe("trace-adapter-instagram"),
        },
        AdapterSpec {
            plugin_id: "x".into(),
            platform: "x".into(),
            binary_path: exe("trace-adapter-x"),
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

/// The one credential-derived value an adapter may see: Xiaohongshu's public list endpoint
/// needs the device identifier `a1` to sign its requests (DEC-041). The session Cookie,
/// `web_session` and every other name stay in Core; other platforms get no context at all.
fn access_context(platform: &str, cookie: Option<&str>, evidence_id: Option<&str>) -> Value {
    let mut context = serde_json::Map::new();
    if platform == "xiaohongshu"
        && let Some(a1) = cookie.and_then(|raw| {
            raw.split(';').find_map(|pair| {
                let (name, value) = pair.trim().split_once('=')?;
                (name.trim() == "a1").then(|| value.trim().to_owned())
            })
        })
        && (8..=128).contains(&a1.len())
        && a1
            .bytes()
            .all(|b| b.is_ascii_graphic() && b != b';' && b != b'=')
    {
        context.insert("device_id".into(), Value::String(a1));
    }
    if let Some(id) = evidence_id.filter(|id| {
        (4..=160).contains(&id.len())
            && id
                .bytes()
                .all(|b| b.is_ascii_alphanumeric() || matches!(b, b'-' | b'_' | b'.'))
    }) {
        context.insert("evidence_id".into(), Value::String(id.to_owned()));
    }
    if context.is_empty() {
        Value::Null
    } else {
        Value::Object(context)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    use tokio::io::{AsyncReadExt, AsyncWriteExt};

    #[test]
    fn only_the_device_identifier_reaches_the_site_that_signs_with_it() {
        let cookie = "a1=synthetic-device-123; web_session=synthetic-session; xsec_source=pc_user";
        assert_eq!(
            access_context("xiaohongshu", Some(cookie), None),
            json!({"device_id": "synthetic-device-123"})
        );
        assert!(access_context("weibo", Some(cookie), None).is_null());
        assert!(
            access_context("xiaohongshu", Some("web_session=synthetic-session"), None).is_null()
        );
        assert!(access_context("xiaohongshu", Some("a1=bad;value"), None).is_null());
        assert!(access_context("xiaohongshu", None, None).is_null());
        assert_eq!(
            access_context("weibo", None, Some("ctx-safe")),
            json!({"evidence_id": "ctx-safe"})
        );
        assert!(access_context("weibo", None, Some("not safe/context")).is_null());
    }

    #[test]
    fn offline_source_errors_keep_safe_state_without_adapter_payload() {
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
            PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../target/debug/host_probe.exe");
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
    async fn credential_scopes_and_cross_origin_redirect_strip_headers() {
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
    async fn plugin_process_isolation_and_cancelled_request_cleanup() {
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
    async fn slow_proxy_does_not_block_same_scope_responses_and_idle_exit() {
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
    fn failure_class_survives_without_untrusted_error_text() {
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
    async fn proxy_completion_cannot_cancel_partial_frame_read() {
        let binary =
            PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../target/debug/frame_probe.exe");
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
