//go:build linux

package companion

import "golang.org/x/sys/unix"

func diskSpace(dir string) (free, total uint64, err error) {
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
