//! Download folders: settings, writing readable copies, checking and syncing them.
use super::{parse, unsupported};
use crate::core::Core;
use crate::error::CoreError;
use serde_json::Value;
use std::sync::Arc;
use trace_contracts::control::Request;
use trace_contracts::control::commands as cmd;
use trace_contracts::download::{
    CheckArgs, CopiesArgs, DownloadSettingsArgs, ExportArgs, ForgetArgs, SyncArgs,
};

pub async fn route(core: &Arc<Core>, req: &Request) -> Result<Value, CoreError> {
    let args = req.args.clone();
    match req.command.as_str() {
        cmd::DOWNLOAD_SETTINGS => Ok(serde_json::to_value(
            crate::downloads::settings(core).await?,
        )?),
        cmd::DOWNLOAD_SETTINGS_SET => {
            let args: DownloadSettingsArgs = parse(args)?;
            Ok(serde_json::to_value(
                crate::downloads::set_settings(core, args).await?,
            )?)
        }
        cmd::DOWNLOAD_EXPORT => {
            let args: ExportArgs = parse(args)?;
            Ok(serde_json::to_value(
                crate::downloads::export_entries(core, &args.entry_ids, args.dir.as_deref())
                    .await?,
            )?)
        }
        cmd::DOWNLOAD_CHECK => {
            let args: CheckArgs = parse(args)?;
            Ok(serde_json::to_value(
                crate::downloads::check(
                    core,
                    args.location_id.as_deref(),
                    args.entry_id.as_deref(),
                )
                .await?,
            )?)
        }
        cmd::DOWNLOAD_COPIES => {
            let args: CopiesArgs = parse(args)?;
            Ok(serde_json::to_value(
                crate::downloads::copies(core, &args.entry_id).await?,
            )?)
        }
        cmd::DOWNLOAD_SYNC => {
            let args: SyncArgs = parse(args)?;
            Ok(serde_json::to_value(
                crate::downloads::sync_to_library(core, &args.entry_ids).await?,
            )?)
        }
        cmd::DOWNLOAD_FORGET => {
            let args: ForgetArgs = parse(args)?;
            Ok(serde_json::to_value(
                crate::downloads::forget(core, &args.entry_id, args.location_id.as_deref()).await?,
            )?)
        }
        other => Err(unsupported(other)),
    }
}
