package shell

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"testing"
	"time"
)

func TestCrashOutputKeepsThePreviousCrashAndPrunes(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(func() { _ = debug.SetCrashOutput(nil, debug.CrashOptions{}) })

	// A clean previous run leaves an empty crash.log: nothing to keep.
	if prev := setupCrashOutput(dir); prev != "" {
		t.Fatalf("first run reported a previous crash %q", prev)
	}
	if prev := setupCrashOutput(dir); prev != "" {
		t.Fatalf("an empty crash.log was kept as %q", prev)
	}

	// Seven earlier crashes, then the last run crashed too.
	for i := range 7 {
		name := filepath.Join(dir, fmt.Sprintf("crash-2026111%dT000000Z.log", i))
		if err := os.WriteFile(name, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	unrelated := filepath.Join(dir, "trace.log")
	if err := os.WriteFile(unrelated, []byte("log"), 0o600); err != nil {
		t.Fatal(err)
	}
	crash := filepath.Join(dir, crashLogName)
	if err := os.WriteFile(crash, []byte("panic: boom\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 11, 18, 9, 30, 0, 0, time.UTC)
	if err := os.Chtimes(crash, when, when); err != nil {
		t.Fatal(err)
	}
	prev := setupCrashOutput(dir)
	if prev != "crash-20261118T093000Z.log" {
		t.Fatalf("previous crash kept as %q", prev)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, prev)); err != nil || string(raw) != "panic: boom\n" {
		t.Fatalf("kept crash file = %q, %v", raw, err)
	}
	if info, err := os.Stat(crash); err != nil || info.Size() != 0 {
		t.Fatalf("a fresh empty crash.log should be open for this run: %v %v", info, err)
	}
	kept, _ := filepath.Glob(filepath.Join(dir, crashLogGlob))
	if len(kept) != crashLogKeep {
		t.Fatalf("kept %d crash files, want %d: %v", len(kept), crashLogKeep, kept)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("unrelated log removed: %v", err)
	}
}
