//! A note of Xiaohongshu as a list entry (from a profile page) and as a full work (from a note
//! page).
use crate::link::id_ok;
use crate::{api, media};
use serde_json::{Value, json};
use trace_contracts::plugin::{EntryHints, EnumeratedEntry, PluginError};
use trace_site_support::kit::{self, Work, clock, count, https, string, text};

pub(crate) const NOTE: &str = "note";
const TITLE_MAX: usize = 60;
const SUMMARY_MAX: usize = 500;

/// A note of a profile's first screen: its card, and the token the page gave for opening it.
pub(crate) struct Card {
    pub id: String,
    pub card: Value,
    pub token: Option<String>,
}

fn field<'a>(value: &'a Value, camel: &str, snake: &str) -> Option<&'a Value> {
    value.get(camel).or_else(|| value.get(snake))
}

/// The cards of the profile page's notes tab, in page order. The site keeps one list per tab
/// and each may be nested; entries that are not notes of this user are not cards.
pub(crate) fn cards(state: &Value, user_id: &str) -> Result<Vec<Card>, PluginError> {
    let mut out = Vec::new();
    let notes = state["user"]["notes"]
        .as_array()
        .map_or(&[][..], Vec::as_slice);
    // The first list is the notes tab; the others (saved, liked) are other users' notes.
    let first = match notes.first() {
        Some(Value::Array(list)) => list.as_slice(),
        _ => notes,
    };
    for item in first {
        let card = if item["noteCard"].is_object() {
            &item["noteCard"]
        } else {
            item
        };
        let Some(id) = string(&card["noteId"])
            .or_else(|| string(&item["id"]))
            .filter(|id| id_ok(id))
        else {
            continue;
        };
        if let Some(author) = string(&card["user"]["userId"])
            && author != user_id
        {
            return Err(kit::schema("account identity mismatch"));
        }
        let token = string(&item["xsecToken"]).or_else(|| string(&card["xsecToken"]));
        out.push(Card {
            id,
            card: card.clone(),
            token,
        });
    }
    Ok(out)
}

/// The cards of the signed profile list (`/api/sns/web/v1/user_posted`). The JSON interface
/// names its fields in snake_case; a page's embedded state uses camelCase, so both are read.
pub(crate) fn api_cards(notes: &Value, user_id: &str) -> Result<Vec<Card>, PluginError> {
    let mut out = Vec::new();
    for item in notes.as_array().map_or(&[][..], Vec::as_slice) {
        let card = field(item, "noteCard", "note_card")
            .filter(|card| card.is_object())
            .unwrap_or(item);
        let Some(id) = field(card, "noteId", "note_id")
            .and_then(string)
            .or_else(|| string(&item["id"]))
            .filter(|id| id_ok(id))
        else {
            continue;
        };
        if let Some(author) =
            string(&card["user"]["userId"]).or_else(|| string(&card["user"]["user_id"]))
            && author != user_id
        {
            return Err(kit::schema("account identity mismatch"));
        }
        let token = string(&item["xsecToken"])
            .or_else(|| string(&item["xsec_token"]))
            .or_else(|| field(card, "xsecToken", "xsec_token").and_then(string));
        out.push(Card {
            id,
            card: card.clone(),
            token,
        });
    }
    Ok(out)
}

/// What the list shows about a note before it is observed in full.
pub(crate) fn entry(card: &Card) -> EnumeratedEntry {
    let c = &card.card;
    let title = field(c, "displayTitle", "display_title")
        .and_then(string)
        .unwrap_or_default();
    let video = string(&c["type"]).as_deref() == Some("video");
    let created = c["time"]
        .as_i64()
        .or_else(|| c["publish_time"].as_i64())
        .and_then(clock::from_unix_ms);
    let interactions = field(c, "interactInfo", "interact_info").unwrap_or(&Value::Null);
    EnumeratedEntry {
        native_entry_key: card.id.clone(),
        native_kind: NOTE.into(),
        title: text::headline(&title, TITLE_MAX),
        published_precision: if created.is_some() {
            "exact"
        } else {
            "unknown"
        }
        .into(),
        published_at: created,
        published_margin_seconds: None,
        pinned: interactions["sticky"] == true,
        summary_body: (!title.is_empty()).then(|| title.chars().take(SUMMARY_MAX).collect()),
        stats: json!({"likes": count(field(interactions, "likedCount", "liked_count").unwrap_or(&Value::Null))}),
        hints: Some(EntryHints {
            content_kinds: vec![if video { "video" } else { "image" }.into()],
            image_count: 0,
            video_count: 0,
            duration_ms: None,
            cover_url: c["cover"]["urlDefault"]
                .as_str()
                .or_else(|| c["cover"]["url_default"].as_str())
                .or_else(|| c["cover"]["url"].as_str())
                .or_else(|| c["cover"]["infoList"][0]["url"].as_str())
                .or_else(|| c["cover"]["info_list"][0]["url"].as_str())
                .and_then(https),
        }),
    }
}

/// The note a note page shows for `id`.
pub(crate) fn detail<'a>(state: &'a Value, id: &str) -> Option<&'a Value> {
    let note = &state["note"]["noteDetailMap"][id]["note"];
    (note.is_object() && string(&note["noteId"]).as_deref() == Some(id)).then_some(note)
}

/// The body as people read it: the site marks a topic as `#旅行[话题]#`.
fn body_of(note: &Value) -> String {
    text::tidy(
        &string(&note["desc"])
            .unwrap_or_default()
            .replace("[话题]", ""),
    )
}

fn names(list: &Value, key: &str) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    for item in list.as_array().into_iter().flatten() {
        if let Some(name) = string(&item[key])
            && !out.contains(&name)
        {
            out.push(name);
        }
    }
    out
}

/// The names under a note's `video` object, and what the entries of its `stream` hold (names of
/// the page's own fields and the kind of value, never the content).
fn video_shape(video: &Value) -> String {
    fn keys(value: &Value) -> Vec<&str> {
        let mut names: Vec<&str> = value
            .as_object()
            .into_iter()
            .flat_map(|o| o.keys().map(String::as_str))
            .filter(|k| k.len() <= 24 && k.chars().all(|c| c.is_ascii_alphanumeric() || c == '_'))
            .collect();
        names.sort_unstable();
        names.truncate(12);
        names
    }
    fn kind(value: &Value) -> String {
        match value {
            Value::Array(items) => format!(
                "array {} of [{}]",
                items.len(),
                keys(items.first().unwrap_or(&Value::Null)).join(",")
            ),
            Value::Object(_) => format!("object [{}]", keys(value).join(",")),
            Value::String(_) => "string".into(),
            Value::Number(_) => "number".into(),
            Value::Bool(_) => "bool".into(),
            Value::Null => "null".into(),
        }
    }
    let stream = &video["media"]["stream"];
    let held: Vec<String> = keys(stream)
        .into_iter()
        .take(6)
        .map(|name| format!("{name} {}", kind(&stream[name])))
        .collect();
    format!(
        "video [{}], media [{}], stream {{{}}}, mediaV2 [{}], consumer [{}]",
        keys(video).join(","),
        keys(&video["media"]).join(","),
        held.join("; "),
        keys(&video["mediaV2"]).join(","),
        keys(&video["consumer"]).join(",")
    )
}

/// One note in full.
pub(crate) fn work(note: &Value, endpoint: &str) -> Result<Work, PluginError> {
    let id = string(&note["noteId"])
        .filter(|id| id_ok(id))
        .ok_or_else(|| kit::schema("note has no id"))?;
    let user = &note["user"];
    let mut work = Work::new(&id, NOTE);
    work.author_key = string(&user["userId"]).ok_or_else(|| kit::schema("note has no author"))?;
    work.author_name = string(&user["nickname"]).or_else(|| string(&user["nickName"]));
    let body = body_of(note);
    let (assets, missing) = media::assets(note);
    work.assets = assets;
    if missing > 0 {
        work.omissions.push("media_unavailable");
    }
    work.title = string(&note["title"]).unwrap_or_else(|| text::headline(&body, TITLE_MAX));
    if work.title.is_empty() {
        work.title = "小红书笔记".into();
    }
    let created = note["time"].as_i64().and_then(clock::from_unix_ms);
    work.published(created.clone());
    work.unified.edited_at = note["lastUpdateTime"]
        .as_i64()
        .and_then(clock::from_unix_ms)
        .filter(|edited| Some(edited) != created.as_ref());
    let info = &note["interactInfo"];
    work.stats = json!({
        "likes": count(&info["likedCount"]),
        "favorites": count(&info["collectedCount"]),
        "comments": count(&info["commentCount"]),
        "shares": count(&info["shareCount"]),
    });
    work.media_headers = api::media_headers();
    let mut tags = names(&note["tagList"], "name");
    for topic in text::topics(&body) {
        if !tags.contains(&topic) {
            tags.push(topic);
        }
    }
    work.unified.tags = tags;
    work.unified.mentions = names(&note["atUserList"], "nickname");
    work.unified.ip_region = string(&note["ipLocation"]);
    work.unified.canonical_url = Some(format!("{}/explore/{id}", api::ORIGIN));
    work.metadata = json!({"note_type": note["type"]});
    work.evidence = json!({"source": endpoint});
    if missing > 0 && note["type"].as_str() == Some("video") {
        // The note is kept as a summary; the names the page does have say where the stream went.
        work.evidence["media_shape"] = json!(video_shape(&note["video"]));
    }
    work.body = body;
    Ok(work)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn only_the_first_list_is_the_notes_tab_and_a_foreign_author_is_an_identity_mismatch() {
        let user = "5a1b2c3d4e5f60718293a4b5";
        let note = |id: &str, by: &str| json!({"id": id, "xsecToken": "tokentoken1", "noteCard": {"noteId": id, "user": {"userId": by}}});
        let state = json!({"user": {"notes": [
            [note("65f0a1b2c3d4e5f607182930", user), {"id": "short"}],
            [note("65f0a1b2c3d4e5f607182931", "someone-else")]]}});
        let found = cards(&state, user).unwrap();
        assert_eq!(found.len(), 1);
        assert_eq!(found[0].token.as_deref(), Some("tokentoken1"));
        let foreign =
            json!({"user": {"notes": [[note("65f0a1b2c3d4e5f607182930", "someone-else")]]}});
        assert!(cards(&foreign, user).is_err());
        assert!(cards(&json!({"user": {}}), user).unwrap().is_empty());
    }

    #[test]
    fn a_video_note_without_a_stream_is_a_summary_that_says_what_the_page_had() {
        let note = json!({"noteId": "65f0a1b2c3d4e5f607182930", "type": "video", "user": {"userId": "u1"},
            "video": {"media": {"videoId": 1, "stream": {"h264": []}}, "consumer": {"originVideoKey": "secret/looking/value"}}});
        let kept = work(&note, "note.page").unwrap();
        assert_eq!(kept.omissions, ["media_unavailable"]);
        let shape = kept.evidence["media_shape"].as_str().unwrap();
        assert!(shape.contains("consumer [originVideoKey]"), "{shape}");
        assert!(shape.contains("h264 array 0 of []"), "{shape}");
        assert!(!shape.contains("secret"), "values are never quoted");
        let picture = json!({"noteId": "65f0a1b2c3d4e5f607182931", "type": "normal", "user": {"userId": "u1"}, "imageList": []});
        assert!(
            work(&picture, "note.page")
                .unwrap()
                .evidence
                .get("media_shape")
                .is_none()
        );
    }

    #[test]
    fn a_topic_marker_is_removed_and_a_detail_must_be_the_note_asked_for() {
        let note = json!({"desc": "去旅行 #旅行[话题]# #美食[话题]#"});
        assert_eq!(body_of(&note), "去旅行 #旅行# #美食#");
        let state = json!({"note": {"noteDetailMap": {"65f0a1b2c3d4e5f607182930": {"note": {"noteId": "65f0a1b2c3d4e5f607182930"}},
                                                      "65f0a1b2c3d4e5f607182931": {"note": {"noteId": "other"}}}}});
        assert!(detail(&state, "65f0a1b2c3d4e5f607182930").is_some());
        assert!(detail(&state, "65f0a1b2c3d4e5f607182931").is_none());
        assert!(detail(&state, "65f0a1b2c3d4e5f607182932").is_none());
    }
}
