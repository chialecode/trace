//! Browsing an author's profile page by page, choosing what to download across pages, and
//! following the download requests that choice creates
//! (docs/design/multi-site-contracts.md §6).

use serde::{Deserialize, Serialize};

/// Pages one preview may load; a longer history is reached by subscribing.
pub const MAX_PAGES: u32 = 10;
/// Entries one preview may list.
pub const MAX_ITEMS: usize = 400;
/// Entries one download request may take.
pub const MAX_SELECTED: usize = 200;

/// Where a download ends up. The library is always written first; a folder additionally gets
/// readable copies (docs/design/multi-site-contracts.md §7.1).
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ts_rs::TS)]
#[serde(deny_unknown_fields)]
#[ts(export)]
pub struct DownloadTarget {
    /// `library` or `folder`.
    pub kind: String,
    /// The folder for this request when `kind` is `folder`; the default folder when absent.
    #[serde(default)]
    pub dir: Option<String>,
}

impl DownloadTarget {
    pub fn library() -> Self {
        Self {
            kind: "library".into(),
            dir: None,
        }
    }
}

/// The first page (`url`) or the next one (`preview_id`) of a profile.
#[derive(Debug, Clone, Default, Serialize, Deserialize, ts_rs::TS)]
#[serde(default, deny_unknown_fields)]
#[ts(export)]
pub struct ProfilePreviewArgs {
    pub url: Option<String>,
    pub preview_id: Option<String>,
    /// Which preview rule to use when the link fits more than one.
    pub rule_id: Option<String>,
    pub credential_id: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct ProfileItem {
    pub native_key: String,
    pub native_kind: String,
    pub title: String,
    /// The start of the text, when the list gave one.
    pub summary: Option<String>,
    pub published_at: Option<String>,
    pub published_precision: String,
    pub pinned: bool,
    pub content_kinds: Vec<String>,
    pub image_count: u32,
    pub video_count: u32,
    #[ts(type = "number | null")]
    pub duration_ms: Option<i64>,
    /// TRACE already holds this entry.
    pub in_library: bool,
    pub entry_id: Option<String>,
    /// How much of the entry is saved when it is in the library.
    pub save_progress: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct ProfileRuleChoice {
    pub rule_id: String,
    pub title: String,
}

/// One page of a profile. `items` is only the page just loaded; earlier pages stay with the
/// preview and their selection stays with the caller.
#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct ProfilePage {
    pub preview_id: String,
    pub platform: String,
    pub site_title: String,
    pub rule_id: String,
    pub rule_title: String,
    /// Other preview rules this link fits.
    pub alternatives: Vec<ProfileRuleChoice>,
    pub account_key: String,
    pub account_name: Option<String>,
    pub authentication: String,
    pub items: Vec<ProfileItem>,
    pub pages_loaded: u32,
    pub page_limit: u32,
    pub total_items: u32,
    /// More can be loaded with the same preview.
    pub has_more: bool,
    /// Loading stopped at the page or entry limit although the profile has more.
    pub limited: bool,
    pub expires_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[serde(deny_unknown_fields)]
#[ts(export)]
pub struct ProfileDownloadArgs {
    pub preview_id: String,
    /// Entries chosen across any number of loaded pages.
    pub native_keys: Vec<String>,
    pub target: DownloadTarget,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct ProfileDownloadReport {
    pub delivery_id: String,
    /// Entries that will be fetched.
    pub accepted: u32,
    /// Entries TRACE already holds completely; only their copies are written.
    pub already_in_library: u32,
    /// Entries left out, each with the reason.
    pub skipped: Vec<SkippedEntry>,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct SkippedEntry {
    pub native_key: String,
    pub reason: String,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize, ts_rs::TS)]
#[serde(default, deny_unknown_fields)]
#[ts(export)]
pub struct DeliveryListArgs {
    pub delivery_id: Option<String>,
    pub limit: Option<u32>,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct DeliveryItemView {
    pub native_key: String,
    pub native_kind: String,
    pub title: String,
    pub entry_id: Option<String>,
    /// `queued` (still being fetched), `ready` (in the library), `exported` (copies written),
    /// `partial`, `failed` or `cancelled`.
    pub state: String,
    pub message: Option<String>,
    #[ts(type = "number")]
    pub files: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct DeliveryView {
    pub delivery_id: String,
    /// `link` or `profile`.
    pub kind: String,
    pub platform: String,
    /// Host and path only.
    pub source: String,
    pub target: DownloadTarget,
    /// `running`, `finishing`, `done` or `cancelled`.
    pub state: String,
    pub created_at: String,
    pub finished_at: Option<String>,
    pub items: Vec<DeliveryItemView>,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct DeliveryList {
    pub items: Vec<DeliveryView>,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[serde(deny_unknown_fields)]
#[ts(export)]
pub struct DeliveryCancelArgs {
    pub delivery_id: String,
}
