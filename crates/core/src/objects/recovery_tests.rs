//! 对象库自动用例（C03/C04/C08）：恢复矩阵、重复字节复用、损坏隔离、
//! replica 完整性标记。作为 `objects` 模块的子模块编译（见 mod.rs 末尾）。

use super::*;
use crate::config::Profile;
use crate::db::DbActor;
use rusqlite::Connection;
use tempfile::TempDir;

struct Env {
    _dir: TempDir,
    profile: Profile,
    db: DbHandle,
    _actor: DbActor,
    store: ObjectStore,
}

fn env() -> Env {
    let dir = TempDir::new().unwrap();
    let profile = Profile {
        archive_id: "t".into(),
        data_root: dir.path().join("data"),
        archive_root: dir.path().join("archive"),
        ..Profile::default()
    };
    std::fs::create_dir_all(profile.data_root.join("state")).unwrap();
    let conn = Connection::open(profile.db_path()).unwrap();
    let (db, actor) = DbActor::start(conn, 64).unwrap();
    let store = ObjectStore::new(profile.clone());
    Env {
        _dir: dir,
        profile,
        db,
        _actor: actor,
        store,
    }
}

fn params(subject: &str) -> ReceiveParams {
    ReceiveParams {
        job_id: None,
        subject_kind: "media_variant".into(),
        subject_id: subject.into(),
        generation: 1,
        mime: Some("video/mp4".into()),
        expected_size: None,
        expected_sha256: None,
        source_validator: None,
    }
}

fn sha256_hex(bytes: &[u8]) -> String {
    use sha2::Digest;
    let mut h = sha2::Sha256::new();
    h.update(bytes);
    h.finalize().iter().map(|b| format!("{b:02x}")).collect()
}

async fn receive_full(env: &Env, op: &str, bytes: &[u8]) -> String {
    let op_id = OperationId::new(op.to_string());
    let mut w = env
        .store
        .start_receiving(&env.db, params(op), &op_id)
        .await
        .unwrap();
    w.write_chunk(&env.db, &op_id, bytes).await.unwrap();
    let (_, sha) = env
        .store
        .finish_and_validate(&env.db, &op_id, w)
        .await
        .unwrap();
    sha
}

/// C04：相同字节合法复用；损坏目标隔离到 .trash，不静默覆盖。
#[tokio::test]
async fn duplicate_bytes_reuse_and_corrupt_target_isolated() {
    let env = env();
    let bytes = b"TRACE-OBJECT-TEST-PAYLOAD-0123456789".repeat(64);
    let sha = receive_full(&env, "mz-op1", &bytes).await;
    assert_eq!(sha, sha256_hex(&bytes));

    let op1 = OperationId::new("mz-op1".to_string());
    let out1 = env.store.install_object(&env.db, &op1, &sha).await.unwrap();
    assert!(matches!(out1, InstallOutcome::Installed), "{out1:?}");
    let path = env.store.object_path(&sha).unwrap();
    assert!(path.exists());

    // 第二来源同字节：复用既有对象，不重复落盘。
    receive_full(&env, "mz-op2", &bytes).await;
    let op2 = OperationId::new("mz-op2".to_string());
    let out2 = env.store.install_object(&env.db, &op2, &sha).await.unwrap();
    assert!(matches!(out2, InstallOutcome::ReusedExisting), "{out2:?}");

    // 损坏既有对象：安装先隔离到 .trash，再落新件。
    std::fs::write(&path, b"corrupted-bytes").unwrap();
    receive_full(&env, "mz-op3", &bytes).await;
    let op3 = OperationId::new("mz-op3".to_string());
    let out3 = env.store.install_object(&env.db, &op3, &sha).await.unwrap();
    assert!(matches!(out3, InstallOutcome::Installed), "{out3:?}");
    assert_eq!(hash_file(&path).unwrap(), sha, "重新安装后对象应为正确字节");
    let trash: Vec<_> = std::fs::read_dir(env.profile.trash_dir())
        .unwrap()
        .flatten()
        .map(|e| e.file_name().to_string_lossy().to_string())
        .collect();
    assert!(
        trash.iter().any(|n| n.starts_with("corrupt-")),
        "损坏对象必须隔离保留证据：{trash:?}"
    );
}

/// C03：崩溃窗口可重入——RECEIVING 截断到检查点、PREPARED 重算安装、
/// COMMITTED 残留 staging 清理，均不误判成功。
#[tokio::test]
async fn recovery_matrix_is_reentrant() {
    let env = env();
    let op_id = OperationId::new("mz-rec1".to_string());
    let mut w = env
        .store
        .start_receiving(&env.db, params("s1"), &op_id)
        .await
        .unwrap();
    w.write_chunk(&env.db, &op_id, b"partial-data")
        .await
        .unwrap();
    let staging = env.profile.staging_dir().join("mz-rec1").join("payload");
    // 模拟掉电撕裂写：检查点之外的字节不可信。
    {
        use std::io::Write as _;
        let mut f = std::fs::OpenOptions::new()
            .append(true)
            .open(&staging)
            .unwrap();
        f.write_all(b"torn-bytes-beyond-checkpoint").unwrap();
    }
    drop(w);
    let s = env.store.recover_materializations(&env.db).await.unwrap();
    assert!(s.resumed_receiving >= 1, "{s:?}");
    assert_eq!(
        std::fs::metadata(&staging).unwrap().len(),
        0,
        "RECEIVING 必须截断到已持久化偏移"
    );

    // 续跑：同一操作重新接收并完成（PREPARED，未业务提交）。
    let mut w2 = env
        .store
        .start_receiving(&env.db, params("s1"), &op_id)
        .await
        .unwrap();
    w2.write_chunk(&env.db, &op_id, b"partial-data")
        .await
        .unwrap();
    let (_, sha) = env
        .store
        .finish_and_validate(&env.db, &op_id, w2)
        .await
        .unwrap();

    // 模拟崩溃后重启：PREPARED 重算并安装，不直接登记成功。
    let s2 = env.store.recover_materializations(&env.db).await.unwrap();
    assert!(s2.recommitted >= 1, "{s2:?}");
    assert!(env.store.object_path(&sha).unwrap().exists());

    // COMMITTED 残留 staging：清理但不撤销档案（§6.4-7）。
    let stage_dir = env.profile.staging_dir().join("mz-rec1");
    std::fs::create_dir_all(&stage_dir).unwrap();
    std::fs::write(stage_dir.join("payload"), b"leftover").unwrap();
    let stage_str = stage_dir.to_string_lossy().to_string();
    let op2 = "mz-rec1".to_string();
    env.db
        .write(move |tx| {
            tx.execute(
                "UPDATE materializations SET stage='COMMITTED', staging_path=?2 WHERE operation_id=?1",
                rusqlite::params![op2, stage_str],
            )?;
            Ok(())
        })
        .await
        .unwrap();
    let s3 = env.store.recover_materializations(&env.db).await.unwrap();
    assert!(s3.cleaned_staging >= 1, "{s3:?}");
    assert!(!stage_dir.exists(), "COMMITTED 残留 staging 必须清理");
}

/// C08：verified 对象缺失/损坏被真实标记，不冒充可用。
#[tokio::test]
async fn verify_replicas_marks_absent_and_corrupt() {
    let env = env();
    let bytes = b"TRACE-REPLICA-TEST".repeat(32);
    let sha = receive_full(&env, "mz-rep1", &bytes).await;
    let op = OperationId::new("mz-rep1".to_string());
    env.store.install_object(&env.db, &op, &sha).await.unwrap();
    let blob_id = format!("sha256:{sha}");
    let sha_db = sha.clone();
    env.db
        .write(move |tx| {
            tx.execute(
                "INSERT OR IGNORE INTO blobs (blob_id, size, mime, first_seen_at) VALUES (?1,?2,'video/mp4','t')",
                rusqlite::params![blob_id, 512],
            )?;
            tx.execute(
                "INSERT INTO replicas (replica_id, blob_id, node_id, store_id, locator, managed, state, verified_at)
                 VALUES ('repl-test', ?1, 'local', 'primary', ?2, 1, 'verified', 't')",
                rusqlite::params![blob_id, sha_db],
            )?;
            Ok(())
        })
        .await
        .unwrap();

    // 完好：0。
    assert_eq!(env.store.verify_replicas(&env.db, 100).await.unwrap(), 0);

    // 缺失：absent。
    std::fs::remove_file(env.store.object_path(&sha).unwrap()).unwrap();
    assert!(env.store.verify_replicas(&env.db, 100).await.unwrap() >= 1);
    let state: String = env
        .db
        .read(|c| {
            Ok(c.query_row(
                "SELECT state FROM replicas WHERE replica_id='repl-test'",
                [],
                |r| r.get(0),
            )?)
        })
        .await
        .unwrap();
    assert_eq!(state, "absent");

    // 损坏：corrupt（旧 succeeded 不阻止重新取回新代次）。
    std::fs::create_dir_all(env.store.object_path(&sha).unwrap().parent().unwrap()).unwrap();
    std::fs::write(env.store.object_path(&sha).unwrap(), b"bad").unwrap();
    env.db
        .write(|tx| {
            tx.execute(
                "UPDATE replicas SET state='verified' WHERE replica_id='repl-test'",
                [],
            )?;
            Ok(())
        })
        .await
        .unwrap();
    assert!(env.store.verify_replicas(&env.db, 100).await.unwrap() >= 1);
    let state: String = env
        .db
        .read(|c| {
            Ok(c.query_row(
                "SELECT state FROM replicas WHERE replica_id='repl-test'",
                [],
                |r| r.get(0),
            )?)
        })
        .await
        .unwrap();
    assert_eq!(state, "corrupt");
}

#[tokio::test]
async fn invalid_validation_never_installs_and_can_retry() {
    let env = env();
    let op = OperationId::new("validation-test");
    let mut p = params("x");
    p.expected_size = Some(4);
    let mut w = env
        .store
        .start_receiving(&env.db, p.clone(), &op)
        .await
        .unwrap();
    w.write_chunk(&env.db, &op, b"bad").await.unwrap();
    assert!(
        env.store
            .finish_and_validate(&env.db, &op, w)
            .await
            .is_err()
    );
    env.store.recover_materializations(&env.db).await.unwrap();
    assert!(!env.store.object_path(&sha256_hex(b"bad")).unwrap().exists());
    let mut w = env.store.start_receiving(&env.db, p, &op).await.unwrap();
    w.write_chunk(&env.db, &op, b"good").await.unwrap();
    let (_, sha) = env
        .store
        .finish_and_validate(&env.db, &op, w)
        .await
        .unwrap();
    env.store.install_object(&env.db, &op, &sha).await.unwrap();
    // Installed-before-commit restart must reuse the final file without a staging payload.
    env.store.recover_materializations(&env.db).await.unwrap();
    assert_eq!(env.store.read_range(&sha, 0, 4).unwrap(), b"good");
}

#[tokio::test]
async fn checkpoint_resume_hashes_entire_file() {
    let env = env();
    let op = OperationId::new("resume-test");
    let prefix = vec![1; crate::config::CHECKPOINT_INTERVAL_BYTES as usize];
    let mut w = env
        .store
        .start_receiving(&env.db, params("x"), &op)
        .await
        .unwrap();
    w.write_chunk(&env.db, &op, &prefix).await.unwrap();
    drop(w);
    let mut w = env
        .store
        .start_receiving(&env.db, params("x"), &op)
        .await
        .unwrap();
    w.write_chunk(&env.db, &op, b"tail").await.unwrap();
    let (size, sha) = env
        .store
        .finish_and_validate(&env.db, &op, w)
        .await
        .unwrap();
    let mut all = prefix;
    all.extend_from_slice(b"tail");
    assert_eq!(size, all.len() as u64);
    assert_eq!(sha, sha256_hex(&all));
}

#[tokio::test]
async fn cancelled_and_stale_commit_are_rejected() {
    let env = env();
    env.db.write(|tx|{
        tx.execute("INSERT INTO source_accounts(account_id,namespace,native_account_key,first_seen_at) VALUES('a','fake','a','t')",[])?;
        tx.execute("INSERT INTO entries(entry_id,namespace,native_kind,native_entry_key,account_id,first_seen_at) VALUES('e','fake','video','e','a','t')",[])?;
        tx.execute("INSERT INTO entry_revisions(revision_id,entry_id,semantic_digest,title,normalized_body,observed_at) VALUES('r','e','x','x','','t')",[])?;
        tx.execute("INSERT INTO media_items(media_id,revision_id,role,position,source_attachment_id) VALUES('m','r','video',0,'m')",[])?;
        Ok(())
    }).await.unwrap();
    crate::jobs::enqueue(
        &env.db,
        "media".into(),
        "media",
        "e",
        serde_json::json!({}),
        1,
        1,
        0,
    )
    .await
    .unwrap();
    let job = crate::jobs::claim_due(&env.db, "instance", 1)
        .await
        .unwrap()
        .remove(0);
    job.mark_running(&env.db).await.unwrap();
    let op = OperationId::new("commit-test");
    let mut p = params("m");
    p.job_id = Some(job.job_id.clone());
    let mut w = env.store.start_receiving(&env.db, p, &op).await.unwrap();
    w.write_chunk(&env.db, &op, b"valid").await.unwrap();
    let (_, sha) = env
        .store
        .finish_and_validate(&env.db, &op, w)
        .await
        .unwrap();
    env.store.install_object(&env.db, &op, &sha).await.unwrap();
    job.cancel(&env.db).await.unwrap();
    let result = env
        .store
        .commit(
            &env.db,
            &op,
            &CommitInfo {
                job: Some(job),
                media_id: "m".into(),
                source_representation: "test".into(),
                claimed_spec: serde_json::json!({}),
            },
            None,
        )
        .await;
    assert!(matches!(result, Err(CoreError::Cancelled)));
    let n: i64 = env
        .db
        .read(|c| Ok(c.query_row("SELECT count(*) FROM media_variants", [], |r| r.get(0))?))
        .await
        .unwrap();
    assert_eq!(n, 0);
}

#[tokio::test]
async fn replica_cursor_reaches_beyond_first_sixty_four() {
    let env = env();
    let bytes = b"cursor-valid";
    let sha = receive_full(&env, "cursor-object", bytes).await;
    env.store
        .install_object(&env.db, &OperationId::new("cursor-object"), &sha)
        .await
        .unwrap();
    let sh = sha.clone();
    env.db.write(move |tx|{
        tx.execute("INSERT INTO blobs(blob_id,size,first_seen_at) VALUES(?1,12,'t')",[format!("sha256:{sh}")])?;
        for i in 0..70 {tx.execute("INSERT INTO replicas(replica_id,blob_id,node_id,store_id,locator,state) VALUES(?1,?2,'local',?1,?3,'verified')",rusqlite::params![format!("rep-{i:03}"),format!("sha256:{sh}"),if i==69{"b".repeat(64)}else{sh.clone()}])?;}
        Ok(())
    }).await.unwrap();
    assert_eq!(env.store.verify_replicas(&env.db, 64).await.unwrap(), 0);
    assert_eq!(env.store.verify_replicas(&env.db, 64).await.unwrap(), 1);
    let state: String = env
        .db
        .read(|c| {
            Ok(c.query_row(
                "SELECT state FROM replicas WHERE replica_id='rep-069'",
                [],
                |r| r.get(0),
            )?)
        })
        .await
        .unwrap();
    assert_eq!(state, "absent");
}

#[tokio::test]
async fn recovery_bounds_committed_cleanup_writes_and_keeps_objects() {
    let env = env();
    let bytes = b"verified bytes remain readable after staging cleanup";
    let sha = receive_full(&env, "protected", bytes).await;
    env.store
        .install_object(&env.db, &OperationId::new("protected"), &sha)
        .await
        .unwrap();
    let staging = env.profile.staging_dir();
    let existing = staging.join("cleanup-0000");
    std::fs::create_dir_all(&existing).unwrap();
    std::fs::write(existing.join("payload"), b"committed leftover").unwrap();
    env.db.write(move |tx| {
        tx.execute("UPDATE materializations SET stage='COMMITTED' WHERE operation_id='protected'",[])?;
        for i in 0..4096 {
            let op=format!("cleanup-{i:04}");
            tx.execute("INSERT INTO materializations(operation_id,subject_kind,subject_id,generation,stage,staging_path,created_at,updated_at) VALUES(?1,'live_fragment','historical-fragment',1,'COMMITTED',?2,'t','t')",rusqlite::params![op,staging.join(&op).to_string_lossy()])?;
        }
        Ok(())
    }).await.unwrap();
    env.db
        .read(|c| {
            c.execute_batch("PRAGMA wal_autocheckpoint=0; PRAGMA wal_checkpoint(TRUNCATE);")?;
            Ok(())
        })
        .await
        .unwrap();
    let started = std::time::Instant::now();
    let summary = env.store.recover_materializations(&env.db).await.unwrap();
    let frames: i64 = env
        .db
        .read(|c| Ok(c.query_row("PRAGMA wal_checkpoint(PASSIVE)", [], |r| r.get(1))?))
        .await
        .unwrap();
    eprintln!(
        "committed cleanup: {} rows, {frames} WAL frames, {:?}",
        summary.cleaned_staging,
        started.elapsed()
    );
    assert_eq!(summary.cleaned_staging, 4097);
    assert!(!existing.exists());
    assert_eq!(
        std::fs::read(env.store.object_path(&sha).unwrap()).unwrap(),
        bytes
    );
    let (rows,pending):(i64,i64)=env.db.read(|c|Ok(c.query_row("SELECT count(*),sum(staging_path IS NOT NULL) FROM materializations WHERE stage='COMMITTED'",[],|r|Ok((r.get(0)?,r.get(1)?)))?)).await.unwrap();
    assert_eq!((rows, pending), (4097, 0));
    assert_eq!(
        env.store
            .recover_materializations(&env.db)
            .await
            .unwrap()
            .cleaned_staging,
        0
    );
    // WAL frames measure durable write amplification, independently of machine
    // speed. Cleaning an already absent directory must not fsync per old row.
    assert!(
        frames > 0 && frames < 2048,
        "historical cleanup wrote {frames} WAL frames"
    );
    let outside = env._dir.path().join("unowned-files");
    std::fs::create_dir_all(&outside).unwrap();
    std::fs::write(outside.join("preserve"), b"outside the staging root").unwrap();
    let path = outside.to_string_lossy().to_string();
    env.db.write(move |tx| {
        tx.execute("INSERT INTO materializations(operation_id,subject_kind,subject_id,stage,staging_path,created_at,updated_at) VALUES('unowned','live_fragment','history','COMMITTED',?1,'t','t')",[path])?;
        Ok(())
    }).await.unwrap();
    assert!(env.store.recover_materializations(&env.db).await.is_err());
    assert_eq!(
        std::fs::read(outside.join("preserve")).unwrap(),
        b"outside the staging root"
    );
}
