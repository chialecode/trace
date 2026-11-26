package subscriptions

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/cmd/trace-mobile/internal/pagekit"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/theme"
	"github.com/chialecode/trace/internal/companionapi"
	"github.com/chialecode/trace/internal/companionclient"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/videoplayer"
	"github.com/egoist/mygo/ui"
)

type fakeHost struct {
	mu         sync.Mutex
	connState  pagekit.ConnState
	desktop    pagekit.DesktopSnapshot
	client     *companionclient.Client
	proxy      *companionclient.MediaProxy
	imgCache   *companionclient.MemoryImageCache
	router     *ui.Router
	invalidate func()
	openedURLs []string
	pushedWins []pushedWinRecord
	tasksBadge int
}

type pushedWinRecord struct {
	title string
	view  func(c *ui.Context)
}

func newFakeHost(cli *companionclient.Client, proxy *companionclient.MediaProxy) *fakeHost {
	r := ui.NewRouter("/")
	r.SystemStack = true
	return &fakeHost{
		connState: pagekit.ConnConnected,
		desktop: pagekit.DesktopSnapshot{
			ComputerName: "Studio-PC",
			Version:      "0.1.0-dev",
			Channel:      "dev",
			ChannelLabel: "开发版",
			LoggedIn:     true,
		},
		client:   cli,
		proxy:    proxy,
		imgCache: companionclient.NewMemoryImageCache(8 << 20),
		router:   r,
	}
}

func (h *fakeHost) ConnState() pagekit.ConnState                  { return h.connState }
func (h *fakeHost) Desktop() pagekit.DesktopSnapshot              { return h.desktop }
func (h *fakeHost) Client() *companionclient.Client               { return h.client }
func (h *fakeHost) MediaProxy() *companionclient.MediaProxy       { return h.proxy }
func (h *fakeHost) ImageCache() *companionclient.MemoryImageCache { return h.imgCache }
func (h *fakeHost) Window() *mygo.Window                          { return nil }
func (h *fakeHost) Router() *ui.Router                            { return h.router }
func (h *fakeHost) PushRoute(path string)                         { h.router.Push(path) }
func (h *fakeHost) Back()                                         { h.router.Back() }
func (h *fakeHost) SelectTab(pagekit.TabID)                       {}
func (h *fakeHost) RefreshStatus()                                {}
func (h *fakeHost) Unpair()                                       {}

func (h *fakeHost) FetchImage(ctx context.Context, path string) ([]byte, error) {
	if raw, ok := h.imgCache.Get(path); ok {
		return raw, nil
	}
	if h.client == nil {
		return nil, &companionclient.Error{Kind: companionclient.KindUnreachable}
	}
	raw, err := h.client.FetchBytes(ctx, path)
	if err != nil {
		return nil, err
	}
	h.imgCache.Put(path, raw)
	return raw, nil
}

func (h *fakeHost) PushWindow(title string, view func(c *ui.Context)) *mygo.Window {
	h.mu.Lock()
	h.pushedWins = append(h.pushedWins, pushedWinRecord{title: title, view: view})
	h.mu.Unlock()
	return nil
}

func (h *fakeHost) Update(fn func()) {
	if fn != nil {
		fn()
	}
	if h.invalidate != nil {
		h.invalidate()
	}
}

func (h *fakeHost) SetTasksBadge(count int) {
	h.mu.Lock()
	h.tasksBadge = count
	h.mu.Unlock()
}

func (h *fakeHost) OpenExternal(rawURL string) error {
	h.mu.Lock()
	h.openedURLs = append(h.openedURLs, rawURL)
	h.mu.Unlock()
	return nil
}

func startFakeCompanionServer(t *testing.T, handler http.HandlerFunc) (*companionclient.Client, *companionclient.MediaProxy, func()) {
	t.Helper()
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

	srv := httptest.NewUnstartedServer(handler)
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
	cli, err := companionclient.NewClient(rec, "tok-test-12345678901234567890", "0.1.0-dev", "dev")
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
	cleanup := func() {
		_ = proxy.Close()
		cli.Close()
		srv.Close()
	}
	return cli, proxy, cleanup
}

func tinyPNG(col color.RGBA) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			img.SetRGBA(x, y, col)
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
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

func assertNoForbiddenEntries(t *testing.T, tt *ui.Tester) {
	t.Helper()
	forbidden := []string{"退出登录", "粘贴 Cookie", "删除订阅", "删除媒体", "退出电脑", "导入订阅", "导出订阅", "修改设置"}
	for _, txt := range tt.Texts() {
		for _, bad := range forbidden {
			if strings.Contains(txt, bad) {
				t.Fatalf("FR-MOB-025 violation: forbidden entry %q found in text %q", bad, txt)
			}
		}
	}
}

func saveShotIfRequested(t *testing.T, tt *ui.Tester, name string) {
	t.Helper()
	dir := strings.TrimSpace(os.Getenv("TRACE_OPS_SHOTS_DIR"))
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

func sampleSubscriptionsFixture() ([]companionapi.SubscriptionDTO, []companionapi.CheckPlanRowDTO, []companionapi.FeedItem) {
	subs := []companionapi.SubscriptionDTO{
		{
			ID:          "sub-1",
			Name:        "科技评测组",
			Note:        "数码与摄影双平台创作者",
			AvatarPath:  "/api/v1/avatars/bilibili/1001",
			PresetLabel: "完整存档",
			Tags:        []string{"scheduled_check", "cache_video", "cache_draw", "auto_record"},
			TagLabels:   []string{"定时检查", "自动缓存视频", "自动缓存图文", "开播录制"},
			Links: []companionapi.CustomLinkDTO{
				{Label: "个人主页", URL: "https://example.com/creator"},
			},
			Live:      true,
			Recording: false,
			Accounts: []companionapi.SubscriptionAccountDTO{
				// Deliberately unsorted to verify fixed site order (B站 -> 抖音 -> 微博 -> X).
				{
					SubID: "sub-1", SubName: "科技评测组", Site: "x", UID: "x-9", UniqueID: "tech_x",
					Name: "TechX", TotalWorks: 42, CachedWorks: 10, LiveSupported: false,
				},
				{
					SubID: "sub-1", SubName: "科技评测组", Site: "weibo", UID: "wb-7",
					Name: "科技评测微博", TotalWorks: 88, CachedWorks: 20, LiveSupported: false,
				},
				{
					SubID: "sub-1", SubName: "科技评测组", Site: "douyin", UID: "dy-5", UniqueID: "tech_dy",
					Name: "科技评测抖音", TotalWorks: 64, CachedWorks: 30,
					LiveSupported: true, LiveStatus: 1, LiveTitle: "新品开箱直播", Recording: true, RecordingKey: "rec-dy-5",
					QualityName: "原画", Codec: "AVC",
				},
				{
					SubID: "sub-1", SubName: "科技评测组", Site: "bilibili", UID: "1001",
					Name: "科技评测B站", TotalWorks: 120, CachedWorks: 95,
					LiveSupported: true, LiveStatus: 1, LiveTitle: "周末答疑直播", Recording: false,
				},
			},
		},
		{
			ID:          "sub-2",
			Name:        "插画记录",
			PresetLabel: "追更并保存",
			TagLabels:   []string{"定时检查", "自动缓存图文"},
			Live:        true,
			Recording:   true,
			Accounts: []companionapi.SubscriptionAccountDTO{
				{
					SubID: "sub-2", SubName: "插画记录", Site: "bilibili", UID: "2002",
					Name: "插画师B站", TotalWorks: 50, CachedWorks: 40,
					LiveSupported: true, LiveStatus: 1, Recording: true, RecordingKey: "rec-b-2002",
				},
			},
		},
	}

	now := time.Now().UTC()
	plan := []companionapi.CheckPlanRowDTO{
		{
			SubID: "sub-1", SubName: "科技评测组", Site: "bilibili", UID: "1001", Name: "科技评测B站",
			AutoRecord: true, LastCheckedAt: now.Add(-20 * time.Minute).Format(time.RFC3339),
			LastKind: "ok", Plan: "每小时", NextAt: now.Add(40 * time.Minute).Format(time.RFC3339),
			LiveCheckedAt: now.Add(-2 * time.Minute).Format(time.RFC3339), LiveEverySec: 180,
		},
		{
			SubID: "sub-1", SubName: "科技评测组", Site: "weibo", UID: "wb-7", Name: "科技评测微博",
			LastCheckedAt: now.Add(-30 * time.Minute).Format(time.RFC3339),
			LastKind:      "ok", Plan: "每 2 小时", NextAt: now.Add(90 * time.Minute).Format(time.RFC3339),
			LiveNote: "微博直播暂不支持录制",
		},
		{
			SubID: "sub-1", SubName: "科技评测组", Site: "x", UID: "x-9", Name: "TechX",
			Plan: "每 2 小时", AfterBackoff: true, NextAt: now.Add(2 * time.Hour).Format(time.RFC3339),
			LiveNote: "X 直播与 Spaces 暂不支持录制",
		},
	}

	feed := []companionapi.FeedItem{
		{
			FeedKey:        "f-1",
			ItemKey:        "bilibili:video:BV1TEST",
			Site:           "bilibili",
			Kind:           "video",
			AuthorUID:      "1001",
			AuthorName:     "科技评测B站",
			SubID:          "sub-1",
			SubName:        "科技评测组",
			Title:          "旗舰相机夜拍实测",
			Text:           "对比三款机型夜景动态范围与防抖表现。",
			PubTs:          now.Add(-2 * time.Hour).Unix(),
			Duration:       625,
			CoverRelPath:   "cover.jpg",
			VideoRelPath:   "video.mp4",
			QualitySummary: "4K · AVC",
		},
		{
			FeedKey:    "f-2",
			ItemKey:    "weibo:dynamic:500001",
			Site:       "weibo",
			Kind:       "dynamic",
			AuthorUID:  "wb-7",
			AuthorName: "科技评测微博",
			SubID:      "sub-1",
			SubName:    "科技评测组",
			Title:      "实拍样张合集",
			Text:       "附带无水印原图，点开可双指缩放查看。",
			PubTs:      now.Add(-5 * time.Hour).Unix(),
			Images: []companionapi.FeedImage{
				{Path: "images/01.jpg", NoWatermark: "images/01_nowm.jpg"},
				{Path: "images/02.jpg"},
			},
		},
	}
	return subs, plan, feed
}

func TestSubscriptionsAllSegmentsDetailControlsAndErrors(t *testing.T) {
	subs, plan, feed := sampleSubscriptionsFixture()
	pngData := tinyPNG(color.RGBA{R: 26, G: 115, B: 232, A: 255})

	var (
		mu              sync.Mutex
		checkAllCalls   int
		checkSubIDs     []string
		checkAcctReqs   []companionapi.CheckAccountRequest
		checkCancelHit  int
		liveStartReqs   []string
		liveStopKeys    []string
		extraLiveChecks atomic.Int32
		nextCheckErr    *companionapi.ErrorDetail
		nextCheckStatus int
	)

	cli, proxy, cleanup := startFakeCompanionServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == companionapi.PathSubscriptions:
			_ = json.NewEncoder(w).Encode(companionapi.SubscriptionsResponse{Items: subs})
		case r.Method == http.MethodGet && r.URL.Path == companionapi.PathSubscriptionsAccounts:
			var all []companionapi.SubscriptionAccountDTO
			for _, s := range subs {
				all = append(all, s.Accounts...)
			}
			_ = json.NewEncoder(w).Encode(companionapi.AllAccountsResponse{Items: all})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, companionapi.PathSubscriptionsPrefix):
			id := strings.TrimPrefix(r.URL.Path, companionapi.PathSubscriptionsPrefix)
			for _, s := range subs {
				if s.ID == id {
					_ = json.NewEncoder(w).Encode(companionapi.SubscriptionDetailResponse{Subscription: s})
					return
				}
			}
			http.NotFound(w, r)
		case r.Method == http.MethodGet && r.URL.Path == companionapi.PathFeed:
			_ = json.NewEncoder(w).Encode(companionapi.FeedPageResponse{Items: feed})
		case r.Method == http.MethodGet && r.URL.Path == companionapi.PathChecksPlan:
			_ = json.NewEncoder(w).Encode(companionapi.CheckPlanResponse{Items: plan, Status: "下一轮定时检查：今天 22:00"})
		case r.Method == http.MethodPost && r.URL.Path == companionapi.PathChecks:
			var req companionapi.CheckRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			mu.Lock()
			if nextCheckErr != nil {
				st := nextCheckStatus
				ed := *nextCheckErr
				nextCheckErr = nil
				mu.Unlock()
				w.WriteHeader(st)
				_ = json.NewEncoder(w).Encode(companionapi.ErrorBody{Error: ed})
				return
			}
			if len(req.SubIDs) == 0 {
				checkAllCalls++
			} else {
				checkSubIDs = append(checkSubIDs, req.SubIDs...)
			}
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(companionapi.ActionResponse{OK: true, Message: "正在检查 1 / 3"})
		case r.Method == http.MethodPost && r.URL.Path == companionapi.PathChecksAccount:
			var req companionapi.CheckAccountRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			mu.Lock()
			if nextCheckErr != nil {
				st := nextCheckStatus
				ed := *nextCheckErr
				nextCheckErr = nil
				mu.Unlock()
				w.WriteHeader(st)
				_ = json.NewEncoder(w).Encode(companionapi.ErrorBody{Error: ed})
				return
			}
			checkAcctReqs = append(checkAcctReqs, req)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(companionapi.ActionResponse{OK: true, Message: "已开始检查该账号"})
		case r.Method == http.MethodPost && r.URL.Path == companionapi.PathChecksCancel:
			mu.Lock()
			checkCancelHit++
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(companionapi.ActionResponse{OK: true, Message: "已取消检查更新"})
		case r.Method == http.MethodPost && r.URL.Path == companionapi.PathLiveStart:
			var req companionapi.StartLiveRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			mu.Lock()
			liveStartReqs = append(liveStartReqs, req.AccountKey)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(companionapi.LiveRecordingDTO{RecordingKey: "rec-new-1", AccountKey: req.AccountKey, Recording: true})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, companionapi.PathLiveRecordingsPrefix) && strings.HasSuffix(r.URL.Path, "/stop"):
			key := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, companionapi.PathLiveRecordingsPrefix), "/stop")
			mu.Lock()
			liveStopKeys = append(liveStopKeys, key)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(companionapi.LiveRecordingDTO{RecordingKey: key, Recording: false})
		case strings.HasPrefix(r.URL.Path, companionapi.PathAvatarPrefix), strings.HasPrefix(r.URL.Path, companionapi.PathMediaPrefix):
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngData)
		default:
			if strings.Contains(r.URL.Path, "live/status") {
				extraLiveChecks.Add(1)
			}
			http.NotFound(w, r)
		}
	})
	defer cleanup()

	host := newFakeHost(cli, proxy)
	pg := New()
	pg.RefreshThrottle = 20 * time.Millisecond

	notify := make(chan struct{}, 64)
	host.invalidate = func() {
		select {
		case notify <- struct{}{}:
		default:
		}
	}

	viewFn := func(c *ui.Context) {
		c.SetTheme(theme.TraceTheme())
		pg.View(c, host)
	}
	tt := ui.NewTester(viewFn, 412, 915)
	pumpUntil := func(cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
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
		t.Fatalf("condition timed out; texts=%v", tt.Texts())
	}

	pg.OnConnected(host)
	pumpUntil(func() bool { return pg.subsLoaded && pg.accountsLoaded && pg.planLoaded })

	// 1. 全部订阅 segment: avatar grid + live/recording badges.
	for _, want := range []string{"全部订阅", "全部账号", "检查计划", "科技评测组", "插画记录", "直播", "录制中"} {
		if !tt.HasText(want) {
			t.Fatalf("全部订阅 missing %q; got %v", want, tt.Texts())
		}
	}
	assertNoHorizontalOverflow(t, tt, 412)
	assertNoForbiddenEntries(t, tt)
	saveShotIfRequested(t, tt, "subs-grid-412x915")

	// 2. 全部账号 segment: fixed site order B站 -> 抖音 -> 微博 -> X & works/cached counts.
	pg.activeSeg = segAllAccounts
	tt.Frame()
	texts := tt.Texts()
	idxB := indexContaining(texts, "B 站（2）")
	idxD := indexContaining(texts, "抖音（1）")
	idxW := indexContaining(texts, "微博（1）")
	idxX := indexContaining(texts, "X（1）")
	if !(idxB >= 0 && idxB < idxD && idxD < idxW && idxW < idxX) {
		t.Fatalf("expected fixed site order B站->抖音->微博->X, got indices B=%d D=%d W=%d X=%d texts=%v", idxB, idxD, idxW, idxX, texts)
	}
	for _, want := range []string{"作品 120 · 已缓存 95", "微博 88 · 已缓存 20", "帖子 42 · 已缓存 10"} {
		if !tt.HasText(want) {
			t.Fatalf("全部账号 missing works count %q", want)
		}
	}
	assertNoHorizontalOverflow(t, tt, 412)
	assertNoForbiddenEntries(t, tt)
	saveShotIfRequested(t, tt, "subs-accounts-412x915")

	// 3. 检查计划 segment: schedule, "检查全部", per-row "检查", WS subs:check progress banner + "取消".
	pg.activeSeg = segCheckPlan
	tt.Frame()
	for _, want := range []string{"检查全部", "微博直播暂不支持录制", "X 直播与 Spaces 暂不支持录制", "退避后"} {
		if !tt.HasText(want) {
			t.Fatalf("检查计划 missing %q; got %v", want, tt.Texts())
		}
	}
	assertNoHorizontalOverflow(t, tt, 412)
	saveShotIfRequested(t, tt, "subs-plan-412x915")

	// Click "检查全部" -> starts check + shows live progress banner with "取消".
	if err := tt.Click("检查全部订阅"); err != nil {
		t.Fatal(err)
	}
	pumpUntil(func() bool {
		mu.Lock()
		defer mu.Unlock()
		return checkAllCalls == 1
	})
	pg.OnEvent(host, companionclient.EventFrame{
		Type:    companionapi.EventSubsCheck,
		Message: "progress:正在检查 2 / 3（科技评测B站）",
	})
	tt.Frame()
	if !tt.HasText("正在检查 2 / 3") {
		t.Fatalf("expected live check progress banner, got %v", tt.Texts())
	}
	if err := tt.Click("取消检查更新"); err != nil {
		t.Fatal(err)
	}
	pumpUntil(func() bool {
		mu.Lock()
		defer mu.Unlock()
		return checkCancelHit == 1 && !pg.checking
	})

	// Per-row "检查" + honest auth_required error ("请先在电脑上登录微博").
	mu.Lock()
	nextCheckStatus = http.StatusPreconditionFailed
	nextCheckErr = &companionapi.ErrorDetail{Code: companionapi.CodeAuthRequired, Site: "weibo", Message: "请先在电脑上登录微博"}
	mu.Unlock()
	if err := tt.Click("检查账号：科技评测微博"); err != nil {
		t.Fatal(err)
	}
	pumpUntil(func() bool { return strings.Contains(pg.actionErr, "请先在电脑上登录微博") })
	if !tt.HasText("请先在电脑上登录微博") {
		t.Fatalf("expected auth_required message on screen, got %v", tt.Texts())
	}

	// Verify risk_control and rate_limited via WS subs:check events.
	pg.OnEvent(host, companionclient.EventFrame{
		Type:    companionapi.EventSubsCheck,
		Site:    "douyin",
		State:   companionapi.CodeRiskControl,
		Message: "触发抖音风控，已暂停检查",
	})
	tt.Frame()
	if !tt.HasText("触发抖音风控") {
		t.Fatalf("expected risk_control error, got %v", tt.Texts())
	}
	pg.OnEvent(host, companionclient.EventFrame{
		Type:    companionapi.EventSubsCheck,
		Site:    "x",
		State:   companionapi.CodeRateLimited,
		Message: "X 触发限流，请稍后再试",
	})
	tt.Frame()
	if !tt.HasText("X 触发限流") {
		t.Fatalf("expected rate_limited error, got %v", tt.Texts())
	}

	// Normal per-row account check succeeds.
	if err := tt.Click("检查账号：科技评测B站"); err != nil {
		t.Fatal(err)
	}
	pumpUntil(func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(checkAcctReqs) == 1
	})
	pg.OnEvent(host, companionclient.EventFrame{Type: companionapi.EventSubsCheck, State: "done", Message: "检查完成"})
	tt.Frame()

	// 4. Open Subscription Detail from 全部订阅 grid.
	pg.activeSeg = segAllSubs
	tt.Frame()
	if err := tt.Click("打开订阅：科技评测组"); err != nil {
		t.Fatal(err)
	}
	pumpUntil(func() bool { return pg.detailSub != nil && len(pg.detailFeed) == 2 })
	tt.Advance(500 * time.Millisecond)
	settleAnim(tt)

	// Verify header: preset, read-only capability tags, clickable custom link, content feed.
	for _, want := range []string{"追踪：完整存档", "定时检查", "自动缓存视频", "开播录制", "个人主页", "旗舰相机夜拍实测", "实拍样张合集"} {
		if !tt.HasText(want) {
			t.Fatalf("detail content missing %q; got %v", want, tt.Texts())
		}
	}
	assertNoHorizontalOverflow(t, tt, 412)
	assertNoForbiddenEntries(t, tt)
	saveShotIfRequested(t, tt, "subs-detail-content-412x915")

	// Tap custom external link -> calls Host.OpenExternal.
	if err := tt.Click("打开外部链接：个人主页"); err != nil {
		t.Fatal(err)
	}
	host.mu.Lock()
	gotURLs := append([]string(nil), host.openedURLs...)
	host.mu.Unlock()
	if len(gotURLs) != 1 || gotURLs[0] != "https://example.com/creator" {
		t.Fatalf("expected OpenExternal(https://example.com/creator), got %v", gotURLs)
	}

	// Tap "⋯" -> "检查更新" calls POST /api/v1/checks with subIds:[sub-1].
	if err := tt.Click("订阅更多操作"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(500 * time.Millisecond)
	settleAnim(tt)
	if err := tt.Click("检查更新"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(500 * time.Millisecond)
	settleAnim(tt)
	pumpUntil(func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(checkSubIDs) == 1 && checkSubIDs[0] == "sub-1"
	})
	pg.OnEvent(host, companionclient.EventFrame{Type: companionapi.EventSubsCheck, State: "done"})
	tt.Frame()

	// Tap video & image in detail Content tab -> calls Host.PushWindow.
	if err := tt.Click("播放视频：旗舰相机夜拍实测"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("查看大图 1/2"); err != nil {
		t.Fatal(err)
	}
	host.mu.Lock()
	winCount := len(host.pushedWins)
	imgWin := host.pushedWins[winCount-1]
	host.mu.Unlock()
	if winCount != 2 {
		t.Fatalf("expected 2 PushWindow calls (video + image), got %d", winCount)
	}
	imgTT := ui.NewTester(imgWin.view, 412, 915)
	if !imgTT.HasText("1 / 2") || !imgTT.HasText("查看原图") || !imgTT.HasText("无水印") {
		t.Fatalf("expected zoomable image viewer controls, got %v", imgTT.Texts())
	}

	// 5. Switch detail to "账号": verify last-known live status (zero extra queries),
	// "开始录制" on B站, "停止录制" on 抖音, and ZERO recording buttons on Weibo & X!
	pg.detailTab = detailTabAccounts
	tt.Frame()
	if extraLiveChecks.Load() != 0 {
		t.Fatalf("expected zero extra live status queries, got %d", extraLiveChecks.Load())
	}
	if _, ok := tt.Find("开始录制：科技评测B站"); !ok {
		t.Fatalf("expected 开始录制 button for live B站 account")
	}
	if _, ok := tt.Find("停止录制：科技评测抖音"); !ok {
		t.Fatalf("expected 停止录制 button for recording 抖音 account")
	}
	for _, forbiddenBtn := range []string{"开始录制：科技评测微博", "停止录制：科技评测微博", "开始录制：TechX", "停止录制：TechX"} {
		if _, ok := tt.Find(forbiddenBtn); ok {
			t.Fatalf("FR-MOB-021 violation: Weibo/X must NOT show recording button %q", forbiddenBtn)
		}
	}
	assertNoHorizontalOverflow(t, tt, 412)
	assertNoForbiddenEntries(t, tt)
	saveShotIfRequested(t, tt, "subs-detail-accounts-412x915")

	// Trigger live start on B站 and live stop on 抖音.
	if err := tt.Click("开始录制：科技评测B站"); err != nil {
		t.Fatal(err)
	}
	pumpUntil(func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(liveStartReqs) == 1 && liveStartReqs[0] == "bilibili:1001" && pg.busyLiveKey == ""
	})
	if err := tt.Click("停止录制：科技评测抖音"); err != nil {
		t.Fatal(err)
	}
	pumpUntil(func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(liveStopKeys) == 1 && liveStopKeys[0] == "rec-dy-5" && pg.busyLiveKey == ""
	})

	// 6. Responsive widths 360x800, 412x915, 800x1280 with zero overflow.
	for _, sz := range [][2]int{{360, 800}, {412, 915}, {800, 1280}} {
		host.router = ui.NewRouter("/")
		host.router.SystemStack = true
		for _, seg := range []int{segAllSubs, segAllAccounts, segCheckPlan} {
			pg.activeSeg = seg
			rTT := ui.NewTester(viewFn, sz[0], sz[1])
			settleAnim(rTT)
			assertNoHorizontalOverflow(t, rTT, sz[0])
			assertNoForbiddenEntries(t, rTT)
		}
		host.router = ui.NewRouter("/sub/sub-1")
		host.router.SystemStack = true
		pg.detailTab = detailTabContent
		rTT := ui.NewTester(viewFn, sz[0], sz[1])
		settleAnim(rTT)
		assertNoHorizontalOverflow(t, rTT, sz[0])
		if sz[0] == 360 {
			saveShotIfRequested(t, rTT, "subs-detail-360x800")
		}
		if sz[0] == 800 {
			saveShotIfRequested(t, rTT, "subs-detail-800x1280")
		}
		pg.detailTab = detailTabAccounts
		rTT.Frame()
		assertNoHorizontalOverflow(t, rTT, sz[0])
	}

	// 7. OnDisconnected clears all in-memory state & bitmaps.
	pg.OnDisconnected()
	if len(pg.subs) != 0 || len(pg.accounts) != 0 || len(pg.plan) != 0 || pg.detailSub != nil || len(pg.detailFeed) != 0 {
		t.Fatal("expected OnDisconnected to clear all in-memory subscription data")
	}
	pg.imgMu.Lock()
	bmLen := len(pg.bitmaps)
	pg.imgMu.Unlock()
	if bmLen != 0 {
		t.Fatalf("expected OnDisconnected to clear all bitmaps, got %d", bmLen)
	}
}

func TestLookupBitmapFailureCooldownNoRequestStorm(t *testing.T) {
	var hits atomic.Int32
	cli, proxy, cleanup := startFakeCompanionServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case companionapi.PathSubscriptions:
			_ = json.NewEncoder(w).Encode(companionapi.SubscriptionsResponse{
				Items: []companionapi.SubscriptionDTO{
					{ID: "sub-404", Name: "缺失头像订阅", AvatarPath: "/api/v1/avatars/bilibili/missing"},
				},
			})
		case companionapi.PathSubscriptionsAccounts:
			_ = json.NewEncoder(w).Encode(companionapi.AllAccountsResponse{})
		case companionapi.PathChecksPlan:
			_ = json.NewEncoder(w).Encode(companionapi.CheckPlanResponse{})
		case "/api/v1/avatars/bilibili/missing":
			hits.Add(1)
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	defer cleanup()

	host := newFakeHost(cli, proxy)
	pg := New()
	viewFn := func(c *ui.Context) {
		c.SetTheme(theme.TraceTheme())
		pg.View(c, host)
	}
	tt := ui.NewTester(viewFn, 412, 915)
	pg.OnConnected(host)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tt.Frame()
		if pg.subsLoaded && hits.Load() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Render 10 consecutive frames after the 404 failure: cooldown must prevent any storm.
	time.Sleep(40 * time.Millisecond)
	for i := 0; i < 10; i++ {
		tt.Frame()
	}
	time.Sleep(40 * time.Millisecond)
	if got := hits.Load(); got != 1 {
		t.Fatalf("expected exactly 1 HTTP request for 404 avatar under failure cooldown, got %d", got)
	}
	pg.OnDisconnected()
	pg.imgMu.Lock()
	failLen := len(pg.imgFailUntil)
	pg.imgMu.Unlock()
	if failLen != 0 {
		t.Fatalf("expected OnDisconnected to clear imgFailUntil, got %d", failLen)
	}
}

func TestVideoPlaybackErrorClassificationAndChildWindowCleanup(t *testing.T) {
	// Network/stream error must NOT be mislabeled as unsupported codec.
	unsup, playErr := classifyVideoPlaybackError("AVC", videoplayer.State{Error: "network timeout reading stream"})
	if unsup != "" || !strings.Contains(playErr, "network timeout") {
		t.Fatalf("expected honest playback error for network timeout, got unsup=%q playErr=%q", unsup, playErr)
	}

	// Codec/decoder error reports unsupported codec honestly.
	unsup, playErr = classifyVideoPlaybackError("AV1", videoplayer.State{Error: "MediaCodec decoder unsupported"})
	if unsup != "这台手机不能播放 AV1 视频" || playErr != "" {
		t.Fatalf("expected AV1 unsupported codec message, got unsup=%q playErr=%q", unsup, playErr)
	}

	// Ready with Width==0 && Height==0 (Android MediaPlayer silent video drop) reports unsupported codec.
	unsup, playErr = classifyVideoPlaybackError("HEVC", videoplayer.State{Ready: true, Width: 0, Height: 0})
	if unsup != "这台手机不能播放 HEVC 视频" || playErr != "" {
		t.Fatalf("expected HEVC unsupported codec message on 0x0 ready, got unsup=%q playErr=%q", unsup, playErr)
	}

	// Multiple open child windows (image viewer + video player) are all tracked and cleaned up on OnDisconnected.
	pg := New()
	var cleaned atomic.Int32
	id1 := pg.registerChildWindow(func() { cleaned.Add(1) })
	id2 := pg.registerChildWindow(func() { cleaned.Add(1) })
	if id1 == 0 || id2 == 0 || id1 == id2 {
		t.Fatalf("expected distinct child window IDs, got %d and %d", id1, id2)
	}
	pg.OnDisconnected()
	if cleaned.Load() != 2 {
		t.Fatalf("expected OnDisconnected to close all 2 child windows, got %d", cleaned.Load())
	}
	pg.childMu.Lock()
	remaining := len(pg.activeChildClosers)
	pg.childMu.Unlock()
	if remaining != 0 {
		t.Fatalf("expected empty activeChildClosers after disconnect, got %d", remaining)
	}
}

func TestLeavingDetailClearsStateAndStopsStalePollingAndPreservesSameSiteOrder(t *testing.T) {
	var detailSubHits, feedHits atomic.Int32
	sub := companionapi.SubscriptionDTO{
		ID:   "sub-order",
		Name: "双B站号测试",
		Accounts: []companionapi.SubscriptionAccountDTO{
			{SubID: "sub-order", Site: "weibo", UID: "w1", Name: "微博号"},
			{SubID: "sub-order", Site: "bilibili", UID: "b2", Name: "Zebra主号", LiveSupported: true},
			{SubID: "sub-order", Site: "bilibili", UID: "b1", Name: "Alpha小号", LiveSupported: true},
		},
	}

	cli, proxy, cleanup := startFakeCompanionServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == companionapi.PathSubscriptions:
			_ = json.NewEncoder(w).Encode(companionapi.SubscriptionsResponse{Items: []companionapi.SubscriptionDTO{sub}})
		case r.URL.Path == companionapi.PathSubscriptionsAccounts:
			_ = json.NewEncoder(w).Encode(companionapi.AllAccountsResponse{Items: sub.Accounts})
		case r.URL.Path == companionapi.PathChecksPlan:
			_ = json.NewEncoder(w).Encode(companionapi.CheckPlanResponse{})
		case r.URL.Path == companionapi.SubscriptionPath("sub-order"):
			detailSubHits.Add(1)
			_ = json.NewEncoder(w).Encode(companionapi.SubscriptionDetailResponse{Subscription: sub})
		case r.URL.Path == companionapi.PathFeed:
			feedHits.Add(1)
			_ = json.NewEncoder(w).Encode(companionapi.FeedPageResponse{})
		default:
			http.NotFound(w, r)
		}
	})
	defer cleanup()

	host := newFakeHost(cli, proxy)
	pg := New()
	pg.RefreshThrottle = 10 * time.Millisecond
	viewFn := func(c *ui.Context) {
		c.SetTheme(theme.TraceTheme())
		pg.View(c, host)
	}
	tt := ui.NewTester(viewFn, 412, 915)
	pg.OnConnected(host)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tt.Frame()
		if pg.subsLoaded {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Open detail and verify same-site accounts preserve original order [Zebra主号, Alpha小号] (DEC-071).
	if err := tt.Click("打开订阅：双B站号测试"); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tt.Frame()
		if detailSubHits.Load() >= 1 && pg.detailSub != nil && len(pg.detailSub.Accounts) == 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	settleAnim(tt)
	if pg.detailSub.Accounts[0].Name != "Zebra主号" || pg.detailSub.Accounts[1].Name != "Alpha小号" || pg.detailSub.Accounts[2].Site != "weibo" {
		t.Fatalf("expected B站 original order preserved before Weibo, got %+v", pg.detailSub.Accounts)
	}

	// Navigate back and settle transition -> detailSubID must be cleared.
	host.Back()
	settleAnim(tt)
	tt.Frame()
	if pg.detailSubID != "" || pg.detailSub != nil {
		t.Fatalf("expected detail state cleared after returning to root, got id=%q sub=%v", pg.detailSubID, pg.detailSub)
	}

	beforeSub := detailSubHits.Load()
	beforeFeed := feedHits.Load()
	// Fire WS events after returning to root: ZERO stale detail/feed fetches must occur.
	pg.OnEvent(host, companionclient.EventFrame{Type: companionapi.EventSubsChanged})
	pg.OnEvent(host, companionclient.EventFrame{Type: companionapi.EventFeedChanged})
	time.Sleep(60 * time.Millisecond)
	if detailSubHits.Load() != beforeSub || feedHits.Load() != beforeFeed {
		t.Fatalf("stale detail polling occurred after leaving detail: subHits %d->%d feedHits %d->%d",
			beforeSub, detailSubHits.Load(), beforeFeed, feedHits.Load())
	}
	pg.OnDisconnected()
}

func settleAnim(tt *ui.Tester) {
	for i := 0; i < 12; i++ {
		tt.Advance(100 * time.Millisecond)
	}
}

func indexContaining(items []string, sub string) int {
	for i, s := range items {
		if strings.Contains(s, sub) {
			return i
		}
	}
	return -1
}
