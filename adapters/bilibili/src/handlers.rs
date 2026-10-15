//! 插件方法实现：解析账号 / 枚举投稿 / 详情观察 / 媒体解析 / 登录探测。
//!
//! 纯映射函数（map_*）与 IO 分离，直接以 tests/fixtures/bilibili 的
//! 结构样本做单元测试（bilibili-capabilities §4 要求断言归档语义）。

use crate::api;
use crate::{AdapterState, wbi};
use serde_json::{Value, json};
use std::time::{Duration, Instant};
use trace_contracts::plugin::{
    AdapterHello, AdapterLimits, EnumerateParams, EnumeratedEntry, EnumeratedPage, MediaCandidate,
    ObserveParams, ObservedDetail, ProbeAuthResult, ResolveAccountParams, ResolveMediaParams,
    ResolveMediaResult, ResolvedAccount, ResourceIdentityDto, UrlSet, methods,
};
use trace_domain::PLUGIN_PROTOCOL_VERSION;
use trace_domain::errors::FailureClass;

/// 每页条数（站点 wbi/arc/search 上限 50，取保守 30）。
const PAGE_SIZE: u32 = 30;
/// Request source maxima; availability still depends on the supplied session.
const REQUESTED_QN: u64 = 127;
const FNVAL: u64 = 4048;

pub fn dispatch(
    state: &AdapterState,
    req: &trace_contracts::plugin::PluginRequest,
    deadline: Duration,
    started: Instant,
) -> Result<Value, trace_contracts::plugin::PluginError> {
    match req.method.as_str() {
        "resolve_source" => crate::source::resolve(state, &req.params, deadline, started)
            .map(|v| serde_json::to_value(v).unwrap()),
        "sample_live_auxiliary" => crate::live::auxiliary(state, &req.params, deadline, started)
            .map(|v| serde_json::to_value(v).unwrap()),
        "probe_live" => crate::live::probe(state, &req.params, deadline, started)
            .map(|v| serde_json::to_value(v).unwrap()),
        methods::HELLO => Ok(serde_json::to_value(hello()).unwrap_or_default()),
        methods::RESOLVE_ACCOUNT => {
            let p: ResolveAccountParams =
                serde_json::from_value(req.params.clone()).map_err(invalid_payload)?;
            resolve_account(state, &p, deadline, started)
                .map(|v| serde_json::to_value(v).unwrap_or_default())
        }
        methods::ENUMERATE => {
            let p: EnumerateParams =
                serde_json::from_value(req.params.clone()).map_err(invalid_payload)?;
            if p.content_type != "video" {
                return crate::content::enumerate(state, &p, deadline, started)
                    .map(|v| serde_json::to_value(v).unwrap());
            }
            enumerate(state, &p, deadline, started)
                .map(|v| serde_json::to_value(v).unwrap_or_default())
        }
        methods::OBSERVE => {
            let p: ObserveParams =
                serde_json::from_value(req.params.clone()).map_err(invalid_payload)?;
            if p.native_kind != "video" {
                return crate::content::observe(state, &p, deadline, started)
                    .map(|v| serde_json::to_value(v.0).unwrap());
            }
            observe(state, &p, deadline, started)
                .map(|v| serde_json::to_value(v).unwrap_or_default())
        }
        methods::RESOLVE_MEDIA => {
            let p: ResolveMediaParams =
                serde_json::from_value(req.params.clone()).map_err(invalid_payload)?;
            if p.native_kind != "video" {
                let (detail, candidates) = crate::content::observe(
                    state,
                    &ObserveParams {
                        native_entry_key: p.native_entry_key.clone(),
                        native_kind: p.native_kind.clone(),
                    },
                    deadline,
                    started,
                )?;
                return Ok(serde_json::to_value(ResolveMediaResult {
                    candidates: candidates
                        .into_iter()
                        .filter(|c| {
                            p.resources
                                .iter()
                                .any(|r| r.source_attachment_id == c.resource.source_attachment_id)
                        })
                        .collect(),
                    notes: json!({"silent_or_muxed":detail.source_version_evidence["silent_or_muxed"]}),
                })
                .unwrap());
            }
            resolve_media(state, &p, deadline, started)
                .map(|v| serde_json::to_value(v).unwrap_or_default())
        }
        methods::PROBE_AUTH => probe_auth(state, deadline, started)
            .map(|v| serde_json::to_value(v).unwrap_or_default()),
        methods::CANCEL | methods::SHUTDOWN => Ok(json!({"ack": true})),
        other => Err(trace_contracts::plugin::PluginError {
            class: FailureClass::Unsupported,
            message: format!("unsupported method {other}"),
            retry_after_ms: None,
            details: Value::Null,
        }),
    }
}

fn invalid_payload(e: serde_json::Error) -> trace_contracts::plugin::PluginError {
    trace_contracts::plugin::PluginError {
        class: FailureClass::InvalidPayload,
        message: format!("bad params: {e}"),
        retry_after_ms: None,
        details: Value::Null,
    }
}

fn hello() -> AdapterHello {
    AdapterHello {
        protocol_version: PLUGIN_PROTOCOL_VERSION.into(),
        adapter_version: env!("CARGO_PKG_VERSION").into(),
        platform: "bilibili".into(),
        capabilities: vec![
            methods::RESOLVE_ACCOUNT.into(),
            methods::ENUMERATE.into(),
            methods::OBSERVE.into(),
            methods::RESOLVE_MEDIA.into(),
            methods::PROBE_AUTH.into(),
            "probe_live".into(),
            "sample_live_auxiliary".into(),
            "resolve_source".into(),
            "plugin_chunks_v1".into(),
        ],
        limits: AdapterLimits {
            max_page_items: PAGE_SIZE,
            supports_cancel: false,
        },
    }
}

// ---------- resolve_account ----------

/// mid 或 space 主页 URL → 平台身份；返回 mid 与 card 交叉核对。
fn resolve_account(
    state: &AdapterState,
    p: &ResolveAccountParams,
    deadline: Duration,
    started: Instant,
) -> Result<ResolvedAccount, trace_contracts::plugin::PluginError> {
    let Some(mid) = extract_mid(&p.input) else {
        return Err(trace_contracts::plugin::PluginError {
            class: FailureClass::InvalidPayload,
            message: "input is not a bilibili mid or space URL".into(),
            retry_after_ms: None,
            details: Value::Null,
        });
    };
    let url = format!("{}?mid={mid}", api::CARD_URL);
    let v = api::get_json(state, &url, false, deadline, started)?;
    let env = api::envelope(&v)?;
    let card_mid = env
        .data
        .pointer("/card/mid")
        .and_then(|m| {
            m.as_str()
                .map(str::to_string)
                .or_else(|| m.as_i64().map(|n| n.to_string()))
        })
        .ok_or_else(|| trace_contracts::plugin::PluginError {
            class: FailureClass::SchemaChanged,
            message: "card response missing card.mid".into(),
            retry_after_ms: None,
            details: Value::Null,
        })?;
    if card_mid != mid {
        return Err(trace_contracts::plugin::PluginError {
            class: FailureClass::SchemaChanged,
            message: "card mid mismatch with requested identity".into(),
            retry_after_ms: None,
            details: json!({"requested": mid, "returned": card_mid}),
        });
    }
    Ok(ResolvedAccount {
        native_account_key: mid.clone(),
        display_name: env
            .data
            .pointer("/card/name")
            .and_then(|n| n.as_str())
            .map(str::to_string),
        profile_url: Some(format!("https://space.bilibili.com/{mid}")),
        evidence: json!({"source": "card", "mid": mid,"profile":trace_contracts::content::safe_metadata(&env.data)}),
    })
}

/// 输入是纯数字 mid 或 space.bilibili.com/{mid} 链接。
fn extract_mid(input: &str) -> Option<String> {
    let t = input.trim();
    if !t.is_empty() && t.chars().all(|c| c.is_ascii_digit()) {
        return Some(t.to_string());
    }
    let idx = t.find("space.bilibili.com/")?;
    let rest = &t[idx + "space.bilibili.com/".len()..];
    let digits: String = rest.chars().take_while(|c| c.is_ascii_digit()).collect();
    if digits.is_empty() {
        None
    } else {
        Some(digits)
    }
}

// ---------- WBI 基础设施 ----------

fn wbi_error(e: &trace_contracts::plugin::PluginError) -> bool {
    e.class == FailureClass::PermissionDenied || e.details.get("api_code") == Some(&json!(-352))
}

fn unix_now() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
}

/// 从 nav 响应提取 wbi 密钥对并导出混淆 key。
pub fn wbi_key_from_nav(nav_data: &Value) -> Option<String> {
    let img_url = nav_data.pointer("/wbi_img/img_url")?.as_str()?;
    let sub_url = nav_data.pointer("/wbi_img/sub_url")?.as_str()?;
    let img = img_url.rsplit('/').next()?.split('.').next()?;
    let sub = sub_url.rsplit('/').next()?.split('.').next()?;
    if [img, sub]
        .into_iter()
        .any(|s| s.len() != 32 || !s.bytes().all(|b| b.is_ascii_hexdigit()))
    {
        return None;
    }
    Some(wbi::mixin_key(img, sub))
}

fn wbi_key_from_response(v: &Value) -> Result<String, trace_contracts::plugin::PluginError> {
    // Logged-out nav still publishes WBI keys. That explicit envelope is only
    // useful for signing; it is never accepted by probe_auth as a login proof.
    let data = if v["code"] == -101
        && v["data"]["isLogin"] == false
        && v["data"].get("v_voucher").is_none()
    {
        v["data"].clone()
    } else {
        api::envelope(v)?.data
    };
    wbi_key_from_nav(&data).ok_or_else(|| trace_contracts::plugin::PluginError {
        class: FailureClass::SchemaChanged,
        message: "nav response missing valid wbi_img keys".into(),
        retry_after_ms: None,
        details: Value::Null,
    })
}

/// 取缓存内的混淆 key；不新鲜则拉一次 nav。
fn ensure_wbi_key(
    state: &AdapterState,
    deadline: Duration,
    started: Instant,
) -> Result<String, trace_contracts::plugin::PluginError> {
    {
        let cache = state.wbi.lock().unwrap();
        if cache.fresh() {
            return Ok(cache.key.clone().unwrap_or_default());
        }
    }
    let v = api::get_json(state, api::NAV_URL, true, deadline, started)?;
    let key = wbi_key_from_response(&v)?;
    state.wbi.lock().unwrap().key = Some(key.clone());
    state.wbi.lock().unwrap().fetched_at = Some(Instant::now());
    Ok(key)
}

/// WBI 签名 GET；签名失效刷新密钥重试一次（不无限重试）。
pub(crate) fn signed_get(
    state: &AdapterState,
    base: &str,
    params: &[(String, String)],
    with_credential: bool,
    deadline: Duration,
    started: Instant,
) -> Result<Value, trace_contracts::plugin::PluginError> {
    let mut refreshed = false;
    loop {
        let mixin = ensure_wbi_key(state, deadline, started)?;
        let wts = unix_now();
        let url = wbi::signed_url(base, params, &mixin, wts);
        match api::get_json(state, &url, with_credential, deadline, started) {
            Ok(v) => {
                let env = api::envelope(&v)?;
                return Ok(env.data);
            }
            Err(e) if !refreshed && wbi_error(&e) => {
                {
                    let mut cache = state.wbi.lock().unwrap();
                    cache.key = None;
                    cache.fetched_at = None;
                }
                refreshed = true;
            }
            Err(e) => return Err(e),
        }
    }
}

// ---------- enumerate ----------

fn enumerate(
    state: &AdapterState,
    p: &EnumerateParams,
    deadline: Duration,
    started: Instant,
) -> Result<EnumeratedPage, trace_contracts::plugin::PluginError> {
    let mid = p.native_account_key.trim().to_string();
    if mid.is_empty() || !mid.chars().all(|c| c.is_ascii_digit()) {
        return Err(trace_contracts::plugin::PluginError {
            class: FailureClass::InvalidPayload,
            message: "native_account_key must be a bilibili mid".into(),
            retry_after_ms: None,
            details: Value::Null,
        });
    }
    let pn: u32 = p
        .cursor
        .as_deref()
        .and_then(|c| c.parse().ok())
        .unwrap_or(1);
    let params = vec![
        ("mid".to_string(), mid),
        ("order".to_string(), "pubdate".to_string()),
        ("pn".to_string(), pn.to_string()),
        ("ps".to_string(), PAGE_SIZE.to_string()),
    ];
    let data = signed_get(state, api::ARC_SEARCH_URL, &params, true, deadline, started)?;
    let cutoff: Option<i64> = p
        .recent_days
        .filter(|d| *d > 0)
        .map(|d| unix_now() as i64 - d * 86_400);
    let count = data.pointer("/page/count").and_then(|c| c.as_i64());
    let (items, truncated_by_cutoff) = map_vlist(&data, cutoff)?;
    // Short/filtered pages are not proof that a source count is exhausted.
    let within_count = match (&count, pn) {
        (Some(c), pn) => (pn as i64) * (PAGE_SIZE as i64) < *c,
        (None, _) => false,
    };
    let has_more = within_count;
    Ok(EnumeratedPage {
        items,
        next_cursor: has_more.then(|| (pn + 1).to_string()),
        has_more,
        coverage: json!({
            "count_observed": count,
            "page": pn,
            "ps": PAGE_SIZE,
            "order": "pubdate",
            "wbi": wbi::ALGO_VERSION,
            "cutoff_hit": truncated_by_cutoff,
        }),
    })
}

/// vlist → 条目（按项过滤日期，置顶/乱序不终止后续读取；
/// 空列表不宣称"无作品"——has_more=false 只表示本轮页边界）。
fn map_vlist(
    data: &Value,
    cutoff: Option<i64>,
) -> Result<(Vec<EnumeratedEntry>, bool), trace_contracts::plugin::PluginError> {
    let vlist = data
        .pointer("/list/vlist")
        .and_then(|v| v.as_array())
        .ok_or_else(|| trace_contracts::plugin::PluginError {
            class: FailureClass::SchemaChanged,
            message: "arc search response missing list.vlist".into(),
            retry_after_ms: None,
            details: Value::Null,
        })?;
    let mut items = Vec::new();
    let mut truncated = false;
    for v in vlist {
        let created = v.get("created").and_then(|c| c.as_i64()).unwrap_or(0);
        if let Some(cutoff) = cutoff
            && created > 0
            && created < cutoff
        {
            truncated = true;
            continue;
        }
        let bvid = v
            .get("bvid")
            .and_then(|b| b.as_str())
            .unwrap_or("")
            .to_string();
        if bvid.is_empty() {
            return Err(trace_contracts::plugin::PluginError {
                class: FailureClass::SchemaChanged,
                message: "vlist entry missing bvid".into(),
                retry_after_ms: None,
                details: Value::Null,
            });
        }
        items.push(EnumeratedEntry {
            native_entry_key: bvid,
            native_kind: "video".into(),
            title: v
                .get("title")
                .and_then(|t| t.as_str())
                .unwrap_or("")
                .to_string(),
            published_at: (created > 0).then(|| rfc3339(created)),
            published_precision: "source".into(),
            pinned: false,
            summary_body: v
                .get("description")
                .and_then(|d| d.as_str())
                .map(str::to_string),
            stats: json!({
                "play": v.get("play"),
                "review": v.get("review"),
                "video_review": v.get("video_review"),
                "favorites": v.get("favorites"),
                "length": v.get("length"),
            }),
        });
    }
    Ok((items, truncated))
}

// ---------- observe ----------

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

fn player_info(
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

fn player_subtitles(
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
                    c.public_headers = public_headers();
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

// ---------- resolve_media ----------

pub(crate) fn resolve_media(
    state: &AdapterState,
    p: &ResolveMediaParams,
    deadline: Duration,
    started: Instant,
) -> Result<ResolveMediaResult, trace_contracts::plugin::PluginError> {
    // 同页 video/audio 共享一次 playurl 调用（cid → 响应）。
    let mut cache: Vec<(String, Value)> = Vec::new();
    let mut player_cache: Vec<(String, Value)> = Vec::new();
    let mut candidates: Vec<MediaCandidate> = Vec::new();
    let mut notes = json!({
        "quality_requested": REQUESTED_QN,
        "fnval": FNVAL,
        "wbi": wbi::ALGO_VERSION,
        "silent_or_muxed": [],
        "rejected_streams": [],
    });
    for resource in &p.resources {
        if resource.role == "cover" {
            let data = signed_get(
                state,
                api::VIEW_URL,
                &[("bvid".into(), p.native_entry_key.clone())],
                true,
                deadline,
                started,
            )?;
            if let Some(pic) = data["pic"].as_str().filter(|s| !s.is_empty()) {
                candidates.push(MediaCandidate {
                    resource: resource.clone(),
                    representation: "cover-v1".into(),
                    kind: if pic.split('?').next().unwrap_or(pic).ends_with(".png") {
                        "image_png".into()
                    } else {
                        "image_jpeg".into()
                    },
                    segments: vec![url_set(pic, &Value::Null)],
                    public_headers: public_headers(),
                    expected_size: None,
                    expected_sha256: None,
                    claimed_spec: json!({}),
                });
            }
            continue;
        }
        let Some((bvid, cid)) = parse_sai(&resource.source_attachment_id) else {
            continue;
        };
        if resource.role == "subtitle" {
            let data = match player_cache.iter().find(|(c, _)| c == &cid) {
                Some((_, v)) => v.clone(),
                None => {
                    let v = player_info(state, &bvid, &cid, deadline, started)?;
                    player_cache.push((cid.clone(), v.clone()));
                    v
                }
            };
            candidates.extend(
                player_subtitles(&data, &bvid, &cid)
                    .1
                    .into_iter()
                    .filter(|c| c.resource.source_attachment_id == resource.source_attachment_id),
            );
            continue;
        }
        let data = match cache.iter().find(|(c, _)| c == &cid) {
            Some((_, v)) => v.clone(),
            None => {
                let params = vec![
                    ("bvid".to_string(), bvid.clone()),
                    ("cid".to_string(), cid.clone()),
                    ("qn".to_string(), REQUESTED_QN.to_string()),
                    ("fnval".to_string(), FNVAL.to_string()),
                    ("fourk".to_string(), "1".to_string()),
                ];
                let v = signed_get(state, api::PLAYURL_URL, &params, true, deadline, started)?;
                cache.push((cid.clone(), v.clone()));
                v
            }
        };
        match resource.role.as_str() {
            "video" if p.quality_hint["all_variants"] == true => {
                candidates.extend(all_video_candidates(&data, resource)?)
            }
            "audio" if p.quality_hint["all_variants"] == true => {
                let audio = all_audio_candidates(&data, resource);
                if audio.is_empty() && has_silent_or_muxed_evidence(&data) {
                    notes["silent_or_muxed"]
                        .as_array_mut()
                        .unwrap()
                        .push(json!(cid));
                }
                candidates.extend(audio);
            }
            "video" => candidates.extend(map_video_candidates_for(
                &data,
                resource,
                p.quality_hint["prefer_fps"] == true,
            )?),
            "audio" => match map_audio_candidate(&data, resource) {
                Some(c) => candidates.push(c),
                None if has_silent_or_muxed_evidence(&data) => notes["silent_or_muxed"]
                    .as_array_mut()
                    .expect("array")
                    .push(json!(cid)),
                None => {}
            },
            _ => {}
        }
    }
    Ok(ResolveMediaResult { candidates, notes })
}

// Missing candidate data alone cannot waive required audio.
fn has_silent_or_muxed_evidence(data: &Value) -> bool {
    data.get("durl")
        .and_then(Value::as_array)
        .is_some_and(|a| !a.is_empty())
        || data
            .pointer("/dash/audio")
            .and_then(Value::as_array)
            .is_some_and(|a| a.is_empty())
}

/// "{bvid}:{cid}:{page}:{kind}" → (bvid, cid)。
fn parse_sai(sai: &str) -> Option<(String, String)> {
    let parts: Vec<&str> = sai.split(':').collect();
    if parts.len() == 4 {
        Some((parts[0].to_string(), parts[1].to_string()))
    } else {
        None
    }
}

fn public_headers() -> Value {
    json!({"referer": api::REFERER, "user-agent": api::UA})
}

fn url_set(primary: &str, backups: &Value) -> UrlSet {
    UrlSet {
        primary: primary.to_string(),
        backups: backups
            .as_array()
            .map(|a| {
                a.iter()
                    .filter_map(|u| u.as_str())
                    .map(str::to_string)
                    .collect()
            })
            .unwrap_or_default(),
        expected_size: None,
        expected_sha256: None,
    }
}

/// 视频/混流候选：DASH 优先（AVC 优先按带宽），否则 durl 全段。
#[cfg(test)]
fn map_video_candidates(
    data: &Value,
    resource: &ResourceIdentityDto,
) -> Result<Vec<MediaCandidate>, trace_contracts::plugin::PluginError> {
    map_video_candidates_for(data, resource, false)
}

fn map_video_candidates_for(
    data: &Value,
    resource: &ResourceIdentityDto,
    prefer_fps: bool,
) -> Result<Vec<MediaCandidate>, trace_contracts::plugin::PluginError> {
    let fail = |class, msg: &str| trace_contracts::plugin::PluginError {
        class,
        message: msg.to_string(),
        retry_after_ms: None,
        details: json!({"cid": resource.source_attachment_id}),
    };
    if let Some(dash) = data.get("dash") {
        let videos = dash
            .get("video")
            .and_then(|v| v.as_array())
            .ok_or_else(|| fail(FailureClass::SchemaChanged, "dash missing video array"))?;
        if videos.is_empty() {
            return Err(fail(
                FailureClass::SourceUnavailable,
                "dash video list empty",
            ));
        }
        // Source quality preference first; codec compatibility breaks ties.
        let mut streams: Vec<&Value> = videos.iter().collect();
        streams.sort_by_key(|s| {
            let codec = s.get("codecs").and_then(|c| c.as_str()).unwrap_or("");
            let avc = codec.starts_with("avc") as i64;
            let bw = s.get("bandwidth").and_then(|b| b.as_i64()).unwrap_or(0);
            (
                std::cmp::Reverse(trace_contracts::content::source_rank(s, prefer_fps)),
                std::cmp::Reverse(avc),
                std::cmp::Reverse(bw),
            )
        });
        let best = streams[0];
        let codec = best
            .get("codecs")
            .and_then(|c| c.as_str())
            .unwrap_or("unknown");
        let id = best.get("id").and_then(|i| i.as_i64()).unwrap_or(0);
        let w = best.get("width").and_then(|b| b.as_i64()).unwrap_or(0);
        let h = best.get("height").and_then(|b| b.as_i64()).unwrap_or(0);
        let base = best
            .get("base_url")
            .and_then(|u| u.as_str())
            .or_else(|| best.get("url").and_then(|u| u.as_str()))
            .unwrap_or("");
        if base.is_empty() {
            return Err(fail(
                FailureClass::SchemaChanged,
                "dash video missing base_url",
            ));
        }
        let representation = format!(
            "dash-{id}-{}-{}x{}",
            codec.split('.').next().unwrap_or("codec"),
            w,
            h
        );
        return Ok(vec![MediaCandidate {
            resource: resource.clone(),
            representation,
            kind: "dash_video".into(),
            segments: vec![url_set(
                base,
                best.get("backup_url").unwrap_or(&Value::Null),
            )],
            public_headers: public_headers(),
            expected_size: None,
            expected_sha256: None,
            claimed_spec: json!({
                "codec": codec,
                "width": w,
                "height": h,
                "frame_rate": best.get("frame_rate"),
                "bandwidth": best.get("bandwidth"),
                "quality_id": id,
                "container": "m4s",
            }),
        }]);
    }
    if let Some(durl) = data.get("durl").and_then(|d| d.as_array()) {
        if durl.is_empty() {
            return Err(fail(FailureClass::SourceUnavailable, "durl list empty"));
        }
        // durl 数组必须全段按序（不能只取 [0]）。
        let mut segments = Vec::new();
        let mut total = Some(0i64);
        for seg in durl {
            let primary = seg
                .get("url")
                .and_then(|u| u.as_str())
                .map(str::to_string)
                .or_else(|| {
                    seg.get("url")
                        .and_then(|u| u.as_array())
                        .and_then(|a| a.first())
                        .and_then(|u| u.as_str())
                        .map(str::to_string)
                })
                .unwrap_or_default();
            if primary.is_empty() {
                return Err(fail(
                    FailureClass::SchemaChanged,
                    "durl segment missing url",
                ));
            }
            let size = seg.get("size").and_then(|s| s.as_i64()).filter(|n| *n > 0);
            total = total.zip(size).and_then(|(sum, n)| sum.checked_add(n));
            let mut urls = url_set(&primary, seg.get("backup_url").unwrap_or(&Value::Null));
            urls.expected_size = size;
            segments.push(urls);
        }
        let qn = data.get("quality").and_then(|q| q.as_i64()).unwrap_or(0);
        return Ok(vec![MediaCandidate {
            resource: resource.clone(),
            representation: format!("durl-qn{qn}-segments{len}", len = segments.len()),
            kind: "durl_segment".into(),
            segments,
            public_headers: public_headers(),
            expected_size: total,
            expected_sha256: None,
            claimed_spec: json!({"container": "flv360+", "quality": qn, "segments": durl.len()}),
        }]);
    }
    Err(fail(
        FailureClass::SourceUnavailable,
        "no dash or durl streams (empty flow)",
    ))
}

fn all_video_candidates(
    data: &Value,
    r: &ResourceIdentityDto,
) -> Result<Vec<MediaCandidate>, trace_contracts::plugin::PluginError> {
    let Some(streams) = data.pointer("/dash/video").and_then(Value::as_array) else {
        return map_video_candidates_for(data, r, false);
    };
    if streams.len() > 128 {
        return Err(trace_site_support::error(
            FailureClass::InvalidPayload,
            "video offer budget",
        ));
    }
    let mut result = Vec::new();
    for stream in streams {
        let mut one = data.clone();
        one["dash"]["video"] = json!([stream]);
        result.extend(map_video_candidates_for(&one, r, false)?);
    }
    result.sort_by_key(|c| {
        std::cmp::Reverse(trace_contracts::content::source_rank(
            &c.claimed_spec,
            false,
        ))
    });
    Ok(result)
}
fn all_audio_candidates(data: &Value, r: &ResourceIdentityDto) -> Vec<MediaCandidate> {
    let mut result = Vec::new();
    if let Some(streams) = data.pointer("/dash/audio").and_then(Value::as_array) {
        for stream in streams.iter().take(32) {
            let mut one = data.clone();
            one["dash"]["audio"] = json!([stream]);
            if let Some(c) = map_audio_candidate(&one, r) {
                result.push(c);
            }
        }
    }
    result
}

/// 音频候选：DASH 音轨按 AAC 优先取最高带宽；无声/混流返回 None。
fn map_audio_candidate(data: &Value, resource: &ResourceIdentityDto) -> Option<MediaCandidate> {
    let audios = data.pointer("/dash/audio")?.as_array()?;
    if audios.is_empty() {
        return None;
    }
    let mut streams: Vec<&Value> = audios.iter().collect();
    streams.sort_by_key(|s| {
        let codec = s.get("codecs").and_then(|c| c.as_str()).unwrap_or("");
        let aac = codec.contains("mp4a") as i64;
        let bw = s.get("bandwidth").and_then(|b| b.as_i64()).unwrap_or(0);
        (std::cmp::Reverse(aac), std::cmp::Reverse(bw))
    });
    let best = streams[0];
    let codec = best
        .get("codecs")
        .and_then(|c| c.as_str())
        .unwrap_or("unknown");
    let id = best.get("id").and_then(|i| i.as_i64()).unwrap_or(0);
    let base = best
        .get("base_url")
        .and_then(|u| u.as_str())
        .or_else(|| best.get("url").and_then(|u| u.as_str()))?;
    if base.is_empty() {
        return None;
    }
    Some(MediaCandidate {
        resource: resource.clone(),
        representation: format!(
            "dash-audio-{id}-{}",
            codec.split('.').next().unwrap_or("codec")
        ),
        kind: "dash_audio".into(),
        segments: vec![url_set(
            base,
            best.get("backup_url").unwrap_or(&Value::Null),
        )],
        public_headers: public_headers(),
        expected_size: None,
        expected_sha256: None,
        claimed_spec: json!({
            "codec": codec,
            "bandwidth": best.get("bandwidth"),
            "quality_id": id,
            "container": "m4s",
        }),
    })
}

// ---------- probe_auth ----------

fn probe_auth(
    state: &AdapterState,
    deadline: Duration,
    started: Instant,
) -> Result<ProbeAuthResult, trace_contracts::plugin::PluginError> {
    let v = api::get_json(state, api::NAV_URL, true, deadline, started)?;
    let env = api::envelope(&v)?;
    let is_login = env
        .data
        .get("isLogin")
        .and_then(|b| b.as_bool())
        .unwrap_or(false);
    Ok(ProbeAuthResult {
        state: if is_login {
            "authenticated"
        } else {
            "anonymous"
        }
        .into(),
        capability: json!({
            "wbi": true,
            "vip_status": env.data.pointer("/vipStatus").cloned().unwrap_or(Value::Null),
        }),
    })
}

// ---------- 时间 ----------

pub fn rfc3339(unix: i64) -> String {
    use time::format_description::well_known::Rfc3339;
    time::OffsetDateTime::from_unix_timestamp(unix)
        .map(|t| t.format(&Rfc3339).unwrap_or_default())
        .unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn err_class(r: Result<ObservedDetail, trace_contracts::plugin::PluginError>) -> FailureClass {
        r.unwrap_err().class
    }

    #[test]
    fn extract_mid_forms() {
        assert_eq!(extract_mid("12345").as_deref(), Some("12345"));
        assert_eq!(
            extract_mid("https://space.bilibili.com/94697/video").as_deref(),
            Some("94697")
        );
        assert_eq!(
            extract_mid("space.bilibili.com/94697/").as_deref(),
            Some("94697")
        );
        assert!(extract_mid("https://example.com/x").is_none());
        assert!(extract_mid("space.bilibili.com/").is_none());
    }

    #[test]
    fn wbi_key_from_nav_fixture() {
        let nav: Value =
            serde_json::from_str(include_str!("../../../tests/fixtures/bilibili/nav.json"))
                .unwrap();
        let key = wbi_key_from_nav(&nav["data"]).unwrap();
        // 固定夹具密钥 → 已知混淆 key。
        assert_eq!(key, "ea1db124af3c7062474693fa704f4ff8");
    }

    #[test]
    fn c14_public_wbi_keys_do_not_prove_login_or_bypass_challenges() {
        let mut guest: Value =
            serde_json::from_str(include_str!("../../../tests/fixtures/bilibili/nav.json"))
                .unwrap();
        guest["code"] = json!(-101);
        assert_eq!(guest["data"]["isLogin"], false);
        assert_eq!(
            wbi_key_from_response(&guest).unwrap(),
            "ea1db124af3c7062474693fa704f4ff8"
        );
        assert_eq!(
            api::envelope(&guest).unwrap_err().class,
            FailureClass::AuthRequired
        );
        for code in [-403, -352] {
            let mut denied = guest.clone();
            denied["code"] = json!(code);
            assert!(wbi_key_from_response(&denied).is_err());
        }
        let mut inconsistent = guest.clone();
        inconsistent["data"]["isLogin"] = json!(true);
        assert!(wbi_key_from_response(&inconsistent).is_err());
        for code in [0, -101] {
            let mut challenge = guest.clone();
            challenge["code"] = json!(code);
            challenge["data"]["v_voucher"] = json!("fixture-challenge");
            assert!(wbi_key_from_response(&challenge).is_err());
        }
        for value in [Value::Null, json!("https://i0.hdslb.com/bfs/wbi/short.png")] {
            let mut malformed = guest.clone();
            malformed["data"]["wbi_img"]["img_url"] = value;
            assert_eq!(
                wbi_key_from_response(&malformed).unwrap_err().class,
                FailureClass::SchemaChanged
            );
        }
    }

    #[test]
    fn enumerate_page_maps_entries_and_cursor() {
        let env: Value = serde_json::from_str(include_str!(
            "../../../tests/fixtures/bilibili/arc_search_page1.json"
        ))
        .unwrap();
        let data = env["data"].clone();
        let (items, truncated) = map_vlist(&data, None).unwrap();
        assert_eq!(items.len(), 3);
        assert!(!truncated);
        assert_eq!(items[0].native_entry_key, "BV1fakeBVfake1");
        assert_eq!(items[0].native_kind, "video");
        assert_eq!(items[0].published_precision, "source");
        assert!(
            items[0]
                .published_at
                .as_deref()
                .unwrap_or("")
                .ends_with('Z')
        );
        // recent_days 截断：cutoff 在所有条目之后 → 立即截断且不产出。
        let (items2, truncated2) = map_vlist(&data, Some(3_000_000_000)).unwrap();
        assert!(truncated2);
        assert!(items2.is_empty());
        // cutoff 在最旧一条之后 1 秒 → 只保留更新的两条。
        let (items3, truncated3) = map_vlist(&data, Some(rfc3339_past_cutoff(&items))).unwrap();
        assert!(truncated3);
        assert_eq!(items3.len(), 2, "only entries newer than cutoff remain");
    }

    fn rfc3339_past_cutoff(items: &[EnumeratedEntry]) -> i64 {
        // 取最后一条（最旧）的发布秒，加一秒作为 cutoff。
        let oldest = items.last().unwrap().published_at.as_deref().unwrap();
        use time::format_description::well_known::Rfc3339;
        time::OffsetDateTime::parse(oldest, &Rfc3339)
            .unwrap()
            .unix_timestamp()
            + 1
    }

    #[test]
    fn c01_pinned_old_video_cannot_hide_later_recent_or_unknown_dates() {
        let data = json!({"list":{"vlist":[{"bvid":"old","created":100},{"bvid":"new","created":300},{"bvid":"unknown"},{"bvid":"later","created":250}]}});
        let (items, filtered) = map_vlist(&data, Some(200)).unwrap();
        assert!(filtered);
        assert_eq!(
            items
                .iter()
                .map(|v| v.native_entry_key.as_str())
                .collect::<Vec<_>>(),
            vec!["new", "unknown", "later"]
        );
        assert!(items[1].published_at.is_none());
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

    fn resources_for(bvid: &str, cid: i64, page: i64) -> Vec<ResourceIdentityDto> {
        vec![
            ResourceIdentityDto {
                role: "video".into(),
                source_attachment_id: format!("{bvid}:{cid}:{page}:video"),
                position: page,
                title: None,
                group_key: Some(cid.to_string()),
                required: true,
            },
            ResourceIdentityDto {
                role: "audio".into(),
                source_attachment_id: format!("{bvid}:{cid}:{page}:audio"),
                position: page,
                title: None,
                group_key: Some(cid.to_string()),
                required: false,
            },
        ]
    }

    #[test]
    fn playurl_dash_prefers_avc_and_maps_audio() {
        let env: Value = serde_json::from_str(include_str!(
            "../../../tests/fixtures/bilibili/playurl_dash.json"
        ))
        .unwrap();
        let data = env["data"].clone();
        let res = resources_for("BV1fakeBVfake1", 1001, 1);
        let mut cands = map_video_candidates(&data, &res[0]).unwrap();
        assert_eq!(cands.len(), 1);
        let c = cands.remove(0);
        // AVC 优先于更高带宽的 HEVC（兼容优先，不承诺最高质量）。
        assert!(c.representation.starts_with("dash-80-avc1-"));
        assert_eq!(c.kind, "dash_video");
        assert_eq!(c.segments.len(), 1);
        assert!(c.segments[0].primary.starts_with("https://"));
        assert!(!c.segments[0].backups.is_empty());
        assert_eq!(c.claimed_spec["container"], "m4s");
        let audio = map_audio_candidate(&data, &res[1]).unwrap();
        assert_eq!(audio.kind, "dash_audio");
        // 同为 AAC 时取最高带宽（30280 = 132k）。
        assert!(audio.representation.starts_with("dash-audio-30280-mp4a"));
    }

    #[test]
    fn playurl_durl_requires_all_segments() {
        let env: Value = serde_json::from_str(include_str!(
            "../../../tests/fixtures/bilibili/playurl_durl.json"
        ))
        .unwrap();
        let data = env["data"].clone();
        let res = resources_for("BV1fakeBVfake3", 3001, 1);
        let mut cands = map_video_candidates(&data, &res[0]).unwrap();
        assert_eq!(cands.len(), 1);
        let c = cands.remove(0);
        assert_eq!(c.kind, "durl_segment");
        assert_eq!(c.segments.len(), 2, "durl segments must all be listed");
        assert_eq!(c.expected_size, Some(4_000_000));
        assert_eq!(
            c.segments[0].expected_size,
            data["durl"][0]["size"].as_i64()
        );
        assert_eq!(
            c.segments[1].expected_size,
            data["durl"][1]["size"].as_i64()
        );
        let mut partial = data.clone();
        partial["durl"][1]["size"] = Value::Null;
        let partial = map_video_candidates(&partial, &res[0]).unwrap().remove(0);
        assert_eq!(
            partial.expected_size, None,
            "an unknown segment makes the aggregate unknown"
        );
        assert_eq!(
            partial.segments[0].expected_size,
            c.segments[0].expected_size
        );
        assert_eq!(partial.segments[1].expected_size, None);
        // 混流：无独立音频候选。
        assert!(map_audio_candidate(&data, &res[1]).is_none());
    }

    #[test]
    fn playurl_silent_dash_yields_no_audio_candidate() {
        let env: Value = serde_json::from_str(include_str!(
            "../../../tests/fixtures/bilibili/playurl_dash_silent.json"
        ))
        .unwrap();
        let data = env["data"].clone();
        let res = resources_for("BV1fakeBVfake4", 4001, 1);
        assert!(map_audio_candidate(&data, &res[1]).is_none());
        assert!(map_video_candidates(&data, &res[0]).is_ok());
    }

    #[test]
    fn playurl_empty_flow_is_source_unavailable() {
        let data = json!({"quality": 80, "dash": {"video": [], "audio": []}});
        let res = resources_for("BV1x", 1, 1);
        let e = map_video_candidates(&data, &res[0]).unwrap_err();
        assert_eq!(e.class, FailureClass::SourceUnavailable);
    }
    #[test]
    fn f009_audio_waiver_requires_explicit_silent_or_muxed_evidence() {
        assert!(!has_silent_or_muxed_evidence(&json!({})));
        assert!(!has_silent_or_muxed_evidence(&json!({"dash":{"video":[]}})));
        assert!(!has_silent_or_muxed_evidence(
            &json!({"dash":{"audio":[{"id":30280}]}})
        ));
        assert!(has_silent_or_muxed_evidence(&json!({"dash":{"audio":[]}})));
        assert!(has_silent_or_muxed_evidence(
            &json!({"durl":[{"url":"https://cdn.invalid/a.flv"}]})
        ));
    }
}
