//! Download folders, the copies TRACE recorded in them, and what an export reads from the
//! catalog. Folders and copies are records only: nothing here moves or deletes a user file.
use crate::error::CoreError;
use rusqlite::{Connection, OptionalExtension, Transaction, params};

#[derive(Debug, Clone)]
pub struct Folder {
    pub location_id: String,
    pub path: String,
    pub is_default: bool,
    pub last_used_at: Option<String>,
}

const FOLDER_COLUMNS: &str = "location_id,path,is_default,last_used_at";

fn folder_row(r: &rusqlite::Row<'_>) -> rusqlite::Result<Folder> {
    Ok(Folder {
        location_id: r.get(0)?,
        path: r.get(1)?,
        is_default: r.get(2)?,
        last_used_at: r.get(3)?,
    })
}

/// Download folders, the default first, then the most recently used.
pub fn folders(c: &Connection) -> Result<Vec<Folder>, CoreError> {
    let sql = format!(
        "SELECT {FOLDER_COLUMNS} FROM storage_locations WHERE kind='download' AND state='active'
         ORDER BY is_default DESC, COALESCE(last_used_at,'') DESC, created_at DESC LIMIT 20"
    );
    let rows = c
        .prepare(&sql)?
        .query_map([], folder_row)?
        .collect::<Result<_, _>>()?;
    Ok(rows)
}

pub fn default_folder(c: &Connection) -> Result<Option<Folder>, CoreError> {
    let sql = format!(
        "SELECT {FOLDER_COLUMNS} FROM storage_locations WHERE kind='download' AND is_default=1"
    );
    Ok(c.query_row(&sql, [], folder_row).optional()?)
}

pub fn folder(c: &Connection, id: &str) -> Result<Option<Folder>, CoreError> {
    let sql = format!(
        "SELECT {FOLDER_COLUMNS} FROM storage_locations WHERE kind='download' AND location_id=?1"
    );
    Ok(c.query_row(&sql, [id], folder_row).optional()?)
}

/// Records `path` as a download folder (once, whatever the letter case) and marks it used.
pub fn use_folder(tx: &Transaction<'_>, path: &str, now: &str) -> Result<String, CoreError> {
    let existing: Option<String> = tx
        .query_row(
            "SELECT location_id FROM storage_locations WHERE kind='download' AND path=?1 COLLATE NOCASE",
            [path],
            |r| r.get(0),
        )
        .optional()?;
    let id = match existing {
        Some(id) => {
            tx.execute(
                "UPDATE storage_locations SET state='active',last_used_at=?2 WHERE location_id=?1",
                params![id, now],
            )?;
            id
        }
        None => {
            let id = format!("dl-{}", uuid::Uuid::new_v4());
            tx.execute(
                "INSERT INTO storage_locations(location_id,kind,path,state,created_at,last_used_at) VALUES(?1,'download',?2,'active',?3,?3)",
                params![id, path, now],
            )?;
            id
        }
    };
    Ok(id)
}

/// Makes `path` the default download folder.
pub fn set_default(tx: &Transaction<'_>, path: &str, now: &str) -> Result<String, CoreError> {
    let id = use_folder(tx, path, now)?;
    tx.execute(
        "UPDATE storage_locations SET is_default=0 WHERE kind='download' AND is_default=1 AND location_id<>?1",
        [&id],
    )?;
    tx.execute(
        "UPDATE storage_locations SET is_default=1 WHERE location_id=?1",
        [&id],
    )?;
    Ok(id)
}

pub fn setting(c: &Connection, key: &str) -> Result<Option<String>, CoreError> {
    Ok(
        c.query_row("SELECT value FROM app_settings WHERE key=?1", [key], |r| {
            r.get(0)
        })
        .optional()?,
    )
}

pub fn put_setting(
    tx: &Transaction<'_>,
    key: &str,
    value: &str,
    now: &str,
) -> Result<(), CoreError> {
    tx.execute(
        "INSERT INTO app_settings(key,value,updated_at) VALUES(?1,?2,?3)
         ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at",
        params![key, value, now],
    )?;
    Ok(())
}

/// The facts of one entry that name its files and fill its sidecar.
#[derive(Debug, Clone)]
pub struct EntryFacts {
    pub entry_id: String,
    pub site: String,
    pub native_kind: String,
    pub native_key: String,
    pub author: String,
    pub author_key: String,
    pub published_at: Option<String>,
    pub revision_id: String,
    pub title: String,
    pub body: String,
    pub observed_at: String,
    pub lifecycle: String,
}

pub fn entry_facts(c: &Connection, entry_id: &str) -> Result<Option<EntryFacts>, CoreError> {
    Ok(c.query_row(
        "SELECT e.entry_id,e.namespace,e.native_kind,e.native_entry_key,COALESCE(a.display_name,''),a.native_account_key,
                e.published_at,r.revision_id,r.title,r.normalized_body,r.observed_at,e.lifecycle
         FROM entries e
         JOIN source_accounts a ON a.account_id=e.account_id
         JOIN current_entry_revisions r ON r.entry_id=e.entry_id
         WHERE e.entry_id=?1",
        [entry_id],
        |r| {
            Ok(EntryFacts {
                entry_id: r.get(0)?,
                site: r.get(1)?,
                native_kind: r.get(2)?,
                native_key: r.get(3)?,
                author: r.get(4)?,
                author_key: r.get(5)?,
                published_at: r.get(6)?,
                revision_id: r.get(7)?,
                title: r.get(8)?,
                body: r.get(9)?,
                observed_at: r.get(10)?,
                lifecycle: r.get(11)?,
            })
        },
    )
    .optional()?)
}

/// A media item of the current revision that can become a file, with the bytes the library
/// holds for it (none while it is deferred, failed or not downloaded yet).
#[derive(Debug, Clone)]
pub struct MediaFile {
    pub media_id: String,
    pub role: String,
    pub blob_id: Option<String>,
    pub size: Option<i64>,
    pub mime: Option<String>,
}

pub fn media_files(c: &Connection, revision_id: &str) -> Result<Vec<MediaFile>, CoreError> {
    let rows = c
        .prepare(
            "SELECT mi.media_id,mi.role,b.blob_id,b.size,b.mime
             FROM media_items mi
             LEFT JOIN media_variants mv ON mv.variant_id=COALESCE(
                 (SELECT s.variant_id FROM asset_selections s WHERE s.media_id=mi.media_id),
                 (SELECT v.variant_id FROM media_variants v WHERE v.media_id=mi.media_id
                  ORDER BY v.created_at DESC,v.variant_id DESC LIMIT 1))
             LEFT JOIN blobs b ON b.blob_id=mv.blob_id
                 AND EXISTS(SELECT 1 FROM replicas rp WHERE rp.blob_id=b.blob_id AND rp.state='verified')
             WHERE mi.revision_id=?1 AND mi.role IN ('image','video','motion','recording','video_segment')
             ORDER BY mi.position,mi.media_id",
        )?
        .query_map([revision_id], |r| {
            Ok(MediaFile {
                media_id: r.get(0)?,
                role: r.get(1)?,
                blob_id: r.get(2)?,
                size: r.get(3)?,
                mime: r.get(4)?,
            })
        })?
        .collect::<Result<_, _>>()?;
    Ok(rows)
}

#[derive(Debug, Clone)]
pub struct ExternalCopy {
    pub copy_id: String,
    pub entry_id: Option<String>,
    pub media_id: Option<String>,
    pub location_id: String,
    pub rel_path: String,
    pub role: String,
    pub size: i64,
    pub sha256: Option<String>,
    pub state: String,
    pub checked_at: Option<String>,
}

const COPY_COLUMNS: &str =
    "copy_id,entry_id,media_id,location_id,rel_path,role,size,sha256,state,checked_at";

fn copy_row(r: &rusqlite::Row<'_>) -> rusqlite::Result<ExternalCopy> {
    Ok(ExternalCopy {
        copy_id: r.get(0)?,
        entry_id: r.get(1)?,
        media_id: r.get(2)?,
        location_id: r.get(3)?,
        rel_path: r.get(4)?,
        role: r.get(5)?,
        size: r.get(6)?,
        sha256: r.get(7)?,
        state: r.get(8)?,
        checked_at: r.get(9)?,
    })
}

/// Copies of one entry, in one folder or in all of them.
pub fn copies_of_entry(
    c: &Connection,
    entry_id: &str,
    location_id: Option<&str>,
) -> Result<Vec<ExternalCopy>, CoreError> {
    let sql = format!(
        "SELECT {COPY_COLUMNS} FROM external_copies WHERE entry_id=?1 AND (?2 IS NULL OR location_id=?2) ORDER BY location_id,rel_path"
    );
    let rows = c
        .prepare(&sql)?
        .query_map(params![entry_id, location_id], copy_row)?
        .collect::<Result<_, _>>()?;
    Ok(rows)
}

/// Copies to re-check: one folder and/or one entry, or everything.
pub fn copies_to_check(
    c: &Connection,
    location_id: Option<&str>,
    entry_id: Option<&str>,
) -> Result<Vec<ExternalCopy>, CoreError> {
    let sql = format!(
        "SELECT {COPY_COLUMNS} FROM external_copies
         WHERE (?1 IS NULL OR location_id=?1) AND (?2 IS NULL OR entry_id=?2)
         ORDER BY location_id,rel_path"
    );
    let rows = c
        .prepare(&sql)?
        .query_map(params![location_id, entry_id], copy_row)?
        .collect::<Result<_, _>>()?;
    Ok(rows)
}

pub fn copy_at(
    c: &Connection,
    location_id: &str,
    rel_path: &str,
) -> Result<Option<ExternalCopy>, CoreError> {
    let sql = format!(
        "SELECT {COPY_COLUMNS} FROM external_copies WHERE location_id=?1 AND rel_path=?2 COLLATE NOCASE"
    );
    Ok(c.query_row(&sql, params![location_id, rel_path], copy_row)
        .optional()?)
}

/// Records a file that is about to be written (or resets the record of one that is written
/// again), with the length and digest it must end up with.
pub fn begin_copy(tx: &Transaction<'_>, copy: &ExternalCopy, now: &str) -> Result<(), CoreError> {
    tx.execute(
        "INSERT INTO external_copies(copy_id,entry_id,media_id,location_id,rel_path,role,size,sha256,state,created_at)
         VALUES(?1,?2,?3,?4,?5,?6,?7,?8,'writing',?9)
         ON CONFLICT(location_id,rel_path) DO UPDATE SET entry_id=excluded.entry_id,media_id=excluded.media_id,
             role=excluded.role,size=excluded.size,sha256=excluded.sha256,state='writing',checked_at=NULL",
        params![
            copy.copy_id,
            copy.entry_id,
            copy.media_id,
            copy.location_id,
            copy.rel_path,
            copy.role,
            copy.size,
            copy.sha256,
            now
        ],
    )?;
    Ok(())
}

pub fn finish_copy(
    tx: &Transaction<'_>,
    copy_id: &str,
    size: i64,
    sha256: &str,
    now: &str,
) -> Result<(), CoreError> {
    tx.execute(
        "UPDATE external_copies SET size=?2,sha256=?3,state='present',checked_at=?4 WHERE copy_id=?1",
        params![copy_id, size, sha256, now],
    )?;
    Ok(())
}

pub fn set_copy_state(
    tx: &Transaction<'_>,
    copy_id: &str,
    state: &str,
    now: &str,
) -> Result<(), CoreError> {
    tx.execute(
        "UPDATE external_copies SET state=?2,checked_at=?3 WHERE copy_id=?1",
        params![copy_id, state, now],
    )?;
    Ok(())
}

pub fn delete_copy(tx: &Transaction<'_>, copy_id: &str) -> Result<(), CoreError> {
    tx.execute("DELETE FROM external_copies WHERE copy_id=?1", [copy_id])?;
    Ok(())
}

/// Forgets the records of an entry (in one folder or all); files stay where they are.
pub fn forget(
    tx: &Transaction<'_>,
    entry_id: &str,
    location_id: Option<&str>,
) -> Result<i64, CoreError> {
    let n = tx.execute(
        "DELETE FROM external_copies WHERE entry_id=?1 AND (?2 IS NULL OR location_id=?2)",
        params![entry_id, location_id],
    )?;
    Ok(n as i64)
}

/// State of the library's verified copy of a blob: whether one exists.
pub fn has_verified_replica(c: &Connection, blob_id: &str) -> Result<bool, CoreError> {
    Ok(c.query_row(
        "SELECT EXISTS(SELECT 1 FROM replicas WHERE blob_id=?1 AND state='verified')",
        [blob_id],
        |r| r.get(0),
    )?)
}

/// Marks the library object of a blob verified again after it was restored from a copy.
pub fn mark_replica_restored(
    tx: &Transaction<'_>,
    blob_id: &str,
    digest: &str,
    now: &str,
) -> Result<(), CoreError> {
    let updated = tx.execute(
        "UPDATE replicas SET state='verified',verified_at=?2,locator=?3 WHERE blob_id=?1 AND store_id='primary' AND state<>'verified'",
        params![blob_id, now, digest],
    )?;
    if updated == 0 {
        tx.execute(
            "INSERT OR IGNORE INTO replicas(replica_id,blob_id,node_id,store_id,locator,managed,state,verified_at)
             VALUES(?1,?2,'local','primary',?3,1,'verified',?4)",
            params![format!("sync-{digest}"), blob_id, digest, now],
        )?;
    }
    Ok(())
}

/// The library object a media item points at, whatever the state of its replica.
pub fn media_blob(c: &Connection, media_id: &str) -> Result<Option<(String, i64)>, CoreError> {
    Ok(c.query_row(
        "SELECT b.blob_id,b.size FROM media_variants mv JOIN blobs b ON b.blob_id=mv.blob_id
         WHERE mv.variant_id=COALESCE(
             (SELECT s.variant_id FROM asset_selections s WHERE s.media_id=?1),
             (SELECT v.variant_id FROM media_variants v WHERE v.media_id=?1 ORDER BY v.created_at DESC,v.variant_id DESC LIMIT 1))",
        [media_id],
        |r| Ok((r.get(0)?, r.get(1)?)),
    )
    .optional()?)
}
