//! The unified metadata of an entry as the interface shows it: timeline cards, the detail
//! view and the filters read this one shape (docs/design/multi-site-contracts.md §4).

use serde::{Deserialize, Serialize};

/// Counters as last observed. A counter the site never reported is absent, not zero.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct EntryStats {
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
    /// When the counters were last seen to change.
    pub observed_at: Option<String>,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct EntryMetaDto {
    pub site: String,
    /// Public link of the work; absent when the site gave none.
    pub canonical_url: Option<String>,
    /// `original`, `repost` or `quote`.
    pub relation: String,
    pub related_key: Option<String>,
    pub edited_at: Option<String>,
    pub language: Option<String>,
    pub client: Option<String>,
    pub location_name: Option<String>,
    pub ip_region: Option<String>,
    /// `text`, `image`, `live_photo`, `video`, `audio`, `article`, `live`.
    pub content_kinds: Vec<String>,
    #[ts(type = "number")]
    pub image_count: i64,
    #[ts(type = "number")]
    pub video_count: i64,
    #[ts(type = "number | null")]
    pub duration_ms: Option<i64>,
    #[ts(type = "number | null")]
    pub width: Option<i64>,
    #[ts(type = "number | null")]
    pub height: Option<i64>,
    pub collection: Option<String>,
    pub category: Option<String>,
    pub visibility: Option<String>,
    pub pinned: bool,
    pub age_restricted: bool,
    pub sponsored: bool,
    pub tags: Vec<String>,
    pub mentions: Vec<String>,
    pub stats: EntryStats,
}

/// A tag and how many entries carry it, for the timeline's topic filter.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct TagCount {
    pub tag: String,
    #[ts(type = "number")]
    pub entries: i64,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize, ts_rs::TS)]
#[serde(deny_unknown_fields)]
#[ts(export)]
pub struct TagsArgs {
    #[serde(default)]
    pub creator_id: Option<String>,
    #[serde(default)]
    pub account_id: Option<String>,
    #[serde(default)]
    pub namespace: Option<String>,
    /// Only tags that start with this text.
    #[serde(default)]
    pub prefix: Option<String>,
    /// At most this many tags (most used first); 1 to 100.
    #[serde(default)]
    #[ts(type = "number | null")]
    pub limit: Option<i64>,
}
