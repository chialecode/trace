//go:build !windows

package library

// longPathName expands Windows 8.3 short names; other systems have none.
func longPathName(p string) string { return p }
