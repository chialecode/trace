//! The explorer over a seeded library and real download folders: folders and counts, sorted
//! and filtered pages, an entry's files, records that disagree with the disk, and names or
//! links that try to leave a folder.
use super::*;
use crate::config::Profile;
use crate::store;
use trace_contracts::explorer::{FileArgs, ListArgs, TreeArgs};

async fn open_core() -> (std::sync::Arc<Core>, tempfile::TempDir) {
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

const LIBRARY: &str = "
INSERT INTO source_accounts(account_id,namespace,native_account_key,display_name,first_seen_at) VALUES
 ('a1','weibo','100','甲作者','t'),('a2','weibo','200','乙作者','t'),('a3','bilibili','300','丙作者','t');
INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at,published_at) VALUES
 ('e1','weibo','post','P1','a1','t','2026-03-05T10:00:00Z'),
 ('e2','weibo','post','P2','a1','t','2026-03-20T10:00:00Z'),
 ('e3','weibo','post','P3','a1','t','2025-12-01T10:00:00Z'),
 ('e4','weibo','post','P4','a1','t',NULL),
 ('e5','weibo','post','P5','a2','t','2026-01-02T10:00:00Z'),
 ('e6','bilibili','video','BV1','a3','t','2026-02-02T10:00:00Z');
INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at,metadata_json) VALUES
 ('r1','e1','d1','春天的花','正文','t','{}'),('r2','e2','d2','夏天','正文','t','{}'),
 ('r3','e3','d3','旧事','正文','t','{}'),('r4','e4','d4','','正文','t','{}'),
 ('r5','e5','d5','乙的帖子','正文','t','{}'),('r6','e6','d6','一段视频','','t','{}');
INSERT INTO entry_meta(entry_id,site,content_kinds,max_duration_ms,max_width,max_height,updated_at) VALUES
 ('e1','weibo','[\"text\",\"image\"]',NULL,NULL,NULL,'t'),
 ('e2','weibo','[\"text\",\"video\"]',90000,1920,1080,'t'),
 ('e6','bilibili','[\"video\"]',600000,1280,720,'t');
INSERT INTO blobs(blob_id,size,mime,first_seen_at) VALUES
 ('sha256:aa',2000,'video/mp4','t'),('sha256:bb',500,'image/jpeg','t'),('sha256:cc',9000,'video/mp4','t');
INSERT INTO replicas(replica_id,blob_id,node_id,store_id,locator,state) VALUES
 ('p1','sha256:aa','n','s','aa','verified'),('p2','sha256:bb','n','s','bb','verified'),('p3','sha256:cc','n','s','cc','verified');
INSERT INTO media_items(media_id,revision_id,role,position,source_attachment_id,required,fetch_state) VALUES
 ('m1','r2','video',0,'v',1,'wanted'),('m2','r2','cover',1,'c',0,'wanted'),
 ('m3','r1','image',0,'i',1,'wanted'),('m4','r6','video',0,'v',1,'wanted'),
 ('m5','r6','audio',1,'au',1,'deferred'),('m6','r1','image',1,'i2',1,'wanted');
INSERT INTO media_variants(variant_id,media_id,source_representation,blob_id,measured_spec,created_at) VALUES
 ('v1','m1','x','sha256:aa','{\"width\":1920,\"height\":1080,\"duration_ms\":90000}','t'),
 ('v2','m2','x','sha256:bb','{\"width\":640,\"height\":360}','t'),
 ('v3','m3','x','sha256:bb',NULL,'t'),
 ('v4','m4','x','sha256:cc','{\"width\":1280,\"height\":720,\"duration_ms\":600000}','t');";

async fn seeded() -> (std::sync::Arc<Core>, tempfile::TempDir) {
    let (core, dir) = open_core().await;
    core.db
        .write(|tx| {
            tx.execute_batch(LIBRARY)?;
            Ok(())
        })
        .await
        .unwrap();
    (core, dir)
}

fn list_args(node: &str) -> ListArgs {
    ListArgs {
        node: node.into(),
        ..ListArgs::default()
    }
}

async fn children(core: &Core, node: Option<&str>) -> Vec<(String, String, Option<i64>)> {
    tree(
        core,
        TreeArgs {
            node: node.map(str::to_owned),
        },
    )
    .await
    .unwrap()
    .nodes
    .into_iter()
    .map(|n| (n.node, n.name, n.count))
    .collect()
}

fn names(view: &ListView) -> Vec<&str> {
    view.rows.iter().map(|r| r.name.as_str()).collect()
}

#[tokio::test]
async fn the_library_folders_follow_site_account_year_month_with_counts() {
    let (core, _dir) = seeded().await;
    let roots = children(&core, None).await;
    assert_eq!(
        roots.iter().map(|r| r.1.as_str()).collect::<Vec<_>>(),
        ["资源库", "下载位置"]
    );
    let sites = children(&core, Some("lib")).await;
    assert_eq!(
        sites
            .iter()
            .map(|s| (s.1.as_str(), s.2))
            .collect::<Vec<_>>(),
        [("哔哩哔哩", Some(1)), ("微博", Some(5))]
    );
    let accounts = children(&core, Some("lib/s:weibo")).await;
    assert_eq!(
        accounts
            .iter()
            .map(|s| (s.1.as_str(), s.2))
            .collect::<Vec<_>>(),
        [("乙作者", Some(1)), ("甲作者", Some(4))]
    );
    let years = children(&core, Some("lib/s:weibo/a:a1")).await;
    assert_eq!(
        years
            .iter()
            .map(|s| (s.1.as_str(), s.2))
            .collect::<Vec<_>>(),
        [
            ("2026 年", Some(2)),
            ("2025 年", Some(1)),
            ("未知时间", Some(1))
        ],
        "newest year first, unknown time last"
    );
    let months = children(&core, Some("lib/s:weibo/a:a1/y:2026")).await;
    assert_eq!(
        months
            .iter()
            .map(|s| (s.1.as_str(), s.2))
            .collect::<Vec<_>>(),
        [("3 月", Some(2))]
    );
    assert!(
        children(&core, Some("lib/s:weibo/a:a1/y:none"))
            .await
            .is_empty()
    );
    assert!(
        children(&core, Some("lib/s:weibo/a:a1/y:2026/m:03"))
            .await
            .is_empty()
    );
}

#[tokio::test]
async fn a_folder_lists_its_entries_newest_first_and_pages_sorts_and_filters() {
    let (core, _dir) = seeded().await;
    let all = list(&core, list_args("lib")).await.unwrap();
    assert_eq!(all.total, 6);
    assert_eq!(
        names(&all),
        [
            "夏天",
            "春天的花",
            "一段视频",
            "乙的帖子",
            "旧事",
            "（无标题）"
        ],
        "newest first, unknown publication time last"
    );
    assert_eq!(all.path, "资源库");

    let site = list(&core, list_args("lib/s:bilibili")).await.unwrap();
    assert_eq!((site.total, names(&site)), (1, vec!["一段视频"]));
    assert_eq!(site.path, "资源库\\哔哩哔哩");
    let month = list(&core, list_args("lib/s:weibo/a:a1/y:2026/m:03"))
        .await
        .unwrap();
    assert_eq!(names(&month), ["夏天", "春天的花"]);
    assert_eq!(month.path, "资源库\\微博\\甲作者\\2026\\03");
    let unknown = list(&core, list_args("lib/s:weibo/a:a1/y:none"))
        .await
        .unwrap();
    assert_eq!(names(&unknown), ["（无标题）"]);

    let mut by_duration = list_args("lib");
    by_duration.sort = Some("duration".into());
    by_duration.descending = true;
    let view = list(&core, by_duration.clone()).await.unwrap();
    assert_eq!(&names(&view)[..2], ["一段视频", "夏天"]);
    assert_eq!(
        view.rows[2].duration_ms, None,
        "entries without a duration stay last"
    );
    by_duration.descending = false;
    assert_eq!(
        &names(&list(&core, by_duration).await.unwrap())[..2],
        ["夏天", "一段视频"]
    );

    let mut by_size = list_args("lib");
    by_size.sort = Some("size".into());
    by_size.descending = true;
    let view = list(&core, by_size).await.unwrap();
    assert_eq!(view.rows[0].name, "一段视频");
    assert_eq!(view.rows[0].size, Some(9000));
    assert_eq!(
        view.rows[1].size,
        Some(2500),
        "video and cover of the same entry"
    );

    let mut by_author = list_args("lib");
    by_author.sort = Some("author".into());
    let view = list(&core, by_author).await.unwrap();
    assert_eq!(view.rows[0].author.as_deref(), Some("丙作者"));

    let mut paged = list_args("lib");
    paged.limit = Some(2);
    paged.offset = Some(4);
    let view = list(&core, paged).await.unwrap();
    assert_eq!(
        (view.total, view.offset, names(&view)),
        (6, 4, vec!["旧事", "（无标题）"])
    );

    let mut query = list_args("lib");
    query.query = Some("  天 ".into());
    assert_eq!(
        names(&list(&core, query).await.unwrap()),
        ["夏天", "春天的花"]
    );
    let mut video = list_args("lib");
    video.kind = Some("video".into());
    assert_eq!(
        names(&list(&core, video).await.unwrap()),
        ["夏天", "一段视频"]
    );
    let mut deferred = list_args("lib");
    deferred.state = Some("deferred".into());
    let view = list(&core, deferred).await.unwrap();
    assert_eq!(names(&view), ["一段视频"]);
    assert_eq!(view.rows[0].state, "deferred");
}

#[tokio::test]
async fn a_page_in_the_far_half_is_the_same_rows_as_reading_the_whole_folder_from_the_start() {
    let (core, _dir) = seeded().await;
    // Twenty-four more entries that share titles, dates and durations, and some that have none,
    // so that ties and unknown values fall in both halves of every ordering.
    core.db
        .write(|tx| {
            tx.execute_batch(
                "WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<24)
                   INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at,published_at)
                   SELECT 'x'||i, CASE WHEN i%2=0 THEN 'weibo' ELSE 'bilibili' END, CASE WHEN i%3=0 THEN 'video' ELSE 'post' END,
                          'X'||i, CASE i%3 WHEN 0 THEN 'a1' WHEN 1 THEN 'a2' ELSE 'a3' END, 't',
                          CASE WHEN i%5=0 THEN NULL ELSE '2026-02-0'||(i%4+1)||'T10:00:00Z' END FROM n;
                 WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<24)
                   INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at,metadata_json)
                   SELECT 'xr'||i,'x'||i,'xd'||i,'同名'||(i%3),'正文','t','{}' FROM n;
                 WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<24)
                   INSERT INTO entry_meta(entry_id,site,content_kinds,max_duration_ms,max_width,max_height,updated_at)
                   SELECT 'x'||i, 'weibo', '[\"text\"]', CASE WHEN i%4=0 THEN NULL ELSE (i%3)*1000 END, 640, 480, 't' FROM n WHERE i%7!=0;",
            )?;
            Ok(())
        })
        .await
        .unwrap();
    for sort in [
        "name",
        "kind",
        "size",
        "duration",
        "dimensions",
        "published",
        "site",
        "author",
    ] {
        for descending in [false, true] {
            let mut whole = list_args("lib");
            whole.sort = Some(sort.into());
            whole.descending = descending;
            whole.limit = Some(500);
            let all = list(&core, whole.clone()).await.unwrap();
            assert_eq!(all.total, 30, "{sort}");
            let all: Vec<_> = all
                .rows
                .iter()
                .map(|r| (r.name.clone(), r.published_at.clone(), r.duration_ms))
                .collect();
            for limit in [1, 2, 5, 7, 30] {
                for offset in 0..=31 {
                    let mut page = whole.clone();
                    page.limit = Some(limit);
                    page.offset = Some(offset);
                    let view = list(&core, page).await.unwrap();
                    let got: Vec<_> = view
                        .rows
                        .iter()
                        .map(|r| (r.name.clone(), r.published_at.clone(), r.duration_ms))
                        .collect();
                    let from = (offset as usize).min(all.len());
                    let to = (from + limit as usize).min(all.len());
                    assert_eq!(
                        got,
                        all[from..to],
                        "{sort} descending={descending} limit={limit} offset={offset}"
                    );
                    assert_eq!(view.total, 30);
                }
            }
        }
    }
}

#[tokio::test]
async fn an_entry_row_describes_what_it_holds() {
    let (core, _dir) = seeded().await;
    let view = list(&core, list_args("lib/s:weibo/a:a1/y:2026/m:03"))
        .await
        .unwrap();
    let video = &view.rows[0];
    assert_eq!(video.entry_id.as_deref(), Some("e2"));
    assert_eq!(video.kind, "video");
    assert_eq!(video.kinds, ["text", "video"]);
    assert_eq!(
        (video.width, video.height, video.duration_ms),
        (Some(1920), Some(1080), Some(90_000))
    );
    assert_eq!(video.site.as_deref(), Some("微博"));
    assert_eq!(video.author.as_deref(), Some("甲作者"));
    assert_eq!(video.node.as_deref(), Some("lib/e:e2"));
    assert_eq!(video.path, "资源库\\微博\\甲作者\\2026\\03\\夏天");
    assert!(!video.can_open && !video.can_reveal && !video.can_sync && !video.can_forget);
    assert_eq!(
        video.blob_id.as_deref(),
        Some("sha256:bb"),
        "the cover, for a thumbnail"
    );
}

#[tokio::test]
async fn an_entry_lists_its_files_in_source_order_with_their_states() {
    let (core, _dir) = seeded().await;
    let view = list(&core, list_args("lib/e:e6")).await.unwrap();
    assert_eq!(view.path, "资源库\\哔哩哔哩\\一段视频");
    assert_eq!(names(&view), ["视频 01.mp4", "音频 02"]);
    assert_eq!(view.rows[0].state, "saved");
    assert_eq!(view.rows[0].size, Some(9000));
    assert_eq!(
        (
            view.rows[0].width,
            view.rows[0].height,
            view.rows[0].duration_ms
        ),
        (Some(1280), Some(720), Some(600_000))
    );
    assert!(view.rows[0].can_open && view.rows[0].blob_id.as_deref() == Some("sha256:cc"));
    assert_eq!(
        view.rows[1].state, "deferred",
        "left undownloaded by the subscription"
    );
    assert!(!view.rows[1].can_open && view.rows[1].blob_id.is_none());

    let images = list(&core, list_args("lib/e:e1")).await.unwrap();
    assert_eq!(names(&images), ["图片 01.jpg", "图片 02"]);
    assert_eq!(
        images.rows[1].state, "missing",
        "wanted but nothing stored yet"
    );

    let mut saved = list_args("lib/e:e1");
    saved.state = Some("saved".into());
    assert_eq!(names(&list(&core, saved).await.unwrap()), ["图片 01.jpg"]);
    let mut sorted = list_args("lib/e:e2");
    sorted.sort = Some("size".into());
    sorted.descending = true;
    assert_eq!(list(&core, sorted).await.unwrap().rows[0].kind, "video");
    assert!(list(&core, list_args("lib/e:nope")).await.is_err());
}

#[tokio::test]
async fn made_up_nodes_sorts_and_filters_are_refused() {
    let (core, _dir) = seeded().await;
    for node in [
        "",
        "lib/x",
        "lib/s:../x",
        "dl/x/..",
        "lib/s:weibo/a:a1/y:2026/m:99",
    ] {
        assert!(list(&core, list_args(node)).await.is_err(), "{node:?}");
    }
    let mut bad_sort = list_args("lib");
    bad_sort.sort = Some("size; DROP TABLE entries".into());
    assert!(list(&core, bad_sort).await.is_err());
    let mut bad_kind = list_args("lib");
    bad_kind.kind = Some("exe".into());
    assert!(list(&core, bad_kind).await.is_err());
    let mut bad_state = list_args("lib");
    bad_state.state = Some("present".into());
    assert!(
        list(&core, bad_state).await.is_ok(),
        "present is a state of a downloaded file and of a library row alike"
    );
    let mut long = list_args("lib");
    long.query = Some("x".repeat(500));
    assert!(list(&core, long).await.is_err());
    // An unknown site or account is an empty folder, not an error and not another site's data.
    assert_eq!(
        list(&core, list_args("lib/s:nowhere")).await.unwrap().total,
        0
    );
    assert_eq!(
        list(&core, list_args("lib/s:bilibili/a:a1"))
            .await
            .unwrap()
            .total,
        0
    );
}

/// A download folder with files TRACE wrote (recorded), one it did not, one that was edited
/// and one that was deleted.
async fn downloads() -> (
    std::sync::Arc<Core>,
    tempfile::TempDir,
    std::path::PathBuf,
    String,
) {
    let (core, dir) = seeded().await;
    let root = dir.path().join("downloads");
    std::fs::create_dir_all(root.join("微博").join("甲作者")).unwrap();
    std::fs::create_dir_all(root.join(".trace-tmp")).unwrap();
    std::fs::write(root.join(".trace-tmp").join("half.part"), b"x").unwrap();
    std::fs::write(
        root.join("微博/甲作者/2026-03-20 夏天 [P2].mp4"),
        vec![1u8; 2000],
    )
    .unwrap();
    std::fs::write(root.join("微博/甲作者/2026-03-20 夏天 [P2].json"), b"{}").unwrap();
    std::fs::write(root.join("微博/甲作者/changed.jpg"), b"edited elsewhere").unwrap();
    std::fs::write(root.join("微博/甲作者/mine.txt"), b"not from TRACE").unwrap();
    std::fs::write(root.join("微博/甲作者/tool.exe"), b"MZ").unwrap();
    let path = root.to_string_lossy().into_owned();
    let location = core
        .db
        .write(move |tx| store::downloads::use_folder(tx, &path, "2026-10-17T00:00:00Z"))
        .await
        .unwrap();
    let loc = location.clone();
    core.db
        .write(move |tx| {
            for (id, entry, media, rel, role, size, state) in [
                ("c1", "e2", Some("m1"), "微博\\甲作者\\2026-03-20 夏天 [P2].mp4", "media", 2000, "present"),
                ("c2", "e2", None, "微博\\甲作者\\2026-03-20 夏天 [P2].json", "sidecar", 2, "present"),
                ("c3", "e1", None, "微博\\甲作者\\changed.jpg", "media", 500, "present"),
                ("c4", "e1", None, "微博\\甲作者\\gone.jpg", "media", 500, "present"),
                ("c5", "e1", None, "..\\..\\evil.txt", "text", 5, "present"),
                ("c6", "e1", None, "微博\\甲作者\\nul.txt", "text", 5, "present"),
            ] {
                tx.execute(
                    "INSERT INTO external_copies(copy_id,entry_id,media_id,location_id,rel_path,role,size,sha256,state,created_at) VALUES(?1,?2,?3,?4,?5,?6,?7,NULL,?8,'t')",
                    rusqlite::params![id, entry, media, loc, rel, role, size, state],
                )?;
            }
            Ok(())
        })
        .await
        .unwrap();
    (core, dir, root, location)
}

fn folder_node(location: &str, rel: &[&str]) -> String {
    Node::Folder {
        location: location.into(),
        rel: rel.iter().map(|s| (*s).to_owned()).collect(),
    }
    .text()
}

#[tokio::test]
async fn download_folders_show_their_real_folders_but_not_the_staging_area() {
    let (core, _dir, root, location) = downloads().await;
    let top = children(&core, Some("dl")).await;
    assert_eq!(top.len(), 1);
    assert_eq!(top[0].1, root.to_string_lossy());
    assert_eq!(top[0].0, folder_node(&location, &[]));
    let below = children(&core, Some(&folder_node(&location, &[]))).await;
    assert_eq!(
        below.iter().map(|b| b.1.as_str()).collect::<Vec<_>>(),
        ["微博"]
    );
    let deeper = children(&core, Some(&folder_node(&location, &["微博"]))).await;
    assert_eq!(
        deeper.iter().map(|b| b.1.as_str()).collect::<Vec<_>>(),
        ["甲作者"]
    );
    assert!(
        children(&core, Some(&folder_node(&location, &["微博", "甲作者"])))
            .await
            .is_empty()
    );
    let places = list(&core, list_args("dl")).await.unwrap();
    assert_eq!(
        (places.rows.len(), places.rows[0].kind.as_str()),
        (1, "folder")
    );
}

#[tokio::test]
async fn a_download_folder_lists_files_with_their_records_and_how_they_differ_from_disk() {
    let (core, _dir, root, location) = downloads().await;
    let view = list(
        &core,
        list_args(&folder_node(&location, &["微博", "甲作者"])),
    )
    .await
    .unwrap();
    assert_eq!(view.path, root.join("微博\\甲作者").to_string_lossy());
    let by_name = |name: &str| {
        view.rows
            .iter()
            .find(|r| r.name == name)
            .unwrap_or_else(|| panic!("{name}: {:?}", names(&view)))
    };
    let video = by_name("2026-03-20 夏天 [P2].mp4");
    assert_eq!(video.state, "present");
    assert_eq!(video.kind, "video");
    assert_eq!(video.entry_id.as_deref(), Some("e2"));
    assert_eq!(video.site.as_deref(), Some("微博"));
    assert_eq!(video.author.as_deref(), Some("甲作者"));
    assert_eq!(video.size, Some(2000));
    assert_eq!(
        video.rel_path.as_deref(),
        Some("微博\\甲作者\\2026-03-20 夏天 [P2].mp4")
    );
    assert_eq!(video.location_id.as_deref(), Some(location.as_str()));
    assert!(video.can_open && video.can_reveal && video.can_sync && video.can_forget);
    assert_eq!(video.width, Some(1920), "measured size of the saved media");
    assert_eq!(by_name("2026-03-20 夏天 [P2].json").kind, "text");

    let mine = by_name("mine.txt");
    assert_eq!(mine.state, "untracked");
    assert!(mine.can_open && mine.can_reveal && !mine.can_sync && !mine.can_forget);
    assert_eq!(mine.entry_id, None);

    let program = by_name("tool.exe");
    assert!(
        program.can_reveal && !program.can_open,
        "a program is shown, never started"
    );

    let edited = by_name("changed.jpg");
    assert_eq!(
        edited.state, "changed",
        "its length is no longer what TRACE wrote"
    );
    assert!(edited.can_open && edited.can_forget && !edited.can_sync);

    let gone = by_name("gone.jpg");
    assert_eq!(gone.state, "missing");
    assert!(!gone.can_open && !gone.can_reveal && gone.can_forget && !gone.can_sync);
    assert_eq!(gone.size, Some(500), "the size TRACE recorded");

    assert!(
        view.rows
            .iter()
            .all(|r| r.name != "evil.txt" && r.name != "nul.txt"),
        "records that point outside the folder or name a device never become rows: {:?}",
        names(&view)
    );
    assert!(view.rows.iter().all(|r| !r.path.contains("..")));

    let mut missing = list_args(&folder_node(&location, &["微博", "甲作者"]));
    missing.state = Some("missing".into());
    assert_eq!(names(&list(&core, missing).await.unwrap()), ["gone.jpg"]);
    let mut untracked = list_args(&folder_node(&location, &["微博", "甲作者"]));
    untracked.state = Some("untracked".into());
    assert_eq!(
        names(&list(&core, untracked).await.unwrap()),
        ["mine.txt", "tool.exe"]
    );
    let mut images = list_args(&folder_node(&location, &["微博", "甲作者"]));
    images.kind = Some("image".into());
    assert_eq!(
        names(&list(&core, images).await.unwrap()),
        ["changed.jpg", "gone.jpg"]
    );

    let top = list(&core, list_args(&folder_node(&location, &[])))
        .await
        .unwrap();
    assert_eq!(names(&top), ["微博"]);
    assert_eq!(top.rows[0].kind, "folder");
    assert_eq!(
        top.rows[0].node.as_deref(),
        Some(folder_node(&location, &["微博"]).as_str())
    );
}

#[tokio::test]
async fn download_listings_sort_keep_folders_first_and_page() {
    let (core, _dir, _root, location) = downloads().await;
    let node = folder_node(&location, &["微博"]);
    std::fs::create_dir_all(_root.join("微博").join("丙作者")).unwrap();
    std::fs::write(_root.join("微博").join("z.txt"), b"zz").unwrap();
    std::fs::write(_root.join("微博").join("a.txt"), b"a").unwrap();
    let mut args = list_args(&node);
    args.sort = Some("size".into());
    args.descending = true;
    let view = list(&core, args.clone()).await.unwrap();
    assert_eq!(
        names(&view),
        ["丙作者", "甲作者", "z.txt", "a.txt"],
        "folders first, then largest file"
    );
    args.descending = false;
    args.limit = Some(2);
    args.offset = Some(2);
    let page = list(&core, args).await.unwrap();
    assert_eq!((page.total, names(&page)), (4, vec!["a.txt", "z.txt"]));
}

#[tokio::test]
async fn open_and_show_refuse_what_could_start_a_program_or_leave_the_folder() {
    let (core, dir, root, location) = downloads().await;
    let ask = |rel: &str| FileArgs {
        location_id: location.clone(),
        rel_path: rel.into(),
    };
    // A program is never started from the interface.
    let error = open(&core, ask("微博\\甲作者\\tool.exe"))
        .await
        .unwrap_err();
    assert!(matches!(error, CoreError::Unsupported(_)), "{error:?}");
    // A folder is not "opened" as a file either.
    assert!(matches!(
        open(&core, ask("微博")).await.unwrap_err(),
        CoreError::Unsupported(_)
    ));
    // Names that leave the folder or name devices are refused before any path is built.
    for rel in [
        "..\\data\\x.txt",
        "微博\\..\\..\\x.txt",
        "C:\\Windows\\win.ini",
        "微博\\nul.txt",
        "a:b",
    ] {
        assert!(open(&core, ask(rel)).await.is_err(), "{rel:?}");
        assert!(reveal(&core, ask(rel)).await.is_err(), "{rel:?}");
    }
    // A file that is not there any more.
    assert!(matches!(
        open(&core, ask("微博\\甲作者\\gone.jpg"))
            .await
            .unwrap_err(),
        CoreError::NotFound(_)
    ));
    assert!(matches!(
        reveal(
            &core,
            FileArgs {
                location_id: "dl-unknown".into(),
                rel_path: "x".into()
            }
        )
        .await
        .unwrap_err(),
        CoreError::NotFound(_)
    ));
    assert!(
        root.join("微博/甲作者/tool.exe").exists(),
        "nothing was changed"
    );
    drop(dir);
}

#[cfg(windows)]
#[tokio::test]
async fn a_junction_inside_a_download_folder_is_not_followed() {
    let (core, dir, root, location) = downloads().await;
    let outside = dir.path().join("outside");
    std::fs::create_dir_all(&outside).unwrap();
    std::fs::write(outside.join("secret.txt"), b"secret").unwrap();
    let link = root.join("link");
    let made = std::process::Command::new("cmd")
        .args(["/C", "mklink", "/J"])
        .arg(&link)
        .arg(&outside)
        .output()
        .unwrap();
    assert!(
        made.status.success(),
        "{}",
        String::from_utf8_lossy(&made.stdout)
    );
    // The junction is not listed as a folder or a file, and cannot be entered or opened.
    let listed = list(&core, list_args(&folder_node(&location, &[])))
        .await
        .unwrap();
    assert!(
        listed.rows.iter().all(|r| r.name != "link"),
        "{:?}",
        names(&listed)
    );
    assert!(
        children(&core, Some(&folder_node(&location, &[])))
            .await
            .iter()
            .all(|n| n.1 != "link")
    );
    assert!(
        list(&core, list_args(&folder_node(&location, &["link"])))
            .await
            .is_err()
    );
    assert!(
        open(
            &core,
            FileArgs {
                location_id: location.clone(),
                rel_path: "link\\secret.txt".into()
            }
        )
        .await
        .is_err()
    );
    assert!(
        reveal(
            &core,
            FileArgs {
                location_id: location,
                rel_path: "link".into()
            }
        )
        .await
        .is_err()
    );
    // Removing the junction must not touch the target.
    std::fs::remove_dir(&link).unwrap();
    assert!(outside.join("secret.txt").exists());
}

#[tokio::test]
async fn a_hundred_thousand_entries_page_sort_and_filter_from_the_first_screen() {
    let (core, _dir) = open_core().await;
    core.db
        .write(|tx| {
            tx.execute_batch(
                "INSERT INTO source_accounts(account_id,namespace,native_account_key,display_name,first_seen_at)
                   SELECT 'acct'||i, CASE WHEN i%2=0 THEN 'weibo' ELSE 'bilibili' END, 'k'||i, '作者'||i, 't' FROM (WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<20) SELECT i FROM n);
                 WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<100000)
                   INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at,published_at)
                   SELECT 'e'||i, CASE WHEN (i%20)%2=1 THEN 'weibo' ELSE 'bilibili' END, 'post', 'K'||i, 'acct'||(i%20+1), 't',
                          CASE WHEN i%50=0 THEN NULL ELSE strftime('%Y-%m-%dT%H:%M:%SZ','2018-01-01',(i*7)||' minutes') END FROM n;
                 WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<100000)
                   INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at,metadata_json)
                   SELECT 'r'||i,'e'||i,'d'||i,'标题'||i,'正文','t','{}' FROM n;
                 WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<100000)
                   INSERT INTO entry_meta(entry_id,site,content_kinds,max_duration_ms,updated_at)
                   SELECT 'e'||i, 'weibo', CASE WHEN i%3=0 THEN '[\"video\"]' ELSE '[\"text\"]' END, i*10, 't' FROM n;",
            )?;
            Ok(())
        })
        .await
        .unwrap();
    let timed = |label: &'static str, started: std::time::Instant| {
        let took = started.elapsed();
        eprintln!("explorer {label}: {took:?}");
        assert!(
            took < std::time::Duration::from_secs(20),
            "{label} took {took:?}"
        );
    };
    let started = std::time::Instant::now();
    let first = list(&core, list_args("lib")).await.unwrap();
    timed("first screen", started);
    assert_eq!((first.total, first.rows.len()), (100_000, 100));
    assert_eq!(first.rows[0].name, "标题99999", "newest first");

    let started = std::time::Instant::now();
    let sites = children(&core, Some("lib")).await;
    timed("site folders", started);
    assert_eq!(sites.len(), 2);
    assert_eq!(sites.iter().map(|s| s.2.unwrap()).sum::<i64>(), 100_000);

    let started = std::time::Instant::now();
    let accounts = children(&core, Some("lib/s:weibo")).await;
    timed("account folders", started);
    assert_eq!(accounts.len(), 10);

    for (sort, descending) in [
        ("duration", true),
        ("name", false),
        ("author", true),
        ("size", true),
    ] {
        let mut args = list_args("lib");
        args.sort = Some(sort.into());
        args.descending = descending;
        args.offset = Some(99_900);
        let started = std::time::Instant::now();
        let page = list(&core, args).await.unwrap();
        timed(sort, started);
        assert_eq!((page.total, page.rows.len()), (100_000, 100), "{sort}");
    }

    let mut filtered = list_args("lib/s:weibo");
    filtered.kind = Some("video".into());
    filtered.query = Some("标题3".into());
    let started = std::time::Instant::now();
    let page = list(&core, filtered).await.unwrap();
    timed("filter", started);
    assert!(
        page.total > 0
            && page
                .rows
                .iter()
                .all(|r| r.kind == "video" && r.name.contains("标题3"))
    );
}
