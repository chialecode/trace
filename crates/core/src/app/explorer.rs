//! The resource explorer: folder tree, file list and the two actions that reach the system.
use super::{parse, unsupported};
use crate::core::Core;
use crate::error::CoreError;
use serde_json::Value;
use std::sync::Arc;
use trace_contracts::control::Request;
use trace_contracts::control::commands as cmd;

pub async fn route(core: &Arc<Core>, req: &Request) -> Result<Value, CoreError> {
    let args = req.args.clone();
    match req.command.as_str() {
        cmd::EXPLORER_TREE => Ok(serde_json::to_value(
            crate::explorer::tree(core, parse(args)?).await?,
        )?),
        cmd::EXPLORER_LIST => Ok(serde_json::to_value(
            crate::explorer::list(core, parse(args)?).await?,
        )?),
        cmd::EXPLORER_OPEN => crate::explorer::open(core, parse(args)?).await,
        cmd::EXPLORER_REVEAL => crate::explorer::reveal(core, parse(args)?).await,
        other => Err(unsupported(other)),
    }
}
