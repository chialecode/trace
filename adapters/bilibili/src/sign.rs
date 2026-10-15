//! 请求级 WBI 签名：密钥获取与缓存、签名 GET、失效后刷新一次。
//! 算法本身（混淆 key、签名向量）在 `wbi`。

use crate::{AdapterState, api, wbi};
use serde_json::{Value, json};
use std::time::{Duration, Instant};
use trace_domain::errors::FailureClass;

fn wbi_error(e: &trace_contracts::plugin::PluginError) -> bool {
    e.class == FailureClass::PermissionDenied || e.details.get("api_code") == Some(&json!(-352))
}

/// 从 nav 响应提取 wbi 密钥对并导出混淆 key。
pub fn wbi_key_from_nav(nav_data: &Value) -> Option<String> {
    let img_url = nav_data.pointer("/wbi_img/img_url")?.as_str()?;
    let sub_url = nav_data.pointer("/wbi_img/sub_url")?.as_str()?;
    let img = img_url.rsplit('/').next()?.split('.').next()?;
    let sub = sub_url.rsplit('/').next()?.split('.').next()?;
    if [img, sub]
        .into_iter()
        .any(|s| s.len() != 32 || !s.bytes().all(|b| b.is_ascii_hexdigit()))
    {
        return None;
    }
    Some(wbi::mixin_key(img, sub))
}

fn wbi_key_from_response(v: &Value) -> Result<String, trace_contracts::plugin::PluginError> {
    // Logged-out nav still publishes WBI keys. That explicit envelope is only
    // useful for signing; it is never accepted by probe_auth as a login proof.
    let data = if v["code"] == -101
        && v["data"]["isLogin"] == false
        && v["data"].get("v_voucher").is_none()
    {
        v["data"].clone()
    } else {
        api::envelope(v)?.data
    };
    wbi_key_from_nav(&data).ok_or_else(|| trace_contracts::plugin::PluginError {
        class: FailureClass::SchemaChanged,
        message: "nav response missing valid wbi_img keys".into(),
        retry_after_ms: None,
        details: Value::Null,
    })
}

/// 取缓存内的混淆 key；不新鲜则拉一次 nav。
fn ensure_wbi_key(
    state: &AdapterState,
    deadline: Duration,
    started: Instant,
) -> Result<String, trace_contracts::plugin::PluginError> {
    {
        let cache = state.wbi.lock().unwrap();
        if cache.fresh() {
            return Ok(cache.key.clone().unwrap_or_default());
        }
    }
    let v = api::get_json(state, api::NAV_URL, true, deadline, started)?;
    let key = wbi_key_from_response(&v)?;
    state.wbi.lock().unwrap().key = Some(key.clone());
    state.wbi.lock().unwrap().fetched_at = Some(Instant::now());
    Ok(key)
}

/// WBI 签名 GET；签名失效刷新密钥重试一次（不无限重试）。
pub(crate) fn signed_get(
    state: &AdapterState,
    base: &str,
    params: &[(String, String)],
    with_credential: bool,
    deadline: Duration,
    started: Instant,
) -> Result<Value, trace_contracts::plugin::PluginError> {
    let mut refreshed = false;
    loop {
        let mixin = ensure_wbi_key(state, deadline, started)?;
        let wts = crate::clock::unix_now();
        let url = wbi::signed_url(base, params, &mixin, wts);
        match api::get_json(state, &url, with_credential, deadline, started) {
            Ok(v) => {
                let env = api::envelope(&v)?;
                return Ok(env.data);
            }
            Err(e) if !refreshed && wbi_error(&e) => {
                {
                    let mut cache = state.wbi.lock().unwrap();
                    cache.key = None;
                    cache.fetched_at = None;
                }
                refreshed = true;
            }
            Err(e) => return Err(e),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn wbi_key_from_nav_fixture() {
        let nav: Value =
            serde_json::from_str(include_str!("../../../tests/fixtures/bilibili/nav.json"))
                .unwrap();
        let key = wbi_key_from_nav(&nav["data"]).unwrap();
        // 固定夹具密钥 → 已知混淆 key。
        assert_eq!(key, "ea1db124af3c7062474693fa704f4ff8");
    }

    #[test]
    fn public_wbi_keys_do_not_prove_login_or_bypass_challenges() {
        let mut guest: Value =
            serde_json::from_str(include_str!("../../../tests/fixtures/bilibili/nav.json"))
                .unwrap();
        guest["code"] = json!(-101);
        assert_eq!(guest["data"]["isLogin"], false);
        assert_eq!(
            wbi_key_from_response(&guest).unwrap(),
            "ea1db124af3c7062474693fa704f4ff8"
        );
        assert_eq!(
            api::envelope(&guest).unwrap_err().class,
            FailureClass::AuthRequired
        );
        for code in [-403, -352] {
            let mut denied = guest.clone();
            denied["code"] = json!(code);
            assert!(wbi_key_from_response(&denied).is_err());
        }
        let mut inconsistent = guest.clone();
        inconsistent["data"]["isLogin"] = json!(true);
        assert!(wbi_key_from_response(&inconsistent).is_err());
        for code in [0, -101] {
            let mut challenge = guest.clone();
            challenge["code"] = json!(code);
            challenge["data"]["v_voucher"] = json!("fixture-challenge");
            assert!(wbi_key_from_response(&challenge).is_err());
        }
        for value in [Value::Null, json!("https://i0.hdslb.com/bfs/wbi/short.png")] {
            let mut malformed = guest.clone();
            malformed["data"]["wbi_img"]["img_url"] = value;
            assert_eq!(
                wbi_key_from_response(&malformed).unwrap_err().class,
                FailureClass::SchemaChanged
            );
        }
    }
}
