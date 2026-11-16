package ui

import (
	"testing"

	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

// DEC-039: the library's kinds and sources are in the context panel beside
// the rail, not repeated in the page; a narrow window without the panel
// keeps them in the page.
func TestLibraryFiltersLiveInTheContextPanel(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 3)
	app.Navigate(RouteLibrary)

	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	panel, ok := tt.Find("上下文侧栏")
	if !ok {
		t.Fatal("the library has no context panel at 1280px")
	}
	video, ok := tt.Find("筛选类型：视频")
	if !ok || video.X+video.W > panel.X+panel.W {
		t.Fatalf("the kind filter is not in the panel: %+v (%v), panel %+v", video, ok, panel)
	}
	if _, dup := tt.Find("媒体库作者筛选"); dup {
		t.Fatal("the page repeats the source filter the panel holds")
	}
	if err := tt.Click("筛选类型：视频"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if app.LibraryKindFilter != "video" {
		t.Fatalf("kind filter = %q after choosing 视频 in the panel", app.LibraryKindFilter)
	}

	narrow := ui.NewTester(app.View, 720, 860)
	narrow.Frame()
	if _, ok := narrow.Find("上下文侧栏"); ok {
		t.Fatal("a 720px window has no room for the panel")
	}
	for _, label := range []string{"筛选类型：图片", "媒体库作者筛选"} {
		if _, ok := narrow.Find(label); !ok {
			t.Fatalf("without the panel the page lost %s", label)
		}
	}
}

// DEC-039, rewritten for DEC-053 ②: Home's panel lists the whole timeline
// and the subscriptions, the live ones first with a ring and no "正在直播"
// section; a live row opens that subscription's feed on Home. A live room
// nobody subscribed to is not listed.
func TestHomePanelListsLiveRooms(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 2)
	sub, err := app.backend.CreateSubscription("直播中的UP", "", []subscription.AccountRef{{Site: "bilibili", UID: "4242", Name: "直播中的UP"}}, subscription.SubRules{})
	if err != nil {
		t.Fatal(err)
	}
	app.refreshSubscriptions()
	app.LiveRooms = []live.RoomState{
		{UID: 4242, Uname: "直播中的UP", RoomID: 5150, LiveStatus: 1, Title: "直播标题"},
		{UID: 4343, Uname: "没订阅的UP", RoomID: 5151, LiveStatus: 1, Title: "别的直播"},
	}
	app.Navigate(RouteParse)
	app.Navigate(RouteHome)
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	for _, label := range []string{"主页全部动态", "主页订阅：直播中的UP"} {
		if _, ok := tt.Find(label); !ok {
			t.Fatalf("home panel misses %s", label)
		}
	}
	for _, label := range []string{"正在直播", "侧栏直播 5150", "主页订阅：没订阅的UP", "直播标题"} {
		if _, ok := tt.Find(label); ok {
			t.Fatalf("home panel still shows %s", label)
		}
	}
	if rows := app.homeSubscriptionRows(); len(rows) == 0 || rows[0].SubID != sub.ID || !app.homeSubLive(rows[0]) {
		t.Fatalf("the live subscription is not first with its ring: %+v", rows)
	}
	if err := tt.Click("主页订阅：直播中的UP"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if app.CurrentRoute != RouteHome || app.HomeActorFilter == nil || app.HomeActorFilter.SubID != sub.ID {
		t.Fatalf("a live row opened %s (%+v), want the subscription's feed on Home", app.CurrentRoute, app.HomeActorFilter)
	}
}

// Fourth-round feedback: a post shows one plain dot. A cached post whose
// video is complete shows only the post's green dot (no second mark on the
// video card); one whose video is partly cached shows a single yellow dot.
func TestPostShowsOneDotForItsWholeContent(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	post := PostCardData{
		Key: "dyn:1", ItemKey: "bilibili:dynamic:1", Kind: "dynamic", RawID: "1",
		AuthorName: "UP", Cached: true, DotStatus: "green", DotLabel: "已缓存：1",
		Video: &PostVideo{BVID: "BV1", Title: "视频", ItemKey: "bilibili:video:BV1", Cached: true, Status: "green"},
	}
	render := func(p PostCardData) *ui.Tester {
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Column(c).Width(600).Children(func() { app.renderPostCard(c, p) })
		}, 800, 700)
		tt.Frame()
		return tt
	}

	tt := render(post)
	if _, ok := tt.Find("已缓存：1"); !ok {
		t.Fatal("the cached post lost its dot")
	}
	if _, ok := tt.Find("已缓存，完整"); ok {
		t.Fatal("the video card carries a second dot")
	}

	post.Video.Status = "yellow"
	tt = render(post)
	if _, ok := tt.Find("部分缓存"); !ok {
		t.Fatal("a post with a partly cached video is not yellow")
	}
	if _, ok := tt.Find("已缓存：1"); ok {
		t.Fatal("a post with a partly cached video still shows green")
	}

	for _, c := range []struct{ post, video, want string }{
		{"", "", ""}, {"green", "", "green"}, {"", "green", "green"},
		{"green", "yellow", "yellow"}, {"yellow", "green", "yellow"},
		{"green", "running", "running"}, {"red", "yellow", "red"},
	} {
		p := PostCardData{DotStatus: c.post, Video: &PostVideo{Status: c.video}}
		if got := postDotStatus(p); got != c.want {
			t.Errorf("post %q + video %q = %q, want %q", c.post, c.video, got, c.want)
		}
	}
}
