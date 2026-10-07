//! Core 结构：进程内服务总线 + 调度器（docs/design.md §9）。
//!
//! - 打开：迁移 → 恢复扫描（旧租约、物化矩阵、副本验证）→ 启动调度。
//! - 调度：持久任务按到期时间认领；预算（元数据节流、媒体全局/单账号
//!   并发）按 m1a-runtime-contracts §6 执行；暂停期间不认领新任务。
//! - 订阅扫描使用单一活动 Job（job_key 唯一），页游标持久化于
//!   scan_checkpoints 与任务载荷（C01/C31）。

use crate::config::Profile;
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
use std::sync::atomic::{AtomicBool, AtomicU64, AtomicUsize, Ordering};
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
type MetadataTurn = Arc<tokio::sync::Mutex<()>>;
use serde_json::Value;
use trace_domain::revision::{Completeness, ContentObservation, ResourceIdentity, semantic_digest};
use trace_domain::search::normalize_v1;
use trace_domain::time::{TimePrecision, TraceTime};

#[cfg(test)]
#[path = "core_tests.rs"]
mod tests;
#[cfg(test)]
#[path = "core_vertical_tests.rs"]
mod vertical_tests;

#[cfg(test)]
#[path = "core_acquisition_tests.rs"]
mod acquisition_tests;
#[cfg(test)]
#[path = "core_lifecycle_tests.rs"]
mod lifecycle_tests;

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
    pub desktop_event_clients: AtomicUsize,
    pub desktop_event_connections: AtomicU64,
    pub cancel: CancellationToken,
    pub media_global: Arc<tokio::sync::Semaphore>,
    pub live_global: Arc<tokio::sync::Semaphore>,
    pub cache_build: tokio::sync::Mutex<()>,
    pub archive_access: tokio::sync::RwLock<()>,
    pub recording_recovery: tokio::sync::Mutex<()>,
    pub(crate) active_jobs: Mutex<HashMap<String, usize>>,
    tasks: tokio_util::task::TaskTracker,
    shutdown_lock: tokio::sync::Mutex<()>,
    account_media: Mutex<HashMap<String, Arc<tokio::sync::Semaphore>>>,
    pub leases: Mutex<HashMap<String, MediaLease>>,
    pub cookie_cache: Arc<Mutex<HashMap<String, String>>>,
    metadata_last_request: Mutex<HashMap<String, Instant>>,
    metadata_urgent: Mutex<HashMap<String, usize>>,
    metadata_regular: Mutex<HashMap<String, usize>>,
    metadata_streak: Mutex<HashMap<String, usize>>,
    metadata_turns: Mutex<HashMap<String, MetadataTurn>>,
    _db_actor: Arc<DbActor>,
}

impl Core {
    pub async fn open(profile: Profile) -> Result<Arc<Self>, CoreError> {
        Self::open_impl(profile, None).await
    }
    #[cfg(debug_assertions)]
    pub async fn open_with_adapters(
        profile: Profile,
        specs: Vec<crate::pluginhost::AdapterSpec>,
    ) -> Result<Arc<Self>, CoreError> {
        Self::open_impl(profile, Some(specs)).await
    }
    async fn open_impl(
        profile: Profile,
        specs: Option<Vec<crate::pluginhost::AdapterSpec>>,
    ) -> Result<Arc<Self>, CoreError> {
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
        let mut http = ConstrainedClient::build()?;
        if let Some(limits) = &profile.acquisition_limits {
            http = http.with_budget(Arc::new(crate::request_budget::RequestBudget::acquisition(
                limits,
                profile.data_root.join("state/acquisition-budget.json"),
            )?));
        }
        let http = Arc::new(http);
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
            specs.unwrap_or_else(|| default_adapter_specs(&prof)),
            credential_lookup,
        );
        let media_backend = MediaBackend::detect(&prof).await;
        let core = Arc::new(Self {
            profile: prof.clone(),
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
            desktop_event_clients: AtomicUsize::new(0),
            desktop_event_connections: AtomicU64::new(0),
            cancel: CancellationToken::new(),
            media_global: Arc::new(tokio::sync::Semaphore::new(
                crate::config::MEDIA_CONCURRENCY_GLOBAL,
            )),
            account_media: Mutex::new(HashMap::new()),
            live_global: Arc::new(tokio::sync::Semaphore::new(
                prof.live_concurrency.clamp(1, 4),
            )),
            tasks: tokio_util::task::TaskTracker::new(),
            cache_build: tokio::sync::Mutex::new(()),
            archive_access: tokio::sync::RwLock::new(()),
            recording_recovery: tokio::sync::Mutex::new(()),
            active_jobs: Mutex::new(HashMap::new()),
            shutdown_lock: tokio::sync::Mutex::new(()),
            leases: Mutex::new(HashMap::new()),
            cookie_cache,
            metadata_last_request: Mutex::new(HashMap::new()),
            metadata_urgent: Mutex::new(HashMap::new()),
            metadata_regular: Mutex::new(HashMap::new()),
            metadata_streak: Mutex::new(HashMap::new()),
            metadata_turns: Mutex::new(HashMap::new()),
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
        let disabled: Vec<String> = core
            .db
            .read(|c| {
                Ok(
                    c.prepare("SELECT platform FROM plugins_state WHERE enabled=0")?
                        .query_map([], |r| r.get(0))?
                        .collect::<Result<Vec<_>, _>>()?,
                )
            })
            .await?;
        for platform in disabled {
            core.host.set_enabled(&platform, false);
        }
        let quality_default=serde_json::json!({"prefer_fps":core.profile.quality_prefer_fps,"wait_seconds":core.profile.recycle_wait_seconds.max(0),"version":"m1-v1"}).to_string();
        core.db
            .write(move |tx| {
                tx.execute(
                    "INSERT OR IGNORE INTO meta(key,value) VALUES('quality_policy',?1)",
                    [quality_default],
                )?;
                Ok(())
            })
            .await?;
        Ok(core)
    }

    /// 启动恢复：旧实例租约回收 + 物化恢复矩阵 + 副本验证（有界）。
    pub async fn recover(&self) -> Result<serde_json::Value, CoreError> {
        crate::scan_range::ensure_deep_lanes(&self.db).await?;
        let stale = jobs::recover_stale_leases(&self.db, self.instance_id.as_str()).await?;
        let mat = self.objects.recover_materializations(&self.db).await?;
        let recordings = crate::recording::recover_closed(self).await?;
        let broken = self.objects.verify_replicas(&self.db, 64).await?;
        self.db.write(crate::upgrades::invalidate_stale).await?;
        Ok(serde_json::json!({
            "stale_leases_requeued": stale,
            "materializations": mat,
            "recording_fragments": recordings,
            "broken_replicas": broken,
        }))
    }

    /// 启动调度循环（daemon 中 spawn）。
    pub fn start_scheduler(self: &Arc<Self>) {
        let core = self.clone();
        self.tasks.spawn(async move {
            if core.db.write(|tx|crate::quality::schedule(tx,false)).await.is_err(){return;}
            let mut tick = tokio::time::interval(Duration::from_millis(500));
            loop {
                tokio::select! {
                    _ = tick.tick() => {
                        if core.cancel.is_cancelled() {break;}
                        if core.paused.load(Ordering::SeqCst) {
                            continue;
                        }
                        if let Ok(claimed) = jobs::claim_due(&core.db, core.instance_id.as_str(), 4.min(32usize.saturating_sub(core.tasks.len())) as i32).await {
                            for job in claimed {
                                let core2 = core.clone();
                                core.tasks.spawn(async move {
                                    execute(core2, job).await;
                                });
                            }
                        }
                        // 派生：搜索索引随 tick 有界推进。
                        let _ = search::index_pending(&core.db, 100).await;
                        core.host.reap_idle().await;
                    }
                    _ = core.cancel.cancelled() => break,
                }
            }
        });
    }

    /// 元数据节流：每命名空间最小间隔 + 抖动（契约 §6）。
    pub async fn metadata_pacing(&self, namespace: &str) {
        self.metadata_pacing_priority(namespace, false).await;
    }
    pub async fn metadata_pacing_priority(&self, namespace: &str, urgent: bool) {
        let interval_ms = self.profile.metadata_interval_ms()
            + rand::random_range(0..=self.profile.metadata_jitter_ms());
        struct Waiting<'a> {
            map: &'a Mutex<HashMap<String, usize>>,
            key: String,
            active: bool,
        }
        impl Drop for Waiting<'_> {
            fn drop(&mut self) {
                if self.active {
                    let mut m = self.map.lock().unwrap();
                    if let Some(n) = m.get_mut(&self.key) {
                        *n = n.saturating_sub(1);
                    }
                }
            }
        }
        let waiting = if urgent {
            &self.metadata_urgent
        } else {
            &self.metadata_regular
        };
        let _waiting = Waiting {
            map: waiting,
            key: namespace.to_owned(),
            active: true,
        };
        *waiting
            .lock()
            .unwrap()
            .entry(namespace.to_owned())
            .or_default() += 1;
        // Tokio's FIFO mutex gives every consumer a turn within its priority
        // class. Racing all sleepers at the same deadline let newer scans keep
        // winning while an observe/resolve waited indefinitely under saturation.
        // The two class heads still share the interval and four-urgent budget.
        let turn = self
            .metadata_turns
            .lock()
            .unwrap()
            .entry(format!(
                "{}:{namespace}",
                if urgent { "urgent" } else { "regular" }
            ))
            .or_insert_with(|| Arc::new(tokio::sync::Mutex::new(())))
            .clone();
        let _turn = tokio::select! {
            _ = self.cancel.cancelled() => return,
            turn = turn.lock() => turn,
        };
        loop {
            if self.cancel.is_cancelled() {
                return;
            }
            let urgent_waiters = self
                .metadata_urgent
                .lock()
                .unwrap()
                .get(namespace)
                .copied()
                .unwrap_or(0);
            let regular_waiters = self
                .metadata_regular
                .lock()
                .unwrap()
                .get(namespace)
                .copied()
                .unwrap_or(0);
            let streak = self
                .metadata_streak
                .lock()
                .unwrap()
                .get(namespace)
                .copied()
                .unwrap_or(0);
            if (urgent && regular_waiters > 0 && streak >= 4)
                || (!urgent && urgent_waiters > 0 && streak < 4)
            {
                tokio::select! {_=self.cancel.cancelled()=>return,_=tokio::time::sleep(Duration::from_millis(100))=>{}}
                continue;
            }
            let decision = {
                let mut map = self.metadata_last_request.lock().unwrap();
                let now = Instant::now();
                let elapsed = map
                    .get(namespace)
                    .map(|t| t.elapsed())
                    .unwrap_or(Duration::from_secs(u64::MAX / 2));
                let min = Duration::from_millis(interval_ms);
                if elapsed >= min {
                    map.insert(namespace.to_string(), now);
                    self.metadata_streak.lock().unwrap().insert(
                        namespace.to_owned(),
                        if urgent { streak.saturating_add(1) } else { 0 },
                    );
                    None
                } else {
                    Some(min - elapsed)
                }
            };
            match decision {
                Some(d) => tokio::select! {
                    _ = self.cancel.cancelled() => return,
                    _ = tokio::time::sleep(d) => {},
                },
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
        self.cookie_cache
            .lock()
            .unwrap()
            .retain(|key, _| key != platform && !key.starts_with(&format!("{platform}/")));
    }

    /// 优雅停机：停止调度 → 关闭适配器；DB 随 Core 关闭。
    pub async fn adapter_call(
        &self,
        job: &ClaimedJob,
        platform: &str,
        method: &str,
        params: Value,
        deadline: Option<u64>,
    ) -> Result<Value, CoreError> {
        let guard = job.clone();
        let p = platform.to_owned();
        let credential = self
            .db
            .read(move |c| jobs::scope::credential(c, &guard, &p))
            .await?;
        let credential_scope = if let Some((id, generation, reference, status)) = credential {
            if !matches!(status.as_str(), "valid" | "authenticated") {
                return Err(CoreError::AuthRequired(
                    "subscription credential requires validation".into(),
                ));
            }
            let secret = String::from_utf8(self.vault.load(&reference)?)
                .map_err(|_| CoreError::AuthRequired("credential encoding".into()))?;
            let scope = format!("{id}/g{generation}");
            self.cache_cookie(&format!("{platform}/{scope}"), secret);
            Some(scope)
        } else {
            self.cookie_cache.lock().unwrap().remove(platform);
            None
        };
        let guard = job.clone();
        self.db.read(move |c| guard.check(c)).await?;
        let result = self
            .host
            .call_scoped(
                platform,
                credential_scope.as_deref(),
                method,
                params,
                deadline,
            )
            .await;
        let guard = job.clone();
        self.db.read(move |c| guard.check(c)).await?;
        result
    }

    pub async fn shutdown(&self) {
        let _lock = self.shutdown_lock.lock().await;
        self.paused.store(true, Ordering::SeqCst);
        // Shutdown permanently cancels this Core instance. Revoke its owner but
        // preserve acquisition generation so the next instance can finish the
        // same durable operation; stale workers still fail the lease check.
        let _ = self.db.write(|tx| {
            tx.execute("UPDATE jobs SET state='queued',lease_owner=NULL,lease_expires_at=NULL,updated_at=?1 WHERE state IN ('leased','running')",[jobs::now()])?;
            Ok(())
        }).await;
        self.cancel.cancel();
        self.host.shutdown_all().await;
        self.tasks.close();
        if tokio::time::timeout(Duration::from_secs(8), self.tasks.wait())
            .await
            .is_err()
        {
            tracing::warn!(
                "shutdown sealing deadline exceeded; durable materializations require recovery"
            );
        }
    }
}

/// 执行单个任务；失败按分类落状态（C26/C29 语义）。
async fn execute(core: Arc<Core>, job: ClaimedJob) {
    // A revoked database lease does not mean the worker has finished sealing
    // files. Keep a runtime count until its complete future has been dropped.
    struct Active<'a>(&'a Mutex<HashMap<String, usize>>, String);
    impl Drop for Active<'_> {
        fn drop(&mut self) {
            let mut jobs = self.0.lock().unwrap();
            if let Some(count) = jobs.get_mut(&self.1) {
                *count -= 1;
                if *count == 0 {
                    jobs.remove(&self.1);
                }
            }
        }
    }
    *core
        .active_jobs
        .lock()
        .unwrap()
        .entry(job.job_id.clone())
        .or_default() += 1;
    let _active = Active(&core.active_jobs, job.job_id.clone());
    if job.mark_running(&core.db).await.is_err() {
        return;
    }
    let work = async {
        match job.job_type.as_str() {
            "source_acquire" => crate::source_acquisition::run(&core, &job).await,
            "auth_completion" => crate::upgrades::run(&core, &job).await,
            "credential_probe" => crate::ipc::dispatch::probe_credential_job(&core, &job).await,
            "scan" => run_scan(core.as_ref(), &job).await,
            "observe" => run_observe(core.as_ref(), &job).await,
            "media" => run_media(core.as_ref(), &job).await,
            "record" => crate::recording::run(core.as_ref(), &job).await,
            "quality_maintenance" => crate::quality::maintain(core.as_ref(), &job).await,
            "account_profile" => crate::accounts::refresh_job(core.as_ref(), &job).await,
            "live_auxiliary" => crate::recording::auxiliary::run(core.as_ref(), &job).await,
            "backup" => {
                let a: trace_contracts::control::BackupStartArgs =
                    serde_json::from_value(job.payload.clone())?;
                crate::storage_ops::backup_guarded(
                    &core,
                    std::path::Path::new(&a.target_root),
                    a.include_media,
                    Some(job.clone()),
                )
                .await
                .map(|_| ())
            }
            "export" => {
                let a: trace_contracts::control::ExportStartArgs =
                    serde_json::from_value(job.payload.clone())?;
                crate::archive_export::export_guarded(
                    &core,
                    std::path::Path::new(&a.target_dir),
                    a.include_media,
                    a.creator_id.as_deref(),
                    Some(job.clone()),
                )
                .await
                .map(|_| ())
            }
            _ => Err(CoreError::InvalidInput("unknown job type".into())),
        }
    };
    tokio::pin!(work);
    let monitor = async {
        let mut tick = tokio::time::interval(Duration::from_millis(200));
        loop {
            tick.tick().await;
            if core.paused.load(Ordering::SeqCst) {
                return true;
            }
            if job.heartbeat(&core.db).await.is_err() {
                return false;
            }
        }
    };
    tokio::pin!(monitor);
    let result = if job.job_type == "record" {
        work.await
    } else {
        tokio::select! {
            result = &mut work => result,
            _ = core.cancel.cancelled() => { let _ = job.requeue(&core.db).await; return; },
            requeue = &mut monitor => {
                if requeue {let _ = job.requeue(&core.db).await;}
                return;
            }
        }
    };
    if core.cancel.is_cancelled() || core.paused.load(Ordering::SeqCst) {
        let _ = job.requeue(&core.db).await;
        if job.job_type == "record" {
            let _ = crate::recording::recover_closed(&core).await;
        }
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
            let message = if matches!(e, CoreError::VersionConflict(_))
                && job.payload["source_request_id"].is_string()
            {
                "source_selection_changed".to_owned()
            } else {
                format!("{:?}", class)
            };
            if job.fail(&core.db, &class, &message, None).await.is_ok() {
                core.hub
                    .publish_progress(&job.job_id, 0.0, Some(message), &job.job_id);
            }
        }
    }
    if job.job_type == "record" {
        let _ = crate::recording::recover_closed(&core).await;
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
        .unwrap_or(core.profile.scan_page_budget() as u64)
        .clamp(1, core.profile.scan_page_budget() as u64) as u32;

    let (account_key, namespace, paused_state, content_type, range_json, created): (
        String,
        String,
        String,
        String,
        String,
        String,
    ) = {
        let sub = subscription_id.clone();
        core.db
            .read(move |c| {
                Ok(c.query_row(
                    "SELECT a.native_account_key, a.namespace, s.state, s.content_type, s.range_json, s.created_at
                     FROM subscriptions s JOIN source_accounts a ON a.account_id=s.account_id
                     WHERE s.subscription_id=?1",
                    [&sub],
                    |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?, r.get(3)?, r.get(4)?, r.get(5)?)),
                )?)
            })
            .await?
    };
    if paused_state != "enabled" {
        return Err(CoreError::Paused("subscription disabled".into()));
    }
    let platform = namespace.split(':').next().unwrap_or("").to_string();
    if matches!(platform.as_str(), "bilibili" | "douyin") {
        crate::accounts::schedule(core, job, &subscription_id).await?;
    }
    if content_type == "live" {
        return crate::recording::poll(core, job, &account_key, &platform).await;
    }

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

    let range_value: Value = serde_json::from_str(&range_json)?;
    if cursor.is_none() && mode == "history" && range_value["kind"] == "resume_cursor" {
        let sub = subscription_id.clone();
        let started=core.db.read(move|c|Ok(c.query_row("SELECT EXISTS(SELECT 1 FROM scan_checkpoints WHERE subscription_id=?1 AND scan_type='history')",[sub],|r|r.get::<_,bool>(0))?)).await?;
        if !started {
            cursor = range_value["cursor"].as_str().map(str::to_owned);
        }
    }
    let range = crate::scan_range::Range::parse(&range_value, &created)?;
    let recent_days = if mode == "incremental" {
        Some(7)
    } else {
        range_value["days"].as_i64()
    };
    let max_pages = if mode == "incremental" {
        max_pages.min(
            range_value["incremental_page_budget"]
                .as_u64()
                .unwrap_or(2)
                .clamp(1, 10) as u32,
        )
    } else {
        max_pages.min(2)
    };
    let mut seen_cursors = std::collections::HashSet::new();
    let mut processed_pages = 0u32;
    let mut has_more = false;
    let mut new_entries = 0i64;

    for _ in 0..max_pages.clamp(1, 100) {
        core.metadata_pacing(&namespace).await;
        if !seen_cursors.insert(cursor.clone()) {
            return Err(CoreError::InvalidInput("cursor cycle".into()));
        }
        let params = EnumerateParams {
            content_type: content_type.clone(),
            native_account_key: account_key.clone(),
            mode: mode.clone(),
            cursor: cursor.clone(),
            recent_days,
            max_pages: 1,
        };
        let page_v = core
            .adapter_call(
                job,
                &platform,
                trace_contracts::plugin::methods::ENUMERATE,
                serde_json::to_value(&params).unwrap_or_default(),
                Some(30_000),
            )
            .await?;
        let page: EnumeratedPage = serde_json::from_value(page_v)
            .map_err(|e| CoreError::InvalidInput(format!("bad page: {e}")))?;
        if page.items.len() > trace_domain::MAX_PAGE_ITEMS as usize
            || (page.has_more && (page.next_cursor.is_none() || page.next_cursor == cursor))
        {
            return Err(CoreError::InvalidInput(
                "non-advancing or oversized page".into(),
            ));
        }
        let (added, _) = process_scan_page(
            core,
            job,
            &namespace,
            &subscription_id,
            &mode,
            page.items
                .iter()
                .filter(|i| range.includes(i.published_at.as_deref()))
                .cloned()
                .collect(),
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

    if mode == "history" && !has_more {
        return Ok(());
    }
    // 单一活动 Job：继续或安排下一轮周期。
    let job_id = job.job_id.clone();
    let sub = subscription_id.clone();
    let cursor2 = if mode == "incremental" && !has_more {
        None
    } else {
        cursor.clone()
    };
    let interval: i64 = if mode == "deep" {
        7 * 86400
    } else {
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
        plus_seconds(&jobs::now(), 1)
    } else {
        plus_seconds(
            &jobs::now(),
            crate::config::jittered_check_seconds(interval),
        )
    };
    let guard = job.clone();
    core.db
        .write(move |tx| {
            guard.check(tx)?;
            if mode == "incremental" {
                crate::scan_range::finish_recent_sweep(
                    tx,
                    &sub,
                    cursor.as_deref(),
                    has_more,
                    processed_pages,
                )?;
            }
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
    let cov = trace_contracts::content::safe_metadata(coverage);
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
                trace_contracts::content::safe_metadata(&i.stats),
            )
        })
        .collect();
    let guard = job.clone();
    core.db
        .write(move |tx| {
            guard.check(tx)?;
            let mut added = 0i64;
            crate::scan_range::record_cursor(tx,&sub,&mode_owned,cursor_owned.as_deref())?;
            let mut last_pub: Option<String> = None;
            for (key, kind, title, published_at, precision, _pinned, summary_body, mut stats) in entries_payload {
                if !stats.is_object() { stats = serde_json::json!({"stats": stats}); }
                stats["_trace_enumeration"] = cov.clone();
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
                        added += 1;
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
                if tx.query_row("SELECT lifecycle='deleted' FROM entries WHERE entry_id=?1",[&actual_id],|r|r.get::<_,bool>(0))? {continue;}
                // 短摘要观察：完整详情由 observe 任务补全（短摘要不覆盖完整 Revision）。
                if let Some(body) = summary_body.filter(|_| !tx.query_row("SELECT EXISTS(SELECT 1 FROM entry_revisions WHERE entry_id=?1 AND completeness='full')", [&actual_id], |r|r.get::<_,bool>(0)).unwrap_or(true)) {
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
                                rusqlite::params![rev_id, actual_id, digest, title, body, now],
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
                         COALESCE((SELECT MAX(observation_seq)+1 FROM source_observations WHERE entry_id=?2),1), ?4)",
                        rusqlite::params![format!("obs-{}", uuid::Uuid::new_v4()), actual_id, now,stats.to_string()],
                    )?;
                }
                jobs::consumers::discover(tx, &actual_id, &sub)?;
                // A stable logical job holds independent pending consumers.
                let okey = format!("observe:{ns}:{kind}:{key}");
                jobs::consumers::request(tx, &okey, "observe", &actual_id,
                    serde_json::json!({"entry_id":actual_id,"subscription_id":sub}), &sub)?;
            }
            tx.execute(
                "INSERT INTO scan_checkpoints (subscription_id, scan_type, cursor, coverage, generation, updated_at)
                 VALUES (?1,?2,?3,?4,1,?5)
                 ON CONFLICT(subscription_id, scan_type) DO UPDATE SET cursor=excluded.cursor, coverage=excluded.coverage, updated_at=excluded.updated_at",
                rusqlite::params![sub, mode_owned, cursor_owned, cov.to_string(), now],
            )?;
            tx.execute("UPDATE jobs SET payload=json_set(payload,'$.cursor',?2) WHERE job_id=?1",rusqlite::params![guard.job_id,cursor_owned])?;
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
    let (namespace, native_key, account_id, native_kind): (String, String, String, String) = {
        let e = entry_id.clone();
        core.db
            .read(move |c| {
                Ok(c.query_row(
                    "SELECT namespace, native_entry_key, account_id, native_kind FROM entries WHERE entry_id=?1",
                    [&e],
                    |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?, r.get(3)?)),
                )?)
            })
            .await?
    };
    let platform = namespace.split(':').next().unwrap_or("").to_string();
    let context = crate::acquisitions::begin(core, job, &platform, &entry_id).await?;
    crate::acquisitions::require_access(&context)?;
    let mut acquisition_job = job.clone();
    acquisition_job.payload["acquisition_context_id"] = Value::String(context.context_id.clone());
    let job = &acquisition_job;
    core.metadata_pacing(&namespace).await;
    let detail_v = core
        .adapter_call(
            job,
            &platform,
            trace_contracts::plugin::methods::OBSERVE,
            serde_json::json!({"native_entry_key": native_key, "native_kind": native_kind}),
            Some(30_000),
        )
        .await?;
    let mut detail: ObservedDetail = serde_json::from_value(detail_v)
        .map_err(|e| CoreError::InvalidInput(format!("bad detail: {e}")))?;
    if detail.native_entry_key != native_key || detail.native_kind != native_kind {
        return Err(CoreError::InvalidInput(
            "observation identity mismatch".into(),
        ));
    }
    detail.metadata = trace_contracts::content::safe_metadata(&detail.metadata);
    detail.stats = trace_contracts::content::safe_metadata(&detail.stats);
    detail.source_version_evidence =
        trace_contracts::content::safe_metadata(&detail.source_version_evidence);
    detail
        .document
        .validate(&detail.resources)
        .map_err(|e| CoreError::InvalidInput(e.into()))?;
    if detail.document.blocks.len() > 8192
        || serde_json::to_vec(&detail.document)?.len() > 512 * 1024
    {
        return Err(CoreError::InvalidInput("document budget exceeded".into()));
    }

    let context =
        crate::acquisitions::resolution_context(core, job, &entry_id, context, &detail.metadata)
            .await?;
    let mut resolved_job = job.clone();
    resolved_job.payload["acquisition_context_id"] = Value::String(context.context_id);
    persist_detail(core, &resolved_job, &entry_id, &account_id, detail).await?;

    Ok(())
}

/// A revision, its observation and required work are committed together. A crash
/// cannot leave a full revision with no queued resource acquisition.
pub(crate) async fn persist_detail(
    core: &Core,
    job: &ClaimedJob,
    entry_id: &str,
    account_id: &str,
    detail: ObservedDetail,
) -> Result<(), CoreError> {
    let e = entry_id.to_owned();
    let account = account_id.to_owned();
    let guard = job.clone();
    core.db.write(move |tx| {
        guard.check(tx)?;
        let resources=detail.resources.iter().map(|r|ResourceIdentity{role:r.role.clone(),source_attachment_id:r.source_attachment_id.clone(),position:r.position}).collect::<Vec<_>>();
        let incoming=ContentObservation{title:detail.title.clone(),normalized_body:normalize_v1(&detail.normalized_body),resources,completeness:if detail.completeness=="full"{Completeness::Full}else{Completeness::Summary},source_version_evidence:None};
        let existing=tx.prepare("SELECT revision_id,completeness,semantic_digest FROM entry_revisions WHERE entry_id=?1")?.query_map([&e],|r|Ok((r.get::<_,String>(0)?,if r.get::<_,String>(1)?=="full"{Completeness::Full}else{Completeness::Summary},r.get::<_,String>(2)?)))?.collect::<Result<Vec<_>,_>>()?;
        let base_digest=semantic_digest(&incoming.title,&incoming.normalized_body,&incoming.resources);
        let mut digest=base_digest.clone();
        let document_body=detail.document.blocks.iter().map(|b|match b { trace_contracts::content::Block::Text{text,style} if style=="paragraph"=>Some(text.as_str()),_=>None }).collect::<Option<Vec<_>>>();
        if !detail.document.blocks.is_empty() && document_body.is_none_or(|blocks|normalize_v1(&blocks.join("\n"))!=incoming.normalized_body) {
            use sha2::Digest;
            let mut hash=sha2::Sha256::new();hash.update(b"trace-document-v1\0");hash.update(base_digest.as_bytes());hash.update(serde_json::to_vec(&detail.document.blocks)?);digest=hash.finalize().iter().map(|b|format!("{b:02x}")).collect();
        }
        let decision=trace_domain::revision::decide_revision_digest(&existing,incoming.completeness,&digest);
        let rid=match decision {
            trace_domain::revision::RevisionDecision::Reuse{revision_id}=>Some(revision_id),
            trace_domain::revision::RevisionDecision::CreateNew=>Some(format!("rev-{}",uuid::Uuid::new_v4())),
            trace_domain::revision::RevisionDecision::KeepExistingIgnoreSummary{..}=>None,
        };
        let ts=jobs::now();
        if let Some(rid)=&rid {

            tx.execute("INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,source_version_evidence,completeness,observed_at,document_json,metadata_json) VALUES(?1,?2,?3,?4,?5,?6,?7,?8,?9,?10) ON CONFLICT(revision_id) DO UPDATE SET completeness=excluded.completeness,document_json=excluded.document_json,metadata_json=excluded.metadata_json,source_version_evidence=excluded.source_version_evidence WHERE entry_revisions.completeness!='full' AND excluded.completeness='full'",rusqlite::params![rid,e,digest,detail.title,detail.normalized_body,detail.source_version_evidence.to_string(),detail.completeness,ts,serde_json::to_string(&detail.document)?,detail.metadata.to_string()])?;
            for r in &detail.resources {
                tx.execute("INSERT OR IGNORE INTO media_items(media_id,revision_id,role,position,source_attachment_id,group_key,title,required) VALUES(?1,?2,?3,?4,?5,?6,?7,?8)",rusqlite::params![format!("med-{}",uuid::Uuid::new_v4()),rid,r.role,r.position,r.source_attachment_id,r.group_key,r.title,r.required as i64])?;
            }
            let mut groups=std::collections::BTreeSet::new();
            for r in &detail.resources { groups.insert(r.group_key.clone().unwrap_or_else(||r.source_attachment_id.clone())); }
            let suppressed:bool=tx.query_row("SELECT media_suppressed!=0 OR lifecycle='deleted' FROM entries WHERE entry_id=?1",[&e],|r|r.get(0))?;
            for group in groups.into_iter().filter(|_|!suppressed) {
                let payload=serde_json::json!({"entry_id":e,"revision_id":rid,"group":group,"account_id":account,"subscription_id":guard.payload.get("subscription_id")});
                if let Some(source)=guard.payload["source_request_id"].as_str() {
                    let mut payload=payload;payload["source_request_id"]=serde_json::json!(source);payload["source_request_generation"]=guard.payload["source_request_generation"].clone();
                    tx.execute("INSERT OR IGNORE INTO jobs(job_id,job_key,job_type,subject_id,payload,state,priority,created_at,updated_at) VALUES(?1,?2,'media',?3,?4,'queued',1,?5,?5)",rusqlite::params![format!("job-{}",uuid::Uuid::new_v4()),format!("source-media:{source}:{rid}:{group}"),e,payload.to_string(),ts])?;
                } else if guard.payload["completion"] == true {
                    crate::upgrades::schedule_media(tx,&guard,payload,&e,rid,&group)?;
                } else if let Some(sub) = guard.payload["subscription_id"].as_str() {
                    jobs::consumers::discover(tx, &e, sub)?;
                    jobs::consumers::request(tx, &format!("media:{rid}:{group}"), "media", &e, payload, sub)?;
                } else {
                    // Explicit archive rechecks without a subscription remain anonymous.
                    tx.execute("INSERT INTO jobs(job_id,job_key,job_type,subject_id,payload,target_generation,priority,state,created_at,updated_at) VALUES(?1,?2,'media',?3,?4,1,1,'queued',?5,?5) ON CONFLICT(job_key) DO UPDATE SET state='queued',target_generation=jobs.target_generation+1,next_run_at=excluded.updated_at,updated_at=excluded.updated_at WHERE jobs.state='succeeded'",rusqlite::params![format!("job-{}",uuid::Uuid::new_v4()),format!("media:{rid}:{group}"),e,payload.to_string(),ts])?;
                }
            }
        }
        let evidence=serde_json::json!({"stats":detail.stats,"metadata":detail.metadata,"source":detail.source_version_evidence});
        tx.execute("INSERT INTO source_observations(observation_id,entry_id,revision_id,access_state,observed_at,observation_seq,evidence) VALUES(?1,?2,?3,?4,?5,COALESCE((SELECT MAX(observation_seq)+1 FROM source_observations WHERE entry_id=?2),1),?6)",rusqlite::params![format!("obs-{}",uuid::Uuid::new_v4()),e,rid,detail.access_state,ts,evidence.to_string()])?;
        if let Some(context)=guard.payload["acquisition_context_id"].as_str() {
            tx.execute("UPDATE source_observations SET context_id=?1 WHERE rowid=last_insert_rowid()",[context])?;
            crate::acquisitions::record(tx,context,None,None,"observed",&Value::Null)?;
        }
        crate::objects::outbox_emit(tx,"entry.revision.created",&e,&serde_json::json!({"revision_id":rid}))?;
        Ok(())
    }).await
}

/// 媒体获取执行器（C27/C28 的核心路径）：组内必要资源齐才整组完成；
/// 每资源独立物化操作；无校验器时按设计从头重取。
pub(crate) async fn run_media(core: &Core, job: &ClaimedJob) -> Result<(), CoreError> {
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

    let (namespace, native_key, account_id, native_kind): (String, String, String, String) = {
        let e = entry_id.clone();
        core.db
            .read(move |c| {
                Ok(c.query_row(
                    "SELECT e.namespace, e.native_entry_key, e.account_id, e.native_kind FROM entries e WHERE e.entry_id=?1",
                    [&e],
                    |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?, r.get(3)?)),
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
    let (default_preference, wait) = crate::quality::policy(core).await?;
    let context = crate::acquisitions::begin(core, job, &platform, &entry_id).await?;
    crate::acquisitions::require_access(&context)?;
    let quality_requirement = crate::quality::requirement(core, job).await?;
    let preference = quality_requirement.prefer_fps.unwrap_or(default_preference);
    let params = ResolveMediaParams {
        native_entry_key: native_key.clone(),
        native_kind,
        resources: resources_dto.clone(),
        quality_hint: serde_json::json!({"prefer_fps":preference,"all_variants":job.payload["source_request_id"].is_string()}),
    };
    let plan_v = core
        .adapter_call(
            job,
            &platform,
            trace_contracts::plugin::methods::RESOLVE_MEDIA,
            serde_json::to_value(&params).unwrap_or_default(),
            Some(30_000),
        )
        .await?;
    let mut plan: trace_contracts::plugin::ResolveMediaResult = serde_json::from_value(plan_v)
        .map_err(|e| CoreError::InvalidInput(format!("bad plan: {e}")))?;
    let context =
        crate::acquisitions::resolution_context(core, job, &entry_id, context, &plan.notes).await?;
    let mut acquisition_job = job.clone();
    acquisition_job.payload["acquisition_context_id"] = Value::String(context.context_id.clone());
    let job = &acquisition_job;
    for candidate in &mut plan.candidates {
        candidate.claimed_spec = trace_contracts::content::safe_metadata(&candidate.claimed_spec);
    }
    if job.payload["source_request_id"].is_string() {
        crate::source_acquisition::select_media(core, job, &mut plan).await?;
    }
    let requested_quality = serde_json::to_value(&quality_requirement)?;
    let offers = plan.candidates.clone();
    let identities = resources_dto.clone();
    let rev = revision_id.clone();
    let guard = job.clone();
    core.db
        .write(move |tx| {
            guard.check(tx)?;
            crate::quality::record_offers(tx, &rev, &identities, &offers, preference)?;
            tx.execute("UPDATE media_items SET acquisition_evidence=json_set(COALESCE(acquisition_evidence,'{}'),'$.requested_quality',json(?2)) WHERE revision_id=?1",rusqlite::params![rev,requested_quality.to_string()])?;
            Ok(())
        })
        .await?;

    crate::quality::filter_required(&quality_requirement, &resources_dto, &mut plan.candidates)?;
    if !job.payload["source_request_id"].is_string()
        && (context.auth_state == trace_contracts::acquisition::AuthenticationState::Authenticated
            || job.payload["completion"] == true)
    {
        crate::upgrades::highest_candidates(&mut plan.candidates, preference);
    }

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
    let mut candidate_job = job.clone();
    if let Some(id) = crate::upgrades::prepare(core, job, &revision_id, &group).await? {
        candidate_job.payload["upgrade_candidate_id"] = Value::String(id);
    }
    let job = &candidate_job;
    let rules =
        crate::transport::platform_rules(&crate::transport::host_rules(&core.profile), &platform);
    let mut pending_segments = Vec::new();
    let mut resolved = HashMap::new();
    for candidate in &plan.candidates {
        let media_id = items
            .iter()
            .find(|(_, _, _, att, _)| att == &candidate.resource.source_attachment_id)
            .map(|(mid, _, _, _, _)| mid.clone());
        let Some(media_id) = media_id else { continue };
        // 已有变体则跳过：Outbox 重放不重下（C07）。
        let saved: Option<(String, String, bool)> = {
            let mid = media_id.clone();
            let representation = candidate.representation.clone();
            let direct = job.payload["source_request_id"].is_string();
            let claimed = if direct {
                crate::source_acquisition::offer_spec(&candidate.claimed_spec)
            } else {
                acquisition_spec(&candidate.claimed_spec)
            };
            let auth = context.auth_state.as_str();
            let credential = context.credential_id.clone();
            let generation = context.credential_generation;
            let work_id = job.job_id.clone();
            let work_generation = job.generation;
            core.db.read(move |c| {
                // A legacy committed operation can be resumed without creating
                // a duplicate variant. Its missing acquisition proof stays unknown;
                // it cannot satisfy a newly authenticated consumer.
                let proof="EXISTS(SELECT 1 FROM resource_acquisitions a JOIN acquisition_contexts x ON x.context_id=a.context_id WHERE a.variant_id=mv.variant_id AND x.auth_state=?3 AND x.credential_id IS ?4 AND x.credential_generation IS ?5)";
                let own="EXISTS(SELECT 1 FROM resource_acquisitions a JOIN acquisition_contexts x ON x.context_id=a.context_id WHERE a.variant_id=mv.variant_id AND x.job_id=?6 AND x.job_generation=?7 AND x.auth_state=?3 AND x.credential_id IS ?4 AND x.credential_generation IS ?5)";
                let sql=format!("SELECT mv.variant_id,mv.blob_id,mv.claimed_spec,{proof} FROM media_variants mv WHERE mv.media_id=?1 AND mv.source_representation=?2 AND (mv.approval_state IN ('automatic','accepted','kept') OR (mv.approval_state='pending' AND {own})) AND NOT EXISTS(SELECT 1 FROM deleted_variants d WHERE d.variant_id=mv.variant_id) AND ({proof} OR (?3='anonymous' AND NOT EXISTS(SELECT 1 FROM resource_acquisitions a JOIN acquisition_contexts x ON x.context_id=a.context_id WHERE a.variant_id=mv.variant_id AND x.auth_state!='unknown') AND EXISTS(SELECT 1 FROM materializations mz WHERE mz.job_id=?6 AND mz.generation=?7 AND mz.subject_id=mv.media_id AND mz.blob_id=mv.blob_id AND mz.stage='COMMITTED'))) AND EXISTS(SELECT 1 FROM replicas r WHERE r.blob_id=mv.blob_id AND r.state='verified') ORDER BY mv.created_at DESC");
                let rows=c.prepare(&sql)?.query_map(rusqlite::params![mid,representation,auth,credential,generation,work_id,work_generation],|r|Ok((r.get::<_,String>(0)?,r.get::<_,String>(1)?,r.get::<_,String>(2)?,r.get::<_,bool>(3)?)))?.collect::<Result<Vec<_>,_>>()?;
                Ok(rows.into_iter().find(|(_,_,spec,_)|serde_json::from_str::<Value>(spec).is_ok_and(|v|if direct {crate::source_acquisition::offer_spec(&v)} else {acquisition_spec(&v)}==claimed)).map(|(variant,blob,_,proof)|(variant,blob,proof)))
            }).await?
        };
        if let Some((variant, blob, same_provenance)) = saved {
            let sha = crate::paths::blob_sha(&blob)?.to_owned();
            if job.payload["source_request_id"].is_string()
                || quality_requirement.strict
                    && matches!(
                        candidate.resource.role.as_str(),
                        "video" | "motion" | "video_segment"
                    )
            {
                let measured = core
                    .media_backend
                    .probe(&core.objects.object_path(&sha)?, &core.cancel)
                    .await?;
                if job.payload["source_request_id"].is_string() {
                    crate::source_acquisition::validate_measured(
                        candidate,
                        &serde_json::to_value(&measured)?,
                    )?;
                }
                crate::quality::enforce_measured(
                    core,
                    job,
                    &candidate.resource.role,
                    &serde_json::to_value(measured)?,
                )
                .await?;
            }
            if verify_candidate_files(core, candidate, std::slice::from_ref(&sha))
                .await
                .is_ok()
            {
                resolved.insert(candidate.resource.source_attachment_id.clone(), sha);
                crate::upgrades::reused(core, job, &media_id, &variant, same_provenance).await?;
                continue;
            }
        }
        let original_op = owned_acquisition_operation(core, job, &media_id, candidate).await?;
        let op_id = uncommitted_operation(core, &original_op).await?;
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
    if job.payload["upgrade_candidate_id"].is_string() {
        crate::upgrades::finish(core, job).await?;
        return Ok(());
    }
    let rev = revision_id.clone();
    let g = group.clone();
    let missing: i64 = core.db.read(move |c| Ok(c.query_row("SELECT count(*) FROM media_items mi WHERE revision_id=?1 AND (group_key=?2 OR (group_key IS NULL AND source_attachment_id=?2)) AND required=1 AND NOT EXISTS(SELECT 1 FROM media_variants mv JOIN replicas r ON r.blob_id=mv.blob_id AND r.state='verified' WHERE mv.media_id=mi.media_id)",rusqlite::params![rev,g],|r|r.get(0))?)).await?;
    if missing > 0 {
        return Err(CoreError::Network(
            "required media missing from resolution".into(),
        ));
    }
    let rev = revision_id.clone();
    let guard = job.clone();
    core.db
        .write(move |tx| {
            guard.check(tx)?;
            crate::quality::evaluate(tx, &rev, preference, wait)
        })
        .await?;
    create_playback_mux(core, job, &revision_id, &group).await?;
    Ok(())
}

/// URLs may rotate without changing an operation, but a new source specification
/// or integrity claim must never relabel an older PREPARED object on recovery.
fn acquisition_operation(
    media: &str,
    job_id: &str,
    generation: i64,
    candidate: &trace_contracts::plugin::MediaCandidate,
) -> OperationId {
    OperationId::new(format!(
        "mz-{media}-{}-{}-{}",
        job_id,
        generation,
        acquisition_digest(candidate)
    ))
}

fn acquisition_digest(candidate: &trace_contracts::plugin::MediaCandidate) -> String {
    let identity = serde_json::json!({"representation":candidate.representation,"spec":acquisition_spec(&candidate.claimed_spec),"size":candidate.expected_size,"sha256":candidate.expected_sha256});
    crate::config::short_hash(&identity.to_string())
}

async fn owned_acquisition_operation(
    core: &Core,
    job: &ClaimedJob,
    media: &str,
    candidate: &trace_contracts::plugin::MediaCandidate,
) -> Result<OperationId, CoreError> {
    let modern = acquisition_operation(media, &job.job_id, job.generation, candidate);
    let legacy = OperationId::new(format!(
        "mz-{media}-{}-{}",
        job.generation,
        acquisition_digest(candidate)
    ));
    let (current, previous, owner, subject, generation) = (
        modern.as_str().to_owned(),
        legacy.as_str().to_owned(),
        job.job_id.clone(),
        media.to_owned(),
        job.generation,
    );
    // Schema-5 prepared operations remain resumable by their original owner.
    // The old spelling is never an alias for another logical task's bytes.
    let use_legacy=core.db.read(move|c|Ok(c.query_row("SELECT NOT EXISTS(SELECT 1 FROM materializations WHERE operation_id=?1) AND EXISTS(SELECT 1 FROM materializations WHERE operation_id=?2 AND job_id=?3 AND subject_id=?4 AND generation=?5)",rusqlite::params![current,previous,owner,subject,generation],|r|r.get::<_,bool>(0))?)).await?;
    Ok(if use_legacy { legacy } else { modern })
}

pub(crate) fn acquisition_spec(v: &Value) -> Value {
    let keys = [
        "width",
        "height",
        "fps",
        "frame_rate",
        "codec",
        "codecs",
        "bit_rate",
        "bitrate",
        "quality",
        "audio_quality",
        "sample_rate",
        "channels",
    ];
    Value::Object(
        keys.into_iter()
            .filter_map(|key| {
                v.get(key)
                    .filter(|value| !value.is_null())
                    .map(|value| (key.to_owned(), value.clone()))
            })
            .collect(),
    )
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
    let path = core.objects.object_path(sha)?;
    let measured = core.media_backend.probe(&path, &core.cancel).await;
    if matches!(candidate.kind.as_str(), "audio" | "image" | "cover") {
        let spec = measured.as_ref().map_err(|_| {
            CoreError::InvalidInput("resource structure could not be verified".into())
        })?;
        let valid = if candidate.kind == "audio" {
            spec.audio_tracks.is_some_and(|n| n > 0)
        } else {
            spec.width.is_some_and(|n| n > 0) && spec.height.is_some_and(|n| n > 0)
        };
        if !valid {
            return Err(CoreError::InvalidInput(
                "required resource tracks missing".into(),
            ));
        }
    }
    if job.payload["source_request_id"].is_string() {
        crate::source_acquisition::validate_measured(
            candidate,
            &measured
                .as_ref()
                .ok()
                .and_then(|v| serde_json::to_value(v).ok())
                .unwrap_or_default(),
        )?;
    }
    crate::quality::enforce_measured(
        core,
        job,
        &candidate.resource.role,
        &measured
            .as_ref()
            .ok()
            .and_then(|v| serde_json::to_value(v).ok())
            .unwrap_or_default(),
    )
    .await?;
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
            measured
                .as_ref()
                .ok()
                .and_then(|spec| serde_json::to_value(spec).ok()),
        )
        .await?;
    if disposition == CommitDisposition::Superseded {
        return Ok(());
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

/// A repaired/rebuilt object gets a new receipt while preserving committed history.
async fn uncommitted_operation(
    core: &Core,
    original: &OperationId,
) -> Result<OperationId, CoreError> {
    let mut operation = original.clone();
    for repair in 1..=32 {
        let id = operation.as_str().to_owned();
        let committed:bool=core.db.read(move|c|Ok(c.query_row("SELECT EXISTS(SELECT 1 FROM materializations WHERE operation_id=?1 AND stage='COMMITTED')",[id],|r|r.get(0))?)).await?;
        if !committed {
            return Ok(operation);
        }
        operation = OperationId::new(format!("{}-repair-{repair}", original.as_str()));
    }
    Err(CoreError::InvalidInput(
        "materialization repair budget".into(),
    ))
}

/// Optional playback derivative. Original tracks remain referenced and unchanged.
pub(crate) async fn create_playback_mux(
    core: &Core,
    job: &ClaimedJob,
    revision: &str,
    group: &str,
) -> Result<(), CoreError> {
    if core.media_backend.ffmpeg.is_none() {
        return Ok(());
    }
    let _cache = core.cache_build.lock().await;
    if !core
        .db
        .read(|c| crate::cache::derived_allowed(c, 0))
        .await?
    {
        return Ok(());
    }
    let r = revision.to_owned();
    let suppressed:bool=core.db.read(move|c|Ok(c.query_row("SELECT e.cache_suppressed!=0 OR e.media_suppressed!=0 OR e.lifecycle='deleted' FROM entries e JOIN entry_revisions r ON r.entry_id=e.entry_id WHERE r.revision_id=?1",[r],|r|r.get(0))?)).await?;
    if suppressed {
        return Ok(());
    }
    let rev = revision.to_owned();
    let group = group.to_owned();
    let tracks:Vec<(String,String,String)>=core.db.read(move |c|Ok(c.prepare("SELECT mi.media_id,mi.role,mv.blob_id FROM media_items mi JOIN asset_selections selected ON selected.media_id=mi.media_id JOIN media_variants mv ON mv.variant_id=selected.variant_id JOIN replicas rp ON rp.blob_id=mv.blob_id AND rp.state='verified' WHERE mi.revision_id=?1 AND mi.group_key=?2 AND mi.role IN ('video','audio') AND mv.measured_spec IS NOT NULL AND mv.approval_state IN ('automatic','accepted','kept') AND mv.source_representation!='playback-mux-v1' ORDER BY mi.role DESC")?.query_map(rusqlite::params![rev,group],|r|Ok((r.get(0)?,r.get(1)?,r.get(2)?)))?.collect::<Result<_,_>>()?)).await?;
    let Some(video) = tracks.iter().find(|t| t.1 == "video") else {
        return Ok(());
    };
    let Some(audio) = tracks.iter().find(|t| t.1 == "audio") else {
        return Ok(());
    };
    let mid = video.0.clone();
    let source_pair = serde_json::json!({"derived_from":[video.2,audio.2]});
    let pair = source_pair.to_string();
    let exists:bool=core.db.read(move |c|Ok(c.query_row("SELECT EXISTS(SELECT 1 FROM media_variants WHERE media_id=?1 AND source_representation='playback-mux-v1' AND claimed_spec=?2 AND EXISTS(SELECT 1 FROM replicas rp WHERE rp.blob_id=media_variants.blob_id AND rp.state='verified'))",rusqlite::params![mid,pair],|r|r.get(0))?)).await?;
    if exists {
        let mid = video.0.clone();
        let pair = source_pair.to_string();
        let guard = job.clone();
        core.db.write(move|tx|{
            guard.check(tx)?;
            if crate::cache::derived_allowed(tx,0)? {
                tx.execute("UPDATE variant_retention SET retained=1,eligible_at=NULL,reason='cache_reused' WHERE variant_id IN (SELECT variant_id FROM media_variants WHERE media_id=?1 AND source_representation='playback-mux-v1' AND claimed_spec=?2) AND reason='cache_cleared'",rusqlite::params![mid,pair])?;
            }
            Ok(())
        }).await?;
        return Ok(());
    }
    use sha2::Digest;
    let pair_digest = sha2::Sha256::digest(source_pair.to_string().as_bytes())
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect::<String>();
    let original_op = OperationId::new(format!(
        "mz-{}-playback-{}-{}",
        video.0,
        job.generation,
        &pair_digest[..16]
    ));
    let op = uncommitted_operation(core, &original_op).await?;
    let info = CommitInfo {
        job: Some(job.clone()),
        media_id: video.0.clone(),
        source_representation: "playback-mux-v1".into(),
        claimed_spec: source_pair,
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
    let estimate = inputs.iter().try_fold(1024 * 1024_i64, |total, path| {
        std::fs::metadata(path).map(|m| total.saturating_add((m.len() as i64).saturating_mul(2)))
    })?;
    if !core
        .db
        .read(move |c| crate::cache::derived_allowed(c, estimate))
        .await?
    {
        return Ok(());
    }
    core.objects.ensure_dirs()?;
    let output = core
        .profile
        .staging_dir()
        .join(format!("{}.mp4", op.as_str()));
    core.media_backend
        .mux_stream_copy(&inputs, output.clone(), &core.cancel)
        .await?;
    let size = output.metadata()?.len() as i64;
    if !core
        .db
        .read(move |c| crate::cache::derived_allowed(c, size))
        .await?
    {
        std::fs::remove_file(&output)?;
        return Ok(());
    }
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
        "image" | "cover" => "image/*",
        "audio" => "audio/*",
        "subtitle_json" => "application/vnd.trace.subtitle+json",
        "dash_audio" => "audio/mp4",
        "image_jpeg" => "image/jpeg",
        "image_png" => "image/png",
        "durl_segment" => "video/x-flv",
        _ => "video/mp4",
    }
}
