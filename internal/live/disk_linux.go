//go:build linux

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
	var blockSize uint64
	if stat.Frsize > 0 {
		blockSize = uint64(stat.Frsize)
	} else if stat.Bsize > 0 {
		blockSize = uint64(stat.Bsize)
	} else {
		return 0, fmt.Errorf("live: invalid statfs block size for %s", target)
	}
	return uint64(stat.Bavail) * blockSize, nil
}

func isPlatformDiskFullError(error) bool {
	return false
}
