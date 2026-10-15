//! DB actor：单线程 rusqlite 连接 + 有界队列（INV-06/09，windows-runtime-contracts §6）。
//!
//! 所有读写在专职线程串行执行；`write` 以 IMMEDIATE 事务包裹并在出错时回滚。
//! 队列满载时调用方 await 背压，不无限增长。

mod migration_v7;
#[cfg(test)]
mod migration_v7_tests;
mod migration_v8;
#[cfg(test)]
mod migration_v8_tests;
pub mod schema;

use crate::error::CoreError;
use rusqlite::Connection;
use std::any::Any;
use std::sync::Arc;
use std::thread::JoinHandle;
use tokio::sync::{mpsc, oneshot};

type Work = Box<dyn FnOnce(&mut Connection) -> Result<Box<dyn Any + Send>, CoreError> + Send>;

struct DbJob {
    work: Work,
    resp: oneshot::Sender<Result<Box<dyn Any + Send>, CoreError>>,
}

#[derive(Clone)]
pub struct DbHandle {
    tx: Arc<mpsc::Sender<DbJob>>,
}

pub struct DbActor {
    handle: Option<JoinHandle<()>>,
}

impl DbActor {
    /// 打开连接、执行迁移并启动 actor 线程。
    pub fn start(
        mut conn: Connection,
        queue_bound: usize,
    ) -> Result<(DbHandle, DbActor), CoreError> {
        schema::configure(&conn)?;
        schema::run_migrations(&mut conn).map_err(CoreError::Db)?;
        let (tx, mut rx) = mpsc::channel::<DbJob>(queue_bound);
        let handle = std::thread::Builder::new()
            .name("trace-db-actor".into())
            .spawn(move || {
                while let Some(job) = rx.blocking_recv() {
                    let DbJob { work, resp } = job;
                    let result = work(&mut conn);
                    let _ = resp.send(result);
                }
            })
            .map_err(|e| CoreError::Internal(format!("spawn db actor: {e}")))?;
        Ok((
            DbHandle { tx: Arc::new(tx) },
            DbActor {
                handle: Some(handle),
            },
        ))
    }
}

impl Drop for DbActor {
    fn drop(&mut self) {
        // 不 join：DbHandle 的发送端可能仍被后台任务持有，join 会与
        // actor 线程等待"全部发送端丢弃"互等形成死锁。约定：最后一个
        // DbHandle 丢弃后队列关闭，线程自行退出并析构 Connection
        // （关闭时 SQLite 做干净收尾；崩溃恢复路径由 recover() 覆盖）。
        let _ = self.handle.take();
    }
}

impl DbHandle {
    /// 执行一次短事务（IMMEDIATE）。闭包错误自动回滚。
    pub async fn write<T, F>(&self, f: F) -> Result<T, CoreError>
    where
        F: FnOnce(&rusqlite::Transaction) -> Result<T, CoreError> + Send + 'static,
        T: Send + 'static,
    {
        let ff = move |conn: &mut Connection| -> Result<Box<dyn Any + Send>, CoreError> {
            let tx = conn
                .transaction_with_behavior(rusqlite::TransactionBehavior::Immediate)
                .map_err(CoreError::Db)?;
            match f(&tx) {
                Ok(v) => {
                    tx.commit().map_err(CoreError::Db)?;
                    Ok(Box::new(v))
                }
                Err(e) => {
                    // 显式回滚后再返回错误。
                    let _ = tx.rollback();
                    Err(e)
                }
            }
        };
        self.downcast(self.send(Box::new(ff)).await?)
    }

    /// 执行一次只读查询（无事务包裹；短查询）。
    pub async fn read<T, F>(&self, f: F) -> Result<T, CoreError>
    where
        F: FnOnce(&Connection) -> Result<T, CoreError> + Send + 'static,
        T: Send + 'static,
    {
        let ff = move |conn: &mut Connection| f(conn).map(|t| Box::new(t) as Box<dyn Any + Send>);
        self.downcast(self.send(Box::new(ff)).await?)
    }

    fn downcast<T: 'static>(&self, boxed: Box<dyn Any + Send>) -> Result<T, CoreError> {
        boxed
            .downcast::<T>()
            .map(|b| *b)
            .map_err(|_| CoreError::Internal("db result type mismatch".into()))
    }

    async fn send(&self, work: Work) -> Result<Box<dyn Any + Send>, CoreError> {
        let (resp_tx, resp_rx) = oneshot::channel();
        self.tx
            .send(DbJob {
                work,
                resp: resp_tx,
            })
            .await
            .map_err(|_| CoreError::Internal("db actor closed".into()))?;
        resp_rx
            .await
            .map_err(|_| CoreError::Internal("db actor dropped job".into()))?
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    fn start_test_db() -> (DbHandle, TempDir) {
        let dir = TempDir::new().unwrap();
        let conn = Connection::open(dir.path().join("t.sqlite3")).unwrap();
        let (h, _a) = DbActor::start(conn, 64).unwrap();
        (h, dir)
    }

    #[tokio::test]
    async fn write_rolls_back_on_error() {
        let (h, _dir) = start_test_db();
        h.write(|tx| {
            tx.execute("CREATE TABLE t(a INTEGER)", [])?;
            tx.execute("INSERT INTO t VALUES (1)", [])?;
            Ok(())
        })
        .await
        .unwrap();
        let err = h
            .write(|tx| {
                tx.execute("INSERT INTO t VALUES (2)", [])?;
                Err::<(), _>(CoreError::InvalidInput("boom".into()))
            })
            .await
            .unwrap_err();
        assert!(matches!(err, CoreError::InvalidInput(_)));
        let count: i64 = h
            .read(|c| Ok(c.query_row("SELECT count(*) FROM t", [], |r| r.get(0))?))
            .await
            .unwrap();
        assert_eq!(count, 1, "second insert must be rolled back");
    }

    #[tokio::test]
    async fn concurrent_calls_serialize() {
        let (h, _dir) = start_test_db();
        h.write(|tx| {
            tx.execute("CREATE TABLE t(a INTEGER)", [])?;
            Ok(())
        })
        .await
        .unwrap();
        let h2 = h.clone();
        let (f1, f2) = tokio::join!(h.write(|_tx| Ok(1u32)), h2.write(|_tx| Ok(2u32)));
        assert_eq!((f1.unwrap(), f2.unwrap()), (1, 2));
    }
}
