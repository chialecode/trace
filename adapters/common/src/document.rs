//! Convert source markup into inert typed blocks. URLs only leave in transient resource plans.
use pulldown_cmark::{Event, Options, Parser, Tag, TagEnd};
use std::collections::BTreeMap;
use trace_contracts::{
    content::{Block, Document},
    plugin::ResourceIdentityDto,
};

#[derive(Default)]
pub struct ParsedDocument {
    pub document: Document,
    pub body: String,
    pub resources: Vec<ResourceIdentityDto>,
    pub urls: BTreeMap<String, String>,
}

impl ParsedDocument {
    pub fn text(&mut self, text: &str, style: &str) {
        if !text.trim().is_empty() {
            self.body.push_str(text);
            self.body.push('\n');
            self.document.blocks.push(Block::Text {
                text: text.into(),
                style: style.into(),
            });
        }
    }
    pub fn image(&mut self, url: &str, caption: &str) {
        let raw = if url.starts_with("//") {
            format!("https:{url}")
        } else {
            url.into()
        };
        if let Some(hash) = crate::url_identity(&raw) {
            let key = format!("image:{}:{}", self.resources.len(), hash);
            self.document.blocks.push(Block::Resource {
                attachment_id: key.clone(),
                caption: caption.into(),
            });
            self.resources.push(crate::resource(
                &key,
                "image",
                self.resources.len() as i64,
                true,
            ));
            self.urls.insert(key, raw);
        } else {
            self.document.omissions.push("unsafe_image_url".into());
        }
    }
}

pub fn markdown(source: &str) -> Result<ParsedDocument, &'static str> {
    if source.len() > 512 * 1024 {
        return Err("document byte budget exceeded");
    }
    let mut out = ParsedDocument::default();
    out.document.version = 1;
    let mut text = String::new();
    let mut style = "paragraph".to_owned();
    let mut in_image = false;
    let mut lists: Vec<Option<u64>> = Vec::new();
    let mut item_images: Vec<Vec<Block>> = Vec::new();
    let mut table: Option<Vec<Vec<String>>> = None;
    let mut row = Vec::new();
    let mut table_resources = Vec::new();
    let mut quote_depth = 0usize;
    for event in Parser::new_ext(
        source,
        Options::ENABLE_TABLES | Options::ENABLE_STRIKETHROUGH | Options::ENABLE_TASKLISTS,
    ) {
        match event {
            Event::Start(Tag::Image {
                dest_url, title, ..
            }) => {
                if let Some(rows) = &table {
                    let caption =
                        format!("表格第{}行第{}列：{}", rows.len() + 1, row.len() + 1, title);
                    let before = out.document.blocks.len();
                    out.image(&dest_url, &caption);
                    if out.document.blocks.len() > before {
                        table_resources.push(out.document.blocks.pop().unwrap());
                        text.push_str(&format!("[图片{}]", table_resources.len()));
                    }
                } else if let Some(images) = item_images.last_mut() {
                    let before = out.document.blocks.len();
                    out.image(&dest_url, &format!("列表第{}层：{}", lists.len(), title));
                    if out.document.blocks.len() > before {
                        images.push(out.document.blocks.pop().unwrap());
                        text.push_str(&format!("[图片{}]", images.len()));
                    }
                } else {
                    out.text(&text, &style);
                    text.clear();
                    out.image(&dest_url, &title);
                }
                in_image = true;
            }
            Event::End(TagEnd::Image) => in_image = false,
            Event::Start(Tag::Item) => item_images.push(Vec::new()),
            Event::Start(Tag::List(start)) => {
                if !text.trim().is_empty() {
                    if let Some(ordinal) = lists.last_mut() {
                        out.body.push_str(&text);
                        out.body.push('\n');
                        let value = *ordinal;
                        if let Some(n) = ordinal {
                            *n = n.saturating_add(1);
                        }
                        out.document.blocks.push(Block::ListItem {
                            text: std::mem::take(&mut text),
                            depth: lists.len(),
                            ordinal: value,
                        });
                    } else {
                        out.text(&text, &style);
                        text.clear();
                    }
                }
                lists.push(start);
            }
            Event::End(TagEnd::List(_)) => {
                lists.pop();
            }
            Event::Start(Tag::BlockQuote(_)) => {
                quote_depth += 1;
                style = "quote".into();
            }
            Event::End(TagEnd::BlockQuote(_)) => {
                quote_depth = quote_depth.saturating_sub(1);
                style = "paragraph".into();
            }
            Event::Start(Tag::Table(_)) => {
                table = Some(Vec::new());
                row.clear();
            }
            Event::End(TagEnd::TableCell) => {
                row.push(std::mem::take(&mut text));
            }
            Event::End(TagEnd::TableHead | TagEnd::TableRow) => {
                if let Some(rows) = &mut table {
                    rows.push(std::mem::take(&mut row));
                }
            }
            Event::End(TagEnd::Table) => {
                if let Some(rows) = table.take() {
                    for row in &rows {
                        out.body.push_str(&row.join("\t"));
                        out.body.push('\n');
                    }
                    out.document
                        .blocks
                        .push(Block::Table { rows, header: true });
                    out.document.blocks.append(&mut table_resources);
                }
            }
            Event::TaskListMarker(done) => text.push_str(if done { "[x] " } else { "[ ] " }),
            Event::Start(Tag::Heading { level, .. }) => style = format!("{level}"),
            Event::Start(Tag::CodeBlock(_)) => style = "code".into(),
            Event::Text(t) | Event::Code(t) if !in_image => text.push_str(&t),
            Event::SoftBreak | Event::HardBreak => text.push('\n'),
            Event::End(
                end @ (TagEnd::Paragraph | TagEnd::Heading(_) | TagEnd::CodeBlock | TagEnd::Item),
            ) => {
                if let Some(ordinal) = lists.last_mut() {
                    if !text.trim().is_empty() {
                        out.body.push_str(&text);
                        out.body.push('\n');
                        let value = *ordinal;
                        if let Some(n) = ordinal {
                            *n = n.saturating_add(1);
                        }
                        out.document.blocks.push(Block::ListItem {
                            text: text.clone(),
                            depth: lists.len(),
                            ordinal: value,
                        });
                    }
                } else {
                    out.text(&text, &style);
                }
                text.clear();
                if end == TagEnd::Item
                    && let Some(mut images) = item_images.pop()
                {
                    out.document.blocks.append(&mut images);
                }
                style = if quote_depth > 0 {
                    "quote"
                } else {
                    "paragraph"
                }
                .into();
            }
            Event::Html(_) | Event::InlineHtml(_) => {
                out.document.omissions.push("raw_html_omitted".into())
            }
            _ => {}
        }
        if out.document.blocks.len() > 8192 {
            return Err("document block budget exceeded");
        }
    }
    out.text(&text, &style);
    Ok(out)
}

pub fn html(source: &str) -> Result<ParsedDocument, &'static str> {
    if source.len() > 512 * 1024 {
        return Err("document byte budget exceeded");
    }
    let tree = scraper::Html::parse_fragment(source);
    let mut out = ParsedDocument::default();
    out.document.version = 1;
    for node in tree.tree.nodes() {
        if node.ancestors().any(|n| {
            n.value().as_element().is_some_and(|e| {
                matches!(
                    e.name(),
                    "script" | "style" | "iframe" | "object" | "template" | "svg"
                )
            })
        }) {
            continue;
        }
        match node.value() {
            scraper::Node::Text(t) => {
                if node.ancestors().any(|n| {
                    n.value()
                        .as_element()
                        .is_some_and(|e| matches!(e.name(), "table" | "li"))
                }) {
                    continue;
                }
                let style = node
                    .ancestors()
                    .find_map(|n| {
                        n.value().as_element().and_then(|e| match e.name() {
                            "h1" => Some("h1"),
                            "h2" => Some("h2"),
                            "h3" => Some("h3"),
                            "h4" => Some("h4"),
                            "h5" => Some("h5"),
                            "h6" => Some("h6"),
                            "pre" | "code" => Some("code"),
                            "blockquote" => Some("quote"),
                            _ => None,
                        })
                    })
                    .unwrap_or("paragraph");
                out.text(t, style);
            }
            scraper::Node::Element(e) if e.name() == "table" => {
                let el = scraper::ElementRef::wrap(node).unwrap();
                let tr = scraper::Selector::parse("tr").unwrap();
                let td = scraper::Selector::parse("td, th").unwrap();
                let rows = el
                    .select(&tr)
                    .map(|r| r.select(&td).map(visible_text).collect::<Vec<_>>())
                    .collect::<Vec<_>>();
                for row in &rows {
                    out.body.push_str(&row.join("\t"));
                    out.body.push('\n');
                }
                out.document.blocks.push(Block::Table {
                    rows,
                    header: el
                        .select(&scraper::Selector::parse("th").unwrap())
                        .next()
                        .is_some(),
                });
            }
            scraper::Node::Element(e) if e.name() == "li" => {
                let text = node
                    .descendants()
                    .filter(|n| {
                        !n.ancestors().take_while(|a| a.id() != node.id()).any(|a| {
                            a.value().as_element().is_some_and(|e| {
                                matches!(e.name(), "script" | "style" | "ul" | "ol")
                            })
                        })
                    })
                    .filter_map(|n| match n.value() {
                        scraper::Node::Text(t) => Some(t.to_string()),
                        scraper::Node::Element(e) if e.name() == "img" => {
                            Some(format!("[图片：{}]", e.attr("alt").unwrap_or("")))
                        }
                        _ => None,
                    })
                    .collect::<String>();
                let depth = node
                    .ancestors()
                    .filter(|n| {
                        n.value()
                            .as_element()
                            .is_some_and(|e| matches!(e.name(), "ul" | "ol"))
                    })
                    .count();
                let ordinal = node.parent().and_then(|p| {
                    p.value()
                        .as_element()
                        .filter(|e| e.name() == "ol")
                        .map(|e| {
                            e.attr("start")
                                .and_then(|s| s.parse::<u64>().ok())
                                .unwrap_or(1)
                                .saturating_add(
                                    node.prev_siblings()
                                        .filter(|s| {
                                            s.value().as_element().is_some_and(|e| e.name() == "li")
                                        })
                                        .count() as u64,
                                )
                        })
                });
                out.body.push_str(&text);
                out.body.push('\n');
                out.document.blocks.push(Block::ListItem {
                    text,
                    depth,
                    ordinal,
                });
            }
            scraper::Node::Element(e) if e.name() == "img" => {
                if let Some(src) = e.attr("data-src").or_else(|| e.attr("src")) {
                    let mut caption = e.attr("alt").unwrap_or("").to_owned();
                    if let Some(cell) = node.ancestors().find(|a| {
                        a.value()
                            .as_element()
                            .is_some_and(|e| matches!(e.name(), "td" | "th"))
                    }) {
                        let col = cell
                            .prev_siblings()
                            .filter(|a| {
                                a.value()
                                    .as_element()
                                    .is_some_and(|e| matches!(e.name(), "td" | "th"))
                            })
                            .count()
                            + 1;
                        let row = cell
                            .ancestors()
                            .find(|a| a.value().as_element().is_some_and(|e| e.name() == "tr"))
                            .map(|r| {
                                r.prev_siblings()
                                    .filter(|a| {
                                        a.value().as_element().is_some_and(|e| e.name() == "tr")
                                    })
                                    .count()
                                    + 1
                            })
                            .unwrap_or(1);
                        caption = format!("表格第{row}行第{col}列：{caption}");
                    }
                    out.image(src, &caption);
                } else {
                    out.document.omissions.push("image_missing_source".into());
                }
            }
            scraper::Node::Element(e) if matches!(e.name(), "video" | "audio" | "iframe") => out
                .document
                .omissions
                .push("embedded_media_requires_resolution".into()),
            _ => {}
        }
        if out.document.blocks.len() > 8192 {
            return Err("document block budget exceeded");
        }
    }
    Ok(out)
}

fn visible_text(el: scraper::ElementRef<'_>) -> String {
    el.descendants()
        .filter(|n| {
            !n.ancestors().any(|a| {
                a.value().as_element().is_some_and(|e| {
                    matches!(
                        e.name(),
                        "script" | "style" | "iframe" | "object" | "template" | "svg"
                    )
                })
            })
        })
        .filter_map(|n| {
            n.value().as_text().map(|t| t.to_string()).or_else(|| {
                n.value()
                    .as_element()
                    .filter(|e| e.name() == "img")
                    .map(|e| format!("[图片：{}]", e.attr("alt").unwrap_or("")))
            })
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn c02_list_images_preserve_text_numbering_and_required_positions() {
        for d in [markdown("3. 前![原图](https://cdn.invalid/a.png)后\n4. 下一项\n").unwrap(),
                  html("<ol start='3'><li>前<img src='https://cdn.invalid/a.png' alt='原图'>后</li><li>下一项</li></ol>").unwrap()] {
            let items=d.document.blocks.iter().filter_map(|b| if let Block::ListItem{text,ordinal,depth}=b {Some((text,ordinal,depth))} else {None}).collect::<Vec<_>>();
            assert_eq!(items.len(),2);
            assert!(items[0].0.starts_with("前[图片") && items[0].0.ends_with("后"));
            assert_eq!((*items[0].1,*items[1].1),(Some(3),Some(4)));
            assert_eq!(d.resources.len(),1);
            assert!(d.resources[0].required);
            assert!(matches!(d.document.blocks[1],Block::Resource{..}));
        }
    }
    #[test]
    fn c02_table_images_preserve_cell_text_positions_and_required_resources() {
        let md = markdown("|列一|列二|\n|---|---|\n|前![图](https://cdn.invalid/a.png)后|乙|\n")
            .unwrap();
        assert!(
            matches!(&md.document.blocks[0],Block::Table{rows,..} if rows[1][0]=="前[图片1]后")
        );
        assert!(
            matches!(&md.document.blocks[1],Block::Resource{caption,..} if caption.contains("第2行第1列"))
        );
        assert_eq!(md.resources.len(), 1);
        assert!(md.resources[0].required);
        let html=html("<table><tr><td>前<img src='https://cdn.invalid/a.png' alt='原图'>后</td><td>乙</td></tr></table>").unwrap();
        assert!(
            matches!(&html.document.blocks[0],Block::Table{rows,..} if rows[0][0]=="前[图片：原图]后")
        );
        assert!(
            matches!(&html.document.blocks[1],Block::Resource{caption,..} if caption.contains("第1行第1列"))
        );
        assert_eq!(html.resources.len(), 1);
        assert!(html.resources[0].required);
    }

    #[test]
    fn c02_document_lists_tables_headings_quotes_and_code_keep_semantics() {
        let d=markdown("## 标题\n\n> 引用\n\n3. 第一项\n   - 内层\n4. 第二项\n\n|列一|列二|\n|---|---|\n|甲|乙|\n\n```text\n安全代码\n```\n").unwrap();
        assert!(
            d.document
                .blocks
                .iter()
                .any(|b| matches!(b,Block::Text{style,text} if style=="h2"&&text=="标题"))
        );
        assert!(
            d.document
                .blocks
                .iter()
                .any(|b| matches!(b,Block::Text{style,text} if style=="quote"&&text=="引用"))
        );
        let list = d
            .document
            .blocks
            .iter()
            .filter_map(|b| {
                if let Block::ListItem {
                    text,
                    depth,
                    ordinal,
                } = b
                {
                    Some((text.as_str(), *depth, *ordinal))
                } else {
                    None
                }
            })
            .collect::<Vec<_>>();
        assert_eq!(
            list,
            vec![
                ("第一项", 1, Some(3)),
                ("内层", 2, None),
                ("第二项", 1, Some(4))
            ]
        );
        assert!(d.document.blocks.iter().any(|b|matches!(b,Block::Table{rows,header} if *header&&rows==&vec![vec!["列一","列二"],vec!["甲","乙"]])));
        assert!(d.body.contains("安全代码"));
        let h=html("<h2>标题</h2><blockquote>引用</blockquote><ol start='3'><li>甲<ul><li>乙</li></ul></li><li>丙</li></ol><table><tr><th>列</th></tr><tr><td>内容<script>sentinel</script></td></tr></table>").unwrap();
        let list = h
            .document
            .blocks
            .iter()
            .filter_map(|b| {
                if let Block::ListItem {
                    text,
                    depth,
                    ordinal,
                } = b
                {
                    Some((text.as_str(), *depth, *ordinal))
                } else {
                    None
                }
            })
            .collect::<Vec<_>>();
        assert_eq!(
            list,
            vec![("甲", 1, Some(3)), ("乙", 2, None), ("丙", 1, Some(4))]
        );
        assert!(!h.body.contains("sentinel"));
        assert!(h.body.contains("内容"));
    }
    #[test]
    fn c02_c06_long_markdown_and_thirty_images_are_inert_and_ordered() {
        let mut source = "完整正文".repeat(3000);
        for n in 0..30 {
            source.push_str(&format!(
                "\n\n段落{n}\n\n![配图](https://cdn.invalid/{n}.jpg?sign=secret)\n"
            ));
        }
        source.push_str("\n<script>alert('secret')</script>\n");
        let out = markdown(&source).unwrap();
        assert!(out.body.len() > 12000);
        assert_eq!(out.resources.len(), 30);
        assert!(
            !serde_json::to_string(&out.document)
                .unwrap()
                .contains("secret")
        );
        assert_eq!(out.resources[29].position, 29);
        assert!(out.urls.values().all(|u| u.contains("sign=secret")));
    }
    #[test]
    fn c06_html_never_preserves_scripts_remote_attributes_or_css() {
        let out=html("<p>正文</p><script>secret</script><style>secret</style><img src='https://cdn.invalid/a.png' onerror='secret'><img src='file:///secret'><iframe src='https://secret.invalid'></iframe>").unwrap();
        assert_eq!(out.body.trim(), "正文");
        assert_eq!(out.resources.len(), 1);
        assert!(
            !serde_json::to_string(&out.document)
                .unwrap()
                .contains("secret")
        );
        assert_eq!(out.document.omissions.len(), 2);
    }
}
