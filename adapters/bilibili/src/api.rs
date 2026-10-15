//! 站点 API 访问：经宿主 proxy.fetch 的受限取数与失败分类。
//!
//! Cookie 由宿主按账号附加，本进程只声明 with_credential；HTTP 状态与
//! 业务码分别分类，未知码不盲猜语义（bilibili-capabilities §2）。

use crate::AdapterState;
use serde_json::{Value, json};
use std::time::{Duration, Instant};
use trace_contracts::plugin::{PluginError, ProxyFetchResult};
use trace_domain::errors::FailureClass;
use trace_site_support::runtime::Fetch;

pub const UA: &str = trace_site_support::kit::SIGNED_UA;
pub const REFERER: &str = "https://www.bilibili.com/";

/// Headers the media CDN expects on public (credential-free) fetches.
pub(crate) fn public_headers() -> Value {
    json!({"referer": REFERER, "user-agent": UA})
}

pub const NAV_URL: &str = "https://api.bilibili.com/x/web-interface/nav";
pub const CARD_URL: &str = "https://api.bilibili.com/x/web-interface/card";
pub const ARC_SEARCH_URL: &str = "https://api.bilibili.com/x/space/wbi/arc/search";
pub const VIEW_URL: &str = "https://api.bilibili.com/x/web-interface/wbi/view";
pub const PLAYURL_URL: &str = "https://api.bilibili.com/x/player/wbi/playurl";

/// 经宿主受限代理执行 GET 并返回结构化响应。
pub fn proxy_get(
    state: &AdapterState,
    url: &str,
    with_credential: bool,
    max_bytes: u32,
    deadline: Duration,
    started: Instant,
) -> Result<ProxyFetchResult, PluginError> {
    proxy_request(
        state,
        url,
        with_credential,
        max_bytes,
        deadline,
        started,
        "GET",
    )
}
pub fn proxy_head(
    state: &AdapterState,
    url: &str,
    deadline: Duration,
    started: Instant,
) -> Result<ProxyFetchResult, PluginError> {
    proxy_request(state, url, false, 512 * 1024, deadline, started, "HEAD")
}
fn proxy_request(
    state: &AdapterState,
    url: &str,
    with_credential: bool,
    max_bytes: u32,
    deadline: Duration,
    started: Instant,
    method: &str,
) -> Result<ProxyFetchResult, PluginError> {
    let host = url::Url::parse(url)
        .ok()
        .and_then(|u| u.host_str().map(str::to_owned));
    let referer = if host.as_deref() == Some("api.live.bilibili.com") {
        "https://live.bilibili.com/"
    } else {
        REFERER
    };
    state.client.request(&Fetch {
        method,
        url,
        headers: json!({"referer": referer, "user-agent": UA}),
        credential: with_credential,
        body: None,
        max_bytes,
        timeout: deadline.saturating_sub(started.elapsed()),
    })
}

use base64::Engine;

/// GET 并解析 JSON 响应体。
pub fn get_json(
    state: &AdapterState,
    url: &str,
    with_credential: bool,
    deadline: Duration,
    started: Instant,
) -> Result<Value, PluginError> {
    let resp = proxy_get(
        state,
        url,
        with_credential,
        8 * 1024 * 1024,
        deadline,
        started,
    )?;
    if resp.status != 200 {
        return Err(classify_http(resp.status));
    }
    let body = base64::engine::general_purpose::STANDARD
        .decode(&resp.body_b64)
        .map_err(|e| PluginError {
            class: FailureClass::InvalidPayload,
            message: format!("proxy body not base64: {e}"),
            retry_after_ms: None,
            details: Value::Null,
        })?;
    serde_json::from_slice(&body).map_err(|e| PluginError {
        class: FailureClass::SchemaChanged,
        message: format!("response is not JSON: {e}"),
        retry_after_ms: None,
        details: Value::Null,
    })
}

/// 站点 API 信封：code=0 且结构可用才算成功；v_voucher 视为挑战。
#[derive(Debug)]
pub struct ApiEnvelope {
    pub data: Value,
}

pub fn envelope(v: &Value) -> Result<ApiEnvelope, PluginError> {
    let code = v.get("code").and_then(|c| c.as_i64());
    let Some(code) = code else {
        return Err(PluginError {
            class: FailureClass::SchemaChanged,
            message: "api response missing code".into(),
            retry_after_ms: None,
            details: Value::Null,
        });
    };
    if code == 0 {
        let data = v.get("data").cloned().unwrap_or(Value::Null);
        if data.get("v_voucher").is_some() {
            // 成功码内嵌挑战：进入等待用户，不当作数据成功（issue 613 证据）。
            return Err(PluginError {
                class: FailureClass::PermissionDenied,
                message: "risk control challenge (v_voucher) despite code=0".into(),
                retry_after_ms: None,
                details: json!({"challenge": "v_voucher"}),
            });
        }
        return Ok(ApiEnvelope { data });
    }
    Err(classify_api_code(
        code,
        v.get("message").and_then(|m| m.as_str()).unwrap_or(""),
    ))
}

/// HTTP 状态分类（受限代理只放行 200 到这里）。
pub(crate) fn classify_http(status: u16) -> PluginError {
    let class = match status {
        401 | 403 => FailureClass::PermissionDenied,
        412 | 429 => FailureClass::RateLimited,
        404 => FailureClass::SourceUnavailable,
        500..=599 => FailureClass::TransientNetwork,
        _ => FailureClass::Unknown,
    };
    PluginError {
        class,
        message: format!("http status {status}"),
        retry_after_ms: None,
        details: Value::Null,
    }
}

/// 业务码分类：已证明语义映射；未知码不盲猜（capabilities §2 风控行）。
pub fn classify_api_code(code: i64, _message: &str) -> PluginError {
    let (class, retry_after) = match code {
        -101 => (FailureClass::AuthRequired, None),
        -412 => (FailureClass::RateLimited, None),
        -352 => (FailureClass::PermissionDenied, None), // 风控校验，需用户动作
        -404 => (FailureClass::SourceUnavailable, None),
        -403 => (FailureClass::PermissionDenied, None),
        -509 => (FailureClass::RateLimited, Some(60_000u64)),
        -799 => (FailureClass::RateLimited, Some(60_000u64)),
        _ => (FailureClass::Unknown, None),
    };
    PluginError {
        class,
        message: format!("api code {code}"),
        retry_after_ms: retry_after,
        details: json!({ "api_code": code }),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn parse(v: Value) -> Result<ApiEnvelope, PluginError> {
        envelope(&v)
    }

    #[test]
    fn code_zero_without_voucher_is_ok() {
        let e = parse(json!({"code": 0, "data": {"a": 1}})).unwrap();
        assert_eq!(e.data["a"], 1);
    }

    #[test]
    fn voucher_inside_success_is_challenge() {
        let err = parse(json!({"code": 0, "data": {"v_voucher": "challenge-x"}})).unwrap_err();
        assert_eq!(err.class, FailureClass::PermissionDenied);
        assert_eq!(err.details["challenge"], "v_voucher");
    }

    #[test]
    fn known_codes_map_to_proven_classes() {
        assert_eq!(
            classify_api_code(-101, "").class,
            FailureClass::AuthRequired
        );
        assert_eq!(classify_api_code(-412, "").class, FailureClass::RateLimited);
        assert_eq!(
            classify_api_code(-352, "").class,
            FailureClass::PermissionDenied
        );
        assert_eq!(
            classify_api_code(-404, "").class,
            FailureClass::SourceUnavailable
        );
    }

    #[test]
    fn unknown_code_stays_unknown() {
        let e = classify_api_code(-777, "mystery");
        assert_eq!(e.class, FailureClass::Unknown);
        assert_eq!(e.details["api_code"], -777);
    }
}
