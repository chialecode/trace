//! Account: profile resolution from ids and share links, login probe.
use crate::api::{BASE, UA, api, fail};
use serde_json::{Value, json};
use trace_contracts::plugin::PluginError;
use trace_site_support::{id, runtime::Client};

pub(crate) fn resolve(client: &Client, params: &Value) -> Result<Value, PluginError> {
    let input = params["input"]
        .as_str()
        .ok_or_else(|| fail("missing account input"))?;
    let key = account_key(client, input)?;
    let v = api(
        client,
        "/aweme/v1/web/user/profile/other/",
        &[("sec_user_id", &key)],
    )?;
    let u = &v["user"];
    if u["sec_uid"].as_str() != Some(&key) {
        return Err(fail("account identity mismatch"));
    }
    Ok(json!({
        "native_account_key": key,
        "display_name": u["nickname"],
        "profile_url": format!("{BASE}/user/{key}"),
        "evidence": {"uid":id(&u["uid"]), "unique_id":id(&u["unique_id"]), "short_id":id(&u["short_id"]), "source":"user.profile.other", "biography":u["signature"], "profile":trace_contracts::content::safe_metadata(u)},
    }))
}

pub(crate) fn probe_auth(client: &Client) -> Result<Value, PluginError> {
    // The self endpoint resolves only the caller's session. query/user
    // also assigns guest IDs and must not be used as login evidence.
    let v = api(client, "/aweme/v1/web/user/profile/self/", &[])?;
    let logged_in = id(&v["user"]["uid"])
        .is_some_and(|s| !s.is_empty() && s != "0" && s.bytes().all(|b| b.is_ascii_digit()))
        && v["user"]["sec_uid"].as_str().is_some_and(|s| !s.is_empty());
    Ok(
        json!({"state":if logged_in {"valid"}else{"unknown"},"capability":{"login_proven":logged_in,"source":"user.profile.self","media_access":"unknown"}}),
    )
}

fn account_key(client: &Client, input: &str) -> Result<String, PluginError> {
    if input.starts_with("MS4")
        && input.len() <= 256
        && input
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_')
    {
        return Ok(input.into());
    }
    let mut url = url::Url::parse(input).map_err(|_| fail("invalid account input"))?;
    if url.scheme() != "https" || !url.username().is_empty() || url.password().is_some() {
        return Err(fail("invalid account URL"));
    }
    if url.host_str() == Some("v.douyin.com") {
        let reply = client.fetch("HEAD", url.as_str(), json!({"user-agent":UA}), false)?;
        if reply.status != 200 {
            return Err(fail("short link resolution failed"));
        }
        url =
            url::Url::parse(&reply.final_url).map_err(|_| fail("short link final URL missing"))?;
    }
    if !matches!(url.host_str(), Some("www.douyin.com" | "www.iesdouyin.com")) {
        return Err(fail("short link left expected site"));
    }
    let parts = url
        .path_segments()
        .ok_or_else(|| fail("invalid account path"))?
        .filter(|s| !s.is_empty())
        .collect::<Vec<_>>();
    if let Some(i) = parts.iter().position(|p| *p == "user") {
        let key = parts
            .get(i + 1)
            .filter(|k| k.starts_with("MS4"))
            .ok_or_else(|| fail("stable account id missing"))?;
        return Ok((*key).to_owned());
    }
    if let Some(i) = parts.iter().position(|p| matches!(*p, "video" | "note")) {
        let key = parts
            .get(i + 1)
            .filter(|k| k.bytes().all(|b| b.is_ascii_digit()))
            .ok_or_else(|| fail("short link content id missing"))?;
        let detail = api(client, "/aweme/v1/web/aweme/detail/", &[("aweme_id", key)])?;
        return id(&detail["aweme_detail"]["author"]["sec_uid"])
            .ok_or_else(|| fail("short link author identity unavailable"));
    }
    Err(fail("expected user or content share URL"))
}
