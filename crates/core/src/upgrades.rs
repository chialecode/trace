//! Login completion has its own bounded durable lane. Verified candidates stay
//! separate from selections until a user confirms the exact comparison snapshot.
use crate::{
    core::Core,
    error::CoreError,
    jobs::{self, ClaimedJob},
};
use rusqlite::{Connection, OptionalExtension, Transaction};
use serde_json::{Value, json};
use trace_contracts::acquisition::{UpgradeAction, UpgradeDecision};

/// Rank per attachment before expanding ordered segments. Required audio and
/// other attachments are independent groups and cannot disappear in this step.
pub fn highest_candidates(
    candidates: &mut Vec<trace_contracts::plugin::MediaCandidate>,
    prefer_fps: bool,
) {
    let mut best = std::collections::BTreeMap::new();
    for (i, c) in candidates.iter().enumerate() {
        let rank = (
            trace_contracts::content::source_rank(&c.claimed_spec, prefer_fps),
            c.claimed_spec["bitrate"]
                .as_i64()
                .or_else(|| c.claimed_spec["bit_rate"].as_i64())
                .unwrap_or(0),
        );
        let key = (
            c.resource.source_attachment_id.clone(),
            c.resource.role.clone(),
        );
        if best.get(&key).is_none_or(|(r, _)| rank > *r) {
            best.insert(key, (rank, i));
        }
    }
    let selected = best
        .into_values()
        .map(|(_, i)| i)
        .collect::<std::collections::BTreeSet<_>>();
    let mut i = 0;
    candidates.retain(|_| {
        let keep = selected.contains(&i);
        i += 1;
        keep
    });
}

pub fn schedule(tx: &Transaction<'_>, credential: &str, generation: i64) -> Result<(), CoreError> {
    let subscriptions=tx.prepare("SELECT subscription_id,config_version FROM subscriptions WHERE credential_id=?1 AND content_type!='live' AND acquisition_kind='subscription'")?.query_map([credential],|r|Ok((r.get::<_,String>(0)?,r.get::<_,i64>(1)?)))?.collect::<Result<Vec<_>,_>>()?;
    for (sub, version) in subscriptions {
        let bound: i64 = tx.query_row(
            "SELECT COALESCE(MAX(rowid),0) FROM entry_subscriptions WHERE subscription_id=?1",
            [&sub],
            |r| r.get(0),
        )?;
        if bound == 0 {
            continue;
        }
        let key = format!("auth-completion:{credential}:{generation}:{sub}:{version}");
        tx.execute("INSERT OR IGNORE INTO jobs(job_id,job_key,job_type,subject_id,payload,priority,state,created_at,updated_at) VALUES(?1,?2,'auth_completion',?3,?4,3,'queued',?5,?5)",rusqlite::params![format!("job-{}",uuid::Uuid::new_v4()),key,credential,json!({"subscription_id":sub,"credential_id":credential,"credential_generation":generation,"upper_bound":bound,"after":0}).to_string(),jobs::now()])?;
    }
    Ok(())
}

pub async fn run(core: &Core, job: &ClaimedJob) -> Result<(), CoreError> {
    let guard = job.clone();
    core.db.write(move|tx|{
        guard.check(tx)?;
        let sub=guard.payload["subscription_id"].as_str();
        let after=guard.payload["after"].as_i64().unwrap_or(0);
        let bound=guard.payload["upper_bound"].as_i64().unwrap_or(0);
        let rows=tx.prepare("SELECT d.rowid,e.entry_id,r.revision_id FROM entry_subscriptions d JOIN entries e ON e.entry_id=d.entry_id JOIN current_entry_revisions r ON r.entry_id=e.entry_id WHERE d.subscription_id=?1 AND d.rowid>?2 AND d.rowid<=?3 AND e.native_kind!='live' AND e.lifecycle!='deleted' AND e.media_suppressed=0 AND EXISTS(SELECT 1 FROM media_items m WHERE m.revision_id=r.revision_id) AND (NOT EXISTS(SELECT 1 FROM acquisition_contexts x WHERE x.entry_id=e.entry_id) OR EXISTS(SELECT 1 FROM acquisition_contexts x WHERE x.entry_id=e.entry_id AND x.auth_state IN ('anonymous','expired','unknown'))) ORDER BY d.rowid LIMIT 16")?.query_map(rusqlite::params![sub,after,bound],|r|Ok((r.get::<_,i64>(0)?,r.get::<_,String>(1)?,r.get::<_,String>(2)?)))?.collect::<Result<Vec<_>,_>>()?;
        for (_,entry,revision) in &rows {
            let key=format!("completion-observe:{}:{entry}",guard.job_id);
            tx.execute("INSERT OR IGNORE INTO jobs(job_id,job_key,job_type,subject_id,payload,priority,state,created_at,updated_at) VALUES(?1,?2,'observe',?3,?4,3,'queued',?5,?5)",rusqlite::params![format!("job-{}",uuid::Uuid::new_v4()),key,entry,json!({"entry_id":entry,"subscription_id":sub,"completion":true,"completion_parent":guard.job_id,"expected_revision_id":revision,"credential_id":guard.payload["credential_id"],"credential_generation":guard.payload["credential_generation"]}).to_string(),jobs::now()])?;
        }
        if rows.len()==16 {
            let next=(time::OffsetDateTime::now_utc()+time::Duration::seconds(2)).format(&time::format_description::well_known::Rfc3339).map_err(|_|CoreError::Internal("clock".into()))?;
            tx.execute("UPDATE jobs SET state='queued',payload=json_set(payload,'$.after',?2),next_run_at=?3,lease_owner=NULL,lease_expires_at=NULL WHERE job_id=?1",rusqlite::params![guard.job_id,rows.last().unwrap().0,next])?;
        }
        Ok(())
    }).await
}

pub fn schedule_media(
    tx: &Transaction<'_>,
    job: &ClaimedJob,
    payload: Value,
    entry: &str,
    revision: &str,
    group: &str,
) -> Result<(), CoreError> {
    let mut payload = payload;
    for key in [
        "completion",
        "completion_parent",
        "expected_revision_id",
        "credential_id",
        "credential_generation",
    ] {
        payload[key] = job.payload[key].clone();
    }
    let key = format!("completion-media:{}:{revision}:{group}", job.job_id);
    tx.execute("INSERT OR IGNORE INTO jobs(job_id,job_key,job_type,subject_id,payload,priority,state,created_at,updated_at) VALUES(?1,?2,'media',?3,?4,3,'queued',?5,?5)",rusqlite::params![format!("job-{}",uuid::Uuid::new_v4()),key,entry,payload.to_string(),jobs::now()])?;
    Ok(())
}

fn policy(c: &Connection) -> Result<Option<String>, CoreError> {
    Ok(c.query_row(
        "SELECT value FROM meta WHERE key='quality_policy'",
        [],
        |r| r.get(0),
    )
    .optional()?)
}
fn members(c: &Connection, id: &str) -> Result<Value, CoreError> {
    Ok(json!(c.prepare("SELECT m.media_id,s.variant_id,COALESCE(s.pinned,0),m.resource_generation FROM upgrade_members u JOIN media_items m ON m.media_id=u.media_id LEFT JOIN asset_selections s ON s.media_id=m.media_id WHERE u.candidate_id=?1 ORDER BY m.media_id")?.query_map([id],|r|Ok(json!({"media_id":r.get::<_,String>(0)?,"variant_id":r.get::<_,Option<String>>(1)?,"pinned":r.get::<_,bool>(2)?,"generation":r.get::<_,i64>(3)?})))?.collect::<Result<Vec<_>,_>>()?))
}
fn snapshot(c: &Connection, id: &str) -> Result<Value, CoreError> {
    let mut v=c.query_row("SELECT u.revision_id,e.deletion_generation,s.config_version,k.generation,u.credential_id,(SELECT revision_id FROM current_entry_revisions WHERE entry_id=e.entry_id),e.lifecycle,e.media_suppressed,s.state,COALESCE(k.status,'anonymous') FROM upgrade_candidates u JOIN entries e ON e.entry_id=u.entry_id LEFT JOIN subscriptions s ON s.subscription_id=u.subscription_id LEFT JOIN credentials_state k ON k.credential_id=u.credential_id WHERE u.candidate_id=?1",[id],|r|Ok(json!({"revision_id":r.get::<_,String>(0)?,"deletion_generation":r.get::<_,i64>(1)?,"subscription_version":r.get::<_,Option<i64>>(2)?,"credential_generation":r.get::<_,Option<i64>>(3)?,"credential_id":r.get::<_,Option<String>>(4)?,"current_revision_id":r.get::<_,Option<String>>(5)?,"lifecycle":r.get::<_,String>(6)?,"media_suppressed":r.get::<_,bool>(7)?,"subscription_state":r.get::<_,Option<String>>(8)?,"credential_status":r.get::<_,String>(9)?})))?;
    let selected = members(c, id)?;
    use sha2::Digest;
    v["selection_digest"] = json!(
        sha2::Sha256::digest(serde_json::to_vec(&selected)?)
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect::<String>()
    );
    v["resource_count"] = json!(selected.as_array().map_or(0, Vec::len));
    v["policy"] = json!(policy(c)?);
    // A shared lease belongs to the executing consumer. Once its result is
    // complete, later consumers may rotate that lease without revoking it.
    // The originating request and its subscription/credential still must match.
    let (generation, request): (i64, Option<i64>) = c.query_row(
        "SELECT CASE WHEN u.consumer_request IS NULL THEN j.target_generation ELSE u.job_generation END,w.requested FROM upgrade_candidates u JOIN jobs j ON j.job_id=u.job_id LEFT JOIN job_consumers w ON w.job_id=u.job_id AND w.subscription_id=u.subscription_id AND u.consumer_request IS NOT NULL WHERE u.candidate_id=?1",
        [id], |r| Ok((r.get(0)?,r.get(1)?)))?;
    v["work_generation"] = json!(generation);
    // Keep schema-5 nonshared comparison tokens compatible. For a shared
    // candidate the field is present even if its consumer was removed, so
    // removing that consumer cannot accidentally match an older token.
    let shared: bool = c.query_row(
        "SELECT consumer_request IS NOT NULL FROM upgrade_candidates WHERE candidate_id=?1",
        [id],
        |r| r.get(0),
    )?;
    if shared {
        v["consumer_request"] = json!(request);
    }
    // New shared confirmations freeze the originating quality preference.
    // Older tokens keep their exact shape when this optional field is absent.
    let preference: Option<bool> = c.query_row(
        "SELECT json_extract(confirmation,'$.prefer_fps') FROM upgrade_candidates WHERE candidate_id=?1",
        [id],
        |r| r.get(0),
    )?;
    if let Some(prefer_fps) = preference {
        v["prefer_fps"] = json!(prefer_fps);
    }
    Ok(v)
}

pub async fn prepare(
    core: &Core,
    job: &ClaimedJob,
    revision: &str,
    group: &str,
) -> Result<Option<String>, CoreError> {
    let Some(context) = job.payload["acquisition_context_id"]
        .as_str()
        .map(str::to_owned)
    else {
        return Ok(None);
    };
    let (guard, revision, group) = (job.clone(), revision.to_owned(), group.to_owned());
    core.db.write(move|tx|{
        guard.check(tx)?;
        let (auth,credential,generation):(String,Option<String>,Option<i64>)=tx.query_row("SELECT auth_state,credential_id,credential_generation FROM acquisition_contexts WHERE context_id=?1",[&context],|r|Ok((r.get(0)?,r.get(1)?,r.get(2)?)))?;
        // A retry completing this same demand may reuse its own committed
        // resources. A previous demand or authentication scope needs a decision.
        let has_old:bool=tx.query_row("SELECT EXISTS(SELECT 1 FROM media_items m JOIN asset_selections s ON s.media_id=m.media_id WHERE m.revision_id=?1 AND (m.group_key=?2 OR (m.group_key IS NULL AND m.source_attachment_id=?2)) AND NOT EXISTS(SELECT 1 FROM resource_acquisitions a JOIN acquisition_contexts x ON x.context_id=a.context_id WHERE a.variant_id=s.variant_id AND x.job_id=?3 AND x.job_generation=?4 AND x.auth_state=?5 AND x.credential_id IS ?6 AND x.credential_generation IS ?7))",rusqlite::params![revision,group,guard.job_id,guard.generation,auth,credential,generation],|r|r.get(0))?;
        if guard.payload["completion"]!=true && !((auth=="authenticated" || guard.payload["source_request_id"].is_string()) && has_old) {return Ok(None)}
        let prior:Option<String>=tx.query_row("SELECT u.candidate_id FROM upgrade_candidates u JOIN entries e ON e.entry_id=u.entry_id WHERE u.job_id=?1 AND u.job_generation=?2 AND u.revision_id=?3 AND u.group_key=?4 AND u.state='collecting' AND u.credential_id IS ?5 AND u.credential_generation IS ?6 AND u.subscription_id IS ?7 AND u.consumer_request IS ?8 AND u.policy IS ?9 AND u.deletion_generation=e.deletion_generation AND NOT EXISTS(SELECT 1 FROM upgrade_members x LEFT JOIN asset_selections s ON s.media_id=x.media_id WHERE x.candidate_id=u.candidate_id AND (s.variant_id IS NOT x.old_variant_id OR COALESCE(s.pinned,0)!=x.old_pinned)) ORDER BY u.created_at DESC LIMIT 1",rusqlite::params![guard.job_id,guard.generation,revision,group,credential,generation,guard.payload["subscription_id"].as_str(),guard.payload["consumer_request"].as_i64(),policy(tx)?],|r|r.get(0)).optional()?;
        if let Some(id)=prior {return Ok(Some(id));}
        tx.execute("UPDATE upgrade_candidates SET state='stale',reason='retry_scope_changed',finished_at=?3 WHERE job_id=?1 AND job_generation=?2 AND state='collecting'",rusqlite::params![guard.job_id,guard.generation,jobs::now()])?;
        let id=format!("upgrade-{}",uuid::Uuid::new_v4());
        let entry:String=tx.query_row("SELECT entry_id FROM entry_revisions WHERE revision_id=?1",[&revision],|r|r.get(0))?;
        tx.execute("INSERT INTO upgrade_candidates(candidate_id,entry_id,revision_id,group_key,context_id,job_id,job_generation,subscription_id,subscription_version,credential_id,credential_generation,deletion_generation,policy,created_at) SELECT ?1,?2,?3,?4,?5,?6,?7,?8,?9,?10,?11,e.deletion_generation,?12,?13 FROM entries e WHERE e.entry_id=?2",rusqlite::params![id,entry,revision,group,context,guard.job_id,guard.generation,guard.payload["subscription_id"].as_str(),guard.payload["execution_scope"]["subscription_version"].as_i64(),credential,generation,policy(tx)?,jobs::now()])?;
        tx.execute("INSERT INTO upgrade_members(candidate_id,media_id,old_variant_id,required,old_pinned) SELECT ?1,m.media_id,s.variant_id,m.required,COALESCE(s.pinned,0) FROM media_items m LEFT JOIN asset_selections s ON s.media_id=m.media_id WHERE m.revision_id=?2 AND (m.group_key=?3 OR (m.group_key IS NULL AND m.source_attachment_id=?3))",rusqlite::params![id,revision,group])?;
        tx.execute("UPDATE upgrade_candidates SET consumer_request=?2 WHERE candidate_id=?1",rusqlite::params![id,guard.payload["consumer_request"].as_i64()])?;
        Ok(Some(id))
    }).await
}

pub fn variant(
    tx: &Transaction<'_>,
    candidate: &str,
    media: &str,
    variant: &str,
) -> Result<(), CoreError> {
    let n=tx.execute("UPDATE upgrade_members SET new_variant_id=?3 WHERE candidate_id=?1 AND media_id=?2 AND EXISTS(SELECT 1 FROM upgrade_candidates WHERE candidate_id=?1 AND state='collecting')",rusqlite::params![candidate,media,variant])?;
    if n != 1 {
        return Err(CoreError::Cancelled);
    }
    Ok(())
}

pub async fn reused(
    core: &Core,
    job: &ClaimedJob,
    media: &str,
    variant_id: &str,
    same_provenance: bool,
) -> Result<(), CoreError> {
    let (job, media, variant_id) = (job.clone(), media.to_owned(), variant_id.to_owned());
    core.db.write(move|tx|{
        job.check(tx)?;
        let (id,spec):(String,String)=tx.query_row("SELECT v.variant_id,COALESCE(v.measured_spec,'{}') FROM media_variants v WHERE v.media_id=?1 AND v.variant_id=?2 AND (v.approval_state IN ('automatic','accepted','kept') OR (v.approval_state='pending' AND EXISTS(SELECT 1 FROM resource_acquisitions a JOIN acquisition_contexts x ON x.context_id=a.context_id JOIN acquisition_contexts current ON current.context_id=?5 WHERE a.variant_id=v.variant_id AND x.job_id=?3 AND x.job_generation=?4 AND x.auth_state=current.auth_state AND x.credential_id IS current.credential_id AND x.credential_generation IS current.credential_generation))) AND NOT EXISTS(SELECT 1 FROM deleted_variants d WHERE d.variant_id=v.variant_id)",rusqlite::params![media,variant_id,job.job_id,job.generation,job.payload["acquisition_context_id"].as_str()],|r|Ok((r.get(0)?,r.get(1)?)))?;
        if let Some(original)=job.payload["acquisition_context_id"].as_str(){
            let context=if same_provenance { original.to_owned() } else {
                let unknown=trace_contracts::acquisition::AcquisitionContext { context_id:format!("ctx-{}",uuid::Uuid::new_v4()),auth_state:trace_contracts::acquisition::AuthenticationState::Unknown,credential_id:None,credential_generation:None,validated_at:None,basis:"legacy_cached_copy_verified".into(),started_at:jobs::now() };
                crate::acquisitions::insert_context(tx,&job,&job.subject_id,&unknown)?;
                unknown.context_id
            };
            crate::acquisitions::record(tx,&context,Some(&media),Some(&id),"cache_verified",&serde_json::from_str(&spec)?)?;
        }
        if let Some(candidate)=job.payload["upgrade_candidate_id"].as_str(){variant(tx,candidate,&media,&id)?;}
        Ok(())
    }).await
}

fn complete(c: &Connection, id: &str) -> Result<bool, CoreError> {
    Ok(c.query_row("SELECT EXISTS(SELECT 1 FROM upgrade_members WHERE candidate_id=?1) AND NOT EXISTS(SELECT 1 FROM upgrade_members u WHERE candidate_id=?1 AND required=1 AND (new_variant_id IS NULL OR NOT EXISTS(SELECT 1 FROM media_variants v JOIN replicas r ON r.blob_id=v.blob_id WHERE v.variant_id=u.new_variant_id AND r.state='verified') OR EXISTS(SELECT 1 FROM deleted_variants d WHERE d.variant_id=u.new_variant_id)))",[id],|r|r.get(0))?)
}

fn confirmation_current(c: &Connection, id: &str, expected: &Value) -> Result<bool, CoreError> {
    let now = snapshot(c, id)?;
    let stopped:bool=c.query_row("SELECT EXISTS(SELECT 1 FROM upgrade_candidates u JOIN jobs j ON j.job_id=u.job_id LEFT JOIN jobs parent ON parent.job_id=json_extract(j.payload,'$.completion_parent') WHERE u.candidate_id=?1 AND (j.state='cancelled' OR parent.state='cancelled')) OR EXISTS(SELECT 1 FROM upgrade_candidates u JOIN entries e ON e.entry_id=u.entry_id JOIN plugins_state p ON p.platform=e.namespace WHERE u.candidate_id=?1 AND p.enabled=0)",[id],|r|r.get(0))?;
    Ok(*expected == now
        && !stopped
        && now["lifecycle"] != "deleted"
        && now["media_suppressed"] != true
        && now["revision_id"] == now["current_revision_id"]
        && now["subscription_state"]
            .as_str()
            .is_none_or(|s| s == "enabled")
        && matches!(
            now["credential_status"].as_str(),
            Some("valid" | "authenticated" | "anonymous")
        )
        && complete(c, id)?)
}

pub fn has_pending(c: &Connection, entry: &str) -> Result<bool, CoreError> {
    let pending=c.prepare_cached("SELECT candidate_id,confirmation FROM upgrade_candidates WHERE entry_id=?1 AND state='pending'")?.query_map([entry],|r|Ok((r.get::<_,String>(0)?,r.get::<_,Option<String>>(1)?)))?.collect::<Result<Vec<_>,_>>()?;
    for (id, expected) in pending {
        if let Some(expected) = expected.and_then(|s| serde_json::from_str::<Value>(&s).ok())
            && confirmation_current(c, &id, &expected)?
        {
            return Ok(true);
        }
    }
    Ok(false)
}
pub async fn finish(core: &Core, job: &ClaimedJob) -> Result<(), CoreError> {
    let Some(id) = job.payload["upgrade_candidate_id"]
        .as_str()
        .map(str::to_owned)
    else {
        return Ok(());
    };
    let guard = job.clone();
    core.db.write(move|tx|{
        guard.check(tx)?;
        if !complete(tx,&id)? {return Err(CoreError::InvalidInput("upgrade candidate has incomplete required resources".into()))}
        let stale:bool=tx.query_row("SELECT EXISTS(SELECT 1 FROM upgrade_members u LEFT JOIN asset_selections s ON s.media_id=u.media_id WHERE u.candidate_id=?1 AND u.old_variant_id IS NOT s.variant_id)",[&id],|r|r.get(0))?;
        let same:bool=tx.query_row("SELECT NOT EXISTS(SELECT 1 FROM upgrade_members u LEFT JOIN media_variants old ON old.variant_id=u.old_variant_id LEFT JOIN media_variants new ON new.variant_id=u.new_variant_id WHERE u.candidate_id=?1 AND u.required=1 AND (old.blob_id IS NULL OR new.blob_id IS NOT old.blob_id))",[&id],|r|r.get(0))?;
        tx.execute("UPDATE upgrade_members SET expected_media_generation=(SELECT resource_generation FROM media_items WHERE media_id=upgrade_members.media_id) WHERE candidate_id=?1",[&id])?;
        let specs=tx.prepare("SELECT old.blob_id,new.blob_id,old.measured_spec,new.measured_spec FROM upgrade_members u LEFT JOIN media_variants old ON old.variant_id=u.old_variant_id LEFT JOIN media_variants new ON new.variant_id=u.new_variant_id WHERE u.candidate_id=?1 AND u.required=1")?.query_map([&id],|r|Ok((r.get::<_,Option<String>>(0)?,r.get::<_,Option<String>>(1)?,r.get::<_,Option<String>>(2)?,r.get::<_,Option<String>>(3)?)))?.collect::<Result<Vec<_>,_>>()?;
        let prefer=guard.payload["execution_scope"]["quality"]["prefer_fps"].as_bool().unwrap_or(false);
        let no_improvement=same||(!specs.is_empty()&&specs.iter().all(|(old,new,a,b)|old.is_some()&&(old==new||a.as_ref().zip(b.as_ref()).is_some_and(|(a,b)|matches!(crate::quality::comparison(&serde_json::from_str::<Value>(a).unwrap_or_default(),&serde_json::from_str::<Value>(b).unwrap_or_default(),prefer),"lower"|"same_spec")))));
        let mut expected=snapshot(tx,&id)?;
        if guard.payload["consumer_request"].as_i64().is_some() {expected["prefer_fps"]=json!(prefer);}
        let state=if stale || expected["revision_id"]!=expected["current_revision_id"] {"stale"} else if no_improvement {"no_improvement"} else {"pending"};
        let reason=if expected["revision_id"]!=expected["current_revision_id"] {"revision_changed"} else if stale {"selection_changed"} else if same {"same_verified_bytes"} else if no_improvement {"no_measured_improvement"} else if guard.payload["expected_revision_id"].is_string() && guard.payload["expected_revision_id"]!=expected["revision_id"] {"content_edited_requires_decision"} else {"verified_candidate_requires_decision"};
        tx.execute("UPDATE upgrade_candidates SET state=?2,reason=?3,confirmation=?4,finished_at=?5 WHERE candidate_id=?1 AND state='collecting'",rusqlite::params![id,state,reason,expected.to_string(),jobs::now()])?;
        if state!="pending" {tx.execute("UPDATE media_variants SET approval_state='rejected' WHERE variant_id IN (SELECT new_variant_id FROM upgrade_members WHERE candidate_id=?1 AND new_variant_id IS NOT old_variant_id) AND approval_state='pending'",[&id])?;}
        crate::objects::outbox_emit(tx,"entry.upgrade_changed",&guard.subject_id,&json!({"candidate_id":id,"state":state}))?;
        Ok(())
    }).await
}

pub fn abandon(
    tx: &Transaction<'_>,
    job: &ClaimedJob,
    state: &str,
    reason: &str,
) -> Result<(), CoreError> {
    tx.execute("UPDATE upgrade_candidates SET state=?2,reason=?4,finished_at=?3 WHERE job_id=?1 AND state='collecting'",rusqlite::params![job.job_id,state,jobs::now(),reason])?;
    Ok(())
}

pub fn invalidate_stale(tx: &Transaction<'_>) -> Result<(), CoreError> {
    tx.execute("UPDATE upgrade_candidates AS u SET state='stale',reason='comparison_scope_changed',finished_at=?1 WHERE state='pending' AND (EXISTS(SELECT 1 FROM entries e WHERE e.entry_id=u.entry_id AND (e.lifecycle='deleted' OR e.media_suppressed=1 OR e.deletion_generation!=u.deletion_generation)) OR u.revision_id IS NOT (SELECT revision_id FROM current_entry_revisions WHERE entry_id=u.entry_id) OR EXISTS(SELECT 1 FROM subscriptions s WHERE s.subscription_id=u.subscription_id AND (s.state!='enabled' OR s.config_version!=u.subscription_version OR s.credential_id IS NOT u.credential_id)) OR (u.credential_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM credentials_state k WHERE k.credential_id=u.credential_id AND k.generation=u.credential_generation AND k.status IN ('valid','authenticated'))) OR EXISTS(SELECT 1 FROM jobs j WHERE j.job_id=u.job_id AND (j.state='cancelled' OR (u.consumer_request IS NULL AND j.target_generation!=u.job_generation))) OR (u.consumer_request IS NOT NULL AND NOT EXISTS(SELECT 1 FROM job_consumers w WHERE w.job_id=u.job_id AND w.subscription_id=u.subscription_id AND w.requested=u.consumer_request AND w.state!='cancelled')) OR EXISTS(SELECT 1 FROM entries e JOIN plugins_state p ON p.platform=e.namespace WHERE e.entry_id=u.entry_id AND p.enabled=0) OR u.policy IS NOT (SELECT value FROM meta WHERE key='quality_policy') OR EXISTS(SELECT 1 FROM upgrade_members x JOIN media_items m ON m.media_id=x.media_id LEFT JOIN asset_selections s ON s.media_id=m.media_id WHERE x.candidate_id=u.candidate_id AND (m.resource_generation!=x.expected_media_generation OR s.variant_id IS NOT x.old_variant_id OR COALESCE(s.pinned,0)!=x.old_pinned)))",[jobs::now()])?;
    tx.execute("UPDATE upgrade_candidates AS u SET state='cancelled',reason='work_cancelled',finished_at=?1 WHERE state='collecting' AND EXISTS(SELECT 1 FROM jobs j WHERE j.job_id=u.job_id AND (j.state='cancelled' OR j.target_generation!=u.job_generation))",[jobs::now()])?;
    Ok(())
}

pub async fn decide(core: &Core, decisions: Vec<UpgradeDecision>) -> Result<Value, CoreError> {
    if decisions.is_empty() || decisions.len() > 50 {
        return Err(CoreError::InvalidInput(
            "choose 1..50 explicit candidates".into(),
        ));
    }
    let _gate = core.archive_access.read().await;
    // Revalidate the actual candidate objects before publishing references. A
    // missing or externally corrupted file cannot become the selected resource.
    for decision in &decisions {
        if matches!(decision.decision, UpgradeAction::Reject) {
            continue;
        }
        let id = decision.candidate_id.clone();
        let blobs=core.db.read(move|c|Ok(c.prepare("SELECT DISTINCT v.blob_id FROM upgrade_members u JOIN media_variants v ON v.variant_id=u.new_variant_id JOIN upgrade_candidates k ON k.candidate_id=u.candidate_id WHERE u.candidate_id=?1 AND k.state='pending'")?.query_map([id],|r|r.get::<_,String>(0))?.collect::<Result<Vec<_>,_>>()?)).await?;
        for blob in blobs {
            let sha = crate::paths::blob_sha(&blob)?.to_owned();
            let path = core.objects.object_path(&sha)?;
            let actual = tokio::task::spawn_blocking(move || crate::objects::hash_file(&path))
                .await
                .map_err(|_| CoreError::Internal("candidate verification task".into()))??;
            if actual != sha {
                return Err(CoreError::InvalidInput(
                    "candidate object verification failed".into(),
                ));
            }
        }
    }
    let (_, wait) = crate::quality::policy(core).await?;
    core.db.write(move|tx|{
        let mut results=Vec::new();
        for decision in decisions {
            let id=&decision.candidate_id;
            let target=match decision.decision {UpgradeAction::Accept=>"accepted",UpgradeAction::Reject=>"rejected",UpgradeAction::KeepBoth=>"kept"};
            let (state,expected,entry):(String,Option<String>,String)=tx.query_row("SELECT state,confirmation,entry_id FROM upgrade_candidates WHERE candidate_id=?1",[id],|r|Ok((r.get(0)?,r.get(1)?,r.get(2)?)))?;
            if state==target {results.push(json!({"candidate_id":id,"state":state,"duplicate":true}));continue}
            if state!="pending" {return Err(CoreError::VersionConflict("candidate is no longer pending".into()))}
            let expected:Value=expected.map(|s|serde_json::from_str(&s)).transpose()?.unwrap_or_default();
            if expected!=decision.expected || !confirmation_current(tx,id,&expected)? {
                return Err(CoreError::VersionConflict("upgrade comparison changed; refresh the comparison".into()));
            }
            let rows=tx.prepare("SELECT media_id,old_variant_id,new_variant_id FROM upgrade_members WHERE candidate_id=?1 AND new_variant_id IS NOT NULL")?.query_map([id],|r|Ok((r.get::<_,String>(0)?,r.get::<_,Option<String>>(1)?,r.get::<_,String>(2)?)))?.collect::<Result<Vec<_>,_>>()?;
            let ts=jobs::now();
            let eligible=(time::OffsetDateTime::now_utc()+time::Duration::seconds(wait)).format(&time::format_description::well_known::Rfc3339).map_err(|_|CoreError::Internal("clock".into()))?;
            for (media,old,new) in rows {
                if old.as_deref()==Some(new.as_str()){continue}
                tx.execute("UPDATE media_variants SET approval_state=?2 WHERE variant_id=?1 AND approval_state='pending'",rusqlite::params![new,target])?;
                if matches!(decision.decision,UpgradeAction::Reject) {
                    tx.execute("INSERT INTO variant_retention(variant_id,retained,reason,eligible_at,updated_at) VALUES(?1,0,'upgrade_rejected',?2,?3) ON CONFLICT(variant_id) DO UPDATE SET retained=0,reason='upgrade_rejected',eligible_at=excluded.eligible_at WHERE pinned=0",rusqlite::params![new,eligible,ts])?;
                    continue;
                }
                tx.execute("INSERT INTO asset_selections(media_id,variant_id,policy_version,reason,updated_at) VALUES(?1,?2,'m1-r2','user_confirmed_upgrade',?3) ON CONFLICT(media_id) DO UPDATE SET variant_id=excluded.variant_id,reason=excluded.reason,policy_version=excluded.policy_version,updated_at=excluded.updated_at",rusqlite::params![media,new,ts])?;
                tx.execute("INSERT INTO variant_retention(variant_id,retained,pinned,reason,updated_at) VALUES(?1,1,?2,'user_confirmed_upgrade',?3) ON CONFLICT(variant_id) DO UPDATE SET retained=1,pinned=MAX(pinned,excluded.pinned),eligible_at=NULL,reason=excluded.reason",rusqlite::params![new,matches!(decision.decision,UpgradeAction::KeepBoth),ts])?;
                if let Some(old)=old {
                    // Keep prior copies unless their measured tracks and duration
                    // prove comparability. Shared and manually pinned bytes remain protected.
                    let (old_spec,new_spec):(Option<String>,Option<String>)=tx.query_row("SELECT old.measured_spec,new.measured_spec FROM media_variants old,media_variants new WHERE old.variant_id=?1 AND new.variant_id=?2",rusqlite::params![old,new],|r|Ok((r.get(0)?,r.get(1)?)))?;
                    let comparable=old_spec.zip(new_spec).is_some_and(|(a,b)|crate::quality::measured(&a).zip(crate::quality::measured(&b)).is_some_and(|(a,b)|crate::quality::comparable(&a,&b)));
                    let pinned:bool=tx.query_row("SELECT old_pinned FROM upgrade_members WHERE candidate_id=?1 AND media_id=?2",rusqlite::params![id,media],|r|r.get(0))?;
                    let keep=pinned||matches!(decision.decision,UpgradeAction::KeepBoth)||!comparable;
                    tx.execute("INSERT INTO variant_retention(variant_id,retained,pinned,reason,eligible_at,updated_at) VALUES(?1,?2,?3,?4,?5,?6) ON CONFLICT(variant_id) DO UPDATE SET retained=CASE WHEN pinned=1 THEN 1 ELSE excluded.retained END,pinned=MAX(pinned,excluded.pinned),reason=excluded.reason,eligible_at=CASE WHEN pinned=1 OR excluded.retained=1 THEN NULL ELSE excluded.eligible_at END",rusqlite::params![old,keep,pinned||matches!(decision.decision,UpgradeAction::KeepBoth),if keep{"user_preserved_previous"}else{"user_confirmed_replacement"},if keep{None}else{Some(&eligible)},ts])?;
                }
            }
            tx.execute("UPDATE upgrade_candidates SET state=?2,finished_at=?3 WHERE candidate_id=?1",rusqlite::params![id,target,ts])?;
            crate::objects::outbox_emit(tx,"entry.upgrade_changed",&entry,&json!({"candidate_id":id,"state":target}))?;
            results.push(json!({"candidate_id":id,"state":target,"duplicate":false}));
        }
        Ok(json!({"items":results}))
    }).await
}

pub async fn list(core: &Core, args: Value) -> Result<Value, CoreError> {
    core.db.write(invalidate_stale).await?;
    let state = args["state"].as_str().map(str::to_owned);
    let namespace = args["namespace"].as_str().map(str::to_owned);
    let creator = args["creator_id"].as_str().map(str::to_owned);
    let before = args["before"].as_i64().unwrap_or(i64::MAX);
    core.db.read(move|c|{
        let filter="(?1 IS NULL OR u.state=?1) AND (?2 IS NULL OR e.namespace=?2) AND (?3 IS NULL OR EXISTS(SELECT 1 FROM account_bindings b WHERE b.account_id=e.account_id AND b.creator_id=?3 AND b.state='active'))";
        let mut items=c.prepare(&format!("SELECT u.rowid,u.candidate_id,u.entry_id,u.revision_id,u.state,u.reason,u.created_at,u.finished_at,u.confirmation,x.auth_state,substr(r.title,1,512),e.namespace,x.started_at FROM upgrade_candidates u JOIN entries e ON e.entry_id=u.entry_id JOIN entry_revisions r ON r.revision_id=u.revision_id JOIN acquisition_contexts x ON x.context_id=u.context_id WHERE {filter} AND u.rowid<?4 ORDER BY u.rowid DESC LIMIT 11"))?.query_map(rusqlite::params![state,namespace,creator,before],|r|Ok(json!({"sequence":r.get::<_,i64>(0)?,"candidate_id":r.get::<_,String>(1)?,"entry_id":r.get::<_,String>(2)?,"revision_id":r.get::<_,String>(3)?,"state":r.get::<_,String>(4)?,"reason":r.get::<_,String>(5)?,"created_at":r.get::<_,String>(6)?,"finished_at":r.get::<_,Option<String>>(7)?,"expected":r.get::<_,Option<String>>(8)?.and_then(|v|serde_json::from_str::<Value>(&v).ok()),"auth_state":r.get::<_,String>(9)?,"title":r.get::<_,String>(10)?,"namespace":r.get::<_,String>(11)?,"acquired_at":r.get::<_,String>(12)?})))?.collect::<Result<Vec<_>,_>>()?;
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

fn resources_on(c: &Connection, id: &str, after: i64) -> Result<Value, CoreError> {
    let mut resources=c.prepare("SELECT m.media_id,m.role,u.required,old.variant_id,ob.size,old.measured_spec,old.created_at,new.variant_id,nb.size,new.measured_spec,new.created_at,COALESCE((SELECT x.auth_state FROM resource_acquisitions a JOIN acquisition_contexts x ON x.context_id=a.context_id WHERE a.variant_id=old.variant_id ORDER BY a.sequence DESC LIMIT 1),'unknown'),EXISTS(SELECT 1 FROM variant_retention k WHERE k.variant_id=old.variant_id AND k.pinned=1),m.rowid FROM upgrade_members u JOIN media_items m ON m.media_id=u.media_id LEFT JOIN media_variants old ON old.variant_id=u.old_variant_id LEFT JOIN blobs ob ON ob.blob_id=old.blob_id LEFT JOIN media_variants new ON new.variant_id=u.new_variant_id LEFT JOIN blobs nb ON nb.blob_id=new.blob_id WHERE candidate_id=?1 AND m.rowid>?2 ORDER BY m.rowid LIMIT 21")?.query_map(rusqlite::params![id,after],|r|Ok(json!({"sequence":r.get::<_,i64>(13)?,"media_id":r.get::<_,String>(0)?,"role":r.get::<_,String>(1)?,"required":r.get::<_,bool>(2)?,"old":{"variant_id":r.get::<_,Option<String>>(3)?,"bytes":r.get::<_,Option<i64>>(4)?,"spec":r.get::<_,Option<String>>(5)?.and_then(|v|serde_json::from_str::<Value>(&v).ok()),"saved_at":r.get::<_,Option<String>>(6)?,"auth_state":r.get::<_,String>(11)?,"pinned":r.get::<_,bool>(12)?},"new":{"variant_id":r.get::<_,Option<String>>(7)?,"bytes":r.get::<_,Option<i64>>(8)?,"spec":r.get::<_,Option<String>>(9)?.and_then(|v|serde_json::from_str::<Value>(&v).ok()),"saved_at":r.get::<_,Option<String>>(10)?}})))?.collect::<Result<Vec<_>,_>>()?;
    let more = resources.len() > 20;
    resources.truncate(20);
    // The current shared job may already execute another consumer. Historical
    // tokens without a frozen preference can use only an unchanged origin.
    let prefer:Option<bool>=c.query_row("SELECT COALESCE(json_extract(u.confirmation,'$.prefer_fps'),CASE WHEN s.config_version=u.subscription_version THEN COALESCE(json_extract(s.quality_json,'$.prefer_fps'),0) WHEN u.subscription_id IS NULL THEN 0 END) FROM upgrade_candidates u LEFT JOIN subscriptions s ON s.subscription_id=u.subscription_id WHERE u.candidate_id=?1",[id],|r|r.get(0))?;
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
    let total: i64 = c.query_row(
        "SELECT count(*) FROM upgrade_members WHERE candidate_id=?1",
        [id],
        |r| r.get(0),
    )?;
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
