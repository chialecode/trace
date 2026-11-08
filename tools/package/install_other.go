//go:build !windows

package main

func platformInstaller() (uninstallReader, processes, func(string) error, error) {
	return nil, nil, nil, errUnsupported
}
