use super::*;
use crate::db::{DbActor, DbHandle};
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
async fn c11_shared_observe_and_media_cover_all_owner_states_and_policies() {
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
async fn c11_shared_control_revocation_restart_and_cancel_do_not_spend_failures() {
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
async fn c11_shared_auth_failure_and_credential_change_do_not_cross_consumers() {
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
