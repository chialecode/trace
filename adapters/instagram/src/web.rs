//! What the web client's pages say: its constants, the session's page tokens, the profile id,
//! the login state and the scripts the page loads.
use serde_json::{Value, json};
use std::collections::BTreeMap;
use std::sync::OnceLock;

pub(crate) const ORIGIN: &str = "https://www.instagram.com";
const SCRIPT_HOST: &str = "https://static.cdninstagram.com/";
/// Most script addresses taken from one page.
const SCRIPTS_LISTED: usize = 80;
pub(crate) const UA: &str = trace_site_support::kit::DESKTOP_UA;

/// The web client's values that change with its releases, kept as data in `data/` so they can
/// be updated without touching the adapter's logic.
fn data() -> &'static Value {
    static DATA: OnceLock<Value> = OnceLock::new();
    DATA.get_or_init(|| {
        serde_json::from_str(include_str!("../data/web_client.json")).unwrap_or(Value::Null)
    })
}

fn text_of(key: &str) -> &'static str {
    data()[key].as_str().unwrap_or_default()
}

pub(crate) fn app_id() -> &'static str {
    text_of("app_id")
}

pub(crate) fn asbd_id() -> &'static str {
    text_of("asbd_id")
}

/// Operation ids known in advance; the web client's scripts are read for any that are missing.
pub(crate) fn seed_doc_ids() -> BTreeMap<String, String> {
    data()["doc_ids"]
        .as_object()
        .into_iter()
        .flatten()
        .filter_map(|(name, id)| Some((name.clone(), id.as_str()?.to_owned())))
        .collect()
}

pub(crate) fn page_headers() -> Value {
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

pub(crate) fn script_headers() -> Value {
    json!({"referer": format!("{ORIGIN}/"), "accept": "*/*", "user-agent": UA})
}

/// The page tokens a GraphQL call repeats from the page.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct Tokens {
    pub dtsg: String,
    pub lsd: String,
}

impl Tokens {
    /// The form's `jazoest` check value: `2` and the sum of the characters of `fb_dtsg`.
    pub(crate) fn jazoest(&self) -> String {
        format!("2{}", self.dtsg.bytes().map(u32::from).sum::<u32>())
    }
}

fn token_chars_ok(token: &str) -> bool {
    (4..=256).contains(&token.len())
        && token.bytes().all(|b| {
            b.is_ascii_alphanumeric() || matches!(b, b':' | b'-' | b'_' | b'%' | b'.' | b'=')
        })
}

/// `"MODULE",[],{"token":"…"` — the value a page module publishes as its `token`.
fn module_token(html: &str, module: &str) -> Option<String> {
    let marker = format!("\"{module}\"");
    let mut from = 0;
    while let Some(found) = html[from..].find(&marker) {
        let start = from + found + marker.len();
        from = start;
        let window = &html[start..html.len().min(start + 120)];
        if let Some(at) = window.find("\"token\":\"") {
            let rest = &window[at + "\"token\":\"".len()..];
            if let Some(end) = rest.find('"')
                && token_chars_ok(&rest[..end])
            {
                return Some(rest[..end].to_owned());
            }
        }
    }
    None
}

pub(crate) fn tokens_in(html: &str) -> Option<Tokens> {
    Some(Tokens {
        dtsg: module_token(html, "DTSGInitialData")?,
        lsd: module_token(html, "LSD")?,
    })
}

/// A decimal value that follows `marker` in quotes.
fn quoted_number_after(html: &str, marker: &str) -> Option<String> {
    let start = html.find(marker)? + marker.len();
    let digits: String = html[start..]
        .chars()
        .take_while(char::is_ascii_digit)
        .collect();
    let closed = html[start + digits.len()..].starts_with('"');
    (closed && (1..=24).contains(&digits.len())).then_some(digits)
}

/// The id of the profile a profile page shows.
pub(crate) fn profile_id_in(html: &str) -> Option<String> {
    quoted_number_after(html, "\"profile_id\":\"")
}

/// Whether the page is the web client's generic error page for a profile. The site serves it
/// (with status 200) in place of a profile it will not show this session: a page that is gone,
/// restricted, or withheld. The words on it are drawn by the client, so the page data is read.
pub(crate) fn profile_error_page_in(html: &str) -> bool {
    html.contains("\"page_logging\":{\"name\":\"httpErrorPage\"")
        && html.contains("\"page_type\":\"PROFILE\"")
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Viewer {
    /// The page says there is no logged-in user.
    Anonymous,
    User,
    /// The page does not say.
    Unknown,
}

/// The page's own statement of who is looking. `CurrentUserInitialData` names the Instagram
/// account in `NON_FACEBOOK_USER_ID` (and again in `IG_USER_EIMU`), and `0` there is the
/// anonymous visitor. `USER_ID` and `ACCOUNT_ID` are the linked Facebook account: they are `0`
/// for a signed-in Instagram account without one, exactly as for a visitor, so they prove nothing.
pub(crate) fn viewer_in(html: &str) -> Viewer {
    let Some(at) = html.find("\"CurrentUserInitialData\"") else {
        return Viewer::Unknown;
    };
    // The block is one flat object: it ends at its first closing brace.
    let rest = &html[at..];
    let block = rest.find('}').map_or(rest, |end| &rest[..end]);
    let id = ["\"NON_FACEBOOK_USER_ID\":\"", "\"IG_USER_EIMU\":\""]
        .iter()
        .find_map(|key| quoted_number_after(block, key));
    match id {
        Some(id) if id.bytes().all(|b| b == b'0') => Viewer::Anonymous,
        Some(_) => Viewer::User,
        None => Viewer::Unknown,
    }
}

/// The addresses of the scripts the page loads from the web client's script host, in page
/// order. Page data escapes slashes, so both forms are read.
pub(crate) fn script_urls_in(html: &str) -> Vec<String> {
    let flat = html.replace("\\/", "/");
    let mut out: Vec<String> = Vec::new();
    let mut from = 0;
    while let Some(found) = flat[from..].find(SCRIPT_HOST) {
        let start = from + found;
        let end = flat[start..]
            .find(|c: char| {
                c.is_whitespace() || matches!(c, '"' | '\'' | '<' | '>' | '\\' | ')' | '(')
            })
            .map_or(flat.len(), |e| start + e);
        from = end.max(start + SCRIPT_HOST.len());
        let url = &flat[start..end];
        let path = url.split('?').next().unwrap_or(url);
        if path.ends_with(".js") && !out.iter().any(|u| u == url) {
            out.push(url.to_owned());
            if out.len() >= SCRIPTS_LISTED {
                break;
            }
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_data_file_holds_the_client_constants() {
        assert!(app_id().bytes().all(|b| b.is_ascii_digit()) && !app_id().is_empty());
        assert!(asbd_id().bytes().all(|b| b.is_ascii_digit()) && !asbd_id().is_empty());
        for (name, id) in seed_doc_ids() {
            assert!(
                !name.is_empty() && id.bytes().all(|b| b.is_ascii_digit()),
                "{name}"
            );
        }
    }

    #[test]
    fn tokens_ids_and_the_viewer_are_read_from_module_data() {
        // The shape of a signed-in page: the Facebook ids are 0, the Instagram ids are not.
        let html = r#"["DTSGInitialData",[],{"token":"NAbc_d:AXyz-1"},258],["LSD",[],{"token":"lsd-Token_9"},323],
            ["CurrentUserInitialData",[],{"ACCOUNT_ID":"0","USER_ID":"0","NAME":"x","IS_BUSINESS_DOMAIN":false,"NON_FACEBOOK_USER_ID":"1500000000000000009","IS_INSTAGRAM_USER":1,"IG_USER_EIMU":"1500000000000000009"},270],
            {"profile_id":"1500000000000000001","other":"x"}"#;
        let tokens = tokens_in(html).unwrap();
        assert_eq!(tokens.dtsg, "NAbc_d:AXyz-1");
        assert_eq!(tokens.lsd, "lsd-Token_9");
        assert_eq!(profile_id_in(html).as_deref(), Some("1500000000000000001"));
        assert_eq!(viewer_in(html), Viewer::User);
        let anonymous = html.replace("1500000000000000009", "0");
        assert_eq!(viewer_in(&anonymous), Viewer::Anonymous);
        let only_eimu = html.replace(r#""NON_FACEBOOK_USER_ID":"1500000000000000009","#, "");
        assert_eq!(viewer_in(&only_eimu), Viewer::User);
        let facebook_only = r#"["CurrentUserInitialData",[],{"ACCOUNT_ID":"17","USER_ID":"17","NAME":"x"},270],{"NON_FACEBOOK_USER_ID":"5"}"#;
        assert_eq!(
            viewer_in(facebook_only),
            Viewer::Unknown,
            "a Facebook id alone, or an id outside the block, proves nothing"
        );
        assert_eq!(viewer_in("<html>"), Viewer::Unknown);
        assert!(tokens_in(r#""DTSGInitialData",[],{"token":"a b"}"#).is_none());
        assert!(tokens_in(r#""LSD",[],{"token":"abcdef"}"#).is_none());
        assert_eq!(profile_id_in(r#""profile_id":"12a""#), None);
        let error_route = r#"{"props":{"page_logging":{"name":"httpErrorPage","params":{}},"failure_reason":null,"restricted_age":null,"page_type":"PROFILE"}}"#;
        assert!(profile_error_page_in(error_route));
        assert!(!profile_error_page_in(
            &error_route.replace("PROFILE", "POST")
        ));
        assert!(!profile_error_page_in(
            &error_route.replace("httpErrorPage", "profilePage")
        ));
    }

    #[test]
    fn jazoest_is_two_and_the_character_sum() {
        let tokens = Tokens {
            dtsg: "ab".into(),
            lsd: "x".into(),
        };
        assert_eq!(tokens.jazoest(), format!("2{}", 97 + 98));
    }

    #[test]
    fn script_addresses_are_taken_from_attributes_and_escaped_page_data_only_from_the_script_host()
    {
        let html = r#"<link rel="preload" as="script" href="https://static.cdninstagram.com/rsrc.php/v4/a/b.js?_nc_x=1">
            <script src="https://static.cdninstagram.com/rsrc.php/v4/c.js" async></script>
            {"url":"https:\/\/static.cdninstagram.com\/rsrc.php\/v4\/d.js"}
            <script src="https://static.cdninstagram.com/rsrc.php/v4/c.js"></script>
            <img src="https://static.cdninstagram.com/rsrc.php/v4/e.png">
            <script src="https://evil.example/f.js"></script>"#;
        assert_eq!(
            script_urls_in(html),
            [
                "https://static.cdninstagram.com/rsrc.php/v4/a/b.js?_nc_x=1",
                "https://static.cdninstagram.com/rsrc.php/v4/c.js",
                "https://static.cdninstagram.com/rsrc.php/v4/d.js",
            ]
        );
    }
}
