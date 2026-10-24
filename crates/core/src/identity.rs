//! Profile records, name normalization and pending identity candidates (stage plan §11.2).
//!
//! A candidate only proposes: nothing here changes a binding, and the evidence keeps the
//! whitelisted profile fields the page showed, never a Cookie, token or signature.
use crate::error::CoreError;
use rusqlite::Transaction;
use serde_json::{Map, Value, json};

/// Field names a profile record may keep. Everything else in an adapter's evidence is dropped.
const PROFILE_FIELDS: [&str; 12] = [
    "display_name",
    "name",
    "nickname",
    "bio",
    "description",
    "signature",
    "avatar",
    "avatar_url",
    "avatarUrl",
    "profile_url",
    "verified",
    "location",
];

/// A value that may be a credential or a short-lived address never enters a profile record.
fn forbidden(key: &str) -> bool {
    let key = key.to_ascii_lowercase();
    key.starts_with("x-")
        || [
            "cookie",
            "token",
            "session",
            "signature",
            "sign",
            "secret",
            "password",
            "credential",
            "authorization",
            "url",
        ]
        .iter()
        .any(|word| key.contains(word))
}

/// The whitelisted profile record: display name, bio, avatar and the site's own fields.
pub fn profile_record(
    evidence: &Value,
    display_name: Option<&str>,
    profile_url: Option<&str>,
) -> Value {
    let safe = trace_contracts::content::safe_metadata(evidence);
    let mut out = Map::new();
    if let Some(name) = display_name {
        out.insert(
            "display_name".into(),
            json!(name.chars().take(512).collect::<String>()),
        );
    }
    if let Some(url) = profile_url {
        out.insert("profile_url".into(), json!(url));
    }
    if let Some(object) = safe.as_object() {
        for (key, value) in object.iter().take(32) {
            if PROFILE_FIELDS.contains(&key.as_str()) {
                match value {
                    Value::String(text) => {
                        let text: String = text.chars().take(512).collect();
                        out.entry(key.clone()).or_insert(json!(text));
                    }
                    Value::Bool(_) | Value::Number(_) => {
                        out.entry(key.clone()).or_insert(value.clone());
                    }
                    _ => {}
                }
                continue;
            }
            if forbidden(key) {
                continue;
            }
            // A bounded set of the site's own scalar fields, so a record stays small.
            if out.len() >= 16 {
                continue;
            }
            match value {
                Value::String(text) => {
                    let text: String = text.chars().take(256).collect();
                    out.entry(key.clone()).or_insert(json!(text));
                }
                Value::Bool(_) | Value::Number(_) => {
                    out.entry(key.clone()).or_insert(value.clone());
                }
                _ => {}
            }
        }
    }
    Value::Object(out)
}

/// Case, full-width/half-width and whitespace are not identity: they are folded before comparing.
pub fn normalized(name: &str) -> String {
    let mut out = String::with_capacity(name.len());
    for ch in name.chars() {
        let folded = match ch {
            '\u{3000}' => ' ',
            '\u{FF01}'..='\u{FF5E}' => char::from_u32(ch as u32 - 0xFEE0).unwrap_or(ch),
            _ => ch,
        };
        if folded.is_whitespace() {
            continue;
        }
        for lower in folded.to_lowercase() {
            out.push(lower);
        }
    }
    out
}

/// The explicit rule: an equal normalized name is a strong candidate; one containing the other
/// (at least three characters) is a weaker one. Anything else is not a candidate.
pub fn similarity(left: &str, right: &str) -> Option<(f64, &'static str)> {
    let (a, b) = (normalized(left), normalized(right));
    if a.is_empty() || b.is_empty() {
        return None;
    }
    if a == b {
        return Some((1.0, "same_name"));
    }
    let (short, long) = if a.len() <= b.len() {
        (&a, &b)
    } else {
        (&b, &a)
    };
    if short.chars().count() >= 3 && long.contains(short.as_str()) {
        return Some((0.75, "similar_name"));
    }
    None
}

/// Proposes, for the observed account, every creator whose same-site account has a similar name.
/// The account's own binding is never changed and a repeated observation writes no new candidate.
pub fn generate_candidates(
    tx: &Transaction<'_>,
    account_id: &str,
    namespace: &str,
    display_name: Option<&str>,
    now: &str,
) -> Result<usize, CoreError> {
    let Some(name) = display_name.filter(|n| !normalized(n).is_empty()) else {
        return Ok(0);
    };
    let others: Vec<(String, Option<String>, String)> = tx
        .prepare(
            "SELECT a.account_id, a.display_name, b.creator_id
             FROM source_accounts a JOIN account_bindings b ON b.account_id=a.account_id AND b.state='active'
             WHERE a.namespace=?1 AND a.account_id!=?2",
        )?
        .query_map(rusqlite::params![namespace, account_id], |r| {
            Ok((
                r.get::<_, String>(0)?,
                r.get::<_, Option<String>>(1)?,
                r.get::<_, String>(2)?,
            ))
        })?
        .collect::<Result<Vec<_>, _>>()?;
    let bound: Option<String> = tx
        .query_row(
            "SELECT creator_id FROM account_bindings WHERE account_id=?1 AND state='active'",
            [account_id],
            |r| r.get(0),
        )
        .ok();
    let mut written = 0;
    for (other_id, other_name, creator_id) in others {
        if bound.as_deref() == Some(creator_id.as_str()) {
            continue;
        }
        let Some(other_name) = other_name else {
            continue;
        };
        let Some((score, kind)) = similarity(name, &other_name) else {
            continue;
        };
        let evidence = json!({
            "observed_name": name,
            "other_account_id": other_id,
            "other_name": other_name,
            "rule": "normalized equality or containment, at least three characters",
        });
        let inserted = tx.execute(
            "INSERT OR IGNORE INTO identity_candidates(candidate_id,account_id,proposed_creator_id,kind,score,evidence_json,state,created_at) VALUES(?1,?2,?3,?4,?5,?6,'pending',?7)",
            rusqlite::params![
                format!("cand-{}", uuid::Uuid::new_v4()),
                account_id,
                creator_id,
                kind,
                score,
                evidence.to_string(),
                now
            ],
        )?;
        written += inserted;
    }
    Ok(written)
}
