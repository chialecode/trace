//! Append-only acquisition provenance, independent of blob deduplication and
//! the credential's current status. Authentication is proved for this batch.
use crate::{
    core::Core,
    error::CoreError,
    jobs::{self, ClaimedJob},
};
use rusqlite::{Connection, OptionalExtension, Transaction};
use serde_json::{Value, json};
use trace_contracts::acquisition::{AcquisitionContext, AuthenticationState as Auth};

pub async fn begin(
    core: &Core,
    job: &ClaimedJob,
    platform: &str,
    entry: &str,
) -> Result<AcquisitionContext, CoreError> {
    let guard = job.clone();
    let p = platform.to_owned();
    let credential = core
        .db
        .read(move |c| jobs::scope::credential(c, &guard, &p))
        .await;
    let mut context = AcquisitionContext {
        context_id: format!("ctx-{}", uuid::Uuid::new_v4()),
        auth_state: Auth::Anonymous,
        credential_id: None,
        credential_generation: None,
        validated_at: None,
        basis: "explicit_anonymous_scope".into(),
        started_at: jobs::now(),
    };
    match credential {
        Ok(Some((id, generation, reference, status))) => {
            context.credential_id = Some(id.clone());
            context.credential_generation = Some(generation);
            let scope = format!("{id}/g{generation}");
            let result = if matches!(status.as_str(), "valid" | "authenticated") {
                match core.vault.load(&reference).and_then(|bytes| {
                    String::from_utf8(bytes)
                        .map_err(|_| CoreError::AuthRequired("credential encoding".into()))
                }) {
                    Ok(secret) => {
                        core.cache_cookie(&format!("{platform}/{scope}"), secret);
                        core.metadata_pacing(platform).await;
                        core.host
                            .call_scoped(
                                platform,
                                Some(&scope),
                                "probe_auth",
                                json!({}),
                                Some(20000),
                            )
                            .await
                    }
                    Err(e) => Err(e),
                }
            } else if status == "invalid" {
                Err(CoreError::AuthRequired("credential expired".into()))
            } else {
                Err(CoreError::Network("credential validation unknown".into()))
            };
            context.auth_state = match &result {
                Ok(v) if matches!(v["state"].as_str(), Some("valid" | "authenticated")) => {
                    Auth::Authenticated
                }
                Ok(v) if matches!(v["state"].as_str(), Some("invalid" | "anonymous")) => {
                    Auth::Expired
                }
                Err(e) if e.failure_class() == trace_domain::errors::FailureClass::AuthRequired => {
                    Auth::Expired
                }
                _ => Auth::Unknown,
            };
            context.validated_at = Some(jobs::now());
            context.basis = "same_generation_batch_probe".into();
        }
        Ok(None) => {}
        Err(CoreError::AuthRequired(_)) => {
            context.auth_state = Auth::Expired;
            context.basis = "credential_unavailable".into();
            context.credential_id = job.payload["execution_scope"]["credential_id"]
                .as_str()
                .map(str::to_owned);
            context.credential_generation =
                job.payload["execution_scope"]["credential_generation"].as_i64();
        }
        Err(e) => return Err(e),
    }
    let saved = context.clone();
    let entry = entry.to_owned();
    let guard = job.clone();
    core.db
        .write(move |tx| {
            guard.check(tx)?;
            insert_context(tx, &guard, &entry, &saved)?;
            if matches!(saved.auth_state, Auth::Expired | Auth::Unknown) {
                record(
                    tx,
                    &saved.context_id,
                    None,
                    None,
                    "authentication_unavailable",
                    &Value::Null,
                )?;
            }
            Ok(())
        })
        .await?;
    Ok(context)
}

pub fn require_access(context: &AcquisitionContext) -> Result<(), CoreError> {
    match context.auth_state {
        Auth::Anonymous | Auth::Authenticated => Ok(()),
        Auth::Expired => Err(CoreError::AuthRequired(
            "acquisition credential expired".into(),
        )),
        Auth::Unknown => Err(CoreError::Network(
            "acquisition authentication unknown".into(),
        )),
    }
}
pub fn insert_context(
    tx: &Transaction<'_>,
    job: &ClaimedJob,
    entry: &str,
    c: &AcquisitionContext,
) -> Result<(), CoreError> {
    tx.execute("INSERT INTO acquisition_contexts(context_id,entry_id,job_id,job_generation,subscription_id,credential_id,credential_generation,auth_state,validated_at,basis,started_at) VALUES(?1,?2,?3,?4,?5,?6,?7,?8,?9,?10,?11)",rusqlite::params![c.context_id,entry,job.job_id,job.generation,job.payload["subscription_id"].as_str(),c.credential_id,c.credential_generation,c.auth_state.as_str(),c.validated_at,c.basis,c.started_at])?;
    Ok(())
}

/// An adapter can explicitly report anonymous resolution after an authenticated
/// attempt. It creates a separate context; it never mutates the original proof.
pub async fn resolution_context(
    core: &Core,
    job: &ClaimedJob,
    entry: &str,
    original: AcquisitionContext,
    notes: &Value,
) -> Result<AcquisitionContext, CoreError> {
    if notes["authentication"] != "anonymous" || original.auth_state == Auth::Anonymous {
        return Ok(original);
    }
    let context = AcquisitionContext {
        context_id: format!("ctx-{}", uuid::Uuid::new_v4()),
        auth_state: Auth::Anonymous,
        credential_id: None,
        credential_generation: None,
        validated_at: None,
        basis: "explicit_anonymous_resolution".into(),
        started_at: jobs::now(),
    };
    let saved = context.clone();
    let guard = job.clone();
    let entry = entry.to_owned();
    core.db
        .write(move |tx| {
            guard.check(tx)?;
            insert_context(tx, &guard, &entry, &saved)
        })
        .await?;
    Ok(context)
}

pub fn record(
    tx: &Transaction<'_>,
    context: &str,
    media: Option<&str>,
    variant: Option<&str>,
    outcome: &str,
    measured: &Value,
) -> Result<(), CoreError> {
    tx.execute("INSERT OR IGNORE INTO resource_acquisitions(acquisition_id,context_id,media_id,variant_id,outcome,finished_at,measured_spec) VALUES(?1,?2,?3,?4,?5,?6,?7)",rusqlite::params![format!("acq-{}",uuid::Uuid::new_v4()),context,media,variant,outcome,jobs::now(),measured.to_string()])?;
    Ok(())
}

/// Preserve the end of a failed attempt without copying upstream error text.
/// Control revocations never call this path and therefore do not spend retries.
pub fn failed(tx: &Transaction<'_>, job: &ClaimedJob, outcome: &str) -> Result<(), CoreError> {
    let context = tx.query_row(
        "SELECT context_id FROM acquisition_contexts WHERE job_id=?1 AND job_generation=?2 ORDER BY rowid DESC LIMIT 1",
        rusqlite::params![job.job_id, job.generation], |r|r.get::<_,String>(0),
    ).optional()?;
    if let Some(context) = context {
        record(tx, &context, None, None, outcome, &Value::Null)?;
    }
    Ok(())
}

pub fn commit(
    tx: &Transaction<'_>,
    job: Option<&ClaimedJob>,
    media: &str,
    variant: &str,
    measured: &Value,
) -> Result<(), CoreError> {
    let context = job
        .and_then(|j| j.payload["acquisition_context_id"].as_str())
        .map(str::to_owned)
        .or(tx
            .query_row(
                "SELECT context_id FROM live_fragments WHERE media_id=?1",
                [media],
                |r| r.get::<_, Option<String>>(0),
            )
            .optional()?
            .flatten());
    if let Some(context) = context {
        record(
            tx,
            &context,
            Some(media),
            Some(variant),
            "acquired",
            measured,
        )?;
    }
    Ok(())
}

pub fn summary(c: &Connection, entry: &str) -> Result<Value, CoreError> {
    let states=c.prepare_cached("SELECT DISTINCT auth_state FROM acquisition_contexts WHERE entry_id=?1 ORDER BY auth_state")?.query_map([entry],|r|r.get::<_,String>(0))?.collect::<Result<Vec<_>,_>>()?;
    let label = match states.len() {
        0 => "unknown",
        1 => &states[0],
        _ => "mixed",
    };
    Ok(json!({"state":label,"states":if states.is_empty(){vec!["unknown".to_owned()]}else{states}}))
}

pub fn fragment(c: &Connection, session: &str, sequence: i64) -> Result<Value, CoreError> {
    Ok(c.query_row("SELECT COALESCE(x.auth_state,'unknown'),v.measured_spec FROM live_fragments f LEFT JOIN acquisition_contexts x ON x.context_id=f.context_id LEFT JOIN asset_selections s ON s.media_id=f.media_id LEFT JOIN media_variants v ON v.variant_id=s.variant_id WHERE f.session_id=?1 AND f.sequence=?2",rusqlite::params![session,sequence],|r|Ok(json!({"authentication":r.get::<_,String>(0)?,"actual_spec":r.get::<_,Option<String>>(1)?.and_then(|v|serde_json::from_str::<Value>(&v).ok())})))?)
}

pub fn variant_summary(c: &Connection, variant: Option<&str>) -> rusqlite::Result<Value> {
    let states=c.prepare("SELECT DISTINCT x.auth_state FROM resource_acquisitions a JOIN acquisition_contexts x ON x.context_id=a.context_id WHERE a.variant_id=?1 ORDER BY x.auth_state")?.query_map([variant],|r|r.get::<_,String>(0))?.collect::<Result<Vec<_>,_>>()?;
    let label = match states.len() {
        0 => "unknown",
        1 => &states[0],
        _ => "mixed",
    };
    Ok(json!({"state":label,"states":if states.is_empty(){vec!["unknown".to_owned()]}else{states}}))
}

pub async fn list(core: &Core, args: Value) -> Result<Value, CoreError> {
    let entry = args["entry_id"]
        .as_str()
        .ok_or_else(|| CoreError::InvalidInput("entry identity".into()))?
        .to_owned();
    let before = args["before"].as_i64().unwrap_or(i64::MAX);
    core.db.read(move|c|{
        let mut rows=c.prepare("SELECT r.sequence,r.acquisition_id,x.context_id,x.auth_state,x.validated_at,x.basis,x.started_at,r.finished_at,r.media_id,r.variant_id,r.outcome,r.measured_spec,x.credential_id,x.credential_generation,EXISTS(SELECT 1 FROM credentials_state k WHERE k.credential_id=x.credential_id AND k.generation=x.credential_generation AND k.status IN ('valid','authenticated')) FROM resource_acquisitions r JOIN acquisition_contexts x ON x.context_id=r.context_id WHERE x.entry_id=?1 AND r.sequence<?2 ORDER BY r.sequence DESC LIMIT 51")?.query_map(rusqlite::params![entry,before],|r|Ok(json!({"sequence":r.get::<_,i64>(0)?,"acquisition_id":r.get::<_,String>(1)?,"context_id":r.get::<_,String>(2)?,"auth_state":r.get::<_,String>(3)?,"validated_at":r.get::<_,Option<String>>(4)?,"basis":r.get::<_,String>(5)?,"started_at":r.get::<_,String>(6)?,"finished_at":r.get::<_,String>(7)?,"media_id":r.get::<_,Option<String>>(8)?,"variant_id":r.get::<_,Option<String>>(9)?,"outcome":r.get::<_,String>(10)?,"measured_spec":serde_json::from_str::<Value>(&r.get::<_,String>(11)?).unwrap_or_default(),"credential_id":r.get::<_,Option<String>>(12)?,"credential_generation":r.get::<_,Option<i64>>(13)?,"current_credential_available":r.get::<_,bool>(14)?})))?.collect::<Result<Vec<_>,_>>()?;
        let more=rows.len()>50;rows.truncate(50);let next=more.then(||rows.last().unwrap()["sequence"].clone());
        Ok(json!({"items":rows,"next_before":next,"summary":summary(c,&entry)?}))
    }).await
}
