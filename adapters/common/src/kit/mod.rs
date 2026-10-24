//! Shared building blocks of the sites that map one work to the common model (Weibo,
//! Xiaohongshu, Instagram, X, YouTube, TikTok). A site module supplies requests and a
//! mapping from its JSON; the rest is here.
pub mod clock;
pub mod memo;
pub mod net;
pub mod page;
pub mod rules;
pub mod serve;
#[cfg(feature = "testing")]
pub mod testing;
pub mod text;
mod work;

pub use work::{Asset, Variant, Work, size_spec};

use crate::error;
use serde_json::Value;
use trace_contracts::plugin::PluginError;
use trace_domain::errors::FailureClass;

/// The User-Agent the six sites' web clients are asked with: a complete desktop browser string
/// (Windows, 64-bit Chrome), not a bare `Mozilla/5.0`.
pub const DESKTOP_UA: &str = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36";

/// The User-Agent Douyin and Bilibili are asked with. It is the value their request
/// signatures were fixed against (the vectors in their tests), so it stays as it was; a new
/// site uses `DESKTOP_UA`.
pub const SIGNED_UA: &str =
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/130.0.0.0 Safari/537.36";

/// The site changed its response shape: stop, do not guess, do not move any cursor.
pub fn schema(message: &str) -> PluginError {
    error(FailureClass::SchemaChanged, message)
}

/// The session is missing or no longer accepted.
pub fn login_needed(message: &str) -> PluginError {
    error(FailureClass::AuthRequired, message)
}

/// The site answered but refuses this account or request (private, blocked, age-gated).
pub fn denied(message: &str) -> PluginError {
    error(FailureClass::PermissionDenied, message)
}

/// The request was limited, challenged or asked to slow down: the content is unknown, not gone.
pub fn limited(message: &str) -> PluginError {
    let mut e = error(FailureClass::RateLimited, message);
    e.retry_after_ms = Some(60_000);
    e
}

/// A decimal identifier of bounded length.
pub fn numeric(text: &str, max: usize) -> bool {
    !text.is_empty() && text.len() <= max && text.bytes().all(|b| b.is_ascii_digit())
}

/// A string field, trimmed; empty counts as missing.
pub fn string(v: &Value) -> Option<String> {
    v.as_str()
        .map(str::trim)
        .filter(|s| !s.is_empty())
        .map(str::to_owned)
}

/// An https address, with a leading `//` completed; anything else is not a media address.
pub fn https(raw: &str) -> Option<String> {
    let full = if raw.starts_with("//") {
        format!("https:{raw}")
    } else if let Some(rest) = raw.strip_prefix("http://") {
        format!("https://{rest}")
    } else {
        raw.to_owned()
    };
    let url = url::Url::parse(&full).ok()?;
    (url.scheme() == "https" && url.username().is_empty() && url.password().is_none())
        .then(|| url.to_string())
}

/// Count as the site wrote it: a number, or text such as `"1.2万"` is not trusted.
pub fn count(v: &Value) -> Option<i64> {
    v.as_i64()
        .or_else(|| v.as_str().and_then(|s| s.trim().parse().ok()))
        .filter(|n| *n >= 0)
}

/// Path segments of an https link of one of `hosts` (suffix match), percent-decoded and
/// without empties; `None`
/// for another scheme, host, or a link with credentials.
pub fn link_parts(input: &str, hosts: &[&str]) -> Option<(String, Vec<String>)> {
    let url = url::Url::parse(input.trim()).ok()?;
    if url.scheme() != "https" || !url.username().is_empty() || url.password().is_some() {
        return None;
    }
    let host = url.host_str()?.to_ascii_lowercase();
    if !trace_domain::sites::host_in(&host, hosts) {
        return None;
    }
    let parts = url
        .path_segments()?
        .filter(|s| !s.is_empty())
        .map(|s| {
            percent_encoding::percent_decode_str(s)
                .decode_utf8_lossy()
                .into_owned()
        })
        .collect();
    Some((host, parts))
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn addresses_are_https_only_and_protocol_relative_ones_are_completed() {
        assert_eq!(
            https("//a.sinaimg.cn/x.jpg").as_deref(),
            Some("https://a.sinaimg.cn/x.jpg")
        );
        assert_eq!(
            https("http://a.sinaimg.cn/x.jpg").as_deref(),
            Some("https://a.sinaimg.cn/x.jpg")
        );
        assert_eq!(https("https://u:p@a.cn/x"), None);
        assert_eq!(https("javascript:alert(1)"), None);
    }

    #[test]
    fn counts_accept_numbers_and_numeric_text_only() {
        assert_eq!(count(&json!(12)), Some(12));
        assert_eq!(count(&json!("34")), Some(34));
        assert_eq!(count(&json!("1.2万")), None);
        assert_eq!(count(&json!(-1)), None);
    }

    #[test]
    fn links_must_belong_to_the_site_over_https() {
        let hosts = ["weibo.com", "weibo.cn"];
        let (host, parts) = link_parts("https://m.weibo.cn/detail/123?x=1", &hosts).unwrap();
        assert_eq!(
            (host.as_str(), parts),
            ("m.weibo.cn", vec!["detail".to_owned(), "123".to_owned()])
        );
        assert!(link_parts("http://weibo.com/1", &hosts).is_none());
        assert!(link_parts("https://weibo.com.evil.example/1", &hosts).is_none());
        assert!(numeric("123", 20) && !numeric("12a", 20) && !numeric("", 20));
    }
}
