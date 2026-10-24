//! One-shot, explicitly invoked protocol check; no scheduler or archive writes.
use crate::{
    config::Profile,
    error::CoreError,
    network::{Mode, NetworkState, Setting, validate_proxy_url},
    pluginhost::{PluginHost, default_adapter_specs},
    request_budget::RequestBudget,
    transport::ConstrainedClient,
};
use serde::Deserialize;
use serde_json::{Value, json};
use std::{path::PathBuf, sync::Arc, time::Duration};
use trace_domain::sites::NetworkAdvice;

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Spec {
    pub platform: String,
    pub max_http_requests: usize,
    pub deadline_seconds: u64,
    pub min_interval_ms: u64,
    #[serde(default)]
    pub credential_file: Option<PathBuf>,
    pub requests: Vec<Step>,
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Step {
    pub method: String,
    pub params: Value,
}
impl Spec {
    pub fn validate(&self) -> Result<(), CoreError> {
        if trace_domain::sites::site(&self.platform).is_none()
            || self.requests.is_empty()
            || self.requests.len() > 20
        {
            return Err(CoreError::InvalidInput("invalid site check scope".into()));
        }
        RequestBudget::new(
            self.max_http_requests,
            self.deadline_seconds,
            self.min_interval_ms,
        )?;
        for step in &self.requests {
            if !matches!(
                step.method.as_str(),
                "probe_auth"
                    | "resolve_account"
                    | "enumerate"
                    | "observe"
                    | "resolve_media"
                    | "probe_live"
                    | "sample_live_auxiliary"
            ) || !step.params.is_object()
            {
                return Err(CoreError::InvalidInput(
                    "unsupported site check operation".into(),
                ));
            }
        }
        Ok(())
    }
}
const SQL_ROWS: &str =
    "one-shot protocol responses; no media download, archive write or subscription";

/// The route of a check: the overseas sites go through the proxy named by
/// `TRACE_TEST_PROXY` when it is set (the local test configuration), otherwise they follow
/// the system like the product does.
fn check_network() -> Arc<NetworkState> {
    let network = Arc::new(NetworkState::default());
    if let Ok(raw) = std::env::var("TRACE_TEST_PROXY")
        && let Ok(url) = validate_proxy_url(&raw)
    {
        for site in trace_domain::sites::SITES
            .iter()
            .filter(|s| s.network == NetworkAdvice::ProxyRecommended)
        {
            network.set(
                site.id,
                Setting {
                    mode: Mode::Manual,
                    proxy_url: Some(url.clone()),
                    auth_ref: None,
                },
                None,
            );
        }
    }
    network
}

pub async fn run(profile: Profile, spec: Spec) -> Result<Value, CoreError> {
    spec.validate()?;
    if !profile.test_allowed_hosts.is_empty() || !profile.extra_media_hosts.is_empty() {
        return Err(CoreError::InvalidInput(
            "site check requires production network rules".into(),
        ));
    }
    let secret = if let Some(path) = &spec.credential_file {
        let meta = std::fs::metadata(path)?;
        if !meta.is_file() || meta.len() > 16384 {
            return Err(CoreError::InvalidInput("credential input budget".into()));
        }
        let value = std::fs::read_to_string(path)?;
        let value = value.trim().to_owned();
        if value.is_empty() || value.contains(['\r', '\n']) {
            return Err(CoreError::InvalidInput(
                "credential must contain one Cookie header value".into(),
            ));
        }
        Some(value)
    } else {
        None
    };
    let credential_present = secret.is_some();
    let budget = Arc::new(RequestBudget::new(
        spec.max_http_requests,
        spec.deadline_seconds,
        spec.min_interval_ms,
    )?);
    let http = Arc::new(
        ConstrainedClient::build()?
            .with_network(check_network())
            .with_budget(budget.clone()),
    );
    let allowed_platform = spec.platform.clone();
    let host = PluginHost::new(
        profile.clone(),
        http,
        default_adapter_specs(&profile),
        Arc::new(move |platform| {
            if platform == allowed_platform {
                secret.clone()
            } else {
                None
            }
        }),
    );
    let mut results = Vec::new();
    let mut failure = None;
    let stopped = {
        let execution = async {
            for (index, step) in spec.requests.iter().enumerate() {
                if budget.snapshot()["stopped"] == true {
                    break;
                }
                match host.call_scoped(&spec.platform,None,&step.method,step.params.clone(),Some(30_000)).await {
                Ok(value)=>results.push(json!({"step":index,"method":step.method,"status":"passed","safe_result":trace_contracts::content::safe_metadata(&value)})),
                Err(error)=>{budget.stop();failure=Some(json!(error.failure_class()));results.push(json!({
                    "step": index,
                    "method": step.method,
                    "status": "failed",
                    "failure_class": error.failure_class(),
                }));break;}
            }
            }
        };
        tokio::pin!(execution);
        tokio::select! {
            _=&mut execution=>false,
            _=tokio::time::sleep(Duration::from_secs(spec.deadline_seconds))=>true,
            _=tokio::signal::ctrl_c()=>true,
        }
    };
    if stopped {
        budget.stop();
    }
    for (index, step) in spec.requests.iter().enumerate().skip(results.len()) {
        results.push(json!({"step":index,"method":step.method,"status":"not-run","reason":"earlier_failure_or_stop"}));
    }
    let budget_failed = budget.snapshot()["stopped"] == true;
    host.shutdown_all().await;
    Ok(json!({
        "platform": spec.platform,
        "status": if stopped || budget_failed || failure.is_some(){"failed"}else{"passed"},
        "credential_supplied": credential_present,
        "failure_class": failure,
        "interrupted_or_deadline": stopped,
        "budget": budget.snapshot(),
        "steps": results,
        "scope": SQL_ROWS,
        "productAcceptance": "not-run",
    }))
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn site_check_rejects_unbounded_and_write_operations_before_network() {
        for (method, maximum) in [("publish", 10), ("observe", 0), ("observe", 101)] {
            let spec = Spec {
                platform: "douyin".into(),
                max_http_requests: maximum,
                deadline_seconds: 60,
                min_interval_ms: 1000,
                credential_file: None,
                requests: vec![Step {
                    method: method.into(),
                    params: json!({}),
                }],
            };
            assert!(spec.validate().is_err());
        }
    }
}
