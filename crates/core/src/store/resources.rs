//! Reads behind the duplicate and similarity views (stage plan §11.5). Read-only by design.
use crate::error::CoreError;
use rusqlite::{Connection, OptionalExtension};
use serde_json::{Value, json};

#[derive(Debug, Clone)]
pub struct MediaRef {
    pub entry_id: String,
    pub media_id: String,
    pub variant_id: String,
    pub blob_id: String,
    pub role: String,
    pub representation: String,
    pub measured: Option<String>,
    pub entry_title: String,
    pub namespace: String,
    pub published_at: Option<String>,
}

const MEDIA_JOIN: &str = "FROM media_variants v JOIN media_items m ON m.media_id=v.media_id JOIN entry_revisions r ON r.revision_id=m.revision_id JOIN entries e ON e.entry_id=r.entry_id";

pub fn entry_media(c: &Connection, entry_id: &str) -> Result<Vec<MediaRef>, CoreError> {
    let sql = format!(
        "SELECT e.entry_id,m.media_id,v.variant_id,v.blob_id,m.role,v.source_representation,v.measured_spec,r.title,e.namespace,e.published_at {MEDIA_JOIN} WHERE e.entry_id=?1 AND e.lifecycle!='deleted' ORDER BY m.position,v.created_at"
    );
    let mut stmt = c.prepare(&sql)?;
    let rows = stmt
        .query_map([entry_id], |r| {
            Ok(MediaRef {
                entry_id: r.get(0)?,
                media_id: r.get(1)?,
                variant_id: r.get(2)?,
                blob_id: r.get(3)?,
                role: r.get(4)?,
                representation: r.get(5)?,
                measured: r.get(6)?,
                entry_title: r.get(7)?,
                namespace: r.get(8)?,
                published_at: r.get(9)?,
            })
        })?
        .collect::<Result<Vec<_>, _>>()?;
    Ok(rows)
}

/// Candidates of the same role in other entries, bounded before any hashing happens.
pub fn candidates(
    c: &Connection,
    role: &str,
    exclude_entry: &str,
    exclude_blob: &str,
    limit: i64,
) -> Result<Vec<MediaRef>, CoreError> {
    let sql = format!(
        "SELECT e.entry_id,m.media_id,v.variant_id,v.blob_id,m.role,v.source_representation,v.measured_spec,r.title,e.namespace,e.published_at {MEDIA_JOIN} WHERE m.role=?1 AND e.entry_id!=?2 AND v.blob_id!=?3 AND e.lifecycle!='deleted' ORDER BY e.published_at DESC,e.entry_id,v.rowid LIMIT ?4"
    );
    let mut stmt = c.prepare(&sql)?;
    let rows = stmt
        .query_map(
            rusqlite::params![role, exclude_entry, exclude_blob, limit],
            |r| {
                Ok(MediaRef {
                    entry_id: r.get(0)?,
                    media_id: r.get(1)?,
                    variant_id: r.get(2)?,
                    blob_id: r.get(3)?,
                    role: r.get(4)?,
                    representation: r.get(5)?,
                    measured: r.get(6)?,
                    entry_title: r.get(7)?,
                    namespace: r.get(8)?,
                    published_at: r.get(9)?,
                })
            },
        )?
        .collect::<Result<Vec<_>, _>>()?;
    Ok(rows)
}

/// Blobs referenced by more than one entry, newest page first.
pub fn duplicates(c: &Connection, after: Option<&str>, limit: i64) -> Result<Value, CoreError> {
    let limit = limit.clamp(1, 100);
    let after = after.unwrap_or("");
    let mut stmt = c.prepare(
        "SELECT v.blob_id, COALESCE(b.size,0),
                (SELECT count(DISTINCT e.entry_id) FROM media_variants v2
                   JOIN media_items m2 ON m2.media_id=v2.media_id
                   JOIN entry_revisions r2 ON r2.revision_id=m2.revision_id
                   JOIN entries e ON e.entry_id=r2.entry_id
                  WHERE v2.blob_id=v.blob_id AND e.lifecycle!='deleted'),
                group_concat(DISTINCT m.role), group_concat(DISTINCT v.source_representation)
         FROM media_variants v
         JOIN media_items m ON m.media_id=v.media_id
         JOIN entry_revisions r ON r.revision_id=m.revision_id
         JOIN entries e ON e.entry_id=r.entry_id
         LEFT JOIN blobs b ON b.blob_id=v.blob_id
         WHERE e.lifecycle!='deleted' AND v.blob_id>?1
         GROUP BY v.blob_id HAVING count(DISTINCT e.entry_id)>1
         ORDER BY v.blob_id LIMIT ?2",
    )?;
    let mut items = stmt
        .query_map(rusqlite::params![after, limit + 1], |r| {
            Ok(json!({
                "blob_id": r.get::<_, String>(0)?,
                "size": r.get::<_, i64>(1)?,
                "entry_count": r.get::<_, i64>(2)?,
                "roles": r.get::<_, Option<String>>(3)?,
                "representations": r.get::<_, Option<String>>(4)?,
            }))
        })?
        .collect::<Result<Vec<_>, _>>()?;
    let more = items.len() as i64 > limit;
    items.truncate(limit as usize);
    for item in &mut items {
        let blob = item["blob_id"].as_str().unwrap_or("").to_owned();
        let mut refs = c.prepare(
            "SELECT DISTINCT e.entry_id,e.namespace,r.title,m.role,v.variant_id,v.source_representation
             FROM media_variants v JOIN media_items m ON m.media_id=v.media_id
             JOIN entry_revisions r ON r.revision_id=m.revision_id JOIN entries e ON e.entry_id=r.entry_id
             WHERE v.blob_id=?1 AND e.lifecycle!='deleted' ORDER BY e.entry_id,m.role LIMIT 20",
        )?;
        let list = refs
            .query_map([blob], |r| {
                Ok(json!({
                    "entry_id": r.get::<_, String>(0)?,
                    "namespace": r.get::<_, String>(1)?,
                    "title": r.get::<_, String>(2)?,
                    "role": r.get::<_, String>(3)?,
                    "variant_id": r.get::<_, String>(4)?,
                    "representation": r.get::<_, String>(5)?,
                }))
            })?
            .collect::<Result<Vec<_>, _>>()?;
        item["references"] = Value::Array(list);
    }
    let next = if more {
        items
            .last()
            .and_then(|v| v["blob_id"].as_str())
            .map(str::to_owned)
    } else {
        None
    };
    Ok(json!({"items": items, "next_after": next}))
}

pub fn signature(c: &Connection, blob_id: &str, kind: &str) -> Result<Option<String>, CoreError> {
    Ok(c.query_row(
        "SELECT signature FROM resource_signatures WHERE blob_id=?1 AND kind=?2 ORDER BY created_at DESC LIMIT 1",
        rusqlite::params![blob_id, kind],
        |r| r.get(0),
    )
    .optional()?)
}

pub fn save_signature(
    tx: &rusqlite::Transaction<'_>,
    blob_id: &str,
    kind: &str,
    algorithm: &str,
    signature: &str,
    now: &str,
) -> Result<(), CoreError> {
    tx.execute(
        "INSERT OR REPLACE INTO resource_signatures(blob_id,kind,algorithm,signature,created_at) VALUES(?1,?2,?3,?4,?5)",
        rusqlite::params![blob_id, kind, algorithm, signature, now],
    )?;
    Ok(())
}

/// The filesystem location of a blob: the managed object, or the observed external path.
pub fn binary_path(
    c: &Connection,
    blob_id: &str,
) -> Result<Option<(std::path::PathBuf, bool)>, CoreError> {
    let row: Option<(String, bool)> = c
        .query_row(
            "SELECT locator,managed FROM replicas WHERE blob_id=?1 AND state IN ('verified','external') ORDER BY managed DESC LIMIT 1",
            [blob_id],
            |r| Ok((r.get(0)?, r.get(1)?)),
        )
        .optional()?;
    Ok(row.map(|(locator, managed)| {
        if managed {
            (std::path::PathBuf::from(locator), true)
        } else {
            (std::path::PathBuf::from(locator), false)
        }
    }))
}

/// One side of a comparison: source, title, a bounded body snippet, media specs and digests.
pub fn side(c: &Connection, entry_id: &str) -> Result<Value, CoreError> {
    let (namespace, kind, published_at, precision, title, body, revision): (
        String,
        String,
        Option<String>,
        Option<String>,
        String,
        String,
        String,
    ) = c
        .query_row(
            "SELECT e.namespace,e.native_kind,e.published_at,e.published_precision,r.title,substr(r.normalized_body,1,500),r.revision_id
             FROM entries e LEFT JOIN current_entry_revisions r ON r.entry_id=e.entry_id
             WHERE e.entry_id=?1 AND e.lifecycle!='deleted'",
            [entry_id],
            |r| {
                Ok((
                    r.get(0)?,
                    r.get(1)?,
                    r.get(2)?,
                    r.get(3)?,
                    r.get(4)?,
                    r.get(5)?,
                    r.get(6)?,
                ))
            },
        )
        .optional()?
        .ok_or_else(|| CoreError::NotFound("entry".into()))?;
    let owner: Option<(String, Option<String>)> = c
        .query_row(
            "SELECT a.namespace,a.display_name FROM entries e JOIN source_accounts a ON a.account_id=e.account_id WHERE e.entry_id=?1",
            [entry_id],
            |r| Ok((r.get(0)?, r.get(1)?)),
        )
        .optional()?;
    let creator: Option<(String, String)> = c
        .query_row(
            "SELECT c.creator_id,c.display_name FROM entries e JOIN account_bindings b ON b.account_id=e.account_id AND b.state='active' JOIN creators c ON c.creator_id=b.creator_id WHERE e.entry_id=?1",
            [entry_id],
            |r| Ok((r.get(0)?, r.get(1)?)),
        )
        .optional()?;
    let media = c
        .prepare(
            "SELECT m.media_id,m.role,v.variant_id,v.blob_id,v.source_representation,v.claimed_spec,v.measured_spec,
                    (SELECT b.size FROM blobs b WHERE b.blob_id=v.blob_id)
             FROM media_items m LEFT JOIN media_variants v ON v.media_id=m.media_id
             WHERE m.revision_id=?1 ORDER BY m.position,v.created_at",
        )?
        .query_map([&revision], |r| {
            Ok(json!({
                "media_id": r.get::<_, String>(0)?,
                "role": r.get::<_, String>(1)?,
                "variant_id": r.get::<_, Option<String>>(2)?,
                "blob_id": r.get::<_, Option<String>>(3)?,
                "representation": r.get::<_, Option<String>>(4)?,
                "claimed": serde_json::from_str::<Value>(&r.get::<_, String>(5)?).unwrap_or(Value::Null),
                "measured": r.get::<_, Option<String>>(6)?.and_then(|v| serde_json::from_str::<Value>(&v).ok()),
                "size": r.get::<_, Option<i64>>(7)?,
            }))
        })?
        .collect::<Result<Vec<_>, _>>()?;
    Ok(json!({
        "entry_id": entry_id,
        "namespace": namespace,
        "kind": kind,
        "published_at": published_at,
        "published_precision": precision,
        "title": title,
        "caption": body,
        "account": owner.map(|(namespace, name)| json!({"namespace": namespace, "display_name": name})),
        "creator": creator.map(|(id, name)| json!({"creator_id": id, "display_name": name})),
        "media": media,
    }))
}

pub fn decision(
    c: &Connection,
    left_kind: &str,
    left_id: &str,
    right_kind: &str,
    right_id: &str,
) -> Result<Option<Value>, CoreError> {
    let row = c
        .query_row(
            "SELECT comparison_id,decision,evidence_json,created_at,decided_at FROM resource_comparisons
             WHERE left_kind=?1 AND left_id=?2 AND right_kind=?3 AND right_id=?4",
            rusqlite::params![left_kind, left_id, right_kind, right_id],
            |r| {
                Ok(json!({
                    "comparison_id": r.get::<_, String>(0)?,
                    "decision": r.get::<_, Option<String>>(1)?,
                    "evidence": serde_json::from_str::<Value>(&r.get::<_, String>(2)?).unwrap_or(Value::Null),
                    "created_at": r.get::<_, String>(3)?,
                    "decided_at": r.get::<_, Option<String>>(4)?,
                }))
            },
        )
        .optional()?;
    Ok(row)
}

#[allow(clippy::too_many_arguments)] // the comparison pair and its recorded decision
pub fn save_decision(
    tx: &rusqlite::Transaction<'_>,
    comparison_id: &str,
    left_kind: &str,
    left_id: &str,
    right_kind: &str,
    right_id: &str,
    decision: &str,
    evidence: &Value,
    now: &str,
) -> Result<(), CoreError> {
    tx.execute(
        "INSERT INTO resource_comparisons(comparison_id,left_kind,left_id,right_kind,right_id,decision,evidence_json,created_at,decided_at) VALUES(?1,?2,?3,?4,?5,?6,?7,?8,?8)
         ON CONFLICT(left_kind,left_id,right_kind,right_id) DO UPDATE SET decision=excluded.decision,evidence_json=excluded.evidence_json,decided_at=excluded.decided_at",
        rusqlite::params![comparison_id, left_kind, left_id, right_kind, right_id, decision, evidence.to_string(), now],
    )?;
    Ok(())
}
