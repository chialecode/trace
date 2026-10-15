//! Tauri command layer.
//!
//! The UI reaches the daemon through one forwarding command, `daemon_call`, whose command
//! name must be in the shared whitelist (`trace_contracts::control::commands::UI_COMMANDS`).
//! Arguments have the control protocol's shape (crates/contracts `*Args`); credentials only
//! pass through memory to the daemon and are never logged (INV-10). The remaining commands
//! act on the desktop process itself (window, startup entry).

use crate::state::{AppState, ensure_daemon};
use serde_json::{Value, json};
use tauri::State;
use trace_contracts::control::commands as cmd;

type CmdResult = Result<Value, String>;

fn main_window_only(window: &tauri::WebviewWindow) -> Result<(), String> {
    if window.label() == "main" {
        Ok(())
    } else {
        Err("window action is only available to the local main window".into())
    }
}

/// Forwards a whitelisted control command to the daemon.
#[tauri::command]
pub async fn daemon_call(
    state: State<'_, AppState>,
    command: String,
    args: Option<Value>,
) -> CmdResult {
    if !cmd::is_ui_command(&command) {
        return Err(format!("command not available to the UI: {command}"));
    }
    let client = ensure_daemon(state.inner()).await?;
    match client
        .request(&command, args.unwrap_or_else(|| json!({})), None, None)
        .await
    {
        Ok(v) => Ok(v),
        Err(e) => {
            // 连接类失败失效缓存，下条命令重连。
            state.inner().invalidate().await;
            Err(e.to_string())
        }
    }
}

#[tauri::command]
pub async fn desktop_close(window: tauri::WebviewWindow) -> CmdResult {
    main_window_only(&window)?;
    window.close().map_err(|_| "无法关闭窗口".to_string())?;
    Ok(json!({"closed":true}))
}

#[tauri::command]
pub async fn desktop_window_minimize(window: tauri::WebviewWindow) -> CmdResult {
    main_window_only(&window)?;
    window
        .minimize()
        .map_err(|_| "无法最小化窗口".to_string())?;
    Ok(json!({"minimized":true}))
}

#[tauri::command]
pub async fn desktop_window_state(window: tauri::WebviewWindow) -> CmdResult {
    main_window_only(&window)?;
    let maximized = window
        .is_maximized()
        .map_err(|_| "无法读取窗口状态".to_string())?;
    Ok(json!({"maximized":maximized}))
}

#[tauri::command]
pub async fn desktop_window_toggle_maximize(window: tauri::WebviewWindow) -> CmdResult {
    main_window_only(&window)?;
    if window
        .is_maximized()
        .map_err(|_| "无法读取窗口状态".to_string())?
    {
        window
            .unmaximize()
            .map_err(|_| "无法还原窗口".to_string())?;
    } else {
        window
            .maximize()
            .map_err(|_| "无法最大化窗口".to_string())?;
    }
    desktop_window_state(window).await
}

#[tauri::command]
pub async fn desktop_exit(state: State<'_, AppState>, app: tauri::AppHandle) -> CmdResult {
    crate::state::shutdown_daemon(state.inner()).await?;
    app.exit(0);
    Ok(json!({"stopped":true}))
}

fn startup_identity(state: &AppState) -> Result<(String, String), String> {
    let exe = std::env::current_exe()
        .map_err(|_| "无法定位程序")?
        .canonicalize()
        .map_err(|_| "程序路径不可用")?;
    let profile = super::state::profile_path()
        .canonicalize()
        .map_err(|_| "请先保存有效的 Profile")?;
    let name = format!(
        "TRACE-{}",
        trace_core::config::short_hash(&state.profile.archive_id)
    );
    let command = trace_core::windows_integration::startup_command(&exe, &profile)
        .map_err(|e| e.to_string())?;
    Ok((name, command))
}
#[tauri::command]
pub async fn startup_status(state: State<'_, AppState>) -> CmdResult {
    let (name, command) = startup_identity(&state)?;
    let actual =
        trace_core::windows_integration::startup_value(&name).map_err(|e| e.to_string())?;
    Ok(
        json!({"enabled":actual.is_some(),"current_location":actual.as_deref()==Some(command.as_str())}),
    )
}
#[tauri::command]
pub async fn startup_configure(state: State<'_, AppState>, enabled: bool) -> CmdResult {
    let (name, command) = startup_identity(&state)?;
    trace_core::windows_integration::startup(&name, if enabled { Some(&command) } else { None })
        .map_err(|e| e.to_string())?;
    Ok(json!({"enabled":enabled,"current_location":enabled}))
}
