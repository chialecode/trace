//! TikTok accounts: the stable identity behind a name, and the login probe.
use crate::api::{self, ORIGIN};
use crate::link::{is_short, parse_account};
use serde_json::json;
use trace_contracts::plugin::{PluginError, ProbeAuthResult, ResolvedAccount};
use trace_domain::errors::FailureClass;
use trace_site_support::kit::{self, net, string};
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

/// What the site says about a page's subject when it does not show it.
pub(crate) fn status(code: i64) -> Option<PluginError> {
    match code {
        0 => None,
        10202 | 10221 | 10204 | 10216 => Some(error(
            FailureClass::SourceUnavailable,
            "TikTok has no such page",
        )),
        10222..=10224 => Some(kit::denied("the TikTok page is private or restricted")),
        _ => Some(kit::schema("TikTok page status is not understood")),
    }
}

pub(crate) fn resolve(net: &dyn Transport, input: &str) -> Result<ResolvedAccount, PluginError> {
    let expanded = expand(net, input)?;
    let name = parse_account(&expanded).ok_or_else(|| {
        error(
            FailureClass::InvalidPayload,
            "not a TikTok account link or @name",
        )
    })?;
    let scope = api::scope(&api::html(net, &format!("/@{name}?lang=en"))?)?;
    let detail = &scope["webapp.user-detail"];
    if let Some(e) = status(detail["statusCode"].as_i64().unwrap_or(0)) {
        return Err(e);
    }
    let user = &detail["userInfo"]["user"];
    let key = string(&user["secUid"]).ok_or_else(|| kit::schema("profile has no secUid"))?;
    let shown = string(&user["uniqueId"]).ok_or_else(|| kit::schema("profile has no uniqueId"))?;
    if !shown.eq_ignore_ascii_case(&name) {
        return Err(kit::schema("account identity mismatch"));
    }
    Ok(ResolvedAccount {
        native_account_key: key,
        display_name: string(&user["nickname"]),
        profile_url: Some(format!("{ORIGIN}/@{shown}")),
        evidence: json!({
            "source": "profile.page",
            "video_count": detail["userInfo"]["stats"]["videoCount"],
            "private": user["privateAccount"],
            "profile": trace_contracts::content::safe_metadata(&json!({
                "unique_id": user["uniqueId"],
                "nickname": user["nickname"],
                "signature": user["signature"],
                "verified": user["verified"],
            })),
        }),
    })
}

/// A login shows as the current user in the page's application context. The absence of one
/// proves nothing (the page may simply not carry it), so it is "unknown", never "invalid".
pub(crate) fn probe(net: &dyn Transport) -> Result<ProbeAuthResult, PluginError> {
    let scope = api::scope(&api::html(net, "/?lang=en")?)?;
    let user = &scope["webapp.app-context"]["user"];
    if string(&user["uniqueId"])
        .or_else(|| string(&user["uid"]))
        .is_some()
    {
        return Ok(ProbeAuthResult {
            state: "valid".into(),
            capability: json!({"login_proven": true, "source": "app-context", "media_access": "unknown"}),
        });
    }
    Ok(ProbeAuthResult {
        state: "unknown".into(),
        capability: json!({"login_proven": false, "source": "app-context"}),
    })
}
