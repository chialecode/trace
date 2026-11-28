//go:build !windows

package companion

import "os"

func openSharedReadFile(path string) (*os.File, error) {
	return os.Open(path)
}
