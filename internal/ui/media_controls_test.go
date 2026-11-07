package ui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/player"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/uistate"
	"github.com/egoist/mygo/ui"
)

func TestAuthExpiryClosesMediaOverlaysAndPreservesPage(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()
	app.Navigate(RouteVideo)
	app.CurrentVideo = &bilibili.VideoDetail{BVID: "BV1fixture", Title: "retained page"}
	app.PlayerOverlay.Open, app.ImageViewer.Open = true, true
	app.TriggerAuthExpired("登录已失效")
	if app.PlayerOverlay.Open || app.ImageViewer.Open {
		t.Fatal("an immersive media layer still covers the forced login page")
	}
	if app.CurrentRoute != RouteLogin || app.SavedRoute != RouteVideo || app.CurrentVideo == nil {
		t.Fatal("closing media on expiry lost the loaded page or return route")
	}
}

func TestFullscreenControlsCallWindowAndRestoreOnClose(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	var calls []bool
	app.BindFullscreenHandler(func(fullscreen bool) { calls = append(calls, fullscreen) })
	app.PlayerOverlay.Open = true
	tester := ui.NewTester(app.View, 720, 560)
	tester.Click("播放器全屏")
	if len(calls) != 1 || !calls[0] {
		t.Fatalf("fullscreen button did not call the native window: %v", calls)
	}
	item := library.ItemMetadata{ItemKey: "video:fixture", Parts: []library.VideoPartRecord{{File: "p01.mp4"}, {File: "p02.mp4"}}}
	app.openPlayerSegment(item, 1)
	if !app.PlayerOverlay.Fullscreen || len(calls) != 1 {
		t.Fatal("changing segments unexpectedly left native fullscreen")
	}
	tester.Frame()
	tester.Key(0, ui.KeyEscape)
	if app.PlayerOverlay.Open || len(calls) != 2 || calls[1] {
		t.Fatalf("Esc did not close the player and restore the window: %v", calls)
	}
	app.PlayerOverlay.Open = true
	app.setPlayerFullscreen(true)
	tester.Frame()
	tester.Click("关闭播放")
	if app.PlayerOverlay.Open || len(calls) != 4 || calls[3] {
		t.Fatalf("close button did not restore the window: %v", calls)
	}
	app.PlayerOverlay.Open = true
	app.setPlayerFullscreen(true)
	app.TriggerAuthExpired("登录已失效")
	if app.PlayerOverlay.Fullscreen || calls[len(calls)-1] {
		t.Fatal("expired login left the native window fullscreen")
	}
}

type delayedViewerBackend struct {
	service.BackendAPI
	started chan context.Context
	release chan struct{}
}

func (b *delayedViewerBackend) FetchImage(ctx context.Context, _ string) ([]byte, error) {
	b.started <- ctx
	<-b.release // Simulate a transport returning after cancellation.
	return nil, errors.New("late image failure")
}

func TestClosedImageViewerRejectsLateFetchAndCancelsWork(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	backend := &delayedViewerBackend{BackendAPI: app.backend, started: make(chan context.Context, 1), release: make(chan struct{})}
	app.backend = backend
	posted := make(chan func(), 1)
	app.dispatch = func(fn func()) { posted <- fn }
	app.OpenImageViewer([]string{"https://fixture.invalid/image.png"}, nil, 0)
	var fetchCtx context.Context
	select {
	case fetchCtx = <-backend.started:
	case <-time.After(time.Second):
		t.Fatal("image loading did not start")
	}
	if !app.ImageViewer.Open {
		t.Fatal("viewer cannot be closed while its image is loading")
	}
	app.closeImageViewer()
	if fetchCtx.Err() != context.Canceled {
		t.Fatal("closing the image viewer did not cancel its fetch")
	}
	close(backend.release)
	select {
	case apply := <-posted:
		apply()
	case <-time.After(time.Second):
		t.Fatal("image fetch did not finish")
	}
	if app.ImageViewer.Open || len(app.ImageViewer.Bitmaps) > 0 || app.BannerError != "" {
		t.Fatal("a late image result reopened the viewer or changed the page")
	}
}

func TestNewImageViewerPreservesInteractionWhenFetchesFinish(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	backend := &delayedViewerBackend{BackendAPI: app.backend, started: make(chan context.Context, 2), release: make(chan struct{})}
	app.backend = backend
	posted := make(chan func(), 2)
	app.dispatch = func(fn func()) { posted <- fn }
	for _, imageURL := range []string{"https://fixture.invalid/old.png", "https://fixture.invalid/new.png"} {
		app.OpenImageViewer([]string{imageURL}, nil, 0)
		select {
		case <-backend.started:
		case <-time.After(time.Second):
			t.Fatal("new image viewer was blocked by the previous request")
		}
	}
	app.ImageViewer.Zoom, app.ImageViewer.PanX, app.ImageViewer.Fit = 2, 24, false
	close(backend.release)
	for i := 0; i < 2; i++ {
		select {
		case apply := <-posted:
			apply()
		case <-time.After(time.Second):
			t.Fatal("image requests did not finish")
		}
	}
	if !app.ImageViewer.Open || app.ImageViewer.URLs[0] != "https://fixture.invalid/new.png" || app.ImageViewer.Zoom != 2 || app.ImageViewer.PanX != 24 || app.ImageViewer.Fit {
		t.Fatal("image completion restored an old viewer or reset the user's current interaction")
	}
}

type failedPlayerPreferencesBackend struct{ service.BackendAPI }

func (b failedPlayerPreferencesBackend) UpdateUIState(uistate.State) (uistate.State, error) {
	return b.GetUIState(), errors.New("synthetic preferences write failure")
}

func TestPlayerOffersVolumeAndDensityAndReportsPreferenceFailure(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	app.PlayerOverlay.Open = true
	tester := ui.NewTester(app.View, 720, 560)
	tester.Click("播放器音量")
	if _, ok := tester.Find("播放器音量调节"); !ok {
		t.Fatal("the volume control only toggles mute and offers no level adjustment")
	}
	tester.Click("播放器更多菜单")
	if _, ok := tester.Find("弹幕密度选择"); !ok {
		t.Fatal("required danmaku density control is missing")
	}
	app.backend = failedPlayerPreferencesBackend{app.backend}
	app.applyAndPersistPlayerPrefs()
	if !strings.Contains(app.BannerError, "preferences write failure") {
		t.Fatal("player silently reported preference persistence as successful")
	}
}

func TestPlayerPreferencesRestoreAreaAndSilentVolume(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	st := app.backend.GetUIState()
	st.Player.DanmakuArea, st.Player.Volume = 1, 0
	st.Player.DanmakuDensity = "high"
	if _, err := app.backend.UpdateUIState(st); err != nil {
		t.Fatal(err)
	}
	restored := NewApp(app.backend, player.NewNullAudioSink())
	defer restored.Close()
	if restored.PlayerOverlay.DanmakuArea != "full" || restored.PlayerOverlay.DanmakuDensity != "高" {
		t.Fatal("player menu reset the saved danmaku preferences")
	}
	if snap := restored.player.Snapshot(640, 360); snap.Volume != 0 || snap.DanmakuArea != 1 || snap.DanmakuDensity != "high" {
		t.Fatalf("player did not restore its silent volume and full display area: volume=%v area=%v", snap.Volume, snap.DanmakuArea)
	}
}

func TestPlayerVolumeAndDensityInputsChangePlaybackAndPersist(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if err := app.player.LoadDecoder("explicit media fixture", player.NewSynthDecoder(16, 16, time.Second, 30)); err != nil {
		t.Fatal(err)
	}
	if err := app.player.LoadDanmakuXML([]byte(`<i><d p="0.2,1,25,16777215,1710000000,0,u1,1">合成弹幕</d></i>`)); err != nil {
		t.Fatal(err)
	}
	app.player.SetSubtitles([]player.SubtitleTrack{{Lang: "zh-CN", Cues: []player.SubtitleCue{{StartSec: 0, EndSec: 1, Text: "合成字幕"}}}})
	app.PlayerOverlay.SubtitleOptions = []string{"关闭", "zh-CN"}
	app.PlayerOverlay.Open = true
	tester := ui.NewTester(app.View, 720, 560)
	if err := tester.Click("播放器音量"); err != nil {
		t.Fatal(err)
	}
	slider, ok := tester.Find("播放器音量调节")
	if !ok {
		t.Fatal("missing volume slider")
	}
	tester.ClickAt(slider.X+slider.W*0.3, slider.Y+slider.H*0.5)
	volume := app.player.Snapshot(1, 1).Volume
	if volume < 0.2 || volume > 0.4 || app.backend.GetUIState().Player.Volume != volume {
		t.Fatalf("volume input did not adjust and persist playback: %v", volume)
	}
	if err := tester.Click("播放器静音"); err != nil {
		t.Fatal(err)
	}
	if !app.player.Snapshot(1, 1).Muted || !app.backend.GetUIState().Player.Muted {
		t.Fatal("mute input was not applied and persisted")
	}
	if err := tester.Click("播放器更多菜单"); err != nil {
		t.Fatal(err)
	}
	if err := tester.Click("弹幕密度选择"); err != nil {
		t.Fatal(err)
	}
	if err := tester.Click("高"); err != nil {
		t.Fatal(err)
	}
	if app.player.Snapshot(1, 1).DanmakuDensity != "high" || app.backend.GetUIState().Player.DanmakuDensity != "high" {
		t.Fatal("density selection did not change and persist playback")
	}
	// F-042: the area menu shows Chinese choices, never the stored machine values.
	if err := tester.Click("弹幕显示区域"); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"quarter", "half", "full"} {
		if _, ok := tester.Find(raw); ok {
			t.Fatalf("danmaku area menu exposes the machine value %q", raw)
		}
	}
	if err := tester.Click("全屏"); err != nil {
		t.Fatal(err)
	}
	if app.player.Snapshot(1, 1).DanmakuArea != 1 || app.backend.GetUIState().Player.DanmakuArea != 1 || app.PlayerOverlay.DanmakuArea != "full" {
		t.Fatal("area selection did not change and persist playback")
	}
	if app.PlayerOverlay.SubtitleLang != "关闭" {
		t.Fatal("default subtitle selection is blank instead of clearly off")
	}
	if err := tester.Click("字幕语言选择"); err != nil {
		t.Fatal(err)
	}
	if err := tester.Click("zh-CN"); err != nil {
		t.Fatal(err)
	}
	if app.player.Snapshot(1, 1).SubtitleLang != "zh-CN" || app.backend.GetUIState().Player.SubtitleLang != "zh-CN" {
		t.Fatal("subtitle language selection was not applied and persisted")
	}
	if err := tester.Click("字幕语言选择"); err != nil {
		t.Fatal(err)
	}
	if err := tester.Click("关闭"); err != nil {
		t.Fatal(err)
	}
	if app.player.Snapshot(1, 1).SubtitleLang != "" || app.backend.GetUIState().Player.SubtitleLang != "" {
		t.Fatal("turning subtitles off did not persist the empty language identifier")
	}
	restored := NewApp(app.backend, player.NewNullAudioSink())
	defer restored.Close()
	if restored.PlayerOverlay.SubtitleLang != "关闭" {
		t.Fatal("restoring the off preference left the subtitle menu blank")
	}
}

func TestPlayerMissingSidecarsExplainAndDisableTheirControls(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	app.PlayerOverlay.Open = true
	app.PlayerOverlay.SubtitleOptions = []string{"关闭"}
	tester := ui.NewTester(app.View, 720, 560)
	if err := tester.Click("播放器更多菜单"); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"弹幕不可用说明", "字幕不可用说明"} {
		if _, ok := tester.Find(label); !ok {
			t.Fatalf("missing sidecar was not explained: %s", label)
		}
	}
	before := app.PlayerOverlay.DanmakuEnabled
	if err := tester.Click("弹幕开关"); err != nil {
		t.Fatal(err)
	}
	if app.PlayerOverlay.DanmakuEnabled != before {
		t.Fatal("an unavailable danmaku control still changed playback preferences")
	}
	if err := tester.Click("弹幕密度选择"); err != nil {
		t.Fatal(err)
	}
	if _, ok := tester.Find("高"); ok {
		t.Fatal("an unavailable density control opened its choices")
	}
}

func TestHiddenImageControlsWakeOnOrdinaryKey(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	app.ImageViewer = ImageViewerState{Open: true, Zoom: 1, Fit: true}
	tester := ui.NewTester(app.View, 720, 560)
	tester.Advance(2100 * time.Millisecond)
	if !app.ImageViewer.ControlsHidden {
		t.Fatal("image controls did not hide after inactivity")
	}
	tester.Key(0, ui.KeyB)
	if app.ImageViewer.ControlsHidden {
		t.Fatal("an ordinary key did not wake hidden media controls")
	}
}
