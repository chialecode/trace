package library

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	if strings.EqualFold(oldDir, newDir) {
		if persist != nil {
			if err := persist(); err != nil {
				return RebuildReport{}, err
			}
		}
		return s.RebuildIndex()
	}
	var originals, copies []string
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
		registered, _ := collectRegisteredAndUserFiles(item)
		for _, source := range registered {
			if _, err := os.Lstat(source); os.IsNotExist(err) {
				continue
			}
			rel, err := filepath.Rel(item.ItemDir, source)
			if err != nil || !safeRegisteredPath(item.ItemDir, filepath.ToSlash(rel)) {
				rollback()
				return RebuildReport{}, fmt.Errorf("library: 迁移文件路径无效")
			}
			dest, err := ResolveItemPath(destDir, filepath.ToSlash(rel))
			if err != nil {
				rollback()
				return RebuildReport{}, err
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
				rollback()
				return RebuildReport{}, err
			}
			input, err := os.Open(source)
			if err != nil {
				rollback()
				return RebuildReport{}, err
			}
			output, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				input.Close()
				rollback()
				return RebuildReport{}, err
			}
			copies = append(copies, dest)
			_, copyErr := io.Copy(output, input)
			err = errors.Join(copyErr, input.Close(), output.Sync(), output.Close())
			if err != nil {
				rollback()
				return RebuildReport{}, err
			}
			originals = append(originals, source)
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
	for _, path := range originals {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return report, err
		}
	}
	for _, item := range items {
		if _, err := EnsureWithinRoot(oldDir, item.ItemDir); err == nil {
			removeEmptyDirsBottomUp(item.ItemDir)
		}
	}
	return report, nil
}
