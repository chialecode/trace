//go:build linux

package remoteserver

import "golang.org/x/sys/unix"

// DiskSpace reports the free (available to this user) and total bytes of
// the file system holding dir.
func DiskSpace(dir string) Disk {
	return func() (uint64, uint64, error) {
		var st unix.Statfs_t
		if err := unix.Statfs(dir, &st); err != nil {
			return 0, 0, err
		}
		unit := uint64(st.Frsize)
		if unit == 0 {
			unit = uint64(st.Bsize)
		}
		return uint64(st.Bavail) * unit, uint64(st.Blocks) * unit, nil
	}
}
