//! Dynamic/opus/article mapping; all requests use the existing constrained host proxy.
use crate::{AdapterState, api};
use serde_json::{Value, json};
use std::time::{Duration, Instant};
use trace_contracts::plugin::*;
use trace_domain::errors::FailureClass;
use trace_site_support::{candidate, document::ParsedDocument, id};

fn fail(msg: &str) -> PluginError {
    trace_site_support::error(FailureClass::SchemaChanged, msg)
}
fn get(
    state: &AdapterState,
    path: &str,
    params: &[(&str, &str)],
    deadline: Duration,
    started: Instant,
) -> Result<Value, PluginError> {
    let mut u = url::Url::parse(&format!("https://api.bilibili.com{path}")).unwrap();
    u.query_pairs_mut().extend_pairs(params.iter().copied());
    api::envelope(&api::get_json(state, u.as_str(), true, deadline, started)?).map(|e| e.data)
}
fn timestamp(v: &Value) -> Option<String> {
    time::OffsetDateTime::from_unix_timestamp(v.as_i64()?)
        .ok()?
        .format(&time::format_description::well_known::Rfc3339)
        .ok()
}

pub fn enumerate(
    state: &AdapterState,
    p: &EnumerateParams,
    d: Duration,
    t: Instant,
) -> Result<EnumeratedPage, PluginError> {
    let cursor = p.cursor.as_deref().unwrap_or("");
    let data = if p.content_type == "article" {
        get(
            state,
            "/x/space/article",
            &[
                ("mid", &p.native_account_key),
                ("pn", if cursor.is_empty() { "1" } else { cursor }),
                ("ps", "30"),
                ("sort", "publish_time"),
            ],
            d,
            t,
        )?
    } else if p.content_type == "image" {
        get(
            state,
            "/x/polymer/web-dynamic/v1/opus/feed/space",
            &[
                ("host_mid", &p.native_account_key),
                ("offset", cursor),
                ("type", "all"),
            ],
            d,
            t,
        )?
    } else if p.content_type == "dynamic" {
        get(
            state,
            "/x/polymer/web-dynamic/v1/feed/space",
            &[("host_mid", &p.native_account_key), ("offset", cursor)],
            d,
            t,
        )?
    } else {
        return Err(trace_site_support::error(
            FailureClass::Unsupported,
            "unsupported Bilibili content lane",
        ));
    };
    let mut items = Vec::new();
    if p.content_type == "article" {
        let list = data["articles"]
            .as_array()
            .ok_or_else(|| fail("article list missing"))?;
        for a in list {
            items.push(EnumeratedEntry {
                native_entry_key: id(&a["id"]).ok_or_else(|| fail("article id missing"))?,
                native_kind: "article".into(),
                title: a["title"].as_str().unwrap_or("").into(),
                published_at: timestamp(&a["publish_time"]),
                published_precision: "source".into(),
                pinned: false,
                summary_body: a["summary"].as_str().map(str::to_owned),
                stats: trace_contracts::content::safe_metadata(&a["stats"]),
            });
        }
        let page = cursor.parse::<u64>().unwrap_or(1);
        let more = page * 30
            < data["count"]
                .as_u64()
                .ok_or_else(|| fail("article count missing"))?;
        return Ok(EnumeratedPage {
            items,
            next_cursor: more.then(|| (page + 1).to_string()),
            has_more: more,
            coverage: json!({"source":"space.article","page":page,"history":"unknown"}),
        });
    }
    let list = data["items"]
        .as_array()
        .ok_or_else(|| fail("dynamic list missing"))?;
    for a in list {
        let key = id(&a["id_str"])
            .or_else(|| id(&a["opus_id"]))
            .ok_or_else(|| fail("dynamic identity missing"))?;
        let body = a
            .pointer("/modules/module_dynamic/desc/text")
            .or_else(|| a.get("content"))
            .and_then(Value::as_str)
            .unwrap_or("");
        items.push(EnumeratedEntry {
            native_entry_key: key,
            native_kind: if p.content_type == "image" {
                "opus"
            } else {
                "dynamic"
            }
            .into(),
            title: a["title"]
                .as_str()
                .unwrap_or(body)
                .chars()
                .take(100)
                .collect(),
            published_at: timestamp(&a["modules"]["module_author"]["pub_ts"]),
            published_precision: "source".into(),
            pinned: a["modules"]["module_tag"]["text"] == "置顶",
            summary_body: Some(body.into()),
            stats: trace_contracts::content::safe_metadata(&a["modules"]["module_stat"]),
        });
    }
    let more = data["has_more"]
        .as_bool()
        .or_else(|| data["has_more"].as_u64().map(|n| n != 0))
        .ok_or_else(|| fail("dynamic has_more missing"))?;
    Ok(EnumeratedPage {
        items,
        next_cursor: if more {
            Some(id(&data["offset"]).ok_or_else(|| fail("dynamic cursor missing"))?)
        } else {
            None
        },
        has_more: more,
        coverage: json!({"source":if p.content_type=="image"{"opus.feed.space"}else{"feed.space"},"history":"unknown"}),
    })
}

pub fn observe(
    state: &AdapterState,
    p: &ObserveParams,
    d: Duration,
    t: Instant,
) -> Result<(ObservedDetail, Vec<MediaCandidate>), PluginError> {
    observe_depth(state, p, d, t, 0)
}

fn observe_depth(
    state: &AdapterState,
    p: &ObserveParams,
    d: Duration,
    t: Instant,
    depth: usize,
) -> Result<(ObservedDetail, Vec<MediaCandidate>), PluginError> {
    if depth >= 4 {
        return Err(fail("related content depth budget exceeded"));
    }
    if !p.native_entry_key.bytes().all(|b| b.is_ascii_digit()) || p.native_entry_key.is_empty() {
        return Err(fail("invalid content identity"));
    }
    let data = match p.native_kind.as_str() {
        "article" => get(
            state,
            "/x/article/view",
            &[("id", &p.native_entry_key)],
            d,
            t,
        )?,
        "opus" => get(
            state,
            "/x/polymer/web-dynamic/v1/opus/detail",
            &[
                ("id", &p.native_entry_key),
                ("features", "htmlNewStyle,opusPrivateVisible,editable"),
            ],
            d,
            t,
        )?,
        "dynamic" => get(
            state,
            "/x/polymer/web-dynamic/v1/detail",
            &[("id", &p.native_entry_key)],
            d,
            t,
        )?,
        _ => return Err(fail("unknown native kind")),
    };
    // The card is only a preview. Replace the body with opus detail while
    // retaining the original dynamic's author/repost graph for resolution.
    let mut body_data = data.clone();
    let mut body_params = p.clone();
    if p.native_kind == "dynamic"
        && data["item"]
            .pointer("/modules/module_dynamic/major/opus")
            .is_some()
    {
        body_data = get(
            state,
            "/x/polymer/web-dynamic/v1/opus/detail",
            &[
                ("id", &p.native_entry_key),
                ("features", "htmlNewStyle,opusPrivateVisible,editable"),
            ],
            d,
            t,
        )?;
        body_params.native_kind = "opus".into();
    }
    let (mut detail, mut plans) = map_detail(&body_data, &body_params)?;
    detail.native_kind = p.native_kind.clone();
    if body_params.native_kind != p.native_kind {
        detail.source_version_evidence["dynamic_opus_identity"] = json!({"dynamic_id":p.native_entry_key,"opus_id":body_data["item"]["id_str"],"source":"dynamic.major.opus + opus.detail"});
        detail.metadata["dynamic_snapshot"] = trace_contracts::content::safe_metadata(&data);
    }
    let mut related: Vec<(&str, String, String)> = Vec::new();
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
        if let trace_contracts::content::Block::Text { style, .. } = block
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
    if related.len() > 16 {
        return Err(fail("related content count budget exceeded"));
    }
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
    {
        for (kind, key, marker) in related {
            if key == p.native_entry_key && kind == p.native_kind {
                continue;
            }
            let related = if kind == "video" {
                (|| {
                    let child = crate::handlers::observe(
                        state,
                        &ObserveParams {
                            native_entry_key: key.clone(),
                            native_kind: "video".into(),
                        },
                        d,
                        t,
                    )?;
                    let plan = crate::handlers::resolve_media(
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
                    let mut child = child;
                    child.source_version_evidence["silent_or_muxed"] =
                        plan.notes["silent_or_muxed"].clone();
                    Ok((child, plan.candidates))
                })()
            } else if kind == "music" {
                crate::audio::observe(state, &key, d, t)
            } else {
                observe_depth(
                    state,
                    &ObserveParams {
                        native_entry_key: key.clone(),
                        native_kind: kind.into(),
                    },
                    d,
                    t,
                    depth + 1,
                )
            };
            match related {
                Ok((child, cp)) => {
                    if child.completeness == "full" {
                        detail
                            .document
                            .omissions
                            .retain(|v| v != &marker && v != "unknown_dynamic");
                    }
                    append_related(&mut detail, &mut plans, child, cp, &marker);
                }
                Err(e) => {
                    if !detail.document.omissions.contains(&marker) {
                        detail.document.omissions.push(marker);
                    }
                    if !detail.metadata["related_unavailable"].is_array() {
                        detail.metadata["related_unavailable"] = json!([]);
                    }
                    detail.metadata["related_unavailable"]
                        .as_array_mut()
                        .unwrap()
                        .push(json!({"kind":kind,"native_id":key,"failure":e.class}));
                }
            }
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

fn append_related(
    parent: &mut ObservedDetail,
    plans: &mut Vec<MediaCandidate>,
    mut child: ObservedDetail,
    mut child_plans: Vec<MediaCandidate>,
    marker: &str,
) {
    use trace_contracts::content::Block;
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

fn card_identity(card: &Value) -> Option<(&'static str, String)> {
    match card["type"].as_str()? {
        "LINK_CARD_TYPE_UGC" => {
            if let Some(bvid) = id(&card["ugc"]["bvid"]) {
                return Some(("video", bvid));
            }
            let raw = card["ugc"]["jump_url"].as_str()?;
            let full = if raw.starts_with("//") {
                format!("https:{raw}")
            } else {
                raw.to_owned()
            };
            let url = url::Url::parse(&full).ok()?;
            if url.scheme() != "https" || url.host_str() != Some("www.bilibili.com") {
                return None;
            }
            let mut parts = url.path_segments()?;
            if parts.next()? != "video" {
                return None;
            }
            let bvid = parts.next()?;
            bvid.starts_with("BV").then(|| ("video", bvid.to_owned()))
        }
        "LINK_CARD_TYPE_OPUS" => id(&card["opus"]["id_str"])
            .or_else(|| id(&card["oid"]))
            .filter(|id| id.bytes().all(|b| b.is_ascii_digit()))
            .map(|id| ("opus", id)),
        "LINK_CARD_TYPE_MUSIC" => id(&card["music"]["id"]).map(|id| ("music", id)),
        _ => None,
    }
}

fn nodes(out: &mut ParsedDocument, value: &Value, style: &str) {
    if let Some(a) = value.as_array() {
        let text = a
            .iter()
            .filter_map(|n| {
                n.pointer("/word/words")
                    .or_else(|| n.pointer("/rich/text"))
                    .or_else(|| n.pointer("/formula/latex_content"))
                    .or_else(|| n.pointer("/formula/latex"))
            })
            .filter_map(Value::as_str)
            .collect::<String>();
        out.text(&text, style);
    }
}
pub fn map_detail(
    data: &Value,
    p: &ObserveParams,
) -> Result<(ObservedDetail, Vec<MediaCandidate>), PluginError> {
    let mut out = ParsedDocument::default();
    out.document.version = 1;
    let mut title = String::new();
    let mut published = None;
    match p.native_kind.as_str() {
        "article" => {
            if let Some(returned) = id(&data["id"])
                && returned != p.native_entry_key
            {
                return Err(fail("article identity mismatch"));
            }
            out = crate::article::parse(
                data["content"]
                    .as_str()
                    .ok_or_else(|| fail("article full body missing"))?,
            )
            .map_err(fail)?;
            title = data["title"].as_str().unwrap_or("").into();
            published = timestamp(&data["publish_time"]);
        }
        "opus" => {
            let item = &data["item"];
            if id(&item["id_str"]).as_deref() != Some(&p.native_entry_key) {
                return Err(fail("opus identity mismatch"));
            }
            title = item["basic"]["title"].as_str().unwrap_or("").into();
            let modules = item["modules"]
                .as_array()
                .ok_or_else(|| fail("opus modules missing"))?;
            let mut found = false;
            for m in modules {
                if m["module_author"].is_object() {
                    published = timestamp(&m["module_author"]["pub_ts"]);
                }
                if let Some(paragraphs) = m["module_content"]["paragraphs"].as_array() {
                    found = true;
                    for para in paragraphs {
                        match para["para_type"].as_i64() {
                            Some(1 | 4) => nodes(
                                &mut out,
                                &para["text"]["nodes"],
                                if para["para_type"] == 4 {
                                    "quote"
                                } else {
                                    "paragraph"
                                },
                            ),
                            Some(2) => {
                                if let Some(pics) = para
                                    .pointer("/pic/pics")
                                    .or_else(|| para.pointer("/pics/pics"))
                                    .and_then(Value::as_array)
                                {
                                    for pic in pics {
                                        if let Some(src) = pic["url"].as_str() {
                                            out.image(src, "");
                                        } else {
                                            out.document
                                                .omissions
                                                .push("opus_picture_missing_url".into());
                                        }
                                    }
                                } else {
                                    out.document.omissions.push("opus_pictures_missing".into());
                                }
                            }
                            Some(3) => out.text("—", "separator"),
                            Some(5) => {
                                if let Some(items) = para["list"]["items"].as_array() {
                                    for item in items {
                                        let mut part = ParsedDocument::default();
                                        nodes(&mut part, &item["nodes"], "paragraph");
                                        out.body.push_str(&part.body);
                                        out.document.blocks.push(
                                            trace_contracts::content::Block::ListItem {
                                                text: part.body.trim_end().into(),
                                                depth: item["level"]
                                                    .as_u64()
                                                    .unwrap_or(1)
                                                    .clamp(1, 8)
                                                    as usize,
                                                ordinal: (para["list"]["style"] == 1)
                                                    .then(|| item["order"].as_u64().unwrap_or(1)),
                                            },
                                        );
                                    }
                                }
                            }
                            Some(6) => {
                                let card = &para["link_card"]["card"];
                                if let Some((kind, key)) = card_identity(card) {
                                    let marker = format!("related:{kind}:{key}");
                                    let label = card["ugc"]["title"]
                                        .as_str()
                                        .or_else(|| card["opus"]["title"].as_str())
                                        .unwrap_or("引用内容");
                                    out.text(label, &marker);
                                    out.document.omissions.push(marker);
                                } else {
                                    let label = card["item_null"]["tips"]
                                        .as_str()
                                        .or_else(|| card["common"]["title"].as_str())
                                        .unwrap_or("附属卡片");
                                    out.text(label, "quote");
                                    out.document
                                        .omissions
                                        .push("opus_card_requires_resolution".into());
                                }
                            }
                            Some(7) => {
                                out.text(para["code"]["content"].as_str().unwrap_or(""), "code")
                            }
                            _ => out
                                .document
                                .omissions
                                .push("opus_card_requires_resolution".into()),
                        }
                    }
                }
            }
            if !found {
                return Err(fail("opus full content module missing"));
            }
        }
        "dynamic" => {
            let item = &data["item"];
            if id(&item["id_str"]).as_deref() != Some(&p.native_entry_key) {
                return Err(fail("dynamic identity mismatch"));
            }
            let m = &item["modules"]["module_dynamic"];
            out.text(m["desc"]["text"].as_str().unwrap_or(""), "paragraph");
            published = timestamp(&item["modules"]["module_author"]["pub_ts"]);
            if let Some(pics) = m.pointer("/major/draw/items").and_then(Value::as_array) {
                for pic in pics {
                    if let Some(src) = pic["src"].as_str() {
                        out.image(src, "");
                    } else {
                        out.document.omissions.push("draw_missing_image".into());
                    }
                }
            } else if m["major"].is_object() {
                out.document
                    .omissions
                    .push("dynamic_card_requires_resolution".into());
            }
            if item["orig"].is_object() {
                out.text(
                    item["orig"]["modules"]["module_dynamic"]["desc"]["text"]
                        .as_str()
                        .unwrap_or("原文当前不可获取"),
                    "quote",
                );
                out.document
                    .omissions
                    .push("repost_original_partial".into());
            }
            if out.body.is_empty() && out.resources.is_empty() {
                out.document.omissions.push("unknown_dynamic".into());
            }
        }
        _ => return Err(fail("unsupported content mapping")),
    }
    if title.is_empty() {
        title = out.body.chars().take(100).collect();
    }
    let plans = out
        .resources
        .iter()
        .filter_map(|r| {
            candidate(
                r.clone(),
                vec![out.urls[&r.source_attachment_id].clone()],
                "image",
                json!({}),
            )
        })
        .collect();
    let complete = out.document.omissions.is_empty();
    Ok((
        ObservedDetail {
            document: out.document,
            metadata: trace_contracts::content::safe_metadata(data),
            native_entry_key: p.native_entry_key.clone(),
            native_kind: p.native_kind.clone(),
            title,
            normalized_body: out.body,
            published_at: published,
            published_precision: "source".into(),
            access_state: "available".into(),
            source_version_evidence: json!({"source":format!("{}.detail",p.native_kind),"native_id":p.native_entry_key}),
            resources: out.resources,
            completeness: if complete { "full" } else { "summary" }.into(),
            stats: trace_contracts::content::safe_metadata(if p.native_kind == "article" {
                &data["stats"]
            } else {
                &data["item"]["modules"]["module_stat"]
            }),
        },
        plans,
    ))
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn c02_opus_thirty_images_and_article_html_preserve_body() {
        let paragraphs=(0..30).flat_map(|n|vec![json!({"para_type":1,"text":{"nodes":[{"word":{"words":format!("正文{n}")}}]}}),json!({"para_type":2,"pic":{"pics":[{"url":format!("https://i0.hdslb.com/{n}.jpg")} ]}})]).collect::<Vec<_>>();
        let (d,p)=map_detail(&json!({"item":{"id_str":"999999999999999999","basic":{"title":"长文"},"modules":[{"module_content":{"paragraphs":paragraphs}}]}}),&ObserveParams{native_entry_key:"999999999999999999".into(),native_kind:"opus".into()}).unwrap();
        assert_eq!(d.completeness, "full");
        assert_eq!(p.len(), 30);
        assert!(d.normalized_body.contains("正文29"));
        assert_eq!(d.document.blocks.len(), 60);
        let (d, _) = map_detail(
            &json!({"content":"<p>完整正文</p><script>secret</script>"}),
            &ObserveParams {
                native_entry_key: "7".into(),
                native_kind: "article".into(),
            },
        )
        .unwrap();
        assert_eq!(d.normalized_body.trim(), "完整正文");
    }
}
