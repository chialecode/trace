//! TikTok's web client: pages that carry their state as a JSON script, and the list call.
use serde_json::{Value, json};
use trace_contracts::plugin::PluginError;
use trace_site_support::kit::{self, net, page};
use trace_site_support::runtime::Transport;

pub(crate) const ORIGIN: &str = "https://www.tiktok.com";
pub(crate) const UA: &str = kit::DESKTOP_UA;
const STATE_ID: &str = "__UNIVERSAL_DATA_FOR_REHYDRATION__";

fn headers() -> Value {
    json!({
        "user-agent": UA,
        "accept-language": "en-US,en;q=0.9",
        "referer": format!("{ORIGIN}/"),
        "accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
    })
}

/// What the media hosts expect; no authentication data.
pub(crate) fn media_headers() -> Value {
    json!({"referer": format!("{ORIGIN}/"), "user-agent": UA})
}

/// A page of the site, as the visitor's session sees it.
pub(crate) fn html(net: &dyn Transport, path: &str) -> Result<String, PluginError> {
    net::text(net, &format!("{ORIGIN}{path}"), headers(), true)
}

/// A page that a challenge replaces carries a verification script instead of the state.
fn challenged(text: &str) -> bool {
    ["captcha", "verify-ele", "tiktok-verify-page"]
        .iter()
        .any(|m| text.contains(m))
}

/// The default scope of the embedded state: one entry per page component.
pub(crate) fn scope(text: &str) -> Result<Value, PluginError> {
    match page::script_json(text, STATE_ID).map(|mut s| s["__DEFAULT_SCOPE__"].take()) {
        Some(scope) if scope.is_object() => Ok(scope),
        _ if challenged(text) => Err(kit::limited("TikTok asks the visitor to verify")),
        _ => Err(kit::schema("page has no embedded state")),
    }
}

/// The list call. The site answers it with items only when the request carries its
/// signature; without, the answer is empty or not JSON at all. Either is "unknown".
pub(crate) fn item_list(
    net: &dyn Transport,
    sec_uid: &str,
    cursor: &str,
) -> Result<Value, PluginError> {
    let url = net::with_query(
        &format!("{ORIGIN}/api/post/item_list/"),
        &[
            ("aid", "1988"),
            ("app_language", "en"),
            ("app_name", "tiktok_web"),
            ("channel", "tiktok_web"),
            ("count", "30"),
            ("cursor", cursor),
            ("device_platform", "web_pc"),
            ("secUid", sec_uid),
        ],
    );
    let body = net::text(net, &url, headers(), true)?;
    if body.trim().is_empty() {
        return Err(kit::limited(
            "TikTok answered the list request without data",
        ));
    }
    serde_json::from_str(&body).map_err(|_| kit::schema("list answer is not JSON"))
}
