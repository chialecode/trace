//! Syncing download-folder copies to the library (docs/design/multi-site-contracts.md §7.3):
//! the file is verified against the digest the library records, and copied into the object
//! store only when the library lacks those bytes or holds a damaged object. The same bytes are
//! never stored twice, and the file in the download folder stays where it is.
use super::write;
use super::{MAX_ENTRIES_PER_REQUEST, user_message};
use crate::core::Core;
use crate::error::CoreError;
use crate::store;
use sha2::{Digest, Sha256};
use std::io::{Read, Write};
use std::path::{Path, PathBuf};
use trace_contracts::download::{SyncItem, SyncReport};

enum Outcome {
    AlreadyThere,
    Restored,
    /// The copy is not what the library recorded, or the library has no record of it.
    Unusable(&'static str),
}

fn object_intact(core: &Core, digest: &str, size: i64) -> bool {
    core.objects
        .object_path(digest)
        .ok()
        .and_then(|p| std::fs::metadata(p).ok())
        .is_some_and(|m| m.is_file() && m.len() == size as u64)
}

/// Where an object goes in the library, and the folders used on the way.
struct Target {
    final_path: PathBuf,
    staging_dir: PathBuf,
    trash_dir: PathBuf,
}

/// Copies `src` into the object store as `digest`, through a staging file on the same volume;
/// a damaged object already there is moved to the quarantine folder first.
fn install(target: &Target, src: &Path, digest: &str, size: u64) -> Result<(), CoreError> {
    let Target {
        final_path,
        staging_dir,
        trash_dir,
    } = target;
    std::fs::create_dir_all(staging_dir)?;
    let staging: PathBuf = staging_dir.join(format!("sync-{}", uuid::Uuid::new_v4()));
    let result = (|| -> Result<(), CoreError> {
        let mut input = std::fs::File::open(src)?;
        let mut out = std::fs::File::create(&staging)?;
        let mut hasher = Sha256::new();
        let mut copied = 0u64;
        let mut buf = vec![0u8; 1024 * 1024];
        loop {
            let n = input.read(&mut buf)?;
            if n == 0 {
                break;
            }
            hasher.update(&buf[..n]);
            out.write_all(&buf[..n])?;
            copied += n as u64;
        }
        out.sync_all()?;
        let got: String = hasher
            .finalize()
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect();
        if copied != size || got != digest {
            return Err(CoreError::InvalidInput(
                "下载目录中的文件与资源库记录的摘要不一致，未同步".into(),
            ));
        }
        if final_path.exists() {
            let name = format!(
                "corrupt-{}-{}",
                time::OffsetDateTime::now_utc().unix_timestamp(),
                &digest[..12]
            );
            std::fs::create_dir_all(trash_dir)?;
            std::fs::rename(final_path, trash_dir.join(name))?;
        }
        if let Some(parent) = final_path.parent() {
            std::fs::create_dir_all(parent)?;
        }
        std::fs::rename(&staging, final_path)?;
        Ok(())
    })();
    let _ = std::fs::remove_file(&staging);
    result
}

async fn sync_copy(
    core: &Core,
    root: &Path,
    copy: &store::downloads::ExternalCopy,
) -> Result<Outcome, CoreError> {
    let Some(media_id) = copy.media_id.clone() else {
        return Ok(Outcome::Unusable("副本没有对应的媒体记录"));
    };
    let Some((blob_id, size)) = core
        .db
        .read(move |c| store::downloads::media_blob(c, &media_id))
        .await?
    else {
        return Ok(Outcome::Unusable("资源库没有这个文件的记录"));
    };
    let digest = crate::paths::blob_sha(&blob_id)?.to_owned();
    let blob = blob_id.clone();
    let verified = core
        .db
        .read(move |c| store::downloads::has_verified_replica(c, &blob))
        .await?;
    if verified && object_intact(core, &digest, size) {
        return Ok(Outcome::AlreadyThere);
    }
    let path = root.join(&copy.rel_path);
    let want = digest.clone();
    let measured = tokio::task::spawn_blocking(move || write::measure(&path))
        .await
        .map_err(|_| CoreError::Internal("校验任务异常中止".into()))?;
    match measured {
        Ok(w) if w.sha256 == want && w.size == size as u64 => {}
        Ok(_) => return Ok(Outcome::Unusable("文件已被改动，与资源库记录不一致")),
        Err(_) => return Ok(Outcome::Unusable("文件已不在下载目录")),
    }
    let target = Target {
        final_path: core.objects.object_path(&digest)?,
        staging_dir: core.objects.staging_dir(),
        trash_dir: core.objects.trash_dir(),
    };
    let (src, dig) = (root.join(&copy.rel_path), digest.clone());
    tokio::task::spawn_blocking(move || install(&target, &src, &dig, size as u64))
        .await
        .map_err(|_| CoreError::Internal("同步任务异常中止".into()))??;
    let now = crate::jobs::now();
    core.db
        .write(move |tx| store::downloads::mark_replica_restored(tx, &blob_id, &digest, &now))
        .await?;
    Ok(Outcome::Restored)
}

async fn sync_entry(core: &Core, entry_id: &str) -> Result<SyncItem, CoreError> {
    let _gate = core.archive_access.read().await;
    let id = entry_id.to_owned();
    let (copies, folders) = core
        .db
        .read(move |c| {
            let copies = store::downloads::copies_of_entry(c, &id, None)?
                .into_iter()
                .filter(|x| x.role == "media" && matches!(x.state.as_str(), "present" | "changed"))
                .collect::<Vec<_>>();
            let mut folders = std::collections::HashMap::new();
            for copy in &copies {
                if let Some(f) = store::downloads::folder(c, &copy.location_id)? {
                    folders.insert(copy.location_id.clone(), f.path);
                }
            }
            Ok((copies, folders))
        })
        .await?;
    let item = |state: &str, files: i64, message: Option<&str>| SyncItem {
        entry_id: entry_id.to_owned(),
        state: state.into(),
        files,
        message: message.map(str::to_owned),
    };
    if copies.is_empty() {
        return Ok(item(
            "skipped",
            0,
            Some("该条目在下载目录中没有可同步的文件"),
        ));
    }
    let (mut restored, mut there, mut reason) = (0i64, 0i64, None);
    for copy in &copies {
        let Some(root) = folders.get(&copy.location_id) else {
            reason = Some("下载目录记录已不存在");
            continue;
        };
        match sync_copy(core, &PathBuf::from(root), copy).await? {
            Outcome::AlreadyThere => there += 1,
            Outcome::Restored => restored += 1,
            Outcome::Unusable(why) => reason = Some(why),
        }
    }
    Ok(if restored > 0 {
        item("restored", restored + there, None)
    } else if there > 0 && reason.is_none() {
        item("already_in_library", there, None)
    } else {
        item("skipped", there, reason)
    })
}

/// Makes sure the library holds the bytes of the entries' copies in download folders.
pub async fn sync_to_library(core: &Core, entry_ids: &[String]) -> Result<SyncReport, CoreError> {
    if entry_ids.is_empty() || entry_ids.len() > MAX_ENTRIES_PER_REQUEST {
        return Err(CoreError::InvalidInput(format!(
            "一次请求需要 1 到 {MAX_ENTRIES_PER_REQUEST} 个条目"
        )));
    }
    let _run = core.downloads_run.lock().await;
    let mut seen = std::collections::HashSet::new();
    let mut items = Vec::new();
    for id in entry_ids.iter().filter(|id| seen.insert(id.as_str())) {
        items.push(match sync_entry(core, id).await {
            Ok(item) => item,
            Err(e) => SyncItem {
                entry_id: id.clone(),
                state: "skipped".into(),
                files: 0,
                message: Some(user_message(&e)),
            },
        });
    }
    Ok(SyncReport { items })
}
