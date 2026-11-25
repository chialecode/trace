package shell

import (
	"bytes"
	"database/sql"
	"image"
	"image/png"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo"
)

func decodeTrayIcon(t *testing.T, badge, active bool) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(RenderTrayIconPNG(badge, active)))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != trayIconSize || img.Bounds().Dy() != trayIconSize {
		t.Fatalf("tray icon is %v, want %dx%d", img.Bounds(), trayIconSize, trayIconSize)
	}
	return img
}

// amberIn reports whether the rectangle holds an amber pixel (#f3a115 within
// antialiasing): the lower-right DEV badge or the upper-right corner.
func amberIn(img image.Image, r image.Rectangle) bool {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			cr, cg, cb, _ := img.At(x, y).RGBA()
			if cr>>8 > 0xd0 && cg>>8 > 0x80 && cg>>8 < 0xc0 && cb>>8 < 0x50 {
				return true
			}
		}
	}
	return false
}

// The "DEV" badge follows the channel only, as the application icon does, and
// the tray icon stays static without an upper-right amber activity dot even
// while cache tasks or live recordings run.
func TestTrayIconBadgeFollowsChannelAndNeverDrawsActivityDot(t *testing.T) {
	for ch, want := range map[string]bool{"dev": true, "ptest": true, "stable": false, "": false} {
		if got := trayBadge(ch); got != want {
			t.Errorf("trayBadge(%q) = %v, want %v", ch, got, want)
		}
	}
	badgeCorner := image.Rect(17, 24, 31, 31)
	activityCorner := image.Rect(19, 1, 31, 12)
	for _, badge := range []bool{false, true} {
		idlePNG := RenderTrayIconPNG(badge, false)
		activePNG := RenderTrayIconPNG(badge, true)
		if !bytes.Equal(idlePNG, activePNG) {
			t.Fatalf("badge=%v: expected static tray icon PNG whether work runs or not", badge)
		}
		idle := decodeTrayIcon(t, badge, false)
		active := decodeTrayIcon(t, badge, true)
		for _, img := range []image.Image{idle, active} {
			if got := amberIn(img, badgeCorner); got != badge {
				t.Errorf("badge=%v: amber in the lower right = %v, want %v", badge, got, badge)
			}
			if amberIn(img, activityCorner) {
				t.Errorf("badge=%v: unexpected amber activity dot in the upper right", badge)
			}
		}
	}
}

func TestTrayTooltipIsPlainDisplayNameAndQuitStillConfirmsActiveWork(t *testing.T) {
	for _, displayName := range []string{"TRACE", "TRACE Dev"} {
		for _, counts := range [][2]int{{0, 0}, {3, 0}, {0, 2}, {3, 1}} {
			got := formatTrayStatusLabel(displayName, counts[0], counts[1])
			if got != displayName {
				t.Fatalf("formatTrayStatusLabel(%q, %d, %d) = %q, want %q", displayName, counts[0], counts[1], got, displayName)
			}
		}
	}

	loc, b := newTestShellBackend(t, "dev")
	db, err := sql.Open("sqlite", filepath.Join(loc.DataDir, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO download_tasks (task_id, site, kind, target_id, status, created_at, updated_at) VALUES ('t_active','bilibili','video','BV1','running',?,?)`, now, now); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	_ = db.Close()

	s := NewShell(loc, b)
	s.quiet = "" // interactive mode
	tasks, recs := s.activeWorkCounts()
	if tasks != 1 || recs != 0 || !s.hasActiveWork() {
		t.Fatalf("expected active work (1, 0), got (%d, %d)", tasks, recs)
	}

	// Tray refresh keeps plain DisplayName tooltip even when tasks run.
	s.tray = &mygo.Tray{}
	s.trayHasState = true
	s.trayLastTooltip = "TRACE Dev"
	dispatched := make(chan struct{}, 1)
	s.trayDispatchMain = func(fn func()) {
		dispatched <- struct{}{}
	}
	s.refreshTrayState()
	select {
	case <-dispatched:
		t.Fatal("expected static plain tooltip not to re-dispatch unnecessary tray update")
	default:
	}
	if s.trayLastTooltip != "TRACE Dev" {
		t.Fatalf("tray tooltip = %q, want plain %q", s.trayLastTooltip, "TRACE Dev")
	}

	// Quit confirmation still triggers in interactive mode when active work exists.
	prompted := make(chan mygo.MessageOptions, 1)
	s.confirmQuitDialog = func(opts mygo.MessageOptions) (mygo.MessageResult, error) {
		prompted <- opts
		return mygo.MessageResult{Button: 0}, nil // cancel quit
	}
	s.RequestQuit(false)
	select {
	case opts := <-prompted:
		if opts.Title != "确认退出 TRACE Dev" {
			t.Fatalf("quit confirm title = %q, want %q", opts.Title, "确认退出 TRACE Dev")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RequestQuit(false) did not prompt confirmation when active work exists")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		confirmOpen := s.quitConfirmOpen
		quitting := s.quitting
		s.mu.Unlock()
		if !confirmOpen {
			if quitting {
				t.Fatal("cancelling quit confirmation must not quit while active work runs")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("quitConfirmOpen was not cleared after confirmation dialog returned")
}
