//! The session Cookies a site sets in its own responses, kept in memory for the media requests
//! of the sites whose video addresses only answer the Cookies of the page that produced them
//! (`Site::session_media`).
//!
//! A jar entry never leaves the process: it is not stored, not given to an adapter and not sent
//! anywhere but to the site's Cookie domain, which the transport enforces for every request. It
//! is kept per credential scope, the same key the credential's own Cookie is cached under, so
//! one account's session never reaches another's, and it is dropped with the credential.
use std::collections::HashMap;
use std::sync::Mutex;

/// Cookies kept per scope, scopes kept, and the longest value: a site's session is a handful of
/// short tokens, so more than this is not one.
const COOKIES_MAX: usize = 64;
const SCOPES_MAX: usize = 64;
const VALUE_MAX: usize = 4096;
/// Set-Cookie lines read from one response.
pub const SET_COOKIES_MAX: usize = 32;

/// The Set-Cookie lines a response carried, as the transport read them. They are the site's
/// session, so they are never printed.
#[derive(Clone, Default)]
pub struct SetCookies(pub Vec<String>);

impl std::fmt::Debug for SetCookies {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "SetCookies({} lines, redacted)", self.0.len())
    }
}

#[derive(Default)]
pub struct SessionJar {
    scopes: Mutex<HashMap<String, Vec<(String, String)>>>,
}

impl std::fmt::Debug for SessionJar {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        let scopes = self.scopes.lock().map(|s| s.len()).unwrap_or(0);
        write!(f, "SessionJar({scopes} scopes, values redacted)")
    }
}

/// A Set-Cookie line: the pair it sets and whether it expires the Cookie instead.
struct Parsed {
    name: String,
    value: String,
    expired: bool,
}

fn token(name: &str) -> bool {
    !name.is_empty()
        && name.len() <= 128
        && name
            .bytes()
            .all(|b| b.is_ascii_graphic() && !matches!(b, b'=' | b';' | b',' | b'"' | b'(' | b')'))
}

fn plain(value: &str) -> bool {
    value.len() <= VALUE_MAX
        && value
            .bytes()
            .all(|b| (b == b' ' || b.is_ascii_graphic()) && b != b';')
}

fn parse(line: &str, now: i64) -> Option<Parsed> {
    let mut parts = line.split(';');
    let (name, value) = parts.next()?.split_once('=')?;
    let (name, value) = (name.trim(), value.trim());
    if !token(name) || !plain(value) {
        return None;
    }
    let mut expired = value.is_empty();
    for attribute in parts {
        let Some((key, text)) = attribute.split_once('=') else {
            continue;
        };
        let text = text.trim();
        match key.trim().to_ascii_lowercase().as_str() {
            "max-age" => {
                if text.parse::<i64>().is_ok_and(|n| n <= 0) {
                    expired = true;
                }
            }
            "expires" => {
                let date = time::OffsetDateTime::parse(
                    text,
                    &time::format_description::well_known::Rfc2822,
                );
                if date.is_ok_and(|d| d.unix_timestamp() <= now) {
                    expired = true;
                }
            }
            _ => {}
        }
    }
    Some(Parsed {
        name: name.to_owned(),
        value: value.to_owned(),
        expired,
    })
}

fn now() -> i64 {
    time::OffsetDateTime::now_utc().unix_timestamp()
}

impl SessionJar {
    /// Takes the Set-Cookie lines of one response into the scope. A Cookie the line expires is
    /// removed; one that is set again replaces its earlier value.
    pub fn remember(&self, scope: &str, set_cookies: &[String]) {
        let at = now();
        let parsed: Vec<Parsed> = set_cookies
            .iter()
            .take(SET_COOKIES_MAX)
            .filter_map(|line| parse(line, at))
            .collect();
        if parsed.is_empty() {
            return;
        }
        let mut scopes = self.scopes.lock().unwrap();
        if !scopes.contains_key(scope)
            && scopes.len() >= SCOPES_MAX
            && let Some(stale) = scopes.keys().next().cloned()
        {
            // Scopes of credentials that were replaced are never asked for again.
            scopes.remove(&stale);
        }
        let jar = scopes.entry(scope.to_owned()).or_default();
        for cookie in parsed {
            jar.retain(|(name, _)| *name != cookie.name);
            if !cookie.expired && jar.len() < COOKIES_MAX {
                jar.push((cookie.name, cookie.value));
            }
        }
        if jar.is_empty() {
            scopes.remove(scope);
        }
    }

    /// The Cookie header of a media request: the credential's own Cookie with the session's
    /// Cookies added, a session Cookie replacing one of the same name. `None` when there is
    /// neither.
    pub fn merged(&self, scope: &str, credential: Option<&str>) -> Option<String> {
        let scopes = self.scopes.lock().unwrap();
        let session = scopes.get(scope).map(Vec::as_slice).unwrap_or_default();
        let mut pairs: Vec<(String, String)> = credential
            .into_iter()
            .flat_map(|header| header.split(';'))
            .filter_map(|pair| {
                let (name, value) = pair.split_once('=')?;
                let name = name.trim();
                (!name.is_empty() && !session.iter().any(|(n, _)| n == name))
                    .then(|| (name.to_owned(), value.trim().to_owned()))
            })
            .collect();
        pairs.extend(session.iter().cloned());
        (!pairs.is_empty()).then(|| {
            pairs
                .iter()
                .map(|(name, value)| format!("{name}={value}"))
                .collect::<Vec<_>>()
                .join("; ")
        })
    }

    /// The Cookie a media request to `url` carries, if any: the credential's with the session's
    /// added, and only when the address is on the site's Cookie domain and not on a media host.
    /// A CDN address gets none, whatever the jar holds.
    pub fn for_media(
        &self,
        rules: &crate::transport::HostRules,
        (scope, credential): (&str, Option<&str>),
        url: &str,
    ) -> Option<String> {
        let site = rules.site.as_deref().and_then(trace_domain::sites::site)?;
        let parsed = url::Url::parse(url).ok()?;
        let host = parsed.host_str()?;
        if !site.session_media
            || parsed.scheme() != "https"
            || !crate::transport::host_matches(host, &rules.cookie_hosts)
            || crate::transport::host_matches(host, &rules.media_hosts)
        {
            return None;
        }
        self.merged(scope, credential)
    }

    /// Drops the sessions of a platform and of every credential scope under it.
    pub fn forget(&self, platform: &str) {
        let nested = format!("{platform}/");
        self.scopes
            .lock()
            .unwrap()
            .retain(|scope, _| scope != platform && !scope.starts_with(&nested));
    }

    #[cfg(test)]
    pub fn is_empty(&self) -> bool {
        self.scopes.lock().unwrap().is_empty()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn lines(list: &[&str]) -> Vec<String> {
        list.iter().map(|l| (*l).to_owned()).collect()
    }

    #[test]
    fn the_session_adds_to_the_credential_and_wins_a_name_they_share() {
        let jar = SessionJar::default();
        jar.remember(
            "tiktok/c1/g1",
            &lines(&[
                "ttwid=T1; Path=/; Domain=.tiktok.com; HttpOnly; Secure",
                "tt_chain_token=abc==; Path=/; Max-Age=3600",
                "sessionid=server-rotated; Path=/",
            ]),
        );
        assert_eq!(
            jar.merged("tiktok/c1/g1", Some("sessionid=mine; msToken=m")),
            Some("msToken=m; ttwid=T1; tt_chain_token=abc==; sessionid=server-rotated".to_owned())
        );
        assert_eq!(
            jar.merged("tiktok/c1/g1", None).as_deref(),
            Some("ttwid=T1; tt_chain_token=abc==; sessionid=server-rotated")
        );
        assert_eq!(
            jar.merged("tiktok/other/g1", Some("a=1")).as_deref(),
            Some("a=1"),
            "another scope's session is not this one's"
        );
        assert_eq!(jar.merged("tiktok", None), None);
    }

    #[test]
    fn a_cookie_the_site_expires_is_removed_and_one_it_sets_again_is_replaced() {
        let jar = SessionJar::default();
        jar.remember("tiktok", &lines(&["a=1", "b=2", "c=3"]));
        jar.remember(
            "tiktok",
            &lines(&[
                "a=9",
                "b=; Path=/",
                "c=3; Max-Age=0",
                "d=4; Expires=Thu, 01 Jan 1970 00:00:00 GMT",
                "e=5; Expires=Fri, 01 Jan 2100 00:00:00 GMT",
            ]),
        );
        assert_eq!(jar.merged("tiktok", None).as_deref(), Some("a=9; e=5"));
        jar.remember("tiktok", &lines(&["a=; Max-Age=-1", "e=; Max-Age=0"]));
        assert!(jar.is_empty(), "a jar with nothing left holds no scope");
    }

    #[test]
    fn what_is_not_a_cookie_pair_is_ignored_and_the_jar_is_bounded() {
        let jar = SessionJar::default();
        jar.remember(
            "tiktok",
            &lines(&[
                "",
                "no-equals-sign",
                "=novalue",
                "bad name=1",
                "ctl=a\u{1}b",
                &format!("long={}", "x".repeat(VALUE_MAX + 1)),
                "ok=1",
            ]),
        );
        assert_eq!(jar.merged("tiktok", None).as_deref(), Some("ok=1"));
        let many: Vec<String> = (0..200).map(|n| format!("k{n}=v")).collect();
        let other = SessionJar::default();
        other.remember("tiktok", &many);
        let kept = other.merged("tiktok", None).unwrap();
        assert_eq!(
            kept.split("; ").count(),
            SET_COOKIES_MAX,
            "only the first lines of a response are read"
        );
        let scopes = SessionJar::default();
        for n in 0..(SCOPES_MAX + 10) {
            scopes.remember(&format!("tiktok/c{n}/g1"), &lines(&["a=1"]));
        }
        assert_eq!(scopes.scopes.lock().unwrap().len(), SCOPES_MAX);
    }

    #[test]
    fn forgetting_a_platform_drops_every_scope_under_it_and_no_other() {
        let jar = SessionJar::default();
        for scope in [
            "tiktok",
            "tiktok/c1/g1",
            "tiktok/c2/g3",
            "tiktokish",
            "x/c1/g1",
        ] {
            jar.remember(scope, &lines(&["a=1"]));
        }
        jar.forget("tiktok");
        assert_eq!(jar.merged("tiktok", None), None);
        assert_eq!(jar.merged("tiktok/c1/g1", None), None);
        assert_eq!(jar.merged("tiktok/c2/g3", None), None);
        assert_eq!(jar.merged("tiktokish", None).as_deref(), Some("a=1"));
        assert_eq!(jar.merged("x/c1/g1", None).as_deref(), Some("a=1"));
    }

    #[test]
    fn only_the_sites_own_video_address_asks_for_the_session() {
        use crate::config::Profile;
        use crate::transport::{host_rules, platform_rules};
        let jar = SessionJar::default();
        jar.remember("tiktok", &lines(&["ttwid=T"]));
        jar.remember("weibo", &lines(&["a=1"]));
        let all = host_rules(&Profile::default());
        let tiktok = platform_rules(&all, "tiktok");
        let ask = |url: &str| jar.for_media(&tiktok, ("tiktok", Some("sessionid=s")), url);
        for url in [
            "https://www.tiktok.com/@a/video/1",
            "https://v16-webapp-prime.tiktok.com/video/tos/x.mp4",
        ] {
            assert_eq!(ask(url).as_deref(), Some("sessionid=s; ttwid=T"), "{url}");
        }
        for url in [
            "https://v16m.tiktokcdn.com/x.mp4",
            "https://sf16-sg.tiktokcdn-us.com/x.mp4",
            "https://v19.tiktokv.com/x.mp4",
            "https://p16.ibytedtos.com/x.mp4",
            "http://www.tiktok.com/x.mp4",
            "https://tiktok.com.evil.example/x.mp4",
            "not a url",
        ] {
            assert_eq!(ask(url), None, "{url}");
        }
        // A site that has no use for a session never sends one, whatever a scope holds.
        let weibo = platform_rules(&all, "weibo");
        assert_eq!(
            jar.for_media(&weibo, ("weibo", Some("c=1")), "https://weibo.com/x.mp4"),
            None
        );
        assert_eq!(
            jar.for_media(&all, ("tiktok", None), "https://www.tiktok.com/x.mp4"),
            None,
            "rules that name no site name no session"
        );
    }

    #[test]
    fn the_values_are_never_printed() {
        let jar = SessionJar::default();
        jar.remember("tiktok", &lines(&["secret=value-that-must-not-appear"]));
        assert!(!format!("{jar:?}").contains("must-not-appear"));
    }
}
