//! Xiaohongshu accounts: the profile page, and the login probe.
use crate::api::{self, ORIGIN};
use crate::link::{is_short, parse_account, token_in};
use crate::memo;
use serde_json::{Value, json};
use trace_contracts::plugin::{PluginError, ProbeAuthResult, ResolvedAccount};
use trace_domain::errors::FailureClass;
use trace_site_support::kit::{self, count, net, string};
use trace_site_support::{error, runtime::Transport};

/// A short link is followed once through the site; any other input is returned as typed.
pub(crate) fn expand(net: &dyn Transport, input: &str) -> Result<String, PluginError> {
    if !is_short(input) {
        return Ok(input.trim().to_owned());
    }
    net::final_url(
        net,
        input.trim(),
        json!({"user-agent": crate::api::UA, "referer": format!("{ORIGIN}/")}),
    )
}

/// The path of a page that opens with the token the site gave for it, if one is known.
pub(crate) fn path_with_token(path: &str, id: &str, source: &str) -> String {
    match memo::recall(id) {
        Some(token) => format!(
            "{path}?xsec_token={}&xsec_source={source}",
            urlencode(&token)
        ),
        None => path.to_owned(),
    }
}

fn urlencode(token: &str) -> String {
    url::form_urlencoded::byte_serialize(token.as_bytes()).collect()
}

/// The profile page's state; the basic information must be there, or the page is not a
/// profile.
pub(crate) fn profile_state(net: &dyn Transport, user_id: &str) -> Result<Value, PluginError> {
    let path = path_with_token(&format!("/user/profile/{user_id}"), user_id, "pc_feed");
    let state = api::state(net, &path)?;
    if string(&state["user"]["userPageData"]["basicInfo"]["nickname"]).is_none() {
        return Err(if state["user"]["loggedIn"] == false {
            kit::login_needed("Xiaohongshu shows no profile without a login")
        } else {
            kit::schema("profile has no basic information")
        });
    }
    Ok(state)
}

pub(crate) fn resolve(net: &dyn Transport, input: &str) -> Result<ResolvedAccount, PluginError> {
    let expanded = expand(net, input)?;
    let user_id = parse_account(&expanded).ok_or_else(|| {
        error(
            FailureClass::InvalidPayload,
            "not a Xiaohongshu profile link or user id",
        )
    })?;
    if let Some(token) = token_in(&expanded) {
        memo::remember(&user_id, &token);
    }
    let state = profile_state(net, &user_id)?;
    let page = &state["user"]["userPageData"];
    let info = &page["basicInfo"];
    let counts: Value = page["interactions"]
        .as_array()
        .into_iter()
        .flatten()
        .filter_map(|i| Some((i["type"].as_str()?.to_owned(), json!(count(&i["count"])))))
        .collect::<serde_json::Map<_, _>>()
        .into();
    Ok(ResolvedAccount {
        native_account_key: user_id.clone(),
        display_name: string(&info["nickname"]),
        profile_url: Some(format!("{ORIGIN}/user/profile/{user_id}")),
        evidence: json!({
            "source": "profile.state",
            "interactions": counts,
            "profile": trace_contracts::content::safe_metadata(&json!({
                "red_id": info["redId"],
                "description": info["desc"],
                "ip_location": info["ipLocation"],
            })),
        }),
    })
}

/// A login shows as `user.loggedIn` in the page state. A redirect to the login page is the
/// same statement; a page that says nothing proves nothing.
pub(crate) fn probe(net: &dyn Transport) -> Result<ProbeAuthResult, PluginError> {
    let result = |state: &str, proven: bool| ProbeAuthResult {
        state: state.into(),
        capability: json!({"login_proven": proven, "source": "page.state"}),
    };
    let state = match api::state(net, "/explore") {
        Ok(state) => state,
        Err(e) if e.class == FailureClass::AuthRequired => return Ok(result("invalid", false)),
        // A page that cannot be read says nothing about the login, and says how it looked.
        Err(e) if e.class == FailureClass::SchemaChanged => {
            return Ok(ProbeAuthResult {
                state: "unknown".into(),
                capability: json!({"login_proven": false, "source": "page.state", "detail": e.message}),
            });
        }
        Err(e) => return Err(e),
    };
    Ok(match state["user"]["loggedIn"].as_bool() {
        Some(true) => result("valid", true),
        Some(false) => result("invalid", false),
        None => result("unknown", false),
    })
}
