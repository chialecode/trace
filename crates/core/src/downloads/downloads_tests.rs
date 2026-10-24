//! C05 download folders: readable names, verified temp-then-rename writes, records, change
//! detection and syncing back to the library.
use super::*;
use crate::config::Profile;
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::collections::BTreeMap;
use std::sync::Arc;
use trace_contracts::control::Request;
use trace_contracts::download::ExportItem;

fn sha(bytes: &[u8]) -> String {
    Sha256::digest(bytes)
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect()
}

struct Fx {
    _dir: tempfile::TempDir,
    core: Arc<Core>,
    root: PathBuf,
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
    let root = dir.path().join("downloads");
    Fx {
        _dir: dir,
        core,
        root,
    }
}

/// One media file the fixture entry carries.
struct Media<'a> {
    id: &'a str,
    role: &'a str,
    mime: &'a str,
    bytes: &'a [u8],
}

fn jpeg<'a>(id: &'a str, bytes: &'a [u8]) -> Media<'a> {
    Media {
        id,
        role: "image",
        mime: "image/jpeg",
        bytes,
    }
}

/// Writes the library object and the catalog rows of one entry; returns the digests.
async fn add_entry(
    core: &Core,
    id: &str,
    title: &str,
    body: &str,
    published: &str,
    media: &[Media<'_>],
) -> Vec<String> {
    let mut digests = Vec::new();
    for m in media {
        let digest = sha(m.bytes);
        let path = core.objects.object_path(&digest).unwrap();
        std::fs::create_dir_all(path.parent().unwrap()).unwrap();
        std::fs::write(path, m.bytes).unwrap();
        digests.push(digest);
    }
    let rows: Vec<(String, String, String, String, String, i64)> = media
        .iter()
        .zip(&digests)
        .map(|(m, d)| {
            (
                m.id.to_owned(),
                m.role.to_owned(),
                m.mime.to_owned(),
                d.clone(),
                format!("v-{}", m.id),
                m.bytes.len() as i64,
            )
        })
        .collect();
    let (id, title, body, published) = (
        id.to_owned(),
        title.to_owned(),
        body.to_owned(),
        published.to_owned(),
    );
    core.db
        .write(move |tx| {
            tx.execute(
                "INSERT OR IGNORE INTO source_accounts(account_id,namespace,native_account_key,display_name,first_seen_at) VALUES('a','weibo','100','作者:甲','t')",
                [],
            )?;
            tx.execute(
                "INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at,published_at) VALUES(?1,'weibo','post',?1,'a','t',?2)",
                rusqlite::params![id, published],
            )?;
            let revision = format!("r-{id}");
            tx.execute(
                "INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at) VALUES(?1,?2,'d',?3,?4,'2026-10-16T00:00:00Z')",
                rusqlite::params![revision, id, title, body],
            )?;
            tx.execute(
                "INSERT INTO entry_meta(entry_id,site,canonical_url,updated_at) VALUES(?1,'weibo',?2,'t')",
                rusqlite::params![id, format!("https://weibo.com/100/{id}")],
            )?;
            for (position, (media_id, role, mime, digest, variant, size)) in rows.iter().enumerate() {
                let blob = format!("sha256:{digest}");
                tx.execute(
                    "INSERT INTO media_items(media_id,revision_id,role,position,source_attachment_id) VALUES(?1,?2,?3,?4,?1)",
                    rusqlite::params![media_id, revision, role, position as i64],
                )?;
                tx.execute(
                    "INSERT OR IGNORE INTO blobs(blob_id,size,mime,first_seen_at) VALUES(?1,?2,?3,'t')",
                    rusqlite::params![blob, size, mime],
                )?;
                tx.execute(
                    "INSERT INTO media_variants(variant_id,media_id,source_representation,blob_id,claimed_spec,measured_spec,created_at) VALUES(?1,?2,'orig',?3,'{}','{}','t')",
                    rusqlite::params![variant, media_id, blob],
                )?;
                tx.execute(
                    "INSERT OR IGNORE INTO replicas(replica_id,blob_id,node_id,store_id,locator,managed,state,verified_at) VALUES(?1,?2,'local','primary',?3,1,'verified','t')",
                    rusqlite::params![format!("rp-{digest}"), blob, digest],
                )?;
                tx.execute(
                    "INSERT INTO asset_selections(media_id,variant_id,policy_version,updated_at) VALUES(?1,?2,'1','t')",
                    rusqlite::params![media_id, variant],
                )?;
            }
            Ok(())
        })
        .await
        .unwrap();
    digests
}

async fn export(fx: &Fx, ids: &[&str]) -> Vec<ExportItem> {
    export_to(fx, ids, &fx.root).await.unwrap().items
}

async fn export_to(
    fx: &Fx,
    ids: &[&str],
    dir: &Path,
) -> Result<trace_contracts::download::ExportReport, CoreError> {
    let ids: Vec<String> = ids.iter().map(|s| (*s).to_owned()).collect();
    export_entries(&fx.core, &ids, Some(&dir.to_string_lossy())).await
}

/// Every file under `root` by relative path.
fn tree(root: &Path) -> BTreeMap<String, Vec<u8>> {
    let mut files = BTreeMap::new();
    let mut pending = vec![root.to_path_buf()];
    while let Some(dir) = pending.pop() {
        let Ok(read) = std::fs::read_dir(&dir) else {
            continue;
        };
        for entry in read.flatten() {
            let path = entry.path();
            if path.is_dir() {
                pending.push(path);
            } else {
                let rel = path
                    .strip_prefix(root)
                    .unwrap()
                    .to_string_lossy()
                    .replace('\\', "/");
                files.insert(rel, std::fs::read(&path).unwrap());
            }
        }
    }
    files
}

async fn call(core: &Arc<Core>, command: &str, args: Value) -> Result<Value, CoreError> {
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

const SINGLE: &str = "微博/作者 甲/2026-10-15 标题 [e1].jpg";
const SINGLE_JSON: &str = "微博/作者 甲/2026-10-15 标题 [e1].json";

#[tokio::test]
async fn a_single_media_entry_becomes_one_readable_file_with_a_sidecar_and_records() {
    let fx = fixture().await;
    let digests = add_entry(
        &fx.core,
        "e1",
        "标题",
        "正文",
        "2026-10-15T08:00:00Z",
        &[jpeg("m1", b"jpeg bytes")],
    )
    .await;
    let items = export(&fx, &["e1"]).await;
    assert_eq!(items[0].state, "exported", "{:?}", items[0]);
    let files = tree(&fx.root);
    assert_eq!(
        files.keys().map(String::as_str).collect::<Vec<_>>(),
        [SINGLE, SINGLE_JSON]
    );
    assert_eq!(files[SINGLE], b"jpeg bytes");
    let side: Value = serde_json::from_slice(&files[SINGLE_JSON]).unwrap();
    assert_eq!(side["site"], "weibo");
    assert_eq!(side["site_title"], "微博");
    assert_eq!(side["entry"]["id"], "e1");
    assert_eq!(side["entry"]["url"], "https://weibo.com/100/e1");
    assert_eq!(side["entry"]["text"], "正文");
    assert_eq!(side["files"][0]["sha256"], digests[0]);
    let text = String::from_utf8_lossy(&files[SINGLE_JSON]).to_lowercase();
    let local = fx
        .root
        .to_string_lossy()
        .to_lowercase()
        .replace('\\', "\\\\");
    assert!(!text.contains(&local) && !text.contains("cookie"), "{text}");
    // Records: media and sidecar, present, with the verified digest.
    let copies = copies(&fx.core, "e1").await.unwrap();
    assert_eq!(copies.len(), 2);
    assert!(copies.iter().all(|c| c.state == "present"));
    assert_eq!(
        copies.iter().find(|c| c.role == "media").unwrap().rel_path,
        SINGLE.replace('/', "\\")
    );
    // No unfinished file is left behind.
    assert!(!fx.root.join(write::TEMP_DIR).exists());
}

#[tokio::test]
async fn several_media_make_a_folder_with_numbered_files_and_the_text() {
    let fx = fixture().await;
    add_entry(
        &fx.core,
        "e1",
        "合集",
        "这是正文",
        "2026-10-15T08:00:00Z",
        &[
            jpeg("m1", b"one"),
            jpeg("m2", b"two"),
            Media {
                id: "m3",
                role: "video",
                mime: "video/mp4",
                bytes: b"three",
            },
        ],
    )
    .await;
    export(&fx, &["e1"]).await;
    let files = tree(&fx.root);
    let names: Vec<&str> = files.keys().map(String::as_str).collect();
    assert_eq!(
        names,
        [
            "微博/作者 甲/2026-10-15 合集 [e1].json",
            "微博/作者 甲/2026-10-15 合集 [e1]/01.jpg",
            "微博/作者 甲/2026-10-15 合集 [e1]/02.jpg",
            "微博/作者 甲/2026-10-15 合集 [e1]/03.mp4",
            "微博/作者 甲/2026-10-15 合集 [e1]/正文.txt",
        ]
    );
    assert_eq!(
        files["微博/作者 甲/2026-10-15 合集 [e1]/正文.txt"],
        "这是正文".as_bytes()
    );
}

#[tokio::test]
async fn running_again_changes_nothing_and_a_deleted_file_is_written_back_in_place() {
    let fx = fixture().await;
    add_entry(
        &fx.core,
        "e1",
        "标题",
        "",
        "2026-10-15T08:00:00Z",
        &[jpeg("m1", b"jpeg bytes")],
    )
    .await;
    export(&fx, &["e1"]).await;
    let before = tree(&fx.root);
    let again = export(&fx, &["e1"]).await;
    assert_eq!(again[0].state, "unchanged", "{:?}", again[0]);
    assert_eq!(tree(&fx.root), before);
    std::fs::remove_file(fx.root.join(SINGLE.replace('/', "\\"))).unwrap();
    let repaired = export(&fx, &["e1"]).await;
    assert_eq!(repaired[0].state, "exported");
    assert_eq!(tree(&fx.root), before);
    assert_eq!(
        copies(&fx.core, "e1").await.unwrap().len(),
        2,
        "no duplicate records"
    );
}

#[tokio::test]
async fn a_changed_title_keeps_writing_to_the_recorded_names() {
    let fx = fixture().await;
    add_entry(
        &fx.core,
        "e1",
        "旧标题",
        "",
        "2026-10-15T08:00:00Z",
        &[jpeg("m1", b"jpeg bytes")],
    )
    .await;
    export(&fx, &["e1"]).await;
    fx.core
        .db
        .write(|tx| {
            tx.execute("UPDATE entry_revisions SET title='新标题'", [])?;
            Ok(())
        })
        .await
        .unwrap();
    let items = export(&fx, &["e1"]).await;
    assert_ne!(items[0].state, "failed");
    let names: Vec<_> = tree(&fx.root).into_keys().collect();
    assert_eq!(names.len(), 2, "{names:?}");
    assert!(names.iter().all(|n| n.contains("旧标题")), "{names:?}");
}

#[tokio::test]
async fn edited_and_deleted_files_are_detected_and_never_overwritten() {
    let fx = fixture().await;
    add_entry(
        &fx.core,
        "e1",
        "标题",
        "",
        "2026-10-15T08:00:00Z",
        &[jpeg("m1", b"jpeg bytes")],
    )
    .await;
    export(&fx, &["e1"]).await;
    let clean = check(&fx.core, None, None).await.unwrap();
    assert_eq!(
        (clean.checked, clean.present, clean.changed, clean.missing),
        (2, 2, 0, 0)
    );
    // Same length, different content: only the digest tells.
    let media = fx.root.join(SINGLE.replace('/', "\\"));
    std::fs::write(&media, b"JPEG BYTES").unwrap();
    let side = fx.root.join(SINGLE_JSON.replace('/', "\\"));
    std::fs::remove_file(&side).unwrap();
    let report = check(&fx.core, None, Some("e1")).await.unwrap();
    assert_eq!((report.changed, report.missing), (1, 1), "{report:?}");
    let states: BTreeMap<_, _> = report
        .problems
        .iter()
        .map(|p| (p.role.as_str(), p.state.as_str()))
        .collect();
    assert_eq!(states["media"], "changed");
    assert_eq!(states["sidecar"], "missing");
    // Exporting again leaves the edited file alone and restores the missing sidecar.
    let items = export(&fx, &["e1"]).await;
    assert_eq!(std::fs::read(&media).unwrap(), b"JPEG BYTES");
    let media_state = items[0].files.iter().find(|f| f.role == "media").unwrap();
    assert_eq!(media_state.state, "changed");
    assert!(side.exists());
    // Once the user puts the original back, the next check is clean again.
    std::fs::write(&media, b"jpeg bytes").unwrap();
    let healed = check(&fx.core, None, None).await.unwrap();
    assert_eq!((healed.changed, healed.missing), (0, 0), "{healed:?}");
}

#[tokio::test]
async fn a_file_the_user_already_has_under_the_name_is_never_replaced() {
    let fx = fixture().await;
    add_entry(
        &fx.core,
        "e1",
        "标题",
        "",
        "2026-10-15T08:00:00Z",
        &[jpeg("m1", b"jpeg bytes")],
    )
    .await;
    let mine = fx.root.join(SINGLE.replace('/', "\\"));
    std::fs::create_dir_all(mine.parent().unwrap()).unwrap();
    std::fs::write(&mine, b"my own file").unwrap();
    let items = export(&fx, &["e1"]).await;
    assert_eq!(items[0].state, "exported", "{:?}", items[0]);
    assert_eq!(std::fs::read(&mine).unwrap(), b"my own file");
    let files = tree(&fx.root);
    assert_eq!(files.len(), 3, "{:?}", files.keys().collect::<Vec<_>>());
    let written = items[0].files.iter().find(|f| f.role == "media").unwrap();
    assert!(written.rel_path.contains('~'), "{}", written.rel_path);
    assert_eq!(files[&written.rel_path.replace('\\', "/")], b"jpeg bytes");
}

#[tokio::test]
async fn hostile_titles_and_authors_stay_inside_the_folder() {
    let fx = fixture().await;
    add_entry(
        &fx.core,
        "e1",
        "..\\..\\evil:*?\"<>|",
        "",
        "../../x",
        &[jpeg("m1", b"a")],
    )
    .await;
    add_entry(
        &fx.core,
        "e2",
        "CON",
        "",
        "2026-10-15T08:00:00Z",
        &[jpeg("m2", b"b")],
    )
    .await;
    fx.core
        .db
        .write(|tx| {
            tx.execute(
                "UPDATE source_accounts SET display_name='..\\..\\Windows'",
                [],
            )?;
            Ok(())
        })
        .await
        .unwrap();
    let items = export(&fx, &["e1", "e2"]).await;
    assert!(items.iter().all(|i| i.state == "exported"), "{items:?}");
    let root = std::fs::canonicalize(&fx.root).unwrap();
    for rel in tree(&fx.root).keys() {
        let path = std::fs::canonicalize(fx.root.join(rel.replace('/', "\\"))).unwrap();
        assert!(path.starts_with(&root), "{rel}");
        assert!(
            !rel.split('/').any(|s| s == ".." || s.ends_with(['.', ' '])),
            "{rel}"
        );
    }
    assert!(!fx.root.parent().unwrap().join("evil").exists());
}

#[tokio::test]
async fn a_path_that_cannot_fit_fails_before_anything_is_written() {
    let fx = fixture().await;
    add_entry(
        &fx.core,
        "e1",
        "标题",
        "",
        "2026-10-15T08:00:00Z",
        &[jpeg("m1", b"jpeg bytes")],
    )
    .await;
    let base = fx.root.to_string_lossy().encode_utf16().count();
    let deep = fx.root.join("x".repeat(250usize.saturating_sub(base + 1)));
    let items = export_to(&fx, &["e1"], &deep).await.unwrap().items;
    assert_eq!(items[0].state, "failed");
    assert!(
        items[0].message.as_deref().unwrap().contains("路径过长"),
        "{:?}",
        items[0]
    );
    assert!(tree(&deep).is_empty());
    assert!(copies(&fx.core, "e1").await.unwrap().is_empty());
}

#[tokio::test]
async fn a_folder_that_cannot_take_files_fails_the_whole_request_up_front() {
    let fx = fixture().await;
    add_entry(
        &fx.core,
        "e1",
        "标题",
        "",
        "2026-10-15T08:00:00Z",
        &[jpeg("m1", b"jpeg bytes")],
    )
    .await;
    // A file where the folder should be.
    std::fs::write(&fx.root, b"not a folder").unwrap();
    let err = export_to(&fx, &["e1"], &fx.root).await.unwrap_err();
    assert!(matches!(err, CoreError::InvalidInput(_)), "{err}");
    assert!(copies(&fx.core, "e1").await.unwrap().is_empty());
    // Inside the library or the data folder is refused too.
    for inside in [
        fx.core.objects.archive_root().join("export"),
        fx.core.profile.data_root.join("export"),
    ] {
        let err = export_to(&fx, &["e1"], &inside).await.unwrap_err();
        assert!(err.to_string().contains("重叠"), "{err}");
        assert!(!inside.exists());
    }
    // Relative and parent-relative paths are not folders.
    for bad in ["downloads", "..\\x", "C:\\a\\..\\b", ""] {
        let err = export_entries(&fx.core, &["e1".to_owned()], Some(bad))
            .await
            .unwrap_err();
        assert!(matches!(err, CoreError::InvalidInput(_)), "{bad}: {err}");
    }
}

#[tokio::test]
async fn too_little_space_is_reported_before_writing() {
    let fx = fixture().await;
    add_entry(
        &fx.core,
        "e1",
        "标题",
        "",
        "2026-10-15T08:00:00Z",
        &[jpeg("m1", b"jpeg bytes")],
    )
    .await;
    fx.core
        .db
        .write(|tx| {
            tx.execute("UPDATE blobs SET size=1125899906842624", [])?;
            Ok(())
        })
        .await
        .unwrap();
    let items = export(&fx, &["e1"]).await;
    assert_eq!(items[0].state, "failed");
    assert!(
        items[0].message.as_deref().unwrap().contains("空间不足"),
        "{:?}",
        items[0]
    );
    assert!(tree(&fx.root).is_empty());
}

#[tokio::test]
async fn a_damaged_library_object_never_reaches_the_folder_as_a_finished_file() {
    let fx = fixture().await;
    let digests = add_entry(
        &fx.core,
        "e1",
        "标题",
        "",
        "2026-10-15T08:00:00Z",
        &[jpeg("m1", b"jpeg bytes")],
    )
    .await;
    // Same length, wrong bytes: the verification after the copy must catch it.
    std::fs::write(
        fx.core.objects.object_path(&digests[0]).unwrap(),
        b"JPEG BYTES",
    )
    .unwrap();
    // A leftover of an interrupted earlier run.
    std::fs::create_dir_all(fx.root.join(write::TEMP_DIR)).unwrap();
    std::fs::write(fx.root.join(write::TEMP_DIR).join("old.part"), b"half").unwrap();
    let items = export(&fx, &["e1"]).await;
    assert_eq!(items[0].state, "failed", "{:?}", items[0]);
    assert!(
        tree(&fx.root).is_empty(),
        "{:?}",
        tree(&fx.root).keys().collect::<Vec<_>>()
    );
    assert!(copies(&fx.core, "e1").await.unwrap().is_empty());
}

#[tokio::test]
async fn entries_without_saved_files_say_so_and_deferred_media_keep_their_number() {
    let fx = fixture().await;
    add_entry(
        &fx.core,
        "e1",
        "标题",
        "",
        "2026-10-15T08:00:00Z",
        &[jpeg("m1", b"a"), jpeg("m2", b"b")],
    )
    .await;
    // m1 is still deferred: no stored bytes for it.
    fx.core
        .db
        .write(|tx| {
            tx.execute("DELETE FROM asset_selections WHERE media_id='m1'", [])?;
            tx.execute("DELETE FROM media_variants WHERE media_id='m1'", [])?;
            Ok(())
        })
        .await
        .unwrap();
    let items = export(&fx, &["e1"]).await;
    assert_eq!(items[0].state, "exported");
    let names: Vec<_> = tree(&fx.root).into_keys().collect();
    assert!(
        names.iter().any(|n| n.ends_with("[e1]/02.jpg")),
        "{names:?}"
    );
    assert!(!names.iter().any(|n| n.ends_with("/01.jpg")), "{names:?}");
    // Nothing at all to write.
    add_entry(
        &fx.core,
        "e3",
        "空",
        "",
        "2026-10-15T08:00:00Z",
        &[jpeg("m9", b"z")],
    )
    .await;
    fx.core
        .db
        .write(|tx| {
            tx.execute("DELETE FROM asset_selections WHERE media_id='m9'", [])?;
            tx.execute("DELETE FROM media_variants WHERE media_id='m9'", [])?;
            Ok(())
        })
        .await
        .unwrap();
    let nothing = export(&fx, &["e3", "missing-entry"]).await;
    assert_eq!(nothing[0].state, "nothing");
    assert_eq!(nothing[1].state, "failed");
}

#[tokio::test]
async fn a_text_only_entry_is_written_as_one_text_file() {
    let fx = fixture().await;
    add_entry(
        &fx.core,
        "t1",
        "只有文字",
        "今天的想法",
        "2026-10-15T08:00:00Z",
        &[],
    )
    .await;
    export(&fx, &["t1"]).await;
    let files = tree(&fx.root);
    assert_eq!(
        files["微博/作者 甲/2026-10-15 只有文字 [t1].txt"],
        "今天的想法".as_bytes()
    );
}

#[tokio::test]
async fn sync_dedupes_when_the_library_has_the_bytes_and_restores_when_it_does_not() {
    let fx = fixture().await;
    let a = add_entry(
        &fx.core,
        "e1",
        "甲",
        "",
        "2026-10-15T08:00:00Z",
        &[jpeg("m1", b"shared bytes")],
    )
    .await;
    add_entry(
        &fx.core,
        "e2",
        "乙",
        "",
        "2026-10-15T08:00:00Z",
        &[jpeg("m2", b"shared bytes")],
    )
    .await;
    export(&fx, &["e1", "e2"]).await;
    let objects = |core: &Core| tree(&core.objects.objects_dir()).len();
    let report = sync_to_library(&fx.core, &["e1".into(), "e2".into()])
        .await
        .unwrap();
    assert!(
        report.items.iter().all(|i| i.state == "already_in_library"),
        "{report:?}"
    );
    assert_eq!(objects(&fx.core), 1, "same bytes are stored once");

    // The library loses the object; both entries share it, so the first sync refills it
    // and the second finds it there.
    let object = fx.core.objects.object_path(&a[0]).unwrap();
    std::fs::remove_file(&object).unwrap();
    fx.core
        .db
        .write(|tx| {
            tx.execute("UPDATE replicas SET state='absent',verified_at=NULL", [])?;
            Ok(())
        })
        .await
        .unwrap();
    let report = sync_to_library(&fx.core, &["e1".into(), "e2".into()])
        .await
        .unwrap();
    assert_eq!(report.items[0].state, "restored", "{report:?}");
    assert_eq!(report.items[1].state, "already_in_library", "{report:?}");
    assert_eq!(std::fs::read(&object).unwrap(), b"shared bytes");
    let state: String = fx
        .core
        .db
        .read(|c| Ok(c.query_row("SELECT state FROM replicas LIMIT 1", [], |r| r.get(0))?))
        .await
        .unwrap();
    assert_eq!(state, "verified");
    // The download folder keeps its files.
    assert!(tree(&fx.root).keys().any(|k| k.ends_with("甲 [e1].jpg")));
}

#[tokio::test]
async fn sync_replaces_a_damaged_object_and_keeps_the_damaged_one_aside() {
    let fx = fixture().await;
    let digests = add_entry(
        &fx.core,
        "e1",
        "标题",
        "",
        "2026-10-15T08:00:00Z",
        &[jpeg("m1", b"good bytes")],
    )
    .await;
    export(&fx, &["e1"]).await;
    let object = fx.core.objects.object_path(&digests[0]).unwrap();
    std::fs::write(&object, b"rotten byt").unwrap();
    fx.core
        .db
        .write(|tx| {
            tx.execute("UPDATE replicas SET state='corrupt',verified_at=NULL", [])?;
            Ok(())
        })
        .await
        .unwrap();
    let report = sync_to_library(&fx.core, &["e1".into()]).await.unwrap();
    assert_eq!(report.items[0].state, "restored", "{report:?}");
    assert_eq!(std::fs::read(&object).unwrap(), b"good bytes");
    assert_eq!(tree(&fx.core.objects.trash_dir()).len(), 1);
}

#[tokio::test]
async fn sync_refuses_a_copy_that_was_edited_or_removed() {
    let fx = fixture().await;
    let digests = add_entry(
        &fx.core,
        "e1",
        "标题",
        "",
        "2026-10-15T08:00:00Z",
        &[jpeg("m1", b"good bytes")],
    )
    .await;
    export(&fx, &["e1"]).await;
    std::fs::remove_file(fx.core.objects.object_path(&digests[0]).unwrap()).unwrap();
    fx.core
        .db
        .write(|tx| {
            tx.execute("UPDATE replicas SET state='absent',verified_at=NULL", [])?;
            Ok(())
        })
        .await
        .unwrap();
    let media = fx.root.join(SINGLE.replace('/', "\\"));
    std::fs::write(&media, b"GOOD BYTES").unwrap();
    let report = sync_to_library(&fx.core, &["e1".into()]).await.unwrap();
    assert_eq!(report.items[0].state, "skipped", "{report:?}");
    assert!(report.items[0].message.as_deref().unwrap().contains("改动"));
    assert!(!fx.core.objects.object_path(&digests[0]).unwrap().exists());
    std::fs::remove_file(&media).unwrap();
    let report = sync_to_library(&fx.core, &["e1".into()]).await.unwrap();
    assert_eq!(report.items[0].state, "skipped");
    let none = sync_to_library(&fx.core, &["nope".into()]).await.unwrap();
    assert_eq!(none.items[0].state, "skipped");
}

#[tokio::test]
async fn forgetting_removes_records_and_keeps_files() {
    let fx = fixture().await;
    add_entry(
        &fx.core,
        "e1",
        "标题",
        "",
        "2026-10-15T08:00:00Z",
        &[jpeg("m1", b"jpeg bytes")],
    )
    .await;
    export(&fx, &["e1"]).await;
    let report = forget(&fx.core, "e1", None).await.unwrap();
    assert_eq!(report.removed, 2);
    assert!(copies(&fx.core, "e1").await.unwrap().is_empty());
    assert_eq!(tree(&fx.root).len(), 2);
}

#[tokio::test]
async fn an_interrupted_write_record_without_a_file_is_dropped_by_the_next_check() {
    let fx = fixture().await;
    add_entry(
        &fx.core,
        "e1",
        "标题",
        "",
        "2026-10-15T08:00:00Z",
        &[jpeg("m1", b"jpeg bytes")],
    )
    .await;
    export(&fx, &["e1"]).await;
    // The process died after the record was made and before the final rename.
    std::fs::remove_file(fx.root.join(SINGLE.replace('/', "\\"))).unwrap();
    fx.core
        .db
        .write(|tx| {
            tx.execute(
                "UPDATE external_copies SET state='writing' WHERE role='media'",
                [],
            )?;
            Ok(())
        })
        .await
        .unwrap();
    let report = check(&fx.core, None, None).await.unwrap();
    assert!(
        report.problems.iter().all(|p| p.role != "media"),
        "{report:?}"
    );
    assert_eq!(copies(&fx.core, "e1").await.unwrap().len(), 1);
    let items = export(&fx, &["e1"]).await;
    assert_eq!(items[0].state, "exported");
    assert_eq!(copies(&fx.core, "e1").await.unwrap().len(), 2);
}

#[tokio::test]
async fn settings_choose_the_default_folder_recent_folders_and_the_sidecar() {
    let fx = fixture().await;
    let initial = settings(&fx.core).await.unwrap();
    assert!(initial.sidecar);
    assert!(
        initial.default_dir.ends_with("TRACE"),
        "{}",
        initial.default_dir
    );
    let chosen = fx.root.join("mine");
    let after = set_settings(
        &fx.core,
        DownloadSettingsArgs {
            default_dir: Some(chosen.to_string_lossy().into_owned()),
            sidecar: Some(false),
        },
    )
    .await
    .unwrap();
    assert_eq!(PathBuf::from(&after.default_dir), chosen);
    assert!(!after.sidecar);
    assert!(chosen.is_dir(), "a usable default is created and probed");
    assert!(!chosen.join(write::TEMP_DIR).exists());
    // A run without a folder goes to the default; no sidecar is written.
    add_entry(
        &fx.core,
        "e1",
        "标题",
        "",
        "2026-10-15T08:00:00Z",
        &[jpeg("m1", b"jpeg bytes")],
    )
    .await;
    let report = export_entries(&fx.core, &["e1".into()], None)
        .await
        .unwrap();
    assert!(report.folder.is_default);
    assert_eq!(tree(&chosen).keys().collect::<Vec<_>>(), [SINGLE]);
    // Another folder for one run joins the recent list; the default stays.
    let other = fx.root.join("other");
    export_to(&fx, &["e1"], &other).await.unwrap();
    let recent = settings(&fx.core).await.unwrap().recent;
    assert_eq!(recent.len(), 2);
    assert!(recent[0].is_default);
    // The same folder written in another letter case is one folder.
    let upper = PathBuf::from(other.to_string_lossy().to_uppercase());
    export_to(&fx, &["e1"], &upper).await.unwrap();
    assert_eq!(settings(&fx.core).await.unwrap().recent.len(), 2);
}

#[tokio::test]
async fn the_commands_route_to_the_same_use_cases() {
    let fx = fixture().await;
    add_entry(
        &fx.core,
        "e1",
        "标题",
        "",
        "2026-10-15T08:00:00Z",
        &[jpeg("m1", b"jpeg bytes")],
    )
    .await;
    let dir = fx.root.to_string_lossy().into_owned();
    let report = call(
        &fx.core,
        "download.export",
        json!({"entry_ids":["e1"],"dir":dir}),
    )
    .await
    .unwrap();
    assert_eq!(report["items"][0]["state"], "exported");
    let copies = call(&fx.core, "download.copies", json!({"entry_id":"e1"}))
        .await
        .unwrap();
    assert_eq!(copies.as_array().unwrap().len(), 2);
    let checked = call(&fx.core, "download.check", json!({})).await.unwrap();
    assert_eq!(checked["present"], 2);
    let synced = call(&fx.core, "download.sync", json!({"entry_ids":["e1"]}))
        .await
        .unwrap();
    assert_eq!(synced["items"][0]["state"], "already_in_library");
    let forgot = call(&fx.core, "download.forget", json!({"entry_id":"e1"}))
        .await
        .unwrap();
    assert_eq!(forgot["removed"], 2);
    let view = call(&fx.core, "download.settings", json!({}))
        .await
        .unwrap();
    assert!(view["sidecar"].as_bool().unwrap());
    assert!(
        call(&fx.core, "download.export", json!({"entry_ids":[]}))
            .await
            .is_err()
    );
    assert!(
        call(
            &fx.core,
            "download.export",
            json!({"entry_ids":["e1"],"extra":1})
        )
        .await
        .is_err()
    );
    assert!(call(&fx.core, "download.unknown", json!({})).await.is_err());
    assert!(trace_contracts::control::commands::is_ui_command(
        "download.export"
    ));
}
