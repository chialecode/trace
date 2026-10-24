//! Dynamic/opus/article mapping; all requests use the existing constrained host proxy.
mod feed;
mod map;
mod observe;

use crate::{AdapterState, api};
use serde_json::Value;
use std::time::{Duration, Instant};
use trace_contracts::plugin::PluginError;
use trace_domain::errors::FailureClass;

pub use feed::enumerate;
pub use observe::observe;

fn fail(msg: &str) -> PluginError {
    trace_site_support::error(FailureClass::SchemaChanged, msg)
}

fn get(
    state: &AdapterState,
    path: &str,
    params: &[(&str, &str)],
    deadline: Duration,
    started: Instant,
) -> Result<Value, PluginError> {
    let mut u = url::Url::parse(&format!("https://api.bilibili.com{path}")).unwrap();
    u.query_pairs_mut().extend_pairs(params.iter().copied());
    api::envelope(&api::get_json(state, u.as_str(), true, deadline, started)?).map(|e| e.data)
}

fn timestamp(v: &Value) -> Option<String> {
    time::OffsetDateTime::from_unix_timestamp(v.as_i64()?)
        .ok()?
        .format(&time::format_description::well_known::Rfc3339)
        .ok()
}

/// Opus detail: also the body source of a dynamic that carries an opus.
fn opus_detail(
    state: &AdapterState,
    key: &str,
    deadline: Duration,
    started: Instant,
) -> Result<Value, PluginError> {
    get(
        state,
        "/x/polymer/web-dynamic/v1/opus/detail",
        &[
            ("id", key),
            ("features", "htmlNewStyle,opusPrivateVisible,editable"),
        ],
        deadline,
        started,
    )
}
