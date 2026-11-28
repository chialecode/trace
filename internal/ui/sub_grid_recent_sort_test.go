package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/player"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/chialecode/trace/internal/uistate"
	"github.com/egoist/mygo/ui"
)

func TestFormatSubGridUpdatedAt(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 11, 28, 15, 30, 0, 0, loc)

	cases := []struct {
		name string
		ts   int64
		want string
	}{
		{name: "zero", ts: 0, want: "暂无缓存"},
		{name: "negative", ts: -10, want: "暂无缓存"},
		{name: "just_now", ts: now.Add(-25 * time.Second).Unix(), want: "刚刚更新"},
		{name: "slight_future_skew", ts: now.Add(20 * time.Second).Unix(), want: "刚刚更新"},
		{name: "minutes", ts: now.Add(-15 * time.Minute).Unix(), want: "15 分钟前更新"},
		{name: "hours", ts: now.Add(-5 * time.Hour).Unix(), want: "5 小时前更新"},
		{name: "one_day", ts: now.Add(-24 * time.Hour).Unix(), want: "1 天前更新"},
		{name: "three_days", ts: now.Add(-3 * 24 * time.Hour).Unix(), want: "3 天前更新"},
		{name: "twenty_nine_days", ts: now.Add(-29 * 24 * time.Hour).Unix(), want: "29 天前更新"},
		{name: "same_year_older", ts: time.Date(2026, 8, 5, 10, 0, 0, 0, loc).Unix(), want: "8月5日更新"},
		{name: "prev_year", ts: time.Date(2025, 12, 19, 9, 0, 0, 0, loc).Unix(), want: "2025年12月19日更新"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatSubGridUpdatedAt(tc.ts, now)
			if got != tc.want {
				t.Fatalf("formatSubGridUpdatedAt(%d) = %q, want %q", tc.ts, got, tc.want)
			}
		})
	}

	// Explicit non-UTC+8 (UTC-8) cross-day boundary: must honor now.Location() rather than host time.Local.
	pst := time.FixedZone("PST", -8*3600)
	nowPST := time.Date(2026, 11, 28, 12, 0, 0, 0, pst)
	// 2025-12-19 01:00 UTC is 2025-12-19 09:00 CST, but 2025-12-18 17:00 PST.
	crossDayTs := time.Date(2025, 12, 19, 1, 0, 0, 0, time.UTC).Unix()
	if got := formatSubGridUpdatedAt(crossDayTs, nowPST); got != "2025年12月18日更新" {
		t.Fatalf("formatSubGridUpdatedAt(UTC-8 cross-day) = %q, want %q", got, "2025年12月18日更新")
	}
	// Same-year cross-day: 2026-08-05 02:00 UTC is 2026-08-04 18:00 PST.
	sameYearCrossDayTs := time.Date(2026, 8, 5, 2, 0, 0, 0, time.UTC).Unix()
	if got := formatSubGridUpdatedAt(sameYearCrossDayTs, nowPST); got != "8月4日更新" {
		t.Fatalf("formatSubGridUpdatedAt(UTC-8 same-year cross-day) = %q, want %q", got, "8月4日更新")
	}
}

func assertSubGridCellOrder(t *testing.T, tt *ui.Tester, orderedIDs []string) {
	t.Helper()
	var prev ui.Rect
	for i, id := range orderedIDs {
		r, ok := tt.Find("选择订阅 " + id)
		if !ok {
			t.Fatalf("missing grid cell for subscription %q", id)
		}
		if i > 0 {
			// In row-major grid order, next cell is either on a lower row (Y > prev.Y + 4)
			// or on the same row to the right (X > prev.X).
			sameRow := r.Y >= prev.Y-4 && r.Y <= prev.Y+4
			if !(r.Y > prev.Y+4 || (sameRow && r.X > prev.X)) {
				t.Fatalf("grid order violated between %q (%.1f,%.1f) and %q (%.1f,%.1f)",
					orderedIDs[i-1], prev.X, prev.Y, id, r.X, r.Y)
			}
		}
		prev = r
	}
}

func TestSubscriptionGridTimestampsAndSortPersistence(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()

	raw, ok := app.backend.(*service.Backend)
	if !ok {
		t.Fatalf("expected *service.Backend, got %T", app.backend)
	}
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()

	now := time.Now()
	tsJustNow := now.Add(-20 * time.Second).Unix()
	tsThreeDays := now.Add(-3 * 24 * time.Hour).Unix()
	tsTwoHours := now.Add(-2 * time.Hour).Unix()

	// Create 5 subscriptions with distinct offline sources:
	// 1. subNoCacheA: no cache, no updates -> "暂无缓存"
	// 2. subCachedOld: Douyin cached 3 days ago -> "3 天前更新"
	// 3. subMultiAcc: Bilibili + Weibo, newest cached 2 hours ago (Weibo) -> "2 小时前更新"
	// 4. subUpdateOnly: X account with LatestUpdateTs just now (update_item only, no library file) -> "刚刚更新"
	// 5. subNoCacheB: no cache, no updates -> "暂无缓存" (verifies stable tie-break with subNoCacheA)
	subNoCacheA, err := raw.CreateSubscription("空缓存甲", "", []subscription.AccountRef{{Site: "douyin", UID: "MS4w_sub_empty_a", Name: "空缓存甲"}}, subscription.EmptySubRules())
	if err != nil {
		t.Fatal(err)
	}
	subCachedOld, err := raw.CreateSubscription("旧缓存乙", "", []subscription.AccountRef{{Site: "douyin", UID: "MS4w_sub_old_b", Name: "旧缓存乙"}}, subscription.EmptySubRules())
	if err != nil {
		t.Fatal(err)
	}
	subMultiAcc, err := raw.CreateSubscription("多账号丙", "", []subscription.AccountRef{
		{Site: "douyin", UID: "MS4w_sub_multi_dy", Name: "多账号抖音"},
		{Site: "weibo", UID: "500001001", Name: "多账号微博"},
	}, subscription.EmptySubRules())
	if err != nil {
		t.Fatal(err)
	}
	subUpdateOnly, err := raw.CreateSubscription("刚更新丁", "", []subscription.AccountRef{{Site: "x", UID: "1000000000000000123", UniqueID: "fresh_x", Name: "刚更新丁"}}, subscription.EmptySubRules())
	if err != nil {
		t.Fatal(err)
	}
	subNoCacheB, err := raw.CreateSubscription("空缓存戊", "", []subscription.AccountRef{{Site: "weibo", UID: "500001002", Name: "空缓存戊"}}, subscription.EmptySubRules())
	if err != nil {
		t.Fatal(err)
	}

	// Seed local library items offline (zero network requests).
	lib := raw.LibraryStore()
	if _, err := lib.SaveItem(filepath.Join(lib.CacheDir(), "douyin", "old_b", "works", "w1"), library.ItemMetadata{
		Version: 1, ID: "w1", RawID: "w1", Site: "douyin", Kind: "video", Status: "complete",
		Title: "三天前作品", PubTs: tsThreeDays, CachedAt: now.UTC().Format(time.RFC3339),
		Author: library.ItemAuthor{Site: "douyin", UID: "MS4w_sub_old_b", Name: "旧缓存乙"},
	}); err != nil {
		t.Fatal(err)
	}
	// Multi-account: older Douyin item (10 days ago) + newer Weibo item (2 hours ago) -> must pick 2 hours ago.
	if _, err := lib.SaveItem(filepath.Join(lib.CacheDir(), "douyin", "multi", "works", "w2"), library.ItemMetadata{
		Version: 1, ID: "w2", RawID: "w2", Site: "douyin", Kind: "video", Status: "complete",
		Title: "十天前作品", PubTs: now.Add(-10 * 24 * time.Hour).Unix(), CachedAt: now.UTC().Format(time.RFC3339),
		Author: library.ItemAuthor{Site: "douyin", UID: "MS4w_sub_multi_dy", Name: "多账号抖音"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := lib.SaveItem(filepath.Join(lib.CacheDir(), "weibo", "multi", "posts", "p1"), library.ItemMetadata{
		Version: 1, ID: "p1", RawID: "p1", Site: "weibo", Kind: "dynamic", Status: "complete",
		Title: "两小时前微博", PubTs: tsTwoHours, CachedAt: now.UTC().Format(time.RFC3339),
		Author: library.ItemAuthor{Site: "weibo", UID: "500001001", Name: "多账号微博"},
	}); err != nil {
		t.Fatal(err)
	}

	app.RefreshData()
	// Simulate an offline update_item timestamp for subUpdateOnly (e.g. from check-updates without caching).
	for i := range app.Subscriptions {
		if app.Subscriptions[i].ID == subUpdateOnly.ID {
			app.Subscriptions[i].LatestUpdateTs = tsJustNow
		}
	}

	// Wrap backend to verify strictly ZERO live/network requests occur on non-Bilibili subscriptions.
	liveGuard := &visibleStatusBackend{BackendAPI: app.backend}
	app.backend = liveGuard

	app.Navigate(RouteSubscriptions)
	tt := ui.NewTester(app.View, 1280, 860)
	settle(tt, 2)

	// Default order: creation order (A, B, C, D, E).
	if got := app.effectiveSubSort(); got != uistate.SubSortDefault {
		t.Fatalf("initial SubSort = %q, want %q", got, uistate.SubSortDefault)
	}
	assertSubGridCellOrder(t, tt, []string{
		subNoCacheA.ID, subCachedOld.ID, subMultiAcc.ID, subUpdateOnly.ID, subNoCacheB.ID,
	})
	for _, wantText := range []string{"默认顺序", "刚刚更新", "2 小时前更新", "3 天前更新", "暂无缓存"} {
		if !tt.HasText(wantText) {
			t.Fatalf("expected text %q on subscriptions grid", wantText)
		}
	}

	// Click sort dropdown and switch to "按最近更新".
	if err := tt.Click("订阅排序方式"); err != nil {
		t.Fatalf("open sort select: %v", err)
	}
	tt.Frame()
	if err := tt.Click("按最近更新"); err != nil {
		t.Fatalf("select 按最近更新: %v", err)
	}
	settle(tt, 2)

	if got := app.effectiveSubSort(); got != uistate.SubSortRecent {
		t.Fatalf("SubSort after selecting 按最近更新 = %q, want %q", got, uistate.SubSortRecent)
	}
	// Recent order: subUpdateOnly (just now) -> subMultiAcc (2h) -> subCachedOld (3d) -> subNoCacheA -> subNoCacheB (stable tie-break!).
	assertSubGridCellOrder(t, tt, []string{
		subUpdateOnly.ID, subMultiAcc.ID, subCachedOld.ID, subNoCacheA.ID, subNoCacheB.ID,
	})

	// Persisted in ui-state.json without bumping schema version.
	persisted := raw.GetUIState()
	if uistate.CurrentSchemaVersion != 4 || persisted.Version != 4 {
		t.Fatalf("ui-state version = %d (schema=%d), want 4", persisted.Version, uistate.CurrentSchemaVersion)
	}
	if persisted.SubSort != uistate.SubSortRecent {
		t.Fatalf("persisted ui-state SubSort = %q, want %q", persisted.SubSort, uistate.SubSortRecent)
	}

	// Fresh App instance restores persisted SubSort = "recent".
	reopened := NewApp(raw, player.NewNullAudioSink())
	defer reopened.Close()
	if got := reopened.effectiveSubSort(); got != uistate.SubSortRecent {
		t.Fatalf("reopened App SubSort = %q, want %q", got, uistate.SubSortRecent)
	}
	reopened.Navigate(RouteSubscriptions)
	reTT := ui.NewTester(reopened.View, 1280, 860)
	settle(reTT, 2)
	if !reTT.HasText("按最近更新") {
		t.Fatal("reopened App did not display persisted sort mode 按最近更新")
	}

	// Verify zero horizontal overflow across 1280 / 1000 / 720 widths.
	for _, w := range []int{1280, 1000, 720} {
		wTT := ui.NewTester(app.View, w, 860)
		settle(wTT, 2)
		bar, okBar := wTT.Find("订阅搜索与排序栏")
		sel, okSel := wTT.Find("订阅排序方式")
		inp, okInp := wTT.Find("订阅搜索过滤框")
		if !okBar || !okSel || !okInp {
			t.Fatalf("width %d missing controls: bar=%v sel=%v inp=%v", w, okBar, okSel, okInp)
		}
		if bar.X < -0.5 || bar.X+bar.W > float32(w)+0.5 {
			t.Fatalf("width %d toolbar overflow: bar=%+v", w, bar)
		}
		if sel.X < -0.5 || sel.X+sel.W > float32(w)+0.5 || sel.W <= 0 || sel.H <= 0 {
			t.Fatalf("width %d sort selector overflow or collapsed: sel=%+v", w, sel)
		}
		if inp.X+inp.W > sel.X+1.0 {
			t.Fatalf("width %d search input overlaps sort selector: inp=%+v sel=%+v", w, inp, sel)
		}
		if _, found := wTT.Find("订阅网格站点"); found {
			t.Fatalf("width %d must never render site badge on subscription grid", w)
		}
	}

	// Switch back to "默认顺序" and verify order + persistence restore.
	if err := tt.Click("订阅排序方式"); err != nil {
		t.Fatalf("reopen sort select: %v", err)
	}
	tt.Frame()
	if err := tt.Click("默认顺序"); err != nil {
		t.Fatalf("select 默认顺序: %v", err)
	}
	settle(tt, 2)
	assertSubGridCellOrder(t, tt, []string{
		subNoCacheA.ID, subCachedOld.ID, subMultiAcc.ID, subUpdateOnly.ID, subNoCacheB.ID,
	})
	if got := raw.GetUIState().SubSort; got != uistate.SubSortDefault {
		t.Fatalf("persisted SubSort after switching back = %q, want %q", got, uistate.SubSortDefault)
	}

	// Strictly zero live/network requests across the entire sort & timestamp flow.
	if len(liveGuard.requests) != 0 {
		t.Fatalf("expected 0 network requests on offline subscription grid flow, got %d", len(liveGuard.requests))
	}
}

// TestSubscriptionGrid500VirtualScrollAndAsyncStatsRefresh verifies:
//  1. 500 synthetic subscriptions render smoothly via virtualized rows across 1280/1000/720.
//  2. Steady frames, scrolling, searching, and sorting do NOT query LibraryStatistics().
//  3. Burst EventLibraryChanged while viewing the subscriptions grid refreshes LibraryStatistics
//     in a single-flight background goroutine without blocking the UI main thread (LESSONS.md #171).
func TestSubscriptionGrid500VirtualScrollAndAsyncStatsRefresh(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()

	now := time.Now()
	const total = 500
	subs := make([]subscription.Subscription, total)
	authors := make([]library.ItemAuthor, 0, total)
	for i := 0; i < total; i++ {
		uid := fmt.Sprintf("MS4w_perf_%04d", i)
		subs[i] = subscription.Subscription{
			ID:       fmt.Sprintf("sub-perf-%04d", i),
			Name:     fmt.Sprintf("合成订阅_%04d", i),
			Site:     "douyin",
			Accounts: []subscription.AccountRef{{Site: "douyin", UID: uid, Name: fmt.Sprintf("合成订阅_%04d", i)}},
		}
		// Give 350 subscriptions varying timestamps (making index 499 the newest) and leave 150 uncached.
		if i%10 >= 3 {
			pubTs := now.Add(-time.Duration(total-i) * time.Hour).Unix()
			authors = append(authors, library.ItemAuthor{
				Site:        "douyin",
				UID:         uid,
				Name:        subs[i].Name,
				LatestPubTs: pubTs,
			})
		}
	}
	app.Subscriptions = subs
	app.subsRev++
	app.libraryStatistics = library.Statistics{Authors: authors}
	app.statsRev++
	app.libraryStatsDirty = false

	statsEntered := make(chan struct{})
	statsRelease := make(chan struct{})
	defer func() {
		select {
		case <-statsRelease:
		default:
			close(statsRelease)
		}
	}()

	stub := &slowEventCountingBackend{
		BackendAPI:   app.backend,
		statsEntered: statsEntered,
		statsRelease: statsRelease,
	}
	app.backend = stub
	app.Navigate(RouteSubscriptions)
	stub.statsCalls.Store(0)

	// Switch to "按最近更新": newest (sub-perf-0499) must appear first!
	app.setSubSort(uistate.SubSortRecent)

	for _, w := range []int{1280, 1000, 720} {
		app.SubListState.ScrollTo(0, ui.Start)
		tt := ui.NewTester(app.View, w, 860)
		settle(tt, 2)
		first, last := app.SubListState.Visible()
		if first != 0 || last < 0 || last-first+1 >= 20 {
			t.Fatalf("width %d rendered too many rows for %d subscriptions: %d..%d", w, total, first, last)
		}
		if _, ok := tt.Find("选择订阅 sub-perf-0499"); !ok {
			t.Fatalf("width %d expected newest subscription sub-perf-0499 at top of sorted grid", w)
		}
		if _, ok := tt.Find("订阅最近更新：sub-perf-0499"); !ok {
			t.Fatalf("width %d missing updated caption for sub-perf-0499", w)
		}
		// Scroll across 20 steps without triggering any SQLite statistics reads.
		listRect, okList := tt.Find("订阅列表")
		if !okList {
			t.Fatalf("width %d missing 订阅列表", w)
		}
		for step := 0; step < 20; step++ {
			tt.Scroll(listRect.X+listRect.W/2, listRect.Y+120, 0, 360)
			tt.Frame()
		}
		bar, okBar := tt.Find("订阅搜索与排序栏")
		if !okBar || bar.X < -0.5 || bar.X+bar.W > float32(w)+0.5 {
			t.Fatalf("width %d toolbar overflow after scrolling: bar=%+v", w, bar)
		}
	}

	if got := stub.statsCalls.Load(); got != 0 {
		t.Fatalf("steady frames and 500-item virtual scroll called LibraryStatistics %d times, want 0", got)
	}

	// Bind async desktop event loop and fire 100 burst EventLibraryChanged events:
	// UI thread must NEVER block on slow LibraryStatistics().
	mainQueue := make(chan func(), 64)
	var posts atomic.Int32
	app.BindEventLoop(func(fn func()) {
		posts.Add(1)
		mainQueue <- fn
	})

	for i := 0; i < 100; i++ {
		stub.emit(service.Event{
			Type:    service.EventLibraryChanged,
			Site:    "douyin",
			Message: fmt.Sprintf("douyin:video:%04d", i),
		})
	}
	if got := posts.Load(); got != 1 {
		t.Fatalf("100 burst EventLibraryChanged events posted %d UI callbacks, want 1", got)
	}

	drainDone := make(chan struct{})
	go func() {
		fn := <-mainQueue
		fn()
		close(drainDone)
	}()
	select {
	case <-drainDone:
	case <-time.After(2 * time.Second):
		t.Fatal("UI main thread blocked waiting for LibraryStatistics() on /subscriptions grid")
	}

	select {
	case <-statsEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("expected single-flight async LibraryStatistics() refresh on /subscriptions grid")
	}

	close(statsRelease)
	select {
	case applyFn := <-mainQueue:
		applyFn()
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for async LibraryStatistics() apply callback")
	}

	if got := stub.statsCalls.Load(); got != 1 {
		t.Fatalf("100 burst events called LibraryStatistics %d times, want 1", got)
	}
}
