package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/egoist/mygo/ui"
)

type cacheQueueMenuStub struct {
	*douyinStubBackend
	enqueued []service.CacheQueueRequest
	tasks    []service.CacheQueueTask
}

func (s *cacheQueueMenuStub) ListCacheQueue() []service.CacheQueueTask {
	out := make([]service.CacheQueueTask, len(s.tasks))
	copy(out, s.tasks)
	return out
}

func (s *cacheQueueMenuStub) FindCacheQueueTask(siteID, user, list string) (service.CacheQueueTask, bool) {
	for _, t := range s.tasks {
		if t.Site == siteID && t.User == user && t.List == list && isUnfinishedCacheQueueStatus(t.Status) {
			return t, true
		}
	}
	return service.CacheQueueTask{}, false
}

func (s *cacheQueueMenuStub) EnqueueCacheQueue(_ context.Context, req service.CacheQueueRequest) (service.CacheQueueTask, error) {
	s.enqueued = append(s.enqueued, req)
	label := "动态"
	if req.List == "videos" {
		label = "投稿"
	} else if req.List == "works" {
		label = "作品"
	}
	task := service.CacheQueueTask{
		ID:         fmt.Sprintf("cq_%s_%s_%s", req.Site, req.List, req.User),
		Site:       req.Site,
		User:       req.User,
		UserName:   req.UserName,
		List:       req.List,
		ListLabel:  label,
		Status:     service.CacheQueueQueued,
		StatusText: "排队中",
	}
	s.tasks = append(s.tasks, task)
	return task, nil
}

// The user page's "⋯" menu offers only "全部缓存" (or "在缓存队列中查看"
// when already queued), has no 10 / 20 / 50 count choices, enqueues without a
// confirmation dialog, and is absent on private Douyin accounts.
func TestUserPageCacheAllQueueEntry(t *testing.T) {
	app, stub, _, cleanup := newDouyinUITestHarness(t)
	defer cleanup()
	posted := make(chan func(), 32)
	app.dispatch = func(fn func()) { posted <- fn }
	_, _ = stub.raw.ImportCookie(context.Background(), "SESSDATA=fixture; bili_jct=csrf; DedeUserID=12345", "")
	_, _ = stub.LoginWithCookie(context.Background(), "douyin", "sessionid=synth_dy_cookie", "")
	cs := &cacheQueueMenuStub{douyinStubBackend: stub}
	app.backend = cs

	app.CurrentUser = &bilibili.UserSpace{Mid: 8888, Name: "测试UP主"}
	app.UserTab = "dynamics"
	for i := 0; i < 4; i++ {
		app.UserDynamics = append(app.UserDynamics, bilibili.DynamicItem{
			ID:        fmt.Sprintf("d%02d", i),
			TypeLabel: "文字动态",
			Text:      "正文",
			Author:    bilibili.AuthorInfo{Mid: 8888},
		})
	}
	app.UserDynamicsNext, app.UserDynamicsHasMore = "offset-4", true
	app.CurrentRoute = RouteUser

	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	if err := tt.Click("空间更多"); err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []string{"缓存更早的内容", "10 条", "20 条（推荐）", "50 条"} {
		if tt.HasText(legacy) {
			t.Fatalf("legacy count-based menu option %q still rendered", legacy)
		}
	}
	if err := tt.ChooseMenuItem("全部缓存"); err != nil {
		t.Fatalf("expected 全部缓存 menu item: %v", err)
	}
	nextPosted(t, posted)()
	waitUIIdle(t, app, posted)

	if len(cs.enqueued) != 1 {
		t.Fatalf("expected 1 EnqueueCacheQueue request, got %+v", cs.enqueued)
	}
	req := cs.enqueued[0]
	if req.Site != "bilibili" || req.List != "dynamics" || req.User != "8888" || len(req.Dynamics) != 4 || req.Cursor != "offset-4" || !req.HasMore {
		t.Fatalf("unexpected EnqueueCacheQueue request: %+v", req)
	}
	if app.BannerInfo != "已加入缓存队列，会在后台慢慢缓存" {
		t.Fatalf("unexpected banner message: %q", app.BannerInfo)
	}

	// Reopening "⋯" on the same list now offers "在缓存队列中查看" and jumps to that queue task.
	tt.Frame()
	if err := tt.Click("空间更多"); err != nil {
		t.Fatal(err)
	}
	if tt.HasText("全部缓存") {
		t.Fatal("expected 全部缓存 to be replaced by 在缓存队列中查看 while unfinished in queue")
	}
	if err := tt.ChooseMenuItem("在缓存队列中查看"); err != nil {
		t.Fatalf("expected 在缓存队列中查看 menu item: %v", err)
	}
	tt.Frame()
	if app.CurrentRoute != RouteTasks || app.effectiveTasksSection() != "queue" || app.SelectedCacheQueueID != "cq_bilibili_dynamics_8888" {
		t.Fatalf("expected jump to RouteTasks queue detail, got route=%s sec=%s id=%s", app.CurrentRoute, app.TasksSection, app.SelectedCacheQueueID)
	}

	// Private Douyin account must not offer "全部缓存" or "在缓存队列中查看".
	app.CurrentDouyinUser = &service.DouyinUserView{
		SecUID:    "MS4wLjABAAAA_private_acct",
		Nickname:  "私密测试号",
		IsPrivate: true,
	}
	app.CurrentRoute = RouteDouyinUser
	tt.Frame()
	if err := tt.Click("抖音作者更多"); err != nil {
		t.Fatal(err)
	}
	if tt.HasText("全部缓存") || tt.HasText("在缓存队列中查看") {
		t.Fatalf("private Douyin account must not expose cache-all entry: %v", tt.Texts())
	}
}

// DEC-046 ⑧: 未缓存 hides cached posts of the loaded pages without asking
// the site, and 全部 brings them back.
func TestUserPageUncachedFilter(t *testing.T) {
	app, stub, netCalls, cleanup := newDouyinUITestHarness(t)
	defer cleanup()
	_, _ = stub.raw.ImportCookie(context.Background(), "SESSDATA=fixture; bili_jct=csrf; DedeUserID=12345", "")
	root := stub.raw.GetSettings().CacheDir
	dir := filepath.Join(root, "bilibili", "8888", "dynamics", "d01")
	_ = os.MkdirAll(dir, 0o700)
	if _, err := stub.raw.LibraryStore().SaveItem(dir, library.ItemMetadata{Version: 1, ID: "d01", RawID: "d01", Site: "bilibili", Kind: "dynamic", Status: "complete",
		Author: library.ItemAuthor{Site: "bilibili", UID: "8888"}}); err != nil {
		t.Fatal(err)
	}
	app.CurrentUser = &bilibili.UserSpace{Mid: 8888, Name: "测试UP主"}
	app.UserTab = "dynamics"
	for i := 0; i < 3; i++ {
		app.UserDynamics = append(app.UserDynamics, bilibili.DynamicItem{ID: fmt.Sprintf("d%02d", i), TypeLabel: "文字动态", Text: fmt.Sprintf("合成正文%02d", i), Author: bilibili.AuthorInfo{Mid: 8888}})
	}
	app.CurrentRoute = RouteUser
	tt := ui.NewTester(app.View, 1280, 1400)
	tt.Frame()
	before := netCalls.Load()
	if !tt.HasText("合成正文01") {
		t.Fatal("全部 hides a cached post")
	}
	if err := tt.Click("用户页筛选：未缓存"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tt.HasText("合成正文01") || !tt.HasText("合成正文00") || !tt.HasText("合成正文02") {
		t.Fatalf("未缓存 shows the wrong posts: %v", tt.Texts())
	}
	if err := tt.Click("用户页筛选：全部"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("合成正文01") {
		t.Fatal("全部 does not bring the cached post back")
	}
	// A reload of the same length holds other posts: the filter follows the posts, not the count.
	if err := tt.Click("用户页筛选：未缓存"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	app.UserDynamics = nil
	for _, id := range []string{"d01", "d03", "d04"} {
		app.UserDynamics = append(app.UserDynamics, bilibili.DynamicItem{ID: id, TypeLabel: "文字动态", Text: "重载正文" + id, Author: bilibili.AuthorInfo{Mid: 8888}})
	}
	tt.Frame()
	if tt.HasText("重载正文d01") || !tt.HasText("重载正文d03") || !tt.HasText("重载正文d04") {
		t.Fatalf("未缓存 after a reload of the same length shows the wrong posts: %v", tt.Texts())
	}
	if netCalls.Load() != before {
		t.Fatal("switching the filter asked the site")
	}
}
