//! Duplicate, similarity and comparison commands (stage plan §11.5).
use super::{need_str, unsupported};
use crate::core::Core;
use crate::error::CoreError;
use crate::store;
use serde_json::Value;
use std::sync::Arc;
use trace_contracts::control::Request;
use trace_contracts::control::commands as cmd;

pub async fn route(core: &Arc<Core>, req: &Request) -> Result<Value, CoreError> {
    let args = req.args.clone();
    match req.command.as_str() {
        cmd::RESOURCE_DUPLICATES => {
            let after = args["after"].as_str().map(str::to_owned);
            let limit = args["limit"].as_i64().unwrap_or(50);
            core.db
                .read(move |c| store::resources::duplicates(c, after.as_deref(), limit))
                .await
        }
        cmd::RESOURCE_SIMILAR => {
            let entry = need_str(&args, "entry_id")?;
            let limit = args["limit"].as_i64().unwrap_or(20);
            crate::resources::similar(core, &entry, limit).await
        }
        cmd::RESOURCE_COMPARE => {
            entry_kind(&args, "left")?;
            entry_kind(&args, "right")?;
            let left = side_arg(&args, "left")?;
            let right = side_arg(&args, "right")?;
            crate::resources::compare(core, &left, &right).await
        }
        cmd::RESOURCE_COMPARE_DECIDE => {
            entry_kind(&args, "left")?;
            entry_kind(&args, "right")?;
            let left = side_arg(&args, "left")?;
            let right = side_arg(&args, "right")?;
            let decision = need_str(&args, "decision")?;
            crate::resources::decide(core, &left, &right, &decision).await
        }
        other => Err(unsupported(other)),
    }
}

/// Accepts `left_entry_id`/`right_entry_id` (the UI's shape) and `left`/`right`.
fn side_arg(args: &Value, side: &str) -> Result<String, CoreError> {
    args[format!("{side}_entry_id")]
        .as_str()
        .or_else(|| args[side].as_str())
        .filter(|s| !s.is_empty() && s.len() <= 512)
        .map(str::to_owned)
        .ok_or_else(|| CoreError::InvalidInput(format!("{side} entry")))
}

/// Comparison is implemented for entries only; a media/variant/blob label is refused instead of
/// being read as an entry id (F-078).
fn entry_kind(args: &Value, side: &str) -> Result<(), CoreError> {
    if let Some(kind) = args[format!("{side}_kind")].as_str()
        && kind != "entry"
    {
        return Err(CoreError::InvalidInput(
            "resource comparison supports entries only".into(),
        ));
    }
    Ok(())
}
