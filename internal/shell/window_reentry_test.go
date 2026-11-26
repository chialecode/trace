package shell

import (
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/uistate"
	"github.com/egoist/mygo"
)

func suppressTestTrayNotification(t *testing.T, s *Shell) {
	t.Helper()
	if _, err := s.backend.MutateUIState(func(st *uistate.State) {
		st.TrayNotified = true
	}); err != nil {
		t.Fatal(err)
	}
}

type fakeShellWindow struct {
	opts            mygo.WindowOptions
	themeCalls      int
	showCalls       int
	showInactCalls  int
	hideCalls       int
	restoreCalls    int
	focusCalls      int
	updateCalls     int
	closeCalls      int
	destroyCalls    int
	onCloseHandlers []func(*mygo.CloseEvent)

	onSetTheme func()
	onShow     func()
	onRestore  func()
	onClose    func()
	events     []string
}

func (w *fakeShellWindow) SetWindowButtonsTheme(mygo.ThemeSource) {
	w.themeCalls++
	w.events = append(w.events, "theme")
	if w.onSetTheme != nil {
		w.onSetTheme()
	}
}
func (w *fakeShellWindow) SetFullScreen(bool) {}
func (w *fakeShellWindow) Restore() {
	w.restoreCalls++
	w.events = append(w.events, "restore")
	if w.onRestore != nil {
		w.onRestore()
	}
}
func (w *fakeShellWindow) Show() {
	w.showCalls++
	w.events = append(w.events, "show")
	if w.onShow != nil {
		w.onShow()
	}
}
func (w *fakeShellWindow) ShowInactive() {
	w.showInactCalls++
	w.events = append(w.events, "show_inactive")
	if w.onShow != nil {
		w.onShow()
	}
}
func (w *fakeShellWindow) Hide() {
	w.hideCalls++
	w.events = append(w.events, "hide")
}
func (w *fakeShellWindow) Focus() {
	w.focusCalls++
	w.events = append(w.events, "focus")
}
func (w *fakeShellWindow) Update(fn func()) {
	w.updateCalls++
	if fn != nil {
		fn()
	}
}
func (w *fakeShellWindow) OnClose(fn func(*mygo.CloseEvent)) func() {
	w.events = append(w.events, "on_close")
	w.onCloseHandlers = append(w.onCloseHandlers, fn)
	return func() {}
}
func (w *fakeShellWindow) Close() {
	w.closeCalls++
	w.events = append(w.events, "close")
	if w.onClose != nil {
		w.onClose()
	}
}
func (w *fakeShellWindow) Destroy() {
	w.destroyCalls++
	w.events = append(w.events, "destroy")
}
func (w *fakeShellWindow) IsMinimized() bool       { return false }
func (w *fakeShellWindow) IsFullScreen() bool      { return false }
func (w *fakeShellWindow) Size() (int, int)        { return 1280, 860 }
func (w *fakeShellWindow) rawWindow() *mygo.Window { return nil }

func runBounded(t *testing.T, label string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s timed out (likely main-thread self-deadlock)", label)
	}
}

// TestOpenOrRestoreMainWindowReentrantTrayClickDoesNotDeadlock reproduces the
// live Windows UI-thread self-deadlock where ShowWindow -> WM_GETOBJECT ->
// UiaReturnRawElementProvider pumps a nested COM STA message loop that
// re-entrantly dispatches a second tray click or RunOnMain callback while the
// first openOrRestoreMainWindow is still running.
func TestOpenOrRestoreMainWindowReentrantTrayClickDoesNotDeadlock(t *testing.T) {
	loc, b := newTestShellBackend(t, "dev")
	s := NewShell(loc, b)
	s.quiet = "" // interactive mode
	suppressTestTrayNotification(t, s)

	var created []*fakeShellWindow
	var showSawPublishedWin bool
	var showSawEvents []string
	s.newMainWindow = func(opts mygo.WindowOptions) shellWindow {
		fw := &fakeShellWindow{opts: opts}
		created = append(created, fw)
		// Simulate Win32 re-entrancy during SetWindowButtonsTheme (pre-publish).
		fw.onSetTheme = func() {
			s.handleTrayClick()
			s.applyTrayStateOnMain()
			s.redraw()
		}
		// Simulate COM STA modal message pump during ShowWindow (post-publish).
		fw.onShow = func() {
			s.mu.Lock()
			showSawPublishedWin = (s.win == fw && s.uiApp != nil)
			s.mu.Unlock()
			showSawEvents = append([]string(nil), fw.events...)
			// Verify BindEventLoop is already wired before Show() completes:
			// a backend settings event must dispatch through fw.Update.
			beforeUpdates := fw.updateCalls
			cur := b.GetSettings()
			_, _ = b.UpdateSettings(cur)
			if fw.updateCalls <= beforeUpdates {
				t.Errorf("expected BindEventLoop to dispatch backend event via win.Update before Show() returns")
			}
			s.handleTrayClick()
			s.RestoreFromTray()
			s.applyTrayStateOnMain()
			s.redraw()
			s.HandleSecondInstance([]string{"https://www.bilibili.com/video/BV1TEST0001"})
		}
		return fw
	}

	runBounded(t, "re-entrant RestoreFromTray", func() {
		s.handleTrayClick()
	})

	if len(created) != 1 {
		t.Fatalf("expected exactly 1 window created despite re-entrant tray clicks, got %d", len(created))
	}
	fw := created[0]
	if !fw.opts.Hidden {
		t.Fatal("expected NewWindow to be created with Hidden: true before Show")
	}
	if !showSawPublishedWin {
		t.Fatal("expected s.win and s.uiApp to be published before Show()")
	}
	// Verify SetWindowButtonsTheme and OnClose ran before Show().
	if len(showSawEvents) < 3 || showSawEvents[0] != "theme" || showSawEvents[1] != "on_close" || showSawEvents[2] != "show" {
		t.Fatalf("unexpected window lifecycle order at Show(): %v", showSawEvents)
	}
	if fw.showCalls != 1 {
		t.Fatalf("expected Show() called once, got %d", fw.showCalls)
	}
	st, ok := appdirs.ReadRuntimeWindowState(loc.DataDir)
	if !ok || !st.WindowOpen || st.PID != os.Getpid() {
		t.Fatalf("unexpected runtime-window.json after open: ok=%v st=%+v", ok, st)
	}

	// Re-entrancy on the reuse-window path (w.Restore() -> nested tray click)
	// must also return without deadlocking or recursing.
	fw.onShow = nil
	fw.onRestore = func() {
		s.handleTrayClick()
	}
	runBounded(t, "re-entrant reuse_window", func() {
		s.handleTrayClick()
	})
	if fw.restoreCalls != 1 || fw.showCalls != 2 || fw.focusCalls != 1 {
		t.Fatalf("unexpected reuse calls: restore=%d show=%d focus=%d", fw.restoreCalls, fw.showCalls, fw.focusCalls)
	}

	// Cleanup UI app cleanly.
	s.CloseToTray()
}

// TestOpenOrRestoreMainWindowTeardownDuringCreateCleansUpWindow ensures that if
// teardown() runs re-entrantly while a window is being constructed, the new
// window is destroyed instead of leaked and runtime-window.json stays closed.
func TestOpenOrRestoreMainWindowTeardownDuringCreateCleansUpWindow(t *testing.T) {
	loc, b := newTestShellBackend(t, "dev")
	s := NewShell(loc, b)
	s.quiet = ""

	var fw *fakeShellWindow
	s.newMainWindow = func(opts mygo.WindowOptions) shellWindow {
		fw = &fakeShellWindow{opts: opts}
		fw.onSetTheme = func() {
			s.teardown()
		}
		return fw
	}

	runBounded(t, "teardown during window create", func() {
		s.openOrRestoreMainWindow()
	})

	if fw == nil {
		t.Fatal("expected fake window to be created")
	}
	if fw.showCalls != 0 {
		t.Fatalf("window must not Show() after teardown, got showCalls=%d", fw.showCalls)
	}
	if fw.destroyCalls != 1 {
		t.Fatalf("expected orphan window to be Destroy()ed after teardown, got destroyCalls=%d", fw.destroyCalls)
	}
	s.mu.Lock()
	win := s.win
	uiApp := s.uiApp
	opening := s.openingWindow
	s.mu.Unlock()
	if win != nil || uiApp != nil || opening {
		t.Fatalf("expected clean shell state after teardown during create, got win=%v uiApp=%v opening=%v", win != nil, uiApp != nil, opening)
	}
	st, ok := appdirs.ReadRuntimeWindowState(loc.DataDir)
	if !ok || st.WindowOpen {
		t.Fatalf("expected runtime-window.json windowOpen=false, got ok=%v st=%+v", ok, st)
	}
}

// TestOpenOrRestoreMainWindowCloseToTrayDuringShowDoesNotClobberRuntimeState
// ensures a re-entrant CloseToTray during Show() is not overwritten back to
// windowOpen=true when Show() unwinds, and a stale OnClose on a closed window
// is ignored via winClosed.
func TestOpenOrRestoreMainWindowCloseToTrayDuringShowDoesNotClobberRuntimeState(t *testing.T) {
	loc, b := newTestShellBackend(t, "dev")
	s := NewShell(loc, b)
	s.quiet = ""
	suppressTestTrayNotification(t, s)

	var fw *fakeShellWindow
	s.newMainWindow = func(opts mygo.WindowOptions) shellWindow {
		fw = &fakeShellWindow{opts: opts}
		fw.onShow = func() {
			s.CloseToTray()
		}
		return fw
	}

	runBounded(t, "CloseToTray during Show()", func() {
		s.openOrRestoreMainWindow()
	})

	if fw == nil || fw.closeCalls != 1 {
		t.Fatalf("expected CloseToTray to close window once, got fw=%v", fw)
	}
	st, ok := appdirs.ReadRuntimeWindowState(loc.DataDir)
	if !ok || st.WindowOpen {
		t.Fatalf("expected runtime-window.json to remain windowOpen=false, got ok=%v st=%+v", ok, st)
	}
	// Stale OnClose callback on the already-closed window must not prevent default or re-trigger CloseToTray.
	if len(fw.onCloseHandlers) == 0 {
		t.Fatal("expected OnClose handler to be registered")
	}
	s.mu.Lock()
	s.closingToTray = false
	s.mu.Unlock()
	ev := &mygo.CloseEvent{}
	fw.onCloseHandlers[0](ev)
	if fw.closeCalls != 1 {
		t.Fatalf("stale OnClose must not trigger extra close, got %d", fw.closeCalls)
	}
}

// TestCloseToTrayReentrantReopenDoesNotClobberRuntimeState ensures that if a
// new window opens re-entrantly during win.Close(), CloseToTray does not
// overwrite runtime-window.json back to windowOpen=false.
func TestCloseToTrayReentrantReopenDoesNotClobberRuntimeState(t *testing.T) {
	loc, b := newTestShellBackend(t, "dev")
	s := NewShell(loc, b)
	s.quiet = ""
	suppressTestTrayNotification(t, s)

	var created []*fakeShellWindow
	s.newMainWindow = func(opts mygo.WindowOptions) shellWindow {
		fw := &fakeShellWindow{opts: opts}
		created = append(created, fw)
		if len(created) == 1 {
			fw.onClose = func() {
				s.RestoreFromTray()
			}
		}
		return fw
	}

	s.openOrRestoreMainWindow()
	runBounded(t, "CloseToTray with re-entrant reopen", func() {
		s.CloseToTray()
	})

	if len(created) != 2 {
		t.Fatalf("expected 2 windows (closed + reopened), got %d", len(created))
	}
	st, ok := appdirs.ReadRuntimeWindowState(loc.DataDir)
	if !ok || !st.WindowOpen {
		t.Fatalf("expected runtime-window.json to stay windowOpen=true after re-entrant reopen, got ok=%v st=%+v", ok, st)
	}
	s.CloseToTray()
}

// TestRequestQuitQueriesActiveWorkOffShellMutex verifies RequestQuit releases
// s.mu before querying backend active work, preventing AB-BA lock inversion
// with backend/event callbacks that lock s.mu.
func TestRequestQuitQueriesActiveWorkOffShellMutex(t *testing.T) {
	loc, b := newTestShellBackend(t, "dev")
	s := NewShell(loc, b)
	s.quiet = ""

	var lockedInCheck atomic.Bool
	prompted := make(chan struct{}, 1)
	s.checkActiveWork = func() bool {
		// Acquiring s.mu here would immediately self-deadlock if RequestQuit held s.mu.
		s.mu.Lock()
		lockedInCheck.Store(true)
		s.mu.Unlock()
		return true
	}
	s.confirmQuitDialog = func(mygo.MessageOptions) (mygo.MessageResult, error) {
		prompted <- struct{}{}
		return mygo.MessageResult{Button: 0}, nil
	}

	runBounded(t, "RequestQuit active-work check off s.mu", func() {
		s.RequestQuit(false)
	})
	if !lockedInCheck.Load() {
		t.Fatal("expected checkActiveWork to run and acquire s.mu freely")
	}
	select {
	case <-prompted:
	case <-time.After(2 * time.Second):
		t.Fatal("expected quit confirmation dialog to be prompted")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		open := s.quitConfirmOpen
		s.mu.Unlock()
		if !open {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("quitConfirmOpen was not cleared after cancel")
}
