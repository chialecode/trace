//! Releasing a replaced library: after the user confirms, its folders are removed. Only the
//! folders TRACE created are touched; anything else the user keeps there stays.
use super::{OWNED_DIRS, status};
use crate::core::Core;
use crate::error::CoreError;
use crate::store;
use std::path::{Path, PathBuf};
use trace_contracts::library::LibraryStatus;

pub(crate) fn nested(a: &Path, b: &Path) -> bool {
    let (a, b) = (
        a.to_string_lossy().to_lowercase().replace('/', "\\"),
        b.to_string_lossy().to_lowercase().replace('/', "\\"),
    );
    let (a, b) = (a.trim_end_matches('\\'), b.trim_end_matches('\\'));
    a == b || a.starts_with(&format!("{b}\\")) || b.starts_with(&format!("{a}\\"))
}

pub async fn release_old(core: &Core, location_id: &str) -> Result<LibraryStatus, CoreError> {
    let id = location_id.to_owned();
    let location = core
        .db
        .read(move |c| store::library::location(c, &id))
        .await?
        .ok_or_else(|| CoreError::NotFound("资源库位置".into()))?;
    if location.state != "old_kept" {
        return Err(CoreError::InvalidInput(
            "只有切换后保留的旧位置可以释放".into(),
        ));
    }
    let old = PathBuf::from(&location.path);
    let active = core.objects.archive_root();
    if nested(&old, &active) || nested(&old, &core.profile.data_root) {
        return Err(CoreError::InvalidInput(
            "旧位置与正在使用的资源库或数据目录重叠，不能释放".into(),
        ));
    }
    let _gate = core.archive_access.write().await;
    let target = old.clone();
    tokio::task::spawn_blocking(move || -> Result<(), CoreError> {
        for dir in OWNED_DIRS {
            let path = target.join(dir);
            if path.exists() {
                crate::paths::no_links(&path)?;
                std::fs::remove_dir_all(&path)?;
            }
        }
        // Only an empty folder goes away; the user's own files keep it alive.
        let _ = std::fs::remove_dir(&target);
        Ok(())
    })
    .await
    .map_err(|_| CoreError::Internal("释放任务异常中止".into()))??;
    let id = location.location_id;
    core.db
        .write(move |tx| store::library::release_location(tx, &id, &crate::jobs::now()))
        .await?;
    status(core).await
}
