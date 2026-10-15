//! Convert source markup into inert typed blocks. URLs only leave in transient resource plans.
mod from_html;
mod from_markdown;

use std::collections::BTreeMap;
use trace_contracts::{
    content::{Block, Document},
    plugin::ResourceIdentityDto,
};

pub use from_html::html;
pub use from_markdown::markdown;

/// Largest accepted source document and block count of its result.
const MAX_SOURCE_BYTES: usize = 512 * 1024;
const MAX_BLOCKS: usize = 8192;

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

impl ParsedDocument {
    /// An empty document of schema version 1.
    fn empty() -> Self {
        let mut out = Self::default();
        out.document.version = 1;
        out
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn list_images_preserve_text_numbering_and_required_positions() {
        for d in [markdown("3. 前![原图](https://cdn.invalid/a.png)后\n4. 下一项\n").unwrap(),
                  html("<ol start='3'><li>前<img src='https://cdn.invalid/a.png' alt='原图'>后</li><li>下一项</li></ol>").unwrap()] {
            let items = d
                .document
                .blocks
                .iter()
                .filter_map(|b| match b {
                    Block::ListItem { text, ordinal, depth } => Some((text, ordinal, depth)),
                    _ => None,
                })
                .collect::<Vec<_>>();
            assert_eq!(items.len(),2);
            assert!(items[0].0.starts_with("前[图片") && items[0].0.ends_with("后"));
            assert_eq!((*items[0].1,*items[1].1),(Some(3),Some(4)));
            assert_eq!(d.resources.len(),1);
            assert!(d.resources[0].required);
            assert!(matches!(d.document.blocks[1],Block::Resource{..}));
        }
    }
    #[test]
    fn table_images_preserve_cell_text_positions_and_required_resources() {
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
    fn document_lists_tables_headings_quotes_and_code_keep_semantics() {
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
    fn long_markdown_and_thirty_images_are_inert_and_ordered() {
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
    fn html_never_preserves_scripts_remote_attributes_or_css() {
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
