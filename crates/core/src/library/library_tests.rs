//! C06 library moves: resumable, verified, switched in one commit, old folder kept until
//! released.
use super::migrate::{Stop, verify_all};
use super::*;
use crate::config::Profile;
use crate::store::library::Item;
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::collections::BTreeMap;
use std::sync::Arc;
use std::time::Duration;
use trace_contracts::control::Request;

fn sha(bytes: &[u8]) -> String {
    Sha256::digest(bytes)
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect()
}

async fn open(dir: &tempfile::TempDir) -> Arc<Core> {
    Core::open(Profile {
        data_root: dir.path().join("data"),
        archive_root: dir.path().join("archive"),
        vault_dir: Some(dir.path().join("vault")),
        ..Default::default()
    })
    .await
    .unwrap()
}

/// Writes a content-addressed object, returning its digest.
fn put_object(core: &Core, bytes: &[u8]) -> String {
    let digest = sha(bytes);
    let path = core.objects.object_path(&digest).unwrap();
    std::fs::create_dir_all(path.parent().unwrap()).unwrap();
    std::fs::write(path, bytes).unwrap();
    digest
}

/// Objects, a manifest and a quarantined file: a small library.
fn populate(core: &Core) -> Vec<String> {
    let mut digests: Vec<String> = (0..5u8)
        .map(|n| put_object(core, format!("object number {n}").as_bytes()))
        .collect();
    digests.push(put_object(core, &vec![7u8; 3 * 1024 * 1024 + 17]));
    let manifests = core.objects.manifests_dir().join("entry-1");
    std::fs::create_dir_all(&manifests).unwrap();
    std::fs::write(manifests.join("manifest.json"), b"{\"entry\":1}").unwrap();
    let trash = core.objects.trash_dir();
    std::fs::create_dir_all(&trash).unwrap();
    std::fs::write(trash.join("recycle-abc"), b"recycled").unwrap();
    digests
}

/// Every file under `root` (except work in flight) by relative path.
fn tree(root: &Path) -> BTreeMap<String, Vec<u8>> {
    let mut files = BTreeMap::new();
    let mut pending = vec![root.to_path_buf()];
    while let Some(dir) = pending.pop() {
        for entry in std::fs::read_dir(&dir).unwrap().flatten() {
            let path = entry.path();
            let rel = path
                .strip_prefix(root)
                .unwrap()
                .to_string_lossy()
                .replace('\\', "/");
            if path.is_dir() {
                if rel != ".staging" {
                    pending.push(path);
                }
            } else if !rel.ends_with(".trace-part") {
                files.insert(rel, std::fs::read(&path).unwrap());
            }
        }
    }
    files
}

async fn call(core: &Arc<Core>, command: &str, args: Value) -> Result<Value, CoreError> {
    crate::app::execute(
        core,
        &Request {
            protocol_version: "0.1".into(),
            request_id: "r".into(),
            command: command.into(),
            args,
            idempotency_key: None,
            if_revision: None,
        },
    )
    .await
}

async fn migration_state(core: &Core) -> Option<(String, Option<String>)> {
    status(core)
        .await
        .unwrap()
        .migration
        .map(|m| (m.state, m.message))
}

/// Polls until the move reaches one of `states`.
async fn wait_for(core: &Core, states: &[&str]) -> (String, Option<String>) {
    for _ in 0..400 {
        if let Some((state, message)) = migration_state(core).await
            && states.contains(&state.as_str())
        {
            return (state, message);
        }
        tokio::time::sleep(Duration::from_millis(50)).await;
    }
    panic!(
        "move never reached {states:?}: {:?}",
        migration_state(core).await
    );
}

/// Polls until the move is parked because work is running.
async fn wait_parked(core: &Core) -> String {
    for _ in 0..400 {
        if let Some((state, Some(message))) = migration_state(core).await
            && state == "ready"
        {
            return message;
        }
        tokio::time::sleep(Duration::from_millis(50)).await;
    }
    panic!("move never parked: {:?}", migration_state(core).await);
}

async fn start_job(core: &Core) {
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
}

async fn end_job(core: &Core) {
    core.db
        .write(|tx| {
            tx.execute("UPDATE jobs SET state='succeeded' WHERE job_id='busy'", [])?;
            Ok(())
        })
        .await
        .unwrap();
}

/// Opens a file so that no other handle (including the copier's) can open it.
#[cfg(windows)]
fn hold_exclusively(path: &Path) -> std::fs::File {
    use std::os::windows::fs::OpenOptionsExt;
    std::fs::OpenOptions::new()
        .read(true)
        .share_mode(0)
        .open(path)
        .unwrap()
}

async fn migrate(core: &Arc<Core>, to: &Path) -> Result<Value, CoreError> {
    call(
        core,
        "library.migrate",
        json!({"to_path": to.to_string_lossy()}),
    )
    .await
}

#[tokio::test]
async fn a_move_copies_verifies_and_switches_in_one_commit_keeping_the_old_library() {
    let dir = tempfile::tempdir().unwrap();
    let core = open(&dir).await;
    let digests = populate(&core);
    let old = core.objects.archive_root();
    let before = tree(&old);
    let initial = status(&core).await.unwrap();
    assert_eq!(initial.active.path, old.to_string_lossy());
    assert!(initial.migration.is_none() && initial.old_kept.is_empty());

    let new = dir.path().join("moved").join("library");
    migrate(&core, &new).await.unwrap();
    let (state, _) = wait_for(&core, &["switched", "failed"]).await;
    assert_eq!(state, "switched");

    assert_eq!(
        tree(&new),
        before,
        "the new library holds exactly the old content"
    );
    assert_eq!(tree(&old), before, "the old library is left as it was");
    assert_eq!(core.objects.archive_root(), new);
    for digest in &digests {
        let path = core.objects.object_path(digest).unwrap();
        assert!(path.starts_with(&new) && path.is_file());
        assert_eq!(&sha(&std::fs::read(path).unwrap()), digest);
    }
    let after = status(&core).await.unwrap();
    assert_eq!(after.active.path, new.to_string_lossy());
    assert_eq!(after.old_kept.len(), 1);
    assert_eq!(after.old_kept[0].path, old.to_string_lossy());
    let migration = after.migration.unwrap();
    assert_eq!(migration.total_items as usize, before.len());
    assert_eq!(migration.done_items, migration.total_items);
    assert!(new.join("objects/sha256").is_dir() && new.join(".staging").is_dir());
    let unverified: i64 = core
        .db
        .read(|c| {
            Ok(c.query_row(
                "SELECT COUNT(*) FROM library_migration_items WHERE state<>'verified'",
                [],
                |r| r.get(0),
            )?)
        })
        .await
        .unwrap();
    assert_eq!(unverified, 0);

    // Work after the switch happens in the new library.
    assert!(core.objects.staging_dir().starts_with(&new));
    let fresh = put_object(&core, b"written after the switch");
    assert!(core.objects.object_path(&fresh).unwrap().starts_with(&new));
    assert!(
        !old.join("objects/sha256")
            .join(&fresh[..2])
            .join(&fresh)
            .exists()
    );
}

#[cfg(windows)]
#[tokio::test]
async fn an_interrupted_move_continues_without_copying_verified_files_again() {
    let dir = tempfile::tempdir().unwrap();
    let core = open(&dir).await;
    let digests = populate(&core);
    let old = core.objects.archive_root();
    let before = tree(&old);
    let new = dir.path().join("new-lib");

    // One source file is held open without sharing, so copying it fails.
    let blocked = &digests[2];
    let _lock = hold_exclusively(&core.objects.object_path(blocked).unwrap());
    migrate(&core, &new).await.unwrap();
    let (state, message) = wait_for(&core, &["failed", "switched"]).await;
    assert_eq!(state, "failed", "{message:?}");
    assert!(message.is_some());
    assert_eq!(
        core.objects.archive_root(),
        old,
        "the old library stays in use"
    );
    assert!(status(&core).await.unwrap().old_kept.is_empty());

    let copied = new
        .join("objects/sha256")
        .join(&digests[0][..2])
        .join(&digests[0]);
    let stamp = std::fs::metadata(&copied).unwrap().modified().unwrap();
    drop(_lock);
    tokio::time::sleep(Duration::from_millis(50)).await;
    migrate(&core, &new).await.unwrap();
    let (state, message) = wait_for(&core, &["switched", "failed"]).await;
    assert_eq!(state, "switched", "{message:?}");
    assert_eq!(tree(&new), before);
    let moves: i64 = core
        .db
        .read(|c| Ok(c.query_row("SELECT COUNT(*) FROM library_migrations", [], |r| r.get(0))?))
        .await
        .unwrap();
    assert_eq!(moves, 1, "the retry continued the same move");
    assert_eq!(
        std::fs::metadata(&copied).unwrap().modified().unwrap(),
        stamp,
        "a verified file is not written again"
    );
}

#[tokio::test]
async fn a_damaged_source_object_stops_the_move_before_anything_switches() {
    let dir = tempfile::tempdir().unwrap();
    let core = open(&dir).await;
    populate(&core);
    let old = core.objects.archive_root();
    let name = sha(b"what the name promises");
    let damaged = core.objects.object_path(&name).unwrap();
    std::fs::create_dir_all(damaged.parent().unwrap()).unwrap();
    std::fs::write(&damaged, b"something else entirely").unwrap();
    let new = dir.path().join("new-lib");
    migrate(&core, &new).await.unwrap();
    let (state, message) = wait_for(&core, &["failed", "switched"]).await;
    assert_eq!(state, "failed");
    assert!(message.unwrap().contains("摘要不符"));
    assert_eq!(core.objects.archive_root(), old);
    assert!(
        !new.join("objects/sha256")
            .join(&name[..2])
            .join(&name)
            .exists()
    );
    assert!(
        !new.join("objects/sha256")
            .join(&name[..2])
            .join(format!("{name}.trace-part"))
            .exists(),
        "no half-written copy remains"
    );
    assert!(status(&core).await.unwrap().old_kept.is_empty());
}

#[tokio::test]
async fn verification_sends_a_wrong_copy_back_and_refuses_to_go_on() {
    let dir = tempfile::tempdir().unwrap();
    let core = open(&dir).await;
    let new = dir.path().join("new-lib");
    std::fs::create_dir_all(new.join("manifests")).unwrap();
    std::fs::write(new.join("manifests/m.json"), b"altered after copying").unwrap();
    let old = core.objects.archive_root().to_string_lossy().into_owned();
    let target = new.to_string_lossy().into_owned();
    core.db
        .write(move |tx| {
            store::library::create_migration(tx, "mig-test", &old, &target, "t")?;
            store::library::add_items(tx, "mig-test", &[("manifests/m.json".into(), 21)])?;
            store::library::update_item(
                tx,
                "mig-test",
                &Item {
                    rel: "manifests/m.json".into(),
                    size: 21,
                    sha256: Some(sha(b"what was copied")),
                    state: "copied".into(),
                },
            )
        })
        .await
        .unwrap();
    let outcome = verify_all(&core, "mig-test", &new).await;
    assert!(matches!(outcome, Err(Stop::Failed(m)) if m.contains("校验失败")));
    let items = core
        .db
        .read(|c| store::library::items(c, "mig-test"))
        .await
        .unwrap();
    assert_eq!(items[0].state, "pending", "it will be copied again");
}

#[tokio::test]
async fn a_failed_switch_commit_leaves_the_old_library_in_use_and_can_be_retried() {
    let dir = tempfile::tempdir().unwrap();
    let core = open(&dir).await;
    populate(&core);
    let old = core.objects.archive_root();
    core.db
        .write(|tx| {
            tx.execute_batch(
                "CREATE TRIGGER refuse_switch BEFORE UPDATE OF state ON storage_locations WHEN NEW.state='old_kept' BEGIN SELECT RAISE(ABORT,'refused'); END;",
            )?;
            Ok(())
        })
        .await
        .unwrap();
    let new = dir.path().join("new-lib");
    migrate(&core, &new).await.unwrap();
    let (state, _) = wait_for(&core, &["failed", "switched"]).await;
    assert_eq!(state, "failed");
    assert_eq!(core.objects.archive_root(), old);
    let active = core
        .db
        .read(store::library::active_library)
        .await
        .unwrap()
        .unwrap();
    assert_eq!(
        active.path,
        old.to_string_lossy(),
        "nothing was half-switched"
    );
    let locations: i64 = core
        .db
        .read(|c| {
            Ok(c.query_row(
                "SELECT COUNT(*) FROM storage_locations WHERE kind='library'",
                [],
                |r| r.get(0),
            )?)
        })
        .await
        .unwrap();
    assert_eq!(locations, 1);

    core.db
        .write(|tx| {
            tx.execute_batch("DROP TRIGGER refuse_switch;")?;
            Ok(())
        })
        .await
        .unwrap();
    migrate(&core, &new).await.unwrap();
    assert_eq!(wait_for(&core, &["switched", "failed"]).await.0, "switched");
    assert_eq!(core.objects.archive_root(), new);
}

#[tokio::test]
async fn the_switch_waits_for_running_work_and_carries_over_what_changed_meanwhile() {
    let dir = tempfile::tempdir().unwrap();
    let core = open(&dir).await;
    let digests = populate(&core);
    let old = core.objects.archive_root();
    start_job(&core).await;
    let new = dir.path().join("new-lib");
    migrate(&core, &new).await.unwrap();
    assert!(wait_parked(&core).await.contains("任务在运行"));
    assert_eq!(core.objects.archive_root(), old);

    // The library changes while the move waits: an object arrives, one is purged, a
    // manifest is rewritten, and a verified copy is truncated.
    let arrived = put_object(&core, b"downloaded while waiting");
    std::fs::remove_file(core.objects.object_path(&digests[0]).unwrap()).unwrap();
    std::fs::write(
        core.objects.manifests_dir().join("entry-1/manifest.json"),
        b"{\"entry\":1,\"rewritten\":true}",
    )
    .unwrap();
    let truncated = new
        .join("objects/sha256")
        .join(&digests[1][..2])
        .join(&digests[1]);
    std::fs::write(&truncated, b"x").unwrap();

    // A second move elsewhere is refused while one is open; the same target is accepted.
    let other = dir.path().join("elsewhere");
    assert!(matches!(
        migrate(&core, &other).await,
        Err(CoreError::InvalidInput(_))
    ));
    assert!(!other.exists());
    let busy = call(&core, "library.switch", json!({})).await;
    assert!(matches!(busy, Err(CoreError::InvalidInput(m)) if m.contains("任务在运行")));
    assert_eq!(core.objects.archive_root(), old, "still waiting");

    end_job(&core).await;
    call(&core, "library.switch", json!({})).await.unwrap();
    assert_eq!(core.objects.archive_root(), new);
    assert_eq!(tree(&new), tree(&old));
    assert!(core.objects.object_path(&arrived).unwrap().is_file());
    assert!(!core.objects.object_path(&digests[0]).unwrap().exists());
    let moves: i64 = core
        .db
        .read(|c| Ok(c.query_row("SELECT COUNT(*) FROM library_migrations", [], |r| r.get(0))?))
        .await
        .unwrap();
    assert_eq!(moves, 1);
    assert!(
        !core.paused.load(std::sync::atomic::Ordering::SeqCst),
        "work resumes"
    );
}

#[tokio::test]
async fn cancelling_stops_the_move_and_deletes_nothing() {
    let dir = tempfile::tempdir().unwrap();
    let core = open(&dir).await;
    populate(&core);
    let old = core.objects.archive_root();
    start_job(&core).await;
    let new = dir.path().join("new-lib");
    migrate(&core, &new).await.unwrap();
    wait_for(&core, &["ready"]).await;
    let copied = tree(&new);
    assert!(!copied.is_empty());
    call(&core, "library.cancel", json!({})).await.unwrap();
    assert_eq!(wait_for(&core, &["cancelled"]).await.0, "cancelled");
    assert_eq!(
        tree(&new),
        copied,
        "the copy is left in place for a later retry"
    );
    assert_eq!(core.objects.archive_root(), old);
    assert!(call(&core, "library.cancel", json!({})).await.is_err());
    // A later move to the same folder continues from the cancelled one.
    end_job(&core).await;
    migrate(&core, &new).await.unwrap();
    assert_eq!(wait_for(&core, &["switched", "failed"]).await.0, "switched");
}

#[tokio::test]
async fn the_old_library_goes_only_when_the_user_releases_it_and_only_trace_folders_go() {
    let dir = tempfile::tempdir().unwrap();
    let core = open(&dir).await;
    populate(&core);
    let old = core.objects.archive_root();
    let new = dir.path().join("new-lib");
    migrate(&core, &new).await.unwrap();
    wait_for(&core, &["switched"]).await;
    std::fs::write(old.join("my-notes.txt"), b"not TRACE's").unwrap();

    let active_id = status(&core).await.unwrap().active.location_id;
    for bad in [active_id.as_str(), "lib-unknown"] {
        assert!(
            call(&core, "library.release_old", json!({"location_id": bad}))
                .await
                .is_err()
        );
    }
    assert!(old.join("objects").is_dir(), "nothing was released");

    let kept = status(&core).await.unwrap().old_kept[0].location_id.clone();
    let after = call(&core, "library.release_old", json!({"location_id": kept}))
        .await
        .unwrap();
    assert_eq!(after["old_kept"].as_array().unwrap().len(), 0);
    for owned in OWNED_DIRS {
        assert!(!old.join(owned).exists(), "{owned}");
    }
    assert_eq!(
        std::fs::read(old.join("my-notes.txt")).unwrap(),
        b"not TRACE's"
    );
    assert!(
        core.objects
            .object_path(&sha(b"object number 1"))
            .unwrap()
            .is_file()
    );
    assert_eq!(after["migration"]["state"], "released");
    assert!(
        call(&core, "library.release_old", json!({"location_id": kept}))
            .await
            .is_err(),
        "a released location cannot be released again"
    );
}

#[tokio::test]
async fn targets_that_could_lose_or_mix_data_are_refused_before_anything_is_copied() {
    let dir = tempfile::tempdir().unwrap();
    let core = open(&dir).await;
    populate(&core);
    let old = core.objects.archive_root();
    let data = core.profile.data_root.clone();
    let file = dir.path().join("a-file");
    std::fs::write(&file, b"x").unwrap();
    let full = dir.path().join("not-empty");
    std::fs::create_dir_all(&full).unwrap();
    std::fs::write(full.join("keep.txt"), b"x").unwrap();
    let relative = "library-here".to_string();
    let parent_ref = dir.path().join("a").join("..").join("b");
    let bad: Vec<String> = vec![
        String::new(),
        "   ".into(),
        relative,
        parent_ref.to_string_lossy().into_owned(),
        old.to_string_lossy().into_owned(),
        format!("{}\\", old.display()),
        old.join("inside").to_string_lossy().into_owned(),
        dir.path().to_string_lossy().into_owned(),
        data.to_string_lossy().into_owned(),
        data.join("state").to_string_lossy().into_owned(),
        file.to_string_lossy().into_owned(),
        full.to_string_lossy().into_owned(),
    ];
    for target in bad {
        let outcome = call(&core, "library.migrate", json!({"to_path": target})).await;
        assert!(
            matches!(outcome, Err(CoreError::InvalidInput(_))),
            "{target:?}: {outcome:?}"
        );
    }
    let moves: i64 = core
        .db
        .read(|c| Ok(c.query_row("SELECT COUNT(*) FROM library_migrations", [], |r| r.get(0))?))
        .await
        .unwrap();
    assert_eq!(moves, 0);
    assert_eq!(core.objects.archive_root(), old);
    assert_eq!(std::fs::read(full.join("keep.txt")).unwrap(), b"x");
}

#[cfg(windows)]
#[tokio::test]
async fn a_target_volume_without_room_for_the_library_is_refused_with_the_numbers() {
    let dir = tempfile::tempdir().unwrap();
    let error = super::migrate::check_space(dir.path(), i64::MAX / 4).unwrap_err();
    assert!(
        matches!(&error, CoreError::StorageFull(m) if m.contains("MB")),
        "{error:?}"
    );
    assert!(super::migrate::check_space(dir.path(), 1024).is_ok());
}
