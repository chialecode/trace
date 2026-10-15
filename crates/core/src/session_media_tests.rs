//! A site whose own video address answers only the session that produced it (TikTok): the media
//! request carries the credential with the session Cookies the site set, in the job's own scope,
//! and no CDN address ever carries them.
use crate::config::Profile;
use crate::core::Core;
use crate::jobs::{self, ClaimedJob};
use crate::objects::ReceiveParams;
use crate::transport::{host_rules, platform_rules};
use serde_json::json;
use std::sync::{Arc, Mutex};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio_util::sync::CancellationToken;
use trace_domain::ids::OperationId;

const PAGE: &str = "https://www.tiktok.com/@creator/video/7000000000000000001";
const VIDEO: &str = "https://v16-webapp-prime.tiktok.com/video/tos/useast2a/clip.mp4?x=1";
const CDN: [&str; 3] = [
    "https://v16m.tiktokcdn.com/clip.mp4",
    "https://sf16-sg.tiktokcdn-us.com/clip.mp4",
    "https://v19.tiktokv.com/clip.mp4",
];

struct Fx {
    _dir: tempfile::TempDir,
    core: Arc<Core>,
}

async fn fixture() -> Fx {
    let dir = tempfile::tempdir().unwrap();
    let core = Core::open(Profile {
        data_root: dir.path().join("data"),
        archive_root: dir.path().join("archive"),
        vault_dir: Some(dir.path().join("vault")),
        adapter_dir: Some(
            std::path::PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../target/debug"),
        ),
        test_allowed_hosts: vec!["127.0.0.1".into()],
        ..Default::default()
    })
    .await
    .unwrap();
    core.vault
        .store("ref-1", b"sessionid=mine; msToken=m")
        .unwrap();
    core.vault.store("ref-2", b"sessionid=other").unwrap();
    core.db
        .write(|tx| {
            for account in ["a1", "a2", "a3"] {
                tx.execute(
                    "INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES(?1,'tiktok',?1,'t')",
                    [account],
                )?;
            }
            for (id, reference) in [("c1", "ref-1"), ("c2", "ref-2")] {
                tx.execute(
                    "INSERT INTO credentials_state(credential_id,platform,vault_ref,status,created_at,updated_at) VALUES(?1,'tiktok',?2,'valid','t','t')",
                    [id, reference],
                )?;
            }
            for (sub, account, credential) in [
                ("s1", "a1", Some("c1")),
                ("s2", "a2", Some("c2")),
                ("s3", "a3", None),
            ] {
                tx.execute(
                    "INSERT INTO subscriptions(subscription_id,account_id,content_type,range_json,credential_id,created_at,updated_at) VALUES(?1,?2,'video','{}',?3,'t','t')",
                    rusqlite::params![sub, account, credential],
                )?;
            }
            Ok(())
        })
        .await
        .unwrap();
    Fx { _dir: dir, core }
}

async fn job_of(core: &Core, subscription: &str) -> ClaimedJob {
    jobs::enqueue(
        &core.db,
        format!("media-{subscription}"),
        "media",
        subscription,
        json!({"subscription_id": subscription}),
        0,
        1,
        0,
    )
    .await
    .unwrap();
    let mut claimed = jobs::claim_due(&core.db, "session-media", 10)
        .await
        .unwrap();
    let at = claimed
        .iter()
        .position(|j| j.payload["subscription_id"] == subscription)
        .unwrap();
    let job = claimed.remove(at);
    job.mark_running(&core.db).await.unwrap();
    job
}

fn lines(list: &[&str]) -> Vec<String> {
    list.iter().map(|l| (*l).to_owned()).collect()
}

#[tokio::test]
async fn the_site_own_video_address_carries_the_jobs_session_and_no_cdn_address_ever_does() {
    let fx = fixture().await;
    let core = &fx.core;
    let jar = core.host.session_jar();
    jar.remember("tiktok/c1/g1", &lines(&["ttwid=T1; Path=/; Secure"]));
    jar.remember("tiktok/c2/g1", &lines(&["ttwid=T2"]));
    jar.remember("tiktok", &lines(&["ttwid=ANON"]));
    let rules = platform_rules(&host_rules(&core.profile), "tiktok");
    let (one, two, anonymous) = (
        job_of(core, "s1").await,
        job_of(core, "s2").await,
        job_of(core, "s3").await,
    );
    for (job, expected) in [
        (&one, "sessionid=mine; msToken=m; ttwid=T1"),
        (&two, "sessionid=other; ttwid=T2"),
        (&anonymous, "ttwid=ANON"),
    ] {
        for url in [VIDEO, PAGE] {
            assert_eq!(
                core.media_cookie(job, &rules, url)
                    .await
                    .unwrap()
                    .as_deref(),
                Some(expected),
                "{url}"
            );
        }
        for url in CDN {
            assert_eq!(
                core.media_cookie(job, &rules, url).await.unwrap(),
                None,
                "{url}"
            );
        }
        assert_eq!(
            core.media_cookie(job, &rules, "http://v16-webapp-prime.tiktok.com/clip.mp4")
                .await
                .unwrap(),
            None,
            "only over https"
        );
        assert_eq!(
            core.media_cookie(job, &rules, "https://evil-tiktok.com/clip.mp4")
                .await
                .unwrap(),
            None
        );
    }
    // Another site's media never carries a session, whatever the jar holds.
    let weibo = platform_rules(&host_rules(&core.profile), "weibo");
    assert_eq!(
        core.media_cookie(&one, &weibo, "https://wx1.sinaimg.cn/clip.mp4")
            .await
            .unwrap(),
        None
    );
}

#[tokio::test]
async fn revoking_a_credential_drops_its_session_and_no_other() {
    let fx = fixture().await;
    let core = &fx.core;
    let jar = core.host.session_jar();
    for scope in ["tiktok/c1/g1", "tiktok/c1/g2", "tiktok/c2/g1", "tiktok"] {
        jar.remember(scope, &lines(&["ttwid=T"]));
    }
    // The revoking commands name the credential as "platform/id".
    core.clear_cookie("tiktok/c1");
    assert_eq!(jar.merged("tiktok/c1/g1", None), None);
    assert_eq!(jar.merged("tiktok/c1/g2", None), None);
    assert!(jar.merged("tiktok/c2/g1", None).is_some());
    assert!(jar.merged("tiktok", None).is_some());
    core.clear_cookie("tiktok");
    assert!(
        jar.is_empty(),
        "removing the site's credentials clears every session"
    );
}

/// Serves each connection with `answer(request)`; returns the address and what it was asked.
async fn serve(
    answer: impl Fn(&str) -> Vec<u8> + Send + 'static,
) -> (String, Arc<Mutex<Vec<String>>>) {
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap().to_string();
    let seen = Arc::new(Mutex::new(Vec::new()));
    let log = seen.clone();
    tokio::spawn(async move {
        while let Ok((mut socket, _)) = listener.accept().await {
            let mut request = Vec::new();
            while !request.ends_with(b"\r\n\r\n") {
                match socket.read_u8().await {
                    Ok(byte) => request.push(byte),
                    Err(_) => break,
                }
            }
            let request = String::from_utf8_lossy(&request).into_owned();
            let reply = answer(&request);
            log.lock().unwrap().push(request);
            let _ = socket.write_all(&reply).await;
        }
    });
    (address, seen)
}

fn ok(body: &[u8]) -> Vec<u8> {
    let mut out = format!(
        "HTTP/1.1 200 OK\r\nContent-Type: video/mp4\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
        body.len()
    )
    .into_bytes();
    out.extend_from_slice(body);
    out
}

async fn fetch(
    core: &Core,
    url: &str,
    cookie: Option<&str>,
) -> Result<u64, crate::error::CoreError> {
    let op = OperationId::new(format!("op-{}", uuid::Uuid::new_v4()));
    let mut writer = core
        .objects
        .start_receiving(
            &core.db,
            ReceiveParams {
                job_id: None,
                subject_kind: "test".into(),
                subject_id: "x".into(),
                generation: 1,
                mime: None,
                expected_size: None,
                expected_sha256: None,
                source_validator: None,
            },
            &op,
        )
        .await
        .unwrap();
    core.http
        .download_media_with(
            &host_rules(&core.profile),
            (url, cookie),
            None,
            (0, None),
            (&mut writer, &core.db, &op),
            &CancellationToken::new(),
            None,
            None,
        )
        .await
        .map(|(received, _)| received)
}

#[tokio::test]
async fn the_cookie_rides_the_request_to_its_host_and_never_follows_a_redirect_elsewhere() {
    let fx = fixture().await;
    let core = &fx.core;
    let (cdn, cdn_seen) = serve(|_| ok(b"from-the-cdn")).await;
    let target = format!("http://{cdn}/clip.mp4");
    let (site, site_seen) = serve(move |request| {
        if request.starts_with("GET /moved") {
            format!(
                "HTTP/1.1 302 Found\r\nLocation: {target}\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"
            )
            .into_bytes()
        } else {
            ok(b"from-the-site")
        }
    })
    .await;
    let cookie = "sessionid=mine; ttwid=T1";
    assert_eq!(
        fetch(core, &format!("http://{site}/clip.mp4"), Some(cookie))
            .await
            .unwrap(),
        13
    );
    assert_eq!(
        fetch(core, &format!("http://{site}/moved"), Some(cookie))
            .await
            .unwrap(),
        12
    );
    let asked = site_seen.lock().unwrap().clone();
    assert_eq!(asked.len(), 2);
    for request in &asked {
        assert!(request.contains(&format!("cookie: {cookie}")), "{request}");
    }
    let elsewhere = cdn_seen.lock().unwrap().clone();
    assert_eq!(elsewhere.len(), 1, "the redirect was followed");
    assert!(
        !elsewhere[0].to_ascii_lowercase().contains("cookie"),
        "{}",
        elsewhere[0]
    );
    // An address with no Cookie asks without one.
    assert!(
        fetch(core, &format!("http://{site}/clip.mp4"), None)
            .await
            .is_ok()
    );
    let asked = site_seen.lock().unwrap().clone();
    assert_eq!(asked.len(), 3);
    assert!(
        !asked[2].to_ascii_lowercase().contains("cookie"),
        "{}",
        asked[2]
    );
}
