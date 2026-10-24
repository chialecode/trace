//! A Weibo status as a list entry and as a full work.
use crate::link::mid_to_mblogid;
use crate::media;
use serde_json::{Value, json};
use trace_contracts::plugin::{EntryHints, EnumeratedEntry, PluginError};
use trace_contracts::unified::Relation;
use trace_site_support::kit::{self, Work, clock, count, string, text};

const KIND: &str = "post";
const TITLE_MAX: usize = 60;
const SUMMARY_MAX: usize = 500;

/// The status's numeric id, the identity of the post.
pub(crate) fn id_of(status: &Value) -> Option<String> {
    string(&status["idstr"])
        .or_else(|| status["id"].as_u64().map(|n| n.to_string()))
        .or_else(|| string(&status["mid"]))
        .filter(|id| kit::numeric(id, 20))
}

/// The id in links (`Aj8Dnpj3b`).
fn short_id(status: &Value, id: &str) -> Option<String> {
    string(&status["mblogid"]).or_else(|| mid_to_mblogid(id))
}

fn author(status: &Value) -> (Option<String>, Option<String>) {
    let user = &status["user"];
    (
        string(&user["idstr"]).or_else(|| user["id"].as_u64().map(|n| n.to_string())),
        string(&user["screen_name"]),
    )
}

/// The author's words: the full text when it was fetched, else the plain text the list
/// carries, else the markup stripped.
fn own_text(status: &Value, long: Option<&str>) -> String {
    if let Some(long) = long.filter(|t| !t.trim().is_empty()) {
        return text::plain(long);
    }
    match string(&status["text_raw"]) {
        Some(raw) => text::tidy(&raw),
        None => text::plain(status["text"].as_str().unwrap_or("")),
    }
}

fn is_article(status: &Value) -> bool {
    status["page_info"]["object_type"].as_str() == Some("article")
}

/// What the list shows about a status before it is observed in full.
pub(crate) fn entry(status: &Value) -> Option<EnumeratedEntry> {
    let id = id_of(status)?;
    let body = own_text(status, None);
    let assets = media::assets(status);
    let count_of = |role: &str| assets.iter().filter(|a| a.role == role).count() as u32;
    let mut kinds = vec!["text".to_owned()];
    for (role, kind) in [
        ("image", "image"),
        ("motion", "live_photo"),
        ("video", "video"),
    ] {
        if count_of(role) > 0 {
            kinds.push(kind.into());
        }
    }
    let cover = assets
        .iter()
        .find(|a| a.role == "cover")
        .or_else(|| assets.iter().find(|a| a.role == "image"))
        .and_then(|a| a.variants.last())
        .and_then(|v| v.urls.first().cloned());
    Some(EnumeratedEntry {
        native_entry_key: id,
        native_kind: KIND.into(),
        title: text::headline(&body, TITLE_MAX),
        published_at: status["created_at"].as_str().and_then(clock::parse_ctime),
        published_precision: if status["created_at"].is_string() {
            "source"
        } else {
            "unknown"
        }
        .into(),
        published_margin_seconds: None,
        pinned: status["isTop"] == 1,
        summary_body: Some(body.chars().take(SUMMARY_MAX).collect()),
        stats: stats(status),
        hints: Some(EntryHints {
            content_kinds: kinds,
            image_count: count_of("image"),
            video_count: count_of("video"),
            duration_ms: assets.iter().filter_map(|a| a.duration_ms).max(),
            cover_url: cover,
        }),
    })
}

fn stats(status: &Value) -> Value {
    json!({
        "shares": count(&status["reposts_count"]),
        "comments": count(&status["comments_count"]),
        "likes": count(&status["attitudes_count"]),
    })
}

/// The `发布于 北京` text without its prefix.
fn region(status: &Value) -> Option<String> {
    let text = string(&status["region_name"])?;
    Some(text.trim_start_matches("发布于").trim().to_owned()).filter(|t| !t.is_empty())
}

fn visibility(status: &Value) -> Option<String> {
    match status["visible"]["type"].as_i64()? {
        0 => Some("public"),
        1 => Some("private"),
        6 => Some("friends"),
        10 => Some("followers"),
        _ => None,
    }
    .map(str::to_owned)
}

/// One status in full. `long` is the full text when the list text was cut short;
/// `endpoint` names what the status came from.
pub(crate) fn work(
    status: &Value,
    long: Option<&str>,
    endpoint: &str,
) -> Result<Work, PluginError> {
    let id = id_of(status).ok_or_else(|| kit::schema("status has no numeric id"))?;
    let (author_key, author_name) = author(status);
    let mut work = Work::new(&id, KIND);
    work.author_key = author_key.ok_or_else(|| kit::schema("status has no author id"))?;
    work.author_name = author_name;
    let own = own_text(status, long);
    let mut body = own.clone();
    let mut relation = Relation::Original;
    let mut related = None;
    if let Some(original) = status["retweeted_status"].as_object() {
        let original = Value::Object(original.clone());
        let (_, name) = author(&original);
        let quoted = own_text(&original, None);
        let who = name.map_or(String::new(), |n| format!(" @{n}"));
        body = text::tidy(&format!("{own}\n\n转发自{who}：{quoted}"));
        relation = Relation::Repost;
        related = id_of(&original);
    } else {
        work.assets = media::assets(status);
    }
    if status["isLongText"] == true && long.is_none() {
        work.omissions.push("longtext_unavailable");
    }
    if is_article(status) {
        let page = &status["page_info"];
        let title = string(&page["page_title"]).unwrap_or_default();
        body = text::tidy(&format!("{body}\n[头条文章] {title}"));
        work.omissions.push("article_summary_only");
    }
    work.title = text::headline(if own.is_empty() { &body } else { &own }, TITLE_MAX);
    if work.title.is_empty() {
        work.title = "微博".into();
    }
    work.published(status["created_at"].as_str().and_then(clock::parse_ctime));
    work.stats = stats(status);
    work.media_headers = crate::api::media_headers();
    let mut tags = text::topics(&body);
    for topic in status["topic_struct"].as_array().into_iter().flatten() {
        tags.extend(string(&topic["topic_title"]));
    }
    let short = short_id(status, &id);
    work.unified.canonical_url =
        short.map(|s| format!("https://weibo.com/{}/{s}", work.author_key));
    work.unified.relation = relation;
    work.unified.related_key = related;
    work.unified.tags = tags;
    work.unified.mentions = text::mentions(&body);
    work.unified.client = status["source"].as_str().map(text::plain);
    work.unified.ip_region = region(status);
    work.unified.visibility = visibility(status);
    work.unified.pinned = status["isTop"] == 1;
    work.unified.edited_at = status["edit_at"].as_str().and_then(clock::parse_ctime);
    work.body = body;
    work.metadata = json!({
        "mblogid": status["mblogid"],
        "edit_count": status["edit_count"],
        "is_long_text": status["isLongText"],
        "source": status["source"],
        "region_name": status["region_name"],
    });
    work.evidence = json!({"source": endpoint, "long_text": long.is_some()});
    Ok(work)
}

/// Every status of a list page that has an identity, in the order the site gave them.
pub(crate) fn entries(list: &[Value]) -> Vec<EnumeratedEntry> {
    list.iter().filter_map(entry).collect()
}
