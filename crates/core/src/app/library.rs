//! Library location: status and the resumable move to another folder.
use super::{parse, unsupported};
use crate::core::Core;
use crate::error::CoreError;
use serde_json::Value;
use std::sync::Arc;
use trace_contracts::control::Request;
use trace_contracts::control::commands as cmd;
use trace_contracts::library::{MigrateArgs, ReleaseArgs};

pub async fn route(core: &Arc<Core>, req: &Request) -> Result<Value, CoreError> {
    let status = match req.command.as_str() {
        cmd::LIBRARY_STATUS => crate::library::status(core).await?,
        cmd::LIBRARY_MIGRATE => {
            let args: MigrateArgs = parse(req.args.clone())?;
            crate::library::start(core, &args.to_path).await?
        }
        cmd::LIBRARY_SWITCH => crate::library::switch(core).await?,
        cmd::LIBRARY_CANCEL => crate::library::cancel(core).await?,
        cmd::LIBRARY_RELEASE_OLD => {
            let args: ReleaseArgs = parse(req.args.clone())?;
            crate::library::release_old(core, &args.location_id).await?
        }
        other => return Err(unsupported(other)),
    };
    Ok(serde_json::to_value(status)?)
}
