//! What a channel tab shows about one video or Short before it is opened.
use crate::api::text;
use crate::link::is_video_id;
use serde_json::{Value, json};
use trace_contracts::plugin::{EntryHints, EnumeratedEntry};
use trace_site_support::kit::clock;
use trace_site_support::kit::{string, text as words};

pub(crate) const VIDEO: &str = "video";
pub(crate) const SHORT: &str = "short";
const TITLE_MAX: usize = 100;

/// The renderers each lane's items come in: the older ones and the view-model forms the
/// site has been moving to.
pub(crate) fn keys(lane: &str) -> &'static [&'static str] {
    match lane {
        VIDEO => &["videoRenderer", "gridVideoRenderer", "lockupViewModel"],
        SHORT => &["shortsLockupViewModel", "reelItemRenderer"],
        _ => &["backstagePostThreadRenderer"],
    }
}

/// An item of a list, or the reason it is left out.
pub(crate) enum Item {
    Entry(Box<EnumeratedEntry>),
    /// Live, upcoming and members-only items cannot be archived as videos.
    Skipped(&'static str),
}

/// `1:02:03` / `12:34` as milliseconds.
pub(crate) fn duration_ms(text: &str) -> Option<i64> {
    let parts: Vec<&str> = text.trim().split(':').collect();
    if parts.len() > 3 {
        return None;
    }
    let mut total = 0i64;
    for (index, part) in parts.iter().enumerate() {
        let n: i64 = part.parse().ok().filter(|n| *n >= 0)?;
        if index > 0 && n >= 60 {
            return None;
        }
        total = total * 60 + n;
    }
    (total > 0).then_some(total * 1000)
}

/// `1,234 views` as 1234. Abbreviations (`1.2K views`) are not facts and give nothing.
pub(crate) fn view_count(text: &str) -> Option<i64> {
    let lower = text.trim().to_ascii_lowercase();
    if lower == "no views" {
        return Some(0);
    }
    let digits = lower
        .strip_suffix(" views")
        .or_else(|| lower.strip_suffix(" view"))?;
    let plain: String = digits.chars().filter(|c| *c != ',').collect();
    plain.parse().ok()
}

/// The still image a video list item stands for; the picture host serves it by video id.
pub(crate) fn cover(id: &str) -> String {
    format!("https://i.ytimg.com/vi/{id}/hqdefault.jpg")
}

fn hints(id: &str, duration: Option<i64>) -> EntryHints {
    EntryHints {
        content_kinds: vec!["video".into()],
        image_count: 0,
        video_count: 1,
        duration_ms: duration,
        cover_url: Some(cover(id)),
    }
}

/// When the list says a work appeared ("3 days ago"): an approximate moment and the unit it
/// is rounded to. The exact day comes with the details.
pub(crate) fn listed_age(text: &str) -> Option<(String, i64)> {
    clock::parse_ago(text, clock::now_unix())
}

fn entry(
    id: &str,
    kind: &str,
    title: &str,
    stats: (Option<i64>, Option<i64>),
    age: Option<(String, i64)>,
) -> Item {
    let (plays, duration) = stats;
    let (published_at, published_precision, published_margin_seconds) = match age {
        Some((at, unit)) => (Some(at), "approximate", Some(unit)),
        None => (None, "unknown", None),
    };
    Item::Entry(Box::new(EnumeratedEntry {
        native_entry_key: id.to_owned(),
        native_kind: kind.to_owned(),
        title: words::headline(title, TITLE_MAX),
        published_at,
        published_precision: published_precision.into(),
        published_margin_seconds,
        pinned: false,
        summary_body: None,
        stats: json!({"plays": plays}),
        hints: Some(hints(id, duration)),
    }))
}

fn badge_styles(renderer: &Value) -> Vec<&str> {
    let badges = renderer["badges"].as_array().into_iter().flatten();
    badges
        .filter_map(|b| b["metadataBadgeRenderer"]["style"].as_str())
        .collect()
}

fn overlay_style(renderer: &Value) -> Option<&str> {
    renderer["thumbnailOverlays"]
        .as_array()?
        .iter()
        .find_map(|o| o["thumbnailOverlayTimeStatusRenderer"]["style"].as_str())
}

fn members_only(renderer: &Value) -> bool {
    badge_styles(renderer)
        .iter()
        .any(|s| s.contains("MEMBERS_ONLY"))
        || renderer["badges"]
            .as_array()
            .into_iter()
            .flatten()
            .any(|b| {
                string(&b["metadataBadgeRenderer"]["label"])
                    .is_some_and(|l| l.eq_ignore_ascii_case("members only"))
            })
}

fn video_renderer(r: &Value) -> Item {
    let Some(id) = string(&r["videoId"]).filter(|id| is_video_id(id)) else {
        return Item::Skipped("no_id");
    };
    if r["upcomingEventData"].is_object()
        || matches!(overlay_style(r), Some("LIVE" | "UPCOMING"))
        || badge_styles(r).contains(&"BADGE_STYLE_TYPE_LIVE_NOW")
    {
        return Item::Skipped("live_or_upcoming");
    }
    if members_only(r) {
        return Item::Skipped("members_only");
    }
    let plays = view_count(&text(&r["viewCountText"]));
    let length = duration_ms(&text(&r["lengthText"]));
    let age = listed_age(&text(&r["publishedTimeText"]));
    entry(&id, VIDEO, &text(&r["title"]), (plays, length), age)
}

/// The view-model form: the id and kind are named, the title and badges are nested.
fn lockup(r: &Value) -> Item {
    if r["contentType"].as_str() != Some("LOCKUP_CONTENT_TYPE_VIDEO") {
        return Item::Skipped("not_a_video");
    }
    let Some(id) = string(&r["contentId"]).filter(|id| is_video_id(id)) else {
        return Item::Skipped("no_id");
    };
    let meta = &r["metadata"]["lockupMetadataViewModel"];
    let badges: Vec<String> = r["contentImage"]["thumbnailViewModel"]["overlays"]
        .as_array()
        .into_iter()
        .flatten()
        .filter_map(|o| o["thumbnailBottomOverlayViewModel"]["badges"].as_array())
        .flatten()
        .map(|b| text(&b["thumbnailBadgeViewModel"]["text"]))
        .collect();
    if badges
        .iter()
        .any(|b| b.eq_ignore_ascii_case("live") || b.eq_ignore_ascii_case("upcoming"))
    {
        return Item::Skipped("live_or_upcoming");
    }
    let plays = meta["metadata"]["contentMetadataViewModel"]["metadataRows"]
        .as_array()
        .into_iter()
        .flatten()
        .flat_map(|row| row["metadataParts"].as_array().into_iter().flatten())
        .find_map(|part| view_count(&text(&part["text"])));
    let length = badges.iter().find_map(|b| duration_ms(b));
    let age = meta["metadata"]["contentMetadataViewModel"]["metadataRows"]
        .as_array()
        .into_iter()
        .flatten()
        .flat_map(|row| row["metadataParts"].as_array().into_iter().flatten())
        .find_map(|part| listed_age(&text(&part["text"])));
    entry(&id, VIDEO, &text(&meta["title"]), (plays, length), age)
}

fn short_renderer(r: &Value, view_model: bool) -> Item {
    let id = if view_model {
        string(&r["onTap"]["innertubeCommand"]["reelWatchEndpoint"]["videoId"]).or_else(|| {
            string(&r["entityId"]).map(|e| e.trim_start_matches("shorts-shelf-item-").to_owned())
        })
    } else {
        string(&r["videoId"])
    };
    let Some(id) = id.filter(|id| is_video_id(id)) else {
        return Item::Skipped("no_id");
    };
    let (title, views) = if view_model {
        (
            text(&r["overlayMetadata"]["primaryText"]),
            text(&r["overlayMetadata"]["secondaryText"]),
        )
    } else {
        (text(&r["headline"]), text(&r["viewCountText"]))
    };
    // A Short's list line shows views only; its details date it.
    entry(&id, SHORT, &title, (view_count(&views), None), None)
}

/// The item of a video or Short list, whichever renderer the site used for it.
pub(crate) fn item(key: &str, renderer: &Value) -> Item {
    match key {
        "videoRenderer" | "gridVideoRenderer" => video_renderer(renderer),
        "lockupViewModel" => lockup(renderer),
        "shortsLockupViewModel" => short_renderer(renderer, true),
        "reelItemRenderer" => short_renderer(renderer, false),
        _ => Item::Skipped("unknown_renderer"),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn durations_and_counts_are_exact_or_nothing() {
        assert_eq!(duration_ms("12:34"), Some(754_000));
        assert_eq!(duration_ms("1:02:03"), Some(3_723_000));
        assert_eq!(duration_ms("0:00"), None);
        assert_eq!(duration_ms("LIVE"), None);
        assert_eq!(duration_ms("1:99"), None);
        assert_eq!(view_count("1,234 views"), Some(1234));
        assert_eq!(view_count("1 view"), Some(1));
        assert_eq!(view_count("No views"), Some(0));
        assert_eq!(view_count("1.2K views"), None);
        assert_eq!(view_count("Streamed 3 days ago"), None);
    }

    #[test]
    fn the_site_lists_a_videos_age_in_the_compact_form_and_it_still_dates_the_video() {
        let lockup = |age: &str| {
            json!({
                "contentType": "LOCKUP_CONTENT_TYPE_VIDEO",
                "contentId": "vid_AAAAAA1",
                "metadata": {"lockupMetadataViewModel": {
                    "title": {"content": "Synthetic"},
                    "metadata": {"contentMetadataViewModel": {"metadataRows": [
                        {"metadataParts": [{"text": {"content": "3.9K"}}, {"text": {"content": age}}]}
                    ]}}
                }}
            })
        };
        for (age, unit) in [
            ("9h ago", 3_600),
            ("12d ago", 86_400),
            ("2w ago", 7 * 86_400),
            ("1mo ago", 30 * 86_400),
        ] {
            let Item::Entry(entry) = item("lockupViewModel", &lockup(age)) else {
                panic!("{age}");
            };
            assert_eq!(
                (
                    entry.published_precision.as_str(),
                    entry.published_margin_seconds
                ),
                ("approximate", Some(unit)),
                "{age}"
            );
            assert!(entry.published_at.is_some(), "{age}");
        }
        let Item::Entry(entry) = item("lockupViewModel", &lockup("Premiered")) else {
            panic!()
        };
        assert_eq!(entry.published_precision, "unknown");
    }
}
