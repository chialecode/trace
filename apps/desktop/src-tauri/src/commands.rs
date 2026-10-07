//! Tauri 命令层：UI 请求 → 控制协议命令的薄转发。
//!
//! 参数形状与 crates/contracts 的 *Args 一致；凭证参数只在内存中
//! 传递给 daemon，不落日志（INV-10）。

use crate::state::{AppState, ensure_daemon};
use serde_json::{Value, json};
use tauri::State;
use trace_contracts::control::commands as cmd;

type CmdResult = Result<Value, String>;

macro_rules! call {
    ($state:expr, $command:expr) => {
        call!($state, $command, json!({}))
    };
    ($state:expr, $command:expr, $args:expr) => {{
        let client = ensure_daemon($state.inner()).await?;
        match client.request($command, $args, None, None).await {
            Ok(v) => Ok(v),
            Err(e) => {
                // 连接类失败失效缓存，下条命令重连。
                $state.inner().invalidate().await;
                Err(e.to_string())
            }
        }
    }};
}

// ---------- daemon ----------

#[tauri::command]
pub async fn daemon_status(state: State<'_, AppState>) -> CmdResult {
    call!(state, cmd::DAEMON_STATUS)
}

#[tauri::command]
pub async fn daemon_pause(state: State<'_, AppState>, paused: bool) -> CmdResult {
    call!(state, cmd::DAEMON_PAUSE, json!({ "paused": paused }))
}

// ---------- 创作者 ----------

#[tauri::command]
pub async fn creator_list(state: State<'_, AppState>) -> CmdResult {
    call!(state, cmd::CREATOR_LIST)
}

#[tauri::command]
pub async fn creator_create(
    state: State<'_, AppState>,
    display_name: String,
    notes: Option<String>,
) -> CmdResult {
    call!(
        state,
        cmd::CREATOR_CREATE,
        json!({ "display_name": display_name, "aliases": [], "tags": [], "notes": notes.unwrap_or_default() })
    )
}

#[tauri::command]
pub async fn creator_update(
    state: State<'_, AppState>,
    creator_id: String,
    display_name: Option<String>,
    notes: Option<String>,
) -> CmdResult {
    call!(
        state,
        cmd::CREATOR_UPDATE,
        json!({ "creator_id": creator_id, "display_name": display_name, "notes": notes })
    )
}

// ---------- 平台账号与订阅 ----------

#[tauri::command]
pub async fn account_bind(
    state: State<'_, AppState>,
    creator_id: String,
    namespace: String,
    native_account_key: String,
) -> CmdResult {
    call!(
        state,
        cmd::ACCOUNT_BIND,
        json!({
            "creator_id": creator_id,
            "namespace": namespace,
            "native_account_key": native_account_key,
        })
    )
}

#[tauri::command]
pub async fn account_list(state: State<'_, AppState>, creator_id: Option<String>) -> CmdResult {
    call!(
        state,
        cmd::ACCOUNT_LIST,
        json!({ "creator_id": creator_id })
    )
}

#[tauri::command]
pub async fn subscription_create(
    state: State<'_, AppState>,
    account_id: String,
    range_kind: String,
    range_days: Option<i64>,
    check_interval_seconds: Option<i64>,
) -> CmdResult {
    call!(
        state,
        cmd::SUBSCRIPTION_CREATE,
        json!({
            "account_id": account_id,
            "content_type": "video",
            "range": if range_kind == "recent_days" {
                json!({"kind": "recent_days", "days": range_days.unwrap_or(30)})
            } else {
                json!({"kind": "from_now"})
            },
            "check_interval_seconds": check_interval_seconds,
        })
    )
}

#[tauri::command]
pub async fn subscription_list(
    state: State<'_, AppState>,
    account_id: Option<String>,
) -> CmdResult {
    call!(
        state,
        cmd::SUBSCRIPTION_LIST,
        json!({ "account_id": account_id })
    )
}

#[tauri::command]
pub async fn subscription_check_now(
    state: State<'_, AppState>,
    subscription_id: String,
) -> CmdResult {
    call!(
        state,
        cmd::SUBSCRIPTION_CHECK_NOW,
        json!({ "subscription_id": subscription_id })
    )
}

// ---------- 时间线与搜索 ----------

// Flat arguments preserve the existing Tauri invoke contract.
#[allow(clippy::too_many_arguments)]
#[tauri::command]
pub async fn archive_timeline(
    state: State<'_, AppState>,
    entry_id: Option<String>,
    creator_id: Option<String>,
    account_id: Option<String>,
    kind: Option<String>,
    save_state: Option<String>,
    query: Option<String>,
    namespace: Option<String>,
    oldest: Option<bool>,
    cursor: Option<String>,
    limit: Option<i64>,
) -> CmdResult {
    call!(
        state,
        cmd::ARCHIVE_TIMELINE,
        json!({
            "entry_id": entry_id,
            "creator_id": creator_id,
            "account_id": account_id,
            "kind": kind,
            "save_state": save_state,
            "query": query,
            "namespace": namespace,
            "oldest": oldest.unwrap_or(false),
            "cursor": cursor,
            "limit": limit.unwrap_or(50),
        })
    )
}

#[tauri::command]
pub async fn archive_entry_get(state: State<'_, AppState>, entry_id: String) -> CmdResult {
    call!(
        state,
        cmd::ARCHIVE_ENTRY_GET,
        json!({ "entry_id": entry_id })
    )
}

#[tauri::command]
pub async fn search_query(
    state: State<'_, AppState>,
    query: String,
    kind: Option<String>,
    limit: Option<i64>,
) -> CmdResult {
    call!(
        state,
        cmd::SEARCH_QUERY,
        json!({ "query": query, "kind": kind, "limit": limit.unwrap_or(50) })
    )
}

// ---------- 任务 ----------

#[tauri::command]
pub async fn job_list(
    state: State<'_, AppState>,
    state_filter: Option<String>,
    job_type: Option<String>,
    limit: Option<i64>,
) -> CmdResult {
    call!(
        state,
        cmd::JOB_LIST,
        json!({ "state": state_filter, "job_type": job_type, "limit": limit.unwrap_or(100) })
    )
}

#[tauri::command]
pub async fn job_get(state: State<'_, AppState>, job_id: String) -> CmdResult {
    call!(state, cmd::JOB_GET, json!({ "job_id": job_id }))
}

#[tauri::command]
pub async fn job_retry(state: State<'_, AppState>, job_id: String) -> CmdResult {
    call!(state, cmd::JOB_RETRY, json!({ "job_id": job_id }))
}

#[tauri::command]
pub async fn job_cancel(state: State<'_, AppState>, job_id: String) -> CmdResult {
    call!(state, cmd::JOB_CANCEL, json!({ "job_id": job_id }))
}

// ---------- 登录与凭证 ----------

#[tauri::command]
pub async fn credential_list(state: State<'_, AppState>, platform: Option<String>) -> CmdResult {
    call!(state, cmd::CREDENTIAL_LIST, json!({ "platform": platform }))
}

#[tauri::command]
pub async fn credential_import(
    state: State<'_, AppState>,
    platform: String,
    subject: Option<String>,
    secret: String,
) -> CmdResult {
    // secret 经内存传给 daemon 入 vault；不写日志、不持久化原文（INV-10）。
    call!(
        state,
        cmd::CREDENTIAL_IMPORT,
        json!({ "platform": platform, "subject": subject, "secret": secret })
    )
}

#[tauri::command]
pub async fn credential_validate(state: State<'_, AppState>, credential_id: String) -> CmdResult {
    call!(
        state,
        cmd::CREDENTIAL_VALIDATE,
        json!({ "credential_id": credential_id })
    )
}

#[tauri::command]
pub async fn credential_revoke(state: State<'_, AppState>, credential_id: String) -> CmdResult {
    call!(
        state,
        cmd::CREDENTIAL_REVOKE,
        json!({ "credential_id": credential_id })
    )
}

// ---------- 模块 ----------

#[tauri::command]
pub async fn plugin_list(state: State<'_, AppState>) -> CmdResult {
    call!(state, cmd::PLUGIN_LIST)
}

#[tauri::command]
pub async fn plugin_set_state(
    state: State<'_, AppState>,
    plugin_id: String,
    enabled: bool,
) -> CmdResult {
    call!(
        state,
        cmd::PLUGIN_SET_STATE,
        json!({ "plugin_id": plugin_id, "state": if enabled { "enabled" } else { "disabled" } })
    )
}

// ---------- 存储 ----------

#[tauri::command]
pub async fn storage_usage(state: State<'_, AppState>) -> CmdResult {
    call!(state, cmd::STORAGE_USAGE)
}

#[tauri::command]
pub async fn export_start(
    state: State<'_, AppState>,
    target_dir: String,
    include_media: bool,
    creator_id: Option<String>,
) -> CmdResult {
    call!(
        state,
        cmd::EXPORT_START,
        json!({ "target_dir": target_dir, "include_media": include_media, "creator_id": creator_id })
    )
}

#[tauri::command]
pub async fn backup_start(state: State<'_, AppState>) -> CmdResult {
    call!(state, cmd::BACKUP_START)
}

#[tauri::command]
pub async fn backup_list(state: State<'_, AppState>) -> CmdResult {
    call!(state, cmd::BACKUP_LIST)
}

#[tauri::command]
pub async fn restore_start(
    state: State<'_, AppState>,
    backup_root: String,
    target_root: String,
) -> CmdResult {
    call!(
        state,
        cmd::RESTORE_START,
        json!({ "backup_root": backup_root, "target_root": target_root })
    )
}

// ---------- 外部打开 ----------

#[tauri::command]
pub async fn open_external(state: State<'_, AppState>, blob_id: String) -> CmdResult {
    call!(state, cmd::MEDIA_OPEN_EXTERNAL, json!({"blob_id":blob_id}))
}

#[tauri::command]
pub async fn subscription_set_enabled(
    state: State<'_, AppState>,
    subscription_id: String,
    enabled: bool,
) -> CmdResult {
    call!(
        state,
        cmd::SUBSCRIPTION_UPDATE,
        json!({"subscription_id":subscription_id,"enabled":enabled})
    )
}

#[tauri::command]
pub async fn open_source(state: State<'_, AppState>, entry_id: String) -> CmdResult {
    call!(
        state,
        cmd::ARCHIVE_OPEN_SOURCE,
        json!({"entry_id":entry_id})
    )
}
