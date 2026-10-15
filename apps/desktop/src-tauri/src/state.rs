//! 外壳运行状态：daemon 生命周期与控制协议客户端。
//!
//! 外壳不做领域写入（契约 §5）：所有业务经控制协议访问独立 daemon；
//! 外壳只在 daemon 未运行时以无窗口方式拉起（先探测，不凭 PID 判断）。

use std::sync::{
    Arc,
    atomic::{AtomicBool, Ordering},
};
use tauri::Emitter;
use tokio::sync::Mutex;
use trace_core::config::Profile;
use trace_core::ipc::client::Client;

pub struct AppState {
    pub profile: Profile,
    client: Mutex<Option<Arc<Client>>>,
    joined: AtomicBool,
    launch: Mutex<()>,
    pub exiting: AtomicBool,
}

impl AppState {
    pub fn new(profile: Profile) -> Self {
        Self {
            profile,
            client: Mutex::new(None),
            joined: AtomicBool::new(false),
            launch: Mutex::new(()),
            exiting: AtomicBool::new(false),
        }
    }

    /// 取已连接客户端；未连接则连接（不拉起 daemon）。
    pub async fn client(&self) -> Result<Arc<Client>, String> {
        if self.exiting.load(Ordering::SeqCst) {
            return Err("正在完全退出，已停止发送新请求".into());
        }
        let mut guard = self.client.lock().await;
        if let Some(c) = guard.as_ref() {
            return Ok(c.clone());
        }
        let client = Client::connect(
            &self.profile.pipe_name(),
            "desktop",
            Some(&self.profile.archive_id),
        )
        .await
        .map_err(|e| format!("daemon 未运行或连接失败：{e}"))?;
        let c = Arc::new(client);
        self.joined.store(true, Ordering::SeqCst);
        *guard = Some(c.clone());
        Ok(c)
    }

    /// 失效缓存连接（断开后重连）。
    pub async fn invalidate(&self) {
        *self.client.lock().await = None;
    }
}

/// 定位 daemon 可执行文件：env 覆盖 → 主程序同目录 → PATH。
pub fn daemon_path() -> std::path::PathBuf {
    if let Ok(p) = std::env::var("TRACE_DAEMON_PATH") {
        return std::path::PathBuf::from(p);
    }
    if let Ok(exe) = std::env::current_exe()
        && let Some(dir) = exe.parent()
    {
        let candidate = dir.join(if cfg!(windows) {
            "trace-daemon.exe"
        } else {
            "trace-daemon"
        });
        if candidate.exists() {
            return candidate;
        }
    }
    std::path::PathBuf::from("trace-daemon")
}

/// 拉起 daemon（无窗口）；ready 由后续连接握手确认。
pub fn spawn_daemon(_profile: &Profile) -> Result<(), String> {
    let path = daemon_path();
    let mut cmd = std::process::Command::new(&path);
    cmd.arg("start").arg("--profile").arg(profile_path());
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        const CREATE_NO_WINDOW: u32 = 0x0800_0000;
        cmd.creation_flags(CREATE_NO_WINDOW);
    }
    cmd.spawn()
        .map_err(|e| format!("启动 daemon 失败（{path:?}）：{e}"))?;
    Ok(())
}

/// 外壳使用的 profile：TRACE_PROFILE 环境变量优先，否则默认开发档案。
/// 打包版随程序目录携带 profile.json（P8 打包脚本写入）。
pub fn profile_path() -> std::path::PathBuf {
    let args: Vec<String> = std::env::args().collect();
    if let Some(p) = args
        .iter()
        .position(|a| a == "--profile")
        .and_then(|i| args.get(i + 1))
    {
        return std::path::PathBuf::from(p);
    }
    if let Ok(p) = std::env::var("TRACE_PROFILE") {
        return std::path::PathBuf::from(p);
    }
    if let Ok(exe) = std::env::current_exe() {
        let candidate = exe
            .parent()
            .unwrap_or(std::path::Path::new("."))
            .join("profile.json");
        if candidate.exists() {
            return candidate;
        }
    }
    std::path::PathBuf::from("profiles/dev/profile.json")
}

pub fn load_shell_profile() -> Profile {
    let path = profile_path();
    match trace_core::config::load_profile(&path) {
        Ok(p) => p,
        Err(_) => Profile {
            archive_id: "dev".into(),
            data_root: std::path::PathBuf::from("profiles/dev/data"),
            archive_root: std::path::PathBuf::from("profiles/dev/archive"),
            ..Profile::default()
        },
    }
}

/// 确认 daemon 可达；不可达则尝试拉起并轮询连接（最多 15s）。
pub async fn ensure_daemon(state: &AppState) -> Result<Arc<Client>, String> {
    let _launch = state.launch.lock().await;
    if state.exiting.load(Ordering::SeqCst) {
        return Err("正在完全退出，已停止发送新请求".into());
    }
    match state.client().await {
        Ok(c) => Ok(c),
        Err(_) => {
            // Once joined, a closed pipe means disconnected or intentionally
            // stopped. Another open window must not undo a full shutdown.
            if state.joined.load(Ordering::SeqCst) {
                return Err("后台已断开。重新打开 TRACE 可启动后台；现有窗口会等待重连。".into());
            }
            spawn_daemon(&state.profile)?;
            let mut last_err = String::new();
            for _ in 0..30 {
                tokio::time::sleep(std::time::Duration::from_millis(500)).await;
                match Client::connect(
                    &state.profile.pipe_name(),
                    "desktop",
                    Some(&state.profile.archive_id),
                )
                .await
                {
                    Ok(c) => {
                        state.joined.store(true, Ordering::SeqCst);
                        let c = Arc::new(c);
                        *state.client.lock().await = Some(c.clone());
                        return Ok(c);
                    }
                    Err(e) => last_err = e.to_string(),
                }
            }
            Err(format!("daemon 未在 15 秒内就绪：{last_err}"))
        }
    }
}

/// Request shutdown without starting a daemon, then wait for its process-held
/// archive lock. A disconnected IPC alone does not prove sealing has finished.
pub async fn shutdown_daemon(state: &AppState) -> Result<(), String> {
    let _launch = state.launch.lock().await;
    let client = state.client().await?;
    state.exiting.store(true, Ordering::SeqCst);
    if let Err(error) = client
        .request(
            trace_contracts::control::commands::DAEMON_SHUTDOWN,
            serde_json::json!({}),
            None,
            None,
        )
        .await
    {
        state.exiting.store(false, Ordering::SeqCst);
        state.invalidate().await;
        return Err(error.to_string());
    }
    let until = tokio::time::Instant::now() + std::time::Duration::from_secs(20);
    loop {
        match std::fs::OpenOptions::new()
            .read(true)
            .write(true)
            .open(state.profile.lock_path())
        {
            Ok(file) if file.try_lock().is_ok() => return Ok(()),
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(()),
            Err(_) => return Err("无法确认后台锁状态，请查看后台诊断。".into()),
            _ => {}
        }
        if tokio::time::Instant::now() >= until {
            return Err("后台仍在结束工作，请稍后检查；未强制终止进程。".into());
        }
        tokio::time::sleep(std::time::Duration::from_millis(100)).await;
    }
}

/// 事件泵：独占连接持续消费 daemon 事件并转发到 webview。
/// 断线自动重连；此连接不用于请求，避免命令通道被事件阻塞。
pub async fn event_pump<R: tauri::Runtime>(state: AppState, handle: tauri::AppHandle<R>)
where
    tauri::AppHandle<R>: tauri::Emitter<R>,
{
    loop {
        // 专用事件连接（每次重连新 Client，独占 events 接收端）。
        if let Ok(mut client) = Client::connect(
            &state.profile.pipe_name(),
            "desktop-events",
            Some(&state.profile.archive_id),
        )
        .await
        {
            let _ = handle.emit(
                "trace://resync",
                serde_json::json!({"reason":"connected-snapshot-required"}),
            );
            while let Some(ev) = client.events.recv().await {
                let payload = match ev {
                    trace_core::ipc::client::ClientEvent::Change(c) => {
                        ("trace://change", serde_json::to_value(c).ok())
                    }
                    trace_core::ipc::client::ClientEvent::Progress(p) => {
                        ("trace://progress", serde_json::to_value(p).ok())
                    }
                    trace_core::ipc::client::ClientEvent::ResyncRequired(reason) => (
                        "trace://resync",
                        Some(serde_json::json!({ "reason": reason })),
                    ),
                };
                if let Some(v) = payload.1 {
                    let _ = handle.emit(payload.0, v);
                }
            }
        }
        let _ = handle.emit(
            "trace://disconnected",
            serde_json::json!({"reason":"daemon-unavailable"}),
        );
        tokio::time::sleep(std::time::Duration::from_secs(2)).await;
    }
}
