//! The metadata proxy: adapters fetch through Core's constrained HTTP client, and Cookies
//! are attached here, per platform and authorized domain, so they never reach the adapter
//! process (INV-10).
use super::CredentialLookup;
#[cfg(test)]
use super::ProxyFixture;
use super::session::send_frame;
use crate::session_jar::SessionJar;
use crate::transport::{
    ConstrainedClient, FetchOutcome, HostRules, MetadataRequest, host_matches, platform_rules,
};
use base64::Engine as _;
use serde_json::Value;
use std::sync::Arc;
use std::sync::atomic::{AtomicU64, Ordering};
use tokio::sync::Mutex;
use tokio_util::sync::CancellationToken;
use trace_contracts::plugin::{
    PluginError, PluginRequest, PluginResponse, ProxyFetchParams, ProxyFetchResult,
};
use trace_contracts::plugin_chunks::{self, METADATA_MAX};
use trace_contracts::wire;
use trace_domain::errors::FailureClass;

/// Proxy requests in flight at once per session; beyond this the session is torn down.
pub(super) const POOL_LIMIT: usize = 32;
/// From this many in flight, new requests are answered with a rate-limit instead of fetched.
pub(super) const OCCUPIED_AT: usize = 8;
const OCCUPIED_RETRY_MS: u64 = 1000;

/// Counter slots: requests accepted, responses built, responses delivered.
pub(super) const ACCEPTED: usize = 0;
const BUILT: usize = 1;
const DELIVERED: usize = 2;

/// Everything one proxy request needs; cloned for each request.
#[derive(Clone)]
pub(super) struct ProxyContext {
    pub http: Arc<ConstrainedClient>,
    pub rules: HostRules,
    pub credentials: CredentialLookup,
    pub session: Session,
    pub platform: String,
    pub counts: Arc<[AtomicU64; 3]>,
    pub stdin: Arc<Mutex<tokio::process::ChildStdin>>,
    pub cancel: CancellationToken,
    #[cfg(test)]
    pub fixture: Option<ProxyFixture>,
}

/// Where a session's Set-Cookie lines are kept: the host's jar, under the key of the
/// credential scope the session runs in.
#[derive(Clone)]
pub(super) struct Session {
    pub jar: Arc<SessionJar>,
    pub scope: String,
}

fn rejection(class: FailureClass, message: &str, retry_after_ms: Option<u64>) -> PluginError {
    PluginError {
        class,
        message: message.into(),
        retry_after_ms,
        details: Value::Null,
    }
}

impl ProxyContext {
    pub(super) fn count(&self, slot: usize) {
        self.counts[slot].fetch_add(1, Ordering::Relaxed);
    }

    /// Answers one adapter request and writes the response frame back. Runs in the bounded
    /// pool so one slow upstream cannot stall unrelated in-flight calls.
    pub(super) async fn serve(self, req: PluginRequest, occupied: bool) {
        let large = req.method == "proxy.fetch_large";
        let response = if occupied {
            Err(rejection(
                FailureClass::RateLimited,
                "proxy concurrency budget",
                Some(OCCUPIED_RETRY_MS),
            ))
        } else {
            tokio::select! {
                _ = self.cancel.cancelled() => return,
                value = self.fetch(&req) => value,
            }
        };
        let out = PluginResponse {
            request_id: req.request_id.clone(),
            ok: response.is_ok(),
            result: response
                .as_ref()
                .ok()
                .and_then(|r| serde_json::to_value(r).ok()),
            error: response.err(),
        };
        self.count(BUILT);
        let frame = encode_response(&out, large);
        if let Ok(frame) = frame
            && send_frame(self.stdin.clone(), frame, self.cancel.clone())
                .await
                .is_ok()
        {
            self.count(DELIVERED);
        }
    }

    async fn fetch(&self, req: &PluginRequest) -> Result<ProxyFetchResult, PluginError> {
        // The ordinary adapter default is 512 KiB, not the legacy protocol's permitted
        // request cap. Older peers may ask for 4 MiB and return a small body. Preserve them
        // while bounding all responses and keeping ordinary frames.
        let budget_ok = req.params["max_bytes"]
            .as_u64()
            .is_some_and(|n| n != 0 && n <= METADATA_MAX as u64);
        if !budget_ok {
            return Err(rejection(
                FailureClass::InvalidPayload,
                "proxy byte budget",
                None,
            ));
        }
        #[cfg(test)]
        if let Some(fixture) = &self.fixture {
            return fixture(&req.params);
        }
        fetch_for_adapter(
            &self.http,
            &self.rules,
            (&self.credentials, &self.session),
            &self.platform,
            &req.params,
            req.context["evidence_id"].as_str(),
        )
        .await
    }
}

/// A response too large for the negotiated frame budget is replaced by a plain failure.
fn encode_response(out: &PluginResponse, large: bool) -> Result<Vec<u8>, wire::FrameError> {
    let frame = if large {
        plugin_chunks::encode(out)
    } else {
        wire::encode_frame(out)
    };
    frame.or_else(|_| {
        wire::encode_frame(&PluginResponse {
            request_id: out.request_id.clone(),
            ok: false,
            result: None,
            error: Some(rejection(
                FailureClass::InvalidPayload,
                "proxy response exceeds negotiated frame budget",
                None,
            )),
        })
    })
}

/// What a request that asks for the site's credential gets.
enum CookieScope {
    /// The URL is on the site's Cookie domain: the Cookie is attached.
    Send,
    /// The URL is on another API domain of the site: the request goes ahead without the Cookie.
    Withhold,
    /// The URL is not an API domain of the site (a media host, an address Core cannot read):
    /// the request is refused.
    Refuse,
}

fn cookie_scope(rules: &HostRules, url: &str) -> CookieScope {
    let host = url::Url::parse(url)
        .ok()
        .and_then(|u| u.host_str().map(str::to_owned))
        .unwrap_or_default();
    if host_matches(&host, &rules.cookie_hosts) || host_matches(&host, &rules.test_allowed) {
        CookieScope::Send
    } else if host_matches(&host, &rules.api_hosts) {
        CookieScope::Withhold
    } else {
        CookieScope::Refuse
    }
}

/// Keeps the session Cookies a response set, for a site whose media needs them and only from a
/// response of the site's own Cookie domain.
fn remember_session(rules: &HostRules, session: &Session, outcome: &FetchOutcome) {
    let wanted = rules
        .site
        .as_deref()
        .and_then(trace_domain::sites::site)
        .is_some_and(|site| site.session_media);
    if wanted && host_matches(&outcome.final_host, &rules.cookie_hosts) {
        session.jar.remember(&session.scope, &outcome.set_cookies.0);
    }
}

async fn fetch_for_adapter(
    http: &ConstrainedClient,
    rules: &HostRules,
    (credentials, session): (&CredentialLookup, &Session),
    platform: &str,
    params: &Value,
    evidence_id: Option<&str>,
) -> Result<ProxyFetchResult, PluginError> {
    let params: ProxyFetchParams = serde_json::from_value(params.clone()).map_err(|e| {
        rejection(
            FailureClass::InvalidPayload,
            &format!("bad proxy params: {e}"),
            None,
        )
    })?;
    let scoped_rules = platform_rules(rules, platform);
    let cookie = if params.with_credential {
        match cookie_scope(&scoped_rules, &params.url) {
            CookieScope::Send => credentials(platform),
            CookieScope::Withhold => None,
            CookieScope::Refuse => {
                return Err(rejection(
                    FailureClass::InvalidPayload,
                    "credential host not allowed",
                    None,
                ));
            }
        }
    } else {
        None
    };
    let request = MetadataRequest {
        method: &params.method,
        url: &params.url,
        headers: Some(&params.headers),
        cookie: cookie.as_deref(),
        body: params.body.as_deref(),
        max_bytes: params.max_bytes,
        evidence_id,
    };
    let outcome = http
        .fetch_query(&scoped_rules, &request)
        .await
        .map_err(|e| {
            let class = e.failure_class();
            rejection(class, &format!("{class:?}"), None)
        })?;
    // The session the page started belongs to the Cookie the page was asked with: with the
    // credential, or with none when the scope has none.
    if params.with_credential || credentials(platform).is_none() {
        remember_session(&scoped_rules, session, &outcome);
    }
    Ok(ProxyFetchResult {
        status: outcome.status,
        headers: serde_json::json!({}),
        body_b64: base64::engine::general_purpose::STANDARD.encode(&outcome.body),
        final_url: outcome.final_url,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::config::Profile;
    use crate::transport::host_rules;
    use trace_domain::sites;

    fn scope(site: &str, url: &str) -> &'static str {
        let rules = platform_rules(&host_rules(&Profile::default()), site);
        match cookie_scope(&rules, url) {
            CookieScope::Send => "send",
            CookieScope::Withhold => "withhold",
            CookieScope::Refuse => "refuse",
        }
    }

    #[test]
    fn the_credential_goes_to_the_cookie_domain_only_and_the_secondary_domains_are_asked_without_it()
     {
        for site in sites::SITES {
            for host in site.cookie_hosts {
                assert_eq!(
                    scope(site.id, &format!("https://www.{host}/a")),
                    "send",
                    "{host}"
                );
                assert_eq!(
                    scope(site.id, &format!("https://{host}/a")),
                    "send",
                    "{host}"
                );
            }
            for host in site
                .api_hosts
                .iter()
                .filter(|h| !site.cookie_hosts.contains(h))
            {
                assert_eq!(
                    scope(site.id, &format!("https://{host}/a")),
                    "withhold",
                    "{host}"
                );
            }
            // A media host, another site, a lookalike and an unreadable address get none.
            for host in site.media_hosts {
                assert_eq!(
                    scope(site.id, &format!("https://{host}/a")),
                    "refuse",
                    "{host}"
                );
            }
            assert_eq!(scope(site.id, "https://evil.example/a"), "refuse");
            assert_eq!(scope(site.id, "not a url"), "refuse");
            for other in sites::SITES.iter().filter(|o| o.id != site.id) {
                assert_eq!(
                    scope(site.id, &format!("https://{}/a", other.api_hosts[0])),
                    "refuse",
                    "{} to {}",
                    site.id,
                    other.id
                );
            }
        }
        // The named secondary domains.
        for (site, host) in [
            ("weibo", "weibo.cn"),
            ("weibo", "t.cn"),
            ("x", "twitter.com"),
            ("x", "t.co"),
            ("xiaohongshu", "xhslink.com"),
        ] {
            assert_eq!(
                scope(site, &format!("https://{host}/a")),
                "withhold",
                "{host}"
            );
        }
        // YouTube carries no Cookie at all, and the unchanged two keep every API host.
        assert_eq!(scope("youtube", "https://www.youtube.com/a"), "withhold");
        assert_eq!(scope("douyin", "https://www.douyin.com/a"), "send");
        assert_eq!(scope("bilibili", "https://api.bilibili.com/a"), "send");
    }

    fn answered(host: &str, lines: &[&str]) -> FetchOutcome {
        FetchOutcome {
            status: 200,
            final_host: host.into(),
            set_cookies: crate::session_jar::SetCookies(
                lines.iter().map(|l| (*l).to_owned()).collect(),
            ),
            ..Default::default()
        }
    }

    #[test]
    fn a_session_starts_only_from_the_cookie_domain_of_a_site_that_needs_one() {
        let rules = |site| platform_rules(&host_rules(&Profile::default()), site);
        let session = |scope: &str| Session {
            jar: Arc::default(),
            scope: scope.into(),
        };
        // TikTok's own pages set the session the video address wants.
        let kept = session("tiktok/c1/g1");
        remember_session(
            &rules("tiktok"),
            &kept,
            &answered(
                "www.tiktok.com",
                &["ttwid=T1; Path=/", "tt_chain_token=a=="],
            ),
        );
        assert_eq!(
            kept.jar.merged("tiktok/c1/g1", None).as_deref(),
            Some("ttwid=T1; tt_chain_token=a==")
        );
        // A response from a CDN, or from a host that is not the site's, starts none.
        for host in ["v16m.tiktokcdn.com", "sf16.tiktokv.com", "evil.example"] {
            let fresh = session("tiktok");
            remember_session(&rules("tiktok"), &fresh, &answered(host, &["ttwid=T"]));
            assert!(fresh.jar.is_empty(), "{host}");
        }
        // Another site's Cookies are only ever the credential's.
        for (site, host) in [
            ("weibo", "weibo.com"),
            ("x", "x.com"),
            ("bilibili", "api.bilibili.com"),
            ("youtube", "www.youtube.com"),
        ] {
            let fresh = session(site);
            remember_session(&rules(site), &fresh, &answered(host, &["a=1"]));
            assert!(fresh.jar.is_empty(), "{site}");
        }
    }
}
