//! Markdown → typed blocks. Images inside tables and list items stay at their
//! text position as `[图片N]` markers while the resource blocks follow the container.
use super::{MAX_BLOCKS, MAX_SOURCE_BYTES, ParsedDocument};
use pulldown_cmark::{Event, Options, Parser, Tag, TagEnd};
use trace_contracts::content::Block;

struct Walker {
    out: ParsedDocument,
    text: String,
    style: String,
    in_image: bool,
    lists: Vec<Option<u64>>,
    item_images: Vec<Vec<Block>>,
    table: Option<Vec<Vec<String>>>,
    row: Vec<String>,
    table_resources: Vec<Block>,
    quote_depth: usize,
}

pub fn markdown(source: &str) -> Result<ParsedDocument, &'static str> {
    if source.len() > MAX_SOURCE_BYTES {
        return Err("document byte budget exceeded");
    }
    let mut walker = Walker {
        out: ParsedDocument::empty(),
        text: String::new(),
        style: "paragraph".to_owned(),
        in_image: false,
        lists: Vec::new(),
        item_images: Vec::new(),
        table: None,
        row: Vec::new(),
        table_resources: Vec::new(),
        quote_depth: 0,
    };
    for event in Parser::new_ext(
        source,
        Options::ENABLE_TABLES | Options::ENABLE_STRIKETHROUGH | Options::ENABLE_TASKLISTS,
    ) {
        walker.event(event);
        if walker.out.document.blocks.len() > MAX_BLOCKS {
            return Err("document block budget exceeded");
        }
    }
    walker.out.text(&walker.text, &walker.style);
    Ok(walker.out)
}

impl Walker {
    fn event(&mut self, event: Event) {
        match event {
            Event::Start(Tag::Image {
                dest_url, title, ..
            }) => self.image(&dest_url, &title),
            Event::End(TagEnd::Image) => self.in_image = false,
            Event::Start(Tag::Item) => self.item_images.push(Vec::new()),
            Event::Start(Tag::List(start)) => {
                if !self.text.trim().is_empty() {
                    if self.lists.is_empty() {
                        self.out.text(&self.text, &self.style);
                        self.text.clear();
                    } else {
                        self.list_item();
                    }
                }
                self.lists.push(start);
            }
            Event::End(TagEnd::List(_)) => {
                self.lists.pop();
            }
            Event::Start(Tag::BlockQuote(_)) => {
                self.quote_depth += 1;
                self.style = "quote".into();
            }
            Event::End(TagEnd::BlockQuote(_)) => {
                self.quote_depth = self.quote_depth.saturating_sub(1);
                self.style = "paragraph".into();
            }
            Event::Start(Tag::Table(_)) => {
                self.table = Some(Vec::new());
                self.row.clear();
            }
            Event::End(TagEnd::TableCell) => self.row.push(std::mem::take(&mut self.text)),
            Event::End(TagEnd::TableHead | TagEnd::TableRow) => {
                if let Some(rows) = &mut self.table {
                    rows.push(std::mem::take(&mut self.row));
                }
            }
            Event::End(TagEnd::Table) => self.table_end(),
            Event::TaskListMarker(done) => self.text.push_str(if done { "[x] " } else { "[ ] " }),
            Event::Start(Tag::Heading { level, .. }) => self.style = format!("{level}"),
            Event::Start(Tag::CodeBlock(_)) => self.style = "code".into(),
            Event::Text(t) | Event::Code(t) if !self.in_image => self.text.push_str(&t),
            Event::SoftBreak | Event::HardBreak => self.text.push('\n'),
            Event::End(
                end @ (TagEnd::Paragraph | TagEnd::Heading(_) | TagEnd::CodeBlock | TagEnd::Item),
            ) => self.block_end(end == TagEnd::Item),
            Event::Html(_) | Event::InlineHtml(_) => {
                self.out.document.omissions.push("raw_html_omitted".into())
            }
            _ => {}
        }
    }

    fn image(&mut self, dest_url: &str, title: &str) {
        let before = self.out.document.blocks.len();
        if let Some(rows) = &self.table {
            let caption = format!(
                "表格第{}行第{}列：{}",
                rows.len() + 1,
                self.row.len() + 1,
                title
            );
            self.out.image(dest_url, &caption);
            if self.out.document.blocks.len() > before {
                self.table_resources
                    .push(self.out.document.blocks.pop().unwrap());
                self.text
                    .push_str(&format!("[图片{}]", self.table_resources.len()));
            }
        } else if let Some(images) = self.item_images.last_mut() {
            self.out.image(
                dest_url,
                &format!("列表第{}层：{}", self.lists.len(), title),
            );
            if self.out.document.blocks.len() > before {
                images.push(self.out.document.blocks.pop().unwrap());
                self.text.push_str(&format!("[图片{}]", images.len()));
            }
        } else {
            self.out.text(&self.text, &self.style);
            self.text.clear();
            self.out.image(dest_url, title);
        }
        self.in_image = true;
    }

    /// The pending text becomes a list item numbered from the innermost list's counter.
    fn list_item(&mut self) {
        let depth = self.lists.len();
        let Some(ordinal) = self.lists.last_mut() else {
            return;
        };
        self.out.body.push_str(&self.text);
        self.out.body.push('\n');
        let value = *ordinal;
        if let Some(n) = ordinal {
            *n = n.saturating_add(1);
        }
        self.out.document.blocks.push(Block::ListItem {
            text: std::mem::take(&mut self.text),
            depth,
            ordinal: value,
        });
    }

    fn table_end(&mut self) {
        if let Some(rows) = self.table.take() {
            for row in &rows {
                self.out.body.push_str(&row.join("\t"));
                self.out.body.push('\n');
            }
            self.out
                .document
                .blocks
                .push(Block::Table { rows, header: true });
            self.out.document.blocks.append(&mut self.table_resources);
        }
    }

    /// End of a paragraph, heading, code block or list item.
    fn block_end(&mut self, item: bool) {
        if !self.lists.is_empty() {
            if !self.text.trim().is_empty() {
                self.list_item();
            }
        } else {
            self.out.text(&self.text, &self.style);
        }
        self.text.clear();
        if item && let Some(mut images) = self.item_images.pop() {
            self.out.document.blocks.append(&mut images);
        }
        self.style = if self.quote_depth > 0 {
            "quote"
        } else {
            "paragraph"
        }
        .into();
    }
}
