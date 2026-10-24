//! Quality policy storage for the control commands.
use crate::error::CoreError;
use rusqlite::Connection;

/// Remembers the policy so later evaluations use the same preference.
pub fn save_policy(c: &Connection, policy_json: &str) -> Result<(), CoreError> {
    c.execute(
        "INSERT INTO meta(key,value) VALUES('quality_policy',?1) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
        [policy_json],
    )?;
    Ok(())
}

pub fn current_revision(c: &Connection, entry_id: &str) -> Result<String, CoreError> {
    Ok(c.query_row(
        "SELECT revision_id FROM current_entry_revisions WHERE entry_id=?1",
        [entry_id],
        |r| r.get(0),
    )?)
}

/// The variant the archive currently uses for an entry, or its newest verified one.
pub fn primary_variant(c: &Connection, entry_id: &str) -> Result<Option<String>, CoreError> {
    use rusqlite::OptionalExtension;
    Ok(c.query_row(
        "SELECT COALESCE(
            (SELECT s.variant_id FROM asset_selections s JOIN media_items m ON m.media_id=s.media_id JOIN entry_revisions r ON r.revision_id=m.revision_id WHERE r.entry_id=?1 ORDER BY s.updated_at DESC LIMIT 1),
            (SELECT v.variant_id FROM media_variants v JOIN replicas p ON p.blob_id=v.blob_id JOIN media_items m ON m.media_id=v.media_id JOIN entry_revisions r ON r.revision_id=m.revision_id WHERE r.entry_id=?1 AND p.state='verified' ORDER BY v.rowid DESC LIMIT 1)
         )",
        [entry_id],
        |r| r.get::<_, Option<String>>(0),
    )
    .optional()?
    .flatten())
}
