//! The `.json` written beside a download: cleaned unified fields and the source link, for
//! sorting and searching outside TRACE. No local path, credential or signed address goes in.
use crate::store::downloads::EntryFacts;
use crate::store::meta::Stored;
use serde_json::{Value, json};

pub struct SidecarFile {
    /// Name relative to the sidecar's folder, `/`-separated.
    pub name: String,
    pub role: String,
    pub size: i64,
    pub sha256: String,
}

/// Deterministic for a given catalog state, so an unchanged entry gives the same bytes and a
/// re-run writes nothing.
pub fn build(
    facts: &EntryFacts,
    stored: &Option<Stored>,
    files: &[SidecarFile],
    text: Option<&str>,
) -> Vec<u8> {
    let site_title = trace_domain::sites::site(&facts.site).map_or("", |s| s.title);
    let unified = stored.as_ref().map_or(Value::Null, |s| {
        serde_json::to_value(&s.unified).unwrap_or(Value::Null)
    });
    let url = stored
        .as_ref()
        .and_then(|s| s.unified.canonical_url.clone());
    let doc = json!({
        "trace_sidecar": 1,
        "site": facts.site,
        "site_title": site_title,
        "author": {
            "name": facts.author,
            "key": facts.author_key,
        },
        "entry": {
            "id": facts.native_key,
            "kind": facts.native_kind,
            "title": facts.title,
            "text": text.unwrap_or(facts.body.as_str()),
            "published_at": facts.published_at,
            "observed_at": facts.observed_at,
            "url": url,
        },
        "unified": unified,
        "images": stored.as_ref().map(|s| s.images),
        "videos": stored.as_ref().map(|s| s.videos),
        "files": files.iter().map(|f| json!({
            "name": f.name,
            "role": f.role,
            "size": f.size,
            "sha256": f.sha256,
        })).collect::<Vec<_>>(),
    });
    let mut bytes = serde_json::to_vec_pretty(&doc).unwrap_or_default();
    bytes.push(b'\n');
    bytes
}
