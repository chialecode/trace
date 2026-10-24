//! 用户视频列表：空间投稿分页与游标。

use crate::clock::{rfc3339, unix_now};
use crate::sign::signed_get;
use crate::{AdapterState, api, wbi};
use serde_json::{Value, json};
use std::time::{Duration, Instant};
use trace_contracts::plugin::{EntryHints, EnumerateParams, EnumeratedEntry, EnumeratedPage};
use trace_domain::errors::FailureClass;

/// 每页条数（站点 wbi/arc/search 上限 50，取保守 30）。
pub(crate) const PAGE_SIZE: u32 = 30;

pub(crate) fn enumerate(
    state: &AdapterState,
    p: &EnumerateParams,
    deadline: Duration,
    started: Instant,
) -> Result<EnumeratedPage, trace_contracts::plugin::PluginError> {
    let mid = p.native_account_key.trim().to_string();
    if mid.is_empty() || !mid.chars().all(|c| c.is_ascii_digit()) {
        return Err(trace_contracts::plugin::PluginError {
            class: FailureClass::InvalidPayload,
            message: "native_account_key must be a bilibili mid".into(),
            retry_after_ms: None,
            details: Value::Null,
        });
    }
    let pn: u32 = p
        .cursor
        .as_deref()
        .and_then(|c| c.parse().ok())
        .unwrap_or(1);
    let web_id = crate::browser::web_id(state, &mid, deadline, started)?;
    let params = crate::browser::list_params(&mid, pn, PAGE_SIZE, web_id.as_deref());
    let data = signed_get(state, api::ARC_SEARCH_URL, &params, true, deadline, started)?;
    let cutoff: Option<i64> = p
        .recent_days
        .filter(|d| *d > 0)
        .map(|d| unix_now() as i64 - d * 86_400);
    let count = data.pointer("/page/count").and_then(|c| c.as_i64());
    let (items, truncated_by_cutoff) = map_vlist(&data, cutoff)?;
    // Short/filtered pages are not proof that a source count is exhausted.
    let within_count = match (&count, pn) {
        (Some(c), pn) => (pn as i64) * (PAGE_SIZE as i64) < *c,
        (None, _) => false,
    };
    let has_more = within_count;
    Ok(EnumeratedPage {
        items,
        next_cursor: has_more.then(|| (pn + 1).to_string()),
        has_more,
        coverage: json!({
            "count_observed": count,
            "page": pn,
            "ps": PAGE_SIZE,
            "order": "pubdate",
            "browser_params": true,
            "web_id": web_id.is_some(),
            "wbi": wbi::ALGO_VERSION,
            "cutoff_hit": truncated_by_cutoff,
        }),
    })
}

/// A video's length as the list gives it (`5:23` or `1:02:03`), in milliseconds.
fn clock_ms(text: &str) -> Option<i64> {
    let parts: Vec<i64> = text
        .trim()
        .split(':')
        .map(|part| part.parse::<i64>().ok().filter(|n| *n >= 0))
        .collect::<Option<_>>()?;
    if parts.is_empty() || parts.len() > 3 {
        return None;
    }
    let seconds = parts.iter().fold(0, |total, part| total * 60 + part);
    (seconds > 0).then_some(seconds * 1000)
}

/// vlist → 条目（按项过滤日期，置顶/乱序不终止后续读取；
/// 空列表不宣称"无作品"——has_more=false 只表示本轮页边界）。
fn map_vlist(
    data: &Value,
    cutoff: Option<i64>,
) -> Result<(Vec<EnumeratedEntry>, bool), trace_contracts::plugin::PluginError> {
    let vlist = data
        .pointer("/list/vlist")
        .and_then(|v| v.as_array())
        .ok_or_else(|| trace_contracts::plugin::PluginError {
            class: FailureClass::SchemaChanged,
            message: "arc search response missing list.vlist".into(),
            retry_after_ms: None,
            details: Value::Null,
        })?;
    let mut items = Vec::new();
    let mut truncated = false;
    for v in vlist {
        let created = v.get("created").and_then(|c| c.as_i64()).unwrap_or(0);
        if let Some(cutoff) = cutoff
            && created > 0
            && created < cutoff
        {
            truncated = true;
            continue;
        }
        let bvid = v
            .get("bvid")
            .and_then(|b| b.as_str())
            .unwrap_or("")
            .to_string();
        if bvid.is_empty() {
            return Err(trace_contracts::plugin::PluginError {
                class: FailureClass::SchemaChanged,
                message: "vlist entry missing bvid".into(),
                retry_after_ms: None,
                details: Value::Null,
            });
        }
        items.push(EnumeratedEntry {
            native_entry_key: bvid,
            native_kind: "video".into(),
            title: v
                .get("title")
                .and_then(|t| t.as_str())
                .unwrap_or("")
                .to_string(),
            published_at: (created > 0).then(|| rfc3339(created)),
            published_precision: "source".into(),
            published_margin_seconds: None,
            pinned: false,
            summary_body: v
                .get("description")
                .and_then(|d| d.as_str())
                .map(str::to_string),
            stats: json!({
                "play": v.get("play"),
                "review": v.get("review"),
                "video_review": v.get("video_review"),
                "favorites": v.get("favorites"),
                "length": v.get("length"),
            }),
            hints: Some(EntryHints {
                content_kinds: vec!["video".into()],
                video_count: 1,
                duration_ms: v.get("length").and_then(Value::as_str).and_then(clock_ms),
                ..EntryHints::default()
            }),
        });
    }
    Ok((items, truncated))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn enumerate_page_maps_entries_and_cursor() {
        let env: Value = serde_json::from_str(include_str!(
            "../../../tests/fixtures/bilibili/arc_search_page1.json"
        ))
        .unwrap();
        let data = env["data"].clone();
        let (items, truncated) = map_vlist(&data, None).unwrap();
        assert_eq!(items.len(), 3);
        assert!(!truncated);
        assert_eq!(items[0].native_entry_key, "BV1fakeBVfake1");
        assert_eq!(items[0].native_kind, "video");
        assert_eq!(items[0].published_precision, "source");
        assert!(
            items[0]
                .published_at
                .as_deref()
                .unwrap_or("")
                .ends_with('Z')
        );
        // recent_days 截断：cutoff 在所有条目之后 → 立即截断且不产出。
        let (items2, truncated2) = map_vlist(&data, Some(3_000_000_000)).unwrap();
        assert!(truncated2);
        assert!(items2.is_empty());
        // cutoff 在最旧一条之后 1 秒 → 只保留更新的两条。
        let (items3, truncated3) = map_vlist(&data, Some(rfc3339_past_cutoff(&items))).unwrap();
        assert!(truncated3);
        assert_eq!(items3.len(), 2, "only entries newer than cutoff remain");
    }

    fn rfc3339_past_cutoff(items: &[EnumeratedEntry]) -> i64 {
        // 取最后一条（最旧）的发布秒，加一秒作为 cutoff。
        let oldest = items.last().unwrap().published_at.as_deref().unwrap();
        use time::format_description::well_known::Rfc3339;
        time::OffsetDateTime::parse(oldest, &Rfc3339)
            .unwrap()
            .unix_timestamp()
            + 1
    }

    #[test]
    fn the_length_a_list_gives_becomes_milliseconds_for_the_preview() {
        assert_eq!(clock_ms("5:23"), Some(323_000));
        assert_eq!(clock_ms("1:02:03"), Some(3_723_000));
        assert_eq!(clock_ms("00:00"), None);
        assert_eq!(clock_ms("soon"), None);
        let data = json!({"list":{"vlist":[{"bvid":"a","created":300,"length":"0:45"},{"bvid":"b","created":200}]}});
        let (items, _) = map_vlist(&data, None).unwrap();
        let hints = items[0].hints.clone().unwrap();
        assert_eq!((hints.video_count, hints.duration_ms), (1, Some(45_000)));
        assert_eq!(items[1].hints.clone().unwrap().duration_ms, None);
    }

    #[test]
    fn pinned_old_video_cannot_hide_later_recent_or_unknown_dates() {
        let data = json!({"list":{"vlist":[{"bvid":"old","created":100},{"bvid":"new","created":300},{"bvid":"unknown"},{"bvid":"later","created":250}]}});
        let (items, filtered) = map_vlist(&data, Some(200)).unwrap();
        assert!(filtered);
        assert_eq!(
            items
                .iter()
                .map(|v| v.native_entry_key.as_str())
                .collect::<Vec<_>>(),
            vec!["new", "unknown", "later"]
        );
        assert!(items[1].published_at.is_none());
    }

    /// The list as the real adapter runs it, against canned site answers.
    mod run {
        use super::*;
        use crate::wbi::WbiCache;
        use std::sync::Mutex;
        use trace_site_support::kit::testing::Fixtures;

        const NAV: &str = include_str!("../../../tests/fixtures/bilibili/nav.json");
        const PAGE: &str = include_str!("../../../tests/fixtures/bilibili/arc_search_page1.json");
        const SPACE: &str = r#"<html><script id="__RENDER_DATA__" type="application/json">%7B%22access_id%22%3A%22eyJ.visitor-1%22%7D</script></html>"#;

        fn site(search: &str) -> Fixtures {
            Fixtures::new()
                .on("/x/web-interface/nav", NAV)
                .on("https://space.bilibili.com/94697", SPACE)
                .on("/x/space/wbi/arc/search", search)
        }

        fn params(cursor: Option<&str>) -> EnumerateParams {
            EnumerateParams {
                content_type: "video".into(),
                native_account_key: "94697".into(),
                mode: "resume_cursor".into(),
                cursor: cursor.map(str::to_owned),
                recent_days: None,
                max_pages: 10,
            }
        }

        fn list(
            site: &Fixtures,
            wbi: &Mutex<WbiCache>,
            cursor: Option<&str>,
        ) -> Result<EnumeratedPage, trace_contracts::plugin::PluginError> {
            let state = AdapterState { client: site, wbi };
            enumerate(
                &state,
                &params(cursor),
                Duration::from_secs(5),
                Instant::now(),
            )
        }

        #[test]
        fn pages_carry_the_browser_fields_and_the_visitor_identity_is_read_once() {
            let site = site(PAGE);
            let wbi = Mutex::new(WbiCache::default());
            let first = list(&site, &wbi, None).unwrap();
            assert_eq!(first.coverage["web_id"], true);
            assert_eq!(first.coverage["browser_params"], true);
            list(&site, &wbi, Some("2")).unwrap();
            assert_eq!(
                site.count("space.bilibili.com"),
                1,
                "one page for two lists"
            );
            assert_eq!(site.count("/x/web-interface/nav"), 1, "one key for both");
            let searches: Vec<_> = site
                .requests()
                .into_iter()
                .filter(|r| r.url.contains("/arc/search"))
                .collect();
            assert_eq!(searches.len(), 2);
            for request in &searches {
                for field in [
                    "web_location=1550101",
                    "platform=web",
                    "dm_img_list=%5B%5D",
                    "dm_img_str=bm8gd2ViZ2",
                    "dm_cover_img_str=bm8gd2ViZ2",
                    "w_webid=eyJ.visitor-1",
                    "w_rid=",
                    "wts=",
                ] {
                    assert!(request.url.contains(field), "{field} in {}", request.url);
                }
                assert!(
                    request.credential,
                    "the account's own session signs the list"
                );
            }
            assert!(searches[1].url.contains("pn=2"));
            let space = site
                .requests()
                .into_iter()
                .find(|r| r.url.contains("space.bilibili.com"))
                .unwrap();
            assert!(space.credential && !space.url.contains('?'));
        }

        #[test]
        fn a_risk_control_answer_is_a_stop_not_an_empty_list() {
            let denied = r#"{"code":-352,"message":"risk control","data":{"v_voucher":"x"}}"#;
            let site = site(denied);
            let wbi = Mutex::new(WbiCache::default());
            let error = list(&site, &wbi, None).unwrap_err();
            assert_eq!(error.class, FailureClass::PermissionDenied);
            assert_eq!(error.details["api_code"], -352);
            // A challenge is not a stale key: one attempt, no retry, no further page.
            assert_eq!(site.count("/arc/search"), 1);
            assert_eq!(site.count("/x/web-interface/nav"), 1);
        }

        #[test]
        fn a_space_page_without_the_identity_still_lists_and_says_so() {
            let site = Fixtures::new()
                .on("/x/web-interface/nav", NAV)
                .on("https://space.bilibili.com/94697", "<html>no state</html>")
                .on("/x/space/wbi/arc/search", PAGE);
            let wbi = Mutex::new(WbiCache::default());
            let page = list(&site, &wbi, None).unwrap();
            assert_eq!(page.coverage["web_id"], false);
            let search = site
                .requests()
                .into_iter()
                .find(|r| r.url.contains("/arc/search"))
                .unwrap();
            assert!(!search.url.contains("w_webid"));
            // Nothing was remembered: the next list asks the space page again.
            list(&site, &wbi, Some("2")).unwrap();
            assert_eq!(site.count("space.bilibili.com"), 2);
        }

        #[test]
        fn a_refused_space_page_stops_the_list_before_it_asks_for_videos() {
            let site = Fixtures::new()
                .on("/x/web-interface/nav", NAV)
                .status("https://space.bilibili.com/94697", 412)
                .on("/x/space/wbi/arc/search", PAGE);
            let wbi = Mutex::new(WbiCache::default());
            assert!(list(&site, &wbi, None).is_err());
            assert_eq!(site.count("/arc/search"), 0);
        }
    }
}
