//! YouTube links: which channel or work a link, a handle or an id names.
use trace_site_support::kit::link_parts;

/// Hosts whose links are YouTube's (`youtu.be` is the short form of watch links).
pub(crate) const HOSTS: [&str; 2] = ["youtube.com", "youtu.be"];
/// Segments after a channel that name one of its tabs, not another channel.
const TABS: [&str; 8] = [
    "videos",
    "shorts",
    "posts",
    "community",
    "streams",
    "featured",
    "about",
    "playlists",
];

/// How a channel link names the channel.
#[derive(Debug, PartialEq, Eq)]
pub(crate) enum Channel {
    /// `UC` and 22 characters: the identity, which never changes.
    Id(String),
    /// `@handle`, without the `@`.
    Handle(String),
    /// The older forms: `c/name` and `user/name`.
    Legacy(&'static str, String),
}

#[derive(Debug, PartialEq, Eq)]
pub(crate) enum Target {
    Channel(Channel),
    Video(String),
    Short(String),
    Post(String),
}

/// An eleven-character video id.
pub(crate) fn is_video_id(id: &str) -> bool {
    id.len() == 11
        && id
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_')
}

/// `UC` and 22 more characters.
pub(crate) fn is_channel_id(id: &str) -> bool {
    id.len() == 24
        && id.starts_with("UC")
        && id
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_')
}

/// A community post id (`Ug...`).
pub(crate) fn is_post_id(id: &str) -> bool {
    id.starts_with("Ug")
        && (20..=80).contains(&id.len())
        && id
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_')
}

fn handle_ok(handle: &str) -> bool {
    !handle.is_empty() && handle.chars().count() <= 100 && !handle.contains(['/', '?', '#', ' '])
}

fn channel_of(parts: &[String]) -> Option<Channel> {
    let first = parts.first()?;
    let rest_is_tab = |n: usize| parts.get(n).is_none_or(|t| TABS.contains(&t.as_str()));
    if let Some(handle) = first.strip_prefix('@') {
        return (handle_ok(handle) && rest_is_tab(1) && parts.len() <= 2)
            .then(|| Channel::Handle(handle.to_owned()));
    }
    let second = parts.get(1)?;
    if !rest_is_tab(2) || parts.len() > 3 {
        return None;
    }
    match first.as_str() {
        "channel" if is_channel_id(second) => Some(Channel::Id(second.clone())),
        "c" if handle_ok(second) => Some(Channel::Legacy("c", second.clone())),
        "user" if handle_ok(second) => Some(Channel::Legacy("user", second.clone())),
        _ => None,
    }
}

/// What a pasted link points at, if it is a link of the site that this adapter understands.
pub(crate) fn parse(input: &str) -> Option<Target> {
    let (host, parts) = link_parts(input, &HOSTS)?;
    let url = url::Url::parse(input.trim()).ok()?;
    if host == "youtu.be" || host.ends_with(".youtu.be") {
        let id = parts.first()?;
        return (parts.len() == 1 && is_video_id(id)).then(|| Target::Video(id.clone()));
    }
    let first = parts.first().map(String::as_str);
    match first {
        Some("watch") => {
            let id = url.query_pairs().find(|(k, _)| k == "v")?.1.into_owned();
            is_video_id(&id).then_some(Target::Video(id))
        }
        Some("shorts") => parts
            .get(1)
            .filter(|i| is_video_id(i))
            .map(|i| Target::Short(i.clone())),
        Some("live" | "embed" | "v") => parts
            .get(1)
            .filter(|i| is_video_id(i))
            .map(|i| Target::Video(i.clone())),
        Some("post") => parts
            .get(1)
            .filter(|i| is_post_id(i))
            .map(|i| Target::Post(i.clone())),
        _ => channel_of(&parts).map(Target::Channel),
    }
}

/// What the user typed to add a channel: a link, `@handle` or a channel id.
pub(crate) fn parse_channel(input: &str) -> Option<Channel> {
    let text = input.trim();
    if is_channel_id(text) {
        return Some(Channel::Id(text.to_owned()));
    }
    if let Some(handle) = text.strip_prefix('@') {
        return handle_ok(handle).then(|| Channel::Handle(handle.to_owned()));
    }
    match parse(text)? {
        Target::Channel(c) => Some(c),
        _ => None,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const ID: &str = "UCabcdefghijklmnopqrstuv";

    #[test]
    fn channel_links_in_every_form_name_the_channel() {
        let handle = |h: &str| Some(Target::Channel(Channel::Handle(h.into())));
        assert_eq!(
            parse("https://www.youtube.com/@SomeName"),
            handle("SomeName")
        );
        assert_eq!(
            parse("https://youtube.com/@SomeName/shorts"),
            handle("SomeName")
        );
        assert_eq!(
            parse("https://www.youtube.com/@%E4%B8%AD%E6%96%87/videos"),
            handle("中文")
        );
        assert_eq!(
            parse(&format!("https://www.youtube.com/channel/{ID}/posts")),
            Some(Target::Channel(Channel::Id(ID.into())))
        );
        assert_eq!(
            parse("https://www.youtube.com/c/OldName"),
            Some(Target::Channel(Channel::Legacy("c", "OldName".into())))
        );
        assert_eq!(
            parse("https://www.youtube.com/user/oldUser/videos"),
            Some(Target::Channel(Channel::Legacy("user", "oldUser".into())))
        );
        assert_eq!(parse("https://www.youtube.com/channel/UCshort"), None);
        assert_eq!(parse("https://www.youtube.com/@a/unknowntab"), None);
    }

    #[test]
    fn work_links_name_the_video_the_short_or_the_post() {
        let video = |id: &str| Some(Target::Video(id.into()));
        assert_eq!(
            parse("https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=5"),
            video("dQw4w9WgXcQ")
        );
        assert_eq!(
            parse("https://m.youtube.com/watch?v=dQw4w9WgXcQ"),
            video("dQw4w9WgXcQ")
        );
        assert_eq!(
            parse("https://youtu.be/dQw4w9WgXcQ?si=x"),
            video("dQw4w9WgXcQ")
        );
        assert_eq!(
            parse("https://www.youtube.com/live/dQw4w9WgXcQ"),
            video("dQw4w9WgXcQ")
        );
        assert_eq!(
            parse("https://www.youtube.com/shorts/abc-DEF_123"),
            Some(Target::Short("abc-DEF_123".into()))
        );
        let post = "UgkxAbCdEfGhIjKlMnOpQrStUvWxYz0123456789";
        assert_eq!(
            parse(&format!("https://www.youtube.com/post/{post}")),
            Some(Target::Post(post.into()))
        );
        assert_eq!(parse("https://www.youtube.com/watch?v=short"), None);
        assert_eq!(parse("http://www.youtube.com/watch?v=dQw4w9WgXcQ"), None);
        assert_eq!(
            parse("https://youtube.com.evil.example/watch?v=dQw4w9WgXcQ"),
            None
        );
    }

    #[test]
    fn typed_input_may_be_a_handle_an_id_or_a_link() {
        assert_eq!(parse_channel("@Name"), Some(Channel::Handle("Name".into())));
        assert_eq!(parse_channel(ID), Some(Channel::Id(ID.into())));
        assert_eq!(
            parse_channel("https://www.youtube.com/@Name"),
            Some(Channel::Handle("Name".into()))
        );
        assert_eq!(
            parse_channel("https://www.youtube.com/watch?v=dQw4w9WgXcQ"),
            None
        );
        assert_eq!(parse_channel("@"), None);
    }
}
