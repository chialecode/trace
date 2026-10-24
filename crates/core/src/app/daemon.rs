//! Daemon status, pause and shutdown.
use super::unsupported;
use crate::core::Core;
use crate::error::CoreError;
use crate::{jobs, store};
use serde_json::{Value, json};
use std::sync::Arc;
use std::sync::atomic::Ordering;
use trace_contracts::control::Request;
use trace_contracts::control::commands as cmd;

pub async fn route(core: &Arc<Core>, req: &Request) -> Result<Value, CoreError> {
    match req.command.as_str() {
        cmd::DAEMON_STATUS => status(core).await,
        cmd::DAEMON_PAUSE => pause(core, &req.args).await,
        cmd::DAEMON_SHUTDOWN => Ok(shutdown(core)),
        other => Err(unsupported(other)),
    }
}

/// Pausing puts running work back in the queue; it resumes under a new generation.
async fn pause(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let paused = args.get("paused").and_then(Value::as_bool).unwrap_or(false);
    let was_paused = core.paused.swap(paused, Ordering::SeqCst);
    if paused && !was_paused {
        core.db
            .write(|tx| store::jobs::requeue_running(tx, &jobs::now()))
            .await?;
    }
    status(core).await
}

fn shutdown(core: &Arc<Core>) -> Value {
    let core = core.clone();
    tokio::spawn(async move {
        tokio::time::sleep(std::time::Duration::from_millis(150)).await;
        core.shutdown().await;
    });
    json!({"accepted": true})
}

async fn status(core: &Arc<Core>) -> Result<Value, CoreError> {
    let counts = jobs::counts(&core.db).await?;
    let usage = crate::storage_ops::usage(core).await?;
    let profile = &core.profile;
    Ok(json!({
        "archive_id": profile.archive_id,
        "instance_id": core.instance_id.as_str(),
        "node_id": core.node_id.as_str(),
        "schema_version": trace_domain::SCHEMA_VERSION,
        "acquisition_budget": core.http.budget_snapshot(),
        "paused": core.paused.load(Ordering::SeqCst),
        "started_at": core.started_at.rfc3339,
        "job_counts": counts,
        "budgets": {
            "metadata_min_interval_ms": profile.metadata_interval_ms(),
            "metadata_jitter_ms": profile.metadata_jitter_ms(),
            "scan_page_budget": profile.scan_page_budget(),
            "live_concurrency": profile.live_concurrency.clamp(1, 4),
            "check_jitter_percent": 5,
        },
        "storage": usage,
        "media_backend": {
            "ffmpeg": core.media_backend.ffmpeg.as_ref().map(|p| p.to_string_lossy().to_string()),
            "ffprobe": core.media_backend.ffprobe.as_ref().map(|p| p.to_string_lossy().to_string()),
            "version": core.media_backend.version,
        },
        "media_tools": core.media_tools.status().await,
    }))
}
