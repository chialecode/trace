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
		parse, ok := tt.Find("快速解析输入框")
		if ok && parse.Y < bar.Height && parse.X+parse.W > float32(w)-bar.Right {
			t.Fatalf("width=%d: the paste box %+v sits under the window buttons", w, parse)
		}
		if drag, ok := tt.Find("窗口拖动区"); !ok || drag.Y != 0 || drag.H != bar.Height {
			t.Fatalf("width=%d: no drag strip above the rail: %+v", w, drag)
		}
		brand, _ := tt.Find("TRACE 主页")
		if brand.Y < bar.Height {
			t.Fatalf("width=%d: the brand %+v sits under the drag strip", w, brand)
		}
	}

	// Without a hidden title bar (tests, full screen) nothing moves.
	tt := ui.NewTester(app.View, 1280, 860)
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
