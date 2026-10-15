//! The tool's description of a video, turned into candidates for the video and soundtrack
//! resources the adapter declared. Only addresses on the site's media hosts are offered.
use crate::error::CoreError;
use serde_json::{Value, json};
use trace_contracts::plugin::{MediaCandidate, ResourceIdentityDto, UrlSet};

const FORMATS_MAX: usize = 400;
const URL_MAX: usize = 8192;
const MEDIA_HOST: &str = "googlevideo.com";
/// Headers a media download may carry; everything else the tool lists is dropped.
const HEADERS: [&str; 4] = ["user-agent", "accept", "accept-language", "referer"];

/// The size of the ranges a media address is fetched in. The video host slows a single long
/// request to a trickle but serves ranges of this size at full speed, as the tool itself does.
const RANGE_BYTES: u64 = 10 << 20;

pub(crate) fn ranged_chunk(url: &str) -> Option<u64> {
    let parsed = url::Url::parse(url).ok()?;
    let host = parsed.host_str()?;
    (host == MEDIA_HOST || host.ends_with(&format!(".{MEDIA_HOST}"))).then_some(RANGE_BYTES)
}

/// A YouTube video id: eleven URL-safe characters.
pub(crate) fn video_id_ok(id: &str) -> bool {
    id.len() == 11
        && id
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || matches!(b, b'-' | b'_'))
}

/// What the formats of a video amount to for the resources of its group.
pub(super) struct Offers {
    pub candidates: Vec<MediaCandidate>,
    /// The group whose soundtrack resource is not needed: its video carries sound or has none.
    pub waives_audio: Option<String>,
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
enum Shape {
    Video,
    Audio,
    Both,
}

struct Format<'a> {
    raw: &'a Value,
    shape: Shape,
    id: String,
    url: &'a str,
}

fn present(codec: &Value) -> bool {
    codec.as_str().is_some_and(|c| !c.is_empty() && c != "none")
}

fn number(value: &Value) -> Option<i64> {
    value
        .as_i64()
        .or_else(|| {
            value
                .as_f64()
                .filter(|n| n.is_finite())
                .map(|n| n.round() as i64)
        })
        .filter(|n| *n > 0)
}

fn usable(raw: &Value) -> Option<Format<'_>> {
    let url = raw["url"].as_str()?;
    let parsed = url::Url::parse(url).ok()?;
    let host = parsed.host_str()?;
    let on_media_host = host == MEDIA_HOST || host.ends_with(&format!(".{MEDIA_HOST}"));
    let plain = matches!(raw["protocol"].as_str(), Some("https" | "http"));
    if url.len() > URL_MAX
        || parsed.scheme() != "https"
        || !parsed.username().is_empty()
        || !on_media_host
        || !plain
        || raw["has_drm"] == true
        || raw["format_note"] == "storyboard"
        || raw["ext"] == "mhtml"
    {
        return None;
    }
    let id = raw["format_id"].as_str()?;
    if id.is_empty()
        || id.len() > 32
        || !id
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || matches!(b, b'-' | b'_' | b'.'))
    {
        return None;
    }
    let shape = match (present(&raw["vcodec"]), present(&raw["acodec"])) {
        (true, true) => Shape::Both,
        (true, false) => Shape::Video,
        (false, true) => Shape::Audio,
        (false, false) => return None,
    };
    if shape != Shape::Audio
        && (number(&raw["width"]).is_none() || number(&raw["height"]).is_none())
    {
        return None;
    }
    Some(Format {
        raw,
        shape,
        id: id.to_owned(),
        url,
    })
}

fn headers(raw: &Value) -> Value {
    let mut out = serde_json::Map::new();
    for (name, value) in raw["http_headers"].as_object().into_iter().flatten() {
        let lower = name.to_ascii_lowercase();
        if let Some(text) = value
            .as_str()
            .filter(|t| t.len() <= 512 && !t.chars().any(char::is_control))
            && HEADERS.contains(&lower.as_str())
        {
            out.insert(lower, json!(text));
        }
    }
    Value::Object(out)
}

fn candidate(resource: &ResourceIdentityDto, format: &Format, kind: &str) -> MediaCandidate {
    let raw = format.raw;
    let size = number(&raw["filesize"]);
    let kilobits = |value: &Value| {
        value
            .as_f64()
            .filter(|n| *n > 0.0)
            .map(|n| (n * 1000.0).round() as i64)
    };
    let spec = if format.shape == Shape::Audio {
        json!({
            "codec": raw["acodec"],
            "bitrate": kilobits(&raw["abr"]).or_else(|| kilobits(&raw["tbr"])),
            "sample_rate": number(&raw["asr"]),
            "channels": number(&raw["audio_channels"]),
            "container": raw["ext"],
        })
    } else {
        json!({
            "width": number(&raw["width"]),
            "height": number(&raw["height"]),
            "fps": raw["fps"].as_f64().filter(|n| *n > 0.0),
            "codec": raw["vcodec"],
            "bitrate": kilobits(&raw["vbr"]).or_else(|| kilobits(&raw["tbr"])),
            "quality": raw["format_note"],
            "container": raw["ext"],
        })
    };
    MediaCandidate {
        resource: resource.clone(),
        representation: format!("yt-{}", format.id),
        kind: kind.into(),
        segments: vec![UrlSet {
            primary: format.url.to_owned(),
            backups: Vec::new(),
            expected_size: size,
            expected_sha256: None,
        }],
        public_headers: headers(raw),
        expected_size: size,
        expected_sha256: None,
        claimed_spec: spec,
    }
}

/// The soundtrack of the video: the original language, preferring AAC (it muxes into MP4
/// without a second thought) and otherwise the highest bitrate.
fn best_audio<'a>(audio: &[&'a Format<'a>]) -> Option<&'a Format<'a>> {
    let language = |f: &Format| f.raw["language_preference"].as_i64().unwrap_or(0);
    let top = audio.iter().map(|f| language(f)).max()?;
    audio
        .iter()
        .filter(|f| language(f) == top)
        .max_by_key(|f| {
            let aac = f.raw["ext"] == "m4a"
                || f.raw["acodec"]
                    .as_str()
                    .is_some_and(|c| c.starts_with("mp4a"));
            let rate = f.raw["abr"]
                .as_f64()
                .or_else(|| f.raw["tbr"].as_f64())
                .unwrap_or(0.0);
            (aac, (rate * 1000.0) as i64)
        })
        .copied()
}

fn refuse(message: &str) -> CoreError {
    CoreError::InvalidInput(message.into())
}

/// Candidates for the group of one video. `all` lists every video rendition (for a choice);
/// otherwise only the best one by the usual ranking.
pub(super) fn offers(
    info: &Value,
    video_id: &str,
    resources: &[ResourceIdentityDto],
    all: bool,
    prefer_fps: bool,
) -> Result<Offers, CoreError> {
    if info["id"].as_str() != Some(video_id) {
        return Err(refuse("the media tool answered for another video"));
    }
    let live = info["live_status"].as_str();
    if info["is_live"] == true || matches!(live, Some("is_live" | "is_upcoming" | "post_live")) {
        return Err(CoreError::Unsupported(
            "live streams are not archived as videos".into(),
        ));
    }
    let listed = info["formats"].as_array().map_or(&[][..], Vec::as_slice);
    if listed.len() > FORMATS_MAX {
        return Err(refuse("too many formats"));
    }
    let mut seen = std::collections::BTreeSet::new();
    let formats: Vec<Format> = listed
        .iter()
        .filter_map(usable)
        .filter(|f| seen.insert(f.id.clone()))
        .collect();
    let with = |shape: Shape| {
        formats
            .iter()
            .filter(move |f| f.shape == shape)
            .collect::<Vec<_>>()
    };
    let (video, audio, muxed) = (with(Shape::Video), with(Shape::Audio), with(Shape::Both));
    let rv = resources.iter().find(|r| r.role == "video");
    let ra = resources.iter().find(|r| r.role == "audio");
    let Some(rv) = rv else {
        return Ok(Offers {
            candidates: Vec::new(),
            waives_audio: None,
        });
    };
    let group = rv
        .group_key
        .clone()
        .unwrap_or_else(|| rv.source_attachment_id.clone());
    let mut out = Offers {
        candidates: Vec::new(),
        waives_audio: None,
    };
    match (ra, best_audio(&audio)) {
        (Some(ra), Some(track)) if !video.is_empty() => {
            out.candidates
                .extend(video.iter().map(|f| candidate(rv, f, "dash_video")));
            out.candidates.push(candidate(ra, track, "dash_audio"));
        }
        _ if !muxed.is_empty() => {
            out.candidates
                .extend(muxed.iter().map(|f| candidate(rv, f, "muxed")));
            out.waives_audio = Some(group);
        }
        _ if !video.is_empty() => {
            // No soundtrack to be had: the video is kept as it is, and says so.
            out.candidates.extend(video.iter().map(|f| {
                let mut c = candidate(rv, f, "dash_video");
                c.claimed_spec["silent"] = json!(true);
                c
            }));
            out.waives_audio = Some(group);
        }
        _ => {
            return Err(CoreError::SourceUnavailable(
                "no downloadable video formats".into(),
            ));
        }
    }
    if !all {
        crate::upgrades::highest_candidates(&mut out.candidates, prefer_fps);
    }
    Ok(out)
}
