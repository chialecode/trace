//! Core：进程内服务总线 + 调度器（docs/design.md §9）。
//!
//! - 打开：迁移 → 恢复扫描（旧租约、物化矩阵、副本验证）→ 启动调度。
//! - 调度：持久任务按到期时间认领；预算（元数据节流、媒体全局/单账号并发）按
//!   windows-runtime-contracts §6 执行；暂停期间不认领新任务。
//! - 任务怎样执行见 `pipeline`，元数据节流见 `pacing`。

use crate::config::Profile;
use crate::db::{DbActor, DbHandle};
use crate::error::CoreError;
use crate::events::EventHub;
use crate::jobs::{self, ClaimedJob};
use crate::media::MediaBackend;
use crate::network::NetworkState;
use crate::objects::ObjectStore;
use crate::pluginhost::{AdapterSpec, CredentialLookup, PluginHost, default_adapter_specs};
use crate::search;
use crate::transport::ConstrainedClient;
use crate::vault::Vault;
use serde_json::Value;
use std::collections::HashMap;
use std::sync::atomic::{AtomicBool, AtomicU64, AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};
use tokio_util::sync::CancellationToken;
use trace_domain::ids::{InstanceId, NodeId};
use trace_domain::time::{TimePrecision, TraceTime};

#[cfg(test)]
use {
    crate::objects::{CommitInfo, ReceiveParams},
    crate::pipeline::{
        acquire::{acquisition_operation, commit_candidate},
        execute,
        media::run_media,
        mux::create_playback_mux,
        observe::run_observe,
        scan::run_scan,
        scan_page::{ScanPage, store_page},
    },
    trace_domain::ids::OperationId,
};

/// Stores a scanned page the way the scan job does, for tests that start from a page.
#[cfg(test)]
#[allow(clippy::too_many_arguments)]
pub(crate) async fn process_scan_page(
    core: &Core,
    job: &ClaimedJob,
    namespace: &str,
    subscription_id: &str,
    mode: &str,
    items: Vec<trace_contracts::plugin::EnumeratedEntry>,
    cursor: Option<String>,
    coverage: &Value,
) -> Result<i64, CoreError> {
    let page = ScanPage {
        namespace,
        subscription_id,
        mode,
        items,
        cursor,
        coverage,
        range: &crate::scan_range::Range {
            start: None,
            end: None,
        },
    };
    let stored = store_page(core, job, page).await?;
    Ok(stored.added + stored.unplaced)
}

#[cfg(test)]
#[path = "gallery_tests.rs"]
mod gallery_tests;
#[cfg(test)]
#[path = "session_media_tests.rs"]
mod session_media_tests;
#[cfg(test)]
#[path = "archive_behavior_tests.rs"]
mod tests;
#[cfg(test)]
#[path = "undated_range_tests.rs"]
mod undated_range_tests;

#[cfg(test)]
#[path = "acquisition_history_tests.rs"]
mod acquisition_history_tests;
#[cfg(test)]
#[path = "job_lifecycle_tests.rs"]
mod job_lifecycle_tests;

#[cfg(test)]
static TEST_CAPABILITY_RESULT: std::sync::LazyLock<Mutex<Option<Value>>> =
    std::sync::LazyLock::new(|| Mutex::new(None));

/// A unit test can stand in for the plugin process; production always calls the plugin.
#[cfg(test)]
pub(crate) fn set_test_capability_result(value: Value) {
    *TEST_CAPABILITY_RESULT.lock().unwrap() = Some(value);
}

#[cfg(test)]
fn take_test_capability_result() -> Option<Value> {
    TEST_CAPABILITY_RESULT.lock().unwrap().take()
}

/// Scheduler tick and how many jobs one tick may start.
const SCHEDULER_TICK: Duration = Duration::from_millis(500);
const CLAIMS_PER_TICK: usize = 4;
const MAX_RUNNING_TASKS: usize = 32;
const SEARCH_INDEX_BATCH: i64 = 100;
const REPLICA_VERIFY_BATCH: u32 = 64;
const DB_QUEUE_DEPTH: usize = 512;
const SHUTDOWN_DEADLINE: Duration = Duration::from_secs(8);

pub(crate) type MetadataTurn = Arc<tokio::sync::Mutex<()>>;

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
    pub network: Arc<NetworkState>,
    pub media_backend: MediaBackend,
    pub media_tools: crate::media_tools::MediaTools,
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
    /// Held while a library move runs; only one runs at a time.
    pub(crate) library_run: tokio::sync::Mutex<()>,
    /// Serializes writes into download folders and their checks.
    pub(crate) downloads_run: tokio::sync::Mutex<()>,
    /// Serializes activation, rollback and removal of plugin releases.
    pub(crate) plugin_lifecycle: tokio::sync::Mutex<()>,
    pub(crate) active_jobs: Mutex<HashMap<String, usize>>,
    tasks: tokio_util::task::TaskTracker,
    shutdown_lock: tokio::sync::Mutex<()>,
    account_media: Mutex<HashMap<String, Arc<tokio::sync::Semaphore>>>,
    pub leases: Mutex<HashMap<String, MediaLease>>,
    pub cookie_cache: Arc<Mutex<HashMap<String, String>>>,
    /// A sign-in proof from the last two minutes is reused by the jobs of the same batch.
    pub(crate) auth_proofs: crate::auth_proofs::AuthProofs,
    pub(crate) metadata_last_request: Mutex<HashMap<String, Instant>>,
    pub(crate) metadata_urgent: Mutex<HashMap<String, usize>>,
    pub(crate) metadata_regular: Mutex<HashMap<String, usize>>,
    pub(crate) metadata_streak: Mutex<HashMap<String, usize>>,
    pub(crate) metadata_turns: Mutex<HashMap<String, MetadataTurn>>,
    _db_actor: Arc<DbActor>,
}

/// HTTP client for site and media traffic, limited by the profile's acquisition budget.
fn build_http(
    profile: &Profile,
    network: &Arc<NetworkState>,
) -> Result<Arc<ConstrainedClient>, CoreError> {
    let mut http = ConstrainedClient::build()?.with_network(network.clone());
    if let Some(limits) = &profile.acquisition_limits {
        http = http.with_budget(Arc::new(crate::request_budget::RequestBudget::acquisition(
            limits,
            profile.data_root.join("state/acquisition-budget.json"),
        )?));
    }
    Ok(Arc::new(http))
}

/// Plugin host whose credential lookup reads the short-lived in-memory Cookie cache
/// (vault contents are decrypted into it only on import or validation).
fn build_host(
    profile: &Profile,
    http: &Arc<ConstrainedClient>,
    cookies: &Arc<Mutex<HashMap<String, String>>>,
    specs: Option<Vec<AdapterSpec>>,
) -> (Profile, PluginHost) {
    let lookup_cache = cookies.clone();
    let credential_lookup: CredentialLookup =
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
    (prof, host)
}

impl Core {
    /// The profile with the library root in use now (it can differ from `profile` after a
    /// move of the library).
    pub(crate) fn live_profile(&self) -> Profile {
        Profile {
            archive_root: self.objects.archive_root(),
            ..self.profile.clone()
        }
    }

    pub async fn open(profile: Profile) -> Result<Arc<Self>, CoreError> {
        Self::open_impl(profile, None).await
    }

    #[cfg(debug_assertions)]
    pub async fn open_with_adapters(
        profile: Profile,
        specs: Vec<AdapterSpec>,
    ) -> Result<Arc<Self>, CoreError> {
        Self::open_impl(profile, Some(specs)).await
    }

    async fn open_impl(
        profile: Profile,
        specs: Option<Vec<AdapterSpec>>,
    ) -> Result<Arc<Self>, CoreError> {
        let core = Self::assemble(profile, specs).await?;
        core.register_instance().await?;
        crate::library::register(&core).await?;
        core.restore_disabled_plugins().await?;
        crate::plugin_install::reconcile_removals(&core).await?;
        crate::plugin_install::restore_active(&core).await?;
        crate::tool_update::restore(&core).await?;
        core.seed_quality_policy().await?;
        crate::app::network::load(&core).await?;
        Ok(core)
    }

    async fn assemble(
        mut profile: Profile,
        specs: Option<Vec<AdapterSpec>>,
    ) -> Result<Arc<Self>, CoreError> {
        let archive = profile.archive()?;
        let db_path = profile.db_path();
        if let Some(parent) = db_path.parent() {
            std::fs::create_dir_all(parent)?;
        }
        let conn = rusqlite::Connection::open(&db_path)?;
        // A library move survives restarts: the root in use is the one recorded in the database.
        profile.archive_root = crate::library::resolve_root(&conn, &profile.archive_root);
        std::fs::create_dir_all(&profile.archive_root)?;
        let (db, actor) = DbActor::start(conn, DB_QUEUE_DEPTH)?;
        let vault_root = profile
            .vault_root()
            .ok_or_else(|| CoreError::Internal("no vault location".into()))?;
        let vault = Vault::new(vault_root)?;
        let network = Arc::new(NetworkState::default());
        let http = build_http(&profile, &network)?;
        let cookie_cache = Arc::new(Mutex::new(HashMap::new()));
        let (prof, host) = build_host(&profile, &http, &cookie_cache, specs);
        let media_backend = MediaBackend::detect(&prof).await;
        let media_tools = crate::media_tools::MediaTools::detect(&prof).await;
        Ok(Arc::new(Self {
            db,
            objects: ObjectStore::new(profile),
            vault,
            hub: Arc::new(EventHub::new()),
            host,
            http,
            network,
            media_backend,
            media_tools,
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
            library_run: tokio::sync::Mutex::new(()),
            downloads_run: tokio::sync::Mutex::new(()),
            plugin_lifecycle: tokio::sync::Mutex::new(()),
            active_jobs: Mutex::new(HashMap::new()),
            shutdown_lock: tokio::sync::Mutex::new(()),
            leases: Mutex::new(HashMap::new()),
            cookie_cache,
            auth_proofs: crate::auth_proofs::AuthProofs::new(Duration::from_secs(120)),
            metadata_last_request: Mutex::new(HashMap::new()),
            metadata_urgent: Mutex::new(HashMap::new()),
            metadata_regular: Mutex::new(HashMap::new()),
            metadata_streak: Mutex::new(HashMap::new()),
            metadata_turns: Mutex::new(HashMap::new()),
            _db_actor: Arc::new(actor),
            profile: prof,
        }))
    }

    /// Records which archive, node and instance own the database now.
    async fn register_instance(&self) -> Result<(), CoreError> {
        let archive_id = self.profile.archive()?.as_str().to_string();
        let node = self.node_id.as_str().to_string();
        let instance = self.instance_id.as_str().to_string();
        self.db
            .write(move |tx| {
                for (key, value) in [
                    ("archive_id", archive_id),
                    ("node_id", node),
                    ("last_instance", instance),
                ] {
                    tx.execute(
                        "INSERT INTO meta(key,value) VALUES(?1,?2)
                         ON CONFLICT(key) DO UPDATE SET value=excluded.value",
                        [key, value.as_str()],
                    )?;
                }
                Ok(())
            })
            .await
    }

    async fn restore_disabled_plugins(&self) -> Result<(), CoreError> {
        let disabled: Vec<String> = self
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
            self.host.set_enabled(&platform, false);
        }
        Ok(())
    }

    async fn seed_quality_policy(&self) -> Result<(), CoreError> {
        let policy = serde_json::json!({
            "prefer_fps": self.profile.quality_prefer_fps,
            "wait_seconds": self.profile.recycle_wait_seconds.max(0),
            "version": "m1-v1",
        })
        .to_string();
        self.db
            .write(move |tx| {
                tx.execute(
                    "INSERT OR IGNORE INTO meta(key,value) VALUES('quality_policy',?1)",
                    [policy],
                )?;
                Ok(())
            })
            .await
    }

    /// 启动恢复：旧实例租约回收 + 物化恢复矩阵 + 副本验证（有界）。
    pub async fn recover(&self) -> Result<serde_json::Value, CoreError> {
        crate::scan_range::ensure_deep_lanes(&self.db).await?;
        let stale = jobs::recover_stale_leases(&self.db, self.instance_id.as_str()).await?;
        let mat = self.objects.recover_materializations(&self.db).await?;
        let recordings = crate::recording::recover_closed(self).await?;
        let broken = self
            .objects
            .verify_replicas(&self.db, REPLICA_VERIFY_BATCH)
            .await?;
        self.db.write(crate::upgrades::invalidate_stale).await?;
        Ok(serde_json::json!({
            "stale_leases_requeued": stale,
            "materializations": mat,
            "recording_fragments": recordings,
            "broken_replicas": broken,
        }))
    }

    /// Runs a background task that shutdown waits for.
    pub(crate) fn spawn_task<F>(&self, task: F)
    where
        F: std::future::Future<Output = ()> + Send + 'static,
    {
        self.tasks.spawn(task);
    }

    /// 启动调度循环（daemon 中 spawn）。
    pub fn start_scheduler(self: &Arc<Self>) {
        let moves = self.clone();
        self.tasks
            .spawn(async move { crate::library::resume_open(&moves).await });
        let legacy = self.clone();
        self.tasks.spawn(async move {
            // Entries kept before the unified fields existed get theirs from what is stored.
            let _ = crate::legacy_metadata::backfill(&legacy).await;
        });
        let catalog = self.clone();
        self.tasks.spawn(async move {
            // The supported-sites page reads what adapters declared; ask them once at start.
            let _ = crate::catalog::refresh(&catalog).await;
        });
        let core = self.clone();
        self.tasks.spawn(async move {
            if core
                .db
                .write(|tx| crate::quality::schedule(tx, false))
                .await
                .is_err()
            {
                return;
            }
            let mut tick = tokio::time::interval(SCHEDULER_TICK);
            loop {
                tokio::select! {
                    _ = tick.tick() => {
                        if core.cancel.is_cancelled() {
                            break;
                        }
                        if core.paused.load(Ordering::SeqCst) {
                            continue;
                        }
                        core.start_due_jobs().await;
                        // 派生：搜索索引随 tick 有界推进。
                        let _ = search::index_pending(&core.db, SEARCH_INDEX_BATCH).await;
                        core.host.reap_idle().await;
                    }
                    _ = core.cancel.cancelled() => break,
                }
            }
        });
    }

    async fn start_due_jobs(self: &Arc<Self>) {
        let room = MAX_RUNNING_TASKS.saturating_sub(self.tasks.len());
        let limit = CLAIMS_PER_TICK.min(room) as i32;
        let Ok(claimed) = jobs::claim_due(&self.db, self.instance_id.as_str(), limit).await else {
            return;
        };
        for job in claimed {
            let core = self.clone();
            self.tasks
                .spawn(async move { crate::pipeline::execute(core, job).await });
        }
    }

    /// 单账号媒体并发：每账号一个许可。
    pub(crate) async fn account_semaphore(&self, account_id: &str) -> Arc<tokio::sync::Semaphore> {
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
        self.auth_proofs.forget(platform);
        self.host.session_jar().forget(platform);
        self.cookie_cache
            .lock()
            .unwrap()
            .retain(|key, _| key != platform && !key.starts_with(&format!("{platform}/")));
    }

    /// Calls the adapter on behalf of the job. The job's credential reaches the adapter
    /// only through the host's scoped Cookie cache; a stale lease aborts before and after.
    pub async fn adapter_call(
        &self,
        job: &ClaimedJob,
        platform: &str,
        method: &str,
        params: Value,
        deadline: Option<u64>,
    ) -> Result<Value, CoreError> {
        let credential_scope = self.load_credential_scope(job, platform).await?;
        let guard = job.clone();
        self.db.read(move |c| guard.check(c)).await?;
        let result = self
            .host
            .call_scoped_with_evidence(
                platform,
                credential_scope.as_deref(),
                method,
                params,
                deadline,
                job.payload["acquisition_context_id"].as_str(),
            )
            .await;
        let guard = job.clone();
        self.db.read(move |c| guard.check(c)).await?;
        result
    }

    /// Calls one method of an optional extension. The plugin must have declared the capability
    /// kind in its installed manifest, the kind's switch must be on, and the method, parameters
    /// and result keys must match the contract; nothing undeclared reaches the plugin.
    pub async fn capability_call(
        &self,
        plugin_id: &str,
        kind: &str,
        method: &str,
        params: Value,
    ) -> Result<Value, CoreError> {
        let release = self
            .db
            .read({
                let plugin = plugin_id.to_owned();
                move |c| crate::store::plugin_releases::active(c, &plugin)
            })
            .await?
            .ok_or_else(|| CoreError::Unsupported(format!("installed plugin {plugin_id}")))?;
        let declared = release["capabilities"]
            .as_array()
            .is_some_and(|items| items.iter().any(|v| v.as_str() == Some(kind)));
        if !declared {
            return Err(CoreError::Unsupported(format!(
                "capability {kind} of {plugin_id}"
            )));
        }
        if !crate::app::plugins::capability_enabled(self, plugin_id, kind).await? {
            return Err(CoreError::Unsupported(format!(
                "capability {kind} is switched off"
            )));
        }
        let spec = trace_contracts::capability::validate(kind, method, &params)
            .map_err(CoreError::InvalidInput)?;
        let platform = release["manifest"]["platform"]
            .as_str()
            .ok_or_else(|| CoreError::Internal("plugin manifest".into()))?
            .to_owned();
        #[cfg(test)]
        let result = match take_test_capability_result() {
            Some(result) => result,
            None => {
                self.host
                    .call(
                        &platform,
                        &format!("capability.{kind}.{method}"),
                        params,
                        Some(spec.timeout_ms),
                    )
                    .await?
            }
        };
        #[cfg(not(test))]
        let result = self
            .host
            .call(
                &platform,
                &format!("capability.{kind}.{method}"),
                params,
                Some(spec.timeout_ms),
            )
            .await?;
        if result.to_string().len() > spec.max_bytes {
            return Err(CoreError::InvalidInput(
                "capability result exceeds the contract".into(),
            ));
        }
        trace_contracts::capability::validate_result(kind, method, &result)
            .map_err(CoreError::InvalidInput)?;
        Ok(result)
    }

    /// A notification is best-effort: a missing or failing extension is recorded and never
    /// changes the outcome of the work that raised the event.
    pub async fn notify(
        &self,
        plugin_id: &str,
        event: &str,
        title: &str,
        summary: &str,
        count: i64,
    ) -> bool {
        let params = serde_json::json!({
            "event": event,
            "title": title.chars().take(200).collect::<String>(),
            "summary": summary.chars().take(500).collect::<String>(),
            "count": count,
        });
        match self
            .capability_call(plugin_id, "notification", "send", params)
            .await
        {
            Ok(result) if result["delivered"] == serde_json::json!(true) => true,
            Ok(_) => {
                tracing::debug!(plugin_id, event, "notification extension did not deliver");
                false
            }
            Err(error) => {
                tracing::debug!(plugin_id, event, error = %error, "notification extension declined");
                false
            }
        }
    }

    /// The Cookie a media request to `url` carries for the job's credential scope: for a site
    /// whose own video addresses answer only the session that produced them, the credential
    /// with the session Cookies the site set; for every other address none.
    pub(crate) async fn media_cookie(
        &self,
        job: &ClaimedJob,
        rules: &crate::transport::HostRules,
        url: &str,
    ) -> Result<Option<String>, CoreError> {
        let Some(site) = rules
            .site
            .as_deref()
            .and_then(trace_domain::sites::site)
            .filter(|site| site.session_media)
        else {
            return Ok(None);
        };
        let scope = self.load_credential_scope(job, site.id).await?;
        let key = scope.map_or_else(|| site.id.to_owned(), |s| format!("{}/{s}", site.id));
        let credential = self.cookie_cache.lock().unwrap().get(&key).cloned();
        Ok(self
            .host
            .session_jar()
            .for_media(rules, (&key, credential.as_deref()), url))
    }

    /// Puts the subscription's credential into the Cookie cache and names its scope;
    /// without a credential the platform's anonymous cache entry is dropped.
    async fn load_credential_scope(
        &self,
        job: &ClaimedJob,
        platform: &str,
    ) -> Result<Option<String>, CoreError> {
        let (guard, p) = (job.clone(), platform.to_owned());
        let credential = self
            .db
            .read(move |c| jobs::scope::credential(c, &guard, &p))
            .await?;
        let Some((id, generation, reference, status)) = credential else {
            self.cookie_cache.lock().unwrap().remove(platform);
            return Ok(None);
        };
        if !matches!(status.as_str(), "valid" | "authenticated") {
            return Err(CoreError::AuthRequired(
                "subscription credential requires validation".into(),
            ));
        }
        let secret = String::from_utf8(self.vault.load(&reference)?)
            .map_err(|_| CoreError::AuthRequired("credential encoding".into()))?;
        let scope = format!("{id}/g{generation}");
        self.cache_cookie(&format!("{platform}/{scope}"), secret);
        Ok(Some(scope))
    }

    /// 优雅停机：停止调度 → 关闭适配器；DB 随 Core 关闭。
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
        if tokio::time::timeout(SHUTDOWN_DEADLINE, self.tasks.wait())
            .await
            .is_err()
        {
            tracing::warn!(
                "shutdown sealing deadline exceeded; durable materializations require recovery"
            );
        }
    }
}
