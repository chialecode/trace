package shell

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/service"
	"github.com/egoist/mygo"
)

func newTestShellBackend(t *testing.T, channel string) (appdirs.Locations, *service.Backend) {
	t.Helper()
	root := t.TempDir()
	loc := appdirs.Locations{
		Channel:         channel,
		Mode:            "dev",
		AppName:         "trace-dev",
		DisplayName:     "TRACE Dev",
		DataDir:         filepath.Join(root, "data"),
		DefaultCacheDir: filepath.Join(root, "cache"),
		CredentialsDir:  filepath.Join(root, "data", "credentials"),
		LogsDir:         filepath.Join(root, "data", "logs"),
		LockFile:        filepath.Join(root, "data", "app.lock"),
	}
	b, err := service.NewBackend(service.Options{
		Locations:        loc,
		Desktop:          service.NewMockDesktop(),
		DisableBgWorkers: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return loc, b
}

func TestTrayNotifiedPersistsAcrossShellRestarts(t *testing.T) {
	loc, b := newTestShellBackend(t, "dev")

	s1 := NewShell(loc, b)
	if !s1.persistCloseToTrayState("settings", 1200, 800, true, true) {
		t.Fatal("first interactive CloseToTray should request tray notification")
	}
	if s1.persistCloseToTrayState("settings", 1200, 800, true, true) {
		t.Fatal("second CloseToTray in same session must not re-notify")
	}
	st := b.GetUIState()
	if !st.TrayNotified || st.Window.Width != 1200 || st.Window.Height != 800 || st.LastRoute != "settings" {
		t.Fatalf("unexpected persisted ui-state: %+v", st)
	}

	// Simulate app restart with a new Shell instance reading the same ui-state.json.
	s2 := NewShell(loc, b)
	if s2.persistCloseToTrayState("home", 1280, 860, true, true) {
		t.Fatal("TrayNotified must persist across Shell restarts")
	}
}

func TestSmokeDirDisabledOnReleaseChannels(t *testing.T) {
	t.Setenv("TRACE_SMOKE_DIR", filepath.Join(t.TempDir(), "smoke"))
	for _, ch := range []string{"release", "stable", "RELEASE", "Stable"} {
		if got := smokeDirForChannel(ch); got != "" {
			t.Fatalf("smokeDirForChannel(%q) = %q, want empty on release channel", ch, got)
		}
	}
	for _, ch := range []string{"dev", "source", "ptest"} {
		if got := smokeDirForChannel(ch); got == "" {
			t.Fatalf("smokeDirForChannel(%q) = empty, want smoke dir on non-release channel", ch)
		}
	}
}

func TestTrayRefreshStrictSerializationAndStopDrain(t *testing.T) {
	var inFlight atomic.Int32
	var maxConcurrent atomic.Int32
	var calls atomic.Int32
	gate := make(chan struct{})
	started := make(chan struct{}, 4)

	s := &Shell{}
	s.trayRefresh = func() {
		cur := inFlight.Add(1)
		for {
			prev := maxConcurrent.Load()
			if cur <= prev || maxConcurrent.CompareAndSwap(prev, cur) {
				break
			}
		}
		calls.Add(1)
		started <- struct{}{}
		<-gate
		inFlight.Add(-1)
	}

	s.scheduleTrayRefresh()
	<-started
	for range 10 {
		s.scheduleTrayRefresh()
	}

	// Stopping while an in-flight refresh runs must drop pending dirty work and wait for exit.
	stopped := make(chan struct{})
	go func() {
		s.stopTrayRefresh()
		close(stopped)
	}()

	select {
	case <-stopped:
		t.Fatal("stopTrayRefresh returned before in-flight refresh completed")
	case <-time.After(30 * time.Millisecond):
	}
	close(gate)

	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("stopTrayRefresh did not return after in-flight refresh finished")
	}

	// Scheduling after stop must be a no-op.
	s.scheduleTrayRefresh()
	time.Sleep(30 * time.Millisecond)

	if maxConcurrent.Load() != 1 {
		t.Fatalf("expected max concurrent tray refreshes = 1, got %d", maxConcurrent.Load())
	}
	if calls.Load() != 1 {
		t.Fatalf("expected stopped tray to drop queued dirty refresh and run once, got %d", calls.Load())
	}
}

func TestTeardownIsIdempotentAndBlocksWindowRestoreWhileQuitting(t *testing.T) {
	loc, b := newTestShellBackend(t, "dev")
	s := NewShell(loc, b)

	winOpen, already := s.teardown()
	if winOpen || already {
		t.Fatalf("first teardown = (%v, %v), want (false, false)", winOpen, already)
	}
	winOpen2, already2 := s.teardown()
	if winOpen2 || !already2 {
		t.Fatalf("second teardown = (%v, %v), want (false, true)", winOpen2, already2)
	}
	// Calling openOrRestoreMainWindow or HandleSecondInstance after teardown must be a safe no-op.
	s.openOrRestoreMainWindow()
	s.HandleSecondInstance([]string{"https://www.bilibili.com/video/BV1TEST0001"})
	if s.win != nil {
		t.Fatal("window must not reopen once Shell is quitting")
	}
}

func TestSyncUninstallVersionRejectsEmptyLocationOrInstallDir(t *testing.T) {
	const id = "io.github.chialecode.trace"
	entries := fakeUninstallEntries{id: {"", "0.1.1"}}
	if wrote, err := syncUninstallVersion(entries, id, "", "0.1.2"); wrote || err != nil {
		t.Fatalf("empty location and installDir should not match via filepath.Clean('.'): wrote=%v err=%v", wrote, err)
	}
	if wrote, err := syncUninstallVersion(entries, id, `""`, "0.1.2"); wrote || err != nil {
		t.Fatalf("quoted empty installDir should not match: wrote=%v err=%v", wrote, err)
	}
	entries[id] = [2]string{`""`, "0.1.1"}
	if wrote, err := syncUninstallVersion(entries, id, ".", "0.1.2"); wrote || err != nil {
		t.Fatalf("empty quoted location must not match '.': wrote=%v err=%v", wrote, err)
	}
}

func TestCrashOutputPreservesExistingWhenBackupNameCollides(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(func() { _ = debug.SetCrashOutput(nil, debug.CrashOptions{}) })

	when := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	stamp := when.UTC().Format(crashLogStamp)
	firstBackup := filepath.Join(dir, "crash-"+stamp+".log")
	if err := os.WriteFile(firstBackup, []byte("older crash\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	crashPath := filepath.Join(dir, crashLogName)
	if err := os.WriteFile(crashPath, []byte("newer crash\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(crashPath, when, when); err != nil {
		t.Fatal(err)
	}

	prev := setupCrashOutput(dir)
	if prev != "crash-"+stamp+"-1.log" {
		t.Fatalf("expected suffixed backup name on collision, got %q", prev)
	}
	if raw, err := os.ReadFile(firstBackup); err != nil || string(raw) != "older crash\n" {
		t.Fatalf("existing crash backup was overwritten: %q %v", raw, err)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, prev)); err != nil || string(raw) != "newer crash\n" {
		t.Fatalf("rotated crash content = %q, %v", raw, err)
	}
}

func TestRenderTrayIconPNGCachingReturnsIndependentCopies(t *testing.T) {
	idle1 := RenderTrayIconPNG(false, false)
	idle2 := RenderTrayIconPNG(false, false)
	if !bytes.Equal(idle1, idle2) {
		t.Fatal("cached idle tray icons differed")
	}
	idle1[0] ^= 0xff
	if bytes.Equal(idle1, RenderTrayIconPNG(false, false)) {
		t.Fatal("mutating returned slice corrupted cached tray icon PNG")
	}
}

func TestStopTrayRefreshDoesNotDeadlockWhenMainThreadIsUnpumped(t *testing.T) {
	loc, b := newTestShellBackend(t, "dev")
	s := NewShell(loc, b)
	s.tray = &mygo.Tray{}

	dispatched := make(chan struct{}, 1)
	unblockMain := make(chan struct{})
	applyDone := make(chan struct{})
	s.trayDispatchMain = func(fn func()) {
		dispatched <- struct{}{}
		<-unblockMain
		fn()
		close(applyDone)
	}

	s.scheduleTrayRefresh()
	select {
	case <-dispatched:
	case <-time.After(2 * time.Second):
		t.Fatal("refreshTrayState did not dispatch main-thread tray apply")
	}

	// Simulate OnQuit running on the main thread before MyGo's loop.shutdown()
	// unblocks RunOnMain: stopTrayRefresh() must return promptly without waiting
	// for the stalled main-thread apply.
	stopped := make(chan struct{})
	go func() {
		s.stopTrayRefresh()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("stopTrayRefresh deadlocked waiting for unpumped main-thread tray apply")
	}

	// Once teardown clears tray and sets quitting, a late main-thread apply is a safe no-op.
	s.mu.Lock()
	s.tray = nil
	s.quitting = true
	s.mu.Unlock()
	close(unblockMain)

	select {
	case <-applyDone:
	case <-time.After(2 * time.Second):
		t.Fatal("late applyTrayStateOnMain did not complete after unblock")
	}
	s.mu.Lock()
	queued, dirty := s.trayApplyQueued, s.trayWantDirty
	s.mu.Unlock()
	if queued || dirty {
		t.Fatalf("expected trayApplyQueued=false, trayWantDirty=false after quit apply, got queued=%v dirty=%v", queued, dirty)
	}
}
