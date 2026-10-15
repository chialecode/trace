//! Local imports and comparison (stage plan §11.4/§11.5, C14).
use crate::config::Profile;
use crate::core::Core;
use serde_json::{Value, json};
use std::path::Path;
use std::sync::Arc;

async fn core() -> (tempfile::TempDir, Arc<Core>) {
    let dir = tempfile::tempdir().unwrap();
    let core = Core::open(Profile {
        data_root: dir.path().join("data"),
        archive_root: dir.path().join("archive"),
        vault_dir: Some(dir.path().join("vault")),
        ..Default::default()
    })
    .await
    .unwrap();
    (dir, core)
}

async fn call(
    core: &Arc<Core>,
    command: &str,
    args: Value,
) -> Result<Value, crate::error::CoreError> {
    crate::app::execute(
        core,
        &trace_contracts::control::Request {
            protocol_version: trace_domain::CONTROL_PROTOCOL_VERSION.into(),
            request_id: uuid::Uuid::new_v4().to_string(),
            command: command.into(),
            idempotency_key: None,
            if_revision: None,
            args,
        },
    )
    .await
}

async fn ok(core: &Arc<Core>, command: &str, args: Value) -> Value {
    call(core, command, args)
        .await
        .unwrap_or_else(|e| panic!("{command}: {e}"))
}

/// Starts a batch and runs its job to completion the way the pipeline would.
async fn start_and_run(core: &Arc<Core>, path: &Path, mode: &str) -> Value {
    let preview = ok(
        core,
        "import.inspect",
        json!({"path": path.to_string_lossy(), "mode": mode}),
    )
    .await;
    let batch = ok(
        core,
        "import.start",
        json!({"path": path.to_string_lossy(), "mode": mode, "preview_token": preview["preview_token"]}),
    )
    .await;
    let batch_id = batch["batch_id"].as_str().unwrap().to_owned();
    let claimed = crate::jobs::claim_due(&core.db, "import-test", 10)
        .await
        .unwrap();
    assert_eq!(claimed.len(), 1, "the import job is queued");
    crate::imports::run(core, &claimed[0]).await.unwrap();
    ok(core, "import.status", json!({"batch_id": batch_id})).await
}

fn write(path: &Path, bytes: impl AsRef<[u8]>) {
    std::fs::create_dir_all(path.parent().unwrap()).unwrap();
    std::fs::write(path, bytes.as_ref()).unwrap();
}

#[tokio::test]
async fn managed_import_leaves_source_untouched() {
    let (dir, core) = core().await;
    let source = dir.path().join("source").join("a.txt");
    write(&source, "托管复制的字节");
    let bytes = std::fs::read(&source).unwrap();
    let status = start_and_run(&core, &dir.path().join("source"), "managed").await;
    assert_eq!(status["batch"]["state"], "done");
    assert_eq!(status["batch"]["done_items"], 1);
    assert_eq!(
        std::fs::read(&source).unwrap(),
        bytes,
        "the source file is unchanged"
    );
    let entry_id = status["items"][0]["entry_id"].as_str().unwrap().to_owned();
    let entry = ok(&core, "archive.entry.get", json!({"entry_id": entry_id})).await;
    assert_eq!(entry["revisions"][0]["title"], "a.txt");
    let managed: i64 = core
        .db
        .read(|c| {
            Ok(c.query_row(
                "SELECT count(*) FROM replicas WHERE managed=1 AND state='verified'",
                [],
                |r| r.get(0),
            )?)
        })
        .await
        .unwrap();
    assert_eq!(managed, 1);
}

#[tokio::test]
async fn in_place_index_never_moves_or_recycles() {
    let (dir, core) = core().await;
    let source = dir.path().join("external").join("b.txt");
    write(&source, "原地索引的字节");
    let status = start_and_run(&core, &dir.path().join("external"), "in_place").await;
    assert_eq!(status["batch"]["state"], "done");
    let entry_id = status["items"][0]["entry_id"].as_str().unwrap().to_owned();
    let (managed, locator): (i64, String) = core
        .db
        .read(|c| {
            Ok(c.query_row(
                "SELECT managed,locator FROM replicas WHERE store_id='external'",
                [],
                |r| Ok((r.get(0)?, r.get(1)?)),
            )?)
        })
        .await
        .unwrap();
    assert_eq!(managed, 0);
    assert_eq!(Path::new(&locator), source.as_path());
    // Deleting the entry and running recycling never touches the external file.
    let preview = ok(
        &core,
        "archive.delete_preview",
        json!({"entry_id": entry_id, "scope": "archive"}),
    )
    .await;
    let token = preview["token"].as_str().unwrap().to_owned();
    ok(
        &core,
        "archive.delete",
        json!({"entry_id": entry_id, "scope": "archive", "token": token}),
    )
    .await;
    let _ = call(&core, "quality.recycle", json!({})).await;
    assert!(
        source.exists(),
        "an in-place file is never moved or recycled"
    );
    assert_eq!(std::fs::read(&source).unwrap(), "原地索引的字节".as_bytes());
}

#[tokio::test]
async fn import_resume_does_not_claim_partial_success() {
    let (dir, core) = core().await;
    let source = dir.path().join("resume");
    write(&source.join("one.txt"), "一");
    write(&source.join("two.txt"), "二");
    let preview = ok(
        &core,
        "import.inspect",
        json!({"path": source.to_string_lossy(), "mode": "managed"}),
    )
    .await;
    let started = ok(
        &core,
        "import.start",
        json!({"path": source.to_string_lossy(), "mode": "managed", "preview_token": preview["preview_token"]}),
    )
    .await;
    let batch_id = started["batch_id"].as_str().unwrap().to_owned();
    // Cancel before the job runs; no item may look committed.
    ok(&core, "import.cancel", json!({"batch_id": batch_id})).await;
    let pending = ok(&core, "import.status", json!({"batch_id": batch_id})).await;
    assert_eq!(pending["batch"]["state"], "cancelled");
    assert_eq!(pending["batch"]["done_items"], 0);
    let resumed = ok(&core, "import.resume", json!({"batch_id": batch_id})).await;
    assert_eq!(resumed["resumed"], true);
    let claimed = crate::jobs::claim_due(&core.db, "import-test", 10)
        .await
        .unwrap();
    assert_eq!(claimed.len(), 1);
    crate::imports::run(&core, &claimed[0]).await.unwrap();
    let done = ok(&core, "import.status", json!({"batch_id": batch_id})).await;
    assert_eq!(done["batch"]["state"], "done");
    assert_eq!(done["batch"]["done_items"], 2);
    let entries: i64 = core
        .db
        .read(|c| {
            Ok(c.query_row(
                "SELECT count(*) FROM entries WHERE namespace='local'",
                [],
                |r| r.get(0),
            )?)
        })
        .await
        .unwrap();
    assert_eq!(entries, 2, "resume commits each file once");
}

#[tokio::test]
async fn duplicate_bytes_reuse_blob() {
    let (dir, core) = core().await;
    let source = dir.path().join("dupes");
    write(&source.join("a.txt"), "同一份字节");
    write(&source.join("b.txt"), "同一份字节");
    let status = start_and_run(&core, &source, "managed").await;
    assert_eq!(status["batch"]["done_items"], 2);
    let blobs: i64 = core
        .db
        .read(|c| Ok(c.query_row("SELECT count(*) FROM blobs", [], |r| r.get(0))?))
        .await
        .unwrap();
    assert_eq!(blobs, 1, "same bytes are stored once");
    let entries: i64 = core
        .db
        .read(|c| {
            Ok(c.query_row(
                "SELECT count(*) FROM entries WHERE namespace='local'",
                [],
                |r| r.get(0),
            )?)
        })
        .await
        .unwrap();
    assert_eq!(entries, 2, "each file keeps its own entry and name");
}

#[tokio::test]
async fn external_change_marks_offline() {
    let (dir, core) = core().await;
    let source = dir.path().join("offline").join("c.txt");
    write(&source, "会消失的文件");
    let status = start_and_run(&core, &dir.path().join("offline"), "in_place").await;
    let entry_id = status["items"][0]["entry_id"].as_str().unwrap().to_owned();
    std::fs::remove_file(&source).unwrap();
    let checked = ok(&core, "import.check", json!({})).await;
    assert_eq!(checked["offline"], 1);
    let state: String = core
        .db
        .read(|c| {
            Ok(c.query_row(
                "SELECT state FROM replicas WHERE store_id='external'",
                [],
                |r| r.get(0),
            )?)
        })
        .await
        .unwrap();
    assert_eq!(state, "offline");
    // The last observation and the entry are kept.
    let entry = ok(&core, "archive.entry.get", json!({"entry_id": entry_id})).await;
    assert_eq!(entry["revisions"][0]["title"], "c.txt");
}

#[tokio::test]
async fn import_rejects_link_traversal() {
    let (_dir, core) = core().await;
    assert!(
        call(&core, "import.inspect", json!({"path": "C:/a/../b"}))
            .await
            .is_err(),
        "a path with .. is refused"
    );
    assert!(
        call(&core, "import.inspect", json!({"path": ""}))
            .await
            .is_err()
    );
}

#[tokio::test]
async fn import_assign_moves_selected_items_only() {
    let (dir, core) = core().await;
    let source = dir.path().join("assign");
    write(&source.join("a.txt"), "第一个");
    write(&source.join("b.txt"), "第二个");
    let status = start_and_run(&core, &source, "managed").await;
    let batch = status["batch"]["batch_id"].as_str().unwrap().to_owned();
    let items = status["items"].as_array().unwrap();
    let selected = items[0]["item_id"].as_str().unwrap().to_owned();
    let selected_entry = items[0]["entry_id"].as_str().unwrap().to_owned();
    let other_entry = items[1]["entry_id"].as_str().unwrap().to_owned();
    let creator = ok(&core, "creator.create", json!({"display_name": "导入目标"})).await;
    let creator_id = creator["creator_id"].as_str().unwrap().to_owned();
    let assigned = ok(
        &core,
        "import.assign",
        json!({"batch_id": batch, "target_creator_id": creator_id, "item_ids": [selected]}),
    )
    .await;
    assert_eq!(assigned["assigned"], 1);
    let accounts: Vec<(String, String)> = core
        .db
        .read(|c| {
            let mut stmt = c.prepare(
                "SELECT entry_id,account_id FROM entries WHERE namespace='local' ORDER BY entry_id",
            )?;
            let rows = stmt
                .query_map([], |r| Ok((r.get(0)?, r.get(1)?)))?
                .collect::<Result<Vec<_>, _>>()?;
            Ok(rows)
        })
        .await
        .unwrap();
    let owner = |entry: &str| {
        accounts
            .iter()
            .find(|(id, _)| id == entry)
            .map(|(_, account)| account.clone())
            .unwrap()
    };
    assert_ne!(owner(&selected_entry), owner(&other_entry));
    let bound_creator: String = core
        .db
        .read({
            let account = owner(&selected_entry);
            move |c| {
                Ok(c.query_row(
                    "SELECT creator_id FROM account_bindings WHERE account_id=?1 AND state='active'",
                    [account],
                    |r| r.get(0),
                )?)
            }
        })
        .await
        .unwrap();
    assert_eq!(bound_creator, creator_id);
}

#[tokio::test]
async fn import_start_requires_the_exact_preview_and_refuses_a_stale_one() {
    let (dir, core) = core().await;
    let source = dir.path().join("preview");
    write(&source.join("a.txt"), "预览时的字节");
    let preview = ok(
        &core,
        "import.inspect",
        json!({"path": source.to_string_lossy(), "mode": "managed"}),
    )
    .await;
    assert_eq!(preview["files"], 1);
    assert_eq!(preview["bytes"].as_i64(), Some("预览时的字节".len() as i64));
    let token = preview["preview_token"].as_str().unwrap().to_owned();
    assert!(
        call(
            &core,
            "import.start",
            json!({"path": source.to_string_lossy(), "mode": "managed"}),
        )
        .await
        .is_err(),
        "a start without the preview confirmation is refused"
    );
    assert!(
        call(
            &core,
            "import.start",
            json!({"path": source.to_string_lossy(), "mode": "in_place", "preview_token": token}),
        )
        .await
        .is_err(),
        "a different mode invalidates the preview"
    );
    write(&source.join("b.txt"), "预览后新增");
    assert!(
        call(
            &core,
            "import.start",
            json!({"path": source.to_string_lossy(), "mode": "managed", "preview_token": token}),
        )
        .await
        .is_err(),
        "changed files invalidate the preview"
    );
    let batches: i64 = core
        .db
        .read(|c| Ok(c.query_row("SELECT count(*) FROM import_batches", [], |r| r.get(0))?))
        .await
        .unwrap();
    assert_eq!(batches, 0, "a refused confirmation writes no batch");
    let fresh = ok(
        &core,
        "import.inspect",
        json!({"path": source.to_string_lossy(), "mode": "managed"}),
    )
    .await;
    let started = ok(
        &core,
        "import.start",
        json!({"path": source.to_string_lossy(), "mode": "managed", "preview_token": fresh["preview_token"]}),
    )
    .await;
    assert_eq!(started["total_items"], 2);
}
