//! One audit row per management action: stable IDs, the action and its counts.
//!
//! Nothing here carries a Cookie, a signed address, a body or an absolute path.
use crate::error::CoreError;
use rusqlite::{Connection, Transaction};
use serde_json::{Value, json};

/// Writes the audit row inside the caller's transaction, so the action and its record share
/// one commit. `subject` is a stable id; `detail` is a small object of ids and counts.
pub fn record(
    tx: &Transaction<'_>,
    kind: &str,
    subject: &str,
    detail: &Value,
    now: &str,
) -> Result<String, CoreError> {
    let id = format!("op-{}", uuid::Uuid::new_v4());
    tx.execute(
        "INSERT INTO management_operations(operation_id,kind,subject,detail_json,created_at) VALUES(?1,?2,?3,?4,?5)",
        rusqlite::params![id, kind, subject, detail.to_string(), now],
    )?;
    Ok(id)
}

/// Newest first, bounded; `after` is the previous page's last operation id.
pub fn list(
    c: &Connection,
    kind: Option<&str>,
    subject: Option<&str>,
    after: Option<&str>,
    limit: i64,
) -> Result<Value, CoreError> {
    let limit = limit.clamp(1, 200);
    let cursor: Option<(String, String)> = match after {
        Some(id) => c
            .query_row(
                "SELECT created_at,operation_id FROM management_operations WHERE operation_id=?1",
                [id],
                |r| Ok((r.get(0)?, r.get(1)?)),
            )
            .ok(),
        None => None,
    };
    let mut stmt = c.prepare(
        "SELECT operation_id,kind,subject,detail_json,created_at FROM management_operations
         WHERE (?1 IS NULL OR kind=?1) AND (?2 IS NULL OR subject=?2)
           AND (?3 IS NULL OR (created_at,operation_id) < (?3,?4))
         ORDER BY created_at DESC, operation_id DESC LIMIT ?5",
    )?;
    let (at, id) = match &cursor {
        Some((at, id)) => (Some(at.as_str()), Some(id.as_str())),
        None => (None, None),
    };
    let mut items = stmt
        .query_map(
            rusqlite::params![kind, subject, at, id, limit + 1],
            |r| {
                Ok(json!({
                    "operation_id": r.get::<_, String>(0)?,
                    "kind": r.get::<_, String>(1)?,
                    "subject": r.get::<_, String>(2)?,
                    "detail": serde_json::from_str::<Value>(&r.get::<_, String>(3)?).unwrap_or(Value::Null),
                    "created_at": r.get::<_, String>(4)?,
                }))
            },
        )?
        .collect::<Result<Vec<_>, _>>()?;
    let more = items.len() as i64 > limit;
    items.truncate(limit as usize);
    let next = if more {
        items
            .last()
            .and_then(|v| v["operation_id"].as_str())
            .map(str::to_owned)
    } else {
        None
    };
    Ok(json!({"items": items, "next_after": next}))
}
