//go:build windows

package companion

import "golang.org/x/sys/windows"

func diskSpace(dir string) (free, total uint64, err error) {
	ptr, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, 0, err
	}
	var avail, tot, totFree uint64
	if err := windows.GetDiskFreeSpaceEx(ptr, &avail, &tot, &totFree); err != nil {
		return 0, 0, err
	}
	return avail, tot, nil
}
