//! Weibo's web JSON interface: the requests, and the envelope every answer comes in.
use serde_json::{Value, json};
use trace_contracts::plugin::PluginError;
use trace_domain::errors::FailureClass;
use trace_site_support::kit::{self, net, schema};
use trace_site_support::{error, runtime::Transport};

pub(crate) const BASE: &str = "https://weibo.com";
pub(crate) const UA: &str = kit::DESKTOP_UA;

/// Headers of the site's own web client. The anti-forgery header is added by Core.
pub(crate) fn headers() -> Value {
    json!({
        "referer": "https://weibo.com/",
        "user-agent": UA,
        "accept": "application/json, text/plain, */*",
        "x-requested-with": "XMLHttpRequest",
    })
}

/// Headers of a page the browser opens (the home page).
pub(crate) fn page_headers() -> Value {
    json!({
        "referer": "https://weibo.com/",
        "user-agent": UA,
        "accept": "text/html,application/xhtml+xml",
    })
}

/// What the media hosts expect; no authentication data.
pub(crate) fn media_headers() -> Value {
    json!({"referer": "https://weibo.com/", "user-agent": UA})
}

/// One `ajax` call, checked.
pub(crate) fn ajax(
    net: &dyn Transport,
    path: &str,
    query: &[(&str, &str)],
) -> Result<Value, PluginError> {
    let url = net::with_query(&format!("{BASE}{path}"), query);
    envelope(net::json(net, &url, headers(), true)?)
}

/// `ok` is 1 for an answer; -100 sends the browser to the login page; anything else is a
/// refusal whose reason is not content state.
pub(crate) fn envelope(v: Value) -> Result<Value, PluginError> {
    match v["ok"].as_i64() {
        Some(1) => Ok(v),
        Some(-100) => Err(kit::login_needed("Weibo asks for a login")),
        Some(_) => Err(error(
            FailureClass::SourceUnavailable,
            "Weibo declined the request",
        )),
        None => Err(schema("Weibo response has no ok flag")),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_envelope_tells_a_login_wall_from_a_refusal_from_a_changed_shape() {
        assert!(envelope(json!({"ok": 1, "data": {}})).is_ok());
        assert_eq!(
            envelope(json!({"ok": -100, "url": "https://passport.weibo.com/x"}))
                .unwrap_err()
                .class,
            FailureClass::AuthRequired
        );
        assert_eq!(
            envelope(json!({"ok": 0, "msg": "x"})).unwrap_err().class,
            FailureClass::SourceUnavailable
        );
        assert_eq!(
            envelope(json!({"data": {}})).unwrap_err().class,
            FailureClass::SchemaChanged
        );
    }
}
