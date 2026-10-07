//! 任务租约与幂等自动用例（C05/C10）：过期租约回收、终态不复活、
//! job_key 唯一、幂等收据不重复生效。

use super::*;
use crate::db::DbActor;
use rusqlite::Connection;
use tempfile::TempDir;

fn test_db() -> (DbHandle, DbActor, TempDir) {
    let dir = TempDir::new().unwrap();
    let conn = Connection::open(dir.path().join("t.sqlite3")).unwrap();
    let (h, a) = DbActor::start(conn, 64).unwrap();
    (h, a, dir)
}

async fn make_job(db: &DbHandle, key: &str) -> String {
    db.write(|tx| {
        tx.execute("INSERT OR IGNORE INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('a','fake','a','t')",[])?;
        tx.execute("INSERT OR IGNORE INTO subscriptions(subscription_id,account_id,content_type,range_json,state,created_at,updated_at) VALUES('sub-x','a','video','{}','enabled','t','t')",[])?;
        Ok(())
    }).await.unwrap();
    enqueue(
        db,
        key.to_string(),
        "scan",
        "sub-x",
        serde_json::json!({}),
        1,
        1,
        0,
    )
    .await
    .unwrap()
}

/// C05：过期/他主租约回收为 queued；终态任务不因回收而复活。
#[tokio::test]
async fn stale_leases_requeue_but_terminal_states_do_not_revive() {
    let (db, _a, _dir) = test_db();
    let job_id = make_job(&db, "scan:sub-x:incremental").await;

    // 模拟崩溃遗留：leased 且租约已过期。
    let id = job_id.clone();
    db.write(move |tx| {
        tx.execute(
            "UPDATE jobs SET state='leased', lease_owner='dead-instance',
             lease_expires_at='2000-01-01T00:00:00Z' WHERE job_id=?1",
            rusqlite::params![id],
        )?;
        Ok(())
    })
    .await
    .unwrap();
    let recovered = recover_stale_leases(&db, "new-instance").await.unwrap();
    assert_eq!(recovered, 1);
    let id_read = job_id.clone();
    let (state, owner): (String, Option<String>) = db
        .read(move |c| {
            Ok(c.query_row(
                "SELECT state, lease_owner FROM jobs WHERE job_id=?1",
                [&id_read],
                |r| Ok((r.get(0)?, r.get(1)?)),
            )?)
        })
        .await
        .unwrap();
    assert_eq!(state, "queued");
    assert!(owner.is_none(), "回收后不得保留旧租约持有者");

    // 终态：succeeded 不复活（INV-09/任务终态）。
    let id = job_id.clone();
    db.write(move |tx| {
        tx.execute(
            "UPDATE jobs SET state='succeeded', lease_owner='x', lease_expires_at='2000-01-01T00:00:00Z' WHERE job_id=?1",
            rusqlite::params![id],
        )?;
        Ok(())
    })
    .await
    .unwrap();
    let recovered2 = recover_stale_leases(&db, "new-instance").await.unwrap();
    assert_eq!(recovered2, 0, "终态任务不属于租约回收范围");
    let again = enqueue(
        &db,
        "scan:sub-x:incremental".into(),
        "scan",
        "sub-x",
        serde_json::json!({"again": true}),
        1,
        1,
        0,
    )
    .await
    .unwrap();
    assert_eq!(again, job_id, "job_key 唯一：既有终态任务不被重复创建");
    let id_read2 = job_id.clone();
    let state: String = db
        .read(move |c| {
            Ok(
                c.query_row("SELECT state FROM jobs WHERE job_id=?1", [&id_read2], |r| {
                    r.get(0)
                })?,
            )
        })
        .await
        .unwrap();
    assert_eq!(state, "succeeded", "终态不得被入队复活");
}

/// C10：幂等收据（同键重发不重复生效）由 dispatch 层保存；此处验证
/// 数据层约定：receipts 表主键唯一。
#[tokio::test]
async fn idempotency_receipt_key_is_unique() {
    let (db, _a, _dir) = test_db();
    let r1 = db
        .write(|tx| {
            tx.execute(
                "INSERT INTO receipts (idempotency_key, command, result_json, created_at) VALUES ('k1','creator.create','{}','t')",
                [],
            )?;
            Ok(())
        })
        .await;
    assert!(r1.is_ok());
    let r2 = db
        .write(|tx| {
            tx.execute(
                "INSERT INTO receipts (idempotency_key, command, result_json, created_at) VALUES ('k1','creator.create','{}','t')",
                [],
            )?;
            Ok(())
        })
        .await;
    assert!(r2.is_err(), "同一幂等键不得重复写入收据");
}

#[tokio::test]
async fn f004_due_retry_is_bounded_and_f005_old_attempt_cannot_finish() {
    let (db, _actor, _dir) = test_db();
    make_job(&db, "retry").await;
    let job = claim_due(&db, "instance", 1).await.unwrap().remove(0);
    job.mark_running(&db).await.unwrap();
    job.fail(&db, &FailureClass::TransientNetwork, "synthetic", None)
        .await
        .unwrap();
    assert!(
        claim_due(&db, "instance", 1).await.unwrap().is_empty(),
        "no busy retry before deadline"
    );
    db.write(|tx| {
        tx.execute("UPDATE jobs SET next_run_at='2000-01-01T00:00:00Z'", [])?;
        Ok(())
    })
    .await
    .unwrap();
    let retried = claim_due(&db, "instance", 1).await.unwrap().remove(0);
    assert_ne!(job.owner, retried.owner);
    assert!(job.mark_running(&db).await.is_err());
    retried.mark_running(&db).await.unwrap();
    retried.succeed(&db).await.unwrap();
    assert!(
        job.fail(&db, &FailureClass::TransientNetwork, "late", None)
            .await
            .is_err()
    );
    let state: String = db
        .read(|c| Ok(c.query_row("SELECT state FROM jobs", [], |r| r.get(0))?))
        .await
        .unwrap();
    assert_eq!(state, "succeeded");
    assert!(lease_expiry(&now()) > now());
}

#[tokio::test]
async fn f004_three_automatic_retries_then_visible_failure() {
    let (db, _actor, _dir) = test_db();
    make_job(&db, "bounded").await;
    for _ in 0..4 {
        db.write(|tx| {
            tx.execute("UPDATE jobs SET next_run_at='2000-01-01T00:00:00Z'", [])?;
            Ok(())
        })
        .await
        .unwrap();
        let job = claim_due(&db, "instance", 1).await.unwrap().remove(0);
        job.mark_running(&db).await.unwrap();
        job.fail(&db, &FailureClass::RateLimited, "synthetic", Some(1))
            .await
            .unwrap();
    }
    assert!(claim_due(&db, "instance", 1).await.unwrap().is_empty());
    let state: String = db
        .read(|c| Ok(c.query_row("SELECT state FROM jobs", [], |r| r.get(0))?))
        .await
        .unwrap();
    assert_eq!(state, "failed");
}
