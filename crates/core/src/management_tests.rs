//! Quality filters, pinned retention and cross-platform work groups (stage plan §11.6/§11.7).
use crate::config::Profile;
use crate::core::Core;
use serde_json::{Value, json};
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

/// One entry with a video, a variant and a selection, for the quality states.
async fn entry(core: &Arc<Core>, id: &str, spec: Option<Value>) {
    let (entry, account) = (id.to_owned(), format!("acc-{id}"));
    core.db
        .write(move |tx| {
            tx.execute(
                "INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES(?1,'bilibili',?1,'t')",
                [&account],
            )?;
            tx.execute(
                "INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at,published_at) VALUES(?1,'bilibili','video',?1,?2,'t','2026-01-01T00:00:00Z')",
                rusqlite::params![entry, account],
            )?;
            tx.execute(
                "INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at) VALUES(?1,?2,'d',?3,'正文','t')",
                rusqlite::params![format!("rev-{entry}"), entry, format!("标题{entry}")],
            )?;
            tx.execute(
                "INSERT INTO media_items(media_id,revision_id,role,position,source_attachment_id) VALUES(?1,?2,'video',0,'v')",
                rusqlite::params![format!("media-{entry}"), format!("rev-{entry}")],
            )?;
            tx.execute(
                "INSERT INTO blobs(blob_id,size,first_seen_at) VALUES(?1,10,'t')",
                [format!("sha256:{}", "a".repeat(64))],
            )
            .ok();
            tx.execute(
                "INSERT INTO media_variants(variant_id,media_id,source_representation,blob_id,claimed_spec,measured_spec,created_at) VALUES(?1,?2,'video-v1',?3,'{}',?4,'t')",
                rusqlite::params![
                    format!("var-{entry}"),
                    format!("media-{entry}"),
                    format!("sha256:{}", "a".repeat(64)),
                    spec.map(|s| s.to_string())
                ],
            )?;
            Ok(())
        })
        .await
        .unwrap();
}

#[tokio::test]
async fn quality_filters_classify_groups() {
    let (_dir, core) = core().await;
    let spec = json!({"duration_ms": 1000, "width": 1, "height": 1});
    entry(&core, "pending", Some(spec.clone())).await;
    entry(&core, "pinned", Some(spec.clone())).await;
    entry(&core, "incomparable", None).await;
    entry(&core, "replaced", Some(spec.clone())).await;
    core.db
        .write(|tx| {
            tx.execute(
                "INSERT INTO upgrade_candidates(candidate_id,entry_id,revision_id,group_key,context_id,job_id,job_generation,deletion_generation,state,created_at) VALUES('u','pending','rev-pending','g','ctx','job',1,0,'pending','t')",
                [],
            )
            .ok();
            Ok(())
        })
        .await
        .unwrap();
    // The upgrade candidate needs a job row for its foreign key; insert it first.
    core.db
        .write(|tx| {
            tx.execute("DELETE FROM upgrade_candidates WHERE candidate_id='u'", []).ok();
            tx.execute("INSERT INTO jobs(job_id,job_key,job_type,subject_id,created_at,updated_at) VALUES('job','job','scan','pending','t','t')", [])?;
            tx.execute("INSERT INTO acquisition_contexts(context_id,entry_id,auth_state,basis,started_at) VALUES('ctx','pending','unknown','test','t')", [])?;
            tx.execute("INSERT INTO upgrade_candidates(candidate_id,entry_id,revision_id,group_key,context_id,job_id,job_generation,deletion_generation,state,created_at) VALUES('u','pending','rev-pending','g','ctx','job',1,0,'pending','t')", [])?;
            tx.execute("INSERT INTO asset_selections(media_id,variant_id,policy_version,reason,pinned,updated_at) VALUES('media-pinned','var-pinned','v','manual',1,'t')", [])?;
            tx.execute("INSERT INTO variant_retention(variant_id,retained,pinned,reason,eligible_at,updated_at) VALUES('var-replaced',0,0,'replaced_by_verified_quality','2020-01-01T00:00:00Z','t')", [])?;
            Ok(())
        })
        .await
        .unwrap();
    let pending = ok(&core, "quality.list", json!({"state": "pending_upgrade"})).await;
    assert_eq!(pending["items"][0]["entry_id"], "pending", "{pending}");
    let pinned = ok(&core, "quality.list", json!({"state": "manually_kept"})).await;
    assert_eq!(pinned["items"][0]["entry_id"], "pinned", "{pinned}");
    let incomparable = ok(&core, "quality.list", json!({"state": "incomparable"})).await;
    assert_eq!(
        incomparable["items"][0]["entry_id"], "incomparable",
        "{incomparable}"
    );
    let replaced = ok(&core, "quality.list", json!({"state": "replaced"})).await;
    let ids: Vec<&str> = replaced["items"]
        .as_array()
        .unwrap()
        .iter()
        .filter_map(|i| i["entry_id"].as_str())
        .collect();
    assert!(ids.contains(&"replaced"), "{replaced}");
    // The old entry-scoped call keeps its shape.
    let one = ok(&core, "quality.list", json!({"entry_id": "pending"})).await;
    assert!(one["items"].is_array());
    assert!(one["state"].is_string());
}

#[tokio::test]
async fn incomparable_is_not_lower_quality() {
    let (_dir, core) = core().await;
    entry(&core, "unknown", None).await;
    let classified = core
        .db
        .read(|c| crate::quality::classify(c, "unknown"))
        .await
        .unwrap();
    assert!(classified.1.iter().any(|s| s == "incomparable"));
    assert!(!classified.1.iter().any(|s| s == "lower"));
    let listed = ok(&core, "quality.list", json!({"state": "incomparable"})).await;
    assert_eq!(listed["items"][0]["state"], "incomparable");
}

#[tokio::test]
async fn pinned_variant_survives_recycle_after_policy_change() {
    let (dir, core) = core().await;
    entry(&core, "keep", Some(json!({"duration_ms": 1000}))).await;
    // The blob the pinned variant points at exists as a managed object.
    let bytes = b"pinned-bytes";
    let sha = {
        use sha2::Digest;
        let mut hasher = sha2::Sha256::new();
        hasher.update(bytes);
        hasher
            .finalize()
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect::<String>()
    };
    let blob = format!("sha256:{sha}");
    let locator = sha.clone();
    core.db
        .write({
            let blob = blob.clone();
            move |tx| {
                tx.execute("DELETE FROM media_variants WHERE variant_id='var-keep'", [])?;
                tx.execute("INSERT INTO blobs(blob_id,size,first_seen_at) VALUES(?1,?2,'t') ON CONFLICT(blob_id) DO NOTHING", rusqlite::params![blob, bytes.len() as i64])?;
                tx.execute("INSERT INTO replicas(replica_id,blob_id,node_id,store_id,locator,managed,state,verified_at) VALUES('rpin',?1,'local','primary',?2,1,'verified','t')", rusqlite::params![blob, locator])?;
                tx.execute("INSERT INTO media_variants(variant_id,media_id,source_representation,blob_id,claimed_spec,measured_spec,created_at) VALUES('var-keep','media-keep','video-v1',?1,'{}','{\"duration_ms\":1000}','t')", [&blob])?;
                tx.execute("INSERT INTO asset_selections(media_id,variant_id,policy_version,reason,pinned,updated_at) VALUES('media-keep','var-keep','v','manual',1,'t')", [])?;
                tx.execute("INSERT INTO variant_retention(variant_id,retained,pinned,reason,eligible_at,updated_at) VALUES('var-keep',0,1,'manual_keep','2020-01-01T00:00:00Z','t')", [])?;
                Ok(())
            }
        })
        .await
        .unwrap();
    let object = dir
        .path()
        .join("archive")
        .join("objects")
        .join(&sha[..2])
        .join(&sha[2..4])
        .join(&sha);
    std::fs::create_dir_all(object.parent().unwrap()).unwrap();
    std::fs::write(&object, bytes).unwrap();
    // Changing the policy starts the sweep; the pinned version stays protected.
    let configured = ok(
        &core,
        "quality.configure",
        json!({"prefer_fps": false, "wait_seconds": 0}),
    )
    .await;
    assert_eq!(configured["scope"]["task_id"], "quality-maintenance");
    assert!(configured["scope"]["estimated_entries"].as_i64().unwrap() >= 1);
    ok(&core, "quality.recycle", json!({})).await;
    assert!(object.exists(), "a pinned variant is never recycled");
}

#[tokio::test]
async fn policy_change_reevaluates_affected_entries() {
    let (_dir, core) = core().await;
    entry(
        &core,
        "one",
        Some(json!({"duration_ms": 1000, "width": 1280, "height": 720})),
    )
    .await;
    let configured = ok(
        &core,
        "quality.configure",
        json!({"prefer_fps": true, "wait_seconds": 60}),
    )
    .await;
    assert_eq!(configured["updated"], true);
    let job: (String, String) = core
        .db
        .read(|c| {
            Ok(c.query_row(
                "SELECT state,payload FROM jobs WHERE job_key='quality-maintenance'",
                [],
                |r| Ok((r.get(0)?, r.get(1)?)),
            )?)
        })
        .await
        .unwrap();
    assert_eq!(job.0, "queued");
    assert_eq!(job.1, "{}", "the sweep resumes from its first batch");
}

async fn entry_with_account(core: &Core, id: &str, account: &str, title: &str) {
    core.db
        .write({
            let (id, account, title) = (id.to_owned(), account.to_owned(), title.to_owned());
            move |tx| {
                tx.execute(
                    "INSERT OR IGNORE INTO source_accounts(account_id,namespace,native_account_key,display_name,first_seen_at) VALUES(?1,?1,?1,?1,'t')",
                    [&account],
                )?;
                tx.execute(
                    "INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at,published_at) VALUES(?1,?2,'post',?1,?3,'t','2026-02-01T00:00:00Z')",
                    rusqlite::params![id, account, account],
                )?;
                tx.execute(
                    "INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at) VALUES(?1,?2,'d',?3,?4,'t')",
                    rusqlite::params![format!("rev-{id}"), id, title, format!("{title} 的正文")],
                )?;
                Ok(())
            }
        })
        .await
        .unwrap();
}

#[tokio::test]
async fn work_group_keeps_each_caption_and_source() {
    let (_dir, core) = core().await;
    entry_with_account(&core, "w1", "weibo", "微博文案").await;
    entry_with_account(&core, "x1", "x", "X caption").await;
    let group = ok(&core, "work.group.create", json!({"title": "同一作品"})).await;
    let gid = group["group_id"].as_str().unwrap().to_owned();
    ok(
        &core,
        "work.group.add",
        json!({"group_id": gid, "entry_ids": ["w1", "x1"]}),
    )
    .await;
    let compared = ok(&core, "work.compare", json!({"group_id": gid})).await;
    let items = compared["items"].as_array().unwrap();
    assert_eq!(items.len(), 2);
    assert!(
        items
            .iter()
            .any(|i| i["title"] == "微博文案" && i["namespace"] == "weibo")
    );
    assert!(
        items
            .iter()
            .any(|i| i["title"] == "X caption" && i["namespace"] == "x")
    );
    let before: i64 = core
        .db
        .read(|c| Ok(c.query_row("SELECT count(*) FROM entries", [], |r| r.get(0))?))
        .await
        .unwrap();
    let deleted = ok(&core, "work.group.delete", json!({"group_id": gid})).await;
    assert_eq!(deleted["entries_deleted"], 0);
    let after: i64 = core
        .db
        .read(|c| Ok(c.query_row("SELECT count(*) FROM entries", [], |r| r.get(0))?))
        .await
        .unwrap();
    assert_eq!(before, after, "deleting a group only removes the relation");
}

#[tokio::test]
async fn work_batch_requires_matching_preview() {
    let (_dir, core) = core().await;
    entry_with_account(&core, "w1", "weibo", "文案").await;
    let preview = ok(
        &core,
        "work.batch.preview",
        json!({"entry_ids": ["w1"], "actions": {"tags_add": ["精选"]}}),
    )
    .await;
    let token = preview["token"].as_str().unwrap().to_owned();
    let applied = ok(&core, "work.batch.apply", json!({"token": token})).await;
    assert_eq!(applied["ok"], true, "{applied}");
    let tags = ok(&core, "archive.tags", json!({})).await;
    assert!(tags.as_array().unwrap().iter().any(|t| t["tag"] == "精选"));
    // The same token is not replayed.
    assert!(
        call(&core, "work.batch.apply", json!({"token": token}))
            .await
            .is_err(),
        "a used preview token is refused"
    );
    // A change after the preview invalidates the digest.
    let preview = ok(
        &core,
        "work.batch.preview",
        json!({"entry_ids": ["w1"], "actions": {"tags_add": ["再选"]}}),
    )
    .await;
    let token = preview["token"].as_str().unwrap().to_owned();
    core.db
        .write(|tx| {
            tx.execute(
                "INSERT INTO entry_terms(entry_id,kind,value) VALUES('w1','tag','外部变化')",
                [],
            )?;
            Ok(())
        })
        .await
        .unwrap();
    assert!(
        call(&core, "work.batch.apply", json!({"token": token}))
            .await
            .is_err(),
        "a changed object set is refused"
    );
}

#[tokio::test]
async fn work_batch_partial_failure_is_per_entry() {
    let (_dir, core) = core().await;
    entry_with_account(&core, "w1", "weibo", "文案").await;
    let preview = ok(
        &core,
        "work.batch.preview",
        json!({"entry_ids": ["w1"], "actions": {"group_add": ["missing-group"]}}),
    )
    .await;
    let token = preview["token"].as_str().unwrap().to_owned();
    let applied = ok(&core, "work.batch.apply", json!({"token": token})).await;
    assert_eq!(applied["ok"], false);
    assert_eq!(applied["failed"], 1);
    assert_eq!(applied["results"][0]["ok"], false);
    assert!(applied["results"][0]["error"].as_str().is_some());
    // The entry itself is untouched and can be retried after the group exists.
    let group = ok(&core, "work.group.create", json!({"title": "稍后"})).await;
    let gid = group["group_id"].as_str().unwrap().to_owned();
    let preview = ok(
        &core,
        "work.batch.preview",
        json!({"entry_ids": ["w1"], "actions": {"group_add": [gid]}}),
    )
    .await;
    let token = preview["token"].as_str().unwrap().to_owned();
    let applied = ok(&core, "work.batch.apply", json!({"token": token})).await;
    assert_eq!(applied["ok"], true);
}

#[tokio::test]
async fn work_compare_is_read_only() {
    let (_dir, core) = core().await;
    entry_with_account(&core, "w1", "weibo", "文案").await;
    entry_with_account(&core, "x1", "x", "caption").await;
    let group = ok(&core, "work.group.create", json!({"title": "组"})).await;
    let gid = group["group_id"].as_str().unwrap().to_owned();
    ok(
        &core,
        "work.group.add",
        json!({"group_id": gid, "entry_ids": ["w1", "x1"]}),
    )
    .await;
    let before: (i64, i64) = core
        .db
        .read(|c| {
            Ok((
                c.query_row("SELECT count(*) FROM entries", [], |r| r.get(0))?,
                c.query_row("SELECT count(*) FROM entry_revisions", [], |r| r.get(0))?,
            ))
        })
        .await
        .unwrap();
    ok(&core, "work.compare", json!({"group_id": gid})).await;
    let after: (i64, i64) = core
        .db
        .read(|c| {
            Ok((
                c.query_row("SELECT count(*) FROM entries", [], |r| r.get(0))?,
                c.query_row("SELECT count(*) FROM entry_revisions", [], |r| r.get(0))?,
            ))
        })
        .await
        .unwrap();
    assert_eq!(before, after);
}
