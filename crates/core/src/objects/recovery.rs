//! Startup recovery matrix (design §6.4) and replica verification.
//!
//! Every non-final materialization is re-entered from its persisted stage; bytes past the
//! checkpoint are not trusted and an object that no longer matches is quarantined, never
//! reported as success.

use super::*;

/// 恢复扫描行：(operation_id, stage, staging_path, final_path, received_sha256)。
type RecoveryRow = (
    String,
    String,
    Option<String>,
    Option<String>,
    Option<String>,
);

const SELECT_PENDING_MATERIALIZATIONS: &str =
    "SELECT operation_id, stage, staging_path, final_path, received_sha256
     FROM materializations WHERE stage NOT IN ('COMMITTED','SUPERSEDED','QUARANTINED')";
const SELECT_RECEIVED_BYTES: &str =
    "SELECT received_bytes FROM materializations WHERE operation_id=?1";
const UPDATE_MATERIALIZATIONS_RECEIVED: &str =
    "UPDATE materializations SET received_sha256=?2,received_bytes=?3 WHERE operation_id=?1";
const SELECT_COMMITTED_STAGING: &str =
    "SELECT rowid,operation_id,staging_path FROM materializations
     WHERE rowid>?1 AND stage='COMMITTED' AND staging_path IS NOT NULL ORDER BY rowid LIMIT 64";
const UPDATE_MATERIALIZATIONS_CLEANED: &str =
    "UPDATE materializations SET staging_path=NULL,updated_at=?2
     WHERE operation_id=?1 AND stage='COMMITTED' AND staging_path=?3";

/// What re-validating a surviving object ended in.
enum Revalidated {
    Quarantined,
    Installed,
}

impl ObjectStore {
    /// 阶段 7 收尾 + 启动恢复矩阵（§6.4）。业务提交（OBJECT_READY）由
    /// Job 执行器重入，此处只统计。
    pub async fn recover_materializations(
        &self,
        db: &DbHandle,
    ) -> Result<RecoverySummary, CoreError> {
        let mut summary = RecoverySummary::default();
        let rows: Vec<RecoveryRow> = db
            .read(|c| {
                let mut stmt = c.prepare(SELECT_PENDING_MATERIALIZATIONS)?;
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
                    if let Some(staging) = staging_path {
                        truncate_to_checkpoint(db, &op, &staging).await?;
                        summary.resumed_receiving += 1;
                    }
                }
                "VALIDATING" | "PREPARED" | "OBJECT_READY" => {
                    let file = surviving_payload(staging_path.as_deref(), final_path.as_deref());
                    match self.revalidate(db, &op, file, sha.as_deref()).await? {
                        Revalidated::Quarantined => summary.quarantined += 1,
                        Revalidated::Installed => {
                            summary.recommitted += 1;
                            summary.awaiting_business_commit += 1;
                        }
                    }
                }
                _ => {}
            }
        }
        summary.cleaned_staging += self.clean_committed_staging(db).await?;
        Ok(summary)
    }

    /// Checks a surviving payload against what was declared and received, then installs it.
    async fn revalidate(
        &self,
        db: &DbHandle,
        op: &str,
        file: Option<PathBuf>,
        received_sha: Option<&str>,
    ) -> Result<Revalidated, CoreError> {
        let op_id = OperationId::new(op.to_owned());
        let Some(file) = file else {
            self.quarantine(db, &op_id).await?;
            return Ok(Revalidated::Quarantined);
        };
        crate::paths::no_links(&file)?;
        let hex = hash_file(&file)?;
        let size = file.metadata()?.len() as i64;
        let key = op.to_owned();
        let (expected_size, expected_sha): (Option<i64>, Option<String>) = db
            .read(move |c| {
                Ok(c.query_row(SELECT_MATERIALIZATIONS, [key], |r| {
                    Ok((r.get(0)?, r.get(1)?))
                })?)
            })
            .await?;
        if expected_size.is_some_and(|s| s != size)
            || expected_sha
                .as_ref()
                .is_some_and(|s| !s.eq_ignore_ascii_case(&hex))
            || received_sha.is_some_and(|s| s != hex)
        {
            self.quarantine(db, &op_id).await?;
            return Ok(Revalidated::Quarantined);
        }
        let (key, digest) = (op.to_owned(), hex.clone());
        db.write(move |tx| {
            tx.execute(
                UPDATE_MATERIALIZATIONS_RECEIVED,
                rusqlite::params![key, digest, size],
            )?;
            Ok(())
        })
        .await?;
        self.install_object(db, &op_id, &hex).await?;
        Ok(Revalidated::Installed)
    }

    /// COMMITTED 但 staging 残留：清理（§6.4-7），不撤销成功档案。
    /// A long recording leaves thousands of already removed staging paths.
    /// A FULL transaction for every such receipt makes startup proportional
    /// to thousands of fsyncs. Clean bounded pages, then acknowledge each
    /// page atomically. A crash before that acknowledgement merely retries
    /// absent directories; committed objects and their receipts stay intact.
    async fn clean_committed_staging(&self, db: &DbHandle) -> Result<i64, CoreError> {
        let mut cleaned = 0i64;
        let mut after = 0i64;
        loop {
            let rows: Vec<(i64, String, String)> = db
                .read(move |c| {
                    let mut stmt = c.prepare(SELECT_COMMITTED_STAGING)?;
                    Ok(stmt
                        .query_map([after], |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)))?
                        .collect::<Result<Vec<_>, _>>()?)
                })
                .await?;
            let Some((last, _, _)) = rows.last() else {
                return Ok(cleaned);
            };
            after = *last;
            for (_, op, staging) in &rows {
                self.remove_owned_staging(op, staging)?;
            }
            cleaned += db
                .write(move |tx| {
                    let timestamp = now();
                    let mut count = 0;
                    for (_, op, staging) in rows {
                        count += tx.execute(
                            UPDATE_MATERIALIZATIONS_CLEANED,
                            rusqlite::params![op, timestamp, staging],
                        )? as i64;
                    }
                    Ok(count)
                })
                .await?;
        }
    }

    /// Removes a staging directory only when it is the one this store owns for the operation.
    fn remove_owned_staging(&self, op: &str, staging: &str) -> Result<(), CoreError> {
        let mut parts = Path::new(op).components();
        let single =
            matches!(parts.next(), Some(std::path::Component::Normal(_))) && parts.next().is_none();
        let expected = self.staging_dir().join(op);
        if !single || Path::new(staging) != expected {
            return Err(CoreError::InvalidInput(
                "unowned staging cleanup path".into(),
            ));
        }
        crate::paths::no_links(&expected)?;
        if expected.exists() {
            std::fs::remove_dir_all(&expected)?;
        }
        Ok(())
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
}

/// 检查点之外的字节不可信：截断到已持久化偏移（§6.4-2）。
async fn truncate_to_checkpoint(db: &DbHandle, op: &str, staging: &str) -> Result<(), CoreError> {
    let payload = PathBuf::from(staging).join("payload");
    let key = op.to_owned();
    let received: i64 = db
        .read(move |c| Ok(c.query_row(SELECT_RECEIVED_BYTES, [&key], |r| r.get(0))?))
        .await
        .unwrap_or(0);
    if payload.exists() {
        let len = payload.metadata().map(|m| m.len()).unwrap_or(0);
        if len > received.max(0) as u64 {
            let file = std::fs::OpenOptions::new().write(true).open(&payload)?;
            file.set_len(received.max(0) as u64)?;
        }
    }
    Ok(())
}

/// The payload that survived the crash: the staged one, else the already installed one.
fn surviving_payload(staging: Option<&str>, installed: Option<&str>) -> Option<PathBuf> {
    staging
        .map(|p| PathBuf::from(p).join("payload"))
        .filter(|p| p.exists())
        .or_else(|| installed.map(PathBuf::from).filter(|p| p.exists()))
}
