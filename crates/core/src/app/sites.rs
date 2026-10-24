//! Supported sites: the persisted rule catalog and link recognition. Both are answered
//! from the database alone, so they work offline and while a plugin is disabled.
use super::{parse, unsupported};
use crate::core::Core;
use crate::error::CoreError;
use serde_json::{Value, json};
use std::sync::Arc;
use trace_contracts::catalog::RecognizeArgs;
use trace_contracts::control::Request;
use trace_contracts::control::commands as cmd;

pub async fn route(core: &Arc<Core>, req: &Request) -> Result<Value, CoreError> {
    match req.command.as_str() {
        cmd::SITE_CATALOG => Ok(serde_json::to_value(
            core.db.read(crate::catalog::view).await?,
        )?),
        cmd::SITE_RECOGNIZE => {
            let args: RecognizeArgs = parse(req.args.clone())?;
            let recognition = core
                .db
                .read(move |c| crate::catalog::recognize(c, &args.url))
                .await?;
            Ok(serde_json::to_value(recognition)?)
        }
        cmd::SITE_REFRESH => Ok(json!({"answered": crate::catalog::refresh(core).await?})),
        other => Err(unsupported(other)),
    }
}
