use super::formats::{offers, ranged_chunk, video_id_ok};
use super::plan::{apply, needed, waive_audio};
use super::run::Sandbox;
use super::*;
use crate::error::CoreError;
use serde_json::{Value, json};
use tokio_util::sync::CancellationToken;
use trace_contracts::plugin::{ResolveMediaResult, ResourceIdentityDto};
use trace_contracts::rules::{RuleDeclaration, RuleStatus};
use trace_domain::errors::FailureClass;

const ID: &str = "abcdefghijk";

fn resource(role: &str, required: bool) -> ResourceIdentityDto {
    ResourceIdentityDto {
        role: role.into(),
        source_attachment_id: format!("{ID}:{role}:0:{role}-{ID}"),
        position: 0,
        title: None,
        group_key: Some(format!("media-{ID}")),
        required,
    }
}

fn resources() -> Vec<ResourceIdentityDto> {
    vec![resource("video", true), resource("audio", true)]
}

/// A format as the tool lists it; `video` and `audio` are codec names or `none`.
fn format(id: &str, video: &str, audio: &str, height: i64, tbr: f64) -> Value {
    let mut f = json!({
        "format_id": id,
        "url": format!("https://rr1---sn-synthetic.googlevideo.com/videoplayback?expire=1&id={id}&sig=SECRET{id}"),
        "protocol": "https",
        "ext": match (video, audio) {
            ("none", "opus") => "webm",
            ("none", _) => "m4a",
            _ => "mp4",
        },
        "vcodec": video,
        "acodec": audio,
        "tbr": tbr,
        "filesize": 1_000_000 + height,
        "format_note": format!("{height}p"),
        "http_headers": {
            "User-Agent": "Mozilla/5.0 synthetic",
            "Accept": "*/*",
            "Sec-Fetch-Mode": "navigate",
            "Cookie": "SECRET=1"
        },
    });
    if video != "none" {
        f["width"] = json!(height * 16 / 9);
        f["height"] = json!(height);
        f["fps"] = json!(30);
    } else {
        f["abr"] = json!(tbr);
        f["asr"] = json!(44100);
        f["audio_channels"] = json!(2);
    }
    f
}

fn info(formats: Vec<Value>) -> Value {
    json!({"id": ID, "title": "synthetic", "formats": formats})
}

fn dash() -> Value {
    info(vec![
        format("sb0", "none", "none", 0, 0.0),
        format("140", "none", "mp4a.40.2", 0, 130.0),
        format("251", "none", "opus", 0, 160.0),
        format("136", "avc1.4d401f", "none", 720, 1500.0),
        format("137", "avc1.640028", "none", 1080, 4000.0),
        format("248", "vp09.00.40.08", "none", 1080, 2000.0),
        format("18", "avc1.42001E", "mp4a.40.2", 360, 500.0),
    ])
}

fn ids(offers: &[trace_contracts::plugin::MediaCandidate]) -> Vec<String> {
    offers.iter().map(|c| c.representation.clone()).collect()
}

#[test]
fn only_the_video_host_is_fetched_in_ranges() {
    for url in [
        "https://rr1---sn-abc.googlevideo.com/videoplayback?id=1",
        "https://googlevideo.com/videoplayback",
    ] {
        assert_eq!(ranged_chunk(url), Some(10 << 20), "{url}");
    }
    for url in [
        "https://notgooglevideo.com/videoplayback",
        "https://googlevideo.com.example.org/videoplayback",
        "https://upos-sz.bilivideo.com/x.m4s",
        "not a url",
    ] {
        assert_eq!(ranged_chunk(url), None, "{url}");
    }
}

#[test]
fn a_video_id_is_eleven_url_safe_characters() {
    assert!(video_id_ok(ID));
    assert!(video_id_ok("a-b_c-d_e-f"));
    for bad in [
        "",
        "short",
        "abcdefghijkl",
        "abcdefghi k",
        "abcdefghij/",
        "abcdefghij\u{e9}",
    ] {
        assert!(!video_id_ok(bad), "{bad:?}");
    }
}

#[test]
fn separate_video_and_sound_are_offered_as_a_pair_and_the_best_pair_is_ranked_out() {
    let all = offers(&dash(), ID, &resources(), true, false).unwrap();
    assert!(all.waives_audio.is_none());
    let mut shown = ids(&all.candidates);
    shown.sort();
    assert_eq!(
        shown,
        ["yt-136", "yt-137", "yt-140", "yt-248"],
        "muxed 18 and the storyboard are not offered"
    );
    let audio = all
        .candidates
        .iter()
        .find(|c| c.kind == "dash_audio")
        .unwrap();
    assert_eq!(
        audio.representation, "yt-140",
        "AAC is preferred as the soundtrack"
    );
    assert_eq!(audio.resource.role, "audio");
    assert_eq!(audio.claimed_spec["bitrate"], 130000);
    let best = offers(&dash(), ID, &resources(), false, false).unwrap();
    let mut kept = ids(&best.candidates);
    kept.sort();
    assert_eq!(
        kept,
        ["yt-137", "yt-140"],
        "highest resolution, the better bitrate on a tie"
    );
    let video = best
        .candidates
        .iter()
        .find(|c| c.resource.role == "video")
        .unwrap();
    assert_eq!(video.kind, "dash_video");
    assert_eq!(video.claimed_spec["height"], 1080);
    assert_eq!(video.claimed_spec["codec"], "avc1.640028");
    assert_eq!(video.expected_size, Some(1_001_080));
    assert_eq!(video.segments[0].expected_size, Some(1_001_080));
}

#[test]
fn frame_rate_can_outrank_resolution_when_asked() {
    let mut fast = format("298", "avc1.4d4020", "none", 720, 3000.0);
    fast["fps"] = json!(60);
    let formats = vec![
        format("140", "none", "mp4a.40.2", 0, 130.0),
        format("137", "avc1.640028", "none", 1080, 4000.0),
        fast,
    ];
    let by_size = offers(&info(formats.clone()), ID, &resources(), false, false).unwrap();
    assert!(ids(&by_size.candidates).contains(&"yt-137".to_owned()));
    let by_rate = offers(&info(formats), ID, &resources(), false, true).unwrap();
    assert!(ids(&by_rate.candidates).contains(&"yt-298".to_owned()));
}

#[test]
fn a_video_with_its_own_sound_waives_the_soundtrack_resource() {
    let only_muxed = info(vec![format("18", "avc1.42001E", "mp4a.40.2", 360, 500.0)]);
    let got = offers(&only_muxed, ID, &resources(), false, false).unwrap();
    assert_eq!(ids(&got.candidates), ["yt-18"]);
    assert_eq!(got.candidates[0].kind, "muxed");
    assert_eq!(got.waives_audio.as_deref(), Some("media-abcdefghijk"));
    let silent = info(vec![format("137", "avc1.640028", "none", 1080, 4000.0)]);
    let got = offers(&silent, ID, &resources(), false, false).unwrap();
    assert_eq!(got.candidates[0].claimed_spec["silent"], true);
    assert_eq!(got.waives_audio.as_deref(), Some("media-abcdefghijk"));
    let no_sound_resource = [resource("video", true)];
    let got = offers(&dash(), ID, &no_sound_resource, false, false).unwrap();
    assert_eq!(
        got.candidates[0].kind, "muxed",
        "a video without a soundtrack resource keeps its sound"
    );
}

#[test]
fn the_original_language_is_the_soundtrack() {
    let mut dubbed = format("140-1", "none", "mp4a.40.2", 0, 190.0);
    dubbed["language_preference"] = json!(-1);
    let mut original = format("140-0", "none", "mp4a.40.2", 0, 130.0);
    original["language_preference"] = json!(10);
    let formats = vec![
        dubbed,
        original,
        format("137", "avc1.640028", "none", 1080, 4000.0),
    ];
    let got = offers(&info(formats), ID, &resources(), false, false).unwrap();
    assert!(ids(&got.candidates).contains(&"yt-140-0".to_owned()));
}

#[test]
fn only_plain_https_addresses_on_the_media_host_are_offered() {
    let mut foreign = format("1", "avc1.640028", "none", 1080, 1.0);
    foreign["url"] = json!("https://evil.example/videoplayback");
    let mut near = format("2", "avc1.640028", "none", 1080, 1.0);
    near["url"] = json!("https://googlevideo.com.evil.example/videoplayback");
    let mut plain = format("3", "avc1.640028", "none", 1080, 1.0);
    plain["url"] = json!("http://rr1.googlevideo.com/videoplayback");
    let mut login = format("4", "avc1.640028", "none", 1080, 1.0);
    login["url"] = json!("https://user:pw@rr1.googlevideo.com/videoplayback");
    let mut playlist = format("5", "avc1.640028", "none", 1080, 1.0);
    playlist["protocol"] = json!("m3u8_native");
    let mut drm = format("6", "avc1.640028", "none", 1080, 1.0);
    drm["has_drm"] = json!(true);
    let mut story = format("7", "avc1.640028", "none", 1080, 1.0);
    story["format_note"] = json!("storyboard");
    let mut odd = format("a b", "avc1.640028", "none", 1080, 1.0);
    odd["url"] = json!("https://rr1.googlevideo.com/videoplayback");
    let mut sizeless = format("8", "avc1.640028", "none", 1080, 1.0);
    sizeless["width"] = Value::Null;
    let formats = vec![
        foreign,
        near,
        plain,
        login,
        playlist,
        drm,
        story,
        odd,
        sizeless,
        format("140", "none", "mp4a.40.2", 0, 130.0),
    ];
    let error = offers(&info(formats), ID, &resources(), true, false)
        .err()
        .unwrap();
    assert!(matches!(error, CoreError::SourceUnavailable(_)), "{error}");
}

#[test]
fn download_headers_are_the_public_allowlist_and_the_rest_is_dropped() {
    let got = offers(&dash(), ID, &resources(), false, false).unwrap();
    for c in &got.candidates {
        assert_eq!(c.public_headers["user-agent"], "Mozilla/5.0 synthetic");
        assert_eq!(c.public_headers["accept"], "*/*");
        assert!(c.public_headers.get("sec-fetch-mode").is_none());
        assert!(c.public_headers.get("cookie").is_none());
        assert!(!c.public_headers.to_string().contains("SECRET"));
    }
}

#[test]
fn an_offer_does_not_change_when_only_the_address_does() {
    let again = || {
        let mut changed = dash();
        for f in changed["formats"].as_array_mut().unwrap() {
            let url = f["url"].as_str().unwrap().replace("expire=1", "expire=999");
            f["url"] = json!(url);
        }
        changed
    };
    let (a, b) = (
        offers(&dash(), ID, &resources(), true, false).unwrap(),
        offers(&again(), ID, &resources(), true, false).unwrap(),
    );
    let key = |o: &super::formats::Offers| {
        let mut ids: Vec<String> = o
            .candidates
            .iter()
            .map(|c| {
                crate::source_offers::offer(c)["id"]
                    .as_str()
                    .unwrap()
                    .to_owned()
            })
            .collect();
        ids.sort();
        ids
    };
    assert_eq!(
        key(&a),
        key(&b),
        "preview and download must see the same offers"
    );
}

#[test]
fn another_video_a_live_stream_or_a_format_flood_is_refused() {
    let mut other = dash();
    other["id"] = json!("zzzzzzzzzzz");
    assert!(matches!(
        offers(&other, ID, &resources(), true, false),
        Err(CoreError::InvalidInput(_))
    ));
    let mut live = dash();
    live["is_live"] = json!(true);
    assert!(matches!(
        offers(&live, ID, &resources(), true, false),
        Err(CoreError::Unsupported(_))
    ));
    let mut upcoming = dash();
    upcoming["live_status"] = json!("is_upcoming");
    assert!(offers(&upcoming, ID, &resources(), true, false).is_err());
    let flood = info(
        (0..401)
            .map(|i| format(&format!("f{i}"), "avc1", "none", 360, 1.0))
            .collect(),
    );
    assert!(matches!(
        offers(&flood, ID, &resources(), true, false),
        Err(CoreError::InvalidInput(_))
    ));
    assert!(matches!(
        offers(&info(vec![]), ID, &resources(), true, false),
        Err(CoreError::SourceUnavailable(_))
    ));
}

#[test]
fn a_plan_without_a_video_resource_gets_nothing() {
    let covers = [resource("cover", false)];
    let got = offers(&dash(), ID, &covers, true, false).unwrap();
    assert!(got.candidates.is_empty());
}

#[test]
fn the_adapter_asks_and_the_site_is_one_the_tool_serves() {
    let mut plan = ResolveMediaResult {
        candidates: Vec::new(),
        notes: json!({"external_media": "yt_dlp"}),
    };
    assert!(needed("youtube", &resources(), &plan));
    assert!(
        !needed("bilibili", &resources(), &plan),
        "a site without the tool"
    );
    plan.notes = json!({});
    assert!(
        !needed("youtube", &resources(), &plan),
        "the adapter did not ask"
    );
}

#[test]
fn a_waived_soundtrack_stops_being_required_in_a_single_link_source() {
    let mut source: trace_contracts::source::ResolvedSource = serde_json::from_value(json!({
        "kind": "video", "canonical_url": "https://youtu.be/abcdefghijk", "native_account_key": "UC1",
        "display_name": null, "source_duration_seconds": 1.0,
        "detail": {
            "document": {"version": 1, "blocks": [], "omissions": []}, "metadata": {},
            "native_entry_key": ID, "native_kind": "video", "title": "t", "normalized_body": "",
            "published_at": null, "published_precision": "unknown", "access_state": "available",
            "source_version_evidence": {}, "resources": [], "completeness": "full", "stats": {}
        },
        "media": {"candidates": [], "notes": {"silent_or_muxed": ["media-abcdefghijk"]}},
        "live": null
    }))
    .unwrap();
    source.detail.as_mut().unwrap().resources = resources();
    waive_audio(&mut source);
    let required: Vec<_> = source
        .detail
        .unwrap()
        .resources
        .iter()
        .map(|r| (r.role.clone(), r.required))
        .collect();
    assert_eq!(
        required,
        [("video".to_owned(), true), ("audio".to_owned(), false)]
    );
}

fn rule(status: RuleStatus, kinds: &[&str]) -> RuleDeclaration {
    serde_json::from_value(json!({
        "id": "youtube.x.y", "site": "youtube", "title": "t", "description": "d",
        "entry": "link", "content_kinds": kinds, "url_patterns": [], "login": "none",
        "network": "proxy_recommended", "limits": [], "status": status_text(status)
    }))
    .unwrap()
}

fn status_text(status: RuleStatus) -> &'static str {
    match status {
        RuleStatus::Implemented => "implemented",
        RuleStatus::Partial => "partial",
        RuleStatus::Unimplemented => "unimplemented",
        RuleStatus::Retired => "retired",
    }
}

#[tokio::test]
async fn without_the_tool_the_video_rules_are_partial_and_say_why() {
    let tools = MediaTools::missing();
    let mut rules = [
        rule(RuleStatus::Implemented, &["video"]),
        rule(RuleStatus::Implemented, &["text"]),
        rule(RuleStatus::Unimplemented, &["video"]),
    ];
    tools.annotate("youtube", &mut rules).await;
    assert_eq!(rules[0].status, RuleStatus::Partial);
    assert_eq!(rules[0].limits.len(), 1);
    assert_eq!(
        rules[1].status,
        RuleStatus::Implemented,
        "rules that need no tool stay"
    );
    assert!(rules[1].limits.is_empty());
    assert_eq!(rules[2].status, RuleStatus::Unimplemented, "never raised");
    tools.annotate("youtube", &mut rules).await;
    assert_eq!(rules[0].limits.len(), 1, "the note is added once");
    let mut elsewhere = [rule(RuleStatus::Implemented, &["video"])];
    tools.annotate("bilibili", &mut elsewhere).await;
    assert_eq!(
        elsewhere[0].status,
        RuleStatus::Implemented,
        "another site is not affected"
    );
    let ready = MediaTools::trusted("yt-dlp.exe".into(), "deno.exe".into());
    let mut served = [rule(RuleStatus::Implemented, &["video"])];
    ready.annotate("youtube", &mut served).await;
    assert_eq!(served[0].status, RuleStatus::Implemented);
}

#[tokio::test]
async fn tools_that_do_not_match_the_lock_are_never_executed() {
    let dir = tempfile::tempdir().unwrap();
    let profile = |d: &Path| crate::config::Profile {
        tools_dir: Some(d.to_path_buf()),
        ..crate::config::Profile::default()
    };
    for name in ["yt-dlp.exe", "deno.exe"] {
        std::fs::write(dir.path().join(name), b"not an executable").unwrap();
    }
    let lock: Value = serde_json::from_str(LOCK).unwrap();
    let files = json!({
        "yt-dlp.exe": crate::objects::hash_file(&dir.path().join("yt-dlp.exe")).unwrap(),
        "deno.exe": crate::objects::hash_file(&dir.path().join("deno.exe")).unwrap(),
    });
    for (receipt, reason) in [
        (
            json!({"lock": {}, "files": {}}),
            "media tools failed checksum validation",
        ),
        (
            json!({"lock": lock, "files": {"yt-dlp.exe": "0".repeat(64), "deno.exe": "0".repeat(64)}}),
            "media tools failed checksum validation",
        ),
        (
            json!({"lock": lock, "files": files}),
            "media tool version mismatch",
        ),
    ] {
        std::fs::write(dir.path().join("verified.json"), receipt.to_string()).unwrap();
        let tools = MediaTools::detect(&profile(dir.path())).await;
        assert!(!tools.ready().await);
        assert_eq!(tools.verified().await.err(), Some(reason));
        assert!(matches!(
            tools.unavailable().await,
            CoreError::Unsupported(_)
        ));
    }
}

#[tokio::test]
async fn the_locked_tools_are_found_and_report_the_locked_versions() {
    let tools = MediaTools::detect(&crate::config::Profile::default()).await;
    let verified = tools.verified().await;
    assert!(
        verified.is_ok(),
        "run scripts/fetch_media_tools.py first: {verified:?}"
    );
    let lock: Value = serde_json::from_str(LOCK).unwrap();
    assert_eq!(verified.ok().as_deref(), lock["yt_dlp"]["version"].as_str());
}

#[cfg(windows)]
mod process {
    use super::*;
    use std::path::Path;

    const SUCCESS: &str = "@echo off\r\necho %*> \"%~dp0args.txt\"\r\nset> \"%~dp0env.txt\"\r\ntype \"%~dp0info.json\"\r\n";

    fn fake(dir: &Path, script: &str) -> MediaTools {
        std::fs::write(dir.join("yt-dlp.cmd"), script).unwrap();
        std::fs::write(dir.join("deno.cmd"), "@echo off\r\n").unwrap();
        MediaTools::trusted(dir.join("yt-dlp.cmd"), dir.join("deno.cmd"))
    }

    #[tokio::test]
    async fn waiting_parser_uses_the_program_selected_after_the_switch() {
        let old = tempfile::tempdir().unwrap();
        let new = tempfile::tempdir().unwrap();
        std::fs::write(
            old.path().join("info.json"),
            json!({"id": ID, "title": "old"}).to_string(),
        )
        .unwrap();
        std::fs::write(
            new.path().join("info.json"),
            json!({"id": ID, "title": "new"}).to_string(),
        )
        .unwrap();
        let tools = fake(old.path(), SUCCESS);
        let replacement = fake(new.path(), SUCCESS);
        let permit = tools.exclusive().await.unwrap();
        let work = old.path().join("work");
        let sandbox = Sandbox {
            work: &work,
            proxy: None,
        };
        let cancel = CancellationToken::new();
        let mut waiting = Box::pin(tools.video_info(&sandbox, ID, &cancel));
        assert!(
            tokio::time::timeout(Duration::from_millis(50), &mut waiting)
                .await
                .is_err()
        );
        // The fake command fixture models the same located-path switch as activation.
        *tools.located.write().unwrap() = replacement.located.read().unwrap().clone();
        drop(permit);
        assert_eq!(
            waiting.await.unwrap()["title"],
            "new",
            "a parser queued behind a switch must not run the old executable"
        );
        assert!(!old.path().join("args.txt").exists());
    }

    #[tokio::test]
    async fn waiting_download_uses_the_program_selected_after_the_switch() {
        let old = tempfile::tempdir().unwrap();
        let new = tempfile::tempdir().unwrap();
        let script = "@echo off\r\ncopy /b \"%~dp0bytes.bin\" video.mp4 >nul\r\n";
        std::fs::write(old.path().join("bytes.bin"), b"old").unwrap();
        std::fs::write(new.path().join("bytes.bin"), b"new").unwrap();
        let tools = fake(old.path(), script);
        let replacement = fake(new.path(), script);
        let permit = tools.exclusive().await.unwrap();
        let work = old.path().join("work");
        let sandbox = Sandbox {
            work: &work,
            proxy: None,
        };
        let cancel = CancellationToken::new();
        let mut waiting =
            Box::pin(tools.fetch_video(&sandbox, "https://example.invalid/video", 1024, &cancel));
        assert!(
            tokio::time::timeout(Duration::from_millis(50), &mut waiting)
                .await
                .is_err()
        );
        *tools.located.write().unwrap() = replacement.located.read().unwrap().clone();
        drop(permit);
        let fetched = waiting.await.unwrap();
        assert_eq!(
            std::fs::read(fetched.path).unwrap(),
            b"new",
            "a queued download must use the current executable"
        );
    }

    #[tokio::test]
    async fn a_run_gets_fixed_arguments_an_empty_environment_and_its_own_directories() {
        let dir = tempfile::tempdir().unwrap();
        std::fs::write(dir.path().join("info.json"), dash().to_string()).unwrap();
        let tools = fake(dir.path(), SUCCESS);
        let work = dir.path().join("work");
        let sandbox = Sandbox {
            work: &work,
            proxy: None,
        };
        let cancel = CancellationToken::new();
        let got = tools.video_info(&sandbox, ID, &cancel).await.unwrap();
        assert_eq!(got["id"], ID);
        let args = std::fs::read_to_string(dir.path().join("args.txt")).unwrap();
        for needed in [
            "--ignore-config",
            "--no-update",
            "--no-cookies",
            "--no-playlist",
            "-J",
            "--proxy \"\"",
            &format!("https://www.youtube.com/watch?v={ID}"),
        ] {
            assert!(args.contains(needed), "{needed} in {args}");
        }
        assert!(args.contains("deno:") && args.contains("deno.cmd"));
        let env = std::fs::read_to_string(dir.path().join("env.txt")).unwrap();
        let line = |name: &str| {
            env.lines()
                .find_map(|l| l.strip_prefix(&format!("{name}=")).map(str::to_owned))
        };
        let temp = line("TEMP").expect("TEMP");
        assert!(temp.to_ascii_lowercase().contains("run-"), "{temp}");
        assert!(
            Path::new(&temp).starts_with(std::fs::canonicalize(&work).unwrap_or(work.clone()))
                || temp.contains("work")
        );
        assert_eq!(line("DENO_NO_UPDATE_CHECK").as_deref(), Some("1"));
        for name in ["HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY"] {
            assert!(
                line(name).is_none(),
                "{name} must not leak into a direct run"
            );
        }
        if let Ok(profile) = std::env::var("USERPROFILE") {
            assert_ne!(line("USERPROFILE").as_deref(), Some(profile.as_str()));
        }
        assert!(
            !work.join("run-").exists()
                && std::fs::read_dir(&work).unwrap().all(|e| !e
                    .unwrap()
                    .file_name()
                    .to_string_lossy()
                    .starts_with("run-")),
            "the per-run directory is removed"
        );
        // The answer is reused, so the confirmation after a preview asks nothing new.
        std::fs::remove_file(dir.path().join("info.json")).unwrap();
        assert_eq!(
            tools.video_info(&sandbox, ID, &cancel).await.unwrap()["id"],
            ID
        );
    }

    #[tokio::test]
    async fn a_routed_run_gets_the_proxy_from_the_environment_only() {
        let dir = tempfile::tempdir().unwrap();
        std::fs::write(dir.path().join("info.json"), dash().to_string()).unwrap();
        let tools = fake(dir.path(), SUCCESS);
        let proxy = Proxy {
            url: "http://127.0.0.1:7890".into(),
            auth: Some(("user".into(), "pw".into())),
        };
        let work = dir.path().join("work");
        let sandbox = Sandbox {
            work: &work,
            proxy: Some(&proxy),
        };
        tools
            .video_info(&sandbox, ID, &CancellationToken::new())
            .await
            .unwrap();
        let args = std::fs::read_to_string(dir.path().join("args.txt")).unwrap();
        assert!(!args.contains("--proxy") && !args.contains("pw"), "{args}");
        let env = std::fs::read_to_string(dir.path().join("env.txt")).unwrap();
        assert!(env.contains("HTTPS_PROXY=http://user:pw@127.0.0.1:7890/"));
        assert!(env.contains("ALL_PROXY=http://user:pw@127.0.0.1:7890/"));
    }

    use crate::network::Proxy;

    async fn failure(script: &str, adjust: impl FnOnce(&mut MediaTools)) -> CoreError {
        let dir = tempfile::tempdir().unwrap();
        std::fs::write(dir.path().join("info.json"), "x".repeat(5000)).unwrap();
        let mut tools = fake(dir.path(), script);
        adjust(&mut tools);
        let work = dir.path().join("work");
        let sandbox = Sandbox {
            work: &work,
            proxy: None,
        };
        tools
            .video_info(&sandbox, ID, &CancellationToken::new())
            .await
            .unwrap_err()
    }

    #[tokio::test]
    async fn a_failing_tool_is_classified_by_its_message_and_never_reports_success() {
        let bot =
            "@echo off\r\necho ERROR: Sign in to confirm you're not a bot 1>&2\r\nexit /b 1\r\n";
        assert!(matches!(
            failure(bot, |_| {}).await,
            CoreError::RateLimited(_)
        ));
        let gone = "@echo off\r\necho ERROR: Video unavailable 1>&2\r\nexit /b 1\r\n";
        assert!(matches!(
            failure(gone, |_| {}).await,
            CoreError::SourceUnavailable(_)
        ));
        let odd = "@echo off\r\necho ERROR: nothing known 1>&2\r\nexit /b 1\r\n";
        assert!(matches!(
            failure(odd, |_| {}).await,
            CoreError::Adapter(FailureClass::SchemaChanged)
        ));
        let junk = "@echo off\r\necho this is not json\r\n";
        assert!(matches!(
            failure(junk, |_| {}).await,
            CoreError::Adapter(FailureClass::SchemaChanged)
        ));
    }

    #[tokio::test]
    async fn output_and_time_are_bounded() {
        let big = "@echo off\r\ntype \"%~dp0info.json\"\r\n";
        let error = failure(big, |t| t.limits.output = 1000).await;
        assert!(matches!(error, CoreError::InvalidInput(m) if m.contains("output limit")));
        let slow = "@echo off\r\nping -n 30 127.0.0.1 >nul\r\n";
        let started = std::time::Instant::now();
        let error = failure(slow, |t| t.limits.run = std::time::Duration::from_secs(1)).await;
        assert!(matches!(error, CoreError::Network(m) if m.contains("timed out")));
        assert!(started.elapsed().as_secs() < 20);
    }

    #[tokio::test]
    async fn a_cancelled_run_stops_and_a_missing_tool_is_unsupported() {
        let dir = tempfile::tempdir().unwrap();
        let tools = fake(dir.path(), "@echo off\r\nping -n 30 127.0.0.1 >nul\r\n");
        let work = dir.path().join("work");
        let sandbox = Sandbox {
            work: &work,
            proxy: None,
        };
        let cancel = CancellationToken::new();
        let stop = cancel.clone();
        tokio::spawn(async move {
            tokio::time::sleep(std::time::Duration::from_millis(300)).await;
            stop.cancel();
        });
        let error = tools.video_info(&sandbox, ID, &cancel).await.unwrap_err();
        assert!(matches!(error, CoreError::Cancelled));
        let none = MediaTools::missing();
        let error = none
            .video_info(&sandbox, ID, &CancellationToken::new())
            .await
            .unwrap_err();
        assert!(
            matches!(error, CoreError::Unsupported(m) if m.contains("pinned media tools missing"))
        );
    }

    #[tokio::test]
    async fn the_tool_completes_a_plan_the_adapter_left_open() {
        let dir = tempfile::tempdir().unwrap();
        std::fs::write(dir.path().join("info.json"), dash().to_string()).unwrap();
        let tools = fake(dir.path(), SUCCESS);
        let work = dir.path().join("work");
        let sandbox = Sandbox {
            work: &work,
            proxy: None,
        };
        let mut plan = ResolveMediaResult {
            candidates: Vec::new(),
            notes: json!({"external_media": "yt_dlp"}),
        };
        apply(
            &tools,
            &sandbox,
            &CancellationToken::new(),
            ID,
            &resources(),
            &json!({"prefer_fps": false}),
            &mut plan,
        )
        .await
        .unwrap();
        let mut kept = ids(&plan.candidates);
        kept.sort();
        assert_eq!(kept, ["yt-137", "yt-140"]);
        assert!(plan.notes["silent_or_muxed"].is_null());
        let error = apply(
            &tools,
            &sandbox,
            &CancellationToken::new(),
            "bad id",
            &resources(),
            &json!({}),
            &mut plan,
        )
        .await
        .unwrap_err();
        assert!(matches!(error, CoreError::InvalidInput(_)));
    }
}
