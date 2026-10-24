//! Local imports: managed copies and in-place indexes (stage plan §11.4, C14).
//!
//! A managed copy goes through the same staging, size/hash validation and object install as every
//! other acquisition. An in-place index only observes the file: it is never copied, renamed,
//! moved or recycled, and its absolute path stays in the local import/replica records.
use crate::core::Core;
use crate::error::CoreError;
use crate::jobs::ClaimedJob;
use crate::objects::ReceiveParams;
use serde_json::{Value, json};
use std::path::{Component, Path, PathBuf};
use std::sync::Arc;
use time::format_description::well_known::Rfc3339;
use trace_domain::ids::OperationId;

/// A preview and a batch are bounded so one directory cannot stall the daemon.
pub const MAX_FILES: usize = 5000;
const MAX_DEPTH: usize = 32;
const COPY_CHUNK: usize = 64 * 1024;
pub const INBOX_CREATOR: &str = "creator-local-inbox";
pub const INBOX_TITLE: &str = "待整理";

/// A preview receipt binds the exact scan the user saw, plus the chosen mode and target
/// creator. `start` recomputes it from the files on disk and refuses a stale confirmation
/// instead of trusting a client-held preview (F-074).
pub fn preview_token(
    root: &Path,
    scan: &Scan,
    mode: &str,
    target_creator_id: Option<&str>,
) -> String {
    use sha2::{Digest, Sha256};
    let mut hasher = Sha256::new();
    hasher.update(root.to_string_lossy().as_bytes());
    hasher.update([0]);
    hasher.update(mode.as_bytes());
    hasher.update([0]);
    hasher.update(target_creator_id.unwrap_or("").as_bytes());
    hasher.update([0]);
    hasher.update(scan.links_skipped.to_le_bytes());
    hasher.update(scan.unreadable.to_le_bytes());
    hasher.update([u8::from(scan.truncated)]);
    for file in &scan.files {
        hasher.update(file.rel.as_bytes());
        hasher.update([0]);
        hasher.update(file.size.to_le_bytes());
        hasher.update([0]);
        hasher.update(file.mtime.as_deref().unwrap_or("").as_bytes());
        hasher.update([0]);
        hasher.update(file.error.as_deref().unwrap_or("").as_bytes());
        hasher.update([0]);
    }
    hasher
        .finalize()
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect()
}

#[derive(Debug, Clone)]
pub struct Scanned {
    pub path: PathBuf,
    pub rel: String,
    pub size: i64,
    pub mtime: Option<String>,
    pub error: Option<String>,
}

#[derive(Debug, Default)]
pub struct Scan {
    pub files: Vec<Scanned>,
    pub links_skipped: usize,
    pub unreadable: usize,
    pub truncated: bool,
}

fn mtime(meta: &std::fs::Metadata) -> Option<String> {
    let modified = meta.modified().ok()?;
    let at = time::OffsetDateTime::from(modified);
    at.format(&Rfc3339).ok()
}

fn relative_name(root: &Path, path: &Path) -> String {
    let rel = path.strip_prefix(root).unwrap_or(path);
    let text = rel.to_string_lossy().replace('\\', "/");
    if text.is_empty() {
        path.file_name()
            .map(|n| n.to_string_lossy().to_string())
            .unwrap_or_default()
    } else {
        text
    }
}

/// The absolute path a caller asked for: no `..` component is accepted.
fn absolute_root(input: &str) -> Result<PathBuf, CoreError> {
    let path = PathBuf::from(input);
    if input.is_empty()
        || input.len() > 4096
        || path.components().any(|c| matches!(c, Component::ParentDir))
    {
        return Err(CoreError::InvalidInput("import path".into()));
    }
    let absolute = std::path::absolute(&path).map_err(CoreError::Io)?;
    Ok(absolute)
}

fn walk(
    root: &Path,
    dir: &Path,
    depth: usize,
    follow_links: bool,
    scan: &mut Scan,
) -> Result<(), CoreError> {
    if depth > MAX_DEPTH {
        scan.truncated = true;
        return Ok(());
    }
    let entries = match std::fs::read_dir(dir) {
        Ok(entries) => entries,
        Err(_) => {
            scan.unreadable += 1;
            return Ok(());
        }
    };
    let mut paths: Vec<PathBuf> = entries.filter_map(Result::ok).map(|e| e.path()).collect();
    paths.sort();
    for path in paths {
        if scan.files.len() >= MAX_FILES {
            scan.truncated = true;
            return Ok(());
        }
        let link = std::fs::symlink_metadata(&path)
            .map(|m| m.file_type().is_symlink())
            .unwrap_or(false);
        if link && !follow_links {
            // A junction or symbolic link is never followed by default: it can leave the tree.
            scan.links_skipped += 1;
            continue;
        }
        let meta = match std::fs::metadata(&path) {
            Ok(meta) => meta,
            Err(_) => {
                scan.unreadable += 1;
                scan.files.push(Scanned {
                    rel: relative_name(root, &path),
                    path,
                    size: 0,
                    mtime: None,
                    error: Some("cannot read".into()),
                });
                continue;
            }
        };
        if meta.is_dir() {
            walk(root, &path, depth + 1, follow_links, scan)?;
            continue;
        }
        if !meta.is_file() {
            scan.unreadable += 1;
            continue;
        }
        scan.files.push(Scanned {
            rel: relative_name(root, &path),
            path,
            size: i64::try_from(meta.len()).unwrap_or(i64::MAX),
            mtime: mtime(&meta),
            error: None,
        });
    }
    Ok(())
}

/// Read-only walk of a file or directory. It never copies, never hashes and never creates rows.
pub fn scan(input: &str, follow_links: bool) -> Result<(PathBuf, Scan), CoreError> {
    let root = absolute_root(input)?;
    let meta =
        std::fs::symlink_metadata(&root).map_err(|_| CoreError::NotFound("import path".into()))?;
    let mut scan = Scan::default();
    if meta.file_type().is_symlink() && !follow_links {
        scan.links_skipped += 1;
        return Ok((root, scan));
    }
    let meta = std::fs::metadata(&root).map_err(|_| CoreError::NotFound("import path".into()))?;
    if meta.is_file() {
        scan.files.push(Scanned {
            rel: root
                .file_name()
                .map(|n| n.to_string_lossy().to_string())
                .unwrap_or_else(|| "file".into()),
            path: root.clone(),
            size: i64::try_from(meta.len()).unwrap_or(i64::MAX),
            mtime: mtime(&meta),
            error: None,
        });
    } else if meta.is_dir() {
        walk(&root, &root, 0, follow_links, &mut scan)?;
    } else {
        return Err(CoreError::InvalidInput("not a file or directory".into()));
    }
    Ok((root, scan))
}

pub fn inspect_value(root: &Path, scan: &Scan) -> Value {
    let total: i64 = scan.files.iter().map(|f| f.size).sum();
    let sample: Vec<Value> = scan
        .files
        .iter()
        .take(200)
        .map(|f| {
            json!({
                "rel_path": f.rel,
                "size": f.size,
                "mtime": f.mtime,
                "error": f.error,
            })
        })
        .collect();
    json!({
        "path": root.to_string_lossy(),
        "files": scan.files.len(),
        "bytes": total,
        "total_bytes": total,
        "links_skipped": scan.links_skipped,
        "unreadable": scan.unreadable,
        "truncated": scan.truncated,
        "items": sample,
    })
}

fn short_error(error: &CoreError) -> String {
    let text = error.to_string();
    text.chars().take(300).collect()
}

fn file_name(rel: &str) -> String {
    rel.rsplit('/')
        .next()
        .unwrap_or(rel)
        .chars()
        .take(512)
        .collect()
}

/// The media role and content kind an imported file's name implies; unknown files stay generic.
fn kind_of(rel: &str) -> (&'static str, &'static str) {
    let extension = rel.rsplit('.').next().unwrap_or("").to_ascii_lowercase();
    match extension.as_str() {
        "jpg" | "jpeg" | "png" | "webp" | "gif" | "bmp" | "heic" | "avif" => ("image", "image"),
        "mp4" | "mov" | "webm" | "mkv" | "avi" | "flv" | "m4v" => ("video", "video"),
        "mp3" | "m4a" | "flac" | "wav" | "ogg" | "aac" => ("audio", "audio"),
        _ => ("file", "file"),
    }
}

/// Creates the entry, revision, media item, variant, blob and replica of one imported file.
#[allow(clippy::too_many_arguments)]
fn commit_rows(
    tx: &rusqlite::Transaction<'_>,
    item_id: &str,
    rel_path: &str,
    sha: &str,
    size: i64,
    mtime: Option<&str>,
    managed: bool,
    account_id: &str,
    source_path: &str,
    now: &str,
) -> Result<String, CoreError> {
    let blob_id = format!("sha256:{sha}");
    let entry_id = format!("loc-{item_id}");
    let revision_id = format!("locrev-{item_id}");
    let media_id = format!("locmedia-{item_id}");
    let variant_id = format!("locvar-{item_id}");
    let title = file_name(rel_path);
    let (role, content_kind) = kind_of(rel_path);
    tx.execute(
        "INSERT OR IGNORE INTO blobs(blob_id,size,mime,first_seen_at) VALUES(?1,?2,NULL,?3)",
        rusqlite::params![blob_id, size, now],
    )?;
    let replica_id = format!("repl-{item_id}-{}", &sha[..12.min(sha.len())]);
    if managed {
        tx.execute(
            "INSERT OR IGNORE INTO replicas(replica_id,blob_id,node_id,store_id,locator,managed,state,verified_at) VALUES(?1,?2,'local','primary',?3,1,'verified',?4)",
            rusqlite::params![replica_id, blob_id, sha, now],
        )?;
    } else {
        // The bytes stay where the user put them; only the observation is recorded.
        tx.execute(
            "INSERT OR IGNORE INTO replicas(replica_id,blob_id,node_id,store_id,locator,managed,state,verified_at) VALUES(?1,?2,'local','external',?3,0,'external',NULL)",
            rusqlite::params![replica_id, blob_id, source_path],
        )?;
    }
    tx.execute(
        "INSERT OR IGNORE INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at,published_at,published_precision) VALUES(?1,'local','file',?2,?3,?4,?5,?6)",
        rusqlite::params![
            entry_id,
            item_id,
            account_id,
            now,
            mtime,
            if mtime.is_some() { "source" } else { "unknown" }
        ],
    )?;
    tx.execute(
        "INSERT OR IGNORE INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,completeness,observed_at,document_json,metadata_json) VALUES(?1,?2,?3,?4,'','full',?5,'{}','{}')",
        rusqlite::params![revision_id, entry_id, sha, title, now],
    )?;
    tx.execute(
        "INSERT OR IGNORE INTO media_items(media_id,revision_id,role,position,source_attachment_id,title,required) VALUES(?1,?2,?3,0,?4,?5,1)",
        rusqlite::params![media_id, revision_id, role, item_id, title],
    )?;
    tx.execute(
        "INSERT OR IGNORE INTO media_variants(variant_id,media_id,source_representation,blob_id,claimed_spec,measured_spec,created_at) VALUES(?1,?2,'local-file-v1',?3,?4,NULL,?5)",
        rusqlite::params![
            variant_id,
            media_id,
            blob_id,
            json!({"size": size}).to_string(),
            now
        ],
    )?;
    if managed {
        tx.execute(
            "INSERT OR IGNORE INTO asset_selections(media_id,variant_id,policy_version,reason,updated_at) VALUES(?1,?2,'import-v1','imported file',?3)",
            rusqlite::params![media_id, variant_id, now],
        )?;
    }
    tx.execute(
        "INSERT OR REPLACE INTO entry_meta(entry_id,site,revision_id,content_kinds,updated_at) VALUES(?1,'local',?2,?3,?4)",
        rusqlite::params![entry_id, revision_id, format!("[\"{content_kind}\"]"), now],
    )?;
    crate::objects::outbox_emit(
        tx,
        "entry.updated",
        &entry_id,
        &json!({"import_item": item_id}),
    )?;
    Ok(entry_id)
}

/// The local account of one batch; created once and bound to the chosen creator.
pub fn ensure_account(
    tx: &rusqlite::Transaction<'_>,
    batch_id: &str,
    creator_id: &str,
    now: &str,
) -> Result<String, CoreError> {
    tx.execute(
        "INSERT OR IGNORE INTO creators(creator_id,display_name,aliases,tags,status,notes,created_at,updated_at) VALUES(?1,?2,'[]','[]','active','',?3,?3)",
        rusqlite::params![creator_id, if creator_id == INBOX_CREATOR { INBOX_TITLE } else { "本地导入" }, now],
    )?;
    let active: bool = tx.query_row(
        "SELECT EXISTS(SELECT 1 FROM creators WHERE creator_id=?1 AND status='active')",
        [creator_id],
        |r| r.get(0),
    )?;
    if !active {
        return Err(CoreError::InvalidInput(
            "import creator is not active".into(),
        ));
    }
    let key = format!("batch:{batch_id}");
    tx.execute(
        "INSERT OR IGNORE INTO source_accounts(account_id,namespace,native_account_key,display_name,first_seen_at) VALUES(?1,'local',?2,'本地导入',?3)",
        rusqlite::params![format!("loc-{batch_id}"), key, now],
    )?;
    let account: String = tx.query_row(
        "SELECT account_id FROM source_accounts WHERE namespace='local' AND native_account_key=?1",
        [&key],
        |r| r.get(0),
    )?;
    crate::store::accounts::bind(tx, &format!("bnd-{account}"), creator_id, &account, now)?;
    Ok(account)
}

/// The local account that receives items assigned to `creator_id`.
pub fn account_for_creator(
    tx: &rusqlite::Transaction<'_>,
    batch_id: &str,
    creator_id: &str,
    now: &str,
) -> Result<String, CoreError> {
    let key = format!("batch:{batch_id}:creator:{creator_id}");
    tx.execute(
        "INSERT OR IGNORE INTO source_accounts(account_id,namespace,native_account_key,display_name,first_seen_at) VALUES(?1,'local',?2,'本地导入',?3)",
        rusqlite::params![format!("loc-{}", uuid::Uuid::new_v4()), key, now],
    )?;
    let account: String = tx.query_row(
        "SELECT account_id FROM source_accounts WHERE namespace='local' AND native_account_key=?1",
        [&key],
        |r| r.get(0),
    )?;
    let bound: bool = tx.query_row(
        "SELECT EXISTS(SELECT 1 FROM account_bindings WHERE account_id=?1 AND state='active' AND creator_id=?2)",
        rusqlite::params![account, creator_id],
        |r| r.get(0),
    )?;
    if !bound {
        crate::store::accounts::bind(tx, &format!("bnd-{account}"), creator_id, &account, now)?;
    }
    Ok(account)
}

/// Copies one file through the object store's staging and validation, then commits its rows.
async fn process_managed(
    core: &Arc<Core>,
    job: &ClaimedJob,
    item: &crate::store::imports::ItemRow,
    account_id: &str,
    now: &str,
) -> Result<(String, String), CoreError> {
    let source = PathBuf::from(&item.source_path);
    let before = std::fs::metadata(&source).map_err(CoreError::Io)?;
    let before_size = i64::try_from(before.len()).unwrap_or(i64::MAX);
    let before_mtime = mtime(&before);
    let op = OperationId::new(format!("import-{}", item.item_id));
    let params = ReceiveParams {
        job_id: Some(job.job_id.clone()),
        subject_kind: "import_item".into(),
        subject_id: item.item_id.clone(),
        generation: job.generation,
        mime: None,
        expected_size: Some(before_size),
        expected_sha256: None,
        source_validator: before_mtime.clone(),
    };
    let mut writer = core.objects.start_receiving(&core.db, params, &op).await?;
    let mut file = tokio::fs::File::open(&source)
        .await
        .map_err(CoreError::Io)?;
    use tokio::io::AsyncReadExt;
    let mut buffer = vec![0u8; COPY_CHUNK];
    loop {
        let read = file.read(&mut buffer).await.map_err(CoreError::Io)?;
        if read == 0 {
            break;
        }
        writer.write_chunk(&core.db, &op, &buffer[..read]).await?;
    }
    let after = std::fs::metadata(&source).map_err(CoreError::Io)?;
    if i64::try_from(after.len()).unwrap_or(i64::MAX) != before_size
        || mtime(&after) != before_mtime
    {
        core.objects.cleanup_staging(&op);
        return Err(CoreError::InvalidInput(
            "source changed while copying".into(),
        ));
    }
    let (size, sha) = core
        .objects
        .finish_and_validate(&core.db, &op, writer)
        .await?;
    core.objects.install_object(&core.db, &op, &sha).await?;
    let size = i64::try_from(size).unwrap_or(i64::MAX);
    let data = (
        item.item_id.clone(),
        item.rel_path.clone(),
        sha.clone(),
        account_id.to_owned(),
        item.source_path.clone(),
        now.to_owned(),
    );
    let entry = core
        .db
        .write(move |tx| {
            commit_rows(
                tx,
                &data.0,
                &data.1,
                &data.2,
                size,
                before_mtime.as_deref(),
                true,
                &data.3,
                &data.4,
                &data.5,
            )
        })
        .await?;
    Ok((sha, entry))
}

/// Hashes an external file without moving it and records the observation.
async fn process_in_place(
    core: &Arc<Core>,
    item: &crate::store::imports::ItemRow,
    account_id: &str,
    now: &str,
) -> Result<(String, String), CoreError> {
    let source = PathBuf::from(&item.source_path);
    let before = std::fs::metadata(&source).map_err(CoreError::Io)?;
    let before_size = i64::try_from(before.len()).unwrap_or(i64::MAX);
    let before_mtime = mtime(&before);
    let sha = crate::objects::hash_file(&source)?;
    let after = std::fs::metadata(&source).map_err(CoreError::Io)?;
    if i64::try_from(after.len()).unwrap_or(i64::MAX) != before_size
        || mtime(&after) != before_mtime
    {
        return Err(CoreError::InvalidInput(
            "source changed while hashing".into(),
        ));
    }
    let data = (
        item.item_id.clone(),
        item.rel_path.clone(),
        sha.clone(),
        account_id.to_owned(),
        item.source_path.clone(),
        now.to_owned(),
    );
    let entry = core
        .db
        .write(move |tx| {
            commit_rows(
                tx,
                &data.0,
                &data.1,
                &data.2,
                before_size,
                before_mtime.as_deref(),
                false,
                &data.3,
                &data.4,
                &data.5,
            )
        })
        .await?;
    Ok((sha, entry))
}

/// Runs one import batch. Cancellation is honoured at each file boundary; committed items stay.
#[allow(clippy::too_many_lines)] // the per-file state machine is one readable sequence
pub async fn run(core: &Arc<Core>, job: &ClaimedJob) -> Result<(), CoreError> {
    let batch_id = job
        .payload
        .get("batch_id")
        .and_then(Value::as_str)
        .map(str::to_owned)
        .ok_or_else(|| CoreError::InvalidInput("import batch".into()))?;
    loop {
        let (state, mode, account): (String, String, Option<String>) = {
            let id = batch_id.clone();
            core.db
                .read(move |c| {
                    Ok(c.query_row(
                        "SELECT state,mode,account_id FROM import_batches WHERE batch_id=?1",
                        [id],
                        |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)),
                    )?)
                })
                .await?
        };
        if state == "cancelled" {
            return Ok(());
        }
        let Some(account_id) = account else {
            return Err(CoreError::Internal("import batch has no account".into()));
        };
        let pending = {
            let id = batch_id.clone();
            core.db
                .read(move |c| crate::store::imports::pending_items(c, &id, 16))
                .await?
        };
        if pending.is_empty() {
            let now = crate::jobs::now();
            let (done, failed): (i64, i64) = {
                let id = batch_id.clone();
                core.db
                    .read(move |c| {
                        Ok(c.query_row(
                            "SELECT done_items,failed_items FROM import_batches WHERE batch_id=?1",
                            [id],
                            |r| Ok((r.get(0)?, r.get(1)?)),
                        )?)
                    })
                    .await?
            };
            let state = if failed > 0 && done == 0 {
                "failed"
            } else {
                "done"
            };
            let id = batch_id.clone();
            core.db
                .write(move |tx| {
                    crate::store::imports::set_batch_state(tx, &id, state, None, &now)?;
                    crate::objects::outbox_emit(
                        tx,
                        "import.changed",
                        &id,
                        &json!({"state": state}),
                    )?;
                    Ok(())
                })
                .await?;
            return Ok(());
        }
        let now = crate::jobs::now();
        {
            let id = batch_id.clone();
            let stamp = now.clone();
            core.db
                .write(move |tx| {
                    crate::store::imports::set_batch_state(tx, &id, "running", None, &stamp)
                })
                .await?;
        }
        for item in pending {
            // A cancel between files stops before the next file.
            let (current, _): (String, String) = {
                let id = batch_id.clone();
                core.db
                    .read(move |c| {
                        Ok(c.query_row(
                            "SELECT state,mode FROM import_batches WHERE batch_id=?1",
                            [id],
                            |r| Ok((r.get(0)?, r.get(1)?)),
                        )?)
                    })
                    .await?
            };
            if current == "cancelled" {
                return Ok(());
            }
            let label = if mode == "managed" {
                "copying"
            } else {
                "hashing"
            };
            {
                let (id, label) = (item.item_id.clone(), label.to_owned());
                let now = crate::jobs::now();
                core.db
                    .write(move |tx| {
                        crate::store::imports::set_item_state(
                            tx, &id, &label, None, None, None, None, None, &now,
                        )
                    })
                    .await?;
            }
            let result = if mode == "managed" {
                process_managed(core, job, &item, &account_id, &now)
                    .await
                    .map(|(sha, entry)| (entry, Some(sha)))
            } else {
                process_in_place(core, &item, &account_id, &now)
                    .await
                    .map(|(sha, entry)| (entry, Some(sha)))
            };
            let item_id = item.item_id.clone();
            let bytes = item.size;
            match result {
                Ok((entry_id, sha)) => {
                    let now = crate::jobs::now();
                    let batch = batch_id.clone();
                    let blob = sha.as_ref().map(|sha| format!("sha256:{sha}"));
                    core.db
                        .write(move |tx| {
                            crate::store::imports::set_item_state(
                                tx,
                                &item_id,
                                "committed",
                                None,
                                sha.as_deref(),
                                blob.as_deref(),
                                Some(&entry_id),
                                None,
                                &now,
                            )?;
                            crate::store::imports::bump_batch(tx, &batch, 1, 0, 0, bytes, &now)?;
                            crate::objects::outbox_emit(
                                tx,
                                "import.changed",
                                &batch,
                                &json!({"item": item_id, "state": "committed"}),
                            )?;
                            Ok(())
                        })
                        .await?;
                }
                Err(error) => {
                    let message = short_error(&error);
                    let now = crate::jobs::now();
                    let (item_id, batch, message2) =
                        (item.item_id.clone(), batch_id.clone(), message.clone());
                    core.db
                        .write(move |tx| {
                            crate::store::imports::set_item_state(
                                tx,
                                &item_id,
                                "failed",
                                Some(&message2),
                                None,
                                None,
                                None,
                                None,
                                &now,
                            )?;
                            crate::store::imports::bump_batch(tx, &batch, 0, 1, 0, 0, &now)?;
                            crate::objects::outbox_emit(
                                tx,
                                "import.changed",
                                &batch,
                                &json!({"item": item_id, "state": "failed"}),
                            )?;
                            Ok(())
                        })
                        .await?;
                }
            }
        }
    }
}
