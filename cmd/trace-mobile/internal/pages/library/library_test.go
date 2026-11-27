package library

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
	"net/url"
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

type libTestHost struct {
	mu        sync.Mutex
	client    *companionclient.Client
	proxy     *companionclient.MediaProxy
	imgCache  *companionclient.MemoryImageCache
	router    *ui.Router
	onUpdate  func()
	samplePNG []byte
}

func (h *libTestHost) ConnState() pagekit.ConnState { return pagekit.ConnConnected }
func (h *libTestHost) Desktop() pagekit.DesktopSnapshot {
	return pagekit.DesktopSnapshot{ComputerName: "Studio-PC"}
}
func (h *libTestHost) Client() *companionclient.Client                   { return h.client }
func (h *libTestHost) MediaProxy() *companionclient.MediaProxy           { return h.proxy }
func (h *libTestHost) ImageCache() *companionclient.MemoryImageCache     { return h.imgCache }
func (h *libTestHost) Window() *mygo.Window                              { return nil }
func (h *libTestHost) Router() *ui.Router                                { return h.router }
func (h *libTestHost) PushRoute(path string)                             { h.router.Push(path) }
func (h *libTestHost) Back()                                             { h.router.Back() }
func (h *libTestHost) PushWindow(string, func(*ui.Context)) *mygo.Window { return nil }
func (h *libTestHost) SetTasksBadge(int)                                 {}
func (h *libTestHost) SelectTab(pagekit.TabID)                           {}
func (h *libTestHost) OpenExternal(string) error                         { return nil }
func (h *libTestHost) RefreshStatus()                                    {}
func (h *libTestHost) Unpair()                                           {}

func (h *libTestHost) Update(fn func()) {
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

func (h *libTestHost) FetchImage(_ context.Context, _ string) ([]byte, error) {
	return h.samplePNG, nil
}

type syntheticLibServer struct {
	mu            sync.Mutex
	items         []companionapi.LibraryItemSummary
	details       map[string]companionapi.LibraryItemDetail
	authors       []companionapi.LibraryAuthorSummary
	authorsBySite map[string][]companionapi.LibraryAuthorSummary
	queries       []url.Values
}

func newSyntheticLibServer(t *testing.T, count int) (*syntheticLibServer, *libTestHost, func()) {
	t.Helper()
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC).Unix()
	sites := []string{"bilibili", "douyin", "weibo", "x"}
	kinds := []string{"video", "dynamic", "live", "dynamic"}

	items := make([]companionapi.LibraryItemSummary, count)
	details := make(map[string]companionapi.LibraryItemDetail, 16)
	for i := 0; i < count; i++ {
		site := sites[i%len(sites)]
		kind := kinds[i%len(kinds)]
		key := fmt.Sprintf("%s_lib_%05d", site, i)
		sum := companionapi.LibraryItemSummary{
			ItemKey:      key,
			Site:         site,
			Kind:         kind,
			Status:       "complete",
			Title:        fmt.Sprintf("媒体库条目 #%d", i),
			AuthorUID:    fmt.Sprintf("u-%d", i%3),
			AuthorName:   fmt.Sprintf("作者 %d", i%3),
			SubID:        fmt.Sprintf("sub-%d", i%2),
			SubName:      fmt.Sprintf("订阅 %d", i%2),
			PubTs:        now - int64(i*120),
			CoverRelPath: "cover.jpg",
			TotalBytes:   12 << 20,
		}
		switch kind {
		case "video":
			sum.Duration = 420
			sum.PrimaryRelPath = "p1.mp4"
			sum.PrimaryKind = "video"
			sum.PartCount = 2
			sum.QualitySummary = "1080P · AVC"
		case "live":
			sum.Duration = 3600
			sum.PrimaryRelPath = "seg_01.mp4"
			sum.PrimaryKind = "live"
			sum.QualitySummary = "原画 · AVC"
		default:
			sum.PrimaryRelPath = "images/01.jpg"
			sum.PrimaryKind = "image"
			sum.ImageCount = 3
		}
		items[i] = sum

		if i < 12 {
			det := companionapi.LibraryItemDetail{
				ItemKey:    key,
				ID:         key,
				Site:       site,
				Kind:       kind,
				Status:     "complete",
				Title:      sum.Title,
				Text:       fmt.Sprintf("这是 %s 的离线只读详情正文。", sum.Title),
				PubTs:      sum.PubTs,
				Duration:   sum.Duration,
				AuthorUID:  sum.AuthorUID,
				AuthorName: sum.AuthorName,
				SubID:      sum.SubID,
				SubName:    sum.SubName,
				TotalBytes: sum.TotalBytes,
			}
			if kind == "video" {
				det.Parts = []companionapi.VideoPartDTO{
					{Page: 1, Title: "上集", Duration: 200, File: "p1.mp4", QualityName: "1080P", Codecs: "AVC"},
					{Page: 2, Title: "下集", Duration: 220, File: "p2.mp4", QualityName: "1080P", Codecs: "AVC"},
				}
			} else if kind == "live" {
				det.Live = &companionapi.LiveInfoDTO{
					RoomID:      "9999",
					QualityName: "原画",
					Codec:       "AVC",
					Segments: []companionapi.LiveSegmentDTO{
						{File: "seg_01.mp4", DurationSec: 1800, Bytes: 500 << 20, Codec: "AVC", QualityName: "原画"},
						{File: "seg_02.mp4", DurationSec: 1800, Bytes: 480 << 20, Codec: "AVC", QualityName: "原画"},
					},
				}
			} else {
				det.Media = []companionapi.MediaFileDTO{
					{Role: "image", Path: "images/01.jpg", Width: 1000, Height: 1200},
					{Role: "image_nowm", Path: "images/01_nowm.jpg"},
					{Role: "image", Path: "images/02.jpg", Motion: "images/02_live.mp4"},
				}
			}
			details[key] = det
		}
	}

	s := &syntheticLibServer{
		items:   items,
		details: details,
		authors: []companionapi.LibraryAuthorSummary{
			{Kind: "subscription", SubID: "sub-0", Name: "订阅 0", ItemCount: count / 2, TotalBytes: 2 << 30},
			{Kind: "subscription", SubID: "sub-1", Name: "订阅 1", ItemCount: count / 2, TotalBytes: 1 << 30},
		},
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
	cli, err := companionclient.NewClient(rec, "tok-secret-lib", "0.1.0-dev", "dev")
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
	host := &libTestHost{
		client:    cli,
		proxy:     proxy,
		imgCache:  companionclient.NewMemoryImageCache(companionclient.DefaultImageCacheBytes),
		router:    ui.NewRouter("/"),
		samplePNG: makeTinyPNG(color.RGBA{R: 90, G: 150, B: 190, A: 255}),
	}
	cleanup := func() {
		_ = proxy.Close()
		cli.Close()
		srv.Close()
	}
	return s, host, cleanup
}

func (s *syntheticLibServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch {
	case r.URL.Path == companionapi.PathLibrary && r.Method == http.MethodGet:
		q := r.URL.Query()
		s.mu.Lock()
		s.queries = append(s.queries, q)
		s.mu.Unlock()

		limit := 40
		if l, err := strconv.Atoi(q.Get("limit")); err == nil && l > 0 {
			limit = l
		}
		offset, _ := strconv.Atoi(q.Get("offset"))
		typ := q.Get("type")
		site := q.Get("site")
		subID := q.Get("subId")

		matched := make([]companionapi.LibraryItemSummary, 0, len(s.items))
		for _, it := range s.items {
			if site != "" && site != "all" && it.Site != site {
				continue
			}
			if subID != "" && it.SubID != subID {
				continue
			}
			if typ == "video" && it.Kind != "video" {
				continue
			}
			if typ == "live" && it.Kind != "live" {
				continue
			}
			if typ == "image" && it.PrimaryKind != "image" {
				continue
			}
			matched = append(matched, it)
		}
		if offset > len(matched) {
			offset = len(matched)
		}
		end := offset + limit
		hasMore := end < len(matched)
		if end > len(matched) {
			end = len(matched)
		}
		var respAuthors []companionapi.LibraryAuthorSummary
		incAuthors := strings.ToLower(strings.TrimSpace(q.Get("includeAuthors")))
		if incAuthors == "1" || incAuthors == "true" {
			s.mu.Lock()
			if s.authorsBySite != nil {
				siteKey := site
				if siteKey == "" {
					siteKey = "all"
				}
				if list, ok := s.authorsBySite[siteKey]; ok {
					respAuthors = append([]companionapi.LibraryAuthorSummary(nil), list...)
				} else {
					respAuthors = []companionapi.LibraryAuthorSummary{}
				}
			} else {
				respAuthors = append([]companionapi.LibraryAuthorSummary(nil), s.authors...)
			}
			s.mu.Unlock()
		}
		_ = json.NewEncoder(w).Encode(companionapi.LibraryListResponse{
			Items:      matched[offset:end],
			Total:      len(matched),
			HasMore:    hasMore,
			NextOffset: end,
			Authors:    respAuthors,
		})

	case strings.HasPrefix(r.URL.Path, companionapi.PathLibraryItemsPrefix) && r.Method == http.MethodGet:
		key, _ := url.PathUnescape(strings.TrimPrefix(r.URL.Path, companionapi.PathLibraryItemsPrefix))
		det, ok := s.details[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(det)

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

func TestColumnsForWidthBreakpoints(t *testing.T) {
	cases := []struct {
		w    float32
		want int
	}{
		{360, 2},
		{412, 2},
		{599, 2},
		{600, 3},
		{800, 3},
		{900, 3},
		{901, 4},
		{1280, 4},
	}
	for _, tc := range cases {
		if got := ColumnsForWidth(tc.w); got != tc.want {
			t.Fatalf("ColumnsForWidth(%v) = %d, want %d", tc.w, got, tc.want)
		}
	}
}

func TestLibrary5000ItemsGridFilterSheetViewerAndReadOnlyDetail(t *testing.T) {
	srv, host, cleanup := newSyntheticLibServer(t, 5000)
	defer cleanup()

	p := New()
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

	pumpUntil(func() bool { return tt.HasText("媒体库条目 #0") })
	if !tt.HasText("媒体库条目 #0") {
		t.Fatalf("expected library grid loaded; texts=%v", tt.Texts())
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("NFR-MOB-PERF-001 violation: library first screen took %s (>2s)", elapsed)
	}

	// Verify responsive layout (2-col on 360/412, 3-col on 800) with zero horizontal overflow.
	assertNoHorizontalOverflow(t, tt, 412)
	saveOptionalShot(t, tt, "library-grid-412x915")

	tt.SetSize(360, 800)
	tt.Frame()
	assertNoHorizontalOverflow(t, tt, 360)
	saveOptionalShot(t, tt, "library-grid-360x800")

	tt.SetSize(800, 1280)
	tt.Frame()
	assertNoHorizontalOverflow(t, tt, 800)
	saveOptionalShot(t, tt, "library-grid-800x1280")

	tt.SetSize(412, 915)
	tt.Frame()

	// Tap video card #0 -> opens VideoPlayer with 2 parts.
	if err := tt.Click("打开 媒体库条目 #0"); err != nil {
		t.Fatalf("click video card: %v", err)
	}
	pumpUntil(func() bool { return openedVideo.ItemKey == "bilibili_lib_00000" })
	if openedVideo.ItemKey != "bilibili_lib_00000" || len(openedVideo.Tracks) != 2 {
		t.Fatalf("expected 2-part video opened, got %+v", openedVideo)
	}

	// Tap image card #1 -> opens ImageViewer with Weibo image_nowm and live photo motion.
	if err := tt.Click("打开 媒体库条目 #1"); err != nil {
		t.Fatalf("click image card: %v", err)
	}
	pumpUntil(func() bool { return len(openedImages) == 2 })
	if len(openedImages) != 2 || openedImages[0].NoWatermarkRelPath != "images/01_nowm.jpg" || openedImages[1].MotionRelPath != "images/02_live.mp4" {
		t.Fatalf("expected image viewer with noWatermark + motion, got %+v", openedImages)
	}

	// Tap "⋯" on item #0 -> opens read-only offline /library/item detail (zero delete/export entries).
	if err := tt.Click("详情 媒体库条目 #0"); err != nil {
		t.Fatalf("click detail button: %v", err)
	}
	tt.Advance(400 * time.Millisecond)
	pumpUntil(func() bool { return tt.HasText("这是 媒体库条目 #0 的离线只读详情正文。") })
	if !tt.HasText("分 P 列表（2）") || !tt.HasText("本机只读详情") {
		t.Fatalf("expected multi-P list and read-only footer on detail; texts=%v", tt.Texts())
	}
	// Strictly verify FR-MOB-025: no delete, export, import, or online parse actions.
	for _, forbidden := range []string{"删除", "导出", "导入", "在解析页打开"} {
		if _, found := tt.Find(forbidden); found {
			t.Fatalf("FR-MOB-025 violation: forbidden action %q found on library detail", forbidden)
		}
	}
	assertNoHorizontalOverflow(t, tt, 412)
	saveOptionalShot(t, tt, "library-detail-412x915")

	// Return to grid.
	if err := tt.Click("返回媒体库"); err != nil {
		t.Fatalf("click back to library: %v", err)
	}
	tt.Advance(400 * time.Millisecond)
	tt.Frame()

	// Open filter bottom sheet and apply type=video + site=bilibili + source=sub-0.
	if err := tt.Click("打开筛选"); err != nil {
		t.Fatalf("open filter sheet: %v", err)
	}
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	saveOptionalShot(t, tt, "library-filter-sheet-412x915")

	if err := tt.Click("类型 视频"); err != nil {
		t.Fatalf("select type video: %v", err)
	}
	tt.Frame()
	if err := tt.Click("站点 B 站"); err != nil {
		t.Fatalf("select site bilibili: %v", err)
	}
	tt.Frame()
	if err := tt.Click("来源 订阅 0"); err != nil {
		t.Fatalf("select sub-0: %v", err)
	}
	tt.Frame()
	if err := tt.Click("完成筛选"); err != nil {
		t.Fatalf("close filter sheet: %v", err)
	}
	pumpUntil(func() bool {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		if len(srv.queries) == 0 {
			return false
		}
		last := srv.queries[len(srv.queries)-1]
		return last.Get("type") == "video" && last.Get("site") == "bilibili" && last.Get("subId") == "sub-0"
	})
	srv.mu.Lock()
	lastQ := srv.queries[len(srv.queries)-1]
	srv.mu.Unlock()
	if lastQ.Get("type") != "video" || lastQ.Get("site") != "bilibili" || lastQ.Get("subId") != "sub-0" {
		t.Fatalf("unexpected filter query: %v", lastQ)
	}

	// Disconnect clears all in-memory data (NFR-MOB-DATA-001).
	p.OnDisconnected()
	p.mu.Lock()
	remItems := len(p.items)
	remDetail := p.detail
	p.mu.Unlock()
	if remItems != 0 || remDetail != nil || p.loader.Len() != 0 {
		t.Fatalf("expected all in-memory library data cleared on disconnect, got items=%d detail=%v imgs=%d", remItems, remDetail, p.loader.Len())
	}
}

func TestLibraryIncludeAuthorsClearOnEmptySiteScrollableSheetAndPartialMultiP(t *testing.T) {
	srv, host, cleanup := newSyntheticLibServer(t, 20)
	defer cleanup()

	// Seed 35 authors on "all" and empty authors on "x" to verify scrollable sheet
	// and stale-author clearing when switching to a site with zero authors.
	manyAuthors := make([]companionapi.LibraryAuthorSummary, 35)
	for i := range manyAuthors {
		manyAuthors[i] = companionapi.LibraryAuthorSummary{
			Kind:       "subscription",
			SubID:      fmt.Sprintf("sub-%02d", i),
			Name:       fmt.Sprintf("大量来源 #%02d", i),
			ItemCount:  i + 1,
			TotalBytes: int64(i+1) * (10 << 20),
		}
	}
	srv.mu.Lock()
	srv.authorsBySite = map[string][]companionapi.LibraryAuthorSummary{
		"all": manyAuthors,
		"x":   {},
	}
	// Seed a partial multi-P video where P1 is uncached (File == "") and P2 is cached ("p2.mp4"),
	// and a partial live recording where segment 1 is uncached (File == "") and segment 2 is cached.
	srv.details["partial_multip"] = companionapi.LibraryItemDetail{
		ItemKey:    "partial_multip",
		ID:         "partial_multip",
		Site:       "bilibili",
		Kind:       "video",
		Status:     "partial",
		Title:      "部分缓存多P视频",
		AuthorUID:  "u1",
		AuthorName: "测试UP",
		Parts: []companionapi.VideoPartDTO{
			{Page: 1, Title: "缺失第一P", Duration: 100, File: ""},
			{Page: 2, Title: "已缓存第二P", Duration: 200, File: "p2.mp4", QualityName: "1080P", Codecs: "AVC"},
		},
	}
	srv.details["partial_live"] = companionapi.LibraryItemDetail{
		ItemKey:    "partial_live",
		ID:         "partial_live",
		Site:       "bilibili",
		Kind:       "live",
		Status:     "partial",
		Title:      "部分缓存直播录像",
		AuthorUID:  "u1",
		AuthorName: "测试主播",
		Live: &companionapi.LiveInfoDTO{
			RoomID:      "7777",
			QualityName: "原画",
			Codec:       "AVC",
			Segments: []companionapi.LiveSegmentDTO{
				{File: "", DurationSec: 600},
				{File: "seg_02.mp4", DurationSec: 900, Bytes: 120 << 20, Codec: "AVC", QualityName: "原画"},
			},
		},
	}
	srv.mu.Unlock()

	p := New()
	var openedVideo mediaview.VideoRequest
	p.openVideoFn = func(_ pagekit.Host, req mediaview.VideoRequest) { openedVideo = req }

	notify := make(chan struct{}, 64)
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

	// 1. Verify includeAuthors=1 was sent and 35 authors loaded.
	pumpUntil(func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return len(p.authors) == 35
	})
	srv.mu.Lock()
	firstQ := srv.queries[0]
	srv.mu.Unlock()
	if firstQ.Get("includeAuthors") != "1" {
		t.Fatalf("expected includeAuthors=1 on initial load, got %v", firstQ)
	}

	// 2. Open filter sheet with 35 authors -> scrollable, zero overflow, header 完成 button stays clickable!
	if err := tt.Click("打开筛选"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	assertNoHorizontalOverflow(t, tt, 412)
	if !tt.HasText("大量来源 #00") {
		t.Fatalf("expected authors in filter sheet; texts=%v", tt.Texts())
	}

	// 3. Switch to site "x" (which returns empty authors) -> stale authors MUST clear!
	if err := tt.Click("站点 X"); err != nil {
		t.Fatal(err)
	}
	pumpUntil(func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.filter.Site == "x" && !p.loading && len(p.authors) == 0
	})
	p.mu.Lock()
	remAuthors := len(p.authors)
	p.mu.Unlock()
	if remAuthors != 0 {
		t.Fatalf("expected stale authors cleared on site with zero authors, got %d", remAuthors)
	}
	if err := tt.Click("完成筛选"); err != nil {
		t.Fatalf("header 完成 button must stay reachable inside scrollable sheet: %v", err)
	}
	tt.Advance(400 * time.Millisecond)
	tt.Frame()

	// 4. Verify partial multi-P detail maps P2 to StartIndex=0 (p2.mp4) and marks P1 未缓存.
	p.openItemDetail(host, "partial_multip")
	tt.Advance(400 * time.Millisecond)
	pumpUntil(func() bool { return tt.HasText("已缓存第二P") })
	if !tt.HasText("未缓存") {
		t.Fatalf("expected uncached P1 marked 未缓存; texts=%v", tt.Texts())
	}
	if err := tt.Click("播放 P2 · 已缓存第二P"); err != nil {
		t.Fatalf("click cached P2: %v", err)
	}
	if openedVideo.StartIndex != 0 || len(openedVideo.Tracks) != 1 || openedVideo.Tracks[0].RelPath != "p2.mp4" {
		t.Fatalf("expected P2 mapped to StartIndex=0 (p2.mp4), got %+v", openedVideo)
	}

	// 5. Verify partial live recording detail maps segment 2 to StartIndex=0 (seg_02.mp4).
	openedVideo = mediaview.VideoRequest{}
	p.openItemDetail(host, "partial_live")
	tt.Advance(400 * time.Millisecond)
	pumpUntil(func() bool { return tt.HasText("录像分段（2）") })
	if err := tt.Click("播放 分段 2"); err != nil {
		t.Fatalf("click cached live segment 2: %v", err)
	}
	if openedVideo.StartIndex != 0 || len(openedVideo.Tracks) != 1 || openedVideo.Tracks[0].RelPath != "seg_02.mp4" {
		t.Fatalf("expected live segment 2 mapped to StartIndex=0 (seg_02.mp4), got %+v", openedVideo)
	}
	p.OnDisconnected()
}

func TestLibraryDetailXThreadDedupMediaAnd360dpNoOverflow(t *testing.T) {
	srv, host, cleanup := newSyntheticLibServer(t, 2)
	defer cleanup()

	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC).Unix()

	// 1. Real cached form (concatenated Text + 1-based MediaIndexes + FromOrigin quote).
	realKey := "x:dynamic:1900000000000060001"
	realDet := companionapi.LibraryItemDetail{
		ItemKey:        realKey,
		ID:             "1900000000000060001",
		Site:           "x",
		Kind:           "dynamic",
		Status:         "complete",
		Title:          "详情串第一段",
		Text:           "详情串第一段正文\n\n详情串第二段正文\n\n详情串第三段收尾",
		PubTs:          now - 180,
		AuthorUID:      "u-x-det",
		AuthorName:     "离线详情作者",
		AuthorUniqueID: "offline_x_detail",
		SubName:        "离线X订阅",
		TotalBytes:     4 << 20,
		NotePages: []companionapi.NotePageDTO{
			{Index: 1, Kind: "image", Image: "images/01.jpg", Width: 900, Height: 600},
			{Index: 2, Kind: "image", FromOrigin: true, Image: "images/02_orig.jpg", Width: 900, Height: 600},
			{Index: 3, Kind: "video", Video: "images/03.mp4", Cover: "images/03.jpg", Duration: 19, Width: 1280, Height: 720},
		},
		Origin: &companionapi.ItemOriginDTO{
			Site:       "x",
			ID:         "1900000000000060099",
			AuthorName: "详情原帖作者",
			Text:       "详情引用原帖文案",
		},
		Thread: []companionapi.ThreadPartDTO{
			{ID: "1900000000000060001", PubTs: now - 180, Text: "详情串第一段正文", MediaIndexes: []int{1, 2}},
			{ID: "1900000000000060002", PubTs: now - 120, Text: "详情串第二段正文", MediaIndexes: []int{3}},
			{ID: "1900000000000060003", PubTs: now - 60, Text: "详情串第三段收尾"},
		},
	}

	// 2. Synthetic form (omits focal ID) with single-video focal.
	synthKey := "x:dynamic:1900000000000060010"
	synthDet := companionapi.LibraryItemDetail{
		ItemKey:        synthKey,
		ID:             "1900000000000060010",
		Site:           "x",
		Kind:           "dynamic",
		Status:         "complete",
		Title:          "合成单视频首帖",
		Text:           "合成单视频首帖正文",
		PubTs:          now - 300,
		Duration:       33,
		AuthorUID:      "u-x-det",
		AuthorName:     "离线详情作者",
		AuthorUniqueID: "offline_x_detail",
		Media: []companionapi.MediaFileDTO{
			{Role: "cover", Path: "cover.jpg"},
			{Role: "video", Path: "video.mp4", Duration: 33},
		},
		Thread: []companionapi.ThreadPartDTO{
			{ID: "1900000000000060011", PubTs: now - 250, Text: "合成单视频续帖正文"},
		},
	}

	// Verify synthetic resolver maps single video to focal without losing media.
	sParts, sFocal, _, sOK := resolveMobileDetailThread(synthDet)
	if !sOK || len(sParts) != 2 || sFocal != 0 || len(sParts[0].Videos) != 1 || sParts[0].Videos[0].Req.Tracks[0].RelPath != "video.mp4" {
		t.Fatalf("synthetic single-video detail thread resolution mismatch: parts=%+v focal=%d ok=%v", sParts, sFocal, sOK)
	}

	srv.mu.Lock()
	srv.details[realKey] = realDet
	srv.details[synthKey] = synthDet
	srv.mu.Unlock()

	p := New()
	p.nowFn = func() time.Time { return time.Unix(now, 0).UTC() }
	var openedImages []mediaview.ImageItem
	var openedVideo mediaview.VideoRequest
	p.openImageFn = func(_ pagekit.Host, items []mediaview.ImageItem, _ int) { openedImages = items }
	p.openVideoFn = func(_ pagekit.Host, req mediaview.VideoRequest) { openedVideo = req }

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
	}, 360, 1200)

	p.openItemDetail(host, realKey)
	tt.Advance(400 * time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tt.Frame()
		if tt.HasText("详情串第三段收尾") {
			break
		}
		select {
		case <-notify:
		case <-time.After(10 * time.Millisecond):
		}
	}

	// Verify zero duplicate text rendering (det.Text NOT rendered above det.Thread).
	texts := tt.Texts()
	for _, seg := range []string{"详情串第一段正文", "详情串第二段正文", "详情串第三段收尾", "详情引用原帖文案"} {
		count := 0
		for _, s := range texts {
			if strings.Contains(s, seg) {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("mobile detail rendered %q %d times (want 1); texts=%v", seg, count, texts)
		}
	}
	if _, ok := tt.Find("X离线串连线 1"); !ok {
		t.Fatal("mobile detail missing connector line 1")
	}
	if _, ok := tt.Find("X离线串连线 2"); !ok {
		t.Fatal("mobile detail missing connector line 2")
	}
	if _, ok := tt.Find("X离线串连线 3"); ok {
		t.Fatal("mobile detail must NOT draw connector line under final tweet")
	}
	if !tt.HasText("本机只读详情") {
		t.Fatal("mobile detail missing read-only footer")
	}
	for _, forbidden := range []string{"删除", "导出", "导入", "在解析页打开"} {
		if _, found := tt.Find(forbidden); found {
			t.Fatalf("FR-MOB-025 violation: forbidden action %q found on mobile thread detail", forbidden)
		}
	}

	if err := tt.Click("查看图片 1 / 1"); err != nil {
		t.Fatalf("click focal image on mobile detail: %v", err)
	}
	if len(openedImages) != 1 || openedImages[0].RelPath != "images/01.jpg" {
		t.Fatalf("expected focal image opened on mobile detail, got %+v", openedImages)
	}
	if err := tt.Click("播放 X 视频"); err != nil {
		t.Fatalf("click reply 2 video on mobile detail: %v", err)
	}
	if len(openedVideo.Tracks) != 1 || openedVideo.Tracks[0].RelPath != "images/03.mp4" {
		t.Fatalf("expected reply 2 video opened on mobile detail, got %+v", openedVideo)
	}

	for _, w := range []int{360, 412, 800} {
		tt.SetSize(w, 1200)
		tt.Frame()
		assertNoHorizontalOverflow(t, tt, w)
	}

	// Regression: quote-only origin media (all NotePages have FromOrigin=true + mirrored Media)
	// must NOT duplicate origin images in library detail.
	quoteOnlyKey := "x:dynamic:1900000000000060020"
	quoteOnlyDet := companionapi.LibraryItemDetail{
		ItemKey:        quoteOnlyKey,
		ID:             "1900000000000060020",
		Site:           "x",
		Kind:           "dynamic",
		Status:         "complete",
		Title:          "仅引用媒体详情首帖",
		Text:           "仅引用媒体详情首帖",
		PubTs:          now - 40,
		AuthorUID:      "u-x-det",
		AuthorName:     "离线详情作者",
		AuthorUniqueID: "offline_x_detail",
		NotePages: []companionapi.NotePageDTO{
			{Index: 1, Kind: "image", FromOrigin: true, Image: "images/01.png", Width: 320, Height: 200},
			{Index: 2, Kind: "video", GIF: true, FromOrigin: true, Image: "images/02.png", Video: "images/02.mp4", Width: 270, Height: 360, Duration: 3},
		},
		Media: []companionapi.MediaFileDTO{
			{Role: "image", Path: "images/01.png", FromOrigin: true, Width: 320, Height: 200},
			{Role: "cover", Path: "images/02.png", FromOrigin: true, Width: 270, Height: 360},
			{Role: "video", Path: "images/02.mp4", GIF: true, FromOrigin: true, Duration: 3},
		},
		Origin: &companionapi.ItemOriginDTO{
			Site:       "x",
			ID:         "1900000000000060098",
			AuthorName: "详情原帖作者",
			Text:       "仅引用媒体原帖正文",
		},
		Thread: []companionapi.ThreadPartDTO{
			{ID: "1900000000000060021", PubTs: now - 20, Text: "仅引用媒体详情自回复"},
		},
	}
	qParts, qFocal, qOrigImgs, qOK := resolveMobileDetailThread(quoteOnlyDet)
	if !qOK || len(qParts) != 2 || qFocal != 0 {
		t.Fatalf("resolveMobileDetailThread(quote-only origin) = (len=%d, focal=%d, ok=%v), want (2, 0, true)", len(qParts), qFocal, qOK)
	}
	if len(qParts[0].Images) != 0 || len(qParts[0].Videos) != 0 || len(qParts[1].Images) != 0 {
		t.Fatalf("quote-only origin detail outer parts must stay media-free, got parts=%+v", qParts)
	}
	if len(qOrigImgs) != 2 || qOrigImgs[0].RelPath != "images/01.png" || qOrigImgs[1].RelPath != "images/02.png" || !qOrigImgs[1].GIF || qOrigImgs[1].MotionRelPath != "images/02.mp4" {
		t.Fatalf("quote-only origin detail images must not duplicate and must keep GIF+MotionRelPath, got %+v", qOrigImgs)
	}

	srv.mu.Lock()
	srv.details[quoteOnlyKey] = quoteOnlyDet
	srv.mu.Unlock()
	p.openItemDetail(host, quoteOnlyKey)
	tt.Advance(400 * time.Millisecond)
	deadline2 := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline2) {
		tt.Frame()
		if tt.HasText("仅引用媒体详情自回复") {
			break
		}
		select {
		case <-notify:
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, ok := tt.Find("查看图片 1 / 2"); !ok {
		t.Fatalf("expected 2 origin image tiles (1 / 2) in detail; texts=%v", tt.Texts())
	}
	if _, ok := tt.Find("查看图片 2 / 2"); !ok {
		t.Fatalf("expected 2 origin image tiles (2 / 2) in detail; texts=%v", tt.Texts())
	}
	if _, ok := tt.Find("查看图片 3 / 4"); ok {
		t.Fatal("detail origin images were duplicated (found 3 / 4)")
	}
	if !tt.HasText("GIF") || tt.HasText("实况") {
		t.Fatalf("expected detail GIF badge without '实况'; texts=%v", tt.Texts())
	}
	tt.SetSize(360, 1200)
	tt.Frame()
	assertNoHorizontalOverflow(t, tt, 360)
	p.OnDisconnected()
}
