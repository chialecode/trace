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
        (4, MIGRATION_004),
        (5, MIGRATION_005),
        (6, MIGRATION_006),
    ]
}

pub const MIGRATION_004: &str = r#"
ALTER TABLE search_state ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1;
ALTER TABLE search_state ADD COLUMN generation INTEGER NOT NULL DEFAULT 1;
ALTER TABLE search_state ADD COLUMN estimated_bytes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE search_state ADD COLUMN budget_bytes INTEGER NOT NULL DEFAULT 4294967296;
ALTER TABLE search_state ADD COLUMN budget_blocked INTEGER NOT NULL DEFAULT 0;
ALTER TABLE search_documents ADD COLUMN cache_bytes INTEGER NOT NULL DEFAULT 0;
UPDATE search_documents SET cache_bytes=length(CAST(normalized_text AS BLOB))*4+1024+128*((SELECT count(*) FROM search_unigram u WHERE u.doc_id=search_documents.doc_id)+(SELECT count(*) FROM search_bigram b WHERE b.doc_id=search_documents.doc_id));
UPDATE search_state SET estimated_bytes=(SELECT COALESCE(sum(cache_bytes),0) FROM search_documents);
CREATE TRIGGER cache_index_insert AFTER INSERT ON search_documents BEGIN UPDATE search_state SET estimated_bytes=estimated_bytes+NEW.cache_bytes WHERE id=1; END;
CREATE TRIGGER cache_index_delete AFTER DELETE ON search_documents BEGIN UPDATE search_state SET estimated_bytes=MAX(0,estimated_bytes-OLD.cache_bytes) WHERE id=1; END;
CREATE TABLE derived_cache_policy(id INTEGER PRIMARY KEY CHECK(id=1),budget_bytes INTEGER NOT NULL DEFAULT 10737418240,enabled INTEGER NOT NULL DEFAULT 1,generation INTEGER NOT NULL DEFAULT 1);
INSERT INTO derived_cache_policy(id) VALUES(1);
CREATE INDEX idx_variant_representation ON media_variants(source_representation,blob_id);
CREATE TABLE account_observations (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id TEXT NOT NULL REFERENCES source_accounts(account_id),
    display_name TEXT,
    observed_at TEXT NOT NULL,
    evidence TEXT NOT NULL
);
CREATE INDEX idx_account_observation ON account_observations(account_id,sequence);
ALTER TABLE subscriptions ADD COLUMN quality_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE credentials_state ADD COLUMN generation INTEGER NOT NULL DEFAULT 1;
CREATE TABLE issue_links (issue_key TEXT NOT NULL REFERENCES issues(issue_key),job_id TEXT NOT NULL,subscription_id TEXT,active INTEGER NOT NULL DEFAULT 1,PRIMARY KEY(issue_key,job_id));
CREATE INDEX idx_issue_job ON issue_links(job_id,active);
CREATE TABLE issue_notifications (issue_key TEXT PRIMARY KEY REFERENCES issues(issue_key),event TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'pending',attempts INTEGER NOT NULL DEFAULT 0,last_error TEXT,updated_at TEXT NOT NULL,version INTEGER NOT NULL DEFAULT 1);
INSERT INTO outbox(topic,aggregate_id,payload,created_at) SELECT 'entry.search_rebuild',entry_id,'{}',strftime('%Y-%m-%dT%H:%M:%SZ','now') FROM entries;
ALTER TABLE entries ADD COLUMN deletion_generation INTEGER NOT NULL DEFAULT 0;
ALTER TABLE entries ADD COLUMN media_suppressed INTEGER NOT NULL DEFAULT 0;
ALTER TABLE entries ADD COLUMN cache_suppressed INTEGER NOT NULL DEFAULT 0;
CREATE TABLE deleted_variants(variant_id TEXT PRIMARY KEY REFERENCES media_variants(variant_id),generation INTEGER NOT NULL);
CREATE INDEX idx_search_scope ON search_documents(doc_id,namespace,entry_id);
CREATE INDEX idx_entries_order ON entries(namespace,(published_at IS NULL),published_at DESC,entry_id DESC);
CREATE INDEX idx_entries_account ON entries(account_id);

ALTER TABLE entry_revisions ADD COLUMN document_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE entry_revisions ADD COLUMN metadata_json TEXT NOT NULL DEFAULT '{}';
CREATE INDEX idx_observation_revision ON source_observations(revision_id,observation_seq);
CREATE TABLE scan_cursor_visits (
    subscription_id TEXT NOT NULL REFERENCES subscriptions(subscription_id),
    scan_type TEXT NOT NULL,
    cursor_digest TEXT NOT NULL,
    PRIMARY KEY(subscription_id,scan_type,cursor_digest)
);
CREATE TABLE variant_retention (
    variant_id TEXT PRIMARY KEY REFERENCES media_variants(variant_id),
    retained INTEGER NOT NULL DEFAULT 1,
    pinned INTEGER NOT NULL DEFAULT 0,
    reason TEXT NOT NULL,
    eligible_at TEXT,
    updated_at TEXT NOT NULL
);
CREATE TABLE object_recycling (
    blob_id TEXT PRIMARY KEY REFERENCES blobs(blob_id),
    phase TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE VIEW current_entry_revisions AS
SELECT r.* FROM entry_revisions r WHERE r.revision_id=(
    SELECT candidate.revision_id FROM entry_revisions candidate
    WHERE candidate.entry_id=r.entry_id
    ORDER BY (candidate.completeness='full') DESC,
      COALESCE((SELECT MAX(o.observation_seq) FROM source_observations o
                WHERE o.revision_id=candidate.revision_id),0) DESC,
      candidate.observed_at DESC,candidate.revision_id DESC LIMIT 1
);
CREATE TABLE live_sessions (
    session_id TEXT PRIMARY KEY,
    subscription_id TEXT NOT NULL REFERENCES subscriptions(subscription_id),
    room_key TEXT NOT NULL,
    source_session TEXT NOT NULL,
    entry_id TEXT REFERENCES entries(entry_id),
    state TEXT NOT NULL,
    generation INTEGER NOT NULL DEFAULT 1,
    manual_stopped INTEGER NOT NULL DEFAULT 0,
    started_at TEXT NOT NULL,
    ended_at TEXT,
    stop_reason TEXT,
    evidence TEXT NOT NULL DEFAULT '{}',
    UNIQUE(subscription_id,room_key,source_session)
);
CREATE TABLE live_fragments (
    fragment_id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES live_sessions(session_id),
    sequence INTEGER NOT NULL,
    source_sequence TEXT NOT NULL,
    media_id TEXT REFERENCES media_items(media_id),
    state TEXT NOT NULL,
    start_ms INTEGER,
    end_ms INTEGER,
    discontinuity INTEGER NOT NULL DEFAULT 0,
    reason TEXT,
    evidence TEXT NOT NULL DEFAULT '{}',
    captured_at TEXT,
    retry_of TEXT REFERENCES live_fragments(fragment_id)
);
CREATE UNIQUE INDEX idx_live_active_source ON live_fragments(session_id,source_sequence)
    WHERE state IN ('receiving','sealed','gap','source');
CREATE INDEX idx_live_session_state ON live_sessions(state);
ALTER TABLE subscriptions ADD COLUMN capture_chat INTEGER NOT NULL DEFAULT 0;
ALTER TABLE subscriptions ADD COLUMN capture_subtitles INTEGER NOT NULL DEFAULT 0;
CREATE TABLE live_messages (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES live_sessions(session_id),
    source_id TEXT NOT NULL,
    source_time TEXT,
    author TEXT,
    body TEXT NOT NULL,
    kind TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    UNIQUE(session_id,source_id)
);
CREATE INDEX idx_live_messages_page ON live_messages(session_id,sequence);
CREATE TABLE live_auxiliary_samples (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES live_sessions(session_id),
    observed_at TEXT NOT NULL,
    coverage TEXT NOT NULL,
    source_count INTEGER NOT NULL DEFAULT 0,
    saved_count INTEGER NOT NULL DEFAULT 0,
    omitted INTEGER NOT NULL DEFAULT 0,
    failure_class TEXT
);
CREATE INDEX idx_live_auxiliary_page ON live_auxiliary_samples(session_id,sequence);
"#;

/// Discovery, pending consumers and an execution lease are separate identities.
/// Only relationships evidenced by old jobs are backfilled; account membership
/// alone is not evidence that a subscription discovered every archived entry.
pub const MIGRATION_005: &str = r#"
CREATE TABLE entry_subscriptions (
    entry_id TEXT NOT NULL REFERENCES entries(entry_id),
    subscription_id TEXT NOT NULL REFERENCES subscriptions(subscription_id),
    first_discovered_at TEXT NOT NULL,
    last_discovered_at TEXT NOT NULL,
    PRIMARY KEY(entry_id,subscription_id)
);
CREATE INDEX idx_discovery_subscription ON entry_subscriptions(subscription_id,entry_id);
CREATE TABLE job_consumers (
    job_id TEXT NOT NULL REFERENCES jobs(job_id),
    subscription_id TEXT NOT NULL REFERENCES subscriptions(subscription_id),
    requested INTEGER NOT NULL DEFAULT 1,
    completed INTEGER NOT NULL DEFAULT 0,
    state TEXT NOT NULL DEFAULT 'queued',
    attempts INTEGER NOT NULL DEFAULT 0,
    next_run_at TEXT,
    last_error TEXT,
    requested_at TEXT NOT NULL,
    PRIMARY KEY(job_id,subscription_id)
);
CREATE INDEX idx_consumer_subscription ON job_consumers(subscription_id,state);
INSERT OR IGNORE INTO entry_subscriptions
    SELECT e.entry_id,s.subscription_id,j.created_at,j.updated_at FROM jobs j
    JOIN entries e ON e.entry_id=j.subject_id
    JOIN subscriptions s ON s.subscription_id=json_extract(j.payload,'$.subscription_id')
    WHERE j.job_type IN ('observe','media');
INSERT INTO job_consumers(job_id,subscription_id,completed,state,attempts,next_run_at,last_error,requested_at)
    SELECT j.job_id,s.subscription_id,CASE WHEN j.state='succeeded' THEN 1 ELSE 0 END,
      CASE WHEN j.state IN ('leased','running','paused','waiting_plugin') THEN 'queued' ELSE j.state END,
      j.attempts,j.next_run_at,j.last_error,j.created_at
    FROM jobs j JOIN subscriptions s ON s.subscription_id=json_extract(j.payload,'$.subscription_id')
    WHERE j.job_type IN ('observe','media');
CREATE TABLE acquisition_contexts (
    context_id TEXT PRIMARY KEY,
    entry_id TEXT NOT NULL REFERENCES entries(entry_id),
    job_id TEXT,
    job_generation INTEGER,
    subscription_id TEXT,
    credential_id TEXT,
    credential_generation INTEGER,
    auth_state TEXT NOT NULL CHECK(auth_state IN ('anonymous','authenticated','expired','unknown')),
    validated_at TEXT,
    basis TEXT NOT NULL,
    started_at TEXT NOT NULL
);
CREATE INDEX idx_acquisition_entry_auth ON acquisition_contexts(entry_id,auth_state,started_at);
CREATE TABLE resource_acquisitions (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    acquisition_id TEXT NOT NULL UNIQUE,
    context_id TEXT NOT NULL REFERENCES acquisition_contexts(context_id),
    media_id TEXT REFERENCES media_items(media_id),
    variant_id TEXT REFERENCES media_variants(variant_id),
    outcome TEXT NOT NULL,
    finished_at TEXT NOT NULL,
    measured_spec TEXT NOT NULL DEFAULT '{}',
    UNIQUE(context_id,media_id,variant_id)
);
CREATE INDEX idx_resource_acquisition_media ON resource_acquisitions(media_id,sequence);
ALTER TABLE source_observations ADD COLUMN context_id TEXT REFERENCES acquisition_contexts(context_id);
ALTER TABLE live_fragments ADD COLUMN context_id TEXT REFERENCES acquisition_contexts(context_id);
ALTER TABLE media_variants ADD COLUMN approval_state TEXT NOT NULL DEFAULT 'automatic';
ALTER TABLE media_items ADD COLUMN resource_generation INTEGER NOT NULL DEFAULT 0;
INSERT INTO acquisition_contexts(context_id,entry_id,auth_state,basis,started_at)
    SELECT 'legacy:'||v.variant_id,r.entry_id,'unknown','legacy_without_authentication_evidence',v.created_at
    FROM media_variants v JOIN media_items m ON m.media_id=v.media_id JOIN entry_revisions r ON r.revision_id=m.revision_id;
INSERT INTO resource_acquisitions(acquisition_id,context_id,media_id,variant_id,outcome,finished_at,measured_spec)
    SELECT 'legacy:'||variant_id,'legacy:'||variant_id,media_id,variant_id,'legacy',created_at,COALESCE(measured_spec,'{}') FROM media_variants;
CREATE TRIGGER acquisition_context_immutable_update BEFORE UPDATE ON acquisition_contexts BEGIN SELECT RAISE(ABORT,'acquisition context is immutable'); END;
CREATE TRIGGER acquisition_context_immutable_delete BEFORE DELETE ON acquisition_contexts BEGIN SELECT RAISE(ABORT,'acquisition context is immutable'); END;
CREATE TRIGGER resource_acquisition_immutable_update BEFORE UPDATE ON resource_acquisitions BEGIN SELECT RAISE(ABORT,'resource acquisition is immutable'); END;
CREATE TRIGGER resource_acquisition_immutable_delete BEFORE DELETE ON resource_acquisitions BEGIN SELECT RAISE(ABORT,'resource acquisition is immutable'); END;
CREATE TABLE upgrade_candidates (
    candidate_id TEXT PRIMARY KEY,
    entry_id TEXT NOT NULL REFERENCES entries(entry_id),
    revision_id TEXT NOT NULL REFERENCES entry_revisions(revision_id),
    group_key TEXT NOT NULL,
    context_id TEXT NOT NULL REFERENCES acquisition_contexts(context_id),
    job_id TEXT NOT NULL REFERENCES jobs(job_id),
    job_generation INTEGER NOT NULL,
    subscription_id TEXT,
    subscription_version INTEGER,
    credential_id TEXT,
    credential_generation INTEGER,
    deletion_generation INTEGER NOT NULL,
    policy TEXT,
    state TEXT NOT NULL DEFAULT 'collecting',
    reason TEXT NOT NULL DEFAULT '',
    confirmation TEXT,
    created_at TEXT NOT NULL,
    finished_at TEXT
);
CREATE INDEX idx_upgrade_state ON upgrade_candidates(state,created_at,candidate_id);
CREATE TABLE upgrade_members (
    candidate_id TEXT NOT NULL REFERENCES upgrade_candidates(candidate_id),
    media_id TEXT NOT NULL REFERENCES media_items(media_id),
    old_variant_id TEXT REFERENCES media_variants(variant_id),
    new_variant_id TEXT REFERENCES media_variants(variant_id),
    required INTEGER NOT NULL,
    expected_media_generation INTEGER NOT NULL DEFAULT 0,
    old_pinned INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(candidate_id,media_id)
);
CREATE INDEX idx_upgrade_variant ON upgrade_members(new_variant_id);
"#;

pub const MIGRATION_006: &str = r#"
ALTER TABLE upgrade_candidates ADD COLUMN consumer_request INTEGER;
ALTER TABLE subscriptions ADD COLUMN acquisition_kind TEXT NOT NULL DEFAULT 'subscription' CHECK(acquisition_kind IN ('subscription','one_shot'));
CREATE TABLE source_requests (
    request_id TEXT PRIMARY KEY,
    platform TEXT NOT NULL,
    kind TEXT NOT NULL CHECK(kind IN ('video','live')),
    source_url TEXT NOT NULL,
    native_key TEXT NOT NULL,
    account_key TEXT NOT NULL,
    account_name TEXT,
    credential_id TEXT,
    credential_generation INTEGER,
    authentication TEXT NOT NULL,
    validated_at TEXT,
    snapshot TEXT NOT NULL,
    selected TEXT NOT NULL DEFAULT '[]',
    state TEXT NOT NULL DEFAULT 'preview',
    generation INTEGER NOT NULL DEFAULT 1,
    scope_id TEXT REFERENCES subscriptions(subscription_id),
    entry_id TEXT REFERENCES entries(entry_id),
    job_id TEXT REFERENCES jobs(job_id),
    session_id TEXT REFERENCES live_sessions(session_id),
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);
CREATE INDEX idx_source_recent ON source_requests(created_at,request_id);
-- One-shot scopes reuse credential/control checks, but can never enumerate an
-- author's content or become a recurring subscription through retry paths.
CREATE TRIGGER one_shot_no_scan BEFORE INSERT ON jobs
WHEN NEW.job_type IN ('scan','auth_completion','account_profile') AND EXISTS(
    SELECT 1 FROM subscriptions s WHERE s.acquisition_kind='one_shot' AND
    (s.subscription_id=NEW.subject_id OR s.subscription_id=json_extract(NEW.payload,'$.subscription_id')))
BEGIN SELECT RAISE(ABORT,'one-shot source cannot scan'); END;
CREATE TRIGGER one_shot_no_scan_update BEFORE UPDATE OF job_type,subject_id,payload ON jobs
WHEN NEW.job_type IN ('scan','auth_completion','account_profile') AND EXISTS(
    SELECT 1 FROM subscriptions s WHERE s.acquisition_kind='one_shot' AND
    (s.subscription_id=NEW.subject_id OR s.subscription_id=json_extract(NEW.payload,'$.subscription_id')))
BEGIN SELECT RAISE(ABORT,'one-shot source cannot scan'); END;
CREATE TABLE deletion_selections (
    entry_id TEXT NOT NULL REFERENCES entries(entry_id),
    generation INTEGER NOT NULL,
    media_id TEXT NOT NULL REFERENCES media_items(media_id),
    variant_id TEXT NOT NULL REFERENCES media_variants(variant_id),
    pinned INTEGER NOT NULL,
    policy_version TEXT NOT NULL,
    reason TEXT,
    PRIMARY KEY(entry_id,generation,media_id)
);
-- Old restores could select a candidate without approval. Keep its history,
-- remove only the invalid selection, then let maintenance select eligible bytes.
DELETE FROM asset_selections WHERE variant_id IN (
    SELECT variant_id FROM media_variants WHERE approval_state NOT IN ('automatic','accepted','kept')
);
CREATE TRIGGER selection_approval_insert BEFORE INSERT ON asset_selections
WHEN EXISTS(SELECT 1 FROM media_variants WHERE variant_id=NEW.variant_id AND approval_state NOT IN ('automatic','accepted','kept'))
BEGIN SELECT RAISE(ABORT,'resource approval required'); END;
CREATE TRIGGER selection_approval_update BEFORE UPDATE OF variant_id ON asset_selections
WHEN EXISTS(SELECT 1 FROM media_variants WHERE variant_id=NEW.variant_id AND approval_state NOT IN ('automatic','accepted','kept'))
BEGIN SELECT RAISE(ABORT,'resource approval required'); END;
CREATE TRIGGER selection_generation_insert AFTER INSERT ON asset_selections
BEGIN UPDATE media_items SET resource_generation=resource_generation+1 WHERE media_id=NEW.media_id; END;
CREATE TRIGGER selection_generation_update AFTER UPDATE OF variant_id,pinned ON asset_selections
WHEN OLD.variant_id IS NOT NEW.variant_id OR OLD.pinned!=NEW.pinned
BEGIN UPDATE media_items SET resource_generation=resource_generation+1 WHERE media_id=NEW.media_id; END;
CREATE TRIGGER selection_generation_delete AFTER DELETE ON asset_selections
BEGIN UPDATE media_items SET resource_generation=resource_generation+1 WHERE media_id=OLD.media_id; END;
"#;

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
    // Preserve a consistent SQLite snapshot before changing an existing schema.
    // The transaction below leaves schema 3 usable when migration fails.
    for (version, sql) in migrations() {
        if version <= current {
            continue;
        }
        if current > 0
            && let Some(path) = conn.path().filter(|p| !p.is_empty())
        {
            let backup = std::path::PathBuf::from(format!(
                "{path}.pre-schema-{version}-{}.sqlite3",
                uuid::Uuid::new_v4()
            ));
            conn.backup("main", backup, None)?;
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
    // Timeline rows reuse a bounded set of statements; cache prepared code,
    // never query results, so current state and authentication remain fresh.
    conn.set_prepared_statement_cache_capacity(64);
    conn.execute_batch(
        "PRAGMA journal_mode=WAL;
         PRAGMA synchronous=FULL;
         PRAGMA foreign_keys=ON;
         PRAGMA cache_size=-32768;
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
