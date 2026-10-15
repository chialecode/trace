//! Content detail: fetch, resolve related content (quoted videos, articles, music,
//! reposts) within fixed depth/count budgets, and merge it into the parent.
use super::map::{card_identity, map_detail};
use super::{fail, get, opus_detail};
use crate::AdapterState;
use serde_json::{Value, json};
use std::time::{Duration, Instant};
use trace_contracts::content::Block;
use trace_contracts::plugin::{
    MediaCandidate, ObserveParams, ObservedDetail, PluginError, ResolveMediaParams,
    ResourceIdentityDto,
};
use trace_site_support::id;

const MAX_RELATED_DEPTH: usize = 4;
const MAX_RELATED: usize = 16;

type Observed = (ObservedDetail, Vec<MediaCandidate>);
/// (kind, native id, omission marker) of one related content.
type Related = (&'static str, String, String);

pub fn observe(
    state: &AdapterState,
    p: &ObserveParams,
    d: Duration,
    t: Instant,
) -> Result<Observed, PluginError> {
    observe_depth(state, p, d, t, 0)
}

fn observe_depth(
    state: &AdapterState,
    p: &ObserveParams,
    d: Duration,
    t: Instant,
    depth: usize,
) -> Result<Observed, PluginError> {
    if depth >= MAX_RELATED_DEPTH {
        return Err(fail("related content depth budget exceeded"));
    }
    if !p.native_entry_key.bytes().all(|b| b.is_ascii_digit()) || p.native_entry_key.is_empty() {
        return Err(fail("invalid content identity"));
    }
    let data = fetch_detail(state, p, d, t)?;
    // The card is only a preview. Replace the body with opus detail while
    // retaining the original dynamic's author/repost graph for resolution.
    let mut body_data = data.clone();
    let mut body_params = p.clone();
    if p.native_kind == "dynamic"
        && data["item"]
            .pointer("/modules/module_dynamic/major/opus")
            .is_some()
    {
        body_data = opus_detail(state, &p.native_entry_key, d, t)?;
        body_params.native_kind = "opus".into();
    }
    let (mut detail, mut plans) = map_detail(&body_data, &body_params)?;
    detail.native_kind = p.native_kind.clone();
    if body_params.native_kind != p.native_kind {
        detail.source_version_evidence["dynamic_opus_identity"] = json!({"dynamic_id":p.native_entry_key,"opus_id":body_data["item"]["id_str"],"source":"dynamic.major.opus + opus.detail"});
        detail.metadata["dynamic_snapshot"] = trace_contracts::content::safe_metadata(&data);
    }
    let related = collect_related(p, &data, &body_data, &detail)?;
    attach_article_collection(state, p, &data, &mut detail, d, t);
    for (kind, key, marker) in related {
        if key == p.native_entry_key && kind == p.native_kind {
            continue;
        }
        match fetch_related(state, kind, &key, d, t, depth) {
            Ok((child, cp)) => {
                if child.completeness == "full" {
                    detail
                        .document
                        .omissions
                        .retain(|v| v != &marker && v != "unknown_dynamic");
                }
                append_related(&mut detail, &mut plans, child, cp, &marker);
            }
            Err(e) => record_unavailable(&mut detail, kind, &key, marker, &e),
        }
    }
    detail.completeness = if detail.document.omissions.is_empty() {
        "full"
    } else {
        "summary"
    }
    .into();
    Ok((detail, plans))
}

fn fetch_detail(
    state: &AdapterState,
    p: &ObserveParams,
    d: Duration,
    t: Instant,
) -> Result<Value, PluginError> {
    match p.native_kind.as_str() {
        "article" => get(
            state,
            "/x/article/view",
            &[("id", &p.native_entry_key)],
            d,
            t,
        ),
        "opus" => opus_detail(state, &p.native_entry_key, d, t),
        "dynamic" => get(
            state,
            "/x/polymer/web-dynamic/v1/detail",
            &[("id", &p.native_entry_key)],
            d,
            t,
        ),
        _ => Err(fail("unknown native kind")),
    }
}

/// Related contents named by the dynamic card, opus link cards and body markers.
fn collect_related(
    p: &ObserveParams,
    data: &Value,
    body_data: &Value,
    detail: &ObservedDetail,
) -> Result<Vec<Related>, PluginError> {
    let mut related: Vec<Related> = Vec::new();
    if p.native_kind == "dynamic" {
        let item = &data["item"];
        let major = &item["modules"]["module_dynamic"]["major"];
        if let Some(key) = id(&major["archive"]["bvid"]) {
            related.push(("video", key, "dynamic_card_requires_resolution".into()));
        }
        if let Some(key) = id(&major["article"]["id"]) {
            related.push(("article", key, "dynamic_card_requires_resolution".into()));
        }
        if let Some(key) = id(&major["music"]["id"]) {
            related.push(("music", key, "dynamic_card_requires_resolution".into()));
        }
        if let Some(key) = id(&item["orig"]["id_str"]) {
            related.push(("dynamic", key, "repost_original_partial".into()));
        }
    }
    if let Some(modules) = body_data["item"]["modules"].as_array() {
        for module in modules {
            if let Some(paras) = module["module_content"]["paragraphs"].as_array() {
                for para in paras {
                    if let Some((kind, key)) = card_identity(&para["link_card"]["card"]) {
                        let marker = format!("related:{kind}:{key}");
                        if !related.iter().any(|(_, _, m)| m == &marker) {
                            related.push((kind, key, marker));
                        }
                    }
                }
            }
        }
    }
    for block in &detail.document.blocks {
        if let Block::Text { style, .. } = block
            && let Some(rest) = style.strip_prefix("related:")
            && let Some((kind, key)) = rest.split_once(':')
        {
            let kind = match kind {
                "video" => "video",
                "article" => "article",
                _ => continue,
            };
            if !related.iter().any(|(_, _, marker)| marker == style) {
                related.push((kind, key.into(), style.clone()));
            }
        }
    }
    if related.len() > MAX_RELATED {
        return Err(fail("related content count budget exceeded"));
    }
    Ok(related)
}

/// An article in a collection records the collection list; failure is evidence, not an error.
fn attach_article_collection(
    state: &AdapterState,
    p: &ObserveParams,
    data: &Value,
    detail: &mut ObservedDetail,
    d: Duration,
    t: Instant,
) {
    if p.native_kind == "article"
        && let Some(collection) = id(&data["list"]["id"]).filter(|id| id != "0")
    {
        detail.metadata["collection"] = match get(
            state,
            "/x/article/list/web/articles",
            &[("id", &collection)],
            d,
            t,
        ) {
            Ok(value) => {
                json!({"id":collection,"coverage":"observed","data":trace_contracts::content::safe_metadata(&value)})
            }
            Err(error) => {
                json!({"id":collection,"coverage":"unavailable","failure":error.class})
            }
        };
    }
}

fn fetch_related(
    state: &AdapterState,
    kind: &str,
    key: &str,
    d: Duration,
    t: Instant,
    depth: usize,
) -> Result<Observed, PluginError> {
    match kind {
        "video" => fetch_related_video(state, key, d, t),
        "music" => crate::audio::observe(state, key, d, t),
        _ => observe_depth(
            state,
            &ObserveParams {
                native_entry_key: key.into(),
                native_kind: kind.into(),
            },
            d,
            t,
            depth + 1,
        ),
    }
}

fn fetch_related_video(
    state: &AdapterState,
    key: &str,
    d: Duration,
    t: Instant,
) -> Result<Observed, PluginError> {
    let mut child = crate::detail::observe(
        state,
        &ObserveParams {
            native_entry_key: key.into(),
            native_kind: "video".into(),
        },
        d,
        t,
    )?;
    let plan = crate::media::resolve_media(
        state,
        &ResolveMediaParams {
            native_entry_key: child.native_entry_key.clone(),
            native_kind: "video".into(),
            resources: child.resources.clone(),
            quality_hint: json!({}),
        },
        d,
        t,
    )?;
    child.source_version_evidence["silent_or_muxed"] = plan.notes["silent_or_muxed"].clone();
    Ok((child, plan.candidates))
}

/// A related content that could not be read stays visible as an omission.
fn record_unavailable(
    detail: &mut ObservedDetail,
    kind: &str,
    key: &str,
    marker: String,
    error: &PluginError,
) {
    if !detail.document.omissions.contains(&marker) {
        detail.document.omissions.push(marker);
    }
    if !detail.metadata["related_unavailable"].is_array() {
        detail.metadata["related_unavailable"] = json!([]);
    }
    detail.metadata["related_unavailable"]
        .as_array_mut()
        .unwrap()
        .push(json!({"kind":kind,"native_id":key,"failure":error.class}));
}

fn append_related(
    parent: &mut ObservedDetail,
    plans: &mut Vec<MediaCandidate>,
    mut child: ObservedDetail,
    mut child_plans: Vec<MediaCandidate>,
    marker: &str,
) {
    let prefix = format!("related:{}:{}:", child.native_kind, child.native_entry_key);
    let offset = parent
        .resources
        .iter()
        .map(|r| r.position)
        .max()
        .unwrap_or(0)
        + 1;
    let map = |r: &mut ResourceIdentityDto| {
        r.source_attachment_id = format!("{prefix}{}", r.source_attachment_id);
        r.group_key = r.group_key.as_ref().map(|g| format!("{prefix}{g}"));
        r.position += offset;
    };
    if child.document.blocks.is_empty() && !child.normalized_body.is_empty() {
        child.document.blocks.push(Block::Text {
            text: child.normalized_body.clone(),
            style: "quote".into(),
        });
    }
    child.document.blocks.insert(
        0,
        Block::Text {
            text: format!("引用：{}", child.title),
            style: "quote".into(),
        },
    );
    parent.normalized_body.push_str(&format!(
        "\n引用：{}\n{}",
        child.title, child.normalized_body
    ));
    for block in &mut child.document.blocks {
        if let Block::Resource { attachment_id, .. } = block {
            *attachment_id = format!("{prefix}{attachment_id}");
        }
    }
    for r in &mut child.resources {
        map(r);
        if r.role == "video" {
            child.document.blocks.push(Block::Resource {
                attachment_id: r.source_attachment_id.clone(),
                caption: r.title.clone().unwrap_or_default(),
            });
        }
    }
    for c in &mut child_plans {
        map(&mut c.resource);
    }
    if let Some(index) = parent
        .document
        .blocks
        .iter()
        .position(|b| matches!(b,Block::Text{style,..} if style==marker))
    {
        parent
            .document
            .blocks
            .splice(index..=index, child.document.blocks);
    } else {
        parent.document.blocks.extend(child.document.blocks);
    }
    parent.document.omissions.extend(child.document.omissions);
    parent.resources.extend(child.resources);
    plans.extend(child_plans);
    let relation = json!({"kind":child.native_kind,"native_id":child.native_entry_key,"author_snapshot":child.metadata,"source":child.source_version_evidence});
    if !parent.metadata["related_content"].is_array() {
        parent.metadata["related_content"] = json!([]);
    }
    parent.metadata["related_content"]
        .as_array_mut()
        .unwrap()
        .push(relation);
    if let Some(silent) = child.source_version_evidence["silent_or_muxed"].as_array() {
        if !parent.source_version_evidence["silent_or_muxed"].is_array() {
            parent.source_version_evidence["silent_or_muxed"] = json!([]);
        }
        for group in silent.iter().filter_map(Value::as_str) {
            parent.source_version_evidence["silent_or_muxed"]
                .as_array_mut()
                .unwrap()
                .push(json!(format!("{prefix}{group}")));
        }
    }
}
