//! Use cases behind the control commands.
//!
//! `ipc/` decodes frames and tracks idempotency receipts; everything a command does —
//! validation, transaction boundaries, events, calls into adapters — happens here, and
//! every SQL statement lives in `store/`. Each command family has one module with a
//! `route` function that maps command names to the use cases of that family.

pub mod accounts;
pub mod archive;
pub mod creators;
pub mod credential_probe;
pub mod credentials;
pub mod daemon;
pub mod downloads;
pub mod explorer;
pub mod identity;
pub mod imports;
pub mod jobs;
pub mod library;
pub mod live;
pub mod maintenance;
pub mod management;
pub mod media;
pub mod media_sync;
pub mod network;
pub mod plugins;
pub mod quality;
pub mod resources;
pub mod sites;
pub mod source;
pub mod storage;
pub mod subscriptions;
pub mod work;

use crate::core::Core;
use crate::error::CoreError;
use serde_json::Value;
use std::sync::Arc;
use trace_contracts::control::Request;

/// Runs one control command and returns its result document.
pub async fn execute(core: &Arc<Core>, req: &Request) -> Result<Value, CoreError> {
    let family = req.command.split('.').next().unwrap_or_default();
    match family {
        "source" => source::route(core, req).await,
        "site" => sites::route(core, req).await,
        "network" => network::route(core, req).await,
        "library" => library::route(core, req).await,
        "download" => downloads::route(core, req).await,
        "explorer" => explorer::route(core, req).await,
        "daemon" => daemon::route(core, req).await,
        "archive" | "health" | "search" => archive::route(core, req).await,
        "creator" => creators::route(core, req).await,
        "identity" => identity::route(core, req).await,
        "import" => imports::route(core, req).await,
        "account" => accounts::route(core, req).await,
        "subscription" => subscriptions::route(core, req).await,
        "upgrade" | "cache" => maintenance::route(core, req).await,
        "job" => jobs::route(core, req).await,
        "credential" => credentials::route(core, req).await,
        "plugin" => plugins::route(core, req).await,
        "storage" => storage::route(core, req).await,
        "live" => live::route(core, req).await,
        "quality" => quality::route(core, req).await,
        "resource" => resources::route(core, req).await,
        "work" => work::route(core, req).await,
        "management" => management::route(core, req).await,
        "tool" => crate::tool_update::route(core, &req.command, req.args.clone()).await,
        "media" => media::route(core, req).await,
        _ => Err(unsupported(&req.command)),
    }
}

pub(crate) fn unsupported(command: &str) -> CoreError {
    CoreError::Unsupported(format!("command {command}"))
}

pub(crate) fn parse<T: serde::de::DeserializeOwned>(args: Value) -> Result<T, CoreError> {
    serde_json::from_value(args).map_err(|e| CoreError::InvalidInput(format!("args: {e}")))
}

/// A required, non-empty string argument of bounded length.
pub(crate) fn need_str(args: &Value, key: &str) -> Result<String, CoreError> {
    args[key]
        .as_str()
        .filter(|s| !s.is_empty() && s.len() <= 4096)
        .map(str::to_owned)
        .ok_or_else(|| CoreError::InvalidInput(format!("missing {key}")))
}

/// A required string argument with a caller-chosen error text.
pub(crate) fn require_str(args: &Value, key: &str, what: &str) -> Result<String, CoreError> {
    args[key]
        .as_str()
        .map(str::to_owned)
        .ok_or_else(|| CoreError::InvalidInput(what.into()))
}

pub(crate) use crate::jobs::plus_secs;

/// A filter that names a creator follows one merge redirect, so an old link keeps working.
pub(crate) async fn resolve_creator(
    core: &Arc<Core>,
    creator_id: Option<String>,
) -> Result<Option<String>, CoreError> {
    let Some(id) = creator_id else {
        return Ok(None);
    };
    let resolved = core
        .db
        .read(move |c| crate::store::identity::resolve(c, &id))
        .await?;
    Ok(Some(resolved))
}
