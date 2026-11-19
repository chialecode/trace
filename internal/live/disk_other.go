//go:build !windows && !linux

package live

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func getFreeDiskBytes(dir string) (uint64, error) {
	target := nearestExistingDir(dir)
	var stat unix.Statfs_t
	if err := unix.Statfs(target, &stat); err != nil {
		return 0, err
	}
	if stat.Bsize <= 0 {
		return 0, fmt.Errorf("live: invalid statfs block size for %s", target)
	}
	return uint64(stat.Bavail) * uint64(stat.Bsize), nil
}

func isPlatformDiskFullError(error) bool {
	return false
}
