package ui

import (
	"context"
	"sync"
	"testing"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/egoist/mygo/ui"
)

type spyUserVideosBackend struct {
	service.BackendAPI
	mu    sync.Mutex
	calls []userVideosCall
	zones []bilibili.UserVideoZone
	items []bilibili.UserVideoItem
}

type userVideosCall struct {
	mid     int64
	page    int
	order   string
	keyword string
	tid     int
}

func (b *spyUserVideosBackend) UserVideosPage(ctx context.Context, mid int64, page int, opts ...service.UserVideosPageOptions) (bilibili.UserVideosPage, error) {
	var opt service.UserVideosPageOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	b.mu.Lock()
	b.calls = append(b.calls, userVideosCall{
		mid:     mid,
		page:    page,
		order:   bilibili.NormalizeUserVideosOrder(opt.Order),
		keyword: opt.Keyword,
		tid:     opt.TID,
	})
	zones := append([]bilibili.UserVideoZone(nil), b.zones...)
	items := append([]bilibili.UserVideoItem(nil), b.items...)
	b.mu.Unlock()
	return bilibili.UserVideosPage{
		Page:     page,
		PageSize: 30,
		Total:    len(items),
		Items:    items,
		Zones:    zones,
	}, nil
}

func (b *spyUserVideosBackend) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.calls)
}

func (b *spyUserVideosBackend) lastCall() userVideosCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls[len(b.calls)-1]
}

func TestBilibiliUserVideosSearchIMEGuardSortAndZones(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()

	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()

	spy := &spyUserVideosBackend{
		BackendAPI: app.backend,
		zones: []bilibili.UserVideoZone{
			{TID: 36, Name: "知识", Count: 18},
			{TID: 188, Name: "科技", Count: 7},
		},
		items: []bilibili.UserVideoItem{
			{BVID: "BV1ui0000001", Title: "空间投稿测试视频", Length: "04:12", PlayCount: 3200},
		},
	}
	app.backend = spy

	app.CurrentRoute = RouteUser
	app.CurrentUser = &bilibili.UserSpace{Mid: 8888, Name: "测试UP主"}
	app.UserTab = "videos"
	app.UserVideos = append([]bilibili.UserVideoItem(nil), spy.items...)
	app.UserVideosZones = append([]bilibili.UserVideoZone(nil), spy.zones...)
	app.UserVideosPage = 1

	tt := ui.NewTester(app.View, 1100, 860)

	// 1. Typing in search input must NOT trigger any request.
	tt.Click("空间投稿搜索框")
	tt.Type("算法")
	if spy.callCount() != 0 {
		t.Fatalf("typing must not fire search requests, got %d", spy.callCount())
	}

	// 2. Pressing Enter while IME is composing must NOT submit search.
	tt.Compose("jiaocheng", 9)
	tt.Key(0, ui.KeyEnter)
	if spy.callCount() != 0 {
		t.Fatalf("IME composing Enter must not submit search, got %d", spy.callCount())
	}

	// 3. End IME composition, type committed text, and press Enter -> submits exactly 1 request.
	tt.Compose("", 0)
	tt.Type("教程")
	tt.Frame()
	if spy.callCount() != 0 {
		t.Fatalf("IME commit alone must not fire search, got %d", spy.callCount())
	}
	tt.Key(0, ui.KeyEnter)
	if spy.callCount() != 1 {
		t.Fatalf("expected 1 search request after Enter outside IME, got %d", spy.callCount())
	}
	if got := spy.lastCall(); got.keyword != "算法教程" || got.order != "pubdate" || got.tid != 0 || got.page != 1 {
		t.Fatalf("unexpected search call: %+v", got)
	}

	// 4. Switching sort pill to "最多播放" resets pagination and queries order=click.
	tt.Frame()
	tt.Click("投稿排序：最多播放")
	if spy.callCount() != 2 {
		t.Fatalf("expected 2 requests after switching sort, got %d", spy.callCount())
	}
	if got := spy.lastCall(); got.order != "click" || got.keyword != "算法教程" || got.page != 1 {
		t.Fatalf("unexpected sort call: %+v", got)
	}

	// 5. Clicking zone pill "投稿分区：知识" queries tid=36 while keeping sort & keyword.
	tt.Frame()
	tt.Click("投稿分区：知识")
	if spy.callCount() != 3 {
		t.Fatalf("expected 3 requests after clicking zone pill, got %d", spy.callCount())
	}
	if got := spy.lastCall(); got.tid != 36 || got.order != "click" || got.keyword != "算法教程" || got.page != 1 {
		t.Fatalf("unexpected zone call: %+v", got)
	}

	// 6. Clearing search restores empty keyword while keeping sort & zone.
	tt.Frame()
	tt.Click("清空空间投稿搜索")
	if spy.callCount() != 4 {
		t.Fatalf("expected 4 requests after clearing search, got %d", spy.callCount())
	}
	if got := spy.lastCall(); got.keyword != "" || got.tid != 36 || got.order != "click" {
		t.Fatalf("unexpected clear call: %+v", got)
	}
}

func TestVideoDetailUpowerBadgesChaptersAndPlayerSeek(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()

	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()

	// 1. Locked upower video shows badge + notice and disables cache button.
	app.CurrentRoute = RouteVideo
	app.CurrentVideo = &bilibili.VideoDetail{
		BVID:              "BV1upLock9999",
		Title:             "充电专属未解锁视频",
		IsUpowerExclusive: true,
		IsUpowerPlay:      false,
		Author:            bilibili.AuthorInfo{Mid: 888, Name: "充电UP"},
		Pages:             []bilibili.VideoPage{{CID: 101, Page: 1, Title: "第一集", Duration: 120}},
	}
	tt := ui.NewTester(app.View, 1100, 860)
	if _, ok := tt.Find("充电专属标记"); !ok {
		t.Fatal("expected locked upower badge")
	}
	if _, ok := tt.Find("充电专属未解锁提示"); !ok {
		t.Fatal("expected locked upower notice banner")
	}
	// Clicking disabled "缓存视频" must not enqueue or trigger error.
	tt.Click("缓存视频")
	if app.BannerInfo != "" || app.BannerError != "" {
		t.Fatalf("clicking disabled cache button should do nothing, got info=%q err=%q", app.BannerInfo, app.BannerError)
	}

	// 2. Unlocked upower video shows "充电专属（已解锁）" badge and no locked notice.
	app.CurrentVideo.IsUpowerPlay = true
	tt.Frame()
	if _, ok := tt.Find("充电专属已解锁标记"); !ok {
		t.Fatal("expected unlocked upower badge")
	}
	if _, ok := tt.Find("充电专属未解锁提示"); ok {
		t.Fatal("unlocked upower video must not show locked notice")
	}

	// 3. Cached video with Chapters shows chapter pills in video detail and in PlayerOverlay.
	itemKey := library.CanonicalItemKey("bilibili", "video", "BV1upLock9999")
	item := library.ItemMetadata{
		Version: 1,
		ItemKey: itemKey,
		ID:      "video:BV1upLock9999",
		Site:    "bilibili",
		Kind:    "video",
		Status:  "complete",
		Title:   "充电专属已解锁视频",
		Parts: []library.VideoPartRecord{
			{
				Page:  1,
				CID:   101,
				Title: "第一集",
				File:  "p01.mp4",
				Chapters: []library.VideoChapter{
					{FromSec: 0, ToSec: 45, Title: "开场"},
					{FromSec: 45, ToSec: 120, Title: "核心演示"},
				},
			},
		},
	}
	app.compactLibCache = map[string]compactLibEntry{
		itemKey: {dotStatus: "green", dotLabel: "已缓存", item: &item},
	}
	app.compactLibCacheRev = app.cacheStateRev
	tt.Frame()
	if _, ok := tt.Find("分P 1 章节看点"); !ok {
		t.Fatal("expected chapters section in video detail")
	}
	if _, ok := tt.Find("视频详情跳转章节：P1-2"); !ok {
		t.Fatal("expected chapter 2 jump pill in video detail")
	}

	// Open PlayerOverlay with chapters and verify chapter bar renders.
	app.PlayerOverlay.Open = true
	app.PlayerOverlay.Title = item.Title
	app.PlayerOverlay.Chapters = item.Parts[0].Chapters
	app.PlayerOverlay.ControlsHidden = false
	tt.Frame()
	if _, ok := tt.Find("播放器章节栏"); !ok {
		t.Fatal("expected chapter bar inside PlayerOverlay")
	}
	if _, ok := tt.Find("章节跳转：2"); !ok {
		t.Fatal("expected chapter 2 button inside PlayerOverlay")
	}
}

func TestBlockedOnlyFansDynamicCardRendersNoticeWithoutCacheAction(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()

	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()

	app.CurrentRoute = RouteVideo
	app.CurrentVideo = nil
	app.CurrentDynamic = &bilibili.DynamicItem{
		ID:          "9988776655",
		Type:        "DYNAMIC_TYPE_DRAW",
		TypeLabel:   "图文",
		Title:       "充电专属图文预告",
		Text:        "欢迎开通充电解锁高清原图",
		PubTs:       1700000000,
		Author:      bilibili.AuthorInfo{Mid: 888, Name: "充电UP"},
		IsOnlyFans:  true,
		Blocked:     true,
		BlockedHint: "成为高档位充电会员后即可查看",
	}

	tt := ui.NewTester(app.View, 1100, 860)
	if _, ok := tt.Find("充电专属锁定提示：dyn:9988776655"); !ok {
		t.Fatal("expected blocked onlyfans notice box on dynamic card")
	}
	// Open post menu and verify cache action is omitted for blocked dynamic.
	tt.Click("动态更多：dyn:9988776655")
	tt.Frame()
	if _, ok := tt.Find("缓存该动态"); ok {
		t.Fatal("blocked onlyfans dynamic should not expose cache action in menu")
	}
}
