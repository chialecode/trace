//! Library locations and the resumable move between them.
use crate::error::CoreError;
use rusqlite::{Connection, OptionalExtension, Transaction, params};

#[derive(Debug, Clone)]
pub struct Location {
    pub location_id: String,
    pub path: String,
    pub state: String,
    pub created_at: String,
}

#[derive(Debug, Clone)]
pub struct Migration {
    pub migration_id: String,
    pub from_path: String,
    pub to_path: String,
    pub state: String,
    pub total_items: i64,
    pub done_items: i64,
    pub total_bytes: i64,
    pub done_bytes: i64,
    pub error: Option<String>,
    pub created_at: String,
    pub updated_at: String,
    pub switched_at: Option<String>,
}

#[derive(Debug, Clone)]
pub struct Item {
    /// Path relative to the library root.
    pub rel: String,
    pub size: i64,
    pub sha256: Option<String>,
    pub state: String,
}

const LOCATION_COLUMNS: &str = "location_id,path,state,created_at";

fn location_row(r: &rusqlite::Row<'_>) -> rusqlite::Result<Location> {
    Ok(Location {
        location_id: r.get(0)?,
        path: r.get(1)?,
        state: r.get(2)?,
        created_at: r.get(3)?,
    })
}

pub fn active_library(c: &Connection) -> Result<Option<Location>, CoreError> {
    Ok(c.query_row(
        &format!(
            "SELECT {LOCATION_COLUMNS} FROM storage_locations WHERE kind='library' AND state='active'"
        ),
        [],
        location_row,
    )
    .optional()?)
}

/// Replaced libraries still holding a full copy, oldest first.
pub fn old_kept(c: &Connection) -> Result<Vec<Location>, CoreError> {
    let mut stmt = c.prepare_cached(&format!(
        "SELECT {LOCATION_COLUMNS} FROM storage_locations WHERE kind='library' AND state='old_kept' ORDER BY created_at,location_id"
    ))?;
    Ok(stmt
        .query_map([], location_row)?
        .collect::<Result<Vec<_>, _>>()?)
}

pub fn location(c: &Connection, id: &str) -> Result<Option<Location>, CoreError> {
    Ok(c.query_row(
        &format!(
            "SELECT {LOCATION_COLUMNS} FROM storage_locations WHERE location_id=?1 AND kind='library'"
        ),
        [id],
        location_row,
    )
    .optional()?)
}

/// Makes `path` the active library row: records it when there is none, corrects the path when
/// the recorded one is gone (a restored or moved profile).
pub fn register_active(tx: &Transaction<'_>, path: &str, now: &str) -> Result<(), CoreError> {
    let current: Option<(String, String)> = tx
        .query_row(
            "SELECT location_id,path FROM storage_locations WHERE kind='library' AND state='active'",
            [],
            |r| Ok((r.get(0)?, r.get(1)?)),
        )
        .optional()?;
    match current {
        Some((_, existing)) if existing == path => {}
        Some((id, _)) => {
            tx.execute(
                "DELETE FROM storage_locations WHERE kind='library' AND path=?1 AND location_id<>?2",
                params![path, id],
            )?;
            tx.execute(
                "UPDATE storage_locations SET path=?2 WHERE location_id=?1",
                params![id, path],
            )?;
        }
        None => {
            tx.execute(
                "DELETE FROM storage_locations WHERE kind='library' AND path=?1",
                [path],
            )?;
            tx.execute(
                "INSERT INTO storage_locations(location_id,kind,path,state,created_at) VALUES(?1,'library',?2,'active',?3)",
                params![format!("lib-{}", uuid::Uuid::new_v4()), path, now],
            )?;
        }
    }
    Ok(())
}

const MIGRATION_COLUMNS: &str = "migration_id,from_path,to_path,state,total_items,done_items,total_bytes,done_bytes,error,created_at,updated_at,switched_at";

fn migration_row(r: &rusqlite::Row<'_>) -> rusqlite::Result<Migration> {
    Ok(Migration {
        migration_id: r.get(0)?,
        from_path: r.get(1)?,
        to_path: r.get(2)?,
        state: r.get(3)?,
        total_items: r.get(4)?,
        done_items: r.get(5)?,
        total_bytes: r.get(6)?,
        done_bytes: r.get(7)?,
        error: r.get(8)?,
        created_at: r.get(9)?,
        updated_at: r.get(10)?,
        switched_at: r.get(11)?,
    })
}

pub fn open_migration(c: &Connection) -> Result<Option<Migration>, CoreError> {
    Ok(c.query_row(
        &format!(
            "SELECT {MIGRATION_COLUMNS} FROM library_migrations WHERE state IN ('copying','verifying','ready')"
        ),
        [],
        migration_row,
    )
    .optional()?)
}

/// The open move, or else the latest one that ended.
pub fn current_migration(c: &Connection) -> Result<Option<Migration>, CoreError> {
    if let Some(open) = open_migration(c)? {
        return Ok(Some(open));
    }
    Ok(c.query_row(
        &format!(
            "SELECT {MIGRATION_COLUMNS} FROM library_migrations ORDER BY created_at DESC,migration_id DESC LIMIT 1"
        ),
        [],
        migration_row,
    )
    .optional()?)
}

pub fn migration(c: &Connection, id: &str) -> Result<Option<Migration>, CoreError> {
    Ok(c.query_row(
        &format!("SELECT {MIGRATION_COLUMNS} FROM library_migrations WHERE migration_id=?1"),
        [id],
        migration_row,
    )
    .optional()?)
}

/// Moves that stopped without switching (failed or cancelled), newest first: a retry to the
/// same folder continues one of them.
pub fn closed_moves(c: &Connection) -> Result<Vec<Migration>, CoreError> {
    let mut stmt = c.prepare_cached(&format!(
        "SELECT {MIGRATION_COLUMNS} FROM library_migrations WHERE state IN ('failed','cancelled') ORDER BY created_at DESC,migration_id DESC"
    ))?;
    Ok(stmt
        .query_map([], migration_row)?
        .collect::<Result<Vec<_>, _>>()?)
}

pub fn create_migration(
    tx: &Transaction<'_>,
    id: &str,
    from: &str,
    to: &str,
    now: &str,
) -> Result<(), CoreError> {
    tx.execute(
        "INSERT INTO library_migrations(migration_id,from_path,to_path,state,created_at,updated_at) VALUES(?1,?2,?3,'copying',?4,?4)",
        params![id, from, to, now],
    )?;
    Ok(())
}

pub fn set_state(
    tx: &Transaction<'_>,
    id: &str,
    state: &str,
    error: Option<&str>,
    now: &str,
) -> Result<(), CoreError> {
    tx.execute(
        "UPDATE library_migrations SET state=?2,error=?3,updated_at=?4 WHERE migration_id=?1",
        params![id, state, error, now],
    )?;
    Ok(())
}

/// Like [`set_state`], but only while the move is still open: a move the user cancelled (or
/// that already switched) is not revived by a late result from its runner.
pub fn set_state_if_open(
    tx: &Transaction<'_>,
    id: &str,
    state: &str,
    error: Option<&str>,
    now: &str,
) -> Result<(), CoreError> {
    tx.execute(
        "UPDATE library_migrations SET state=?2,error=?3,updated_at=?4 WHERE migration_id=?1 AND state IN ('copying','verifying','ready')",
        params![id, state, error, now],
    )?;
    Ok(())
}

pub fn add_items(
    tx: &Transaction<'_>,
    id: &str,
    items: &[(String, i64)],
) -> Result<usize, CoreError> {
    let mut insert = tx.prepare_cached(
        "INSERT OR IGNORE INTO library_migration_items(migration_id,blob_id,size) VALUES(?1,?2,?3)",
    )?;
    let mut added = 0;
    for (rel, size) in items {
        added += insert.execute(params![id, rel, size])?;
    }
    Ok(added)
}

pub fn items(c: &Connection, id: &str) -> Result<Vec<Item>, CoreError> {
    let mut stmt = c.prepare_cached(
        "SELECT blob_id,size,sha256,state FROM library_migration_items WHERE migration_id=?1 ORDER BY blob_id",
    )?;
    Ok(stmt
        .query_map([id], |r| {
            Ok(Item {
                rel: r.get(0)?,
                size: r.get(1)?,
                sha256: r.get(2)?,
                state: r.get(3)?,
            })
        })?
        .collect::<Result<Vec<_>, _>>()?)
}

pub fn items_in_state(
    c: &Connection,
    id: &str,
    state: &str,
    limit: i64,
) -> Result<Vec<Item>, CoreError> {
    let mut stmt = c.prepare_cached(
        "SELECT blob_id,size,sha256,state FROM library_migration_items WHERE migration_id=?1 AND state=?2 ORDER BY blob_id LIMIT ?3",
    )?;
    Ok(stmt
        .query_map(params![id, state, limit], |r| {
            Ok(Item {
                rel: r.get(0)?,
                size: r.get(1)?,
                sha256: r.get(2)?,
                state: r.get(3)?,
            })
        })?
        .collect::<Result<Vec<_>, _>>()?)
}

pub fn update_item(tx: &Transaction<'_>, id: &str, item: &Item) -> Result<(), CoreError> {
    tx.execute(
        "UPDATE library_migration_items SET size=?3,sha256=?4,state=?5 WHERE migration_id=?1 AND blob_id=?2",
        params![id, item.rel, item.size, item.sha256, item.state],
    )?;
    Ok(())
}

pub fn remove_items(tx: &Transaction<'_>, id: &str, rels: &[String]) -> Result<(), CoreError> {
    let mut delete = tx.prepare_cached(
        "DELETE FROM library_migration_items WHERE migration_id=?1 AND blob_id=?2",
    )?;
    for rel in rels {
        delete.execute(params![id, rel])?;
    }
    Ok(())
}

/// Recomputes the progress counters from the item rows.
pub fn refresh_counters(tx: &Transaction<'_>, id: &str, now: &str) -> Result<(), CoreError> {
    tx.execute(
        "UPDATE library_migrations SET
            total_items=(SELECT COUNT(*) FROM library_migration_items WHERE migration_id=?1),
            total_bytes=(SELECT COALESCE(SUM(size),0) FROM library_migration_items WHERE migration_id=?1),
            done_items=(SELECT COUNT(*) FROM library_migration_items WHERE migration_id=?1 AND state<>'pending'),
            done_bytes=(SELECT COALESCE(SUM(size),0) FROM library_migration_items WHERE migration_id=?1 AND state<>'pending'),
            updated_at=?2
         WHERE migration_id=?1",
        params![id, now],
    )?;
    Ok(())
}

/// The single commit of a move: the new folder becomes the library, the old one is kept.
pub fn switch(
    tx: &Transaction<'_>,
    id: &str,
    from: &str,
    to: &str,
    now: &str,
) -> Result<(), CoreError> {
    tx.execute(
        "UPDATE storage_locations SET state='old_kept' WHERE kind='library' AND state='active' AND path=?1",
        [from],
    )?;
    let existing: Option<String> = tx
        .query_row(
            "SELECT location_id FROM storage_locations WHERE kind='library' AND path=?1",
            [to],
            |r| r.get(0),
        )
        .optional()?;
    match existing {
        Some(location) => tx.execute(
            "UPDATE storage_locations SET state='active' WHERE location_id=?1",
            [location],
        )?,
        None => tx.execute(
            "INSERT INTO storage_locations(location_id,kind,path,state,created_at) VALUES(?1,'library',?2,'active',?3)",
            params![format!("lib-{}", uuid::Uuid::new_v4()), to, now],
        )?,
    };
    tx.execute(
        "UPDATE library_migrations SET state='switched',error=NULL,switched_at=?2,updated_at=?2 WHERE migration_id=?1",
        params![id, now],
    )?;
    crate::objects::outbox_emit(
        tx,
        "library.switched",
        id,
        &serde_json::json!({"migration_id": id}),
    )?;
    Ok(())
}

pub fn release_location(
    tx: &Transaction<'_>,
    location_id: &str,
    now: &str,
) -> Result<(), CoreError> {
    tx.execute(
        "UPDATE storage_locations SET state='released' WHERE location_id=?1 AND state='old_kept'",
        [location_id],
    )?;
    tx.execute(
        "UPDATE library_migrations SET state='released',updated_at=?2 WHERE state='switched' AND from_path=(SELECT path FROM storage_locations WHERE location_id=?1)",
        params![location_id, now],
    )?;
    Ok(())
}

/// A restore starts a new library; nothing about the source machine's folders applies.
pub fn reset_for_restore(c: &Connection) -> Result<(), CoreError> {
    c.execute("DELETE FROM library_migration_items", [])?;
    c.execute("DELETE FROM library_migrations", [])?;
    c.execute("DELETE FROM storage_locations WHERE kind='library'", [])?;
    Ok(())
}
