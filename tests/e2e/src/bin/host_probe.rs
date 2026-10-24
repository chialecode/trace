//! Bounded process/credential failure fixture; never included in a product package.
use serde_json::{Value, json};
use trace_contracts::plugin::{PluginError, PluginRequest};
use trace_site_support::runtime::Client;
fn dispatch(c: &Client, r: &PluginRequest) -> Result<Value, PluginError> {
    match r.method.as_str() {
        "hello" => Ok(
            json!({"protocol_version":"0.1","adapter_version":"fixture","platform":r.params["platform"],"capabilities":["probe"],"limits":{"max_page_items":1,"supports_cancel":false}}),
        ),
        "probe" => c.json(r.params["url"].as_str().unwrap(), json!({}), true),
        "hold" => {
            std::thread::sleep(std::time::Duration::from_millis(500));
            Ok(json!({"ready":true}))
        }
        _ => Ok(json!({"pid":std::process::id()})),
    }
}
fn main() {
    trace_site_support::runtime::serve(dispatch);
}
