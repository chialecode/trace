package library

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"

	"github.com/chialecode/trace/internal/storage"
)

var (
	migrateV2HookMu sync.Mutex
	migrateV2Hook   func(tx *sql.Tx) error
)

// SetMigrateV2HookForTest installs a fault-injection callback invoked inside
// the v1 -> v2 SQLite migration transaction right before PRAGMA user_version = 2.
func SetMigrateV2HookForTest(fn func(tx *sql.Tx) error) func() {
	migrateV2HookMu.Lock()
	prev := migrateV2Hook
	migrateV2Hook = fn
	migrateV2HookMu.Unlock()
	return func() {
		migrateV2HookMu.Lock()
		migrateV2Hook = prev
		migrateV2HookMu.Unlock()
	}
}

func (s *Store) initSchema() error {
	v, err := storage.CheckSQLiteSchema(s.db, "library.db", CurrentSchemaVersion)
	if err != nil {
		return err
	}
	if v < CurrentSchemaVersion {
		return s.migrateSchemaToV2()
	}
	for _, q := range schemaV2Statements() {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("library: init schema: %w", err)
		}
	}
	return nil
}

func schemaV2Tables() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS library_items (
			item_key TEXT PRIMARY KEY,
			site TEXT NOT NULL,
			kind TEXT NOT NULL,
			raw_id TEXT NOT NULL,
			mid INTEGER NOT NULL DEFAULT 0,
			author_uid TEXT NOT NULL DEFAULT '',
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
		);`,
		`CREATE TABLE IF NOT EXISTS item_refs (
			from_key TEXT NOT NULL,
			to_key TEXT NOT NULL,
			PRIMARY KEY (from_key, to_key)
		);`,
		`CREATE TABLE IF NOT EXISTS feed_items (
			feed_key TEXT PRIMARY KEY,
			site TEXT NOT NULL,
			kind TEXT NOT NULL,
			raw_id TEXT NOT NULL,
			mid INTEGER NOT NULL DEFAULT 0,
			author_uid TEXT NOT NULL DEFAULT '',
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
		);`,
		`CREATE TABLE IF NOT EXISTS download_tasks (
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
			author_uid TEXT NOT NULL DEFAULT '',
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
		);`,
		`CREATE TABLE IF NOT EXISTS task_attempts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id TEXT NOT NULL,
			attempt_time TEXT NOT NULL,
			file_path TEXT NOT NULL,
			offset_bytes INTEGER NOT NULL DEFAULT 0,
			host TEXT NOT NULL DEFAULT '',
			attempt_num INTEGER NOT NULL DEFAULT 1,
			error_kind TEXT NOT NULL DEFAULT '',
			http_status INTEGER NOT NULL DEFAULT 0
		);`,
	}
}

func schemaV2Indexes() []string {
	return []string{
		`CREATE INDEX IF NOT EXISTS idx_library_items_pub_ts ON library_items(pub_ts DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_library_items_mid ON library_items(mid);`,
		`CREATE INDEX IF NOT EXISTS idx_library_items_site_author ON library_items(site, author_uid);`,
		`CREATE INDEX IF NOT EXISTS idx_library_items_via ON library_items(site, json_extract(json_data, '$.via.uid'));`,
		`CREATE INDEX IF NOT EXISTS idx_item_refs_to ON item_refs(to_key);`,
		`CREATE INDEX IF NOT EXISTS idx_feed_items_pub_ts ON feed_items(pub_ts DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_feed_items_mid ON feed_items(mid);`,
		`CREATE INDEX IF NOT EXISTS idx_feed_items_site_author ON feed_items(site, author_uid);`,
		`CREATE INDEX IF NOT EXISTS idx_download_tasks_site_author ON download_tasks(site, author_uid);`,
	}
}

func schemaV2Statements() []string {
	tables := schemaV2Tables()
	indexes := schemaV2Indexes()
	out := make([]string, 0, len(tables)+len(indexes))
	out = append(out, tables...)
	out = append(out, indexes...)
	return out
}

func (s *Store) migrateSchemaToV2() error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("library: begin schema v2 migration: %w", err)
	}
	defer tx.Rollback()

	for _, q := range schemaV2Tables() {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("library: migrate schema v2 base table: %w", err)
		}
	}

	for _, tbl := range []string{"library_items", "feed_items", "download_tasks"} {
		hasCol, err := tableHasColumnTx(tx, tbl, "author_uid")
		if err != nil {
			return err
		}
		if !hasCol {
			if _, err := tx.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN author_uid TEXT NOT NULL DEFAULT ''`, tbl)); err != nil {
				return fmt.Errorf("library: add author_uid on %s: %w", tbl, err)
			}
		}
		if _, err := tx.Exec(fmt.Sprintf(`UPDATE %s SET author_uid = CASE WHEN mid > 0 THEN CAST(mid AS TEXT) ELSE '' END WHERE author_uid = ''`, tbl)); err != nil {
			return fmt.Errorf("library: backfill author_uid on %s: %w", tbl, err)
		}
	}

	for _, q := range schemaV2Indexes() {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("library: create schema v2 index: %w", err)
		}
	}

	migrateV2HookMu.Lock()
	hook := migrateV2Hook
	migrateV2HookMu.Unlock()
	if hook != nil {
		if err := hook(tx); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, CurrentSchemaVersion)); err != nil {
		return fmt.Errorf("library: stamp user_version %d: %w", CurrentSchemaVersion, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("library: commit schema v2 migration: %w", err)
	}
	_, _ = s.db.Exec(`PRAGMA wal_checkpoint(PASSIVE)`)
	return nil
}

func tableHasColumnTx(tx *sql.Tx, table, column string) (bool, error) {
	rows, err := tx.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid        int
			name, ctyp string
			notNull    int
			dflt       sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &ctyp, &notNull, &dflt, &pk); err != nil {
			return false, err
		}
		if strings.EqualFold(name, column) {
			return true, nil
		}
	}
	return false, rows.Err()
}
