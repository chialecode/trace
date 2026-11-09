//go:build windows

package library

import "golang.org/x/sys/windows"

// longPathName expands the 8.3 short names in an existing path, such as
// RUNNER~1 in C:\Users\RUNNER~1\AppData\Local\Temp, as filepath.EvalSymlinks
// does; unlike it, it does not follow links or junctions. It returns the
// path unchanged when it cannot be expanded.
func longPathName(p string) string {
	in, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return p
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetLongPathName(in, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || int(n) > len(buf) {
		return p
	}
	return windows.UTF16ToString(buf[:n])
}
