//! The archive as users browse it: timeline, entries, history, resources, deletion, health and search.
use super::media::launch_explorer;
use super::{need_str, parse, unsupported};
use crate::core::Core;
use crate::db::DbHandle;
use crate::error::CoreError;
use crate::store;
use serde_json::{Value, json};
use std::ffi::OsStr;
use std::sync::Arc;
use trace_contracts::control as ctl;
use trace_contracts::control::Request;
use trace_contracts::control::commands as cmd;
use trace_contracts::media_sync::MediaSyncArgs;

pub use crate::store::archive::{PageArgs, ReadArgs, ResourceArgs};

pub async fn route(core: &Arc<Core>, req: &Request) -> Result<Value, CoreError> {
    let args = req.args.clone();
    match req.command.as_str() {
        cmd::ARCHIVE_TIMELINE => {
            let mut args: ctl::ArchiveTimelineArgs = parse(args)?;
            args.creator_id = super::resolve_creator(core, args.creator_id).await?;
            crate::feed::query(&core.db, args).await
        }
        cmd::ARCHIVE_ENTRY_GET => entry_with_authentication(core, parse(args)?).await,
        cmd::ARCHIVE_SUBSCRIPTION_EVIDENCE => subscription_evidence(core, &args).await,
        cmd::ARCHIVE_TAGS => {
            let args: trace_contracts::entry_meta::TagsArgs = parse(args)?;
            Ok(serde_json::to_value(
                core.db
                    .read(move |c| store::meta::top_tags(c, &args))
                    .await?,
            )?)
        }
        cmd::ARCHIVE_OPEN_SOURCE => open_source(core, parse(args)?).await,
        "archive.acquisitions" => crate::acquisitions::list(core, args).await,
        "archive.resources" => resources(&core.db, parse(args)?).await,
        "archive.entry.page" => page(&core.db, parse(args)?).await,
        "archive.revision.read" => read(&core.db, parse(args)?).await,
        "archive.observation.read" => observation_read(core, &args).await,
        "archive.entry.recheck" => recheck(&core.db, need_str(&args, "entry_id")?).await,
        cmd::ARCHIVE_ENTRY_SYNC => {
            let a: MediaSyncArgs = parse(args)?;
            Ok(serde_json::to_value(
                super::media_sync::sync(core, &a.entry_ids).await?,
            )?)
        }
        cmd::ARCHIVE_RECHECK => Ok(serde_json::to_value(
            super::media_sync::recheck(core, parse(args)?).await?,
        )?),
        "archive.delete_preview"
        | "archive.delete"
        | "archive.restore_deleted"
        | "archive.deleted" => deletion(core, req.command.as_str(), &args).await,
        "health.list" => crate::issues::listing(core).await,
        "health.refs" => {
            let key = need_str(&args, "issue_key")?;
            crate::issues::refs(core, key, args["after"].as_str().unwrap_or("").to_owned()).await
        }
        cmd::SEARCH_QUERY => {
            let results =
                crate::search::query(&core.db, parse::<ctl::SearchQueryArgs>(args)?).await?;
            Ok(serde_json::to_value(results)?)
        }
        other => Err(unsupported(other)),
    }
}

async fn subscription_evidence(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let subscription = need_str(args, "subscription_id")?;
    let limit = args["limit"].as_i64().unwrap_or(500);
    let mut evidence = core
        .db
        .read(move |c| store::archive::subscription_evidence(c, &subscription, limit))
        .await?;
    let context_ids: std::collections::HashSet<String> = evidence["entries"]
        .as_array()
        .into_iter()
        .flatten()
        .flat_map(|entry| {
            entry["contexts"]
                .as_array()
                .into_iter()
                .flatten()
                .filter_map(|context| context["context_id"].as_str().map(str::to_owned))
        })
        .collect();
    let budget = core.http.budget_snapshot();
    let requests: Vec<Value> = budget
        .as_ref()
        .and_then(|value| value["requests"].as_array())
        .into_iter()
        .flatten()
        .filter(|attempt| {
            attempt["evidence_id"]
                .as_str()
                .is_some_and(|id| context_ids.contains(id))
        })
        .map(|attempt| {
            json!({
                "evidence_id": attempt["evidence_id"],
                "endpoint": attempt["endpoint"],
                "method": attempt["method"],
                "status": attempt["status"],
                "metadata": attempt["metadata"],
                "started_at": attempt["started_at"],
            })
        })
        .collect();
    let mut media_bytes = serde_json::Map::new();
    if let Some(bytes) = budget
        .as_ref()
        .and_then(|value| value["evidence_media_bytes"].as_object())
    {
        for (context, value) in bytes {
            if context_ids.contains(context) {
                media_bytes.insert(context.clone(), value.clone());
            }
        }
    }
    evidence["requests"] = Value::Array(requests);
    evidence["evidence_media_bytes"] = Value::Object(media_bytes);
    evidence["budget_available"] = Value::Bool(budget.is_some());
    Ok(evidence)
}

async fn deletion(core: &Arc<Core>, command: &str, args: &Value) -> Result<Value, CoreError> {
    match command {
        "archive.delete_preview" => {
            let (entry, scope) = (need_str(args, "entry_id")?, need_str(args, "scope")?);
            crate::deletion::preview(core, entry, scope).await
        }
        "archive.delete" => {
            let (entry, scope) = (need_str(args, "entry_id")?, need_str(args, "scope")?);
            crate::deletion::apply(core, entry, scope, need_str(args, "token")?).await
        }
        "archive.restore_deleted" => {
            let entry = need_str(args, "entry_id")?;
            let generation = args["generation"]
                .as_i64()
                .ok_or_else(|| CoreError::InvalidInput("deletion generation".into()))?;
            crate::deletion::restore(core, entry, generation).await
        }
        _ => crate::deletion::listing(core, args["after"].as_str().unwrap_or("").to_owned()).await,
    }
}

pub async fn page(db: &DbHandle, a: PageArgs) -> Result<Value, CoreError> {
    db.read(move |c| store::archive::page(c, &a)).await
}

/// Independent resource inventory, including missing resources and every saved variant.
pub async fn resources(db: &DbHandle, a: ResourceArgs) -> Result<Value, CoreError> {
    db.read(move |c| store::archive::resources(c, a)).await
}

pub async fn read(db: &DbHandle, a: ReadArgs) -> Result<Value, CoreError> {
    db.read(move |c| store::archive::read(c, a)).await
}

async fn observation_read(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let entry = need_str(args, "entry_id")?;
    let seq = args["seq"]
        .as_i64()
        .ok_or_else(|| CoreError::InvalidInput("observation sequence".into()))?;
    let offset = args["offset"].as_u64().unwrap_or(0);
    core.db
        .read(move |c| store::archive::observation(c, &entry, seq, offset))
        .await
}

pub async fn entry(db: &DbHandle, entry_id: String) -> Result<Value, CoreError> {
    db.read(move |c| store::archive::entry(c, &entry_id)).await
}

async fn entry_with_authentication(
    core: &Arc<Core>,
    a: ctl::ArchiveEntryGetArgs,
) -> Result<Value, CoreError> {
    let entry_id = a.entry_id;
    let for_summary = entry_id.clone();
    let mut result = entry(&core.db, entry_id).await?;
    let for_meta = for_summary.clone();
    result["authentication"] = core
        .db
        .read(move |c| crate::acquisitions::summary(c, &for_summary))
        .await?;
    result["meta"] = serde_json::to_value(
        core.db
            .read(move |c| store::meta::summary(c, &for_meta))
            .await?,
    )?;
    Ok(result)
}

/// Asks the subscription that covers the entry to observe it again.
async fn recheck(db: &DbHandle, entry: String) -> Result<Value, CoreError> {
    db.write(move |tx| {
        let id = super::media_sync::recheck_entry(tx, &entry)?;
        Ok(json!({"job_id": id}))
    })
    .await
}

async fn open_source(core: &Arc<Core>, a: ctl::ArchiveEntryGetArgs) -> Result<Value, CoreError> {
    let url = core
        .db
        .read(move |c| {
            let (namespace, kind, key) = store::entries::native_identity(c, &a.entry_id)?;
            Ok(crate::feed::entry_link(
                c,
                &a.entry_id,
                &namespace,
                &kind,
                &key,
            ))
        })
        .await?
        .ok_or_else(|| CoreError::Unsupported("source URL unavailable".into()))?;
    launch_explorer(OsStr::new(&url))?;
    Ok(json!({"opened": true}))
}
