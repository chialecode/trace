//! A post of Instagram as a list entry and as a full work.
use crate::link::code_ok;
use crate::{media, web};
use serde_json::{Value, json};
use trace_contracts::plugin::{EntryHints, EnumeratedEntry, PluginError};
use trace_site_support::kit::{self, Work, clock, count, page, string, text};

pub(crate) const POST: &str = "post";
const TITLE_MAX: usize = 60;
const SUMMARY_MAX: usize = 500;

pub(crate) fn code_of(node: &Value) -> Option<String> {
    string(&node["code"]).filter(|c| code_ok(c))
}

/// The owner's `(id, username, full name)`.
pub(crate) fn owner(node: &Value) -> (Option<String>, Option<String>, Option<String>) {
    let user = if node["user"].is_object() {
        &node["user"]
    } else {
        &node["owner"]
    };
    let id = string(&user["pk"])
        .or_else(|| user["pk"].as_i64().map(|n| n.to_string()))
        .or_else(|| string(&user["id"]));
    (id, string(&user["username"]), string(&user["full_name"]))
}

fn caption(node: &Value) -> String {
    let raw = node["caption"]["text"]
        .as_str()
        .or_else(|| node["caption"].as_str())
        .unwrap_or("");
    text::tidy(raw)
}

fn stats(node: &Value) -> Value {
    let views = ["play_count", "ig_play_count", "view_count"]
        .iter()
        .find_map(|k| count(&node[*k]));
    json!({
        "plays": views,
        "likes": count(&node["like_count"]),
        "comments": count(&node["comment_count"]),
    })
}

fn pinned(node: &Value) -> bool {
    node["timeline_pinned_user_ids"]
        .as_array()
        .is_some_and(|ids| !ids.is_empty())
        || node["is_pinned"] == true
}

/// What the list shows about a post before it is observed in full.
pub(crate) fn entry(node: &Value) -> Option<EnumeratedEntry> {
    let code = code_of(node)?;
    let words = caption(node);
    let (assets, _) = media::assets(node);
    let count_of = |role: &str| assets.iter().filter(|a| a.role == role).count() as u32;
    let mut kinds = Vec::new();
    for (present, kind) in [
        (!words.is_empty(), "text"),
        (count_of("image") > 0, "image"),
        (count_of("video") > 0, "video"),
    ] {
        if present {
            kinds.push(kind.to_owned());
        }
    }
    let cover = assets
        .iter()
        .find(|a| a.role == "cover")
        .or_else(|| assets.iter().find(|a| a.role == "image"))
        .and_then(|a| a.variants.last())
        .and_then(|v| v.urls.first().cloned());
    let taken = node["taken_at"].as_i64().and_then(clock::from_unix);
    Some(EnumeratedEntry {
        native_entry_key: code,
        native_kind: POST.into(),
        title: text::headline(&words, TITLE_MAX),
        published_precision: if taken.is_some() { "source" } else { "unknown" }.into(),
        published_at: taken,
        published_margin_seconds: None,
        pinned: pinned(node),
        summary_body: (!words.is_empty()).then(|| words.chars().take(SUMMARY_MAX).collect()),
        stats: stats(node),
        hints: Some(EntryHints {
            content_kinds: kinds,
            image_count: count_of("image"),
            video_count: count_of("video"),
            duration_ms: assets.iter().filter_map(|a| a.duration_ms).max(),
            cover_url: cover,
        }),
    })
}

/// The post whose short code is `code`, wherever the page's embedded data keeps it.
pub(crate) fn find_in_page(html: &str, code: &str) -> Option<Value> {
    let wanted = |v: &Value| v["code"].as_str() == Some(code) && v["media_type"].is_number();
    page::json_scripts(html)
        .iter()
        .find_map(|doc| page::find_object(doc, &wanted).cloned())
}

/// One post in full.
pub(crate) fn work(node: &Value, endpoint: &str) -> Result<Work, PluginError> {
    let code = code_of(node).ok_or_else(|| kit::schema("post has no code"))?;
    let (owner_id, username, full_name) = owner(node);
    let words = caption(node);
    let mut work = Work::new(&code, POST);
    work.author_key = owner_id.ok_or_else(|| kit::schema("post has no owner"))?;
    work.author_name = full_name.or_else(|| username.clone());
    let (assets, missing) = media::assets(node);
    work.assets = assets;
    if missing > 0 {
        work.omissions.push("media_unavailable");
    }
    work.title = text::headline(&words, TITLE_MAX);
    if work.title.is_empty() {
        work.title = if node["product_type"].as_str() == Some("clips") {
            "Instagram 短视频"
        } else {
            "Instagram 帖子"
        }
        .into();
    }
    work.published(node["taken_at"].as_i64().and_then(clock::from_unix));
    work.stats = stats(node);
    work.media_headers = web::media_headers();
    work.unified.tags = text::hashtags(&words);
    work.unified.mentions = text::mentions(&words);
    work.unified.canonical_url = Some(format!("{}/p/{code}/", web::ORIGIN));
    work.unified.location_name = string(&node["location"]["name"]);
    work.unified.pinned = pinned(node);
    work.unified.sponsored = node["is_paid_partnership"] == true;
    work.metadata = json!({
        "product_type": node["product_type"],
        "media_type": node["media_type"],
        "comments_disabled": node["comments_disabled"],
        "carousel_size": node["carousel_media_count"],
    });
    work.evidence = json!({"source": endpoint});
    work.body = words;
    Ok(work)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_post_is_found_by_its_code_wherever_the_page_keeps_it() {
        let html = r#"<script type="application/json">{"require":[["x",[{"items":[{"code":"AAAAA11111","media_type":1,"pk":"1"}]}]]]}</script>
            <script type="application/json">{"code":"BBBBB22222","media_type":2}</script>"#;
        assert_eq!(find_in_page(html, "BBBBB22222").unwrap()["media_type"], 2);
        assert_eq!(find_in_page(html, "AAAAA11111").unwrap()["pk"], "1");
        assert!(find_in_page(html, "CCCCC33333").is_none());
    }
}
