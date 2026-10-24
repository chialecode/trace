//! Explicit, isolated synthetic archive for ACT-003: one creator with an account on each of the
//! eight sites, entries of every content form, a playable video, an entry whose media was never
//! saved, videos the subscription setting left undownloaded, and a download folder with present
//! and missing external copies. Never accepts an existing directory; no network, credential or
//! real subscription is involved.
use std::path::PathBuf;
use trace_core::{
    config::Profile,
    core::Core,
    objects::{CommitInfo, ReceiveParams},
};
use trace_domain::ids::OperationId;

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let target = PathBuf::from(
        std::env::args()
            .nth(1)
            .ok_or("usage: prepare_acceptance <new-directory>")?,
    );
    trace_core::paths::no_links(&target)?;
    if target.exists() {
        return Err("refusing existing directory; choose a new isolated target".into());
    }
    std::fs::create_dir_all(&target)?;
    let target = target.canonicalize()?;
    let profile = Profile {
        archive_id: format!("synthetic-{}", uuid::Uuid::new_v4()),
        data_root: target.join("data"),
        archive_root: target.join("archive"),
        vault_dir: Some(target.join("private-vault")),
        ..Profile::default()
    };
    let core = Core::open(profile.clone()).await?;
    let ffmpeg = core
        .media_backend
        .ffmpeg
        .as_ref()
        .ok_or("run python scripts/fetch_media.py first")?;
    let sample = target.join("synthetic-avc-aac.mp4");
    let mut command = std::process::Command::new(ffmpeg);
    command
        .args([
            "-v",
            "error",
            "-f",
            "lavfi",
            "-i",
            "testsrc2=size=640x360:rate=25",
            "-f",
            "lavfi",
            "-i",
            "sine=frequency=660:sample_rate=48000",
            "-t",
            "12",
            "-c:v",
            "libopenh264",
            "-b:v",
            "800k",
            "-c:a",
            "aac",
            "-movflags",
            "+faststart",
        ])
        .arg(&sample);
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        command.creation_flags(0x0800_0000);
    }
    let result = command.output()?;
    if !result.status.success() {
        return Err("synthetic media generation failed".into());
    }
    let cover = target.join("synthetic-cover.png");
    let mut command = std::process::Command::new(ffmpeg);
    command
        .args(["-v", "error", "-i"])
        .arg(&sample)
        .args(["-frames:v", "1"])
        .arg(&cover);
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        command.creation_flags(0x0800_0000);
    }
    if !command.output()?.status.success() {
        return Err("synthetic cover generation failed".into());
    }
    let downloads = target.join("downloads");
    std::fs::create_dir_all(downloads.join("bilibili"))?;
    let folder = downloads.clone();
    core.db.write(move |tx| seed_catalog(tx, &folder)).await?;
    for (id, path, mime) in [
        ("playable", sample.clone(), "video/mp4"),
        ("cover", cover.clone(), "image/png"),
    ] {
        let op = OperationId::new(format!("synthetic-{id}"));
        let mut writer = core
            .objects
            .start_receiving(
                &core.db,
                ReceiveParams {
                    job_id: None,
                    subject_kind: "media_variant".into(),
                    subject_id: id.into(),
                    generation: 1,
                    mime: Some(mime.into()),
                    expected_size: Some(path.metadata()?.len() as i64),
                    expected_sha256: None,
                    source_validator: None,
                },
                &op,
            )
            .await?;
        use std::io::Read;
        let mut file = std::fs::File::open(&path)?;
        let mut buf = vec![0; 256 * 1024];
        loop {
            let n = file.read(&mut buf)?;
            if n == 0 {
                break;
            }
            writer.write_chunk(&core.db, &op, &buf[..n]).await?;
        }
        let (_, sha) = core
            .objects
            .finish_and_validate(&core.db, &op, writer)
            .await?;
        core.objects.install_object(&core.db, &op, &sha).await?;
        let spec = core.media_backend.probe(&path, &core.cancel).await?;
        core.objects
            .commit(
                &core.db,
                &op,
                &CommitInfo {
                    job: None,
                    media_id: id.into(),
                    source_representation: "synthetic-acceptance".into(),
                    claimed_spec: serde_json::json!({}),
                },
                Some(serde_json::to_value(spec)?),
            )
            .await?;
    }
    seed_copies(&core, &sample, &cover, &downloads).await?;
    trace_core::search::index_pending(&core.db, 100).await?;
    std::fs::write(
        target.join("profile.json"),
        serde_json::to_vec_pretty(&profile)?,
    )?;
    let backup = trace_core::storage_ops::backup(&core, &target.join("backups"), true).await?;
    let summary = serde_json::json!({
        "synthetic": true,
        "profile": target.join("profile.json"),
        "backup": backup,
        "entries": ENTRY_COUNT,
        "sites": 8,
        "download_folder": downloads,
        "real_site_requests": 0,
        "test_allowed_hosts": profile.test_allowed_hosts,
    });
    std::fs::write(
        target.join("acceptance.json"),
        serde_json::to_vec_pretty(&summary)?,
    )?;
    println!("{}", serde_json::to_string_pretty(&summary)?);
    core.shutdown().await;
    Ok(())
}

/// `(key, native kind, title, body, form, image count, video count, tags)`.
type Row = (
    &'static str,
    &'static str,
    &'static str,
    &'static str,
    &'static str,
    i64,
    i64,
    &'static str,
);
/// `(site, account name, entries)`.
const SITE_CONTENT: [(&str, &str, [Row; 2]); 8] = [
    (
        "weibo",
        "合成微博账号",
        [
            (
                "wb-1",
                "post",
                "合成微博：带三张图片的短文",
                "只有合成文字，用来检查中文排版与图片数量显示。",
                "image",
                3,
                0,
                "合成,微博",
            ),
            (
                "wb-2",
                "post",
                "合成微博：纯文字",
                "没有图片和视频的帖子。",
                "text",
                0,
                0,
                "合成",
            ),
        ],
    ),
    (
        "xiaohongshu",
        "合成小红书账号",
        [
            (
                "xhs-1",
                "note",
                "合成笔记：图文",
                "多张图片的图文笔记，图片尚未保存。",
                "image",
                4,
                0,
                "合成,图文",
            ),
            (
                "xhs-2",
                "note",
                "合成笔记：视频",
                "视频笔记，订阅设置为不自动下载。",
                "video",
                0,
                1,
                "合成,视频",
            ),
        ],
    ),
    (
        "instagram",
        "合成 Instagram 账号",
        [
            (
                "ig-1",
                "post",
                "合成帖子：轮播",
                "三张图片的轮播。",
                "image",
                3,
                0,
                "合成",
            ),
            (
                "ig-2",
                "post",
                "合成帖子：短视频",
                "竖屏短视频。",
                "video",
                0,
                1,
                "合成,短视频",
            ),
        ],
    ),
    (
        "x",
        "合成 X 账号",
        [
            (
                "x-1",
                "post",
                "合成推文：带标签的文字",
                "含 #合成 标签的文字推文。",
                "text",
                0,
                0,
                "合成",
            ),
            (
                "x-2",
                "post",
                "合成推文：一张图片",
                "一张图片的推文。",
                "image",
                1,
                0,
                "合成,图片",
            ),
        ],
    ),
    (
        "youtube",
        "合成 YouTube 频道",
        [
            (
                "yt-1",
                "video",
                "合成频道视频：长视频",
                "订阅不自动下载，视频显示“未下载”。",
                "video",
                0,
                1,
                "合成",
            ),
            (
                "yt-2",
                "post",
                "合成频道社区帖子",
                "社区帖子的文字。",
                "text",
                0,
                0,
                "合成,社区",
            ),
        ],
    ),
    (
        "tiktok",
        "合成 TikTok 账号",
        [
            (
                "tt-1",
                "video",
                "合成短视频一",
                "TikTok 风格的短视频。",
                "video",
                0,
                1,
                "合成",
            ),
            (
                "tt-2",
                "video",
                "合成短视频二",
                "第二条短视频。",
                "video",
                0,
                1,
                "合成",
            ),
        ],
    ),
    (
        "douyin",
        "合成抖音账号",
        [
            (
                "dy-1",
                "video",
                "合成抖音视频",
                "抖音视频。",
                "video",
                0,
                1,
                "合成",
            ),
            (
                "dy-2",
                "note",
                "合成抖音图文",
                "抖音图文，两张图片。",
                "image",
                2,
                0,
                "合成,图文",
            ),
        ],
    ),
    (
        "bilibili",
        "合成哔哩哔哩账号",
        [
            (
                "playable",
                "video",
                "合成媒体：12 秒 AVC/AAC 播放与跳播",
                "隔离合成样本；不会采集真实平台。",
                "video",
                0,
                1,
                "合成,播放",
            ),
            (
                "missing",
                "video",
                "合成缺失媒体：应显示尚未保存，不能伪装完整",
                "隔离合成样本；不会采集真实平台。",
                "video",
                0,
                1,
                "合成",
            ),
        ],
    ),
];
const ENTRY_COUNT: usize = 16;
const SEEDED_AT: &str = "2026-10-07T00:00:00Z";

/// One creator with an account on every site and the synthetic entries of each. A video the
/// subscription setting left alone is "deferred"; `playable` and `missing` are the entries the
/// older sample had, kept under their ids so the manual steps still find them.
fn seed_catalog(
    tx: &rusqlite::Transaction<'_>,
    downloads: &std::path::Path,
) -> Result<(), trace_core::error::CoreError> {
    tx.execute("INSERT INTO creators(creator_id,display_name,created_at,updated_at,notes) VALUES('synthetic-creator','合成验收创作者',?1,?1,'隔离验收档案；无真实订阅或凭证')", [SEEDED_AT])?;
    let mut day = 1;
    for (site, account_name, entries) in SITE_CONTENT {
        let account = format!("synthetic-account-{site}");
        tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,display_name,first_seen_at) VALUES(?1,?2,'acceptance',?3,?4)", rusqlite::params![account, site, account_name, SEEDED_AT])?;
        tx.execute("INSERT INTO account_bindings(binding_id,creator_id,account_id,created_at) VALUES(?1,'synthetic-creator',?2,?3)", rusqlite::params![format!("b-{site}"), account, SEEDED_AT])?;
        for (key, kind, title, body, _form, images, videos, tags) in entries {
            day += 1;
            let stamp = format!("2026-10-{day:02}T08:00:00Z");
            let precision = if key == "yt-2" { "day" } else { "second" };
            tx.execute("INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,published_at,published_precision,first_seen_at) VALUES(?1,?2,?3,?1,?4,?5,?6,?7)", rusqlite::params![key, site, kind, account, stamp, precision, SEEDED_AT])?;
            tx.execute("INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,completeness,observed_at) VALUES(?1,?1,?1,?2,?3,'full',?4)", rusqlite::params![key, title, body, SEEDED_AT])?;
            let mut kinds = vec!["text"];
            if images > 0 {
                kinds.push("image");
            }
            if videos > 0 {
                kinds.push("video");
            }
            let kinds = serde_json::to_string(&kinds).unwrap_or_default();
            let url = format!("https://example.invalid/{site}/{key}");
            let duration = (videos > 0).then_some(12_000);
            tx.execute("INSERT INTO entry_meta(entry_id,site,revision_id,canonical_url,content_kinds,image_count,video_count,max_duration_ms,likes,plays,updated_at) VALUES(?1,?2,?1,?3,?4,?5,?6,?7,?8,?9,?10)", rusqlite::params![key, site, url, kinds, images, videos, duration, 10 + day, 1000 + day * 7, SEEDED_AT])?;
            for tag in tags.split(',') {
                tx.execute(
                    "INSERT INTO entry_terms(entry_id,kind,value) VALUES(?1,'tag',?2)",
                    rusqlite::params![key, tag],
                )?;
            }
            for position in 0..images {
                tx.execute("INSERT INTO media_items(media_id,revision_id,role,position,source_attachment_id,required,fetch_state) VALUES(?1,?2,'image',?3,?1,1,'deferred')", rusqlite::params![format!("{key}-image-{position}"), key, position])?;
            }
            if videos > 0 {
                // Only `playable` is stored; `missing` was wanted and never arrived; the rest were
                // left alone by the subscription setting.
                let state = if key == "playable" || key == "missing" {
                    "wanted"
                } else {
                    "deferred"
                };
                tx.execute("INSERT INTO media_items(media_id,revision_id,role,position,source_attachment_id,group_key,required,fetch_state) VALUES(?1,?1,'video',0,?1,?1,1,?2)", rusqlite::params![key, state])?;
            }
            trace_core::objects::outbox_emit(
                tx,
                "entry.revision.created",
                key,
                &serde_json::json!({"revision_id": key}),
            )?;
        }
    }
    tx.execute("INSERT INTO media_items(media_id,revision_id,role,position,source_attachment_id,required) VALUES('cover','playable','cover',0,'cover',0)", [])?;
    tx.execute("INSERT INTO storage_locations(location_id,kind,path,label,is_default,state,created_at) VALUES('synthetic-download','download',?1,'合成下载文件夹',1,'active',?2)", rusqlite::params![downloads.to_string_lossy(), SEEDED_AT])?;
    Ok(())
}

/// Readable copies of the playable video in the download folder: the video and its note are
/// present; the cover is recorded but its file is not there, so a check marks it "missing".
async fn seed_copies(
    core: &Core,
    sample: &std::path::Path,
    cover: &std::path::Path,
    downloads: &std::path::Path,
) -> Result<(), Box<dyn std::error::Error>> {
    use sha2::{Digest, Sha256};
    let video_file = downloads.join("bilibili").join("synthetic-playable.mp4");
    std::fs::copy(sample, &video_file)?;
    let note_file = downloads
        .join("bilibili")
        .join("synthetic-playable.trace.json");
    let note = serde_json::json!({"synthetic": true, "entry": "playable", "title": "合成媒体：12 秒 AVC/AAC 播放与跳播"});
    std::fs::write(&note_file, serde_json::to_vec_pretty(&note)?)?;
    let digest = |path: &std::path::Path| -> std::io::Result<(i64, String)> {
        let bytes = std::fs::read(path)?;
        let hex = Sha256::digest(&bytes)
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect::<String>();
        Ok((bytes.len() as i64, hex))
    };
    let rows = [
        (
            "copy-video",
            Some("playable"),
            "bilibili/synthetic-playable.mp4",
            "media",
            digest(&video_file)?,
        ),
        (
            "copy-note",
            None,
            "bilibili/synthetic-playable.trace.json",
            "sidecar",
            digest(&note_file)?,
        ),
        (
            "copy-cover",
            Some("cover"),
            "bilibili/synthetic-playable-cover.png",
            "media",
            digest(cover)?,
        ),
    ];
    core.db
        .write(move |tx| {
            for (id, media, rel, role, (size, sha)) in rows {
                tx.execute("INSERT INTO external_copies(copy_id,entry_id,media_id,location_id,rel_path,role,size,sha256,state,created_at) VALUES(?1,'playable',?2,'synthetic-download',?3,?4,?5,?6,'present',?7)", rusqlite::params![id, media, rel, role, size, sha, SEEDED_AT])?;
            }
            Ok(())
        })
        .await?;
    // The cover's file is deliberately absent from the folder: the check records it as missing.
    let report = trace_core::downloads::check(core, None, None).await?;
    if report.present != 2 || report.missing != 1 {
        return Err(format!("synthetic copies did not check as planned: {report:?}").into());
    }
    Ok(())
}
