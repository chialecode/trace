//! 稳定身份类型。
//!
//! 内部 ID 是无业务含义的 UUID（跨语言以字符串传递，避免 JS 精度丢失，
//! docs/design/m1a-runtime-contracts.md §2）；来源身份由
//! `(source_namespace, native_kind, native_entry_key)` 三元组唯一确定，
//! 昵称、URL 与插件安装 ID 均不进入身份（INV-01）。

use serde::{Deserialize, Serialize};
use std::fmt;

macro_rules! string_id {
    ($(#[$meta:meta])* $name:ident) => {
        $(#[$meta])*
        #[derive(Debug, Clone, PartialEq, Eq, Hash, PartialOrd, Ord, Serialize, Deserialize)]
        #[serde(transparent)]
        pub struct $name(String);

        impl $name {
            /// 生成新的随机内部 ID。
            pub fn generate() -> Self {
                Self(uuid::Uuid::new_v4().to_string())
            }

            /// 使用已有值构造（内部路径，要求非空）。
            pub fn new(value: impl Into<String>) -> Self {
                let value = value.into();
                assert!(!value.is_empty(), "id must not be empty");
                Self(value)
            }

            /// 校验并构造；不可信输入（协议消息）必须走此入口。
            pub fn parse(value: impl Into<String>) -> Result<Self, IdError> {
                let value = value.into();
                if value.is_empty() {
                    return Err(IdError::Empty);
                }
                if value.len() > 256 {
                    return Err(IdError::TooLong);
                }
                Ok(Self(value))
            }

            pub fn as_str(&self) -> &str {
                &self.0
            }
        }

        impl fmt::Display for $name {
            fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
                f.write_str(&self.0)
            }
        }
    };
}

#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum IdError {
    #[error("id must not be empty")]
    Empty,
    #[error("id exceeds 256 bytes")]
    TooLong,
}

string_id!(
    /// 创作者全局 ID，不随改名变化（FR-ID-001）。
    CreatorId
);
string_id!(
    /// 来源账号 ID：平台命名空间 + 稳定账号键。
    AccountId
);
string_id!(
    /// 发布记录（Entry）ID。
    EntryId
);
string_id!(
    /// 内容版本（Revision）ID。
    RevisionId
);
string_id!(
    /// 媒体项 ID。
    MediaId
);
string_id!(
    /// 媒体变体 ID。
    VariantId
);
string_id!(
    /// 订阅 ID。
    SubscriptionId
);
string_id!(
    /// 逻辑任务 ID。
    JobId
);
string_id!(
    /// 执行尝试 ID。
    AttemptId
);
string_id!(
    /// 文件提交操作 ID。
    OperationId
);
string_id!(
    /// 备份会话 ID。
    BackupId
);
string_id!(
    /// 导出任务 ID。
    ExportId
);
string_id!(
    /// 凭证绑定 ID（不含秘密本身）。
    CredentialId
);
string_id!(
    /// 采集节点 ID。
    NodeId
);
string_id!(
    /// daemon 实例 ID（每次启动唯一）。
    InstanceId
);
string_id!(
    /// 档案 ID。
    ArchiveId
);
string_id!(
    /// 删除记录 ID。
    DeletionRecordId
);
string_id!(
    /// 媒体处理操作 ID（探测、封装等）。
    MediaOpId
);

/// Blob 身份：`sha256:<64位小写hex>`。
#[derive(Debug, Clone, PartialEq, Eq, Hash, PartialOrd, Ord, Serialize, Deserialize)]
#[serde(transparent)]
pub struct BlobId(String);

impl BlobId {
    pub fn from_sha256_hex(hex: &str) -> Result<Self, IdError> {
        if hex.len() != 64 || !hex.bytes().all(|b| b.is_ascii_hexdigit()) {
            return Err(IdError::Empty);
        }
        Ok(Self(format!("sha256:{}", hex.to_ascii_lowercase())))
    }

    pub fn sha256_hex(&self) -> &str {
        self.0.split_once(':').map(|(_, h)| h).unwrap_or_default()
    }

    pub fn as_str(&self) -> &str {
        &self.0
    }
}

impl fmt::Display for BlobId {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.0)
    }
}

/// 来源命名空间：平台 + 必要的站点实例身份（docs/design.md §5.2）。
#[derive(Debug, Clone, PartialEq, Eq, Hash, PartialOrd, Ord, Serialize, Deserialize)]
#[serde(transparent)]
pub struct SourceNamespace(String);

impl SourceNamespace {
    pub fn new(platform: &str, instance: Option<&str>) -> Result<Self, IdError> {
        let value = match instance {
            Some(i) => format!("{}:{}", platform, i),
            None => platform.to_string(),
        };
        if value.is_empty() || value.len() > 128 {
            return Err(IdError::TooLong);
        }
        Ok(Self(value))
    }

    pub fn platform(&self) -> &str {
        self.0.split(':').next().unwrap_or_default()
    }

    pub fn as_str(&self) -> &str {
        &self.0
    }
}

impl fmt::Display for SourceNamespace {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.0)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn generated_ids_are_unique_and_non_empty() {
        let a = CreatorId::generate();
        let b = CreatorId::generate();
        assert_ne!(a, b);
        assert!(!a.as_str().is_empty());
    }

    #[test]
    fn blob_id_validates_hash_shape() {
        let good = "a".repeat(64);
        let id = BlobId::from_sha256_hex(&good).expect("valid hash");
        assert_eq!(id.sha256_hex(), good);
        assert!(BlobId::from_sha256_hex("xyz").is_err());
        assert!(BlobId::from_sha256_hex(&"A".repeat(64)).is_ok());
    }

    #[test]
    fn namespace_separates_platform_and_instance() {
        let ns = SourceNamespace::new("bilibili", None).unwrap();
        assert_eq!(ns.platform(), "bilibili");
        let ns2 = SourceNamespace::new("douyin", Some("www")).unwrap();
        assert_eq!(ns2.platform(), "douyin");
        assert!(SourceNamespace::new("", None).is_err());
    }

    #[test]
    fn id_parse_rejects_empty_and_overlong() {
        assert_eq!(EntryId::parse(""), Err(IdError::Empty));
        assert_eq!(EntryId::parse("x".repeat(300)), Err(IdError::TooLong));
        assert!(EntryId::parse("ok").is_ok());
    }
}
