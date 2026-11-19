package library

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chialecode/trace/internal/storage"
)

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
	if err := jsonUnmarshal(raw, &list); err != nil {
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
	roots := s.allRootsLocked()
	var remaining []pendingDeleteRecord
	for _, rec := range pending {
		validRoot := false
		for _, root := range roots {
			if _, err := EnsureWithinRoot(root, rec.ItemDir); err == nil && !samePath(root, rec.ItemDir) {
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
			if err != nil || !safeRegisteredPathStrict(rec.ItemDir, filepath.ToSlash(rel)) {
				allGone = false
				continue
			}
			if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
				allGone = false
			}
		}
		if rec.ItemDir != "" {
			removeEmptyDirsBottomUp(rec.ItemDir)
			removeEmptyAuthorDirs(rec.ItemDir, roots)
		}
		if !allGone {
			remaining = append(remaining, rec)
		}
	}
	return s.writePendingDeletesLocked(remaining)
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
			vMeta, vOk, err := s.getItemLocked(vidKey)
			if err != nil {
				return DeletePlan{}, fmt.Errorf("library: check referenced video %s: %w", vidKey, err)
			}
			if !vOk {
				continue
			}
			var otherRefs int
			if err := s.db.QueryRow(`SELECT COUNT(1) FROM item_refs WHERE to_key = ? AND from_key != ?`, vidKey, meta.ItemKey).Scan(&otherRefs); err != nil {
				return DeletePlan{}, fmt.Errorf("library: check shared refs for %s: %w", vidKey, err)
			}
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
		if !safeRegisteredPathStrict(itemDir, rel) {
			return
		}
		if !regRel[norm] {
			regRel[norm] = true
			registeredAbs = append(registeredAbs, filepath.Join(itemDir, filepath.FromSlash(rel)))
		}
	}

	visitManifestFiles(meta, func(f ManifestFile) {
		addRel(f.Path)
	})
	for _, part := range meta.PendingParts {
		addRel(part)
		if meta.Kind == "live" && strings.HasPrefix(part, "part-") && strings.HasSuffix(part, ".mp4.part") {
			// Both names are reserved before recording starts. A crash after the
			// final rename can leave the MP4 present before its media row is saved.
			addRel(strings.TrimSuffix(part, ".part"))
		}
	}
	addRel("orig.json")

	// Scan itemDir for *.part files (which belong to TRACE) and user-owned files (which MUST be preserved).
	// Never traverse symlinks or Windows directory junctions (INV-08/09).
	if rejectDirectoryLinks(itemDir) == nil {
		_ = filepath.WalkDir(itemDir, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if isLinkLike(d.Type()) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
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
	}

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
		roots := s.allRootsLocked()
		for _, dir := range itemDirs {
			removeEmptyDirsBottomUp(dir)
			removeEmptyAuthorDirs(dir, roots)
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
// ONLY if they are completely empty and not symlinks/junctions (using os.Remove, never os.RemoveAll).
func removeEmptyDirsBottomUp(itemDir string) {
	itemDir = filepath.Clean(itemDir)
	if itemDir == "" || itemDir == "." {
		return
	}
	if info, err := os.Lstat(itemDir); err != nil || !info.IsDir() || isLinkLike(info.Mode()) {
		return
	}
	var dirs []string
	_ = filepath.WalkDir(itemDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if isLinkLike(d.Type()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	})
	// Sort deepest paths first.
	sort.Slice(dirs, func(i, j int) bool {
		return len(dirs[i]) > len(dirs[j])
	})
	for _, d := range dirs {
		info, err := os.Lstat(d)
		if err != nil || !info.IsDir() || isLinkLike(info.Mode()) {
			continue
		}
		entries, err := os.ReadDir(d)
		if err == nil && len(entries) == 0 {
			_ = os.Remove(d)
		}
	}
}

// removeEmptyAuthorDirs removes the now-empty kind folder (videos/, works/…)
// and author folder above a deleted item, so deleting a creator's last item
// leaves no empty folders. It only climbs inside a library root, never removes
// the root or its site folder (<root>/bilibili), and uses os.Remove, which
// fails on any folder that still has files.
func removeEmptyAuthorDirs(itemDir string, roots []string) {
	itemDir = filepath.Clean(itemDir)
	for _, root := range roots {
		root = filepath.Clean(root)
		rel, err := filepath.Rel(root, itemDir)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			continue
		}
		// <site>/<author>/<kind>/<item>: remove <kind>, then <author>.
		depth := len(strings.Split(filepath.ToSlash(rel), "/"))
		dir := filepath.Dir(itemDir)
		for depth--; depth >= 2; depth-- {
			if info, err := os.Lstat(dir); err != nil || !info.IsDir() || isLinkLike(info.Mode()) {
				return
			}
			if os.Remove(dir) != nil {
				return
			}
			dir = filepath.Dir(dir)
		}
		return
	}
}
