//! Rebuildable data has separate accounting and controls. Source attachments,
//! including downloaded covers, remain archive originals rather than caches.
use crate::{core::Core, db::DbHandle, error::CoreError};
use rusqlite::{Connection, params};
use serde_json::{Value, json};

pub fn search_ready(c: &Connection) -> Result<(), CoreError> {
    let ready: bool = c.query_row(
        "SELECT enabled=1 AND budget_blocked=0 AND rebuilding=0 FROM search_state WHERE id=1",
        [],
        |r| r.get(0),
    )?;
    if ready {
        Ok(())
    } else {
        Err(CoreError::Paused(
            "search cache disabled, rebuilding or budget exhausted; see storage".into(),
        ))
    }
}
pub async fn status(db: &DbHandle) -> Result<Value, CoreError> {
    db.read(|c|{
        let (bytes,budget,enabled,blocked,rebuilding):(i64,i64,bool,bool,bool)=c.query_row("SELECT estimated_bytes,budget_bytes,enabled,budget_blocked,rebuilding FROM search_state WHERE id=1",[],|r|Ok((r.get(0)?,r.get(1)?,r.get(2)?,r.get(3)?,r.get(4)?)))?;
        let (derived_budget,derived_enabled):(i64,bool)=c.query_row("SELECT budget_bytes,enabled FROM derived_cache_policy WHERE id=1",[],|r|Ok((r.get(0)?,r.get(1)?)))?;
        let derived:i64=c.query_row("SELECT COALESCE(sum(b.size),0) FROM blobs b WHERE EXISTS(SELECT 1 FROM media_variants v WHERE v.blob_id=b.blob_id AND v.source_representation='playback-mux-v1') AND EXISTS(SELECT 1 FROM replicas p WHERE p.blob_id=b.blob_id AND p.managed=1 AND p.state='verified')",[],|r|r.get(0))?;
        Ok(json!({"index_estimated_bytes":bytes,"index_budget_bytes":budget,"index_enabled":enabled,"index_budget_blocked":blocked,"index_rebuilding":rebuilding,"derived_bytes":derived,"derived_budget_bytes":derived_budget,"derived_enabled":derived_enabled,"thumbnail_bytes":0,"thumbnail_budget_bytes":0,"thumbnail_state":"no_generated_thumbnails"}))
    }).await
}
pub async fn configure(core: &Core, index: i64, derived: i64) -> Result<Value, CoreError> {
    if !(1024 * 1024..=1024_i64.pow(4)).contains(&index)
        || !(0..=1024_i64.pow(4)).contains(&derived)
    {
        return Err(CoreError::InvalidInput("cache budget".into()));
    }
    let _cache = core.cache_build.lock().await;
    core.db
        .write(move |tx| {
            tx.execute(
                "UPDATE search_state SET budget_bytes=?1,budget_blocked=(estimated_bytes>?1) WHERE id=1",
                [index],
            )?;
            tx.execute(
                "UPDATE derived_cache_policy SET budget_bytes=?1 WHERE id=1",
                [derived],
            )?;
            Ok(())
        })
        .await?;
    status(&core.db).await
}
pub async fn control(core: &Core, scope: String, rebuild: bool) -> Result<Value, CoreError> {
    let _cache = core.cache_build.lock().await;
    let _gate = core.archive_access.write().await;
    core.db.write(move|tx|{
        match scope.as_str(){
            "search"=>{
                tx.execute("DELETE FROM search_documents",[])?;
                tx.execute("INSERT INTO search_fts(search_fts) VALUES('rebuild')",[])?;
                tx.execute("UPDATE search_state SET generation=generation+1,enabled=?1,rebuilding=?1,indexed_seq=0,budget_blocked=0,estimated_bytes=0 WHERE id=1",[rebuild])?;
                // A fresh event per entry also covers migrated or compacted outboxes.
                tx.execute("UPDATE outbox SET processed_at=?1 WHERE processed_at IS NULL AND topic LIKE 'entry%'",[crate::jobs::now()])?;
                tx.execute("INSERT INTO outbox(topic,aggregate_id,payload,created_at) SELECT 'entry.search_rebuild',entry_id,'{}',?1 FROM entries WHERE lifecycle!='deleted'",[crate::jobs::now()])?;
            },
            "playback"=>{
                tx.execute("UPDATE derived_cache_policy SET enabled=?1,generation=generation+1 WHERE id=1",[rebuild])?;
                if !rebuild {
                    tx.execute("INSERT INTO variant_retention(variant_id,retained,pinned,reason,eligible_at,updated_at) SELECT v.variant_id,0,0,'cache_cleared',?1,?1 FROM media_variants v WHERE v.source_representation='playback-mux-v1' AND NOT EXISTS(SELECT 1 FROM variant_retention k WHERE k.variant_id=v.variant_id AND k.pinned=1) AND NOT EXISTS(SELECT 1 FROM asset_selections s WHERE s.variant_id=v.variant_id AND s.pinned=1) ON CONFLICT(variant_id) DO UPDATE SET retained=0,reason='cache_cleared',eligible_at=excluded.eligible_at,updated_at=excluded.updated_at",[crate::jobs::now()])?;
                    tx.execute("DELETE FROM asset_selections WHERE variant_id IN (SELECT v.variant_id FROM media_variants v JOIN variant_retention k ON k.variant_id=v.variant_id WHERE v.source_representation='playback-mux-v1' AND k.retained=0 AND k.pinned=0) AND pinned=0",[])?;
                }
                crate::quality::schedule(tx,true)?;
            },
            "thumbnails"=>{}, // None generated; originals are never removed as thumbnails.
            _=>return Err(CoreError::InvalidInput("cache scope".into())),
        }
        crate::objects::outbox_emit(tx,"cache.changed","archive",&json!({"scope":scope,"rebuild":rebuild}))?;Ok(())
    }).await?;
    status(&core.db).await
}
pub fn derived_allowed(c: &Connection, additional: i64) -> Result<bool, CoreError> {
    Ok(c.query_row("SELECT enabled=1 AND ?1<=budget_bytes-COALESCE((SELECT sum(b.size) FROM blobs b WHERE EXISTS(SELECT 1 FROM media_variants v WHERE v.blob_id=b.blob_id AND v.source_representation='playback-mux-v1') AND EXISTS(SELECT 1 FROM replicas p WHERE p.blob_id=b.blob_id AND p.managed=1 AND p.state='verified')),0) FROM derived_cache_policy WHERE id=1",params![additional],|r|r.get(0))?)
}
