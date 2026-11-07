//go:build windows

package appdirs

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func resolveDocumentsDir() (string, error) {
	if p, err := windows.KnownFolderPath(windows.FOLDERID_Documents, 0); err == nil && p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Documents"), nil
}
