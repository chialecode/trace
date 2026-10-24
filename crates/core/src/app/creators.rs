//! Creators: the person-level identity that account bindings attach to (INV-01).
use super::{parse, unsupported};
use crate::core::Core;
use crate::error::CoreError;
use crate::store::creators::Field;
use crate::{jobs, store};
use serde_json::{Value, json};
use std::sync::Arc;
use trace_contracts::control as ctl;
use trace_contracts::control::Request;
use trace_contracts::control::commands as cmd;

pub async fn route(core: &Arc<Core>, req: &Request) -> Result<Value, CoreError> {
    let args = req.args.clone();
    match req.command.as_str() {
        cmd::CREATOR_CREATE => create(core, parse(args)?).await,
        cmd::CREATOR_UPDATE => update(core, parse(args)?).await,
        cmd::CREATOR_LIST => list(core, &args).await,
        cmd::CREATOR_GET => get(core, parse(args)?).await,
        cmd::CREATOR_MERGE
        | cmd::CREATOR_MERGE_PREVIEW
        | cmd::CREATOR_SPLIT
        | cmd::CREATOR_SPLIT_PREVIEW
        | cmd::CREATOR_RELINK
        | cmd::CREATOR_RELINK_PREVIEW
        | cmd::CREATOR_OPERATIONS => super::identity::route(core, req).await,
        other => Err(unsupported(other)),
    }
}

async fn dto(core: &Arc<Core>, id: &str) -> Result<Value, CoreError> {
    let id = id.to_owned();
    core.db
        .read(move |c| Ok(serde_json::to_value(store::creators::dto(c, &id)?)?))
        .await
}

async fn create(core: &Arc<Core>, a: ctl::CreatorCreateArgs) -> Result<Value, CoreError> {
    let id = format!("cre-{}", uuid::Uuid::new_v4());
    let now = jobs::now();
    let aliases = serde_json::to_string(&a.aliases)?;
    let tags = serde_json::to_string(&a.tags)?;
    let new_id = id.clone();
    core.db
        .write(move |tx| {
            store::creators::insert(
                tx,
                &new_id,
                &a.display_name,
                &aliases,
                &tags,
                &a.notes,
                &now,
            )?;
            crate::objects::outbox_emit(tx, "creator.changed", &new_id, &json!({}))?;
            Ok(())
        })
        .await?;
    dto(core, &id).await
}

/// Renaming never changes identity (INV-01); the search index follows the event.
async fn update(core: &Arc<Core>, a: ctl::CreatorUpdateArgs) -> Result<Value, CoreError> {
    let bad_name = a
        .display_name
        .as_ref()
        .is_some_and(|s| s.trim().is_empty() || s.len() > 1024);
    let bad_status = a
        .status
        .as_deref()
        .is_some_and(|s| !matches!(s, "active" | "archived"));
    if bad_name || bad_status {
        return Err(CoreError::InvalidInput("creator name/status".into()));
    }
    let aliases = a.aliases.as_ref().map(serde_json::to_string).transpose()?;
    let tags = a.tags.as_ref().map(serde_json::to_string).transpose()?;
    let id = a.creator_id.clone();
    let changes = [
        (Field::DisplayName, a.display_name),
        (Field::Aliases, aliases),
        (Field::Tags, tags),
        (Field::Notes, a.notes),
        (Field::Status, a.status),
    ];
    core.db
        .write(move |tx| {
            for (field, value) in changes {
                if let Some(value) = value {
                    store::creators::set_field(tx, &id, field, &value, &jobs::now())?;
                }
            }
            crate::objects::outbox_emit(tx, "creator.changed", &id, &json!({}))?;
            Ok(())
        })
        .await?;
    dto(core, &a.creator_id).await
}

async fn list(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let include_archived = args
        .get("include_archived")
        .and_then(Value::as_bool)
        .unwrap_or(false);
    let ids = core
        .db
        .read(move |c| store::creators::list_ids(c, include_archived))
        .await?;
    let mut items = Vec::new();
    for id in ids {
        items.push(dto(core, &id).await?);
    }
    Ok(json!({ "items": items }))
}

async fn get(core: &Arc<Core>, a: ctl::CreatorGetArgs) -> Result<Value, CoreError> {
    let requested = a.creator_id.clone();
    let resolved = core
        .db
        .read(move |c| store::identity::resolve(c, &requested))
        .await?;
    let mut creator = dto(core, &resolved).await?;
    let id = resolved.clone();
    let accounts = core
        .db
        .read(move |c| store::creators::bound_accounts(c, &id))
        .await?;
    creator["accounts"] = Value::Array(accounts);
    let scope = {
        let id = resolved.clone();
        core.db
            .read(move |c| store::identity::scope(c, &id))
            .await?
    };
    creator["scope"] = scope;
    if resolved != a.creator_id {
        creator["merged_into"] = Value::String(resolved);
    }
    Ok(creator)
}
