//! The audit list for management actions (stage plan §11: every action is queryable).
use super::unsupported;
use crate::core::Core;
use crate::error::CoreError;
use crate::store;
use serde_json::Value;
use std::sync::Arc;
use trace_contracts::control::Request;
use trace_contracts::control::commands as cmd;

pub async fn route(core: &Arc<Core>, req: &Request) -> Result<Value, CoreError> {
    match req.command.as_str() {
        cmd::MANAGEMENT_OPERATIONS => {
            let kind = req.args["kind"].as_str().map(str::to_owned);
            let subject = req.args["subject"].as_str().map(str::to_owned);
            let after = req.args["after"].as_str().map(str::to_owned);
            let limit = req.args["limit"].as_i64().unwrap_or(50);
            core.db
                .read(move |c| {
                    store::management::list(
                        c,
                        kind.as_deref(),
                        subject.as_deref(),
                        after.as_deref(),
                        limit,
                    )
                })
                .await
        }
        other => Err(unsupported(other)),
    }
}
