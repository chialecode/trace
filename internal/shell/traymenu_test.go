package shell

import (
	"testing"

	"github.com/chialecode/trace/internal/appdirs"
)

// The tray menu only opens the window and quits (2026-11-15 feedback); the
// status lives in the tooltip.
func TestTrayMenuOnlyOpensAndQuits(t *testing.T) {
	s := &Shell{loc: appdirs.Locations{DisplayName: "TRACE Dev"}}
	entries := s.trayMenuEntries()
	if len(entries) != 2 || entries[0].label != "打开 TRACE Dev" || entries[1].label != "退出" {
		t.Fatalf("tray menu = %+v", entries)
	}
	for _, e := range entries {
		if e.run == nil {
			t.Fatalf("%q does nothing", e.label)
		}
	}
}

// Quiet mode suppresses tray left-click restore and right-click popup menus so
// automated runs never open a modal Win32 menu or call SetForegroundWindow.
func TestQuietModeSuppressesTrayClickAndMenu(t *testing.T) {
	for _, quiet := range []string{"hidden", "inactive"} {
		s := &Shell{
			loc:   appdirs.Locations{DisplayName: "TRACE Dev"},
			quiet: quiet,
		}
		called := false
		s.handleTrayRightClick(func([]trayMenuEntry) {
			called = true
		})
		if called {
			t.Fatalf("quiet=%q: expected tray right-click menu to be suppressed", quiet)
		}
		// Must be a no-op without a backend or window initialized.
		s.handleTrayClick()
	}

	interactive := &Shell{
		loc:   appdirs.Locations{DisplayName: "TRACE Dev"},
		quiet: "",
	}
	var got []trayMenuEntry
	interactive.handleTrayRightClick(func(entries []trayMenuEntry) {
		got = entries
	})
	if len(got) != 2 || got[0].label != "打开 TRACE Dev" || got[1].label != "退出" {
		t.Fatalf("interactive tray right-click entries = %+v", got)
	}
}
