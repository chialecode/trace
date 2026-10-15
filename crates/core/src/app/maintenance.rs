//! Quality upgrade candidates and the derived-data cache.
use super::{parse, unsupported};
use crate::core::Core;
use crate::error::CoreError;
use serde_json::Value;
use std::sync::Arc;
use trace_contracts::control::Request;

pub async fn route(core: &Arc<Core>, req: &Request) -> Result<Value, CoreError> {
    let args = req.args.clone();
    match req.command.as_str() {
        "upgrade.list" => crate::upgrades::list(core, args).await,
        "upgrade.resources" => crate::upgrades::resources(core, args).await,
        "upgrade.decide" => decide(core, args).await,
        "cache.status" => crate::cache::status(&core.db).await,
        "cache.configure" => configure(core, &args).await,
        "cache.control" => {
            let scope = args["scope"].as_str().unwrap_or_default().to_owned();
            let rebuild = args["rebuild"].as_bool().unwrap_or(false);
            crate::cache::control(core, scope, rebuild).await
        }
        other => Err(unsupported(other)),
    }
}

/// One choice, or a list of them under `items`.
async fn decide(core: &Arc<Core>, args: Value) -> Result<Value, CoreError> {
    let choices = if args.get("items").is_some() {
        parse(args["items"].clone())?
    } else {
        vec![parse(args)?]
    };
    crate::upgrades::decide(core, choices).await
}

async fn configure(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let index = args["index_budget_bytes"]
        .as_i64()
        .ok_or_else(|| CoreError::InvalidInput("index budget".into()))?;
    let derived = args["derived_budget_bytes"]
        .as_i64()
        .ok_or_else(|| CoreError::InvalidInput("derived budget".into()))?;
    crate::cache::configure(core, index, derived).await
}
