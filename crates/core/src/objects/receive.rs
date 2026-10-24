//! Receiving: staging, validation and installation of objects (stages 1–5).

use super::*;

const SELECT_MATERIALIZATIONS_OWNED: &str = "SELECT stage,received_sha256,expected_size,expected_sha256 FROM materializations WHERE operation_id=?1 AND job_id=?2 AND generation=?3";

const SELECT_MATERIALIZATIONS_2: &str = "SELECT stage,received_sha256,received_bytes,expected_size,expected_sha256 FROM materializations WHERE operation_id=?1";

impl ObjectStore {
    /// 阶段 1：登记接收，创建操作日志与 staging 文件。已存在同 id 的
    /// RECEIVING 记录时复用（可重入；断点续传从持久化检查点继续）。
    pub async fn start_receiving(
        &self,
        db: &DbHandle,
        params: ReceiveParams,
        op_id: &OperationId,
    ) -> Result<StagedWriter, CoreError> {
        self.ensure_dirs()?;
        let staging_dir = self.staging_dir().join(op_id.as_str());
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
                let owned: bool = tx.query_row(
                    "SELECT job_id IS ?2 AND subject_kind=?3 AND subject_id=?4 AND generation=?5 AND expected_size IS ?6 AND expected_sha256 IS ?7 FROM materializations WHERE operation_id=?1",
                    rusqlite::params![op,job_id,subject_kind,subject_id,generation,expected_size,expected_sha], |r| r.get(0))?;
                if !owned {
                    return Err(CoreError::VersionConflict("materialization belongs to another acquisition".into()));
                }
                tx.execute("UPDATE materializations SET stage='RECEIVING',received_bytes=0,received_sha256=NULL,final_path=NULL WHERE operation_id=?1 AND stage IN ('QUARANTINED','SUPERSEDED')",[&op])?;
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
        let reservation = self.budget.transfer(params.expected_size);
        reservation.progress(params.expected_size, offset);
        let profile = self.profile();
        self.budget.check(&profile, 0)?;
        let writer = StagedWriter::open(file_path, offset)?;
        Ok(writer.with_budget(
            self.budget.clone(),
            reservation,
            profile,
            params.expected_size,
        ))
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
        let (expected_size, expected_sha, expected_mime): (Option<i64>, Option<String>, Option<String>) = db
            .read(move |c| {
                Ok(c.query_row(
                    "SELECT expected_size, expected_sha256, mime FROM materializations WHERE operation_id=?1",
                    [&op],
                    |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)),
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
        if expected_mime.as_deref() == Some("application/vnd.trace.subtitle+json") {
            let valid = (|| -> Result<bool, CoreError> {
                if size > 8 * 1024 * 1024 {
                    return Ok(false);
                }
                let value: serde_json::Value =
                    serde_json::from_reader(std::fs::File::open(&file_path)?)?;
                Ok(value["body"].as_array().is_some_and(|lines| {
                    lines.len() <= 50000
                        && lines.iter().all(|line| {
                            let from = line["from"].as_f64();
                            let to = line["to"].as_f64();
                            from.zip(to).is_some_and(|(from, to)| {
                                from.is_finite() && to.is_finite() && from >= 0.0 && to >= from
                            }) && line["content"].as_str().is_some_and(|s| s.len() <= 16384)
                        })
                }))
            })();
            if !valid.unwrap_or(false) {
                self.quarantine(db, op_id).await?;
                return Err(CoreError::InvalidInput("invalid subtitle document".into()));
            }
        }
        if let Some(family) = expected_mime.as_deref().and_then(|m| m.strip_suffix('*')) {
            let mut prefix = [0u8; 8192];
            let n = std::fs::File::open(&file_path)?.read(&mut prefix)?;
            let detected = infer::get(&prefix[..n]).map(|v| v.mime_type());
            // MP4/WebM can carry audio-only streams but their magic identifies
            // the container. Core verifies actual tracks before committing audio.
            let audio_container = family == "audio/"
                && matches!(
                    detected,
                    Some("video/mp4" | "video/webm" | "application/ogg")
                );
            if !detected.is_some_and(|mime| mime.starts_with(family)) && !audio_container {
                self.quarantine(db, op_id).await?;
                return Err(CoreError::InvalidInput(
                    "resource bytes do not match media family".into(),
                ));
            }
            let mime = detected.unwrap().to_owned();
            let op = op_id.as_str().to_owned();
            db.write(move |tx| {
                tx.execute(
                    "UPDATE materializations SET mime=?2 WHERE operation_id=?1",
                    rusqlite::params![op, mime],
                )?;
                Ok(())
            })
            .await?;
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
        let (stage, received, size, expected_size, expected_sha): (
            String,
            Option<String>,
            i64,
            Option<i64>,
            Option<String>,
        ) = db
            .read(move |c| {
                Ok(c.query_row(SELECT_MATERIALIZATIONS_2, [op], |r| {
                    Ok((r.get(0)?, r.get(1)?, r.get(2)?, r.get(3)?, r.get(4)?))
                })?)
            })
            .await?;
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
            tx.execute(UPDATE_MATERIALIZATIONS, rusqlite::params![op, now()])?;
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
        let staging_file = self.staging_dir().join(op_id.as_str()).join("payload");
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
            std::fs::rename(&final_path, self.trash_dir().join(corrupt_name))?;
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

    pub async fn ready(
        &self,
        db: &DbHandle,
        op_id: &OperationId,
        job: &crate::jobs::ClaimedJob,
    ) -> Result<bool, CoreError> {
        let op = op_id.as_str().to_owned();
        let guard = job.clone();
        type ReadyRow = (String, Option<String>, Option<i64>, Option<String>);
        let row: Option<ReadyRow> = db
            .read(move |c| {
                guard.check(c)?;
                Ok(c.query_row(
                    SELECT_MATERIALIZATIONS_OWNED,
                    rusqlite::params![op, guard.job_id, guard.generation],
                    |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?, r.get(3)?)),
                )
                .ok())
            })
            .await?;
        let Some((stage, sha, expected_size, expected_sha)) = row else {
            return Ok(false);
        };
        if !matches!(stage.as_str(), "VALIDATING" | "PREPARED" | "OBJECT_READY") {
            return Ok(false);
        }
        let staging = self.staging_dir().join(op_id.as_str()).join("payload");
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
}
