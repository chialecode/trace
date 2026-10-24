//! Recorded sessions linked to later replay entries (stage plan §11.3). Read-only for archives.
use crate::error::CoreError;
use rusqlite::{Connection, OptionalExtension};
use serde_json::{Value, json};

#[derive(Debug, Clone)]
pub struct SessionRef {
    pub session_id: String,
    pub entry_id: Option<String>,
    pub account_id: String,
    pub state: String,
    pub started_at: String,
    pub ended_at: Option<String>,
    pub room_key: String,
}

#[derive(Debug, Clone)]
pub struct EntryRef {
    pub entry_id: String,
    pub account_id: String,
    pub published_at: Option<String>,
    pub title: String,
}

pub fn session(c: &Connection, session_id: &str) -> Result<SessionRef, CoreError> {
    c.query_row(
        "SELECT l.session_id,l.entry_id,s.account_id,l.state,l.started_at,l.ended_at,l.room_key
         FROM live_sessions l JOIN subscriptions s ON s.subscription_id=l.subscription_id
         WHERE l.session_id=?1",
        [session_id],
        |r| {
            Ok(SessionRef {
                session_id: r.get(0)?,
                entry_id: r.get(1)?,
                account_id: r.get(2)?,
                state: r.get(3)?,
                started_at: r.get(4)?,
                ended_at: r.get(5)?,
                room_key: r.get(6)?,
            })
        },
    )
    .optional()?
    .ok_or_else(|| CoreError::NotFound("live session".into()))
}

pub fn entry(c: &Connection, entry_id: &str) -> Result<EntryRef, CoreError> {
    c.query_row(
        "SELECT e.entry_id,e.account_id,e.published_at,COALESCE(r.title,'')
         FROM entries e LEFT JOIN current_entry_revisions r ON r.entry_id=e.entry_id
         WHERE e.entry_id=?1 AND e.lifecycle!='deleted'",
        [entry_id],
        |r| {
            Ok(EntryRef {
                entry_id: r.get(0)?,
                account_id: r.get(1)?,
                published_at: r.get(2)?,
                title: r.get(3)?,
            })
        },
    )
    .optional()?
    .ok_or_else(|| CoreError::NotFound("entry".into()))
}

/// Sealed fragment intervals of the session, independent of which track they carry.
pub fn ranges(c: &Connection, session_id: &str) -> Result<Vec<(i64, i64)>, CoreError> {
    let mut stmt = c.prepare(
        "SELECT start_ms,end_ms FROM live_fragments
         WHERE session_id=?1 AND state='sealed' AND start_ms IS NOT NULL AND end_ms IS NOT NULL AND end_ms>start_ms
         ORDER BY start_ms,end_ms",
    )?;
    let rows = stmt
        .query_map([session_id], |r| Ok((r.get(0)?, r.get(1)?)))?
        .collect::<Result<Vec<_>, _>>()?;
    Ok(rows)
}

/// The measured specification of the recording's selected variants (the best one it has).
pub fn recording_spec(c: &Connection, session_id: &str) -> Result<Option<Value>, CoreError> {
    let raw: Option<String> = c
        .query_row(
            "SELECT v.measured_spec FROM live_fragments f
             JOIN media_items m ON m.media_id=f.media_id
             LEFT JOIN asset_selections s ON s.media_id=m.media_id
             JOIN media_variants v ON v.variant_id=COALESCE(s.variant_id,(SELECT mv.variant_id FROM media_variants mv WHERE mv.media_id=m.media_id ORDER BY mv.created_at DESC LIMIT 1))
             WHERE f.session_id=?1 AND v.measured_spec IS NOT NULL AND v.measured_spec!='null'
             ORDER BY v.measured_spec DESC LIMIT 1",
            [session_id],
            |r| r.get(0),
        )
        .optional()?;
    Ok(raw.and_then(|s| serde_json::from_str::<Value>(&s).ok()))
}

pub fn replay_spec(c: &Connection, entry_id: &str) -> Result<Option<Value>, CoreError> {
    let raw: Option<String> = c
        .query_row(
            "SELECT v.measured_spec FROM media_items m
             JOIN entry_revisions r ON r.revision_id=m.revision_id
             LEFT JOIN asset_selections s ON s.media_id=m.media_id
             JOIN media_variants v ON v.variant_id=COALESCE(s.variant_id,(SELECT mv.variant_id FROM media_variants mv WHERE mv.media_id=m.media_id ORDER BY mv.created_at DESC LIMIT 1))
             WHERE r.entry_id=?1 AND m.role IN ('video','motion','media') AND v.measured_spec IS NOT NULL
             ORDER BY v.measured_spec DESC LIMIT 1",
            [entry_id],
            |r| r.get(0),
        )
        .optional()?;
    Ok(raw.and_then(|s| serde_json::from_str::<Value>(&s).ok()))
}

pub fn existing(
    c: &Connection,
    session_id: &str,
    entry_id: &str,
) -> Result<Option<Value>, CoreError> {
    Ok(c.query_row(
        "SELECT link_id,state,decision,coverage_json,quality_json,evidence_json,created_at,decided_at
         FROM live_replay_links WHERE session_id=?1 AND entry_id=?2",
        rusqlite::params![session_id, entry_id],
        |r| {
            Ok(json!({
                "link_id": r.get::<_, String>(0)?,
                "state": r.get::<_, String>(1)?,
                "decision": r.get::<_, Option<String>>(2)?,
                "coverage": serde_json::from_str::<Value>(&r.get::<_, String>(3)?).unwrap_or(Value::Null),
                "quality": serde_json::from_str::<Value>(&r.get::<_, String>(4)?).unwrap_or(Value::Null),
                "evidence": serde_json::from_str::<Value>(&r.get::<_, String>(5)?).unwrap_or(Value::Null),
                "created_at": r.get::<_, String>(6)?,
                "decided_at": r.get::<_, Option<String>>(7)?,
            }))
        },
    )
    .optional()?)
}

#[allow(clippy::too_many_arguments)]
pub fn insert_link(
    c: &Connection,
    link_id: &str,
    session_id: &str,
    entry_id: &str,
    decision: &str,
    coverage: &Value,
    quality: &Value,
    evidence: &Value,
    now: &str,
) -> Result<(), CoreError> {
    c.execute(
        "INSERT INTO live_replay_links(link_id,session_id,entry_id,state,decision,coverage_json,quality_json,evidence_json,created_at,decided_at)
         VALUES(?1,?2,?3,'confirmed',?4,?5,?6,?7,?8,?8)
         ON CONFLICT(session_id,entry_id) DO UPDATE SET state='confirmed',decision=excluded.decision,coverage_json=excluded.coverage_json,quality_json=excluded.quality_json,evidence_json=excluded.evidence_json,decided_at=excluded.decided_at",
        rusqlite::params![link_id, session_id, entry_id, decision, coverage.to_string(), quality.to_string(), evidence.to_string(), now],
    )?;
    Ok(())
}

pub fn reject(
    c: &Connection,
    link_id: &str,
    session_id: &str,
    entry_id: &str,
    reason: &str,
    now: &str,
) -> Result<bool, CoreError> {
    let updated = c.execute(
        "INSERT INTO live_replay_links(link_id,session_id,entry_id,state,decision,coverage_json,quality_json,evidence_json,created_at,decided_at)
         VALUES(?1,?2,?3,'rejected',NULL,'{}','{}',?4,?5,?5)
         ON CONFLICT(session_id,entry_id) DO UPDATE SET state='rejected',decision=NULL,evidence_json=excluded.evidence_json,decided_at=excluded.decided_at",
        rusqlite::params![link_id, session_id, entry_id, json!({"reason": reason}).to_string(), now],
    )?;
    Ok(updated > 0)
}

pub fn links(
    c: &Connection,
    session_id: Option<&str>,
    entry_id: Option<&str>,
) -> Result<Value, CoreError> {
    let mut stmt = c.prepare(
        "SELECT l.link_id,l.session_id,l.entry_id,l.state,l.decision,l.coverage_json,l.quality_json,l.created_at,l.decided_at,
                COALESCE(r.title,''),e.namespace
         FROM live_replay_links l JOIN entries e ON e.entry_id=l.entry_id
         LEFT JOIN current_entry_revisions r ON r.entry_id=l.entry_id
         WHERE (?1 IS NULL OR l.session_id=?1) AND (?2 IS NULL OR l.entry_id=?2)
         ORDER BY l.created_at DESC,l.link_id DESC LIMIT 100",
    )?;
    let items = stmt
        .query_map(rusqlite::params![session_id, entry_id], |r| {
            Ok(json!({
                "link_id": r.get::<_, String>(0)?,
                "session_id": r.get::<_, String>(1)?,
                "entry_id": r.get::<_, String>(2)?,
                "state": r.get::<_, String>(3)?,
                "decision": r.get::<_, Option<String>>(4)?,
                "coverage": serde_json::from_str::<Value>(&r.get::<_, String>(5)?).unwrap_or(Value::Null),
                "quality": serde_json::from_str::<Value>(&r.get::<_, String>(6)?).unwrap_or(Value::Null),
                "created_at": r.get::<_, String>(7)?,
                "decided_at": r.get::<_, Option<String>>(8)?,
                "title": r.get::<_, String>(9)?,
                "namespace": r.get::<_, String>(10)?,
            }))
        })?
        .collect::<Result<Vec<_>, _>>()?;
    Ok(json!({"items": items}))
}

/// Archived entries of the same account that were published around the session.
pub fn candidates_for_session(c: &Connection, s: &SessionRef) -> Result<Value, CoreError> {
    let mut stmt = c.prepare(
        "SELECT e.entry_id,e.namespace,e.published_at,COALESCE(r.title,''),e.native_kind
         FROM entries e LEFT JOIN current_entry_revisions r ON r.entry_id=e.entry_id
         WHERE e.account_id=?1 AND e.lifecycle!='deleted' AND e.entry_id!=COALESCE(?2,'')
           AND e.published_at IS NOT NULL
           AND datetime(e.published_at) >= datetime(?3,'-6 hours') AND datetime(e.published_at) <= datetime(?4,'+30 hours')
         ORDER BY datetime(e.published_at) DESC,e.entry_id DESC LIMIT 50",
    )?;
    let end = s.ended_at.clone().unwrap_or_else(|| s.started_at.clone());
    let items = stmt
        .query_map(
            rusqlite::params![s.account_id, s.entry_id, s.started_at, end],
            |r| {
                Ok(json!({
                    "entry_id": r.get::<_, String>(0)?,
                    "namespace": r.get::<_, String>(1)?,
                    "published_at": r.get::<_, Option<String>>(2)?,
                    "title": r.get::<_, String>(3)?,
                    "kind": r.get::<_, String>(4)?,
                }))
            },
        )?
        .collect::<Result<Vec<_>, _>>()?;
    Ok(json!({"session_id": s.session_id, "items": items}))
}

/// Sealed sessions of the same account around an entry, for linking from the entry side.
pub fn candidates_for_entry(c: &Connection, e: &EntryRef) -> Result<Value, CoreError> {
    let mut stmt = c.prepare(
        "SELECT l.session_id,l.state,l.started_at,l.ended_at,l.room_key
         FROM live_sessions l JOIN subscriptions s ON s.subscription_id=l.subscription_id
         WHERE s.account_id=?1 AND l.state='sealed'
           AND (?2 IS NULL OR datetime(l.started_at) BETWEEN datetime(?2,'-30 hours') AND datetime(?2,'+6 hours'))
         ORDER BY l.started_at DESC LIMIT 50",
    )?;
    let items = stmt
        .query_map(rusqlite::params![e.account_id, e.published_at], |r| {
            Ok(json!({
                "session_id": r.get::<_, String>(0)?,
                "state": r.get::<_, String>(1)?,
                "started_at": r.get::<_, String>(2)?,
                "ended_at": r.get::<_, Option<String>>(3)?,
                "room_key": r.get::<_, String>(4)?,
            }))
        })?
        .collect::<Result<Vec<_>, _>>()?;
    Ok(json!({"entry_id": e.entry_id, "items": items}))
}
