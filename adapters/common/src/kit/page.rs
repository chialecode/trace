//! State that site pages embed as JSON: `window.__INITIAL_STATE__ = {...}`, a
//! `<script type="application/json">` element, `var ytInitialData = {...};`.
use serde_json::Value;

/// Largest document read out of a page.
const STATE_MAX: usize = 6 * 1024 * 1024;

/// The JSON value that follows `marker` (the first `{` or `[` after it), found by balancing
/// brackets outside strings. JavaScript's bare `undefined` is read as `null`.
pub fn json_after(page: &str, marker: &str) -> Option<Value> {
    let start = page.find(marker)? + marker.len();
    let rest = &page[start..];
    let open = rest.find(['{', '['])?;
    // Only separators may sit between the marker and the value.
    if !rest[..open]
        .chars()
        .all(|c| c.is_whitespace() || matches!(c, '=' | ':' | '(' | ','))
    {
        return None;
    }
    let text = balanced(&rest[open..])?;
    serde_json::from_str(&text).ok()
}

/// Why `json_after` found nothing, in words that hold no page content: the step that failed
/// and, for a document that does not parse, the position and the word it stopped at.
pub fn explain_json_after(page: &str, marker: &str) -> String {
    let Some(start) = page.find(marker).map(|i| i + marker.len()) else {
        return "marker absent".into();
    };
    let rest = &page[start..];
    let Some(open) = rest.find(['{', '[']) else {
        return "no value after the marker".into();
    };
    let between: String = rest[..open].chars().take(16).collect();
    if !rest[..open]
        .chars()
        .all(|c| c.is_whitespace() || matches!(c, '=' | ':' | '(' | ','))
    {
        let shown: String = between
            .chars()
            .map(|c| {
                if c.is_ascii_alphanumeric() || matches!(c, '.' | '(' | '=' | ' ') {
                    c
                } else {
                    '?'
                }
            })
            .collect();
        return format!("value does not follow the marker directly ({shown:?})");
    }
    let Some(text) = balanced(&rest[open..]) else {
        return "value is not bracket-balanced within the limit".into();
    };
    match serde_json::from_str::<Value>(&text) {
        Ok(_) => "parses".into(),
        Err(e) => {
            let at = text
                .lines()
                .nth(e.line().saturating_sub(1))
                .and_then(|line| line.get(e.column().saturating_sub(1)..))
                .unwrap_or("");
            let word: String = at
                .chars()
                .take_while(|c| c.is_ascii_alphanumeric() || *c == '_')
                .take(16)
                .collect();
            format!("{e}; stopped at {word:?}")
        }
    }
}

/// The text of the `<script>` element with this `id` (or, for `needle`, the first one that
/// contains it), parsed as JSON.
pub fn script_json(page: &str, id: &str) -> Option<Value> {
    serde_json::from_str(&script_text(page, id)?).ok()
}

/// The same for a document the page percent-encodes (`%7B%22key%22%3A1%7D`) so that it can
/// sit in a script element untouched.
pub fn script_json_encoded(page: &str, id: &str) -> Option<Value> {
    let text = script_text(page, id)?;
    let decoded = percent_encoding::percent_decode_str(text.trim())
        .decode_utf8()
        .ok()?;
    serde_json::from_str(&decoded).ok()
}

fn script_text(page: &str, id: &str) -> Option<String> {
    let doc = scraper::Html::parse_document(page);
    let selector = scraper::Selector::parse(&format!("script#{id}")).ok()?;
    let element = doc.select(&selector).next()?;
    let text: String = element.text().collect();
    (text.len() <= STATE_MAX).then_some(text)
}

/// The `src` of every `<script>` element of a page, in order.
pub fn script_srcs(page: &str) -> Vec<String> {
    let doc = scraper::Html::parse_document(page);
    let Ok(selector) = scraper::Selector::parse("script[src]") else {
        return Vec::new();
    };
    doc.select(&selector)
        .filter_map(|e| e.value().attr("src").map(str::to_owned))
        .collect()
}

/// The `content` of the page's `<meta name="…">` element.
pub fn meta_content(page: &str, name: &str) -> Option<String> {
    if name.is_empty()
        || !name
            .chars()
            .all(|c| c.is_ascii_alphanumeric() || matches!(c, '-' | '_' | ':'))
    {
        return None;
    }
    let doc = scraper::Html::parse_document(page);
    let selector = scraper::Selector::parse(&format!("meta[name=\"{name}\"]")).ok()?;
    doc.select(&selector)
        .find_map(|e| e.value().attr("content").map(str::to_owned))
}

/// Most elements whose attributes [`nested_attrs`] reads.
const NESTED_MAX: usize = 64;

/// For every element whose `id` starts with `id_prefix` (in document order): the `attr` of each
/// element child of its first element child, `None` where a child has none. An inline image
/// holding a group of shapes reads as `[[its shapes' attr…]]`.
pub fn nested_attrs(page: &str, id_prefix: &str, attr: &str) -> Vec<Vec<Option<String>>> {
    let doc = scraper::Html::parse_document(page);
    let Ok(selector) = scraper::Selector::parse("[id]") else {
        return Vec::new();
    };
    doc.select(&selector)
        .filter(|e| {
            e.value()
                .attr("id")
                .is_some_and(|id| id.starts_with(id_prefix))
        })
        .take(NESTED_MAX)
        .map(|element| {
            let mut children = element.children().filter_map(scraper::ElementRef::wrap);
            children
                .next()
                .map(|first| {
                    first
                        .children()
                        .filter_map(scraper::ElementRef::wrap)
                        .map(|child| child.value().attr(attr).map(str::to_owned))
                        .collect()
                })
                .unwrap_or_default()
        })
        .collect()
}

/// Most JSON script elements read from one page, and the most text they may hold together.
const SCRIPTS_JSON_MAX: usize = 400;
const SCRIPTS_TEXT_MAX: usize = 24 * 1024 * 1024;

/// Every `<script type="application/json">` document of a page that parses, in order. Pages
/// that stream their data hold dozens of them; one that does not parse is skipped.
pub fn json_scripts(page: &str) -> Vec<Value> {
    let doc = scraper::Html::parse_document(page);
    let Ok(selector) = scraper::Selector::parse(r#"script[type="application/json"]"#) else {
        return Vec::new();
    };
    let mut total = 0usize;
    let mut out = Vec::new();
    for element in doc.select(&selector).take(SCRIPTS_JSON_MAX) {
        let text: String = element.text().collect();
        total += text.len();
        if total > SCRIPTS_TEXT_MAX {
            break;
        }
        if text.len() <= STATE_MAX
            && let Ok(value) = serde_json::from_str(&text)
        {
            out.push(value);
        }
    }
    out
}

/// Deepest nesting [`find_all`] looks into; real documents stay far below it.
const DEPTH_MAX: usize = 48;

/// The first object under `root` (in document order) that `wanted` accepts. An accepted object
/// is returned as it is; what is inside it is not searched.
pub fn find_object<'a>(root: &'a Value, wanted: &dyn Fn(&Value) -> bool) -> Option<&'a Value> {
    fn walk<'a>(v: &'a Value, wanted: &dyn Fn(&Value) -> bool, depth: usize) -> Option<&'a Value> {
        if depth > DEPTH_MAX {
            return None;
        }
        match v {
            Value::Object(map) => {
                if wanted(v) {
                    return Some(v);
                }
                map.values()
                    .find_map(|child| walk(child, wanted, depth + 1))
            }
            Value::Array(items) => items.iter().find_map(|i| walk(i, wanted, depth + 1)),
            _ => None,
        }
    }
    walk(root, wanted, 0)
}

/// Every value under `root` that is held by one of `keys`, in the order of the arrays that
/// hold them. A match is not searched inside: a post's attached video is not a video of the
/// list.
pub fn find_all<'a>(root: &'a Value, keys: &[&str]) -> Vec<(&'a str, &'a Value)> {
    fn walk<'a>(v: &'a Value, keys: &[&str], depth: usize, out: &mut Vec<(&'a str, &'a Value)>) {
        if depth > DEPTH_MAX {
            return;
        }
        match v {
            Value::Object(map) => {
                for (key, child) in map {
                    if keys.contains(&key.as_str()) {
                        out.push((key.as_str(), child));
                    } else {
                        walk(child, keys, depth + 1, out);
                    }
                }
            }
            Value::Array(items) => items.iter().for_each(|i| walk(i, keys, depth + 1, out)),
            _ => {}
        }
    }
    let mut out = Vec::new();
    walk(root, keys, 0, &mut out);
    out
}

/// The index after `Name(…)` that starts at `from`, with its arguments skipped (strings in
/// them may hold brackets).
fn skip_construction(text: &str, from: usize) -> Option<usize> {
    let rest = &text[from..];
    let name = rest
        .find(|c: char| !(c.is_ascii_alphanumeric() || c == '_' || c == '.'))
        .filter(|n| *n > 0)?;
    if !rest[name..].starts_with('(') {
        return None;
    }
    let (mut depth, mut quote, mut escaped) = (0usize, None::<char>, false);
    for (offset, c) in rest[name..].char_indices() {
        if let Some(q) = quote {
            if escaped {
                escaped = false;
            } else if c == '\\' {
                escaped = true;
            } else if c == q {
                quote = None;
            }
            continue;
        }
        match c {
            '"' | '\'' | '`' => quote = Some(c),
            '(' => depth += 1,
            ')' => {
                depth = depth.checked_sub(1)?;
                if depth == 0 {
                    return Some(from + name + offset + 1);
                }
            }
            _ => {}
        }
    }
    None
}

/// Copies the bracketed value at the start of `text`, turning JavaScript's `undefined`, `NaN`,
/// `Infinity` and `new …(…)` into `null`.
fn balanced(text: &str) -> Option<String> {
    let mut out = String::new();
    let mut depth = 0usize;
    let mut in_string = false;
    let mut escaped = false;
    let bytes = text.as_bytes();
    let mut i = 0;
    while i < text.len() {
        if out.len() > STATE_MAX {
            return None;
        }
        let c = text[i..].chars().next()?;
        if in_string {
            out.push(c);
            if escaped {
                escaped = false;
            } else if c == '\\' {
                escaped = true;
            } else if c == '"' {
                in_string = false;
            }
            i += c.len_utf8();
            continue;
        }
        match c {
            '"' => {
                in_string = true;
                out.push(c);
            }
            '{' | '[' => {
                depth += 1;
                out.push(c);
            }
            '}' | ']' => {
                depth = depth.checked_sub(1)?;
                out.push(c);
                if depth == 0 {
                    return Some(out);
                }
            }
            'u' if bytes[i..].starts_with(b"undefined") => {
                out.push_str("null");
                i += "undefined".len();
                continue;
            }
            'N' if bytes[i..].starts_with(b"NaN") => {
                out.push_str("null");
                i += "NaN".len();
                continue;
            }
            'I' if bytes[i..].starts_with(b"Infinity") => {
                out.push_str("null");
                i += "Infinity".len();
                continue;
            }
            'n' if bytes[i..].starts_with(b"new ") => {
                // A serialised object (`new Date("…")`, `new Map([…])`) is not data of the page.
                out.push_str("null");
                i = skip_construction(text, i + "new ".len())?;
                continue;
            }
            _ => out.push(c),
        }
        i += c.len_utf8();
    }
    None
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn a_state_assignment_is_cut_out_at_its_closing_bracket() {
        let page = r#"<script>window.__INITIAL_STATE__={"a":{"b":[1,2,{"c":"}"}],"d":"x\"}"},"u":undefined};window.other=1</script>"#;
        assert_eq!(
            json_after(page, "window.__INITIAL_STATE__="),
            Some(json!({"a": {"b": [1, 2, {"c": "}"}], "d": "x\"}"}, "u": null}))
        );
    }

    #[test]
    fn var_assignments_with_spaces_work_and_other_text_before_the_value_does_not() {
        let page = "var ytInitialData = {\"x\": 1};</script>";
        assert_eq!(json_after(page, "ytInitialData ="), Some(json!({"x": 1})));
        assert_eq!(
            json_after("ytInitialData is {\"x\":1}", "ytInitialData"),
            None
        );
    }

    #[test]
    fn a_meta_value_and_the_attributes_inside_an_inline_image_are_read() {
        let page = r#"<head><meta name="site-verification" content="abc+/="><meta name="other" content="x"></head>
            <svg id="loading-x-anim-0"><g><path d="M 1,2 C 3"/><path d="M 4,5 C 6"/><path/></g></svg>
            <svg id="loading-x-anim-1"><g><path d="M 7,8"/></g></svg>
            <svg id="unrelated"><g><path d="no"/></g></svg>
            <svg id="loading-x-anim-2"></svg>"#;
        assert_eq!(
            meta_content(page, "site-verification").as_deref(),
            Some("abc+/=")
        );
        assert_eq!(meta_content(page, "missing"), None);
        assert_eq!(meta_content(page, "bad\"name"), None);
        assert_eq!(
            nested_attrs(page, "loading-x-anim", "d"),
            [
                vec![
                    Some("M 1,2 C 3".to_owned()),
                    Some("M 4,5 C 6".to_owned()),
                    None
                ],
                vec![Some("M 7,8".to_owned())],
                vec![],
            ]
        );
    }

    #[test]
    fn scripts_are_listed_and_a_match_hides_what_is_inside_it() {
        let page = r#"<script src="https://a.example/main.js"></script><script>x</script><script src="/b.js"></script>"#;
        assert_eq!(script_srcs(page), ["https://a.example/main.js", "/b.js"]);
        let doc = serde_json::json!({"a": [{"hit": {"id": 1, "x": {"hit": {"id": 2}}}}, {"b": {"hit": {"id": 3}}}]});
        let ids: Vec<_> = find_all(&doc, &["hit"])
            .iter()
            .map(|(_, v)| v["id"].as_i64().unwrap())
            .collect();
        assert_eq!(ids, [1, 3]);
    }

    #[test]
    fn json_scripts_are_read_in_order_and_an_object_is_found_by_what_it_holds() {
        let page = r#"<script type="application/json">{"a":[{"code":"x","n":1},{"code":"y","n":2}]}</script>
            <script type="application/json">not json</script>
            <script type="text/javascript">{"code":"z"}</script>
            <script type="application/json" data-sjs>{"b":{"code":"y","n":3}}</script>"#;
        let docs = json_scripts(page);
        assert_eq!(docs.len(), 2);
        let wanted = |code: &'static str| move |v: &Value| v["code"] == code;
        assert_eq!(find_object(&docs[0], &wanted("y")).unwrap()["n"], 2);
        assert_eq!(find_object(&docs[1], &wanted("y")).unwrap()["n"], 3);
        assert!(find_object(&docs[0], &wanted("z")).is_none());
    }

    #[test]
    fn javascript_only_values_inside_a_state_read_as_null() {
        let page = r#"s={"a":new Date("2020-01-01"),"b":new Map([["k)",new Set([1])]]),"c":undefined,"d":NaN,"e":[Infinity,"new X(1)"]};"#;
        let value = json_after(page, "s=").unwrap();
        assert_eq!(
            value,
            serde_json::json!({"a":null,"b":null,"c":null,"d":null,"e":[null,"new X(1)"]})
        );
        assert_eq!(json_after("s={\"a\":new Date(\"x\"", "s="), None);
        assert_eq!(json_after("s={\"a\":new (1)}", "s="), None);
    }

    #[test]
    fn a_state_that_cannot_be_read_is_explained_without_quoting_the_page() {
        let explain = |page: &str| explain_json_after(page, "a =");
        assert_eq!(explain("nothing"), "marker absent");
        assert_eq!(explain("a = 5"), "no value after the marker");
        assert!(
            explain("a = JSON.parse({\"x\":1})").contains("does not follow"),
            "{}",
            explain("a = JSON.parse({})")
        );
        assert_eq!(explain("a = {\"x\":1}"), "parses");
        let broken = explain("a = {\"x\":bad}");
        assert!(broken.contains("bad"), "{broken}");
        assert!(explain("a = {\"x\":[1,").contains("balanced"));
    }

    #[test]
    fn an_unterminated_or_missing_value_is_none() {
        assert_eq!(json_after("a = {\"x\": [1,", "a ="), None);
        assert_eq!(json_after("nothing here", "a ="), None);
    }

    #[test]
    fn a_json_script_element_is_read_by_id() {
        let page = r#"<html><body><script id="__UNIVERSAL_DATA_FOR_REHYDRATION__" type="application/json">{"__DEFAULT_SCOPE__":{"k":1}}</script></body></html>"#;
        assert_eq!(
            script_json(page, "__UNIVERSAL_DATA_FOR_REHYDRATION__"),
            Some(json!({"__DEFAULT_SCOPE__": {"k": 1}}))
        );
        assert_eq!(script_json(page, "absent"), None);
    }

    #[test]
    fn a_percent_encoded_script_document_is_decoded_before_it_is_parsed() {
        let page = r#"<script id="__RENDER_DATA__" type="application/json">%7B%22access_id%22%3A%22a.b%22%2C%22n%22%3A%22%E4%B8%AD%22%7D</script>"#;
        assert_eq!(
            script_json_encoded(page, "__RENDER_DATA__"),
            Some(json!({"access_id": "a.b", "n": "中"}))
        );
        assert_eq!(script_json_encoded(page, "absent"), None);
        let broken = r#"<script id="x">%7B%22a%22</script>"#;
        assert_eq!(script_json_encoded(broken, "x"), None);
    }
}
