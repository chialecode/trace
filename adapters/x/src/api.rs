//! X's web client GraphQL interface: one call shape, one error reading.
use crate::catalog::{self, Operation};
use serde_json::{Map, Value, json};
use trace_contracts::plugin::PluginError;
use trace_domain::errors::FailureClass;
use trace_site_support::kit::{self, net};
use trace_site_support::runtime::Transport;

pub(crate) const ORIGIN: &str = "https://x.com";
pub(crate) const UA: &str = kit::DESKTOP_UA;

pub(crate) fn page_headers() -> Value {
    json!({
        "user-agent": UA,
        "accept-language": "en-US,en;q=0.9",
        "accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
    })
}

/// What the media hosts expect; no authentication data.
pub(crate) fn media_headers() -> Value {
    json!({"referer": format!("{ORIGIN}/"), "user-agent": UA})
}

/// Headers of the web client's API calls. The anti-forgery and session-type headers are added
/// by Core from the session; the bearer is the client's public token, not the visitor's.
fn api_headers(bearer: &str) -> Value {
    json!({
        "authorization": format!("Bearer {bearer}"),
        "x-twitter-active-user": "yes",
        "x-twitter-client-language": "en",
        "user-agent": UA,
        "referer": format!("{ORIGIN}/"),
        "accept": "*/*",
    })
}

fn flags<'a>(names: impl Iterator<Item = (&'a String, bool)>) -> Value {
    Value::Object(
        names
            .map(|(n, v)| (n.clone(), Value::Bool(v)))
            .collect::<Map<_, _>>(),
    )
}

/// The path of an operation, which is also what its transaction id signs.
fn path_for(op_name: &str, op: &Operation) -> String {
    format!("/i/api/graphql/{}/{op_name}", op.id)
}

fn url_for(op_name: &str, op: &Operation, variables: &Value) -> String {
    let mut query = vec![
        ("variables", variables.to_string()),
        (
            "features",
            flags(op.features.iter().map(|(n, v)| (n, *v))).to_string(),
        ),
    ];
    if !op.toggles.is_empty() {
        query.push((
            "fieldToggles",
            flags(op.toggles.iter().map(|n| (n, false))).to_string(),
        ));
    }
    let pairs: Vec<(&str, &str)> = query.iter().map(|(k, v)| (*k, v.as_str())).collect();
    net::with_query(&format!("{ORIGIN}{}", path_for(op_name, op)), &pairs)
}

/// The errors a GraphQL answer carries, by what they mean for the content.
fn failure(answer: &Value) -> Option<PluginError> {
    let errors = answer["errors"].as_array().filter(|e| !e.is_empty())?;
    let code = errors[0]["code"]
        .as_i64()
        .or_else(|| errors[0]["extensions"]["code"].as_i64());
    Some(match code {
        Some(88 | 429) => kit::limited("X asks to slow down"),
        Some(32 | 215 | 401) => kit::login_needed("X asks for a login"),
        Some(63 | 144) => {
            trace_site_support::error(FailureClass::SourceUnavailable, "X has no such item")
        }
        _ => kit::schema("X answered with an error that is not understood"),
    })
}

/// Calls one operation with the shipped ids. When the site rejects them, its own scripts are
/// read (at most once per interval) and the call is made again with what they say and, when the
/// page gave the material, with the transaction id the web client signs its calls with; a
/// second rejection is a changed interface, never an empty result.
pub(crate) fn graphql(
    net: &dyn Transport,
    name: &str,
    variables: &Value,
) -> Result<Value, PluginError> {
    let mut known = catalog::get(net)?;
    let mut rejected = None;
    for attempt in 0..2 {
        let op = known.operation(name)?;
        let bearer = known
            .bearer
            .as_deref()
            .ok_or_else(|| kit::schema("no web client token"))?;
        let mut headers = api_headers(bearer);
        let signed = known.material.as_ref().is_some_and(|material| {
            headers["x-client-transaction-id"] =
                Value::String(material.sign("GET", &path_for(name, op)));
            true
        });
        match net::json(net, &url_for(name, op, variables), headers, true) {
            Ok(answer) => {
                // The same ids were rejected unsigned and accepted signed: the site wants the
                // transaction id, so the next reads of it include the material.
                if signed && rejected.as_deref() == Some(op.id.as_str()) {
                    catalog::needs_signing();
                }
                // Partial data with errors is still data; errors alone are the failure.
                if answer["data"].is_null()
                    && let Some(e) = failure(&answer)
                {
                    return Err(e);
                }
                return Ok(answer);
            }
            Err(e)
                if matches!(
                    e.class,
                    FailureClass::InvalidPayload | FailureClass::SourceUnavailable
                ) =>
            {
                if attempt == 1 {
                    return Err(kit::schema(
                        "X rejected the operation after its ids were read again",
                    ));
                }
                rejected = Some(op.id.clone());
                known = catalog::refresh(net)?;
            }
            Err(e) => return Err(e),
        }
    }
    Err(kit::schema("unreachable"))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn errors_are_read_by_code_and_data_wins_over_errors() {
        let class = |v: Value| failure(&v).unwrap().class;
        assert_eq!(
            class(json!({"errors": [{"code": 88}]})),
            FailureClass::RateLimited
        );
        assert_eq!(
            class(json!({"errors": [{"extensions": {"code": 32}}]})),
            FailureClass::AuthRequired
        );
        assert_eq!(
            class(json!({"errors": [{"code": 63}]})),
            FailureClass::SourceUnavailable
        );
        assert_eq!(
            class(json!({"errors": [{"message": "?"}]})),
            FailureClass::SchemaChanged
        );
        assert!(failure(&json!({"data": {}})).is_none());
    }
}
