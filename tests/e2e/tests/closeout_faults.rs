//! Task-level restart/cancel windows against the real resolver and streaming executor.
use std::{
    sync::atomic::Ordering,
    time::{Duration, Instant},
};
use trace_core::{core::Core, jobs, objects::ReceiveParams};
use trace_domain::ids::OperationId;
use trace_e2e::{FakeSource, test_profile};

async fn state(core: &Core) -> String {
    core.db
        .read(|c| {
            Ok(
                c.query_row("SELECT state FROM jobs WHERE job_key='window'", [], |r| {
                    r.get(0)
                })?,
            )
        })
        .await
        .unwrap()
}
async fn wait_state(core: &Core, expected: &str) {
    let deadline = Instant::now() + Duration::from_secs(30);
    loop {
        let current = state(core).await;
        if current == expected {
            return;
        }
        assert!(
            Instant::now() < deadline,
            "expected {expected}, got {current}"
        );
        tokio::time::sleep(Duration::from_millis(50)).await;
    }
}

#[tokio::test(flavor = "multi_thread")]
async fn f005_f006_f007_task_windows_restart_pause_shutdown_and_cancel() {
    let source = FakeSource::start_with_media_delay(30);
    // One test in this executable; set before any adapter process is spawned.
    unsafe {
        std::env::set_var("FAKE_BASE_URL", &source.base_url);
    }
    for window in [
        "prepared",
        "object_ready",
        "between_resources",
        "pause",
        "shutdown",
        "cancel",
    ] {
        let root = tempfile::tempdir().unwrap();
        let profile = test_profile(root.path(), &trace_e2e::adapter_dir());
        let mut core = Core::open(profile.clone()).await.unwrap();
        core.db.write(|tx| {
            tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('a','fake','up-1','t')",[])?;
            tx.execute("INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES('e','fake','video','v-1','a','t')",[])?;
            tx.execute("INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,completeness,observed_at) VALUES('r','e','x','window','','full','t')",[])?;
            tx.execute("INSERT INTO media_items(media_id,revision_id,role,position,source_attachment_id,group_key,required) VALUES('m','r','video',1,'v-1/p1','p1',1)",[])?;
            Ok(())
        }).await.unwrap();
        jobs::enqueue(
            &core.db,
            "window".into(),
            "media",
            "e",
            serde_json::json!({"entry_id":"e","revision_id":"r","group":"p1"}),
            1,
            1,
            0,
        )
        .await
        .unwrap();
        if matches!(window, "prepared" | "object_ready" | "between_resources") {
            let job = jobs::claim_due(&core.db, core.instance_id.as_str(), 1)
                .await
                .unwrap()
                .remove(0);
            job.mark_running(&core.db).await.unwrap();
            // Must match the resolver representation; detect drift with the assertions below.
            let op = OperationId::new(format!(
                "mz-m-1-{}",
                trace_core::config::short_hash("muxed-v-1-p1")
            ));
            let bytes = trace_e2e::media_bytes("v-1-p1.bin");
            let mut writer = core
                .objects
                .start_receiving(
                    &core.db,
                    ReceiveParams {
                        job_id: Some(job.job_id.clone()),
                        subject_kind: "media_variant".into(),
                        subject_id: "m".into(),
                        generation: 1,
                        mime: Some("video/mp4".into()),
                        expected_size: Some(bytes.len() as i64),
                        expected_sha256: None,
                        source_validator: None,
                    },
                    &op,
                )
                .await
                .unwrap();
            writer.write_chunk(&core.db, &op, &bytes).await.unwrap();
            let (_, sha) = core
                .objects
                .finish_and_validate(&core.db, &op, writer)
                .await
                .unwrap();
            if window == "object_ready" || window == "between_resources" {
                core.objects
                    .install_object(&core.db, &op, &sha)
                    .await
                    .unwrap();
            }
            if window == "between_resources" {
                core.objects
                    .commit(
                        &core.db,
                        &op,
                        &trace_core::objects::CommitInfo {
                            job: Some(job.clone()),
                            media_id: "m".into(),
                            source_representation: "muxed-v-1-p1".into(),
                            claimed_spec: serde_json::json!({}),
                        },
                        None,
                    )
                    .await
                    .unwrap();
                core.db.write(|tx| { tx.execute("INSERT INTO media_items(media_id,revision_id,role,position,source_attachment_id,group_key,required) VALUES('audio','r','audio',1,'v-1/p1/audio','p1',1)",[])?; Ok(()) }).await.unwrap();
                assert_eq!(
                    state(&core).await,
                    "running",
                    "one resource cannot finish the whole job"
                );
            }
            core.shutdown().await;
            drop(core);
            core = Core::open(profile.clone()).await.unwrap();
            core.recover().await.unwrap();
        }
        core.start_scheduler();
        if matches!(window, "pause" | "shutdown" | "cancel") {
            let deadline = Instant::now() + Duration::from_secs(15);
            loop {
                let receiving: bool = core
                    .db
                    .read(|c| {
                        Ok(c.query_row(
                            "SELECT EXISTS(SELECT 1 FROM materializations WHERE stage='RECEIVING')",
                            [],
                            |r| r.get(0),
                        )?)
                    })
                    .await
                    .unwrap();
                if receiving {
                    tokio::time::sleep(Duration::from_millis(250)).await;
                    break;
                }
                assert!(Instant::now() < deadline, "no active download for {window}");
                tokio::time::sleep(Duration::from_millis(40)).await;
            }
            if window == "cancel" {
                core.db.write(|tx| { tx.execute("UPDATE jobs SET state='cancelled',target_generation=target_generation+1,lease_owner=NULL WHERE job_key='window'",[])?; Ok(()) }).await.unwrap();
                tokio::time::sleep(Duration::from_millis(700)).await;
                assert_eq!(state(&core).await, "cancelled");
                let variants: i64 = core
                    .db
                    .read(|c| {
                        Ok(c.query_row("SELECT count(*) FROM media_variants", [], |r| r.get(0))?)
                    })
                    .await
                    .unwrap();
                assert_eq!(variants, 0, "late download must not commit");
            } else if window == "pause" {
                core.paused.store(true, Ordering::SeqCst);
                wait_state(&core, "queued").await;
                tokio::time::sleep(Duration::from_millis(600)).await;
                assert_eq!(state(&core).await, "queued");
                core.paused.store(false, Ordering::SeqCst);
            } else {
                core.shutdown().await;
                wait_state(&core, "queued").await;
                drop(core);
                core = Core::open(profile.clone()).await.unwrap();
                core.recover().await.unwrap();
                core.start_scheduler();
            }
        }
        if window != "cancel" {
            wait_state(&core, "succeeded").await;
            let (variants, operations): (i64, i64) = core
                .db
                .read(|c| {
                    Ok((
                        c.query_row("SELECT count(*) FROM media_variants", [], |r| r.get(0))?,
                        c.query_row("SELECT count(*) FROM materializations", [], |r| r.get(0))?,
                    ))
                })
                .await
                .unwrap();
            assert_eq!(
                variants,
                if window == "between_resources" { 2 } else { 1 },
                "{window}"
            );
            assert_eq!(
                operations,
                if window == "between_resources" { 2 } else { 1 },
                "window must reenter same operation: {window}"
            );
            let sha: String = core
                .db
                .read(|c| {
                    Ok(c.query_row(
                        "SELECT substr(blob_id,8) FROM media_variants WHERE media_id='m'",
                        [],
                        |r| r.get(0),
                    )?)
                })
                .await
                .unwrap();
            assert_eq!(
                core.objects
                    .read_range(&sha, 0, trace_e2e::media_bytes("v-1-p1.bin").len() as u64)
                    .unwrap(),
                trace_e2e::media_bytes("v-1-p1.bin")
            );
        }
        core.shutdown().await;
    }
}
