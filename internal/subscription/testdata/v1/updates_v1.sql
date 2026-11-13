PRAGMA user_version = 1;

CREATE TABLE account_state (
	site TEXT NOT NULL,
	uid TEXT NOT NULL,
	baseline_at TEXT NOT NULL DEFAULT '',
	last_checked_at TEXT NOT NULL DEFAULT '',
	last_kind TEXT NOT NULL DEFAULT '',
	newest_id TEXT NOT NULL DEFAULT '',
	newest_ts INTEGER NOT NULL DEFAULT 0,
	note TEXT NOT NULL DEFAULT '',
	unlinked_at TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (site, uid)
);

CREATE TABLE update_item (
	site TEXT NOT NULL,
	uid TEXT NOT NULL,
	dyn_id TEXT NOT NULL,
	pub_ts INTEGER NOT NULL DEFAULT 0,
	type TEXT NOT NULL DEFAULT '',
	snapshot TEXT NOT NULL DEFAULT '',
	discovered_at TEXT NOT NULL DEFAULT '',
	seen_at TEXT NOT NULL DEFAULT '',
	baseline_snapshot INTEGER NOT NULL DEFAULT 0,
	auto_task_id TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (site, uid, dyn_id)
);

CREATE INDEX idx_update_item_pub ON update_item(site, uid, pub_ts DESC, dyn_id DESC);

CREATE TABLE scheduler_state (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	consecutive_failures INTEGER NOT NULL DEFAULT 0,
	backoff_until TEXT NOT NULL DEFAULT '',
	last_stop_reason TEXT NOT NULL DEFAULT '',
	last_stop_at TEXT NOT NULL DEFAULT '',
	token_timestamps_json TEXT NOT NULL DEFAULT '[]'
);

INSERT INTO account_state (site, uid, baseline_at, last_checked_at, last_kind, newest_id, newest_ts, note, unlinked_at) VALUES
	('bilibili', '1001', '2026-11-05T08:00:00Z', '2026-11-06T08:00:00Z', 'ok', '99001', 1700000500, '', '');

INSERT INTO update_item (site, uid, dyn_id, pub_ts, type, snapshot, discovered_at, seen_at, baseline_snapshot, auto_task_id) VALUES
	('bilibili', '1001', '99001', 1700000500, 'video', '{"id":"99001","title":"Post-baseline synthetic item"}', '2026-11-06T08:00:00Z', '', 0, '');

INSERT INTO scheduler_state (id, consecutive_failures, backoff_until, last_stop_reason, last_stop_at, token_timestamps_json) VALUES
	(1, 2, '2026-11-12T12:00:00Z', 'risk_control', '2026-11-12T10:00:00Z', '[1700000100,1700000200]');
