//! Tauri 桌面外壳：窗口、受控 IPC 转发与媒体桥。
//!
//! 外壳不做领域写入；全部业务通过控制协议访问独立 daemon
//! （docs/design/m1a-runtime-contracts.md §1/§5）。

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
    if std::env::args().any(|a| a == "--background-only") {
        // Logon launcher has no WebView. A failed explicit profile never falls
        // back to another archive; the daemon OS lock arbitrates double launch.
        let profile = match trace_core::config::load_profile(&state::profile_path()) {
            Ok(p) => p,
            Err(_) => {
                eprintln!("TRACE background profile unavailable");
                std::process::exit(2);
            }
        };
        let rt = tokio::runtime::Runtime::new().expect("startup runtime");
        let app = state::AppState::new(profile);
        if rt.block_on(state::ensure_daemon(&app)).is_err() {
            eprintln!("TRACE background unavailable");
            std::process::exit(1);
        }
        return;
    }
    let profile = if arguments.iter().any(|arg| arg == "--profile")
        || std::env::var_os("TRACE_PROFILE").is_some()
    {
        trace_core::config::load_profile(&state::profile_path()).unwrap_or_else(|_| {
            eprintln!("TRACE explicit profile unavailable");
            std::process::exit(2);
        })
    } else {
        state::load_shell_profile()
    };
    let media_profile = profile.clone();
    let minimized = std::env::args().any(|a| a == "--minimized");
    let show_status = arguments.iter().any(|a| a == "--show-status");
    let mut context = tauri::generate_context!();
    if minimized {
        for window in &mut context.config_mut().app.windows {
            window.visible = false;
            window.focus = false;
        }
    }
    let builder = tauri::Builder::default().setup(move |app| {
        let app_state = state::AppState::new(profile.clone());
        let pump_state = state::AppState::new(profile.clone());
        let handle = app.handle().clone();
        tauri::async_runtime::spawn(async move {
            state::event_pump(pump_state, handle).await;
        });
        app.manage(app_state);
        if minimized && let Some(window) = app.get_webview_window("main") {
            #[cfg(windows)]
            {
                use windows::Win32::{
                    Foundation::HWND,
                    UI::WindowsAndMessaging::{SW_SHOWMINNOACTIVE, ShowWindow},
                };
                // Showing a previously hidden Tauri window restores its normal
                // state. Reveal it minimized in one OS call without activating
                // it, so background verification never covers the user's work.
                let hwnd = HWND(window.hwnd()?.0);
                unsafe {
                    let _ = ShowWindow(hwnd, SW_SHOWMINNOACTIVE);
                }
            }
            #[cfg(not(windows))]
            {
                window.show()?;
                window.minimize()?;
            }
        }
        Ok(())
    });
    let builder = media::register_media_protocol(builder, media_profile);
    builder
        .on_page_load(move |webview, payload| {
            if show_status && payload.event() == tauri::webview::PageLoadEvent::Finished {
                // Fixed local navigation; never evaluate notification/URI input.
                let _ = webview.eval("location.hash = '#/status'");
            }
        })
        .invoke_handler(tauri::generate_handler![
            commands::daemon_status,
            commands::desktop_close,
            commands::desktop_exit,
            commands::startup_status,
            commands::startup_configure,
            commands::health_list,
            commands::health_refs,
            commands::daemon_pause,
            commands::creator_list,
            commands::creator_create,
            commands::creator_update,
            commands::account_bind,
            commands::account_resolve,
            commands::account_history,
            commands::account_list,
            commands::subscription_create,
            commands::subscription_update,
            commands::subscription_list,
            commands::subscription_set_enabled,
            commands::subscription_check_now,
            commands::live_set_recording,
            commands::live_fragments,
            commands::live_auxiliary,
            commands::quality_list,
            commands::quality_configure,
            commands::quality_pin,
            commands::quality_recycle,
            commands::archive_timeline,
            commands::archive_acquisitions,
            commands::upgrade_list,
            commands::source_preview,
            commands::source_confirm,
            commands::source_list,
            commands::source_cancel,
            commands::upgrade_resources,
            commands::upgrade_decide,
            commands::archive_entry_get,
            commands::archive_resources,
            commands::cache_status,
            commands::cache_configure,
            commands::cache_control,
            commands::archive_delete_preview,
            commands::archive_delete,
            commands::archive_deleted,
            commands::archive_restore_deleted,
            commands::archive_entry_page,
            commands::archive_revision_read,
            commands::archive_observation_read,
            commands::archive_entry_recheck,
            commands::job_retry_failed,
            commands::search_query,
            commands::job_list,
            commands::job_get,
            commands::job_retry,
            commands::job_cancel,
            commands::credential_import,
            commands::credential_update,
            commands::credential_list,
            commands::credential_validate,
            commands::credential_revoke,
            commands::plugin_list,
            commands::plugin_set_state,
            commands::storage_usage,
            commands::export_start,
            commands::backup_start,
            commands::backup_list,
            commands::restore_start,
            commands::open_external,
            commands::open_source,
        ])
        .run(context)
        .expect("error while running tauri application");
}
