//! Import batches and their items (stage plan §11.4). Every statement lives here.
use crate::error::CoreError;
use rusqlite::{Connection, OptionalExtension, Transaction};
use serde_json::{Value, json};

pub struct NewBatch<'a> {
    pub batch_id: &'a str,
    pub mode: &'a str,
    pub source_path: &'a str,
    pub target_creator_id: Option<&'a str>,
    pub account_id: Option<&'a str>,
    pub now: &'a str,
}

pub struct NewItem<'a> {
    pub item_id: &'a str,
    pub batch_id: &'a str,
    pub position: i64,
    pub source_path: &'a str,
    pub rel_path: &'a str,
    pub size: i64,
    pub mtime: Option<&'a str>,
    pub now: &'a str,
}

#[derive(Debug, Clone)]
pub struct ItemRow {
    pub item_id: String,
    pub source_path: String,
    pub rel_path: String,
    pub mtime: Option<String>,
    pub size: i64,
    pub sha256: Option<String>,
    pub blob_id: Option<String>,
    pub entry_id: Option<String>,
    pub state: String,
}

pub fn create_batch(tx: &Transaction<'_>, batch: &NewBatch<'_>) -> Result<(), CoreError> {
    tx.execute(
        "INSERT INTO import_batches(batch_id,mode,source_path,state,target_creator_id,account_id,created_at,updated_at) VALUES(?1,?2,?3,'pending',?4,?5,?6,?6)",
        rusqlite::params![
            batch.batch_id,
            batch.mode,
            batch.source_path,
            batch.target_creator_id,
            batch.account_id,
            batch.now
        ],
    )?;
    Ok(())
}

pub fn insert_item(tx: &Transaction<'_>, item: &NewItem<'_>) -> Result<(), CoreError> {
    tx.execute(
        "INSERT INTO import_items(item_id,batch_id,position,source_path,rel_path,size,mtime,state,created_at,updated_at) VALUES(?1,?2,?3,?4,?5,?6,?7,'pending',?8,?8)",
        rusqlite::params![
            item.item_id,
            item.batch_id,
            item.position,
            item.source_path,
            item.rel_path,
            item.size,
            item.mtime,
            item.now
        ],
    )?;
    tx.execute(
        "UPDATE import_batches SET total_items=total_items+1,total_bytes=total_bytes+?2,updated_at=?3 WHERE batch_id=?1",
        rusqlite::params![item.batch_id, item.size, item.now],
    )?;
    Ok(())
}

pub fn set_batch_state(
    tx: &Transaction<'_>,
    batch_id: &str,
    state: &str,
    error: Option<&str>,
    now: &str,
) -> Result<(), CoreError> {
    tx.execute(
        "UPDATE import_batches SET state=?2,error=?3,updated_at=?4 WHERE batch_id=?1",
        rusqlite::params![batch_id, state, error, now],
    )?;
    Ok(())
}

pub fn batch_state(c: &Connection, batch_id: &str) -> Result<Option<String>, CoreError> {
    Ok(c.query_row(
        "SELECT state FROM import_batches WHERE batch_id=?1",
        [batch_id],
        |r| r.get(0),
    )
    .optional()?)
}

#[allow(clippy::too_many_arguments)] // one row of import state; every field is a column
pub fn set_item_state(
    tx: &Transaction<'_>,
    item_id: &str,
    state: &str,
    error: Option<&str>,
    sha256: Option<&str>,
    blob_id: Option<&str>,
    entry_id: Option<&str>,
    observed: Option<(i64, Option<&str>)>,
    now: &str,
) -> Result<(), CoreError> {
    tx.execute(
        "UPDATE import_items SET state=?2,error=?3,sha256=COALESCE(?4,sha256),blob_id=COALESCE(?5,blob_id),entry_id=COALESCE(?6,entry_id),observed_size=COALESCE(?7,observed_size),observed_mtime=COALESCE(?8,observed_mtime),checked_at=CASE WHEN ?7 IS NULL THEN checked_at ELSE ?9 END,updated_at=?9 WHERE item_id=?1",
        rusqlite::params![
            item_id,
            state,
            error,
            sha256,
            blob_id,
            entry_id,
            observed.map(|o| o.0),
            observed.and_then(|o| o.1),
            now
        ],
    )?;
    Ok(())
}

/// A committed item's entry moved to another creator's local account.
pub fn set_item_entry(
    tx: &Transaction<'_>,
    item_id: &str,
    entry_id: &str,
) -> Result<(), CoreError> {
    tx.execute(
        "UPDATE import_items SET entry_id=?2 WHERE item_id=?1",
        rusqlite::params![item_id, entry_id],
    )?;
    Ok(())
}

pub fn bump_batch(
    tx: &Transaction<'_>,
    batch_id: &str,
    done: i64,
    failed: i64,
    skipped: i64,
    bytes: i64,
    now: &str,
) -> Result<(), CoreError> {
    tx.execute(
        "UPDATE import_batches SET done_items=done_items+?2,failed_items=failed_items+?3,skipped_items=skipped_items+?4,done_bytes=done_bytes+?5,updated_at=?6 WHERE batch_id=?1",
        rusqlite::params![batch_id, done, failed, skipped, bytes, now],
    )?;
    Ok(())
}

/// Pending work of one batch, in selection order.
pub fn pending_items(
    c: &Connection,
    batch_id: &str,
    limit: i64,
) -> Result<Vec<ItemRow>, CoreError> {
    let mut stmt = c.prepare(
        "SELECT item_id,source_path,rel_path,size,mtime,sha256,blob_id,entry_id,state FROM import_items
         WHERE batch_id=?1 AND state IN ('pending','copying','hashing') ORDER BY position LIMIT ?2",
    )?;
    let rows = stmt
        .query_map(rusqlite::params![batch_id, limit], |r| {
            Ok(ItemRow {
                item_id: r.get(0)?,
                source_path: r.get(1)?,
                rel_path: r.get(2)?,
                mtime: r.get(4)?,
                size: r.get(3)?,
                sha256: r.get(5)?,
                blob_id: r.get(6)?,
                entry_id: r.get(7)?,
                state: r.get(8)?,
            })
        })?
        .collect::<Result<Vec<_>, _>>()?;
    Ok(rows)
}

/// Every committed item of one batch, for an explicit re-assignment.
pub fn committed_items(c: &Connection, batch_id: &str) -> Result<Vec<ItemRow>, CoreError> {
    let mut stmt = c.prepare(
        "SELECT item_id,source_path,rel_path,size,mtime,sha256,blob_id,entry_id,state FROM import_items
         WHERE batch_id=?1 AND state='committed' ORDER BY position",
    )?;
    let rows = stmt
        .query_map([batch_id], |r| {
            Ok(ItemRow {
                item_id: r.get(0)?,
                source_path: r.get(1)?,
                rel_path: r.get(2)?,
                mtime: r.get(4)?,
                size: r.get(3)?,
                sha256: r.get(5)?,
                blob_id: r.get(6)?,
                entry_id: r.get(7)?,
                state: r.get(8)?,
            })
        })?
        .collect::<Result<Vec<_>, _>>()?;
    Ok(rows)
}

/// External items whose file is worth re-checking.
pub fn external_items(c: &Connection, batch_id: Option<&str>) -> Result<Vec<ItemRow>, CoreError> {
    let mut stmt = c.prepare(
        "SELECT item_id,source_path,rel_path,size,mtime,sha256,blob_id,entry_id,state FROM import_items
         WHERE state='committed' AND (?1 IS NULL OR batch_id=?1)
           AND EXISTS(SELECT 1 FROM replicas r WHERE r.blob_id=import_items.blob_id AND r.managed=0)",
    )?;
    let rows = stmt
        .query_map([batch_id], |r| {
            Ok(ItemRow {
                item_id: r.get(0)?,
                source_path: r.get(1)?,
                rel_path: r.get(2)?,
                mtime: r.get(4)?,
                size: r.get(3)?,
                sha256: r.get(5)?,
                blob_id: r.get(6)?,
                entry_id: r.get(7)?,
                state: r.get(8)?,
            })
        })?
        .collect::<Result<Vec<_>, _>>()?;
    Ok(rows)
}

pub fn batch(c: &Connection, batch_id: &str) -> Result<Value, CoreError> {
    c.query_row(
        "SELECT batch_id,mode,source_path,state,target_creator_id,account_id,total_items,done_items,failed_items,skipped_items,total_bytes,done_bytes,error,created_at,updated_at FROM import_batches WHERE batch_id=?1",
        [batch_id],
        |r| {
            Ok(json!({
                "batch_id": r.get::<_, String>(0)?,
                "mode": r.get::<_, String>(1)?,
                "source_path": r.get::<_, String>(2)?,
                "state": r.get::<_, String>(3)?,
                "target_creator_id": r.get::<_, Option<String>>(4)?,
                "account_id": r.get::<_, Option<String>>(5)?,
                "total_items": r.get::<_, i64>(6)?,
                "done_items": r.get::<_, i64>(7)?,
                "failed_items": r.get::<_, i64>(8)?,
                "skipped_items": r.get::<_, i64>(9)?,
                "total_bytes": r.get::<_, i64>(10)?,
                "done_bytes": r.get::<_, i64>(11)?,
                "error": r.get::<_, Option<String>>(12)?,
                "created_at": r.get::<_, String>(13)?,
                "updated_at": r.get::<_, String>(14)?,
            }))
        },
    )
    .optional()?
    .ok_or_else(|| CoreError::NotFound("import batch".into()))
}

pub fn items(c: &Connection, batch_id: &str, after: i64, limit: i64) -> Result<Value, CoreError> {
    let limit = limit.clamp(1, 200);
    let mut stmt = c.prepare(
        "SELECT position,item_id,source_path,rel_path,size,sha256,blob_id,entry_id,state,error,observed_size,observed_mtime,checked_at FROM import_items WHERE batch_id=?1 AND position>?2 ORDER BY position LIMIT ?3",
    )?;
    let mut items = stmt
        .query_map(rusqlite::params![batch_id, after, limit + 1], |r| {
            Ok(json!({
                "position": r.get::<_, i64>(0)?,
                "item_id": r.get::<_, String>(1)?,
                "source_path": r.get::<_, String>(2)?,
                "rel_path": r.get::<_, String>(3)?,
                "size": r.get::<_, i64>(4)?,
                "sha256": r.get::<_, Option<String>>(5)?,
                "blob_id": r.get::<_, Option<String>>(6)?,
                "entry_id": r.get::<_, Option<String>>(7)?,
                "state": r.get::<_, String>(8)?,
                "error": r.get::<_, Option<String>>(9)?,
                "observed_size": r.get::<_, Option<i64>>(10)?,
                "observed_mtime": r.get::<_, Option<String>>(11)?,
                "checked_at": r.get::<_, Option<String>>(12)?,
            }))
        })?
        .collect::<Result<Vec<_>, _>>()?;
    let more = items.len() as i64 > limit;
    items.truncate(limit as usize);
    let next = if more {
        items.last().and_then(|v| v["position"].as_i64())
    } else {
        None
    };
    Ok(json!({"items": items, "next_after": next}))
}

pub fn batches(c: &Connection, after: Option<&str>, limit: i64) -> Result<Value, CoreError> {
    let limit = limit.clamp(1, 100);
    let cursor: Option<(String, String)> = match after {
        Some(id) => c
            .query_row(
                "SELECT created_at,batch_id FROM import_batches WHERE batch_id=?1",
                [id],
                |r| Ok((r.get(0)?, r.get(1)?)),
            )
            .ok(),
        None => None,
    };
    let (at, id) = match &cursor {
        Some((at, id)) => (Some(at.as_str()), Some(id.as_str())),
        None => (None, None),
    };
    let mut stmt = c.prepare(
        "SELECT batch_id,mode,source_path,state,target_creator_id,total_items,done_items,failed_items,skipped_items,created_at,updated_at FROM import_batches WHERE (?1 IS NULL OR (created_at,batch_id) < (?1,?2)) ORDER BY created_at DESC,batch_id DESC LIMIT ?3",
    )?;
    let mut items = stmt
        .query_map(rusqlite::params![at, id, limit + 1], |r| {
            Ok(json!({
                "batch_id": r.get::<_, String>(0)?,
                "mode": r.get::<_, String>(1)?,
                "source_path": r.get::<_, String>(2)?,
                "state": r.get::<_, String>(3)?,
                "target_creator_id": r.get::<_, Option<String>>(4)?,
                "total_items": r.get::<_, i64>(5)?,
                "done_items": r.get::<_, i64>(6)?,
                "failed_items": r.get::<_, i64>(7)?,
                "skipped_items": r.get::<_, i64>(8)?,
                "created_at": r.get::<_, String>(9)?,
                "updated_at": r.get::<_, String>(10)?,
            }))
        })?
        .collect::<Result<Vec<_>, _>>()?;
    let more = items.len() as i64 > limit;
    items.truncate(limit as usize);
    let next = if more {
        items
            .last()
            .and_then(|v| v["batch_id"].as_str())
            .map(str::to_owned)
    } else {
        None
    };
    Ok(json!({"items": items, "next_after": next}))
}

/// Paths already recorded by an earlier import, so a preview can name duplicates without hashing.
pub fn known_paths(c: &Connection) -> Result<Vec<(String, i64)>, CoreError> {
    let mut stmt =
        c.prepare("SELECT source_path,size FROM import_items WHERE state='committed'")?;
    let rows = stmt
        .query_map([], |r| Ok((r.get(0)?, r.get(1)?)))?
        .collect::<Result<Vec<_>, _>>()?;
    Ok(rows)
}

/// Notes what an external check saw without changing the committed observation.
pub fn record_external_observation(
    tx: &Transaction<'_>,
    item_id: &str,
    size: Option<i64>,
    mtime: Option<&str>,
    now: &str,
) -> Result<(), CoreError> {
    tx.execute(
        "UPDATE import_items SET observed_size=?2,observed_mtime=?3,checked_at=?4,updated_at=?4 WHERE item_id=?1",
        rusqlite::params![item_id, size, mtime, now],
    )?;
    Ok(())
}

/// Resets a cancelled or failed batch for another run; committed items stay committed.
pub fn resume(tx: &Transaction<'_>, batch_id: &str, now: &str) -> Result<i64, CoreError> {
    let reset = tx.execute(
        "UPDATE import_items SET state='pending',error=NULL,updated_at=?2 WHERE batch_id=?1 AND state IN ('failed','cancelled')",
        rusqlite::params![batch_id, now],
    )?;
    tx.execute(
        "UPDATE import_batches SET state='pending',error=NULL,failed_items=0,updated_at=?2 WHERE batch_id=?1",
        rusqlite::params![batch_id, now],
    )?;
    Ok(reset as i64)
}
