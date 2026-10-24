//! One video or Short in full, from its watch page. The file itself is not here: the video
//! asset carries no download address, Core's media tool resolves it.
use crate::api::{self, ORIGIN};
use crate::entry::{SHORT, VIDEO, cover};
use crate::link::{is_channel_id, is_video_id};
use serde_json::{Value, json};
use trace_contracts::plugin::PluginError;
use trace_domain::errors::FailureClass;
use trace_site_support::error;
use trace_site_support::kit::{self, Asset, Variant, Work, clock, count, https, string, text};
use trace_site_support::runtime::Transport;

const TITLE_MAX: usize = 100;

/// Whether the site lets a visitor play the video, and if not, why that is not "gone".
fn playability(player: &Value) -> Result<(), PluginError> {
    let status = &player["playabilityStatus"];
    let reason = string(&status["reason"])
        .unwrap_or_default()
        .to_ascii_lowercase();
    match status["status"].as_str() {
        Some("OK") => Ok(()),
        Some("LOGIN_REQUIRED")
            if reason.contains("not a bot") || reason.contains("unusual traffic") =>
        {
            Err(kit::limited(
                "YouTube asks the visitor to prove they are human",
            ))
        }
        Some("LOGIN_REQUIRED")
            if reason.contains("your age")
                || reason.contains("age-restricted")
                || reason.contains("inappropriate") =>
        {
            Err(kit::denied("the video is age-restricted"))
        }
        Some("LOGIN_REQUIRED") => Err(kit::login_needed("the video needs a sign-in")),
        Some("UNPLAYABLE") => Err(kit::denied("the video is not available to this visitor")),
        Some("ERROR") => Err(error(
            FailureClass::SourceUnavailable,
            "the video is unavailable",
        )),
        Some("LIVE_STREAM_OFFLINE") => Err(error(
            FailureClass::Unsupported,
            "live streams are not archived as videos",
        )),
        _ => Err(kit::schema("watch page has no playability status")),
    }
}

/// The still pictures the player response lists, as a cover with every size.
fn thumbnail(id: &str, details: &Value) -> Asset {
    let mut variants: Vec<Variant> = details["thumbnail"]["thumbnails"]
        .as_array()
        .into_iter()
        .flatten()
        .filter_map(|t| {
            let url = https(t["url"].as_str()?)?;
            let (w, h) = (t["width"].as_i64(), t["height"].as_i64());
            // The picture host puts a signature in the query of some sizes; the address is
            // used as given, never stored.
            Some(
                Variant::new(
                    "cover",
                    &format!("{}x{}", w.unwrap_or(0), h.unwrap_or(0)),
                    vec![url],
                )
                .spec(kit::size_spec(w, h)),
            )
        })
        .collect();
    if variants.is_empty() {
        variants.push(Variant::new("cover", "hqdefault", vec![cover(id)]));
    }
    Asset::new("cover", &format!("cover-{id}"), variants)
}

/// The day (or the instant) the site gives as the publication time.
fn published(micro: &Value) -> (Option<String>, &'static str) {
    for key in ["publishDate", "uploadDate"] {
        let Some(raw) = string(&micro[key]) else {
            continue;
        };
        if let Some(at) = clock::parse_iso(&raw) {
            return (Some(at), "source");
        }
        if let Some(day) = clock::parse_date(&raw) {
            return (Some(day), "day");
        }
    }
    (None, "unknown")
}

pub(crate) fn work(net: &dyn Transport, id: &str, kind: &str) -> Result<Work, PluginError> {
    if !is_video_id(id) || !matches!(kind, VIDEO | SHORT) {
        return Err(error(FailureClass::InvalidPayload, "invalid YouTube video"));
    }
    let page = api::html(net, &format!("/watch?v={id}&hl=en"))?;
    let player = api::player_response(&page)?;
    playability(&player)?;
    map(&player, id, kind)
}

fn map(player: &Value, id: &str, kind: &str) -> Result<Work, PluginError> {
    let details = &player["videoDetails"];
    if details["videoId"].as_str() != Some(id) {
        return Err(kit::schema("watch page is for another video"));
    }
    if details["isLive"] == true || details["isUpcoming"] == true {
        return Err(error(
            FailureClass::Unsupported,
            "live streams are not archived as videos",
        ));
    }
    let micro = &player["microformat"]["playerMicroformatRenderer"];
    let author = string(&details["channelId"])
        .filter(|c| is_channel_id(c))
        .ok_or_else(|| kit::schema("video has no channel"))?;
    let mut work = Work::new(id, kind);
    work.author_key = author;
    work.author_name = string(&details["author"]);
    work.title = text::headline(&string(&details["title"]).unwrap_or_default(), TITLE_MAX);
    if work.title.is_empty() {
        return Err(kit::schema("video has no title"));
    }
    work.body = text::tidy(&string(&details["shortDescription"]).unwrap_or_default());
    let (at, precision) = published(micro);
    work.published(at);
    if work.published_at.is_some() {
        work.precision = precision;
    }
    work.media_headers = api::media_headers();
    let seconds = count(&details["lengthSeconds"]).filter(|s| *s > 0);
    work.assets.push(thumbnail(id, details));
    // No variants: the media tool of the host answers for these resources at download time.
    // The soundtrack is its own resource because YouTube serves the best video without sound.
    let group = format!("media-{id}");
    work.assets.push(
        Asset::new("video", &format!("video-{id}"), Vec::new())
            .duration(seconds.map(|s| s * 1000))
            .group(&group),
    );
    work.assets
        .push(Asset::new("audio", &format!("audio-{id}"), Vec::new()).group(&group));
    work.external_media = Some("yt_dlp");
    work.stats = json!({"plays": count(&details["viewCount"])});
    let mut tags: Vec<String> = details["keywords"]
        .as_array()
        .into_iter()
        .flatten()
        .filter_map(string)
        .collect();
    tags.extend(text::hashtags(&work.body));
    work.unified.tags = tags;
    work.unified.canonical_url = Some(if kind == SHORT {
        format!("{ORIGIN}/shorts/{id}")
    } else {
        format!("https://youtu.be/{id}")
    });
    work.unified.category = string(&micro["category"]);
    work.unified.visibility = Some(
        if micro["isUnlisted"] == true {
            "unlisted"
        } else {
            "public"
        }
        .into(),
    );
    let languages: Vec<Value> =
        player["captions"]["playerCaptionsTracklistRenderer"]["captionTracks"]
            .as_array()
            .into_iter()
            .flatten()
            .filter_map(|t| {
                t["languageCode"]
                    .as_str()
                    .map(|l| json!({"language": l, "auto": t["kind"] == "asr"}))
            })
            .collect();
    work.metadata = json!({"caption_tracks": languages});
    work.evidence = json!({"source": "watch.page", "media": "external_tool"});
    Ok(work)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn what_the_site_says_about_playability_is_not_content_state() {
        let status =
            |s: &str, reason: &str| json!({"playabilityStatus": {"status": s, "reason": reason}});
        assert!(playability(&status("OK", "")).is_ok());
        let class = |p: Value| playability(&p).unwrap_err().class;
        assert_eq!(
            class(status(
                "LOGIN_REQUIRED",
                "Sign in to confirm you're not a bot"
            )),
            FailureClass::RateLimited
        );
        assert_eq!(
            class(status("LOGIN_REQUIRED", "Sign in to confirm your age")),
            FailureClass::PermissionDenied
        );
        assert_eq!(
            class(status("LOGIN_REQUIRED", "")),
            FailureClass::AuthRequired
        );
        assert_eq!(
            class(status("UNPLAYABLE", "Members-only content")),
            FailureClass::PermissionDenied
        );
        assert_eq!(
            class(status("ERROR", "Video unavailable")),
            FailureClass::SourceUnavailable
        );
        assert_eq!(class(json!({})), FailureClass::SchemaChanged);
    }
}
