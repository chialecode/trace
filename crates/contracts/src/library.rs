//! Library location: where the content-addressed object store lives, and the resumable
//! move to another folder (docs/design/multi-site-contracts.md §7.4).

use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[serde(deny_unknown_fields)]
#[ts(export)]
pub struct MigrateArgs {
    /// Absolute path of the new library folder (empty or not yet existing).
    pub to_path: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[serde(deny_unknown_fields)]
#[ts(export)]
pub struct ReleaseArgs {
    pub location_id: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct LocationView {
    pub location_id: String,
    pub path: String,
    /// `active` (in use), `old_kept` (replaced, waiting for the user to release it) or `released`.
    pub state: String,
    pub created_at: String,
    /// Free space on the volume of this folder, when it can be read.
    #[ts(type = "number | null")]
    pub free_bytes: Option<i64>,
    /// Whether the folder exists now.
    pub exists: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct MigrationView {
    pub migration_id: String,
    pub from_path: String,
    pub to_path: String,
    /// `copying`, `verifying`, `ready` (verified, waiting for the switch), `switched`,
    /// `failed`, `cancelled` or `released`.
    pub state: String,
    #[ts(type = "number")]
    pub total_items: i64,
    #[ts(type = "number")]
    pub done_items: i64,
    #[ts(type = "number")]
    pub total_bytes: i64,
    #[ts(type = "number")]
    pub done_bytes: i64,
    /// Why the move stopped or is waiting, in plain Chinese.
    pub message: Option<String>,
    pub created_at: String,
    pub updated_at: String,
    pub switched_at: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct LibraryStatus {
    pub active: LocationView,
    /// Replaced libraries that still hold a full copy of the content.
    pub old_kept: Vec<LocationView>,
    /// The open move, or the latest finished one.
    pub migration: Option<MigrationView>,
}
