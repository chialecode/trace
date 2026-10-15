//! Command routing for the control protocol (INV-09: write commands keep an idempotency receipt).
//!
//! This layer only decides whether a request is a repeat of an earlier write and records
//! the first result. What a command does lives in `app/`; every SQL statement in `store/`.

use crate::app;
use crate::core::Core;
use crate::error::CoreError;
use crate::{jobs, store};
use serde_json::Value;
use std::sync::Arc;
use trace_contracts::control::commands as cmd;
use trace_contracts::control::{Request, Response};
use trace_domain::CONTROL_PROTOCOL_VERSION;

pub fn error_response(request_id: &str, e: CoreError) -> Response {
    crate::pluginhost::fault_log::record_core(&e);
    Response {
        protocol_version: CONTROL_PROTOCOL_VERSION.into(),
        request_id: request_id.into(),
        ok: false,
        result: None,
        error: Some(e.to_api_error()),
    }
}

fn ok_response(request_id: &str, result: Value) -> Response {
    Response {
        protocol_version: CONTROL_PROTOCOL_VERSION.into(),
        request_id: request_id.into(),
        ok: true,
        result: Some(result),
        error: None,
    }
}

/// Write commands need an idempotency receipt.
fn is_write(command: &str) -> bool {
    matches!(
        command,
        cmd::CREATOR_CREATE
            | cmd::CREATOR_UPDATE
            | cmd::ACCOUNT_BIND
            | cmd::ACCOUNT_UNBIND
            | cmd::ACCOUNT_UPDATE
            | cmd::SUBSCRIPTION_CREATE
            | cmd::SUBSCRIPTION_UPDATE
            | "archive.delete"
            | "archive.restore_deleted"
            | "credential.update"
            | cmd::CREDENTIAL_IMPORT
            | cmd::CREDENTIAL_REVOKE
            | cmd::PLUGIN_SET_STATE
            | cmd::EXPORT_START
            | cmd::BACKUP_START
            | cmd::RESTORE_START
            | cmd::DAEMON_PAUSE
            | cmd::DAEMON_SHUTDOWN
            | "archive.entry.recheck"
            | cmd::ARCHIVE_ENTRY_SYNC
            | cmd::ARCHIVE_RECHECK
            | cmd::NETWORK_SET
            | cmd::LIBRARY_MIGRATE
            | cmd::LIBRARY_SWITCH
            | cmd::LIBRARY_CANCEL
            | cmd::LIBRARY_RELEASE_OLD
            | cmd::DOWNLOAD_SETTINGS_SET
            | cmd::DOWNLOAD_EXPORT
            | cmd::DOWNLOAD_SYNC
            | cmd::DOWNLOAD_FORGET
            | "upgrade.decide"
            | "job.retry_failed"
            | "quality.configure"
            | "cache.configure"
            | "cache.control"
            | "account.resolve"
            | "source.confirm"
            | cmd::SOURCE_PROFILE_DOWNLOAD
            | cmd::SOURCE_DELIVERY_CANCEL
            | "source.cancel"
            | cmd::CREATOR_MERGE
            | cmd::CREATOR_SPLIT
            | cmd::CREATOR_RELINK
            | cmd::IDENTITY_CONFIRM
            | cmd::IDENTITY_REJECT
            | cmd::LIVE_REPLAY_LINK
            | cmd::LIVE_REPLAY_REJECT
            | cmd::IMPORT_START
            | cmd::IMPORT_CANCEL
            | cmd::IMPORT_RESUME
            | cmd::IMPORT_ASSIGN
            | cmd::IMPORT_CHECK
            | cmd::RESOURCE_COMPARE_DECIDE
            | cmd::WORK_GROUP_CREATE
            | cmd::WORK_GROUP_UPDATE
            | cmd::WORK_GROUP_DELETE
            | cmd::WORK_GROUP_ADD
            | cmd::WORK_GROUP_REMOVE
            | cmd::WORK_BATCH_APPLY
            | cmd::PLUGIN_INSTALL
            | cmd::PLUGIN_ACTIVATE
            | cmd::PLUGIN_UPGRADE
            | cmd::PLUGIN_ROLLBACK
            | cmd::PLUGIN_REMOVE
            | cmd::PLUGIN_CAPABILITY_SET
            | cmd::TOOL_CHECK_UPDATE
            | cmd::TOOL_APPLY_UPDATE
            | cmd::TOOL_ROLLBACK
    )
}

pub async fn dispatch(core: &Arc<Core>, req: Request) -> Response {
    let Some(key) = req
        .idempotency_key
        .clone()
        .filter(|_| is_write(&req.command))
    else {
        return respond(&req, app::execute(core, &req).await);
    };
    // A repeated key returns the first result (C10).
    let lookup = key.clone();
    let earlier = core
        .db
        .read(move |c| Ok(store::receipts::find(c, &lookup)))
        .await
        .unwrap_or(None);
    if let Some(cached) = earlier {
        let result = serde_json::from_str(&cached).unwrap_or(Value::Null);
        return ok_response(&req.request_id, result);
    }
    let result = app::execute(core, &req).await;
    if let Ok(value) = &result {
        let text = value.to_string();
        let _ = core
            .db
            .write(move |tx| store::receipts::insert(tx, &key, &text, &jobs::now()))
            .await;
    }
    respond(&req, result)
}

fn respond(req: &Request, result: Result<Value, CoreError>) -> Response {
    match result {
        Ok(value) => ok_response(&req.request_id, value),
        Err(e) => error_response(&req.request_id, e),
    }
}
