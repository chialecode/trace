//! Creators and their account bindings (read side).
use crate::error::CoreError;
use rusqlite::{Connection, OptionalExtension};
use serde_json::{Value, json};
use trace_contracts::control::CreatorDto;

/// A creator column a partial update can change.
#[derive(Debug, Clone, Copy)]
pub enum Field {
    DisplayName,
    Aliases,
    Tags,
    Notes,
    Status,
}

pub fn insert(
    c: &Connection,
    id: &str,
    display_name: &str,
    aliases_json: &str,
    tags_json: &str,
    notes: &str,
    now: &str,
) -> Result<(), CoreError> {
    c.execute(
        "INSERT INTO creators (creator_id, display_name, aliases, tags, status, notes, created_at, updated_at)
         VALUES (?1,?2,?3,?4,'active',?5,?6,?6)",
        rusqlite::params![id, display_name, aliases_json, tags_json, notes, now],
    )?;
    Ok(())
}

pub fn set_field(
    c: &Connection,
    id: &str,
    field: Field,
    value: &str,
    now: &str,
) -> Result<(), CoreError> {
    let sql = match field {
        Field::DisplayName => {
            "UPDATE creators SET display_name=?2, updated_at=?3 WHERE creator_id=?1"
        }
        Field::Aliases => "UPDATE creators SET aliases=?2, updated_at=?3 WHERE creator_id=?1",
        Field::Tags => "UPDATE creators SET tags=?2, updated_at=?3 WHERE creator_id=?1",
        Field::Notes => "UPDATE creators SET notes=?2, updated_at=?3 WHERE creator_id=?1",
        Field::Status => "UPDATE creators SET status=?2, updated_at=?3 WHERE creator_id=?1",
    };
    c.execute(sql, rusqlite::params![id, value, now])?;
    Ok(())
}

pub fn list_ids(c: &Connection, include_archived: bool) -> Result<Vec<String>, CoreError> {
    // A merged creator is not an independent entry any more: its id redirects to the target.
    let sql = if include_archived {
        "SELECT creator_id FROM creators WHERE status!='merged' ORDER BY created_at"
    } else {
        "SELECT creator_id FROM creators WHERE status='active' ORDER BY created_at"
    };
    let mut stmt = c.prepare(sql)?;
    let rows = stmt
        .query_map([], |r| r.get(0))?
        .collect::<Result<Vec<_>, _>>()?;
    Ok(rows)
}

/// Accounts bound to the creator, in the control protocol's JSON shape.
pub fn bound_accounts(c: &Connection, creator_id: &str) -> Result<Vec<Value>, CoreError> {
    let mut stmt = c.prepare(
        "SELECT a.account_id, a.namespace, a.native_account_key, a.display_name, a.profile_url
         FROM source_accounts a JOIN account_bindings b ON b.account_id=a.account_id
         WHERE b.creator_id=?1",
    )?;
    let rows = stmt
        .query_map([creator_id], |r| {
            Ok(json!({
                "account_id": r.get::<_, String>(0)?,
                "namespace": r.get::<_, String>(1)?,
                "native_account_key": r.get::<_, String>(2)?,
                "display_name": r.get::<_, Option<String>>(3)?,
                "profile_url": r.get::<_, Option<String>>(4)?,
            }))
        })?
        .collect::<Result<Vec<_>, _>>()?;
    Ok(rows)
}

pub fn dto(c: &Connection, id: &str) -> Result<CreatorDto, CoreError> {
    c.query_row(
        "SELECT creator_id, display_name, aliases, tags, status, notes, created_at, updated_at,
                (SELECT count(*) FROM account_bindings b WHERE b.creator_id=creators.creator_id AND b.state='active'),
                (SELECT count(*) FROM entries e JOIN account_bindings b2 ON b2.account_id=e.account_id WHERE b2.creator_id=creators.creator_id)
         FROM creators WHERE creator_id=?1",
        [id],
        |r| {
            let list = |column: usize| -> rusqlite::Result<Vec<String>> {
                let raw: String = r.get(column)?;
                serde_json::from_str(&raw).map_err(|error| {
                    rusqlite::Error::FromSqlConversionFailure(
                        column,
                        rusqlite::types::Type::Text,
                        Box::new(error),
                    )
                })
            };
            Ok(CreatorDto {
                creator_id: r.get(0)?,
                display_name: r.get(1)?,
                aliases: list(2)?,
                tags: list(3)?,
                status: r.get(4)?,
                notes: r.get(5)?,
                created_at: r.get(6)?,
                updated_at: r.get(7)?,
                account_count: r.get(8)?,
                entry_count: r.get(9)?,
            })
        },
    )
    .optional()?
    .ok_or_else(|| CoreError::NotFound("creator".into()))
}
