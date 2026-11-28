//go:build windows

package companion

import (
	"os"

	"golang.org/x/sys/windows"
)

// openSharedReadFile opens a live recording .part file read-only with
// FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE so concurrent
// recording writes, rotation, finalization (os.Rename), and cleanup (os.Remove)
// on Windows never fail with a sharing-lock violation.
func openSharedReadFile(path string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	h, err := windows.CreateFile(
		p,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}
