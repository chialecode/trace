//go:build windows

package live

import (
	"errors"

	"golang.org/x/sys/windows"
)

func getFreeDiskBytes(dir string) (uint64, error) {
	target := nearestExistingDir(dir)
	ptr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return 0, err
	}
	var freeBytesAvailable, totalBytes, totalFreeBytes uint64
	if err := windows.GetDiskFreeSpaceEx(ptr, &freeBytesAvailable, &totalBytes, &totalFreeBytes); err != nil {
		return 0, err
	}
	return freeBytesAvailable, nil
}

func isPlatformDiskFullError(err error) bool {
	return errors.Is(err, windows.ERROR_DISK_FULL) || errors.Is(err, windows.ERROR_HANDLE_DISK_FULL)
}
