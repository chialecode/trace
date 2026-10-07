//! Additional closeout regressions. A's original probes remain unchanged.
use serde_json::json;
use std::{
    sync::{
        Arc,
        atomic::{AtomicUsize, Ordering},
    },
    time::Duration,
};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio_util::sync::CancellationToken;
use trace_contracts::control::{Request, commands};
use trace_core::{
    core::Core,
    ipc::dispatch,
    jobs,
    objects::ReceiveParams,
    transport::{ConstrainedClient, host_rules},
};
use trace_domain::ids::OperationId;

static ADAPTER_ENV: tokio::sync::Mutex<()> = tokio::sync::Mutex::const_new(());

async fn enabled(core: &Arc<Core>, value: bool) {
    let response = dispatch::dispatch(
        core,
        Request {
            protocol_version: "0.1".into(),
            request_id: uuid::Uuid::new_v4().to_string(),
            command: commands::SUBSCRIPTION_UPDATE.into(),
            args: json!({"subscription_id":"s","enabled":value}),
            idempotency_key: None,
            if_revision: None,
        },
    )
    .await;
    assert!(response.ok, "{response:?}");
}

async fn seed_account(core: &Core) {
    core.db.write(|tx| {
        tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('a','fake','a','t')",[])?;
        Ok(())
    }).await.unwrap();
}

#[tokio::test]
async fn f007_chunked_lengths_and_success_statuses_are_exact() {
    for (status, offset, payload, expected, ok) in [
        (206, 3, "defghij", None, true),
        (206, 3, "de", None, false),
        (206, 3, "defghijk", None, false),
        (206, 0, "abcdefghij", None, false),
        (200, 0, "abc", Some(3), true),
        (200, 0, "ab", Some(3), false),
        (200, 0, "abcd", Some(3), false),
        (200, 3, "abcdefghij", None, false),
        (201, 0, "abc", None, false),
        (204, 0, "", None, false),
        (205, 3, "", None, false),
    ] {
        let dir = tempfile::tempdir().unwrap();
        let core = Core::open(trace_e2e::test_profile(
            dir.path(),
            &trace_e2e::adapter_dir(),
        ))
        .await
        .unwrap();
        let op = OperationId::new("range-case");
        let mut writer = core
            .objects
            .start_receiving(
                &core.db,
                ReceiveParams {
                    job_id: None,
                    subject_kind: "test".into(),
                    subject_id: "x".into(),
                    generation: 1,
                    mime: None,
                    expected_size: None,
                    expected_sha256: None,
                    source_validator: Some("\"stable\"".into()),
                },
                &op,
            )
            .await
            .unwrap();
        if offset > 0 {
            writer.write_chunk(&core.db, &op, b"abc").await.unwrap();
        }
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let url = format!("http://{}/media", listener.local_addr().unwrap());
        let source = tokio::spawn(async move {
            let (mut socket, _) = listener.accept().await.unwrap();
            let mut request = [0; 4096];
            let _ = socket.read(&mut request).await;
            let range = if status == 206 {
                "Content-Range: bytes 3-9/10\r\n"
            } else {
                ""
            };
            let body = if payload.is_empty() {
                "0\r\n\r\n".to_owned()
            } else {
                format!("{:x}\r\n{payload}\r\n0\r\n\r\n", payload.len())
            };
            let response = format!(
                "HTTP/1.1 {status} Test\r\nETag: \"stable\"\r\n{range}Transfer-Encoding: chunked\r\nConnection: close\r\n\r\n{body}"
            );
            let _ = socket.write_all(response.as_bytes()).await;
        });
        let result = ConstrainedClient::build()
            .unwrap()
            .download_media(
                &host_rules(&core.profile),
                &url,
                None,
                offset,
                Some("\"stable\""),
                (&mut writer, &core.db, &op),
                &CancellationToken::new(),
                expected,
            )
            .await;
        source.await.unwrap();
        assert_eq!(
            result.is_ok(),
            ok,
            "status={status}, offset={offset}, payload={payload}: {result:?}"
        );
        if ok {
            let (size, sha) = core
                .objects
                .finish_and_validate(&core.db, &op, writer)
                .await
                .unwrap();
            core.objects
                .install_object(&core.db, &op, &sha)
                .await
                .unwrap();
            let bytes = std::fs::read(core.objects.object_path(&sha).unwrap()).unwrap();
            assert_eq!(size as usize, bytes.len());
            assert_eq!(
                bytes,
                if offset > 0 {
                    b"abcdefghij".to_vec()
                } else {
                    b"abc".to_vec()
                }
            );
        } else {
            let stage: String = core
                .db
                .read(|c| Ok(c.query_row("SELECT stage FROM materializations", [], |r| r.get(0))?))
                .await
                .unwrap();
            assert_eq!(stage, "RECEIVING");
            assert_eq!(
                core.db
                    .read(|c| Ok(
                        c.query_row("SELECT count(*) FROM blobs", [], |r| r.get::<_, i64>(0))?
                    ))
                    .await
                    .unwrap(),
                0
            );
        }
        core.shutdown().await;
    }
}

#[tokio::test(flavor = "multi_thread")]
async fn f008_segment_sizes_hashes_and_legacy_aggregate_gate_completion() {
    let _env = ADAPTER_ENV.lock().await;
    for (sizes, total, hash, ok) in [
        (json!([2, 5]), None, None, true),
        (json!([8, 8]), None, None, false),
        (json!([null, null]), Some(7), None, true),
        (json!([null, null]), Some(8), None, false),
        (json!([2, null]), Some(7), None, true),
        (json!([8, null]), None, None, false),
        (json!([2, 5]), None, Some("bad"), false),
        (json!([2, 5]), None, Some("part"), false),
        // A re-review: valid digests must be accepted, including an old aggregate-only peer.
        (json!([2, 5]), Some(7), Some("good"), true),
        (json!([null, null]), Some(7), Some("legacy-good"), true),
    ] {
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let base = format!("http://{}", listener.local_addr().unwrap());
        unsafe {
            std::env::set_var("FAKE_BASE_URL", &base);
        }
        let source = tokio::spawn(async move {
            loop {
                let (mut socket, _) = listener.accept().await.unwrap();
                let mut request = [0; 4096];
                let n = socket.read(&mut request).await.unwrap();
                let header = String::from_utf8_lossy(&request[..n]);
                let body = if header.contains("/fake/api/stream") {
                    let part_hash = match hash {
                        Some("part") => Some("0".repeat(64)),
                        Some("good") => Some(
                            "fb8e20fc2e4c3f248c60c39bd652f3c1347298bb977b8b4d5903b85055620603"
                                .to_owned(),
                        ),
                        _ => None,
                    };
                    let second_hash = (hash == Some("good")).then(|| {
                        "ff7834266e9e68caf1ca05fd2f11d469f6599abab3a62508cb645fde65d30dc3"
                            .to_owned()
                    });
                    let group_hash = match hash {
                        Some("good" | "legacy-good") => Some(
                            "7d1a54127b222502f5b79b5fb0803061152a44f92b37e23c6527baf665d4da9a"
                                .to_owned(),
                        ),
                        Some("bad") => Some("0".repeat(64)),
                        _ => None,
                    };
                    json!({"segments":[{"u":format!("{base}/one"),"size":sizes[0],"sha256":part_hash}, {"u":format!("{base}/two"),"size":sizes[1],"sha256":second_hash}],"total_size":total,"sha256":group_hash}).to_string()
                } else if header.contains("/one") {
                    "ab".into()
                } else {
                    "cdefg".into()
                };
                let response = format!(
                    "HTTP/1.1 200 OK\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
                    body.len()
                );
                let _ = socket.write_all(response.as_bytes()).await;
            }
        });
        let dir = tempfile::tempdir().unwrap();
        let core = Core::open(trace_e2e::test_profile(
            dir.path(),
            &trace_e2e::adapter_dir(),
        ))
        .await
        .unwrap();
        seed_account(&core).await;
        core.db.write(|tx| {
            tx.execute("INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES('e','fake','clip','v-2','a','t')",[])?;
            tx.execute("INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at,completeness) VALUES('r','e','d','segments','','t','full')",[])?;
            tx.execute("INSERT INTO media_items(media_id,revision_id,role,position,source_attachment_id,group_key,required) VALUES('m','r','video',1,'v-2/p1','p1',1)",[])?; Ok(())
        }).await.unwrap();
        jobs::enqueue(
            &core.db,
            "segments".into(),
            "media",
            "e",
            json!({"entry_id":"e","revision_id":"r","group":"p1"}),
            1,
            1,
            0,
        )
        .await
        .unwrap();
        core.start_scheduler();
        let state = tokio::time::timeout(Duration::from_secs(25), async {
            loop {
                let state: String = core
                    .db
                    .read(|c| {
                        Ok(c.query_row(
                            "SELECT state FROM jobs WHERE job_key='segments'",
                            [],
                            |r| r.get(0),
                        )?)
                    })
                    .await
                    .unwrap();
                if !matches!(state.as_str(), "queued" | "leased" | "running") {
                    break state;
                }
                tokio::time::sleep(Duration::from_millis(30)).await;
            }
        })
        .await
        .unwrap();
        let page = trace_core::feed::query(&core.db, serde_json::from_value(json!({})).unwrap())
            .await
            .unwrap();
        assert_eq!(
            state == "succeeded",
            ok,
            "{state}, total={total:?}, hash={hash:?}"
        );
        assert_eq!(page["items"][0]["save_progress"] == "complete", ok);
        if ok {
            let sizes:Vec<i64>=core.db.read(|c|Ok(c.prepare("SELECT b.size FROM media_items mi JOIN media_variants mv ON mv.media_id=mi.media_id JOIN blobs b ON b.blob_id=mv.blob_id ORDER BY mi.position")?.query_map([],|r|r.get(0))?.collect::<Result<_,_>>()?)).await.unwrap();
            assert_eq!(sizes, vec![2, 5]);
        } else {
            assert_eq!(
                core.db
                    .read(|c| Ok(c
                        .query_row("SELECT count(*) FROM media_variants", [], |r| r
                            .get::<_, i64>(0))?))
                    .await
                    .unwrap(),
                0
            );
        }
        core.shutdown().await;
        source.abort();
    }
}

#[tokio::test(flavor = "multi_thread")]
async fn f015_pause_due_running_resume_idempotency_and_restart() {
    let _env = ADAPTER_ENV.lock().await;
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    unsafe {
        std::env::set_var(
            "FAKE_BASE_URL",
            format!("http://{}", listener.local_addr().unwrap()),
        );
    }
    let requests = Arc::new(AtomicUsize::new(0));
    let entered = Arc::new(tokio::sync::Semaphore::new(0));
    let release = Arc::new(tokio::sync::Semaphore::new(0));
    let (count, signal, gate) = (requests.clone(), entered.clone(), release.clone());
    let source = tokio::spawn(async move {
        loop {
            let (mut socket, _) = listener.accept().await.unwrap();
            let mut request = [0; 4096];
            let _ = socket.read(&mut request).await;
            let first = count.fetch_add(1, Ordering::SeqCst) == 0;
            if first {
                signal.add_permits(1);
                gate.acquire().await.unwrap().forget();
            }
            let body = if first {
                json!({"entries":[{"id":"late","heading":"must not commit"}],"next":null})
            } else {
                json!({"entries":[],"next":null})
            }
            .to_string();
            let response = format!(
                "HTTP/1.1 200 OK\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
                body.len()
            );
            let _ = socket.write_all(response.as_bytes()).await;
        }
    });
    let dir = tempfile::tempdir().unwrap();
    let profile = trace_e2e::test_profile(dir.path(), &trace_e2e::adapter_dir());
    let core = Core::open(profile.clone()).await.unwrap();
    seed_account(&core).await;
    core.db.write(|tx| {tx.execute("INSERT INTO subscriptions(subscription_id,account_id,content_type,range_json,state,created_at,updated_at) VALUES('s','a','video','{}','enabled','t','t')",[])?;Ok(())}).await.unwrap();
    jobs::enqueue(
        &core.db,
        "scan:s:incremental".into(),
        "scan",
        "s",
        json!({"subscription_id":"s","mode":"incremental"}),
        1,
        1,
        60_000,
    )
    .await
    .unwrap();
    core.db
        .write(|tx| {
            tx.execute("UPDATE jobs SET attempts=2", [])?;
            Ok(())
        })
        .await
        .unwrap();
    enabled(&core, false).await;
    core.db
        .write(|tx| {
            tx.execute("UPDATE jobs SET next_run_at='2000-01-01T00:00:00Z'", [])?;
            Ok(())
        })
        .await
        .unwrap();
    assert!(
        jobs::claim_due(&core.db, "test", 10)
            .await
            .unwrap()
            .is_empty()
    );
    core.shutdown().await;
    drop(core);
    let core = Core::open(profile).await.unwrap();
    core.recover().await.unwrap();
    assert!(
        jobs::claim_due(&core.db, "test", 10)
            .await
            .unwrap()
            .is_empty()
    );
    enabled(&core, true).await;
    core.start_scheduler();
    tokio::time::timeout(Duration::from_secs(15), entered.acquire())
        .await
        .unwrap()
        .unwrap()
        .forget();
    let generation: i64 = core
        .db
        .read(|c| {
            Ok(c.query_row(
                "SELECT target_generation FROM jobs WHERE job_key='scan:s:incremental'",
                [],
                |r| r.get(0),
            )?)
        })
        .await
        .unwrap();
    enabled(&core, true).await; // Must not revoke/restart an already active scan.
    assert_eq!(
        core.db
            .read(|c| Ok(c.query_row(
                "SELECT target_generation FROM jobs WHERE job_key='scan:s:incremental'",
                [],
                |r| r.get::<_, i64>(0)
            )?))
            .await
            .unwrap(),
        generation
    );
    enabled(&core, false).await;
    enabled(&core, true).await; // Pause/resume can race a late page from the previous lease.
    enabled(&core, true).await;
    release.add_permits(1);
    tokio::time::timeout(Duration::from_secs(20), async {
        loop {
            let checkpoints: i64 = core
                .db
                .read(|c| {
                    Ok(c.query_row("SELECT count(*) FROM scan_checkpoints", [], |r| r.get(0))?)
                })
                .await
                .unwrap();
            if checkpoints > 0 {
                break;
            }
            tokio::time::sleep(Duration::from_millis(30)).await;
        }
    })
    .await
    .unwrap();
    let facts: (i64, i64, i64) = core
        .db
        .read(|c| {
            Ok((
                c.query_row("SELECT count(*) FROM jobs WHERE job_type='scan' AND json_extract(payload,'$.mode')='incremental'", [], |r| r.get(0))?,
                c.query_row("SELECT attempts FROM jobs WHERE job_key='scan:s:incremental'", [], |r| r.get(0))?,
                c.query_row("SELECT count(*) FROM entries", [], |r| r.get(0))?,
            ))
        })
        .await
        .unwrap();
    assert_eq!(
        facts,
        (1, 2, 0),
        "one scan, preserved failure budget, late page rejected"
    );
    // M1 adds an independent deep cursor and archive maintenance. Keep the
    // original incremental assertions exact and reject any unexplained job.
    let all_jobs: Vec<(String, String)> = core
        .db
        .read(|c| {
            Ok(
                c.prepare("SELECT job_key,job_type FROM jobs ORDER BY job_key")?
                    .query_map([], |r| Ok((r.get(0)?, r.get(1)?)))?
                    .collect::<Result<Vec<_>, _>>()?,
            )
        })
        .await
        .unwrap();
    assert_eq!(
        all_jobs,
        vec![
            ("quality-maintenance".into(), "quality_maintenance".into()),
            ("scan:s:deep".into(), "scan".into()),
            ("scan:s:incremental".into(), "scan".into())
        ]
    );
    assert!(requests.load(Ordering::SeqCst) >= 2);
    core.shutdown().await;
    source.abort();
}
