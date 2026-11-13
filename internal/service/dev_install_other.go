//go:build !windows

package service

// ReadSystemInstalledDevVersion returns "" on non-Windows platforms.
func ReadSystemInstalledDevVersion() string {
	return ""
}
