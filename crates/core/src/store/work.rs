//! Work groups and batch previews (stage plan §11.7). Groups are relations; entries stay.
use crate::error::CoreError;
use rusqlite::{Connection, OptionalExtension, Transaction};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};

pub fn create(
    tx: &Transaction<'_>,
    id: &str,
    title: &str,
    description: &str,
    now: &str,
) -> Result<(), CoreError> {
    tx.execute(
        "INSERT INTO work_groups(group_id,title,description,created_at,updated_at) VALUES(?1,?2,?3,?4,?4)",
        rusqlite::params![id, title, description, now],
    )?;
    Ok(())
}

pub fn update(
    tx: &Transaction<'_>,
    id: &str,
    title: Option<&str>,
    description: Option<&str>,
    now: &str,
) -> Result<(), CoreError> {
    let changed = tx.execute(
        "UPDATE work_groups SET title=COALESCE(?2,title),description=COALESCE(?3,description),updated_at=?4 WHERE group_id=?1",
        rusqlite::params![id, title, description, now],
    )?;
    if changed == 0 {
        return Err(CoreError::NotFound("work group".into()));
    }
    Ok(())
}

/// Deletes the group and its memberships only; the entries themselves are untouched.
pub fn delete(tx: &Transaction<'_>, id: &str) -> Result<usize, CoreError> {
    let members: i64 = tx.query_row(
        "SELECT count(*) FROM work_group_members WHERE group_id=?1",
        [id],
        |r| r.get(0),
    )?;
    let deleted = tx.execute("DELETE FROM work_groups WHERE group_id=?1", [id])?;
    if deleted == 0 {
        return Err(CoreError::NotFound("work group".into()));
    }
    Ok(usize::try_from(members).unwrap_or(0))
}

pub fn add_members(
    tx: &Transaction<'_>,
    id: &str,
    entry_ids: &[String],
    now: &str,
) -> Result<usize, CoreError> {
    group_exists(tx, id)?;
    let mut added = 0;
    for entry in entry_ids.iter().take(500) {
        let exists: bool = tx.query_row(
            "SELECT EXISTS(SELECT 1 FROM entries WHERE entry_id=?1 AND lifecycle!='deleted')",
            [entry],
            |r| r.get(0),
        )?;
        if !exists {
            return Err(CoreError::NotFound(format!("entry {entry}")));
        }
        added += tx.execute(
            "INSERT OR IGNORE INTO work_group_members(group_id,entry_id,added_at) VALUES(?1,?2,?3)",
            rusqlite::params![id, entry, now],
        )?;
    }
    Ok(added)
}

pub fn remove_members(
    tx: &Transaction<'_>,
    id: &str,
    entry_ids: &[String],
) -> Result<usize, CoreError> {
    group_exists(tx, id)?;
    let mut removed = 0;
    for entry in entry_ids.iter().take(500) {
        removed += tx.execute(
            "DELETE FROM work_group_members WHERE group_id=?1 AND entry_id=?2",
            rusqlite::params![id, entry],
        )?;
    }
    Ok(removed)
}

fn group_exists(c: &Connection, id: &str) -> Result<(), CoreError> {
    let exists: bool = c.query_row(
        "SELECT EXISTS(SELECT 1 FROM work_groups WHERE group_id=?1)",
        [id],
        |r| r.get(0),
    )?;
    if exists {
        Ok(())
    } else {
        Err(CoreError::NotFound("work group".into()))
    }
}

pub fn groups(c: &Connection, after: Option<&str>, limit: i64) -> Result<Value, CoreError> {
    let limit = limit.clamp(1, 100);
    let after = after.unwrap_or("");
    let mut stmt = c.prepare(
        "SELECT g.group_id,g.title,g.description,g.created_at,g.updated_at,
                (SELECT count(*) FROM work_group_members m WHERE m.group_id=g.group_id)
         FROM work_groups g WHERE g.group_id>?1 ORDER BY g.group_id LIMIT ?2",
    )?;
    let mut items = stmt
        .query_map(rusqlite::params![after, limit + 1], |r| {
            Ok(json!({
                "group_id": r.get::<_, String>(0)?,
                "title": r.get::<_, String>(1)?,
                "description": r.get::<_, String>(2)?,
                "created_at": r.get::<_, String>(3)?,
                "updated_at": r.get::<_, String>(4)?,
                "member_count": r.get::<_, i64>(5)?,
            }))
        })?
        .collect::<Result<Vec<_>, _>>()?;
    let more = items.len() as i64 > limit;
    items.truncate(limit as usize);
    let next = if more {
        items
            .last()
            .and_then(|v| v["group_id"].as_str())
            .map(str::to_owned)
    } else {
        None
    };
    Ok(json!({"items": items, "next_after": next}))
}

pub fn group(c: &Connection, id: &str) -> Result<Value, CoreError> {
    let group = c
        .query_row(
            "SELECT group_id,title,description,created_at,updated_at,
                    (SELECT count(*) FROM work_group_members m WHERE m.group_id=work_groups.group_id)
             FROM work_groups WHERE group_id=?1",
            [id],
            |r| {
                Ok(json!({
                    "group_id": r.get::<_, String>(0)?,
                    "title": r.get::<_, String>(1)?,
                    "description": r.get::<_, String>(2)?,
                    "created_at": r.get::<_, String>(3)?,
                    "updated_at": r.get::<_, String>(4)?,
                    "member_count": r.get::<_, i64>(5)?,
                }))
            },
        )
        .optional()?
        .ok_or_else(|| CoreError::NotFound("work group".into()))?;
    let mut members = c
        .prepare(
            "SELECT e.entry_id,e.namespace,COALESCE(r.title,''),e.published_at,e.published_precision,
                    a.display_name,cr.display_name
             FROM work_group_members m
             JOIN entries e ON e.entry_id=m.entry_id AND e.lifecycle!='deleted'
             LEFT JOIN current_entry_revisions r ON r.entry_id=e.entry_id
             LEFT JOIN account_bindings b ON b.account_id=e.account_id AND b.state='active'
             LEFT JOIN source_accounts a ON a.account_id=e.account_id
             LEFT JOIN creators cr ON cr.creator_id=b.creator_id
             WHERE m.group_id=?1 ORDER BY e.published_at IS NULL,e.published_at DESC,e.entry_id LIMIT 500",
        )?
        .query_map([id], |r| {
            Ok(json!({
                "entry_id": r.get::<_, String>(0)?,
                "namespace": r.get::<_, String>(1)?,
                "title": r.get::<_, String>(2)?,
                "published_at": r.get::<_, Option<String>>(3)?,
                "published_precision": r.get::<_, Option<String>>(4)?,
                "account_name": r.get::<_, Option<String>>(5)?,
                "creator_name": r.get::<_, Option<String>>(6)?,
            }))
        })?
        .collect::<Result<Vec<_>, _>>()?;
    members.truncate(500);
    Ok(json!({"group": group, "members": members}))
}

/// Read-only: each member keeps its own caption, source and selected media.
pub fn compare(c: &Connection, id: &str) -> Result<Value, CoreError> {
    group_exists(c, id)?;
    let mut stmt = c.prepare(
        "SELECT e.entry_id,e.namespace,e.published_at,e.published_precision,
                COALESCE(r.title,''),substr(r.normalized_body,1,500),
                a.display_name,cr.display_name,em.content_kinds,em.plays,em.likes
         FROM work_group_members m
         JOIN entries e ON e.entry_id=m.entry_id AND e.lifecycle!='deleted'
         LEFT JOIN current_entry_revisions r ON r.entry_id=e.entry_id
         LEFT JOIN account_bindings b ON b.account_id=e.account_id AND b.state='active'
         LEFT JOIN source_accounts a ON a.account_id=e.account_id
         LEFT JOIN creators cr ON cr.creator_id=b.creator_id
         LEFT JOIN entry_meta em ON em.entry_id=e.entry_id
         WHERE m.group_id=?1 ORDER BY e.published_at IS NULL,e.published_at DESC,e.entry_id",
    )?;
    let mut items = stmt
        .query_map([id], |r| {
            let entry_id: String = r.get(0)?;
            let content_kinds: Option<String> = r.get(8)?;
            let plays: Option<i64> = r.get(9)?;
            let likes: Option<i64> = r.get(10)?;
            Ok(json!({
                "entry_id": entry_id,
                "namespace": r.get::<_, String>(1)?,
                "published_at": r.get::<_, Option<String>>(2)?,
                "published_precision": r.get::<_, Option<String>>(3)?,
                "title": r.get::<_, String>(4)?,
                "caption": r.get::<_, String>(5)?,
                "account_name": r.get::<_, Option<String>>(6)?,
                "creator_name": r.get::<_, Option<String>>(7)?,
                "content_kinds": content_kinds.and_then(|v| serde_json::from_str::<Value>(&v).ok()).unwrap_or(Value::Null),
                "stats": json!({"plays": plays, "likes": likes}),
            }))
        })?
        .collect::<Result<Vec<_>, _>>()?;
    items.truncate(200);
    Ok(json!({"group_id": id, "items": items}))
}

/// The digest a preview binds: the exact entry set and the state its actions would read.
pub fn scope_digest(
    c: &Connection,
    entry_ids: &[String],
    actions: &Value,
) -> Result<String, CoreError> {
    let mut sorted = entry_ids.to_vec();
    sorted.sort();
    sorted.dedup();
    let mut hasher = Sha256::new();
    for entry in &sorted {
        hasher.update(entry.as_bytes());
        hasher.update([0]);
        let exists: bool = c.query_row(
            "SELECT EXISTS(SELECT 1 FROM entries WHERE entry_id=?1 AND lifecycle!='deleted')",
            [entry],
            |r| r.get(0),
        )?;
        hasher.update([u8::from(exists)]);
        let mut tags =
            c.prepare("SELECT kind,value FROM entry_terms WHERE entry_id=?1 ORDER BY kind,value")?;
        for row in tags.query_map([entry], |r| {
            Ok((r.get::<_, String>(0)?, r.get::<_, String>(1)?))
        })? {
            let (kind, value) = row?;
            hasher.update(kind.as_bytes());
            hasher.update(value.as_bytes());
        }
        let mut groups = c.prepare(
            "SELECT group_id FROM work_group_members WHERE entry_id=?1 ORDER BY group_id",
        )?;
        for row in groups.query_map([entry], |r| r.get::<_, String>(0))? {
            hasher.update(row?.as_bytes());
        }
    }
    hasher.update(actions.to_string().as_bytes());
    let digest = hasher.finalize();
    Ok(digest.iter().map(|b| format!("{b:02x}")).collect())
}

pub fn save_preview(
    tx: &Transaction<'_>,
    token: &str,
    actions: &Value,
    items: &Value,
    digest: &str,
    now: &str,
    expires_at: &str,
) -> Result<(), CoreError> {
    tx.execute(
        "INSERT INTO work_batch_previews(token,actions_json,items_json,scope_digest,created_at,expires_at) VALUES(?1,?2,?3,?4,?5,?6)",
        rusqlite::params![token, actions.to_string(), items.to_string(), digest, now, expires_at],
    )?;
    Ok(())
}

pub fn preview(c: &Connection, token: &str) -> Result<Value, CoreError> {
    c.query_row(
        "SELECT token,actions_json,items_json,scope_digest,created_at,expires_at,used_at FROM work_batch_previews WHERE token=?1",
        [token],
        |r| {
            Ok(json!({
                "token": r.get::<_, String>(0)?,
                "actions": serde_json::from_str::<Value>(&r.get::<_, String>(1)?).unwrap_or(Value::Null),
                "items": serde_json::from_str::<Value>(&r.get::<_, String>(2)?).unwrap_or(Value::Null),
                "scope_digest": r.get::<_, String>(3)?,
                "created_at": r.get::<_, String>(4)?,
                "expires_at": r.get::<_, String>(5)?,
                "used_at": r.get::<_, Option<String>>(6)?,
            }))
        },
    )
    .optional()?
    .ok_or_else(|| CoreError::NotFound("batch preview".into()))
}

pub fn mark_used(tx: &Transaction<'_>, token: &str, now: &str) -> Result<(), CoreError> {
    tx.execute(
        "UPDATE work_batch_previews SET used_at=?2 WHERE token=?1",
        rusqlite::params![token, now],
    )?;
    Ok(())
}
