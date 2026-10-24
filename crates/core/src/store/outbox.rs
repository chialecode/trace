//! Change events written in the same transaction as the change.
use crate::error::CoreError;
use rusqlite::Connection;

/// One outbox row: sequence, topic, aggregate id, payload text, creation time.
pub type Row = (i64, String, String, String, String);

/// Sequence of the newest event; a new client subscription starts after it.
pub fn latest_seq(c: &Connection) -> Result<i64, CoreError> {
    Ok(c.query_row("SELECT COALESCE(max(seq),0) FROM outbox", [], |r| r.get(0))?)
}

/// Up to `limit` events after `seq`, oldest first.
pub fn after(c: &Connection, seq: i64, limit: i64) -> Result<Vec<Row>, CoreError> {
    let mut stmt = c.prepare(
        "SELECT seq, topic, aggregate_id, payload, created_at FROM outbox WHERE seq > ?1 ORDER BY seq LIMIT ?2",
    )?;
    let rows = stmt
        .query_map(rusqlite::params![seq, limit], |r| {
            Ok((r.get(0)?, r.get(1)?, r.get(2)?, r.get(3)?, r.get(4)?))
        })?
        .collect::<Result<Vec<_>, _>>()?;
    Ok(rows)
}
