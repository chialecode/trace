//go:build windows

package shell

import (
	"errors"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const uninstallKeyPrefix = `Software\Microsoft\Windows\CurrentVersion\Uninstall\`

type registryUninstallEntry struct{}

func (registryUninstallEntry) Read(identifier string) (string, string, bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, uninstallKeyPrefix+identifier, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	defer k.Close()
	loc, _, err := k.GetStringValue("InstallLocation")
	if err != nil {
		return "", "", false, nil
	}
	ver, _, _ := k.GetStringValue("DisplayVersion")
	return strings.TrimSpace(loc), strings.TrimSpace(ver), true, nil
}

func (registryUninstallEntry) SetVersion(identifier, version string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, uninstallKeyPrefix+identifier, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue("DisplayVersion", version)
}

func systemUninstallEntry() uninstallEntry { return registryUninstallEntry{} }
