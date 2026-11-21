package ui

import (
	"context"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

type weiboRetweetStub struct {
	service.BackendAPI
	raw *service.Backend

	mu          sync.Mutex
	wbStatus    credential.Status
	avatars     map[string]string
	cachedCalls []struct {
		mid string
		via *library.ItemVia
	}
	openedLinks   []string
	stagedPreview []struct{ site, url, ext string }
	previewFile   string
	wbPosts       map[string]*service.WeiboPostView
	mineSections  map[string][]service.AccountSection
	minePages     map[string]service.AccountPage
}

func newWeiboRetweetHarness(t *testing.T) (*App, *weiboRetweetStub, func()) {
	t.Helper()
	app, _, cleanup := newTestUIApp(t)
	raw := app.backend.(*service.Backend)
	if _, err := raw.ImportCookie(context.Background(), "SESSDATA=synth_bili; bili_jct=csrf; DedeUserID=12345", ""); err != nil {
		cleanup()
		t.Fatal(err)
	}
	stub := &weiboRetweetStub{
		BackendAPI: raw,
		raw:        raw,
		wbStatus: credential.Status{
			Site:     string(site.Weibo),
			LoggedIn: true,
			State:    "logged_in",
			Source:   "manual",
			UID:      "500000100",
			Uname:    "微博测试号",
		},
		avatars:      map[string]string{},
		wbPosts:      map[string]*service.WeiboPostView{},
		mineSections: map[string][]service.AccountSection{},
		minePages:    map[string]service.AccountPage{},
	}
	app.backend = stub
	return app, stub, cleanup
}

func (s *weiboRetweetStub) StatusOf(siteID string) credential.Status {
	if normalizeSiteIDUI(siteID) == "weibo" {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.wbStatus
	}
	return s.BackendAPI.StatusOf(siteID)
}

func (s *weiboRetweetStub) Statuses() map[string]credential.Status {
	s.mu.Lock()
	wb := s.wbStatus
	s.mu.Unlock()
	return map[string]credential.Status{
		string(site.Bilibili): s.BackendAPI.StatusOf(string(site.Bilibili)),
		string(site.Douyin):   s.BackendAPI.StatusOf(string(site.Douyin)),
		string(site.Weibo):    wb,
	}
}

func (s *weiboRetweetStub) HasAnyLogin() bool { return true }

func (s *weiboRetweetStub) AccountAvatarPath(siteID, uid string) string {
	key := normalizeSiteIDUI(siteID) + ":" + strings.TrimSpace(uid)
	s.mu.Lock()
	p := s.avatars[key]
	s.mu.Unlock()
	if p != "" {
		return p
	}
	return s.BackendAPI.AccountAvatarPath(siteID, uid)
}

func (s *weiboRetweetStub) FetchImage(_ context.Context, _ string) ([]byte, error) {
	return nil, context.Canceled
}

func (s *weiboRetweetStub) CacheWeiboPost(_ context.Context, mid string, via *library.ItemVia) (download.Task, error) {
	var viaCopy *library.ItemVia
	if via != nil {
		cp := *via
		viaCopy = &cp
	}
	s.mu.Lock()
	s.cachedCalls = append(s.cachedCalls, struct {
		mid string
		via *library.ItemVia
	}{mid: mid, via: viaCopy})
	s.mu.Unlock()
	return download.Task{TaskID: "wb-rt-" + mid, Site: "weibo", Kind: "dynamic", TargetID: mid, Status: "queued"}, nil
}

func (s *weiboRetweetStub) StagePreviewMedia(_ context.Context, siteID, rawURL, ext string) (string, error) {
	s.mu.Lock()
	s.stagedPreview = append(s.stagedPreview, struct{ site, url, ext string }{site: siteID, url: rawURL, ext: ext})
	pf := s.previewFile
	s.mu.Unlock()
	if pf != "" {
		return pf, nil
	}
	return "", &site.Error{Site: site.Weibo, Kind: site.ErrKindUnavailable, Message: "合成视频预览桩不可解码"}
}

func (s *weiboRetweetStub) OpenLink(_ context.Context, raw string) (*service.OpenLinkResult, error) {
	s.mu.Lock()
	s.openedLinks = append(s.openedLinks, raw)
	var matched *service.WeiboPostView
	for mid, p := range s.wbPosts {
		if strings.Contains(raw, mid) {
			cp := *p
			matched = &cp
			break
		}
	}
	s.mu.Unlock()
	if matched != nil {
		return &service.OpenLinkResult{
			Site:       string(site.Weibo),
			SiteTarget: &site.ResolveTarget{Site: site.Weibo, Kind: "dynamic", ID: matched.Mid, CanonicalURL: matched.CanonicalURL},
			WeiboPost:  matched,
		}, nil
	}
	return &service.OpenLinkResult{
		Site:       string(site.Weibo),
		SiteTarget: &site.ResolveTarget{Site: site.Weibo, Kind: "dynamic", ID: raw, CanonicalURL: raw},
		WeiboPost:  &service.WeiboPostView{Mid: raw, Text: "打开的原微博"},
	}, nil
}

func (s *weiboRetweetStub) AccountSections(siteID string) []service.AccountSection {
	s.mu.Lock()
	defer s.mu.Unlock()
	if secs, ok := s.mineSections[siteID]; ok {
		return append([]service.AccountSection(nil), secs...)
	}
	return s.BackendAPI.AccountSections(siteID)
}

func (s *weiboRetweetStub) AccountFolders(_ context.Context, _, _ string) ([]service.AccountFolder, error) {
	return nil, nil
}

func (s *weiboRetweetStub) AccountPage(_ context.Context, siteID, kind, _, _ string) (service.AccountPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pg, ok := s.minePages[siteID+"/"+kind]; ok {
		return pg, nil
	}
	return service.AccountPage{}, nil
}

func TestMultilineTextLineHeightReadabilityWithoutClipping(t *testing.T) {
	if LineHeightBody != 1.6 || LineHeightCompact != 1.45 || LineHeightHeading != 1.35 {
		t.Fatalf("unexpected line height constants: body=%v compact=%v heading=%v", LineHeightBody, LineHeightCompact, LineHeightHeading)
	}

	app, _, cleanup := newWeiboRetweetHarness(t)
	defer cleanup()
	app.CompleteLogin()

	// Compare a 1-line post vs a 4-line post at 1280x860: 15px * 1.6 = 24px/line => 4 lines >= 88px.
	app.CurrentRoute = RouteWeiboPost
	app.CurrentWeiboPost = &service.WeiboPostView{
		Mid:    "5090000001",
		Author: service.WeiboAuthorDTO{UID: "500001", ScreenName: "排印测试博主"},
		Text:   "第一行正文测试内容",
	}
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	singleRect, ok := tt.Find("微博正文")
	if !ok {
		t.Fatal("missing 微博正文 for 1-line post")
	}
	btnRect, ok := tt.Find("缓存当前微博")
	if !ok {
		t.Fatal("missing 缓存当前微博 button")
	}
	if btnRect.H > 42 {
		t.Fatalf("single-line pill button height unexpectedly inflated: %.1f", btnRect.H)
	}

	app.CurrentWeiboPost = &service.WeiboPostView{
		Mid:    "5090000002",
		Author: service.WeiboAuthorDTO{UID: "500001", ScreenName: "排印测试博主"},
		Text:   "第一行多段落中文测试内容。\n第二行检验行高与留白呼吸感。\n第三行确认不会出现上下裁切。\n第四行验证完整展示无溢出。",
	}
	tt.Frame()
	multiRect, ok := tt.Find("微博正文")
	if !ok {
		t.Fatal("missing 微博正文 for 4-line post")
	}
	if multiRect.H < 88 {
		t.Fatalf("expected 4-line body height >= 88px with LineHeightBody=1.6 (24px/line), got %.1f (1-line=%.1f)", multiRect.H, singleRect.H)
	}
	if multiRect.X+multiRect.W > 1280.5 {
		t.Fatalf("multiline body overflowed horizontally: rect=%+v", multiRect)
	}
}

func TestWeiboRetweetDetailOnlineOfflineOuterBindingAndOpenOrigin(t *testing.T) {
	app, stub, cleanup := newWeiboRetweetHarness(t)
	defer cleanup()
	app.CompleteLogin()

	posted := make(chan func(), 64)
	app.dispatch = func(fn func()) { posted <- fn }

	const (
		fwdUID   = "500000001"
		origUID  = "500000099"
		fwdMid   = "5000000000008001"
		origMid  = "5000000000007001"
		videoURL = "https://f.video.weibocdn.com/origin_preview.mp4"
	)

	origPost := &service.WeiboPostView{
		Mid:          origMid,
		Author:       service.WeiboAuthorDTO{UID: origUID, ScreenName: "原博主甲"},
		CreatedAt:    1710004000,
		Text:         "原微博完整正文：第一段说明拍摄过程。\n第二段补充镜头与光圈参数。",
		CanonicalURL: "https://weibo.com/" + origUID + "/" + origMid,
		CacheKey:     library.CanonicalItemKey("weibo", "dynamic", origMid),
		Images: []service.WeiboMediaItemDTO{
			{Index: 1, Kind: "image", FromOrigin: true, ImageURL: "https://wx1.sinaimg.cn/large/orig_01.jpg", Width: 800, Height: 600},
			{Index: 2, Kind: "live_photo", FromOrigin: true, ImageURL: "https://wx1.sinaimg.cn/large/orig_02.jpg", MotionURL: "https://video.weibo.com/media/play?livephoto=orig02", Width: 800, Height: 800},
		},
		Video: &service.WeiboVideoDTO{
			ObjectID:     "1034:8001",
			Title:        "原微博高清航拍视频",
			DurationSec:  95,
			VideoURL:     videoURL,
			QualityLabel: "1080P",
			Codec:        "hevc",
		},
	}
	fwdPost := &service.WeiboPostView{
		Mid:          fwdMid,
		Author:       service.WeiboAuthorDTO{UID: fwdUID, ScreenName: "转发订阅博主"},
		CreatedAt:    1710004800,
		Text:         "转发收藏！原博主的航拍和图文都很完整。",
		IsRetweet:    true,
		Retweeted:    origPost,
		Card:         &service.WeiboRichCardDTO{Kind: "article", Label: "头条文章", Title: "外层附带卡片", URL: "https://weibo.com/ttarticle/p/show?id=1"},
		CanonicalURL: "https://weibo.com/" + fwdUID + "/" + fwdMid,
		CacheKey:     library.CanonicalItemKey("weibo", "dynamic", fwdMid),
	}
	stub.mu.Lock()
	stub.wbPosts[fwdMid] = fwdPost
	stub.wbPosts[origMid] = origPost
	stub.mu.Unlock()

	// 1. OpenWeiboPostFor sets weiboCacheFor; online retweet detail shows 缓存 button despite outer article card,
	//    renders origin text/images/LivePhoto/video and 打开原微博, and caches with ViaForward under forUID.
	app.OpenWeiboPostFor(fwdMid, fwdPost.CanonicalURL, fwdPost.Text, fwdUID, "转发订阅博主")
	waitUIIdle(t, app, posted)

	tt := ui.NewTester(app.View, 1280, 1400)
	tt.Frame()

	for _, lbl := range []string{"缓存当前微博", "转发原微博卡片", "转发原微博正文", "打开原微博", "查看图文页 1", "实况标记 2", "微博视频卡片"} {
		if _, ok := tt.Find(lbl); !ok {
			t.Fatalf("expected online retweet detail control %q", lbl)
		}
	}

	if err := tt.Click("缓存当前微博"); err != nil {
		t.Fatalf("click cache button: %v", err)
	}
	waitUIIdle(t, app, posted)
	stub.mu.Lock()
	calls := append([]struct {
		mid string
		via *library.ItemVia
	}(nil), stub.cachedCalls...)
	stub.mu.Unlock()
	if len(calls) != 1 || calls[0].mid != fwdMid || calls[0].via == nil || calls[0].via.Reason != library.ViaForward || calls[0].via.UID != fwdUID {
		t.Fatalf("unexpected CacheWeiboPost call: %+v", calls)
	}

	// Clicking uncached origin video card with VideoURL triggers StagePreviewMedia (never silent dead-click).
	tt.Frame()
	if err := tt.Click("微博视频卡片"); err != nil {
		t.Fatalf("click uncached origin video card: %v", err)
	}
	waitUIIdle(t, app, posted)
	tt.Frame()
	stub.mu.Lock()
	staged := append([]struct{ site, url, ext string }(nil), stub.stagedPreview...)
	stub.mu.Unlock()
	if len(staged) != 1 || staged[0].site != "weibo" || staged[0].url != videoURL {
		t.Fatalf("expected StagePreviewMedia for uncached origin video, got %+v", staged)
	}
	app.closePlayerOverlay()

	// Uncached video card WITHOUT VideoURL shows inline cache hint and never dead-clicks.
	origNoURL := *origPost
	vidNoURL := *origPost.Video
	vidNoURL.VideoURL = ""
	origNoURL.Video = &vidNoURL
	fwdNoURL := *fwdPost
	fwdNoURL.Retweeted = &origNoURL
	app.CurrentWeiboPost = &fwdNoURL
	app.BannerInfo = ""
	tt.Frame()
	if _, ok := tt.Find("微博视频缓存提示"); !ok {
		t.Fatal("expected inline hint 微博视频缓存提示 when VideoURL is empty")
	}
	if err := tt.Click("微博视频卡片"); err != nil {
		t.Fatalf("click uncached video card without URL: %v", err)
	}
	if !strings.Contains(app.BannerInfo, "缓存") {
		t.Fatalf("expected BannerInfo hint on uncached video click, got %q", app.BannerInfo)
	}

	// Clicking 打开原微博 opens origin post and clears weiboCacheFor.
	app.CurrentWeiboPost = fwdPost
	tt.Frame()
	if err := tt.Click("打开原微博"); err != nil {
		t.Fatalf("click 打开原微博: %v", err)
	}
	if app.weiboCacheFor.ForUID != "" {
		t.Fatalf("expected 打开原微博 to clear weiboCacheFor, got %+v", app.weiboCacheFor)
	}
	waitUIIdle(t, app, posted)
	if app.CurrentWeiboPost == nil || app.CurrentWeiboPost.Mid != origMid {
		t.Fatalf("expected CurrentWeiboPost to switch to origin %s, got %+v", origMid, app.CurrentWeiboPost)
	}

	// 2. Cached retweet on disk under weibo/<fwdUID>/forwards/<fwdMid>/:
	//    Online detail binds origin LivePhoto & video to OUTER itemKey; Offline detail renders FULL retweet card.
	lib := stub.raw.LibraryStore()
	fwdDir := filepath.Join(lib.CacheDir(), "weibo", fwdUID, "forwards", fwdMid)
	if err := os.MkdirAll(filepath.Join(fwdDir, "images"), 0o700); err != nil {
		t.Fatal(err)
	}
	pngBytes := buildTestPNGBytes()
	_ = os.WriteFile(filepath.Join(fwdDir, "images", "01.png"), pngBytes, 0o600)
	_ = os.WriteFile(filepath.Join(fwdDir, "images", "02.png"), pngBytes, 0o600)
	_ = os.WriteFile(filepath.Join(fwdDir, "images", "03.png"), pngBytes, 0o600)
	_ = os.WriteFile(filepath.Join(fwdDir, "images", "03_live.mp4"), []byte("synth-live-mp4"), 0o600)
	_ = os.WriteFile(filepath.Join(fwdDir, "cover.png"), pngBytes, 0o600)
	_ = os.WriteFile(filepath.Join(fwdDir, "video.mp4"), []byte("synth-video-mp4"), 0o600)

	savedFwd, err := lib.SaveItem(fwdDir, library.ItemMetadata{
		Version:     1,
		ID:          "dynamic:" + fwdMid,
		RawID:       fwdMid,
		Site:        "weibo",
		Kind:        "dynamic",
		DynamicType: "forward",
		TypeLabel:   "转发",
		Status:      "complete",
		Title:       "转发收藏！",
		Text:        "转发收藏！附带一张外层配图。",
		URL:         fwdPost.CanonicalURL,
		PubTs:       1710004800,
		Duration:    95,
		Author:      library.ItemAuthor{Site: "weibo", UID: fwdUID, Name: "转发订阅博主"},
		Via: &library.ItemVia{
			Reason:    library.ViaForward,
			Site:      "weibo",
			UID:       fwdUID,
			Name:      "转发订阅博主",
			DynamicID: fwdMid,
		},
		Origin: &library.ItemOrigin{
			Site:            "weibo",
			ID:              origMid,
			CanonicalURL:    origPost.CanonicalURL,
			Author:          library.ItemAuthor{Site: "weibo", UID: origUID, Name: "原博主甲"},
			PubTs:           1710004000,
			Title:           "原微博高清航拍视频",
			Text:            origPost.Text,
			VideoFromOrigin: true,
		},
		NotePages: []library.NotePageRecord{
			{Index: 1, Kind: "image", FromOrigin: false, Image: "images/01.png", Width: 64, Height: 64},
			{Index: 2, Kind: "image", FromOrigin: true, Image: "images/02.png", Width: 64, Height: 64},
			{Index: 3, Kind: "motion", FromOrigin: true, Image: "images/03.png", Motion: "images/03_live.mp4", Width: 64, Height: 64, Duration: 3},
		},
		Media: []library.MediaFile{
			{Role: "image", Path: "images/01.png", FromOrigin: false, Width: 64, Height: 64},
			{Role: "image", Path: "images/02.png", FromOrigin: true, Width: 64, Height: 64},
			{Role: "image", Path: "images/03.png", Motion: "images/03_live.mp4", FromOrigin: true, Width: 64, Height: 64},
			{Role: "motion", Path: "images/03_live.mp4", FromOrigin: true, Duration: 3},
			{Role: "cover", Path: "cover.png", FromOrigin: true, Width: 64, Height: 64},
			{Role: "video", Path: "video.mp4", FromOrigin: true, Duration: 95},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Online view with cached outer item: origin images bind to outer ItemKey & local files!
	onlineCachedFwd := *fwdPost
	onlineCachedFwd.Images = []service.WeiboMediaItemDTO{{Index: 1, Kind: "image", ImageURL: "https://wx1.sinaimg.cn/large/outer_01.jpg", Width: 64, Height: 64}}
	app.CurrentWeiboPost = &onlineCachedFwd
	app.CurrentRoute = RouteWeiboPost
	tt.Frame()

	// Origin media pages scoped via FromOrigin must bind to outer itemKey and resolve local motion path!
	origPages := weiboMediaToNotePagesScoped(origPost.Images, &savedFwd, true, len(onlineCachedFwd.Images), false)
	if len(origPages) != 2 || origPages[0].ImageRel != "images/02.png" || origPages[1].MotionRel != "images/03_live.mp4" {
		t.Fatalf("unexpected scoped origin pages: %+v", origPages)
	}
	outerPages := weiboMediaToNotePagesScoped(onlineCachedFwd.Images, &savedFwd, false, len(onlineCachedFwd.Images), false)
	if len(outerPages) != 1 || outerPages[0].ImageRel != "images/01.png" {
		t.Fatalf("unexpected scoped outer pages: %+v", outerPages)
	}
	// Open origin LivePhoto through viewer bound to outer ItemKey.
	app.openNotePagesViewer(savedFwd.ItemKey, "weibo", origPages, 1)
	if !app.ImageViewer.Open || app.ImageViewer.ItemKey != savedFwd.ItemKey {
		t.Fatalf("ImageViewer should bind to outer ItemKey %s, got open=%v key=%q", savedFwd.ItemKey, app.ImageViewer.Open, app.ImageViewer.ItemKey)
	}
	if len(app.ImageViewer.InlineMediaPaths) < 2 || !strings.HasSuffix(app.ImageViewer.InlineMediaPaths[1], "03_live.mp4") {
		t.Fatalf("expected resolved local origin LivePhoto motion path, got %v", app.ImageViewer.InlineMediaPaths)
	}
	app.closeImageViewer()

	// Clicking origin video card on online cached retweet plays outer cached item locally!
	if err := tt.Click("微博视频卡片"); err != nil {
		t.Fatalf("click cached origin video card: %v", err)
	}
	if !app.PlayerOverlay.Open || app.PlayerOverlay.ItemKey != savedFwd.ItemKey {
		t.Fatalf("expected PlayerOverlay bound to outer %s, got open=%v key=%q", savedFwd.ItemKey, app.PlayerOverlay.Open, app.PlayerOverlay.ItemKey)
	}
	app.closePlayerOverlay()

	// 3. Offline detail: regular retweet (Via.UID == Author.UID) must NOT show "转发自 <Author>",
	//    shows "转发微博" badge, and renders full retweet card without "打开原微博".
	app.OpenLibraryItemDetail(savedFwd)
	tt.Frame()
	if _, ok := tt.Find("本机转发微博标记"); !ok {
		t.Fatal("expected 本机转发微博标记 badge in offline retweet detail")
	}
	if _, ok := tt.Find("本机转发来源标记"); ok {
		t.Fatal("must NOT show cross-account forward source line when Via.UID == Author.UID")
	}
	if tt.HasText("转发自 转发订阅博主") {
		t.Fatal("must NOT show broken 转发自 <Author> label when Via.UID == Author.UID")
	}
	if r, ok := tt.Find("转发原微博卡片"); !ok || !tt.HasText("原博主甲") || !tt.HasText("原微博完整正文") {
		t.Fatalf("expected full origin retweet card in offline Weibo detail: cardFound=%v rect=%+v hasAuthor=%v hasText=%v savedOrigin=%+v",
			ok, r, tt.HasText("原博主甲"), tt.HasText("原微博完整正文"), savedFwd.Origin)
	}
	if _, ok := tt.Find("打开原微博"); ok {
		t.Fatal("offline detail must NOT show online 打开原微博 button")
	}

	// 4. Offline detail for cross-account fast-retweet (Via.UID != Author.UID) with single video:
	//    shows "转发微博" badge AND "由 <Via.Name> 转发", and renders playable origin video card!
	fastRetweetItem := savedFwd
	fastRetweetItem.ItemKey = library.CanonicalItemKey("weibo", "dynamic", "5000000000008002")
	fastRetweetItem.Author = library.ItemAuthor{Site: "weibo", UID: origUID, Name: "原博主甲"}
	fastRetweetItem.NotePages = nil
	fastRetweetItem.Media = []library.MediaFile{
		{Role: "cover", Path: "cover.png", FromOrigin: true, Width: 64, Height: 64},
		{Role: "video", Path: "video.mp4", FromOrigin: true, Duration: 95},
	}
	app.OpenLibraryItemDetail(fastRetweetItem)
	tt.Frame()
	if _, ok := tt.Find("本机转发微博标记"); !ok {
		t.Fatal("expected 本机转发微博标记 on fast-retweet offline detail")
	}
	if _, ok := tt.Find("本机转发来源标记"); !ok || !tt.HasText("由 转发订阅博主 转发") {
		t.Fatal("expected 由 转发订阅博主 转发 on cross-account fast-retweet offline detail")
	}
	if _, ok := tt.Find("微博视频卡片"); !ok || !tt.HasText("原微博高清航拍视频") {
		t.Fatal("expected playable origin video card on offline retweet detail")
	}
}

func TestHomeAndSubDetailCachedWeiboRetweetOriginAndFastRetweetAvatar(t *testing.T) {
	app, stub, cleanup := newWeiboRetweetHarness(t)
	defer cleanup()

	avatarDir := t.TempDir()
	subColor := color.RGBA{R: 230, G: 64, B: 140, A: 255}
	origColor := color.RGBA{R: 30, G: 180, B: 230, A: 255}
	subPNG := writeSolidPNG(t, avatarDir, "wb_sub.png", subColor)
	origPNG := writeSolidPNG(t, avatarDir, "wb_orig.png", origColor)

	const (
		subUID  = "500000010"
		origUID = "500000090"
		fastMid = "5000000000009001"
		imgMid  = "5000000000009002"
	)
	stub.mu.Lock()
	stub.avatars["weibo:"+subUID] = subPNG
	stub.avatars["weibo:"+origUID] = origPNG
	stub.mu.Unlock()

	sub, err := stub.raw.CreateSubscription("微博订阅主号", "", []subscription.AccountRef{
		{Site: "weibo", UID: subUID, Name: "转发博主A"},
	}, subscription.SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatal(err)
	}

	lib := stub.raw.LibraryStore()
	// 1. Cached fast-retweet single video (Via.UID = subUID != Author.UID = origUID).
	fastDir := filepath.Join(lib.CacheDir(), "weibo", subUID, "forwards", fastMid)
	_ = os.MkdirAll(fastDir, 0o700)
	_ = os.WriteFile(filepath.Join(fastDir, "cover.png"), buildTestPNGBytes(), 0o600)
	_ = os.WriteFile(filepath.Join(fastDir, "video.mp4"), []byte("synth-mp4"), 0o600)
	savedFast, err := lib.SaveItem(fastDir, library.ItemMetadata{
		Version:     1,
		ID:          "dynamic:" + fastMid,
		RawID:       fastMid,
		Site:        "weibo",
		Kind:        "dynamic",
		DynamicType: "forward",
		TypeLabel:   "转发",
		Status:      "complete",
		Title:       "快转微博",
		Text:        "快转微博",
		PubTs:       1730010000,
		Duration:    64,
		Author:      library.ItemAuthor{Site: "weibo", UID: origUID, Name: "原视频作者"},
		Via: &library.ItemVia{
			Reason:    library.ViaForward,
			Site:      "weibo",
			UID:       subUID,
			Name:      "转发博主A",
			DynamicID: fastMid,
		},
		Origin: &library.ItemOrigin{
			Site:            "weibo",
			ID:              "5000000000008888",
			Author:          library.ItemAuthor{Site: "weibo", UID: origUID, Name: "原视频作者"},
			PubTs:           1730009000,
			Title:           "原视频专属标题：山海延时",
			Text:            "原视频完整正文描述。",
			VideoFromOrigin: true,
		},
		Media: []library.MediaFile{
			{Role: "cover", Path: "cover.png", FromOrigin: true, Width: 64, Height: 64},
			{Role: "video", Path: "video.mp4", FromOrigin: true, Duration: 64},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// 2. Cached regular retweet with outer + FromOrigin images and outer Card.
	imgDir := filepath.Join(lib.CacheDir(), "weibo", subUID, "forwards", imgMid)
	_ = os.MkdirAll(filepath.Join(imgDir, "images"), 0o700)
	_ = os.WriteFile(filepath.Join(imgDir, "images", "01.png"), buildTestPNGBytes(), 0o600)
	_ = os.WriteFile(filepath.Join(imgDir, "images", "02.png"), buildTestPNGBytes(), 0o600)
	savedImg, err := lib.SaveItem(imgDir, library.ItemMetadata{
		Version:     1,
		ID:          "dynamic:" + imgMid,
		RawID:       imgMid,
		Site:        "weibo",
		Kind:        "dynamic",
		DynamicType: "forward",
		TypeLabel:   "转发",
		Status:      "complete",
		Title:       "太好看了转发一下",
		Text:        "太好看了转发一下",
		PubTs:       1730009500,
		Author:      library.ItemAuthor{Site: "weibo", UID: subUID, Name: "转发博主A"},
		Via:         &library.ItemVia{Reason: library.ViaForward, Site: "weibo", UID: subUID, Name: "转发博主A", DynamicID: imgMid},
		Card:        &library.ItemRichCard{Kind: "webpage", Label: "网页链接", Title: "外层链接卡片", URL: "https://example.com/a"},
		Origin: &library.ItemOrigin{
			Site:   "weibo",
			ID:     "5000000000007777",
			Author: library.ItemAuthor{Site: "weibo", UID: origUID, Name: "原摄影博主"},
			PubTs:  1730008000,
			Text:   "原图文微博完整正文内容。",
		},
		NotePages: []library.NotePageRecord{
			{Index: 1, Kind: "image", FromOrigin: false, Image: "images/01.png", Width: 64, Height: 64},
			{Index: 2, Kind: "image", FromOrigin: true, Image: "images/02.png", Width: 64, Height: 64},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	app.CompleteLogin()
	app.RestartHomeSession()
	app.Navigate(RouteHome)

	// Verify buildFeedPostCard hydration directly.
	fastCard := app.buildFeedPostCard(library.FeedEntry{
		FeedKey: savedFast.ItemKey, ItemKey: savedFast.ItemKey, Site: "weibo", Kind: "dynamic",
		RawID: fastMid, AuthorUID: origUID, AuthorName: "原视频作者", Title: "快转微博", Summary: "快转微博",
		PubTs: savedFast.PubTs, Cached: true, CacheStatus: "green",
	})
	if fastCard.Video != nil {
		t.Fatalf("VideoFromOrigin must attach video to Origin.Video, not outer Video: %+v", fastCard.Video)
	}
	if fastCard.Origin == nil || fastCard.Origin.Video == nil || fastCard.Origin.Video.Title != "原视频专属标题：山海延时" || fastCard.Origin.Video.ItemKey != savedFast.ItemKey {
		t.Fatalf("expected Origin.Video with origin title & outer ItemKey, got %+v", fastCard.Origin)
	}

	imgCard := app.buildFeedPostCard(library.FeedEntry{
		FeedKey: savedImg.ItemKey, ItemKey: savedImg.ItemKey, Site: "weibo", Kind: "dynamic",
		RawID: imgMid, AuthorUID: subUID, AuthorName: "转发博主A", Title: "太好看了转发一下", Summary: "太好看了转发一下",
		PubTs: savedImg.PubTs, Cached: true, CacheStatus: "green",
	})
	if len(imgCard.NotePages) != 1 || imgCard.NotePages[0].ImageRel != "images/01.png" {
		t.Fatalf("expected 1 outer NotePage, got %+v", imgCard.NotePages)
	}
	if imgCard.Origin == nil || len(imgCard.Origin.NotePages) != 1 || imgCard.Origin.NotePages[0].ImageRel != "images/02.png" {
		t.Fatalf("expected 1 origin NotePage, got %+v", imgCard.Origin)
	}
	if imgCard.WeiboCard == nil || imgCard.WeiboCard.Title != "外层链接卡片" {
		t.Fatalf("expected outer WeiboCard mapped from ItemMetadata.Card, got %+v", imgCard.WeiboCard)
	}

	// Render Home: fast-retweet renders subscriber's local PNG avatar and Origin.Video card.
	tt := ui.NewTester(app.View, 1280, 1200)
	settle(tt, 2)
	avatarRect, ok := tt.Find("作者头像：" + savedFast.ItemKey)
	if !ok {
		t.Fatalf("missing avatar for fast-retweet on Home")
	}
	assertColorClose(t, "Home fast-retweet subscriber avatar", sampleCenterRGBA(t, tt.Image(), avatarRect), subColor)
	if _, ok := tt.Find("转发原内容：" + savedFast.ItemKey); !ok {
		t.Fatal("missing origin quote box for fast-retweet on Home")
	}
	if _, ok := tt.Find("播放原视频卡片：" + savedFast.ItemKey); !ok || !tt.HasText("原视频专属标题：山海延时") {
		t.Fatal("missing playable origin video card on Home")
	}

	// Render Subscription Detail › 内容: fast-retweet ALSO appears & displays subscriber's avatar + origin quote box.
	app.SubDetailTab = "content"
	app.OpenSubscriptionDetail(sub)
	settle(tt, 2)
	subAvatarRect, ok := tt.Find("作者头像：" + savedFast.ItemKey)
	if !ok {
		t.Fatalf("missing fast-retweet in Subscription Detail › 内容 (items=%d)", len(app.SubFeedItems))
	}
	assertColorClose(t, "Sub Detail fast-retweet subscriber avatar", sampleCenterRGBA(t, tt.Image(), subAvatarRect), subColor)
	if _, ok := tt.Find("转发原内容：" + savedFast.ItemKey); !ok {
		t.Fatal("missing origin quote box on Subscription Detail › 内容")
	}
}

func TestWeiboUserProfileRetweetCardOpenForAndMineFavoritesRetweet(t *testing.T) {
	app, stub, cleanup := newWeiboRetweetHarness(t)
	defer cleanup()
	app.CompleteLogin()

	posted := make(chan func(), 64)
	app.dispatch = func(fn func()) { posted <- fn }

	const (
		profileUID = "500000050"
		origUID    = "500000060"
		rtMid      = "5000000000006001"
		fastMid    = "5000000000006002"
	)

	user := &service.WeiboUserView{
		UID:           profileUID,
		ScreenName:    "主页博主B",
		StatusesCount: 20,
	}
	rtPost := service.WeiboPostView{
		Mid:              rtMid,
		Author:           service.WeiboAuthorDTO{UID: profileUID, ScreenName: "主页博主B"},
		CreatedAt:        1730020000,
		Text:             "主页转发这条精彩图文和视频",
		IsRetweet:        true,
		OriginIncomplete: true,
		CanonicalURL:     "https://weibo.com/" + profileUID + "/" + rtMid,
		Retweeted: &service.WeiboPostView{
			Mid:       "5000000000005555",
			Author:    service.WeiboAuthorDTO{UID: origUID, ScreenName: "原创作者C"},
			CreatedAt: 1730019000,
			Text:      "原微博节选文字内容。",
			Images:    []service.WeiboMediaItemDTO{{Index: 1, Kind: "image", ImageURL: "https://wx1.sinaimg.cn/large/u_01.jpg", Width: 640, Height: 480}},
			Video:     &service.WeiboVideoDTO{ObjectID: "1034:5555", Title: "原创视频标题", DurationSec: 45},
		},
	}

	// 1. weiboPostToPostCard populates full Origin, Incomplete hint, WeiboCacheVia, and OnOpenPost -> OpenWeiboPostFor.
	card := app.weiboPostToPostCard(rtPost, user)
	if card.Origin == nil || len(card.Origin.NotePages) != 1 || card.Origin.Video == nil || !card.Origin.Incomplete {
		t.Fatalf("expected full Origin with NotePages, Video, and Incomplete=true, got %+v", card.Origin)
	}
	if card.WeiboCacheVia == nil || card.WeiboCacheVia.Reason != library.ViaForward || card.WeiboCacheVia.UID != profileUID {
		t.Fatalf("expected WeiboCacheVia for profile %s, got %+v", profileUID, card.WeiboCacheVia)
	}

	// Cache via card "⋯ → 缓存" (cachePost) passes ViaForward with profileUID!
	app.cachePost(card)
	waitUIIdle(t, app, posted)
	stub.mu.Lock()
	nCalls := len(stub.cachedCalls)
	var lastCall struct {
		mid string
		via *library.ItemVia
	}
	if nCalls > 0 {
		lastCall = stub.cachedCalls[nCalls-1]
	}
	stub.mu.Unlock()
	if nCalls != 1 || lastCall.mid != rtMid || lastCall.via == nil || lastCall.via.UID != profileUID {
		t.Fatalf("cachePost on Weibo user retweet card did not pass ViaForward: %+v", lastCall)
	}

	// Render Weibo User Profile and click incomplete hint -> sets weiboCacheFor!
	app.CurrentWeiboUser = user
	app.CurrentWeiboPosts = []service.WeiboPostView{rtPost}
	app.CurrentRoute = RouteWeiboUser
	app.WeiboUserListState = &ui.ListState{}
	tt := ui.NewTester(app.View, 1280, 1200)
	tt.Frame()
	if _, ok := tt.Find("原微博未完整提示：wb:" + rtMid); !ok {
		t.Fatal("expected OriginIncomplete hint on Weibo User Profile retweet card")
	}
	if err := tt.Click("原微博未完整提示：wb:" + rtMid); err != nil {
		t.Fatalf("click OriginIncomplete hint: %v", err)
	}
	if app.weiboCacheFor.ForUID != profileUID || app.weiboCacheFor.Mid != rtMid {
		t.Fatalf("expected clicking profile retweet to set weiboCacheFor=%s/%s, got %+v", profileUID, rtMid, app.weiboCacheFor)
	}
	waitUIIdle(t, app, posted)

	// Direct OpenWeiboPost clears weiboCacheFor; ForwarderHintUID still resolves ViaForward for fast-retweets!
	app.OpenWeiboPost(fastMid, "直接打开快转", "原创作者C", origUID, "", "https://weibo.com/"+profileUID+"/"+fastMid)
	if app.weiboCacheFor.ForUID != "" {
		t.Fatalf("direct OpenWeiboPost must clear weiboCacheFor, got %+v", app.weiboCacheFor)
	}
	waitUIIdle(t, app, posted)

	hintPost := &service.WeiboPostView{
		Mid:               fastMid,
		Author:            service.WeiboAuthorDTO{UID: origUID, ScreenName: "原创作者C"},
		IsFastRetweet:     true,
		ForwarderHintUID:  profileUID,
		ForwarderHintName: "主页博主B",
	}
	hintVia := app.weiboCacheViaFor(hintPost)
	if hintVia == nil || hintVia.Reason != library.ViaForward || hintVia.UID != profileUID || hintVia.Name != "主页博主B" {
		t.Fatalf("expected weiboCacheViaFor to use ForwarderHintUID for fast-retweet, got %+v", hintVia)
	}

	// 2. Mine › Weibo Favorites renders retweet quote box cleanly.
	stub.mu.Lock()
	stub.mineSections["weibo"] = []service.AccountSection{
		{Site: "weibo", Kind: service.AccountFavorites, Label: "收藏", Available: true},
	}
	stub.minePages["weibo/favorites"] = service.AccountPage{
		Items: []service.AccountItem{
			{
				Kind:       service.AccountItemPost,
				Site:       "weibo",
				ID:         rtMid,
				Title:      rtPost.Text,
				Author:     "主页博主B",
				AuthorID:   profileUID,
				BrowserURL: rtPost.CanonicalURL,
				Post:       &rtPost,
			},
		},
	}
	stub.mu.Unlock()
	app.OpenMine("weibo", service.AccountFavorites)
	waitUIIdle(t, app, posted)
	tt.Frame()
	if _, ok := tt.Find("我的微博转发内容：" + rtMid); !ok {
		t.Fatal("expected Mine › Weibo Favorites to render retweet quote box")
	}
	if !tt.HasText("原创作者C") || !tt.HasText("原微博节选文字内容") || !tt.HasText("原创视频标题") {
		t.Fatal("expected Mine › Weibo Favorites retweet box to display origin author, text, and video summary")
	}
}
