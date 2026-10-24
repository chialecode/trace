//! Instagram links: which account or post a link, a name or a code names.
use trace_site_support::kit::link_parts;

/// Hosts whose links are Instagram's.
pub(crate) const HOSTS: [&str; 1] = ["instagram.com"];
/// First path segments that are pages of the site, not accounts.
const RESERVED: [&str; 26] = [
    "p",
    "reel",
    "reels",
    "tv",
    "explore",
    "accounts",
    "direct",
    "stories",
    "about",
    "legal",
    "developer",
    "web",
    "api",
    "graphql",
    "challenge",
    "oauth",
    "emails",
    "session",
    "privacy",
    "terms",
    "directory",
    "locations",
    "topics",
    "download",
    "login",
    "nametag",
];
/// Path words in front of a post's code.
const POST_KINDS: [&str; 4] = ["p", "reel", "reels", "tv"];

#[derive(Debug, PartialEq, Eq)]
pub(crate) enum Target {
    /// The username, without the `@`. It can change; the adapter resolves it to the profile id.
    Account(String),
    /// The short code in a post's address.
    Post(String),
}

/// 1 to 30 letters, digits, periods and underscores.
pub(crate) fn username_ok(name: &str) -> bool {
    (1..=30).contains(&name.len())
        && name
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'.' || b == b'_')
        && !RESERVED.contains(&name.to_ascii_lowercase().as_str())
}

/// A post's short code: the base-64 form of its id.
pub(crate) fn code_ok(code: &str) -> bool {
    (5..=40).contains(&code.len())
        && code
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'_' || b == b'-')
}

pub(crate) fn parse(input: &str) -> Option<Target> {
    let (_, parts) = link_parts(input, &HOSTS)?;
    match parts.as_slice() {
        [kind, code] if POST_KINDS.contains(&kind.as_str()) && code_ok(code) => {
            Some(Target::Post(code.clone()))
        }
        [name, kind, code]
            if username_ok(name) && POST_KINDS.contains(&kind.as_str()) && code_ok(code) =>
        {
            Some(Target::Post(code.clone()))
        }
        [name] if username_ok(name) => Some(Target::Account(name.clone())),
        [name, page] if username_ok(name) && page == "reels" => Some(Target::Account(name.clone())),
        _ => None,
    }
}

/// What the user typed to add an account: a link, `@name` or the bare name.
pub(crate) fn parse_account(input: &str) -> Option<String> {
    let text = input.trim();
    let bare = text.strip_prefix('@').unwrap_or(text);
    if username_ok(bare) {
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
        let account = |s: &str| Some(Target::Account(s.to_owned()));
        let post = |s: &str| Some(Target::Post(s.to_owned()));
        assert_eq!(
            parse("https://www.instagram.com/synthetic.user/"),
            account("synthetic.user")
        );
        assert_eq!(parse("https://instagram.com/a_b?hl=en"), account("a_b"));
        assert_eq!(
            parse("https://www.instagram.com/a_b/reels/"),
            account("a_b")
        );
        assert_eq!(
            parse("https://www.instagram.com/p/CxYz_-12345/"),
            post("CxYz_-12345")
        );
        assert_eq!(
            parse("https://www.instagram.com/reel/CxYz12345/?igsh=x"),
            post("CxYz12345")
        );
        assert_eq!(
            parse("https://www.instagram.com/reels/CxYz12345/"),
            post("CxYz12345")
        );
        assert_eq!(
            parse("https://www.instagram.com/tv/CxYz12345/"),
            post("CxYz12345")
        );
        assert_eq!(
            parse("https://www.instagram.com/someone/p/CxYz12345/"),
            post("CxYz12345")
        );
        for rejected in [
            "https://www.instagram.com/explore/",
            "https://www.instagram.com/accounts/login/",
            "https://www.instagram.com/p/",
            "https://www.instagram.com/p/ab/",
            "https://www.instagram.com/stories/someone/123/",
            "https://www.instagram.com/bad name/",
            "http://www.instagram.com/someone/",
            "https://instagram.com.evil.example/someone/",
            "https://www.example.com/someone/",
        ] {
            assert_eq!(parse(rejected), None, "{rejected}");
        }
    }

    #[test]
    fn typed_input_may_be_a_name_with_or_without_the_at_sign_or_a_link() {
        for input in [
            "@synthetic.user",
            "synthetic.user",
            " https://www.instagram.com/synthetic.user/ ",
        ] {
            assert_eq!(
                parse_account(input).as_deref(),
                Some("synthetic.user"),
                "{input}"
            );
        }
        assert_eq!(
            parse_account("https://www.instagram.com/p/CxYz12345/"),
            None
        );
        assert_eq!(parse_account("explore"), None);
        assert_eq!(parse_account(""), None);
        assert_eq!(parse_account(&"a".repeat(31)), None);
    }
}
