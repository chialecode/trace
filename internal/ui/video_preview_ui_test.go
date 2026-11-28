package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/egoist/mygo/ui"
)

type videoPreviewStub struct {
	service.BackendAPI
	raw *service.Backend

	mu               sync.Mutex
	stageCalls       []struct{ site, url, ext string }
	stagePath        string
	stageErr         error
	stageHook        func(ctx context.Context, siteID, rawURL, ext string) (string, error)
	cachedDouyin     []string
	cachedDouyinErr  error
	cacheDouyinHook  func(ctx context.Context, awemeID string) (download.Task, error)
	cachedDouyinFor  []struct{ id, uid, name string }
	cachedWeiboPosts []string
	cachedWeiboVid   []string
	cachedXPosts     []string
}

func newVideoPreviewHarness(t *testing.T) (*App, *videoPreviewStub, chan func(), func()) {
	t.Helper()
	app, _, cleanup := newTestUIApp(t)
	raw := app.backend.(*service.Backend)
	if _, err := raw.ImportCookie(context.Background(), "SESSDATA=synth_bili; bili_jct=csrf; DedeUserID=12345", ""); err != nil {
		cleanup()
		t.Fatalf("seed bilibili login: %v", err)
	}
	stub := &videoPreviewStub{
		BackendAPI: raw,
		raw:        raw,
		stageErr: &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindBlocked,
			Message: "视频体积超过临时预览上限（128 MiB），请点击「缓存到媒体库」完整下载后观看",
		},
	}
	app.backend = stub
	app.CompleteLogin()
	posted := make(chan func(), 64)
	app.dispatch = func(fn func()) { posted <- fn }
	return app, stub, posted, cleanup
}

func (s *videoPreviewStub) HasAnyLogin() bool { return true }

func (s *videoPreviewStub) StatusOf(siteID string) credential.Status {
	id := normalizeSiteIDUI(siteID)
	return credential.Status{
		Site:     id,
		LoggedIn: true,
		State:    "logged_in",
		Source:   "manual",
		UID:      "u_" + id,
		Uname:    "测试号_" + id,
	}
}

func (s *videoPreviewStub) Statuses() map[string]credential.Status {
	return map[string]credential.Status{
		string(site.Bilibili): s.StatusOf("bilibili"),
		string(site.Douyin):   s.StatusOf("douyin"),
		string(site.Weibo):    s.StatusOf("weibo"),
		string(site.X):        s.StatusOf("x"),
	}
}

func (s *videoPreviewStub) FetchImage(_ context.Context, _ string) ([]byte, error) {
	return nil, context.Canceled
}

func (s *videoPreviewStub) StagePreviewMedia(ctx context.Context, siteID, rawURL, ext string) (string, error) {
	s.mu.Lock()
	s.stageCalls = append(s.stageCalls, struct{ site, url, ext string }{site: siteID, url: rawURL, ext: ext})
	hook, p, err := s.stageHook, s.stagePath, s.stageErr
	s.mu.Unlock()
	if hook != nil {
		return hook(ctx, siteID, rawURL, ext)
	}
	if p != "" && err == nil {
		return p, nil
	}
	return "", err
}

func (s *videoPreviewStub) CacheDouyinWork(ctx context.Context, awemeID string) (download.Task, error) {
	s.mu.Lock()
	s.cachedDouyin = append(s.cachedDouyin, awemeID)
	hook, cErr := s.cacheDouyinHook, s.cachedDouyinErr
	s.mu.Unlock()
	if hook != nil {
		return hook(ctx, awemeID)
	}
	if cErr != nil {
		return download.Task{}, cErr
	}
	return download.Task{TaskID: "dy-" + awemeID, Site: "douyin", Kind: "video", TargetID: awemeID, Status: "queued"}, nil
}

func (s *videoPreviewStub) CacheDouyinWorkFor(_ context.Context, awemeID, forUID, forName string) (download.Task, error) {
	s.mu.Lock()
	s.cachedDouyinFor = append(s.cachedDouyinFor, struct{ id, uid, name string }{id: awemeID, uid: forUID, name: forName})
	s.mu.Unlock()
	return download.Task{TaskID: "dy-for-" + awemeID, Site: "douyin", Kind: "video", TargetID: awemeID, Status: "queued"}, nil
}

func (s *videoPreviewStub) CacheWeiboPost(_ context.Context, mid string, _ *library.ItemVia) (download.Task, error) {
	s.mu.Lock()
	s.cachedWeiboPosts = append(s.cachedWeiboPosts, mid)
	s.mu.Unlock()
	return download.Task{TaskID: "wb-" + mid, Site: "weibo", Kind: "dynamic", TargetID: mid, Status: "queued"}, nil
}

func (s *videoPreviewStub) CacheWeiboVideo(_ context.Context, oid string) (download.Task, error) {
	s.mu.Lock()
	s.cachedWeiboVid = append(s.cachedWeiboVid, oid)
	s.mu.Unlock()
	return download.Task{TaskID: "wbv-" + oid, Site: "weibo", Kind: "video", TargetID: oid, Status: "queued"}, nil
}

func (s *videoPreviewStub) CacheXPost(_ context.Context, statusID string, _ *library.ItemVia) (download.Task, error) {
	s.mu.Lock()
	s.cachedXPosts = append(s.cachedXPosts, statusID)
	s.mu.Unlock()
	return download.Task{TaskID: "x-" + statusID, Site: "x", Kind: "dynamic", TargetID: statusID, Status: "queued"}, nil
}

func TestDouyinUncachedVideoCoverClickOpensPreviewAndCachesToLibrary(t *testing.T) {
	app, stub, posted, cleanup := newVideoPreviewHarness(t)
	defer cleanup()

	const (
		awemeID  = "7399887766"
		videoURL = "https://v26-web.douyinvod.com/synth/short_preview.mp4"
	)
	app.CurrentRoute = RouteDouyinWork
	app.CurrentLibraryDetail = nil
	app.CurrentDouyinWork = &service.DouyinWorkView{
		ID:           awemeID,
		Kind:         "video",
		Title:        "高原星空短视频",
		Desc:         "高原星空短视频\n无水印高码率原片",
		CreateTime:   1710005000,
		DurationSec:  36,
		Author:       service.DouyinAuthorDTO{SecUID: "MS4wLjABAAAA_dy_preview", Nickname: "星空摄影师"},
		CoverURL:     "https://p3-sign.douyinpic.com/cover.jpg",
		VideoURL:     videoURL,
		Width:        1080,
		Height:       1920,
		CanonicalURL: "https://www.douyin.com/video/" + awemeID,
		CacheKey:     library.CanonicalItemKey("douyin", "video", awemeID),
	}

	tt := ui.NewTester(app.View, 1280, 900)
	tt.Frame()

	for _, lbl := range []string{"抖音视频封面播放", "抖音视频预览提示"} {
		if _, ok := tt.Find(lbl); !ok {
			t.Fatalf("expected uncached Douyin video control %q", lbl)
		}
	}

	// Clicking the cover stages preview media and enters PreviewMode in PlayerOverlay.
	if err := tt.Click("抖音视频封面播放"); err != nil {
		t.Fatalf("click Douyin cover: %v", err)
	}
	waitUIIdle(t, app, posted)
	tt.Frame()

	stub.mu.Lock()
	calls := append([]struct{ site, url, ext string }(nil), stub.stageCalls...)
	stub.mu.Unlock()
	if len(calls) != 1 || calls[0].site != "douyin" || calls[0].url != videoURL || calls[0].ext != ".mp4" {
		t.Fatalf("unexpected StagePreviewMedia calls: %+v", calls)
	}
	if !app.PlayerOverlay.Open || !app.PlayerOverlay.PreviewMode || app.PlayerOverlay.PreviewSite != "douyin" {
		t.Fatalf("expected PlayerOverlay Open=true PreviewMode=true site=douyin, got open=%v preview=%v site=%q",
			app.PlayerOverlay.Open, app.PlayerOverlay.PreviewMode, app.PlayerOverlay.PreviewSite)
	}
	for _, lbl := range []string{"临时预览标记", "临时预览缓存到媒体库", "临时预览错误说明", "预览失败缓存到媒体库"} {
		if _, ok := tt.Find(lbl); !ok {
			t.Fatalf("expected preview overlay control %q", lbl)
		}
	}

	// Open "..." menu in PreviewMode: MUST NOT expose library folder or external player buttons.
	app.PlayerOverlay.MoreMenuOpen = true
	tt.Frame()
	for _, forbidden := range []string{"播放层打开文件夹", "用系统播放器打开"} {
		if _, ok := tt.Find(forbidden); ok {
			t.Fatalf("PreviewMode more menu must hide %q to avoid leaking temp preview path", forbidden)
		}
	}
	app.PlayerOverlay.MoreMenuOpen = false
	tt.Frame()

	// Clicking top-bar "缓存到媒体库" triggers CacheDouyinWork and marks queued.
	if err := tt.Click("临时预览缓存到媒体库"); err != nil {
		t.Fatalf("click top-bar 临时预览缓存到媒体库: %v", err)
	}
	waitUIIdle(t, app, posted)
	stub.mu.Lock()
	dyCached := append([]string(nil), stub.cachedDouyin...)
	stub.mu.Unlock()
	if len(dyCached) != 1 || dyCached[0] != awemeID {
		t.Fatalf("expected CacheDouyinWork(%q), got %v", awemeID, dyCached)
	}
	if !app.PlayerOverlay.PreviewCacheQueued {
		t.Fatal("expected PreviewCacheQueued=true after clicking 缓存到媒体库")
	}

	// Closing overlay resets PreviewMode and cache callback completely.
	app.closePlayerOverlay()
	if app.PlayerOverlay.Open || app.PlayerOverlay.PreviewMode || app.PlayerOverlay.PreviewOnCache != nil || app.PlayerOverlay.PreviewCacheQueued {
		t.Fatalf("closePlayerOverlay did not reset preview state: %+v", app.PlayerOverlay)
	}

	// Paid/encrypted/uncacheable Douyin works MUST NOT preview or call StagePreviewMedia.
	app.CurrentDouyinWork.Uncacheable = true
	app.CurrentDouyinWork.UncacheableReason = "该作品为付费或加密内容，不支持缓存"
	tt.Frame()
	if _, ok := tt.Find("抖音视频预览提示"); ok {
		t.Fatal("uncacheable Douyin work must not show 抖音视频预览提示")
	}
	if err := tt.Click("抖音视频封面播放"); err != nil {
		t.Fatalf("click uncacheable cover: %v", err)
	}
	waitUIIdle(t, app, posted)
	stub.mu.Lock()
	afterUncacheable := len(stub.stageCalls)
	stub.mu.Unlock()
	if afterUncacheable != 1 || app.PlayerOverlay.PreviewMode {
		t.Fatalf("uncacheable work triggered StagePreviewMedia or PreviewMode: calls=%d preview=%v", afterUncacheable, app.PlayerOverlay.PreviewMode)
	}
	app.closeImageViewer()
}

func TestWeiboAndXUncachedVideoPreviewCacheCTAAndHLSExclusion(t *testing.T) {
	app, stub, posted, cleanup := newVideoPreviewHarness(t)
	defer cleanup()

	tt := ui.NewTester(app.View, 1280, 900)

	// 1. Weibo uncached single-file MP4 post preview -> PreviewMode + cache CTA triggers CacheWeiboPost.
	const wbMid = "5000000000009001"
	app.CurrentRoute = RouteWeiboPost
	app.CurrentWeiboVideo = nil
	app.CurrentWeiboPost = &service.WeiboPostView{
		Mid:       wbMid,
		Author:    service.WeiboAuthorDTO{UID: "5001", ScreenName: "微博短视频博主"},
		CreatedAt: 1710005100,
		Text:      "测试微博短视频零缓存预览",
		Video: &service.WeiboVideoDTO{
			ObjectID:     "1034:9001",
			Title:        "微博测试短视频",
			DurationSec:  28,
			VideoURL:     "https://f.video.weibocdn.com/synth_9001.mp4",
			QualityLabel: "1080P",
			Codec:        "avc",
		},
		CanonicalURL: "https://weibo.com/5001/" + wbMid,
		CacheKey:     library.CanonicalItemKey("weibo", "dynamic", wbMid),
	}
	tt.Frame()
	if err := tt.Click("微博视频卡片"); err != nil {
		t.Fatalf("click Weibo video card: %v", err)
	}
	waitUIIdle(t, app, posted)
	tt.Frame()
	if !app.PlayerOverlay.Open || !app.PlayerOverlay.PreviewMode || app.PlayerOverlay.PreviewSite != "weibo" {
		t.Fatalf("Weibo preview state unexpected: open=%v preview=%v site=%q",
			app.PlayerOverlay.Open, app.PlayerOverlay.PreviewMode, app.PlayerOverlay.PreviewSite)
	}
	if err := tt.Click("预览失败缓存到媒体库"); err != nil {
		t.Fatalf("click error-card 缓存到媒体库 for Weibo: %v", err)
	}
	waitUIIdle(t, app, posted)
	stub.mu.Lock()
	wbCached := append([]string(nil), stub.cachedWeiboPosts...)
	stub.mu.Unlock()
	if len(wbCached) != 1 || wbCached[0] != wbMid {
		t.Fatalf("expected CacheWeiboPost(%q), got %v", wbMid, wbCached)
	}
	app.closePlayerOverlay()

	// 2. Weibo standalone 1034: video preview -> cache CTA triggers CacheWeiboVideo.
	const wbOID = "1034:9002"
	app.CurrentWeiboPost = nil
	app.CurrentWeiboVideo = &service.WeiboVideoView{
		ObjectID:     wbOID,
		Title:        "微博独立短视频",
		AuthorUID:    "5002",
		AuthorName:   "独立视频博主",
		CanonicalURL: "https://weibo.com/tv/show/" + wbOID,
		CacheKey:     library.CanonicalItemKey("weibo", "video", "1034_9002"),
		Video: &service.WeiboVideoDTO{
			ObjectID:    wbOID,
			Title:       "微博独立短视频",
			DurationSec: 40,
			VideoURL:    "https://f.video.weibocdn.com/synth_9002.mp4",
		},
	}
	tt.Frame()
	if err := tt.Click("微博视频卡片"); err != nil {
		t.Fatalf("click standalone Weibo video card: %v", err)
	}
	waitUIIdle(t, app, posted)
	tt.Frame()
	if err := tt.Click("临时预览缓存到媒体库"); err != nil {
		t.Fatalf("click top-bar 缓存到媒体库 for standalone Weibo video: %v", err)
	}
	waitUIIdle(t, app, posted)
	stub.mu.Lock()
	wbVidCached := append([]string(nil), stub.cachedWeiboVid...)
	stub.mu.Unlock()
	if len(wbVidCached) != 1 || wbVidCached[0] != wbOID {
		t.Fatalf("expected CacheWeiboVideo(%q), got %v", wbOID, wbVidCached)
	}
	app.closePlayerOverlay()

	// 3. X uncached non-HLS MP4 preview -> PreviewMode + cache CTA triggers CacheXPost.
	const xPostID = "1800000000000009003"
	app.CurrentRoute = RouteXPost
	app.CurrentXPost = &service.XPostDTO{
		ID:        xPostID,
		Author:    service.XAuthorDTO{RestID: "9003", ScreenName: "x_short_vid", Name: "X短视频作者"},
		CreatedAt: 1710005200,
		Text:      "测试 X 单文件 MP4 零缓存预览",
		Media: []service.XMediaItemDTO{
			{
				Index:       1,
				Kind:        "video",
				DurationSec: 22,
				VideoURL:    "https://video.twimg.com/ext_tw_video/9003/pu/vid/1280x720/clip.mp4",
				Width:       1280,
				Height:      720,
			},
		},
		CanonicalURL: "https://x.com/x_short_vid/status/" + xPostID,
		CacheKey:     library.CanonicalItemKey("x", "dynamic", xPostID),
	}
	tt.Frame()
	if err := tt.Click("X视频卡片"); err != nil {
		t.Fatalf("click X video card: %v", err)
	}
	waitUIIdle(t, app, posted)
	tt.Frame()
	if !app.PlayerOverlay.Open || !app.PlayerOverlay.PreviewMode || app.PlayerOverlay.PreviewSite != "x" {
		t.Fatalf("X preview state unexpected: open=%v preview=%v site=%q",
			app.PlayerOverlay.Open, app.PlayerOverlay.PreviewMode, app.PlayerOverlay.PreviewSite)
	}
	if err := tt.Click("临时预览缓存到媒体库"); err != nil {
		t.Fatalf("click top-bar 缓存到媒体库 for X: %v", err)
	}
	waitUIIdle(t, app, posted)
	stub.mu.Lock()
	xCached := append([]string(nil), stub.cachedXPosts...)
	stageBeforeHLS := len(stub.stageCalls)
	stub.mu.Unlock()
	if len(xCached) != 1 || xCached[0] != xPostID {
		t.Fatalf("expected CacheXPost(%q), got %v", xPostID, xCached)
	}
	app.closePlayerOverlay()

	// 4. X HLS-only video MUST remain excluded from preview (0 StagePreviewMedia calls, HLS notice shown).
	app.CurrentXPost = &service.XPostDTO{
		ID:        "1800000000000009004",
		Author:    service.XAuthorDTO{RestID: "9004", ScreenName: "x_hls_only", Name: "X分段流作者"},
		CreatedAt: 1710005300,
		Text:      "仅有 HLS 分段流的帖子",
		Media: []service.XMediaItemDTO{
			{
				Index:         1,
				Kind:          "video",
				HLSOnly:       true,
				HLSOnlyNotice: "这条视频只有分段流，暂不支持缓存",
				DurationSec:   50,
			},
		},
		CanonicalURL: "https://x.com/x_hls_only/status/1800000000000009004",
	}
	tt.Frame()
	if _, ok := tt.Find("X视频卡片"); ok {
		t.Fatal("X HLS-only video must not render clickable preview card X视频卡片")
	}
	if _, ok := tt.Find("X分段流提示"); !ok {
		t.Fatal("expected HLS-only notice X分段流提示")
	}
	stub.mu.Lock()
	stageAfterHLS := len(stub.stageCalls)
	stub.mu.Unlock()
	if stageAfterHLS != stageBeforeHLS {
		t.Fatalf("X HLS-only post triggered %d StagePreviewMedia calls, want 0", stageAfterHLS-stageBeforeHLS)
	}
}

func TestPlayerOverlayPreviewResetOfflineLibraryAndResponsiveWidths(t *testing.T) {
	app, stub, posted, cleanup := newVideoPreviewHarness(t)
	defer cleanup()

	// Seed a real local library item for offline /library/item and switch-from-preview checks.
	lib := stub.raw.LibraryStore()
	itemDir := filepath.Join(lib.CacheDir(), "douyin", "dy_local_1", "videos", "7399000001")
	if err := os.MkdirAll(itemDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(itemDir, "video.mp4"), []byte("synthetic-local-mp4"), 0o600); err != nil {
		t.Fatal(err)
	}
	saved, err := lib.SaveItem(itemDir, library.ItemMetadata{
		Version:  1,
		ID:       "video:7399000001",
		RawID:    "7399000001",
		Site:     "douyin",
		Kind:     "video",
		Status:   "complete",
		Title:    "已缓存的本机抖音视频",
		URL:      "https://www.douyin.com/video/7399000001",
		PubTs:    1710006000,
		Duration: 30,
		Author:   library.ItemAuthor{Site: "douyin", UID: "dy_local_1", Name: "离线作者"},
		Media:    []library.MediaFile{{Role: "video", Path: "video.mp4", Duration: 30}},
	})
	if err != nil {
		t.Fatalf("SaveItem: %v", err)
	}

	// 1. Offline /library/item click opens local player with PreviewMode=false and 0 StagePreviewMedia calls.
	app.OpenLibraryItemDetail(saved)
	tt := ui.NewTester(app.View, 1280, 900)
	tt.Frame()
	if _, ok := tt.Find("抖音视频预览提示"); ok {
		t.Fatal("offline /library/item must never show online preview hint")
	}
	if err := tt.Click("抖音视频封面播放"); err != nil {
		t.Fatalf("click offline library cover: %v", err)
	}
	waitUIIdle(t, app, posted)
	if !app.PlayerOverlay.Open || app.PlayerOverlay.PreviewMode {
		t.Fatalf("offline library playback expected Open=true PreviewMode=false, got open=%v preview=%v",
			app.PlayerOverlay.Open, app.PlayerOverlay.PreviewMode)
	}
	stub.mu.Lock()
	offlineStages := len(stub.stageCalls)
	stub.mu.Unlock()
	if offlineStages != 0 {
		t.Fatalf("offline /library/item issued %d StagePreviewMedia calls, want 0", offlineStages)
	}

	// 2. Switching from PreviewMode directly into a cached library item resets PreviewMode and restores menu actions.
	app.PlayerOverlay.PreviewMode = true
	app.PlayerOverlay.PreviewSite = "douyin"
	app.PlayerOverlay.PreviewOnCache = func(context.Context) error { return nil }
	app.OpenPlayerOverlay(saved)
	waitUIIdle(t, app, posted)
	if app.PlayerOverlay.PreviewMode || app.PlayerOverlay.PreviewOnCache != nil {
		t.Fatal("OpenPlayerOverlay(saved) did not reset PreviewMode state")
	}
	app.PlayerOverlay.MoreMenuOpen = true
	tt.Frame()
	for _, want := range []string{"播放层打开文件夹", "用系统播放器打开"} {
		if _, ok := tt.Find(want); !ok {
			t.Fatalf("cached library playback menu should restore %q", want)
		}
	}
	app.closePlayerOverlay()

	// 3. DecoderMissing in PreviewMode offers 缓存到媒体库 instead of external player.
	app.PlayerOverlay.Open = true
	app.PlayerOverlay.PreviewMode = true
	app.PlayerOverlay.Title = "缺失解码器的临时预览"
	app.PlayerOverlay.MediaPath = filepath.Join(t.TempDir(), "preview_douyin_temp.mp4")
	app.PlayerOverlay.DecoderMissing = true
	app.PlayerOverlay.MissingReason = "系统缺少 AV1 解码器"
	cachedCalled := false
	app.PlayerOverlay.PreviewOnCache = func(context.Context) error { cachedCalled = true; return nil }
	tt.Frame()
	if _, ok := tt.Find("缺失解码器时用系统播放器打开"); ok {
		t.Fatal("PreviewMode DecoderMissing must not expose system player on temp preview file")
	}
	if err := tt.Click("缺失解码器时缓存到媒体库"); err != nil {
		t.Fatalf("click 缺失解码器时缓存到媒体库: %v", err)
	}
	waitUIIdle(t, app, posted)
	if !cachedCalled || !app.PlayerOverlay.PreviewCacheQueued {
		t.Fatalf("expected DecoderMissing preview cache button to invoke PreviewOnCache and set queued, called=%v queued=%v", cachedCalled, app.PlayerOverlay.PreviewCacheQueued)
	}
	app.closePlayerOverlay()

	// 4. Responsive widths 720, 1000, 1280: zero horizontal overflow in Douyin uncached detail & PreviewMode overlay.
	app.CurrentRoute = RouteDouyinWork
	app.CurrentLibraryDetail = nil
	app.CurrentDouyinWork = &service.DouyinWorkView{
		ID:          "7399000099",
		Kind:        "video",
		Title:       "一段非常长的抖音短视频标题用于检验窄窗与宽窗下临时预览标记和缓存按钮零横向溢出",
		DurationSec: 45,
		Author:      service.DouyinAuthorDTO{SecUID: "MS4wLjABAAAA_resp", Nickname: "响应式测试作者"},
		VideoURL:    "https://v26-web.douyinvod.com/synth/responsive.mp4",
		Width:       1080,
		Height:      1440,
	}
	for _, w := range []int{720, 1000, 1280} {
		wf := float32(w)
		// Uncached Douyin detail page
		app.PlayerOverlay.Open = false
		tester := ui.NewTester(app.View, w, 760)
		tester.Frame()
		for _, lbl := range []string{"抖音视频封面播放", "抖音视频预览提示"} {
			r, ok := tester.Find(lbl)
			if !ok {
				t.Fatalf("width %d: missing %q", w, lbl)
			}
			if r.X < -0.5 || r.X+r.W > wf+0.5 {
				t.Fatalf("width %d: %q overflowed horizontally: rect=%+v", w, lbl, r)
			}
		}

		// PlayerOverlay in PreviewMode with long title + error card
		app.PlayerOverlay.Open = true
		app.PlayerOverlay.PreviewMode = true
		app.PlayerOverlay.Title = app.CurrentDouyinWork.Title
		app.PlayerOverlay.LoadError = "无法预览此视频：视频体积超过临时预览上限（128 MiB），请点击「缓存到媒体库」完整下载后观看"
		app.PlayerOverlay.PreviewOnCache = func(context.Context) error { return nil }
		app.PlayerOverlay.PreviewCacheQueued = false
		app.PlayerOverlay.ControlsHidden = false
		app.PlayerOverlay.LastActiveAt = time.Time{}
		tester.Frame()
		for _, lbl := range []string{"临时预览标记", "临时预览缓存到媒体库", "播放器更多菜单", "关闭播放", "临时预览错误说明", "预览失败缓存到媒体库"} {
			r, ok := tester.Find(lbl)
			if !ok {
				t.Fatalf("width %d: missing overlay control %q", w, lbl)
			}
			if r.X < -0.5 || r.X+r.W > wf+0.5 {
				t.Fatalf("width %d: overlay control %q overflowed horizontally: rect=%+v", w, lbl, r)
			}
		}
	}
	app.closePlayerOverlay()
}

func TestPlayerOverlayPreviewCacheFailureShowsErrorAndAllowsRetry(t *testing.T) {
	app, stub, posted, cleanup := newVideoPreviewHarness(t)
	defer cleanup()

	const awemeID = "7399887799"
	cacheStarted := make(chan struct{}, 2)
	cacheRelease := make(chan struct{})
	var releaseOnce sync.Once
	releaseGate := func() { releaseOnce.Do(func() { close(cacheRelease) }) }
	t.Cleanup(releaseGate)

	var attempt int
	stub.cacheDouyinHook = func(ctx context.Context, id string) (download.Task, error) {
		attempt++
		cur := attempt
		if cur == 1 {
			cacheStarted <- struct{}{}
			select {
			case <-cacheRelease:
			case <-ctx.Done():
				return download.Task{}, ctx.Err()
			}
			return download.Task{}, &site.Error{Site: site.Douyin, Kind: site.ErrKindAuthRequired, Message: "抖音登录已失效，请重新粘贴 Cookie"}
		}
		return download.Task{TaskID: "dy-retry-" + id, Site: "douyin", Kind: "video", TargetID: id, Status: "queued"}, nil
	}

	app.CurrentRoute = RouteDouyinWork
	app.CurrentLibraryDetail = nil
	app.CurrentDouyinWork = &service.DouyinWorkView{
		ID:          awemeID,
		Kind:        "video",
		Title:       "缓存失败可重试的临时预览",
		DurationSec: 25,
		Author:      service.DouyinAuthorDTO{SecUID: "MS4wLjABAAAA_retry", Nickname: "测试作者"},
		VideoURL:    "https://v26-web.douyinvod.com/synth/retry.mp4",
		Width:       1080,
		Height:      1920,
		// Partial cached metadata without any local video file must still permit online temporary preview.
		CachedItem: &library.ItemMetadata{
			ItemKey: library.CanonicalItemKey("douyin", "video", awemeID),
			Site:    "douyin",
			Kind:    "video",
			Status:  "partial",
			Media:   []library.MediaFile{{Role: "cover", Path: "cover.jpg"}},
		},
	}

	tt := ui.NewTester(app.View, 1280, 900)
	tt.Frame()
	if _, ok := tt.Find("抖音视频预览提示"); !ok {
		t.Fatal("expected Douyin partial item without local video file to allow temporary preview")
	}
	if err := tt.Click("抖音视频封面播放"); err != nil {
		t.Fatalf("click cover: %v", err)
	}
	waitUIIdle(t, app, posted)
	tt.Frame()

	// Click "缓存到媒体库": while in flight, PreviewCacheQueued MUST remain false (no optimistic lie).
	if err := tt.Click("临时预览缓存到媒体库"); err != nil {
		t.Fatalf("click 临时预览缓存到媒体库 (attempt 1): %v", err)
	}
	select {
	case <-cacheStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for async CacheDouyinWork to start")
	}
	if app.PlayerOverlay.PreviewCacheQueued {
		t.Fatal("PreviewCacheQueued must not be set optimistically before async enqueue succeeds")
	}
	if !app.previewCacheInFlight() {
		t.Fatal("expected previewCacheInFlight() == true while async enqueue is running")
	}

	// Release first attempt -> fails with auth_required -> error surfaced inside overlay & button stays retryable!
	releaseGate()
	waitUIIdle(t, app, posted)
	tt.Frame()
	if app.PlayerOverlay.PreviewCacheQueued {
		t.Fatal("PreviewCacheQueued must remain false when cache enqueue fails")
	}
	if !strings.Contains(app.PlayerOverlay.PreviewCacheError, "抖音登录已失效") {
		t.Fatalf("expected sanitized overlay cache error, got %q", app.PlayerOverlay.PreviewCacheError)
	}
	if _, ok := tt.Find("临时预览缓存失败提示"); !ok {
		t.Fatal("expected overlay-visible error element 临时预览缓存失败提示")
	}

	// Also verify overlay error banner renders when LoadError == "" (playing/loading preview).
	app.PlayerOverlay.LoadError = ""
	tt.Frame()
	if _, ok := tt.Find("临时预览缓存失败提示"); !ok {
		t.Fatal("expected floating overlay error banner 临时预览缓存失败提示 when LoadError is empty")
	}

	// Retry click -> second attempt succeeds -> PreviewCacheQueued becomes true and error clears!
	if err := tt.Click("临时预览缓存到媒体库"); err != nil {
		t.Fatalf("click 临时预览缓存到媒体库 (attempt 2 retry): %v", err)
	}
	waitUIIdle(t, app, posted)
	tt.Frame()
	if !app.PlayerOverlay.PreviewCacheQueued || app.PlayerOverlay.PreviewCacheError != "" {
		t.Fatalf("expected queued=true and empty error after retry success, got queued=%v err=%q",
			app.PlayerOverlay.PreviewCacheQueued, app.PlayerOverlay.PreviewCacheError)
	}
	if app.BannerInfo != "已加入缓存任务" {
		t.Fatalf("expected BannerInfo 已加入缓存任务, got %q", app.BannerInfo)
	}
	app.closePlayerOverlay()
}

func TestPlayerOverlayPreviewCancelsInFlightStageOnCloseAndSwitch(t *testing.T) {
	app, stub, posted, cleanup := newVideoPreviewHarness(t)
	defer cleanup()

	ctxCh := make(chan context.Context, 4)
	stub.stageHook = func(ctx context.Context, _, _, _ string) (string, error) {
		ctxCh <- ctx
		<-ctx.Done()
		return "", ctx.Err()
	}

	waitCtx := func(label string) context.Context {
		t.Helper()
		select {
		case c := <-ctxCh:
			return c
		case <-time.After(3 * time.Second):
			t.Fatalf("%s: timed out waiting for StagePreviewMedia to start", label)
			return nil
		}
	}
	assertCanceled := func(label string, ctx context.Context) {
		t.Helper()
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
			t.Fatalf("%s: expected StagePreviewMedia ctx to be canceled", label)
		}
	}

	// 1. Closing PlayerOverlay mid-preview cancels StagePreviewMedia ctx immediately.
	app.openRemoteVideoPreview("douyin", "预览 1", "https://v26-web.douyinvod.com/synth/slow1.mp4")
	ctx1 := waitCtx("close-mid-preview")
	app.closePlayerOverlay()
	assertCanceled("close-mid-preview", ctx1)
	waitUIIdle(t, app, posted)
	if app.PlayerOverlay.Open || app.PlayerOverlay.PreviewMode {
		t.Fatalf("expected closed overlay after mid-preview close, got open=%v preview=%v", app.PlayerOverlay.Open, app.PlayerOverlay.PreviewMode)
	}

	// 2. Opening a second preview cancels the first preview's ctx immediately.
	app.openRemoteVideoPreview("douyin", "预览 2", "https://v26-web.douyinvod.com/synth/slow2.mp4")
	ctx2 := waitCtx("switch-preview-first")
	app.openRemoteVideoPreview("weibo", "预览 3", "https://f.video.weibocdn.com/synth/slow3.mp4")
	assertCanceled("switch-preview-first", ctx2)
	ctx3 := waitCtx("switch-preview-second")

	// 3. Switching to a cached library item cancels the active preview ctx immediately and resets PreviewMode.
	app.OpenPlayerOverlay(library.ItemMetadata{
		ItemKey: "douyin:video:empty",
		Title:   "本地视频切换",
	})
	assertCanceled("switch-to-library", ctx3)
	waitUIIdle(t, app, posted)
	if app.PlayerOverlay.PreviewMode {
		t.Fatal("expected PreviewMode == false after switching to library playback")
	}
	app.closePlayerOverlay()
}

func TestPlayerOverlayPreviewErrorRendersWithoutDecoder(t *testing.T) {
	app, _, posted, cleanup := newVideoPreviewHarness(t)
	defer cleanup()

	// 1. Simulate a Linux/non-MF environment where a prior open left DecoderMissing=true
	//    and then a new temporary preview fails at StagePreviewMedia (oversize > 128 MiB).
	app.PlayerOverlay.Open = true
	app.PlayerOverlay.DecoderMissing = true
	app.PlayerOverlay.MissingReason = "media decoder missing"

	cachedCount := 0
	app.openRemoteVideoPreviewWithCache("douyin", "超限短视频", "https://v26-web.douyinvod.com/synth/oversize.mp4", false, previewCacheAction{
		ItemKey:    "douyin:video:oversize",
		PendingKey: "preview-cache-oversize",
		Enqueue: func(context.Context) error {
			cachedCount++
			return nil
		},
	})
	waitUIIdle(t, app, posted)

	if app.PlayerOverlay.DecoderMissing {
		t.Fatal("non-codec StagePreviewMedia error must clear DecoderMissing")
	}
	if app.PlayerOverlay.MissingReason != "" {
		t.Fatalf("non-codec StagePreviewMedia error must clear MissingReason, got %q", app.PlayerOverlay.MissingReason)
	}

	// 2. Also verify LoadError renders cleanly across 720/1000/1280 when a.player == nil
	//    (no platform decoder controller) and the error-card cache CTA remains clickable.
	origPlayer := app.player
	app.player = nil
	t.Cleanup(func() { app.player = origPlayer })

	for _, w := range []int{720, 1000, 1280} {
		wf := float32(w)
		tester := ui.NewTester(app.View, w, 760)
		tester.Frame()
		for _, lbl := range []string{"临时预览标记", "临时预览缓存到媒体库", "临时预览错误说明", "预览失败缓存到媒体库"} {
			r, ok := tester.Find(lbl)
			if !ok {
				t.Fatalf("width %d (player=nil): missing %q", w, lbl)
			}
			if r.X < -0.5 || r.X+r.W > wf+0.5 {
				t.Fatalf("width %d (player=nil): %q overflowed horizontally: rect=%+v", w, lbl, r)
			}
		}
		if w == 1280 {
			if err := tester.Click("预览失败缓存到媒体库"); err != nil {
				t.Fatalf("click 预览失败缓存到媒体库 with player=nil: %v", err)
			}
			waitUIIdle(t, app, posted)
		}
	}
	if cachedCount != 1 || !app.PlayerOverlay.PreviewCacheQueued {
		t.Fatalf("expected error-card cache CTA to succeed with player=nil, count=%d queued=%v", cachedCount, app.PlayerOverlay.PreviewCacheQueued)
	}
	app.closePlayerOverlay()
}
