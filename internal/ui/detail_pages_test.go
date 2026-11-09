package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/egoist/mygo/ui"
)

// gatedOpenBackend holds every OpenLink until the test releases it, and
// fails the links listed in fail.
type gatedOpenBackend struct {
	service.BackendAPI
	release chan struct{}
	fail    map[string]bool
}

func (b *gatedOpenBackend) OpenLink(ctx context.Context, raw string) (*service.OpenLinkResult, error) {
	select {
	case <-b.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if b.fail[raw] {
		return nil, errors.New("synthetic open failure")
	}
	return b.BackendAPI.OpenLink(ctx, raw)
}

func nextPosted(t *testing.T, posted chan func()) func() {
	t.Helper()
	select {
	case fn := <-posted:
		return fn
	case <-time.After(2 * time.Second):
		t.Fatal("background work did not finish")
		return nil
	}
}

// F-057: a card click shows its page at once, before OpenLink returns; the
// newest click wins, a failure stays on the page with a retry, and a page
// the user left is not brought back by a late result.
func TestOpeningFromCardShowsPageBeforeOpenLinkReturns(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)
	backend := &gatedOpenBackend{BackendAPI: app.backend, release: make(chan struct{}, 4), fail: map[string]bool{"BV1yy411c7mE": true}}
	app.backend = backend
	posted := make(chan func(), 8)
	app.dispatch = func(fn func()) { posted <- fn }
	tt := ui.NewTester(app.View, 1000, 860)

	app.CurrentRoute = RouteUser
	app.OpenVideo("BV1xx411c7mD", "第一个视频", "测试UP主", 8888, "", "02:00")
	if app.CurrentRoute != RouteVideo || app.Opening == nil || app.Opening.Title != "第一个视频" || app.PreviousRoute != RouteUser {
		t.Fatalf("the video page did not show at once: route=%s opening=%+v", app.CurrentRoute, app.Opening)
	}
	tt.Frame()
	if _, ok := tt.Find("正在打开"); !ok {
		t.Fatal("the opening page does not show what the card knew")
	}

	// A second click before the first returns: the first result is dropped.
	app.OpenVideo("BV1yy411c7mE", "第二个视频", "测试UP主", 8888, "", "")
	backend.release <- struct{}{}
	backend.release <- struct{}{}
	for i := 0; i < 2; i++ {
		nextPosted(t, posted)()
	}
	if app.CurrentVideo != nil || app.Opening == nil || app.Opening.Title != "第二个视频" {
		t.Fatalf("an older click replaced the newest page: video=%v opening=%+v", app.CurrentVideo, app.Opening)
	}
	if app.Opening.Err == "" || app.BannerError != "" {
		t.Fatalf("the failure of the newest click is not shown on its page: %+v banner=%q", app.Opening, app.BannerError)
	}
	tt.Frame()
	if _, ok := tt.Find("重试打开"); !ok {
		t.Fatal("a failed open offers no retry")
	}

	// Retry succeeds once OpenLink does.
	backend.fail = nil
	app.RetryOpening()
	backend.release <- struct{}{}
	nextPosted(t, posted)()
	if app.Opening != nil || app.CurrentVideo == nil || app.CurrentRoute != RouteVideo {
		t.Fatalf("retry did not open the video: opening=%+v video=%v", app.Opening, app.CurrentVideo)
	}
	// The recent-parse thumbnails follow in the background and never block the page.
	for drained := false; !drained; {
		select {
		case fn := <-posted:
			fn()
		case <-time.After(200 * time.Millisecond):
			drained = true
		}
	}

	// Leaving the page while it opens keeps the user where they went.
	app.OpenUser(8888, "测试UP主")
	if app.CurrentRoute != RouteUser || app.Opening == nil {
		t.Fatalf("the user page did not show at once: route=%s", app.CurrentRoute)
	}
	app.Navigate(RouteLibrary)
	backend.release <- struct{}{}
	nextPosted(t, posted)()
	if app.CurrentRoute != RouteLibrary || app.Opening != nil {
		t.Fatalf("a late result pulled the user back: route=%s opening=%+v", app.CurrentRoute, app.Opening)
	}
}

func savePlayableItem(t *testing.T, app *App) library.ItemMetadata {
	t.Helper()
	itemDir := filepath.Join(t.TempDir(), "bilibili", "8888_测试UP主", "2026-03-01_video_BV1play0001_播放")
	if err := os.MkdirAll(itemDir, 0o700); err != nil {
		t.Fatal(err)
	}
	vBytes, err := download.BuildSyntheticVideoM4S(640, 360)
	if err != nil {
		t.Fatal(err)
	}
	aBytes, err := download.BuildSyntheticAudioM4S(15)
	if err != nil {
		t.Fatal(err)
	}
	vPath, aPath := filepath.Join(itemDir, "v.m4s"), filepath.Join(itemDir, "a.m4s")
	_ = os.WriteFile(vPath, vBytes, 0o600)
	_ = os.WriteFile(aPath, aBytes, 0o600)
	if _, err := download.MuxDASHFiles(vPath, aPath, filepath.Join(itemDir, "P01.mp4")); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(vPath)
	_ = os.Remove(aPath)
	_ = os.WriteFile(filepath.Join(itemDir, "P01.danmaku.xml"), []byte(`<i><d p="0.2,1,25,16777215,1710000000,0,u1,1">弹幕</d></i>`), 0o600)
	item, err := app.backend.(*service.Backend).LibraryStore().SaveItem(itemDir, library.ItemMetadata{
		Version: 1, ID: "BV1play0001", RawID: "BV1play0001", Site: "bilibili", Kind: "video", Status: "complete",
		Title: "播放", Author: library.ItemAuthor{Mid: 8888, Name: "测试UP主"},
		Media: []library.MediaFile{{Role: "video", Path: "P01.mp4"}, {Role: "danmaku", Path: "P01.danmaku.xml"}},
		Parts: []library.VideoPartRecord{{CID: 1, Page: 1, File: "P01.mp4", Danmaku: "P01.danmaku.xml"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

// F-057: the player layer shows at once; opening the file and parsing its
// danmaku run off the UI thread, and a result for a closed layer is dropped.
func TestPlayerOpensFilesOffTheUIThread(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("needs Media Foundation")
	}
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)
	item := savePlayableItem(t, app)
	posted := make(chan func(), 4)
	app.dispatch = func(fn func()) { posted <- fn }

	app.OpenPlayerOverlay(item)
	if !app.PlayerOverlay.Open || !app.PlayerOverlay.Loading || app.player.Snapshot(640, 360).Backend != "" {
		t.Fatalf("the file was opened on the UI thread: loading=%v backend=%q", app.PlayerOverlay.Loading, app.player.Snapshot(640, 360).Backend)
	}
	tt := ui.NewTester(app.View, 1000, 860)
	tt.Frame()
	if _, ok := tt.Find("播放器加载中"); !ok {
		t.Fatal("the player layer shows no progress while the file opens")
	}
	nextPosted(t, posted)()
	if snap := app.player.Snapshot(640, 360); app.PlayerOverlay.Loading || snap.Backend != "windows-mf" || app.PlayerOverlay.LoadError != "" {
		t.Fatalf("the opened file was not attached: loading=%v backend=%q err=%q", app.PlayerOverlay.Loading, snap.Backend, app.PlayerOverlay.LoadError)
	}

	app.closePlayerOverlay()
	app.OpenPlayerOverlay(item)
	app.closePlayerOverlay()
	nextPosted(t, posted)()
	if app.PlayerOverlay.Open || app.player.Snapshot(640, 360).Backend != "" {
		t.Fatal("a file opened for a closed player layer was attached")
	}
}

// F-058: a creator's posts fill the column; the header is row 0 and
// scrolls away instead of leaving the list a fixed strip. (The
// subscription's own "已缓存" list was removed in DEC-040.)
func TestDetailListsFillTheColumnAndScrollTheirHeader(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)
	app.CurrentUser = &bilibili.UserSpace{Mid: 8888, Name: "测试UP主"}
	app.UserTab = "dynamics"
	app.UserDynamics = nil
	for i := 0; i < 12; i++ {
		app.UserDynamics = append(app.UserDynamics, bilibili.DynamicItem{ID: fmt.Sprintf("d%02d", i), TypeLabel: "文字动态", Text: "一行正文", Author: bilibili.AuthorInfo{Mid: 8888, Name: "测试UP主"}})
	}
	app.LiveRooms = append(app.LiveRooms, live.RoomState{UID: 8888, RoomID: 7734200, LiveStatus: 1, Title: "直播标题"})
	app.CurrentRoute = RouteUser
	short := ui.NewTester(app.View, 925, 640)
	short.Frame()
	short.Frame()
	if first, last := app.UserDynListState.Visible(); first != 0 || last < 2 {
		t.Fatalf("a short window shows rows %d..%d of the creator page", first, last)
	}
	for _, gone := range []string{"在浏览器中打开空间", "浏览器观看"} {
		if _, found := short.Find(gone); found {
			t.Fatalf("the creator header still shows %q as a button", gone)
		}
	}
	if got := menuItems(t, short, "空间更多"); strings.Join(got, ",") != "在浏览器中打开空间,在浏览器中观看直播" {
		t.Fatalf("creator ⋯ items = %v", got)
	}
}

// Settings: the categories replace the left navigation as names only, and
// a choice shows its name, never the stored value.
func TestSettingsSidebarShowsNamesOnly(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)
	app.Navigate(RouteHome)
	app.Navigate(RouteSettings)
	app.SettingsCategory = "general"
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	for _, cat := range settingsCategories {
		if _, ok := tt.Find("设置分类：" + cat.Label); !ok {
			t.Fatalf("settings sidebar misses %s", cat.Label)
		}
	}
	// The icon rail stays (DEC-039); the panel holds the categories, not
	// another page's filters.
	for _, gone := range []string{"主页全部动态", "订阅侧栏：全部订阅", "登录的账号与自动续期状态", "关闭窗口行为与后台托盘内存释放", "tray"} {
		if _, found := tt.Find(gone); found {
			t.Fatalf("settings still shows %q", gone)
		}
	}
	if _, ok := tt.Find("最小化到托盘（推荐）"); !ok {
		t.Fatal("the close action does not show its name")
	}
	if err := tt.Click("返回"); err != nil {
		t.Fatal(err)
	}
	if app.CurrentRoute != RouteHome {
		t.Fatalf("leaving settings went to %s", app.CurrentRoute)
	}
	tt.Frame()
	if _, ok := tt.Find("主页"); !ok {
		t.Fatal("the main navigation did not come back")
	}
}
