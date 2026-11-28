package mediaview

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
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
	"testing"
	"time"

	"github.com/chialecode/trace/cmd/trace-mobile/internal/codeccheck"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/pagekit"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/theme"
	"github.com/chialecode/trace/internal/companionapi"
	"github.com/chialecode/trace/internal/companionclient"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/videoplayer"
	"github.com/egoist/mygo/ui"
)

func makeTestPNG(w, h int, col color.RGBA) []byte {
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

type fakeHost struct {
	mu       sync.Mutex
	client   *companionclient.Client
	proxy    *companionclient.MediaProxy
	imgCache *companionclient.MemoryImageCache
	router   *ui.Router
	onUpdate func()
	fetched  []string
	images   map[string][]byte
}

func newFakeHost(t *testing.T, handler http.HandlerFunc) (*fakeHost, func()) {
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
		Name:     "Desk-PC",
		Channel:  "dev",
		FP:       fp,
		Addrs:    []string{addr},
		LastAddr: addr,
	}
	cli, err := companionclient.NewClient(rec, "tok-test-secret", "0.1.0-dev", "dev")
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

	fh := &fakeHost{
		client:   cli,
		proxy:    proxy,
		imgCache: companionclient.NewMemoryImageCache(companionclient.DefaultImageCacheBytes),
		router:   ui.NewRouter("/"),
		images:   make(map[string][]byte),
	}
	cleanup := func() {
		_ = proxy.Close()
		cli.Close()
		srv.Close()
	}
	return fh, cleanup
}

func (f *fakeHost) ConnState() pagekit.ConnState { return pagekit.ConnConnected }
func (f *fakeHost) Desktop() pagekit.DesktopSnapshot {
	return pagekit.DesktopSnapshot{ComputerName: "Desk-PC"}
}
func (f *fakeHost) Client() *companionclient.Client                   { return f.client }
func (f *fakeHost) MediaProxy() *companionclient.MediaProxy           { return f.proxy }
func (f *fakeHost) ImageCache() *companionclient.MemoryImageCache     { return f.imgCache }
func (f *fakeHost) Window() *mygo.Window                              { return nil }
func (f *fakeHost) Router() *ui.Router                                { return f.router }
func (f *fakeHost) PushRoute(path string)                             { f.router.Push(path) }
func (f *fakeHost) Back()                                             { f.router.Back() }
func (f *fakeHost) PushWindow(string, func(*ui.Context)) *mygo.Window { return nil }
func (f *fakeHost) SetTasksBadge(int)                                 {}
func (f *fakeHost) SelectTab(pagekit.TabID)                           {}
func (f *fakeHost) OpenExternal(string) error                         { return nil }
func (f *fakeHost) RefreshStatus()                                    {}
func (f *fakeHost) Unpair()                                           {}

func (f *fakeHost) Update(fn func()) {
	if fn != nil {
		fn()
	}
	f.mu.Lock()
	cb := f.onUpdate
	f.mu.Unlock()
	if cb != nil {
		cb()
	}
}

func (f *fakeHost) FetchImage(_ context.Context, path string) ([]byte, error) {
	f.mu.Lock()
	f.fetched = append(f.fetched, path)
	raw, ok := f.images[path]
	f.mu.Unlock()
	if ok {
		return raw, nil
	}
	return makeTestPNG(120, 80, color.RGBA{R: 50, G: 120, B: 220, A: 255}), nil
}

type mockPlaybackHandle struct {
	mu        sync.Mutex
	source    string
	opts      videoplayer.Options
	state     videoplayer.State
	onState   func(videoplayer.State)
	destroyed bool
	paused    bool
	volume    float64
	lastSeek  time.Duration
}

func (m *mockPlaybackHandle) Play() {
	m.mu.Lock()
	m.paused = false
	m.state.Playing = true
	cb := m.onState
	st := m.state
	m.mu.Unlock()
	if cb != nil {
		cb(st)
	}
}

func (m *mockPlaybackHandle) Pause() {
	m.mu.Lock()
	m.paused = true
	m.state.Playing = false
	m.mu.Unlock()
}

func (m *mockPlaybackHandle) Seek(to time.Duration) {
	m.mu.Lock()
	m.lastSeek = to
	m.state.Position = to
	m.mu.Unlock()
}

func (m *mockPlaybackHandle) SetVolume(v float64) {
	m.mu.Lock()
	m.volume = v
	m.mu.Unlock()
}

func (m *mockPlaybackHandle) State() videoplayer.State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

func (m *mockPlaybackHandle) OnState(fn func(videoplayer.State)) {
	m.mu.Lock()
	m.onState = fn
	m.mu.Unlock()
}

func (m *mockPlaybackHandle) Destroy() {
	m.mu.Lock()
	m.destroyed = true
	m.mu.Unlock()
}

func (m *mockPlaybackHandle) NativeView() ui.NativeWebView { return nil }

func (m *mockPlaybackHandle) emit(st videoplayer.State) {
	m.mu.Lock()
	m.state = st
	cb := m.onState
	m.mu.Unlock()
	if cb != nil {
		cb(st)
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

func TestFormatHelpersAndSafeLinks(t *testing.T) {
	if got := SiteLabel("bilibili"); got != "B 站" {
		t.Fatalf("SiteLabel(bilibili) = %q", got)
	}
	if got := FormatDuration(0); got != "" {
		t.Fatalf("unknown duration 0 must return empty string, got %q", got)
	}
	if got := FormatDuration(125); got != "02:05" {
		t.Fatalf("FormatDuration(125) = %q", got)
	}
	if got := FormatDuration(3665); got != "1:01:05" {
		t.Fatalf("FormatDuration(3665) = %q", got)
	}
	links := ExtractLinks("看这里 https://example.com/post?a=1 和 https://en.wikipedia.org/wiki/Go_(programming_language) 及 https://zh.wikipedia.org/wiki/人工智能，跳过 https://video.twimg.com/a.mp4 和 javascript:alert(1)", []companionapi.TextLink{
		{Label: "恶意", URL: "sinaweibo://userinfo?uid=1"},
		{Label: "签名流", URL: "https://example.com/dl?auth_token=SECRET&ct0=CSRF"},
		{Label: "官网", URL: "https://trace.example.org/docs"},
	})
	if len(links) != 4 {
		t.Fatalf("expected 4 safe links, got %+v", links)
	}
	if links[0].URL != "https://trace.example.org/docs" ||
		links[1].URL != "https://example.com/post?a=1" ||
		links[2].URL != "https://en.wikipedia.org/wiki/Go_(programming_language)" ||
		links[3].URL != "https://zh.wikipedia.org/wiki/%E4%BA%BA%E5%B7%A5%E6%99%BA%E8%83%BD" {
		t.Fatalf("unexpected extracted links: %+v", links)
	}
	for _, bad := range []string{
		"http://:8080/path",
		"https://user:pass@example.com/secret",
		`https://example.com/a\b`,
		"https://video.twimg.com/ext_tw_video/1/a.mp4",
		"https://ton.x.com/i/ton/1",
		"https://f.video.weibocdn.com/a.mp4?Expires=1&ssig=abc",
		"https://example.com/file?X-Amz-Signature=deadbeef",
	} {
		if IsSafeExternalURL(bad) {
			t.Fatalf("IsSafeExternalURL(%q) = true, want false", bad)
		}
	}
}

func TestImageViewerWeiboNoWatermarkOriginalAndLivePhoto(t *testing.T) {
	fh, cleanup := newFakeHost(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
	})
	defer cleanup()

	var createdMu sync.Mutex
	var created []*mockPlaybackHandle
	SetPlayerFactoryForTest(func(_ *mygo.Window, source string, opts videoplayer.Options) (PlaybackHandle, error) {
		h := &mockPlaybackHandle{source: source, opts: opts}
		createdMu.Lock()
		created = append(created, h)
		createdMu.Unlock()
		return h, nil
	})
	defer SetPlayerFactoryForTest(nil)

	items := []ImageItem{
		{
			ItemKey:            "weibo_1",
			RelPath:            "images/01.jpg",
			NoWatermarkRelPath: "images/01_nowm.jpg",
		},
		{
			ItemKey:       "weibo_1",
			RelPath:       "images/02.jpg",
			MotionRelPath: "images/02_live.mp4",
		},
	}

	closed := false
	v := NewImageViewer(fh, items, 0, func() { closed = true })
	defer v.Close()

	view := func(c *ui.Context) {
		c.SetTheme(theme.TraceTheme())
		v.View(c)
	}

	tt := ui.NewTester(view, 412, 915)
	notify := make(chan struct{}, 16)
	fh.onUpdate = func() {
		select {
		case notify <- struct{}{}:
		default:
		}
	}
	// Let initial image load.
	for range 10 {
		tt.Frame()
		select {
		case <-notify:
		case <-time.After(10 * time.Millisecond):
		}
	}

	// Default uses ?w=2560 and shows Weibo 原图 / 无水印 toggle + 查看原图.
	if got := v.ActivePath(0); !strings.HasSuffix(got, "/images/01.jpg?w=2560") {
		t.Fatalf("expected default ?w=2560 path, got %q", got)
	}
	if !tt.HasText("1 / 2") || !tt.HasText("无水印") || !tt.HasText("查看原图") {
		t.Fatalf("missing expected viewer controls; texts=%v", tt.Texts())
	}
	assertNoHorizontalOverflow(t, tt, 412)
	saveOptionalShot(t, tt, "mediaview-image-weibo-412x915")

	// Toggle Weibo 无水印.
	if err := tt.Click("切换无水印"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := v.ActivePath(0); !strings.HasSuffix(got, "/images/01_nowm.jpg?w=2560") {
		t.Fatalf("expected no-watermark path, got %q", got)
	}

	// Click 查看原图 -> removes ?w=2560.
	if err := tt.Click("查看原图"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := v.ActivePath(0); strings.Contains(got, "?w=") || !strings.HasSuffix(got, "/images/01_nowm.jpg") {
		t.Fatalf("expected original full file path without ?w=, got %q", got)
	}

	// Navigate to image 2 (live photo without image_nowm): toggle hides, 实况 badge shows, clip starts!
	if err := tt.Click("下一张"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("2 / 2") || !tt.HasText("实况") {
		t.Fatalf("expected 2 / 2 and 实况 badge; texts=%v", tt.Texts())
	}
	if _, hasNoWM := tt.Find("切换无水印"); hasNoWM {
		t.Fatal("Weibo 无水印 toggle must hide on image without image_nowm")
	}
	createdMu.Lock()
	motionCount := len(created)
	var motionHandle *mockPlaybackHandle
	if motionCount > 0 {
		motionHandle = created[0]
	}
	createdMu.Unlock()
	if motionCount != 1 || !strings.Contains(motionHandle.source, "http://127.0.0.1:") || !strings.Contains(motionHandle.source, "02_live.mp4") {
		t.Fatalf("expected live photo clip auto-play over loopback proxy, got count=%d handle=%+v", motionCount, motionHandle)
	}

	// Responsive sizes 360x800 and 800x1280.
	for _, sz := range [][2]int{{360, 800}, {800, 1280}} {
		tt.SetSize(sz[0], sz[1])
		tt.Frame()
		assertNoHorizontalOverflow(t, tt, sz[0])
	}

	// Closing revokes ticket & destroys motion player.
	v.Close()
	if !closed {
		t.Fatal("expected onClose callback")
	}
	motionHandle.mu.Lock()
	destroyed := motionHandle.destroyed
	motionHandle.mu.Unlock()
	if !destroyed {
		t.Fatal("expected motion clip player destroyed on close")
	}
}

func TestVideoPlayerTwoLayerCodecCheckMultiPAndSequentialLive(t *testing.T) {
	fh, cleanup := newFakeHost(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
	})
	defer cleanup()

	// 1. Layer-1 MediaCodecList pre-check blocks AV1 before creating player.
	codeccheck.SetProbeForTest(func(mime string, w, h int) (bool, bool) {
		if mime == "video/av01" {
			return false, true
		}
		return true, true
	})
	defer codeccheck.SetProbeForTest(nil)

	playerStarts := 0
	SetPlayerFactoryForTest(func(_ *mygo.Window, source string, opts videoplayer.Options) (PlaybackHandle, error) {
		playerStarts++
		return &mockPlaybackHandle{source: source, opts: opts}, nil
	})
	defer SetPlayerFactoryForTest(nil)

	vpAV1 := NewVideoPlayer(fh, VideoRequest{
		ItemKey: "bili_av1",
		Title:   "AV1 4K 测试视频",
		Tracks: []VideoTrack{{
			Label:          "P1",
			RelPath:        "video.mp4",
			CodecHint:      "av01.0.08M.08",
			QualitySummary: "4K · AV1",
			Width:          3840,
			Height:         2160,
		}},
	}, nil)
	defer vpAV1.Close()

	ttAV1 := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(theme.TraceTheme())
		vpAV1.View(c)
	}, 412, 915)
	if playerStarts != 0 {
		t.Fatalf("Layer-1 precheck should prevent starting player for unsupported AV1, got %d starts", playerStarts)
	}
	if !ttAV1.HasText("这台手机不能播放 AV1 视频") {
		t.Fatalf("expected explicit AV1 unsupported message, got texts=%v", ttAV1.Texts())
	}
	assertNoHorizontalOverflow(t, ttAV1, 412)
	saveOptionalShot(t, ttAV1, "mediaview-video-unsupported-av1-412x915")

	// 2. Layer-2 runtime fallback: Android MediaPlayer silent video drop (Ready=true, Width=0, Height=0).
	codeccheck.SetProbeForTest(nil) // simulate unknown pre-check
	var lastHandle *mockPlaybackHandle
	SetPlayerFactoryForTest(func(_ *mygo.Window, source string, opts videoplayer.Options) (PlaybackHandle, error) {
		lastHandle = &mockPlaybackHandle{source: source, opts: opts}
		return lastHandle, nil
	})

	vpDrop := NewVideoPlayer(fh, VideoRequest{
		ItemKey: "bili_hevc",
		Title:   "HEVC 静默黑屏兜底",
		Tracks: []VideoTrack{{
			Label:          "P1",
			RelPath:        "video.mp4",
			CodecHint:      "HEVC",
			QualitySummary: "1080P · HEVC",
		}},
	}, nil)
	defer vpDrop.Close()
	ttDrop := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(theme.TraceTheme())
		vpDrop.View(c)
	}, 412, 915)
	if lastHandle == nil {
		t.Fatal("expected player created when pre-check is unknown")
	}
	lastHandle.emit(videoplayer.State{Ready: true, Playing: true, Width: 0, Height: 0})
	ttDrop.Frame()
	if !ttDrop.HasText("这台手机不能播放 HEVC 视频") {
		t.Fatalf("expected runtime Width=0&&Height=0 fallback message, got texts=%v", ttDrop.Texts())
	}

	// 3. Multi-P picker + landscape orientation toggle + sequential multi-part live recording.
	var orientations []mygo.Orientation
	SetOrientationSetterForTest(func(o mygo.Orientation) {
		orientations = append(orientations, o)
	})
	defer SetOrientationSetterForTest(nil)

	var handles []*mockPlaybackHandle
	SetPlayerFactoryForTest(func(_ *mygo.Window, source string, opts videoplayer.Options) (PlaybackHandle, error) {
		h := &mockPlaybackHandle{source: source, opts: opts}
		handles = append(handles, h)
		return h, nil
	})

	vpLive := NewVideoPlayer(fh, VideoRequest{
		ItemKey:         "live_rec_1",
		Title:           "晚间直播录像",
		IsLiveRecording: true,
		Tracks: []VideoTrack{
			{Label: "第 1 段", RelPath: "live_001.mp4", DurationSec: 600, CodecHint: "AVC", QualitySummary: "原画 · AVC"},
			{Label: "第 2 段", RelPath: "live_002.mp4", DurationSec: 420, CodecHint: "AVC", QualitySummary: "原画 · AVC"},
		},
	}, nil)
	defer vpLive.Close()

	ttLive := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(theme.TraceTheme())
		vpLive.View(c)
	}, 412, 915)

	if len(handles) != 1 || !strings.Contains(handles[0].source, "live_001.mp4") {
		t.Fatalf("expected segment 1 playing over loopback proxy, got %+v", handles)
	}
	handles[0].emit(videoplayer.State{Ready: true, Playing: true, Width: 1920, Height: 1080, Duration: 600 * time.Second, Position: 120 * time.Second})
	ttLive.Frame()
	assertNoHorizontalOverflow(t, ttLive, 412)
	saveOptionalShot(t, ttLive, "mediaview-video-player-412x915")

	// Toggle landscape fullscreen.
	if err := ttLive.Click("横屏全屏"); err != nil {
		t.Fatal(err)
	}
	ttLive.Frame()
	if len(orientations) == 0 || orientations[len(orientations)-1] != mygo.OrientationLandscape {
		t.Fatalf("expected OrientationLandscape set, got %v", orientations)
	}

	// Segment 1 ends -> automatically advances to segment 2!
	handles[0].emit(videoplayer.State{Ready: true, Ended: true, Width: 1920, Height: 1080})
	ttLive.Frame()
	if vpLive.CurrentIndex() != 1 || len(handles) != 2 || !strings.Contains(handles[1].source, "live_002.mp4") {
		t.Fatalf("expected auto-advance to segment 2, idx=%d handles=%d", vpLive.CurrentIndex(), len(handles))
	}

	// Open segment picker sheet and switch back to segment 1.
	if err := ttLive.Click("分段 (2)"); err != nil {
		t.Fatal(err)
	}
	ttLive.Advance(400 * time.Millisecond)
	ttLive.Frame()
	if err := ttLive.Click("分段 1 · 第 1 段"); err != nil {
		t.Fatal(err)
	}
	ttLive.Advance(400 * time.Millisecond)
	ttLive.Frame()
	if vpLive.CurrentIndex() != 0 {
		t.Fatalf("expected switched back to segment 0 via sheet, got %d", vpLive.CurrentIndex())
	}

	// CloseAllActive resets orientation and stops player.
	CloseAllActive()
	if orientations[len(orientations)-1] != mygo.OrientationAny {
		t.Fatalf("expected orientation restored to OrientationAny on close, got %v", orientations)
	}
}
