//! Community posts: the list entry, and the post in full from its own page.
use crate::api::{self, ORIGIN, find_all, text};
use crate::link::{is_channel_id, is_post_id};
use serde_json::{Value, json};
use trace_contracts::plugin::{EntryHints, EnumeratedEntry, PluginError};
use trace_contracts::unified::Relation;
use trace_domain::errors::FailureClass;
use trace_site_support::error;
use trace_site_support::kit::{self, Asset, Variant, Work, https, string, text as words};

pub(crate) const POST: &str = "post";
const TITLE_MAX: usize = 60;
const SUMMARY_MAX: usize = 500;

/// The post inside a thread renderer of a list, or the renderer itself.
pub(crate) fn renderer(thread: &Value) -> &Value {
    let inner = &thread["post"]["backstagePostRenderer"];
    if inner.is_object() { inner } else { thread }
}

/// Votes as the site shows them; `1.2K` is an abbreviation and gives nothing.
fn likes(post: &Value) -> Option<i64> {
    let shown = string(&post["voteCount"]["simpleText"])
        .or_else(|| string(&post["voteCount"]["runs"][0]["text"]))?;
    shown.replace(',', "").parse().ok()
}

/// One picture of an attachment as an asset: every size the site lists, largest preferred.
fn picture(image: &Value, index: usize) -> Option<Asset> {
    let sizes = image["image"]["thumbnails"].as_array()?;
    let variants: Vec<Variant> = sizes
        .iter()
        .filter_map(|t| {
            let url = https(t["url"].as_str()?)?;
            let (w, h) = (t["width"].as_i64(), t["height"].as_i64());
            Some(
                Variant::new(
                    "image",
                    &format!("{}x{}", w.unwrap_or(0), h.unwrap_or(0)),
                    vec![url],
                )
                .spec(kit::size_spec(w, h)),
            )
        })
        .collect();
    (!variants.is_empty()).then(|| Asset::new("image", &format!("img{index}"), variants))
}

fn pictures(post: &Value) -> Vec<Asset> {
    let attachment = &post["backstageAttachment"];
    let mut images: Vec<&Value> = Vec::new();
    if attachment["backstageImageRenderer"].is_object() {
        images.push(&attachment["backstageImageRenderer"]);
    }
    for item in attachment["postMultiImageRenderer"]["images"]
        .as_array()
        .into_iter()
        .flatten()
    {
        images.push(&item["backstageImageRenderer"]);
    }
    images
        .into_iter()
        .enumerate()
        .filter_map(|(i, image)| picture(image, i))
        .collect()
}

/// The attached video's id, if the post shares one.
fn attached_video(post: &Value) -> Option<String> {
    string(&post["backstageAttachment"]["videoRenderer"]["videoId"])
}

fn poll(post: &Value) -> Option<String> {
    let choices: Vec<String> = post["backstageAttachment"]["pollRenderer"]["choices"]
        .as_array()?
        .iter()
        .map(|c| text(&c["text"]))
        .filter(|t| !t.is_empty())
        .collect();
    (!choices.is_empty()).then(|| format!("投票：{}", choices.join(" / ")))
}

fn body(post: &Value) -> String {
    let mut parts = vec![words::tidy(&text(&post["contentText"]))];
    parts.extend(poll(post));
    parts.retain(|p| !p.is_empty());
    parts.join("\n")
}

/// A post of a list, from its thread renderer.
pub(crate) fn entry(thread: &Value) -> Option<EnumeratedEntry> {
    let post = renderer(thread);
    let id = string(&post["postId"]).filter(|id| is_post_id(id))?;
    let age = crate::entry::listed_age(&crate::api::text(&post["publishedTimeText"]));
    let text = body(post);
    let assets = pictures(post);
    let mut kinds = vec!["text".to_owned()];
    if !assets.is_empty() {
        kinds.push("image".into());
    }
    let attached = attached_video(post);
    if attached.is_some() {
        kinds.push("video".into());
    }
    let cover = assets
        .first()
        .and_then(|a| a.variants.last())
        .and_then(|v| v.urls.first().cloned());
    Some(EnumeratedEntry {
        native_entry_key: id,
        native_kind: POST.into(),
        title: if text.is_empty() {
            "社区帖子".into()
        } else {
            words::headline(&text, TITLE_MAX)
        },
        published_at: age.as_ref().map(|(at, _)| at.clone()),
        published_precision: if age.is_some() {
            "approximate"
        } else {
            "unknown"
        }
        .into(),
        published_margin_seconds: age.as_ref().map(|(_, unit)| *unit),
        pinned: post["pinnedText"].is_object(),
        summary_body: Some(text.chars().take(SUMMARY_MAX).collect()),
        stats: json!({"likes": likes(post)}),
        hints: Some(EntryHints {
            content_kinds: kinds,
            image_count: assets.len() as u32,
            video_count: u32::from(attached.is_some()),
            duration_ms: None,
            cover_url: cover,
        }),
    })
}

/// One post in full.
pub(crate) fn work(
    net: &dyn trace_site_support::runtime::Transport,
    id: &str,
) -> Result<Work, PluginError> {
    if !is_post_id(id) {
        return Err(error(
            FailureClass::InvalidPayload,
            "invalid YouTube post id",
        ));
    }
    let data = api::initial_data(&api::html(net, &format!("/post/{id}?hl=en"))?)?;
    let found = find_all(&data, &["backstagePostRenderer"])
        .into_iter()
        .map(|(_, p)| p)
        .find(|p| p["postId"].as_str() == Some(id))
        .ok_or_else(|| kit::schema("post page does not hold the post"))?;
    map(found, id)
}

fn map(post: &Value, id: &str) -> Result<Work, PluginError> {
    let author = string(&post["authorEndpoint"]["browseEndpoint"]["browseId"])
        .filter(|a| is_channel_id(a))
        .ok_or_else(|| kit::schema("post has no author channel"))?;
    let mut work = Work::new(id, POST);
    work.author_key = author;
    work.author_name = string(&post["authorText"]["runs"][0]["text"]);
    let mut body = body(post);
    work.assets = pictures(post);
    work.media_headers = api::media_headers();
    if let Some(video) = attached_video(post) {
        work.unified.relation = Relation::Quote;
        work.unified.related_key = Some(video.clone());
        let title = text(&post["backstageAttachment"]["videoRenderer"]["title"]);
        body = words::tidy(&format!("{body}\n[视频] {title} https://youtu.be/{video}"));
    }
    if let Some(kind) = post["backstageAttachment"]
        .as_object()
        .and_then(|a| a.keys().next())
        .filter(|k| {
            !matches!(
                k.as_str(),
                "backstageImageRenderer"
                    | "postMultiImageRenderer"
                    | "videoRenderer"
                    | "pollRenderer"
            )
        })
    {
        work.omissions.push("attachment_unsupported");
        work.metadata = json!({"unsupported_attachment": kind});
    }
    work.title = if body.is_empty() {
        "社区帖子".into()
    } else {
        words::headline(&body, TITLE_MAX)
    };
    work.unified.canonical_url = Some(format!("{ORIGIN}/post/{id}"));
    work.unified.tags = words::hashtags(&body);
    work.unified.pinned = post["pinnedText"].is_object();
    work.stats = json!({"likes": likes(post)});
    // The page shows how long ago, not when: the day stays unknown rather than guessed.
    work.published(None);
    work.evidence =
        json!({"source": "post.page", "published_text": text(&post["publishedTimeText"])});
    work.body = body;
    Ok(work)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_post_without_an_author_channel_is_a_changed_shape() {
        let post = json!({"postId": "UgkxAbCdEfGhIjKlMnOpQrStUvWxYz0123456789", "contentText": {"runs": [{"text": "x"}]}});
        assert_eq!(
            map(&post, "UgkxAbCdEfGhIjKlMnOpQrStUvWxYz0123456789")
                .unwrap_err()
                .class,
            FailureClass::SchemaChanged
        );
    }
}
