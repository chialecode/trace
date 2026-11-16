package library

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ChangeCacheDir prepares a writable root and its durable ledger before the
// caller persists settings. Existing files and index rows stay at their roots.
// A crash between the two writes leaves an extra browsable root, never lost data.
func (s *Store) ChangeCacheDir(dir string, persist func() error) error {
	if strings.TrimSpace(dir) == "" || !filepath.IsAbs(dir) {
		return fmt.Errorf("library: 保存位置必须是绝对路径")
	}
	dir = filepath.Clean(dir)
	if err := rejectDirectoryLinks(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".trace-write-check-*")
	if err != nil {
		return err
	}
	name := f.Name()
	err = f.Close()
	removeErr := os.Remove(name)
	if err != nil || removeErr != nil {
		return errors.Join(err, removeErr)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	oldDir, oldRoots := s.cacheDir, append([]string(nil), s.extraRoots...)
	s.cacheDir = dir
	if oldDir != "" && !strings.EqualFold(oldDir, dir) {
		s.extraRoots = append(s.extraRoots, oldDir)
	}
	if err := s.saveRootsLocked(); err != nil {
		s.cacheDir, s.extraRoots = oldDir, oldRoots
		return err
	}
	if persist != nil {
		if err := persist(); err != nil {
			s.cacheDir, s.extraRoots = oldDir, oldRoots
			return errors.Join(err, s.saveRootsLocked())
		}
	}
	return nil
}

func rejectDirectoryLinks(dir string) error {
	for current := dir; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return fmt.Errorf("library: 保存位置含链接或非目录节点")
			}
			// EvalSymlinks also expands 8.3 short names (RUNNER~1), which
			// are not links: compare with the long form of the path.
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil || !strings.EqualFold(filepath.Clean(resolved), filepath.Clean(longPathName(current))) {
				return fmt.Errorf("library: 保存位置含目录联接")
			}
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	return nil
}

// verifiedDirTTL is how long a directory found free of links is trusted by
// the read path (safeRegisteredPath). Showing a screen of thumbnails resolves
// dozens of files under the same ancestors; walking every ancestor with
// EvalSymlinks for each file made a page take seconds (DEC-057). Changing a
// location, scanning and migrating still check in full.
const verifiedDirTTL = 3 * time.Second

var verifiedDirs sync.Map // cleaned lower-case dir -> time.Time verified

// rejectDirectoryLinksRecent is rejectDirectoryLinks that stops at the first
// ancestor verified within verifiedDirTTL (all of whose ancestors were then
// verified too) and remembers what it walked.
func rejectDirectoryLinksRecent(dir string) error {
	now := time.Now()
	var walked []string
	for current := filepath.Clean(dir); ; current = filepath.Dir(current) {
		key := strings.ToLower(current)
		if at, ok := verifiedDirs.Load(key); ok && now.Sub(at.(time.Time)) < verifiedDirTTL {
			break
		}
		info, err := os.Lstat(current)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return fmt.Errorf("library: 保存位置含链接或非目录节点")
			}
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil || !strings.EqualFold(filepath.Clean(resolved), filepath.Clean(longPathName(current))) {
				return fmt.Errorf("library: 保存位置含目录联接")
			}
			walked = append(walked, key)
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	for _, key := range walked {
		verifiedDirs.Store(key, now)
	}
	return nil
}
