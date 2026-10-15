//! The pictures, live photos and videos of a note, as assets with their variants.
use serde_json::{Value, json};
use trace_site_support::kit::{Asset, Variant, https, size_spec, string};

/// How many groups of streams and how many entries in a group are read.
const GROUPS_MAX: usize = 8;
const ENTRIES_MAX: usize = 8;

fn dimension(v: &Value) -> Option<i64> {
    v.as_i64().filter(|n| *n > 0)
}

/// A picture: the default rendition and the other scenes the site lists for it.
fn picture(item: &Value) -> Option<Asset> {
    let (w, h) = (dimension(&item["width"]), dimension(&item["height"]));
    let mut variants = Vec::new();
    let mut seen: Vec<String> = Vec::new();
    let mut add = |name: &str, url: Option<&str>, rank: i64| {
        if let Some(url) = url.and_then(https)
            && !seen.contains(&url)
        {
            let mut spec = size_spec(w, h);
            spec["rank"] = json!(rank);
            seen.push(url.clone());
            variants.push(Variant::new("image", name, vec![url]).spec(spec));
        }
    };
    add("default", item["urlDefault"].as_str(), 3);
    for scene in item["infoList"].as_array().into_iter().flatten() {
        let name = scene["imageScene"].as_str().unwrap_or("scene");
        add(name, scene["url"].as_str(), 2);
    }
    add("preview", item["urlPre"].as_str(), 1);
    let id = string(&item["fileId"])
        .or_else(|| string(&item["traceId"]))
        .unwrap_or_default();
    (!variants.is_empty()).then(|| Asset::new("image", &id, variants))
}

/// The codec a stream entry names, in the words the other sites use; the page's own name for
/// the group when the entry names none.
fn codec_of(entry: &Value, group: &str) -> String {
    let named = string(&entry["videoCodec"])
        .or_else(|| string(&entry["codec"]))
        .unwrap_or_else(|| group.to_owned())
        .to_ascii_lowercase();
    if named.contains("264") || named.contains("avc") {
        "h264".into()
    } else if named.contains("265") || named.contains("hevc") || named.contains("hvc") {
        "h265".into()
    } else if named.contains("av1") || named.contains("av01") {
        "av1".into()
    } else {
        named
            .chars()
            .filter(char::is_ascii_alphanumeric)
            .take(12)
            .collect()
    }
}

/// The progressive streams of a video or of a live photo, one variant per codec and size. The
/// page groups them under the codec's name (`h264`) or under a name of its own choosing; each
/// entry says what it is, so every group is read.
fn streams(stream: &Value) -> (Vec<Variant>, Option<i64>) {
    let mut variants = Vec::new();
    let mut duration = None;
    let Some(groups) = stream.as_object() else {
        return (variants, duration);
    };
    let mut names: Vec<&String> = groups.keys().take(GROUPS_MAX).collect();
    names.sort_by_key(|name| {
        ["h264", "h265", "av1"]
            .iter()
            .position(|known| known == name)
            .unwrap_or(usize::MAX)
    });
    for group in names {
        for (i, s) in groups[group]
            .as_array()
            .into_iter()
            .flatten()
            .take(ENTRIES_MAX)
            .enumerate()
        {
            let mut urls: Vec<String> = s["masterUrl"]
                .as_str()
                .and_then(https)
                .into_iter()
                .collect();
            urls.extend(
                s["backupUrls"]
                    .as_array()
                    .into_iter()
                    .flatten()
                    .filter_map(|u| u.as_str().and_then(https)),
            );
            if urls.is_empty() {
                continue;
            }
            let codec = codec_of(s, group);
            let (w, h) = (dimension(&s["width"]), dimension(&s["height"]));
            let mut spec = size_spec(w, h);
            spec["codec"] = json!(codec);
            spec["bitrate"] = json!(dimension(&s["avgBitrate"]).unwrap_or(0));
            spec["fps"] = s["fps"].clone();
            let name = match (w, h) {
                (Some(w), Some(h)) => format!("{codec}-{w}x{h}"),
                _ => format!("{codec}-{i}"),
            };
            if variants.iter().any(|v: &Variant| v.representation == name) {
                continue;
            }
            duration = duration.max(dimension(&s["duration"]));
            variants.push(
                Variant::new("muxed", &name, urls)
                    .spec(spec)
                    .size(dimension(&s["size"])),
            );
        }
    }
    (variants, duration)
}

fn video(note: &Value) -> (Vec<Asset>, usize) {
    let (variants, ms) = streams(&note["video"]["media"]["stream"]);
    if variants.is_empty() {
        return (Vec::new(), 1);
    }
    let seconds = dimension(&note["video"]["capa"]["duration"]).map(|s| s * 1000);
    let video_id = &note["video"]["media"]["videoId"];
    let id = string(video_id)
        .or_else(|| video_id.as_i64().map(|n| n.to_string()))
        .unwrap_or_default();
    let mut out = vec![Asset::new("video", &id, variants).duration(ms.or(seconds))];
    // The first picture of a video note is its cover.
    if let Some(cover) = note["imageList"]
        .as_array()
        .and_then(|l| l.first())
        .and_then(picture)
    {
        let mut cover = Asset::new(
            "cover",
            &cover.id,
            cover
                .variants
                .into_iter()
                .take(1)
                .map(|mut v| {
                    v.kind = "cover";
                    v
                })
                .collect(),
        );
        cover.id = format!("{}-cover", cover.id);
        out.push(cover);
    }
    (out, 0)
}

fn pictures(note: &Value) -> (Vec<Asset>, usize) {
    let mut out = Vec::new();
    let mut missing = 0;
    for item in note["imageList"].as_array().into_iter().flatten() {
        let Some(asset) = picture(item) else {
            missing += 1;
            continue;
        };
        let live = item["livePhoto"] == true;
        let id = asset.id.clone();
        out.push(asset);
        if live {
            let (variants, ms) = streams(&item["stream"]);
            if variants.is_empty() {
                missing += 1;
            } else {
                out.push(Asset::new("motion", &format!("{id}-motion"), variants).duration(ms));
            }
        }
    }
    (out, missing)
}

/// The media of a note in the order of the note, and how many the site lists that give no
/// file (the caller records the omission).
pub(crate) fn assets(note: &Value) -> (Vec<Asset>, usize) {
    if note["type"].as_str() == Some("video") {
        video(note)
    } else {
        pictures(note)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn pictures_keep_the_default_first_and_a_live_photo_adds_its_motion() {
        let note = json!({"type": "normal", "imageList": [
            {"fileId": "f1", "width": 1440, "height": 1920,
             "urlDefault": "http://sns-webpic.xhscdn.com/a/f1!nd_dft",
             "urlPre": "http://sns-webpic.xhscdn.com/a/f1!nd_prv",
             "infoList": [
                {"imageScene": "WB_DFT", "url": "http://sns-webpic.xhscdn.com/a/f1!nd_dft"},
                {"imageScene": "WB_PRV", "url": "http://sns-webpic.xhscdn.com/a/f1!nd_prv2"}]},
            {"fileId": "f2", "livePhoto": true, "urlDefault": "http://sns-webpic.xhscdn.com/a/f2",
             "stream": {"h264": [{"masterUrl": "http://sns-video.xhscdn.com/f2.mp4", "width": 720, "height": 960, "duration": 3000}]}},
            {"fileId": "f3"},
            {"fileId": "f4", "livePhoto": true, "urlDefault": "http://sns-webpic.xhscdn.com/a/f4"}]});
        let (found, missing) = assets(&note);
        let roles: Vec<_> = found.iter().map(|a| (a.role, a.id.as_str())).collect();
        assert_eq!(
            roles,
            [
                ("image", "f1"),
                ("image", "f2"),
                ("motion", "f2-motion"),
                ("image", "f4")
            ]
        );
        assert_eq!(
            missing, 2,
            "a picture without an address and a live photo without its motion"
        );
        let names: Vec<_> = found[0]
            .variants
            .iter()
            .map(|v| v.representation.as_str())
            .collect();
        assert_eq!(
            names,
            ["default", "WB_PRV", "preview"],
            "the same address is listed once"
        );
        assert!(found[0].variants[0].urls[0].starts_with("https://"));
        assert_eq!(found[2].duration_ms, Some(3000));
    }

    #[test]
    fn a_video_lists_every_codec_and_size_with_the_first_picture_as_its_cover() {
        let note = json!({"type": "video",
            "video": {"capa": {"duration": 30}, "media": {"videoId": 77, "stream": {
                "h264": [{"masterUrl": "http://sns-video.xhscdn.com/a.mp4", "backupUrls": ["http://sns-video-bak.xhscdn.com/a.mp4"],
                          "width": 1080, "height": 1920, "avgBitrate": 2000000, "size": 9000000, "duration": 30500, "fps": 30}],
                "h265": [{"masterUrl": "http://sns-video.xhscdn.com/b.mp4", "width": 1080, "height": 1920, "avgBitrate": 1200000}],
                "av1": []}}},
            "imageList": [{"fileId": "c1", "urlDefault": "http://sns-webpic.xhscdn.com/c1"}]});
        let (found, missing) = assets(&note);
        assert_eq!(missing, 0);
        let roles: Vec<_> = found.iter().map(|a| (a.role, a.id.as_str())).collect();
        assert_eq!(roles, [("video", "77"), ("cover", "c1-cover")]);
        let names: Vec<_> = found[0]
            .variants
            .iter()
            .map(|v| v.representation.as_str())
            .collect();
        assert_eq!(names, ["h264-1080x1920", "h265-1080x1920"]);
        assert_eq!(
            found[0].variants[0].urls.len(),
            2,
            "the backup address follows the main one"
        );
        assert_eq!(found[0].variants[0].size, Some(9_000_000));
        assert_eq!(found[0].duration_ms, Some(30_500));
        assert_eq!(found[1].variants[0].kind, "cover");
        let (none, missing) = assets(&json!({"type": "video", "video": {}}));
        assert!(none.is_empty() && missing == 1);
    }

    #[test]
    fn streams_under_names_of_the_pages_own_are_read_by_what_each_entry_says() {
        let entry = |codec: &str, width: i64, url: &str| json!({"videoCodec": codec, "masterUrl": url, "width": width, "height": width * 2, "avgBitrate": 900000, "defaultStream": 1});
        let note = json!({"type": "video", "video": {"capa": {"duration": 12}, "media": {"videoId": 5, "stream": {
            "EF5": [entry("hevc", 720, "http://sns-video.xhscdn.com/c.mp4"), json!({"videoCodec": "hevc"})],
            "EF4": [entry("avc1.64001f", 720, "http://sns-video.xhscdn.com/a.mp4"), entry("avc1.64001f", 1080, "http://sns-video.xhscdn.com/b.mp4")],
            "h264": [entry("h264", 480, "http://sns-video.xhscdn.com/d.mp4")]}}}});
        let (found, missing) = assets(&note);
        assert_eq!(missing, 0);
        let names: Vec<_> = found[0]
            .variants
            .iter()
            .map(|v| v.representation.as_str())
            .collect();
        assert_eq!(
            names,
            [
                "h264-480x960",
                "h264-720x1440",
                "h264-1080x2160",
                "h265-720x1440"
            ],
            "the page's own order of names does not matter and an entry without an address is skipped"
        );
        let unnamed = json!({"type": "video", "video": {"media": {"videoId": 6, "stream": {
            "EF9": [{"masterUrl": "http://sns-video.xhscdn.com/e.mp4", "width": 100, "height": 200}]}}}});
        assert_eq!(
            assets(&unnamed).0[0].variants[0].representation,
            "ef9-100x200"
        );
    }
}
