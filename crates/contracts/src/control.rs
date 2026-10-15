//! 控制协议 DTO（GUI/CLI ↔ daemon）。
//!
//! 命令名使用 `group.action` 字符串；参数/结果为带 schema 注记的类型化结构。
//! 所有 ID 与大整数以字符串跨语言传递。写命令带 `idempotency_key` 与
//! `if_revision`；收据同事务保存（INV-09）。

use serde::{Deserialize, Serialize};
use serde_json::Value;

/// 客户端 → daemon 请求信封。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Request {
    pub protocol_version: String,
    pub request_id: String,
    pub command: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub idempotency_key: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub if_revision: Option<i64>,
    #[serde(default)]
    pub args: Value,
}

/// daemon → 客户端响应信封。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Response {
    pub protocol_version: String,
    pub request_id: String,
    pub ok: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub result: Option<Value>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub error: Option<ApiError>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ApiError {
    pub code: ErrorCode,
    pub retryable: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub user_action: Option<String>,
    #[serde(default)]
    pub related_ids: Vec<String>,
    /// 只携带安全字段；不得回显原始错误体或凭证。
    #[serde(default)]
    pub details: Value,
}

/// 稳定错误码（runtime-contracts §2：不能全部落到“失败请重试”）。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ErrorCode {
    InvalidInput,
    NotFound,
    VersionConflict,
    NotReady,
    ResyncRequired,
    DaemonPaused,
    AuthRequired,
    RateLimited,
    StorageFull,
    PluginUnavailable,
    Unsupported,
    Cancelled,
    Internal,
}

/// 服务端 → 客户端帧（握手/响应/事件三态，tag 供 TS 侧判别）。
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "frame", rename_all = "snake_case")]
pub enum ServerFrame {
    Hello(HelloResponse),
    Response(Response),
    Event(trace_contracts_events::EventFrame),
}

// 避免循环依赖命名冲突：EventFrame 定义于 events 模块。
use crate::events as trace_contracts_events;

/// 握手请求（客户端连接后先发送）。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct HelloRequest {
    pub client: String,
    pub protocol_version: String,
    /// 客户端应持有的档案 ID；服务端校验一致才复用连接（C22）。
    #[serde(default)]
    pub archive_id: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct HelloResponse {
    pub protocol_version: String,
    pub archive_id: String,
    pub schema_version: i64,
    pub plugin_protocol: String,
    pub node_id: String,
    pub instance_id: String,
    pub capabilities: Vec<String>,
    pub paused: bool,
    pub recovery: RecoverySnapshot,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct RecoverySnapshot {
    pub in_progress: bool,
    pub pending_materializations: i64,
    pub stale_leases_requeued: i64,
    pub quarantined_objects: i64,
}

// ---------- 命令参数 ----------

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct DaemonStatusArgs {}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DaemonPauseArgs {
    pub paused: bool,
    pub reason: Option<String>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct DaemonShutdownArgs {}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CreatorCreateArgs {
    pub display_name: String,
    #[serde(default)]
    pub aliases: Vec<String>,
    #[serde(default)]
    pub tags: Vec<String>,
    #[serde(default)]
    pub notes: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CreatorUpdateArgs {
    pub creator_id: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub display_name: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub aliases: Option<Vec<String>>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub tags: Option<Vec<String>>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub notes: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub status: Option<String>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct CreatorListArgs {
    #[serde(default)]
    pub include_archived: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CreatorGetArgs {
    pub creator_id: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AccountBindArgs {
    pub creator_id: String,
    pub namespace: String,
    pub native_account_key: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub display_name: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub profile_url: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AccountUnbindArgs {
    pub account_id: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AccountUpdateArgs {
    pub account_id: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub display_name: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub profile_url: Option<String>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct AccountListArgs {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub creator_id: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SubscriptionCreateArgs {
    #[serde(default)]
    pub quality: QualityRequirement,
    pub account_id: String,
    #[serde(default = "default_content_type")]
    pub content_type: String,
    /// {"kind":"from_now"} 或 {"kind":"recent_days","days":30}
    #[serde(default)]
    pub range: Value,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub credential_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub check_interval_seconds: Option<i64>,
    #[serde(default)]
    pub capture_chat: bool,
    #[serde(default)]
    pub capture_subtitles: bool,
    /// Download discovered videos on their own. Absent means off for a new subscription.
    #[serde(default)]
    pub auto_download: bool,
}

fn default_content_type() -> String {
    "video".into()
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SubscriptionUpdateArgs {
    /// None leaves the binding unchanged; empty string explicitly selects guest access.
    #[serde(default)]
    pub credential_id: Option<String>,
    #[serde(default)]
    pub quality: Option<QualityRequirement>,
    pub subscription_id: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub range: Option<Value>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub enabled: Option<bool>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub check_interval_seconds: Option<i64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub capture_chat: Option<bool>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub capture_subtitles: Option<bool>,
    /// Affects videos discovered from now on; saved and undownloaded ones stay as they are.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub auto_download: Option<bool>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize, ts_rs::TS)]
#[ts(export, optional_fields = nullable)]
#[serde(default, deny_unknown_fields)]
pub struct QualityRequirement {
    pub min_height: Option<u32>,
    pub min_fps: Option<u32>,
    pub strict: bool,
    pub prefer_fps: Option<bool>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SubscriptionCheckNowArgs {
    pub subscription_id: String,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct SubscriptionListArgs {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub creator_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub account_id: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ArchiveTimelineArgs {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub authentication: Option<crate::acquisition::AuthenticationState>,
    /// Optional membership query using the same filters as the timeline.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub entry_id: Option<String>,
    #[serde(default)]
    pub query: Option<String>,
    #[serde(default)]
    pub namespace: Option<String>,
    #[serde(default)]
    pub oldest: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub creator_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub account_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub kind: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub save_state: Option<String>,
    /// Inclusive start and exclusive end in RFC3339; unknown source time is excluded.
    #[serde(default)]
    pub published_from: Option<String>,
    #[serde(default)]
    pub published_before: Option<String>,
    #[serde(default)]
    pub min_height: Option<u32>,
    #[serde(default)]
    pub codec: Option<String>,
    /// 键集分页游标（发布时间+entry_id 编码）。
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub cursor: Option<String>,
    /// `published` (default), `plays`, `likes`, `comments`, `favorites` or `duration`. Entries
    /// without the value (unknown publication time, never counted) come last in either order.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub sort: Option<String>,
    /// Only entries that carry this tag.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub tag: Option<String>,
    /// Only entries that hold this kind of content: `text`, `image`, `live_photo`, `video`,
    /// `audio`, `article` or `live`.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub content_kind: Option<String>,
    #[serde(default = "default_limit")]
    pub limit: i64,
}

fn default_limit() -> i64 {
    50
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ArchiveEntryGetArgs {
    pub entry_id: String,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct JobListArgs {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub state: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub job_type: Option<String>,
    #[serde(default = "default_limit")]
    pub limit: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct JobGetArgs {
    pub job_id: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct JobRetryArgs {
    pub job_id: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct JobCancelArgs {
    pub job_id: String,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct CredentialListArgs {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub platform: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CredentialImportArgs {
    pub platform: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub subject: Option<String>,
    /// 敏感内容：仅经 IPC 内存传递到 vault，不落日志/数据库/导出（INV-10）。
    pub secret: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CredentialRefArgs {
    pub credential_id: String,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct PluginListArgs {}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PluginSetStateArgs {
    pub plugin_id: String,
    /// "enabled" | "disabled"
    pub state: String,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct StorageUsageArgs {}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ExportStartArgs {
    pub target_dir: String,
    pub include_media: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub creator_id: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BackupStartArgs {
    pub target_root: String,
    pub include_media: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BackupListArgs {}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RestoreStartArgs {
    /// 备份目录（含备份清单）。
    pub backup_root: String,
    /// 必须为空目录的隔离恢复目标。
    pub target_root: String,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct BackupOpListArgs {}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MediaLeaseArgs {
    pub blob_id: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MediaReadArgs {
    pub lease_id: String,
    pub offset: i64,
    pub length: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SearchQueryArgs {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub authentication: Option<crate::acquisition::AuthenticationState>,
    pub query: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub creator_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub namespace: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub kind: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub from: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub to: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub cursor: Option<String>,
    #[serde(default = "default_limit")]
    pub limit: i64,
}

// ---------- 结果 DTO ----------

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct CreatorDto {
    pub creator_id: String,
    pub display_name: String,
    pub aliases: Vec<String>,
    pub tags: Vec<String>,
    pub status: String,
    pub notes: String,
    pub created_at: String,
    pub updated_at: String,
    #[ts(type = "number")]
    pub account_count: i64,
    #[ts(type = "number")]
    pub entry_count: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct AccountDto {
    pub account_id: String,
    pub namespace: String,
    pub native_account_key: String,
    pub display_name: Option<String>,
    pub profile_url: Option<String>,
    pub creator_id: Option<String>,
    #[ts(type = "number")]
    pub subscription_count: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct SubscriptionDto {
    pub subscription_id: String,
    pub account_id: String,
    pub content_type: String,
    #[ts(type = "unknown")]
    pub range: Value,
    pub state: String,
    #[ts(type = "number")]
    pub config_version: i64,
    pub credential_id: Option<String>,
    pub updated_at: String,
    pub capture_chat: bool,
    pub capture_subtitles: bool,
    #[ts(type = "number")]
    pub check_interval_seconds: i64,
    pub next_check_at: Option<String>,
    /// Per scan type: `{scan_type, coverage}` rows.
    #[ts(type = "unknown")]
    pub coverage: Value,
    pub quality: QualityRequirement,
    /// Whether videos found by later checks are downloaded on their own.
    #[serde(default)]
    pub auto_download: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct TimelineItemDto {
    pub entry_id: String,
    pub current_revision_id: Option<String>,
    pub title: Option<String>,
    pub namespace: String,
    pub kind: String,
    pub published_at: Option<String>,
    pub published_precision: Option<String>,
    pub discovered_at: String,
    pub access_state: String,
    pub save_progress: String,
    pub quality_state: String,
    #[ts(type = "unknown")]
    pub media_summary: Value,
    #[serde(default)]
    pub account_id: Option<String>,
    #[serde(default)]
    pub account_name: Option<String>,
    #[serde(default)]
    pub native_account_key: Option<String>,
    #[serde(default)]
    pub profile_url: Option<String>,
    #[serde(default)]
    pub creator_id: Option<String>,
    #[serde(default)]
    pub creator_name: Option<String>,
    #[serde(default)]
    pub source_key: Option<String>,
    #[serde(default)]
    pub source_url: Option<String>,
    #[serde(default)]
    pub cover_blob_id: Option<String>,
    #[serde(default)]
    pub authentication: Option<crate::acquisition::AuthenticationSummary>,
    /// Unified fields of the entry; absent for an entry observed before they were kept.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[ts(optional)]
    pub meta: Option<crate::entry_meta::EntryMetaDto>,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct TimelineCounts {
    #[ts(type = "number")]
    pub global: i64,
    #[ts(type = "number")]
    pub filtered: i64,
    pub scope: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct TimelinePageDto {
    pub items: Vec<TimelineItemDto>,
    pub next_cursor: Option<String>,
    #[serde(default)]
    pub counts: Option<TimelineCounts>,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct PageDto<T> {
    pub items: Vec<T>,
    pub next_cursor: Option<String>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct JobDto {
    pub job_id: String,
    pub job_key: String,
    pub job_type: String,
    pub subject_id: String,
    pub state: String,
    #[ts(type = "number")]
    pub attempts: i64,
    #[ts(type = "number")]
    pub target_generation: i64,
    pub priority: i32,
    pub next_run_at: Option<String>,
    #[ts(type = "{ class?: string; message?: string } | null")]
    pub last_error: Option<Value>,
    pub created_at: String,
    pub updated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct CredentialDto {
    pub credential_id: String,
    pub platform: String,
    pub subject: Option<String>,
    pub status: String,
    #[ts(type = "unknown")]
    pub capability: Value,
    pub validated_at: Option<String>,
    pub created_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct PluginDto {
    pub plugin_id: String,
    pub platform: String,
    pub version: String,
    pub state: String,
    pub protocol: String,
    pub capabilities: Vec<String>,
    #[serde(default)]
    pub active_tasks: usize,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct StorageUsageDto {
    #[ts(type = "number")]
    pub db_bytes: i64,
    #[ts(type = "number")]
    pub objects_bytes: i64,
    #[ts(type = "number")]
    pub staging_bytes: i64,
    #[ts(type = "number")]
    pub object_count: i64,
    #[ts(type = "number | null")]
    pub free_bytes: Option<i64>,
}

/// 任务/长操作受理响应。
#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct AcceptedDto {
    pub job_id: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct MediaLeaseDto {
    pub lease_id: String,
    pub blob_id: String,
    #[ts(type = "number")]
    pub size: i64,
    pub mime: Option<String>,
    pub expires_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct MediaChunkDto {
    pub lease_id: String,
    #[ts(type = "number")]
    pub offset: i64,
    pub eof: bool,
    /// base64 编码的字节（有界分块；≤ 512 KiB 原始字节）。
    pub data_b64: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct SearchResultItemDto {
    pub doc_kind: String,
    pub entry_id: Option<String>,
    pub revision_id: Option<String>,
    pub creator_id: Option<String>,
    pub namespace: Option<String>,
    pub title: Option<String>,
    pub snippet: String,
    pub published_at: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct SearchStatusDto {
    /// 索引已消费的 Outbox 序号。
    #[ts(type = "number")]
    pub indexed_seq: i64,
    #[ts(type = "number")]
    pub total_seq: i64,
    pub lagging: bool,
    pub rebuilding: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct SearchResultsDto {
    pub items: Vec<SearchResultItemDto>,
    pub next_cursor: Option<String>,
    /// 实际使用的检索路径：fts / gram / literal。
    pub mode: String,
    pub status: SearchStatusDto,
}

pub mod commands;
