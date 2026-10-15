//! Douyin web API: signed GET through the host proxy and response classification.
use serde_json::{Value, json};
use trace_contracts::plugin::PluginError;
use trace_domain::errors::FailureClass;
use trace_site_support::{error, runtime::Client};

pub(crate) const BASE: &str = "https://www.douyin.com";
pub(crate) const UA: &str = trace_site_support::kit::SIGNED_UA;

pub(crate) fn fail(message: &str) -> PluginError {
    error(FailureClass::SchemaChanged, message)
}

/// Headers the media CDN expects; they carry no authentication data.
pub(crate) fn public_headers() -> Value {
    json!({"referer":"https://www.douyin.com/","user-agent":UA})
}

pub(crate) fn api(
    client: &Client,
    path: &str,
    params: &[(&str, &str)],
) -> Result<Value, PluginError> {
    let mut url = url::Url::parse(&format!("{BASE}{path}")).unwrap();
    url.query_pairs_mut()
        .append_pair("aid", "6383")
        .append_pair("device_platform", "webapp")
        .extend_pairs(params.iter().copied());
    let time_ms = (time::OffsetDateTime::now_utc().unix_timestamp_nanos() / 1_000_000) as i64;
    let signature = crate::signature::sign_at(url.query().unwrap_or(""), UA, time_ms);
    url.query_pairs_mut().append_pair("a_bogus", &signature);
    envelope(client.json_large(url.as_str(), public_headers(), true)?)
}

pub fn envelope(v: Value) -> Result<Value, PluginError> {
    match v["status_code"].as_i64() {
        Some(0) if v.get("verify_data").is_none() => Ok(v),
        Some(8 | 9 | 2190008) => Err(error(
            FailureClass::AuthRequired,
            "Douyin session requires validation",
        )),
        Some(4 | 10000) => Err(error(
            FailureClass::PermissionDenied,
            "Douyin access denied",
        )),
        Some(_) => Err(error(FailureClass::Unknown, "Douyin business failure")),
        None => Err(fail("Douyin response missing status code")),
    }
}
