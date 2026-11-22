//go:build windows

package build

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestParseDesktopShellOutput(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		wantPID  uint32
		wantPPID uint32
		wantErr  bool
	}{
		{name: "normal", in: "35436 8668", wantPID: 35436, wantPPID: 8668},
		{name: "whitespace", in: "  1234\t5678\r\n", wantPID: 1234, wantPPID: 5678},
		{name: "empty", in: "", wantErr: true},
		{name: "single pid only", in: "35436", wantErr: true},
		{name: "extra field", in: "35436 8668 99", wantErr: true},
		{name: "zero pid", in: "0 8668", wantErr: true},
		{name: "zero ppid", in: "35436 0", wantErr: true},
		{name: "trailing garbage", in: "35436 8668x", wantErr: true},
		{name: "negative", in: "-1 8668", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pid, ppid, err := parseDesktopShellOutput(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseDesktopShellOutput(%q) = (%d, %d, nil), want error", tc.in, pid, ppid)
				}
				return
			}
			if err != nil || pid != tc.wantPID || ppid != tc.wantPPID {
				t.Fatalf("parseDesktopShellOutput(%q) = (%d, %d, %v), want (%d, %d, nil)", tc.in, pid, ppid, err, tc.wantPID, tc.wantPPID)
			}
		})
	}
}

func TestIsExplorerImage(t *testing.T) {
	tests := []struct {
		path       string
		systemRoot string
		want       bool
	}{
		{path: `C:\WINDOWS\Explorer.EXE`, systemRoot: `C:\Windows`, want: true},
		{path: `C:\Windows\explorer.exe`, systemRoot: `C:\Windows\`, want: true},
		{path: `C:\Temp\explorer.exe`, systemRoot: `C:\Windows`, want: false},
		{path: `C:\Windows\System32\cmd.exe`, systemRoot: `C:\Windows`, want: false},
		{path: ``, systemRoot: `C:\Windows`, want: false},
		{path: `C:\Windows\explorer.exe`, systemRoot: ``, want: false},
	}
	for _, tc := range tests {
		if got := isExplorerImage(tc.path, tc.systemRoot); got != tc.want {
			t.Errorf("isExplorerImage(%q, %q) = %v, want %v", tc.path, tc.systemRoot, got, tc.want)
		}
	}
}

func TestVerifyParentIsExplorer(t *testing.T) {
	const (
		sysRoot     = `C:\Windows`
		explorerImg = `C:\WINDOWS\Explorer.EXE`
		cmdImg      = `C:\Windows\System32\cmd.exe`
		selfPID     = 100
		explorerPID = 8668
		childPID    = 35436
	)

	t.Run("live child of explorer", func(t *testing.T) {
		ok := verifyParentIsExplorer(
			childPID, explorerPID, selfPID, sysRoot,
			func(pid uint32) (uint32, bool, error) { return explorerPID, true, nil },
			func(pid uint32) (string, error) {
				if pid != explorerPID {
					t.Fatalf("unexpected image lookup for pid %d", pid)
				}
				return explorerImg, nil
			},
		)
		if !ok {
			t.Fatal("expected true for live child of explorer.exe")
		}
	})

	t.Run("live child ppid mismatch", func(t *testing.T) {
		ok := verifyParentIsExplorer(
			childPID, explorerPID, selfPID, sysRoot,
			func(uint32) (uint32, bool, error) { return 9999, true, nil },
			func(uint32) (string, error) { return explorerImg, nil },
		)
		if ok {
			t.Fatal("expected false when live parent pid differs from reported ppid")
		}
	})

	t.Run("live child of non-explorer", func(t *testing.T) {
		ok := verifyParentIsExplorer(
			childPID, 4242, selfPID, sysRoot,
			func(uint32) (uint32, bool, error) { return 4242, true, nil },
			func(uint32) (string, error) { return cmdImg, nil },
		)
		if ok {
			t.Fatal("expected false for child of cmd.exe")
		}
	})

	t.Run("live child of self", func(t *testing.T) {
		ok := verifyParentIsExplorer(
			childPID, selfPID, selfPID, sysRoot,
			func(uint32) (uint32, bool, error) { return selfPID, true, nil },
			func(uint32) (string, error) { return explorerImg, nil },
		)
		if ok {
			t.Fatal("expected false when parent is self")
		}
	})

	t.Run("already exited child with explorer reportedPPID", func(t *testing.T) {
		ok := verifyParentIsExplorer(
			childPID, explorerPID, selfPID, sysRoot,
			func(uint32) (uint32, bool, error) { return 0, false, nil },
			func(pid uint32) (string, error) {
				if pid != explorerPID {
					t.Fatalf("unexpected image lookup for pid %d", pid)
				}
				return explorerImg, nil
			},
		)
		if !ok {
			t.Fatal("expected true when short-lived child exited but reportedPPID is live explorer.exe")
		}
	})

	t.Run("snapshot error fails closed", func(t *testing.T) {
		ok := verifyParentIsExplorer(
			childPID, explorerPID, selfPID, sysRoot,
			func(uint32) (uint32, bool, error) { return 0, false, errors.New("snapshot failed") },
			func(uint32) (string, error) { return explorerImg, nil },
		)
		if ok {
			t.Fatal("expected false on snapshot error")
		}
	})

	t.Run("image lookup error fails closed", func(t *testing.T) {
		ok := verifyParentIsExplorer(
			childPID, explorerPID, selfPID, sysRoot,
			func(uint32) (uint32, bool, error) { return explorerPID, true, nil },
			func(uint32) (string, error) { return "", errors.New("access denied") },
		)
		if ok {
			t.Fatal("expected false on image lookup error")
		}
	})
}

func TestLaunchInstalledOutsideJobDecision(t *testing.T) {
	t.Run("outside job uses LaunchDetachedOutsideJob directly", func(t *testing.T) {
		shellCalled := false
		detachedCalled := false
		inJob, err := launchInstalledOutsideJob(`C:\trace.exe`, []string{"--background"}, nil, desktopShellDeps{
			selfInJob: func() bool { return false },
			launchViaShell: func(string, []string) (uint32, uint32, error) {
				shellCalled = true
				return 0, 0, nil
			},
			parentIsExplorer: func(uint32, uint32) bool { return false },
			pidInJob:         func(uint32) bool { return false },
			launchDetached: func(string, []string, []string) (bool, error) {
				detachedCalled = true
				return false, nil
			},
		})
		if err != nil || inJob || shellCalled || !detachedCalled {
			t.Fatalf("inJob=%v err=%v shell=%v detached=%v", inJob, err, shellCalled, detachedCalled)
		}
	})

	t.Run("inside job explorer child ignores PCA system job false positive", func(t *testing.T) {
		detachedCalled := false
		inJob, err := launchInstalledOutsideJob(`C:\trace.exe`, []string{"--background"}, nil, desktopShellDeps{
			selfInJob:        func() bool { return true },
			launchViaShell:   func(string, []string) (uint32, uint32, error) { return 35436, 8668, nil },
			parentIsExplorer: func(pid, ppid uint32) bool { return pid == 35436 && ppid == 8668 },
			// Simulates IsProcessInJob(h, NULL, &in) returning true due to Windows PCA/Shell GUI job.
			pidInJob: func(uint32) bool { return true },
			launchDetached: func(string, []string, []string) (bool, error) {
				detachedCalled = true
				return true, nil
			},
		})
		if err != nil || inJob || detachedCalled {
			t.Fatalf("inJob=%v err=%v detached=%v, want inJob=false err=nil detached=false", inJob, err, detachedCalled)
		}
	})

	t.Run("inside job unverified parent falls back to pidInJob without double launch", func(t *testing.T) {
		detachedCalled := false
		inJob, err := launchInstalledOutsideJob(`C:\trace.exe`, []string{"--background"}, nil, desktopShellDeps{
			selfInJob:        func() bool { return true },
			launchViaShell:   func(string, []string) (uint32, uint32, error) { return 35436, 9999, nil },
			parentIsExplorer: func(uint32, uint32) bool { return false },
			pidInJob:         func(pid uint32) bool { return pid == 35436 },
			launchDetached: func(string, []string, []string) (bool, error) {
				detachedCalled = true
				return false, nil
			},
		})
		if err != nil || !inJob || detachedCalled {
			t.Fatalf("inJob=%v err=%v detached=%v, want inJob=true err=nil detached=false", inJob, err, detachedCalled)
		}
	})

	t.Run("inside job shell launch failure falls back to LaunchDetachedOutsideJob", func(t *testing.T) {
		detachedCalled := false
		inJob, err := launchInstalledOutsideJob(`C:\trace.exe`, []string{"--background"}, nil, desktopShellDeps{
			selfInJob:        func() bool { return true },
			launchViaShell:   func(string, []string) (uint32, uint32, error) { return 0, 0, errors.New("no desktop") },
			parentIsExplorer: func(uint32, uint32) bool { return false },
			pidInJob:         func(uint32) bool { return false },
			launchDetached: func(string, []string, []string) (bool, error) {
				detachedCalled = true
				return true, nil
			},
		})
		if err != nil || !inJob || !detachedCalled {
			t.Fatalf("inJob=%v err=%v detached=%v, want inJob=true err=nil detached=true", inJob, err, detachedCalled)
		}
	})
}

func TestProcessParentIDCurrentProcess(t *testing.T) {
	ppid, found, err := processParentID(uint32(os.Getpid()))
	if err != nil {
		t.Fatalf("processParentID(self): %v", err)
	}
	if !found || ppid == 0 {
		t.Fatalf("processParentID(self) = (%d, %v), want found with non-zero ppid", ppid, found)
	}
}

// TestLaunchInstalledOutsideJob starts a short hidden ping through
// LaunchInstalledOutsideJob, or inside a job object through the desktop shell
// as that function does first. It starts a real process, so it only runs with
// TRACE_RUN_SHELL_LAUNCH_TEST=1; run it inside a job that forbids breaking
// away and ends its processes on close to check the escape.
func TestLaunchInstalledOutsideJob(t *testing.T) {
	if os.Getenv("TRACE_RUN_SHELL_LAUNCH_TEST") != "1" {
		t.Skip("set TRACE_RUN_SHELL_LAUNCH_TEST=1 to start a process through the desktop shell")
	}
	ping := filepath.Join(os.Getenv("SystemRoot"), "System32", "PING.EXE")
	args := []string{"-n", "4", "127.0.0.1"}
	if processInJob(uint32(os.Getpid())) {
		// Exercise the same decision path as LaunchInstalledOutsideJob, but with SW_HIDE.
		var gotPID, gotPPID uint32
		inJob, err := launchInstalledOutsideJob(ping, args, os.Environ(), desktopShellDeps{
			selfInJob: func() bool { return true },
			launchViaShell: func(exe string, a []string) (uint32, uint32, error) {
				var serr error
				gotPID, gotPPID, serr = launchViaDesktopShell(exe, a, windows.SW_HIDE)
				return gotPID, gotPPID, serr
			},
			parentIsExplorer: parentIsExplorer,
			pidInJob:         processInJob,
			launchDetached:   LaunchDetachedOutsideJob,
		})
		if err != nil {
			t.Fatalf("desktop shell launch: %v", err)
		}
		if !parentIsExplorer(gotPID, gotPPID) {
			t.Fatalf("process %d (ppid %d) was not confirmed as a child of explorer.exe", gotPID, gotPPID)
		}
		if inJob {
			t.Fatalf("process %d (ppid %d) reported inJob=true despite explorer.exe parent", gotPID, gotPPID)
		}
		t.Logf("launched hidden ping pid=%d ppid=%d (explorer.exe), rawIsProcessInJob=%v, reportedInJob=%v", gotPID, gotPPID, processInJob(gotPID), inJob)
		return
	}
	inJob, err := LaunchInstalledOutsideJob(ping, args, os.Environ())
	if err != nil {
		t.Fatalf("LaunchInstalledOutsideJob: %v", err)
	}
	if inJob {
		t.Fatal("started inside a job although this test is in none")
	}
}
