package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

func TestDouyinZeroPostProfileShowsEmptyNotAuthExpired(t *testing.T) {
	app, stub, _, cleanup := newDouyinUITestHarness(t)
	defer cleanup()

	posted := make(chan func(), 32)
	app.dispatch = func(fn func()) { posted <- fn }

	stub.mu.Lock()
	stub.dyStatus = credential.Status{Site: "douyin", LoggedIn: true, State: "logged_in", UID: "90001", Uname: "抖音号"}
	zeroUID := "MS4wLjABAAAA_synth_zero_post"
	stub.users[zeroUID] = &service.DouyinUserView{
		SecUID:       zeroUID,
		Nickname:     "零作品公开账号",
		AwemeCount:   0,
		CanonicalURL: "https://www.douyin.com/user/" + zeroUID,
	}
	stub.userPages[zeroUID] = map[string]douyinPageFixture{
		"": {page: service.DouyinUserWorksPage{Works: nil, HasMore: false, UncertainEmpty: false}},
	}
	uncertainUID := "MS4wLjABAAAA_synth_uncertain_page1"
	stub.users[uncertainUID] = &service.DouyinUserView{
		SecUID:       uncertainUID,
		Nickname:     "受限首屏账号",
		AwemeCount:   10,
		CanonicalURL: "https://www.douyin.com/user/" + uncertainUID,
	}
	stub.userPages[uncertainUID] = map[string]douyinPageFixture{
		"": {page: service.DouyinUserWorksPage{
			Works:           nil,
			HasMore:         false,
			UncertainEmpty:  true,
			UncertainReason: "首屏无可浏览公开作品（可能为仅好友可见或受限），可稍后重试",
		}},
	}
	stub.mu.Unlock()
	app.CompleteLogin()

	tt := ui.NewTester(app.View, 1280, 860)

	// 1. Genuine 0-post Douyin profile renders "还没有发布公开作品" and NOT "登录已失效".
	app.OpenDouyinUser(zeroUID, "零作品公开账号")
	waitUIIdle(t, app, posted)
	tt.Frame()
	if app.CurrentRoute != RouteDouyinUser {
		t.Fatalf("CurrentRoute = %s, want %s", app.CurrentRoute, RouteDouyinUser)
	}
	if app.DouyinWorksAuthExpired {
		t.Fatal("genuine 0-post Douyin profile must NOT set DouyinWorksAuthExpired")
	}
	if !tt.HasText("还没有发布公开作品") {
		t.Fatal("expected 还没有发布公开作品 on genuine 0-post Douyin profile")
	}
	if tt.HasText("登录已失效") {
		t.Fatal("must NOT display 登录已失效 on genuine 0-post Douyin profile")
	}
	if _, ok := tt.Find("抖音失效去登录"); ok {
		t.Fatal("must NOT show 抖音失效去登录 on genuine 0-post Douyin profile")
	}

	// 2. Page-1 UncertainEmpty sets retryable DouyinWorksError instead of DouyinWorksAuthExpired.
	app.OpenDouyinUser(uncertainUID, "受限首屏账号")
	waitUIIdle(t, app, posted)
	tt.Frame()
	if app.DouyinWorksAuthExpired {
		t.Fatal("page-1 UncertainEmpty must NOT set DouyinWorksAuthExpired")
	}
	if app.DouyinWorksError == "" {
		t.Fatal("expected DouyinWorksError populated for page-1 UncertainEmpty")
	}
	if _, ok := tt.Find("重试加载抖音作品"); !ok {
		t.Fatal("expected retry button on page-1 UncertainEmpty")
	}
	if _, ok := tt.Find("抖音失效去登录"); ok {
		t.Fatal("page-1 UncertainEmpty must NOT show 登录已失效 re-login button")
	}

	// Page-1 retry (cursor == "") still sets DouyinWorksError, not DouyinWorksAuthExpired.
	if err := tt.Click("重试加载抖音作品"); err != nil {
		t.Fatalf("click retry on page-1 UncertainEmpty: %v", err)
	}
	waitUIIdle(t, app, posted)
	tt.Frame()
	if app.DouyinWorksAuthExpired || app.DouyinWorksError == "" {
		t.Fatalf("page-1 retry UncertainEmpty: expected error without auth expired, got expired=%v err=%q",
			app.DouyinWorksAuthExpired, app.DouyinWorksError)
	}
}

type multiSiteNavStub struct {
	service.BackendAPI
	raw *service.Backend

	mu       sync.Mutex
	dyStatus credential.Status
	wbStatus credential.Status
	netCalls atomic.Int32
}

func (s *multiSiteNavStub) Statuses() map[string]credential.Status {
	s.mu.Lock()
	dy, wb := s.dyStatus, s.wbStatus
	s.mu.Unlock()
	return map[string]credential.Status{
		"bilibili": s.BackendAPI.StatusOf("bilibili"),
		"douyin":   dy,
		"weibo":    wb,
	}
}

func (s *multiSiteNavStub) StatusOf(siteID string) credential.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch strings.ToLower(strings.TrimSpace(siteID)) {
	case "douyin":
		return s.dyStatus
	case "weibo":
		return s.wbStatus
	default:
		return s.BackendAPI.StatusOf(siteID)
	}
}

func (s *multiSiteNavStub) HasAnyLogin() bool {
	return s.BackendAPI.StatusOf("bilibili").LoggedIn || s.StatusOf("douyin").LoggedIn || s.StatusOf("weibo").LoggedIn
}

func (s *multiSiteNavStub) OpenLink(_ context.Context, rawInput string) (*service.OpenLinkResult, error) {
	s.netCalls.Add(1)
	switch {
	case strings.Contains(rawInput, "douyin.com/user/"):
		uid := strings.TrimPrefix(rawInput[strings.Index(rawInput, "/user/"):], "/user/")
		return &service.OpenLinkResult{
			Site:            "douyin",
			SiteTarget:      &site.ResolveTarget{Site: site.Douyin, Kind: "user", ID: uid, CanonicalURL: rawInput},
			DouyinUser:      &service.DouyinUserView{SecUID: uid, Nickname: "抖音创作者", CanonicalURL: rawInput},
			DouyinUserWorks: &service.DouyinUserWorksPage{},
		}, nil
	case strings.Contains(rawInput, "weibo.com/u/"):
		uid := strings.TrimPrefix(rawInput[strings.Index(rawInput, "/u/"):], "/u/")
		return &service.OpenLinkResult{
			Site:           "weibo",
			SiteTarget:     &site.ResolveTarget{Site: site.Weibo, Kind: "user", ID: uid, CanonicalURL: rawInput},
			WeiboUser:      &service.WeiboUserView{UID: uid, ScreenName: "微博创作者", CanonicalURL: rawInput},
			WeiboUserPosts: &service.WeiboUserPostsPage{Posts: []service.WeiboPostView{{Mid: "5001", Author: service.WeiboAuthorDTO{UID: uid, ScreenName: "微博创作者"}, Text: "示例微博"}}},
		}, nil
	case strings.Contains(rawInput, "weibo.com/"):
		return &service.OpenLinkResult{
			Site:       "weibo",
			SiteTarget: &site.ResolveTarget{Site: site.Weibo, Kind: "dynamic", ID: "5001", CanonicalURL: rawInput},
			WeiboPost:  &service.WeiboPostView{Mid: "5001", Author: service.WeiboAuthorDTO{UID: "500000001", ScreenName: "微博创作者"}, Text: "示例微博"},
		}, nil
	default:
		return &service.OpenLinkResult{
			Site:   "bilibili",
			Target: bilibili.ResolveTarget{Kind: "user", Mid: 20001, CanonicalURL: "https://space.bilibili.com/20001"},
			User:   &bilibili.UserSpace{Mid: 20001, Name: "B站UP主", CanonicalURL: "https://space.bilibili.com/20001"},
		}, nil
	}
}

func TestSubscriptionDetailAccountNavigationBackPreservesSubAndTab(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	raw := app.backend.(*service.Backend)

	stub := &multiSiteNavStub{
		BackendAPI: raw,
		raw:        raw,
		dyStatus:   credential.Status{Site: "douyin", LoggedIn: true, State: "logged_in", UID: "9001", Uname: "抖音号"},
		wbStatus:   credential.Status{Site: "weibo", LoggedIn: true, State: "logged_in", UID: "5001", Uname: "微博号"},
	}
	app.backend = stub
	if _, err := raw.ImportCookie(context.Background(), "SESSDATA=synth_bili; bili_jct=csrf; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}

	dyUID := "MS4wLjABAAAA_synth_nav_dy"
	wbUID := "500000001"
	sub, err := raw.CreateSubscription("跨站目标订阅", "", []subscription.AccountRef{
		{Site: "bilibili", UID: "20001", Name: "B站UP主"},
		{Site: "douyin", UID: dyUID, Name: "抖音创作者"},
		{Site: "weibo", UID: wbUID, Name: "微博创作者"},
	}, subscription.SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatal(err)
	}

	// Also seed one cached item per site so the "内容" tab renders author entries for all 3 sites.
	for _, seed := range []struct {
		site, uid string
		mid       int64
		id, name  string
		pubTs     int64
	}{
		{"bilibili", "20001", 20001, "BV1nav000001", "B站UP主", 1710003000},
		{"douyin", dyUID, 0, "7399000001", "抖音创作者", 1710002000},
		{"weibo", wbUID, 0, "5000000000000301", "微博创作者", 1710001000},
	} {
		dir := filepath.Join(raw.LibraryStore().CacheDir(), seed.site, seed.uid, seed.id)
		_ = os.MkdirAll(dir, 0o700)
		if _, err := raw.LibraryStore().SaveItem(dir, library.ItemMetadata{
			Version: 1, ID: seed.id, RawID: seed.id, Site: seed.site, Kind: "dynamic", Status: "complete",
			Title: seed.name + " 的内容", Text: seed.name + " 的内容", PubTs: seed.pubTs, CachedAt: "2026-03-02T12:00:00Z",
			Author: library.ItemAuthor{Site: seed.site, UID: seed.uid, Mid: seed.mid, Name: seed.name},
		}); err != nil {
			t.Fatal(err)
		}
	}

	app.CompleteLogin()
	posted := make(chan func(), 32)
	app.BindEventLoop(func(fn func()) { posted <- fn })
	tt := ui.NewTester(app.View, 1280, 860)

	// 1. From "账号" tab -> open Bilibili, Douyin, Weibo user pages -> click Back returns to THAT sub detail & "accounts" tab!
	cases := []struct {
		name      string
		btnLabel  string
		wantRoute string
	}{
		{"bilibili", "订阅账号主页：bilibili:20001", RouteUser},
		{"douyin", "订阅账号主页：douyin:" + dyUID, RouteDouyinUser},
		{"weibo", "订阅账号主页：weibo:" + wbUID, RouteWeiboUser},
	}
	for _, tc := range cases {
		t.Run("accounts-tab-"+tc.name, func(t *testing.T) {
			app.SubDetailTab = "accounts"
			app.OpenSubscriptionDetail(sub)
			app.SubDetailTab = "accounts"
			tt.Frame()

			if err := tt.Click(tc.btnLabel); err != nil {
				t.Fatalf("click %s: %v", tc.btnLabel, err)
			}
			waitUIIdle(t, app, posted)
			tt.Frame()
			if app.CurrentRoute != tc.wantRoute {
				t.Fatalf("opened route = %s, want %s", app.CurrentRoute, tc.wantRoute)
			}

			if err := tt.Click("返回"); err != nil {
				t.Fatalf("click Back from %s: %v", tc.wantRoute, err)
			}
			tt.Frame()
			if app.CurrentRoute != RouteSubscriptions || !app.SubDetailOpen || app.SelectedSubID != sub.ID || app.SubDetailTab != "accounts" {
				t.Fatalf("Back from %s lost subscription detail: route=%s open=%v subID=%q tab=%q (want sub=%q tab=accounts)",
					tc.wantRoute, app.CurrentRoute, app.SubDetailOpen, app.SelectedSubID, app.SubDetailTab, sub.ID)
			}
		})
	}

	// 2. Multi-hop: /subscriptions/<id>?tab=accounts -> /weibo/user -> /weibo/post -> Back -> /weibo/user -> Back -> /subscriptions/<id>?tab=accounts.
	app.SubDetailTab = "accounts"
	app.OpenSubscriptionDetail(sub)
	app.SubDetailTab = "accounts"
	tt.Frame()
	_ = tt.Click("订阅账号主页：weibo:" + wbUID)
	waitUIIdle(t, app, posted)
	tt.Frame()
	app.OpenWeiboPost("5001", "示例微博", "微博创作者", wbUID, "", "https://weibo.com/"+wbUID+"/5001")
	waitUIIdle(t, app, posted)
	tt.Frame()
	if app.CurrentRoute != RouteWeiboPost {
		t.Fatalf("expected RouteWeiboPost, got %s", app.CurrentRoute)
	}
	_ = tt.Click("返回")
	tt.Frame()
	if app.CurrentRoute != RouteWeiboUser {
		t.Fatalf("first Back expected RouteWeiboUser, got %s", app.CurrentRoute)
	}
	_ = tt.Click("返回")
	tt.Frame()
	if app.CurrentRoute != RouteSubscriptions || !app.SubDetailOpen || app.SelectedSubID != sub.ID || app.SubDetailTab != "accounts" {
		t.Fatalf("second Back expected sub detail accounts tab, got route=%s open=%v subID=%q tab=%q",
			app.CurrentRoute, app.SubDetailOpen, app.SelectedSubID, app.SubDetailTab)
	}

	// 3. From "内容" tab -> click author avatar/name -> opens user page -> click Back returns to THAT sub detail & "content" tab!
	app.SubDetailTab = "content"
	app.OpenSubscriptionDetail(sub)
	app.SubDetailTab = "content"
	tt.Frame()
	if err := tt.Click("作者头像：weibo:dynamic:5000000000000301"); err != nil {
		t.Fatalf("click author avatar on subscription content tab: %v", err)
	}
	waitUIIdle(t, app, posted)
	tt.Frame()
	if app.CurrentRoute != RouteWeiboUser {
		t.Fatalf("expected RouteWeiboUser from content tab author click, got %s", app.CurrentRoute)
	}
	if err := tt.Click("返回"); err != nil {
		t.Fatalf("click Back from user page opened via content tab: %v", err)
	}
	tt.Frame()
	if app.CurrentRoute != RouteSubscriptions || !app.SubDetailOpen || app.SelectedSubID != sub.ID || app.SubDetailTab != "content" {
		t.Fatalf("Back to content tab failed: route=%s open=%v subID=%q tab=%q",
			app.CurrentRoute, app.SubDetailOpen, app.SelectedSubID, app.SubDetailTab)
	}
}

func TestHomeHeaderRefreshIconSyncsDiskOffline(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	raw := app.backend.(*service.Backend)

	stub := &multiSiteNavStub{
		BackendAPI: raw,
		raw:        raw,
		dyStatus:   credential.Status{Site: "douyin", LoggedIn: true, State: "logged_in", UID: "9001", Uname: "抖音号"},
		wbStatus:   credential.Status{Site: "weibo", LoggedIn: true, State: "logged_in", UID: "5001", Uname: "微博号"},
	}
	app.backend = stub
	if _, err := raw.ImportCookie(context.Background(), "SESSDATA=synth_bili; bili_jct=csrf; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}

	sub, err := raw.CreateSubscription("本地同步订阅", "", []subscription.AccountRef{
		{Site: "weibo", UID: "500000001", Name: "微博同步号"},
	}, subscription.SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatal(err)
	}

	// Seed 1 initial cached Weibo post.
	dir1 := filepath.Join(raw.LibraryStore().CacheDir(), "weibo", "500000001", "posts", "5000000000000601")
	_ = os.MkdirAll(dir1, 0o700)
	_ = os.WriteFile(filepath.Join(dir1, "img_01.png"), buildTestPNGBytes(), 0o600)
	item1, err := raw.LibraryStore().SaveItem(dir1, library.ItemMetadata{
		Version: 1, ID: "5000000000000601", RawID: "5000000000000601", Site: "weibo", Kind: "dynamic", Status: "complete",
		Title: "初始微博条目", Text: "初始微博条目", PubTs: 1710001000, CachedAt: "2026-03-01T10:00:00Z",
		Author: library.ItemAuthor{Site: "weibo", UID: "500000001", Name: "微博同步号"},
		Media:  []library.MediaFile{{Role: "image", Path: "img_01.png", Width: 64, Height: 64}},
	})
	if err != nil {
		t.Fatal(err)
	}

	app.CompleteLogin()
	posted := make(chan func(), 32)
	app.BindEventLoop(func(fn func()) { posted <- fn })

	app.Navigate(RouteHome)
	app.setFeedSiteFilter("weibo")
	tt := ui.NewTester(app.View, 720, 860)
	tt.Frame()

	// Verify Home header refresh icon button exists, is compact icon-only (square 34x34), fits within 720w, and has NO "刷新" text label.
	btnRect, ok := tt.Find("主页刷新")
	if !ok {
		t.Fatal("expected Home header refresh icon button (主页刷新)")
	}
	if btnRect.W > 36 || btnRect.H > 36 || btnRect.W < 30 || btnRect.H < 30 || btnRect.X+btnRect.W > 720.5 {
		t.Fatalf("expected compact 34x34 icon-only refresh button within 720w, got rect=%+v", btnRect)
	}
	if _, ok := tt.Find("刷新"); ok {
		t.Fatal("Home header refresh button must be icon-only (no text node 刷新)")
	}
	if len(app.FeedItems) != 1 || app.FeedItems[0].ItemKey != item1.ItemKey {
		t.Fatalf("expected 1 initial Weibo item on Home, got %d", len(app.FeedItems))
	}

	// Simulate external disk changes: delete item1's folder on disk, and add a new item2 in Library!
	if err := os.RemoveAll(dir1); err != nil {
		t.Fatal(err)
	}
	dir2 := filepath.Join(raw.LibraryStore().CacheDir(), "weibo", "500000001", "posts", "5000000000000602")
	_ = os.MkdirAll(dir2, 0o700)
	_ = os.WriteFile(filepath.Join(dir2, "img_01.png"), buildTestPNGBytes(), 0o600)
	item2, err := raw.LibraryStore().SaveItem(dir2, library.ItemMetadata{
		Version: 1, ID: "5000000000000602", RawID: "5000000000000602", Site: "weibo", Kind: "dynamic", Status: "complete",
		Title: "新缓存的微博条目", Text: "新缓存的微博条目", PubTs: 1710009000, CachedAt: "2026-03-02T12:00:00Z",
		Author: library.ItemAuthor{Site: "weibo", UID: "500000001", Name: "微博同步号"},
		Media:  []library.MediaFile{{Role: "image", Path: "img_01.png", Width: 64, Height: 64}},
	})
	if err != nil {
		t.Fatal(err)
	}

	stub.netCalls.Store(0)
	if err := tt.Click("主页刷新"); err != nil {
		t.Fatalf("click Home refresh icon: %v", err)
	}
	waitUIIdle(t, app, posted)
	tt.Frame()

	// Assert 0 network calls, active site filter preserved, deleted item pruned, newly cached item synced!
	if got := stub.netCalls.Load(); got != 0 {
		t.Fatalf("Home refresh must be strictly offline (0 network calls), got %d", got)
	}
	if app.FeedSiteFilter != "weibo" {
		t.Fatalf("Home refresh changed FeedSiteFilter to %q, want weibo", app.FeedSiteFilter)
	}
	if app.LibraryRefresh.Open {
		t.Fatal("Home refresh must sync in place without popping up LibraryRefresh modal dialog")
	}
	if len(app.FeedItems) != 1 || app.FeedItems[0].ItemKey != item2.ItemKey {
		t.Fatalf("expected Home feed synced to newly cached item2 (and item1 pruned), got %+v", app.FeedItems)
	}
	rows := app.homeSubscriptionRows()
	if len(rows) != 1 || rows[0].SubID != sub.ID || rows[0].LatestTs != item2.PubTs {
		t.Fatalf("expected sidebar subscription row refreshed with item2 LatestTs=%d, got %+v", item2.PubTs, rows)
	}

	// Also verify Home refresh preserves active HomeActorFilter (subscription filter).
	if !app.OpenHomeUpdatedActor(sub.ID) {
		t.Fatal("expected opening HomeActorFilter for subscription")
	}
	tt.Frame()
	if err := tt.Click("主页刷新"); err != nil {
		t.Fatalf("click Home refresh icon while actor filtered: %v", err)
	}
	waitUIIdle(t, app, posted)
	tt.Frame()
	if stub.netCalls.Load() != 0 {
		t.Fatalf("expected 0 network calls on actor-filtered Home refresh, got %d", stub.netCalls.Load())
	}
	if app.HomeActorFilter == nil || app.HomeActorFilter.SubID != sub.ID {
		t.Fatalf("expected HomeActorFilter preserved after Home refresh, got %+v", app.HomeActorFilter)
	}
	if len(app.FeedItems) != 1 || app.FeedItems[0].ItemKey != item2.ItemKey {
		t.Fatalf("expected actor-filtered Home feed to keep item2, got %+v", app.FeedItems)
	}
}
