//! Entries whose list line gave no exact date are placed against the subscription's range by
//! their details (multi-site contracts §5.3), below the adapter: what is stored, what is
//! queued and what the subscription counts as found.
use crate::config::Profile;
use crate::core::Core;
use crate::jobs::{self, ClaimedJob};
use crate::pipeline::observe::persist_detail;
use crate::pipeline::scan_page::{ScanPage, Stored, store_page};
use crate::scan_range::Range;
use serde_json::{Value, json};
use std::sync::Arc;
use trace_contracts::content::Document;
use trace_contracts::control::Request;
use trace_contracts::plugin::{EnumeratedEntry, ObservedDetail, ResourceIdentityDto};

struct Fx {
    _dir: tempfile::TempDir,
    core: Arc<Core>,
    subscription: String,
}

async fn call(core: &Arc<Core>, command: &str, args: Value) -> Value {
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
    .unwrap()
}

async fn fixture(range: Value) -> Fx {
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
        json!({"display_name": "作者", "aliases": [], "tags": [], "notes": ""}),
    )
    .await;
    let account = call(
        &core,
        "account.bind",
        json!({"creator_id": creator["creator_id"], "namespace": "bilibili", "native_account_key": "7"}),
    )
    .await;
    let subscription = call(
        &core,
        "subscription.create",
        json!({"account_id": account["account_id"], "content_type": "video",
               "range": range, "auto_download": true}),
    )
    .await;
    Fx {
        _dir: dir,
        core,
        subscription: subscription["subscription_id"].as_str().unwrap().into(),
    }
}

fn days_ago(days: i64) -> String {
    (time::OffsetDateTime::now_utc() - time::Duration::days(days))
        .format(&time::format_description::well_known::Rfc3339)
        .unwrap()
}

fn listed(
    key: &str,
    published_at: Option<String>,
    precision: &str,
    margin: Option<i64>,
) -> EnumeratedEntry {
    EnumeratedEntry {
        native_entry_key: key.into(),
        native_kind: "video".into(),
        title: key.into(),
        published_at,
        published_precision: precision.into(),
        published_margin_seconds: margin,
        pinned: false,
        summary_body: None,
        stats: Value::Null,
        hints: None,
    }
}

async fn the_range(fx: &Fx) -> Range {
    let id = fx.subscription.clone();
    let (range, created): (String, String) = fx
        .core
        .db
        .read(move |c| {
            Ok(c.query_row(
                "SELECT range_json, created_at FROM subscriptions WHERE subscription_id=?1",
                [id],
                |r| Ok((r.get(0)?, r.get(1)?)),
            )?)
        })
        .await
        .unwrap();
    Range::parse(&serde_json::from_str(&range).unwrap(), &created).unwrap()
}

/// A job as the pipeline holds it while it works: leased and running.
async fn running(fx: &Fx, kind: &str, subject: &str, payload: Value) -> ClaimedJob {
    let id = jobs::enqueue(
        &fx.core.db,
        format!("{kind}-{}", uuid::Uuid::new_v4()),
        kind,
        subject,
        payload,
        1,
        1,
        0,
    )
    .await
    .unwrap();
    let job = jobs::claim_due(&fx.core.db, "undated", 500)
        .await
        .unwrap()
        .into_iter()
        .find(|j| j.job_id == id)
        .expect("the job is claimed");
    job.mark_running(&fx.core.db).await.unwrap();
    job
}

async fn store(fx: &Fx, items: Vec<EnumeratedEntry>) -> Stored {
    let job = running(
        fx,
        "scan",
        &fx.subscription,
        json!({"subscription_id": fx.subscription}),
    )
    .await;
    let range = the_range(fx).await;
    store_page(
        &fx.core,
        &job,
        ScanPage {
            namespace: "bilibili",
            subscription_id: &fx.subscription,
            mode: "incremental",
            items,
            cursor: None,
            coverage: &json!({}),
            range: &range,
        },
    )
    .await
    .unwrap()
}

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

fn detail(key: &str, published_at: Option<String>, precision: &str) -> ObservedDetail {
    ObservedDetail {
        document: Document::default(),
        metadata: json!({}),
        native_entry_key: key.into(),
        native_kind: "video".into(),
        title: "标题".into(),
        normalized_body: "正文".into(),
        published_at,
        published_precision: precision.into(),
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

/// One observation of `key`, as the pipeline runs it for `payload`.
async fn observe(fx: &Fx, key: &str, payload: Value, detail: ObservedDetail) {
    let key = key.to_owned();
    let (entry, account): (String, String) = fx
        .core
        .db
        .read(move |c| {
            Ok(c.query_row(
                "SELECT entry_id, account_id FROM entries WHERE native_entry_key=?1",
                [key],
                |r| Ok((r.get(0)?, r.get(1)?)),
            )?)
        })
        .await
        .unwrap();
    let mut payload = payload;
    payload["entry_id"] = json!(entry);
    let job = running(fx, "observe", &entry, payload).await;
    persist_detail(&fx.core, &job, &entry, &account, detail)
        .await
        .unwrap();
}

async fn number(fx: &Fx, sql: &str, key: &str) -> i64 {
    let (sql, key) = (sql.to_owned(), key.to_owned());
    fx.core
        .db
        .read(move |c| Ok(c.query_row(&sql, [key], |r| r.get::<_, i64>(0))?))
        .await
        .unwrap()
}

const FOUND: &str = "SELECT count(*) FROM entry_subscriptions d JOIN entries e ON e.entry_id=d.entry_id WHERE e.native_entry_key=?1";
const OBSERVE_JOBS: &str = "SELECT count(*) FROM jobs WHERE job_type='observe' AND json_extract(payload,'$.entry_id')=(SELECT entry_id FROM entries WHERE native_entry_key=?1) AND job_key LIKE 'observe:%'";
const OBSERVE_REQUESTS: &str = "SELECT COALESCE(SUM(c.requested),0) FROM job_consumers c JOIN jobs j ON j.job_id=c.job_id WHERE j.job_type='observe' AND json_extract(j.payload,'$.entry_id')=(SELECT entry_id FROM entries WHERE native_entry_key=?1)";
const MEDIA_JOBS: &str = "SELECT count(*) FROM jobs WHERE job_type='media' AND subject_id=(SELECT entry_id FROM entries WHERE native_entry_key=?1)";
const DEFERRED: &str = "SELECT count(*) FROM media_items m JOIN entry_revisions r ON r.revision_id=m.revision_id JOIN entries e ON e.entry_id=r.entry_id WHERE e.native_entry_key=?1 AND m.fetch_state='deferred'";

#[tokio::test]
async fn a_repeat_incremental_check_does_not_fetch_a_known_undated_detail_again() {
    let fx = fixture(json!({"kind": "recent_days", "days": 7})).await;
    let first = store(&fx, vec![listed("known", None, "unknown", None)]).await;
    assert_eq!(
        first,
        Stored {
            added: 0,
            unplaced: 1
        }
    );
    assert_eq!(number(&fx, OBSERVE_REQUESTS, "known").await, 1);
    observe(
        &fx,
        "known",
        json!({"subscription_id": fx.subscription}),
        detail("known", Some(days_ago(1)), "source"),
    )
    .await;
    // The detail job finished before the next check.
    fx.core
        .db
        .write(|tx| {
            tx.execute(
                "UPDATE job_consumers SET completed=requested,state='succeeded' WHERE job_id IN (SELECT job_id FROM jobs WHERE job_type='observe')",
                [],
            )?;
            tx.execute(
                "UPDATE jobs SET state='succeeded' WHERE job_type='observe'",
                [],
            )?;
            Ok(())
        })
        .await
        .unwrap();
    let second = store(&fx, vec![listed("known", None, "unknown", None)]).await;
    assert_eq!(
        second,
        Stored {
            added: 0,
            unplaced: 0
        },
        "the known entry is not counted as new"
    );
    assert_eq!(
        number(&fx, OBSERVE_REQUESTS, "known").await,
        1,
        "a repeat check reads the list, not the same detail again"
    );
}

#[tokio::test]
async fn an_entry_the_details_put_outside_is_neither_found_nor_observed_again() {
    let fx = fixture(json!({"kind": "recent_days", "days": 7})).await;
    let first = store(&fx, vec![listed("old", None, "unknown", None)]).await;
    assert_eq!(
        first,
        Stored {
            added: 0,
            unplaced: 1
        }
    );
    assert_eq!(number(&fx, FOUND, "old").await, 1);
    observe(
        &fx,
        "old",
        json!({"subscription_id": fx.subscription}),
        detail("old", Some(days_ago(60)), "source"),
    )
    .await;
    assert_eq!(
        number(&fx, FOUND, "old").await,
        0,
        "outside: no longer a find"
    );
    assert_eq!(number(&fx, MEDIA_JOBS, "old").await, 0);
    assert_eq!(number(&fx, DEFERRED, "old").await, 3);

    // The next scan sees the same line without a date: it is left alone this time.
    let jobs_before = number(&fx, OBSERVE_JOBS, "old").await;
    let again = store(&fx, vec![listed("old", None, "unknown", None)]).await;
    assert_eq!(again, Stored::default());
    assert_eq!(number(&fx, FOUND, "old").await, 0);
    assert_eq!(number(&fx, OBSERVE_JOBS, "old").await, jobs_before);
    fx.core.shutdown().await;
}

#[tokio::test]
async fn a_download_somebody_asked_for_is_never_held_to_the_range() {
    let fx = fixture(json!({"kind": "recent_days", "days": 7})).await;
    store(&fx, vec![listed("old", None, "unknown", None)]).await;
    // A preview selection (or a link) fetches the work whatever its date says.
    observe(
        &fx,
        "old",
        json!({"subscription_id": fx.subscription, "delivery_id": "dx"}),
        detail("old", Some(days_ago(60)), "source"),
    )
    .await;
    assert_eq!(number(&fx, FOUND, "old").await, 1);
    assert_eq!(number(&fx, MEDIA_JOBS, "old").await, 2);
    assert_eq!(number(&fx, DEFERRED, "old").await, 0);
    fx.core.shutdown().await;
}

#[tokio::test]
async fn a_range_that_asks_nothing_of_the_date_takes_undated_works_as_they_come() {
    let fx = fixture(json!({"kind": "all"})).await;
    let stored = store(&fx, vec![listed("any", None, "unknown", None)]).await;
    assert_eq!(
        stored,
        Stored {
            added: 1,
            unplaced: 0
        }
    );
    observe(
        &fx,
        "any",
        json!({"subscription_id": fx.subscription}),
        detail("any", None, "unknown"),
    )
    .await;
    assert_eq!(number(&fx, DEFERRED, "any").await, 0);
    assert_eq!(number(&fx, MEDIA_JOBS, "any").await, 2);
    fx.core.shutdown().await;
}

#[tokio::test]
async fn a_list_date_that_is_approximate_stays_approximate_until_the_source_gives_the_time() {
    let fx = fixture(json!({"kind": "recent_days", "days": 7})).await;
    // "3 days ago" with a day of margin: inside the week for certain, stored as a guess.
    let line = listed("near", Some(days_ago(3)), "approximate", Some(86_400));
    assert_eq!(
        store(&fx, vec![line]).await,
        Stored {
            added: 1,
            unplaced: 0
        }
    );
    let stored = |key: &'static str| {
        let fx = &fx;
        async move {
            let key = key.to_owned();
            fx.core
                .db
                .read(move |c| {
                    Ok(c.query_row(
                        "SELECT published_precision FROM entries WHERE native_entry_key=?1",
                        [key],
                        |r| r.get::<_, String>(0),
                    )?)
                })
                .await
                .unwrap()
        }
    };
    assert_eq!(stored("near").await, "approximate");
    // A detail that only repeats a guess changes nothing about an exact date; here there is none
    // yet, so the guess is replaced by the time the source states.
    let exact = days_ago(3);
    observe(
        &fx,
        "near",
        json!({"subscription_id": fx.subscription}),
        detail("near", Some(exact.clone()), "source"),
    )
    .await;
    assert_eq!(stored("near").await, "source");
    observe(
        &fx,
        "near",
        json!({"subscription_id": fx.subscription}),
        detail("near", Some(days_ago(30)), "approximate"),
    )
    .await;
    let date: String = fx
        .core
        .db
        .read(|c| {
            Ok(c.query_row(
                "SELECT published_at FROM entries WHERE native_entry_key='near'",
                [],
                |r| r.get(0),
            )?)
        })
        .await
        .unwrap();
    assert_eq!(date, exact, "a guess never replaces the source's time");

    // "30 days ago" is outside the week by more than its margin: never stored at all.
    let far = listed("far", Some(days_ago(30)), "approximate", Some(86_400));
    let kept = crate::pipeline::scan::narrowed(&the_range(&fx).await, &[far]);
    assert!(kept.is_empty());
    fx.core.shutdown().await;
}

#[tokio::test]
async fn a_day_precise_date_is_placed_with_a_day_of_margin() {
    let fx = fixture(json!({"kind": "recent_days", "days": 7})).await;
    store(&fx, vec![listed("day", None, "unknown", None)]).await;
    // The site gave the day only: at midnight UTC it reads as just before the range starts
    // (the range began 7 days ago at this hour), yet the work may well have come that day.
    let midnight = {
        let at = time::OffsetDateTime::now_utc() - time::Duration::days(7);
        at.replace_time(time::Time::MIDNIGHT)
            .format(&time::format_description::well_known::Rfc3339)
            .unwrap()
    };
    observe(
        &fx,
        "day",
        json!({"subscription_id": fx.subscription}),
        detail("day", Some(midnight), "day"),
    )
    .await;
    assert_eq!(
        number(&fx, FOUND, "day").await,
        1,
        "not placed outside on a guess"
    );
    assert_eq!(
        number(&fx, DEFERRED, "day").await,
        2,
        "but not placed inside either"
    );
    fx.core.shutdown().await;
}

#[tokio::test]
async fn a_list_without_any_date_is_capped_only_for_a_range_that_follows_the_present() {
    use crate::pipeline::scan::undated_cap_reached;
    use trace_contracts::plugin::EnumeratedPage;
    let fx = fixture(json!({"kind": "recent_days", "days": 7})).await;
    let recent = the_range(&fx).await;
    let page = |lines: Vec<EnumeratedEntry>, more: bool| EnumeratedPage {
        items: lines,
        next_cursor: more.then(|| "next".into()),
        has_more: more,
        coverage: json!({}),
    };
    let undated = || {
        vec![
            listed("a", None, "unknown", None),
            listed("b", None, "unknown", None),
        ]
    };
    let reached = |range: Range, page: EnumeratedPage| {
        let (fx, subscription) = (&fx, fx.subscription.clone());
        async move {
            undated_cap_reached(
                &fx.core,
                (subscription.as_str(), "incremental", &range),
                &page,
            )
            .await
            .unwrap()
        }
    };
    // Four pages of the sweep are behind it: the fifth is the last one read.
    for cursor in ["c1", "c2", "c3", "c4"] {
        let (id, visited) = (fx.subscription.clone(), cursor.to_owned());
        fx.core
            .db
            .write(move |tx| {
                crate::scan_range::record_cursor(tx, &id, "incremental", Some(&visited))?;
                Ok(())
            })
            .await
            .unwrap();
        let expected = (cursor == "c4").then_some(5);
        assert_eq!(reached(recent, page(undated(), true)).await, expected);
    }
    // A line with a date (even an approximate one) is the adapter's to cut the list by.
    let dated = vec![
        listed("a", None, "unknown", None),
        listed("b", Some(days_ago(1)), "approximate", Some(86_400)),
    ];
    assert_eq!(reached(recent, page(dated, true)).await, None);
    // The end of a list needs no cap, and neither does a range that does not follow the present.
    assert_eq!(reached(recent, page(undated(), false)).await, None);
    let all = Range {
        start: None,
        end: None,
    };
    assert_eq!(reached(all, page(undated(), true)).await, None);
    let closed = Range::parse(
        &json!({"kind": "date_range", "start": "2026-01-01T00:00:00Z", "end": "2026-02-01T00:00:00Z"}),
        "2026-10-18T00:00:00Z",
    )
    .unwrap();
    assert_eq!(reached(closed, page(undated(), true)).await, None);
    fx.core.shutdown().await;
}
