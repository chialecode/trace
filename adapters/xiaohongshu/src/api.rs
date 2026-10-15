//! Xiaohongshu's web pages, which carry their data as an embedded `__INITIAL_STATE__`, and
//! the signed list interface the profile's "load more" asks for.
use crate::sign;
use serde_json::{Value, json};
use trace_contracts::plugin::PluginError;
use trace_site_support::error;
use trace_site_support::kit::{self, net, page};
use trace_site_support::runtime::{self, Fetch, Transport, body_bytes};

pub(crate) const ORIGIN: &str = "https://www.xiaohongshu.com";
pub(crate) const API_ORIGIN: &str = "https://edith.xiaohongshu.com";
pub(crate) const USER_POSTED: &str = "/api/sns/web/v1/user_posted";
pub(crate) const UA: &str = kit::DESKTOP_UA;

fn headers() -> Value {
    json!({
        "user-agent": UA,
        "accept-language": "zh-CN,zh;q=0.9",
        "referer": format!("{ORIGIN}/"),
        "accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
    })
}

/// What the media hosts expect; no authentication data.
pub(crate) fn media_headers() -> Value {
    json!({"referer": format!("{ORIGIN}/"), "user-agent": UA})
}

/// A page that a verification replaces carries the verification instead of the state.
fn challenged(text: &str) -> bool {
    ["captcha", "安全验证", "请通过验证"]
        .iter()
        .any(|m| text.contains(m))
}

/// The state a page embeds as `window.__INITIAL_STATE__={...}`.
pub(crate) fn state_in(text: &str) -> Result<Value, PluginError> {
    page::json_after(text, "window.__INITIAL_STATE__=")
        .or_else(|| page::json_after(text, "window.__INITIAL_STATE__ ="))
        .filter(Value::is_object)
        .ok_or_else(|| {
            if challenged(text) {
                kit::limited("Xiaohongshu asks the visitor to verify")
            } else {
                kit::schema(&format!("page has no embedded state ({})", shape(text)))
            }
        })
}

/// What a page that carries no readable state looks like, in words that hold nothing private:
/// its size, whether it names the state at all and the start of its title.
pub(crate) fn shape(text: &str) -> String {
    let title = text
        .split_once("<title>")
        .and_then(|(_, rest)| rest.split_once("</title>"))
        .map(|(title, _)| title.trim().chars().take(24).collect::<String>())
        .unwrap_or_default();
    format!(
        "{} bytes, state named {}, state {}, title {title:?}",
        text.len(),
        text.contains("__INITIAL_STATE__"),
        page::explain_json_after(text, "window.__INITIAL_STATE__=")
    )
}

/// A page of the site as the visitor's session sees it, with its state. A login or
/// verification page replaces the page that was asked for, and is told apart by its address.
pub(crate) fn state(net: &dyn Transport, path_and_query: &str) -> Result<Value, PluginError> {
    let (text, final_url) = net::page(net, &format!("{ORIGIN}{path_and_query}"), headers(), true)?;
    if let Ok(landed) = url::Url::parse(&final_url) {
        let path = landed.path();
        if path.contains("captcha") || path.contains("verify") {
            return Err(kit::limited("Xiaohongshu asks the visitor to verify"));
        }
        if path.contains("login") {
            return Err(kit::login_needed("Xiaohongshu asks for a login"));
        }
    }
    state_in(&text)
}

/// One page of the signed profile list. The device identifier comes from the host context;
/// the credential Cookie is attached by the host and is never visible here. A signature the
/// site refuses (406/461) stops the list instead of being retried with other parameters.
pub(crate) fn signed_notes(
    net: &dyn Transport,
    user_id: &str,
    cursor: &str,
    device_id: &str,
) -> Result<Value, PluginError> {
    let params = [
        ("num", "30"),
        ("cursor", cursor),
        ("user_id", user_id),
        ("image_formats", "jpg,webp,avif"),
    ];
    let signature = sign::headers(device_id, USER_POSTED, &params);
    let query = sign::query_string(&params);
    let url = format!("{API_ORIGIN}{USER_POSTED}?{query}");
    let headers = json!({
        "user-agent": UA,
        "accept": "application/json, text/plain, */*",
        "accept-language": "zh-CN,zh;q=0.9",
        "referer": format!("{ORIGIN}/"),
        "x-s": signature.x_s,
        "x-t": signature.x_t,
        "x-s-common": signature.x_s_common,
        "x-b3-traceid": signature.b3_trace_id,
        "x-xray-traceid": signature.xray_trace_id,
    });
    let reply = net.request(&Fetch::get(&url, headers, true).large())?;
    match reply.status {
        200 => {}
        406 | 461 => {
            return Err(kit::denied(
                "Xiaohongshu rejected the request signature; history is not complete",
            ));
        }
        401 | 403 => return Err(kit::login_needed("Xiaohongshu asks for a login")),
        other => {
            return Err(error(
                runtime::classify_http(other),
                "Xiaohongshu list request failed",
            ));
        }
    }
    let value: Value = serde_json::from_slice(&body_bytes(&reply)?)
        .map_err(|_| kit::schema("Xiaohongshu list answer is not JSON"))?;
    if value["success"] == false {
        let code = value["code"].as_i64().unwrap_or(0);
        return Err(match code {
            401 | 403 => kit::login_needed("Xiaohongshu asks for a login"),
            406 | 461 => {
                kit::denied("Xiaohongshu rejected the request signature; history is not complete")
            }
            429 => kit::limited("Xiaohongshu limits the list request"),
            _ => kit::schema("Xiaohongshu list answer is not a success"),
        });
    }
    Ok(value)
}

#[cfg(test)]
mod tests {
    use super::*;
    use trace_domain::errors::FailureClass;

    #[test]
    fn the_embedded_state_is_cut_out_and_a_verification_page_is_not_a_changed_page() {
        let page =
            r#"<script>window.__INITIAL_STATE__={"user":{"loggedIn":true},"x":undefined}</script>"#;
        let state = state_in(page).unwrap();
        assert_eq!(state["user"]["loggedIn"], true);
        assert!(state["x"].is_null());
        assert_eq!(
            state_in("<html>安全验证</html>").unwrap_err().class,
            FailureClass::RateLimited
        );
        assert_eq!(
            state_in("<html>nothing</html>").unwrap_err().class,
            FailureClass::SchemaChanged
        );
        assert_eq!(
            state_in("window.__INITIAL_STATE__=[1]").unwrap_err().class,
            FailureClass::SchemaChanged
        );
    }
}
