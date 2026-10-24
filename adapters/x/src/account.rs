//! X accounts: the stable identity behind a name, and the login probe.
use crate::api::{self, ORIGIN};
use crate::link::{is_short, parse_account};
use serde_json::{Value, json};
use trace_contracts::plugin::{PluginError, ProbeAuthResult, ResolvedAccount};
use trace_domain::errors::FailureClass;
use trace_site_support::kit::{self, net, page, string};
use trace_site_support::{error, runtime::Transport};

/// A short link is followed once through the site; any other input is returned as typed.
pub(crate) fn expand(net: &dyn Transport, input: &str) -> Result<String, PluginError> {
    if !is_short(input) {
        return Ok(input.trim().to_owned());
    }
    net::final_url(net, input.trim(), json!({"user-agent": crate::api::UA}))
}

/// Why the site shows no user, in terms of what the visitor can do about it.
fn unavailable(user: &Value) -> PluginError {
    match user["reason"].as_str() {
        Some("Protected" | "Blocked") => kit::denied("the X account is protected"),
        Some("NsfwLoggedOut" | "NsfwViewerHasNoStatedAge") => {
            kit::login_needed("the X account needs a login to be shown")
        }
        Some("Suspended") => error(
            FailureClass::SourceUnavailable,
            "the X account is suspended",
        ),
        _ => kit::schema("X user is unavailable for a reason that is not understood"),
    }
}

pub(crate) fn resolve(net: &dyn Transport, input: &str) -> Result<ResolvedAccount, PluginError> {
    let expanded = expand(net, input)?;
    let name = parse_account(&expanded).ok_or_else(|| {
        error(
            FailureClass::InvalidPayload,
            "not an X account link or @name",
        )
    })?;
    let answer = api::graphql(
        net,
        "UserByScreenName",
        &json!({"screen_name": name, "withSafetyModeUserFields": true}),
    )?;
    let user = &answer["data"]["user"]["result"];
    if user.is_null() {
        return Err(error(FailureClass::SourceUnavailable, "X has no such user"));
    }
    if user["__typename"].as_str() == Some("UserUnavailable") {
        return Err(unavailable(user));
    }
    let key = string(&user["rest_id"])
        .filter(|k| kit::numeric(k, 24))
        .ok_or_else(|| kit::schema("user has no rest_id"))?;
    let pick =
        |field: &str| string(&user["core"][field]).or_else(|| string(&user["legacy"][field]));
    let shown = pick("screen_name").ok_or_else(|| kit::schema("user has no screen_name"))?;
    if !shown.eq_ignore_ascii_case(&name) {
        return Err(kit::schema("account identity mismatch"));
    }
    Ok(ResolvedAccount {
        native_account_key: key,
        display_name: pick("name"),
        profile_url: Some(format!("{ORIGIN}/{shown}")),
        evidence: json!({
            "source": "graphql.UserByScreenName",
            "statuses_count": user["legacy"]["statuses_count"],
            "protected": user["privacy"]["protected"].as_bool().or(user["legacy"]["protected"].as_bool()),
            "profile": trace_contracts::content::safe_metadata(&json!({
                "screen_name": shown,
                "description": user["legacy"]["description"],
                "verified": user["is_blue_verified"],
            })),
        }),
    })
}

/// A login shows as the session's user id in the home page's state. The web client's older
/// settings endpoint does not answer a web session, so the page is the proof; without a user
/// id the answer is "unknown", never "invalid".
pub(crate) fn probe(net: &dyn Transport) -> Result<ProbeAuthResult, PluginError> {
    let home = net::text(net, &format!("{ORIGIN}/"), api::page_headers(), true)?;
    let state = page::json_after(&home, "__INITIAL_STATE__=")
        .or_else(|| page::json_after(&home, "__INITIAL_STATE__ ="));
    if let Some(state) = &state
        && string(&state["session"]["user_id"]).is_some()
    {
        return Ok(ProbeAuthResult {
            state: "valid".into(),
            capability: json!({"login_proven": true, "source": "home.state", "media_access": "unknown"}),
        });
    }
    Ok(ProbeAuthResult {
        state: "unknown".into(),
        capability: json!({"login_proven": false, "source": "home.state", "state_read": state.is_some()}),
    })
}
