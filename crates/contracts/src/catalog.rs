//! The persisted rule catalog as the supported-sites page and link recognition see it.
//! Both are answered from Core's database; neither makes a network request.

use crate::rules::{RuleDeclaration, RuleEntry, RuleLogin, RuleNetwork, RuleObject, RuleStatus};
use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct RuleView {
    pub declaration: RuleDeclaration,
    /// False once the adapter stopped declaring the rule (it is shown as retired).
    pub declared: bool,
    pub declared_at: String,
    pub adapter_version: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct SiteView {
    pub id: String,
    pub title: String,
    pub login: RuleLogin,
    pub network: RuleNetwork,
    /// `enabled`, `disabled`; a plugin that was never switched is enabled.
    pub plugin_state: String,
    pub rules: Vec<RuleView>,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct SiteCatalog {
    pub sites: Vec<SiteView>,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[serde(deny_unknown_fields)]
#[ts(export)]
pub struct RecognizeArgs {
    pub url: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct RecognizedRule {
    pub rule_id: String,
    pub title: String,
    pub entry: RuleEntry,
    pub object: RuleObject,
    pub status: RuleStatus,
    pub login: RuleLogin,
    pub network: RuleNetwork,
    pub lane: Option<String>,
    pub captures: Vec<(String, String)>,
}

/// What a pasted link is, before anything is requested.
#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct Recognition {
    /// Host and path only; the query and fragment are never echoed.
    pub shown: String,
    pub site: Option<String>,
    pub site_title: Option<String>,
    pub object: Option<RuleObject>,
    pub rules: Vec<RecognizedRule>,
    /// A short link: the adapter expands it before the rules above can be chosen.
    pub needs_expansion: bool,
    /// Why nothing matched, in plain Chinese.
    pub message: Option<String>,
}
