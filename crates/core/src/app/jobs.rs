//! Job inspection and explicit user actions on jobs.
use super::{parse, unsupported};
use crate::core::Core;
use crate::error::CoreError;
use crate::{jobs, store};
use serde_json::{Value, json};
use std::sync::Arc;
use trace_contracts::control as ctl;
use trace_contracts::control::Request;
use trace_contracts::control::commands as cmd;

const RETRY_BATCH_MAX: usize = 100;

pub async fn route(core: &Arc<Core>, req: &Request) -> Result<Value, CoreError> {
    let args = req.args.clone();
    match req.command.as_str() {
        "job.retry_failed" => retry_failed(core, args).await,
        cmd::JOB_LIST => list(core, parse(args)?).await,
        cmd::JOB_GET => get(core, parse(args)?).await,
        cmd::JOB_RETRY => retry(core, parse(args)?).await,
        cmd::JOB_CANCEL => cancel(core, parse(args)?).await,
        other => Err(unsupported(other)),
    }
}

/// Requeues up to 100 stopped jobs at once; jobs that are not stopped are left alone.
async fn retry_failed(core: &Arc<Core>, args: Value) -> Result<Value, CoreError> {
    let ids: Vec<String> = parse(args["job_ids"].clone())?;
    if ids.is_empty() || ids.len() > RETRY_BATCH_MAX {
        return Err(CoreError::InvalidInput("retry batch size".into()));
    }
    let requeued = core
        .db
        .write(move |tx| {
            let mut count = 0;
            for id in ids {
                if store::jobs::retry_stopped(tx, &id, &jobs::now())? > 0 {
                    jobs::consumers::retry(tx, &id)?;
                    count += 1;
                }
            }
            Ok(count)
        })
        .await?;
    Ok(json!({"requeued": requeued}))
}

async fn list(core: &Arc<Core>, a: ctl::JobListArgs) -> Result<Value, CoreError> {
    let limit = a.limit.clamp(1, 200);
    let items = core
        .db
        .read(move |c| store::jobs::list(c, a.state.as_deref(), a.job_type.as_deref(), limit))
        .await?;
    Ok(serde_json::to_value(ctl::PageDto {
        items,
        next_cursor: None,
    })?)
}

async fn get(core: &Arc<Core>, a: ctl::JobGetArgs) -> Result<Value, CoreError> {
    let job = core
        .db
        .read(move |c| store::jobs::get(c, &a.job_id))
        .await?
        .ok_or_else(|| CoreError::NotFound("job".into()))?;
    Ok(serde_json::to_value(job)?)
}

/// Explicit retry keeps the execution history (earlier errors stay) and requeues the job.
async fn retry(core: &Arc<Core>, a: ctl::JobRetryArgs) -> Result<Value, CoreError> {
    core.db
        .write(move |tx| {
            if store::jobs::retry(tx, &a.job_id, &jobs::now())? == 0 {
                return Err(CoreError::NotFound("retryable job".into()));
            }
            jobs::consumers::retry(tx, &a.job_id)
        })
        .await?;
    Ok(json!({"ok": true}))
}

/// Cancels directly, without a new target generation: no attempt-level cancellation is needed.
async fn cancel(core: &Arc<Core>, a: ctl::JobCancelArgs) -> Result<Value, CoreError> {
    let cancelled = core
        .db
        .write(move |tx| jobs::cancel_work(tx, &a.job_id))
        .await?;
    Ok(json!({"cancelled": cancelled}))
}
