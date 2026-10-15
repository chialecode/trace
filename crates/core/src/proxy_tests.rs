//! C04 network and proxy at the transport boundary: a proxied request names only declared
//! domains, never resolves them here, never accepts an address, re-checks every hop and
//! reports proxy trouble as a network problem.
use super::*;
use crate::config::Profile;
use crate::network::{Mode, NetworkState, Setting, SystemProxy};
use std::net::IpAddr;
use std::sync::{Arc, Mutex};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpListener;
use trace_domain::sites;

struct FakeProxy {
    url: String,
    heads: Arc<Mutex<Vec<String>>>,
}

impl FakeProxy {
    fn seen(&self) -> Vec<String> {
        self.heads.lock().unwrap().clone()
    }
}

/// An HTTP forward proxy that answers each connection with the next canned reply and
/// records the head of every request it receives.
async fn fake_proxy(replies: Vec<&'static str>) -> FakeProxy {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let url = format!("http://{}", listener.local_addr().unwrap());
    let heads = Arc::new(Mutex::new(Vec::new()));
    let seen = heads.clone();
    tokio::spawn(async move {
        let mut replies = replies.into_iter();
        while let Ok((mut stream, _)) = listener.accept().await {
            let mut buffer = Vec::new();
            let mut chunk = [0u8; 1024];
            while !buffer.windows(4).any(|w| w == b"\r\n\r\n") {
                match stream.read(&mut chunk).await {
                    Ok(0) | Err(_) => break,
                    Ok(n) => buffer.extend_from_slice(&chunk[..n]),
                }
            }
            seen.lock()
                .unwrap()
                .push(String::from_utf8_lossy(&buffer).into_owned());
            let reply = replies.next().unwrap_or("HTTP/1.1 500 X\r\n\r\n");
            let _ = stream.write_all(reply.as_bytes()).await;
            let _ = stream.shutdown().await;
        }
    });
    FakeProxy { url, heads }
}

const OK: &str = "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok";

fn manual(url: &str) -> Setting {
    Setting {
        mode: Mode::Manual,
        proxy_url: Some(url.into()),
        auth_ref: None,
    }
}

fn client(network: NetworkState) -> ConstrainedClient {
    ConstrainedClient::build()
        .unwrap()
        .with_network(Arc::new(network))
}

fn quiet_system() -> NetworkState {
    NetworkState::new(Box::new(SystemProxy::default))
}

/// Rules of `site` that also allow one plain-HTTP test host.
fn rules_with_test_host(site: &str, host: &str) -> HostRules {
    let profile = Profile {
        test_allowed_hosts: vec![host.into()],
        ..Profile::default()
    };
    platform_rules(&host_rules(&profile), site)
}

fn production_rules(site: &str) -> HostRules {
    platform_rules(&host_rules(&Profile::default()), site)
}

#[test]
fn every_site_is_scoped_to_its_own_declared_domains() {
    for site in sites::SITES {
        let rules = production_rules(site.id);
        assert_eq!(rules.site.as_deref(), Some(site.id));
        let api: Vec<&str> = rules.api_hosts.iter().map(String::as_str).collect();
        assert_eq!(api, site.api_hosts, "{} api hosts", site.id);
        let media: Vec<&str> = rules.media_hosts.iter().map(String::as_str).collect();
        assert_eq!(media, site.media_hosts, "{} media hosts", site.id);
        for other in sites::SITES.iter().filter(|o| o.id != site.id) {
            for host in other.api_hosts.iter().chain(other.media_hosts) {
                assert!(
                    !host_matches(host, &rules.api_hosts)
                        && !host_matches(host, &rules.media_hosts),
                    "{} must not reach {host}",
                    site.id
                );
            }
        }
    }
    let unknown = platform_rules(&host_rules(&Profile::default()), "fake");
    assert!(unknown.api_hosts.is_empty() && unknown.media_hosts.is_empty());
    assert_eq!(unknown.site, None);
}

#[test]
fn extra_media_hosts_stay_available_to_every_site() {
    let profile = Profile {
        extra_media_hosts: vec!["cdn.example-media.test".into()],
        ..Profile::default()
    };
    for site in sites::SITES {
        let rules = platform_rules(&host_rules(&profile), site.id);
        assert!(host_matches("cdn.example-media.test", &rules.media_hosts));
    }
}

#[test]
fn cookies_go_only_to_the_cookie_domain_of_the_site_whatever_the_route() {
    let c = client(quiet_system());
    let cookie = Some("sid=secret");
    for site in sites::SITES {
        let rules = production_rules(site.id);
        for host in site.cookie_hosts {
            let url = Url::parse(&format!("https://{host}/x")).unwrap();
            let headers = c.build_headers(&rules, None, cookie, &url).unwrap();
            assert!(headers.contains_key(reqwest::header::COOKIE), "{host}");
        }
        // The site's other API domains and its media hosts never get the Cookie.
        for host in site
            .api_hosts
            .iter()
            .filter(|h| !site.cookie_hosts.contains(h))
            .chain(site.media_hosts)
        {
            let url = Url::parse(&format!("https://{host}/x")).unwrap();
            assert!(
                c.build_headers(&rules, None, cookie, &url).is_err(),
                "{} host {host} must not get a Cookie",
                site.id
            );
        }
        for other in sites::SITES.iter().filter(|o| o.id != site.id) {
            for host in other.api_hosts.iter().chain(other.media_hosts) {
                let url = Url::parse(&format!("https://{host}/x")).unwrap();
                assert!(
                    c.build_headers(&rules, None, cookie, &url).is_err(),
                    "{} Cookie must not reach {host} of {}",
                    site.id,
                    other.id
                );
            }
        }
    }
}

#[test]
fn tun_virtual_addresses_stay_allowed_for_direct_connections() {
    for ip in ["198.18.0.1", "198.19.255.254", "198.18.12.34"] {
        let parsed: IpAddr = ip.parse().unwrap();
        assert!(!is_forbidden_ip(&parsed), "{ip} is a TUN fake address");
        assert!(assert_public_host(ip, 443).is_ok(), "{ip}");
    }
    for ip in [
        "10.0.0.1",
        "172.16.0.1",
        "192.168.0.1",
        "127.0.0.1",
        "169.254.1.1",
    ] {
        assert!(is_forbidden_ip(&ip.parse().unwrap()), "{ip} stays refused");
    }
}

#[test]
fn an_enabled_system_proxy_without_an_address_connects_directly() {
    let tun = NetworkState::new(Box::new(|| SystemProxy {
        enabled: true,
        server: String::new(),
    }));
    let c = client(tun);
    for site in sites::SITES {
        assert_eq!(
            c.route(&production_rules(site.id)),
            Route::Direct,
            "{}",
            site.id
        );
    }
    // Rules that name no site (tests, unregistered platforms) never use a proxy.
    let proxied = quiet_system();
    proxied.set(crate::network::GLOBAL, manual("http://127.0.0.1:1"), None);
    assert_eq!(
        client(proxied).route(&platform_rules(&host_rules(&Profile::default()), "fake")),
        Route::Direct
    );
}

#[tokio::test]
async fn a_proxied_request_names_the_host_to_the_proxy_instead_of_an_address() {
    let proxy = fake_proxy(vec![OK]).await;
    let host = "no-such-host.bilibili-proxy.test";
    let rules = rules_with_test_host("bilibili", host);
    let url = format!("http://{host}/api/x?q=1");
    let network = quiet_system();
    network.set("bilibili", manual(&proxy.url), None);
    let out = client(network)
        .fetch_metadata(&rules, "GET", &url, None, None, 1024)
        .await
        .unwrap();
    assert_eq!((out.status, out.body.as_slice()), (200, b"ok".as_slice()));
    let seen = proxy.seen();
    assert_eq!(seen.len(), 1);
    assert!(
        seen[0].starts_with(&format!("GET http://{host}/api/x?q=1 HTTP/1.1")),
        "{}",
        seen[0]
    );
}

#[tokio::test]
async fn the_proxy_login_is_sent_to_the_proxy_only_and_the_site_cookie_is_withheld_from_other_hosts()
 {
    let proxy = fake_proxy(vec![OK]).await;
    let host = "login.weibo-proxy.test";
    let rules = rules_with_test_host("weibo", host);
    let network = quiet_system();
    network.set(
        "weibo",
        manual(&proxy.url),
        Some(("user".into(), "pw".into())),
    );
    client(network)
        .fetch_metadata(&rules, "GET", &format!("http://{host}/x"), None, None, 1024)
        .await
        .unwrap();
    let head = proxy.seen().remove(0).to_ascii_lowercase();
    // base64("user:pw")
    assert!(
        head.contains("proxy-authorization: basic dxnlcjpwdw=="),
        "{head}"
    );
    assert!(!head.contains("cookie:"));
}

#[tokio::test]
async fn proxy_trouble_is_a_network_problem_not_a_content_state() {
    // Nothing listens: connection refused.
    let closed = {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        format!("http://{}", listener.local_addr().unwrap())
    };
    let host = "down.x-proxy.test";
    let rules = rules_with_test_host("x", host);
    let url = format!("http://{host}/x");
    let network = quiet_system();
    network.set("x", manual(&closed), None);
    let err = client(network)
        .fetch_metadata(&rules, "GET", &url, None, None, 1024)
        .await
        .unwrap_err();
    assert!(
        matches!(&err, CoreError::Network(m) if m.contains("proxy")),
        "{err:?}"
    );
    assert_eq!(
        err.failure_class(),
        trace_domain::errors::FailureClass::TransientNetwork
    );

    // The proxy refuses the login.
    let refusing = fake_proxy(vec![
        "HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\nConnection: close\r\n\r\n",
    ])
    .await;
    let network = quiet_system();
    network.set("x", manual(&refusing.url), None);
    let err = client(network)
        .fetch_metadata(&rules, "GET", &url, None, None, 1024)
        .await
        .unwrap_err();
    assert!(
        matches!(&err, CoreError::Network(m) if m.contains("proxy authentication")),
        "{err:?}"
    );
    assert_eq!(
        err.failure_class(),
        trace_domain::errors::FailureClass::TransientNetwork
    );
}

#[tokio::test]
async fn through_a_proxy_addresses_other_sites_and_unknown_hosts_are_refused_before_connecting() {
    let proxy = fake_proxy(vec![OK; 16]).await;
    let network = quiet_system();
    network.set("instagram", manual(&proxy.url), None);
    let c = client(network);
    let rules = production_rules("instagram");
    for target in [
        "https://127.0.0.1/x",
        "https://[::1]/x",
        "https://10.0.0.5/x",
        "https://192.168.1.1/x",
        "https://169.254.169.254/latest/meta-data",
        "https://198.18.0.5/x",
        "https://localhost/x",
        "https://internal.corp/x",
        "https://www.instagram.com.evil.example/x",
        "https://evil-instagram.com/x",
        "https://x.com/x",
        "http://www.instagram.com/x",
        "ftp://www.instagram.com/x",
    ] {
        let err = c
            .fetch_metadata(&rules, "GET", target, None, None, 1024)
            .await
            .unwrap_err();
        assert!(matches!(err, CoreError::Network(_)), "{target}: {err:?}");
        assert!(
            !matches!(&err, CoreError::Network(m) if m.contains("proxy")),
            "{target} was refused by the boundary, not by the proxy: {err:?}"
        );
    }
    assert!(
        proxy.seen().is_empty(),
        "no refused target may reach the proxy: {:?}",
        proxy.seen()
    );
}

#[tokio::test]
async fn every_redirect_hop_through_a_proxy_is_checked_again() {
    let host = "hop.tiktok-proxy.test";
    let rules = rules_with_test_host("tiktok", host);
    let redirect = |location: &'static str| -> &'static str {
        Box::leak(
            format!("HTTP/1.1 302 Found\r\nLocation: {location}\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
                .into_boxed_str(),
        )
    };
    for location in [
        "https://127.0.0.1/admin",
        "https://169.254.169.254/latest",
        "https://other-site.example/x",
        "http://www.tiktok.com/plain",
    ] {
        let proxy = fake_proxy(vec![redirect(location), OK]).await;
        let network = quiet_system();
        network.set("tiktok", manual(&proxy.url), None);
        let err = client(network)
            .fetch_metadata(
                &rules,
                "GET",
                &format!("http://{host}/start"),
                None,
                None,
                1024,
            )
            .await
            .unwrap_err();
        assert!(matches!(err, CoreError::Network(_)), "{location}: {err:?}");
        assert_eq!(proxy.seen().len(), 1, "{location} must not be requested");
    }
}

#[tokio::test]
async fn a_socks5_proxy_receives_the_host_name_not_a_locally_resolved_address() {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let url = format!("socks5://{}", listener.local_addr().unwrap());
    let asked = Arc::new(Mutex::new(Vec::<String>::new()));
    let record = asked.clone();
    tokio::spawn(async move {
        while let Ok((mut stream, _)) = listener.accept().await {
            let mut greeting = [0u8; 3];
            if stream.read_exact(&mut greeting).await.is_err() {
                continue;
            }
            let _ = stream.write_all(&[5, 0]).await;
            let mut head = [0u8; 4];
            if stream.read_exact(&mut head).await.is_err() {
                continue;
            }
            let name = if head[3] == 3 {
                let mut len = [0u8; 1];
                let _ = stream.read_exact(&mut len).await;
                let mut name = vec![0u8; len[0] as usize];
                let _ = stream.read_exact(&mut name).await;
                String::from_utf8_lossy(&name).into_owned()
            } else {
                format!("address-type-{}", head[3])
            };
            record.lock().unwrap().push(name);
            // 05 = connection refused
            let _ = stream.write_all(&[5, 5, 0, 1, 0, 0, 0, 0, 0, 0]).await;
        }
    });
    let host = "named.youtube-proxy.test";
    let rules = rules_with_test_host("youtube", host);
    let network = quiet_system();
    network.set("youtube", manual(&url), None);
    let err = client(network)
        .fetch_metadata(&rules, "GET", &format!("http://{host}/x"), None, None, 1024)
        .await
        .unwrap_err();
    assert!(matches!(err, CoreError::Network(_)), "{err:?}");
    assert_eq!(asked.lock().unwrap().as_slice(), [host.to_owned()]);
}

/// What a proxied query needs, and no more: each site sends only its own protocol headers,
/// a body rides on a POST to an API host and nowhere else, and the anti-forgery headers a
/// site wants are copied from the Cookie by Core.
mod queries {
    use super::*;
    use serde_json::json;

    const FORM: &str = "application/x-www-form-urlencoded";

    async fn send(
        site: &str,
        host: &str,
        request: MetadataRequest<'_>,
    ) -> (Result<FetchOutcome, CoreError>, Vec<String>) {
        let proxy = fake_proxy(vec![OK]).await;
        let rules = rules_with_test_host(site, host);
        let network = quiet_system();
        network.set(site, manual(&proxy.url), None);
        let out = client(network).fetch_query(&rules, &request).await;
        (out, proxy.seen())
    }

    fn post<'a>(
        url: &'a str,
        headers: &'a serde_json::Value,
        body: Option<&'a str>,
    ) -> MetadataRequest<'a> {
        MetadataRequest {
            method: "POST",
            url,
            headers: Some(headers),
            cookie: None,
            body,
            max_bytes: 1024,
            evidence_id: None,
        }
    }

    #[tokio::test]
    async fn a_post_carries_its_body_and_content_type_to_the_api_host() {
        let host = "graph.instagram-query.test";
        let url = format!("http://{host}/graphql/query");
        let headers =
            json!({"content-type": FORM, "x-fb-lsd": "token-a", "x-ig-app-id": "936619743392459"});
        let (out, seen) = send("instagram", host, post(&url, &headers, Some("a=1&b=2"))).await;
        assert_eq!(out.unwrap().status, 200);
        let head = seen[0].to_ascii_lowercase();
        assert!(head.starts_with("post "), "{head}");
        assert!(head.contains("content-length: 7"), "{head}");
        assert!(head.contains("x-fb-lsd: token-a"), "{head}");
        assert!(seen[0].ends_with("a=1&b=2"), "{}", seen[0]);
    }

    #[tokio::test]
    async fn a_body_without_a_post_or_with_the_wrong_kind_never_leaves() {
        let host = "graph.instagram-query.test";
        let url = format!("http://{host}/graphql/query");
        let form = json!({"content-type": FORM});
        let text = json!({"content-type": "text/plain"});
        let none = json!({});
        let big = "x".repeat(trace_contracts::plugin::PROXY_BODY_MAX + 1);
        for (request, why) in [
            (post(&url, &form, None), "a POST without a body"),
            (post(&url, &text, Some("a=1")), "a body of an unknown kind"),
            (post(&url, &none, Some("a=1")), "a body that names no kind"),
            (post(&url, &form, Some(&big)), "an oversized body"),
            (
                MetadataRequest {
                    method: "GET",
                    ..post(&url, &form, Some("a=1"))
                },
                "a GET with a body",
            ),
        ] {
            let (out, seen) = send("instagram", host, request).await;
            assert!(out.is_err(), "{why} must be refused");
            assert!(seen.is_empty(), "{why} must not be sent");
        }
        // A media host takes no query bodies.
        let rules = production_rules("instagram");
        let err = client(quiet_system())
            .fetch_query(
                &rules,
                &post("https://static.cdninstagram.com/q", &form, Some("a=1")),
            )
            .await
            .unwrap_err();
        assert!(matches!(err, CoreError::InvalidInput(_)), "{err:?}");
    }

    #[tokio::test]
    async fn a_body_is_not_replayed_to_a_redirect_target() {
        let proxy = fake_proxy(vec![
            "HTTP/1.1 302 Found\r\nLocation: http://elsewhere.instagram-query.test/\r\nContent-Length: 0\r\nConnection: close\r\n\r\n",
        ])
        .await;
        let host = "graph.instagram-query.test";
        let rules = rules_with_test_host("instagram", host);
        let network = quiet_system();
        network.set("instagram", manual(&proxy.url), None);
        let url = format!("http://{host}/graphql/query");
        let headers = json!({"content-type": FORM});
        let err = client(network)
            .fetch_query(&rules, &post(&url, &headers, Some("a=1")))
            .await
            .unwrap_err();
        assert!(
            matches!(&err, CoreError::Network(m) if m.contains("redirect")),
            "{err:?}"
        );
        assert_eq!(proxy.seen().len(), 1);
    }

    #[test]
    fn each_site_may_send_only_its_own_protocol_headers() {
        let allowed = |site: &str| production_rules(site).allowed_headers;
        for site in sites::SITES {
            let headers = allowed(site.id);
            for base in sites::BASE_REQUEST_HEADERS {
                assert!(headers.iter().any(|h| h == base), "{} base {base}", site.id);
            }
            assert!(!headers.iter().any(|h| h == "cookie"), "{} Cookie", site.id);
            for other in sites::SITES.iter().filter(|o| o.id != site.id) {
                for extra in other.request_headers {
                    assert!(
                        site.request_headers.contains(extra) || !headers.iter().any(|h| h == extra),
                        "{} must not send {extra} of {}",
                        site.id,
                        other.id
                    );
                }
            }
        }
        assert!(allowed("x").iter().any(|h| h == "authorization"));
        assert!(!allowed("instagram").iter().any(|h| h == "authorization"));
    }

    #[tokio::test]
    async fn an_adapter_cannot_send_a_header_its_site_does_not_declare() {
        let host = "api.weibo-query.test";
        let url = format!("http://{host}/x");
        let rules = rules_with_test_host("weibo", host);
        let headers = json!({"x-csrf-token": "forged"});
        let err = client(quiet_system())
            .fetch_metadata(&rules, "GET", &url, Some(&headers), None, 1024)
            .await
            .unwrap_err();
        assert!(matches!(err, CoreError::InvalidInput(_)), "{err:?}");
    }

    #[tokio::test]
    async fn anti_forgery_headers_are_copied_from_the_cookie_by_core() {
        let cookie = "auth_token=SECRET-AUTH; ct0=csrf-value; lang=en";
        let host = "api.x-query.test";
        let proxy = fake_proxy(vec![OK]).await;
        let rules = rules_with_test_host("x", host);
        let network = quiet_system();
        network.set("x", manual(&proxy.url), None);
        client(network)
            .fetch_metadata(
                &rules,
                "GET",
                &format!("http://{host}/x"),
                None,
                Some(cookie),
                1024,
            )
            .await
            .unwrap();
        let head = proxy.seen().remove(0).to_ascii_lowercase();
        assert!(head.contains("x-csrf-token: csrf-value"), "{head}");
        assert!(
            head.contains("x-twitter-auth-type: oauth2session"),
            "{head}"
        );
        let derived = |site: &str, cookie: &str| {
            crate::site_context::session_headers(Some(site), cookie)
                .into_iter()
                .map(|(n, v)| format!("{n}={}", v.to_str().unwrap()))
                .collect::<Vec<_>>()
        };
        assert_eq!(
            derived("instagram", "csrftoken=abc; sessionid=zzz"),
            ["x-csrftoken=abc"]
        );
        assert_eq!(
            derived("weibo", "XSRF-TOKEN=tok; SUB=zzz"),
            ["x-xsrf-token=tok"]
        );
        assert!(derived("weibo", "SUB=zzz").is_empty());
        assert!(derived("bilibili", "csrftoken=abc; ct0=x").is_empty());
        assert!(crate::site_context::session_headers(None, "ct0=x").is_empty());
    }
}
