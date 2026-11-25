package library

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/storage"
)

// RebuildReport summarizes the result of scanning <cache_dir> and rebuilding the SQLite index.
type RebuildReport struct {
	Scanned         int      `json:"scanned"`
	Indexed         int      `json:"indexed"`
	MissingFiles    int      `json:"missingFiles"`
	CorruptBackedUp []string `json:"corruptBackedUp,omitempty"`
	// Removed lists indexed items whose item.json is gone (their folder was
	// deleted outside TRACE); they left the index in this scan.
	Removed []ChangedItem `json:"removed,omitempty"`
	// NewlyMissing lists items that were complete before this scan and now
	// lack files (deleted, emptied or changed outside TRACE).
	NewlyMissing []ChangedItem `json:"newlyMissing,omitempty"`
}

// ChangedItem is one library item a scan found changed on disk.
type ChangedItem struct {
	ItemKey string   `json:"itemKey"`
	Site    string   `json:"site"`
	Kind    string   `json:"kind"`
	Title   string   `json:"title"`
	ItemDir string   `json:"itemDir"`
	Files   []string `json:"files,omitempty"` // manifest paths that are missing or changed
}

// Store manages the local cache library (item.json files + SQLite index + unified feed).
type Store struct {
	rebuildMu          sync.Mutex
	mu                 sync.RWMutex
	mutationSeq        uint64
	db                 *sql.DB
	cacheDir           string
	dataDir            string
	extraRoots         []string
	rootsFilePath      string
	pendingDeletesPath string
	node               *ItemNode
	onSaved            []func(ItemMetadata)
	onRebuildWalk      func()
	allowMovedFiles    bool
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
	if err := s.recoverInterruptedSwapsLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

// SetAllowMovedFiles configures remote/export mode (remote.md §6.1), where
// uploaded files may be moved or deleted from the export folder without
// demoting saved or queried items to missing_files. Default is false.
func (s *Store) SetAllowMovedFiles(allow bool) {
	s.mu.Lock()
	s.allowMovedFiles = allow
	s.mu.Unlock()
}

// AllowMovedFiles reports whether remote/export allow-moved-files mode is active.
func (s *Store) AllowMovedFiles() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.allowMovedFiles
}

// LibraryRoots returns all registered library root folders (active cacheDir first).
func (s *Store) LibraryRoots() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.allRootsLocked()
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
		if clean == "" || clean == "." {
			continue
		}
		k := pathKey(clean)
		if !seen[k] {
			seen[k] = true
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
		key := pathKey(clean)
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
// Disk SHA-256 verification runs outside s.mu.Lock() so large media files never
// block concurrent live checkpoints or UI library queries.
func (s *Store) SaveItem(itemDir string, meta ItemMetadata) (ItemMetadata, error) {
	return s.saveItem(itemDir, meta, true)
}

func normalizeItemMetadataForSave(itemDir string, meta ItemMetadata) ItemMetadata {
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
	if meta.Site == "weibo" && meta.Kind == "video" {
		meta.RawID = strings.ReplaceAll(meta.RawID, ":", "_")
	}
	meta.ItemKey = CanonicalItemKey(meta.Site, meta.Kind, meta.RawID)
	SanitizeItemMetadata(&meta)
	if meta.CachedAt == "" {
		meta.CachedAt = time.Now().UTC().Format(time.RFC3339)
	}
	meta.ItemDir = filepath.Clean(itemDir)
	return meta
}

func applyVerifiedStatus(meta *ItemMetadata, missing bool, totalBytes int64) {
	meta.TotalBytes = totalBytes
	if missing && meta.Status != "recording" && meta.Status != "failed" && meta.Status != "partial" {
		meta.Status = "missing_files"
	} else if meta.Status == "" || (meta.Status == "missing_files" && !missing) {
		meta.Status = "complete"
	}
}

func hasMediaSHA256(media []MediaFile) bool {
	for _, m := range media {
		if strings.TrimSpace(m.SHA256) != "" {
			return true
		}
	}
	return false
}

func (s *Store) saveItem(itemDir string, meta ItemMetadata, checkHash bool) (ItemMetadata, error) {
	baseMeta := normalizeItemMetadataForSave(itemDir, meta)
	origMedia := slices.Clone(baseMeta.Media)
	needsHash := checkHash && hasMediaSHA256(origMedia)

	const maxAttempts = 2
	for attempt := 0; attempt < maxAttempts; attempt++ {
		var (
			hashSnaps map[hashSnapKey]fileStatSnap
			didHash   bool
		)
		if needsHash {
			s.mu.RLock()
			allowMoved := s.allowMovedFiles
			s.mu.RUnlock()

			if !allowMoved {
				hashWork := baseMeta
				hashWork.Media = slices.Clone(origMedia)
				_, _, hashSnaps = verifyItemFilesWithSnaps(hashWork.ItemDir, &hashWork, true)
				didHash = true
			}
		}

		s.mu.Lock()
		work := baseMeta
		work.Media = slices.Clone(origMedia)

		if s.allowMovedFiles {
			work.TotalBytes = sumItemBytesAllowMoved(work.ItemDir, &work)
			if work.Status == "" {
				work.Status = "complete"
			}
		} else if needsHash {
			if !didHash {
				// allowMovedFiles transitioned true -> false between RLock and Lock:
				// unlock and run the unlocked hash pass instead of hashing under s.mu.
				if attempt+1 < maxAttempts {
					s.mu.Unlock()
					continue
				}
				missing, totalBytes := verifyItemFiles(work.ItemDir, &work, false)
				applyVerifiedStatus(&work, missing || true, totalBytes)
			} else {
				statMissing, statBytes, drifted := verifyItemFilesAgainstSnaps(work.ItemDir, &work, hashSnaps)
				if drifted {
					if attempt+1 < maxAttempts {
						s.mu.Unlock()
						continue
					}
					statMissing = true
				}
				applyVerifiedStatus(&work, statMissing, statBytes)
			}
		} else {
			missing, totalBytes := verifyItemFiles(work.ItemDir, &work, false)
			applyVerifiedStatus(&work, missing, totalBytes)
		}

		saved, err := s.commitSavedItemLocked(work)
		s.mu.Unlock()
		return saved, err
	}

	return ItemMetadata{}, fmt.Errorf("library: 保存条目校验重试超限")
}

func (s *Store) commitSavedItemLocked(meta ItemMetadata) (ItemMetadata, error) {
	itemJSONPath := filepath.Join(meta.ItemDir, "item.json")
	if raw, err := os.ReadFile(itemJSONPath); err == nil {
		previous, err := ParseItemMetadata(raw)
		if err != nil || previous.ItemKey != meta.ItemKey {
			return ItemMetadata{}, fmt.Errorf("library: 目标清单无法确认归属，原文件已保留")
		}
	} else if !os.IsNotExist(err) {
		return ItemMetadata{}, err
	}
	if meta.CachedBy == nil && s.node != nil {
		n := *s.node
		meta.CachedBy = &n
	}
	// The folder may have been emptied and removed by an upload (rclone
	// move); the manifest is written into a new one (remote.md §6.1).
	if err := os.MkdirAll(meta.ItemDir, 0o700); err != nil {
		return ItemMetadata{}, err
	}
	if err := storage.WriteJSONAtomic(itemJSONPath, meta, 0o600); err != nil {
		return ItemMetadata{}, err
	}

	if err := s.upsertItemRowLocked(meta); err != nil {
		return ItemMetadata{}, err
	}
	s.mutationSeq++
	for _, fn := range s.onSaved {
		fn(meta)
	}
	return meta, nil
}

// OnSaved registers fn to run after every manifest SaveItem (and recording
// checkpoint) writes. fn runs with the store locked, so it must only hand
// the item on (the uploader queues it) and never call back into the store.
func (s *Store) OnSaved(fn func(ItemMetadata)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onSaved = append(s.onSaved, fn)
}

func (s *Store) upsertItemRowLocked(meta ItemMetadata) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := upsertItemRowTx(tx, meta); err != nil {
		return err
	}
	return tx.Commit()
}

func upsertItemRowTx(tx *sql.Tx, meta ItemMetadata) error {
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
	return err
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
	if entry.Site == "weibo" && entry.Kind == "video" {
		entry.RawID = strings.ReplaceAll(entry.RawID, ":", "_")
	}
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
	switch entry.Site {
	case "douyin":
		if isRemoteOrSignedURL(entry.CoverURL) {
			entry.CoverURL = ""
		}
		if isRemoteOrSignedURL(entry.AuthorFace) {
			entry.AuthorFace = ""
		}
	case "weibo":
		entry.CoverURL = sanitizeWeiboManifestURL(entry.CoverURL)
		entry.AuthorFace = sanitizeWeiboManifestURL(entry.AuthorFace)
	case "x":
		entry.CoverURL = sanitizeXManifestURL(entry.CoverURL)
		entry.AuthorFace = sanitizeXManifestURL(entry.AuthorFace)
	default:
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
	if err == nil {
		s.mutationSeq++
	}
	return err
}

// GetItem retrieves a single cached ItemMetadata by canonical itemKey ("<site>:<kind>:<id>"), short key ("<kind>:<id>"), or rawID.
func (s *Store) GetItem(keyOrID string) (ItemMetadata, bool, error) {
	s.mu.RLock()
	allowMoved := s.allowMovedFiles
	meta, ok, err := s.getItemLocked(keyOrID)
	s.mu.RUnlock()
	if err != nil || !ok {
		return meta, ok, err
	}
	if !allowMoved {
		refreshItemFileState(&meta)
	}
	return meta, true, nil
}

// GetItemMetaOnly retrieves a single cached ItemMetadata from the SQLite index
// without verifying files on disk (no os.Lstat / EvalSymlinks syscalls).
// Useful for UI card/thumbnail cover lookups where RefreshIndex / States handle
// disk integrity checks.
func (s *Store) GetItemMetaOnly(keyOrID string) (ItemMetadata, bool, error) {
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
	switch {
	case strings.Count(keyOrID, ":") == 0:
		err = s.db.QueryRow(`
			SELECT json_data, item_dir, status, total_bytes, upgrade_available, upgrade_hint
			FROM library_items
			WHERE item_key = ? OR raw_id = ?
			LIMIT 1
		`, keyOrID, keyOrID).Scan(&jsonStr, &itemDir, &status, &totalBytes, &upAvail, &upHint)
	case strings.Count(keyOrID, ":") == 1 || strings.HasPrefix(strings.ToLower(keyOrID), "live:"):
		err = s.db.QueryRow(`
			SELECT json_data, item_dir, status, total_bytes, upgrade_available, upgrade_hint
			FROM library_items
			WHERE item_key = ? OR item_key = ?
			LIMIT 1
		`, keyOrID, "bilibili:"+keyOrID).Scan(&jsonStr, &itemDir, &status, &totalBytes, &upAvail, &upHint)
	default:
		normKey := normalizeLookupKey(keyOrID)
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
	return meta, true, nil
}

func normalizeLookupKey(k string) string {
	lower := strings.ToLower(k)
	if strings.HasPrefix(lower, "douyin:note:") {
		return "douyin:dynamic:" + k[len("douyin:note:"):]
	}
	if strings.HasPrefix(lower, "weibo:video:") {
		return "weibo:video:" + strings.ReplaceAll(k[len("weibo:video:"):], ":", "_")
	}
	return k
}

func refreshItemFileState(meta *ItemMetadata) {
	if missing, actualBytes := verifyItemFiles(meta.ItemDir, meta, false); missing && meta.Status != "recording" {
		if hasInterruptedSwapDirs(meta.ItemDir) && RecoverInterruptedItemSwap(meta.ItemDir, *meta) == nil {
			missing, actualBytes = verifyItemFiles(meta.ItemDir, meta, false)
		}
		if missing {
			meta.Status = "missing_files"
			meta.TotalBytes = actualBytes
		} else {
			meta.TotalBytes = actualBytes
		}
	}
}

func (s *Store) fetchItemsBatchLocked(itemKeys, rawIDs []string, byKey, byRaw map[string]ItemMetadata) {
	const chunkSize = 400
	queryChunk := func(col string, vals []string) {
		for start := 0; start < len(vals); start += chunkSize {
			end := min(start+chunkSize, len(vals))
			chunk := vals[start:end]
			ph := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
			args := make([]any, len(chunk))
			for i, v := range chunk {
				args[i] = v
			}
			rows, err := s.db.Query(`SELECT item_key, raw_id, json_data, item_dir, status, total_bytes, upgrade_available, upgrade_hint FROM library_items WHERE `+col+` IN (`+ph+`)`, args...)
			if err != nil {
				continue
			}
			for rows.Next() {
				var itemKey, rawID, jsonStr, itemDir, status, upHint string
				var totalBytes int64
				var upAvail int
				if err := rows.Scan(&itemKey, &rawID, &jsonStr, &itemDir, &status, &totalBytes, &upAvail, &upHint); err != nil {
					continue
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
				byKey[itemKey] = meta
				if _, exists := byRaw[rawID]; !exists {
					byRaw[rawID] = meta
				}
			}
			_ = rows.Close()
		}
	}
	if len(itemKeys) > 0 {
		queryChunk("item_key", itemKeys)
	}
	if len(rawIDs) > 0 {
		queryChunk("raw_id", rawIDs)
	}
}

// States computes batch DotState for the given item keys (FR-CACHE-004).
func (s *Store) States(keys []string) map[string]DotState {
	out := make(map[string]DotState, len(keys))
	if len(keys) == 0 {
		return out
	}

	seenKey := make(map[string]bool, len(keys))
	seenRaw := make(map[string]bool)
	var itemKeys, rawIDs []string
	addKey := func(k string) {
		if k != "" && !seenKey[k] {
			seenKey[k] = true
			itemKeys = append(itemKeys, k)
		}
	}
	for _, raw := range keys {
		k := strings.TrimSpace(raw)
		if k == "" {
			continue
		}
		switch {
		case strings.Count(k, ":") == 0:
			addKey(k)
			if !seenRaw[k] {
				seenRaw[k] = true
				rawIDs = append(rawIDs, k)
			}
		case strings.Count(k, ":") == 1 || strings.HasPrefix(strings.ToLower(k), "live:"):
			addKey(k)
			addKey("bilibili:" + k)
		default:
			addKey(normalizeLookupKey(k))
		}
	}

	byKey := make(map[string]ItemMetadata, len(itemKeys))
	byRaw := make(map[string]ItemMetadata, len(rawIDs))

	s.mu.RLock()
	allowMoved := s.allowMovedFiles
	s.fetchItemsBatchLocked(itemKeys, rawIDs, byKey, byRaw)

	// Fetch any videos referenced by matched dynamics in one extra batched query.
	var refKeys []string
	seenRef := make(map[string]bool)
	for _, meta := range byKey {
		if meta.Kind == "dynamic" && len(meta.Videos) > 0 {
			for _, vid := range meta.Videos {
				vk := CanonicalItemKey(meta.Site, "video", vid)
				if _, ok := byKey[vk]; !ok && !seenRef[vk] {
					seenRef[vk] = true
					refKeys = append(refKeys, vk)
				}
			}
		}
	}
	if len(refKeys) > 0 {
		s.fetchItemsBatchLocked(refKeys, nil, byKey, byRaw)
	}
	s.mu.RUnlock()

	// Refresh on-disk file state outside the store lock once per unique item.
	if !allowMoved {
		for k, meta := range byKey {
			refreshItemFileState(&meta)
			byKey[k] = meta
		}
		for r, meta := range byRaw {
			if updated, ok := byKey[meta.ItemKey]; ok {
				byRaw[r] = updated
			}
		}
	}

	lookup := func(k string) (ItemMetadata, bool) {
		k = strings.TrimSpace(k)
		if k == "" {
			return ItemMetadata{}, false
		}
		switch {
		case strings.Count(k, ":") == 0:
			if m, ok := byKey[k]; ok {
				return m, true
			}
			m, ok := byRaw[k]
			return m, ok
		case strings.Count(k, ":") == 1 || strings.HasPrefix(strings.ToLower(k), "live:"):
			if m, ok := byKey[k]; ok {
				return m, true
			}
			m, ok := byKey["bilibili:"+k]
			return m, ok
		default:
			m, ok := byKey[normalizeLookupKey(k)]
			return m, ok
		}
	}

	for _, k := range keys {
		meta, ok := lookup(k)
		if !ok {
			out[k] = DotState{Key: k, Dot: "none", Label: "未缓存"}
			continue
		}
		dot := ComputeDotState(meta)
		if meta.Kind == "dynamic" && len(meta.Videos) > 0 {
			for _, vid := range meta.Videos {
				vMeta, vOk := byKey[CanonicalItemKey(meta.Site, "video", vid)]
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
		meta, err := ParseItemMetadata([]byte(jsonStr))
		if err != nil {
			return nil, err
		}
		meta.ItemDir = itemDir
		meta.Status = status
		meta.TotalBytes = totalBytes
		meta.UpgradeAvailable = upAvail == 1
		meta.UpgradeHint = upHint
		out = append(out, meta)
	}
	return out, rows.Err()
}
