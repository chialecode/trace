//! Instagram accounts: the stable identity behind a name, and the login probe.
use crate::api::{self, Session};
use crate::link::parse_account;
use crate::web::{self, ORIGIN, Viewer};
use serde_json::{Value, json};
use trace_contracts::plugin::{PluginError, ProbeAuthResult, ResolvedAccount};
use trace_domain::errors::FailureClass;
use trace_site_support::error;
use trace_site_support::kit::{self, count, string};
use trace_site_support::runtime::Transport;

/// The number of posts the profile says it has.
pub(crate) fn media_count(user: &Value) -> Option<i64> {
    count(&user["media_count"]).or_else(|| count(&user["edge_owner_to_timeline_media"]["count"]))
}

pub(crate) fn id_of(user: &Value) -> Option<String> {
    string(&user["pk"])
        .or_else(|| user["pk"].as_i64().map(|n| n.to_string()))
        .or_else(|| string(&user["id"]))
}

pub(crate) fn resolve(net: &dyn Transport, input: &str) -> Result<ResolvedAccount, PluginError> {
    let name = parse_account(input).ok_or_else(|| {
        error(
            FailureClass::InvalidPayload,
            "not an Instagram account link or name",
        )
    })?;
    let html = api::fetch_page(net, &format!("/{name}/"))?;
    let session = Session::read(&html)?;
    let Some(profile_id) = session.profile_id.clone() else {
        return Err(if html.contains("this page isn") {
            error(
                FailureClass::SourceUnavailable,
                "Instagram has no such page",
            )
        } else if web::profile_error_page_in(&html) {
            error(
                FailureClass::SourceUnavailable,
                "Instagram shows its error page instead of this profile",
            )
        } else {
            kit::schema("page has no profile id")
        });
    };
    let user = api::profile(net, &session, &profile_id)?;
    let shown = string(&user["username"]).ok_or_else(|| kit::schema("profile has no username"))?;
    if !shown.eq_ignore_ascii_case(&name) || id_of(&user).is_some_and(|id| id != profile_id) {
        return Err(kit::schema("account identity mismatch"));
    }
    if user["is_private"] == true && user["friendship_status"]["following"] != true {
        return Err(kit::denied("the Instagram account is private"));
    }
    Ok(ResolvedAccount {
        native_account_key: profile_id,
        display_name: string(&user["full_name"]).or_else(|| Some(shown.clone())),
        profile_url: Some(format!("{ORIGIN}/{shown}/")),
        evidence: json!({
            "source": "graphql.PolarisProfilePageContentQuery",
            "media_count": media_count(&user),
            "private": user["is_private"],
            "profile": trace_contracts::content::safe_metadata(&json!({
                "username": shown,
                "biography": user["biography"],
                "verified": user["is_verified"],
                "follower_count": count(&user["follower_count"]),
            })),
        }),
    })
}

/// A login shows as the viewer ids in the home page's data. A redirect to the login page is
/// the same statement; a page that says nothing proves nothing.
pub(crate) fn probe(net: &dyn Transport) -> Result<ProbeAuthResult, PluginError> {
    let state = |state: &str, proven: bool| ProbeAuthResult {
        state: state.into(),
        capability: json!({"login_proven": proven, "source": "home.viewer", "media_access": "unknown"}),
    };
    let html = match api::fetch_page(net, "/") {
        Ok(html) => html,
        Err(e) if e.class == FailureClass::AuthRequired => return Ok(state("invalid", false)),
        Err(e) => return Err(e),
    };
    Ok(match web::viewer_in(&html) {
        Viewer::User => state("valid", true),
        Viewer::Anonymous => state("invalid", false),
        Viewer::Unknown => state("unknown", false),
    })
}
