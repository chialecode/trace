//go:build windows

package main

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestLockedBundleFileRestoresPreviousPackage(t *testing.T) {
	for _, name := range []string{"trace.exe", "VERSION.txt"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dest, src := filepath.Join(root, "dest"), filepath.Join(root, "src")
			want, _ := writeTestBundle(t, dest, "old")
			_, names := writeTestBundle(t, src, "new")
			prune := name == "trace.exe"
			if !prune {
				names = []string{"VERSION.txt", "SHA256SUMS.txt"}
			}
			path, err := windows.UTF16PtrFromString(filepath.Join(dest, name))
			if err != nil {
				t.Fatal(err)
			}
			// Deny deletion/rename while allowing reads, like a file in use.
			handle, err := windows.CreateFile(path, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = windows.CloseHandle(handle) })
			err = publishBundle(src, dest, names, prune)
			if err == nil || !strings.Contains(err.Error(), "previous package restored") {
				t.Fatalf("locked file did not trigger a complete rollback: %v", err)
			}
			assertTestBundle(t, dest, want)
			if backups, err := filepath.Glob(filepath.Join(root, ".backup-*")); err != nil || len(backups) != 0 {
				t.Fatalf("a successful rollback retained unnecessary backups: %v err=%v", backups, err)
			}
		})
	}
}
