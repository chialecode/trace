//! Shapes the management pages read (stage plan §11). Core assembles most of these as JSON;
//! the exported types keep the desktop wrapper and the Rust fields in step.
use serde::{Deserialize, Serialize};
use serde_json::Value;

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct ManagementOperationDto {
    pub operation_id: String,
    pub kind: String,
    pub source_creator_id: Option<String>,
    pub target_creator_id: Option<String>,
    pub account_ids: Vec<String>,
    #[ts(type = "unknown")]
    pub counts: Value,
    pub created_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct IdentityCandidateDto {
    pub candidate_id: String,
    pub account_id: String,
    pub proposed_creator_id: String,
    pub kind: String,
    pub score: f64,
    #[ts(type = "unknown")]
    pub evidence: Value,
    pub state: String,
    pub created_at: String,
    pub decided_at: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct ProfileObservationDto {
    pub sequence: i64,
    pub display_name: Option<String>,
    pub observed_at: String,
    #[ts(type = "unknown")]
    pub evidence: Value,
    #[ts(type = "unknown")]
    pub profile: Value,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct ImportBatchDto {
    pub batch_id: String,
    pub mode: String,
    pub source_path: String,
    pub state: String,
    pub target_creator_id: Option<String>,
    pub total_items: i64,
    pub done_items: i64,
    pub failed_items: i64,
    pub skipped_items: i64,
    pub total_bytes: i64,
    pub done_bytes: i64,
    pub error: Option<String>,
    pub created_at: String,
    pub updated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct ImportItemDto {
    pub item_id: String,
    pub position: i64,
    pub source_path: String,
    pub rel_path: String,
    pub size: i64,
    pub mtime: Option<String>,
    pub sha256: Option<String>,
    pub entry_id: Option<String>,
    pub state: String,
    pub error: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct PluginReleaseDto {
    pub plugin_id: String,
    pub version: String,
    pub digest: String,
    pub protocol: String,
    pub state: String,
    pub installed_at: String,
    pub capabilities: Vec<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct CapabilityMethodDto {
    pub name: String,
    pub params: Vec<String>,
    pub results: Vec<String>,
    #[ts(type = "number")]
    pub timeout_ms: u64,
    #[ts(type = "number")]
    pub max_bytes: usize,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct CapabilityContractDto {
    pub kind: String,
    pub version: String,
    #[ts(type = "number")]
    pub max_bytes: usize,
    pub methods: Vec<CapabilityMethodDto>,
}
