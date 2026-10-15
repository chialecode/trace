//! Download folders (docs/design/multi-site-contracts.md §7): readable copies of archived
//! content in folders the user chooses.
//!
//! A download always goes through the library first — the content-addressed store keeps the
//! single verified copy — and is then written to the folder under a readable name. TRACE
//! records every file it wrote and never moves, renames or removes one afterwards; a file that
//! was edited or deleted outside TRACE is marked "changed" or "missing" when checked.
use crate::core::Core;
use crate::error::CoreError;
use crate::store;
use std::path::{Component, Path, PathBuf, Prefix};
use trace_contracts::download::{
    DownloadFolderView, DownloadSettingsArgs, DownloadSettingsView, ExportReport,
};
use trace_contracts::preview::DownloadTarget;

mod check;
pub mod delivery;
mod export;
mod naming;
mod sidecar;
mod sync;
mod write;

pub use check::{check, copies, forget};
pub use sync::sync_to_library;

#[cfg(test)]
#[path = "downloads_tests.rs"]
mod downloads_tests;
#[cfg(test)]
#[path = "downloads_write_tests.rs"]
mod downloads_write_tests;

const SIDECAR_KEY: &str = "download.sidecar";
const MAX_PATH_TEXT: usize = 1024;
/// Entries accepted by one request; larger batches are split by the caller.
pub const MAX_ENTRIES_PER_REQUEST: usize = 200;

/// The folder used when nothing was chosen: `TRACE` inside the system Downloads folder.
pub fn default_dir() -> PathBuf {
    std::env::var_os("USERPROFILE")
        .map(PathBuf::from)
        .map_or_else(
            || std::env::temp_dir().join("TRACE"),
            |home| home.join("Downloads").join("TRACE"),
        )
}

/// A folder path from the UI as a clean absolute path: no relative parts, no device paths.
pub(crate) fn clean_dir(text: &str) -> Result<PathBuf, CoreError> {
    let text = text.trim();
    if text.is_empty() || text.len() > MAX_PATH_TEXT || text.contains('\0') {
        return Err(CoreError::InvalidInput("下载目录路径无效".into()));
    }
    let path = PathBuf::from(text.replace('/', "\\"));
    if !path.is_absolute() {
        return Err(CoreError::InvalidInput("下载目录必须是完整路径".into()));
    }
    let mut clean = PathBuf::new();
    for component in path.components() {
        match component {
            Component::Prefix(p) if matches!(p.kind(), Prefix::Disk(_) | Prefix::UNC(..)) => {
                clean.push(component.as_os_str());
            }
            Component::RootDir => clean.push(component.as_os_str()),
            Component::Normal(name) => clean.push(name),
            Component::CurDir => {}
            _ => {
                return Err(CoreError::InvalidInput(
                    "下载目录不能包含 .. 或设备路径".into(),
                ));
            }
        }
    }
    Ok(clean)
}

fn path_text(path: &Path) -> String {
    path.to_string_lossy().trim_end_matches('\\').to_owned()
}

/// A usable download folder: clean, apart from the library and the data folder.
fn validate_dir(core: &Core, text: &str) -> Result<PathBuf, CoreError> {
    let dir = clean_dir(text)?;
    if crate::library::nested(&dir, &core.objects.archive_root())
        || crate::library::nested(&dir, &core.profile.data_root)
    {
        return Err(CoreError::InvalidInput(
            "下载目录不能与资源库或数据目录重叠".into(),
        ));
    }
    Ok(dir)
}

fn folder_view(folder: &store::downloads::Folder) -> DownloadFolderView {
    let path = PathBuf::from(&folder.path);
    DownloadFolderView {
        location_id: folder.location_id.clone(),
        path: folder.path.clone(),
        is_default: folder.is_default,
        last_used_at: folder.last_used_at.clone(),
        free_bytes: crate::storage_ops::free_bytes(&crate::library::existing_ancestor(&path)),
        exists: path.is_dir(),
    }
}

async fn sidecar_enabled(core: &Core) -> Result<bool, CoreError> {
    let value = core
        .db
        .read(|c| store::downloads::setting(c, SIDECAR_KEY))
        .await?;
    Ok(value.as_deref() != Some("0"))
}

pub async fn settings(core: &Core) -> Result<DownloadSettingsView, CoreError> {
    let (folders, default, sidecar) = core
        .db
        .read(|c| {
            Ok((
                store::downloads::folders(c)?,
                store::downloads::default_folder(c)?,
                store::downloads::setting(c, SIDECAR_KEY)?,
            ))
        })
        .await?;
    Ok(DownloadSettingsView {
        default_dir: default.map_or_else(|| path_text(&default_dir()), |d| d.path),
        recent: folders.iter().map(folder_view).collect(),
        sidecar: sidecar.as_deref() != Some("0"),
    })
}

pub async fn set_settings(
    core: &Core,
    args: DownloadSettingsArgs,
) -> Result<DownloadSettingsView, CoreError> {
    let _run = core.downloads_run.lock().await;
    let dir = args
        .default_dir
        .as_deref()
        .map(|text| validate_dir(core, text))
        .transpose()?;
    if let Some(dir) = &dir {
        // A folder that cannot take a file is refused now, not at the first download.
        let probe = dir.clone();
        tokio::task::spawn_blocking(move || {
            write::probe_writable(&probe)?;
            write::clear_temp(&probe);
            Ok::<_, CoreError>(())
        })
        .await
        .map_err(|_| CoreError::Internal("目录检查异常中止".into()))??;
    }
    let sidecar = args.sidecar;
    core.db
        .write(move |tx| {
            let now = crate::jobs::now();
            if let Some(dir) = dir {
                store::downloads::set_default(tx, &path_text(&dir), &now)?;
            }
            if let Some(on) = sidecar {
                store::downloads::put_setting(tx, SIDECAR_KEY, if on { "1" } else { "0" }, &now)?;
            }
            Ok(())
        })
        .await?;
    settings(core).await
}

/// The folder a request resolves to: the one given, or the default.
async fn resolve_root(core: &Core, dir: Option<&str>) -> Result<PathBuf, CoreError> {
    match dir {
        Some(text) => validate_dir(core, text),
        None => {
            let default = core.db.read(store::downloads::default_folder).await?;
            validate_dir(
                core,
                &default.map_or_else(|| path_text(&default_dir()), |d| d.path),
            )
        }
    }
}

/// Refuses a folder that cannot take a file now. With the run lock held nothing else writes
/// here, so `clear` also removes the leftovers of an interrupted run.
async fn check_root(root: &Path, clear: bool) -> Result<(), CoreError> {
    let probe = root.to_path_buf();
    tokio::task::spawn_blocking(move || {
        write::probe_writable(&probe)?;
        if clear {
            write::clear_temp(&probe);
        } else {
            let _ = std::fs::remove_dir(probe.join(write::TEMP_DIR));
        }
        Ok::<_, CoreError>(())
    })
    .await
    .map_err(|_| CoreError::Internal("目录检查异常中止".into()))?
}

/// The folder for one run, checked and recorded before anything is written.
async fn prepare_folder(
    core: &Core,
    dir: Option<&str>,
) -> Result<(store::downloads::Folder, PathBuf), CoreError> {
    let root = resolve_root(core, dir).await?;
    check_root(&root, true).await?;
    let text = path_text(&root);
    let id = core
        .db
        .write(move |tx| store::downloads::use_folder(tx, &text, &crate::jobs::now()))
        .await?;
    let folder = core
        .db
        .read(move |c| store::downloads::folder(c, &id))
        .await?
        .ok_or_else(|| CoreError::Internal("download folder missing".into()))?;
    Ok((folder, root))
}

/// Checks where a request will write before the request is accepted. A folder that cannot
/// take a file is refused now, not when the content has already been fetched.
pub async fn preflight(core: &Core, target: &DownloadTarget) -> Result<DownloadTarget, CoreError> {
    match (target.kind.as_str(), target.dir.as_deref()) {
        ("library", None) => Ok(DownloadTarget::library()),
        ("library", Some(_)) => Err(CoreError::InvalidInput(
            "下载到资源库时不需要选择目录".into(),
        )),
        ("folder", dir) => {
            let _run = core.downloads_run.lock().await;
            let root = resolve_root(core, dir).await?;
            check_root(&root, false).await?;
            Ok(DownloadTarget {
                kind: "folder".into(),
                dir: dir.map(|_| path_text(&root)),
            })
        }
        _ => Err(CoreError::InvalidInput("下载位置无效".into())),
    }
}

/// What the user can act on in an error, without the internal prefix.
pub(crate) fn user_message(e: &CoreError) -> String {
    match e {
        CoreError::InvalidInput(m)
        | CoreError::StorageFull(m)
        | CoreError::NotFound(m)
        | CoreError::Unsupported(m) => m.clone(),
        _ => "内部错误，详见日志".into(),
    }
}

/// Writes readable copies of the given entries to a download folder (the default one when
/// `dir` is absent). A folder that cannot take files fails the whole call before anything is
/// written; an entry that cannot be written fails alone and is reported.
pub async fn export_entries(
    core: &Core,
    entry_ids: &[String],
    dir: Option<&str>,
) -> Result<ExportReport, CoreError> {
    if entry_ids.is_empty() || entry_ids.len() > MAX_ENTRIES_PER_REQUEST {
        return Err(CoreError::InvalidInput(format!(
            "一次请求需要 1 到 {MAX_ENTRIES_PER_REQUEST} 个条目"
        )));
    }
    let _run = core.downloads_run.lock().await;
    let (folder, root) = prepare_folder(core, dir).await?;
    let sidecar = sidecar_enabled(core).await?;
    let mut seen = std::collections::HashSet::new();
    let mut items = Vec::new();
    for id in entry_ids.iter().filter(|id| seen.insert(id.as_str())) {
        items.push(
            match export::export_entry(core, &folder.location_id, &root, id, sidecar).await {
                Ok(item) => item,
                Err(e) => export::failed(id, &e),
            },
        );
    }
    let _ = std::fs::remove_dir(root.join(write::TEMP_DIR));
    let folder = core
        .db
        .read({
            let id = folder.location_id.clone();
            move |c| store::downloads::folder(c, &id)
        })
        .await?
        .unwrap_or(folder);
    Ok(ExportReport {
        folder: folder_view(&folder),
        items,
    })
}
