//! 持久任务存储与调度辅助（docs/design.md §9.2）。
//!
//! Job = 逻辑目标（类型+主体+代次+策略版本），job_key 唯一约束防重复；
//! Attempt 记录每次执行。旧实例租约由恢复扫描回收。

use crate::db::DbHandle;
use crate::error::CoreError;
use trace_domain::time::{TimePrecision, TraceTime};
use trace_domain::{errors::FailureClass, states::JobState};

pub fn now() -> String {
    TraceTime::now(TimePrecision::Discovery).rfc3339
}

/// 入队（或复活既有逻辑目标）。返回 job_id。
/// 参数对应 jobs 表一行的完整初值，内聚 SQL 写入故不拆分（clippy 8/7 豁免）。
#[allow(clippy::too_many_arguments)]
pub async fn enqueue(
    db: &DbHandle,
    job_key: String,
    job_type: &str,
    subject_id: &str,
    payload: serde_json::Value,
    priority: i32,
    target_generation: i64,
    run_after_ms: u64,
) -> Result<String, CoreError> {
    let jt = job_type.to_string();
    let subject = subject_id.to_string();
    let next_run_at = plus_ms(
        &TraceTime::now(TimePrecision::Discovery).rfc3339,
        run_after_ms,
    );
    let job_id = format!("job-{}", uuid::Uuid::new_v4());
    db.write(move |tx| {
        // 唯一 job_key：已存在则不重复创建（INV-09）。
        let existing: Option<(String, String)> = tx
            .query_row(
                "SELECT job_id, state FROM jobs WHERE job_key=?1",
                [&job_key],
                |r| Ok((r.get(0)?, r.get(1)?)),
            )
            .ok();
        if let Some((id, state)) = existing {
            let s = parse_job_state(&state);
            if s.is_terminal() {
                // 终态任务由显式重试产生新代次，这里不复活。
                return Ok(id);
            }
            return Ok(id);
        }
        tx.execute(
            "INSERT INTO jobs (job_id, job_key, job_type, subject_id, payload, target_generation,
             priority, state, next_run_at, created_at, updated_at)
             VALUES (?1,?2,?3,?4,?5,?6,?7,'queued',?8,?8,?8)",
            rusqlite::params![
                job_id,
                job_key,
                jt,
                subject,
                payload.to_string(),
                target_generation,
                priority,
                next_run_at
            ],
        )?;
        Ok(job_id)
    })
    .await
}

/// Subscription and scan state change in one transaction. Revoking a lease makes late pages inert.
pub fn set_subscription_enabled(
    tx: &rusqlite::Transaction<'_>,
    id: &str,
    enabled: bool,
) -> Result<(), CoreError> {
    let old: String = tx.query_row(
        "SELECT state FROM subscriptions WHERE subscription_id=?1",
        [id],
        |r| r.get(0),
    )?;
    let state = if enabled { "enabled" } else { "paused" };
    if old == state {
        return Ok(());
    }
    let ts = now();
    tx.execute("UPDATE subscriptions SET state=?2,config_version=config_version+1,updated_at=?3 WHERE subscription_id=?1", rusqlite::params![id,state,ts])?;
    if enabled {
        tx.execute("UPDATE jobs SET state='queued',target_generation=target_generation+1,lease_owner=NULL,lease_expires_at=NULL,next_run_at=?2,updated_at=?2,last_error=NULL WHERE job_type='scan' AND subject_id=?1 AND state NOT IN ('succeeded','cancelled')",rusqlite::params![id,ts])?;
        // Restore/import and old profiles can lack a recurring scan. The stable key prevents duplicates.
        tx.execute("INSERT OR IGNORE INTO jobs(job_id,job_key,job_type,subject_id,payload,target_generation,priority,state,next_run_at,created_at,updated_at) VALUES(?1,?2,'scan',?3,?4,1,1,'queued',?5,?5,?5)",rusqlite::params![format!("job-{}",uuid::Uuid::new_v4()),format!("scan:{id}:incremental"),id,serde_json::json!({"subscription_id":id,"mode":"incremental"}).to_string(),ts])?;
    } else {
        tx.execute("UPDATE jobs SET state='paused',target_generation=target_generation+1,lease_owner=NULL,lease_expires_at=NULL,updated_at=?2 WHERE job_type='scan' AND subject_id=?1 AND state NOT IN ('succeeded','cancelled','paused')",rusqlite::params![id,ts])?;
    }
    crate::objects::outbox_emit(
        tx,
        "subscription.updated",
        id,
        &serde_json::json!({"state":state}),
    )?;
    Ok(())
}

/// 旧实例租约回收 + 恢复扫描入口。
pub async fn recover_stale_leases(db: &DbHandle, instance_id: &str) -> Result<i64, CoreError> {
    let me = instance_id.to_string();
    let ts = now();
    db.write(move |tx| {
        let stale: Vec<String> = {
            let mut stmt = tx.prepare(
                "SELECT job_id FROM jobs
                 WHERE state IN ('leased','running')
                   AND (lease_owner IS NULL OR lease_owner != ?1 OR lease_expires_at IS NULL OR lease_expires_at < ?2)",
            )?;
            stmt
                .query_map(rusqlite::params![me, ts], |r| r.get(0))?
                .collect::<Result<Vec<_>, _>>()?
        };
        for id in &stale {
            tx.execute(
                "UPDATE jobs SET state='queued', lease_owner=NULL, lease_expires_at=NULL,
                 next_run_at=?2, updated_at=?2 WHERE job_id=?1",
                rusqlite::params![id, ts],
            )?;
        }
        Ok(stale.len() as i64)
    })
    .await
}

/// 认领到期任务（原子），返回 (job_id, type, subject, payload)。
pub async fn claim_due(
    db: &DbHandle,
    instance_id: &str,
    limit: i32,
) -> Result<Vec<ClaimedJob>, CoreError> {
    let me = instance_id.to_string();
    let ts = now();
    db.write(move |tx| {
        let mut claimed = Vec::new();
        let due: Vec<(String, String, String, String, i64)> = {
            let mut stmt = tx.prepare(
                "SELECT job_id, job_type, subject_id, payload, target_generation FROM jobs
                 WHERE state IN ('queued','waiting_retry','waiting_storage')
                   AND (next_run_at IS NULL OR next_run_at <= ?1)
                   AND (job_type != 'scan' OR EXISTS(SELECT 1 FROM subscriptions s WHERE s.subscription_id=jobs.subject_id AND s.state='enabled'))
                 ORDER BY priority ASC, next_run_at ASC LIMIT ?2",
            )?;

            stmt.query_map(rusqlite::params![ts, limit], |r| {
                Ok((r.get(0)?, r.get(1)?, r.get(2)?, r.get(3)?, r.get(4)?))
            })?
            .collect::<Result<Vec<_>, _>>()?
        };
        for (job_id, jt, subject, payload, generation) in due {
            let owner = format!("{me}:{}", uuid::Uuid::new_v4());
            let updated = tx.execute(
                "UPDATE jobs SET state='leased', lease_owner=?2, lease_expires_at=?3, updated_at=?3
                 WHERE job_id=?1 AND state IN ('queued','waiting_retry','waiting_storage')",
                rusqlite::params![job_id, owner, lease_expiry(&ts)],
            )?;
            if updated == 1 {
                claimed.push(ClaimedJob {
                    job_id,
                    owner,
                    generation,
                    job_type: jt,
                    subject_id: subject,
                    payload: serde_json::from_str(&payload).unwrap_or_default(),
                });
            }
        }
        Ok(claimed)
    })
    .await
}

fn lease_expiry(from: &str) -> String {
    plus_ms(from, 300_000)
}

#[derive(Debug, Clone)]
pub struct ClaimedJob {
    pub owner: String,
    pub generation: i64,
    pub job_id: String,
    pub job_type: String,
    pub subject_id: String,
    pub payload: serde_json::Value,
}

impl ClaimedJob {
    pub async fn mark_running(&self, db: &DbHandle) -> Result<(), CoreError> {
        let job = self.clone();
        db.write(move |tx| {
            job.check(tx)?;
            tx.execute(
                "UPDATE jobs SET state='running', updated_at=?2 WHERE job_id=?1",
                rusqlite::params![job.job_id, now()],
            )?;
            Ok(())
        })
        .await
    }

    pub fn check(&self, tx: &rusqlite::Connection) -> Result<(), CoreError> {
        let valid: bool = tx.query_row("SELECT EXISTS(SELECT 1 FROM jobs WHERE job_id=?1 AND lease_owner=?2 AND target_generation=?3 AND state IN ('leased','running') AND lease_expires_at>?4 AND (job_type!='scan' OR EXISTS(SELECT 1 FROM subscriptions s WHERE s.subscription_id=jobs.subject_id AND s.state='enabled')))", rusqlite::params![self.job_id,self.owner,self.generation,now()], |r| r.get(0))?;
        if valid {
            Ok(())
        } else {
            Err(CoreError::Cancelled)
        }
    }

    pub async fn heartbeat(&self, db: &DbHandle) -> Result<(), CoreError> {
        let job = self.clone();
        db.write(move |tx| {
            job.check(tx)?;
            tx.execute(
                "UPDATE jobs SET lease_expires_at=?2 WHERE job_id=?1",
                rusqlite::params![job.job_id, lease_expiry(&now())],
            )?;
            Ok(())
        })
        .await
    }

    pub async fn requeue(&self, db: &DbHandle) -> Result<(), CoreError> {
        let job = self.clone();
        db.write(move |tx| {
            tx.execute("UPDATE jobs SET state='queued',lease_owner=NULL,lease_expires_at=NULL,next_run_at=?2 WHERE job_id=?1 AND lease_owner=?3 AND target_generation=?4 AND state IN ('running','leased')",rusqlite::params![job.job_id,now(),job.owner,job.generation])?;
            Ok(())
        }).await
    }

    /// Only the executor may finish the whole group. A scan that rescheduled itself stays queued.
    pub async fn succeed(&self, db: &DbHandle) -> Result<(), CoreError> {
        let job = self.clone();
        db.write(move |tx| {
            tx.execute("UPDATE jobs SET state='succeeded', lease_owner=NULL, lease_expires_at=NULL, updated_at=?2 WHERE job_id=?1 AND state='running' AND lease_owner=?3 AND target_generation=?4 AND lease_expires_at>?2", rusqlite::params![job.job_id, now(),job.owner,job.generation])?;
            Ok(())
        }).await
    }

    /// 失败分类：决定退避或 waiting 状态。
    pub async fn fail(
        &self,
        db: &DbHandle,
        class: &FailureClass,
        message: &str,
        retry_after_ms: Option<u64>,
    ) -> Result<(), CoreError> {
        let id = self.job_id.clone();
        let job = self.clone();
        let class_owned = *class;
        let class_str = serde_json::to_string(class).unwrap_or_default();
        let msg = sanitize_message(message);
        let error_json = serde_json::json!({
            "class": class_str.trim_matches('"'),
            "message": msg,
            "retry_after_ms": retry_after_ms,
            "attempt_at": now(),
        })
        .to_string();
        db.write(move |tx| {
            job.check(tx)?;
            let attempts: i64 = tx
                .query_row("SELECT attempts FROM jobs WHERE job_id=?1", [&id], |r| r.get(0))
                .unwrap_or(0);
            let (state, delay_ms): (&str, Option<u64>) = match &class_owned {
                _ if attempts >= 3 && class_owned.suggest_retryable() => ("failed", None),
                FailureClass::Cancelled => ("cancelled", None),
                FailureClass::StorageFull => ("waiting_storage", Some(60_000)),
                FailureClass::AuthRequired | FailureClass::PermissionDenied | FailureClass::CapabilityLost => {
                    ("waiting_auth", None)
                }
                FailureClass::RateLimited => ("waiting_retry", Some(retry_after_ms.unwrap_or(60_000))),
                FailureClass::TransientNetwork
                | FailureClass::PluginCrash
                | FailureClass::SourceUnavailable => ("waiting_retry", Some(backoff_ms(attempts as u32))),
                _ => ("failed", None),
            };
            let next_run_at = delay_ms.map(|d| plus_ms(&TraceTime::now(TimePrecision::Discovery).rfc3339, d));
            tx.execute(
                "UPDATE jobs SET state=?2, attempts=attempts+1, lease_owner=NULL,
                 lease_expires_at=NULL, last_error=?3, next_run_at=COALESCE(?4, next_run_at), updated_at=?5
                 WHERE job_id=?1",
                rusqlite::params![id, state, error_json, next_run_at, now()],
            )?;
            tx.execute(
                "INSERT INTO attempts (attempt_id, job_id, owner_instance_id, attempt_no, started_at, ended_at, result, error)
                 VALUES (?1,?2,'local',?3,?4,?5,'failed',?6)",
                rusqlite::params![format!("att-{}", uuid::Uuid::new_v4()), id, attempts + 1, error_json, now(), error_json],
            )?;
            Ok(())
        })
        .await
    }

    /// 取消：只影响未完成任务；已完成的保持终态。
    pub async fn cancel(&self, db: &DbHandle) -> Result<bool, CoreError> {
        let id = self.job_id.clone();
        db.write(move |tx| {
            let n = tx.execute(
                "UPDATE jobs SET state='cancelled', target_generation=target_generation+1, lease_owner=NULL, lease_expires_at=NULL, updated_at=?2
                 WHERE job_id=?1 AND state NOT IN ('succeeded','failed','cancelled')",
                rusqlite::params![id, now()],
            )?;
            Ok(n == 1)
        })
        .await
    }
}

/// 索引/派生事件计数（诊断）。
pub async fn counts(db: &DbHandle) -> Result<serde_json::Value, CoreError> {
    db.read(|c| {
        let mut stmt = c.prepare("SELECT state, count(*) FROM jobs GROUP BY state")?;
        let rows = stmt
            .query_map([], |r| Ok((r.get::<_, String>(0)?, r.get::<_, i64>(1)?)))?
            .collect::<Result<Vec<_>, _>>()?;
        let map: serde_json::Map<String, serde_json::Value> = rows
            .into_iter()
            .map(|(k, v)| (k, serde_json::Value::from(v)))
            .collect();
        Ok(serde_json::Value::Object(map))
    })
    .await
}

/// 指数退避：5s 起，封顶 30 分钟（design §9.3）。
pub fn backoff_ms(attempts: u32) -> u64 {
    let base = 5_000u64;
    let capped = attempts.min(12);
    base.saturating_mul(1u64 << capped).min(30 * 60 * 1000)
}

/// 日志/错误信息脱敏：URL 只留主机；疑似凭证形态的 token 逐个替换（C13）。
pub fn sanitize_message(msg: &str) -> String {
    msg.split_whitespace()
        .map(|w| {
            if w.starts_with("http://") || w.starts_with("https://") {
                let host = w
                    .split("://")
                    .nth(1)
                    .and_then(|rest| rest.split('/').next())
                    .unwrap_or("");
                format!("[url host={host}]")
            } else {
                let lower = w.to_lowercase();
                if lower.contains("sessdata")
                    || lower.contains("cookie:")
                    || lower.contains("cookie=")
                    || lower.contains("authorization:")
                    || lower.contains("password=")
                    || lower.contains("token=")
                {
                    "[redacted]".to_string()
                } else {
                    w.to_string()
                }
            }
        })
        .collect::<Vec<_>>()
        .join(" ")
}

fn plus_ms(rfc3339: &str, ms: u64) -> String {
    use time::format_description::well_known::Rfc3339;
    if let Ok(t) = time::OffsetDateTime::parse(rfc3339, &Rfc3339) {
        let t2 = t + time::Duration::milliseconds(ms as i64);
        t2.format(&Rfc3339).unwrap_or_else(|_| rfc3339.to_string())
    } else {
        rfc3339.to_string()
    }
}

/// DB 字符串 → 领域状态（free fn：JobState 定义于 domain）。
pub fn parse_job_state(s: &str) -> JobState {
    match s {
        "queued" => JobState::Queued,
        "leased" => JobState::Leased,
        "running" => JobState::Running,
        "waiting_retry" => JobState::WaitingRetry,
        "waiting_auth" => JobState::WaitingAuth,
        "waiting_plugin" => JobState::WaitingPlugin,
        "waiting_storage" => JobState::WaitingStorage,
        "waiting_user" => JobState::WaitingUser,
        "paused" => JobState::Paused,
        "succeeded" => JobState::Succeeded,
        "failed" => JobState::Failed,
        "cancelled" => JobState::Cancelled,
        _ => JobState::Queued,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn backoff_grows_and_caps() {
        assert_eq!(backoff_ms(0), 5_000);
        assert_eq!(backoff_ms(1), 10_000);
        assert!(backoff_ms(8) > backoff_ms(4));
        assert_eq!(backoff_ms(30), 30 * 60 * 1000);
    }

    #[test]
    fn sanitize_redacts_urls_and_credentials() {
        let m = sanitize_message(
            "failed GET https://api.bilibili.com/x?wts=abc&rid=1 session=SESSDATA=xxx",
        );
        assert!(!m.contains("wts=abc"));
        assert!(m.contains("host=api.bilibili.com"));
        assert!(m.contains("redacted"));
    }
}

#[cfg(test)]
#[path = "lease_tests.rs"]
mod lease_tests;
