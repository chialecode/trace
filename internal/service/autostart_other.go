//go:build !windows

package service

// SystemAutostartRegistry returns nil on non-Windows platforms where Windows
// Run autostart is unsupported.
func SystemAutostartRegistry() AutostartRegistry {
	return nil
}
