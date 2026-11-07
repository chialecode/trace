package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/player"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/storage"
	"github.com/chialecode/trace/internal/ui"
	"github.com/egoist/mygo"
	mygoui "github.com/egoist/mygo/ui"
)

// QuietMode resolves the effective quiet mode for the given channel (§6).
// Quiet mode is only active on dev/source channels; release/stable ignores TRACE_QUIET.
func QuietMode(channel string) string {
	if strings.EqualFold(channel, "release") || strings.EqualFold(channel, "stable") {
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

// RenderTrayIconPNG generates a 32x32 PNG tray icon in pure Go: a white "T"
// on the accent blue (DEC-039), with an amber corner while tasks or
// recordings run (P13).
func RenderTrayIconPNG(hasActiveWork bool) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	bg := color.RGBA{R: 26, G: 115, B: 232, A: 255}  // #1a73e8
	fg := color.RGBA{R: 255, G: 255, B: 255, A: 255} // #ffffff
	dot := color.RGBA{R: 243, G: 161, B: 21, A: 255} // #f3a115

	for y := 2; y < 30; y++ {
		for x := 2; x < 30; x++ {
			img.SetRGBA(x, y, bg)
		}
	}
	// Draw 'T' glyph in white
	for y := 8; y < 12; y++ {
		for x := 8; x < 24; x++ {
			img.SetRGBA(x, y, fg)
		}
	}
	for y := 12; y < 25; y++ {
		for x := 14; x < 18; x++ {
			img.SetRGBA(x, y, fg)
		}
	}
	// Optional active work indicator dot in bottom-right corner
	if hasActiveWork {
		for y := 21; y < 30; y++ {
			for x := 21; x < 30; x++ {
				img.SetRGBA(x, y, dot)
			}
		}
	}

	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// RuntimeStateSnapshot records window, tray, memory, and focus state for smoke tests and PR-4 (C35, C36, C39).
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
	SecondInstanceArgs     []string `json:"secondInstanceArgs,omitempty"`
	Timestamp              string   `json:"timestamp"`
}

// Shell manages the native desktop lifecycle, single-instance lock, tray icon, and window creation/destruction.
type Shell struct {
	mu                 sync.Mutex
	loc                appdirs.Locations
	quiet              string
	lockHandle         *appdirs.Lock
	backend            *service.Backend
	uiApp              *ui.App
	win                *mygo.Window
	tray               *mygo.Tray
	statusMenuItem     *mygo.MenuItem
	unsubEvents        func()
	closingToTray      bool
	quitting           bool
	notifiedTrayOnce   bool
	lastRoute          string
	secondInstanceArgs []string
	stoleFocus         bool
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

// Run executes the full TRACE desktop application lifecycle per P13 & §1.5:
//  1. appdirs.Resolve()
//  2. mygo.App.SetName(loc.AppName)
//  3. mygo.App.RequestSingleInstanceLock() (second instance forwards args and exits before touching DataDir)
//  4. appdirs.AcquireLock(loc.LockFile)
//  5. mygo.App.SetPath(userData/cache/logs -> <DataDir>/mygo, cache, logs)
//  6. service.NewBackend(...)
//  7. mygo.App.Run()
func Run() error {
	loc, err := appdirs.Resolve()
	if err != nil {
		return fmt.Errorf("shell: resolve appdirs: %w", err)
	}

	// Step 2: Set lowercase application name BEFORE RequestSingleInstanceLock and SetPath (DEC-029).
	mygo.App.SetName(loc.AppName)
	mygo.App.SetVersion("0.1.0")

	if QuietMode(loc.Channel) != "" {
		_ = SetBelowNormalPriority()
	}

	// Step 3: Request single-instance lock. Second instance forwards args and returns immediately without touching DataDir (C36).
	if !mygo.App.RequestSingleInstanceLock() {
		return nil
	}

	// Step 4: Ensure directories and acquire exclusive app.lock inside DataDir.
	if err := appdirs.EnsureDirectories(loc); err != nil {
		return fmt.Errorf("shell: ensure directories: %w", err)
	}
	lockHandle, err := appdirs.LockDataDir(loc.DataDir)
	if err != nil {
		return fmt.Errorf("shell: acquire app.lock: %w", err)
	}
	defer func() {
		_ = lockHandle.Release()
	}()

	// Step 5: Redirect MyGo internal paths inside <DataDir> so %APPDATA% is never touched (INV-14, C36).
	mygoDir := filepath.Join(loc.DataDir, "mygo")
	cacheSubDir := filepath.Join(loc.DataDir, "cache")
	_ = os.MkdirAll(mygoDir, 0o700)
	_ = os.MkdirAll(cacheSubDir, 0o700)
	mygo.App.SetPath(mygo.PathUserData, mygoDir)
	mygo.App.SetPath(mygo.PathCache, cacheSubDir)
	mygo.App.SetPath(mygo.PathLogs, loc.LogsDir)

	// Step 6: Start Backend service.
	backend, err := service.NewBackend(service.Options{
		Locations: loc,
	})
	if err != nil {
		return fmt.Errorf("shell: init backend: %w", err)
	}
	defer func() {
		_ = backend.Close()
	}()

	sh := NewShell(loc, backend)
	sh.lockHandle = lockHandle

	// Keep the event loop running when closing window to tray (DEC-030).
	mygo.App.OnWindowAllClosed(func() {
		// Do not quit when window is closed to tray; Quit is triggered explicitly.
	})

	mygo.App.OnBeforeQuit(func(e *mygo.QuitEvent) {
		sh.mu.Lock()
		sh.quitting = true
		sh.mu.Unlock()
	})

	mygo.App.OnSecondInstance(func(args []string, workingDir string) {
		mygo.RunOnMain(func() {
			sh.HandleSecondInstance(args)
		})
	})

	mygo.App.WhenReady(func() {
		sh.setupTray()
		sh.openOrRestoreMainWindow()
		if smokeDir := strings.TrimSpace(os.Getenv("TRACE_SMOKE_DIR")); smokeDir != "" {
			go sh.runSmokeControlLoop(smokeDir)
		}
	})

	return mygo.App.Run()
}

func (s *Shell) audioSinkForMode() player.AudioSink {
	if s.quiet != "" {
		return player.NewNullAudioSink()
	}
	return player.NewMyGoAudioSink()
}

func (s *Shell) openOrRestoreMainWindow() {
	s.mu.Lock()
	if s.win != nil {
		w := s.win
		quiet := s.quiet
		s.mu.Unlock()
		if quiet == "" {
			w.Restore()
			w.Show()
			w.Focus()
		} else if quiet == "inactive" {
			w.ShowInactive()
		}
		return
	}

	uiApp := ui.NewApp(s.backend, s.audioSinkForMode())
	if s.lastRoute != "" {
		uiApp.Navigate(s.lastRoute)
	}
	s.uiApp = uiApp

	uiSt := s.backend.GetUIState()
	width := uiSt.Window.Width
	height := uiSt.Window.Height
	if width < 720 {
		width = 1280
	}
	if height < 520 {
		height = 860
	}

	winOpts := mygo.WindowOptions{
		Title:           s.loc.DisplayName,
		Width:           width,
		Height:          height,
		MinWidth:        720,
		MinHeight:       520,
		Hidden:          s.quiet != "",
		AutoHideMenuBar: true,
		BackgroundColor: "#ffffff",
		Content:         mygoui.View(uiApp.View),
	}
	win := mygo.NewWindow(winOpts)
	uiApp.BindFullscreenHandler(win.SetFullScreen)
	if s.quiet == "hidden" {
		win.Hide()
	} else if s.quiet == "inactive" {
		win.ShowInactive()
	}

	uiApp.BindEventLoop(func(fn func()) {
		win.Update(fn)
	})

	win.OnClose(func(e *mygo.CloseEvent) {
		s.mu.Lock()
		quitting := s.quitting
		closingToTray := s.closingToTray
		s.mu.Unlock()
		if quitting || closingToTray {
			return
		}
		closeAction := s.backend.GetSettings().Window.CloseAction
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

	s.win = win
	s.closingToTray = false
	s.mu.Unlock()

	s.checkForegroundSafety()
}

func (s *Shell) checkForegroundSafety() {
	if s.quiet == "" {
		return
	}
	_, fgPID := GetForegroundWindowHandle()
	if fgPID != 0 && fgPID == uint32(os.Getpid()) {
		s.mu.Lock()
		s.stoleFocus = true
		s.mu.Unlock()
	}
}

// CloseToTray destroys the native UI window, releases bitmaps and UI subscriptions,
// and forces garbage collection + OS memory return so tray resident memory stays < 60 MB (DEC-030, INV-13, C35).
func (s *Shell) CloseToTray() {
	s.mu.Lock()
	if s.uiApp != nil {
		s.lastRoute = s.uiApp.CurrentRoute
		st := s.backend.GetUIState()
		st.LastRoute = s.lastRoute
		_, _ = s.backend.UpdateUIState(st)
		s.uiApp.Close()
		s.uiApp = nil
	}
	win := s.win
	s.win = nil
	s.closingToTray = true
	firstNotify := !s.notifiedTrayOnce
	s.notifiedTrayOnce = true
	quiet := s.quiet
	title := s.loc.DisplayName
	s.mu.Unlock()

	if win != nil {
		win.Close()
	}

	runtime.GC()
	debug.FreeOSMemory()
	TrimWorkingSet()

	// Only show first-time tray notification in interactive mode; quiet mode never sends notifications (§6.3).
	if firstNotify && quiet == "" && mygo.NotificationsSupported() {
		n := mygo.NewNotification(mygo.NotificationOptions{
			ID:     "trace-tray-hint",
			Title:  title,
			Body:   "窗口已销毁并最小化到托盘，后台任务继续运行。",
			Silent: true,
		})
		_ = n.Show()
	}
	s.checkForegroundSafety()
}

// RestoreFromTray recreates the native UI window and restores the previous route (DEC-030, C35).
func (s *Shell) RestoreFromTray() {
	s.openOrRestoreMainWindow()
	s.checkForegroundSafety()
}

// HandleSecondInstance handles command-line arguments forwarded from a second instance (C35, C36).
func (s *Shell) HandleSecondInstance(args []string) {
	s.mu.Lock()
	s.secondInstanceArgs = append([]string(nil), args...)
	s.mu.Unlock()

	s.openOrRestoreMainWindow()

	// If a Bilibili link/BV/UID was passed in args, parse it in the UI
	for _, arg := range args {
		trimmed := strings.TrimSpace(arg)
		if strings.HasPrefix(trimmed, "BV") || strings.HasPrefix(trimmed, "http") || strings.HasPrefix(strings.ToUpper(trimmed), "UID:") {
			s.mu.Lock()
			app := s.uiApp
			w := s.win
			s.mu.Unlock()
			if app != nil {
				app.SubmitParse(trimmed)
				if w != nil {
					w.Update(func() {})
				}
			}
			break
		}
	}
	s.checkForegroundSafety()
	if smokeDir := strings.TrimSpace(os.Getenv("TRACE_SMOKE_DIR")); smokeDir != "" {
		snap := s.Snapshot()
		if raw, err := json.MarshalIndent(snap, "", "  "); err == nil {
			_ = storage.WriteFileAtomic(filepath.Join(smokeDir, "second_instance.json"), raw, 0o600)
		}
	}
}

func (s *Shell) setupTray() {
	hasWork := s.hasActiveWork()
	iconBytes := RenderTrayIconPNG(hasWork)

	statusItem := &mygo.MenuItem{
		ID:       "status",
		Label:    s.trayStatusLabel(),
		Disabled: true,
	}
	s.statusMenuItem = statusItem

	menu := mygo.NewMenu([]*mygo.MenuItem{
		statusItem,
		mygo.Separator(),
		{
			ID:    "open",
			Label: "打开 " + s.loc.DisplayName,
			Click: func(*mygo.MenuItem, *mygo.Window) {
				s.RestoreFromTray()
			},
		},
		{
			ID:    "check_subs",
			Label: "立即检查全部订阅",
			Click: func(*mygo.MenuItem, *mygo.Window) {
				go func() {
					_, _ = s.backend.CheckSubscriptionsNow(context.Background(), 0)
				}()
			},
		},
		mygo.Separator(),
		{
			ID:    "quit",
			Label: "退出",
			Click: func(*mygo.MenuItem, *mygo.Window) {
				s.RequestQuit(false)
			},
		},
	})

	tray, err := mygo.NewTray(mygo.TrayOptions{
		ID:      s.loc.AppName + "-tray",
		Icon:    iconBytes,
		ToolTip: s.loc.DisplayName,
		Menu:    menu,
	})
	if err == nil {
		s.mu.Lock()
		s.tray = tray
		s.mu.Unlock()
	}

	s.unsubEvents = s.backend.SubscribeEvents(func(ev service.Event) {
		s.refreshTrayState()
	})
}

func (s *Shell) hasActiveWork() bool {
	if tasks, err := s.backend.ListDownloadTasks(); err == nil {
		for _, t := range tasks {
			if t.Status == "running" || t.Status == "queued" {
				return true
			}
		}
	}
	for _, r := range s.backend.ListLiveRooms() {
		if r.Recording {
			return true
		}
	}
	return false
}

func (s *Shell) trayStatusLabel() string {
	activeTasks := 0
	if tasks, err := s.backend.ListDownloadTasks(); err == nil {
		for _, t := range tasks {
			if t.Status == "running" || t.Status == "queued" {
				activeTasks++
			}
		}
	}
	activeRecs := 0
	for _, r := range s.backend.ListLiveRooms() {
		if r.Recording {
			activeRecs++
		}
	}
	if activeTasks == 0 && activeRecs == 0 {
		return s.loc.DisplayName + "：空闲运行中"
	}
	return fmt.Sprintf("%s：下载 %d · 录制 %d", s.loc.DisplayName, activeTasks, activeRecs)
}

func (s *Shell) refreshTrayState() {
	s.mu.Lock()
	tray := s.tray
	item := s.statusMenuItem
	s.mu.Unlock()
	if tray == nil {
		return
	}
	hasWork := s.hasActiveWork()
	_ = tray.SetIcon(RenderTrayIconPNG(hasWork), false)
	if item != nil {
		item.SetLabel(s.trayStatusLabel())
	}
}

// RequestQuit quits the application, optionally asking for confirmation if tasks/recordings are active in interactive mode (P13, C35).
func (s *Shell) RequestQuit(force bool) {
	if !force && s.quiet == "" && s.hasActiveWork() {
		go func() {
			res, err := mygo.Dialog.Message(mygo.MessageOptions{
				Type:          mygo.MessageWarning,
				Title:         "确认退出 " + s.loc.DisplayName,
				Message:       "当前仍有正在运行的缓存任务或直播录制，确定要退出吗？",
				Detail:        "退出前会自动暂停任务并安全收尾正在录制的分段。",
				Buttons:       []string{"取消", "确认退出"},
				DefaultButton: 0,
				CancelButton:  0,
			})
			if err == nil && res.Button == 1 {
				mygo.RunOnMain(func() {
					s.performQuit()
				})
			}
		}()
		return
	}
	s.performQuit()
}

func (s *Shell) performQuit() {
	s.mu.Lock()
	s.quitting = true
	if s.unsubEvents != nil {
		s.unsubEvents()
		s.unsubEvents = nil
	}
	if s.uiApp != nil {
		s.uiApp.Close()
		s.uiApp = nil
	}
	if s.tray != nil {
		s.tray.Destroy()
		s.tray = nil
	}
	s.mu.Unlock()

	s.backend.PauseAllActiveWork()
	mygo.App.Quit()
}

// Snapshot captures the current runtime state of the shell for testing and smoke assertions.
func (s *Shell) Snapshot() RuntimeStateSnapshot {
	s.checkForegroundSafety()
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
	s.mu.Unlock()

	return RuntimeStateSnapshot{
		PID:                    pid,
		AppName:                s.loc.AppName,
		DisplayName:            s.loc.DisplayName,
		Channel:                s.loc.Channel,
		Mode:                   s.loc.Mode,
		QuietMode:              s.quiet,
		DataDir:                s.loc.DataDir,
		CacheDir:               s.backend.LibraryStore().CacheDir(),
		WindowOpen:             winOpen,
		TrayActive:             trayActive,
		ActiveRoute:            route,
		WorkingSetBytes:        ws,
		PrivateWorkingSetBytes: privWS,
		PrivateBytes:           priv,
		WebView2ProcessCount:   wv2Count,
		ForegroundPID:          fgPID,
		StealFocusDetected:     stole,
		SecondInstanceArgs:     args,
		Timestamp:              time.Now().UTC().Format(time.RFC3339),
	}
}

func (s *Shell) runSmokeControlLoop(smokeDir string) {
	_ = os.MkdirAll(smokeDir, 0o700)
	writeSnap := func(name string) {
		snap := s.Snapshot()
		if raw, err := json.MarshalIndent(snap, "", "  "); err == nil {
			_ = storage.WriteFileAtomic(filepath.Join(smokeDir, name), raw, 0o600)
		}
	}

	writeSnap("ready.json")
	cmdFile := filepath.Join(smokeDir, "command.txt")
	lastCmd := ""

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		s.mu.Lock()
		quitting := s.quitting
		s.mu.Unlock()
		if quitting {
			return
		}
		raw, err := os.ReadFile(cmdFile)
		if err != nil {
			continue
		}
		cmd := strings.TrimSpace(string(raw))
		if cmd == "" || cmd == lastCmd {
			continue
		}
		lastCmd = cmd

		switch cmd {
		case "close_to_tray":
			done := make(chan struct{})
			mygo.RunOnMain(func() {
				s.CloseToTray()
				close(done)
			})
			<-done
			// CloseToTray already collects and trims; measure after it settles.
			time.Sleep(2 * time.Second)
			writeSnap("tray_state.json")

		case "restore_window":
			done := make(chan struct{})
			mygo.RunOnMain(func() {
				s.RestoreFromTray()
				close(done)
			})
			<-done
			time.Sleep(50 * time.Millisecond)
			writeSnap("restored_state.json")

		case "snapshot":
			writeSnap("snapshot.json")

		case "quit":
			writeSnap("final_state.json")
			mygo.RunOnMain(func() {
				s.RequestQuit(true)
			})
			return
		}
	}
}
