//! HTML → typed blocks. Scripts, styles, frames and every attribute except a few
//! known ones are dropped; media elements stay visible as omissions.
use super::{MAX_BLOCKS, MAX_SOURCE_BYTES, ParsedDocument};
use scraper::{ElementRef, Node};
use trace_contracts::content::Block;

/// Subtrees that never contribute content.
const INERT: [&str; 6] = ["script", "style", "iframe", "object", "template", "svg"];

pub fn html(source: &str) -> Result<ParsedDocument, &'static str> {
    if source.len() > MAX_SOURCE_BYTES {
        return Err("document byte budget exceeded");
    }
    let tree = scraper::Html::parse_fragment(source);
    let mut out = ParsedDocument::empty();
    for node in tree.tree.nodes() {
        if node.ancestors().any(|n| {
            n.value()
                .as_element()
                .is_some_and(|e| INERT.contains(&e.name()))
        }) {
            continue;
        }
        match node.value() {
            Node::Text(t) => {
                let in_container = node.ancestors().any(|n| {
                    n.value()
                        .as_element()
                        .is_some_and(|e| matches!(e.name(), "table" | "li"))
                });
                if !in_container {
                    let style = node
                        .ancestors()
                        .find_map(|n| n.value().as_element().and_then(|e| text_style(e.name())))
                        .unwrap_or("paragraph");
                    out.text(t, style);
                }
            }
            Node::Element(e) if e.name() == "table" => {
                table(&mut out, ElementRef::wrap(node).unwrap())
            }
            Node::Element(e) if e.name() == "li" => {
                list_item(&mut out, ElementRef::wrap(node).unwrap())
            }
            Node::Element(e) if e.name() == "img" => {
                image(&mut out, ElementRef::wrap(node).unwrap())
            }
            Node::Element(e) if matches!(e.name(), "video" | "audio" | "iframe") => out
                .document
                .omissions
                .push("embedded_media_requires_resolution".into()),
            _ => {}
        }
        if out.document.blocks.len() > MAX_BLOCKS {
            return Err("document block budget exceeded");
        }
    }
    Ok(out)
}

fn text_style(element: &str) -> Option<&'static str> {
    match element {
        "h1" => Some("h1"),
        "h2" => Some("h2"),
        "h3" => Some("h3"),
        "h4" => Some("h4"),
        "h5" => Some("h5"),
        "h6" => Some("h6"),
        "pre" | "code" => Some("code"),
        "blockquote" => Some("quote"),
        _ => None,
    }
}

fn table(out: &mut ParsedDocument, el: ElementRef) {
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

fn list_item(out: &mut ParsedDocument, node: ElementRef) {
    let text = node
        .descendants()
        .filter(|n| {
            !n.ancestors().take_while(|a| a.id() != node.id()).any(|a| {
                a.value()
                    .as_element()
                    .is_some_and(|e| matches!(e.name(), "script" | "style" | "ul" | "ol"))
            })
        })
        .filter_map(|n| match n.value() {
            Node::Text(t) => Some(t.to_string()),
            Node::Element(e) if e.name() == "img" => {
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
                            .filter(|s| s.value().as_element().is_some_and(|e| e.name() == "li"))
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

fn image(out: &mut ParsedDocument, node: ElementRef) {
    let e = node.value();
    let Some(src) = e.attr("data-src").or_else(|| e.attr("src")) else {
        out.document.omissions.push("image_missing_source".into());
        return;
    };
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
                    .filter(|a| a.value().as_element().is_some_and(|e| e.name() == "tr"))
                    .count()
                    + 1
            })
            .unwrap_or(1);
        caption = format!("表格第{row}行第{col}列：{caption}");
    }
    out.image(src, &caption);
}

fn visible_text(el: ElementRef<'_>) -> String {
    el.descendants()
        .filter(|n| {
            !n.ancestors().any(|a| {
                a.value()
                    .as_element()
                    .is_some_and(|e| INERT.contains(&e.name()))
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
