//! The pictures, videos and GIFs of a post, as assets with their variants.
use serde_json::{Value, json};
use trace_site_support::kit::{Asset, Variant, https, size_spec, string};

fn dimension(v: &Value) -> Option<i64> {
    v.as_i64().filter(|n| *n > 0)
}

/// `…/vid/avc1/1280x720/x.mp4` states the picture size of a rendition.
fn size_in_path(url: &str) -> (Option<i64>, Option<i64>) {
    url.split('/')
        .find_map(|part| {
            let (w, h) = part.split_once('x')?;
            Some((w.parse::<i64>().ok()?, h.parse::<i64>().ok()?))
        })
        .map_or((None, None), |(w, h)| (Some(w), Some(h)))
}

/// A picture in the sizes the picture host offers, the original first.
fn picture(item: &Value, id: &str) -> Option<Asset> {
    let base = https(item["media_url_https"].as_str()?)?;
    let (w, h) = (
        dimension(&item["original_info"]["width"]),
        dimension(&item["original_info"]["height"]),
    );
    let variants = [("orig", 4), ("large", 3), ("medium", 2), ("small", 1)]
        .into_iter()
        .map(|(name, rank)| {
            let (width, height) = if name == "orig" {
                (w, h)
            } else {
                let size = &item["sizes"][name];
                (dimension(&size["w"]), dimension(&size["h"]))
            };
            let mut spec = size_spec(width, height);
            spec["rank"] = json!(rank);
            Variant::new("image", name, vec![format!("{base}?name={name}")]).spec(spec)
        })
        .collect();
    Some(Asset::new("image", id, variants))
}

/// A video or GIF: the progressive files only (the playlist form needs segment handling).
fn video(item: &Value, id: &str) -> Vec<Asset> {
    let info = &item["video_info"];
    let variants: Vec<Variant> = info["variants"]
        .as_array()
        .into_iter()
        .flatten()
        .filter(|v| v["content_type"].as_str() == Some("video/mp4"))
        .filter_map(|v| {
            let url = https(v["url"].as_str()?)?;
            let bitrate = v["bitrate"].as_i64().unwrap_or(0);
            let (width, height) = size_in_path(&url);
            let mut spec = size_spec(width, height);
            spec["bitrate"] = json!(bitrate);
            Some(Variant::new("muxed", &format!("mp4-{bitrate}"), vec![url]).spec(spec))
        })
        .collect();
    let mut out = Vec::new();
    if !variants.is_empty() {
        let caption = if item["type"].as_str() == Some("animated_gif") {
            "GIF"
        } else {
            ""
        };
        out.push(
            Asset::new("video", id, variants)
                .duration(info["duration_millis"].as_i64())
                .caption(caption),
        );
        if let Some(url) = item["media_url_https"].as_str().and_then(https) {
            out.push(Asset::new(
                "cover",
                &format!("{id}-cover"),
                vec![Variant::new("cover", "poster", vec![url])],
            ));
        }
    }
    out
}

/// The media of a post, in the order of the post. A medium the site lists but offers no file
/// for gives nothing, and the caller records the omission.
pub(crate) fn assets(legacy: &Value) -> (Vec<Asset>, usize) {
    let items = legacy["extended_entities"]["media"]
        .as_array()
        .or_else(|| legacy["entities"]["media"].as_array());
    let mut out = Vec::new();
    let mut listed = 0usize;
    for item in items.into_iter().flatten() {
        listed += 1;
        let Some(id) = string(&item["id_str"]) else {
            continue;
        };
        match item["type"].as_str() {
            Some("photo") => out.extend(picture(item, &id)),
            Some("video" | "animated_gif") => out.extend(video(item, &id)),
            _ => {}
        }
    }
    let have = out.iter().filter(|a| a.role != "cover").count();
    (out, listed.saturating_sub(have))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rendition_sizes_come_from_the_path_when_the_site_states_them_there() {
        assert_eq!(
            size_in_path(
                "https://video.twimg.com/ext_tw_video/1/pu/vid/avc1/1280x720/a.mp4?tag=12"
            ),
            (Some(1280), Some(720))
        );
        assert_eq!(size_in_path("https://video.twimg.com/a.mp4"), (None, None));
    }
}
