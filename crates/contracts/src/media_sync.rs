//! Fetching videos a subscription left undownloaded, and re-observing entries without
//! touching the subscription's scan position (docs/design/multi-site-contracts.md §5).

use serde::{Deserialize, Serialize};

/// Entries per request, for both commands.
pub const MAX_ENTRIES: usize = 200;

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[serde(deny_unknown_fields)]
#[ts(export)]
pub struct MediaSyncArgs {
    pub entry_ids: Vec<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct MediaSyncItem {
    pub entry_id: String,
    /// `queued` (something was queued or retried now), `already_queued` (the work is already
    /// waiting or running — selecting again changes nothing), `nothing` (no undownloaded
    /// video) or `failed`.
    pub state: String,
    /// Media groups queued or retried by this request.
    #[ts(type = "number")]
    pub groups: i64,
    pub message: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct MediaSyncReport {
    pub items: Vec<MediaSyncItem>,
}

/// Which entries to observe again: exactly one of a list, a time range, or the failed ones.
#[derive(Debug, Clone, Default, Serialize, Deserialize, ts_rs::TS)]
#[serde(default, deny_unknown_fields)]
#[ts(export)]
pub struct RecheckArgs {
    pub entry_ids: Vec<String>,
    /// Publication time bounds (`YYYY-MM-DD` or RFC 3339), either or both.
    pub from: Option<String>,
    pub to: Option<String>,
    /// Limits a range or the failed ones to one source account.
    pub account_id: Option<String>,
    /// Retry what failed (observations and media) instead of observing again.
    pub failed: bool,
    /// At most this many entries (default 100, at most 200).
    pub limit: Option<u32>,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct RecheckItem {
    pub entry_id: String,
    /// `queued`, `retried` or `skipped`.
    pub state: String,
    pub message: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct RecheckReport {
    #[ts(type = "number")]
    pub queued: i64,
    #[ts(type = "number")]
    pub skipped: i64,
    /// More entries matched than the limit allowed; ask again with a narrower range.
    pub truncated: bool,
    pub items: Vec<RecheckItem>,
}
