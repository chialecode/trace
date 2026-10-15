//! Read side: pending candidates and the per-resource comparison.

use super::*;

pub async fn list(core: &Core, args: Value) -> Result<Value, CoreError> {
    core.db.write(invalidate_stale).await?;
    let state = args["state"].as_str().map(str::to_owned);
    let namespace = args["namespace"].as_str().map(str::to_owned);
    let creator = args["creator_id"].as_str().map(str::to_owned);
    let before = args["before"].as_i64().unwrap_or(i64::MAX);
    core.db.read(move|c|{
        let filter="(?1 IS NULL OR u.state=?1) AND (?2 IS NULL OR e.namespace=?2) AND (?3 IS NULL OR EXISTS(SELECT 1 FROM account_bindings b WHERE b.account_id=e.account_id AND b.creator_id=?3 AND b.state='active'))";
        let mut items=c.prepare(&format!("SELECT u.rowid,u.candidate_id,u.entry_id,u.revision_id,u.state,u.reason,u.created_at,u.finished_at,u.confirmation,x.auth_state,substr(r.title,1,512),e.namespace,x.started_at FROM upgrade_candidates u JOIN entries e ON e.entry_id=u.entry_id JOIN entry_revisions r ON r.revision_id=u.revision_id JOIN acquisition_contexts x ON x.context_id=u.context_id WHERE {filter} AND u.rowid<?4 ORDER BY u.rowid DESC LIMIT 11"))?.query_map(rusqlite::params![state,namespace,creator,before],|r|Ok(json!({
            "sequence": r.get::<_, i64>(0)?,
            "candidate_id": r.get::<_, String>(1)?,
            "entry_id": r.get::<_, String>(2)?,
            "revision_id": r.get::<_, String>(3)?,
            "state": r.get::<_, String>(4)?,
            "reason": r.get::<_, String>(5)?,
            "created_at": r.get::<_, String>(6)?,
            "finished_at": r.get::<_, Option<String>>(7)?,
            "expected": r.get::<_, Option<String>>(8)?.and_then(|v|serde_json::from_str::<Value>(&v).ok()),
            "auth_state": r.get::<_, String>(9)?,
            "title": r.get::<_, String>(10)?,
            "namespace": r.get::<_, String>(11)?,
            "acquired_at": r.get::<_, String>(12)?,
        })))?.collect::<Result<Vec<_>,_>>()?;
        let more=items.len()>10;items.truncate(10);let next=more.then(||items.last().unwrap()["sequence"].clone());
        for item in &mut items {
            let id=item["candidate_id"].as_str().unwrap().to_owned();
            let page=resources_on(c,&id,0)?;
            item["resources"]=page["items"].clone();item["resources_next"]=page["next_after"].clone();item["resource_count"]=page["total"].clone();
            item["complete"]=json!(complete(c,&id)?);
        }
        let total:i64=c.query_row(&format!("SELECT count(*) FROM upgrade_candidates u JOIN entries e ON e.entry_id=u.entry_id WHERE {filter}"),rusqlite::params![state,namespace,creator],|r|r.get(0))?;
        Ok(json!({"items":items,"next_before":next,"total":total}))
    }).await
}

const SELECT_RESOURCE_ACQUISITIONS: &str = "SELECT m.media_id,m.role,u.required,old.variant_id,ob.size,old.measured_spec,old.created_at,new.variant_id,nb.size,new.measured_spec,new.created_at,COALESCE((SELECT x.auth_state FROM resource_acquisitions a JOIN acquisition_contexts x ON x.context_id=a.context_id WHERE a.variant_id=old.variant_id ORDER BY a.sequence DESC LIMIT 1),'unknown'),EXISTS(SELECT 1 FROM variant_retention k WHERE k.variant_id=old.variant_id AND k.pinned=1),m.rowid FROM upgrade_members u JOIN media_items m ON m.media_id=u.media_id LEFT JOIN media_variants old ON old.variant_id=u.old_variant_id LEFT JOIN blobs ob ON ob.blob_id=old.blob_id LEFT JOIN media_variants new ON new.variant_id=u.new_variant_id LEFT JOIN blobs nb ON nb.blob_id=new.blob_id WHERE candidate_id=?1 AND m.rowid>?2 ORDER BY m.rowid LIMIT 21";

const SELECT_UPGRADE_CANDIDATES_4: &str = "SELECT COALESCE(json_extract(u.confirmation,'$.prefer_fps'),CASE WHEN s.config_version=u.subscription_version THEN COALESCE(json_extract(s.quality_json,'$.prefer_fps'),0) WHEN u.subscription_id IS NULL THEN 0 END) FROM upgrade_candidates u LEFT JOIN subscriptions s ON s.subscription_id=u.subscription_id WHERE u.candidate_id=?1";
const SELECT_UPGRADE_MEMBERS_8: &str = "SELECT count(*) FROM upgrade_members WHERE candidate_id=?1";

fn resources_on(c: &Connection, id: &str, after: i64) -> Result<Value, CoreError> {
    let mut resources=c.prepare(SELECT_RESOURCE_ACQUISITIONS)?.query_map(rusqlite::params![id,after],|r|Ok(json!({
        "sequence": r.get::<_, i64>(13)?,
        "media_id": r.get::<_, String>(0)?,
        "role": r.get::<_, String>(1)?,
        "required": r.get::<_, bool>(2)?,
        "old": {
            "variant_id": r.get::<_, Option<String>>(3)?,
            "bytes": r.get::<_, Option<i64>>(4)?,
            "spec": r.get::<_, Option<String>>(5)?.and_then(|v|serde_json::from_str::<Value>(&v).ok()),
            "saved_at": r.get::<_, Option<String>>(6)?,
            "auth_state": r.get::<_, String>(11)?,
            "pinned": r.get::<_, bool>(12)?,
        },
        "new": {
            "variant_id": r.get::<_, Option<String>>(7)?,
            "bytes": r.get::<_, Option<i64>>(8)?,
            "spec": r.get::<_, Option<String>>(9)?.and_then(|v|serde_json::from_str::<Value>(&v).ok()),
            "saved_at": r.get::<_, Option<String>>(10)?,
        },
    })))?.collect::<Result<Vec<_>,_>>()?;
    let more = resources.len() > 20;
    resources.truncate(20);
    // The current shared job may already execute another consumer. Historical
    // tokens without a frozen preference can use only an unchanged origin.
    let prefer: Option<bool> = c.query_row(SELECT_UPGRADE_CANDIDATES_4, [id], |r| r.get(0))?;
    for resource in &mut resources {
        resource["comparison"] = json!(if resource["old"]["variant_id"].is_null() {
            "new_resource"
        } else if let Some(prefer) = prefer {
            crate::quality::comparison(&resource["old"]["spec"], &resource["new"]["spec"], prefer)
        } else {
            "unknown"
        });
    }
    let next = more.then(|| resources.last().unwrap()["sequence"].clone());
    let total: i64 = c.query_row(SELECT_UPGRADE_MEMBERS_8, [id], |r| r.get(0))?;
    Ok(json!({"items":resources,"next_after":next,"total":total}))
}
pub async fn resources(core: &Core, args: Value) -> Result<Value, CoreError> {
    let id = args["candidate_id"]
        .as_str()
        .ok_or_else(|| CoreError::InvalidInput("candidate identity".into()))?
        .to_owned();
    let after = args["after"].as_i64().unwrap_or(0);
    core.db.read(move |c| resources_on(c, &id, after)).await
}
