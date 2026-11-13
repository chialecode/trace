//go:build windows

package service

import (
	"errors"

	"github.com/chialecode/trace/internal/update"
	"golang.org/x/sys/windows/registry"
)

const devUninstallKeyPrefix = `Software\Microsoft\Windows\CurrentVersion\Uninstall\`

// ReadSystemInstalledDevVersion reads DisplayVersion from
// HKCU\Software\Microsoft\Windows\CurrentVersion\Uninstall\io.github.chialecode.trace-dev
// in read-only mode without modifying any registry key or value.
func ReadSystemInstalledDevVersion() string {
	return readRegistryDisplayVersion(registry.CURRENT_USER, devUninstallKeyPrefix+devUninstallIdentifier)
}

func readRegistryDisplayVersion(root registry.Key, subkey string) string {
	k, err := registry.OpenKey(root, subkey, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) || err != nil {
		return ""
	}
	defer k.Close()
	ver, _, err := k.GetStringValue("DisplayVersion")
	if err != nil {
		return ""
	}
	return update.SanitizeVersion(ver)
}
