//! Download folders: where readable copies of archived content are written, and what TRACE
//! records about them (docs/design/multi-site-contracts.md §7.1–§7.3).

use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct DownloadFolderView {
    pub location_id: String,
    pub path: String,
    pub is_default: bool,
    pub last_used_at: Option<String>,
    /// Free space on the volume of this folder, when it can be read.
    #[ts(type = "number | null")]
    pub free_bytes: Option<i64>,
    /// Whether the folder exists now.
    pub exists: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct DownloadSettingsView {
    /// The folder used when a download does not choose one.
    pub default_dir: String,
    /// Folders used before, most recent first (the default folder included when it was used).
    pub recent: Vec<DownloadFolderView>,
    /// Whether a same-name `.json` with the cleaned fields is written beside each download.
    pub sidecar: bool,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize, ts_rs::TS)]
#[serde(deny_unknown_fields)]
#[ts(export)]
pub struct DownloadSettingsArgs {
    /// Absolute path of the new default folder.
    #[serde(default)]
    pub default_dir: Option<String>,
    #[serde(default)]
    pub sidecar: Option<bool>,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[serde(deny_unknown_fields)]
#[ts(export)]
pub struct ExportArgs {
    pub entry_ids: Vec<String>,
    /// Folder for this run; the default folder when absent.
    #[serde(default)]
    pub dir: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct ExportedFile {
    /// Path below the download folder.
    pub rel_path: String,
    /// `media`, `text` or `sidecar`.
    pub role: String,
    #[ts(type = "number")]
    pub size: i64,
    /// `written` now, `present` already there and intact, `changed` edited outside TRACE and
    /// left alone.
    pub state: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct ExportItem {
    pub entry_id: String,
    /// `exported` (something was written), `unchanged` (everything was already there),
    /// `nothing` (the entry has no saved file yet) or `failed`.
    pub state: String,
    pub files: Vec<ExportedFile>,
    /// Why it failed or has nothing, in plain Chinese.
    pub message: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct ExportReport {
    pub folder: DownloadFolderView,
    pub items: Vec<ExportItem>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize, ts_rs::TS)]
#[serde(deny_unknown_fields)]
#[ts(export)]
pub struct CheckArgs {
    #[serde(default)]
    pub location_id: Option<String>,
    #[serde(default)]
    pub entry_id: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct CopyView {
    pub copy_id: String,
    pub entry_id: Option<String>,
    pub location_id: String,
    pub rel_path: String,
    /// `media`, `text` or `sidecar`.
    pub role: String,
    #[ts(type = "number")]
    pub size: i64,
    /// `present`, `changed` (edited outside TRACE), `missing` (no longer there) or `writing`.
    pub state: String,
    pub checked_at: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct CheckReport {
    #[ts(type = "number")]
    pub checked: i64,
    #[ts(type = "number")]
    pub present: i64,
    #[ts(type = "number")]
    pub changed: i64,
    #[ts(type = "number")]
    pub missing: i64,
    /// The copies that are changed or missing now.
    pub problems: Vec<CopyView>,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[serde(deny_unknown_fields)]
#[ts(export)]
pub struct CopiesArgs {
    pub entry_id: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[serde(deny_unknown_fields)]
#[ts(export)]
pub struct SyncArgs {
    pub entry_ids: Vec<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct SyncItem {
    pub entry_id: String,
    /// `already_in_library` (the library holds the same bytes, nothing copied), `restored`
    /// (the library lacked or had a damaged object; it was refilled from the download folder)
    /// or `skipped`.
    pub state: String,
    #[ts(type = "number")]
    pub files: i64,
    pub message: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct SyncReport {
    pub items: Vec<SyncItem>,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[serde(deny_unknown_fields)]
#[ts(export)]
pub struct ForgetArgs {
    pub entry_id: String,
    /// Only the records of this folder; all folders when absent. Files are never touched.
    #[serde(default)]
    pub location_id: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct ForgetReport {
    #[ts(type = "number")]
    pub removed: i64,
}
