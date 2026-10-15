//! A YouTube video through the production adapter, the media tool and the ordinary pipeline:
//! the adapter reads the page, the (stand-in) tool lists the formats, and Core downloads,
//! verifies and commits them. The tool's address is a local server routed from a media host.
use super::*;
use crate::config::Profile;
use crate::core::Core;
use serde_json::{Value, json};
use std::sync::Arc;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use trace_contracts::control::Request;
use trace_contracts::plugin::ProxyFetchResult;
use trace_contracts::source::{SourceConfirmArgs, SourcePreviewArgs};

const WATCH: &str = include_str!("../../../../adapters/youtube/fixtures/watch_video.html");
const VIDEO: &str = "vid_AAAAAA1";
const LINK: &str = "https://www.youtube.com/watch?v=vid_AAAAAA1";

fn debug_dir() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../target/debug")
}

/// A tools directory the way the fetch script leaves one: the two programs and a receipt of
/// their digests under the real lock. The programs are the test stand-in, which reports the
/// locked versions and answers with `info.json`; `runnable: false` leaves placeholders.
fn install_tools(root: &Path, info: Option<&Value>, runnable: bool) -> PathBuf {
    let dir = root.join("tools");
    std::fs::create_dir_all(&dir).unwrap();
    for name in ["yt-dlp.exe", "deno.exe"] {
        if runnable {
            std::fs::copy(
                debug_dir().join("trace-fake-media-tool.exe"),
                dir.join(name),
            )
            .unwrap();
        } else {
            std::fs::write(dir.join(name), b"not an executable").unwrap();
        }
    }
    if let Some(info) = info {
        std::fs::write(dir.join("info.json"), info.to_string()).unwrap();
    }
    let lock: Value = serde_json::from_str(LOCK).unwrap();
    let files = json!({
        "yt-dlp.exe": crate::objects::hash_file(&dir.join("yt-dlp.exe")).unwrap(),
        "deno.exe": crate::objects::hash_file(&dir.join("deno.exe")).unwrap(),
    });
    std::fs::write(
        dir.join("verified.json"),
        json!({"lock": lock, "files": files}).to_string(),
    )
    .unwrap();
    dir
}

async fn open(root: &Path, tools: &Path) -> Arc<Core> {
    Core::open(Profile {
        data_root: root.join("data"),
        archive_root: root.join("archive"),
        vault_dir: Some(root.join("vault")),
        adapter_dir: Some(debug_dir()),
        tools_dir: Some(tools.to_path_buf()),
        test_allowed_hosts: vec!["127.0.0.1".into(), "fixture.googlevideo.com".into()],
        ..Default::default()
    })
    .await
    .unwrap()
}

/// What the site's pages answer: the watch page of the fixture video.
fn serve_pages(core: &Core) {
    core.host.set_proxy_fixture(Arc::new(|params| {
        let url = url::Url::parse(params["url"].as_str().unwrap()).unwrap();
        assert_eq!(url.path(), "/watch", "the adapter asked for {url}");
        Ok(ProxyFetchResult {
            status: 200,
            headers: json!({}),
            body_b64: base64::Engine::encode(
                &base64::engine::general_purpose::STANDARD,
                WATCH.as_bytes(),
            ),
            final_url: params["url"].as_str().unwrap().into(),
        })
    }));
}

fn ffmpeg(core: &Core, args: &[&str], output: &Path) {
    let mut command = std::process::Command::new(core.media_backend.ffmpeg.as_ref().unwrap());
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        command.creation_flags(0x0800_0000);
    }
    assert!(
        command
            .args(["-hide_banner", "-loglevel", "error"])
            .args(args)
            .arg(output)
            .status()
            .unwrap()
            .success()
    );
}

/// A video track and a sound track cut from one small synthetic file.
fn media(core: &Core, dir: &Path) -> (Vec<u8>, Vec<u8>) {
    let whole = dir.join("whole.mp4");
    ffmpeg(
        core,
        &[
            "-f",
            "lavfi",
            "-i",
            "testsrc=size=1280x720:rate=25",
            "-f",
            "lavfi",
            "-i",
            "sine=frequency=440:sample_rate=44100",
            "-t",
            "2",
            "-c:v",
            "libopenh264",
            "-pix_fmt",
            "yuv420p",
            "-c:a",
            "aac",
        ],
        &whole,
    );
    let (video, audio) = (dir.join("video.mp4"), dir.join("audio.m4a"));
    let whole_text = whole.to_str().unwrap();
    ffmpeg(
        core,
        &["-i", whole_text, "-map", "0:v:0", "-c", "copy"],
        &video,
    );
    ffmpeg(
        core,
        &["-i", whole_text, "-map", "0:a:0", "-c", "copy"],
        &audio,
    );
    (std::fs::read(video).unwrap(), std::fs::read(audio).unwrap())
}

fn format(id: &str, video: Option<i64>, url: &str, size: Option<usize>) -> Value {
    let mut f = json!({
        "format_id": id, "url": url, "protocol": "https", "ext": if video.is_some() { "mp4" } else { "m4a" },
        "vcodec": if video.is_some() { "avc1.640028" } else { "none" },
        "acodec": if video.is_some() { "none" } else { "mp4a.40.2" },
        "tbr": 1000.0, "filesize": size,
        "http_headers": {"User-Agent": "Mozilla/5.0 synthetic", "Cookie": "SECRET=1"},
    });
    if let Some(height) = video {
        f["width"] = json!(height * 16 / 9);
        f["height"] = json!(height);
        f["fps"] = json!(25);
    } else {
        f["abr"] = json!(128.0);
    }
    f
}

async fn drain(core: &Core) {
    for _ in 0..20 {
        let batch = crate::jobs::claim_due(&core.db, "tool-test", 1)
            .await
            .unwrap();
        if batch.is_empty() {
            return;
        }
        for job in batch {
            job.mark_running(&core.db).await.unwrap();
            match job.job_type.as_str() {
                "source_acquire" => crate::source_acquisition::run(core, &job).await,
                "media" => crate::pipeline::media::run_media(core, &job).await,
                "quality_maintenance" => crate::quality::maintain(core, &job).await,
                other => panic!("unexpected job {other}"),
            }
            .unwrap();
            job.succeed(&core.db).await.unwrap();
        }
    }
    panic!("the work did not drain");
}

const SELECTED: &str = "SELECT m.role,v.blob_id FROM media_items m JOIN asset_selections s ON s.media_id=m.media_id JOIN media_variants v ON v.variant_id=s.variant_id WHERE m.required=1 ORDER BY m.role DESC";

#[tokio::test]
async fn a_youtube_link_is_archived_from_the_tools_formats_with_its_sound_and_a_playable_copy() {
    let dir = tempfile::tempdir().unwrap();
    let scratch = tempfile::tempdir().unwrap();
    let bootstrap = open(dir.path(), &install_tools(dir.path(), None, true)).await;
    let (video, audio) = media(&bootstrap, scratch.path());
    bootstrap.shutdown().await;
    drop(bootstrap);

    let host = "https://fixture.googlevideo.com";
    let info = json!({"id": VIDEO, "title": "synthetic", "formats": [
        format("137", Some(1080), &format!("{host}/unselected.mp4"), None),
        format("136", Some(720), &format!("{host}/video.mp4"), Some(video.len())),
        format("140", None, &format!("{host}/audio.m4a"), Some(audio.len())),
    ]});
    let tools = install_tools(dir.path(), Some(&info), true);
    let core = open(dir.path(), &tools).await;
    serve_pages(&core);
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    core.http.route_fixture(
        "fixture.googlevideo.com",
        &format!("http://{}", listener.local_addr().unwrap()),
    );
    let (sent_video, sent_audio) = (video.clone(), audio.clone());
    let server = tokio::spawn(async move {
        for _ in 0..2 {
            let (mut socket, _) = listener.accept().await.unwrap();
            let mut request = [0; 8192];
            let count = socket.read(&mut request).await.unwrap();
            let request = String::from_utf8_lossy(&request[..count]).to_ascii_lowercase();
            assert!(
                !request.contains("cookie:"),
                "no credential reaches a media host"
            );
            assert!(request.contains("user-agent: mozilla/5.0 synthetic"));
            let bytes = if request.starts_with("get /video.mp4 ") {
                &sent_video
            } else if request.starts_with("get /audio.m4a ") {
                &sent_audio
            } else {
                panic!("an unselected rendition was requested")
            };
            // The video host is fetched in ranges: the whole small file is the first one.
            assert!(
                request.contains(&format!("range: bytes=0-{}\r\n", bytes.len() - 1)),
                "{request}"
            );
            socket
                .write_all(
                    format!(
                        "HTTP/1.1 206 Partial Content\r\nContent-Length: {len}\r\nContent-Range: bytes 0-{last}/{len}\r\nContent-Type: video/mp4\r\nConnection: close\r\n\r\n",
                        len = bytes.len(),
                        last = bytes.len() - 1
                    )
                    .as_bytes(),
                )
                .await
                .unwrap();
            socket.write_all(bytes).await.unwrap();
        }
    });

    let preview = crate::source_acquisition::preview(
        &core,
        SourcePreviewArgs {
            url: LINK.into(),
            credential_id: None,
        },
    )
    .await
    .unwrap();
    let offers = preview["offers"].as_array().unwrap();
    let required: Vec<_> = offers
        .iter()
        .filter(|o| o["required"] == true)
        .map(|o| o["role"].as_str().unwrap())
        .collect();
    assert_eq!(required.iter().filter(|r| **r == "video").count(), 2);
    assert_eq!(required.iter().filter(|r| **r == "audio").count(), 1);
    assert_eq!(required.len(), 3, "the cover stays optional: {offers:?}");
    let chosen: Vec<String> = offers
        .iter()
        .filter(|o| o["role"] == "audio" || (o["role"] == "video" && o["spec"]["height"] == 720))
        .map(|o| o["id"].as_str().unwrap().to_owned())
        .collect();
    assert_eq!(chosen.len(), 2);
    crate::source_acquisition::confirm(
        &core,
        SourceConfirmArgs {
            preview_id: preview["preview_id"].as_str().unwrap().into(),
            selected: chosen,
            destination: None,
        },
    )
    .await
    .unwrap();
    drain(&core).await;
    server.await.unwrap();

    let selected: Vec<(String, String)> = core
        .db
        .read(|c| {
            Ok(c.prepare(SELECTED)?
                .query_map([], |r| Ok((r.get(0)?, r.get(1)?)))?
                .collect::<Result<Vec<_>, _>>()?)
        })
        .await
        .unwrap();
    assert_eq!(selected.len(), 2);
    let sha = |bytes: &[u8]| {
        use sha2::{Digest, Sha256};
        Sha256::digest(bytes)
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect::<String>()
    };
    assert_eq!(
        selected[0],
        ("video".to_owned(), format!("sha256:{}", sha(&video)))
    );
    assert_eq!(
        selected[1],
        ("audio".to_owned(), format!("sha256:{}", sha(&audio)))
    );
    let playback: String = core
        .db
        .read(|c| {
            Ok(c.query_row(
                "SELECT blob_id FROM media_variants WHERE source_representation='playback-mux-v1'",
                [],
                |r| r.get(0),
            )?)
        })
        .await
        .unwrap();
    let path = core
        .objects
        .object_path(playback.strip_prefix("sha256:").unwrap())
        .unwrap();
    let measured = core.media_backend.probe(&path, &core.cancel).await.unwrap();
    assert_eq!((measured.width, measured.height), (Some(1280), Some(720)));
    assert_eq!(measured.audio_tracks, Some(1));

    // The tool ran once for the preview, the confirmation and the download together.
    assert_eq!(
        std::fs::read_to_string(tools.join("runs.txt"))
            .unwrap()
            .lines()
            .count(),
        1
    );
    let runs = std::fs::read_to_string(tools.join("args.txt")).unwrap();
    for needed in ["--ignore-config", "--no-cookies", "--no-update", "-J", LINK] {
        assert!(runs.contains(needed), "{needed} in {runs}");
    }
    assert!(!runs.contains("SECRET") && !runs.to_lowercase().contains("cookie="));
    core.shutdown().await;
}

#[tokio::test]
async fn a_tool_that_cannot_run_fails_a_link_visibly_and_demotes_the_video_rules() {
    let dir = tempfile::tempdir().unwrap();
    let tools = install_tools(dir.path(), None, false);
    let core = open(dir.path(), &tools).await;
    serve_pages(&core);
    let error = crate::source_acquisition::preview(
        &core,
        SourcePreviewArgs {
            url: LINK.into(),
            credential_id: None,
        },
    )
    .await
    .unwrap_err();
    assert!(
        matches!(&error, crate::error::CoreError::Unsupported(m) if m.contains("media tools unavailable")),
        "{error}"
    );
    crate::catalog::refresh(&core).await.unwrap();
    let rules = core
        .db
        .read(|c| crate::store::rules::list(c, Some("youtube")))
        .await
        .unwrap();
    assert!(!rules.is_empty(), "the youtube adapter declared its rules");
    let video_rules: Vec<_> = rules
        .iter()
        .filter(|r| r.declaration.content_kinds.iter().any(|k| k == "video"))
        .collect();
    assert!(!video_rules.is_empty());
    for rule in video_rules {
        assert_ne!(
            rule.declaration.status,
            trace_contracts::rules::RuleStatus::Implemented
        );
        assert!(
            rule.declaration
                .limits
                .iter()
                .any(|l| l.contains("媒体解析工具")),
            "{} says why",
            rule.declaration.id
        );
    }
    core.shutdown().await;
}

#[tokio::test]
async fn a_challenge_from_the_site_is_a_rate_limit_not_an_empty_or_removed_video() {
    let dir = tempfile::tempdir().unwrap();
    let tools = install_tools(dir.path(), None, true);
    std::fs::write(
        tools.join("fail.txt"),
        "ERROR: Sign in to confirm you're not a bot",
    )
    .unwrap();
    let core = open(dir.path(), &tools).await;
    serve_pages(&core);
    let error = crate::source_acquisition::preview(
        &core,
        SourcePreviewArgs {
            url: LINK.into(),
            credential_id: None,
        },
    )
    .await
    .unwrap_err();
    assert!(
        matches!(error, crate::error::CoreError::RateLimited(_)),
        "{error}"
    );
    core.shutdown().await;
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

/// One discovery observation of a YouTube video by a subscription, as the scan runs it.
async fn discover(core: &Arc<Core>, subscription: &str, payload: Value) -> (i64, i64) {
    use trace_contracts::content::Document;
    use trace_contracts::plugin::{ObservedDetail, ResourceIdentityDto};
    let resource = |role: &str, group: Option<&str>| ResourceIdentityDto {
        role: role.into(),
        source_attachment_id: format!("{VIDEO}:{role}"),
        position: 0,
        title: None,
        group_key: group.map(str::to_owned),
        required: true,
    };
    let detail = ObservedDetail {
        document: Document::default(),
        metadata: json!({}),
        native_entry_key: VIDEO.into(),
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
        access_state: "available".into(),
        source_version_evidence: json!({}),
        resources: vec![
            resource("video", Some("media-1")),
            resource("audio", Some("media-1")),
        ],
        completeness: "full".into(),
        stats: json!({}),
        unified: None,
    };
    let account: String = core
        .db
        .write(|tx| {
            let account: String =
                tx.query_row("SELECT account_id FROM source_accounts LIMIT 1", [], |r| r.get(0))?;
            tx.execute(
                "INSERT OR IGNORE INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES('pe','youtube','video',?1,?2,'t')",
                rusqlite::params![VIDEO, account],
            )?;
            Ok(account)
        })
        .await
        .unwrap();
    let mut payload = payload;
    payload["subscription_id"] = json!(subscription);
    let id = crate::jobs::enqueue(
        &core.db,
        format!("observe-{}", uuid::Uuid::new_v4()),
        "observe",
        "pe",
        payload,
        1,
        1,
        0,
    )
    .await
    .unwrap();
    let job = crate::jobs::claim_due(&core.db, "discover-test", 50)
        .await
        .unwrap()
        .into_iter()
        .find(|j| j.job_id == id)
        .unwrap();
    job.mark_running(&core.db).await.unwrap();
    crate::pipeline::observe::persist_detail(core, &job, "pe", &account, detail)
        .await
        .unwrap();
    core.db
        .read(|c| {
            let count = |sql: &str| c.query_row(sql, [], |r| r.get::<_, i64>(0));
            Ok((
                count("SELECT count(*) FROM media_items WHERE fetch_state='deferred'")?,
                count("SELECT count(*) FROM jobs WHERE job_type='media'")?,
            ))
        })
        .await
        .unwrap()
}

async fn youtube_subscription(core: &Arc<Core>) -> String {
    let creator = call(
        core,
        "creator.create",
        json!({"display_name": "作者", "aliases": [], "tags": [], "notes": ""}),
    )
    .await;
    let account = call(
        core,
        "account.bind",
        json!({"creator_id": creator["creator_id"], "namespace": "youtube", "native_account_key": "UCtest_channel_00000001a"}),
    )
    .await;
    let subscription = call(
        core,
        "subscription.create",
        json!({"account_id": account["account_id"], "content_type": "video",
               "range": {"kind": "recent_days", "days": 30}, "auto_download": true}),
    )
    .await;
    subscription["subscription_id"].as_str().unwrap().into()
}

#[tokio::test]
async fn a_discovered_video_is_left_undownloaded_while_its_tool_is_missing_and_a_request_is_not() {
    // The subscription downloads automatically, but nothing can fetch a YouTube video.
    let broken = tempfile::tempdir().unwrap();
    let core = open(broken.path(), &install_tools(broken.path(), None, false)).await;
    let subscription = youtube_subscription(&core).await;
    let (deferred, media_jobs) = discover(&core, &subscription, json!({})).await;
    assert_eq!(
        (deferred, media_jobs),
        (2, 0),
        "metadata is kept; the video and its sound wait"
    );
    core.shutdown().await;

    // An explicit request is never deferred: it runs and reports the missing tool itself.
    let broken = tempfile::tempdir().unwrap();
    let core = open(broken.path(), &install_tools(broken.path(), None, false)).await;
    let subscription = youtube_subscription(&core).await;
    let (deferred, media_jobs) = discover(&core, &subscription, json!({"delivery_id": "dx"})).await;
    assert_eq!((deferred, media_jobs), (0, 1));
    core.shutdown().await;

    // With the tool in place the same discovery downloads as the subscription says.
    let ready = tempfile::tempdir().unwrap();
    let core = open(ready.path(), &install_tools(ready.path(), None, true)).await;
    let subscription = youtube_subscription(&core).await;
    let (deferred, media_jobs) = discover(&core, &subscription, json!({})).await;
    assert_eq!((deferred, media_jobs), (0, 1));
    core.shutdown().await;
}

/// A recent-days subscription over a list whose lines carry no date (§5.3), through the
/// production YouTube adapter: the list is read for a few pages only, and the details decide
/// what the subscription downloads and what counts as its discovery.
mod undated_range {
    use super::*;
    use std::sync::atomic::{AtomicUsize, Ordering};
    use time::format_description::well_known::Rfc3339;

    const CHANNEL: &str = "UCtest_channel_00000001a";
    const PAGE_SIZE: usize = 10;
    const PAGES: usize = 10;

    fn video_id(n: usize) -> String {
        format!("vid_{n:07}")
    }

    fn items(page: usize) -> Vec<Value> {
        (page * PAGE_SIZE..(page + 1) * PAGE_SIZE)
            .map(|n| {
                json!({"richItemRenderer": {"content": {"videoRenderer": {
                    "videoId": video_id(n),
                    "title": {"runs": [{"text": format!("历史视频 {n}")}]},
                    "lengthText": {"simpleText": "3:05"},
                    "viewCountText": {"simpleText": "100 views"},
                }}}})
            })
            .collect()
    }

    fn continuation(page: usize) -> Option<Value> {
        (page + 1 < PAGES).then(|| {
            json!({"continuationItemRenderer": {"continuationEndpoint": {
                "continuationCommand": {"token": format!("PAGE_{}", page + 1)}
            }}})
        })
    }

    /// The channel's videos tab: the first page of 100 videos, none with an age.
    fn first_page() -> String {
        let mut contents = items(0);
        contents.extend(continuation(0));
        let data = json!({
            "metadata": {"channelMetadataRenderer": {"externalId": CHANNEL}},
            "contents": {"twoColumnBrowseResultsRenderer": {"tabs": [{"tabRenderer": {
                "selected": true,
                "endpoint": {"commandMetadata": {"webCommandMetadata": {"url": format!("/channel/{CHANNEL}/videos")}}},
                "content": {"richGridRenderer": {"contents": contents}},
            }}]}},
        });
        format!(
            "<html><script>ytcfg.set({{\"INNERTUBE_CLIENT_VERSION\":\"2.20261015.01.00\"}});</script><script>var ytInitialData = {data};</script></html>"
        )
    }

    fn later_page(token: &str) -> String {
        let page: usize = token.trim_start_matches("PAGE_").parse().unwrap();
        let mut contents = items(page);
        contents.extend(continuation(page));
        json!({"onResponseReceivedActions": [{"appendContinuationItemsAction": {
            "continuationItems": contents
        }}]})
        .to_string()
    }

    /// The watch page of video `n`, published `days` days ago (or with no date at all).
    fn watch(n: usize, days: Option<i64>) -> String {
        let page = WATCH.replace(VIDEO, &video_id(n));
        match days {
            Some(days) => {
                let at = (time::OffsetDateTime::now_utc() - time::Duration::days(days))
                    .format(&Rfc3339)
                    .unwrap();
                page.replace("2026-09-01T10:00:00-07:00", &at)
            }
            None => page
                .replace("\"publishDate\"", "\"publishedOn\"")
                .replace("\"uploadDate\"", "\"uploadedOn\""),
        }
    }

    /// The pages the site answers; `ages` says how old each watched video is.
    fn serve(core: &Core, ages: fn(usize) -> Option<i64>) -> Arc<AtomicUsize> {
        let list_requests = Arc::new(AtomicUsize::new(0));
        let counted = list_requests.clone();
        core.host.set_proxy_fixture(Arc::new(move |params| {
            let url = url::Url::parse(params["url"].as_str().unwrap()).unwrap();
            let body = if url.path() == "/watch" {
                let id = url
                    .query_pairs()
                    .find(|(k, _)| k == "v")
                    .map(|(_, v)| v.into_owned())
                    .unwrap();
                let n: usize = id.trim_start_matches("vid_").parse().unwrap();
                watch(n, ages(n))
            } else if url.path().starts_with("/channel/") {
                counted.fetch_add(1, Ordering::SeqCst);
                first_page()
            } else {
                assert_eq!(url.path(), "/youtubei/v1/browse", "asked for {url}");
                counted.fetch_add(1, Ordering::SeqCst);
                let sent: Value = serde_json::from_str(params["body"].as_str().unwrap()).unwrap();
                later_page(sent["continuation"].as_str().unwrap())
            };
            Ok(ProxyFetchResult {
                status: 200,
                headers: json!({}),
                body_b64: base64::Engine::encode(
                    &base64::engine::general_purpose::STANDARD,
                    body.as_bytes(),
                ),
                final_url: params["url"].as_str().unwrap().into(),
            })
        }));
        list_requests
    }

    async fn subscription(core: &Arc<Core>, days: i64) -> String {
        let creator = call(
            core,
            "creator.create",
            json!({"display_name": "作者", "aliases": [], "tags": [], "notes": ""}),
        )
        .await;
        let account = call(
            core,
            "account.bind",
            json!({"creator_id": creator["creator_id"], "namespace": "youtube", "native_account_key": CHANNEL}),
        )
        .await;
        let subscription = call(
            core,
            "subscription.create",
            json!({"account_id": account["account_id"], "content_type": "video",
                   "range": {"kind": "recent_days", "days": days}, "auto_download": true}),
        )
        .await;
        subscription["subscription_id"].as_str().unwrap().into()
    }

    /// Runs the subscription's incremental scan until its sweep ends.
    async fn sweep(core: &Arc<Core>, subscription: &str) {
        for _ in 0..8 {
            let id = subscription.to_owned();
            core.db
                .write(move |tx| {
                    tx.execute(
                        "UPDATE jobs SET state='queued', next_run_at=?2, lease_owner=NULL, lease_expires_at=NULL WHERE job_key='scan:'||?1||':incremental'",
                        rusqlite::params![id, crate::jobs::now()],
                    )?;
                    Ok(())
                })
                .await
                .unwrap();
            let due = crate::jobs::claim_due(&core.db, "sweep", 500)
                .await
                .unwrap();
            let Some(job) = due
                .into_iter()
                .find(|j| j.job_type == "scan" && j.payload["mode"] == "incremental")
            else {
                let rows: Vec<String> = core
                    .db
                    .read(|c| {
                        let mut s = c.prepare(
                            "SELECT job_key,state,next_run_at,payload FROM jobs WHERE job_type='scan'",
                        )?;
                        let row = |r: &rusqlite::Row| {
                            let (key, state): (String, String) = (r.get(0)?, r.get(1)?);
                            let (due, payload): (Option<String>, String) = (r.get(2)?, r.get(3)?);
                            Ok(format!("{:?}", (key, state, due, payload)))
                        };
                        Ok(s.query_map([], row)?.collect::<Result<Vec<_>, _>>()?)
                    })
                    .await
                    .unwrap();
                panic!("the incremental scan is due: {rows:#?}");
            };
            job.mark_running(&core.db).await.unwrap();
            crate::pipeline::scan::run_scan(core, &job).await.unwrap();
            let _ = job.succeed(&core.db).await;
            let id = subscription.to_owned();
            let cursor: Option<String> = core
                .db
                .read(move |c| {
                    Ok(c.query_row(
                        "SELECT cursor FROM scan_checkpoints WHERE subscription_id=?1 AND scan_type='incremental'",
                        [id],
                        |r| r.get(0),
                    )?)
                })
                .await
                .unwrap();
            if cursor.is_none() {
                return;
            }
        }
        panic!("the sweep did not end");
    }

    /// Runs the observation of video `n` of the subscription through the adapter.
    async fn observe(core: &Arc<Core>, subscription: &str, n: usize) {
        let key = video_id(n);
        let wanted = key.clone();
        let entry: String = core
            .db
            .read(move |c| {
                Ok(c.query_row(
                    "SELECT entry_id FROM entries WHERE native_entry_key=?1",
                    [wanted],
                    |r| r.get(0),
                )?)
            })
            .await
            .unwrap();
        let id = crate::jobs::enqueue(
            &core.db,
            format!("observe-{key}-{}", uuid::Uuid::new_v4()),
            "observe",
            &entry,
            json!({"entry_id": entry, "subscription_id": subscription}),
            1,
            1,
            0,
        )
        .await
        .unwrap();
        let job = crate::jobs::claim_due(&core.db, "observe", 500)
            .await
            .unwrap()
            .into_iter()
            .find(|j| j.job_id == id)
            .expect("the observation is claimed");
        job.mark_running(&core.db).await.unwrap();
        crate::pipeline::observe::run_observe(core, &job)
            .await
            .unwrap();
    }

    async fn number(core: &Arc<Core>, sql: String) -> i64 {
        core.db
            .read(move |c| Ok(c.query_row(&sql, [], |r| r.get::<_, i64>(0))?))
            .await
            .unwrap()
    }

    fn ages(n: usize) -> Option<i64> {
        match n {
            0..=3 => Some(2),
            4 => None,
            _ => Some(40),
        }
    }

    #[tokio::test]
    async fn a_recent_week_over_100_undated_videos_reads_a_few_pages_and_downloads_only_what_the_details_place_inside()
     {
        let dir = tempfile::tempdir().unwrap();
        let core = open(dir.path(), &install_tools(dir.path(), None, true)).await;
        let list_requests = serve(&core, ages);
        let subscription = subscription(&core, 7).await;

        sweep(&core, &subscription).await;
        // Five pages of a hundred videos, not the whole history; the coverage says why.
        assert_eq!(list_requests.load(Ordering::SeqCst), 5);
        assert_eq!(
            number(&core, "SELECT count(*) FROM entries".into()).await,
            50
        );
        let coverage: String = core
            .db
            .read(|c| {
                Ok(c.query_row(
                    "SELECT coverage FROM scan_checkpoints WHERE scan_type='incremental'",
                    [],
                    |r| r.get(0),
                )?)
            })
            .await
            .unwrap();
        assert!(coverage.contains("undated_page_cap"), "{coverage}");
        // None of the fifty could be placed by the list: nothing is counted as found yet.
        let progress: Vec<Value> = core
            .db
            .read(|c| {
                let mut s = c.prepare("SELECT payload FROM outbox WHERE topic='scan.progress'")?;
                let rows = s
                    .query_map([], |r| r.get::<_, String>(0))?
                    .collect::<Result<Vec<_>, _>>()?;
                Ok(rows
                    .iter()
                    .map(|p| serde_json::from_str(p).unwrap())
                    .collect())
            })
            .await
            .unwrap();
        let total = |field: &str| {
            progress
                .iter()
                .map(|p| p[field].as_i64().unwrap())
                .sum::<i64>()
        };
        assert_eq!((total("new_entries"), total("unplaced_entries")), (0, 50));

        // The details of three of them: dated two days ago, dated 40 days ago, and not dated.
        for n in [0, 5, 4] {
            observe(&core, &subscription, n).await;
        }
        let of = |n: usize, what: &str| {
            format!(
                "SELECT {what} FROM entries WHERE native_entry_key='{}'",
                video_id(n)
            )
        };
        let media_jobs = |n: usize| {
            format!(
                "SELECT count(*) FROM jobs WHERE job_type='media' AND subject_id=(SELECT entry_id FROM entries WHERE native_entry_key='{}')",
                video_id(n)
            )
        };
        let deferred = |n: usize| {
            format!(
                "SELECT count(*) FROM media_items m JOIN entry_revisions r ON r.revision_id=m.revision_id JOIN entries e ON e.entry_id=r.entry_id WHERE e.native_entry_key='{}' AND m.fetch_state='deferred'",
                video_id(n)
            )
        };
        let found = |n: usize| {
            format!(
                "SELECT count(*) FROM entry_subscriptions d JOIN entries e ON e.entry_id=d.entry_id WHERE e.native_entry_key='{}'",
                video_id(n)
            )
        };
        // Inside: the exact time is stored as the source gave it; the cover and the video group
        // (video and soundtrack) are queued; the entry stays one of the subscription's finds.
        assert_eq!(number(&core, of(0, "published_at IS NOT NULL")).await, 1);
        assert_eq!(
            number(&core, of(0, "published_precision='source'")).await,
            1
        );
        assert_eq!(number(&core, media_jobs(0)).await, 2);
        assert_eq!(number(&core, found(0)).await, 1);
        // Outside: dated, nothing fetched (cover, video, soundtrack all wait for a manual sync),
        // no longer one of the subscription's finds.
        assert_eq!(number(&core, of(5, "published_at IS NOT NULL")).await, 1);
        assert_eq!(number(&core, media_jobs(5)).await, 0);
        assert_eq!(number(&core, deferred(5)).await, 3);
        assert_eq!(number(&core, found(5)).await, 0);
        // Undated details: an unknown publish time, no automatic video (the cover still comes),
        // still one of the finds.
        assert_eq!(number(&core, of(4, "published_at IS NOT NULL")).await, 0);
        assert_eq!(number(&core, media_jobs(4)).await, 1);
        assert_eq!(number(&core, deferred(4)).await, 2);
        assert_eq!(number(&core, found(4)).await, 1);
        // The observation keeps what the details said about the range.
        let evidence: Vec<String> = core
            .db
            .read(|c| {
                let mut s = c.prepare(
                    "SELECT json_extract(evidence,'$.range') FROM source_observations WHERE json_extract(evidence,'$.range') IS NOT NULL ORDER BY observed_at, observation_id",
                )?;
                Ok(s.query_map([], |r| r.get::<_, String>(0))?
                    .collect::<Result<Vec<_>, _>>()?)
            })
            .await
            .unwrap();
        assert_eq!(evidence, ["inside", "outside", "unplaced"]);
        core.shutdown().await;
    }
}

/// A TikTok video whose own address the site refuses even to the page's session: the tool
/// downloads the file in its place, and only then, through the production adapter and the
/// ordinary pipeline.
mod tiktok_fallback {
    use super::*;
    use std::sync::atomic::{AtomicUsize, Ordering};

    const PAGE: &str = include_str!("../../../../adapters/tiktok/fixtures/video.html");
    const KEY: &str = "7000000000000000001";
    const SITE_LINK: &str = "https://www.tiktok.com/@synthetic.creator/video/7000000000000000001";
    const SITE_VIDEO: &str = "https://v16-webapp-prime.tiktok.com/video/tos/normal_720_0.mp4";

    /// The fixture page with its 720p rendition on the site's own domain, the way the web
    /// client hands out a playable address.
    fn page(size: usize) -> String {
        PAGE.replace("\"DataSize\": 3500000", &format!("\"DataSize\": {size}"))
            .replace(
                "https://v16a.tiktokcdn.com/video/normal_720_0.mp4?sig=x",
                SITE_VIDEO,
            )
            .replace(
                "https://v16a-b.tiktokcdn.com/video/normal_720_0.mp4?sig=x",
                "https://v16-webapp-prime.tiktok.com/video/tos/backup_720_0.mp4",
            )
    }

    fn serve_pages(core: &Core, size: usize) {
        core.host.set_proxy_fixture(Arc::new(move |params| {
            let url = url::Url::parse(params["url"].as_str().unwrap()).unwrap();
            assert_eq!(url.host_str(), Some("www.tiktok.com"), "{url}");
            assert_eq!(url.path(), format!("/@_/video/{KEY}"), "{url}");
            Ok(ProxyFetchResult {
                status: 200,
                headers: json!({}),
                body_b64: base64::Engine::encode(
                    &base64::engine::general_purpose::STANDARD,
                    page(size).as_bytes(),
                ),
                final_url: params["url"].as_str().unwrap().into(),
            })
        }));
    }

    /// The site's video host: answers every request with `status` and counts them.
    async fn site_video_host(core: &Core, status: u16, bytes: Vec<u8>) -> Arc<AtomicUsize> {
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        core.http.route_fixture(
            "v16-webapp-prime.tiktok.com",
            &format!("http://{}", listener.local_addr().unwrap()),
        );
        let asked = Arc::new(AtomicUsize::new(0));
        let counted = asked.clone();
        tokio::spawn(async move {
            while let Ok((mut socket, _)) = listener.accept().await {
                let mut request = [0; 8192];
                let _ = socket.read(&mut request).await;
                counted.fetch_add(1, Ordering::SeqCst);
                let head = if status == 200 {
                    format!(
                        "HTTP/1.1 200 OK\r\nContent-Length: {}\r\nContent-Type: video/mp4\r\nConnection: close\r\n\r\n",
                        bytes.len()
                    )
                } else {
                    format!(
                        "HTTP/1.1 {status} Refused\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"
                    )
                };
                let _ = socket.write_all(head.as_bytes()).await;
                if status == 200 {
                    let _ = socket.write_all(&bytes).await;
                }
            }
        });
        asked
    }

    /// A muxed 720x1280 clip, the way the site's own renditions are.
    fn clip(core: &Core, dir: &Path) -> Vec<u8> {
        let out = dir.join("clip.mp4");
        ffmpeg(
            core,
            &[
                "-f",
                "lavfi",
                "-i",
                "testsrc=size=720x1280:rate=25",
                "-f",
                "lavfi",
                "-i",
                "sine=frequency=440:sample_rate=44100",
                "-t",
                "1",
                "-c:v",
                "libopenh264",
                "-pix_fmt",
                "yuv420p",
                "-c:a",
                "aac",
            ],
            &out,
        );
        std::fs::read(out).unwrap()
    }

    async fn confirm_the_720_rendition(core: &Core) {
        let preview = crate::source_acquisition::preview(
            core,
            SourcePreviewArgs {
                url: SITE_LINK.into(),
                credential_id: None,
            },
        )
        .await
        .unwrap();
        let chosen: Vec<String> = preview["offers"]
            .as_array()
            .unwrap()
            .iter()
            .filter(|o| o["role"] == "video" && o["spec"]["height"] == 1280)
            .map(|o| o["id"].as_str().unwrap().to_owned())
            .collect();
        assert_eq!(chosen.len(), 1, "{preview}");
        crate::source_acquisition::confirm(
            core,
            SourceConfirmArgs {
                preview_id: preview["preview_id"].as_str().unwrap().into(),
                selected: chosen,
                destination: None,
            },
        )
        .await
        .unwrap();
    }

    /// Runs the queued work and returns the first failure of a media job, if any.
    async fn run_all(core: &Core) -> Option<crate::error::CoreError> {
        for _ in 0..20 {
            let batch = crate::jobs::claim_due(&core.db, "tool-test", 1)
                .await
                .unwrap();
            if batch.is_empty() {
                return None;
            }
            for job in batch {
                job.mark_running(&core.db).await.unwrap();
                let result = match job.job_type.as_str() {
                    "source_acquire" => crate::source_acquisition::run(core, &job).await,
                    "media" => crate::pipeline::media::run_media(core, &job).await,
                    "quality_maintenance" => crate::quality::maintain(core, &job).await,
                    other => panic!("unexpected job {other}"),
                };
                if let Err(error) = result {
                    return Some(error);
                }
                job.succeed(&core.db).await.unwrap();
            }
        }
        panic!("the work did not drain");
    }

    async fn open_tiktok(root: &Path, tools: &Path) -> Arc<Core> {
        Core::open(Profile {
            data_root: root.join("data"),
            archive_root: root.join("archive"),
            vault_dir: Some(root.join("vault")),
            adapter_dir: Some(debug_dir()),
            tools_dir: Some(tools.to_path_buf()),
            test_allowed_hosts: vec!["127.0.0.1".into(), "v16-webapp-prime.tiktok.com".into()],
            ..Default::default()
        })
        .await
        .unwrap()
    }

    const VARIANTS: &str = "SELECT source_representation, blob_id FROM media_variants WHERE source_representation NOT LIKE 'playback%'";

    async fn variants(core: &Core) -> Vec<(String, String)> {
        core.db
            .read(|c| {
                Ok(c.prepare(VARIANTS)?
                    .query_map([], |r| Ok((r.get(0)?, r.get(1)?)))?
                    .collect::<Result<Vec<_>, _>>()?)
            })
            .await
            .unwrap()
    }

    fn sha(bytes: &[u8]) -> String {
        use sha2::{Digest, Sha256};
        format!(
            "sha256:{}",
            Sha256::digest(bytes)
                .iter()
                .map(|b| format!("{b:02x}"))
                .collect::<String>()
        )
    }

    #[tokio::test]
    async fn the_tool_downloads_the_video_only_when_the_site_refuses_its_own_address() {
        let dir = tempfile::tempdir().unwrap();
        let scratch = tempfile::tempdir().unwrap();
        let bootstrap = open_tiktok(dir.path(), &install_tools(dir.path(), None, true)).await;
        let clip = clip(&bootstrap, scratch.path());
        bootstrap.shutdown().await;
        drop(bootstrap);

        // Refused: the tool runs once, with the fixed arguments and no site credential, and the
        // file it leaves is what is archived, marked as the tool's.
        let tools = install_tools(dir.path(), None, true);
        std::fs::write(tools.join("download.bin"), &clip).unwrap();
        let core = open_tiktok(dir.path(), &tools).await;
        serve_pages(&core, clip.len());
        let asked = site_video_host(&core, 403, Vec::new()).await;
        confirm_the_720_rendition(&core).await;
        assert!(run_all(&core).await.is_none());
        assert_eq!(
            asked.load(Ordering::SeqCst),
            2,
            "the address and its backup were tried"
        );
        let stored = variants(&core).await;
        assert_eq!(stored.len(), 1, "{stored:?}");
        assert!(stored[0].0.ends_with("+media-tool"), "{stored:?}");
        assert_eq!(stored[0].1, sha(&clip));
        let runs = std::fs::read_to_string(tools.join("args.txt")).unwrap();
        for needed in [
            "--ignore-config",
            "--no-cookies",
            "--no-update",
            "--max-filesize",
            "https://www.tiktok.com/@_/video/7000000000000000001",
        ] {
            assert!(runs.contains(needed), "{needed} in {runs}");
        }
        assert!(
            !runs.contains("-J"),
            "a download, not a format query: {runs}"
        );
        assert_eq!(
            std::fs::read_to_string(tools.join("runs.txt"))
                .unwrap()
                .lines()
                .count(),
            1
        );
        core.shutdown().await;
    }

    #[tokio::test]
    async fn an_address_the_site_serves_is_downloaded_by_core_and_the_tool_never_runs() {
        let dir = tempfile::tempdir().unwrap();
        let scratch = tempfile::tempdir().unwrap();
        let bootstrap = open_tiktok(dir.path(), &install_tools(dir.path(), None, true)).await;
        let clip = clip(&bootstrap, scratch.path());
        bootstrap.shutdown().await;
        drop(bootstrap);
        let tools = install_tools(dir.path(), None, true);
        std::fs::write(tools.join("download.bin"), b"never used").unwrap();
        let core = open_tiktok(dir.path(), &tools).await;
        serve_pages(&core, clip.len());
        site_video_host(&core, 200, clip.clone()).await;
        confirm_the_720_rendition(&core).await;
        assert!(run_all(&core).await.is_none());
        let stored = variants(&core).await;
        assert_eq!(stored.len(), 1, "{stored:?}");
        assert!(!stored[0].0.contains("media-tool"), "{stored:?}");
        assert_eq!(stored[0].1, sha(&clip));
        assert!(
            !tools.join("runs.txt").exists(),
            "the tool is not consulted for an address that works"
        );
        core.shutdown().await;
    }

    #[tokio::test]
    async fn without_the_tool_a_refused_video_fails_as_the_sites_refusal_and_stores_nothing() {
        let dir = tempfile::tempdir().unwrap();
        let tools = install_tools(dir.path(), None, false);
        let core = open_tiktok(dir.path(), &tools).await;
        serve_pages(&core, 1_000);
        site_video_host(&core, 403, Vec::new()).await;
        confirm_the_720_rendition(&core).await;
        let error = run_all(&core).await.expect("the refusal is reported");
        assert!(
            matches!(&error, crate::error::CoreError::SourceUnavailable(m) if m.contains("HTTP 403")),
            "{error}"
        );
        assert!(variants(&core).await.is_empty());
        core.shutdown().await;
    }

    #[tokio::test]
    async fn a_tool_that_fails_reports_its_own_failure_not_a_stored_file() {
        let dir = tempfile::tempdir().unwrap();
        let tools = install_tools(dir.path(), None, true);
        std::fs::write(
            tools.join("fail.txt"),
            "ERROR: Sign in to confirm you're not a bot",
        )
        .unwrap();
        let core = open_tiktok(dir.path(), &tools).await;
        serve_pages(&core, 1_000);
        site_video_host(&core, 403, Vec::new()).await;
        confirm_the_720_rendition(&core).await;
        let error = run_all(&core).await.expect("the failure is reported");
        assert!(
            matches!(error, crate::error::CoreError::RateLimited(_)),
            "{error}"
        );
        assert!(variants(&core).await.is_empty());
        core.shutdown().await;
    }
}
