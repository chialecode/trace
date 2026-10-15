#[cfg(test)]
mod tests {
    use serde_json::json;
    use std::sync::Arc;
    use trace_contracts::acquisition::{
        AcquisitionContext, AuthenticationState as Auth, UpgradeAction, UpgradeDecision,
    };
    use trace_core::{
        acquisitions,
        config::Profile,
        core::Core,
        deletion,
        jobs::{self, ClaimedJob},
        objects::{CommitInfo, ReceiveParams},
        quality, upgrades,
    };
    use trace_domain::ids::OperationId;

    async fn fixture() -> (tempfile::TempDir, Arc<Core>) {
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
            tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('account','douyin','MS4-synthetic-review','t')", [])?;
            tx.execute("INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES('entry','douyin','video','71','account','t')", [])?;
            tx.execute("INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at) VALUES('revision','entry','digest','Review fixture','Synthetic body','t')", [])?;
            tx.execute("INSERT INTO media_items(media_id,revision_id,role,position,source_attachment_id,group_key,required) VALUES('video','revision','video',0,'video','group',1)", [])?;
            for id in ["first", "second"] {
                tx.execute("INSERT INTO credentials_state(credential_id,platform,vault_ref,status,created_at,updated_at) VALUES(?1,'douyin','synthetic-unused-vault-ref','valid','t','t')", [id])?;
                tx.execute("INSERT INTO subscriptions(subscription_id,account_id,content_type,range_json,credential_id,created_at,updated_at) VALUES(?1,'account',?2,'{}',?1,'t','t')", rusqlite::params![id, if id=="first" {"dynamic"} else {"video"}])?;
            }
            Ok(())
        }).await.unwrap();
        (dir, core)
    }
    async fn claim(core: &Core) -> ClaimedJob {
        let job = jobs::claim_due(&core.db, "a-review", 1)
            .await
            .unwrap()
            .remove(0);
        job.mark_running(&core.db).await.unwrap();
        job
    }
    async fn context(core: &Core, job: &mut ClaimedJob, auth: Auth) {
        let c = AcquisitionContext {
            context_id: format!("ctx-{}", uuid::Uuid::new_v4()),
            auth_state: auth,
            credential_id: job.payload["execution_scope"]["credential_id"]
                .as_str()
                .map(str::to_owned),
            credential_generation: job.payload["execution_scope"]["credential_generation"].as_i64(),
            validated_at: Some(jobs::now()),
            basis: "isolated_semantic_review_fixture".into(),
            started_at: jobs::now(),
        };
        let saved = c.clone();
        let guard = job.clone();
        core.db
            .write(move |tx| {
                guard.check(tx)?;
                acquisitions::insert_context(tx, &guard, "entry", &saved)
            })
            .await
            .unwrap();
        job.payload["acquisition_context_id"] = json!(c.context_id);
    }
    async fn object(core: &Core, job: &ClaimedJob, high: bool) {
        let bytes = if high {
            b"high synthetic verified content".as_slice()
        } else {
            b"low synthetic verified content".as_slice()
        };
        candidate_object(
            core,
            job,
            if high { "high" } else { "low" },
            bytes,
            if high { 1920 } else { 640 },
            if high { 1080 } else { 360 },
        )
        .await;
    }
    async fn candidate_object(
        core: &Core,
        job: &ClaimedJob,
        representation: &str,
        bytes: &[u8],
        width: i64,
        height: i64,
    ) {
        let op = OperationId::new(format!("a-review-{}", uuid::Uuid::new_v4()));
        let mut writer = core
            .objects
            .start_receiving(
                &core.db,
                ReceiveParams {
                    job_id: Some(job.job_id.clone()),
                    subject_kind: "media_variant".into(),
                    subject_id: "video".into(),
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
        writer.write_chunk(&core.db, &op, bytes).await.unwrap();
        let (_, sha) = core
            .objects
            .finish_and_validate(&core.db, &op, writer)
            .await
            .unwrap();
        core.objects
            .install_object(&core.db, &op, &sha)
            .await
            .unwrap();
        core.objects.commit(&core.db,&op,&CommitInfo {job:Some(job.clone()),media_id:"video".into(),source_representation:representation.into(),claimed_spec:json!({})},
            Some(json!({"width":width,"height":height,"fps":30.0,"duration_ms":1000,"codec":"h264","audio_tracks":1,"audio_channels":2}))).await.unwrap();
    }
    async fn old_resource(core: &Core) {
        jobs::enqueue(
            &core.db,
            "old-resource".into(),
            "media",
            "entry",
            json!({"entry_id":"entry","revision_id":"revision","group":"group"}),
            0,
            1,
            0,
        )
        .await
        .unwrap();
        let mut job = claim(core).await;
        context(core, &mut job, Auth::Anonymous).await;
        object(core, &job, false).await;
        job.succeed(&core.db).await.unwrap();
    }
    async fn selected(core: &Core) -> (String, String) {
        core.db.read(|c| Ok(c.query_row("SELECT v.source_representation,v.approval_state FROM asset_selections s JOIN media_variants v ON v.variant_id=s.variant_id WHERE s.media_id='video'",[],|r|Ok((r.get(0)?,r.get(1)?)))?)).await.unwrap()
    }

    #[tokio::test]
    async fn a_completed_shared_consumer_keeps_its_downloaded_upgrade_available() {
        let (_dir, core) = fixture().await;
        old_resource(&core).await;
        core.db.write(|tx| {
            for sub in ["first","second"] {
                jobs::consumers::discover(tx,"entry",sub)?;
                jobs::consumers::request(tx,"media:revision:group","media","entry",json!({"entry_id":"entry","revision_id":"revision","group":"group","subscription_id":sub}),sub)?;
            }
            Ok(())
        }).await.unwrap();
        let mut first = claim(&core).await;
        assert_eq!(first.payload["subscription_id"], "first");
        context(&core, &mut first, Auth::Authenticated).await;
        let candidate = upgrades::prepare(&core, &first, "revision", "group")
            .await
            .unwrap()
            .unwrap();
        first.payload["upgrade_candidate_id"] = json!(candidate);
        object(&core, &first, true).await;
        upgrades::finish(&core, &first).await.unwrap();
        let before = upgrades::list(&core, json!({"state":"pending"}))
            .await
            .unwrap();
        assert_eq!(before["total"], 1);
        first.succeed(&core.db).await.unwrap();
        let after = upgrades::list(&core, json!({})).await.unwrap();
        let state = after["items"][0]["state"].clone();
        let successor = claim(&core).await;
        assert_eq!(successor.payload["subscription_id"], "second");
        assert_eq!(selected(&core).await.0, "low");
        core.shutdown().await;
        assert_eq!(
            state, "pending",
            "a different enabled consumer merely becoming runnable invalidated the first consumer's fully downloaded upgrade before any resource, credential, subscription, or old selection changed: {after}"
        );
    }

    #[tokio::test]
    async fn c14_shared_candidate_comparison_keeps_originating_quality_preference() {
        for legacy_token in [false, true] {
            let (_dir, core) = fixture().await;
            old_resource(&core).await;
            core.db.write(|tx| {
                tx.execute("UPDATE media_variants SET measured_spec=json_set(measured_spec,'$.width',3840,'$.height',2160,'$.fps',30) WHERE source_representation='low'",[])?;
                tx.execute("UPDATE subscriptions SET quality_json='{\"prefer_fps\":true}' WHERE subscription_id='first'",[])?;
                for sub in ["first","second"] {
                    jobs::consumers::discover(tx,"entry",sub)?;
                    jobs::consumers::request(tx,"shared","media","entry",json!({"entry_id":"entry","revision_id":"revision","group":"group","subscription_id":sub}),sub)?;
                }
                Ok(())
            }).await.unwrap();
            let mut first = claim(&core).await;
            context(&core, &mut first, Auth::Authenticated).await;
            let id = upgrades::prepare(&core, &first, "revision", "group")
                .await
                .unwrap()
                .unwrap();
            first.payload["upgrade_candidate_id"] = json!(id);
            object(&core, &first, true).await;
            core.db.write(|tx| {
                tx.execute("UPDATE media_variants SET measured_spec=json_set(measured_spec,'$.fps',60) WHERE source_representation='high'",[])?;
                Ok(())
            }).await.unwrap();
            upgrades::finish(&core, &first).await.unwrap();
            if legacy_token {
                core.db.write(|tx| {
                    tx.execute("UPDATE upgrade_candidates SET confirmation=json_remove(confirmation,'$.prefer_fps')",[])?;
                    Ok(())
                }).await.unwrap();
            }
            let page = upgrades::list(&core, json!({"state":"pending"}))
                .await
                .unwrap();
            let expected = page["items"][0]["expected"].clone();
            assert_eq!(page["items"][0]["resources"][0]["comparison"], "higher");
            first.succeed(&core.db).await.unwrap();
            let next = claim(&core).await;
            assert_eq!(next.payload["subscription_id"], "second");
            assert_ne!(
                next.payload["execution_scope"]["quality"]["prefer_fps"],
                true
            );
            let after = upgrades::resources(&core, json!({"candidate_id":id}))
                .await
                .unwrap();
            assert_eq!(after["items"][0]["comparison"], "higher");
            upgrades::decide(
                &core,
                vec![UpgradeDecision {
                    candidate_id: id.clone(),
                    decision: UpgradeAction::Accept,
                    expected,
                }],
            )
            .await
            .unwrap();
            assert_eq!(selected(&core).await.0, "high");
            core.db.write(|tx| {
                tx.execute("UPDATE subscriptions SET quality_json='{}',config_version=config_version+1 WHERE subscription_id='first'",[])?;
                Ok(())
            }).await.unwrap();
            let history = upgrades::resources(&core, json!({"candidate_id":id}))
                .await
                .unwrap();
            assert_eq!(
                history["items"][0]["comparison"],
                if legacy_token { "unknown" } else { "higher" }
            );
            core.shutdown().await;
        }
    }

    #[tokio::test]
    async fn a_delete_restore_does_not_promote_a_rejected_upgrade_to_selection() {
        let (_dir, core) = fixture().await;
        old_resource(&core).await;
        jobs::enqueue(&core.db,"upgrade-request".into(),"media","entry",json!({"entry_id":"entry","revision_id":"revision","group":"group","subscription_id":"first"}),0,1,0).await.unwrap();
        let mut job = claim(&core).await;
        context(&core, &mut job, Auth::Authenticated).await;
        let candidate = upgrades::prepare(&core, &job, "revision", "group")
            .await
            .unwrap()
            .unwrap();
        job.payload["upgrade_candidate_id"] = json!(candidate);
        object(&core, &job, true).await;
        upgrades::finish(&core, &job).await.unwrap();
        let page = upgrades::list(&core, json!({"state":"pending"}))
            .await
            .unwrap();
        let expected = page["items"][0]["expected"].clone();
        upgrades::decide(
            &core,
            vec![UpgradeDecision {
                candidate_id: candidate,
                decision: UpgradeAction::Reject,
                expected,
            }],
        )
        .await
        .unwrap();
        job.succeed(&core.db).await.unwrap();
        assert_eq!(selected(&core).await.0, "low");
        let preview = deletion::preview(&core, "entry".into(), "copies".into())
            .await
            .unwrap();
        let deleted = deletion::apply(
            &core,
            "entry".into(),
            "copies".into(),
            preview["token"].as_str().unwrap().into(),
        )
        .await
        .unwrap();
        deletion::restore(
            &core,
            "entry".into(),
            deleted["generation"].as_i64().unwrap(),
        )
        .await
        .unwrap();
        core.db
            .write(|tx| quality::evaluate(tx, "revision", false, 86400))
            .await
            .unwrap();
        let actual = selected(&core).await;
        core.shutdown().await;
        assert_eq!(
            actual,
            ("low".into(), "automatic".into()),
            "restore and subsequent quality maintenance must preserve the explicit rejection; actual {actual:?}"
        );
    }

    #[tokio::test]
    async fn a_prepared_bytes_from_cancelled_job_are_not_ready_for_new_completion() {
        let (_dir, core) = fixture().await;
        let payload = |generation| json!({"entry_id":"entry","revision_id":"revision","group":"group","subscription_id":"first","completion":true,"credential_id":"first","credential_generation":generation});
        jobs::enqueue(
            &core.db,
            "completion-media:old".into(),
            "media",
            "entry",
            payload(1),
            0,
            1,
            0,
        )
        .await
        .unwrap();
        let old = claim(&core).await;
        let bytes = b"prepared synthetic media";
        // This is the production operation identity for two completion jobs
        // with the same media, task generation and unchanged source candidate.
        let identity =
            json!({"representation":"source","spec":{},"size":bytes.len(),"sha256":null});
        let op = OperationId::new(format!(
            "mz-video-{}-{}",
            old.generation,
            trace_core::config::short_hash(&identity.to_string())
        ));
        let mut writer = core
            .objects
            .start_receiving(
                &core.db,
                ReceiveParams {
                    job_id: Some(old.job_id.clone()),
                    subject_kind: "media_variant".into(),
                    subject_id: "video".into(),
                    generation: old.generation,
                    mime: Some("application/octet-stream".into()),
                    expected_size: Some(bytes.len() as i64),
                    expected_sha256: None,
                    source_validator: None,
                },
                &op,
            )
            .await
            .unwrap();
        writer.write_chunk(&core.db, &op, bytes).await.unwrap();
        let (_, sha) = core
            .objects
            .finish_and_validate(&core.db, &op, writer)
            .await
            .unwrap();
        core.objects
            .install_object(&core.db, &op, &sha)
            .await
            .unwrap();
        assert!(old.cancel(&core.db).await.unwrap());
        // Model completion scheduled after a newly validated credential generation.
        core.db.write(|tx|{tx.execute("UPDATE credentials_state SET generation=2,status='valid' WHERE credential_id='first'",[])?;Ok(())}).await.unwrap();
        jobs::enqueue(
            &core.db,
            "completion-media:new".into(),
            "media",
            "entry",
            payload(2),
            0,
            1,
            0,
        )
        .await
        .unwrap();
        let fresh = claim(&core).await;
        assert_ne!(old.job_id, fresh.job_id);
        assert_eq!(old.generation, fresh.generation);
        let ready = core.objects.ready(&core.db, &op, &fresh).await.unwrap();
        let commit = core
            .objects
            .commit(
                &core.db,
                &op,
                &CommitInfo {
                    job: Some(fresh.clone()),
                    media_id: "video".into(),
                    source_representation: "source".into(),
                    claimed_spec: json!({}),
                },
                None,
            )
            .await;
        let failure = if let Err(error) = commit {
            fresh
                .fail(
                    &core.db,
                    &error.failure_class(),
                    "isolated completion failure",
                    None,
                )
                .await
                .unwrap();
            Some(format!("{error:?}"))
        } else {
            None
        };
        let id = fresh.job_id.clone();
        let terminal = core
            .db
            .read(move |c| {
                Ok(
                    c.query_row("SELECT state FROM jobs WHERE job_id=?1", [id], |r| {
                        r.get::<_, String>(0)
                    })?,
                )
            })
            .await
            .unwrap();
        core.shutdown().await;
        assert!(
            !ready,
            "another job's OBJECT_READY bytes were offered to a new credential completion; commit returns {failure:?}, and normal failure handling leaves the new request {terminal}, preventing a fresh transfer"
        );
    }

    #[tokio::test]
    async fn c14_completed_candidate_survives_successor_same_lower_failure_and_new_offer() {
        for outcome in ["same", "lower", "no_improvement", "failure", "higher"] {
            let (_dir, core) = fixture().await;
            old_resource(&core).await;
            core.db.write(|tx|{for sub in ["first","second"]{jobs::consumers::discover(tx,"entry",sub)?;jobs::consumers::request(tx,"shared","media","entry",json!({"entry_id":"entry","revision_id":"revision","group":"group","subscription_id":sub}),sub)?;}Ok(())}).await.unwrap();
            let mut first = claim(&core).await;
            context(&core, &mut first, Auth::Authenticated).await;
            let first_id = upgrades::prepare(&core, &first, "revision", "group")
                .await
                .unwrap()
                .unwrap();
            first.payload["upgrade_candidate_id"] = json!(first_id);
            object(&core, &first, true).await;
            upgrades::finish(&core, &first).await.unwrap();
            first.succeed(&core.db).await.unwrap();
            let mut next = claim(&core).await;
            context(&core, &mut next, Auth::Authenticated).await;
            if outcome == "failure" {
                next.fail(
                    &core.db,
                    &trace_domain::errors::FailureClass::TransientNetwork,
                    "fixture network failure",
                    None,
                )
                .await
                .unwrap();
            } else {
                let id = upgrades::prepare(&core, &next, "revision", "group")
                    .await
                    .unwrap()
                    .unwrap();
                next.payload["upgrade_candidate_id"] = json!(id);
                if outcome == "higher" {
                    candidate_object(
                        &core,
                        &next,
                        "higher",
                        b"distinct synthetic 4K candidate",
                        3840,
                        2160,
                    )
                    .await;
                } else if outcome == "lower" {
                    candidate_object(
                        &core,
                        &next,
                        "lower",
                        b"distinct synthetic lower candidate",
                        320,
                        180,
                    )
                    .await;
                } else {
                    object(&core, &next, outcome == "same").await;
                }
                upgrades::finish(&core, &next).await.unwrap();
                next.succeed(&core.db).await.unwrap();
                let state = core
                    .db
                    .read(move |c| {
                        Ok(c.query_row(
                            "SELECT state FROM upgrade_candidates WHERE candidate_id=?1",
                            [id],
                            |r| r.get::<_, String>(0),
                        )?)
                    })
                    .await
                    .unwrap();
                assert_eq!(
                    state,
                    if matches!(outcome, "lower" | "no_improvement") {
                        "no_improvement"
                    } else {
                        "pending"
                    }
                );
            }
            let list = upgrades::list(&core, json!({"state":"pending"}))
                .await
                .unwrap();
            let original = list["items"]
                .as_array()
                .unwrap()
                .iter()
                .find(|v| v["candidate_id"] == first_id)
                .expect("completed first consumer candidate survives successor");
            assert_eq!(selected(&core).await.0, "low");
            let decision = UpgradeDecision {
                candidate_id: first_id,
                decision: UpgradeAction::Accept,
                expected: original["expected"].clone(),
            };
            upgrades::decide(&core, vec![decision]).await.unwrap();
            assert_eq!(selected(&core).await.0, "high");
            core.shutdown().await;
        }
    }
    #[tokio::test]
    async fn c14_restore_copies_archive_pending_rejected_pinned_missing_and_recycled() {
        use rusqlite::OptionalExtension;
        for scope in ["copies", "archive"] {
            for has_old in [false, true] {
                for rejected in [false, true] {
                    let (_dir, core) = fixture().await;
                    if has_old {
                        old_resource(&core).await;
                    }
                    jobs::enqueue(&core.db,"candidate".into(),"media","entry",json!({"entry_id":"entry","revision_id":"revision","group":"group","subscription_id":"first","completion":true}),0,1,0).await.unwrap();
                    let mut job = claim(&core).await;
                    context(&core, &mut job, Auth::Authenticated).await;
                    let id = upgrades::prepare(&core, &job, "revision", "group")
                        .await
                        .unwrap()
                        .unwrap();
                    job.payload["upgrade_candidate_id"] = json!(id);
                    object(&core, &job, true).await;
                    upgrades::finish(&core, &job).await.unwrap();
                    if rejected {
                        let page = upgrades::list(&core, json!({"state":"pending"}))
                            .await
                            .unwrap();
                        upgrades::decide(
                            &core,
                            vec![UpgradeDecision {
                                candidate_id: id,
                                decision: UpgradeAction::Reject,
                                expected: page["items"][0]["expected"].clone(),
                            }],
                        )
                        .await
                        .unwrap();
                    }
                    job.succeed(&core.db).await.unwrap();
                    if has_old && rejected {
                        core.db
                            .write(|tx| {
                                tx.execute(
                                    "UPDATE asset_selections SET pinned=1 WHERE media_id='video'",
                                    [],
                                )?;
                                Ok(())
                            })
                            .await
                            .unwrap();
                    }
                    let p = deletion::preview(&core, "entry".into(), scope.into())
                        .await
                        .unwrap();
                    let d = deletion::apply(
                        &core,
                        "entry".into(),
                        scope.into(),
                        p["token"].as_str().unwrap().into(),
                    )
                    .await
                    .unwrap();
                    deletion::restore(&core, "entry".into(), d["generation"].as_i64().unwrap())
                        .await
                        .unwrap();
                    core.db
                        .write(|tx| quality::evaluate(tx, "revision", false, 86400))
                        .await
                        .unwrap();
                    let actual=core.db.read(|c|Ok(c.query_row("SELECT v.source_representation FROM asset_selections s JOIN media_variants v ON v.variant_id=s.variant_id WHERE s.media_id='video'",[],|r|r.get::<_,String>(0)).optional()?)).await.unwrap();
                    assert_eq!(actual, has_old.then(|| "low".to_owned()));
                    core.db.read(|c|{assert_eq!(c.query_row("SELECT count(*) FROM media_variants WHERE approval_state IN ('pending','rejected')",[],|r|r.get::<_,i64>(0))?,1);Ok(())}).await.unwrap();
                    core.shutdown().await;
                }
            }
        }
        let (_dir, core) = fixture().await;
        old_resource(&core).await;
        let p = deletion::preview(&core, "entry".into(), "copies".into())
            .await
            .unwrap();
        let d = deletion::apply(
            &core,
            "entry".into(),
            "copies".into(),
            p["token"].as_str().unwrap().into(),
        )
        .await
        .unwrap();
        core.db
            .write(|tx| {
                tx.execute("UPDATE replicas SET state='recycled'", [])?;
                Ok(())
            })
            .await
            .unwrap();
        deletion::restore(&core, "entry".into(), d["generation"].as_i64().unwrap())
            .await
            .unwrap();
        assert_eq!(
            core.db
                .read(|c| Ok(
                    c.query_row("SELECT count(*) FROM asset_selections", [], |r| r
                        .get::<_, i64>(0))?
                ))
                .await
                .unwrap(),
            0
        );
        core.shutdown().await;
    }
    #[tokio::test]
    async fn c14_materialization_owner_checked_at_receiving_prepared_ready_and_committed() {
        for stage in ["receiving", "prepared", "ready", "committed"] {
            let (_dir, core) = fixture().await;
            jobs::enqueue(
                &core.db,
                "old".into(),
                "media",
                "entry",
                json!({"entry_id":"entry"}),
                0,
                1,
                0,
            )
            .await
            .unwrap();
            let old = claim(&core).await;
            let op = OperationId::new("explicit-collision");
            let params = ReceiveParams {
                job_id: Some(old.job_id.clone()),
                subject_kind: "media_variant".into(),
                subject_id: "video".into(),
                generation: old.generation,
                mime: None,
                expected_size: Some(4),
                expected_sha256: None,
                source_validator: None,
            };
            let mut writer = core
                .objects
                .start_receiving(&core.db, params.clone(), &op)
                .await
                .unwrap();
            writer.write_chunk(&core.db, &op, b"data").await.unwrap();
            if stage != "receiving" {
                let (_, sha) = core
                    .objects
                    .finish_and_validate(&core.db, &op, writer)
                    .await
                    .unwrap();
                if stage != "prepared" {
                    core.objects
                        .install_object(&core.db, &op, &sha)
                        .await
                        .unwrap();
                    if stage == "committed" {
                        core.objects
                            .commit(
                                &core.db,
                                &op,
                                &CommitInfo {
                                    job: Some(old.clone()),
                                    media_id: "video".into(),
                                    source_representation: "source".into(),
                                    claimed_spec: json!({}),
                                },
                                None,
                            )
                            .await
                            .unwrap();
                    }
                }
            }
            old.cancel(&core.db).await.unwrap();
            jobs::enqueue(
                &core.db,
                "fresh".into(),
                "media",
                "entry",
                json!({"entry_id":"entry"}),
                0,
                1,
                0,
            )
            .await
            .unwrap();
            let fresh = claim(&core).await;
            assert!(!core.objects.ready(&core.db, &op, &fresh).await.unwrap());
            let mut other = params;
            other.job_id = Some(fresh.job_id.clone());
            assert!(
                core.objects
                    .start_receiving(&core.db, other, &op)
                    .await
                    .is_err()
            );
            assert!(
                core.objects
                    .commit(
                        &core.db,
                        &op,
                        &CommitInfo {
                            job: Some(fresh.clone()),
                            media_id: "video".into(),
                            source_representation: "source".into(),
                            claimed_spec: json!({})
                        },
                        None
                    )
                    .await
                    .is_err()
            );
            core.db.read(move|c|{assert_eq!(c.query_row("SELECT job_id FROM materializations WHERE operation_id='explicit-collision'",[],|r|r.get::<_,String>(0))?,old.job_id);Ok(())}).await.unwrap();
            core.shutdown().await;
        }
    }
}
