//! Data access for the control commands.
//!
//! Every SQL statement behind `ipc/` lives here, grouped by aggregate. Functions take the
//! caller's connection or transaction and never open their own, so the transaction
//! boundary stays with the use case in `app/`.

pub mod accounts;
pub mod archive;
pub mod creators;
pub mod credentials;
pub mod deliveries;
pub mod downloads;
pub mod entries;
pub mod explorer;
pub mod identity;
pub mod imports;
pub mod jobs;
pub mod library;
pub mod live;
pub mod management;
pub mod media;
pub mod media_sync;
pub mod meta;
pub mod network;
pub mod outbox;
pub mod plugin_releases;
pub mod plugins;
pub mod previews;
pub mod quality;
pub mod receipts;
pub mod replays;
pub mod resources;
pub mod rules;
pub mod scan;
pub mod subscriptions;
pub mod work;

/// A JSON column that may be NULL, empty or malformed reads as `null`, never as an error.
pub(crate) fn json_column(raw: rusqlite::Result<String>) -> serde_json::Value {
    raw.ok()
        .and_then(|text| serde_json::from_str(&text).ok())
        .unwrap_or_default()
}
