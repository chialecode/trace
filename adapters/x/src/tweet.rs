//! A post of X as a list entry and as a full work.
use crate::{api, media};
use serde_json::{Value, json};
use trace_contracts::plugin::{EntryHints, EnumeratedEntry, PluginError};
use trace_contracts::unified::Relation;
use trace_site_support::kit::{self, Work, clock, count, numeric, string, text};

pub(crate) const POST: &str = "post";
const TITLE_MAX: usize = 60;
const SUMMARY_MAX: usize = 500;

/// The post itself, whether the result is wrapped by visibility results or not. A tombstone
/// (deleted, protected, unavailable) is not a post.
pub(crate) fn unwrap(result: &Value) -> Option<&Value> {
    match result["__typename"].as_str() {
        Some("Tweet") => Some(result),
        Some("TweetWithVisibilityResults") => Some(&result["tweet"]).filter(|t| t.is_object()),
        _ => None,
    }
}

pub(crate) fn id_of(tweet: &Value) -> Option<String> {
    string(&tweet["rest_id"]).filter(|id| numeric(id, 24))
}

/// `(rest_id, screen_name, name)` of the author.
pub(crate) fn author(tweet: &Value) -> (Option<String>, Option<String>, Option<String>) {
    let user = &tweet["core"]["user_results"]["result"];
    let pick = |key: &str| string(&user["core"][key]).or_else(|| string(&user["legacy"][key]));
    (string(&user["rest_id"]), pick("screen_name"), pick("name"))
}

/// The author's words with short links replaced by what they point to. A long post carries
/// its full text in `note_tweet`; the rest is cut to the range the site displays (media links
/// and the leading mentions of a reply fall outside it).
fn own_text(tweet: &Value) -> String {
    let legacy = &tweet["legacy"];
    let note = &tweet["note_tweet"]["note_tweet_results"]["result"];
    let (raw, urls) = match string(&note["text"]) {
        Some(full) => (full, &note["entity_set"]["urls"]),
        None => {
            let full = legacy["full_text"].as_str().unwrap_or("");
            let range = legacy["display_text_range"].as_array();
            let bounds = range.and_then(|r| Some((r.first()?.as_u64()?, r.get(1)?.as_u64()?)));
            let shown = match bounds {
                Some((from, to)) if to >= from => full
                    .chars()
                    .skip(from as usize)
                    .take((to - from) as usize)
                    .collect(),
                _ => full.to_owned(),
            };
            (shown, &legacy["entities"]["urls"])
        }
    };
    let mut expanded = raw;
    for entity in urls.as_array().into_iter().flatten() {
        if let (Some(short), Some(full)) = (entity["url"].as_str(), entity["expanded_url"].as_str())
        {
            expanded = expanded.replace(short, full);
        }
    }
    text::plain(&expanded)
}

fn stats(tweet: &Value) -> Value {
    let legacy = &tweet["legacy"];
    json!({
        "plays": count(&tweet["views"]["count"]),
        "likes": count(&legacy["favorite_count"]),
        "shares": count(&legacy["retweet_count"]),
        "comments": count(&legacy["reply_count"]),
        "favorites": count(&legacy["bookmark_count"]),
    })
}

fn original(tweet: &Value) -> Option<&Value> {
    unwrap(&tweet["legacy"]["retweeted_status_result"]["result"])
}

fn quoted(tweet: &Value) -> Option<&Value> {
    unwrap(&tweet["quoted_status_result"]["result"])
}

/// What the list shows about a post before it is observed in full.
pub(crate) fn entry(tweet: &Value, pinned: bool) -> Option<EnumeratedEntry> {
    let id = id_of(tweet)?;
    let legacy = &tweet["legacy"];
    let repost = original(tweet);
    let words = repost.map_or_else(|| own_text(tweet), own_text);
    // A repost is saved without the original's media (see `work`), so the list says the same.
    let (assets, _) = if repost.is_some() {
        (Vec::new(), 0)
    } else {
        media::assets(&tweet["legacy"])
    };
    let count_of = |role: &str| assets.iter().filter(|a| a.role == role).count() as u32;
    let mut kinds = vec!["text".to_owned()];
    for (role, kind) in [("image", "image"), ("video", "video")] {
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
    let created = legacy["created_at"].as_str().and_then(clock::parse_ctime);
    Some(EnumeratedEntry {
        native_entry_key: id,
        native_kind: POST.into(),
        title: text::headline(&words, TITLE_MAX),
        published_precision: if created.is_some() {
            "source"
        } else {
            "unknown"
        }
        .into(),
        published_at: created,
        published_margin_seconds: None,
        pinned,
        summary_body: Some(words.chars().take(SUMMARY_MAX).collect()),
        stats: stats(tweet),
        hints: Some(EntryHints {
            content_kinds: kinds,
            image_count: count_of("image"),
            video_count: count_of("video"),
            duration_ms: assets.iter().filter_map(|a| a.duration_ms).max(),
            cover_url: cover,
        }),
    })
}

fn handle_of(tweet: &Value) -> String {
    author(tweet).1.map_or(String::new(), |n| format!(" @{n}"))
}

/// One post in full. A repost keeps the relation and an excerpt of the original, not its
/// media; a quote keeps its own media and the relation to what it quotes.
pub(crate) fn work(tweet: &Value, endpoint: &str) -> Result<Work, PluginError> {
    let id = id_of(tweet).ok_or_else(|| kit::schema("post has no id"))?;
    let (author_key, handle, name) = author(tweet);
    let legacy = &tweet["legacy"];
    let mut work = Work::new(&id, POST);
    work.author_key = author_key.ok_or_else(|| kit::schema("post has no author"))?;
    work.author_name = name.or_else(|| handle.clone());
    let mine = own_text(tweet);
    let mut body = mine.clone();
    if let Some(shared) = original(tweet) {
        work.unified.relation = Relation::Repost;
        work.unified.related_key = id_of(shared);
        body = text::tidy(&format!(
            "转发自{}：{}",
            handle_of(shared),
            own_text(shared)
        ));
    } else {
        let (assets, missing) = media::assets(legacy);
        work.assets = assets;
        if missing > 0 {
            work.omissions.push("media_unavailable");
        }
        if let Some(target) = quoted(tweet) {
            work.unified.relation = Relation::Quote;
            work.unified.related_key = id_of(target);
            body = text::tidy(&format!(
                "{mine}\n\n引用{}：{}",
                handle_of(target),
                own_text(target)
            ));
        }
    }
    work.title = text::headline(if mine.is_empty() { &body } else { &mine }, TITLE_MAX);
    if work.title.is_empty() {
        work.title = "推文".into();
    }
    work.published(legacy["created_at"].as_str().and_then(clock::parse_ctime));
    work.stats = stats(tweet);
    work.media_headers = api::media_headers();
    work.unified.tags = legacy["entities"]["hashtags"]
        .as_array()
        .into_iter()
        .flatten()
        .filter_map(|h| string(&h["text"]))
        .collect();
    work.unified.mentions = legacy["entities"]["user_mentions"]
        .as_array()
        .into_iter()
        .flatten()
        .filter_map(|m| string(&m["screen_name"]))
        .collect();
    work.unified.canonical_url = handle.map(|h| format!("{}/{h}/status/{id}", api::ORIGIN));
    work.unified.language = string(&legacy["lang"]).filter(|l| l != "und" && l != "zxx");
    work.unified.client = string(&tweet["source"]).map(|s| text::plain(&s));
    work.unified.age_restricted = legacy["possibly_sensitive"] == true;
    work.metadata = json!({
        "conversation_id": legacy["conversation_id_str"],
        "in_reply_to": legacy["in_reply_to_status_id_str"],
        "quote_count": legacy["quote_count"],
        "edit_versions": tweet["edit_control"]["edit_tweet_ids"].as_array().map(Vec::len),
    });
    work.evidence = json!({"source": endpoint, "long_text": tweet["note_tweet"].is_object()});
    work.body = body;
    Ok(work)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn only_real_posts_are_unwrapped_and_tombstones_are_not_posts() {
        let post = json!({"__typename": "Tweet", "rest_id": "1"});
        assert!(unwrap(&post).is_some());
        let wrapped = json!({"__typename": "TweetWithVisibilityResults", "tweet": post});
        assert_eq!(id_of(unwrap(&wrapped).unwrap()).as_deref(), Some("1"));
        assert!(unwrap(&json!({"__typename": "TweetTombstone"})).is_none());
        assert!(unwrap(&json!({})).is_none());
    }
}
