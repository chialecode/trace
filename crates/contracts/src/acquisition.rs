//! Safe historical authentication facts. Opaque references never contain vault
//! paths, cookie hashes, headers or source access URLs.
use serde::{Deserialize, Serialize};
use serde_json::Value;

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum AuthenticationState {
    Anonymous,
    Authenticated,
    Expired,
    Unknown,
}
impl AuthenticationState {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Anonymous => "anonymous",
            Self::Authenticated => "authenticated",
            Self::Expired => "expired",
            Self::Unknown => "unknown",
        }
    }
}
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AcquisitionContext {
    pub context_id: String,
    pub auth_state: AuthenticationState,
    pub credential_id: Option<String>,
    pub credential_generation: Option<i64>,
    pub validated_at: Option<String>,
    pub basis: String,
    pub started_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct UpgradeDecision {
    pub candidate_id: String,
    pub decision: UpgradeAction,
    /// Returned by the comparison query; binds old selections, revisions and
    /// all relevant generations. Each item of a batch carries its own snapshot.
    pub expected: Value,
}
#[derive(Debug, Clone, Copy, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum UpgradeAction {
    Accept,
    Reject,
    KeepBoth,
}
