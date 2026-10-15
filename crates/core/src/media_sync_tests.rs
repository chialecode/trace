//! C07 auto-download and sync: an undownloaded video is its own state, a manual sync queues each
//! media group once, and observing again never moves a scan position.
use crate::app::media_sync::{recheck, sync};
use crate::config::Profile;
use crate::core::Core;
use serde_json::{Value, json};
use std::sync::Arc;
use trace_contracts::control::Request;
use trace_contracts::media_sync::RecheckArgs;

struct Fx {
    _dir: tempfile::TempDir,
    core: Arc<Core>,
    subscription: String,
}

async fn call(
    core: &Arc<Core>,
    command: &str,
    args: Value,
) -> Result<Value, crate::error::CoreError> {
    crate::app::execute(
        core,
        &Request {
            protocol_version: "0.1".into(),
            request_id: "r".into(),
            command: command.into(),
            args,
            idempotency_key: None,
            if_revision: None,
        },
    )
    .await
}

async fn fixture(auto_download: bool) -> Fx {
    let dir = tempfile::tempdir().unwrap();
    let core = Core::open(Profile {
        data_root: dir.path().join("data"),
        archive_root: dir.path().join("archive"),
        vault_dir: Some(dir.path().join("vault")),
        ..Default::default()
    })
    .await
    .unwrap();
    let creator = call(
        &core,
        "creator.create",
        json!({"display_name":"作者","aliases":[],"tags":[],"notes":""}),
    )
    .await
    .unwrap();
    let account = call(
        &core,
        "account.bind",
        json!({"creator_id": creator["creator_id"], "namespace":"bilibili","native_account_key":"7"}),
    )
    .await
    .unwrap();
    let subscription = call(
        &core,
        "subscription.create",
        json!({"account_id": account["account_id"], "content_type":"video",
               "range":{"kind":"recent_days","days":30}, "auto_download": auto_download}),
    )
    .await
    .unwrap();
    Fx {
        _dir: dir,
        core,
        subscription: subscription["subscription_id"].as_str().unwrap().into(),
    }
}

/// An entry with a video group (video + audio) and an image, as a subscription left it:
/// the video group is `deferred` when `defer` is set.
async fn add_entry(fx: &Fx, id: &str, published: &str, defer: bool) {
    let (id, published, sub) = (id.to_owned(), published.to_owned(), fx.subscription.clone());
    fx.core
        .db
        .write(move |tx| {
            let account: String = tx.query_row(
                "SELECT account_id FROM source_accounts LIMIT 1",
                [],
                |r| r.get(0),
            )?;
            tx.execute(
                "INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at,published_at) VALUES(?1,'bilibili','video',?1,?2,'t',?3)",
                rusqlite::params![id, account, published],
            )?;
            let revision = format!("r-{id}");
            tx.execute(
                "INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at) VALUES(?1,?2,'d','标题','正文','2026-10-16T00:00:00Z')",
                rusqlite::params![revision, id],
            )?;
            let state = if defer { "deferred" } else { "wanted" };
            for (media, role, position, group) in [
                (format!("{id}-v"), "video", 0, Some("g1")),
                (format!("{id}-a"), "audio", 1, Some("g1")),
                (format!("{id}-i"), "image", 2, None),
            ] {
                let fetch = if role == "image" { "wanted" } else { state };
                tx.execute(
                    "INSERT INTO media_items(media_id,revision_id,role,position,source_attachment_id,group_key,fetch_state) VALUES(?1,?2,?3,?4,?1,?5,?6)",
                    rusqlite::params![media, revision, role, position, group, fetch],
                )?;
            }
            tx.execute(
                "INSERT INTO entry_subscriptions VALUES(?1,?2,'t','t')",
                rusqlite::params![id, sub],
            )?;
            Ok(())
        })
        .await
        .unwrap();
}

/// Marks the image as saved, the way a finished image download would.
async fn save_image(fx: &Fx, id: &str) {
    let media = format!("{id}-i");
    let digest = format!("{:064x}", media.len());
    fx.core
        .db
        .write(move |tx| {
            let blob = format!("sha256:{digest}");
            tx.execute(
                "INSERT OR IGNORE INTO blobs(blob_id,size,mime,first_seen_at) VALUES(?1,3,'image/jpeg','t')",
                [&blob],
            )?;
            tx.execute(
                "INSERT INTO media_variants(variant_id,media_id,source_representation,blob_id,claimed_spec,measured_spec,created_at) VALUES(?1,?2,'o',?3,'{}','{}','t')",
                rusqlite::params![format!("v-{media}"), media, blob],
            )?;
            tx.execute(
                "INSERT OR IGNORE INTO replicas(replica_id,blob_id,node_id,store_id,locator,managed,state,verified_at) VALUES(?1,?2,'local','primary',?3,1,'verified','t')",
                rusqlite::params![format!("rp-{digest}"), blob, digest],
            )?;
            Ok(())
        })
        .await
        .unwrap();
}

async fn count(core: &Core, sql: &'static str) -> i64 {
    core.db
        .read(move |c| Ok(c.query_row(sql, [], |r| r.get(0))?))
        .await
        .unwrap()
}

async fn progress(core: &Arc<Core>, id: &str) -> (String, String) {
    let id = id.to_owned();
    core.db
        .read(move |c| {
            let (_, progress, quality, _) = crate::store::entries::states(c, &id)?;
            Ok((progress, quality))
        })
        .await
        .unwrap()
}

#[tokio::test]
async fn an_undownloaded_video_is_its_own_state_not_missing_or_failed() {
    let fx = fixture(false).await;
    add_entry(&fx, "e1", "2026-10-15T08:00:00Z", true).await;
    // Only the video is held back: nothing is missing, nothing failed.
    assert_eq!(progress(&fx.core, "e1").await.0, "pending");
    // The image is the only wanted resource; once it is saved the entry is "deferred".
    save_image(&fx, "e1").await;
    let (state, quality) = progress(&fx.core, "e1").await;
    assert_eq!(state, "deferred");
    assert_eq!(
        quality, "unknown",
        "no quality verdict for what was never fetched"
    );
    let timeline = call(&fx.core, "archive.timeline", json!({"limit":10}))
        .await
        .unwrap();
    assert_eq!(timeline["items"][0]["save_progress"], "deferred");
    // The save-state filter agrees with the annotation.
    let filtered = call(
        &fx.core,
        "archive.timeline",
        json!({"limit":10,"save_state":"deferred"}),
    )
    .await
    .unwrap();
    assert_eq!(filtered["items"].as_array().unwrap().len(), 1);
    let none = call(
        &fx.core,
        "archive.timeline",
        json!({"limit":10,"save_state":"pending"}),
    )
    .await
    .unwrap();
    assert!(none["items"].as_array().unwrap().is_empty());
    // The resource list says so per resource.
    let resources = call(&fx.core, "archive.resources", json!({"role":"video"}))
        .await
        .unwrap();
    assert_eq!(resources["items"][0]["fetch_state"], "deferred");
    let media = call(
        &fx.core,
        "archive.entry.page",
        json!({"entry_id":"e1","section":"media"}),
    )
    .await
    .unwrap();
    assert!(
        media["items"]
            .as_array()
            .unwrap()
            .iter()
            .any(|m| m["fetch_state"] == "deferred")
    );
    // No media job exists and none failed.
    assert_eq!(
        count(&fx.core, "SELECT count(*) FROM jobs WHERE job_type='media'").await,
        0
    );
    assert_eq!(
        count(&fx.core, "SELECT count(*) FROM jobs WHERE state='failed'").await,
        0
    );
}

#[tokio::test]
async fn an_entry_that_downloads_automatically_has_no_deferred_state() {
    let fx = fixture(true).await;
    add_entry(&fx, "e1", "2026-10-15T08:00:00Z", false).await;
    save_image(&fx, "e1").await;
    assert_eq!(progress(&fx.core, "e1").await.0, "partial");
    let report = sync(&fx.core, &["e1".into()]).await.unwrap();
    assert_eq!(report.items[0].state, "nothing", "{report:?}");
}

#[tokio::test]
async fn sync_queues_each_media_group_once_and_selecting_again_changes_nothing() {
    let fx = fixture(false).await;
    add_entry(&fx, "e1", "2026-10-15T08:00:00Z", true).await;
    let first = sync(&fx.core, &["e1".into(), "e1".into()]).await.unwrap();
    assert_eq!(first.items.len(), 1, "a repeated id is one request");
    assert_eq!(
        (first.items[0].state.as_str(), first.items[0].groups),
        ("queued", 1)
    );
    assert_eq!(
        count(&fx.core, "SELECT count(*) FROM jobs WHERE job_type='media'").await,
        1
    );
    assert_eq!(
        count(
            &fx.core,
            "SELECT count(*) FROM media_items WHERE fetch_state='deferred'"
        )
        .await,
        0
    );
    let payload: String = fx
        .core
        .db
        .read(|c| {
            Ok(
                c.query_row("SELECT payload FROM jobs WHERE job_type='media'", [], |r| {
                    r.get(0)
                })?,
            )
        })
        .await
        .unwrap();
    let payload: Value = serde_json::from_str(&payload).unwrap();
    assert_eq!(payload["group"], "g1");
    assert_eq!(payload["subscription_id"], fx.subscription.as_str());
    let jobs_before = count(&fx.core, "SELECT count(*) FROM jobs").await;
    for _ in 0..3 {
        let again = sync(&fx.core, &["e1".into()]).await.unwrap();
        assert_eq!(again.items[0].state, "already_queued", "{again:?}");
    }
    assert_eq!(
        count(&fx.core, "SELECT count(*) FROM jobs").await,
        jobs_before
    );
    assert_eq!(
        count(&fx.core, "SELECT count(*) FROM job_consumers").await,
        1
    );
}

#[tokio::test]
async fn sync_retries_failed_media_work_and_reports_unknown_entries() {
    let fx = fixture(false).await;
    add_entry(&fx, "e1", "2026-10-15T08:00:00Z", true).await;
    sync(&fx.core, &["e1".into()]).await.unwrap();
    // The download failed partway.
    fx.core
        .db
        .write(|tx| {
            tx.execute(
                "UPDATE jobs SET state='failed',attempts=5 WHERE job_type='media'",
                [],
            )?;
            tx.execute("UPDATE job_consumers SET state='failed',attempts=5", [])?;
            Ok(())
        })
        .await
        .unwrap();
    let retried = sync(&fx.core, &["e1".into(), "missing".into()])
        .await
        .unwrap();
    assert_eq!(retried.items[0].state, "queued", "{retried:?}");
    assert_eq!(retried.items[1].state, "nothing");
    let state: String = fx
        .core
        .db
        .read(|c| {
            Ok(
                c.query_row("SELECT state FROM jobs WHERE job_type='media'", [], |r| {
                    r.get(0)
                })?,
            )
        })
        .await
        .unwrap();
    assert_eq!(state, "queued");
    assert_eq!(
        count(&fx.core, "SELECT count(*) FROM jobs WHERE job_type='media'").await,
        1
    );
    assert!(sync(&fx.core, &[]).await.is_err());
}

#[tokio::test]
async fn sync_without_a_usable_subscription_says_so_and_leaves_the_video_deferred() {
    let fx = fixture(false).await;
    add_entry(&fx, "e1", "2026-10-15T08:00:00Z", true).await;
    call(
        &fx.core,
        "subscription.update",
        json!({"subscription_id": fx.subscription, "enabled": false}),
    )
    .await
    .unwrap();
    let report = sync(&fx.core, &["e1".into()]).await.unwrap();
    assert_eq!(report.items[0].state, "failed", "{report:?}");
    assert!(report.items[0].message.as_deref().unwrap().contains("订阅"));
    assert_eq!(
        count(
            &fx.core,
            "SELECT count(*) FROM media_items WHERE fetch_state='deferred'"
        )
        .await,
        2
    );
    assert_eq!(
        count(&fx.core, "SELECT count(*) FROM jobs WHERE job_type='media'").await,
        0
    );
}

#[tokio::test]
async fn the_switch_is_stored_per_subscription_and_changes_nothing_that_exists() {
    let fx = fixture(false).await;
    add_entry(&fx, "e1", "2026-10-15T08:00:00Z", true).await;
    let listed = |value: Value, id: &str| -> bool {
        value["items"]
            .as_array()
            .unwrap()
            .iter()
            .find(|s| s["subscription_id"] == id)
            .unwrap()["auto_download"]
            .as_bool()
            .unwrap()
    };
    let list = call(&fx.core, "subscription.list", json!({}))
        .await
        .unwrap();
    assert!(!listed(list, &fx.subscription));
    for on in [true, false, true] {
        call(
            &fx.core,
            "subscription.update",
            json!({"subscription_id": fx.subscription, "auto_download": on}),
        )
        .await
        .unwrap();
        let list = call(&fx.core, "subscription.list", json!({}))
            .await
            .unwrap();
        assert_eq!(listed(list, &fx.subscription), on);
    }
    // Turning it on neither downloads nor forgets what was left undownloaded.
    assert_eq!(
        count(
            &fx.core,
            "SELECT count(*) FROM media_items WHERE fetch_state='deferred'"
        )
        .await,
        2
    );
    assert_eq!(
        count(&fx.core, "SELECT count(*) FROM jobs WHERE job_type='media'").await,
        0
    );
}

#[tokio::test]
async fn observing_again_by_list_period_or_failure_never_moves_the_scan_position() {
    let fx = fixture(false).await;
    add_entry(&fx, "e1", "2026-10-10T08:00:00Z", true).await;
    add_entry(&fx, "e2", "2026-10-12T08:00:00Z", true).await;
    add_entry(&fx, "e3", "2026-10-14T08:00:00Z", true).await;
    let sub = fx.subscription.clone();
    fx.core
        .db
        .write(move |tx| {
            tx.execute(
                "INSERT INTO scan_checkpoints(subscription_id,scan_type,cursor,coverage,generation,updated_at) VALUES(?1,'recent','page-9','{\"seen\":3}',4,'x')",
                [&sub],
            )?;
            tx.execute(
                "INSERT INTO scan_cursor_visits VALUES(?1,'recent','abc')",
                [&sub],
            )?;
            Ok(())
        })
        .await
        .unwrap();
    let snapshot = |core: Arc<Core>| async move {
        core.db
            .read(|c| {
                let mut out = Vec::new();
                for sql in [
                    "SELECT subscription_id||scan_type||COALESCE(cursor,'-')||coverage||generation||COALESCE(updated_at,'-') FROM scan_checkpoints ORDER BY 1",
                    "SELECT subscription_id||scan_type||cursor_digest FROM scan_cursor_visits ORDER BY 1",
                ] {
                    out.extend(
                        c.prepare(sql)?
                            .query_map([], |r| r.get::<_, String>(0))?
                            .collect::<Result<Vec<_>, _>>()?,
                    );
                }
                Ok(out)
            })
            .await
            .unwrap()
    };
    let before = snapshot(fx.core.clone()).await;
    assert!(before.len() >= 2);

    let listed = recheck(
        &fx.core,
        RecheckArgs {
            entry_ids: vec!["e1".into()],
            ..Default::default()
        },
    )
    .await
    .unwrap();
    assert_eq!((listed.queued, listed.skipped), (1, 0), "{listed:?}");
    // 11th to 12th (the upper date includes its whole day): e1 and e2, newest first.
    let period = recheck(
        &fx.core,
        RecheckArgs {
            from: Some("2026-10-10".into()),
            to: Some("2026-10-12".into()),
            ..Default::default()
        },
    )
    .await
    .unwrap();
    let ids: Vec<_> = period.items.iter().map(|i| i.entry_id.as_str()).collect();
    assert_eq!(ids, ["e2", "e1"]);
    // A narrower limit says it left some out.
    let limited = recheck(
        &fx.core,
        RecheckArgs {
            from: Some("2026-10-01".into()),
            limit: Some(2),
            ..Default::default()
        },
    )
    .await
    .unwrap();
    assert_eq!(limited.items.len(), 2);
    assert!(limited.truncated);
    // Failed work is retried, not observed again.
    sync(&fx.core, &["e3".into()]).await.unwrap();
    fx.core
        .db
        .write(|tx| {
            tx.execute("UPDATE jobs SET state='failed' WHERE job_type='media'", [])?;
            tx.execute("UPDATE job_consumers SET state='failed' WHERE job_id IN (SELECT job_id FROM jobs WHERE job_type='media')", [])?;
            Ok(())
        })
        .await
        .unwrap();
    let failed = recheck(
        &fx.core,
        RecheckArgs {
            failed: true,
            ..Default::default()
        },
    )
    .await
    .unwrap();
    assert_eq!(failed.items.len(), 1, "{failed:?}");
    assert_eq!(failed.items[0].state, "retried");
    assert_eq!(failed.items[0].entry_id, "e3");
    assert_eq!(
        snapshot(fx.core.clone()).await,
        before,
        "scan position is untouched"
    );
}

#[tokio::test]
async fn recheck_asks_for_exactly_one_kind_of_selection_and_a_readable_period() {
    let fx = fixture(false).await;
    for args in [
        RecheckArgs::default(),
        RecheckArgs {
            entry_ids: vec!["a".into()],
            failed: true,
            ..Default::default()
        },
        RecheckArgs {
            from: Some("2026-10-01".into()),
            failed: true,
            ..Default::default()
        },
        RecheckArgs {
            from: Some("tomorrow".into()),
            ..Default::default()
        },
        RecheckArgs {
            to: Some("2026-13-45".into()),
            ..Default::default()
        },
        RecheckArgs {
            entry_ids: (0..201).map(|n| n.to_string()).collect(),
            ..Default::default()
        },
    ] {
        assert!(recheck(&fx.core, args.clone()).await.is_err(), "{args:?}");
    }
    // A full instant works as well as a date.
    let ok = recheck(
        &fx.core,
        RecheckArgs {
            from: Some("2026-10-01T00:00:00+08:00".into()),
            ..Default::default()
        },
    )
    .await
    .unwrap();
    assert!(ok.items.is_empty() && !ok.truncated);
    // An entry that no enabled subscription covers is skipped with a reason, not an error.
    let report = recheck(
        &fx.core,
        RecheckArgs {
            entry_ids: vec!["nope".into()],
            ..Default::default()
        },
    )
    .await
    .unwrap();
    assert_eq!((report.queued, report.skipped), (0, 1));
    assert!(report.items[0].message.is_some());
}

#[tokio::test]
async fn the_commands_are_reachable_and_validate_their_arguments() {
    let fx = fixture(false).await;
    add_entry(&fx, "e1", "2026-10-15T08:00:00Z", true).await;
    let report = call(&fx.core, "archive.entry.sync", json!({"entry_ids":["e1"]}))
        .await
        .unwrap();
    assert_eq!(report["items"][0]["state"], "queued");
    let again = call(&fx.core, "archive.recheck", json!({"entry_ids":["e1"]}))
        .await
        .unwrap();
    assert_eq!(again["queued"], 1);
    assert!(call(&fx.core, "archive.recheck", json!({})).await.is_err());
    assert!(
        call(
            &fx.core,
            "archive.entry.sync",
            json!({"entry_ids":["e1"],"x":1})
        )
        .await
        .is_err()
    );
    // The older single-entry command keeps working.
    let single = call(&fx.core, "archive.entry.recheck", json!({"entry_id":"e1"}))
        .await
        .unwrap();
    assert!(single["job_id"].is_string());
    for name in ["archive.entry.sync", "archive.recheck"] {
        assert!(trace_contracts::control::commands::is_ui_command(name));
    }
}

mod discovery {
    use super::{Fx, count, fixture};
    use crate::jobs;
    use crate::pipeline::observe::persist_detail;
    use serde_json::{Value, json};
    use trace_contracts::content::Document;
    use trace_contracts::plugin::{ObservedDetail, ResourceIdentityDto};

    fn resource(role: &str, key: &str, position: i64, group: Option<&str>) -> ResourceIdentityDto {
        ResourceIdentityDto {
            role: role.into(),
            source_attachment_id: key.into(),
            position,
            title: None,
            group_key: group.map(str::to_owned),
            required: true,
        }
    }

    fn detail(key: &str) -> ObservedDetail {
        ObservedDetail {
            document: Document::default(),
            metadata: json!({}),
            native_entry_key: key.into(),
            native_kind: "video".into(),
            title: "标题".into(),
            normalized_body: "正文".into(),
            // Dated inside the subscription's range: an undated detail gets no automatic video.
            published_at: Some(
                (time::OffsetDateTime::now_utc() - time::Duration::days(1))
                    .format(&time::format_description::well_known::Rfc3339)
                    .unwrap(),
            ),
            published_precision: "source".into(),
            access_state: "visible".into(),
            source_version_evidence: json!({}),
            resources: vec![
                resource("video", "v1", 0, Some("g1")),
                resource("audio", "a1", 1, Some("g1")),
                resource("image", "i1", 2, None),
            ],
            completeness: "full".into(),
            stats: json!({}),
            unified: None,
        }
    }

    /// One observation of `key` by a job carrying `payload`, as the discovery pipeline runs it.
    async fn observe(fx: &Fx, key: &str, payload: Value) {
        let entry = format!("pe-{key}");
        let (e, k) = (entry.clone(), key.to_owned());
        let account: String = fx
            .core
            .db
            .write(move |tx| {
                let account: String = tx.query_row(
                    "SELECT account_id FROM source_accounts LIMIT 1",
                    [],
                    |r| r.get(0),
                )?;
                tx.execute(
                    "INSERT OR IGNORE INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES(?1,'bilibili','video',?2,?3,'t')",
                    rusqlite::params![e, k, account],
                )?;
                Ok(account)
            })
            .await
            .unwrap();
        let job_key = format!("observe-{key}-{}", uuid::Uuid::new_v4());
        let id = jobs::enqueue(&fx.core.db, job_key, "observe", &entry, payload, 1, 1, 0)
            .await
            .unwrap();
        // The subscription's own scan job is due as well; take the one made here.
        let job = jobs::claim_due(&fx.core.db, "defer-test", 50)
            .await
            .unwrap()
            .into_iter()
            .find(|j| j.job_id == id)
            .expect("the observation job is claimed");
        job.mark_running(&fx.core.db).await.unwrap();
        persist_detail(&fx.core, &job, &entry, &account, detail(key))
            .await
            .unwrap();
    }

    async fn deferred(fx: &Fx, key: &str) -> i64 {
        let entry = format!("pe-{key}");
        fx.core
            .db
            .read(move |c| {
                Ok(c.query_row(
                    "SELECT count(*) FROM media_items m JOIN entry_revisions r ON r.revision_id=m.revision_id
                     WHERE r.entry_id=?1 AND m.fetch_state='deferred'",
                    [&entry],
                    |r| r.get(0),
                )?)
            })
            .await
            .unwrap()
    }

    async fn media_jobs(fx: &Fx, key: &str) -> i64 {
        let entry = format!("pe-{key}");
        fx.core
            .db
            .read(move |c| {
                Ok(c.query_row(
                    "SELECT count(*) FROM jobs WHERE job_type='media' AND subject_id=?1",
                    [&entry],
                    |r| r.get(0),
                )?)
            })
            .await
            .unwrap()
    }

    #[tokio::test]
    async fn discovery_without_auto_download_keeps_the_video_group_and_downloads_the_rest() {
        let fx = fixture(false).await;
        let payload = json!({"subscription_id": fx.subscription});
        observe(&fx, "p1", payload.clone()).await;
        // The video and its audio are left undownloaded; the image is fetched as before.
        assert_eq!(deferred(&fx, "p1").await, 2);
        assert_eq!(media_jobs(&fx, "p1").await, 1);
        // Observing again changes nothing, and turning the switch on later does not
        // download what was already left behind.
        observe(&fx, "p1", payload.clone()).await;
        let sub = fx.subscription.clone();
        fx.core
            .db
            .write(move |tx| {
                crate::store::subscriptions::set_auto_download(tx, &sub, true, "now")?;
                Ok(())
            })
            .await
            .unwrap();
        observe(&fx, "p1", payload.clone()).await;
        assert_eq!(deferred(&fx, "p1").await, 2);
        assert_eq!(media_jobs(&fx, "p1").await, 1);
        // What is discovered after the switch is on downloads everything.
        observe(&fx, "p2", payload).await;
        assert_eq!(deferred(&fx, "p2").await, 0);
        assert_eq!(media_jobs(&fx, "p2").await, 2);
    }

    #[tokio::test]
    async fn a_download_request_fetches_a_video_a_subscription_left_behind() {
        let fx = fixture(false).await;
        let sub = json!({"subscription_id": fx.subscription});
        observe(&fx, "kept", sub).await;
        assert_eq!(deferred(&fx, "kept").await, 2);
        // The same entry asked for by a preview selection: the held-back group is wanted now
        // and its work belongs to the request, not to the subscription.
        observe(
            &fx,
            "kept",
            json!({"subscription_id": fx.subscription, "delivery_id": "dx"}),
        )
        .await;
        assert_eq!(deferred(&fx, "kept").await, 0);
        let keys: Vec<String> = fx
            .core
            .db
            .read(|c| {
                Ok(
                    c.prepare("SELECT job_key FROM jobs WHERE job_type='media' ORDER BY job_key")?
                        .query_map([], |r| r.get(0))?
                        .collect::<Result<Vec<String>, _>>()?,
                )
            })
            .await
            .unwrap();
        assert_eq!(
            keys.iter()
                .filter(|k| k.starts_with("delivery-media:dx:"))
                .count(),
            2
        );
        let owned: i64 = count(
            &fx.core,
            "SELECT count(*) FROM jobs WHERE job_type='media' AND json_extract(payload,'$.delivery_id')='dx'",
        )
        .await;
        assert_eq!(owned, 2);
    }

    #[tokio::test]
    async fn explicit_requests_and_recheck_without_a_subscription_always_download() {
        let fx = fixture(false).await;
        fx.core
            .db
            .write(|tx| {
                tx.execute(
                    "INSERT INTO source_requests(request_id,platform,kind,source_url,native_key,account_key,authentication,snapshot,created_at,expires_at) VALUES('sr-1','bilibili','entry','https://www.bilibili.com/video/BV1','BV1','7','anonymous','{}','t','t')",
                    [],
                )?;
                Ok(())
            })
            .await
            .unwrap();
        observe(
            &fx,
            "link",
            json!({"subscription_id": fx.subscription, "source_request_id": "sr-1", "source_request_generation": 1}),
        )
        .await;
        observe(
            &fx,
            "login",
            json!({"subscription_id": fx.subscription, "completion": true}),
        )
        .await;
        observe(&fx, "bare", json!({})).await;
        for key in ["link", "login", "bare"] {
            assert_eq!(deferred(&fx, key).await, 0, "{key}");
            assert_eq!(media_jobs(&fx, key).await, 2, "{key}");
        }
        assert_eq!(
            count(
                &fx.core,
                "SELECT count(*) FROM media_items WHERE fetch_state='deferred'"
            )
            .await,
            0
        );
    }
}
