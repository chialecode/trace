//! The pictures and renditions of a TikTok work, as assets with their variants.
use serde_json::{Value, json};
use trace_site_support::kit::{Asset, Variant, https, size_spec, string};

/// Codecs the Windows player handles without extra components rank above the others.
fn codec_rank(codec: &str) -> i64 {
    if codec.eq_ignore_ascii_case("h264") {
        2
    } else {
        1
    }
}

fn urls(list: &Value) -> Vec<String> {
    list.as_array()
        .into_iter()
        .flatten()
        .filter_map(|u| u.as_str().and_then(https))
        .collect()
}

fn dimension(v: &Value) -> Option<i64> {
    v.as_i64()
        .or_else(|| v.as_str().and_then(|s| s.parse().ok()))
        .filter(|n| *n > 0)
}

/// The renditions the player lists, then the plain play address if none of them is there.
/// The download address carries the site's mark and is used only when nothing else exists.
fn renditions(video: &Value) -> Vec<Variant> {
    let mut out = Vec::new();
    for info in video["bitrateInfo"].as_array().into_iter().flatten() {
        let play = &info["PlayAddr"];
        let list = urls(&play["UrlList"]);
        if list.is_empty() {
            continue;
        }
        let gear = string(&info["GearName"]).unwrap_or_else(|| "bitrate".into());
        let codec = string(&info["CodecType"]).unwrap_or_default();
        let name = format!("{gear}-{codec}");
        out.push(
            Variant::new("muxed", &name, list)
                .spec(json!({
                    "width": dimension(&play["Width"]),
                    "height": dimension(&play["Height"]),
                    "bitrate": info["Bitrate"].as_i64(),
                    "codec": codec,
                    "rank": codec_rank(&codec),
                }))
                .size(dimension(&play["DataSize"])),
        );
    }
    if out.is_empty() {
        for (key, name, rank) in [("playAddr", "play", 2), ("downloadAddr", "download", 1)] {
            if let Some(url) = video[key].as_str().and_then(https) {
                let spec = json!({
                    "width": dimension(&video["width"]),
                    "height": dimension(&video["height"]),
                    "rank": rank,
                });
                out.push(Variant::new("muxed", name, vec![url]).spec(spec));
            }
        }
    }
    out
}

/// The video of a work and its cover.
pub(crate) fn video(id: &str, video: &Value) -> Vec<Asset> {
    let variants = renditions(video);
    let mut out = Vec::new();
    if !variants.is_empty() {
        let seconds = dimension(&video["duration"]);
        out.push(
            Asset::new("video", &format!("video-{id}"), variants)
                .duration(seconds.map(|s| s * 1000)),
        );
    }
    let covers: Vec<Variant> = [("originCover", 3), ("cover", 2), ("dynamicCover", 1)]
        .into_iter()
        .filter_map(|(key, rank)| {
            let url = video[key].as_str().and_then(https)?;
            Some(Variant::new("cover", key, vec![url]).spec(json!({"rank": rank})))
        })
        .collect();
    if !covers.is_empty() {
        out.push(Asset::new("cover", &format!("cover-{id}"), covers));
    }
    out
}

/// The pictures of a photo post, in the order of the post.
pub(crate) fn pictures(id: &str, post: &Value) -> Vec<Asset> {
    post["images"]
        .as_array()
        .into_iter()
        .flatten()
        .enumerate()
        .filter_map(|(i, image)| {
            let list = urls(&image["imageURL"]["urlList"]);
            if list.is_empty() {
                return None;
            }
            let spec = size_spec(
                dimension(&image["imageWidth"]),
                dimension(&image["imageHeight"]),
            );
            Some(Asset::new(
                "image",
                &format!("{id}-{i}"),
                vec![Variant::new("image", "original", list).spec(spec)],
            ))
        })
        .collect()
}

/// The sound of a photo post, which is part of the post; a video carries its own.
pub(crate) fn sound(id: &str, music: &Value) -> Option<Asset> {
    let url = music["playUrl"].as_str().and_then(https)?;
    let seconds = dimension(&music["duration"]);
    let mut sound = Asset::new(
        "audio",
        &format!("sound-{id}"),
        vec![Variant::new("audio", "music", vec![url])],
    )
    .duration(seconds.map(|s| s * 1000));
    // A soundtrack the site withholds (rights) must not fail the post.
    sound.required = false;
    Some(sound)
}
