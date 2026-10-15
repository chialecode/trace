//! Explicit, previewed deletion scopes. Identity tombstones survive scans and
//! backups; bytes use the existing shared-reference/lease-aware recycling journal.
use crate::{core::Core, error::CoreError};
use rusqlite::{Connection, params};
use serde_json::{Value, json};

const SELECT_MEDIA_VARIANTS: &str = "SELECT v.variant_id,v.blob_id,COALESCE(k.pinned,0) OR COALESCE(s.pinned,0),b.size FROM media_variants v JOIN media_items m ON m.media_id=v.media_id JOIN entry_revisions r ON r.revision_id=m.revision_id JOIN blobs b ON b.blob_id=v.blob_id LEFT JOIN variant_retention k ON k.variant_id=v.variant_id LEFT JOIN asset_selections s ON s.variant_id=v.variant_id WHERE r.entry_id=?1 AND (?2!='cache' OR v.source_representation='playback-mux-v1') ORDER BY v.variant_id";

fn snapshot(c: &Connection, entry: &str, scope: &str) -> Result<Value, CoreError> {
    if !matches!(scope, "cache" | "copies" | "archive") {
        return Err(CoreError::InvalidInput("deletion scope".into()));
    }
    let (generation, lifecycle, suppressed): (i64, String, bool) = c.query_row(
        "SELECT deletion_generation,lifecycle,media_suppressed FROM entries WHERE entry_id=?1",
        [entry],
        |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)),
    )?;
    let variants = c
        .prepare(SELECT_MEDIA_VARIANTS)?
        .query_map(params![entry, scope], |r| {
            Ok(json!({
                "variant": r.get::<_, String>(0)?,
                "blob": r.get::<_, String>(1)?,
                "pinned": r.get::<_, bool>(2)?,
                "bytes": r.get::<_, i64>(3)?,
            }))
        })?
        .collect::<Result<Vec<_>, _>>()?;
    let revisions = c
        .prepare("SELECT revision_id FROM entry_revisions WHERE entry_id=?1 ORDER BY revision_id")?
        .query_map([entry], |r| r.get::<_, String>(0))?
        .collect::<Result<Vec<_>, _>>()?;
    Ok(
        json!({"entry":entry,"scope":scope,"generation":generation,"lifecycle":lifecycle,"suppressed":suppressed,"variants":variants,"revisions":revisions}),
    )
}
fn token(value: &Value) -> String {
    crate::config::short_hash(&value.to_string())
}
pub async fn preview(core: &Core, entry: String, scope: String) -> Result<Value, CoreError> {
    let wait = core.profile.manual_delete_wait_seconds.clamp(0, 30 * 86400);
    core.db
        .read(move |c| {
            let value = snapshot(c, &entry, &scope)?;
            let variants = value["variants"].as_array().unwrap();
            let pinned = variants.iter().filter(|v| v["pinned"] == true).count();
            Ok(json!({
                "entry_id": entry,
                "scope": scope,
                "token": token(&value),
                "generation": value["generation"],
                "variants": variants.len(),
                "pinned_preserved": pinned,
                "wait_seconds": wait,
                "reclaim_bytes": "depends_on_shared_references_and_active_readers",
                "revisions": value["revisions"].as_array().unwrap().len(),
            }))
        })
        .await
}
pub async fn apply(
    core: &Core,
    entry: String,
    scope: String,
    expected: String,
) -> Result<Value, CoreError> {
    let _gate = core.archive_access.write().await;
    let wait = core.profile.manual_delete_wait_seconds.clamp(0, 30 * 86400);
    let eligible = (time::OffsetDateTime::now_utc() + time::Duration::seconds(wait.max(0)))
        .format(&time::format_description::well_known::Rfc3339)
        .map_err(|_| CoreError::Internal("clock".into()))?;
    core.db.write(move|tx|{
        let value=snapshot(tx,&entry,&scope)?;if token(&value)!=expected{return Err(CoreError::VersionConflict("deletion preview is stale".into()));}
        let generation=value["generation"].as_i64().unwrap()+1;
        tx.execute("UPDATE entries SET deletion_generation=?2,cache_suppressed=CASE WHEN ?3='cache' THEN 1 ELSE cache_suppressed END,lifecycle=CASE WHEN ?3='archive' THEN 'deleted' ELSE lifecycle END,media_suppressed=CASE WHEN ?3 IN ('archive','copies') THEN 1 ELSE media_suppressed END WHERE entry_id=?1",params![entry,generation,scope])?;
        tx.execute("INSERT INTO deletion_records(record_id,scope,object_id,generation,reason,created_at) VALUES(?1,?2,?3,?4,'user_requested',?5)",params![format!("delete-{}",uuid::Uuid::new_v4()),scope,entry,generation,crate::jobs::now()])?;
        // Cache removes derived playback only. Archive/copies stop acquisitions
        // before releasing references; late receive/recovery cannot resurrect.
        if scope!="cache" {
            tx.execute("UPDATE jobs SET state='cancelled',target_generation=target_generation+1,lease_owner=NULL,lease_expires_at=NULL,updated_at=?2 WHERE (subject_id=?1 OR json_extract(payload,'$.entry_id')=?1 OR subject_id IN (SELECT session_id FROM live_sessions WHERE entry_id=?1)) AND (?3='archive' OR job_type IN ('media','record','live_auxiliary'))",params![entry,crate::jobs::now(),scope])?;
            tx.execute("UPDATE live_sessions SET state='sealed',manual_stopped=1,ended_at=COALESCE(ended_at,?2),stop_reason='user_deleted' WHERE entry_id=?1",params![entry,crate::jobs::now()])?;
            tx.execute("UPDATE live_fragments SET state='interrupted',reason='user_deleted' WHERE session_id IN (SELECT session_id FROM live_sessions WHERE entry_id=?1) AND state='receiving'",[&entry])?;
            tx.execute("UPDATE materializations SET stage='QUARANTINED' WHERE subject_id IN (SELECT m.media_id FROM media_items m JOIN entry_revisions r ON r.revision_id=m.revision_id WHERE r.entry_id=?1) AND stage!='COMMITTED'",[&entry])?;
        }
        tx.execute("INSERT INTO deletion_selections SELECT ?1,?2,s.media_id,s.variant_id,s.pinned,s.policy_version,s.reason FROM asset_selections s JOIN media_items m ON m.media_id=s.media_id JOIN entry_revisions r ON r.revision_id=m.revision_id WHERE r.entry_id=?1",params![entry,generation])?;
        for variant in value["variants"].as_array().unwrap().iter().filter(|v|v["pinned"]!=true){
            let id=variant["variant"].as_str().unwrap();
            tx.execute("DELETE FROM asset_selections WHERE variant_id=?1",[id])?;
            tx.execute("INSERT OR REPLACE INTO deleted_variants(variant_id,generation) VALUES(?1,?2)",params![id,generation])?;
            tx.execute("INSERT INTO variant_retention(variant_id,retained,pinned,reason,eligible_at,updated_at) VALUES(?1,0,0,'user_deleted',?2,?3) ON CONFLICT(variant_id) DO UPDATE SET retained=0,reason='user_deleted',eligible_at=excluded.eligible_at,updated_at=excluded.updated_at",params![id,eligible,crate::jobs::now()])?;
        }
        if scope=="archive"{tx.execute("DELETE FROM search_documents WHERE entry_id=?1",[&entry])?;}
        crate::objects::outbox_emit(tx,"entry.deletion_changed",&entry,&json!({"scope":scope,"generation":generation}))?;
        crate::quality::schedule(tx,true)?;
        Ok(json!({"generation":generation,"eligible_at":eligible,"state":"waiting_recycle","scope":scope}))
    }).await
}
pub async fn restore(core: &Core, entry: String, generation: i64) -> Result<Value, CoreError> {
    let _gate = core.archive_access.write().await;
    core.db.write(move|tx|{
        if tx.execute("UPDATE entries SET lifecycle='active',media_suppressed=0,cache_suppressed=0,deletion_generation=deletion_generation+1 WHERE entry_id=?1 AND deletion_generation=?2",params![entry,generation])?!=1{return Err(CoreError::VersionConflict("deletion generation changed".into()));}
        tx.execute("UPDATE variant_retention SET retained=1,eligible_at=NULL,reason='explicitly_restored' WHERE variant_id IN (SELECT d.variant_id FROM deleted_variants d JOIN media_variants v ON v.variant_id=d.variant_id JOIN media_items m ON m.media_id=v.media_id JOIN entry_revisions r ON r.revision_id=m.revision_id WHERE r.entry_id=?1 AND v.source_representation!='playback-mux-v1')",[&entry])?;
        tx.execute("DELETE FROM deleted_variants WHERE variant_id IN (SELECT v.variant_id FROM media_variants v JOIN media_items m ON m.media_id=v.media_id JOIN entry_revisions r ON r.revision_id=m.revision_id WHERE r.entry_id=?1)",[&entry])?;
        // Restore the prior approved choice if its bytes remain verified.
        // A tombstone reversal is never a decision to accept an upgrade.
        tx.execute("INSERT OR IGNORE INTO asset_selections(media_id,variant_id,pinned,policy_version,reason,updated_at) SELECT d.media_id,d.variant_id,d.pinned,d.policy_version,d.reason,?3 FROM deletion_selections d JOIN media_variants v ON v.variant_id=d.variant_id WHERE d.entry_id=?1 AND d.generation=?2 AND v.approval_state IN ('automatic','accepted','kept') AND v.source_representation!='playback-mux-v1' AND EXISTS(SELECT 1 FROM replicas p WHERE p.blob_id=v.blob_id AND p.state='verified')",params![entry,generation,crate::jobs::now()])?;
        tx.execute("INSERT OR IGNORE INTO asset_selections(media_id,variant_id,policy_version,reason,updated_at) SELECT m.media_id,(SELECT v.variant_id FROM media_variants v WHERE v.media_id=m.media_id AND v.approval_state IN ('automatic','accepted','kept') AND v.source_representation!='playback-mux-v1' AND EXISTS(SELECT 1 FROM replicas p WHERE p.blob_id=v.blob_id AND p.state='verified') ORDER BY v.created_at DESC,v.variant_id LIMIT 1),'m1-restore','explicitly_restored',?2 FROM media_items m JOIN entry_revisions r ON r.revision_id=m.revision_id WHERE r.entry_id=?1 AND EXISTS(SELECT 1 FROM media_variants v WHERE v.media_id=m.media_id AND v.approval_state IN ('automatic','accepted','kept') AND v.source_representation!='playback-mux-v1' AND EXISTS(SELECT 1 FROM replicas p WHERE p.blob_id=v.blob_id AND p.state='verified'))",params![entry,crate::jobs::now()])?;
        tx.execute("INSERT INTO deletion_records(record_id,scope,object_id,generation,reason,created_at) VALUES(?1,'restore',?2,?3,'explicit_reinclude',?4)",params![format!("restore-{}",uuid::Uuid::new_v4()),entry,generation+1,crate::jobs::now()])?;
        crate::objects::outbox_emit(tx,"entry.restored",&entry,&json!({"generation":generation+1}))?;
        crate::quality::schedule(tx,true)?;
        Ok(json!({"generation":generation+1,"state":"restored_metadata","media":"verify_or_recheck_required"}))
    }).await
}
const SELECT_CURRENT_ENTRY_REVISIONS: &str = "SELECT e.entry_id,e.lifecycle,e.deletion_generation,e.media_suppressed,(SELECT r.title FROM current_entry_revisions r WHERE r.entry_id=e.entry_id) FROM entries e WHERE (e.lifecycle='deleted' OR e.media_suppressed=1 OR e.cache_suppressed=1) AND e.entry_id>?1 ORDER BY e.entry_id LIMIT 101";

pub async fn listing(core: &Core, after: String) -> Result<Value, CoreError> {
    core.db
        .read(move |c| {
            let mut items = c
                .prepare(SELECT_CURRENT_ENTRY_REVISIONS)?
                .query_map([after], |r| {
                    Ok(json!({
                        "entry_id": r.get::<_, String>(0)?,
                        "lifecycle": r.get::<_, String>(1)?,
                        "generation": r.get::<_, i64>(2)?,
                        "media_suppressed": r.get::<_, bool>(3)?,
                        "title": r.get::<_, Option<String>>(4)?,
                    }))
                })?
                .collect::<Result<Vec<_>, _>>()?;
            let more = items.len() > 100;
            items.truncate(100);
            let next = if more {
                items.last().map(|v| v["entry_id"].clone())
            } else {
                None
            };
            Ok(json!({"items":items,"next":next}))
        })
        .await
}
