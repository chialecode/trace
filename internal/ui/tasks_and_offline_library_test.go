package ui

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
	"github.com/egoist/mygo/ui"
)

type spyOfflineLibraryBackend struct {
	service.BackendAPI
	siteCalls atomic.Int32
	opened    []string
}

func (b *spyOfflineLibraryBackend) OpenLink(_ context.Context, input string) (*service.OpenLinkResult, error) {
	b.siteCalls.Add(1)
	b.opened = append(b.opened, input)
	return &service.OpenLinkResult{
		Site:       "douyin",
		DouyinUser: &service.DouyinUserView{SecUID: "MS4wLjABAAAA_hist_user", Nickname: "抖音历史作者"},
	}, nil
}

func (b *spyOfflineLibraryBackend) FetchImage(_ context.Context, _ string) ([]byte, error) {
	b.siteCalls.Add(1)
	return nil, errors.New("unexpected remote image fetch in offline library")
}

func (b *spyOfflineLibraryBackend) StagePreviewMedia(_ context.Context, _, _, _ string) (string, error) {
	b.siteCalls.Add(1)
	return "", errors.New("unexpected remote preview stage in offline library")
}

func (b *spyOfflineLibraryBackend) OpenVideo(_ context.Context, _ string) (bilibili.VideoDetail, bool, error) {
	b.siteCalls.Add(1)
	return bilibili.VideoDetail{}, false, errors.New("unexpected OpenVideo in offline library")
}

func (b *spyOfflineLibraryBackend) OpenDynamic(_ context.Context, _ string) (bilibili.DynamicItem, error) {
	b.siteCalls.Add(1)
	return bilibili.DynamicItem{}, errors.New("unexpected OpenDynamic in offline library")
}

func (b *spyOfflineLibraryBackend) OpenUser(_ context.Context, _ int64) (bilibili.UserSpace, error) {
	b.siteCalls.Add(1)
	return bilibili.UserSpace{}, errors.New("unexpected OpenUser in offline library")
}

func (b *spyOfflineLibraryBackend) OpenDouyinWork(_ context.Context, _ string) (service.DouyinWorkView, error) {
	b.siteCalls.Add(1)
	return service.DouyinWorkView{}, errors.New("unexpected OpenDouyinWork in offline library")
}

func (b *spyOfflineLibraryBackend) OpenDouyinUser(_ context.Context, _ string) (service.DouyinUserView, error) {
	b.siteCalls.Add(1)
	return service.DouyinUserView{}, errors.New("unexpected OpenDouyinUser in offline library")
}

// Parse page main panel contains only the centered input box (no history list),
// while the context sidebar lists recent parses, opens on row click, deletes entry+thumbs,
// and requires inline confirmation before clearing all.
func TestParsePageCenteredInputAndHistorySidebar(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)
	posted := make(chan func(), 32)
	app.dispatch = func(fn func()) { posted <- fn }

	raw := app.backend.(*service.Backend)
	hist := raw.HistoryStore()
	avThumb, err := hist.SaveThumb("video", "BV1ParseHist1", "avatar", ".png", buildTestPNGBytes())
	if err != nil {
		t.Fatal(err)
	}
	cvThumb, err := hist.SaveThumb("video", "BV1ParseHist1", "cover", ".png", buildTestPNGBytes())
	if err != nil {
		t.Fatal(err)
	}
	if err := hist.Record(storage.HistoryEntry{
		Site:         "bilibili",
		Kind:         "video",
		ID:           "BV1ParseHist1",
		Title:        "侧栏解析历史条目一",
		Subtitle:     "作者甲",
		CanonicalURL: "https://www.bilibili.com/video/BV1ParseHist1",
		AvatarThumb:  avThumb,
		CoverThumb:   cvThumb,
	}); err != nil {
		t.Fatal(err)
	}
	if err := hist.Record(storage.HistoryEntry{
		Site:         "douyin",
		Kind:         "user",
		ID:           "MS4wLjABAAAA_hist_user",
		Title:        "抖音历史作者",
		CanonicalURL: "https://www.douyin.com/user/MS4wLjABAAAA_hist_user",
	}); err != nil {
		t.Fatal(err)
	}
	app.HistoryItems = app.backend.ListHistory()
	spy := &spyOfflineLibraryBackend{BackendAPI: app.backend}
	app.backend = spy

	app.Navigate(RouteParse)
	for _, w := range []int{1280, 1000, 720} {
		tt := ui.NewTester(app.View, w, 860)
		tt.Frame()
		box, ok := tt.Find("解析大输入框容器")
		if !ok {
			t.Fatalf("width %d: missing centered parse input container", w)
		}
		if box.X < 0 || box.X+box.W > float32(w)+1 {
			t.Fatalf("width %d: parse input container overflows viewport: %+v", w, box)
		}
		for _, req := range []string{"链接解析输入框", "立即解析"} {
			if _, ok := tt.Find(req); !ok {
				t.Fatalf("width %d: missing %s on parse route", w, req)
			}
		}
		center, ok := tt.Find("中栏内容")
		if !ok {
			t.Fatalf("width %d: missing center column", w)
		}
		if row, ok := tt.Find("历史打开：BV1ParseHist1"); w >= 1000 {
			panel, pok := tt.Find("上下文侧栏")
			if !pok || !ok || row.X+row.W > panel.X+panel.W+1 || row.X >= center.X {
				t.Fatalf("width %d: history row must live in context sidebar, row=%+v panel=%+v center=%+v", w, row, panel, center)
			}
		} else {
			// Narrow window: no sidebar, so the recent parses fall back under the input.
			_, fok := tt.Find("解析页最近解析")
			if !ok || !fok || row.X < center.X || row.Y < box.Y+box.H {
				t.Fatalf("width %d: recent parses must stay reachable under the input when the sidebar is hidden, row=%+v (found=%v) fallback=%v box=%+v", w, row, ok, fok, box)
			}
		}
	}

	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()

	// Delete single entry via "⋯ -> 删除" and verify its thumbnails are cleaned up.
	chooseMenuItem(t, tt, "历史更多：BV1ParseHist1", "删除")
	tt.Frame()
	if _, ok := tt.Find("历史打开：BV1ParseHist1"); ok {
		t.Fatal("deleted history entry still shown in sidebar")
	}
	if _, err := raw.HistoryThumbPath(avThumb); err == nil {
		t.Fatal("expected avatar thumbnail to be deleted with history entry")
	}
	if _, err := raw.HistoryThumbPath(cvThumb); err == nil {
		t.Fatal("expected cover thumbnail to be deleted with history entry")
	}

	// "全部清空" shows inline confirmation; canceling keeps remaining entry, confirming clears all.
	chooseMenuItem(t, tt, "最近解析更多", "全部清空")
	tt.Frame()
	if !app.HistoryClearConfirmOpen {
		t.Fatal("expected inline confirmation before clearing history")
	}
	if err := tt.Click("取消清空历史"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(app.HistoryItems) != 1 {
		t.Fatalf("canceling clear removed history: got %d", len(app.HistoryItems))
	}

	// Clicking a history row opens its canonical URL.
	if err := tt.Click("历史打开：MS4wLjABAAAA_hist_user"); err != nil {
		t.Fatal(err)
	}
	drainPosted(posted)
	if len(spy.opened) != 1 || spy.opened[0] != "https://www.douyin.com/user/MS4wLjABAAAA_hist_user" {
		t.Fatalf("expected clicking history row to open canonical URL, got %v", spy.opened)
	}

	app.Navigate(RouteParse)
	tt.Frame()
	chooseMenuItem(t, tt, "最近解析更多", "全部清空")
	tt.Frame()
	if err := tt.Click("确认清空历史"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(app.HistoryItems) != 0 {
		t.Fatalf("expected 0 history items after confirming clear, got %d", len(app.HistoryItems))
	}
}

// RouteParse, RouteVideo, and RouteUser share the exact same recent-parse sidebar
// and highlight the currently open entry.
func TestVideoUserAndParseShareSameHistorySidebarWithActiveHighlight(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)

	hist := app.backend.(*service.Backend).HistoryStore()
	_ = hist.Record(storage.HistoryEntry{
		Site: "bilibili", Kind: "video", ID: "BV1Shared01",
		Title: "共用侧栏视频", Subtitle: "UP一", CanonicalURL: "https://www.bilibili.com/video/BV1Shared01",
	})
	_ = hist.Record(storage.HistoryEntry{
		Site: "bilibili", Kind: "user", ID: "3030",
		Title: "共用侧栏UP主", CanonicalURL: "https://space.bilibili.com/3030",
	})
	app.HistoryItems = app.backend.ListHistory()

	tt := ui.NewTester(app.View, 1280, 860)
	for _, route := range []string{RouteParse, RouteVideo, RouteUser} {
		app.CurrentRoute = route
		if route == RouteVideo {
			app.CurrentVideo = &bilibili.VideoDetail{BVID: "BV1Shared01", Title: "共用侧栏视频", Author: bilibili.AuthorInfo{Mid: 3030, Name: "共用侧栏UP主"}}
		} else {
			app.CurrentVideo = nil
		}
		if route == RouteUser {
			app.CurrentUser = &bilibili.UserSpace{Mid: 3030, Name: "共用侧栏UP主"}
		} else {
			app.CurrentUser = nil
		}
		tt.Frame()
		for _, label := range []string{"历史打开：BV1Shared01", "历史打开：3030"} {
			if _, ok := tt.Find(label); !ok {
				t.Fatalf("route %s missing shared sidebar entry %s", route, label)
			}
		}
		switch route {
		case RouteParse:
			if app.isCurrentHistoryEntry("bilibili", "video", "BV1Shared01") || app.isCurrentHistoryEntry("bilibili", "user", "3030") {
				t.Fatal("parse route must not highlight any unopened history entry")
			}
		case RouteVideo:
			if !app.isCurrentHistoryEntry("bilibili", "video", "BV1Shared01") || app.isCurrentHistoryEntry("bilibili", "user", "3030") {
				t.Fatal("video route did not highlight the open video entry exclusively")
			}
		case RouteUser:
			if !app.isCurrentHistoryEntry("bilibili", "user", "3030") || app.isCurrentHistoryEntry("bilibili", "video", "BV1Shared01") {
				t.Fatal("user route did not highlight the open user entry exclusively")
			}
		}
	}
}

// Library has no sort toggle, ignores legacy "cached_at" preference without error,
// and orders items (including live recordings by start time) newest-first by publish time.
func TestLibraryHasNoSortToggleAndIgnoresLegacyCachedAtSort(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)

	raw := app.backend.(*service.Backend)
	// Write a raw v2 ui-state.json containing legacy "cached_at" field to verify silent ignore + no error.
	statePath := filepath.Join(raw.Locations().DataDir, "ui-state.json")
	legacyJSON := `{"version":2,"lastRoute":"/library","sortPref":"cached_at","librarySort":"cached_at"}`
	if err := os.WriteFile(statePath, []byte(legacyJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	olderCachedLater := seedLibraryControlItem(t, app, library.ItemMetadata{
		Site: "bilibili", Kind: "video", ID: "BV1OldPubNewCache", Title: "旧发布新缓存视频",
		PubTs: 1700000100, CachedAt: "2026-11-15T10:00:00Z",
		Media: []library.MediaFile{{Role: "video", Path: "P01.mp4"}},
	})
	liveRec := seedLibraryControlItem(t, app, library.ItemMetadata{
		Site: "bilibili", Kind: "live", ID: "99-1700000500", Title: "中间开播录像",
		PubTs: 1700000500, CachedAt: "2026-11-10T10:00:00Z",
		Media: []library.MediaFile{{Role: "live_segment", Path: "part-001.mp4", Duration: 10}},
		Live:  &library.LiveRecordingInfo{RoomID: 99, StartedAt: "2023-11-14T22:21:40Z"},
	})
	newerCachedEarlier := seedLibraryControlItem(t, app, library.ItemMetadata{
		Site: "douyin", Kind: "video", ID: "7399001", Title: "新发布旧缓存作品",
		PubTs: 1700000900, CachedAt: "2026-11-01T10:00:00Z",
		Media: []library.MediaFile{{Role: "video", Path: "video.mp4"}},
	})

	app2 := NewApp(raw, nil)
	defer app2.Close()
	app2.LibrarySort = "cached_at" // legacy in-memory value must also be ignored
	app2.Navigate(RouteLibrary)
	app2.reloadLibraryItems()

	tt := ui.NewTester(app2.View, 1280, 860)
	tt.Frame()
	if _, found := tt.Find("媒体库排序切换"); found {
		t.Fatal("library must not show a sort toggle")
	}
	if app2.BannerError != "" || app2.LibraryError != "" {
		t.Fatalf("unexpected error loading library with legacy sort preference: banner=%q lib=%q", app2.BannerError, app2.LibraryError)
	}
	if len(app2.LibraryItems) < 3 {
		t.Fatalf("expected 3 library items, got %d", len(app2.LibraryItems))
	}
	gotOrder := []string{app2.LibraryItems[0].ItemKey, app2.LibraryItems[1].ItemKey, app2.LibraryItems[2].ItemKey}
	wantOrder := []string{newerCachedEarlier.ItemKey, liveRec.ItemKey, olderCachedLater.ItemKey}
	if strings.Join(gotOrder, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("library sort order = %v, want publish-time newest-first %v", gotOrder, wantOrder)
	}
}

// Every click path inside Library and RouteLibraryItem stays on library routes,
// keeps "媒体库" highlighted in the rail, makes 0 site requests, and preserves filter & scroll on Back.
func TestLibraryOfflineNavigationNeverLeavesLibraryOrRequestsSite(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)
	posted := make(chan func(), 64)
	app.dispatch = func(fn func()) { posted <- fn }

	pngBytes := buildTestPNGBytes()
	mp4Bytes := buildTestMP4Bytes(t, t.TempDir())
	lib := app.backend.(*service.Backend).LibraryStore()

	writeItem := func(relDir string, files map[string][]byte, meta library.ItemMetadata) library.ItemMetadata {
		dir := filepath.Join(lib.CacheDir(), relDir)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		for name, raw := range files {
			if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		saved, err := lib.SaveItem(dir, meta)
		if err != nil {
			t.Fatal(err)
		}
		return saved
	}

	vidItem := writeItem("video/BV1Offline01", map[string][]byte{"P01.mp4": mp4Bytes, "cover.png": pngBytes}, library.ItemMetadata{
		Site: "bilibili", Kind: "video", ID: "BV1Offline01", RawID: "BV1Offline01", Title: "离线多P视频",
		PubTs: 1710000900, UpgradeAvailable: true,
		Author: library.ItemAuthor{Site: "bilibili", Mid: 7788, Name: "离线UP", Face: "https://img.example.invalid/face.jpg"},
		Media:  []library.MediaFile{{Role: "cover", Path: "cover.png"}, {Role: "video", Path: "P01.mp4", Duration: 4}},
		Parts:  []library.VideoPartRecord{{Page: 1, CID: 11, Title: "第一P", File: "P01.mp4", Duration: 4}},
	})
	dynItem := writeItem("dynamic/dyn_off_1", map[string][]byte{"01.png": pngBytes}, library.ItemMetadata{
		Site: "bilibili", Kind: "dynamic", ID: "dyn_off_1", RawID: "dyn_off_1", Title: "离线图文动态",
		PubTs:  1710000800,
		Author: library.ItemAuthor{Site: "bilibili", Mid: 7788, Name: "离线UP", Face: "https://img.example.invalid/face.jpg"},
		Media:  []library.MediaFile{{Role: "image", Path: "01.png", Width: 800, Height: 600}},
		Videos: []string{vidItem.RawID},
	})
	recItem := writeItem("live/88-1", map[string][]byte{"part-001.mp4": mp4Bytes}, library.ItemMetadata{
		Site: "bilibili", Kind: "live", ID: "88-1", RawID: "88-1", Title: "离线直播录像",
		PubTs: 1710000700,
		Media: []library.MediaFile{{Role: "live_segment", Path: "part-001.mp4", Duration: 5}},
		Live:  &library.LiveRecordingInfo{RoomID: 88, Segments: []library.LiveSegmentInfo{{File: "part-001.mp4", DurationSec: 5}}},
	})
	dyNote := writeItem("douyin/note_off_1", map[string][]byte{"p01.png": pngBytes}, library.ItemMetadata{
		Site: "douyin", Kind: "dynamic", ID: "750001", RawID: "750001", Title: "离线抖音图文",
		PubTs:     1710000600,
		Author:    library.ItemAuthor{Site: "douyin", UID: "sec_offline", Name: "离线抖音作者", Face: "https://img.example.invalid/dy.jpg"},
		Media:     []library.MediaFile{{Role: "image", Path: "p01.png", Width: 900, Height: 1200}},
		NotePages: []library.NotePageRecord{{Index: 1, Kind: "image", Image: "p01.png", Width: 900, Height: 1200}},
	})
	// Extra items so LibraryListState has scrollable rows.
	for i := 0; i < 14; i++ {
		id := "BV1ExtraOff" + string(rune('A'+i))
		writeItem("video/"+id, map[string][]byte{"P01.mp4": mp4Bytes}, library.ItemMetadata{
			Site: "bilibili", Kind: "video", ID: id, RawID: id, Title: "占位离线视频 " + id,
			PubTs: int64(1710000100 + i),
			Media: []library.MediaFile{{Role: "video", Path: "P01.mp4", Duration: 3}},
		})
	}

	app.RefreshData()
	spy := &spyOfflineLibraryBackend{BackendAPI: app.backend}
	app.backend = spy

	app.Navigate(RouteLibrary)
	app.LibrarySiteFilter = string(site.Bilibili)
	app.selectLibraryFilter()
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	app.LibraryListState.ScrollTo(2, ui.Start)
	tt.Frame()
	savedFirst, _ := app.LibraryListState.Visible()
	app.LibraryListState.ScrollTo(0, ui.Start)
	tt.Frame()

	assertOfflineLibraryState := func(step string) {
		t.Helper()
		if app.CurrentRoute != RouteLibrary && app.CurrentRoute != RouteLibraryItem {
			t.Fatalf("%s: route escaped library: %s", step, app.CurrentRoute)
		}
		if got := spy.siteCalls.Load(); got != 0 {
			t.Fatalf("%s: expected 0 site calls in offline library, got %d", step, got)
		}
		if _, ok := tt.Find("上下文侧栏"); !ok {
			t.Fatalf("%s: expected library context panel to stay visible", step)
		}
	}

	// 1. Click video thumbnail in Library -> opens PlayerOverlay on RouteLibrary.
	if err := tt.Click("媒体缩略图 " + vidItem.ItemKey); err != nil {
		t.Fatal(err)
	}
	drainPosted(posted)
	tt.Frame()
	if !app.PlayerOverlay.Open || app.CurrentRoute != RouteLibrary {
		t.Fatalf("clicking video tile did not open local player on RouteLibrary: open=%v route=%s", app.PlayerOverlay.Open, app.CurrentRoute)
	}
	assertOfflineLibraryState("click video tile")
	app.closePlayerOverlay()
	tt.Frame()

	// 2. Click image tile in Library -> opens ImageViewer on RouteLibrary.
	if err := tt.Click("媒体缩略图 " + dynItem.ItemKey); err != nil {
		t.Fatal(err)
	}
	drainPosted(posted)
	tt.Frame()
	if !app.ImageViewer.Open || app.CurrentRoute != RouteLibrary {
		t.Fatalf("clicking image tile did not open local viewer on RouteLibrary: open=%v route=%s", app.ImageViewer.Open, app.CurrentRoute)
	}
	assertOfflineLibraryState("click image tile")
	app.closeImageViewer()
	tt.Frame()

	// Scroll down before opening detail so we can verify scroll + filter restoration on Back!
	app.LibraryListState.ScrollTo(2, ui.Start)
	tt.Frame()
	beforeBackFirst, _ := app.LibraryListState.Visible()
	if beforeBackFirst != savedFirst || beforeBackFirst == 0 {
		t.Fatalf("expected non-zero scrolled row before opening detail, got %d", beforeBackFirst)
	}

	// 3. Open video detail via OpenLibraryItemDetail (same action as "⋯ -> 详情") -> RouteLibraryItem.
	app.OpenLibraryItemDetail(vidItem)
	tt.Frame()
	if app.CurrentRoute != RouteLibraryItem {
		t.Fatalf("expected RouteLibraryItem, got %s", app.CurrentRoute)
	}
	for _, netOnly := range []string{"缓存视频", "视频详情升级缓存", "补全：" + vidItem.ItemKey, "进入空间", "选择分P 1"} {
		if _, ok := tt.Find(netOnly); ok {
			t.Fatalf("offline video detail exposed network control %q", netOnly)
		}
	}
	if err := tt.Click("播放分P 1"); err != nil {
		t.Fatal(err)
	}
	drainPosted(posted)
	if !app.PlayerOverlay.Open {
		t.Fatal("clicking 播放分P 1 did not open local player")
	}
	app.closePlayerOverlay()
	tt.Frame()
	assertOfflineLibraryState("video detail part play")

	// 4. Press Back -> returns to RouteLibrary with exact filter and scroll row intact!
	if err := tt.Click("返回"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if app.CurrentRoute != RouteLibrary || app.LibrarySiteFilter != string(site.Bilibili) {
		t.Fatalf("Back from RouteLibraryItem lost library route/filter: route=%s site=%s", app.CurrentRoute, app.LibrarySiteFilter)
	}
	afterBackFirst, _ := app.LibraryListState.Visible()
	if afterBackFirst != beforeBackFirst {
		t.Fatalf("Back from RouteLibraryItem lost scroll position: got %d, want %d", afterBackFirst, beforeBackFirst)
	}

	// 5. Dynamic detail (image + referenced video), Recording detail (segment), and Douyin note detail.
	app.OpenLibraryItemDetail(dynItem)
	tt.Frame()
	if err := tt.Click("查看图片 1"); err != nil {
		t.Fatal(err)
	}
	drainPosted(posted)
	if !app.ImageViewer.Open {
		t.Fatal("clicking image in offline dynamic detail did not open ImageViewer")
	}
	app.closeImageViewer()
	tt.Frame()
	if err := tt.Click("播放视频卡片：" + vidItem.ItemKey); err != nil {
		t.Fatal(err)
	}
	drainPosted(posted)
	if !app.PlayerOverlay.Open {
		t.Fatal("clicking cached video card in offline dynamic detail did not open PlayerOverlay")
	}
	app.closePlayerOverlay()
	tt.Frame()
	assertOfflineLibraryState("dynamic detail image & video")

	app.OpenLibraryItemDetail(recItem)
	tt.Frame()
	if err := tt.Click("播放录像片段：" + recItem.ItemKey + ":part-001.mp4"); err != nil {
		t.Fatal(err)
	}
	drainPosted(posted)
	if !app.PlayerOverlay.Open {
		t.Fatal("clicking segment in offline recording detail did not open PlayerOverlay")
	}
	app.closePlayerOverlay()
	tt.Frame()
	assertOfflineLibraryState("recording segment play")

	app.OpenLibraryItemDetail(dyNote)
	tt.Frame()
	if err := tt.Click("查看图文页 1"); err != nil {
		t.Fatal(err)
	}
	drainPosted(posted)
	if !app.ImageViewer.Open {
		t.Fatal("clicking note page in offline Douyin detail did not open ImageViewer")
	}
	app.closeImageViewer()
	tt.Frame()
	assertOfflineLibraryState("douyin note page view")
}

// The icon rail "任务" badge counts ONLY queued and running download tasks,
// never live recordings, waiting/running cache-queue items, or finished/paused tasks.
func TestTasksBadgeCountsOnlyActiveDownloadTasks(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()

	app.LiveRooms = []live.RoomState{{RoomID: 101, Recording: true}}
	app.LiveAccounts = []service.LiveAccountState{{Site: "douyin", UID: "sec_1", Recording: true}}
	app.CacheQueue = []service.CacheQueueTask{
		{ID: "cq_wait", Status: service.CacheQueueWaiting},
		{ID: "cq_run", Status: service.CacheQueueRunning},
	}
	app.Tasks = []download.Task{
		{TaskID: "t-done", Status: "completed"},
		{TaskID: "t-paused", Status: "paused"},
		{TaskID: "t-fail", Status: "failed"},
	}
	if got := app.activeWorkCount(); got != 0 {
		t.Fatalf("expected activeWorkCount() = 0 when only recordings, cache queue, and inactive tasks exist, got %d", got)
	}

	app.Tasks = append(app.Tasks,
		download.Task{TaskID: "t-run", Status: "running"},
		download.Task{TaskID: "t-queue", Status: "queued"},
	)
	if got := app.activeWorkCount(); got != 2 {
		t.Fatalf("expected activeWorkCount() = 2 for 1 running + 1 queued task, got %d", got)
	}
}

type tasksQueueAndLiveStub struct {
	*spyOfflineLibraryBackend
	queue      []service.CacheQueueTask
	rooms      []live.RoomState
	onStopRoom func(roomID int64) (live.RoomState, error)
}

func (s *tasksQueueAndLiveStub) ListLiveRooms() []live.RoomState {
	if len(s.rooms) > 0 {
		out := make([]live.RoomState, len(s.rooms))
		copy(out, s.rooms)
		return out
	}
	return s.spyOfflineLibraryBackend.ListLiveRooms()
}

func (s *tasksQueueAndLiveStub) ListCacheQueue() []service.CacheQueueTask {
	out := make([]service.CacheQueueTask, len(s.queue))
	copy(out, s.queue)
	return out
}

func (s *tasksQueueAndLiveStub) PauseCacheQueue(id string) (service.CacheQueueTask, error) {
	for i := range s.queue {
		if s.queue[i].ID == id {
			s.queue[i].Status = service.CacheQueuePaused
			s.queue[i].StatusText = "已手动暂停"
			s.queue[i].StopReason = "已手动暂停"
			return s.queue[i], nil
		}
	}
	return service.CacheQueueTask{}, errors.New("not found")
}

func (s *tasksQueueAndLiveStub) ResumeCacheQueue(id string) (service.CacheQueueTask, error) {
	for i := range s.queue {
		if s.queue[i].ID == id {
			s.queue[i].Status = service.CacheQueueRunning
			s.queue[i].StatusText = "进行中"
			s.queue[i].StopReason = ""
			return s.queue[i], nil
		}
	}
	return service.CacheQueueTask{}, errors.New("not found")
}

func (s *tasksQueueAndLiveStub) CancelCacheQueue(id string) (service.CacheQueueTask, error) {
	for i := range s.queue {
		if s.queue[i].ID == id {
			s.queue[i].Status = service.CacheQueueStopped
			s.queue[i].StatusText = "已取消"
			s.queue[i].StopReason = "已取消"
			return s.queue[i], nil
		}
	}
	return service.CacheQueueTask{}, errors.New("not found")
}

func (s *tasksQueueAndLiveStub) RemoveCacheQueue(id string) error {
	for i := range s.queue {
		if s.queue[i].ID == id {
			if s.queue[i].Status != service.CacheQueueStopped && s.queue[i].Status != service.CacheQueueCompleted {
				return errors.New("still running")
			}
			s.queue = append(s.queue[:i], s.queue[i+1:]...)
			return nil
		}
	}
	return errors.New("not found")
}

func (s *tasksQueueAndLiveStub) RemoveFinishedCacheQueues() (int, error) {
	kept, n := s.queue[:0], 0
	for _, q := range s.queue {
		if q.Status == service.CacheQueueStopped || q.Status == service.CacheQueueCompleted {
			n++
			continue
		}
		kept = append(kept, q)
	}
	s.queue = kept
	return n, nil
}

func (s *tasksQueueAndLiveStub) StopLiveRecording(roomID int64) (live.RoomState, error) {
	if s.onStopRoom != nil {
		return s.onStopRoom(roomID)
	}
	return s.spyOfflineLibraryBackend.StopLiveRecording(roomID)
}

// Tasks page sidebar offers four uniform entries (Download Tasks, Cache Queue,
// Live Recordings, Incidents, DEC-057); the Cache Queue page lists one card per
// queue that opens to its recent items, pause/resume/cancel/remove work without
// deleting cached items, and narrow windows (<1000px) fall back to the top
// segmented selector without overflow.
func TestTasksSidebarSectionsAndCacheQueueDetail(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	posted := make(chan func(), 64)
	app.dispatch = func(fn func()) { posted <- fn }

	seedUIFeed(t, app, 0)
	raw := app.backend.(*service.Backend)
	cacheDir := raw.GetSettings().CacheDir
	lib := raw.LibraryStore()

	mp4Bytes, err := download.BuildSyntheticVideoM4S(12, 30)
	if err != nil {
		t.Fatal(err)
	}
	vidDir := filepath.Join(cacheDir, "bilibili", "20001", "videos", "BV1queue001")
	if err := os.MkdirAll(vidDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vidDir, "P01.mp4"), mp4Bytes, 0o600); err != nil {
		t.Fatal(err)
	}
	cachedVid, err := lib.SaveItem(vidDir, library.ItemMetadata{
		Site: "bilibili", Kind: "video", ID: "BV1queue001", RawID: "BV1queue001", Title: "队列刚缓存好的视频",
		PubTs: 1710002000,
		Parts: []library.VideoPartRecord{{Page: 1, CID: 1, Title: "P1", File: "P01.mp4", Duration: 2}},
		Media: []library.MediaFile{{Role: "video", Path: "P01.mp4", Duration: 2}},
	})
	if err != nil {
		t.Fatal(err)
	}

	stub := &tasksQueueAndLiveStub{
		spyOfflineLibraryBackend: &spyOfflineLibraryBackend{BackendAPI: app.backend},
		queue: []service.CacheQueueTask{
			{
				ID:           "cq_run",
				Site:         "bilibili",
				User:         "20001",
				UserName:     "技术频道_Alpha",
				List:         "videos",
				ListLabel:    "投稿",
				Status:       service.CacheQueueRunning,
				StatusText:   "进行中",
				CachedCount:  12,
				SkippedCount: 3,
				ActiveTitle:  "正在缓存第 13 条视频",
				RecentItems: []service.CacheQueueRecentItem{
					{ItemKey: cachedVid.ItemKey, Title: cachedVid.Title, CachedAt: "2026-11-15T05:30:00Z"},
				},
			},
			{
				ID:           "cq_backoff",
				Site:         "douyin",
				User:         "MS4wLjABAAAA_dy_queue",
				UserName:     "高原摄影师",
				List:         "works",
				ListLabel:    "作品",
				Status:       service.CacheQueueWaitingBackoff,
				StatusText:   "遇到风控，等待站点退避结束",
				StopReason:   "遇到风控，等待站点退避结束",
				CachedCount:  5,
				SkippedCount: 1,
				NextRunAt:    "2026-11-15T06:15:00Z",
			},
		},
	}
	stub.rooms = []live.RoomState{
		{RoomID: 9002, UID: 20001, Uname: "技术频道_Alpha", Title: "架构直播", LiveStatus: 1, Recording: true},
	}
	app.backend = stub
	app.CacheQueue = stub.ListCacheQueue()
	app.Tasks = []download.Task{
		{TaskID: "dl_1", Site: "bilibili", Kind: "video", TargetID: "BV1dl1", Title: "下载任务一", Status: "running", Progress: 0.4, TotalBytes: 1024},
		{TaskID: "dl_2", Site: "douyin", Kind: "video", TargetID: "73001", Title: "下载任务二", Status: "queued", TotalBytes: 2048},
	}
	app.LiveRooms = stub.ListLiveRooms()

	app.Navigate(RouteTasks)
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()

	// Downloads section is selected by default; recording rows must NOT appear on Downloads.
	for _, must := range []string{"上下文侧栏", "任务侧栏：下载任务", "任务侧栏：缓存队列", "任务侧栏：直播录制", "任务侧栏：录制异常", "任务站点：dl_1"} {
		if _, ok := tt.Find(must); !ok {
			t.Fatalf("missing control %q on RouteTasks", must)
		}
	}
	if _, ok := tt.Find("录制站点：bilibili:9002"); ok {
		t.Fatal("live recording row leaked into Downloads section")
	}

	if _, ok := tt.Find("任务侧栏：队列：cq_run"); ok {
		t.Fatal("queues are listed in the sidebar instead of the 缓存队列 page")
	}
	if !tt.HasText("缓存队列（2）") {
		t.Fatal("the sidebar does not count the two unfinished queues")
	}

	// The Cache Queue page lists every queue as a card, closed.
	if err := tt.Click("任务侧栏：缓存队列"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, must := range []string{"缓存队列卡片：cq_run", "缓存队列卡片：cq_backoff", "队列详情站点：cq_run", "队列详情计数：cq_run", "暂停缓存队列：cq_run", "取消缓存队列：cq_run"} {
		if _, ok := tt.Find(must); !ok {
			t.Fatalf("missing queue control %q", must)
		}
	}
	if _, ok := tt.Find("队列最近缓存：" + cachedVid.ItemKey); ok {
		t.Fatal("a queue card is open before it is clicked")
	}
	if !tt.HasText("取消或移除队列不会删除已缓存到媒体库的内容") {
		t.Fatal("expected cancel safety explanation on the queue page")
	}
	if err := tt.Click("展开缓存队列：cq_run"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tt.Find("队列最近缓存：" + cachedVid.ItemKey); !ok {
		t.Fatal("opening the queue card does not show its recent items")
	}

	// Click recent cached item -> opens local player without site requests.
	if err := tt.Click("队列最近缓存：" + cachedVid.ItemKey); err != nil {
		t.Fatal(err)
	}
	drainPosted(posted)
	tt.Frame()
	if !app.PlayerOverlay.Open {
		t.Fatal("clicking recent item in queue detail did not open local player")
	}
	app.closePlayerOverlay()
	tt.Frame()
	if stub.siteCalls.Load() != 0 {
		t.Fatalf("expected 0 site calls opening recent queue item, got %d", stub.siteCalls.Load())
	}

	// Pause -> Resume -> Cancel (preserving cached library items).
	if err := tt.Click("暂停缓存队列：cq_run"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tt.Find("继续缓存队列：cq_run"); !ok {
		t.Fatal("expected 继续 button after pausing queue task")
	}
	if err := tt.Click("继续缓存队列：cq_run"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("取消缓存队列：cq_run"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, stillCached, err := lib.GetItem(cachedVid.ItemKey); err != nil || !stillCached {
		t.Fatalf("canceling cache queue task must not delete cached library items: ok=%v err=%v", stillCached, err)
	}
	// A canceled queue can be removed from the list; its items stay.
	if err := tt.Click("移除缓存队列：cq_run"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tt.Find("缓存队列卡片：cq_run"); ok {
		t.Fatal("the removed queue is still listed")
	}
	if _, stillCached, err := lib.GetItem(cachedVid.ItemKey); err != nil || !stillCached {
		t.Fatalf("removing a cache queue must not delete cached library items: ok=%v err=%v", stillCached, err)
	}

	// Switch to Live Recordings section via sidebar.
	if err := tt.Click("任务侧栏：直播录制"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tt.Find("录制站点：bilibili:9002"); !ok {
		t.Fatal("expected active live recording row in Live Recordings section")
	}

	// Narrow window (<1000px) uses top segmented selector without horizontal overflow.
	ttNarrow := ui.NewTester(app.View, 720, 860)
	ttNarrow.Frame()
	if _, ok := ttNarrow.Find("上下文侧栏"); ok {
		t.Fatal("expected context panel to fold away at 720px")
	}
	centerRect, ok := ttNarrow.Find("中栏内容")
	if !ok || centerRect.X+centerRect.W > 720.5 {
		t.Fatalf("narrow /tasks overflowed horizontally: %+v", centerRect)
	}
	if err := ttNarrow.Click("任务分段：下载任务"); err != nil {
		t.Fatal(err)
	}
	ttNarrow.Frame()
	if app.effectiveTasksSection() != "downloads" {
		t.Fatalf("narrow segment switch failed: %s", app.effectiveTasksSection())
	}
}

func buildGrowingVideoPartChunks(t *testing.T, numChunks int) (initAndFirst []byte, extraChunks [][]byte) {
	t.Helper()
	vRaw, err := download.BuildSyntheticVideoM4S(30, 30)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := mp4.DecodeFile(bytes.NewReader(vRaw), mp4.WithDecodeFlags(mp4.DecStartOnMoof))
	if err != nil || parsed.Init == nil || len(parsed.Segments) == 0 {
		t.Fatalf("DecodeFile synthetic m4s: %v", err)
	}
	samples, err := parsed.Segments[0].Fragments[0].GetFullSamples(parsed.Init.Moov.Mvex.Trexs[0])
	if err != nil || len(samples) == 0 {
		t.Fatalf("GetFullSamples: %v", err)
	}
	var durPerChunk uint64
	for _, s := range samples {
		durPerChunk += uint64(s.Dur)
	}
	initAndFirst = vRaw
	for idx := 1; idx < numChunks; idx++ {
		frag, err := mp4.CreateFragment(uint32(idx+1), 1)
		if err != nil {
			t.Fatal(err)
		}
		cur := uint64(idx) * durPerChunk
		for _, s := range samples {
			cp := s
			cp.DecodeTime = cur
			cur += uint64(cp.Dur)
			frag.AddFullSample(cp)
		}
		var buf bytes.Buffer
		if err := frag.Encode(&buf); err != nil {
			t.Fatal(err)
		}
		extraChunks = append(extraChunks, buf.Bytes())
	}
	return initAndFirst, extraChunks
}

// Clicking an active live recording starts real-time in-page preview
// from the growing .part file with 0 extra site requests, advances frames as the .part
// file grows, shows an honest message on unsupported streams, and immediately releases
// file handles when leaving the view or stopping the recording so .part rename succeeds.
func TestTasksLiveRecordingRealTimePreviewAndHandleRelease(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	posted := make(chan func(), 64)
	app.dispatch = func(fn func()) { posted <- fn }

	seedUIFeed(t, app, 0)
	recDir := t.TempDir()
	partPath := filepath.Join(recDir, "part-001.mp4.part")
	initAndFirst, moreChunks := buildGrowingVideoPartChunks(t, 4)

	writerFile, err := os.OpenFile(partPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writerFile.Close() }()
	if _, err := writerFile.Write(initAndFirst); err != nil {
		t.Fatal(err)
	}
	_ = writerFile.Sync()

	var stopRenameErr error
	finalMP4Path := filepath.Join(recDir, "part-001.mp4")
	stub := &tasksQueueAndLiveStub{
		spyOfflineLibraryBackend: &spyOfflineLibraryBackend{BackendAPI: app.backend},
		onStopRoom: func(roomID int64) (live.RoomState, error) {
			_ = writerFile.Close()
			stopRenameErr = os.Rename(partPath, finalMP4Path)
			return live.RoomState{RoomID: roomID, UID: 20001, Uname: "技术频道_Alpha", Recording: false}, stopRenameErr
		},
	}
	app.backend = stub

	app.LiveRooms = []live.RoomState{
		{
			RoomID:           9002,
			UID:              20001,
			Uname:            "技术频道_Alpha",
			Title:            "HLS fMP4 边录边播实测",
			LiveStatus:       1,
			Quality:          10000,
			QualityName:      "原画",
			Codec:            "avc",
			Transport:        "hls_fmp4",
			Recording:        true,
			RecordSegments:   1,
			RecordPartIndex:  1,
			RecordPartPath:   partPath,
			RecordPartReady:  true,
			PreviewSupported: true,
		},
	}
	app.LiveAccounts = []service.LiveAccountState{
		{
			Site:             "douyin",
			AccountKey:       "douyin:sec_audio_only",
			RecordingKey:     "douyin:live:77001",
			UID:              "sec_audio_only",
			RoomID:           "77001",
			Uname:            "电台主播",
			Title:            "深夜纯语音电台",
			LiveStatus:       1,
			AudioOnly:        true,
			Recording:        true,
			PreviewSupported: false,
			PreviewReason:    "纯音频直播暂不支持画面实时预览",
		},
	}

	app.Navigate(RouteTasks)
	app.selectTasksLive()
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()

	// 1. Unsupported recording honestly states "这场录制暂不支持实时预览".
	if err := tt.Click("预览直播录制：douyin:live:77001"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tt.Find("直播不支持实时预览提示"); !ok {
		t.Fatal("expected unsupported live preview message for audio-only stream")
	}

	// 2. Click active fMP4 recording -> opens real OpenLivePreview without any site calls.
	if err := tt.Click("预览直播录制：bilibili:live:9002"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		drainPosted(posted)
		tt.Frame()
		if !app.LivePreviewLoading && app.livePreviewPlayer.Bitmap() != nil {
			break
		}
		if app.LivePreviewError != "" {
			t.Fatalf("live preview failed to open: %s", app.LivePreviewError)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if app.livePreviewPlayer.Bitmap() == nil {
		t.Fatalf("expected decoded live preview bitmap, loading=%v err=%q", app.LivePreviewLoading, app.LivePreviewError)
	}
	if _, ok := tt.Find("弹幕开关"); ok {
		t.Fatal("live preview must not expose danmaku toggle")
	}

	// Grow the .part file on disk and verify playback advances past the first 1-second chunk!
	for _, chunk := range moreChunks {
		if _, err := writerFile.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	_ = writerFile.Sync()

	deadline = time.Now().Add(4 * time.Second)
	var advancedPos float64
	for time.Now().Before(deadline) {
		app.livePreviewPlayer.Advance(120 * time.Millisecond)
		tt.Frame()
		advancedPos = app.livePreviewPlayer.Snapshot(1, 1).PositionSec
		if advancedPos >= 1.3 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if advancedPos < 1.3 {
		t.Fatalf("expected live preview position to advance into appended chunks (>=1.3s), got %.2fs", advancedPos)
	}

	// Zero extra site requests during real-time preview!
	if got := stub.siteCalls.Load(); got != 0 {
		t.Fatalf("expected 0 site requests during live preview, got %d", got)
	}

	// 3. Leaving the Live Recordings tab immediately closes preview and releases file handles.
	if err := tt.Click("任务侧栏：下载任务"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if app.LivePreviewKey != "" {
		t.Fatalf("expected LivePreviewKey to clear on leaving live section, got %q", app.LivePreviewKey)
	}
	_ = writerFile.Close()
	tempCheckPath := filepath.Join(recDir, "part-001.check.mp4")
	if err := os.Rename(partPath, tempCheckPath); err != nil {
		t.Fatalf("file handle still locked after leaving live preview tab: %v", err)
	}
	if err := os.Rename(tempCheckPath, partPath); err != nil {
		t.Fatal(err)
	}

	// 4. Re-open preview, then click "停止录制" (with only Bilibili recording left) -> handle released before StopLiveRecording renames .part!
	app.LiveAccounts = nil
	if err := tt.Click("任务侧栏：直播录制"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("预览直播录制：bilibili:live:9002"); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		drainPosted(posted)
		tt.Frame()
		if !app.LivePreviewLoading && app.livePreviewPlayer.Bitmap() != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := tt.Click("任务停止录制 9002"); err != nil {
		t.Fatal(err)
	}
	waitUIIdle(t, app, posted)
	tt.Frame()
	if stopRenameErr != nil {
		t.Fatalf("stopping recording failed to rename .part file (handle leak): %v", stopRenameErr)
	}
	if _, ok := tt.Find("直播录制空状态"); !ok || !tt.HasText("现在没有直播在录制") {
		t.Fatal("expected empty state 现在没有直播在录制 after stopping all recordings")
	}
}

func drainPosted(ch chan func()) {
	for {
		select {
		case fn := <-ch:
			if fn != nil {
				fn()
			}
		default:
			return
		}
	}
}
