//! One-source resolution. Temporary media plans stay between adapter and Core;
//! the UI receives only the safe offer/selection projection.
use crate::plugin::{LiveSnapshot, ObservedDetail, ResolveMediaResult};
use serde::{Deserialize, Serialize};
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ResolvedSource {
    pub kind: String,
    pub canonical_url: String,
    pub native_account_key: String,
    pub display_name: Option<String>,
    #[serde(default)]
    pub source_duration_seconds: Option<f64>,
    pub detail: Option<ObservedDetail>,
    pub media: ResolveMediaResult,
    pub live: Option<LiveSnapshot>,
}
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SourcePreviewArgs {
    pub url: String,
    #[serde(default)]
    pub credential_id: Option<String>,
}
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SourceConfirmArgs {
    pub preview_id: String,
    pub selected: Vec<String>,
}
