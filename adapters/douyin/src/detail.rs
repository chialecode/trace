//! Aweme detail: one content (video, image post, article) → document, resources and
//! download candidates. Source-provided media lengths are kept or rejected, never lost.
use crate::api::{fail, public_headers};
use crate::post::{gallery, kind, timestamp, urls};
use serde_json::{Value, json};
use trace_contracts::content::Block;
use trace_contracts::plugin::*;
use trace_site_support::{
    candidate,
    document::{self, ParsedDocument},
    id, resource,
};

/// Document, resources and candidates collected while one aweme is mapped.
struct Draft {
    parsed: ParsedDocument,
    plans: Vec<MediaCandidate>,
    complete: bool,
}

impl Draft {
    fn new() -> Self {
        let mut parsed = ParsedDocument::default();
        parsed.document.version = 1;
        Self {
            parsed,
            plans: Vec::new(),
            complete: true,
        }
    }
}

// Preserve a source-provided size through the host's EOF and object validation.
// Zero/missing is unknown; malformed or overflowing claims are not silently lost.
fn sized_candidate(
    resource: ResourceIdentityDto,
    address: &Value,
    kind: &str,
    spec: Value,
) -> Result<Option<MediaCandidate>, PluginError> {
    let size = match address.get("data_size") {
        None | Some(Value::Null) => None,
        Some(value) => {
            let n = value
                .as_u64()
                .filter(|n| *n <= i64::MAX as u64)
                .ok_or_else(|| fail("invalid source media length"))?;
            (n > 0).then_some(n as i64)
        }
    };
    Ok(
        candidate(resource, urls(address), kind, spec).map(|mut candidate| {
            candidate.expected_size = size;
            candidate.segments[0].expected_size = size;
            candidate
        }),
    )
}

#[cfg(test)]
pub fn detail(
    v: &Value,
    expected: &str,
) -> Result<(ObservedDetail, Vec<MediaCandidate>), PluginError> {
    detail_for(v, expected, false)
}

pub(crate) fn detail_for(
    v: &Value,
    expected: &str,
    prefer_fps: bool,
) -> Result<(ObservedDetail, Vec<MediaCandidate>), PluginError> {
    if id(&v["aweme_id"]).as_deref() != Some(expected) {
        return Err(fail("detail identity mismatch or empty detail"));
    }
    let k = kind(v);
    let mut draft = Draft::new();
    let title = if k == "article" {
        v["article_info"]["article_title"].as_str()
    } else {
        v["desc"].as_str()
    }
    .unwrap_or("")
    .to_owned();
    if k == "article" {
        article_body(v, &mut draft)?;
    } else {
        draft
            .parsed
            .text(v["desc"].as_str().unwrap_or(""), "paragraph");
    }
    match k {
        "image" => gallery_resources(v, expected, &mut draft)?,
        "video" => video_resource(v, expected, prefer_fps, &mut draft)?,
        _ => {}
    }
    // Type 163 video.play_addr may be BGM. It must never become a main video.
    if k != "video"
        && (v["music"].is_object()
            || (k == "article" && !urls(&v["video"]["play_addr"]).is_empty()))
    {
        music_resource(v, k, expected, &mut draft)?;
    }
    cover_resource(v, expected, &mut draft)?;
    if k == "unknown" {
        draft.complete = false;
        draft
            .parsed
            .document
            .omissions
            .push("unsupported_native_type".into());
    }
    if !draft.parsed.document.omissions.is_empty() {
        draft.complete = false;
    }
    // The observed video CDN requires the same public browser context as the
    // successful source request. These headers carry no authentication data;
    // Core still prohibits cookies and other private headers on media hosts.
    for plan in &mut draft.plans {
        plan.public_headers = public_headers();
    }
    Ok(observed_detail(v, expected, k, title, draft))
}

fn observed_detail(
    v: &Value,
    expected: &str,
    k: &str,
    title: String,
    draft: Draft,
) -> (ObservedDetail, Vec<MediaCandidate>) {
    let Draft {
        parsed,
        plans,
        complete,
    } = draft;
    let mut metadata = trace_contracts::content::safe_metadata(v);
    // Douyin's author.signature is public profile prose, not a request signature.
    // Map the known field explicitly while generic extensions remain fail closed.
    metadata["author_biography"] =
        trace_contracts::content::safe_metadata(&v["author"]["signature"]);
    (
        ObservedDetail {
            document: parsed.document,
            metadata,
            native_entry_key: expected.into(),
            native_kind: k.into(),
            title,
            normalized_body: parsed.body,
            published_at: timestamp(&v["create_time"]),
            published_precision: "source".into(),
            access_state: "available".into(),
            source_version_evidence: json!({"source":"aweme.detail","aweme_type":v["aweme_type"],"article_id":id(&v["article_info"]["article_id"])}),
            resources: parsed.resources,
            completeness: if complete { "full" } else { "summary" }.into(),
            unified: None,
            stats: trace_contracts::content::safe_metadata(&v["statistics"]),
        },
        plans,
    )
}

fn article_body(v: &Value, draft: &mut Draft) -> Result<(), PluginError> {
    let info = &v["article_info"];
    let content = if let Some(s) = info["article_content"].as_str() {
        serde_json::from_str::<Value>(s).map_err(|_| fail("article_content JSON string invalid"))?
    } else {
        info["article_content"].clone()
    };
    let markdown = content["markdown"]
        .as_str()
        .ok_or_else(|| fail("full article markdown missing"))?;
    if markdown.is_empty() {
        return Err(fail("empty article body"));
    }
    draft.parsed = document::markdown(markdown).map_err(fail)?;
    draft.complete = ![
        &info["has_more"],
        &content["has_more"],
        &content["is_truncated"],
    ]
    .iter()
    .any(|v| v.as_bool() == Some(true) || v.as_u64().is_some_and(|n| n != 0));
    for r in &draft.parsed.resources {
        if let Some(url) = draft.parsed.urls.get(&r.source_attachment_id) {
            draft
                .plans
                .push(candidate(r.clone(), vec![url.clone()], "image", json!({})).unwrap());
        }
    }
    Ok(())
}

fn gallery_resources(v: &Value, expected: &str, draft: &mut Draft) -> Result<(), PluginError> {
    let images = gallery(v).ok_or_else(|| fail("gallery image list missing"))?;
    if images.is_empty() {
        return Err(fail("empty gallery"));
    }
    for (i, img) in images.iter().enumerate() {
        gallery_image(img, i, expected, draft)?;
        if img["video"].is_object() || img["live_photo"].is_object() {
            let video = img
                .get("video")
                .filter(|v| v.is_object())
                .unwrap_or(&img["live_photo"]);
            gallery_motion(video, i, expected, draft)?;
        }
    }
    Ok(())
}

fn gallery_image(
    img: &Value,
    i: usize,
    expected: &str,
    draft: &mut Draft,
) -> Result<(), PluginError> {
    let image = ["original_image", "display_image"]
        .iter()
        .find_map(|key| img.get(*key).filter(|v| !urls(v).is_empty()))
        .unwrap_or(img);
    let identity = id(&image["uri"])
        .or_else(|| {
            urls(image)
                .first()
                .and_then(|u| trace_site_support::url_identity(u))
        })
        .unwrap_or_else(|| format!("missing-{i}"));
    let r = resource(
        &format!("{expected}:image:{i}:{identity}"),
        "image",
        i as i64,
        true,
    );
    draft.parsed.document.blocks.push(Block::Resource {
        attachment_id: r.source_attachment_id.clone(),
        caption: img["caption"].as_str().unwrap_or("").into(),
    });
    if let Some(c) = sized_candidate(
        r.clone(),
        image,
        "image",
        json!({"width":image["width"],"height":image["height"]}),
    )? {
        draft.plans.push(c);
    }
    draft.parsed.resources.push(r);
    Ok(())
}

fn gallery_motion(
    video: &Value,
    i: usize,
    expected: &str,
    draft: &mut Draft,
) -> Result<(), PluginError> {
    let identity = asset_identity(video, &video["play_addr"]);
    let r = resource(
        &format!("{expected}:motion:{i}:{identity}"),
        "motion",
        i as i64,
        true,
    );
    if let Some(c) = sized_candidate(r.clone(), &video["play_addr"], "muxed", json!({}))? {
        draft.plans.push(c);
    }
    draft.parsed.document.blocks.push(Block::Resource {
        attachment_id: r.source_attachment_id.clone(),
        caption: "实况片段".into(),
    });
    draft.parsed.resources.push(r);
    Ok(())
}

fn video_resource(
    v: &Value,
    expected: &str,
    prefer_fps: bool,
    draft: &mut Draft,
) -> Result<(), PluginError> {
    let identity = id(&v["video"]["vid"])
        .or_else(|| id(&v["video"]["play_addr"]["uri"]))
        .unwrap_or_else(|| expected.into());
    let r = resource(&format!("{expected}:video:{identity}"), "video", 0, true);
    let video = &v["video"];
    let best = video["bit_rate"].as_array().and_then(|a| {
        a.iter()
            .filter(|a| !urls(&a["play_addr"]).is_empty())
            .max_by_key(|a| {
                (
                    trace_contracts::content::source_rank(
                        &video_spec(&a["play_addr"], a, video),
                        prefer_fps,
                    ),
                    a["bit_rate"].as_u64().unwrap_or(0),
                )
            })
    });
    let address = best.map(|a| &a["play_addr"]).unwrap_or(&video["play_addr"]);
    if let Some(c) = sized_candidate(
        r.clone(),
        address,
        "muxed",
        video_spec(address, best.unwrap_or(video), video),
    )? {
        draft.plans.push(c);
    }
    draft.parsed.resources.push(r);
    Ok(())
}

fn music_resource(
    v: &Value,
    k: &str,
    expected: &str,
    draft: &mut Draft,
) -> Result<(), PluginError> {
    let music = if !urls(&v["music"]["play_url"]).is_empty() {
        &v["music"]["play_url"]
    } else if k == "article" {
        &v["video"]["play_addr"]
    } else {
        &Value::Null
    };
    let identity = asset_identity(&v["music"], music);
    let r = resource(&format!("{expected}:music:{identity}"), "music", 0, true);
    if let Some(c) = sized_candidate(
        r.clone(),
        music,
        "audio",
        json!({"source":if !urls(&v["music"]["play_url"]).is_empty(){"music.play_url"}else{"article.video.play_addr"}}),
    )? {
        draft.plans.push(c);
    }
    draft.parsed.resources.push(r);
    Ok(())
}

fn cover_resource(v: &Value, expected: &str, draft: &mut Draft) -> Result<(), PluginError> {
    if urls(&v["video"]["cover"]).is_empty() {
        return Ok(());
    }
    let r = resource(&format!("{expected}:cover"), "cover", 0, false);
    draft
        .plans
        .push(sized_candidate(r.clone(), &v["video"]["cover"], "cover", json!({}))?.unwrap());
    draft.parsed.resources.push(r);
    Ok(())
}

pub(crate) fn all_candidates(v: &Value, key: &str) -> Result<Vec<MediaCandidate>, PluginError> {
    let (detail, mut result) = detail_for(v, key, false)?;
    if detail.native_kind != "video" {
        return Ok(result);
    }
    let streams = v["video"]["bit_rate"].as_array();
    if let Some(streams) = streams.filter(|s| !s.is_empty()) {
        if streams.len() > 128 {
            return Err(fail("video offer budget"));
        }
        result.retain(|c| c.resource.role != "video");
        for stream in streams {
            if urls(&stream["play_addr"]).is_empty() {
                continue;
            }
            let mut one = v.clone();
            one["video"]["bit_rate"] = json!([stream]);
            let (_, plans) = detail_for(&one, key, false)?;
            for mut c in plans.into_iter().filter(|c| c.resource.role == "video") {
                c.representation = format!(
                    "muxed-{}-{}-{}",
                    stream["gear_name"].as_str().unwrap_or("source"),
                    stream["bit_rate"].as_i64().unwrap_or(0),
                    stream["is_h265"].as_i64().unwrap_or(0)
                );
                c.claimed_spec["codec"] =
                    json!(if stream["is_h265"] == 1 || v["video"]["is_h265"] == 1 {
                        "hevc"
                    } else {
                        "h264"
                    });
                result.push(c);
            }
        }
    }
    result.sort_by_key(|c| {
        std::cmp::Reverse(trace_contracts::content::source_rank(
            &c.claimed_spec,
            false,
        ))
    });
    Ok(result)
}

fn asset_identity(asset: &Value, address: &Value) -> String {
    id(&asset["id_str"])
        .or_else(|| id(&asset["id"]))
        .or_else(|| id(&asset["vid"]))
        .or_else(|| id(&address["uri"]))
        .or_else(|| {
            urls(address)
                .first()
                .and_then(|u| trace_site_support::url_identity(u))
        })
        .unwrap_or_else(|| "unresolved".into())
}

fn video_spec(address: &Value, representation: &Value, video: &Value) -> Value {
    let dimension = |key: &str| {
        address[key]
            .as_u64()
            .filter(|n| *n > 0)
            .or_else(|| video[key].as_u64().filter(|n| *n > 0))
    };
    let fps = representation["FPS"]
        .as_f64()
        .or_else(|| representation["fps"].as_f64())
        .or_else(|| video["fps"].as_f64())
        .filter(|n| n.is_finite() && *n > 0.0);
    json!({"width":dimension("width"),"height":dimension("height"),"fps":fps,
        "duration_ms":video["duration"],"bit_rate":representation["bit_rate"]})
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::api::envelope;
    use crate::post::page;
    #[test]
    fn music_and_motion_edits_change_identity_but_signed_url_rotation_does_not() {
        let post = |file: &str, query: &str| {
            json!({
                "aweme_id": "1",
                "aweme_type": 68,
                "desc": "same body",
                "images": [{"uri":"still", "url_list":["https://cdn.douyinpic.com/still.jpg"], "video":{"play_addr":{"url_list":[format!("https://cdn.douyinvod.com/{file}.mp4?{query}")]}}}],
                "music": {"play_url":{"url_list":[format!("https://cdn.douyinvod.com/{file}.mp3?{query}")]}},
            })
        };
        let a = detail(&post("first", "sign=one"), "1").unwrap().0.resources;
        let rotated = detail(&post("first", "sign=two"), "1").unwrap().0.resources;
        let edited = detail(&post("second", "sign=two"), "1")
            .unwrap()
            .0
            .resources;
        let identities = |r: Vec<ResourceIdentityDto>| {
            r.into_iter()
                .map(|r| r.source_attachment_id)
                .collect::<Vec<_>>()
        };
        let a = identities(a);
        assert_eq!(a, identities(rotated));
        let changed = identities(edited);
        assert_eq!(a[0], changed[0]);
        assert_ne!(a[1], changed[1]);
        assert_ne!(a[2], changed[2]);
    }

    #[test]
    fn preference_selects_available_stream_and_matching_claims() {
        let v = json!({"aweme_id":"1","aweme_type":0,"video":{"width":320,"height":240,"duration":10000,"bit_rate":[
            {"FPS":30,"bit_rate":8000000,"play_addr":{"width":3840,"height":2160,"url_list":["https://cdn.invalid/4k.mp4"]}},
            {"FPS":60,"bit_rate":4000000,"play_addr":{"width":1920,"height":1080,"url_list":["https://cdn.invalid/60fps.mp4"]}},
            {"FPS":120,"bit_rate":9000000,"play_addr":{"width":7680,"height":4320,"url_list":[]}}
        ]}});
        for (fps, width, url) in [(false, 3840, "4k.mp4"), (true, 1920, "60fps.mp4")] {
            let (_, p) = detail_for(&v, "1", fps).unwrap();
            assert_eq!(p.len(), 1);
            assert_eq!(p[0].claimed_spec["width"], width);
            assert_eq!(
                p[0].claimed_spec["fps"].as_f64(),
                Some(if fps { 60.0 } else { 30.0 })
            );
            assert!(p[0].segments[0].primary.ends_with(url));
        }
    }
    #[test]
    fn article_full_detail_exceeds_summary_and_music_is_not_video() {
        let md = format!(
            "{}\n\n![图](https://cdn.invalid/full.jpg)",
            "正文".repeat(5000)
        );
        for string in [false, true] {
            let content = json!({"markdown":md});
            let v = json!({
                "aweme_id": "90071992547409931",
                "aweme_type": 163,
                "desc": "简介",
                "article_info": {"article_title":"长文", "article_id":"90071992547409932", "article_content":if string{json!(content.to_string())}else{content}, "has_more":false},
                "music": {"play_url":{"url_list":["https://cdn.invalid/music.mp3"]}},
                "video": {"play_addr":{"url_list":["https://cdn.invalid/bgm.mp3"]}},
            });
            let (d, p) = detail(&v, "90071992547409931").unwrap();
            assert!(d.normalized_body.chars().count() > 10000);
            assert_eq!(d.completeness, "full");
            assert_eq!(d.resources.len(), 2);
            assert!(p.iter().all(|c| c.resource.role != "video"));
            assert_eq!(d.source_version_evidence["article_id"], "90071992547409932");
        }
    }
    #[test]
    fn partial_article_and_empty_pagination_never_claim_completion() {
        assert!(detail(&json!({"aweme_id":"1","aweme_type":163,"desc":"简介"}), "1").is_err());
        let page = page(
            &json!({"aweme_list":[],"has_more":1,"max_cursor":"90071992547409931"}),
            "article",
        )
        .unwrap();
        assert!(page.has_more);
        assert_eq!(page.next_cursor.as_deref(), Some("90071992547409931"));
        assert!(page.items.is_empty());
        assert!(envelope(json!({"status_code":0,"verify_data":{}})).is_err());
        let (d,_)=detail(&json!({"aweme_id":"1","aweme_type":163,"article_info":{"article_content":{"markdown":"未完"},"has_more":true}}),"1").unwrap();
        assert_eq!(d.completeness, "summary");
    }
    #[test]
    fn nested_gallery_motion_missing_still_and_article_bgm() {
        let v = json!({"aweme_id":"1","image_post_info":{"images":[
            {"original_image":{"uri":"original-1","width":4000,"height":3000,"url_list":["https://cdn.invalid/full.jpg"]},"display_image":{"url_list":["https://cdn.invalid/small.jpg"]},"video":null,"live_photo":{"play_addr":{"url_list":["https://cdn.invalid/live.mp4"]}}},
            {"video":{"play_addr":{"url_list":["https://cdn.invalid/motion-only.mp4"]}}}
        ]},"mix_info":{"mix_id":"99999999999999999","mix_name":"合集"},"author":{"signature":"公开简介","auth_token":"secret"}});
        let (d, p) = detail(&v, "1").unwrap();
        assert_eq!(d.native_kind, "image");
        assert_eq!(d.resources.len(), 4);
        assert_eq!(p.len(), 3);
        assert_eq!(p[0].claimed_spec["width"], 4000);
        assert!(p[0].segments[0].primary.ends_with("full.jpg"));
        assert_eq!(
            d.resources
                .iter()
                .filter(|r| r.role == "motion" && r.required)
                .count(),
            2
        );
        assert!(d.resources.iter().any(|r| {
            r.role == "image"
                && r.required
                && !p
                    .iter()
                    .any(|c| c.resource.source_attachment_id == r.source_attachment_id)
        }));
        assert_eq!(d.metadata["mix_info"]["mix_id"], "99999999999999999");
        assert_eq!(d.metadata["author_biography"], "公开简介");
        assert!(!d.metadata.to_string().contains("secret"));
        let (d,p)=detail(&json!({"aweme_id":"2","aweme_type":163,"article_info":{"article_content":{"markdown":"长文"}},"video":{"play_addr":{"url_list":["https://cdn.invalid/bgm.mp3"]}}}),"2").unwrap();
        assert_eq!(d.resources[0].role, "music");
        assert_eq!(p[0].kind, "audio");
    }
}

#[cfg(test)]
mod acquisition_integrity_tests {
    use super::*;

    #[test]
    fn declared_douyin_media_length_reaches_candidate() {
        let value = json!({"aweme_id":"71","aweme_type":0,"video":{
            "width":640,"height":360,"duration":1000,
            "play_addr":{"uri":"source-71","url_list":["https://media.douyinvod.com/71.mp4"],"data_size":4096}
        }});
        let (_, candidates) = detail(&value, "71").unwrap();
        assert_eq!(candidates.len(), 1);
        assert_eq!(
            candidates[0].expected_size,
            Some(4096),
            "source data_size must constrain HTTP EOF; hashing received bytes alone is not source completeness"
        );
    }
    #[test]
    fn media_lengths_cover_selected_variant_motion_music_and_invalid_claims() {
        let value = json!({"aweme_id":"72","aweme_type":68,
            "images":[{"url_list":["https://media.douyinpic.com/72.jpg"],"data_size":100,
                "video":{"play_addr":{"url_list":["https://media.douyinvod.com/motion.mp4"],"data_size":200}}}],
            "music":{"id":"3","play_url":{"url_list":["https://media.douyinvod.com/music.mp4"],"data_size":300}}});
        let (_, candidates) = detail(&value, "72").unwrap();
        for (role, size) in [("image", 100), ("motion", 200), ("music", 300)] {
            let candidate = candidates.iter().find(|c| c.resource.role == role).unwrap();
            assert_eq!(candidate.expected_size, Some(size));
            assert_eq!(candidate.segments[0].expected_size, Some(size));
        }
        let mut video = json!({"aweme_id":"73","aweme_type":0,"video":{
            "play_addr":{"url_list":["https://media.douyinvod.com/low.mp4"],"data_size":500},
            "bit_rate":[{"play_addr":{"url_list":["https://media.douyinvod.com/high.mp4"],"data_size":900}}]}});
        assert_eq!(detail(&video, "73").unwrap().1[0].expected_size, Some(900));
        for invalid in [json!(-1), json!("900"), json!(9223372036854775808u64)] {
            video["video"]["bit_rate"][0]["play_addr"]["data_size"] = invalid;
            assert!(detail(&video, "73").is_err());
        }
        video["video"]["bit_rate"][0]["play_addr"]["data_size"] = json!(0);
        assert_eq!(detail(&video, "73").unwrap().1[0].expected_size, None);
    }
}
