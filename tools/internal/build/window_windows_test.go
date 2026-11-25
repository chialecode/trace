//go:build windows

package build

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/chialecode/trace/internal/appdirs"
)

func TestInterpretEnumWindowsResult(t *testing.T) {
	cases := []struct {
		name      string
		r1        uintptr
		e1        syscall.Errno
		found     bool
		enumCount int
		wantOpen  bool
		wantErr   error
	}{
		{
			name:      "found early stop returns true even when r1=0 and e1=0",
			r1:        0,
			e1:        0,
			found:     true,
			enumCount: 3,
			wantOpen:  true,
		},
		{
			name:      "normal desktop completed without match",
			r1:        1,
			e1:        0,
			found:     false,
			enumCount: 12,
			wantOpen:  false,
		},
		{
			name:      "isolated empty desktop r1=0 e1=0 does not fabricate EINVAL",
			r1:        0,
			e1:        0,
			found:     false,
			enumCount: 0,
			wantOpen:  false,
		},
		{
			name:      "genuine Win32 failure is surfaced",
			r1:        0,
			e1:        syscall.ERROR_ACCESS_DENIED,
			found:     false,
			enumCount: 0,
			wantOpen:  false,
			wantErr:   syscall.ERROR_ACCESS_DENIED,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			open, err := interpretEnumWindowsResult(tc.r1, tc.e1, tc.found, tc.enumCount)
			if open != tc.wantOpen {
				t.Fatalf("open = %v, want %v (err=%v)", open, tc.wantOpen, err)
			}
			if tc.wantErr == nil && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestWindowOpenForDataDirUsesLiveMarkerAndSurvivesEmptyDesktop(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	pid := os.Getpid()

	if err := appdirs.WriteRuntimeWindowState(dir, pid, true); err != nil {
		t.Fatal(err)
	}
	open, err := WindowOpenForDataDir(exe, dir)
	if err != nil || !open {
		t.Fatalf("WindowOpenForDataDir(live open=true) = (%v, %v), want (true, nil)", open, err)
	}

	if err := appdirs.WriteRuntimeWindowState(dir, pid, false); err != nil {
		t.Fatal(err)
	}
	open, err = WindowOpenForDataDir(exe, dir)
	if err != nil || open {
		t.Fatalf("WindowOpenForDataDir(live open=false) = (%v, %v), want (false, nil)", open, err)
	}

	// Stale PID marker falls back to EnumWindows without returning EINVAL.
	if err := appdirs.WriteRuntimeWindowState(dir, pid+999999, true); err != nil {
		t.Fatal(err)
	}
	open, err = WindowOpenForDataDir(exe, dir)
	if err != nil || open {
		t.Fatalf("WindowOpenForDataDir(stale pid fallback) = (%v, %v), want (false, nil)", open, err)
	}

	// Corrupt marker falls back cleanly without error.
	if err := os.WriteFile(filepath.Join(dir, appdirs.RuntimeWindowFile), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	open, err = WindowOpenForDataDir(exe, dir)
	if err != nil || open {
		t.Fatalf("WindowOpenForDataDir(corrupt marker fallback) = (%v, %v), want (false, nil)", open, err)
	}
}
