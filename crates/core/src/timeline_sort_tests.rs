//! The timeline ordered by a counter or the length, filtered by tag and kind of content, and
//! the unified fields it and the entry view carry.
use crate::config::Profile;
use crate::core::Core;
use serde_json::{Value, json};
use std::sync::Arc;
use trace_contracts::control::Request;

const SEED: &str = "
INSERT INTO source_accounts(account_id,namespace,native_account_key,display_name,first_seen_at) VALUES('a','weibo','1','作者','t');
INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at,published_at) VALUES
 ('e1','weibo','post','P1','a','t','2026-03-01T00:00:00Z'),
 ('e2','weibo','post','P2','a','t','2026-03-02T00:00:00Z'),
 ('e3','weibo','post','P3','a','t','2026-03-03T00:00:00Z'),
 ('e4','weibo','post','P4','a','t',NULL),
 ('e5','weibo','post','P5','a','t','2026-03-05T00:00:00Z');
INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at,metadata_json) VALUES
 ('r1','e1','d1','一','正文','t','{}'),('r2','e2','d2','二','正文','t','{}'),('r3','e3','d3','三','正文','t','{}'),
 ('r4','e4','d4','四','正文','t','{}'),('r5','e5','d5','五','正文','t','{}');
INSERT INTO entry_meta(entry_id,site,content_kinds,image_count,video_count,max_duration_ms,plays,likes,comments,favorites,updated_at,canonical_url,ip_region,stats_observed_at) VALUES
 ('e1','weibo','[\"text\",\"image\"]',3,0,NULL,50,5,NULL,NULL,'t','https://weibo.com/1/P1','广东','2026-03-06T00:00:00Z'),
 ('e2','weibo','[\"text\",\"video\"]',0,1,90000,1000,2,7,1,'t',NULL,NULL,NULL),
 ('e3','weibo','[\"video\"]',0,1,5000,NULL,NULL,NULL,NULL,'t',NULL,NULL,NULL),
 ('e4','weibo','[\"text\"]',0,0,NULL,300,NULL,NULL,NULL,'t',NULL,NULL,NULL);
INSERT INTO entry_terms(entry_id,kind,value) VALUES
 ('e1','tag','旅行'),('e1','tag','摄影'),('e2','tag','旅行'),('e4','tag','美食'),('e1','mention','小明');";

async fn seeded() -> (Arc<Core>, tempfile::TempDir) {
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

async fn call(core: &Arc<Core>, command: &str, args: Value) -> Result<Value, String> {
    let result = crate::ipc::dispatch::dispatch(
        core,
        Request {
            protocol_version: trace_domain::CONTROL_PROTOCOL_VERSION.into(),
            request_id: uuid::Uuid::new_v4().to_string(),
            command: command.into(),
            idempotency_key: None,
            if_revision: None,
            args,
        },
    )
    .await;
    if result.ok {
        Ok(result.result.unwrap())
    } else {
        Err(format!("{:?}", result.error))
    }
}

fn titles(page: &Value) -> Vec<String> {
    page["items"]
        .as_array()
        .unwrap()
        .iter()
        .map(|i| i["title"].as_str().unwrap().to_owned())
        .collect()
}

#[tokio::test]
async fn the_default_order_is_still_newest_first_with_unknown_time_last() {
    let (core, _dir) = seeded().await;
    let page = call(&core, "archive.timeline", json!({})).await.unwrap();
    assert_eq!(titles(&page), ["五", "三", "二", "一", "四"]);
    assert!(
        page["items"][0].get("sort_key").is_none(),
        "an internal column is not an item field"
    );
    let explicit = call(&core, "archive.timeline", json!({"sort": "published"}))
        .await
        .unwrap();
    assert_eq!(titles(&explicit), titles(&page));
}

#[tokio::test]
async fn a_counter_orders_the_timeline_with_unknown_values_last_in_either_direction() {
    let (core, _dir) = seeded().await;
    let high = call(&core, "archive.timeline", json!({"sort": "plays"}))
        .await
        .unwrap();
    assert_eq!(
        titles(&high),
        ["二", "四", "一", "五", "三"],
        "most played first, never counted last"
    );
    let low = call(
        &core,
        "archive.timeline",
        json!({"sort": "plays", "oldest": true}),
    )
    .await
    .unwrap();
    assert_eq!(
        titles(&low),
        ["一", "四", "二", "三", "五"],
        "fewest first, never counted still last"
    );
    let by_duration = call(&core, "archive.timeline", json!({"sort": "duration"}))
        .await
        .unwrap();
    assert_eq!(&titles(&by_duration)[..2], ["二", "三"]);
    let by_likes = call(&core, "archive.timeline", json!({"sort": "likes"}))
        .await
        .unwrap();
    assert_eq!(&titles(&by_likes)[..2], ["一", "二"]);
    for sort in ["comments", "favorites"] {
        let page = call(&core, "archive.timeline", json!({"sort": sort}))
            .await
            .unwrap();
        assert_eq!(page["items"].as_array().unwrap().len(), 5, "{sort}");
    }
}

#[tokio::test]
async fn paging_a_counter_order_visits_every_entry_once_and_a_cursor_belongs_to_its_order() {
    let (core, _dir) = seeded().await;
    let mut seen = Vec::new();
    let mut cursor = Value::Null;
    loop {
        let page = call(
            &core,
            "archive.timeline",
            json!({"sort": "plays", "limit": 2, "cursor": cursor}),
        )
        .await
        .unwrap();
        seen.extend(titles(&page));
        match page["next_cursor"].as_str() {
            Some(next) => cursor = json!(next),
            None => break,
        }
    }
    assert_eq!(seen, ["二", "四", "一", "五", "三"]);
    let first = call(
        &core,
        "archive.timeline",
        json!({"sort": "plays", "limit": 2}),
    )
    .await
    .unwrap();
    let next = first["next_cursor"].as_str().unwrap();
    assert!(
        call(
            &core,
            "archive.timeline",
            json!({"limit": 2, "cursor": next})
        )
        .await
        .is_err(),
        "another order"
    );
    assert!(
        call(
            &core,
            "archive.timeline",
            json!({"sort": "likes", "limit": 2, "cursor": next})
        )
        .await
        .is_err()
    );
    assert!(
        call(
            &core,
            "archive.timeline",
            json!({"sort": "bogus; DROP TABLE entries"})
        )
        .await
        .is_err()
    );
}

#[tokio::test]
async fn tag_and_kind_of_content_filter_the_timeline_and_its_counts() {
    let (core, _dir) = seeded().await;
    let tagged = call(&core, "archive.timeline", json!({"tag": "旅行"}))
        .await
        .unwrap();
    assert_eq!(titles(&tagged), ["二", "一"]);
    assert_eq!(tagged["counts"]["filtered"], 2);
    let hashed = call(&core, "archive.timeline", json!({"tag": "#摄影"}))
        .await
        .unwrap();
    assert_eq!(titles(&hashed), ["一"]);
    assert_eq!(
        call(&core, "archive.timeline", json!({"tag": "没有"}))
            .await
            .unwrap()["items"],
        json!([])
    );
    let video = call(&core, "archive.timeline", json!({"content_kind": "video"}))
        .await
        .unwrap();
    assert_eq!(titles(&video), ["三", "二"]);
    let both = call(
        &core,
        "archive.timeline",
        json!({"content_kind": "video", "tag": "旅行", "sort": "plays"}),
    )
    .await
    .unwrap();
    assert_eq!(titles(&both), ["二"]);
    let text = call(&core, "archive.timeline", json!({"content_kind": "text"}))
        .await
        .unwrap();
    assert_eq!(titles(&text).len(), 3);
}

#[tokio::test]
async fn items_and_the_entry_view_carry_the_unified_fields() {
    let (core, _dir) = seeded().await;
    let page = call(&core, "archive.timeline", json!({"tag": "摄影"}))
        .await
        .unwrap();
    let meta = &page["items"][0]["meta"];
    assert_eq!(meta["site"], "weibo");
    assert_eq!(meta["content_kinds"], json!(["text", "image"]));
    assert_eq!(
        (meta["image_count"].as_i64(), meta["video_count"].as_i64()),
        (Some(3), Some(0))
    );
    assert_eq!(meta["tags"], json!(["摄影", "旅行"]));
    assert_eq!(meta["mentions"], json!(["小明"]));
    assert_eq!(meta["ip_region"], "广东");
    assert_eq!(meta["canonical_url"], "https://weibo.com/1/P1");
    assert_eq!(meta["relation"], "original");
    assert_eq!(meta["stats"]["plays"], 50);
    assert_eq!(
        meta["stats"]["comments"],
        Value::Null,
        "a counter never reported is not zero"
    );
    assert_eq!(meta["stats"]["observed_at"], "2026-03-06T00:00:00Z");
    let entry = call(&core, "archive.entry.get", json!({"entry_id": "e2"}))
        .await
        .unwrap();
    assert_eq!(entry["meta"]["duration_ms"], 90000);
    assert_eq!(entry["meta"]["stats"]["favorites"], 1);
    let none = call(&core, "archive.entry.get", json!({"entry_id": "e5"}))
        .await
        .unwrap();
    assert_eq!(
        none["meta"],
        Value::Null,
        "no unified record before one was observed"
    );
}

#[tokio::test]
async fn tags_come_most_used_first_within_the_scope() {
    let (core, _dir) = seeded().await;
    let all = call(&core, "archive.tags", json!({})).await.unwrap();
    assert_eq!(
        all,
        json!([{"tag":"旅行","entries":2},{"tag":"摄影","entries":1},{"tag":"美食","entries":1}])
    );
    let prefix = call(&core, "archive.tags", json!({"prefix": "#旅"}))
        .await
        .unwrap();
    assert_eq!(prefix, json!([{"tag":"旅行","entries":2}]));
    let limited = call(&core, "archive.tags", json!({"limit": 1}))
        .await
        .unwrap();
    assert_eq!(limited.as_array().unwrap().len(), 1);
    let other_site = call(&core, "archive.tags", json!({"namespace": "bilibili"}))
        .await
        .unwrap();
    assert_eq!(other_site, json!([]));
    let account = call(
        &core,
        "archive.tags",
        json!({"account_id": "a", "prefix": "美"}),
    )
    .await
    .unwrap();
    assert_eq!(account, json!([{"tag":"美食","entries":1}]));
    assert!(
        call(&core, "archive.tags", json!({"unknown": 1}))
            .await
            .is_err()
    );
}
