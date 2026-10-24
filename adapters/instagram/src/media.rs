//! The pictures and videos of a post, as assets with their variants.
use serde_json::{Value, json};
use trace_site_support::kit::{Asset, Variant, https, size_spec, string};

fn dimension(v: &Value) -> Option<i64> {
    v.as_i64().filter(|n| *n > 0)
}

/// The id the site gives a medium: the numeric `pk`, else the composite `id`.
fn id_of(item: &Value) -> Option<String> {
    string(&item["pk"])
        .or_else(|| item["pk"].as_i64().map(|n| n.to_string()))
        .or_else(|| string(&item["id"]))
        .map(|id| id.chars().take(64).collect())
}

/// A picture in every size the site lists, the widest first.
fn picture(item: &Value, id: &str) -> Option<Asset> {
    let mut candidates: Vec<(i64, i64, String)> = item["image_versions2"]["candidates"]
        .as_array()?
        .iter()
        .filter_map(|c| {
            Some((
                dimension(&c["width"]).unwrap_or(0),
                dimension(&c["height"]).unwrap_or(0),
                https(c["url"].as_str()?)?,
            ))
        })
        .collect();
    candidates.sort_by_key(|(w, _, _)| std::cmp::Reverse(*w));
    let variants: Vec<Variant> = candidates
        .into_iter()
        .enumerate()
        .map(|(i, (w, h, url))| {
            let mut spec = size_spec((w > 0).then_some(w), (h > 0).then_some(h));
            spec["rank"] = json!(w);
            let name = if w > 0 && h > 0 {
                format!("{w}x{h}")
            } else {
                format!("size-{i}")
            };
            Variant::new("image", &name, vec![url]).spec(spec)
        })
        .collect();
    (!variants.is_empty()).then(|| Asset::new("image", id, variants))
}

/// A video: the files the site lists, the site's own order breaking ties between equal sizes,
/// and the first frame as its cover.
fn video(item: &Value, id: &str) -> Vec<Asset> {
    let files = item["video_versions"]
        .as_array()
        .map_or(&[][..], Vec::as_slice);
    let variants: Vec<Variant> = files
        .iter()
        .enumerate()
        .filter_map(|(i, v)| {
            let url = https(v["url"].as_str()?)?;
            let (w, h) = (dimension(&v["width"]), dimension(&v["height"]));
            let mut spec = size_spec(w, h);
            spec["rank"] = json!(files.len() - i);
            let name = match (v["type"].as_i64(), w, h) {
                (Some(t), Some(w), Some(h)) => format!("v{t}-{w}x{h}"),
                _ => format!("file-{i}"),
            };
            Some(Variant::new("muxed", &name, vec![url]).spec(spec))
        })
        .collect();
    if variants.is_empty() {
        return Vec::new();
    }
    let seconds = item["video_duration"].as_f64().filter(|s| *s > 0.0);
    let mut out = vec![
        Asset::new("video", id, variants).duration(seconds.map(|s| (s * 1000.0).round() as i64)),
    ];
    if let Some(cover) = picture(item, &format!("{id}-cover")) {
        let mut cover = cover;
        cover.role = "cover";
        cover.required = false;
        cover.inline = false;
        cover.variants.truncate(1);
        for v in &mut cover.variants {
            v.kind = "cover";
        }
        out.push(cover);
    }
    out
}

/// The media of a post in the order of the post, and how many the site lists that give no
/// file (the caller records the omission).
pub(crate) fn assets(node: &Value) -> (Vec<Asset>, usize) {
    let children = node["carousel_media"].as_array().filter(|c| !c.is_empty());
    let items: Vec<&Value> = match children {
        Some(list) => list.iter().collect(),
        None => vec![node],
    };
    let mut out = Vec::new();
    let mut missing = 0usize;
    for item in items {
        let Some(id) = id_of(item) else {
            missing += 1;
            continue;
        };
        let is_video = item["media_type"].as_i64() == Some(2)
            || (item["media_type"].is_null() && item["video_versions"].is_array());
        let made = if is_video {
            video(item, &id)
        } else {
            picture(item, &id).into_iter().collect()
        };
        if made.is_empty() {
            missing += 1;
        }
        out.extend(made);
    }
    (out, missing)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_picture_lists_its_sizes_widest_first_and_a_video_keeps_the_sites_order_for_ties() {
        let image = json!({"pk": "1", "media_type": 1, "image_versions2": {"candidates": [
            {"url": "https://scontent.cdninstagram.com/a_small.jpg", "width": 320, "height": 320},
            {"url": "https://scontent.cdninstagram.com/a_big.jpg", "width": 1080, "height": 1080},
            {"url": "http://scontent.cdninstagram.com/a_mid.jpg", "width": 640, "height": 640},
            {"url": "javascript:alert(1)", "width": 99, "height": 99}]}});
        let (found, missing) = assets(&image);
        assert_eq!(missing, 0);
        let names: Vec<_> = found[0]
            .variants
            .iter()
            .map(|v| v.representation.as_str())
            .collect();
        assert_eq!(names, ["1080x1080", "640x640", "320x320"]);
        assert!(found[0].variants[1].urls[0].starts_with("https://"));

        let clip = json!({"pk": 2, "media_type": 2, "video_duration": 12.5,
            "video_versions": [
                {"url": "https://v.cdninstagram.com/hi.mp4", "width": 720, "height": 1280, "type": 101},
                {"url": "https://v.cdninstagram.com/lo.mp4", "width": 720, "height": 1280, "type": 102}],
            "image_versions2": {"candidates": [
                {"url": "https://scontent.cdninstagram.com/f1.jpg", "width": 720, "height": 1280},
                {"url": "https://scontent.cdninstagram.com/f2.jpg", "width": 360, "height": 640}]}});
        let (found, missing) = assets(&clip);
        assert_eq!(missing, 0);
        let roles: Vec<_> = found
            .iter()
            .map(|a| (a.role, a.required, a.id.as_str()))
            .collect();
        assert_eq!(roles, [("video", true, "2"), ("cover", false, "2-cover")]);
        assert_eq!(found[0].duration_ms, Some(12_500));
        assert_eq!(found[0].variants[0].spec["rank"], 2);
        assert_eq!(found[0].variants[1].spec["rank"], 1);
        assert_eq!(found[1].variants.len(), 1);
        assert_eq!(found[1].variants[0].kind, "cover");
    }

    #[test]
    fn a_carousel_is_its_children_and_a_child_without_a_file_is_counted() {
        let node = json!({"pk": "9", "media_type": 8, "carousel_media": [
            {"pk": "91", "media_type": 1, "image_versions2": {"candidates": [
                {"url": "https://scontent.cdninstagram.com/c1.jpg", "width": 1080, "height": 1350}]}},
            {"pk": "92", "media_type": 2, "video_versions": []},
            {"media_type": 1}]});
        let (found, missing) = assets(&node);
        assert_eq!(found.len(), 1);
        assert_eq!(found[0].id, "91");
        assert_eq!(missing, 2);
    }
}
