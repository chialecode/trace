package live

import (
	"os"
	"path/filepath"
	"strings"
)

// nearestExistingDir walks upward from dir until it finds an existing directory
// so disk space queries keep working after remote upload/move removes an empty itemDir
// or before cacheRoot is created.
func nearestExistingDir(dir string) string {
	clean := filepath.Clean(strings.TrimSpace(dir))
	if clean == "" || clean == "." {
		if wd, err := os.Getwd(); err == nil {
			return wd
		}
		return "."
	}
	if abs, err := filepath.Abs(clean); err == nil {
		clean = abs
	}
	cur := clean
	for {
		info, err := os.Stat(cur)
		if err == nil {
			if info.IsDir() {
				return cur
			}
			parent := filepath.Dir(cur)
			if parent == cur {
				return cur
			}
			cur = parent
			continue
		}
		if !os.IsNotExist(err) {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return cur
		}
		cur = parent
	}
}
