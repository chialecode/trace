package ui

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/egoist/mygo/ui"
)

type weiboEditHistoryStub struct {
	*weiboStubBackend
	ehCalls atomic.Int32
	ehRevs  []service.WeiboEditRevisionDTO
	ehErr   error
}

func (s *weiboEditHistoryStub) WeiboPostEditHistory(_ context.Context, _ string) ([]service.WeiboEditRevisionDTO, error) {
	s.ehCalls.Add(1)
	if s.ehErr != nil {
		return nil, s.ehErr
	}
	return s.ehRevs, nil
}

func TestWeiboOnlineEditHistory_ExpandLoadAndFallback(t *testing.T) {
	app, baseStub, cleanup := newWeiboUITestHarness(t)
	defer cleanup()

	baseStub.mu.Lock()
	baseStub.wbStatus = credential.Status{Site: string(site.Weibo), State: "logged_in", LoggedIn: true, UID: "500000001", Uname: "合成测试"}
	baseStub.mu.Unlock()

	posted := make(chan func(), 32)
	app.dispatch = func(fn func()) { posted <- fn }
	drain := func() {
		for {
			select {
			case fn := <-posted:
				fn()
			default:
				return
			}
		}
	}

	stub := &weiboEditHistoryStub{
		weiboStubBackend: baseStub,
		ehRevs: []service.WeiboEditRevisionDTO{
			{
				Version: 3,
				EditTs:  1764203600,
				Text:    "最新修订版正文，含说明链接",
				Links:   []service.TextLinkDTO{{Label: "说明文档", URL: "https://example.org/v3"}},
			},
			{
				Version: 2,
				EditTs:  1764201800,
				Text:    "第二次修订版正文：补充实验数据",
			},
			{
				Version: 1,
				EditTs:  1764200000,
				Text:    "最初首发版本正文",
			},
		},
	}
	app.backend = stub
	app.CurrentRoute = RouteWeiboPost
	app.CurrentWeiboPost = &service.WeiboPostView{
		Mid:       "5009001002003001",
		Author:    service.WeiboAuthorDTO{UID: "500000001", ScreenName: "合成博主"},
		CreatedAt: 1764203600,
		EditCount: 2,
		Text:      "最新修订版正文，含说明链接",
		Source:    "微博网页版",
		CacheKey:  "weibo:dynamic:5009001002003001",
	}

	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()

	// Collapsed by default -> 0 editHistory calls until clicked.
	if stub.ehCalls.Load() != 0 {
		t.Fatalf("expected 0 editHistory calls while collapsed, got %d", stub.ehCalls.Load())
	}
	if _, ok := tt.Find("切换微博编辑历史"); !ok {
		t.Fatalf("expected 切换微博编辑历史 button visible")
	}

	// Click toggle to expand and trigger async load.
	if err := tt.Click("切换微博编辑历史"); err != nil {
		t.Fatalf("click 切换微博编辑历史: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		drain()
		tt.Frame()
		if len(app.WeiboEditHistoryItems) == 3 {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if stub.ehCalls.Load() != 1 {
		t.Fatalf("expected 1 editHistory call after expand, got %d", stub.ehCalls.Load())
	}
	if _, ok := tt.Find("编辑历史版本 3"); !ok {
		t.Fatalf("expected 编辑历史版本 3 rendered after load")
	}
	if _, ok := tt.Find("编辑历史版本 1"); !ok {
		t.Fatalf("expected 编辑历史版本 1 rendered after load")
	}

	// Verify 720 / 1000 / 1280 widths render cleanly without horizontal overflow.
	for _, w := range []int{720, 1000, 1280} {
		wtt := ui.NewTester(app.View, w, 860)
		wtt.Frame()
		sec, ok := wtt.Find("微博编辑历史区")
		if !ok {
			t.Fatalf("width %d: missing 微博编辑历史区", w)
		}
		if sec.X+sec.W > float32(w)+1 {
			t.Fatalf("width %d: 微博编辑历史区 overflowed right edge: %+v", w, sec)
		}
	}

	// Error + fallback to locally cached EditHistory.
	stub.ehErr = errors.New("weibo [rate_limited]: 请求过于频繁")
	app.WeiboEditHistoryMid = ""
	app.WeiboEditHistoryItems = nil
	app.WeiboEditHistoryExpanded = false
	app.CurrentWeiboPost.CachedItem = &library.ItemMetadata{
		Version:   1,
		ItemKey:   "weibo:dynamic:5009001002003001",
		Site:      "weibo",
		Kind:      "dynamic",
		EditCount: 2,
		EditHistory: []library.ItemEditRevision{
			{Version: 1, EditTs: 1764200000, Text: "本机缓存的原始版本正文"},
		},
	}
	tt.Frame()
	if err := tt.Click("切换微博编辑历史"); err != nil {
		t.Fatalf("click retry expand: %v", err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		drain()
		tt.Frame()
		if app.WeiboEditHistoryError != "" {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if _, ok := tt.Find("重试加载微博编辑历史"); !ok {
		t.Fatalf("expected retry button on editHistory error")
	}
	if _, ok := tt.Find("本机编辑历史版本 1"); !ok {
		t.Fatalf("expected cached edit history fallback rendered on error")
	}
}

func TestWeiboOfflineEditHistory_ZeroNetworkAndResponsiveWidths(t *testing.T) {
	app, baseStub, cleanup := newWeiboUITestHarness(t)
	defer cleanup()

	baseStub.mu.Lock()
	baseStub.wbStatus = credential.Status{Site: string(site.Weibo), State: "logged_in", LoggedIn: true, UID: "500000001", Uname: "合成测试"}
	baseStub.mu.Unlock()

	stub := &weiboEditHistoryStub{weiboStubBackend: baseStub}
	app.backend = stub

	item := library.ItemMetadata{
		Version:   1,
		ID:        "dynamic:5009001002003099",
		RawID:     "5009001002003099",
		ItemKey:   "weibo:dynamic:5009001002003099",
		Site:      "weibo",
		Kind:      "dynamic",
		Status:    "complete",
		Title:     "离线已编辑微博示例",
		Text:      "最终修订正文",
		PubTs:     1764203600,
		EditCount: 2,
		Author:    library.ItemAuthor{Site: "weibo", UID: "500000001", Name: "合成博主"},
		Media: []library.MediaFile{
			{Role: "image", Path: "images/01.png", Width: 640, Height: 480},
			{Role: "edit_image", Path: "images/edit_v01_01.png", Width: 640, Height: 480},
		},
		EditHistory: []library.ItemEditRevision{
			{
				Version:    3,
				EditTs:     1764203600,
				Text:       "最终修订正文",
				Links:      []library.ItemLink{{Label: "项目主页", URL: "https://example.org/home"}},
				MediaPaths: []string{"images/01.png"},
			},
			{
				Version:    1,
				EditTs:     1764200000,
				Text:       "首次发布原稿正文（旧配图已替换）",
				MediaPaths: []string{"images/edit_v01_01.png"},
			},
		},
	}

	app.OpenLibraryItemDetail(item)

	for _, w := range []int{720, 1000, 1280} {
		tt := ui.NewTester(app.View, w, 860)
		tt.Frame()
		sec, ok := tt.Find("本机微博编辑历史区")
		if !ok {
			t.Fatalf("width %d: missing 本机微博编辑历史区", w)
		}
		if sec.X+sec.W > float32(w)+1 {
			t.Fatalf("width %d: 本机微博编辑历史区 overflowed: %+v", w, sec)
		}
		if _, ok := tt.Find("本机编辑历史版本 3"); !ok {
			t.Fatalf("width %d: expected 本机编辑历史版本 3", w)
		}
		if _, ok := tt.Find("本机编辑历史版本 1"); !ok {
			t.Fatalf("width %d: expected 本机编辑历史版本 1", w)
		}
	}

	// Strictly zero network requests in offline mode!
	if stub.ehCalls.Load() != 0 || stub.wbCalls.Load() != 0 {
		t.Fatalf("expected 0 network calls in offline detail, got eh=%d wb=%d", stub.ehCalls.Load(), stub.wbCalls.Load())
	}

	// Toggle collapse works without any network call.
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	if err := tt.Click("切换本机微博编辑历史"); err != nil {
		t.Fatalf("click 切换本机微博编辑历史: %v", err)
	}
	tt.Frame()
	if _, ok := tt.Find("本机编辑历史版本 1"); ok {
		t.Fatalf("expected offline edit history collapsed after click")
	}
	if !strings.Contains(app.WeiboOfflineEditHistoryKey, "5009001002003099") {
		t.Fatalf("unexpected WeiboOfflineEditHistoryKey=%q", app.WeiboOfflineEditHistoryKey)
	}
}
