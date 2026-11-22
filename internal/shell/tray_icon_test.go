package shell

import (
	"bytes"
	"image"
	"image/png"
	"testing"
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
// antialiasing): the badge or the activity dot.
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

// The "DEV" badge follows the channel only, as the application icon does;
// the activity dot never paints the badge's corner, so running work cannot
// look like (or hide) the badge.
func TestTrayIconBadgeFollowsChannelAndActivityStaysOutOfItsCorner(t *testing.T) {
	for ch, want := range map[string]bool{"dev": true, "ptest": true, "stable": false, "": false} {
		if got := trayBadge(ch); got != want {
			t.Errorf("trayBadge(%q) = %v, want %v", ch, got, want)
		}
	}
	badgeCorner := image.Rect(17, 24, 31, 31)
	activityCorner := image.Rect(19, 1, 31, 12)
	for _, badge := range []bool{false, true} {
		idle := decodeTrayIcon(t, badge, false)
		active := decodeTrayIcon(t, badge, true)
		for _, img := range []image.Image{idle, active} {
			if got := amberIn(img, badgeCorner); got != badge {
				t.Errorf("badge=%v: amber in the lower right = %v", badge, got)
			}
		}
		if amberIn(idle, activityCorner) || !amberIn(active, activityCorner) {
			t.Errorf("badge=%v: the activity dot shows only while work runs", badge)
		}
		for y := badgeCorner.Min.Y; y < badgeCorner.Max.Y; y++ {
			for x := badgeCorner.Min.X; x < badgeCorner.Max.X; x++ {
				if idle.At(x, y) != active.At(x, y) {
					t.Fatalf("badge=%v: activity changed pixel (%d,%d) of the badge corner", badge, x, y)
				}
			}
		}
	}
}
