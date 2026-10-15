//! The library in use is recorded in the database: it survives restarts, follows a profile
//! that was copied elsewhere, and does not leak into a restore.
use super::*;
use crate::config::Profile;
use crate::core::Core;
use std::sync::Arc;
use std::time::Duration;

fn profile(dir: &tempfile::TempDir) -> Profile {
    Profile {
        data_root: dir.path().join("data"),
        archive_root: dir.path().join("archive"),
        vault_dir: Some(dir.path().join("vault")),
        ..Default::default()
    }
}

async fn wait_for(core: &Core, state: &str) {
    for _ in 0..400 {
        if status(core)
            .await
            .unwrap()
            .migration
            .is_some_and(|m| m.state == state)
        {
            return;
        }
        tokio::time::sleep(Duration::from_millis(50)).await;
    }
    panic!("move never reached {state}");
}

async fn move_to(core: &Arc<Core>, to: &Path) {
    start(core, &to.to_string_lossy()).await.unwrap();
    wait_for(core, "switched").await;
}

async fn close(core: Arc<Core>) {
    core.shutdown().await;
    drop(core);
    tokio::time::sleep(Duration::from_millis(100)).await;
}

fn put_object(core: &Core, bytes: &[u8]) -> String {
    use sha2::{Digest, Sha256};
    let digest: String = Sha256::digest(bytes)
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect();
    let path = core.objects.object_path(&digest).unwrap();
    std::fs::create_dir_all(path.parent().unwrap()).unwrap();
    std::fs::write(path, bytes).unwrap();
    digest
}

#[tokio::test]
async fn the_library_in_use_survives_a_restart_whatever_the_profile_says() {
    let dir = tempfile::tempdir().unwrap();
    let core = Core::open(profile(&dir)).await.unwrap();
    let digest = put_object(&core, b"kept across restarts");
    let new = dir.path().join("moved");
    move_to(&core, &new).await;
    close(core).await;

    // The profile still names the original folder.
    let again = Core::open(profile(&dir)).await.unwrap();
    assert_eq!(again.objects.archive_root(), new);
    assert_eq!(
        again.objects.read_range(&digest, 0, 64).unwrap(),
        b"kept across restarts"
    );
    let view = status(&again).await.unwrap();
    assert_eq!(view.active.path, new.to_string_lossy());
    assert_eq!(view.old_kept.len(), 1);
    assert_eq!(view.migration.unwrap().state, "switched");
}

#[tokio::test]
async fn a_recorded_library_that_is_gone_gives_way_to_a_profile_that_was_copied_elsewhere() {
    let dir = tempfile::tempdir().unwrap();
    let core = Core::open(profile(&dir)).await.unwrap();
    close(core).await;
    let missing = dir.path().join("unplugged").join("library");
    let db = rusqlite::Connection::open(profile(&dir).db_path()).unwrap();
    db.execute(
        "UPDATE storage_locations SET path=?1 WHERE kind='library'",
        [missing.to_string_lossy()],
    )
    .unwrap();
    assert_eq!(
        resolve_root(&db, &profile(&dir).archive_root),
        profile(&dir).archive_root,
        "the profile's own folder exists, the recorded one does not"
    );
    drop(db);
    let again = Core::open(profile(&dir)).await.unwrap();
    assert_eq!(again.objects.archive_root(), profile(&dir).archive_root);
    assert_eq!(
        status(&again).await.unwrap().active.path,
        profile(&dir).archive_root.to_string_lossy(),
        "the record follows"
    );
    close(again).await;

    // With neither folder present the recorded one is kept rather than silently replaced.
    let db = rusqlite::Connection::open(profile(&dir).db_path()).unwrap();
    db.execute(
        "UPDATE storage_locations SET path=?1 WHERE kind='library'",
        [missing.to_string_lossy()],
    )
    .unwrap();
    std::fs::remove_dir_all(profile(&dir).archive_root).unwrap();
    assert_eq!(resolve_root(&db, &profile(&dir).archive_root), missing);
}

#[tokio::test]
async fn a_move_that_was_waiting_when_the_program_stopped_finishes_after_the_next_start() {
    let dir = tempfile::tempdir().unwrap();
    let core = Core::open(profile(&dir)).await.unwrap();
    put_object(&core, b"content to move");
    core.db
        .write(|tx| {
            tx.execute(
                "INSERT INTO jobs(job_id,job_key,job_type,subject_id,state,created_at,updated_at) VALUES('busy','busy','scan','s','running','t','t')",
                [],
            )?;
            Ok(())
        })
        .await
        .unwrap();
    let new = dir.path().join("moved");
    start(&core, &new.to_string_lossy()).await.unwrap();
    // The runner has given up waiting and parked the move.
    for _ in 0..400 {
        let parked = status(&core)
            .await
            .unwrap()
            .migration
            .is_some_and(|m| m.state == "ready" && m.message.is_some());
        if parked {
            break;
        }
        tokio::time::sleep(Duration::from_millis(50)).await;
    }
    close(core).await;

    let again = Core::open(profile(&dir)).await.unwrap();
    assert_eq!(again.objects.archive_root(), profile(&dir).archive_root);
    again
        .db
        .write(|tx| {
            tx.execute("UPDATE jobs SET state='succeeded'", [])?;
            Ok(())
        })
        .await
        .unwrap();
    again.start_scheduler();
    wait_for(&again, "switched").await;
    assert_eq!(again.objects.archive_root(), new);
}

#[tokio::test]
async fn a_restore_starts_a_fresh_library_in_its_own_folder() {
    let dir = tempfile::tempdir().unwrap();
    let core = Core::open(profile(&dir)).await.unwrap();
    put_object(&core, b"content");
    move_to(&core, &dir.path().join("moved")).await;
    let backups = dir.path().join("backups");
    let info = crate::storage_ops::backup(&core, &backups, false)
        .await
        .unwrap();
    let backup = backups.join(info["backup_id"].as_str().unwrap());
    let target = dir.path().join("restored");
    crate::storage_ops::restore(&core, &backup, &target)
        .await
        .unwrap();
    let mut restored_profile = crate::config::load_profile(&target.join("profile.json")).unwrap();
    restored_profile.vault_dir = Some(dir.path().join("restored-vault"));
    let restored = Core::open(restored_profile.clone()).await.unwrap();
    assert_eq!(
        restored.objects.archive_root(),
        restored_profile.archive_root
    );
    let view = status(&restored).await.unwrap();
    assert_eq!(
        view.active.path,
        restored_profile.archive_root.to_string_lossy()
    );
    assert!(view.old_kept.is_empty() && view.migration.is_none());
}
