//! Controlled task-lifecycle peer only. Never registered or bundled in TRACE.
//! Production site parsing is tested separately by m1_production_adapters.
use serde_json::{Value, json};
use trace_contracts::plugin::{PluginError, PluginRequest};
use trace_site_support::runtime::Client;
fn dispatch(client: &Client, request: &PluginRequest) -> Result<Value, PluginError> {
    if request.method == "hello" {
        return Ok(
            json!({"protocol_version":"0.1","adapter_version":"lifecycle-fixture","platform":request.params["platform"],"capabilities":["enumerate","observe","resolve_media","probe_live","resolve_account","sample_live_auxiliary","probe_auth"],"limits":{"max_page_items":20,"supports_cancel":false}}),
        );
    }
    let mut url = url::Url::parse("https://lifecycle.invalid/").unwrap();
    url.set_path(&request.method);
    url.query_pairs_mut()
        .append_pair("params", &request.params.to_string());
    client.json(url.as_str(), json!({}), false)
}
fn main() {
    trace_site_support::runtime::serve(dispatch);
}
