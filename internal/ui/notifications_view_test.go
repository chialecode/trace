package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/update"
	"github.com/egoist/mygo/ui"
)

type stubNotificationUpdater struct {
	st             update.State
	dismissed      bool
	dismissUpdated bool
	downloadCalls  int
	restartCalls   int
}

func (u *stubNotificationUpdater) State() update.State {
	st := u.st
	if u.dismissed {
		st.BannerDismissed = true
	}
	if u.dismissUpdated {
		st.UpdatedTo = ""
		st.UpdatedNotes = ""
	}
	return st
}
func (u *stubNotificationUpdater) CheckNow(context.Context) {}
func (u *stubNotificationUpdater) Download(context.Context) { u.downloadCalls++ }
func (u *stubNotificationUpdater) DismissBanner()           { u.dismissed = true }
func (u *stubNotificationUpdater) DismissUpdatedNotice()    { u.dismissUpdated = true }
func (u *stubNotificationUpdater) RestartPrompt() string    { return "" }
func (u *stubNotificationUpdater) Restart()                 { u.restartCalls++ }
func (u *stubNotificationUpdater) PrefsChanged()            {}

func TestFloatingNotificationsDoNotShiftPageLayout(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()
	app.PopulateSyntheticFeed(10)
	app.Navigate(RouteHome)

	upd := &stubNotificationUpdater{
		st: update.State{
			Enabled:        true,
			Channel:        "stable",
			CurrentVersion: "0.1.2",
			Phase:          update.PhaseReady,
			Release: &update.Release{
				Version: "0.1.3",
				Notes:   "- 通知统一移至右下角悬浮气泡\n- 修复主线程卡顿问题",
			},
			Prefs: update.Prefs{AutoCheck: true, AutoDownload: true},
		},
	}

	bar := ui.TitleBar{Height: 32, Right: 138}
	for _, w := range []int{1280, 1000, 720} {
		app.BindUpdater(nil)
		app.UpdateNotesOpen = false
		app.BannerError = ""
		app.BannerLoginSite = ""
		app.clearBannerInfo()
		delete(app.pending, "check-updates")

		tt := ui.NewTester(app.View, w, 860)
		tt.SetTitleBar(bar)
		tt.Frame()

		topBefore, okTop := tt.Find("页面顶部栏")
		centerBefore, okCenter := tt.Find("中栏内容")
		refreshBefore, okRef := tt.Find("主页刷新")
		if !okTop || !okCenter || !okRef {
			t.Fatalf("width=%d: missing baseline landmarks (top=%v center=%v refresh=%v)", w, okTop, okCenter, okRef)
		}
		if _, ok := tt.Find("悬浮通知区"); ok {
			t.Fatalf("width=%d: expected no 悬浮通知区 when no notifications are active", w)
		}

		// Enable update bubble (with expanded notes), error bubble (with login CTA),
		// info bubble, and foreground loading indicator simultaneously.
		app.BindUpdater(upd)
		app.UpdateNotesOpen = true
		app.BannerError = "抖音登录已失效，请重新粘贴 Cookie"
		app.BannerLoginSite = "douyin"
		app.notifyInfo("已加入缓存队列：BV1xx411c7mD")
		app.pending["check-updates"] = true
		tt.Frame()

		topAfter, okTopAfter := tt.Find("页面顶部栏")
		centerAfter, okCenterAfter := tt.Find("中栏内容")
		refreshAfter, okRefAfter := tt.Find("主页刷新")
		if !okTopAfter || !okCenterAfter || !okRefAfter {
			t.Fatalf("width=%d: missing landmarks with notifications active", w)
		}
		if topAfter != topBefore {
			t.Fatalf("width=%d: 页面顶部栏 shifted/resized: before=%+v after=%+v", w, topBefore, topAfter)
		}
		if centerAfter != centerBefore {
			t.Fatalf("width=%d: 中栏内容 shifted/resized: before=%+v after=%+v", w, centerBefore, centerAfter)
		}
		if refreshAfter != refreshBefore {
			t.Fatalf("width=%d: 主页刷新 shifted: before=%+v after=%+v", w, refreshBefore, refreshAfter)
		}

		stack, okStack := tt.Find("悬浮通知区")
		if !okStack {
			t.Fatalf("width=%d: missing 悬浮通知区", w)
		}
		if stack.X < -0.5 || stack.Y < -0.5 || stack.X+stack.W > float32(w)+0.5 || stack.Y+stack.H > 860.5 {
			t.Fatalf("width=%d: 悬浮通知区 overflows viewport: %+v", w, stack)
		}
		// Anchored to bottom-right corner with ~16px inset.
		if stack.X+stack.W < float32(w)-24 {
			t.Fatalf("width=%d: 悬浮通知区 is not right-aligned: %+v", w, stack)
		}
		if stack.Y+stack.H < 860-24 {
			t.Fatalf("width=%d: 悬浮通知区 is not bottom-aligned: %+v", w, stack)
		}
		for _, lbl := range []string{"更新提示", "更新内容", "关闭更新提示", "重启以更新到新版本", "全局错误提示", "提示去登录", "关闭错误提示", "全局消息提示", "关闭消息提示", "请求进行中"} {
			r, ok := tt.Find(lbl)
			if !ok || r.W <= 0 || r.H <= 0 || r.X < -0.5 || r.Y < -0.5 || r.X+r.W > float32(w)+0.5 || r.Y+r.H > 860.5 {
				t.Fatalf("width=%d: control %q missing or out of bounds: ok=%v rect=%+v", w, lbl, ok, r)
			}
		}
		delete(app.pending, "check-updates")
	}
}

func TestFloatingNotificationsInteractiveActionsAndPassThrough(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()
	app.Navigate(RouteHome)

	upd := &stubNotificationUpdater{
		st: update.State{
			Enabled:        true,
			Channel:        "stable",
			CurrentVersion: "0.1.2",
			Phase:          update.PhaseAvailable,
			Release:        &update.Release{Version: "0.1.3", Notes: "- 第一条更新\n- 第二条更新"},
			Prefs:          update.Prefs{AutoCheck: true, AutoDownload: false},
		},
	}
	app.BindUpdater(upd)
	app.BannerError = "抖音登录已失效，请重新粘贴 Cookie"
	app.BannerLoginSite = "douyin"
	app.notifyInfo("已复制链接")

	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()

	// 1. Clicking underlying navigation outside floating bubbles passes through cleanly.
	if err := tt.Click("订阅"); err != nil {
		t.Fatalf("click underlying nav while bubbles open: %v", err)
	}
	tt.Frame()
	if app.CurrentRoute != RouteSubscriptions {
		t.Fatalf("expected PassThrough click to switch to RouteSubscriptions, got %s", app.CurrentRoute)
	}

	// Re-arm error notification after route change (Navigate clears prior route errors).
	app.BannerError = "抖音登录已失效，请重新粘贴 Cookie"
	app.BannerLoginSite = "douyin"
	tt.Frame()

	// 2. Dismiss info bubble.
	if err := tt.Click("关闭消息提示"); err != nil {
		t.Fatalf("click 关闭消息提示: %v", err)
	}
	tt.Frame()
	if app.BannerInfo != "" {
		t.Fatalf("expected BannerInfo cleared, got %q", app.BannerInfo)
	}
	if _, ok := tt.Find("全局消息提示"); ok {
		t.Fatal("全局消息提示 remained visible after dismissal")
	}

	// 3. Toggle release notes inside update bubble.
	if err := tt.Click("更新内容"); err != nil {
		t.Fatalf("click 更新内容: %v", err)
	}
	tt.Frame()
	if !app.UpdateNotesOpen || !tt.HasText("第一条更新") {
		t.Fatalf("expected UpdateNotesOpen=true and notes visible, got open=%v", app.UpdateNotesOpen)
	}
	if err := tt.Click("收起更新内容"); err != nil {
		t.Fatalf("click 收起更新内容: %v", err)
	}
	tt.Frame()
	if app.UpdateNotesOpen {
		t.Fatal("expected UpdateNotesOpen=false after clicking 收起更新内容")
	}

	// 4. Primary update action ("下载并安装") and dismiss update bubble.
	if err := tt.Click("下载并安装新版本"); err != nil {
		t.Fatalf("click 下载并安装新版本: %v", err)
	}
	tt.Frame()
	if upd.downloadCalls != 1 {
		t.Fatalf("expected Download to be invoked once, got %d", upd.downloadCalls)
	}
	// Switch to PhaseReady and verify "重启以更新" triggers Restart.
	upd.st.Phase = update.PhaseReady
	tt.Frame()
	if err := tt.Click("重启以更新到新版本"); err != nil {
		t.Fatalf("click 重启以更新到新版本: %v", err)
	}
	tt.Frame()
	if upd.restartCalls != 1 {
		t.Fatalf("expected Restart to be invoked once, got %d", upd.restartCalls)
	}
	if err := tt.Click("关闭更新提示"); err != nil {
		t.Fatalf("click 关闭更新提示: %v", err)
	}
	tt.Frame()
	if !upd.dismissed {
		t.Fatal("expected DismissBanner to be invoked")
	}
	if _, ok := tt.Find("更新提示"); ok {
		t.Fatal("更新提示 remained visible after dismissal")
	}

	// 5. Error bubble "去登录" navigates to login with preselected site and clears error.
	if err := tt.Click("提示去登录"); err != nil {
		t.Fatalf("click 提示去登录: %v", err)
	}
	tt.Frame()
	if app.CurrentRoute != RouteLogin || app.LoginSite != "douyin" || app.BannerError != "" || app.BannerLoginSite != "" {
		t.Fatalf("unexpected state after 提示去登录: route=%s site=%q err=%q loginSite=%q",
			app.CurrentRoute, app.LoginSite, app.BannerError, app.BannerLoginSite)
	}

	// 6. Error bubble manual dismiss.
	app.Navigate(RouteHome)
	app.BannerError = "网络请求失败"
	tt.Frame()
	if err := tt.Click("关闭错误提示"); err != nil {
		t.Fatalf("click 关闭错误提示: %v", err)
	}
	tt.Frame()
	if app.BannerError != "" {
		t.Fatalf("expected BannerError cleared, got %q", app.BannerError)
	}
	if _, ok := tt.Find("悬浮通知区"); ok {
		t.Fatal("expected 悬浮通知区 absent once all bubbles dismissed")
	}
}

func TestFloatingInfoAutoDismissHoverPauseAndPersistentErrors(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()
	app.Navigate(RouteHome)

	upd := &stubNotificationUpdater{
		st: update.State{
			Enabled:        true,
			Channel:        "stable",
			CurrentVersion: "0.1.2",
			UpdatedTo:      "0.1.2",
			UpdatedNotes:   "- 已更新内容",
		},
	}
	app.BindUpdater(upd)
	app.BannerError = "持久化错误不应自动消失"
	app.notifyInfo("已复制链接")

	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()

	infoRect, ok := tt.Find("全局消息提示")
	if !ok {
		t.Fatal("expected 全局消息提示 on initial frame")
	}

	// Halfway through auto-dismiss window: still visible.
	tt.Advance(3 * time.Second)
	if app.BannerInfo != "已复制链接" || !tt.HasText("已复制链接") {
		t.Fatalf("info bubble dismissed too early at 3s: %q", app.BannerInfo)
	}

	// Hover over the info bubble: countdown pauses even across 10s.
	tt.Move(infoRect.X+infoRect.W/2, infoRect.Y+infoRect.H/2)
	tt.Frame()
	tt.Advance(10 * time.Second)
	if app.BannerInfo != "已复制链接" || !tt.HasText("已复制链接") {
		t.Fatalf("info bubble dismissed while hovered: %q", app.BannerInfo)
	}

	// Move pointer away: remaining ~3s elapses, then auto-dismisses.
	tt.Move(20, 20)
	tt.Frame()
	tt.Advance(2 * time.Second)
	if app.BannerInfo != "已复制链接" {
		t.Fatalf("info bubble dismissed before remaining time elapsed: %q", app.BannerInfo)
	}
	tt.Advance(2 * time.Second)
	if app.BannerInfo != "" {
		t.Fatalf("info bubble did not auto-dismiss after unhovered timeout: %q", app.BannerInfo)
	}
	if _, ok := tt.Find("全局消息提示"); ok {
		t.Fatal("全局消息提示 remained in view after auto-dismiss")
	}

	// Error and update bubbles must remain persistent even after 30s.
	tt.Advance(30 * time.Second)
	if app.BannerError == "" || !tt.HasText("持久化错误不应自动消失") {
		t.Fatal("error bubble unexpectedly auto-dismissed")
	}
	if _, ok := tt.Find("更新提示"); !ok {
		t.Fatal("update bubble unexpectedly auto-dismissed")
	}

	// Re-sending the exact same text restarts a fresh auto-dismiss window.
	app.notifyInfo("已复制链接")
	tt.Frame()
	tt.Advance(4 * time.Second)
	if app.BannerInfo != "已复制链接" {
		t.Fatal("re-sent info notification vanished before 6s")
	}
	app.notifyInfo("已复制链接")
	tt.Frame()
	tt.Advance(4 * time.Second)
	if app.BannerInfo != "已复制链接" {
		t.Fatal("duplicate notifyInfo did not refresh countdown")
	}
	tt.Advance(3 * time.Second)
	if app.BannerInfo != "" {
		t.Fatalf("refreshed info notification did not expire after timeout: %q", app.BannerInfo)
	}
}

func TestFloatingNotificationsBoundsLongNotesDedupeAndToastViewport(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()

	// 1. Long release notes remain vertically bounded even on a short 600px window.
	var notesLines []string
	for i := 1; i <= 25; i++ {
		notesLines = append(notesLines, fmt.Sprintf("- 第 %d 条较长的更新说明条目用于测试高度约束与滚动行为", i))
	}
	app.BindUpdater(&stubNotificationUpdater{
		st: update.State{
			Enabled:        true,
			Channel:        "stable",
			CurrentVersion: "0.1.2",
			Phase:          update.PhaseReady,
			Release:        &update.Release{Version: "0.1.3", Notes: strings.Join(notesLines, "\n")},
			Prefs:          update.Prefs{AutoCheck: true, AutoDownload: true},
		},
	})
	app.UpdateNotesOpen = true
	app.Navigate(RouteHome)

	tt := ui.NewTester(app.View, 720, 600)
	tt.Frame()
	updRect, ok := tt.Find("更新提示")
	if !ok {
		t.Fatal("missing 更新提示 for long release notes")
	}
	if updRect.Y < 0 || updRect.Y+updRect.H > 600.5 || updRect.X < 0 || updRect.X+updRect.W > 720.5 {
		t.Fatalf("long update bubble overflowed 720x600 viewport: %+v", updRect)
	}
	app.BindUpdater(nil)
	app.UpdateNotesOpen = false

	// 2. Deduplicate inline errors on RouteParse and RouteLogin.
	app.Navigate(RouteParse)
	app.LastParseError = "无法识别的链接"
	app.BannerError = "无法识别的链接"
	tt.Frame()
	if _, ok := tt.Find("全局错误提示"); ok {
		t.Fatal("RouteParse should not show duplicate 全局错误提示 matching LastParseError")
	}

	app.Navigate(RouteLogin)
	app.LoginError = "凭证已失效"
	app.BannerError = "凭证已失效"
	tt.Frame()
	if _, ok := tt.Find("全局错误提示"); ok {
		t.Fatal("RouteLogin should not show duplicate 全局错误提示 matching LoginError")
	}
	app.BannerError = ""

	// 3. Direct Context.Toast calls also render inside our bottom-right ToastViewportBase.
	app.Navigate(RouteHome)
	var fired bool
	toastTester := ui.NewTester(func(c *ui.Context) {
		app.View(c)
		if !fired {
			fired = true
			c.Toast("外部轻提示")
		}
	}, 1000, 720)
	toastTester.Frame()
	toastTester.Frame()
	r, ok := toastTester.Find("外部轻提示")
	if !ok {
		t.Fatal("expected Context.Toast to render via unified ToastViewportBase")
	}
	if r.X < 500 || r.Y < 500 || r.X+r.W > 1000.5 || r.Y+r.H > 720.5 {
		t.Fatalf("expected Context.Toast in bottom-right quadrant, got %+v", r)
	}

	// 4. Floating info notification remains visible above PlayerOverlay and does not steal Esc.
	app.PlayerOverlay.Open = true
	app.notifyInfo("已加入缓存任务")
	tt2 := ui.NewTester(app.View, 1280, 860)
	tt2.Frame()
	if _, ok := tt2.Find("全局消息提示"); !ok {
		t.Fatal("expected 全局消息提示 visible while PlayerOverlay is open")
	}
	tt2.Key(0, ui.KeyEscape)
	tt2.Frame()
	if app.PlayerOverlay.Open {
		t.Fatal("Escape did not close PlayerOverlay while floating notification was visible")
	}
}
