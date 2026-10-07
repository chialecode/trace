//! 内容版本判定（INV-02、C02）。
//!
//! 语义摘要 = 规范化(标题+正文+资源身份列表) 的 SHA-256。统计字段
//! （播放量、点赞）与短期 URL 不参与摘要；完整详情不被短摘要覆盖。

use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

/// 参与语义摘要的资源身份：角色 + 来源附件 ID + 顺序。
#[derive(Debug, Clone, PartialEq, Eq, PartialOrd, Ord, Serialize, Deserialize)]
pub struct ResourceIdentity {
    pub role: String,
    pub source_attachment_id: String,
    pub position: i64,
}

/// 观察到的内容载荷（列表摘要或详情）。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, Default)]
pub struct ContentObservation {
    pub title: String,
    pub normalized_body: String,
    /// 观察完整性：summary = 仅列表字段；full = 声明正文与媒体清单完整。
    #[serde(rename = "completeness")]
    pub completeness: Completeness,
    pub resources: Vec<ResourceIdentity>,
    /// 来源版本证据（有则用来源，无则观察顺序）。
    pub source_version_evidence: Option<String>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, Default)]
#[serde(rename_all = "snake_case")]
pub enum Completeness {
    #[default]
    Summary,
    Full,
}

/// 新观察与既有版本的合并动作。
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum RevisionDecision {
    /// 复用已有版本（语义相同）。
    Reuse { revision_id: String },
    /// 生成新版本；旧版本保持不变。
    CreateNew,
    /// 短摘要不覆盖已存在的完整版本（detail 完整性不足且语义不同时）。
    KeepExistingIgnoreSummary { reason: &'static str },
}

/// 计算语义摘要。规范化输入必须已由调用方完成（core 统一规范化）。
pub fn semantic_digest(
    title: &str,
    normalized_body: &str,
    resources: &[ResourceIdentity],
) -> String {
    let mut hasher = Sha256::new();
    hasher.update(b"trace-semantic-v1\x00");
    hasher.update(title.len().to_le_bytes());
    hasher.update(title.as_bytes());
    hasher.update(b"\x00");
    hasher.update(normalized_body.len().to_le_bytes());
    hasher.update(normalized_body.as_bytes());
    hasher.update(b"\x00");
    let mut sorted: Vec<&ResourceIdentity> = resources.iter().collect();
    sorted.sort();
    for r in sorted {
        hasher.update(b"R");
        hasher.update(r.role.as_bytes());
        hasher.update(b"\x1f");
        hasher.update(r.source_attachment_id.as_bytes());
        hasher.update(b"\x1f");
        hasher.update(r.position.to_le_bytes());
        hasher.update(b"\x1e");
    }
    hex(&hasher.finalize())
}

fn hex(bytes: &[u8]) -> String {
    let mut s = String::with_capacity(bytes.len() * 2);
    for b in bytes {
        s.push_str(&format!("{:02x}", b));
    }
    s
}

/// 对既有版本集合应用新观察。
///
/// 规则（C02/AC-04 子集）：
/// - 语义相同 → 复用（正文编辑回退 A→B→A 会命中既有版本）；
/// - 语义不同 → 新版本追加；旧版本绝不原地覆盖；
/// - 新观察是短摘要且已有完整版本 → 即使语义不同也不覆盖完整详情；
///   若摘要语义与完整版本不同，登记为新观察但保留既有版本为“当前”。
pub fn decide_revision(
    existing: &[(String, Completeness, String)],
    incoming: &ContentObservation,
) -> RevisionDecision {
    let digest = semantic_digest(
        &incoming.title,
        &incoming.normalized_body,
        &incoming.resources,
    );
    if let Some((id, _, _)) = existing.iter().find(|(_, _, d)| *d == digest) {
        return RevisionDecision::Reuse {
            revision_id: id.clone(),
        };
    }
    let has_full = existing.iter().any(|(_, c, _)| *c == Completeness::Full);
    if has_full && incoming.completeness == Completeness::Summary {
        return RevisionDecision::KeepExistingIgnoreSummary {
            reason: "full revision already exists; summary cannot overwrite it",
        };
    }
    RevisionDecision::CreateNew
}

#[cfg(test)]
mod tests {
    use super::*;

    fn obs(
        title: &str,
        body: &str,
        full: bool,
        extra: Option<ResourceIdentity>,
    ) -> ContentObservation {
        let mut resources = vec![ResourceIdentity {
            role: "main_video".into(),
            source_attachment_id: "cid-1".into(),
            position: 1,
        }];
        if let Some(e) = extra {
            resources.push(e);
        }
        ContentObservation {
            title: title.into(),
            normalized_body: body.into(),
            completeness: if full {
                Completeness::Full
            } else {
                Completeness::Summary
            },
            resources,
            source_version_evidence: None,
        }
    }

    #[test]
    fn digest_excludes_stats_and_is_stable() {
        let a = semantic_digest(
            "t",
            "b",
            &[ResourceIdentity {
                role: "main_video".into(),
                source_attachment_id: "cid-1".into(),
                position: 1,
            }],
        );
        let b = semantic_digest(
            "t",
            "b",
            &[ResourceIdentity {
                role: "main_video".into(),
                source_attachment_id: "cid-1".into(),
                position: 1,
            }],
        );
        assert_eq!(a, b);
        assert!(a.len() == 64);
    }

    #[test]
    fn stats_change_does_not_create_revision() {
        // 统计不进入观察载荷，本测试锁定：相同语义两次观察 → Reuse。
        let existing = vec![(
            "rev-1".to_string(),
            Completeness::Full,
            semantic_digest(
                "t",
                "b",
                &[ResourceIdentity {
                    role: "main_video".into(),
                    source_attachment_id: "cid-1".into(),
                    position: 1,
                }],
            ),
        )];
        let incoming = obs("t", "b", true, None);
        assert_eq!(
            decide_revision(&existing, &incoming),
            RevisionDecision::Reuse {
                revision_id: "rev-1".into()
            }
        );
    }

    #[test]
    fn body_edit_creates_new_revision_and_revert_reuses() {
        let base = &[ResourceIdentity {
            role: "main_video".into(),
            source_attachment_id: "cid-1".into(),
            position: 1,
        }];
        let d_a = semantic_digest("t", "A", base);
        let d_b = semantic_digest("t", "B", base);
        let revisions = vec![
            ("rev-a".to_string(), Completeness::Full, d_a.clone()),
            ("rev-b".to_string(), Completeness::Full, d_b.clone()),
        ];
        // 回退到 A → 复用 rev-a，不创建第三份。
        let revert = obs("t", "A", true, None);
        assert_eq!(
            decide_revision(&revisions, &revert),
            RevisionDecision::Reuse {
                revision_id: "rev-a".into()
            }
        );
        // 新语义 → 新版本。
        let fresh = obs("t", "C", true, None);
        assert_eq!(
            decide_revision(&revisions, &fresh),
            RevisionDecision::CreateNew
        );
    }

    #[test]
    fn summary_never_overwrites_full_revision() {
        let base = &[ResourceIdentity {
            role: "main_video".into(),
            source_attachment_id: "cid-1".into(),
            position: 1,
        }];
        let full = vec![(
            "rev-full".to_string(),
            Completeness::Full,
            semantic_digest("长标题", "完整正文", base),
        )];
        let summary = ContentObservation {
            title: "短摘要".into(),
            normalized_body: "".into(),
            completeness: Completeness::Summary,
            resources: vec![ResourceIdentity {
                role: "main_video".into(),
                source_attachment_id: "cid-1".into(),
                position: 1,
            }],
            source_version_evidence: None,
        };
        assert!(matches!(
            decide_revision(&full, &summary),
            RevisionDecision::KeepExistingIgnoreSummary { .. }
        ));
    }

    #[test]
    fn resource_change_creates_revision() {
        let base = &[ResourceIdentity {
            role: "main_video".into(),
            source_attachment_id: "cid-1".into(),
            position: 1,
        }];
        let with_p2 = semantic_digest(
            "t",
            "b",
            &[
                ResourceIdentity {
                    role: "main_video".into(),
                    source_attachment_id: "cid-1".into(),
                    position: 1,
                },
                ResourceIdentity {
                    role: "main_video".into(),
                    source_attachment_id: "cid-2".into(),
                    position: 2,
                },
            ],
        );
        let d1 = semantic_digest("t", "b", base);
        assert_ne!(d1, with_p2);
    }
}
