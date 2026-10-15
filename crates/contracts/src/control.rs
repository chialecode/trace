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
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
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

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CreatorDto {
    pub creator_id: String,
    pub display_name: String,
    pub aliases: Vec<String>,
    pub tags: Vec<String>,
    pub status: String,
    pub notes: String,
    pub created_at: String,
    pub updated_at: String,
    pub account_count: i64,
    pub entry_count: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AccountDto {
    pub account_id: String,
    pub namespace: String,
    pub native_account_key: String,
    pub display_name: Option<String>,
    pub profile_url: Option<String>,
    pub creator_id: Option<String>,
    pub subscription_count: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SubscriptionDto {
    pub subscription_id: String,
    pub account_id: String,
    pub content_type: String,
    pub range: Value,
    pub state: String,
    pub config_version: i64,
    pub credential_id: Option<String>,
    pub coverage: Value,
    pub last_check_at: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
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
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TimelineCounts {
    pub global: i64,
    pub filtered: i64,
    pub scope: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TimelinePageDto {
    pub items: Vec<TimelineItemDto>,
    pub next_cursor: Option<String>,
    #[serde(default)]
    pub counts: Option<TimelineCounts>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PageDto<T> {
    pub items: Vec<T>,
    pub next_cursor: Option<String>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct JobDto {
    pub job_id: String,
    pub job_key: String,
    pub job_type: String,
    pub subject_id: String,
    pub state: String,
    pub attempts: i64,
    pub target_generation: i64,
    pub priority: i32,
    pub next_run_at: Option<String>,
    pub last_error: Option<Value>,
    pub created_at: String,
    pub updated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CredentialDto {
    pub credential_id: String,
    pub platform: String,
    pub subject: Option<String>,
    pub status: String,
    pub capability: Value,
    pub validated_at: Option<String>,
    pub created_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
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

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct StorageUsageDto {
    pub db_bytes: i64,
    pub objects_bytes: i64,
    pub staging_bytes: i64,
    pub object_count: i64,
    pub free_bytes: Option<i64>,
}

/// 任务/长操作受理响应。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AcceptedDto {
    pub job_id: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MediaLeaseDto {
    pub lease_id: String,
    pub blob_id: String,
    pub size: i64,
    pub mime: Option<String>,
    pub expires_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MediaChunkDto {
    pub lease_id: String,
    pub offset: i64,
    pub eof: bool,
    /// base64 编码的字节（有界分块；≤ 512 KiB 原始字节）。
    pub data_b64: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
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

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SearchStatusDto {
    /// 索引已消费的 Outbox 序号。
    pub indexed_seq: i64,
    pub total_seq: i64,
    pub lagging: bool,
    pub rebuilding: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SearchResultsDto {
    pub items: Vec<SearchResultItemDto>,
    pub next_cursor: Option<String>,
    /// 实际使用的检索路径：fts / gram / literal。
    pub mode: String,
    pub status: SearchStatusDto,
}

/// daemon 命令名常量（客户端与服务端共同引用，避免拼写漂移）。
pub mod commands {
    pub const DAEMON_STATUS: &str = "daemon.status";
    pub const DAEMON_PAUSE: &str = "daemon.pause";
    pub const DAEMON_SHUTDOWN: &str = "daemon.shutdown";
    pub const CREATOR_CREATE: &str = "creator.create";
    pub const CREATOR_UPDATE: &str = "creator.update";
    pub const CREATOR_LIST: &str = "creator.list";
    pub const CREATOR_GET: &str = "creator.get";
    pub const ACCOUNT_BIND: &str = "account.bind";
    pub const ACCOUNT_UNBIND: &str = "account.unbind";
    pub const ACCOUNT_UPDATE: &str = "account.update";
    pub const ACCOUNT_LIST: &str = "account.list";
    pub const SUBSCRIPTION_CREATE: &str = "subscription.create";
    pub const SUBSCRIPTION_UPDATE: &str = "subscription.update";
    pub const SUBSCRIPTION_CHECK_NOW: &str = "subscription.check_now";
    pub const SUBSCRIPTION_LIST: &str = "subscription.list";
    pub const ARCHIVE_TIMELINE: &str = "archive.timeline";
    pub const ARCHIVE_ENTRY_GET: &str = "archive.entry.get";
    pub const JOB_LIST: &str = "job.list";
    pub const JOB_GET: &str = "job.get";
    pub const JOB_RETRY: &str = "job.retry";
    pub const JOB_CANCEL: &str = "job.cancel";
    pub const CREDENTIAL_LIST: &str = "credential.list";
    pub const CREDENTIAL_IMPORT: &str = "credential.import";
    pub const CREDENTIAL_VALIDATE: &str = "credential.validate";
    pub const CREDENTIAL_REVOKE: &str = "credential.revoke";
    pub const PLUGIN_LIST: &str = "plugin.list";
    pub const PLUGIN_SET_STATE: &str = "plugin.set_state";
    pub const STORAGE_USAGE: &str = "storage.usage";
    pub const EXPORT_START: &str = "storage.export.start";
    pub const BACKUP_START: &str = "storage.backup.start";
    pub const BACKUP_LIST: &str = "storage.backup.list";
    pub const RESTORE_START: &str = "storage.restore.start";
    pub const ARCHIVE_OPEN_SOURCE: &str = "archive.open_source";
    pub const MEDIA_RELEASE: &str = "media.release";
    pub const MEDIA_OPEN_EXTERNAL: &str = "media.open_external";
    pub const MEDIA_LEASE: &str = "media.lease";
    pub const MEDIA_READ: &str = "media.read_range";
    pub const SEARCH_QUERY: &str = "search.query";
}
