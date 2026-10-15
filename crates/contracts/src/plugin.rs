//! 插件协议 DTO（核心 ↔ 平台适配器，docs/design.md §8）。
//!
//! 双向帧：任何一侧都可以发起请求（适配器通过 `proxy.fetch` 使用核心的
//! 受限 HTTP 代理），响应用 `request_id` 关联。媒体字节不经 JSON 帧；
//! 适配器只返回候选访问计划，由核心传输引擎下载。

use serde::{Deserialize, Serialize};
use serde_json::Value;
use trace_domain::errors::FailureClass;

/// Finite public history samples are independent of recording completeness.
/// Device identity, cookies and continuation internals never cross this DTO.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LiveAuxiliary {
    pub messages: Vec<LiveMessage>,
    pub coverage: String,
    pub source_count: u32,
    pub omitted: u32,
}
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LiveMessage {
    pub source_id: String,
    pub author: Option<String>,
    pub text: String,
    pub source_time: Option<String>,
    pub kind: String,
}

/// 主机 → 适配器请求。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PluginRequest {
    pub protocol_version: String,
    pub request_id: String,
    /// 截止时间（毫秒）；适配器必须在截止前响应或返回超时错误。
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub deadline_ms: Option<u64>,
    pub method: String,
    #[serde(default)]
    pub params: Value,
    /// Host-provided access context for a site whose public API needs a device identifier
    /// (DEC-041). It never carries a login Cookie or a session token.
    #[serde(default, skip_serializing_if = "Value::is_null")]
    pub context: Value,
}

/// 通用响应（双向共用）。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PluginResponse {
    pub request_id: String,
    pub ok: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub result: Option<Value>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub error: Option<PluginError>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PluginError {
    pub class: FailureClass,
    pub message: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub retry_after_ms: Option<u64>,
    /// 安全细节：结构化白名单字段，禁止原始响应直出。
    #[serde(default)]
    pub details: Value,
}

/// 适配器握手结果。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AdapterHello {
    pub protocol_version: String,
    pub adapter_version: String,
    pub platform: String,
    pub capabilities: Vec<String>,
    pub limits: AdapterLimits,
    /// What the adapter can subscribe to, preview and download. Older adapters declare
    /// nothing; Core then derives `partial` rules from their capabilities.
    #[serde(default)]
    pub rules: Vec<crate::rules::RuleDeclaration>,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct AdapterLimits {
    #[serde(default = "default_max_page")]
    pub max_page_items: u32,
    #[serde(default)]
    pub supports_cancel: bool,
}

fn default_max_page() -> u32 {
    trace_domain::MAX_PAGE_ITEMS
}

/// resolve_account：主页 URL / 稳定 ID → 平台身份。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ResolveAccountParams {
    pub input: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ResolvedAccount {
    pub native_account_key: String,
    pub display_name: Option<String>,
    pub profile_url: Option<String>,
    pub evidence: Value,
}

/// enumerate：有界分页枚举。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct EnumerateParams {
    /// Independent subscription lane; old protocol peers default to video.
    #[serde(default = "default_content_type")]
    pub content_type: String,
    pub native_account_key: String,
    /// from_now / recent_days / resume_cursor。
    pub mode: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub cursor: Option<String>,
    /// recent_days 模式的有界天数。
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub recent_days: Option<i64>,
    /// 预算：本轮最多页数（含已处理页）。
    #[serde(default = "default_max_pages")]
    pub max_pages: u32,
}

fn default_max_pages() -> u32 {
    10
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct EnumeratedEntry {
    pub native_entry_key: String,
    pub native_kind: String,
    pub title: String,
    /// RFC3339；来源未提供时为 null 且 precision=unknown。
    pub published_at: Option<String>,
    #[serde(default)]
    pub published_precision: String,
    /// 精度为 `approximate` 时：真实发布时间可能偏离 `published_at` 多少秒（至少一个单位）。
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub published_margin_seconds: Option<i64>,
    #[serde(default)]
    pub pinned: bool,
    /// 列表摘要（不可覆盖完整详情）。
    #[serde(default)]
    pub summary_body: Option<String>,
    #[serde(default)]
    pub stats: Value,
    /// What a profile preview shows about the entry before it is observed in full.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub hints: Option<EntryHints>,
}

/// Counts and a thumbnail address the list already carried. All are optional claims; the
/// observed detail is the record.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct EntryHints {
    #[serde(default)]
    pub content_kinds: Vec<String>,
    #[serde(default)]
    pub image_count: u32,
    #[serde(default)]
    pub video_count: u32,
    #[serde(default)]
    pub duration_ms: Option<i64>,
    /// A thumbnail on one of the site's media hosts; Core fetches it on demand.
    #[serde(default)]
    pub cover_url: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct EnumeratedPage {
    pub items: Vec<EnumeratedEntry>,
    /// 续页游标（pn+1 或站点游标）；None 表示按适配器判断已到边界。
    pub next_cursor: Option<String>,
    pub has_more: bool,
    /// 覆盖证据：count 观察值、页范围、受限说明。
    pub coverage: Value,
}

/// observe：单条内容详情。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ObserveParams {
    pub native_entry_key: String,
    #[serde(default = "default_content_type")]
    pub native_kind: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ObservedDetail {
    #[serde(default)]
    pub document: crate::content::Document,
    #[serde(default)]
    pub metadata: Value,
    pub native_entry_key: String,
    pub native_kind: String,
    pub title: String,
    pub normalized_body: String,
    pub published_at: Option<String>,
    pub published_precision: String,
    pub access_state: String,
    /// 来源版本证据（结构版本号或观察时间）。
    pub source_version_evidence: Value,
    /// 资源身份列表（分 P、封面等）。
    pub resources: Vec<ResourceIdentityDto>,
    /// 观察完整性声明：full = 正文与资源清单完整。
    pub completeness: String,
    #[serde(default)]
    pub stats: Value,
    /// Site fields mapped to the unified metadata; absent for adapters that predate it.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub unified: Option<crate::unified::UnifiedMetadata>,
}

#[derive(Debug, Clone, PartialEq, Eq, PartialOrd, Ord, Serialize, Deserialize)]
pub struct ResourceIdentityDto {
    pub role: String,
    pub source_attachment_id: String,
    pub position: i64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub title: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub group_key: Option<String>,
    /// 该资源是否为归档必需（封面可选）。
    #[serde(default = "default_required")]
    pub required: bool,
}

fn default_required() -> bool {
    true
}

fn default_content_type() -> String {
    "video".into()
}

/// resolve_media：媒体候选访问计划（不含 Cookie；URL 为短期签名地址）。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ResolveMediaParams {
    pub native_entry_key: String,
    #[serde(default = "default_content_type")]
    pub native_kind: String,
    pub resources: Vec<ResourceIdentityDto>,
    /// 质量目标（候选排序参考，不改变保存事实）。
    #[serde(default)]
    pub quality_hint: Value,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MediaCandidate {
    pub resource: ResourceIdentityDto,
    pub representation: String,
    /// dash_video / dash_audio / muxed / durl_segment
    pub kind: String,
    /// 分段：durl 多段时按序全部列出；每段含主/备地址。
    pub segments: Vec<UrlSet>,
    /// 非敏感公共头（referer/user-agent 白名单）。
    #[serde(default)]
    pub public_headers: Value,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub expected_size: Option<i64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub expected_sha256: Option<String>,
    #[serde(default)]
    pub claimed_spec: Value,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct UrlSet {
    pub primary: String,
    #[serde(default)]
    pub backups: Vec<String>,
    /// This segment's known length/hash; candidate-level values describe the ordered whole.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub expected_size: Option<i64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub expected_sha256: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ResolveMediaResult {
    pub candidates: Vec<MediaCandidate>,
    #[serde(default)]
    pub notes: Value,
}

/// probe_auth：检查登录能力。
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct ProbeAuthParams {}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ProbeAuthResult {
    pub state: String,
    pub capability: Value,
}

/// 适配器 → 主机：受限 HTTP 代理请求。
/// 核心校验 scheme/host/重定向/私网地址，并按账号附加 Cookie。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ProxyFetchParams {
    pub method: String,
    pub url: String,
    /// 允许的公共头（host 侧白名单校验）。
    #[serde(default)]
    pub headers: Value,
    #[serde(default = "default_max_body")]
    pub max_bytes: u32,
    /// 是否允许携带该账号 Cookie（仅站点 API 域）。
    #[serde(default)]
    pub with_credential: bool,
    /// 只读查询的请求体（站点的 GraphQL 等）；仅 POST 接受，文本且不超过
    /// [`PROXY_BODY_MAX`] 字节，`content-type` 头必须是表单或 JSON。
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub body: Option<String>,
}

/// Largest request body an adapter may send through the proxy.
pub const PROXY_BODY_MAX: usize = 64 * 1024;

fn default_max_body() -> u32 {
    8 * 1024 * 1024
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ProxyFetchResult {
    pub status: u16,
    #[serde(default)]
    pub headers: Value,
    /// 响应体（≤ max_bytes；超限拒绝而不是截断）。
    pub body_b64: String,
    #[serde(default)]
    pub final_url: String,
}

/// 主机侧方法名常量。
pub mod methods {
    pub const HELLO: &str = "hello";
    pub const RESOLVE_ACCOUNT: &str = "resolve_account";
    pub const ENUMERATE: &str = "enumerate";
    pub const OBSERVE: &str = "observe";
    pub const RESOLVE_MEDIA: &str = "resolve_media";
    pub const PROBE_AUTH: &str = "probe_auth";
    pub const CANCEL: &str = "cancel";
    pub const SHUTDOWN: &str = "shutdown";
    pub const PROXY_FETCH: &str = "proxy.fetch";
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CancelParams {
    #[serde(default)]
    pub cancel_all: bool,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct ShutdownParams {}

/// Short-lived plan; URLs must never be persisted in a session record.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LiveStream {
    pub protocol: String,
    pub url: String,
    pub quality: String,
    #[serde(default)]
    pub audio_only: bool,
    #[serde(default)]
    pub public_headers: Value,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LiveSnapshot {
    pub room_key: String,
    /// live / offline / unknown; request failures are never offline.
    pub state: String,
    pub source_session: Option<String>,
    pub title: String,
    #[serde(default)]
    pub metadata: Value,
    #[serde(default)]
    pub streams: Vec<LiveStream>,
}
