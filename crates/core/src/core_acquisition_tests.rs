use super::*;
use crate::{acquisitions, upgrades};
use serde_json::json;
use trace_contracts::acquisition::{
    AcquisitionContext, AuthenticationState as Auth, UpgradeAction, UpgradeDecision,
};

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn c11_metadata_pacing_fifo_and_urgent_budget_prevent_consumer_starvation() {
    let dir = tempfile::tempdir().unwrap();
    let core = Core::open(Profile {
        data_root: dir.path().join("data"),
        archive_root: dir.path().join("archive"),
        vault_dir: Some(dir.path().join("vault")),
        metadata_jitter_ms: 0,
        ..Default::default()
    })
    .await
    .unwrap();
    core.metadata_pacing("bilibili").await;
    let grants = Arc::new(Mutex::new(Vec::new()));
    let mut regular = Vec::new();
    for index in 0..4 {
        let core2 = core.clone();
        let output = grants.clone();
        regular.push(tokio::spawn(async move {
            core2.metadata_pacing("bilibili").await;
            output.lock().unwrap().push((false, index));
        }));
        tokio::time::timeout(Duration::from_secs(1), async {
            loop {
                if core
                    .metadata_regular
                    .lock()
                    .unwrap()
                    .get("bilibili")
                    .copied()
                    .unwrap_or(0)
                    == index + 1
                {
                    break;
                }
                tokio::task::yield_now().await;
            }
        })
        .await
        .unwrap();
    }
    // Cancelling the oldest request releases its turn without poisoning the lane.
    let cancelled = regular.remove(0);
    cancelled.abort();
    assert!(cancelled.await.is_err());
    let mut urgent = Vec::new();
    for index in 0..5 {
        let core2 = core.clone();
        let output = grants.clone();
        urgent.push(tokio::spawn(async move {
            core2.metadata_pacing_priority("bilibili", true).await;
            output.lock().unwrap().push((true, index));
        }));
        tokio::time::timeout(Duration::from_secs(1), async {
            loop {
                if core
                    .metadata_urgent
                    .lock()
                    .unwrap()
                    .get("bilibili")
                    .copied()
                    .unwrap_or(0)
                    == index + 1
                {
                    break;
                }
                tokio::task::yield_now().await;
            }
        })
        .await
        .unwrap();
    }
    tokio::time::timeout(Duration::from_millis(500), core.metadata_pacing("douyin"))
        .await
        .expect("one site's queue must not hold the other site");
    tokio::time::timeout(Duration::from_secs(30), async {
        for task in regular.into_iter().chain(urgent) {
            task.await.unwrap();
        }
    })
    .await
    .expect("every admitted consumer has a bounded turn");
    let actual = grants.lock().unwrap().clone();
    assert_eq!(
        actual
            .iter()
            .filter(|(urgent, _)| !*urgent)
            .map(|(_, i)| *i)
            .collect::<Vec<_>>(),
        vec![1, 2, 3],
        "regular FIFO: {actual:?}"
    );
    assert_eq!(
        actual
            .iter()
            .filter(|(urgent, _)| *urgent)
            .map(|(_, i)| *i)
            .collect::<Vec<_>>(),
        vec![0, 1, 2, 3, 4],
        "urgent FIFO: {actual:?}"
    );
    assert!(
        actual.iter().position(|(urgent, _)| !*urgent).unwrap() <= 4,
        "urgent traffic must leave a regular turn: {actual:?}"
    );
    assert_eq!(
        core.metadata_regular.lock().unwrap().get("bilibili"),
        Some(&0)
    );
    assert_eq!(
        core.metadata_urgent.lock().unwrap().get("bilibili"),
        Some(&0)
    );
    core.shutdown().await;
}

async fn core_fixture() -> (tempfile::TempDir, Arc<Core>) {
    let dir = tempfile::tempdir().unwrap();
    let core = Core::open(Profile {
        data_root: dir.path().join("data"),
        archive_root: dir.path().join("archive"),
        vault_dir: Some(dir.path().join("vault")),
        adapter_dir: Some(
            std::path::PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../target/debug"),
        ),
        test_allowed_hosts: vec!["127.0.0.1".into(), "fixture.douyinvod.com".into()],
        ..Default::default()
    })
    .await
    .unwrap();
    core.db.write(|tx|{
        tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('a','douyin','MS4test','t')",[])?;
        tx.execute("INSERT INTO subscriptions(subscription_id,account_id,content_type,range_json,created_at,updated_at) VALUES('s','a','video','{}','t','t')",[])?;
        tx.execute("INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES('e','douyin','video','71','a','t')",[])?;
        tx.execute("INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at) VALUES('r','e','d','认证历史','正文','t')",[])?;
        for role in ["video","audio"] {tx.execute("INSERT INTO media_items(media_id,revision_id,role,position,source_attachment_id,group_key,required) VALUES(?1,'r',?1,0,?1,'g',1)",[role])?;}
        Ok(())
    }).await.unwrap();
    (dir, core)
}
async fn work(core: &Core, key: &str, auth: Auth) -> ClaimedJob {
    jobs::enqueue(
        &core.db,
        key.into(),
        "media",
        "e",
        json!({"entry_id":"e","revision_id":"r","group":"g","subscription_id":"s"}),
        0,
        1,
        0,
    )
    .await
    .unwrap();
    let mut job = jobs::claim_due(&core.db, "r2", 1).await.unwrap().remove(0);
    job.mark_running(&core.db).await.unwrap();
    let context = AcquisitionContext {
        context_id: format!("ctx-{}", uuid::Uuid::new_v4()),
        auth_state: auth,
        credential_id: job.payload["execution_scope"]["credential_id"]
            .as_str()
            .map(str::to_owned),
        credential_generation: job.payload["execution_scope"]["credential_generation"].as_i64(),
        validated_at: (auth == Auth::Authenticated).then(jobs::now),
        basis: "isolated_test_proof".into(),
        started_at: jobs::now(),
    };
    let guard = job.clone();
    let saved = context.clone();
    core.db
        .write(move |tx| {
            guard.check(tx)?;
            acquisitions::insert_context(tx, &guard, "e", &saved)
        })
        .await
        .unwrap();
    job.payload["acquisition_context_id"] = json!(context.context_id);
    job
}
async fn object(core: &Core, job: &ClaimedJob, media: &str, version: &str) -> String {
    let op = OperationId::new(format!("test-{}", uuid::Uuid::new_v4()));
    let bytes = format!("{media}/{version}/verified synthetic bytes");
    let mut writer = core
        .objects
        .start_receiving(
            &core.db,
            ReceiveParams {
                job_id: Some(job.job_id.clone()),
                subject_kind: "media_variant".into(),
                subject_id: media.into(),
                generation: job.generation,
                mime: Some("application/octet-stream".into()),
                expected_size: Some(bytes.len() as i64),
                expected_sha256: None,
                source_validator: None,
            },
            &op,
        )
        .await
        .unwrap();
    writer
        .write_chunk(&core.db, &op, bytes.as_bytes())
        .await
        .unwrap();
    let (_, sha) = core
        .objects
        .finish_and_validate(&core.db, &op, writer)
        .await
        .unwrap();
    core.objects
        .install_object(&core.db, &op, &sha)
        .await
        .unwrap();
    core.objects.commit(&core.db,&op,&CommitInfo {job:Some(job.clone()),media_id:media.into(),source_representation:version.into(),claimed_spec:json!({})},Some(json!({"width":if version=="old"{640}else{1920},"height":if version=="old"{360}else{1080},"fps":30.0,"duration_ms":1000,"codec":"h264","audio_tracks":1,"audio_channels":2}))).await.unwrap();
    sha
}
async fn candidate(complete: bool) -> (tempfile::TempDir, Arc<Core>, ClaimedJob, String) {
    candidate_version(complete, "new").await
}
async fn candidate_version(
    complete: bool,
    version: &str,
) -> (tempfile::TempDir, Arc<Core>, ClaimedJob, String) {
    let (dir, core) = core_fixture().await;
    let old = work(&core, "old", Auth::Anonymous).await;
    for media in ["video", "audio"] {
        object(&core, &old, media, "old").await;
    }
    old.succeed(&core.db).await.unwrap();
    core.db.write(|tx|{
        tx.execute("INSERT INTO credentials_state(credential_id,platform,vault_ref,status,created_at,updated_at) VALUES('credential','douyin','unused-test-reference','valid','t','t')",[])?;
        tx.execute("UPDATE subscriptions SET credential_id='credential',config_version=2 WHERE subscription_id='s'",[])?;Ok(())
    }).await.unwrap();
    let mut job = work(&core, "new", Auth::Authenticated).await;
    let id = upgrades::prepare(&core, &job, "r", "g")
        .await
        .unwrap()
        .unwrap();
    job.payload["upgrade_candidate_id"] = json!(id);
    object(&core, &job, "video", version).await;
    if complete {
        object(&core, &job, "audio", version).await;
        upgrades::finish(&core, &job).await.unwrap();
    }
    (dir, core, job, id)
}
async fn choice(core: &Core, id: &str, decision: UpgradeAction) -> UpgradeDecision {
    let page = upgrades::list(core, json!({})).await.unwrap();
    let item = page["items"]
        .as_array()
        .unwrap()
        .iter()
        .find(|v| v["candidate_id"] == id)
        .unwrap();
    assert_eq!(item["state"], "pending", "{item}");
    UpgradeDecision {
        candidate_id: id.into(),
        decision,
        expected: item["expected"].clone(),
    }
}
async fn selections(core: &Core) -> Vec<String> {
    core.db.read(|c|Ok(c.prepare("SELECT v.source_representation FROM asset_selections s JOIN media_variants v ON v.variant_id=s.variant_id ORDER BY s.media_id")?.query_map([],|r|r.get::<_,String>(0))?.collect::<Result<Vec<_>,_>>()?)).await.unwrap()
}

#[tokio::test]
async fn c14_pending_retry_reuses_only_its_own_variant_and_legacy_token_shape() {
    let (_dir, core, job, id) = candidate(false).await;
    let variant=core.db.read(|c|Ok(c.query_row("SELECT variant_id FROM media_variants WHERE media_id='video' AND source_representation='new'",[],|r|r.get::<_,String>(0))?)).await.unwrap();
    job.requeue(&core.db).await.unwrap();
    let mut resumed = jobs::claim_due(&core.db, "restarted", 1)
        .await
        .unwrap()
        .remove(0);
    resumed.mark_running(&core.db).await.unwrap();
    resumed.payload["acquisition_context_id"] = job.payload["acquisition_context_id"].clone();
    assert_eq!(
        upgrades::prepare(&core, &resumed, "r", "g")
            .await
            .unwrap()
            .unwrap(),
        id
    );
    resumed.payload["upgrade_candidate_id"] = json!(id);
    upgrades::reused(&core, &resumed, "video", &variant, true)
        .await
        .unwrap();
    object(&core, &resumed, "audio", "new").await;
    upgrades::finish(&core, &resumed).await.unwrap();
    let decision = choice(&core, &id, UpgradeAction::Accept).await;
    assert!(
        decision.expected.get("consumer_request").is_none(),
        "nonshared schema-5 confirmation shape must remain compatible"
    );
    resumed.succeed(&core.db).await.unwrap();
    let other = work(&core, "different-completion", Auth::Authenticated).await;
    assert!(
        upgrades::reused(&core, &other, "video", &variant, true)
            .await
            .is_err(),
        "another logical task cannot borrow a pending acquisition"
    );
    other.succeed(&core.db).await.unwrap();
    core.db
        .read(|c| {
            assert_eq!(
                c.query_row(
                    "SELECT count(*) FROM media_variants WHERE source_representation='new'",
                    [],
                    |r| r.get::<_, i64>(0)
                )?,
                2
            );
            Ok(())
        })
        .await
        .unwrap();
    upgrades::decide(&core, vec![decision]).await.unwrap();
    assert_eq!(selections(&core).await, vec!["new", "new"]);
    core.shutdown().await;
}

#[tokio::test]
async fn c11_upgrade_accept_reject_keep_both_preserve_old_until_explicit_atomic_decision() {
    for action in [
        UpgradeAction::Accept,
        UpgradeAction::Reject,
        UpgradeAction::KeepBoth,
    ] {
        let (_dir, core, job, id) = candidate(true).await;
        assert_eq!(selections(&core).await, vec!["old", "old"]);
        // Background quality maintenance cannot select pending authenticated variants.
        core.db
            .write(|tx| crate::quality::evaluate(tx, "r", false, 0))
            .await
            .unwrap();
        assert_eq!(selections(&core).await, vec!["old", "old"]);
        let decision = choice(&core, &id, action).await;
        let first = upgrades::decide(&core, vec![decision.clone()])
            .await
            .unwrap();
        assert_eq!(first["items"][0]["duplicate"], false);
        assert_eq!(
            selections(&core).await,
            if matches!(action, UpgradeAction::Reject) {
                vec!["old", "old"]
            } else {
                vec!["new", "new"]
            }
        );
        let again = upgrades::decide(&core, vec![decision]).await.unwrap();
        assert_eq!(again["items"][0]["duplicate"], true);
        if matches!(action, UpgradeAction::KeepBoth) {
            assert_eq!(
                core.db
                    .read(|c| Ok(c.query_row(
                        "SELECT count(*) FROM variant_retention WHERE retained=1 AND pinned=1",
                        [],
                        |r| r.get::<_, i64>(0)
                    )?))
                    .await
                    .unwrap(),
                4
            );
        }
        assert_eq!(
            core.db
                .read(|c| Ok(c.query_row(
                    "SELECT count(*) FROM resource_acquisitions",
                    [],
                    |r| r.get::<_, i64>(0)
                )?))
                .await
                .unwrap(),
            4
        );
        job.succeed(&core.db).await.unwrap();
        core.shutdown().await;
    }
}

#[tokio::test]
async fn c11_upgrade_stale_confirmations_reject_selection_revision_generations_cancel_and_corruption()
 {
    for change in [
        "pin",
        "policy",
        "revision",
        "delete",
        "credential",
        "pause_resume",
        "module",
        "cancel",
        "new_download",
        "missing_file",
    ] {
        let (_dir, core, job, id) = candidate(true).await;
        let decision = choice(&core, &id, UpgradeAction::Accept).await;
        if change == "missing_file" {
            let sha=core.db.read(|c|Ok(c.query_row("SELECT substr(blob_id,8) FROM media_variants WHERE source_representation='new' LIMIT 1",[],|r|r.get::<_,String>(0))?)).await.unwrap();
            let path = core.objects.object_path(&sha).unwrap();
            assert!(path.starts_with(&core.profile.archive_root));
            std::fs::remove_file(path).unwrap();
        } else {
            let name = change.to_owned();
            let id = job.job_id.clone();
            core.db.write(move|tx|{match name.as_str(){
                "pin"=>{tx.execute("UPDATE asset_selections SET pinned=1",[])?;},
                "policy"=>{tx.execute("INSERT OR REPLACE INTO meta VALUES('quality_policy','{\"prefer_fps\":true}')",[])?;},
                "revision"=>{tx.execute("INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at) VALUES('edited','e','edited','edit','edit','z')",[])?;},
                "delete"=>{tx.execute("UPDATE entries SET lifecycle='deleted',deletion_generation=1",[])?;},
                "credential"=>{tx.execute("UPDATE credentials_state SET generation=generation+1",[])?;},
                "pause_resume"=>{jobs::set_subscription_enabled(tx,"s",false)?;jobs::set_subscription_enabled(tx,"s",true)?;},
                "module"=>{tx.execute("INSERT INTO plugins_state(plugin_id,platform,version,protocol,binary_path,enabled,updated_at) VALUES('douyin','douyin','1','1','none',0,'t')",[])?;},
                "cancel"=>{tx.execute("UPDATE jobs SET state='cancelled',target_generation=target_generation+1,lease_owner=NULL WHERE job_id=?1",[id])?;},
                "new_download"=>{tx.execute("UPDATE media_items SET resource_generation=resource_generation+1 WHERE media_id='video'",[])?;},_=>unreachable!()
            }Ok(())}).await.unwrap();
        }
        assert!(
            upgrades::decide(&core, vec![decision]).await.is_err(),
            "{change}"
        );
        assert_eq!(selections(&core).await, vec!["old", "old"], "{change}");
        core.shutdown().await;
    }
}

#[tokio::test]
async fn c11_incomplete_or_cancelled_candidate_never_borrows_old_required_tracks() {
    let (_dir, core, job, id) = candidate(false).await;
    assert!(upgrades::finish(&core, &job).await.is_err());
    assert_eq!(selections(&core).await, vec!["old", "old"]);
    job.cancel(&core.db).await.unwrap();
    assert!(upgrades::finish(&core, &job).await.is_err());
    let page = upgrades::list(&core, json!({})).await.unwrap();
    assert_eq!(page["items"][0]["candidate_id"], id);
    assert_eq!(page["items"][0]["state"], "cancelled");
    core.db
        .write(|tx| crate::quality::evaluate(tx, "r", false, 0))
        .await
        .unwrap();
    assert_eq!(selections(&core).await, vec!["old", "old"]);
    core.shutdown().await;
}

#[tokio::test]
async fn c11_completed_candidate_cancel_and_failed_attempt_keep_old_resources_and_history() {
    for parent in [false, true] {
        let (_dir, core, job, id) = candidate(true).await;
        let decision = choice(&core, &id, UpgradeAction::Accept).await;
        job.succeed(&core.db).await.unwrap();
        let job_id = job.job_id.clone();
        let cancel_id = if parent {
            core.db.write(move |tx| {
                tx.execute("INSERT INTO jobs(job_id,job_key,job_type,subject_id,payload,state,created_at,updated_at) VALUES('completion-parent','completion-parent','auth_completion','credential','{}','succeeded','t','t')",[])?;
                tx.execute("UPDATE jobs SET payload=json_set(payload,'$.completion_parent','completion-parent') WHERE job_id=?1",[job_id])?;
                Ok(())
            }).await.unwrap();
            "completion-parent".to_owned()
        } else {
            job_id
        };
        assert!(
            core.db
                .write(move |tx| jobs::cancel_work(tx, &cancel_id))
                .await
                .unwrap()
        );
        assert!(upgrades::decide(&core, vec![decision]).await.is_err());
        assert_eq!(
            upgrades::list(&core, json!({})).await.unwrap()["items"][0]["state"],
            "cancelled"
        );
        assert_eq!(selections(&core).await, vec!["old", "old"]);
        assert_eq!(
            core.db
                .read(|c| Ok(
                    c.query_row("SELECT count(*) FROM attempts", [], |r| r.get::<_, i64>(0))?
                ))
                .await
                .unwrap(),
            0
        );
        core.shutdown().await;
    }
    let (_dir, core, job, _id) = candidate(false).await;
    job.fail(
        &core.db,
        &trace_domain::errors::FailureClass::PermissionDenied,
        "Cookie=must-not-be-retained",
        None,
    )
    .await
    .unwrap();
    let page = upgrades::list(&core, json!({})).await.unwrap();
    assert_eq!(page["items"][0]["state"], "failed");
    assert_eq!(page["items"][0]["reason"], "permission_denied");
    let history = acquisitions::list(&core, json!({"entry_id":"e"}))
        .await
        .unwrap();
    assert_eq!(history["items"][0]["outcome"], "permission_denied");
    assert_eq!(history["items"][0]["auth_state"], "authenticated");
    assert!(!history.to_string().contains("must-not-be-retained"));
    assert_eq!(selections(&core).await, vec!["old", "old"]);
    core.shutdown().await;
}

#[tokio::test]
async fn c11_edited_completion_and_variant_labels_are_explicit_without_replacing_history() {
    let (_dir, core, mut job, id) = candidate(false).await;
    job.payload["expected_revision_id"] = json!("previous-revision");
    object(&core, &job, "audio", "new").await;
    upgrades::finish(&core, &job).await.unwrap();
    let page = upgrades::list(&core, json!({})).await.unwrap();
    assert_eq!(
        page["items"][0]["reason"],
        "content_edited_requires_decision"
    );
    assert_eq!(page["items"][0]["state"], "pending");
    assert_eq!(page["items"][0]["candidate_id"], id);
    let media = crate::ipc::archive::page(
        &core.db,
        serde_json::from_value(json!({"entry_id":"e","section":"media"})).unwrap(),
    )
    .await
    .unwrap();
    assert_eq!(media["items"].as_array().unwrap().len(), 4);
    for variant in media["items"].as_array().unwrap() {
        let old = variant["representation"] == "old";
        assert_eq!(
            variant["authentication"]["state"],
            if old { "anonymous" } else { "authenticated" }
        );
        assert_eq!(
            variant["approval_state"],
            if old { "automatic" } else { "pending" }
        );
        assert_eq!(variant["selected"], old);
    }
    assert_eq!(selections(&core).await, vec!["old", "old"]);
    let guard = job.clone();
    core.db.write(move |tx|{
        upgrades::schedule_media(tx,&guard,json!({"entry_id":"e","subscription_id":"s"}),"e","edited-revision","g")?;
        let expected:String=tx.query_row("SELECT json_extract(payload,'$.expected_revision_id') FROM jobs WHERE job_key LIKE 'completion-media:%'",[],|r|r.get(0))?;
        assert_eq!(expected,"previous-revision");
        Ok(())
    }).await.unwrap();
    core.shutdown().await;
}

#[tokio::test]
async fn c11_legacy_recovery_cache_verification_does_not_invent_acquisition_identity() {
    let (_dir, core) = core_fixture().await;
    let job = work(&core, "legacy-resume", Auth::Anonymous).await;
    let mut legacy = job.clone();
    legacy
        .payload
        .as_object_mut()
        .unwrap()
        .remove("acquisition_context_id");
    let sha = object(&core, &legacy, "video", "old").await;
    let variant=core.db.read(move |c|Ok(c.query_row("SELECT variant_id FROM media_variants WHERE media_id='video' AND blob_id=?1 AND source_representation='old'", [format!("sha256:{sha}")], |r|r.get::<_,String>(0))?)).await.unwrap();
    upgrades::reused(&core, &job, "video", &variant, false)
        .await
        .unwrap();
    let history = acquisitions::list(&core, json!({"entry_id":"e"}))
        .await
        .unwrap();
    assert_eq!(history["items"].as_array().unwrap().len(), 1);
    assert_eq!(history["items"][0]["outcome"], "cache_verified");
    assert_eq!(history["items"][0]["auth_state"], "unknown");
    assert_eq!(history["items"][0]["basis"], "legacy_cached_copy_verified");
    let page = crate::ipc::archive::page(
        &core.db,
        serde_json::from_value(json!({"entry_id":"e","section":"media"})).unwrap(),
    )
    .await
    .unwrap();
    let video = page["items"]
        .as_array()
        .unwrap()
        .iter()
        .find(|v| v["role"] == "video")
        .unwrap();
    assert_eq!(video["authentication"]["state"], "unknown");
    core.shutdown().await;
}

#[tokio::test]
async fn c11_restore_invalidates_pending_confirmations_and_retry_finishes_original_acquisition() {
    let (dir, core, job, id) = candidate(true).await;
    let old_confirmation = choice(&core, &id, UpgradeAction::Accept).await;
    job.succeed(&core.db).await.unwrap();
    let backup = crate::storage_ops::backup(&core, &dir.path().join("pending-backup"), true)
        .await
        .unwrap();
    let source = dir
        .path()
        .join("pending-backup")
        .join(backup["backup_id"].as_str().unwrap());
    let result = crate::storage_ops::restore(&core, &source, &dir.path().join("pending-restored"))
        .await
        .unwrap();
    let profile: Profile =
        serde_json::from_slice(&std::fs::read(result["profile_path"].as_str().unwrap()).unwrap())
            .unwrap();
    let restored = Core::open(profile).await.unwrap();
    restored.recover().await.unwrap();
    assert!(
        !restored
            .db
            .read(|c| upgrades::has_pending(c, "e"))
            .await
            .unwrap()
    );
    assert_eq!(
        upgrades::list(&restored, json!({})).await.unwrap()["items"][0]["state"],
        "stale"
    );
    assert!(
        upgrades::decide(&restored, vec![old_confirmation])
            .await
            .is_err()
    );
    assert_eq!(selections(&restored).await, vec!["old", "old"]);
    assert_eq!(
        acquisitions::list(&restored, json!({"entry_id":"e"}))
            .await
            .unwrap()["items"]
            .as_array()
            .unwrap()
            .len(),
        4
    );
    restored.shutdown().await;
    core.shutdown().await;

    let (_dir, core) = core_fixture().await;
    let mut first = work(&core, "same-demand", Auth::Authenticated).await;
    object(&core, &first, "video", "old").await;
    // The same demand still owns the missing audio; completing it cannot invent
    // an upgrade of a previously completed acquisition.
    assert!(
        upgrades::prepare(&core, &first, "r", "g")
            .await
            .unwrap()
            .is_none()
    );
    object(&core, &first, "audio", "old").await;
    assert_eq!(selections(&core).await, vec!["old", "old"]);
    first.payload["completion"] = json!(true);
    assert!(
        upgrades::prepare(&core, &first, "r", "g")
            .await
            .unwrap()
            .is_some()
    );
    core.shutdown().await;
}

#[tokio::test]
async fn c11_authentication_timelines_share_identity_counts_pages_and_immutable_history() {
    let (_dir, core) = core_fixture().await;
    core.db.write(|tx|{
        for n in 0..67 {
            let entry=format!("entry-{n:03}");
            tx.execute("INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at,published_at) VALUES(?1,'douyin','video',?1,'a','t','2026-10-08T01:00:00Z')",[&entry])?;
            tx.execute("INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at) VALUES(?1,?1,'d','同一内容','正文','t')",[&entry])?;
            for state in ["anonymous","authenticated","expired","unknown"] {tx.execute("INSERT INTO acquisition_contexts(context_id,entry_id,auth_state,basis,started_at) VALUES(?1,?2,?3,'fixed_test_observation','t')",rusqlite::params![format!("{entry}:{state}"),entry,state])?;}
        }Ok(())
    }).await.unwrap();
    for state in ["anonymous", "authenticated", "expired", "unknown"] {
        let mut cursor = Value::Null;
        let mut ids = std::collections::BTreeSet::new();
        loop {
            let page=crate::feed::query(&core.db,serde_json::from_value(json!({"authentication":state,"namespace":"douyin","kind":"video","limit":13,"cursor":cursor})).unwrap()).await.unwrap();
            assert_eq!(
                page["counts"]["filtered"],
                if state == "unknown" { 68 } else { 67 }
            );
            for item in page["items"].as_array().unwrap() {
                assert!(ids.insert(item["entry_id"].as_str().unwrap().to_owned()));
            }
            cursor = page["next_cursor"].clone();
            if cursor.is_null() {
                break;
            }
            assert!(crate::feed::query(&core.db,serde_json::from_value(json!({"authentication":if state=="anonymous"{"authenticated"}else{"anonymous"},"cursor":cursor})).unwrap()).await.is_err());
        }
        assert_eq!(ids.len(), if state == "unknown" { 68 } else { 67 });
        let membership = crate::feed::query(
            &core.db,
            serde_json::from_value(json!({"authentication":state,"entry_id":"entry-010"})).unwrap(),
        )
        .await
        .unwrap();
        assert_eq!(membership["counts"]["filtered"], 1);
        assert_eq!(membership["items"][0]["authentication"]["state"], "mixed");
    }
    let protected = core
        .db
        .write(|tx| {
            tx.execute("UPDATE acquisition_contexts SET auth_state='anonymous'", [])?;
            Ok(())
        })
        .await;
    assert!(protected.is_err());
    core.shutdown().await;
}

#[tokio::test]
async fn c11_identical_bytes_keep_two_authentication_histories_without_false_upgrade() {
    let (_dir, core, _job, _id) = candidate_version(true, "old").await;
    let page = upgrades::list(&core, json!({})).await.unwrap();
    assert_eq!(page["items"][0]["state"], "no_improvement");
    assert_eq!(selections(&core).await, vec!["old", "old"]);
    core.db
        .read(|c| {
            assert_eq!(
                c.query_row("SELECT count(*) FROM blobs", [], |r| r.get::<_, i64>(0))?,
                2
            );
            assert_eq!(
                c.query_row("SELECT count(*) FROM resource_acquisitions", [], |r| r
                    .get::<_, i64>(0))?,
                4
            );
            Ok(())
        })
        .await
        .unwrap();
    assert_eq!(
        acquisitions::list(&core, json!({"entry_id":"e"}))
            .await
            .unwrap()["summary"]["state"],
        "mixed"
    );
    core.shutdown().await;
}

#[tokio::test]
async fn c11_pending_upgrade_survives_restart_and_anonymous_fallback_has_new_context() {
    let (_dir, core, job, id) = candidate(true).await;
    let decision = choice(&core, &id, UpgradeAction::Accept).await;
    let original = AcquisitionContext {
        context_id: job.payload["acquisition_context_id"]
            .as_str()
            .unwrap()
            .into(),
        auth_state: Auth::Authenticated,
        credential_id: Some("credential".into()),
        credential_generation: Some(1),
        validated_at: Some(jobs::now()),
        basis: "test_proof".into(),
        started_at: jobs::now(),
    };
    let fallback = acquisitions::resolution_context(
        &core,
        &job,
        "e",
        original.clone(),
        &json!({"authentication":"anonymous"}),
    )
    .await
    .unwrap();
    assert_ne!(fallback.context_id, original.context_id);
    assert_eq!(fallback.auth_state, Auth::Anonymous);
    assert!(fallback.credential_id.is_none());
    let profile = core.profile.clone();
    core.shutdown().await;
    drop(core);
    let reopened = Core::open(profile).await.unwrap();
    assert_eq!(selections(&reopened).await, vec!["old", "old"]);
    upgrades::decide(&reopened, vec![decision]).await.unwrap();
    assert_eq!(selections(&reopened).await, vec!["new", "new"]);
    reopened.shutdown().await;
}

#[tokio::test]
async fn c11_same_blob_distinct_acquisitions_backup_restore_keep_history_without_credentials() {
    let (dir, core, job, id) = candidate(true).await;
    let decision = choice(&core, &id, UpgradeAction::KeepBoth).await;
    upgrades::decide(&core, vec![decision.clone()])
        .await
        .unwrap();
    job.succeed(&core.db).await.unwrap();
    let job = work(&core, "same-bytes", Auth::Authenticated).await;
    object(&core, &job, "video", "new").await;
    job.succeed(&core.db).await.unwrap();
    let before = acquisitions::list(&core, json!({"entry_id":"e"}))
        .await
        .unwrap();
    assert_eq!(before["items"].as_array().unwrap().len(), 5);
    assert_eq!(
        core.db
            .read(|c| Ok(c.query_row("SELECT count(*) FROM blobs", [], |r| r.get::<_, i64>(0))?))
            .await
            .unwrap(),
        4
    );
    for include in [false, true] {
        let backup = crate::storage_ops::backup(
            &core,
            &dir.path().join(if include { "full" } else { "metadata" }),
            include,
        )
        .await
        .unwrap();
        let location = if include { "full" } else { "metadata" };
        let source = dir
            .path()
            .join(location)
            .join(backup["backup_id"].as_str().unwrap());
        let result = crate::storage_ops::restore(
            &core,
            &source,
            &dir.path().join(format!("restored-{include}")),
        )
        .await
        .unwrap();
        let profile: Profile = serde_json::from_slice(
            &std::fs::read(result["profile_path"].as_str().unwrap()).unwrap(),
        )
        .unwrap();
        let restored = Core::open(profile).await.unwrap();
        let after = acquisitions::list(&restored, json!({"entry_id":"e"}))
            .await
            .unwrap();
        assert_eq!(after["items"].as_array().unwrap().len(), 5);
        assert!(
            after["items"]
                .as_array()
                .unwrap()
                .iter()
                .all(|v| v["current_credential_available"] == false)
        );
        assert_eq!(after["summary"]["state"], "mixed");
        assert_eq!(
            restored
                .db
                .read(|c| Ok(c
                    .query_row("SELECT count(*) FROM credentials_state", [], |r| r
                        .get::<_, i64>(0))?))
                .await
                .unwrap(),
            0
        );
        restored.shutdown().await;
    }
    let output = dir.path().join("export");
    crate::archive_export::export(&core, &output, false, None)
        .await
        .unwrap();
    core.shutdown().await;
}

#[tokio::test]
async fn c11_two_production_auth_probes_require_batch_proof_and_preserve_expired_unknown() {
    use base64::Engine;
    for platform in ["bilibili", "douyin"] {
        let (_dir, core) = core_fixture().await;
        let site = platform.to_owned();
        core.vault
            .store("proof-vault", b"SESSION=synthetic-proof")
            .unwrap();
        core.db.write(move|tx|{tx.execute("UPDATE source_accounts SET namespace=?1",[&site])?;tx.execute("UPDATE entries SET namespace=?1",[&site])?;tx.execute("INSERT INTO credentials_state(credential_id,platform,vault_ref,status,created_at,updated_at) VALUES('proof',?1,'proof-vault','valid','t','t')",[site])?;tx.execute("UPDATE subscriptions SET credential_id='proof'",[])?;Ok(())}).await.unwrap();
        let phase = Arc::new(AtomicUsize::new(0));
        let observed = phase.clone();
        core.host.set_proxy_fixture(Arc::new(move |params| {
            let url = params["url"].as_str().unwrap();
            assert!(url.contains(if platform == "bilibili" {
                "/x/web-interface/nav"
            } else {
                "/user/profile/self/"
            }));
            assert_eq!(params["with_credential"], true);
            if observed.load(Ordering::SeqCst) > 0 {
                return Err(trace_contracts::plugin::PluginError {
                    class: if observed.load(Ordering::SeqCst) == 1 {
                        trace_domain::errors::FailureClass::AuthRequired
                    } else {
                        trace_domain::errors::FailureClass::TransientNetwork
                    },
                    message: "synthetic failure".into(),
                    retry_after_ms: None,
                    details: Value::Null,
                });
            }
            let value = if platform == "bilibili" {
                json!({"code":0,"data":{"isLogin":true}})
            } else {
                json!({"status_code":0,"user":{"uid":"71","sec_uid":"MS4proof"}})
            };
            Ok(trace_contracts::plugin::ProxyFetchResult {
                status: 200,
                headers: json!({}),
                body_b64: base64::engine::general_purpose::STANDARD.encode(value.to_string()),
                final_url: url.into(),
            })
        }));
        jobs::enqueue(
            &core.db,
            "proof-job".into(),
            "observe",
            "e",
            json!({"subscription_id":"s","entry_id":"e"}),
            0,
            1,
            0,
        )
        .await
        .unwrap();
        let job = jobs::claim_due(&core.db, "proof", 1)
            .await
            .unwrap()
            .remove(0);
        job.mark_running(&core.db).await.unwrap();
        for (phase_value, expected) in [
            (0, Auth::Authenticated),
            (1, Auth::Expired),
            (2, Auth::Unknown),
        ] {
            phase.store(phase_value, Ordering::SeqCst);
            let context = acquisitions::begin(&core, &job, platform, "e")
                .await
                .unwrap();
            assert_eq!(context.auth_state, expected);
            assert_eq!(context.credential_generation, Some(1));
        }
        assert_eq!(
            acquisitions::summary(
                &rusqlite::Connection::open(core.profile.db_path()).unwrap(),
                "e"
            )
            .unwrap()["state"],
            "mixed"
        );
        core.shutdown().await;
    }
}

#[test]
fn c11_schema4_migration_unknown_history_and_failure_roll_back_without_rewriting_old_migrations() {
    use crate::db::schema::*;
    for fail in [false, true] {
        let dir = tempfile::tempdir().unwrap();
        let mut c = rusqlite::Connection::open(dir.path().join("schema4.db")).unwrap();
        for (v, sql) in migrations().into_iter().take(4) {
            c.execute_batch(sql).unwrap();
            c.execute("INSERT INTO meta VALUES('schema_version',?1) ON CONFLICT(key) DO UPDATE SET value=excluded.value",[v.to_string()]).unwrap();
        }
        c.execute_batch("INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('a','bilibili','1','t');INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES('e','bilibili','video','1','a','t');INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at) VALUES('r','e','d','old','old','t');INSERT INTO media_items(media_id,revision_id,role,position,source_attachment_id) VALUES('m','r','video',0,'m');INSERT INTO blobs(blob_id,size,first_seen_at) VALUES('sha256:old',4,'t');INSERT INTO media_variants VALUES('v','m','old','sha256:old','{}','{}','t');").unwrap();
        if fail {
            c.execute("CREATE TABLE job_consumers(injected_failure INTEGER)", [])
                .unwrap();
        }
        assert_eq!(run_migrations(&mut c).is_err(), fail);
        assert_eq!(
            c.query_row(
                "SELECT value FROM meta WHERE key='schema_version'",
                [],
                |r| r.get::<_, String>(0)
            )
            .unwrap(),
            if fail {
                "4".to_owned()
            } else {
                trace_domain::SCHEMA_VERSION.to_string()
            }
        );
        if fail {
            assert!(
                !c.query_row(
                    "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name='entry_subscriptions')",
                    [],
                    |r| r.get::<_, bool>(0)
                )
                .unwrap()
            );
        } else {
            assert_eq!(
                c.query_row("SELECT auth_state FROM acquisition_contexts", [], |r| {
                    r.get::<_, String>(0)
                })
                .unwrap(),
                "unknown"
            );
            assert!(c.execute("DELETE FROM resource_acquisitions", []).is_err());
        }
        let snapshot = std::fs::read_dir(dir.path())
            .unwrap()
            .filter_map(Result::ok)
            .find(|f| f.file_name().to_string_lossy().contains("pre-schema-5"))
            .unwrap();
        let old = rusqlite::Connection::open(snapshot.path()).unwrap();
        assert_eq!(
            old.query_row(
                "SELECT value FROM meta WHERE key='schema_version'",
                [],
                |r| r.get::<_, String>(0)
            )
            .unwrap(),
            "4"
        );
    }
}

#[tokio::test]
async fn c11_overlapping_production_subscriptions_keep_authenticated_cdn_history_and_old_selection()
{
    use base64::Engine;
    use tokio::io::{AsyncReadExt, AsyncWriteExt};
    for site in ["bilibili", "douyin"] {
        let (dir, core) = core_fixture().await;
        let mut media = Vec::new();
        for (name, size) in [("low", "64x64"), ("high", "128x72")] {
            let path = dir.path().join(format!("{name}.mp4"));
            assert!(
                std::process::Command::new(core.media_backend.ffmpeg.as_ref().unwrap())
                    .args([
                        "-hide_banner",
                        "-loglevel",
                        "error",
                        "-f",
                        "lavfi",
                        "-i",
                        &format!("testsrc=size={size}:rate=10"),
                        "-t",
                        "1",
                        "-c:v",
                        "libopenh264",
                        "-pix_fmt",
                        "yuv420p"
                    ])
                    .arg(&path)
                    .status()
                    .unwrap()
                    .success()
            );
            media.push(std::fs::read(path).unwrap());
        }
        let sizes = [media[0].len(), media[1].len()];
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let base = format!("http://{}", listener.local_addr().unwrap());
        core.http.route_fixture("fixture.douyinvod.com", &base);
        let count = Arc::new(AtomicUsize::new(0));
        let count2 = count.clone();
        let server = tokio::spawn(async move {
            loop {
                let (mut socket, _) = listener.accept().await.unwrap();
                let mut request = Vec::new();
                let mut buf = [0; 4096];
                while !request.windows(4).any(|v| v == b"\r\n\r\n") {
                    let n = socket.read(&mut buf).await.unwrap();
                    if n == 0 {
                        break;
                    }
                    request.extend_from_slice(&buf[..n]);
                    assert!(request.len() < 16384);
                }
                let request = String::from_utf8_lossy(&request);
                assert!(!request.to_lowercase().contains("cookie:"));
                assert!(!request.to_lowercase().contains("authorization:"));
                count2.fetch_add(1, Ordering::SeqCst);
                let data = &media[usize::from(request.contains("high.mp4"))];
                socket.write_all(format!("HTTP/1.1 200 OK\r\nContent-Length: {}\r\nContent-Type: video/mp4\r\nConnection: close\r\n\r\n",data.len()).as_bytes()).await.unwrap();
                socket.write_all(data).await.unwrap();
            }
        });
        let phase = Arc::new(AtomicUsize::new(0));
        let phase2 = phase.clone();
        let play_base = if site == "bilibili" {
            base.clone()
        } else {
            "https://fixture.douyinvod.com".into()
        };
        core.host.set_proxy_fixture(Arc::new(move|p|{
            let url=url::Url::parse(p["url"].as_str().unwrap()).unwrap();let high=phase2.load(Ordering::SeqCst)==1;let endpoint=url.path();let source=format!("{play_base}/{}.mp4",if high{"high"}else{"low"});
            let value=if site=="douyin" {
                if endpoint.ends_with("/user/profile/self/"){assert!(high);json!({"status_code":0,"user":{"uid":"71","sec_uid":"MS4proof"}})}
                else {assert!(endpoint.ends_with("/aweme/detail/"));json!({"status_code":0,"aweme_detail":{"aweme_id":"71","aweme_type":0,"desc":"same source body","video":{"vid":"stable-source","width":if high{128}else{64},"height":if high{72}else{64},"play_addr":{"url_list":[source],"data_size":sizes[usize::from(high)]}}}})}
            } else {match endpoint {
                "/x/web-interface/nav"=>{let mut v:Value=serde_json::from_str(include_str!("../../../tests/fixtures/bilibili/nav.json")).unwrap();v["data"]["isLogin"]=json!(high);v},
                "/x/web-interface/view"|"/x/web-interface/wbi/view"=>json!({"code":0,"data":{"bvid":"BV1fixture","aid":1,"title":"same title","desc":"same source body","owner":{"mid":1},"pubdate":1700000000,"pages":[{"cid":1,"page":1,"part":"main"}]}}),
                "/x/player/wbi/v2"|"/x/player/v2"=>json!({"code":0,"data":{"subtitle":{"subtitles":[]}}}),
                "/x/player/wbi/playurl"=>json!({"code":0,"data":{"quality":if high{80}else{16},"dash":{"video":[{"id":if high{80}else{16},"base_url":source,"width":if high{128}else{64},"height":if high{72}else{64},"frame_rate":"10","codecs":"avc1.42001e","bandwidth":100000}],"audio":[]}}}),
                _=>panic!("unexpected production endpoint {endpoint}")
            }};
            Ok(trace_contracts::plugin::ProxyFetchResult{status:200,headers:json!({}),body_b64:base64::engine::general_purpose::STANDARD.encode(value.to_string()),final_url:url.to_string()})
        }));
        core.vault
            .store("scope-proof", b"SESSION=synthetic-scope-only")
            .unwrap();
        core.db.write(move|tx|{
            tx.execute("UPDATE source_accounts SET namespace=?1",[site])?;tx.execute("UPDATE entries SET namespace=?1,native_entry_key=?2",rusqlite::params![site,if site=="bilibili"{"BV1fixture"}else{"71"}])?;
            tx.execute("UPDATE subscriptions SET content_type='dynamic'",[])?;
            tx.execute("INSERT INTO credentials_state(credential_id,platform,vault_ref,status,created_at,updated_at) VALUES('proof',?1,'scope-proof','valid','t','t')",[site])?;
            tx.execute("INSERT INTO subscriptions(subscription_id,account_id,content_type,range_json,credential_id,quality_json,created_at,updated_at) VALUES('second','a','video','{}','proof','{\"min_height\":72,\"strict\":true}','t','t')",[])?;
            Ok(())
        }).await.unwrap();
        for sub in ["s", "second"] {
            jobs::enqueue(
                &core.db,
                format!("scan:{sub}"),
                "scan",
                sub,
                json!({"subscription_id":sub,"mode":"incremental"}),
                0,
                1,
                0,
            )
            .await
            .unwrap();
        }
        let scans = jobs::claim_due(&core.db, "production", 2).await.unwrap();
        for (sub, high) in [("s", false), ("second", true)] {
            if high {
                core.db
                    .write(|tx| jobs::set_subscription_enabled(tx, "s", false))
                    .await
                    .unwrap();
                phase.store(1, Ordering::SeqCst);
            }
            let scan = scans.iter().find(|j| j.subject_id == sub).unwrap();
            process_scan_page(
                &core,
                scan,
                site,
                sub,
                "incremental",
                vec![trace_contracts::plugin::EnumeratedEntry {
                    native_entry_key: if site == "bilibili" {
                        "BV1fixture"
                    } else {
                        "71"
                    }
                    .into(),
                    native_kind: "video".into(),
                    title: "source".into(),
                    published_at: None,
                    published_precision: "unknown".into(),
                    pinned: false,
                    summary_body: None,
                    stats: json!({}),
                }],
                None,
                &json!({}),
            )
            .await
            .unwrap();
            let observe = jobs::claim_due(&core.db, "production", 1)
                .await
                .unwrap()
                .remove(0);
            assert_eq!(observe.job_type, "observe");
            assert_eq!(observe.payload["subscription_id"], sub);
            observe.mark_running(&core.db).await.unwrap();
            run_observe(&core, &observe).await.unwrap();
            observe.succeed(&core.db).await.unwrap();
            let download = jobs::claim_due(&core.db, "production", 1)
                .await
                .unwrap()
                .remove(0);
            assert_eq!(download.job_type, "media");
            assert_eq!(download.payload["subscription_id"], sub);
            download.mark_running(&core.db).await.unwrap();
            run_media(&core, &download).await.unwrap();
            download.succeed(&core.db).await.unwrap();
        }
        assert_eq!(
            count.load(Ordering::SeqCst),
            2,
            "both acquisition identities must actually receive bytes"
        );
        assert_eq!(
            core.db
                .read(|c| Ok(
                    c.query_row("SELECT count(*) FROM entries", [], |r| r.get::<_, i64>(0))?
                ))
                .await
                .unwrap(),
            1
        );
        let page = upgrades::list(&core, json!({"state":"pending"}))
            .await
            .unwrap();
        assert_eq!(page["total"], 1, "{page}");
        let item = &page["items"][0];
        assert_eq!(item["auth_state"], "authenticated");
        assert!(
            item["resources"]
                .as_array()
                .unwrap()
                .iter()
                .any(|r| r["new"]["spec"]["height"] == 72 && r["old"]["spec"]["height"] == 64)
        );
        for state in ["anonymous", "authenticated"] {
            let timeline = crate::feed::query(
                &core.db,
                serde_json::from_value(json!({"authentication":state})).unwrap(),
            )
            .await
            .unwrap();
            assert_eq!(timeline["counts"]["filtered"], 1);
            assert_eq!(timeline["items"][0]["entry_id"], "e");
        }
        upgrades::decide(
            &core,
            vec![UpgradeDecision {
                candidate_id: item["candidate_id"].as_str().unwrap().into(),
                decision: UpgradeAction::KeepBoth,
                expected: item["expected"].clone(),
            }],
        )
        .await
        .unwrap();
        core.shutdown().await;
        server.abort();
    }
}

#[tokio::test]
async fn c11_login_completion_is_bounded_persistent_deduplicated_and_excludes_live_deleted() {
    let (_dir, core) = core_fixture().await;
    core.db.write(|tx|{
        tx.execute("INSERT INTO credentials_state(credential_id,platform,vault_ref,status,created_at,updated_at) VALUES('proof','douyin','unused','valid','t','t')",[])?;tx.execute("UPDATE subscriptions SET credential_id='proof'",[])?;
        for n in 0..40 {let id=format!("entry-{n:03}");tx.execute("INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at,lifecycle) VALUES(?1,'douyin',?2,?1,'a','t',?3)",rusqlite::params![id,if n==38{"live"}else{"video"},if n==39{"deleted"}else{"active"}])?;tx.execute("INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at) VALUES(?1,?1,'d','title','body','t')",[&id])?;tx.execute("INSERT INTO media_items(media_id,revision_id,role,position,source_attachment_id) VALUES(?1,?1,'video',0,?1)",[&id])?;jobs::consumers::discover(tx,&id,"s")?;}
        upgrades::schedule(tx,"proof",1)?;upgrades::schedule(tx,"proof",1)?;Ok(())
    }).await.unwrap();
    let job = jobs::claim_due(&core.db, "completion", 1)
        .await
        .unwrap()
        .remove(0);
    assert_eq!(job.job_type, "auth_completion");
    job.mark_running(&core.db).await.unwrap();
    upgrades::run(&core, &job).await.unwrap();
    assert_eq!(
        core.db
            .read(|c| Ok(c.query_row(
                "SELECT count(*) FROM jobs WHERE job_type='observe'",
                [],
                |r| r.get::<_, i64>(0)
            )?))
            .await
            .unwrap(),
        16
    );
    for _ in 0..3 {
        core.db
            .write(|tx| {
                tx.execute(
                    "UPDATE jobs SET next_run_at=NULL,priority=0 WHERE job_type='auth_completion'",
                    [],
                )?;
                Ok(())
            })
            .await
            .unwrap();
        let jobs = jobs::claim_due(&core.db, "completion", 1).await.unwrap();
        let Some(next) = jobs.into_iter().next() else {
            break;
        };
        assert_eq!(next.job_type, "auth_completion");
        next.mark_running(&core.db).await.unwrap();
        upgrades::run(&core, &next).await.unwrap();
        next.succeed(&core.db).await.unwrap();
        let complete = core
            .db
            .read(|c| {
                Ok(c.query_row(
                    "SELECT state='succeeded' FROM jobs WHERE job_type='auth_completion'",
                    [],
                    |r| r.get::<_, bool>(0),
                )?)
            })
            .await
            .unwrap();
        if complete {
            break;
        }
    }
    core.db
        .read(|c| {
            assert_eq!(
                c.query_row(
                    "SELECT count(*) FROM jobs WHERE job_type='observe'",
                    [],
                    |r| r.get::<_, i64>(0)
                )?,
                38
            );
            assert_eq!(
                c.query_row(
                    "SELECT count(*) FROM jobs WHERE job_type='auth_completion'",
                    [],
                    |r| r.get::<_, i64>(0)
                )?,
                1
            );
            Ok(())
        })
        .await
        .unwrap();
    core.shutdown().await;
}
