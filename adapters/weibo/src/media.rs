//! The pictures, live photos and videos of a Weibo status, as assets with their variants.
use serde_json::{Value, json};
use trace_site_support::{
    kit::{Asset, Variant, https, string},
    url_identity,
};

/// Picture renditions, best first; the number is the site's own order of preference.
const PICTURES: [(&str, i64); 6] = [
    ("largest", 6),
    ("original", 5),
    ("mw2000", 4),
    ("large", 3),
    ("bmiddle", 2),
    ("thumbnail", 1),
];

/// Video renditions the web client knows, in the order of preference it implies.
const VIDEOS: [(&str, &str, i64); 7] = [
    ("mp4_720p_mp4", "mp4-720p", 6),
    ("mp4_hd_mp4", "mp4-hd", 5),
    ("mp4_hd_url", "mp4-hd", 5),
    ("stream_url_hd", "stream-hd", 4),
    ("mp4_sd_url", "mp4-sd", 3),
    ("mp4_ld_mp4", "mp4-ld", 2),
    ("stream_url", "stream", 1),
];

fn dimension(v: &Value) -> Option<i64> {
    v.as_i64()
        .or_else(|| v.as_str().and_then(|s| s.parse().ok()))
        .filter(|n| *n > 0)
}

/// A live photo's motion part hides behind a player address that carries the real file as
/// a query value; the file itself lives on the picture host.
fn motion_address(raw: &str) -> Option<String> {
    let url = url::Url::parse(raw).ok()?;
    let inner = url
        .query_pairs()
        .find(|(k, _)| k == "livephoto")
        .map(|(_, v)| v.into_owned());
    https(inner.as_deref().unwrap_or(raw))
}

/// One picture of `pic_infos`: the still and, for a live photo or a GIF, its motion part.
pub(crate) fn picture(id: &str, info: &Value) -> Vec<Asset> {
    let variants: Vec<Variant> = PICTURES
        .iter()
        .filter_map(|(name, rank)| {
            let part = &info[*name];
            let url = https(part["url"].as_str()?)?;
            Some(Variant::new("image", name, vec![url]).spec(json!({
                "width": dimension(&part["width"]),
                "height": dimension(&part["height"]),
                "rank": rank,
            })))
        })
        .collect();
    let mut out = Vec::new();
    if !variants.is_empty() {
        out.push(Asset::new("image", id, variants));
    }
    let motion = matches!(info["type"].as_str(), Some("livephoto" | "gif"))
        .then(|| info["video"].as_str().and_then(motion_address))
        .flatten();
    if let Some(url) = motion {
        out.push(
            Asset::new(
                "motion",
                &format!("{id}-motion"),
                vec![Variant::new("muxed", "motion", vec![url])],
            )
            .caption("实况片段"),
        );
    }
    out
}

/// The renditions of one video, from the status's `page_info` or a mixed-media item. The same
/// file offered under two names counts once, at its better name.
fn video_variants(info: &Value) -> Vec<Variant> {
    let sources = [&info["media_info"], &info["urls"], info];
    let mut found: Vec<(String, Variant)> = Vec::new();
    for (key, label, rank) in VIDEOS {
        for source in sources {
            let Some(url) = source[key].as_str().and_then(https) else {
                continue;
            };
            let identity = url_identity(&url).unwrap_or_else(|| url.clone());
            let variant = Variant::new("muxed", label, vec![url]).spec(json!({"rank": rank}));
            match found.iter_mut().find(|(known, _)| *known == identity) {
                Some((_, old)) if old.spec["rank"].as_i64() < Some(rank) => *old = variant,
                Some(_) => {}
                None => found.push((identity, variant)),
            }
        }
    }
    found.into_iter().map(|(_, v)| v).collect()
}

/// A video and its cover. `id` is the site's media id when it has one.
pub(crate) fn video(info: &Value) -> Vec<Asset> {
    let variants = video_variants(info);
    if variants.is_empty() {
        return Vec::new();
    }
    let id = string(&info["media_info"]["media_id"])
        .or_else(|| string(&info["media_id"]))
        .or_else(|| string(&info["object_id"]))
        .or_else(|| string(&info["id"]))
        .unwrap_or_default();
    let seconds = info["media_info"]["duration"]
        .as_f64()
        .or_else(|| info["duration"].as_f64())
        .filter(|n| n.is_finite() && *n > 0.0);
    let mut out =
        vec![Asset::new("video", &id, variants).duration(seconds.map(|s| (s * 1000.0) as i64))];
    let cover = info["page_pic"]["url"]
        .as_str()
        .or_else(|| info["page_pic"].as_str())
        .or_else(|| info["pic_info"]["pic_big"]["url"].as_str())
        .and_then(https);
    if let Some(url) = cover {
        let cover_id = if id.is_empty() {
            String::new()
        } else {
            format!("{id}-cover")
        };
        out.push(Asset::new(
            "cover",
            &cover_id,
            vec![Variant::new("cover", "cover", vec![url])],
        ));
    }
    out
}

/// Whether `page_info` is a playable video (a web article or a link card is not).
pub(crate) fn is_video(page: &Value) -> bool {
    matches!(page["object_type"].as_str(), Some("video")) || page["type"].as_str() == Some("video")
}

/// Every asset of a status, in the order the author arranged them.
pub(crate) fn assets(status: &Value) -> Vec<Asset> {
    let mut out = Vec::new();
    if let Some(items) = status["mix_media_info"]["items"]
        .as_array()
        .filter(|a| !a.is_empty())
    {
        for item in items {
            let data = &item["data"];
            match item["type"].as_str() {
                Some("pic") => {
                    let id = string(&item["id"]).or_else(|| string(&data["pic_id"]));
                    out.extend(picture(id.as_deref().unwrap_or_default(), data));
                }
                Some("video") => out.extend(video(data)),
                _ => {}
            }
        }
        return out;
    }
    let infos = &status["pic_infos"];
    let ids: Vec<String> = match status["pic_ids"].as_array() {
        Some(ids) => ids.iter().filter_map(string).collect(),
        None => infos
            .as_object()
            .map(|o| o.keys().cloned().collect())
            .unwrap_or_default(),
    };
    for id in ids {
        if infos[&id].is_object() {
            out.extend(picture(&id, &infos[&id]));
        }
    }
    if is_video(&status["page_info"]) {
        out.extend(video(&status["page_info"]));
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_picture_offers_its_renditions_and_a_live_photo_adds_its_motion_part() {
        let info = json!({
            "type": "livephoto",
            "largest": {"url": "https://wx1.sinaimg.cn/large/a.jpg", "width": 4032, "height": 3024},
            "thumbnail": {"url": "https://wx1.sinaimg.cn/thumbnail/a.jpg", "width": "180", "height": "135"},
            "video": "https://video.weibo.com/media/play?livephoto=https%3A%2F%2Flivephoto.us.sinaimg.cn%2F0001.mov&x=1"
        });
        let assets = picture("pic1", &info);
        assert_eq!(assets.len(), 2);
        assert_eq!(assets[0].role, "image");
        assert_eq!(assets[0].variants.len(), 2);
        assert_eq!(assets[0].variants[0].representation, "largest");
        assert_eq!(
            assets[0].variants[1].spec["width"], 180,
            "text sizes are read as numbers"
        );
        assert_eq!(assets[1].role, "motion");
        assert_eq!(assets[1].id, "pic1-motion");
        assert_eq!(
            assets[1].variants[0].urls,
            ["https://livephoto.us.sinaimg.cn/0001.mov"],
            "the real file, not the player page"
        );
    }

    #[test]
    fn a_still_picture_has_no_motion_part_even_if_a_video_field_is_present() {
        let info = json!({"type": "pic", "large": {"url": "https://wx1.sinaimg.cn/large/a.jpg"},
                          "video": "https://video.weibo.com/x"});
        assert_eq!(picture("p", &info).len(), 1);
        assert!(picture("p", &json!({"type": "pic"})).is_empty());
    }

    #[test]
    fn one_file_under_two_names_is_offered_once_at_its_better_name() {
        let page = json!({
            "object_type": "video",
            "media_info": {
                "media_id": "1034:99", "duration": 12.5,
                "stream_url": "https://f.video.weibocdn.com/o0/a.mp4?label=mp4_ld&ssig=1",
                "mp4_hd_url": "https://f.video.weibocdn.com/o0/b.mp4?label=mp4_hd&ssig=2",
                "stream_url_hd": "https://f.video.weibocdn.com/o0/b.mp4?label=mp4_hd&ssig=3"
            },
            "urls": {"mp4_720p_mp4": "https://f.video.weibocdn.com/o0/c.mp4?label=mp4_720p"},
            "page_pic": "https://wx1.sinaimg.cn/orj480/cover.jpg"
        });
        let assets = video(&page);
        assert_eq!(assets.len(), 2);
        let names: Vec<_> = assets[0]
            .variants
            .iter()
            .map(|v| v.representation.as_str())
            .collect();
        assert_eq!(names, ["mp4-720p", "mp4-hd", "stream"]);
        assert_eq!(assets[0].id, "1034:99");
        assert_eq!(assets[0].duration_ms, Some(12_500));
        assert_eq!(assets[1].role, "cover");
        assert!(!assets[1].required);
    }

    #[test]
    fn mixed_media_keep_the_authors_order_and_pictures_come_before_a_video_otherwise() {
        let mixed = json!({"mix_media_info": {"items": [
            {"type": "video", "data": {"media_info": {"mp4_hd_url": "https://f.video.weibocdn.com/v.mp4"}}},
            {"type": "pic", "id": "p9", "data": {"large": {"url": "https://wx1.sinaimg.cn/large/p9.jpg"}}}
        ]}});
        let roles: Vec<_> = assets(&mixed).iter().map(|a| a.role).collect();
        assert_eq!(roles, ["video", "image"]);
        let plain = json!({
            "pic_ids": ["a", "b"],
            "pic_infos": {"b": {"large": {"url": "https://wx1.sinaimg.cn/large/b.jpg"}},
                          "a": {"large": {"url": "https://wx1.sinaimg.cn/large/a.jpg"}}},
            "page_info": {"object_type": "video", "media_info": {"mp4_sd_url": "https://f.video.weibocdn.com/v.mp4"}}
        });
        let list = assets(&plain);
        assert_eq!(
            list.iter().map(|a| a.id.as_str()).collect::<Vec<_>>(),
            ["a", "b", ""]
        );
    }

    #[test]
    fn a_link_card_is_not_a_video() {
        let status =
            json!({"page_info": {"object_type": "webpage", "page_url": "https://example.com"}});
        assert!(assets(&status).is_empty());
        assert!(!is_video(&json!({})));
    }
}
