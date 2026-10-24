//! The download-folder side of the explorer: the real folders below the places the user
//! chose, TRACE's records of what it wrote there, and the two actions that reach the system
//! (open, show in the system explorer).
//!
//! A path is never taken from the interface. It arrives as a folder id plus clean names, is
//! built below the folder's root, and every part that exists is checked not to be a link or a
//! junction that could lead out of the root.
use super::node::{Node, clean_name, clean_rel};
use super::{Filters, Sorting, kind_of_extension, sort_rows};
use crate::app::media::launch_explorer;
use crate::core::Core;
use crate::error::CoreError;
use crate::store;
use crate::store::downloads::Folder;
use crate::store::explorer::CopyRow;
use std::collections::HashMap;
use std::path::{Path, PathBuf};
use trace_contracts::explorer::{
    ExplorerNode, ExplorerRow, FileArgs, ListArgs, ListView, TreeView,
};

/// Entries one folder listing reads; a larger folder is shown truncated.
pub const FOLDER_ENTRIES_MAX: usize = 20_000;
/// Child folders one tree answer carries.
const SUBFOLDERS_MAX: usize = 2000;
/// TRACE's own staging folder inside a download folder; never shown.
const STAGING: &str = ".trace-tmp";

/// Files that are opened with the system's default program. Anything else — programs,
/// scripts, shortcuts, installers — is only ever shown, never started.
const OPENABLE: [&str; 25] = [
    "jpg", "jpeg", "png", "webp", "gif", "bmp", "mp4", "m4v", "mov", "webm", "mkv", "flv", "ts",
    "m4a", "mp3", "aac", "flac", "wav", "ogg", "opus", "txt", "md", "json", "srt", "vtt",
];

fn extension(name: &str) -> String {
    name.rsplit_once('.')
        .map(|(_, e)| e.to_ascii_lowercase())
        .unwrap_or_default()
}

fn openable(name: &str) -> bool {
    OPENABLE.contains(&extension(name).as_str())
}

fn is_reparse(meta: &std::fs::Metadata) -> bool {
    #[cfg(windows)]
    {
        use std::os::windows::fs::MetadataExt;
        const REPARSE_POINT: u32 = 0x400;
        meta.file_attributes() & REPARSE_POINT != 0
    }
    #[cfg(not(windows))]
    {
        meta.file_type().is_symlink()
    }
}

/// `root` plus `rel`, when no existing part of it is a link or junction. A part that does not
/// exist yet ends the check; the caller decides what a missing path means.
fn resolve(root: &Path, rel: &[String]) -> Result<PathBuf, CoreError> {
    let mut path = root.to_path_buf();
    for name in rel {
        clean_name(name)?;
        path.push(name);
        match std::fs::symlink_metadata(&path) {
            Ok(meta) if is_reparse(&meta) => {
                return Err(CoreError::InvalidInput(
                    "link inside a download folder".into(),
                ));
            }
            Ok(_) => {}
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
            Err(e) => return Err(e.into()),
        }
    }
    Ok(path)
}

async fn folder_of(core: &Core, location: &str) -> Result<Folder, CoreError> {
    let id = location.to_owned();
    core.db
        .read(move |c| store::downloads::folder(c, &id))
        .await?
        .ok_or_else(|| CoreError::NotFound("download folder".into()))
}

fn node_of(location: &str, rel: &[String], name: &str) -> String {
    Node::child_folder(location, rel, name).text()
}

pub(super) async fn tree_downloads(core: &Core) -> Result<TreeView, CoreError> {
    let folders = core.db.read(store::downloads::folders).await?;
    Ok(TreeView {
        nodes: folders
            .into_iter()
            .map(|f| ExplorerNode {
                node: Node::Folder {
                    location: f.location_id,
                    rel: Vec::new(),
                }
                .text(),
                name: f.path,
                kind: "folder".into(),
                count: None,
                expandable: true,
            })
            .collect(),
        truncated: false,
    })
}

/// What a read of one real folder found.
struct Listing {
    folders: Vec<String>,
    files: Vec<(String, u64)>,
    truncated: bool,
}

fn read_folder(dir: &Path) -> Result<Listing, CoreError> {
    let mut listing = Listing {
        folders: Vec::new(),
        files: Vec::new(),
        truncated: false,
    };
    let reader = match std::fs::read_dir(dir) {
        Ok(reader) => reader,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
            return Err(CoreError::NotFound("folder".into()));
        }
        Err(e) => return Err(e.into()),
    };
    for (seen, item) in reader.enumerate() {
        if seen >= FOLDER_ENTRIES_MAX {
            listing.truncated = true;
            break;
        }
        let Ok(item) = item else { continue };
        let Ok(name) = item.file_name().into_string() else {
            continue;
        };
        if name == STAGING || clean_name(&name).is_err() {
            continue;
        }
        let Ok(meta) = std::fs::symlink_metadata(item.path()) else {
            continue;
        };
        if is_reparse(&meta) {
            continue;
        }
        if meta.is_dir() {
            listing.folders.push(name);
        } else if meta.is_file() {
            listing.files.push((name, meta.len()));
        }
    }
    Ok(listing)
}

pub(super) async fn tree_folder(
    core: &Core,
    location: &str,
    rel: &[String],
) -> Result<TreeView, CoreError> {
    let folder = folder_of(core, location).await?;
    let dir = resolve(Path::new(&folder.path), rel)?;
    let listing = tokio::task::spawn_blocking(move || read_folder(&dir))
        .await
        .map_err(|_| CoreError::Internal("folder listing".into()))??;
    let mut names = listing.folders;
    names.sort_by_key(|n| n.to_lowercase());
    let truncated = listing.truncated || names.len() > SUBFOLDERS_MAX;
    names.truncate(SUBFOLDERS_MAX);
    Ok(TreeView {
        nodes: names
            .into_iter()
            .map(|name| ExplorerNode {
                node: node_of(location, rel, &name),
                name,
                kind: "folder".into(),
                count: None,
                expandable: true,
            })
            .collect(),
        truncated,
    })
}

pub(super) async fn list_downloads(core: &Core) -> Result<ListView, CoreError> {
    let folders = core.db.read(store::downloads::folders).await?;
    let rows: Vec<ExplorerRow> = folders
        .into_iter()
        .map(|f| {
            let exists = Path::new(&f.path).is_dir();
            ExplorerRow {
                key: format!("folder:{}", f.location_id),
                name: f.path.clone(),
                kind: "folder".into(),
                kinds: Vec::new(),
                size: None,
                duration_ms: None,
                width: None,
                height: None,
                published_at: None,
                site: None,
                author: None,
                state: if exists { "present" } else { "missing" }.into(),
                entry_id: None,
                blob_id: None,
                location_id: Some(f.location_id.clone()),
                rel_path: Some(String::new()),
                path: f.path,
                node: Some(
                    Node::Folder {
                        location: f.location_id,
                        rel: Vec::new(),
                    }
                    .text(),
                ),
                can_open: false,
                can_reveal: exists,
                can_sync: false,
                can_forget: false,
            }
        })
        .collect();
    let total = rows.len() as i64;
    Ok(ListView {
        path: "下载位置".into(),
        rows,
        total,
        offset: 0,
        truncated: false,
    })
}

fn join_rel(rel: &[String], name: &str) -> String {
    rel.iter()
        .map(String::as_str)
        .chain(std::iter::once(name))
        .collect::<Vec<_>>()
        .join("\\")
}

fn folder_row(folder: &Folder, rel: &[String], name: &str) -> ExplorerRow {
    ExplorerRow {
        key: format!("dir:{}", join_rel(rel, name)),
        name: name.to_owned(),
        kind: "folder".into(),
        kinds: Vec::new(),
        size: None,
        duration_ms: None,
        width: None,
        height: None,
        published_at: None,
        site: None,
        author: None,
        state: "present".into(),
        entry_id: None,
        blob_id: None,
        location_id: Some(folder.location_id.clone()),
        rel_path: Some(join_rel(rel, name)),
        path: Path::new(&folder.path)
            .join(join_rel(rel, name))
            .to_string_lossy()
            .into_owned(),
        node: Some(node_of(&folder.location_id, rel, name)),
        can_open: false,
        can_reveal: true,
        can_sync: false,
        can_forget: false,
    }
}

fn file_row(
    folder: &Folder,
    rel: &[String],
    name: &str,
    size: Option<u64>,
    record: Option<&CopyRow>,
) -> ExplorerRow {
    let rel_path = join_rel(rel, name);
    let exists = size.is_some();
    let site = record
        .and_then(|r| r.site.as_deref())
        .map(|s| trace_domain::sites::site(s).map_or_else(|| s.to_owned(), |x| x.title.to_owned()));
    // The recorded state is the last full check; a length that differs now is visible at once.
    let state = record.map_or("untracked", |r| match (size, r.state.as_str()) {
        (None, _) => "missing",
        (Some(len), _) if r.size >= 0 && len != r.size as u64 => "changed",
        (Some(_), "missing") => "present",
        (Some(_), state @ ("present" | "changed" | "writing")) => state,
        (Some(_), _) => "changed",
    });
    ExplorerRow {
        key: format!("file:{rel_path}"),
        name: name.to_owned(),
        kind: kind_of_extension(&extension(name)).into(),
        kinds: Vec::new(),
        size: size.map(|s| s as i64).or_else(|| record.map(|r| r.size)),
        duration_ms: record.and_then(|r| r.duration_ms),
        width: record.and_then(|r| r.width),
        height: record.and_then(|r| r.height),
        published_at: record.and_then(|r| r.published_at.clone()),
        site,
        author: record.and_then(|r| r.author.clone()),
        state: state.into(),
        entry_id: record.and_then(|r| r.entry_id.clone()),
        blob_id: None,
        location_id: Some(folder.location_id.clone()),
        path: Path::new(&folder.path)
            .join(&rel_path)
            .to_string_lossy()
            .into_owned(),
        rel_path: Some(rel_path),
        node: None,
        can_open: exists && openable(name),
        can_reveal: exists,
        can_sync: exists && state == "present" && record.is_some_and(|r| r.entry_id.is_some()),
        can_forget: record.is_some(),
    }
}

/// Direct children of a download folder: real subfolders and files, with what TRACE recorded
/// about each file it wrote, and records whose file is gone.
pub(super) async fn list_folder(
    core: &Core,
    location: &str,
    rel: &[String],
    args: &ListArgs,
    sorting: &Sorting,
    filters: &Filters,
) -> Result<ListView, CoreError> {
    let folder = folder_of(core, location).await?;
    let dir = resolve(Path::new(&folder.path), rel)?;
    let prefix = if rel.is_empty() {
        String::new()
    } else {
        format!("{}\\", rel.join("\\"))
    };
    let (id, p) = (folder.location_id.clone(), prefix.clone());
    let records = core
        .db
        .read(move |c| store::explorer::copies_below(c, &id, &p))
        .await?;
    let listing = tokio::task::spawn_blocking(move || read_folder(&dir))
        .await
        .map_err(|_| CoreError::Internal("folder listing".into()))??;
    let mut by_name: HashMap<String, &CopyRow> = HashMap::new();
    for record in &records {
        let name = &record.rel_path[prefix.len().min(record.rel_path.len())..];
        // A tampered record must never become a row that points outside the folder.
        if clean_rel(&record.rel_path).is_ok() && !name.contains('\\') {
            by_name.insert(name.to_lowercase(), record);
        }
    }
    let mut rows: Vec<ExplorerRow> = listing
        .folders
        .iter()
        .map(|name| folder_row(&folder, rel, name))
        .collect();
    let mut seen = std::collections::HashSet::new();
    for (name, size) in &listing.files {
        let key = name.to_lowercase();
        seen.insert(key.clone());
        rows.push(file_row(
            &folder,
            rel,
            name,
            Some(*size),
            by_name.get(&key).copied(),
        ));
    }
    for (key, record) in &by_name {
        if !seen.contains(key) {
            let name = &record.rel_path[prefix.len()..];
            rows.push(file_row(&folder, rel, name, None, Some(record)));
        }
    }
    rows.retain(|r| filters.accepts(r));
    sort_rows(&mut rows, &sorting.column, args.descending);
    let total = rows.len() as i64;
    let page = rows
        .into_iter()
        .skip(sorting.offset.max(0) as usize)
        .take(sorting.limit.max(1) as usize)
        .collect();
    let mut path = folder.path.clone();
    if !rel.is_empty() {
        path = Path::new(&path)
            .join(rel.join("\\"))
            .to_string_lossy()
            .into_owned();
    }
    Ok(ListView {
        path,
        rows: page,
        total,
        offset: sorting.offset,
        truncated: listing.truncated,
    })
}

/// The path without the extended-length prefix that canonicalizing adds on Windows; the
/// system's file explorer does not take it everywhere.
fn plain(path: PathBuf) -> PathBuf {
    let text = path.to_string_lossy();
    match text.strip_prefix(r"\?\") {
        Some(rest) if rest.as_bytes().get(1) == Some(&b':') => PathBuf::from(rest),
        _ => path,
    }
}

/// The existing file or folder a request names.
async fn target(core: &Core, args: &FileArgs) -> Result<(PathBuf, bool), CoreError> {
    let rel = clean_rel(&args.rel_path)?;
    let folder = folder_of(core, &args.location_id).await?;
    let path = resolve(Path::new(&folder.path), &rel)?;
    let meta = std::fs::symlink_metadata(&path)
        .map_err(|_| CoreError::NotFound("file is not there any more".into()))?;
    if is_reparse(&meta) {
        return Err(CoreError::InvalidInput(
            "link inside a download folder".into(),
        ));
    }
    // Resolve once more on the real path: the folder itself may sit behind a junction the
    // user made on purpose, but nothing below it may lead out.
    let root = std::fs::canonicalize(&folder.path)?;
    let real = std::fs::canonicalize(&path)?;
    if !real.starts_with(&root) {
        return Err(CoreError::InvalidInput(
            "outside the download folder".into(),
        ));
    }
    Ok((plain(real), meta.is_dir()))
}

/// Opens a file of a download folder with the default program — only types that are
/// content, never something the system would run.
pub async fn open(core: &Core, args: FileArgs) -> Result<serde_json::Value, CoreError> {
    let (path, is_dir) = target(core, &args).await?;
    let name = path
        .file_name()
        .and_then(|n| n.to_str())
        .unwrap_or_default()
        .to_owned();
    if is_dir || !openable(&name) {
        return Err(CoreError::Unsupported(
            "这种文件不会被直接打开，请在系统资源管理器中显示".into(),
        ));
    }
    launch_explorer(path.as_os_str())?;
    Ok(serde_json::json!({"opened": true}))
}

/// Shows a file or folder of a download folder in the system's file explorer.
pub async fn reveal(core: &Core, args: FileArgs) -> Result<serde_json::Value, CoreError> {
    let (path, is_dir) = target(core, &args).await?;
    crate::app::media::reveal_in_explorer(path.as_os_str(), is_dir)?;
    Ok(serde_json::json!({"revealed": true}))
}
