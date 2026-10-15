//! Safe historical authentication facts. Opaque references never contain vault
//! paths, cookie hashes, headers or source access URLs.
use serde::{Deserialize, Serialize};
use serde_json::Value;

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, ts_rs::TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
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

/// The authentication of everything an entry holds: one state, or `mixed` with each state.
#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct AuthenticationSummary {
    pub state: AuthenticationSummaryState,
    pub states: Vec<AuthenticationState>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, ts_rs::TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub enum AuthenticationSummaryState {
    Anonymous,
    Authenticated,
    Expired,
    Unknown,
    Mixed,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[serde(deny_unknown_fields)]
#[ts(export)]
pub struct UpgradeDecision {
    pub candidate_id: String,
    pub decision: UpgradeAction,
    /// Returned by the comparison query; binds old selections, revisions and
    /// all relevant generations. Each item of a batch carries its own snapshot.
    #[ts(type = "unknown")]
    pub expected: Value,
}
#[derive(Debug, Clone, Copy, Serialize, Deserialize, ts_rs::TS)]
#[serde(rename_all = "snake_case")]
#[ts(export)]
pub enum UpgradeAction {
    Accept,
    Reject,
    KeepBoth,
}
