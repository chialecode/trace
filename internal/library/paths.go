package library

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// samePath compares two cleaned filesystem paths using the OS's case sensitivity.
func samePath(a, b string) bool {
	ca, cb := filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(ca, cb)
	}
	return ca == cb
}

// pathKey returns a canonical map key for a filesystem path.
func pathKey(p string) string {
	c := filepath.Clean(p)
	if runtime.GOOS == "windows" {
		return strings.ToLower(c)
	}
	return c
}

// IsLinkLike reports symlinks and other reparse points. Since Go 1.23 a
// Windows directory junction reports ModeIrregular instead of ModeSymlink.
func IsLinkLike(mode os.FileMode) bool {
	return mode&(os.ModeSymlink|os.ModeIrregular) != 0
}

func isLinkLike(mode os.FileMode) bool {
	return IsLinkLike(mode)
}

// verifiedDirTTL is how long an ancestor directory found free of links is
// trusted by the read path (safeRegisteredPathRead). Showing a screen of
// thumbnails resolves dozens of files under the same ancestors; walking every
// ancestor with EvalSymlinks for each file made a page take seconds (DEC-057).
// The item directory itself, the leaf file, and all write/delete/migration/scan
// paths always check in full without using this cache (data-and-cache.md §3).
const verifiedDirTTL = 3 * time.Second

var verifiedDirs sync.Map // pathKey(dir) -> time.Time verified

func checkSingleDirLink(current string) (exists bool, err error) {
	info, err := os.Lstat(current)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if isLinkLike(info.Mode()) || !info.IsDir() {
		return true, fmt.Errorf("library: 保存位置含链接或非目录节点")
	}
	// EvalSymlinks also expands 8.3 short names (RUNNER~1), which are not links:
	// compare with the long form of the path.
	resolved, err := filepath.EvalSymlinks(current)
	if err != nil || !samePath(resolved, longPathName(current)) {
		return true, fmt.Errorf("library: 保存位置含目录联接")
	}
	return true, nil
}

func checkDirectoryLinks(dir string, useRecentCache bool) error {
	now := time.Now()
	var walked []string
	for current := filepath.Clean(dir); ; current = filepath.Dir(current) {
		key := pathKey(current)
		if useRecentCache {
			if at, ok := verifiedDirs.Load(key); ok && now.Sub(at.(time.Time)) < verifiedDirTTL {
				break
			}
		}
		exists, err := checkSingleDirLink(current)
		if err != nil {
			return err
		}
		if exists && useRecentCache {
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

// RejectDirectoryLinks verifies that dir and all of its existing ancestors are
// real directories (no symlinks or Windows directory junctions).
func RejectDirectoryLinks(dir string) error {
	return checkDirectoryLinks(dir, false)
}

func rejectDirectoryLinks(dir string) error {
	return checkDirectoryLinks(dir, false)
}

func rejectDirectoryLinksRecent(dir string) error {
	return checkDirectoryLinks(dir, true)
}

func safeRegisteredPathChecked(root, rel string, allowAncestorCache bool) bool {
	root = filepath.Clean(root)
	if allowAncestorCache {
		// Cache only ancestors strictly above root; always check root itself
		// and every relative segment fresh (data-and-cache.md §3).
		parent := filepath.Dir(root)
		if parent != root {
			if rejectDirectoryLinksRecent(parent) != nil {
				return false
			}
		}
		if exists, err := checkSingleDirLink(root); err != nil || !exists {
			return false
		}
	} else {
		if rejectDirectoryLinks(root) != nil {
			return false
		}
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

// safeRegisteredPathStrict checks root, all ancestors, and all segments without
// any TTL cache. Used for all write, delete, migration, and transfer paths.
func safeRegisteredPathStrict(root, rel string) bool {
	return safeRegisteredPathChecked(root, rel, false)
}

// safeRegisteredPathRead caches ancestor directories strictly above root for
// verifiedDirTTL while always checking root and every relative segment fresh.
func safeRegisteredPathRead(root, rel string) bool {
	return safeRegisteredPathChecked(root, rel, true)
}

// ResolveItemPath validates an item-relative path strictly (no ancestor cache)
// before a registered file is written, renamed, or removed (INV-08). The final
// file may be absent, but any symlink or junction is rejected.
func ResolveItemPath(root, rel string) (string, error) {
	if !safeRegisteredPathStrict(root, rel) {
		return "", fmt.Errorf("library: unsafe item-relative path")
	}
	return EnsureWithinRoot(root, filepath.Join(root, filepath.FromSlash(rel)))
}

func resolveItemPathRead(root, rel string) (string, error) {
	if !safeRegisteredPathRead(root, rel) {
		return "", fmt.Errorf("library: unsafe item-relative path")
	}
	return EnsureWithinRoot(root, filepath.Join(root, filepath.FromSlash(rel)))
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

	if !safeRegisteredPathRead(meta.ItemDir, relPath) {
		return "", fmt.Errorf("library: media path contains a link or invalid segment")
	}

	// Check Lstat on meta.ItemDir and fullPath and EvalSymlinks to reject symlinks/junctions.
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
