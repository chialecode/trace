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
    let profile = state::load_shell_profile();
    let media_profile = profile.clone();
    let builder = tauri::Builder::default().setup(move |app| {
        let app_state = state::AppState::new(profile.clone());
        let pump_state = state::AppState::new(profile.clone());
        let handle = app.handle().clone();
        tauri::async_runtime::spawn(async move {
            state::event_pump(pump_state, handle).await;
        });
        app.manage(app_state);
        Ok(())
    });
    let builder = media::register_media_protocol(builder, media_profile);
    builder
        .invoke_handler(tauri::generate_handler![
            commands::daemon_status,
            commands::daemon_pause,
            commands::creator_list,
            commands::creator_create,
            commands::creator_update,
            commands::account_bind,
            commands::account_list,
            commands::subscription_create,
            commands::subscription_list,
            commands::subscription_set_enabled,
            commands::subscription_check_now,
            commands::archive_timeline,
            commands::archive_entry_get,
            commands::search_query,
            commands::job_list,
            commands::job_get,
            commands::job_retry,
            commands::job_cancel,
            commands::credential_import,
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
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}
