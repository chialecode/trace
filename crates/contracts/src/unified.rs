//! Unified metadata: the site-independent fields an adapter maps its own data to
//! (docs/design/multi-site-contracts.md §4). Core stores them as indexed columns and
//! sub-tables for sorting, filtering and search; a field the site does not give stays empty.

use serde::{Deserialize, Serialize};
use serde_json::Value;
use trace_domain::sites;

/// How an entry relates to another entry of the same site.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default, Serialize, Deserialize, ts_rs::TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub enum Relation {
    #[default]
    Original,
    Repost,
    Quote,
}

impl Relation {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Original => "original",
            Self::Repost => "repost",
            Self::Quote => "quote",
        }
    }

    pub fn parse(value: &str) -> Self {
        match value {
            "repost" => Self::Repost,
            "quote" => Self::Quote,
            _ => Self::Original,
        }
    }
}

/// Counters observed at one time. A change here never creates a content revision.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct StatCounters {
    #[ts(type = "number | null")]
    pub plays: Option<i64>,
    #[ts(type = "number | null")]
    pub likes: Option<i64>,
    #[ts(type = "number | null")]
    pub comments: Option<i64>,
    #[ts(type = "number | null")]
    pub shares: Option<i64>,
    #[ts(type = "number | null")]
    pub favorites: Option<i64>,
    #[ts(type = "number | null")]
    pub coins: Option<i64>,
    #[ts(type = "number | null")]
    pub danmaku: Option<i64>,
}

impl StatCounters {
    pub fn is_empty(&self) -> bool {
        *self == Self::default()
    }

    /// Counters from a free-form statistics object, accepting the common key spellings.
    /// Negative or non-numeric values are dropped rather than guessed.
    pub fn from_value(stats: &Value) -> Self {
        let pick = |names: &[&str]| -> Option<i64> {
            names.iter().find_map(|name| {
                let value = stats.get(*name)?;
                let n = value
                    .as_i64()
                    .or_else(|| value.as_str().and_then(|s| s.parse().ok()))?;
                (n >= 0).then_some(n)
            })
        };
        Self {
            plays: pick(&["plays", "play", "view", "views", "play_count", "view_count"]),
            likes: pick(&[
                "likes",
                "like",
                "digg_count",
                "like_count",
                "favorite_count",
            ]),
            comments: pick(&["comments", "reply", "comment_count", "reply_count"]),
            shares: pick(&[
                "shares",
                "share",
                "share_count",
                "repost_count",
                "retweet_count",
            ]),
            favorites: pick(&["favorites", "collect_count", "bookmark_count", "favorite"]),
            coins: pick(&["coins", "coin"]),
            danmaku: pick(&["danmaku", "danmaku_count"]),
        }
    }
}

/// What an adapter reports for one entry. Every field is optional or empty by default.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct UnifiedMetadata {
    /// Public link of the work: `https`, in the site's domains, without query or fragment.
    #[serde(default)]
    pub canonical_url: Option<String>,
    #[serde(default)]
    pub relation: Relation,
    /// Native key of the entry that was reposted or quoted.
    #[serde(default)]
    pub related_key: Option<String>,
    #[serde(default)]
    pub edited_at: Option<String>,
    #[serde(default)]
    pub tags: Vec<String>,
    #[serde(default)]
    pub mentions: Vec<String>,
    #[serde(default)]
    pub language: Option<String>,
    #[serde(default)]
    pub client: Option<String>,
    #[serde(default)]
    pub location_name: Option<String>,
    #[serde(default)]
    pub ip_region: Option<String>,
    #[serde(default)]
    pub content_kinds: Vec<String>,
    #[serde(default)]
    #[ts(type = "number | null")]
    pub max_duration_ms: Option<i64>,
    #[serde(default)]
    #[ts(type = "number | null")]
    pub max_width: Option<i64>,
    #[serde(default)]
    #[ts(type = "number | null")]
    pub max_height: Option<i64>,
    /// Attachment id of the cover resource.
    #[serde(default)]
    pub cover: Option<String>,
    #[serde(default)]
    pub collection: Option<String>,
    #[serde(default)]
    pub category: Option<String>,
    #[serde(default)]
    pub pinned: bool,
    #[serde(default)]
    pub age_restricted: bool,
    #[serde(default)]
    pub sponsored: bool,
    #[serde(default)]
    pub visibility: Option<String>,
    #[serde(default)]
    pub stats: StatCounters,
}

const TEXT_MAX: usize = 200;
const TERM_MAX: usize = 100;
const TERMS_PER_KIND: usize = 64;

fn text(value: Option<String>, max: usize) -> Option<String> {
    let value = value?;
    let value = value.trim();
    if value.is_empty() || value.chars().any(char::is_control) {
        return None;
    }
    Some(value.chars().take(max).collect())
}

fn terms(values: Vec<String>) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    for value in values {
        let Some(term) = text(
            Some(value.trim_start_matches(['#', '@']).to_owned()),
            TERM_MAX,
        ) else {
            continue;
        };
        if !out.iter().any(|existing| existing == &term) {
            out.push(term);
        }
        if out.len() == TERMS_PER_KIND {
            break;
        }
    }
    out
}

fn positive(value: Option<i64>) -> Option<i64> {
    value.filter(|n| *n > 0)
}

/// A public work link: https, one of the site's domains, no credentials, query or fragment.
pub fn public_work_url(raw: &str, site: &str) -> Option<String> {
    let url = url::Url::parse(raw).ok()?;
    let host = url.host_str()?;
    let owned = sites::site(site)?.owns_host(host);
    (url.scheme() == "https"
        && owned
        && url.username().is_empty()
        && url.password().is_none()
        && url.query().is_none()
        && url.fragment().is_none()
        && raw.len() <= 512)
        .then(|| url.to_string())
}

impl UnifiedMetadata {
    /// Reduces an adapter's claims to what may be stored: bounded text, deduplicated terms,
    /// positive measures, known content kinds and a public work link of the site.
    pub fn sanitized(mut self, site: &str) -> Self {
        self.canonical_url = self.canonical_url.and_then(|u| public_work_url(&u, site));
        self.related_key = text(self.related_key, TEXT_MAX);
        if self.relation == Relation::Original {
            self.related_key = None;
        }
        self.edited_at = text(self.edited_at, 40);
        self.language = text(self.language, 16);
        self.client = text(self.client, TEXT_MAX);
        self.location_name = text(self.location_name, TEXT_MAX);
        self.ip_region = text(self.ip_region, TEXT_MAX);
        self.collection = text(self.collection, TEXT_MAX);
        self.category = text(self.category, TEXT_MAX);
        self.visibility = text(self.visibility, 32);
        self.cover = text(self.cover, TEXT_MAX);
        self.tags = terms(self.tags);
        self.mentions = terms(self.mentions);
        self.content_kinds.retain(|k| {
            [
                "text",
                "image",
                "live_photo",
                "video",
                "audio",
                "article",
                "live",
            ]
            .contains(&k.as_str())
        });
        self.content_kinds.sort();
        self.content_kinds.dedup();
        self.max_duration_ms = positive(self.max_duration_ms);
        self.max_width = positive(self.max_width);
        self.max_height = positive(self.max_height);
        self
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sanitizing_drops_foreign_links_secrets_and_duplicates() {
        let raw = UnifiedMetadata {
            canonical_url: Some("https://weibo.com/123/Abc?token=secret".into()),
            tags: vec!["#旅行".into(), "旅行".into(), " ".into(), "a\nb".into()],
            mentions: vec!["@张三".into()],
            relation: Relation::Original,
            related_key: Some("ignored".into()),
            max_width: Some(-5),
            content_kinds: vec![
                "video".into(),
                "video".into(),
                "bogus".into(),
                "text".into(),
            ],
            ..Default::default()
        };
        let clean = raw.sanitized("weibo");
        assert_eq!(
            clean.canonical_url, None,
            "a link with a query is not public"
        );
        assert_eq!(clean.tags, ["旅行"]);
        assert_eq!(clean.mentions, ["张三"]);
        assert_eq!(clean.related_key, None);
        assert_eq!(clean.max_width, None);
        assert_eq!(clean.content_kinds, ["text", "video"]);
    }

    #[test]
    fn work_links_must_be_https_and_inside_the_site() {
        assert_eq!(
            public_work_url("https://www.weibo.com/123/Abc", "weibo").as_deref(),
            Some("https://www.weibo.com/123/Abc")
        );
        assert!(public_work_url("http://weibo.com/1/a", "weibo").is_none());
        assert!(public_work_url("https://example.com/1/a", "weibo").is_none());
        assert!(public_work_url("https://weibo.com/1/a#frag", "weibo").is_none());
        assert!(public_work_url("https://user:pw@weibo.com/1/a", "weibo").is_none());
        assert!(public_work_url("https://weibo.com/1/a", "x").is_none());
    }

    #[test]
    fn counters_accept_site_spellings_and_drop_bad_values() {
        let stats = StatCounters::from_value(&serde_json::json!({
            "view": 10, "digg_count": "7", "reply": -1, "coin": 3, "danmaku": "x"
        }));
        assert_eq!(stats.plays, Some(10));
        assert_eq!(stats.likes, Some(7));
        assert_eq!(stats.comments, None);
        assert_eq!(stats.coins, Some(3));
        assert_eq!(stats.danmaku, None);
        assert!(StatCounters::from_value(&serde_json::json!({})).is_empty());
    }

    #[test]
    fn related_entry_is_kept_only_for_reposts_and_quotes() {
        let repost = UnifiedMetadata {
            relation: Relation::Repost,
            related_key: Some("999".into()),
            ..Default::default()
        }
        .sanitized("x");
        assert_eq!(repost.related_key.as_deref(), Some("999"));
    }
}
