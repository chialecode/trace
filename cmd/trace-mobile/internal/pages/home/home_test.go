package home

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chialecode/trace/cmd/trace-mobile/internal/mediaview"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/pagekit"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/theme"
	"github.com/chialecode/trace/internal/companionapi"
	"github.com/chialecode/trace/internal/companionclient"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func makeTinyPNG(col color.RGBA) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 64, 36))
	for y := 0; y < 36; y++ {
		for x := 0; x < 64; x++ {
			img.SetRGBA(x, y, col)
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

type homeTestHost struct {
	mu         sync.Mutex
	client     *companionclient.Client
	proxy      *companionclient.MediaProxy
	imgCache   *companionclient.MemoryImageCache
	router     *ui.Router
	onUpdate   func()
	openedURLs []string
	samplePNG  []byte
}

func (h *homeTestHost) ConnState() pagekit.ConnState { return pagekit.ConnConnected }
func (h *homeTestHost) Desktop() pagekit.DesktopSnapshot {
	return pagekit.DesktopSnapshot{ComputerName: "Studio-PC"}
}
func (h *homeTestHost) Client() *companionclient.Client                   { return h.client }
func (h *homeTestHost) MediaProxy() *companionclient.MediaProxy           { return h.proxy }
func (h *homeTestHost) ImageCache() *companionclient.MemoryImageCache     { return h.imgCache }
func (h *homeTestHost) Window() *mygo.Window                              { return nil }
func (h *homeTestHost) Router() *ui.Router                                { return h.router }
func (h *homeTestHost) PushRoute(path string)                             { h.router.Push(path) }
func (h *homeTestHost) Back()                                             { h.router.Back() }
func (h *homeTestHost) PushWindow(string, func(*ui.Context)) *mygo.Window { return nil }
func (h *homeTestHost) SetTasksBadge(int)                                 {}
func (h *homeTestHost) SelectTab(pagekit.TabID)                           {}
func (h *homeTestHost) RefreshStatus()                                    {}
func (h *homeTestHost) Unpair()                                           {}

func (h *homeTestHost) OpenExternal(rawURL string) error {
	h.mu.Lock()
	h.openedURLs = append(h.openedURLs, rawURL)
	h.mu.Unlock()
	return nil
}

func (h *homeTestHost) Update(fn func()) {
	if fn != nil {
		fn()
	}
	h.mu.Lock()
	cb := h.onUpdate
	h.mu.Unlock()
	if cb != nil {
		cb()
	}
}

func (h *homeTestHost) FetchImage(_ context.Context, _ string) ([]byte, error) {
	return h.samplePNG, nil
}

type syntheticFeedServer struct {
	mu          sync.Mutex
	items       []companionapi.FeedItem
	subs        []companionapi.FeedSubscriptionRow
	subsBySite  map[string][]companionapi.FeedSubscriptionRow
	positions   map[string]companionapi.HomePosition
	seenReqs    []companionapi.FeedSeenRequest
	putPosReqs  []companionapi.HomePosition
	desktopSeen map[string]int64
}

func newSyntheticFeedServer(t *testing.T, count int) (*syntheticFeedServer, *homeTestHost, func()) {
	t.Helper()
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC).Unix()
	sites := []string{"bilibili", "douyin", "weibo", "x"}

	items := make([]companionapi.FeedItem, count)
	for i := 0; i < count; i++ {
		site := sites[i%len(sites)]
		pubTs := now - int64(i*60)
		it := companionapi.FeedItem{
			FeedKey:    fmt.Sprintf("feed-%05d", i),
			ItemKey:    fmt.Sprintf("%s_item_%05d", site, i),
			Site:       site,
			Kind:       "dynamic",
			AuthorUID:  fmt.Sprintf("uid-%d", i%10),
			AuthorName: fmt.Sprintf("创作者 %d", i%10),
			SubID:      fmt.Sprintf("sub-%d", i%4),
			SubName:    fmt.Sprintf("订阅组 %d", i%4),
			Title:      fmt.Sprintf("缓存内容标题 #%d", i),
			Text:       fmt.Sprintf("这是第 %d 条合成缓存内容，包含链接 https://example.com/item/%d 供测试。", i, i),
			PubTs:      pubTs,
		}
		switch i % 4 {
		case 0:
			it.Kind = "video"
			it.Duration = 325
			it.CoverRelPath = "cover.jpg"
			it.VideoRelPath = "video.mp4"
			it.QualitySummary = "1080P · AVC"
		case 1:
			it.Images = []companionapi.FeedImage{
				{Path: "images/01.jpg", Width: 1200, Height: 800},
				{Path: "images/02.jpg", Width: 800, Height: 800, Motion: "images/02_live.mp4"},
			}
		case 2:
			it.Images = []companionapi.FeedImage{
				{Path: "images/01.jpg", NoWatermark: "images/01_nowm.jpg", Width: 900, Height: 1200},
			}
			it.Origin = &companionapi.ItemOriginDTO{
				Site:       "weibo",
				AuthorName: "原博主",
				Text:       "被转发的原微博正文",
			}
		case 3:
			it.Kind = "live"
			it.CoverRelPath = "cover.jpg"
			it.QualitySummary = "原画 · AVC"
			it.Live = &companionapi.LiveInfoDTO{
				RoomID:      "8888",
				QualityName: "原画",
				Codec:       "AVC",
				Segments: []companionapi.LiveSegmentDTO{
					{File: "seg_01.mp4", DurationSec: 1800, Codec: "AVC"},
					{File: "seg_02.mp4", DurationSec: 1200, Codec: "AVC"},
				},
			}
		}
		items[i] = it
	}

	s := &syntheticFeedServer{
		items: items,
		subs: []companionapi.FeedSubscriptionRow{
			{SubID: "sub-0", Name: "正在直播主播", Live: true, Unread: true, LatestPubTs: now},
			{SubID: "sub-1", Name: "有更新作者", Live: false, Unread: true, LatestPubTs: now - 60},
			{SubID: "sub-2", Name: "已读订阅", Live: false, Unread: false, LatestPubTs: now - 120},
		},
		positions:   map[string]companionapi.HomePosition{},
		desktopSeen: map[string]int64{},
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	fp := companionclient.Fingerprint(der)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(s.serveHTTP))
	srv.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
	}
	srv.StartTLS()

	addr := strings.TrimPrefix(srv.URL, "https://")
	rec := companionclient.PairingRecord{
		Node:     "node-1",
		Name:     "Studio-PC",
		Channel:  "dev",
		FP:       fp,
		Addrs:    []string{addr},
		LastAddr: addr,
	}
	cli, err := companionclient.NewClient(rec, "tok-secret-home", "0.1.0-dev", "dev")
	if err != nil {
		srv.Close()
		t.Fatal(err)
	}
	proxy, err := companionclient.NewMediaProxy(cli.HTTPClient(), func() (string, string, string, string, bool) {
		return cli.ActiveBaseURL(), cli.Token(), cli.AppVersion(), cli.Channel(), true
	})
	if err != nil {
		cli.Close()
		srv.Close()
		t.Fatal(err)
	}

	host := &homeTestHost{
		client:    cli,
		proxy:     proxy,
		imgCache:  companionclient.NewMemoryImageCache(companionclient.DefaultImageCacheBytes),
		router:    ui.NewRouter("/"),
		samplePNG: makeTinyPNG(color.RGBA{R: 70, G: 140, B: 230, A: 255}),
	}
	cleanup := func() {
		_ = proxy.Close()
		cli.Close()
		srv.Close()
	}
	return s, host, cleanup
}

func (s *syntheticFeedServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch {
	case r.URL.Path == companionapi.PathFeed && r.Method == http.MethodGet:
		q := r.URL.Query()
		site := q.Get("site")
		subID := q.Get("subId")
		limit := 30
		if l, err := strconv.Atoi(q.Get("limit")); err == nil && l > 0 {
			limit = l
		}
		cursorPubTs, _ := strconv.ParseInt(q.Get("cursorPubTs"), 10, 64)
		cursorKey := q.Get("cursorKey")

		s.mu.Lock()
		filtered := make([]companionapi.FeedItem, 0, limit+1)
		for _, it := range s.items {
			if site != "" && site != "all" && it.Site != site {
				continue
			}
			if subID != "" && it.SubID != subID {
				continue
			}
			// Match real desktop handleFeed: cursor filtering ONLY activates when cursorKey != "",
			// and skips rows >= (cursorPubTs, cursorKey) in DESC order.
			if cursorKey != "" {
				if it.PubTs > cursorPubTs || (it.PubTs == cursorPubTs && it.FeedKey >= cursorKey) {
					continue
				}
			}
			filtered = append(filtered, it)
			if len(filtered) > limit {
				break
			}
		}
		s.mu.Unlock()

		hasMore := len(filtered) > limit
		if hasMore {
			filtered = filtered[:limit]
		}
		var next *companionapi.FeedCursor
		if hasMore && len(filtered) > 0 {
			last := filtered[len(filtered)-1]
			next = &companionapi.FeedCursor{PubTs: last.PubTs, Key: last.FeedKey}
		}
		_ = json.NewEncoder(w).Encode(companionapi.FeedPageResponse{
			Items:      filtered,
			NextCursor: next,
			HasMore:    hasMore,
		})

	case r.URL.Path == companionapi.PathFeedSubscriptions && r.Method == http.MethodGet:
		site := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("site")))
		if site == "" {
			site = "all"
		}
		s.mu.Lock()
		var rows []companionapi.FeedSubscriptionRow
		if s.subsBySite != nil {
			if list, ok := s.subsBySite[site]; ok {
				rows = append([]companionapi.FeedSubscriptionRow(nil), list...)
			}
		}
		if rows == nil {
			rows = append([]companionapi.FeedSubscriptionRow(nil), s.subs...)
		}
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(companionapi.FeedSubscriptionsResponse{Items: rows})

	case r.URL.Path == companionapi.PathFeedSeen && r.Method == http.MethodPost:
		var req companionapi.FeedSeenRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		s.mu.Lock()
		s.seenReqs = append(s.seenReqs, req)
		s.desktopSeen["sub:"+req.SubID] = req.SeenTs
		for i := range s.subs {
			if s.subs[i].SubID == req.SubID {
				s.subs[i].Unread = false
			}
		}
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(companionapi.ActionResponse{OK: true})

	case r.URL.Path == companionapi.PathFeedPosition && r.Method == http.MethodGet:
		s.mu.Lock()
		posCopy := make(map[string]companionapi.HomePosition, len(s.positions))
		for k, v := range s.positions {
			posCopy[k] = v
		}
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(companionapi.HomePositionsResponse{Positions: posCopy})

	case r.URL.Path == companionapi.PathFeedPosition && r.Method == http.MethodPut:
		var pos companionapi.HomePosition
		_ = json.NewDecoder(r.Body).Decode(&pos)
		s.mu.Lock()
		site := pos.Site
		if site == "" {
			site = "all"
		}
		s.positions[site] = pos
		s.putPosReqs = append(s.putPosReqs, pos)
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(companionapi.ActionResponse{OK: true})

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func assertNoHorizontalOverflow(t *testing.T, tt *ui.Tester, width int) {
	t.Helper()
	for _, info := range tt.AccessAll() {
		if info.Bounds.W <= 0 || info.Bounds.H <= 0 || info.Offscreen {
			continue
		}
		if info.Bounds.X < -1 || info.Bounds.X+info.Bounds.W > float32(width)+1 {
			t.Fatalf("horizontal overflow at width=%d on %q (%s): rect=%+v", width, info.Name, info.Value, info.Bounds)
		}
	}
}

func saveOptionalShot(t *testing.T, tt *ui.Tester, name string) {
	t.Helper()
	dir := strings.TrimSpace(os.Getenv("TRACE_MOBILE_SHOTS_DIR"))
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, tt.Image()); err != nil {
		t.Fatal(err)
	}
}

func TestHome5000ItemsPerformanceSharedSeenPositionAndResponsive(t *testing.T) {
	srv, host, cleanup := newSyntheticFeedServer(t, 5000)
	defer cleanup()

	p := New()
	p.posSaveDelay = 20 * time.Millisecond
	p.nowFn = func() time.Time { return time.Date(2026, 11, 26, 12, 5, 0, 0, time.UTC) }

	var openedImages []mediaview.ImageItem
	var openedVideo mediaview.VideoRequest
	p.openImageFn = func(_ pagekit.Host, items []mediaview.ImageItem, _ int) { openedImages = items }
	p.openVideoFn = func(_ pagekit.Host, req mediaview.VideoRequest) { openedVideo = req }

	notify := make(chan struct{}, 64)
	host.onUpdate = func() {
		select {
		case notify <- struct{}{}:
		default:
		}
	}

	view := func(c *ui.Context) {
		c.SetTheme(theme.TraceTheme())
		p.View(c, host)
	}

	start := time.Now()
	p.OnConnected(host)
	tt := ui.NewTester(view, 412, 915)
	pumpUntil := func(cond func() bool) {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			tt.Frame()
			if cond() {
				return
			}
			select {
			case <-notify:
			case <-time.After(10 * time.Millisecond):
			}
		}
	}

	pumpUntil(func() bool {
		return tt.HasText("缓存内容标题 #0") && tt.HasText("正在直播主播")
	})
	firstScreenElapsed := time.Since(start)
	if !tt.HasText("缓存内容标题 #0") {
		t.Fatalf("expected first screen feed items within 2s; texts=%v", tt.Texts())
	}
	if firstScreenElapsed > 2*time.Second {
		t.Fatalf("NFR-MOB-PERF-001 violation: first screen took %s (>2s)", firstScreenElapsed)
	}

	// Verify responsive layout with zero overflow across 360x800, 412x915, 800x1280.
	assertNoHorizontalOverflow(t, tt, 412)
	saveOptionalShot(t, tt, "home-feed-412x915")

	tt.SetSize(360, 800)
	tt.Frame()
	assertNoHorizontalOverflow(t, tt, 360)
	saveOptionalShot(t, tt, "home-feed-360x800")

	tt.SetSize(800, 1280)
	tt.Frame()
	assertNoHorizontalOverflow(t, tt, 800)
	saveOptionalShot(t, tt, "home-feed-800x1280")

	tt.SetSize(412, 915)
	tt.Frame()

	// Verify clickable http(s) link opens via Host.OpenExternal.
	if err := tt.Click("打开链接 example.com/item/0"); err != nil {
		t.Fatalf("click external link: %v", err)
	}
	host.mu.Lock()
	gotURLs := append([]string(nil), host.openedURLs...)
	host.mu.Unlock()
	if len(gotURLs) != 1 || gotURLs[0] != "https://example.com/item/0" {
		t.Fatalf("expected OpenExternal called with item 0 link, got %v", gotURLs)
	}

	// Verify tapping video card invokes video player.
	if err := tt.Click("播放 缓存内容标题 #0"); err != nil {
		t.Fatalf("click video card: %v", err)
	}
	if openedVideo.ItemKey != "bilibili_item_00000" || len(openedVideo.Tracks) != 1 {
		t.Fatalf("expected video player opened for item 0, got %+v", openedVideo)
	}

	// Verify tapping image tile invokes image viewer with live photo metadata.
	if err := tt.Click("查看图片 1 / 2"); err != nil {
		t.Fatalf("click image tile: %v", err)
	}
	if len(openedImages) != 2 || openedImages[1].MotionRelPath != "images/02_live.mp4" {
		t.Fatalf("expected image viewer opened with 2 images including live photo, got %+v", openedImages)
	}

	// Verify shared read state via POST /api/v1/feed/seen when opening a subscription.
	if err := tt.Click("正在直播主播，直播中，有未读"); err != nil {
		t.Fatalf("click live unread sub: %v", err)
	}
	pumpUntil(func() bool {
		srv.mu.Lock()
		n := len(srv.seenReqs)
		srv.mu.Unlock()
		return n > 0 && tt.HasText("正在直播主播")
	})
	srv.mu.Lock()
	seenCount := len(srv.seenReqs)
	desktopTS := srv.desktopSeen["sub:sub-0"]
	srv.mu.Unlock()
	if seenCount == 0 || desktopTS == 0 {
		t.Fatalf("expected POST /api/v1/feed/seen shared with desktop, count=%d ts=%d", seenCount, desktopTS)
	}
	// Return to main timeline: unread dot is gone and order did not jump.
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	if err := tt.Click("返回主页"); err != nil {
		t.Fatalf("click back to home: %v", err)
	}
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	if _, stillUnread := tt.Find("正在直播主播，直播中，有未读"); stillUnread {
		t.Fatal("expected unread dot cleared after opening subscription")
	}

	// Verify site filter switches to 微博.
	if err := tt.Click("筛选 微博"); err != nil {
		t.Fatalf("click weibo filter: %v", err)
	}
	pumpUntil(func() bool { return tt.HasText("被转发的原微博正文") })
	if !tt.HasText("被转发的原微博正文") {
		t.Fatalf("expected weibo filtered post with origin quote, texts=%v", tt.Texts())
	}

	// Verify per-device position saved via PUT /api/v1/feed/position.
	pumpUntil(func() bool {
		srv.mu.Lock()
		n := len(srv.putPosReqs)
		srv.mu.Unlock()
		return n > 0
	})
	srv.mu.Lock()
	putCount := len(srv.putPosReqs)
	srv.mu.Unlock()
	if putCount == 0 {
		t.Fatal("expected per-device reading position saved via PUT /api/v1/feed/position")
	}

	// Verify OnDisconnected clears all in-memory data (NFR-MOB-DATA-001).
	p.OnDisconnected()
	p.mu.Lock()
	remFeeds := len(p.feeds)
	remSubs := len(p.subs)
	p.mu.Unlock()
	if remFeeds != 0 || remSubs != 0 || p.loader.Len() != 0 {
		t.Fatalf("expected all in-memory feed & image data cleared on disconnect, got feeds=%d subs=%d imgs=%d", remFeeds, remSubs, p.loader.Len())
	}
}

func TestHomeRestoresPerDevicePositionDeepIn5000Feed(t *testing.T) {
	srv, host, cleanup := newSyntheticFeedServer(t, 5000)
	defer cleanup()

	// Seed a per-device saved position at item #1200 (deep in the 5000-item feed).
	target := srv.items[1200]
	srv.mu.Lock()
	srv.positions["all"] = companionapi.HomePosition{
		Site:  "all",
		Key:   target.FeedKey,
		PubTs: target.PubTs,
	}
	srv.mu.Unlock()

	p := New()
	p.nowFn = func() time.Time { return time.Date(2026, 11, 26, 12, 5, 0, 0, time.UTC) }

	notify := make(chan struct{}, 64)
	host.onUpdate = func() {
		select {
		case notify <- struct{}{}:
		default:
		}
	}

	start := time.Now()
	p.OnConnected(host)
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(theme.TraceTheme())
		p.View(c, host)
	}, 412, 915)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tt.Frame()
		if tt.HasText("缓存内容标题 #1200") {
			break
		}
		select {
		case <-notify:
		case <-time.After(10 * time.Millisecond):
		}
	}
	if !tt.HasText("缓存内容标题 #1200") {
		t.Fatalf("expected restored saved position item #1200; texts=%v", tt.Texts())
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("restoring position in 5000-item feed took %s (>2s)", elapsed)
	}
	p.OnDisconnected()
}

func TestHomeCrossSiteSwitchRestoresLiveUnreadOrder(t *testing.T) {
	srv, host, cleanup := newSyntheticFeedServer(t, 40)
	defer cleanup()

	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC).Unix()
	srv.mu.Lock()
	srv.subsBySite = map[string][]companionapi.FeedSubscriptionRow{
		"all": {
			{SubID: "sub-wb-live", Name: "直播微博博主", Live: true, Unread: true, LatestPubTs: now},
			{SubID: "sub-bili-read", Name: "已读B站UP", Live: false, Unread: false, LatestPubTs: now - 300},
		},
		"bilibili": {
			{SubID: "sub-bili-read", Name: "已读B站UP", Live: false, Unread: false, LatestPubTs: now - 300},
		},
	}
	srv.mu.Unlock()

	p := New()
	p.nowFn = func() time.Time { return time.Date(2026, 11, 26, 12, 5, 0, 0, time.UTC) }
	notify := make(chan struct{}, 32)
	host.onUpdate = func() {
		select {
		case notify <- struct{}{}:
		default:
		}
	}

	p.OnConnected(host)
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(theme.TraceTheme())
		p.View(c, host)
	}, 412, 915)
	pumpUntil := func(cond func() bool) {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			tt.Frame()
			if cond() {
				return
			}
			select {
			case <-notify:
			case <-time.After(10 * time.Millisecond):
			}
		}
	}

	pumpUntil(func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return len(p.subs) == 2 && p.subs[0].SubID == "sub-wb-live"
	})

	// Switch to B站 (only sub-bili-read).
	if err := tt.Click("筛选 B 站"); err != nil {
		t.Fatal(err)
	}
	pumpUntil(func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.subsSiteLoaded == "bilibili" && len(p.subs) == 1 && p.subs[0].SubID == "sub-bili-read"
	})

	// Switch back to 全部 -> live Weibo sub MUST be back at index 0 ahead of read B站 sub!
	if err := tt.Click("筛选 全部"); err != nil {
		t.Fatal(err)
	}
	pumpUntil(func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.subsSiteLoaded == "all" && len(p.subs) == 2
	})
	p.mu.Lock()
	gotSubs := append([]companionapi.FeedSubscriptionRow(nil), p.subs...)
	p.mu.Unlock()
	if len(gotSubs) != 2 || gotSubs[0].SubID != "sub-wb-live" || gotSubs[1].SubID != "sub-bili-read" {
		t.Fatalf("expected live sub first after switching back to 全部, got %+v", gotSubs)
	}
	p.OnDisconnected()
}
