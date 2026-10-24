//! Pure mapping of article/opus/dynamic detail responses into a document.
use super::{fail, timestamp};
use serde_json::{Value, json};
use trace_contracts::content::Block;
use trace_contracts::plugin::{MediaCandidate, ObserveParams, ObservedDetail, PluginError};
use trace_site_support::{candidate, document::ParsedDocument, id};

/// What one content kind contributes before the shared detail is assembled.
struct Mapped {
    out: ParsedDocument,
    title: String,
    published: Option<String>,
}

pub fn card_identity(card: &Value) -> Option<(&'static str, String)> {
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

fn new_document() -> ParsedDocument {
    let mut out = ParsedDocument::default();
    out.document.version = 1;
    out
}

pub fn map_detail(
    data: &Value,
    p: &ObserveParams,
) -> Result<(ObservedDetail, Vec<MediaCandidate>), PluginError> {
    let Mapped {
        out,
        mut title,
        published,
    } = match p.native_kind.as_str() {
        "article" => map_article(data, p)?,
        "opus" => map_opus(data, p)?,
        "dynamic" => map_dynamic(data, p)?,
        _ => return Err(fail("unsupported content mapping")),
    };
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
            unified: None,
            stats: trace_contracts::content::safe_metadata(if p.native_kind == "article" {
                &data["stats"]
            } else {
                &data["item"]["modules"]["module_stat"]
            }),
        },
        plans,
    ))
}

fn map_article(data: &Value, p: &ObserveParams) -> Result<Mapped, PluginError> {
    if let Some(returned) = id(&data["id"])
        && returned != p.native_entry_key
    {
        return Err(fail("article identity mismatch"));
    }
    let out = crate::article::parse(
        data["content"]
            .as_str()
            .ok_or_else(|| fail("article full body missing"))?,
    )
    .map_err(fail)?;
    Ok(Mapped {
        out,
        title: data["title"].as_str().unwrap_or("").into(),
        published: timestamp(&data["publish_time"]),
    })
}

fn map_opus(data: &Value, p: &ObserveParams) -> Result<Mapped, PluginError> {
    let item = &data["item"];
    if id(&item["id_str"]).as_deref() != Some(&p.native_entry_key) {
        return Err(fail("opus identity mismatch"));
    }
    let title = item["basic"]["title"].as_str().unwrap_or("").into();
    let modules = item["modules"]
        .as_array()
        .ok_or_else(|| fail("opus modules missing"))?;
    let mut out = new_document();
    let mut published = None;
    let mut found = false;
    for m in modules {
        if m["module_author"].is_object() {
            published = timestamp(&m["module_author"]["pub_ts"]);
        }
        if let Some(paragraphs) = m["module_content"]["paragraphs"].as_array() {
            found = true;
            for para in paragraphs {
                opus_paragraph(&mut out, para);
            }
        }
    }
    if !found {
        return Err(fail("opus full content module missing"));
    }
    Ok(Mapped {
        out,
        title,
        published,
    })
}

fn opus_paragraph(out: &mut ParsedDocument, para: &Value) {
    match para["para_type"].as_i64() {
        Some(1 | 4) => nodes(
            out,
            &para["text"]["nodes"],
            if para["para_type"] == 4 {
                "quote"
            } else {
                "paragraph"
            },
        ),
        Some(2) => opus_pictures(out, para),
        Some(3) => out.text("—", "separator"),
        Some(5) => opus_list(out, para),
        Some(6) => opus_link_card(out, para),
        Some(7) => out.text(para["code"]["content"].as_str().unwrap_or(""), "code"),
        _ => out
            .document
            .omissions
            .push("opus_card_requires_resolution".into()),
    }
}

fn opus_pictures(out: &mut ParsedDocument, para: &Value) {
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

fn opus_list(out: &mut ParsedDocument, para: &Value) {
    if let Some(items) = para["list"]["items"].as_array() {
        for item in items {
            let mut part = ParsedDocument::default();
            nodes(&mut part, &item["nodes"], "paragraph");
            out.body.push_str(&part.body);
            out.document.blocks.push(Block::ListItem {
                text: part.body.trim_end().into(),
                depth: item["level"].as_u64().unwrap_or(1).clamp(1, 8) as usize,
                ordinal: (para["list"]["style"] == 1).then(|| item["order"].as_u64().unwrap_or(1)),
            });
        }
    }
}

fn opus_link_card(out: &mut ParsedDocument, para: &Value) {
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

fn map_dynamic(data: &Value, p: &ObserveParams) -> Result<Mapped, PluginError> {
    let item = &data["item"];
    if id(&item["id_str"]).as_deref() != Some(&p.native_entry_key) {
        return Err(fail("dynamic identity mismatch"));
    }
    let mut out = new_document();
    let m = &item["modules"]["module_dynamic"];
    out.text(m["desc"]["text"].as_str().unwrap_or(""), "paragraph");
    let published = timestamp(&item["modules"]["module_author"]["pub_ts"]);
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
    Ok(Mapped {
        out,
        title: String::new(),
        published,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn opus_thirty_images_and_article_html_preserve_body() {
        let paragraphs=(0..30).flat_map(|n|vec![json!({
            "para_type": 1,
            "text": {"nodes":[{"word":{"words":format!("正文{n}")}}]},
        }),json!({"para_type":2,"pic":{"pics":[{"url":format!("https://i0.hdslb.com/{n}.jpg")} ]}})]).collect::<Vec<_>>();
        let detail = json!({"item": {
            "id_str": "999999999999999999",
            "basic": {"title": "长文"},
            "modules": [{"module_content": {"paragraphs": paragraphs}}],
        }});
        let params = ObserveParams {
            native_entry_key: "999999999999999999".into(),
            native_kind: "opus".into(),
        };
        let (d, p) = map_detail(&detail, &params).unwrap();
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
