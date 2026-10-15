//! Profile、路径布局与端点命名。
//!
//! 布局沿用 docs/design.md §6.1；端点名由当前用户与 archive_id 派生
//! （windows-runtime-contracts §2），不暴露用户路径。凭证 vault 独立于档案路径。

use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::path::{Path, PathBuf};
use trace_domain::ids::ArchiveId;

pub const DEFAULT_CHECK_INTERVAL_SECONDS: i64 = 300;
/// 一轮枚举预算（windows-runtime-contracts §6）。
pub const DEFAULT_MAX_SCAN_PAGES: u32 = 10;
/// 元数据请求最小间隔（毫秒），加抖动。
pub const METADATA_MIN_INTERVAL_MS: u64 = 2000;
/// 媒体全局并发与单账号并发。
pub const MEDIA_CONCURRENCY_GLOBAL: usize = 2;
pub const MEDIA_CONCURRENCY_PER_ACCOUNT: usize = 1;
/// 连接/元数据超时。
pub const CONNECT_TIMEOUT_SECS: u64 = 10;
pub const METADATA_TOTAL_TIMEOUT_SECS: u64 = 30;
/// 传输检查点间隔（字节）。
pub const CHECKPOINT_INTERVAL_BYTES: u64 = 4 * 1024 * 1024;
/// 媒体读取租约时长。
pub const MEDIA_LEASE_TTL_SECS: u64 = 600;
/// 媒体分块读取上限（base64 后仍低于 1 MiB 帧）。
pub const MEDIA_CHUNK_MAX: i64 = 512 * 1024;
/// 磁盘安全余量。
pub const DISK_SAFETY_MARGIN_BYTES: i64 = 1024 * 1024 * 1024;

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(default)]
pub struct Profile {
    /// Optional per-Profile bounded acquisition run. Counts survive Core restart;
    /// this is a local network budget, never a permission to access a source.
    pub acquisition_limits: Option<AcquisitionLimits>,
    pub archive_id: String,
    /// 运行元数据（数据库、迁移、锁、备份临时）。
    pub data_root: PathBuf,
    /// 档案大文件根（objects/manifests/.staging/.trash）。
    pub archive_root: PathBuf,
    /// 凭证 vault 目录；缺省为系统私有目录，可显式覆盖（测试隔离）。
    pub vault_dir: Option<PathBuf>,
    pub adapter_dir: Option<PathBuf>,
    pub media_dir: Option<PathBuf>,
    /// Directory of the locked media tools (yt-dlp, Deno); packaged and development
    /// locations are found without it.
    pub tools_dir: Option<PathBuf>,
    pub allow_media_path: bool,
    /// Independent live slots; default supports the four-stream resource target.
    pub live_concurrency: usize,
    pub metadata_min_interval_ms: u64,
    pub metadata_jitter_ms: u64,
    pub scan_page_budget: u32,
    pub quality_prefer_fps: bool,
    pub recycle_wait_seconds: i64,
    pub manual_delete_wait_seconds: i64,
    /// Recording limits; rotation uses a decodable boundary and retains raw bytes.
    pub recording_fragment_seconds: u64,
    pub recording_fragment_bytes: u64,
    pub recording_disk_reserve_bytes: u64,
    /// 平台媒体传输允许域的额外扩展（默认内置保守清单）。
    pub extra_media_hosts: Vec<String>,
    /// 测试专用：显式允许的本地测试服务（仅测试构建生效，发行包默认拒绝）。
    pub test_allowed_hosts: Vec<String>,
}

impl Default for Profile {
    fn default() -> Self {
        Self {
            acquisition_limits: None,
            archive_id: "default".into(),
            data_root: PathBuf::from("profiles/default/data"),
            archive_root: PathBuf::from("profiles/default/archive"),
            vault_dir: None,
            adapter_dir: None,
            media_dir: None,
            tools_dir: None,
            allow_media_path: false,
            live_concurrency: 4,
            metadata_min_interval_ms: METADATA_MIN_INTERVAL_MS,
            metadata_jitter_ms: 250,
            scan_page_budget: DEFAULT_MAX_SCAN_PAGES,
            quality_prefer_fps: false,
            recycle_wait_seconds: 86400,
            manual_delete_wait_seconds: 7 * 86400,
            recording_fragment_seconds: 600,
            recording_fragment_bytes: 512 * 1024 * 1024,
            recording_disk_reserve_bytes: DISK_SAFETY_MARGIN_BYTES as u64,
            extra_media_hosts: Vec::new(),
            test_allowed_hosts: Vec::new(),
        }
    }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AcquisitionLimits {
    pub metadata_requests: usize,
    pub media_bytes: usize,
    pub interval_ms: u64,
}

impl Profile {
    pub fn metadata_interval_ms(&self) -> u64 {
        self.metadata_min_interval_ms
            .clamp(METADATA_MIN_INTERVAL_MS, 60000)
    }
    pub fn metadata_jitter_ms(&self) -> u64 {
        self.metadata_jitter_ms.min(1000)
    }
    pub fn scan_page_budget(&self) -> u32 {
        self.scan_page_budget.clamp(1, 100)
    }
    pub fn archive(&self) -> Result<ArchiveId, ConfigError> {
        ArchiveId::parse(self.archive_id.clone()).map_err(|_| ConfigError::InvalidArchiveId)
    }

    pub fn db_path(&self) -> PathBuf {
        self.data_root.join("state").join("catalog.sqlite3")
    }

    pub fn lock_path(&self) -> PathBuf {
        self.data_root.join("state").join("daemon.lock")
    }

    pub fn wal_snapshot_dir(&self) -> PathBuf {
        self.data_root.join("state").join("backups")
    }

    pub fn objects_dir(&self) -> PathBuf {
        self.archive_root.join("objects").join("sha256")
    }

    pub fn staging_dir(&self) -> PathBuf {
        self.archive_root.join(".staging")
    }

    pub fn trash_dir(&self) -> PathBuf {
        self.archive_root.join(".trash")
    }

    pub fn manifests_dir(&self) -> PathBuf {
        self.archive_root.join("manifests")
    }

    pub fn vault_root(&self) -> Option<PathBuf> {
        self.vault_dir.clone().or_else(|| {
            std::env::var_os("LOCALAPPDATA").map(|l| {
                Path::new(&l)
                    .join("trace")
                    .join("vault")
                    .join(&self.archive_id)
            })
        })
    }

    /// 控制管道名：`trace-ctrl-<archive 前 8 hex>-<user 前 8 hex>`。
    pub fn pipe_name(&self) -> String {
        format!(
            "\\\\.\\pipe\\trace-ctrl-{}-{}",
            short_hash(&self.archive_id),
            short_hash(&current_user())
        )
    }
}

pub fn jittered_check_seconds(seconds: i64) -> i64 {
    let seconds = seconds.clamp(5, 7 * 86400);
    let spread = (seconds / 20).max(1);
    (seconds + rand::random_range(-spread..=spread)).max(5)
}

#[derive(Debug, thiserror::Error)]
pub enum ConfigError {
    #[error("invalid archive id")]
    InvalidArchiveId,
    #[error("io error: {0}")]
    Io(#[from] std::io::Error),
}

pub fn short_hash(input: &str) -> String {
    let mut hasher = Sha256::new();
    hasher.update(input.as_bytes());
    let out = hasher.finalize();
    out[..4].iter().map(|b| format!("{b:02x}")).collect()
}

pub fn current_user() -> String {
    std::env::var("USERNAME").unwrap_or_else(|_| "unknown".into())
}

/// 从 JSON 文件加载 Profile。
pub fn load_profile(path: &Path) -> Result<Profile, ConfigError> {
    let raw = std::fs::read_to_string(path)?;
    Ok(serde_json::from_str(&raw)?)
}

#[derive(Debug, thiserror::Error)]
#[error("profile json error: {0}")]
pub struct ProfileJsonError(#[from] pub serde_json::Error);

impl From<serde_json::Error> for ConfigError {
    fn from(e: serde_json::Error) -> Self {
        // serde_json 错误与 io 错误分别呈现，避免掩盖来源。
        ConfigError::Io(std::io::Error::other(e.to_string()))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn pipe_name_derives_from_archive_and_user_without_paths() {
        let p = Profile::default();
        let name = p.pipe_name();
        assert!(name.starts_with("\\\\.\\pipe\\trace-ctrl-"));
        // 不暴露用户名/用户路径/盘符。
        assert!(
            !name.contains(&current_user()),
            "no username in endpoint: {name}"
        );
        assert!(!name.contains(':'), "no drive letter: {name}");
        assert_eq!(name.len(), "\\\\.\\pipe\\trace-ctrl-".len() + 8 + 1 + 8);
    }

    #[test]
    fn layout_separates_state_and_archive() {
        let p = Profile::default();
        assert!(p.db_path().starts_with(p.data_root.join("state")));
        assert!(p.objects_dir().starts_with(&p.archive_root));
        assert!(p.staging_dir().starts_with(&p.archive_root));
        // vault 与档案根不同位置。
        if let Some(v) = p.vault_root() {
            assert!(!v.starts_with(&p.archive_root));
        }
    }
}
