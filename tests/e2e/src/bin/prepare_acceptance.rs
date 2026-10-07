//! Explicit, isolated synthetic archive for ACT-003. Never accepts an existing directory.
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
    core.db.write(|tx| {
        tx.execute("INSERT INTO creators(creator_id,display_name,created_at,updated_at,notes) VALUES('synthetic-creator','合成验收创作者','2026-10-07T00:00:00Z','2026-10-07T00:00:00Z','隔离验收档案；无真实订阅或凭证')",[])?;
        tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,display_name,first_seen_at) VALUES('synthetic-account','synthetic','acceptance','离线合成来源','2026-10-07T00:00:00Z')",[])?;
        tx.execute("INSERT INTO account_bindings(binding_id,creator_id,account_id,created_at) VALUES('b','synthetic-creator','synthetic-account','2026-10-07T00:00:00Z')",[])?;
        for (id,title) in [("playable","合成媒体：12 秒 AVC/AAC 播放与跳播"),("missing","合成缺失媒体：应显示尚未保存，不能伪装完整")] {
            tx.execute("INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,published_at,published_precision,first_seen_at) VALUES(?1,'synthetic','video',?1,'synthetic-account','2026-10-07T00:00:00Z','second','2026-10-07T00:00:00Z')",[id])?;
            tx.execute("INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,completeness,observed_at) VALUES(?1,?1,?1,?2,'隔离合成样本；不会采集真实平台。','full','2026-10-07T00:00:00Z')",rusqlite::params![id,title])?;
            tx.execute("INSERT INTO media_items(media_id,revision_id,role,position,source_attachment_id,group_key,required) VALUES(?1,?1,'video',0,?1,?1,1)",[id])?;
            trace_core::objects::outbox_emit(tx,"entry.revision.created",id,&serde_json::json!({"revision_id":id}))?;
        }
        tx.execute("INSERT INTO media_items(media_id,revision_id,role,position,source_attachment_id,required) VALUES('cover','playable','cover',0,'cover',0)",[])?;
        Ok(())
    }).await?;
    for (id, path, mime) in [
        ("playable", sample, "video/mp4"),
        ("cover", cover, "image/png"),
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
    trace_core::search::index_pending(&core.db, 100).await?;
    std::fs::write(
        target.join("profile.json"),
        serde_json::to_vec_pretty(&profile)?,
    )?;
    let backup = trace_core::storage_ops::backup(&core, &target.join("backups"), true).await?;
    let summary = serde_json::json!({"synthetic":true,"profile":target.join("profile.json"),"backup":backup,"entries":2,"real_site_requests":0,"test_allowed_hosts":profile.test_allowed_hosts});
    std::fs::write(
        target.join("acceptance.json"),
        serde_json::to_vec_pretty(&summary)?,
    )?;
    println!("{}", serde_json::to_string_pretty(&summary)?);
    core.shutdown().await;
    Ok(())
}
