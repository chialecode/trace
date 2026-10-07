//! SQLite schema 与迁移（docs/design.md §6.2，SCHEMA_VERSION 正本在 domain）。
//!
//! 每个迁移单事务执行；失败回滚到旧版本（C23）。FTS5 trigram + 单/双字
//! 候选表按 docs/design.md §13.2 建立。

use rusqlite::Connection;
use trace_domain::SCHEMA_VERSION;

pub const MIGRATION_001: &str = r#"
CREATE TABLE meta (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE creators (
    creator_id TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    aliases TEXT NOT NULL DEFAULT '[]',
    tags TEXT NOT NULL DEFAULT '[]',
    status TEXT NOT NULL DEFAULT 'active',
    notes TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE source_accounts (
    account_id TEXT PRIMARY KEY,
    namespace TEXT NOT NULL,
    native_account_key TEXT NOT NULL,
    display_name TEXT,
    profile_url TEXT,
    first_seen_at TEXT NOT NULL,
    profile_updated_at TEXT,
    UNIQUE(namespace, native_account_key)
);

CREATE TABLE account_bindings (
    binding_id TEXT PRIMARY KEY,
    creator_id TEXT NOT NULL REFERENCES creators(creator_id),
    account_id TEXT NOT NULL UNIQUE REFERENCES source_accounts(account_id),
    state TEXT NOT NULL DEFAULT 'active',
    created_at TEXT NOT NULL,
    ended_at TEXT
);

CREATE TABLE subscriptions (
    subscription_id TEXT PRIMARY KEY,
    account_id TEXT NOT NULL REFERENCES source_accounts(account_id),
    content_type TEXT NOT NULL,
    range_json TEXT NOT NULL,
    quality_policy_version TEXT NOT NULL DEFAULT 'v1',
    credential_id TEXT,
    config_version INTEGER NOT NULL DEFAULT 1,
    state TEXT NOT NULL DEFAULT 'enabled',
    check_interval_seconds INTEGER NOT NULL DEFAULT 300,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(account_id, content_type)
);

CREATE TABLE scan_checkpoints (
    subscription_id TEXT NOT NULL,
    scan_type TEXT NOT NULL,
    cursor TEXT,
    coverage TEXT NOT NULL DEFAULT '{}',
    generation INTEGER NOT NULL DEFAULT 1,
    updated_at TEXT,
    PRIMARY KEY (subscription_id, scan_type)
);

CREATE TABLE entries (
    entry_id TEXT PRIMARY KEY,
    namespace TEXT NOT NULL,
    native_kind TEXT NOT NULL,
    native_entry_key TEXT NOT NULL,
    account_id TEXT NOT NULL REFERENCES source_accounts(account_id),
    first_seen_at TEXT NOT NULL,
    published_at TEXT,
    published_precision TEXT,
    lifecycle TEXT NOT NULL DEFAULT 'active',
    UNIQUE(namespace, native_kind, native_entry_key)
);

CREATE TABLE entry_revisions (
    revision_id TEXT PRIMARY KEY,
    entry_id TEXT NOT NULL REFERENCES entries(entry_id),
    semantic_digest TEXT NOT NULL,
    title TEXT NOT NULL,
    normalized_body TEXT NOT NULL,
    source_version_evidence TEXT,
    completeness TEXT NOT NULL DEFAULT 'full',
    observed_at TEXT NOT NULL,
    UNIQUE(entry_id, semantic_digest)
);

CREATE TABLE source_observations (
    observation_id TEXT PRIMARY KEY,
    entry_id TEXT NOT NULL REFERENCES entries(entry_id),
    revision_id TEXT REFERENCES entry_revisions(revision_id),
    access_state TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    observation_seq INTEGER NOT NULL,
    evidence TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX idx_obs_entry ON source_observations(entry_id, observation_seq);

CREATE TABLE media_items (
    media_id TEXT PRIMARY KEY,
    revision_id TEXT NOT NULL REFERENCES entry_revisions(revision_id),
    role TEXT NOT NULL,
    position INTEGER NOT NULL,
    source_attachment_id TEXT NOT NULL,
    group_key TEXT,
    title TEXT,
    required INTEGER NOT NULL DEFAULT 1,
    UNIQUE(revision_id, role, position, source_attachment_id)
);

CREATE TABLE media_variants (
    variant_id TEXT PRIMARY KEY,
    media_id TEXT NOT NULL REFERENCES media_items(media_id),
    source_representation TEXT NOT NULL,
    blob_id TEXT NOT NULL REFERENCES blobs(blob_id),
    claimed_spec TEXT NOT NULL DEFAULT '{}',
    measured_spec TEXT,
    created_at TEXT NOT NULL
);
CREATE INDEX idx_variant_media ON media_variants(media_id);

CREATE TABLE blobs (
    blob_id TEXT PRIMARY KEY,
    size INTEGER NOT NULL,
    mime TEXT,
    structure_check TEXT,
    first_seen_at TEXT NOT NULL,
    UNIQUE(blob_id)
);

CREATE TABLE replicas (
    replica_id TEXT PRIMARY KEY,
    blob_id TEXT NOT NULL REFERENCES blobs(blob_id),
    node_id TEXT NOT NULL,
    store_id TEXT NOT NULL,
    locator TEXT NOT NULL,
    managed INTEGER NOT NULL DEFAULT 1,
    state TEXT NOT NULL DEFAULT 'staged',
    verified_at TEXT,
    UNIQUE(blob_id, store_id, locator)
);
CREATE INDEX idx_replica_blob ON replicas(blob_id);

CREATE TABLE asset_selections (
    media_id TEXT PRIMARY KEY REFERENCES media_items(media_id),
    variant_id TEXT NOT NULL REFERENCES media_variants(variant_id),
    policy_version TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    pinned INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL
);

CREATE TABLE jobs (
    job_id TEXT PRIMARY KEY,
    job_key TEXT NOT NULL UNIQUE,
    job_type TEXT NOT NULL,
    subject_id TEXT NOT NULL,
    payload TEXT NOT NULL DEFAULT '{}',
    target_generation INTEGER NOT NULL DEFAULT 1,
    config_version INTEGER NOT NULL DEFAULT 0,
    priority INTEGER NOT NULL DEFAULT 2,
    state TEXT NOT NULL DEFAULT 'queued',
    next_run_at TEXT,
    lease_owner TEXT,
    lease_expires_at TEXT,
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX idx_jobs_state ON jobs(state, next_run_at);
CREATE INDEX idx_jobs_subject ON jobs(subject_id);

CREATE TABLE attempts (
    attempt_id TEXT PRIMARY KEY,
    job_id TEXT NOT NULL REFERENCES jobs(job_id),
    owner_instance_id TEXT NOT NULL,
    attempt_no INTEGER NOT NULL,
    started_at TEXT NOT NULL,
    ended_at TEXT,
    result TEXT,
    error TEXT
);
CREATE INDEX idx_attempt_job ON attempts(job_id, attempt_no);

CREATE TABLE materializations (
    operation_id TEXT PRIMARY KEY,
    job_id TEXT REFERENCES jobs(job_id),
    subject_kind TEXT NOT NULL,
    subject_id TEXT NOT NULL,
    generation INTEGER NOT NULL DEFAULT 1,
    stage TEXT NOT NULL DEFAULT 'RECEIVING',
    staging_path TEXT,
    final_path TEXT,
    expected_size INTEGER,
    expected_sha256 TEXT,
    received_bytes INTEGER NOT NULL DEFAULT 0,
    received_sha256 TEXT,
    source_validator TEXT,
    blob_id TEXT,
    mime TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX idx_materialization_stage ON materializations(stage);

CREATE TABLE outbox (
    seq INTEGER PRIMARY KEY AUTOINCREMENT,
    topic TEXT NOT NULL,
    aggregate_id TEXT NOT NULL,
    payload TEXT NOT NULL,
    created_at TEXT NOT NULL,
    processed_at TEXT,
    consumer TEXT
);
CREATE INDEX idx_outbox_pending ON outbox(processed_at) WHERE processed_at IS NULL;

CREATE TABLE receipts (
    idempotency_key TEXT PRIMARY KEY,
    command TEXT NOT NULL,
    result_json TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE credentials_state (
    credential_id TEXT PRIMARY KEY,
    platform TEXT NOT NULL,
    subject TEXT,
    status TEXT NOT NULL DEFAULT 'unknown',
    capability TEXT NOT NULL DEFAULT '{}',
    validated_at TEXT,
    vault_ref TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE plugins_state (
    plugin_id TEXT PRIMARY KEY,
    platform TEXT NOT NULL,
    version TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'installed',
    protocol TEXT NOT NULL,
    capabilities TEXT NOT NULL DEFAULT '[]',
    binary_path TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    updated_at TEXT NOT NULL
);

CREATE TABLE deletion_records (
    record_id TEXT PRIMARY KEY,
    scope TEXT NOT NULL,
    object_id TEXT NOT NULL,
    generation INTEGER NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
);

CREATE TABLE issues (
    issue_key TEXT PRIMARY KEY,
    category TEXT NOT NULL,
    severity TEXT NOT NULL,
    detail TEXT NOT NULL,
    user_action TEXT,
    related_refs TEXT NOT NULL DEFAULT '[]',
    first_seen_at TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    resolved_at TEXT
);

CREATE TABLE search_documents (
    doc_id INTEGER PRIMARY KEY AUTOINCREMENT,
    doc_kind TEXT NOT NULL,
    ref_id TEXT NOT NULL,
    entry_id TEXT,
    creator_id TEXT,
    namespace TEXT,
    published_at TEXT,
    title TEXT,
    normalized_text TEXT NOT NULL,
    index_version INTEGER NOT NULL,
    UNIQUE(doc_kind, ref_id, index_version)
);
CREATE INDEX idx_search_docs_entry ON search_documents(entry_id);

CREATE TABLE search_unigram (
    gram TEXT NOT NULL,
    doc_id INTEGER NOT NULL REFERENCES search_documents(doc_id) ON DELETE CASCADE,
    PRIMARY KEY (gram, doc_id)
) WITHOUT ROWID;

CREATE TABLE search_bigram (
    gram TEXT NOT NULL,
    doc_id INTEGER NOT NULL REFERENCES search_documents(doc_id) ON DELETE CASCADE,
    PRIMARY KEY (gram, doc_id)
) WITHOUT ROWID;

CREATE TABLE search_state (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    indexed_seq INTEGER NOT NULL DEFAULT 0,
    index_version INTEGER NOT NULL DEFAULT 1,
    rebuilding INTEGER NOT NULL DEFAULT 0,
    rebuild_from_seq INTEGER
);

INSERT INTO search_state (id, indexed_seq, index_version) VALUES (1, 0, 1);

CREATE VIRTUAL TABLE search_fts USING fts5(normalized_text, content='search_documents', content_rowid='doc_id', tokenize='trigram');
"#;

/// 触发器保持 FTS 与文档表同步（索引可重建）。
pub const MIGRATION_002_TRIGGERS: &str = r#"
CREATE TRIGGER search_fts_insert AFTER INSERT ON search_documents BEGIN
    INSERT INTO search_fts(rowid, normalized_text) VALUES (new.doc_id, new.normalized_text);
END;
CREATE TRIGGER search_fts_delete AFTER DELETE ON search_documents BEGIN
    INSERT INTO search_fts(search_fts, rowid, normalized_text) VALUES ('delete', old.doc_id, old.normalized_text);
END;
CREATE TRIGGER search_fts_update AFTER UPDATE ON search_documents BEGIN
    INSERT INTO search_fts(search_fts, rowid, normalized_text) VALUES ('delete', old.doc_id, old.normalized_text);
    INSERT INTO search_fts(rowid, normalized_text) VALUES (new.doc_id, new.normalized_text);
END;
"#;

pub fn migrations() -> Vec<(i64, &'static str)> {
    vec![
        (1, MIGRATION_001),
        (2, MIGRATION_002_TRIGGERS),
        (
            3,
            "ALTER TABLE media_items ADD COLUMN acquisition_evidence TEXT;",
        ),
    ]
}

/// 执行未应用的迁移；每个迁移独立事务，失败即回滚（C23）。
pub fn run_migrations(conn: &mut Connection) -> Result<(), rusqlite::Error> {
    let current: i64 = {
        let q = conn.query_row(
            "SELECT name FROM sqlite_master WHERE type='table' AND name='meta'",
            [],
            |_| Ok(()),
        );
        if q.is_ok() {
            conn.query_row(
                "SELECT value FROM meta WHERE key='schema_version'",
                [],
                |r| r.get::<_, String>(0),
            )?
            .parse()
            .unwrap_or(0)
        } else {
            0
        }
    };
    for (version, sql) in migrations() {
        if version <= current {
            continue;
        }
        let tx = conn.transaction_with_behavior(rusqlite::TransactionBehavior::Immediate)?;
        tx.execute_batch(sql)?;
        tx.execute(
            "INSERT INTO meta(key, value) VALUES('schema_version', ?1)
             ON CONFLICT(key) DO UPDATE SET value=excluded.value",
            [version.to_string()],
        )?;
        tx.commit()?;
        tracing::info!(version, "migration applied");
    }
    let now: i64 = conn
        .query_row(
            "SELECT value FROM meta WHERE key='schema_version'",
            [],
            |r| r.get::<_, String>(0),
        )?
        .parse()
        .unwrap_or(0);
    if now > SCHEMA_VERSION {
        // 旧程序遇到更高 schema：拒绝写入（FR-DATA-004 / C23）。
        return Err(rusqlite::Error::ToSqlConversionFailure(
            format!("database schema {now} is newer than supported {SCHEMA_VERSION}").into(),
        ));
    }
    Ok(())
}

/// 数据库连接初始化：WAL、FULL 同步、外键、busy 超时。
pub fn configure(conn: &Connection) -> Result<(), rusqlite::Error> {
    conn.execute_batch(
        "PRAGMA journal_mode=WAL;
         PRAGMA synchronous=FULL;
         PRAGMA foreign_keys=ON;
         PRAGMA busy_timeout=5000;",
    )?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn migrations_apply_idempotently_and_reject_newer_schema() {
        let mut conn = Connection::open_in_memory().unwrap();
        configure(&conn).unwrap();
        run_migrations(&mut conn).unwrap();
        // 再跑一遍无副作用。
        run_migrations(&mut conn).unwrap();
        let v: String = conn
            .query_row(
                "SELECT value FROM meta WHERE key='schema_version'",
                [],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(v.parse::<i64>().unwrap(), migrations().len() as i64);

        // 模拟“新程序写过的库”：手工把版本调高，本程序必须拒绝。
        conn.execute("UPDATE meta SET value='999' WHERE key='schema_version'", [])
            .unwrap();
        assert!(
            run_migrations(&mut conn).is_err(),
            "must refuse newer schema"
        );
    }

    #[test]
    fn fts5_trigram_available() {
        let conn = Connection::open_in_memory().unwrap();
        conn.execute_batch(MIGRATION_001).unwrap();
        conn.execute_batch(MIGRATION_002_TRIGGERS).unwrap();
        conn.execute(
            "INSERT INTO search_documents(doc_kind, ref_id, normalized_text, index_version) VALUES('entry','e1','中文标题测试',1)",
            [],
        )
        .unwrap();
        // trigram 对 >=3 字查询有效。
        let n: i64 = conn
            .query_row(
                "SELECT count(*) FROM search_fts WHERE search_fts MATCH '\"文标题\"'",
                [],
                |r| r.get(0),
            )
            .unwrap();
        assert!(n >= 1);
    }
}
