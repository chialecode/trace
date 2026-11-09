package ui

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/player"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/chialecode/trace/internal/uistate"
	"github.com/egoist/mygo/ui"
)

func TestOriginalImageAndMissingMediaNeverUseSyntheticFallback(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" || r.Header.Get("Referer") != "" {
			t.Error("image request leaked credentials or referer")
		}
		if r.URL.Path != "/original.png" {
			t.Errorf("expected original path, got %s", r.URL.Path)
		}
		img := image.NewRGBA(image.Rect(0, 0, 13, 7))
		img.Set(0, 0, color.RGBA{R: 220, A: 255})
		_ = png.Encode(w, img)
	}))
	defer server.Close()
	app.OpenImageViewer([]string{server.URL + "/original.png@320w"}, nil, 0)
	if app.ImageViewer.Bitmaps[0] == nil {
		t.Fatalf("original did not load: %+v", app.ImageViewer.Errors)
	}
	if w, h := app.ImageViewer.Bitmaps[0].Size(); w != 13 || h != 7 {
		t.Fatalf("unexpected original size %dx%d", w, h)
	}
	app.OpenItemImageViewer("missing", nil, []string{filepath.Join(t.TempDir(), "private.png")}, 0)
	if app.ImageViewer.Bitmaps[0] != nil || app.ImageViewer.Errors[0] == "" {
		t.Fatal("unregistered absolute path must not render")
	}
	_ = app.player.LoadDecoder("explicit test fixture", player.NewSynthDecoder(16, 16, time.Second, 30))
	app.OpenPlayerOverlay(library.ItemMetadata{ItemKey: "missing", Parts: []library.VideoPartRecord{{File: "../private.mp4"}}})
	if app.PlayerOverlay.LoadError == "" || app.player.Snapshot(1, 1).Open {
		t.Fatal("missing media retained or fabricated a playable video")
	}
}

func TestAuthGuardAndNamedMultiAccountSubscriptionUI(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if app.CurrentRoute != RouteLogin {
		t.Fatalf("startup auth guard route=%s", app.CurrentRoute)
	}
	app.Navigate(RouteLibrary)
	if app.CurrentRoute != RouteLogin || app.SavedRoute != RouteLibrary {
		t.Fatal("logged-out navigation escaped guard")
	}
	_, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", "")
	if err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()
	sub, err := app.backend.CreateSubscription("Named person", "note", []subscription.AccountRef{{Site: "bilibili", UID: "1001", Name: "first"}, {Site: "bilibili", UID: "2002", Name: "second"}}, subscription.SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatal(err)
	}
	app.RefreshData()
	app.syncSelectedSubRules(sub)
	app.Navigate(RouteSubscriptions)
	tt := ui.NewTester(app.View, 1000, 860)
	if _, ok := tt.Find("选择订阅 " + sub.ID); !ok {
		t.Fatal("stable subscription ID missing in UI")
	}
	if ids := app.selectedSubscriptionAuthors(); len(ids) != 2 || ids[1] != 2002 {
		t.Fatalf("secondary account missing from cache filter: %v", ids)
	}
	app.Navigate(RouteSettings)
	tt.SetSize(720, 860)
	tt.Frame()
	if _, ok := tt.Find("设置分类：关于"); !ok {
		t.Fatal("narrow settings category clipped")
	}
}

func TestRequestRunsOffWindowAndPublishesOnlyOnDispatcher(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	posted := make(chan func(), 1)
	app.dispatch = func(fn func()) { posted <- fn }
	release := make(chan struct{})
	applied := false
	request(app, "fixture", func(ctx context.Context) (int, error) {
		select {
		case <-release:
			return 42, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}, func(value int) { applied = value == 42 })
	if applied {
		t.Fatal("request blocked or applied synchronously")
	}
	close(release)
	select {
	case fn := <-posted:
		if applied {
			t.Fatal("worker mutated UI before dispatch")
		}
		fn()
	case <-time.After(time.Second):
		t.Fatal("request did not post result")
	}
	if !applied {
		t.Fatal("result was not applied on dispatcher")
	}
}

func TestLateRequestErrorDoesNotChangeAnotherUserPage(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	posted, release := make(chan func(), 1), make(chan struct{})
	app.dispatch = func(fn func()) { posted <- fn }
	currentUID := int64(1)
	requestGuarded(app, "user-dynamics-1", func() bool { return currentUID == 1 }, func(context.Context) (int, error) {
		<-release
		return 0, errors.New("old user request failed")
	}, func(int) { t.Error("late success changed another page") }, nil)
	currentUID = 2
	close(release)
	select {
	case apply := <-posted:
		apply()
	case <-time.After(time.Second):
		t.Fatal("late request did not finish")
	}
	if app.BannerError != "" || app.UserDynamicsError != "" {
		t.Fatal("old request error contaminated current page")
	}
}

type failedImageBackend struct {
	service.BackendAPI
	hits atomic.Int32
}

func (b *failedImageBackend) FetchImage(context.Context, string) ([]byte, error) {
	b.hits.Add(1)
	return nil, errors.New("synthetic unavailable image")
}

func TestUnavailableThumbnailIsNotRetriedEveryFrame(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	backend := &failedImageBackend{BackendAPI: app.backend}
	app.backend = backend
	posted := make(chan func(), 1)
	app.dispatch = func(fn func()) { posted <- fn }
	const url = "https://image.example.invalid/unavailable.png"
	app.CurrentRoute = RouteUser
	app.lookupRemoteBitmap(url)
	select {
	case apply := <-posted:
		apply()
	case <-time.After(time.Second):
		t.Fatal("image request did not finish")
	}
	for i := 0; i < 20; i++ {
		app.lookupRemoteBitmap(url)
	}
	if backend.hits.Load() != 1 {
		t.Fatalf("unavailable image was retried %d times", backend.hits.Load())
	}
	// DEC-040: after a pause it is tried again, a few times at most, so a
	// passing failure does not leave the placeholder for the session.
	key := "public-image:" + url
	for want := int32(2); want <= remoteImageTries+1; want++ {
		r := app.imageRetry[key]
		r.at = time.Now().Add(-time.Second)
		app.imageRetry[key] = r
		for i := 0; i < 5; i++ {
			app.lookupRemoteBitmap(url)
		}
		if want <= remoteImageTries {
			select {
			case apply := <-posted:
				apply()
			case <-time.After(time.Second):
				t.Fatal("image retry did not finish")
			}
		}
		if got, max := backend.hits.Load(), min(want, remoteImageTries); got != max {
			t.Fatalf("after the pause: %d fetches, want %d", got, max)
		}
	}
}

func seedUIFeed(t *testing.T, app *App, count int) {
	t.Helper()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	lib := app.backend.(*service.Backend).LibraryStore()
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("%d", 100000+i)
		_, err := lib.SaveItem(filepath.Join(lib.CacheDir(), "dynamic", id), library.ItemMetadata{Site: "bilibili", Kind: "dynamic", ID: id, Title: "synthetic cached post " + id, Text: "cached post for viewport and pagination regression", PubTs: int64(i + 1), Author: library.ItemAuthor{Mid: 1001, Name: "synthetic author"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	app.CompleteLogin()
	app.Navigate(RouteHome)
}

// DEC-040: progress events and navigation do not move the timeline; a new
// session opens at the newest post (no restored reading position), and the
// newest post seen is stored only as the next session's "有更新" baseline.
func TestFeedProgressEventsKeepPositionAndRestartOpensAtNewest(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 80)
	tt := ui.NewTester(app.View, 1000, 860)
	viewport, ok := tt.Find("主页信息流列表")
	if !ok {
		t.Fatal("feed list missing")
	}
	for _, removed := range []string{"切换排序", "最新在前", "最早在前"} {
		if _, found := tt.Find(removed); found {
			t.Fatalf("unexpected sort toggle control %q on /home", removed)
		}
	}
	if seen := app.backend.Anchor(); seen.Key != app.FeedItems[0].FeedKey || seen.PubTs != app.FeedItems[0].PubTs {
		t.Fatalf("the newest post on screen was not recorded as seen: %+v", seen)
	}
	tt.Scroll(viewport.X+viewport.W/2, viewport.Y+100, 0, 463)
	tt.Frame()
	first, _ := app.FeedListState.Visible()
	key, offset, count := app.FeedItems[first].FeedKey, app.FeedScroll.Y, len(app.FeedItems)
	app.handleBackendEvent(service.Event{Type: service.EventTaskUpdated, Message: "synthetic-progress"})
	tt.Frame()
	after, _ := app.FeedListState.Visible()
	if len(app.FeedItems) != count || app.FeedItems[after].FeedKey != key || app.FeedScroll.Y != offset {
		t.Fatal("task progress reloaded or moved the feed")
	}
	if _, found := tt.Find("条更新的内容"); found {
		t.Fatal("the timeline still announces unread posts")
	}
	app.Navigate(RouteParse)
	app.Navigate(RouteHome)
	tt.Frame()
	if app.FeedScroll.Y != offset {
		t.Fatal("switching pages lost the feed scroll offset within the session")
	}
	newest := app.FeedItems[0]
	restored := NewApp(app.backend, player.NewNullAudioSink())
	defer restored.Close()
	restored.Navigate(RouteHome)
	view := ui.NewTester(restored.View, 1000, 860)
	for i := 0; i < 4; i++ {
		view.Frame()
	}
	index, _ := restored.FeedListState.Visible()
	if index != 0 || restored.FeedItems[0].FeedKey != newest.FeedKey || restored.FeedScroll.Y != 0 {
		t.Fatalf("a new session must open at the newest post, got first=%d scroll=%v", index, restored.FeedScroll.Y)
	}
	if restored.homeBaselineTs != newest.PubTs {
		t.Fatalf("the next session's baseline = %d, want the newest post seen %d", restored.homeBaselineTs, newest.PubTs)
	}
}

func TestThumbnailCoverAndPlayOverlayShareFullArea(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	image := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			image.SetRGBA(x, y, color.RGBA{R: 230, G: 30, B: 40, A: 255})
		}
	}
	app.imageCache["fixture::cover.png"] = ui.NewBitmap(image)
	tester := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(240).Height(240).Children(func() {
			app.renderMediaThumb(c, "fixture", "cover.png", "fixture", "", "", true, 240, 240)
		})
	}, 240, 240)
	screenshot := tester.Image()
	for _, point := range []struct{ x, y int }{{120, 30}, {120, 210}, {30, 120}} {
		pixel := screenshot.RGBAAt(point.x, point.y)
		if pixel.R < 200 || pixel.G > 80 || pixel.B > 90 {
			t.Fatalf("cover does not fill thumbnail at %+v: %+v", point, pixel)
		}
	}
}

func TestLoginModesAndLogoutConfirmation(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	tester := ui.NewTester(app.View, 1000, 860)
	if _, shown := tester.Find("手动Cookie输入框"); shown {
		t.Fatal("manual login fields are shown in QR mode")
	}
	tester.Click("登录标签：粘贴凭证")
	if _, shown := tester.Find("手动Cookie输入框"); !shown {
		t.Fatal("manual login tab has no fields")
	}
	if _, shown := tester.Find("获取登录二维码"); shown {
		t.Fatal("QR controls remain in manual mode")
	}
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()
	app.Navigate(RouteSettings)
	tester.Frame()
	tester.Click("退出登录")
	if !app.ConfirmLogoutOpen || !app.backend.AccountStatus().LoggedIn {
		t.Fatal("logout happened before confirmation")
	}
	tester.Click("取消退出登录")
	if app.ConfirmLogoutOpen || !app.backend.AccountStatus().LoggedIn {
		t.Fatal("cancel logout changed account")
	}
	tester.Click("退出登录")
	tester.Click("确认退出登录")
	if app.backend.AccountStatus().LoggedIn || app.CurrentRoute != RouteLogin {
		t.Fatal("confirmed logout did not finish")
	}
}

type upgradeTestBackend struct {
	service.BackendAPI
	candidates []library.ItemMetadata
	checked    []library.ItemMetadata
	err        error
	started    chan struct{}
	hits       atomic.Int32
}

func (b *upgradeTestBackend) UpgradeCandidates(int) ([]library.ItemMetadata, error) {
	return b.candidates, nil
}

func (b *upgradeTestBackend) CheckUpgradeKeys(ctx context.Context, _ []string, _ bool) ([]library.ItemMetadata, error) {
	b.hits.Add(1)
	if b.started != nil {
		close(b.started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return b.checked, b.err
}

func TestQualityCheckDistinguishesFailureFromCancellationAndCountsUpgrades(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	backend := &upgradeTestBackend{BackendAPI: app.backend, candidates: []library.ItemMetadata{{ItemKey: "fixture"}}, err: errors.New("synthetic network failure")}
	app.backend = backend
	app.checkQualityUpgrades()
	if !strings.Contains(app.BannerError, "synthetic network failure") || strings.Contains(app.BannerInfo, "已取消") {
		t.Fatalf("ordinary failure was mislabeled as cancellation: %q / %q", app.BannerError, app.BannerInfo)
	}
	backend.err = nil
	backend.checked = []library.ItemMetadata{{ItemKey: "fixture", UpgradeAvailable: false}}
	app.checkQualityUpgrades()
	if !strings.Contains(app.BannerInfo, "0 个可升级") || app.UpgradeDone != 1 || app.UpgradeTotal != 1 {
		t.Fatalf("checked item was counted as an upgrade: %q, %d/%d", app.BannerInfo, app.UpgradeDone, app.UpgradeTotal)
	}
}

func TestCancelQualityCheckDoesNotRequestRemainingCandidates(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	backend := &upgradeTestBackend{BackendAPI: app.backend, candidates: []library.ItemMetadata{{ItemKey: "one"}, {ItemKey: "two"}, {ItemKey: "three"}}, started: make(chan struct{})}
	app.backend = backend
	posted := make(chan func(), 8)
	app.dispatch = func(fn func()) { posted <- fn }
	app.checkQualityUpgrades()
	select {
	case <-backend.started:
	case <-time.After(time.Second):
		t.Fatal("quality check did not start")
	}
	app.upgradeCancel()
	for app.pending["upgrade-check"] {
		select {
		case apply := <-posted:
			apply()
		case <-time.After(time.Second):
			t.Fatal("quality check did not finish after cancellation")
		}
	}
	if backend.hits.Load() != 1 || app.upgradeCancel != nil || app.BannerError != "" || !strings.Contains(app.BannerInfo, "已取消画质检查（0/3）") {
		t.Fatalf("canceled check continued or lost progress: calls=%d, %q / %q", backend.hits.Load(), app.BannerError, app.BannerInfo)
	}
}

func TestSubscriptionListVirtualizesAndSearchResetsScroll(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()
	app.Navigate(RouteSubscriptions)
	for i := 0; i < 1000; i++ {
		app.Subscriptions = append(app.Subscriptions, subscription.Subscription{ID: fmt.Sprintf("sub-%04d", i), Name: fmt.Sprintf("synthetic subscription %04d", i)})
	}
	tester := ui.NewTester(app.View, 1000, 860)
	first, last := app.SubListState.Visible()
	if first != 0 || last >= 20 {
		t.Fatalf("subscription grid rendered too many rows for 1000 subscriptions: %d..%d", first, last)
	}
	cols := 4
	targetIdx := 720
	app.SubListState.ScrollTo(targetIdx/cols, ui.Start)
	tester.Frame()
	tester.Frame()
	if _, found := tester.Find(fmt.Sprintf("选择订阅 sub-%04d", targetIdx)); !found {
		t.Fatalf("later subscriptions in 1000-item grid are unreachable (row=%d)", targetIdx/cols)
	}
	app.SubSearchQuery = "synthetic subscription 0003"
	tester.Frame()
	tester.Frame()
	if _, found := tester.Find("选择订阅 sub-0003"); !found {
		t.Fatal("search did not reset the virtual grid scroll")
	}
	app.pending["check-updates"] = true
	app.handleBackendEvent(service.Event{Type: service.EventSubscriptionChecked, Message: "progress:2/5"})
	if app.CheckDone != 2 || app.CheckTotal != 5 || len(app.Subscriptions) != 1000 {
		t.Fatal("check progress reloaded subscriptions or lost counts")
	}
	delete(app.pending, "check-updates")
}

// TestSubscriptionGridParseStatusLogsAndTaskRemoval covers the subscription
// avatar grid and detail, the parse page status, the logs settings and the
// confirmation before a failed task is removed.
func TestSubscriptionGridParseStatusLogsAndTaskRemoval(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()

	// Right column no longer has "最近更新" or "最近看过的用户" cards (moved to Home updated avatars row under the "主页" title).
	app.Navigate(RouteHome)
	tt := ui.NewTester(app.View, 1280, 860)
	for _, removedCard := range []string{"最近看过的用户", "最近更新"} {
		if _, ok := tt.Find(removedCard); ok {
			t.Fatalf("expected right column card %q to be removed (DEC-037)", removedCard)
		}
	}
	subA, _ := app.backend.CreateSubscription("Creator A", "note A", []subscription.AccountRef{{Site: "bilibili", UID: "1001", Name: "AccA"}}, subscription.SubRules{ScheduledCheck: true})
	subB, _ := app.backend.CreateSubscription("Creator B", "note B", []subscription.AccountRef{{Site: "bilibili", UID: "2002", Name: "AccB"}}, subscription.SubRules{ScheduledCheck: true})
	app.RefreshData()
	_ = subB

	// /subscriptions circular avatar grid, live badge one-click record, and clicking avatar opens /subscriptions/:id detail with 5 tabs and 管理 menu.
	// Also seed a legacy recorded live item with Quality/Codec only in Live.Segments[0] to verify the quality fallback in Subscriptions and Library.
	liveItemDir := filepath.Join(t.TempDir(), "bilibili", "1001_AccA", "2026-03-01_live_8888_存量直播录像")
	_ = os.MkdirAll(liveItemDir, 0o700)
	_ = os.WriteFile(filepath.Join(liveItemDir, "part-001.mp4"), []byte("synthetic"), 0o600)
	savedLiveItem, err := app.backend.(*service.Backend).LibraryStore().SaveItem(liveItemDir, library.ItemMetadata{
		Version:  1,
		ID:       "live_8888_1",
		RawID:    "8888",
		Site:     "bilibili",
		Kind:     "live",
		Status:   "complete",
		Title:    "存量直播录像",
		Author:   library.ItemAuthor{Mid: 1001, Name: "AccA"},
		Duration: 60,
		Media: []library.MediaFile{
			{Role: "live_segment", Path: "part-001.mp4", Duration: 60},
		},
		Live: &library.LiveRecordingInfo{
			RoomID: 8888,
			Segments: []library.LiveSegmentInfo{
				{File: "part-001.mp4", DurationSec: 60, Quality: 10000, Codec: "avc"},
			},
		},
	})
	if err != nil {
		t.Fatalf("SaveItem live: %v", err)
	}
	app.RefreshData()
	app.LiveRooms = []live.RoomState{
		{UID: 1001, RoomID: 8888, Uname: "AccA", Title: "Live Stream", LiveStatus: 1, Quality: 10000, QualityName: "原画", Codec: "avc"},
	}
	app.backend = &visibleStatusBackend{BackendAPI: app.backend, rooms: map[int64]live.RoomState{1001: app.LiveRooms[0]}}
	app.Navigate(RouteSubscriptions)
	tt.Frame()
	if _, ok := tt.Find("网格开始录制 8888"); !ok {
		t.Fatal("expected live badge button 网格开始录制 8888 on subscription avatar grid")
	}
	tt.Click("选择订阅 " + subA.ID)
	tt.Frame()
	if !app.SubDetailOpen || app.SelectedSubID != subA.ID {
		t.Fatalf("expected clicking avatar to open subscription detail for %s", subA.ID)
	}
	// DEC-040: no "更新", "已缓存" or "规则" tabs; the rule is one button.
	for _, tabLabel := range []string{"订阅标签：直播", "订阅标签：账号", "订阅缓存规则：默认"} {
		if _, ok := tt.Find(tabLabel); !ok {
			t.Fatalf("expected %s in subscription detail", tabLabel)
		}
	}
	for _, gone := range []string{"订阅标签：更新", "订阅标签：已缓存", "订阅标签：规则", "检查该订阅更新"} {
		if _, ok := tt.Find(gone); ok {
			t.Fatalf("subscription detail still shows %s", gone)
		}
	}
	if got := menuItems(t, tt, "订阅管理菜单"); strings.Join(got, ",") != "检查更新,改名,备注,更换头像,-,删除订阅" {
		t.Fatalf("订阅管理菜单 items = %v", got)
	}
	tt.Click("订阅标签：直播")
	tt.Frame()
	if _, ok := tt.Find("· 原画 · AVC"); !ok {
		t.Fatal("expected 原画 · AVC quality/codec text in subscription live tab")
	}
	if _, ok := tt.Find("已录制的直播录像（1）"); !ok {
		t.Fatal("expected 已录制的直播录像（1） section in subscription live tab")
	}
	if _, ok := tt.Find("时长 01:00 · 1 段 · 原画 · AVC"); !ok {
		t.Fatal("expected legacy Segments[0] quality fallback (时长 01:00 · 1 段 · 原画 · AVC) in subscription live tab")
	}

	// Verify Library card and "⋯" menu + detail view show legacy Segments[0] quality fallback.
	app.Navigate(RouteLibrary)
	app.ExpandedItemKey = savedLiveItem.ItemKey
	tt.Frame()
	if _, ok := tt.Find("AccA · 原画 · AVC"); !ok {
		t.Fatal("expected AccA · 原画 · AVC on library live card from Segments[0] fallback")
	}
	if got := tt.Menu(); strings.Join(got, ",") != "详情,在资源管理器中显示,-,删除" {
		t.Fatalf("expected the library ⋯ menu to float open when ExpandedItemKey is set, got %v", got)
	}
	if err := tt.ChooseMenuItem("详情"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if app.CurrentRoute != RouteVideo || app.CurrentLibraryDetail == nil || app.CurrentLibraryDetail.Kind != "live" {
		t.Fatalf("expected clicking 详情 in library ⋯ menu to open /video detail, got route=%s", app.CurrentRoute)
	}
	if _, ok := tt.Find("直播间: 8888 · 原画 · AVC"); !ok {
		t.Fatal("expected 直播间: 8888 · 原画 · AVC on video/live detail page opened from library ⋯ menu")
	}

	// Parse page never shows unrelated background loading status; Settings has "日志" category with background status & monospace logs.
	app.pending["upgrade-check"] = true
	app.Navigate(RouteParse)
	tt.Frame()
	if _, shown := tt.Find("请求进行中"); shown {
		t.Fatal("Parse route must not display unrelated background loading status")
	}
	delete(app.pending, "upgrade-check")

	app.Navigate(RouteSettings)
	tt.Frame()
	if _, ok := tt.Find("设置分类：日志"); !ok {
		t.Fatal("expected 设置分类：日志 in Settings")
	}
	tt.Click("设置分类：日志")
	tt.Frame()
	for _, label := range []string{"索引重建", "检查更新", "自动缓存", "直播录制", "日志页打开日志文件夹", "复制运行日志"} {
		if _, ok := tt.Find(label); !ok {
			t.Fatalf("expected %s in Settings -> 日志 category", label)
		}
	}

	// Tasks page shows LastError inline and prompts confirmation ("会删除未完成的临时文件") before removing a failed task.
	app.Tasks = []download.Task{
		{
			TaskID:       "task-fail-1",
			Site:         "bilibili",
			Kind:         "video",
			TargetID:     "BV1fail0001",
			Title:        "失败测试任务",
			Status:       "failed",
			ErrorKind:    "network",
			ErrorMessage: "分段连接超时",
			LastError:    "分段连接超时",
		},
	}
	app.Navigate(RouteTasks)
	tt.Frame()
	if _, ok := tt.Find("原因 [network]：分段连接超时"); !ok {
		t.Fatal("expected inline LastError reason on failed task row")
	}
	tt.Click("移除任务：task-fail-1")
	tt.Frame()
	if app.TaskRemoveConfirmID != "task-fail-1" {
		t.Fatalf("expected clicking 移除 on failed task to open confirmation prompt, got TaskRemoveConfirmID=%q", app.TaskRemoveConfirmID)
	}
	if _, ok := tt.Find("确认移除该任务？会删除未完成的临时文件，已缓存完成的文件会保留。"); !ok {
		t.Fatal("expected confirmation prompt mentioning 会删除未完成的临时文件")
	}
	tt.Click("取消移除任务：task-fail-1")
	tt.Frame()
	if app.TaskRemoveConfirmID != "" {
		t.Fatal("expected cancel to dismiss failed task remove confirmation")
	}
}

// TestHomePostsUpdatedRowParseHistoryAndLibraryMenus covers posts without
// text buttons, the row of updated accounts, the parse history menu and the
// library tile menu (DEC-037).
func TestHomePostsUpdatedRowParseHistoryAndLibraryMenus(t *testing.T) {
	app, desk, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()

	lib := app.backend.(*service.Backend).LibraryStore()
	hist := app.backend.(*service.Backend).HistoryStore()

	// Seed 2 subscriptions (SubAlpha with 2 linked accounts 1001 & 1002, SubBeta with account 2001) and 1 unsubscribed creator (3001).
	subAlpha, err := app.backend.CreateSubscription("SubAlpha", "", []subscription.AccountRef{
		{Site: "bilibili", UID: "1001", Name: "AlphaMain"},
		{Site: "bilibili", UID: "1002", Name: "AlphaAlt"},
	}, subscription.SubRules{})
	if err != nil {
		t.Fatal(err)
	}
	subBeta, err := app.backend.CreateSubscription("SubBeta", "", []subscription.AccountRef{
		{Site: "bilibili", UID: "2001", Name: "BetaCreator"},
	}, subscription.SubRules{})
	if err != nil {
		t.Fatal(err)
	}

	// Seed old cached item at pubTs=1710000100 (this will be the last-viewed anchor),
	// and newer cached items for SubAlpha (on secondary account 1002 at pubTs=1710000500),
	// SubBeta (video + referenced dynamic at pubTs=1710000800), and unsubscribed creator 3001 (at pubTs=1710000600).
	saveCachedPost := func(id string, mid int64, author string, pubTs int64, videos []string) library.ItemMetadata {
		dir := filepath.Join(lib.CacheDir(), "dynamic", id)
		_ = os.MkdirAll(filepath.Join(dir, "images"), 0o700)
		img := image.NewRGBA(image.Rect(0, 0, 8, 8))
		img.SetRGBA(0, 0, color.RGBA{R: 200, G: 60, B: 90, A: 255})
		var f *os.File
		f, _ = os.Create(filepath.Join(dir, "images", "01.png"))
		_ = png.Encode(f, img)
		_ = f.Close()
		it, err := lib.SaveItem(dir, library.ItemMetadata{
			Site:   "bilibili",
			Kind:   "dynamic",
			ID:     id,
			Title:  "Cached Post " + id,
			Text:   "Body of " + id,
			PubTs:  pubTs,
			Author: library.ItemAuthor{Mid: mid, Name: author},
			Media:  []library.MediaFile{{Role: "image", Path: "images/01.png", Width: 8, Height: 8}},
			Videos: videos,
		})
		if err != nil {
			t.Fatal(err)
		}
		return it
	}

	vidDir := filepath.Join(lib.CacheDir(), "video", "BV1beta001")
	_ = os.MkdirAll(vidDir, 0o700)
	_ = os.WriteFile(filepath.Join(vidDir, "P01.mp4"), []byte("synthetic-mp4"), 0o600)
	_, err = lib.SaveItem(vidDir, library.ItemMetadata{
		Site:             "bilibili",
		Kind:             "video",
		ID:               "BV1beta001",
		Title:            "Beta Cached Video",
		PubTs:            1710000750,
		Duration:         95,
		UpgradeAvailable: true,
		UpgradeHint:      "可升级至 4K",
		Author:           library.ItemAuthor{Mid: 2001, Name: "BetaCreator"},
		Media:            []library.MediaFile{{Role: "video", Path: "P01.mp4", Duration: 95}},
		Parts:            []library.VideoPartRecord{{CID: 1, Page: 1, Title: "P1", Duration: 95, File: "P01.mp4"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	oldItem := saveCachedPost("dyn_old", 1001, "AlphaMain", 1710000100, nil)
	_ = saveCachedPost("dyn_alpha_new", 1002, "AlphaAlt", 1710000500, nil)
	_ = saveCachedPost("dyn_unsub_new", 3001, "UnsubUP", 1710000600, nil)
	betaDyn := saveCachedPost("dyn_beta_new", 2001, "BetaCreator", 1710000800, []string{"BV1beta001"})

	// Save anchor at oldItem (pubTs=1710000100) so SubBeta (800), UnsubUP (600), and SubAlpha (500) all have updates after the anchor!
	if err := app.backend.SaveAnchor(uistate.FeedAnchor{Order: "newest", Key: oldItem.ItemKey, PubTs: oldItem.PubTs}); err != nil {
		t.Fatal(err)
	}
	app.homeBaselineLoaded = false // as after a restart: the stored value is the previous session's
	app.FeedItems = nil
	app.RefreshData()
	app.Navigate(RouteHome)

	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()

	// Verify Home posts have ZERO bottom text buttons!
	for _, forbiddenBtn := range []string{"内置播放", "查看图片", "播放视频", "升级缓存", "补存弹幕和字幕", "查看封面", "浏览器打开", "复制链接"} {
		if _, found := tt.Find(forbiddenBtn); found {
			t.Fatalf("Home feed must not display button %q", forbiddenBtn)
		}
	}

	// Verify "主页有更新头像行" is visible and ordered by latestTs DESC: SubBeta -> UnsubUP -> SubAlpha.
	// Also verify sitting idle or navigating /home -> /parse -> /home keeps the updated people.
	tt.Advance(1500 * time.Millisecond)
	tt.Frame()
	app.Navigate(RouteParse)
	tt.Frame()
	app.Navigate(RouteHome)
	tt.Frame()
	// DEC-040: viewing the timeline records the newest post seen for the
	// next session, but this session's "有更新" baseline does not move.
	if app.homeBaselineTs != oldItem.PubTs {
		t.Fatalf("viewing Home moved this session's baseline: got %d, want %d", app.homeBaselineTs, oldItem.PubTs)
	}
	actors := app.homeUpdatedActors()
	if len(actors) != 3 || actors[0].Name != "SubBeta" || actors[1].Name != "UnsubUP" || actors[2].Name != "SubAlpha" {
		t.Fatalf("unexpected Home updated actors order: %+v", actors)
	}
	if _, ok := tt.Find("主页有更新头像行"); !ok {
		t.Fatal("expected 主页有更新头像行 to be rendered on Home")
	}

	// Click SubAlpha avatar in the row -> enters single-subscription view on Home, marks SubAlpha seen in ui-state.json.
	tt.Click("主页更新头像：SubAlpha")
	tt.Frame()
	if app.HomeActorFilter == nil || app.HomeActorFilter.SubID != subAlpha.ID {
		t.Fatalf("expected HomeActorFilter to be set to SubAlpha, got %+v", app.HomeActorFilter)
	}
	if _, ok := tt.Find("返回主页时间线"); !ok {
		t.Fatal("expected 返回主页时间线 back arrow in single-subscription Home header")
	}
	// DEC-039: the people with updates are in the context panel, which
	// stays beside a single subscription's timeline with that one chosen;
	// the timeline itself shows no row of avatars.
	if row, ok := tt.Find("主页有更新头像行"); ok {
		if center, _ := tt.Find("中栏内容"); row.X >= center.X {
			t.Fatal("updated avatars row must not be shown inside single-subscription view")
		}
	}
	// Verify all items in single-subscription view belong to SubAlpha's linked accounts (1001 and 1002).
	if len(app.FeedItems) != 2 || app.FeedItems[0].RawID != "dyn_alpha_new" || app.FeedItems[1].RawID != "dyn_old" {
		t.Fatalf("expected SubAlpha's 2 items from newest to oldest, got %+v", app.FeedItems)
	}
	if seenTs := app.backend.GetUIState().HomeSeen["sub:"+subAlpha.ID]; seenTs != 1710000500 {
		t.Fatalf("expected Q-009 HomeSeen[sub:%s]=1710000500, got %d", subAlpha.ID, seenTs)
	}

	// Click back arrow -> returns to main timeline, and SubAlpha is gone from updated avatars row!
	tt.Click("返回主页时间线")
	tt.Frame()
	if app.HomeActorFilter != nil {
		t.Fatal("expected HomeActorFilter to be nil after clicking 返回主页时间线")
	}
	if app.homeBaselineTs != oldItem.PubTs {
		t.Fatalf("single-subscription view moved the session baseline: got %d, want %d", app.homeBaselineTs, oldItem.PubTs)
	}
	actorsAfter := app.homeUpdatedActors()
	if len(actorsAfter) != 2 || actorsAfter[0].Name != "SubBeta" || actorsAfter[1].Name != "UnsubUP" {
		t.Fatalf("expected SubAlpha to disappear from updated avatars row after viewing, got %+v", actorsAfter)
	}

	// Open SubBeta in single-subscription view and click the embedded video card on betaDyn -> opens PlayerOverlay.
	tt.Click("主页更新头像：SubBeta")
	tt.Frame()
	tt.Click("播放视频卡片：bilibili:video:BV1beta001")
	tt.Frame()
	if !app.PlayerOverlay.Open || app.PlayerOverlay.ItemKey != "bilibili:video:BV1beta001" {
		t.Fatalf("expected clicking video card on Home post to open PlayerOverlay for BV1beta001, got %+v", app.PlayerOverlay)
	}
	app.closePlayerOverlay()
	tt.Frame()
	tt.Click("返回主页时间线")
	tt.Frame()

	// Mark remaining actor UnsubUP seen -> updated avatars row disappears completely ("没有时整行不显示").
	tt.Click("主页更新头像：UnsubUP")
	tt.Frame()
	tt.Click("返回主页时间线")
	tt.Frame()
	if _, ok := tt.Find("主页有更新头像行"); ok {
		t.Fatal("expected 主页有更新头像行 to be hidden when no actors have unseen updates")
	}
	_ = betaDyn
	_ = subBeta

	// Parse page local thumbnails, click body to open, right-side "⋯" menu, and header "⋯" clear confirmation.
	avThumb, err := hist.SaveThumb("video", "BV1xx411c7mD", "avatar", ".png", []byte("png"))
	if err != nil {
		t.Fatal(err)
	}
	cvThumb, err := hist.SaveThumb("video", "BV1xx411c7mD", "cover", ".png", []byte("png"))
	if err != nil {
		t.Fatal(err)
	}
	_ = hist.Record(storage.HistoryEntry{
		Site:         "bilibili",
		Kind:         "video",
		ID:           "BV1xx411c7mD",
		Title:        "缓存缩略图测试视频",
		Subtitle:     "测试UP主",
		CanonicalURL: "https://www.bilibili.com/video/BV1xx411c7mD",
		AvatarThumb:  avThumb,
		CoverThumb:   cvThumb,
		DurationText: "02:00",
	})
	app.HistoryItems = app.backend.ListHistory()
	app.Navigate(RouteParse)
	tt.Frame()
	for _, oldHistBtn := range []string{"历史复制链接：BV1xx411c7mD", "历史浏览器打开：BV1xx411c7mD", "删除历史：BV1xx411c7mD"} {
		if _, found := tt.Find(oldHistBtn); found {
			t.Fatalf("history action %q must be hidden until ⋯ menu is opened", oldHistBtn)
		}
	}
	chooseMenuItem(t, tt, "历史更多：BV1xx411c7mD", "在浏览器中打开")
	if acts := desk.Actions(); len(acts) == 0 || acts[len(acts)-1].Kind != "url" {
		t.Fatalf("expected url desktop action from history ⋯ menu, got %+v", acts)
	}
	// Header "⋯" -> "全部清空" -> confirmation -> clears history and deletes local thumbs.
	chooseMenuItem(t, tt, "最近解析更多", "全部清空")
	tt.Frame()
	if !app.HistoryClearConfirmOpen {
		t.Fatal("expected clicking 全部清空 to open confirmation bar first")
	}
	tt.Click("确认清空历史")
	tt.Frame()
	if len(app.HistoryItems) != 0 {
		t.Fatalf("expected 0 history items after confirmed clear, got %d", len(app.HistoryItems))
	}
	if _, err := app.backend.HistoryThumbPath(cvThumb); err == nil {
		t.Fatal("expected confirmed clear to delete cached thumbnail files in thumbs/")
	}

	// On /user (non-Home PostCard), uncached dynamic has only a single "缓存" button at the bottom and a "⋯" button in the header.
	app.CurrentUser = &bilibili.UserSpace{Mid: 2001, Name: "BetaCreator", CanonicalURL: "https://space.bilibili.com/2001"}
	app.UserTab = "dynamics"
	app.UserDynamics = []bilibili.DynamicItem{
		{
			ID:           "dyn_uncached_user_1",
			Type:         "DYNAMIC_TYPE_WORD",
			TypeLabel:    "文字动态",
			Title:        "Uncached User Dynamic",
			Text:         "Uncached dynamic body",
			CanonicalURL: "https://t.bilibili.com/dyn_uncached_user_1",
			Author:       bilibili.AuthorInfo{Mid: 2001, Name: "BetaCreator"},
		},
	}
	app.CurrentRoute = RouteUser
	tt.Frame()
	// a post carries no text buttons; caching, the browser and the
	// link are items of the floating "⋯" menu, which takes no room in the page.
	for _, gone := range []string{"缓存动态：dyn_uncached_user_1", "在浏览器中打开 dyn:dyn_uncached_user_1", "帖子菜单：dyn:dyn_uncached_user_1"} {
		if _, found := tt.Find(gone); found {
			t.Fatalf("PostCard still shows %q in the page", gone)
		}
	}
	if got := menuItems(t, tt, "帖子更多：dyn:dyn_uncached_user_1"); strings.Join(got, ",") != "缓存,在浏览器中打开,复制链接" {
		t.Fatalf("PostCard ⋯ items = %v", got)
	}

	// Library header has no "重建索引" (moved to Settings -> "存储"); tile has only bottom-right "⋯" menu; "详情" opens /video with "补存弹幕和字幕", "删除" opens DeleteConfirm.
	app.Navigate(RouteLibrary)
	tt.Frame()
	if _, found := tt.Find("重建索引"); found {
		t.Fatal("重建索引 must be moved out of /library header to Settings -> 存储")
	}
	chooseMenuItem(t, tt, "媒体更多：bilibili:video:BV1beta001", "删除")
	tt.Frame()
	if !app.DeleteConfirm.Open || app.DeleteConfirm.Item.ItemKey != "bilibili:video:BV1beta001" {
		t.Fatalf("expected 删除 in library ⋯ menu to open DeleteConfirm modal, got %+v", app.DeleteConfirm)
	}
	app.DeleteConfirm.Open = false
	tt.Frame()

	chooseMenuItem(t, tt, "媒体更多：bilibili:video:BV1beta001", "详情")
	tt.Frame()
	if app.CurrentRoute != RouteVideo || app.CurrentVideo == nil || app.CurrentVideo.BVID != "BV1beta001" {
		t.Fatalf("expected 详情 in library ⋯ menu to open /video for BV1beta001, got route=%s video=%+v", app.CurrentRoute, app.CurrentVideo)
	}
	if _, ok := tt.Find("视频详情升级缓存"); !ok {
		t.Fatal("expected 视频详情升级缓存 on /video detail page")
	}
	if _, ok := tt.Find("补全：bilibili:video:BV1beta001"); !ok {
		t.Fatal("expected 补存弹幕和字幕 (补全：bilibili:video:BV1beta001) on /video detail page")
	}

	app.Navigate(RouteSettings)
	app.SettingsCategory = "storage"
	tt.Frame()
	if _, ok := tt.Find("重建索引"); !ok {
		t.Fatal("expected 重建索引 button inside Settings -> 存储")
	}
}

// chooseMenuItem opens the floating "⋯" menu of button and chooses item.
func chooseMenuItem(t *testing.T, tt *ui.Tester, button, item string) {
	t.Helper()
	if err := tt.Click(button); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(item); err != nil {
		t.Fatalf("%s > %s: %v (menu %v)", button, item, err, tt.Menu())
	}
}

// menuItems opens the floating "⋯" menu of button, returns its items ("-"
// for separators) and closes it.
func menuItems(t *testing.T, tt *ui.Tester, button string) []string {
	t.Helper()
	if err := tt.Click(button); err != nil {
		t.Fatal(err)
	}
	items := tt.Menu()
	tt.CloseMenu()
	return items
}
