//! Plain text out of site markup, and the tags and mentions inside it.
use std::collections::BTreeSet;

/// Longest text one field keeps; sites cap posts far below this.
const TEXT_MAX: usize = 100_000;

/// Markup to plain text: `<br>` becomes a line break, an `<img alt>` (an emoji) becomes its
/// alt text, scripts and styles vanish, entities are decoded and blank runs collapse.
pub fn plain(markup: &str) -> String {
    let mut prepared = String::with_capacity(markup.len());
    let lower = markup.to_ascii_lowercase();
    let mut i = 0;
    while i < markup.len() {
        if lower[i..].starts_with("<br")
            && let Some(end) = markup[i..].find('>')
        {
            prepared.push('\n');
            i += end + 1;
            continue;
        }
        let ch = markup[i..].chars().next().unwrap_or(' ');
        prepared.push(ch);
        i += ch.len_utf8();
    }
    let tree = scraper::Html::parse_fragment(&prepared);
    let mut out = String::new();
    for node in tree.tree.nodes() {
        if node.ancestors().any(|n| {
            n.value()
                .as_element()
                .is_some_and(|e| matches!(e.name(), "script" | "style"))
        }) {
            continue;
        }
        match node.value() {
            scraper::Node::Text(t) => out.push_str(t),
            scraper::Node::Element(e) if e.name() == "img" => {
                if let Some(alt) = e.attr("alt") {
                    out.push_str(alt);
                }
            }
            _ => {}
        }
    }
    tidy(&out)
}

/// Trims every line, drops control characters and joins runs of blank lines into one break.
pub fn tidy(text: &str) -> String {
    let mut lines: Vec<String> = Vec::new();
    let mut blank = false;
    for line in text.replace("\r\n", "\n").replace('\r', "\n").split('\n') {
        let line: String = line
            .chars()
            .filter(|c| !c.is_control() || *c == '\t')
            .collect();
        let line = line.trim();
        if line.is_empty() {
            blank = !lines.is_empty();
            continue;
        }
        if blank {
            lines.push(String::new());
            blank = false;
        }
        lines.push(line.to_owned());
    }
    let joined = lines.join("\n");
    joined.chars().take(TEXT_MAX).collect()
}

/// The first line, cut to `max` characters: a title for a work that has none of its own.
pub fn headline(text: &str, max: usize) -> String {
    let line = text.lines().find(|l| !l.trim().is_empty()).unwrap_or("");
    let line = line.trim();
    if line.chars().count() <= max {
        return line.to_owned();
    }
    let mut cut: String = line.chars().take(max.saturating_sub(1)).collect();
    cut.push('…');
    cut
}

fn is_term_char(c: char) -> bool {
    c.is_alphanumeric() || c == '_' || c == '·'
}

/// `#tag` terms (X, Instagram, TikTok, YouTube), in first-seen order without repeats.
pub fn hashtags(text: &str) -> Vec<String> {
    let mut seen = BTreeSet::new();
    let mut out = Vec::new();
    let chars: Vec<char> = text.chars().collect();
    let mut i = 0;
    while i < chars.len() {
        if chars[i] != '#' && chars[i] != '＃' {
            i += 1;
            continue;
        }
        let start = i + 1;
        let mut end = start;
        while end < chars.len() && is_term_char(chars[end]) {
            end += 1;
        }
        let term: String = chars[start..end].iter().collect();
        if !term.is_empty() && term.chars().count() <= 64 && seen.insert(term.clone()) {
            out.push(term);
        }
        i = end.max(i + 1);
    }
    out
}

/// `#话题#` terms (Weibo, Xiaohongshu): text between two hashes on one line, no longer than
/// 64 characters and not starting or ending with a space.
pub fn topics(text: &str) -> Vec<String> {
    let mut seen = BTreeSet::new();
    let mut out = Vec::new();
    for line in text.lines() {
        let chars: Vec<char> = line.chars().collect();
        let mut i = 0;
        while i < chars.len() {
            if chars[i] != '#' && chars[i] != '＃' {
                i += 1;
                continue;
            }
            let Some(close) =
                (i + 1..chars.len().min(i + 66)).find(|j| chars[*j] == '#' || chars[*j] == '＃')
            else {
                break;
            };
            let term: String = chars[i + 1..close].iter().collect();
            let plain = !term.is_empty() && term == term.trim();
            if plain && seen.insert(term.clone()) {
                out.push(term);
                i = close + 1;
            } else {
                i = close;
            }
        }
    }
    out
}

/// `@name` terms, in first-seen order without repeats.
pub fn mentions(text: &str) -> Vec<String> {
    let mut seen = BTreeSet::new();
    let mut out = Vec::new();
    let chars: Vec<char> = text.chars().collect();
    let mut i = 0;
    while i < chars.len() {
        if chars[i] != '@' && chars[i] != '＠' {
            i += 1;
            continue;
        }
        let start = i + 1;
        let mut end = start;
        while end < chars.len()
            && (is_term_char(chars[end]) || chars[end] == '-' || chars[end] == '.')
        {
            end += 1;
        }
        let name: String = chars[start..end].iter().collect();
        let name = name.trim_end_matches(['.', '-']).to_owned();
        if !name.is_empty() && name.chars().count() <= 64 && seen.insert(name.clone()) {
            out.push(name);
        }
        i = end.max(i + 1);
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn markup_becomes_plain_text_with_breaks_and_emoji_names() {
        let html = "第一行<br />第二行 <a href=\"https://t.cn/x\">链接</a><img alt=\"[doge]\" src=\"x\"> &amp; 完<script>bad()</script>";
        assert_eq!(plain(html), "第一行\n第二行 链接[doge] & 完");
    }

    #[test]
    fn blank_runs_collapse_and_control_characters_go() {
        assert_eq!(tidy("a\r\n\r\n\r\n  b \u{0}c\n"), "a\n\nb c");
        assert_eq!(tidy("\n\n"), "");
    }

    #[test]
    fn a_headline_is_the_first_line_cut_with_an_ellipsis() {
        assert_eq!(headline("\n  标题行\n正文", 10), "标题行");
        assert_eq!(headline("0123456789abc", 8), "0123456…");
        assert_eq!(headline("", 8), "");
    }

    #[test]
    fn plain_hashtags_end_at_the_first_character_that_is_not_part_of_a_word() {
        assert_eq!(
            hashtags("a #Travel_2026, #travel_2026 #旅行! #"),
            ["Travel_2026", "travel_2026", "旅行"]
        );
    }

    #[test]
    fn wrapped_topics_keep_inner_spaces_and_skip_stray_hashes() {
        assert_eq!(
            topics(
                "今天 #旅行# 很好 #两个 词# 结尾 # 孤立 #
#第二行# #旅行#"
            ),
            ["旅行", "两个 词", "第二行"]
        );
        assert!(topics("没有 # 话题").is_empty());
    }

    #[test]
    fn mentions_stop_at_punctuation_and_repeat_once() {
        assert_eq!(
            mentions("@张三 你好 @li-si. 再见 @张三，@a.b"),
            ["张三", "li-si", "a.b"]
        );
    }
}
