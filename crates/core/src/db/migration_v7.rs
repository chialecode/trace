//! Schema 7: eight-site catalog, unified metadata, per-subscription auto-download,
//! download locations with external copies, library-location migration and proxy settings
//! (docs/design/multi-site-contracts.md §4–§9, §12).
//!
//! Existing subscriptions migrate to auto-download ON, so their behavior does not change;
//! new subscriptions choose their own value in code (default OFF). Nothing here rewrites
//! archived content: media rows keep `fetch_state='wanted'` and unified metadata is
//! projected from stored revisions by a maintenance pass.

pub const MIGRATION_007: &str = r#"
ALTER TABLE subscriptions ADD COLUMN auto_download INTEGER NOT NULL DEFAULT 1 CHECK(auto_download IN (0,1));
-- wanted: archive into the library; deferred: left undownloaded by the subscription setting.
ALTER TABLE media_items ADD COLUMN fetch_state TEXT NOT NULL DEFAULT 'wanted' CHECK(fetch_state IN ('wanted','deferred'));
CREATE INDEX idx_media_fetch_state ON media_items(fetch_state, revision_id);

CREATE TABLE entry_meta (
    entry_id TEXT PRIMARY KEY REFERENCES entries(entry_id),
    site TEXT NOT NULL,
    revision_id TEXT REFERENCES entry_revisions(revision_id),
    canonical_url TEXT,
    relation TEXT NOT NULL DEFAULT 'original' CHECK(relation IN ('original','repost','quote')),
    related_key TEXT,
    edited_at TEXT,
    language TEXT,
    client TEXT,
    location_name TEXT,
    ip_region TEXT,
    content_kinds TEXT NOT NULL DEFAULT '[]',
    image_count INTEGER NOT NULL DEFAULT 0,
    video_count INTEGER NOT NULL DEFAULT 0,
    max_duration_ms INTEGER,
    max_width INTEGER,
    max_height INTEGER,
    cover TEXT,
    collection TEXT,
    category TEXT,
    pinned INTEGER NOT NULL DEFAULT 0,
    age_restricted INTEGER NOT NULL DEFAULT 0,
    sponsored INTEGER NOT NULL DEFAULT 0,
    visibility TEXT,
    plays INTEGER,
    likes INTEGER,
    comments INTEGER,
    shares INTEGER,
    favorites INTEGER,
    coins INTEGER,
    danmaku INTEGER,
    stats_observed_at TEXT,
    updated_at TEXT NOT NULL
);
CREATE INDEX idx_meta_site ON entry_meta(site, entry_id);
CREATE INDEX idx_meta_plays ON entry_meta(plays);
CREATE INDEX idx_meta_duration ON entry_meta(max_duration_ms);
-- The explorer orders a library of a hundred thousand entries by name without sorting it.
CREATE INDEX idx_revisions_title ON entry_revisions(lower(title), entry_id);
CREATE INDEX idx_entries_recent ON entries((published_at IS NULL), published_at DESC, entry_id DESC);

CREATE TABLE entry_terms (
    entry_id TEXT NOT NULL REFERENCES entries(entry_id),
    kind TEXT NOT NULL CHECK(kind IN ('tag','mention')),
    value TEXT NOT NULL,
    PRIMARY KEY(entry_id, kind, value)
) WITHOUT ROWID;
CREATE INDEX idx_terms_value ON entry_terms(kind, value, entry_id);

-- Appended only when a counter changed, so history stays small and revisions stay untouched.
CREATE TABLE entry_stat_observations (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    entry_id TEXT NOT NULL REFERENCES entries(entry_id),
    observed_at TEXT NOT NULL,
    plays INTEGER, likes INTEGER, comments INTEGER, shares INTEGER,
    favorites INTEGER, coins INTEGER, danmaku INTEGER
);
CREATE INDEX idx_stat_entry ON entry_stat_observations(entry_id, sequence);

CREATE TABLE site_rules (
    rule_id TEXT PRIMARY KEY,
    site TEXT NOT NULL,
    plugin_id TEXT NOT NULL,
    adapter_version TEXT NOT NULL,
    declaration TEXT NOT NULL,
    status TEXT NOT NULL CHECK(status IN ('implemented','partial','unimplemented','retired')),
    declared INTEGER NOT NULL DEFAULT 1,
    declared_at TEXT NOT NULL
);
CREATE INDEX idx_rules_site ON site_rules(site, rule_id);

CREATE TABLE storage_locations (
    location_id TEXT PRIMARY KEY,
    kind TEXT NOT NULL CHECK(kind IN ('library','download')),
    path TEXT NOT NULL,
    label TEXT NOT NULL DEFAULT '',
    is_default INTEGER NOT NULL DEFAULT 0,
    state TEXT NOT NULL DEFAULT 'active' CHECK(state IN ('active','old_kept','released')),
    created_at TEXT NOT NULL,
    last_used_at TEXT,
    UNIQUE(kind, path)
);
CREATE UNIQUE INDEX idx_one_default_download ON storage_locations(kind) WHERE is_default=1;
CREATE UNIQUE INDEX idx_one_active_library ON storage_locations(kind) WHERE kind='library' AND state='active';

CREATE TABLE external_copies (
    copy_id TEXT PRIMARY KEY,
    entry_id TEXT REFERENCES entries(entry_id),
    media_id TEXT REFERENCES media_items(media_id),
    location_id TEXT NOT NULL REFERENCES storage_locations(location_id),
    rel_path TEXT NOT NULL,
    role TEXT NOT NULL CHECK(role IN ('media','sidecar','text')),
    size INTEGER NOT NULL DEFAULT 0,
    sha256 TEXT,
    state TEXT NOT NULL DEFAULT 'writing' CHECK(state IN ('writing','present','changed','missing')),
    created_at TEXT NOT NULL,
    checked_at TEXT,
    UNIQUE(location_id, rel_path)
);
CREATE INDEX idx_copies_entry ON external_copies(entry_id, state);
CREATE INDEX idx_copies_media ON external_copies(media_id);

CREATE TABLE library_migrations (
    migration_id TEXT PRIMARY KEY,
    from_path TEXT NOT NULL,
    to_path TEXT NOT NULL,
    state TEXT NOT NULL CHECK(state IN ('copying','verifying','ready','switched','failed','cancelled','released')),
    total_items INTEGER NOT NULL DEFAULT 0,
    done_items INTEGER NOT NULL DEFAULT 0,
    total_bytes INTEGER NOT NULL DEFAULT 0,
    done_bytes INTEGER NOT NULL DEFAULT 0,
    error TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    switched_at TEXT
);
CREATE UNIQUE INDEX idx_one_open_migration ON library_migrations((1)) WHERE state IN ('copying','verifying','ready');

CREATE TABLE library_migration_items (
    migration_id TEXT NOT NULL REFERENCES library_migrations(migration_id),
    -- path relative to the library root
    blob_id TEXT NOT NULL,
    size INTEGER NOT NULL,
    sha256 TEXT,
    state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','copied','verified')),
    PRIMARY KEY(migration_id, blob_id)
) WITHOUT ROWID;

CREATE TABLE network_settings (
    scope TEXT PRIMARY KEY,
    mode TEXT NOT NULL CHECK(mode IN ('direct','system','manual')),
    proxy_url TEXT,
    auth_ref TEXT,
    updated_at TEXT NOT NULL
);

CREATE TABLE app_settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

-- Pages of an author's profile the user is browsing before choosing what to download. The
-- items are what was shown, so a selection can only name something the user saw.
CREATE TABLE profile_previews (
    preview_id TEXT PRIMARY KEY,
    platform TEXT NOT NULL,
    rule_id TEXT NOT NULL,
    rule_title TEXT NOT NULL DEFAULT '',
    source_url TEXT NOT NULL,
    account_key TEXT NOT NULL,
    account_name TEXT,
    lane TEXT,
    credential_id TEXT,
    credential_generation INTEGER,
    authentication TEXT NOT NULL CHECK(authentication IN ('anonymous','authenticated')),
    pages INTEGER NOT NULL DEFAULT 0,
    next_cursor TEXT,
    has_more INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);
CREATE TABLE profile_preview_items (
    preview_id TEXT NOT NULL REFERENCES profile_previews(preview_id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    native_key TEXT NOT NULL,
    native_kind TEXT NOT NULL,
    title TEXT NOT NULL,
    PRIMARY KEY(preview_id, native_key)
) WITHOUT ROWID;

-- A download request: a linked entry or the entries chosen in a preview, and where their
-- readable copies go once every job of the request has settled.
CREATE TABLE deliveries (
    delivery_id TEXT PRIMARY KEY,
    kind TEXT NOT NULL CHECK(kind IN ('link','profile')),
    platform TEXT NOT NULL,
    source_url TEXT NOT NULL,
    request_id TEXT,
    scope_id TEXT REFERENCES subscriptions(subscription_id),
    -- {"kind":"library"} or {"kind":"folder","dir":"<absolute path or null for the default>"}
    destination TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'running' CHECK(state IN ('running','finishing','done','cancelled')),
    generation INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    finished_at TEXT
);
CREATE INDEX idx_deliveries_recent ON deliveries(created_at, delivery_id);
CREATE TABLE delivery_items (
    delivery_id TEXT NOT NULL REFERENCES deliveries(delivery_id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    native_key TEXT NOT NULL,
    native_kind TEXT NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    entry_id TEXT REFERENCES entries(entry_id),
    state TEXT NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','ready','exported','partial','failed','cancelled')),
    message TEXT,
    files INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(delivery_id, position)
) WITHOUT ROWID;
CREATE INDEX idx_delivery_items_entry ON delivery_items(entry_id);

-- source_requests.kind grows from video/live to any linked entry; SQLite cannot alter a CHECK.
CREATE TABLE source_requests_v7 (
    request_id TEXT PRIMARY KEY,
    platform TEXT NOT NULL,
    kind TEXT NOT NULL CHECK(kind IN ('video','live','entry')),
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
    expires_at TEXT NOT NULL,
    destination TEXT
);
INSERT INTO source_requests_v7(request_id,platform,kind,source_url,native_key,account_key,account_name,credential_id,credential_generation,authentication,validated_at,snapshot,selected,state,generation,scope_id,entry_id,job_id,session_id,created_at,expires_at)
    SELECT request_id,platform,kind,source_url,native_key,account_key,account_name,credential_id,credential_generation,authentication,validated_at,snapshot,selected,state,generation,scope_id,entry_id,job_id,session_id,created_at,expires_at FROM source_requests;
DROP TABLE source_requests;
ALTER TABLE source_requests_v7 RENAME TO source_requests;
CREATE INDEX idx_source_recent ON source_requests(created_at,request_id);
"#;
