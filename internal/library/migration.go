package library

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/chialecode/trace/internal/storage"
)

func (s *Store) moveRegisteredCache(newDir string, persist func() error) (RebuildReport, error) {
	if strings.TrimSpace(newDir) == "" || !filepath.IsAbs(newDir) {
		return RebuildReport{}, fmt.Errorf("library: 保存位置必须是绝对路径")
	}
	newDir = filepath.Clean(newDir)
	if err := rejectDirectoryLinks(newDir); err != nil {
		return RebuildReport{}, err
	}
	if err := os.MkdirAll(newDir, 0700); err != nil {
		return RebuildReport{}, err
	}
	s.mu.RLock()
	oldDir := s.cacheDir
	items, err := s.listAllItemsLocked()
	s.mu.RUnlock()
	if err != nil {
		return RebuildReport{}, err
	}
	if samePath(oldDir, newDir) {
		if persist != nil {
			if err := persist(); err != nil {
				return RebuildReport{}, err
			}
		}
		return s.RebuildIndex()
	}

	type migratedItem struct {
		itemKey string
		itemDir string
		files   []string
	}
	var migrated []migratedItem
	var copies []string
	rollback := func() {
		for _, path := range copies {
			_ = os.Remove(path)
		}
		for _, item := range items {
			if rel, err := filepath.Rel(oldDir, item.ItemDir); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
				removeEmptyDirsBottomUp(filepath.Join(newDir, rel))
			}
		}
	}

	for _, item := range items {
		if _, err := EnsureWithinRoot(oldDir, item.ItemDir); err != nil {
			continue
		}
		if item.Status == "recording" {
			rollback()
			return RebuildReport{}, fmt.Errorf("library: 正在录制的文件不能迁移")
		}
		relDir, err := filepath.Rel(oldDir, item.ItemDir)
		if err != nil || relDir == "." {
			rollback()
			return RebuildReport{}, fmt.Errorf("library: 条目目录无效")
		}
		destDir := filepath.Join(newDir, relDir)
		if err := rejectDirectoryLinks(destDir); err != nil {
			rollback()
			return RebuildReport{}, err
		}
		if err := os.MkdirAll(destDir, 0700); err != nil {
			rollback()
			return RebuildReport{}, err
		}

		// Map expected SHA-256 digests from the manifest for verification during copy.
		expectedSHA := map[string]string{}
		for _, mf := range RegisteredFiles(item) {
			if mf.SHA256 != "" {
				expectedSHA[strings.ToLower(mf.Path)] = mf.SHA256
			}
		}

		registered, _ := collectRegisteredAndUserFiles(item)
		var origForItem []string
		for _, source := range registered {
			if _, err := os.Lstat(source); os.IsNotExist(err) {
				continue
			}
			rel, err := filepath.Rel(item.ItemDir, source)
			relSlash := filepath.ToSlash(rel)
			if err != nil || !safeRegisteredPathStrict(item.ItemDir, relSlash) {
				rollback()
				return RebuildReport{}, fmt.Errorf("library: 迁移文件路径无效")
			}
			dest, err := ResolveItemPath(destDir, relSlash)
			if err != nil {
				rollback()
				return RebuildReport{}, err
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
				rollback()
				return RebuildReport{}, err
			}
			if _, err := os.Lstat(dest); err == nil {
				rollback()
				return RebuildReport{}, fmt.Errorf("library: 目标文件已存在: %s", dest)
			}
			if err := copyFileAtomicVerified(source, dest, expectedSHA[strings.ToLower(cleanManifestRel(relSlash))]); err != nil {
				rollback()
				return RebuildReport{}, err
			}
			copies = append(copies, dest)
			origForItem = append(origForItem, source)
		}
		if len(origForItem) > 0 {
			migrated = append(migrated, migratedItem{itemKey: item.ItemKey, itemDir: item.ItemDir, files: origForItem})
		}
	}

	if err := s.ChangeCacheDir(newDir, persist); err != nil {
		rollback()
		return RebuildReport{}, err
	}
	report, err := s.RebuildIndex()
	if err != nil {
		return report, err
	} // Original files remain recoverable on any indexing failure.

	// Clean up migrated originals; never abort mid-way, record any locked files
	// into pending-deletes.json so they are cleaned on next startup.
	var removeErrs []error
	var leftover []pendingDeleteRecord
	for _, mi := range migrated {
		var failedFiles []string
		for _, path := range mi.files {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				failedFiles = append(failedFiles, path)
				removeErrs = append(removeErrs, err)
			}
		}
		if len(failedFiles) > 0 {
			leftover = append(leftover, pendingDeleteRecord{
				ItemKey:         mi.itemKey + ":migrated",
				ItemDir:         mi.itemDir,
				RegisteredFiles: failedFiles,
			})
		}
		removeEmptyDirsBottomUp(mi.itemDir)
		removeEmptyAuthorDirs(mi.itemDir, []string{oldDir})
	}
	if len(leftover) > 0 {
		s.mu.Lock()
		if existing, rErr := s.readPendingDeletesLocked(); rErr == nil {
			_ = s.writePendingDeletesLocked(append(existing, leftover...))
		}
		s.mu.Unlock()
	}
	if len(removeErrs) > 0 {
		return report, errors.Join(removeErrs...)
	}
	return report, nil
}

func copyFileAtomicVerified(src, dst, wantSHA string) error {
	input, err := os.Open(src)
	if err != nil {
		return err
	}
	defer input.Close()

	tmp := storage.TempSiblingPath(dst)
	output, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	cleanupTmp := func() {
		_ = output.Close()
		_ = os.Remove(tmp)
	}

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(output, h), input); err != nil {
		cleanupTmp()
		return err
	}
	if err := output.Sync(); err != nil {
		cleanupTmp()
		return err
	}
	if err := output.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if wantSHA != "" {
		gotSHA := hex.EncodeToString(h.Sum(nil))
		if !strings.EqualFold(gotSHA, wantSHA) {
			_ = os.Remove(tmp)
			return fmt.Errorf("library: 迁移校验和不一致 (%s)", filepath.Base(dst))
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
