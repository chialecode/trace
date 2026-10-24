//! 正交状态枚举（docs/design.md §5.4）与任务/文件状态机。
//!
//! 不使用单一字段混合“已下载、登录失败、来源删除”等不同概念。

use serde::{Deserialize, Serialize};

/// 来源可访问性：依据最近有效来源观察。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, Default)]
#[serde(rename_all = "snake_case")]
pub enum Accessibility {
    #[default]
    Unknown,
    Visible,
    Restricted,
    Unavailable,
    DeletedConfirmed,
}

/// 资源保存程度：由本次版本所选资源清单与有效副本判断。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, Default)]
#[serde(rename_all = "snake_case")]
pub enum SaveProgress {
    #[default]
    Undiscovered,
    Pending,
    Partial,
    Complete,
    /// 视频按订阅设置未下载：与“缺失”“失败”分开，不计入必需资源的缺口。
    Deferred,
    Unknown,
}

/// 质量满足情况。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, Default)]
#[serde(rename_all = "snake_case")]
pub enum QualitySatisfaction {
    #[default]
    Unknown,
    MeetsTarget,
    BelowTarget,
    Incomparable,
    Pinned,
}

/// 历史覆盖状态。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, Default)]
#[serde(rename_all = "snake_case")]
pub enum Coverage {
    #[default]
    NotScanned,
    Scanning,
    BoundedComplete,
    Unknown,
}

/// 副本可用性。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, Default)]
#[serde(rename_all = "snake_case")]
pub enum ReplicaState {
    #[default]
    Absent,
    Transferring,
    Staged,
    Verified,
    Offline,
    Corrupt,
    Retired,
}

/// 逻辑任务状态（docs/design.md §9.2 状态机）。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, Default)]
#[serde(rename_all = "snake_case")]
pub enum JobState {
    #[default]
    Queued,
    Leased,
    Running,
    WaitingRetry,
    WaitingAuth,
    WaitingPlugin,
    WaitingStorage,
    WaitingUser,
    Paused,
    Succeeded,
    Failed,
    Cancelled,
}

impl JobState {
    /// 终态（可由用户显式重试产生新执行，但不自动复活）。
    pub fn is_terminal(self) -> bool {
        matches!(
            self,
            JobState::Succeeded | JobState::Failed | JobState::Cancelled
        )
    }

    /// 等待类状态：有明确唤醒条件，不进入高频自动重试。
    pub fn is_waiting(self) -> bool {
        matches!(
            self,
            JobState::WaitingAuth
                | JobState::WaitingPlugin
                | JobState::WaitingStorage
                | JobState::WaitingUser
        )
    }

    /// 合法迁移表；调度器据此拒绝非法迁移（如终态直接改回 running）。
    pub fn can_transition_to(self, next: JobState) -> bool {
        use JobState::*;
        match self {
            Queued | Leased | Running => true,
            WaitingRetry | WaitingAuth | WaitingPlugin | WaitingStorage | WaitingUser | Paused => {
                matches!(next, Queued | Leased | Running | Cancelled | Failed)
            }
            Succeeded | Failed | Cancelled => false,
        }
    }
}

/// 文件提交阶段（docs/design.md §6.3）。等待网络/存储属于 Job 状态，
/// 文件操作只保留最后确认的阶段。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, Default)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum MaterializationStage {
    #[default]
    Receiving,
    Validating,
    Prepared,
    ObjectReady,
    Committed,
    Superseded,
    Quarantined,
}

/// 平台插件状态（docs/design.md §8.4）。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, Default)]
#[serde(rename_all = "snake_case")]
pub enum PluginState {
    #[default]
    Installed,
    Starting,
    Ready,
    Draining,
    Disabled,
    Incompatible,
    Degraded,
}

/// 订阅执行状态。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, Default)]
#[serde(rename_all = "snake_case")]
pub enum SubscriptionState {
    #[default]
    Enabled,
    Paused,
    Disabled,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn job_terminal_states_reject_reactivation() {
        assert!(!JobState::Succeeded.can_transition_to(JobState::Queued));
        assert!(!JobState::Failed.can_transition_to(JobState::Running));
        assert!(JobState::WaitingStorage.can_transition_to(JobState::Queued));
        assert!(JobState::Running.can_transition_to(JobState::WaitingAuth));
        assert!(JobState::Cancelled.is_terminal());
        assert!(JobState::WaitingPlugin.is_waiting());
    }

    #[test]
    fn serde_uses_snake_case() {
        let json = serde_json::to_string(&JobState::WaitingStorage).unwrap();
        assert_eq!(json, "\"waiting_storage\"");
        let json2 = serde_json::to_string(&MaterializationStage::ObjectReady).unwrap();
        assert_eq!(json2, "\"OBJECT_READY\"");
    }
}
