//! One adapter process: how it is launched, how its stdio frames are read and written, and
//! what the reader does with each message.
use super::proxy::{ACCEPTED, OCCUPIED_AT, POOL_LIMIT, ProxyContext};
use super::{AdapterSpec, PendingCalls};
use crate::error::CoreError;
use serde_json::Value;
use std::collections::HashMap;
use std::process::Stdio;
use std::sync::Arc;
use std::time::{Duration, Instant};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::sync::Mutex;
use tokio_util::sync::CancellationToken;
use trace_contracts::plugin::{AdapterHello, PluginError, PluginRequest, PluginResponse};
use trace_contracts::plugin_chunks::Decoder;
use trace_domain::errors::FailureClass;

/// Windows `CREATE_NO_WINDOW`: implicitly started adapters show no console window.
const CREATE_NO_WINDOW: u32 = 0x0800_0000;
const FRAME_WRITE_DEADLINE: Duration = Duration::from_secs(25);
const CHILD_EXIT_GRACE: Duration = Duration::from_secs(2);
const STDERR_LOGGED_ACTIVITY: u64 = 3;

pub(super) struct SessionInner {
    pub stdin: Arc<Mutex<tokio::process::ChildStdin>>,
    pub pending: Arc<std::sync::Mutex<PendingCalls>>,
    pub hello: Arc<Mutex<AdapterHello>>,
    pub cancel: CancellationToken,
    pub exited: CancellationToken,
    pub last_used: std::sync::Mutex<Instant>,
}

impl SessionInner {
    /// A session whose handshake has not happened yet.
    pub(super) fn new(stdin: tokio::process::ChildStdin, platform: &str) -> Self {
        Self {
            stdin: Arc::new(Mutex::new(stdin)),
            pending: Arc::new(std::sync::Mutex::new(HashMap::new())),
            hello: Arc::new(Mutex::new(AdapterHello {
                protocol_version: trace_domain::PLUGIN_PROTOCOL_VERSION.into(),
                adapter_version: "0".into(),
                platform: platform.into(),
                capabilities: vec![],
                limits: Default::default(),
                rules: Vec::new(),
            })),
            cancel: CancellationToken::new(),
            exited: CancellationToken::new(),
            last_used: std::sync::Mutex::new(Instant::now()),
        }
    }
}

/// A started adapter process with its pipes taken.
pub(super) struct Launched {
    pub child: tokio::process::Child,
    pub stdin: tokio::process::ChildStdin,
    pub stdout: tokio::process::ChildStdout,
    pub stderr: Option<tokio::process::ChildStderr>,
}

pub(super) fn launch(spec: &AdapterSpec) -> Result<Launched, CoreError> {
    let unavailable = |what: &str| CoreError::PluginUnavailable(what.into());
    let mut child = tokio::process::Command::new(&spec.binary_path)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .creation_flags(CREATE_NO_WINDOW)
        .kill_on_drop(true)
        .spawn()
        .map_err(|e| CoreError::PluginUnavailable(format!("spawn adapter: {e}")))?;
    let stdin = child.stdin.take().ok_or_else(|| unavailable("no stdin"))?;
    let stdout = child
        .stdout
        .take()
        .ok_or_else(|| unavailable("no stdout"))?;
    let stderr = child.stderr.take();
    Ok(Launched {
        child,
        stdin,
        stdout,
        stderr,
    })
}

/// stderr is the adapter's diagnostic channel: counted, not forwarded, so sensitive content
/// never reaches the log.
pub(super) async fn drain_stderr(mut stderr: tokio::process::ChildStderr) {
    let mut activity = 0u64;
    let mut buf = vec![0u8; 4096];
    loop {
        match stderr.read(&mut buf).await {
            Ok(0) | Err(_) => break,
            Ok(_) => {
                activity += 1;
                if activity <= STDERR_LOGGED_ACTIVITY {
                    tracing::debug!("adapter stderr activity (not echoed)");
                }
            }
        }
    }
}

// An abandoned caller cannot abandon a partially written stdio frame. The
// dedicated send task either finishes the frame or invalidates the session.
pub(super) async fn send_frame(
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
            _ = cancel.cancelled() => Err(CoreError::Cancelled),
            result = tokio::time::timeout(FRAME_WRITE_DEADLINE, write) => match result {
                Ok(Ok(())) => Ok(()),
                _ => Err(CoreError::PluginUnavailable("adapter frame write failed".into())),
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

pub(super) async fn read_frame(
    reader: &mut (impl AsyncReadExt + Unpin),
) -> std::io::Result<Vec<u8>> {
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

pub(super) fn response_to_result(resp: PluginResponse) -> Result<Value, PluginError> {
    if resp.ok {
        Ok(resp.result.unwrap_or(Value::Null))
    } else {
        Err(resp.error.unwrap_or(PluginError {
            class: FailureClass::Unknown,
            message: "adapter error".into(),
            retry_after_ms: None,
            details: Value::Null,
        }))
    }
}

/// Whether the reader goes on after a message.
enum Flow {
    Continue,
    Stop,
}

/// Reads the adapter's frames until it exits or the session is cancelled. It deliberately
/// holds none of the session's `Arc`, whose count tells idle sessions from busy ones.
pub(super) struct Reader {
    pub stdout: tokio::process::ChildStdout,
    pub child: tokio::process::Child,
    pub pending: Arc<std::sync::Mutex<PendingCalls>>,
    pub cancel: CancellationToken,
    pub exited: CancellationToken,
    pub proxy: ProxyContext,
}

impl Reader {
    pub(super) async fn run(mut self) {
        let mut proxy_tasks = tokio::task::JoinSet::new();
        let mut chunks = Decoder::default();
        while let Some(Ok(bytes)) = self.next_frame(&mut proxy_tasks).await {
            if let Flow::Stop = self.handle(&bytes, &mut chunks, &mut proxy_tasks) {
                break;
            }
        }
        self.finish(proxy_tasks).await;
    }

    /// `None` when the session was cancelled while waiting.
    async fn next_frame(
        &mut self,
        proxy_tasks: &mut tokio::task::JoinSet<()>,
    ) -> Option<std::io::Result<Vec<u8>>> {
        // read_exact consumes bytes before yielding and is not cancel safe. Keep this
        // future alive when a proxy finishes mid-frame.
        let reading = read_frame(&mut self.stdout);
        tokio::pin!(reading);
        loop {
            tokio::select! {
                _ = proxy_tasks.join_next(), if !proxy_tasks.is_empty() => {},
                frame = &mut reading => return Some(frame),
                _ = self.cancel.cancelled() => return None,
            }
        }
    }

    fn handle(
        &self,
        bytes: &[u8],
        chunks: &mut Decoder,
        proxy_tasks: &mut tokio::task::JoinSet<()>,
    ) -> Flow {
        let Ok(msg) = serde_json::from_slice::<Value>(bytes) else {
            return Flow::Continue;
        };
        let msg = match chunks.push(msg) {
            Ok(Some(v)) => v,
            Ok(None) => return Flow::Continue,
            Err(_) => return Flow::Stop,
        };
        if msg.get("ok").is_some() && msg.get("method").is_none() {
            self.deliver(msg);
            return Flow::Continue;
        }
        let is_proxy = matches!(
            msg.get("method").and_then(|m| m.as_str()),
            Some("proxy.fetch" | "proxy.fetch_large")
        );
        if is_proxy {
            return self.spawn_proxy(msg, proxy_tasks);
        }
        Flow::Continue
    }

    /// Hands a response to the call waiting for it.
    fn deliver(&self, msg: Value) {
        let Ok(resp) = serde_json::from_value::<PluginResponse>(msg) else {
            return;
        };
        if let Some(tx) = self.pending.lock().unwrap().remove(&resp.request_id) {
            let _ = tx.send(response_to_result(resp));
        }
    }

    /// Keeps reading responses while one upstream is slow; the bounded pool cannot stall
    /// unrelated in-flight calls.
    fn spawn_proxy(&self, msg: Value, proxy_tasks: &mut tokio::task::JoinSet<()>) -> Flow {
        let Ok(req) = serde_json::from_value::<PluginRequest>(msg) else {
            return Flow::Continue;
        };
        if proxy_tasks.len() >= POOL_LIMIT {
            return Flow::Stop;
        }
        let occupied = proxy_tasks.len() >= OCCUPIED_AT;
        self.proxy.count(ACCEPTED);
        proxy_tasks.spawn(self.proxy.clone().serve(req, occupied));
        Flow::Continue
    }

    async fn finish(mut self, mut proxy_tasks: tokio::task::JoinSet<()>) {
        self.cancel.cancel();
        proxy_tasks.abort_all();
        self.pending.lock().unwrap().clear();
        let _ = self.child.start_kill();
        let _ = tokio::time::timeout(CHILD_EXIT_GRACE, self.child.wait()).await;
        self.exited.cancel();
    }
}
