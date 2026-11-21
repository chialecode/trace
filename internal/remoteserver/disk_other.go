//go:build !windows && !linux

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
		return uint64(st.Bavail) * uint64(st.Bsize), uint64(st.Blocks) * uint64(st.Bsize), nil
	}
}
