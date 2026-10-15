//! 任务类型与扫描模式。
//!
//! 数据库里的 `jobs.job_type`、`jobs.payload.mode` 仍是下面这些固定字符串；本模块把它们收成
//! 枚举，让拼写错误在编译期暴露，并保证旧库里的 JSON 原样可读（不改 schema）。

use serde::{Deserialize, Serialize};
use std::fmt;

/// 持久任务类型（`jobs.job_type`）。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum JobType {
    Scan,
    Observe,
    Media,
    Record,
    LiveAuxiliary,
    SourceAcquire,
    AuthCompletion,
    CredentialProbe,
    AccountProfile,
    QualityMaintenance,
    Backup,
    Export,
    /// Writes the readable copies a finished download request asked for.
    Delivery,
    /// Processes an explicit local import batch.
    Import,
}

impl JobType {
    pub const fn as_str(self) -> &'static str {
        match self {
            Self::Scan => "scan",
            Self::Observe => "observe",
            Self::Media => "media",
            Self::Record => "record",
            Self::LiveAuxiliary => "live_auxiliary",
            Self::SourceAcquire => "source_acquire",
            Self::AuthCompletion => "auth_completion",
            Self::CredentialProbe => "credential_probe",
            Self::AccountProfile => "account_profile",
            Self::QualityMaintenance => "quality_maintenance",
            Self::Backup => "backup",
            Self::Export => "export",
            Self::Delivery => "delivery",
            Self::Import => "import",
        }
    }

    /// 未知字符串返回 `None`，由调用方按“未知任务类型”处理。
    pub fn parse(text: &str) -> Option<Self> {
        [
            Self::Scan,
            Self::Observe,
            Self::Media,
            Self::Record,
            Self::LiveAuxiliary,
            Self::SourceAcquire,
            Self::AuthCompletion,
            Self::CredentialProbe,
            Self::AccountProfile,
            Self::QualityMaintenance,
            Self::Backup,
            Self::Export,
            Self::Delivery,
            Self::Import,
        ]
        .into_iter()
        .find(|t| t.as_str() == text)
    }
}

impl fmt::Display for JobType {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

/// 订阅扫描的三条互相独立的游标通道。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ScanMode {
    /// 近期新内容；循环执行。
    Incremental,
    /// 回溯历史；到达范围边界后结束。
    History,
    /// 低频全量核对，发现缺漏与变化。
    Deep,
}

impl ScanMode {
    pub const fn as_str(self) -> &'static str {
        match self {
            Self::Incremental => "incremental",
            Self::History => "history",
            Self::Deep => "deep",
        }
    }

    pub fn parse(text: &str) -> Option<Self> {
        [Self::Incremental, Self::History, Self::Deep]
            .into_iter()
            .find(|m| m.as_str() == text)
    }

    /// 稳定任务键 `scan:<订阅>:<模式>`；同一订阅同一通道只有一个活动任务。
    pub fn job_key(self, subscription_id: &str) -> String {
        format!("scan:{subscription_id}:{}", self.as_str())
    }

    /// 近期通道优先于回溯与核对。
    pub const fn priority(self) -> i32 {
        match self {
            Self::Incremental => 1,
            Self::History | Self::Deep => 2,
        }
    }

    /// 新订阅需要的通道：直播只轮询；`from_now` 范围不回溯历史。
    pub fn lanes(live: bool, from_now: bool) -> &'static [ScanMode] {
        match (live, from_now) {
            (true, _) => &[Self::Incremental],
            (false, false) => &[Self::Incremental, Self::History, Self::Deep],
            (false, true) => &[Self::Incremental, Self::Deep],
        }
    }
}

impl fmt::Display for ScanMode {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

/// 扫描任务载荷；序列化结果与既有库里的 JSON 完全一致。
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct ScanPayload {
    pub subscription_id: String,
    pub mode: ScanMode,
}

impl ScanPayload {
    pub fn new(subscription_id: impl Into<String>, mode: ScanMode) -> Self {
        Self {
            subscription_id: subscription_id.into(),
            mode,
        }
    }

    /// 写入 `jobs.payload` 的 JSON 文本。
    pub fn to_json_text(&self) -> String {
        serde_json::to_string(self).expect("scan payload serializes")
    }
}

/// 从任务载荷读扫描模式：缺失视为近期通道（旧任务没有该字段）；未知取值是错误，不静默当作别的通道。
pub fn scan_mode_of(payload: &serde_json::Value) -> Result<ScanMode, String> {
    match payload.get("mode") {
        None | Some(serde_json::Value::Null) => Ok(ScanMode::Incremental),
        Some(serde_json::Value::String(text)) => {
            ScanMode::parse(text).ok_or_else(|| format!("unknown scan mode {text}"))
        }
        Some(_) => Err("scan mode is not text".into()),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn job_type_names_round_trip_and_unknown_is_rejected() {
        for t in [
            JobType::Scan,
            JobType::Observe,
            JobType::Media,
            JobType::Record,
            JobType::LiveAuxiliary,
            JobType::SourceAcquire,
            JobType::AuthCompletion,
            JobType::CredentialProbe,
            JobType::AccountProfile,
            JobType::QualityMaintenance,
            JobType::Backup,
            JobType::Export,
        ] {
            assert_eq!(JobType::parse(t.as_str()), Some(t));
            assert_eq!(serde_json::to_value(t).unwrap(), json!(t.as_str()));
        }
        assert_eq!(JobType::parse("Scan"), None);
        assert_eq!(JobType::parse(""), None);
    }

    #[test]
    fn scan_payload_serializes_exactly_like_rows_written_before_typing() {
        let typed = ScanPayload::new("sub-1", ScanMode::History).to_json_text();
        let legacy = json!({"subscription_id":"sub-1","mode":"history"}).to_string();
        assert_eq!(
            serde_json::from_str::<serde_json::Value>(&typed).unwrap(),
            serde_json::from_str::<serde_json::Value>(&legacy).unwrap()
        );
        let read: ScanPayload = serde_json::from_str(&legacy).unwrap();
        assert_eq!(read, ScanPayload::new("sub-1", ScanMode::History));
    }

    #[test]
    fn missing_scan_mode_means_incremental_but_garbage_is_an_error() {
        assert_eq!(
            scan_mode_of(&json!({"subscription_id":"s"})),
            Ok(ScanMode::Incremental)
        );
        assert_eq!(
            scan_mode_of(&json!({"mode":null})),
            Ok(ScanMode::Incremental)
        );
        assert_eq!(scan_mode_of(&json!({"mode":"deep"})), Ok(ScanMode::Deep));
        assert!(scan_mode_of(&json!({"mode":"Deep"})).is_err());
        assert!(scan_mode_of(&json!({"mode":3})).is_err());
    }

    #[test]
    fn lanes_follow_content_and_range() {
        assert_eq!(ScanMode::lanes(true, false), &[ScanMode::Incremental]);
        assert_eq!(ScanMode::lanes(true, true), &[ScanMode::Incremental]);
        assert_eq!(
            ScanMode::lanes(false, false),
            &[ScanMode::Incremental, ScanMode::History, ScanMode::Deep]
        );
        assert_eq!(
            ScanMode::lanes(false, true),
            &[ScanMode::Incremental, ScanMode::Deep]
        );
        assert_eq!(ScanMode::Deep.job_key("s"), "scan:s:deep");
        assert_eq!(ScanMode::Incremental.priority(), 1);
        assert_eq!(ScanMode::History.priority(), 2);
    }
}
