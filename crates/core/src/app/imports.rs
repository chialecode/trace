//! Local import commands: preview, start, status, cancel, resume, assign, external check.
use super::{need_str, unsupported};
use crate::core::Core;
use crate::error::CoreError;
use crate::store::imports::{NewBatch, NewItem};
use crate::{jobs, store};
use serde_json::{Value, json};
use std::path::Path;
use std::sync::Arc;
use trace_contracts::control::Request;
use trace_contracts::control::commands as cmd;

pub async fn route(core: &Arc<Core>, req: &Request) -> Result<Value, CoreError> {
    let args = req.args.clone();
    match req.command.as_str() {
        cmd::IMPORT_INSPECT => inspect(core, &args).await,
        cmd::IMPORT_START => start(core, &args).await,
        cmd::IMPORT_STATUS => status(core, &args).await,
        cmd::IMPORT_CANCEL => cancel(core, &args).await,
        cmd::IMPORT_RESUME => resume(core, &args).await,
        cmd::IMPORT_ASSIGN => assign(core, &args).await,
        cmd::IMPORT_CHECK => check(core, &args).await,
        other => Err(unsupported(other)),
    }
}

fn follow_links(args: &Value) -> bool {
    args["follow_links"].as_bool().unwrap_or(false)
}

async fn inspect(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let path = need_str(args, "path")?;
    let mode = args["mode"]
        .as_str()
        .filter(|m| matches!(*m, "managed" | "in_place"))
        .unwrap_or("managed");
    let target = args["target_creator_id"].as_str();
    let (root, scan) = crate::imports::scan(&path, follow_links(args))?;
    let mut value = crate::imports::inspect_value(&root, &scan);
    // A path already recorded by an earlier committed import is a duplicate candidate; the
    // preview names it without hashing anything.
    let known = core.db.read(store::imports::known_paths).await?;
    let existing: std::collections::HashMap<String, i64> = known.into_iter().collect();
    let duplicates: Vec<Value> = scan
        .files
        .iter()
        .filter(|f| {
            existing
                .get(&f.path.to_string_lossy().to_string())
                .is_some_and(|size| *size == f.size)
        })
        .take(100)
        .map(|f| json!({"rel_path": f.rel, "reason": "already imported"}))
        .collect();
    value["duplicates"] = Value::Array(duplicates);
    value["preview_token"] = json!(crate::imports::preview_token(&root, &scan, mode, target));
    value["mode"] = json!(mode);
    value["target_creator_id"] = json!(target);
    Ok(value)
}

async fn start(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let path = need_str(args, "path")?;
    let mode = args["mode"]
        .as_str()
        .filter(|m| matches!(*m, "managed" | "in_place"))
        .ok_or_else(|| CoreError::InvalidInput("import mode".into()))?
        .to_owned();
    let target = args["target_creator_id"].as_str().map(str::to_owned);
    let preview_token = need_str(args, "preview_token")?;
    if let Some(creator) = &target {
        let exists: bool = core
            .db
            .read({
                let creator = creator.clone();
                move |c| {
                    Ok(c.query_row(
                        "SELECT EXISTS(SELECT 1 FROM creators WHERE creator_id=?1 AND status='active')",
                        [creator],
                        |r| r.get(0),
                    )?)
                }
            })
            .await?;
        if !exists {
            return Err(CoreError::InvalidInput(
                "import creator is not active".into(),
            ));
        }
    }
    let (root, scan) = crate::imports::scan(&path, follow_links(args))?;
    // Core scans the files again: a path, file, mode or target that differs from what was
    // previewed cannot start with the old confirmation.
    let actual = crate::imports::preview_token(&root, &scan, &mode, target.as_deref());
    if actual != preview_token {
        return Err(CoreError::VersionConflict(
            "导入预览已过期或与当前路径、方式、目标不一致，请重新预览".into(),
        ));
    }
    if scan.files.is_empty() {
        return Err(CoreError::InvalidInput(
            "the path has no importable file (empty, links only or unreadable)".into(),
        ));
    }
    let batch_id = format!("imp-{}", uuid::Uuid::new_v4());
    let source = root.to_string_lossy().to_string();
    let creator = target.unwrap_or_else(|| crate::imports::INBOX_CREATOR.to_owned());
    let now = jobs::now();
    let (id, files) = (batch_id.clone(), scan.files.clone());
    core.db
        .write(move |tx| {
            let account = crate::imports::ensure_account(tx, &id, &creator, &now)?;
            store::imports::create_batch(
                tx,
                &NewBatch {
                    batch_id: &id,
                    mode: &mode,
                    source_path: &source,
                    target_creator_id: Some(&creator),
                    account_id: Some(&account),
                    now: &now,
                },
            )?;
            let mut failed = 0i64;
            for (position, file) in files.iter().enumerate() {
                let item_id = format!("item-{}", uuid::Uuid::new_v4());
                store::imports::insert_item(
                    tx,
                    &NewItem {
                        item_id: &item_id,
                        batch_id: &id,
                        position: position as i64,
                        source_path: &file.path.to_string_lossy(),
                        rel_path: &file.rel,
                        size: file.size,
                        mtime: file.mtime.as_deref(),
                        now: &now,
                    },
                )?;
                if let Some(error) = &file.error {
                    failed += 1;
                    store::imports::set_item_state(
                        tx,
                        &item_id,
                        "failed",
                        Some(error),
                        None,
                        None,
                        None,
                        None,
                        &now,
                    )?;
                }
            }
            if failed > 0 {
                store::imports::bump_batch(tx, &id, 0, failed, 0, 0, &now)?;
            }
            store::management::record(
                tx,
                "import_start",
                &id,
                &json!({"mode": mode, "files": files.len(), "bytes": files.iter().map(|f| f.size).sum::<i64>()}),
                &now,
            )?;
            Ok(())
        })
        .await?;
    jobs::enqueue(
        &core.db,
        format!("import:{batch_id}"),
        "import",
        &batch_id,
        json!({"batch_id": batch_id}),
        2,
        1,
        0,
    )
    .await?;
    let id = batch_id.clone();
    core.db.read(move |c| store::imports::batch(c, &id)).await
}

async fn status(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let batch = args["batch_id"].as_str().map(str::to_owned);
    let after = args["after"].as_i64().unwrap_or(-1);
    let limit = args["limit"].as_i64().unwrap_or(50);
    match batch {
        Some(id) => {
            let batch = {
                let id = id.clone();
                core.db.read(move |c| store::imports::batch(c, &id)).await?
            };
            let items = core
                .db
                .read(move |c| store::imports::items(c, &id, after, limit))
                .await?;
            Ok(json!({"batch": batch, "items": items["items"], "next_after": items["next_after"]}))
        }
        None => {
            let batches = core
                .db
                .read(move |c| store::imports::batches(c, None, 20))
                .await?;
            let latest = batches["items"][0]["batch_id"].as_str().map(str::to_owned);
            let detail = match latest {
                Some(id) => {
                    let batch = {
                        let id = id.clone();
                        core.db.read(move |c| store::imports::batch(c, &id)).await?
                    };
                    let items = core
                        .db
                        .read(move |c| store::imports::items(c, &id, after, limit))
                        .await?;
                    json!({"batch": batch, "items": items["items"], "next_after": items["next_after"]})
                }
                None => json!({"batch": null, "items": []}),
            };
            Ok(json!({"batches": batches["items"], "latest": detail}))
        }
    }
}

async fn cancel(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let batch = need_str(args, "batch_id")?;
    let now = jobs::now();
    let changed = core
        .db
        .write(move |tx| {
            let state: Option<String> = tx
                .query_row(
                    "SELECT state FROM import_batches WHERE batch_id=?1",
                    [&batch],
                    |r| r.get(0),
                )
                .ok();
            let Some(state) = state else {
                return Err(CoreError::NotFound("import batch".into()));
            };
            if !matches!(state.as_str(), "pending" | "running") {
                return Ok(false);
            }
            store::imports::set_batch_state(tx, &batch, "cancelled", None, &now)?;
            tx.execute(
                "UPDATE import_items SET state='cancelled',updated_at=?2 WHERE batch_id=?1 AND state IN ('pending','copying','hashing')",
                rusqlite::params![batch, now],
            )?;
            // The queued job stops before its next file; a running one sees the batch state.
            tx.execute(
                "UPDATE jobs SET state='cancelled',target_generation=target_generation+1,lease_owner=NULL,lease_expires_at=NULL,updated_at=?2 WHERE job_key=?1 AND state NOT IN ('succeeded','failed','cancelled')",
                rusqlite::params![format!("import:{batch}"), now],
            )?;
            crate::objects::outbox_emit(tx, "import.changed", &batch, &json!({"state": "cancelled"}))?;
            Ok(true)
        })
        .await?;
    Ok(json!({"cancelled": changed}))
}

async fn resume(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let batch = need_str(args, "batch_id")?;
    let now = jobs::now();
    let id = batch.clone();
    let reset = core
        .db
        .write(move |tx| {
            let state: Option<String> = tx
                .query_row(
                    "SELECT state FROM import_batches WHERE batch_id=?1",
                    [&id],
                    |r| r.get(0),
                )
                .ok();
            let Some(state) = state else {
                return Err(CoreError::NotFound("import batch".into()));
            };
            if !matches!(state.as_str(), "cancelled" | "failed") {
                return Err(CoreError::VersionConflict(
                    "only a cancelled or failed import can resume".into(),
                ));
            }
            let reset = store::imports::resume(tx, &id, &now)?;
            tx.execute(
                "UPDATE jobs SET state='queued',target_generation=target_generation+1,lease_owner=NULL,lease_expires_at=NULL,next_run_at=?2,updated_at=?2,last_error=NULL WHERE job_key=?1",
                rusqlite::params![format!("import:{id}"), now],
            )?;
            crate::objects::outbox_emit(tx, "import.changed", &id, &json!({"state": "pending"}))?;
            Ok(reset)
        })
        .await?;
    Ok(json!({"resumed": true, "items_reset": reset}))
}

async fn assign(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let batch = need_str(args, "batch_id")?;
    let creator = need_str(args, "target_creator_id")?;
    let selected: Option<Vec<String>> = args["item_ids"].as_array().map(|values| {
        values
            .iter()
            .filter_map(Value::as_str)
            .take(MAX_ASSIGN)
            .map(str::to_owned)
            .collect()
    });
    let now = jobs::now();
    let (id, creator_id) = (batch.clone(), creator.clone());
    core.db
        .write(move |tx| {
            let active: bool = tx.query_row(
                "SELECT EXISTS(SELECT 1 FROM creators WHERE creator_id=?1 AND status='active')",
                [&creator_id],
                |r| r.get(0),
            )?;
            if !active {
                return Err(CoreError::InvalidInput(
                    "target creator is not active".into(),
                ));
            }
            let items = store::imports::committed_items(tx, &id)?;
            let account = crate::imports::account_for_creator(tx, &id, &creator_id, &now)?;
            let mut moved = 0usize;
            for item in items {
                if selected
                    .as_ref()
                    .is_some_and(|ids| !ids.contains(&item.item_id))
                {
                    continue;
                }
                let Some(entry) = item.entry_id else {
                    continue;
                };
                tx.execute(
                    "UPDATE entries SET account_id=?2 WHERE entry_id=?1",
                    rusqlite::params![entry, account],
                )?;
                store::imports::set_item_entry(tx, &item.item_id, &entry)?;
                moved += 1;
            }
            tx.execute(
                "UPDATE import_batches SET target_creator_id=?2,updated_at=?3 WHERE batch_id=?1",
                rusqlite::params![id, creator_id, now],
            )?;
            store::management::record(
                tx,
                "import_assign",
                &id,
                &json!({"creator_id": creator_id, "items": moved}),
                &now,
            )?;
            crate::objects::outbox_emit(
                tx,
                "import.changed",
                &id,
                &json!({"assigned": moved, "creator_id": creator_id}),
            )?;
            Ok(moved)
        })
        .await
        .map(|moved| json!({"assigned": moved}))
}

const MAX_ASSIGN: usize = 5000;

async fn check(core: &Arc<Core>, args: &Value) -> Result<Value, CoreError> {
    let batch = args["batch_id"].as_str().map(str::to_owned);
    let items = core
        .db
        .read(move |c| store::imports::external_items(c, batch.as_deref()))
        .await?;
    let mut changed = 0i64;
    let mut offline = 0i64;
    let mut unchanged = 0i64;
    for item in items {
        let path = Path::new(&item.source_path);
        let observation = std::fs::metadata(path).ok().map(|meta| {
            (
                i64::try_from(meta.len()).unwrap_or(i64::MAX),
                meta.modified().ok().map(|at| {
                    time::OffsetDateTime::from(at)
                        .format(&time::format_description::well_known::Rfc3339)
                        .unwrap_or_default()
                }),
            )
        });
        let state = match &observation {
            None => "offline",
            Some((size, mtime)) => {
                if *size == item.size && mtime.as_deref() == item.mtime.as_deref() {
                    "external"
                } else {
                    "changed"
                }
            }
        };
        match state {
            "offline" => offline += 1,
            "external" => unchanged += 1,
            _ => changed += 1,
        }
        let now = jobs::now();
        let source = item.source_path.clone();
        let blob = item.blob_id.clone();
        let item_id = item.item_id.clone();
        let size = observation.as_ref().map(|o| o.0);
        let mtime = observation.as_ref().and_then(|o| o.1.clone());
        core.db
            .write(move |tx| {
                if let Some(blob) = blob {
                    let target = if state == "external" { "external" } else { state };
                    tx.execute(
                        "UPDATE replicas SET state=?3 WHERE blob_id=?1 AND store_id='external' AND locator=?2",
                        rusqlite::params![blob, source, target],
                    )?;
                }
                store::imports::record_external_observation(
                    tx,
                    &item_id,
                    size,
                    mtime.as_deref(),
                    &now,
                )?;
                Ok(())
            })
            .await?;
    }
    Ok(json!({
        "checked": unchanged + changed + offline,
        "unchanged": unchanged,
        "changed": changed,
        "offline": offline,
    }))
}
