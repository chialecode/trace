//! Entry facts: where an entry lives at its source and its orthogonal states.
use crate::error::CoreError;
use rusqlite::Connection;
use serde_json::{Map, Value};
use trace_domain::quality::{ResourceSave, save_progress};

/// Namespace, native kind and native key of an entry.
pub fn native_identity(
    c: &Connection,
    entry_id: &str,
) -> Result<(String, String, String), CoreError> {
    Ok(c.query_row(
        "SELECT namespace,native_kind,native_entry_key FROM entries WHERE entry_id=?1",
        [entry_id],
        |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)),
    )?)
}

/// Records the publish time an observation gave. A date from the source replaces what the
/// list guessed; a guess never replaces a date from the source, and an observation without a
/// date leaves the entry as it is.
pub fn apply_published(
    c: &Connection,
    entry_id: &str,
    published_at: Option<&str>,
    precision: &str,
) -> Result<(), CoreError> {
    use time::format_description::well_known::Rfc3339;
    let Some(date) = published_at else {
        return Ok(());
    };
    if time::OffsetDateTime::parse(date, &Rfc3339).is_err() {
        return Ok(());
    }
    let precision = match precision {
        "unknown" | "discovery" => return Ok(()),
        "" => "source",
        known @ ("approximate" | "source" | "second" | "minute" | "millisecond" | "day") => known,
        _ => "source",
    };
    c.execute(
        "UPDATE entries SET published_at=?2, published_precision=?3
         WHERE entry_id=?1
           AND (?3!='approximate' OR published_at IS NULL OR COALESCE(published_precision,'') IN ('','approximate','unknown','discovery'))",
        rusqlite::params![entry_id, date, precision],
    )?;
    Ok(())
}

/// Where an entry lives at its source, with the account that owns it.
pub struct Source {
    pub namespace: String,
    pub native_key: String,
    pub account_id: String,
    pub native_kind: String,
}

pub fn source(c: &Connection, entry_id: &str) -> Result<Source, CoreError> {
    Ok(c.query_row(
        "SELECT namespace, native_entry_key, account_id, native_kind FROM entries WHERE entry_id=?1",
        [entry_id],
        |r| {
            Ok(Source {
                namespace: r.get(0)?,
                native_key: r.get(1)?,
                account_id: r.get(2)?,
                native_kind: r.get(3)?,
            })
        },
    )?)
}

/// Access state from the latest observation, `unknown` before the first one.
fn access_state(c: &Connection, entry: &str) -> String {
    c.prepare_cached("SELECT access_state FROM source_observations WHERE entry_id=?1 ORDER BY observation_seq DESC LIMIT 1")
        .and_then(|mut statement| statement.query_row([entry], |r| r.get(0)))
        .unwrap_or_else(|_| "unknown".into())
}

/// Required resources of the current revision and whether each has a verified replica.
fn resource_saves(c: &Connection, entry: &str) -> Result<Vec<ResourceSave>, CoreError> {
    let mut stmt = c.prepare_cached(
        "SELECT mi.required, EXISTS(SELECT 1 FROM media_variants mv JOIN replicas r ON r.blob_id=mv.blob_id AND r.state='verified' WHERE mv.media_id=mi.media_id), mi.fetch_state='deferred' FROM media_items mi WHERE mi.revision_id=(SELECT revision_id FROM current_entry_revisions WHERE entry_id=?1)",
    )?;
    let rows = stmt
        .query_map([entry], |r| {
            Ok(ResourceSave {
                required: r.get(0)?,
                has_verified_blob: r.get(1)?,
                deferred: r.get(2)?,
            })
        })?
        .collect::<Result<Vec<_>, _>>()?;
    Ok(rows)
}

/// Whether the best revision holds the full body.
fn body_complete(c: &Connection, entry: &str) -> bool {
    c.prepare_cached("SELECT completeness='full' FROM entry_revisions WHERE entry_id=?1 ORDER BY (completeness='full') DESC,observed_at DESC LIMIT 1")
        .and_then(|mut statement| statement.query_row([entry], |r| r.get(0)))
        .unwrap_or(false)
}

/// Dynamics, opus posts and articles carry their content as text, not as media.
fn is_text_kind(c: &Connection, entry: &str) -> bool {
    c.prepare_cached(
        "SELECT native_kind IN ('dynamic','opus','article') FROM entries WHERE entry_id=?1",
    )
    .and_then(|mut statement| statement.query_row([entry], |r| r.get(0)))
    .unwrap_or(false)
}

/// A live entry is incomplete while its session or any fragment is not sealed.
fn live_incomplete(c: &Connection, entry: &str) -> Result<bool, CoreError> {
    Ok(c.prepare_cached("SELECT EXISTS(SELECT 1 FROM live_sessions l WHERE l.entry_id=?1 AND (l.state!='sealed' OR EXISTS(SELECT 1 FROM live_fragments f WHERE f.session_id=l.session_id AND f.state NOT IN ('sealed','superseded','source'))))")?
        .query_row([entry], |r| r.get(0))?)
}

/// Media item counts of the current revision by role.
fn media_roles(c: &Connection, entry: &str) -> Result<Map<String, Value>, CoreError> {
    let mut stmt = c.prepare_cached(
        "SELECT role, count(*) FROM media_items mi
         WHERE mi.revision_id = (SELECT revision_id FROM current_entry_revisions WHERE entry_id=?1)
         GROUP BY role",
    )?;
    let roles = stmt
        .query_map([entry], |r| {
            Ok((r.get::<_, String>(0)?, r.get::<_, i64>(1)?))
        })?
        .collect::<Result<Vec<_>, _>>()?
        .into_iter()
        .map(|(role, count)| (role, Value::from(count)))
        .collect();
    Ok(roles)
}

/// Orthogonal states of one entry: access, save progress, quality and media roles.
pub(crate) fn states(
    c: &Connection,
    entry: &str,
) -> Result<(String, String, String, Value), CoreError> {
    let access = access_state(c, entry);
    let resources = resource_saves(c, entry)?;
    let mut progress = serde_json::to_value(save_progress(&resources))?
        .as_str()
        .unwrap_or("unknown")
        .to_owned();
    let body_complete = body_complete(c, entry);
    if !body_complete && progress == "complete" {
        progress = "partial".into();
    }
    if body_complete && resources.is_empty() && is_text_kind(c, entry) {
        progress = "complete".into();
    }
    if live_incomplete(c, entry)? && progress == "complete" {
        progress = "partial".into();
    }
    let quality = crate::quality::state(c, entry)?;
    Ok((
        access,
        progress,
        quality,
        Value::Object(media_roles(c, entry)?),
    ))
}

/// SQL for the save progress of the current revision `r` of entry `e`; the set-based twin of
/// [`states`], shared by the timeline and the explorer so both rank the same way.
pub const SAVE_PROGRESS_SQL: &str = "CASE WHEN r.completeness!='full' THEN 'partial' WHEN e.native_kind='live' AND EXISTS(SELECT 1 FROM live_sessions l WHERE l.entry_id=e.entry_id AND (l.state!='sealed' OR EXISTS(SELECT 1 FROM live_fragments f WHERE f.session_id=l.session_id AND f.state NOT IN ('sealed','superseded','source')))) THEN 'partial' WHEN NOT EXISTS(SELECT 1 FROM media_items m WHERE m.revision_id=r.revision_id AND m.required=1) THEN CASE WHEN e.native_kind IN ('dynamic','opus','article') THEN 'complete' ELSE 'unknown' END
          WHEN NOT EXISTS(SELECT 1 FROM media_items m WHERE m.revision_id=r.revision_id AND m.required=1 AND m.fetch_state='wanted' AND NOT EXISTS(SELECT 1 FROM media_variants v JOIN replicas rp ON rp.blob_id=v.blob_id AND rp.state='verified' WHERE v.media_id=m.media_id)) THEN CASE WHEN EXISTS(SELECT 1 FROM media_items m WHERE m.revision_id=r.revision_id AND m.required=1 AND m.fetch_state='deferred' AND NOT EXISTS(SELECT 1 FROM media_variants v JOIN replicas rp ON rp.blob_id=v.blob_id AND rp.state='verified' WHERE v.media_id=m.media_id)) THEN 'deferred' ELSE 'complete' END
          WHEN EXISTS(SELECT 1 FROM media_items m JOIN media_variants v ON v.media_id=m.media_id JOIN replicas rp ON rp.blob_id=v.blob_id AND rp.state='verified' WHERE m.revision_id=r.revision_id AND m.required=1) THEN 'partial' ELSE 'pending' END";
