//! Profile previews: which page the user has reached and which entries they were shown.
use crate::error::CoreError;
use rusqlite::{Connection, OptionalExtension, Transaction, params};

#[derive(Clone)]
pub struct Preview {
    pub preview_id: String,
    pub platform: String,
    pub rule_id: String,
    pub rule_title: String,
    pub source_url: String,
    pub account_key: String,
    pub account_name: Option<String>,
    pub lane: Option<String>,
    pub credential_id: Option<String>,
    pub credential_generation: Option<i64>,
    pub authentication: String,
    pub pages: u32,
    pub next_cursor: Option<String>,
    pub has_more: bool,
    pub expires_at: String,
}

pub struct Shown<'a> {
    pub native_key: &'a str,
    pub native_kind: &'a str,
    pub title: &'a str,
}

pub fn insert(tx: &Transaction<'_>, p: &Preview, now: &str) -> Result<(), CoreError> {
    tx.execute(
        "INSERT INTO profile_previews(preview_id,platform,rule_id,rule_title,source_url,account_key,account_name,lane,credential_id,credential_generation,authentication,pages,next_cursor,has_more,created_at,expires_at) VALUES(?1,?2,?3,?13,?4,?5,?6,?7,?8,?9,?10,0,NULL,1,?11,?12)",
        params![
            p.preview_id,
            p.platform,
            p.rule_id,
            p.source_url,
            p.account_key,
            p.account_name,
            p.lane,
            p.credential_id,
            p.credential_generation,
            p.authentication,
            now,
            p.expires_at,
            p.rule_title
        ],
    )?;
    Ok(())
}

pub fn load(c: &Connection, id: &str) -> Result<Option<Preview>, CoreError> {
    Ok(c.query_row(
        "SELECT preview_id,platform,rule_id,rule_title,source_url,account_key,account_name,lane,credential_id,credential_generation,authentication,pages,next_cursor,has_more,expires_at FROM profile_previews WHERE preview_id=?1",
        [id],
        |r| {
            Ok(Preview {
                preview_id: r.get(0)?,
                platform: r.get(1)?,
                rule_id: r.get(2)?,
                rule_title: r.get(3)?,
                source_url: r.get(4)?,
                account_key: r.get(5)?,
                account_name: r.get(6)?,
                lane: r.get(7)?,
                credential_id: r.get(8)?,
                credential_generation: r.get(9)?,
                authentication: r.get(10)?,
                pages: r.get(11)?,
                next_cursor: r.get(12)?,
                has_more: r.get(13)?,
                expires_at: r.get(14)?,
            })
        },
    )
    .optional()?)
}

/// Records a loaded page: its new entries (those not already shown) and where to continue.
/// Returns the keys that were new, in order.
pub fn add_page(
    tx: &Transaction<'_>,
    id: &str,
    items: &[Shown<'_>],
    next_cursor: Option<&str>,
    has_more: bool,
    expected_pages: u32,
) -> Result<Vec<String>, CoreError> {
    // Two requests for the same page: only the one that saw the current page count wins.
    let moved = tx.execute(
        "UPDATE profile_previews SET pages=pages+1,next_cursor=?2,has_more=?3 WHERE preview_id=?1 AND pages=?4",
        params![id, next_cursor, has_more, expected_pages],
    )?;
    if moved != 1 {
        return Err(CoreError::VersionConflict(
            "预览已经翻到别的页，请刷新".into(),
        ));
    }
    let mut position: i64 = tx.query_row(
        "SELECT COALESCE(MAX(position)+1,0) FROM profile_preview_items WHERE preview_id=?1",
        [id],
        |r| r.get(0),
    )?;
    let mut fresh = Vec::new();
    for item in items {
        let added = tx.execute(
            "INSERT OR IGNORE INTO profile_preview_items(preview_id,position,native_key,native_kind,title) VALUES(?1,?2,?3,?4,?5)",
            params![id, position, item.native_key, item.native_kind, item.title],
        )?;
        if added == 1 {
            position += 1;
            fresh.push(item.native_key.to_owned());
        }
    }
    Ok(fresh)
}

pub fn shown_count(c: &Connection, id: &str) -> Result<u32, CoreError> {
    Ok(c.query_row(
        "SELECT count(*) FROM profile_preview_items WHERE preview_id=?1",
        [id],
        |r| r.get(0),
    )?)
}

/// What was shown for the given keys: `(native_key, native_kind, title)`.
pub fn shown(
    c: &Connection,
    id: &str,
    keys: &[String],
) -> Result<Vec<(String, String, String)>, CoreError> {
    let mut stmt = c.prepare(
        "SELECT native_key,native_kind,title FROM profile_preview_items WHERE preview_id=?1 AND native_key=?2",
    )?;
    let mut out = Vec::new();
    for key in keys {
        if let Some(row) = stmt
            .query_row(params![id, key], |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)))
            .optional()?
        {
            out.push(row);
        }
    }
    Ok(out)
}

/// Previews nobody can continue any more.
pub fn purge_expired(tx: &Transaction<'_>, now: &str) -> Result<(), CoreError> {
    tx.execute(
        "DELETE FROM profile_preview_items WHERE preview_id IN (SELECT preview_id FROM profile_previews WHERE julianday(expires_at)<julianday(?1))",
        [now],
    )?;
    tx.execute(
        "DELETE FROM profile_previews WHERE julianday(expires_at)<julianday(?1)",
        [now],
    )?;
    Ok(())
}

/// How TRACE already holds the given entries: `(native_key, entry_id, save progress inputs)`.
pub fn known_entries(
    c: &Connection,
    namespace: &str,
    keys: &[String],
) -> Result<Vec<(String, String, bool)>, CoreError> {
    let mut stmt = c.prepare(
        "SELECT entry_id,lifecycle='deleted' OR media_suppressed=1 FROM entries WHERE namespace=?1 AND native_entry_key=?2 AND EXISTS(SELECT 1 FROM entry_revisions r WHERE r.entry_id=entries.entry_id) ORDER BY native_kind LIMIT 1",
    )?;
    let mut out = Vec::new();
    for key in keys {
        if let Some((entry, blocked)) = stmt
            .query_row(params![namespace, key], |r| {
                Ok((r.get::<_, String>(0)?, r.get::<_, bool>(1)?))
            })
            .optional()?
        {
            out.push((key.clone(), entry, blocked));
        }
    }
    Ok(out)
}
