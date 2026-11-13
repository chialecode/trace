package library

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
)

// RebuildReport summarizes the result of scanning <cache_dir> and rebuilding the SQLite index.
type RebuildReport struct {
	Scanned         int      `json:"scanned"`
	Indexed         int      `json:"indexed"`
	MissingFiles    int      `json:"missingFiles"`
	CorruptBackedUp []string `json:"corruptBackedUp,omitempty"`
}

// Store manages the local cache library (item.json files + SQLite index + unified feed).
type Store struct {
	mu                 sync.RWMutex
	db                 *sql.DB
	cacheDir           string
	dataDir            string
	extraRoots         []string
	rootsFilePath      string
	pendingDeletesPath string
}

// Open initializes the library SQLite schema in db and sets the active cacheDir.
func Open(db *sql.DB, cacheDir string) (*Store, error) {
	return OpenWithDataDir(db, cacheDir, "")
}

// OpenWithDataDir initializes the library Store with an explicit dataDir for library-roots.json and pending-deletes.json.
func OpenWithDataDir(db *sql.DB, cacheDir, dataDir string) (*Store, error) {
	cleanCache := filepath.Clean(cacheDir)
	cleanData := filepath.Clean(dataDir)
	if dataDir == "" {
		cleanData = filepath.Dir(cleanCache)
	}
	s := &Store{
		db:                 db,
		cacheDir:           cleanCache,
		dataDir:            cleanData,
		rootsFilePath:      filepath.Join(cleanData, "library-roots.json"),
		pendingDeletesPath: filepath.Join(cleanData, "pending-deletes.json"),
	}
	if err := s.initSchema(); err != nil {
		return nil, err
	}
	if err := s.loadRootsLocked(); err != nil {
		return nil, err
	}
	if err := s.saveRootsLocked(); err != nil {
		return nil, err
	}
	if err := s.cleanupPendingDeletesLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

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

func schemaV2Statements() []string {
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
		`CREATE INDEX IF NOT EXISTS idx_library_items_pub_ts ON library_items(pub_ts DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_library_items_mid ON library_items(mid);`,
		`CREATE INDEX IF NOT EXISTS idx_library_items_site_author ON library_items(site, author_uid);`,
		`CREATE TABLE IF NOT EXISTS item_refs (
			from_key TEXT NOT NULL,
			to_key TEXT NOT NULL,
			PRIMARY KEY (from_key, to_key)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_item_refs_to ON item_refs(to_key);`,
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
		`CREATE INDEX IF NOT EXISTS idx_feed_items_pub_ts ON feed_items(pub_ts DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_feed_items_mid ON feed_items(mid);`,
		`CREATE INDEX IF NOT EXISTS idx_feed_items_site_author ON feed_items(site, author_uid);`,
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
		`CREATE INDEX IF NOT EXISTS idx_download_tasks_site_author ON download_tasks(site, author_uid);`,
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

func (s *Store) migrateSchemaToV2() error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("library: begin schema v2 migration: %w", err)
	}
	defer tx.Rollback()

	baseTables := []string{
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
	for _, q := range baseTables {
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

	indexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_library_items_pub_ts ON library_items(pub_ts DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_library_items_mid ON library_items(mid);`,
		`CREATE INDEX IF NOT EXISTS idx_library_items_site_author ON library_items(site, author_uid);`,
		`CREATE INDEX IF NOT EXISTS idx_item_refs_to ON item_refs(to_key);`,
		`CREATE INDEX IF NOT EXISTS idx_feed_items_pub_ts ON feed_items(pub_ts DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_feed_items_mid ON feed_items(mid);`,
		`CREATE INDEX IF NOT EXISTS idx_feed_items_site_author ON feed_items(site, author_uid);`,
		`CREATE INDEX IF NOT EXISTS idx_download_tasks_site_author ON download_tasks(site, author_uid);`,
	}
	for _, q := range indexes {
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

func (s *Store) loadRootsLocked() error {
	if s.rootsFilePath == "" {
		return nil
	}
	raw, err := os.ReadFile(s.rootsFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var payload struct {
		Roots []string `json:"roots"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		_, backupErr := storage.BackupCorruptFile(s.rootsFilePath)
		return backupErr
	}
	seen := make(map[string]bool)
	s.extraRoots = nil
	for _, r := range payload.Roots {
		clean := filepath.Clean(strings.TrimSpace(r))
		if clean != "" && !seen[strings.ToLower(clean)] {
			seen[strings.ToLower(clean)] = true
			s.extraRoots = append(s.extraRoots, clean)
		}
	}
	return nil
}

func (s *Store) saveRootsLocked() error {
	if s.rootsFilePath == "" {
		return nil
	}
	payload := struct {
		Version int      `json:"version"`
		Roots   []string `json:"roots"`
	}{
		Version: 1,
		Roots:   s.allRootsLocked(),
	}
	return storage.WriteJSONAtomic(s.rootsFilePath, payload, 0o600)
}

func (s *Store) allRootsLocked() []string {
	seen := make(map[string]bool)
	var out []string
	add := func(r string) {
		clean := filepath.Clean(strings.TrimSpace(r))
		if clean == "" || clean == "." {
			return
		}
		key := strings.ToLower(clean)
		if !seen[key] {
			seen[key] = true
			out = append(out, clean)
		}
	}
	add(s.cacheDir)
	for _, r := range s.extraRoots {
		add(r)
	}
	return out
}

type pendingDeleteRecord struct {
	ItemKey         string   `json:"itemKey"`
	ItemDir         string   `json:"itemDir"`
	RegisteredFiles []string `json:"registeredFiles"`
}

func (s *Store) readPendingDeletesLocked() ([]pendingDeleteRecord, error) {
	if s.pendingDeletesPath == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(s.pendingDeletesPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("library: 删除账本读取失败: %w", err)
	}
	var list []pendingDeleteRecord
	if err := json.Unmarshal(raw, &list); err != nil {
		// Keep the original ledger in place: treating it as empty could resurrect
		// files whose deletion was already committed before a crash.
		return nil, fmt.Errorf("library: 删除账本损坏，原文件已保留: %w", err)
	}
	return list, nil
}

func (s *Store) writePendingDeletesLocked(list []pendingDeleteRecord) error {
	if s.pendingDeletesPath == "" {
		return nil
	}
	if len(list) == 0 {
		err := os.Remove(s.pendingDeletesPath)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return storage.WriteJSONAtomic(s.pendingDeletesPath, list, 0o600)
}

func (s *Store) cleanupPendingDeletesLocked() error {
	pending, err := s.readPendingDeletesLocked()
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	var remaining []pendingDeleteRecord
	for _, rec := range pending {
		validRoot := false
		for _, root := range s.allRootsLocked() {
			if _, err := EnsureWithinRoot(root, rec.ItemDir); err == nil && !strings.EqualFold(root, rec.ItemDir) {
				validRoot = true
				break
			}
		}
		if !validRoot {
			remaining = append(remaining, rec)
			continue
		}
		var exists int
		if err := s.db.QueryRow(`SELECT COUNT(1) FROM library_items WHERE item_key = ?`, rec.ItemKey).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			// Re-cached since pending delete was recorded; skip cleanup.
			continue
		}
		allGone := true
		for _, f := range rec.RegisteredFiles {
			rel, err := filepath.Rel(rec.ItemDir, f)
			if err != nil || !safeRegisteredPath(rec.ItemDir, filepath.ToSlash(rel)) {
				allGone = false
				continue
			}
			if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
				allGone = false
			}
		}
		if rec.ItemDir != "" {
			removeEmptyDirsBottomUp(rec.ItemDir)
		}
		if !allGone {
			remaining = append(remaining, rec)
		}
	}
	return s.writePendingDeletesLocked(remaining)
}

// CacheDir returns the current root cache directory.
func (s *Store) CacheDir() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cacheDir
}

// SetCacheDir updates the active cache directory in memory and records it in library-roots.json.
func (s *Store) SetCacheDir(dir string) error {
	return s.ChangeCacheDir(dir, nil)
}

// SaveItem writes item.json atomically into itemDir, verifies referenced media files, and indexes it in SQLite.
func (s *Store) SaveItem(itemDir string, meta ItemMetadata) (ItemMetadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveItemLocked(itemDir, meta)
}

func (s *Store) saveItemLocked(itemDir string, meta ItemMetadata, checkHash ...bool) (ItemMetadata, error) {
	if meta.Version <= 0 {
		meta.Version = 1
	}
	if meta.Site == "" {
		meta.Site = "bilibili"
	}
	if meta.Site == "douyin" && strings.EqualFold(meta.Kind, "note") {
		meta.Kind = "dynamic"
	}
	meta.RawID = NormalizeRawID(meta.Kind, meta.ID)
	meta.ItemKey = CanonicalItemKey(meta.Site, meta.Kind, meta.RawID)
	SanitizeItemMetadata(&meta)
	if meta.CachedAt == "" {
		meta.CachedAt = time.Now().UTC().Format(time.RFC3339)
	}
	meta.ItemDir = filepath.Clean(itemDir)

	missing, totalBytes := verifyItemFiles(meta.ItemDir, &meta, checkHash...)
	meta.TotalBytes = totalBytes
	if missing && meta.Status != "recording" && meta.Status != "failed" && meta.Status != "partial" {
		meta.Status = "missing_files"
	} else if meta.Status == "" || (meta.Status == "missing_files" && !missing) {
		meta.Status = "complete"
	}

	itemJSONPath := filepath.Join(meta.ItemDir, "item.json")
	if raw, err := os.ReadFile(itemJSONPath); err == nil {
		previous, err := ParseItemMetadata(raw)
		if err != nil || previous.ItemKey != meta.ItemKey {
			return ItemMetadata{}, fmt.Errorf("library: 目标清单无法确认归属，原文件已保留")
		}
	} else if !os.IsNotExist(err) {
		return ItemMetadata{}, err
	}
	if err := storage.WriteJSONAtomic(itemJSONPath, meta, 0o600); err != nil {
		return ItemMetadata{}, err
	}

	if err := s.upsertItemRowLocked(meta); err != nil {
		return ItemMetadata{}, err
	}
	return meta, nil
}

// RebuildIndex walks all registered library roots for item.json files, backs up corrupt files,
// checks media file integrity, and rebuilds the SQLite library_items index.
func (s *Store) RebuildIndex() (RebuildReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var report RebuildReport
	roots := s.allRootsLocked()
	if err := os.MkdirAll(s.cacheDir, 0o700); err != nil {
		return report, err
	}

	pendingMap := make(map[string]bool)
	pending, err := s.readPendingDeletesLocked()
	if err != nil {
		return report, err
	}
	for _, p := range pending {
		pendingMap[p.ItemKey] = true
	}

	seenKeys := make(map[string]bool)
	for _, root := range roots {
		if _, err := os.Stat(root); err != nil {
			return report, err
		}
		if err := rejectDirectoryLinks(root); err != nil {
			return report, err
		}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.Type()&os.ModeSymlink != 0 {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				return nil
			}
			if !strings.EqualFold(d.Name(), "item.json") {
				return nil
			}
			if report.Scanned >= 100000 {
				return fmt.Errorf("library: 清单扫描超过单轮上限，索引清理未执行")
			}
			report.Scanned++

			raw, rErr := os.ReadFile(path)
			if rErr != nil {
				return rErr
			}
			meta, pErr := ParseItemMetadata(raw)
			if pErr != nil {
				backup, bErr := storage.BackupCorruptFile(path)
				if bErr == nil && backup != "" {
					report.CorruptBackedUp = append(report.CorruptBackedUp, backup)
				}
				return bErr
			}
			if pendingMap[meta.ItemKey] || seenKeys[meta.ItemKey] {
				return nil
			}

			itemDir := filepath.Dir(path)
			if _, err := EnsureWithinRoot(root, itemDir); err != nil {
				return err
			}
			meta.ItemDir = filepath.Clean(itemDir)
			missing, totalBytes := verifyItemFiles(meta.ItemDir, &meta)
			meta.TotalBytes = totalBytes
			if missing && meta.Status != "recording" && meta.Status != "failed" && meta.Status != "partial" {
				meta.Status = "missing_files"
				report.MissingFiles++
				if err := storage.WriteJSONAtomic(path, meta, 0o600); err != nil {
					return err
				}
			} else if meta.Status == "missing_files" && !missing {
				meta.Status = "complete"
				if err := storage.WriteJSONAtomic(path, meta, 0o600); err != nil {
					return err
				}
			}

			if err := s.upsertItemRowLocked(meta); err != nil {
				return err
			}
			seenKeys[meta.ItemKey] = true
			report.Indexed++
			return nil
		})
		if err != nil {
			return report, err
		}
	}

	rows, err := s.db.Query(`SELECT item_key FROM library_items`)
	if err != nil {
		return report, err
	}
	var stale []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return report, err
		}
		if !seenKeys[k] {
			stale = append(stale, k)
		}
	}
	rowErr := rows.Err()
	if err := errors.Join(rowErr, rows.Close()); err != nil {
		return report, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	for _, k := range stale {
		if _, err := tx.Exec(`DELETE FROM library_items WHERE item_key = ?`, k); err != nil {
			return report, err
		}
		if _, err := tx.Exec(`DELETE FROM item_refs WHERE from_key = ? OR to_key = ?`, k, k); err != nil {
			return report, err
		}
		if _, err := tx.Exec(`UPDATE feed_items SET cached = 0, cache_status = '', item_key = '' WHERE item_key = ?`, k); err != nil {
			return report, err
		}
	}
	return report, tx.Commit()
}

// AddLibraryRoot scans an existing cache folder (including bilibili/* and douyin/*/{works,lives}/*)
// or legacy trace-data folder containing library/, and indexes its items without moving files
// or overwriting existing keys (FR-LIB-006).
func (s *Store) AddLibraryRoot(selectedDir string) (AddRootReport, error) {
	if strings.TrimSpace(selectedDir) == "" || !filepath.IsAbs(selectedDir) {
		return AddRootReport{}, fmt.Errorf("library: folder path cannot be empty")
	}
	selectedDir = filepath.Clean(selectedDir)
	info, err := os.Lstat(selectedDir)
	if err != nil {
		return AddRootReport{}, fmt.Errorf("library: stat folder: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return AddRootReport{}, fmt.Errorf("library: symlink root folder is not allowed")
	}
	if !info.IsDir() {
		return AddRootReport{}, fmt.Errorf("library: %s is not a directory", selectedDir)
	}

	// Check if selectedDir is a legacy portable trace-data directory containing a library/ subdirectory (even without settings.json!).
	scanRoot := selectedDir
	libSub := filepath.Join(selectedDir, "library")
	if lInfo, lErr := os.Lstat(libSub); lErr == nil && lInfo.IsDir() && lInfo.Mode()&os.ModeSymlink == 0 {
		scanRoot = libSub
	} else {
		// Also check optional settings.json inside selectedDir.
		setBytes, sErr := os.ReadFile(filepath.Join(selectedDir, "settings.json"))
		if sErr == nil {
			var cfg struct {
				CacheRoot string `json:"cacheRoot"`
				CacheDir  string `json:"cacheDir"`
			}
			if json.Unmarshal(setBytes, &cfg) == nil {
				cand := strings.TrimSpace(cfg.CacheRoot)
				if cand == "" {
					cand = strings.TrimSpace(cfg.CacheDir)
				}
				if cand != "" {
					if !filepath.IsAbs(cand) {
						cand = filepath.Join(selectedDir, cand)
					}
					if cInfo, cErr := os.Lstat(cand); cErr == nil && cInfo.IsDir() && cInfo.Mode()&os.ModeSymlink == 0 {
						scanRoot = filepath.Clean(cand)
					}
				}
			}
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	report := AddRootReport{Root: scanRoot}
	if err := rejectDirectoryLinks(scanRoot); err != nil {
		return report, err
	}
	oldRoots := append([]string(nil), s.extraRoots...)
	found := false
	for _, r := range s.allRootsLocked() {
		found = found || strings.EqualFold(r, scanRoot)
	}
	if !found {
		s.extraRoots = append(s.extraRoots, scanRoot)
	}
	// Register the recovery root before importing any index rows. A failed
	// ledger write must not leave rows whose source disappears after restart.
	if err := s.saveRootsLocked(); err != nil {
		s.extraRoots = oldRoots
		return report, err
	}
	err = filepath.WalkDir(scanRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		// Never follow symlinks or junctions (INV-08).
		if d.Type()&os.ModeSymlink != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !strings.EqualFold(d.Name(), "item.json") {
			return nil
		}
		if report.Scanned >= 100000 {
			return fmt.Errorf("library: 清单扫描超过单轮上限")
		}
		report.Scanned++

		raw, rErr := os.ReadFile(path)
		if rErr != nil {
			return rErr
		}
		meta, pErr := ParseItemMetadata(raw)
		if pErr != nil {
			return nil
		}

		// If itemKey already exists in index, keep existing item and count as Skipped.
		var existingCount int
		if err := s.db.QueryRow(`SELECT COUNT(1) FROM library_items WHERE item_key = ?`, meta.ItemKey).Scan(&existingCount); err != nil {
			return err
		}
		if existingCount > 0 {
			report.Skipped++
			return nil
		}

		itemDir := filepath.Dir(path)
		if _, err := EnsureWithinRoot(scanRoot, itemDir); err != nil {
			return err
		}
		meta.ItemDir = filepath.Clean(itemDir)
		missing, totalBytes := verifyItemFiles(meta.ItemDir, &meta)
		meta.TotalBytes = totalBytes
		if missing && meta.Status != "recording" && meta.Status != "failed" && meta.Status != "partial" {
			meta.Status = "missing_files"
		}
		if uErr := s.upsertItemRowLocked(meta); uErr != nil {
			return uErr
		}
		report.Added++
		return nil
	})
	return report, err
}

// MediaPath resolves and validates a whitelisted media file path belonging to key (INV-08).
// It rejects '.', '..', backslashes, colons, null bytes, unregistered filenames, and symlinks/junctions.
func (s *Store) MediaPath(key, relPath string) (string, error) {
	relPath = strings.TrimSpace(relPath)
	if relPath == "" {
		return "", fmt.Errorf("library: empty relative media path")
	}
	if strings.ContainsRune(relPath, 0) || strings.ContainsRune(relPath, '\\') || strings.ContainsRune(relPath, ':') {
		return "", fmt.Errorf("library: forbidden characters in media path")
	}
	if filepath.IsAbs(relPath) || strings.HasPrefix(relPath, "/") {
		return "", fmt.Errorf("library: media path must be relative")
	}
	segments := strings.Split(relPath, "/")
	for _, seg := range segments {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("library: invalid segment %q in media path", seg)
		}
	}

	meta, ok, err := s.GetItem(key)
	if err != nil {
		return "", err
	}
	if !ok || meta.ItemDir == "" {
		return "", fmt.Errorf("library: item %q not found", key)
	}

	normRel := filepath.ToSlash(filepath.Clean(filepath.FromSlash(relPath)))
	if !isRegisteredInManifest(meta, normRel) {
		return "", fmt.Errorf("library: file %q is not registered in item %q manifest", relPath, key)
	}

	fullPath, err := EnsureWithinRoot(meta.ItemDir, filepath.Join(meta.ItemDir, filepath.FromSlash(normRel)))
	if err != nil {
		return "", err
	}

	if !safeRegisteredPath(meta.ItemDir, relPath) {
		return "", fmt.Errorf("library: media path contains a link or invalid segment")
	}

	// Check Lstat on every segment from meta.ItemDir to fullPath and EvalSymlinks to reject symlinks/junctions.
	dirInfo, err := os.Lstat(meta.ItemDir)
	if err != nil {
		return "", err
	}
	if isLinkLike(dirInfo.Mode()) {
		return "", fmt.Errorf("library: item directory is a symlink")
	}
	fileInfo, err := os.Lstat(fullPath)
	if err != nil {
		return "", err
	}
	if isLinkLike(fileInfo.Mode()) || fileInfo.IsDir() {
		return "", fmt.Errorf("library: media target is a symlink or directory")
	}

	evalRoot, err1 := filepath.EvalSymlinks(meta.ItemDir)
	evalTarget, err2 := filepath.EvalSymlinks(fullPath)
	if err1 != nil || err2 != nil {
		return "", fmt.Errorf("library: symlink evaluation failed")
	}
	if _, err := EnsureWithinRoot(evalRoot, evalTarget); err != nil {
		return "", fmt.Errorf("library: symlink target escapes item directory")
	}
	return fullPath, nil
}

func cleanManifestRel(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	return filepath.ToSlash(filepath.Clean(filepath.FromSlash(p)))
}

func isRegisteredInManifest(meta ItemMetadata, normRel string) bool {
	if normRel == "" {
		return false
	}
	for _, m := range meta.Media {
		if cleanManifestRel(m.Path) == normRel || cleanManifestRel(m.Motion) == normRel {
			return true
		}
	}
	for _, p := range meta.Parts {
		if cleanManifestRel(p.File) == normRel ||
			cleanManifestRel(p.Danmaku) == normRel ||
			cleanManifestRel(p.Audio.SavedAs) == normRel {
			return true
		}
		for _, sub := range p.Subtitles {
			if cleanManifestRel(sub) == normRel {
				return true
			}
		}
	}
	if meta.DouyinVideo != nil && cleanManifestRel(meta.DouyinVideo.File) == normRel {
		return true
	}
	for _, np := range meta.NotePages {
		if cleanManifestRel(np.Image) == normRel ||
			cleanManifestRel(np.Cover) == normRel ||
			cleanManifestRel(np.Motion) == normRel ||
			cleanManifestRel(np.Video) == normRel {
			return true
		}
		if np.VideoInfo != nil && cleanManifestRel(np.VideoInfo.File) == normRel {
			return true
		}
	}
	if meta.Music != nil {
		if cleanManifestRel(meta.Music.File) == normRel || cleanManifestRel(meta.Music.Cover) == normRel {
			return true
		}
	}
	if meta.Live != nil {
		for _, seg := range meta.Live.Segments {
			if cleanManifestRel(seg.File) == normRel {
				return true
			}
		}
	}
	return false
}

func verifyItemFiles(itemDir string, meta *ItemMetadata, checkHash ...bool) (missing bool, totalBytes int64) {
	checkedPaths := make(map[string]bool)

	checkRel := func(rel string) bool {
		rel = strings.TrimSpace(rel)
		if rel == "" {
			return true
		}
		full, err := ResolveItemPath(itemDir, rel)
		if err != nil {
			return false
		}
		info, err := os.Lstat(full)
		if err != nil || info.IsDir() || info.Size() == 0 {
			return false
		}
		norm := cleanManifestRel(rel)
		if !checkedPaths[norm] {
			checkedPaths[norm] = true
			totalBytes += info.Size()
		}
		return true
	}

	for i := range meta.Media {
		rel := meta.Media[i].Path
		if !checkRel(rel) {
			missing = true
		} else if full, err := ResolveItemPath(itemDir, rel); err == nil {
			info, err := os.Lstat(full)
			if err != nil {
				missing = true
				continue
			}
			if meta.Media[i].Bytes > 0 && meta.Media[i].Bytes != info.Size() {
				missing = true
			} else if meta.Media[i].Bytes == 0 {
				meta.Media[i].Bytes = info.Size()
			}
			if meta.Media[i].SHA256 != "" && (len(checkHash) == 0 || checkHash[0]) {
				if !VerifyMediaFile(itemDir, meta.Media[i], true) {
					missing = true
				}
			}
		}
		if meta.Media[i].Motion != "" && !checkRel(meta.Media[i].Motion) {
			missing = true
		}
	}

	for _, part := range meta.Parts {
		if part.File != "" && !checkRel(part.File) {
			missing = true
		}
		if part.Danmaku != "" && !checkRel(part.Danmaku) {
			missing = true
		}
		if part.Audio.SavedAs != "" && !checkRel(part.Audio.SavedAs) {
			missing = true
		}
		for _, sub := range part.Subtitles {
			if sub != "" && !checkRel(sub) {
				missing = true
			}
		}
	}

	if meta.DouyinVideo != nil && meta.DouyinVideo.File != "" && !checkRel(meta.DouyinVideo.File) {
		missing = true
	}

	for _, np := range meta.NotePages {
		for _, rel := range []string{np.Image, np.Cover, np.Motion, np.Video} {
			if rel != "" && !checkRel(rel) {
				missing = true
			}
		}
		if np.VideoInfo != nil && np.VideoInfo.File != "" && !checkRel(np.VideoInfo.File) {
			missing = true
		}
	}

	if meta.Music != nil {
		if meta.Music.File != "" && !checkRel(meta.Music.File) {
			missing = true
		}
		if meta.Music.Cover != "" && !checkRel(meta.Music.Cover) {
			missing = true
		}
	}

	if meta.Live != nil {
		for _, seg := range meta.Live.Segments {
			if seg.File != "" && !checkRel(seg.File) {
				missing = true
			}
		}
	}

	hasStructuredMedia := len(meta.Media) > 0 || len(meta.Parts) > 0 ||
		(meta.DouyinVideo != nil && meta.DouyinVideo.File != "") ||
		len(meta.NotePages) > 0 ||
		(meta.Music != nil && meta.Music.File != "") ||
		(meta.Live != nil && len(meta.Live.Segments) > 0)
	if !hasStructuredMedia && meta.Kind != "dynamic" && meta.Status != "partial" && meta.Status != "recording" {
		missing = true
	}
	return missing, totalBytes
}

// VerifyMediaFile checks a registered file without rewriting its expected size.
func VerifyMediaFile(itemDir string, file MediaFile, checkHash bool) bool {
	full, err := ResolveItemPath(itemDir, file.Path)
	if err != nil {
		return false
	}
	info, err := os.Lstat(full)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || file.Bytes > 0 && info.Size() != file.Bytes {
		return false
	}
	if !checkHash || file.SHA256 == "" {
		return true
	}
	f, err := os.Open(full)
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	return strings.EqualFold(hex.EncodeToString(h.Sum(nil)), file.SHA256)
}

func (s *Store) upsertItemRowLocked(meta ItemMetadata) error {
	SanitizeItemMetadata(&meta)
	jsonBytes, err := json.Marshal(meta)
	if err != nil {
		return err
	}

	summary := strings.TrimSpace(meta.Desc)
	if summary == "" {
		summary = strings.TrimSpace(meta.Text)
	}
	coverPath := ""
	for _, m := range meta.Media {
		if m.Role == "cover" || m.Role == "image" {
			coverPath = filepath.Join(meta.ItemDir, filepath.FromSlash(m.Path))
			break
		}
	}
	if coverPath == "" && len(meta.NotePages) > 0 {
		for _, np := range meta.NotePages {
			cand := np.Image
			if cand == "" {
				cand = np.Cover
			}
			if cand != "" {
				coverPath = filepath.Join(meta.ItemDir, filepath.FromSlash(cand))
				break
			}
		}
	}

	upAvail := 0
	if meta.UpgradeAvailable {
		upAvail = 1
	}
	_, authorUID := NormalizeAuthorUID(meta.Site, meta.Author.UID, meta.Author.Mid)

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`
		INSERT INTO library_items (
			item_key, site, kind, raw_id, mid, author_uid, author_name, author_face,
			title, summary, cover_path, canonical_url, pub_ts, cached_at,
			status, item_dir, total_bytes, upgrade_available, upgrade_hint, json_data
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(item_key) DO UPDATE SET
			mid = excluded.mid,
			author_uid = excluded.author_uid,
			author_name = excluded.author_name,
			author_face = excluded.author_face,
			title = excluded.title,
			summary = excluded.summary,
			cover_path = excluded.cover_path,
			canonical_url = excluded.canonical_url,
			pub_ts = excluded.pub_ts,
			cached_at = excluded.cached_at,
			status = excluded.status,
			item_dir = excluded.item_dir,
			total_bytes = excluded.total_bytes,
			upgrade_available = excluded.upgrade_available,
			upgrade_hint = excluded.upgrade_hint,
			json_data = excluded.json_data
	`,
		meta.ItemKey, meta.Site, meta.Kind, meta.RawID,
		meta.Author.Mid, authorUID, meta.Author.Name, meta.Author.Face,
		meta.Title, summary, coverPath, meta.URL, meta.PubTs, meta.CachedAt,
		meta.Status, meta.ItemDir, meta.TotalBytes, upAvail, meta.UpgradeHint, string(jsonBytes),
	)
	if err != nil {
		return err
	}

	// Update item_refs for dynamic -> video references.
	if _, err := tx.Exec(`DELETE FROM item_refs WHERE from_key = ?`, meta.ItemKey); err != nil {
		return err
	}
	if meta.Kind == "dynamic" {
		for _, vid := range meta.Videos {
			vidKey := CanonicalItemKey(meta.Site, "video", vid)
			if _, err := tx.Exec(`INSERT OR IGNORE INTO item_refs (from_key, to_key) VALUES (?, ?)`, meta.ItemKey, vidKey); err != nil {
				return err
			}
		}
	}

	_, err = tx.Exec(`
		INSERT INTO feed_items (
			feed_key, site, kind, raw_id, mid, author_uid, author_name, author_face,
			title, summary, cover_url, canonical_url, pub_ts, discovered_at,
			cached, cache_status, item_key
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
		ON CONFLICT(feed_key) DO UPDATE SET
			mid = excluded.mid,
			author_uid = excluded.author_uid,
			cached = 1,
			cache_status = excluded.cache_status,
			item_key = excluded.item_key,
			title = CASE WHEN feed_items.title = '' THEN excluded.title ELSE feed_items.title END,
			summary = CASE WHEN feed_items.summary = '' THEN excluded.summary ELSE feed_items.summary END,
			cover_url = CASE WHEN feed_items.cover_url = '' THEN excluded.cover_url ELSE feed_items.cover_url END
	`,
		meta.ItemKey, meta.Site, meta.Kind, meta.RawID,
		meta.Author.Mid, authorUID, meta.Author.Name, meta.Author.Face,
		meta.Title, summary, coverPath, meta.URL, meta.PubTs, meta.CachedAt,
		meta.Status, meta.ItemKey,
	)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// UpsertFeedEntry inserts or updates an online-discovered item in the unified feed without overwriting existing cache linkage.
func (s *Store) UpsertFeedEntry(entry FeedEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry.Site = strings.ToLower(strings.TrimSpace(entry.Site))
	if entry.Site == "" {
		entry.Site = "bilibili"
	}
	if entry.Site == "douyin" && strings.EqualFold(entry.Kind, "note") {
		entry.Kind = "dynamic"
	}
	entry.RawID = NormalizeRawID(entry.Kind, entry.RawID)
	entry.FeedKey = CanonicalItemKey(entry.Site, entry.Kind, entry.RawID)
	if entry.DiscoveredAt == "" {
		entry.DiscoveredAt = time.Now().UTC().Format(time.RFC3339)
	}
	_, authorUID := NormalizeAuthorUID(entry.Site, entry.AuthorUID, entry.Mid)
	if entry.Site == "bilibili" && entry.Mid <= 0 && authorUID != "" {
		if n, err := strconv.ParseInt(authorUID, 10, 64); err == nil && n > 0 {
			entry.Mid = n
		}
	}
	entry.CanonicalURL = sanitizeManifestPageURL(entry.CanonicalURL, entry.Site, entry.Kind, entry.RawID, nil)
	if entry.Site == "douyin" {
		if isRemoteOrSignedURL(entry.CoverURL) {
			entry.CoverURL = ""
		}
		if isRemoteOrSignedURL(entry.AuthorFace) {
			entry.AuthorFace = ""
		}
	} else {
		entry.CoverURL = stripURLQueryAndFragment(entry.CoverURL)
		entry.AuthorFace = stripURLQueryAndFragment(entry.AuthorFace)
	}

	var cachedStatus string
	err := s.db.QueryRow(`SELECT status FROM library_items WHERE item_key = ?`, entry.FeedKey).Scan(&cachedStatus)
	cachedInt := 0
	itemKey := ""
	if err == nil && cachedStatus != "" {
		cachedInt = 1
		itemKey = entry.FeedKey
	}

	_, err = s.db.Exec(`
		INSERT INTO feed_items (
			feed_key, site, kind, raw_id, mid, author_uid, author_name, author_face,
			title, summary, cover_url, canonical_url, pub_ts, discovered_at,
			cached, cache_status, item_key
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(feed_key) DO UPDATE SET
			mid = excluded.mid,
			author_uid = CASE WHEN excluded.author_uid != '' THEN excluded.author_uid ELSE feed_items.author_uid END,
			author_name = excluded.author_name,
			author_face = CASE WHEN excluded.author_face != '' THEN excluded.author_face ELSE feed_items.author_face END,
			title = excluded.title,
			summary = CASE WHEN excluded.summary != '' THEN excluded.summary ELSE feed_items.summary END,
			cover_url = CASE WHEN excluded.cover_url != '' THEN excluded.cover_url ELSE feed_items.cover_url END,
			canonical_url = CASE WHEN excluded.canonical_url != '' THEN excluded.canonical_url ELSE feed_items.canonical_url END,
			pub_ts = CASE WHEN excluded.pub_ts > 0 THEN excluded.pub_ts ELSE feed_items.pub_ts END,
			cached = CASE WHEN ? = 1 THEN 1 ELSE feed_items.cached END,
			cache_status = CASE WHEN ? != '' THEN ? ELSE feed_items.cache_status END,
			item_key = CASE WHEN ? != '' THEN ? ELSE feed_items.item_key END
	`,
		entry.FeedKey, entry.Site, entry.Kind, entry.RawID,
		entry.Mid, authorUID, entry.AuthorName, entry.AuthorFace,
		entry.Title, entry.Summary, entry.CoverURL, entry.CanonicalURL,
		entry.PubTs, entry.DiscoveredAt,
		cachedInt, cachedStatus, itemKey,
		cachedInt, cachedStatus, cachedStatus, itemKey, itemKey,
	)
	return err
}

// GetItem retrieves a single cached ItemMetadata by canonical itemKey ("<site>:<kind>:<id>"), short key ("<kind>:<id>"), or rawID.
func (s *Store) GetItem(keyOrID string) (ItemMetadata, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.getItemLocked(keyOrID)
}

func (s *Store) getItemLocked(keyOrID string) (ItemMetadata, bool, error) {
	keyOrID = strings.TrimSpace(keyOrID)
	if keyOrID == "" {
		return ItemMetadata{}, false, nil
	}
	var jsonStr, itemDir, status string
	var totalBytes int64
	var upAvail int
	var upHint string

	var err error
	switch strings.Count(keyOrID, ":") {
	case 0:
		err = s.db.QueryRow(`
			SELECT json_data, item_dir, status, total_bytes, upgrade_available, upgrade_hint
			FROM library_items
			WHERE item_key = ? OR raw_id = ?
			LIMIT 1
		`, keyOrID, keyOrID).Scan(&jsonStr, &itemDir, &status, &totalBytes, &upAvail, &upHint)
	case 1:
		err = s.db.QueryRow(`
			SELECT json_data, item_dir, status, total_bytes, upgrade_available, upgrade_hint
			FROM library_items
			WHERE item_key = ? OR item_key = ?
			LIMIT 1
		`, keyOrID, "bilibili:"+keyOrID).Scan(&jsonStr, &itemDir, &status, &totalBytes, &upAvail, &upHint)
	default:
		normKey := keyOrID
		if strings.HasPrefix(strings.ToLower(normKey), "douyin:note:") {
			normKey = "douyin:dynamic:" + normKey[len("douyin:note:"):]
		}
		err = s.db.QueryRow(`
			SELECT json_data, item_dir, status, total_bytes, upgrade_available, upgrade_hint
			FROM library_items
			WHERE item_key = ?
			LIMIT 1
		`, normKey).Scan(&jsonStr, &itemDir, &status, &totalBytes, &upAvail, &upHint)
	}
	if err != nil {
		if err == sql.ErrNoRows {
			return ItemMetadata{}, false, nil
		}
		return ItemMetadata{}, false, err
	}

	meta, err := ParseItemMetadata([]byte(jsonStr))
	if err != nil {
		return ItemMetadata{}, false, err
	}
	meta.ItemDir = itemDir
	meta.Status = status
	meta.TotalBytes = totalBytes
	meta.UpgradeAvailable = upAvail == 1
	meta.UpgradeHint = upHint
	refreshItemFileState(&meta)
	return meta, true, nil
}

func refreshItemFileState(meta *ItemMetadata) {
	if missing, actualBytes := verifyItemFiles(meta.ItemDir, meta, false); missing && meta.Status != "recording" {
		meta.Status = "missing_files"
		meta.TotalBytes = actualBytes
	}
}

// States computes batch DotState for the given item keys (FR-CACHE-004).
func (s *Store) States(keys []string) map[string]DotState {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make(map[string]DotState, len(keys))
	for _, k := range keys {
		meta, ok, err := s.getItemLocked(k)
		if err != nil || !ok {
			out[k] = DotState{Key: k, Dot: "none", Label: "未缓存"}
			continue
		}
		dot := ComputeDotState(meta)
		if meta.Kind == "dynamic" && len(meta.Videos) > 0 {
			for _, vid := range meta.Videos {
				vMeta, vOk, _ := s.getItemLocked(CanonicalItemKey(meta.Site, "video", vid))
				if !vOk {
					dot.Dot = "red"
					dot.Label = "引用视频缺失"
					break
				}
				vDot := ComputeDotState(vMeta)
				if vDot.Dot == "red" {
					dot.Dot = "red"
					dot.Label = vDot.Label
					break
				}
				if vDot.Dot == "yellow" && dot.Dot == "green" {
					dot.Dot = "yellow"
					dot.Label = vDot.Label
				}
			}
		}
		dot.Key = k
		out[k] = dot
	}
	return out
}

func appendSiteAndAuthorClauses(clauses []string, args []any, colPrefix, siteFilter string, mid int64, authorUID string, mids []int64, refs []AuthorRef) ([]string, []any) {
	normSiteFilter := strings.ToLower(strings.TrimSpace(siteFilter))
	if normSiteFilter != "" && normSiteFilter != "all" {
		clauses = append(clauses, colPrefix+"site = ?")
		args = append(args, normSiteFilter)
	}

	var collected []AuthorRef
	seen := make(map[string]bool)
	addRef := func(site, uid string) {
		uid = strings.TrimSpace(uid)
		if uid == "" {
			return
		}
		site = strings.ToLower(strings.TrimSpace(site))
		if site == "" {
			if normSiteFilter != "" && normSiteFilter != "all" {
				site = normSiteFilter
			} else {
				site = "bilibili"
			}
		}
		k := site + "\x00" + uid
		if !seen[k] {
			seen[k] = true
			collected = append(collected, AuthorRef{Site: site, UID: uid})
		}
	}
	if mid > 0 {
		addRef("bilibili", strconv.FormatInt(mid, 10))
	}
	if strings.TrimSpace(authorUID) != "" {
		addRef("", authorUID)
	}
	for _, m := range mids {
		if m > 0 {
			addRef("bilibili", strconv.FormatInt(m, 10))
		}
	}
	for _, r := range refs {
		addRef(r.Site, r.UID)
	}
	if len(collected) == 0 {
		return clauses, args
	}

	bySite := make(map[string][]string)
	var siteOrder []string
	for _, r := range collected {
		if _, ok := bySite[r.Site]; !ok {
			siteOrder = append(siteOrder, r.Site)
		}
		bySite[r.Site] = append(bySite[r.Site], r.UID)
	}
	var orParts []string
	for _, st := range siteOrder {
		uids := bySite[st]
		if len(uids) == 1 {
			orParts = append(orParts, "("+colPrefix+"site = ? AND "+colPrefix+"author_uid = ?)")
			args = append(args, st, uids[0])
		} else {
			ph := make([]string, len(uids))
			args = append(args, st)
			for i, u := range uids {
				ph[i] = "?"
				args = append(args, u)
			}
			orParts = append(orParts, "("+colPrefix+"site = ? AND "+colPrefix+"author_uid IN ("+strings.Join(ph, ",")+"))")
		}
	}
	if len(orParts) == 1 {
		clauses = append(clauses, orParts[0])
	} else {
		clauses = append(clauses, "("+strings.Join(orParts, " OR ")+")")
	}
	return clauses, args
}

// ListItems returns cached library items matching filter.
func (s *Store) ListItems(filter FeedFilter) ([]ItemMetadata, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var clauses []string
	var args []any
	if filter.Kind == "image" {
		clauses = append(clauses, "(EXISTS (SELECT 1 FROM json_each(json_data, '$.media') WHERE json_extract(value, '$.role') = 'image') OR EXISTS (SELECT 1 FROM json_each(json_data, '$.notePages') WHERE json_extract(value, '$.kind') IN ('image', 'motion')))")
	} else if filter.Kind != "" && filter.Kind != "all" {
		clauses = append(clauses, "kind = ?")
		args = append(args, filter.Kind)
	}
	clauses, args = appendSiteAndAuthorClauses(clauses, args, "", filter.Site, filter.Mid, filter.AuthorUID, filter.Authors, filter.AuthorRefs)
	if kw := strings.TrimSpace(filter.Keyword); kw != "" {
		clauses = append(clauses, "(title LIKE ? OR summary LIKE ? OR author_name LIKE ? OR raw_id LIKE ?)")
		pat := "%" + kw + "%"
		args = append(args, pat, pat, pat, pat)
	}

	query := `SELECT json_data, item_dir, status, total_bytes, upgrade_available, upgrade_hint FROM library_items`
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	if strings.EqualFold(filter.Sort, "cached_at") {
		query += " ORDER BY cached_at DESC, item_key DESC"
	} else if strings.EqualFold(filter.Sort, "oldest") {
		query += " ORDER BY pub_ts ASC, item_key ASC"
	} else {
		query += " ORDER BY pub_ts DESC, item_key DESC"
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 200
	}
	query += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, max(0, filter.Offset))

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ItemMetadata
	for rows.Next() {
		var jsonStr, itemDir, status, upHint string
		var totalBytes int64
		var upAvail int
		if err := rows.Scan(&jsonStr, &itemDir, &status, &totalBytes, &upAvail, &upHint); err != nil {
			return nil, err
		}
		meta, err := ParseItemMetadata([]byte(jsonStr))
		if err != nil {
			continue
		}
		meta.ItemDir = itemDir
		meta.Status = status
		meta.TotalBytes = totalBytes
		meta.UpgradeAvailable = upAvail == 1
		meta.UpgradeHint = upHint
		refreshItemFileState(&meta)
		out = append(out, meta)
	}
	return out, rows.Err()
}

// FeedPage implements bidirectional keyset pagination over (pub_ts, feed_key) for the home feed (FR-FEED-001).
// It excludes videos that are referenced by a cached dynamic item so videos referenced by dynamics do not appear twice.
func (s *Store) FeedPage(req FeedPageRequest) (FeedPageResult, error) {
	if req.Direction == "around" && req.Cursor != nil {
		pivot, err := s.feedAnchorCursor(req)
		if err != nil || pivot == nil {
			return FeedPageResult{}, err
		}
		beforeReq := req
		beforeReq.Cursor = pivot
		beforeReq.Order = "newest"
		beforeReq.Direction = "newer"
		beforeReq.Limit = 30
		before, err := s.FeedPage(beforeReq)
		if err != nil {
			return FeedPageResult{}, err
		}
		afterReq := beforeReq
		afterReq.Direction = "older"
		cursor := *pivot
		cursor.Key += "\x00"
		afterReq.Cursor = &cursor
		after, err := s.FeedPage(afterReq)
		if err != nil {
			return FeedPageResult{}, err
		}
		result := after
		result.Items = append(before.Items, after.Items...)
		if req.Order == "oldest" {
			for i, j := 0, len(result.Items)-1; i < j; i, j = i+1, j-1 {
				result.Items[i], result.Items[j] = result.Items[j], result.Items[i]
			}
		}
		if len(result.Items) > 0 {
			first, last := result.Items[0], result.Items[len(result.Items)-1]
			result.PrevCursor = &FeedCursor{PubTs: first.PubTs, Key: first.FeedKey}
			result.NextCursor = &FeedCursor{PubTs: last.PubTs, Key: last.FeedKey}
		}
		return result, nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	order := strings.ToLower(strings.TrimSpace(req.Order))
	if order != "oldest" {
		order = "newest"
	}
	dir := strings.ToLower(strings.TrimSpace(req.Direction))
	if dir != "newer" {
		dir = "older"
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 30
	}

	clauses, args := feedPageClauses(req)

	// Determine SQL comparison and sort order for keyset cursor (pub_ts, feed_key).
	// For "newest" (DESC):
	//   - direction="older": (pub_ts < c.PubTs OR (pub_ts = c.PubTs AND feed_key < c.Key)) ORDER BY pub_ts DESC, feed_key DESC
	//   - direction="newer": (pub_ts > c.PubTs OR (pub_ts = c.PubTs AND feed_key > c.Key)) ORDER BY pub_ts ASC, feed_key ASC (then reverse)
	// For "oldest" (ASC):
	//   - direction="older": (pub_ts > c.PubTs OR (pub_ts = c.PubTs AND feed_key > c.Key)) ORDER BY pub_ts ASC, feed_key ASC
	//   - direction="newer": (pub_ts < c.PubTs OR (pub_ts = c.PubTs AND feed_key < c.Key)) ORDER BY pub_ts DESC, feed_key DESC (then reverse)
	wantDesc := (order == "newest" && dir == "older") || (order == "oldest" && dir == "newer")
	reverseAfter := dir == "newer"

	if req.Cursor != nil && (req.Cursor.Key != "" || req.Cursor.PubTs > 0) {
		if wantDesc {
			clauses = append(clauses, "(f.pub_ts < ? OR (f.pub_ts = ? AND f.feed_key < ?))")
		} else {
			clauses = append(clauses, "(f.pub_ts > ? OR (f.pub_ts = ? AND f.feed_key > ?))")
		}
		args = append(args, req.Cursor.PubTs, req.Cursor.PubTs, req.Cursor.Key)
	}

	query := `
		SELECT
			f.feed_key, f.site, f.kind, f.raw_id, f.mid, f.author_uid, f.author_name, f.author_face,
			f.title, f.summary, f.cover_url, f.canonical_url, f.pub_ts, f.discovered_at,
			f.cached, f.cache_status, f.item_key,
			COALESCE(l.item_dir, ''), COALESCE(l.upgrade_available, 0), COALESCE(l.upgrade_hint, '')
		FROM feed_items f
		LEFT JOIN library_items l ON f.item_key = l.item_key
		WHERE ` + strings.Join(clauses, " AND ")

	if wantDesc {
		query += " ORDER BY f.pub_ts DESC, f.feed_key DESC"
	} else {
		query += " ORDER BY f.pub_ts ASC, f.feed_key ASC"
	}
	query += fmt.Sprintf(" LIMIT %d", limit+1)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return FeedPageResult{}, err
	}
	defer rows.Close()

	var items []FeedEntry
	for rows.Next() {
		var e FeedEntry
		var cachedInt, upAvail int
		if err := rows.Scan(
			&e.FeedKey, &e.Site, &e.Kind, &e.RawID, &e.Mid, &e.AuthorUID, &e.AuthorName, &e.AuthorFace,
			&e.Title, &e.Summary, &e.CoverURL, &e.CanonicalURL, &e.PubTs, &e.DiscoveredAt,
			&cachedInt, &e.CacheStatus, &e.ItemKey,
			&e.ItemDir, &upAvail, &e.UpgradeHint,
		); err != nil {
			return FeedPageResult{}, err
		}
		if e.AuthorUID == "" && e.Mid > 0 && e.Site == "bilibili" {
			e.AuthorUID = strconv.FormatInt(e.Mid, 10)
		}
		e.Cached = cachedInt == 1
		e.UpgradeAvailable = upAvail == 1
		items = append(items, e)
	}
	if err := rows.Err(); err != nil {
		return FeedPageResult{}, err
	}

	hasMore := false
	if len(items) > limit {
		hasMore = true
		items = items[:limit]
	}
	if reverseAfter && len(items) > 1 {
		for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
			items[i], items[j] = items[j], items[i]
		}
	}

	var res FeedPageResult
	res.Items = items
	res.HasMore = hasMore
	if len(items) > 0 {
		first := items[0]
		last := items[len(items)-1]
		res.PrevCursor = &FeedCursor{PubTs: first.PubTs, Key: first.FeedKey}
		res.NextCursor = &FeedCursor{PubTs: last.PubTs, Key: last.FeedKey}
	}
	return res, nil
}

func feedPageClauses(req FeedPageRequest) ([]string, []any) {
	clauses := []string{"NOT (f.kind = 'video' AND EXISTS (SELECT 1 FROM item_refs r WHERE r.to_key = f.feed_key))"}
	var args []any
	if req.CachedOnly {
		clauses = append(clauses, "f.cached = 1")
	}
	if req.Kind != "" && req.Kind != "all" {
		clauses = append(clauses, "f.kind = ?")
		args = append(args, req.Kind)
	}
	clauses, args = appendSiteAndAuthorClauses(clauses, args, "f.", req.Site, 0, "", req.Authors, req.AuthorRefs)
	return clauses, args
}

// Resolve a surviving key first; otherwise choose the newest item at or before
// the saved timestamp (the earliest item if everything is newer). Resolving the
// pivot before paging also handles more than one page of equal timestamps.
func (s *Store) feedAnchorCursor(req FeedPageRequest) (*FeedCursor, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	clauses, args := feedPageClauses(req)
	base := `SELECT f.feed_key, f.pub_ts FROM feed_items f WHERE ` + strings.Join(clauses, " AND ")
	var pivot FeedCursor
	if req.Cursor.Key != "" {
		keyArgs := append(append([]any(nil), args...), req.Cursor.Key)
		err := s.db.QueryRow(base+` AND f.feed_key = ?`, keyArgs...).Scan(&pivot.Key, &pivot.PubTs)
		if err == nil {
			return &pivot, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	if req.Cursor.PubTs > 0 {
		tsArgs := append(append([]any(nil), args...), req.Cursor.PubTs)
		err := s.db.QueryRow(base+` AND f.pub_ts <= ? ORDER BY f.pub_ts DESC, f.feed_key DESC LIMIT 1`, tsArgs...).Scan(&pivot.Key, &pivot.PubTs)
		if err == nil {
			return &pivot, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	order := "DESC"
	if req.Cursor.PubTs > 0 {
		order = "ASC"
	}
	err := s.db.QueryRow(base+" ORDER BY f.pub_ts "+order+", f.feed_key "+order+" LIMIT 1", args...).Scan(&pivot.Key, &pivot.PubTs)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &pivot, err
}

// NewerCount returns the number of feed items published more recently than the given anchor (pubTs, key).
func (s *Store) NewerCount(anchorPubTs int64, anchorKey string) (int, error) {
	return s.newerCount(anchorPubTs, anchorKey, false)
}
func (s *Store) NewerCachedCount(anchorPubTs int64, anchorKey string) (int, error) {
	return s.newerCount(anchorPubTs, anchorKey, true)
}
func (s *Store) newerCount(anchorPubTs int64, anchorKey string, cachedOnly bool) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if anchorPubTs <= 0 && anchorKey == "" {
		return 0, nil
	}
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(1) FROM feed_items f
		WHERE (? = 0 OR f.cached = 1) AND NOT (f.kind = 'video' AND EXISTS (SELECT 1 FROM item_refs r WHERE r.to_key = f.feed_key))
		  AND (f.pub_ts > ? OR (f.pub_ts = ? AND f.feed_key > ?))
	`, cachedOnly, anchorPubTs, anchorPubTs, anchorKey).Scan(&count)
	return count, err
}

// QueryFeed queries the unified feed (both online-discovered and locally cached items) ordered by pub_ts DESC.
func (s *Store) QueryFeed(filter FeedFilter) ([]FeedEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var clauses []string
	var args []any
	if filter.Kind != "" && filter.Kind != "all" {
		clauses = append(clauses, "f.kind = ?")
		args = append(args, filter.Kind)
	}
	clauses, args = appendSiteAndAuthorClauses(clauses, args, "f.", filter.Site, filter.Mid, filter.AuthorUID, filter.Authors, filter.AuthorRefs)
	if filter.CachedOnly {
		clauses = append(clauses, "f.cached = 1")
	}
	if kw := strings.TrimSpace(filter.Keyword); kw != "" {
		clauses = append(clauses, "(f.title LIKE ? OR f.summary LIKE ? OR f.author_name LIKE ? OR f.raw_id LIKE ?)")
		pat := "%" + kw + "%"
		args = append(args, pat, pat, pat, pat)
	}

	query := `
		SELECT
			f.feed_key, f.site, f.kind, f.raw_id, f.mid, f.author_uid, f.author_name, f.author_face,
			f.title, f.summary, f.cover_url, f.canonical_url, f.pub_ts, f.discovered_at,
			f.cached, f.cache_status, f.item_key,
			COALESCE(l.item_dir, ''), COALESCE(l.upgrade_available, 0), COALESCE(l.upgrade_hint, '')
		FROM feed_items f
		LEFT JOIN library_items l ON f.item_key = l.item_key
	`
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	if strings.EqualFold(filter.Sort, "oldest") {
		query += " ORDER BY f.pub_ts ASC, f.feed_key ASC"
	} else {
		query += " ORDER BY f.pub_ts DESC, f.feed_key DESC"
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 200
	}
	query += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, max(0, filter.Offset))

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []FeedEntry
	for rows.Next() {
		var e FeedEntry
		var cachedInt, upAvail int
		if err := rows.Scan(
			&e.FeedKey, &e.Site, &e.Kind, &e.RawID, &e.Mid, &e.AuthorUID, &e.AuthorName, &e.AuthorFace,
			&e.Title, &e.Summary, &e.CoverURL, &e.CanonicalURL, &e.PubTs, &e.DiscoveredAt,
			&cachedInt, &e.CacheStatus, &e.ItemKey,
			&e.ItemDir, &upAvail, &e.UpgradeHint,
		); err != nil {
			return nil, err
		}
		if e.AuthorUID == "" && e.Mid > 0 && e.Site == "bilibili" {
			e.AuthorUID = strconv.FormatInt(e.Mid, 10)
		}
		e.Cached = cachedInt == 1
		e.UpgradeAvailable = upAvail == 1
		out = append(out, e)
	}
	return out, rows.Err()
}

// PlanDelete computes the exact DeletePlan for an item without modifying disk or DB (FR-LIB-003, INV-09).
func (s *Store) PlanDelete(itemKey string) (DeletePlan, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.planDeleteLocked(itemKey)
}

func (s *Store) planDeleteLocked(itemKey string) (DeletePlan, error) {
	meta, ok, err := s.getItemLocked(itemKey)
	if err != nil {
		return DeletePlan{}, err
	}
	if !ok {
		return DeletePlan{}, fmt.Errorf("library: item %q not found", itemKey)
	}
	if meta.Status == "recording" {
		return DeletePlan{}, fmt.Errorf("library: 正在录制的直播条目不能删除，请先停止录制")
	}

	plan := DeletePlan{
		TargetKey: meta.ItemKey,
		ItemKeys:  []string{meta.ItemKey},
	}

	itemsToDelete := []ItemMetadata{meta}

	// If deleting a dynamic item that references videos, check if each referenced video is shared or directly cached.
	if meta.Kind == "dynamic" && len(meta.Videos) > 0 {
		for _, vid := range meta.Videos {
			vidKey := CanonicalItemKey(meta.Site, "video", vid)
			vMeta, vOk, _ := s.getItemLocked(vidKey)
			if !vOk {
				continue
			}
			var otherRefs int
			_ = s.db.QueryRow(`SELECT COUNT(1) FROM item_refs WHERE to_key = ? AND from_key != ?`, vidKey, meta.ItemKey).Scan(&otherRefs)
			if otherRefs > 0 || vMeta.DirectCached {
				plan.PreservedSharedVideos = append(plan.PreservedSharedVideos, vidKey)
			} else {
				plan.ItemKeys = append(plan.ItemKeys, vidKey)
				itemsToDelete = append(itemsToDelete, vMeta)
			}
		}
	}

	seenFiles := make(map[string]bool)
	seenUserFiles := make(map[string]bool)
	for _, it := range itemsToDelete {
		regFiles, userFiles := collectRegisteredAndUserFiles(it)
		for _, f := range regFiles {
			if !seenFiles[f] {
				seenFiles[f] = true
				plan.RegisteredFiles = append(plan.RegisteredFiles, f)
			}
		}
		for _, uf := range userFiles {
			if !seenUserFiles[uf] {
				seenUserFiles[uf] = true
				plan.PreservedUserFiles = append(plan.PreservedUserFiles, uf)
			}
		}
	}
	return plan, nil
}

func collectRegisteredAndUserFiles(meta ItemMetadata) (registeredAbs []string, userRel []string) {
	if meta.ItemDir == "" {
		return nil, nil
	}
	itemDir := filepath.Clean(meta.ItemDir)
	regRel := make(map[string]bool)
	addRel := func(rel string) {
		rel = strings.TrimSpace(rel)
		if rel == "" {
			return
		}
		norm := strings.ToLower(filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel))))
		if !safeRegisteredPath(itemDir, rel) {
			return
		}
		if !regRel[norm] {
			regRel[norm] = true
			registeredAbs = append(registeredAbs, filepath.Join(itemDir, filepath.FromSlash(rel)))
		}
	}

	for _, m := range meta.Media {
		addRel(m.Path)
		addRel(m.Motion)
	}
	for _, part := range meta.PendingParts {
		addRel(part)
		if meta.Kind == "live" && strings.HasPrefix(part, "part-") && strings.HasSuffix(part, ".mp4.part") {
			// Both names are reserved before recording starts. A crash after the
			// final rename can leave the MP4 present before its media row is saved.
			addRel(strings.TrimSuffix(part, ".part"))
		}
	}
	for _, p := range meta.Parts {
		addRel(p.File)
		addRel(p.Danmaku)
		addRel(p.Audio.SavedAs)
		for _, sub := range p.Subtitles {
			addRel(sub)
		}
	}
	if meta.DouyinVideo != nil {
		addRel(meta.DouyinVideo.File)
	}
	for _, np := range meta.NotePages {
		addRel(np.Image)
		addRel(np.Cover)
		addRel(np.Motion)
		addRel(np.Video)
		if np.VideoInfo != nil {
			addRel(np.VideoInfo.File)
		}
	}
	if meta.Music != nil {
		addRel(meta.Music.File)
		addRel(meta.Music.Cover)
	}
	if meta.Live != nil {
		for _, seg := range meta.Live.Segments {
			addRel(seg.File)
		}
	}
	addRel("orig.json")

	// Scan itemDir for *.part files (which belong to TRACE) and user-owned files (which MUST be preserved).
	_ = filepath.WalkDir(itemDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return nil
		}
		rel, rErr := filepath.Rel(itemDir, path)
		if rErr != nil {
			return nil
		}
		norm := strings.ToLower(filepath.ToSlash(rel))
		if norm == "item.json" {
			return nil
		}
		if strings.HasSuffix(norm, ".part") && regRel[strings.TrimSuffix(norm, ".part")] {
			addRel(rel)
			return nil
		}
		if !regRel[norm] {
			userRel = append(userRel, filepath.ToSlash(rel))
		}
		return nil
	})

	// item.json is always deleted last among registered files.
	addRel("item.json")
	return registeredAbs, userRel
}

// Delete executes PlanDelete: removes only registered files and empty directories, never recursive RemoveAll (INV-09).
func (s *Store) Delete(itemKey string) (DeletePlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deleteWithPlanLocked(itemKey, true)
}

// DeleteItem removes an item from the index and optionally deletes its registered files from disk (INV-09).
// It NEVER calls os.RemoveAll on the item directory; user-placed files are strictly preserved.
func (s *Store) DeleteItem(itemKey string, deleteFiles bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.deleteWithPlanLocked(itemKey, deleteFiles)
	return err
}

func (s *Store) deleteWithPlanLocked(itemKey string, deleteFiles bool) (DeletePlan, error) {
	plan, err := s.planDeleteLocked(itemKey)
	if err != nil {
		return DeletePlan{}, err
	}

	var itemDirs []string
	for _, k := range plan.ItemKeys {
		if m, ok, _ := s.getItemLocked(k); ok && m.ItemDir != "" {
			itemDirs = append(itemDirs, m.ItemDir)
		}
	}

	// 1. Write pending-deletes.json before deleting files (data-and-cache.md §2.8).
	if deleteFiles && len(plan.RegisteredFiles) > 0 {
		pending, err := s.readPendingDeletesLocked()
		if err != nil {
			return plan, err
		}
		for _, k := range plan.ItemKeys {
			m, ok, _ := s.getItemLocked(k)
			if ok {
				regFiles, _ := collectRegisteredAndUserFiles(m)
				pending = append(pending, pendingDeleteRecord{
					ItemKey:         k,
					ItemDir:         m.ItemDir,
					RegisteredFiles: regFiles,
				})
			}
		}
		if err := s.writePendingDeletesLocked(pending); err != nil {
			return plan, err
		}
	}

	// 2. Remove from index and references.
	tx, err := s.db.Begin()
	if err != nil {
		return plan, err
	}
	defer tx.Rollback()
	for _, k := range plan.ItemKeys {
		if _, err := tx.Exec(`DELETE FROM library_items WHERE item_key = ?`, k); err != nil {
			return plan, err
		}
		if _, err := tx.Exec(`DELETE FROM item_refs WHERE from_key = ? OR to_key = ?`, k, k); err != nil {
			return plan, err
		}
		if _, err := tx.Exec(`UPDATE feed_items SET cached = 0, cache_status = '', item_key = '' WHERE item_key = ?`, k); err != nil {
			return plan, err
		}
	}
	if err := tx.Commit(); err != nil {
		return plan, err
	}

	// 3. Delete only registered files one by one, then remove empty directories bottom-up (NEVER os.RemoveAll).
	if deleteFiles {
		allRemoved := true
		var removeErrors []error
		for _, f := range plan.RegisteredFiles {
			if rErr := os.Remove(f); rErr != nil && !os.IsNotExist(rErr) {
				allRemoved = false
				removeErrors = append(removeErrors, rErr)
			}
		}
		for _, dir := range itemDirs {
			removeEmptyDirsBottomUp(dir)
		}
		if allRemoved {
			// Clear completed keys from pending-deletes.json.
			pending, err := s.readPendingDeletesLocked()
			if err != nil {
				return plan, err
			}
			doneSet := make(map[string]bool, len(plan.ItemKeys))
			for _, k := range plan.ItemKeys {
				doneSet[k] = true
			}
			var rem []pendingDeleteRecord
			for _, p := range pending {
				if !doneSet[p.ItemKey] {
					rem = append(rem, p)
				}
			}
			if err := s.writePendingDeletesLocked(rem); err != nil {
				return plan, err
			}
		}
		if !allRemoved {
			return plan, errors.Join(removeErrors...)
		}
	}

	return plan, nil
}

// removeEmptyDirsBottomUp removes empty subdirectories (such as images/) and the item directory itself
// ONLY if they are completely empty (using os.Remove, never os.RemoveAll).
func removeEmptyDirsBottomUp(itemDir string) {
	itemDir = filepath.Clean(itemDir)
	if itemDir == "" || itemDir == "." {
		return
	}
	var dirs []string
	_ = filepath.WalkDir(itemDir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	})
	// Sort deepest paths first.
	sort.Slice(dirs, func(i, j int) bool {
		return len(dirs[i]) > len(dirs[j])
	})
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err == nil && len(entries) == 0 {
			_ = os.Remove(d)
		}
	}
}

// MigrateCacheDir changes the active cache directory, optionally moving registered files to newDir,
// while preserving any user-owned files in the old directory and keeping old roots browsable.
func (s *Store) MigrateCacheDir(newDir string, moveExisting bool) (RebuildReport, error) {
	return s.MigrateCacheDirWithPersistence(newDir, moveExisting, nil)
}

// MigrateCacheDirWithPersistence commits the caller's settings before any old
// registered file is removed. On persistence failure, only new copies roll back.
func (s *Store) MigrateCacheDirWithPersistence(newDir string, moveExisting bool, persist func() error) (RebuildReport, error) {
	if !moveExisting {
		return RebuildReport{}, s.ChangeCacheDir(newDir, persist)
	}
	return s.moveRegisteredCache(newDir, persist)
}

func (s *Store) listAllItemsLocked() ([]ItemMetadata, error) {
	rows, err := s.db.Query(`SELECT json_data, item_dir, status, total_bytes, upgrade_available, upgrade_hint FROM library_items`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ItemMetadata
	for rows.Next() {
		var jsonStr, itemDir, status, upHint string
		var totalBytes int64
		var upAvail int
		if err := rows.Scan(&jsonStr, &itemDir, &status, &totalBytes, &upAvail, &upHint); err != nil {
			return nil, err
		}
		if meta, err := ParseItemMetadata([]byte(jsonStr)); err != nil {
			return nil, err
		} else {
			meta.ItemDir = itemDir
			meta.Status = status
			meta.TotalBytes = totalBytes
			meta.UpgradeAvailable = upAvail == 1
			meta.UpgradeHint = upHint
			out = append(out, meta)
		}
	}
	return out, rows.Err()
}

// UpgradeCandidates returns up to 50 cached video items ordered by oldest UpgradeCheckedAt first (FR-CACHE-006).
func (s *Store) UpgradeCandidates(limit int) ([]ItemMetadata, error) {
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	items, err := s.ListItems(FeedFilter{Site: "bilibili", Kind: "video", Limit: 500})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(items, func(i, j int) bool {
		ti, _ := time.Parse(time.RFC3339, items[i].UpgradeCheckedAt)
		tj, _ := time.Parse(time.RFC3339, items[j].UpgradeCheckedAt)
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		return items[i].PubTs < items[j].PubTs
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

// CheckUpgradeKeys probes the given video keys (or all candidates if keys is empty) for quality upgrades.
// When force is false, items checked within the last 24 hours are skipped without network requests (FR-CACHE-006).
func (s *Store) CheckUpgradeKeys(ctx context.Context, client *bilibili.Client, keys []string, force bool, opts bilibili.PlaySelectionOptions) ([]ItemMetadata, error) {
	if client == nil {
		return nil, fmt.Errorf("library: bilibili client is nil")
	}

	var candidates []ItemMetadata
	if len(keys) == 0 {
		var err error
		candidates, err = s.UpgradeCandidates(50)
		if err != nil {
			return nil, err
		}
	} else {
		for _, k := range keys {
			m, ok, err := s.GetItem(k)
			if err != nil {
				return nil, &bilibili.SiteError{Kind: "storage", Message: "读取缓存记录失败", Err: err}
			}
			if ok && m.Kind == "video" {
				candidates = append(candidates, m)
			}
		}
	}

	var upgradable []ItemMetadata
	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339)

	for _, meta := range candidates {
		if err := ctx.Err(); err != nil {
			return upgradable, err
		}
		if len(meta.Parts) == 0 {
			continue
		}
		if !force && meta.UpgradeCheckedAt != "" {
			if lastT, err := time.Parse(time.RFC3339, meta.UpgradeCheckedAt); err == nil && now.Sub(lastT) < 24*time.Hour {
				if meta.UpgradeAvailable {
					upgradable = append(upgradable, meta)
				}
				continue
			}
		}

		// Probe P1 only (data-and-cache.md §2.7).
		part := &meta.Parts[0]
		if part.CID <= 0 {
			continue
		}
		plan, err := client.PlayURL(ctx, meta.RawID, part.CID, opts)
		if err != nil {
			return upgradable, err
		}

		part.Available = PartAvailableInfo{
			AcceptQuality: plan.Available.AcceptQuality,
			Formats:       plan.Available.Formats,
			Returned:      plan.Available.Returned,
			BestVideo:     plan.Available.BestVideo,
			BestAudio:     plan.Available.BestAudio,
			HasDolby:      plan.Available.HasDolby,
			HasFlac:       plan.Available.HasFlac,
			ProbedAt:      nowStr,
			Downgraded:    plan.Available.Downgraded,
			DowngradeHint: plan.Available.DowngradeHint,
		}

		var hints []string
		if bilibili.QualityRank(plan.Video.Quality) > bilibili.QualityRank(part.Video.Quality) {
			hints = append(hints, fmt.Sprintf("P%02d 视频可由 %s (%d) 升级至 %s (%d)",
				part.Page,
				bilibili.QualityName(part.Video.Quality), part.Video.Quality,
				plan.Video.QualityName, plan.Video.Quality,
			))
		}
		if plan.Audio != nil && audioPriority(plan.Audio.Quality) > audioPriority(part.Audio.Quality) {
			hints = append(hints, fmt.Sprintf("P%02d 音频可由 %s 升级至 %s",
				part.Page,
				part.Audio.Name,
				plan.Audio.Name,
			))
		}

		meta.UpgradeCheckedAt = nowStr
		meta.UpgradeAvailable = len(hints) > 0
		meta.UpgradeHint = strings.Join(hints, "；")
		updated, err := s.SaveItem(meta.ItemDir, meta)
		if err != nil {
			return upgradable, &bilibili.SiteError{Kind: "storage", Message: "画质探测结果保存失败", Err: err}
		}
		if updated.UpgradeAvailable {
			upgradable = append(upgradable, updated)
		}
	}

	sort.SliceStable(upgradable, func(i, j int) bool {
		return upgradable[i].PubTs > upgradable[j].PubTs
	})
	return upgradable, nil
}

// CheckQualityUpgrade probes cached video items against the Bilibili PlayURL API (with force=true for direct calls).
func (s *Store) CheckQualityUpgrade(ctx context.Context, client *bilibili.Client, opts bilibili.PlaySelectionOptions) ([]ItemMetadata, error) {
	return s.CheckUpgradeKeys(ctx, client, nil, true, opts)
}

func audioPriority(id int) int {
	switch id {
	case 30251:
		return 50
	case 30250:
		return 40
	case 30280:
		return 30
	case 30232:
		return 20
	case 30216:
		return 10
	default:
		return 0
	}
}

func safeRegisteredPath(root, rel string) bool {
	if rejectDirectoryLinks(filepath.Clean(root)) != nil {
		return false
	}
	if rel == "" || filepath.IsAbs(rel) || strings.ContainsAny(rel, "\\:\x00") {
		return false
	}
	current := root
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return false
		}
		if isLinkLike(info.Mode()) {
			return false
		}
	}
	info, err := os.Lstat(root)
	return err == nil && info.IsDir() && !isLinkLike(info.Mode())
}

// isLinkLike reports symlinks and other reparse points. Since Go 1.23 a
// Windows directory junction reports ModeIrregular instead of ModeSymlink.
func isLinkLike(mode os.FileMode) bool {
	return mode&(os.ModeSymlink|os.ModeIrregular) != 0
}

// ResolveItemPath validates an item-relative path before a registered file is
// read, written, or removed. The final file may be absent, but links are rejected.
func ResolveItemPath(root, rel string) (string, error) {
	if !safeRegisteredPath(root, rel) {
		return "", fmt.Errorf("library: unsafe item-relative path")
	}
	return EnsureWithinRoot(root, filepath.Join(root, filepath.FromSlash(rel)))
}
