PRAGMA user_version = 1;

CREATE TABLE library_items (
	item_key TEXT PRIMARY KEY,
	site TEXT NOT NULL,
	kind TEXT NOT NULL,
	raw_id TEXT NOT NULL,
	mid INTEGER NOT NULL DEFAULT 0,
	author_name TEXT NOT NULL DEFAULT '',
	author_face TEXT NOT NULL DEFAULT '',
	title TEXT NOT NULL DEFAULT '',
	summary TEXT NOT NULL DEFAULT '',
	cover_path TEXT NOT NULL DEFAULT '',
	canonical_url TEXT NOT NULL DEFAULT '',
	pub_ts INTEGER NOT NULL DEFAULT 0,
	cached_at TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT 'complete',
	item_dir TEXT NOT NULL DEFAULT '',
	total_bytes INTEGER NOT NULL DEFAULT 0,
	upgrade_available INTEGER NOT NULL DEFAULT 0,
	upgrade_hint TEXT NOT NULL DEFAULT '',
	json_data TEXT NOT NULL
);

CREATE INDEX idx_library_items_pub_ts ON library_items(pub_ts DESC);
CREATE INDEX idx_library_items_mid ON library_items(mid);

CREATE TABLE item_refs (
	from_key TEXT NOT NULL,
	to_key TEXT NOT NULL,
	PRIMARY KEY (from_key, to_key)
);

CREATE INDEX idx_item_refs_to ON item_refs(to_key);

CREATE TABLE feed_items (
	feed_key TEXT PRIMARY KEY,
	site TEXT NOT NULL,
	kind TEXT NOT NULL,
	raw_id TEXT NOT NULL,
	mid INTEGER NOT NULL DEFAULT 0,
	author_name TEXT NOT NULL DEFAULT '',
	author_face TEXT NOT NULL DEFAULT '',
	title TEXT NOT NULL DEFAULT '',
	summary TEXT NOT NULL DEFAULT '',
	cover_url TEXT NOT NULL DEFAULT '',
	canonical_url TEXT NOT NULL DEFAULT '',
	pub_ts INTEGER NOT NULL DEFAULT 0,
	discovered_at TEXT NOT NULL DEFAULT '',
	cached INTEGER NOT NULL DEFAULT 0,
	cache_status TEXT NOT NULL DEFAULT '',
	item_key TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_feed_items_pub_ts ON feed_items(pub_ts DESC);
CREATE INDEX idx_feed_items_mid ON feed_items(mid);

CREATE TABLE download_tasks (
	task_id TEXT PRIMARY KEY,
	site TEXT NOT NULL,
	kind TEXT NOT NULL,
	source TEXT NOT NULL DEFAULT 'manual',
	target_id TEXT NOT NULL,
	cid INTEGER NOT NULL DEFAULT 0,
	page INTEGER NOT NULL DEFAULT 1,
	title TEXT NOT NULL DEFAULT '',
	part_title TEXT NOT NULL DEFAULT '',
	mid INTEGER NOT NULL DEFAULT 0,
	author_name TEXT NOT NULL DEFAULT '',
	author_face TEXT NOT NULL DEFAULT '',
	cover_url TEXT NOT NULL DEFAULT '',
	cache_linked_videos INTEGER NOT NULL DEFAULT 0,
	status TEXT NOT NULL DEFAULT 'queued',
	pause_reason TEXT NOT NULL DEFAULT '',
	error_kind TEXT NOT NULL DEFAULT '',
	error_message TEXT NOT NULL DEFAULT '',
	quality_hint TEXT NOT NULL DEFAULT '',
	bytes_downloaded INTEGER NOT NULL DEFAULT 0,
	total_bytes INTEGER NOT NULL DEFAULT 0,
	progress REAL NOT NULL DEFAULT 0,
	item_key TEXT NOT NULL DEFAULT '',
	item_dir TEXT NOT NULL DEFAULT '',
	files_json TEXT NOT NULL DEFAULT '[]',
	attempts_json TEXT NOT NULL DEFAULT '[]',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

CREATE TABLE task_attempts (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	task_id TEXT NOT NULL,
	attempt_time TEXT NOT NULL,
	file_path TEXT NOT NULL,
	offset_bytes INTEGER NOT NULL DEFAULT 0,
	host TEXT NOT NULL DEFAULT '',
	attempt_num INTEGER NOT NULL DEFAULT 1,
	error_kind TEXT NOT NULL DEFAULT '',
	http_status INTEGER NOT NULL DEFAULT 0
);

INSERT INTO feed_items (feed_key, site, kind, raw_id, mid, author_name, title, summary, canonical_url, pub_ts, discovered_at, cached, cache_status, item_key) VALUES
	('bilibili:video:BV17x411w7KC', 'bilibili', 'video', 'BV17x411w7KC', 1001, 'SyntheticCreatorA', 'V1 Cached Video', 'Summary A', 'https://www.bilibili.com/video/BV17x411w7KC', 1700000300, '2026-11-05T08:00:00Z', 1, 'complete', 'bilibili:video:BV17x411w7KC'),
	('bilibili:dynamic:88001', 'bilibili', 'dynamic', '88001', 2002, 'SyntheticCreatorB', 'V1 Cached Dynamic', 'Summary B', 'https://t.bilibili.com/88001', 1700000400, '2026-11-05T09:00:00Z', 1, 'complete', 'bilibili:dynamic:88001');

INSERT INTO download_tasks (task_id, site, kind, source, target_id, cid, page, title, mid, author_name, status, item_key, created_at, updated_at) VALUES
	('task-v1-001', 'bilibili', 'video', 'manual', 'BV17x411w7KC', 9001, 1, 'V1 Cached Video', 1001, 'SyntheticCreatorA', 'completed', 'bilibili:video:BV17x411w7KC', '2026-11-05T08:00:00Z', '2026-11-05T08:01:00Z');
