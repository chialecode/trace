//! Instagram's web client calls: a page with its tokens, and a GraphQL operation by form.
use crate::ops;
use crate::web::{self, ORIGIN, Tokens, Viewer};
use serde_json::{Value, json};
use trace_contracts::plugin::PluginError;
use trace_site_support::kit::{self, net};
use trace_site_support::runtime::{Fetch, Transport, body_bytes};

/// The page tokens and scripts of one page load; they belong to the visitor's session.
#[derive(Debug, Clone)]
pub(crate) struct Session {
    pub tokens: Tokens,
    pub scripts: Vec<String>,
    pub profile_id: Option<String>,
}

impl Session {
    pub(crate) fn read(html: &str) -> Result<Self, PluginError> {
        if web::viewer_in(html) == Viewer::Anonymous {
            return Err(kit::login_needed("Instagram shows the login page"));
        }
        let tokens = web::tokens_in(html).ok_or_else(|| kit::schema("page has no page tokens"))?;
        Ok(Self {
            tokens,
            scripts: web::script_urls_in(html),
            profile_id: web::profile_id_in(html),
        })
    }
}

/// A page of the site as the visitor's session sees it. A login or verification page
/// replaces the page that was asked for, and is told apart from it by its address.
pub(crate) fn fetch_page(net: &dyn Transport, path: &str) -> Result<String, PluginError> {
    let (text, final_url) = net::page(net, &format!("{ORIGIN}{path}"), web::page_headers(), true)?;
    let landed = url::Url::parse(&final_url).map(|u| u.path().to_owned());
    match landed.as_deref() {
        Ok(p) if p.starts_with("/accounts/login") => {
            Err(kit::login_needed("Instagram asks for a login"))
        }
        Ok(p)
            if [
                "/challenge",
                "/accounts/suspended",
                "/accounts/consent",
                "/accounts/onetap",
            ]
            .iter()
            .any(|m| p.starts_with(m)) =>
        {
            Err(kit::limited("Instagram asks the visitor to verify"))
        }
        _ => Ok(text),
    }
}

/// Why a GraphQL call did not return data.
enum Outcome {
    /// The operation id is not known any more.
    Stale,
    Failed(PluginError),
}

fn failure(answer: &Value) -> Option<Outcome> {
    let has_data = answer["data"].as_object().is_some_and(|d| !d.is_empty());
    if has_data {
        return None;
    }
    let said: String = [
        &answer["message"],
        &answer["error_type"],
        &answer["errors"][0]["message"],
        &answer["errors"][0]["summary"],
        &answer["errors"][0]["description"],
    ]
    .iter()
    .filter_map(|v| v.as_str())
    .map(|t| format!("{} ", t.to_ascii_lowercase()))
    .collect();
    let has = |words: &[&str]| words.iter().any(|w| said.contains(w));
    Some(
        if answer["require_login"] == true || has(&["login_required", "log in"]) {
            Outcome::Failed(kit::login_needed("Instagram asks for a login"))
        } else if has(&["challenge", "checkpoint", "consent_required"]) {
            Outcome::Failed(kit::limited("Instagram asks the visitor to verify"))
        } else if has(&[
            "wait a few minutes",
            "rate limit",
            "feedback_required",
            "too many",
        ]) {
            Outcome::Failed(kit::limited("Instagram asks to slow down"))
        } else if has(&["persisted", "doc_id", "unknown query", "query not found"]) {
            Outcome::Stale
        } else {
            Outcome::Failed(kit::schema("Instagram answered without data"))
        },
    )
}

/// The answer text without the guard prefix the site puts in front of some JSON.
fn json_of(text: &str) -> Option<Value> {
    let trimmed = text.trim_start();
    let body = trimmed.strip_prefix("for (;;);").unwrap_or(trimmed);
    serde_json::from_str(body).ok()
}

fn post(
    net: &dyn Transport,
    session: &Session,
    endpoint: &str,
    name: &str,
    doc_id: &str,
    variables: &Value,
) -> Result<Value, Outcome> {
    let variables = variables.to_string();
    let body = net::form(&[
        ("fb_dtsg", &session.tokens.dtsg),
        ("jazoest", &session.tokens.jazoest()),
        ("lsd", &session.tokens.lsd),
        ("__a", "1"),
        ("__comet_req", "7"),
        ("__d", "www"),
        ("dpr", "1"),
        ("fb_api_caller_class", "RelayModern"),
        ("fb_api_req_friendly_name", name),
        ("variables", &variables),
        ("server_timestamps", "true"),
        ("doc_id", doc_id),
    ]);
    let headers = json!({
        "content-type": "application/x-www-form-urlencoded",
        "x-ig-app-id": web::app_id(),
        "x-asbd-id": web::asbd_id(),
        "x-fb-lsd": session.tokens.lsd,
        "x-fb-friendly-name": name,
        "x-requested-with": "XMLHttpRequest",
        "referer": format!("{ORIGIN}/"),
        "user-agent": web::UA,
        "accept": "*/*",
    });
    let url = format!("{ORIGIN}{endpoint}");
    let reply = match net.request(&Fetch::post(&url, headers, &body, true).large()) {
        Ok(reply) => reply,
        // The site answers an unknown operation with a client error.
        Err(e) if e.class == trace_domain::errors::FailureClass::InvalidPayload => {
            return Err(Outcome::Stale);
        }
        Err(e) => return Err(Outcome::Failed(e)),
    };
    let bytes = body_bytes(&reply).map_err(Outcome::Failed)?;
    let answer = json_of(&String::from_utf8_lossy(&bytes))
        .ok_or_else(|| Outcome::Failed(kit::schema("Instagram answer is not JSON")))?;
    match failure(&answer) {
        Some(outcome) => Err(outcome),
        None => Ok(answer),
    }
}

/// Calls one operation. An id the site no longer knows is read again once; a second
/// rejection is a changed interface, never an empty result.
pub(crate) fn graphql(
    net: &dyn Transport,
    session: &Session,
    endpoint: &str,
    name: &str,
    variables: &Value,
) -> Result<Value, PluginError> {
    for attempt in 0..2 {
        let known = ops::get(net, &session.scripts)?;
        match post(net, session, endpoint, name, known.id(name)?, variables) {
            Ok(answer) => return Ok(answer),
            Err(Outcome::Failed(e)) => return Err(e),
            Err(Outcome::Stale) => {
                ops::forget();
                if attempt == 1 {
                    return Err(kit::schema(
                        "Instagram rejected the operation after its ids were read again",
                    ));
                }
            }
        }
    }
    Err(kit::schema("unreachable"))
}

/// The profile of an account by its id; the answer also tells who is looking.
pub(crate) fn profile(
    net: &dyn Transport,
    session: &Session,
    profile_id: &str,
) -> Result<Value, PluginError> {
    let answer = graphql(
        net,
        session,
        "/api/graphql",
        ops::PROFILE,
        &json!({"id": profile_id, "render_surface": "PROFILE"}),
    )?;
    let data = &answer["data"];
    if data.get("viewer").is_some_and(Value::is_null) {
        return Err(kit::login_needed("Instagram does not know the visitor"));
    }
    let user = &data["user"];
    if !user.is_object() {
        return Err(trace_site_support::error(
            trace_domain::errors::FailureClass::SourceUnavailable,
            "Instagram shows no such user",
        ));
    }
    Ok(user.clone())
}

#[cfg(test)]
mod tests {
    use super::*;
    use trace_domain::errors::FailureClass;

    fn class(answer: Value) -> Option<FailureClass> {
        match failure(&answer)? {
            Outcome::Stale => None,
            Outcome::Failed(e) => Some(e.class),
        }
    }

    #[test]
    fn data_wins_and_errors_are_told_apart_by_what_they_say() {
        assert!(failure(&json!({"data": {"user": null}})).is_none());
        assert_eq!(
            class(json!({"status": "fail", "message": "login_required", "require_login": true})),
            Some(FailureClass::AuthRequired)
        );
        assert_eq!(
            class(json!({"message": "challenge_required", "status": "fail"})),
            Some(FailureClass::RateLimited)
        );
        assert_eq!(
            class(json!({"message": "Please wait a few minutes before you try again."})),
            Some(FailureClass::RateLimited)
        );
        assert!(matches!(
            failure(&json!({"errors": [{"message": "PersistedQueryNotFound"}]})),
            Some(Outcome::Stale)
        ));
        assert_eq!(
            class(json!({"data": {}})),
            Some(FailureClass::SchemaChanged)
        );
        assert_eq!(class(json!({})), Some(FailureClass::SchemaChanged));
    }

    #[test]
    fn the_guard_prefix_is_removed_and_text_that_is_not_json_is_nothing() {
        assert_eq!(json_of("for (;;);{\"a\":1}"), Some(json!({"a": 1})));
        assert_eq!(json_of("  {\"a\":1}"), Some(json!({"a": 1})));
        assert_eq!(json_of("<html>"), None);
    }
}
