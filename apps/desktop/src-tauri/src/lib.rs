//! Tauri 桌面外壳：窗口、受控 IPC 转发与媒体桥。
//!
//! 外壳不做领域写入；全部业务通过控制协议访问独立 daemon
//! （docs/design/windows-runtime-contracts.md §1/§5）。
#![cfg_attr(test, allow(clippy::too_many_lines))]

mod commands;
mod media;
mod state;

use tauri::Manager;

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    let arguments: Vec<String> = std::env::args().collect();
    if arguments.iter().enumerate().any(|(i, arg)| {
        arg == "--profile" && arguments.get(i + 1).is_none_or(|p| p.starts_with("--"))
    }) {
        eprintln!("TRACE profile argument missing");
        std::process::exit(2);
    }
    if arguments.iter().any(|a| a == "--background-only") {
        run_background_only();
        return;
    }
    let profile = startup_profile(&arguments);
    let media_profile = profile.clone();
    let minimized = arguments.iter().any(|a| a == "--minimized");
    let show_status = arguments.iter().any(|a| a == "--show-status");
    let mut context = tauri::generate_context!();
    if minimized {
        for window in &mut context.config_mut().app.windows {
            window.visible = false;
            window.focus = false;
        }
    }
    let builder = tauri::Builder::default().setup(move |app| setup(app, &profile, minimized));
    media::register_media_protocol(builder, media_profile)
        .on_page_load(move |webview, payload| {
            if show_status && payload.event() == tauri::webview::PageLoadEvent::Finished {
                // Fixed local navigation; never evaluate notification/URI input.
                // The router mounts after this event; navigate on the next task so its
                // initial hash normalization cannot overwrite the notification target.
                let _ = webview.eval(
                    "setTimeout(() => { if (location.hash !== '#/status') location.hash = '#/status'; }, 250)",
                );
            }
        })
        .invoke_handler(invoke_handler())
        .run(context)
        .expect("error while running tauri application");
}

/// Logon launcher: it has no WebView. A failed explicit profile never falls back to
/// another archive; the daemon OS lock arbitrates double launch.
fn run_background_only() {
    let Ok(profile) = trace_core::config::load_profile(&state::profile_path()) else {
        eprintln!("TRACE background profile unavailable");
        std::process::exit(2);
    };
    let rt = tokio::runtime::Runtime::new().expect("startup runtime");
    let app = state::AppState::new(profile);
    if rt.block_on(state::ensure_daemon(&app)).is_err() {
        eprintln!("TRACE background unavailable");
        std::process::exit(1);
    }
}

/// An explicit profile must load or the shell stops; otherwise the shell's own profile.
fn startup_profile(arguments: &[String]) -> trace_core::config::Profile {
    if arguments.iter().any(|arg| arg == "--profile") || std::env::var_os("TRACE_PROFILE").is_some()
    {
        trace_core::config::load_profile(&state::profile_path()).unwrap_or_else(|_| {
            eprintln!("TRACE explicit profile unavailable");
            std::process::exit(2);
        })
    } else {
        state::load_shell_profile()
    }
}

fn setup(
    app: &mut tauri::App,
    profile: &trace_core::config::Profile,
    minimized: bool,
) -> Result<(), Box<dyn std::error::Error>> {
    let app_state = state::AppState::new(profile.clone());
    let pump_state = state::AppState::new(profile.clone());
    let handle = app.handle().clone();
    tauri::async_runtime::spawn(async move {
        state::event_pump(pump_state, handle).await;
    });
    app.manage(app_state);
    if minimized && let Some(window) = app.get_webview_window("main") {
        reveal_minimized(&window)?;
    }
    Ok(())
}

#[cfg(windows)]
fn reveal_minimized(window: &tauri::WebviewWindow) -> tauri::Result<()> {
    use windows::Win32::{
        Foundation::HWND,
        UI::WindowsAndMessaging::{SW_SHOWMINNOACTIVE, ShowWindow},
    };
    // Showing a previously hidden Tauri window restores its normal state. Reveal it
    // minimized in one OS call without activating it, so background verification never
    // covers the user's work.
    let hwnd = HWND(window.hwnd()?.0);
    unsafe {
        let _ = ShowWindow(hwnd, SW_SHOWMINNOACTIVE);
    }
    Ok(())
}

#[cfg(not(windows))]
fn reveal_minimized(window: &tauri::WebviewWindow) -> tauri::Result<()> {
    window.show()?;
    window.minimize()
}

fn invoke_handler() -> impl Fn(tauri::ipc::Invoke) -> bool + Send + Sync + 'static {
    tauri::generate_handler![
        commands::daemon_call,
        commands::desktop_close,
        commands::desktop_window_minimize,
        commands::desktop_window_state,
        commands::desktop_window_toggle_maximize,
        commands::desktop_exit,
        commands::startup_status,
        commands::startup_configure,
    ]
}
