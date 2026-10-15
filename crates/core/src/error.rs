//! 核心统一错误类型。

use trace_contracts::control::{ApiError, ErrorCode};
use trace_domain::errors::FailureClass;

#[derive(Debug, thiserror::Error)]
pub enum CoreError {
    #[error("db error: {0}")]
    Db(#[from] rusqlite::Error),
    #[error("io error: {0}")]
    Io(#[from] std::io::Error),
    #[error("json error: {0}")]
    Json(#[from] serde_json::Error),
    #[error("invalid input: {0}")]
    InvalidInput(String),
    #[error("not found: {0}")]
    NotFound(String),
    #[error("version conflict on {0}")]
    VersionConflict(String),
    #[error("paused: {0}")]
    Paused(String),
    #[error("plugin unavailable: {0}")]
    PluginUnavailable(String),
    #[error("storage full: {0}")]
    StorageFull(String),
    #[error("auth required: {0}")]
    AuthRequired(String),
    #[error("rate limited{0}")]
    RateLimited(String),
    #[error("source unavailable: {0}")]
    SourceUnavailable(String),
    #[error("network: {0}")]
    Network(String),
    #[error("adapter failure: {0:?}")]
    Adapter(FailureClass),
    #[error("cancelled")]
    Cancelled,
    #[error("unsupported: {0}")]
    Unsupported(String),
    #[error("internal: {0}")]
    Internal(String),
}

impl From<crate::config::ConfigError> for CoreError {
    fn from(e: crate::config::ConfigError) -> Self {
        CoreError::Internal(e.to_string())
    }
}

impl CoreError {
    pub fn error_code(&self) -> ErrorCode {
        match self {
            CoreError::InvalidInput(_) => ErrorCode::InvalidInput,
            CoreError::NotFound(_) => ErrorCode::NotFound,
            CoreError::VersionConflict(_) => ErrorCode::VersionConflict,
            CoreError::Paused(_) => ErrorCode::DaemonPaused,
            CoreError::PluginUnavailable(_) => ErrorCode::PluginUnavailable,
            CoreError::StorageFull(_) => ErrorCode::StorageFull,
            CoreError::AuthRequired(_) => ErrorCode::AuthRequired,
            CoreError::RateLimited(_) => ErrorCode::RateLimited,
            CoreError::Cancelled => ErrorCode::Cancelled,
            CoreError::Unsupported(_) => ErrorCode::Unsupported,
            CoreError::Db(_) | CoreError::Io(_) | CoreError::Json(_) | CoreError::Internal(_) => {
                ErrorCode::Internal
            }
            CoreError::Adapter(
                FailureClass::AuthRequired
                | FailureClass::PermissionDenied
                | FailureClass::CapabilityLost,
            ) => ErrorCode::AuthRequired,
            CoreError::Adapter(FailureClass::RateLimited) => ErrorCode::RateLimited,
            CoreError::Adapter(FailureClass::PluginCrash) => ErrorCode::PluginUnavailable,
            CoreError::Adapter(FailureClass::Unsupported) => ErrorCode::Unsupported,
            CoreError::Adapter(FailureClass::InvalidPayload | FailureClass::SchemaChanged) => {
                ErrorCode::InvalidInput
            }
            CoreError::Adapter(_) | CoreError::Network(_) | CoreError::SourceUnavailable(_) => {
                ErrorCode::Internal
            }
        }
    }

    pub fn failure_class(&self) -> FailureClass {
        match self {
            CoreError::Network(_) => FailureClass::TransientNetwork,
            CoreError::SourceUnavailable(_) => FailureClass::SourceUnavailable,
            CoreError::RateLimited(_) => FailureClass::RateLimited,
            CoreError::AuthRequired(_) => FailureClass::AuthRequired,
            CoreError::PluginUnavailable(_) => FailureClass::PluginCrash,
            CoreError::StorageFull(_) => FailureClass::StorageFull,
            CoreError::Cancelled => FailureClass::Cancelled,
            CoreError::Adapter(class) => *class,
            _ => FailureClass::Unknown,
        }
    }

    /// 转换为 API 错误：只携带安全字段（C13/C15——不得回显原始错误体）。
    pub fn to_api_error(&self) -> ApiError {
        ApiError {
            code: self.error_code(),
            retryable: matches!(self, CoreError::Adapter(class) if class.suggest_retryable())
                || matches!(
                    self,
                    CoreError::Network(_)
                        | CoreError::SourceUnavailable(_)
                        | CoreError::RateLimited(_)
                        | CoreError::StorageFull(_)
                ),
            user_action: self.user_action(),
            related_ids: Vec::new(),
            details: serde_json::json!({}),
        }
    }

    fn user_action(&self) -> Option<String> {
        match self {
            CoreError::SourceUnavailable(reason) if reason == "room is not live" => {
                Some("该房间当前未开播，请等待开播或选择另一个直播链接".into())
            }
            CoreError::SourceUnavailable(_) => {
                Some("来源当前没有可用资源，请检查来源状态或稍后重试".into())
            }
            CoreError::Adapter(FailureClass::Unsupported) => {
                Some("该来源格式或传输方式当前不受支持".into())
            }
            CoreError::Adapter(FailureClass::AuthRequired) => Some("重新登录该平台账号".into()),
            CoreError::Adapter(FailureClass::PermissionDenied | FailureClass::CapabilityLost) => {
                Some("检查该账号的来源访问权限".into())
            }
            CoreError::Adapter(
                FailureClass::SchemaChanged
                | FailureClass::InvalidPayload
                | FailureClass::PluginCrash,
            ) => Some("检查平台模块状态与更新".into()),
            CoreError::Adapter(FailureClass::StorageFull) => Some("释放存储空间后重试".into()),
            CoreError::AuthRequired(_) => Some("重新登录该平台账号".into()),
            CoreError::VersionConflict(_) => Some("刷新当前内容后重试".into()),
            CoreError::StorageFull(_) => Some("释放存储空间后重试".into()),
            CoreError::PluginUnavailable(_) => Some("检查平台模块状态".into()),
            CoreError::Paused(_) => Some("恢复后台采集后重试".into()),
            _ => None,
        }
    }
}
