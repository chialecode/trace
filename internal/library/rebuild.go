package library

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/chialecode/trace/internal/storage"
)

// RebuildIndex walks all registered library roots for item.json files, backs up corrupt files,
// checks media file integrity (including hashes), and rebuilds the SQLite library_items index.
func (s *Store) RebuildIndex() (RebuildReport, error) {
	return s.rebuildIndex(true)
}

// RefreshIndex is the media library's "刷新": the same scan as RebuildIndex,
// but files are only checked for presence and size (no hashing), so it stays
// quick on large libraries. The report lists what changed on disk.
func (s *Store) RefreshIndex() (RebuildReport, error) {
	return s.rebuildIndex(false)
}

type indexedItem struct {
	site            string
	kind            string
	title           string
	status          string
	itemDir         string
	totalBytes      int64
	jsonData        string
	feedCached      int
	feedCacheStatus string
	feedItemKey     string
}

func (s *Store) indexedItemsLocked() (map[string]indexedItem, error) {
	rows, err := s.db.Query(`
		SELECT l.item_key, l.site, l.kind, l.title, l.status, l.item_dir, l.total_bytes, l.json_data,
		       COALESCE(f.cached, 0), COALESCE(f.cache_status, ''), COALESCE(f.item_key, '')
		FROM library_items l
		LEFT JOIN feed_items f ON f.feed_key = l.item_key
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]indexedItem)
	for rows.Next() {
		var key string
		var it indexedItem
		if err := rows.Scan(
			&key, &it.site, &it.kind, &it.title, &it.status, &it.itemDir, &it.totalBytes, &it.jsonData,
			&it.feedCached, &it.feedCacheStatus, &it.feedItemKey,
		); err != nil {
			return nil, err
		}
		out[key] = it
	}
	return out, rows.Err()
}

func indexedItemMatches(prev indexedItem, meta ItemMetadata, jsonData string) bool {
	return prev.status == meta.Status &&
		prev.totalBytes == meta.TotalBytes &&
		prev.itemDir == meta.ItemDir &&
		prev.jsonData == jsonData &&
		prev.feedCached == 1 &&
		prev.feedCacheStatus == meta.Status &&
		prev.feedItemKey == meta.ItemKey
}

// rebuildAfterWalkHook is a test-only hook fired after rebuildIndex finishes its
// unlocked filesystem walk and before it acquires s.mu.Lock() for the final phase.
var rebuildAfterWalkHook func()

func (s *Store) rebuildIndex(checkHash bool) (RebuildReport, error) {
	// Serialize overlapping scans without holding s.mu across disk I/O so UI
	// reads (GetItem, FeedPage, Statistics, CountCachedWorks) never block.
	s.rebuildMu.Lock()
	defer s.rebuildMu.Unlock()

	var report RebuildReport

	s.mu.RLock()
	startSeq := s.mutationSeq
	cacheDir := s.cacheDir
	previous, err := s.indexedItemsLocked()
	if err != nil {
		s.mu.RUnlock()
		return report, err
	}
	roots := s.allRootsLocked()
	pending, err := s.readPendingDeletesLocked()
	if err != nil {
		s.mu.RUnlock()
		return report, err
	}
	walkHook := s.onRebuildWalk
	s.mu.RUnlock()

	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return report, err
	}

	pendingMap := make(map[string]bool, len(pending))
	for _, p := range pending {
		pendingMap[p.ItemKey] = true
	}

	if walkHook != nil {
		walkHook()
	}

	type scannedItem struct {
		path            string
		meta            ItemMetadata
		jsonData        string
		diskWriteNeeded bool
		missingFiles    bool
		newlyMissing    *ChangedItem
	}
	seenKeys := make(map[string]bool, len(previous))
	var discovered []scannedItem
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
			// Never follow symlinks or Windows directory junctions (INV-08).
			if isLinkLike(d.Type()) {
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
				if os.IsNotExist(rErr) {
					return nil
				}
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
			prevStatus := meta.Status
			prevTotalBytes := meta.TotalBytes
			missing, totalBytes, missingPaths := verifyItemFilesDetail(meta.ItemDir, &meta, checkHash)
			meta.TotalBytes = totalBytes
			diskWriteNeeded := false
			missingFiles := false
			var newlyMissing *ChangedItem
			if missing && meta.Status != "recording" && meta.Status != "failed" && meta.Status != "partial" {
				if prev, ok := previous[meta.ItemKey]; ok && prev.status != "missing_files" {
					ci := ChangedItem{ItemKey: meta.ItemKey, Site: meta.Site, Kind: meta.Kind, Title: meta.Title, ItemDir: meta.ItemDir, Files: missingPaths}
					newlyMissing = &ci
				}
				meta.Status = "missing_files"
				missingFiles = true
				if prevStatus != "missing_files" || meta.TotalBytes != prevTotalBytes {
					diskWriteNeeded = true
				}
			} else if meta.Status == "missing_files" && !missing {
				meta.Status = "complete"
				diskWriteNeeded = true
			}

			sanitized := meta
			SanitizeItemMetadata(&sanitized)
			jsonBytes, mErr := json.Marshal(sanitized)
			if mErr != nil {
				return mErr
			}

			discovered = append(discovered, scannedItem{
				path:            path,
				meta:            sanitized,
				jsonData:        string(jsonBytes),
				diskWriteNeeded: diskWriteNeeded,
				missingFiles:    missingFiles,
				newlyMissing:    newlyMissing,
			})
			seenKeys[meta.ItemKey] = true
			return nil
		})
		if err != nil {
			return report, err
		}
	}

	if hook := rebuildAfterWalkHook; hook != nil {
		hook()
	}

	var stale []string
	for k := range previous {
		if !seenKeys[k] {
			stale = append(stale, k)
		}
	}

	// Acquire s.mu.Lock() only for the final diff, disk manifest updates, and SQLite commit.
	s.mu.Lock()
	defer s.mu.Unlock()

	current := previous
	currentPending := pendingMap
	mutatedDuringWalk := s.mutationSeq != startSeq
	if mutatedDuringWalk {
		current, err = s.indexedItemsLocked()
		if err != nil {
			return report, err
		}
		latestPending, pErr := s.readPendingDeletesLocked()
		if pErr != nil {
			return report, pErr
		}
		currentPending = make(map[string]bool, len(latestPending))
		for _, p := range latestPending {
			currentPending[p.ItemKey] = true
		}
	}

	toUpsert := make([]scannedItem, 0, len(discovered))
	for _, sc := range discovered {
		k := sc.meta.ItemKey
		if currentPending[k] {
			continue
		}
		if mutatedDuringWalk {
			if prev, wasInPrev := previous[k]; wasInPrev {
				cur, stillInCur := current[k]
				if !stillInCur || cur != prev {
					// Deleted or updated by a concurrent SaveItem/Delete while we walked.
					continue
				}
			} else if _, nowInCur := current[k]; nowInCur {
				// Created by a concurrent SaveItem/AddLibraryRoot while we walked.
				continue
			}
			// Even if a concurrent SaveItem wrote identical SQLite metadata while
			// restoring files on disk, re-verify before overwriting item.json.
			if sc.diskWriteNeeded {
				if _, statErr := os.Stat(sc.path); statErr != nil {
					continue
				}
				recheck := sc.meta
				stillMissing, _, _ := verifyItemFilesDetail(recheck.ItemDir, &recheck, false)
				if stillMissing != sc.missingFiles {
					continue
				}
			}
		}
		report.Indexed++
		if sc.missingFiles {
			report.MissingFiles++
		}
		if sc.newlyMissing != nil {
			report.NewlyMissing = append(report.NewlyMissing, *sc.newlyMissing)
		}
		if !checkHash && !sc.diskWriteNeeded {
			if cur, ok := current[k]; ok && indexedItemMatches(cur, sc.meta, sc.jsonData) {
				continue
			}
		}
		toUpsert = append(toUpsert, sc)
	}

	toDelete := make([]string, 0, len(stale))
	for _, k := range stale {
		prev := previous[k]
		if mutatedDuringWalk {
			cur, stillInCur := current[k]
			if !stillInCur || cur != prev {
				continue
			}
		}
		toDelete = append(toDelete, k)
		if !currentPending[k] {
			report.Removed = append(report.Removed, ChangedItem{ItemKey: k, Site: prev.site, Kind: prev.kind, Title: prev.title, ItemDir: prev.itemDir})
		}
	}

	if len(toUpsert) == 0 && len(toDelete) == 0 {
		return report, nil
	}

	for _, sc := range toUpsert {
		if sc.diskWriteNeeded {
			if err := storage.WriteJSONAtomic(sc.path, sc.meta, 0o600); err != nil {
				return report, err
			}
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	for _, sc := range toUpsert {
		if err := upsertItemRowTx(tx, sc.meta); err != nil {
			return report, err
		}
	}
	for _, k := range toDelete {
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
	if err := tx.Commit(); err != nil {
		return report, err
	}
	s.mutationSeq++
	return report, nil
}

// AddLibraryRoot scans an existing cache folder (including bilibili/*, douyin/*/{works,lives}/*,
// weibo/*/{posts,forwards}/*, and x/*/{posts,forwards}/*) or legacy trace-data folder containing
// library/, and indexes its items without moving files or overwriting existing keys (FR-LIB-006).
func (s *Store) AddLibraryRoot(selectedDir string) (AddRootReport, error) {
	if strings.TrimSpace(selectedDir) == "" || !filepath.IsAbs(selectedDir) {
		return AddRootReport{}, fmt.Errorf("library: folder path cannot be empty")
	}
	selectedDir = filepath.Clean(selectedDir)
	info, err := os.Lstat(selectedDir)
	if err != nil {
		return AddRootReport{}, fmt.Errorf("library: stat folder: %w", err)
	}
	if isLinkLike(info.Mode()) {
		return AddRootReport{}, fmt.Errorf("library: symlink root folder is not allowed")
	}
	if !info.IsDir() {
		return AddRootReport{}, fmt.Errorf("library: %s is not a directory", selectedDir)
	}

	// Check if selectedDir is a legacy portable trace-data directory containing a library/ subdirectory (even without settings.json).
	scanRoot := selectedDir
	libSub := filepath.Join(selectedDir, "library")
	if lInfo, lErr := os.Lstat(libSub); lErr == nil && lInfo.IsDir() && !isLinkLike(lInfo.Mode()) {
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
					if cInfo, cErr := os.Lstat(cand); cErr == nil && cInfo.IsDir() && !isLinkLike(cInfo.Mode()) {
						scanRoot = filepath.Clean(cand)
					}
				}
			}
		}
	}

	s.rebuildMu.Lock()
	defer s.rebuildMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	report := AddRootReport{Root: scanRoot}
	if err := rejectDirectoryLinks(scanRoot); err != nil {
		return report, err
	}
	existing, err := s.indexedItemsLocked()
	if err != nil {
		return report, err
	}
	oldRoots := append([]string(nil), s.extraRoots...)
	found := false
	for _, r := range s.allRootsLocked() {
		found = found || samePath(r, scanRoot)
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

	var toInsert []ItemMetadata
	seenNew := make(map[string]bool)
	err = filepath.WalkDir(scanRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		// Never follow symlinks or Windows directory junctions (INV-08).
		if isLinkLike(d.Type()) {
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
		if _, ok := existing[meta.ItemKey]; ok || seenNew[meta.ItemKey] {
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
		toInsert = append(toInsert, meta)
		seenNew[meta.ItemKey] = true
		report.Added++
		return nil
	})
	if err != nil {
		return report, err
	}
	if len(toInsert) == 0 {
		return report, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	for _, meta := range toInsert {
		if err := upsertItemRowTx(tx, meta); err != nil {
			return report, err
		}
	}
	if err := tx.Commit(); err != nil {
		return report, err
	}
	s.mutationSeq++
	return report, nil
}

func jsonUnmarshal(raw []byte, v any) error {
	if len(raw) == 0 {
		return errors.New("empty json")
	}
	return json.Unmarshal(raw, v)
}
