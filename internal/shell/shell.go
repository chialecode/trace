package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/companion"
	"github.com/chialecode/trace/internal/companionapi"
	"github.com/chialecode/trace/internal/player"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/storage"
	"github.com/chialecode/trace/internal/ui"
	"github.com/chialecode/trace/internal/uistate"
	"github.com/chialecode/trace/internal/update"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/netproxy"
	mygoui "github.com/egoist/mygo/ui"
)

func isReleaseChannel(channel string) bool {
	return strings.EqualFold(channel, "release") || strings.EqualFold(channel, "stable")
}

// QuietMode resolves the effective quiet mode for the given channel.
// Quiet mode is only active on dev/source channels; release/stable ignores TRACE_QUIET.
func QuietMode(channel string) string {
	if isReleaseChannel(channel) {
		return ""
	}
	raw := strings.ToLower(strings.TrimSpace(os.Getenv("TRACE_QUIET")))
	switch raw {
	case "1", "true", "hidden":
		return "hidden"
	case "inactive":
		return "inactive"
	default:
		return ""
	}
}

// RuntimeStateSnapshot records window, tray, memory, and focus state for smoke tests and diagnostics.
type RuntimeStateSnapshot struct {
	PID                    int      `json:"pid"`
	AppName                string   `json:"appName"`
	DisplayName            string   `json:"displayName"`
	Channel                string   `json:"channel"`
	Mode                   string   `json:"mode"`
	QuietMode              string   `json:"quietMode"`
	DataDir                string   `json:"dataDir"`
	CacheDir               string   `json:"cacheDir"`
	WindowOpen             bool     `json:"windowOpen"`
	TrayActive             bool     `json:"trayActive"`
	ActiveRoute            string   `json:"activeRoute"`
	WorkingSetBytes        int64    `json:"workingSetBytes"`
	PrivateWorkingSetBytes int64    `json:"privateWorkingSetBytes"`
	PrivateBytes           int64    `json:"privateBytes"`
	WebView2ProcessCount   int      `json:"webview2ProcessCount"`
	ForegroundPID          uint32   `json:"foregroundPid"`
	StealFocusDetected     bool     `json:"stealFocusDetected"`
	StealFocusDetail       string   `json:"stealFocusDetail,omitempty"`
	SecondInstanceArgs     []string `json:"secondInstanceArgs,omitempty"`
	Version                string   `json:"version"`
	Commit                 string   `json:"commit"`
	BuildTime              string   `json:"buildTime"`
	Timestamp              string   `json:"timestamp"`
}

// shellWindow abstracts *mygo.Window so Shell's main-thread re-entrancy and
// lifecycle transitions can be tested deterministically without OS windows.
type shellWindow interface {
	SetWindowButtonsTheme(mygo.ThemeSource)
	SetFullScreen(bool)
	Restore()
	Show()
	ShowInactive()
	Hide()
	Focus()
	Update(func())
	OnClose(func(*mygo.CloseEvent)) func()
	Close()
	Destroy()
	IsMinimized() bool
	IsFullScreen() bool
	Size() (int, int)
	rawWindow() *mygo.Window
}

type mygoShellWindow struct {
	*mygo.Window
}

func (w mygoShellWindow) rawWindow() *mygo.Window { return w.Window }

func newMyGoShellWindow(opts mygo.WindowOptions) shellWindow {
	return mygoShellWindow{Window: mygo.NewWindow(opts)}
}

// Shell manages the native desktop lifecycle, single-instance lock, tray icon, and window creation/destruction.
type Shell struct {
	mu                 sync.Mutex
	loc                appdirs.Locations
	quiet              string
	lockHandle         *appdirs.Lock
	backend            *service.Backend
	uiApp              *ui.App
	win                shellWindow
	winClosed          *atomic.Bool
	tray               *mygo.Tray
	unsubEvents        func()
	openingWindow      bool
	closingToTray      bool
	quitting           bool
	quitConfirmOpen    bool
	lastRoute          string
	secondInstanceArgs []string
	stoleFocus         bool
	stoleFocusDetail   string
	updates            *update.Controller
	companion          *companion.Manager

	// Serialized tray refresh worker state; hooks below are replaceable in tests.
	trayMu            sync.Mutex
	trayRunning       bool
	trayDirty         bool
	trayStopped       bool
	trayWg            sync.WaitGroup
	trayRefresh       func()
	trayDispatchMain  func(func())
	confirmQuitDialog func(mygo.MessageOptions) (mygo.MessageResult, error)
	openFileDialog    func(mygo.OpenDialogOptions) ([]string, error)
	saveFileDialog    func(mygo.SaveDialogOptions) (string, error)
	newMainWindow     func(mygo.WindowOptions) shellWindow
	checkActiveWork   func() bool
	trayLastTooltip   string
	trayHasState      bool
	trayWantTooltip   string
	trayWantDirty     bool
	trayApplyQueued   bool
}

// NewShell creates a new Shell instance for the given locations and backend.
func NewShell(loc appdirs.Locations, backend *service.Backend) *Shell {
	st := backend.GetUIState()
	route := st.LastRoute
	if route == "" {
		route = ui.RouteHome
	}
	return &Shell{
		loc:       loc,
		quiet:     QuietMode(loc.Channel),
		backend:   backend,
		lastRoute: route,
	}
}

// Run executes the full TRACE desktop application lifecycle:
//  1. appdirs.Resolve()
//  2. mygo.App.SetName(loc.AppName)
//  3. mygo.App.RequestSingleInstanceLock() (second instance forwards args and exits before touching DataDir)
//  4. appdirs.LockDataDir(loc.DataDir)
//  5. mygo.App.SetPath(userData/cache/logs -> <DataDir>/mygo, cache, logs)
//  6. service.NewBackend(...)
//  7. mygo.App.Run()
func Run() error {
	loc, err := appdirs.Resolve()
	if err != nil {
		return fmt.Errorf("shell: resolve appdirs: %w", err)
	}

	// Set lowercase application name BEFORE RequestSingleInstanceLock and SetPath (DEC-029).
	build := appdirs.BuildInfo()
	mygo.App.SetName(loc.AppName)
	mygo.App.SetVersion(build.Version)
	launch := ParseLaunchArgs(os.Args[1:])

	if QuietMode(loc.Channel) != "" {
		_ = SetBelowNormalPriority()
	}

	// Request single-instance lock. Second instance forwards args and returns immediately without touching DataDir.
	if !mygo.App.RequestSingleInstanceLock() {
		return nil
	}
	// --quit-for-update asks a running TRACE to quit; when none runs there
	// is nothing to do, and this launch must not start one.
	if launch.QuitForUpdate {
		return nil
	}

	// Ensure directories and acquire exclusive app.lock inside DataDir.
	if err := appdirs.EnsureDirectories(loc); err != nil {
		return fmt.Errorf("shell: ensure directories: %w", err)
	}
	lockHandle, err := appdirs.LockDataDir(loc.DataDir)
	if err != nil {
		return fmt.Errorf("shell: acquire app.lock: %w", err)
	}

	var (
		cleanupOnce sync.Once
		stopUpdates context.CancelFunc
		sh          *Shell
		backend     *service.Backend
	)
	cleanup := func() {
		cleanupOnce.Do(func() {
			if stopUpdates != nil {
				stopUpdates()
			}
			if sh != nil {
				if sh.companion != nil {
					sh.companion.Shutdown(companionapi.ByeReasonExit)
				}
				sh.stopTrayRefresh()
			}
			if backend != nil {
				_ = backend.Close()
			}
			appdirs.RemoveRuntimeWindowState(loc.DataDir, os.Getpid())
			if lockHandle != nil {
				_ = lockHandle.Release()
			}
		})
	}
	defer cleanup()

	previousCrash := setupCrashOutput(loc.LogsDir)

	// Redirect MyGo internal paths inside <DataDir> so %APPDATA% is never touched (INV-14).
	mygoDir := filepath.Join(loc.DataDir, "mygo")
	cacheSubDir := filepath.Join(loc.DataDir, "cache")
	_ = os.MkdirAll(mygoDir, 0o700)
	_ = os.MkdirAll(cacheSubDir, 0o700)
	mygo.App.SetPath(mygo.PathUserData, mygoDir)
	mygo.App.SetPath(mygo.PathCache, cacheSubDir)
	mygo.App.SetPath(mygo.PathLogs, loc.LogsDir)

	// "Follow the system" proxy reads Windows' own settings, not only HTTP(S)_PROXY (DEC-046 ⑤).
	site.SetSystemProxyFunc(netproxy.Proxy)
	backend, err = service.NewBackend(service.Options{
		Locations:           loc,
		InstalledDevVersion: service.ReadSystemInstalledDevVersion,
	})
	if err != nil {
		if storage.IsNewerData(err) {
			return refuseNewerData(loc, err)
		}
		return fmt.Errorf("shell: init backend: %w", err)
	}

	if previousCrash != "" {
		backend.Logf("the previous run ended in a crash; traceback kept in logs/%s", previousCrash)
	}

	sh = NewShell(loc, backend)
	sh.lockHandle = lockHandle

	decision := recordStartupDecision(backend, loc.Channel, build.Version, launch, time.Now().UTC())
	if loc.Mode == "installed" {
		if wrote, err := syncUninstallVersion(systemUninstallEntry(), loc.Identifier, loc.InstallDir, build.Version); err != nil {
			backend.Logf("update the uninstall entry: %v", err)
		} else if wrote {
			backend.Logf("uninstall entry now names %s", build.Version)
		}
	}
	var updateCtx context.Context
	updateCtx, stopUpdates = context.WithCancel(context.Background())
	defer stopUpdates()
	sh.updates = newUpdateController(backend, sh.redraw, sh.RestartToUpdate)
	if decision.UpdatedTo != "" {
		sh.updates.SetUpdatedNotice(decision.UpdatedTo, decision.UpdatedNotes)
	}
	if compMgr, cErr := companion.NewManager(companion.ManagerOptions{
		DataDir:  loc.DataDir,
		Channel:  loc.Channel,
		Version:  build.Version,
		Backend:  backend,
		Logf:     backend.Logf,
		OnChange: sh.redraw,
	}); cErr != nil {
		backend.Logf("companion init failed: %v", cErr)
	} else {
		sh.companion = compMgr
		curSet := backend.GetSettings()
		if curSet.Companion.Enabled {
			if err := compMgr.ApplySettings(true, curSet.Companion.Port); err != nil {
				backend.Logf("companion auto-start failed: %v", err)
			}
		}
	}

	// Keep the event loop running when closing window to tray (DEC-030).
	mygo.App.OnWindowAllClosed(func() {
		// Do not quit when window is closed to tray; Quit is triggered explicitly.
	})

	mygo.App.OnBeforeQuit(func(e *mygo.QuitEvent) {
		_, alreadyQuitting := sh.teardown()
		if !alreadyQuitting {
			if sh.companion != nil {
				sh.companion.Shutdown(companionapi.ByeReasonExit)
			}
			// OS session shutdown or external quit bypassed RequestQuit: pause tasks and finalize recordings synchronously.
			backend.ExitGracefully(10 * time.Second)
		}
	})

	// Release SQLite databases and app.lock synchronously inside OnQuit, which
	// MyGo runs after the event loop ends and BEFORE Relaunch spawns the new binary.
	mygo.App.OnQuit(cleanup)

	mygo.App.OnSecondInstance(func(args []string, workingDir string) {
		mygo.RunOnMain(func() {
			sh.HandleSecondInstance(args)
		})
	})

	mygo.App.WhenReady(func() {
		sh.setupTray()
		sh.recordRuntimeWindowState(false)
		if decision.OpenWindow {
			sh.openOrRestoreMainWindow()
		}
		sh.updates.Start(updateCtx)
		if smokeDir := smokeDirForChannel(loc.Channel); smokeDir != "" {
			go sh.runSmokeControlLoop(smokeDir)
		}
	})

	return mygo.App.Run()
}

func audioSinkForQuiet(quiet string) player.AudioSink {
	if quiet != "" {
		return player.NewNullAudioSink()
	}
	return player.NewMyGoAudioSink()
}

func (s *Shell) audioSinkForMode() player.AudioSink {
	return audioSinkForQuiet(s.quiet)
}

func captureWindowSize(win shellWindow) (int, int, bool) {
	if win == nil || win.IsMinimized() || win.IsFullScreen() {
		return 0, 0, false
	}
	w, h := win.Size()
	if w < 720 || h < 520 {
		return 0, 0, false
	}
	return w, h, true
}

// mainWindowButtonsTheme keeps hidden-titlebar caption glyphs dark over the
// fixed white (#ffffff) workbench even when Windows is in dark mode.
const mainWindowButtonsTheme = mygo.ThemeLight

func (s *Shell) recordRuntimeWindowState(open bool) {
	_ = appdirs.WriteRuntimeWindowState(s.loc.DataDir, os.Getpid(), open)
}

func (s *Shell) openOrRestoreMainWindow() {
	s.mu.Lock()
	if s.quitting || s.openingWindow {
		s.mu.Unlock()
		return
	}
	if s.win != nil {
		w := s.win
		quiet := s.quiet
		s.openingWindow = true
		s.mu.Unlock()
		defer func() {
			s.mu.Lock()
			s.openingWindow = false
			s.mu.Unlock()
		}()
		stillCurrent := func() bool {
			s.mu.Lock()
			ok := !s.quitting && s.win == w
			s.mu.Unlock()
			return ok
		}
		if quiet == "" {
			w.Restore()
			if stillCurrent() {
				w.Show()
			}
			if stillCurrent() {
				w.Focus()
			}
		} else if quiet == "inactive" {
			w.ShowInactive()
		}
		if stillCurrent() {
			s.recordRuntimeWindowState(quiet != "hidden")
		}
		s.checkForegroundSafety("reuse_window")
		return
	}

	s.openingWindow = true
	backend := s.backend
	updates := s.updates
	comp := s.companion
	lastRoute := s.lastRoute
	quiet := s.quiet
	displayName := s.loc.DisplayName
	newWin := s.newMainWindow
	if newWin == nil {
		newWin = newMyGoShellWindow
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.openingWindow = false
		s.mu.Unlock()
	}()

	// Never hold s.mu across UI construction, backend reads, or MyGo/Win32
	// window calls: Windows ShowWindow / UI Automation COM STA loops can
	// re-entrantly dispatch tray clicks or RunOnMain callbacks on the main
	// thread, which would self-deadlock on s.mu.
	uiApp := ui.NewApp(backend, audioSinkForQuiet(quiet))
	if updates != nil {
		uiApp.BindUpdater(updates)
	}
	if comp != nil {
		uiApp.BindCompanion(comp)
	}
	if lastRoute != "" {
		uiApp.Navigate(lastRoute)
	}

	uiSt := backend.GetUIState()
	width := uiSt.Window.Width
	height := uiSt.Window.Height
	if width < 720 {
		width = 1280
	}
	if height < 520 {
		height = 860
	}

	s.mu.Lock()
	if s.quitting {
		s.mu.Unlock()
		uiApp.Close()
		return
	}
	s.mu.Unlock()

	// Always create Hidden: true first so SetWindowButtonsTheme does not
	// prematurely settleShow() before BindEventLoop, OnClose, and s.win are
	// wired (and so the first View frame runs with a.dispatch already bound).
	winOpts := mygo.WindowOptions{
		Title:           displayName,
		Width:           width,
		Height:          height,
		MinWidth:        720,
		MinHeight:       520,
		Hidden:          true,
		AutoHideMenuBar: true,
		// Immersive title bar (DEC-046 ④): the page reaches the top and the
		// system's minimize, maximize and close buttons stay over it.
		TitleBarStyle:   mygo.TitleBarHidden,
		BackgroundColor: "#ffffff",
		Content:         mygoui.View(uiApp.View),
	}
	win := newWin(winOpts)
	win.SetWindowButtonsTheme(mainWindowButtonsTheme)
	winClosed := &atomic.Bool{}
	uiApp.BindFullscreenHandler(func(full bool) {
		if quiet == "" && !winClosed.Load() {
			win.SetFullScreen(full)
		}
	})
	uiApp.BindFileDialog(s.newFileDialogHandler(win.rawWindow(), winClosed))
	uiApp.BindEventLoop(func(fn func()) {
		if !winClosed.Load() {
			win.Update(fn)
		}
	})

	win.OnClose(func(e *mygo.CloseEvent) {
		if winClosed.Load() {
			return
		}
		s.mu.Lock()
		quitting := s.quitting
		closingToTray := s.closingToTray
		s.mu.Unlock()
		if quitting || closingToTray {
			return
		}
		closeAction := backend.GetSettings().Window.CloseAction
		if closeAction == "tray" {
			e.PreventDefault()
			go func() {
				mygo.RunOnMain(func() {
					s.CloseToTray()
				})
			}()
		} else {
			e.PreventDefault()
			s.RequestQuit(false)
		}
	})

	s.mu.Lock()
	if s.quitting {
		s.mu.Unlock()
		winClosed.Store(true)
		uiApp.Close()
		win.Destroy()
		return
	}
	s.uiApp = uiApp
	s.win = win
	s.winClosed = winClosed
	s.closingToTray = false
	s.mu.Unlock()

	if quiet == "" {
		win.Show()
	} else if quiet == "inactive" {
		win.ShowInactive()
	} else if quiet == "hidden" {
		win.Hide()
	}

	s.mu.Lock()
	stillOpen := !s.quitting && s.win == win
	s.mu.Unlock()
	if stillOpen {
		s.recordRuntimeWindowState(quiet != "hidden")
	}
	s.checkForegroundSafety("open_window")
}

func (s *Shell) checkForegroundSafety(stage string) {
	if s.quiet == "" {
		return
	}
	fgHWND, fgPID := GetForegroundWindowHandle()
	if fgPID != 0 && fgPID == uint32(os.Getpid()) {
		detail := stage
		if desc := DescribeWindowHandle(fgHWND); desc != "" {
			detail = stage + " (" + desc + ")"
		}
		s.mu.Lock()
		s.stoleFocus = true
		if s.stoleFocusDetail == "" {
			s.stoleFocusDetail = detail
		}
		s.mu.Unlock()
	}
}

func (s *Shell) persistCloseToTrayState(route string, width, height int, saveSize, interactive bool) bool {
	if s.backend == nil {
		return false
	}
	var shouldNotify bool
	_, _ = s.backend.MutateUIState(func(st *uistate.State) {
		if route != "" {
			st.LastRoute = route
		}
		if saveSize {
			st.Window.Width = width
			st.Window.Height = height
		}
		if interactive && !st.TrayNotified {
			shouldNotify = true
			st.TrayNotified = true
		}
	})
	return shouldNotify
}

// CloseToTray destroys the native UI window, releases bitmaps and UI subscriptions,
// and forces garbage collection + OS memory return so tray resident memory stays < 60 MB (DEC-030, INV-13).
func (s *Shell) CloseToTray() {
	s.mu.Lock()
	win := s.win
	uiApp := s.uiApp
	winClosed := s.winClosed
	quiet := s.quiet
	title := s.loc.DisplayName
	s.mu.Unlock()

	var route string
	if uiApp != nil {
		route = uiApp.CurrentRoute
	}
	w, h, saveSize := captureWindowSize(win)

	s.mu.Lock()
	if route != "" {
		s.lastRoute = route
	}
	if s.uiApp == uiApp {
		s.uiApp = nil
	}
	if s.win == win {
		s.win = nil
		s.winClosed = nil
	}
	s.closingToTray = true
	s.mu.Unlock()

	if winClosed != nil {
		winClosed.Store(true)
	}
	firstNotify := s.persistCloseToTrayState(route, w, h, saveSize, quiet == "")
	if uiApp != nil {
		uiApp.Close()
	}
	if win != nil {
		win.Close()
	}
	s.mu.Lock()
	stillClosed := s.win == nil
	s.mu.Unlock()
	if stillClosed {
		s.recordRuntimeWindowState(false)
	}

	runtime.GC()
	debug.FreeOSMemory()
	TrimWorkingSet()

	// Only show first-time tray notification in interactive mode; quiet mode never sends notifications.
	if firstNotify && quiet == "" && mygo.NotificationsSupported() {
		n := mygo.NewNotification(mygo.NotificationOptions{
			ID:     "trace-tray-hint",
			Title:  title,
			Body:   "窗口已销毁并最小化到托盘，后台任务继续运行。",
			Silent: true,
		})
		_ = n.Show()
	}
	s.checkForegroundSafety("close_to_tray")
}

// RestoreFromTray recreates the native UI window and restores the previous route (DEC-030).
func (s *Shell) RestoreFromTray() {
	s.openOrRestoreMainWindow()
	s.checkForegroundSafety("restore_from_tray")
}

// HandleSecondInstance handles command-line arguments forwarded from a second instance.
func (s *Shell) HandleSecondInstance(args []string) {
	s.mu.Lock()
	s.secondInstanceArgs = append([]string(nil), args...)
	quitting := s.quitting
	s.mu.Unlock()

	launch := ParseLaunchArgs(args)
	if launch.QuitForUpdate {
		s.QuitForUpdate("dev-install", false)
		return
	}
	if quitting || launch.Background {
		// Already quitting or launched in background: nothing to open.
		return
	}

	s.openOrRestoreMainWindow()

	// If a Bilibili link/BV/UID was passed in args, parse it in the UI
	for _, arg := range args {
		trimmed := strings.TrimSpace(arg)
		if strings.HasPrefix(trimmed, "BV") || strings.HasPrefix(trimmed, "http") || strings.HasPrefix(strings.ToUpper(trimmed), "UID:") {
			s.mu.Lock()
			app := s.uiApp
			w := s.win
			wc := s.winClosed
			s.mu.Unlock()
			if app != nil {
				app.SubmitParse(trimmed)
				if w != nil && (wc == nil || !wc.Load()) {
					w.Update(func() {})
				}
			}
			break
		}
	}
	s.checkForegroundSafety("second_instance")
	if smokeDir := smokeDirForChannel(s.loc.Channel); smokeDir != "" {
		snap := s.Snapshot()
		if raw, err := json.MarshalIndent(snap, "", "  "); err == nil {
			_ = storage.WriteFileAtomic(filepath.Join(smokeDir, "second_instance.json"), raw, 0o600)
		}
	}
}

// RequestQuit quits the application, optionally asking for confirmation if tasks/recordings are active in interactive mode.
func (s *Shell) RequestQuit(force bool) {
	s.mu.Lock()
	if s.quitting {
		s.mu.Unlock()
		return
	}
	quiet := s.quiet
	confirmOpen := s.quitConfirmOpen
	activeCheck := s.checkActiveWork
	s.mu.Unlock()

	if !force && quiet == "" {
		if confirmOpen {
			return
		}
		if activeCheck == nil {
			activeCheck = s.hasActiveWork
		}
		// Query backend task/recording locks off s.mu to avoid AB-BA lock inversion.
		if activeCheck() {
			s.mu.Lock()
			if s.quitting || s.quitConfirmOpen {
				s.mu.Unlock()
				return
			}
			s.quitConfirmOpen = true
			displayName := s.loc.DisplayName
			ask := s.confirmQuitDialog
			if ask == nil {
				ask = mygo.Dialog.Message
			}
			s.mu.Unlock()
			go func() {
				res, err := ask(mygo.MessageOptions{
					Type:          mygo.MessageWarning,
					Title:         "确认退出 " + displayName,
					Message:       "当前仍有正在运行的缓存任务或直播录制，确定要退出吗？",
					Detail:        "退出前会自动暂停任务并安全收尾正在录制的分段。",
					Buttons:       []string{"取消", "确认退出"},
					DefaultButton: 0,
					CancelButton:  0,
				})
				s.mu.Lock()
				s.quitConfirmOpen = false
				quitting := s.quitting
				s.mu.Unlock()
				if !quitting && err == nil && res.Button == 1 {
					mygo.RunOnMain(func() {
						s.performQuit()
					})
				}
			}()
			return
		}
		s.mu.Lock()
		if s.quitting || s.quitConfirmOpen {
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
	}
	s.performQuit()
}

const quitPauseTimeout = 30 * time.Second

func (s *Shell) performQuit() {
	_, alreadyQuitting := s.teardown()
	if alreadyQuitting {
		return
	}
	// Pausing finalizes recordings, which may take a while: off the main
	// thread, so that the event loop keeps running meanwhile, bounded so a
	// stuck stream can never hang exit forever.
	go func() {
		if s.companion != nil {
			s.companion.Shutdown(companionapi.ByeReasonExit)
		}
		if s.backend != nil && !s.backend.ExitGracefully(quitPauseTimeout) {
			s.backend.Logf("pause for exit timed out after %s; proceeding with quit", quitPauseTimeout)
		}
		mygo.RunOnMain(func() { mygo.App.Quit() })
	}()
}

// teardown releases the window, the interface and the tray before a quit,
// saving the route and window dimensions. It returns whether the window was
// open and whether a quit was already in progress.
func (s *Shell) teardown() (windowOpen bool, alreadyQuitting bool) {
	s.mu.Lock()
	if s.quitting {
		s.mu.Unlock()
		return false, true
	}
	s.quitting = true
	win := s.win
	uiApp := s.uiApp
	winClosed := s.winClosed
	unsub := s.unsubEvents
	tray := s.tray
	s.unsubEvents = nil
	s.uiApp = nil
	s.win = nil
	s.winClosed = nil
	s.tray = nil
	s.mu.Unlock()

	windowOpen = win != nil
	var route string
	if uiApp != nil {
		route = uiApp.CurrentRoute
	}
	w, h, saveSize := captureWindowSize(win)

	if winClosed != nil {
		winClosed.Store(true)
	}
	if unsub != nil {
		unsub()
	}
	s.markTrayStopped()

	if route != "" {
		s.mu.Lock()
		s.lastRoute = route
		s.mu.Unlock()
	}
	if s.backend != nil && (route != "" || saveSize) {
		_, _ = s.backend.MutateUIState(func(st *uistate.State) {
			if route != "" {
				st.LastRoute = route
			}
			if saveSize {
				st.Window.Width = w
				st.Window.Height = h
			}
		})
	}
	if uiApp != nil {
		uiApp.Close()
	}
	if tray != nil {
		tray.Destroy()
	}
	if win != nil {
		win.Close()
	}
	s.recordRuntimeWindowState(false)
	return windowOpen, false
}

// QuitForUpdate saves TRACE's work and quits without asking, for an
// update: tasks are paused as the tray's Quit pauses them, recordings are
// finalized, and ui-state.json records why TRACE quit and whether its
// window was open, so that the next launch reopens it the same way. With
// relaunch, TRACE then starts again (the downloaded version, for
// "restart to update"); otherwise tools/package installs the new build
// and starts it.
func (s *Shell) QuitForUpdate(reason string, relaunch bool) {
	windowOpen, alreadyQuitting := s.teardown()
	if alreadyQuitting {
		return
	}
	from := appdirs.BuildInfo().Version
	go func() {
		if s.companion != nil {
			s.companion.Shutdown(companionapi.ByeReasonUpdate)
		}
		if s.backend != nil {
			if !s.backend.ExitGracefully(quitPauseTimeout) {
				s.backend.Logf("pause for update quit timed out after %s; continuing", quitPauseTimeout)
			}
			_, err := s.backend.MutateUIState(func(st *uistate.State) {
				st.Update.Restart = &uistate.UpdateRestart{Reason: reason, WindowOpen: windowOpen, FromVersion: from, At: time.Now().UTC().Format(time.RFC3339)}
			})
			if err != nil {
				s.backend.Logf("record quit for update: %v", err)
			}
			s.backend.Logf("quitting for update (%s, window open: %v)", reason, windowOpen)
		}
		mygo.RunOnMain(func() {
			if relaunch {
				mygo.App.Relaunch()
			} else {
				mygo.App.Quit()
			}
		})
	}()
}

// RestartToUpdate restarts TRACE into the downloaded update.
func (s *Shell) RestartToUpdate() {
	mygo.RunOnMain(func() { s.QuitForUpdate("update", true) })
}

// redraw asks the window, when there is one, to render again.
func (s *Shell) redraw() {
	s.mu.Lock()
	w := s.win
	wc := s.winClosed
	s.mu.Unlock()
	if w != nil && (wc == nil || !wc.Load()) {
		w.Update(func() {})
	}
}

// refuseNewerData tells the user that the data directory holds data of a
// newer TRACE and exits without writing to it.
func refuseNewerData(loc appdirs.Locations, cause error) error {
	fmt.Fprintln(os.Stderr, cause.Error())
	if QuietMode(loc.Channel) != "" {
		return cause
	}
	mygo.App.OnWindowAllClosed(func() {})
	mygo.App.WhenReady(func() {
		go func() {
			_, _ = mygo.Dialog.Message(mygo.MessageOptions{
				Type:    mygo.MessageError,
				Title:   loc.DisplayName,
				Message: storage.NewerDataMessage,
				Detail:  cause.Error() + "\n\n数据没有改动。请安装新版 " + loc.DisplayName + " 后再打开。",
				Buttons: []string{"退出"},
			})
			mygo.RunOnMain(func() { mygo.App.Quit() })
		}()
	})
	_ = mygo.App.Run()
	return cause
}

// Snapshot captures the current runtime state of the shell for testing and smoke assertions.
func (s *Shell) Snapshot() RuntimeStateSnapshot {
	s.checkForegroundSafety("snapshot")
	ws, privWS, priv := CurrentProcessMemoryBytes()
	_, fgPID := GetForegroundWindowHandle()
	pid := os.Getpid()
	wv2Count := CountWebView2Processes(uint32(pid))

	s.mu.Lock()
	winOpen := s.win != nil && s.uiApp != nil
	trayActive := s.tray != nil
	route := s.lastRoute
	if s.uiApp != nil {
		route = s.uiApp.CurrentRoute
	}
	args := append([]string(nil), s.secondInstanceArgs...)
	stole := s.stoleFocus
	stoleDetail := s.stoleFocusDetail
	s.mu.Unlock()

	var cacheDir string
	if s.backend != nil {
		cacheDir = s.backend.LibraryStore().CacheDir()
	}

	return RuntimeStateSnapshot{
		PID:                    pid,
		AppName:                s.loc.AppName,
		DisplayName:            s.loc.DisplayName,
		Channel:                s.loc.Channel,
		Mode:                   s.loc.Mode,
		QuietMode:              s.quiet,
		DataDir:                s.loc.DataDir,
		CacheDir:               cacheDir,
		WindowOpen:             winOpen,
		TrayActive:             trayActive,
		ActiveRoute:            route,
		WorkingSetBytes:        ws,
		PrivateWorkingSetBytes: privWS,
		PrivateBytes:           priv,
		WebView2ProcessCount:   wv2Count,
		ForegroundPID:          fgPID,
		StealFocusDetected:     stole,
		StealFocusDetail:       stoleDetail,
		SecondInstanceArgs:     args,
		Version:                appdirs.BuildInfo().Version,
		Commit:                 appdirs.BuildInfo().Commit,
		BuildTime:              appdirs.BuildInfo().BuildTime,
		Timestamp:              time.Now().UTC().Format(time.RFC3339),
	}
}
