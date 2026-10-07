//! 失败分类（INV-08、docs/design.md §8.4）。
//!
//! 认证失败、挑战/人工处理、限流、离线/超时、存储不足、输入错误、
//! 版本冲突、插件不可用和未知远端响应不允许全部归入“失败请重试”。

use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum FailureClass {
    TransientNetwork,
    RateLimited,
    AuthRequired,
    PermissionDenied,
    CapabilityLost,
    SourceUnavailable,
    SourceDeletedConfirmed,
    Unsupported,
    SchemaChanged,
    InvalidPayload,
    PluginCrash,
    StorageFull,
    VersionConflict,
    Cancelled,
    Unknown,
}

impl FailureClass {
    /// 核心最终决定重试政策；此处是分类自带的建议。
    pub fn suggest_retryable(self) -> bool {
        matches!(
            self,
            FailureClass::TransientNetwork
                | FailureClass::RateLimited
                | FailureClass::SourceUnavailable
                | FailureClass::PluginCrash
                | FailureClass::StorageFull
        )
    }

    /// 需要用户/凭证动作的类别（进入 waiting_auth / waiting_user）。
    pub fn requires_user_action(self) -> bool {
        matches!(
            self,
            FailureClass::AuthRequired
                | FailureClass::PermissionDenied
                | FailureClass::CapabilityLost
        )
    }

    /// 来源删除的确认必须有权威证据；单次消失/404 不落入此类别。
    pub fn is_confirmed_deletion(self) -> bool {
        matches!(self, FailureClass::SourceDeletedConfirmed)
    }
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Failure {
    pub class: FailureClass,
    pub message: String,
    pub retry_after_ms: Option<u64>,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn classification_boundaries() {
        assert!(FailureClass::TransientNetwork.suggest_retryable());
        assert!(!FailureClass::AuthRequired.suggest_retryable());
        assert!(FailureClass::AuthRequired.requires_user_action());
        assert!(!FailureClass::RateLimited.requires_user_action());
        assert!(FailureClass::SourceDeletedConfirmed.is_confirmed_deletion());
        assert!(!FailureClass::SourceUnavailable.is_confirmed_deletion());
    }

    #[test]
    fn serde_snake_case() {
        assert_eq!(
            serde_json::to_string(&FailureClass::PluginCrash).unwrap(),
            "\"plugin_crash\""
        );
    }
}
