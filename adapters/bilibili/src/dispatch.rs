//! 插件方法分发：方法名 → 能力模块。

use crate::AdapterState;
use crate::account::{probe_auth, resolve_account};
use crate::detail::observe;
use crate::list::{PAGE_SIZE, enumerate};
use crate::media::resolve_media;
use serde_json::{Value, json};
use std::time::{Duration, Instant};
use trace_contracts::plugin::{
    AdapterHello, AdapterLimits, EnumerateParams, MediaCandidate, ObserveParams, ObservedDetail,
    ResolveAccountParams, ResolveMediaParams, ResolveMediaResult, methods,
};
use trace_domain::PLUGIN_PROTOCOL_VERSION;
use trace_domain::errors::FailureClass;
use trace_site_support::kit::memo::Recent;

/// What an article, opus or dynamic offered a moment ago (its soundtrack evidence and media
/// candidates). Each picture is its own media group; without this every one of them would
/// read the same content again.
static RECENT_CONTENT: Recent<(Value, Vec<MediaCandidate>)> =
    Recent::new(Duration::from_secs(120), 8);

fn remember(p: &ObserveParams, detail: &ObservedDetail, candidates: &[MediaCandidate]) {
    RECENT_CONTENT.put(
        &format!("{}/{}", p.native_kind, p.native_entry_key),
        (
            detail.source_version_evidence["silent_or_muxed"].clone(),
            candidates.to_vec(),
        ),
    );
}

pub(crate) fn dispatch(
    state: &AdapterState,
    req: &trace_contracts::plugin::PluginRequest,
    deadline: Duration,
    started: Instant,
) -> Result<Value, trace_contracts::plugin::PluginError> {
    match req.method.as_str() {
        "resolve_source" => crate::source::resolve(state, &req.params, deadline, started)
            .map(|v| serde_json::to_value(v).unwrap()),
        "sample_live_auxiliary" => crate::live::auxiliary(state, &req.params, deadline, started)
            .map(|v| serde_json::to_value(v).unwrap()),
        "probe_live" => crate::live::probe(state, &req.params, deadline, started)
            .map(|v| serde_json::to_value(v).unwrap()),
        methods::HELLO => Ok(serde_json::to_value(hello()).unwrap_or_default()),
        methods::RESOLVE_ACCOUNT => {
            let p: ResolveAccountParams =
                serde_json::from_value(req.params.clone()).map_err(invalid_payload)?;
            resolve_account(state, &p, deadline, started)
                .map(|v| serde_json::to_value(v).unwrap_or_default())
        }
        methods::ENUMERATE => {
            let p: EnumerateParams =
                serde_json::from_value(req.params.clone()).map_err(invalid_payload)?;
            if p.content_type != "video" {
                return crate::content::enumerate(state, &p, deadline, started)
                    .map(|v| serde_json::to_value(v).unwrap());
            }
            enumerate(state, &p, deadline, started)
                .map(|v| serde_json::to_value(v).unwrap_or_default())
        }
        methods::OBSERVE => {
            let p: ObserveParams =
                serde_json::from_value(req.params.clone()).map_err(invalid_payload)?;
            if p.native_kind != "video" {
                let (detail, candidates) = crate::content::observe(state, &p, deadline, started)?;
                remember(&p, &detail, &candidates);
                return Ok(serde_json::to_value(detail).unwrap());
            }
            observe(state, &p, deadline, started)
                .map(|v| serde_json::to_value(v).unwrap_or_default())
        }
        methods::RESOLVE_MEDIA => {
            let p: ResolveMediaParams =
                serde_json::from_value(req.params.clone()).map_err(invalid_payload)?;
            if p.native_kind != "video" {
                let key = format!("{}/{}", p.native_kind, p.native_entry_key);
                let (silent_or_muxed, candidates) = match RECENT_CONTENT.get(&key) {
                    Some(remembered) => remembered,
                    None => {
                        let observe = ObserveParams {
                            native_entry_key: p.native_entry_key.clone(),
                            native_kind: p.native_kind.clone(),
                        };
                        let (detail, candidates) =
                            crate::content::observe(state, &observe, deadline, started)?;
                        remember(&observe, &detail, &candidates);
                        (
                            detail.source_version_evidence["silent_or_muxed"].clone(),
                            candidates,
                        )
                    }
                };
                return Ok(serde_json::to_value(ResolveMediaResult {
                    candidates: candidates
                        .into_iter()
                        .filter(|c| {
                            p.resources
                                .iter()
                                .any(|r| r.source_attachment_id == c.resource.source_attachment_id)
                        })
                        .collect(),
                    notes: json!({"silent_or_muxed": silent_or_muxed}),
                })
                .unwrap());
            }
            resolve_media(state, &p, deadline, started)
                .map(|v| serde_json::to_value(v).unwrap_or_default())
        }
        methods::PROBE_AUTH => probe_auth(state, deadline, started)
            .map(|v| serde_json::to_value(v).unwrap_or_default()),
        methods::CANCEL | methods::SHUTDOWN => Ok(json!({"ack": true})),
        other => Err(trace_contracts::plugin::PluginError {
            class: FailureClass::Unsupported,
            message: format!("unsupported method {other}"),
            retry_after_ms: None,
            details: Value::Null,
        }),
    }
}

fn invalid_payload(e: serde_json::Error) -> trace_contracts::plugin::PluginError {
    trace_contracts::plugin::PluginError {
        class: FailureClass::InvalidPayload,
        message: format!("bad params: {e}"),
        retry_after_ms: None,
        details: Value::Null,
    }
}

fn hello() -> AdapterHello {
    AdapterHello {
        protocol_version: PLUGIN_PROTOCOL_VERSION.into(),
        adapter_version: env!("CARGO_PKG_VERSION").into(),
        platform: "bilibili".into(),
        capabilities: vec![
            methods::RESOLVE_ACCOUNT.into(),
            methods::ENUMERATE.into(),
            methods::OBSERVE.into(),
            methods::RESOLVE_MEDIA.into(),
            methods::PROBE_AUTH.into(),
            "probe_live".into(),
            "sample_live_auxiliary".into(),
            "resolve_source".into(),
            "plugin_chunks_v1".into(),
        ],
        limits: AdapterLimits {
            max_page_items: PAGE_SIZE,
            supports_cancel: false,
        },
        rules: crate::rules::rules(),
    }
}
