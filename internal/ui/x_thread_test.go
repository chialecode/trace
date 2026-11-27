package ui

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

func countTextOccurrences(texts []string, needle string) int {
	n := 0
	for _, s := range texts {
		if strings.Contains(s, needle) {
			n++
		}
	}
	return n
}

func assertDesktopNoHorizontalOverflow(t *testing.T, tt *ui.Tester, width int, label string) {
	t.Helper()
	for _, info := range tt.AccessAll() {
		if info.Bounds.W <= 0 || info.Bounds.H <= 0 || info.Offscreen {
			continue
		}
		if info.Bounds.X < -1 || info.Bounds.X+info.Bounds.W > float32(width)+1 {
			t.Fatalf("%s horizontal overflow at width=%d on %q (%s): rect=%+v", label, width, info.Name, info.Value, info.Bounds)
		}
	}
}

func TestResolveCachedXThreadPartsCompatAndMediaMapping(t *testing.T) {
	now := time.Now().Unix()

	// 1. Real cached form (x_planner.go): [ancestor, focal(ID==RawID), continuation, media-only reply],
	// 1-based MediaIndexes referencing NotePages[].Index, plus FromOrigin filtering and unclaimed fallback.
	realItem := library.ItemMetadata{
		ItemKey: "x:dynamic:1900000000000010002",
		Site:    "x",
		Kind:    "dynamic",
		ID:      "1900000000000010002",
		RawID:   "1900000000000010002",
		Text:    "上文第1条\n\n主帖第2条\n\n续帖第3条",
		PubTs:   now - 200,
		Author:  library.ItemAuthor{Site: "x", UID: "900010001", UniqueID: "thread_author", Name: "串作者"},
		Origin: &library.ItemOrigin{
			Site:   "x",
			ID:     "1900000000000099999",
			Author: library.ItemAuthor{Site: "x", UID: "900010099", UniqueID: "quoted_user", Name: "被引作者"},
			Text:   "被引用的原帖正文",
		},
		NotePages: []library.NotePageRecord{
			{Index: 1, Kind: "image", Image: "images/01.jpg", Width: 800, Height: 600},
			{Index: 2, Kind: "image", Image: "images/02.jpg", Width: 800, Height: 600},
			{Index: 3, Kind: "image", FromOrigin: true, Image: "images/03_orig.jpg", Width: 800, Height: 600},
			{Index: 4, Kind: "gif", GIF: true, Image: "images/04.jpg", Video: "images/04.mp4", Width: 640, Height: 360},
			{Index: 5, Kind: "image", Image: "images/05_unclaimed.jpg", Width: 800, Height: 600},
		},
		Thread: []library.ItemThreadPart{
			{ID: "1900000000000010001", PubTs: now - 300, Text: "上文第1条", MediaIndexes: []int{1}},
			// x_planner.go may include FromOrigin page index (3) inside focal MediaIndexes; must be filtered out!
			{ID: "1900000000000010002", PubTs: now - 200, Text: "主帖第2条", MediaIndexes: []int{2, 3}},
			{ID: "1900000000000010003", PubTs: now - 100, Text: "续帖第3条"},
			// Media-only self-reply (empty Text) must still be preserved with its media.
			{ID: "1900000000000010004", PubTs: now - 50, Text: "", MediaIndexes: []int{4}},
		},
	}

	parts, focalIdx, ok := resolveCachedXThreadParts(realItem, nil, nil, "", "")
	if !ok || len(parts) != 4 || focalIdx != 1 {
		t.Fatalf("resolveCachedXThreadParts(real) = (len=%d, focal=%d, ok=%v), want (4, 1, true)", len(parts), focalIdx, ok)
	}
	if parts[0].Text != "上文第1条" || len(parts[0].NotePages) != 1 || parts[0].NotePages[0].ImageRel != "images/01.jpg" {
		t.Fatalf("ancestor part mismatch: %+v", parts[0])
	}
	// Focal gets page 2 + unclaimed page 5, and MUST NOT receive FromOrigin page 3!
	if !parts[1].IsFocal || parts[1].Text != "主帖第2条" || len(parts[1].NotePages) != 2 {
		t.Fatalf("focal part mismatch: %+v", parts[1])
	}
	if parts[1].NotePages[0].ImageRel != "images/02.jpg" || parts[1].NotePages[1].ImageRel != "images/05_unclaimed.jpg" {
		t.Fatalf("focal pages = %+v, want [02.jpg, 05_unclaimed.jpg] without FromOrigin 03_orig.jpg", parts[1].NotePages)
	}
	if parts[2].Text != "续帖第3条" || len(parts[2].NotePages) != 0 {
		t.Fatalf("text-only reply mismatch: %+v", parts[2])
	}
	if parts[3].Text != "" || len(parts[3].NotePages) != 1 || parts[3].NotePages[0].VideoRel != "images/04.mp4" || !parts[3].NotePages[0].IsGIF || parts[3].NotePages[0].Kind != "video" {
		t.Fatalf("media-only GIF reply mismatch: %+v", parts[3])
	}

	// 2. Single-element real thread matching RawID must NOT be treated as a multi-tweet thread.
	soloItem := library.ItemMetadata{
		Site:   "x",
		RawID:  "1900000000000010010",
		Text:   "单独一条推文",
		Thread: []library.ItemThreadPart{{ID: "1900000000000010010", Text: "单独一条推文"}},
	}
	if _, _, ok := resolveCachedXThreadParts(soloItem, nil, nil, "", ""); ok {
		t.Fatal("single-node item.Thread matching RawID must return ok=false")
	}

	// 3. Synthetic / historical form where item.Thread omits focal RawID, with concatenated item.Text.
	synthItem := library.ItemMetadata{
		Site:   "x",
		RawID:  "1900000000000010020",
		PubTs:  now - 500,
		Text:   "首帖原始正文\n\n历史合成续帖正文",
		Author: library.ItemAuthor{Site: "x", UID: "900010001", UniqueID: "thread_author", Name: "串作者"},
		NotePages: []library.NotePageRecord{
			{Index: 1, Kind: "image", Image: "images/focal.jpg"},
		},
		Thread: []library.ItemThreadPart{
			{ID: "1900000000000010021", PubTs: now - 400, Text: "历史合成续帖正文"},
		},
	}
	synthParts, synthFocal, synthOK := resolveCachedXThreadParts(synthItem, nil, nil, "", "")
	if !synthOK || len(synthParts) != 2 || synthFocal != 0 {
		t.Fatalf("resolveCachedXThreadParts(synth) = (len=%d, focal=%d, ok=%v), want (2, 0, true)", len(synthParts), synthFocal, synthOK)
	}
	if synthParts[0].Text != "首帖原始正文" || len(synthParts[0].NotePages) != 1 || synthParts[0].NotePages[0].ImageRel != "images/focal.jpg" {
		t.Fatalf("synthetic focal part mismatch: %+v", synthParts[0])
	}
	if synthParts[1].Text != "历史合成续帖正文" || len(synthParts[1].NotePages) != 0 {
		t.Fatalf("synthetic continuation mismatch: %+v", synthParts[1])
	}

	// 4. Single-video focal (NotePages empty) + text-only continuation attaches video to focal.
	vidCard := &PostVideo{BVID: "1900000000000010030", Title: "X 视频", ItemKey: "x:dynamic:1900000000000010030", Cached: true}
	vidThreadItem := library.ItemMetadata{
		Site:   "x",
		RawID:  "1900000000000010030",
		PubTs:  now - 500,
		Text:   "主帖带单视频\n\n纯文字补充说明",
		Author: library.ItemAuthor{Site: "x", UID: "900010001", UniqueID: "thread_author", Name: "串作者"},
		Media: []library.MediaFile{
			{Role: "cover", Path: "cover.jpg"},
			{Role: "video", Path: "video.mp4", Duration: 38},
		},
		Thread: []library.ItemThreadPart{
			{ID: "1900000000000010030", PubTs: now - 500, Text: "主帖带单视频"},
			{ID: "1900000000000010031", PubTs: now - 400, Text: "纯文字补充说明"},
		},
	}
	vidParts, vidFocal, vidOK := resolveCachedXThreadParts(vidThreadItem, vidCard, nil, "", "")
	if !vidOK || len(vidParts) != 2 || vidFocal != 0 {
		t.Fatalf("resolveCachedXThreadParts(singleVideo) = (len=%d, focal=%d, ok=%v), want (2, 0, true)", len(vidParts), vidFocal, vidOK)
	}
	if vidParts[0].Video != vidCard || vidParts[1].Video != nil {
		t.Fatalf("single video should belong exclusively to focal part: p0=%v p1=%v", vidParts[0].Video, vidParts[1].Video)
	}
}

func TestXThreadDisplayHomePostCardCollapseExpandAndWidths(t *testing.T) {
	app, stub, cleanup := newXUITestHarness(t)
	defer cleanup()

	desk := stub.raw.Desktop().(*service.MockDesktop)
	stub.mu.Lock()
	stub.xStatus = credential.Status{Site: string(site.X), LoggedIn: true, State: "logged_in", UID: "900001001", UniqueID: "me_x", Uname: "当前X号"}
	stub.mu.Unlock()

	if _, err := stub.raw.CreateSubscription("科技观察订阅", "", []subscription.AccountRef{
		{Site: "x", UID: "900020001", UniqueID: "tech_thread_x", Name: "科技观察_X"},
	}, subscription.SubRules{ScheduledCheck: true}); err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}

	now := time.Now().Unix()
	cacheDir := stub.raw.LibraryStore().CacheDir()

	// Seed 1: Real cached 3-tweet thread with 1-based MediaIndexes, link, and quote origin.
	t3Dir := filepath.Join(cacheDir, "x", "900020001_tech_thread_x", "forwards", "1900000000000020001")
	saved3, err := stub.raw.LibraryStore().SaveItem(t3Dir, library.ItemMetadata{
		Version:   1,
		ID:        "1900000000000020001",
		RawID:     "1900000000000020001",
		Site:      "x",
		Kind:      "dynamic",
		TypeLabel: "引用帖子",
		Status:    "complete",
		Title:     "三段自回复首帖正文",
		Text:      "三段自回复首帖正文，详见 发布说明\n\n三段自回复第二条正文\n\n三段自回复第三条收尾",
		Links:     []library.ItemLink{{Label: "发布说明", URL: "https://example.dev/thread-release"}},
		URL:       "https://x.com/tech_thread_x/status/1900000000000020001",
		PubTs:     now - 100,
		CachedAt:  time.Now().UTC().Format(time.RFC3339),
		Author:    library.ItemAuthor{Site: "x", UID: "900020001", UniqueID: "tech_thread_x", Name: "科技观察_X"},
		Origin: &library.ItemOrigin{
			Site:   "x",
			ID:     "1900000000000020099",
			Author: library.ItemAuthor{Site: "x", UID: "900020099", UniqueID: "quoted_author", Name: "被引作者"},
			Text:   "引用原帖独立文案",
		},
		NotePages: []library.NotePageRecord{
			{Index: 1, Kind: "image", Image: "images/01.jpg", Width: 800, Height: 450},
			{Index: 2, Kind: "image", FromOrigin: true, Image: "images/02_orig.jpg", Width: 800, Height: 450},
			{Index: 3, Kind: "image", Image: "images/03.jpg", Width: 800, Height: 450},
		},
		Thread: []library.ItemThreadPart{
			{
				ID:           "1900000000000020001",
				PubTs:        now - 100,
				Text:         "三段自回复首帖正文，详见 发布说明",
				Links:        []library.ItemLink{{Label: "发布说明", URL: "https://example.dev/thread-release"}},
				MediaIndexes: []int{1, 2},
			},
			{
				ID:           "1900000000000020002",
				PubTs:        now - 80,
				Text:         "三段自回复第二条正文",
				MediaIndexes: []int{3},
			},
			{
				ID:    "1900000000000020003",
				PubTs: now - 60,
				Text:  "三段自回复第三条收尾",
			},
		},
	})
	if err != nil {
		t.Fatalf("SaveItem(3-part thread): %v", err)
	}

	// Seed 2: Single-video focal + text-only continuation thread.
	vidDir := filepath.Join(cacheDir, "x", "900020001_tech_thread_x", "posts", "1900000000000020010")
	savedVid, err := stub.raw.LibraryStore().SaveItem(vidDir, library.ItemMetadata{
		Version:  1,
		ID:       "1900000000000020010",
		RawID:    "1900000000000020010",
		Site:     "x",
		Kind:     "dynamic",
		Status:   "complete",
		Title:    "单视频串首帖",
		Text:     "单视频串首帖\n\n视频后的文字补充说明",
		URL:      "https://x.com/tech_thread_x/status/1900000000000020010",
		PubTs:    now - 200,
		Duration: 42,
		CachedAt: time.Now().UTC().Format(time.RFC3339),
		Author:   library.ItemAuthor{Site: "x", UID: "900020001", UniqueID: "tech_thread_x", Name: "科技观察_X"},
		Media: []library.MediaFile{
			{Role: "cover", Path: "cover.jpg", Width: 640, Height: 360},
			{Role: "video", Path: "video.mp4", Duration: 42},
		},
		Thread: []library.ItemThreadPart{
			{ID: "1900000000000020010", PubTs: now - 200, Text: "单视频串首帖"},
			{ID: "1900000000000020011", PubTs: now - 180, Text: "视频后的文字补充说明"},
		},
	})
	if err != nil {
		t.Fatalf("SaveItem(video thread): %v", err)
	}

	// Seed 3: 6-tweet long thread (tests default collapse to first 3 + inline expand/collapse).
	t6Dir := filepath.Join(cacheDir, "x", "900020001_tech_thread_x", "posts", "1900000000000020020")
	var t6Parts []library.ItemThreadPart
	var t6Texts []string
	for i := 1; i <= 6; i++ {
		segTxt := fmt.Sprintf("超长串第%d段独特正文", i)
		t6Texts = append(t6Texts, segTxt)
		t6Parts = append(t6Parts, library.ItemThreadPart{
			ID:    fmt.Sprintf("190000000000002002%d", i-1),
			PubTs: now - int64(400-i*10),
			Text:  segTxt,
		})
	}
	saved6, err := stub.raw.LibraryStore().SaveItem(t6Dir, library.ItemMetadata{
		Version:  1,
		ID:       "1900000000000020020",
		RawID:    "1900000000000020020",
		Site:     "x",
		Kind:     "dynamic",
		Status:   "complete",
		Title:    "超长串第1段独特正文",
		Text:     strings.Join(t6Texts, "\n\n"),
		URL:      "https://x.com/tech_thread_x/status/1900000000000020020",
		PubTs:    now - 300,
		CachedAt: time.Now().UTC().Format(time.RFC3339),
		Author:   library.ItemAuthor{Site: "x", UID: "900020001", UniqueID: "tech_thread_x", Name: "科技观察_X"},
		Thread:   t6Parts,
	})
	if err != nil {
		t.Fatalf("SaveItem(6-part thread): %v", err)
	}

	// Render 3-part thread PostCard directly in Home route context.
	app.CompleteLogin()
	app.Navigate(RouteHome)
	app.FeedSiteFilter = "x"
	app.FeedItems = []library.FeedEntry{
		{
			FeedKey:      saved3.ItemKey,
			ItemKey:      saved3.ItemKey,
			Site:         "x",
			Kind:         "dynamic",
			RawID:        saved3.RawID,
			AuthorUID:    saved3.Author.UID,
			AuthorName:   saved3.Author.Name,
			Title:        saved3.Title,
			Summary:      saved3.Text,
			CanonicalURL: saved3.URL,
			PubTs:        saved3.PubTs,
			Cached:       true,
			CacheStatus:  "green",
		},
	}
	app.postCards = nil

	tt := ui.NewTester(app.View, 1280, 1200)
	tt.Frame()

	texts := tt.Texts()
	for _, seg := range []string{"三段自回复首帖正文", "三段自回复第二条正文", "三段自回复第三条收尾", "引用原帖独立文案"} {
		if got := countTextOccurrences(texts, seg); got != 1 {
			t.Fatalf("expected %q rendered exactly once on Home PostCard, got %d (texts=%v)", seg, got, texts)
		}
	}
	if tt.HasText("含 3 条作者自回复串") || tt.HasText("含 2 条作者自回复串") {
		t.Fatal("Home PostCard must NOT render legacy collapsed thread hint for rendered threads")
	}
	if _, ok := tt.Find("X串连线：" + saved3.ItemKey + ":1"); !ok {
		t.Fatal("missing vertical connector line under tweet 1")
	}
	if _, ok := tt.Find("X串连线：" + saved3.ItemKey + ":2"); !ok {
		t.Fatal("missing vertical connector line under tweet 2")
	}
	if _, ok := tt.Find("X串连线：" + saved3.ItemKey + ":3"); ok {
		t.Fatal("must NOT draw connector line under final tweet 3")
	}
	if _, ok := tt.Find("主页订阅名：" + saved3.ItemKey); !ok {
		t.Fatal("first thread row must retain subscription name on Home")
	}
	if _, ok := tt.Find("主页作者站点：" + saved3.ItemKey); !ok {
		t.Fatal("first thread row must retain X site badge on Home")
	}
	if _, ok := tt.Find("转发原内容：" + saved3.ItemKey); !ok {
		t.Fatal("focal thread row must retain Origin quote box")
	}
	// docs/ui.md §3.1: Home PostCard must NOT show extra text action buttons.
	for _, forbidden := range []string{"内置播放", "查看图片", "在浏览器中打开", "复制链接"} {
		if tt.HasText(forbidden) {
			t.Fatalf("Home PostCard must not show text action button %q", forbidden)
		}
	}
	// Clickable hyperlink inside thread part works.
	if err := tt.Click("打开链接：发布说明"); err != nil {
		t.Fatalf("Click thread hyperlink failed: %v", err)
	}
	if !slices.Contains(desk.Actions(), service.RecordedAction{Kind: "url", Target: "https://example.dev/thread-release"}) {
		t.Fatalf("expected thread link click recorded, got %+v", desk.Actions())
	}

	// Verify 720 / 1000 / 1280 three-width zero horizontal overflow on Home 3-part thread.
	for _, w := range []int{720, 1000, 1280} {
		tt.SetSize(w, 960)
		tt.Frame()
		assertDesktopNoHorizontalOverflow(t, tt, w, "home-x-3part-thread")
	}

	// Verify single-video focal + text-only continuation on Home PostCard.
	app.FeedItems = []library.FeedEntry{{
		FeedKey:      savedVid.ItemKey,
		ItemKey:      savedVid.ItemKey,
		Site:         "x",
		Kind:         "dynamic",
		RawID:        savedVid.RawID,
		AuthorUID:    savedVid.Author.UID,
		AuthorName:   savedVid.Author.Name,
		Title:        savedVid.Title,
		Summary:      savedVid.Text,
		CanonicalURL: savedVid.URL,
		PubTs:        savedVid.PubTs,
		Cached:       true,
		CacheStatus:  "green",
	}}
	app.postCards = nil
	tt.SetSize(1280, 960)
	tt.Frame()
	if _, ok := tt.Find("播放视频卡片：" + savedVid.ItemKey); !ok {
		t.Fatal("single-video focal thread missing video card on focal node")
	}
	if !tt.HasText("单视频串首帖") || !tt.HasText("视频后的文字补充说明") {
		t.Fatal("single-video thread missing focal or continuation text")
	}
	if _, ok := tt.Find("X串连线：" + savedVid.ItemKey + ":1"); !ok {
		t.Fatal("single-video thread missing connector line under focal")
	}
	if _, ok := tt.Find("X串连线：" + savedVid.ItemKey + ":2"); ok {
		t.Fatal("single-video thread must not draw line under final reply")
	}

	// Verify 6-tweet long thread defaults to first 3, expands on click, collapses on click, zero overflow at 720/1000/1280.
	app.FeedItems = []library.FeedEntry{{
		FeedKey:      saved6.ItemKey,
		ItemKey:      saved6.ItemKey,
		Site:         "x",
		Kind:         "dynamic",
		RawID:        saved6.RawID,
		AuthorUID:    saved6.Author.UID,
		AuthorName:   saved6.Author.Name,
		Title:        saved6.Title,
		Summary:      saved6.Text,
		CanonicalURL: saved6.URL,
		PubTs:        saved6.PubTs,
		Cached:       true,
		CacheStatus:  "green",
	}}
	app.postCards = nil
	tt.SetSize(1280, 1200)
	tt.Frame()

	for _, vis := range []string{"超长串第1段独特正文", "超长串第2段独特正文", "超长串第3段独特正文", "展开其余 3 条自回复"} {
		if !tt.HasText(vis) {
			t.Fatalf("collapsed 6-part thread missing %q", vis)
		}
	}
	for _, hidden := range []string{"超长串第4段独特正文", "超长串第5段独特正文", "超长串第6段独特正文"} {
		if tt.HasText(hidden) {
			t.Fatalf("collapsed 6-part thread should hide %q initially", hidden)
		}
	}
	// Expand inline!
	if err := tt.Click("展开自回复串：" + saved6.ItemKey); err != nil {
		t.Fatalf("Click expand 6-part thread failed: %v", err)
	}
	tt.Frame()
	for i := 1; i <= 6; i++ {
		want := fmt.Sprintf("超长串第%d段独特正文", i)
		if !tt.HasText(want) {
			t.Fatalf("expanded 6-part thread missing %q", want)
		}
	}
	if _, ok := tt.Find("X串连线：" + saved6.ItemKey + ":5"); !ok {
		t.Fatal("expanded 6-part thread missing connector line on node 5")
	}
	if _, ok := tt.Find("X串连线：" + saved6.ItemKey + ":6"); ok {
		t.Fatal("expanded 6-part thread must NOT draw connector line on final node 6")
	}
	if !tt.HasText("收起自回复串") {
		t.Fatal("expanded 6-part thread missing 收起自回复串 toggle")
	}
	for _, w := range []int{720, 1000, 1280} {
		tt.SetSize(w, 1200)
		tt.Frame()
		assertDesktopNoHorizontalOverflow(t, tt, w, "home-x-6part-expanded")
	}
	// Collapse back!
	if err := tt.Click("收起自回复串：" + saved6.ItemKey); err != nil {
		t.Fatalf("Click collapse 6-part thread failed: %v", err)
	}
	tt.Frame()
	if tt.HasText("超长串第4段独特正文") || !tt.HasText("展开其余 3 条自回复") {
		t.Fatal("collapsing 6-part thread did not hide tail tweets")
	}
}

func TestXThreadDisplayOnlineAndOfflineDetailsAndZeroNetwork(t *testing.T) {
	app, stub, cleanup := newXUITestHarness(t)
	defer cleanup()

	now := time.Now().Unix()

	// 1. Online X Post Detail (/x/post/:id) with ThreadAncestors -> Focal -> Thread continuations.
	app.CurrentLibraryDetail = nil
	app.CurrentXPost = &service.XPostDTO{
		ID:           "1900000000000030002",
		Author:       service.XAuthorDTO{RestID: "900030001", ScreenName: "online_threader", Name: "在线串作者"},
		CreatedAt:    now - 200,
		Text:         "在线主帖核心段落正文",
		CanonicalURL: "https://x.com/online_threader/status/1900000000000030002",
		ThreadAncestors: []service.XPostDTO{{
			ID:           "1900000000000030001",
			Author:       service.XAuthorDTO{RestID: "900030001", ScreenName: "online_threader", Name: "在线串作者"},
			CreatedAt:    now - 300,
			Text:         "在线上文第1条引导段落",
			CanonicalURL: "https://x.com/online_threader/status/1900000000000030001",
		}},
		Thread: []service.XPostDTO{
			{
				ID:           "1900000000000030003",
				Author:       service.XAuthorDTO{RestID: "900030001", ScreenName: "online_threader", Name: "在线串作者"},
				CreatedAt:    now - 100,
				Text:         "在线续帖第3条正文",
				CanonicalURL: "https://x.com/online_threader/status/1900000000000030003",
			},
			{
				ID:           "1900000000000030004",
				Author:       service.XAuthorDTO{RestID: "900030001", ScreenName: "online_threader", Name: "在线串作者"},
				CreatedAt:    now - 50,
				Text:         "在线续帖第4条收尾",
				CanonicalURL: "https://x.com/online_threader/status/1900000000000030004",
			},
		},
		Stats: service.XStatsDTO{ReplyCount: 10, RetweetCount: 20, LikeCount: 300},
	}
	app.CurrentRoute = RouteXPost

	tt := ui.NewTester(app.View, 1280, 1200)
	tt.Frame()

	for _, wantLabel := range []string{
		"X作者自回复串",
		"X串上文：1900000000000030001",
		"X串连线：1900000000000030001",
		"X串主帖",
		"X串连线：1900000000000030002",
		"X串条目：1900000000000030003",
		"X串连线：1900000000000030003",
		"X串条目：1900000000000030004",
		"查看X串帖子：1900000000000030001",
		"查看X串帖子：1900000000000030003",
		"查看X串帖子：1900000000000030004",
		"缓存当前X帖子",
		"X在线统计",
	} {
		if _, ok := tt.Find(wantLabel); !ok {
			t.Fatalf("online thread detail missing label %q", wantLabel)
		}
	}
	if _, ok := tt.Find("X串连线：1900000000000030004"); ok {
		t.Fatal("online thread detail must NOT draw connector line under final tweet")
	}
	for _, w := range []int{720, 1000, 1280} {
		tt.SetSize(w, 1200)
		tt.Frame()
		assertDesktopNoHorizontalOverflow(t, tt, w, "x-post-online-thread")
	}

	// 2. X User Page (/x/user) via xPostToPostCard renders vertical connected thread.
	userCard := app.xPostToPostCard(*app.CurrentXPost, nil)
	if len(userCard.ThreadParts) != 4 {
		t.Fatalf("xPostToPostCard expected 4 ThreadParts (1 anc + 1 focal + 2 cont), got %d", len(userCard.ThreadParts))
	}

	// 3. Offline X Detail (/library/item) with real cached concatenated item.Text + 1-based MediaIndexes:
	// MUST NOT duplicate text, MUST NOT use 0-based item.Media index, MUST make ZERO network calls!
	callsBefore := stub.xCalls.Load()
	offlineReal := library.ItemMetadata{
		ItemKey: "x:dynamic:1900000000000030101",
		Site:    "x",
		Kind:    "dynamic",
		ID:      "1900000000000030101",
		RawID:   "1900000000000030101",
		Title:   "离线实机形态首帖",
		Text:    "离线实机形态首帖\n\n离线实机形态第二条",
		PubTs:   now - 500,
		Author:  library.ItemAuthor{Site: "x", UID: "900030001", UniqueID: "online_threader", Name: "在线串作者"},
		NotePages: []library.NotePageRecord{
			{Index: 1, Kind: "image", Image: "images/01.jpg", Width: 800, Height: 600},
			{Index: 2, Kind: "video", GIF: true, Image: "images/02.jpg", Video: "images/02.mp4", Width: 640, Height: 360},
		},
		Thread: []library.ItemThreadPart{
			{ID: "1900000000000030101", PubTs: now - 500, Text: "离线实机形态首帖", MediaIndexes: []int{1}},
			{ID: "1900000000000030102", PubTs: now - 450, Text: "离线实机形态第二条", MediaIndexes: []int{2}},
		},
	}
	app.OpenLibraryItemDetail(offlineReal)
	tt.SetSize(1280, 1000)
	tt.Frame()

	offTexts := tt.Texts()
	if got := countTextOccurrences(offTexts, "离线实机形态首帖"); got != 1 {
		t.Fatalf("offline detail rendered focal text %d times (want 1); texts=%v", got, offTexts)
	}
	if got := countTextOccurrences(offTexts, "离线实机形态第二条"); got != 1 {
		t.Fatalf("offline detail rendered continuation text %d times (want 1); texts=%v", got, offTexts)
	}
	if _, ok := tt.Find("X离线串连线：1"); !ok {
		t.Fatal("offline detail missing connector line under node 1")
	}
	if _, ok := tt.Find("X离线串连线：2"); ok {
		t.Fatal("offline detail must NOT draw connector line under final node 2")
	}
	if _, ok := tt.Find("X在线统计"); ok {
		t.Fatal("offline detail must NOT render online stats")
	}
	if _, ok := tt.Find("缓存当前X帖子"); ok {
		t.Fatal("offline detail must NOT render online cache button")
	}
	if stub.xCalls.Load() != callsBefore {
		t.Fatalf("offline X thread detail triggered %d network calls (want 0)", stub.xCalls.Load()-callsBefore)
	}
	for _, w := range []int{720, 1000, 1280} {
		tt.SetSize(w, 1000)
		tt.Frame()
		assertDesktopNoHorizontalOverflow(t, tt, w, "x-post-offline-thread")
	}
}

func TestXReplyContextQuoteLabelThreadPartOriginAndNestedOriginDesktop(t *testing.T) {
	app, stub, cleanup := newXUITestHarness(t)
	defer cleanup()

	now := time.Now().Unix()
	cacheDir := stub.raw.LibraryStore().CacheDir()

	// 1. Seed a legacy cached X quote post (old TypeLabel == "转发") + reply context without handle
	// to verify display upgrade to "引用帖子" and "回复一条帖子" fallback on Home PostCard.
	legacyDir := filepath.Join(cacheDir, "x", "900040001_legacy_q", "forwards", "1900000000000040001")
	savedLegacy, err := stub.raw.LibraryStore().SaveItem(legacyDir, library.ItemMetadata{
		Version:     1,
		ID:          "1900000000000040001",
		RawID:       "1900000000000040001",
		Site:        "x",
		Kind:        "dynamic",
		DynamicType: "forward",
		TypeLabel:   "转发",
		Status:      "complete",
		Title:       "历史引用帖正文",
		Text:        "历史引用帖正文",
		URL:         "https://x.com/legacy_q/status/1900000000000040001",
		PubTs:       now - 100,
		CachedAt:    time.Now().UTC().Format(time.RFC3339),
		Author:      library.ItemAuthor{Site: "x", UID: "900040001", UniqueID: "legacy_q", Name: "旧版引用作者"},
		InReplyTo:   &library.ItemReplyTarget{StatusID: "1900000000000039999"},
		Origin: &library.ItemOrigin{
			Site:   "x",
			ID:     "1900000000000040099",
			Author: library.ItemAuthor{Site: "x", UID: "900040099", UniqueID: "orig_author", Name: "原帖作者"},
			Text:   "历史被引原帖正文",
		},
	})
	if err != nil {
		t.Fatalf("SaveItem(legacy quote): %v", err)
	}

	// 2. Seed a rich DEC-091 X quote thread with @handle reply context, 2-level nested Origin.Origin,
	// and non-focal thread part embedded Origin.
	richDir := filepath.Join(cacheDir, "x", "900040001_legacy_q", "forwards", "1900000000000040010")
	savedRich, err := stub.raw.LibraryStore().SaveItem(richDir, library.ItemMetadata{
		Version:     1,
		ID:          "1900000000000040010",
		RawID:       "1900000000000040010",
		Site:        "x",
		Kind:        "dynamic",
		DynamicType: "forward",
		TypeLabel:   "引用帖子",
		Status:      "complete",
		Title:       "桌面富引用串首帖",
		Text:        "桌面富引用串首帖\n\n桌面富引用串续帖",
		URL:         "https://x.com/legacy_q/status/1900000000000040010",
		PubTs:       now - 50,
		CachedAt:    time.Now().UTC().Format(time.RFC3339),
		Author:      library.ItemAuthor{Site: "x", UID: "900040001", UniqueID: "legacy_q", Name: "旧版引用作者"},
		InReplyTo:   &library.ItemReplyTarget{StatusID: "1900000000000040000", ScreenName: "external_target"},
		Origin: &library.ItemOrigin{
			Site:      "x",
			ID:        "1900000000000040100",
			Author:    library.ItemAuthor{Site: "x", UID: "900040100", UniqueID: "outer_desk", Name: "桌面外层作者"},
			Text:      "桌面外层引用正文",
			InReplyTo: &library.ItemReplyTarget{StatusID: "1900000000000040090", ScreenName: "outer_replied"},
			Origin: &library.ItemOrigin{
				Site:   "x",
				ID:     "1900000000000040101",
				Author: library.ItemAuthor{Site: "x", UID: "900040101", UniqueID: "inner_desk", Name: "桌面内层作者"},
				Text:   "桌面内层嵌套正文",
			},
		},
		Thread: []library.ItemThreadPart{
			{ID: "1900000000000040010", PubTs: now - 50, Text: "桌面富引用串首帖"},
			{
				ID:    "1900000000000040011",
				PubTs: now - 30,
				Text:  "桌面富引用串续帖",
				Origin: &library.ItemOrigin{
					Site:   "x",
					ID:     "1900000000000040102",
					Author: library.ItemAuthor{Site: "x", UID: "900040102", UniqueID: "part_desk", Name: "桌面续帖引用作者"},
					Text:   "桌面续帖内嵌引用正文",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("SaveItem(rich quote thread): %v", err)
	}

	app.CurrentRoute = RouteHome
	app.FeedItems = []library.FeedEntry{
		{
			FeedKey: savedRich.ItemKey, ItemKey: savedRich.ItemKey, Site: "x", Kind: "dynamic",
			RawID: savedRich.RawID, AuthorUID: savedRich.Author.UID, AuthorName: savedRich.Author.Name,
			Title: savedRich.Title, Summary: savedRich.Text, CanonicalURL: savedRich.URL, PubTs: savedRich.PubTs,
			Cached: true, CacheStatus: "green",
		},
		{
			FeedKey: savedLegacy.ItemKey, ItemKey: savedLegacy.ItemKey, Site: "x", Kind: "dynamic",
			RawID: savedLegacy.RawID, AuthorUID: savedLegacy.Author.UID, AuthorName: savedLegacy.Author.Name,
			Title: savedLegacy.Title, Summary: savedLegacy.Text, CanonicalURL: savedLegacy.URL, PubTs: savedLegacy.PubTs,
			Cached: true, CacheStatus: "green",
		},
	}
	app.postCards = nil

	tt := ui.NewTester(app.View, 1280, 1400)
	tt.Frame()

	for _, want := range []string{
		"引用帖子",
		"回复 @external_target",
		"回复 @outer_replied",
		"桌面外层引用正文",
		"桌面内层嵌套正文",
		"桌面续帖内嵌引用正文",
		"回复一条帖子",
		"历史引用帖正文",
	} {
		if !tt.HasText(want) {
			t.Fatalf("Home PostCard missing %q; texts=%v", want, tt.Texts())
		}
	}
	for _, w := range []int{720, 1000, 1280} {
		tt.SetSize(w, 1400)
		tt.Frame()
		assertDesktopNoHorizontalOverflow(t, tt, w, "home-x-dec091")
	}

	// 3. Online X Post Detail: internal self-reply InReplyTo suppressed, depth-3 nested quote truncated,
	// and non-focal thread part embedded quote rendered.
	app.CurrentLibraryDetail = nil
	app.CurrentXPost = &service.XPostDTO{
		ID:                  "1900000000000040202",
		Author:              service.XAuthorDTO{RestID: "900040200", ScreenName: "online_op", Name: "在线引用作者"},
		CreatedAt:           now - 200,
		Text:                "在线串主帖正文",
		CanonicalURL:        "https://x.com/online_op/status/1900000000000040202",
		InReplyToStatusID:   "1900000000000040201", // internal self-reply to ancestor -> suppressed
		InReplyToScreenName: "online_op",
		IsQuote:             true,
		Quoted: &service.XPostDTO{
			ID:     "1900000000000040250",
			Author: service.XAuthorDTO{RestID: "900040250", ScreenName: "on_outer", Name: "在线外层作者"},
			Text:   "在线外层被引正文",
			Quoted: &service.XPostDTO{
				ID:     "1900000000000040251",
				Author: service.XAuthorDTO{RestID: "900040251", ScreenName: "on_inner", Name: "在线内层作者"},
				Text:   "在线内层嵌套正文",
				Quoted: &service.XPostDTO{
					ID:     "1900000000000040252",
					Author: service.XAuthorDTO{RestID: "900040252", ScreenName: "on_deep", Name: "在线超深层"},
					Text:   "在线第三层不应渲染",
				},
			},
		},
		ThreadAncestors: []service.XPostDTO{{
			ID:                  "1900000000000040201",
			Author:              service.XAuthorDTO{RestID: "900040200", ScreenName: "online_op", Name: "在线引用作者"},
			CreatedAt:           now - 300,
			Text:                "在线串首帖回复外部",
			InReplyToStatusID:   "1900000000000040199",
			InReplyToScreenName: "on_ext_parent",
		}},
		Thread: []service.XPostDTO{{
			ID:        "1900000000000040203",
			Author:    service.XAuthorDTO{RestID: "900040200", ScreenName: "online_op", Name: "在线引用作者"},
			CreatedAt: now - 100,
			Text:      "在线串续帖内嵌引用",
			IsQuote:   true,
			Quoted: &service.XPostDTO{
				ID:     "1900000000000040260",
				Author: service.XAuthorDTO{RestID: "900040260", ScreenName: "on_part_q", Name: "在线续帖引用作者"},
				Text:   "在线续帖被引用正文",
			},
		}},
	}
	app.CurrentRoute = RouteXPost
	tt.SetSize(1280, 1400)
	tt.Frame()

	for _, want := range []string{
		"回复 @on_ext_parent",
		"在线外层被引正文",
		"在线内层嵌套正文",
		"在线续帖被引用正文",
	} {
		if !tt.HasText(want) {
			t.Fatalf("online X detail missing %q; texts=%v", want, tt.Texts())
		}
	}
	if tt.HasText("回复 @online_op") {
		t.Fatalf("online X detail must suppress internal self-reply InReplyTo; texts=%v", tt.Texts())
	}
	if tt.HasText("在线第三层不应渲染") {
		t.Fatalf("online X detail must cap nested quotes at depth 1; texts=%v", tt.Texts())
	}
	for _, w := range []int{720, 1000, 1280} {
		tt.SetSize(w, 1400)
		tt.Frame()
		assertDesktopNoHorizontalOverflow(t, tt, w, "x-post-online-dec091")
	}

	// 4. Offline X Detail: renders all DEC-091 fields with ZERO network calls and zero overflow across 720/1000/1280.
	callsBefore := stub.xCalls.Load()
	app.OpenLibraryItemDetail(savedRich)
	tt.SetSize(1280, 1400)
	tt.Frame()

	for _, want := range []string{
		"引用帖子",
		"回复 @external_target",
		"回复 @outer_replied",
		"桌面外层引用正文",
		"桌面内层嵌套正文",
		"桌面续帖内嵌引用正文",
	} {
		if !tt.HasText(want) {
			t.Fatalf("offline X detail missing %q; texts=%v", want, tt.Texts())
		}
	}
	if stub.xCalls.Load() != callsBefore {
		t.Fatalf("offline X DEC-091 detail triggered %d network calls (want 0)", stub.xCalls.Load()-callsBefore)
	}
	for _, w := range []int{720, 1000, 1280} {
		tt.SetSize(w, 1400)
		tt.Frame()
		assertDesktopNoHorizontalOverflow(t, tt, w, "x-post-offline-dec091")
	}

	// 5. Offline X Detail regression: legacy pure retweet ("转发" + "RT @...") MUST NOT be mislabeled as "引用帖子",
	// and duplicate Thread[1].Origin.ID == item.Origin.ID must render only once.
	legacyPureRTDir := filepath.Join(cacheDir, "x", "900040001_legacy_q", "forwards", "1900000000000040020")
	savedLegacyRT, err := stub.raw.LibraryStore().SaveItem(legacyPureRTDir, library.ItemMetadata{
		Version:     1,
		ID:          "1900000000000040020",
		RawID:       "1900000000000040020",
		Site:        "x",
		Kind:        "dynamic",
		DynamicType: "forward",
		TypeLabel:   "转发",
		Status:      "complete",
		Title:       "RT @orig_author: 旧版纯转帖正文",
		Text:        "RT @orig_author: 旧版纯转帖正文",
		URL:         "https://x.com/legacy_q/status/1900000000000040020",
		PubTs:       now - 20,
		CachedAt:    time.Now().UTC().Format(time.RFC3339),
		Author:      library.ItemAuthor{Site: "x", UID: "900040001", UniqueID: "legacy_q", Name: "旧版引用作者"},
		Origin: &library.ItemOrigin{
			Site:   "x",
			ID:     "1900000000000040299",
			Author: library.ItemAuthor{Site: "x", UID: "900040299", UniqueID: "orig_author", Name: "原帖作者"},
			Text:   "旧版纯转帖原帖内容",
		},
	})
	if err != nil {
		t.Fatalf("SaveItem(legacy pure RT): %v", err)
	}
	app.OpenLibraryItemDetail(savedLegacyRT)
	tt.SetSize(1280, 1000)
	tt.Frame()
	if tt.HasText("引用帖子") {
		t.Fatalf("offline X detail mislabeled legacy pure RT as 引用帖子; texts=%v", tt.Texts())
	}
	if !tt.HasText("转发") || !tt.HasText("旧版纯转帖原帖内容") {
		t.Fatalf("offline X detail missing legacy pure RT label/origin; texts=%v", tt.Texts())
	}

	dupOriginItem := savedRich
	dupOriginItem.Thread = append([]library.ItemThreadPart(nil), savedRich.Thread...)
	dupOriginItem.Thread[1].Origin = savedRich.Origin
	app.OpenLibraryItemDetail(dupOriginItem)
	tt.SetSize(1280, 1400)
	tt.Frame()
	if count := countTextOccurrences(tt.Texts(), "桌面外层引用正文"); count != 1 {
		t.Fatalf("offline X detail rendered duplicated thread-part Origin %d times (want 1)", count)
	}
}
