//! Quality commands: pinning a variant, recycling superseded copies, listing and policy.
use super::{require_str, unsupported};
use crate::core::Core;
use crate::error::CoreError;
use crate::store;
use serde_json::{Value, json};
use std::sync::Arc;
use trace_contracts::control::Request;

const RECYCLE_WAIT_MAX_SECONDS: i64 = 2_592_000;
const POLICY_VERSION: &str = "m1-v1";

pub async fn route(core: &Arc<Core>, req: &Request) -> Result<Value, CoreError> {
    let args = &req.args;
    match req.command.as_str() {
        "quality.pin" => pin(core, args).await,
        "quality.recycle" => crate::quality::recycle(core).await,
        "quality.list" => match args["entry_id"].as_str().filter(|s| !s.is_empty()) {
            Some(entry) => {
                crate::quality::listing(core, entry.to_owned(), args["after"].as_i64().unwrap_or(0))
                    .await
            }
            None => {
                crate::quality::filtered_listing(
                    core,
                    args["state"].as_str(),
                    args["after"].as_str(),
                    args["limit"].as_i64().unwrap_or(50),
                )
                .await
            }
        },
        "quality.configure" => configure(core, args).await,
        other => Err(unsupported(other)),
    }
}

async fn pin(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let _gate = core.archive_access.read().await;
    let enabled = args["enabled"]
        .as_bool()
        .ok_or_else(|| CoreError::InvalidInput("pin state".into()))?;
    if let Some(variant) = args["variant_id"].as_str().filter(|s| !s.is_empty()) {
        let variant = variant.to_owned();
        core.db
            .write(move |tx| crate::quality::pin(tx, &variant, enabled))
            .await?;
        return Ok(json!({"pinned": enabled}));
    }
    let entry = require_str(args, "entry_id", "entry identity")?.to_owned();
    core.db
        .write(move |tx| crate::quality::pin_entry(tx, &entry, enabled))
        .await?;
    Ok(json!({"pinned": enabled}))
}

/// Saves the policy; with an entry it re-evaluates that entry, without one it queues the same
/// bounded sweep from the beginning and returns the task and the range it will cover.
async fn configure(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let prefer_fps = args["prefer_fps"]
        .as_bool()
        .ok_or_else(|| CoreError::InvalidInput("quality preference".into()))?;
    let wait = args["wait_seconds"]
        .as_i64()
        .filter(|n| (0..=RECYCLE_WAIT_MAX_SECONDS).contains(n))
        .ok_or_else(|| CoreError::InvalidInput("recycle wait".into()))?;
    let entry = args["entry_id"]
        .as_str()
        .filter(|s| !s.is_empty())
        .map(str::to_owned);
    let result = core
        .db
        .write(move |tx| {
            let policy =
                json!({"prefer_fps": prefer_fps, "wait_seconds": wait, "version": POLICY_VERSION});
            store::quality::save_policy(tx, &policy.to_string())?;
            let scope = match &entry {
                Some(entry) => {
                    let revision = store::quality::current_revision(tx, entry)?;
                    crate::quality::evaluate(tx, &revision, prefer_fps, wait)?;
                    let payload = json!({"policy": policy});
                    crate::objects::outbox_emit(tx, "entry.quality_changed", entry, &payload)?;
                    json!({"entry_id": entry})
                }
                None => {
                    // The sweep resumes from its first batch; already fixed versions stay fixed.
                    crate::quality::schedule(tx, true)?;
                    let entries: i64 = tx.query_row(
                        "SELECT count(DISTINCT r.entry_id) FROM media_items m JOIN entry_revisions r ON r.revision_id=m.revision_id",
                        [],
                        |r| r.get(0),
                    )?;
                    json!({"task_id": "quality-maintenance", "estimated_entries": entries})
                }
            };
            Ok(json!({"updated": true, "scope": scope, "policy": policy}))
        })
        .await?;
    Ok(result)
}
