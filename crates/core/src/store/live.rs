//! Live sessions and their fragments as the control commands read and change them.
use crate::error::CoreError;
use rusqlite::Connection;
use serde_json::{Value, json};

const FRAGMENTS_SQL: &str = "SELECT f.sequence,f.state,f.start_ms,f.end_ms,f.reason,v.blob_id,b.mime,EXISTS(SELECT 1 FROM replicas rp WHERE rp.blob_id=v.blob_id AND rp.state='verified'),COALESCE(m.acquisition_evidence,'{}'),f.captured_at,v.created_at FROM live_fragments f LEFT JOIN media_items m ON m.media_id=f.media_id LEFT JOIN asset_selections s ON s.media_id=m.media_id LEFT JOIN media_variants v ON v.variant_id=s.variant_id LEFT JOIN blobs b ON b.blob_id=v.blob_id WHERE f.session_id=?1 AND f.sequence<?2 AND COALESCE(m.role,'')!='recording_source' ORDER BY f.sequence DESC LIMIT 101";
const FRAGMENT_PAGE: usize = 100;

fn fragment_item(r: &rusqlite::Row<'_>) -> rusqlite::Result<Value> {
    Ok(json!({
        "sequence": r.get::<_, i64>(0)?,
        "state": r.get::<_, String>(1)?,
        "start_ms": r.get::<_, Option<i64>>(2)?,
        "end_ms": r.get::<_, Option<i64>>(3)?,
        "reason": r.get::<_, Option<String>>(4)?,
        "blob_id": r.get::<_, Option<String>>(5)?,
        "mime": r.get::<_, Option<String>>(6)?,
        "available": r.get::<_, bool>(7)?,
        "quality": r.get::<_, String>(8)?.parse::<Value>().unwrap_or_default(),
        "captured_at": r.get::<_, Option<String>>(9)?,
        "saved_at": r.get::<_, Option<String>>(10)?,
    }))
}

/// Fragments older than `before`, newest first, with the authentication label each was captured under.
pub fn fragments_page(c: &Connection, session_id: &str, before: i64) -> Result<Value, CoreError> {
    let mut items = c
        .prepare(FRAGMENTS_SQL)?
        .query_map(rusqlite::params![session_id, before], fragment_item)?
        .collect::<Result<Vec<_>, _>>()?;
    for item in &mut items {
        let labels =
            crate::acquisitions::fragment(c, session_id, item["sequence"].as_i64().unwrap_or(0))?;
        item["authentication"] = labels["authentication"].clone();
        item["actual_spec"] = labels["actual_spec"].clone();
    }
    let more = items.len() > FRAGMENT_PAGE;
    items.truncate(FRAGMENT_PAGE);
    let next = more.then(|| items.last().unwrap()["sequence"].clone());
    let total: i64 = c.query_row(
        "SELECT count(*) FROM live_fragments f LEFT JOIN media_items m ON m.media_id=f.media_id WHERE f.session_id=?1 AND COALESCE(m.role,'')!='recording_source'",
        [session_id],
        |r| r.get(0),
    )?;
    Ok(json!({"items": items, "next_before": next, "total": total}))
}

/// Starts or stops recording; returns false when the session already was in that state.
pub fn set_recording(
    c: &Connection,
    session_id: &str,
    enabled: bool,
    expected_generation: Option<i64>,
    now: &str,
) -> Result<bool, CoreError> {
    let (generation, stopped): (i64, i64) = c.query_row(
        "SELECT generation,manual_stopped FROM live_sessions WHERE session_id=?1",
        [session_id],
        |r| Ok((r.get(0)?, r.get(1)?)),
    )?;
    if expected_generation.is_some_and(|e| e != generation) {
        return Err(CoreError::VersionConflict("live session".into()));
    }
    if stopped == i64::from(!enabled) {
        return Ok(false);
    }
    c.execute(
        "UPDATE live_sessions SET manual_stopped=?2,state=?3,generation=generation+1,stop_reason=?4 WHERE session_id=?1",
        rusqlite::params![
            session_id,
            i64::from(!enabled),
            if enabled { "waiting" } else { "stopping" },
            if enabled { "manual_start" } else { "manual_stop" }
        ],
    )?;
    if enabled {
        c.execute(
            "UPDATE jobs SET state='queued',target_generation=target_generation+1,lease_owner=NULL,lease_expires_at=NULL,next_run_at=?2 WHERE job_key=?1",
            rusqlite::params![format!("record:{session_id}"), now],
        )?;
    }
    Ok(true)
}
