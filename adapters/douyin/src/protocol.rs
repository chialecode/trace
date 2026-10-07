use serde_json::{Value, json};
use trace_contracts::plugin::*;
use trace_domain::errors::FailureClass;
use trace_site_support::{
    candidate,
    document::{self, ParsedDocument},
    error, id, resource,
    runtime::Client,
};

const BASE: &str = "https://www.douyin.com";
pub(crate) const UA: &str =
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/130.0.0.0 Safari/537.36";
fn fail(message: &str) -> PluginError {
    error(FailureClass::SchemaChanged, message)
}
pub(crate) fn api(
    client: &Client,
    path: &str,
    params: &[(&str, &str)],
) -> Result<Value, PluginError> {
    let mut url = url::Url::parse(&format!("{BASE}{path}")).unwrap();
    url.query_pairs_mut()
        .append_pair("aid", "6383")
        .append_pair("device_platform", "webapp")
        .extend_pairs(params.iter().copied());
    let time_ms = (time::OffsetDateTime::now_utc().unix_timestamp_nanos() / 1_000_000) as i64;
    let signature = crate::signature::sign_at(url.query().unwrap_or(""), UA, time_ms);
    url.query_pairs_mut().append_pair("a_bogus", &signature);
    let v=client.json_large(url.as_str(),json!({"referer":"https://www.douyin.com/","user-agent":"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/130.0.0.0 Safari/537.36"}),true)?;
    envelope(v)
}
pub fn envelope(v: Value) -> Result<Value, PluginError> {
    match v["status_code"].as_i64() {
        Some(0) if v.get("verify_data").is_none() => Ok(v),
        Some(8 | 9 | 2190008) => Err(error(
            FailureClass::AuthRequired,
            "Douyin session requires validation",
        )),
        Some(4 | 10000) => Err(error(
            FailureClass::PermissionDenied,
            "Douyin access denied",
        )),
        Some(_) => Err(error(FailureClass::Unknown, "Douyin business failure")),
        None => Err(fail("Douyin response missing status code")),
    }
}
pub fn dispatch(client: &Client, req: &PluginRequest) -> Result<Value, PluginError> {
    let p = &req.params;
    match req.method.as_str() {
        "resolve_source" => {
            crate::source::resolve(client, p).map(|v| serde_json::to_value(v).unwrap())
        }
        "sample_live_auxiliary" => {
            crate::live::auxiliary(client, p).map(|v| serde_json::to_value(v).unwrap())
        }
        "probe_live" => crate::live::probe(client, p).map(|v| serde_json::to_value(v).unwrap()),
        "hello" => Ok(
            json!({"protocol_version":trace_domain::PLUGIN_PROTOCOL_VERSION,"adapter_version":env!("CARGO_PKG_VERSION"),"platform":"douyin","capabilities":["resolve_account","enumerate","observe","resolve_media","probe_auth","probe_live","sample_live_auxiliary","resolve_source","plugin_chunks_v1"],"limits":{"max_page_items":20,"supports_cancel":false}}),
        ),
        "resolve_account" => {
            let input = p["input"]
                .as_str()
                .ok_or_else(|| fail("missing account input"))?;
            let key = account_key(client, input)?;
            let v = api(
                client,
                "/aweme/v1/web/user/profile/other/",
                &[("sec_user_id", &key)],
            )?;
            let u = &v["user"];
            if u["sec_uid"].as_str() != Some(&key) {
                return Err(fail("account identity mismatch"));
            }
            Ok(
                json!({"native_account_key":key,"display_name":u["nickname"],"profile_url":format!("{BASE}/user/{key}"),"evidence":{"uid":id(&u["uid"]),"unique_id":id(&u["unique_id"]),"short_id":id(&u["short_id"]),"source":"user.profile.other","biography":u["signature"],"profile":trace_contracts::content::safe_metadata(u)}}),
            )
        }
        "enumerate" => {
            let p: EnumerateParams = serde_json::from_value(p.clone())
                .map_err(|_| fail("invalid enumeration parameters"))?;
            serde_json::to_value(crate::enumeration::enumerate(client, &p)?)
                .map_err(|_| fail("page serialization"))
        }
        "observe" | "resolve_media" => {
            let key = p["native_entry_key"]
                .as_str()
                .ok_or_else(|| fail("missing entry id"))?;
            if key.is_empty() || !key.bytes().all(|b| b.is_ascii_digit()) {
                return Err(fail("invalid aweme id"));
            }
            let v = api(client, "/aweme/v1/web/aweme/detail/", &[("aweme_id", key)])?;
            let (detail, mut plan) = detail_for(
                &v["aweme_detail"],
                key,
                p["quality_hint"]["prefer_fps"] == true,
            )?;
            if p["quality_hint"]["all_variants"] == true {
                plan = all_candidates(&v["aweme_detail"], key)?;
            }
            if req.method == "observe" {
                Ok(serde_json::to_value(detail).unwrap())
            } else {
                let p: ResolveMediaParams = serde_json::from_value(p.clone())
                    .map_err(|_| fail("invalid media parameters"))?;
                Ok(serde_json::to_value(ResolveMediaResult {
                    candidates: plan
                        .into_iter()
                        .filter(|c| {
                            p.resources
                                .iter()
                                .any(|r| r.source_attachment_id == c.resource.source_attachment_id)
                        })
                        .collect(),
                    notes: json!({}),
                })
                .unwrap())
            }
        }
        "probe_auth" => {
            // The self endpoint resolves only the caller's session. query/user
            // also assigns guest IDs and must not be used as login evidence.
            let v = api(client, "/aweme/v1/web/user/profile/self/", &[])?;
            let logged_in = id(&v["user"]["uid"]).is_some_and(|s| {
                !s.is_empty() && s != "0" && s.bytes().all(|b| b.is_ascii_digit())
            }) && v["user"]["sec_uid"].as_str().is_some_and(|s| !s.is_empty());
            Ok(
                json!({"state":if logged_in {"valid"}else{"unknown"},"capability":{"login_proven":logged_in,"source":"user.profile.self","media_access":"unknown"}}),
            )
        }
        _ => Err(error(
            FailureClass::Unsupported,
            "unsupported Douyin method",
        )),
    }
}

fn account_key(client: &Client, input: &str) -> Result<String, PluginError> {
    if input.starts_with("MS4")
        && input.len() <= 256
        && input
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_')
    {
        return Ok(input.into());
    }
    let mut url = url::Url::parse(input).map_err(|_| fail("invalid account input"))?;
    if url.scheme() != "https" || !url.username().is_empty() || url.password().is_some() {
        return Err(fail("invalid account URL"));
    }
    if url.host_str() == Some("v.douyin.com") {
        let reply = client.fetch("HEAD", url.as_str(), json!({"user-agent":UA}), false)?;
        if reply.status != 200 {
            return Err(fail("short link resolution failed"));
        }
        url =
            url::Url::parse(&reply.final_url).map_err(|_| fail("short link final URL missing"))?;
    }
    if !matches!(url.host_str(), Some("www.douyin.com" | "www.iesdouyin.com")) {
        return Err(fail("short link left expected site"));
    }
    let parts = url
        .path_segments()
        .ok_or_else(|| fail("invalid account path"))?
        .filter(|s| !s.is_empty())
        .collect::<Vec<_>>();
    if let Some(i) = parts.iter().position(|p| *p == "user") {
        let key = parts
            .get(i + 1)
            .filter(|k| k.starts_with("MS4"))
            .ok_or_else(|| fail("stable account id missing"))?;
        return Ok((*key).to_owned());
    }
    if let Some(i) = parts.iter().position(|p| matches!(*p, "video" | "note")) {
        let key = parts
            .get(i + 1)
            .filter(|k| k.bytes().all(|b| b.is_ascii_digit()))
            .ok_or_else(|| fail("short link content id missing"))?;
        let detail = api(client, "/aweme/v1/web/aweme/detail/", &[("aweme_id", key)])?;
        return id(&detail["aweme_detail"]["author"]["sec_uid"])
            .ok_or_else(|| fail("short link author identity unavailable"));
    }
    Err(fail("expected user or content share URL"))
}

fn kind(v: &Value) -> &'static str {
    if v["aweme_type"] == 163 || v["article_info"].is_object() {
        "article"
    } else if gallery(v).is_some() || matches!(v["aweme_type"].as_i64(), Some(2 | 68 | 150)) {
        "image"
    } else if v["video"].is_object() {
        "video"
    } else {
        "unknown"
    }
}
fn gallery(v: &Value) -> Option<&Vec<Value>> {
    v["images"]
        .as_array()
        .or_else(|| v["image_post_info"]["images"].as_array())
}
fn timestamp(v: &Value) -> Option<String> {
    time::OffsetDateTime::from_unix_timestamp(v.as_i64()?)
        .ok()?
        .format(&time::format_description::well_known::Rfc3339)
        .ok()
}
pub fn page(v: &Value, lane: &str) -> Result<EnumeratedPage, PluginError> {
    let values = v["aweme_list"]
        .as_array()
        .ok_or_else(|| fail("post response missing list"))?;
    let more = match &v["has_more"] {
        Value::Bool(b) => *b,
        Value::Number(n) => n.as_u64().ok_or_else(|| fail("invalid has_more"))? != 0,
        _ => return Err(fail("missing has_more")),
    };
    let cursor = id(&v["max_cursor"]);
    if more && cursor.is_none() {
        return Err(fail("missing continuation cursor"));
    }
    let mut items = Vec::new();
    for a in values {
        let k = kind(a);
        if lane != "dynamic" && lane != "all" && lane != k {
            continue;
        }
        items.push(EnumeratedEntry {
            native_entry_key: id(&a["aweme_id"]).ok_or_else(|| fail("missing aweme identity"))?,
            native_kind: k.into(),
            title: a["desc"].as_str().unwrap_or("").into(),
            published_at: timestamp(&a["create_time"]),
            published_precision: "source".into(),
            pinned: a["is_top"] == 1,
            summary_body: a["desc"].as_str().map(str::to_owned),
            stats: trace_contracts::content::safe_metadata(&a["statistics"]),
        });
    }
    Ok(EnumeratedPage {
        items,
        next_cursor: if more { cursor } else { None },
        has_more: more,
        coverage: json!({"source":"aweme.post","lane":lane,"observed_count":values.len(),"history":"unknown"}),
    })
}
fn urls(v: &Value) -> Vec<String> {
    v["url_list"]
        .as_array()
        .map(|a| {
            a.iter()
                .filter_map(Value::as_str)
                .map(str::to_owned)
                .collect()
        })
        .unwrap_or_default()
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
    let mut parsed = ParsedDocument::default();
    parsed.document.version = 1;
    let mut plans = Vec::new();
    let mut complete = true;
    let title = if k == "article" {
        v["article_info"]["article_title"].as_str()
    } else {
        v["desc"].as_str()
    }
    .unwrap_or("")
    .to_owned();
    if k == "article" {
        let info = &v["article_info"];
        let content = if let Some(s) = info["article_content"].as_str() {
            serde_json::from_str::<Value>(s)
                .map_err(|_| fail("article_content JSON string invalid"))?
        } else {
            info["article_content"].clone()
        };
        let markdown = content["markdown"]
            .as_str()
            .ok_or_else(|| fail("full article markdown missing"))?;
        if markdown.is_empty() {
            return Err(fail("empty article body"));
        }
        parsed = document::markdown(markdown).map_err(fail)?;
        complete = ![
            &info["has_more"],
            &content["has_more"],
            &content["is_truncated"],
        ]
        .iter()
        .any(|v| v.as_bool() == Some(true) || v.as_u64().is_some_and(|n| n != 0));
        for r in &parsed.resources {
            if let Some(url) = parsed.urls.get(&r.source_attachment_id) {
                plans.push(candidate(r.clone(), vec![url.clone()], "image", json!({})).unwrap());
            }
        }
    } else {
        parsed.text(v["desc"].as_str().unwrap_or(""), "paragraph");
    }
    if k == "image" {
        let images = gallery(v).ok_or_else(|| fail("gallery image list missing"))?;
        if images.is_empty() {
            return Err(fail("empty gallery"));
        }
        for (i, img) in images.iter().enumerate() {
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
            parsed
                .document
                .blocks
                .push(trace_contracts::content::Block::Resource {
                    attachment_id: r.source_attachment_id.clone(),
                    caption: img["caption"].as_str().unwrap_or("").into(),
                });
            if let Some(c) = sized_candidate(
                r.clone(),
                image,
                "image",
                json!({"width":image["width"],"height":image["height"]}),
            )? {
                plans.push(c);
            }
            parsed.resources.push(r);
            if img["video"].is_object() || img["live_photo"].is_object() {
                let video = img
                    .get("video")
                    .filter(|v| v.is_object())
                    .unwrap_or(&img["live_photo"]);
                let identity = asset_identity(video, &video["play_addr"]);
                let r = resource(
                    &format!("{expected}:motion:{i}:{identity}"),
                    "motion",
                    i as i64,
                    true,
                );
                if let Some(c) =
                    sized_candidate(r.clone(), &video["play_addr"], "muxed", json!({}))?
                {
                    plans.push(c);
                }
                parsed
                    .document
                    .blocks
                    .push(trace_contracts::content::Block::Resource {
                        attachment_id: r.source_attachment_id.clone(),
                        caption: "实况片段".into(),
                    });
                parsed.resources.push(r);
            }
        }
    }
    if k == "video" {
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
            plans.push(c);
        }
        parsed.resources.push(r);
    }
    // Type 163 video.play_addr may be BGM. It must never become a main video.
    if k != "video"
        && (v["music"].is_object()
            || (k == "article" && !urls(&v["video"]["play_addr"]).is_empty()))
    {
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
            plans.push(c);
        }
        parsed.resources.push(r);
    }
    let cover = urls(&v["video"]["cover"]);
    if !cover.is_empty() {
        let r = resource(&format!("{expected}:cover"), "cover", 0, false);
        plans.push(sized_candidate(r.clone(), &v["video"]["cover"], "cover", json!({}))?.unwrap());
        parsed.resources.push(r);
    }
    if k == "unknown" {
        complete = false;
        parsed
            .document
            .omissions
            .push("unsupported_native_type".into());
    }
    if !parsed.document.omissions.is_empty() {
        complete = false;
    }
    // The observed video CDN requires the same public browser context as the
    // successful source request. These headers carry no authentication data;
    // Core still prohibits cookies and other private headers on media hosts.
    for plan in &mut plans {
        plan.public_headers = json!({"referer":"https://www.douyin.com/","user-agent":UA});
    }
    let mut metadata = trace_contracts::content::safe_metadata(v);
    // Douyin's author.signature is public profile prose, not a request signature.
    // Map the known field explicitly while generic extensions remain fail closed.
    metadata["author_biography"] =
        trace_contracts::content::safe_metadata(&v["author"]["signature"]);
    Ok((
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
            stats: trace_contracts::content::safe_metadata(&v["statistics"]),
        },
        plans,
    ))
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
    #[test]
    fn c02_music_and_motion_edits_change_identity_but_signed_url_rotation_does_not() {
        let post = |file: &str, query: &str| json!({"aweme_id":"1","aweme_type":68,"desc":"same body","images":[{"uri":"still","url_list":["https://cdn.douyinpic.com/still.jpg"],"video":{"play_addr":{"url_list":[format!("https://cdn.douyinvod.com/{file}.mp4?{query}")]}}}],"music":{"play_url":{"url_list":[format!("https://cdn.douyinvod.com/{file}.mp3?{query}")]}}});
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
    fn c07_preference_selects_available_stream_and_matching_claims() {
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
    fn c02_article_full_detail_exceeds_summary_and_music_is_not_video() {
        let md = format!(
            "{}\n\n![图](https://cdn.invalid/full.jpg)",
            "正文".repeat(5000)
        );
        for string in [false, true] {
            let content = json!({"markdown":md});
            let v = json!({"aweme_id":"90071992547409931","aweme_type":163,"desc":"简介","article_info":{"article_title":"长文","article_id":"90071992547409932","article_content":if string{json!(content.to_string())}else{content},"has_more":false},"music":{"play_url":{"url_list":["https://cdn.invalid/music.mp3"]}},"video":{"play_addr":{"url_list":["https://cdn.invalid/bgm.mp3"]}}});
            let (d, p) = detail(&v, "90071992547409931").unwrap();
            assert!(d.normalized_body.chars().count() > 10000);
            assert_eq!(d.completeness, "full");
            assert_eq!(d.resources.len(), 2);
            assert!(p.iter().all(|c| c.resource.role != "video"));
            assert_eq!(d.source_version_evidence["article_id"], "90071992547409932");
        }
    }
    #[test]
    fn c01_c02_partial_article_and_empty_pagination_never_claim_completion() {
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
    fn c02_nested_gallery_motion_missing_still_and_article_bgm() {
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
