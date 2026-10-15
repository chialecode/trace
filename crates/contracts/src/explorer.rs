//! Resource explorer: a folder tree and a file list over the library and the download folders
//! (docs/design/multi-site-contracts.md §8.1).
//!
//! A node is an opaque address that Core produced; the interface hands it back unchanged.
//! Core parses it again on every call, so a made-up or tampered address is refused and never
//! reaches a path.

use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Default, Serialize, Deserialize, ts_rs::TS)]
#[serde(deny_unknown_fields)]
#[ts(export)]
pub struct TreeArgs {
    /// The node whose child folders are wanted; the two roots when absent.
    #[serde(default)]
    pub node: Option<String>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct ExplorerNode {
    pub node: String,
    /// What the folder is called, already in plain Chinese where Core names it.
    pub name: String,
    /// `library`, `downloads`, `site`, `account`, `year`, `month` or `folder`.
    pub kind: String,
    /// Entries below the folder, when counting them is cheap.
    #[ts(type = "number | null")]
    pub count: Option<i64>,
    /// Whether the folder may have child folders worth asking for.
    pub expandable: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct TreeView {
    pub nodes: Vec<ExplorerNode>,
    /// The folder holds more child folders than one answer carries.
    pub truncated: bool,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize, ts_rs::TS)]
#[serde(deny_unknown_fields)]
#[ts(export)]
pub struct ListArgs {
    pub node: String,
    /// `name`, `kind`, `size`, `duration`, `dimensions`, `published`, `site` or `author`.
    #[serde(default)]
    pub sort: Option<String>,
    #[serde(default)]
    pub descending: bool,
    /// Text the name contains, case-insensitive.
    #[serde(default)]
    pub query: Option<String>,
    /// `video`, `image`, `audio`, `text`, `article`, `live` or `other`.
    #[serde(default)]
    pub kind: Option<String>,
    /// Library: `complete`, `partial`, `pending`, `deferred`. Download folders: `present`,
    /// `changed`, `missing`, `untracked`.
    #[serde(default)]
    pub state: Option<String>,
    #[serde(default)]
    #[ts(type = "number | null")]
    pub offset: Option<i64>,
    /// 1 to 200; 100 when absent.
    #[serde(default)]
    #[ts(type = "number | null")]
    pub limit: Option<i64>,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct ExplorerRow {
    /// Stable within one answer; also the key for selections.
    pub key: String,
    pub name: String,
    /// `folder`, `video`, `image`, `audio`, `text`, `article`, `live` or `other`.
    pub kind: String,
    /// Everything the entry holds (`text`, `image`, `video`, …); only for entries.
    pub kinds: Vec<String>,
    #[ts(type = "number | null")]
    pub size: Option<i64>,
    #[ts(type = "number | null")]
    pub duration_ms: Option<i64>,
    #[ts(type = "number | null")]
    pub width: Option<i64>,
    #[ts(type = "number | null")]
    pub height: Option<i64>,
    pub published_at: Option<String>,
    pub site: Option<String>,
    pub author: Option<String>,
    /// Library entry: `complete`, `partial`, `pending`, `deferred`, `unknown`. Library file:
    /// `saved`, `deferred` or `missing`. Download folder: `present`, `changed`, `missing` or
    /// `untracked`.
    pub state: String,
    pub entry_id: Option<String>,
    /// A verified saved object, for previewing and opening a library file.
    pub blob_id: Option<String>,
    /// Download folder and the file's path below it.
    pub location_id: Option<String>,
    pub rel_path: Option<String>,
    /// Where the row is, for copying: the folder path for a download file, a readable
    /// library path otherwise.
    pub path: String,
    /// A folder or an entry that the list can step into.
    pub node: Option<String>,
    pub can_open: bool,
    pub can_reveal: bool,
    pub can_sync: bool,
    pub can_forget: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct ListView {
    /// Where the list is, for the address bar.
    pub path: String,
    pub rows: Vec<ExplorerRow>,
    #[ts(type = "number")]
    pub total: i64,
    #[ts(type = "number")]
    pub offset: i64,
    /// The folder holds more files than one listing reads; sorting and paging cover only
    /// what was read.
    pub truncated: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[serde(deny_unknown_fields)]
#[ts(export)]
pub struct FileArgs {
    pub location_id: String,
    pub rel_path: String,
}
