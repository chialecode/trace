package ui

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
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

func TestXHyperlinksEndToEndEdgeCasesAndSecurity(t *testing.T) {
	// 1. Unit assertions for ValidateSafeJumpURL security boundary (rejects signed/CDN/unsafe, keeps clean web URLs).
	for _, bad := range []string{
		"",
		"javascript:alert(1)",
		"file:///C:/Windows/win.ini",
		"https://user:pass@example.com/secret",
		"http://:8080/path",
		`https://example.com/a\b`,
		"https://video.twimg.com/ext_tw_video/1/pu/vid/a.mp4",
		"https://video-s.twimg.com/ext_tw_video/1/a.mp4",
		"https://ton.x.com/i/ton/data/dm/123",
		"https://pbs.twimg.com/media/synth.jpg?format=jpg&name=orig",
		"https://prod.pscp.tv/live/1.m3u8",
		"https://f.video.weibocdn.com/a.mp4?Expires=1800000000&ssig=abc",
		"https://example.com/page?auth_token=SECRET&ct0=CSRF",
		"https://example.com/page?X-Amz-Signature=deadbeef",
		"https://example.com/page?token=ephemeral",
	} {
		if got, ok := ValidateSafeJumpURL(bad); ok {
			t.Fatalf("ValidateSafeJumpURL(%q) = (%q, true), want rejected", bad, got)
		}
	}
	for _, good := range []string{
		"https://example.com/docs/subpage",
		"https://example.com/blog/token-bucket",
		"https://example.com/articles/policy-guide",
		"https://example.com/page?a=1&copy=2&reg=us#sec-1",
		"HTTPS://Example.COM/path",
	} {
		if got, ok := ValidateSafeJumpURL(good); !ok || got == "" {
			t.Fatalf("ValidateSafeJumpURL(%q) = (%q, %v), want allowed", good, got, ok)
		}
	}

	// 2. Parser edge cases: CJK punctuation, non-ASCII IRI, parenthesized English prose,
	// cached fragment mismatch, legacy t.co pairing, display_url case/www/... mismatch, bare domains.
	t.Run("non-ASCII IRI, CJK punctuation, and parenthesized English prose", func(t *testing.T) {
		paras, hasRich := parseRichTextParagraphs(
			"参考 https://zh.wikipedia.org/wiki/人工智能，以及 https://example.com/docs很赞。\n"+
				"Check the release (https://github.com/org/repo/releases/v2) and (https://example.org/notes)!",
			[]service.TextLinkDTO{
				{Label: "github.com/org/repo/rel…", URL: "https://github.com/org/repo/releases/v2"},
			},
			false,
		)
		if !hasRich || len(paras) != 2 {
			t.Fatalf("unexpected paragraphs: hasRich=%v paras=%+v", hasRich, paras)
		}
		var gotLinks []richSpan
		for _, p := range paras {
			for _, sp := range p.Spans {
				if sp.Kind == richSpanLink {
					gotLinks = append(gotLinks, sp)
				}
			}
		}
		if len(gotLinks) != 4 {
			t.Fatalf("expected 4 links, got %+v", gotLinks)
		}
		if gotLinks[0].URL != "https://zh.wikipedia.org/wiki/%E4%BA%BA%E5%B7%A5%E6%99%BA%E8%83%BD" {
			t.Fatalf("IRI link corrupted: %+v", gotLinks[0])
		}
		if gotLinks[1].URL != "https://example.com/docs" {
			t.Fatalf("CJK-adjacent ASCII URL over-consumed: %+v", gotLinks[1])
		}
		if gotLinks[2].Label != "github.com/org/repo/rel…" || gotLinks[2].URL != "https://github.com/org/repo/releases/v2" {
			t.Fatalf("parenthesized English prose mangled structured X link: %+v", gotLinks[2])
		}
		if gotLinks[3].Label == "and" || gotLinks[3].URL != "https://example.org/notes" {
			t.Fatalf("parenthesized English word 'and' must not be swallowed as link label: %+v", gotLinks[3])
		}
	})

	t.Run("historical cached X compatibility: fragment-stripped Links, raw t.co, display_url variants, bare domains", func(t *testing.T) {
		// 2a. item.Links stripped #sec-1 while item.Text kept #sec-1.
		parasFrag, _ := parseRichTextParagraphs(
			"详见 https://example.com/docs/spec#sec-1 结尾。",
			[]service.TextLinkDTO{{Label: "example.com/docs/spec…", URL: "https://example.com/docs/spec"}},
			false,
		)
		if len(parasFrag) != 1 || len(parasFrag[0].Spans) != 3 ||
			parasFrag[0].Spans[1].Label != "example.com/docs/spec…" ||
			parasFrag[0].Spans[1].URL != "https://example.com/docs/spec#sec-1" {
			t.Fatalf("cached fragment mismatch failed: %+v", parasFrag)
		}

		// 2b. Historical item.Text still has raw t.co while item.Links holds expanded URL.
		parasTCo, _ := parseRichTextParagraphs(
			"旧缓存帖子 https://t.co/LegacyShort1 说明",
			[]service.TextLinkDTO{{Label: "docs.example.dev/guide", URL: "https://docs.example.dev/guide"}},
			false,
		)
		if len(parasTCo) != 1 || len(parasTCo[0].Spans) != 3 ||
			parasTCo[0].Spans[1].Label != "docs.example.dev/guide" ||
			parasTCo[0].Spans[1].URL != "https://docs.example.dev/guide" {
			t.Fatalf("legacy t.co pairing failed: %+v", parasTCo)
		}

		// 2c. Historical display_url case / www. / ASCII "..." mismatch.
		parasDisp, _ := parseRichTextParagraphs(
			"查看 WWW.Example.com/very/long... 完毕",
			[]service.TextLinkDTO{{Label: "example.com/very/long…", URL: "https://example.com/very/long/full-path"}},
			false,
		)
		if len(parasDisp) != 1 || len(parasDisp[0].Spans) != 3 ||
			parasDisp[0].Spans[1].URL != "https://example.com/very/long/full-path" {
			t.Fatalf("display_url variant matching failed: %+v", parasDisp)
		}

		// 2d. Historical item with empty Links + bare domains (and non-link exclusions).
		parasBare, hasRich := parseRichTextParagraphs(
			"仓库 github.com/org/repo 和 www.example.org/docs 忽略 item.json v2.0.0 dev@example.com 以及截断 example.com/cut… 结束",
			nil,
			false,
		)
		if !hasRich {
			t.Fatal("expected bare domains to be parsed as rich links")
		}
		var bareLinks []richSpan
		for _, sp := range parasBare[0].Spans {
			if sp.Kind == richSpanLink {
				bareLinks = append(bareLinks, sp)
			}
		}
		if len(bareLinks) != 2 ||
			bareLinks[0].URL != "https://github.com/org/repo" ||
			bareLinks[1].URL != "https://www.example.org/docs" {
			t.Fatalf("unexpected bare domain links: %+v", bareLinks)
		}
	})

	// 3. End-to-end UI click tests across Home multiline cached X post, Online X Detail
	// (t.co DTO hydration, nested depth>0 quote, X Article header + repeated link),
	// Offline X Detail (card + nested quote), X User bio, X List description, and Mine › X.
	app, stub, cleanup := newXUITestHarness(t)
	defer cleanup()
	desk := stub.raw.Desktop().(*service.MockDesktop)

	// 3a. Multiline cached X post in Home feed: no raw URL in duplicate plain Title, clicking link jumps cleanly.
	xDir := filepath.Join(stub.raw.LibraryStore().CacheDir(), "x", "900004004", "posts", "1900000000000099501")
	savedX, err := stub.raw.LibraryStore().SaveItem(xDir, library.ItemMetadata{
		Version:   1,
		ID:        "dynamic:1900000000000099501",
		RawID:     "1900000000000099501",
		Site:      "x",
		Kind:      "dynamic",
		TypeLabel: "帖子",
		Status:    "complete",
		Title:     "多行X帖子标题 https://github.com/org/repo/releases/tag/v3.0.0#changelog",
		Text:      "多行X帖子标题\nhttps://github.com/org/repo/releases/tag/v3.0.0#changelog",
		Links: []library.ItemLink{
			{Label: "github.com/org/repo/rel…", URL: "https://github.com/org/repo/releases/tag/v3.0.0#changelog"},
		},
		Card: &library.ItemRichCard{
			Kind:  "webpage",
			Label: "网页链接",
			Title: "离线卡片标题",
			Desc:  "离线卡片摘要",
			URL:   "https://cards.example.dev/offline-article",
		},
		Origin: &library.ItemOrigin{
			Site:   "x",
			ID:     "1900000000000099500",
			Author: library.ItemAuthor{Site: "x", UID: "900005005", UniqueID: "mid_x", Name: "中层作者"},
			Text:   "中层引用 https://mid.example.dev/post",
			Links:  []library.ItemLink{{Label: "mid.example.dev/post", URL: "https://mid.example.dev/post"}},
			Origin: &library.ItemOrigin{
				Site:   "x",
				ID:     "1900000000000099499",
				Author: library.ItemAuthor{Site: "x", UID: "900006006", UniqueID: "inner_x", Name: "内层作者"},
				Text:   "内层引用 https://inner.example.dev/deep",
				Links:  []library.ItemLink{{Label: "inner.example.dev/deep", URL: "https://inner.example.dev/deep"}},
			},
		},
		URL:      "https://x.com/synth_writer/status/1900000000000099501",
		PubTs:    1710020000,
		CachedAt: "2026-03-05T10:00:00Z",
		Author:   library.ItemAuthor{Site: "x", UID: "900004004", UniqueID: "synth_writer", Name: "合成作者"},
	})
	if err != nil {
		t.Fatalf("SaveItem(x multiline): %v", err)
	}
	if _, err := stub.raw.CreateSubscription("合成作者", "", []subscription.AccountRef{
		{Site: "x", UID: "900004004", UniqueID: "synth_writer", Name: "合成作者"},
	}, subscription.SubRules{ScheduledCheck: true}); err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	app.CompleteLogin()
	app.Navigate(RouteHome)
	tt := ui.NewTester(app.View, 1280, 1200)
	tt.Frame()
	if tt.HasText("https://github.com/org/repo/releases/tag/v3.0.0#changelog") {
		t.Fatal("multiline cached X post in Home feed exposed raw URL via duplicate plain Title")
	}
	if err := tt.Click("打开链接：github.com/org/repo/rel…"); err != nil {
		t.Fatalf("Click Home X multiline link failed: %v", err)
	}
	if err := tt.Click("打开链接：inner.example.dev/deep"); err != nil {
		t.Fatalf("Click Home nested depth>0 X origin link failed: %v", err)
	}

	// 3b. Offline X detail: card button + nested depth>0 origin link clickable with 0 network calls.
	callsBefore := stub.xCalls.Load()
	app.OpenLibraryItemDetail(savedX)
	tt.Frame()
	if err := tt.Click("X卡片浏览器打开：webpage"); err != nil {
		t.Fatalf("Click offline X rich card open button failed: %v", err)
	}
	if err := tt.Click("打开链接：inner.example.dev/deep"); err != nil {
		t.Fatalf("Click offline X nested depth>0 origin link failed: %v", err)
	}
	if stub.xCalls.Load() != callsBefore {
		t.Fatalf("offline X detail issued %d unexpected network calls", stub.xCalls.Load()-callsBefore)
	}

	// 3c. Online X detail: unexpanded t.co in DTO + clamped 6-line expand + X Article heading & repeated links.
	app.CurrentLibraryDetail = nil
	app.CurrentXPost = &service.XPostDTO{
		ID:        "1900000000000099502",
		Author:    service.XAuthorDTO{RestID: "900004004", ScreenName: "synth_writer", Name: "合成作者"},
		CreatedAt: 1710021000,
		Text:      "含未展开短链 https://t.co/RawDto01 结束。",
		URLs: []service.XURLEntityDTO{{
			ShortURL:    "https://t.co/RawDto01",
			ExpandedURL: "https://expanded.example.dev/from-dto?a=1&copy=2",
			DisplayURL:  "expanded.example.dev/from-dto",
		}},
		Article: &service.XArticleDTO{
			Title: "文章标题测试",
			Blocks: []service.XArticleBlockDTO{
				{
					Type:  "header-one",
					Text:  "第一章 规范入口",
					Links: []service.TextLinkDTO{{Label: "规范入口", URL: "https://art.example.dev/h1"}},
				},
				{
					Type: "unstyled",
					Text: "参考 入口甲 以及 入口乙 结束。",
					Links: []service.TextLinkDTO{
						{Label: "入口甲", URL: "https://art.example.dev/shared"},
						{Label: "入口乙", URL: "https://art.example.dev/shared"},
					},
				},
			},
		},
		CanonicalURL: "https://x.com/synth_writer/status/1900000000000099502",
	}
	app.CurrentRoute = RouteXPost
	tt.Frame()
	if tt.HasText("https://t.co/RawDto01") {
		t.Fatal("online X detail leaked raw t.co when URLs entity had ExpandedURL")
	}
	if err := tt.Click("打开链接：expanded.example.dev/from-dto"); err != nil {
		t.Fatalf("Click hydrated DTO link failed: %v", err)
	}
	if err := tt.Click("打开链接：规范入口"); err != nil {
		t.Fatalf("Click X Article header-one link failed: %v", err)
	}
	if err := tt.Click("打开链接：入口乙"); err != nil {
		t.Fatalf("Click X Article second repeated-URL anchor failed: %v", err)
	}

	// 3d. X User bio & X List description clickable links, plus clamped 6-line X post expand & click.
	app.CurrentXUser = &service.XUserDTO{
		RestID:      "900004004",
		ScreenName:  "synth_writer",
		Name:        "合成作者",
		Description: "主页链接 https://bio.example.dev/me 欢迎访问",
	}
	app.CurrentXPosts = []service.XPostDTO{{
		ID:           "1900000000000099508",
		Author:       service.XAuthorDTO{RestID: "900004004", ScreenName: "synth_writer", Name: "合成作者"},
		CreatedAt:    1710021500,
		Text:         "第一行含顶链 https://top.example.dev/above\n第二行内容\n第三行内容\n第四行内容\n第五行内容\n第六行内容\n第七行折叠链接 https://bottom.example.dev/below",
		CanonicalURL: "https://x.com/synth_writer/status/1900000000000099508",
	}}
	app.CurrentRoute = RouteXUser
	tt.Frame()
	if err := tt.Click("打开链接：bio.example.dev/me"); err != nil {
		t.Fatalf("Click X user bio link failed: %v", err)
	}
	if err := tt.Click("打开链接：top.example.dev/above"); err != nil {
		t.Fatalf("Click visible link on clamped 6-line X post failed: %v", err)
	}
	if _, ok := tt.Find("打开链接：bottom.example.dev/below"); ok {
		t.Fatal("line-7 link should be hidden before expanding clamped X post")
	}
	if err := tt.Click("正文展开收起：x:1900000000000099508"); err != nil {
		t.Fatalf("Click expand on clamped X post failed: %v", err)
	}
	tt.Frame()
	if err := tt.Click("打开链接：bottom.example.dev/below"); err != nil {
		t.Fatalf("Click revealed link after expanding clamped X post failed: %v", err)
	}

	app.XList.List = &service.XListDTO{
		ID:           "18000000099",
		Name:         "合成列表",
		Description:  "列表说明 https://list.example.dev/about",
		CanonicalURL: "https://x.com/i/lists/18000000099",
	}
	app.XList.Tab = "posts"
	app.XList.Posts = nil
	app.CurrentRoute = RouteXList
	tt.Frame()
	if err := tt.Click("打开链接：list.example.dev/about"); err != nil {
		t.Fatalf("Click X list description link failed: %v", err)
	}

	// 3e. Mine › X bookmark row hyperlink click must NOT navigate away from RouteMine.
	stub.mu.Lock()
	stub.xStatus = credential.Status{Site: string(site.X), LoggedIn: true, State: "logged_in", UID: "900001001", UniqueID: "me_x", Uname: "我的X号"}
	stub.mineSections["x"] = []service.AccountSection{{Site: "x", Kind: service.AccountBookmarks, Label: "书签", Available: true}}
	stub.minePages["x/bookmarks|"] = service.AccountPage{
		Items: []service.AccountItem{{
			Kind:   service.AccountItemPost,
			Site:   "x",
			ID:     "1900000000000099509",
			Author: "书签作者",
			XPost: &service.XPostDTO{
				ID:        "1900000000000099509",
				Author:    service.XAuthorDTO{RestID: "900004004", ScreenName: "synth_writer", Name: "书签作者"},
				CreatedAt: 1710022000,
				Text:      "书签正文 https://t.co/MineX01",
				URLs: []service.XURLEntityDTO{{
					ShortURL:    "https://t.co/MineX01",
					ExpandedURL: "https://mine.example.dev/bookmark-target",
					DisplayURL:  "mine.example.dev/bookmark…",
				}},
				CanonicalURL: "https://x.com/synth_writer/status/1900000000000099509",
			},
		}},
	}
	stub.mu.Unlock()
	app.OpenMine("x", service.AccountBookmarks)
	tt.Frame()
	tt.Frame()
	if err := tt.Click("打开链接：mine.example.dev/bookmark…"); err != nil {
		t.Fatalf("Click Mine X bookmark link failed: %v", err)
	}
	if app.CurrentRoute != RouteMine {
		t.Fatalf("clicking hyperlink in Mine X row navigated away to %v", app.CurrentRoute)
	}

	// Verify MockDesktop received EVERY exact safe expanded target URL.
	actions := desk.Actions()
	for _, wantURL := range []string{
		"https://github.com/org/repo/releases/tag/v3.0.0#changelog",
		"https://inner.example.dev/deep",
		"https://cards.example.dev/offline-article",
		"https://expanded.example.dev/from-dto?a=1&copy=2",
		"https://art.example.dev/h1",
		"https://art.example.dev/shared",
		"https://bio.example.dev/me",
		"https://top.example.dev/above",
		"https://bottom.example.dev/below",
		"https://list.example.dev/about",
		"https://mine.example.dev/bookmark-target",
	} {
		if !slices.Contains(actions, service.RecordedAction{Kind: "url", Target: wantURL}) {
			t.Fatalf("expected MockDesktop url %q, got %+v", wantURL, actions)
		}
	}
}

func TestXHyperlinkUnicodeLowercaseAndSelfStatusFilterRegression(t *testing.T) {
	// P1a: indexInlineLinkPrefix mixed-case scheme fallback after variable-width Unicode 'İ' (U+0130).
	paras1, ok1 := parseRichTextParagraphs("İstanbul Http://Example.com/istanbul-ok 结束", nil, false)
	if !ok1 || len(paras1) != 1 {
		t.Fatalf("parseRichTextParagraphs(İstanbul mixed-case Http://) failed: ok=%v paras=%+v", ok1, paras1)
	}
	var links1 []richSpan
	for _, sp := range paras1[0].Spans {
		if sp.Kind == richSpanLink {
			links1 = append(links1, sp)
		}
	}
	if len(links1) != 1 || links1[0].URL != "http://Example.com/istanbul-ok" {
		t.Fatalf("mixed-case Http:// after İ parsed wrong: %+v", links1)
	}

	paras2, ok2 := parseRichTextParagraphs("İİİİİİİİİİİİİİİHttp://a.co", nil, false)
	if !ok2 || len(paras2) != 1 {
		t.Fatalf("parseRichTextParagraphs(repeated İ + Http://a.co) failed: ok=%v paras=%+v", ok2, paras2)
	}
	var links2 []richSpan
	for _, sp := range paras2[0].Spans {
		if sp.Kind == richSpanLink {
			links2 = append(links2, sp)
		}
	}
	if len(links2) != 1 || links2[0].URL != "http://a.co" {
		t.Fatalf("repeated İ + Http://a.co parsed wrong: %+v", links2)
	}

	// P1b: replaceShortURLVariantsUI / xPostTextAndLinksUI with 'İ' before t.co at end of string.
	postIst := &service.XPostDTO{
		ID:   "1900000000000099701",
		Text: "İstanbul https://t.co/ab",
		URLs: []service.XURLEntityDTO{{
			ShortURL:    "https://t.co/ab",
			ExpandedURL: "https://example.com/istanbul-ok",
			DisplayURL:  "example.com/istanbul-ok",
		}},
	}
	gotIstText, gotIstLinks := xPostTextAndLinksUI(postIst, nil, false)
	if gotIstText != "İstanbul https://example.com/istanbul-ok" {
		t.Fatalf("xPostTextAndLinksUI(İstanbul + t.co) text = %q", gotIstText)
	}
	if len(gotIstLinks) != 1 || gotIstLinks[0].URL != "https://example.com/istanbul-ok" {
		t.Fatalf("xPostTextAndLinksUI(İstanbul + t.co) links = %+v", gotIstLinks)
	}

	postRepIst := &service.XPostDTO{
		ID:   "1900000000000099702",
		Text: "İİİİİİİİİİİİİİİHTTP://T.CO/ab",
		URLs: []service.XURLEntityDTO{{
			ShortURL:    "https://t.co/ab",
			ExpandedURL: "https://example.com/istanbul-ok",
			DisplayURL:  "example.com/istanbul-ok",
		}},
	}
	gotRepText, _ := xPostTextAndLinksUI(postRepIst, nil, false)
	if gotRepText != "İİİİİİİİİİİİİİİhttps://example.com/istanbul-ok" {
		t.Fatalf("xPostTextAndLinksUI(repeated İ + HTTP://T.CO/ab) text = %q", gotRepText)
	}

	// P1c: case-insensitive display_url matching after 'İ' via asciiEqualFold.
	parasDisp, okDisp := parseRichTextParagraphs("İstanbul Example.COM/docs… 结束", []service.TextLinkDTO{
		{Label: "example.com/docs…", URL: "https://example.com/docs/full"},
	}, false)
	if !okDisp || len(parasDisp) != 1 {
		t.Fatalf("display_url after İ failed: ok=%v paras=%+v", okDisp, parasDisp)
	}
	var dispLinks []richSpan
	for _, sp := range parasDisp[0].Spans {
		if sp.Kind == richSpanLink {
			dispLinks = append(dispLinks, sp)
		}
	}
	if len(dispLinks) != 1 || dispLinks[0].URL != "https://example.com/docs/full" {
		t.Fatalf("display_url after İ parsed wrong: %+v", dispLinks)
	}

	// P2a: When p.Links is already populated (self-truncation link stripped by BuildPostLinks),
	// xPostLinksUI must NOT re-add the self-status link from p.URLs or mis-pair an unrelated t.co.
	postWithLinks := &service.XPostDTO{
		ID:   "1900000000000099777",
		Text: "正文 https://docs.example.dev/a 以及未知短链 https://t.co/Unmapped99",
		Links: []service.TextLinkDTO{
			{Label: "docs.example.dev/a", URL: "https://docs.example.dev/a"},
		},
		URLs: []service.XURLEntityDTO{
			{
				ShortURL:    "https://t.co/self777",
				ExpandedURL: "https://x.com/synth_writer/status/1900000000000099777",
				DisplayURL:  "x.com/i/web/status/1…",
			},
			{
				ShortURL:    "https://t.co/docsA",
				ExpandedURL: "https://docs.example.dev/a",
				DisplayURL:  "docs.example.dev/a",
			},
		},
	}
	gotTextA, gotLinksA := xPostTextAndLinksUI(postWithLinks, nil, false)
	if len(gotLinksA) != 1 || gotLinksA[0].URL != "https://docs.example.dev/a" {
		t.Fatalf("xPostLinksUI re-added self-status link when p.Links populated: %+v", gotLinksA)
	}
	parasA, okA := parseRichTextParagraphs(gotTextA, gotLinksA, false)
	if !okA || len(parasA) != 1 {
		t.Fatalf("parseRichTextParagraphs P2a failed: ok=%v paras=%+v", okA, parasA)
	}
	var spanURLsA []string
	for _, sp := range parasA[0].Spans {
		if sp.Kind == richSpanLink {
			spanURLsA = append(spanURLsA, sp.URL)
		}
	}
	wantURLsA := []string{"https://docs.example.dev/a", "https://t.co/Unmapped99"}
	if !slices.Equal(spanURLsA, wantURLsA) {
		t.Fatalf("P2a span URLs = %v, want %v (must not mis-pair unmapped t.co to self-status)", spanURLsA, wantURLsA)
	}

	// P2b: When p.Links is empty, xPostLinksUI falls back to p.URLs while STILL filtering
	// self-status links for p.ID (preserving other-tweet status links), and xPostTextAndLinksUI
	// strips unexpanded self-status t.co instead of expanding it.
	postOnlyURLs := &service.XPostDTO{
		ID:   "1900000000000099777",
		Text: "摘要 https://t.co/self777 另见 https://t.co/other888 和 https://t.co/Unmapped99",
		URLs: []service.XURLEntityDTO{
			{
				ShortURL:    "https://t.co/self777",
				ExpandedURL: "https://x.com/synth_writer/status/1900000000000099777/photo/1",
				DisplayURL:  "pic.x.com/self777",
			},
			{
				ShortURL:    "https://t.co/other888",
				ExpandedURL: "https://x.com/other_user/status/1900000000000099888",
				DisplayURL:  "x.com/other_user/sta…",
			},
		},
	}
	gotTextB, gotLinksB := xPostTextAndLinksUI(postOnlyURLs, nil, false)
	if strings.Contains(gotTextB, "self777") || strings.Contains(gotTextB, "1900000000000099777") {
		t.Fatalf("xPostTextAndLinksUI failed to strip self-status t.co: %q", gotTextB)
	}
	if !strings.Contains(gotTextB, "https://x.com/other_user/status/1900000000000099888") {
		t.Fatalf("xPostTextAndLinksUI failed to expand other-tweet status URL: %q", gotTextB)
	}
	if len(gotLinksB) != 1 || gotLinksB[0].URL != "https://x.com/other_user/status/1900000000000099888" {
		t.Fatalf("xPostLinksUI fallback from p.URLs did not filter self-status properly: %+v", gotLinksB)
	}
	parasB, okB := parseRichTextParagraphs(gotTextB, gotLinksB, false)
	if !okB || len(parasB) != 1 {
		t.Fatalf("parseRichTextParagraphs P2b failed: ok=%v paras=%+v", okB, parasB)
	}
	var spanURLsB []string
	for _, sp := range parasB[0].Spans {
		if sp.Kind == richSpanLink {
			spanURLsB = append(spanURLsB, sp.URL)
		}
	}
	wantURLsB := []string{"https://x.com/other_user/status/1900000000000099888", "https://t.co/Unmapped99"}
	if !slices.Equal(spanURLsB, wantURLsB) {
		t.Fatalf("P2b span URLs = %v, want %v", spanURLsB, wantURLsB)
	}
}
