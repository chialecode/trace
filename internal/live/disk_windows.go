package live

import (
	"golang.org/x/sys/windows"
	"path/filepath"
)

func getFreeDiskBytes(dir string) (uint64, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	ptr, err := windows.UTF16PtrFromString(abs)
	if err != nil {
		return 0, err
	}
	var freeBytesAvailable, totalBytes, totalFreeBytes uint64
	if err := windows.GetDiskFreeSpaceEx(ptr, &freeBytesAvailable, &totalBytes, &totalFreeBytes); err != nil {
		return 0, err
	}
	return freeBytesAvailable, nil
}
