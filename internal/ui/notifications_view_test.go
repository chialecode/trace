package ui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/update"
	"github.com/egoist/mygo/ui"
)

type stubNotificationUpdater struct {
	mu             sync.Mutex
	st             update.State
	dismissed      bool
	dismissUpdated bool
	downloadCalls  int
	downloadCh     chan struct{}
	restartCalls   int
	restartPrompt  string
}

func (u *stubNotificationUpdater) State() update.State {
	u.mu.Lock()
	defer u.mu.Unlock()
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
func (u *stubNotificationUpdater) Download(context.Context) {
	u.mu.Lock()
	u.downloadCalls++
	ch := u.downloadCh
	u.mu.Unlock()
	if ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
func (u *stubNotificationUpdater) DismissBanner() {
	u.mu.Lock()
	u.dismissed = true
	u.mu.Unlock()
}
func (u *stubNotificationUpdater) DismissUpdatedNotice() {
	u.mu.Lock()
	u.dismissUpdated = true
	u.mu.Unlock()
}
func (u *stubNotificationUpdater) RestartPrompt() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.restartPrompt
}
func (u *stubNotificationUpdater) Restart() {
	u.mu.Lock()
	u.restartCalls++
	u.mu.Unlock()
}
func (u *stubNotificationUpdater) PrefsChanged() {}

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
		downloadCh: make(chan struct{}, 2),
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
		app.clearErrorNotifications()
		app.clearInfoNotifications()
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

		// Queue update, error, and info notifications in order + foreground loading indicator.
		// Newest notification covers older ones on top without shifting page layout.
		app.BindUpdater(upd)
		app.UpdateNotesOpen = true
		tt.Frame()
		app.notifyError("抖音登录已失效，请重新粘贴 Cookie", "douyin")
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
		if stack.X+stack.W < float32(w)-24 {
			t.Fatalf("width=%d: 悬浮通知区 is not right-aligned: %+v", w, stack)
		}
		if stack.Y+stack.H < 860-24 {
			t.Fatalf("width=%d: 悬浮通知区 is not bottom-aligned: %+v", w, stack)
		}
		// Topmost bubble is the newest info bubble with "+2" remaining underneath.
		for _, lbl := range []string{"全局消息提示", "剩余通知数", "关闭消息提示", "请求进行中"} {
			r, ok := tt.Find(lbl)
			if !ok || r.W <= 0 || r.H <= 0 || r.X < -0.5 || r.Y < -0.5 || r.X+r.W > float32(w)+0.5 || r.Y+r.H > 860.5 {
				t.Fatalf("width=%d: control %q missing or out of bounds: ok=%v rect=%+v", w, lbl, ok, r)
			}
		}
		if !tt.HasText("还有 2 条") {
			t.Fatalf("width=%d: expected stacked remaining count 还有 2 条", w)
		}
		// Covered older bubbles must not expose clickable controls until top bubble is closed.
		if _, ok := tt.Find("提示去登录"); ok {
			t.Fatalf("width=%d: covered error bubble should stay underneath until top bubble is dismissed", w)
		}
		delete(app.pending, "check-updates")
	}
}

func TestFloatingNotificationsOverlayStackDismissOneByOneAndPassThrough(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()
	app.Navigate(RouteHome)

	upd := &stubNotificationUpdater{
		downloadCh: make(chan struct{}, 2),
		st: update.State{
			Enabled:        true,
			Channel:        "stable",
			CurrentVersion: "0.1.2",
			Phase:          update.PhaseAvailable,
			Release:        &update.Release{Version: "0.1.3", Notes: "- 第一条更新\n- 第二条更新"},
			Prefs:          update.Prefs{AutoCheck: true, AutoDownload: false},
		},
	}

	// Arrival order (oldest -> newest):
	// 1. Update notice
	// 2. First info notice ("已加入缓存队列：BV1")
	// 3. Second info notice ("已复制链接")
	app.BindUpdater(upd)
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()

	app.notifyInfo("已加入缓存队列：BV1")
	app.notifyInfo("已复制链接")
	tt.Frame()

	// 1. Clicking underlying navigation outside floating bubble passes through cleanly.
	if err := tt.Click("订阅"); err != nil {
		t.Fatalf("click underlying nav while notification bubble open: %v", err)
	}
	tt.Frame()
	if app.CurrentRoute != RouteSubscriptions {
		t.Fatalf("expected PassThrough click to switch to RouteSubscriptions, got %s", app.CurrentRoute)
	}

	// 2. Push an error notice on top of the 3 existing notices -> 4 total ("还有 3 条").
	app.notifyError("抖音登录已失效，请重新粘贴 Cookie", "douyin")
	tt.Frame()
	if !tt.HasText("抖音登录已失效，请重新粘贴 Cookie") || !tt.HasText("还有 3 条") {
		t.Fatal("expected newest error bubble covering 3 older notifications")
	}
	if _, ok := tt.Find("全局消息提示"); ok {
		t.Fatal("older info bubble should remain underneath while error covers it")
	}

	// 3. Close top error bubble -> reveals newest info ("已复制链接") with "还有 2 条".
	if err := tt.Click("关闭错误提示"); err != nil {
		t.Fatalf("click 关闭错误提示: %v", err)
	}
	tt.Frame()
	if app.BannerError != "" {
		t.Fatalf("expected BannerError cleared after closing only error, got %q", app.BannerError)
	}
	if !tt.HasText("已复制链接") || !tt.HasText("还有 2 条") {
		t.Fatal("expected closing error to reveal 已复制链接 with 还有 2 条")
	}

	// 4. Close "已复制链接" -> reveals older info ("已加入缓存队列：BV1") with "还有 1 条".
	if err := tt.Click("关闭消息提示"); err != nil {
		t.Fatalf("click 关闭消息提示 (1st info): %v", err)
	}
	tt.Frame()
	if app.BannerInfo != "已加入缓存队列：BV1" {
		t.Fatalf("expected BannerInfo to fall back to older info, got %q", app.BannerInfo)
	}
	if !tt.HasText("已加入缓存队列：BV1") || !tt.HasText("还有 1 条") {
		t.Fatal("expected closing newest info to reveal 已加入缓存队列：BV1 with 还有 1 条")
	}

	// 5. Close "已加入缓存队列：BV1" -> reveals update bubble with no remaining count badge.
	if err := tt.Click("关闭消息提示"); err != nil {
		t.Fatalf("click 关闭消息提示 (2nd info): %v", err)
	}
	tt.Frame()
	if app.BannerInfo != "" {
		t.Fatalf("expected BannerInfo empty once all info bubbles closed, got %q", app.BannerInfo)
	}
	if _, ok := tt.Find("更新提示"); !ok {
		t.Fatal("expected update bubble revealed after closing all newer bubbles")
	}
	if _, ok := tt.Find("剩余通知数"); ok {
		t.Fatal("expected no 剩余通知数 badge when only 1 notification remains")
	}

	// 6. Toggle release notes, invoke Download & Restart, then close update bubble.
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
	if err := tt.Click("下载并安装新版本"); err != nil {
		t.Fatalf("click 下载并安装新版本: %v", err)
	}
	select {
	case <-upd.downloadCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Download invocation")
	}
	tt.Frame()
	upd.mu.Lock()
	dlCalls := upd.downloadCalls
	upd.st.Phase = update.PhaseReady
	upd.mu.Unlock()
	if dlCalls != 1 {
		t.Fatalf("expected Download invoked once, got %d", dlCalls)
	}
	tt.Frame()
	if err := tt.Click("重启以更新到新版本"); err != nil {
		t.Fatalf("click 重启以更新到新版本: %v", err)
	}
	tt.Frame()
	upd.mu.Lock()
	rstCalls := upd.restartCalls
	upd.mu.Unlock()
	if rstCalls != 1 {
		t.Fatalf("expected Restart invoked once, got %d", rstCalls)
	}
	if err := tt.Click("关闭更新提示"); err != nil {
		t.Fatalf("click 关闭更新提示: %v", err)
	}
	tt.Frame()
	upd.mu.Lock()
	dismissed := upd.dismissed
	upd.mu.Unlock()
	if !dismissed {
		t.Fatal("expected DismissBanner invoked")
	}
	if _, ok := tt.Find("悬浮通知区"); ok {
		t.Fatal("expected 悬浮通知区 absent once all bubbles are closed one by one")
	}

	// 7. Error bubble "去登录" closes active modals/overlays, navigates to /login with
	// preselected site, and preserves both older errors and info notifications underneath.
	app.notifyInfo("后台任务完成")
	app.notifyError("任务状态写入失败", "")
	app.notifyError("抖音登录已失效，请重新粘贴 Cookie", "douyin")
	app.PlayerOverlay.Open = true
	app.ConflictDiff.Open = true
	app.MultiTagBatch.Open = true
	tt.Frame()
	if err := tt.Click("提示去登录"); err != nil {
		t.Fatalf("click 提示去登录 over overlays: %v", err)
	}
	tt.Frame()
	if app.CurrentRoute != RouteLogin || app.LoginSite != "douyin" {
		t.Fatalf("expected RouteLogin with douyin preselected, got route=%s site=%q", app.CurrentRoute, app.LoginSite)
	}
	if app.PlayerOverlay.Open || app.ConflictDiff.Open || app.MultiTagBatch.Open {
		t.Fatalf("expected 提示去登录 to close all open modals/overlays: player=%v diff=%v batch=%v",
			app.PlayerOverlay.Open, app.ConflictDiff.Open, app.MultiTagBatch.Open)
	}
	if app.BannerError != "任务状态写入失败" || !tt.HasText("任务状态写入失败") || !tt.HasText("还有 1 条") {
		t.Fatalf("expected older error preserved on top of info notice after 提示去登录, got BannerError=%q BannerInfo=%q",
			app.BannerError, app.BannerInfo)
	}
	if err := tt.Click("关闭错误提示"); err != nil {
		t.Fatalf("click 关闭错误提示 for older preserved error: %v", err)
	}
	tt.Frame()
	if app.BannerInfo != "后台任务完成" || !tt.HasText("后台任务完成") {
		t.Fatalf("expected underlying info notice revealed after closing older error, got BannerInfo=%q", app.BannerInfo)
	}
}

func TestFloatingNotificationsSurviveNavigationAndDetailOpen(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()
	app.Navigate(RouteHome)

	// Queue an unread error covered underneath a newer info notice.
	app.notifyError("抖音登录已失效，请重新粘贴 Cookie", "douyin")
	app.notifyInfo("已加入缓存队列：BV1xx411c7mD")

	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()

	// Switching routes and opening offline library detail must not discard unread errors.
	app.Navigate(RouteSubscriptions)
	tt.Frame()
	app.Navigate(RouteLibrary)
	tt.Frame()
	app.OpenLibraryItemDetail(library.ItemMetadata{
		ItemKey: "bilibili:video:BV1xx411c7mD",
		Site:    "bilibili",
		Kind:    "video",
		RawID:   "BV1xx411c7mD",
		Title:   "示例本地视频",
	})
	tt.Frame()
	app.Navigate(RouteHome)
	tt.Frame()

	if !tt.HasText("已加入缓存队列：BV1xx411c7mD") || !tt.HasText("还有 1 条") {
		t.Fatal("expected info bubble covering unread error to survive route changes and detail open")
	}
	if err := tt.Click("关闭消息提示"); err != nil {
		t.Fatalf("click 关闭消息提示: %v", err)
	}
	tt.Frame()
	if app.BannerError != "抖音登录已失效，请重新粘贴 Cookie" || !tt.HasText("抖音登录已失效，请重新粘贴 Cookie") {
		t.Fatalf("expected covered error notice preserved after navigation, got BannerError=%q", app.BannerError)
	}
}

func TestFloatingNotificationsRestartConfirmAboveMediaOverlay(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()
	app.Navigate(RouteHome)

	upd := &stubNotificationUpdater{
		downloadCh: make(chan struct{}, 2),
		st: update.State{
			Enabled:        true,
			Channel:        "stable",
			CurrentVersion: "0.1.2",
			Phase:          update.PhaseReady,
			Release:        &update.Release{Version: "0.1.3", Notes: "- 更新说明"},
			Prefs:          update.Prefs{AutoCheck: true, AutoDownload: true},
		},
		restartPrompt: "重启会暂停 1 个缓存任务并结束 0 场录制，重启后可以继续缓存。",
	}
	app.BindUpdater(upd)
	app.PlayerOverlay.Open = true

	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()

	if err := tt.Click("重启以更新到新版本"); err != nil {
		t.Fatalf("click 重启以更新到新版本 over PlayerOverlay: %v", err)
	}
	tt.Frame()
	if !app.RestartConfirmOpen {
		t.Fatal("expected RestartConfirmOpen=true when RestartPrompt is non-empty")
	}
	if _, ok := tt.Find("重启以更新确认对话框"); !ok {
		t.Fatal("expected 重启以更新确认对话框 rendered on top of PlayerOverlay")
	}
	// Clicking confirm must reach the dialog above PlayerOverlay and invoke Restart.
	if err := tt.Click("确认重启以更新"); err != nil {
		t.Fatalf("click 确认重启以更新 above PlayerOverlay: %v", err)
	}
	tt.Frame()
	if upd.restartCalls != 1 {
		t.Fatalf("expected Restart called once after confirming above PlayerOverlay, got %d", upd.restartCalls)
	}
}

func TestFloatingNotificationsInlineErrorNoReboundAndDistinctErrorVisible(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()

	// 1. /login: TriggerAuthExpired sets both LoginError and BannerError (deduped).
	// Switching login site clears LoginError without rebounding old BannerError.
	app.TriggerAuthExpired("登录已失效，请重新登录")
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	if _, ok := tt.Find("全局错误提示"); ok {
		t.Fatal("RouteLogin should suppress duplicate BannerError matching LoginError")
	}
	if err := tt.Click("登录站点：抖音"); err != nil {
		t.Fatalf("switch login site to Douyin: %v", err)
	}
	tt.Frame()
	if _, ok := tt.Find("全局错误提示"); ok {
		t.Fatal("switching login site must not rebound cleared LoginError as floating bubble")
	}
	// A distinct background BannerError on /login still displays visibly.
	app.notifyError("后台任务写入失败", "")
	tt.Frame()
	if _, ok := tt.Find("全局错误提示"); !ok || !tt.HasText("后台任务写入失败") {
		t.Fatal("distinct BannerError on RouteLogin must still show as floating bubble")
	}

	// 2. /parse: parse error matching LastParseError is suppressed and does not rebound on SubmitParse("").
	app.CompleteLogin()
	app.Navigate(RouteParse)
	app.setLastParseError("无法识别的链接")
	app.notifyError("无法识别的链接", "")
	tt.Frame()
	if _, ok := tt.Find("全局错误提示"); ok {
		t.Fatal("RouteParse should suppress duplicate BannerError matching LastParseError")
	}
	app.SubmitParse("")
	tt.Frame()
	if _, ok := tt.Find("全局错误提示"); ok {
		t.Fatal("empty SubmitParse must not rebound prior LastParseError as floating bubble")
	}
	// A distinct background BannerError on /parse still displays visibly,
	// and leaving /parse clears only LastParseError without rebounding it.
	app.notifyError("磁盘空间不足", "")
	app.setLastParseError("另一条解析错误")
	app.notifyError("另一条解析错误", "")
	tt.Frame()
	if _, ok := tt.Find("全局错误提示"); !ok || !tt.HasText("磁盘空间不足") {
		t.Fatal("distinct BannerError on RouteParse must still show as floating bubble")
	}
	app.Navigate(RouteHome)
	tt.Frame()
	if tt.HasText("另一条解析错误") {
		t.Fatal("leaving RouteParse must not rebound prior LastParseError as floating bubble")
	}
	if !tt.HasText("磁盘空间不足") {
		t.Fatal("leaving RouteParse must preserve distinct background error 磁盘空间不足")
	}

	// 3. Concurrent auth failure while already on /login (no site logged in) updates
	// LoginError without rebounding the previously deduplicated LoginError.
	app.ClearNotifications()
	if _, err := app.backend.LogoutSite("bilibili"); err != nil {
		t.Fatal(err)
	}
	app.TriggerAuthExpired("首次登录已失效")
	tt.Frame()
	if _, ok := tt.Find("全局错误提示"); ok {
		t.Fatal("expected initial TriggerAuthExpired error deduplicated on RouteLogin")
	}
	app.showError(&site.Error{Site: site.Bilibili, Kind: site.ErrKindAuthRequired, Message: "二次登录已失效"})
	tt.Frame()
	if app.LoginError != "二次登录已失效" {
		t.Fatalf("expected LoginError updated to 二次登录已失效, got %q", app.LoginError)
	}
	if _, ok := tt.Find("全局错误提示"); ok {
		t.Fatalf("concurrent auth failure on RouteLogin must not rebound 首次登录已失效 as floating bubble (BannerError=%q)", app.BannerError)
	}
	// Leaving /login to /settings clears deduplicated LoginError cleanly without rebound.
	app.Navigate(RouteSettings)
	tt.Frame()
	if _, ok := tt.Find("全局错误提示"); ok {
		t.Fatalf("leaving RouteLogin must not rebound deduplicated LoginError (BannerError=%q)", app.BannerError)
	}
}

func TestFloatingNotificationsScreenReaderSingleCleanAnnouncement(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()
	app.Navigate(RouteHome)

	tt := ui.NewTester(app.View, 1280, 860)
	tt.UseScreenReader()
	tt.Frame()
	_ = tt.Announcements()

	// 1. Info notification announces clean message exactly once (no button/badge leakage).
	app.notifyInfo("已复制链接")
	tt.Frame()
	got := tt.Announcements()
	if len(got) != 1 || got[0] != "已复制链接" {
		t.Fatalf("info announcements = %q, want [\"已复制链接\"]", got)
	}

	// 2. Newer error covering info announces clean error text once (no "还有 1 条" or "关闭错误提示").
	app.notifyError("抖音请求失败", "")
	tt.Frame()
	got = tt.Announcements()
	if len(got) != 1 || got[0] != "抖音请求失败" {
		t.Fatalf("error announcements = %q, want [\"抖音请求失败\"]", got)
	}

	// 3. Newer update notice announces title once, and expanding release notes does not dump changelog.
	app.BindUpdater(&stubNotificationUpdater{
		st: update.State{
			Enabled:        true,
			Channel:        "stable",
			CurrentVersion: "0.1.2",
			Phase:          update.PhaseReady,
			Release:        &update.Release{Version: "0.1.3", Notes: "- 详细更新日志第一行\n- 详细更新日志第二行"},
			Prefs:          update.Prefs{AutoCheck: true, AutoDownload: true},
		},
	})
	tt.Frame()
	got = tt.Announcements()
	if len(got) != 1 || got[0] != "新版本 0.1.3 已就绪" {
		t.Fatalf("update announcements = %q, want [\"新版本 0.1.3 已就绪\"]", got)
	}

	if err := tt.Click("更新内容"); err != nil {
		t.Fatalf("click 更新内容: %v", err)
	}
	tt.Frame()
	got = tt.Announcements()
	if len(got) != 0 {
		t.Fatalf("expanding update notes should not trigger extra announcements, got %q", got)
	}
}

func TestFloatingNotificationsNeverAutoDismiss(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()
	app.Navigate(RouteHome)

	app.notifyInfo("较早的通知")
	app.notifyInfo("最新的通知")
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()

	// Even after 5 minutes with no hover, nothing auto-dismisses.
	tt.Advance(5 * time.Minute)
	tt.Frame()
	if app.BannerInfo != "最新的通知" || !tt.HasText("最新的通知") || !tt.HasText("还有 1 条") {
		t.Fatalf("floating notifications must never auto-dismiss: BannerInfo=%q", app.BannerInfo)
	}

	// Closing top reveals older notice, which also persists indefinitely.
	if err := tt.Click("关闭消息提示"); err != nil {
		t.Fatalf("click 关闭消息提示: %v", err)
	}
	tt.Frame()
	tt.Advance(5 * time.Minute)
	tt.Frame()
	if app.BannerInfo != "较早的通知" || !tt.HasText("较早的通知") {
		t.Fatalf("older notification must remain persistent until manually closed: BannerInfo=%q", app.BannerInfo)
	}
}

func TestFloatingNotificationsBoundsLongNotesDedupeAndToastViewport(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()

	// 1. Long release notes + stacked older notifications remain vertically bounded
	// even on a short 720x600 window.
	app.Navigate(RouteHome)
	app.notifyInfo("旧消息一")
	app.notifyInfo("旧消息二")
	app.notifyError("旧错误三", "")
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

	tt := ui.NewTester(app.View, 720, 600)
	tt.Frame()
	stackRect, okStack := tt.Find("悬浮通知区")
	updRect, ok := tt.Find("更新提示")
	if !ok || !okStack {
		t.Fatalf("missing 悬浮通知区 (%v) or 更新提示 (%v) for long release notes", okStack, ok)
	}
	if stackRect.Y < 0 || stackRect.Y+stackRect.H > 600.5 || stackRect.X < 0 || stackRect.X+stackRect.W > 720.5 {
		t.Fatalf("stacked notification deck overflowed 720x600 viewport: %+v", stackRect)
	}
	if updRect.Y < 0 || updRect.Y+updRect.H > 600.5 || updRect.X < 0 || updRect.X+updRect.W > 720.5 {
		t.Fatalf("long update bubble overflowed 720x600 viewport: %+v", updRect)
	}
	app.BindUpdater(nil)
	app.UpdateNotesOpen = false
	app.clearInfoNotifications()
	app.clearErrorNotifications()

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
	app.clearErrorNotifications()

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
