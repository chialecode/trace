//! The library location and its resumable move (docs/design/multi-site-contracts.md §7.4).
//!
//! The library is the content-addressed object store plus manifests and the quarantine
//! folders, under one root. The root in use is recorded in the database; at start it is
//! resolved from there, so a move survives restarts. A move copies and verifies every file,
//! then switches in one database commit; the old folder is kept until the user releases it.
use crate::core::Core;
use crate::error::CoreError;
use crate::store;
use rusqlite::Connection;
use std::path::{Path, PathBuf};
use trace_contracts::library::{LibraryStatus, LocationView, MigrationView};

mod migrate;
mod release;

pub use migrate::{cancel, resume_open, start, switch};
pub(crate) use release::nested;
pub use release::release_old;

#[cfg(test)]
#[path = "library_restart_tests.rs"]
mod library_restart_tests;
#[cfg(test)]
#[path = "library_tests.rs"]
mod library_tests;

/// The folders inside a library root that belong to TRACE; anything else there is the
/// user's and is never touched.
pub const OWNED_DIRS: [&str; 4] = ["objects", "manifests", ".trash", ".staging"];

/// The library root to use at start: the recorded one while it exists; the configured one when
/// the recorded one is gone but the configured one is there (a profile copied elsewhere).
/// With neither present the recorded root wins, so a disconnected drive is reported rather
/// than silently replaced by an empty library.
pub fn resolve_root(conn: &Connection, configured: &Path) -> PathBuf {
    let Ok(Some(active)) = store::library::active_library(conn) else {
        return configured.to_path_buf();
    };
    let recorded = PathBuf::from(&active.path);
    if recorded == configured || recorded.is_dir() || !configured.is_dir() {
        recorded
    } else {
        configured.to_path_buf()
    }
}

/// Records the root in use as the active library.
pub async fn register(core: &Core) -> Result<(), CoreError> {
    let root = core.objects.archive_root().to_string_lossy().into_owned();
    core.db
        .write(move |tx| store::library::register_active(tx, &root, &crate::jobs::now()))
        .await
}

fn location_view(location: &store::library::Location) -> LocationView {
    let path = PathBuf::from(&location.path);
    LocationView {
        location_id: location.location_id.clone(),
        path: location.path.clone(),
        state: location.state.clone(),
        created_at: location.created_at.clone(),
        free_bytes: crate::storage_ops::free_bytes(&existing_ancestor(&path)),
        exists: path.is_dir(),
    }
}

/// The nearest folder at or above `path` that exists (free space is asked of a real folder).
pub(crate) fn existing_ancestor(path: &Path) -> PathBuf {
    path.ancestors()
        .find(|p| p.is_dir())
        .map(Path::to_path_buf)
        .unwrap_or_else(|| path.to_path_buf())
}

fn migration_view(m: &store::library::Migration) -> MigrationView {
    MigrationView {
        migration_id: m.migration_id.clone(),
        from_path: m.from_path.clone(),
        to_path: m.to_path.clone(),
        state: m.state.clone(),
        total_items: m.total_items,
        done_items: m.done_items,
        total_bytes: m.total_bytes,
        done_bytes: m.done_bytes,
        message: m.error.clone(),
        created_at: m.created_at.clone(),
        updated_at: m.updated_at.clone(),
        switched_at: m.switched_at.clone(),
    }
}

pub async fn status(core: &Core) -> Result<LibraryStatus, CoreError> {
    let (active, kept, migration) = core
        .db
        .read(|c| {
            Ok((
                store::library::active_library(c)?,
                store::library::old_kept(c)?,
                store::library::current_migration(c)?,
            ))
        })
        .await?;
    let active = active.ok_or_else(|| CoreError::Internal("no active library".into()))?;
    Ok(LibraryStatus {
        active: location_view(&active),
        old_kept: kept.iter().map(location_view).collect(),
        migration: migration.as_ref().map(migration_view),
    })
}
