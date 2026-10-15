//! X links: which account or post a link, a name or an id names.
use trace_site_support::kit::{link_parts, numeric};

/// Hosts whose links are X's (the old name stays valid).
pub(crate) const HOSTS: [&str; 3] = ["x.com", "twitter.com", "t.co"];
/// First path segments that are pages of the site, not accounts.
const RESERVED: [&str; 24] = [
    "home",
    "explore",
    "search",
    "notifications",
    "messages",
    "i",
    "settings",
    "login",
    "logout",
    "signup",
    "tos",
    "privacy",
    "compose",
    "hashtag",
    "intent",
    "share",
    "jobs",
    "about",
    "help",
    "download",
    "account",
    "oauth",
    "flow",
    "communities",
];
/// Pages of an account that still name the account.
const ACCOUNT_PAGES: [&str; 5] = ["with_replies", "media", "highlights", "articles", "likes"];

#[derive(Debug, PartialEq, Eq)]
pub(crate) enum Target {
    /// `screen_name`, without the `@`. It can change; the adapter resolves it to `rest_id`.
    Account(String),
    Post(String),
}

/// 1 to 15 letters, digits and underscores.
pub(crate) fn screen_name_ok(name: &str) -> bool {
    (1..=15).contains(&name.len())
        && name.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'_')
        && !RESERVED.contains(&name.to_ascii_lowercase().as_str())
}

pub(crate) fn is_short(input: &str) -> bool {
    link_parts(input, &HOSTS)
        .is_some_and(|(host, parts)| host == "t.co" && parts.len() == 1 && !parts[0].is_empty())
}

pub(crate) fn parse(input: &str) -> Option<Target> {
    let (host, parts) = link_parts(input, &HOSTS)?;
    if host == "t.co" {
        return None;
    }
    match parts.as_slice() {
        [i, status, id] if i == "i" && status == "status" && numeric(id, 24) => {
            Some(Target::Post(id.clone()))
        }
        [name, status, id, ..] if status == "status" && numeric(id, 24) && screen_name_ok(name) => {
            Some(Target::Post(id.clone()))
        }
        [name] if screen_name_ok(name) => Some(Target::Account(name.clone())),
        [name, page] if screen_name_ok(name) && ACCOUNT_PAGES.contains(&page.as_str()) => {
            Some(Target::Account(name.clone()))
        }
        _ => None,
    }
}

/// What the user typed to add an account: a link, `@name` or the bare name.
pub(crate) fn parse_account(input: &str) -> Option<String> {
    let text = input.trim();
    let bare = text.strip_prefix('@').unwrap_or(text);
    if screen_name_ok(bare) {
        return Some(bare.to_owned());
    }
    match parse(text)? {
        Target::Account(name) => Some(name),
        Target::Post(_) => None,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn accounts_and_posts_are_told_apart_and_site_pages_are_not_accounts() {
        let account = |n: &str| Some(Target::Account(n.into()));
        assert_eq!(
            parse("https://x.com/sample_handle1"),
            account("sample_handle1")
        );
        assert_eq!(
            parse("https://twitter.com/Some_Name/media"),
            account("Some_Name")
        );
        assert_eq!(
            parse("https://mobile.twitter.com/a/with_replies"),
            account("a")
        );
        assert_eq!(
            parse("https://x.com/a/status/1700000000000000001?s=20"),
            Some(Target::Post("1700000000000000001".into()))
        );
        assert_eq!(
            parse("https://x.com/a/status/1700000000000000001/photo/1"),
            Some(Target::Post("1700000000000000001".into()))
        );
        assert_eq!(
            parse("https://x.com/i/status/1700000000000000001"),
            Some(Target::Post("1700000000000000001".into()))
        );
        assert_eq!(parse("https://x.com/home"), None);
        assert_eq!(parse("https://x.com/i/communities/1"), None);
        assert_eq!(parse("https://x.com/a/status/abc"), None);
        assert_eq!(parse("https://x.com/toolongnameforanaccount"), None);
        assert_eq!(parse("http://x.com/a"), None);
        assert_eq!(parse("https://x.com.evil.example/a"), None);
    }

    #[test]
    fn typed_input_may_be_a_name_with_or_without_the_at_sign_or_a_link() {
        assert_eq!(parse_account("@mama_mia"), Some("mama_mia".into()));
        assert_eq!(parse_account("mama_mia"), Some("mama_mia".into()));
        assert_eq!(
            parse_account("https://x.com/mama_mia"),
            Some("mama_mia".into())
        );
        assert_eq!(parse_account("https://x.com/a/status/1"), None);
        assert_eq!(parse_account("home"), None);
        assert_eq!(parse_account("a b"), None);
    }

    #[test]
    fn short_links_are_recognized_before_they_are_expanded() {
        assert!(is_short("https://t.co/AbCdEf1234"));
        assert!(!is_short("https://x.com/a"));
        assert!(!is_short("https://t.co/"));
    }
}
