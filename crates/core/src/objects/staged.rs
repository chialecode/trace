//! The staging writer: bounded, resumable payload writes with storage reservations.

use super::*;

struct WriterBudget {
    budget: std::sync::Arc<crate::storage_budget::StorageBudget>,
    reservation: crate::storage_budget::Reservation,
    profile: Profile,
    expected: Option<i64>,
}
/// 流式写入器：增量哈希 + 检查点回调。
pub struct StagedWriter {
    file: std::fs::File,
    pub path: PathBuf,
    hasher: Sha256,
    written: u64,
    last_checkpoint: u64,
    budget: Option<WriterBudget>,
}

impl StagedWriter {
    /// Charges the written bytes against the storage budget from now on.
    pub(super) fn with_budget(
        mut self,
        budget: std::sync::Arc<crate::storage_budget::StorageBudget>,
        reservation: crate::storage_budget::Reservation,
        profile: Profile,
        expected: Option<i64>,
    ) -> Self {
        self.budget = Some(WriterBudget {
            budget,
            reservation,
            profile,
            expected,
        });
        self
    }

    pub(super) fn open(path: PathBuf, offset: u64) -> Result<Self, CoreError> {
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
            budget: None,
        })
    }

    /// 追加字节；ENOSPC 等错误原样上抛（C06）。每 4 MiB 持久化检查点。
    pub async fn write_chunk(
        &mut self,
        db: &DbHandle,
        op_id: &OperationId,
        chunk: &[u8],
    ) -> Result<(), CoreError> {
        if let Some(b) = &self.budget {
            b.reservation.progress(b.expected, self.written);
            b.budget.check(&b.profile, chunk.len())?;
        }
        // Account for each successful write even when a later write hits ENOSPC.
        // Recording can then seal the exact received prefix with interruption evidence.
        let mut rest = chunk;
        while !rest.is_empty() {
            match self.file.write(rest) {
                Ok(0) => return Err(CoreError::Io(std::io::ErrorKind::WriteZero.into())),
                Ok(n) => {
                    self.hasher.update(&rest[..n]);
                    self.written += n as u64;
                    rest = &rest[n..];
                }
                Err(e) if e.kind() == std::io::ErrorKind::Interrupted => continue,
                Err(e) => return Err(CoreError::Io(e)),
            }
        }
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
    pub fn written_len(&self) -> u64 {
        self.written
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
