package ui

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

func TestClickableHyperlinksAcrossViewsAndCachedItems(t *testing.T) {
	app, stub, cleanup := newWeiboUITestHarness(t)
	defer cleanup()

	desk := stub.raw.Desktop().(*service.MockDesktop)
	if _, err := stub.raw.ImportCookie(context.Background(), "SESSDATA=synth_bili; bili_jct=csrf; DedeUserID=12345", ""); err != nil {
		t.Fatalf("ImportCookie: %v", err)
	}
	stub.mu.Lock()
	stub.wbStatus = credential.Status{Site: string(site.Weibo), LoggedIn: true, State: "logged_in", UID: "100000001", Uname: "测试账号"}
	stub.mu.Unlock()

	// 1. Weibo online post detail: structured links (including duplicate "网页链接" labels and retweet links)
	// render as clickable labels without exposing raw URLs, and clicking opens the exact target URL.
	app.CurrentWeiboPost = &service.WeiboPostView{
		Mid:       "5000000000009101",
		Author:    service.WeiboAuthorDTO{UID: "500000001", ScreenName: "技术频道_微博"},
		CreatedAt: 1710005000,
		Text:      "发布归档说明 #系统工程# 第一篇 网页链接 第二篇 网页链接 欢迎查阅。",
		Links: []service.TextLinkDTO{
			{Label: "#系统工程#", URL: "https://s.weibo.com/weibo?q=%23%E7%B3%BB%E7%BB%9F%E5%B7%A5%E7%A8%8B%23"},
			{Label: "网页链接", URL: "https://example.com/first-doc"},
			{Label: "网页链接", URL: "https://example.com/second-doc"},
		},
		IsRetweet: true,
		Retweeted: &service.WeiboPostView{
			Mid:       "5000000000009100",
			Author:    service.WeiboAuthorDTO{UID: "500000002", ScreenName: "原博主"},
			CreatedAt: 1710004000,
			Text:      "原博附架构参考 规范指南 供对照。",
			Links: []service.TextLinkDTO{
				{Label: "规范指南", URL: "https://example.org/architecture-spec"},
			},
			CanonicalURL: "https://weibo.com/500000002/5000000000009100",
		},
		CanonicalURL: "https://weibo.com/500000001/5000000000009101",
		CacheKey:     "weibo:dynamic:5000000000009101",
	}
	app.CurrentLibraryDetail = nil
	app.CurrentRoute = RouteWeiboPost

	tt := ui.NewTester(app.View, 1280, 960)
	tt.Frame()

	for _, rawAddr := range []string{"https://example.com/first-doc", "https://example.com/second-doc", "https://example.org/architecture-spec"} {
		if tt.HasText(rawAddr) {
			t.Fatalf("Weibo post detail should render concise link labels instead of raw URL %q", rawAddr)
		}
	}
	if err := tt.Click("打开链接：网页链接"); err != nil {
		t.Fatalf("Click first 网页链接 failed: %v", err)
	}
	if err := tt.Click("打开链接：网页链接 (2)"); err != nil {
		t.Fatalf("Click second 网页链接 failed: %v", err)
	}
	if err := tt.Click("打开链接：规范指南"); err != nil {
		t.Fatalf("Click retweet 规范指南 failed: %v", err)
	}
	actions := desk.Actions()
	for _, wantURL := range []string{
		"https://example.com/first-doc",
		"https://example.com/second-doc",
		"https://example.org/architecture-spec",
	} {
		if !slices.Contains(actions, service.RecordedAction{Kind: "url", Target: wantURL}) {
			t.Fatalf("expected MockDesktop url %q, got %+v", wantURL, actions)
		}
	}

	// 2. Cached items (both new structured item.Links AND legacy already-cached "标题 (https://...)" / raw URLs)
	// work in Home feed AND offline /library/item view!
	legacyDir := filepath.Join(stub.raw.LibraryStore().CacheDir(), "weibo", "500000001_技术频道_微博", "posts", "5000000000009102")
	savedLegacy, err := stub.raw.LibraryStore().SaveItem(legacyDir, library.ItemMetadata{
		Version:   1,
		ID:        "dynamic:5000000000009102",
		RawID:     "5000000000009102",
		Site:      "weibo",
		Kind:      "dynamic",
		TypeLabel: "微博",
		Status:    "complete",
		Title:     "既有缓存微博兼容测试",
		Text:      "历史缓存条目：参考 合成主页 (https://weibo.com/u/1000000001) 以及裸链接 https://docs.example.com/2026/handbook/intro?from=feed 结束。",
		URL:       "https://weibo.com/500000001/5000000000009102",
		PubTs:     1710006000,
		CachedAt:  "2026-03-03T10:00:00Z",
		Author:    library.ItemAuthor{Site: "weibo", UID: "500000001", Name: "技术频道_微博"},
	})
	if err != nil {
		t.Fatalf("SaveItem(legacy): %v", err)
	}

	app.OpenLibraryItemDetail(savedLegacy)
	tt.Frame()
	if tt.HasText("https://weibo.com/u/1000000001") || tt.HasText("https://docs.example.com") {
		t.Fatal("legacy cached item still rendered raw https:// URL text instead of concise hyperlink label")
	}
	if err := tt.Click("打开链接：合成主页"); err != nil {
		t.Fatalf("Click legacy 合成主页 failed: %v", err)
	}
	conciseLabel := FormatConciseLinkLabel("https://docs.example.com/2026/handbook/intro?from=feed")
	if err := tt.Click("打开链接：" + conciseLabel); err != nil {
		t.Fatalf("Click concise bare URL label %q failed: %v", conciseLabel, err)
	}
	actions = desk.Actions()
	if !slices.Contains(actions, service.RecordedAction{Kind: "url", Target: "https://weibo.com/u/1000000001"}) ||
		!slices.Contains(actions, service.RecordedAction{Kind: "url", Target: "https://docs.example.com/2026/handbook/intro?from=feed"}) {
		t.Fatalf("expected legacy & bare URL clicks recorded in MockDesktop, got %+v", actions)
	}

	// Verify Home feed PostCard also renders clickable links on cached entries.
	if _, err := stub.raw.CreateSubscription("技术频道_微博", "", []subscription.AccountRef{
		{Site: "weibo", UID: "500000001", Name: "技术频道_微博"},
	}, subscription.SubRules{ScheduledCheck: true}); err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	app.CompleteLogin()
	app.Navigate(RouteHome)
	tt.Frame()
	if err := tt.Click("主页站点筛选：微博"); err != nil {
		t.Fatalf("Click Home Weibo filter: %v", err)
	}
	tt.Frame()
	if _, ok := tt.Find("打开链接：合成主页"); !ok {
		t.Fatal("expected clickable link 打开链接：合成主页 in Home feed PostCard")
	}

	// 3. X post detail: URLEntity DisplayURL -> ExpandedURL + self-reply thread inline URL.
	app.CurrentXPost = &service.XPostDTO{
		ID:        "1900000000000000901",
		Author:    service.XAuthorDTO{RestID: "900004004", ScreenName: "synth_x", Name: "合成X作者"},
		CreatedAt: 1710007000,
		Text:      "发布新版本说明 https://github.com/org/project/releases/tag/v2.0.0 欢迎体验。",
		URLs: []service.XURLEntityDTO{
			{
				ShortURL:    "https://t.co/synthrel",
				ExpandedURL: "https://github.com/org/project/releases/tag/v2.0.0",
				DisplayURL:  "github.com/org/project/re…",
			},
		},
		Thread: []service.XPostDTO{
			{
				ID:        "1900000000000000902",
				Author:    service.XAuthorDTO{RestID: "900004004", ScreenName: "synth_x", Name: "合成X作者"},
				CreatedAt: 1710007060,
				Text:      "补充基准测试报告 https://bench.example.dev/report",
			},
		},
		CanonicalURL: "https://x.com/synth_x/status/1900000000000000901",
	}
	app.CurrentLibraryDetail = nil
	app.CurrentRoute = RouteXPost
	tt.Frame()

	if tt.HasText("https://github.com/org/project/releases/tag/v2.0.0") {
		t.Fatal("X post detail rendered raw expanded URL instead of DisplayURL")
	}
	if err := tt.Click("打开链接：github.com/org/project/re…"); err != nil {
		t.Fatalf("Click X DisplayURL link failed: %v", err)
	}
	if err := tt.Click("打开链接：bench.example.dev/report"); err != nil {
		t.Fatalf("Click X thread link failed: %v", err)
	}
	actions = desk.Actions()
	if !slices.Contains(actions, service.RecordedAction{Kind: "url", Target: "https://github.com/org/project/releases/tag/v2.0.0"}) ||
		!slices.Contains(actions, service.RecordedAction{Kind: "url", Target: "https://bench.example.dev/report"}) {
		t.Fatalf("expected X post & thread clicks recorded, got %+v", actions)
	}

	// 3b. Cached offline X thread detail: clickable links in both focal and continuation parts.
	app.OpenLibraryItemDetail(library.ItemMetadata{
		ItemKey: "x:dynamic:1900000000000000905",
		Site:    "x",
		Kind:    "dynamic",
		ID:      "1900000000000000905",
		RawID:   "1900000000000000905",
		Text:    "离线串首条 架构设计\n\n离线串续条 性能报告",
		PubTs:   1710007100,
		Author:  library.ItemAuthor{Site: "x", UID: "900004004", UniqueID: "synth_x", Name: "合成X作者"},
		Thread: []library.ItemThreadPart{
			{
				ID:    "1900000000000000905",
				PubTs: 1710007100,
				Text:  "离线串首条 架构设计",
				Links: []library.ItemLink{{Label: "架构设计", URL: "https://docs.example.dev/arch"}},
			},
			{
				ID:    "1900000000000000906",
				PubTs: 1710007160,
				Text:  "离线串续条 性能报告",
				Links: []library.ItemLink{{Label: "性能报告", URL: "https://docs.example.dev/perf"}},
			},
		},
	})
	tt.Frame()
	if err := tt.Click("打开链接：架构设计"); err != nil {
		t.Fatalf("Click offline X thread focal link failed: %v", err)
	}
	if err := tt.Click("打开链接：性能报告"); err != nil {
		t.Fatalf("Click offline X thread continuation link failed: %v", err)
	}
	actions = desk.Actions()
	if !slices.Contains(actions, service.RecordedAction{Kind: "url", Target: "https://docs.example.dev/arch"}) ||
		!slices.Contains(actions, service.RecordedAction{Kind: "url", Target: "https://docs.example.dev/perf"}) {
		t.Fatalf("expected offline X thread link clicks recorded, got %+v", actions)
	}

	// 4. Security check: non-HTTP(S) schemes (javascript:, file:, data:, userinfo) are strictly rejected
	// both in rich-text parsing (never clickable) and in openExternalURL!
	beforeCount := len(desk.Actions())
	app.CurrentWeiboPost = &service.WeiboPostView{
		Mid:          "5000000000009199",
		Author:       service.WeiboAuthorDTO{UID: "500000001", ScreenName: "安全测试"},
		CreatedAt:    1710008000,
		Text:         "恶意协议测试 javascript:alert(1) 与 file:///C:/Windows/win.ini 结束",
		Links:        []service.TextLinkDTO{{Label: "恶意脚本", URL: "javascript:alert(1)"}, {Label: "本地文件", URL: "file:///C:/Windows/win.ini"}},
		CanonicalURL: "https://weibo.com/500000001/5000000000009199",
	}
	app.CurrentRoute = RouteWeiboPost
	tt.Frame()

	if _, ok := tt.Find("打开链接：恶意脚本"); ok {
		t.Fatal("javascript: link must NEVER be rendered as clickable")
	}
	if _, ok := tt.Find("打开链接：本地文件"); ok {
		t.Fatal("file: link must NEVER be rendered as clickable")
	}
	app.openExternalURL("javascript:alert(1)")
	app.openExternalURL("file:///etc/passwd")
	app.openExternalURL("https://user:pass@example.com/secret")
	app.openExternalURL("http://:8080/path")
	if _, ok := ValidateSafeJumpURL("http://:8080/path"); ok {
		t.Fatal("ValidateSafeJumpURL must reject port-only host without hostname")
	}
	if got := len(desk.Actions()); got != beforeCount {
		t.Fatalf("unsafe schemes reached Desktop! before=%d after=%d actions=%v", beforeCount, got, desk.Actions())
	}
	if app.BannerError == "" {
		t.Fatal("expected BannerError when rejecting unsafe scheme in openExternalURL")
	}
}

func TestMinePostRowHyperlinkJumpWithoutNavigatingAway(t *testing.T) {
	app, stub, cleanup := newMineApp(t)
	defer cleanup()

	desk := stub.BackendAPI.Desktop().(*service.MockDesktop)
	stub.logged["weibo"] = true
	wbPost := service.WeiboPostView{
		Mid:          "5000000000009105",
		Author:       service.WeiboAuthorDTO{UID: "500000008", ScreenName: "收藏博主"},
		CreatedAt:    1710006500,
		Text:         "收藏条目整理：参考 收藏链接地址 查看详情。",
		Links:        []service.TextLinkDTO{{Label: "收藏链接地址", URL: "https://example.com/fav-jump"}},
		CanonicalURL: "https://weibo.com/500000008/5000000000009105",
	}
	stub.pages["weibo/collection//"] = service.AccountPage{
		Items: []service.AccountItem{
			{
				Kind:   service.AccountItemPost,
				Site:   "weibo",
				ID:     wbPost.Mid,
				Author: "收藏博主",
				Post:   &wbPost,
			},
		},
	}

	app.Navigate(RouteMine)
	app.mineSelect("weibo", service.AccountCollection)
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	tt.Frame()

	if err := tt.Click("打开链接：收藏链接地址"); err != nil {
		t.Fatalf("Click Mine Weibo row hyperlink failed: %v", err)
	}
	if app.CurrentRoute != RouteMine {
		t.Fatalf("clicking hyperlink inside Mine row navigated away to %v, want RouteMine", app.CurrentRoute)
	}
	if !slices.Contains(desk.Actions(), service.RecordedAction{Kind: "url", Target: "https://example.com/fav-jump"}) {
		t.Fatalf("expected Mine hyperlink click recorded in MockDesktop, got %+v", desk.Actions())
	}
}

func TestLegacyCachedSinaWeiboAppLinksFallbackOffline(t *testing.T) {
	app, stub, cleanup := newWeiboUITestHarness(t)
	defer cleanup()

	desk := stub.raw.Desktop().(*service.MockDesktop)
	const cid = "100808c9a1f5c18abbfc31cecd32f45a14ddc1"
	legacyDir := filepath.Join(stub.raw.LibraryStore().CacheDir(), "weibo", "500000001_技术频道_微博", "posts", "5000000000009188")
	savedLegacy, err := stub.raw.LibraryStore().SaveItem(legacyDir, library.ItemMetadata{
		Version:   1,
		ID:        "dynamic:5000000000009188",
		RawID:     "5000000000009188",
		Site:      "weibo",
		Kind:      "dynamic",
		TypeLabel: "微博",
		Status:    "complete",
		Title:     "旧缓存超话链接离线展示",
		Text: "合成超话 (sinaweibo://pageinfo?containerid=" + cid + "&extparam=合成超话)\n" +
			"查看 主页入口 (sinaweibo://userinfo?uid=1000000001) 与 危险入口 (sinaweibo://browser?url=javascript:alert(1)) 结束。",
		URL:      "https://weibo.com/500000001/5000000000009188",
		PubTs:    1710006800,
		CachedAt: "2026-03-04T10:00:00Z",
		Author:   library.ItemAuthor{Site: "weibo", UID: "500000001", Name: "技术频道_微博"},
	})
	if err != nil {
		t.Fatalf("SaveItem(legacy sinaweibo): %v", err)
	}

	app.OpenLibraryItemDetail(savedLegacy)
	tt := ui.NewTester(app.View, 1280, 900)
	tt.Frame()

	if tt.HasText("sinaweibo://pageinfo") || tt.HasText("sinaweibo://userinfo") {
		t.Fatal("legacy cached sinaweibo://pageinfo or userinfo URL must render as clean clickable label")
	}
	if _, ok := tt.Find("打开链接：危险入口"); ok {
		t.Fatal("unknown sinaweibo://browser link must NEVER render as clickable")
	}
	if err := tt.Click("打开链接：合成超话"); err != nil {
		t.Fatalf("Click legacy supertopic link failed: %v", err)
	}
	if err := tt.Click("打开链接：主页入口"); err != nil {
		t.Fatalf("Click legacy userinfo link failed: %v", err)
	}
	actions := desk.Actions()
	if !slices.Contains(actions, service.RecordedAction{Kind: "url", Target: "https://weibo.com/p/" + cid}) {
		t.Fatalf("expected converted supertopic URL in MockDesktop, got %+v", actions)
	}
	if !slices.Contains(actions, service.RecordedAction{Kind: "url", Target: "https://weibo.com/u/1000000001"}) {
		t.Fatalf("expected converted userinfo URL in MockDesktop, got %+v", actions)
	}
}
