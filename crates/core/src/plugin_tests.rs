//! Plugin install, activation, rollback, removal and capability routing (stage plan §11.8/§11.9).
use crate::config::Profile;
use crate::core::Core;
use serde_json::{Value, json};
use std::path::{Path, PathBuf};
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

fn hex(bytes: &[u8]) -> String {
    use sha2::Digest;
    let mut hasher = sha2::Sha256::new();
    hasher.update(bytes);
    hasher
        .finalize()
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect()
}

/// A directory holding one plugin binary and a matching manifest.
fn plugin(
    root: &Path,
    id: &str,
    version: &str,
    protocol: &str,
    capabilities: &[&str],
    payload: &[u8],
) -> PathBuf {
    let dir = root.join(format!("{id}-{version}"));
    std::fs::create_dir_all(&dir).unwrap();
    std::fs::write(dir.join("plugin.bin"), payload).unwrap();
    let manifest = json!({
        "id": id,
        "name": id,
        "version": version,
        "protocol": protocol,
        "platform": "weibo",
        "entrypoint": "plugin.bin",
        "files": [{"path": "plugin.bin", "sha256": hex(payload)}],
        "capabilities": capabilities,
        "limits": {"max_pages": 1},
        "permissions": ["read_public_pages"],
    });
    std::fs::write(
        dir.join("plugin.json"),
        serde_json::to_vec_pretty(&manifest).unwrap(),
    )
    .unwrap();
    dir
}

#[tokio::test]
async fn plugin_install_verifies_manifest_digests() {
    let (dir, core) = core().await;
    let source = plugin(
        &dir.path().join("src"),
        "weibo-extra",
        "1.0.0",
        "0.1",
        &["notification"],
        b"first",
    );
    let installed = ok(
        &core,
        "plugin.install",
        json!({"source_dir": source.to_string_lossy()}),
    )
    .await;
    assert_eq!(installed["installed"], true);
    // A tampered digest is refused before anything is copied.
    let tampered = plugin(
        &dir.path().join("bad"),
        "weibo-extra",
        "1.0.1",
        "0.1",
        &[],
        b"second",
    );
    let manifest: Value =
        serde_json::from_slice(&std::fs::read(tampered.join("plugin.json")).unwrap()).unwrap();
    let mut manifest = manifest;
    manifest["files"][0]["sha256"] = json!("0".repeat(64));
    std::fs::write(
        tampered.join("plugin.json"),
        serde_json::to_vec(&manifest).unwrap(),
    )
    .unwrap();
    assert!(
        call(
            &core,
            "plugin.install",
            json!({"source_dir": tampered.to_string_lossy()})
        )
        .await
        .is_err(),
        "a digest mismatch is refused"
    );
    // An undeclared file is refused.
    let extra = plugin(
        &dir.path().join("extra"),
        "weibo-extra",
        "1.0.2",
        "0.1",
        &[],
        b"third",
    );
    std::fs::write(extra.join("hidden.bin"), b"undeclared").unwrap();
    assert!(
        call(
            &core,
            "plugin.install",
            json!({"source_dir": extra.to_string_lossy()})
        )
        .await
        .is_err(),
        "an undeclared file is refused"
    );
    // A path with `..` is refused before reading the directory.
    assert!(
        call(
            &core,
            "plugin.install",
            json!({"source_dir": "C:/plugins/../evil"})
        )
        .await
        .is_err()
    );
}

#[tokio::test]
async fn incompatible_plugin_cannot_enable() {
    let (dir, core) = core().await;
    let wrong_protocol = plugin(
        &dir.path().join("src"),
        "weibo-extra",
        "2.0.0",
        "9.9",
        &[],
        b"x",
    );
    assert!(
        call(
            &core,
            "plugin.install",
            json!({"source_dir": wrong_protocol.to_string_lossy()}),
        )
        .await
        .is_err(),
        "a different protocol is refused at install time"
    );
    let unknown_capability = plugin(
        &dir.path().join("src"),
        "weibo-extra",
        "2.0.1",
        "0.1",
        &["telepathy"],
        b"x",
    );
    assert!(
        call(
            &core,
            "plugin.install",
            json!({"source_dir": unknown_capability.to_string_lossy()}),
        )
        .await
        .is_err(),
        "an unknown capability is refused"
    );
}

#[tokio::test]
async fn plugin_upgrade_is_side_by_side() {
    let (dir, core) = core().await;
    let v1 = plugin(
        &dir.path().join("src"),
        "weibo-extra",
        "1.0.0",
        "0.1",
        &[],
        b"one",
    );
    let v2 = plugin(
        &dir.path().join("src"),
        "weibo-extra",
        "1.1.0",
        "0.1",
        &[],
        b"two",
    );
    ok(
        &core,
        "plugin.install",
        json!({"source_dir": v1.to_string_lossy()}),
    )
    .await;
    ok(
        &core,
        "plugin.install",
        json!({"source_dir": v2.to_string_lossy()}),
    )
    .await;
    ok(
        &core,
        "plugin.activate",
        json!({"plugin_id": "weibo-extra", "version": "1.0.0"}),
    )
    .await;
    ok(
        &core,
        "plugin.upgrade",
        json!({"source_dir": v2.to_string_lossy()}),
    )
    .await;
    let releases = ok(
        &core,
        "plugin.releases",
        json!({"plugin_id": "weibo-extra"}),
    )
    .await;
    let items = releases["items"].as_array().unwrap();
    assert_eq!(items.len(), 2, "{releases}");
    assert!(items.iter().any(|r| r["version"] == "1.0.0"));
    assert!(
        items
            .iter()
            .any(|r| r["version"] == "1.1.0" && r["state"] == "active")
    );
    // Both version directories still exist.
    for version in ["1.0.0", "1.1.0"] {
        assert!(
            dir.path()
                .join("data")
                .join("plugins")
                .join("weibo-extra")
                .join(version)
                .exists()
        );
    }
}

#[tokio::test]
async fn plugin_rollback_keeps_previous_release() {
    let (dir, core) = core().await;
    let v1 = plugin(
        &dir.path().join("src"),
        "weibo-extra",
        "1.0.0",
        "0.1",
        &[],
        b"one",
    );
    let v2 = plugin(
        &dir.path().join("src"),
        "weibo-extra",
        "1.1.0",
        "0.1",
        &[],
        b"two",
    );
    ok(
        &core,
        "plugin.install",
        json!({"source_dir": v1.to_string_lossy()}),
    )
    .await;
    ok(
        &core,
        "plugin.install",
        json!({"source_dir": v2.to_string_lossy()}),
    )
    .await;
    ok(
        &core,
        "plugin.activate",
        json!({"plugin_id": "weibo-extra", "version": "1.1.0"}),
    )
    .await;
    let rolled = ok(
        &core,
        "plugin.rollback",
        json!({"plugin_id": "weibo-extra"}),
    )
    .await;
    assert_eq!(rolled["version"], "1.0.0");
    assert_eq!(rolled["rolled_back_from"], "1.1.0");
    let releases = ok(
        &core,
        "plugin.releases",
        json!({"plugin_id": "weibo-extra"}),
    )
    .await;
    assert!(
        releases["items"]
            .as_array()
            .unwrap()
            .iter()
            .any(|r| r["version"] == "1.0.0" && r["state"] == "active")
    );
}

#[tokio::test]
async fn plugin_remove_leaves_archive_readable() {
    let (dir, core) = core().await;
    let v1 = plugin(
        &dir.path().join("src"),
        "weibo-extra",
        "1.0.0",
        "0.1",
        &[],
        b"one",
    );
    let v2 = plugin(
        &dir.path().join("src"),
        "weibo-extra",
        "1.1.0",
        "0.1",
        &[],
        b"two",
    );
    ok(
        &core,
        "plugin.install",
        json!({"source_dir": v1.to_string_lossy()}),
    )
    .await;
    ok(
        &core,
        "plugin.install",
        json!({"source_dir": v2.to_string_lossy()}),
    )
    .await;
    ok(
        &core,
        "plugin.activate",
        json!({"plugin_id": "weibo-extra", "version": "1.0.0"}),
    )
    .await;
    // Create one archived entry, then remove the inactive version.
    core.db
        .write(|tx| {
            tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('a','weibo','1','t')", [])?;
            tx.execute("INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES('e','weibo','post','1','a','t')", [])?;
            tx.execute("INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at) VALUES('r','e','d','保存的标题','正文','t')", [])?;
            Ok(())
        })
        .await
        .unwrap();
    ok(
        &core,
        "plugin.remove",
        json!({"plugin_id": "weibo-extra", "version": "1.1.0"}),
    )
    .await;
    assert!(
        !dir.path()
            .join("data")
            .join("plugins")
            .join("weibo-extra")
            .join("1.1.0")
            .exists()
    );
    assert!(
        call(
            &core,
            "plugin.remove",
            json!({"plugin_id": "weibo-extra", "version": "1.0.0"}),
        )
        .await
        .is_err(),
        "the active version cannot be removed"
    );
    let entry = ok(&core, "archive.entry.get", json!({"entry_id": "e"})).await;
    assert_eq!(entry["revisions"][0]["title"], "保存的标题");
}

/// A notification extension that fails is isolated: the caller gets a false result and the
/// base archive stays readable. Optional extensions never become a prerequisite.
#[tokio::test]
async fn notification_failure_is_isolated() {
    let (dir, core) = core().await;
    assert!(
        !core
            .notify("absent-extension", "job.failed", "标题", "摘要", 1)
            .await,
        "an unavailable notification extension is not a caller failure"
    );
    core.db
        .write(|tx| {
            tx.execute(
                "INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('a','weibo','1','t')",
                [],
            )?;
            tx.execute(
                "INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES('e','weibo','post','1','a','t')",
                [],
            )?;
            tx.execute(
                "INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at) VALUES('r','e','d','标题','正文','t')",
                [],
            )?;
            Ok(())
        })
        .await
        .unwrap();
    let entry = ok(&core, "archive.entry.get", json!({"entry_id": "e"})).await;
    assert_eq!(entry["revisions"][0]["title"], "标题");
    drop(dir);
}

#[tokio::test]
async fn a_failed_plugin_removal_stays_retryable_and_keeps_the_archive() {
    let (dir, core) = core().await;
    let v1 = plugin(
        &dir.path().join("src"),
        "weibo-extra",
        "1.0.0",
        "0.1",
        &[],
        b"one",
    );
    let v2 = plugin(
        &dir.path().join("src"),
        "weibo-extra",
        "1.1.0",
        "0.1",
        &[],
        b"two",
    );
    for source in [&v1, &v2] {
        ok(
            &core,
            "plugin.install",
            json!({"source_dir": source.to_string_lossy()}),
        )
        .await;
    }
    ok(
        &core,
        "plugin.activate",
        json!({"plugin_id": "weibo-extra", "version": "1.1.0"}),
    )
    .await;
    core.db
        .write(|tx| {
            tx.execute(
                "INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('a','weibo','1','t')",
                [],
            )?;
            tx.execute(
                "INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES('e','weibo','post','1','a','t')",
                [],
            )?;
            tx.execute(
                "INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at) VALUES('r','e','d','保存的标题','正文','t')",
                [],
            )?;
            Ok(())
        })
        .await
        .unwrap();
    let release_dir = dir
        .path()
        .join("data")
        .join("plugins")
        .join("weibo-extra")
        .join("1.0.0");
    #[cfg(windows)]
    let held = {
        use std::os::windows::fs::OpenOptionsExt;
        std::fs::OpenOptions::new()
            .read(true)
            .share_mode(0)
            .open(release_dir.join("plugin.bin"))
            .unwrap()
    };
    #[cfg(not(windows))]
    let held = std::fs::File::open(release_dir.join("plugin.bin")).unwrap();
    assert!(
        call(
            &core,
            "plugin.remove",
            json!({"plugin_id": "weibo-extra", "version": "1.0.0"}),
        )
        .await
        .is_err(),
        "a locked file is reported, not treated as removed"
    );
    let releases = ok(
        &core,
        "plugin.releases",
        json!({"plugin_id": "weibo-extra"}),
    )
    .await;
    assert!(
        releases["items"]
            .as_array()
            .unwrap()
            .iter()
            .any(|r| { r["version"] == "1.0.0" && r["state"] == "removing" }),
        "{releases}"
    );
    assert!(release_dir.join("plugin.bin").exists());
    let entry = ok(&core, "archive.entry.get", json!({"entry_id": "e"})).await;
    assert_eq!(entry["revisions"][0]["title"], "保存的标题");
    drop(held);
    ok(
        &core,
        "plugin.remove",
        json!({"plugin_id": "weibo-extra", "version": "1.0.0"}),
    )
    .await;
    assert!(!release_dir.exists());
    let releases = ok(
        &core,
        "plugin.releases",
        json!({"plugin_id": "weibo-extra"}),
    )
    .await;
    assert!(
        releases["items"]
            .as_array()
            .unwrap()
            .iter()
            .all(|r| r["version"] != "1.0.0")
    );
    let entry = ok(&core, "archive.entry.get", json!({"entry_id": "e"})).await;
    assert_eq!(entry["revisions"][0]["title"], "保存的标题");
}

#[cfg(windows)]
#[tokio::test]
async fn a_locked_plugin_tombstone_does_not_block_archive_startup() {
    use std::os::windows::fs::OpenOptionsExt;
    let dir = tempfile::tempdir().unwrap();
    let profile = Profile {
        data_root: dir.path().join("data"),
        archive_root: dir.path().join("archive"),
        vault_dir: Some(dir.path().join("vault")),
        ..Default::default()
    };
    let core = Core::open(profile.clone()).await.unwrap();
    let path = profile.data_root.join("plugins/weibo-extra/1.0.1");
    let trash = path.with_file_name(".removing-1.0.1");
    std::fs::create_dir_all(&trash).unwrap();
    std::fs::write(trash.join("plugin.bin"), b"left").unwrap();
    core.db.write(move |tx| {
        tx.execute("INSERT INTO plugin_releases(plugin_id,version,manifest_json,path,digest,protocol,capabilities_json,state,installed_at) VALUES('weibo-extra','1.0.1','{}',?1,'d','0.1','[]','installed','t')", [path.to_string_lossy().to_string()])?;
        tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('a','weibo','1','t')", [])?;
        tx.execute("INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES('e','weibo','post','1','a','t')", [])?;
        tx.execute("INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at) VALUES('r','e','d','档案仍可读','正文','t')", [])?;
        Ok(())
    }).await.unwrap();
    core.shutdown().await;
    let held = std::fs::OpenOptions::new()
        .read(true)
        .share_mode(0)
        .open(trash.join("plugin.bin"))
        .unwrap();
    let reopened = Core::open(profile).await;
    assert!(
        reopened.is_ok(),
        "optional cleanup must not prevent Core opening: {:?}",
        reopened.err()
    );
    let reopened = reopened.unwrap();
    let entry = ok(&reopened, "archive.entry.get", json!({"entry_id": "e"})).await;
    assert_eq!(entry["revisions"][0]["title"], "档案仍可读");
    let releases = ok(
        &reopened,
        "plugin.releases",
        json!({"plugin_id": "weibo-extra"}),
    )
    .await;
    assert_eq!(releases["items"][0]["state"], "removing");
    drop(held);
    crate::plugin_install::reconcile_removals(&reopened)
        .await
        .unwrap();
    let releases = ok(
        &reopened,
        "plugin.releases",
        json!({"plugin_id": "weibo-extra"}),
    )
    .await;
    assert_eq!(releases["items"].as_array().unwrap().len(), 0);
}

#[tokio::test]
async fn removal_reconciles_after_files_are_gone_but_the_record_remains() {
    let dir = tempfile::tempdir().unwrap();
    let profile = Profile {
        data_root: dir.path().join("data"),
        archive_root: dir.path().join("archive"),
        vault_dir: Some(dir.path().join("vault")),
        ..Default::default()
    };
    let core = Core::open(profile.clone()).await.unwrap();
    let path = profile.data_root.join("plugins/weibo-extra/1.0.1");
    core.db.write(move |tx| {
        tx.execute("INSERT INTO plugin_releases(plugin_id,version,manifest_json,path,digest,protocol,capabilities_json,state,installed_at) VALUES('weibo-extra','1.0.1','{}',?1,'d','0.1','[]','installed','t')", [path.to_string_lossy().to_string()])?;
        crate::store::plugin_releases::begin_remove(tx, "weibo-extra", "1.0.1", "t")?;
        Ok(())
    }).await.unwrap();
    core.shutdown().await;
    let reopened = Core::open(profile).await.unwrap();
    let releases = ok(
        &reopened,
        "plugin.releases",
        json!({"plugin_id": "weibo-extra"}),
    )
    .await;
    assert_eq!(
        releases["items"].as_array().unwrap().len(),
        0,
        "post-delete/pre-DB crash must not keep a dead release"
    );
}

#[tokio::test]
async fn activation_and_removal_of_the_same_release_are_mutually_exclusive() {
    let (dir, core) = core().await;
    let v1 = plugin(
        &dir.path().join("src"),
        "weibo-extra",
        "1.0.0",
        "0.1",
        &[],
        b"one",
    );
    let v2 = plugin(
        &dir.path().join("src"),
        "weibo-extra",
        "1.1.0",
        "0.1",
        &[],
        b"two",
    );
    for source in [&v1, &v2] {
        ok(
            &core,
            "plugin.install",
            json!({"source_dir": source.to_string_lossy()}),
        )
        .await;
    }
    ok(
        &core,
        "plugin.activate",
        json!({"plugin_id": "weibo-extra", "version": "1.0.0"}),
    )
    .await;
    let (activated, removed) = tokio::join!(
        crate::plugin_install::activate(&core, "weibo-extra", "1.1.0"),
        crate::plugin_install::remove(&core, "weibo-extra", "1.1.0")
    );
    assert_ne!(
        activated.is_ok(),
        removed.is_ok(),
        "activation and removal cannot both commit: {activated:?} / {removed:?}"
    );
    let releases = ok(
        &core,
        "plugin.releases",
        json!({"plugin_id": "weibo-extra"}),
    )
    .await;
    let active = releases["items"]
        .as_array()
        .unwrap()
        .iter()
        .find(|release| release["state"] == "active")
        .expect("one active release remains");
    assert!(
        PathBuf::from(active["path"].as_str().unwrap()).is_dir(),
        "the active release still exists: {releases}"
    );
}

#[tokio::test]
async fn a_removal_interrupted_after_the_directory_is_gone_is_reconciled_at_startup() {
    let dir = tempfile::tempdir().unwrap();
    let profile = Profile {
        data_root: dir.path().join("data"),
        archive_root: dir.path().join("archive"),
        vault_dir: Some(dir.path().join("vault")),
        ..Default::default()
    };
    let core = Core::open(profile.clone()).await.unwrap();
    let gone = profile
        .data_root
        .join("plugins")
        .join("weibo-extra")
        .join("1.0.1")
        .to_string_lossy()
        .to_string();
    let trash = profile
        .data_root
        .join("plugins")
        .join("weibo-extra")
        .join(".removing-1.0.1");
    std::fs::create_dir_all(&trash).unwrap();
    std::fs::write(trash.join("plugin.bin"), b"left").unwrap();
    core.db
        .write(move |tx| {
            tx.execute("INSERT INTO plugin_releases(plugin_id,version,manifest_json,path,digest,protocol,capabilities_json,state,installed_at) VALUES('weibo-extra','1.0.1','{}',?1,'d','0.1','[]','installed','t')",[gone])?;
            Ok(())
        })
        .await
        .unwrap();
    core.shutdown().await;
    let reopened = Core::open(profile).await.unwrap();
    let releases = ok(
        &reopened,
        "plugin.releases",
        json!({"plugin_id": "weibo-extra"}),
    )
    .await;
    assert_eq!(releases["items"].as_array().unwrap().len(), 0);
    assert!(
        !trash.exists(),
        "leftover files of an interrupted removal are gone"
    );
}

#[tokio::test]
async fn capability_results_are_typed_and_a_failure_never_touches_the_archive() {
    let (dir, core) = core().await;
    let source = plugin(
        &dir.path().join("src"),
        "weibo-extra",
        "1.0.0",
        "0.1",
        &[
            "login_assist",
            "notification",
            "storage",
            "content_processing",
        ],
        b"one",
    );
    ok(
        &core,
        "plugin.install",
        json!({"source_dir": source.to_string_lossy()}),
    )
    .await;
    ok(
        &core,
        "plugin.activate",
        json!({"plugin_id": "weibo-extra", "version": "1.0.0"}),
    )
    .await;
    core.db
        .write(|tx| {
            tx.execute(
                "INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('a','weibo','1','t')",
                [],
            )?;
            tx.execute(
                "INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES('e','weibo','post','1','a','t')",
                [],
            )?;
            tx.execute(
                "INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at) VALUES('r','e','d','原始标题','原始正文','t')",
                [],
            )?;
            Ok(())
        })
        .await
        .unwrap();
    for (kind, method, params, result) in [
        (
            "login_assist",
            "begin",
            json!({"site": "weibo"}),
            json!({"state": "waiting", "prompt": "scan"}),
        ),
        (
            "notification",
            "send",
            json!({"event": "x"}),
            json!({"delivered": true}),
        ),
        (
            "storage",
            "prepare",
            json!({"kind_id": "k", "expected_bytes": 1}),
            json!({"handle": "h", "max_bytes": 1}),
        ),
        (
            "content_processing",
            "process",
            json!({"input_kind": "image", "output_kind": "text", "bytes": 1}),
            json!({"derived_id": "d", "output_kind": "text", "bytes": 1}),
        ),
    ] {
        crate::core::set_test_capability_result(result.clone());
        let value = core
            .capability_call("weibo-extra", kind, method, params)
            .await
            .unwrap_or_else(|e| panic!("{kind}.{method}: {e}"));
        assert_eq!(value, result);
    }
    for bad in [
        json!(null),
        json!([]),
        json!("ok"),
        json!({}),
        json!({"delivered": "true"}),
        json!({"delivered": true, "undeclared": 1}),
    ] {
        crate::core::set_test_capability_result(bad.clone());
        assert!(
            core.capability_call("weibo-extra", "notification", "send", json!({"event": "x"}))
                .await
                .is_err(),
            "{bad}"
        );
    }
    crate::core::set_test_capability_result(json!({"delivered": false}));
    assert!(
        !core
            .notify("weibo-extra", "job.failed", "标题", "摘要", 1)
            .await
    );
    crate::core::set_test_capability_result(json!({"delivered": true}));
    assert!(
        core.notify("weibo-extra", "job.failed", "标题", "摘要", 1)
            .await
    );
    let entry = ok(&core, "archive.entry.get", json!({"entry_id": "e"})).await;
    assert_eq!(entry["revisions"][0]["title"], "原始标题");
    assert_eq!(entry["revisions"][0]["body"], "原始正文");
}

#[tokio::test]
async fn optional_extensions_off_keeps_base_archive() {
    let (dir, core) = core().await;
    let source = plugin(
        &dir.path().join("src"),
        "weibo-extra",
        "1.0.0",
        "0.1",
        &[
            "login_assist",
            "notification",
            "storage",
            "content_processing",
        ],
        b"one",
    );
    ok(
        &core,
        "plugin.install",
        json!({"source_dir": source.to_string_lossy()}),
    )
    .await;
    ok(
        &core,
        "plugin.activate",
        json!({"plugin_id": "weibo-extra", "version": "1.0.0"}),
    )
    .await;
    let capabilities = ok(&core, "plugin.capabilities", json!({})).await;
    assert_eq!(capabilities["contracts"].as_array().unwrap().len(), 4);
    // Every optional extension is switched off; the base acquisition path never consults one.
    for kind in [
        "login_assist",
        "notification",
        "storage",
        "content_processing",
    ] {
        ok(
            &core,
            "plugin.capability_set",
            json!({"plugin_id": "weibo-extra", "kind": kind, "enabled": false}),
        )
        .await;
    }
    let active = ok(&core, "plugin.capabilities", json!({})).await;
    assert!(
        active["active"][0]["capabilities"]
            .as_array()
            .unwrap()
            .iter()
            .all(|c| c["enabled"] == false),
        "{active}"
    );
    assert!(
        core.capability_call("weibo-extra", "notification", "send", json!({"event": "x"}))
            .await
            .is_err()
    );
    assert!(
        !core
            .notify("weibo-extra", "job.failed", "标题", "摘要", 1)
            .await
    );
    // The production scan storage path saves a synthetic work while every extension is off.
    core.db
        .write(|tx| {
            tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('a','weibo','1','t')",[])?;
            tx.execute("INSERT INTO subscriptions(subscription_id,account_id,content_type,range_json,created_at,updated_at) VALUES('s','a','post','{}','t','t')",[])?;
            tx.execute("INSERT INTO credentials_state(credential_id,platform,subject,vault_ref,status,capability,created_at,updated_at) VALUES('cred','weibo','owner','vault-ref','valid','{}','t','t')",[])?;
            Ok(())
        })
        .await
        .unwrap();
    crate::jobs::enqueue(
        &core.db,
        "scan-optional-off".into(),
        "scan",
        "s",
        json!({}),
        1,
        1,
        0,
    )
    .await
    .unwrap();
    let job = crate::jobs::claim_due(&core.db, "optional-off", 1)
        .await
        .unwrap()
        .remove(0);
    job.mark_running(&core.db).await.unwrap();
    let item: trace_contracts::plugin::EnumeratedEntry = serde_json::from_value(json!({
        "native_entry_key": "77",
        "native_kind": "post",
        "title": "基础归档作品",
        "summary_body": "基础正文",
        "published_at": null
    }))
    .unwrap();
    crate::core::process_scan_page(
        &core,
        &job,
        "weibo",
        "s",
        "incremental",
        vec![item],
        Some("one".into()),
        &json!({}),
    )
    .await
    .unwrap();
    let timeline = ok(
        &core,
        "archive.timeline",
        json!({"namespace": "weibo", "limit": 10}),
    )
    .await;
    assert_eq!(timeline["items"][0]["title"], "基础归档作品");
    let output = dir.path().join("export-off");
    crate::archive_export::export(&core, &output, false, None)
        .await
        .unwrap();
    let entries = std::fs::read_to_string(output.join("entries.jsonl")).unwrap();
    assert!(entries.contains("基础归档作品"), "{entries}");
    let credentials = ok(&core, "credential.list", json!({"platform": "weibo"})).await;
    assert_eq!(credentials["items"][0]["credential_id"], "cred");
}
