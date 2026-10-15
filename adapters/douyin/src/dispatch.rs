//! Plugin method dispatch: method name → capability module.
use crate::api::{api, fail};
use crate::detail::{all_candidates, detail_for};
use serde_json::{Value, json};
use trace_contracts::plugin::*;
use trace_domain::errors::FailureClass;
use trace_site_support::{error, runtime::Client};

pub fn dispatch(client: &Client, req: &PluginRequest) -> Result<Value, PluginError> {
    let p = &req.params;
    match req.method.as_str() {
        "resolve_source" => {
            crate::source::resolve(client, p).map(|v| serde_json::to_value(v).unwrap())
        }
        "sample_live_auxiliary" => {
            crate::live::auxiliary(client, p).map(|v| serde_json::to_value(v).unwrap())
        }
        "probe_live" => crate::live::probe(client, p).map(|v| serde_json::to_value(v).unwrap()),
        "hello" => Ok(hello()),
        "resolve_account" => crate::account::resolve(client, p),
        "enumerate" => {
            let p: EnumerateParams = serde_json::from_value(p.clone())
                .map_err(|_| fail("invalid enumeration parameters"))?;
            serde_json::to_value(crate::enumeration::enumerate(client, &p)?)
                .map_err(|_| fail("page serialization"))
        }
        "observe" => observe(client, p),
        "resolve_media" => resolve_media(client, p),
        "probe_auth" => crate::account::probe_auth(client),
        _ => Err(error(
            FailureClass::Unsupported,
            "unsupported Douyin method",
        )),
    }
}

fn hello() -> Value {
    json!({
        "protocol_version": trace_domain::PLUGIN_PROTOCOL_VERSION,
        "adapter_version": env!("CARGO_PKG_VERSION"),
        "platform": "douyin",
        "capabilities": ["resolve_account", "enumerate", "observe", "resolve_media", "probe_auth", "probe_live", "sample_live_auxiliary", "resolve_source", "plugin_chunks_v1"],
        "limits": {"max_page_items": 20, "supports_cancel": false},
        "rules": crate::rules::rules(),
    })
}

/// Detail response of one aweme for `observe` and `resolve_media`.
fn aweme_detail(client: &Client, p: &Value) -> Result<(Value, String), PluginError> {
    let key = p["native_entry_key"]
        .as_str()
        .ok_or_else(|| fail("missing entry id"))?;
    if key.is_empty() || !key.bytes().all(|b| b.is_ascii_digit()) {
        return Err(fail("invalid aweme id"));
    }
    let v = api(client, "/aweme/v1/web/aweme/detail/", &[("aweme_id", key)])?;
    Ok((v, key.to_owned()))
}

fn observe(client: &Client, p: &Value) -> Result<Value, PluginError> {
    let (v, key) = aweme_detail(client, p)?;
    let (detail, _) = detail_for(
        &v["aweme_detail"],
        &key,
        p["quality_hint"]["prefer_fps"] == true,
    )?;
    Ok(serde_json::to_value(detail).unwrap())
}

fn resolve_media(client: &Client, p: &Value) -> Result<Value, PluginError> {
    let (v, key) = aweme_detail(client, p)?;
    let (_, mut plan) = detail_for(
        &v["aweme_detail"],
        &key,
        p["quality_hint"]["prefer_fps"] == true,
    )?;
    if p["quality_hint"]["all_variants"] == true {
        plan = all_candidates(&v["aweme_detail"], &key)?;
    }
    let p: ResolveMediaParams =
        serde_json::from_value(p.clone()).map_err(|_| fail("invalid media parameters"))?;
    Ok(serde_json::to_value(ResolveMediaResult {
        candidates: plan
            .into_iter()
            .filter(|c| {
                p.resources
                    .iter()
                    .any(|r| r.source_attachment_id == c.resource.source_attachment_id)
            })
            .collect(),
        notes: json!({}),
    })
    .unwrap())
}
