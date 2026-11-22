package ui

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/player"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/chialecode/trace/internal/uistate"
	"github.com/egoist/mygo/ui"
)

type s1TrackingBackend struct {
	service.BackendAPI
	raw *service.Backend

	fetchImageCalls atomic.Int32
	liveQueryCalls  atomic.Int32
	feedPageCalls   atomic.Int32

	mu               sync.Mutex
	dyLoggedIn       bool
	rooms            map[int64]live.RoomState
	accounts         map[string]service.LiveAccountState
	startedBiliRooms []int64
	stoppedBiliRooms []int64
	startedAcctRooms []string
	stoppedAcctKeys  []string
	openedLiveURLs   []string
	openedLinks      []string
	localAvatars     map[string]string
}

func (b *s1TrackingBackend) StatusOf(siteID string) credential.Status {
	if strings.EqualFold(strings.TrimSpace(siteID), string(site.Douyin)) {
		b.mu.Lock()
		loggedIn := b.dyLoggedIn
		b.mu.Unlock()
		if loggedIn {
			return credential.Status{
				Site:     string(site.Douyin),
				LoggedIn: true,
				State:    "active",
				SecUID:   "MS4wLjABAAAA_self",
				Uname:    "抖音测试号",
			}
		}
	}
	return b.BackendAPI.StatusOf(siteID)
}

func (b *s1TrackingBackend) FetchImage(ctx context.Context, rawURL string) ([]byte, error) {
	b.fetchImageCalls.Add(1)
	return b.BackendAPI.FetchImage(ctx, rawURL)
}

func (b *s1TrackingBackend) AccountAvatarPath(siteID, uid string) string {
	b.mu.Lock()
	p := ""
	if b.localAvatars != nil {
		p = b.localAvatars[strings.ToLower(strings.TrimSpace(siteID))+":"+strings.TrimSpace(uid)]
	}
	b.mu.Unlock()
	if p != "" {
		return p
	}
	return b.BackendAPI.AccountAvatarPath(siteID, uid)
}

func (b *s1TrackingBackend) FeedPage(req library.FeedPageRequest) (library.FeedPageResult, error) {
	b.feedPageCalls.Add(1)
	if len(req.Authors) == 0 && len(req.AuthorRefs) == 0 {
		return library.FeedPageResult{}, fmt.Errorf("unexpected unfiltered FeedPage query without AuthorRefs")
	}
	return b.BackendAPI.FeedPage(req)
}

func (b *s1TrackingBackend) LiveStatus(ctx context.Context, uids []int64) ([]live.RoomState, error) {
	b.liveQueryCalls.Add(1)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.rooms != nil {
		var out []live.RoomState
		for _, id := range uids {
			if rm, ok := b.rooms[id]; ok {
				out = append(out, rm)
			} else {
				out = append(out, live.RoomState{UID: id})
			}
		}
		return out, nil
	}
	return b.BackendAPI.LiveStatus(ctx, uids)
}

func (b *s1TrackingBackend) LiveStatuses(ctx context.Context, accountKeys []string) ([]service.LiveAccountState, error) {
	b.liveQueryCalls.Add(1)
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []service.LiveAccountState
	for _, k := range accountKeys {
		if st, ok := b.accounts[k]; ok {
			out = append(out, st)
		}
	}
	return out, nil
}

func (b *s1TrackingBackend) ListLiveRooms() []live.RoomState {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.rooms != nil {
		out := make([]live.RoomState, 0, len(b.rooms))
		for _, rm := range b.rooms {
			out = append(out, rm)
		}
		return out
	}
	return b.BackendAPI.ListLiveRooms()
}

func (b *s1TrackingBackend) ListLiveAccounts() []service.LiveAccountState {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.accounts != nil {
		out := make([]service.LiveAccountState, 0, len(b.accounts))
		for _, st := range b.accounts {
			out = append(out, st)
		}
		return out
	}
	return b.BackendAPI.ListLiveAccounts()
}

func (b *s1TrackingBackend) StartLiveRecording(_ context.Context, roomID int64) (live.RoomState, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.startedBiliRooms = append(b.startedBiliRooms, roomID)
	for uid, rm := range b.rooms {
		if rm.RoomID == roomID {
			rm.Recording = true
			rm.RecordDurationSec = 65
			rm.RecordBytes = 12 * 1024 * 1024
			b.rooms[uid] = rm
			return rm, nil
		}
	}
	return live.RoomState{RoomID: roomID, LiveStatus: 1, Recording: true, RecordDurationSec: 65, RecordBytes: 12 * 1024 * 1024}, nil
}

func (b *s1TrackingBackend) StopLiveRecording(roomID int64) (live.RoomState, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stoppedBiliRooms = append(b.stoppedBiliRooms, roomID)
	for uid, rm := range b.rooms {
		if rm.RoomID == roomID {
			rm.Recording = false
			b.rooms[uid] = rm
			return rm, nil
		}
	}
	return live.RoomState{RoomID: roomID, LiveStatus: 1, Recording: false}, nil
}

func (b *s1TrackingBackend) StartLiveRecordingByRoom(_ context.Context, siteID, roomOrWebRID string) (service.LiveAccountState, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.startedAcctRooms = append(b.startedAcctRooms, siteID+":"+roomOrWebRID)
	for k, st := range b.accounts {
		if strings.EqualFold(st.Site, siteID) && (st.WebRID == roomOrWebRID || st.RoomID == roomOrWebRID) {
			st.Recording = true
			st.RecordDurationSec = 95
			st.RecordBytes = 24 * 1024 * 1024
			b.accounts[k] = st
			return st, nil
		}
	}
	return service.LiveAccountState{Site: siteID, WebRID: roomOrWebRID, LiveStatus: 1, Recording: true}, nil
}

func (b *s1TrackingBackend) StopLiveRecordingByKey(recordingOrAccountKey string) (service.LiveAccountState, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stoppedAcctKeys = append(b.stoppedAcctKeys, recordingOrAccountKey)
	for k, st := range b.accounts {
		if st.RecordingKey == recordingOrAccountKey || st.AccountKey == recordingOrAccountKey || k == recordingOrAccountKey {
			st.Recording = false
			b.accounts[k] = st
			return st, nil
		}
	}
	return service.LiveAccountState{AccountKey: recordingOrAccountKey, LiveStatus: 1, Recording: false}, nil
}

func (b *s1TrackingBackend) OpenLiveRoomInBrowser(roomID int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.openedLiveURLs = append(b.openedLiveURLs, fmt.Sprintf("bilibili:%d", roomID))
	return nil
}

func (b *s1TrackingBackend) OpenLiveInBrowser(siteID, roomOrWebRID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.openedLiveURLs = append(b.openedLiveURLs, siteID+":"+roomOrWebRID)
	return nil
}

func (b *s1TrackingBackend) OpenLink(_ context.Context, raw string) (*service.OpenLinkResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.openedLinks = append(b.openedLinks, raw)
	return &service.OpenLinkResult{
		Site:       string(site.Douyin),
		SiteTarget: &site.ResolveTarget{Site: site.Douyin, Kind: "user", ID: "MS4wLjABAAAA_dy_live", SecUID: "MS4wLjABAAAA_dy_live"},
		DouyinUser: &service.DouyinUserView{
			SecUID:   "MS4wLjABAAAA_dy_live",
			Nickname: "抖音主播",
		},
	}, nil
}

// Subscription detail defaults to "内容", renders cached items across all linked
// Bilibili + Douyin accounts (including live recordings) with virtualized pagination and
// strictly 0 site HTTP requests, shows proper empty state without extra primary buttons,
// and guards 0-account subscriptions from querying the entire library (DEC-051 ⑦).
func TestSubscriptionDetailContentTabCrossSiteZeroRequestsAndEmptyState(t *testing.T) {
	var siteHTTPRequests atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/x/web-interface/nav", func(w http.ResponseWriter, r *http.Request) {
		siteHTTPRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"code":0,"data":{"isLogin":true,"mid":12345,"uname":"测试用户"}}`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		siteHTTPRequests.Add(1)
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	root := t.TempDir()
	loc := appdirs.Locations{
		Channel:         "dev",
		Mode:            "dev",
		AppName:         "trace-dev",
		DisplayName:     "TRACE Dev",
		DataDir:         filepath.Join(root, "data"),
		DefaultCacheDir: filepath.Join(root, "cache"),
		CredentialsDir:  filepath.Join(root, "data", "credentials"),
		LogsDir:         filepath.Join(root, "data", "logs"),
		LockFile:        filepath.Join(root, "data", "app.lock"),
	}
	rawBackend, err := service.NewBackend(service.Options{
		Locations: loc,
		BilibiliOpts: bilibili.ClientOptions{
			Endpoints: bilibili.Endpoints{
				APIBase:      srv.URL,
				PassportBase: srv.URL,
				WWWBase:      srv.URL,
				SpaceBase:    srv.URL,
				DynamicBase:  srv.URL,
				LiveAPIBase:  srv.URL,
				LiveWebBase:  srv.URL,
			},
			PacingInterval: time.Millisecond,
			AllowLocalhost: true,
		},
		Desktop: service.NewMockDesktop(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rawBackend.Close()

	if _, err := rawBackend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}

	tracker := &s1TrackingBackend{
		BackendAPI: rawBackend,
		raw:        rawBackend,
		dyLoggedIn: true,
	}
	app := NewApp(tracker, player.NewNullAudioSink())
	defer app.Close()

	posted := make(chan func(), 64)
	app.dispatch = func(fn func()) { posted <- fn }

	// Create cross-site subscription (Bilibili + Douyin), empty cached subscription, and 0-account subscription.
	subCross, err := rawBackend.CreateSubscription("跨站创作者甲", "同时运营B站与抖音", []subscription.AccountRef{
		{Site: "bilibili", UID: "5001", Name: "B站甲", Face: srv.URL + "/remote-bili-face.jpg"},
		{Site: "douyin", UID: "MS4wLjABAAAA_s1_dy", UniqueID: "dy_alpha", Name: "抖音甲", Face: srv.URL + "/remote-dy-face.jpg"},
	}, subscription.SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatal(err)
	}
	subEmpty, err := rawBackend.CreateSubscription("暂无缓存创作者", "", []subscription.AccountRef{
		{Site: "bilibili", UID: "5002", Name: "空内容UP"},
	}, subscription.SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatal(err)
	}
	subNoAcc, err := rawBackend.CreateSubscription("未关联账号订阅", "", nil, subscription.SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatal(err)
	}

	lib := rawBackend.LibraryStore()
	savePNG := func(dir, name string) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		img := image.NewRGBA(image.Rect(0, 0, 64, 64))
		img.Set(10, 10, color.RGBA{40, 120, 200, 255})
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			f.Close()
			t.Fatal(err)
		}
		f.Close()
	}

	// Seed cached items for Bilibili 5001 (dynamic with image, video, and live recording).
	dynDir := filepath.Join(lib.CacheDir(), "bilibili", "5001", "dyn_top")
	savePNG(dynDir, "img1.png")
	if _, err := lib.SaveItem(dynDir, library.ItemMetadata{
		Site:   "bilibili",
		Kind:   "dynamic",
		ID:     "dyn_top",
		Title:  "甲的B站图文动态",
		Desc:   "图文正文内容",
		PubTs:  1710000400,
		Author: library.ItemAuthor{Mid: 5001, UID: "5001", Name: "B站甲", Face: srv.URL + "/face1.jpg"},
		Media:  []library.MediaFile{{Role: "image", Path: "img1.png", Width: 64, Height: 64}},
	}); err != nil {
		t.Fatal(err)
	}

	biliLiveDir := filepath.Join(lib.CacheDir(), "bilibili", "5001", "live_1")
	_ = os.MkdirAll(biliLiveDir, 0o755)
	_ = os.WriteFile(filepath.Join(biliLiveDir, "part-001.mp4"), []byte("synth"), 0o600)
	if _, err := lib.SaveItem(biliLiveDir, library.ItemMetadata{
		Site:     "bilibili",
		Kind:     "live",
		ID:       "live_5001_1",
		RawID:    "95001",
		Title:    "甲的B站直播录像",
		PubTs:    1710000300,
		Duration: 180,
		Author:   library.ItemAuthor{Mid: 5001, UID: "5001", Name: "B站甲"},
		Media:    []library.MediaFile{{Role: "live_segment", Path: "part-001.mp4", Duration: 180}},
		Live: &library.LiveRecordingInfo{
			RoomID:   95001,
			Segments: []library.LiveSegmentInfo{{File: "part-001.mp4", DurationSec: 180, Quality: 10000, Codec: "avc"}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	biliVidDir := filepath.Join(lib.CacheDir(), "bilibili", "5001", "BV1s1cross")
	_ = os.MkdirAll(biliVidDir, 0o755)
	if _, err := lib.SaveItem(biliVidDir, library.ItemMetadata{
		Site:   "bilibili",
		Kind:   "video",
		ID:     "BV1s1cross",
		Title:  "甲的B站投稿视频",
		Desc:   "视频简介",
		PubTs:  1710000200,
		Author: library.ItemAuthor{Mid: 5001, UID: "5001", Name: "B站甲"},
		Parts:  []library.VideoPartRecord{{Page: 1, Duration: 95, Video: library.PartVideoInfo{QualityName: "1080P 高码率"}}},
	}); err != nil {
		t.Fatal(err)
	}

	// Seed cached items for Douyin MS4wLjABAAAA_s1_dy (newest work + live recording + 65 older items for pagination).
	dyWorkDir := filepath.Join(lib.CacheDir(), "douyin", "dy_work_newest")
	_ = os.MkdirAll(dyWorkDir, 0o755)
	if _, err := lib.SaveItem(dyWorkDir, library.ItemMetadata{
		Site:        "douyin",
		Kind:        "video",
		ID:          "7350000001",
		Title:       "甲的抖音最新作品",
		PubTs:       1710000500,
		Author:      library.ItemAuthor{UID: "MS4wLjABAAAA_s1_dy", Name: "抖音甲"},
		DouyinVideo: &library.DouyinVideoInfo{QualityName: "原画", Codec: "hevc"},
	}); err != nil {
		t.Fatal(err)
	}

	dyLiveDir := filepath.Join(lib.CacheDir(), "douyin", "dy_live_1")
	_ = os.MkdirAll(dyLiveDir, 0o755)
	_ = os.WriteFile(filepath.Join(dyLiveDir, "part-001.mp4"), []byte("synth"), 0o600)
	if _, err := lib.SaveItem(dyLiveDir, library.ItemMetadata{
		Site:     "douyin",
		Kind:     "live",
		ID:       "dy_live_rec_1",
		RawID:    "7399001",
		Title:    "甲的抖音直播录像",
		PubTs:    1710000100,
		Duration: 240,
		Author:   library.ItemAuthor{UID: "MS4wLjABAAAA_s1_dy", Name: "抖音甲"},
		Media:    []library.MediaFile{{Role: "live_segment", Path: "part-001.mp4", Duration: 240}},
		Live: &library.LiveRecordingInfo{
			WebRID:      "7399001",
			QualityName: "原画",
			Codec:       "flv-avc",
			Segments:    []library.LiveSegmentInfo{{File: "part-001.mp4", DurationSec: 240}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= 65; i++ {
		d := filepath.Join(lib.CacheDir(), "douyin", fmt.Sprintf("older_%02d", i))
		_ = os.MkdirAll(d, 0o755)
		if _, err := lib.SaveItem(d, library.ItemMetadata{
			Site:   "douyin",
			Kind:   "dynamic",
			ID:     fmt.Sprintf("730000%04d", i),
			Title:  fmt.Sprintf("甲的早期图文_%02d", i),
			PubTs:  int64(1709900000 + i),
			Author: library.ItemAuthor{UID: "MS4wLjABAAAA_s1_dy", Name: "抖音甲"},
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Seed cached items for OTHER authors that MUST NOT leak into subCross's feed.
	otherBiliDir := filepath.Join(lib.CacheDir(), "bilibili", "9999", "BV1other")
	_ = os.MkdirAll(otherBiliDir, 0o755)
	if _, err := lib.SaveItem(otherBiliDir, library.ItemMetadata{
		Site:   "bilibili",
		Kind:   "video",
		ID:     "BV1other",
		Title:  "路人乙的B站视频",
		PubTs:  1710000600,
		Author: library.ItemAuthor{Mid: 9999, UID: "9999", Name: "路人乙"},
	}); err != nil {
		t.Fatal(err)
	}
	otherDyDir := filepath.Join(lib.CacheDir(), "douyin", "other_dy")
	_ = os.MkdirAll(otherDyDir, 0o755)
	if _, err := lib.SaveItem(otherDyDir, library.ItemMetadata{
		Site:   "douyin",
		Kind:   "dynamic",
		ID:     "7388888888",
		Title:  "路人丙的抖音图文",
		PubTs:  1710000550,
		Author: library.ItemAuthor{UID: "MS4wLjABAAAA_other", Name: "路人丙"},
	}); err != nil {
		t.Fatal(err)
	}

	app.RefreshData()
	// Even if Home was filtered to bilibili, subscription detail "内容" must include all linked accounts across sites.
	app.FeedSiteFilter = "bilibili"
	drainPosted(posted)

	// Reset all network counters right before opening subscription detail.
	siteHTTPRequests.Store(0)
	tracker.fetchImageCalls.Store(0)
	tracker.liveQueryCalls.Store(0)

	tt := ui.NewTester(app.View, 1280, 860)
	app.OpenSubscriptionDetail(subCross)
	tt.Frame()
	drainPosted(posted)
	tt.Frame()

	if app.SubDetailTab != "content" {
		t.Fatalf("expected subscription detail to default to content tab, got %q", app.SubDetailTab)
	}
	for _, wantTab := range []string{"订阅标签：内容", "订阅标签：账号"} {
		if _, ok := tt.Find(wantTab); !ok {
			t.Fatalf("missing tab %s", wantTab)
		}
	}
	if _, ok := tt.Find("订阅标签：直播"); ok {
		t.Fatal("legacy 直播 tab must not appear")
	}

	// Verify newest-first ordering across both sites + live recordings, and exclusion of other authors.
	if len(app.SubFeedItems) != 60 || !app.SubFeedMoreAfter {
		t.Fatalf("expected initial page of 60 items with HasMore=true, got len=%d hasMore=%v", len(app.SubFeedItems), app.SubFeedMoreAfter)
	}
	wantTop5 := []string{
		"甲的抖音最新作品",
		"甲的B站图文动态",
		"甲的B站直播录像",
		"甲的B站投稿视频",
		"甲的抖音直播录像",
	}
	for i, wantTitle := range wantTop5 {
		if app.SubFeedItems[i].Title != wantTitle {
			t.Fatalf("SubFeedItems[%d].Title = %q, want %q", i, app.SubFeedItems[i].Title, wantTitle)
		}
	}
	for _, e := range app.SubFeedItems {
		if e.Title == "路人乙的B站视频" || e.Title == "路人丙的抖音图文" {
			t.Fatalf("unrelated author item leaked into subscription content feed: %+v", e)
		}
	}
	for _, visibleTitle := range []string{"甲的抖音最新作品", "甲的B站图文动态", "甲的B站直播录像"} {
		if !tt.HasText(visibleTitle) {
			t.Fatalf("expected %q rendered in content tab", visibleTitle)
		}
	}
	if tt.HasText("路人乙的B站视频") || tt.HasText("路人丙的抖音图文") {
		t.Fatal("unrelated author content rendered in subscription detail")
	}

	// Clicking an image opens the local image viewer without network calls.
	if err := tt.Click("查看图片 1"); err != nil {
		t.Fatalf("Click 查看图片 1: %v", err)
	}
	tt.Frame()
	drainPosted(posted)
	if !app.ImageViewer.Open {
		t.Fatal("expected clicking cached post image to open local ImageViewer")
	}
	app.closeImageViewer()
	tt.Frame()

	// Scroll virtualized list to bottom to auto-load remaining 10 older items (total 70).
	listRect, ok := tt.Find("订阅内容信息流列表")
	if !ok {
		t.Fatal("missing virtualized list 订阅内容信息流列表")
	}
	for i := 0; i < 30 && len(app.SubFeedItems) < 70; i++ {
		tt.Scroll(listRect.X+listRect.W/2, listRect.Y+listRect.H/2, 0, 1200)
		tt.Frame()
	}
	if len(app.SubFeedItems) != 70 || app.SubFeedMoreAfter {
		t.Fatalf("expected auto-pagination to load all 70 items (hasMore=false), got len=%d hasMore=%v", len(app.SubFeedItems), app.SubFeedMoreAfter)
	}
	for _, e := range app.SubFeedItems {
		if e.Title == "路人乙的B站视频" || e.Title == "路人丙的抖音图文" {
			t.Fatalf("unrelated author item leaked after pagination: %+v", e)
		}
	}

	// Strictly zero site HTTP requests, remote image fetches, or live status queries on "内容" tab.
	drainPosted(posted)
	if got := siteHTTPRequests.Load(); got != 0 {
		t.Fatalf("expected 0 site HTTP requests on subscription content tab, got %d", got)
	}
	if got := tracker.fetchImageCalls.Load(); got != 0 {
		t.Fatalf("expected 0 FetchImage calls on subscription content tab, got %d", got)
	}
	if got := tracker.liveQueryCalls.Load(); got != 0 {
		t.Fatalf("expected 0 LiveStatus queries on subscription content tab, got %d", got)
	}

	// Each cached post has a local-only "⋯" (show in folder, delete); deleting
	// a dynamic removes it from the feed and its registered files from disk.
	app.SubFeedListState.ScrollTo(0, ui.Start)
	tt.Frame()
	dynFeedKey := ""
	for _, e := range app.SubFeedItems {
		if e.Title == "甲的B站图文动态" {
			dynFeedKey = e.FeedKey
		}
	}
	if dynFeedKey == "" {
		t.Fatal("B站 dynamic missing from subscription feed")
	}
	if got := menuItems(t, tt, "帖子更多："+dynFeedKey); strings.Join(got, ",") != "在资源管理器中显示,-,删除" {
		t.Fatalf("content post menu = %v", got)
	}
	tt.Frame()
	chooseMenuItem(t, tt, "帖子更多："+dynFeedKey, "删除")
	tt.Frame()
	if !app.DeleteConfirm.Open || !tt.HasText("动态：甲的B站图文动态") {
		t.Fatalf("expected delete confirmation naming the dynamic, open=%v", app.DeleteConfirm.Open)
	}
	if err := tt.Click("确认删除"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	drainPosted(posted)
	tt.Frame()
	if len(app.SubFeedItems) == 0 || app.SubFeedItems[0].Title != "甲的抖音最新作品" {
		t.Fatalf("after delete the feed should reload from the newest post, got %d items", len(app.SubFeedItems))
	}
	for _, e := range app.SubFeedItems {
		if e.Title == "甲的B站图文动态" {
			t.Fatal("deleted dynamic still in subscription feed")
		}
	}
	if _, err := os.Stat(filepath.Join(dynDir, "img1.png")); !os.IsNotExist(err) {
		t.Fatalf("deleted dynamic's image still on disk: %v", err)
	}
	if got := siteHTTPRequests.Load(); got != 0 {
		t.Fatalf("expected 0 site HTTP requests after deleting from content tab, got %d", got)
	}

	// Empty state for subscription with 0 cached items: exact copy, hint for "⋯" -> "检查更新", no extra primary button.
	app.OpenSubscriptionDetail(subEmpty)
	tt.Frame()
	if !tt.HasText("还没有缓存这个订阅的内容") {
		t.Fatal("missing empty state title 还没有缓存这个订阅的内容")
	}
	if !tt.HasText("检查更新") {
		t.Fatal("missing empty state hint referencing 检查更新 in ⋯ menu")
	}
	for _, forbiddenBtn := range []string{"去解析链接", "检查该订阅更新"} {
		if _, ok := tt.Find(forbiddenBtn); ok {
			t.Fatalf("empty state must not add extra primary/action button %q", forbiddenBtn)
		}
	}

	// Guard: subscription with 0 linked accounts must NOT call FeedPage and must return 0 items.
	beforeFeedCalls := tracker.feedPageCalls.Load()
	app.OpenSubscriptionDetail(subNoAcc)
	tt.Frame()
	if len(app.SubFeedItems) != 0 {
		t.Fatalf("0-account subscription returned %d items, want 0", len(app.SubFeedItems))
	}
	if afterFeedCalls := tracker.feedPageCalls.Load(); afterFeedCalls != beforeFeedCalls {
		t.Fatalf("0-account subscription called FeedPage %d times, want 0", afterFeedCalls-beforeFeedCalls)
	}
	if got := siteHTTPRequests.Load(); got != 0 {
		t.Fatalf("expected 0 site HTTP requests after empty/0-account views, got %d", got)
	}
}

// "账号" tab merges live status per row, provides direct start/stop recording
// for live/recording accounts, puts secondary actions in each row's "⋯" menu, omits
// duplicate recorded-lives list, and keeps bottom "关联另一个账号" intact.
func TestSubscriptionDetailAccountsTabLiveRecordingAndMoreMenu(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	rawBackend := app.backend.(*service.Backend)
	if _, err := rawBackend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()

	tracker := &s1TrackingBackend{
		BackendAPI: rawBackend,
		raw:        rawBackend,
		dyLoggedIn: true,
		rooms: map[int64]live.RoomState{
			6001: {UID: 6001, RoomID: 0, Uname: "无直播间UP", LiveStatus: 0},
			6002: {UID: 6002, RoomID: 9002, Uname: "离线UP", LiveStatus: 0},
			6003: {UID: 6003, RoomID: 9003, Uname: "开播UP", Title: "晚间歌会", LiveStatus: 1, Quality: 10000, QualityName: "原画", Codec: "avc"},
			6004: {UID: 6004, RoomID: 9004, Uname: "无画质UP", Title: "纯标题直播", LiveStatus: 1},
		},
		accounts: map[string]service.LiveAccountState{
			"douyin:MS4wLjABAAAA_dy_live": {
				Site:         "douyin",
				AccountKey:   "douyin:MS4wLjABAAAA_dy_live",
				RecordingKey: "douyin:live:778899",
				UID:          "MS4wLjABAAAA_dy_live",
				WebRID:       "778899",
				RoomID:       "778899",
				Uname:        "抖音主播",
				Title:        "抖音弹唱",
				LiveStatus:   1,
				QualityName:  "原画",
				Codec:        "hevc",
			},
		},
	}
	app.backend = tracker

	sub, err := rawBackend.CreateSubscription("多账号直播订阅", "", []subscription.AccountRef{
		{Site: "bilibili", UID: "6001", Name: "无直播间UP"},
		{Site: "bilibili", UID: "6002", Name: "离线UP"},
		{Site: "bilibili", UID: "6003", Name: "开播UP"},
		{Site: "bilibili", UID: "6004", Name: "无画质UP"},
		{Site: "douyin", UID: "MS4wLjABAAAA_dy_live", UniqueID: "dy_singer", Name: "抖音主播"},
	}, subscription.SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatal(err)
	}

	// Save a recorded live item for 6003 to confirm it is NOT listed under "账号" tab.
	recDir := filepath.Join(rawBackend.LibraryStore().CacheDir(), "bilibili", "6003", "rec_1")
	_ = os.MkdirAll(recDir, 0o755)
	_ = os.WriteFile(filepath.Join(recDir, "part-001.mp4"), []byte("synth"), 0o600)
	if _, err := rawBackend.LibraryStore().SaveItem(recDir, library.ItemMetadata{
		Site:     "bilibili",
		Kind:     "live",
		ID:       "rec_6003_1",
		RawID:    "9003",
		Title:    "历史直播回放",
		PubTs:    1710000900,
		Duration: 60,
		Author:   library.ItemAuthor{Mid: 6003, UID: "6003", Name: "开播UP"},
		Media:    []library.MediaFile{{Role: "live_segment", Path: "part-001.mp4", Duration: 60}},
		Live:     &library.LiveRecordingInfo{RoomID: 9003, Segments: []library.LiveSegmentInfo{{File: "part-001.mp4", DurationSec: 60}}},
	}); err != nil {
		t.Fatal(err)
	}

	app.RefreshData()
	tt := ui.NewTester(app.View, 1280, 960)
	app.OpenSubscriptionDetail(sub)
	tt.Frame()

	// Switch to "账号" tab.
	if err := tt.Click("订阅标签：账号"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if app.SubDetailTab != "accounts" {
		t.Fatalf("expected SubDetailTab=accounts, got %q", app.SubDetailTab)
	}

	// Verify site badges, handles, and all live status line formats.
	for _, wantText := range []string{
		"关联账号（5 个）",
		"未开通直播间",
		"未开播",
		"直播中 · 晚间歌会 · 原画 · AVC",
		"直播中 · 纯标题直播",
		"直播中 · 抖音弹唱 · 原画 · HEVC",
		"· 抖音号：dy_singer",
	} {
		if !tt.HasText(wantText) {
			t.Fatalf("expected %q in accounts tab", wantText)
		}
	}
	for _, badge := range []string{
		"订阅详情账号站点：bilibili:6003",
		"订阅详情账号站点：douyin:MS4wLjABAAAA_dy_live",
	} {
		if _, ok := tt.Find(badge); !ok {
			t.Fatalf("missing site badge %s", badge)
		}
	}
	if tt.HasText("已录制的直播录像") || tt.HasText("历史直播回放") {
		t.Fatal("recorded live videos must not appear in 账号 tab")
	}

	// Bottom "关联另一个账号" input & button remain intact.
	if _, ok := tt.Find("关联账号输入"); !ok {
		t.Fatal("missing 关联账号输入 input box")
	}
	if _, ok := tt.Find("预览关联账号"); !ok {
		t.Fatal("missing 预览关联账号 button")
	}

	// Verify row "⋯" menu items: no-room omits "在浏览器中观看", offline/live with room includes it.
	if got := strings.Join(menuItems(t, tt, "订阅账号更多：bilibili:6001"), ","); got != "打开主页,用作头像,刷新直播状态,更改关联订阅,-,解除关联" {
		t.Fatalf("no-room account menu = %q", got)
	}
	if got := strings.Join(menuItems(t, tt, "订阅账号更多：bilibili:6002"), ","); got != "打开主页,在浏览器中观看,用作头像,刷新直播状态,更改关联订阅,-,解除关联" {
		t.Fatalf("offline-with-room account menu = %q", got)
	}
	if got := strings.Join(menuItems(t, tt, "订阅账号更多：douyin:MS4wLjABAAAA_dy_live"), ","); got != "打开主页,在浏览器中观看,用作头像,刷新直播状态,更改关联订阅,-,解除关联" {
		t.Fatalf("douyin live account menu = %q", got)
	}

	// Direct start/stop recording on Bilibili row (6003 -> room 9003).
	if err := tt.Click("开始录制 9003"); err != nil {
		t.Fatalf("Click 开始录制 9003: %v", err)
	}
	tt.Frame()
	if len(tracker.startedBiliRooms) != 1 || tracker.startedBiliRooms[0] != 9003 {
		t.Fatalf("startedBiliRooms = %v, want [9003]", tracker.startedBiliRooms)
	}
	if !tt.HasText("录制中 · 已录 01:05 · 12 MB · 原画 · AVC") {
		t.Fatal("expected recording status line with duration, size, and quality/codec after starting Bilibili recording")
	}
	if err := tt.Click("停止录制 9003"); err != nil {
		t.Fatalf("Click 停止录制 9003: %v", err)
	}
	tt.Frame()
	if len(tracker.stoppedBiliRooms) != 1 || tracker.stoppedBiliRooms[0] != 9003 {
		t.Fatalf("stoppedBiliRooms = %v, want [9003]", tracker.stoppedBiliRooms)
	}

	// Direct start/stop recording on Douyin row.
	if err := tt.Click("开始录制 douyin:MS4wLjABAAAA_dy_live"); err != nil {
		t.Fatalf("Click Douyin start recording: %v", err)
	}
	tt.Frame()
	if len(tracker.startedAcctRooms) != 1 || tracker.startedAcctRooms[0] != "douyin:778899" {
		t.Fatalf("startedAcctRooms = %v, want [douyin:778899]", tracker.startedAcctRooms)
	}
	if !tt.HasText("录制中 · 已录 01:35 · 24 MB · 原画 · HEVC") {
		t.Fatal("expected Douyin recording status line after starting recording")
	}
	if err := tt.Click("停止录制 douyin:MS4wLjABAAAA_dy_live"); err != nil {
		t.Fatalf("Click Douyin stop recording: %v", err)
	}
	tt.Frame()
	if len(tracker.stoppedAcctKeys) != 1 || tracker.stoppedAcctKeys[0] != "douyin:live:778899" {
		t.Fatalf("stoppedAcctKeys = %v, want [douyin:live:778899]", tracker.stoppedAcctKeys)
	}

	// Row "⋯" menu actions: "刷新直播状态", "在浏览器中观看", "用作头像", "解除关联", "打开主页".
	beforeRefresh := tracker.liveQueryCalls.Load()
	if err := tt.Click("订阅账号更多：bilibili:6003"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.ChooseMenuItem("刷新直播状态"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tracker.liveQueryCalls.Load() <= beforeRefresh {
		t.Fatal("expected row 刷新直播状态 to trigger live status query")
	}

	beforeSecRefresh := tracker.liveQueryCalls.Load()
	if err := tt.Click("订阅直播更多"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.ChooseMenuItem("刷新直播状态"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tracker.liveQueryCalls.Load() <= beforeSecRefresh {
		t.Fatal("expected section 刷新直播状态 to trigger live status query")
	}

	if err := tt.Click("订阅账号更多：bilibili:6003"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.ChooseMenuItem("在浏览器中观看"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(tracker.openedLiveURLs) != 1 || tracker.openedLiveURLs[0] != "bilibili:9003" {
		t.Fatalf("openedLiveURLs = %v, want [bilibili:9003]", tracker.openedLiveURLs)
	}

	if err := tt.Click("订阅账号更多：douyin:MS4wLjABAAAA_dy_live"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.ChooseMenuItem("用作头像"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	subsNow, _ := rawBackend.ListSubscriptions()
	if len(subsNow) != 1 || subsNow[0].Avatar.Site != "douyin" || subsNow[0].Avatar.UID != "MS4wLjABAAAA_dy_live" {
		t.Fatalf("expected avatar switched to douyin account, got %+v", subsNow[0].Avatar)
	}

	if err := tt.Click("订阅账号更多：bilibili:6004"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.ChooseMenuItem("解除关联"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("关联账号（4 个）") {
		t.Fatal("expected 关联账号（4 个） after unlinking bilibili:6004")
	}

	if err := tt.Click("订阅账号更多：douyin:MS4wLjABAAAA_dy_live"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.ChooseMenuItem("打开主页"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if app.CurrentRoute != RouteDouyinUser {
		t.Fatalf("expected 打开主页 to navigate to RouteDouyinUser, got %q", app.CurrentRoute)
	}
	if len(tracker.openedLinks) != 1 || !strings.Contains(tracker.openedLinks[0], "MS4wLjABAAAA_dy_live") {
		t.Fatalf("expected synthetic OpenLink for MS4wLjABAAAA_dy_live, got %v", tracker.openedLinks)
	}
}

// Legacy SubDetailTab="live" in ui-state.json, route parameters, or in-memory
// field normalizes to "accounts", while empty or unknown values default to "content".
func TestLegacyLiveTabNormalizesToAccounts(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	rawBackend := app.backend.(*service.Backend)
	if _, err := rawBackend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()

	sub, err := rawBackend.CreateSubscription("兼容测试订阅", "", []subscription.AccountRef{
		{Site: "bilibili", UID: "7001", Name: "测试UP"},
	}, subscription.SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatal(err)
	}
	app.RefreshData()

	// 1. Persisted legacy subDetailTab="live" in ui-state.json (schema version stays 2).
	st := rawBackend.GetUIState()
	st.SubDetailTab = "live"
	updated, err := rawBackend.UpdateUIState(st)
	if err != nil {
		t.Fatal(err)
	}
	if uistate.CurrentSchemaVersion != 3 {
		t.Fatalf("uistate.CurrentSchemaVersion = %d, want 3", uistate.CurrentSchemaVersion)
	}
	if updated.Version != uistate.CurrentSchemaVersion || updated.SubDetailTab != "accounts" {
		t.Fatalf("expected UpdateUIState(live) -> Version=%d, SubDetailTab=accounts, got %+v", uistate.CurrentSchemaVersion, updated)
	}
	app2 := NewApp(rawBackend, player.NewNullAudioSink())
	defer app2.Close()
	if app2.SubDetailTab != "accounts" {
		t.Fatalf("NewApp from legacy live state: SubDetailTab = %q, want accounts", app2.SubDetailTab)
	}

	// 2. In-memory SubDetailTab="live" normalizes on OpenSubscriptionDetail and on render.
	tt := ui.NewTester(app.View, 1280, 860)
	app.SubDetailTab = "live"
	app.OpenSubscriptionDetail(sub)
	if app.SubDetailTab != "accounts" {
		t.Fatalf("OpenSubscriptionDetail with SubDetailTab=live got %q, want accounts", app.SubDetailTab)
	}
	app.SubDetailTab = "live" // direct mutation after OpenSubscriptionDetail (e.g. legacy caller)
	tt.Frame()
	if app.SubDetailTab != "accounts" || !tt.HasText("关联账号（1 个）") {
		t.Fatalf("render with SubDetailTab=live got %q, want accounts tab rendered", app.SubDetailTab)
	}

	// 3. Route parameter ?tab=live and /subscriptions/:id/live normalize to "accounts"; unknown defaults to "content".
	app.SubDetailTab = "content"
	app.Navigate("/subscriptions/" + sub.ID + "?tab=live")
	tt.Frame()
	if !app.SubDetailOpen || app.SelectedSubID != sub.ID || app.SubDetailTab != "accounts" {
		t.Fatalf("Navigate ?tab=live: open=%v id=%q tab=%q", app.SubDetailOpen, app.SelectedSubID, app.SubDetailTab)
	}

	app.Navigate("/subscriptions/" + sub.ID + "?tab=unknown_legacy")
	tt.Frame()
	if app.SubDetailTab != "content" {
		t.Fatalf("Navigate ?tab=unknown_legacy: tab=%q, want content", app.SubDetailTab)
	}

	app.SubDetailTab = ""
	tt.Frame()
	if app.SubDetailTab != "content" {
		t.Fatalf("empty SubDetailTab rendered as %q, want content", app.SubDetailTab)
	}
}

// Both "内容" and "账号" tabs prefer local avatar copies (0 remote
// FetchImage calls) and have no horizontal overflow across 1280 / 1000 / 720 widths.
func TestSubscriptionDetailThreeWidthsAndLocalAvatars(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	rawBackend := app.backend.(*service.Backend)
	if _, err := rawBackend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()

	posted := make(chan func(), 64)
	app.dispatch = func(fn func()) { posted <- fn }

	avatarFile := filepath.Join(t.TempDir(), "local_avatar.png")
	{
		img := image.NewRGBA(image.Rect(0, 0, 64, 64))
		img.Set(8, 8, color.RGBA{30, 140, 210, 255})
		f, err := os.Create(avatarFile)
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			f.Close()
			t.Fatal(err)
		}
		f.Close()
	}

	tracker := &s1TrackingBackend{
		BackendAPI: rawBackend,
		raw:        rawBackend,
		dyLoggedIn: true,
		localAvatars: map[string]string{
			"bilibili:8001":                avatarFile,
			"douyin:MS4wLjABAAAA_local_av": avatarFile,
		},
		rooms: map[int64]live.RoomState{
			8001: {UID: 8001, RoomID: 18001, Uname: "本机头像UP", Title: "窄窗超长直播标题防溢出验证测试流", LiveStatus: 1, Quality: 10000, QualityName: "原画", Codec: "avc"},
		},
		accounts: map[string]service.LiveAccountState{
			"douyin:MS4wLjABAAAA_local_av": {
				Site:       "douyin",
				AccountKey: "douyin:MS4wLjABAAAA_local_av",
				UID:        "MS4wLjABAAAA_local_av",
				WebRID:     "889900",
				RoomID:     "889900",
				Uname:      "本机头像抖音号",
				LiveStatus: 0,
			},
		},
	}
	app.backend = tracker

	sub, err := rawBackend.CreateSubscription("三档宽度与本机头像订阅", "", []subscription.AccountRef{
		{Site: "bilibili", UID: "8001", Name: "本机头像UP", Face: "http://127.0.0.1:1/never_fetch_bili.png"},
		{Site: "douyin", UID: "MS4wLjABAAAA_local_av", UniqueID: "local_dy_id", Name: "本机头像抖音号", Face: "http://127.0.0.1:1/never_fetch_dy.png"},
	}, subscription.SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatal(err)
	}
	itemDir := filepath.Join(rawBackend.LibraryStore().CacheDir(), "bilibili", "8001", "BV1WidthTest")
	_ = os.MkdirAll(itemDir, 0o755)
	_ = os.WriteFile(filepath.Join(itemDir, "video.mp4"), []byte("synth"), 0o600)
	if _, err := rawBackend.LibraryStore().SaveItem(itemDir, library.ItemMetadata{
		Site:     "bilibili",
		Kind:     "video",
		ID:       "BV1WidthTest",
		Title:    "三档宽度本机缓存视频条目",
		PubTs:    1710002000,
		Duration: 42,
		Author: library.ItemAuthor{
			Mid:  8001,
			UID:  "8001",
			Name: "本机头像UP",
			Face: "http://127.0.0.1:1/never_fetch_bili.png",
		},
		Media: []library.MediaFile{{Role: "video", Path: "video.mp4", Duration: 42}},
	}); err != nil {
		t.Fatal(err)
	}

	app.RefreshData()

	for _, w := range []int{1280, 1000, 720} {
		for _, tab := range []string{"content", "accounts"} {
			tt := ui.NewTester(app.View, w, 860)
			app.OpenSubscriptionDetail(sub)
			app.setSubDetailTab(tab)
			for i := 0; i < 3; i++ {
				tt.Frame()
				drainPosted(posted)
			}
			leftRect, okL := tt.Find("左栏导航")
			centerRect, okC := tt.Find("中栏内容")
			if !okL || !okC {
				t.Fatalf("width=%d tab=%s missing shell columns: left=%v center=%v", w, tab, okL, okC)
			}
			if leftRect.X < -0.5 || centerRect.X+centerRect.W > float32(w)+0.5 {
				t.Fatalf("width=%d tab=%s horizontal overflow: left=%+v center=%+v", w, tab, leftRect, centerRect)
			}
			reqLabels := []string{"订阅标签：内容", "订阅标签：账号", "订阅管理菜单"}
			if tab == "accounts" {
				reqLabels = append(reqLabels, "订阅详情账号站点：bilibili:8001", "开始录制 18001")
			}
			for _, lbl := range reqLabels {
				r, ok := tt.Find(lbl)
				if !ok || r.W <= 0 || r.H <= 0 || r.X < -0.5 || r.X+r.W > float32(w)+0.5 {
					t.Fatalf("width=%d tab=%s control %q out of bounds: ok=%v rect=%+v", w, tab, lbl, ok, r)
				}
			}
		}
	}

	if got := tracker.fetchImageCalls.Load(); got != 0 {
		t.Fatalf("expected local avatars to satisfy header and account rows with 0 FetchImage calls, got %d", got)
	}
}
