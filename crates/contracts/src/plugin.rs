//! 插件协议 DTO（核心 ↔ 平台适配器，docs/design.md §8）。
//!
//! 双向帧：任何一侧都可以发起请求（适配器通过 `proxy.fetch` 使用核心的
//! 受限 HTTP 代理），响应用 `request_id` 关联。媒体字节不经 JSON 帧；
//! 适配器只返回候选访问计划，由核心传输引擎下载。

use serde::{Deserialize, Serialize};
use serde_json::Value;
use trace_domain::errors::FailureClass;

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
    #[serde(default)]
    pub pinned: bool,
    /// 列表摘要（不可覆盖完整详情）。
    #[serde(default)]
    pub summary_body: Option<String>,
    #[serde(default)]
    pub stats: Value,
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
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ObservedDetail {
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

/// resolve_media：媒体候选访问计划（不含 Cookie；URL 为短期签名地址）。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ResolveMediaParams {
    pub native_entry_key: String,
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
}

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
