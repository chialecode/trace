package appdirs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeWindowStateLifecycle(t *testing.T) {
	dir := t.TempDir()

	if _, ok := ReadRuntimeWindowState(dir); ok {
		t.Fatal("expected missing marker to report ok=false")
	}

	if err := WriteRuntimeWindowState(dir, 1234, true); err != nil {
		t.Fatalf("WriteRuntimeWindowState(true): %v", err)
	}
	st, ok := ReadRuntimeWindowState(dir)
	if !ok || st.PID != 1234 || !st.WindowOpen || st.UpdatedAt == "" {
		t.Fatalf("ReadRuntimeWindowState = (%+v, %v), want pid=1234 open=true", st, ok)
	}

	if err := WriteRuntimeWindowState(dir, 1234, false); err != nil {
		t.Fatalf("WriteRuntimeWindowState(false): %v", err)
	}
	st, ok = ReadRuntimeWindowState(dir)
	if !ok || st.PID != 1234 || st.WindowOpen {
		t.Fatalf("ReadRuntimeWindowState = (%+v, %v), want pid=1234 open=false", st, ok)
	}

	// Another PID must not remove this instance's marker.
	RemoveRuntimeWindowState(dir, 9999)
	if _, ok := ReadRuntimeWindowState(dir); !ok {
		t.Fatal("RemoveRuntimeWindowState removed another PID's marker")
	}

	RemoveRuntimeWindowState(dir, 1234)
	if _, ok := ReadRuntimeWindowState(dir); ok {
		t.Fatal("RemoveRuntimeWindowState failed to remove own marker")
	}

	// Corrupt or invalid PID markers are ignored safely.
	if err := os.WriteFile(filepath.Join(dir, RuntimeWindowFile), []byte("{bad json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadRuntimeWindowState(dir); ok {
		t.Fatal("corrupt marker reported ok=true")
	}
}
