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
	plan, _, err := s.planDeleteBatchLocked([]string{itemKey})
	return plan, err
}

// PlanDeleteBatch computes the exact DeletePlan for multiple items as a single
// batch without modifying disk or DB (FR-LIB-003, INV-09, DEC-083).
// Shared referenced videos are evaluated excluding the entire selected batch:
// if every referencing dynamic is in the batch (and the video was not directly
// cached), the video is included in the deletion plan; if any unselected
// dynamic still references it or it was directly cached, it is preserved.
func (s *Store) PlanDeleteBatch(itemKeys []string) (DeletePlan, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	plan, _, err := s.planDeleteBatchLocked(itemKeys)
	return plan, err
}

func (s *Store) planDeleteBatchLocked(itemKeys []string) (DeletePlan, []ItemMetadata, error) {
	var targets []ItemMetadata
	selectedSet := make(map[string]bool, len(itemKeys))
	for _, rawKey := range itemKeys {
		k := strings.TrimSpace(rawKey)
		if k == "" {
			continue
		}
		meta, ok, err := s.getItemLocked(k)
		if err != nil {
			return DeletePlan{}, nil, err
		}
		if !ok {
			return DeletePlan{}, nil, fmt.Errorf("library: item %q not found", k)
		}
		if meta.Status == "recording" {
			return DeletePlan{}, nil, fmt.Errorf("library: 正在录制的直播条目不能删除，请先停止录制")
		}
		if selectedSet[meta.ItemKey] {
			continue
		}
		selectedSet[meta.ItemKey] = true
		targets = append(targets, meta)
	}
	if len(targets) == 0 {
		return DeletePlan{}, nil, fmt.Errorf("library: 请先选择要删除的条目")
	}

	plan := DeletePlan{
		TargetKey:  targets[0].ItemKey,
		TargetKeys: make([]string, 0, len(targets)),
		ItemKeys:   make([]string, 0, len(targets)),
	}
	itemsToDelete := make([]ItemMetadata, 0, len(targets))
	seenDeleteItems := make(map[string]bool, len(targets))
	for _, meta := range targets {
		plan.TargetKeys = append(plan.TargetKeys, meta.ItemKey)
		plan.ItemKeys = append(plan.ItemKeys, meta.ItemKey)
		itemsToDelete = append(itemsToDelete, meta)
		seenDeleteItems[meta.ItemKey] = true
	}

	// If deleting dynamic items that reference videos, check whether each referenced
	// video has any reference OUTSIDE the entire selected batch or was directly cached.
	seenPreservedShared := make(map[string]bool)
	for _, meta := range targets {
		if meta.Kind != "dynamic" || len(meta.Videos) == 0 {
			continue
		}
		for _, vid := range meta.Videos {
			vidKey := CanonicalItemKey(meta.Site, "video", vid)
			if seenDeleteItems[vidKey] || seenPreservedShared[vidKey] {
				continue
			}
			vMeta, vOk, err := s.getItemLocked(vidKey)
			if err != nil {
				return DeletePlan{}, nil, fmt.Errorf("library: check referenced video %s: %w", vidKey, err)
			}
			if !vOk {
				continue
			}
			hasExternalRef, err := s.hasRefOutsideBatchLocked(vidKey, selectedSet)
			if err != nil {
				return DeletePlan{}, nil, fmt.Errorf("library: check shared refs for %s: %w", vidKey, err)
			}
			if hasExternalRef || vMeta.DirectCached {
				seenPreservedShared[vidKey] = true
				plan.PreservedSharedVideos = append(plan.PreservedSharedVideos, vidKey)
			} else {
				if vMeta.Status == "recording" {
					return DeletePlan{}, nil, fmt.Errorf("library: 正在录制的直播条目不能删除，请先停止录制")
				}
				seenDeleteItems[vidKey] = true
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
			fk := pathKey(f)
			if !seenFiles[fk] {
				seenFiles[fk] = true
				plan.RegisteredFiles = append(plan.RegisteredFiles, f)
			}
		}
		for _, uf := range userFiles {
			uKey := pathKey(it.ItemDir) + "\x00" + uf
			if !seenUserFiles[uKey] {
				seenUserFiles[uKey] = true
				plan.PreservedUserFiles = append(plan.PreservedUserFiles, uf)
			}
		}
	}
	return plan, itemsToDelete, nil
}

func (s *Store) hasRefOutsideBatchLocked(toKey string, selectedSet map[string]bool) (bool, error) {
	rows, err := s.db.Query(`SELECT from_key FROM item_refs WHERE to_key = ?`, toKey)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var fromKey string
		if err := rows.Scan(&fromKey); err != nil {
			return false, err
		}
		if !selectedSet[fromKey] {
			return true, nil
		}
	}
	return false, rows.Err()
}

func collectRegisteredAndUserFiles(meta ItemMetadata) (registeredAbs []string, userRel []string) {
	if meta.ItemDir == "" {
		return nil, nil
	}
	itemDir := filepath.Clean(meta.ItemDir)
	// knownRel tracks all safe TRACE-owned relative paths (even if absent on disk)
	// so WalkDir never misclassifies them or their companion *.part files as user files.
	// addedRel tracks existing regular files already included in registeredAbs.
	knownRel := make(map[string]bool)
	addedRel := make(map[string]bool)
	addExistingRel := func(rel string, deferItemJSON bool) {
		rel = strings.TrimSpace(rel)
		if rel == "" {
			return
		}
		if !safeRegisteredPathStrict(itemDir, rel) {
			return
		}
		cleanRel := filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel)))
		norm := strings.ToLower(cleanRel)
		if deferItemJSON && norm == "item.json" {
			return
		}
		knownRel[norm] = true
		if addedRel[norm] {
			return
		}
		abs := filepath.Join(itemDir, filepath.FromSlash(cleanRel))
		info, err := os.Lstat(abs)
		if err != nil || isLinkLike(info.Mode()) || !info.Mode().IsRegular() {
			return
		}
		addedRel[norm] = true
		registeredAbs = append(registeredAbs, abs)
	}

	visitManifestFiles(meta, func(f ManifestFile) {
		addExistingRel(f.Path, true)
	})
	for _, part := range meta.PendingParts {
		addExistingRel(part, true)
		if meta.Kind == "live" && strings.HasPrefix(part, "part-") && strings.HasSuffix(part, ".mp4.part") {
			// Both names are reserved before recording starts. A crash after the
			// final rename can leave the MP4 present before its media row is saved.
			addExistingRel(strings.TrimSuffix(part, ".part"), true)
		}
	}
	addExistingRel("orig.json", true)

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
			relSlash := filepath.ToSlash(rel)
			norm := strings.ToLower(relSlash)
			if norm == "item.json" {
				return nil
			}
			if strings.HasSuffix(norm, ".part") && knownRel[strings.TrimSuffix(norm, ".part")] {
				addExistingRel(relSlash, true)
				return nil
			}
			if !knownRel[norm] {
				userRel = append(userRel, relSlash)
			}
			return nil
		})
	}

	// item.json is always deleted last among registered files.
	addExistingRel("item.json", false)
	return registeredAbs, userRel
}

// Delete executes PlanDelete: removes only registered files and empty directories, never recursive RemoveAll (INV-09).
func (s *Store) Delete(itemKey string) (DeletePlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deleteBatchWithPlanLocked([]string{itemKey}, true)
}

// DeleteItem removes an item from the index and optionally deletes its registered files from disk (INV-09).
// It NEVER calls os.RemoveAll on the item directory; user-placed files are strictly preserved.
func (s *Store) DeleteItem(itemKey string, deleteFiles bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.deleteBatchWithPlanLocked([]string{itemKey}, deleteFiles)
	return err
}

// DeleteBatch removes multiple items as a single atomic index transaction and
// optionally deletes their registered files from disk (FR-LIB-003, INV-09, DEC-083).
// It NEVER calls os.RemoveAll on any directory; non-TRACE user files are strictly preserved.
func (s *Store) DeleteBatch(itemKeys []string, deleteFiles bool) (DeletePlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deleteBatchWithPlanLocked(itemKeys, deleteFiles)
}

func (s *Store) deleteBatchWithPlanLocked(itemKeys []string, deleteFiles bool) (DeletePlan, error) {
	plan, itemsToDelete, err := s.planDeleteBatchLocked(itemKeys)
	if err != nil {
		return DeletePlan{}, err
	}

	var itemDirs []string
	seenDirs := make(map[string]bool, len(itemsToDelete))
	for _, m := range itemsToDelete {
		if m.ItemDir != "" {
			clean := filepath.Clean(m.ItemDir)
			if !seenDirs[clean] {
				seenDirs[clean] = true
				itemDirs = append(itemDirs, clean)
			}
		}
	}

	// 1. Write pending-deletes.json before deleting files (data-and-cache.md §2.8).
	if deleteFiles && len(plan.RegisteredFiles) > 0 {
		pending, err := s.readPendingDeletesLocked()
		if err != nil {
			return DeletePlan{}, err
		}
		for _, m := range itemsToDelete {
			regFiles, _ := collectRegisteredAndUserFiles(m)
			pending = append(pending, pendingDeleteRecord{
				ItemKey:         m.ItemKey,
				ItemDir:         m.ItemDir,
				RegisteredFiles: regFiles,
			})
		}
		if err := s.writePendingDeletesLocked(pending); err != nil {
			return DeletePlan{}, err
		}
	}

	// 2. Remove from index and references.
	tx, err := s.db.Begin()
	if err != nil {
		return DeletePlan{}, err
	}
	defer tx.Rollback()
	for _, k := range plan.ItemKeys {
		if _, err := tx.Exec(`DELETE FROM library_items WHERE item_key = ?`, k); err != nil {
			return DeletePlan{}, err
		}
		if _, err := tx.Exec(`DELETE FROM item_refs WHERE from_key = ? OR to_key = ?`, k, k); err != nil {
			return DeletePlan{}, err
		}
		if _, err := tx.Exec(`UPDATE feed_items SET cached = 0, cache_status = '', item_key = '' WHERE item_key = ?`, k); err != nil {
			return DeletePlan{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return DeletePlan{}, err
	}
	s.mutationSeq++

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
