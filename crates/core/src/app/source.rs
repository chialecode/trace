//! Source acquisition: preview a link, confirm or cancel the request.
use super::{parse, unsupported};
use crate::core::Core;
use crate::error::CoreError;
use crate::source_acquisition as acquisition;
use serde_json::Value;
use std::sync::Arc;
use trace_contracts::control::Request;
use trace_contracts::control::commands as cmd;
use trace_contracts::preview::DeliveryCancelArgs;

pub async fn route(core: &Arc<Core>, req: &Request) -> Result<Value, CoreError> {
    let args = req.args.clone();
    match req.command.as_str() {
        "source.preview" => acquisition::preview(core, parse(args)?).await,
        cmd::SOURCE_PROFILE_PREVIEW => Ok(serde_json::to_value(
            crate::profile_preview::preview(core, parse(args)?).await?,
        )?),
        cmd::SOURCE_PROFILE_DOWNLOAD => Ok(serde_json::to_value(
            crate::profile_preview::download(core, parse(args)?).await?,
        )?),
        cmd::SOURCE_DELIVERIES => Ok(serde_json::to_value(
            crate::downloads::delivery::list(core, parse(args)?).await?,
        )?),
        cmd::SOURCE_DELIVERY_CANCEL => {
            let a: DeliveryCancelArgs = parse(args)?;
            let stopped = crate::downloads::delivery::cancel(core, a.delivery_id).await?;
            Ok(serde_json::json!({"cancelled": stopped}))
        }
        "source.confirm" => acquisition::confirm(core, parse(args)?).await,
        "source.list" => acquisition::list(core).await,
        "source.cancel" => {
            let request_id = args["request_id"]
                .as_str()
                .ok_or_else(|| CoreError::InvalidInput("source request ID".into()))?
                .to_owned();
            acquisition::cancel(core, request_id).await
        }
        other => Err(unsupported(other)),
    }
}
