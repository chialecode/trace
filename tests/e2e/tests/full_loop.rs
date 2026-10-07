//! 离线全流程闭环（C01/C10/C13/C17/C20/C27 合成/C30）：
//! 合成 HTTP 源 + fake 适配器 + 核心（进程内）+ 调度器。
//!
//! 链路：创作者 → 平台账号 → 订阅（近期范围）→ 扫描（游标分页两页）
//! → 详情观察 → 媒体下载/校验/提交 → 中文搜索 → 备份 → 恢复校验 → 哨兵扫描。
//! 全程无真实网络；127.0.0.1 经 profile.test_allowed_hosts 显式放行。

use std::time::Duration;
use tempfile::TempDir;
use trace_contracts::control::{Request, commands as cmd};
use trace_core::config::Profile;
use trace_core::core::Core;
use trace_core::ipc::dispatch;
use trace_e2e::{FakeSource, collect_files, test_profile};

fn req(command: &'static str, args: serde_json::Value, key: Option<String>) -> Request {
    Request {
        protocol_version: trace_domain::CONTROL_PROTOCOL_VERSION.into(),
        request_id: format!("e2e-{}", uuid::Uuid::new_v4()),
        command: command.into(),
        idempotency_key: key,
        if_revision: None,
        args,
    }
}

async fn count(core: &Core, sql: &'static str) -> i64 {
    core.db
        .read(move |c| Ok(c.query_row(sql, [], |r| r.get::<_, i64>(0))?))
        .await
        .unwrap_or(0)
}

#[tokio::test(flavor = "multi_thread")]
async fn offline_full_loop_scan_observe_media_search_backup_restore() {
    let source = FakeSource::start();
    // 适配器子进程经环境变量获得合成源地址（测试进程级注入）。
    // SAFETY: 本测试二进制只含此一个用例，设置发生在 spawn 适配器之前。
    unsafe {
        std::env::set_var("FAKE_BASE_URL", &source.base_url);
    }
    let root = TempDir::new().unwrap();
    let profile: Profile = test_profile(
        root.path(),
        std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../../target/debug")
            .as_path(),
    );

    let core = Core::open(profile.clone()).await.expect("core open");
    // 哨兵凭证（C13）：合成认证仅用于本地代理，显式关联订阅。
    let sentinel = "SENTINEL-COOKIE-7f3a9-do-not-leak";
    let r = dispatch::dispatch(
        &core,
        req(
            cmd::CREDENTIAL_IMPORT,
            serde_json::json!({"platform": "fake", "subject": null, "secret": sentinel}),
            None,
        ),
    )
    .await;
    assert!(r.ok, "credential import failed: {r:?}");
    let credential_id = r.result.as_ref().unwrap()["credential_id"]
        .as_str()
        .unwrap()
        .to_owned();

    core.start_scheduler();

    // 创作者（幂等键重发同结果，C10）。
    let mk = req(
        cmd::CREATOR_CREATE,
        serde_json::json!({"display_name": "合成创作者", "aliases": [], "tags": [], "notes": ""}),
        Some("e2e-mk-creator".into()),
    );
    let r1 = dispatch::dispatch(&core, mk.clone()).await;
    assert!(r1.ok, "creator create failed: {r1:?}");
    let r1b = dispatch::dispatch(&core, mk).await;
    assert_eq!(
        r1.result.as_ref().unwrap()["creator_id"],
        r1b.result.unwrap()["creator_id"],
        "幂等键重发必须返回同一创作者"
    );
    let creator_id = r1.result.unwrap()["creator_id"]
        .as_str()
        .expect("creator_id")
        .to_string();

    let r = dispatch::dispatch(
        &core,
        req(
            cmd::ACCOUNT_BIND,
            serde_json::json!({
                "creator_id": creator_id,
                "namespace": "fake",
                "native_account_key": "up-1",
            }),
            None,
        ),
    )
    .await;
    assert!(r.ok, "account bind failed: {r:?}");
    let account_id = r.result.unwrap()["account_id"]
        .as_str()
        .expect("account_id")
        .to_string();
    let again=dispatch::dispatch(&core,req(cmd::ACCOUNT_BIND,serde_json::json!({"creator_id":creator_id,"namespace":"fake","native_account_key":"up-1"}),None)).await;
    assert!(again.ok);
    assert_eq!(
        again.result.unwrap()["account_id"],
        account_id,
        "existing binding must return persisted ID"
    );

    let r = dispatch::dispatch(
        &core,
        req(
            cmd::SUBSCRIPTION_CREATE,
            serde_json::json!({
                "account_id": account_id,
                "content_type": "video",
                "credential_id": credential_id,
                "range": {"kind": "recent_days", "days": 365},
            }),
            None,
        ),
    )
    .await;
    assert!(r.ok, "subscription create failed: {r:?}");

    // 等待闭环：扫描两页（v-1、v-2）→ observe → media 提交完成。
    let deadline = std::time::Instant::now() + Duration::from_secs(180);
    let mut saw_partial = false;
    loop {
        let states = dispatch::dispatch(
            &core,
            req(cmd::ARCHIVE_TIMELINE, serde_json::json!({"limit":50}), None),
        )
        .await;
        if !states.ok {
            panic!(
                "feed query failed: {:?}",
                trace_core::feed::query(
                    &core.db,
                    serde_json::from_value(serde_json::json!({"limit":50})).unwrap()
                )
                .await
            );
        }
        if states.ok {
            saw_partial |= states.result.unwrap()["items"]
                .as_array()
                .unwrap()
                .iter()
                .any(|e| e["save_progress"] == "partial");
        }
        let (variants, entries) = tokio::join!(
            count(&core, "SELECT count(*) FROM media_variants"),
            count(&core, "SELECT count(*) FROM entries"),
        );
        if variants >= 5 && entries >= 2 && count(&core,"SELECT count(*) FROM jobs WHERE job_type IN ('observe','media') AND state!='succeeded'").await==0 {
            break;
        }
        assert!(
            std::time::Instant::now() < deadline,
            "pipeline did not complete: variants={variants} entries={entries}"
        );
        tokio::time::sleep(Duration::from_millis(400)).await;
    }
    assert!(
        saw_partial,
        "second resource failure must leave partial visible; attempts={}, media={:?}",
        count(&core, "SELECT count(*) FROM attempts").await,
        core.db
            .read(|c| Ok(c
                .prepare("SELECT role,required,source_attachment_id FROM media_items")?
                .query_map([], |r| Ok((
                    r.get::<_, String>(0)?,
                    r.get::<_, i64>(1)?,
                    r.get::<_, String>(2)?
                )))?
                .collect::<Result<Vec<_>, _>>()?))
            .await
    );
    assert!(
        count(&core, "SELECT count(*) FROM attempts WHERE result='failed'").await > 0,
        "transient failure must be recorded"
    );
    assert_eq!(
        count(
            &core,
            "SELECT count(*) FROM media_items WHERE role='video_segment' AND required=1"
        )
        .await,
        2
    );
    // 每个变体都有已验证 blob。
    let verified: i64 = core
        .db
        .read(|c| {
            Ok(c.query_row(
                "SELECT count(*) FROM media_variants v JOIN replicas r ON r.blob_id=v.blob_id WHERE r.state='verified'",
                [],
                |r| r.get(0),
            )?)
        })
        .await
        .unwrap_or(0);
    assert!(verified >= 2, "verified variants={verified}");

    // F-010: handles are bounded, released and never accept caller-controlled paths.
    let blob: String = core
        .db
        .read(|c| {
            Ok(
                c.query_row("SELECT blob_id FROM media_variants LIMIT 1", [], |r| {
                    r.get(0)
                })?,
            )
        })
        .await
        .unwrap();
    for bad in ["sha256:../outside", "sha256:x", "C:\\outside.exe"] {
        assert!(
            !dispatch::dispatch(
                &core,
                req(cmd::MEDIA_LEASE, serde_json::json!({"blob_id":bad}), None)
            )
            .await
            .ok
        );
        assert!(
            !dispatch::dispatch(
                &core,
                req(
                    cmd::MEDIA_OPEN_EXTERNAL,
                    serde_json::json!({"blob_id":bad}),
                    None
                )
            )
            .await
            .ok
        );
    }
    let lease = dispatch::dispatch(
        &core,
        req(cmd::MEDIA_LEASE, serde_json::json!({"blob_id":blob}), None),
    )
    .await;
    assert!(lease.ok);
    let id = lease.result.unwrap()["lease_id"]
        .as_str()
        .unwrap()
        .to_owned();
    assert!(
        !dispatch::dispatch(
            &core,
            req(
                cmd::MEDIA_READ,
                serde_json::json!({"lease_id":id,"offset":-1,"length":10}),
                None
            )
        )
        .await
        .ok
    );
    let read = dispatch::dispatch(
        &core,
        req(
            cmd::MEDIA_READ,
            serde_json::json!({"lease_id":id,"offset":0,"length":10000000}),
            None,
        ),
    )
    .await;
    assert!(
        !read.ok,
        "oversized IPC reads must be rejected before allocation"
    );
    let read = dispatch::dispatch(&core,req(cmd::MEDIA_READ,serde_json::json!({"lease_id":id,"offset":0,"length":trace_core::config::MEDIA_CHUNK_MAX}),None)).await;
    assert!(read.ok);
    use base64::Engine;
    let bytes = base64::engine::general_purpose::STANDARD
        .decode(read.result.unwrap()["data_b64"].as_str().unwrap())
        .unwrap();
    assert!(bytes.len() <= trace_core::config::MEDIA_CHUNK_MAX as usize);
    assert!(
        dispatch::dispatch(
            &core,
            req(cmd::MEDIA_RELEASE, serde_json::json!({"lease_id":id}), None)
        )
        .await
        .ok
    );
    assert!(
        !dispatch::dispatch(
            &core,
            req(
                cmd::MEDIA_READ,
                serde_json::json!({"lease_id":id,"offset":0,"length":10}),
                None
            )
        )
        .await
        .ok
    );

    // 中文搜索（C17）：3 字以上走 FTS trigram。
    let deadline = std::time::Instant::now() + Duration::from_secs(30);
    let mut hit = false;
    while std::time::Instant::now() < deadline {
        let r = dispatch::dispatch(
            &core,
            req(
                cmd::SEARCH_QUERY,
                serde_json::json!({"query": "中文搜索测试", "limit": 20}),
                None,
            ),
        )
        .await;
        assert!(r.ok, "search failed: {r:?}");
        let items = r.result.unwrap()["items"]
            .as_array()
            .cloned()
            .unwrap_or_default();
        if items.iter().any(|i| {
            i["title"]
                .as_str()
                .map(|t| t.contains("合成视频一"))
                .unwrap_or(false)
        }) {
            hit = true;
            break;
        }
        tokio::time::sleep(Duration::from_millis(500)).await;
    }
    assert!(hit, "中文搜索未命中合成标题（索引滞后或 FTS 失效）");

    // Default UI backup returns a durable job, lists only completed manifests, and errors stay visible.
    let backup_job =
        dispatch::dispatch(&core, req(cmd::BACKUP_START, serde_json::json!({}), None)).await;
    assert!(backup_job.ok);
    let backup_id = backup_job.result.unwrap()["job_id"]
        .as_str()
        .unwrap()
        .to_owned();
    let blocked_export = root.path().join("not-a-directory");
    std::fs::write(&blocked_export, b"preserve").unwrap();
    let failed_export = dispatch::dispatch(
        &core,
        req(
            cmd::EXPORT_START,
            serde_json::json!({"target_dir":blocked_export,"include_media":true}),
            None,
        ),
    )
    .await;
    assert!(failed_export.ok);
    let export_id = failed_export.result.unwrap()["job_id"]
        .as_str()
        .unwrap()
        .to_owned();
    let deadline = std::time::Instant::now() + Duration::from_secs(15);
    loop {
        let b = dispatch::dispatch(
            &core,
            req(cmd::JOB_GET, serde_json::json!({"job_id":backup_id}), None),
        )
        .await;
        let e = dispatch::dispatch(
            &core,
            req(cmd::JOB_GET, serde_json::json!({"job_id":export_id}), None),
        )
        .await;
        if b.result.as_ref().is_some_and(|r| r["state"] == "succeeded")
            && e.result
                .as_ref()
                .is_some_and(|r| r["state"] == "failed" && !r["last_error"].is_null())
        {
            break;
        }
        assert!(
            std::time::Instant::now() < deadline,
            "backup/export terminal state missing: {b:?} {e:?}"
        );
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    let backups =
        dispatch::dispatch(&core, req(cmd::BACKUP_LIST, serde_json::json!({}), None)).await;
    assert!(backups.ok);
    let list = backups.result.unwrap();
    assert_eq!(list["items"].as_array().unwrap().len(), 1);
    assert!(
        std::path::Path::new(list["items"][0]["backup_root"].as_str().unwrap())
            .join("manifest.json")
            .exists()
    );
    assert_eq!(std::fs::read(blocked_export).unwrap(), b"preserve");

    // 备份（C20）：直接调用（确定性）。
    let backup_root = root.path().join("backups");
    let backup_info = trace_core::storage_ops::backup(&core, &backup_root, true)
        .await
        .expect("backup");
    let backup_dir = backup_root.join(backup_info["backup_id"].as_str().unwrap_or(""));
    assert!(backup_dir.join("manifest.json").exists());
    assert!(backup_dir.join("catalog.sqlite3").exists());
    let restored_info =
        trace_core::storage_ops::restore(&core, &backup_dir, &root.path().join("restored"))
            .await
            .expect("restore");
    assert_eq!(restored_info["credentials"], "require-relogin");

    let mut restored_profile =
        trace_core::config::load_profile(&root.path().join("restored/profile.json")).unwrap();
    restored_profile.vault_dir = Some(root.path().join("restored-vault"));
    let restored = Core::open(restored_profile).await.unwrap();
    restored.recover().await.unwrap();
    assert!(count(&restored, "SELECT count(*) FROM entries").await >= 2);
    assert_eq!(
        count(&restored, "SELECT count(*) FROM credentials_state").await,
        0
    );
    let blobs: Vec<String> = restored
        .db
        .read(|c| {
            Ok(c.prepare("SELECT blob_id FROM blobs")?
                .query_map([], |r| r.get(0))?
                .collect::<Result<_, _>>()?)
        })
        .await
        .unwrap();
    for blob in blobs {
        let sha = trace_core::paths::blob_sha(&blob).unwrap();
        assert_eq!(
            trace_core::objects::hash_file(&restored.objects.object_path(sha).unwrap()).unwrap(),
            sha
        );
        assert!(!restored.objects.read_range(sha, 0, 32).unwrap().is_empty());
    }
    let page = dispatch::dispatch(
        &restored,
        req(cmd::ARCHIVE_TIMELINE, serde_json::json!({"limit":50}), None),
    )
    .await;
    assert!(page.ok, "{page:?}");
    for entry in page.result.unwrap()["items"].as_array().unwrap() {
        assert_eq!(entry["save_progress"], "complete");
        assert_eq!(entry["quality_state"], "unknown");
    }
    assert!(
        source
            .credential_requests
            .load(std::sync::atomic::Ordering::SeqCst)
            > 0,
        "sentinel must travel through a real proxy request"
    );
    restored.shutdown().await;
    // 哨兵扫描（C13）：全部落盘文件（DB/WAL/vault/备份/日志）无明文。
    core.shutdown().await;
    let mut files = Vec::new();
    collect_files(root.path(), &mut files);
    assert!(files.len() > 5, "异常：几乎没有产物可扫描");
    let mut hits = Vec::new();
    for f in &files {
        if let Ok(bytes) = std::fs::read(f)
            && bytes
                .windows(sentinel.len())
                .any(|w| w == sentinel.as_bytes())
        {
            hits.push(f.clone());
        }
    }
    assert!(
        hits.is_empty(),
        "哨兵泄露于：{hits:?}（C13：vault 加密、DB/备份/日志不含凭证明文）"
    );

    // vault 文件本身存在（DPAPI 加密，不含明文）。
    let mut vf = Vec::new();
    collect_files(&root.path().join("vault"), &mut vf);
    assert!(!vf.is_empty(), "凭证应保存在 vault 目录");
}
