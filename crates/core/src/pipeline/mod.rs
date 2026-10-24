//! The job pipeline: how every job type runs and how its outcome is recorded (C26/C29).
//!
//! A job runs under supervision: the lease is renewed while it works, a pause or shutdown
//! puts it back in the queue, and a failure is stored as a class, never as an upstream body.

pub(crate) mod acquire;
pub(crate) mod media;
pub(crate) mod mux;
pub(crate) mod observe;
pub(crate) mod scan;
pub(crate) mod scan_page;
pub(crate) mod standing;

use crate::core::Core;
use crate::error::CoreError;
use crate::jobs::ClaimedJob;
use std::collections::HashMap;
use std::future::Future;
use std::path::Path;
use std::sync::atomic::Ordering;
use std::sync::{Arc, Mutex};
use std::time::Duration;
use trace_contracts::control::{BackupStartArgs, ExportStartArgs};
use trace_domain::jobs::JobType;

const HEARTBEAT_INTERVAL: Duration = Duration::from_millis(200);

/// A revoked database lease does not mean the worker has finished sealing files, so a
/// runtime count stays until the job's whole future has been dropped.
struct Active<'a> {
    counts: &'a Mutex<HashMap<String, usize>>,
    job_id: String,
}

impl<'a> Active<'a> {
    fn enter(counts: &'a Mutex<HashMap<String, usize>>, job_id: &str) -> Self {
        *counts.lock().unwrap().entry(job_id.to_owned()).or_default() += 1;
        Self {
            counts,
            job_id: job_id.to_owned(),
        }
    }
}

impl Drop for Active<'_> {
    fn drop(&mut self) {
        let mut jobs = self.counts.lock().unwrap();
        if let Some(count) = jobs.get_mut(&self.job_id) {
            *count -= 1;
            if *count == 0 {
                jobs.remove(&self.job_id);
            }
        }
    }
}

/// Runs one claimed job to its recorded outcome.
pub(crate) async fn execute(core: Arc<Core>, job: ClaimedJob) {
    let _active = Active::enter(&core.active_jobs, &job.job_id);
    if job.mark_running(&core.db).await.is_err() {
        return;
    }
    let is_recording = job.job_type == JobType::Record.as_str();
    let Some(result) = supervise(&core, &job, run(&core, &job), is_recording).await else {
        return;
    };
    if core.cancel.is_cancelled() || core.paused.load(Ordering::SeqCst) {
        let _ = job.requeue(&core.db).await;
    } else {
        record_outcome(&core, &job, result).await;
    }
    if is_recording {
        let _ = crate::recording::recover_closed(&core).await;
    }
}

/// Dispatches the job to the code of its type.
async fn run(core: &Arc<Core>, job: &ClaimedJob) -> Result<(), CoreError> {
    match JobType::parse(&job.job_type) {
        Some(JobType::SourceAcquire) => crate::source_acquisition::run(core, job).await,
        Some(JobType::AuthCompletion) => crate::upgrades::run(core, job).await,
        Some(JobType::CredentialProbe) => crate::app::credential_probe::run_job(core, job).await,
        Some(JobType::Scan) => scan::run_scan(core, job).await,
        Some(JobType::Observe) => observe::run_observe(core, job).await,
        Some(JobType::Media) => media::run_media(core, job).await,
        Some(JobType::Record) => crate::recording::run(core, job).await,
        Some(JobType::QualityMaintenance) => crate::quality::maintain(core, job).await,
        Some(JobType::AccountProfile) => crate::accounts::refresh_job(core, job).await,
        Some(JobType::LiveAuxiliary) => crate::recording::auxiliary::run(core, job).await,
        Some(JobType::Backup) => run_backup(core, job).await,
        Some(JobType::Export) => run_export(core, job).await,
        Some(JobType::Delivery) => crate::downloads::delivery::run(core, job).await,
        Some(JobType::Import) => crate::imports::run(core, job).await,
        None => Err(CoreError::InvalidInput("unknown job type".into())),
    }
}

async fn run_backup(core: &Arc<Core>, job: &ClaimedJob) -> Result<(), CoreError> {
    let a: BackupStartArgs = serde_json::from_value(job.payload.clone())?;
    let root = Path::new(&a.target_root);
    crate::storage_ops::backup_guarded(core, root, a.include_media, Some(job.clone()))
        .await
        .map(|_| ())
}

async fn run_export(core: &Arc<Core>, job: &ClaimedJob) -> Result<(), CoreError> {
    let a: ExportStartArgs = serde_json::from_value(job.payload.clone())?;
    crate::archive_export::export_guarded(
        core,
        Path::new(&a.target_dir),
        a.include_media,
        a.creator_id.as_deref(),
        Some(job.clone()),
    )
    .await
    .map(|_| ())
}

/// Waits for the work while renewing the lease. `None` means the job was handed back to
/// the queue (shutdown, pause, or a lost lease); recordings run to the end regardless.
async fn supervise(
    core: &Arc<Core>,
    job: &ClaimedJob,
    work: impl Future<Output = Result<(), CoreError>>,
    uninterruptible: bool,
) -> Option<Result<(), CoreError>> {
    if uninterruptible {
        return Some(work.await);
    }
    let monitor = async {
        let mut tick = tokio::time::interval(HEARTBEAT_INTERVAL);
        loop {
            tick.tick().await;
            if core.paused.load(Ordering::SeqCst) {
                return true;
            }
            if job.heartbeat(&core.db).await.is_err() {
                return false;
            }
        }
    };
    tokio::pin!(work);
    tokio::pin!(monitor);
    tokio::select! {
        result = &mut work => Some(result),
        _ = core.cancel.cancelled() => {
            let _ = job.requeue(&core.db).await;
            None
        },
        requeue = &mut monitor => {
            if requeue {
                let _ = job.requeue(&core.db).await;
            }
            None
        }
    }
}

async fn record_outcome(core: &Arc<Core>, job: &ClaimedJob, result: Result<(), CoreError>) {
    match result {
        Ok(()) => {
            let _ = job.succeed(&core.db).await;
        }
        // Administrative pauses are not failed attempts. Claiming also checks subscription state.
        Err(CoreError::Paused(_)) => {
            let _ = job.requeue(&core.db).await;
        }
        Err(e) => record_failure(core, job, e).await,
    }
}

/// Persists a class, never an upstream error body (credentials/URLs can occur anywhere).
async fn record_failure(core: &Arc<Core>, job: &ClaimedJob, e: CoreError) {
    let class = e.failure_class();
    let message = if matches!(e, CoreError::VersionConflict(_))
        && job.payload["source_request_id"].is_string()
    {
        "source_selection_changed".to_owned()
    } else {
        format!("{:?}", class)
    };
    if job.fail(&core.db, &class, &message, None).await.is_ok() {
        core.hub
            .publish_progress(&job.job_id, 0.0, Some(message), &job.job_id);
    }
}
