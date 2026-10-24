//! The eight supported sites and the network domains each one may use.
//!
//! Core derives its request boundary from this table: a site's adapter and media
//! downloads may only reach the hosts listed for that site, and a Cookie is only ever
//! attached to the site's Cookie hosts, the domain the user copied the Cookie from (a subset of
//! its API hosts). Adding a site is a change to this table, its adapter
//! and its rule declarations (docs/design/multi-site-contracts.md §2).

use serde::{Deserialize, Serialize};

/// How much a site needs a signed-in account.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum LoginNeed {
    None,
    Optional,
    Required,
}

/// Whether the site is normally reachable without a proxy from the user's network.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum NetworkAdvice {
    Direct,
    ProxyRecommended,
}

/// One site: identity, Chinese name, domains and default conditions.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Site {
    /// Stable id; also the source namespace and the adapter's platform name.
    pub id: &'static str,
    pub title: &'static str,
    /// Metadata hosts (suffix match). Requests to them are allowed; only some also get a Cookie.
    pub api_hosts: &'static [&'static str],
    /// The hosts (suffix match, a subset of `api_hosts`) a Cookie may be attached to: the domain
    /// the user copied it from. The site's other API hosts only ever get Cookie-free requests.
    pub cookie_hosts: &'static [&'static str],
    /// Media and image hosts (suffix match). Never receive Cookies.
    pub media_hosts: &'static [&'static str],
    /// Hosts that only appear in links people paste (profile, mobile and share pages). The
    /// site's links are recognized there; no request goes to them and no Cookie is sent.
    pub link_hosts: &'static [&'static str],
    /// Request headers beyond [`BASE_REQUEST_HEADERS`] that this site's adapter may send.
    /// They are protocol constants of the site's web client, never credentials.
    pub request_headers: &'static [&'static str],
    /// The site hands out a video address on its own Cookie domain that only answers requests
    /// carrying the Cookies of the page that produced it. Core then remembers, in memory, the
    /// session Cookies the site sets and sends them with the credential on such a media request;
    /// a media host never gets them.
    pub session_media: bool,
    pub login: LoginNeed,
    pub network: NetworkAdvice,
}

/// Request headers every adapter may send to any of its site's hosts. A Cookie is not one of
/// them: Core attaches it, and only to the site's Cookie hosts.
pub const BASE_REQUEST_HEADERS: [&str; 6] = [
    "referer",
    "user-agent",
    "accept",
    "accept-language",
    "origin",
    "content-type",
];

pub const SITES: [Site; 8] = [
    Site {
        id: "weibo",
        title: "微博",
        api_hosts: &["weibo.com", "weibo.cn", "t.cn"],
        cookie_hosts: &["weibo.com"],
        media_hosts: &["sinaimg.cn", "weibocdn.com"],
        link_hosts: &[],
        request_headers: &["x-requested-with"],
        session_media: false,
        login: LoginNeed::Required,
        network: NetworkAdvice::Direct,
    },
    Site {
        id: "xiaohongshu",
        title: "小红书",
        api_hosts: &["xiaohongshu.com", "xhslink.com"],
        cookie_hosts: &["xiaohongshu.com"],
        media_hosts: &["xhscdn.com"],
        link_hosts: &[],
        // The public list endpoint signs each request with the visitor's device identifier;
        // these are request signatures, not credentials (DEC-041).
        request_headers: &["x-s", "x-t", "x-s-common", "x-b3-traceid", "x-xray-traceid"],
        session_media: false,
        login: LoginNeed::Optional,
        network: NetworkAdvice::Direct,
    },
    Site {
        id: "instagram",
        title: "Instagram",
        api_hosts: &["instagram.com"],
        cookie_hosts: &["instagram.com"],
        media_hosts: &["cdninstagram.com", "fbcdn.net"],
        link_hosts: &[],
        request_headers: &[
            "x-ig-app-id",
            "x-asbd-id",
            "x-fb-lsd",
            "x-fb-friendly-name",
            "x-requested-with",
        ],
        session_media: false,
        login: LoginNeed::Required,
        network: NetworkAdvice::ProxyRecommended,
    },
    Site {
        id: "x",
        title: "X（Twitter）",
        api_hosts: &["x.com", "twitter.com", "t.co"],
        cookie_hosts: &["x.com"],
        media_hosts: &["twimg.com"],
        link_hosts: &[],
        request_headers: &[
            "authorization",
            "x-twitter-active-user",
            "x-twitter-client-language",
            "x-client-transaction-id",
        ],
        session_media: false,
        login: LoginNeed::Required,
        network: NetworkAdvice::ProxyRecommended,
    },
    Site {
        id: "youtube",
        title: "YouTube",
        api_hosts: &["youtube.com", "youtu.be"],
        cookie_hosts: &[],
        media_hosts: &["googlevideo.com", "ytimg.com", "ggpht.com"],
        link_hosts: &[],
        request_headers: &[
            "x-youtube-client-name",
            "x-youtube-client-version",
            "x-goog-visitor-id",
        ],
        session_media: false,
        login: LoginNeed::None,
        network: NetworkAdvice::ProxyRecommended,
    },
    Site {
        id: "douyin",
        title: "抖音",
        api_hosts: &["www.douyin.com", "live.douyin.com", "webcast.amemv.com"],
        cookie_hosts: &["www.douyin.com", "live.douyin.com", "webcast.amemv.com"],
        media_hosts: &[
            "douyinvod.com",
            "douyinpic.com",
            "byteimg.com",
            "pstatp.com",
            "amemv.com",
            "douyincdn.com",
            "douyinliving.com",
            "v.douyin.com",
            "www.iesdouyin.com",
        ],
        link_hosts: &[],
        request_headers: &[],
        session_media: false,
        login: LoginNeed::Optional,
        network: NetworkAdvice::Direct,
    },
    Site {
        id: "tiktok",
        title: "TikTok",
        api_hosts: &["tiktok.com"],
        cookie_hosts: &["tiktok.com"],
        media_hosts: &[
            "tiktokcdn.com",
            "tiktokcdn-us.com",
            "tiktokv.com",
            "byteoversea.com",
            "ibytedtos.com",
            "muscdn.com",
        ],
        link_hosts: &[],
        request_headers: &[],
        session_media: true,
        login: LoginNeed::Optional,
        network: NetworkAdvice::ProxyRecommended,
    },
    Site {
        id: "bilibili",
        title: "哔哩哔哩",
        api_hosts: &[
            "api.bilibili.com",
            "www.bilibili.com",
            "space.bilibili.com",
            "api.live.bilibili.com",
        ],
        cookie_hosts: &[
            "api.bilibili.com",
            "www.bilibili.com",
            "space.bilibili.com",
            "api.live.bilibili.com",
        ],
        media_hosts: &[
            "bilivideo.com",
            "hdslb.com",
            "akamaized.net",
            "bilivideo.cn",
            "bytefcdnrd.com",
            "b23.tv",
            "live.bilibili.com",
        ],
        link_hosts: &["bilibili.com"],
        request_headers: &[],
        session_media: false,
        login: LoginNeed::Optional,
        network: NetworkAdvice::Direct,
    },
];

/// The registered site of an id (`bilibili`, `x`, ...).
pub fn site(id: &str) -> Option<&'static Site> {
    SITES.iter().find(|s| s.id == id)
}

/// Whether `host` is one of `domains` or a subdomain of one (case-insensitive, trailing dot ignored).
pub fn host_in(host: &str, domains: &[&str]) -> bool {
    let host = host.trim_end_matches('.').to_ascii_lowercase();
    domains
        .iter()
        .any(|d| host == *d || host.ends_with(&format!(".{d}")))
}

impl Site {
    /// Whether the host belongs to this site's metadata, media or link domains.
    pub fn owns_host(&self, host: &str) -> bool {
        host_in(host, self.api_hosts)
            || host_in(host, self.media_hosts)
            || host_in(host, self.link_hosts)
    }
}

/// The site whose domains contain `host`, if any.
pub fn site_for_host(host: &str) -> Option<&'static Site> {
    SITES.iter().find(|s| s.owns_host(host))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn eight_sites_with_unique_ids_and_disjoint_domains() {
        assert_eq!(SITES.len(), 8);
        for (i, a) in SITES.iter().enumerate() {
            for b in SITES.iter().skip(i + 1) {
                assert_ne!(a.id, b.id);
                for domain in a.api_hosts.iter().chain(a.media_hosts).chain(a.link_hosts) {
                    assert!(
                        !b.api_hosts
                            .iter()
                            .chain(b.media_hosts)
                            .chain(b.link_hosts)
                            .any(|d| d == domain),
                        "{domain} is claimed by {} and {}",
                        a.id,
                        b.id
                    );
                }
            }
        }
    }

    #[test]
    fn no_domain_of_one_site_falls_under_a_domain_of_another() {
        for a in SITES.iter() {
            for b in SITES.iter().filter(|b| b.id != a.id) {
                for host in a.api_hosts.iter().chain(a.media_hosts).chain(a.link_hosts) {
                    assert!(
                        !b.owns_host(host),
                        "{host} of {} falls under a domain of {}",
                        a.id,
                        b.id
                    );
                }
            }
        }
    }

    #[test]
    fn media_hosts_never_fall_under_an_api_host_where_cookies_may_go() {
        for site in SITES.iter() {
            for host in site.media_hosts {
                assert!(
                    !host_in(host, site.api_hosts),
                    "{host} of {} would receive the site's Cookie",
                    site.id
                );
            }
        }
    }

    #[test]
    fn a_cookie_goes_only_to_the_domain_it_was_copied_from() {
        for site in SITES.iter() {
            for host in site.cookie_hosts {
                assert!(
                    host_in(host, site.api_hosts),
                    "{host} of {} takes a Cookie but is not an API host",
                    site.id
                );
                // The two sites that kept every API host as a Cookie host keep their existing
                // host lists; the others take a Cookie only where no media host is.
                assert!(
                    matches!(site.id, "douyin" | "bilibili") || !host_in(host, site.media_hosts),
                    "{host} of {} is a media host",
                    site.id
                );
            }
        }
        let cookie_hosts = |id: &str| site(id).unwrap().cookie_hosts;
        assert_eq!(cookie_hosts("weibo"), ["weibo.com"]);
        assert_eq!(cookie_hosts("xiaohongshu"), ["xiaohongshu.com"]);
        assert_eq!(cookie_hosts("instagram"), ["instagram.com"]);
        assert_eq!(cookie_hosts("x"), ["x.com"]);
        assert_eq!(cookie_hosts("tiktok"), ["tiktok.com"]);
        assert!(cookie_hosts("youtube").is_empty());
        // The list signature headers are protocol constants, never credentials.
        let xhs = site("xiaohongshu").unwrap().request_headers;
        for header in ["x-s", "x-t", "x-s-common", "x-b3-traceid", "x-xray-traceid"] {
            assert!(xhs.contains(&header), "{header}");
            assert!(!header.contains("cookie"), "{header}");
        }
        // The sites that were not changed keep every API host.
        for id in ["douyin", "bilibili"] {
            assert_eq!(cookie_hosts(id), site(id).unwrap().api_hosts, "{id}");
        }
        // The secondary domains of a site never see its Cookie.
        for (id, host) in [
            ("weibo", "weibo.cn"),
            ("weibo", "t.cn"),
            ("x", "twitter.com"),
            ("x", "t.co"),
            ("xiaohongshu", "xhslink.com"),
        ] {
            let s = site(id).unwrap();
            assert!(host_in(host, s.api_hosts), "{host} is an API host of {id}");
            assert!(
                !host_in(host, s.cookie_hosts),
                "{host} must not get {id}'s Cookie"
            );
        }
    }

    #[test]
    fn host_matching_respects_label_boundaries() {
        assert!(host_in(
            "edith.xiaohongshu.com",
            site("xiaohongshu").unwrap().api_hosts
        ));
        assert!(host_in("X.COM.", site("x").unwrap().api_hosts));
        assert!(!host_in("evilx.com", site("x").unwrap().api_hosts));
        assert!(!host_in("x.com.evil.example", site("x").unwrap().api_hosts));
        assert_eq!(site_for_host("pbs.twimg.com").map(|s| s.id), Some("x"));
        assert_eq!(
            site_for_host("sample.bytefcdnrd.com").map(|s| s.id),
            Some("bilibili")
        );
        assert!(site_for_host("example.com").is_none());
    }

    #[test]
    fn overseas_sites_advise_a_proxy_and_domestic_ones_do_not() {
        let proxied: Vec<_> = SITES
            .iter()
            .filter(|s| s.network == NetworkAdvice::ProxyRecommended)
            .map(|s| s.id)
            .collect();
        assert_eq!(proxied, ["instagram", "x", "youtube", "tiktok"]);
    }
}
