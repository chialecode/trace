#![cfg_attr(test, allow(clippy::too_many_lines))]
pub mod document;
pub mod kit;
pub mod runtime;

use serde_json::Value;
use sha2::{Digest, Sha256};
use trace_contracts::plugin::{MediaCandidate, PluginError, ResourceIdentityDto, UrlSet};
use trace_domain::errors::FailureClass;

pub fn error(class: FailureClass, message: &str) -> PluginError {
    PluginError {
        class,
        message: message.into(),
        retry_after_ms: None,
        details: Value::Null,
    }
}

pub fn id(v: &Value) -> Option<String> {
    v.as_str()
        .filter(|s| !s.is_empty())
        .map(str::to_owned)
        .or_else(|| v.as_u64().map(|n| n.to_string()))
}

pub fn resource(key: &str, role: &str, position: i64, required: bool) -> ResourceIdentityDto {
    ResourceIdentityDto {
        source_attachment_id: key.into(),
        role: role.into(),
        position,
        title: None,
        group_key: None,
        required,
    }
}

/// A URL is transient. Only its hash (without rotating query credentials) is a resource identity.
pub fn url_identity(raw: &str) -> Option<String> {
    let mut u = url::Url::parse(raw).ok()?;
    if u.scheme() != "https" || !u.username().is_empty() || u.password().is_some() {
        return None;
    }
    u.set_query(None);
    u.set_fragment(None);
    Some(
        Sha256::digest(u.as_str().as_bytes())
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect(),
    )
}

pub fn candidate(
    resource: ResourceIdentityDto,
    urls: Vec<String>,
    kind: &str,
    spec: Value,
) -> Option<MediaCandidate> {
    let mut urls = urls.into_iter();
    let primary = urls.next()?;
    Some(MediaCandidate {
        resource,
        representation: kind.into(),
        kind: kind.into(),
        segments: vec![UrlSet {
            primary,
            backups: urls.collect(),
            expected_size: None,
            expected_sha256: None,
        }],
        public_headers: Value::Null,
        expected_size: None,
        expected_sha256: None,
        claimed_spec: spec,
    })
}
