//! Versioned capability contracts for optional plugin extensions (stage plan §11.9).
//!
//! Each kind is separate and small; a plugin declares the kinds it implements in its manifest,
//! and the host routes only the methods the matching contract lists. Site adapters keep their
//! existing `resolve_account`/`enumerate`/`observe`/`resolve_media`/`probe_auth` contracts and
//! are not forced into these extensions.
use serde_json::Value;

#[derive(Clone, Copy, PartialEq, Eq)]
pub enum ResultType {
    Text,
    Number,
    Bool,
    Object,
    Array,
}

pub struct ResultField {
    pub name: &'static str,
    pub required: bool,
    pub kind: ResultType,
}

pub struct CapabilityMethod {
    pub name: &'static str,
    /// Parameter names the host accepts; other keys are refused before anything is sent.
    pub params: &'static [&'static str],
    /// Result names the host passes on; anything else is refused.
    pub results: &'static [&'static str],
    /// Required fields and their JSON types; a null, scalar or array where the contract names
    /// an object is refused (F-075).
    pub result_fields: &'static [ResultField],
    pub timeout_ms: u64,
    pub max_bytes: usize,
}

pub struct CapabilityContract {
    pub kind: &'static str,
    pub version: &'static str,
    pub methods: &'static [CapabilityMethod],
    /// Largest parameter/result document the host forwards for this kind.
    pub max_bytes: usize,
}

const LOGIN_STATE: &[ResultField] = &[
    ResultField {
        name: "state",
        required: true,
        kind: ResultType::Text,
    },
    ResultField {
        name: "prompt",
        required: false,
        kind: ResultType::Text,
    },
    ResultField {
        name: "step",
        required: false,
        kind: ResultType::Text,
    },
];

const LOGIN_ASSIST: &[CapabilityMethod] = &[
    CapabilityMethod {
        name: "begin",
        params: &["site", "locale"],
        results: &["state", "prompt", "step"],
        result_fields: LOGIN_STATE,
        timeout_ms: 10_000,
        max_bytes: 16 * 1024,
    },
    CapabilityMethod {
        name: "status",
        params: &["handle"],
        results: &["state", "prompt", "step"],
        result_fields: LOGIN_STATE,
        timeout_ms: 5_000,
        max_bytes: 16 * 1024,
    },
    CapabilityMethod {
        name: "cancel",
        params: &["handle"],
        results: &["cancelled"],
        result_fields: &[ResultField {
            name: "cancelled",
            required: true,
            kind: ResultType::Bool,
        }],
        timeout_ms: 5_000,
        max_bytes: 4 * 1024,
    },
];

const NOTIFICATION: &[CapabilityMethod] = &[CapabilityMethod {
    name: "send",
    params: &["event", "title", "summary", "count"],
    results: &["delivered"],
    result_fields: &[ResultField {
        name: "delivered",
        required: true,
        kind: ResultType::Bool,
    }],
    timeout_ms: 5_000,
    max_bytes: 16 * 1024,
}];

const STORAGE_PREPARE: &[ResultField] = &[
    ResultField {
        name: "handle",
        required: true,
        kind: ResultType::Text,
    },
    ResultField {
        name: "max_bytes",
        required: true,
        kind: ResultType::Number,
    },
];

const STORAGE: &[CapabilityMethod] = &[
    CapabilityMethod {
        name: "prepare",
        params: &["kind_id", "expected_bytes"],
        results: &["handle", "max_bytes"],
        result_fields: STORAGE_PREPARE,
        timeout_ms: 5_000,
        max_bytes: 4 * 1024,
    },
    CapabilityMethod {
        name: "write",
        params: &["handle", "offset", "chunk_b64"],
        results: &["written"],
        result_fields: &[ResultField {
            name: "written",
            required: true,
            kind: ResultType::Number,
        }],
        timeout_ms: 30_000,
        max_bytes: 4 * 1024 * 1024,
    },
    CapabilityMethod {
        name: "finish",
        params: &["handle", "sha256"],
        results: &["derived_id", "size"],
        result_fields: &[
            ResultField {
                name: "derived_id",
                required: true,
                kind: ResultType::Text,
            },
            ResultField {
                name: "size",
                required: true,
                kind: ResultType::Number,
            },
        ],
        timeout_ms: 30_000,
        max_bytes: 4 * 1024,
    },
    CapabilityMethod {
        name: "discard",
        params: &["handle"],
        results: &["discarded"],
        result_fields: &[ResultField {
            name: "discarded",
            required: true,
            kind: ResultType::Bool,
        }],
        timeout_ms: 5_000,
        max_bytes: 4 * 1024,
    },
];

const CONTENT_PROCESSING: &[CapabilityMethod] = &[
    CapabilityMethod {
        name: "process",
        params: &["input_kind", "output_kind", "bytes", "options"],
        results: &["derived_id", "output_kind", "bytes"],
        result_fields: &[
            ResultField {
                name: "derived_id",
                required: true,
                kind: ResultType::Text,
            },
            ResultField {
                name: "output_kind",
                required: true,
                kind: ResultType::Text,
            },
            ResultField {
                name: "bytes",
                required: true,
                kind: ResultType::Number,
            },
        ],
        timeout_ms: 120_000,
        max_bytes: 64 * 1024,
    },
    CapabilityMethod {
        name: "cancel",
        params: &["handle"],
        results: &["cancelled"],
        result_fields: &[ResultField {
            name: "cancelled",
            required: true,
            kind: ResultType::Bool,
        }],
        timeout_ms: 5_000,
        max_bytes: 4 * 1024,
    },
];

pub const CAPABILITIES: [CapabilityContract; 4] = [
    CapabilityContract {
        kind: "login_assist",
        version: "1.0",
        methods: LOGIN_ASSIST,
        max_bytes: 64 * 1024,
    },
    CapabilityContract {
        kind: "notification",
        version: "1.0",
        methods: NOTIFICATION,
        max_bytes: 64 * 1024,
    },
    CapabilityContract {
        kind: "storage",
        version: "1.0",
        methods: STORAGE,
        max_bytes: 8 * 1024 * 1024,
    },
    CapabilityContract {
        kind: "content_processing",
        version: "1.0",
        methods: CONTENT_PROCESSING,
        max_bytes: 256 * 1024,
    },
];

pub fn contract(kind: &str) -> Option<&'static CapabilityContract> {
    CAPABILITIES.iter().find(|c| c.kind == kind)
}

pub fn method(kind: &str, name: &str) -> Option<&'static CapabilityMethod> {
    contract(kind)?.methods.iter().find(|m| m.name == name)
}

fn forbidden_key(key: &str) -> bool {
    let key = key.to_ascii_lowercase();
    [
        "cookie",
        "token",
        "session",
        "password",
        "secret",
        "credential",
        "authorization",
    ]
    .iter()
    .any(|word| key.contains(word))
}

/// Absolute paths and archive destinations never cross a capability boundary.
fn path_like(value: &str) -> bool {
    value.starts_with('/')
        || value.starts_with('\\')
        || value.contains(":\\")
        || value.contains(":/")
        || value.contains("..\\")
        || value.contains("../")
}

fn check_value(kind: &str, value: &Value) -> Result<(), String> {
    match value {
        Value::String(text) => {
            if kind != "content_processing" && path_like(text) {
                return Err("absolute paths are not accepted".into());
            }
            if text.len() > 4096 {
                return Err("value too long".into());
            }
            Ok(())
        }
        Value::Array(items) => {
            for item in items.iter().take(256) {
                check_value(kind, item)?;
            }
            Ok(())
        }
        Value::Object(map) => {
            for (key, value) in map.iter().take(64) {
                if forbidden_key(key) {
                    return Err(format!("field {key} is not part of the contract"));
                }
                check_value(kind, value)?;
            }
            Ok(())
        }
        _ => Ok(()),
    }
}

/// Validates one call against its contract: declared method, declared parameter names, no
/// credential-shaped key, no absolute path, bounded size.
pub fn validate(
    kind: &str,
    method_name: &str,
    params: &Value,
) -> Result<&'static CapabilityMethod, String> {
    let Some(m) = method(kind, method_name) else {
        return Err(format!("{kind} does not declare {method_name}"));
    };
    let object = params
        .as_object()
        .ok_or_else(|| "params must be an object".to_owned())?;
    for (key, value) in object {
        if !m.params.contains(&key.as_str()) {
            return Err(format!("{kind}.{method_name} does not accept {key}"));
        }
        if forbidden_key(key) {
            return Err(format!("{key} is not a capability parameter"));
        }
        check_value(kind, value)?;
    }
    if params.to_string().len() > contract(kind).map(|c| c.max_bytes).unwrap_or(0) {
        return Err("params exceed the contract size".into());
    }
    Ok(m)
}

/// Validates the shape of one capability result: an object, only declared fields, every
/// required field present, and each field of the declared JSON type. A null, a bare array or a
/// scalar does not satisfy a contract that names fields (F-075).
pub fn validate_result(kind: &str, method_name: &str, result: &Value) -> Result<(), String> {
    let Some(m) = method(kind, method_name) else {
        return Err(format!("{kind} does not declare {method_name}"));
    };
    let object = result
        .as_object()
        .ok_or_else(|| format!("{kind}.{method_name} result must be an object"))?;
    for key in object.keys() {
        if !m.results.contains(&key.as_str()) {
            return Err(format!(
                "{kind}.{method_name} result has undeclared field {key}"
            ));
        }
    }
    for field in m.result_fields {
        let Some(value) = object.get(field.name) else {
            if field.required {
                return Err(format!(
                    "{kind}.{method_name} result is missing {}",
                    field.name
                ));
            }
            continue;
        };
        let matches = match field.kind {
            ResultType::Text => value.is_string(),
            ResultType::Number => value.is_number(),
            ResultType::Bool => value.is_boolean(),
            ResultType::Object => value.is_object(),
            ResultType::Array => value.is_array(),
        };
        if !matches {
            return Err(format!(
                "{kind}.{method_name} result field {} has the wrong type",
                field.name
            ));
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn capability_routes_only_declared_methods() {
        assert!(
            validate(
                "notification",
                "send",
                &json!({"event": "job.failed", "count": 1})
            )
            .is_ok()
        );
        assert!(validate("notification", "spam", &json!({})).is_err());
        assert!(validate("login_assist", "begin", &json!({"site": "weibo"})).is_ok());
        assert!(
            validate(
                "login_assist",
                "begin",
                &json!({"site": "weibo", "cookie": "x"})
            )
            .is_err()
        );
    }

    #[test]
    fn capability_result_shapes_are_checked_for_every_kind() {
        for (kind, method, good) in [
            (
                "login_assist",
                "begin",
                json!({"state": "waiting", "prompt": "scan", "step": "1"}),
            ),
            ("login_assist", "cancel", json!({"cancelled": true})),
            ("notification", "send", json!({"delivered": true})),
            (
                "storage",
                "prepare",
                json!({"handle": "h", "max_bytes": 10}),
            ),
            ("storage", "write", json!({"written": 4})),
            ("storage", "finish", json!({"derived_id": "d", "size": 4})),
            ("storage", "discard", json!({"discarded": false})),
            (
                "content_processing",
                "process",
                json!({"derived_id": "d", "output_kind": "text", "bytes": 4}),
            ),
            ("content_processing", "cancel", json!({"cancelled": true})),
        ] {
            validate_result(kind, method, &good).unwrap_or_else(|e| panic!("{kind}.{method}: {e}"));
        }
        for (kind, method, bad) in [
            ("notification", "send", json!(null)),
            ("notification", "send", json!([])),
            ("notification", "send", json!("true")),
            ("notification", "send", json!({})),
            ("notification", "send", json!({"delivered": "yes"})),
            (
                "notification",
                "send",
                json!({"delivered": true, "extra": 1}),
            ),
            ("login_assist", "begin", json!({"prompt": "scan"})),
            ("storage", "prepare", json!({"handle": "h"})),
            (
                "content_processing",
                "process",
                json!({"derived_id": "d", "output_kind": "text", "bytes": "4"}),
            ),
        ] {
            assert!(
                validate_result(kind, method, &bad).is_err(),
                "{kind}.{method}: {bad}"
            );
        }
    }

    #[test]
    fn login_assist_never_receives_credentials() {
        for key in ["cookie", "web_session", "access_token", "password"] {
            assert!(
                validate("login_assist", "begin", &json!({"site": "x", key: "s"})).is_err(),
                "{key} must be refused"
            );
        }
    }

    #[test]
    fn content_processing_cannot_replace_original() {
        assert!(
            validate(
                "content_processing",
                "process",
                &json!({"input_kind": "image", "output_kind": "text", "bytes": 10})
            )
            .is_ok()
        );
        assert!(
            validate(
                "content_processing",
                "process",
                &json!({"input_kind": "image", "archive_path": "C:/archive/x"})
            )
            .is_err()
        );
        assert!(
            validate(
                "storage",
                "finish",
                &json!({"handle": "h", "path": "C:/archive/final"})
            )
            .is_err()
        );
    }
}
