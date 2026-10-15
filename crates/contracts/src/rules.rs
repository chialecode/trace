//! Rule declarations: what each site adapter says it can subscribe to, preview or
//! download (docs/design/multi-site-contracts.md §3).
//!
//! An adapter declares its rules in the handshake; Core validates and persists them, and
//! the supported-sites page and link recognition read only that persisted catalog. A rule
//! is `implemented` only with a real verification record, so a declaration without one is
//! stored as `partial` (see [`RuleDeclaration::normalized`]).

use serde::{Deserialize, Serialize};
use trace_domain::sites::{self, LoginNeed, NetworkAdvice};

/// What a rule is for.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, ts_rs::TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub enum RuleEntry {
    /// Follow an account's published content.
    Subscription,
    /// List an account's content so the user can pick what to download.
    Preview,
    /// Download one work from its link.
    Link,
}

/// What a link pattern identifies.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, ts_rs::TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub enum RuleObject {
    Account,
    Entry,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, ts_rs::TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub enum RuleStatus {
    Implemented,
    Partial,
    Unimplemented,
    Retired,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, ts_rs::TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub enum RuleLogin {
    None,
    Optional,
    Required,
}

impl From<LoginNeed> for RuleLogin {
    fn from(value: LoginNeed) -> Self {
        match value {
            LoginNeed::None => Self::None,
            LoginNeed::Optional => Self::Optional,
            LoginNeed::Required => Self::Required,
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, ts_rs::TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub enum RuleNetwork {
    Direct,
    ProxyRecommended,
}

impl From<NetworkAdvice> for RuleNetwork {
    fn from(value: NetworkAdvice) -> Self {
        match value {
            NetworkAdvice::Direct => Self::Direct,
            NetworkAdvice::ProxyRecommended => Self::ProxyRecommended,
        }
    }
}

/// A link form the rule recognizes. `path` segments are literal, `*` (one segment),
/// `{name}` (one segment, captured), `@{name}` (one segment that starts with the literal
/// before the brace, the rest captured) or a final `**` (anything that follows). Matching
/// only proposes a rule; the adapter's own resolution confirms the identity.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct UrlPattern {
    /// A domain; subdomains match too.
    pub host: String,
    pub path: String,
    pub object: RuleObject,
    /// The link is a short form that the adapter must expand (within the site's domains,
    /// at most five hops) before it can be matched to an account or an entry.
    #[serde(default)]
    pub expand: bool,
}

/// A user-selectable option of a rule, with its default.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct RuleParam {
    pub key: String,
    pub title: String,
    /// `bool`, `enum` or `number`.
    pub kind: String,
    pub default: String,
    #[serde(default)]
    pub options: Vec<String>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct RuleDeclaration {
    /// `<site>.<object>.<channel>`; never renamed once published.
    pub id: String,
    pub site: String,
    pub title: String,
    pub description: String,
    pub entry: RuleEntry,
    /// `text`, `image`, `live_photo`, `video`, `audio`, `article`, `live`.
    pub content_kinds: Vec<String>,
    pub url_patterns: Vec<UrlPattern>,
    #[serde(default)]
    pub params: Vec<RuleParam>,
    pub login: RuleLogin,
    #[serde(default)]
    pub login_note: Option<String>,
    pub network: RuleNetwork,
    /// Known limits, in plain Chinese. Required for `partial` and `unimplemented`.
    #[serde(default)]
    pub limits: Vec<String>,
    pub status: RuleStatus,
    /// Date of the last real verification (`YYYY-MM-DD`).
    #[serde(default)]
    pub verified_at: Option<String>,
    #[serde(default)]
    pub verified_scope: Option<String>,
    /// The subscription lane (`content_type`) a `subscription` rule maps to.
    #[serde(default)]
    pub lane: Option<String>,
}

/// Why a declaration cannot be catalogued.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum RuleError {
    #[error("rule id `{0}` is not <site>.<object>.<channel> of the declaring site")]
    BadId(String),
    #[error("rule `{0}` names an unknown site")]
    UnknownSite(String),
    #[error("rule `{rule}` pattern host `{host}` is outside the site's domains")]
    ForeignHost { rule: String, host: String },
    #[error("subscription rule `{0}` needs a lane")]
    NoLane(String),
}

const KNOWN_KINDS: [&str; 7] = [
    "text",
    "image",
    "live_photo",
    "video",
    "audio",
    "article",
    "live",
];

impl RuleDeclaration {
    /// Checks identity and domains against the registry and applies the status rules:
    /// `implemented` needs a verification date and scope (otherwise it is stored as
    /// `partial` with the reason), and `partial`/`unimplemented` always carry a limit.
    pub fn normalized(mut self, declaring_site: &str) -> Result<Self, RuleError> {
        let site =
            sites::site(&self.site).ok_or_else(|| RuleError::UnknownSite(self.id.clone()))?;
        let well_formed = self.site == declaring_site
            && self.id.split('.').count() == 3
            && self.id.split('.').all(|part| {
                !part.is_empty()
                    && part
                        .bytes()
                        .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'_')
            })
            && self.id.starts_with(&format!("{}.", self.site));
        if !well_formed {
            return Err(RuleError::BadId(self.id));
        }
        for pattern in &self.url_patterns {
            if !site.owns_host(&pattern.host) {
                return Err(RuleError::ForeignHost {
                    rule: self.id.clone(),
                    host: pattern.host.clone(),
                });
            }
        }
        if self.entry == RuleEntry::Subscription && self.lane.as_deref().is_none_or(str::is_empty) {
            return Err(RuleError::NoLane(self.id));
        }
        self.content_kinds
            .retain(|k| KNOWN_KINDS.contains(&k.as_str()));
        self.limits.retain(|l| !l.trim().is_empty());
        let verified = self.verified_at.as_deref().is_some_and(|d| !d.is_empty())
            && self
                .verified_scope
                .as_deref()
                .is_some_and(|d| !d.is_empty());
        if self.status == RuleStatus::Implemented && !verified {
            self.status = RuleStatus::Partial;
            self.limits.push("声明为已实现但没有真实验证记录".into());
        }
        if !verified {
            self.verified_at = None;
            self.verified_scope = None;
        }
        if matches!(self.status, RuleStatus::Partial | RuleStatus::Unimplemented)
            && self.limits.is_empty()
        {
            self.limits.push("适配器没有说明限制".into());
        }
        Ok(self)
    }

    /// Whether the rule can be used right now (not retired or unimplemented).
    pub fn usable(&self) -> bool {
        matches!(self.status, RuleStatus::Implemented | RuleStatus::Partial)
    }
}

/// What a link matched.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct PatternMatch {
    pub object: RuleObject,
    /// `{name}` captures, in order.
    pub captures: Vec<(String, String)>,
    /// Literal path segments in the pattern. When several patterns fit one link (`/u/{uid}`
    /// and `/{uid}/{mid}` both fit `/u/123`), the one with more literal segments is the
    /// intended reading.
    pub specificity: u32,
}

/// `{name}` or `prefix{name}`: the literal prefix and the capture's name.
fn capture(segment: &str) -> Option<(&str, &str)> {
    let open = segment.find('{')?;
    let name = segment[open..].strip_prefix('{')?.strip_suffix('}')?;
    (!name.is_empty()).then_some((&segment[..open], name))
}

impl UrlPattern {
    /// Matches an `https` link's host and path. Query and fragment are ignored.
    pub fn matches(&self, host: &str, path: &str) -> Option<PatternMatch> {
        if !sites::host_in(host, &[self.host.as_str()]) {
            return None;
        }
        let want: Vec<&str> = self.path.split('/').filter(|s| !s.is_empty()).collect();
        let have: Vec<&str> = path.split('/').filter(|s| !s.is_empty()).collect();
        let mut captures = Vec::new();
        // A prefixed capture (`@{name}`) names its kind of page by the prefix, as a literal does.
        let specificity = want
            .iter()
            .filter(|s| {
                **s != "*" && **s != "**" && capture(s).is_none_or(|(prefix, _)| !prefix.is_empty())
            })
            .count() as u32;
        for (index, segment) in want.iter().enumerate() {
            if *segment == "**" {
                return (index + 1 == want.len()).then_some(PatternMatch {
                    object: self.object,
                    captures,
                    specificity,
                });
            }
            let actual = have.get(index)?;
            if let Some((prefix, name)) = capture(segment) {
                let head = actual.get(..prefix.len())?;
                let rest = &actual[prefix.len()..];
                if !head.eq_ignore_ascii_case(prefix) || rest.is_empty() {
                    return None;
                }
                captures.push((name.to_owned(), rest.to_owned()));
            } else if *segment != "*" && !segment.eq_ignore_ascii_case(actual) {
                return None;
            }
        }
        (want.len() == have.len()).then_some(PatternMatch {
            object: self.object,
            captures,
            specificity,
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn rule(status: RuleStatus) -> RuleDeclaration {
        RuleDeclaration {
            id: "weibo.user.posts".into(),
            site: "weibo".into(),
            title: "用户微博".into(),
            description: "原创与转发".into(),
            entry: RuleEntry::Subscription,
            content_kinds: vec!["text".into(), "image".into(), "bogus".into()],
            url_patterns: vec![UrlPattern {
                host: "weibo.com".into(),
                path: "/u/{uid}".into(),
                object: RuleObject::Account,
                expand: false,
            }],
            params: vec![],
            login: RuleLogin::Optional,
            login_note: None,
            network: RuleNetwork::Direct,
            limits: vec![],
            status,
            verified_at: None,
            verified_scope: None,
            lane: Some("post".into()),
        }
    }

    #[test]
    fn unverified_implemented_rules_are_stored_as_partial_with_a_reason() {
        let normalized = rule(RuleStatus::Implemented).normalized("weibo").unwrap();
        assert_eq!(normalized.status, RuleStatus::Partial);
        assert!(normalized.limits.iter().any(|l| l.contains("没有真实验证")));
        assert_eq!(normalized.content_kinds, ["text", "image"]);
    }

    #[test]
    fn verified_implemented_rules_keep_their_status_and_evidence() {
        let mut verified = rule(RuleStatus::Implemented);
        verified.verified_at = Some("2026-10-16".into());
        verified.verified_scope = Some("登录态 2 页列表".into());
        let normalized = verified.normalized("weibo").unwrap();
        assert_eq!(normalized.status, RuleStatus::Implemented);
        assert_eq!(normalized.verified_at.as_deref(), Some("2026-10-16"));
        assert!(normalized.limits.is_empty());
    }

    #[test]
    fn partial_and_unimplemented_always_say_what_is_missing() {
        for status in [RuleStatus::Partial, RuleStatus::Unimplemented] {
            let normalized = rule(status).normalized("weibo").unwrap();
            assert!(!normalized.limits.is_empty());
        }
    }

    #[test]
    fn foreign_hosts_ids_and_missing_lanes_are_rejected() {
        let mut foreign = rule(RuleStatus::Partial);
        foreign.url_patterns[0].host = "example.com".into();
        assert!(matches!(
            foreign.normalized("weibo"),
            Err(RuleError::ForeignHost { .. })
        ));
        assert!(matches!(
            rule(RuleStatus::Partial).normalized("x"),
            Err(RuleError::BadId(_))
        ));
        let mut renamed = rule(RuleStatus::Partial);
        renamed.id = "weibo.user".into();
        assert!(matches!(
            renamed.normalized("weibo"),
            Err(RuleError::BadId(_))
        ));
        let mut no_lane = rule(RuleStatus::Partial);
        no_lane.lane = None;
        assert!(matches!(
            no_lane.normalized("weibo"),
            Err(RuleError::NoLane(_))
        ));
        let mut unknown = rule(RuleStatus::Partial);
        unknown.site = "nowhere".into();
        assert!(matches!(
            unknown.normalized("nowhere"),
            Err(RuleError::UnknownSite(_))
        ));
    }

    #[test]
    fn patterns_capture_segments_and_respect_boundaries() {
        let account = UrlPattern {
            host: "weibo.com".into(),
            path: "/u/{uid}".into(),
            object: RuleObject::Account,
            expand: false,
        };
        let hit = account.matches("www.weibo.com", "/u/123").unwrap();
        assert_eq!(hit.captures, [("uid".to_owned(), "123".to_owned())]);
        assert!(account.matches("weibo.com", "/u/123/extra").is_none());
        assert!(account.matches("weibo.com", "/u").is_none());
        assert!(account.matches("evilweibo.com", "/u/123").is_none());
        let rest = UrlPattern {
            host: "x.com".into(),
            path: "/{name}/status/**".into(),
            object: RuleObject::Entry,
            expand: false,
        };
        assert!(rest.matches("x.com", "/a/status/1/photo/1").is_some());
        assert!(rest.matches("x.com", "/a/likes/1").is_none());
    }

    #[test]
    fn a_prefixed_capture_matches_handles_and_counts_as_specific() {
        let handle = UrlPattern {
            host: "youtube.com".into(),
            path: "/@{handle}".into(),
            object: RuleObject::Account,
            expand: false,
        };
        let hit = handle.matches("www.youtube.com", "/@SomeName").unwrap();
        assert_eq!(hit.captures, [("handle".to_owned(), "SomeName".to_owned())]);
        assert_eq!(hit.specificity, 1);
        assert!(handle.matches("youtube.com", "/@").is_none());
        assert!(handle.matches("youtube.com", "/watch").is_none());
        assert!(handle.matches("youtube.com", "/@a/videos").is_none());
        let plain = UrlPattern {
            path: "/{name}".into(),
            ..handle
        };
        assert_eq!(plain.matches("youtube.com", "/@a").unwrap().specificity, 0);
    }
}
