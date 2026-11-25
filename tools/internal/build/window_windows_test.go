//go:build windows

package build

import (
	"os"
	"path/filepath"
	"testing"
)

// The test binary runs no window of its own (a console belongs to the console
// host), and an executable no process runs has none.
func TestWindowOpenWithoutWindows(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, exe := range []string{self, filepath.Join(t.TempDir(), "absent.exe")} {
		open, err := WindowOpen(exe)
		if err != nil || open {
			t.Fatalf("WindowOpen(%s) = %v, %v", filepath.Base(exe), open, err)
		}
	}
	if running, err := ProcessRunning(self); err != nil || !running {
		t.Fatalf("ProcessRunning(self) = %v, %v", running, err)
	}
}
