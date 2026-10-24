//! Idempotency receipts of write commands (INV-09).
use crate::error::CoreError;
use rusqlite::Connection;

/// The stored result of an earlier command with the same key.
pub fn find(c: &Connection, key: &str) -> Option<String> {
    c.query_row(
        "SELECT result_json FROM receipts WHERE idempotency_key=?1",
        [key],
        |r| r.get::<_, String>(0),
    )
    .ok()
}

pub fn insert(c: &Connection, key: &str, result_json: &str, now: &str) -> Result<(), CoreError> {
    c.execute(
        "INSERT OR IGNORE INTO receipts (idempotency_key, command, result_json, created_at)
         VALUES (?1,?2,?3,?4)",
        rusqlite::params![key, "receipt", result_json, now],
    )?;
    Ok(())
}
