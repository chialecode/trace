//! 领域实体（docs/design.md §5）。
//!
//! 这些结构描述应有行为；SQLite 列映射见 `trace-core::storage::schema`。
//! 统计类字段（播放量等）只出现在观察属性中，不进入内容语义。

use crate::ids::*;
use crate::states::*;
use crate::time::TraceTime;
use serde::{Deserialize, Serialize};
use std::collections::BTreeMap;

/// 创作者：用户维护的统一身份。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Creator {
    pub creator_id: CreatorId,
    pub display_name: String,
    pub aliases: Vec<String>,
    pub tags: Vec<String>,
    pub status: CreatorStatus,
    pub notes: String,
    pub created_at: TraceTime,
    pub updated_at: TraceTime,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, Default)]
#[serde(rename_all = "snake_case")]
pub enum CreatorStatus {
    #[default]
    Active,
    Archived,
}

/// 来源账号：平台稳定身份；昵称/主页是可变资料（INV-01）。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct SourceAccount {
    pub account_id: AccountId,
    pub namespace: SourceNamespace,
    pub native_account_key: String,
    pub display_name: Option<String>,
    pub profile_url: Option<String>,
    pub first_seen_at: TraceTime,
    pub profile_updated_at: Option<TraceTime>,
}

/// 创作者 ↔ 来源账号 关联；默认一个账号一个有效创作者关联。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct AccountBinding {
    pub binding_id: String,
    pub creator_id: CreatorId,
    pub account_id: AccountId,
    pub state: BindingState,
    pub created_at: TraceTime,
    pub ended_at: Option<TraceTime>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, Default)]
#[serde(rename_all = "snake_case")]
pub enum BindingState {
    #[default]
    Active,
    Ended,
}

/// 发布记录（Entry）：来源三元组唯一。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Entry {
    pub entry_id: EntryId,
    pub namespace: SourceNamespace,
    /// 内容种类：M1a 普通视频为 `video`；保留未来扩展（live/image 等）。
    pub native_kind: String,
    pub native_entry_key: String,
    pub account_id: AccountId,
    pub first_seen_at: TraceTime,
    /// 来源发布时间；未知保持 None（FR-BROWSE-002）。
    pub published_at: Option<TraceTime>,
    pub lifecycle: EntryLifecycle,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, Default)]
#[serde(rename_all = "snake_case")]
pub enum EntryLifecycle {
    #[default]
    Active,
    /// 用户明确删除进入等待回收；来源消失不产生此状态。
    DeletedPending,
    Deleted,
}

/// 已观察内容版本（INV-02：新观察不原地覆盖旧版）。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct EntryRevision {
    pub revision_id: RevisionId,
    pub entry_id: EntryId,
    /// 语义摘要：规范化正文 + 资源身份的稳定哈希（不包含统计）。
    pub semantic_digest: String,
    pub title: String,
    /// 规范化正文文本（已清理，不含脚本）。
    pub normalized_body: String,
    /// 来源版本证据（如来源版本号）；无则记录观察顺序。
    pub source_version_evidence: Option<String>,
    pub observed_at: TraceTime,
    pub media_items: Vec<MediaItem>,
}

/// 来源观察：记录重现/删除/可见性变化，不物理删除既有版本。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct SourceObservation {
    pub observation_id: String,
    pub entry_id: EntryId,
    pub revision_id: Option<RevisionId>,
    pub access_state: Accessibility,
    pub observed_at: TraceTime,
    /// 单调递增观察序号（A→B→A 重现的事实）。
    pub observation_seq: i64,
    pub evidence: ObservationEvidence,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, Default)]
pub struct ObservationEvidence {
    /// 来源可用的排序证据（如列表接口位置、响应结构版本）。
    pub detail: BTreeMap<String, String>,
}

/// 媒体项：同一版本内的语义角色与顺序。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct MediaItem {
    pub media_id: MediaId,
    pub revision_id: RevisionId,
    /// main_video / audio / cover / image / subtitle / extra_part。
    pub role: String,
    /// 展示顺序（如分 P 序号）。
    pub position: i64,
    /// 来源附件身份（如 cid、分段索引）。
    pub source_attachment_id: String,
    /// 资源组标识（同一 P 的视频+音频同组）。
    pub group_key: Option<String>,
    pub title: Option<String>,
}

/// 媒体变体：已取得的表示；保存声称与实测规格。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct MediaVariant {
    pub variant_id: VariantId,
    pub media_id: MediaId,
    pub source_representation: String,
    pub blob_id: BlobId,
    pub claimed_spec: MediaSpec,
    pub measured_spec: Option<MediaSpec>,
    pub created_at: TraceTime,
}

/// 规格：来源标签为声称值，探测结果单独保存（FR-QUALITY-001）。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, Default)]
pub struct MediaSpec {
    pub container: Option<String>,
    pub codec: Option<String>,
    pub width: Option<i64>,
    pub height: Option<i64>,
    pub fps: Option<f64>,
    pub duration_ms: Option<i64>,
    pub bitrate_bps: Option<i64>,
    pub audio_codec: Option<String>,
    pub audio_channels: Option<i64>,
    /// 实际轨道数（无声视频为 0，需证据）。
    pub audio_tracks: Option<i64>,
}

/// 字节实体：内容寻址，独立于来源与路径（INV-04）。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Blob {
    pub blob_id: BlobId,
    pub size: i64,
    pub mime: Option<String>,
    pub structure_check: Option<String>,
    pub first_seen_at: TraceTime,
}

/// 托管副本。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Replica {
    pub replica_id: String,
    pub blob_id: BlobId,
    pub node_id: NodeId,
    pub store_id: String,
    pub locator: String,
    pub managed: bool,
    pub state: ReplicaState,
    pub verified_at: Option<TraceTime>,
}

/// 订阅配置。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Subscription {
    pub subscription_id: SubscriptionId,
    pub account_id: AccountId,
    pub content_type: String,
    pub range: SubscriptionRange,
    pub quality_policy_version: String,
    pub login_credential_id: Option<CredentialId>,
    pub config_version: i64,
    pub state: SubscriptionState,
    pub created_at: TraceTime,
    pub updated_at: TraceTime,
}

/// 历史范围（M1a：现在开始 / 有界近期；完整自定义回填在 M1b）。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, Default)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum SubscriptionRange {
    #[default]
    FromNow,
    /// 最近 N 天（有界）。
    RecentDays { days: i64 },
}

/// 逻辑任务（Job）。目标 = 类型 + 主体 + 代次 + 策略版本。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Job {
    pub job_id: JobId,
    /// 稳定去重键（工作类型+主体+代次），唯一约束防重复（INV-09）。
    pub job_key: String,
    pub job_type: JobType,
    pub subject_id: String,
    pub target_generation: i64,
    pub config_version: i64,
    pub priority: i32,
    pub state: JobState,
    pub next_run_at: Option<TraceTime>,
    pub lease_owner: Option<InstanceId>,
    pub lease_expires_at: Option<TraceTime>,
    pub attempts: i64,
    pub last_error: Option<JobError>,
    pub created_at: TraceTime,
    pub updated_at: TraceTime,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum JobType {
    /// 订阅枚举检查（增量/历史扫描同一类型，范围在任务载荷）。
    Scan,
    /// 单条内容详细观察。
    Observe,
    /// 媒体获取。
    MediaDownload,
    /// 搜索索引/清单派生（Outbox 驱动）。
    Derive,
    /// 备份/导出长操作。
    DataOp,
    /// 副本恢复。
    Recover,
}

/// 任务失败的结构化记录（分类而非“失败请重试”）。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct JobError {
    pub class: crate::errors::FailureClass,
    pub message: String,
    pub retry_after_ms: Option<u64>,
    pub attempt_at: TraceTime,
}

/// 执行尝试。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Attempt {
    pub attempt_id: AttemptId,
    pub job_id: JobId,
    pub owner_instance_id: InstanceId,
    pub attempt_no: i64,
    pub started_at: TraceTime,
    pub ended_at: Option<TraceTime>,
    pub result: Option<AttemptResult>,
    pub error: Option<JobError>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum AttemptResult {
    Succeeded,
    Failed,
    Cancelled,
}

/// 文件提交操作日志（docs/design.md §6.3）。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Materialization {
    pub operation_id: OperationId,
    pub job_id: Option<JobId>,
    pub subject_kind: String,
    pub subject_id: String,
    pub generation: i64,
    pub stage: MaterializationStage,
    pub staging_path: Option<String>,
    pub expected_size: Option<i64>,
    pub expected_sha256: Option<String>,
    pub received_bytes: i64,
    pub received_sha256: Option<String>,
    pub final_path: Option<String>,
    pub blob_id: Option<BlobId>,
    pub created_at: TraceTime,
    pub updated_at: TraceTime,
}

/// 删除记录：作用范围 + 代次，防止旧操作复活档案（INV-12 预留）。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct DeletionRecord {
    pub record_id: DeletionRecordId,
    pub scope: DeletionScope,
    pub object_id: String,
    pub generation: i64,
    pub reason: String,
    pub created_at: TraceTime,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum DeletionScope {
    Replica,
    Entry,
    Creator,
}

/// 告警问题（可按键合并的持续故障）。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Issue {
    pub issue_key: String,
    pub category: String,
    pub severity: IssueSeverity,
    pub detail: String,
    pub user_action: Option<String>,
    pub related_refs: Vec<String>,
    pub first_seen_at: TraceTime,
    pub last_seen_at: TraceTime,
    pub resolved_at: Option<TraceTime>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum IssueSeverity {
    Info,
    Warning,
    Error,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn revision_serialization_roundtrip() {
        let rev = EntryRevision {
            revision_id: RevisionId::generate(),
            entry_id: EntryId::generate(),
            semantic_digest: "sha256:aa".into(),
            title: "标题".into(),
            normalized_body: "正文".into(),
            source_version_evidence: None,
            observed_at: TraceTime::now(crate::time::TimePrecision::Discovery),
            media_items: vec![],
        };
        let json = serde_json::to_string(&rev).unwrap();
        let back: EntryRevision = serde_json::from_str(&json).unwrap();
        assert_eq!(back, rev);
    }

    #[test]
    fn published_time_is_optional() {
        let json = r#"{"rfc3339":"2024-01-01T00:00:00Z","precision":"source"}"#;
        let t: TraceTime = serde_json::from_str(json).unwrap();
        assert_eq!(t.precision, crate::time::TimePrecision::Source);
    }
}
