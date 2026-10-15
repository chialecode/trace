//! The fields a browser adds to the space video list request (docs: LOOP-002).
//!
//! The site's own page calls the list endpoint with more than the signed minimum: the page it
//! is called from, the environment fields of the browser (`dm_img_*`) and the visitor
//! identity the space page embeds (`w_webid`). Without them a request that worked for a
//! couple of pages is more likely to be answered with the risk-control code, so the list
//! sends them. All of them are ordinary query fields that the signature covers.
//!
//! This client has no WebGL, no pointer and no scrolled page, and says so the way a browser
//! in that state does: no recorded input, and the text a script without WebGL reports.
use crate::{AdapterState, api};
use base64::Engine;
use std::collections::HashMap;
use std::time::{Duration, Instant};
use trace_contracts::plugin::PluginError;
use trace_site_support::kit::page;

/// The page identity of an author's video tab.
const WEB_LOCATION: &str = "1550101";
/// The space page is read only for the identity it embeds; this is its response budget.
const SPACE_PAGE_MAX: u32 = 2 * 1024 * 1024;
const ID_TTL: Duration = Duration::from_secs(30 * 60);
const IDS_MAX: usize = 16;
/// Longest visitor identity accepted from a page.
const ID_LEN_MAX: usize = 600;

/// A browser script cuts the last two characters of the base64 text of what it reports.
fn reported(text: &str) -> String {
    let mut encoded = base64::engine::general_purpose::STANDARD.encode(text);
    encoded.truncate(encoded.len().saturating_sub(2));
    encoded
}

/// The query of one page of an author's video list, in the order a browser writes it. The
/// caller signs it; `web_id` is the visitor identity when the space page gave one.
pub(crate) fn list_params(
    mid: &str,
    page: u32,
    page_size: u32,
    web_id: Option<&str>,
) -> Vec<(String, String)> {
    let field = |key: &str, value: &str| (key.to_owned(), value.to_owned());
    let mut params = vec![
        field("mid", mid),
        field("ps", &page_size.to_string()),
        field("tid", "0"),
        field("pn", &page.to_string()),
        field("keyword", ""),
        field("order", "pubdate"),
        field("platform", "web"),
        field("web_location", WEB_LOCATION),
        field("order_avoided", "true"),
        field("dm_img_list", "[]"),
        field("dm_img_str", &reported("no webgl")),
        field("dm_cover_img_str", &reported("no webgl")),
        field("dm_img_inter", r#"{"ds":[],"wh":[0,0,0],"of":[0,0,0]}"#),
    ];
    if let Some(id) = web_id {
        params.push(field("w_webid", id));
    }
    params
}

/// The visitor identity the space page embeds as percent-encoded JSON, if it is one.
pub(crate) fn access_id(html: &str) -> Option<String> {
    let data = page::script_json_encoded(html, "__RENDER_DATA__")?;
    let id = data["access_id"].as_str()?;
    (!id.is_empty()
        && id.len() <= ID_LEN_MAX
        && id
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || matches!(b, b'.' | b'-' | b'_' | b'=')))
    .then(|| id.to_owned())
}

/// Visitor identities by author, fresh for a while and few in number.
#[derive(Default)]
pub struct WebIds(HashMap<String, (Instant, String)>);

impl WebIds {
    fn get(&self, mid: &str) -> Option<String> {
        self.0
            .get(mid)
            .filter(|(at, _)| at.elapsed() < ID_TTL)
            .map(|(_, id)| id.clone())
    }

    fn put(&mut self, mid: &str, id: String) {
        self.0.retain(|_, (at, _)| at.elapsed() < ID_TTL);
        if self.0.len() >= IDS_MAX
            && let Some(oldest) = self
                .0
                .iter()
                .min_by_key(|(_, (at, _))| *at)
                .map(|(mid, _)| mid.clone())
        {
            self.0.remove(&oldest);
        }
        self.0.insert(mid.to_owned(), (Instant::now(), id));
    }
}

/// The visitor identity for an author: remembered, or read from the author's space page. A
/// page that cannot be read is the site's answer and passes as an error; a page without the
/// identity returns `None` and the list goes on without it.
pub(crate) fn web_id(
    state: &AdapterState,
    mid: &str,
    deadline: Duration,
    started: Instant,
) -> Result<Option<String>, PluginError> {
    if let Some(id) = state.wbi.lock().unwrap().web_ids.get(mid) {
        return Ok(Some(id));
    }
    let url = format!("https://space.bilibili.com/{mid}");
    let reply = api::proxy_get(state, &url, true, SPACE_PAGE_MAX, deadline, started)?;
    if reply.status != 200 {
        return Err(api::classify_http(reply.status));
    }
    let body = base64::engine::general_purpose::STANDARD
        .decode(&reply.body_b64)
        .map_err(|_| {
            trace_site_support::error(
                trace_domain::errors::FailureClass::InvalidPayload,
                "space page body is not base64",
            )
        })?;
    let id = access_id(&String::from_utf8_lossy(&body));
    if let Some(id) = &id {
        state.wbi.lock().unwrap().web_ids.put(mid, id.clone());
    }
    Ok(id)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::wbi;

    const IMG: &str = "7cd084941338484aae1ad9425b84077c";
    const SUB: &str = "4932caff0ff746eab6f01bf08b70ac45";

    #[test]
    fn what_a_client_without_webgl_reports_is_fixed() {
        assert_eq!(reported("no webgl"), "bm8gd2ViZ2");
        assert_eq!(reported("ab"), "YW");
    }

    #[test]
    fn the_list_query_is_written_in_a_fixed_order() {
        let params = list_params("94697", 2, 30, Some("eyJ.web-id_1"));
        let text: Vec<String> = params.iter().map(|(k, v)| format!("{k}={v}")).collect();
        assert_eq!(
            text,
            [
                "mid=94697",
                "ps=30",
                "tid=0",
                "pn=2",
                "keyword=",
                "order=pubdate",
                "platform=web",
                "web_location=1550101",
                "order_avoided=true",
                "dm_img_list=[]",
                "dm_img_str=bm8gd2ViZ2",
                "dm_cover_img_str=bm8gd2ViZ2",
                r#"dm_img_inter={"ds":[],"wh":[0,0,0],"of":[0,0,0]}"#,
                "w_webid=eyJ.web-id_1",
            ]
        );
        assert_eq!(
            list_params("94697", 1, 30, None).len(),
            13,
            "no identity, no field"
        );
    }

    /// The expected address was computed apart from this code: the query sorted by key and
    /// percent-encoded with only unreserved characters left, `wts` added, and the MD5 of that
    /// text followed by the 32-character key derived from the two published keys.
    #[test]
    fn the_signed_list_request_has_a_fixed_vector() {
        let url = wbi::signed_url(
            "https://api.bilibili.com/x/space/wbi/arc/search",
            &list_params("94697", 2, 30, Some("eyJ.web-id_1")),
            &wbi::mixin_key(IMG, SUB),
            1_702_204_169,
        );
        assert_eq!(
            url,
            "https://api.bilibili.com/x/space/wbi/arc/search?dm_cover_img_str=bm8gd2ViZ2&dm_img_inter=%7B%22ds%22%3A%5B%5D%2C%22wh%22%3A%5B0%2C0%2C0%5D%2C%22of%22%3A%5B0%2C0%2C0%5D%7D&dm_img_list=%5B%5D&dm_img_str=bm8gd2ViZ2&keyword=&mid=94697&order=pubdate&order_avoided=true&platform=web&pn=2&ps=30&tid=0&w_webid=eyJ.web-id_1&web_location=1550101&wts=1702204169&w_rid=d1973c4ae71235d7bc494f04744540c9"
        );
    }

    #[test]
    fn the_identity_is_read_from_the_space_page_state() {
        let page = |state: &str| {
            format!(
                r#"<html><script id="__RENDER_DATA__" type="application/json">{state}</script></html>"#
            )
        };
        assert_eq!(
            access_id(&page("%7B%22access_id%22%3A%22eyJabc.def-g_h%22%7D")).as_deref(),
            Some("eyJabc.def-g_h")
        );
        assert_eq!(access_id(&page("%7B%7D")), None, "no identity");
        assert_eq!(
            access_id(&page("%7B%22access_id%22%3A%22a%20b%22%7D")),
            None,
            "not an identity"
        );
        let long = format!("%7B%22access_id%22%3A%22{}%22%7D", "a".repeat(601));
        assert_eq!(access_id(&page(&long)), None, "too long");
        assert_eq!(access_id("<html>a challenge page</html>"), None);
    }

    #[test]
    fn remembered_identities_expire_and_stay_few() {
        let mut ids = WebIds::default();
        ids.put("1", "one".into());
        assert_eq!(ids.get("1").as_deref(), Some("one"));
        ids.0.get_mut("1").unwrap().0 = Instant::now() - ID_TTL - Duration::from_secs(1);
        assert_eq!(ids.get("1"), None);
        for n in 0..(IDS_MAX + 5) {
            ids.put(&format!("m{n}"), format!("id{n}"));
        }
        assert_eq!(ids.0.len(), IDS_MAX);
        assert_eq!(
            ids.get(&format!("m{}", IDS_MAX + 4)).as_deref(),
            Some("id20")
        );
    }
}
