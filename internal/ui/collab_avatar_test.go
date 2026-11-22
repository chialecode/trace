package ui

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

type collabAvatarStub struct {
	service.BackendAPI
	raw *service.Backend

	mu              sync.Mutex
	avatars         map[string]string
	avatarCalls     map[string]int
	openedLinks     []string
	fetchImageCalls atomic.Int32
}

func (b *collabAvatarStub) StatusOf(siteID string) credential.Status {
	switch normalizeSiteIDUI(siteID) {
	case "douyin":
		return credential.Status{Site: "douyin", LoggedIn: true, State: "active", SecUID: "MS4wLjABAAAA_self", Uname: "抖音测试号"}
	case "weibo":
		return credential.Status{Site: "weibo", LoggedIn: true, State: "active", UID: "5000001", Uname: "微博测试号"}
	default:
		return b.BackendAPI.StatusOf(siteID)
	}
}

func (b *collabAvatarStub) HasAnyLogin() bool {
	return true
}

func (b *collabAvatarStub) AccountAvatarPath(siteID, uid string) string {
	key := normalizeSiteIDUI(siteID) + ":" + strings.TrimSpace(uid)
	b.mu.Lock()
	if b.avatarCalls == nil {
		b.avatarCalls = make(map[string]int)
	}
	b.avatarCalls[key]++
	p := b.avatars[key]
	b.mu.Unlock()
	if p != "" {
		return p
	}
	return b.BackendAPI.AccountAvatarPath(siteID, uid)
}

func (b *collabAvatarStub) FetchImage(ctx context.Context, rawURL string) ([]byte, error) {
	b.fetchImageCalls.Add(1)
	return nil, context.Canceled
}

func (b *collabAvatarStub) OpenLink(_ context.Context, raw string) (*service.OpenLinkResult, error) {
	b.mu.Lock()
	b.openedLinks = append(b.openedLinks, raw)
	b.mu.Unlock()
	return &service.OpenLinkResult{
		Site:       string(site.Douyin),
		SiteTarget: &site.ResolveTarget{Site: site.Douyin, Kind: "user", ID: raw, SecUID: raw},
		DouyinUser: &service.DouyinUserView{
			SecUID:   raw,
			Nickname: "解析目标作者",
		},
	}, nil
}

func writeSolidPNG(t *testing.T, dir, name string, col color.RGBA) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.SetRGBA(x, y, col)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func sampleCenterRGBA(t *testing.T, img image.Image, r ui.Rect) color.RGBA {
	t.Helper()
	cx := int(r.X + r.W/2)
	cy := int(r.Y + r.H/2)
	if !image.Pt(cx, cy).In(img.Bounds()) {
		t.Fatalf("avatar center (%d,%d) outside image bounds %v (rect=%+v)", cx, cy, img.Bounds(), r)
	}
	return color.RGBAModel.Convert(img.At(cx, cy)).(color.RGBA)
}

func assertColorClose(t *testing.T, label string, got, want color.RGBA) {
	t.Helper()
	diff := func(a, b uint8) int {
		if a > b {
			return int(a - b)
		}
		return int(b - a)
	}
	if diff(got.R, want.R) > 12 || diff(got.G, want.G) > 12 || diff(got.B, want.B) > 12 {
		t.Fatalf("%s center pixel = #%02x%02x%02x (a=%d), want close to #%02x%02x%02x",
			label, got.R, got.G, got.B, got.A, want.R, want.G, want.B)
	}
}

func TestDouyinCollabAvatarUsesCachedViaAccountOnHomeAndSubDetail(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	raw := app.backend.(*service.Backend)
	if _, err := raw.ImportCookie(context.Background(), "SESSDATA=synth_bili; bili_jct=csrf; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}

	avatarDir := t.TempDir()
	viaColor := color.RGBA{R: 224, G: 32, B: 192, A: 255}       // magenta: cached via account
	coCreatorColor := color.RGBA{R: 16, G: 208, B: 224, A: 255} // cyan: co-creator
	viaPNG := writeSolidPNG(t, avatarDir, "via.png", viaColor)
	coPNG := writeSolidPNG(t, avatarDir, "cocreator.png", coCreatorColor)

	const (
		viaUID       = "MS4wLjABAAAA_cached_via_account"
		coCreatorUID = "MS4wLjABAAAA_cocreator_publisher"
		awemeID      = "7399880001"
	)

	stub := &collabAvatarStub{
		BackendAPI: raw,
		raw:        raw,
		avatars: map[string]string{
			"douyin:" + viaUID: viaPNG,
		},
	}
	app.backend = stub

	subVia, err := raw.CreateSubscription("猫九酱", "", []subscription.AccountRef{
		{Site: "douyin", UID: viaUID, Name: "猫九酱抖音"},
	}, subscription.SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatal(err)
	}

	lib := raw.LibraryStore()
	workDir := filepath.Join(lib.CacheDir(), "douyin", "via_hash", "works", awemeID)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	saved, err := lib.SaveItem(workDir, library.ItemMetadata{
		Version: 1,
		Site:    "douyin",
		Kind:    "video",
		ID:      awemeID,
		RawID:   awemeID,
		Status:  "complete",
		Title:   "抖音共创视频作品",
		Desc:    "共创简介",
		PubTs:   1730001000,
		Author: library.ItemAuthor{
			Site: "douyin",
			UID:  coCreatorUID,
			Name: "悠长假期",
			Face: "https://example.invalid/cocreator.jpg",
		},
		Via: &library.ItemVia{
			Reason: library.ViaCollab,
			Site:   "douyin",
			UID:    viaUID,
			Name:   "猫九酱抖音",
		},
		DouyinVideo: &library.DouyinVideoInfo{QualityName: "原画", Codec: "hevc"},
	})
	if err != nil {
		t.Fatal(err)
	}

	app.RefreshData()
	app.RestartHomeSession()
	app.Navigate(RouteHome)

	// (a) Only via has a local avatar PNG and co-creator has none:
	//     Home renders via's local PNG (NOT initial), keeps co-creator author name + blue subVia name,
	//     and clicking avatar opens subVia (zero network).
	tt := ui.NewTester(app.View, 1280, 860)
	settle(tt, 2)

	post := app.feedEntryToPostCard(app.FeedItems[0])
	if gotSub, ok := app.homePostSubscription(post); !ok || gotSub.ID != subVia.ID {
		t.Fatalf("(a) homePostSubscription = (%q, %v), want subVia %q", gotSub.ID, ok, subVia.ID)
	}
	if !tt.HasText("悠长假期") {
		t.Fatal("(a) Home collab post must keep co-creator post.AuthorName")
	}
	if _, ok := tt.Find("主页订阅名：" + saved.ItemKey); !ok || !tt.HasText("猫九酱") {
		t.Fatal("(a) Home collab post must display blue subscription name 猫九酱 (DEC-063 ②)")
	}
	avatarRect, ok := tt.Find("作者头像：" + saved.ItemKey)
	if !ok {
		t.Fatalf("(a) missing avatar box for %s", saved.ItemKey)
	}
	assertColorClose(t, "(a) Home via-only avatar", sampleCenterRGBA(t, tt.Image(), avatarRect), viaColor)
	if stub.fetchImageCalls.Load() != 0 {
		t.Fatalf("(a) Home collab avatar must make 0 remote FetchImage calls, got %d", stub.fetchImageCalls.Load())
	}

	// Clicking avatar on Home opens subVia with 0 OpenLink network calls.
	if err := tt.Click("作者头像：" + saved.ItemKey); err != nil {
		t.Fatalf("(a) click collab avatar on Home: %v", err)
	}
	tt.Frame()
	if app.CurrentRoute != RouteSubscriptions || !app.SubDetailOpen || app.SelectedSubID != subVia.ID {
		t.Fatalf("(a) clicking Home collab avatar navigated to route=%s detail=%v sub=%q, want subVia=%q",
			app.CurrentRoute, app.SubDetailOpen, app.SelectedSubID, subVia.ID)
	}
	stub.mu.Lock()
	openedBeforeSubDetail := len(stub.openedLinks)
	stub.mu.Unlock()
	if openedBeforeSubDetail != 0 {
		t.Fatalf("(a) clicking Home collab avatar must be zero-network, got %d OpenLink calls", openedBeforeSubDetail)
	}

	// (b) Subscription BOTH co-creator AND via, and give BOTH distinct local PNGs:
	//     homePostSubscription MUST still return via's subscription and render via's PNG (NOT co-creator's).
	subCo, err := raw.CreateSubscription("悠长假期订阅", "", []subscription.AccountRef{
		{Site: "douyin", UID: coCreatorUID, Name: "悠长假期"},
	}, subscription.SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatal(err)
	}
	stub.mu.Lock()
	stub.avatars["douyin:"+coCreatorUID] = coPNG
	stub.mu.Unlock()
	app.handleBackendEvent(service.Event{Type: service.EventLibraryChanged})
	app.RefreshData()
	app.Navigate(RouteHome)
	settle(tt, 2)

	post = app.feedEntryToPostCard(app.FeedItems[0])
	if gotSub, ok := app.homePostSubscription(post); !ok || gotSub.ID != subVia.ID || gotSub.ID == subCo.ID {
		t.Fatalf("(b) homePostSubscription when both subscribed = (%q, %v), want via sub %q (not co-creator sub %q)",
			gotSub.ID, ok, subVia.ID, subCo.ID)
	}
	if !tt.HasText("猫九酱") || tt.HasText("悠长假期订阅") {
		t.Fatal("(b) Home collab post blue subscription label should show via's subscription 猫九酱, not co-creator's")
	}
	avatarRect, ok = tt.Find("作者头像：" + saved.ItemKey)
	if !ok {
		t.Fatalf("(b) missing avatar box for %s", saved.ItemKey)
	}
	assertColorClose(t, "(b) Home both-subscribed collab avatar", sampleCenterRGBA(t, tt.Image(), avatarRect), viaColor)

	// (c) Subscription Detail › 内容 (RouteSubscriptions, post.InHomeFeed=true):
	//     renders via's local PNG (NOT co-creator's) and clicking avatar does NOT jump to co-creator.
	app.SubDetailTab = "content"
	app.OpenSubscriptionDetail(subVia)
	settle(tt, 2)
	if app.CurrentRoute != RouteSubscriptions || !app.SubDetailOpen || app.SubDetailTab != "content" {
		t.Fatalf("(c) expected Subscription Detail content tab, got route=%s open=%v tab=%s",
			app.CurrentRoute, app.SubDetailOpen, app.SubDetailTab)
	}
	subAvatarRect, ok := tt.Find("作者头像：" + saved.ItemKey)
	if !ok {
		t.Fatalf("(c) missing collab post avatar on Subscription Detail content tab (items=%d)", len(app.SubFeedItems))
	}
	assertColorClose(t, "(c) Subscription Detail content collab avatar", sampleCenterRGBA(t, tt.Image(), subAvatarRect), viaColor)

	if err := tt.Click("作者头像：" + saved.ItemKey); err != nil {
		t.Fatalf("(c) click collab avatar on Subscription Detail: %v", err)
	}
	tt.Frame()
	if app.CurrentRoute != RouteSubscriptions || !app.SubDetailOpen {
		t.Fatalf("(c) clicking collab avatar on Subscription Detail must stay on Subscription Detail, got route=%s", app.CurrentRoute)
	}
	stub.mu.Lock()
	openedAfterSubDetail := len(stub.openedLinks)
	stub.mu.Unlock()
	if openedAfterSubDetail != 0 {
		t.Fatalf("(c) clicking collab avatar on Subscription Detail must not open user page, got %v", stub.openedLinks)
	}
}

func TestBilibiliCollabLegacyMidSubscriptionMatchesViaAndRendersViaAvatar(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	raw := app.backend.(*service.Backend)
	if _, err := raw.ImportCookie(context.Background(), "SESSDATA=synth_bili; bili_jct=csrf; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}

	avatarDir := t.TempDir()
	viaColor := color.RGBA{R: 240, G: 128, B: 32, A: 255} // orange: cached via member
	coColor := color.RGBA{R: 32, G: 96, B: 240, A: 255}   // blue: co-creator uploader
	viaPNG := writeSolidPNG(t, avatarDir, "bili_via.png", viaColor)
	coPNG := writeSolidPNG(t, avatarDir, "bili_co.png", coColor)

	const (
		viaMid int64 = 1382015878
		coMid  int64 = 9999001
		bvid         = "BV1Ryui6nE4G"
	)

	stub := &collabAvatarStub{
		BackendAPI: raw,
		raw:        raw,
		avatars: map[string]string{
			"bilibili:1382015878": viaPNG,
			"bilibili:9999001":    coPNG,
		},
	}
	app.backend = stub

	lib := raw.LibraryStore()
	vidDir := filepath.Join(lib.CacheDir(), "bilibili", "1382015878", "videos", bvid)
	if err := os.MkdirAll(vidDir, 0o755); err != nil {
		t.Fatal(err)
	}
	saved, err := lib.SaveItem(vidDir, library.ItemMetadata{
		Version: 1,
		Site:    "bilibili",
		Kind:    "video",
		ID:      bvid,
		RawID:   bvid,
		Status:  "complete",
		Title:   "B站合作视频测试",
		Desc:    "合作简介",
		PubTs:   1730002000,
		Author: library.ItemAuthor{
			Site: "bilibili",
			Mid:  coMid,
			UID:  "9999001",
			Name: "第一投稿人",
		},
		Via: &library.ItemVia{
			Reason: library.ViaCollab,
			Site:   "bilibili",
			UID:    "1382015878",
			Name:   "合作成员",
		},
		Parts: []library.VideoPartRecord{{Page: 1, Duration: 90}},
	})
	if err != nil {
		t.Fatal(err)
	}

	app.RefreshData()
	// Inject legacy Mid-only subscription (Accounts intentionally empty) plus co-creator sub.
	app.Subscriptions = []subscription.Subscription{
		{ID: "sub_legacy_via", Name: "旧版成员订阅", Mid: viaMid},
		{ID: "sub_co_uploader", Name: "投稿人订阅", Mid: coMid},
	}
	app.subsRev++
	app.RestartHomeSession()
	app.Navigate(RouteHome)

	tt := ui.NewTester(app.View, 1280, 860)
	settle(tt, 2)

	post := app.feedEntryToPostCard(app.FeedItems[0])
	gotSub, ok := app.homePostSubscription(post)
	if !ok || gotSub.ID != "sub_legacy_via" {
		t.Fatalf("legacy Mid-only homePostSubscription = (%q, %v), want sub_legacy_via", gotSub.ID, ok)
	}
	if !tt.HasText("旧版成员订阅") {
		t.Fatal("expected legacy Mid-only subscription name 旧版成员订阅 on Home collab post")
	}
	avatarRect, ok := tt.Find("作者头像：" + saved.ItemKey)
	if !ok {
		t.Fatalf("missing avatar box for %s", saved.ItemKey)
	}
	assertColorClose(t, "Bilibili legacy Mid-only collab avatar", sampleCenterRGBA(t, tt.Image(), avatarRect), viaColor)
}

func TestCollabAvatarMultiAccountFallbackAndUnsubscribedClickAndWeiboFastRetweet(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	raw := app.backend.(*service.Backend)
	if _, err := raw.ImportCookie(context.Background(), "SESSDATA=synth_bili; bili_jct=csrf; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}

	avatarDir := t.TempDir()
	primaryColor := color.RGBA{R: 32, G: 200, B: 64, A: 255} // green: subscription primary account avatar
	primaryPNG := writeSolidPNG(t, avatarDir, "primary.png", primaryColor)

	const (
		biliPrimaryUID = "20088"
		dyViaUID       = "MS4wLjABAAAA_dy_no_local_avatar"
		dyCoUID        = "MS4wLjABAAAA_dy_cocreator"
		wbFwdUID       = "2000000002"
		wbOrigUID      = "3000000003"
	)

	stub := &collabAvatarStub{
		BackendAPI: raw,
		raw:        raw,
		avatars: map[string]string{
			"bilibili:" + biliPrimaryUID: primaryPNG,
		},
	}
	app.backend = stub

	// 1. Multi-account subscription: via Douyin account has NO local avatar,
	//    but subscription's primary Bilibili account DOES -> falls back to primary local avatar.
	subMulti, err := raw.CreateSubscription("跨站多账号订阅", "", []subscription.AccountRef{
		{Site: "bilibili", UID: biliPrimaryUID, Name: "主账号B站"},
		{Site: "douyin", UID: dyViaUID, Name: "副账号抖音"},
	}, subscription.SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatal(err)
	}
	subWb, err := raw.CreateSubscription("微博转发订阅", "", []subscription.AccountRef{
		{Site: "weibo", UID: wbFwdUID, Name: "转发博主"},
	}, subscription.SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatal(err)
	}

	lib := raw.LibraryStore()
	dyDir := filepath.Join(lib.CacheDir(), "douyin", "multi_hash", "works", "7399880002")
	_ = os.MkdirAll(dyDir, 0o755)
	savedMulti, err := lib.SaveItem(dyDir, library.ItemMetadata{
		Version: 1, Site: "douyin", Kind: "video", ID: "7399880002", RawID: "7399880002", Status: "complete",
		Title: "多账号回退头像共创", PubTs: 1730003000,
		Author: library.ItemAuthor{Site: "douyin", UID: dyCoUID, Name: "外部共创者"},
		Via:    &library.ItemVia{Reason: library.ViaCollab, Site: "douyin", UID: dyViaUID},
	})
	if err != nil {
		t.Fatal(err)
	}

	app.RefreshData()
	app.RestartHomeSession()
	app.Navigate(RouteHome)
	tt := ui.NewTester(app.View, 1280, 860)
	settle(tt, 3)

	avatarRect, ok := tt.Find("作者头像：" + savedMulti.ItemKey)
	if !ok {
		t.Fatalf("missing avatar box for %s", savedMulti.ItemKey)
	}
	assertColorClose(t, "multi-account primary fallback avatar", sampleCenterRGBA(t, tt.Image(), avatarRect), primaryColor)

	// Verify steady-frame negative caching: AccountAvatarPath should not be re-called every frame.
	stub.mu.Lock()
	callsAfterSettle := stub.avatarCalls["douyin:"+dyViaUID]
	stub.mu.Unlock()
	settle(tt, 5)
	stub.mu.Lock()
	callsAfterMoreFrames := stub.avatarCalls["douyin:"+dyViaUID]
	stub.mu.Unlock()
	if callsAfterMoreFrames != callsAfterSettle {
		t.Fatalf("AccountAvatarPath re-called on steady frames: before=%d after=%d", callsAfterSettle, callsAfterMoreFrames)
	}

	// 2. Weibo fast-retweet (ViaForward where viaUID != post.AuthorUID):
	//    homePostSubscription matches forwarder's subscription first.
	wbPost := PostCardData{
		Key:        "weibo:dynamic:5000000000000099",
		Site:       "weibo",
		Kind:       "dynamic",
		AuthorUID:  wbOrigUID,
		AuthorName: "原博主",
		InHomeFeed: true,
		LibItem: &library.ItemMetadata{
			Site: "weibo",
			Kind: "dynamic",
			Via: &library.ItemVia{
				Reason:    library.ViaForward,
				Site:      "weibo",
				UID:       wbFwdUID,
				Name:      "转发博主",
				DynamicID: "5000000000000099",
			},
		},
	}
	if gotWbSub, ok := app.homePostSubscription(wbPost); !ok || gotWbSub.ID != subWb.ID {
		t.Fatalf("Weibo fast-retweet homePostSubscription = (%q, %v), want %q", gotWbSub.ID, ok, subWb.ID)
	}

	// 3. Home collab post when !homeSubOK && !post.Offline: clicking avatar opens via's user page (not co-creator's).
	if err := raw.DeleteSubscription(subMulti.ID); err != nil {
		t.Fatal(err)
	}
	app.RefreshData()
	app.Navigate(RouteHome)
	settle(tt, 2)
	if err := tt.Click("作者头像：" + savedMulti.ItemKey); err != nil {
		t.Fatalf("click unsubscribed collab avatar on Home: %v", err)
	}
	tt.Frame()
	stub.mu.Lock()
	opened := append([]string(nil), stub.openedLinks...)
	stub.mu.Unlock()
	if len(opened) != 1 || !strings.Contains(opened[0], dyViaUID) || strings.Contains(opened[0], dyCoUID) {
		t.Fatalf("unsubscribed Home collab avatar opened %v, want via UID %s (not co-creator %s)", opened, dyViaUID, dyCoUID)
	}
}

func TestCollabAvatarAsyncDecodeDoesNotPrematurelyFallbackOrFetchRemote(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	raw := app.backend.(*service.Backend)
	if _, err := raw.ImportCookie(context.Background(), "SESSDATA=synth_bili; bili_jct=csrf; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}

	posted := make(chan func(), 64)
	app.dispatch = func(fn func()) { posted <- fn }

	avatarDir := t.TempDir()
	primaryColor := color.RGBA{R: 32, G: 200, B: 64, A: 255}
	viaColor := color.RGBA{R: 224, G: 32, B: 192, A: 255}
	primaryPNG := writeSolidPNG(t, avatarDir, "async_primary.png", primaryColor)
	viaPNG := writeSolidPNG(t, avatarDir, "async_via.png", viaColor)

	const (
		biliPrimaryUID = "20099"
		dyViaUID       = "MS4wLjABAAAA_dy_async_via"
		dyCoUID        = "MS4wLjABAAAA_dy_async_co"
	)

	stub := &collabAvatarStub{
		BackendAPI: raw,
		raw:        raw,
		avatars: map[string]string{
			"bilibili:" + biliPrimaryUID: primaryPNG,
			"douyin:" + dyViaUID:         viaPNG,
		},
	}
	app.backend = stub

	if _, err := raw.CreateSubscription("异步多账号订阅", "", []subscription.AccountRef{
		{Site: "bilibili", UID: biliPrimaryUID, Name: "主账号B站"},
		{Site: "douyin", UID: dyViaUID, Name: "副账号抖音"},
	}, subscription.SubRules{ScheduledCheck: true}); err != nil {
		t.Fatal(err)
	}

	lib := raw.LibraryStore()
	dyDir := filepath.Join(lib.CacheDir(), "douyin", "async_hash", "works", "7399880099")
	_ = os.MkdirAll(dyDir, 0o755)
	saved, err := lib.SaveItem(dyDir, library.ItemMetadata{
		Version: 1, Site: "douyin", Kind: "video", ID: "7399880099", RawID: "7399880099", Status: "complete",
		Title: "异步头像解码共创", PubTs: 1730004000,
		Author: library.ItemAuthor{Site: "douyin", UID: dyCoUID, Name: "共创发布者", Face: "https://example.invalid/co.jpg"},
		Via:    &library.ItemVia{Reason: library.ViaCollab, Site: "douyin", UID: dyViaUID, Name: "副账号抖音"},
	})
	if err != nil {
		t.Fatal(err)
	}

	app.RefreshData()
	app.RestartHomeSession()
	app.Navigate(RouteHome)

	tt := ui.NewTester(app.View, 1280, 860)
	// Frame 1 triggers async decode for viaPNG; pending state must prevent falling back to primaryPNG or remote FetchImage.
	tt.Frame()
	waitUIIdle(t, app, posted)
	tt.Frame()

	avatarRect, ok := tt.Find("作者头像：" + saved.ItemKey)
	if !ok {
		t.Fatalf("missing avatar box for %s", saved.ItemKey)
	}
	assertColorClose(t, "async decoded via avatar", sampleCenterRGBA(t, tt.Image(), avatarRect), viaColor)
	if stub.fetchImageCalls.Load() != 0 {
		t.Fatalf("async collab avatar must make 0 remote FetchImage calls, got %d", stub.fetchImageCalls.Load())
	}
}
