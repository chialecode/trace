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

/// 恢复扫描行：(operation_id, stage, staging_path, final_path, received_sha256)。
type RecoveryRow = (
    String,
    String,
    Option<String>,
    Option<String>,
    Option<String>,
);

pub struct ObjectStore {
    pub profile: Profile,
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

fn now() -> String {
    TraceTime::now(TimePrecision::Discovery).rfc3339
}

impl ObjectStore {
    pub fn new(profile: Profile) -> Self {
        Self { profile }
    }

    pub fn ensure_dirs(&self) -> std::io::Result<()> {
        std::fs::create_dir_all(self.profile.objects_dir())?;
        std::fs::create_dir_all(self.profile.staging_dir())?;
        std::fs::create_dir_all(self.profile.trash_dir())?;
        std::fs::create_dir_all(self.profile.manifests_dir())?;
        std::fs::create_dir_all(self.profile.wal_snapshot_dir())?;
        Ok(())
    }

    /// 阶段 1：登记接收，创建操作日志与 staging 文件。已存在同 id 的
    /// RECEIVING 记录时复用（可重入；断点续传从持久化检查点继续）。
    pub async fn start_receiving(
        &self,
        db: &DbHandle,
        params: ReceiveParams,
        op_id: &OperationId,
    ) -> Result<StagedWriter, CoreError> {
        self.ensure_dirs()?;
        let staging_dir = self.profile.staging_dir().join(op_id.as_str());
        let ts = now();
        let op = op_id.as_str().to_string();
        let job_id = params.job_id.clone();
        let subject_kind = params.subject_kind.clone();
        let subject_id = params.subject_id.clone();
        let generation = params.generation;
        let staging_str = staging_dir.to_string_lossy().to_string();
        let expected_size = params.expected_size;
        let expected_sha = params.expected_sha256.clone();
        let validator = params.source_validator.clone();
        let mime = params.mime.clone();
        let (existing_stage, existing_bytes): (Option<String>, i64) = db
            .write(move |tx| {
                tx.execute(
                    "INSERT OR IGNORE INTO materializations
                     (operation_id, job_id, subject_kind, subject_id, generation, stage,
                      staging_path, expected_size, expected_sha256, source_validator, mime,
                      created_at, updated_at)
                     VALUES (?1, ?2, ?3, ?4, ?5, 'RECEIVING', ?6, ?7, ?8, ?9, ?10, ?11, ?11)",
                    rusqlite::params![
                        op,
                        job_id,
                        subject_kind,
                        subject_id,
                        generation,
                        staging_str,
                        expected_size,
                        expected_sha,
                        validator,
                        mime,
                        ts,
                    ],
                )?;
                tx.execute("UPDATE materializations SET stage='RECEIVING',received_bytes=0,received_sha256=NULL,final_path=NULL,expected_size=?2,expected_sha256=?3,generation=?4 WHERE operation_id=?1 AND stage IN ('QUARANTINED','SUPERSEDED')",rusqlite::params![op,expected_size,expected_sha,generation])?;
                let (stage, bytes) = tx.query_row(
                    "SELECT stage, received_bytes FROM materializations WHERE operation_id=?1",
                    [&op],
                    |r| Ok((r.get::<_, String>(0)?, r.get::<_, i64>(1)?)),
                )?;
                Ok((Some(stage), bytes))
            })
            .await?;
        if let Some(stage) = &existing_stage
            && stage != "RECEIVING"
        {
            return Err(CoreError::Internal(format!(
                "materialization {op_id} already in stage {stage}"
            )));
        }
        std::fs::create_dir_all(&staging_dir)?;
        let file_path = staging_dir.join("payload");
        let offset = if existing_stage.is_some() {
            existing_bytes.max(0) as u64
        } else {
            0
        };
        StagedWriter::open(file_path, offset)
    }

    /// 阶段 3：校验（大小/哈希）→ PREPARED 并持久化最终位置。
    pub async fn finish_and_validate(
        &self,
        db: &DbHandle,
        op_id: &OperationId,
        writer: StagedWriter,
    ) -> Result<(u64, String), CoreError> {
        let file_path = writer.path.clone();
        let (size, sha_hex) = writer.finish()?;
        let op = op_id.as_str().to_string();
        let size_i = size as i64;
        let sha_owned = sha_hex.clone();
        db.write(move |tx| {
            tx.execute(
                "UPDATE materializations SET stage='VALIDATING', received_bytes=?2,
                 received_sha256=?3, updated_at=?4 WHERE operation_id=?1",
                rusqlite::params![op, size_i, sha_owned, now()],
            )?;
            Ok(())
        })
        .await?;
        let op = op_id.as_str().to_string();
        let (expected_size, expected_sha): (Option<i64>, Option<String>) = db
            .read(move |c| {
                Ok(c.query_row(
                    "SELECT expected_size, expected_sha256 FROM materializations WHERE operation_id=?1",
                    [&op],
                    |r| Ok((r.get(0)?, r.get(1)?)),
                )?)
            })
            .await?;
        if expected_size.is_some_and(|exp| exp != size as i64)
            || expected_sha
                .as_ref()
                .is_some_and(|exp| !exp.eq_ignore_ascii_case(&sha_hex))
        {
            self.quarantine(db, op_id).await?;
            return Err(CoreError::InvalidInput(
                "media size or sha256 mismatch".into(),
            ));
        }
        // 文件落盘边界 + PREPARED 持久化最终位置。
        let final_path = self.object_path(&sha_hex)?;
        fsync_dir_if_possible(file_path.parent().unwrap_or(Path::new(".")));
        let op = op_id.as_str().to_string();
        let final_str = final_path.to_string_lossy().to_string();
        db.write(move |tx| {
            tx.execute(
                "UPDATE materializations SET stage='PREPARED', final_path=?2, updated_at=?3
                 WHERE operation_id=?1",
                rusqlite::params![op, final_str, now()],
            )?;
            Ok(())
        })
        .await?;
        Ok((size, sha_hex))
    }

    /// 阶段 5：安装对象（同卷原子改名；目标存在则校验复用或隔离损坏），
    /// 成功后持久化 OBJECT_READY。
    pub async fn install_object(
        &self,
        db: &DbHandle,
        op_id: &OperationId,
        sha_hex: &str,
    ) -> Result<InstallOutcome, CoreError> {
        let op = op_id.as_str().to_owned();
        let (stage, received, size, expected_size, expected_sha): (String, Option<String>, i64, Option<i64>, Option<String>) = db.read(move |c| Ok(c.query_row(
            "SELECT stage,received_sha256,received_bytes,expected_size,expected_sha256 FROM materializations WHERE operation_id=?1", [op],
            |r| Ok((r.get(0)?,r.get(1)?,r.get(2)?,r.get(3)?,r.get(4)?)))?)).await?;
        if !matches!(stage.as_str(), "VALIDATING" | "PREPARED" | "OBJECT_READY")
            || received.as_deref() != Some(sha_hex)
            || expected_size.is_some_and(|s| s != size)
            || expected_sha.is_some_and(|s| !s.eq_ignore_ascii_case(sha_hex))
        {
            return Err(CoreError::InvalidInput(
                "unvalidated materialization cannot install".into(),
            ));
        }
        let outcome = self.install_object_sync(op_id, sha_hex)?;
        let op = op_id.as_str().to_string();
        db.write(move |tx| {
            tx.execute(
                "UPDATE materializations SET stage='OBJECT_READY', updated_at=?2 WHERE operation_id=?1",
                rusqlite::params![op, now()],
            )?;
            Ok(())
        })
        .await?;
        Ok(outcome)
    }

    fn install_object_sync(
        &self,
        op_id: &OperationId,
        sha_hex: &str,
    ) -> Result<InstallOutcome, CoreError> {
        let staging_file = self
            .profile
            .staging_dir()
            .join(op_id.as_str())
            .join("payload");
        let final_path = self.object_path(sha_hex)?;
        if !staging_file.exists() {
            if final_path.exists() && hash_file(&final_path)? == sha_hex {
                return Ok(InstallOutcome::ReusedExisting);
            }
            return Err(CoreError::Internal("staging payload missing".into()));
        }
        if hash_file(&staging_file)? != sha_hex {
            return Err(CoreError::InvalidInput("staging digest mismatch".into()));
        }
        if final_path.exists() {
            let existing = hash_file(&final_path)?;
            if existing.eq_ignore_ascii_case(sha_hex) {
                // 合法复用：来源独立但字节相同（INV-04）。
                self.cleanup_staging(op_id);
                return Ok(InstallOutcome::ReusedExisting);
            }
            // 损坏对象隔离，不静默覆盖（C04）。
            let corrupt_name = format!(
                "corrupt-{}-{}",
                OffsetDateTime::now_utc().unix_timestamp(),
                &sha_hex[..12.min(sha_hex.len())]
            );
            std::fs::rename(&final_path, self.profile.trash_dir().join(corrupt_name))?;
            fsync_dir_if_possible(final_path.parent().unwrap_or(Path::new(".")));
        }
        if let Some(parent) = final_path.parent() {
            std::fs::create_dir_all(parent)?;
        }
        std::fs::rename(&staging_file, &final_path)?;
        fsync_dir_if_possible(final_path.parent().unwrap_or(Path::new(".")));
        self.cleanup_staging(op_id);
        Ok(InstallOutcome::Installed)
    }

    /// 阶段 6：业务提交（短事务重检代次/取消 → 建立引用 → Outbox → COMMITTED）。
    pub async fn commit(
        &self,
        db: &DbHandle,
        op_id: &OperationId,
        info: &CommitInfo,
        measured_spec: Option<serde_json::Value>,
    ) -> Result<CommitDisposition, CoreError> {
        let info = info.clone();
        let measured = measured_spec.clone();
        let op = op_id.as_str().to_string();
        let disposition: Option<CommitDisposition> = db
            .write(move |tx| {
                let (stage, blob_sha, size, mime, job_id, subject_id): (
                    String,
                    String,
                    i64,
                    Option<String>,
                    Option<String>,
                    String,
                ) = tx.query_row(
                    "SELECT stage, received_sha256, received_bytes, mime, job_id, subject_id
                     FROM materializations WHERE operation_id=?1",
                    [&op],
                    |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?, r.get(3)?, r.get(4)?, r.get(5)?)),
                )?;
                if stage == "COMMITTED" {
                    // 幂等重入（Outbox 重放/重试）。
                    return Ok(Some(CommitDisposition::Committed));
                }
                if stage != "OBJECT_READY" {
                    return Err(CoreError::Internal(format!("cannot commit from stage {stage}")));
                }
                if let Some(job) = &info.job {
                    job.check(tx)?;
                    let generation: i64 = tx.query_row("SELECT generation FROM materializations WHERE operation_id=?1",[&op],|r|r.get(0))?;
                    if job_id.as_deref() != Some(job.job_id.as_str()) || generation != job.generation {
                        return Err(CoreError::Cancelled);
                    }
                } else if job_id.is_some() {
                    return Err(CoreError::Cancelled);
                }
                // 代次/取消重检：目标媒体项必须仍存在（取消/删除不复活，INV-11）。
                let media_exists: i64 = tx.query_row(
                    "SELECT count(*) FROM media_items WHERE media_id=?1",
                    [&info.media_id],
                    |r| r.get(0),
                )?;
                if media_exists == 0 {
                    tx.execute(
                        "UPDATE materializations SET stage='SUPERSEDED', updated_at=?2 WHERE operation_id=?1",
                        rusqlite::params![&op, now()],
                    )?;
                    if let Some(j) = &job_id {
                        tx.execute(
                            "UPDATE jobs SET state='cancelled', updated_at=?2 WHERE job_id=?1",
                            rusqlite::params![j, now()],
                        )?;
                    }
                    return Ok(Some(CommitDisposition::Superseded));
                }
                let blob_id = format!("sha256:{blob_sha}");
                let ts = now();
                tx.execute(
                    "INSERT OR IGNORE INTO blobs (blob_id, size, mime, first_seen_at) VALUES (?1,?2,?3,?4)",
                    rusqlite::params![blob_id, size, mime, ts],
                )?;
                // replica：对象已通过安装校验 → verified。
                let replica_id = format!("repl-{}", uuid::Uuid::new_v4());
                tx.execute(
                    "INSERT OR IGNORE INTO replicas (replica_id, blob_id, node_id, store_id, locator, managed, state, verified_at)
                     VALUES (?1,?2,'local','primary',?3,1,'verified',?4)",
                    rusqlite::params![replica_id, blob_id, blob_sha, ts],
                )?;
                let variant_id = format!("var-{}", uuid::Uuid::new_v4());
                tx.execute(
                    "INSERT INTO media_variants (variant_id, media_id, source_representation, blob_id, claimed_spec, measured_spec, created_at)
                     VALUES (?1,?2,?3,?4,?5,?6,?7)",
                    rusqlite::params![variant_id, info.media_id, info.source_representation, blob_id, info.claimed_spec.to_string(), measured.map(|m| m.to_string()), ts],
                )?;
                tx.execute(
                    "INSERT OR IGNORE INTO asset_selections (media_id, variant_id, policy_version, reason, updated_at)
                     VALUES (?1,?2,'v1','first verified variant',?3)",
                    rusqlite::params![info.media_id, variant_id, ts],
                )?;
                tx.execute(
                    "UPDATE materializations SET stage='COMMITTED', blob_id=?2, updated_at=?3 WHERE operation_id=?1",
                    rusqlite::params![&op, blob_id, ts],
                )?;
                outbox_emit(
                    tx,
                    "media.variant.added",
                    &info.media_id,
                    &serde_json::json!({
                        "media_id": info.media_id,
                        "variant_id": variant_id,
                        "blob_id": blob_id,
                        "size": size,
                    }),
                )?;
                outbox_emit(
                    tx,
                    "entry.media_changed",
                    &subject_id,
                    &serde_json::json!({"media_id": info.media_id}),
                )?;
                Ok(Some(CommitDisposition::Committed))
            })
            .await?;
        Ok(disposition.unwrap_or(CommitDisposition::Committed))
    }

    /// 阶段 7 收尾 + 启动恢复矩阵（§6.4）。业务提交（OBJECT_READY）由
    /// Job 执行器重入，此处只统计。
    pub async fn recover_materializations(
        &self,
        db: &DbHandle,
    ) -> Result<RecoverySummary, CoreError> {
        let mut summary = RecoverySummary::default();
        let rows: Vec<RecoveryRow> = db
            .read(|c| {
                let mut stmt = c.prepare(
                    "SELECT operation_id, stage, staging_path, final_path, received_sha256
                     FROM materializations WHERE stage NOT IN ('COMMITTED','SUPERSEDED','QUARANTINED')",
                )?;
                let rows = stmt
                    .query_map([], |r| {
                        Ok((r.get(0)?, r.get(1)?, r.get(2)?, r.get(3)?, r.get(4)?))
                    })?
                    .collect::<Result<Vec<_>, _>>()?;
                Ok(rows)
            })
            .await?;
        for (op, stage, staging_path, final_path, sha) in rows {
            match stage.as_str() {
                "RECEIVING" => {
                    // 检查点之外的字节不可信：截断到已持久化偏移（§6.4-2）。
                    if let Some(sp) = staging_path {
                        let f = PathBuf::from(&sp).join("payload");
                        let received: i64 = db
                            .read(move |c| {
                                Ok(c.query_row(
                                    "SELECT received_bytes FROM materializations WHERE operation_id=?1",
                                    [&op],
                                    |r| r.get(0),
                                )?)
                            })
                            .await
                            .unwrap_or(0);
                        if f.exists() {
                            let len = f.metadata().map(|m| m.len()).unwrap_or(0);
                            if len > received.max(0) as u64 {
                                let file = std::fs::OpenOptions::new().write(true).open(&f)?;
                                file.set_len(received.max(0) as u64)?;
                            }
                        }
                        summary.resumed_receiving += 1;
                    }
                }
                "VALIDATING" | "PREPARED" | "OBJECT_READY" => {
                    let op_id = OperationId::new(op.clone());
                    let staging = staging_path
                        .as_ref()
                        .map(|p| PathBuf::from(p).join("payload"));
                    let final_file = final_path.as_ref().map(PathBuf::from);
                    let file = staging
                        .filter(|p| p.exists())
                        .or_else(|| final_file.filter(|p| p.exists()));
                    if let Some(file) = file {
                        crate::paths::no_links(&file)?;
                        let hex = hash_file(&file)?;
                        let size = file.metadata()?.len() as i64;
                        let op2 = op.clone();
                        let (expected_size, expected_sha): (Option<i64>,Option<String>) = db.read(move |c| Ok(c.query_row("SELECT expected_size,expected_sha256 FROM materializations WHERE operation_id=?1",[op2],|r|Ok((r.get(0)?,r.get(1)?)))?)).await?;
                        if expected_size.is_some_and(|s| s != size)
                            || expected_sha
                                .as_ref()
                                .is_some_and(|s| !s.eq_ignore_ascii_case(&hex))
                            || sha.as_ref().is_some_and(|s| s != &hex)
                        {
                            self.quarantine(db, &op_id).await?;
                            summary.quarantined += 1;
                            continue;
                        }
                        let op2 = op.clone();
                        let hex2 = hex.clone();
                        db.write(move |tx| { tx.execute("UPDATE materializations SET received_sha256=?2,received_bytes=?3 WHERE operation_id=?1",rusqlite::params![op2,hex2,size])?; Ok(()) }).await?;
                        self.install_object(db, &op_id, &hex).await?;
                        summary.recommitted += 1;
                        summary.awaiting_business_commit += 1;
                    } else {
                        self.quarantine(db, &op_id).await?;
                        summary.quarantined += 1;
                    }
                }
                _ => {}
            }
        }
        // COMMITTED 但 staging 残留：清理（§6.4-7），不撤销成功档案。
        let committed_with_staging: Vec<(String, Option<String>)> = db
            .read(|c| {
                let mut stmt = c.prepare(
                    "SELECT operation_id, staging_path FROM materializations WHERE stage='COMMITTED' AND staging_path IS NOT NULL",
                )?;
                let rows = stmt
                    .query_map([], |r| Ok((r.get(0)?, r.get(1)?)))?
                    .collect::<Result<Vec<_>, _>>()?;
                Ok(rows)
            })
            .await?;
        for (op, sp) in committed_with_staging {
            if let Some(sp) = sp {
                if Path::new(&sp).exists() {
                    std::fs::remove_dir_all(&sp)?;
                }
                let op2 = op.clone();
                db.write(move |tx| {
                    tx.execute(
                        "UPDATE materializations SET staging_path=NULL, updated_at=?2 WHERE operation_id=?1",
                        rusqlite::params![op2, now()],
                    )?;
                    Ok(())
                })
                .await?;
                summary.cleaned_staging += 1;
            }
        }
        Ok(summary)
    }

    /// 已验证对象的完整性检查（C08）：缺失/损坏真实标记，旧 succeeded
    /// 不阻止恢复新代次。
    pub async fn verify_replicas(&self, db: &DbHandle, limit: u32) -> Result<i64, CoreError> {
        let rows: Vec<(String, String)> = db
            .read(move |c| {
                let mut stmt = c.prepare(
                    "SELECT r.replica_id, r.locator FROM replicas r
                     WHERE r.state IN ('verified','staged') ORDER BY CASE WHEN r.replica_id > COALESCE((SELECT value FROM meta WHERE key='replica_verify_cursor'),'') THEN 0 ELSE 1 END, r.replica_id LIMIT ?1",
                )?;
                let rows = stmt
                    .query_map([limit], |r| Ok((r.get(0)?, r.get(1)?)))?
                    .collect::<Result<Vec<_>, _>>()?;
                Ok(rows)
            })
            .await?;
        let cursor = rows.last().map(|r| r.0.clone()).unwrap_or_default();
        let mut broken = 0i64;
        for (replica_id, locator) in rows {
            let path = self.object_path(&locator)?;
            let new_state = if !path.exists() {
                "absent"
            } else {
                match hash_file(&path) {
                    Ok(hex) if hex.eq_ignore_ascii_case(&locator) => continue,
                    _ => "corrupt",
                }
            };
            broken += 1;
            let state2 = new_state.to_string();
            db.write(move |tx| {
                tx.execute(
                    "UPDATE replicas SET state=?2, verified_at=NULL WHERE replica_id=?1",
                    rusqlite::params![replica_id, state2],
                )?;
                outbox_emit(
                    tx,
                    "replica.state_changed",
                    &replica_id,
                    &serde_json::json!({"replica_id": replica_id, "state": state2}),
                )?;
                Ok(())
            })
            .await?;
        }
        db.write(move |tx| { tx.execute("INSERT INTO meta(key,value) VALUES('replica_verify_cursor',?1) ON CONFLICT(key) DO UPDATE SET value=excluded.value",[cursor])?; Ok(()) }).await?;
        Ok(broken)
    }

    pub fn object_path(&self, sha_hex: &str) -> Result<PathBuf, CoreError> {
        crate::paths::object(&self.profile.objects_dir(), sha_hex)
    }

    pub async fn quarantine(&self, db: &DbHandle, op_id: &OperationId) -> Result<(), CoreError> {
        let op = op_id.as_str().to_owned();
        db.write(move |tx| { tx.execute("UPDATE materializations SET stage='QUARANTINED',updated_at=?2 WHERE operation_id=?1",rusqlite::params![op,now()])?; Ok(()) }).await
    }

    pub async fn ready(
        &self,
        db: &DbHandle,
        op_id: &OperationId,
        job: &crate::jobs::ClaimedJob,
    ) -> Result<bool, CoreError> {
        let op = op_id.as_str().to_owned();
        let guard = job.clone();
        type ReadyRow = (String, Option<String>, Option<i64>, Option<String>);
        let row:Option<ReadyRow>=db.read(move |c|{
            guard.check(c)?;
            Ok(c.query_row("SELECT stage,received_sha256,expected_size,expected_sha256 FROM materializations WHERE operation_id=?1",[op],|r|Ok((r.get(0)?,r.get(1)?,r.get(2)?,r.get(3)?))).ok())
        }).await?;
        let Some((stage, sha, expected_size, expected_sha)) = row else {
            return Ok(false);
        };
        if !matches!(stage.as_str(), "VALIDATING" | "PREPARED" | "OBJECT_READY") {
            return Ok(false);
        }
        let staging = self
            .profile
            .staging_dir()
            .join(op_id.as_str())
            .join("payload");
        let file = if staging.exists() {
            staging
        } else if let Some(sha) = &sha {
            self.object_path(sha)?
        } else {
            self.quarantine(db, op_id).await?;
            return Ok(false);
        };
        if !file.exists() {
            self.quarantine(db, op_id).await?;
            return Ok(false);
        }
        let actual = hash_file(&file)?;
        let size = file.metadata()?.len() as i64;
        if sha.as_ref().is_some_and(|s| s != &actual)
            || expected_size.is_some_and(|s| s != size)
            || expected_sha.as_ref().is_some_and(|s| s != &actual)
        {
            self.quarantine(db, op_id).await?;
            return Ok(false);
        }
        self.install_object(db, op_id, &actual).await?;
        Ok(true)
    }

    pub fn cleanup_staging(&self, op_id: &OperationId) {
        let dir = self.profile.staging_dir().join(op_id.as_str());
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

/// 流式写入器：增量哈希 + 检查点回调。
pub struct StagedWriter {
    file: std::fs::File,
    pub path: PathBuf,
    hasher: Sha256,
    written: u64,
    last_checkpoint: u64,
}

impl StagedWriter {
    fn open(path: PathBuf, offset: u64) -> Result<Self, CoreError> {
        use std::io::Seek;
        let mut hasher = Sha256::new();
        if offset > 0 {
            let mut prefix = std::fs::File::open(&path)?;
            if prefix.metadata()?.len() < offset {
                return Err(CoreError::InvalidInput("checkpoint exceeds file".into()));
            }
            let mut remaining = offset;
            let mut buf = vec![0; 256 * 1024];
            while remaining > 0 {
                let count = remaining.min(buf.len() as u64) as usize;
                prefix.read_exact(&mut buf[..count])?;
                hasher.update(&buf[..count]);
                remaining -= count as u64;
            }
        }
        let mut file = std::fs::OpenOptions::new()
            .create(true)
            .write(true)
            .truncate(offset == 0)
            .open(&path)?;
        file.set_len(offset)?;
        file.seek(std::io::SeekFrom::Start(offset))
            .map_err(CoreError::Io)?;
        Ok(Self {
            file,
            path,
            hasher,
            written: offset,
            last_checkpoint: offset,
        })
    }

    /// 追加字节；ENOSPC 等错误原样上抛（C06）。每 4 MiB 持久化检查点。
    pub async fn write_chunk(
        &mut self,
        db: &DbHandle,
        op_id: &OperationId,
        chunk: &[u8],
    ) -> Result<(), CoreError> {
        self.file.write_all(chunk)?;
        self.hasher.update(chunk);
        self.written += chunk.len() as u64;
        if self.written - self.last_checkpoint >= crate::config::CHECKPOINT_INTERVAL_BYTES {
            self.file.sync_all()?;
            self.last_checkpoint = self.written;
            let w = self.written as i64;
            let op = op_id.as_str().to_string();
            db.write(move |tx| {
                tx.execute(
                    "UPDATE materializations SET received_bytes=?2, updated_at=?3 WHERE operation_id=?1",
                    rusqlite::params![op, w, now()],
                )?;
                Ok(())
            })
            .await?;
        }
        Ok(())
    }

    /// 弱校验器场景：丢弃检查点字节，从头重取（design §9.4）。
    pub async fn reset(&mut self) -> Result<(), CoreError> {
        use std::io::Seek;
        self.file.set_len(0)?;
        self.file.seek(std::io::SeekFrom::Start(0))?;
        self.hasher = Sha256::new();
        self.written = 0;
        self.last_checkpoint = 0;
        Ok(())
    }

    /// 结束接收：flush + fsync，返回总大小与完整哈希。
    pub fn finish(mut self) -> Result<(u64, String), CoreError> {
        self.file.flush()?;
        self.file.sync_all()?;
        let final_hash = self.hasher.finalize();
        let hex: String = final_hash.iter().map(|b| format!("{b:02x}")).collect();
        Ok((self.written, hex))
    }
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
