package ui

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/player"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/egoist/mygo/ui"
)

func newTestUIApp(t *testing.T) (*App, *service.MockDesktop, func()) {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/x/web-interface/nav", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"code":0,"data":{"isLogin":true,"mid":12345,"uname":"UI测试用户","wbi_img":{"img_url":"https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png","sub_url":"https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png"}}}`)
	})
	mux.HandleFunc("/x/passport-login/web/qrcode/generate", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"code":0,"data":{"url":"https://passport.bilibili.com/h5-app/passport/login/scan?qrcode_key=ui_qr_1","qrcode_key":"ui_qr_1"}}`)
	})
	mux.HandleFunc("/x/passport-login/web/qrcode/poll", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "SESSDATA", Value: "qr_sess_val"})
		http.SetCookie(w, &http.Cookie{Name: "bili_jct", Value: "qr_csrf_val"})
		http.SetCookie(w, &http.Cookie{Name: "DedeUserID", Value: "12345"})
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"code":0,"data":{"code":0,"message":"扫码成功","refresh_token":"qr_ref_val","timestamp":1710000000}}`)
	})
	mux.HandleFunc("/x/web-interface/wbi/view", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"code":0,"data":{"bvid":"BV1xx411c7mD","aid":999,"videos":2,"title":"原生界面测试视频","desc":"测试视频简介","pic":"http://i0.hdslb.com/bfs/archive/cover.jpg","pubdate":1710000000,"owner":{"mid":8888,"name":"测试UP主","face":"https://i0.hdslb.com/bfs/face/avatar.jpg"},"pages":[{"cid":101,"page":1,"part":"第一集","duration":60},{"cid":102,"page":2,"part":"第二集","duration":90}]}}`)
	})
	mux.HandleFunc("/x/relation/followings", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"code":0,"data":{"total":2,"list":[{"mid":2001,"uname":"关注UP_A","sign":"签名A"},{"mid":2002,"uname":"关注UP_B","sign":"签名B"}]}}`)
	})
	mux.HandleFunc("/x/relation/tags", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"code":0,"data":[]}`)
	})
	mux.HandleFunc("/room/v1/Room/get_status_info_by_uids", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"code":0,"data":{"1001":{"uid":1001,"room_id":8888,"live_status":1,"uname":"AccA","title":"Live Stream","live_time":1700000000}}}`)
	})

	srv := httptest.NewServer(mux)
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
	desk := service.NewMockDesktop()
	b, err := service.NewBackend(service.Options{
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
		Desktop:          desk,
		DisableBgWorkers: true,
	})
	if err != nil {
		srv.Close()
		t.Fatalf("NewBackend: %v", err)
	}

	sink := player.NewNullAudioSink()
	app := NewApp(b, sink)
	app.BindEventLoop(nil)

	cleanup := func() {
		app.Close()
		_ = b.Close()
		srv.Close()
	}
	return app, desk, cleanup
}

// TestThreeColumnResponsiveFixedSidebarsComposeAndVirtualList verifies:
// - 3 widths (1280x860, 1000x860, 720x860) have zero horizontal overflow
// - Left and Right sidebars keep fixed coordinates when scrolling the Center column
// - IME composition (`tt.Compose`) pressing Enter does NOT submit parse
// - 5000-item virtualized feed list renders cleanly in memory via `ui.NewTester`
func TestThreeColumnResponsiveFixedSidebarsComposeAndVirtualList(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()

	_, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", "")
	if err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()
	app.PopulateSyntheticFeed(5000)

	widths := []int{1280, 1000, 720}
	for _, w := range widths {
		tt := ui.NewTester(app.View, w, 860)

		leftRect, okL := tt.Find("左栏导航")
		centerRect, okC := tt.Find("中栏内容")
		if !okL || !okC || leftRect.W <= 0 || centerRect.W <= 0 {
			t.Fatalf("width=%d: expected valid left (%+v, %v) and center (%+v, %v) rects", w, leftRect, okL, centerRect, okC)
		}
		if leftRect.X < 0 || centerRect.X+centerRect.W > float32(w)+0.5 {
			t.Fatalf("width=%d: horizontal overflow detected: left=%+v center=%+v", w, leftRect, centerRect)
		}

		// DEC-039: a 64px icon rail; on Home a context panel beside it from
		// 1000px (272 / 240), none at 720 where the paste box stays in the
		// page's top bar.
		if leftRect.W != railWidth {
			t.Fatalf("width=%d: expected icon rail width %d, got %v", w, railWidth, leftRect.W)
		}
		panelRect, okP := tt.Find("上下文侧栏")
		wantPanel := map[int]float32{1280: 272, 1000: 240, 720: 0}[w]
		if wantPanel == 0 {
			if okP {
				t.Fatalf("width=%d: no context panel expected, got %+v", w, panelRect)
			}
		} else if !okP || panelRect.W != wantPanel || panelRect.X != leftRect.X+leftRect.W || panelRect.X+panelRect.W > centerRect.X+0.5 {
			t.Fatalf("width=%d: context panel %+v (%v), want width %v between rail %+v and page %+v", w, panelRect, okP, wantPanel, leftRect, centerRect)
		}
		if inlineParse, okQ := tt.Find("快速解析输入框"); !okQ || inlineParse.W <= 0 || inlineParse.X+inlineParse.W > float32(w)+0.5 {
			t.Fatalf("width=%d: expected the paste box in the top bar, got %+v (%v)", w, inlineParse, okQ)
		}
		// Scroll the page: the rail and the panel do not move.
		tt.Scroll(centerRect.X+centerRect.W/2, centerRect.Y+300, 0, 600)
		leftAfter, _ := tt.Find("左栏导航")
		panelAfter, _ := tt.Find("上下文侧栏")
		if leftAfter != leftRect || panelAfter != panelRect {
			t.Fatalf("width=%d: sidebar moved during page scroll: rail %+v->%+v, panel %+v->%+v", w, leftRect, leftAfter, panelRect, panelAfter)
		}
	}

	// Verify IME composition (`tt.Compose`) Enter key guard on /parse
	app.Navigate(RouteParse)
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Click("链接解析输入框")
	tt.Compose("BV1xx411c7mD", 11)
	tt.Key(0, ui.KeyEnter)
	if app.CurrentRoute != RouteParse {
		t.Fatalf("expected Enter during IME composition NOT to submit or leave /parse, got route %s", app.CurrentRoute)
	}
}

// TestAuthGuardLoginCookieClearOverlaysAndDesktopActions verifies login cookie clear, overlays, and desktop actions.
func TestAuthGuardLoginCookieClearOverlaysAndDesktopActions(t *testing.T) {
	app, desk, cleanup := newTestUIApp(t)
	defer cleanup()

	tt := ui.NewTester(app.View, 1280, 860)

	// 1. Login via Manual Cookie and verify input is cleared immediately on submit
	app.Navigate(RouteLogin)
	tt.Frame()
	tt.Click("登录标签：粘贴凭证")
	app.ManualCookieInput = "SESSDATA=secret_sess; bili_jct=secret_csrf; DedeUserID=12345"
	app.ManualRefreshInput = "secret_refresh"
	tt.Frame()
	tt.Click("验证并保存凭证")
	if app.ManualCookieInput != "" || app.ManualRefreshInput != "" {
		t.Fatalf("expected manual cookie & refresh inputs to be cleared immediately on submit")
	}

	// 2. Parse video link -> navigates to /video
	app.SubmitParse("BV1xx411c7mD")
	tt.Frame()
	if app.CurrentRoute != RouteVideo || app.CurrentVideo == nil || len(app.CurrentVideo.Pages) != 2 {
		t.Fatalf("expected /video with 2 pages loaded, got route=%s video=%+v", app.CurrentRoute, app.CurrentVideo)
	}

	// 3. Simulate mid-session -101 expiry -> transitions to /login while preserving loaded video & SavedRoute
	app.TriggerAuthExpired("会话过期 (-101)")
	tt.Frame()
	if app.CurrentRoute != RouteLogin || app.SavedRoute != RouteVideo || app.CurrentVideo == nil {
		t.Fatalf("expected auth guard to redirect to /login with SavedRoute=/video and preserve CurrentVideo")
	}

	// Re-login via QR code flow -> returns to /video with loaded data intact
	tt.Click("登录标签：扫码")
	tt.Click("获取登录二维码")
	if app.QRBitmap == nil {
		t.Fatalf("expected QRBitmap to be generated")
	}
	tt.Click("检查扫码状态")
	if app.CurrentRoute != RouteVideo || app.CurrentVideo == nil || app.CurrentVideo.BVID != "BV1xx411c7mD" {
		t.Fatalf("expected re-login to restore /video and CurrentVideo, got route=%s", app.CurrentRoute)
	}

	// 4. Immersive Image Viewer Overlay: https enforcement, floating circular buttons, 2s auto-hide & wakeup, Esc close
	app.OpenImageViewer([]string{"http://image.example.invalid/pic1.jpg", "https://image.example.invalid/pic2.jpg"}, nil, 0)
	tt.Frame()
	if !app.ImageViewer.Open || app.ImageViewer.URLs[0] != "https://image.example.invalid/pic1.jpg" {
		t.Fatalf("expected http image URL to be upgraded to https, got %+v", app.ImageViewer.URLs)
	}
	tt.Key(0, ui.KeyEqual)
	if app.ImageViewer.Zoom <= 1.0 {
		t.Fatalf("expected zoom > 1.0 after pressing +, got %v", app.ImageViewer.Zoom)
	}
	tt.Click("看图1:1")
	if app.ImageViewer.Zoom != 1.0 || app.ImageViewer.Fit {
		t.Fatalf("expected zoom 1.0 and Fit=false after clicking 1:1, got zoom=%v fit=%v", app.ImageViewer.Zoom, app.ImageViewer.Fit)
	}
	// Verify wheel zoom, pointer drag pan, and double-click toggle
	app.imageInput(ui.InputEvent{Kind: ui.InputScroll, DY: -1})
	if app.ImageViewer.Zoom <= 1.0 {
		t.Fatalf("expected wheel scroll up to zoom in (>1.0), got %v", app.ImageViewer.Zoom)
	}
	app.imageInput(ui.InputEvent{Kind: ui.InputPointerDown, Button: 0, X: 100, Y: 100, Clicks: 1})
	app.imageInput(ui.InputEvent{Kind: ui.InputPointerMove, X: 140, Y: 130})
	app.imageInput(ui.InputEvent{Kind: ui.InputPointerUp, Button: 0})
	if app.ImageViewer.PanX != 40 || app.ImageViewer.PanY != 30 {
		t.Fatalf("expected pointer drag to pan image by (40,30), got (%v,%v)", app.ImageViewer.PanX, app.ImageViewer.PanY)
	}
	app.imageInput(ui.InputEvent{Kind: ui.InputPointerDown, Button: 0, X: 140, Y: 130, Clicks: 2})
	app.imageInput(ui.InputEvent{Kind: ui.InputPointerUp, Button: 0})
	if !app.ImageViewer.Fit || app.ImageViewer.Zoom != 1.0 || app.ImageViewer.PanX != 0 || app.ImageViewer.PanY != 0 {
		t.Fatalf("expected double-click to reset Fit=true, Zoom=1.0, Pan=(0,0), got fit=%v zoom=%v pan=(%v,%v)",
			app.ImageViewer.Fit, app.ImageViewer.Zoom, app.ImageViewer.PanX, app.ImageViewer.PanY)
	}
	tt.Click("看图1:1")
	tt.Click("看图适应")
	if !app.ImageViewer.Fit {
		t.Fatalf("expected Fit=true after clicking 看图适应")
	}
	tt.Click("看图下一张")
	if app.ImageViewer.Index != 1 {
		t.Fatalf("expected image index 1, got %d", app.ImageViewer.Index)
	}
	// Verify 2-second inactivity auto-hide and keyboard wakeup
	tt.Advance(2100 * time.Millisecond)
	tt.Frame()
	if !app.ImageViewer.ControlsHidden {
		t.Fatalf("expected ImageViewer controls to auto-hide after 2s of inactivity")
	}
	if _, shown := tt.Find("关闭看图"); shown {
		t.Fatalf("expected floating close button to be hidden after 2s inactivity")
	}
	tt.Key(0, ui.KeyLeft)
	tt.Frame()
	if app.ImageViewer.ControlsHidden || app.ImageViewer.Index != 0 {
		t.Fatalf("expected Left key to wake controls and move to index 0, got hidden=%v index=%d", app.ImageViewer.ControlsHidden, app.ImageViewer.Index)
	}
	if _, shown := tt.Find("关闭看图"); !shown {
		t.Fatalf("expected floating close button to reappear after wakeup")
	}
	tt.Key(0, ui.KeyEscape)
	if app.ImageViewer.Open {
		t.Fatalf("expected Escape to close ImageViewer overlay")
	}

	// 5. Real cached item on disk: Immersive Player Overlay (MFDecoder + "⋯" menu + 2s auto-hide) + Image Viewer via Backend.MediaPath
	itemDir := filepath.Join(t.TempDir(), "bilibili", "8888_测试UP主", "2026-03-01_video_BV1xx411c7mD_原生界面测试视频")
	if err := os.MkdirAll(itemDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	vBytes, err := download.BuildSyntheticVideoM4S(640, 360)
	if err != nil {
		t.Fatalf("BuildSyntheticVideoM4S: %v", err)
	}
	aBytes, err := download.BuildSyntheticAudioM4S(15)
	if err != nil {
		t.Fatalf("BuildSyntheticAudioM4S: %v", err)
	}
	vPath := filepath.Join(itemDir, "v.m4s")
	aPath := filepath.Join(itemDir, "a.m4s")
	mp4Path := filepath.Join(itemDir, "P01_第一集.mp4")
	_ = os.WriteFile(vPath, vBytes, 0o600)
	_ = os.WriteFile(aPath, aBytes, 0o600)
	if _, err := download.MuxDASHFiles(vPath, aPath, mp4Path); err != nil {
		t.Fatalf("MuxDASHFiles: %v", err)
	}
	_ = os.Remove(vPath)
	_ = os.Remove(aPath)

	dmPath := filepath.Join(itemDir, "P01.danmaku.xml")
	_ = os.WriteFile(dmPath, []byte(`<i><d p="0.2,1,25,16777215,1710000000,0,u1,1">真实磁盘弹幕</d></i>`), 0o600)
	vttPath := filepath.Join(itemDir, "P01.zh-CN.vtt")
	_ = os.WriteFile(vttPath, []byte("WEBVTT\n\n00:00:00.000 --> 00:00:05.000\n真实磁盘中文字幕\n"), 0o600)

	coverImg := image.NewRGBA(image.Rect(0, 0, 64, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 64; x++ {
			coverImg.SetRGBA(x, y, color.RGBA{R: 220, G: 80, B: 120, A: 255})
		}
	}
	var pngBuf bytes.Buffer
	_ = png.Encode(&pngBuf, coverImg)
	_ = os.WriteFile(filepath.Join(itemDir, "cover.png"), pngBuf.Bytes(), 0o600)

	savedItem, err := app.backend.(*service.Backend).LibraryStore().SaveItem(itemDir, library.ItemMetadata{
		Version: 1,
		ID:      "BV1xx411c7mD",
		RawID:   "BV1xx411c7mD",
		Site:    "bilibili",
		Kind:    "video",
		Status:  "complete",
		Title:   "原生界面测试视频",
		Author:  library.ItemAuthor{Mid: 8888, Name: "测试UP主"},
		Media: []library.MediaFile{
			{Role: "video", Path: "P01_第一集.mp4"},
			{Role: "danmaku", Path: "P01.danmaku.xml"},
			{Role: "subtitle", Path: "P01.zh-CN.vtt"},
			{Role: "cover", Path: "cover.png"},
		},
		Parts: []library.VideoPartRecord{
			{
				CID:       101,
				Page:      1,
				Title:     "第一集",
				File:      "P01_第一集.mp4",
				Danmaku:   "P01.danmaku.xml",
				Subtitles: []string{"P01.zh-CN.vtt"},
			},
		},
	})
	if err != nil {
		t.Fatalf("SaveItem: %v", err)
	}

	app.OpenItemImageViewer(savedItem.ItemKey, nil, []string{"cover.png"}, 0)
	tt.Frame()
	if !app.ImageViewer.Open || len(app.ImageViewer.Bitmaps) == 0 || app.ImageViewer.Bitmaps[0] == nil {
		t.Fatalf("expected OpenItemImageViewer to decode local cover.png via MediaPath")
	}
	tt.Click("看图打开文件夹")
	imgActions := desk.Actions()
	if len(imgActions) == 0 || imgActions[len(imgActions)-1].Kind != "open_path" {
		t.Fatalf("expected 看图打开文件夹 to invoke Desktop.OpenPath on cached item folder, got %+v", imgActions)
	}
	app.ImageViewer.Open = false

	app.OpenPlayerOverlay(savedItem)
	tt.Frame()
	if !app.PlayerOverlay.Open {
		t.Fatalf("expected PlayerOverlay to be open")
	}
	snap := app.player.Snapshot(640, 240)
	if runtime.GOOS == "windows" && (snap.Backend != "windows-mf" || snap.Width != 640 || snap.Height != 360) {
		t.Fatalf("expected real Media Foundation decoder (640x360), got backend=%q %dx%d", snap.Backend, snap.Width, snap.Height)
	}
	tt.Click("播放器全屏")
	if !app.PlayerOverlay.Fullscreen {
		t.Fatalf("expected clicking 播放器全屏 to toggle Fullscreen=true")
	}
	// Open "⋯" menu in immersive player and verify danmaku/subtitle/folder/external player controls
	tt.Click("播放器更多菜单")
	if !app.PlayerOverlay.MoreMenuOpen {
		t.Fatalf("expected PlayerOverlay MoreMenuOpen=true after clicking 播放器更多菜单")
	}
	if _, ok := tt.Find("弹幕开关"); !ok {
		t.Fatalf("expected 弹幕开关 inside 播放器更多面板")
	}
	tt.Click("播放层打开文件夹")
	folderActions := desk.Actions()
	if len(folderActions) < 2 || folderActions[len(folderActions)-1].Kind != "open_path" {
		t.Fatalf("expected 播放层打开文件夹 to invoke Desktop.OpenPath, got %+v", folderActions)
	}
	app.PlayerOverlay.DanmakuEnabled = false
	app.PlayerOverlay.SubtitleLang = "zh-CN"
	app.applyAndPersistPlayerPrefs()
	persistedUI := app.backend.GetUIState()
	if persistedUI.Player.DanmakuVisible != false || persistedUI.Player.SubtitleLang != "zh-CN" {
		t.Fatalf("expected player preferences to persist to ui-state.json, got %+v", persistedUI.Player)
	}
	app.PlayerOverlay.MoreMenuOpen = false
	tt.Frame()
	// Verify 2s auto-hide and Space wakeup on PlayerOverlay
	tt.Advance(2100 * time.Millisecond)
	tt.Frame()
	if !app.PlayerOverlay.ControlsHidden {
		t.Fatalf("expected PlayerOverlay controls to auto-hide after 2s of inactivity")
	}
	tt.Key(0, ui.KeySpace)
	tt.Frame()
	if app.PlayerOverlay.ControlsHidden {
		t.Fatalf("expected Space key to wake PlayerOverlay controls")
	}
	// Missing decoder fallback action via service.Desktop
	app.PlayerOverlay.DecoderMissing = true
	tt.Frame()
	tt.Click("缺失解码器时用系统播放器打开")
	actions := desk.Actions()
	if len(actions) == 0 || actions[len(actions)-1].Kind != "open_path" {
		t.Fatalf("expected Desktop.OpenPath to be recorded when clicking 用系统播放器打开, got %+v", actions)
	}
	tt.Click("关闭播放")
	if app.PlayerOverlay.Open {
		t.Fatalf("expected PlayerOverlay to close")
	}

	// 6. Subscriptions Import from Followings via top "更多" menu
	app.Navigate(RouteSubscriptions)
	tt.Frame()
	tt.Click("订阅更多菜单")
	tt.Click("从 B 站关注列表导入")
	if !app.ImportSubModalOpen || len(app.ImportFollowings) != 2 {
		t.Fatalf("expected import modal with 2 followings, got open=%v len=%d", app.ImportSubModalOpen, len(app.ImportFollowings))
	}
	tt.Click("全选已加载")
	tt.Click("确认导入关注")
	if len(app.Subscriptions) != 2 {
		t.Fatalf("expected 2 imported subscriptions, got %d", len(app.Subscriptions))
	}
}
