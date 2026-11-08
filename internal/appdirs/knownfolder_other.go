//go:build !windows

package appdirs

import (
	"os"
	"path/filepath"
)

func resolveDocumentsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Documents"), nil
}
