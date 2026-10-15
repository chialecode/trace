//! TikTok links: which account or work a link names.
use trace_site_support::kit::{link_parts, numeric};

/// Hosts whose links are TikTok's (the short-link hosts are subdomains of it).
pub(crate) const HOSTS: [&str; 1] = ["tiktok.com"];

#[derive(Debug, PartialEq, Eq)]
pub(crate) enum Target {
    /// `@uniqueId`, without the `@`. The name can change; the adapter resolves it to `secUid`.
    Account(String),
    Video {
        user: String,
        id: String,
    },
    Photo {
        user: String,
        id: String,
    },
}

fn unique_id_ok(id: &str) -> bool {
    (1..=64).contains(&id.len())
        && id
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'.' || b == b'_')
}

/// A short link: the hosts `vm.` and `vt.` and the `/t/<code>` path of the main host.
pub(crate) fn is_short(input: &str) -> bool {
    let Some((host, parts)) = link_parts(input, &HOSTS) else {
        return false;
    };
    let code_ok =
        |c: &String| (4..=32).contains(&c.len()) && c.bytes().all(|b| b.is_ascii_alphanumeric());
    match host.as_str() {
        "vm.tiktok.com" | "vt.tiktok.com" => parts.len() == 1 && code_ok(&parts[0]),
        _ => parts.len() == 2 && parts[0] == "t" && code_ok(&parts[1]),
    }
}

pub(crate) fn parse(input: &str) -> Option<Target> {
    let (_, parts) = link_parts(input, &HOSTS)?;
    let name = parts.first()?.strip_prefix('@')?;
    if !unique_id_ok(name) {
        return None;
    }
    match parts.as_slice() {
        [_] => Some(Target::Account(name.to_owned())),
        [_, kind, id] if numeric(id, 24) => match kind.as_str() {
            "video" => Some(Target::Video {
                user: name.to_owned(),
                id: id.clone(),
            }),
            "photo" => Some(Target::Photo {
                user: name.to_owned(),
                id: id.clone(),
            }),
            _ => None,
        },
        _ => None,
    }
}

/// What the user typed to add an account: a link, or `@uniqueId`.
pub(crate) fn parse_account(input: &str) -> Option<String> {
    let text = input.trim();
    if let Some(name) = text.strip_prefix('@') {
        return unique_id_ok(name).then(|| name.to_owned());
    }
    match parse(text)? {
        Target::Account(name) => Some(name),
        _ => None,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn account_and_work_links_are_told_apart_and_names_are_checked() {
        assert_eq!(
            parse("https://www.tiktok.com/@zach.king?lang=en"),
            Some(Target::Account("zach.king".into()))
        );
        assert_eq!(
            parse("https://www.tiktok.com/@a_b/video/7000000000000000001"),
            Some(Target::Video {
                user: "a_b".into(),
                id: "7000000000000000001".into()
            })
        );
        assert_eq!(
            parse("https://tiktok.com/@a/photo/7000000000000000002"),
            Some(Target::Photo {
                user: "a".into(),
                id: "7000000000000000002".into()
            })
        );
        assert_eq!(parse("https://www.tiktok.com/@a/video/abc"), None);
        assert_eq!(parse("https://www.tiktok.com/@a/live"), None);
        assert_eq!(parse("https://www.tiktok.com/discover"), None);
        assert_eq!(parse("https://www.tiktok.com/@bad name"), None);
        assert_eq!(parse("http://www.tiktok.com/@a"), None);
        assert_eq!(parse("https://tiktok.com.evil.example/@a"), None);
    }

    #[test]
    fn short_links_are_recognized_before_they_are_expanded() {
        assert!(is_short("https://vm.tiktok.com/ZMabc123/"));
        assert!(is_short("https://vt.tiktok.com/ZSabc123"));
        assert!(is_short("https://www.tiktok.com/t/ZTabc123/"));
        assert!(!is_short("https://www.tiktok.com/@a"));
        assert!(!is_short("https://example.com/t/ZTabc123"));
        assert!(!is_short("https://vm.tiktok.com/"));
    }

    #[test]
    fn typed_input_may_be_a_handle_or_a_link() {
        assert_eq!(parse_account("@zach.king"), Some("zach.king".into()));
        assert_eq!(
            parse_account("https://www.tiktok.com/@zach"),
            Some("zach".into())
        );
        assert_eq!(parse_account("@"), None);
        assert_eq!(parse_account("zach"), None);
        assert_eq!(
            parse_account("https://www.tiktok.com/@a/video/7000000000000000001"),
            None
        );
    }
}
