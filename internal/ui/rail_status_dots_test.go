package ui

import (
	"image"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

func subImageBytes(img *image.RGBA, r ui.Rect) []byte {
	b := img.Bounds()
	x0 := max(b.Min.X, int(r.X))
	y0 := max(b.Min.Y, int(r.Y))
	x1 := min(b.Max.X, int(r.X+r.W))
	y1 := min(b.Max.Y, int(r.Y+r.H))
	var out []byte
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			c := img.RGBAAt(x, y)
			out = append(out, c.R, c.G, c.B, c.A)
		}
	}
	return out
}

func hasLiveOrangePixel(img *image.RGBA, r ui.Rect) bool {
	b := img.Bounds()
	x0 := max(b.Min.X, int(r.X))
	y0 := max(b.Min.Y, int(r.Y))
	x1 := min(b.Max.X, int(r.X+r.W))
	y1 := min(b.Max.Y, int(r.Y+r.H))
	// ColorLive is #c2410c (R=194, G=65, B=12).
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			c := img.RGBAAt(x, y)
			if c.R > 160 && c.G > 40 && c.G < 110 && c.B < 45 {
				return true
			}
		}
	}
	return false
}

// The left-rail "订阅" button never shows a passive orange live dot or a
// "订阅 · 有订阅正在直播" tooltip when subscribed accounts go live or record;
// live and recording states remain proactively visible inside 主页, 订阅, and
// 任务, while Home's unread dots and Tasks' download count badge stay intact.
func TestRailSubscriptionOmitsPassiveLiveDotAndKeepsProactiveIndicators(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 3)

	liveSub, err := app.backend.CreateSubscription("直播创作者", "", []subscription.AccountRef{
		{Site: "bilibili", UID: "7701", Name: "直播创作者B"},
		{Site: "douyin", UID: "MS4wLjABAAAA_live_creator", Name: "直播创作者D"},
	}, subscription.SubRules{})
	if err != nil {
		t.Fatal(err)
	}
	unreadSub, err := app.backend.CreateSubscription("未读创作者", "", []subscription.AccountRef{
		{Site: "bilibili", UID: "1001", Name: "UP1"},
	}, subscription.SubRules{})
	if err != nil {
		t.Fatal(err)
	}
	st := app.backend.GetUIState()
	st.HomeSeen = map[string]int64{"sub:" + unreadSub.ID: 1}
	_, _ = app.backend.UpdateUIState(st)
	app.refreshSubscriptions()

	app.Navigate(RouteHome)
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()

	subRectIdle, ok := tt.Find("订阅")
	if !ok {
		t.Fatal("missing left-rail 订阅 button")
	}
	idlePixels := subImageBytes(tt.Image(), subRectIdle)

	// Activate both Bilibili and Douyin live rooms (with active recordings) and 2 download tasks.
	app.LiveRooms = []live.RoomState{
		{UID: 7701, Uname: "直播创作者B", RoomID: 8801, LiveStatus: 1, Recording: true, Title: "合成直播间B"},
	}
	app.LiveAccounts = []service.LiveAccountState{
		{Site: "douyin", UID: "MS4wLjABAAAA_live_creator", Uname: "直播创作者D", RoomID: "9901", LiveStatus: 1, Recording: true, Title: "合成直播间D"},
	}
	app.Tasks = []download.Task{
		{TaskID: "dl_1", Site: "bilibili", Kind: "video", TargetID: "BV1", Status: "running"},
		{TaskID: "dl_2", Site: "douyin", Kind: "video", TargetID: "7301", Status: "queued"},
		{TaskID: "dl_done", Site: "bilibili", Kind: "video", TargetID: "BV2", Status: "completed"},
	}
	app.RestartHomeSession()
	tt.Frame()

	subRectLive, ok := tt.Find("订阅")
	if !ok {
		t.Fatal("missing left-rail 订阅 button while live")
	}
	imgLive := tt.Image()
	if hasLiveOrangePixel(imgLive, subRectLive) {
		t.Fatal("left-rail 订阅 button still draws a passive orange live dot")
	}
	livePixels := subImageBytes(imgLive, subRectLive)
	if len(idlePixels) != len(livePixels) {
		t.Fatalf("订阅 button pixel size changed: %d vs %d", len(idlePixels), len(livePixels))
	}
	for i := range idlePixels {
		if idlePixels[i] != livePixels[i] {
			t.Fatal("left-rail 订阅 button changed pixels when subscribed rooms went live")
		}
	}

	// Hovering left-rail "订阅" shows plain "订阅", never "订阅 · 有订阅正在直播".
	tt.Move(subRectLive.X+subRectLive.W/2, subRectLive.Y+subRectLive.H/2)
	tt.Advance(2 * time.Second)
	tt.Frame()
	if tt.HasText("订阅 · 有订阅正在直播") || tt.HasText("有订阅正在直播") {
		t.Fatal("left-rail 订阅 tooltip still exposes passive live reminder text")
	}
	if !tt.HasText("订阅") {
		t.Fatal("expected plain 订阅 tooltip on hover")
	}

	// Left-rail "任务" still shows active download count badge (2, excluding live recordings).
	tasksRect, ok := tt.Find("任务")
	if !ok {
		t.Fatal("missing left-rail 任务 button")
	}
	tt.Move(tasksRect.X+tasksRect.W/2, tasksRect.Y+tasksRect.H/2)
	tt.Advance(2 * time.Second)
	tt.Frame()
	if !tt.HasText("任务 · 2 个下载排队或进行中") {
		t.Fatal("expected left-rail 任务 tooltip to keep active download count")
	}
	tt.Move(640, 430)
	tt.Frame()

	// Proactive indicators on Home remain intact: live subscription first with live ring, unread subscription keeps unread dot.
	rows := app.homeSubscriptionRows()
	if len(rows) == 0 || rows[0].SubID != liveSub.ID || !app.homeSubLive(rows[0]) {
		t.Fatalf("expected live subscription first with live ring on Home sidebar, got %+v", rows)
	}
	if _, ok := tt.Find("未读圆点：" + unreadSub.Name); !ok {
		t.Fatalf("expected Home sidebar unread dot for %s to stay intact", unreadSub.Name)
	}

	// Proactive indicators on Subscriptions page remain intact.
	app.Navigate(RouteSubscriptions)
	tt.Frame()
	if !app.isSubscriptionLive(liveSub) || !tt.HasText("录制中") {
		t.Fatal("expected Subscriptions page to keep proactive live/recording indicator")
	}

	// Proactive indicators on Tasks › 直播录制 remain intact.
	app.Navigate(RouteTasks)
	app.TasksSection = "live"
	tt.Frame()
	if !tt.HasText("录制中") {
		t.Fatal("expected Tasks › 直播录制 to keep proactive recording status")
	}
}
