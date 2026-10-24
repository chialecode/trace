//! Persistent recording sessions. Adapters resolve identities and short-lived
//! plans; this module alone receives bytes and commits them through ObjectStore.
//!
//! - `live_session`: registering a detection as a durable session.
//! - `capture_hls` / `capture_flv`: receiving a stream; `subtitles` and `auxiliary` add
//!   optional tracks.
//! - `fragment`: a fragment's reservation, quality admission and seal; `recovery` finishes
//!   what a crash left half-sealed.
pub mod auxiliary;
mod capture_flv;
mod capture_hls;
pub mod flv;
mod fragment;
pub mod hls;
mod hls_source;
mod live_session;
mod recovery;
mod subtitles;
#[cfg(test)]
pub(crate) mod tests;

use crate::{
    core::Core,
    error::CoreError,
    jobs::{self, ClaimedJob},
    pipeline::observe::with_context,
    transport::{HostRules, host_rules},
};
use serde_json::json;
use std::{sync::atomic::Ordering, time::Duration};
use trace_contracts::acquisition::{AcquisitionContext, AuthenticationState};
use trace_contracts::plugin::{LiveSnapshot, LiveStream};

use fragment::gap;
pub use live_session::register;
pub use recovery::recover_closed;
use recovery::recover_pending;
#[cfg(test)]
use {
    crate::objects::CommitInfo,
    fragment::{begin, validate_fragment},
};

const RESOURCE_LIMIT: usize = 64 * 1024 * 1024;
const PROBE_DEADLINE_MS: u64 = 30_000;
/// A poll that found nothing to record waits this long before the same session is retried.
const RETRY_SECONDS: u64 = 2;
const STATE_POLL: Duration = Duration::from_millis(200);

fn invalid(message: &str) -> CoreError {
    CoreError::InvalidInput(message.into())
}

async fn reschedule(core: &Core, job: &ClaimedJob, seconds: u64) -> Result<(), CoreError> {
    let j = job.clone();
    let next = (time::OffsetDateTime::now_utc() + time::Duration::seconds(seconds as i64))
        .format(&time::format_description::well_known::Rfc3339)
        .map_err(|_| invalid("clock"))?;
    core.db
        .write(move |tx| {
            j.check(tx)?;
            tx.execute(
                "UPDATE jobs SET state='queued',lease_owner=NULL,lease_expires_at=NULL,next_run_at=?2 WHERE job_id=?1",
                rusqlite::params![j.job_id, next],
            )?;
            Ok(())
        })
        .await
}

/// Checks whether the account is live and starts or continues its session.
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
            json!({"native_account_key": account}),
            Some(PROBE_DEADLINE_MS),
        )
        .await?;
    let snapshot: LiveSnapshot = serde_json::from_value(value)?;
    if let Some(session) = register(core, job, snapshot).await? {
        auxiliary::schedule(core, job, &session).await?;
    }
    let subscription = job.payload["subscription_id"]
        .as_str()
        .ok_or_else(|| invalid("live subscription"))?
        .to_owned();
    let interval = core
        .db
        .read(move |c| {
            Ok(c.query_row(
                "SELECT check_interval_seconds FROM subscriptions WHERE subscription_id=?1",
                [subscription],
                |r| r.get::<_, i64>(0),
            )?)
        })
        .await?;
    let seconds = crate::config::jittered_check_seconds(interval) as u64;
    reschedule(core, job, seconds).await
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

/// The account a recording session belongs to and where its entry lives.
struct SessionTarget {
    account: String,
    namespace: String,
    entry: String,
}

async fn session_target(core: &Core, job: &ClaimedJob) -> Result<SessionTarget, CoreError> {
    let session = job.subject_id.clone();
    core.db
        .read(move |c| {
            Ok(c.query_row(
                "SELECT a.native_account_key,a.namespace,l.entry_id FROM live_sessions l JOIN subscriptions s ON s.subscription_id=l.subscription_id JOIN source_accounts a ON a.account_id=s.account_id WHERE l.session_id=?1",
                [session],
                |r| {
                    Ok(SessionTarget {
                        account: r.get(0)?,
                        namespace: r.get(1)?,
                        entry: r.get(2)?,
                    })
                },
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
    let target = session_target(core, job).await?;
    if matches!(state(core, job).await?.as_str(), "stopping" | "sealed") {
        return seal_session(core, job).await;
    }
    let platform = target.namespace.split(':').next().unwrap_or("");
    let context = crate::acquisitions::begin(core, job, platform, &target.entry).await?;
    let job = &with_context(job, &context.context_id);
    if let Err(error) = crate::acquisitions::require_access(&context) {
        return refuse_access(core, job, &context, error).await;
    }
    let snapshot = detect(core, job, platform, &target.account).await?;
    let context = crate::acquisitions::resolution_context(
        core,
        job,
        &target.entry,
        context,
        &snapshot.metadata,
    )
    .await?;
    let job = &with_context(job, &context.context_id);
    if snapshot.state == "offline" {
        return seal_session(core, job).await;
    }
    if snapshot.state != "live" {
        return Err(CoreError::Network("live status unknown".into()));
    }
    let registered = register(core, job, snapshot.clone()).await?;
    if registered.as_deref() != Some(job.subject_id.as_str()) {
        return seal_session(core, job).await;
    }
    let stream = best_stream(&snapshot)?;
    mark_recording(core, job).await?;
    let received = receive(core, job, stream).await;
    conclude(core, job, received).await
}

/// Without proof of access the session records an authentication gap instead of bytes.
async fn refuse_access(
    core: &Core,
    job: &ClaimedJob,
    context: &AcquisitionContext,
    error: CoreError,
) -> Result<(), CoreError> {
    let reason = if context.auth_state == AuthenticationState::Expired {
        "authentication_expired"
    } else {
        "authentication_unknown"
    };
    gap(
        core,
        job,
        format!("authentication:{}", context.context_id),
        reason,
    )
    .await?;
    Err(error)
}

/// What the source says about the room: from the user's request for a direct source,
/// else from the adapter.
async fn detect(
    core: &Core,
    job: &ClaimedJob,
    platform: &str,
    account: &str,
) -> Result<LiveSnapshot, CoreError> {
    if job.payload["source_request_id"].is_string() {
        return crate::source_acquisition::resolve_live(core, job).await;
    }
    let value = core
        .adapter_call(
            job,
            platform,
            "probe_live",
            json!({"native_account_key": account}),
            Some(PROBE_DEADLINE_MS),
        )
        .await?;
    Ok(serde_json::from_value(value)?)
}

/// HLS is preferred over FLV; the higher quality wins within a protocol.
fn best_stream(snapshot: &LiveSnapshot) -> Result<&LiveStream, CoreError> {
    snapshot
        .streams
        .iter()
        .filter(|s| matches!(s.protocol.as_str(), "flv" | "hls"))
        .max_by_key(|s| (u8::from(s.protocol == "hls"), quality_rank(&s.quality)))
        .ok_or_else(|| CoreError::Unsupported("no recording stream".into()))
}

async fn mark_recording(core: &Core, job: &ClaimedJob) -> Result<(), CoreError> {
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
        .await
}

/// A finished stream seals the session, a pause in the stream retries it shortly, and a
/// failure leaves it reconnecting with the failure class as the reason.
async fn conclude(
    core: &Core,
    job: &ClaimedJob,
    received: Result<bool, CoreError>,
) -> Result<(), CoreError> {
    match received {
        Ok(true) => seal_session(core, job).await,
        Ok(false) => reschedule(core, job, RETRY_SECONDS).await,
        Err(e) => {
            let j = job.clone();
            let reason = format!("{:?}", e.failure_class());
            let _ = core
                .db
                .write(move |tx| {
                    j.check(tx)?;
                    tx.execute(
                        "UPDATE live_sessions SET state='reconnecting',stop_reason=?2 WHERE session_id=?1",
                        rusqlite::params![j.subject_id, reason],
                    )?;
                    Ok(())
                })
                .await;
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
    let job = &ensure_context(core, job).await?;
    let _reservation = core.objects.budget.recording(&job.subject_id);
    recover_pending(core, job).await?;
    match stream.protocol.as_str() {
        "hls" => receive_hls(core, job, stream).await,
        "flv" => capture_flv::record_flv(core, job, stream).await,
        _ => Err(CoreError::Unsupported("recording protocol".into())),
    }
}

/// An HLS capture stops at the next check once the session is stopping.
async fn receive_hls(
    core: &Core,
    job: &ClaimedJob,
    stream: &LiveStream,
) -> Result<bool, CoreError> {
    let work = capture_hls::record_hls(core, job, stream);
    tokio::pin!(work);
    let mut tick = tokio::time::interval(STATE_POLL);
    loop {
        tokio::select! {
            result = &mut work => break result,
            _ = tick.tick() => {
                if state(core, job).await? == "stopping" {
                    break Ok(true);
                }
            }
        }
    }
}

/// A job started outside `run` has no resolution proof; its context says so.
async fn ensure_context(core: &Core, job: &ClaimedJob) -> Result<ClaimedJob, CoreError> {
    if !job.payload["acquisition_context_id"].is_null() {
        return Ok(job.clone());
    }
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
    let context = AcquisitionContext {
        context_id: format!("ctx-{}", uuid::Uuid::new_v4()),
        auth_state: AuthenticationState::Unknown,
        credential_id: None,
        credential_generation: None,
        validated_at: None,
        basis: "direct_transport_without_resolution_proof".into(),
        started_at: jobs::now(),
    };
    let (saved, guard) = (context.clone(), job.clone());
    core.db
        .write(move |tx| {
            guard.check(tx)?;
            crate::acquisitions::insert_context(tx, &guard, &entry, &saved)
        })
        .await?;
    Ok(with_context(job, &context.context_id))
}

async fn media_rules(core: &Core, job: &ClaimedJob) -> Result<HostRules, CoreError> {
    let session = job.subject_id.clone();
    let site: String = core
        .db
        .read(move |c| {
            Ok(c.query_row(
                "SELECT e.namespace FROM live_sessions l JOIN entries e ON e.entry_id=l.entry_id WHERE l.session_id=?1",
                [session],
                |r| r.get(0),
            )?)
        })
        .await?;
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
    core.db
        .write(move |tx| {
            j.check(tx)?;
            tx.execute(
                "UPDATE live_sessions SET state='sealed',ended_at=COALESCE(ended_at,?2) WHERE session_id=?1",
                rusqlite::params![j.subject_id, jobs::now()],
            )?;
            crate::objects::outbox_emit(
                tx,
                "live.updated",
                &j.subject_id,
                &json!({"state": "sealed"}),
            )?;
            Ok(())
        })
        .await
}
