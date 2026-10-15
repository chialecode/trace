//! Storage usage, export, backup and restore.
use super::{parse, unsupported};
use crate::core::Core;
use crate::error::CoreError;
use crate::jobs;
use serde_json::{Value, json};
use std::path::Path;
use std::sync::Arc;
use trace_contracts::control as ctl;
use trace_contracts::control::Request;
use trace_contracts::control::commands as cmd;

const BACKUP_LIST_MAX: usize = 200;
const MANIFEST_MAX_BYTES: u64 = 1_048_576;

pub async fn route(core: &Arc<Core>, req: &Request) -> Result<Value, CoreError> {
    let args = req.args.clone();
    match req.command.as_str() {
        cmd::STORAGE_USAGE => crate::storage_ops::usage(core).await,
        cmd::EXPORT_START => export(core, parse(args)?).await,
        cmd::BACKUP_START => backup(core, args).await,
        cmd::BACKUP_LIST => backups(core),
        cmd::RESTORE_START => restore(core, parse(args)?).await,
        other => Err(unsupported(other)),
    }
}

/// Queues a long job on the archive; the caller follows it through the job list.
async fn enqueue_archive_job(
    core: &Arc<Core>,
    kind: &str,
    payload: Value,
) -> Result<String, CoreError> {
    let key = format!("{kind}:{}", uuid::Uuid::new_v4());
    jobs::enqueue(&core.db, key, kind, "archive", payload, 1, 1, 2).await
}

async fn export(core: &Arc<Core>, a: ctl::ExportStartArgs) -> Result<Value, CoreError> {
    if a.target_dir.trim().is_empty() {
        return Err(CoreError::InvalidInput("target".into()));
    }
    crate::paths::no_links(Path::new(&a.target_dir))?;
    let job_id = enqueue_archive_job(core, "export", serde_json::to_value(&a)?).await?;
    Ok(json!({"accepted": true, "job_id": job_id, "target": a.target_dir}))
}

/// An empty request backs up everything to the default backup folder.
async fn backup(core: &Arc<Core>, args: Value) -> Result<Value, CoreError> {
    let a: ctl::BackupStartArgs = if args.as_object().is_some_and(|a| a.is_empty()) {
        ctl::BackupStartArgs {
            target_root: default_backup_root(core).to_string_lossy().to_string(),
            include_media: true,
        }
    } else {
        parse(args)?
    };
    if a.target_root.trim().is_empty() {
        return Err(CoreError::InvalidInput("target".into()));
    }
    crate::paths::no_links(Path::new(&a.target_root))?;
    let job_id = enqueue_archive_job(core, "backup", serde_json::to_value(&a)?).await?;
    Ok(json!({"accepted": true, "job_id": job_id, "target": a.target_root}))
}

fn default_backup_root(core: &Core) -> std::path::PathBuf {
    core.profile.data_root.join("state/backups")
}

/// Manifests of the backups in the default folder, newest first.
fn backups(core: &Arc<Core>) -> Result<Value, CoreError> {
    let root = default_backup_root(core);
    crate::paths::no_links(&root)?;
    let mut items = Vec::new();
    if root.exists() {
        for entry in std::fs::read_dir(root)?.take(BACKUP_LIST_MAX) {
            let path = entry?.path();
            crate::paths::no_links(&path)?;
            let manifest = path.join("manifest.json");
            if manifest.is_file()
                && manifest.metadata()?.len() <= MANIFEST_MAX_BYTES
                && let Ok(mut value) = serde_json::from_slice::<Value>(&std::fs::read(manifest)?)
            {
                value["backup_root"] = path.to_string_lossy().to_string().into();
                items.push(value);
            }
        }
    }
    items.sort_by(|a, b| b["created_at"].as_str().cmp(&a["created_at"].as_str()));
    Ok(json!({
        "items": items,
        "next_cursor": null,
        "scope": "local-default-backups-up-to-200",
    }))
}

async fn restore(core: &Arc<Core>, a: ctl::RestoreStartArgs) -> Result<Value, CoreError> {
    crate::storage_ops::restore(core, Path::new(&a.backup_root), Path::new(&a.target_root)).await
}
