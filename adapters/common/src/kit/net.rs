//! Requests a site module makes: JSON, page text and read-only query bodies, all through the
//! host's proxy. Anything that is not a plain success is an error with a failure class; no
//! caller sees a body that did not come with a success status.
use crate::runtime::{Fetch, Transport, body_bytes, decode_json};
use serde_json::Value;
use trace_contracts::plugin::PluginError;

/// A JSON document by GET.
pub fn json(
    net: &dyn Transport,
    url: &str,
    headers: Value,
    credential: bool,
) -> Result<Value, PluginError> {
    decode_json(net.request(&Fetch::get(url, headers, credential).large())?)
}

/// A page (HTML or script) by GET, as text. Invalid UTF-8 is replaced, never an error: a
/// page that is not text fails later, when the state it should hold is not found.
pub fn text(
    net: &dyn Transport,
    url: &str,
    headers: Value,
    credential: bool,
) -> Result<String, PluginError> {
    let reply = net.request(&Fetch::get(url, headers, credential).large())?;
    Ok(String::from_utf8_lossy(&body_bytes(&reply)?).into_owned())
}

/// A page as text, with the address it was finally served from (a consent or login page
/// replaces the page that was asked for).
pub fn page(
    net: &dyn Transport,
    url: &str,
    headers: Value,
    credential: bool,
) -> Result<(String, String), PluginError> {
    let reply = net.request(&Fetch::get(url, headers, credential).large())?;
    let text = String::from_utf8_lossy(&body_bytes(&reply)?).into_owned();
    Ok((text, reply.final_url))
}

/// A read-only query by POST (a form or a JSON document, as `content-type` in `headers`
/// says), answered with JSON.
pub fn query(
    net: &dyn Transport,
    url: &str,
    headers: Value,
    body: &str,
    credential: bool,
) -> Result<Value, PluginError> {
    decode_json(net.request(&Fetch::post(url, headers, body, credential).large())?)
}

/// Where a short link ends up, asked of the site without any credential.
pub fn final_url(net: &dyn Transport, url: &str, headers: Value) -> Result<String, PluginError> {
    let reply = net.request(&Fetch::get(url, headers, false).with_method("HEAD"))?;
    if reply.final_url.is_empty() {
        return Err(crate::error(
            trace_domain::errors::FailureClass::SourceUnavailable,
            "short link did not resolve",
        ));
    }
    Ok(reply.final_url)
}

/// `a=1&b=2` from pairs, percent-encoded.
pub fn form(pairs: &[(&str, &str)]) -> String {
    url::form_urlencoded::Serializer::new(String::new())
        .extend_pairs(pairs)
        .finish()
}

/// `https://host/path?a=1` from a base and pairs.
pub fn with_query(base: &str, pairs: &[(&str, &str)]) -> String {
    if pairs.is_empty() {
        return base.to_owned();
    }
    let sep = if base.contains('?') { '&' } else { '?' };
    format!("{base}{sep}{}", form(pairs))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn forms_and_queries_are_percent_encoded() {
        assert_eq!(form(&[("a", "1 2"), ("b", "中")]), "a=1+2&b=%E4%B8%AD");
        assert_eq!(
            with_query("https://x.example/p", &[("q", "a&b")]),
            "https://x.example/p?q=a%26b"
        );
        assert_eq!(
            with_query("https://x.example/p?z=1", &[("q", "1")]),
            "https://x.example/p?z=1&q=1"
        );
        assert_eq!(
            with_query("https://x.example/p", &[]),
            "https://x.example/p"
        );
    }
}
