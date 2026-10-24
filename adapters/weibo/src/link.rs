//! Weibo links: which account or post a link names, and the short id ↔ numeric id mapping.
use trace_site_support::kit::{link_parts, numeric};

const ALPHABET: &[u8; 62] = b"0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ";
/// Hosts whose links are Weibo's (the desktop site, the mobile site and its API host).
pub(crate) const HOSTS: [&str; 2] = ["weibo.com", "weibo.cn"];
/// First path segments that are pages of the site, not account names.
const RESERVED: [&str; 18] = [
    "u",
    "n",
    "detail",
    "status",
    "ajax",
    "login",
    "signup",
    "hot",
    "search",
    "explore",
    "home",
    "at",
    "tv",
    "p",
    "ttarticle",
    "newlogin",
    "sinaurl",
    "profile",
];

/// How an account link names the account.
#[derive(Debug, PartialEq, Eq)]
pub(crate) enum Account {
    /// The numeric id, which never changes.
    Uid(String),
    /// A personalized domain (`weibo.com/<name>`).
    Custom(String),
    /// The display name (`weibo.com/n/<name>`).
    Nick(String),
}

#[derive(Debug, PartialEq, Eq)]
pub(crate) enum Target {
    Account(Account),
    /// A post by its numeric id.
    Post(String),
}

fn base62(text: &str) -> Option<u64> {
    text.bytes().try_fold(0u64, |acc, b| {
        let digit = ALPHABET.iter().position(|a| *a == b)? as u64;
        acc.checked_mul(62)?.checked_add(digit)
    })
}

/// The short id in a post link (`Aj8Dnpj3b`) as the post's numeric id: groups of four
/// characters from the right are base 62, each giving seven digits except the first.
pub(crate) fn mblogid_to_mid(short: &str) -> Option<String> {
    if short.is_empty() || short.len() > 12 || !short.bytes().all(|b| b.is_ascii_alphanumeric()) {
        return None;
    }
    let bytes = short.as_bytes();
    let mut groups = Vec::new();
    let mut end = bytes.len();
    while end > 0 {
        let start = end.saturating_sub(4);
        groups.push(std::str::from_utf8(&bytes[start..end]).ok()?);
        end = start;
    }
    let mut out = String::new();
    for (i, group) in groups.iter().rev().enumerate() {
        let n = base62(group)?;
        if i == 0 {
            out.push_str(&n.to_string());
        } else {
            out.push_str(&format!("{n:07}"));
        }
    }
    Some(out)
}

/// The inverse of [`mblogid_to_mid`].
pub(crate) fn mid_to_mblogid(mid: &str) -> Option<String> {
    if !numeric(mid, 20) {
        return None;
    }
    let mut chunks = Vec::new();
    let mut end = mid.len();
    while end > 0 {
        let start = end.saturating_sub(7);
        chunks.push(&mid[start..end]);
        end = start;
    }
    let mut out = String::new();
    for (i, chunk) in chunks.iter().rev().enumerate() {
        let mut n: u64 = chunk.parse().ok()?;
        let mut digits = Vec::new();
        while n > 0 {
            digits.push(ALPHABET[(n % 62) as usize]);
            n /= 62;
        }
        digits.reverse();
        let mut text = String::from_utf8(digits).ok()?;
        if i > 0 {
            text = format!("{text:0>4}");
        }
        out.push_str(&text);
    }
    Some(out)
}

fn post_id(token: &str) -> Option<String> {
    if numeric(token, 20) && token.len() >= 12 {
        Some(token.to_owned())
    } else {
        mblogid_to_mid(token)
    }
}

fn user_id(token: &str) -> Option<Target> {
    (numeric(token, 12) && token.len() >= 5)
        .then(|| Target::Account(Account::Uid(token.to_owned())))
}

/// Recognizes a link (or a bare user id) of the site.
pub(crate) fn parse(input: &str) -> Option<Target> {
    let input = input.trim();
    if numeric(input, 12) {
        return user_id(input);
    }
    let (_, parts) = link_parts(input, &HOSTS)?;
    let parts: Vec<&str> = parts.iter().map(String::as_str).collect();
    match parts.as_slice() {
        ["u" | "profile", uid] => user_id(uid),
        ["n", nick] => Some(Target::Account(Account::Nick((*nick).to_owned()))),
        ["detail" | "status", id] => post_id(id).map(Target::Post),
        [first, second] if numeric(first, 12) && !RESERVED.contains(first) => {
            post_id(second).map(Target::Post)
        }
        [one] if numeric(one, 12) => user_id(one),
        [one]
            if !RESERVED.contains(one) && one.chars().all(|c| c.is_alphanumeric() || c == '_') =>
        {
            Some(Target::Account(Account::Custom((*one).to_owned())))
        }
        _ => None,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_short_id_and_the_numeric_id_convert_both_ways() {
        // 4 characters = one group; 9 characters = 1 + 4 + 4.
        assert_eq!(mblogid_to_mid("0001").as_deref(), Some("1"));
        assert_eq!(mblogid_to_mid("1000").as_deref(), Some("238328"));
        assert_eq!(
            mblogid_to_mid("a00000001").as_deref(),
            Some("1000000000000001")
        );
        for mid in ["4906123456789012", "4913871234567890", "5012345000000001"] {
            let short = mid_to_mblogid(mid).unwrap();
            assert_eq!(mblogid_to_mid(&short).as_deref(), Some(mid), "{short}");
        }
        assert_eq!(mblogid_to_mid("bad id"), None);
        assert_eq!(mblogid_to_mid(""), None);
        assert_eq!(mid_to_mblogid("12x"), None);
    }

    #[test]
    fn accounts_are_told_by_their_link_form() {
        let uid = |s: &str| Some(Target::Account(Account::Uid(s.into())));
        assert_eq!(parse("https://weibo.com/u/1234567890"), uid("1234567890"));
        assert_eq!(parse("https://m.weibo.cn/u/1234567890"), uid("1234567890"));
        assert_eq!(
            parse("https://m.weibo.cn/profile/1234567890?x=1"),
            uid("1234567890")
        );
        assert_eq!(parse("https://weibo.com/1234567890"), uid("1234567890"));
        assert_eq!(parse("1234567890"), uid("1234567890"));
        assert_eq!(
            parse("https://weibo.com/n/某个昵称"),
            Some(Target::Account(Account::Nick("某个昵称".into())))
        );
        assert_eq!(
            parse("https://weibo.com/mycustom"),
            Some(Target::Account(Account::Custom("mycustom".into())))
        );
        assert_eq!(parse("https://weibo.com/ajax"), None);
        assert_eq!(parse("http://weibo.com/u/1234567890"), None);
        assert_eq!(parse("https://example.com/u/1234567890"), None);
    }

    #[test]
    fn posts_are_told_by_their_link_form() {
        let post = |s: &str| Some(Target::Post(s.into()));
        assert_eq!(
            parse("https://m.weibo.cn/detail/4906123456789012"),
            post("4906123456789012")
        );
        assert_eq!(
            parse("https://m.weibo.cn/status/4906123456789012"),
            post("4906123456789012")
        );
        let short = mid_to_mblogid("4906123456789012").unwrap();
        assert_eq!(
            parse(&format!("https://weibo.com/1234567890/{short}")),
            post("4906123456789012")
        );
        assert_eq!(
            parse(&format!("https://weibo.com/detail/{short}")),
            post("4906123456789012")
        );
        assert_eq!(parse("https://weibo.com/1234567890/not%20an%20id"), None);
    }
}
