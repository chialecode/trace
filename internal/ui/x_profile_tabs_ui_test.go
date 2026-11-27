package ui

import (
	"fmt"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/egoist/mygo/ui"
)

func TestXUserPageFourTabsLazyLoadPreserveStateAndMenuActions(t *testing.T) {
	app, stub, cleanup := newXUITestHarness(t)
	defer cleanup()

	uid := "900004004"
	stub.xUsers[uid] = &service.XUserDTO{
		RestID:         uid,
		ScreenName:     "synth_creator",
		Name:           "合成多标签X博主",
		Description:    "验证四标签懒加载、状态保留与当前页批量缓存",
		IsBlueVerified: true,
		FriendsCount:   50,
		FollowersCount: 8200,
		StatusesCount:  320,
		CanonicalURL:   "https://x.com/synth_creator",
	}
	stub.xUserTabPages[uid] = map[string]map[string]xPageFixture{
		"posts": {
			"": {
				page: service.XUserPostsPageDTO{
					Tab: "posts",
					Posts: []service.XPostDTO{{
						ID:        "1900000000000004001",
						Author:    service.XAuthorDTO{RestID: uid, ScreenName: "synth_creator", Name: "合成多标签X博主"},
						CreatedAt: time.Now().Unix() - 100,
						Text:      "帖子标签首条内容",
						CacheKey:  "x:dynamic:1900000000000004001",
					}},
				},
			},
		},
		"replies": {
			"": {
				page: service.XUserPostsPageDTO{
					Tab: "replies",
					Posts: []service.XPostDTO{{
						ID:                "1900000000000004101",
						Author:            service.XAuthorDTO{RestID: uid, ScreenName: "synth_creator", Name: "合成多标签X博主"},
						CreatedAt:         time.Now().Unix() - 200,
						InReplyToStatusID: "1900000000000000001",
						Text:              "回复标签第一条回复",
						CacheKey:          "x:dynamic:1900000000000004101",
					}},
					NextCursor: "rep-cur-2",
					HasMore:    true,
				},
			},
		},
		"media": {
			"": {
				page: service.XUserPostsPageDTO{
					Tab: "media",
					Posts: []service.XPostDTO{{
						ID:              "1900000000000004201",
						Author:          service.XAuthorDTO{RestID: uid, ScreenName: "synth_creator", Name: "合成多标签X博主"},
						CreatedAt:       time.Now().Unix() - 300,
						Text:            "纯分段流不可缓存媒体帖",
						HasHLSOnlyVideo: true,
						Media:           []service.XMediaItemDTO{{Index: 1, Kind: "video", HLSOnly: true}},
						CacheKey:        "x:dynamic:1900000000000004201",
					}},
				},
			},
		},
		"highlights": {
			"": {
				err: &site.Error{Site: site.X, Kind: site.ErrKindRateLimited, Message: "X 限流，约 10 分钟后再试"},
			},
		},
	}

	app.OpenXUser(uid, "合成多标签X博主")
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	tt.Frame()

	// All 4 tabs visible, default on "posts".
	for _, lbl := range []string{"X用户标签：帖子", "X用户标签：回复", "X用户标签：媒体", "X用户标签：精选"} {
		if _, ok := tt.Find(lbl); !ok {
			t.Fatalf("missing tab %q", lbl)
		}
	}
	if app.XUserTab != "posts" || !tt.HasText("帖子标签首条内容") {
		t.Fatalf("expected default posts tab with content, tab=%q", app.XUserTab)
	}

	// Verify "⋯" menu on "posts" tab shows "全部缓存" and NOT "缓存当前页未缓存".
	if err := tt.Click("X主页更多"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("全部缓存") || tt.HasText("缓存当前页未缓存") {
		t.Fatalf("posts tab menu should show '全部缓存' and hide '缓存当前页未缓存'")
	}
	app.XUserSubMenuOpen = false
	tt.Frame()

	// Switch to "回复" -> lazy loads once.
	if err := tt.Click("X用户标签：回复"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if app.XUserTab != "replies" || !tt.HasText("回复标签第一条回复") {
		t.Fatalf("replies tab did not load: tab=%q posts=%d", app.XUserTab, len(app.CurrentXPosts))
	}

	// Verify "⋯" menu on "replies" tab shows "缓存当前页未缓存" and NOT "全部缓存".
	if err := tt.Click("X主页更多"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("缓存当前页未缓存") || tt.HasText("全部缓存") {
		t.Fatalf("replies tab menu should show '缓存当前页未缓存' and hide '全部缓存'")
	}
	app.XUserSubMenuOpen = false
	tt.Frame()

	// Switch to "媒体" -> lazy loads once (only has 1 uncacheable HLS-only post).
	if err := tt.Click("X用户标签：媒体"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if app.XUserTab != "media" || !tt.HasText("纯分段流不可缓存媒体帖") {
		t.Fatalf("media tab did not load: tab=%q", app.XUserTab)
	}
	if got := app.countXCurrentTabCacheableUncached(); got != 0 {
		t.Fatalf("pure HLS-only media page should report 0 cacheable uncached items, got %d", got)
	}
	// Verify "缓存当前页未缓存" on media tab with 0 cacheable uncached items is disabled.
	if err := tt.Click("X主页更多"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("缓存当前页未缓存") {
		t.Fatal("media tab menu should render disabled '缓存当前页未缓存'")
	}
	_ = tt.Click("缓存当前页未缓存")
	tt.Frame()
	stub.mu.Lock()
	disabledCalls := len(stub.xBatchPageReqs)
	stub.mu.Unlock()
	if disabledCalls != 0 {
		t.Fatalf("disabled '缓存当前页未缓存' menu item must not trigger CacheXPostsPage, got %d calls", disabledCalls)
	}
	app.XUserSubMenuOpen = false
	tt.Frame()

	// Switch to "精选" -> independent error state with retry button, does not wipe other tabs.
	if err := tt.Click("X用户标签：精选"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if app.XUserTab != "highlights" || app.XPostsError == "" {
		t.Fatalf("highlights tab expected error state, tab=%q err=%q", app.XUserTab, app.XPostsError)
	}
	if _, ok := tt.Find("重试加载X帖子"); !ok {
		t.Fatal("highlights error should render retry button")
	}

	// Switch back to "回复" -> preserved state, ZERO redundant XUserTabPosts call!
	stub.mu.Lock()
	repCallsBefore := stub.xTabCalls[uid+"|replies|"]
	stub.mu.Unlock()
	if err := tt.Click("X用户标签：回复"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	stub.mu.Lock()
	repCallsAfter := stub.xTabCalls[uid+"|replies|"]
	stub.mu.Unlock()
	if repCallsAfter != repCallsBefore {
		t.Fatalf("switching back to loaded replies tab re-fetched backend: before=%d after=%d", repCallsBefore, repCallsAfter)
	}
	if !tt.HasText("回复标签第一条回复") {
		t.Fatal("replies tab content was not preserved across tab switch")
	}

	// Trigger current-page batch cache on "回复" tab -> invokes CacheXPostsPage and does NOT enqueue background CacheQueue.
	app.CacheXCurrentTabUncached()
	tt.Frame()
	stub.mu.Lock()
	batchCount := len(stub.xBatchPageReqs)
	var batchTab string
	if batchCount > 0 {
		batchTab = stub.xBatchPageReqs[0].Tab
	}
	queueCount := len(stub.queuedReqs)
	stub.mu.Unlock()
	if batchCount != 1 || batchTab != "replies" {
		t.Fatalf("expected 1 CacheXPostsPage call on replies tab, got count=%d tab=%q", batchCount, batchTab)
	}
	if queueCount != 0 {
		t.Fatalf("replies tab batch cache must not touch cache-queue.json queue, got %d", queueCount)
	}

	// Direct OpenLink on /media opens straight onto "media" tab.
	app.SubmitParse("https://x.com/synth_creator/media")
	tt.Frame()
	tt.Frame()
	if app.CurrentRoute != RouteXUser || app.XUserTab != "media" {
		t.Fatalf("OpenLink(/media) = (route=%q tab=%q), want (RouteXUser, media)", app.CurrentRoute, app.XUserTab)
	}
}

func TestXUserPageFourTabsNoHorizontalOverflowAt720_1000_1280(t *testing.T) {
	app, _, cleanup := newXUITestHarness(t)
	defer cleanup()

	uid := "900005005"
	app.CurrentXUser = &service.XUserDTO{
		RestID:         uid,
		ScreenName:     "synth_layout_x",
		Name:           "宽窄窗口布局测试X账号名称",
		Description:    "验证 720 / 1000 / 1280 三档宽度下四标签与筛选零横向溢出",
		IsBlueVerified: true,
		FriendsCount:   123,
		FollowersCount: 45678,
		StatusesCount:  987,
		CanonicalURL:   "https://x.com/synth_layout_x",
	}
	app.CurrentXPosts = []service.XPostDTO{{
		ID:        "1900000000000005001",
		Author:    service.XAuthorDTO{RestID: uid, ScreenName: "synth_layout_x", Name: "宽窄窗口布局测试X账号名称"},
		CreatedAt: time.Now().Unix() - 60,
		Text:      "合成布局检查内容",
		CacheKey:  "x:dynamic:1900000000000005001",
	}}
	app.CurrentRoute = RouteXUser

	widths := []int{720, 1000, 1280}
	tabs := []string{"posts", "replies", "media", "highlights"}
	requiredLabels := []string{
		"X用户标签：帖子",
		"X用户标签：回复",
		"X用户标签：媒体",
		"X用户标签：精选",
		"用户页筛选：全部",
		"用户页筛选：未缓存",
		"X主页更多",
	}

	for _, tab := range tabs {
		for _, w := range widths {
			t.Run(fmt.Sprintf("%s_%dpx", tab, w), func(t *testing.T) {
				app.XUserTab = tab
				app.XUserTabs = nil
				app.XUserListState = &ui.ListState{}
				tt := ui.NewTester(app.View, w, 860)
				tt.Frame()

				leftRect, okL := tt.Find("左栏导航")
				centerRect, okC := tt.Find("中栏内容")
				if !okL || !okC || leftRect.X < -0.5 || centerRect.X+centerRect.W > float32(w)+0.5 {
					t.Fatalf("outer shell overflow at %dpx: left=%+v center=%+v", w, leftRect, centerRect)
				}

				prevTabRight := float32(-1)
				for _, lbl := range requiredLabels {
					r, ok := tt.Find(lbl)
					if !ok || r.W <= 0 || r.H <= 0 || r.X < -0.5 || r.X+r.W > float32(w)+0.5 {
						t.Fatalf("control %q out of bounds at %dpx: ok=%v rect=%+v", lbl, w, ok, r)
					}
					if lbl == "X用户标签：帖子" || lbl == "X用户标签：回复" || lbl == "X用户标签：媒体" || lbl == "X用户标签：精选" {
						if prevTabRight >= 0 && r.X < prevTabRight-0.5 {
							t.Fatalf("tab %q overlaps previous tab at %dpx: rect=%+v prevRight=%.1f", lbl, w, r, prevTabRight)
						}
						prevTabRight = r.X + r.W
					}
				}
			})
		}
	}
}
