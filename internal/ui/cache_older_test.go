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

type cacheOlderStub struct {
	*douyinStubBackend
	got []service.CacheOlderRequest
}

func (s *cacheOlderStub) CacheOlderUserItems(_ context.Context, req service.CacheOlderRequest) (service.CacheOlderResult, error) {
	s.got = append(s.got, req)
	return service.CacheOlderResult{Enqueued: req.Limit, Skipped: 2, Stopped: "遇到站点风控，已停止"}, nil
}

// DEC-046 ⑥: the user page's ⋯ offers 缓存更早的内容 with 10 / 20 / 50 per
// batch; the loaded list and where it stops go with the request, and the
// outcome, including a stop at risk control, is told.
func TestUserPageCachesOlderPosts(t *testing.T) {
	app, stub, _, cleanup := newDouyinUITestHarness(t)
	defer cleanup()
	posted := make(chan func(), 32)
	app.dispatch = func(fn func()) { posted <- fn }
	_, _ = stub.raw.ImportCookie(context.Background(), "SESSDATA=fixture; bili_jct=csrf; DedeUserID=12345", "")
	cs := &cacheOlderStub{douyinStubBackend: stub}
	app.backend = cs

	app.CurrentUser = &bilibili.UserSpace{Mid: 8888, Name: "测试UP主"}
	app.UserTab = "dynamics"
	for i := 0; i < 5; i++ {
		app.UserDynamics = append(app.UserDynamics, bilibili.DynamicItem{ID: fmt.Sprintf("d%02d", i), TypeLabel: "文字动态", Text: "正文", Author: bilibili.AuthorInfo{Mid: 8888}})
	}
	app.UserDynamicsNext, app.UserDynamicsHasMore = "offset-5", true
	app.CurrentRoute = RouteUser
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	if err := tt.Click("空间更多"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("缓存更早的内容", "20 条（推荐）"); err != nil {
		t.Fatal(err)
	}
	nextPosted(t, posted)()
	waitUIIdle(t, app, posted)
	if len(cs.got) != 1 {
		t.Fatalf("requests %+v", cs.got)
	}
	req := cs.got[0]
	if req.Site != "bilibili" || req.List != "dynamics" || req.User != "8888" || req.Limit != 20 || len(req.Dynamics) != 5 || req.Cursor != "offset-5" || !req.HasMore {
		t.Fatalf("request %+v", req)
	}
	if app.BannerInfo != "已把 20 条更早的内容加入缓存队列，跳过 2 条已缓存或不能缓存的。遇到站点风控，已停止" {
		t.Fatalf("outcome %q", app.BannerInfo)
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
