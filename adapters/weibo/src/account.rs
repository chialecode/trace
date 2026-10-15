//! Weibo accounts: profile resolution from any link form, and the login probe.
use crate::api;
use crate::link::{Account, Target, parse};
use serde_json::json;
use trace_contracts::plugin::{PluginError, ProbeAuthResult, ResolvedAccount};
use trace_domain::errors::FailureClass;
use trace_site_support::kit::{self, net, page, string};
use trace_site_support::{error, id, runtime::Transport};

/// A short link (`t.cn/...`) is followed once through the site; any other input is returned.
pub(crate) fn expand(net: &dyn Transport, input: &str) -> Result<String, PluginError> {
    let is_short = url::Url::parse(input.trim())
        .ok()
        .and_then(|u| u.host_str().map(str::to_ascii_lowercase))
        .is_some_and(|h| h == "t.cn");
    if !is_short {
        return Ok(input.trim().to_owned());
    }
    let url = url::Url::parse(input.trim()).map_err(|_| kit::schema("short link"))?;
    if url.scheme() != "https" {
        return Err(error(FailureClass::InvalidPayload, "short links are https"));
    }
    net::final_url(net, url.as_str(), json!({"user-agent": crate::api::UA}))
}

pub(crate) fn resolve(net: &dyn Transport, input: &str) -> Result<ResolvedAccount, PluginError> {
    let expanded = expand(net, input)?;
    let Some(Target::Account(account)) = parse(&expanded) else {
        return Err(error(
            FailureClass::InvalidPayload,
            "not a Weibo account link or user id",
        ));
    };
    let (param, value) = match &account {
        Account::Uid(uid) => ("uid", uid.as_str()),
        Account::Custom(name) => ("custom", name.as_str()),
        Account::Nick(nick) => ("screen_name", nick.as_str()),
    };
    let v = api::ajax(net, "/ajax/profile/info", &[(param, value)])?;
    let user = &v["data"]["user"];
    let key = string(&user["idstr"])
        .or_else(|| id(&user["id"]))
        .filter(|k| kit::numeric(k, 20))
        .ok_or_else(|| kit::schema("profile has no user id"))?;
    if let Account::Uid(uid) = &account
        && *uid != key
    {
        return Err(kit::schema("account identity mismatch"));
    }
    Ok(ResolvedAccount {
        display_name: string(&user["screen_name"]),
        profile_url: Some(format!("https://weibo.com/u/{key}")),
        evidence: json!({
            "source": "ajax.profile.info",
            "statuses_count": user["statuses_count"],
            "profile": trace_contracts::content::safe_metadata(user),
        }),
        native_account_key: key,
    })
}

/// Whether the session is a login, in two steps, each checked on the real site against the same
/// request without the Cookie (2026-10-18):
///
/// 1. The desktop configuration (`/ajax/config/get_config`) sits behind the login wall: `-100`
///    is a visitor. Its `ok` 1 is not yet a proof, because the answer names no user.
/// 2. The home page names the signed-in user in its `window.$CONFIG` (`user.idstr`, the same as
///    `uid`); a visitor's home page is a redirect to the visitor login on `passport.weibo.com`.
///
/// Any other answer is "unknown", never a guess in either direction. Two answers prove nothing
/// and are not used: the group list (`/ajax/feed/allGroups`) answers a visitor with `ok` 1 and the
/// default groups, and the mobile host is another cookie domain.
pub(crate) fn probe(net: &dyn Transport) -> Result<ProbeAuthResult, PluginError> {
    const CONFIG: &str = "ajax.config.get_config";
    const HOME: &str = "home.config";
    let reply = match net::json(
        net,
        &format!("{}/ajax/config/get_config", api::BASE),
        api::headers(),
        true,
    ) {
        Ok(reply) => reply,
        Err(e) if e.class == FailureClass::AuthRequired => return Ok(anonymous(CONFIG)),
        Err(e) => return Err(e),
    };
    match reply["ok"].as_i64() {
        Some(1) => {}
        Some(-100) => return Ok(anonymous(CONFIG)),
        _ => return Ok(unknown("no configuration answer")),
    }
    let (home, served_from) =
        net::page(net, &format!("{}/", api::BASE), api::page_headers(), true)?;
    let served_host = url::Url::parse(&served_from)
        .ok()
        .and_then(|u| u.host_str().map(str::to_ascii_lowercase));
    if served_host.as_deref() == Some("passport.weibo.com") {
        return Ok(anonymous(HOME));
    }
    let Some(config) = page::json_after(&home, "window.$CONFIG") else {
        return Ok(unknown("home page has no configuration"));
    };
    let number = |v: &serde_json::Value| {
        string(v)
            .or_else(|| id(v))
            .filter(|k| k != "0" && kit::numeric(k, 20))
    };
    let user = match &config["user"]["idstr"] {
        serde_json::Value::Null => number(&config["user"]["id"]),
        idstr => number(idstr),
    };
    let uid = number(&config["uid"]);
    Ok(match user {
        Some(user) if uid.as_ref().is_none_or(|uid| *uid == user) => ProbeAuthResult {
            state: "valid".into(),
            capability: json!({"login_proven": true, "source": HOME, "media_access": "unknown"}),
        },
        _ => unknown("home page names no user"),
    })
}

fn anonymous(source: &str) -> ProbeAuthResult {
    ProbeAuthResult {
        state: "anonymous".into(),
        capability: json!({"login_proven": false, "source": source}),
    }
}

fn unknown(source: &str) -> ProbeAuthResult {
    ProbeAuthResult {
        state: "unknown".into(),
        capability: json!({"login_proven": false, "source": source}),
    }
}
