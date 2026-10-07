//! Real production adapters over stdio, synthetic metadata at the proxy boundary,
//! real local HTTP bytes through Core acquisition, then offline reads and edits.
use super::*;
use crate::storage_ops;
use base64::Engine;
use serde_json::json;
use std::sync::atomic::AtomicUsize;
use tokio::io::{AsyncReadExt, AsyncWriteExt};

async fn drain(core: &Arc<Core>) {
    for _ in 0..200 {
        let jobs = jobs::claim_due(&core.db, "vertical", 1).await.unwrap();
        if jobs.is_empty() {
            return;
        }
        for job in jobs {
            let kind = job.job_type.clone();
            let id = job.job_id.clone();
            job.mark_running(&core.db).await.unwrap();
            let result = match kind.as_str() {
                "scan" => run_scan(core, &job).await,
                "observe" => run_observe(core, &job).await,
                "media" => run_media(core, &job).await,
                "account_profile" => crate::accounts::refresh_job(core, &job).await,
                _ => panic!("unexpected job type"),
            };
            assert!(result.is_ok(), "{kind} {:?}: {result:?}", job.payload);
            let _ = job.succeed(&core.db).await;
            let failure: Option<String> = core
                .db
                .read(move |c| {
                    Ok(
                        c.query_row("SELECT last_error FROM jobs WHERE job_id=?1", [id], |r| {
                            r.get(0)
                        })?,
                    )
                })
                .await
                .unwrap();
            assert!(failure.is_none(), "{kind} failed: {failure:?}");
        }
    }
    panic!("vertical acquisition exceeded job budget");
}

#[tokio::test]
async fn c02_gallery_live_photo_required_tracks_and_edit_resources() {
    let temp = tempfile::tempdir().unwrap();
    let core = Core::open(Profile {
        archive_id: "production-vertical".into(),
        data_root: temp.path().join("data"),
        archive_root: temp.path().join("archive"),
        vault_dir: Some(temp.path().join("vault")),
        adapter_dir: Some(
            std::path::PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../target/debug"),
        ),
        test_allowed_hosts: vec!["127.0.0.1".into(), "fixture.douyinpic.com".into()],
        ..Default::default()
    })
    .await
    .unwrap();
    let video = temp.path().join("fixture.mp4");
    let status = std::process::Command::new(core.media_backend.ffmpeg.as_ref().unwrap())
        .args([
            "-hide_banner",
            "-loglevel",
            "error",
            "-f",
            "lavfi",
            "-i",
            "testsrc=size=64x64:rate=10",
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
        ])
        .arg(&video)
        .status()
        .unwrap();
    assert!(status.success());
    let video = std::fs::read(video).unwrap();
    let video_size = video.len();
    let png=base64::engine::general_purpose::STANDARD.decode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aF9sAAAAASUVORK5CYII=").unwrap();
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    core.http.route_fixture(
        "fixture.douyinpic.com",
        &format!("http://{}", listener.local_addr().unwrap()),
    );
    let media_requests = Arc::new(AtomicUsize::new(0));
    let observed = media_requests.clone();
    let server = tokio::spawn(async move {
        loop {
            let (mut socket, _) = listener.accept().await.unwrap();
            let mut request = vec![];
            let mut block = [0; 2048];
            while !request.windows(4).any(|w| w == b"\r\n\r\n") {
                let n = socket.read(&mut block).await.unwrap();
                if n == 0 {
                    break;
                }
                request.extend_from_slice(&block[..n]);
                assert!(request.len() < 8192);
            }
            let request = String::from_utf8_lossy(&request);
            assert!(!request.to_ascii_lowercase().contains("cookie:"));
            observed.fetch_add(1, Ordering::SeqCst);
            let (bytes, mime) = if request.contains(".mp4") {
                (&video, "video/mp4")
            } else {
                (&png, "image/png")
            };
            socket.write_all(format!("HTTP/1.1 200 OK\r\nContent-Length: {}\r\nContent-Type: {mime}\r\nConnection: close\r\n\r\n",bytes.len()).as_bytes()).await.unwrap();
            socket.write_all(bytes).await.unwrap();
        }
    });
    let phase = Arc::new(AtomicUsize::new(0));
    let fixture_phase = phase.clone();
    let metadata_requests = Arc::new(AtomicUsize::new(0));
    let counted = metadata_requests.clone();
    core.host.set_proxy_fixture(Arc::new(move|p|{
        let url=url::Url::parse(p["url"].as_str().unwrap()).unwrap();assert_eq!(url.scheme(),"https");counted.fetch_add(1,Ordering::SeqCst);
        let phase=fixture_phase.load(Ordering::SeqCst);let edit=phase>=2;
        let image=|n:usize|json!({"uri":format!("image-{n}-{}",if edit{2}else{1}),"width":if n==0{1920}else{1080},"height":if n==0{1080}else{1920},"url_list":[format!("https://fixture.douyinpic.com/{n}-{}.png",if edit{2}else{1})]});
        let douyin=|id:&str|match id{
            "101"=>json!({"aweme_id":"101","aweme_type":68,"desc":if edit{"编辑后的图文"}else{"原始图文"},"images":[image(0),{"uri":"motion-still","width":1080,"height":1920,"url_list":["https://fixture.douyinpic.com/portrait.png"],"video":{"play_addr":{"url_list":["https://fixture.douyinpic.com/motion.mp4"]}}}],"music":{"play_url":{"url_list":["https://fixture.douyinpic.com/music.mp4"]}},"statistics":{"digg_count":phase}}),
            "102"=>json!({"aweme_id":"102","aweme_type":163,"desc":"摘要","article_info":{"article_title":"完整长文","article_content":{"markdown":format!("# 全文\n{}\n![图](https://fixture.douyinpic.com/article.png)","完整正文".repeat(800))},"has_more":false}}),
            "103"=>json!({"aweme_id":"103","aweme_type":0,"desc":"视频说明","video":{"vid":"stable-video","width":64,"height":64,"play_addr":{"url_list":["https://fixture.douyinpic.com/video.mp4"]}}}),
            _=>panic!("unexpected Douyin identity")
        };
        let data=match (url.host_str().unwrap(),url.path()){
            ("www.douyin.com","/aweme/v1/web/user/profile/other/")=>json!({"status_code":0,"user":{"sec_uid":"MS4fixture","uid":"90071992547409931","nickname":if edit{"新昵称"}else{"旧昵称"}}}),
            ("www.douyin.com","/aweme/v1/web/aweme/post/")=>json!({"status_code":0,"has_more":false,"max_cursor":0,"aweme_list":[douyin("101"),douyin("102"),douyin("103")]}),
            ("www.douyin.com","/aweme/v1/web/mix/list/")=>json!({"status_code":0,"has_more":false,"cursor":0,"mix_infos":[]}),
            ("www.douyin.com","/aweme/v1/web/series/list/")=>json!({"status_code":0,"has_more":false,"cursor":0,"series_infos":[]}),
            ("www.douyin.com","/aweme/v1/web/aweme/detail/")=>json!({"status_code":0,"aweme_detail":douyin(&url.query_pairs().find(|(k,_)|k=="aweme_id").unwrap().1)}),
            ("api.bilibili.com","/x/web-interface/card")=>json!({"code":0,"data":{"card":{"mid":"1","name":"B站作者"}}}),
            ("api.bilibili.com","/x/web-interface/nav")=>json!({"code":0,"data":{"wbi_img":{"img_url":"https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png","sub_url":"https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png"}}}),
            ("api.bilibili.com","/x/space/article")=>json!({"code":0,"data":{"count":1,"articles":[{"id":201,"title":"专栏全文","summary":"摘要"}]}}),
            ("api.bilibili.com","/x/article/view")=>json!({"code":0,"data":{"id":201,"title":"专栏全文","content":"<h2>专栏全文</h2><p>完整文章内容</p><img src='https://fixture.douyinpic.com/bili.png'>"}}),
            ("api.bilibili.com","/x/polymer/web-dynamic/v1/feed/space")=>json!({"code":0,"data":{"has_more":false,"items":[{"id_str":"202","modules":{"module_dynamic":{"desc":{"text":"动态摘要"}}}}]}}),
            ("api.bilibili.com","/x/polymer/web-dynamic/v1/detail")=>json!({"code":0,"data":{"item":{"id_str":"202","type":"DYNAMIC_TYPE_WORD","modules":{"module_dynamic":{"desc":{"text":"完整文字动态"}}}}}}),
            ("api.bilibili.com","/x/polymer/web-dynamic/v1/opus/feed/space")=>json!({"code":0,"data":{"has_more":false,"items":[{"opus_id":"203","content":"图文摘要"}]}}),
            ("api.bilibili.com","/x/polymer/web-dynamic/v1/opus/detail")=>json!({"code":0,"data":{"item":{"id_str":"203","modules":[{"module_content":{"paragraphs":[{"para_type":1,"text":{"nodes":[{"word":{"words":"图文完整正文"}}]}},{"para_type":2,"pic":{"pics":[{"url":"https://fixture.douyinpic.com/opus.png","width":1,"height":1}]}}]}}]}}}),
            ("api.bilibili.com","/x/space/wbi/arc/search")=>json!({"code":0,"data":{"page":{"count":1},"list":{"vlist":[{"bvid":"BVfixture","title":"视频","description":"视频说明"}]}}}),
            ("api.bilibili.com","/x/web-interface/wbi/view")|("api.bilibili.com","/x/web-interface/view")=>json!({"code":0,"data":{"bvid":"BVfixture","aid":17,"title":"视频","desc":"视频完整说明","pages":[{"cid":31,"page":1,"part":"第一部分"},{"cid":32,"page":2,"part":"第二部分"}]}}),
            ("api.bilibili.com","/x/player/wbi/v2")=>json!({"code":0,"data":{"subtitle":{"subtitles":[]}}}),
            ("api.bilibili.com","/x/player/wbi/playurl")=>json!({"code":0,"data":{"durl":[{"order":1,"size":video_size,"length":1000,"url":"https://fixture.douyinpic.com/bili-video.mp4"}]}}),
            _=>panic!("unexpected production endpoint: {}",url.path())
        };
        let body=serde_json::to_vec(&data).unwrap();assert!(body.len()<=p["max_bytes"].as_u64().unwrap()as usize);
        Ok(trace_contracts::plugin::ProxyFetchResult{status:200,headers:json!({}),body_b64:base64::engine::general_purpose::STANDARD.encode(body),final_url:url.to_string()})
    }));
    for (namespace, input, kinds) in [
        ("douyin", "MS4fixture", vec!["dynamic"]),
        (
            "bilibili",
            "1",
            vec!["article", "dynamic", "image", "video"],
        ),
    ] {
        let resolved =
            crate::accounts::resolve(&core, json!({"namespace":namespace,"input":input}))
                .await
                .unwrap();
        for kind in kinds {
            let sub = format!("{namespace}-{kind}");
            let account = resolved["account_id"].as_str().unwrap().to_owned();
            let s = sub.clone();
            let kind = kind.to_owned();
            core.db.write(move|tx|{tx.execute("INSERT INTO subscriptions(subscription_id,account_id,content_type,range_json,created_at,updated_at) VALUES(?1,?2,?3,'{}','t','t')",rusqlite::params![s,account,kind])?;Ok(())}).await.unwrap();
            jobs::enqueue(
                &core.db,
                format!("scan:{sub}:history"),
                "scan",
                &sub,
                json!({"subscription_id":sub,"mode":"history"}),
                1,
                1,
                0,
            )
            .await
            .unwrap();
        }
    }
    drain(&core).await;
    let rows:Vec<(String,String,String,String)>=core.db.read(|c|Ok(c.prepare("SELECT e.entry_id,e.namespace,e.native_kind,r.completeness FROM entries e JOIN current_entry_revisions r ON r.entry_id=e.entry_id ORDER BY e.namespace,e.native_kind")?.query_map([],|r|Ok((r.get(0)?,r.get(1)?,r.get(2)?,r.get(3)?)))?.collect::<Result<Vec<_>,_>>()?)).await.unwrap();
    assert_eq!(rows.len(), 7);
    assert!(rows.iter().all(|r| r.3 == "full"), "{rows:?}");
    let gallery = rows
        .iter()
        .find(|r| r.1 == "douyin" && r.2 == "image")
        .unwrap()
        .0
        .clone();
    for (entry, _, _, _) in &rows {
        let id = entry.clone();
        let state = core
            .db
            .read(move |c| crate::ipc::dispatch::entry_states_sync(c, &id).map(|v| v.1))
            .await
            .unwrap();
        assert_eq!(state, "complete", "entry {entry}");
    }
    let before = media_requests.load(Ordering::SeqCst);
    assert!(before >= 10);
    let id = gallery.clone();
    let original = core
        .db
        .read(move |c| storage_ops::generate_manifest(c, &id))
        .await
        .unwrap()
        .unwrap();
    assert!(
        original["media"]
            .as_array()
            .unwrap()
            .iter()
            .any(|m| m["role"] == "motion")
    );
    assert!(
        original["media"]
            .as_array()
            .unwrap()
            .iter()
            .any(|m| m["role"] == "music")
    );
    for round in [1, 2] {
        phase.store(round, Ordering::SeqCst);
        let entry = gallery.clone();
        core.db.write(move|tx|{tx.execute("UPDATE jobs SET state='queued',next_run_at=?2,target_generation=target_generation+1,lease_owner=NULL,lease_expires_at=NULL WHERE job_type='observe' AND subject_id=?1",rusqlite::params![entry,jobs::now()])?;Ok(())}).await.unwrap();
        drain(&core).await;
        if round == 1 {
            assert_eq!(
                media_requests.load(Ordering::SeqCst),
                before,
                "statistics-only refresh reuses verified resources"
            );
        }
    }
    let entry = gallery.clone();
    let versions: i64 = core
        .db
        .read(move |c| {
            Ok(c.query_row(
                "SELECT count(*) FROM entry_revisions WHERE entry_id=?1 AND completeness='full'",
                [entry],
                |r| r.get(0),
            )?)
        })
        .await
        .unwrap();
    assert_eq!(versions, 2);
    core.host.shutdown_all().await;
    server.abort();
    crate::search::index_pending(&core.db, 100).await.unwrap();
    for (entry, _, _, _) in rows {
        let manifest = core
            .db
            .read(move |c| storage_ops::generate_manifest(c, &entry))
            .await
            .unwrap()
            .unwrap();
        assert!(!manifest.to_string().contains("a_bogus"));
        for media in manifest["media"].as_array().unwrap() {
            if let Some(blob) = media["blob_id"].as_str() {
                assert!(
                    !core
                        .objects
                        .read_range(blob.strip_prefix("sha256:").unwrap(), 0, 32)
                        .unwrap()
                        .is_empty()
                );
            }
        }
    }
    assert!(metadata_requests.load(Ordering::SeqCst) > 15);
    core.shutdown().await;
}
