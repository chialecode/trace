//! Space feeds: article list, opus feed and dynamic feed, one page per call.
use super::{fail, get, timestamp};
use crate::AdapterState;
use serde_json::{Value, json};
use std::time::{Duration, Instant};
use trace_contracts::plugin::{EnumerateParams, EnumeratedEntry, EnumeratedPage, PluginError};
use trace_domain::errors::FailureClass;
use trace_site_support::id;

const ARTICLE_PAGE_SIZE: u64 = 30;

pub fn enumerate(
    state: &AdapterState,
    p: &EnumerateParams,
    d: Duration,
    t: Instant,
) -> Result<EnumeratedPage, PluginError> {
    let cursor = p.cursor.as_deref().unwrap_or("");
    match p.content_type.as_str() {
        "article" => article_page(state, p, cursor, d, t),
        "image" | "dynamic" => feed_page(state, p, cursor, d, t),
        _ => Err(trace_site_support::error(
            FailureClass::Unsupported,
            "unsupported Bilibili content lane",
        )),
    }
}

fn article_page(
    state: &AdapterState,
    p: &EnumerateParams,
    cursor: &str,
    d: Duration,
    t: Instant,
) -> Result<EnumeratedPage, PluginError> {
    let data = get(
        state,
        "/x/space/article",
        &[
            ("mid", &p.native_account_key),
            ("pn", if cursor.is_empty() { "1" } else { cursor }),
            ("ps", "30"),
            ("sort", "publish_time"),
        ],
        d,
        t,
    )?;
    let list = data["articles"]
        .as_array()
        .ok_or_else(|| fail("article list missing"))?;
    let mut items = Vec::new();
    for a in list {
        items.push(EnumeratedEntry {
            native_entry_key: id(&a["id"]).ok_or_else(|| fail("article id missing"))?,
            native_kind: "article".into(),
            title: a["title"].as_str().unwrap_or("").into(),
            published_at: timestamp(&a["publish_time"]),
            published_precision: "source".into(),
            published_margin_seconds: None,
            pinned: false,
            summary_body: a["summary"].as_str().map(str::to_owned),
            stats: trace_contracts::content::safe_metadata(&a["stats"]),
            hints: None,
        });
    }
    let page = cursor.parse::<u64>().unwrap_or(1);
    let more = page * ARTICLE_PAGE_SIZE
        < data["count"]
            .as_u64()
            .ok_or_else(|| fail("article count missing"))?;
    Ok(EnumeratedPage {
        items,
        next_cursor: more.then(|| (page + 1).to_string()),
        has_more: more,
        coverage: json!({"source":"space.article","page":page,"history":"unknown"}),
    })
}

/// Opus feed (`image` lane) or dynamic feed (`dynamic` lane).
fn feed_page(
    state: &AdapterState,
    p: &EnumerateParams,
    cursor: &str,
    d: Duration,
    t: Instant,
) -> Result<EnumeratedPage, PluginError> {
    let opus = p.content_type == "image";
    let data = if opus {
        get(
            state,
            "/x/polymer/web-dynamic/v1/opus/feed/space",
            &[
                ("host_mid", &p.native_account_key),
                ("offset", cursor),
                ("type", "all"),
            ],
            d,
            t,
        )?
    } else {
        get(
            state,
            "/x/polymer/web-dynamic/v1/feed/space",
            &[("host_mid", &p.native_account_key), ("offset", cursor)],
            d,
            t,
        )?
    };
    let list = data["items"]
        .as_array()
        .ok_or_else(|| fail("dynamic list missing"))?;
    let mut items = Vec::new();
    for a in list {
        items.push(feed_entry(a, opus)?);
    }
    let more = data["has_more"]
        .as_bool()
        .or_else(|| data["has_more"].as_u64().map(|n| n != 0))
        .ok_or_else(|| fail("dynamic has_more missing"))?;
    Ok(EnumeratedPage {
        items,
        next_cursor: if more {
            Some(id(&data["offset"]).ok_or_else(|| fail("dynamic cursor missing"))?)
        } else {
            None
        },
        has_more: more,
        coverage: json!({"source":if opus{"opus.feed.space"}else{"feed.space"},"history":"unknown"}),
    })
}

fn feed_entry(a: &Value, opus: bool) -> Result<EnumeratedEntry, PluginError> {
    let key = id(&a["id_str"])
        .or_else(|| id(&a["opus_id"]))
        .ok_or_else(|| fail("dynamic identity missing"))?;
    let body = a
        .pointer("/modules/module_dynamic/desc/text")
        .or_else(|| a.get("content"))
        .and_then(Value::as_str)
        .unwrap_or("");
    Ok(EnumeratedEntry {
        native_entry_key: key,
        native_kind: if opus { "opus" } else { "dynamic" }.into(),
        title: a["title"]
            .as_str()
            .unwrap_or(body)
            .chars()
            .take(100)
            .collect(),
        published_at: timestamp(&a["modules"]["module_author"]["pub_ts"]),
        published_precision: "source".into(),
        published_margin_seconds: None,
        pinned: a["modules"]["module_tag"]["text"] == "置顶",
        summary_body: Some(body.into()),
        stats: trace_contracts::content::safe_metadata(&a["modules"]["module_stat"]),
        hints: None,
    })
}
