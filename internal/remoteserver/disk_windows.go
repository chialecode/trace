//go:build windows

package remoteserver

import "golang.org/x/sys/windows"

// DiskSpace reports the free (available to this user) and total bytes of
// the volume holding dir.
func DiskSpace(dir string) Disk {
	return func() (uint64, uint64, error) {
		ptr, err := windows.UTF16PtrFromString(dir)
		if err != nil {
			return 0, 0, err
		}
		var free, total, totalFree uint64
		if err := windows.GetDiskFreeSpaceEx(ptr, &free, &total, &totalFree); err != nil {
			return 0, 0, err
		}
		return free, total, nil
	}
}
