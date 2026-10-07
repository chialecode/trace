//! named pipe 服务端：当前用户 ACL + 拒绝远程 + 握手（C09/C22）。
//!
//! 首实例经 CreateNamedPipeW 携带受保护 DACL（当前用户+SYSTEM+Admins）
//! 与 FILE_FLAG_FIRST_PIPE_INSTANCE 创建；后续实例继承同一管道对象。
//! 拒绝远程客户端（PIPE_REJECT_REMOTE_CLIENTS）；安全边界不依赖端点名
//! 的不可猜测性。

use crate::error::CoreError;
use std::os::windows::io::RawHandle;
use std::sync::Arc;
use std::time::Duration;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use trace_contracts::control::{
    ApiError, HelloRequest, HelloResponse, RecoverySnapshot, Request, Response, ServerFrame,
};
use trace_contracts::events::{ChangeEvent, EventFrame};
use trace_contracts::wire;
use trace_domain::CONTROL_PROTOCOL_VERSION;
use windows::Win32::Foundation::{CloseHandle, GetLastError, HANDLE, HLOCAL, LocalFree};
use windows::Win32::Security::Authorization::{
    ConvertSecurityDescriptorToStringSecurityDescriptorW, ConvertSidToStringSidW,
    ConvertStringSecurityDescriptorToSecurityDescriptorW, GetSecurityInfo, SDDL_REVISION_1,
    SE_FILE_OBJECT,
};
use windows::Win32::Security::{
    DACL_SECURITY_INFORMATION, GetTokenInformation, PSECURITY_DESCRIPTOR, SECURITY_ATTRIBUTES,
    TOKEN_QUERY, TOKEN_USER, TokenUser,
};
use windows::Win32::Storage::FileSystem::{
    CreateFileW, FILE_FLAG_OVERLAPPED, FILE_GENERIC_READ, FILE_SHARE_READ, FILE_SHARE_WRITE,
    OPEN_EXISTING,
};
use windows::Win32::System::Pipes::*;
use windows::Win32::System::Threading::{GetCurrentProcess, OpenProcessToken};
use windows::core::PCWSTR;

/// PIPE_ACCESS_DUPLEX = 0x3（GENERIC_READ|GENERIC_WRITE）。
const PIPE_ACCESS_DUPLEX: u32 = 0x0000_0003;
/// FILE_FLAG_FIRST_PIPE_INSTANCE = 0x00080000。
const FILE_FLAG_FIRST_PIPE_INSTANCE: u32 = 0x0008_0000;

/// 当前用户 SID 字符串。
pub fn current_user_sid() -> Result<String, CoreError> {
    unsafe {
        let mut token = HANDLE::default();
        OpenProcessToken(GetCurrentProcess(), TOKEN_QUERY, &mut token)
            .map_err(|e| CoreError::Internal(format!("open token: {e}")))?;
        let mut len = 0u32;
        let _ = GetTokenInformation(token, TokenUser, None, 0, &mut len);
        let mut buf = vec![0u8; len as usize];
        GetTokenInformation(
            token,
            TokenUser,
            Some(buf.as_mut_ptr() as *mut _),
            len,
            &mut len,
        )
        .map_err(|e| CoreError::Internal(format!("token info: {e}")))?;
        let user = buf.as_ptr() as *const TOKEN_USER;
        let mut sid_str = windows::core::PWSTR::default();
        ConvertSidToStringSidW((*user).User.Sid, &mut sid_str)
            .map_err(|e| CoreError::Internal(format!("sid: {e}")))?;
        let s = sid_str
            .to_string()
            .map_err(|e| CoreError::Internal(format!("{e}")))?;
        let _ = LocalFree(Some(HLOCAL(sid_str.0 as *mut core::ffi::c_void)));
        Ok(s)
    }
}

/// 读取对象 DACL 的 SDDL 字符串（用于 ACL 断言测试）。
pub fn read_dacl_sddl(handle: HANDLE) -> Result<String, CoreError> {
    unsafe {
        let mut sd: PSECURITY_DESCRIPTOR = PSECURITY_DESCRIPTOR(core::ptr::null_mut());
        let status = GetSecurityInfo(
            handle,
            SE_FILE_OBJECT,
            DACL_SECURITY_INFORMATION,
            None,
            None,
            None,
            None,
            Some(&mut sd),
        );
        if status.0 != 0 {
            return Err(CoreError::Internal(format!("get security: {status:?}")));
        }
        let mut sddl = windows::core::PWSTR::default();
        let mut len = 0u32;
        ConvertSecurityDescriptorToStringSecurityDescriptorW(
            sd,
            SDDL_REVISION_1,
            DACL_SECURITY_INFORMATION,
            &mut sddl,
            Some(&mut len),
        )
        .map_err(|e| CoreError::Internal(format!("sddl str: {e}")))?;
        let s = sddl
            .to_string()
            .map_err(|e| CoreError::Internal(format!("{e}")))?;
        let _ = LocalFree(Some(HLOCAL(sddl.0 as *mut core::ffi::c_void)));
        let _ = LocalFree(Some(HLOCAL(sd.0)));
        Ok(s)
    }
}

/// 校验管道 DACL：仅当前用户/SYSTEM/Administrators 拥有允许 ACE（C22）。
pub fn verify_pipe_acl(pipe_name: &str, sid: &str) -> Result<bool, CoreError> {
    let wide: Vec<u16> = pipe_name.encode_utf16().chain([0]).collect();
    unsafe {
        let handle = CreateFileW(
            PCWSTR(wide.as_ptr()),
            FILE_GENERIC_READ.0,
            FILE_SHARE_READ | FILE_SHARE_WRITE,
            None,
            OPEN_EXISTING,
            FILE_FLAG_OVERLAPPED,
            None,
        )
        .map_err(|e| CoreError::Internal(format!("open pipe: {e}")))?;
        let h = HANDLE(handle.0);
        let sddl = read_dacl_sddl(h)?;
        let _ = CloseHandle(h);
        // 受保护 DACL（D:P），每个允许 ACE 的受托者是当前用户/SY/BA。
        let ok = sddl.contains("D:P")
            && sddl.matches('A').count() > 0
            && sddl.contains(&format!("(A;;FA;;;{sid})"))
            && sddl.contains("(A;;FA;;;SY)")
            && sddl.contains("(A;;FA;;;BA)");
        Ok(ok)
    }
}

fn create_first_instance(
    name: &str,
) -> Result<tokio::net::windows::named_pipe::NamedPipeServer, CoreError> {
    let sid = current_user_sid()?;
    let sddl = windows::core::HSTRING::from(format!("D:P(A;;FA;;;{sid})(A;;FA;;;SY)(A;;FA;;;BA)"));
    unsafe {
        let mut psd_ptr: PSECURITY_DESCRIPTOR = PSECURITY_DESCRIPTOR(core::ptr::null_mut());
        ConvertStringSecurityDescriptorToSecurityDescriptorW(
            PCWSTR(sddl.as_ptr()),
            SDDL_REVISION_1,
            &mut psd_ptr,
            None,
        )
        .map_err(|e| CoreError::Internal(format!("sddl: {e}")))?;
        let sa = SECURITY_ATTRIBUTES {
            nLength: std::mem::size_of::<SECURITY_ATTRIBUTES>() as u32,
            lpSecurityDescriptor: psd_ptr.0,
            bInheritHandle: false.into(),
        };
        let wide: Vec<u16> = name.encode_utf16().chain([0]).collect();
        let handle = CreateNamedPipeW(
            PCWSTR(wide.as_ptr()),
            windows::Win32::Storage::FileSystem::FILE_FLAGS_AND_ATTRIBUTES(
                PIPE_ACCESS_DUPLEX | FILE_FLAG_FIRST_PIPE_INSTANCE | 0x4000_0000,
            ),
            PIPE_TYPE_BYTE | PIPE_READMODE_BYTE | PIPE_WAIT | PIPE_REJECT_REMOTE_CLIENTS,
            PIPE_UNLIMITED_INSTANCES,
            1_048_576,
            1_048_576,
            0,
            Some(&sa),
        );
        // 安全描述符已复制进内核对象；释放临时内存。
        let _ = LocalFree(Some(HLOCAL(psd_ptr.0)));
        if handle.is_invalid() {
            return Err(CoreError::Internal(format!(
                "CreateNamedPipeW failed: {:?}",
                GetLastError()
            )));
        }
        tokio::net::windows::named_pipe::NamedPipeServer::from_raw_handle(handle.0 as RawHandle)
            .map_err(|e| CoreError::Internal(format!("wrap pipe handle: {e}")))
    }
}

pub struct IpcServer {
    pub core: Arc<crate::core::Core>,
}

impl IpcServer {
    pub async fn serve(self) -> Result<(), CoreError> {
        let name = self.core.profile.pipe_name();
        let mut server = create_first_instance(&name)?;
        let core = self.core.clone();
        tracing::info!(%name, "pipe server listening");
        loop {
            server.connect().await.map_err(CoreError::Io)?;
            let connected = server;
            server = tokio::net::windows::named_pipe::ServerOptions::new()
                .create(&name)
                .map_err(CoreError::Io)?;
            let core2 = core.clone();
            tokio::spawn(async move {
                if let Err(e) = handle_session(connected, core2).await {
                    tracing::debug!("session ended: {e}");
                }
            });
        }
    }
}

async fn handle_session(
    mut server: tokio::net::windows::named_pipe::NamedPipeServer,
    core: Arc<crate::core::Core>,
) -> Result<(), CoreError> {
    // 握手：第一帧必须是 HelloRequest；archive 不匹配拒绝（C22）。
    let hello_bytes = read_frame_bytes(&mut server).await?;
    let hello: HelloRequest = serde_json::from_slice(&hello_bytes)
        .map_err(|_| CoreError::InvalidInput("bad hello".into()))?;
    let archive_id = core.profile.archive()?.as_str().to_string();
    if let Some(client_archive) = &hello.archive_id
        && client_archive != &archive_id
    {
        let err = error_frame("", CoreError::InvalidInput("archive mismatch".into()));
        write_frame(&mut server, &err).await?;
        return Err(CoreError::InvalidInput("archive mismatch".into()));
    }
    let hello_resp = HelloResponse {
        protocol_version: CONTROL_PROTOCOL_VERSION.into(),
        archive_id,
        schema_version: trace_domain::SCHEMA_VERSION,
        plugin_protocol: trace_domain::PLUGIN_PROTOCOL_VERSION.into(),
        node_id: core.node_id.as_str().into(),
        instance_id: core.instance_id.as_str().into(),
        capabilities: vec!["control".into(), "events".into(), "media".into()],
        paused: core.paused.load(std::sync::atomic::Ordering::SeqCst),
        recovery: RecoverySnapshot::default(),
    };
    write_frame(&mut server, &ServerFrame::Hello(hello_resp)).await?;

    // 主循环：请求→响应；Outbox 增量事件与进度事件穿插。
    let mut outbox_seq: i64 = core
        .db
        .read(|c| Ok(c.query_row("SELECT COALESCE(max(seq),0) FROM outbox", [], |r| r.get(0))?))
        .await?;
    let mut decoder = FrameReader::default();
    let mut event_tick = tokio::time::interval(Duration::from_millis(400));
    let mut progress_rx = core.hub.subscribe();
    loop {
        tokio::select! {
            frame = decoder.read(&mut server) => {
                let bytes = frame?;
                let req: Request = match serde_json::from_slice(&bytes) {
                    Ok(r) => r,
                    Err(_) => {
                        let resp = error_frame("", CoreError::InvalidInput("bad request".into()));
                        write_frame(&mut server, &resp).await?;
                        continue;
                    }
                };
                let resp = crate::ipc::dispatch::dispatch(&core, req).await;
                write_frame(&mut server, &ServerFrame::Response(resp)).await?;
            }
            pe = progress_rx.recv() => {
                if let Ok(pe) = pe {
                    write_frame(&mut server, &ServerFrame::Event(EventFrame::Progress(pe))).await?;
                }
            }
            _ = event_tick.tick() => {
                let events: Vec<(i64, String, String, String, String)> = {
                    let start = outbox_seq;
                    core.db.read(move |c| {
                        let mut stmt = c.prepare(
                            "SELECT seq, topic, aggregate_id, payload, created_at FROM outbox WHERE seq > ?1 ORDER BY seq LIMIT 64",
                        )?;
                        let rows = stmt.query_map([start], |r| {
                            Ok((r.get(0)?, r.get(1)?, r.get(2)?, r.get(3)?, r.get(4)?))
                        })?.collect::<Result<Vec<_>, _>>()?;
                        Ok(rows)
                    }).await?
                };
                for (seq, topic, aggregate_id, payload, created_at) in events {
                    outbox_seq = seq;
                    let frame = ServerFrame::Event(EventFrame::Change(ChangeEvent {
                        seq: seq.to_string(),
                        topic,
                        aggregate_id,
                        payload: serde_json::from_str(&payload).unwrap_or_default(),
                        occurred_at: created_at,
                    }));
                    write_frame(&mut server, &frame).await?;
                }
            }
        }
    }
}

fn error_frame(request_id: &str, e: CoreError) -> ServerFrame {
    ServerFrame::Response(Response {
        protocol_version: CONTROL_PROTOCOL_VERSION.into(),
        request_id: request_id.into(),
        ok: false,
        result: None,
        error: Some(ApiError {
            code: e.error_code(),
            retryable: false,
            user_action: None,
            related_ids: vec![],
            details: serde_json::json!({}),
        }),
    })
}

async fn write_frame(
    server: &mut tokio::net::windows::named_pipe::NamedPipeServer,
    frame: &ServerFrame,
) -> Result<(), CoreError> {
    let bytes = wire::encode_frame(frame).map_err(|e| CoreError::Internal(format!("{e}")))?;
    server.write_all(&bytes).await.map_err(CoreError::Io)?;
    server.flush().await.map_err(CoreError::Io)?;
    Ok(())
}

/// 读一帧原始字节（先校验长度上限再分配，C22）。
pub async fn read_frame_bytes(
    reader: &mut (impl AsyncReadExt + Unpin),
) -> Result<Vec<u8>, CoreError> {
    let mut len_buf = [0u8; 4];
    reader
        .read_exact(&mut len_buf)
        .await
        .map_err(|e| CoreError::Io(std::io::Error::new(e.kind(), e.to_string())))?;
    let len = u32::from_le_bytes(len_buf);
    if len > trace_domain::MAX_FRAME_BYTES {
        return Err(CoreError::InvalidInput(format!("frame too large: {len}")));
    }
    let mut buf = vec![0u8; len as usize];
    reader.read_exact(&mut buf).await.map_err(CoreError::Io)?;
    Ok(buf)
}

/// Offsets live outside the selected future. `read` is cancellation-safe; `read_exact` is not.
#[derive(Default)]
pub struct FrameReader {
    header: [u8; 4],
    header_read: usize,
    body: Vec<u8>,
    body_read: usize,
}
impl FrameReader {
    pub async fn read(
        &mut self,
        reader: &mut (impl AsyncReadExt + Unpin),
    ) -> Result<Vec<u8>, CoreError> {
        while self.header_read < 4 {
            let n = reader.read(&mut self.header[self.header_read..]).await?;
            if n == 0 {
                return Err(CoreError::Network("IPC closed".into()));
            }
            self.header_read += n;
        }
        let len = u32::from_le_bytes(self.header) as usize;
        if len > trace_domain::MAX_FRAME_BYTES as usize {
            return Err(CoreError::InvalidInput("frame too large".into()));
        }
        self.body.resize(len, 0);
        while self.body_read < len {
            let n = reader.read(&mut self.body[self.body_read..]).await?;
            if n == 0 {
                return Err(CoreError::Network("IPC closed".into()));
            }
            self.body_read += n;
        }
        self.header_read = 0;
        self.body_read = 0;
        Ok(std::mem::take(&mut self.body))
    }
}

#[cfg(test)]
mod framing_tests {
    use super::*;
    #[tokio::test]
    async fn f013_fragmented_frame_survives_competing_events() {
        let (mut tx, mut rx) = tokio::io::duplex(32);
        let payload = vec![42; 800_000];
        let expected = payload.clone();
        let sender = tokio::spawn(async move {
            tx.write_all(&(payload.len() as u32).to_le_bytes())
                .await
                .unwrap();
            for piece in payload.chunks(333) {
                tx.write_all(piece).await.unwrap();
                tokio::task::yield_now().await;
            }
        });
        let mut decoder = FrameReader::default();
        let result = loop {
            tokio::select! {
                result = decoder.read(&mut rx) => break result.unwrap(),
                _ = tokio::task::yield_now() => {},
            }
        };
        assert_eq!(result, expected);
        sender.await.unwrap();
    }
}
