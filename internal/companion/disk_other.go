//go:build !windows && !linux

package companion

func diskSpace(dir string) (free, total uint64, err error) {
	return 0, 0, nil
}
