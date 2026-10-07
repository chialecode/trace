//! Task-lifecycle tests use a controlled protocol peer. Site parsing is covered
//! by core_vertical_tests with both production executables, not by this peer.
use super::*;
use base64::Engine;
use serde_json::json;
use std::sync::atomic::AtomicUsize;
use tokio::io::{AsyncReadExt, AsyncWriteExt};

async fn command(core: &Arc<Core>, name: &str, args: Value) -> Value {
    let result = crate::ipc::dispatch::dispatch(
        core,
        trace_contracts::control::Request {
            protocol_version: trace_domain::CONTROL_PROTOCOL_VERSION.into(),
            request_id: uuid::Uuid::new_v4().to_string(),
            command: name.into(),
            idempotency_key: None,
            if_revision: None,
            args,
        },
    )
    .await;
    assert!(result.ok, "{name}: {:?}", result.error);
    result.result.unwrap_or(Value::Null)
}
async fn wait_for(counter: &AtomicUsize, expected: usize) {
    tokio::time::timeout(Duration::from_secs(10), async {
        while counter.load(Ordering::SeqCst) < expected {
            tokio::time::sleep(Duration::from_millis(5)).await;
        }
    })
    .await
    .expect("both task workers must reach the actual source operation");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 8)]
async fn c05_all_job_types_plugin_isolation_and_shutdown_sealing() {
    let root = tempfile::tempdir().unwrap();
    let probe = Core::open(Profile {
        data_root: root.path().join("probe-data"),
        archive_root: root.path().join("probe-archive"),
        vault_dir: Some(root.path().join("probe-vault")),
        ..Default::default()
    })
    .await
    .unwrap();
    let bytes =
        crate::recording::tests::flv_sample(&probe, &root.path().join("media.flv"), "64x64");
    let mp4 = root.path().join("media.mp4");
    assert!(
        std::process::Command::new(probe.media_backend.ffmpeg.as_ref().unwrap())
            .args(["-hide_banner", "-loglevel", "error", "-i"])
            .arg(root.path().join("media.flv"))
            .args(["-c", "copy"])
            .arg(&mp4)
            .status()
            .unwrap()
            .success()
    );
    let download = std::fs::read(mp4).unwrap();
    probe.shutdown().await;
    drop(probe);
    for kind in [
        "scan",
        "observe",
        "media",
        "record",
        "account_profile",
        "live_auxiliary",
        "credential_probe",
    ] {
        for action in ["pause_resume", "plugin_disable", "cancel", "shutdown"] {
            eprintln!("lifecycle scenario {kind}/{action}");
            let path = root.path().join(format!("{kind}-{action}"));
            let specs = ["bilibili", "douyin"]
                .into_iter()
                .map(|site| crate::pluginhost::AdapterSpec {
                    platform: site.into(),
                    plugin_id: site.into(),
                    binary_path: std::path::PathBuf::from(env!("CARGO_MANIFEST_DIR"))
                        .join("../../target/debug/m1_lifecycle_adapter.exe"),
                })
                .collect();
            let core = Core::open_with_adapters(
                Profile {
                    archive_id: format!("lifecycle-{kind}-{action}"),
                    data_root: path.join("data"),
                    archive_root: path.join("archive"),
                    vault_dir: Some(path.join("vault")),
                    test_allowed_hosts: vec!["127.0.0.1".into()],
                    ..Default::default()
                },
                specs,
            )
            .await
            .unwrap();
            let reached = Arc::new(AtomicUsize::new(0));
            let release = Arc::new(AtomicBool::new(false));
            let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
            let base = format!("http://{}", listener.local_addr().unwrap());
            let (seen, unlock, media) = (
                reached.clone(),
                release.clone(),
                if kind == "record" {
                    bytes.clone()
                } else {
                    download.clone()
                },
            );
            let server = tokio::spawn(async move {
                loop {
                    let Ok((mut socket, _)) = listener.accept().await else {
                        break;
                    };
                    let (seen, unlock, media) = (seen.clone(), unlock.clone(), media.clone());
                    tokio::spawn(async move {
                        let mut buf = [0; 4096];
                        let mut request = Vec::new();
                        while !request.windows(4).any(|w| w == b"\r\n\r\n") {
                            let n = socket.read(&mut buf).await.unwrap();
                            if n == 0 {
                                return;
                            }
                            request.extend_from_slice(&buf[..n]);
                        }
                        let recording = String::from_utf8_lossy(&request).contains("/live");
                        let prefix = if recording {
                            media.len()
                        } else {
                            media.len() / 2
                        };
                        let _=socket.write_all(format!("HTTP/1.1 200 OK\r\nContent-Type: video/x-flv\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",if recording {media.len()+1} else {media.len()}).as_bytes()).await;
                        let _ = socket.write_all(&media[..prefix]).await;
                        seen.fetch_add(1, Ordering::SeqCst);
                        while !unlock.load(Ordering::SeqCst) {
                            tokio::time::sleep(Duration::from_millis(5)).await;
                        }
                        if !recording {
                            let _ = socket.write_all(&media[prefix..]).await;
                        }
                    });
                }
            });
            let (seen, unlock) = (reached.clone(), release.clone());
            let size = download.len();
            core.host.set_proxy_fixture(Arc::new(move|p| {
                let url=url::Url::parse(p["url"].as_str().unwrap()).unwrap();
                let params:Value=serde_json::from_str(&url.query_pairs().find(|(k,_)|k=="params").unwrap().1).unwrap();
                let method=url.path().trim_start_matches('/');
                if !matches!(kind,"media"|"record") {
                    seen.fetch_add(1,Ordering::SeqCst);let start=Instant::now();
                    while !unlock.load(Ordering::SeqCst) && start.elapsed()<Duration::from_secs(10) {std::thread::sleep(Duration::from_millis(5));}
                }
                let value=match method {
                    "enumerate"=>json!({"items":[{"native_entry_key":"new","native_kind":"article","title":"late page","published_precision":"unknown","pinned":false,"stats":{}}],"next_cursor":null,"has_more":false,"coverage":{}}),
                    "observe"=>json!({"native_entry_key":"seed","native_kind":"article","title":"late detail","normalized_body":"new body","published_precision":"unknown","access_state":"available","source_version_evidence":{},"resources":[],"completeness":"full","stats":{}}),
                    "resolve_media"=>json!({"candidates":[{"resource":params["resources"][0],"representation":"test","kind":"progressive","segments":[{"primary":format!("{base}/download")}],"expected_size":size,"claimed_spec":{},"public_headers":{}}],"notes":{}}),
                    "probe_live"=>json!({"room_key":"7","source_session":"1700000000","state":"live","title":"live","metadata":{},"streams":[{"protocol":"flv","url":format!("{base}/live"),"quality":"origin","audio_only":false,"public_headers":{}}]}),
                    "resolve_account"=>json!({"native_account_key":params["input"],"display_name":"new profile","profile_url":null,"evidence":{}}),
                    "probe_auth"=>json!({"state":"valid","capability":{}}),
                    "sample_live_auxiliary"=>json!({"messages":[{"source_id":"1","text":"late chat","author":null,"source_time":null,"kind":"chat"}],"coverage":"sampled_history","source_count":1,"omitted":0}),
                    _=>panic!("unexpected lifecycle request {method}"),
                };
                Ok(trace_contracts::plugin::ProxyFetchResult{status:200,headers:json!({}),body_b64:base64::engine::general_purpose::STANDARD.encode(value.to_string()),final_url:url.to_string()})
            }));
            if kind == "credential_probe" {
                for site in ["bilibili", "douyin"] {
                    core.vault
                        .store(&format!("credential-{site}"), b"SESSION=synthetic")
                        .unwrap();
                }
            }
            core.db.write(move|tx| {
                for site in ["bilibili","douyin"] {
                    let sub=format!("sub-{site}");let entry=format!("entry-{site}");let revision=format!("revision-{site}");let live=format!("live-{site}");
                    tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES(?1,?1,?2,'t')",rusqlite::params![site,if site=="bilibili"{"1"}else{"MS4fixture"}])?;
                    tx.execute("INSERT INTO subscriptions(subscription_id,account_id,content_type,range_json,capture_chat,created_at,updated_at) VALUES(?1,?2,?3,'{}',1,'t','t')",rusqlite::params![sub,site,if matches!(kind,"record"|"live_auxiliary"){"live"}else{"article"}])?;
                    tx.execute("INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES(?1,?2,?3,'seed',?2,'t')",rusqlite::params![entry,site,if kind=="media"{"video"}else{"article"}])?;
                    tx.execute("INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,completeness,observed_at) VALUES(?1,?2,'original','original','old body','full','t')",rusqlite::params![revision,entry])?;
                    tx.execute("INSERT INTO media_items(media_id,revision_id,role,position,source_attachment_id,required) VALUES(?1,?2,'video',0,'source',1)",rusqlite::params![format!("media-{site}"),revision])?;
                    tx.execute("INSERT INTO live_sessions(session_id,subscription_id,room_key,source_session,entry_id,state,started_at) VALUES(?1,?2,'7','1700000000',?3,'waiting','t')",rusqlite::params![live,sub,entry])?;
                    if kind=="credential_probe" {tx.execute("INSERT INTO credentials_state(credential_id,platform,vault_ref,status,created_at,updated_at) VALUES(?1,?2,?1,'unknown','t','t')",rusqlite::params![format!("credential-{site}"),site])?;}
                    let subject=match kind {"credential_probe"=>format!("credential-{site}"),"scan"=>sub.clone(),"account_profile"=>site.into(),"record"|"live_auxiliary"=>live,_=>entry.clone()};
                    tx.execute("INSERT INTO jobs(job_id,job_key,job_type,subject_id,payload,priority,state,attempts,created_at,updated_at) VALUES(?1,?1,?2,?3,?4,0,'queued',2,'t','t')",rusqlite::params![format!("job-{site}"),kind,subject,json!({"subscription_id":sub,"entry_id":entry,"revision_id":revision,"group":"source","mode":"incremental","credential_id":format!("credential-{site}"),"credential_generation":1,"platform":site}).to_string()])?;
                }Ok(())
            }).await.unwrap();
            let old = jobs::claim_due(&core.db, "lifecycle", 2).await.unwrap();
            assert_eq!(old.len(), 2);
            let workers = old
                .iter()
                .map(|job| {
                    let job = job.clone();
                    let c = core.clone();
                    core.tasks.spawn(async move {
                        execute(c, job).await;
                    })
                })
                .collect::<Vec<_>>();
            wait_for(&reached, 2).await;
            let active = command(&core, "plugin.list", json!({})).await;
            for site in ["bilibili", "douyin"] {
                let plugin = active["items"]
                    .as_array()
                    .unwrap()
                    .iter()
                    .find(|p| p["platform"] == site)
                    .unwrap();
                assert_eq!(plugin["active_tasks"], 1, "{kind}/{action}/{site}");
            }
            if kind == "record" {
                tokio::time::timeout(Duration::from_secs(5), async {
                    loop {
                        let n: i64 = core
                            .db
                            .read(|c| {
                                Ok(c.query_row(
                                    "SELECT count(*) FROM live_fragments WHERE state='receiving'",
                                    [],
                                    |r| r.get(0),
                                )?)
                            })
                            .await
                            .unwrap();
                        if n >= 4 {
                            break;
                        }
                        tokio::time::sleep(Duration::from_millis(10)).await;
                    }
                })
                .await
                .expect("recording bytes must be durable before lifecycle action");
            }
            match action {
                "pause_resume" => {
                    command(&core, "daemon.pause", json!({"paused":true})).await;
                    command(&core, "daemon.pause", json!({"paused":false})).await;
                }
                "plugin_disable" => {
                    command(
                        &core,
                        "plugin.set_state",
                        json!({"plugin_id":"bilibili","state":"disabled"}),
                    )
                    .await;
                    let current = command(&core, "plugin.list", json!({})).await;
                    let stopped = current["items"]
                        .as_array()
                        .unwrap()
                        .iter()
                        .find(|p| p["platform"] == "bilibili")
                        .unwrap();
                    assert_eq!(
                        stopped["state"],
                        if stopped["active_tasks"].as_u64().unwrap() > 0 {
                            "stopping"
                        } else {
                            "disabled"
                        }
                    );
                }
                "cancel" => {
                    for job in &old {
                        command(&core, "job.cancel", json!({"job_id":job.job_id})).await;
                    }
                }
                "shutdown" => {
                    let token = core.cancel.clone();
                    let unlock = release.clone();
                    let unlocker = tokio::spawn(async move {
                        token.cancelled().await;
                        unlock.store(true, Ordering::SeqCst);
                    });
                    core.shutdown().await;
                    unlocker.await.unwrap();
                }
                _ => unreachable!(),
            }
            release.store(true, Ordering::SeqCst);
            for worker in workers {
                tokio::time::timeout(Duration::from_secs(10), worker)
                    .await
                    .unwrap()
                    .unwrap();
            }
            let isolated = action == "plugin_disable";
            assert!(
                core.active_jobs.lock().unwrap().is_empty(),
                "completed workers release lifecycle counts"
            );
            if isolated {
                let complete = command(&core, "plugin.list", json!({})).await;
                let stopped = complete["items"]
                    .as_array()
                    .unwrap()
                    .iter()
                    .find(|p| p["platform"] == "bilibili")
                    .unwrap();
                assert_eq!(stopped["state"], "disabled");
                assert_eq!(stopped["active_tasks"], 0);
            }
            for job in &old {
                if !isolated || job.job_id == "job-bilibili" {
                    assert!(job.heartbeat(&core.db).await.is_err(), "{kind}/{action}");
                }
            }
            core.db.read(move|c| {
                let attempts:i64=c.query_row("SELECT attempts FROM jobs WHERE job_id='job-bilibili'",[],|r|r.get(0))?;assert_eq!(attempts,2,"administrative action consumes no failure budget: {kind}/{action}");
                let old_effect:i64=match kind {
                    "scan"=>c.query_row("SELECT count(*) FROM entries WHERE namespace='bilibili' AND native_entry_key='new'",[],|r|r.get(0))?,
                    "observe"=>c.query_row("SELECT count(*) FROM entry_revisions WHERE entry_id='entry-bilibili' AND title='late detail'",[],|r|r.get(0))?,
                    "media"=>c.query_row("SELECT count(*) FROM media_variants WHERE media_id='media-bilibili'",[],|r|r.get(0))?,
                    "account_profile"=>c.query_row("SELECT count(*) FROM account_observations WHERE account_id='bilibili'",[],|r|r.get(0))?,
                    "live_auxiliary"=>c.query_row("SELECT count(*) FROM live_messages WHERE session_id='live-bilibili'",[],|r|r.get(0))?,
                    "credential_probe"=>c.query_row("SELECT count(*) FROM credentials_state WHERE platform='bilibili' AND status='valid'",[],|r|r.get(0))?,
                    "record"=>c.query_row("SELECT count(*) FROM live_fragments WHERE state='receiving'",[],|r|r.get(0))?,_=>unreachable!(),
                };assert_eq!(old_effect,0,"old work cannot commit late state: {kind}/{action}");
                if isolated && kind!="record" {
                    let state:String=c.query_row("SELECT state FROM jobs WHERE job_id='job-douyin'",[],|r|r.get(0))?;
                    assert!(matches!(state.as_str(),"succeeded"|"queued"),"other platform must progress: {kind} {state}");
                }
                Ok(())
            }).await.unwrap();
            if kind == "record" {
                let n:i64=core.db.read(|c|Ok(c.query_row("SELECT count(*) FROM live_fragments WHERE state IN ('sealed','source')",[],|r|r.get(0))?)).await.unwrap();
                assert!(n >= 2, "closed captures survive {action}");
                assert_eq!(
                    core.objects.verify_replicas(&core.db, 100).await.unwrap(),
                    0
                );
            }
            core.shutdown().await;
            server.abort();
        }
    }
}

#[tokio::test(flavor = "multi_thread")]
async fn c05_local_maintenance_cancellation_and_plugin_independence() {
    for kind in ["backup", "export", "quality_maintenance"] {
        for action in ["pause_resume", "cancel", "shutdown", "plugin_disable"] {
            let dir = tempfile::tempdir().unwrap();
            let core = Core::open(Profile {
                archive_id: "local-lifecycle".into(),
                data_root: dir.path().join("data"),
                archive_root: dir.path().join("archive"),
                vault_dir: Some(dir.path().join("vault")),
                ..Default::default()
            })
            .await
            .unwrap();
            let target = dir.path().join("output");
            let payload = match kind {
                "backup" => json!({"target_root":target,"include_media":false}),
                "export" => json!({"target_dir":target,"include_media":false}),
                _ => json!({}),
            };
            jobs::enqueue(
                &core.db,
                "local-work".into(),
                kind,
                "archive",
                payload,
                1,
                1,
                0,
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
            let job = jobs::claim_due(&core.db, core.instance_id.as_str(), 1)
                .await
                .unwrap()
                .remove(0);
            // Real maintenance operations wait for the same archive gate used
            // by exports/backup/recycling. Revocation occurs while execute runs.
            let gate = core.archive_access.write().await;
            let worker = core.tasks.spawn(execute(core.clone(), job.clone()));
            tokio::time::timeout(Duration::from_secs(5), async {
                loop {
                    let id = job.job_id.clone();
                    let running = core
                        .db
                        .read(move |c| {
                            Ok(c.query_row(
                                "SELECT state='running' FROM jobs WHERE job_id=?1",
                                [id],
                                |r| r.get::<_, bool>(0),
                            )?)
                        })
                        .await
                        .unwrap();
                    if running {
                        break;
                    }
                    tokio::time::sleep(Duration::from_millis(10)).await;
                }
            })
            .await
            .unwrap();
            match action {
                "pause_resume" => {
                    command(&core, "daemon.pause", json!({"paused":true})).await;
                    command(&core, "daemon.pause", json!({"paused":false})).await;
                }
                "cancel" => {
                    command(&core, "job.cancel", json!({"job_id":job.job_id})).await;
                }
                "shutdown" => core.shutdown().await,
                "plugin_disable" => {
                    command(
                        &core,
                        "plugin.set_state",
                        json!({"plugin_id":"bilibili","state":"disabled"}),
                    )
                    .await;
                }
                _ => unreachable!(),
            }
            if action != "plugin_disable" {
                tokio::time::timeout(Duration::from_secs(3), worker)
                    .await
                    .unwrap()
                    .unwrap();
                assert!(job.heartbeat(&core.db).await.is_err());
                drop(gate);
                assert!(!target.join("export.json").exists());
                if target.exists() {
                    for d in std::fs::read_dir(&target).unwrap() {
                        assert!(!d.unwrap().path().join("manifest.json").exists());
                    }
                }
            } else {
                drop(gate);
                tokio::time::timeout(Duration::from_secs(10), worker)
                    .await
                    .unwrap()
                    .unwrap();
                if kind == "export" {
                    assert!(target.join("export.json").exists());
                }
                if kind == "backup" {
                    assert_eq!(
                        std::fs::read_dir(&target)
                            .unwrap()
                            .filter(|d| d.as_ref().unwrap().path().join("manifest.json").exists())
                            .count(),
                        1
                    );
                }
            }
            core.db
                .read(|c| {
                    assert_eq!(
                        c.query_row(
                            "SELECT attempts FROM jobs WHERE job_key='local-work'",
                            [],
                            |r| r.get::<_, i64>(0)
                        )?,
                        2
                    );
                    Ok(())
                })
                .await
                .unwrap();
            core.shutdown().await;
        }
    }
}
