//! 统一文件库与文件提交状态机（docs/design.md §6.3/§6.4）。
//!
//! 文件系统与 SQLite 没有单一事务；每个阶段边界都持久化到
//! `materializations`，恢复矩阵 `recover_materializations` 保证可重入、
//! 不误报成功（INV-03/11，C03/C04/C06/C08）。

use crate::config::Profile;
use crate::db::DbHandle;
use crate::error::CoreError;
use sha2::{Digest, Sha256};
use std::io::{Read, Write};
use std::path::{Path, PathBuf};
use time::OffsetDateTime;
use trace_domain::ids::{BlobId, OperationId};
use trace_domain::states::MaterializationStage;
use trace_domain::time::{TimePrecision, TraceTime};

pub struct ObjectStore {
    /// The profile whose `archive_root` is the active library; changes only when a library
    /// move switches (see `library`).
    profile: std::sync::RwLock<Profile>,
    pub budget: std::sync::Arc<crate::storage_budget::StorageBudget>,
}

/// 接收操作参数。
#[derive(Debug, Clone)]
pub struct ReceiveParams {
    pub job_id: Option<String>,
    pub subject_kind: String,
    pub subject_id: String,
    pub generation: i64,
    pub mime: Option<String>,
    pub expected_size: Option<i64>,
    pub expected_sha256: Option<String>,
    /// 来源校验器（如 ETag / Content-Length），用于续传身份核验（C28）。
    pub source_validator: Option<String>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum InstallOutcome {
    Installed,
    /// 目标路径已有相同哈希对象：合法复用（INV-04）。
    ReusedExisting,
    /// 目标路径原对象损坏：移入 .trash 隔离后安装新对象。
    ReplacedCorrupt,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub enum CommitDisposition {
    #[default]
    Committed,
    Superseded,
}

/// 提交所需业务信息。
#[derive(Debug, Clone)]
pub struct CommitInfo {
    pub job: Option<crate::jobs::ClaimedJob>,
    pub media_id: String,
    pub source_representation: String,
    pub claimed_spec: serde_json::Value,
}

/// 恢复扫描摘要。
#[derive(Debug, Clone, Default, serde::Serialize)]
pub struct RecoverySummary {
    pub resumed_receiving: i64,
    pub recommitted: i64,
    pub awaiting_business_commit: i64,
    pub cleaned_staging: i64,
    pub quarantined: i64,
}

mod commit;
mod receive;
mod recovery;
mod staged;

pub use staged::StagedWriter;

const UPDATE_MATERIALIZATIONS_QUARANTINE: &str =
    "UPDATE materializations SET stage='QUARANTINED',updated_at=?2 WHERE operation_id=?1";

fn now() -> String {
    TraceTime::now(TimePrecision::Discovery).rfc3339
}

const SELECT_MATERIALIZATIONS: &str =
    "SELECT expected_size,expected_sha256 FROM materializations WHERE operation_id=?1";
const UPDATE_MATERIALIZATIONS: &str =
    "UPDATE materializations SET stage='OBJECT_READY', updated_at=?2 WHERE operation_id=?1";

impl ObjectStore {
    pub fn new(profile: Profile) -> Self {
        Self {
            profile: std::sync::RwLock::new(profile),
            budget: std::sync::Arc::default(),
        }
    }

    /// A snapshot of the profile with the active library root.
    pub fn profile(&self) -> Profile {
        self.profile.read().unwrap().clone()
    }

    pub fn archive_root(&self) -> PathBuf {
        self.profile.read().unwrap().archive_root.clone()
    }

    pub fn objects_dir(&self) -> PathBuf {
        self.profile.read().unwrap().objects_dir()
    }

    pub fn staging_dir(&self) -> PathBuf {
        self.profile.read().unwrap().staging_dir()
    }

    pub fn trash_dir(&self) -> PathBuf {
        self.profile.read().unwrap().trash_dir()
    }

    pub fn manifests_dir(&self) -> PathBuf {
        self.profile.read().unwrap().manifests_dir()
    }

    /// Makes `root` the library from now on. Callers hold the archive gate with no job
    /// running, so no operation straddles the change.
    pub(crate) fn set_archive_root(&self, root: PathBuf) {
        self.profile.write().unwrap().archive_root = root;
    }

    pub fn ensure_dirs(&self) -> std::io::Result<()> {
        let profile = self.profile();
        std::fs::create_dir_all(profile.objects_dir())?;
        std::fs::create_dir_all(profile.staging_dir())?;
        std::fs::create_dir_all(profile.trash_dir())?;
        std::fs::create_dir_all(profile.manifests_dir())?;
        std::fs::create_dir_all(profile.wal_snapshot_dir())?;
        Ok(())
    }

    pub fn object_path(&self, sha_hex: &str) -> Result<PathBuf, CoreError> {
        crate::paths::object(&self.objects_dir(), sha_hex)
    }

    pub async fn quarantine(&self, db: &DbHandle, op_id: &OperationId) -> Result<(), CoreError> {
        let op = op_id.as_str().to_owned();
        db.write(move |tx| {
            tx.execute(
                UPDATE_MATERIALIZATIONS_QUARANTINE,
                rusqlite::params![op, now()],
            )?;
            Ok(())
        })
        .await
    }

    pub fn cleanup_staging(&self, op_id: &OperationId) {
        let dir = self.staging_dir().join(op_id.as_str());
        if dir.exists() {
            let _ = std::fs::remove_dir_all(&dir);
        }
    }

    /// 受控读取：有界 Range 读取（C16），供媒体桥使用。
    pub fn read_range(
        &self,
        sha_hex: &str,
        offset: u64,
        length: u64,
    ) -> Result<Vec<u8>, CoreError> {
        if length > crate::config::MEDIA_CHUNK_MAX as u64 {
            return Err(CoreError::InvalidInput("read chunk too large".into()));
        }
        let path = self.object_path(sha_hex)?;
        let mut file = std::fs::File::open(&path).map_err(|e| match e.kind() {
            std::io::ErrorKind::NotFound => CoreError::NotFound(format!("blob {sha_hex}")),
            _ => CoreError::Io(e),
        })?;
        use std::io::Seek;
        file.seek(std::io::SeekFrom::Start(offset))?;
        let mut buf = vec![0u8; length as usize];
        let mut read_total = 0usize;
        loop {
            let n = file.read(&mut buf[read_total..])?;
            if n == 0 {
                buf.truncate(read_total);
                break;
            }
            read_total += n;
            if read_total == buf.len() {
                break;
            }
        }
        buf.truncate(read_total);
        Ok(buf)
    }

    pub fn blob_size(&self, sha_hex: &str) -> Result<u64, CoreError> {
        Ok(std::fs::metadata(self.object_path(sha_hex)?)?.len())
    }
}

pub fn outbox_emit(
    tx: &rusqlite::Transaction<'_>,
    topic: &str,
    aggregate_id: &str,
    payload: &serde_json::Value,
) -> Result<(), CoreError> {
    tx.execute(
        "INSERT INTO outbox (topic, aggregate_id, payload, created_at) VALUES (?1,?2,?3,?4)",
        rusqlite::params![topic, aggregate_id, payload.to_string(), now()],
    )?;
    Ok(())
}

pub fn hash_file(path: &Path) -> Result<String, CoreError> {
    let mut file = std::fs::File::open(path)?;
    let mut hasher = Sha256::new();
    let mut buf = vec![0u8; 256 * 1024];
    loop {
        let n = file.read(&mut buf)?;
        if n == 0 {
            break;
        }
        hasher.update(&buf[..n]);
    }
    Ok(hasher
        .finalize()
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect())
}

#[cfg(windows)]
fn fsync_dir_if_possible(dir: &Path) {
    use windows::Win32::Foundation::CloseHandle;
    use windows::Win32::Storage::FileSystem::*;
    use windows::core::PCWSTR;
    let mut wide: Vec<u16> = dir.as_os_str().to_string_lossy().encode_utf16().collect();
    wide.push(0);
    unsafe {
        if let Ok(h) = CreateFileW(
            PCWSTR(wide.as_ptr()),
            FILE_GENERIC_WRITE.0,
            FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
            None,
            OPEN_EXISTING,
            FILE_FLAG_BACKUP_SEMANTICS,
            None,
        ) {
            let _ = FlushFileBuffers(h);
            let _ = CloseHandle(h);
        }
    }
}

#[cfg(not(windows))]
fn fsync_dir_if_possible(dir: &Path) {
    if let Ok(f) = std::fs::File::open(dir) {
        let _ = f.sync_all();
    }
}

// MaterializationStage 引用保持领域状态机为正本（文档同步）。
const _: () = {
    let _ = MaterializationStage::default;
    let _ = BlobId::from_sha256_hex;
};

#[cfg(test)]
#[path = "recovery_tests.rs"]
mod recovery_tests;
