package ui

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/player"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/chialecode/trace/internal/uistate"
	"github.com/egoist/mygo/ui"
)

// seedMixedFeed caches bili Bilibili and dy Douyin posts, interleaved in
// time, without navigating.
func seedMixedFeed(t *testing.T, app *App, bili, dy int) {
	t.Helper()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	lib := app.backend.(*service.Backend).LibraryStore()
	for i := 0; i < bili; i++ {
		id := fmt.Sprintf("%d", 100000+i)
		mid := int64(1001 + i%3)
		if _, err := lib.SaveItem(filepath.Join(lib.CacheDir(), "dynamic", id), library.ItemMetadata{Site: "bilibili", Kind: "dynamic", ID: id, Title: "synthetic bili post " + id, Text: "cached post for the reading position", PubTs: int64(1000 + 2*i), Author: library.ItemAuthor{Mid: mid, Name: fmt.Sprintf("bili author %d", mid)}}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < dy; i++ {
		id := fmt.Sprintf("7%05d", i)
		if _, err := lib.SaveItem(filepath.Join(lib.CacheDir(), "douyin", id), library.ItemMetadata{Site: "douyin", Kind: "dynamic", ID: id, RawID: id, Title: "synthetic douyin post " + id, Text: "cached douyin post", PubTs: int64(1001 + 2*i), Author: library.ItemAuthor{Site: "douyin", UID: "MS4wLjABAAAA_synth_dy_1", Name: "dy author"}}); err != nil {
			t.Fatal(err)
		}
	}
	app.CompleteLogin()
}

// homeTop returns the post at the top of the timeline and how far its top
// is above the list's, as the last frame laid them out. The tester's rects
// are clipped to the list, so the offset is the one the timeline measured
// from the rows' bounds, checked against where the next row shows.
type homeAt struct {
	key        string
	off, place int
}

// same tells whether two places show the same post at the same offset.
func (h homeAt) same(o homeAt) bool {
	return h.key == o.key && abs(h.off-o.off) <= 1 && abs(h.place-o.place) <= 1
}

func homeTop(t *testing.T, app *App, tt *ui.Tester) homeAt {
	t.Helper()
	tt.Frame()
	first, last := app.FeedListState.Visible()
	if first < 0 || last <= first {
		t.Fatal("the timeline shows too few rows")
	}
	key := app.FeedItems[first].FeedKey
	if app.homePosSeen.Key != key {
		t.Fatalf("the timeline measured %q, the top is %q", app.homePosSeen.Key, key)
	}
	next, ok := tt.Find("信息流行 " + app.FeedItems[first+1].FeedKey)
	list, ok2 := tt.Find("主页信息流列表")
	if !ok || !ok2 {
		t.Fatal("rows or list not found")
	}
	return homeAt{key, app.homePosSeen.OffsetPx, int(math.Round(float64(next.Y - list.Y)))}
}

func settle(tt *ui.Tester, frames int) {
	for i := 0; i < frames; i++ {
		tt.Frame()
	}
}

// scrollHomeTo scrolls the main timeline to row index, then extra px down.
func scrollHomeTo(app *App, tt *ui.Tester, index int, extra float32) {
	app.FeedListState.ScrollTo(index, ui.Start)
	settle(tt, 3)
	if extra != 0 {
		list, _ := tt.Find("主页信息流列表")
		tt.Scroll(list.X+list.W/2, list.Y+120, 0, extra)
		settle(tt, 2)
	}
}

// Each site filter keeps its reading position; a new session opens each filter
// there the first time it shows, newer posts load above without moving the rows
// in view, a deleted post gives way to the nearest older one, and a
// subscription's feed keeps no position.
func TestHomeRestoresReadingPositionPerSiteFilter(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedMixedFeed(t, app, 90, 30)
	app.Navigate(RouteHome)
	tt := ui.NewTester(app.View, 1000, 860)
	settle(tt, 2)

	scrollHomeTo(app, tt, 40, 37)
	allAt := homeTop(t, app, tt)
	allKey, allOffset := allAt.key, allAt.off
	if allOffset != 37 {
		t.Fatalf("expected the top row scrolled 37px away, offset %d", allOffset)
	}
	tt.Advance(1500 * time.Millisecond)
	tt.Frame()
	saved := app.backend.GetUIState().HomePositions["all"]
	if saved.Key != allKey || abs(saved.OffsetPx-allOffset) > 1 {
		t.Fatalf("all: saved %+v, want %s at %d", saved, allKey, allOffset)
	}

	app.setFeedSiteFilter("bilibili")
	settle(tt, 4)
	scrollHomeTo(app, tt, 10, 21)
	biliAt := homeTop(t, app, tt)
	biliKey, biliOffset := biliAt.key, biliAt.off
	if !strings.HasPrefix(biliKey, "bilibili:") {
		t.Fatalf("bilibili filter shows %s", biliKey)
	}
	// Switching back within the session returns to the place left.
	app.setFeedSiteFilter("all")
	settle(tt, 4)
	if got := homeTop(t, app, tt); !got.same(allAt) {
		t.Fatalf("all again: %+v, want %+v", got, allAt)
	}
	if got := app.backend.GetUIState().HomePositions["bilibili"]; got.Key != biliKey || abs(got.OffsetPx-biliOffset) > 1 {
		t.Fatalf("switching filters did not save bilibili's position: %+v", got)
	}

	// A subscription's feed writes no position.
	before := app.backend.GetUIState().HomePositions
	app.openHomeActorFeed(HomeUpdatedActor{Key: "sub:none", Mid: 1001, Name: "bili author 1001", Authors: []int64{1001}})
	settle(tt, 2)
	list, _ := tt.Find("主页信息流列表")
	tt.Scroll(list.X+list.W/2, list.Y+120, 0, 400)
	tt.Advance(1500 * time.Millisecond)
	settle(tt, 2)
	if after := app.backend.GetUIState().HomePositions; after["all"] != before["all"] || after["bilibili"] != before["bilibili"] || len(after) != len(before) {
		t.Fatalf("a subscription's feed wrote a position: %+v -> %+v", before, after)
	}
	app.closeHomeActorFeed()
	settle(tt, 2)
	if got := homeTop(t, app, tt); !got.same(allAt) {
		t.Fatalf("leaving the subscription's feed: %+v, want %+v", got, allAt)
	}

	// Restart: a new App reads the positions as at startup.
	app.Close()
	restart := func() (*App, *ui.Tester) {
		next := NewApp(app.backend, player.NewNullAudioSink())
		next.Navigate(RouteHome)
		view := ui.NewTester(next.View, 1000, 860)
		settle(view, 4)
		return next, view
	}
	restored, view := restart()
	if got := homeTop(t, restored, view); !got.same(allAt) {
		t.Fatalf("restart all: %+v, want %+v", got, allAt)
	}
	// Scrolling up loads the newer posts above; the rows in view stay.
	// Newer posts were loaded above the position without moving it
	// (checked above); scrolling up reaches them.
	if restored.FeedItems[0].FeedKey == allKey || restored.feedMoreBefore && len(restored.FeedItems) < 90 {
		t.Fatalf("no newer posts above the position: first %s of %d", restored.FeedItems[0].FeedKey, len(restored.FeedItems))
	}
	list, _ = view.Find("主页信息流列表")
	view.Scroll(list.X+list.W/2, list.Y+120, 0, -float32(allOffset))
	settle(view, 4)
	if _, ok := view.Find("信息流行 " + allKey); !ok {
		t.Fatal("loading newer posts moved the post read out of view")
	}
	if got := homeTop(t, restored, view); got.key != allKey || got.off != 0 || abs(got.place-(allAt.place+allOffset)) > 1 {
		t.Fatalf("loading newer posts moved the rows in view: %+v", got)
	}
	restored.setFeedSiteFilter("bilibili")
	settle(view, 4)
	if got := homeTop(t, restored, view); !got.same(biliAt) {
		t.Fatalf("restart bilibili: %+v, want %+v", got, biliAt)
	}
	restored.setFeedSiteFilter("douyin")
	settle(view, 4)
	if got := homeTop(t, restored, view); got.key != restored.FeedItems[0].FeedKey || got.off != 0 || !strings.HasPrefix(got.key, "douyin:") {
		t.Fatalf("douyin has no position and must open at the newest: %+v", got)
	}
	page, err := restored.backend.FeedPage(library.FeedPageRequest{Order: "newest", Site: "douyin", CachedOnly: true, Limit: 1})
	if err != nil || len(page.Items) == 0 || page.Items[0].FeedKey != restored.FeedItems[0].FeedKey {
		t.Fatal("douyin did not open at its newest post")
	}

	// The site filter chosen is kept too: back to "全部" for the next run.
	restored.setFeedSiteFilter("all")
	restored.Close()

	// The post read was deleted: the nearest one not newer than it.
	lib := app.backend.(*service.Backend).LibraryStore()
	pos := app.backend.GetUIState().HomePositions["all"]
	page, err = app.backend.FeedPage(library.FeedPageRequest{Cursor: &library.FeedCursor{Key: pos.Key, PubTs: pos.PubTs}, Direction: "older", Order: "newest", CachedOnly: true, Limit: 1})
	if err != nil || len(page.Items) == 0 {
		t.Fatal("no post older than the position")
	}
	olderKey := page.Items[0].FeedKey
	if err := lib.DeleteItem(pos.Key, true); err != nil {
		t.Fatal(err)
	}
	afterDelete, view2 := restart()
	defer afterDelete.Close()
	if got := homeTop(t, afterDelete, view2); got.key != olderKey || got.off != 0 {
		t.Fatalf("after deleting %s: %+v, want %s at 0", pos.Key, got, olderKey)
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// NFR-PERF-001 (DEC-053 ①): with 5000 cached posts, a new session is back
// at the position within a second.
func TestHomeRestoresPositionQuicklyWith5000Posts(t *testing.T) {
	if testing.Short() {
		t.Skip("bulk fixture")
	}
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedMixedFeed(t, app, 5000, 0)
	app.Navigate(RouteHome)
	tt := ui.NewTester(app.View, 1000, 860)
	settle(tt, 2)
	// A position deep in the timeline, as after a long read.
	page, err := app.backend.FeedPage(library.FeedPageRequest{Order: "newest", CachedOnly: true, Limit: 1, Cursor: &library.FeedCursor{PubTs: 1000 + 2*1200}, Direction: "older"})
	if err != nil || len(page.Items) == 0 {
		t.Fatal("no fixture post")
	}
	target := page.Items[0]
	if _, err := app.backend.UpdateUIState(func() uistate.State {
		st := app.backend.GetUIState()
		st.HomePositions = map[string]uistate.HomePosition{"all": {Key: target.FeedKey, PubTs: target.PubTs, OffsetPx: 15}}
		return st
	}()); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	next := NewApp(app.backend, player.NewNullAudioSink())
	defer next.Close()
	next.Navigate(RouteHome)
	view := ui.NewTester(next.View, 1000, 860)
	settle(view, 4)
	elapsed := time.Since(start)
	if got := homeTop(t, next, view); got.key != target.FeedKey || abs(got.off-15) > 1 {
		t.Fatalf("%+v, want %s at 15", got, target.FeedKey)
	}
	budget := time.Second
	if raceDetectorEnabled {
		// Go's race detector adds 5-10x overhead to pure-Go SQLite + software rasterization;
		// the strict 1 s NFR-PERF-001 SLO is enforced by the non-race quality gate run.
		budget = 15 * time.Second
	}
	if elapsed > budget {
		t.Fatalf("restoring the position took %v (NFR-PERF-001 budget: %v)", elapsed, budget)
	}
}

// homeSubsFixture: three subscriptions with unread content, two read, one
// Douyin-only, and an anchor between them.
func homeSubsFixture(t *testing.T, app *App) map[string]subscription.Subscription {
	t.Helper()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	lib := app.backend.(*service.Backend).LibraryStore()
	subs := map[string]subscription.Subscription{}
	mk := func(name, siteID, uid string) {
		s, err := app.backend.CreateSubscription(name, "", []subscription.AccountRef{{Site: siteID, UID: uid, Name: name}}, subscription.SubRules{})
		if err != nil {
			t.Fatal(err)
		}
		subs[name] = s
	}
	post := func(id, siteID, uid string, mid int64, ts int64) {
		meta := library.ItemMetadata{Site: siteID, Kind: "dynamic", ID: id, RawID: id, Title: "post " + id, Text: "body", PubTs: ts}
		if siteID == "bilibili" {
			meta.Author = library.ItemAuthor{Mid: mid, Name: uid}
		} else {
			meta.Author = library.ItemAuthor{Site: siteID, UID: uid, Name: uid}
		}
		if _, err := lib.SaveItem(filepath.Join(lib.CacheDir(), siteID, id), meta); err != nil {
			t.Fatal(err)
		}
	}
	mk("未读甲", "bilibili", "3001")
	mk("未读乙", "bilibili", "3002")
	mk("未读丙", "douyin", "MS4wLjABAAAA_synth_unread_c")
	mk("已读丁", "bilibili", "3004")
	mk("已读戊", "bilibili", "3005")
	mk("无内容", "bilibili", "3006")
	post("d1", "bilibili", "3001", 3001, 5000)
	post("d2", "bilibili", "3002", 3002, 7000)
	post("d3", "douyin", "MS4wLjABAAAA_synth_unread_c", 0, 6000)
	post("d4", "bilibili", "3004", 3004, 3000)
	post("d5", "bilibili", "3005", 3005, 2000)
	if err := app.backend.SaveAnchor(uistate.FeedAnchor{Order: "newest", Key: "bilibili:dynamic:d4", PubTs: 4000}); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()
	app.RestartHomeSession()
	app.RefreshData()
	return subs
}

func homeRowNames(app *App) string {
	var out []string
	for _, r := range app.homeSubscriptionRows() {
		out = append(out, r.Name)
	}
	return strings.Join(out, ",")
}

// Unread subscriptions appear first with a dot, newest first, then the read
// ones; opening one clears its dot without moving it, the next entry to Home
// moves it among the read, and content cached during a stay adds a dot without
// moving rows.
func TestHomePanelSubscriptionsUnreadOrderAndDots(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	subs := homeSubsFixture(t, app)
	app.Navigate(RouteHome)
	tt := ui.NewTester(app.View, 1280, 860)
	settle(tt, 2)
	if got := homeRowNames(app); got != "未读乙,未读丙,未读甲,已读丁,已读戊" {
		t.Fatalf("order = %s", got)
	}
	for _, name := range []string{"未读甲", "未读乙", "未读丙"} {
		if _, ok := tt.Find("未读圆点：" + name); !ok {
			t.Fatalf("%s has no dot", name)
		}
	}
	for _, name := range []string{"已读丁", "已读戊"} {
		if _, ok := tt.Find("未读圆点：" + name); ok {
			t.Fatalf("%s has a dot", name)
		}
	}
	if _, ok := tt.Find("主页订阅：无内容"); ok {
		t.Fatal("a subscription without cached content is listed")
	}

	if err := tt.Click("主页订阅：未读乙"); err != nil {
		t.Fatal(err)
	}
	settle(tt, 2)
	if _, ok := tt.Find("未读圆点：未读乙"); ok {
		t.Fatal("the dot stayed after opening")
	}
	if got := app.backend.GetUIState().HomeSeen["sub:"+subs["未读乙"].ID]; got != 7000 {
		t.Fatalf("homeSeen = %d, want 7000", got)
	}
	if got := homeRowNames(app); got != "未读乙,未读丙,未读甲,已读丁,已读戊" {
		t.Fatalf("opening moved the rows: %s", got)
	}
	app.closeHomeActorFeed()

	// Leaving Home and coming back sorts again.
	app.Navigate(RouteParse)
	app.Navigate(RouteHome)
	settle(tt, 2)
	if got := homeRowNames(app); got != "未读丙,未读甲,未读乙,已读丁,已读戊" {
		t.Fatalf("after re-entering Home: %s", got)
	}

	// New content for a read subscription: a dot, no move.
	lib := app.backend.(*service.Backend).LibraryStore()
	if _, err := lib.SaveItem(filepath.Join(lib.CacheDir(), "bilibili", "d6"), library.ItemMetadata{Site: "bilibili", Kind: "dynamic", ID: "d6", Title: "new", Text: "new", PubTs: 9000, Author: library.ItemAuthor{Mid: 3005, Name: "3005"}}); err != nil {
		t.Fatal(err)
	}
	app.handleBackendEvent(service.Event{Type: service.EventLibraryChanged})
	settle(tt, 2)
	if _, ok := tt.Find("未读圆点：已读戊"); !ok {
		t.Fatal("new content did not add a dot")
	}
	if got := homeRowNames(app); got != "未读丙,未读甲,未读乙,已读丁,已读戊" {
		t.Fatalf("new content moved the rows: %s", got)
	}
}

// First use shows no dots; with only the anchor, newer subscriptions are
// unread; the Douyin filter lists and sorts by Douyin content only.
func TestHomePanelFirstUseAnchorAndSiteFilter(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	homeSubsFixture(t, app)
	// First use: neither the anchor nor homeSeen.
	if _, err := app.backend.UpdateUIState(func() uistate.State {
		st := app.backend.GetUIState()
		st.Anchor = uistate.FeedAnchor{Order: "newest"}
		st.HomeSeen = nil
		return st
	}()); err != nil {
		t.Fatal(err)
	}
	fresh := NewApp(app.backend, player.NewNullAudioSink())
	defer fresh.Close()
	fresh.Navigate(RouteHome)
	tt := ui.NewTester(fresh.View, 1280, 860)
	settle(tt, 1)
	// The view itself records the anchor; the session keeps the startup one.
	for _, r := range fresh.homeSubscriptionRows() {
		if fresh.homeSubUnread(r) {
			t.Fatalf("first use shows %s unread", r.Name)
		}
		if _, ok := tt.Find("未读圆点：" + r.Name); ok {
			t.Fatalf("first use shows a dot on %s", r.Name)
		}
	}

	// Douyin: only the Douyin subscription, by Douyin content.
	app.setFeedSiteFilter("douyin")
	if got := homeRowNames(app); got != "未读丙" {
		t.Fatalf("douyin filter lists %s", got)
	}
	app.setFeedSiteFilter("bilibili")
	if got := homeRowNames(app); got != "未读乙,未读甲,已读丁,已读戊" {
		t.Fatalf("bilibili filter lists %s", got)
	}
}

// Live subscriptions appear first and ringed, even without content; no section
// titles or status text; going live during a stay moves nothing; the narrow
// window shows the avatar strip with "全部动态" first.
func TestHomePanelLiveFirstNoTitlesAndNarrowStrip(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	subs := homeSubsFixture(t, app)
	app.LiveRooms = []live.RoomState{
		{UID: 3006, Uname: "无内容", RoomID: 5150, LiveStatus: 1, Title: "直播标题"},
		{UID: 9999, Uname: "没订阅的直播", RoomID: 6160, LiveStatus: 1, Title: "别人的直播"},
	}
	app.Navigate(RouteHome)
	tt := ui.NewTester(app.View, 1280, 860)
	settle(tt, 2)
	if got := homeRowNames(app); got != "无内容,未读乙,未读丙,未读甲,已读丁,已读戊" {
		t.Fatalf("order = %s", got)
	}
	for _, gone := range []string{"正在直播", "有更新", "直播标题", "录制中", "主页订阅：没订阅的直播", "侧栏直播 6160"} {
		if _, ok := tt.Find(gone); ok {
			t.Fatalf("the Home panel shows %q", gone)
		}
	}
	if err := tt.Click("主页订阅：无内容"); err != nil {
		t.Fatal(err)
	}
	settle(tt, 1)
	if app.HomeActorFilter == nil || app.HomeActorFilter.SubID != subs["无内容"].ID {
		t.Fatalf("a live row opens its subscription's feed, got %+v", app.HomeActorFilter)
	}
	app.closeHomeActorFeed()

	// Going live during the stay: ring only.
	app.LiveRooms = append(app.LiveRooms, live.RoomState{UID: 3005, Uname: "已读戊", RoomID: 7170, LiveStatus: 1})
	settle(tt, 1)
	if got := homeRowNames(app); got != "无内容,未读乙,未读丙,未读甲,已读丁,已读戊" {
		t.Fatalf("going live moved the rows: %s", got)
	}
	if !app.homeSubLive(app.homeSubscriptionRows()[5]) {
		t.Fatal("the live ring did not follow")
	}

	narrow := ui.NewTester(app.View, 720, 860)
	settle(narrow, 2)
	strip, ok := narrow.Find("主页订阅头像条")
	if !ok {
		t.Fatal("the narrow window has no avatar strip")
	}
	all, ok := narrow.Find("主页订阅头像：全部动态")
	first, ok2 := narrow.Find("主页订阅头像：无内容")
	if !ok || !ok2 || all.X >= first.X || all.Y < strip.Y {
		t.Fatalf("全部动态 must lead the strip: %+v %+v", all, first)
	}
	if _, ok := narrow.Find("未读圆点：未读乙"); !ok {
		t.Fatal("the strip has no unread dot")
	}
}

// A thousand subscriptions are virtualized and the panel does not read the
// library while drawing.
func TestHomePanelThousandSubscriptionsVirtualized(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 3)
	subs := make([]subscription.Subscription, 1000)
	for i := range subs {
		subs[i] = subscription.Subscription{ID: fmt.Sprintf("s%04d", i), Name: fmt.Sprintf("订阅%04d", i), Site: "bilibili", Mid: int64(50000 + i), Accounts: []subscription.AccountRef{{Site: "bilibili", UID: fmt.Sprint(50000 + i)}}}
	}
	authors := make([]library.ItemAuthor, 1000)
	for i := range authors {
		authors[i] = library.ItemAuthor{Site: "bilibili", UID: fmt.Sprint(50000 + i), Mid: int64(50000 + i), LatestPubTs: int64(10000 + i)}
	}
	app.Subscriptions = subs
	app.libraryStatistics.Authors = authors
	app.enterHomeSubscriptions()
	tt := ui.NewTester(app.View, 1280, 860)
	settle(tt, 3)
	counter := &statsCountingBackend{BackendAPI: app.backend}
	app.backend = counter
	settle(tt, 1)
	built := 0
	for i := 0; i < 1000; i++ {
		if _, ok := tt.Find(fmt.Sprintf("主页订阅：订阅%04d", i)); ok {
			built++
		}
	}
	if built == 0 || built > 120 {
		t.Fatalf("%d of 1000 rows built", built)
	}
	list, _ := tt.Find("主页订阅列表")
	for i := 0; i < 20; i++ {
		tt.Scroll(list.X+list.W/2, list.Y+100, 0, 400)
		tt.Frame()
	}
	if counter.stats != 0 || counter.feed != 0 {
		t.Fatalf("drawing read the library: %d statistics, %d feed pages", counter.stats, counter.feed)
	}
}

type statsCountingBackend struct {
	service.BackendAPI
	stats, feed int
}

func (b *statsCountingBackend) LibraryStatistics() (library.Statistics, error) {
	b.stats++
	return b.BackendAPI.LibraryStatistics()
}

func (b *statsCountingBackend) FeedPage(req library.FeedPageRequest) (library.FeedPageResult, error) {
	b.feed++
	return b.BackendAPI.FeedPage(req)
}
