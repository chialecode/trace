//! 跨语言契约黄金样本测试。
//!
//! 每个样本先按类型化 DTO 反序列化再重新序列化，断言语义字段不丢失；
//! 样本覆盖成功、冲突、未知可选字段、大整数、空值与错误（计划 §3）。
#![allow(clippy::too_many_lines)]

use trace_contracts::control::{ApiError, ErrorCode, Request, Response};
use trace_contracts::plugin::{PluginError, PluginRequest, PluginResponse};
use trace_domain::errors::FailureClass;

fn parse<T: serde::de::DeserializeOwned>(raw: &'static str) -> T {
    serde_json::from_str(raw).expect("golden sample must parse")
}

#[test]
fn control_request_success_roundtrip() {
    let v: serde_json::Value = parse(include_str!("golden/control_request_success.json"));
    let req: Request = serde_json::from_value(v.clone()).expect("typed request");
    assert_eq!(req.command, "subscription.update");
    assert_eq!(req.idempotency_key.as_deref(), Some("mut-20260926-0001"));
    assert_eq!(req.if_revision, Some(12));
    let back = serde_json::to_value(&req).unwrap();
    assert_eq!(back, v);
}

#[test]
fn control_response_error_and_conflict() {
    let err_v: serde_json::Value = parse(include_str!("golden/control_response_error.json"));
    let resp: Response = serde_json::from_value(err_v.clone()).unwrap();
    let err = resp.error.clone().expect("error present");
    assert_eq!(err.code, ErrorCode::AuthRequired);
    assert!(err.user_action.is_some());
    let back = serde_json::to_value(&resp).unwrap();
    assert_eq!(back, err_v);

    let conflict_v: serde_json::Value =
        parse(include_str!("golden/control_response_conflict.json"));
    let resp2: Response = serde_json::from_value(conflict_v.clone()).unwrap();
    assert_eq!(
        resp2.error.as_ref().map(|e| e.code),
        Some(ErrorCode::VersionConflict)
    );
    assert_eq!(serde_json::to_value(&resp2).unwrap(), conflict_v);
}

#[test]
fn control_unknown_optional_field_is_tolerated() {
    let v: serde_json::Value = parse(include_str!("golden/control_request_unknown_field.json"));
    let req: Request = serde_json::from_value(v).expect("unknown optional args keys ignored");
    assert_eq!(req.command, "creator.create");
}

#[test]
fn segment_validation_roundtrip_and_legacy_defaults() {
    use trace_contracts::plugin::UrlSet;
    let legacy = serde_json::json!({"primary":"https://media.example/one","backups":[]});
    let urls: UrlSet = serde_json::from_value(legacy.clone()).unwrap();
    assert!(urls.expected_size.is_none() && urls.expected_sha256.is_none());
    assert_eq!(serde_json::to_value(urls).unwrap(), legacy);
    let current = serde_json::json!({"primary":"https://media.example/one","backups":[],"expected_size":7,"expected_sha256":"a".repeat(64)});
    let urls: UrlSet = serde_json::from_value(current.clone()).unwrap();
    assert_eq!(serde_json::to_value(urls).unwrap(), current);
}

#[test]
fn control_big_ids_and_nulls() {
    let v: serde_json::Value = parse(include_str!("golden/control_request_big_null.json"));
    let req: Request = serde_json::from_value(v).unwrap();
    let args = req.args;
    assert_eq!(
        args["creator_id"].as_str().unwrap().len(),
        36,
        "creator_id stays a string"
    );
    assert!(args["notes"].is_null());
}

#[test]
fn plugin_request_and_response_roundtrip() {
    let req_v: serde_json::Value = parse(include_str!("golden/plugin_request_enumerate.json"));
    let req: PluginRequest = serde_json::from_value(req_v.clone()).unwrap();
    assert_eq!(req.method, "enumerate");
    assert_eq!(serde_json::to_value(&req).unwrap(), req_v);

    let resp_v: serde_json::Value = parse(include_str!("golden/plugin_response_bigint.json"));
    let resp: PluginResponse = serde_json::from_value(resp_v.clone()).unwrap();
    let result = resp.result.clone().unwrap();
    assert_eq!(
        result["items"][0]["stats"]["view"], 9007199254740993i64,
        "stats remain inside opaque Value; consumers must treat as string when crossing JS"
    );
    assert_eq!(serde_json::to_value(&resp).unwrap(), resp_v);
}

#[test]
fn plugin_error_carries_class_not_raw_body() {
    let err = PluginError {
        class: FailureClass::RateLimited,
        message: "rate limited by adapter".into(),
        retry_after_ms: Some(2000),
        details: serde_json::json!({"http_status": 412}),
    };
    let json = serde_json::to_string(&err).unwrap();
    assert!(json.contains("rate_limited"));
    assert!(!json.contains("Cookie"), "no raw bodies in error contract");
    let _ = ApiError {
        code: ErrorCode::RateLimited,
        retryable: true,
        user_action: Some("稍后再试".into()),
        related_ids: vec![],
        details: serde_json::json!({}),
    };
}

#[test]
fn proxy_fetch_params_roundtrip() {
    use trace_contracts::plugin::{ProxyFetchParams, ProxyFetchResult};
    let p = ProxyFetchParams {
        method: "GET".into(),
        url: "https://api.example.com/x?wts=1".into(),
        headers: serde_json::json!({"referer": "https://www.example.com/"}),
        max_bytes: 1024,
        with_credential: true,
        body: None,
    };
    let json = serde_json::to_string(&p).unwrap();
    let back: ProxyFetchParams = serde_json::from_str(&json).unwrap();
    assert_eq!(back.max_bytes, 1024);

    let r = ProxyFetchResult {
        status: 200,
        headers: serde_json::json!({}),
        body_b64: "aGVsbG8=".into(),
        final_url: "https://api.example.com/x?wts=1".into(),
    };
    let json = serde_json::to_string(&r).unwrap();
    assert!(json.contains("body_b64"));
}

#[test]
fn timeline_additive_fields_roundtrip_and_legacy_defaults() {
    use trace_contracts::control::{ArchiveTimelineArgs, TimelinePageDto};
    let sample: serde_json::Value = parse(include_str!("golden/timeline_page.json"));
    let page: TimelinePageDto = serde_json::from_value(sample.clone()).unwrap();
    assert_eq!(serde_json::to_value(page).unwrap(), sample);
    let args: ArchiveTimelineArgs =
        serde_json::from_value(serde_json::json!({"limit":50,"future_optional":true})).unwrap();
    assert!(args.query.is_none());
    assert!(!args.oldest);
    let page: TimelinePageDto =
        serde_json::from_value(serde_json::json!({"items":[],"next_cursor":null})).unwrap();
    assert!(page.counts.is_none());
}
