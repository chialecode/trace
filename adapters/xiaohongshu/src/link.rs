//! Xiaohongshu links: which account or note a link or an id names.
use trace_site_support::kit::link_parts;

/// Hosts whose links are Xiaohongshu's (`xhslink.com` is the short form of share links).
pub(crate) const HOSTS: [&str; 2] = ["xiaohongshu.com", "xhslink.com"];

#[derive(Debug, PartialEq, Eq)]
pub(crate) enum Target {
    /// The user id, 24 hexadecimal characters.
    Account(String),
    Note(String),
}

/// The ids of users and notes: 24 lowercase hexadecimal characters.
pub(crate) fn id_ok(id: &str) -> bool {
    id.len() == 24 && id.bytes().all(|b| matches!(b, b'0'..=b'9' | b'a'..=b'f'))
}

/// An `xsec_token`: the signed parameter a share link carries for the note it names.
pub(crate) fn token_ok(token: &str) -> bool {
    (8..=300).contains(&token.len())
        && token.bytes().all(|b| {
            b.is_ascii_alphanumeric() || matches!(b, b'-' | b'_' | b'=' | b'+' | b'/' | b'.')
        })
}

pub(crate) fn is_short(input: &str) -> bool {
    link_parts(input, &HOSTS).is_some_and(|(host, parts)| {
        host == "xhslink.com"
            && !parts.is_empty()
            && parts.len() <= 2
            && parts.iter().all(|p| !p.is_empty())
    })
}

pub(crate) fn parse(input: &str) -> Option<Target> {
    let (host, parts) = link_parts(input, &HOSTS)?;
    if host == "xhslink.com" {
        return None;
    }
    let parts: Vec<&str> = parts.iter().map(String::as_str).collect();
    match parts.as_slice() {
        ["user", "profile", user] if id_ok(user) => Some(Target::Account((*user).to_owned())),
        ["user", "profile", _, note]
        | ["explore", note]
        | ["discovery", "item", note]
        | ["search_result", note]
            if id_ok(note) =>
        {
            Some(Target::Note((*note).to_owned()))
        }
        _ => None,
    }
}

/// The `xsec_token` a link carries, if it is well formed.
pub(crate) fn token_in(input: &str) -> Option<String> {
    let url = url::Url::parse(input.trim()).ok()?;
    url.query_pairs()
        .find(|(k, _)| k == "xsec_token")
        .map(|(_, v)| v.into_owned())
        .filter(|t| token_ok(t))
}

/// What the user typed to add an account: a profile link or the bare user id.
pub(crate) fn parse_account(input: &str) -> Option<String> {
    let text = input.trim();
    if id_ok(text) {
        return Some(text.to_owned());
    }
    match parse(text)? {
        Target::Account(id) => Some(id),
        Target::Note(_) => None,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const USER: &str = "5a1b2c3d4e5f60718293a4b5";
    const NOTE: &str = "65f0a1b2c3d4e5f607182930";

    #[test]
    fn accounts_and_notes_are_told_apart_in_every_link_form() {
        let account = |s: &str| Some(Target::Account(s.to_owned()));
        let note = |s: &str| Some(Target::Note(s.to_owned()));
        assert_eq!(
            parse(&format!(
                "https://www.xiaohongshu.com/user/profile/{USER}?xsec_token=abcdefgh1234"
            )),
            account(USER)
        );
        assert_eq!(
            parse(&format!(
                "https://www.xiaohongshu.com/explore/{NOTE}?xsec_token=abcdefgh1234&xsec_source=pc_feed"
            )),
            note(NOTE)
        );
        assert_eq!(
            parse(&format!(
                "https://www.xiaohongshu.com/discovery/item/{NOTE}"
            )),
            note(NOTE)
        );
        assert_eq!(
            parse(&format!(
                "https://www.xiaohongshu.com/user/profile/{USER}/{NOTE}"
            )),
            note(NOTE)
        );
        assert_eq!(
            parse(&format!("https://www.xiaohongshu.com/search_result/{NOTE}")),
            note(NOTE)
        );
        for rejected in [
            "https://www.xiaohongshu.com/explore",
            "https://www.xiaohongshu.com/explore/short",
            "https://www.xiaohongshu.com/explore/65F0A1B2C3D4E5F607182930",
            "https://www.xiaohongshu.com/user/profile/zz",
            "http://www.xiaohongshu.com/explore/65f0a1b2c3d4e5f607182930",
            "https://xiaohongshu.com.evil.example/explore/65f0a1b2c3d4e5f607182930",
            "https://xhslink.com/a/abcdef",
        ] {
            assert_eq!(parse(rejected), None, "{rejected}");
        }
    }

    #[test]
    fn short_links_tokens_and_typed_accounts() {
        assert!(is_short("https://xhslink.com/a/AbCdEf"));
        assert!(is_short("https://xhslink.com/AbCdEf"));
        assert!(!is_short("https://xhslink.com/"));
        assert!(!is_short("https://www.xiaohongshu.com/explore/x"));
        assert_eq!(
            token_in(&format!(
                "https://www.xiaohongshu.com/explore/{NOTE}?xsec_token=AB-cd_ef12%3D"
            ))
            .as_deref(),
            Some("AB-cd_ef12=")
        );
        assert_eq!(
            token_in("https://www.xiaohongshu.com/explore/x?xsec_token=a b"),
            None
        );
        assert_eq!(token_in("https://www.xiaohongshu.com/explore/x"), None);
        assert_eq!(parse_account(USER).as_deref(), Some(USER));
        assert_eq!(
            parse_account(&format!(
                " https://www.xiaohongshu.com/user/profile/{USER} "
            ))
            .as_deref(),
            Some(USER)
        );
        assert_eq!(
            parse_account(&format!("https://www.xiaohongshu.com/explore/{NOTE}")),
            None
        );
        assert_eq!(parse_account("nickname"), None);
    }
}
