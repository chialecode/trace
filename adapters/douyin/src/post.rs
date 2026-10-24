//! Aweme classification and the post-list page mapping.
use crate::api::fail;
use serde_json::{Value, json};
use trace_contracts::plugin::{EntryHints, EnumeratedEntry, EnumeratedPage, PluginError};
use trace_site_support::id;

pub(crate) fn kind(v: &Value) -> &'static str {
    if v["aweme_type"] == 163 || v["article_info"].is_object() {
        "article"
    } else if gallery(v).is_some() || matches!(v["aweme_type"].as_i64(), Some(2 | 68 | 150)) {
        "image"
    } else if v["video"].is_object() {
        "video"
    } else {
        "unknown"
    }
}
pub(crate) fn gallery(v: &Value) -> Option<&Vec<Value>> {
    v["images"]
        .as_array()
        .or_else(|| v["image_post_info"]["images"].as_array())
}
pub(crate) fn timestamp(v: &Value) -> Option<String> {
    time::OffsetDateTime::from_unix_timestamp(v.as_i64()?)
        .ok()?
        .format(&time::format_description::well_known::Rfc3339)
        .ok()
}
/// What the list already says about a work, for the preview: its kind, the number of pictures
/// and the length of a video.
fn hints(a: &Value, kind: &str) -> Option<EntryHints> {
    let hints = match kind {
        "video" => EntryHints {
            content_kinds: vec!["video".into()],
            video_count: 1,
            duration_ms: a["video"]["duration"].as_i64().filter(|n| *n > 0),
            ..EntryHints::default()
        },
        "image" => EntryHints {
            content_kinds: vec!["image".into()],
            image_count: gallery(a).map_or(0, |g| g.len().min(u32::MAX as usize) as u32),
            ..EntryHints::default()
        },
        "article" => EntryHints {
            content_kinds: vec!["article".into()],
            ..EntryHints::default()
        },
        _ => return None,
    };
    Some(hints)
}

pub(crate) fn page(v: &Value, lane: &str) -> Result<EnumeratedPage, PluginError> {
    let values = v["aweme_list"]
        .as_array()
        .ok_or_else(|| fail("post response missing list"))?;
    let more = match &v["has_more"] {
        Value::Bool(b) => *b,
        Value::Number(n) => n.as_u64().ok_or_else(|| fail("invalid has_more"))? != 0,
        _ => return Err(fail("missing has_more")),
    };
    let cursor = id(&v["max_cursor"]);
    if more && cursor.is_none() {
        return Err(fail("missing continuation cursor"));
    }
    let mut items = Vec::new();
    for a in values {
        let k = kind(a);
        if lane != "dynamic" && lane != "all" && lane != k {
            continue;
        }
        items.push(EnumeratedEntry {
            native_entry_key: id(&a["aweme_id"]).ok_or_else(|| fail("missing aweme identity"))?,
            native_kind: k.into(),
            title: a["desc"].as_str().unwrap_or("").into(),
            published_at: timestamp(&a["create_time"]),
            published_precision: "source".into(),
            published_margin_seconds: None,
            pinned: a["is_top"] == 1,
            summary_body: a["desc"].as_str().map(str::to_owned),
            stats: trace_contracts::content::safe_metadata(&a["statistics"]),
            hints: hints(a, k),
        });
    }
    Ok(EnumeratedPage {
        items,
        next_cursor: if more { cursor } else { None },
        has_more: more,
        coverage: json!({"source":"aweme.post","lane":lane,"observed_count":values.len(),"history":"unknown"}),
    })
}
pub(crate) fn urls(v: &Value) -> Vec<String> {
    v["url_list"]
        .as_array()
        .map(|a| {
            a.iter()
                .filter_map(Value::as_str)
                .map(str::to_owned)
                .collect()
        })
        .unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_list_tells_the_preview_what_each_work_is() {
        let list = json!({"has_more":0,"max_cursor":"0","aweme_list":[
            {"aweme_id":"1","video":{"duration":15000},"create_time":1760000000},
            {"aweme_id":"2","aweme_type":68,"images":[{},{},{}],"create_time":1760000100},
            {"aweme_id":"3","video":{},"create_time":1760000200}]});
        let page = page(&list, "all").unwrap();
        let hints = |i: usize| page.items[i].hints.clone().unwrap();
        assert_eq!(
            (
                hints(0).content_kinds,
                hints(0).video_count,
                hints(0).duration_ms
            ),
            (vec!["video".to_string()], 1, Some(15000))
        );
        assert_eq!(
            (hints(1).content_kinds, hints(1).image_count),
            (vec!["image".to_string()], 3)
        );
        assert_eq!(
            hints(2).duration_ms,
            None,
            "a length the list does not give stays unknown"
        );
    }
}
