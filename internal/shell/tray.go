package shell

import (
	"bytes"
	"fmt"
	"sync"

	"github.com/chialecode/trace/internal/brandicon"
	"github.com/chialecode/trace/internal/service"
	"github.com/egoist/mygo"
)

// trayIconSize is the size the tray icon is drawn at; Windows scales it to
// the notification area.
const trayIconSize = 32

var (
	trayIconOnce sync.Once
	// trayIcons is indexed by trayIconIndex.
	trayIcons [4][]byte
)

func trayIconIndex(badge, hasActiveWork bool) int {
	i := 0
	if badge {
		i += 2
	}
	if hasActiveWork {
		i++
	}
	return i
}

// RenderTrayIconPNG returns the cached 32x32 PNG tray icon: the application
// icon's mark (internal/brandicon), so the development and test identities
// show the same amber "DEV" badge in the lower right as their application
// icon (badge), with an amber dot in the upper right while cache tasks or
// live recordings run.
func RenderTrayIconPNG(badge, hasActiveWork bool) []byte {
	trayIconOnce.Do(func() {
		for _, b := range []bool{false, true} {
			for _, w := range []bool{false, true} {
				data, err := brandicon.PNG(trayIconSize, brandicon.Options{Badge: b, Activity: w})
				if err != nil {
					panic(fmt.Sprintf("tray icon: %v", err)) // encoding an in-memory RGBA image does not fail
				}
				trayIcons[trayIconIndex(b, w)] = data
			}
		}
	})
	return bytes.Clone(trayIcons[trayIconIndex(badge, hasActiveWork)])
}

// trayBadge reports whether the channel's tray icon carries the "DEV" badge:
// the development and test identities, whose application icon has it too.
func trayBadge(channel string) bool {
	return channel == "dev" || channel == "ptest"
}

// trayMenuEntry is one item of the tray menu.
type trayMenuEntry struct {
	label string
	run   func()
}

// trayMenuEntries is the whole tray menu: open and quit.
// The status shows in the icon's tooltip instead.
func (s *Shell) trayMenuEntries() []trayMenuEntry {
	return []trayMenuEntry{
		{label: "打开 " + s.loc.DisplayName, run: s.RestoreFromTray},
		{label: "退出", run: func() { s.RequestQuit(false) }},
	}
}

func (s *Shell) handleTrayClick() {
	if s.quiet != "" {
		return
	}
	s.RestoreFromTray()
}

func (s *Shell) handleTrayRightClick(show func([]trayMenuEntry)) {
	if s.quiet != "" || show == nil {
		return
	}
	show(s.trayMenuEntries())
}

func formatTrayStatusLabel(displayName string, activeTasks, activeRecs int) string {
	if activeTasks == 0 && activeRecs == 0 {
		return displayName
	}
	return fmt.Sprintf("%s · 正在缓存 %d 项 · 录制 %d 场", displayName, activeTasks, activeRecs)
}

func (s *Shell) activeWorkCounts() (int, int) {
	if s.backend == nil {
		return 0, 0
	}
	return s.backend.ActiveWork()
}

func (s *Shell) hasActiveWork() bool {
	tasks, recs := s.activeWorkCounts()
	return tasks > 0 || recs > 0
}

func (s *Shell) setupTray() {
	tasks, recs := s.activeWorkCounts()
	hasWork := tasks > 0 || recs > 0
	tooltip := formatTrayStatusLabel(s.loc.DisplayName, tasks, recs)
	iconBytes := RenderTrayIconPNG(trayBadge(s.loc.Channel), hasWork)

	opts := mygo.TrayOptions{
		ID:      s.loc.AppName + "-tray",
		Icon:    iconBytes,
		ToolTip: tooltip,
	}
	// Quiet mode never attaches interactive tray menus or click handlers so
	// stray taskbar clicks during automated tests cannot pop up a menu or
	// call SetForegroundWindow.
	if s.quiet == "" && !nativeTrayMenu {
		var items []*mygo.MenuItem
		for _, e := range s.trayMenuEntries() {
			run := e.run
			items = append(items, &mygo.MenuItem{Label: e.label, Click: func(*mygo.MenuItem, *mygo.Window) { run() }})
		}
		opts.Menu = mygo.NewMenu(items)
	}
	tray, err := mygo.NewTray(opts)
	if err == nil {
		if s.quiet == "" {
			tray.OnClick(s.handleTrayClick)
			if nativeTrayMenu {
				tray.OnRightClick(func() { s.handleTrayRightClick(popUpTrayMenu) })
			}
		}
		s.mu.Lock()
		s.tray = tray
		s.trayLastHasWork = hasWork
		s.trayLastTooltip = tooltip
		s.trayHasState = true
		s.mu.Unlock()
	}

	s.unsubEvents = s.backend.SubscribeEvents(func(service.Event) {
		s.scheduleTrayRefresh()
	})
}

// scheduleTrayRefresh refreshes the tray off the publisher's goroutine.
// At most one refresh executes at a time; bursts arriving during an active
// refresh coalesce into a single follow-up refresh.
func (s *Shell) scheduleTrayRefresh() {
	s.trayMu.Lock()
	if s.trayStopped {
		s.trayMu.Unlock()
		return
	}
	if s.trayRunning {
		s.trayDirty = true
		s.trayMu.Unlock()
		return
	}
	s.trayRunning = true
	s.trayDirty = false
	s.trayWg.Add(1)
	s.trayMu.Unlock()

	go s.runTrayRefreshLoop()
}

func (s *Shell) runTrayRefreshLoop() {
	defer s.trayWg.Done()
	for {
		fn := s.trayRefresh
		if fn != nil {
			fn()
		} else {
			s.refreshTrayState()
		}
		s.trayMu.Lock()
		if !s.trayDirty || s.trayStopped {
			s.trayRunning = false
			s.trayDirty = false
			s.trayMu.Unlock()
			return
		}
		s.trayDirty = false
		s.trayMu.Unlock()
	}
}

// markTrayStopped prevents new tray refreshes without blocking the UI thread.
func (s *Shell) markTrayStopped() {
	s.trayMu.Lock()
	s.trayStopped = true
	s.trayDirty = false
	s.trayMu.Unlock()
}

// stopTrayRefresh marks the tray stopped and waits for any in-flight refresh
// goroutine to exit. Call after the event loop stops (or off the main thread).
func (s *Shell) stopTrayRefresh() {
	s.markTrayStopped()
	s.trayWg.Wait()
}

func (s *Shell) refreshTrayState() {
	s.mu.Lock()
	hasTray := s.tray != nil
	quitting := s.quitting
	displayName := s.loc.DisplayName
	s.mu.Unlock()
	if !hasTray || quitting {
		return
	}

	// Query backend state off-lock while still tracked under s.trayWg so
	// stopTrayRefresh() drains all backend reads before backend.Close().
	tasks, recs := s.activeWorkCounts()
	hasWork := tasks > 0 || recs > 0
	tooltip := formatTrayStatusLabel(displayName, tasks, recs)

	s.mu.Lock()
	if s.tray == nil || s.quitting {
		s.mu.Unlock()
		return
	}
	if s.trayHasState && !s.trayWantDirty && s.trayLastHasWork == hasWork && s.trayLastTooltip == tooltip {
		s.mu.Unlock()
		return
	}
	s.trayWantHasWork = hasWork
	s.trayWantTooltip = tooltip
	s.trayWantDirty = true
	if s.trayApplyQueued {
		s.mu.Unlock()
		return
	}
	s.trayApplyQueued = true
	dispatch := s.trayDispatchMain
	if dispatch == nil {
		dispatch = mygo.RunOnMain
	}
	s.mu.Unlock()

	// Dispatch main-thread tray mutation asynchronously outside s.trayWg:
	// MyGo fires OnQuit on the main thread before loop.shutdown() closes the
	// RunOnMain queue, so waiting on RunOnMain inside s.trayWg would deadlock
	// OnQuit -> stopTrayRefresh().
	go dispatch(s.applyTrayStateOnMain)
}

func (s *Shell) applyTrayStateOnMain() {
	for {
		s.mu.Lock()
		tray := s.tray
		if tray == nil || s.quitting {
			s.trayApplyQueued = false
			s.trayWantDirty = false
			s.mu.Unlock()
			return
		}
		if !s.trayWantDirty {
			s.trayApplyQueued = false
			s.mu.Unlock()
			return
		}
		hasWork := s.trayWantHasWork
		tooltip := s.trayWantTooltip
		s.trayWantDirty = false
		iconChanged := !s.trayHasState || s.trayLastHasWork != hasWork
		tooltipChanged := !s.trayHasState || s.trayLastTooltip != tooltip
		s.trayHasState = true
		s.trayLastHasWork = hasWork
		s.trayLastTooltip = tooltip
		s.mu.Unlock()

		if iconChanged {
			_ = tray.SetIcon(RenderTrayIconPNG(trayBadge(s.loc.Channel), hasWork), false)
		}
		if tooltipChanged {
			tray.SetToolTip(tooltip)
		}
	}
}
