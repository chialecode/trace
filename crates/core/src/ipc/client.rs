//! IPC 客户端：GUI 外壳、CLI 与测试共用。
//!
//! 单 reader 任务统一接收服务端帧：响应按 request_id 路由，事件转发
//! 到 `events` 通道；写半部独占锁。

use crate::error::CoreError;
use std::collections::HashMap;
use std::sync::Arc;
use std::time::Duration;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::sync::{Mutex, mpsc, oneshot};
use tokio_util::sync::CancellationToken;
use trace_contracts::control::{HelloRequest, HelloResponse, Request, Response, ServerFrame};
use trace_contracts::events::EventFrame;
use trace_contracts::wire;
use trace_domain::CONTROL_PROTOCOL_VERSION;

/// 打开管道客户端；ERROR_PIPE_BUSY 时短暂重试（服务端按需创建实例）。
async fn open_pipe_with_retry(
    pipe_name: &str,
) -> Result<tokio::net::windows::named_pipe::NamedPipeClient, CoreError> {
    let mut last = None;
    for _ in 0..24 {
        match tokio::net::windows::named_pipe::ClientOptions::new().open(pipe_name) {
            Ok(c) => return Ok(c),
            Err(e) if e.raw_os_error() == Some(231) => {
                last = Some(e);
                tokio::time::sleep(Duration::from_millis(100)).await;
            }
            Err(e) => return Err(CoreError::Io(e)),
        }
    }
    Err(CoreError::Io(last.unwrap_or_else(|| {
        std::io::Error::new(std::io::ErrorKind::WouldBlock, "pipe busy")
    })))
}

pub enum ClientEvent {
    Change(trace_contracts::events::ChangeEvent),
    Progress(trace_contracts::events::ProgressEvent),
    ResyncRequired(String),
}

/// request_id → 等待中的响应通道。
type Pending = HashMap<String, oneshot::Sender<Result<Response, CoreError>>>;

/// 单连接写半部（与服务端单实例全双工会话一致）。
type PipeWrite = tokio::io::WriteHalf<tokio::net::windows::named_pipe::NamedPipeClient>;

pub struct Client {
    writer: Mutex<PipeWrite>,
    pending: Arc<Mutex<Pending>>,
    pub hello: HelloResponse,
    next_request_id: std::sync::atomic::AtomicU64,
    pub events: mpsc::Receiver<ClientEvent>,
    cancel: CancellationToken,
}

impl Drop for Client {
    fn drop(&mut self) {
        // Media responses use short-lived clients. Release their reader and pipe too.
        self.cancel.cancel();
    }
}

impl Client {
    /// 连接 + 握手；启动事件泵。
    pub async fn connect(
        pipe_name: &str,
        client: &str,
        archive_id: Option<&str>,
    ) -> Result<Self, CoreError> {
        // 单连接：服务端每个实例是一条全双工会话；实例按需创建，
        // 空闲实例暂缺时 ERROR_PIPE_BUSY(231) 短暂重试（不无限等待）。
        let mut conn = open_pipe_with_retry(pipe_name).await?;

        let hello_req = HelloRequest {
            client: client.into(),
            protocol_version: CONTROL_PROTOCOL_VERSION.into(),
            archive_id: archive_id.map(|s| s.to_string()),
        };
        let frame =
            wire::encode_frame(&hello_req).map_err(|e| CoreError::Internal(format!("{e}")))?;
        conn.write_all(&frame).await.map_err(CoreError::Io)?;
        conn.flush().await.map_err(CoreError::Io)?;
        let first = read_frame(&mut conn).await?;
        let hello = match first {
            ServerFrame::Hello(h) => h,
            _ => return Err(CoreError::InvalidInput("expected hello".into())),
        };
        if hello.protocol_version != CONTROL_PROTOCOL_VERSION {
            return Err(CoreError::Unsupported(format!(
                "control protocol mismatch: {}",
                hello.protocol_version
            )));
        }
        // 握手完成后拆分读写半部：读半部交给事件泵，写半部供请求使用。
        let (io, writer) = tokio::io::split(conn);

        let pending: Arc<Mutex<Pending>> = Arc::new(Mutex::new(Pending::new()));
        let (tx, rx) = mpsc::channel(256);
        let cancel = CancellationToken::new();
        let cancel2 = cancel.clone();
        let pending2 = pending.clone();
        tokio::spawn(async move {
            let mut io = io;
            loop {
                tokio::select! {
                    frame = read_frame(&mut io) => {
                        match frame {
                            Ok(ServerFrame::Response(resp)) => {
                                if let Some(txp) = pending2.lock().await.remove(&resp.request_id) {
                                    if resp.ok {
                                        let _ = txp.send(Ok(resp));
                                    } else {
                                        let _ = txp.send(Err(CoreError::InvalidInput(match &resp.error {
                                            Some(err) => format!("{:?}: {}", err.code, err.user_action.clone().unwrap_or_default()),
                                            None => "error".into(),
                                        })));
                                    }
                                }
                            }
                            Ok(ServerFrame::Event(EventFrame::Change(c))) => {
                                if tx.try_send(ClientEvent::Change(c)).is_err() {
                                    break;
                                }
                            }
                            Ok(ServerFrame::Event(EventFrame::Progress(p))) => {
                                let _ = tx.try_send(ClientEvent::Progress(p));
                            }
                            Ok(ServerFrame::Event(EventFrame::ResyncRequired { reason })) => {
                                let _ = tx.try_send(ClientEvent::ResyncRequired(reason));
                            }
                            Ok(ServerFrame::Hello(_)) => {}
                            Ok(_) => {}
                            Err(_) => break,
                        }
                    }
                    _ = cancel2.cancelled() => break,
                }
            }
            // 唤醒所有 pending，避免悬挂。
            let mut map = pending2.lock().await;
            for (_, txp) in map.drain() {
                let _ = txp.send(Err(CoreError::Internal("connection closed".into())));
            }
        });

        Ok(Self {
            writer: Mutex::new(writer),
            pending,
            hello,
            next_request_id: std::sync::atomic::AtomicU64::new(1),
            events: rx,
            cancel,
        })
    }

    /// 单次请求/响应。断开连接不取消已受理的后台任务（契约 §2）。
    pub async fn request(
        &self,
        command: &str,
        args: serde_json::Value,
        idempotency_key: Option<String>,
        if_revision: Option<i64>,
    ) -> Result<serde_json::Value, CoreError> {
        let request_id = format!(
            "req-{}",
            self.next_request_id
                .fetch_add(1, std::sync::atomic::Ordering::SeqCst)
        );
        let req = Request {
            protocol_version: CONTROL_PROTOCOL_VERSION.into(),
            request_id: request_id.clone(),
            command: command.into(),
            idempotency_key,
            if_revision,
            args,
        };
        let (tx, rx) = oneshot::channel();
        self.pending.lock().await.insert(request_id.clone(), tx);
        let frame = wire::encode_frame(&req).map_err(|e| CoreError::Internal(format!("{e}")))?;
        {
            let mut writer = self.writer.lock().await;
            writer.write_all(&frame).await.map_err(CoreError::Io)?;
            writer.flush().await.map_err(CoreError::Io)?;
        }
        let resp = tokio::time::timeout(Duration::from_secs(60), rx).await;
        match resp {
            Ok(Ok(Ok(r))) => {
                if r.request_id != request_id {
                    return Err(CoreError::Internal("response id mismatch".into()));
                }
                Ok(r.result.unwrap_or(serde_json::Value::Null))
            }
            Ok(Ok(Err(e))) => Err(e),
            Ok(Err(_)) => Err(CoreError::Internal("response channel closed".into())),
            Err(_) => {
                self.pending.lock().await.remove(&request_id);
                Err(CoreError::Network("ipc timeout".into()))
            }
        }
    }

    pub async fn shutdown(self) {
        self.cancel.cancel();
    }
}

async fn read_frame(reader: &mut (impl AsyncReadExt + Unpin)) -> Result<ServerFrame, CoreError> {
    let mut len_buf = [0u8; 4];
    reader
        .read_exact(&mut len_buf)
        .await
        .map_err(CoreError::Io)?;
    let len = u32::from_le_bytes(len_buf);
    if len > trace_domain::MAX_FRAME_BYTES {
        return Err(CoreError::InvalidInput("frame too large".into()));
    }
    let mut buf = vec![0u8; len as usize];
    reader.read_exact(&mut buf).await.map_err(CoreError::Io)?;
    serde_json::from_slice(&buf).map_err(CoreError::Json)
}
