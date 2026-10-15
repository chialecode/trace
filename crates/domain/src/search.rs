//! 搜索规范化 v1（docs/design.md §13.2、windows-runtime-contracts §5）。
//!
//! v1 规则：NFC + Unicode 小写；保留标点与空白语义；不做分词同义扩展。
//! 索引与查询必须使用同一实现；原文不变。

use unicode_normalization::UnicodeNormalization;

pub const SEARCH_NORMALIZATION_VERSION: i64 = 1;

pub fn normalize_v1(text: &str) -> String {
    text.nfc().flat_map(|c| c.to_lowercase()).collect()
}

/// FTS MATCH 的引号包裹：把用户输入整体作为字面短语，特殊字符不构成语法。
pub fn fts_quote_phrase(user_input: &str) -> String {
    let mut out = String::with_capacity(user_input.len() + 2);
    out.push('"');
    for c in user_input.chars() {
        if c == '"' {
            out.push_str("\"\"");
        } else {
            out.push(c);
        }
    }
    out.push('"');
    out
}

/// LIKE 字面模式转义（配合 ESCAPE '\'）。
pub fn like_escape(user_input: &str) -> String {
    let mut out = String::with_capacity(user_input.len() + 2);
    for c in user_input.chars() {
        match c {
            '\\' | '%' | '_' => {
                out.push('\\');
                out.push(c);
            }
            _ => out.push(c),
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn nfc_and_lowercase() {
        // NFC：组合字符与预组合形式一致（v1 只做 NFC + 小写，不做兼容分解）。
        assert_eq!(normalize_v1("Cafe\u{0301}"), normalize_v1("Café"));
        assert_eq!(normalize_v1("Hello"), "hello");
        assert_eq!(normalize_v1("中文搜索"), "中文搜索");
        // 保留标点与空白。
        assert_eq!(normalize_v1("a b,c"), "a b,c");
        // v1 明确不做全角→半角折叠（记录语义边界，待用户确认需求再升级版本）。
        assert_eq!(normalize_v1("ＣＡＦＥ"), "ｃａｆｅ");
    }

    #[test]
    fn fts_quoting_neutralizes_syntax() {
        assert_eq!(fts_quote_phrase("a\" OR 1=1 --"), "\"a\"\" OR 1=1 --\"");
        // NOT、AND 等不再作为操作符。
        let q = fts_quote_phrase("NOT AND OR");
        assert!(q.starts_with('"') && q.ends_with('"'));
    }

    #[test]
    fn like_escape_handles_wildcards() {
        assert_eq!(like_escape("50%_off\\x"), "50\\%\\_off\\\\x");
    }
}
