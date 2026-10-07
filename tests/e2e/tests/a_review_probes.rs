//! Agent A targeted probes; synthetic data only.
use serde_json::json;
use std::time::Duration;
use tokio_util::sync::CancellationToken;
use trace_contracts::control::{Request, commands as cmd};
use trace_core::{
    config::Profile,
    core::Core,
    ipc::dispatch,
    jobs,
    objects::{ObjectStore, ReceiveParams},
    transport::{ConstrainedClient, host_rules},
};
use trace_domain::ids::OperationId;

fn request(command: &str, args: serde_json::Value) -> Request {
    Request {
        protocol_version: "0.1".into(),
        request_id: uuid::Uuid::new_v4().to_string(),
        command: command.into(),
        args,
        idempotency_key: None,
        if_revision: None,
    }
}

#[tokio::test]
async fn a_f007_chunked_range_must_reach_declared_total() {
    use tokio::io::{AsyncReadExt, AsyncWriteExt};
    let root = tempfile::tempdir().unwrap();
    let profile = Profile {
        data_root: root.path().join("data"),
        archive_root: root.path().join("archive"),
        vault_dir: Some(root.path().join("vault")),
        test_allowed_hosts: vec!["127.0.0.1".into()],
        ..Profile::default()
    };
    std::fs::create_dir_all(profile.db_path().parent().unwrap()).unwrap();
    let (db, _actor) =
        trace_core::db::DbActor::start(rusqlite::Connection::open(profile.db_path()).unwrap(), 64)
            .unwrap();
    let store = ObjectStore::new(profile.clone());
    let op = OperationId::new("range-short");
    let mut writer = store
        .start_receiving(
            &db,
            ReceiveParams {
                job_id: None,
                subject_kind: "test".into(),
                subject_id: "test".into(),
                generation: 1,
                mime: None,
                expected_size: None,
                expected_sha256: None,
                source_validator: Some("\"stable\"".into()),
            },
            &op,
        )
        .await
        .unwrap();
    writer.write_chunk(&db, &op, b"abc").await.unwrap();
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let url = format!("http://{}/file", listener.local_addr().unwrap());
    let source = tokio::spawn(async move {
        let (mut socket, _) = listener.accept().await.unwrap();
        let mut buffer = [0; 4096];
        let _ = socket.read(&mut buffer).await.unwrap();
        socket.write_all(b"HTTP/1.1 206 Partial Content\r\nETag: \"stable\"\r\nContent-Range: bytes 3-9/10\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n2\r\nde\r\n0\r\n\r\n").await.unwrap();
    });
    let result = ConstrainedClient::build()
        .unwrap()
        .download_media(
            &host_rules(&profile),
            &url,
            None,
            3,
            Some("\"stable\""),
            (&mut writer, &db, &op),
            &CancellationToken::new(),
            None,
        )
        .await;
    source.await.unwrap();
    if result.is_ok() {
        let (size, sha) = store.finish_and_validate(&db, &op, writer).await.unwrap();
        store.install_object(&db, &op, &sha).await.unwrap();
        eprintln!(
            "A probe: Range total=10, accepted and installed bytes={size}, result={result:?}"
        );
    }
    assert!(
        result.is_err(),
        "Range body ended before Content-Range total; must reject incomplete bytes"
    );
}

#[tokio::test(flavor = "multi_thread")]
async fn a_u02_subscription_resume_must_resume_scan() {
    let root = tempfile::tempdir().unwrap();
    let core = Core::open(Profile {
        data_root: root.path().join("data"),
        archive_root: root.path().join("archive"),
        vault_dir: Some(root.path().join("vault")),
        ..Profile::default()
    })
    .await
    .unwrap();
    core.db.write(|tx| {
        tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('a','fake','a','t')",[])?;
        tx.execute("INSERT INTO subscriptions(subscription_id,account_id,content_type,range_json,state,created_at,updated_at) VALUES('s','a','video','{}','enabled','t','t')",[])?;
        Ok(())
    }).await.unwrap();
    jobs::enqueue(
        &core.db,
        "scan:s:incremental".into(),
        "scan",
        "s",
        json!({"subscription_id":"s","mode":"incremental"}),
        1,
        1,
        0,
    )
    .await
    .unwrap();
    assert!(
        dispatch::dispatch(
            &core,
            request(
                cmd::SUBSCRIPTION_UPDATE,
                json!({"subscription_id":"s","enabled":false})
            )
        )
        .await
        .ok
    );
    core.start_scheduler();
    tokio::time::sleep(Duration::from_millis(800)).await;
    let paused_job: String = core
        .db
        .read(|c| Ok(c.query_row("SELECT state FROM jobs", [], |r| r.get(0))?))
        .await
        .unwrap();
    assert!(
        dispatch::dispatch(
            &core,
            request(
                cmd::SUBSCRIPTION_UPDATE,
                json!({"subscription_id":"s","enabled":true})
            )
        )
        .await
        .ok
    );
    tokio::time::sleep(Duration::from_millis(800)).await;
    let (state, sub): (String, String) = core
        .db
        .read(|c| {
            Ok((
                c.query_row("SELECT state FROM jobs", [], |r| r.get(0))?,
                c.query_row("SELECT state FROM subscriptions", [], |r| r.get(0))?,
            ))
        })
        .await
        .unwrap();
    core.shutdown().await;
    eprintln!("A probe: paused job={paused_job}, resumed subscription={sub}, job={state}");
    assert_ne!(
        state, "failed",
        "resuming a subscription must make its scheduled scan runnable again"
    );
}

#[tokio::test(flavor = "multi_thread")]
async fn a_f008_segments_must_keep_declared_size_validation() {
    use tokio::io::{AsyncReadExt, AsyncWriteExt};
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let base = format!("http://{}", listener.local_addr().unwrap());
    // Only this test creates a fake adapter process; the other tests use no adapter.
    unsafe {
        std::env::set_var("FAKE_BASE_URL", &base);
    }
    let source = tokio::spawn(async move {
        loop {
            let (mut socket, _) = listener.accept().await.unwrap();
            let mut buffer = [0; 4096];
            let n = socket.read(&mut buffer).await.unwrap();
            let header = String::from_utf8_lossy(&buffer[..n]);
            let body = if header.contains("/fake/api/stream") {
                json!({"segments":[{"u":format!("{base}/segment0"),"size":8},{"u":format!("{base}/segment1"),"size":8}]}).to_string()
            } else {
                "x".into()
            };
            let response = format!(
                "HTTP/1.1 200 OK\r\nContent-Type: application/octet-stream\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
                body.len()
            );
            let _ = socket.write_all(response.as_bytes()).await;
        }
    });
    let root = tempfile::tempdir().unwrap();
    let core = Core::open(trace_e2e::test_profile(
        root.path(),
        &trace_e2e::adapter_dir(),
    ))
    .await
    .unwrap();
    core.db.write(|tx| {
        tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('a','fake','a','t')",[])?;
        tx.execute("INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES('e','fake','clip','v-2','a','t')",[])?;
        tx.execute("INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at,completeness) VALUES('r','e','d','two segments','body','t','full')",[])?;
        tx.execute("INSERT INTO media_items(media_id,revision_id,role,position,source_attachment_id,group_key,required) VALUES('m','r','video',1,'v-2/p1','p1',1)",[])?;
        Ok(())
    }).await.unwrap();
    jobs::enqueue(
        &core.db,
        "segments".into(),
        "media",
        "e",
        json!({"entry_id":"e","revision_id":"r","group":"p1"}),
        1,
        1,
        0,
    )
    .await
    .unwrap();
    core.start_scheduler();
    let mut state = String::new();
    for _ in 0..100 {
        state = core
            .db
            .read(|c| Ok(c.query_row("SELECT state FROM jobs", [], |r| r.get::<_, String>(0))?))
            .await
            .unwrap();
        if !matches!(state.as_str(), "queued" | "leased" | "running") {
            break;
        }
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    let page = trace_core::feed::query(&core.db, serde_json::from_value(json!({})).unwrap())
        .await
        .unwrap();
    let saved_bytes:i64=core.db.read(|c|Ok(c.query_row("SELECT COALESCE(sum(b.size),0) FROM media_variants v JOIN blobs b ON b.blob_id=v.blob_id",[],|r|r.get(0))?)).await.unwrap();
    core.shutdown().await;
    source.abort();
    eprintln!(
        "A probe: segments declare 8+8 bytes, saved={saved_bytes}, job={state}, save_progress={}",
        page["items"][0]["save_progress"]
    );
    assert_ne!(
        state, "succeeded",
        "undersized segments must not make the resource group successful"
    );
}
