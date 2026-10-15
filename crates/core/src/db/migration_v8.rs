//! Schema 8: the management and plugin capabilities the user added to this stage
//! (docs/delivery/stage-plan.md §11): explicit creator relations and profile history,
//! recorded replays, local imports, resource comparison, work groups, and plugin releases.
//!
//! Every table is additive. Nothing here rewrites archived content: entries, revisions,
//! objects and file paths keep their identity, and an import that indexes a file in place
//! only records what was observed. A failed migration leaves schema 7 usable (the runner
//! wraps each version in one transaction and keeps a snapshot first).

pub const MIGRATION_008: &str = r#"
-- Merging changes only the explicit binding: entries, blobs, replicas and file paths stay.
CREATE TABLE creator_redirects (
    source_creator_id TEXT PRIMARY KEY REFERENCES creators(creator_id),
    target_creator_id TEXT NOT NULL REFERENCES creators(creator_id),
    created_at TEXT NOT NULL,
    CHECK(source_creator_id != target_creator_id)
);

CREATE TABLE creator_operations (
    operation_id TEXT PRIMARY KEY,
    kind TEXT NOT NULL CHECK(kind IN ('merge','split','relink','candidate_confirm','candidate_reject')),
    source_creator_id TEXT,
    target_creator_id TEXT,
    account_ids TEXT NOT NULL DEFAULT '[]',
    counts TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL
);
CREATE INDEX idx_creator_operations_time ON creator_operations(created_at DESC, operation_id DESC);
CREATE INDEX idx_creator_operations_source ON creator_operations(source_creator_id, created_at DESC);

-- A candidate only proposes; the binding changes when the user confirms it.
CREATE TABLE identity_candidates (
    candidate_id TEXT PRIMARY KEY,
    account_id TEXT NOT NULL REFERENCES source_accounts(account_id),
    proposed_creator_id TEXT NOT NULL REFERENCES creators(creator_id),
    kind TEXT NOT NULL,
    score REAL NOT NULL,
    evidence_json TEXT NOT NULL DEFAULT '{}',
    state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','confirmed','rejected')),
    created_at TEXT NOT NULL,
    decided_at TEXT,
    UNIQUE(account_id, proposed_creator_id, kind)
);
CREATE INDEX idx_identity_candidates_state ON identity_candidates(state, created_at DESC, candidate_id DESC);

-- Profile observations keep the whitelisted fields the page showed; credentials never enter.
ALTER TABLE account_observations ADD COLUMN profile_json TEXT NOT NULL DEFAULT '{}';

CREATE TABLE live_replay_links (
    link_id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES live_sessions(session_id),
    entry_id TEXT NOT NULL REFERENCES entries(entry_id),
    state TEXT NOT NULL DEFAULT 'candidate' CHECK(state IN ('candidate','confirmed','rejected')),
    decision TEXT CHECK(decision IN ('keep_both','replay_primary')),
    coverage_json TEXT NOT NULL DEFAULT '{}',
    quality_json TEXT NOT NULL DEFAULT '{}',
    evidence_json TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL,
    decided_at TEXT,
    UNIQUE(session_id, entry_id)
);
CREATE INDEX idx_replay_entry ON live_replay_links(entry_id, state);

CREATE TABLE import_batches (
    batch_id TEXT PRIMARY KEY,
    mode TEXT NOT NULL CHECK(mode IN ('managed','in_place')),
    source_path TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','running','done','failed','cancelled')),
    target_creator_id TEXT REFERENCES creators(creator_id),
    account_id TEXT REFERENCES source_accounts(account_id),
    total_items INTEGER NOT NULL DEFAULT 0,
    done_items INTEGER NOT NULL DEFAULT 0,
    failed_items INTEGER NOT NULL DEFAULT 0,
    skipped_items INTEGER NOT NULL DEFAULT 0,
    total_bytes INTEGER NOT NULL DEFAULT 0,
    done_bytes INTEGER NOT NULL DEFAULT 0,
    error TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX idx_import_batches_recent ON import_batches(created_at DESC, batch_id DESC);

CREATE TABLE import_items (
    item_id TEXT PRIMARY KEY,
    batch_id TEXT NOT NULL REFERENCES import_batches(batch_id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    source_path TEXT NOT NULL,
    rel_path TEXT NOT NULL,
    size INTEGER NOT NULL DEFAULT 0,
    mtime TEXT,
    sha256 TEXT,
    blob_id TEXT REFERENCES blobs(blob_id),
    entry_id TEXT REFERENCES entries(entry_id),
    state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','copying','hashing','committed','failed','cancelled')),
    error TEXT,
    observed_size INTEGER,
    observed_mtime TEXT,
    checked_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(batch_id, rel_path)
);
CREATE INDEX idx_import_items_batch ON import_items(batch_id, position);
CREATE INDEX idx_import_items_state ON import_items(state);

-- A comparison decision is recorded, never acted on: nothing is deleted, merged or replaced.
CREATE TABLE resource_signatures (
    blob_id TEXT NOT NULL REFERENCES blobs(blob_id),
    kind TEXT NOT NULL,
    algorithm TEXT NOT NULL,
    signature TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY(blob_id, kind, algorithm)
);

CREATE TABLE resource_comparisons (
    comparison_id TEXT PRIMARY KEY,
    left_kind TEXT NOT NULL CHECK(left_kind IN ('entry','media','variant','blob')),
    left_id TEXT NOT NULL,
    right_kind TEXT NOT NULL CHECK(right_kind IN ('entry','media','variant','blob')),
    right_id TEXT NOT NULL,
    decision TEXT CHECK(decision IN ('not_duplicate','keep_both','different_versions')),
    evidence_json TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL,
    decided_at TEXT,
    UNIQUE(left_kind, left_id, right_kind, right_id)
);
CREATE INDEX idx_comparisons_decision ON resource_comparisons(decided_at DESC);

CREATE TABLE work_groups (
    group_id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE work_group_members (
    group_id TEXT NOT NULL REFERENCES work_groups(group_id) ON DELETE CASCADE,
    entry_id TEXT NOT NULL REFERENCES entries(entry_id),
    added_at TEXT NOT NULL,
    PRIMARY KEY(group_id, entry_id)
) WITHOUT ROWID;
CREATE INDEX idx_work_group_entry ON work_group_members(entry_id);

-- A batch acts only on the exact objects the preview showed; a used or stale token is refused.
CREATE TABLE work_batch_previews (
    token TEXT PRIMARY KEY,
    actions_json TEXT NOT NULL,
    items_json TEXT NOT NULL,
    scope_digest TEXT NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    used_at TEXT
);

-- Releases are side by side: the active row is a pointer, never an overwrite.
CREATE TABLE plugin_releases (
    plugin_id TEXT NOT NULL,
    version TEXT NOT NULL,
    manifest_json TEXT NOT NULL,
    path TEXT NOT NULL,
    digest TEXT NOT NULL,
    protocol TEXT NOT NULL,
    capabilities_json TEXT NOT NULL DEFAULT '[]',
    state TEXT NOT NULL DEFAULT 'installed' CHECK(state IN ('installed','active','superseded','failed')),
    installed_at TEXT NOT NULL,
    PRIMARY KEY(plugin_id, version)
);
CREATE INDEX idx_plugin_releases_active ON plugin_releases(plugin_id, state);

-- One audit row for every management action; stable IDs, actions and counts only.
CREATE TABLE management_operations (
    operation_id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    subject TEXT NOT NULL DEFAULT '',
    detail_json TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL
);
CREATE INDEX idx_management_operations_time ON management_operations(created_at DESC, operation_id DESC);
"#;
