package ui

import (
	"context"
	"testing"

	"github.com/egoist/mygo/ui"
)

// DEC-046 ④: a 52px rail; under the hidden title bar the page reaches the
// top, the window buttons keep the top-right corner to themselves, the top
// of the rail and panel moves the window, and the media panels open below
// the buttons.
func TestImmersiveTitleBarKeepsControlsClearOfWindowButtons(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()
	app.PopulateSyntheticFeed(20)

	bar := ui.TitleBar{Height: 32, Right: 138}
	for _, w := range []int{1280, 1000, 720} {
		app.Navigate(RouteLibrary) // a page using the full width
		tt := ui.NewTester(app.View, w, 860)
		tt.SetTitleBar(bar)
		tt.Frame()
		rail, ok := tt.Find("左栏导航")
		if !ok || rail.W != 52 || rail.Y != 0 {
			t.Fatalf("width=%d: rail %+v, want 52 wide from the top", w, rail)
		}
		top, ok := tt.Find("页面顶部栏")
		if !ok || top.Y != 0 {
			t.Fatalf("width=%d: the page's top bar does not reach the window top: %+v", w, top)
		}
		if _, ok := tt.Find("快速解析输入框"); ok {
			t.Fatalf("width=%d: unexpected quick-parse input in page top bar", w)
		}
		more, ok := tt.Find("媒体库更多菜单")
		if !ok {
			t.Fatalf("width=%d: missing library header more menu", w)
		}
		if more.Y < bar.Height && more.X+more.W > float32(w)-bar.Right {
			t.Fatalf("width=%d: the library more menu %+v sits under the window buttons", w, more)
		}
		if drag, ok := tt.Find("窗口拖动区"); !ok || drag.Y != 0 || drag.H != bar.Height {
			t.Fatalf("width=%d: no drag strip above the rail: %+v", w, drag)
		}
		// The mark is not a button and may sit in the drag strip; the first
		// page button lines up with the panel's first row (DEC-057).
		if _, ok := tt.Find("TRACE 标志"); !ok {
			t.Fatalf("width=%d: no TRACE mark on the rail", w)
		}
		home, ok := tt.Find("主页")
		if !ok || home.Y < bar.Height {
			t.Fatalf("width=%d: the first rail button %+v sits under the drag strip", w, home)
		}
	}
	app.Navigate(RouteHome)
	tt := ui.NewTester(app.View, 1280, 860)
	tt.SetTitleBar(bar)
	tt.Frame()
	home, _ := tt.Find("主页")
	first, ok := tt.Find("主页全部动态")
	if !ok {
		t.Fatal("no 全部动态 row in the home panel")
	}
	if d := (home.Y + home.H/2) - (first.Y + first.H/2); d > 6 || d < -6 {
		t.Fatalf("first rail button center %v is %vpx off the panel's first row center %v", home.Y+home.H/2, d, first.Y+first.H/2)
	}

	// Without a hidden title bar (tests, full screen) nothing moves.
	tt = ui.NewTester(app.View, 1280, 860)
	if _, ok := tt.Find("窗口拖动区"); ok {
		t.Fatal("a drag strip shows without a hidden title bar")
	}

	_, y, _, _ := mediaPanelRectBelow(1280, 560, 32)
	if y < 40 {
		t.Fatalf("media panel top %v sits under the window buttons", y)
	}
}

// DEC-046 ⑦: Home lists cached posts only and draws no status dot; the same
// post elsewhere (user page, subscription) keeps its dot (DEC-039 ⑥).
func TestHomePostsDrawNoStatusDot(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	render := func(post PostCardData) *ui.Tester {
		return ui.NewTester(func(c *ui.Context) {
			ui.Column(c).Width(720).Children(func() { app.renderPostCard(c, post) })
		}, 900, 600)
	}
	post := PostCardData{Key: "dot-post", Text: "合成帖子", DotStatus: "yellow", DotLabel: "部分缓存：合成"}
	if _, ok := render(post).Find("部分缓存：合成"); !ok {
		t.Fatal("a post outside Home lost its status dot")
	}
	post.InHomeFeed = true
	if _, ok := render(post).Find("部分缓存：合成"); ok {
		t.Fatal("a Home post draws a status dot")
	}
}

// DEC-069: page top bars omit the header quick-parse box at every width while
// keeping page titles and route-specific header actions intact; parsing lives
// solely on /parse.
func TestPageHeadersOmitQuickParseAndKeepRouteActions(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()
	app.PopulateSyntheticFeed(5)

	type routeCheck struct {
		name           string
		setup          func()
		requiredLabels []string
	}
	checks := []routeCheck{
		{
			name:           "home",
			setup:          func() { app.Navigate(RouteHome) },
			requiredLabels: []string{"页面顶部栏", "主页刷新"},
		},
		{
			name: "subscriptions",
			setup: func() {
				app.SubDetailOpen = false
				app.Navigate(RouteSubscriptions)
			},
			requiredLabels: []string{"页面顶部栏", "添加订阅", "订阅更多菜单"},
		},
		{
			name: "mine-list",
			setup: func() {
				app.Navigate(RouteMine)
				app.Mine.Site = "bilibili"
				app.Mine.Kind = "following"
				app.Mine.Picker = false
			},
			requiredLabels: []string{"页面顶部栏", "我的更多菜单"},
		},
		{
			name:           "parse",
			setup:          func() { app.Navigate(RouteParse) },
			requiredLabels: []string{"页面顶部栏", "链接解析输入框", "立即解析"},
		},
		{
			name: "library",
			setup: func() {
				app.Transfer.ImportPage = false
				app.Navigate(RouteLibrary)
			},
			requiredLabels: []string{"页面顶部栏", "媒体库更多菜单"},
		},
		{
			name: "tasks-downloads",
			setup: func() {
				app.TasksSection = "downloads"
				app.Navigate(RouteTasks)
			},
			requiredLabels: []string{"页面顶部栏", "检查画质升级"},
		},
		{
			name:           "video",
			setup:          func() { app.CurrentRoute = RouteVideo },
			requiredLabels: []string{"页面顶部栏", "返回"},
		},
		{
			name:           "user",
			setup:          func() { app.CurrentRoute = RouteUser },
			requiredLabels: []string{"页面顶部栏", "返回"},
		},
		{
			name:           "douyin-work",
			setup:          func() { app.CurrentRoute = RouteDouyinWork },
			requiredLabels: []string{"页面顶部栏", "返回"},
		},
		{
			name:           "douyin-user",
			setup:          func() { app.CurrentRoute = RouteDouyinUser },
			requiredLabels: []string{"页面顶部栏", "返回"},
		},
		{
			name:           "weibo-post",
			setup:          func() { app.CurrentRoute = RouteWeiboPost },
			requiredLabels: []string{"页面顶部栏", "返回"},
		},
		{
			name:           "weibo-user",
			setup:          func() { app.CurrentRoute = RouteWeiboUser },
			requiredLabels: []string{"页面顶部栏", "返回"},
		},
		{
			name:           "x-post",
			setup:          func() { app.CurrentRoute = RouteXPost },
			requiredLabels: []string{"页面顶部栏", "返回"},
		},
		{
			name:           "x-user",
			setup:          func() { app.CurrentRoute = RouteXUser },
			requiredLabels: []string{"页面顶部栏", "返回"},
		},
		{
			name:           "library-item",
			setup:          func() { app.CurrentRoute = RouteLibraryItem },
			requiredLabels: []string{"页面顶部栏", "返回"},
		},
		{
			name:           "settings",
			setup:          func() { app.Navigate(RouteSettings) },
			requiredLabels: []string{"页面顶部栏"},
		},
		{
			name:           "login",
			setup:          func() { app.Navigate(RouteLogin) },
			requiredLabels: []string{"页面顶部栏", "返回"},
		},
	}

	for _, rc := range checks {
		for _, w := range []int{1280, 1000, 720} {
			rc.setup()
			tt := ui.NewTester(app.View, w, 860)
			tt.Frame()

			title := app.routeTitle()
			if !tt.HasText(title) {
				t.Fatalf("route=%s width=%d: missing page title %q", rc.name, w, title)
			}
			for _, label := range rc.requiredLabels {
				r, ok := tt.Find(label)
				if !ok || r.W <= 0 || r.H <= 0 || r.X+r.W > float32(w)+0.5 {
					t.Fatalf("route=%s width=%d: expected control %q visible within bounds, got %+v (ok=%v)", rc.name, w, label, r, ok)
				}
			}
			for _, forbidden := range []string{"快速解析输入框", "快速解析提交"} {
				if r, ok := tt.Find(forbidden); ok {
					t.Fatalf("route=%s width=%d: unexpected header quick-parse control %q at %+v", rc.name, w, forbidden, r)
				}
			}
			if tt.HasText("粘贴链接、分享文本或 UID 解析") {
				t.Fatalf("route=%s width=%d: unexpected quick-parse placeholder text", rc.name, w)
			}
			if rc.name != "parse" {
				if _, ok := tt.Find("链接解析输入框"); ok {
					t.Fatalf("route=%s width=%d: /parse input should only appear on /parse", rc.name, w)
				}
			}
		}
	}

	// Parsing on /parse continues to work cleanly.
	app.Navigate(RouteParse)
	tt := ui.NewTester(app.View, 1280, 860)
	app.ParsePageInput = "BV1xx411c7mD"
	tt.Frame()
	if err := tt.Click("立即解析"); err != nil {
		t.Fatalf("failed to click 立即解析 on /parse: %v", err)
	}
	tt.Frame()
	if app.CurrentRoute != RouteVideo || app.CurrentVideo == nil {
		t.Fatalf("expected /parse submit to open /video, got route=%s video=%+v", app.CurrentRoute, app.CurrentVideo)
	}
}
