use super::*;
use serde_json::json;

#[tokio::test]
async fn c08_playback_cache_clear_rebuild_budget_and_originals() {
    let dir = tempfile::tempdir().unwrap();
    let core = Core::open(Profile {
        data_root: dir.path().join("data"),
        archive_root: dir.path().join("archive"),
        vault_dir: Some(dir.path().join("vault")),
        ..Default::default()
    })
    .await
    .unwrap();
    core.db.write(|tx|{
        tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('a','bilibili','42','t')",[])?;
        tx.execute("INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES('e','bilibili','video','42','a','t')",[])?;
        tx.execute("INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,completeness,observed_at) VALUES('r','e','digest','缓存','正文','full','t')",[])?;
        for role in ["video","audio"] {tx.execute("INSERT INTO media_items(media_id,revision_id,role,position,source_attachment_id,group_key,required) VALUES(?1,'r',?1,0,?1,'g',1)",[role])?;}
        Ok(())
    }).await.unwrap();
    jobs::enqueue(
        &core.db,
        "cache-test".into(),
        "media",
        "e",
        json!({}),
        1,
        1,
        0,
    )
    .await
    .unwrap();
    let job = jobs::claim_due(&core.db, "cache-test", 1)
        .await
        .unwrap()
        .remove(0);
    job.mark_running(&core.db).await.unwrap();
    let mut source_paths = Vec::new();
    for role in ["video", "audio"] {
        let file = dir.path().join(format!("{role}.mp4"));
        let mut ffmpeg = std::process::Command::new(core.media_backend.ffmpeg.as_ref().unwrap());
        ffmpeg.args(["-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i"]);
        if role == "video" {
            ffmpeg.args([
                "testsrc=size=64x64:rate=10",
                "-c:v",
                "libopenh264",
                "-pix_fmt",
                "yuv420p",
            ]);
        } else {
            ffmpeg.args(["sine=frequency=440:sample_rate=44100", "-c:a", "aac"]);
        }
        assert!(
            ffmpeg
                .args(["-t", "1"])
                .arg(&file)
                .status()
                .unwrap()
                .success()
        );
        let bytes = std::fs::read(&file).unwrap();
        let spec = core.media_backend.probe(&file, &core.cancel).await.unwrap();
        let op = OperationId::new(format!("cache-source-{role}"));
        let mut writer = core
            .objects
            .start_receiving(
                &core.db,
                ReceiveParams {
                    job_id: Some(job.job_id.clone()),
                    subject_kind: "media_variant".into(),
                    subject_id: role.into(),
                    generation: job.generation,
                    mime: Some(
                        if role == "video" {
                            "video/mp4"
                        } else {
                            "audio/mp4"
                        }
                        .into(),
                    ),
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
        core.objects
            .install_object(&core.db, &op, &sha)
            .await
            .unwrap();
        core.objects
            .commit(
                &core.db,
                &op,
                &CommitInfo {
                    job: Some(job.clone()),
                    media_id: role.into(),
                    source_representation: "original".into(),
                    claimed_spec: json!({}),
                },
                Some(serde_json::to_value(spec).unwrap()),
            )
            .await
            .unwrap();
        source_paths.push(core.objects.object_path(&sha).unwrap());
    }
    create_playback_mux(&core, &job, "r", "g").await.unwrap();
    assert!(
        crate::cache::status(&core.db).await.unwrap()["derived_bytes"]
            .as_i64()
            .unwrap()
            > 0
    );
    crate::cache::control(&core, "playback".into(), false)
        .await
        .unwrap();
    crate::cache::configure(&core, 1048576, 0).await.unwrap();
    crate::cache::control(&core, "playback".into(), true)
        .await
        .unwrap();
    core.db
        .write(|tx| crate::quality::evaluate(tx, "r", false, 0))
        .await
        .unwrap();
    create_playback_mux(&core, &job, "r", "g").await.unwrap();
    core.db
        .read(|c| {
            assert_eq!(
                c.query_row(
                    "SELECT retained FROM variant_retention WHERE reason='cache_cleared'",
                    [],
                    |r| r.get::<_, i64>(0)
                )?,
                0,
                "reenabling under zero budget cannot silently resurrect cleared cache"
            );
            Ok(())
        })
        .await
        .unwrap();
    crate::quality::recycle(&core).await.unwrap();
    crate::quality::recycle(&core).await.unwrap();
    assert_eq!(
        crate::cache::status(&core.db).await.unwrap()["derived_bytes"],
        0
    );
    assert!(source_paths.iter().all(|p| p.exists()));
    crate::cache::configure(&core, 1048576, 64 * 1024 * 1024)
        .await
        .unwrap();
    create_playback_mux(&core, &job, "r", "g").await.unwrap();
    assert!(
        crate::cache::status(&core.db).await.unwrap()["derived_bytes"]
            .as_i64()
            .unwrap()
            > 0
    );
    core.db.read(|c|{assert_eq!(c.query_row("SELECT count(*) FROM materializations WHERE operation_id LIKE '%playback%' AND stage='COMMITTED'",[],|r|r.get::<_,i64>(0))?,2,"rebuild preserves old materialization and commits a new receipt");assert_eq!(c.query_row("SELECT count(*) FROM asset_selections s JOIN media_variants v ON s.variant_id=v.variant_id WHERE source_representation='original'",[],|r|r.get::<_,i64>(0))?,2);Ok(())}).await.unwrap();
    core.shutdown().await;
}

#[tokio::test]
async fn c07_deleted_identity_rescan_and_backup_never_silently_reinclude() {
    let dir = tempfile::tempdir().unwrap();
    let core = Core::open(Profile {
        data_root: dir.path().join("data"),
        archive_root: dir.path().join("archive"),
        vault_dir: Some(dir.path().join("vault")),
        ..Default::default()
    })
    .await
    .unwrap();
    core.db.write(|tx|{tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('a','douyin','MS4fixture','t')",[])?;tx.execute("INSERT INTO subscriptions(subscription_id,account_id,content_type,range_json,created_at,updated_at) VALUES('s','a','article','{}','t','t')",[])?;Ok(())}).await.unwrap();
    jobs::enqueue(
        &core.db,
        "scan-delete".into(),
        "scan",
        "s",
        json!({}),
        1,
        1,
        0,
    )
    .await
    .unwrap();
    let job = jobs::claim_due(&core.db, "delete-test", 1)
        .await
        .unwrap()
        .remove(0);
    job.mark_running(&core.db).await.unwrap();
    let row = || {
        serde_json::from_value(json!({"native_entry_key":"77","native_kind":"article","title":"完整记录","summary_body":"保留的列表摘要","published_at":null})).unwrap()
    };
    process_scan_page(
        &core,
        &job,
        "douyin",
        "s",
        "deep",
        vec![row()],
        Some("first".into()),
        &json!({}),
    )
    .await
    .unwrap();
    let entry: String = core
        .db
        .read(|c| Ok(c.query_row("SELECT entry_id FROM entries", [], |r| r.get(0))?))
        .await
        .unwrap();
    let preview = crate::deletion::preview(&core, entry.clone(), "archive".into())
        .await
        .unwrap();
    let deleted = crate::deletion::apply(
        &core,
        entry.clone(),
        "archive".into(),
        preview["token"].as_str().unwrap().into(),
    )
    .await
    .unwrap();
    process_scan_page(
        &core,
        &job,
        "douyin",
        "s",
        "deep",
        vec![row()],
        Some("second".into()),
        &json!({}),
    )
    .await
    .unwrap();
    core.db
        .read(|c| {
            assert_eq!(
                c.query_row("SELECT lifecycle FROM entries", [], |r| r
                    .get::<_, String>(0))?,
                "deleted"
            );
            assert_eq!(
                c.query_row("SELECT count(*) FROM entry_revisions", [], |r| r
                    .get::<_, i64>(0))?,
                1
            );
            assert_eq!(
                c.query_row("SELECT count(*) FROM source_observations", [], |r| r
                    .get::<_, i64>(0))?,
                1
            );
            assert_eq!(
                c.query_row("SELECT state FROM jobs WHERE job_type='observe'", [], |r| r
                    .get::<_, String>(0))?,
                "cancelled"
            );
            Ok(())
        })
        .await
        .unwrap();
    let backups = dir.path().join("backups");
    let backup = crate::storage_ops::backup(&core, &backups, false)
        .await
        .unwrap();
    let result = crate::storage_ops::restore(
        &core,
        &backups.join(backup["backup_id"].as_str().unwrap()),
        &dir.path().join("restored"),
    )
    .await
    .unwrap();
    let restored = Core::open(
        crate::config::load_profile(std::path::Path::new(
            result["profile_path"].as_str().unwrap(),
        ))
        .unwrap(),
    )
    .await
    .unwrap();
    let e = entry.clone();
    restored
        .db
        .read(move |c| {
            assert_eq!(
                c.query_row(
                    "SELECT lifecycle FROM entries WHERE entry_id=?1",
                    [e],
                    |r| r.get::<_, String>(0)
                )?,
                "deleted"
            );
            assert_eq!(
                c.query_row("SELECT count(*) FROM deletion_records", [], |r| r
                    .get::<_, i64>(0))?,
                1
            );
            Ok(())
        })
        .await
        .unwrap();
    assert_eq!(
        crate::feed::query(&restored.db, serde_json::from_value(json!({})).unwrap())
            .await
            .unwrap()["counts"]["global"],
        0
    );
    crate::deletion::restore(&restored, entry, deleted["generation"].as_i64().unwrap())
        .await
        .unwrap();
    assert_eq!(
        crate::feed::query(&restored.db, serde_json::from_value(json!({})).unwrap())
            .await
            .unwrap()["counts"]["global"],
        1
    );
    restored.shutdown().await;
    core.shutdown().await;
}

#[tokio::test]
async fn c01_collection_membership_is_preserved_per_entry_observation() {
    let dir = tempfile::tempdir().unwrap();
    let core = Core::open(Profile {
        archive_id: "collection-observations".into(),
        data_root: dir.path().join("data"),
        archive_root: dir.path().join("archive"),
        vault_dir: Some(dir.path().join("vault")),
        ..Default::default()
    })
    .await
    .unwrap();
    core.db.write(|tx|{
        tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('a','douyin','MS4fixture','t')",[])?;
        tx.execute("INSERT INTO subscriptions(subscription_id,account_id,content_type,range_json,created_at,updated_at) VALUES('s','a','article','{}','t','t')",[])?;Ok(())
    }).await.unwrap();
    jobs::enqueue(&core.db, "scan".into(), "scan", "s", json!({}), 1, 1, 0)
        .await
        .unwrap();
    let job = jobs::claim_due(&core.db, "fixture", 1)
        .await
        .unwrap()
        .remove(0);
    job.mark_running(&core.db).await.unwrap();
    for (index, source) in ["mix", "series"].iter().enumerate() {
        let entry=serde_json::from_value(json!({"native_entry_key":"77","native_kind":"article","title":"正文","published_at":null,"summary_body":"列表摘要","stats":{"likes":index}})).unwrap();
        process_scan_page(&core,&job,"douyin","s","deep",vec![entry],Some(index.to_string()),&json!({"source":"mix.aweme","collection_source":source,"collection_id":"90071992547409931"})).await.unwrap();
    }
    core.db.read(|c|{
        let rows=c.prepare("SELECT json_extract(evidence,'$._trace_enumeration.collection_source'),json_extract(evidence,'$._trace_enumeration.collection_id') FROM source_observations ORDER BY observation_seq")?.query_map([],|r|Ok((r.get::<_,String>(0)?,r.get::<_,String>(1)?)))?.collect::<Result<Vec<_>,_>>()?;
        assert_eq!(rows,vec![("mix".into(),"90071992547409931".into()),("series".into(),"90071992547409931".into())]);
        assert_eq!(c.query_row("SELECT count(*) FROM entries",[],|r|r.get::<_,i64>(0))?,1);
        assert_eq!(c.query_row("SELECT count(*) FROM entry_revisions",[],|r|r.get::<_,i64>(0))?,1,"collection/stat observations do not create body revisions");Ok(())
    }).await.unwrap();
    core.shutdown().await;
}

#[tokio::test]
async fn c07_real_media_probe_keeps_each_variants_own_spec_and_upgrades_selection() {
    let dir = tempfile::tempdir().unwrap();
    let core = Core::open(Profile {
        archive_id: "measured-quality".into(),
        data_root: dir.path().join("data"),
        archive_root: dir.path().join("archive"),
        vault_dir: Some(dir.path().join("vault")),
        ..Default::default()
    })
    .await
    .unwrap();
    core.db.write(|tx|{
        tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('a','bilibili','1','t')",[])?;
        tx.execute("INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES('e','bilibili','video','1','a','t')",[])?;
        tx.execute("INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at) VALUES('r','e','digest','title','body','t')",[])?;
        tx.execute("INSERT INTO media_items(media_id,revision_id,role,position,source_attachment_id) VALUES('m','r','video',0,'source')",[])?;Ok(())
    }).await.unwrap();
    jobs::enqueue(&core.db, "quality".into(), "media", "e", json!({}), 1, 1, 0)
        .await
        .unwrap();
    let job = jobs::claim_due(&core.db, "test", 1)
        .await
        .unwrap()
        .remove(0);
    job.mark_running(&core.db).await.unwrap();
    for width in [64, 128] {
        let path = dir.path().join(format!("sample-{width}.mp4"));
        let status = std::process::Command::new(core.media_backend.ffmpeg.as_ref().unwrap())
            .args([
                "-hide_banner",
                "-loglevel",
                "error",
                "-f",
                "lavfi",
                "-i",
                &format!("testsrc=size={width}x{width}:rate=10"),
                "-t",
                "1",
                "-c:v",
                "libopenh264",
                "-pix_fmt",
                "yuv420p",
            ])
            .arg(&path)
            .status()
            .unwrap();
        assert!(status.success());
        let bytes = std::fs::read(path).unwrap();
        let op = OperationId::new(format!("quality-{width}"));
        let mut writer = core
            .objects
            .start_receiving(
                &core.db,
                ReceiveParams {
                    job_id: Some(job.job_id.clone()),
                    subject_kind: "media_variant".into(),
                    subject_id: "m".into(),
                    generation: job.generation,
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
        core.objects
            .install_object(&core.db, &op, &sha)
            .await
            .unwrap();
        let candidate:trace_contracts::plugin::MediaCandidate=serde_json::from_value(json!({"resource":{"role":"video","source_attachment_id":"source","position":0},"representation":format!("{width}"),"kind":"muxed","segments":[],"claimed_spec":{"width":width}})).unwrap();
        commit_candidate(&core, &job, &candidate, "m", &op, &sha)
            .await
            .unwrap();
    }
    let widths:Vec<i64>=core.db.read(|c|Ok(c.prepare("SELECT json_extract(measured_spec,'$.width') FROM media_variants ORDER BY source_representation+0")?.query_map([],|r|r.get(0))?.collect::<Result<_,_>>()?)).await.unwrap();
    assert_eq!(widths, vec![64, 128]);
    core.db
        .write(|tx| crate::quality::evaluate(tx, "r", false, 86400))
        .await
        .unwrap();
    let(selected,released):(i64,i64)=core.db.read(|c|Ok((c.query_row("SELECT json_extract(v.measured_spec,'$.width') FROM media_variants v JOIN asset_selections a ON a.variant_id=v.variant_id",[],|r|r.get(0))?,c.query_row("SELECT count(*) FROM variant_retention WHERE retained=0 AND eligible_at IS NOT NULL",[],|r|r.get(0))?))).await.unwrap();
    assert_eq!(selected, 128);
    assert_eq!(released, 1);
    core.db.write(|tx|{
        let resource:trace_contracts::plugin::ResourceIdentityDto=serde_json::from_value(json!({"role":"video","source_attachment_id":"source","position":0})).unwrap();
        for width in [128,64] {
            let candidate:trace_contracts::plugin::MediaCandidate=serde_json::from_value(json!({"resource":resource,"representation":width.to_string(),"kind":"muxed","segments":[],"claimed_spec":{"width":width,"height":width,"frame_rate":"10000/1000"}})).unwrap();
            crate::quality::record_offers(tx,"r",std::slice::from_ref(&resource),&[candidate],false)?;
            assert_eq!(crate::quality::state(tx,"e")?,if width==128{"satisfied"}else{"degraded"});
        }
        assert_eq!(tx.query_row("SELECT json_extract(acquisition_evidence,'$.quality_target.width') FROM media_items WHERE media_id='m'",[],|r|r.get::<_,i64>(0))?,128,"a worse source offer cannot silently lower the target");
        assert_eq!(tx.query_row("SELECT json_extract(v.measured_spec,'$.width') FROM media_variants v JOIN asset_selections a ON a.variant_id=v.variant_id",[],|r|r.get::<_,i64>(0))?,128,"existing verified high quality remains selected");
        Ok(())
    }).await.unwrap();
    // A claimed 256p label cannot admit actual 64p bytes under strict policy.
    core.db.write(|tx|{
        tx.execute("INSERT INTO subscriptions(subscription_id,account_id,content_type,range_json,quality_json,created_at,updated_at) VALUES('s','a','video','{}','{\"min_height\":256,\"strict\":true}','t','t')",[])?;
        Ok(())
    }).await.unwrap();
    let mut strict_job = job.clone();
    strict_job.payload = json!({"subscription_id":"s"});
    let op = OperationId::new("strict-claimed-high");
    let bytes = std::fs::read(dir.path().join("sample-64.mp4")).unwrap();
    let mut writer = core
        .objects
        .start_receiving(
            &core.db,
            ReceiveParams {
                job_id: Some(job.job_id.clone()),
                subject_kind: "media_variant".into(),
                subject_id: "m".into(),
                generation: job.generation,
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
    core.objects
        .install_object(&core.db, &op, &sha)
        .await
        .unwrap();
    let candidate:trace_contracts::plugin::MediaCandidate=serde_json::from_value(json!({"resource":{"role":"video","source_attachment_id":"source","position":0,"required":true},"representation":"claimed-256-actual-64","kind":"muxed","segments":[],"claimed_spec":{"height":256}})).unwrap();
    assert!(matches!(
        commit_candidate(&core, &strict_job, &candidate, "m", &op, &sha).await,
        Err(CoreError::Adapter(
            trace_domain::errors::FailureClass::CapabilityLost
        ))
    ));
    core.db.write(|tx|{
        assert_eq!(tx.query_row("SELECT count(*) FROM media_variants",[],|r|r.get::<_,i64>(0))?,2);
        assert_eq!(tx.query_row("SELECT json_extract(v.measured_spec,'$.height') FROM asset_selections s JOIN media_variants v ON v.variant_id=s.variant_id",[],|r|r.get::<_,i64>(0))?,128);
        tx.execute("UPDATE subscriptions SET quality_json='{\"min_height\":256,\"strict\":false}'",[])?;
        tx.execute("UPDATE media_items SET acquisition_evidence=json_set(acquisition_evidence,'$.requested_quality',json('{\"min_height\":256,\"strict\":false}'))",[])?;
        Ok(())
    }).await.unwrap();
    commit_candidate(&core, &strict_job, &candidate, "m", &op, &sha)
        .await
        .unwrap();
    core.db
        .read(|c| {
            assert_eq!(
                c.query_row("SELECT count(*) FROM media_variants", [], |r| r
                    .get::<_, i64>(0))?,
                3
            );
            assert_eq!(crate::quality::state(c, "e")?, "degraded");
            Ok(())
        })
        .await
        .unwrap();
    core.shutdown().await;
}

#[tokio::test]
async fn c05_metadata_priority_reserves_progress_for_regular_work() {
    let dir = tempfile::tempdir().unwrap();
    let core = Core::open(Profile {
        archive_id: "pacing".into(),
        data_root: dir.path().join("data"),
        archive_root: dir.path().join("archive"),
        vault_dir: Some(dir.path().join("vault")),
        ..Default::default()
    })
    .await
    .unwrap();
    core.metadata_urgent
        .lock()
        .unwrap()
        .insert("bilibili".into(), 10);
    core.metadata_streak
        .lock()
        .unwrap()
        .insert("bilibili".into(), 4);
    tokio::time::timeout(Duration::from_secs(1), core.metadata_pacing("bilibili"))
        .await
        .expect("a live queue must not indefinitely starve content discovery");
    assert_eq!(core.metadata_streak.lock().unwrap()["bilibili"], 0);
    assert_eq!(core.metadata_regular.lock().unwrap()["bilibili"], 0);
    core.metadata_regular
        .lock()
        .unwrap()
        .insert("bilibili".into(), 10);
    tokio::time::timeout(
        Duration::from_secs(3),
        core.metadata_pacing_priority("bilibili", true),
    )
    .await
    .expect("recording detection retains priority after a regular grant");
    assert_eq!(core.metadata_streak.lock().unwrap()["bilibili"], 1);
    core.shutdown().await;
}

#[test]
fn c07_acquisition_identity_separates_spec_and_integrity_but_not_signed_urls() {
    let mut plan:trace_contracts::plugin::MediaCandidate=serde_json::from_value(json!({"resource":{"role":"video","source_attachment_id":"a","position":0},"representation":"same","kind":"muxed","segments":[{"primary":"https://cdn.invalid/a?signature=one","backups":[]}],"claimed_spec":{"width":128},"expected_size":123})).unwrap();
    let first = acquisition_operation("m", "same-job", 1, &plan);
    assert_ne!(first, acquisition_operation("m", "other-job", 1, &plan));
    plan.segments[0].primary = "https://cdn.invalid/a?signature=two".into();
    assert_eq!(first, acquisition_operation("m", "same-job", 1, &plan));
    plan.claimed_spec["width"] = json!(256);
    assert_ne!(first, acquisition_operation("m", "same-job", 1, &plan));
    plan.claimed_spec["width"] = json!(128);
    plan.expected_size = Some(456);
    assert_ne!(first, acquisition_operation("m", "same-job", 1, &plan));
    plan.expected_size = Some(123);
    plan.expected_sha256 = Some("a".repeat(64));
    assert_ne!(first, acquisition_operation("m", "same-job", 1, &plan));
    assert_ne!(
        acquisition_operation("m", "same-job", 1, &plan),
        acquisition_operation("m", "same-job", 2, &plan)
    );
}

#[tokio::test]
async fn overlapping_subscription_can_continue_same_entry_after_owner_pause() {
    let dir = tempfile::tempdir().unwrap();
    let core = Core::open(Profile {
        data_root: dir.path().join("data"),
        archive_root: dir.path().join("archive"),
        vault_dir: Some(dir.path().join("vault")),
        ..Default::default()
    })
    .await
    .unwrap();
    core.db.write(|tx| {
        tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('a','douyin','MS4synthetic','t')",[])?;
        for (id,kind) in [("first","dynamic"),("second","video")] {
            tx.execute("INSERT INTO subscriptions(subscription_id,account_id,content_type,range_json,created_at,updated_at) VALUES(?1,'a',?2,'{}','t','t')",rusqlite::params![id,kind])?;
        }
        Ok(())
    }).await.unwrap();
    for id in ["first", "second"] {
        jobs::enqueue(
            &core.db,
            format!("scan-review-{id}"),
            "scan",
            id,
            json!({"subscription_id":id,"mode":"incremental"}),
            1,
            1,
            0,
        )
        .await
        .unwrap();
    }
    let claimed = jobs::claim_due(&core.db, "a-review", 10).await.unwrap();
    let first = claimed.iter().find(|j| j.subject_id == "first").unwrap();
    let second = claimed.iter().find(|j| j.subject_id == "second").unwrap();
    let entry = || trace_contracts::plugin::EnumeratedEntry {
        native_entry_key: "71".into(),
        native_kind: "video".into(),
        title: "Synthetic entry".into(),
        published_at: None,
        published_precision: "unknown".into(),
        pinned: false,
        summary_body: Some("Synthetic body".into()),
        stats: json!({}),
    };
    process_scan_page(
        &core,
        first,
        "douyin",
        "first",
        "incremental",
        vec![entry()],
        None,
        &json!({}),
    )
    .await
    .unwrap();
    core.db
        .write(|tx| jobs::set_subscription_enabled(tx, "first", false))
        .await
        .unwrap();
    process_scan_page(
        &core,
        second,
        "douyin",
        "second",
        "incremental",
        vec![entry()],
        None,
        &json!({}),
    )
    .await
    .unwrap();
    let jobs = jobs::claim_due(&core.db, "a-review-next", 10)
        .await
        .unwrap();
    let available = jobs
        .iter()
        .any(|job| job.job_type == "observe" && job.payload["subscription_id"] == "second");
    let state = core
        .db
        .read(|c| {
            Ok(c.query_row(
                "SELECT state,payload FROM jobs WHERE job_type='observe'",
                [],
                |r| Ok((r.get::<_, String>(0)?, r.get::<_, String>(1)?)),
            )?)
        })
        .await
        .unwrap();
    core.shutdown().await;
    assert!(
        available,
        "enabled video subscription must continue observation after dynamic subscription pauses; actual {state:?}"
    );
}
