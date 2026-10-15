//! A TikTok work as the site's `itemStruct` states it: the same shape in a list and on the
//! work's own page, mapped to a list entry and to a full work.
use crate::{api, media};
use serde_json::{Value, json};
use trace_contracts::plugin::{EntryHints, EnumeratedEntry, PluginError};
use trace_site_support::kit::{self, Work, clock, count, numeric, string, text};

pub(crate) const VIDEO: &str = "video";
pub(crate) const PHOTO: &str = "photo";
const TITLE_MAX: usize = 100;
const SUMMARY_MAX: usize = 500;

pub(crate) fn id_of(item: &Value) -> Option<String> {
    string(&item["id"]).filter(|id| numeric(id, 24))
}

/// A photo post has `imagePost`; everything else is a video.
pub(crate) fn kind_of(item: &Value) -> &'static str {
    if item["imagePost"]["images"].is_array() {
        PHOTO
    } else {
        VIDEO
    }
}

fn author(item: &Value) -> (Option<String>, Option<String>, Option<String>) {
    let a = &item["author"];
    (
        string(&a["secUid"]),
        string(&a["uniqueId"]),
        string(&a["nickname"]),
    )
}

/// Counters as exact numbers: the string form (`statsV2`) is the unabbreviated one.
fn stats(item: &Value) -> Value {
    let pick =
        |v2: &str, v1: &str| count(&item["statsV2"][v2]).or_else(|| count(&item["stats"][v1]));
    json!({
        "plays": pick("playCount", "playCount"),
        "likes": pick("diggCount", "diggCount"),
        "comments": pick("commentCount", "commentCount"),
        "shares": pick("shareCount", "shareCount"),
        "favorites": pick("collectCount", "collectCount"),
    })
}

fn created(item: &Value) -> Option<String> {
    count(&item["createTime"]).and_then(clock::from_unix)
}

fn assets(item: &Value, id: &str) -> Vec<kit::Asset> {
    if kind_of(item) == PHOTO {
        let post = &item["imagePost"];
        let mut out = media::pictures(id, post);
        out.extend(media::sound(id, &item["music"]));
        return out;
    }
    media::video(id, &item["video"])
}

/// What the list shows about a work before it is observed in full.
pub(crate) fn entry(item: &Value) -> Option<EnumeratedEntry> {
    let id = id_of(item)?;
    let kind = kind_of(item);
    let desc = text::tidy(item["desc"].as_str().unwrap_or(""));
    let all = assets(item, &id);
    let count_of = |role: &str| all.iter().filter(|a| a.role == role).count() as u32;
    let cover = all
        .iter()
        .find(|a| a.role == "cover")
        .or_else(|| all.iter().find(|a| a.role == "image"))
        .and_then(|a| a.variants.first())
        .and_then(|v| v.urls.first().cloned());
    let created = created(item);
    Some(EnumeratedEntry {
        native_entry_key: id,
        native_kind: kind.into(),
        title: if desc.is_empty() {
            "TikTok".into()
        } else {
            text::headline(&desc, TITLE_MAX)
        },
        published_precision: if created.is_some() {
            "source"
        } else {
            "unknown"
        }
        .into(),
        published_at: created,
        published_margin_seconds: None,
        pinned: item["isPinnedItem"] == true,
        summary_body: Some(desc.chars().take(SUMMARY_MAX).collect()),
        stats: stats(item),
        hints: Some(EntryHints {
            content_kinds: vec![if kind == PHOTO { "image" } else { "video" }.into()],
            image_count: count_of("image"),
            video_count: count_of("video"),
            duration_ms: all
                .iter()
                .filter_map(|a| a.duration_ms)
                .max()
                .filter(|_| kind == VIDEO),
            cover_url: cover,
        }),
    })
}

fn extra(item: &Value, key: &str, kind: i64) -> Vec<String> {
    item["textExtra"]
        .as_array()
        .into_iter()
        .flatten()
        .filter(|e| e["type"].as_i64() == Some(kind))
        .filter_map(|e| string(&e[key]))
        .collect()
}

/// One work in full, from the `itemStruct` of its page.
pub(crate) fn work(item: &Value, endpoint: &str) -> Result<Work, PluginError> {
    let id = id_of(item).ok_or_else(|| kit::schema("item has no id"))?;
    let kind = kind_of(item);
    let (sec_uid, unique_id, nickname) = author(item);
    let mut work = Work::new(&id, kind);
    work.author_key = sec_uid.ok_or_else(|| kit::schema("item has no author"))?;
    work.author_name = nickname.or_else(|| unique_id.clone());
    work.body = text::tidy(item["desc"].as_str().unwrap_or(""));
    work.title = if work.body.is_empty() {
        "TikTok".into()
    } else {
        text::headline(&work.body, TITLE_MAX)
    };
    work.published(created(item));
    work.stats = stats(item);
    work.assets = assets(item, &id);
    work.media_headers = api::media_headers();
    let required = if kind == PHOTO { "image" } else { "video" };
    if !work.assets.iter().any(|a| a.role == required) {
        work.omissions.push("media_unavailable");
    }
    let mut tags = extra(item, "hashtagName", 1);
    tags.extend(text::hashtags(&work.body));
    let mut seen = std::collections::BTreeSet::new();
    tags.retain(|t| seen.insert(t.clone()));
    work.unified.tags = tags;
    work.unified.mentions = extra(item, "userUniqueId", 0);
    work.unified.canonical_url = unique_id.map(|u| {
        let path = if kind == PHOTO { "photo" } else { "video" };
        format!("{}/@{u}/{path}/{id}", api::ORIGIN)
    });
    work.unified.ip_region = string(&item["locationCreated"]);
    work.unified.language = string(&item["textLanguage"]);
    work.unified.sponsored = item["isAd"] == true;
    work.unified.pinned = item["isPinnedItem"] == true;
    work.unified.visibility = Some(
        if item["privateItem"] == true {
            "private"
        } else {
            "public"
        }
        .into(),
    );
    work.metadata = json!({
        "music_title": item["music"]["title"],
        "music_author": item["music"]["authorName"],
        "duet_enabled": item["duetEnabled"],
        "stitch_enabled": item["stitchEnabled"],
    });
    work.evidence = json!({"source": endpoint});
    Ok(work)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn counters_prefer_the_exact_string_form_and_fall_back_to_numbers() {
        let item = json!({"statsV2": {"playCount": "1234567"}, "stats": {"playCount": 1_200_000, "diggCount": 9}});
        let s = stats(&item);
        assert_eq!(s["plays"], 1_234_567);
        assert_eq!(s["likes"], 9);
        assert!(s["shares"].is_null());
    }
}
