//! The article endpoint has HTML and JSON-encoded ops bodies. Interpret ops,
//! never display the JSON as a successfully parsed article.
use serde_json::Value;
use trace_contracts::content::{Block, TextSpan};
use trace_site_support::document::{self, ParsedDocument};

pub fn parse(source: &str) -> Result<ParsedDocument, &'static str> {
    if source.len() > 512 * 1024 {
        return Err("article body budget exceeded");
    }
    if !source.trim_start().starts_with(['{', '[']) {
        return document::html(source);
    }
    let value: Value = serde_json::from_str(source).map_err(|_| "invalid article ops JSON")?;
    let ops = value["ops"].as_array().ok_or("article ops missing")?;
    if ops.len() > 8192 {
        return Err("article ops budget exceeded");
    }
    let mut out = ParsedDocument::default();
    out.document.version = 1;
    let mut spans = Vec::new();
    let mut ordinal = 0;
    for op in ops {
        // The documented spelling is singular; tolerate plural Quill spelling.
        let attr = op
            .get("attribute")
            .or_else(|| op.get("attributes"))
            .unwrap_or(&Value::Null);
        if let Some(text) = op["insert"].as_str() {
            for (i, part) in text.split('\n').enumerate() {
                if i > 0 {
                    flush(&mut out, &mut spans, attr, &mut ordinal);
                }
                if !part.is_empty() {
                    spans.push(TextSpan {
                        text: part.into(),
                        bold: attr["bold"] == true,
                        italic: attr["italic"] == true,
                        strike: attr["strike"] == true,
                    });
                }
            }
        } else if let Some(insert) = op["insert"].as_object() {
            flush(&mut out, &mut spans, &Value::Null, &mut ordinal);
            if insert.len() != 1 {
                return Err("ambiguous article embed");
            }
            let (kind, card) = insert.iter().next().ok_or("empty article embed")?;
            let caption = card["alt"].as_str().unwrap_or("");
            match kind.as_str() {
                "native-image" | "cut-off" => {
                    if let Some(url) = card["url"].as_str() {
                        out.image(url, caption);
                    } else {
                        out.document
                            .omissions
                            .push("article_image_missing_url".into());
                    }
                }
                "video-card" | "article-card" => {
                    let video = kind == "video-card";
                    let native = card["id"].as_str().unwrap_or("");
                    let prefix = if video { "av" } else { "cv" };
                    if let Some(digits) = native
                        .strip_prefix(prefix)
                        .filter(|s| !s.is_empty() && s.bytes().all(|b| b.is_ascii_digit()))
                    {
                        let key = if video { native } else { digits };
                        let marker =
                            format!("related:{}:{key}", if video { "video" } else { "article" });
                        out.text(if caption.is_empty() { native } else { caption }, &marker);
                        out.document.omissions.push(marker);
                    } else {
                        out.document
                            .omissions
                            .push("article_card_identity_missing".into());
                    }
                }
                _ => {
                    out.text(
                        if caption.is_empty() {
                            "附属卡片"
                        } else {
                            caption
                        },
                        "quote",
                    );
                    if let Some(url) = card["url"].as_str() {
                        out.image(url, caption);
                    }
                    out.document
                        .omissions
                        .push("article_embed_not_fully_resolved".into());
                }
            }
        } else {
            return Err("article op missing insert");
        }
    }
    flush(&mut out, &mut spans, &Value::Null, &mut ordinal);
    Ok(out)
}

fn flush(out: &mut ParsedDocument, spans: &mut Vec<TextSpan>, attr: &Value, ordinal: &mut u64) {
    if spans.is_empty() {
        return;
    }
    let style = if let Some(level) = attr["header"].as_u64().filter(|l| (1..=6).contains(l)) {
        format!("h{level}")
    } else if attr["blockquote"] == true {
        "quote".into()
    } else {
        match attr["list"].as_str() {
            Some("ordered") => "ordered",
            Some("bullet") => "bullet",
            _ => "paragraph",
        }
        .into()
    };
    let number = if style == "ordered" {
        *ordinal += 1;
        Some(*ordinal)
    } else {
        *ordinal = 0;
        None
    };
    for span in spans.iter() {
        out.body.push_str(&span.text);
    }
    out.body.push('\n');
    out.document.blocks.push(Block::RichText {
        spans: std::mem::take(spans),
        style,
        depth: attr["indent"].as_u64().unwrap_or(0).min(7) as usize + 1,
        ordinal: number,
    });
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    #[test]
    fn article_ops_preserve_lines_styles_images_and_fail_closed() {
        let d=parse(&json!({"ops":[{"insert":"标题"},{"insert":"\n","attribute":{"header":2}},
            {"insert":"粗体","attributes":{"bold":true}},{"insert":"后半句\n"},
            {"insert":"第一"},{"insert":"\n","attribute":{"list":"ordered"}},
            {"insert":"第二"},{"insert":"\n","attribute":{"list":"ordered"}},
            {"insert":{"native-image":{"url":"https://i0.hdslb.com/image.png","alt":"原图"}}},
            {"insert":{"video-card":{"id":"av17","url":"https://i0.hdslb.com/preview.png"}}},
            {"insert":{"live-card":{"id":"lv3","alt":"直播卡","url":"https://i0.hdslb.com/live.png"}}}
        ]}).to_string()).unwrap();
        assert_eq!(d.body, "标题\n粗体后半句\n第一\n第二\nav17\n直播卡\n");
        assert!(matches!(&d.document.blocks[0],Block::RichText{style,..} if style=="h2"));
        assert!(
            matches!(&d.document.blocks[1],Block::RichText{spans,..} if spans[0].bold && !spans[1].bold)
        );
        assert!(matches!(
            &d.document.blocks[3],
            Block::RichText {
                ordinal: Some(2),
                ..
            }
        ));
        assert_eq!(
            d.resources.len(),
            2,
            "video preview is not the video resource"
        );
        assert!(d.resources.iter().all(|r| r.required));
        assert!(d.document.omissions.contains(&"related:video:av17".into()));
        assert!(
            d.document
                .omissions
                .contains(&"article_embed_not_fully_resolved".into())
        );
        for bad in ["{bad", "[]", "{\"ops\":[{\"retain\":1}]}"] {
            assert!(parse(bad).is_err());
        }
    }
}
