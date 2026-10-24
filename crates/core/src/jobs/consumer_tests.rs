use super::*;
use crate::db::{DbActor, DbHandle};
use crate::store;
use serde_json::json;

async fn fixture(kind: &str) -> (DbHandle, DbActor, tempfile::TempDir) {
    let dir = tempfile::tempdir().unwrap();
    let (db, actor) =
        DbActor::start(Connection::open(dir.path().join("archive.db")).unwrap(), 64).unwrap();
    db.write(|tx| {
        tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('a','douyin','MS4fixture','t')",[])?;
        tx.execute("INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES('e','douyin','video','1','a','t')",[])?;
        tx.execute("INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at) VALUES('r','e','d','test','body','t')",[])?;
        for (id,content,height) in [("first","dynamic",720),("second","video",1080)] {
            tx.execute("INSERT INTO subscriptions(subscription_id,account_id,content_type,range_json,quality_json,created_at,updated_at) VALUES(?1,'a',?2,'{}',?3,'t','t')",rusqlite::params![id,content,json!({"strict":true,"min_height":height}).to_string()])?;
        }
        Ok(())
    }).await.unwrap();
    add(&db, kind, "first").await;
    (db, actor, dir)
}
async fn add(db: &DbHandle, kind: &str, sub: &str) {
    let (kind, sub) = (kind.to_owned(), sub.to_owned());
    db.write(move |tx| {
        discover(tx, "e", &sub)?;
        request(
            tx,
            &format!("{kind}:e"),
            &kind,
            "e",
            json!({"entry_id":"e","revision_id":"r","group":"g","subscription_id":sub}),
            &sub,
        )?;
        Ok(())
    })
    .await
    .unwrap();
}
async fn claim(db: &DbHandle) -> ClaimedJob {
    let job = jobs::claim_due(db, "consumers-test", 1)
        .await
        .unwrap()
        .remove(0);
    job.mark_running(db).await.unwrap();
    job
}

#[tokio::test]
async fn shared_observe_and_media_cover_all_owner_states_and_policies() {
    for kind in ["observe", "media"] {
        for state in ["queued", "running", "paused", "succeeded"] {
            let (db, _actor, _dir) = fixture(kind).await;
            let first = if matches!(state, "running" | "succeeded") {
                Some(claim(&db).await)
            } else {
                None
            };
            if state == "succeeded" {
                first.as_ref().unwrap().succeed(&db).await.unwrap();
            }
            if state == "paused" {
                db.write(|tx| jobs::set_subscription_enabled(tx, "first", false))
                    .await
                    .unwrap();
            }
            add(&db, kind, "second").await;
            if let Some(first) = &first
                && state == "running"
            {
                assert!(
                    first.heartbeat(&db).await.is_ok(),
                    "new consumer must not steal a valid lease"
                );
                assert_eq!(
                    first.payload["execution_scope"]["quality"]["min_height"],
                    720
                );
                first.succeed(&db).await.unwrap();
            } else if state == "queued" {
                let first = claim(&db).await;
                assert_eq!(first.payload["subscription_id"], "first");
                first.succeed(&db).await.unwrap();
            }
            let second = claim(&db).await;
            assert_eq!(
                second.payload["subscription_id"], "second",
                "{kind}/{state}"
            );
            assert_eq!(
                second.payload["execution_scope"]["quality"]["min_height"],
                1080
            );
            second.succeed(&db).await.unwrap();
            db.read(|c| {
                assert_eq!(
                    c.query_row("SELECT count(*) FROM entries", [], |r| r.get::<_, i64>(0))?,
                    1
                );
                assert_eq!(
                    c.query_row(
                        "SELECT count(*) FROM jobs WHERE job_type IN ('observe','media')",
                        [],
                        |r| r.get::<_, i64>(0)
                    )?,
                    1
                );
                assert_eq!(
                    c.query_row("SELECT count(*) FROM entry_subscriptions", [], |r| r
                        .get::<_, i64>(0))?,
                    2
                );
                Ok(())
            })
            .await
            .unwrap();
        }
    }
}

#[tokio::test]
async fn shared_control_revocation_restart_and_cancel_do_not_spend_failures() {
    for kind in ["observe", "media"] {
        for action in ["pause_resume", "cancel", "module", "delete", "restart"] {
            let (db, _actor, _dir) = fixture(kind).await;
            add(&db, kind, "second").await;
            let old = claim(&db).await;
            let action_owned = action.to_owned();
            let id = old.job_id.clone();
            db.write(move|tx|{
                match action_owned.as_str() {
                    "pause_resume"=>{jobs::set_subscription_enabled(tx,"first",false)?;jobs::set_subscription_enabled(tx,"first",true)?;},
                    "cancel"=>{tx.execute("UPDATE jobs SET state='cancelled',target_generation=target_generation+1,lease_owner=NULL WHERE job_id=?1",[id])?;},
                    "module"=>{tx.execute("INSERT INTO plugins_state(plugin_id,platform,version,protocol,binary_path,enabled,updated_at) VALUES('douyin','douyin','1','1','none',0,'t')",[])?;tx.execute("UPDATE jobs SET state='waiting_plugin',target_generation=target_generation+1,lease_owner=NULL",[])?;},
                    "delete"=>{tx.execute("UPDATE entries SET lifecycle='deleted',deletion_generation=deletion_generation+1",[])?;},
                    "restart"=>{},
                    _=>unreachable!()
                }
                Ok(())
            }).await.unwrap();
            if action == "restart" {
                jobs::recover_stale_leases(&db, "replacement-instance")
                    .await
                    .unwrap();
            }
            assert!(
                old.heartbeat(&db).await.is_err(),
                "{kind}/{action}: old lease"
            );
            assert!(
                old.fail(
                    &db,
                    &trace_domain::errors::FailureClass::TransientNetwork,
                    "late",
                    None
                )
                .await
                .is_err()
            );
            add(&db, kind, "second").await;
            let due = jobs::claim_due(&db, "next", 10).await.unwrap();
            if matches!(action, "cancel" | "module" | "delete") {
                assert!(due.iter().all(|j| j.job_type != kind), "{kind}/{action}");
            } else {
                assert!(due.iter().any(|j| j.job_type == kind));
            }
            db.read(|c| {
                assert_eq!(
                    c.query_row("SELECT sum(attempts) FROM job_consumers", [], |r| r
                        .get::<_, i64>(0))?,
                    0
                );
                assert_eq!(
                    c.query_row("SELECT count(*) FROM attempts", [], |r| r.get::<_, i64>(0))?,
                    0
                );
                Ok(())
            })
            .await
            .unwrap();
        }
    }
}

#[tokio::test]
async fn site_refusal_is_scoped_to_one_consumer_and_needs_explicit_retry() {
    let (db, _actor, _dir) = fixture("observe").await;
    db.write(|tx| {
        for id in ["one", "two"] {
            tx.execute("INSERT INTO credentials_state(credential_id,platform,subject,vault_ref,status,capability,created_at,updated_at) VALUES(?1,'douyin','test',?1,'valid','{}','t','t')", [id])?;
        }
        tx.execute("UPDATE subscriptions SET credential_id=CASE subscription_id WHEN 'first' THEN 'one' ELSE 'two' END", [])?;
        Ok(())
    }).await.unwrap();
    add(&db, "observe", "second").await;
    let first = claim(&db).await;
    assert_eq!(first.payload["subscription_id"], "first");
    first
        .fail(
            &db,
            &trace_domain::errors::FailureClass::PermissionDenied,
            "signature refused",
            None,
        )
        .await
        .unwrap();
    db.write(|tx| crate::upgrades::schedule(tx, "one", 1))
        .await
        .unwrap();
    let scheduled_before_reprobe: i64 = db
        .read(|c| {
            Ok(c.query_row(
                "SELECT count(*) FROM jobs WHERE job_type='auth_completion'",
                [],
                |r| r.get(0),
            )?)
        })
        .await
        .unwrap();
    assert_eq!(
        scheduled_before_reprobe, 0,
        "a valid-only probe path is blocked before any credential change"
    );
    db.write(|tx| {
        credential_changed(tx, "one", false)?;
        credential_changed(tx, "one", true)?;
        crate::upgrades::schedule(tx, "one", 1)
    })
    .await
    .unwrap();
    db.read(|c| {
        assert_eq!(
            c.query_row(
                "SELECT state FROM job_consumers WHERE subscription_id='first'",
                [],
                |r| r.get::<_, String>(0)
            )?,
            "waiting_user"
        );
        assert_eq!(
            c.query_row(
                "SELECT count(*) FROM jobs WHERE job_type='auth_completion'",
                [],
                |r| r.get::<_, i64>(0)
            )?,
            0,
            "a valid-only probe cannot schedule completion for a refused owner"
        );
        Ok(())
    })
    .await
    .unwrap();
    let second = claim(&db).await;
    assert_eq!(
        second.payload["subscription_id"], "second",
        "the other consumer must not starve"
    );
    second.succeed(&db).await.unwrap();
    jobs::recover_stale_leases(&db, "restart-after-refusal")
        .await
        .unwrap();
    assert!(
        jobs::claim_due(&db, "restart-after-refusal", 10)
            .await
            .unwrap()
            .is_empty(),
        "restart must not revive the refused consumer"
    );
    let retry = first.job_id.clone();
    db.write(move |tx| {
        store::jobs::retry(tx, &retry, &jobs::now())?;
        super::retry(tx, &retry)
    })
    .await
    .unwrap();
    let mut retried = Vec::new();
    for _ in 0..2 {
        let job = claim(&db).await;
        retried.push(job.payload["subscription_id"].as_str().unwrap().to_owned());
        job.succeed(&db).await.unwrap();
    }
    retried.sort();
    assert_eq!(retried, ["first", "second"]);
}

#[tokio::test]
async fn site_refusal_stops_all_scan_lanes_until_an_explicit_retry() {
    let (db, _actor, _dir) = fixture("observe").await;
    db.write(|tx| {
        tx.execute("DELETE FROM job_consumers", [])?;
        tx.execute("DELETE FROM jobs", [])?;
        for lane in [
            trace_domain::jobs::ScanMode::Incremental,
            trace_domain::jobs::ScanMode::History,
            trace_domain::jobs::ScanMode::Deep,
        ] {
            crate::store::subscriptions::insert_scan_lane(tx, "first", lane, &jobs::now())?;
        }
        tx.execute("UPDATE jobs SET next_run_at=NULL", [])?;
        Ok(())
    })
    .await
    .unwrap();
    let first = claim(&db).await;
    first
        .fail(
            &db,
            &trace_domain::errors::FailureClass::PermissionDenied,
            "signature refused",
            None,
        )
        .await
        .unwrap();
    db.read(|c| {
        assert_eq!(
            c.query_row(
                "SELECT count(*) FROM jobs WHERE state='waiting_user'",
                [],
                |r| r.get::<_, i64>(0)
            )?,
            3
        );
        Ok(())
    })
    .await
    .unwrap();
    jobs::recover_stale_leases(&db, "restart-after-scan-refusal")
        .await
        .unwrap();
    assert!(
        jobs::claim_due(&db, "restart-after-scan-refusal", 10)
            .await
            .unwrap()
            .is_empty()
    );
    let retry = first.job_id.clone();
    db.write(move |tx| store::jobs::retry(tx, &retry, &jobs::now()))
        .await
        .unwrap();
    let due = jobs::claim_due(&db, "explicit-retry", 10).await.unwrap();
    assert_eq!(
        due.iter()
            .filter(|job| job.payload["subscription_id"] == "first")
            .count(),
        1,
        "only the explicitly retried lane is unlocked"
    );
    assert!(
        jobs::claim_due(&db, "explicit-retry-again", 10)
            .await
            .unwrap()
            .is_empty()
    );
}

#[tokio::test]
async fn range_and_quality_changes_do_not_reopen_a_refused_subscription() {
    let (db, _actor, _dir) = fixture("observe").await;
    db.write(|tx| {
        tx.execute("DELETE FROM job_consumers", [])?;
        tx.execute("DELETE FROM jobs", [])?;
        for lane in [
            trace_domain::jobs::ScanMode::Incremental,
            trace_domain::jobs::ScanMode::History,
            trace_domain::jobs::ScanMode::Deep,
        ] {
            crate::store::subscriptions::insert_scan_lane(tx, "first", lane, &jobs::now())?;
        }
        tx.execute("UPDATE jobs SET next_run_at=NULL", [])?;
        Ok(())
    })
    .await
    .unwrap();
    let denied = claim(&db).await;
    denied
        .fail(
            &db,
            &trace_domain::errors::FailureClass::PermissionDenied,
            "signature refused",
            None,
        )
        .await
        .unwrap();
    db.write(|tx| {
        tx.execute(
            "INSERT INTO credentials_state(credential_id,platform,subject,vault_ref,status,capability,created_at,updated_at)
             VALUES('cred','douyin','test','cred','valid','{}','t','t')",
            [],
        )?;
        tx.execute("UPDATE subscriptions SET credential_id='cred' WHERE subscription_id='first'", [])?;
        tx.execute(
            "INSERT INTO jobs(job_id,job_key,job_type,subject_id,payload,priority,state,created_at,updated_at)
             VALUES('media-first','media:first','media','e','{\"subscription_id\":\"first\"}',1,'succeeded','t','t')",
            [],
        )?;
        tx.execute(
            "INSERT INTO jobs(job_id,job_key,job_type,subject_id,payload,priority,state,created_at,updated_at)
             VALUES('auth-wait','auth-wait','observe','e','{\"subscription_id\":\"first\"}',1,'waiting_auth','t','t')",
            [],
        )?;
        tx.execute(
            "INSERT INTO jobs(job_id,job_key,job_type,subject_id,payload,priority,state,created_at,updated_at)
             VALUES('profile-first','profile:first','account_profile','a','{\"subscription_id\":\"first\"}',1,'paused','t','t')",
            [],
        )?;
        tx.execute(
            "INSERT INTO jobs(job_id,job_key,job_type,subject_id,payload,priority,state,last_error,created_at,updated_at)
             VALUES('refusal-marker','refusal-marker','observe','e','{\"subscription_id\":\"first\"}',1,'waiting_user','{\"class\":\"permission_denied\"}','t','t')",
            [],
        )?;
        crate::store::subscriptions::set_range(tx, "first", "{}", &jobs::now())?;
        crate::store::subscriptions::requeue_media_work(tx, "first", &jobs::now())?;
        crate::store::subscriptions::requeue_work_after_credential_change(
            tx,
            "first",
            &jobs::now(),
        )?;
        store::credentials::wake_waiting_auth(tx, "cred", &jobs::now())?;
        credential_changed(tx, "cred", true)?;
        crate::accounts::reconcile_subscription(tx, "first")?;
        tx.execute("DELETE FROM jobs WHERE job_key='scan:first:history'", [])?;
        crate::store::subscriptions::ensure_history_lane(tx, "first", &jobs::now())
    })
    .await
    .unwrap();
    db.write(|tx| {
        tx.execute(
            "UPDATE jobs SET state='cancelled' WHERE job_key='scan:first:incremental'",
            [],
        )?;
        tx.execute("DELETE FROM jobs WHERE job_key='scan:first:deep'", [])?;
        Ok(())
    })
    .await
    .unwrap();
    crate::scan_range::ensure_deep_lanes(&db).await.unwrap();
    db.read(|c| {
        assert_eq!(
            c.query_row(
                "SELECT count(*) FROM jobs WHERE job_type='scan' AND state='waiting_user'",
                [],
                |r| r.get::<_, i64>(0)
            )?,
            0
        );
        assert_eq!(
            c.query_row(
                "SELECT state FROM jobs WHERE job_id='media-first'",
                [],
                |r| r.get::<_, String>(0)
            )?,
            "succeeded"
        );
        assert_eq!(
            c.query_row("SELECT state FROM jobs WHERE job_id='auth-wait'", [], |r| r
                .get::<_, String>(0))?,
            "waiting_auth",
            "a credential wake cannot cross a permission stop on the subscription"
        );
        assert_eq!(
            c.query_row(
                "SELECT state FROM jobs WHERE job_id='profile-first'",
                [],
                |r| r.get::<_, String>(0)
            )?,
            "paused",
            "account profile reconciliation cannot reopen refused work"
        );
        assert_eq!(
            c.query_row(
                "SELECT count(*) FROM jobs WHERE job_key='scan:first:history'",
                [],
                |r| r.get::<_, i64>(0)
            )?,
            0,
            "a refused subscription must not grow a fresh history lane"
        );
        assert_eq!(
            c.query_row(
                "SELECT count(*) FROM jobs WHERE job_key='scan:first:deep'",
                [],
                |r| r.get::<_, i64>(0)
            )?,
            0,
            "startup recovery must not grow a fresh deep lane"
        );
        Ok(())
    })
    .await
    .unwrap();
}

#[tokio::test]
async fn enabling_a_refused_subscription_does_not_create_a_fresh_scan_lane() {
    let (db, _actor, _dir) = fixture("observe").await;
    db.write(|tx| {
        tx.execute("DELETE FROM job_consumers", [])?;
        tx.execute("DELETE FROM jobs", [])?;
        for lane in [
            trace_domain::jobs::ScanMode::History,
            trace_domain::jobs::ScanMode::Deep,
        ] {
            crate::store::subscriptions::insert_scan_lane(tx, "first", lane, &jobs::now())?;
        }
        tx.execute("UPDATE jobs SET next_run_at=NULL", [])?;
        Ok(())
    })
    .await
    .unwrap();
    let denied = claim(&db).await;
    denied
        .fail(
            &db,
            &trace_domain::errors::FailureClass::PermissionDenied,
            "signature refused",
            None,
        )
        .await
        .unwrap();
    db.write(|tx| {
        jobs::set_subscription_enabled(tx, "first", false)?;
        jobs::set_subscription_enabled(tx, "first", true)
    })
    .await
    .unwrap();
    db.read(|c| {
        assert_eq!(
            c.query_row(
                "SELECT count(*) FROM jobs WHERE job_key='scan:first:incremental'",
                [],
                |r| r.get::<_, i64>(0)
            )?,
            0,
            "re-enabling cannot add a queued lane past the refusal"
        );
        assert_eq!(
            c.query_row(
                "SELECT count(*) FROM jobs WHERE job_type='scan' AND state='waiting_user'",
                [],
                |r| r.get::<_, i64>(0)
            )?,
            2
        );
        Ok(())
    })
    .await
    .unwrap();
    assert!(
        jobs::claim_due(&db, "re-enabled", 10)
            .await
            .unwrap()
            .is_empty()
    );
}

#[tokio::test]
async fn permission_refusal_is_only_retried_by_an_explicit_user_action() {
    use trace_domain::errors::FailureClass;
    let (db, _actor, _dir) = fixture("observe").await;
    db.write(|tx| {
        tx.execute("INSERT INTO credentials_state(credential_id,platform,subject,vault_ref,status,capability,created_at,updated_at) VALUES('one','douyin','test','one','valid','{}','t','t')",[])?;
        tx.execute("UPDATE subscriptions SET credential_id='one'",[])?;
        Ok(())
    }).await.unwrap();
    let job = claim(&db).await;
    job.fail(
        &db,
        &FailureClass::PermissionDenied,
        "signature refused with HTTP 461",
        None,
    )
    .await
    .unwrap();
    let job_id = job.job_id.clone();
    db.read(move |c| {
        let (state, error): (String, Option<String>) = c.query_row(
            "SELECT state,last_error FROM jobs WHERE job_id=?1",
            [&job_id],
            |r| Ok((r.get(0)?, r.get(1)?)),
        )?;
        assert_eq!(state, "waiting_user");
        let error: serde_json::Value = serde_json::from_str(&error.unwrap()).unwrap();
        assert_eq!(error["class"], "permission_denied");
        Ok(())
    })
    .await
    .unwrap();
    // A valid probe, credential change, upgrade scheduling and quality requeueing must not
    // turn a site refusal into work that is sent again without the user asking (DEC-041).
    db.write(|tx| {
        store::credentials::wake_waiting_auth(tx, "one", &jobs::now())?;
        credential_changed(tx, "one", true)?;
        crate::upgrades::schedule(tx, "one", 1)?;
        store::credentials::requeue_media_below_target(tx, "one", &jobs::now())
    })
    .await
    .unwrap();
    jobs::recover_stale_leases(&db, "restart-instance")
        .await
        .unwrap();
    let job_id = job.job_id.clone();
    db.read(move |c| {
        assert_eq!(
            c.query_row("SELECT state FROM jobs WHERE job_id=?1", [&job_id], |r| {
                r.get::<_, String>(0)
            })?,
            "waiting_user"
        );
        assert_eq!(
            c.query_row(
                "SELECT count(*) FROM jobs WHERE job_type='auth_completion'",
                [],
                |r| r.get::<_, i64>(0)
            )?,
            0
        );
        Ok(())
    })
    .await
    .unwrap();
    assert!(
        jobs::claim_due(&db, "next", 10).await.unwrap().is_empty(),
        "a refused subscription is not on the scan clock"
    );
    // Only the existing explicit retry command revives it.
    let retry = job.job_id.clone();
    db.write(move |tx| {
        store::jobs::retry(tx, &retry, &jobs::now())?;
        super::retry(tx, &retry)
    })
    .await
    .unwrap();
    let next = claim(&db).await;
    assert_eq!(next.job_id, job.job_id);
    assert_eq!(next.payload["subscription_id"], "first");
}

#[tokio::test]
async fn a_missing_incremental_lane_cannot_bypass_a_history_refusal() {
    let (db, _actor, _dir) = fixture("observe").await;
    db.write(|tx| {
        tx.execute("DELETE FROM job_consumers", [])?;
        tx.execute("DELETE FROM jobs", [])?;
        crate::store::subscriptions::insert_scan_lane(
            tx,
            "first",
            trace_domain::jobs::ScanMode::History,
            &jobs::now(),
        )?;
        tx.execute("UPDATE jobs SET next_run_at=NULL", [])?;
        Ok(())
    })
    .await
    .unwrap();
    let denied = claim(&db).await;
    denied
        .fail(
            &db,
            &trace_domain::errors::FailureClass::PermissionDenied,
            "signature refused",
            None,
        )
        .await
        .unwrap();
    let _ = db
        .write(|tx| crate::store::subscriptions::request_check_now(tx, "first", &jobs::now()))
        .await;
    assert!(
        jobs::claim_due(&db, "check-clock", 10)
            .await
            .unwrap()
            .is_empty(),
        "check_now must not create a fresh lane while this subscription is refused"
    );
}

#[tokio::test]
async fn a_range_change_cannot_erase_the_last_history_refusal() {
    let (db, _actor, _dir) = fixture("observe").await;
    db.write(|tx| {
        tx.execute("DELETE FROM job_consumers", [])?;
        tx.execute("DELETE FROM jobs", [])?;
        crate::store::subscriptions::insert_scan_lane(
            tx,
            "first",
            trace_domain::jobs::ScanMode::History,
            &jobs::now(),
        )?;
        tx.execute("UPDATE jobs SET next_run_at=NULL", [])?;
        Ok(())
    })
    .await
    .unwrap();
    let denied = claim(&db).await;
    denied
        .fail(
            &db,
            &trace_domain::errors::FailureClass::PermissionDenied,
            "signature refused",
            None,
        )
        .await
        .unwrap();
    db.write(|tx| {
        crate::store::subscriptions::set_range(
            tx,
            "first",
            "{\"kind\":\"from_now\"}",
            &jobs::now(),
        )?;
        crate::store::subscriptions::cancel_history_lane(tx, "first")?;
        assert!(
            subscription_refused(tx, "first")?,
            "changing the range is not an explicit permission retry"
        );
        Ok(())
    })
    .await
    .unwrap();
}

#[tokio::test]
async fn cancelling_a_refusal_does_not_grant_permission_to_automatic_work() {
    let (db, _actor, _dir) = fixture("observe").await;
    db.write(|tx| {
        tx.execute("DELETE FROM job_consumers", [])?;
        Ok(())
    })
    .await
    .unwrap();
    let denied = claim(&db).await;
    denied
        .fail(
            &db,
            &trace_domain::errors::FailureClass::PermissionDenied,
            "signature refused",
            None,
        )
        .await
        .unwrap();
    let id = denied.job_id.clone();
    db.write(move |tx| {
        jobs::cancel_work(tx, &id)?;
        crate::store::subscriptions::set_range(tx, "first", "{}", &jobs::now())?;
        crate::store::subscriptions::ensure_history_lane(tx, "first", &jobs::now())?;
        Ok(())
    })
    .await
    .unwrap();
    crate::scan_range::ensure_deep_lanes(&db).await.unwrap();
    let checks = db
        .write(|tx| crate::store::subscriptions::request_check_now(tx, "first", &jobs::now()))
        .await;
    assert!(matches!(checks, Err(CoreError::VersionConflict(_))));
    assert!(
        jobs::claim_due(&db, "cancelled-refusal", 10)
            .await
            .unwrap()
            .iter()
            .all(|job| job.payload["subscription_id"] != "first")
    );
    let retry = denied.job_id.clone();
    db.write(move |tx| {
        store::jobs::retry(tx, &retry, &jobs::now())?;
        assert!(!subscription_refused(tx, "first")?);
        Ok(())
    })
    .await
    .unwrap();
    assert_eq!(claim(&db).await.job_id, denied.job_id);
}

#[tokio::test]
async fn shared_auth_failure_and_credential_change_do_not_cross_consumers() {
    for kind in ["observe", "media"] {
        let (db, _actor, _dir) = fixture(kind).await;
        db.write(|tx|{
            for id in ["one","two"] {tx.execute("INSERT INTO credentials_state(credential_id,platform,subject,vault_ref,status,capability,created_at,updated_at) VALUES(?1,'douyin','test',?1,'valid','{}','t','t')",[id])?;}
            tx.execute("UPDATE subscriptions SET credential_id=CASE subscription_id WHEN 'first' THEN 'one' ELSE 'two' END",[])?;
            Ok(())
        }).await.unwrap();
        add(&db, kind, "second").await;
        let first = claim(&db).await;
        assert_eq!(first.payload["execution_scope"]["credential_id"], "one");
        first
            .fail(
                &db,
                &trace_domain::errors::FailureClass::AuthRequired,
                "auth",
                None,
            )
            .await
            .unwrap();
        let second = claim(&db).await;
        assert_eq!(second.payload["execution_scope"]["credential_id"], "two");
        assert_eq!(
            second.payload["execution_scope"]["credential_generation"],
            1
        );
        db.write(|tx| {
            tx.execute(
                "UPDATE credentials_state SET generation=2 WHERE credential_id='two'",
                [],
            )?;
            credential_changed(tx, "two", false)
        })
        .await
        .unwrap();
        assert!(second.heartbeat(&db).await.is_err());
        db.write(|tx| credential_changed(tx, "two", true))
            .await
            .unwrap();
        let next = claim(&db).await;
        assert_eq!(next.payload["execution_scope"]["credential_generation"], 2);
        db.read(|c| {
            assert_eq!(
                c.query_row(
                    "SELECT attempts FROM job_consumers WHERE subscription_id='first'",
                    [],
                    |r| r.get::<_, i64>(0)
                )?,
                1
            );
            assert_eq!(
                c.query_row(
                    "SELECT attempts FROM job_consumers WHERE subscription_id='second'",
                    [],
                    |r| r.get::<_, i64>(0)
                )?,
                0
            );
            Ok(())
        })
        .await
        .unwrap();
    }
}
