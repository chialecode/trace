//! Persistent recording sessions. Adapters resolve identities and short-lived
//! plans; this module alone receives bytes and commits them through ObjectStore.
pub mod auxiliary;
pub mod flv;
pub mod hls;
mod subtitles;
#[cfg(test)]
pub(crate) mod tests;
use crate::{
    core::Core,
    error::CoreError,
    jobs::{self, ClaimedJob},
    objects::{CommitInfo, ReceiveParams, StagedWriter},
    transport::{HostRules, host_rules},
};
use serde_json::{Value, json};
use std::{
    sync::atomic::Ordering,
    time::{Duration, Instant},
};
use trace_contracts::plugin::{LiveSnapshot, LiveStream};
use trace_domain::ids::OperationId;
use url::Url;
const RESOURCE_LIMIT: usize = 64 * 1024 * 1024;

fn invalid(message: &str) -> CoreError {
    CoreError::InvalidInput(message.into())
}
async fn reschedule(core: &Core, job: &ClaimedJob, seconds: u64) -> Result<(), CoreError> {
    let j = job.clone();
    let next = (time::OffsetDateTime::now_utc() + time::Duration::seconds(seconds as i64))
        .format(&time::format_description::well_known::Rfc3339)
        .map_err(|_| invalid("clock"))?;
    core.db.write(move|tx|{j.check(tx)?;tx.execute("UPDATE jobs SET state='queued',lease_owner=NULL,lease_expires_at=NULL,next_run_at=?2 WHERE job_id=?1",rusqlite::params![j.job_id,next])?;Ok(())}).await
}
pub async fn poll(
    core: &Core,
    job: &ClaimedJob,
    account: &str,
    platform: &str,
) -> Result<(), CoreError> {
    core.metadata_pacing_priority(platform, true).await;
    let value = core
        .adapter_call(
            job,
            platform,
            "probe_live",
            json!({"native_account_key":account}),
            Some(30_000),
        )
        .await?;
    let snapshot: LiveSnapshot = serde_json::from_value(value)?;
    if let Some(session) = register(core, job, snapshot).await? {
        auxiliary::schedule(core, job, &session).await?;
    }
    let sub = job.payload["subscription_id"]
        .as_str()
        .ok_or_else(|| invalid("live subscription"))?
        .to_owned();
    let interval = core
        .db
        .read(move |c| {
            Ok(c.query_row(
                "SELECT check_interval_seconds FROM subscriptions WHERE subscription_id=?1",
                [sub],
                |r| r.get::<_, i64>(0),
            )?)
        })
        .await?;
    reschedule(
        core,
        job,
        crate::config::jittered_check_seconds(interval) as u64,
    )
    .await
}

pub async fn register(
    core: &Core,
    job: &ClaimedJob,
    snapshot: LiveSnapshot,
) -> Result<Option<String>, CoreError> {
    if !matches!(snapshot.state.as_str(), "live" | "offline" | "unknown")
        || snapshot.room_key.is_empty()
    {
        return Err(invalid("invalid live detection"));
    }
    if snapshot.state == "unknown" {
        return Err(CoreError::Network("live status unknown".into()));
    }
    let sub = job.payload["subscription_id"]
        .as_str()
        .ok_or_else(|| invalid("live subscription"))?
        .to_owned();
    let guard = job.clone();
    core.db.write(move|tx|{
        guard.check(tx)?;let ts=jobs::now();
        if snapshot.state=="offline" {
            tx.execute("UPDATE live_sessions SET state=CASE WHEN state='recording' THEN 'stopping' ELSE 'sealed' END,ended_at=?2,stop_reason='source_offline' WHERE subscription_id=?1 AND ended_at IS NULL",rusqlite::params![sub,ts])?;
            return Ok(None);
        }
        use rusqlite::OptionalExtension;
        let duplicate:bool=tx.query_row("SELECT EXISTS(SELECT 1 FROM live_sessions l JOIN entries e ON e.entry_id=l.entry_id JOIN subscriptions previous ON previous.subscription_id=l.subscription_id JOIN subscriptions current ON current.subscription_id=?1 JOIN source_accounts a ON a.account_id=current.account_id WHERE l.subscription_id!=?1 AND l.room_key=?2 AND e.namespace=a.namespace AND l.state IN ('waiting','recording','reconnecting','stopping') AND (previous.acquisition_kind='one_shot' OR current.acquisition_kind='one_shot'))",rusqlite::params![sub,snapshot.room_key],|r|r.get(0))?;
        if duplicate {return Err(CoreError::VersionConflict("room already has an active recording".into()));}
        // The first poll may lack an upstream session identity. Upgrade the
        // durable local session in place; its entry, fragments and stop choice
        // remain the same. A later distinct upstream session closes the old one.
        if let Some(source)=snapshot.source_session.as_deref(){
            let local:Option<(String,String,String)>=tx.query_row("SELECT session_id,entry_id,source_session FROM live_sessions WHERE subscription_id=?1 AND room_key=?2 AND ended_at IS NULL AND source_session LIKE 'local-%' ORDER BY started_at DESC LIMIT 1",rusqlite::params![sub,snapshot.room_key],|r|Ok((r.get(0)?,r.get(1)?,r.get(2)?))).optional()?;
            let known:bool=tx.query_row("SELECT EXISTS(SELECT 1 FROM live_sessions WHERE subscription_id=?1 AND room_key=?2 AND source_session=?3)",rusqlite::params![sub,snapshot.room_key,source],|r|r.get(0))?;
            if let Some((sid,entry,prior))=local.filter(|_|!known){
                tx.execute("UPDATE live_sessions SET source_session=?2,evidence=json_set(evidence,'$.identity_basis','source_after_local','$.prior_local_identity',?3,'$.source_session',?2) WHERE session_id=?1",rusqlite::params![sid,source,prior])?;
                tx.execute("UPDATE entries SET native_entry_key=?2 WHERE entry_id=?1",rusqlite::params![entry,format!("{}:{source}",snapshot.room_key)])?;
            }
        }
        let source=snapshot.source_session.clone().or_else(||tx.query_row("SELECT source_session FROM live_sessions WHERE subscription_id=?1 AND room_key=?2 AND ended_at IS NULL ORDER BY started_at DESC LIMIT 1",rusqlite::params![sub,snapshot.room_key],|r|r.get(0)).optional().ok().flatten()).unwrap_or_else(||format!("local-{}",uuid::Uuid::new_v4()));
        tx.execute("UPDATE live_sessions SET state='stopping',ended_at=?4,stop_reason='source_session_replaced' WHERE subscription_id=?1 AND ended_at IS NULL AND (room_key!=?2 OR source_session!=?3)",rusqlite::params![sub,snapshot.room_key,source,ts])?;
        let existing:Option<(String,i64)>=tx.query_row("SELECT session_id,manual_stopped FROM live_sessions WHERE subscription_id=?1 AND room_key=?2 AND source_session=?3",rusqlite::params![sub,snapshot.room_key,source],|r|Ok((r.get(0)?,r.get(1)?))).optional()?;
        if let Some((id,stopped))=existing {if stopped!=0{return Ok(None);}return Ok(Some(id));}
        let sid=format!("live-{}",uuid::Uuid::new_v4());let entry=format!("ent-{}",uuid::Uuid::new_v4());let revision=format!("rev-{}",uuid::Uuid::new_v4());
        tx.execute("INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at,published_at,published_precision) SELECT ?1,a.namespace,'live',?2,a.account_id,?3,?3,'discovery' FROM source_accounts a JOIN subscriptions s ON s.account_id=a.account_id WHERE s.subscription_id=?4",rusqlite::params![entry,format!("{}:{source}",snapshot.room_key),ts,sub])?;
        let evidence=trace_contracts::content::safe_metadata(&json!({"room":snapshot.metadata,"source_session":snapshot.source_session,"identity_basis":if source.starts_with("local-"){ "local_until_confirmed_offline"}else{"source"}}));
        tx.execute("INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,completeness,observed_at,metadata_json) VALUES(?1,?2,?3,?4,'','full',?5,?6)",rusqlite::params![revision,entry,crate::config::short_hash(&source),snapshot.title,ts,evidence.to_string()])?;
        tx.execute("INSERT INTO live_sessions(session_id,subscription_id,room_key,source_session,entry_id,state,started_at,evidence) VALUES(?1,?2,?3,?4,?5,'waiting',?6,?7)",rusqlite::params![sid,sub,snapshot.room_key,source,entry,ts,evidence.to_string()])?;
        tx.execute("INSERT INTO jobs(job_id,job_key,job_type,subject_id,payload,priority,state,created_at,updated_at) VALUES(?1,?2,'record',?3,?4,0,'queued',?5,?5)",rusqlite::params![format!("job-{}",uuid::Uuid::new_v4()),format!("record:{sid}"),sid,json!({"session_id":sid,"subscription_id":sub}).to_string(),ts])?;
        crate::objects::outbox_emit(tx,"entry.revision.created",&entry,&json!({"session_id":sid}))?;
        Ok(Some(sid))
    }).await
}

async fn state(core: &Core, job: &ClaimedJob) -> Result<String, CoreError> {
    if core.cancel.is_cancelled() || core.paused.load(Ordering::SeqCst) {
        return Err(CoreError::Paused("recording suspended".into()));
    }
    let j = job.clone();
    core.db
        .read(move |c| {
            j.check(c)?;
            Ok(c.query_row(
                "SELECT state FROM live_sessions WHERE session_id=?1",
                [j.subject_id],
                |r| r.get(0),
            )?)
        })
        .await
}
pub async fn run(core: &Core, job: &ClaimedJob) -> Result<(), CoreError> {
    let _permit = core
        .live_global
        .try_acquire()
        .map_err(|_| CoreError::Paused("recording concurrency limit".into()))?;
    recover_pending(core, job).await?;
    let j = job.clone();
    let (account,namespace):(String,String)=core.db.read(move|c|Ok(c.query_row("SELECT a.native_account_key,a.namespace FROM live_sessions l JOIN subscriptions s ON s.subscription_id=l.subscription_id JOIN source_accounts a ON a.account_id=s.account_id WHERE l.session_id=?1",[j.subject_id],|r|Ok((r.get(0)?,r.get(1)?)))?)).await?;
    if matches!(state(core, job).await?.as_str(), "stopping" | "sealed") {
        return seal_session(core, job).await;
    }
    let platform = namespace.split(':').next().unwrap_or("");
    let session = job.subject_id.clone();
    let entry: String = core
        .db
        .read(move |c| {
            Ok(c.query_row(
                "SELECT entry_id FROM live_sessions WHERE session_id=?1",
                [session],
                |r| r.get(0),
            )?)
        })
        .await?;
    let context = crate::acquisitions::begin(core, job, platform, &entry).await?;
    let mut acquisition_job = job.clone();
    acquisition_job.payload["acquisition_context_id"] = json!(context.context_id.clone());
    let job = &acquisition_job;
    if let Err(error) = crate::acquisitions::require_access(&context) {
        gap(
            core,
            job,
            format!("authentication:{}", context.context_id),
            if context.auth_state == trace_contracts::acquisition::AuthenticationState::Expired {
                "authentication_expired"
            } else {
                "authentication_unknown"
            },
        )
        .await?;
        return Err(error);
    }
    let snapshot: LiveSnapshot = if job.payload["source_request_id"].is_string() {
        crate::source_acquisition::resolve_live(core, job).await?
    } else {
        let v = core
            .adapter_call(
                job,
                platform,
                "probe_live",
                json!({"native_account_key":account}),
                Some(30_000),
            )
            .await?;
        serde_json::from_value(v)?
    };
    let context =
        crate::acquisitions::resolution_context(core, job, &entry, context, &snapshot.metadata)
            .await?;
    let mut resolved_job = job.clone();
    resolved_job.payload["acquisition_context_id"] = json!(context.context_id);
    let job = &resolved_job;
    if snapshot.state == "offline" {
        return seal_session(core, job).await;
    }
    if snapshot.state != "live" {
        return Err(CoreError::Network("live status unknown".into()));
    }
    if register(core, job, snapshot.clone()).await?.as_deref() != Some(job.subject_id.as_str()) {
        return seal_session(core, job).await;
    }
    let stream = snapshot
        .streams
        .iter()
        .filter(|s| matches!(s.protocol.as_str(), "flv" | "hls"))
        .max_by_key(|s| {
            (
                if s.protocol == "hls" { 1 } else { 0 },
                quality_rank(&s.quality),
            )
        })
        .ok_or_else(|| CoreError::Unsupported("no recording stream".into()))?;
    let j = job.clone();
    core.db
        .write(move |tx| {
            j.check(tx)?;
            tx.execute(
                "UPDATE live_sessions SET state='recording' WHERE session_id=?1",
                [j.subject_id],
            )?;
            Ok(())
        })
        .await?;
    let result = receive(core, job, stream).await;
    match result {
        Ok(ended) => {
            if ended {
                seal_session(core, job).await
            } else {
                reschedule(core, job, 2).await
            }
        }
        Err(e) => {
            let j = job.clone();
            let reason = format!("{:?}", e.failure_class());
            let _=core.db.write(move|tx|{j.check(tx)?;tx.execute("UPDATE live_sessions SET state='reconnecting',stop_reason=?2 WHERE session_id=?1",rusqlite::params![j.subject_id,reason])?;Ok(())}).await;
            Err(e)
        }
    }
}

/// Receive one bounded recording interval using the same path as the scheduler.
/// Used by the offline load harness with an explicitly isolated Profile.
pub async fn receive(
    core: &Core,
    job: &ClaimedJob,
    stream: &LiveStream,
) -> Result<bool, CoreError> {
    let mut acquisition_job = job.clone();
    if acquisition_job.payload["acquisition_context_id"].is_null() {
        let session = job.subject_id.clone();
        let entry: String = core
            .db
            .read(move |c| {
                Ok(c.query_row(
                    "SELECT entry_id FROM live_sessions WHERE session_id=?1",
                    [session],
                    |r| r.get(0),
                )?)
            })
            .await?;
        let context = trace_contracts::acquisition::AcquisitionContext {
            context_id: format!("ctx-{}", uuid::Uuid::new_v4()),
            auth_state: trace_contracts::acquisition::AuthenticationState::Unknown,
            credential_id: None,
            credential_generation: None,
            validated_at: None,
            basis: "direct_transport_without_resolution_proof".into(),
            started_at: crate::jobs::now(),
        };
        let saved = context.clone();
        let guard = job.clone();
        core.db
            .write(move |tx| {
                guard.check(tx)?;
                crate::acquisitions::insert_context(tx, &guard, &entry, &saved)
            })
            .await?;
        acquisition_job.payload["acquisition_context_id"] = json!(context.context_id);
    }
    let job = &acquisition_job;
    let _reservation = core.objects.budget.recording(&job.subject_id);
    recover_pending(core, job).await?;
    match stream.protocol.as_str() {
        "hls" => {
            let work = record_hls(core, job, stream);
            tokio::pin!(work);
            let mut tick = tokio::time::interval(Duration::from_millis(200));
            loop {
                tokio::select! {
                    result=&mut work=>break result,
                    _=tick.tick()=>{if state(core,job).await?=="stopping"{break Ok(true);}}
                }
            }
        }
        "flv" => record_flv(core, job, stream).await,
        _ => Err(CoreError::Unsupported("recording protocol".into())),
    }
}
async fn media_rules(core: &Core, job: &ClaimedJob) -> Result<HostRules, CoreError> {
    let id = job.subject_id.clone();
    let site:String=core.db.read(move|c|Ok(c.query_row("SELECT e.namespace FROM live_sessions l JOIN entries e ON e.entry_id=l.entry_id WHERE l.session_id=?1",[id],|r|r.get(0))?)).await?;
    Ok(crate::transport::platform_rules(
        &host_rules(&core.profile),
        site.split(':').next().unwrap_or(&site),
    ))
}
fn quality_rank(q: &str) -> u64 {
    q.parse().unwrap_or(match q {
        "origin" | "ORIGIN" | "FULL_HD1" => 10000,
        "uhd" => 5000,
        "hd" | "HD1" => 2000,
        _ => 0,
    })
}
async fn seal_session(core: &Core, job: &ClaimedJob) -> Result<(), CoreError> {
    let j = job.clone();
    core.db.write(move|tx|{j.check(tx)?;tx.execute("UPDATE live_sessions SET state='sealed',ended_at=COALESCE(ended_at,?2) WHERE session_id=?1",rusqlite::params![j.subject_id,jobs::now()])?;crate::objects::outbox_emit(tx,"live.updated",&j.subject_id,&json!({"state":"sealed"}))?;Ok(())}).await
}

struct Fragment {
    id: String,
    media: String,
    op: OperationId,
    writer: StagedWriter,
}
async fn begin(
    core: &Core,
    job: &ClaimedJob,
    source: String,
    mime: &str,
    start: i64,
) -> Result<Option<Fragment>, CoreError> {
    disk_budget(core, 1024 * 1024)?;
    let j = job.clone();
    let id = format!("frag-{}", uuid::Uuid::new_v4());
    let fid = id.clone();
    let media = format!("med-{}", uuid::Uuid::new_v4());
    let mid = media.clone();
    let created=core.db.write(move|tx|{
        j.check(tx)?;let exists:bool=tx.query_row("SELECT EXISTS(SELECT 1 FROM live_fragments WHERE session_id=?1 AND source_sequence=?2 AND state IN ('receiving','sealed','gap','source'))",rusqlite::params![j.subject_id,source],|r|r.get(0))?;if exists{return Ok(false);}
        let revision:String=tx.query_row("SELECT r.revision_id FROM live_sessions l JOIN entry_revisions r ON r.entry_id=l.entry_id WHERE l.session_id=?1",[&j.subject_id],|r|r.get(0))?;
        let seq:i64=tx.query_row("SELECT COALESCE(MAX(sequence)+1,0) FROM live_fragments WHERE session_id=?1",[&j.subject_id],|r|r.get(0))?;
        tx.execute("INSERT INTO media_items(media_id,revision_id,role,position,source_attachment_id,required) VALUES(?1,?2,'recording',?3,?4,1)",rusqlite::params![mid,revision,seq,fid])?;
        tx.execute("INSERT INTO live_fragments(fragment_id,session_id,sequence,source_sequence,media_id,state,start_ms,captured_at,retry_of) VALUES(?1,?2,?3,?4,?5,'receiving',?6,strftime('%Y-%m-%dT%H:%M:%SZ','now'),(SELECT fragment_id FROM live_fragments WHERE session_id=?2 AND source_sequence=?4 AND state='interrupted' ORDER BY sequence DESC LIMIT 1))",rusqlite::params![fid,j.subject_id,seq,source,mid,start])?;tx.execute("UPDATE live_fragments SET context_id=?2 WHERE fragment_id=?1",rusqlite::params![fid,j.payload["acquisition_context_id"].as_str()])?;Ok(true)
    }).await?;
    if !created {
        return Ok(None);
    }
    let op = OperationId::new(format!("live-{id}"));
    let writer = core
        .objects
        .start_receiving(
            &core.db,
            ReceiveParams {
                job_id: Some(job.job_id.clone()),
                subject_kind: "live_fragment".into(),
                subject_id: media.clone(),
                generation: job.generation,
                mime: Some(mime.into()),
                expected_size: None,
                expected_sha256: None,
                source_validator: None,
            },
            &op,
        )
        .await?;
    Ok(Some(Fragment {
        id,
        media,
        op,
        writer,
    }))
}
fn disk_budget(core: &Core, bytes: usize) -> Result<(), CoreError> {
    core.objects.budget.check(&core.profile, bytes)
}

async fn finish(
    core: &Core,
    job: &ClaimedJob,
    f: Fragment,
    end: i64,
    reason: Option<&str>,
    mut spec: Value,
) -> Result<(), CoreError> {
    spec["requested_quality"] =
        serde_json::to_value(crate::quality::requirement(core, job).await?)?;
    spec["direct_source"] = json!(job.payload["source_request_id"].is_string());
    spec["captured_until"] = json!(jobs::now());
    let _sealing = core.recording_recovery.lock().await;
    let fid = f.id.clone();
    let why = reason.map(str::to_owned);
    let evidence = trace_contracts::content::safe_metadata(&spec).to_string();
    core.db.write(move|tx|{tx.execute("UPDATE live_fragments SET end_ms=?2,reason=?3,evidence=?4 WHERE fragment_id=?1 AND state='receiving'",rusqlite::params![fid,end,why,evidence])?;Ok(())}).await?;
    let (_, sha) = core
        .objects
        .finish_and_validate(&core.db, &f.op, f.writer)
        .await?;
    core.objects.install_object(&core.db, &f.op, &sha).await?;
    let original = spec["original_capture"] == true;
    let measured = if original {
        json!({"original_capture":true,"container_validation":"not_applicable_raw_capture"})
    } else {
        validate_fragment(core, &sha).await?
    };
    if !admit_quality(core, &f.id, &f.op, &spec, &measured).await? {
        return Err(CoreError::Adapter(
            trace_domain::errors::FailureClass::CapabilityLost,
        ));
    }
    // A revoked lease leaves OBJECT_READY for guarded recovery, not a late reference.
    core.objects
        .commit(
            &core.db,
            &f.op,
            &CommitInfo {
                job: Some(job.clone()),
                media_id: f.media,
                source_representation: if spec["original_capture"] == true {
                    "recorded-original"
                } else {
                    "recorded-source"
                }
                .into(),
                claimed_spec: spec,
            },
            Some(measured),
        )
        .await?;
    let j = job.clone();
    let reason = reason.map(str::to_owned);
    core.db
        .write(move |tx| {
            j.check(tx)?;
            tx.execute(
                "UPDATE live_fragments SET state=?4,end_ms=?2,reason=?3 WHERE fragment_id=?1",
                rusqlite::params![f.id, end, reason,if original{"source"}else{"sealed"}],
            )?;
            tx.execute("UPDATE live_fragments SET state='superseded',reason='recovered_by_retry' WHERE session_id=?1 AND state='interrupted' AND source_sequence=(SELECT source_sequence FROM live_fragments WHERE fragment_id=?2)",rusqlite::params![j.subject_id,f.id])?;
            tx.execute("UPDATE media_items SET required=0 WHERE media_id IN (SELECT media_id FROM live_fragments WHERE session_id=?1 AND state='superseded')",[&j.subject_id])?;
            crate::objects::outbox_emit(
                tx,
                "live.fragment.sealed",
                &j.subject_id,
                &json!({"fragment_id":f.id}),
            )?;
            Ok(())
        })
        .await
}
/// Raw captures remain separate provenance, never accepted playback media. The
/// measured derivative alone is subject to admission; rejected bytes stay in
/// quarantine with an explicit gap and cannot be promoted by crash recovery.
async fn admit_quality(
    core: &Core,
    fragment: &str,
    op: &OperationId,
    evidence: &Value,
    measured: &Value,
) -> Result<bool, CoreError> {
    let p: trace_contracts::control::QualityRequirement =
        serde_json::from_value(evidence["requested_quality"].clone()).unwrap_or_default();
    if evidence["original_capture"] == true {
        return Ok(true);
    }
    let has_video = measured["codec"].as_str().is_some();
    let auxiliary = evidence["track"]
        .as_str()
        .is_some_and(|s| s.starts_with("audio") || s.starts_with("subtitle"));
    let matches = (evidence["direct_source"] != true
        || auxiliary
        || crate::source_acquisition::live_meets_minimum(measured))
        && (auxiliary
            || has_video && crate::quality::meets(&p, measured)
            || p.min_height.is_none() && p.min_fps.is_none());
    let id = fragment.to_owned();
    let measured = measured.clone();
    let requested = serde_json::to_value(&p)?;
    core.db.write(move|tx|{tx.execute("UPDATE media_items SET acquisition_evidence=json_set(COALESCE(acquisition_evidence,'{}'),'$.requested_quality',json(?2),'$.actual_quality',json(?3),'$.below_target',?4) WHERE media_id=(SELECT media_id FROM live_fragments WHERE fragment_id=?1)",rusqlite::params![id,requested.to_string(),measured.to_string(),!matches])?;Ok(())}).await?;
    if !p.strict || matches {
        return Ok(true);
    }
    core.objects.quarantine(&core.db, op).await?;
    let id = fragment.to_owned();
    core.db.write(move|tx|{tx.execute("UPDATE live_fragments SET state='gap',reason='strict_quality_unrecorded',discontinuity=1 WHERE fragment_id=?1",[&id])?;crate::objects::outbox_emit(tx,"live.quality_gap",&id,&json!({"reason":"strict_quality_unrecorded"}))?;Ok(())}).await?;
    Ok(false)
}

async fn gap(core: &Core, job: &ClaimedJob, source: String, reason: &str) -> Result<(), CoreError> {
    let j = job.clone();
    let reason = reason.to_owned();
    core.db.write(move|tx|{j.check(tx)?;tx.execute("INSERT OR IGNORE INTO live_fragments(fragment_id,session_id,sequence,source_sequence,state,reason,discontinuity,context_id) VALUES(?1,?2,COALESCE((SELECT MAX(sequence)+1 FROM live_fragments WHERE session_id=?2),0),?3,'gap',?4,1,?5)",rusqlite::params![format!("gap-{}",uuid::Uuid::new_v4()),j.subject_id,source,reason,j.payload["acquisition_context_id"].as_str()])?;Ok(())}).await
}

async fn validate_fragment(core: &Core, sha: &str) -> Result<Value, CoreError> {
    let spec = core
        .media_backend
        .probe(
            &core.objects.object_path(sha)?,
            &tokio_util::sync::CancellationToken::new(),
        )
        .await?;
    if spec.codec.is_none() && spec.audio_codec.is_none() && spec.subtitle_tracks.unwrap_or(0) == 0
    {
        return Err(invalid("recording fragment has no decodable stream"));
    }
    serde_json::to_value(spec).map_err(CoreError::from)
}

async fn recover_pending(core: &Core, job: &ClaimedJob) -> Result<(), CoreError> {
    let _sealing = core.recording_recovery.lock().await;
    let orphan_job = job.clone();
    core.db.write(move|tx|{orphan_job.check(tx)?;tx.execute("UPDATE live_fragments SET state='interrupted',reason='interrupted_before_receiving' WHERE session_id=?1 AND state='receiving' AND NOT EXISTS(SELECT 1 FROM materializations m WHERE m.subject_id=live_fragments.media_id)",[orphan_job.subject_id])?;Ok(())}).await?;
    let j = job.clone();
    let pending:Vec<(String,String,String,String,Option<String>,String)>=core.db.read(move|c|{j.check(c)?;Ok(c.prepare("SELECT f.fragment_id,f.media_id,m.operation_id,m.stage,m.received_sha256,f.evidence FROM live_fragments f JOIN materializations m ON m.subject_id=f.media_id WHERE f.session_id=?1 AND f.state='receiving'")?.query_map([j.subject_id],|r|Ok((r.get(0)?,r.get(1)?,r.get(2)?,r.get(3)?,r.get(4)?,r.get(5)?)))?.collect::<Result<Vec<_>,_>>()?)}).await?;
    for (fid, mid, op, stage, sha, evidence) in pending {
        let evidence: Value = serde_json::from_str(&evidence)?;
        let original = evidence["original_capture"] == true;
        if matches!(stage.as_str(), "PREPARED" | "OBJECT_READY" | "COMMITTED") {
            let opid = OperationId::new(op.clone());
            if stage == "PREPARED" {
                core.objects
                    .install_object(
                        &core.db,
                        &opid,
                        sha.as_deref()
                            .ok_or_else(|| invalid("sealed hash missing"))?,
                    )
                    .await?;
            }
            let j = job.clone();
            core.db
                .write(move |tx| {
                    j.check(tx)?;
                    tx.execute(
                        "UPDATE materializations SET job_id=?2,generation=?3 WHERE operation_id=?1",
                        rusqlite::params![op, j.job_id, j.generation],
                    )?;
                    Ok(())
                })
                .await?;
            let measured = if original {
                json!({"original_capture":true})
            } else {
                match validate_fragment(
                    core,
                    sha.as_deref()
                        .ok_or_else(|| invalid("sealed hash missing"))?,
                )
                .await
                {
                    Ok(spec) => spec,
                    Err(_) => {
                        core.objects.quarantine(&core.db, &opid).await?;
                        let j = job.clone();
                        core.db.write(move|tx|{j.check(tx)?;tx.execute("UPDATE live_fragments SET state='interrupted',reason='recovery_container_invalid' WHERE fragment_id=?1",[fid])?;Ok(())}).await?;
                        continue;
                    }
                }
            };
            if !admit_quality(core, &fid, &opid, &evidence, &measured).await? {
                continue;
            }
            core.objects
                .commit(
                    &core.db,
                    &opid,
                    &CommitInfo {
                        job: Some(job.clone()),
                        media_id: mid,
                        source_representation: if original {
                            "recorded-original"
                        } else {
                            "recorded-source"
                        }
                        .into(),
                        claimed_spec: evidence,
                    },
                    Some(measured),
                )
                .await?;
            let j = job.clone();
            core.db.write(move|tx|{j.check(tx)?;tx.execute("UPDATE live_fragments SET state=?2,reason='recovered_commit_window' WHERE fragment_id=?1",rusqlite::params![fid,if original{"source"}else{"sealed"}])?;Ok(())}).await?;
        } else {
            let j = job.clone();
            core.db.write(move|tx|{j.check(tx)?;tx.execute("UPDATE live_fragments SET state='interrupted',reason='unsealed_after_restart' WHERE fragment_id=?1",[fid])?;Ok(())}).await?;
        }
    }
    Ok(())
}

/// Finish only durable, already closed captures whose recording worker no longer
/// owns a live lease. This local recovery never restarts capture or revives a job.
/// A cancelled subscription still retains the bytes captured before cancellation.
pub async fn recover_closed(core: &Core) -> Result<usize, CoreError> {
    let _sealing = core.recording_recovery.lock().await;
    let mut after = 0i64;
    let mut recovered = 0;
    loop {
        type Row = (i64, String, String, String, String, String, String, String);
        let rows:Vec<Row>=core.db.read(move|c|Ok(c.prepare("SELECT f.rowid,f.fragment_id,f.session_id,f.media_id,m.operation_id,m.stage,COALESCE(m.received_sha256,''),f.evidence FROM live_fragments f JOIN materializations m ON m.subject_id=f.media_id LEFT JOIN jobs j ON j.job_id=m.job_id WHERE f.rowid>?1 AND f.state='receiving' AND f.end_ms IS NOT NULL AND m.subject_kind='live_fragment' AND m.stage IN ('PREPARED','OBJECT_READY','COMMITTED') AND COALESCE(j.state,'') NOT IN ('leased','running') ORDER BY f.rowid LIMIT 64")?.query_map([after],|r|Ok((r.get(0)?,r.get(1)?,r.get(2)?,r.get(3)?,r.get(4)?,r.get(5)?,r.get(6)?,r.get(7)?)))?.collect::<Result<Vec<_>,_>>()?)).await?;
        if rows.is_empty() {
            break;
        }
        for (row, fid, sid, mid, op, stage, sha, evidence) in rows {
            after = row;
            let opid = OperationId::new(op.clone());
            let mut evidence: Value = serde_json::from_str(&evidence)?;
            let original = evidence["original_capture"] == true;
            if stage != "COMMITTED" {
                // Verify the entire object even for OBJECT_READY. No old "verified"
                // flag is evidence that disk bytes have survived interruption.
                if core
                    .objects
                    .install_object(&core.db, &opid, &sha)
                    .await
                    .is_err()
                {
                    core.objects.quarantine(&core.db, &opid).await?;
                    core.db.write(move|tx|{tx.execute("UPDATE live_fragments SET state='interrupted',reason='recovery_object_invalid' WHERE fragment_id=?1",[fid])?;Ok(())}).await?;
                    continue;
                }
                let measured = if original {
                    json!({"original_capture":true})
                } else {
                    match validate_fragment(core, &sha).await {
                        Ok(spec) => spec,
                        Err(_) => {
                            core.objects.quarantine(&core.db, &opid).await?;
                            core.db.write(move|tx|{tx.execute("UPDATE live_fragments SET state='interrupted',reason='recovery_container_invalid' WHERE fragment_id=?1",[fid])?;Ok(())}).await?;
                            continue;
                        }
                    }
                };
                if !admit_quality(core, &fid, &opid, &evidence, &measured).await? {
                    continue;
                }
                let id = op.clone();
                let adopted=core.db.write(move|tx|Ok(tx.execute("UPDATE materializations SET job_id=NULL WHERE operation_id=?1 AND subject_kind='live_fragment' AND stage='OBJECT_READY' AND NOT EXISTS(SELECT 1 FROM jobs j WHERE j.job_id=materializations.job_id AND j.state IN ('leased','running')) AND EXISTS(SELECT 1 FROM live_fragments f JOIN media_items mi ON mi.media_id=f.media_id WHERE f.media_id=materializations.subject_id AND f.state='receiving' AND f.end_ms IS NOT NULL)",[id])?==1)).await?;
                if !adopted {
                    continue;
                }
                core.objects
                    .commit(
                        &core.db,
                        &opid,
                        &CommitInfo {
                            job: None,
                            media_id: mid,
                            source_representation: if original {
                                "recorded-original"
                            } else {
                                "recorded-source"
                            }
                            .into(),
                            claimed_spec: evidence.clone(),
                        },
                        Some(measured),
                    )
                    .await?;
            }
            evidence["commit_recovery"] = json!("closed_capture_after_lease_release");
            core.db.write(move|tx|{tx.execute("UPDATE live_fragments SET state=?2,evidence=?3 WHERE fragment_id=?1 AND state='receiving'",rusqlite::params![fid,if original{"source"}else{"sealed"},evidence.to_string()])?;crate::objects::outbox_emit(tx,"live.fragment.sealed",&sid,&json!({"fragment_id":fid,"recovered":true}))?;Ok(())}).await?;
            recovered += 1;
        }
    }
    Ok(recovered)
}

async fn get(
    core: &Core,
    rules: &HostRules,
    url: &str,
    stream: &LiveStream,
    range: Option<(u64, u64)>,
    limit: usize,
) -> Result<(Vec<u8>, Url), CoreError> {
    core.http
        .live_resource(rules, url, &stream.public_headers, range, limit)
        .await
}
async fn plaintext(
    core: &Core,
    rules: &HostRules,
    base: &Url,
    stream: &LiveStream,
    bytes: Vec<u8>,
    key: Option<&m3u8_rs::Key>,
    sequence: u64,
) -> Result<Vec<u8>, CoreError> {
    if let Some(k) = key {
        hls::supported_key(k)?;
        if k.method == m3u8_rs::KeyMethod::AES128 {
            let url = base
                .join(
                    k.uri
                        .as_deref()
                        .ok_or_else(|| invalid("HLS key URI missing"))?,
                )
                .map_err(|_| invalid("HLS key URI"))?;
            let (key, _) = get(core, rules, url.as_str(), stream, None, 16).await?;
            if key.len() != 16 {
                return Err(invalid("HLS AES key length"));
            }
            return hls::decrypt(&bytes, &key, &hls::iv(k, sequence)?);
        }
    }
    Ok(bytes)
}

async fn preserve_source(
    core: &Core,
    job: &ClaimedJob,
    source: String,
    bytes: &[u8],
) -> Result<String, CoreError> {
    use sha2::Digest;
    let digest: String = sha2::Sha256::digest(bytes)
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect();
    let source = format!("{source}:sha256:{digest}");
    let Some(mut raw) = begin(core, job, source.clone(), "application/octet-stream", 0).await?
    else {
        let sid = job.subject_id.clone();
        return core.db.read(move|c|Ok(c.query_row("SELECT media_id FROM live_fragments WHERE session_id=?1 AND source_sequence=?2 AND state IN ('sealed','source')",rusqlite::params![sid,source],|r|r.get(0))?)).await;
    };
    let mid = raw.media.clone();
    let m = mid.clone();
    core.db
        .write(move |tx| {
            tx.execute(
                "UPDATE media_items SET role='recording_source',required=0 WHERE media_id=?1",
                [m],
            )?;
            Ok(())
        })
        .await?;
    raw.writer.write_chunk(&core.db, &raw.op, bytes).await?;
    finish(core,job,raw,0,None,json!({"original_capture":true,"byte_count":bytes.len(),"boundary":"exact_resource_response"})).await?;
    Ok(mid)
}
async fn record_hls(core: &Core, job: &ClaimedJob, stream: &LiveStream) -> Result<bool, CoreError> {
    let rules = media_rules(core, job).await?;
    let (bytes, base) = get(core, &rules, &stream.url, stream, None, 512 * 1024).await?;
    let mut tracks = vec![];
    let mut subtitles = vec![];
    let subtitle_base = base.clone();
    match hls::parse(&bytes)? {
        hls::Playlist::MediaPlaylist(p) => tracks.push(("main".to_owned(), base, p)),
        hls::Playlist::MasterPlaylist(m) => {
            let v = m
                .variants
                .iter()
                .filter(|v| !v.is_i_frame)
                .max_by_key(|v| v.bandwidth)
                .ok_or_else(|| invalid("HLS master has no variant"))?;
            let mut urls = vec![("main".to_owned(), v.uri.clone())];
            if let Some(group) = &v.audio {
                let audio = m
                    .alternatives
                    .iter()
                    .filter(|a| {
                        a.media_type == m3u8_rs::AlternativeMediaType::Audio && &a.group_id == group
                    })
                    .max_by_key(|a| a.default)
                    .ok_or_else(|| invalid("HLS required audio missing"))?;
                if let Some(uri) = &audio.uri {
                    urls.push(("audio".into(), uri.clone()));
                }
            }
            if let Some(group) = &v.subtitles {
                subtitles = m
                    .alternatives
                    .iter()
                    .filter(|a| {
                        a.media_type == m3u8_rs::AlternativeMediaType::Subtitles
                            && &a.group_id == group
                    })
                    .cloned()
                    .collect();
            }
            for (role, uri) in urls {
                let uri = base.join(&uri).map_err(|_| invalid("HLS variant URI"))?;
                let (b, u) = get(core, &rules, uri.as_str(), stream, None, 512 * 1024).await?;
                let hls::Playlist::MediaPlaylist(p) = hls::parse(&b)? else {
                    return Err(invalid("nested HLS master depth"));
                };
                tracks.push((role, u, p));
            }
        }
    }
    let mut ended = true;
    for (track, base, p) in tracks {
        ended &= p.end_list;
        let j = job.clone();
        let field = format!("hls_{track}");
        let f = field.clone();
        let prior: Value = core
            .db
            .read(move |c| {
                let raw: String = c.query_row(
                    "SELECT evidence FROM live_sessions WHERE session_id=?1",
                    [j.subject_id],
                    |r| r.get(0),
                )?;
                Ok(serde_json::from_str::<Value>(&raw)?[&f].clone())
            })
            .await?;
        let mut epoch = prior["epoch"].as_u64().unwrap_or(0);
        if prior["first"]
            .as_u64()
            .is_some_and(|n| p.media_sequence < n)
        {
            epoch += 1;
            gap(
                core,
                job,
                format!("reset:{track}:{epoch}"),
                "media_sequence_reset",
            )
            .await?;
        }
        if let Some(last) = prior["last"].as_u64()
            && p.media_sequence > last + 1
        {
            gap(
                core,
                job,
                format!("window:{track}:{epoch}:{}", p.media_sequence),
                "segments_slid_out_of_window",
            )
            .await?;
        }
        let mut last = prior["last"]
            .as_u64()
            .filter(|_| epoch == prior["epoch"].as_u64().unwrap_or(0));
        let segments = hls::segments(&p)?;
        let final_sequence = segments.last().map(|s| s.sequence);
        let sid = job.subject_id.clone();
        let clock_track = track.clone();
        let mut clock:i64=core.db.read(move|c|Ok(c.query_row("SELECT COALESCE(MAX(end_ms),0) FROM live_fragments WHERE session_id=?1 AND state IN ('sealed','gap') AND json_extract(evidence,'$.track')=?2",rusqlite::params![sid,clock_track],|r|r.get(0))?)).await?;
        let mut acquired = 0;
        for seg in segments {
            if state(core, job).await? == "stopping" {
                return Ok(true);
            }
            job.heartbeat(&core.db).await?;
            let source = format!("hls:{track}:{epoch}:{}:{}", seg.discontinuity, seg.sequence);
            if seg.source_gap {
                let sid = job.subject_id.clone();
                let key = source.clone();
                let prior_end:Option<i64>=core.db.read(move|c|{use rusqlite::OptionalExtension;Ok(c.query_row("SELECT end_ms FROM live_fragments WHERE session_id=?1 AND source_sequence=?2 AND state IN ('gap','sealed') AND end_ms IS NOT NULL",rusqlite::params![sid,key],|r|r.get(0)).optional()?)}).await?;
                if let Some(end) = prior_end {
                    clock = end;
                    last = Some(seg.sequence);
                    continue;
                }
                gap(core, job, source.clone(), "source_declared_gap").await?;
                let sid = job.subject_id.clone();
                let track = track.clone();
                let end = clock.saturating_add(seg.duration_ms);
                core.db.write(move|tx|{tx.execute("UPDATE live_fragments SET start_ms=COALESCE(start_ms,?3),end_ms=COALESCE(end_ms,?4),evidence=?5 WHERE session_id=?1 AND source_sequence=?2 AND state='gap'",rusqlite::params![sid,source,clock,end,json!({"track":track,"timeline":"captured_source_duration"}).to_string()])?;Ok(())}).await?;
                clock = end;
                last = Some(seg.sequence);
                continue;
            }
            let sid = job.subject_id.clone();
            let source2 = source.clone();
            let existing_end:Option<i64>=core.db.read(move|c|{use rusqlite::OptionalExtension;Ok(c.query_row("SELECT end_ms FROM live_fragments WHERE session_id=?1 AND source_sequence=?2 AND state='sealed'",rusqlite::params![sid,source2],|r|r.get(0)).optional()?)}).await?;
            if let Some(end) = existing_end {
                clock = end;
                last = Some(seg.sequence);
                continue;
            }
            if acquired >= 64 {
                ended = false;
                break;
            }
            acquired += 1;
            disk_budget(core, RESOURCE_LIMIT)?;
            let u = base
                .join(&seg.uri)
                .map_err(|_| invalid("HLS segment URI"))?;
            let (bytes, _) =
                get(core, &rules, u.as_str(), stream, seg.range, RESOURCE_LIMIT).await?;
            core.objects.budget.observe(
                &job.subject_id,
                bytes.len(),
                Some(seg.duration_ms.max(1) as u64),
            );
            let original = preserve_source(core, job, format!("raw:{source}"), &bytes).await?;
            let bytes = plaintext(
                core,
                &rules,
                &base,
                stream,
                bytes,
                seg.key.as_ref(),
                seg.sequence,
            )
            .await?;
            let mime = if seg.map.is_some() {
                "video/mp4"
            } else {
                "video/mp2t"
            };
            let Some(mut f) = begin(core, job, source, mime, clock).await? else {
                continue;
            };
            let mut source_media = vec![original];
            if let Some(map) = seg.map {
                if seg
                    .map_key
                    .as_ref()
                    .is_some_and(|k| k.method == m3u8_rs::KeyMethod::AES128 && k.iv.is_none())
                {
                    return Err(invalid("encrypted HLS MAP requires explicit IV"));
                }
                let u = base.join(&map.uri).map_err(|_| invalid("HLS MAP URI"))?;
                let range = map
                    .byte_range
                    .map(|r| {
                        r.offset
                            .map(|o| (o, r.length))
                            .ok_or_else(|| invalid("HLS MAP range requires offset"))
                    })
                    .transpose()?;
                let (b, _) = get(core, &rules, u.as_str(), stream, range, RESOURCE_LIMIT).await?;
                source_media.push(
                    preserve_source(
                        core,
                        job,
                        format!(
                            "raw-map:{track}:{epoch}:{}:{}",
                            seg.discontinuity, seg.sequence
                        ),
                        &b,
                    )
                    .await?,
                );
                let b = plaintext(
                    core,
                    &rules,
                    &base,
                    stream,
                    b,
                    seg.map_key.as_ref(),
                    seg.sequence,
                )
                .await?;
                f.writer.write_chunk(&core.db, &f.op, &b).await?;
            }
            f.writer.write_chunk(&core.db, &f.op, &bytes).await?;
            clock = clock.saturating_add(seg.duration_ms);
            finish(core,job,f,clock,None,json!({"protocol":"hls","track":track,"quality":stream.quality,"sequence":seg.sequence,"discontinuity":seg.discontinuity,"source_media_ids":source_media,"transformation":"authorized_decrypt_and_initialization_prefix","timeline":"captured_source_duration","encrypted":seg.key.as_ref().is_some_and(|k|k.method==m3u8_rs::KeyMethod::AES128)})).await?;
            last = Some(seg.sequence);
        }
        ended &= last == final_sequence;
        let j = job.clone();
        let progress = json!({"first":p.media_sequence,"last":last,"epoch":epoch});
        core.db
            .write(move |tx| {
                j.check(tx)?;
                let raw: String = tx.query_row(
                    "SELECT evidence FROM live_sessions WHERE session_id=?1",
                    [&j.subject_id],
                    |r| r.get(0),
                )?;
                let mut v: Value = serde_json::from_str(&raw)?;
                v[field] = progress;
                tx.execute(
                    "UPDATE live_sessions SET evidence=?2 WHERE session_id=?1",
                    rusqlite::params![j.subject_id, v.to_string()],
                )?;
                Ok(())
            })
            .await?;
    }
    subtitles::capture(core, job, stream, &subtitles, &subtitle_base).await?;
    Ok(ended)
}

async fn raw_flv(core: &Core, job: &ClaimedJob, start: i64) -> Result<Fragment, CoreError> {
    let raw = begin(
        core,
        job,
        format!("raw-flv:{}", uuid::Uuid::new_v4()),
        "application/octet-stream",
        start,
    )
    .await?
    .ok_or_else(|| invalid("raw capture identity"))?;
    let mid = raw.media.clone();
    core.db
        .write(move |tx| {
            tx.execute(
                "UPDATE media_items SET role='recording_source',required=0 WHERE media_id=?1",
                [mid],
            )?;
            Ok(())
        })
        .await?;
    Ok(raw)
}
async fn finish_raw_flv(
    core: &Core,
    job: &ClaimedJob,
    raw: Fragment,
    last: u32,
    reason: &str,
    tail: usize,
) -> Result<(), CoreError> {
    let bytes = raw.writer.written_len();
    finish(core,job,raw,last as i64,Some(reason),json!({"protocol":"flv","original_capture":true,"byte_count":bytes,"boundary":"exact_received_bytes","incomplete_tail_bytes":tail})).await
}
async fn finish_flv(
    core: &Core,
    job: &ClaimedJob,
    f: Fragment,
    source_range: (u32, u32),
    reason: &str,
    sources: &[String],
    quality: &str,
) -> Result<(), CoreError> {
    finish(core,job,f,source_range.1 as i64,Some(reason),json!({"protocol":"flv","quality":quality,"source_media_ids":sources,"transformation":"frame_boundary_mux_timestamp_zero","source_start_ms":source_range.0})).await
}
async fn record_flv(core: &Core, job: &ClaimedJob, stream: &LiveStream) -> Result<bool, CoreError> {
    let rules = media_rules(core, job).await?;
    let mut response = core
        .http
        .open_media(&rules, &stream.url, &stream.public_headers, None)
        .await?;
    let mut raw = Some(raw_flv(core, job, 0).await?);
    let mut framer = flv::Framer::default();
    let mut mux = flv_core::Muxer::new();
    let mut configs: Vec<flv_core::Tag> = vec![];
    let mut f: Option<Fragment> = None;
    let mut sources = vec![];
    let mut first = 0u32;
    let mut last = 0u32;
    let mut size = 0usize;
    let mut started = Instant::now();
    let mut raw_started = Instant::now();
    let mut last_chunk = Instant::now();
    let mut interval = tokio::time::interval(Duration::from_millis(200));
    let mut reason = "stream_disconnected";
    let mut ended = false;
    let seconds = core.profile.recording_fragment_seconds.clamp(1, 86400);
    let byte_limit = core
        .profile
        .recording_fragment_bytes
        .clamp(65536, 2 * 1024 * 1024 * 1024);
    // Every exit, including write failures and revoked leases, reaches finalization.
    let capture:Result<(),CoreError>=async {
        'capture: loop {
            let chunk = tokio::select! {
                _=interval.tick()=>{
                    match state(core,job).await {Ok(s) if s=="stopping"=>{reason="requested_stop";ended=true;break;},Err(_)=>{reason="lifecycle_interrupted";break;},_=>{}}
                    if let Err(e)=job.heartbeat(&core.db).await {reason="lifecycle_interrupted";return Err(e);}
                    if last_chunk.elapsed()>Duration::from_secs(65){reason="idle_or_sleep_gap";break;}continue;
                },
                value=response.chunk()=>match value{Ok(Some(b))=>{core.http.received_media(b.len())?;b},Ok(None)=>break,Err(_)=>return Err(CoreError::Network("recording transport interrupted".into()))},
            };
            if last_chunk.elapsed()>Duration::from_secs(65){reason="idle_or_sleep_gap";break;}
            last_chunk=Instant::now();
            core.objects.budget.observe(&job.subject_id,chunk.len(),None);
            if disk_budget(core,chunk.len()).is_err(){reason="disk_budget_exhausted";return Err(CoreError::StorageFull("recording reserve reached".into()));}
            if raw.is_none(){raw=Some(raw_flv(core,job,last as i64).await?);raw_started=Instant::now();}
            let source=raw.as_ref().unwrap().media.clone();
            if !sources.contains(&source){sources.push(source);}
            {let raw=raw.as_mut().unwrap();raw.writer.write_chunk(&core.db,&raw.op,&chunk).await?;}
            for slice in chunk.chunks(32768) {
                let tags=match framer.push(slice){Ok(tags)=>tags,Err(_)=>{reason="invalid_flv_frame";break 'capture;}};
                for tag in tags {
                    let timed=matches!(tag.tag_type,flv_core::TagType::Audio|flv_core::TagType::Video) && !flv::is_config(&tag);
                    let clock_reset=timed && tag.timestamp_ms.saturating_add(2000)<last;
                    let clock_gap=timed && f.is_some() && tag.timestamp_ms>last.saturating_add(65000);
                    let changed=flv::is_config(&tag) && configs.iter().any(|t|t.tag_type==tag.tag_type && t.data!=tag.data);
                    let boundary=flv::is_boundary(&tag,framer.has_video());
                    let rotate=(started.elapsed()>=Duration::from_secs(seconds) || u64::from(tag.timestamp_ms.saturating_sub(first))>=seconds*1000 || size as u64>=byte_limit) && boundary;
                    if clock_reset || clock_gap || changed || rotate {
                        let why=if clock_reset{"source_clock_reset"}else if clock_gap{"source_timestamp_gap"}else if changed{"codec_configuration_changed"}else{"rotation"};
                        if let Some(part)=f.take(){finish_flv(core,job,part,(first,last),why,&sources,&stream.quality).await?;}
                        if clock_reset||clock_gap||changed {gap(core,job,format!("flv-gap:{}",uuid::Uuid::new_v4()),why).await?;}
                        sources=vec![raw.as_ref().unwrap().media.clone()];size=0;started=Instant::now();
                        if clock_reset{last=tag.timestamp_ms;}
                    }
                    if flv::is_config(&tag){configs.retain(|t|t.tag_type!=tag.tag_type);configs.push(tag.clone());}
                    if f.is_none(){
                        if !boundary {continue;}
                        f=begin(core,job,format!("flv:{}",uuid::Uuid::new_v4()),"video/x-flv",tag.timestamp_ms as i64).await?;
                        first=tag.timestamp_ms;started=Instant::now();size=0;
                        let mut header=vec![];mux.write_header(framer.has_audio(),framer.has_video(),&mut header);
                        for c in &configs {let mut c=c.clone();c.timestamp_ms=0;mux.write_tag(&c,&mut header).map_err(|_|invalid("FLV configuration"))?;}
                        if let Some(f)=&mut f{f.writer.write_chunk(&core.db,&f.op,&header).await?;size+=header.len();}
                    }
                    if disk_budget(core,tag.data.len()).is_err(){reason="disk_budget_exhausted";return Err(CoreError::StorageFull("recording reserve reached".into()));}
                    let mut bytes=vec![];let mut normalized=tag.clone();normalized.timestamp_ms=tag.timestamp_ms.saturating_sub(first);
                    mux.write_tag(&normalized,&mut bytes).map_err(|_|invalid("FLV mux frame"))?;
                    if let Some(f)=&mut f{f.writer.write_chunk(&core.db,&f.op,&bytes).await?;size+=bytes.len();}
                    if timed {last=tag.timestamp_ms;}
                }
            }
            if size as u64>byte_limit+8*1024*1024 {reason="no_rotation_boundary";break;}
            if raw.as_ref().is_some_and(|r|r.writer.written_len()>=byte_limit) || raw_started.elapsed()>=Duration::from_secs(seconds){
                finish_raw_flv(core,job,raw.take().unwrap(),last,"rotation",framer.tail_len()).await?;
            }
        }
        Ok(())
    }.await;
    if capture.is_err() && reason == "stream_disconnected" {
        reason = "stream_write_failed";
    }
    // Finalize both independently: a failure of raw reference commit must not drop
    // the already buffered playable prefix, and vice versa.
    let raw_result = if let Some(raw) = raw {
        finish_raw_flv(core, job, raw, last, reason, framer.tail_len()).await
    } else {
        Ok(())
    };
    let media_result = if let Some(f) = f {
        finish_flv(
            core,
            job,
            f,
            (first, last),
            reason,
            &sources,
            &stream.quality,
        )
        .await
    } else {
        Ok(())
    };
    let gap_result = if !ended {
        gap(
            core,
            job,
            format!("flv-gap:{}", uuid::Uuid::new_v4()),
            reason,
        )
        .await
    } else {
        Ok(())
    };
    capture?;
    raw_result?;
    media_result?;
    gap_result?;
    Ok(ended)
}
