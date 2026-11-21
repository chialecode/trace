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
	site, kind, title, status, itemDir string
}

func (s *Store) indexedItemsLocked() (map[string]indexedItem, error) {
	rows, err := s.db.Query(`SELECT item_key, site, kind, title, status, item_dir FROM library_items`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]indexedItem)
	for rows.Next() {
		var key string
		var it indexedItem
		if err := rows.Scan(&key, &it.site, &it.kind, &it.title, &it.status, &it.itemDir); err != nil {
			return nil, err
		}
		out[key] = it
	}
	return out, rows.Err()
}

func (s *Store) rebuildIndex(checkHash bool) (RebuildReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var report RebuildReport
	previous, err := s.indexedItemsLocked()
	if err != nil {
		return report, err
	}
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
	var discovered []ItemMetadata
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
			missing, totalBytes, missingPaths := verifyItemFilesDetail(meta.ItemDir, &meta, checkHash)
			meta.TotalBytes = totalBytes
			if missing && meta.Status != "recording" && meta.Status != "failed" && meta.Status != "partial" {
				if prev, ok := previous[meta.ItemKey]; ok && prev.status != "missing_files" {
					report.NewlyMissing = append(report.NewlyMissing, ChangedItem{ItemKey: meta.ItemKey, Site: meta.Site, Kind: meta.Kind, Title: meta.Title, ItemDir: meta.ItemDir, Files: missingPaths})
				}
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

			discovered = append(discovered, meta)
			seenKeys[meta.ItemKey] = true
			report.Indexed++
			return nil
		})
		if err != nil {
			return report, err
		}
	}

	var stale []string
	for k, prev := range previous {
		if !seenKeys[k] {
			stale = append(stale, k)
			if !pendingMap[k] {
				report.Removed = append(report.Removed, ChangedItem{ItemKey: k, Site: prev.site, Kind: prev.kind, Title: prev.title, ItemDir: prev.itemDir})
			}
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	for _, meta := range discovered {
		if err := upsertItemRowTx(tx, meta); err != nil {
			return report, err
		}
	}
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

// AddLibraryRoot scans an existing cache folder (including bilibili/*, douyin/*/{works,lives}/*,
// and weibo/*/{posts,forwards}/*) or legacy trace-data folder containing library/, and indexes its
// items without moving files or overwriting existing keys (FR-LIB-006).
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
	return report, nil
}

func jsonUnmarshal(raw []byte, v any) error {
	if len(raw) == 0 {
		return errors.New("empty json")
	}
	return json.Unmarshal(raw, v)
}
