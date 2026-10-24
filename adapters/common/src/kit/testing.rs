//! Canned site responses for adapter tests (feature `testing`). The fixtures play the part of
//! Core's proxy: a failure status becomes the failure class Core would report, and every
//! request is recorded so a test can assert what was sent and, as important, what was not.
use crate::error;
use crate::runtime::{Fetch, Transport};
use serde_json::Value;
use std::sync::Mutex;
use trace_contracts::plugin::{PluginError, ProxyFetchResult};
use trace_domain::errors::FailureClass;

/// One request as the fixtures saw it.
#[derive(Debug, Clone)]
pub struct Recorded {
    pub method: String,
    pub url: String,
    pub headers: Value,
    pub body: Option<String>,
    pub credential: bool,
}

struct Route {
    needle: String,
    status: u16,
    body: Vec<u8>,
    final_url: String,
    uses: Option<usize>,
    /// Return the status to the adapter instead of turning it into a failure class, as the
    /// real proxy does for a response it does not interpret itself.
    raw: bool,
}

#[derive(Default)]
pub struct Fixtures {
    routes: Mutex<Vec<Route>>,
    log: Mutex<Vec<Recorded>>,
}

impl Fixtures {
    pub fn new() -> Self {
        Self::default()
    }

    /// Answers 200 with `body` to a request whose `"<METHOD> <url>\n<body>"` contains `needle`.
    /// Earlier routes win.
    pub fn on(self, needle: &str, body: impl Into<Vec<u8>>) -> Self {
        self.route(needle, 200, body.into(), None, None)
    }

    pub fn json(self, needle: &str, body: &Value) -> Self {
        self.on(needle, serde_json::to_vec(body).expect("fixture json"))
    }

    /// Answers `status` (a failure becomes the class Core reports for it).
    pub fn status(self, needle: &str, status: u16) -> Self {
        self.route(needle, status, Vec::new(), None, None)
    }

    /// Answers `status` as a response the adapter itself inspects (the real proxy delivers
    /// the status of a request it does not interpret).
    pub fn http_status(self, needle: &str, status: u16) -> Self {
        let this = self.route(needle, status, Vec::new(), None, None);
        if let Some(route) = this.routes.lock().unwrap().last_mut() {
            route.raw = true;
        }
        this
    }

    /// Answers 200 with `body` once, then falls through to later routes.
    pub fn once(self, needle: &str, body: impl Into<Vec<u8>>) -> Self {
        self.route(needle, 200, body.into(), None, Some(1))
    }

    /// Answers a redirect-following request with this final address.
    pub fn redirect(self, needle: &str, final_url: &str) -> Self {
        self.route(needle, 200, Vec::new(), Some(final_url), None)
    }

    fn route(
        self,
        needle: &str,
        status: u16,
        body: Vec<u8>,
        final_url: Option<&str>,
        uses: Option<usize>,
    ) -> Self {
        self.routes.lock().unwrap().push(Route {
            needle: needle.into(),
            status,
            body,
            final_url: final_url.unwrap_or_default().into(),
            uses,
            raw: false,
        });
        self
    }

    pub fn requests(&self) -> Vec<Recorded> {
        self.log.lock().unwrap().clone()
    }

    /// Requests whose address contains `needle`.
    pub fn count(&self, needle: &str) -> usize {
        self.log
            .lock()
            .unwrap()
            .iter()
            .filter(|r| r.url.contains(needle))
            .count()
    }
}

fn failure(status: u16) -> FailureClass {
    match status {
        401 => FailureClass::AuthRequired,
        403 | 412 => FailureClass::PermissionDenied,
        429 => FailureClass::RateLimited,
        404 | 410 => FailureClass::SourceUnavailable,
        500..=599 => FailureClass::TransientNetwork,
        _ => FailureClass::InvalidPayload,
    }
}

impl Transport for Fixtures {
    fn request(&self, fetch: &Fetch) -> Result<ProxyFetchResult, PluginError> {
        // A site is never asked with a bare or partial browser string: every request that names
        // a User-Agent names the complete desktop one (or, for the two sites whose signatures
        // were fixed against it, the value they have always had).
        if let Some(agent) = fetch.headers.get("user-agent") {
            assert!(
                matches!(agent.as_str(), Some(super::DESKTOP_UA | super::SIGNED_UA)),
                "{} {} names an incomplete User-Agent: {agent}",
                fetch.method,
                fetch.url
            );
        }
        self.log.lock().unwrap().push(Recorded {
            method: fetch.method.into(),
            url: fetch.url.into(),
            headers: fetch.headers.clone(),
            body: fetch.body.map(str::to_owned),
            credential: fetch.credential,
        });
        let subject = format!(
            "{} {}\n{}",
            fetch.method,
            fetch.url,
            fetch.body.unwrap_or_default()
        );
        let mut routes = self.routes.lock().unwrap();
        let Some(route) = routes
            .iter_mut()
            .find(|r| r.uses != Some(0) && subject.contains(&r.needle))
        else {
            return Err(error(
                FailureClass::TransientNetwork,
                "no fixture for request",
            ));
        };
        if let Some(n) = &mut route.uses {
            *n -= 1;
        }
        if !route.raw && !(200..300).contains(&route.status) {
            return Err(error(failure(route.status), "site HTTP failure"));
        }
        use base64::Engine as _;
        Ok(ProxyFetchResult {
            status: route.status,
            headers: Value::Null,
            body_b64: base64::engine::general_purpose::STANDARD.encode(&route.body),
            final_url: if route.final_url.is_empty() {
                fetch.url.to_owned()
            } else {
                route.final_url.clone()
            },
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::kit::DESKTOP_UA;
    use serde_json::json;

    #[test]
    fn the_desktop_user_agent_is_a_complete_browser_string() {
        assert!(DESKTOP_UA.starts_with("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/"));
        assert!(DESKTOP_UA.contains(" (KHTML, like Gecko) Chrome/"));
        assert!(DESKTOP_UA.ends_with(" Safari/537.36"));
    }

    #[test]
    fn a_request_with_a_bare_or_partial_user_agent_is_refused_by_the_fixtures() {
        let fixtures = Fixtures::new().on("example", "ok");
        for agent in [
            "Mozilla/5.0",
            "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/130.0.0.0",
            "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/130.0.0.0 Safari/537.36",
            "",
        ] {
            let fetch = Fetch::get("https://example.test/", json!({"user-agent": agent}), false);
            let result = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
                let _ = fixtures.request(&fetch);
            }));
            assert!(result.is_err(), "{agent:?} must be refused");
        }
        let ok = Fetch::get(
            "https://example.test/",
            json!({"user-agent": DESKTOP_UA}),
            false,
        );
        assert!(fixtures.request(&ok).is_ok());
        let none = Fetch::get("https://example.test/", json!({}), false);
        assert!(fixtures.request(&none).is_ok());
    }
}
