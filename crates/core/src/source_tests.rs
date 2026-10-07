use super::*;
use base64::Engine;
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use trace_contracts::{content::Document, plugin::*};

async fn fixture() -> (tempfile::TempDir, Arc<Core>) {
    let dir = tempfile::tempdir().unwrap();
    let core = Core::open(crate::config::Profile {
        data_root: dir.path().join("data"),
        archive_root: dir.path().join("archive"),
        vault_dir: Some(dir.path().join("vault")),
        adapter_dir: Some(
            std::path::PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../target/debug"),
        ),
        test_allowed_hosts: vec![
            "127.0.0.1".into(),
            "fixture.bilivideo.com".into(),
            "fixture.douyinvod.com".into(),
        ],
        ..Default::default()
    })
    .await
    .unwrap();
    (dir, core)
}
fn source() -> ResolvedSource {
    let resource = ResourceIdentityDto {
        role: "video".into(),
        source_attachment_id: "video".into(),
        position: 0,
        title: None,
        group_key: Some("group".into()),
        required: true,
    };
    let candidate = MediaCandidate {
        resource: resource.clone(),
        representation: "1080".into(),
        kind: "muxed".into(),
        segments: vec![UrlSet {
            primary: "https://fixture.douyinvod.com/file.mp4?token=ephemeral".into(),
            backups: vec![],
            expected_size: Some(100),
            expected_sha256: None,
        }],
        public_headers: json!({}),
        expected_size: Some(100),
        expected_sha256: None,
        claimed_spec: json!({"width":1920,"height":1080,"codec":"h264"}),
    };
    ResolvedSource {
        kind: "video".into(),
        canonical_url: "https://www.douyin.com/video/7000000000000000001".into(),
        native_account_key: "MS4synthetic".into(),
        display_name: Some("合成来源".into()),
        source_duration_seconds: Some(1.0),
        detail: Some(ObservedDetail {
            native_entry_key: "7000000000000000001".into(),
            native_kind: "video".into(),
            title: "明确选择".into(),
            normalized_body: "正文".into(),
            published_at: None,
            published_precision: "unknown".into(),
            access_state: "available".into(),
            source_version_evidence: json!({}),
            resources: vec![resource],
            completeness: "full".into(),
            stats: json!({}),
            document: Document::default(),
            metadata: json!({}),
        }),
        media: ResolveMediaResult {
            candidates: vec![candidate],
            notes: json!({}),
        },
        live: None,
    }
}
fn selected(preview: &Value) -> Vec<String> {
    preview["offers"]
        .as_array()
        .unwrap()
        .iter()
        .filter(|o| o["required"] == true)
        .map(|o| o["id"].as_str().unwrap().into())
        .collect()
}
async fn save(core: &Core) -> Value {
    save_preview(core, "douyin", source(), None, None)
        .await
        .unwrap()
}

#[tokio::test]
async fn c14_direct_live_offline_seals_but_denial_and_unknown_do_not() {
    for site in ["bilibili", "douyin"] {
        let (_dir, core) = fixture().await;
        let mode = Arc::new(AtomicUsize::new(0));
        let source_mode = mode.clone();
        core.host.set_proxy_fixture(Arc::new(move |request| {
            let raw = request["url"].as_str().unwrap();
            let url = url::Url::parse(raw).unwrap();
            let mode = source_mode.load(Ordering::SeqCst);
            if mode == 2 {
                return Err(PluginError {
                    class: trace_domain::errors::FailureClass::PermissionDenied,
                    message: "synthetic source denial".into(),
                    retry_after_ms: None,
                    details: Value::Null,
                });
            }
            let body = match url.path() {
                "/x/web-interface/nav" => serde_json::from_str(include_str!(
                    "../../../tests/fixtures/bilibili/nav.json"
                ))
                .unwrap(),
                "/xlive/web-room/v1/index/getInfoByRoom" => json!({"code":0,"data":{"room_info":{"room_id":700,"uid":12,"live_status":if mode==1{0}else if mode==3{99}else{1},"live_time":123,"title":"合成直播"},"anchor_info":{"base_info":{"uname":"合成主播"}}}}),
                "/xlive/web-room/v2/index/getRoomPlayInfo" => json!({"code":0,"data":{"playurl_info":{"playurl":{"stream":[{"format":[{"format_name":"flv","codec":[{"codec_name":"avc","current_qn":10000,"base_url":"/stream.flv","url_info":[{"host":"https://fixture.bilivideo.com","extra":"?token=ephemeral"}]}]}]}]}}}}),
                "/webcast/room/web/enter/" if mode==1 => json!({"status_code":0,"data":{"data":[{"status":4}]}}),
                "/webcast/room/web/enter/" => json!({"status_code":0,"data":{"data":[{"id_str":"90000000000000001","status":if mode==3{0}else{2},"title":"合成直播","owner":{"sec_uid":"MS4LiveOwner","nickname":"合成主播"},"stream_url":{"flv_pull_url":{"FULL_HD1":"http://fixture.douyinliving.com/live.flv?token=ephemeral"}}}]}}),
                _ => panic!("direct live resolution must not scan or download: {raw}"),
            };
            Ok(ProxyFetchResult {
                status: 200,
                headers: json!({}),
                body_b64: base64::engine::general_purpose::STANDARD
                    .encode(serde_json::to_vec(&body).unwrap()),
                final_url: raw.into(),
            })
        }));
        let url = format!("https://live.{site}.com/77");
        let preview = preview(
            &core,
            SourcePreviewArgs {
                url: url.clone(),
                credential_id: None,
            },
        )
        .await
        .unwrap();
        confirm(
            &core,
            SourceConfirmArgs {
                preview_id: preview["preview_id"].as_str().unwrap().into(),
                selected: selected(&preview),
            },
        )
        .await
        .unwrap();
        let source_job = jobs::claim_due(&core.db, "source-review", 1)
            .await
            .unwrap()
            .remove(0);
        source_job.mark_running(&core.db).await.unwrap();
        run(&core, &source_job).await.unwrap();
        source_job.succeed(&core.db).await.unwrap();
        let record = jobs::claim_due(&core.db, "source-review", 1)
            .await
            .unwrap()
            .remove(0);
        assert_eq!(record.job_type, "record");
        record.mark_running(&core.db).await.unwrap();
        for denied_or_unknown in [2, 3] {
            mode.store(denied_or_unknown, Ordering::SeqCst);
            assert!(crate::recording::run(&core, &record).await.is_err());
            core.db
                .read(|c| {
                    assert_eq!(
                        c.query_row("SELECT state FROM live_sessions", [], |r| r
                            .get::<_, String>(0))?,
                        "waiting"
                    );
                    Ok(())
                })
                .await
                .unwrap();
        }
        mode.store(1, Ordering::SeqCst);
        let ended = crate::recording::run(&core, &record).await;
        let state = core
            .db
            .read(|c| {
                Ok(c.query_row("SELECT state FROM live_sessions", [], |r| {
                    r.get::<_, String>(0)
                })?)
            })
            .await
            .unwrap();
        let profile = core.profile.clone();
        core.shutdown().await;
        drop(core);
        assert!(
            ended.is_ok(),
            "{site}: explicit offline must seal the existing session: {ended:?}, state={state}"
        );
        assert_eq!(state, "sealed");
        let core = Core::open(profile).await.unwrap();
        core.db.read(|c| {
            assert_eq!(c.query_row("SELECT state FROM live_sessions",[],|r|r.get::<_,String>(0))?,"sealed");
            assert_eq!(c.query_row("SELECT count(*) FROM jobs WHERE job_type IN ('scan','auth_completion','account_profile')",[],|r|r.get::<_,i64>(0))?,0);
            Ok(())
        }).await.unwrap();
        core.shutdown().await;
    }
}

async fn drain_source_jobs(core: &Core) {
    for _ in 0..20 {
        let batch = jobs::claim_due(&core.db, "source-test", 1).await.unwrap();
        if batch.is_empty() {
            return;
        }
        for job in batch {
            job.mark_running(&core.db).await.unwrap();
            match job.job_type.as_str() {
                "source_acquire" => run(core, &job).await,
                "media" => crate::core::run_media(core, &job).await,
                "quality_maintenance" => crate::quality::maintain(core, &job).await,
                _ => panic!("unexpected one-source job {}", job.job_type),
            }
            .unwrap();
            job.succeed(&core.db).await.unwrap();
        }
    }
    panic!("one-source work did not drain");
}

#[tokio::test]
async fn c14_live_distinct_previews_share_queued_request_and_reject_changed_choice() {
    let (_dir, core) = fixture().await;
    let mut source = source();
    source.kind = "live".into();
    source.canonical_url = "https://live.douyin.com/700000001".into();
    source.detail = None;
    source.media.candidates.clear();
    source.live = Some(LiveSnapshot {
        room_key: "actual-room".into(),
        state: "live".into(),
        source_session: Some("broadcast".into()),
        title: "合成直播".into(),
        metadata: json!({"display_room_key":"700000001"}),
        streams: vec![LiveStream {
            protocol: "flv".into(),
            url: "https://fixture.douyinvod.com/live.flv".into(),
            quality: "HD".into(),
            audio_only: false,
            public_headers: json!({}),
        }],
    });
    let a = save_preview(&core, "douyin", source.clone(), None, None)
        .await
        .unwrap();
    let b = save_preview(&core, "douyin", source.clone(), None, None)
        .await
        .unwrap();
    let (a, b) = tokio::join!(
        confirm(
            &core,
            SourceConfirmArgs {
                preview_id: a["preview_id"].as_str().unwrap().into(),
                selected: selected(&a)
            }
        ),
        confirm(
            &core,
            SourceConfirmArgs {
                preview_id: b["preview_id"].as_str().unwrap().into(),
                selected: selected(&b)
            }
        )
    );
    let a = a.unwrap();
    assert_eq!(a["job_id"], b.unwrap()["job_id"]);
    source.live.as_mut().unwrap().streams[0].quality = "SD".into();
    let c = save_preview(&core, "douyin", source, None, None)
        .await
        .unwrap();
    assert!(
        confirm(
            &core,
            SourceConfirmArgs {
                preview_id: c["preview_id"].as_str().unwrap().into(),
                selected: selected(&c)
            }
        )
        .await
        .is_err()
    );
    core.db
        .read(|c| {
            assert_eq!(
                c.query_row(
                    "SELECT count(*) FROM jobs WHERE job_type='source_acquire'",
                    [],
                    |r| r.get::<_, i64>(0)
                )?,
                1
            );
            assert_eq!(
                c.query_row(
                    "SELECT count(*) FROM subscriptions WHERE acquisition_kind='subscription'",
                    [],
                    |r| r.get::<_, i64>(0)
                )?,
                0
            );
            Ok(())
        })
        .await
        .unwrap();
    cancel(&core, a["request_id"].as_str().unwrap().into())
        .await
        .unwrap();
    let next = confirm(
        &core,
        SourceConfirmArgs {
            preview_id: c["preview_id"].as_str().unwrap().into(),
            selected: selected(&c),
        },
    )
    .await
    .unwrap();
    assert_ne!(a["job_id"], next["job_id"]);
    core.shutdown().await;
}

#[tokio::test]
async fn c14_source_preview_expiry_credential_change_idempotency_cancel_and_restart() {
    let (_dir, core) = fixture().await;
    let preview = save(&core).await;
    let id = preview["preview_id"].as_str().unwrap().to_owned();
    assert!(!preview.to_string().contains("ephemeral"));
    assert!(
        confirm(
            &core,
            SourceConfirmArgs {
                preview_id: id.clone(),
                selected: vec![]
            }
        )
        .await
        .is_err()
    );
    assert!(
        confirm(
            &core,
            SourceConfirmArgs {
                preview_id: id.clone(),
                selected: vec!["forged-choice".into()]
            }
        )
        .await
        .is_err()
    );
    let a = SourceConfirmArgs {
        preview_id: id.clone(),
        selected: selected(&preview),
    };
    let (first, second) = tokio::join!(confirm(&core, a.clone()), confirm(&core, a.clone()));
    assert_eq!(first.unwrap()["job_id"], second.unwrap()["job_id"]);
    core.db
        .read(|c| {
            assert_eq!(
                c.query_row(
                    "SELECT count(*) FROM subscriptions WHERE acquisition_kind='subscription'",
                    [],
                    |r| r.get::<_, i64>(0)
                )?,
                0
            );
            assert_eq!(
                c.query_row(
                    "SELECT count(*) FROM jobs WHERE job_type='source_acquire'",
                    [],
                    |r| r.get::<_, i64>(0)
                )?,
                1
            );
            Ok(())
        })
        .await
        .unwrap();
    let profile = core.profile.clone();
    core.shutdown().await;
    drop(core);
    let core = Core::open(profile).await.unwrap();
    assert_eq!(list(&core).await.unwrap()["items"][0]["request_id"], id);
    cancel(&core, id.clone()).await.unwrap();
    assert!(confirm(&core, a).await.is_err());
    let preview = save(&core).await;
    let id = preview["preview_id"].as_str().unwrap().to_owned();
    let p = id.clone();
    core.db
        .write(move |tx| {
            tx.execute(
                "UPDATE source_requests SET expires_at='2000-01-01' WHERE request_id=?1",
                [p],
            )?;
            Ok(())
        })
        .await
        .unwrap();
    assert!(
        confirm(
            &core,
            SourceConfirmArgs {
                preview_id: id,
                selected: selected(&preview)
            }
        )
        .await
        .is_err()
    );
    core.db.write(|tx|{tx.execute("INSERT INTO credentials_state(credential_id,platform,vault_ref,status,created_at,updated_at) VALUES('credential','douyin','unused','valid','t','t')",[])?;Ok(())}).await.unwrap();
    let preview = save_preview(
        &core,
        "douyin",
        source(),
        Some(("credential".into(), 1)),
        Some(jobs::now()),
    )
    .await
    .unwrap();
    core.db
        .write(|tx| {
            tx.execute(
                "UPDATE credentials_state SET generation=2 WHERE credential_id='credential'",
                [],
            )?;
            Ok(())
        })
        .await
        .unwrap();
    assert!(
        confirm(
            &core,
            SourceConfirmArgs {
                preview_id: preview["preview_id"].as_str().unwrap().into(),
                selected: selected(&preview)
            }
        )
        .await
        .is_err()
    );
    core.shutdown().await;
}

#[test]
fn c14_selection_rejects_downgrade_required_resource_changes_and_actual_mismatch() {
    let original = source();
    let before = source_snapshot(&original).unwrap();
    let ids = vec![before["offers"][0]["id"].as_str().unwrap().into()];
    let mut rotated = original.clone();
    rotated.media.candidates[0].segments[0].primary =
        "https://fixture.douyinvod.com/file.mp4?token=rotated".into();
    assert_eq!(before, source_snapshot(&rotated).unwrap());
    for change in 0..6 {
        let mut changed = original.clone();
        let candidate = &mut changed.media.candidates[0];
        match change {
            0 => candidate.claimed_spec["height"] = json!(720),
            1 => candidate.segments.push(candidate.segments[0].clone()),
            2 => candidate.expected_size = Some(99),
            3 => candidate.claimed_spec["codec"] = json!("hevc"),
            4 => candidate.claimed_spec["bandwidth"] = json!(500000),
            _ => candidate.claimed_spec["container"] = json!("flv"),
        };
        assert!(selection(&source_snapshot(&changed).unwrap(), &ids).is_err());
    }
    let mut missing = original.clone();
    let mut r = missing.detail.as_ref().unwrap().resources[0].clone();
    r.role = "audio".into();
    r.source_attachment_id = "required-audio".into();
    missing.detail.as_mut().unwrap().resources.push(r);
    assert!(source_snapshot(&missing).is_err());
    for measured in [
        json!({"width":1280,"height":720,"codec":"h264","audio_tracks":1}),
        json!({"width":1920,"height":1080,"codec":"hevc","audio_tracks":1}),
        json!({"width":1920,"height":1080,"codec":"h264","audio_tracks":0}),
        json!({}),
    ] {
        assert!(validate_measured(&original.media.candidates[0], &measured).is_err());
    }
    assert!(
        validate_measured(
            &original.media.candidates[0],
            &json!({"width":1920,"height":1080,"codec":"h264","audio_tracks":1})
        )
        .is_ok()
    );
    assert!(!live_meets_minimum(
        &json!({"width":360,"height":1280,"codec":"h264"})
    ));
    assert!(live_meets_minimum(
        &json!({"width":1280,"height":720,"codec":"h264"})
    ));
    assert!(
        CoreError::SourceUnavailable("room is not live".into())
            .to_api_error()
            .user_action
            .unwrap()
            .contains("未开播")
    );
    for input in [
        "http://www.douyin.com/video/1",
        "https://www.douyin.com.evil.invalid/video/1",
        "https://user@www.bilibili.com/video/BV1Ab4C5D6E7",
        "https://127.0.0.1/video/1",
    ] {
        assert!(platform(input).is_err());
    }
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn c14_two_production_links_selected_media_core_objects_offline_and_upgrade() {
    for site in ["bilibili", "douyin"] {
        let (dir, core) = fixture().await;
        let media = dir.path().join("test.mp4");
        let mut ff = std::process::Command::new(core.media_backend.ffmpeg.as_ref().unwrap());
        #[cfg(windows)]
        {
            use std::os::windows::process::CommandExt;
            ff.creation_flags(0x08000000);
        }
        assert!(
            ff.args([
                "-hide_banner",
                "-loglevel",
                "error",
                "-f",
                "lavfi",
                "-i",
                "testsrc=size=1280x720:rate=25",
                "-f",
                "lavfi",
                "-i",
                "sine=frequency=440:sample_rate=44100",
                "-t",
                "1",
                "-c:v",
                "libopenh264",
                "-pix_fmt",
                "yuv420p",
                "-c:a",
                "aac"
            ])
            .arg(&media)
            .status()
            .unwrap()
            .success()
        );
        let bytes = std::fs::read(&media).unwrap();
        let length = bytes.len();
        let upgraded = dir.path().join("high.mp4");
        let mut ff = std::process::Command::new(core.media_backend.ffmpeg.as_ref().unwrap());
        #[cfg(windows)]
        {
            use std::os::windows::process::CommandExt;
            ff.creation_flags(0x08000000);
        }
        assert!(
            ff.args([
                "-hide_banner",
                "-loglevel",
                "error",
                "-f",
                "lavfi",
                "-i",
                "testsrc=size=1920x1080:rate=25",
                "-f",
                "lavfi",
                "-i",
                "sine=frequency=440:sample_rate=44100",
                "-t",
                "1",
                "-c:v",
                "libopenh264",
                "-pix_fmt",
                "yuv420p",
                "-c:a",
                "aac"
            ])
            .arg(&upgraded)
            .status()
            .unwrap()
            .success()
        );
        let high_bytes = std::fs::read(upgraded).unwrap();
        let high_length = high_bytes.len();
        let high_hash = digest_bytes(&high_bytes);
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let address = listener.local_addr().unwrap();
        for host in ["fixture.bilivideo.com", "fixture.douyinvod.com"] {
            core.http.route_fixture(host, &format!("http://{address}"));
        }
        let expected = bytes.clone();
        let server = tokio::spawn(async move {
            loop {
                let (mut socket, _) = listener.accept().await.unwrap();
                let bytes = bytes.clone();
                let high_bytes = high_bytes.clone();
                tokio::spawn(async move {
                    let mut request = [0; 8192];
                    let n = socket.read(&mut request).await.unwrap();
                    if site == "douyin" {
                        let headers = String::from_utf8_lossy(&request[..n]).to_ascii_lowercase();
                        assert!(headers.contains("user-agent: mozilla/5.0"));
                        assert!(headers.contains("referer: https://www.douyin.com/"));
                    }
                    let bytes = if String::from_utf8_lossy(&request[..n]).contains("/high.mp4") {
                        high_bytes
                    } else {
                        bytes
                    };
                    assert!(
                        !String::from_utf8_lossy(&request[..n])
                            .to_ascii_lowercase()
                            .contains("cookie:")
                    );
                    socket.write_all(format!("HTTP/1.1 200 OK\r\nContent-Length: {}\r\nContent-Type: video/mp4\r\nConnection: close\r\n\r\n",bytes.len()).as_bytes()).await.unwrap();
                    socket.write_all(&bytes).await.unwrap();
                });
            }
        });
        let high = Arc::new(AtomicBool::new(false));
        let offered = high.clone();
        core.host.set_proxy_fixture(Arc::new(move|params|{
            let high=offered.load(Ordering::SeqCst);let length=if high {high_length} else {length};let width=if high {1920} else {1280};let height=if high {1080} else {720};let name=if high {"high"} else {"video"};
            let u=url::Url::parse(params["url"].as_str().unwrap()).unwrap();let value=match u.path(){
                "/x/web-interface/nav"=>serde_json::from_str::<Value>(include_str!("../../../tests/fixtures/bilibili/nav.json")).unwrap(),
                "/x/web-interface/wbi/view"=>json!({"code":0,"data":{"bvid":"BV1Ab4C5D6E7","aid":1,"title":"合成视频","desc":"正文","owner":{"mid":1,"name":"合成作者"},"pages":[{"cid":2,"page":1,"part":"正片","duration":1}],"stat":{}}}),
                "/x/player/wbi/playurl"=>json!({"code":0,"data":{"quality":if high {120} else {80},"durl":[{"url":format!("https://fixture.bilivideo.com/{name}.mp4"),"size":length,"length":1000}]}}),
                "/aweme/v1/web/aweme/detail/"=>json!({"status_code":0,"aweme_detail":{"aweme_id":"7000000000000000001","aweme_type":0,"desc":"合成视频","author":{"sec_uid":"MS4synthetic","nickname":"合成作者"},"video":{"width":width,"height":height,"duration":1000,"play_addr":{"uri":"video","url_list":[format!("https://fixture.douyinvod.com/{name}.mp4")],"data_size":length}}}}),
                _=>panic!("one-source operation must not scan an author: {}",u.path())};
            Ok(ProxyFetchResult{status:200,headers:json!({}),body_b64:base64::engine::general_purpose::STANDARD.encode(value.to_string()),final_url:params["url"].as_str().unwrap().into()})
        }));
        let link = if site == "bilibili" {
            "https://www.bilibili.com/video/BV1Ab4C5D6E7"
        } else {
            "https://www.douyin.com/video/7000000000000000001"
        };
        let p = preview(
            &core,
            SourcePreviewArgs {
                url: link.into(),
                credential_id: None,
            },
        )
        .await
        .unwrap();
        let result = confirm(
            &core,
            SourceConfirmArgs {
                preview_id: p["preview_id"].as_str().unwrap().into(),
                selected: selected(&p),
            },
        )
        .await
        .unwrap();
        for _ in 0..12 {
            let jobs = jobs::claim_due(&core.db, "source-test", 1).await.unwrap();
            if jobs.is_empty() {
                break;
            }
            for job in jobs {
                job.mark_running(&core.db).await.unwrap();
                let result = match job.job_type.as_str() {
                    "source_acquire" => run(&core, &job).await,
                    "media" => crate::core::run_media(&core, &job).await,
                    "quality_maintenance" => crate::quality::maintain(&core, &job).await,
                    _ => panic!("unexpected source work {}", job.job_type),
                };
                assert!(result.is_ok(), "{site} {}: {result:?}", job.job_type);
                job.succeed(&core.db).await.unwrap();
            }
        }
        let rows=core.db.read(|c|Ok(c.prepare("SELECT b.blob_id,b.size FROM media_variants v JOIN blobs b ON b.blob_id=v.blob_id JOIN asset_selections s ON s.variant_id=v.variant_id")?.query_map([],|r|Ok((r.get::<_,String>(0)?,r.get::<_,i64>(1)?)))?.collect::<Result<Vec<_>,_>>()?)).await.unwrap();
        assert!(!rows.is_empty());
        assert!(rows.iter().any(|(_, n)| *n == length as i64));
        for (blob, _) in rows {
            assert_eq!(
                std::fs::read(
                    core.objects
                        .object_path(blob.strip_prefix("sha256:").unwrap())
                        .unwrap()
                )
                .unwrap(),
                expected
            );
        }
        let entry = result["entry_id"].as_str().unwrap().to_owned();
        let acquisition = crate::acquisitions::list(&core, json!({"entry_id":entry}))
            .await
            .unwrap();
        assert!(
            acquisition["items"]
                .as_array()
                .unwrap()
                .iter()
                .any(|v| v["outcome"] == "acquired")
        );
        let counts = core
            .db
            .read(|c| {
                Ok((
                    c.query_row(
                        "SELECT count(*) FROM subscriptions WHERE acquisition_kind='subscription'",
                        [],
                        |r| r.get::<_, i64>(0),
                    )?,
                    c.query_row("SELECT count(*) FROM jobs WHERE job_type='scan'", [], |r| {
                        r.get::<_, i64>(0)
                    })?,
                ))
            })
            .await
            .unwrap();
        assert_eq!(counts, (0, 0));
        high.store(true, Ordering::SeqCst);
        let p = preview(
            &core,
            SourcePreviewArgs {
                url: link.into(),
                credential_id: None,
            },
        )
        .await
        .unwrap();
        confirm(
            &core,
            SourceConfirmArgs {
                preview_id: p["preview_id"].as_str().unwrap().into(),
                selected: selected(&p),
            },
        )
        .await
        .unwrap();
        drain_source_jobs(&core).await;
        let upgraded = crate::upgrades::list(&core, json!({"state":"pending"}))
            .await
            .unwrap();
        assert_eq!(
            upgraded["items"].as_array().unwrap().len(),
            1,
            "{site}: explicit source upgrade missing"
        );
        let selected_blob=core.db.read(|c|Ok(c.query_row("SELECT v.blob_id FROM asset_selections s JOIN media_variants v ON v.variant_id=s.variant_id WHERE v.source_representation!='playback-mux-v1' LIMIT 1",[],|r|r.get::<_,String>(0))?)).await.unwrap();
        assert_eq!(
            selected_blob,
            format!("sha256:{}", digest_bytes(&expected)),
            "new source request replaced prior bytes without a decision"
        );
        let pending = &upgraded["items"][0];
        crate::upgrades::decide(
            &core,
            vec![trace_contracts::acquisition::UpgradeDecision {
                candidate_id: pending["candidate_id"].as_str().unwrap().into(),
                decision: trace_contracts::acquisition::UpgradeAction::Accept,
                expected: pending["expected"].clone(),
            }],
        )
        .await
        .unwrap();
        core.db.read(move|c|{let blob:String=c.query_row("SELECT v.blob_id FROM asset_selections s JOIN media_variants v ON v.variant_id=s.variant_id WHERE v.source_representation!='playback-mux-v1' LIMIT 1",[],|r|r.get(0))?;assert_eq!(blob,format!("sha256:{high_hash}"));Ok(())}).await.unwrap();
        let profile = core.profile.clone();
        core.shutdown().await;
        drop(core);
        server.abort();
        let core = Core::open(profile).await.unwrap();
        assert!(
            list(&core).await.unwrap()["items"]
                .as_array()
                .unwrap()
                .iter()
                .any(|r| r["state"] == "succeeded")
        );
        core.shutdown().await;
    }
}

fn digest_bytes(bytes: &[u8]) -> String {
    Sha256::digest(bytes)
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect()
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn c14_dash_choice_survives_safe_id_normalization_and_keeps_required_audio() {
    let (dir, core) = fixture().await;
    let muxed = dir.path().join("fixture.mp4");
    let video = dir.path().join("video.mp4");
    let audio = dir.path().join("audio.m4a");
    let execute = |args: &[&str], output: &std::path::Path| {
        let mut cmd = std::process::Command::new(core.media_backend.ffmpeg.as_ref().unwrap());
        #[cfg(windows)]
        {
            use std::os::windows::process::CommandExt;
            cmd.creation_flags(0x08000000);
        }
        assert!(
            cmd.args(["-hide_banner", "-loglevel", "error"])
                .args(args)
                .arg(output)
                .status()
                .unwrap()
                .success()
        );
    };
    execute(
        &[
            "-f",
            "lavfi",
            "-i",
            "testsrc=size=1280x720:rate=25",
            "-f",
            "lavfi",
            "-i",
            "sine=frequency=440:sample_rate=44100",
            "-t",
            "2",
            "-c:v",
            "libopenh264",
            "-pix_fmt",
            "yuv420p",
            "-c:a",
            "aac",
        ],
        &muxed,
    );
    execute(
        &["-i", muxed.to_str().unwrap(), "-map", "0:v:0", "-c", "copy"],
        &video,
    );
    execute(
        &["-i", muxed.to_str().unwrap(), "-map", "0:a:0", "-c", "copy"],
        &audio,
    );
    let video_bytes = std::fs::read(video).unwrap();
    let audio_bytes = std::fs::read(audio).unwrap();
    let expected = [digest_bytes(&video_bytes), digest_bytes(&audio_bytes)];
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    core.http.route_fixture(
        "fixture.bilivideo.com",
        &format!("http://{}", listener.local_addr().unwrap()),
    );
    let server = tokio::spawn(async move {
        for _ in 0..2 {
            let (mut socket, _) = listener.accept().await.unwrap();
            let mut request = [0; 8192];
            let count = socket.read(&mut request).await.unwrap();
            let request = String::from_utf8_lossy(&request[..count]).to_ascii_lowercase();
            assert!(!request.contains("cookie:"));
            let bytes = if request.starts_with("get /video.mp4 ") {
                &video_bytes
            } else if request.starts_with("get /audio.m4a ") {
                &audio_bytes
            } else {
                panic!("unselected source was requested")
            };
            socket.write_all(format!("HTTP/1.1 200 OK\r\nContent-Length: {}\r\nContent-Type: video/mp4\r\nConnection: close\r\n\r\n",bytes.len()).as_bytes()).await.unwrap();
            socket.write_all(bytes).await.unwrap();
        }
    });
    core.host.set_proxy_fixture(Arc::new(|params| {
        let url = url::Url::parse(params["url"].as_str().unwrap()).unwrap();
        let value = match url.path() {
            "/x/web-interface/nav" => {
                assert_eq!(params["with_credential"], true);
                let mut value:Value=serde_json::from_str(include_str!("../../../tests/fixtures/bilibili/nav.json")).unwrap();
                value["code"]=json!(-101);value
            },
            "/x/web-interface/wbi/view" => json!({"code":0,"data":{"bvid":"BV1Ab4C5D6E7","aid":1,"title":"DASH合成选项","desc":"必要音轨","duration":2,"owner":{"mid":1,"name":"合成作者"},"pages":[{"cid":2,"page":1,"part":"正片","duration":2}],"stat":{}}}),
            "/x/player/wbi/playurl" => json!({"code":0,"data":{"dash":{"video":[{"id":80,"width":1920,"height":1080,"codecs":"avc1.640028","frame_rate":"25","bandwidth":2000000,"base_url":"https://fixture.bilivideo.com/unselected.mp4"},{"id":64,"width":1280,"height":720,"codecs":"avc1.640028","frame_rate":"25","bandwidth":1000000,"base_url":"https://fixture.bilivideo.com/video.mp4"}],"audio":[{"id":30280,"codecs":"mp4a.40.2","bandwidth":128000,"base_url":"https://fixture.bilivideo.com/audio.m4a"}]}}}),
            _ => panic!("one-shot source made an unrelated metadata request"),
        };
        Ok(ProxyFetchResult { status:200, headers:json!({}), body_b64:base64::engine::general_purpose::STANDARD.encode(value.to_string()), final_url:params["url"].as_str().unwrap().into() })
    }));
    let p = preview(
        &core,
        SourcePreviewArgs {
            url: "https://www.bilibili.com/video/BV1Ab4C5D6E7".into(),
            credential_id: None,
        },
    )
    .await
    .unwrap();
    let chosen = p["offers"]
        .as_array()
        .unwrap()
        .iter()
        .filter(|v| v["role"] == "audio" || v["spec"]["width"] == 1280)
        .map(|v| v["id"].as_str().unwrap().to_owned())
        .collect::<Vec<_>>();
    assert_eq!(chosen.len(), 2);
    confirm(
        &core,
        SourceConfirmArgs {
            preview_id: p["preview_id"].as_str().unwrap().into(),
            selected: chosen,
        },
    )
    .await
    .unwrap();
    drain_source_jobs(&core).await;
    server.await.unwrap();
    let selected=core.db.read(|c|Ok(c.prepare("SELECT m.role,v.blob_id FROM media_items m JOIN asset_selections s ON s.media_id=m.media_id JOIN media_variants v ON v.variant_id=s.variant_id WHERE m.required=1 ORDER BY m.role DESC")?.query_map([],|r|Ok((r.get::<_,String>(0)?,r.get::<_,String>(1)?)))?.collect::<Result<Vec<_>,_>>()?)).await.unwrap();
    assert_eq!(selected.len(), 2);
    for ((role, blob), digest) in selected.iter().zip(expected) {
        assert!(matches!(role.as_str(), "video" | "audio"));
        assert_eq!(blob, &format!("sha256:{digest}"));
        assert_eq!(
            digest_bytes(&std::fs::read(core.objects.object_path(&digest).unwrap()).unwrap()),
            digest
        );
    }
    let playback: String = core
        .db
        .read(|c| {
            Ok(c.query_row(
                "SELECT blob_id FROM media_variants WHERE source_representation='playback-mux-v1'",
                [],
                |r| r.get(0),
            )?)
        })
        .await
        .unwrap();
    let measured = core
        .media_backend
        .probe(
            &core
                .objects
                .object_path(playback.strip_prefix("sha256:").unwrap())
                .unwrap(),
            &core.cancel,
        )
        .await
        .unwrap();
    assert_eq!((measured.width, measured.height), (Some(1280), Some(720)));
    assert_eq!(measured.audio_tracks, Some(1));
    let profile = core.profile.clone();
    core.shutdown().await;
    drop(core);
    let reopened = Core::open(profile).await.unwrap();
    assert!(
        list(&reopened).await.unwrap()["items"]
            .as_array()
            .unwrap()
            .iter()
            .any(|r| r["state"] == "succeeded")
    );
    reopened.shutdown().await;
}
