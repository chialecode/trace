//! Unified metadata persistence: field mapping, empty values, counters that never create a
//! content version, and nothing secret in what is stored.
use crate::config::Profile;
use crate::core::Core;
use crate::jobs::{self, ClaimedJob};
use crate::pipeline::observe::persist_detail;
use crate::store;
use serde_json::{Value, json};
use trace_contracts::content::Document;
use trace_contracts::plugin::{ObservedDetail, ResourceIdentityDto};
use trace_contracts::unified::{Relation, StatCounters, UnifiedMetadata};

const SEED: &str = "
INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('a','weibo','1','t');
INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES('e','weibo','post','P1','a','t');
INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES('f','weibo','post','P2','a','t');";

async fn core_with_entries() -> (std::sync::Arc<Core>, tempfile::TempDir) {
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
            tx.execute_batch(SEED)?;
            Ok(())
        })
        .await
        .unwrap();
    (core, dir)
}

async fn running_job(core: &Core, key: &str) -> ClaimedJob {
    jobs::enqueue(&core.db, key.into(), "observe", "e", json!({}), 1, 1, 0)
        .await
        .unwrap();
    let job = jobs::claim_due(&core.db, "unified-test", 1)
        .await
        .unwrap()
        .remove(0);
    job.mark_running(&core.db).await.unwrap();
    job
}

fn resource(role: &str, key: &str, position: i64) -> ResourceIdentityDto {
    ResourceIdentityDto {
        role: role.into(),
        source_attachment_id: key.into(),
        position,
        title: None,
        group_key: None,
        required: role != "cover",
    }
}

fn detail(
    key: &str,
    completeness: &str,
    unified: Option<UnifiedMetadata>,
    stats: Value,
) -> ObservedDetail {
    ObservedDetail {
        document: Document::default(),
        metadata: json!({}),
        native_entry_key: key.into(),
        native_kind: "post".into(),
        title: "标题".into(),
        normalized_body: "正文 #旅行".into(),
        published_at: None,
        published_precision: "unknown".into(),
        access_state: "visible".into(),
        source_version_evidence: json!({}),
        resources: vec![
            resource("image", "i1", 0),
            resource("image", "i2", 1),
            resource("video", "v1", 2),
            resource("cover", "c1", 3),
        ],
        completeness: completeness.into(),
        stats,
        unified,
    }
}

fn mapped() -> UnifiedMetadata {
    UnifiedMetadata {
        canonical_url: Some("https://weibo.com/1/AbC".into()),
        relation: Relation::Quote,
        related_key: Some("P0".into()),
        tags: vec!["#旅行".into(), "摄影".into()],
        mentions: vec!["@小明".into()],
        language: Some("zh".into()),
        client: Some("iPhone".into()),
        ip_region: Some("北京".into()),
        max_duration_ms: Some(61_000),
        max_width: Some(1920),
        max_height: Some(1080),
        stats: StatCounters {
            likes: Some(10),
            comments: Some(2),
            ..Default::default()
        },
        ..Default::default()
    }
}

#[tokio::test]
async fn mapped_fields_are_stored_derived_counts_win_and_missing_stays_empty() {
    let (core, _dir) = core_with_entries().await;
    let job = running_job(&core, "unified-1").await;
    persist_detail(
        &core,
        &job,
        "e",
        "a",
        detail("P1", "full", Some(mapped()), json!({})),
    )
    .await
    .unwrap();
    let stored = core
        .db
        .read(|c| store::meta::load(c, "e"))
        .await
        .unwrap()
        .expect("metadata is stored with the revision");
    assert_eq!(stored.site, "weibo");
    assert_eq!(
        (stored.images, stored.videos),
        (2, 1),
        "counts come from the observed resources"
    );
    let u = stored.unified;
    assert_eq!(u.canonical_url.as_deref(), Some("https://weibo.com/1/AbC"));
    assert_eq!(
        (u.relation, u.related_key.as_deref()),
        (Relation::Quote, Some("P0"))
    );
    assert_eq!(u.tags, ["摄影", "旅行"]);
    assert_eq!(u.mentions, ["小明"]);
    assert_eq!(u.content_kinds, ["image", "text", "video"]);
    assert_eq!(u.cover.as_deref(), Some("c1"));
    assert_eq!(
        (u.max_duration_ms, u.max_width, u.max_height),
        (Some(61_000), Some(1920), Some(1080))
    );
    assert_eq!((u.stats.likes, u.stats.comments), (Some(10), Some(2)));
    assert_eq!(
        u.stats.plays, None,
        "a counter the site never gave is not invented"
    );
    assert_eq!(
        (u.location_name, u.collection, u.visibility),
        (None, None, None)
    );
}

#[tokio::test]
async fn a_changed_counter_is_an_observation_not_a_new_content_version() {
    let (core, _dir) = core_with_entries().await;
    let job = running_job(&core, "unified-2").await;
    persist_detail(
        &core,
        &job,
        "e",
        "a",
        detail("P1", "full", Some(mapped()), json!({})),
    )
    .await
    .unwrap();
    let mut later = mapped();
    later.stats = StatCounters {
        likes: Some(99),
        plays: Some(5000),
        ..Default::default()
    };
    persist_detail(
        &core,
        &job,
        "e",
        "a",
        detail("P1", "full", Some(later), json!({})),
    )
    .await
    .unwrap();
    persist_detail(
        &core,
        &job,
        "e",
        "a",
        detail("P1", "full", Some(mapped_same_as_later()), json!({})),
    )
    .await
    .unwrap();
    let (revisions, history, stored) = core
        .db
        .read(|c| {
            let revisions: i64 = c.query_row(
                "SELECT count(*) FROM entry_revisions WHERE entry_id='e'",
                [],
                |r| r.get(0),
            )?;
            Ok((
                revisions,
                store::meta::stat_history(c, "e")?,
                store::meta::load(c, "e")?.unwrap(),
            ))
        })
        .await
        .unwrap();
    assert_eq!(revisions, 1, "counters never create a revision");
    assert_eq!(history.len(), 2, "an identical observation writes nothing");
    assert_eq!(history[0].1.likes, Some(10));
    let stats = stored.unified.stats;
    assert_eq!(
        (stats.likes, stats.plays, stats.comments),
        (Some(99), Some(5000), Some(2))
    );
}

fn mapped_same_as_later() -> UnifiedMetadata {
    let mut same = mapped();
    same.stats = StatCounters {
        likes: Some(99),
        plays: Some(5000),
        ..Default::default()
    };
    same
}

#[tokio::test]
async fn a_summary_never_replaces_the_content_fields_of_a_full_revision() {
    let (core, _dir) = core_with_entries().await;
    let job = running_job(&core, "unified-3").await;
    persist_detail(
        &core,
        &job,
        "e",
        "a",
        detail("P1", "full", Some(mapped()), json!({})),
    )
    .await
    .unwrap();
    let mut summary = detail(
        "P1",
        "summary",
        Some(UnifiedMetadata::default()),
        json!({"likes": 77}),
    );
    summary.resources.clear();
    persist_detail(&core, &job, "e", "a", summary)
        .await
        .unwrap();
    let stored = core
        .db
        .read(|c| store::meta::load(c, "e"))
        .await
        .unwrap()
        .unwrap();
    assert_eq!(stored.unified.tags, ["摄影", "旅行"]);
    assert_eq!(stored.videos, 1);
    assert_eq!(
        stored.unified.stats.likes,
        Some(77),
        "but the counters it saw are recorded"
    );
}

#[tokio::test]
async fn legacy_adapters_without_a_mapping_still_get_counts_and_counters() {
    let (core, _dir) = core_with_entries().await;
    let job = running_job(&core, "unified-4").await;
    persist_detail(
        &core,
        &job,
        "f",
        "a",
        detail(
            "P2",
            "full",
            None,
            json!({"view": 12, "reply": 3, "token": "Bearer abc"}),
        ),
    )
    .await
    .unwrap();
    let stored = core
        .db
        .read(|c| store::meta::load(c, "f"))
        .await
        .unwrap()
        .unwrap();
    assert_eq!(stored.unified.stats.plays, Some(12));
    assert_eq!(stored.unified.stats.comments, Some(3));
    assert_eq!(stored.unified.canonical_url, None);
    assert_eq!(stored.images, 2);
}

#[tokio::test]
async fn foreign_links_and_secrets_do_not_reach_the_stored_metadata() {
    let (core, _dir) = core_with_entries().await;
    let job = running_job(&core, "unified-5").await;
    let mut hostile = mapped();
    hostile.canonical_url = Some("https://evil.example/1/a".into());
    hostile.client = Some("line1\nline2".into());
    persist_detail(
        &core,
        &job,
        "e",
        "a",
        detail("P1", "full", Some(hostile), json!({})),
    )
    .await
    .unwrap();
    let dump = core
        .db
        .read(|c| {
            Ok(c.query_row(
                "SELECT json_object('url',canonical_url,'client',client) FROM entry_meta WHERE entry_id='e'",
                [],
                |r| r.get::<_, String>(0),
            )?)
        })
        .await
        .unwrap();
    let value: Value = serde_json::from_str(&dump).unwrap();
    assert!(value["url"].is_null());
    assert!(value["client"].is_null());
}

async fn is_pinned(core: &Core) -> bool {
    core.db
        .read(|c| {
            Ok(c.query_row(
                "SELECT pinned FROM entry_meta WHERE entry_id='e'",
                [],
                |r| r.get::<_, bool>(0),
            )?)
        })
        .await
        .unwrap()
}

#[tokio::test]
async fn listing_pinned_state_survives_a_detail_observation_and_can_be_cleared() {
    let (core, _dir) = core_with_entries().await;
    let job = running_job(&core, "unified-6").await;
    core.db
        .write(|tx| {
            store::meta::record_listing(tx, "e", "weibo", true, &StatCounters::default(), "t")
        })
        .await
        .unwrap();
    persist_detail(
        &core,
        &job,
        "e",
        "a",
        detail("P1", "full", Some(mapped()), json!({})),
    )
    .await
    .unwrap();
    assert!(is_pinned(&core).await);
    core.db
        .write(|tx| {
            store::meta::record_listing(tx, "e", "weibo", false, &StatCounters::default(), "t2")
        })
        .await
        .unwrap();
    assert!(!is_pinned(&core).await);
}
