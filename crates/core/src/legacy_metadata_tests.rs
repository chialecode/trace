//! What the unified fields say about entries that Bilibili and Douyin saved as their own
//! records, shaped like what those adapters store.
use super::*;
use crate::config::Profile;
use crate::jobs;
use crate::pipeline::observe::persist_detail;
use serde_json::json;
use trace_contracts::content::Document;
use trace_contracts::plugin::ObservedDetail;

fn unified(site: &str, kind: &str, key: &str, metadata: Value, stats: Value) -> UnifiedMetadata {
    project(site, kind, key, &metadata, &stats).expect("these two sites are projected")
}

#[test]
fn a_bilibili_video_gives_its_link_length_size_category_collection_and_counters() {
    let u = unified(
        "bilibili",
        "video",
        "BV1xx411c7mD",
        json!({
            "bvid": "BV1xx411c7mD",
            "duration": 3723,
            "dimension": {"width": 1920, "height": 1080, "rotate": 0},
            "pages": [
                {"duration": 3000, "dimension": {"width": 1280, "height": 720, "rotate": 0}},
                {"duration": 3723, "dimension": {"width": 1920, "height": 1080, "rotate": 0}}
            ],
            "tname": "科技",
            "ugc_season": {"title": "年度合集", "id": 7}
        }),
        json!({"view": 1200, "like": 80, "coin": 20, "favorite": 30, "share": 4, "reply": 9, "danmaku": 55}),
    );
    assert_eq!(
        u.canonical_url.as_deref(),
        Some("https://www.bilibili.com/video/BV1xx411c7mD")
    );
    assert_eq!(u.max_duration_ms, Some(3_723_000));
    assert_eq!((u.max_width, u.max_height), (Some(1920), Some(1080)));
    assert_eq!(u.category.as_deref(), Some("科技"));
    assert_eq!(u.collection.as_deref(), Some("年度合集"));
    assert_eq!(
        u.stats,
        StatCounters {
            plays: Some(1200),
            likes: Some(80),
            comments: Some(9),
            shares: Some(4),
            favorites: Some(30),
            coins: Some(20),
            danmaku: Some(55),
        }
    );
    assert_eq!(u.relation, Relation::Original);
}

#[test]
fn a_rotated_bilibili_video_swaps_width_and_height_and_a_missing_size_stays_empty() {
    let rotated = unified(
        "bilibili",
        "video",
        "BV1",
        json!({"duration": 60, "dimension": {"width": 1920, "height": 1080, "rotate": 1}}),
        json!({}),
    );
    assert_eq!(
        (rotated.max_width, rotated.max_height),
        (Some(1080), Some(1920))
    );
    let bare = unified("bilibili", "video", "BV2", json!({}), json!({}));
    assert_eq!(
        (bare.max_duration_ms, bare.max_width, bare.max_height),
        (None, None, None),
        "nothing is invented"
    );
    assert!(bare.stats.is_empty());
}

#[test]
fn a_bilibili_article_gives_its_link_kind_category_and_tags() {
    let u = unified(
        "bilibili",
        "article",
        "7654321",
        json!({"category": {"name": "日常"}, "tags": [{"name": "随笔"}, {"name": "#生活"}, {"name": " "}]}),
        json!({"view": 10}),
    );
    assert_eq!(
        u.canonical_url.as_deref(),
        Some("https://www.bilibili.com/read/cv7654321")
    );
    assert_eq!(u.content_kinds, ["article"]);
    assert_eq!(u.category.as_deref(), Some("日常"));
    assert_eq!(u.tags, ["随笔", "生活"]);
    assert_eq!(u.stats.plays, Some(10));
}

#[test]
fn a_bilibili_forwarded_dynamic_names_its_origin_region_topics_and_mentions() {
    let metadata = json!({"item": {
        "type": "DYNAMIC_TYPE_FORWARD",
        "orig": {"id_str": "998877"},
        "modules": {
            "module_author": {"pub_location_text": "IP属地：广东"},
            "module_dynamic": {
                "desc": {"rich_text_nodes": [
                    {"type": "RICH_TEXT_NODE_TYPE_TEXT", "text": "转发"},
                    {"type": "RICH_TEXT_NODE_TYPE_TOPIC", "text": "#打卡#"},
                    {"type": "RICH_TEXT_NODE_TYPE_AT", "text": "@某人"}
                ]},
                "topic": {"name": "日常"}
            }
        }
    }});
    let u = unified(
        "bilibili",
        "dynamic",
        "123456",
        metadata,
        json!({"like": 3}),
    );
    assert_eq!(
        u.canonical_url.as_deref(),
        Some("https://t.bilibili.com/123456")
    );
    assert_eq!(u.ip_region.as_deref(), Some("广东"));
    assert_eq!(
        (u.relation, u.related_key.as_deref()),
        (Relation::Repost, Some("998877"))
    );
    assert_eq!(u.mentions, ["某人"]);
    assert!(u.tags.contains(&"日常".to_owned()) && u.tags.iter().any(|t| t.contains("打卡")));
    assert_eq!(u.stats.likes, Some(3));
}

#[test]
fn a_bilibili_opus_in_the_module_list_shape_reads_its_region() {
    let u = unified(
        "bilibili",
        "opus",
        "555",
        json!({"item": {"modules": [
            {"module_type": "MODULE_TYPE_TITLE"},
            {"module_author": {"pub_location_text": "IP属地：上海"}}
        ]}}),
        json!({}),
    );
    assert_eq!(
        u.canonical_url.as_deref(),
        Some("https://www.bilibili.com/opus/555")
    );
    assert_eq!(u.ip_region.as_deref(), Some("上海"));
    assert_eq!(u.relation, Relation::Original);
}

#[test]
fn a_douyin_video_gives_its_link_size_region_collection_topics_place_and_pin() {
    let u = unified(
        "douyin",
        "video",
        "7311111111111111111",
        json!({
            "video": {"duration": 22833, "width": 1080, "height": 1920},
            "ip_label": "IP属地：浙江",
            "mix_info": {"mix_name": "探店合集"},
            "is_top": 1,
            "is_ads": false,
            "text_extra": [{"hashtag_name": "美食", "type": 1}, {"type": 0}, {"hashtag_name": " "}],
            "cha_list": [{"cha_name": "美食"}, {"cha_name": "探店"}],
            "poi_info": {"poi_name": "某某路"},
            "video_tag": [{"tag_name": "生活"}, {"tag_name": "美食"}]
        }),
        json!({"digg_count": 5, "comment_count": 2, "share_count": 1, "collect_count": 3, "play_count": 40}),
    );
    assert_eq!(
        u.canonical_url.as_deref(),
        Some("https://www.douyin.com/video/7311111111111111111")
    );
    assert_eq!(u.max_duration_ms, Some(22_833));
    assert_eq!((u.max_width, u.max_height), (Some(1080), Some(1920)));
    assert_eq!(u.ip_region.as_deref(), Some("浙江"));
    assert_eq!(u.collection.as_deref(), Some("探店合集"));
    assert_eq!(u.location_name.as_deref(), Some("某某路"));
    assert_eq!(u.category.as_deref(), Some("生活"));
    assert_eq!(u.tags, ["美食", "探店"]);
    assert!(u.pinned);
    assert!(!u.sponsored);
    assert_eq!(
        u.stats,
        StatCounters {
            plays: Some(40),
            likes: Some(5),
            comments: Some(2),
            shares: Some(1),
            favorites: Some(3),
            ..Default::default()
        }
    );
}

#[test]
fn a_douyin_gallery_gives_its_note_link_and_the_largest_picture() {
    let u = unified(
        "douyin",
        "image",
        "7322222222222222222",
        json!({"images": [{"width": 720, "height": 1280}, {"width": 1080, "height": 1440}], "is_ads": true}),
        json!({}),
    );
    assert_eq!(
        u.canonical_url.as_deref(),
        Some("https://www.douyin.com/note/7322222222222222222")
    );
    assert_eq!((u.max_width, u.max_height), (Some(1080), Some(1440)));
    assert!(u.sponsored);
    assert_eq!(u.max_duration_ms, None);
    let article = unified("douyin", "article", "1", json!({}), json!({}));
    assert_eq!(article.content_kinds, ["article"]);
    assert_eq!(
        article.canonical_url, None,
        "no public link is known for an article"
    );
}

#[test]
fn another_site_and_a_hostile_record_give_nothing_wrong() {
    assert!(project("weibo", "post", "P1", &json!({}), &json!({})).is_none());
    let u = unified(
        "bilibili",
        "video",
        "BV1?token=x",
        json!({"duration": "abc", "pages": "no", "ugc_season": {"title": 5}}),
        json!({"view": -1}),
    );
    assert_eq!(
        u.canonical_url, None,
        "a key that would add a query is not a public link"
    );
    assert_eq!(u.max_duration_ms, None);
    assert_eq!(u.collection, None);
    assert_eq!(u.stats.plays, None);
}

async fn open() -> (std::sync::Arc<Core>, tempfile::TempDir) {
    let dir = tempfile::tempdir().unwrap();
    let core = Core::open(Profile {
        data_root: dir.path().join("data"),
        archive_root: dir.path().join("archive"),
        vault_dir: Some(dir.path().join("vault")),
        ..Default::default()
    })
    .await
    .unwrap();
    (core, dir)
}

const SEED: &str = "
INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('ab','bilibili','1','t'),('ad','douyin','2','t'),('aw','weibo','3','t');
INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES
 ('eb','bilibili','video','BV1xx','ab','t'),('ed','douyin','video','731','ad','t'),('ew','weibo','post','P1','aw','t'),('en','bilibili','video','BV2yy','ab','t');
INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at,completeness,metadata_json) VALUES
 ('rb','eb','d1','视频','简介','2026-01-01','full','{\"duration\":90,\"dimension\":{\"width\":1280,\"height\":720},\"tname\":\"音乐\"}'),
 ('rd','ed','d2','视频','','2026-01-01','full','{\"video\":{\"duration\":5000,\"width\":720,\"height\":1280},\"ip_label\":\"IP属地：北京\"}'),
 ('rw','ew','d3','帖子','正文','2026-01-01','full','{}');
INSERT INTO media_items(media_id,revision_id,role,position,source_attachment_id,required) VALUES
 ('m1','rb','video',0,'v',1),('m2','rb','cover',1,'c',0),('m3','rd','video',0,'v',1);
INSERT INTO source_observations(observation_id,entry_id,revision_id,access_state,observed_at,observation_seq,evidence) VALUES
 ('o1','eb','rb','visible','2026-01-01',1,'{\"stats\":{\"view\":7,\"like\":2}}'),
 ('o2','eb','rb','visible','2026-01-02',2,'{\"stats\":{}}'),
 ('o3','ed','rd','visible','2026-01-01',1,'{\"stats\":{\"digg_count\":9}}');
INSERT INTO entry_meta(entry_id,site,pinned,updated_at) VALUES('eb','bilibili',0,'t');";

async fn stored(core: &Core, entry: &'static str) -> Option<store::meta::Stored> {
    core.db
        .read(move |c| store::meta::load(c, entry))
        .await
        .unwrap()
}

#[tokio::test]
async fn entries_kept_before_the_unified_fields_get_them_once_from_what_is_stored() {
    let (core, _dir) = open().await;
    core.db
        .write(|tx| {
            tx.execute_batch(SEED)?;
            Ok(())
        })
        .await
        .unwrap();
    assert_eq!(
        backfill(&core).await.unwrap(),
        2,
        "only the two sites' entries with a revision"
    );

    let video = stored(&core, "eb").await.expect("filled");
    assert_eq!(video.site, "bilibili");
    assert_eq!(
        video.unified.canonical_url.as_deref(),
        Some("https://www.bilibili.com/video/BV1xx")
    );
    assert_eq!(
        (
            video.unified.max_duration_ms,
            video.unified.max_width,
            video.unified.max_height
        ),
        (Some(90_000), Some(1280), Some(720))
    );
    assert_eq!(video.unified.category.as_deref(), Some("音乐"));
    assert_eq!(
        (video.images, video.videos),
        (0, 1),
        "counts come from the stored resources"
    );
    assert_eq!(video.unified.cover.as_deref(), Some("c"));
    assert_eq!(video.unified.content_kinds, ["text", "video"]);
    assert_eq!(
        (video.unified.stats.plays, video.unified.stats.likes),
        (Some(7), Some(2)),
        "the newest observation that carried counters"
    );

    let douyin = stored(&core, "ed").await.expect("filled");
    assert_eq!(douyin.unified.ip_region.as_deref(), Some("北京"));
    assert_eq!(douyin.unified.stats.likes, Some(9));
    assert_eq!(
        douyin.unified.content_kinds,
        ["video"],
        "no body, so no text"
    );

    assert!(
        stored(&core, "ew").await.is_none(),
        "another site maps its own fields"
    );
    assert!(
        stored(&core, "en").await.is_none(),
        "an entry with no stored revision has nothing to read"
    );

    let history_before: i64 = core
        .db
        .read(|c| {
            Ok(
                c.query_row("SELECT COUNT(*) FROM entry_stat_observations", [], |r| {
                    r.get(0)
                })?,
            )
        })
        .await
        .unwrap();
    assert_eq!(history_before, 2, "one counter observation per entry");
    assert_eq!(
        backfill(&core).await.unwrap(),
        0,
        "a second pass changes nothing"
    );
    let history_after: i64 = core
        .db
        .read(|c| {
            Ok(
                c.query_row("SELECT COUNT(*) FROM entry_stat_observations", [], |r| {
                    r.get(0)
                })?,
            )
        })
        .await
        .unwrap();
    assert_eq!(history_after, history_before);
}

#[tokio::test]
async fn a_new_observation_without_mapped_fields_is_filled_from_its_own_record() {
    let (core, _dir) = open().await;
    core.db
        .write(|tx| {
            tx.execute_batch(
                "INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('ab','bilibili','1','t');
                 INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES('eb','bilibili','video','BV1zz','ab','t');",
            )?;
            Ok(())
        })
        .await
        .unwrap();
    jobs::enqueue(
        &core.db,
        "legacy-1".into(),
        "observe",
        "eb",
        json!({}),
        1,
        1,
        0,
    )
    .await
    .unwrap();
    let job = jobs::claim_due(&core.db, "legacy-test", 1)
        .await
        .unwrap()
        .remove(0);
    job.mark_running(&core.db).await.unwrap();
    let detail = ObservedDetail {
        document: Document::default(),
        metadata: json!({"duration": 12, "tname": "动画"}),
        native_entry_key: "BV1zz".into(),
        native_kind: "video".into(),
        title: "标题".into(),
        normalized_body: String::new(),
        published_at: None,
        published_precision: "unknown".into(),
        access_state: "visible".into(),
        source_version_evidence: json!({}),
        resources: Vec::new(),
        completeness: "full".into(),
        stats: json!({"view": 3}),
        unified: None,
    };
    persist_detail(&core, &job, "eb", "ab", detail)
        .await
        .unwrap();
    let u = stored(&core, "eb")
        .await
        .expect("stored with the revision")
        .unified;
    assert_eq!(u.max_duration_ms, Some(12_000));
    assert_eq!(u.category.as_deref(), Some("动画"));
    assert_eq!(u.stats.plays, Some(3));
    assert_eq!(backfill(&core).await.unwrap(), 0, "nothing is left to fill");
}
