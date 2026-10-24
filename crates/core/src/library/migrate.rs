//! The move: plan, copy, verify, switch. Every step is recorded per file, so an interrupted
//! move continues where it stopped; nothing is switched until every file is verified, and
//! the switch itself is one database commit.
use super::{OWNED_DIRS, existing_ancestor, status};
use crate::core::Core;
use crate::error::CoreError;
use crate::store;
use crate::store::library::{Item, Migration};
use sha2::{Digest, Sha256};
use std::io::{Read, Write};
use std::path::{Component, Path, PathBuf};
use std::sync::Arc;
use std::sync::atomic::Ordering;
use std::time::Duration;
use trace_contracts::library::LibraryStatus;

const BATCH_ITEMS: i64 = 64;
const BATCH_BYTES: i64 = 256 * 1024 * 1024;
const COPY_BUFFER: usize = 1024 * 1024;
const IDLE_WAIT: Duration = if cfg!(test) {
    Duration::from_secs(1)
} else {
    Duration::from_secs(30)
};
const IDLE_POLL: Duration = Duration::from_millis(200);
/// Copies are written under this suffix and renamed, so a stopped copy never looks complete.
const PART_SUFFIX: &str = ".trace-part";
const OBJECT_PREFIX: &str = "objects/sha256/";

/// Why a run stopped.
pub(super) enum Stop {
    /// The user cancelled, or the program is shutting down: resumable later.
    Cancelled,
    Failed(String),
    /// Everything is copied and verified; the switch waits for a quiet moment.
    Waiting(String),
}

pub(super) type Run<T> = Result<T, Stop>;

impl From<CoreError> for Stop {
    fn from(error: CoreError) -> Self {
        Stop::Failed(describe(&error))
    }
}

fn describe(error: &CoreError) -> String {
    match error {
        CoreError::StorageFull(_) => "新位置空间不足".into(),
        CoreError::Io(e) if e.kind() == std::io::ErrorKind::StorageFull => "新位置空间不足".into(),
        CoreError::Io(e) if e.kind() == std::io::ErrorKind::PermissionDenied => {
            "没有写入新位置的权限".into()
        }
        CoreError::InvalidInput(message) | CoreError::Internal(message) => message.clone(),
        other => other.to_string(),
    }
}

fn blocking<T>(joined: Result<T, tokio::task::JoinError>) -> Result<T, CoreError> {
    joined.map_err(|_| CoreError::Internal("迁移任务异常中止".into()))
}

/// A path as one comparable string: separators, trailing separator and (on Windows) case
/// ignored.
fn comparable(path: &Path) -> String {
    let text = path.to_string_lossy().replace('/', "\\");
    let text = text.trim_end_matches('\\').to_owned();
    if cfg!(windows) {
        text.to_lowercase()
    } else {
        text
    }
}

fn is_inside(inner: &Path, outer: &Path) -> bool {
    let (inner, outer) = (comparable(inner), comparable(outer));
    inner.len() > outer.len()
        && inner.starts_with(&outer)
        && inner.as_bytes().get(outer.len()) == Some(&b'\\')
}

fn same(a: &Path, b: &Path) -> bool {
    comparable(a) == comparable(b)
}

/// The checks on a requested target before anything is copied.
fn check_target(
    raw: &str,
    active: &Path,
    data_root: &Path,
    earlier_attempt: bool,
) -> Result<PathBuf, CoreError> {
    let invalid = |m: &str| CoreError::InvalidInput(m.into());
    let path = Path::new(raw.trim());
    if raw.trim().is_empty() || !path.is_absolute() {
        return Err(invalid("请填写完整的新位置路径"));
    }
    if path.components().any(|c| matches!(c, Component::ParentDir)) {
        return Err(invalid("新位置路径不能包含 .."));
    }
    let target = path.to_path_buf();
    if same(&target, active) {
        return Err(invalid("新位置与当前资源库位置相同"));
    }
    if is_inside(&target, active) || is_inside(active, &target) {
        return Err(invalid(
            "新位置不能在当前资源库里面，当前资源库也不能在新位置里面",
        ));
    }
    if same(&target, data_root) || is_inside(&target, data_root) || is_inside(data_root, &target) {
        return Err(invalid(
            "资源库不能放进数据目录（数据库所在位置），也不能包住它",
        ));
    }
    crate::paths::no_links(&target)?;
    match std::fs::metadata(&target) {
        Ok(meta) if !meta.is_dir() => return Err(invalid("新位置是一个文件，不是文件夹")),
        Ok(_) => {
            let empty = std::fs::read_dir(&target)?.next().is_none();
            if !empty && !earlier_attempt {
                return Err(invalid("新位置不是空文件夹"));
            }
        }
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
        Err(e) => return Err(e.into()),
    }
    Ok(target)
}

/// Creates the folder and proves it can be written, before any content moves.
fn check_writable(target: &Path) -> Result<(), CoreError> {
    std::fs::create_dir_all(target).map_err(|e| {
        CoreError::InvalidInput(format!("无法创建新位置：{}", describe(&CoreError::from(e))))
    })?;
    let probe = target.join(".trace-write-test");
    let written = std::fs::write(&probe, b"trace");
    let _ = std::fs::remove_file(&probe);
    written.map_err(|e| {
        CoreError::InvalidInput(format!("新位置不可写：{}", describe(&CoreError::from(e))))
    })
}

pub(super) fn check_space(target: &Path, total_bytes: i64) -> Result<(), CoreError> {
    let free = crate::storage_ops::free_bytes(&existing_ancestor(target));
    let needed = total_bytes.saturating_add(crate::config::DISK_SAFETY_MARGIN_BYTES);
    if free.is_some_and(|free| free < needed) {
        return Err(CoreError::StorageFull(format!(
            "新位置空间不足：需要约 {} MB，可用 {} MB",
            needed / 1_048_576,
            free.unwrap_or_default() / 1_048_576
        )));
    }
    Ok(())
}

fn is_link(meta: &std::fs::Metadata) -> bool {
    #[cfg(windows)]
    {
        use std::os::windows::fs::MetadataExt;
        meta.file_attributes() & 0x400 != 0 || meta.file_type().is_symlink()
    }
    #[cfg(not(windows))]
    {
        meta.file_type().is_symlink()
    }
}

/// Every file of the library worth keeping (relative path with `/`, size). The staging folder
/// holds work in flight and is not part of the library's content.
fn walk(root: &Path) -> Result<Vec<(String, i64)>, CoreError> {
    let mut found = Vec::new();
    let mut pending = vec![root.to_path_buf()];
    while let Some(dir) = pending.pop() {
        for entry in std::fs::read_dir(&dir)? {
            let entry = entry?;
            let path = entry.path();
            let meta = std::fs::symlink_metadata(&path)?;
            if is_link(&meta) {
                return Err(CoreError::InvalidInput(
                    "资源库里有符号链接或联接点，无法迁移".into(),
                ));
            }
            let rel = path
                .strip_prefix(root)
                .map_err(|_| CoreError::Internal("path outside library".into()))?;
            let rel_text = rel
                .components()
                .map(|c| c.as_os_str().to_string_lossy().into_owned())
                .collect::<Vec<_>>()
                .join("/");
            if meta.is_dir() {
                if rel_text != ".staging" {
                    pending.push(path);
                }
            } else if !rel_text.ends_with(PART_SUFFIX) {
                found.push((rel_text, i64::try_from(meta.len()).unwrap_or(i64::MAX)));
            }
        }
    }
    found.sort();
    Ok(found)
}

fn join_rel(root: &Path, rel: &str) -> Result<PathBuf, CoreError> {
    let mut path = root.to_path_buf();
    for part in rel.split('/') {
        if part.is_empty() || part == "." || part == ".." || part.contains(['\\', ':']) {
            return Err(CoreError::InvalidInput("资源库里有无法识别的路径".into()));
        }
        path.push(part);
    }
    Ok(path)
}

/// For an object file, the digest its name promises.
fn promised_digest(rel: &str) -> Option<&str> {
    let name = rel.strip_prefix(OBJECT_PREFIX)?.rsplit('/').next()?;
    crate::paths::sha256(name).ok()
}

enum Copied {
    Done(Item),
    /// The file left the library after it was planned.
    Gone,
}

fn copy_file(from: &Path, to: &Path, item: &Item) -> Result<Copied, CoreError> {
    let source = join_rel(from, &item.rel)?;
    let target = join_rel(to, &item.rel)?;
    let mut input = match std::fs::File::open(&source) {
        Ok(file) => file,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(Copied::Gone),
        Err(e) => return Err(e.into()),
    };
    if let Some(parent) = target.parent() {
        std::fs::create_dir_all(parent)?;
    }
    let part = PathBuf::from(format!("{}{PART_SUFFIX}", target.display()));
    let mut output = std::fs::File::create(&part)?;
    let mut hasher = Sha256::new();
    let mut buffer = vec![0u8; COPY_BUFFER];
    let mut size = 0i64;
    loop {
        let n = input.read(&mut buffer)?;
        if n == 0 {
            break;
        }
        hasher.update(&buffer[..n]);
        output.write_all(&buffer[..n])?;
        size += n as i64;
    }
    output.sync_all()?;
    drop(output);
    let digest = hex(&hasher.finalize());
    if promised_digest(&item.rel).is_some_and(|promised| promised != digest) {
        let _ = std::fs::remove_file(&part);
        return Err(CoreError::InvalidInput(format!(
            "资源库里的文件 {} 与它的摘要不符（来源已损坏），不能迁移",
            item.rel
        )));
    }
    std::fs::rename(&part, &target)?;
    Ok(Copied::Done(Item {
        rel: item.rel.clone(),
        size,
        sha256: Some(digest),
        state: "copied".into(),
    }))
}

fn hex(bytes: &[u8]) -> String {
    bytes.iter().map(|b| format!("{b:02x}")).collect()
}

fn hash_path(path: &Path) -> Result<(String, i64), CoreError> {
    let mut file = std::fs::File::open(path)?;
    let mut hasher = Sha256::new();
    let mut buffer = vec![0u8; COPY_BUFFER];
    let mut size = 0i64;
    loop {
        let n = file.read(&mut buffer)?;
        if n == 0 {
            break;
        }
        hasher.update(&buffer[..n]);
        size += n as i64;
    }
    Ok((hex(&hasher.finalize()), size))
}

/// Up to a batch of items of one state, capped by bytes so a stop request is noticed soon.
async fn next_batch(core: &Core, id: &str, state: &'static str) -> Result<Vec<Item>, CoreError> {
    let id = id.to_owned();
    let items = core
        .db
        .read(move |c| store::library::items_in_state(c, &id, state, BATCH_ITEMS))
        .await?;
    let mut bytes = 0;
    let mut batch = Vec::new();
    for item in items {
        if !batch.is_empty() && bytes + item.size > BATCH_BYTES {
            break;
        }
        bytes += item.size;
        batch.push(item);
    }
    Ok(batch)
}

async fn check_stop(core: &Core, id: &str) -> Run<()> {
    if core.cancel.is_cancelled() {
        return Err(Stop::Cancelled);
    }
    let key = id.to_owned();
    let state = core
        .db
        .read(move |c| Ok(store::library::migration(c, &key)?.map(|m| m.state)))
        .await?;
    match state.as_deref() {
        Some("cancelled") | None => Err(Stop::Cancelled),
        _ => Ok(()),
    }
}

async fn record(
    core: &Core,
    id: &str,
    done: Vec<Item>,
    gone: Vec<String>,
) -> Result<(), CoreError> {
    let id = id.to_owned();
    core.db
        .write(move |tx| {
            for item in &done {
                store::library::update_item(tx, &id, item)?;
            }
            store::library::remove_items(tx, &id, &gone)?;
            store::library::refresh_counters(tx, &id, &crate::jobs::now())
        })
        .await
}

async fn copy_all(core: &Core, id: &str, from: &Path, to: &Path) -> Run<()> {
    loop {
        check_stop(core, id).await?;
        let batch = next_batch(core, id, "pending").await?;
        if batch.is_empty() {
            return Ok(());
        }
        let (source, target) = (from.to_path_buf(), to.to_path_buf());
        let copied = blocking(
            tokio::task::spawn_blocking(move || {
                batch
                    .into_iter()
                    .map(|item| {
                        let result = copy_file(&source, &target, &item);
                        (item, result)
                    })
                    .collect::<Vec<_>>()
            })
            .await,
        )?;
        let (mut done, mut gone, mut failure) = (Vec::new(), Vec::new(), None);
        for (item, result) in copied {
            match result {
                Ok(Copied::Done(copied)) => done.push(copied),
                Ok(Copied::Gone) => gone.push(item.rel),
                Err(error) => {
                    failure.get_or_insert(error);
                }
            }
        }
        record(core, id, done, gone).await?;
        if let Some(error) = failure {
            return Err(error.into());
        }
    }
}

/// Re-reads every copy and compares it with the digest recorded when it was written (and, for
/// objects, with the digest in its name). A bad copy goes back to pending and stops the move.
pub(super) async fn verify_all(core: &Core, id: &str, to: &Path) -> Run<()> {
    loop {
        check_stop(core, id).await?;
        let batch = next_batch(core, id, "copied").await?;
        if batch.is_empty() {
            return Ok(());
        }
        let target = to.to_path_buf();
        let checked = blocking(
            tokio::task::spawn_blocking(move || {
                batch
                    .into_iter()
                    .map(|item| {
                        let verdict = join_rel(&target, &item.rel)
                            .and_then(|path| hash_path(&path))
                            .map(|(digest, size)| {
                                Some(digest) == item.sha256
                                    && size == item.size
                                    && promised_digest(&item.rel).is_none_or(|promised| {
                                        Some(promised) == item.sha256.as_deref()
                                    })
                            });
                        (item, verdict)
                    })
                    .collect::<Vec<_>>()
            })
            .await,
        )?;
        let (mut done, mut bad) = (Vec::new(), Vec::new());
        for (mut item, verdict) in checked {
            if matches!(verdict, Ok(true)) {
                item.state = "verified".into();
            } else {
                item.state = "pending".into();
                item.sha256 = None;
                bad.push(item.rel.clone());
            }
            done.push(item);
        }
        record(core, id, done, Vec::new()).await?;
        if let Some(first) = bad.first() {
            return Err(Stop::Failed(format!(
                "校验失败：新位置里的 {first} 与来源不一致（共 {} 个），已标记重新复制；没有切换",
                bad.len()
            )));
        }
    }
}

/// Brings the item list in line with the library as it is now: new files are added, changed
/// ones go back to pending, vanished ones are dropped together with their copy. Object files
/// never change under their name, so only their size is compared; the small metadata files
/// are always copied again.
async fn reconcile(core: &Core, id: &str, from: &Path, to: &Path) -> Result<usize, CoreError> {
    let source = from.to_path_buf();
    let found = blocking(tokio::task::spawn_blocking(move || walk(&source)).await)??;
    let key = id.to_owned();
    let known = core
        .db
        .read(move |c| store::library::items(c, &key))
        .await?;
    let mut pending = Vec::new();
    let mut fresh = Vec::new();
    let mut gone = Vec::new();
    let known_by_rel: std::collections::HashMap<_, _> =
        known.iter().map(|i| (i.rel.clone(), i)).collect();
    for (rel, size) in &found {
        match known_by_rel.get(rel) {
            None => fresh.push((rel.clone(), *size)),
            Some(item) => {
                let copy_intact = item.state == "pending"
                    || join_rel(to, rel)
                        .and_then(|path| Ok(std::fs::metadata(path)?.len()))
                        .is_ok_and(|len| i64::try_from(len).ok() == Some(item.size));
                let stable = promised_digest(rel).is_some() && item.size == *size && copy_intact;
                if !stable && item.state != "pending" {
                    pending.push(Item {
                        rel: rel.clone(),
                        size: *size,
                        sha256: None,
                        state: "pending".into(),
                    });
                }
            }
        }
    }
    let present: std::collections::HashSet<_> = found.iter().map(|(rel, _)| rel).collect();
    for item in &known {
        if !present.contains(&item.rel) {
            gone.push(item.rel.clone());
        }
    }
    let removed = gone.clone();
    let target = to.to_path_buf();
    blocking(
        tokio::task::spawn_blocking(move || {
            for rel in &removed {
                if let Ok(path) = join_rel(&target, rel) {
                    let _ = std::fs::remove_file(path);
                }
            }
        })
        .await,
    )?;
    let changed = fresh.len() + pending.len() + gone.len();
    let key = id.to_owned();
    core.db
        .write(move |tx| {
            store::library::add_items(tx, &key, &fresh)?;
            for item in &pending {
                store::library::update_item(tx, &key, item)?;
            }
            store::library::remove_items(tx, &key, &gone)?;
            store::library::refresh_counters(tx, &key, &crate::jobs::now())
        })
        .await?;
    Ok(changed)
}

/// Ends or parks a run, unless the move was cancelled or switched in the meantime.
async fn settle(
    core: &Core,
    id: &str,
    state: &'static str,
    error: Option<String>,
) -> Result<(), CoreError> {
    let id = id.to_owned();
    core.db
        .write(move |tx| {
            store::library::set_state_if_open(tx, &id, state, error.as_deref(), &crate::jobs::now())
        })
        .await
}

async fn set_state(
    core: &Core,
    id: &str,
    state: &'static str,
    error: Option<String>,
) -> Result<(), CoreError> {
    let id = id.to_owned();
    core.db
        .write(move |tx| {
            store::library::set_state(tx, &id, state, error.as_deref(), &crate::jobs::now())
        })
        .await
}

/// Starts a move to `to_raw`, or continues the open one (or a failed or cancelled earlier
/// attempt) that targets the same folder. The work runs in the background; poll the status.
pub async fn start(core: &Arc<Core>, to_raw: &str) -> Result<LibraryStatus, CoreError> {
    let active = core.objects.archive_root();
    let data_root = core.profile.data_root.clone();
    let open = core.db.read(store::library::open_migration).await?;
    let id = if let Some(open) = open {
        if !same(Path::new(&open.to_path), Path::new(to_raw.trim())) {
            return Err(CoreError::InvalidInput(
                "已有一次迁移在进行，请等它完成或先取消".into(),
            ));
        }
        open.migration_id
    } else {
        let raw = Path::new(to_raw.trim());
        let earlier = core
            .db
            .read(store::library::closed_moves)
            .await?
            .into_iter()
            .find(|m| same(Path::new(&m.to_path), raw) && same(Path::new(&m.from_path), &active));
        let target = check_target(to_raw, &active, &data_root, earlier.is_some())?;
        let writable = target.clone();
        blocking(tokio::task::spawn_blocking(move || check_writable(&writable)).await)??;
        let id = match earlier {
            Some(previous) => {
                let id = previous.migration_id;
                set_state(core, &id, "copying", None).await?;
                id
            }
            None => create(core, &active, &target).await?,
        };
        reconcile(core, &id, &active, &target).await?;
        let remaining =
            core.db
                .read({
                    let id = id.clone();
                    move |c| {
                        Ok(
                            store::library::migration(c, &id)?
                                .map(|m| m.total_bytes - m.done_bytes),
                        )
                    }
                })
                .await?
                .unwrap_or_default();
        if let Err(error) = check_space(&target, remaining) {
            set_state(core, &id, "failed", Some(describe(&error))).await?;
            return Err(error);
        }
        id
    };
    spawn_runner(core, id);
    status(core).await
}

async fn create(core: &Core, from: &Path, to: &Path) -> Result<String, CoreError> {
    let id = format!("mig-{}", uuid::Uuid::new_v4());
    let (key, from, to) = (
        id.clone(),
        from.to_string_lossy().into_owned(),
        to.to_string_lossy().into_owned(),
    );
    core.db
        .write(move |tx| {
            store::library::create_migration(tx, &key, &from, &to, &crate::jobs::now())
        })
        .await?;
    Ok(id)
}

fn spawn_runner(core: &Arc<Core>, id: String) {
    let runner = core.clone();
    core.spawn_task(async move {
        let Ok(_running) = runner.library_run.try_lock() else {
            return;
        };
        run(&runner, &id).await;
    });
}

/// Continues a move that was open when the program stopped.
pub async fn resume_open(core: &Arc<Core>) {
    if let Ok(Some(open)) = core.db.read(store::library::open_migration).await {
        spawn_runner(core, open.migration_id);
    }
}

async fn run(core: &Arc<Core>, id: &str) {
    match drive(core, id).await {
        Ok(()) | Err(Stop::Cancelled) => {}
        Err(Stop::Waiting(message)) => {
            let _ = settle(core, id, "ready", Some(message)).await;
        }
        Err(Stop::Failed(message)) => {
            tracing::warn!(%message, "library move stopped");
            let _ = settle(core, id, "failed", Some(message)).await;
        }
    }
}

async fn load(core: &Core, id: &str) -> Run<Migration> {
    let key = id.to_owned();
    core.db
        .read(move |c| store::library::migration(c, &key))
        .await?
        .ok_or_else(|| Stop::Failed("迁移记录不存在".into()))
}

async fn drive(core: &Arc<Core>, id: &str) -> Run<()> {
    loop {
        let migration = load(core, id).await?;
        let (from, to) = (
            PathBuf::from(&migration.from_path),
            PathBuf::from(&migration.to_path),
        );
        match migration.state.as_str() {
            "copying" => {
                copy_all(core, id, &from, &to).await?;
                set_state(core, id, "verifying", None).await?;
            }
            "verifying" => {
                verify_all(core, id, &to).await?;
                set_state(core, id, "ready", None).await?;
            }
            "ready" => return switch_now(core, &migration).await,
            _ => return Ok(()),
        }
    }
}

/// Switches a verified move now. Used by the user's "switch now" and by the runner.
pub async fn switch(core: &Arc<Core>) -> Result<LibraryStatus, CoreError> {
    let Some(open) = core.db.read(store::library::open_migration).await? else {
        return Err(CoreError::NotFound("没有进行中的迁移".into()));
    };
    if open.state != "ready" {
        return Err(CoreError::InvalidInput(
            "迁移还没有复制并校验完，不能切换".into(),
        ));
    }
    let Ok(_running) = core.library_run.try_lock() else {
        return Err(CoreError::InvalidInput("迁移正在处理中，请稍后再试".into()));
    };
    match switch_now(core, &open).await {
        Ok(()) => {}
        Err(Stop::Waiting(message)) => {
            settle(core, &open.migration_id, "ready", Some(message.clone())).await?;
            return Err(CoreError::InvalidInput(message));
        }
        Err(Stop::Failed(message)) => {
            settle(core, &open.migration_id, "failed", Some(message.clone())).await?;
            return Err(CoreError::InvalidInput(message));
        }
        Err(Stop::Cancelled) => return Err(CoreError::Cancelled),
    }
    status(core).await
}

/// Cancels the open move. Nothing is deleted at the target, so a later move to the same
/// folder reuses what was already copied.
pub async fn cancel(core: &Arc<Core>) -> Result<LibraryStatus, CoreError> {
    let Some(open) = core.db.read(store::library::open_migration).await? else {
        return Err(CoreError::NotFound("没有进行中的迁移".into()));
    };
    set_state(core, &open.migration_id, "cancelled", None).await?;
    status(core).await
}

async fn wait_idle(core: &Core, id: &str) -> Run<()> {
    let deadline = tokio::time::Instant::now() + IDLE_WAIT;
    loop {
        check_stop(core, id).await?;
        let running: i64 = core
            .db
            .read(|c| {
                Ok(c.query_row(
                    "SELECT COUNT(*) FROM jobs WHERE state IN ('leased','running')",
                    [],
                    |r| r.get(0),
                )?)
            })
            .await?;
        if running == 0 {
            return Ok(());
        }
        if tokio::time::Instant::now() >= deadline {
            return Err(Stop::Waiting(format!(
                "已复制并校验完毕。还有 {running} 个任务在运行（含录制），空闲后再点“现在切换”"
            )));
        }
        tokio::time::sleep(IDLE_POLL).await;
    }
}

/// Stops new work, waits for running work to end, copies what changed in the meantime, and
/// makes the switch. The new folder is made ready first, so a failure leaves the old library
/// in use.
async fn switch_now(core: &Arc<Core>, migration: &Migration) -> Run<()> {
    let _gate = core.archive_access.write().await;
    let was_paused = core.paused.swap(true, Ordering::SeqCst);
    let outcome = switch_paused(core, migration).await;
    core.paused.store(was_paused, Ordering::SeqCst);
    outcome
}

async fn switch_paused(core: &Arc<Core>, migration: &Migration) -> Run<()> {
    let id = migration.migration_id.as_str();
    let (from, to) = (
        PathBuf::from(&migration.from_path),
        PathBuf::from(&migration.to_path),
    );
    wait_idle(core, id).await?;
    if reconcile(core, id, &from, &to).await? > 0 {
        copy_all(core, id, &from, &to).await?;
        verify_all(core, id, &to).await?;
    }
    let target = to.clone();
    blocking(
        tokio::task::spawn_blocking(move || -> Result<(), CoreError> {
            for dir in OWNED_DIRS {
                std::fs::create_dir_all(target.join(dir))?;
            }
            std::fs::create_dir_all(target.join("objects").join("sha256"))?;
            Ok(())
        })
        .await,
    )??;
    let (key, old, new) = (
        id.to_owned(),
        migration.from_path.clone(),
        migration.to_path.clone(),
    );
    core.db
        .write(move |tx| store::library::switch(tx, &key, &old, &new, &crate::jobs::now()))
        .await?;
    core.objects.set_archive_root(to);
    Ok(())
}
