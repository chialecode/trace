package ui

import (
	"errors"
	"math"
	"testing"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/uistate"
	"github.com/egoist/mygo/ui"
)

type feedFaultBackend struct {
	service.BackendAPI
	feedErr       error
	stateErr      error
	anchorErr     error
	statisticsErr error
	statsCalls    int
	feedCalls     int
}

func (b *feedFaultBackend) FeedPage(req library.FeedPageRequest) (library.FeedPageResult, error) {
	b.feedCalls++
	if b.feedErr != nil {
		return library.FeedPageResult{}, b.feedErr
	}
	return b.BackendAPI.FeedPage(req)
}

func TestHomePaginationFailureWaitsForExplicitRetry(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 140)
	backend := &feedFaultBackend{BackendAPI: app.backend, feedErr: errors.New("synthetic pagination read failed")}
	app.backend = backend
	before := len(app.FeedItems)
	app.loadFeedEdge(false)
	initialCalls := backend.feedCalls
	tt := ui.NewTester(app.View, 1000, 860)
	for i := 0; i < 8; i++ {
		tt.Frame()
	}
	if app.FeedError == "" || backend.feedCalls != initialCalls || len(app.FeedItems) != before {
		t.Fatal("a failed page was hidden, discarded items or retried automatically")
	}
	backend.feedErr = nil
	app.openHomeActorFeed(HomeUpdatedActor{Key: "mid:1001", Mid: 1001, Name: "synthetic author", Authors: []int64{1001}, LatestTs: 140})
	app.closeHomeActorFeed()
	if app.FeedError == "" {
		t.Fatal("returning from an actor view discarded the main pagination failure")
	}
	tt.Frame()
	if err := tt.Click("重试主页分页"); err != nil {
		t.Fatalf("pagination failure does not offer a visible retry action: %v", err)
	}
	if app.FeedError != "" || len(app.FeedItems) <= before {
		t.Fatal("explicit retry failed to append the next page")
	}
	seen := make(map[string]bool)
	for _, entry := range app.FeedItems {
		if seen[entry.FeedKey] {
			t.Fatal("retried pagination duplicated an existing item")
		}
		seen[entry.FeedKey] = true
	}
}

// Reloading the newest posts after a read failure keeps the list and the
// scroll position the user is looking at.
func TestHomeLatestFailureKeepsVisiblePosition(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 100)
	tt := ui.NewTester(app.View, 1000, 860)
	viewport, found := tt.Find("主页信息流列表")
	if !found {
		t.Fatal("feed viewport missing")
	}
	tt.Scroll(viewport.X+viewport.W/2, viewport.Y+100, 0, 541)
	tt.Frame()
	first, _ := app.FeedListState.Visible()
	key, offset, count := app.FeedItems[first].FeedKey, app.FeedScroll.Y, len(app.FeedItems)
	app.backend = &feedFaultBackend{BackendAPI: app.backend, feedErr: errors.New("synthetic latest read failure")}
	app.BannerError = ""
	app.showLatestFeed()
	tt.Frame()
	after, _ := app.FeedListState.Visible()
	if app.BannerError == "" || len(app.FeedItems) != count || app.FeedItems[after].FeedKey != key || app.FeedScroll.Y != offset {
		t.Fatal("a failed reload hid the error or moved the timeline")
	}
}

func (b *feedFaultBackend) UpdateUIState(st uistate.State) (uistate.State, error) {
	if b.stateErr != nil {
		return uistate.State{}, b.stateErr
	}
	return b.BackendAPI.UpdateUIState(st)
}

func (b *feedFaultBackend) SaveAnchor(anchor uistate.FeedAnchor) error {
	if b.anchorErr != nil {
		return b.anchorErr
	}
	return b.BackendAPI.SaveAnchor(anchor)
}

func (b *feedFaultBackend) LibraryStatistics() (library.Statistics, error) {
	b.statsCalls++
	if b.statisticsErr != nil {
		return library.Statistics{}, b.statisticsErr
	}
	return b.BackendAPI.LibraryStatistics()
}

func TestHomeActorReadFailureKeepsMainTimelineAndSeenState(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 80)
	actor := HomeUpdatedActor{Key: "mid:1001", Mid: 1001, Name: "synthetic author", Authors: []int64{1001}, LatestTs: 80}
	before := append([]library.FeedEntry(nil), app.FeedItems...)
	backend := &feedFaultBackend{BackendAPI: app.backend, feedErr: errors.New("synthetic feed read failed")}
	app.backend = backend
	app.openHomeActorFeed(actor)
	if app.HomeActorFilter != nil || len(app.FeedItems) != len(before) || app.FeedItems[0].FeedKey != before[0].FeedKey {
		t.Fatal("a failed actor query replaced the main timeline with a partial fallback")
	}
	if app.BannerError == "" {
		t.Fatal("actor query failure was hidden")
	}
	if seen := app.backend.GetUIState().HomeSeen[actor.Key]; seen != 0 {
		t.Fatalf("an actor was marked seen without successfully loading its feed: %d", seen)
	}
}

// Within a session, leaving one person's posts returns to the same place in
// the main timeline.
func TestHomeActorReturnRestoresRowAndPixelOffset(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 110)
	tt := ui.NewTester(app.View, 1000, 860)
	viewport, found := tt.Find("主页信息流列表")
	if !found {
		t.Fatal("feed viewport missing")
	}
	tt.Scroll(viewport.X+viewport.W/2, viewport.Y+100, 0, 541)
	tt.Frame()
	first, _ := app.FeedListState.Visible()
	key, scrollY := app.FeedItems[first].FeedKey, app.FeedScroll.Y
	if scrollY <= 0 {
		t.Fatal("fixture must scroll the timeline")
	}
	app.openHomeActorFeed(HomeUpdatedActor{Key: "mid:1001", Mid: 1001, Name: "synthetic author", Authors: []int64{1001}, LatestTs: 110})
	tt.Frame()
	app.closeHomeActorFeed()
	for i := 0; i < 6; i++ {
		tt.Frame()
	}
	first, _ = app.FeedListState.Visible()
	if first < 0 || first >= len(app.FeedItems) || app.FeedItems[first].FeedKey != key || math.Abs(float64(app.FeedScroll.Y-scrollY)) > 2 {
		t.Fatalf("return from actor feed lost the viewport: want %s at %v, got first=%d at %v", key, scrollY, first, app.FeedScroll.Y)
	}
}

func TestHomePersistenceFailuresAreVisibleAndKeepOldState(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 80)
	backend := &feedFaultBackend{BackendAPI: app.backend, anchorErr: errors.New("synthetic anchor write failed")}
	app.backend = backend
	before := app.backend.Anchor()
	app.homeSeenTs = 0
	app.BannerError = ""
	tt := ui.NewTester(app.View, 1000, 860)
	tt.Frame()
	if app.BannerError == "" || app.backend.Anchor() != before {
		t.Fatal("seen-time write failure was hidden or changed the committed value")
	}
	backend.anchorErr = nil
	backend.stateErr = errors.New("synthetic seen timestamp write failed")
	app.BannerError = ""
	actor := HomeUpdatedActor{Key: "mid:1001", Mid: 1001, Name: "synthetic author", Authors: []int64{1001}, LatestTs: 80}
	app.openHomeActorFeed(actor)
	if app.BannerError == "" || app.backend.GetUIState().HomeSeen[actor.Key] != 0 {
		t.Fatal("seen timestamp write failure was hidden or changed committed state")
	}
	app.Navigate(RouteParse)
	if app.BannerError == "" || app.backend.GetUIState().LastRoute == RouteParse {
		t.Fatal("route write failure was hidden or changed committed state")
	}
}

func TestHomeStatisticsReadsAreIndependentOfRedraws(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 80)
	backend := &feedFaultBackend{BackendAPI: app.backend}
	app.backend = backend
	app.RefreshData()
	initialCalls := backend.statsCalls
	tt := ui.NewTester(app.View, 1000, 860)
	for i := 0; i < 15; i++ {
		tt.Frame()
	}
	if initialCalls != 1 || backend.statsCalls != initialCalls {
		t.Fatalf("home redraws repeatedly queried SQLite statistics: refresh=%d total=%d", initialCalls, backend.statsCalls)
	}
	backend.statisticsErr = errors.New("synthetic statistics read failed")
	previous := app.libraryStatistics.Items
	app.handleBackendEvent(service.Event{Type: service.EventLibraryChanged})
	if app.BannerError == "" || app.libraryStatistics.Items != previous {
		t.Fatal("statistics read failure was displayed as an empty avatar row")
	}
}
