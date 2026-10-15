//! Optimized, isolated load harness. All acquisition runs through the production
//! scheduler/ObjectStore/IPC; only the two upstream adapters and HTTP are synthetic.
use serde_json::json;
use std::{
    path::PathBuf,
    sync::{
        Arc,
        atomic::{AtomicU64, Ordering},
    },
    time::{Duration, Instant},
};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use trace_core::{
    config::Profile,
    core::Core,
    ipc::{client::Client, server::IpcServer},
    pluginhost::AdapterSpec,
};
type Result<T> = std::result::Result<T, Box<dyn std::error::Error + Send + Sync>>;

#[tokio::main]
async fn main() -> Result<()> {
    let args = std::env::args().collect::<Vec<_>>();
    let root = PathBuf::from(args.get(1).ok_or("missing new output directory")?);
    if let Some(gate) = args.get(5) {
        tokio::time::timeout(Duration::from_secs(30), async {
            while !std::path::Path::new(gate).exists() {
                tokio::time::sleep(Duration::from_millis(10)).await;
            }
        })
        .await?;
    }
    if root.exists() {
        return Err("output directory already exists".into());
    }
    let idle: u64 = args.get(2).ok_or("idle seconds")?.parse()?;
    let duration: u64 = args.get(3).ok_or("recording seconds")?.parse()?;
    let entries: i64 = args
        .get(4)
        .map(|s| s.parse())
        .transpose()?
        .unwrap_or(100000);
    std::fs::create_dir_all(&root)?;
    let root = std::fs::canonicalize(root)?;
    let exe = std::env::current_exe()?;
    let bin = exe.parent().ok_or("bin path")?;
    let profile = Profile {
        archive_id: format!("measure-{}", uuid::Uuid::new_v4()),
        data_root: root.join("data"),
        archive_root: root.join("archive"),
        vault_dir: Some(root.join("vault")),
        adapter_dir: Some(bin.to_path_buf()),
        media_dir: Some(PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../target/media")),
        test_allowed_hosts: vec!["127.0.0.1".into()],
        ..Default::default()
    };
    std::fs::write(
        root.join("profile.json"),
        serde_json::to_vec_pretty(&profile)?,
    )?;
    let specs: Vec<AdapterSpec> = ["load_bili", "load_douyin"]
        .into_iter()
        .map(|p| AdapterSpec {
            platform: p.into(),
            plugin_id: p.into(),
            binary_path: bin.join("m1_bench_adapter.exe"),
        })
        .collect();
    let core = Core::open_with_adapters(profile.clone(), specs.clone()).await?;
    let seed = Instant::now();
    core.db.write(move|tx|{
        for ns in ["load_bili","load_douyin"]{
            for n in 0..60 {
                let aid=format!("{ns}-{n}");let sub=format!("sub-{aid}");let live=n>=50;
                tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,display_name,first_seen_at) VALUES(?1,?2,?3,'合成账号','t')",rusqlite::params![aid,ns,n.to_string()])?;
                tx.execute("INSERT INTO subscriptions(subscription_id,account_id,content_type,range_json,check_interval_seconds,created_at,updated_at) VALUES(?1,?2,?3,'{}',?4,?5,?5)",rusqlite::params![sub,aid,if live{"live"}else{"video"},if live{30}else{300},trace_core::jobs::now()])?;
                tx.execute("INSERT INTO jobs(job_id,job_key,job_type,subject_id,payload,priority,state,created_at,updated_at) VALUES(?1,?1,'scan',?2,?3,?4,'queued','t','t')",rusqlite::params![format!("scan:{sub}:incremental"),sub,json!({"subscription_id":sub,"mode":"incremental"}).to_string(),if live{0}else{1}])?;
            }
        }
        let text="中文档案图文视频长篇离线搜索记录".repeat(32);
        let (uni,bi)=trace_core::search::grams(&text);
        let cache_charge=text.len()*4+1024+128*(uni.len()+bi.len());
        for n in 0..entries {
            let id=format!("seed-{n}");let ns=if n%2==0{"load_bili"}else{"load_douyin"};let kind=["video","dynamic","image","article","live"][(n%5)as usize];
            tx.prepare_cached("INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at,published_at) VALUES(?1,?2,?3,?1,?4,'t','2026-10-01T00:00:00Z')")?.execute(rusqlite::params![id,ns,kind,format!("{ns}-0")])?;
            tx.prepare_cached("INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,completeness,observed_at) VALUES(?1,?1,'d','中文合成档案',?2,'full','t')")?.execute(rusqlite::params![id,text])?;
            for m in 0..5{tx.prepare_cached("INSERT INTO media_items(media_id,revision_id,role,position,source_attachment_id,required) VALUES(?1,?2,'image',?3,?1,0)")?.execute(rusqlite::params![format!("media-{n}-{m}"),id,m])?;}
            tx.prepare_cached("INSERT INTO search_documents(doc_kind,ref_id,entry_id,namespace,title,normalized_text,index_version,cache_bytes) VALUES('entry',?1,?1,?2,'中文合成档案',?3,1,?4)")?.execute(rusqlite::params![id,ns,text,cache_charge as i64])?;
            let doc=tx.last_insert_rowid();
            for gram in &uni{tx.prepare_cached("INSERT INTO search_unigram VALUES(?1,?2)")?.execute(rusqlite::params![gram,doc])?;}
            for gram in &bi{tx.prepare_cached("INSERT INTO search_bigram VALUES(?1,?2)")?.execute(rusqlite::params![gram,doc])?;}
        }Ok(())
    }).await?;
    let shape: (i64, i64, i64, i64) = core
        .db
        .read(|c| {
            Ok(c.query_row(
                "SELECT (SELECT min(length(normalized_body)) FROM entry_revisions),
                        (SELECT max(length(normalized_body)) FROM entry_revisions),
                        (SELECT count(*) FROM subscriptions WHERE content_type!='live'),
                        (SELECT count(*) FROM subscriptions WHERE content_type='live')",
                [],
                |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?, r.get(3)?)),
            )?)
        })
        .await?;
    if shape.0 != shape.1 || shape.0 < 500 || shape.2 != 100 || shape.3 != 20 {
        return Err("seed workload does not match the declared NFR baseline".into());
    }
    let segment = root.join("source.ts");
    let status =
        std::process::Command::new(core.media_backend.ffmpeg.as_ref().ok_or("pinned ffmpeg")?)
            .args([
                "-hide_banner",
                "-loglevel",
                "error",
                "-f",
                "lavfi",
                "-i",
                "testsrc2=size=1280x720:rate=25",
                "-t",
                "2",
                "-c:v",
                "mpeg2video",
                "-b:v",
                "8M",
                "-minrate",
                "8M",
                "-maxrate",
                "8M",
                "-bufsize",
                "8M",
                "-f",
                "mpegts",
            ])
            .arg(&segment)
            .status()?;
    if !status.success() {
        return Err("source generation failed".into());
    }
    let bytes = Arc::new(std::fs::read(segment)?);
    let segment_bytes = bytes.len();
    // First queries use a newly opened Core/SQLite connection, not the seeding
    // connection's page/statement caches. The OS file cache is left alone so
    // measurement does not disrupt other applications on the user's desktop.
    core.shutdown().await;
    drop(core);
    let core = Core::open_with_adapters(profile.clone(), specs.clone()).await?;
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await?;
    let base = format!("http://{}", listener.local_addr()?);
    // Only this dedicated harness sets environment before spawning the adapters.
    unsafe {
        std::env::set_var("TRACE_BENCH_SOURCE", &base);
        std::env::set_var("TRACE_BENCH_DIAGNOSTICS", &root);
    }
    let start = Instant::now();
    let observable_unix = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)?
        .as_secs_f64()
        + idle as f64;
    let traffic = Arc::new(AtomicU64::new(0));
    let sent = traffic.clone();
    let server = tokio::spawn(async move {
        loop {
            let Ok((mut socket, _)) = listener.accept().await else {
                break;
            };
            let bytes = bytes.clone();
            let sent = sent.clone();
            tokio::spawn(async move {
                let mut req = Vec::new();
                while !req.ends_with(b"\r\n\r\n") {
                    let mut chunk = [0u8; 1024];
                    let n = socket.read(&mut chunk).await?;
                    if n == 0 || req.len() + n > 8192 {
                        return Err(std::io::Error::other("invalid synthetic request headers"));
                    }
                    req.extend_from_slice(&chunk[..n]);
                }
                let req = String::from_utf8_lossy(&req);
                let path = req.split_whitespace().nth(1).unwrap_or("/");
                tokio::time::sleep(Duration::from_millis(100)).await;
                let seconds = start.elapsed().as_secs();
                let recording = seconds >= idle;
                let mixed = seconds >= idle + duration / 2;
                let repeats = (duration / 4).max(1);
                let body = if path.starts_with("/state") {
                    let account = path
                        .split("account=")
                        .nth(1)
                        .and_then(|s| s.parse::<u64>().ok())
                        .unwrap_or(999);
                    json!({"live":recording&&(account==50||(!mixed&&account==51)),"download":mixed&&account==0,"download_bytes":segment_bytes as u64*repeats}).to_string().into_bytes()
                } else if path.starts_with("/index.m3u8") {
                    let seq = seconds.saturating_sub(idle) / 2;
                    let first = seq.saturating_sub(5);
                    let items = (first..=seq)
                        .map(|s| format!("#EXTINF:2,\nseg.ts?seq={s}\n"))
                        .collect::<String>();
                    format!(
                        "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:{first}\n{items}"
                    )
                    .into_bytes()
                } else if path.starts_with("/download") {
                    socket.write_all(format!("HTTP/1.1 200 OK\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",segment_bytes as u64*repeats).as_bytes()).await?;
                    for _ in 0..repeats {
                        socket.write_all(&bytes).await?;
                        sent.fetch_add(bytes.len() as u64, Ordering::Relaxed);
                        tokio::time::sleep(Duration::from_secs(2)).await;
                    }
                    return Ok::<(), std::io::Error>(());
                } else {
                    let seq = path
                        .split("seq=")
                        .nth(1)
                        .and_then(|s| s.parse::<u64>().ok())
                        .unwrap_or(0);
                    let mut out = bytes.to_vec();
                    // Legal MPEG-TS null packet makes each acquired source interval unique.
                    let mut null = [0xff; 188];
                    null[..4].copy_from_slice(&[0x47, 0x1f, 0xff, 0x10]);
                    null[4..12].copy_from_slice(&seq.to_be_bytes());
                    out.extend_from_slice(&null);
                    out
                };
                socket
                    .write_all(
                        format!(
                            "HTTP/1.1 200 OK\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
                            body.len()
                        )
                        .as_bytes(),
                    )
                    .await?;
                socket.write_all(&body).await?;
                sent.fetch_add(body.len() as u64, Ordering::Relaxed);
                Ok::<(), std::io::Error>(())
            });
        }
    });
    core.recover().await?;
    core.start_scheduler();
    let service = IpcServer { core: core.clone() };
    let ipc = tokio::spawn(async move { service.serve().await });
    tokio::time::sleep(Duration::from_millis(200)).await;
    let mut client =
        Client::connect(&profile.pipe_name(), "m1-load", Some(&profile.archive_id)).await?;
    let mut events = std::mem::replace(&mut client.events, tokio::sync::mpsc::channel(1).1);
    tokio::spawn(async move { while events.recv().await.is_some() {} });
    std::fs::write(
        root.join("ready.json"),
        serde_json::to_vec(
            &json!({"pid":std::process::id(),"metadataEntries":entries,"mediaMetadata":entries*5,"bodyMedianChars":shape.0,"ordinaryCheckTargets":shape.2,"liveCheckTargets":shape.3,"httpLatencyMs":100,"queryPageSize":50,"seedSeconds":seed.elapsed().as_secs_f64(),"streamBytesPerSecond":segment_bytes/2,"idleSeconds":idle,"durationSeconds":duration,"queryCacheState":"new_core_connection_os_cache_unflushed"}),
        )?,
    )?;
    let mut queries: Vec<f64> = Vec::new();
    let mut tick = tokio::time::interval(Duration::from_secs(5));
    while start.elapsed().as_secs() <= idle + duration {
        tick.tick().await;
        if start.elapsed().as_secs() >= idle + duration / 2 {
            // Request the first mixed-phase scan until it durably discovers each
            // download. A scan already in flight at the phase boundary can
            // otherwise overwrite a one-shot next_run_at with its old cycle.
            core.db.write(|tx|{
                tx.execute("UPDATE jobs SET next_run_at=?1 WHERE job_type='scan' AND state='queued' AND job_key IN ('scan:sub-load_bili-0:incremental','scan:sub-load_douyin-0:incremental') AND NOT EXISTS(SELECT 1 FROM entries e WHERE e.account_id=substr(jobs.subject_id,5) AND e.entry_id NOT LIKE 'seed-%')",[trace_core::jobs::now()])?;
                Ok(())
            }).await?;
        }
        let mut times = Vec::new();
        let queries_due = start.elapsed().as_secs() >= idle || queries.is_empty();
        for query in ["中", "中文", "离线搜索", "图文视频"]
            .into_iter()
            .filter(|_| queries_due)
        {
            let t = Instant::now();
            let page = client
                .request(
                    "archive.timeline",
                    json!({"query":query,"limit":50,"namespace":"load_bili"}),
                    None,
                    None,
                )
                .await?;
            if page["items"]
                .as_array()
                .is_none_or(|a| a.len() != 50.min(entries as usize / 2))
            {
                return Err("load query returned incorrect page".into());
            }
            times.push(t.elapsed().as_secs_f64() * 1000.0);
        }
        queries.extend_from_slice(&times);
        let counts=core.db.read(|c|Ok(json!({"queueDepth":c.query_row("SELECT count(*) FROM jobs WHERE state IN ('queued','running','claimed')",[],|r|r.get::<_,i64>(0))?,"activeRecordings":c.query_row("SELECT count(*) FROM live_sessions WHERE state IN ('recording','reconnecting') AND manual_stopped=0",[],|r|r.get::<_,i64>(0))?,"activeDownloads":c.query_row("SELECT count(*) FROM jobs WHERE job_type='media' AND state='running'",[],|r|r.get::<_,i64>(0))?,"sealedFragments":c.query_row("SELECT count(*) FROM live_fragments WHERE state='sealed'",[],|r|r.get::<_,i64>(0))?}))).await?;
        let progress = json!({"elapsedSeconds":start.elapsed().as_secs_f64(),"phase":if start.elapsed().as_secs()<idle{"idle"}else if start.elapsed().as_secs()<idle+duration/2{"four-recordings"}else{"mixed"},"queryTimesMs":times,"proxyCounts":core.host.proxy_counts(),"desktopClients":core.desktop_event_clients.load(Ordering::Relaxed),"desktopConnections":core.desktop_event_connections.load(Ordering::Relaxed),"throughputBytes":traffic.load(Ordering::Relaxed),"jobs":counts});
        std::fs::write(root.join("progress.json"), serde_json::to_vec(&progress)?)?;
    }
    std::fs::write(
        root.join("query-samples.json"),
        serde_json::to_vec(&queries)?,
    )?;
    let starts=core.db.read(move|c|Ok(c.prepare("SELECT s.session_id,(julianday(s.started_at)-2440587.5)*86400.0,(julianday(MIN(m.created_at))-2440587.5)*86400.0 FROM live_sessions s JOIN live_fragments f ON f.session_id=s.session_id JOIN materializations m ON m.subject_id=f.media_id GROUP BY s.session_id")?.query_map([],|r|Ok(json!({"session":r.get::<_,String>(0)?,"observableUnix":observable_unix,"pollIntervalSeconds":30,"detectedUnix":r.get::<_,f64>(1)?,"firstReceiveUnix":r.get::<_,f64>(2)?})))?.collect::<std::result::Result<Vec<_>,_>>()?)).await?;
    let errors: i64 = core
        .db
        .read(|c| {
            Ok(c.query_row(
                "SELECT count(*) FROM jobs WHERE last_error IS NOT NULL",
                [],
                |r| r.get(0),
            )?)
        })
        .await?;
    let unfinished: i64 = core.db.read(|c|Ok(c.query_row("SELECT count(*) FROM jobs j JOIN subscriptions s ON s.subscription_id=json_extract(j.payload,'$.subscription_id') WHERE s.content_type!='live' AND j.state NOT IN ('succeeded','failed','cancelled')",[],|r|r.get(0))?)).await?;
    if unfinished < 100 {
        return Err("recovery workload requires 100 unfinished ordinary tasks".into());
    }
    core.shutdown().await;
    drop(client);
    let _ = ipc.await;
    drop(core);
    let recovery_start = start.elapsed().as_secs_f64();
    let recovered = Core::open_with_adapters(profile.clone(), specs).await?;
    recovered.recover().await?;
    let rows: i64 = recovered
        .db
        .read(|c| {
            Ok(c.query_row(
                "SELECT count(*) FROM entries WHERE entry_id LIKE 'seed-%'",
                [],
                |r| r.get(0),
            )?)
        })
        .await?;
    if rows != entries {
        return Err("recovered metadata count mismatch".into());
    }
    recovered.start_scheduler();
    let service = IpcServer {
        core: recovered.clone(),
    };
    let restored_ipc = tokio::spawn(async move { service.serve().await });
    let restored_client = tokio::time::timeout(Duration::from_secs(5), async {
        loop {
            if let Ok(client) = Client::connect(
                &profile.pipe_name(),
                "m1-recovery",
                Some(&profile.archive_id),
            )
            .await
            {
                break client;
            }
            tokio::time::sleep(Duration::from_millis(20)).await;
        }
    })
    .await?;
    let status = restored_client
        .request("daemon.status", json!({}), None, None)
        .await?;
    if status["instance_id"] != recovered.instance_id.as_str() {
        return Err("reopened service instance mismatch".into());
    }
    let recovery_end = start.elapsed().as_secs_f64();
    std::fs::write(
        root.join("lifecycle.json"),
        serde_json::to_vec(
            &json!({"recordingStarts":starts,"jobErrors":errors,"recovery":{"kind":"core_reopen","startedElapsed":recovery_start,"finishedElapsed":recovery_end,"metadataEntries":rows,"unfinishedOrdinaryTasks":unfinished,"serviceAccessible":true}}),
        )?,
    )?;
    recovered.shutdown().await;
    drop(restored_client);
    let _ = restored_ipc.await;
    server.abort();
    Ok(())
}
