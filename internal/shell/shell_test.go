package shell

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/ui"
	"github.com/egoist/mygo"
)

type trayHelperResult struct {
	ReadyWindowOpen       bool                 `json:"readyWindowOpen"`
	TrayWindowOpen        bool                 `json:"trayWindowOpen"`
	RestoredWindowOpen    bool                 `json:"restoredWindowOpen"`
	TraySnapshot          RuntimeStateSnapshot `json:"traySnapshot"`
	RecreateLatenciesMs   []int64              `json:"recreateLatenciesMs"`
	StealFocusDetectedAny bool                 `json:"stealFocusDetectedAny"`
}

func TestMain(m *testing.M) {
	if outPath := os.Getenv("TRACE_SHELL_HELPER_OUT"); outPath != "" {
		if err := runRealHiddenWindowHelper(outPath); err != nil {
			fmt.Fprintf(os.Stderr, "real window helper failed: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runRealHiddenWindowHelper(outPath string) error {
	_ = os.Setenv("TRACE_QUIET", "1")
	_ = SetBelowNormalPriority()

	root, err := os.MkdirTemp("", "trace-tray-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)

	loc := appdirs.Locations{
		Channel:         "dev",
		Mode:            "dev",
		AppName:         "trace-dev",
		DisplayName:     "TRACE Dev",
		DataDir:         filepath.Join(root, "trace-dev"),
		DefaultCacheDir: filepath.Join(root, "cache"),
		CredentialsDir:  filepath.Join(root, "trace-dev", "credentials"),
		LogsDir:         filepath.Join(root, "trace-dev", "logs"),
		LockFile:        filepath.Join(root, "trace-dev", "app.lock"),
	}
	if err := appdirs.EnsureDirectories(loc); err != nil {
		return err
	}

	mygo.App.SetName(loc.AppName)
	mygo.App.SetVersion("0.1.0")
	mygoDir := filepath.Join(loc.DataDir, "mygo")
	cacheSubDir := filepath.Join(loc.DataDir, "cache")
	_ = os.MkdirAll(mygoDir, 0o700)
	_ = os.MkdirAll(cacheSubDir, 0o700)
	mygo.App.SetPath(mygo.PathUserData, mygoDir)
	mygo.App.SetPath(mygo.PathCache, cacheSubDir)
	mygo.App.SetPath(mygo.PathLogs, loc.LogsDir)

	b, err := service.NewBackend(service.Options{
		Locations:        loc,
		Desktop:          service.NewMockDesktop(),
		DisableBgWorkers: true,
	})
	if err != nil {
		return err
	}
	defer b.Close()

	sh := NewShell(loc, b)
	mygo.App.OnWindowAllClosed(func() {})

	var helperErr error
	mygo.App.WhenReady(func() {
		sh.setupTray()
		sh.openOrRestoreMainWindow()
		if sh.uiApp != nil {
			sh.uiApp.PopulateSyntheticFeed(2000)
			sh.uiApp.Navigate(ui.RouteSettings)
		}

		go func() {
			time.Sleep(60 * time.Millisecond)
			var res trayHelperResult
			done := make(chan struct{})
			mygo.RunOnMain(func() {
				readySnap := sh.Snapshot()
				res.ReadyWindowOpen = readySnap.WindowOpen
				if readySnap.StealFocusDetected {
					res.StealFocusDetectedAny = true
				}
				sh.CloseToTray()
				close(done)
			})
			<-done

			// Verify background events do NOT block when real UI window is destroyed
			for i := 0; i < 20; i++ {
				_, _ = b.RebuildLibraryIndex()
			}

			runtime.GC()
			debug.FreeOSMemory()
			TrimWorkingSet()
			time.Sleep(40 * time.Millisecond)

			traySnap := sh.Snapshot()
			res.TrayWindowOpen = traySnap.WindowOpen
			res.TraySnapshot = traySnap
			if traySnap.StealFocusDetected {
				res.StealFocusDetectedAny = true
			}

			// Reopen real hidden MyGo window 5 times and measure recreate latency < 2s (PR-4, C35)
			for i := 0; i < 5; i++ {
				t0 := time.Now()
				stepDone := make(chan struct{})
				mygo.RunOnMain(func() {
					sh.RestoreFromTray()
					if sh.uiApp != nil {
						sh.uiApp.PopulateSyntheticFeed(500)
					}
					rSnap := sh.Snapshot()
					res.RestoredWindowOpen = rSnap.WindowOpen
					if rSnap.StealFocusDetected {
						res.StealFocusDetectedAny = true
					}
					if sh.uiApp == nil || sh.uiApp.CurrentRoute != ui.RouteSettings {
						helperErr = fmt.Errorf("recreate #%d: expected route %s", i+1, ui.RouteSettings)
					}
					close(stepDone)
				})
				<-stepDone
				dur := time.Since(t0)
				res.RecreateLatenciesMs = append(res.RecreateLatenciesMs, dur.Milliseconds())

				closeDone := make(chan struct{})
				mygo.RunOnMain(func() {
					sh.CloseToTray()
					close(closeDone)
				})
				<-closeDone
			}

			// CloseToTray already collects and trims; measure the settled state
			// rather than the instant after a forced trim.
			time.Sleep(2 * time.Second)
			res.TraySnapshot = sh.Snapshot()

			raw, _ := json.MarshalIndent(res, "", "  ")
			_ = os.WriteFile(outPath, raw, 0o600)

			mygo.RunOnMain(func() {
				sh.RequestQuit(true)
			})
		}()
	})

	if err := mygo.App.Run(); err != nil {
		return err
	}
	return helperErr
}

// TestQuietModeTrayIconCloseToTrayAndMemory verifies C35, C36, and PR-4(1):
//   - QuietMode resolution on dev/source vs release/stable (release/stable ignores TRACE_QUIET)
//   - Tray icon PNG rendering (with and without active work indicator dot)
//   - Real MyGo hidden window CloseToTray & RestoreFromTray lifecycle: real window & bitmaps destroyed,
//     background events continue without blocking, GC + FreeOSMemory keeps working set < 60 MB,
//     0 msedgewebview2.exe processes, GetForegroundWindow never becomes a TRACE window in quiet mode,
//     and route is restored across 5 real window recreations.
func TestQuietModeTrayIconCloseToTrayAndMemory(t *testing.T) {
	t.Setenv("TRACE_QUIET", "1")
	if got := QuietMode("dev"); got != "hidden" {
		t.Fatalf("expected QuietMode(dev)=hidden, got %q", got)
	}
	if got := QuietMode("release"); got != "" {
		t.Fatalf("expected QuietMode(release)='', got %q", got)
	}
	if got := QuietMode("stable"); got != "" {
		t.Fatalf("expected QuietMode(stable)='', got %q", got)
	}
	t.Setenv("TRACE_QUIET", "inactive")
	if got := QuietMode("dev"); got != "inactive" {
		t.Fatalf("expected QuietMode(dev)=inactive, got %q", got)
	}
	t.Setenv("TRACE_QUIET", "1")

	// Verify Tray PNG generation
	idlePNG := RenderTrayIconPNG(false)
	activePNG := RenderTrayIconPNG(true)
	if bytes.Equal(idlePNG, activePNG) {
		t.Fatalf("expected active tray icon PNG to differ from idle tray icon PNG")
	}
	if img, err := png.Decode(bytes.NewReader(activePNG)); err != nil || img.Bounds().Dx() != 32 {
		t.Fatalf("expected valid 32x32 tray PNG, got err=%v", err)
	}

	// Run real hidden MyGo window helper in an isolated subprocess. It needs
	// a desktop: Linux CI has no display, so the window part is not run
	// there (Windows runs it locally).
	if runtime.GOOS != "windows" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		t.Skip("no display: the real tray window helper is not run")
	}
	tmpDir := t.TempDir()
	outPath := filepath.Join(tmpDir, "tray_helper.json")
	tempSockDir := filepath.Join(tmpDir, "temp")
	_ = os.MkdirAll(tempSockDir, 0o700)

	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(),
		"TRACE_QUIET=1",
		"TRACE_SHELL_HELPER_OUT="+outPath,
		"TEMP="+tempSockDir,
		"TMP="+tempSockDir,
	)
	appdirs.SetSubprocessQuiet(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("real window helper failed: %v\n%s", err, string(out))
	}

	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read helper output: %v", err)
	}
	var res trayHelperResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("unmarshal helper output: %v", err)
	}

	if !res.ReadyWindowOpen {
		t.Fatalf("expected ReadyWindowOpen=true (real MyGo window created)")
	}
	if res.TrayWindowOpen {
		t.Fatalf("expected TrayWindowOpen=false after CloseToTray")
	}
	if !res.RestoredWindowOpen {
		t.Fatalf("expected RestoredWindowOpen=true after RestoreFromTray")
	}
	snap := res.TraySnapshot
	if snap.ActiveRoute != ui.RouteSettings {
		t.Fatalf("expected ActiveRoute=%s to be preserved in tray, got %s", ui.RouteSettings, snap.ActiveRoute)
	}
	if snap.WebView2ProcessCount != 0 {
		t.Fatalf("expected 0 msedgewebview2.exe processes, got %d", snap.WebView2ProcessCount)
	}
	const maxTrayBytes = 60 * 1024 * 1024
	if snap.PrivateWorkingSetBytes <= 0 || snap.PrivateWorkingSetBytes > maxTrayBytes {
		t.Fatalf("expected tray private working set in (0, 60 MiB), got %d (commit %d)", snap.PrivateWorkingSetBytes, snap.PrivateBytes)
	}
	if res.StealFocusDetectedAny || snap.StealFocusDetected {
		t.Fatalf("expected StealFocusDetected=false in quiet mode")
	}
	if len(res.RecreateLatenciesMs) != 5 {
		t.Fatalf("expected 5 recreate latencies, got %v", res.RecreateLatenciesMs)
	}
	for i, ms := range res.RecreateLatenciesMs {
		if ms >= 2000 {
			t.Fatalf("recreate #%d took %dms, want < 2000ms", i+1, ms)
		}
	}
}
