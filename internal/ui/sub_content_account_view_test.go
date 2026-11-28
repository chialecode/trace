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

type fourSiteAccountNavStub struct {
	service.BackendAPI
	raw *service.Backend

	mu          sync.Mutex
	openedLinks []string
	netCalls    atomic.Int32
	fetchCalls  atomic.Int32
}

func (s *fourSiteAccountNavStub) Statuses() map[string]credential.Status {
	return map[string]credential.Status{
		"bilibili": s.BackendAPI.StatusOf("bilibili"),
		"douyin":   s.StatusOf("douyin"),
		"weibo":    s.StatusOf("weibo"),
		"x":        s.StatusOf("x"),
	}
}

func (s *fourSiteAccountNavStub) StatusOf(siteID string) credential.Status {
	switch normalizeSiteIDUI(siteID) {
	case "douyin":
		return credential.Status{Site: "douyin", LoggedIn: true, State: "logged_in", SecUID: "MS4wLjABAAAA_self_dy", Uname: "抖音号"}
	case "weibo":
		return credential.Status{Site: "weibo", LoggedIn: true, State: "logged_in", UID: "5001", Uname: "微博号"}
	case "x":
		return credential.Status{Site: "x", LoggedIn: true, State: "logged_in", UID: "900001", UniqueID: "self_x", Uname: "X账号"}
	default:
		return s.BackendAPI.StatusOf(siteID)
	}
}

func (s *fourSiteAccountNavStub) HasAnyLogin() bool {
	return true
}

func (s *fourSiteAccountNavStub) FetchImage(ctx context.Context, rawURL string) ([]byte, error) {
	s.fetchCalls.Add(1)
	return nil, context.Canceled
}

func (s *fourSiteAccountNavStub) OpenLink(_ context.Context, rawInput string) (*service.OpenLinkResult, error) {
	s.netCalls.Add(1)
	s.mu.Lock()
	s.openedLinks = append(s.openedLinks, rawInput)
	s.mu.Unlock()

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
			WeiboUserPosts: &service.WeiboUserPostsPage{},
		}, nil
	case strings.Contains(rawInput, "x.com/i/web/status/"):
		sid := strings.TrimPrefix(rawInput[strings.Index(rawInput, "/status/"):], "/status/")
		return &service.OpenLinkResult{
			Site:       "x",
			SiteTarget: &site.ResolveTarget{Site: site.X, Kind: "dynamic", ID: sid, CanonicalURL: rawInput},
			XPost: &service.XPostDTO{
				ID:     sid,
				Text:   "合成X帖子详情",
				Author: service.XAuthorDTO{RestID: "1900000000000000001", ScreenName: "alpha_x", Name: "X创作者"},
			},
		}, nil
	case strings.Contains(rawInput, "x.com/"):
		uid := strings.TrimPrefix(rawInput, "https://x.com/i/user/")
		if uid == rawInput {
			uid = strings.TrimPrefix(rawInput, "https://x.com/")
		}
		return &service.OpenLinkResult{
			Site:       "x",
			SiteTarget: &site.ResolveTarget{Site: site.X, Kind: "user", ID: uid, CanonicalURL: rawInput},
			XUser: &service.XUserDTO{
				RestID:        uid,
				ScreenName:    "alpha_x",
				Name:          "X创作者",
				StatusesCount: 42,
				CanonicalURL:  rawInput,
			},
			XUserPosts: &service.XUserPostsPageDTO{
				Posts: []service.XPostDTO{
					{
						ID:     "1900000000000099999",
						Text:   "X用户页首帖",
						Author: service.XAuthorDTO{RestID: uid, ScreenName: "alpha_x", Name: "X创作者"},
					},
				},
			},
		}, nil
	default:
		uidStr := strings.TrimPrefix(strings.TrimSpace(rawInput), "UID:")
		mid := parseBilibiliMidUI("bilibili", uidStr)
		if mid <= 0 {
			mid = 20001
		}
		return &service.OpenLinkResult{
			Site:   "bilibili",
			Target: bilibili.ResolveTarget{Kind: "user", Mid: mid, CanonicalURL: "https://space.bilibili.com/" + uidStr},
			User:   &bilibili.UserSpace{Mid: mid, Name: "B站创作者", CanonicalURL: "https://space.bilibili.com/" + uidStr},
		}, nil
	}
}

func TestSubscriptionContentAccountFilterAndDirectProfileNavigation(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	raw := app.backend.(*service.Backend)
	if _, err := raw.ImportCookie(context.Background(), "SESSDATA=synth_bili; bili_jct=csrf; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}

	stub := &fourSiteAccountNavStub{BackendAPI: raw, raw: raw}
	app.backend = stub

	const (
		biliUID = "20001"
		dyUID   = "MS4wLjABAAAA_dec095_dy"
		wbUID   = "500000095"
		xUID    = "1900000000000000001"
		xOnlyID = "1900000000000000002"
		emptyWB = "500000096"
	)

	subMulti, err := raw.CreateSubscription("四站跨平台作者", "覆盖四站账号", []subscription.AccountRef{
		{Site: "bilibili", UID: biliUID, Name: "B站创作者"},
		{Site: "douyin", UID: dyUID, UniqueID: "dy_095", Name: "抖音创作者"},
		{Site: "weibo", UID: wbUID, Name: "微博创作者"},
		{Site: "x", UID: xUID, UniqueID: "alpha_x", Name: "X创作者"},
	}, subscription.SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatal(err)
	}
	subXOnly, err := raw.CreateSubscription("单账号X订阅", "", []subscription.AccountRef{
		{Site: "x", UID: xOnlyID, UniqueID: "solo_x", Name: "独苗X博主"},
	}, subscription.SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatal(err)
	}
	subMixedEmpty, err := raw.CreateSubscription("部分有缓存订阅", "", []subscription.AccountRef{
		{Site: "x", UID: "1900000000000000003", UniqueID: "has_x", Name: "有缓存X号"},
		{Site: "weibo", UID: emptyWB, Name: "无缓存微博号"},
	}, subscription.SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatal(err)
	}
	subNoAccounts, err := raw.CreateSubscription("零账号订阅", "", nil, subscription.EmptySubRules())
	if err != nil {
		t.Fatal(err)
	}

	lib := raw.LibraryStore()
	seedPost := func(siteID, uid string, mid int64, uniqueID, id, authorName, title string, pubTs int64) library.ItemMetadata {
		dir := filepath.Join(lib.CacheDir(), siteID, uid, id)
		_ = os.MkdirAll(dir, 0o700)
		saved, err := lib.SaveItem(dir, library.ItemMetadata{
			Version: 1, ID: id, RawID: id, Site: siteID, Kind: "dynamic", Status: "complete",
			Title: title, Text: title, PubTs: pubTs, CachedAt: "2026-11-28T06:00:00Z",
			Author: library.ItemAuthor{Site: siteID, UID: uid, Mid: mid, UniqueID: uniqueID, Name: authorName},
		})
		if err != nil {
			t.Fatal(err)
		}
		return saved
	}

	// Bilibili item intentionally has Mid=0 and UID="20001" to verify fallback resolution in openCreator.
	biliItem := seedPost("bilibili", biliUID, 0, "", "dyn_bili_95", "B站发帖作者", "B站单账号内容", 1730004000)
	dyItem := seedPost("douyin", dyUID, 0, "dy_095", "7395000001", "抖音发帖作者", "抖音单账号内容", 1730003000)
	wbItem := seedPost("weibo", wbUID, 0, "", "5000000000000951", "微博发帖作者", "微博单账号内容", 1730002000)
	xItem := seedPost("x", xUID, 0, "alpha_x", "1995000000000000001", "X发帖作者", "X单账号内容", 1730001000)
	_ = seedPost("x", xOnlyID, 0, "solo_x", "1995000000000000002", "独苗X博主", "单账号X帖子", 1730000500)
	_ = seedPost("x", "1900000000000000003", 0, "has_x", "1995000000000000003", "有缓存X号", "混合订阅X帖子", 1730000400)

	app.CompleteLogin()
	posted := make(chan func(), 64)
	app.dispatch = func(fn func()) { posted <- fn }
	tt := ui.NewTester(app.View, 1280, 860)

	// 1. Multi-account subscription: default "全部" shows all 4 posts, hides "打开账号主页", 0 network calls.
	stub.netCalls.Store(0)
	stub.fetchCalls.Store(0)
	app.SubDetailTab = "content"
	app.OpenSubscriptionDetail(subMulti)
	tt.Frame()
	if _, ok := tt.Find("订阅内容账号切换栏"); !ok {
		t.Fatal("missing 订阅内容账号切换栏 on multi-account subscription")
	}
	if _, ok := tt.Find("订阅内容账号筛选：全部"); !ok {
		t.Fatal("missing 全部 filter pill on multi-account subscription")
	}
	if _, ok := tt.Find("打开账号主页"); ok {
		t.Fatal("打开账号主页 should not appear while 全部 is selected on multi-account subscription")
	}
	if len(app.SubFeedItems) != 4 {
		t.Fatalf("expected 4 items under 全部, got %d", len(app.SubFeedItems))
	}

	// Verify filtering + "打开账号主页" + Back preservation across all 4 sites.
	siteCases := []struct {
		name       string
		accountKey string
		wantTitle  string
		wantRoute  string
		wantURLSub string
	}{
		{"bilibili", "bilibili:" + biliUID, "B站单账号内容", RouteUser, "UID:" + biliUID},
		{"douyin", "douyin:" + dyUID, "抖音单账号内容", RouteDouyinUser, dyUID},
		{"weibo", "weibo:" + wbUID, "微博单账号内容", RouteWeiboUser, wbUID},
		{"x", "x:" + xUID, "X单账号内容", RouteXUser, xUID},
	}
	for _, tc := range siteCases {
		t.Run("multi-account-"+tc.name, func(t *testing.T) {
			beforeNet := stub.netCalls.Load()
			if err := tt.Click("订阅内容账号筛选：" + tc.accountKey); err != nil {
				t.Fatalf("click account chip %s: %v", tc.accountKey, err)
			}
			tt.Frame()
			if got := stub.netCalls.Load(); got != beforeNet {
				t.Fatalf("local account filter triggered %d network calls", got-beforeNet)
			}
			if app.SubFeedAccountKey != tc.accountKey {
				t.Fatalf("SubFeedAccountKey = %q, want %q", app.SubFeedAccountKey, tc.accountKey)
			}
			if len(app.SubFeedItems) != 1 || app.SubFeedItems[0].Title != tc.wantTitle {
				t.Fatalf("filtered feed = %+v, want only %q", app.SubFeedItems, tc.wantTitle)
			}
			if _, ok := tt.Find("打开账号主页"); !ok {
				t.Fatal("expected 打开账号主页 button when single account is selected")
			}

			if err := tt.Click("打开账号主页"); err != nil {
				t.Fatalf("click 打开账号主页: %v", err)
			}
			waitUIIdle(t, app, posted)
			tt.Frame()
			if app.CurrentRoute != tc.wantRoute {
				t.Fatalf("打开账号主页 navigated to %s, want %s", app.CurrentRoute, tc.wantRoute)
			}
			stub.mu.Lock()
			lastOpened := stub.openedLinks[len(stub.openedLinks)-1]
			stub.mu.Unlock()
			if !strings.Contains(lastOpened, tc.wantURLSub) {
				t.Fatalf("OpenLink target = %q, want substring %q", lastOpened, tc.wantURLSub)
			}
			if tc.wantRoute == RouteXUser {
				for _, tabLabel := range []string{"X用户标签：帖子", "X用户标签：回复", "X用户标签：媒体", "X用户标签：精选", "X用户标签：文章"} {
					if _, ok := tt.Find(tabLabel); !ok {
						t.Fatalf("opened X user page missing tab %q", tabLabel)
					}
				}
			}

			// Multi-hop check on X: XUser -> XPost -> Back -> XUser -> Back -> SubDetail(content + x account).
			if tc.wantRoute == RouteXUser {
				app.OpenXPost("1900000000000099999", "合成X帖子详情", "X创作者", xUID, "", "")
				waitUIIdle(t, app, posted)
				tt.Frame()
				if app.CurrentRoute != RouteXPost {
					t.Fatalf("expected RouteXPost, got %s", app.CurrentRoute)
				}
				if err := tt.Click("返回"); err != nil {
					t.Fatal(err)
				}
				tt.Frame()
				if app.CurrentRoute != RouteXUser {
					t.Fatalf("first Back expected RouteXUser, got %s", app.CurrentRoute)
				}
			}

			if err := tt.Click("返回"); err != nil {
				t.Fatalf("click Back from %s: %v", tc.wantRoute, err)
			}
			tt.Frame()
			if app.CurrentRoute != RouteSubscriptions || !app.SubDetailOpen || app.SelectedSubID != subMulti.ID || app.SubDetailTab != "content" || app.SubFeedAccountKey != tc.accountKey {
				t.Fatalf("Back lost detail/account filter state: route=%s open=%v sub=%q tab=%q acct=%q",
					app.CurrentRoute, app.SubDetailOpen, app.SelectedSubID, app.SubDetailTab, app.SubFeedAccountKey)
			}
			if len(app.SubFeedItems) != 1 || app.SubFeedItems[0].Title != tc.wantTitle {
				t.Fatalf("Back did not keep filtered single-account feed: %+v", app.SubFeedItems)
			}
		})
	}

	// Switch back to "全部" restores all 4 posts with 0 network calls.
	netBeforeAll := stub.netCalls.Load()
	if err := tt.Click("订阅内容账号筛选：全部"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if stub.netCalls.Load() != netBeforeAll {
		t.Fatal("switching back to 全部 must not trigger network calls")
	}
	if app.SubFeedAccountKey != "" || len(app.SubFeedItems) != 4 {
		t.Fatalf("switching back to 全部 failed: acct=%q len=%d", app.SubFeedAccountKey, len(app.SubFeedItems))
	}
	if stub.fetchCalls.Load() != 0 {
		t.Fatalf("expected 0 remote FetchImage calls on content tab, got %d", stub.fetchCalls.Load())
	}

	// 2. Post card author avatar & nickname clicks:
	//    - On Subscription Detail › 内容: open single-account online user page (all 4 sites, including Bilibili Mid=0 + AuthorUID).
	//    - On Home: strictly retain DEC-057 ④ (subscribed author avatar opens Subscription Detail, 0 OpenLink calls).
	for _, pc := range []struct {
		name      string
		itemKey   string
		author    string
		wantRoute string
	}{
		{"bilibili", biliItem.ItemKey, "B站发帖作者", RouteUser},
		{"douyin", dyItem.ItemKey, "抖音发帖作者", RouteDouyinUser},
		{"weibo", wbItem.ItemKey, "微博发帖作者", RouteWeiboUser},
		{"x", xItem.ItemKey, "X发帖作者", RouteXUser},
	} {
		if err := tt.Click("作者头像：" + pc.itemKey); err != nil {
			t.Fatalf("content tab click avatar (%s): %v", pc.name, err)
		}
		waitUIIdle(t, app, posted)
		tt.Frame()
		if app.CurrentRoute != pc.wantRoute {
			t.Fatalf("content tab avatar (%s) opened %s, want %s", pc.name, app.CurrentRoute, pc.wantRoute)
		}
		if err := tt.Click("返回"); err != nil {
			t.Fatalf("Back from %s: %v", pc.wantRoute, err)
		}
		tt.Frame()
		if app.CurrentRoute != RouteSubscriptions || !app.SubDetailOpen || app.SelectedSubID != subMulti.ID || app.SubDetailTab != "content" {
			t.Fatalf("Back after avatar click (%s) failed: route=%s open=%v sub=%q tab=%q",
				pc.name, app.CurrentRoute, app.SubDetailOpen, app.SelectedSubID, app.SubDetailTab)
		}
	}

	// Clicking author nickname in Subscription Detail › 内容 (with X filtered) opens RouteXUser and Back keeps X filter.
	if err := tt.Click("订阅内容账号筛选：x:" + xUID); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("X发帖作者"); err != nil {
		t.Fatalf("click author nickname on content tab: %v", err)
	}
	waitUIIdle(t, app, posted)
	tt.Frame()
	if app.CurrentRoute != RouteXUser {
		t.Fatalf("clicking author nickname on content tab opened %s, want RouteXUser", app.CurrentRoute)
	}
	if err := tt.Click("返回"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if app.CurrentRoute != RouteSubscriptions || !app.SubDetailOpen || app.SubFeedAccountKey != "x:"+xUID {
		t.Fatalf("Back after nickname click lost X filter: route=%s open=%v acct=%q", app.CurrentRoute, app.SubDetailOpen, app.SubFeedAccountKey)
	}

	// Verify Home feed still obeys DEC-057 ④: clicking subscribed X author's avatar opens Subscription Detail (0 network calls).
	app.Navigate(RouteHome)
	app.setFeedSiteFilter("x")
	settle(tt, 2)
	netBeforeHome := stub.netCalls.Load()
	if err := tt.Click("作者头像：" + xItem.ItemKey); err != nil {
		t.Fatalf("click subscribed X avatar on Home: %v", err)
	}
	tt.Frame()
	if app.CurrentRoute != RouteSubscriptions || !app.SubDetailOpen || app.SelectedSubID != subMulti.ID {
		t.Fatalf("Home subscribed avatar must open subscription detail (DEC-057 ④), got route=%s open=%v sub=%q",
			app.CurrentRoute, app.SubDetailOpen, app.SelectedSubID)
	}
	if stub.netCalls.Load() != netBeforeHome {
		t.Fatal("Home subscribed avatar click must be zero-network")
	}

	// 3. Single-account subscription (X-only): shows lightweight account bar + 打开账号主页 without 全部 pill.
	app.SubDetailTab = "content"
	app.OpenSubscriptionDetail(subXOnly)
	tt.Frame()
	if _, ok := tt.Find("订阅内容账号切换栏"); !ok {
		t.Fatal("single-account subscription missing 订阅内容账号切换栏")
	}
	if _, ok := tt.Find("订阅内容单账号：x:" + xOnlyID); !ok {
		t.Fatal("single-account subscription missing 订阅内容单账号 row")
	}
	if _, ok := tt.Find("订阅内容账号筛选：全部"); ok {
		t.Fatal("single-account subscription should not render 全部 filter pill")
	}
	if err := tt.Click("打开账号主页"); err != nil {
		t.Fatalf("single-account click 打开账号主页: %v", err)
	}
	waitUIIdle(t, app, posted)
	tt.Frame()
	if app.CurrentRoute != RouteXUser {
		t.Fatalf("single-account 打开账号主页 navigated to %s, want RouteXUser", app.CurrentRoute)
	}
	if err := tt.Click("返回"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if app.CurrentRoute != RouteSubscriptions || !app.SubDetailOpen || app.SelectedSubID != subXOnly.ID || app.SubDetailTab != "content" {
		t.Fatalf("Back from single-account XUser failed: route=%s open=%v sub=%q tab=%q",
			app.CurrentRoute, app.SubDetailOpen, app.SelectedSubID, app.SubDetailTab)
	}

	// 4. Multi-account subscription filtering to an account with 0 cached items shows tailored empty state + 打开账号主页.
	app.OpenSubscriptionDetail(subMixedEmpty)
	tt.Frame()
	if err := tt.Click("订阅内容账号筛选：weibo:" + emptyWB); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(app.SubFeedItems) != 0 || !tt.HasText("还没有缓存「无缓存微博号」的内容") {
		t.Fatalf("expected single-account empty state, items=%d texts=%v", len(app.SubFeedItems), tt.Texts())
	}
	if _, ok := tt.Find("打开账号主页"); !ok {
		t.Fatal("single-account empty state must keep 打开账号主页 visible")
	}

	// 5. Zero-account subscription hides the account bar.
	app.OpenSubscriptionDetail(subNoAccounts)
	tt.Frame()
	if _, ok := tt.Find("订阅内容账号切换栏"); ok {
		t.Fatal("0-account subscription must not render 订阅内容账号切换栏")
	}

	// 6. Subscription Detail › 账号 (row body + inline 打开主页 button + ⋯ menu) & 全部账号 (⋯ -> 打开账号主页 + Back).
	app.OpenSubscriptionDetail(subMulti)
	app.setSubDetailTab("accounts")
	tt.Frame()
	if err := tt.Click("打开关联账号主页：x:" + xUID); err != nil {
		t.Fatalf("accounts tab click inline 打开主页 for X: %v", err)
	}
	waitUIIdle(t, app, posted)
	tt.Frame()
	if app.CurrentRoute != RouteXUser {
		t.Fatalf("accounts tab inline 打开主页 opened %s, want RouteXUser", app.CurrentRoute)
	}
	if err := tt.Click("返回"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if app.CurrentRoute != RouteSubscriptions || !app.SubDetailOpen || app.SubDetailTab != "accounts" {
		t.Fatalf("Back from accounts tab inline button failed: route=%s open=%v tab=%q",
			app.CurrentRoute, app.SubDetailOpen, app.SubDetailTab)
	}

	// 全部账号: row opens subscription; "⋯ -> 打开账号主页" opens XUser and Back returns to 全部账号.
	app.openSubscriptionAccounts()
	tt.Frame()
	if err := tt.Click("订阅账号：x:" + xUID); err != nil {
		t.Fatalf("all accounts click row: %v", err)
	}
	tt.Frame()
	if !app.SubDetailOpen || app.SelectedSubID != subMulti.ID {
		t.Fatalf("all accounts row click should open subscription detail, open=%v sub=%q", app.SubDetailOpen, app.SelectedSubID)
	}

	app.openSubscriptionAccounts()
	tt.Frame()
	chooseMenuItem(t, tt, "全部账号更多：x:"+xUID, "打开账号主页")
	waitUIIdle(t, app, posted)
	tt.Frame()
	if app.CurrentRoute != RouteXUser {
		t.Fatalf("all accounts menu 打开账号主页 opened %s, want RouteXUser", app.CurrentRoute)
	}
	if err := tt.Click("返回"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if app.CurrentRoute != RouteSubscriptions || !app.SubAccountsOpen || app.SubDetailOpen {
		t.Fatalf("Back from all accounts 打开账号主页 failed: route=%s accounts=%v detail=%v",
			app.CurrentRoute, app.SubAccountsOpen, app.SubDetailOpen)
	}
}

func TestSubscriptionContentAccountBarResponsiveZeroOverflow(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	raw := app.backend.(*service.Backend)
	if _, err := raw.ImportCookie(context.Background(), "SESSDATA=synth_bili; bili_jct=csrf; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.backend = &fourSiteAccountNavStub{BackendAPI: raw, raw: raw}

	subLong, err := raw.CreateSubscription("超长名称跨四站多账号测试订阅", "", []subscription.AccountRef{
		{Site: "bilibili", UID: "20001", Name: "超长名称哔哩哔哩创作者主频道"},
		{Site: "douyin", UID: "MS4wLjABAAAA_long_dy", UniqueID: "long_dy", Name: "超长名称抖音短视频工作室"},
		{Site: "weibo", UID: "500000199", Name: "超长名称微博摄影日常主号"},
		{Site: "x", UID: "1900000000000000199", UniqueID: "long_x_account_name", Name: "超长名称X技术专栏主号"},
	}, subscription.SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatal(err)
	}
	subSolo, err := raw.CreateSubscription("超长名称单账号X测试订阅", "", []subscription.AccountRef{
		{Site: "x", UID: "1900000000000000200", UniqueID: "solo_long_x", Name: "超长名称单账号X创作者专栏"},
	}, subscription.SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatal(err)
	}

	app.CompleteLogin()

	for _, w := range []int{1280, 1000, 720} {
		// Multi-account content tab with X selected + 打开账号主页 visible.
		tt := ui.NewTester(app.View, w, 860)
		app.SubDetailTab = "content"
		app.OpenSubscriptionDetail(subLong)
		app.SelectSubFeedAccount("x:1900000000000000199")
		tt.Frame()
		assertNoHorizontalOverflow(t, tt, w, []string{
			"订阅内容账号切换栏",
			"订阅内容账号筛选：全部",
			"订阅内容账号筛选：bilibili:20001",
			"订阅内容账号筛选：douyin:MS4wLjABAAAA_long_dy",
			"订阅内容账号筛选：weibo:500000199",
			"订阅内容账号筛选：x:1900000000000000199",
			"打开账号主页",
		})

		// Multi-account accounts tab with inline 打开主页 buttons.
		app.setSubDetailTab("accounts")
		tt.Frame()
		assertNoHorizontalOverflow(t, tt, w, []string{
			"订阅账号主页：bilibili:20001",
			"打开关联账号主页：bilibili:20001",
			"订阅账号主页：x:1900000000000000199",
			"打开关联账号主页：x:1900000000000000199",
		})

		// Single-account content tab with 打开账号主页.
		app.SubDetailTab = "content"
		app.OpenSubscriptionDetail(subSolo)
		tt.Frame()
		assertNoHorizontalOverflow(t, tt, w, []string{
			"订阅内容账号切换栏",
			"订阅内容单账号：x:1900000000000000200",
			"打开账号主页",
		})
	}
}
