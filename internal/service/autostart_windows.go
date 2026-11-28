//go:build windows

package service

import (
	"errors"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const windowsRunRegPath = `Software\Microsoft\Windows\CurrentVersion\Run`

type windowsRunRegistry struct{}

// SystemAutostartRegistry returns the per-user Windows Run registry adapter
// (HKCU\Software\Microsoft\Windows\CurrentVersion\Run). Callers must only wire
// this in interactive non-quiet desktop runs; tests and quiet runs must inject
// NewMemoryAutostartRegistry instead.
func SystemAutostartRegistry() AutostartRegistry {
	return windowsRunRegistry{}
}

func (windowsRunRegistry) Get(name string) (string, bool, error) {
	if strings.TrimSpace(name) == "" {
		return "", false, errors.New("autostart: empty registry value name")
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, windowsRunRegPath, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer k.Close()
	val, _, err := k.GetStringValue(name)
	if errors.Is(err, registry.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(val), true, nil
}

func (windowsRunRegistry) Set(name, command string) error {
	if err := validateAutostartRegInput(name, command); err != nil {
		return err
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, windowsRunRegPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(name, command)
}

func (windowsRunRegistry) Delete(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("autostart: empty registry value name")
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, windowsRunRegPath, registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer k.Close()
	err = k.DeleteValue(name)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	return err
}
