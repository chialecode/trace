//! Live sessions: fragments, auxiliary data and the manual recording switch.
use super::{require_str, unsupported};
use crate::core::Core;
use crate::error::CoreError;
use crate::{jobs, store};
use serde_json::{Value, json};
use std::sync::Arc;
use trace_contracts::control::Request;
use trace_contracts::control::commands as cmd;

pub async fn route(core: &Arc<Core>, req: &Request) -> Result<Value, CoreError> {
    let args = &req.args;
    match req.command.as_str() {
        "live.fragments" => fragments(core, args).await,
        "live.auxiliary" => {
            let session = require_str(args, "session_id", "session id")?;
            crate::recording::auxiliary::page(core, session, args["before"].as_i64()).await
        }
        "live.set_recording" => set_recording(core, args).await,
        cmd::LIVE_REPLAY_CANDIDATES => replay_candidates(core, args).await,
        cmd::LIVE_REPLAY_LINK => {
            let session = require_str(args, "session_id", "session id")?;
            let entry = require_str(args, "entry_id", "entry id")?;
            let decision = args["decision"].as_str().unwrap_or("keep_both");
            crate::replays::link(core, &session, &entry, decision).await
        }
        cmd::LIVE_REPLAY_REJECT => {
            let session = require_str(args, "session_id", "session id")?;
            let entry = require_str(args, "entry_id", "entry id")?;
            let reason = args["reason"].as_str().unwrap_or("");
            crate::replays::reject(core, &session, &entry, reason).await
        }
        cmd::LIVE_REPLAY_LINKS => {
            let session = args["session_id"].as_str().map(str::to_owned);
            let entry = args["entry_id"].as_str().map(str::to_owned);
            core.db
                .read(move |c| store::replays::links(c, session.as_deref(), entry.as_deref()))
                .await
        }
        other => Err(unsupported(other)),
    }
}

async fn replay_candidates(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    if let Some(session_id) = args["session_id"].as_str() {
        let id = session_id.to_owned();
        let session = core
            .db
            .read(move |c| store::replays::session(c, &id))
            .await?;
        let mut candidates = core
            .db
            .read(move |c| store::replays::candidates_for_session(c, &session))
            .await?;
        if let Some(items) = candidates["items"].as_array_mut() {
            for candidate in items {
                let Some(entry_id) = candidate["entry_id"].as_str().map(str::to_owned) else {
                    continue;
                };
                let preview = crate::replays::preview(core, session_id, &entry_id).await?;
                if let Some(fields) = preview.as_object()
                    && let Some(candidate) = candidate.as_object_mut()
                {
                    candidate.extend(fields.clone());
                }
            }
        }
        return Ok(candidates);
    }
    if let Some(entry_id) = args["entry_id"].as_str() {
        let id = entry_id.to_owned();
        let entry = core.db.read(move |c| store::replays::entry(c, &id)).await?;
        let candidates = core
            .db
            .read(move |c| store::replays::candidates_for_entry(c, &entry))
            .await?;
        return Ok(candidates);
    }
    Err(CoreError::InvalidInput(
        "a session_id or entry_id is required".into(),
    ))
}

async fn fragments(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let session = require_str(args, "session_id", "session identity")?;
    let before = args["before"].as_i64().unwrap_or(i64::MAX);
    core.db
        .read(move |c| store::live::fragments_page(c, &session, before))
        .await
}

async fn set_recording(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let session = require_str(args, "session_id", "session id")?;
    let enabled = args["enabled"]
        .as_bool()
        .ok_or_else(|| CoreError::InvalidInput("recording state".into()))?;
    let expected_generation = args["generation"].as_i64();
    core.db
        .write(move |tx| {
            let changed = store::live::set_recording(
                tx,
                &session,
                enabled,
                expected_generation,
                &jobs::now(),
            )?;
            if changed {
                let payload = json!({"recording_enabled": enabled});
                crate::objects::outbox_emit(tx, "live.updated", &session, &payload)?;
            }
            Ok(())
        })
        .await?;
    Ok(json!({"ok": true}))
}
