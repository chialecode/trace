package player

import (
	"fmt"
	"testing"
)

func TestDanmakuDensityFiltersStableTimelineEntries(t *testing.T) {
	var items []DanmakuItem
	for i := 0; i < 30; i++ {
		items = append(items, DanmakuItem{ID: fmt.Sprintf("comment-%02d", i), TimeSec: float64(i) * 0.01, Mode: 1, Text: "合成弹幕"})
	}
	engine := NewDanmakuEngine(items)
	previous := 0
	for _, density := range []string{"low", "medium", "high", "unlimited"} {
		atStart := engine.LayoutFrame(0.9, 1280, 1080, 1, 1, density)
		if len(atStart) <= previous {
			t.Fatalf("density %s did not increase visible comments: %d <= %d", density, len(atStart), previous)
		}
		previous = len(atStart)
		atNextFrame := engine.LayoutFrame(1, 1280, 1080, 1, 1, density)
		if len(atNextFrame) != len(atStart) {
			t.Fatal("density selection changed despite no comments entering or leaving the timeline")
		}
		for i := range atStart {
			if atStart[i].ID != atNextFrame[i].ID {
				t.Fatal("density caused comments to flicker between adjacent frames")
			}
		}
		if stale := engine.LayoutFrame(20, 1280, 1080, 1, 1, density); len(stale) != 0 {
			t.Fatal("seek retained old comments")
		}
		seekBack := engine.LayoutFrame(0.9, 1280, 1080, 1, 1, density)
		if len(seekBack) != len(atStart) {
			t.Fatal("seek back lost density-filtered comments")
		}
	}
}
