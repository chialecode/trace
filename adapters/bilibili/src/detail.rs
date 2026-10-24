//! 单条视频详情：view 响应 → 详情观察、分 P 与字幕资源。

use crate::clock::{rfc3339, unix_now};
use crate::sign::signed_get;
use crate::{AdapterState, api};
use serde_json::{Value, json};
use std::time::{Duration, Instant};
use trace_contracts::plugin::{MediaCandidate, ObserveParams, ObservedDetail, ResourceIdentityDto};
use trace_domain::errors::FailureClass;

pub(crate) fn observe(
    state: &AdapterState,
    p: &ObserveParams,
    deadline: Duration,
    started: Instant,
) -> Result<ObservedDetail, trace_contracts::plugin::PluginError> {
    let key = p.native_entry_key.trim();
    let aid = key
        .strip_prefix("av")
        .filter(|n| !n.is_empty() && n.bytes().all(|b| b.is_ascii_digit()));
    let params = vec![(
        if aid.is_some() { "aid" } else { "bvid" }.to_string(),
        aid.unwrap_or(key).to_owned(),
    )];
    let data = signed_get(state, api::VIEW_URL, &params, true, deadline, started)?;
    if let Some(expected) = aid
        && trace_site_support::id(&data["aid"]).as_deref() != Some(expected)
    {
        return Err(trace_site_support::error(
            FailureClass::SchemaChanged,
            "view aid mismatch",
        ));
    }
    let bvid = if aid.is_some() {
        data["bvid"].as_str().unwrap_or_default()
    } else {
        key
    }
    .to_owned();
    let mut detail = map_view(&data, &bvid)?;
    if let Some(aid) = aid {
        detail.source_version_evidence["identity_mapping"] =
            json!({"aid":aid,"bvid":bvid,"source":"view.aid+bvid"});
    }
    detail.metadata["player_pages"] = json!([]);
    for resource in detail
        .resources
        .clone()
        .iter()
        .filter(|r| r.role == "video")
        .take(32)
    {
        let Some(cid) = resource.group_key.as_deref() else {
            continue;
        };
        match player_info(state, &bvid, cid, deadline, started) {
            Ok(player) => {
                let (resources, _) = player_subtitles(&player, &bvid, cid);
                detail.resources.extend(resources);
                detail.metadata["player_pages"].as_array_mut().unwrap().push(json!({"cid":cid,"coverage":"observed","data":trace_contracts::content::safe_metadata(&player)}));
            }
            Err(error) => detail.metadata["player_pages"]
                .as_array_mut()
                .unwrap()
                .push(json!({"cid":cid,"coverage":"unavailable","failure":error.class})),
        }
    }
    detail.metadata["player_page_budget"] = json!(32);
    Ok(detail)
}

pub(crate) fn player_info(
    state: &AdapterState,
    bvid: &str,
    cid: &str,
    deadline: Duration,
    started: Instant,
) -> Result<Value, trace_contracts::plugin::PluginError> {
    signed_get(
        state,
        "https://api.bilibili.com/x/player/wbi/v2",
        &[("bvid".into(), bvid.into()), ("cid".into(), cid.into())],
        true,
        deadline,
        started,
    )
}

pub(crate) fn player_subtitles(
    data: &Value,
    bvid: &str,
    cid: &str,
) -> (Vec<ResourceIdentityDto>, Vec<MediaCandidate>) {
    let mut resources = Vec::new();
    let mut candidates = Vec::new();
    if let Some(subtitles) = data["subtitle"]["subtitles"].as_array() {
        for (i, subtitle) in subtitles.iter().take(64).enumerate() {
            let Some(id) = trace_site_support::id(&subtitle["id_str"])
                .or_else(|| trace_site_support::id(&subtitle["id"]))
            else {
                continue;
            };
            let mut r = trace_site_support::resource(
                &format!("{bvid}:{cid}:subtitle:{id}"),
                "subtitle",
                i as i64,
                false,
            );
            r.group_key = Some(cid.into());
            r.title = subtitle["lan_doc"]
                .as_str()
                .or_else(|| subtitle["lan"].as_str())
                .map(str::to_owned);
            if let Some(raw) = subtitle["subtitle_url"].as_str() {
                let url = if raw.starts_with("//") {
                    format!("https:{raw}")
                } else {
                    raw.to_owned()
                };
                if trace_site_support::url_identity(&url).is_some() {
                    let mut c = trace_site_support::candidate(
                        r.clone(),
                        vec![url],
                        "subtitle_json",
                        json!({"language":subtitle["lan"],"source":"player.subtitle"}),
                    )
                    .unwrap();
                    c.public_headers = api::public_headers();
                    candidates.push(c);
                }
            }
            resources.push(r);
        }
    }
    (resources, candidates)
}

/// view 响应 → 详情观察：每 P video 资源（必要）+ audio 资源（可选，
/// DASH 无音轨/durl 混流场景不阻断保存完整性）；封面可选。
pub(crate) fn map_view(
    data: &Value,
    expected_bvid: &str,
) -> Result<ObservedDetail, trace_contracts::plugin::PluginError> {
    let fail = |class, msg: &str, details: Value| trace_contracts::plugin::PluginError {
        class,
        message: msg.to_string(),
        retry_after_ms: None,
        details,
    };
    let bvid = data.get("bvid").and_then(|b| b.as_str()).unwrap_or("");
    if bvid.is_empty() {
        return Err(fail(
            FailureClass::SourceUnavailable,
            "view response missing bvid (unavailable or deleted?)",
            Value::Null,
        ));
    }
    if bvid != expected_bvid {
        return Err(fail(
            FailureClass::SchemaChanged,
            "view bvid mismatch",
            json!({"requested": expected_bvid, "returned": bvid}),
        ));
    }
    let pages = data
        .get("pages")
        .and_then(|p| p.as_array())
        .ok_or_else(|| {
            fail(
                FailureClass::SchemaChanged,
                "view response missing pages",
                Value::Null,
            )
        })?;
    if pages.is_empty() {
        return Err(fail(
            FailureClass::SourceUnavailable,
            "video has no playable pages",
            Value::Null,
        ));
    }
    let pubdate = data.get("pubdate").and_then(|v| v.as_i64()).unwrap_or(0);
    let mut resources: Vec<ResourceIdentityDto> = Vec::new();
    if data
        .get("pic")
        .and_then(|p| p.as_str())
        .filter(|p| !p.is_empty())
        .is_some()
    {
        let pic = data.get("pic").and_then(|p| p.as_str()).unwrap_or("cover");
        resources.push(ResourceIdentityDto {
            role: "cover".into(),
            source_attachment_id: format!("{bvid}:cover:{}", pic_hash(pic)),
            position: 0,
            title: None,
            group_key: None,
            required: false,
        });
    }
    for p in pages {
        let cid = p.get("cid").and_then(|c| c.as_i64()).unwrap_or(0);
        let page = p.get("page").and_then(|c| c.as_i64()).unwrap_or(0);
        if cid == 0 {
            return Err(fail(
                FailureClass::SchemaChanged,
                "page entry missing cid",
                Value::Null,
            ));
        }
        resources.push(ResourceIdentityDto {
            role: "video".into(),
            source_attachment_id: format!("{bvid}:{cid}:{page}:video"),
            position: page,
            title: p.get("part").and_then(|t| t.as_str()).map(str::to_string),
            group_key: Some(cid.to_string()),
            required: true,
        });
        resources.push(ResourceIdentityDto {
            role: "audio".into(),
            source_attachment_id: format!("{bvid}:{cid}:{page}:audio"),
            position: page,
            title: None,
            group_key: Some(cid.to_string()),
            // Required until resolve_media supplies explicit silent/muxed evidence.
            required: true,
        });
    }
    let stat = data.get("stat").cloned().unwrap_or(Value::Null);
    Ok(ObservedDetail {
        document: Default::default(),
        metadata: trace_contracts::content::safe_metadata(data),
        native_entry_key: bvid.to_string(),
        native_kind: "video".into(),
        title: data
            .get("title")
            .and_then(|t| t.as_str())
            .unwrap_or("")
            .to_string(),
        normalized_body: data
            .get("desc")
            .and_then(|d| d.as_str())
            .unwrap_or("")
            .trim()
            .to_string(),
        published_at: (pubdate > 0).then(|| rfc3339(pubdate)),
        published_precision: "source".into(),
        access_state: "available".into(),
        source_version_evidence: json!({
            "aid": data.get("aid"),
            "bvid": bvid,
            "cid_count": pages.len(),
            "observed_at": rfc3339(unix_now() as i64),
        }),
        resources,
        completeness: "full".into(),
        unified: None,
        stats: json!({
            "view": stat.get("view"),
            "danmaku": stat.get("danmaku"),
            "reply": stat.get("reply"),
            "favorite": stat.get("favorite"),
            "like": stat.get("like"),
        }),
    })
}

fn pic_hash(pic: &str) -> String {
    // 稳定短标识：取路径末段（含扩展名）；同 URL 稳定。
    pic.rsplit('/').next().unwrap_or("cover").to_string()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn err_class(r: Result<ObservedDetail, trace_contracts::plugin::PluginError>) -> FailureClass {
        r.unwrap_err().class
    }

    #[test]
    fn observe_single_p_maps_resources() {
        let env: Value = serde_json::from_str(include_str!(
            "../../../tests/fixtures/bilibili/view_single.json"
        ))
        .unwrap();
        let d = map_view(&env["data"], "BV1fakeBVfake1").unwrap();
        assert_eq!(d.title, "测试视频一");
        assert!(d.normalized_body.contains("第一个"));
        assert_eq!(d.access_state, "available");
        assert_eq!(d.completeness, "full");
        // 封面（可选）+ 1P video（必要）+ 1P audio（可选）。
        assert_eq!(d.resources.len(), 3);
        let video = d.resources.iter().find(|r| r.role == "video").unwrap();
        assert!(video.required);
        assert_eq!(video.source_attachment_id, "BV1fakeBVfake1:1001:1:video");
        assert_eq!(video.group_key.as_deref(), Some("1001"));
        let audio = d.resources.iter().find(|r| r.role == "audio").unwrap();
        assert!(audio.required);
        let cover = d.resources.iter().find(|r| r.role == "cover").unwrap();
        assert!(!cover.required);
        assert_eq!(d.source_version_evidence["cid_count"], 1);
    }

    #[test]
    fn observe_multi_p_maps_group_per_cid() {
        let env: Value = serde_json::from_str(include_str!(
            "../../../tests/fixtures/bilibili/view_multi.json"
        ))
        .unwrap();
        let d = map_view(&env["data"], "BV1fakeBVfake2").unwrap();
        let videos: Vec<_> = d.resources.iter().filter(|r| r.role == "video").collect();
        let audios: Vec<_> = d.resources.iter().filter(|r| r.role == "audio").collect();
        assert_eq!(videos.len(), 2);
        assert_eq!(audios.len(), 2);
        // 每 P 组不同：cid 作为资源组。
        assert_ne!(videos[0].group_key, videos[1].group_key);
        assert_eq!(d.source_version_evidence["cid_count"], 2);
    }

    #[test]
    fn observe_rejects_bvid_mismatch_and_empty_pages() {
        let env: Value = serde_json::from_str(include_str!(
            "../../../tests/fixtures/bilibili/view_single.json"
        ))
        .unwrap();
        assert_eq!(
            err_class(map_view(&env["data"], "BV1other")),
            FailureClass::SchemaChanged
        );
        let empty = json!({"bvid": "BV1x", "pages": []});
        assert_eq!(
            err_class(map_view(&empty, "BV1x")),
            FailureClass::SourceUnavailable
        );
    }
}
