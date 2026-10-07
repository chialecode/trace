//! Synthetic load source only. Never registered by the production host or package.
use serde_json::{Value, json};
use trace_contracts::plugin::{PluginError, PluginRequest};
use trace_site_support::runtime::Client;
fn dispatch(c: &Client, r: &PluginRequest) -> Result<Value, PluginError> {
    if r.method == "hello" {
        return Ok(
            json!({"protocol_version":"0.1","adapter_version":"m1-load","platform":r.params["platform"],"capabilities":["enumerate","observe","resolve_media","probe_live"],"limits":{"max_page_items":100,"supports_cancel":false}}),
        );
    }
    let base = std::env::var("TRACE_BENCH_SOURCE").unwrap_or_default();
    let account = r.params["native_account_key"]
        .as_str()
        .or_else(|| r.params["native_entry_key"].as_str())
        .unwrap_or("0");
    let v = c
        .json(&format!("{base}/state?account={account}"), json!({}), false)
        .inspect_err(|error| {
            if let Ok(root) = std::env::var("TRACE_BENCH_DIAGNOSTICS") {
                use std::io::Write;
                if let Ok(mut file) = std::fs::OpenOptions::new().create(true).append(true).open(
                    std::path::Path::new(&root).join(format!("proxy-{}.log", std::process::id())),
                ) {
                    // The dedicated load helper receives only local synthetic data.
                    let _ = writeln!(
                        file,
                        "method={} class={:?} reason={}",
                        r.method, error.class, error.message
                    );
                }
            }
        })?;
    match r.method.as_str() {
        "enumerate" => Ok(
            json!({"items":if v["download"]==true{json!([{"native_entry_key":account,"native_kind":"video","title":"合成下载","published_precision":"unknown","pinned":false,"stats":{}}])}else{json!([])},"next_cursor":null,"has_more":false,"coverage":{"synthetic":true}}),
        ),
        "observe" => Ok(
            json!({"native_entry_key":account,"native_kind":"video","title":"合成下载","normalized_body":"资源测量","published_precision":"unknown","access_state":"available","source_version_evidence":{},"completeness":"full","stats":{},"resources":[{"role":"video","source_attachment_id":"source","position":0,"required":true}]}),
        ),
        "resolve_media" => {
            let resource = trace_site_support::resource("source", "video", 0, true);
            let mut plan = trace_site_support::candidate(
                resource,
                vec![format!("{base}/download")],
                "progressive",
                json!({}),
            )
            .expect("one source");
            plan.expected_size = v["download_bytes"].as_i64();
            Ok(json!({"candidates":[plan],"notes":{}}))
        }
        "probe_live" => Ok(
            json!({"room_key":account,"source_session":"load-session","state":if v["live"]==true{"live"}else{"offline"},"title":"合成录制","metadata":{},"streams":if v["live"]==true{json!([{"protocol":"hls","url":format!("{base}/index.m3u8"),"quality":"8000000","audio_only":false,"public_headers":{}}])}else{json!([])}}),
        ),
        _ => Err(trace_site_support::error(
            trace_domain::errors::FailureClass::Unsupported,
            "synthetic method",
        )),
    }
}
fn main() {
    trace_site_support::runtime::serve(dispatch);
}
