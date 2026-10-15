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
pub async fn live_set_recording(
    state: State<'_, AppState>,
    session_id: String,
    enabled: bool,
    generation: i64,
) -> CmdResult {
    call!(
        state,
        "live.set_recording",
        json!({"session_id":session_id,"enabled":enabled,"generation":generation})
    )
}

#[tauri::command]
pub async fn daemon_status(state: State<'_, AppState>) -> CmdResult {
    call!(state, cmd::DAEMON_STATUS)
}

#[tauri::command]
pub async fn desktop_close(window: tauri::WebviewWindow) -> CmdResult {
    window.close().map_err(|_| "无法关闭窗口".to_string())?;
    Ok(json!({"closed":true}))
}

#[tauri::command]
pub async fn desktop_exit(state: State<'_, AppState>, app: tauri::AppHandle) -> CmdResult {
    crate::state::shutdown_daemon(state.inner()).await?;
    app.exit(0);
    Ok(json!({"stopped":true}))
}

#[tauri::command]
pub async fn daemon_pause(state: State<'_, AppState>, paused: bool) -> CmdResult {
    call!(state, cmd::DAEMON_PAUSE, json!({ "paused": paused }))
}

// ---------- 创作者 ----------

#[tauri::command]
pub async fn creator_list(state: State<'_, AppState>) -> CmdResult {
    call!(state, cmd::CREATOR_LIST, json!({"include_archived":true}))
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
    aliases: Option<Vec<String>>,
    tags: Option<Vec<String>>,
    status: Option<String>,
) -> CmdResult {
    call!(
        state,
        cmd::CREATOR_UPDATE,
        json!({ "creator_id": creator_id, "display_name": display_name, "notes": notes, "aliases":aliases,"tags":tags,"status":status })
    )
}

// ---------- 平台账号与订阅 ----------

#[tauri::command]
pub async fn account_resolve(
    state: State<'_, AppState>,
    namespace: String,
    input: String,
    credential_id: Option<String>,
) -> CmdResult {
    call!(
        state,
        "account.resolve",
        json!({"namespace":namespace,"input":input,"credential_id":credential_id})
    )
}

#[tauri::command]
pub async fn account_history(
    state: State<'_, AppState>,
    account_id: String,
    before: Option<i64>,
) -> CmdResult {
    call!(
        state,
        "account.history",
        json!({"account_id":account_id,"before":before})
    )
}

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
    request: trace_contracts::control::SubscriptionCreateArgs,
) -> CmdResult {
    call!(
        state,
        cmd::SUBSCRIPTION_CREATE,
        serde_json::to_value(request).map_err(|e| e.to_string())?
    )
}

#[tauri::command]
pub async fn subscription_update(
    state: State<'_, AppState>,
    request: trace_contracts::control::SubscriptionUpdateArgs,
) -> CmdResult {
    call!(
        state,
        cmd::SUBSCRIPTION_UPDATE,
        serde_json::to_value(request).map_err(|e| e.to_string())?
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
    authentication: Option<trace_contracts::acquisition::AuthenticationState>,
    state: State<'_, AppState>,
    entry_id: Option<String>,
    creator_id: Option<String>,
    account_id: Option<String>,
    kind: Option<String>,
    save_state: Option<String>,
    published_from: Option<String>,
    published_before: Option<String>,
    min_height: Option<u32>,
    codec: Option<String>,
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
            "authentication": authentication,
            "entry_id": entry_id,
            "creator_id": creator_id,
            "account_id": account_id,
            "kind": kind,
            "save_state": save_state,
            "published_from": published_from,
            "published_before": published_before,
            "min_height": min_height,
            "codec": codec,
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
pub async fn archive_entry_page(
    state: State<'_, AppState>,
    entry_id: String,
    section: String,
    revision_id: Option<String>,
    cursor: Option<String>,
) -> CmdResult {
    call!(
        state,
        "archive.entry.page",
        json!({"entry_id":entry_id,"section":section,"revision_id":revision_id,"cursor":cursor})
    )
}

#[tauri::command]
pub async fn archive_revision_read(
    state: State<'_, AppState>,
    entry_id: String,
    revision_id: String,
    field: String,
    offset: u64,
) -> CmdResult {
    call!(
        state,
        "archive.revision.read",
        json!({"entry_id":entry_id,"revision_id":revision_id,"field":field,"offset":offset})
    )
}

#[tauri::command]
pub async fn archive_observation_read(
    state: State<'_, AppState>,
    entry_id: String,
    seq: i64,
    offset: u64,
) -> CmdResult {
    call!(
        state,
        "archive.observation.read",
        json!({"entry_id":entry_id,"seq":seq,"offset":offset})
    )
}
#[tauri::command]
pub async fn archive_entry_recheck(state: State<'_, AppState>, entry_id: String) -> CmdResult {
    call!(state, "archive.entry.recheck", json!({"entry_id":entry_id}))
}
#[tauri::command]
pub async fn job_retry_failed(state: State<'_, AppState>, job_ids: Vec<String>) -> CmdResult {
    call!(state, "job.retry_failed", json!({"job_ids":job_ids}))
}

#[tauri::command]
pub async fn search_query(
    state: State<'_, AppState>,
    query: Option<String>,
    kind: Option<String>,
    limit: Option<i64>,
    request: Option<trace_contracts::control::SearchQueryArgs>,
) -> CmdResult {
    // Preserve the old three-field shell call while new callers use the shared
    // control DTO, including creator/platform/date filters and continuation.
    let args = match request {
        Some(request) => serde_json::to_value(request).map_err(|e| e.to_string())?,
        None => json!({"query":query.unwrap_or_default(),"kind":kind,"limit":limit.unwrap_or(50)}),
    };
    call!(state, cmd::SEARCH_QUERY, args)
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
pub async fn quality_list(
    state: State<'_, AppState>,
    entry_id: String,
    after: Option<i64>,
) -> CmdResult {
    call!(
        state,
        "quality.list",
        json!({"entry_id":entry_id,"after":after})
    )
}
#[tauri::command]
pub async fn quality_configure(
    state: State<'_, AppState>,
    entry_id: String,
    prefer_fps: bool,
    wait_seconds: i64,
) -> CmdResult {
    call!(
        state,
        "quality.configure",
        json!({"entry_id":entry_id,"prefer_fps":prefer_fps,"wait_seconds":wait_seconds})
    )
}
#[tauri::command]
pub async fn quality_pin(
    state: State<'_, AppState>,
    variant_id: String,
    enabled: bool,
) -> CmdResult {
    call!(
        state,
        "quality.pin",
        json!({"variant_id":variant_id,"enabled":enabled})
    )
}
#[tauri::command]
pub async fn quality_recycle(state: State<'_, AppState>) -> CmdResult {
    call!(state, "quality.recycle")
}
#[tauri::command]
pub async fn live_fragments(
    state: State<'_, AppState>,
    session_id: String,
    before: Option<i64>,
) -> CmdResult {
    call!(
        state,
        "live.fragments",
        json!({"session_id":session_id,"before":before})
    )
}
#[tauri::command]
pub async fn live_auxiliary(
    state: State<'_, AppState>,
    session_id: String,
    before: Option<i64>,
) -> CmdResult {
    call!(
        state,
        "live.auxiliary",
        json!({"session_id":session_id,"before":before})
    )
}

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

#[tauri::command]
pub async fn credential_update(
    state: State<'_, AppState>,
    credential_id: String,
    secret: String,
) -> CmdResult {
    call!(
        state,
        "credential.update",
        json!({"credential_id":credential_id,"secret":secret})
    )
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
#[tauri::command]
pub async fn health_list(state: State<'_, AppState>) -> CmdResult {
    call!(state, "health.list")
}
#[tauri::command]
pub async fn health_refs(
    state: State<'_, AppState>,
    issue_key: String,
    after: Option<String>,
) -> CmdResult {
    call!(
        state,
        "health.refs",
        json!({"issue_key":issue_key,"after":after})
    )
}

#[tauri::command]
pub async fn archive_delete_preview(
    state: State<'_, AppState>,
    entry_id: String,
    scope: String,
) -> CmdResult {
    call!(
        state,
        "archive.delete_preview",
        json!({"entry_id":entry_id,"scope":scope})
    )
}
#[tauri::command]
pub async fn archive_delete(
    state: State<'_, AppState>,
    entry_id: String,
    scope: String,
    token: String,
) -> CmdResult {
    call!(
        state,
        "archive.delete",
        json!({"entry_id":entry_id,"scope":scope,"token":token})
    )
}
#[tauri::command]
pub async fn archive_deleted(state: State<'_, AppState>, after: Option<String>) -> CmdResult {
    call!(state, "archive.deleted", json!({"after":after}))
}
#[tauri::command]
pub async fn archive_restore_deleted(
    state: State<'_, AppState>,
    entry_id: String,
    generation: i64,
) -> CmdResult {
    call!(
        state,
        "archive.restore_deleted",
        json!({"entry_id":entry_id,"generation":generation})
    )
}

#[tauri::command]
pub async fn archive_resources(state: State<'_, AppState>, request: Value) -> CmdResult {
    call!(state, "archive.resources", request)
}

#[tauri::command]
pub async fn cache_status(state: State<'_, AppState>) -> CmdResult {
    call!(state, "cache.status")
}
#[tauri::command]
pub async fn cache_configure(
    state: State<'_, AppState>,
    index_budget_bytes: i64,
    derived_budget_bytes: i64,
) -> CmdResult {
    call!(
        state,
        "cache.configure",
        json!({"index_budget_bytes":index_budget_bytes,"derived_budget_bytes":derived_budget_bytes})
    )
}
#[tauri::command]
pub async fn cache_control(state: State<'_, AppState>, scope: String, rebuild: bool) -> CmdResult {
    call!(
        state,
        "cache.control",
        json!({"scope":scope,"rebuild":rebuild})
    )
}

#[tauri::command]
pub async fn archive_acquisitions(
    state: State<'_, AppState>,
    entry_id: String,
    before: Option<i64>,
) -> CmdResult {
    call!(
        state,
        "archive.acquisitions",
        json!({"entry_id":entry_id,"before":before})
    )
}
#[tauri::command]
pub async fn upgrade_list(state: State<'_, AppState>, request: Value) -> CmdResult {
    call!(state, "upgrade.list", request)
}
#[tauri::command]
pub async fn upgrade_resources(state: State<'_, AppState>, request: Value) -> CmdResult {
    call!(state, "upgrade.resources", request)
}
#[tauri::command]
pub async fn upgrade_decide(
    state: State<'_, AppState>,
    items: Vec<trace_contracts::acquisition::UpgradeDecision>,
) -> CmdResult {
    call!(state, "upgrade.decide", json!({"items":items}))
}

#[tauri::command]
pub async fn source_preview(state: State<'_, AppState>, request: Value) -> CmdResult {
    call!(state, "source.preview", request)
}
#[tauri::command]
pub async fn source_confirm(state: State<'_, AppState>, request: Value) -> CmdResult {
    call!(state, "source.confirm", request)
}
#[tauri::command]
pub async fn source_list(state: State<'_, AppState>) -> CmdResult {
    call!(state, "source.list")
}
#[tauri::command]
pub async fn source_cancel(state: State<'_, AppState>, request_id: String) -> CmdResult {
    call!(state, "source.cancel", json!({"request_id":request_id}))
}
