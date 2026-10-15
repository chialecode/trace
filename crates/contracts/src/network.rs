//! Network settings: how each site's requests leave this machine
//! (docs/design/multi-site-contracts.md §9).

use crate::rules::RuleNetwork;
use serde::{Deserialize, Serialize};

/// Modes a scope can be set to, plus `default` which removes the saved choice.
pub const MODES: [&str; 4] = ["default", "direct", "system", "manual"];

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[serde(deny_unknown_fields)]
#[ts(export)]
pub struct NetworkSetArgs {
    /// `global` or a site id.
    pub scope: String,
    /// `default` (remove the saved choice), `direct`, `system` or `manual`.
    pub mode: String,
    /// `http://host:port` or `socks5://host:port`; manual mode only.
    #[serde(default)]
    pub proxy_url: Option<String>,
    /// Proxy login; stored in the system credential store, never echoed back.
    #[serde(default)]
    pub username: Option<String>,
    #[serde(default)]
    pub password: Option<String>,
    /// Keep the saved login when none is supplied (manual mode only).
    #[serde(default)]
    pub keep_login: bool,
}

/// A saved choice.
#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct NetworkScope {
    pub scope: String,
    pub mode: String,
    pub proxy_url: Option<String>,
    pub has_login: bool,
}

/// How one site connects now and why.
#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct NetworkSite {
    pub site: String,
    pub title: String,
    pub advice: RuleNetwork,
    /// `site`, `global` or `default`: which saved choice applies.
    pub source: String,
    pub mode: String,
    /// `direct` or `proxy`: what a request does right now.
    pub route: String,
    pub description: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct SystemProxyView {
    pub enabled: bool,
    /// The address "follow the system" would use; empty when the system has none.
    pub address: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, ts_rs::TS)]
#[ts(export)]
pub struct NetworkView {
    pub settings: Vec<NetworkScope>,
    pub sites: Vec<NetworkSite>,
    pub system: SystemProxyView,
}
