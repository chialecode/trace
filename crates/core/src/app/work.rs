//! Cross-platform work groups and explicit batch cleanup (stage plan §11.7).
use super::{need_str, unsupported};
use crate::core::Core;
use crate::error::CoreError;
use crate::store;
use crate::store::work::scope_digest;
use serde_json::{Value, json};
use std::sync::Arc;
use trace_contracts::control::Request;
use trace_contracts::control::commands as cmd;

const PREVIEW_TTL_SECONDS: i64 = 300;
const MAX_ENTRIES: usize = 200;

pub async fn route(core: &Arc<Core>, req: &Request) -> Result<Value, CoreError> {
    let args = req.args.clone();
    match req.command.as_str() {
        cmd::WORK_GROUP_CREATE => create(core, &args).await,
        cmd::WORK_GROUP_UPDATE => update(core, &args).await,
        cmd::WORK_GROUP_DELETE => delete(core, &args).await,
        cmd::WORK_GROUP_ADD => add(core, &args).await,
        cmd::WORK_GROUP_REMOVE => remove(core, &args).await,
        cmd::WORK_GROUP_LIST => list(core, &args).await,
        cmd::WORK_GROUP_GET => get(core, &args).await,
        cmd::WORK_COMPARE => compare(core, &args).await,
        cmd::WORK_BATCH_PREVIEW => preview(core, &args).await,
        cmd::WORK_BATCH_APPLY => apply(core, &args).await,
        other => Err(unsupported(other)),
    }
}

async fn create(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let title = need_str(args, "title")?;
    let description = args["description"].as_str().unwrap_or("").to_owned();
    let id = format!("wg-{}", uuid::Uuid::new_v4());
    let new_id = id.clone();
    let now = crate::jobs::now();
    core.db
        .write(move |tx| {
            store::work::create(tx, &new_id, &title, &description, &now)?;
            store::management::record(
                tx,
                "work_group_create",
                &new_id,
                &json!({"title": title}),
                &now,
            )?;
            crate::objects::outbox_emit(tx, "work.changed", &new_id, &json!({}))?;
            Ok(())
        })
        .await?;
    Ok(json!({"group_id": id}))
}

async fn update(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let id = need_str(args, "group_id")?;
    let title = args["title"].as_str().map(str::to_owned);
    let description = args["description"].as_str().map(str::to_owned);
    let now = crate::jobs::now();
    core.db
        .write(move |tx| {
            store::work::update(tx, &id, title.as_deref(), description.as_deref(), &now)?;
            crate::objects::outbox_emit(tx, "work.changed", &id, &json!({}))?;
            Ok(())
        })
        .await?;
    Ok(json!({"updated": true}))
}

async fn delete(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let id = need_str(args, "group_id")?;
    let now = crate::jobs::now();
    let members = core
        .db
        .write(move |tx| {
            let members = store::work::delete(tx, &id)?;
            store::management::record(
                tx,
                "work_group_delete",
                &id,
                &json!({"members_unlinked": members, "entries_deleted": 0}),
                &now,
            )?;
            crate::objects::outbox_emit(tx, "work.changed", &id, &json!({}))?;
            Ok(members)
        })
        .await?;
    Ok(json!({"deleted": true, "members_unlinked": members, "entries_deleted": 0}))
}

fn entry_ids(args: &Value) -> Result<Vec<String>, CoreError> {
    let entries: Vec<String> = args["entry_ids"]
        .as_array()
        .map(|values| {
            values
                .iter()
                .filter_map(Value::as_str)
                .take(MAX_ENTRIES)
                .map(str::to_owned)
                .collect()
        })
        .ok_or_else(|| CoreError::InvalidInput("entry_ids".into()))?;
    if entries.is_empty() {
        return Err(CoreError::InvalidInput("select at least one entry".into()));
    }
    Ok(entries)
}

async fn add(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let id = need_str(args, "group_id")?;
    let entries = entry_ids(args)?;
    let now = crate::jobs::now();
    let added = core
        .db
        .write(move |tx| {
            let added = store::work::add_members(tx, &id, &entries, &now)?;
            store::management::record(
                tx,
                "work_group_add",
                &id,
                &json!({"added": added, "requested": entries.len()}),
                &now,
            )?;
            crate::objects::outbox_emit(tx, "work.changed", &id, &json!({}))?;
            Ok(added)
        })
        .await?;
    Ok(json!({"added": added}))
}

async fn remove(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let id = need_str(args, "group_id")?;
    let entries = entry_ids(args)?;
    let now = crate::jobs::now();
    let removed = core
        .db
        .write(move |tx| {
            let removed = store::work::remove_members(tx, &id, &entries)?;
            store::management::record(
                tx,
                "work_group_remove",
                &id,
                &json!({"removed": removed}),
                &now,
            )?;
            crate::objects::outbox_emit(tx, "work.changed", &id, &json!({}))?;
            Ok(removed)
        })
        .await?;
    Ok(json!({"removed": removed}))
}

async fn list(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let after = args["after"].as_str().map(str::to_owned);
    let limit = args["limit"].as_i64().unwrap_or(50);
    core.db
        .read(move |c| store::work::groups(c, after.as_deref(), limit))
        .await
}

async fn get(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let id = need_str(args, "group_id")?;
    core.db.read(move |c| store::work::group(c, &id)).await
}

async fn compare(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let id = need_str(args, "group_id")?;
    core.db.read(move |c| store::work::compare(c, &id)).await
}

fn validate_actions(actions: &Value) -> Result<Value, CoreError> {
    let object = actions
        .as_object()
        .ok_or_else(|| CoreError::InvalidInput("batch actions".into()))?;
    let allowed = [
        "tags_add",
        "tags_remove",
        "group_add",
        "group_remove",
        "sync_media",
        "reevaluate_quality",
    ];
    if object.keys().any(|key| !allowed.contains(&key.as_str())) {
        return Err(CoreError::InvalidInput(
            "only tags, groups, sync and re-evaluation are batch actions".into(),
        ));
    }
    let mut out = serde_json::Map::new();
    for key in ["tags_add", "tags_remove"] {
        if let Some(values) = object.get(key) {
            let tags = values
                .as_array()
                .ok_or_else(|| CoreError::InvalidInput((*key).to_owned()))?
                .iter()
                .filter_map(Value::as_str)
                .map(str::trim)
                .filter(|tag| !tag.is_empty() && tag.len() <= 128)
                .take(64)
                .map(str::to_owned)
                .collect::<Vec<_>>();
            out.insert(key.into(), json!(tags));
        }
    }
    for key in ["group_add", "group_remove"] {
        if let Some(values) = object.get(key) {
            let groups = values
                .as_array()
                .ok_or_else(|| CoreError::InvalidInput((*key).to_owned()))?
                .iter()
                .filter_map(Value::as_str)
                .map(str::to_owned)
                .take(32)
                .collect::<Vec<_>>();
            out.insert(key.into(), json!(groups));
        }
    }
    for key in ["sync_media", "reevaluate_quality"] {
        if let Some(value) = object.get(key) {
            out.insert(
                key.into(),
                json!(
                    value
                        .as_bool()
                        .ok_or_else(|| CoreError::InvalidInput((*key).to_owned()))?
                ),
            );
        }
    }
    if out.is_empty() {
        return Err(CoreError::InvalidInput("select at least one action".into()));
    }
    Ok(Value::Object(out))
}

async fn preview(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let entries = entry_ids(args)?;
    let actions = validate_actions(&args["actions"])?;
    let now = crate::jobs::now();
    let expires_at = crate::jobs::plus_secs(&now, PREVIEW_TTL_SECONDS);
    let items = core
        .db
        .read({
            let entries = entries.clone();
            let actions = actions.clone();
            move |c| build_preview(c, &entries, &actions)
        })
        .await?;
    let digest = {
        let entries = entries.clone();
        let actions = actions.clone();
        core.db
            .read(move |c| scope_digest(c, &entries, &actions))
            .await?
    };
    let token = format!("batch-{}", uuid::Uuid::new_v4());
    let item_list = items.clone();
    let (t, a, d) = (token.clone(), actions.clone(), digest.clone());
    let (n, e) = (now.clone(), expires_at.clone());
    core.db
        .write(move |tx| {
            store::work::save_preview(tx, &t, &a, &item_list, &d, &n, &e)?;
            store::management::record(
                tx,
                "work_batch_preview",
                &t,
                &json!({"entries": item_list.as_array().map(|a| a.len()).unwrap_or(0)}),
                &n,
            )?;
            Ok(())
        })
        .await?;
    Ok(json!({
        "token": token,
        "expires_at": expires_at,
        "scope_digest": digest,
        "actions": actions,
        "items": items,
    }))
}

fn build_preview(
    c: &rusqlite::Connection,
    entries: &[String],
    actions: &Value,
) -> Result<Value, CoreError> {
    let mut items = Vec::new();
    for entry in entries {
        let found: Option<(String, String)> = c
            .query_row(
                "SELECT e.namespace,COALESCE(r.title,'') FROM entries e LEFT JOIN current_entry_revisions r ON r.entry_id=e.entry_id WHERE e.entry_id=?1 AND e.lifecycle!='deleted'",
                [entry],
                |r| Ok((r.get(0)?, r.get(1)?)),
            )
            .ok();
        let Some((namespace, title)) = found else {
            items.push(json!({"entry_id": entry, "skip": "entry not found or deleted"}));
            continue;
        };
        let mut applied = Vec::new();
        let mut impact = serde_json::Map::new();
        if let Some(tags) = actions["tags_add"].as_array() {
            let mut added = 0;
            for tag in tags {
                let tag = tag.as_str().unwrap_or_default();
                let exists: bool = c.query_row(
                    "SELECT EXISTS(SELECT 1 FROM entry_terms WHERE entry_id=?1 AND kind='tag' AND value=?2)",
                    rusqlite::params![entry, tag],
                    |r| r.get(0),
                )?;
                if !exists {
                    added += 1;
                    applied.push(json!({"action": "tag_add", "value": tag}));
                }
            }
            impact.insert("tags_added".into(), json!(added));
        }
        if let Some(tags) = actions["tags_remove"].as_array() {
            let mut removed = 0;
            for tag in tags {
                let tag = tag.as_str().unwrap_or_default();
                let exists: bool = c.query_row(
                    "SELECT EXISTS(SELECT 1 FROM entry_terms WHERE entry_id=?1 AND kind='tag' AND value=?2)",
                    rusqlite::params![entry, tag],
                    |r| r.get(0),
                )?;
                if exists {
                    removed += 1;
                    applied.push(json!({"action": "tag_remove", "value": tag}));
                }
            }
            impact.insert("tags_removed".into(), json!(removed));
        }
        for key in ["group_add", "group_remove"] {
            if let Some(groups) = actions[key].as_array() {
                let mut changed = 0;
                for group in groups {
                    let group = group.as_str().unwrap_or_default();
                    let exists: bool = c.query_row(
                        "SELECT EXISTS(SELECT 1 FROM work_group_members WHERE group_id=?1 AND entry_id=?2)",
                        rusqlite::params![group, entry],
                        |r| r.get(0),
                    )?;
                    let wanted = key == "group_add";
                    if exists != wanted {
                        changed += 1;
                        applied.push(json!({"action": key, "value": group}));
                    }
                }
                impact.insert(format!("{key}_changed"), json!(changed));
            }
        }
        if actions["sync_media"] == json!(true) {
            let deferred = store::media_sync::deferred_groups(c, entry)?;
            impact.insert("download_groups".into(), json!(deferred.len()));
            impact.insert("download_bytes".into(), Value::Null);
            applied.push(json!({"action": "sync_media", "value": deferred.len()}));
        }
        if actions["reevaluate_quality"] == json!(true) {
            impact.insert("evaluated_entries".into(), json!(1));
            applied.push(json!({"action": "reevaluate_quality", "value": 1}));
        }
        items.push(json!({
            "entry_id": entry,
            "namespace": namespace,
            "title": title,
            "pending": applied,
            "impact": impact,
            "skip": Value::Null,
        }));
    }
    Ok(Value::Array(items))
}

async fn apply(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let token = need_str(args, "token")?;
    let preview = {
        let token = token.clone();
        core.db
            .read(move |c| store::work::preview(c, &token))
            .await?
    };
    if preview["used_at"].is_string() {
        return Err(CoreError::VersionConflict(
            "batch preview was already used".into(),
        ));
    }
    let now = crate::jobs::now();
    if preview["expires_at"]
        .as_str()
        .is_some_and(|at| at < now.as_str())
    {
        return Err(CoreError::VersionConflict("batch preview expired".into()));
    }
    let entries: Vec<String> = preview["items"]
        .as_array()
        .map(|items| {
            items
                .iter()
                .filter(|i| !i["skip"].is_string())
                .filter_map(|i| i["entry_id"].as_str().map(str::to_owned))
                .collect()
        })
        .unwrap_or_default();
    let actions = preview["actions"].clone();
    let digest = {
        let (entries, actions) = (entries.clone(), actions.clone());
        core.db
            .read(move |c| scope_digest(c, &entries, &actions))
            .await?
    };
    if digest != preview["scope_digest"] {
        return Err(CoreError::VersionConflict(
            "the previewed objects changed; preview again".into(),
        ));
    }
    let (t, a) = (token.clone(), actions.clone());
    let recorded = entries.clone();
    core.db
        .write(move |tx| {
            store::work::mark_used(tx, &t, &now)?;
            store::management::record(
                tx,
                "work_batch_apply",
                &t,
                &json!({"entries": recorded.len(), "actions": a}),
                &now,
            )?;
            Ok(())
        })
        .await?;
    let mut results = Vec::new();
    for entry in entries {
        let (e, a) = (entry.clone(), actions.clone());
        let applied = core.db.write(move |tx| apply_entry(tx, &e, &a)).await;
        let mut result = match applied {
            Ok(count) => json!({"entry_id": entry, "ok": true, "applied": count, "error": null}),
            Err(error) => {
                json!({"entry_id": entry, "ok": false, "applied": 0, "error": error.to_string()})
            }
        };
        if result["ok"] == json!(true) && actions["sync_media"] == json!(true) {
            match crate::app::media_sync::sync(core, std::slice::from_ref(&entry)).await {
                Ok(report) => {
                    result["sync"] = serde_json::to_value(report).unwrap_or(Value::Null);
                }
                Err(error) => {
                    result["ok"] = json!(false);
                    result["error"] = json!(error.to_string());
                }
            }
        }
        results.push(result);
    }
    if actions["reevaluate_quality"] == json!(true) {
        core.db
            .write(|tx| crate::quality::schedule(tx, true))
            .await?;
    }
    let failed = results.iter().filter(|r| r["ok"] != json!(true)).count();
    Ok(json!({
        "token": token,
        "results": results,
        "ok": failed == 0,
        "failed": failed,
    }))
}

fn apply_entry(
    tx: &rusqlite::Transaction<'_>,
    entry: &str,
    actions: &Value,
) -> Result<usize, CoreError> {
    let exists: bool = tx.query_row(
        "SELECT EXISTS(SELECT 1 FROM entries WHERE entry_id=?1 AND lifecycle!='deleted')",
        [entry],
        |r| r.get(0),
    )?;
    if !exists {
        return Err(CoreError::NotFound("entry".into()));
    }
    let now = crate::jobs::now();
    let mut applied = 0;
    for tag in actions["tags_add"].as_array().into_iter().flatten() {
        let tag = tag.as_str().unwrap_or_default();
        applied += tx.execute(
            "INSERT OR IGNORE INTO entry_terms(entry_id,kind,value) VALUES(?1,'tag',?2)",
            rusqlite::params![entry, tag],
        )?;
    }
    for tag in actions["tags_remove"].as_array().into_iter().flatten() {
        let tag = tag.as_str().unwrap_or_default();
        applied += tx.execute(
            "DELETE FROM entry_terms WHERE entry_id=?1 AND kind='tag' AND value=?2",
            rusqlite::params![entry, tag],
        )?;
    }
    for group in actions["group_add"].as_array().into_iter().flatten() {
        let group = group.as_str().unwrap_or_default();
        let exists: bool = tx.query_row(
            "SELECT EXISTS(SELECT 1 FROM work_groups WHERE group_id=?1)",
            [group],
            |r| r.get(0),
        )?;
        if !exists {
            return Err(CoreError::NotFound(format!("work group {group}")));
        }
        applied += tx.execute(
            "INSERT OR IGNORE INTO work_group_members(group_id,entry_id,added_at) VALUES(?1,?2,?3)",
            rusqlite::params![group, entry, now],
        )?;
    }
    for group in actions["group_remove"].as_array().into_iter().flatten() {
        let group = group.as_str().unwrap_or_default();
        applied += tx.execute(
            "DELETE FROM work_group_members WHERE group_id=?1 AND entry_id=?2",
            rusqlite::params![group, entry],
        )?;
    }
    if applied > 0 {
        crate::objects::outbox_emit(
            tx,
            "entry.terms_changed",
            entry,
            &json!({"applied": applied}),
        )?;
    }
    Ok(applied)
}
