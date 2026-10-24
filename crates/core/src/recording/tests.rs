use super::*;
use std::sync::Arc;

const SELECT_LIVE_FRAGMENTS: &str = "SELECT sequence FROM live_fragments WHERE session_id=?1 AND state IN ('sealed','gap') ORDER BY sequence";
const INSERT_CREDENTIALS_STATE: &str = "INSERT INTO credentials_state(credential_id,platform,vault_ref,status,created_at,updated_at) VALUES('login','bilibili','unused','valid','t','t')";
const SELECT_JOBS: &str = "SELECT count(*) FROM jobs WHERE job_type='auth_completion'";

#[tokio::test]
async fn fragment_authentication_actual_specs_gaps_restart_export_and_no_backfill() {
    use trace_contracts::acquisition::{AcquisitionContext, AuthenticationState as Auth};
    let (dir, core, scan) = fixture().await;
    let job = recording_job(&core, &scan).await;
    let sid = job.subject_id.clone();
    let entry = core
        .db
        .read(move |c| {
            Ok(c.query_row(
                "SELECT entry_id FROM live_sessions WHERE session_id=?1",
                [sid],
                |r| r.get::<_, String>(0),
            )?)
        })
        .await
        .unwrap();
    for (state, size) in [
        (Auth::Anonymous, "64x64"),
        (Auth::Authenticated, "128x72"),
        (Auth::Unknown, "64x64"),
    ] {
        let context = AcquisitionContext {
            context_id: format!("ctx-{}", uuid::Uuid::new_v4()),
            auth_state: state,
            credential_id: None,
            credential_generation: None,
            validated_at: None,
            basis: "isolated_fragment_context".into(),
            started_at: jobs::now(),
        };
        let e = entry.clone();
        let c = context.clone();
        let guard = job.clone();
        core.db
            .write(move |tx| crate::acquisitions::insert_context(tx, &guard, &e, &c))
            .await
            .unwrap();
        let mut scoped = job.clone();
        scoped.payload["acquisition_context_id"] = json!(context.context_id);
        let bytes = flv_sample(
            &core,
            &dir.path().join(format!("{}.flv", state.as_str())),
            size,
        );
        let (url, server) = http_bytes(bytes).await;
        receive(
            &core,
            &scoped,
            &LiveStream {
                protocol: "flv".into(),
                url,
                quality: "declared-original".into(),
                audio_only: false,
                public_headers: json!({}),
            },
        )
        .await
        .unwrap();
        server.abort();
    }
    let context = AcquisitionContext {
        context_id: "expired-gap".into(),
        auth_state: Auth::Expired,
        credential_id: None,
        credential_generation: None,
        validated_at: None,
        basis: "expired_at_boundary".into(),
        started_at: jobs::now(),
    };
    let e = entry.clone();
    let guard = job.clone();
    core.db
        .write(move |tx| crate::acquisitions::insert_context(tx, &guard, &e, &context))
        .await
        .unwrap();
    let mut expired = job.clone();
    expired.payload["acquisition_context_id"] = json!("expired-gap");
    gap(
        &core,
        &expired,
        "expired-boundary".into(),
        "authentication_expired",
    )
    .await
    .unwrap();
    let id = job.subject_id.clone();
    let facts = core
        .db
        .read(move |c| {
            let rows = c
                .prepare(SELECT_LIVE_FRAGMENTS)?
                .query_map([&id], |r| r.get::<_, i64>(0))?
                .collect::<Result<Vec<_>, _>>()?;
            rows.into_iter()
                .map(|seq| crate::acquisitions::fragment(c, &id, seq))
                .collect::<Result<Vec<_>, _>>()
        })
        .await
        .unwrap();
    for state in ["anonymous", "authenticated", "unknown", "expired"] {
        assert!(
            facts.iter().any(|v| v["authentication"] == state),
            "{state}: {facts:?}"
        );
    }
    for height in [64, 72] {
        assert!(facts.iter().any(|v| v["actual_spec"]["height"] == height));
    }
    assert!(facts.iter().any(|v| {
        v["actual_spec"]["bitrate_bps"]
            .as_i64()
            .is_some_and(|n| n > 0)
    }));
    core.db
        .write(|tx| {
            tx.execute(INSERT_CREDENTIALS_STATE, [])?;
            tx.execute("UPDATE subscriptions SET credential_id='login'", [])?;
            crate::upgrades::schedule(tx, "login", 1)?;
            assert_eq!(tx.query_row(SELECT_JOBS, [], |r| r.get::<_, i64>(0))?, 0);
            Ok(())
        })
        .await
        .unwrap();
    let profile = core.profile.clone();
    core.shutdown().await;
    drop(core);
    let core = Core::open(profile).await.unwrap();
    let e = entry.clone();
    assert_eq!(
        core.db
            .read(move |c| crate::acquisitions::summary(c, &e))
            .await
            .unwrap()["state"],
        "mixed"
    );
    let output = dir.path().join("live-export");
    crate::archive_export::export(&core, &output, false, None)
        .await
        .unwrap();
    let folder = std::fs::read_dir(&output)
        .unwrap()
        .filter_map(Result::ok)
        .find(|f| f.path().is_dir())
        .unwrap()
        .path();
    let lines = std::fs::read_to_string(folder.join("live.jsonl")).unwrap();
    assert!(
        lines.contains("authenticated")
            && lines.contains("expired")
            && lines.contains("bitrate_bps")
    );
    core.shutdown().await;
}
pub(super) async fn fixture() -> (tempfile::TempDir, Arc<Core>, ClaimedJob) {
    fixture_rotation(600).await
}
async fn fixture_rotation(seconds: u64) -> (tempfile::TempDir, Arc<Core>, ClaimedJob) {
    let dir = tempfile::tempdir().unwrap();
    let profile = crate::config::Profile {
        archive_id: "m1-live-tests".into(),
        data_root: dir.path().join("data"),
        archive_root: dir.path().join("archive"),
        vault_dir: Some(dir.path().join("vault")),
        test_allowed_hosts: vec!["127.0.0.1".into()],
        recording_fragment_seconds: seconds,
        ..Default::default()
    };
    let core = Core::open(profile).await.unwrap();
    core.db.write(|tx|{tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('a','bilibili','7','now')",[])?;tx.execute("INSERT INTO subscriptions(subscription_id,account_id,content_type,range_json,created_at,updated_at) VALUES('s','a','live','{}','now','now')",[])?;Ok(())}).await.unwrap();
    jobs::enqueue(
        &core.db,
        "scan:s:incremental".into(),
        "scan",
        "s",
        json!({"subscription_id":"s"}),
        0,
        1,
        0,
    )
    .await
    .unwrap();
    let job = jobs::claim_due(&core.db, "test", 1)
        .await
        .unwrap()
        .remove(0);
    job.mark_running(&core.db).await.unwrap();
    (dir, core, job)
}
pub(super) fn snapshot() -> LiveSnapshot {
    LiveSnapshot {
        room_key: "7".into(),
        state: "live".into(),
        source_session: Some("1700000000".into()),
        title: "录制测试".into(),
        metadata: json!({}),
        streams: vec![],
    }
}

async fn recording_job(core: &Core, job: &ClaimedJob) -> ClaimedJob {
    register(core, job, snapshot()).await.unwrap();
    let record = jobs::claim_due(&core.db, "test", 1)
        .await
        .unwrap()
        .remove(0);
    record.mark_running(&core.db).await.unwrap();
    record
}

pub(crate) fn flv_sample(core: &Core, path: &std::path::Path, size: &str) -> Vec<u8> {
    let status = std::process::Command::new(core.media_backend.ffmpeg.as_ref().unwrap())
        .args([
            "-hide_banner",
            "-loglevel",
            "error",
            "-f",
            "lavfi",
            "-i",
            &format!("testsrc=size={size}:rate=10"),
            "-t",
            "3.6",
            "-c:v",
            "libopenh264",
            "-pix_fmt",
            "yuv420p",
            "-g",
            "10",
            "-f",
            "flv",
        ])
        .arg(path)
        .status()
        .unwrap();
    assert!(status.success());
    std::fs::read(path).unwrap()
}
async fn http_bytes(bytes: Vec<u8>) -> (String, tokio::task::JoinHandle<()>) {
    use tokio::io::{AsyncReadExt, AsyncWriteExt};
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let url = format!("http://{}/stream", listener.local_addr().unwrap());
    let server = tokio::spawn(async move {
        loop {
            let (mut socket, _) = listener.accept().await.unwrap();
            let bytes = bytes.clone();
            tokio::spawn(async move {
                let mut req = [0; 4096];
                let _ = socket.read(&mut req).await;
                let _ = socket
                    .write_all(
                        format!(
                            "HTTP/1.1 200 OK\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
                            bytes.len()
                        )
                        .as_bytes(),
                    )
                    .await;
                for part in bytes.chunks(8192) {
                    if socket.write_all(part).await.is_err() {
                        break;
                    }
                }
            });
        }
    });
    (url, server)
}

const SELECT_LIVE_FRAGMENTS_2: &str = "SELECT f.source_sequence,COALESCE(f.reason,''),COALESCE(m.received_sha256,'') FROM live_fragments f JOIN materializations m ON m.subject_id=f.media_id";
const SELECT_LIVE_FRAGMENTS_3: &str = "SELECT f.start_ms,f.end_ms,b.blob_id FROM live_fragments f JOIN media_items m ON m.media_id=f.media_id JOIN asset_selections a ON a.media_id=m.media_id JOIN media_variants v ON v.variant_id=a.variant_id JOIN blobs b ON b.blob_id=v.blob_id WHERE f.state='sealed' ORDER BY f.sequence";
const SELECT_MEDIA_VARIANTS: &str = "SELECT v.blob_id FROM media_variants v JOIN media_items m ON m.media_id=v.media_id WHERE m.role='recording_source'";
const SELECT_LIVE_FRAGMENTS_4: &str =
    "SELECT count(*) FROM live_fragments WHERE state='gap' AND reason=?1";

#[tokio::test]
async fn flv_recording_rotation_clock_codec_sleep_and_disk_full() {
    let (dir, mut core, scan) = fixture_rotation(1).await;
    let job = recording_job(&core, &scan).await;
    let bytes = flv_sample(&core, &dir.path().join("one.flv"), "64x64");
    let second = flv_sample(&core, &dir.path().join("two.flv"), "96x96");
    let (url, server) = http_bytes(bytes.clone()).await;
    let stream = LiveStream {
        protocol: "flv".into(),
        url,
        quality: "origin".into(),
        audio_only: false,
        public_headers: json!({}),
    };
    let received = receive(&core, &job, &stream).await;
    if received.is_err() {
        let rows: Vec<(String, String, String)> = core
            .db
            .read(|c| {
                Ok(c.prepare(SELECT_LIVE_FRAGMENTS_2)?
                    .query_map([], |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)))?
                    .collect::<Result<Vec<_>, _>>()?)
            })
            .await
            .unwrap();
        for (source, reason, sha) in rows {
            eprintln!("{source} {reason} {sha}");
            if !sha.is_empty() {
                let output =
                    std::process::Command::new(core.media_backend.ffprobe.as_ref().unwrap())
                        .args(["-v", "error", "-show_streams"])
                        .arg(core.objects.object_path(&sha).unwrap())
                        .output()
                        .unwrap();
                eprintln!(
                    "{} {}",
                    String::from_utf8_lossy(&output.stdout),
                    String::from_utf8_lossy(&output.stderr)
                );
            }
        }
    }
    assert!(!received.unwrap());
    server.abort();
    let parts: Vec<(i64, i64, String)> = core
        .db
        .read(|c| {
            Ok(c.prepare(SELECT_LIVE_FRAGMENTS_3)?
                .query_map([], |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)))?
                .collect::<Result<Vec<_>, _>>()?)
        })
        .await
        .unwrap();
    assert!(
        parts.len() >= 3,
        "source timestamps should rotate without reconnecting"
    );
    assert_eq!(parts[0].0, 0);
    assert!(parts.last().unwrap().1 >= 3400);
    assert!(
        parts.windows(2).all(|p| p[1].0 - p[0].1 <= 100),
        "no dropped keyframe at rotation"
    );
    for (_, _, blob) in &parts {
        let sha = blob.strip_prefix("sha256:").unwrap();
        assert_eq!(
            crate::objects::hash_file(&core.objects.object_path(sha).unwrap()).unwrap(),
            sha
        );
    }
    let raw: String = core
        .db
        .read(|c| Ok(c.query_row(SELECT_MEDIA_VARIANTS, [], |r| r.get(0))?))
        .await
        .unwrap();
    assert_eq!(
        std::fs::read(
            core.objects
                .object_path(raw.strip_prefix("sha256:").unwrap())
                .unwrap()
        )
        .unwrap(),
        bytes
    );
    for scenario in ["clock", "codec", "sleep"] {
        let first = flv::Framer::default().push(&bytes).unwrap();
        let next = flv::Framer::default()
            .push(if scenario == "codec" { &second } else { &bytes })
            .unwrap();
        let mut mux = flv_core::Muxer::new();
        let mut joined = vec![];
        mux.write_header(false, true, &mut joined);
        for tag in first {
            mux.write_tag(&tag, &mut joined).unwrap();
        }
        for mut tag in next {
            if scenario == "codec" {
                tag.timestamp_ms += 4000;
            } else if scenario == "sleep" {
                tag.timestamp_ms += 70000;
            }
            mux.write_tag(&tag, &mut joined).unwrap();
        }
        let (url, server) = http_bytes(joined).await;
        let mut other = stream.clone();
        other.url = url;
        receive(&core, &job, &other).await.unwrap();
        server.abort();
        let why = match scenario {
            "clock" => "source_clock_reset",
            "codec" => "codec_configuration_changed",
            _ => "source_timestamp_gap",
        };
        let count: i64 = core
            .db
            .read(move |c| Ok(c.query_row(SELECT_LIVE_FRAGMENTS_4, [why], |r| r.get(0))?))
            .await
            .unwrap();
        assert!(count > 0, "{why}");
    }
    Arc::get_mut(&mut core)
        .unwrap()
        .profile
        .recording_disk_reserve_bytes = u64::MAX;
    let (url, server) = http_bytes(bytes).await;
    let mut blocked = stream;
    blocked.url = url;
    assert!(receive(&core, &job, &blocked).await.is_err());
    server.abort();
    for (_, _, blob) in parts {
        let sha = blob.strip_prefix("sha256:").unwrap();
        assert_eq!(
            crate::objects::hash_file(&core.objects.object_path(sha).unwrap()).unwrap(),
            sha
        );
    }
    core.shutdown().await;
}

#[tokio::test]
async fn closed_capture_four_commit_windows_survive_cancel_and_restart() {
    let (dir, core, scan) = fixture().await;
    let job = recording_job(&core, &scan).await;
    let bytes = flv_sample(&core, &dir.path().join("one.flv"), "64x64");
    for window in ["receiving", "prepared", "object_ready", "committed"] {
        let mut f = begin(&core, &job, window.into(), "video/x-flv", 0)
            .await
            .unwrap()
            .unwrap();
        f.writer.write_chunk(&core.db, &f.op, &bytes).await.unwrap();
        if window == "receiving" {
            drop(f);
            continue;
        }
        let fid = f.id.clone();
        core.db
            .write(move |tx| {
                tx.execute(
                    "UPDATE live_fragments SET end_ms=3600,evidence='{}' WHERE fragment_id=?1",
                    [fid],
                )?;
                Ok(())
            })
            .await
            .unwrap();
        let (_, sha) = core
            .objects
            .finish_and_validate(&core.db, &f.op, f.writer)
            .await
            .unwrap();
        if window == "prepared" {
            continue;
        }
        core.objects
            .install_object(&core.db, &f.op, &sha)
            .await
            .unwrap();
        if window == "object_ready" {
            continue;
        }
        core.objects
            .commit(
                &core.db,
                &f.op,
                &CommitInfo {
                    job: Some(job.clone()),
                    media_id: f.media,
                    source_representation: "recorded-source".into(),
                    claimed_spec: json!({}),
                },
                Some(validate_fragment(&core, &sha).await.unwrap()),
            )
            .await
            .unwrap();
    }
    assert_eq!(
        recover_closed(&core).await.unwrap(),
        0,
        "cannot adopt a running worker's objects"
    );
    let id = job.job_id.clone();
    core.db.write(move|tx|{tx.execute("UPDATE jobs SET state='cancelled',target_generation=target_generation+1,lease_owner=NULL,lease_expires_at=NULL WHERE job_id=?1",[id])?;Ok(())}).await.unwrap();
    let profile = core.profile.clone();
    core.shutdown().await;
    drop(core);
    let reopened = Core::open(profile).await.unwrap();
    reopened.recover().await.unwrap();
    assert_eq!(
        recover_closed(&reopened).await.unwrap(),
        0,
        "recovery is idempotent"
    );
    let counts: (i64, i64, i64) = reopened
        .db
        .read(|c| {
            Ok((
                c.query_row(
                    "SELECT count(*) FROM live_fragments WHERE state='sealed'",
                    [],
                    |r| r.get(0),
                )?,
                c.query_row("SELECT count(*) FROM media_variants", [], |r| r.get(0))?,
                c.query_row(
                    "SELECT count(*) FROM jobs WHERE job_type='record' AND state='cancelled'",
                    [],
                    |r| r.get(0),
                )?,
            ))
        })
        .await
        .unwrap();
    assert_eq!(counts, (3, 3, 1));
    assert!(
        job.check(&rusqlite::Connection::open(reopened.profile.db_path()).unwrap())
            .is_err()
    );
    reopened.shutdown().await;
}

const SELECT_LIVE_FRAGMENTS_5: &str = "SELECT json_extract(evidence,'$.track'),start_ms,end_ms,source_sequence FROM live_fragments WHERE state='sealed' ORDER BY source_sequence";
const SELECT_LIVE_FRAGMENTS_6: &str =
    "SELECT count(*) FROM live_fragments WHERE state='superseded'";
const SELECT_LIVE_SESSIONS: &str =
    "SELECT json_extract(evidence,'$.subtitle_capture.state') FROM live_sessions";
const SELECT_MEDIA_ITEMS: &str = "SELECT substr(v.blob_id,8) FROM media_items m JOIN media_variants v ON v.media_id=m.media_id WHERE m.role='subtitle'";
const SELECT_LIVE_FRAGMENTS_7: &str = "SELECT json_array_length(f.evidence,'$.source_media_ids') FROM live_fragments f JOIN media_items m ON m.media_id=f.media_id WHERE m.role='subtitle' ORDER BY f.sequence DESC LIMIT 1";

#[tokio::test]
async fn recording_commit_windows_and_independent_hls_audio() {
    use tokio::io::{AsyncReadExt, AsyncWriteExt};
    let (dir, core, scan) = fixture().await;
    let job = recording_job(&core, &scan).await;
    let video_path = dir.path().join("video.ts");
    let audio_path = dir.path().join("audio.ts");
    for (path, source, encoder) in [
        (&video_path, "testsrc=size=64x64:rate=10", "mpeg2video"),
        (&audio_path, "sine=frequency=440", "aac"),
    ] {
        let status = std::process::Command::new(core.media_backend.ffmpeg.as_ref().unwrap())
            .args([
                "-hide_banner",
                "-loglevel",
                "error",
                "-f",
                "lavfi",
                "-i",
                source,
                "-t",
                "1",
                if encoder == "aac" { "-c:a" } else { "-c:v" },
                encoder,
                "-f",
                "mpegts",
            ])
            .arg(path)
            .status()
            .unwrap();
        assert!(status.success());
    }
    let video = Arc::new(std::fs::read(video_path).unwrap());
    let audio = Arc::new(std::fs::read(audio_path).unwrap());
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let url = format!("http://{}/master.m3u8", listener.local_addr().unwrap());
    let corrupt = Arc::new(std::sync::atomic::AtomicBool::new(true));
    let fail = corrupt.clone();
    let subtitle_bad = Arc::new(std::sync::atomic::AtomicBool::new(true));
    let bad_subtitle = subtitle_bad.clone();
    let subtitle_map = Arc::new(std::sync::atomic::AtomicBool::new(false));
    let with_map = subtitle_map.clone();
    let server = tokio::spawn(async move {
        loop {
            let (mut socket, _) = listener.accept().await.unwrap();
            let (video, audio, fail) = (video.clone(), audio.clone(), fail.clone());
            let bad_subtitle = bad_subtitle.clone();
            let with_map = with_map.clone();
            tokio::spawn(async move {
                let mut request = [0; 4096];
                let n = socket.read(&mut request).await.unwrap();
                let request = String::from_utf8_lossy(&request[..n]);
                let body = if request.contains("/master.m3u8") {
                    b"#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"audio\",NAME=\"main\",DEFAULT=YES,URI=\"audio.m3u8\"\n#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID=\"subs\",NAME=\"Chinese\",LANGUAGE=\"zh\",URI=\"subtitles.m3u8\"\n#EXT-X-STREAM-INF:BANDWIDTH=8000000,AUDIO=\"audio\",SUBTITLES=\"subs\"\nvideo.m3u8\n".to_vec()
                } else if request.contains("/subtitles.m3u8") && with_map.load(Ordering::SeqCst) {
                    b"#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:2\n#EXT-X-MAP:URI=\"header.vtt\"\n#EXTINF:1,\ncue.vtt\n#EXT-X-ENDLIST\n".to_vec()
                } else if request.contains("/header.vtt") {
                    b"WEBVTT\nX-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:90000\n".to_vec()
                } else if request.contains("/cue.vtt") {
                    "\n00:00:00.000 --> 00:00:01.000\n分离头中文字幕\n"
                        .as_bytes()
                        .to_vec()
                } else if request.contains("/subtitles.m3u8") {
                    b"#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:1\n#EXTINF:1,\ntext.vtt\n#EXT-X-ENDLIST\n".to_vec()
                } else if request.contains("/text.vtt") {
                    if bad_subtitle.load(Ordering::SeqCst) {
                        b"invalid".to_vec()
                    } else {
                        "WEBVTT\nX-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:90000\n\n00:00:00.000 --> 00:00:01.000\n中文字幕 <b>纯文本阅读</b>\n".as_bytes().to_vec()
                    }
                } else if request.contains(".m3u8") {
                    let track = if request.contains("/audio") {
                        "audio"
                    } else {
                        "video"
                    };
                    format!("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:1\n#EXTINF:1,\n{track}.ts\n#EXTINF:1,\n{track}.ts\n#EXT-X-ENDLIST\n").into_bytes()
                } else if request.contains("/audio.ts") {
                    if fail.load(Ordering::SeqCst) {
                        b"corrupt media".to_vec()
                    } else {
                        audio.to_vec()
                    }
                } else {
                    video.to_vec()
                };
                let _ = socket
                    .write_all(
                        format!(
                            "HTTP/1.1 200 OK\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
                            body.len()
                        )
                        .as_bytes(),
                    )
                    .await;
                let _ = socket.write_all(&body).await;
            });
        }
    });
    let stream = LiveStream {
        protocol: "hls".into(),
        url,
        quality: "origin".into(),
        audio_only: false,
        public_headers: json!({}),
    };
    assert!(receive(&core, &job, &stream).await.is_err());
    let first: i64 = core
        .db
        .read(|c| {
            Ok(c.query_row(
                "SELECT count(*) FROM live_fragments WHERE state='sealed'",
                [],
                |r| r.get(0),
            )?)
        })
        .await
        .unwrap();
    assert_eq!(
        first, 2,
        "video prefix is retained when independent audio is invalid"
    );
    corrupt.store(false, Ordering::SeqCst);
    assert!(receive(&core, &job, &stream).await.unwrap());
    let rows: Vec<(String, i64, i64, String)> = core
        .db
        .read(|c| {
            Ok(c.prepare(SELECT_LIVE_FRAGMENTS_5)?
                .query_map([], |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?, r.get(3)?)))?
                .collect::<Result<Vec<_>, _>>()?)
        })
        .await
        .unwrap();
    assert_eq!(rows.len(), 4);
    for track in ["main", "audio"] {
        let spans: Vec<_> = rows
            .iter()
            .filter(|r| r.0 == track)
            .map(|r| (r.1, r.2))
            .collect();
        assert_eq!(spans, vec![(0, 1000), (1000, 2000)]);
    }
    let superseded: i64 = core
        .db
        .read(|c| Ok(c.query_row(SELECT_LIVE_FRAGMENTS_6, [], |r| r.get(0))?))
        .await
        .unwrap();
    assert_eq!(
        superseded, 1,
        "failed audio remains linked as retry evidence"
    );
    assert!(receive(&core, &job, &stream).await.unwrap());
    let count: i64 = core
        .db
        .read(|c| {
            Ok(c.query_row(
                "SELECT count(*) FROM live_fragments WHERE state='sealed'",
                [],
                |r| r.get(0),
            )?)
        })
        .await
        .unwrap();
    assert_eq!(count, 4);
    core.db
        .write(|tx| {
            tx.execute("UPDATE subscriptions SET capture_subtitles=1", [])?;
            Ok(())
        })
        .await
        .unwrap();
    assert!(
        receive(&core, &job, &stream).await.unwrap(),
        "optional subtitle failure cannot fail completed A/V"
    );
    core.db
        .read(|c| {
            assert_eq!(
                c.query_row(SELECT_LIVE_SESSIONS, [], |r| r.get::<_, String>(0))?,
                "failed"
            );
            Ok(())
        })
        .await
        .unwrap();
    subtitle_bad.store(false, Ordering::SeqCst);
    for _ in 0..2 {
        assert!(receive(&core, &job, &stream).await.unwrap());
    }
    let sha: String = core
        .db
        .read(|c| {
            assert_eq!(
                c.query_row(
                    "SELECT count(*) FROM media_items WHERE role='subtitle'",
                    [],
                    |r| r.get::<_, i64>(0)
                )?,
                1,
                "same segment does not duplicate"
            );
            assert!(c.query_row(
                "SELECT required=0 FROM media_items WHERE role='subtitle'",
                [],
                |r| r.get::<_, bool>(0)
            )?);
            Ok(c.query_row(SELECT_MEDIA_ITEMS, [], |r| r.get(0))?)
        })
        .await
        .unwrap();
    let bytes = std::fs::read(core.objects.object_path(&sha).unwrap()).unwrap();
    assert!(String::from_utf8(bytes).unwrap().contains("中文字幕"));
    subtitle_map.store(true, Ordering::SeqCst);
    assert!(receive(&core, &job, &stream).await.unwrap());
    core.db
        .read(|c| {
            assert_eq!(
                c.query_row(
                    "SELECT count(*) FROM media_items WHERE role='subtitle'",
                    [],
                    |r| r.get::<_, i64>(0)
                )?,
                2
            );
            assert_eq!(
                c.query_row(SELECT_LIVE_FRAGMENTS_7, [], |r| r.get::<_, i64>(0))?,
                2,
                "raw MAP and segment both retained"
            );
            Ok(())
        })
        .await
        .unwrap();
    core.db
        .write(|tx| {
            tx.execute("UPDATE live_sessions SET state='stopping'", [])?;
            Ok(())
        })
        .await
        .unwrap();
    assert!(receive(&core, &job, &stream).await.unwrap());
    core.db
        .read(|c| {
            assert_eq!(
                c.query_row(
                    "SELECT count(*) FROM media_items WHERE role='subtitle'",
                    [],
                    |r| r.get::<_, i64>(0)
                )?,
                2
            );
            Ok(())
        })
        .await
        .unwrap();
    server.abort();
    core.shutdown().await;
}

#[tokio::test]
async fn nested_hls_private_network_key_map_and_credential_scopes() {
    use tokio::io::{AsyncReadExt, AsyncWriteExt};
    let (_dir, core, scan) = fixture().await;
    let job = recording_job(&core, &scan).await;
    for target in [
        "https://192.168.0.1/secret",
        "https://[::ffff:127.0.0.1]/secret",
        "file:///not-media",
    ] {
        for field in ["variant", "key", "map"] {
            let text = match field {
                "variant" => format!("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\n{target}\n"),
                "key" => format!(
                    "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-KEY:METHOD=AES-128,URI=\"{target}\",IV=0x01\n#EXTINF:1,\nsegment.ts\n"
                ),
                _ => format!(
                    "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MAP:URI=\"{target}\"\n#EXTINF:1,\nsegment.ts\n"
                ),
            };
            let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
            let url = format!("http://{}/index.m3u8", listener.local_addr().unwrap());
            let count = Arc::new(std::sync::atomic::AtomicUsize::new(0));
            let calls = count.clone();
            let server = tokio::spawn(async move {
                loop {
                    let (mut s, _) = listener.accept().await.unwrap();
                    let text = text.clone();
                    let calls = calls.clone();
                    tokio::spawn(async move {
                        let mut req = [0; 4096];
                        let n = s.read(&mut req).await.unwrap();
                        let req = String::from_utf8_lossy(&req[..n]);
                        assert!(!req.to_ascii_lowercase().contains("cookie:"));
                        calls.fetch_add(1, Ordering::SeqCst);
                        let bytes = if req.contains("/index.m3u8") {
                            text.into_bytes()
                        } else {
                            vec![0; 16]
                        };
                        let _=s.write_all(format!("HTTP/1.1 200 OK\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",bytes.len()).as_bytes()).await;
                        let _ = s.write_all(&bytes).await;
                    });
                }
            });
            let stream = LiveStream {
                protocol: "hls".into(),
                url,
                quality: "test".into(),
                audio_only: false,
                public_headers: json!({}),
            };
            assert!(
                receive(&core, &job, &stream).await.is_err(),
                "{field} {target}"
            );
            assert_eq!(
                count.load(Ordering::SeqCst),
                if field == "variant" { 1 } else { 2 }
            );
            let mut secret = stream.clone();
            secret.public_headers = json!({"Cookie":"must-never-be-sent"});
            assert!(receive(&core, &job, &secret).await.is_err());
            assert_eq!(
                count.load(Ordering::SeqCst),
                if field == "variant" { 1 } else { 2 }
            );
            server.abort();
        }
    }
    let sealed: i64 = core
        .db
        .read(|c| {
            Ok(c.query_row(
                "SELECT count(*) FROM live_fragments WHERE state='sealed'",
                [],
                |r| r.get(0),
            )?)
        })
        .await
        .unwrap();
    assert_eq!(sealed, 0);
    core.shutdown().await;
}

#[tokio::test]
async fn same_session_manual_stop_offline_and_late_generation() {
    let (_dir, core, job) = fixture().await;
    let id = register(&core, &job, snapshot()).await.unwrap().unwrap();
    assert_eq!(
        register(&core, &job, snapshot()).await.unwrap(),
        Some(id.clone())
    );
    let id2 = id.clone();
    core.db
        .write(move |tx| {
            tx.execute(
                "UPDATE live_sessions SET manual_stopped=1,state='stopping' WHERE session_id=?1",
                [id2],
            )?;
            Ok(())
        })
        .await
        .unwrap();
    assert!(register(&core, &job, snapshot()).await.unwrap().is_none());
    core.db
        .write(|tx| jobs::set_subscription_enabled(tx, "s", false))
        .await
        .unwrap();
    core.db
        .write(|tx| jobs::set_subscription_enabled(tx, "s", true))
        .await
        .unwrap();
    assert!(register(&core, &job, snapshot()).await.is_err());
    let count: i64 = core
        .db
        .read(|c| Ok(c.query_row("SELECT count(*) FROM live_sessions", [], |r| r.get(0))?))
        .await
        .unwrap();
    assert_eq!(count, 1);
    core.shutdown().await;
}

#[tokio::test]
async fn local_identity_upgrade_and_new_broadcast_preserve_stop_and_history() {
    let (_dir, core, job) = fixture().await;
    let mut local = snapshot();
    local.source_session = None;
    let id = register(&core, &job, local.clone()).await.unwrap().unwrap();
    assert_eq!(
        register(&core, &job, local).await.unwrap(),
        Some(id.clone())
    );
    let previous = core
        .db
        .read(|c| {
            Ok(c.query_row("SELECT entry_id FROM live_sessions", [], |r| {
                r.get::<_, String>(0)
            })?)
        })
        .await
        .unwrap();
    assert_eq!(
        register(&core, &job, snapshot()).await.unwrap(),
        Some(id.clone())
    );
    let entry = core
        .db
        .read(|c| {
            Ok(c.query_row("SELECT entry_id FROM live_sessions", [], |r| {
                r.get::<_, String>(0)
            })?)
        })
        .await
        .unwrap();
    assert_eq!(
        entry, previous,
        "identity upgrade cannot fork archive history"
    );
    core.db
        .write(|tx| {
            tx.execute(
                "UPDATE live_sessions SET manual_stopped=1,state='stopping'",
                [],
            )?;
            Ok(())
        })
        .await
        .unwrap();
    assert!(register(&core, &job, snapshot()).await.unwrap().is_none());
    let mut next = snapshot();
    next.source_session = Some("next-broadcast".into());
    let next_id = register(&core, &job, next.clone()).await.unwrap().unwrap();
    assert_ne!(next_id, id);
    assert_eq!(register(&core, &job, next).await.unwrap(), Some(next_id));
    let rows = core
        .db
        .read(|c| {
            Ok((
                c.query_row("SELECT count(*) FROM live_sessions", [], |r| {
                    r.get::<_, i64>(0)
                })?,
                c.query_row(
                    "SELECT count(*) FROM live_sessions WHERE ended_at IS NULL",
                    [],
                    |r| r.get::<_, i64>(0),
                )?,
                c.query_row(
                    "SELECT stop_reason FROM live_sessions WHERE manual_stopped=1",
                    [],
                    |r| r.get::<_, String>(0),
                )?,
            ))
        })
        .await
        .unwrap();
    assert_eq!(rows, (2, 1, "source_session_replaced".into()));
    core.shutdown().await;
}

#[tokio::test]
async fn real_ts_ingestion_dedup_and_missing_window_evidence() {
    use tokio::io::{AsyncReadExt, AsyncWriteExt};
    let (dir, core, job) = fixture().await;
    let sid = register(&core, &job, snapshot()).await.unwrap().unwrap();
    let job = jobs::claim_due(&core.db, "test", 1)
        .await
        .unwrap()
        .remove(0);
    assert_eq!(job.subject_id, sid);
    job.mark_running(&core.db).await.unwrap();
    let path = dir.path().join("source.ts");
    let status = std::process::Command::new(
        core.media_backend
            .ffmpeg
            .as_ref()
            .expect("pinned media backend needed"),
    )
    .args([
        "-hide_banner",
        "-loglevel",
        "error",
        "-f",
        "lavfi",
        "-i",
        "testsrc=size=64x64:rate=10",
        "-t",
        "1",
        "-c:v",
        "mpeg2video",
        "-f",
        "mpegts",
    ])
    .arg(&path)
    .status()
    .unwrap();
    assert!(status.success());
    let bytes = Arc::new(std::fs::read(&path).unwrap());
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let base = format!("http://{}", listener.local_addr().unwrap());
    let sequence = Arc::new(std::sync::atomic::AtomicU64::new(1));
    let server_seq = sequence.clone();
    let data = bytes.clone();
    let server = tokio::spawn(async move {
        loop {
            let (mut socket, _) = listener.accept().await.unwrap();
            let data = data.clone();
            let seq = server_seq.load(Ordering::SeqCst);
            tokio::spawn(async move {
                let mut request = [0u8; 4096];
                let n = socket.read(&mut request).await.unwrap();
                let req = String::from_utf8_lossy(&request[..n]);
                let body = if req.contains("/index.m3u8") {
                    format!("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:{seq}\n#EXTINF:1,\nsegment.ts\n").into_bytes()
                } else {
                    data.to_vec()
                };
                socket
                    .write_all(
                        format!(
                            "HTTP/1.1 200 OK\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
                            body.len()
                        )
                        .as_bytes(),
                    )
                    .await
                    .unwrap();
                socket.write_all(&body).await.unwrap();
            });
        }
    });
    let stream = LiveStream {
        protocol: "hls".into(),
        url: format!("{base}/index.m3u8"),
        quality: "test".into(),
        audio_only: false,
        public_headers: json!({}),
    };
    assert!(!receive(&core, &job, &stream).await.unwrap());
    assert!(!receive(&core, &job, &stream).await.unwrap());
    sequence.store(4, Ordering::SeqCst);
    receive(&core, &job, &stream).await.unwrap();
    let counts:(i64,i64)=core.db.read(|c|Ok((c.query_row("SELECT count(*) FROM live_fragments WHERE state='sealed'",[],|r|r.get(0))?,c.query_row("SELECT count(*) FROM live_fragments WHERE state='gap' AND reason='segments_slid_out_of_window'",[],|r|r.get(0))?))).await.unwrap();
    assert_eq!(counts, (2, 1));
    let blobs: i64 = core
        .db
        .read(|c| Ok(c.query_row("SELECT count(*) FROM blobs", [], |r| r.get(0))?))
        .await
        .unwrap();
    assert_eq!(
        blobs, 1,
        "identical segment bytes reuse one content-addressed object"
    );
    server.abort();
    core.shutdown().await;
}

#[tokio::test]
async fn strict_quality_gap_survives_recovery_and_fallback_keeps_measured_media() {
    let (dir, core, scan) = fixture().await;
    core.db.write(|tx|{tx.execute("UPDATE subscriptions SET quality_json='{\"min_height\":1080,\"strict\":true}' WHERE subscription_id='s'",[])?;Ok(())}).await.unwrap();
    let job = recording_job(&core, &scan).await;
    let bytes = flv_sample(&core, &dir.path().join("quality.flv"), "64x64");
    let (url, server) = http_bytes(bytes).await;
    let stream = LiveStream {
        protocol: "flv".into(),
        url,
        quality: "origin".into(),
        audio_only: false,
        public_headers: json!({}),
    };
    assert!(matches!(
        receive(&core, &job, &stream).await,
        Err(CoreError::Adapter(
            trace_domain::errors::FailureClass::CapabilityLost
        ))
    ));
    recover_pending(&core, &job).await.unwrap();
    core.db.read(|c|{
        assert_eq!(c.query_row("SELECT count(*) FROM live_fragments WHERE state='sealed'",[],|r|r.get::<_,i64>(0))?,0);
        assert!(c.query_row("SELECT count(*) FROM live_fragments WHERE state='gap' AND reason='strict_quality_unrecorded' AND end_ms>start_ms",[],|r|r.get::<_,i64>(0))?>0);
        assert_eq!(c.query_row("SELECT count(*) FROM media_variants v JOIN media_items m ON m.media_id=v.media_id WHERE m.role='recording'",[],|r|r.get::<_,i64>(0))?,0);
        Ok(())
    }).await.unwrap();
    let response = crate::ipc::dispatch::dispatch(
        &core,
        trace_contracts::control::Request {
            protocol_version: "0.1".into(),
            request_id: "quality-update".into(),
            command: "subscription.update".into(),
            idempotency_key: None,
            if_revision: None,
            args: json!({"subscription_id":"s","quality":{"min_height":1080,"strict":false}}),
        },
    )
    .await;
    assert!(response.ok, "{response:?}");
    assert!(
        job.heartbeat(&core.db).await.is_err(),
        "old strict-policy lease must be revoked"
    );
    let job = jobs::claim_due(&core.db, "test-fallback", 10)
        .await
        .unwrap()
        .into_iter()
        .find(|j| j.job_type == "record")
        .unwrap();
    job.mark_running(&core.db).await.unwrap();
    receive(&core, &job, &stream).await.unwrap();
    core.db.read(|c|{assert!(c.query_row("SELECT count(*) FROM live_fragments f JOIN media_items m ON m.media_id=f.media_id WHERE f.state='sealed' AND json_extract(m.acquisition_evidence,'$.below_target')=1",[],|r|r.get::<_,i64>(0))?>0);Ok(())}).await.unwrap();
    server.abort();
    core.shutdown().await;
}
