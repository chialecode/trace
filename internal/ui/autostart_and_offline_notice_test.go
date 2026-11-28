package ui

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/player"
	"github.com/chialecode/trace/internal/service"
	"github.com/egoist/mygo/ui"
)

func newAutostartTestApp(t *testing.T, channel, mode, targetGOOS, version string, reg *service.MemoryAutostartRegistry) (*App, *service.Backend, func()) {
	t.Helper()
	root := t.TempDir()
	ch := appdirs.EffectiveChannel(channel)
	appName := appdirs.AppNameForChannel(ch)
	loc := appdirs.Locations{
		Channel:         ch,
		Mode:            mode,
		AppName:         appName,
		DisplayName:     appdirs.DisplayNameForChannel(ch),
		Identifier:      appdirs.IdentifierForChannel(ch),
		InstallDir:      filepath.Join(root, "Programs", appName),
		DataDir:         filepath.Join(root, appName),
		DefaultCacheDir: filepath.Join(root, "cache", appName),
		CredentialsDir:  filepath.Join(root, appName, "credentials"),
		LogsDir:         filepath.Join(root, appName, "logs"),
		LockFile:        filepath.Join(root, appName, "app.lock"),
	}
	if err := appdirs.EnsureDirectories(loc); err != nil {
		t.Fatal(err)
	}
	b, err := service.NewBackend(service.Options{
		Locations:         loc,
		Desktop:           service.NewMockDesktop(),
		DisableBgWorkers:  true,
		AutostartRegistry: reg,
		AutostartExePath:  `C:\Users\demo\AppData\Local\Programs\` + appName + `\trace.exe`,
		AutostartVersion:  version,
		AutostartGOOS:     targetGOOS,
	})
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(b, player.NewNullAudioSink())
	return app, b, func() {
		app.Close()
		_ = b.Close()
	}
}

func TestSettingsGeneralAutostartToggleErrorAndResponsiveLayout(t *testing.T) {
	reg := service.NewMemoryAutostartRegistry()
	devVal := appdirs.AutostartValueNameForChannel("dev")
	stableVal := appdirs.AutostartValueNameForChannel("stable")
	if err := reg.Set(stableVal, `"C:\Apps\trace\trace.exe" --background`); err != nil {
		t.Fatal(err)
	}

	app, b, cleanup := newAutostartTestApp(t, "dev", "installed", "windows", "0.1.3-dev.202611280100", reg)
	defer cleanup()
	app.Navigate(RouteSettings)
	app.SettingsCategory = "general"

	// Responsive zero-overflow check at 1280 / 1000 / 720.
	for _, w := range []int{1280, 1000, 720} {
		tt := ui.NewTester(app.View, w, 860)
		tt.Frame()
		if !tt.HasText("开机自动启动（默认最小化到托盘）") {
			t.Fatalf("width=%d: missing autostart toggle title", w)
		}
		for _, lbl := range []string{"关闭窗口动作选择", "开机自动启动开关"} {
			r, ok := tt.Find(lbl)
			if !ok || r.W <= 0 || r.H <= 0 || r.X < -0.5 || r.X+r.W > float32(w)+0.5 {
				t.Fatalf("width=%d: control %q missing or overflows viewport: ok=%v rect=%+v", w, lbl, ok, r)
			}
		}
	}

	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()

	// Toggle ON: writes only dev Run entry and persists AutoStart=true.
	if err := tt.Click("开机自动启动开关"); err != nil {
		t.Fatalf("enable autostart click: %v", err)
	}
	tt.Frame()
	if !b.GetSettings().Window.AutoStart || !app.DraftSettings.Window.AutoStart {
		t.Fatal("expected AutoStart=true after toggle ON")
	}
	snap := reg.Snapshot()
	if snap[devVal] != `"C:\Users\demo\AppData\Local\Programs\trace-dev\trace.exe" --background` {
		t.Fatalf("dev Run entry = %q", snap[devVal])
	}
	if snap[stableVal] != `"C:\Apps\trace\trace.exe" --background` {
		t.Fatalf("stable Run entry clobbered: %q", snap[stableVal])
	}

	// Toggle OFF: removes only dev Run entry.
	if err := tt.Click("开机自动启动开关"); err != nil {
		t.Fatalf("disable autostart click: %v", err)
	}
	tt.Frame()
	if b.GetSettings().Window.AutoStart || app.DraftSettings.Window.AutoStart {
		t.Fatal("expected AutoStart=false after toggle OFF")
	}
	snap = reg.Snapshot()
	if _, exists := snap[devVal]; exists {
		t.Fatalf("dev Run entry not removed: %+v", snap)
	}
	if snap[stableVal] != `"C:\Apps\trace\trace.exe" --background` {
		t.Fatalf("stable Run entry touched on dev toggle OFF: %+v", snap)
	}

	// Registry write error reverts toggle and shows inline error.
	reg.SetErr = errors.New("registry access denied")
	if err := tt.Click("开机自动启动开关"); err != nil {
		t.Fatalf("click with SetErr: %v", err)
	}
	tt.Frame()
	if b.GetSettings().Window.AutoStart || app.DraftSettings.Window.AutoStart {
		t.Fatal("expected AutoStart reverted to false on registry write failure")
	}
	if _, ok := tt.Find("开机自动启动错误"); !ok || !tt.HasText("写入 Windows 开机启动项失败：registry access denied") {
		t.Fatalf("expected inline autostart error, got %q", app.GeneralAutostartError)
	}

	// Switching settings category clears transient autostart error.
	if err := tt.Click("设置分类：网络"); err != nil {
		t.Fatalf("switch to network category: %v", err)
	}
	tt.Frame()
	if app.GeneralAutostartError != "" {
		t.Fatalf("expected GeneralAutostartError cleared on category switch, got %q", app.GeneralAutostartError)
	}

	// Leaving RouteSettings also clears transient autostart error.
	app.SettingsCategory = "general"
	app.GeneralAutostartError = "stale error"
	app.Navigate(RouteHome)
	if app.GeneralAutostartError != "" {
		t.Fatalf("expected GeneralAutostartError cleared on leaving settings, got %q", app.GeneralAutostartError)
	}
}

func TestSettingsGeneralAutostartUnsupportedFeedback(t *testing.T) {
	reg := service.NewMemoryAutostartRegistry()
	app, b, cleanup := newAutostartTestApp(t, "stable", "installed", "linux", "0.1.3", reg)
	defer cleanup()
	app.Navigate(RouteSettings)
	app.SettingsCategory = "general"

	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	if !tt.HasText("开机自动启动仅支持 Windows 桌面版") {
		t.Fatal("expected clear non-Windows unsupported reason")
	}
	// Clicking disabled switch must not enable autostart or touch registry.
	_ = tt.Click("开机自动启动开关")
	tt.Frame()
	if b.GetSettings().Window.AutoStart {
		t.Fatal("disabled switch must not enable autostart")
	}
	if len(reg.Snapshot()) != 0 {
		t.Fatalf("registry touched while unsupported: %+v", reg.Snapshot())
	}
}

func TestOfflineLiveNoticeFloatingStackLayoutPersistenceAndActions(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()
	app.PopulateSyntheticFeed(6)
	app.Navigate(RouteHome)

	bar := ui.TitleBar{Height: 32, Right: 138}
	for _, w := range []int{1280, 1000, 720} {
		app.ClearNotifications()
		tt := ui.NewTester(app.View, w, 860)
		tt.SetTitleBar(bar)
		tt.Frame()

		topBefore, okTop := tt.Find("页面顶部栏")
		centerBefore, okCenter := tt.Find("中栏内容")
		if !okTop || !okCenter {
			t.Fatalf("width=%d: missing baseline landmarks", w)
		}

		app.ShowOfflineLiveNotice(service.OfflineLiveGapNotice{
			Message: service.OfflineLiveGapMessage,
			Gap:     2*time.Hour + 15*time.Minute,
		})
		tt.Frame()

		topAfter, _ := tt.Find("页面顶部栏")
		centerAfter, _ := tt.Find("中栏内容")
		if topAfter != topBefore || centerAfter != centerBefore {
			t.Fatalf("width=%d: offline live notice shifted layout", w)
		}
		if !tt.HasText(service.OfflineLiveGapMessage) {
			t.Fatalf("width=%d: missing exact offline live gap message", w)
		}
		for _, lbl := range []string{"悬浮通知区", "离线开播提示", "离线提示前往订阅", "离线提示查看直播", "关闭离线开播提示"} {
			r, ok := tt.Find(lbl)
			if !ok || r.W <= 0 || r.H <= 0 || r.X < -0.5 || r.Y < -0.5 || r.X+r.W > float32(w)+0.5 || r.Y+r.H > 860.5 {
				t.Fatalf("width=%d: control %q missing or out of bounds: ok=%v rect=%+v", w, lbl, ok, r)
			}
		}
	}

	// Persistence (never auto-dismisses), stacking under newer notice, and quick actions.
	app.ClearNotifications()
	app.Navigate(RouteHome)
	app.ShowOfflineLiveNotice(service.OfflineLiveGapNotice{
		Message: service.OfflineLiveGapMessage,
		Gap:     45 * time.Minute,
	})
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	tt.Advance(10 * time.Minute)
	tt.Frame()
	if _, ok := tt.Find("离线开播提示"); !ok {
		t.Fatal("offline live notice must persist until user acts or dismisses (DEC-086)")
	}

	// Newer notice stacks cleanly on top; closing it reveals the offline live notice below.
	app.notifyInfo("已复制链接")
	tt.Frame()
	if !tt.HasText("还有 1 条") {
		t.Fatal("expected stacked count 还有 1 条")
	}
	if err := tt.Click("关闭消息提示"); err != nil {
		t.Fatalf("close newer info notice: %v", err)
	}
	tt.Frame()
	if _, ok := tt.Find("离线开播提示"); !ok {
		t.Fatal("expected offline live notice revealed underneath")
	}

	// Clicking 前往订阅 dismisses notice and navigates to RouteSubscriptions.
	if err := tt.Click("离线提示前往订阅"); err != nil {
		t.Fatalf("click 前往订阅: %v", err)
	}
	tt.Frame()
	if app.CurrentRoute != RouteSubscriptions {
		t.Fatalf("CurrentRoute = %q, want %q", app.CurrentRoute, RouteSubscriptions)
	}
	if _, ok := tt.Find("离线开播提示"); ok {
		t.Fatal("expected offline live notice dismissed after clicking 前往订阅")
	}

	// Clicking 查看直播 dismisses notice and opens Tasks › Live.
	app.ShowOfflineLiveNotice(service.OfflineLiveGapNotice{
		Message: service.OfflineLiveGapMessage,
		Gap:     3 * time.Hour,
	})
	tt.Frame()
	if err := tt.Click("离线提示查看直播"); err != nil {
		t.Fatalf("click 查看直播: %v", err)
	}
	tt.Frame()
	if app.CurrentRoute != RouteTasks || app.effectiveTasksSection() != TasksSectionLive {
		t.Fatalf("route/section = (%q, %q), want (%q, %q)", app.CurrentRoute, app.effectiveTasksSection(), RouteTasks, TasksSectionLive)
	}
	if _, ok := tt.Find("离线开播提示"); ok {
		t.Fatal("expected offline live notice dismissed after clicking 查看直播")
	}

	// Close button dismisses cleanly.
	app.ShowOfflineLiveNotice(service.OfflineLiveGapNotice{
		Message: service.OfflineLiveGapMessage,
		Gap:     time.Hour,
	})
	tt.Frame()
	if err := tt.Click("关闭离线开播提示"); err != nil {
		t.Fatalf("click close offline notice: %v", err)
	}
	tt.Frame()
	if _, ok := tt.Find("离线开播提示"); ok {
		t.Fatal("expected offline live notice dismissed after close click")
	}
}
