//! 账号：主页链接/mid 解析与登录探测。

use crate::{AdapterState, api};
use serde_json::{Value, json};
use std::time::{Duration, Instant};
use trace_contracts::plugin::{ProbeAuthResult, ResolveAccountParams, ResolvedAccount};
use trace_domain::errors::FailureClass;

/// mid 或 space 主页 URL → 平台身份；返回 mid 与 card 交叉核对。
pub(crate) fn resolve_account(
    state: &AdapterState,
    p: &ResolveAccountParams,
    deadline: Duration,
    started: Instant,
) -> Result<ResolvedAccount, trace_contracts::plugin::PluginError> {
    let Some(mid) = extract_mid(&p.input) else {
        return Err(trace_contracts::plugin::PluginError {
            class: FailureClass::InvalidPayload,
            message: "input is not a bilibili mid or space URL".into(),
            retry_after_ms: None,
            details: Value::Null,
        });
    };
    let url = format!("{}?mid={mid}", api::CARD_URL);
    let v = api::get_json(state, &url, false, deadline, started)?;
    let env = api::envelope(&v)?;
    let card_mid = env
        .data
        .pointer("/card/mid")
        .and_then(|m| {
            m.as_str()
                .map(str::to_string)
                .or_else(|| m.as_i64().map(|n| n.to_string()))
        })
        .ok_or_else(|| trace_contracts::plugin::PluginError {
            class: FailureClass::SchemaChanged,
            message: "card response missing card.mid".into(),
            retry_after_ms: None,
            details: Value::Null,
        })?;
    if card_mid != mid {
        return Err(trace_contracts::plugin::PluginError {
            class: FailureClass::SchemaChanged,
            message: "card mid mismatch with requested identity".into(),
            retry_after_ms: None,
            details: json!({"requested": mid, "returned": card_mid}),
        });
    }
    Ok(ResolvedAccount {
        native_account_key: mid.clone(),
        display_name: env
            .data
            .pointer("/card/name")
            .and_then(|n| n.as_str())
            .map(str::to_string),
        profile_url: Some(format!("https://space.bilibili.com/{mid}")),
        evidence: json!({"source": "card", "mid": mid,"profile":trace_contracts::content::safe_metadata(&env.data)}),
    })
}

/// 输入是纯数字 mid 或 space.bilibili.com/{mid} 链接。
fn extract_mid(input: &str) -> Option<String> {
    let t = input.trim();
    if !t.is_empty() && t.chars().all(|c| c.is_ascii_digit()) {
        return Some(t.to_string());
    }
    let idx = t.find("space.bilibili.com/")?;
    let rest = &t[idx + "space.bilibili.com/".len()..];
    let digits: String = rest.chars().take_while(|c| c.is_ascii_digit()).collect();
    if digits.is_empty() {
        None
    } else {
        Some(digits)
    }
}

pub(crate) fn probe_auth(
    state: &AdapterState,
    deadline: Duration,
    started: Instant,
) -> Result<ProbeAuthResult, trace_contracts::plugin::PluginError> {
    let v = api::get_json(state, api::NAV_URL, true, deadline, started)?;
    let env = api::envelope(&v)?;
    let is_login = env
        .data
        .get("isLogin")
        .and_then(|b| b.as_bool())
        .unwrap_or(false);
    Ok(ProbeAuthResult {
        state: if is_login {
            "authenticated"
        } else {
            "anonymous"
        }
        .into(),
        capability: json!({
            "wbi": true,
            "vip_status": env.data.pointer("/vipStatus").cloned().unwrap_or(Value::Null),
        }),
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn extract_mid_forms() {
        assert_eq!(extract_mid("12345").as_deref(), Some("12345"));
        assert_eq!(
            extract_mid("https://space.bilibili.com/94697/video").as_deref(),
            Some("94697")
        );
        assert_eq!(
            extract_mid("space.bilibili.com/94697/").as_deref(),
            Some("94697")
        );
        assert!(extract_mid("https://example.com/x").is_none());
        assert!(extract_mid("space.bilibili.com/").is_none());
    }
}
