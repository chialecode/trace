//! YouTube's web client interface: pages with embedded state, and the browse call that
//! continues a list. No key, no login: what a visitor of the site would be shown.
use serde_json::{Value, json};
use trace_contracts::plugin::PluginError;
use trace_site_support::kit::{self, net, page, schema};
use trace_site_support::runtime::Transport;

pub(crate) const ORIGIN: &str = "https://www.youtube.com";
pub(crate) const UA: &str = kit::DESKTOP_UA;

fn page_headers() -> Value {
    json!({
        "user-agent": UA,
        "accept-language": "en-US,en;q=0.9",
        "accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
    })
}

/// What the media hosts expect; no authentication data.
pub(crate) fn media_headers() -> Value {
    json!({"referer": format!("{ORIGIN}/"), "user-agent": UA})
}

/// A page of the site as text. A consent wall, a sign-in page or the "unusual traffic" page
/// in its place is a refusal to answer, never content.
pub(crate) fn html(net: &dyn Transport, path: &str) -> Result<String, PluginError> {
    let (text, at) = net::page(net, &format!("{ORIGIN}{path}"), page_headers(), false)?;
    if let Ok(at) = url::Url::parse(&at) {
        let host = at.host_str().unwrap_or_default();
        if host.starts_with("consent.") {
            return Err(kit::denied("YouTube asks for consent before showing pages"));
        }
        if host == "accounts.google.com" {
            return Err(kit::denied("YouTube asks the visitor to sign in"));
        }
        if at.path().starts_with("/sorry") {
            return Err(kit::limited(
                "YouTube asks the visitor to prove they are human",
            ));
        }
    }
    Ok(text)
}

/// `var ytInitialData = {...};` of a page.
pub(crate) fn initial_data(text: &str) -> Result<Value, PluginError> {
    page::json_after(text, "ytInitialData =")
        .or_else(|| page::json_after(text, "ytInitialData\"] ="))
        .ok_or_else(|| schema("page has no initial data"))
}

/// `var ytInitialPlayerResponse = {...};` of a watch page.
pub(crate) fn player_response(text: &str) -> Result<Value, PluginError> {
    page::json_after(text, "ytInitialPlayerResponse =")
        .ok_or_else(|| schema("page has no player response"))
}

/// The web client's version, as the page announces it.
pub(crate) fn client_version(text: &str) -> Option<String> {
    let marker = "\"INNERTUBE_CLIENT_VERSION\"";
    let rest = text[text.find(marker)? + marker.len()..].trim_start();
    let rest = rest.strip_prefix(':')?.trim_start().strip_prefix('"')?;
    let version: String = rest
        .chars()
        .take_while(|c| c.is_ascii_digit() || *c == '.')
        .collect();
    (version.len() >= 5 && rest[version.len()..].starts_with('"')).then_some(version)
}

/// One browse call: a continuation token, or a channel tab.
pub(crate) fn browse(
    net: &dyn Transport,
    version: &str,
    request: Value,
) -> Result<Value, PluginError> {
    let mut body = json!({"context": {"client": {
        "hl": "en", "clientName": "WEB", "clientVersion": version,
    }}});
    if let (Some(into), Some(from)) = (body.as_object_mut(), request.as_object()) {
        into.extend(from.clone());
    }
    let headers = json!({
        "user-agent": UA,
        "accept-language": "en-US,en;q=0.9",
        "origin": ORIGIN,
        "referer": format!("{ORIGIN}/"),
        "content-type": "application/json",
        "x-youtube-client-name": "1",
        "x-youtube-client-version": version,
    });
    net::query(
        net,
        &format!("{ORIGIN}/youtubei/v1/browse?prettyPrint=false"),
        headers,
        &body.to_string(),
        false,
    )
}

/// The text of a `simpleText` / `runs` value, or of a view model's `content`.
pub(crate) fn text(v: &Value) -> String {
    if let Some(s) = v.as_str() {
        return s.trim().to_owned();
    }
    if let Some(s) = v["simpleText"].as_str().or_else(|| v["content"].as_str()) {
        return s.trim().to_owned();
    }
    v["runs"]
        .as_array()
        .map(|runs| runs.iter().filter_map(|r| r["text"].as_str()).collect())
        .unwrap_or_default()
}

pub(crate) use kit::page::find_all;

/// The channel's identity as a channel page states it.
pub(crate) fn channel_id(data: &Value) -> Option<String> {
    kit::string(&data["metadata"]["channelMetadataRenderer"]["externalId"])
        .filter(|id| crate::link::is_channel_id(id))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn versions_are_read_from_the_page_config_and_nothing_else_passes() {
        let page = r#"ytcfg.set({"INNERTUBE_CLIENT_VERSION":"2.20261015.01.00","X":1})"#;
        assert_eq!(client_version(page).as_deref(), Some("2.20261015.01.00"));
        assert_eq!(client_version(r#""INNERTUBE_CLIENT_VERSION":"x""#), None);
        assert_eq!(client_version("nothing"), None);
    }

    #[test]
    fn text_comes_from_runs_simple_text_or_content() {
        assert_eq!(text(&json!({"simpleText": " a "})), "a");
        assert_eq!(text(&json!({"runs": [{"text": "a"}, {"text": "b"}]})), "ab");
        assert_eq!(text(&json!({"content": "c"})), "c");
        assert_eq!(text(&json!(" plain ")), "plain");
        assert_eq!(text(&json!(null)), "");
    }

    #[test]
    fn a_match_hides_what_is_inside_it() {
        let doc = json!({"a": [{"videoRenderer": {"id": 1, "x": {"videoRenderer": {"id": 2}}}},
            {"b": {"videoRenderer": {"id": 3}}}]});
        let ids: Vec<_> = find_all(&doc, &["videoRenderer"])
            .iter()
            .map(|(_, v)| v["id"].as_i64().unwrap())
            .collect();
        assert_eq!(ids, [1, 3]);
    }
}
