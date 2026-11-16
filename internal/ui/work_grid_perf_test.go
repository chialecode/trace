package ui

import (
	"fmt"
	"testing"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/egoist/mygo/ui"
)

type libraryReadCounter struct {
	*douyinStubBackend
	stateKeys, itemReads int
}

func (s *libraryReadCounter) States(keys []string) map[string]library.DotState {
	s.stateKeys += len(keys)
	return s.douyinStubBackend.States(keys)
}

func (s *libraryReadCounter) GetLibraryItem(key string) (library.ItemMetadata, bool, error) {
	s.itemReads++
	return s.douyinStubBackend.GetLibraryItem(key)
}

// A Douyin user page with hundreds of works loaded (after 缓存更早的内容 or
// long scrolling) must not read the library on every frame: that made it
// stutter (2026-11-15 feedback). A state change re-reads only what is shown.
func TestDouyinWorkGridDoesNotReadTheLibraryEachFrame(t *testing.T) {
	app, stub, _, cleanup := newDouyinUITestHarness(t)
	defer cleanup()
	counter := &libraryReadCounter{douyinStubBackend: stub}
	app.backend = counter

	app.CurrentDouyinUser = &service.DouyinUserView{SecUID: "MS4wLjABAAAA_synth_grid_user_000001", Nickname: "网格作者"}
	for i := 0; i < 300; i++ {
		id := fmt.Sprintf("73000000000000%05d", i)
		app.CurrentDouyinWorks = append(app.CurrentDouyinWorks, service.DouyinWorkCardDTO{
			ID: id, Kind: "video", Title: "作品 " + id, CacheKey: "douyin:work:" + id,
		})
	}
	app.CurrentRoute = RouteDouyinUser
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	tt.Frame()
	// The list lays out a first batch of rows before it knows their heights.
	if counter.stateKeys == 0 || counter.stateKeys >= 300 {
		t.Fatalf("first frames asked for %d states; want only the rows laid out", counter.stateKeys)
	}
	counter.stateKeys, counter.itemReads = 0, 0
	for range 5 {
		tt.Frame()
	}
	if counter.stateKeys != 0 || counter.itemReads != 0 {
		t.Fatalf("steady frames read the library: states=%d items=%d", counter.stateKeys, counter.itemReads)
	}
	app.cacheStateRev++ // a task or the library changed
	tt.Frame()
	if counter.stateKeys == 0 || counter.stateKeys > 80 {
		t.Fatalf("after a change asked for %d states; want only the visible cards again", counter.stateKeys)
	}
}
