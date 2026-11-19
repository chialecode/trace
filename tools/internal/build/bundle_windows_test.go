//go:build windows

package build

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
			err = PublishBundle(src, dest, names, prune)
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

func TestRunQuietContextTimesOut(t *testing.T) {
	if os.Getenv("TRACE_TEST_HANG_HELPER") == "1" {
		time.Sleep(10 * time.Second)
		return
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = RunQuietContext(ctx, self, []string{"-test.run=^TestRunQuietContextTimesOut$"}, append(os.Environ(), "TRACE_TEST_HANG_HELPER=1"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("RunQuietContext took too long to cancel: %s", time.Since(start))
	}
}
