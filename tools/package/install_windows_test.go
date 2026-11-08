//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The real process and registry readers only read: the test binary is
// found by its path, a path nobody runs is not, and an identity nobody
// installed has no entry.
func TestWindowsReadersOnlyRead(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if running, err := (winProcesses{}).Running(self); err != nil || !running {
		t.Fatalf("test binary not found running: %v %v", running, err)
	}
	if running, err := (winProcesses{}).Running(filepath.Join(t.TempDir(), "trace.exe")); err != nil || running {
		t.Fatalf("unused path running: %v %v", running, err)
	}
	if _, ok, err := (winRegistry{}).Lookup("io.github.chialecode.trace-test-absent"); err != nil || ok {
		t.Fatalf("absent identity: %v %v", ok, err)
	}
}
