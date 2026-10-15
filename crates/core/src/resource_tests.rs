//! Duplicate and similarity views (stage plan §11.5, C14). Nothing here deletes or merges.
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

/// Imports one directory and returns the entry id of each committed item.
async fn import(core: &Arc<Core>, path: &Path) -> Vec<String> {
    let preview = ok(
        core,
        "import.inspect",
        json!({"path": path.to_string_lossy(), "mode": "managed"}),
    )
    .await;
    let batch = ok(
        core,
        "import.start",
        json!({"path": path.to_string_lossy(), "mode": "managed", "preview_token": preview["preview_token"]}),
    )
    .await;
    let batch_id = batch["batch_id"].as_str().unwrap().to_owned();
    let claimed = crate::jobs::claim_due(&core.db, "resource-test", 10)
        .await
        .unwrap();
    crate::imports::run(core, &claimed[0]).await.unwrap();
    let status = ok(core, "import.status", json!({"batch_id": batch_id})).await;
    status["items"]
        .as_array()
        .unwrap()
        .iter()
        .filter_map(|v| v["entry_id"].as_str().map(str::to_owned))
        .collect()
}

fn stripes(path: &Path, width_black: u32) {
    let mut image = image::RgbImage::new(32, 32);
    for (x, _y, pixel) in image.enumerate_pixels_mut() {
        let value = if x < width_black { 0 } else { 255 };
        *pixel = image::Rgb([value, value, value]);
    }
    image.save(path).unwrap();
}

fn checker(path: &Path) {
    let mut image = image::RgbImage::new(32, 32);
    for (x, y, pixel) in image.enumerate_pixels_mut() {
        let value = if (x / 2 + y / 2) % 2 == 0 { 0 } else { 255 };
        *pixel = image::Rgb([value, value, value]);
    }
    image.save(path).unwrap();
}

#[tokio::test]
async fn comparison_uses_the_ui_entry_contract_and_only_records_a_decision() {
    let (dir, core) = core().await;
    let source = dir.path().join("compare");
    std::fs::create_dir_all(&source).unwrap();
    stripes(&source.join("a.png"), 8);
    stripes(&source.join("b.png"), 24);
    let entries = import(&core, &source).await;
    let (left, right) = (entries[0].clone(), entries[1].clone());
    let result = ok(
        &core,
        "resource.compare",
        json!({"left_entry_id": left, "right_entry_id": right}),
    )
    .await;
    assert_eq!(result["left"]["entry_id"], left);
    assert_eq!(result["right"]["entry_id"], right);
    for decision in ["not_duplicate", "keep_both", "different_versions"] {
        let recorded = ok(
            &core,
            "resource.compare.decide",
            json!({"left_entry_id": left, "right_entry_id": right, "decision": decision}),
        )
        .await;
        assert_eq!(recorded["decision"], decision);
    }
    let missing: i64 = core
        .db
        .read(|c| {
            Ok(c.query_row(
                "SELECT count(*) FROM entries WHERE lifecycle='deleted'",
                [],
                |r| r.get(0),
            )?)
        })
        .await
        .unwrap();
    assert_eq!(missing, 0, "a decision never deletes an entry");
    assert!(
        call(
            &core,
            "resource.compare",
            json!({"left_kind": "media", "left_entry_id": left, "right_entry_id": right}),
        )
        .await
        .is_err(),
        "an unsupported object kind is refused"
    );
}

#[tokio::test]
async fn exact_duplicates_group_by_sha256() {
    let (dir, core) = core().await;
    let source = dir.path().join("dup");
    std::fs::create_dir_all(&source).unwrap();
    stripes(&source.join("a.png"), 16);
    std::fs::copy(source.join("a.png"), source.join("b.png")).unwrap();
    checker(&source.join("c.png"));
    let entries = import(&core, &source).await;
    assert_eq!(entries.len(), 3);
    let groups = ok(&core, "resource.duplicates", json!({})).await;
    let items = groups["items"].as_array().unwrap();
    assert_eq!(
        items.len(),
        1,
        "only the identical bytes form a duplicate group"
    );
    assert_eq!(items[0]["entry_count"], 2);
    assert_eq!(items[0]["references"].as_array().unwrap().len(), 2);
    let sha = items[0]["blob_id"].as_str().unwrap();
    assert!(sha.starts_with("sha256:"));
    let different = ok(&core, "resource.duplicates", json!({"after": sha})).await;
    assert!(different["items"].as_array().unwrap().is_empty());
}

#[tokio::test]
async fn similar_candidates_require_signature() {
    let (dir, core) = core().await;
    let source = dir.path().join("similar");
    std::fs::create_dir_all(&source).unwrap();
    stripes(&source.join("a.png"), 16);
    stripes(&source.join("b.png"), 17);
    checker(&source.join("c.png"));
    let entries = import(&core, &source).await;
    assert_eq!(entries.len(), 3);
    let result = ok(&core, "resource.similar", json!({"entry_id": entries[0]})).await;
    assert_eq!(result["state"], "ok", "{result}");
    let items = result["items"].as_array().unwrap();
    assert!(
        items.iter().any(|i| i["entry_id"] == entries[1]),
        "the shifted image is a candidate: {result}"
    );
    assert!(
        !items.iter().any(|i| i["entry_id"] == entries[2]),
        "the checkerboard is not forced into a candidate: {result}"
    );
    // The signature is stored once and reused.
    let stored: i64 = core
        .db
        .read(|c| {
            Ok(c.query_row(
                "SELECT count(*) FROM resource_signatures WHERE kind='ahash64'",
                [],
                |r| r.get(0),
            )?)
        })
        .await
        .unwrap();
    assert!(stored >= 2);
}

#[tokio::test]
async fn incomparable_is_not_similar() {
    let (dir, core) = core().await;
    let source = dir.path().join("text");
    std::fs::create_dir_all(&source).unwrap();
    std::fs::write(source.join("note.txt"), "一段文字").unwrap();
    let entries = import(&core, &source).await;
    let result = ok(&core, "resource.similar", json!({"entry_id": entries[0]})).await;
    assert_eq!(result["state"], "incomparable");
    assert!(result["items"].as_array().unwrap().is_empty());
}

#[tokio::test]
async fn compare_decision_never_deletes() {
    let (dir, core) = core().await;
    let source = dir.path().join("compare");
    std::fs::create_dir_all(&source).unwrap();
    stripes(&source.join("a.png"), 16);
    stripes(&source.join("b.png"), 17);
    let entries = import(&core, &source).await;
    let before: (i64, i64) = core
        .db
        .read(|c| {
            Ok((
                c.query_row("SELECT count(*) FROM entries", [], |r| r.get(0))?,
                c.query_row("SELECT count(*) FROM blobs", [], |r| r.get(0))?,
            ))
        })
        .await
        .unwrap();
    let compared = ok(
        &core,
        "resource.compare",
        json!({"left_entry_id": entries[0], "right_entry_id": entries[1]}),
    )
    .await;
    assert_eq!(compared["left"]["entry_id"], entries[0]);
    assert_eq!(compared["right"]["entry_id"], entries[1]);
    assert_eq!(compared["exact_duplicate"], false);
    let decided = ok(
        &core,
        "resource.compare.decide",
        json!({"left_entry_id": entries[0], "right_entry_id": entries[1], "decision": "keep_both"}),
    )
    .await;
    assert_eq!(decided["decision"], "keep_both");
    let again = ok(
        &core,
        "resource.compare",
        json!({"left_entry_id": entries[0], "right_entry_id": entries[1]}),
    )
    .await;
    assert_eq!(again["decision"]["decision"], "keep_both");
    let after: (i64, i64) = core
        .db
        .read(|c| {
            Ok((
                c.query_row("SELECT count(*) FROM entries", [], |r| r.get(0))?,
                c.query_row("SELECT count(*) FROM blobs", [], |r| r.get(0))?,
            ))
        })
        .await
        .unwrap();
    assert_eq!(
        before, after,
        "a decision never deletes an entry or an object"
    );
}
