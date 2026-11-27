package ui

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

func makeSynthArticlePNGBytes(w, h int, col color.RGBA) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, col)
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func assertRectsNoHorizontalOverflow(t *testing.T, tt *ui.Tester, width int, labels []string) {
	t.Helper()
	limit := float32(width) + 1.5
	for _, lbl := range labels {
		r, ok := tt.Find(lbl)
		if !ok {
			t.Fatalf("width %d: missing required label %q", width, lbl)
		}
		if r.X < -1.5 || r.X+r.W > limit {
			t.Fatalf("width %d: label %q overflows horizontally: rect=%+v", width, lbl, r)
		}
	}
}

func TestXUserArticlesFifthTabAndPublicListsModalLayout(t *testing.T) {
	app, stub, cleanup := newXUITestHarness(t)
	defer cleanup()

	if _, err := stub.LoginWithCookie(context.Background(), string(site.X), "auth_token=a; ct0=b", ""); err != nil {
		t.Fatal(err)
	}
	uid := "1000000000000000101"
	handle := "synth_creator"
	stub.xUsers[uid] = &service.XUserDTO{
		RestID:        uid,
		ScreenName:    handle,
		Name:          "合成创作者",
		StatusesCount: 42,
		CanonicalURL:  "https://x.com/" + handle,
	}
	articlePost := service.XPostDTO{
		ID:           "1900000000000008001",
		Author:       service.XAuthorDTO{RestID: uid, ScreenName: handle, Name: "合成创作者"},
		CreatedAt:    1710006000,
		Text:         "文章纯文本回退",
		CanonicalURL: "https://x.com/" + handle + "/status/1900000000000008001",
		CacheKey:     "x:dynamic:1900000000000008001",
		Article: &service.XArticleDTO{
			Title:       "X Articles 五标签合成长文",
			PreviewText: "文章预览摘要",
			PlainText:   "文章纯文本回退",
			Blocks: []service.XArticleBlockDTO{
				{Type: "header-one", Text: "章节一"},
				{Type: "unstyled", Text: "正文段落"},
			},
		},
	}
	stub.xUserTabPages[uid] = map[string]map[string]xPageFixture{
		"posts": {
			"": {page: service.XUserPostsPageDTO{Tab: "posts"}},
		},
		"articles": {
			"": {page: service.XUserPostsPageDTO{Tab: "articles", Posts: []service.XPostDTO{articlePost}}},
		},
	}
	stub.xUserLists[uid] = service.XUserListsPageDTO{
		Lists: []service.XListDTO{
			{ID: "18000000001", Name: "公开工程列表", Description: "只读列表简介", MemberCount: 15, CanonicalURL: "https://x.com/i/lists/18000000001"},
		},
	}

	app.CurrentXUser = stub.xUsers[uid]
	app.XUserTab = "posts"
	app.XUserTabs = nil
	app.CurrentRoute = RouteXUser

	// 1. Verify all 5 tabs render with zero horizontal overflow and zero tab overlap at 720, 1000, and 1280px.
	tabLabels := []string{
		"X用户标签：帖子",
		"X用户标签：回复",
		"X用户标签：媒体",
		"X用户标签：精选",
		"X用户标签：文章",
		"用户页筛选：全部",
		"用户页筛选：未缓存",
	}
	for _, w := range []int{720, 1000, 1280} {
		tt := ui.NewTester(app.View, w, 860)
		tt.Frame()
		assertRectsNoHorizontalOverflow(t, tt, w, tabLabels)
		var prevRight float32
		for _, lbl := range tabLabels[:5] {
			r, _ := tt.Find(lbl)
			if r.X < prevRight-0.5 {
				t.Fatalf("width %d: tab %q overlaps previous tab (x=%.1f < prevRight=%.1f)", w, lbl, r.X, prevRight)
			}
			prevRight = r.X + r.W
		}
	}

	// 2. Switching to 「文章」 lazily loads UserArticlesTweets and supports batch caching current page.
	app.SelectXUserTab("articles")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(app.CurrentXPosts) == 1 && app.CurrentXPosts[0].ID == articlePost.ID {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(app.CurrentXPosts) != 1 || app.CurrentXPosts[0].ID != articlePost.ID {
		t.Fatalf("articles tab did not load articlePost: %+v", app.CurrentXPosts)
	}

	app.CacheXCurrentTabUncached()
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stub.mu.Lock()
		n := len(stub.xBatchPageReqs)
		stub.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	stub.mu.Lock()
	if len(stub.xBatchPageReqs) != 1 || stub.xBatchPageReqs[0].Tab != "articles" {
		t.Fatalf("unexpected batch cache request for articles tab: %+v", stub.xBatchPageReqs)
	}
	stub.mu.Unlock()

	// 3. Opening the user's public lists modal loads and renders lists cleanly across widths.
	app.OpenXUserListsModal(uid, "合成创作者")
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(app.XUserLists.Lists) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	tt := ui.NewTester(app.View, 720, 860)
	tt.Frame()
	if _, ok := tt.Find("打开X公开列表：18000000001"); !ok {
		t.Fatal("user public lists modal did not render list row")
	}
}

func TestXArticleOnlineAndOfflineDetailRenderingZeroNetworkAndLegacyFallback(t *testing.T) {
	app, stub, cleanup := newXUITestHarness(t)
	defer cleanup()

	// 1. Online X Article detail renders title, cover, rich blocks, inline image, tweet citation, and 缓存文章 button.
	app.CurrentXPost = &service.XPostDTO{
		ID:           "1900000000000008101",
		Author:       service.XAuthorDTO{RestID: "1000000000000000101", ScreenName: "synth_writer", Name: "专栏作者"},
		CreatedAt:    1710006100,
		CanonicalURL: "https://x.com/synth_writer/status/1900000000000008101",
		CacheKey:     "x:dynamic:1900000000000008101",
		Article: &service.XArticleDTO{
			Title:     "在线 X 文章排版与缓存验证",
			PlainText: "在线文章纯文本",
			Cover:     &service.XArticleImageDTO{PublicPreviewURL: "https://pbs.twimg.com/media/SynthCover.jpg", Width: 1200, Height: 675},
			Blocks: []service.XArticleBlockDTO{
				{Type: "header-one", Text: "一、总览"},
				{Type: "header-two", Text: "1.1 小节"},
				{Type: "unstyled", Text: "阅读公开规范文档。", Links: []service.TextLinkDTO{{Label: "公开规范文档", URL: "https://example.com/spec"}}},
				{Type: "blockquote", Text: "引用块文案内容"},
				{Type: "ordered-list-item", Text: "有序项第一条"},
				{Type: "unordered-list-item", Text: "无序项第一条"},
				{Type: "atomic", Image: &service.XArticleImageDTO{PublicPreviewURL: "https://pbs.twimg.com/media/SynthInline1.jpg", Width: 800, Height: 600}},
				{Type: "atomic", TweetID: "1791257321273778888"},
			},
		},
	}
	app.CurrentRoute = RouteXPost

	for _, w := range []int{720, 1000, 1280} {
		tt := ui.NewTester(app.View, w, 1200)
		tt.Frame()
		assertRectsNoHorizontalOverflow(t, tt, w, []string{
			"X文章全文",
			"X文章标题",
			"查看文章封面",
			"查看文章图片 1",
			"X文章引用帖子：1791257321273778888",
			"缓存当前X帖子",
		})
		for _, txt := range []string{"在线 X 文章排版与缓存验证", "一、总览", "1.1 小节", "引用块文案内容", "有序项第一条", "无序项第一条"} {
			if !tt.HasText(txt) {
				t.Fatalf("width %d: online article missing text %q", w, txt)
			}
		}
	}

	// 2. Legacy degraded CardKindArticle (without full Article) keeps degraded card and hides cache button.
	app.CurrentXPost = &service.XPostDTO{
		ID:           "1900000000000008102",
		Author:       service.XAuthorDTO{RestID: "1000000000000000101", ScreenName: "synth_writer", Name: "专栏作者"},
		CreatedAt:    1710006200,
		Text:         "旧版降级文章摘要",
		CanonicalURL: "https://x.com/synth_writer/status/1900000000000008102",
		Card:         &service.XRichCardDTO{Kind: "article", Label: "X Articles", Title: "旧版文章标题", Desc: "仅摘要", URL: "https://x.com/i/article/1", Unsupported: true},
	}
	ttLegacy := ui.NewTester(app.View, 1000, 900)
	ttLegacy.Frame()
	if _, ok := ttLegacy.Find("缓存当前X帖子"); ok {
		t.Fatal("legacy degraded CardKindArticle must not show cache button")
	}
	if _, ok := ttLegacy.Find("X卡片：article"); !ok {
		t.Fatal("legacy degraded CardKindArticle card missing")
	}

	// 3. Offline /library/item X Article renders strictly offline (0 xCalls) and opens ImageViewer on click.
	postDir := filepath.Join(stub.raw.LibraryStore().CacheDir(), "x", "1000000000000000101", "posts", "1900000000000008103")
	if err := os.MkdirAll(filepath.Join(postDir, "images"), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(postDir, "cover.png"), makeSynthArticlePNGBytes(320, 180, color.RGBA{R: 40, G: 110, B: 200, A: 255}), 0o600)
	_ = os.WriteFile(filepath.Join(postDir, "images", "01.png"), makeSynthArticlePNGBytes(240, 160, color.RGBA{R: 80, G: 160, B: 110, A: 255}), 0o600)

	savedArt, err := stub.raw.LibraryStore().SaveItem(postDir, library.ItemMetadata{
		Version:     1,
		ID:          "1900000000000008103",
		Site:        "x",
		Kind:        "dynamic",
		DynamicType: "article",
		TypeLabel:   "文章",
		Status:      "complete",
		Title:       "离线 X 文章全文测试",
		Text:        "离线纯文本回退",
		PubTs:       1710006300,
		Author:      library.ItemAuthor{Site: "x", UID: "1000000000000000101", UniqueID: "synth_writer", Name: "专栏作者"},
		Media: []library.MediaFile{
			{Role: "cover", Path: "cover.png", Width: 320, Height: 180},
			{Role: "image", Path: "images/01.png", Width: 240, Height: 160},
		},
		Article: &library.ItemArticle{
			Title:     "离线 X 文章全文测试",
			CoverRel:  "cover.png",
			PlainText: "离线纯文本回退",
			Blocks: []library.ItemArticleBlock{
				{Type: "header-one", Text: "离线标题"},
				{Type: "unstyled", Text: "离线段落", Links: []library.ItemLink{{Label: "外链", URL: "https://example.com/offline"}}},
				{Type: "atomic", MediaRel: "images/01.png"},
				{Type: "atomic", TweetID: "1791257321273778888"},
			},
		},
	})
	if err != nil {
		t.Fatalf("SaveItem offline article: %v", err)
	}

	callsBefore := stub.xCalls.Load()
	app.OpenLibraryItemDetail(savedArt)
	ttOff := ui.NewTester(app.View, 1000, 1200)
	ttOff.Frame()
	if stub.xCalls.Load() != callsBefore {
		t.Fatalf("offline article detail issued %d X network calls (want 0)", stub.xCalls.Load()-callsBefore)
	}
	assertRectsNoHorizontalOverflow(t, ttOff, 1000, []string{
		"X离线文章全文",
		"X文章标题",
		"查看文章封面",
		"查看文章图片 1",
		"X文章引用帖子：1791257321273778888",
	})
	if _, ok := ttOff.Find("缓存当前X帖子"); ok {
		t.Fatal("offline article detail must not show online cache button")
	}
	if _, ok := ttOff.Find("打开文章引用帖子：1791257321273778888"); ok {
		t.Fatal("offline article detail must not show online open-tweet button")
	}
	// Clicking inline image opens ImageViewer with strictly local relPaths (empty remote URLs).
	ttOff.Click("查看文章图片 1")
	ttOff.Frame()
	if !app.ImageViewer.Open || len(app.ImageViewer.RelPaths) != 2 || app.ImageViewer.Index != 1 {
		t.Fatalf("clicking inline article image did not open ImageViewer cleanly: %+v", app.ImageViewer)
	}
	for _, u := range app.ImageViewer.URLs {
		if strings.TrimSpace(u) != "" {
			t.Fatalf("offline ImageViewer carried remote URL %q", u)
		}
	}
}

func TestXListRouteTabsMemberSubscribeBatchImportAndResponsiveLayout(t *testing.T) {
	app, stub, cleanup := newXUITestHarness(t)
	defer cleanup()

	if _, err := stub.LoginWithCookie(context.Background(), string(site.X), "auth_token=a; ct0=b", ""); err != nil {
		t.Fatal(err)
	}
	// Pre-subscribe one member so the list shows both "已订阅" and "订阅".
	sub, err := stub.raw.CreateSubscription("已订阅列表成员", "", []subscription.AccountRef{
		{Site: string(site.X), UID: "1000000000000000201", UniqueID: "member_one", Name: "成员一"},
	}, subscription.DefaultSubRules())
	if err != nil {
		t.Fatal(err)
	}
	app.RefreshData()

	listID := "18000000001"
	stub.xLists[listID] = &service.XListDTO{
		ID:              listID,
		Name:            "Go 开源架构观察",
		Description:     "只读合成公开列表，支持帖子浏览与成员批量导入。",
		MemberCount:     2,
		SubscriberCount: 18,
		Creator:         service.XAuthorDTO{RestID: "1000000000000000101", ScreenName: "synth_curator", Name: "列表维护者"},
		CanonicalURL:    "https://x.com/i/lists/" + listID,
	}
	stub.xListTweets[listID] = service.XListTweetsPageDTO{
		ListID: listID,
		Posts: []service.XPostDTO{
			{
				ID:           "1900000000000009001",
				Author:       service.XAuthorDTO{RestID: "1000000000000000201", ScreenName: "member_one", Name: "成员一"},
				CreatedAt:    1710007000,
				Text:         "公开列表帖子正文测试",
				CanonicalURL: "https://x.com/member_one/status/1900000000000009001",
				CacheKey:     "x:dynamic:1900000000000009001",
			},
		},
	}
	stub.xListMembers[listID] = service.XListMembersPageDTO{
		ListID: listID,
		Members: []service.XListMemberDTO{
			{RestID: "1000000000000000201", ScreenName: "member_one", Name: "成员一", Description: "已订阅成员简介", Subscribed: true, OwnerSubID: sub.ID, OwnerSubName: sub.Name},
			{RestID: "1000000000000000202", ScreenName: "member_two", Name: "成员二", Description: "未订阅成员简介"},
		},
	}

	// 1. Open public list link via Parse.
	app.OpenXList(listID, "Go 开源架构观察")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if app.CurrentRoute == RouteXList && app.XList.List != nil && len(app.XList.Posts) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if app.CurrentRoute != RouteXList || app.XList.List == nil {
		t.Fatalf("OpenXList did not navigate to RouteXList: route=%s list=%+v", app.CurrentRoute, app.XList.List)
	}

	// 2. Verify Posts tab across 720, 1000, and 1280px (zero horizontal overflow).
	for _, w := range []int{720, 1000, 1280} {
		tt := ui.NewTester(app.View, w, 900)
		tt.Frame()
		assertRectsNoHorizontalOverflow(t, tt, w, []string{
			"X列表头部",
			"X列表名称",
			"X列表浏览器打开",
			"X列表更多",
			"X列表标签：帖子",
			"X列表标签：成员",
			"X列表帖子流",
		})
	}

	// Batch cache current page on Posts tab uses Tab="list".
	app.CacheXListCurrentPageUncached()
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stub.mu.Lock()
		n := len(stub.xBatchPageReqs)
		stub.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	stub.mu.Lock()
	if len(stub.xBatchPageReqs) != 1 || stub.xBatchPageReqs[0].Tab != "list" {
		t.Fatalf("unexpected batch cache request for list tab: %+v", stub.xBatchPageReqs)
	}
	stub.mu.Unlock()

	// 3. Switch to Members tab -> lazy loads members once, renders subscribed vs unsubscribed, zero overflow at 720/1000/1280px.
	app.SelectXListTab("members")
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(app.XList.Members) == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(app.XList.Members) != 2 {
		t.Fatalf("list members did not load: %+v", app.XList.Members)
	}
	for _, w := range []int{720, 1000, 1280} {
		tt := ui.NewTester(app.View, w, 900)
		tt.Frame()
		assertRectsNoHorizontalOverflow(t, tt, w, []string{
			"X列表成员操作栏",
			"批量导入X列表未订阅成员",
			"X列表成员：1000000000000000201",
			"X列表成员已订阅：1000000000000000201",
			"X列表成员：1000000000000000202",
			"订阅X列表成员：1000000000000000202",
		})
	}

	// 4. Clicking individual "订阅" opens SubscribePicker pre-filled with member_two.
	ttMem := ui.NewTester(app.View, 1000, 900)
	ttMem.Frame()
	ttMem.Click("订阅X列表成员：1000000000000000202")
	ttMem.Frame()
	if !app.SubscribePicker.Open || app.SubscribePicker.Account.UID != "1000000000000000202" {
		t.Fatalf("clicking member Subscribe did not open SubscribePicker: %+v", app.SubscribePicker)
	}
	app.SubscribePicker.Open = false

	// 5. Clicking "批量导入未订阅成员" imports member_two with DEC-080 default preset (完整存档)!
	stub.importDelegateRaw = true
	app.ImportUnsubscribedXListMembers()
	stub.mu.Lock()
	impSite := stub.importedSite
	impCnt := stub.importedCnt
	impRules := stub.importedRules
	stub.mu.Unlock()
	if impSite != string(site.X) || impCnt != 1 {
		t.Fatalf("ImportFollowingCreators called with site=%q cnt=%d, want x/1", impSite, impCnt)
	}
	if !impRules.ScheduledCheck || !impRules.FullCache || !impRules.AutoCache.Enabled {
		t.Fatalf("imported rules do not match DEC-080 PresetFullArchive: %+v", impRules)
	}
	subs, err := stub.raw.ListSubscriptions()
	if err != nil {
		t.Fatal(err)
	}
	var foundNew *subscription.Subscription
	for i := range subs {
		for _, acc := range subs[i].Accounts {
			if acc.Site == string(site.X) && acc.UID == "1000000000000000202" {
				foundNew = &subs[i]
				break
			}
		}
	}
	if foundNew == nil {
		t.Fatalf("batch import did not create subscription for unsubscribed list member; subs=%+v", subs)
	}
	presetID, _ := subscription.MatchTrackingPreset(foundNew.Tags)
	if presetID != subscription.PresetFullArchive {
		t.Fatalf("imported list member preset = %q, want %q (DEC-080)", presetID, subscription.PresetFullArchive)
	}
	// Verify UI state flipped member_two to subscribed as well.
	if !app.XList.Members[1].Subscribed {
		t.Fatal("expected member_two UI state to be marked Subscribed after batch import")
	}
	// Deleting a subscription immediately updates xListMemberOwner even if stale DTO still had Subscribed=true.
	if err := stub.raw.DeleteSubscription(sub.ID); err != nil {
		t.Fatalf("DeleteSubscription: %v", err)
	}
	app.RefreshData()
	if _, owned := app.xListMemberOwner(app.XList.Members[0]); owned {
		t.Fatal("expected deleted subscription member to no longer report owned despite stale DTO Subscribed=true")
	}
}
