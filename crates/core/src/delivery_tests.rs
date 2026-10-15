//! Download requests: a request settles once, only when no job of it is on its way, its copies
//! are written from what was really saved, and cancelling it stops its work.
use crate::config::Profile;
use crate::core::Core;
use crate::downloads::delivery;
use crate::jobs;
use crate::store::deliveries as repo;
use serde_json::{Value, json};
use std::sync::Arc;
use trace_contracts::preview::{DeliveryListArgs, DownloadTarget};

struct Fx {
    _dir: tempfile::TempDir,
    core: Arc<Core>,
}

async fn fixture() -> Fx {
    let dir = tempfile::tempdir().unwrap();
    let core = Core::open(Profile {
        data_root: dir.path().join("data"),
        archive_root: dir.path().join("archive"),
        vault_dir: Some(dir.path().join("vault")),
        ..Default::default()
    })
    .await
    .unwrap();
    core.db
        .write(|tx| {
            tx.execute_batch(
                "INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('a','weibo','1','t');
                 INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES('done','weibo','dynamic','P1','a','t');
                 INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES('gone','weibo','dynamic','P2','a','t');
                 INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at) VALUES('r1','done','d','标题','正文','2026-10-16T00:00:00Z');",
            )?;
            Ok(())
        })
        .await
        .unwrap();
    Fx { _dir: dir, core }
}

/// A request over the two entries, with one job per entry on its way.
async fn request(fx: &Fx, target: &DownloadTarget) -> String {
    let destination = serde_json::to_string(target).unwrap();
    fx.core
        .db
        .write(move |tx| {
            let now = jobs::now();
            repo::create_scope(tx, "once-d1", "a", None, &now)?;
            repo::insert(
                tx,
                &repo::NewDelivery {
                    delivery_id: "d1",
                    kind: "profile",
                    platform: "weibo",
                    source_url: "https://weibo.com/u/1?token=secret",
                    request_id: None,
                    scope_id: "once-d1",
                    destination: &destination,
                },
                &[
                    repo::NewItem {
                        native_key: "P1",
                        native_kind: "dynamic",
                        title: "第一条",
                        entry_id: "done",
                        ready: false,
                    },
                    repo::NewItem {
                        native_key: "P2",
                        native_kind: "dynamic",
                        title: "第二条",
                        entry_id: "gone",
                        ready: false,
                    },
                ],
                &now,
            )?;
            for entry in ["done", "gone"] {
                tx.execute(
                    "INSERT INTO jobs(job_id,job_key,job_type,subject_id,payload,state,priority,created_at,updated_at) VALUES(?1,?1,'media',?2,?3,'queued',1,?4,?4)",
                    rusqlite::params![
                        format!("job-{entry}"),
                        entry,
                        json!({"entry_id": entry, "delivery_id": "d1"}).to_string(),
                        now
                    ],
                )?;
            }
            Ok(())
        })
        .await
        .unwrap();
    "d1".into()
}

async fn set_job(fx: &Fx, job: &'static str, state: &'static str, class: Option<&'static str>) {
    fx.core
        .db
        .write(move |tx| {
            tx.execute(
                "UPDATE jobs SET state=?2,last_error=?3 WHERE job_id=?1",
                rusqlite::params![
                    job,
                    state,
                    class.map(|c| json!({"class": c, "message": "x"}).to_string())
                ],
            )?;
            repo::job_settled(tx, &json!({"delivery_id": "d1"}), &jobs::now())
        })
        .await
        .unwrap();
}

async fn state_of(fx: &Fx) -> (String, i64) {
    fx.core
        .db
        .read(|c| {
            Ok(c.query_row(
                "SELECT state,generation FROM deliveries WHERE delivery_id='d1'",
                [],
                |r| Ok((r.get(0)?, r.get(1)?)),
            )?)
        })
        .await
        .unwrap()
}

async fn delivery_jobs(fx: &Fx) -> i64 {
    fx.core
        .db
        .read(|c| {
            Ok(c.query_row(
                "SELECT count(*) FROM jobs WHERE job_type='delivery'",
                [],
                |r| r.get(0),
            )?)
        })
        .await
        .unwrap()
}

#[tokio::test]
async fn a_request_settles_only_when_no_job_is_on_its_way_and_queues_its_copy_job_once() {
    let fx = fixture().await;
    request(&fx, &DownloadTarget::library()).await;
    // One job finished, the other is still queued: nothing to deliver yet.
    set_job(&fx, "job-done", "succeeded", None).await;
    assert_eq!(state_of(&fx).await, ("running".into(), 0));
    assert_eq!(delivery_jobs(&fx).await, 0);
    // Waiting for a retry is still on its way; waiting for a login is not.
    set_job(&fx, "job-gone", "waiting_retry", Some("transient_network")).await;
    assert_eq!(delivery_jobs(&fx).await, 0);
    set_job(&fx, "job-gone", "waiting_auth", Some("auth_required")).await;
    assert_eq!(state_of(&fx).await, ("finishing".into(), 1));
    assert_eq!(delivery_jobs(&fx).await, 1);
    // The same settling twice queues nothing more.
    set_job(&fx, "job-gone", "waiting_auth", Some("auth_required")).await;
    assert_eq!(delivery_jobs(&fx).await, 1);
}

#[tokio::test]
async fn copies_are_written_from_what_was_saved_and_a_late_finish_delivers_again() {
    let fx = fixture().await;
    let dir = tempfile::tempdir().unwrap();
    let target = DownloadTarget {
        kind: "folder".into(),
        dir: Some(dir.path().join("out").to_string_lossy().into_owned()),
    };
    request(&fx, &target).await;
    set_job(&fx, "job-done", "succeeded", None).await;
    set_job(&fx, "job-gone", "waiting_auth", Some("auth_required")).await;
    let job = jobs::claim_due(&fx.core.db, "delivery-test", 10)
        .await
        .unwrap()
        .into_iter()
        .find(|j| j.job_type == "delivery")
        .expect("the copy job is queued");
    delivery::run(&fx.core, &job).await.unwrap();
    let listed = delivery::list(&fx.core, DeliveryListArgs::default())
        .await
        .unwrap();
    let view = &listed.items[0];
    assert_eq!(view.state, "done");
    assert_eq!(view.source, "weibo.com/u/1", "the query is never shown");
    let by_key = |key: &str| view.items.iter().find(|i| i.native_key == key).unwrap();
    assert_eq!(by_key("P1").state, "exported");
    assert!(by_key("P1").files >= 1);
    assert_eq!(by_key("P2").state, "failed");
    assert!(
        by_key("P2").message.as_deref().unwrap().contains("登录"),
        "{:?}",
        by_key("P2").message
    );
    assert!(
        std::fs::read_dir(dir.path().join("out"))
            .unwrap()
            .next()
            .is_some()
    );
    // The login completes later and the stopped job finishes: the request delivers again.
    set_job(&fx, "job-gone", "succeeded", None).await;
    assert_eq!(state_of(&fx).await, ("finishing".into(), 2));
    assert_eq!(delivery_jobs(&fx).await, 2);
}

#[tokio::test]
async fn cancelling_stops_the_work_the_items_and_the_scope() {
    let fx = fixture().await;
    request(&fx, &DownloadTarget::library()).await;
    assert!(delivery::cancel(&fx.core, "d1".into()).await.unwrap());
    assert!(
        !delivery::cancel(&fx.core, "d1".into()).await.unwrap(),
        "cancelling twice changes nothing"
    );
    let (jobs_left, items, scope): (i64, i64, String) = fx
        .core
        .db
        .read(|c| {
            Ok((
                c.query_row(
                    "SELECT count(*) FROM jobs WHERE state NOT IN ('cancelled','succeeded','failed')",
                    [],
                    |r| r.get(0),
                )?,
                c.query_row(
                    "SELECT count(*) FROM delivery_items WHERE state='cancelled'",
                    [],
                    |r| r.get(0),
                )?,
                c.query_row(
                    "SELECT state FROM subscriptions WHERE subscription_id='once-d1'",
                    [],
                    |r| r.get(0),
                )?,
            ))
        })
        .await
        .unwrap();
    assert_eq!((jobs_left, items, scope.as_str()), (0, 2, "paused"));
    // Nothing settles a cancelled request.
    fx.core
        .db
        .write(|tx| repo::settle(tx, "d1", &jobs::now()).map(|_| ()))
        .await
        .unwrap();
    assert_eq!(state_of(&fx).await.0, "cancelled");
    assert_eq!(delivery_jobs(&fx).await, 0);
}

#[tokio::test]
async fn a_preview_selection_names_only_shown_entries_and_holds_what_is_complete() {
    use crate::profile_preview::download;
    use trace_contracts::preview::ProfileDownloadArgs;
    let fx = fixture().await;
    // A preview of weibo account 1 that showed P1 (held completely) and P3 (new), then expired.
    fx.core
        .db
        .write(|tx| {
            tx.execute_batch(
                "INSERT INTO profile_previews(preview_id,platform,rule_id,rule_title,source_url,account_key,authentication,pages,has_more,created_at,expires_at) VALUES('pv','weibo','weibo.user.preview','主页','https://weibo.com/u/1','1','anonymous',1,0,'2026-10-16T00:00:00Z','2999-01-01T00:00:00Z');
                 INSERT INTO profile_preview_items(preview_id,position,native_key,native_kind,title) VALUES('pv',0,'P1','dynamic','第一条');
                 INSERT INTO profile_preview_items(preview_id,position,native_key,native_kind,title) VALUES('pv',1,'P3','dynamic','第三条');",
            )?;
            Ok(())
        })
        .await
        .unwrap();
    let args = |keys: &[&str]| ProfileDownloadArgs {
        preview_id: "pv".into(),
        native_keys: keys.iter().map(|k| (*k).to_owned()).collect(),
        target: DownloadTarget::library(),
    };
    assert!(download(&fx.core, args(&[])).await.is_err());
    assert!(
        download(&fx.core, args(&["P9"])).await.is_err(),
        "an entry nobody was shown is not downloadable"
    );
    let report = download(&fx.core, args(&["P1", "P3", "P9"])).await.unwrap();
    assert_eq!((report.accepted, report.already_in_library), (1, 1));
    assert_eq!(report.skipped.len(), 1);
    assert_eq!(report.skipped[0].native_key, "P9");
    // Only the new entry is observed, as a request of its own that is never held back.
    let observe: Vec<Value> = fx
        .core
        .db
        .read(|c| {
            let rows = c
                .prepare("SELECT payload FROM jobs WHERE job_type='observe'")?
                .query_map([], |r| r.get::<_, String>(0))?
                .collect::<Result<Vec<_>, _>>()?;
            Ok(rows
                .iter()
                .map(|p| serde_json::from_str(p).unwrap())
                .collect())
        })
        .await
        .unwrap();
    assert_eq!(observe.len(), 1);
    assert_eq!(observe[0]["delivery_id"], report.delivery_id.as_str());
    assert!(
        observe[0]["subscription_id"]
            .as_str()
            .unwrap()
            .starts_with("once-")
    );
    // The held entry is "ready" at once; the new one is still being fetched.
    let listed = delivery::list(
        &fx.core,
        DeliveryListArgs {
            delivery_id: Some(report.delivery_id),
            limit: None,
        },
    )
    .await
    .unwrap();
    let states: Vec<_> = listed.items[0]
        .items
        .iter()
        .map(|i| (i.native_key.as_str(), i.state.as_str()))
        .collect();
    assert_eq!(states, [("P1", "ready"), ("P3", "queued")]);
}
