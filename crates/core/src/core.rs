//! Core 结构：进程内服务总线 + 调度器（docs/design.md §9）。
//!
//! - 打开：迁移 → 恢复扫描（旧租约、物化矩阵、副本验证）→ 启动调度。
//! - 调度：持久任务按到期时间认领；预算（元数据节流、媒体全局/单账号
//!   并发）按 m1a-runtime-contracts §6 执行；暂停期间不认领新任务。
//! - 订阅扫描使用单一活动 Job（job_key 唯一），页游标持久化于
//!   scan_checkpoints 与任务载荷（C01/C31）。

use crate::config::{METADATA_MIN_INTERVAL_MS, Profile};
use crate::db::{DbActor, DbHandle};
use crate::error::CoreError;
use crate::events::EventHub;
use crate::jobs::{self, ClaimedJob};
use crate::media::MediaBackend;
use crate::objects::{CommitDisposition, CommitInfo, ObjectStore, ReceiveParams};
use crate::pluginhost::{PluginHost, default_adapter_specs};
use crate::search;
use crate::transport::ConstrainedClient;
use crate::vault::Vault;
use std::collections::HashMap;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};
use tokio_util::sync::CancellationToken;
use trace_contracts::plugin::{
    EnumerateParams, EnumeratedPage, ObservedDetail, ResolveMediaParams,
};
use trace_domain::ids::{InstanceId, NodeId, OperationId};

/// 扫描页入库行（process_scan_page 的批量载荷）。
type ScanEntryRow = (
    String,
    String,
    String,
    Option<String>,
    String,
    bool,
    Option<String>,
    Value,
);
use serde_json::Value;
use trace_domain::revision::{
    Completeness, ContentObservation, ResourceIdentity, decide_revision, semantic_digest,
};
use trace_domain::search::normalize_v1;
use trace_domain::time::{TimePrecision, TraceTime};

#[derive(Clone)]
pub struct MediaLease {
    pub blob_sha: String,
    pub size: i64,
    pub mime: Option<String>,
    pub expires_at: Instant,
}

pub struct Core {
    pub profile: Profile,
    pub db: DbHandle,
    pub objects: ObjectStore,
    pub vault: Vault,
    pub hub: Arc<EventHub>,
    pub host: PluginHost,
    pub http: Arc<ConstrainedClient>,
    pub media_backend: MediaBackend,
    pub instance_id: InstanceId,
    pub node_id: NodeId,
    pub started_at: TraceTime,
    pub paused: AtomicBool,
    pub cancel: CancellationToken,
    pub media_global: Arc<tokio::sync::Semaphore>,
    account_media: Mutex<HashMap<String, Arc<tokio::sync::Semaphore>>>,
    pub leases: Mutex<HashMap<String, MediaLease>>,
    pub cookie_cache: Arc<Mutex<HashMap<String, String>>>,
    metadata_last_request: Mutex<HashMap<String, Instant>>,
    _db_actor: Arc<DbActor>,
}

impl Core {
    pub async fn open(profile: Profile) -> Result<Arc<Self>, CoreError> {
        let archive = profile.archive()?;
        let db_path = profile.db_path();
        if let Some(parent) = db_path.parent() {
            std::fs::create_dir_all(parent)?;
        }
        std::fs::create_dir_all(&profile.archive_root)?;
        let conn = rusqlite::Connection::open(&db_path)?;
        let (db, actor) = DbActor::start(conn, 512)?;
        let vault_root = profile
            .vault_root()
            .ok_or_else(|| CoreError::Internal("no vault location".into()))?;
        let vault = Vault::new(vault_root)?;
        let http = Arc::new(ConstrainedClient::build()?);
        let cookie_cache: Arc<Mutex<HashMap<String, String>>> =
            Arc::new(Mutex::new(HashMap::new()));
        // 凭证查询闭包：按平台从短期内存缓存读取（vault 内容只在导入/验证时解密入缓存）。
        let lookup_cache = cookie_cache.clone();
        let credential_lookup: crate::pluginhost::CredentialLookup =
            Arc::new(move |platform| lookup_cache.lock().unwrap().get(platform).cloned());
        let adapter_dir = profile.adapter_dir.clone().or_else(|| {
            std::env::current_exe()
                .ok()
                .map(|e| e.parent().unwrap().to_path_buf())
        });
        let mut prof = profile.clone();
        prof.adapter_dir = adapter_dir;
        let host = PluginHost::new(
            prof.clone(),
            http.clone(),
            default_adapter_specs(&prof),
            credential_lookup,
        );
        let media_backend = MediaBackend::detect(&prof).await;
        let core = Arc::new(Self {
            profile: prof,
            db,
            objects: ObjectStore::new(profile),
            vault,
            hub: Arc::new(EventHub::new()),
            host,
            http,
            media_backend,
            instance_id: InstanceId::generate(),
            node_id: NodeId::new(format!("node-{}", archive.as_str())),
            started_at: TraceTime::now(TimePrecision::Discovery),
            paused: AtomicBool::new(false),
            cancel: CancellationToken::new(),
            media_global: Arc::new(tokio::sync::Semaphore::new(
                crate::config::MEDIA_CONCURRENCY_GLOBAL,
            )),
            account_media: Mutex::new(HashMap::new()),
            leases: Mutex::new(HashMap::new()),
            cookie_cache,
            metadata_last_request: Mutex::new(HashMap::new()),
            _db_actor: Arc::new(actor),
        });
        let archive_id = archive.as_str().to_string();
        let node = core.node_id.as_str().to_string();
        let instance = core.instance_id.as_str().to_string();
        core.db
            .write(move |tx| {
                tx.execute(
                    "INSERT INTO meta(key,value) VALUES('archive_id',?1)
                     ON CONFLICT(key) DO UPDATE SET value=excluded.value",
                    [archive_id],
                )?;
                tx.execute(
                    "INSERT INTO meta(key,value) VALUES('node_id',?1)
                     ON CONFLICT(key) DO UPDATE SET value=excluded.value",
                    [node],
                )?;
                tx.execute(
                    "INSERT INTO meta(key,value) VALUES('last_instance',?1)
                     ON CONFLICT(key) DO UPDATE SET value=excluded.value",
                    [instance],
                )?;
                Ok(())
            })
            .await?;
        Ok(core)
    }

    /// 启动恢复：旧实例租约回收 + 物化恢复矩阵 + 副本验证（有界）。
    pub async fn recover(&self) -> Result<serde_json::Value, CoreError> {
        let stale = jobs::recover_stale_leases(&self.db, self.instance_id.as_str()).await?;
        let mat = self.objects.recover_materializations(&self.db).await?;
        let broken = self.objects.verify_replicas(&self.db, 64).await?;
        Ok(serde_json::json!({
            "stale_leases_requeued": stale,
            "materializations": mat,
            "broken_replicas": broken,
        }))
    }

    /// 启动调度循环（daemon 中 spawn）。
    pub fn start_scheduler(self: &Arc<Self>) {
        let core = self.clone();
        tokio::spawn(async move {
            let mut tick = tokio::time::interval(Duration::from_millis(500));
            loop {
                tokio::select! {
                    _ = tick.tick() => {
                        if core.paused.load(Ordering::SeqCst) {
                            continue;
                        }
                        if let Ok(claimed) = jobs::claim_due(&core.db, core.instance_id.as_str(), 4).await {
                            for job in claimed {
                                let core2 = core.clone();
                                tokio::spawn(async move {
                                    execute(core2, job).await;
                                });
                            }
                        }
                        // 派生：搜索索引随 tick 有界推进。
                        let _ = search::index_pending(&core.db, 100).await;
                    }
                    _ = core.cancel.cancelled() => break,
                }
            }
        });
    }

    /// 元数据节流：每命名空间最小间隔 + 抖动（契约 §6）。
    pub async fn metadata_pacing(&self, namespace: &str) {
        loop {
            let decision = {
                let mut map = self.metadata_last_request.lock().unwrap();
                let now = Instant::now();
                let elapsed = map
                    .get(namespace)
                    .map(|t| t.elapsed())
                    .unwrap_or(Duration::from_secs(u64::MAX / 2));
                let min = Duration::from_millis(METADATA_MIN_INTERVAL_MS + (nanos_jitter() % 500));
                if elapsed >= min {
                    map.insert(namespace.to_string(), now);
                    None
                } else {
                    Some(min - elapsed)
                }
            };
            match decision {
                Some(d) => tokio::time::sleep(d).await,
                None => return,
            }
        }
    }

    async fn account_semaphore(&self, account_id: &str) -> Arc<tokio::sync::Semaphore> {
        let mut map = self.account_media.lock().unwrap();
        map.entry(account_id.to_string())
            .or_insert_with(|| {
                Arc::new(tokio::sync::Semaphore::new(
                    crate::config::MEDIA_CONCURRENCY_PER_ACCOUNT,
                ))
            })
            .clone()
    }

    /// 凭证导入/验证后填充短期 Cookie 缓存（内存，仅本进程；撤销时清除）。
    pub fn cache_cookie(&self, platform: &str, cookie: String) {
        self.cookie_cache
            .lock()
            .unwrap()
            .insert(platform.to_string(), cookie);
    }

    pub fn clear_cookie(&self, platform: &str) {
        self.cookie_cache.lock().unwrap().remove(platform);
    }

    /// 优雅停机：停止调度 → 关闭适配器；DB 随 Core 关闭。
    pub async fn shutdown(&self) {
        self.cancel.cancel();
        self.host.shutdown_all().await;
    }
}

fn nanos_jitter() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.subsec_nanos() as u64)
        .unwrap_or(0)
}

/// 执行单个任务；失败按分类落状态（C26/C29 语义）。
async fn execute(core: Arc<Core>, job: ClaimedJob) {
    if job.mark_running(&core.db).await.is_err() {
        return;
    }
    let work = async {
        match job.job_type.as_str() {
            "scan" => run_scan(core.as_ref(), &job).await,
            "observe" => run_observe(core.as_ref(), &job).await,
            "media" => run_media(core.as_ref(), &job).await,
            "backup" => {
                let a: trace_contracts::control::BackupStartArgs =
                    serde_json::from_value(job.payload.clone())?;
                crate::storage_ops::backup(
                    &core,
                    std::path::Path::new(&a.target_root),
                    a.include_media,
                )
                .await
                .map(|_| ())
            }
            "export" => {
                let a: trace_contracts::control::ExportStartArgs =
                    serde_json::from_value(job.payload.clone())?;
                crate::storage_ops::export(
                    &core,
                    std::path::Path::new(&a.target_dir),
                    a.include_media,
                    a.creator_id.as_deref(),
                )
                .await
                .map(|_| ())
            }
            _ => Err(CoreError::InvalidInput("unknown job type".into())),
        }
    };
    tokio::pin!(work);
    let mut monitor = tokio::time::interval(Duration::from_millis(200));
    let result = loop {
        tokio::select! {
            result = &mut work => break result,
            _ = core.cancel.cancelled() => { let _ = job.requeue(&core.db).await; return; },
            _ = monitor.tick() => {
                if core.paused.load(Ordering::SeqCst) {
                    let _ = job.requeue(&core.db).await;
                    return;
                }
                if job.heartbeat(&core.db).await.is_err() { return; }
            }
        }
    };
    if core.cancel.is_cancelled() || core.paused.load(Ordering::SeqCst) {
        let _ = job.requeue(&core.db).await;
        return;
    }
    match result {
        Ok(()) => {
            let _ = job.succeed(&core.db).await;
        }
        Err(CoreError::Paused(_)) => {
            // Administrative pauses are not failed attempts. Claiming also checks subscription state.
            let _ = job.requeue(&core.db).await;
        }
        Err(e) => {
            // Persist a class, never an upstream error body (credentials/URLs can occur anywhere).
            let class = e.failure_class();
            let message = format!("{:?}", class);
            let _ = job.fail(&core.db, &class, &message, None).await;
            core.hub
                .publish_progress(&job.job_id, 0.0, Some(message), &job.job_id);
        }
    }
}

/// 订阅扫描执行器（C01/C25/C30/C31 的核心路径）。
///
/// 单一活动 Job：预算内逐页处理；预算耗尽保持 queued 续跑；页边界
/// 结束时按 check_interval 安排下一轮（不产生重复活动检查）。
async fn run_scan(core: &Core, job: &ClaimedJob) -> Result<(), CoreError> {
    let subscription_id = job
        .payload
        .get("subscription_id")
        .and_then(|v| v.as_str())
        .ok_or_else(|| CoreError::InvalidInput("payload".into()))?
        .to_string();
    let mode = job
        .payload
        .get("mode")
        .and_then(|v| v.as_str())
        .unwrap_or("incremental")
        .to_string();
    let max_pages = job
        .payload
        .get("max_pages")
        .and_then(|v| v.as_u64())
        .unwrap_or(crate::config::DEFAULT_MAX_SCAN_PAGES as u64) as u32;

    let (account_key, namespace, paused_state): (String, String, String) = {
        let sub = subscription_id.clone();
        core.db
            .read(move |c| {
                Ok(c.query_row(
                    "SELECT s.account_id, a.namespace, s.state
                     FROM subscriptions s JOIN source_accounts a ON a.account_id=s.account_id
                     WHERE s.subscription_id=?1",
                    [&sub],
                    |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)),
                )?)
            })
            .await?
    };
    if paused_state != "enabled" {
        return Err(CoreError::Paused("subscription disabled".into()));
    }
    let platform = namespace.split(':').next().unwrap_or("").to_string();

    // 插件停用/缺失 → waiting_plugin（C19）。
    if core.host.spec_for_platform(&platform).is_none() {
        let job_id = job.job_id.clone();
        let guard = job.clone();
        core.db
            .write(move |tx| {
                guard.check(tx)?;
                tx.execute(
                    "UPDATE jobs SET state='waiting_plugin', updated_at=?2 WHERE job_id=?1",
                    rusqlite::params![job_id, jobs::now()],
                )?;
                Ok(())
            })
            .await?;
        return Ok(());
    }
    core.metadata_pacing(&namespace).await;

    let mut cursor: Option<String> = job
        .payload
        .get("cursor")
        .and_then(|v| v.as_str())
        .map(|s| s.to_string());
    if cursor.is_none() {
        cursor = {
            let sub = subscription_id.clone();
            let mode2 = mode.clone();
            core.db
                .read(move |c| {
                    Ok(c.query_row(
                        "SELECT cursor FROM scan_checkpoints WHERE subscription_id=?1 AND scan_type=?2",
                        rusqlite::params![sub, mode2],
                        |r| r.get(0),
                    )
                    .ok())
                })
                .await?
        };
    }

    let recent_days = job.payload.get("recent_days").and_then(|v| v.as_i64());
    let mut processed_pages = 0u32;
    let mut has_more = false;
    let mut new_entries = 0i64;

    for _ in 0..max_pages {
        let params = EnumerateParams {
            native_account_key: account_key.clone(),
            mode: mode.clone(),
            cursor: cursor.clone(),
            recent_days,
            max_pages: 1,
        };
        let page_v = core
            .host
            .call(
                &platform,
                trace_contracts::plugin::methods::ENUMERATE,
                serde_json::to_value(&params).unwrap_or_default(),
                Some(30_000),
            )
            .await?;
        let page: EnumeratedPage = serde_json::from_value(page_v)
            .map_err(|e| CoreError::InvalidInput(format!("bad page: {e}")))?;
        let (added, _) = process_scan_page(
            core,
            job,
            &namespace,
            &subscription_id,
            &mode,
            page.items.clone(),
            page.next_cursor.clone(),
            &page.coverage,
        )
        .await?;
        new_entries += added;
        cursor = page.next_cursor.clone();
        processed_pages += 1;
        has_more = page.has_more;
        if !page.has_more {
            break;
        }
    }

    // 单一活动 Job：继续或安排下一轮周期。
    let job_id = job.job_id.clone();
    let sub = subscription_id.clone();
    let cursor2 = cursor.clone();
    let interval: i64 = {
        let sub2 = subscription_id.clone();
        core.db
            .read(move |c| {
                Ok(c.query_row(
                    "SELECT check_interval_seconds FROM subscriptions WHERE subscription_id=?1",
                    [&sub2],
                    |r| r.get::<_, i64>(0),
                )
                .unwrap_or(crate::config::DEFAULT_CHECK_INTERVAL_SECONDS))
            })
            .await?
    };
    let next_run_at = if has_more {
        jobs::now()
    } else {
        plus_seconds(&jobs::now(), interval)
    };
    let guard = job.clone();
    core.db
        .write(move |tx| {
            guard.check(tx)?;
            crate::objects::outbox_emit(
                tx,
                "scan.progress",
                &sub,
                &serde_json::json!({"pages": processed_pages, "new_entries": new_entries}),
            )?;
            tx.execute(
                "UPDATE jobs SET state='queued', next_run_at=?2, updated_at=?2,
                 payload=json_set(payload, '$.cursor', ?3)
                 WHERE job_id=?1",
                rusqlite::params![job_id, next_run_at, cursor2],
            )?;
            Ok(())
        })
        .await?;
    Ok(())
}

fn plus_seconds(rfc3339: &str, secs: i64) -> String {
    use time::format_description::well_known::Rfc3339;
    if let Ok(t) = time::OffsetDateTime::parse(rfc3339, &Rfc3339) {
        let t2 = t + time::Duration::seconds(secs);
        t2.format(&Rfc3339).unwrap_or_else(|_| rfc3339.to_string())
    } else {
        rfc3339.to_string()
    }
}

/// 扫描页事务（§9.1）：upsert Entry/观察 + 游标 + 覆盖同事务提交（C01）。
#[allow(clippy::too_many_arguments)]
async fn process_scan_page(
    core: &Core,
    job: &ClaimedJob,
    namespace: &str,
    subscription_id: &str,
    mode: &str,
    page: Vec<trace_contracts::plugin::EnumeratedEntry>,
    cursor: Option<String>,
    coverage: &serde_json::Value,
) -> Result<(i64, Option<String>), CoreError> {
    let ns = namespace.to_string();
    let sub = subscription_id.to_string();
    let mode_owned = mode.to_string();
    let cursor_owned = cursor;
    let cov = coverage.clone();
    let now = jobs::now();
    let entries_payload: Vec<ScanEntryRow> = page
        .into_iter()
        .map(|i| {
            (
                i.native_entry_key,
                i.native_kind,
                i.title,
                i.published_at,
                i.published_precision,
                i.pinned,
                i.summary_body,
                i.stats,
            )
        })
        .collect();
    let guard = job.clone();
    core.db
        .write(move |tx| {
            guard.check(tx)?;
            let added = 0i64;
            let mut last_pub: Option<String> = None;
            for (key, kind, title, published_at, precision, _pinned, summary_body, stats) in entries_payload {
                if last_pub.is_none() {
                    last_pub = published_at.clone();
                }
                let inserted_id = format!("ent-{}", uuid::Uuid::new_v4());
                let actual_id: String = tx
                    .query_row(
                        "SELECT entry_id FROM entries WHERE namespace=?1 AND native_kind=?2 AND native_entry_key=?3",
                        rusqlite::params![ns, kind, key],
                        |r| r.get::<_, String>(0),
                    )
                    .or_else(|_| -> Result<String, CoreError> {
                        tx.execute(
                            "INSERT INTO entries (entry_id, namespace, native_kind, native_entry_key, account_id,
                             first_seen_at, published_at, published_precision, lifecycle)
                             SELECT ?1, ?2, ?3, ?4, s.account_id, ?5, ?6, ?7, 'active'
                             FROM source_accounts s
                             JOIN subscriptions sub ON sub.account_id = s.account_id
                             WHERE sub.subscription_id = ?8 LIMIT 1",
                            rusqlite::params![inserted_id, ns, kind, key, now, published_at, precision, sub],
                        )?;
                        Ok(inserted_id)
                    })?;
                // 短摘要观察：完整详情由 observe 任务补全（短摘要不覆盖完整 Revision）。
                if let Some(body) = summary_body {
                    let digest = semantic_digest(&title, &normalize_v1(&body), &[]);
                    let rev_id = format!("rev-{}", uuid::Uuid::new_v4());
                    let existing: Option<String> = tx
                        .query_row(
                            "SELECT revision_id FROM entry_revisions WHERE entry_id=?1 AND semantic_digest=?2",
                            rusqlite::params![actual_id, digest],
                            |r| r.get(0),
                        )
                        .ok();
                    let rid = match existing {
                        Some(r) => r,
                        None => {
                            tx.execute(
                                "INSERT INTO entry_revisions (revision_id, entry_id, semantic_digest, title, normalized_body, completeness, observed_at)
                                 VALUES (?1,?2,?3,?4,?5,'summary',?6)",
                                rusqlite::params![rev_id, actual_id, digest, title, normalize_v1(&body), now],
                            )?;
                            rev_id
                        }
                    };
                    tx.execute(
                        "INSERT INTO source_observations (observation_id, entry_id, revision_id, access_state, observed_at, observation_seq, evidence)
                         VALUES (?1,?2,?3,'visible',?4,
                         COALESCE((SELECT MAX(observation_seq)+1 FROM source_observations WHERE entry_id=?2),1), ?5)",
                        rusqlite::params![format!("obs-{}", uuid::Uuid::new_v4()), actual_id, rid, now, stats.to_string()],
                    )?;
                } else {
                    tx.execute(
                        "INSERT INTO source_observations (observation_id, entry_id, revision_id, access_state, observed_at, observation_seq, evidence)
                         VALUES (?1,?2,NULL,'visible',?3,
                         COALESCE((SELECT MAX(observation_seq)+1 FROM source_observations WHERE entry_id=?2),1), '{}')",
                        rusqlite::params![format!("obs-{}", uuid::Uuid::new_v4()), actual_id, now],
                    )?;
                }
                // 详情任务（唯一 job_key 合并，重启不重复，INV-09）。
                let okey = format!("observe:{ns}:{key}");
                tx.execute(
                    "INSERT OR IGNORE INTO jobs (job_id, job_key, job_type, subject_id, payload, priority, state, created_at, updated_at)
                     VALUES (?1,?2,'observe',?3,?4,1,'queued',?5,?5)",
                    rusqlite::params![
                        format!("job-{}", uuid::Uuid::new_v4()),
                        okey,
                        actual_id,
                        serde_json::json!({"entry_id": actual_id}).to_string(),
                        now
                    ],
                )?;
            }
            tx.execute(
                "INSERT INTO scan_checkpoints (subscription_id, scan_type, cursor, coverage, generation, updated_at)
                 VALUES (?1,?2,?3,?4,1,?5)
                 ON CONFLICT(subscription_id, scan_type) DO UPDATE SET cursor=excluded.cursor, coverage=excluded.coverage, updated_at=excluded.updated_at",
                rusqlite::params![sub, mode_owned, cursor_owned, cov.to_string(), now],
            )?;
            Ok((added, last_pub))
        })
        .await
}

/// 详情观察执行器（C02：身份稳定、版本追加、摘要不覆盖完整）。
async fn run_observe(core: &Core, job: &ClaimedJob) -> Result<(), CoreError> {
    let entry_id = job
        .payload
        .get("entry_id")
        .and_then(|v| v.as_str())
        .ok_or_else(|| CoreError::InvalidInput("payload".into()))?
        .to_string();
    let (namespace, native_key, account_id): (String, String, String) = {
        let e = entry_id.clone();
        core.db
            .read(move |c| {
                Ok(c.query_row(
                    "SELECT namespace, native_entry_key, account_id FROM entries WHERE entry_id=?1",
                    [&e],
                    |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)),
                )?)
            })
            .await?
    };
    let platform = namespace.split(':').next().unwrap_or("").to_string();
    core.metadata_pacing(&namespace).await;
    let detail_v = core
        .host
        .call(
            &platform,
            trace_contracts::plugin::methods::OBSERVE,
            serde_json::json!({"native_entry_key": native_key}),
            Some(30_000),
        )
        .await?;
    let detail: ObservedDetail = serde_json::from_value(detail_v)
        .map_err(|e| CoreError::InvalidInput(format!("bad detail: {e}")))?;

    let existing: Vec<(String, String, String)> = {
        let e = entry_id.clone();
        core.db
            .read(move |c| {
                let mut stmt = c.prepare(
                    "SELECT revision_id, completeness, semantic_digest FROM entry_revisions WHERE entry_id=?1",
                )?;
                let rows = stmt
                    .query_map([&e], |r| {
                        Ok((r.get::<_, String>(0)?, r.get::<_, String>(1)?, r.get::<_, String>(2)?))
                    })?
                    .collect::<Result<Vec<_>, _>>()?;
                Ok(rows)
            })
            .await?
    };
    let resources: Vec<ResourceIdentity> = detail
        .resources
        .iter()
        .map(|r| ResourceIdentity {
            role: r.role.clone(),
            source_attachment_id: r.source_attachment_id.clone(),
            position: r.position,
        })
        .collect();
    let incoming = ContentObservation {
        title: detail.title.clone(),
        normalized_body: normalize_v1(&detail.normalized_body),
        completeness: if detail.completeness == "full" {
            Completeness::Full
        } else {
            Completeness::Summary
        },
        resources,
        source_version_evidence: None,
    };
    let existing_typed: Vec<(String, Completeness, String)> = existing
        .iter()
        .map(|(id, c, d)| {
            (
                id.clone(),
                if c == "full" {
                    Completeness::Full
                } else {
                    Completeness::Summary
                },
                d.clone(),
            )
        })
        .collect();
    let obs_id = format!("obs-{}", uuid::Uuid::new_v4());
    match decide_revision(&existing_typed, &incoming) {
        trace_domain::revision::RevisionDecision::Reuse { revision_id } => {
            let e = entry_id.clone();
            let r = revision_id;
            let access = detail.access_state.clone();
            let guard = job.clone();
            core.db
                .write(move |tx| {
                    guard.check(tx)?;
                    tx.execute(
                        "INSERT INTO source_observations (observation_id, entry_id, revision_id, access_state, observed_at, observation_seq, evidence)
                         VALUES (?1,?2,?3,?4,?5,COALESCE((SELECT MAX(observation_seq)+1 FROM source_observations WHERE entry_id=?2),1),'{}')",
                        rusqlite::params![obs_id, e, r, access, jobs::now()],
                    )?;
                    Ok(())
                })
                .await?;
        }
        trace_domain::revision::RevisionDecision::KeepExistingIgnoreSummary { .. } => {
            let e = entry_id.clone();
            let access = detail.access_state.clone();
            let guard = job.clone();
            core.db
                .write(move |tx| {
                    guard.check(tx)?;
                    tx.execute(
                        "INSERT INTO source_observations (observation_id, entry_id, revision_id, access_state, observed_at, observation_seq, evidence)
                         VALUES (?1,?2,NULL,?3,?4,COALESCE((SELECT MAX(observation_seq)+1 FROM source_observations WHERE entry_id=?2),1),'{}')",
                        rusqlite::params![obs_id, e, access, jobs::now()],
                    )?;
                    Ok(())
                })
                .await?;
        }
        trace_domain::revision::RevisionDecision::CreateNew => {
            let revision_id = format!("rev-{}", uuid::Uuid::new_v4());
            let digest = semantic_digest(
                &incoming.title,
                &incoming.normalized_body,
                &incoming.resources,
            );
            let e = entry_id.clone();
            let detail2 = detail.clone();
            let rev2 = revision_id.clone();
            let now = jobs::now();
            let guard = job.clone();
            core.db
                .write(move |tx| {
                    guard.check(tx)?;
                    tx.execute(
                        "INSERT INTO entry_revisions (revision_id, entry_id, semantic_digest, title, normalized_body, source_version_evidence, completeness, observed_at)
                         VALUES (?1,?2,?3,?4,?5,?6,'full',?7)",
                        rusqlite::params![rev2, e, digest, detail2.title, normalize_v1(&detail2.normalized_body), detail2.source_version_evidence.to_string(), now],
                    )?;
                    for r in &detail2.resources {
                        tx.execute(
                            "INSERT OR IGNORE INTO media_items (media_id, revision_id, role, position, source_attachment_id, group_key, title, required)
                             VALUES (?1,?2,?3,?4,?5,?6,?7,?8)",
                            rusqlite::params![
                                format!("med-{}", uuid::Uuid::new_v4()),
                                rev2,
                                r.role,
                                r.position,
                                r.source_attachment_id,
                                r.group_key,
                                r.title,
                                r.required as i64
                            ],
                        )?;
                    }
                    let access = detail2.access_state.clone();
                    tx.execute(
                        "INSERT INTO source_observations (observation_id, entry_id, revision_id, access_state, observed_at, observation_seq, evidence)
                         VALUES (?1,?2,?3,?4,?5,COALESCE((SELECT MAX(observation_seq)+1 FROM source_observations WHERE entry_id=?2),1),'{}')",
                        rusqlite::params![format!("obs-{}", uuid::Uuid::new_v4()), e, rev2, access, now],
                    )?;
                    crate::objects::outbox_emit(
                        tx,
                        "entry.revision.created",
                        &e,
                        &serde_json::json!({"revision_id": rev2}),
                    )?;
                    Ok(())
                })
                .await?;
            // 媒体任务按资源组入队（同一组视频+音频合并获取）。
            let mut groups: Vec<String> = Vec::new();
            for r in &detail.resources {
                if !r.required && r.role != "cover" {
                    continue;
                }
                let g = r
                    .group_key
                    .clone()
                    .unwrap_or_else(|| r.source_attachment_id.clone());
                if !groups.contains(&g) {
                    groups.push(g);
                }
            }
            for group in groups {
                let jk = format!("media:{revision_id}:{group}");
                let payload = serde_json::json!({
                    "entry_id": entry_id,
                    "revision_id": revision_id,
                    "group": group,
                    "account_id": account_id,
                });
                jobs::enqueue(&core.db, jk, "media", &entry_id, payload, 1, 1, 0).await?;
            }
        }
    }
    Ok(())
}

/// 媒体获取执行器（C27/C28 的核心路径）：组内必要资源齐才整组完成；
/// 每资源独立物化操作；无校验器时按设计从头重取。
async fn run_media(core: &Core, job: &ClaimedJob) -> Result<(), CoreError> {
    let entry_id = job
        .payload
        .get("entry_id")
        .and_then(|v| v.as_str())
        .unwrap_or_default()
        .to_string();
    let revision_id = job
        .payload
        .get("revision_id")
        .and_then(|v| v.as_str())
        .unwrap_or_default()
        .to_string();
    let group = job
        .payload
        .get("group")
        .and_then(|v| v.as_str())
        .unwrap_or_default()
        .to_string();

    let (namespace, native_key, account_id): (String, String, String) = {
        let e = entry_id.clone();
        core.db
            .read(move |c| {
                Ok(c.query_row(
                    "SELECT e.namespace, e.native_entry_key, e.account_id FROM entries e WHERE e.entry_id=?1",
                    [&e],
                    |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)),
                )?)
            })
            .await?
    };
    let platform = namespace.split(':').next().unwrap_or("").to_string();

    let mut items: Vec<(String, String, i64, String, bool)> = {
        let rev = revision_id.clone();
        let g = group.clone();
        core.db
            .read(move |c| {
                let mut stmt = c.prepare(
                    "SELECT media_id, role, position, source_attachment_id, required FROM media_items
                     WHERE revision_id=?1 AND (group_key=?2 OR (group_key IS NULL AND source_attachment_id=?2))",
                )?;
                let rows = stmt
                    .query_map(rusqlite::params![rev, g], |r| {
                        Ok((
                            r.get(0)?,
                            r.get(1)?,
                            r.get(2)?,
                            r.get(3)?,
                            r.get::<_, i64>(4)? != 0,
                        ))
                    })?
                    .collect::<Result<Vec<_>, _>>()?;
                Ok(rows)
            })
            .await?
    };
    if items.is_empty() {
        return Err(CoreError::NotFound("media group".into()));
    }

    // 预算：全局 2 + 单账号 1。
    let _global = core
        .media_global
        .acquire()
        .await
        .map_err(|_| CoreError::Internal("semaphore closed".into()))?;
    let account_sem = core.account_semaphore(&account_id).await;
    let _account = account_sem
        .acquire()
        .await
        .map_err(|_| CoreError::Internal("semaphore closed".into()))?;
    core.metadata_pacing(&namespace).await;

    let resources_dto: Vec<trace_contracts::plugin::ResourceIdentityDto> = items
        .iter()
        .filter(|(_, role, _, _, _)| role != "video_segment")
        .map(
            |(_mid, role, pos, att, req)| trace_contracts::plugin::ResourceIdentityDto {
                role: role.clone(),
                source_attachment_id: att.clone(),
                position: *pos,
                title: None,
                group_key: Some(group.clone()),
                required: *req,
            },
        )
        .collect();
    let params = ResolveMediaParams {
        native_entry_key: native_key.clone(),
        resources: resources_dto.clone(),
        quality_hint: serde_json::json!({}),
    };
    let plan_v = core
        .host
        .call(
            &platform,
            trace_contracts::plugin::methods::RESOLVE_MEDIA,
            serde_json::to_value(&params).unwrap_or_default(),
            Some(30_000),
        )
        .await?;
    let mut plan: trace_contracts::plugin::ResolveMediaResult = serde_json::from_value(plan_v)
        .map_err(|e| CoreError::InvalidInput(format!("bad plan: {e}")))?;

    // Explicit evidence may waive an independent audio resource, never a missing candidate alone.
    let silent = plan.notes["silent_or_muxed"]
        .as_array()
        .is_some_and(|a| a.iter().any(|v| v.as_str() == Some(group.as_str())));
    let audio_present = plan.candidates.iter().any(|c| c.resource.role == "audio");
    let rev = revision_id.clone();
    let g = group.clone();
    let guard = job.clone();
    core.db.write(move |tx| {
        guard.check(tx)?;
        tx.execute("UPDATE media_items SET required=?3,acquisition_evidence=?4 WHERE revision_id=?1 AND group_key=?2 AND role='audio'",rusqlite::params![rev,g,!(silent && !audio_present),serde_json::json!({"resolver":"v1","audio_present":audio_present,"silent_or_muxed":silent}).to_string()])?;
        Ok(())
    }).await?;
    // Each container segment remains an independent ordered resource. Never concatenate bytes.
    let mut expanded = Vec::new();
    let mut segment_groups = Vec::new();
    for candidate in &plan.candidates {
        if candidate.segments.is_empty() || candidate.segments.len() > 1000 {
            return Err(CoreError::InvalidInput(
                "invalid media segment count".into(),
            ));
        }
        if candidate.segments.len() == 1 {
            let mut single = candidate.clone();
            let segment = &candidate.segments[0];
            if single
                .expected_size
                .zip(segment.expected_size)
                .is_some_and(|(a, b)| a != b)
                || single
                    .expected_sha256
                    .as_ref()
                    .zip(segment.expected_sha256.as_ref())
                    .is_some_and(|(a, b)| a != b)
            {
                return Err(CoreError::InvalidInput(
                    "conflicting segment validation".into(),
                ));
            }
            single.expected_size = single.expected_size.or(segment.expected_size);
            single.expected_sha256 = single
                .expected_sha256
                .or_else(|| segment.expected_sha256.clone());
            expanded.push(single);
            continue;
        }
        let original = items
            .iter()
            .find(|(_, _, _, att, _)| att == &candidate.resource.source_attachment_id)
            .map(|i| i.0.clone())
            .ok_or_else(|| CoreError::InvalidInput("segment resource".into()))?;
        let rev = revision_id.clone();
        let g = group.clone();
        let guard = job.clone();
        let mut rows = Vec::new();
        let mut attachments = Vec::new();
        for (index, segment) in candidate.segments.iter().enumerate() {
            let mid = format!("{original}-segment-{index}");
            let mut c = candidate.clone();
            c.resource.source_attachment_id = format!(
                "{}:segment:{index}",
                candidate.resource.source_attachment_id
            );
            c.resource.role = "video_segment".into();
            c.resource.position = index as i64;
            c.resource.required = true;
            c.segments = vec![segment.clone()];
            c.expected_size = segment.expected_size;
            c.expected_sha256 = segment.expected_sha256.clone();
            attachments.push(c.resource.source_attachment_id.clone());
            rows.push((
                mid.clone(),
                c.resource.source_attachment_id.clone(),
                index as i64,
            ));
            items.push((
                mid,
                "video_segment".into(),
                index as i64,
                c.resource.source_attachment_id.clone(),
                true,
            ));
            expanded.push(c);
        }
        segment_groups.push((candidate.clone(), attachments));
        let evidence = serde_json::json!({
            "validation":"ordered-segments-v1", "expected_size":candidate.expected_size,
            "expected_sha256":candidate.expected_sha256,
            "segments":candidate.segments.iter().map(|s|serde_json::json!({"expected_size":s.expected_size,"expected_sha256":s.expected_sha256})).collect::<Vec<_>>()
        }).to_string();
        core.db.write(move |tx| {
            guard.check(tx)?;
            tx.execute("UPDATE media_items SET required=0,acquisition_evidence=?2 WHERE media_id=?1",rusqlite::params![original,evidence])?;
            for (mid,att,index) in rows { tx.execute("INSERT OR IGNORE INTO media_items(media_id,revision_id,role,position,source_attachment_id,group_key,required) VALUES(?1,?2,'video_segment',?3,?4,?5,1)",rusqlite::params![mid,rev,index,att,g])?; }
            Ok(())
        }).await?;
    }
    plan.candidates = expanded;
    let rules = crate::transport::host_rules(&core.profile);
    let mut pending_segments = Vec::new();
    let mut resolved = HashMap::new();
    for candidate in &plan.candidates {
        let media_id = items
            .iter()
            .find(|(_, _, _, att, _)| att == &candidate.resource.source_attachment_id)
            .map(|(mid, _, _, _, _)| mid.clone());
        let Some(media_id) = media_id else { continue };
        // 已有变体则跳过：Outbox 重放不重下（C07）。
        let saved: Option<String> = {
            let mid = media_id.clone();
            let representation = candidate.representation.clone();
            core.db.read(move |c| {
                use rusqlite::OptionalExtension;
                Ok(c.query_row("SELECT mv.blob_id FROM media_variants mv JOIN replicas r ON r.blob_id=mv.blob_id AND r.state='verified' WHERE mv.media_id=?1 AND mv.source_representation=?2 LIMIT 1", rusqlite::params![mid,representation], |r| r.get(0)).optional()?)
            }).await?
        };
        if let Some(blob) = saved {
            let sha = crate::paths::blob_sha(&blob)?.to_owned();
            verify_candidate_files(core, candidate, std::slice::from_ref(&sha)).await?;
            resolved.insert(candidate.resource.source_attachment_id.clone(), sha);
            continue;
        }
        let op_id = OperationId::new(format!(
            "mz-{media_id}-{}-{}",
            job.generation,
            crate::config::short_hash(&candidate.representation)
        ));
        let ready = core.objects.ready(&core.db, &op_id, job).await?;
        let sha = if ready {
            let op = op_id.as_str().to_owned();
            core.db
                .read(move |c| {
                    Ok(c.query_row(
                        "SELECT received_sha256 FROM materializations WHERE operation_id=?1",
                        [op],
                        |r| r.get::<_, String>(0),
                    )?)
                })
                .await?
        } else {
            // A committed-but-missing replica needs a fresh materialization, without removing its history.
            let op_id = op_id.clone();
            let mut writer = core
                .objects
                .start_receiving(
                    &core.db,
                    ReceiveParams {
                        job_id: Some(job.job_id.clone()),
                        subject_kind: "media_variant".into(),
                        subject_id: media_id.clone(),
                        generation: job.generation,
                        mime: Some(mime_for(&candidate.kind).to_string()),
                        expected_size: candidate.expected_size,
                        expected_sha256: candidate.expected_sha256.clone(),
                        source_validator: None,
                    },
                    &op_id,
                )
                .await?;
            let seg = &candidate.segments[0];
            let mut last_error = None;
            let mut downloaded = false;
            for url in std::iter::once(&seg.primary).chain(seg.backups.iter()) {
                // Current adapters have no strong validator: restart rather than append unproven bytes.
                writer.reset().await?;
                let op = op_id.as_str().to_owned();
                core.db
                    .write(move |tx| {
                        tx.execute(
                            "UPDATE materializations SET received_bytes=0 WHERE operation_id=?1",
                            [op],
                        )?;
                        Ok(())
                    })
                    .await?;
                match core
                    .http
                    .download_media(
                        &rules,
                        url,
                        Some(&candidate.public_headers),
                        0,
                        None,
                        (&mut writer, &core.db, &op_id),
                        &core.cancel,
                        candidate.expected_size,
                    )
                    .await
                {
                    Ok(_) => {
                        downloaded = true;
                        break;
                    }
                    Err(e) => {
                        last_error = Some(e);
                    }
                }
            }
            if !downloaded {
                return Err(last_error.unwrap_or_else(|| CoreError::Network("no media URL".into())));
            }
            let (_, sha) = core
                .objects
                .finish_and_validate(&core.db, &op_id, writer)
                .await?;
            core.objects.install_object(&core.db, &op_id, &sha).await?;
            sha
        };
        verify_candidate_files(core, candidate, std::slice::from_ref(&sha)).await?;
        resolved.insert(candidate.resource.source_attachment_id.clone(), sha.clone());
        if candidate.resource.role == "video_segment" {
            pending_segments.push((candidate.clone(), media_id, op_id, sha));
        } else {
            commit_candidate(core, job, candidate, &media_id, &op_id, &sha).await?;
        }
    }
    // Validate the ordered whole before any newly prepared segments contribute to completeness.
    // This also supports old protocol peers that provide only an aggregate size/hash.
    for (candidate, attachments) in segment_groups {
        let hashes = attachments
            .iter()
            .map(|att| {
                resolved
                    .get(att)
                    .cloned()
                    .ok_or_else(|| CoreError::InvalidInput("unresolved segment".into()))
            })
            .collect::<Result<Vec<_>, _>>()?;
        verify_candidate_files(core, &candidate, &hashes).await?;
    }
    for (candidate, media_id, op_id, sha) in pending_segments {
        commit_candidate(core, job, &candidate, &media_id, &op_id, &sha).await?;
    }
    let rev = revision_id.clone();
    let g = group.clone();
    let missing: i64 = core.db.read(move |c| Ok(c.query_row("SELECT count(*) FROM media_items mi WHERE revision_id=?1 AND (group_key=?2 OR (group_key IS NULL AND source_attachment_id=?2)) AND required=1 AND NOT EXISTS(SELECT 1 FROM media_variants mv JOIN replicas r ON r.blob_id=mv.blob_id AND r.state='verified' WHERE mv.media_id=mi.media_id)",rusqlite::params![rev,g],|r|r.get(0))?)).await?;
    if missing > 0 {
        return Err(CoreError::Network(
            "required media missing from resolution".into(),
        ));
    }
    create_playback_mux(core, job, &revision_id, &group).await?;
    Ok(())
}

async fn commit_candidate(
    core: &Core,
    job: &ClaimedJob,
    candidate: &trace_contracts::plugin::MediaCandidate,
    media_id: &str,
    op_id: &OperationId,
    sha: &str,
) -> Result<(), CoreError> {
    let spec_v: Value = candidate.claimed_spec.clone();
    let disposition = core
        .objects
        .commit(
            &core.db,
            op_id,
            &CommitInfo {
                job: Some(job.clone()),
                media_id: media_id.to_owned(),
                source_representation: candidate.representation.clone(),
                claimed_spec: spec_v,
            },
            None,
        )
        .await?;
    if disposition == CommitDisposition::Superseded {
        return Ok(());
    }
    // 实测规格探测（后端缺失时保留 unknown，不伪装）。
    let path = core.objects.object_path(sha)?;
    if let Ok(spec) = core.media_backend.probe(&path, &core.cancel).await {
        let spec_json = serde_json::to_value(&spec).unwrap_or_default();
        let mid = media_id.to_owned();
        core.db
            .write(move |tx| {
                tx.execute(
                    "UPDATE media_variants SET measured_spec=?2 WHERE media_id=?1",
                    rusqlite::params![mid, spec_json.to_string()],
                )?;
                Ok(())
            })
            .await?;
    }
    Ok(())
}

async fn verify_candidate_files(
    core: &Core,
    candidate: &trace_contracts::plugin::MediaCandidate,
    hashes: &[String],
) -> Result<(), CoreError> {
    use sha2::{Digest, Sha256};
    use tokio::io::AsyncReadExt;
    let mut size = 0u64;
    let mut digest = Sha256::new();
    let mut buffer = vec![0u8; 256 * 1024];
    for sha in hashes {
        let path = core.objects.object_path(sha)?;
        size = size
            .checked_add(tokio::fs::metadata(&path).await?.len())
            .ok_or_else(|| CoreError::InvalidInput("segment size overflow".into()))?;
        if candidate.expected_sha256.is_some() {
            let mut file = tokio::fs::File::open(path).await?;
            loop {
                let n = file.read(&mut buffer).await?;
                if n == 0 {
                    break;
                }
                digest.update(&buffer[..n]);
            }
        }
    }
    if candidate
        .expected_size
        .is_some_and(|n| n < 0 || n as u64 != size)
    {
        return Err(CoreError::InvalidInput(
            "segment group size mismatch".into(),
        ));
    }
    if let Some(expected) = &candidate.expected_sha256 {
        crate::paths::sha256(expected)?;
        let actual = digest
            .finalize()
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect::<String>();
        if &actual != expected {
            return Err(CoreError::InvalidInput(
                "segment group digest mismatch".into(),
            ));
        }
    }
    Ok(())
}

/// Optional playback derivative. Original tracks remain referenced and unchanged.
async fn create_playback_mux(
    core: &Core,
    job: &ClaimedJob,
    revision: &str,
    group: &str,
) -> Result<(), CoreError> {
    if core.media_backend.ffmpeg.is_none() {
        return Ok(());
    }
    let rev = revision.to_owned();
    let group = group.to_owned();
    let tracks:Vec<(String,String,String)>=core.db.read(move |c|Ok(c.prepare("SELECT mi.media_id,mi.role,mv.blob_id FROM media_items mi JOIN media_variants mv ON mv.media_id=mi.media_id JOIN replicas rp ON rp.blob_id=mv.blob_id AND rp.state='verified' WHERE mi.revision_id=?1 AND mi.group_key=?2 AND mi.role IN ('video','audio') AND mv.measured_spec IS NOT NULL AND mv.source_representation!='playback-mux-v1' ORDER BY mi.role DESC")?.query_map(rusqlite::params![rev,group],|r|Ok((r.get(0)?,r.get(1)?,r.get(2)?)))?.collect::<Result<_,_>>()?)).await?;
    let Some(video) = tracks.iter().find(|t| t.1 == "video") else {
        return Ok(());
    };
    let Some(audio) = tracks.iter().find(|t| t.1 == "audio") else {
        return Ok(());
    };
    let mid = video.0.clone();
    let exists:bool=core.db.read(move |c|Ok(c.query_row("SELECT EXISTS(SELECT 1 FROM media_variants WHERE media_id=?1 AND source_representation='playback-mux-v1')",[mid],|r|r.get(0))?)).await?;
    if exists {
        return Ok(());
    }
    let op = OperationId::new(format!("mz-{}-playback-{}", video.0, job.generation));
    let info = CommitInfo {
        job: Some(job.clone()),
        media_id: video.0.clone(),
        source_representation: "playback-mux-v1".into(),
        claimed_spec: serde_json::json!({"derived_from":[video.2,audio.2]}),
    };
    if core.objects.ready(&core.db, &op, job).await? {
        core.objects.commit(&core.db, &op, &info, None).await?;
        return Ok(());
    }
    let inputs = vec![
        core.objects
            .object_path(crate::paths::blob_sha(&video.2)?)?,
        core.objects
            .object_path(crate::paths::blob_sha(&audio.2)?)?,
    ];
    core.objects.ensure_dirs()?;
    let output = core
        .profile
        .staging_dir()
        .join(format!("{}.mp4", op.as_str()));
    core.media_backend
        .mux_stream_copy(&inputs, output.clone(), &core.cancel)
        .await?;
    let spec = core.media_backend.probe(&output, &core.cancel).await?;
    if spec.audio_tracks.unwrap_or(0) < 1 || spec.width.is_none() {
        return Err(CoreError::InvalidInput("mux missing tracks".into()));
    }
    let mut writer = core
        .objects
        .start_receiving(
            &core.db,
            ReceiveParams {
                job_id: Some(job.job_id.clone()),
                subject_kind: "media_variant".into(),
                subject_id: video.0.clone(),
                generation: job.generation,
                mime: Some("video/mp4".into()),
                expected_size: Some(output.metadata()?.len() as i64),
                expected_sha256: None,
                source_validator: None,
            },
            &op,
        )
        .await?;
    writer.reset().await?;
    use std::io::Read;
    let mut file = std::fs::File::open(&output)?;
    let mut buffer = vec![0; 256 * 1024];
    loop {
        let n = file.read(&mut buffer)?;
        if n == 0 {
            break;
        }
        writer.write_chunk(&core.db, &op, &buffer[..n]).await?;
    }
    drop(file);
    let (_, sha) = core
        .objects
        .finish_and_validate(&core.db, &op, writer)
        .await?;
    core.objects.install_object(&core.db, &op, &sha).await?;
    core.objects
        .commit(&core.db, &op, &info, Some(serde_json::to_value(spec)?))
        .await?;
    std::fs::remove_file(output)?;
    Ok(())
}

fn mime_for(kind: &str) -> &'static str {
    match kind {
        "dash_audio" => "audio/mp4",
        "image_jpeg" => "image/jpeg",
        "image_png" => "image/png",
        "durl_segment" => "video/x-flv",
        _ => "video/mp4",
    }
}
