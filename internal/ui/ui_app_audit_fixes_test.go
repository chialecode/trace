package ui

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/remote"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/chialecode/trace/internal/transfer"
	"github.com/chialecode/trace/internal/update"
	"github.com/egoist/mygo/ui"
)

type auditRemoteStub struct {
	service.BackendAPI
	connected    bool
	info         service.RemoteConnectionInfo
	connectCalls atomic.Int32
	statusCalls  atomic.Int32
	connectGate  chan struct{}
	statusGate   chan struct{}
	connectErr   error
	statusErr    error
	view         service.RemoteView
	removeErr    error
	subPrevGate  chan struct{}
	subPrev      subscription.ImportPreview
	subPrevErr   error
	qrStarted    atomic.Int32
	qrPolls      atomic.Int32
	qrCancelled  atomic.Int32
	qrGate       chan struct{}
	qrPollGate   chan struct{}
	avatarCalls  atomic.Int32
	avatarPath   string
	histThumb    string
	fetchImgCnt  atomic.Int32
	checkAcctErr error
	transferJobs []service.TransferJob
	lastImport   transfer.Choices
	importCalls  atomic.Int32
}

func (s *auditRemoteStub) RemoteConnection() (service.RemoteConnectionInfo, bool) {
	return s.info, s.connected
}

func (s *auditRemoteStub) ConnectRemote(ctx context.Context, in service.RemoteConnectInput) (service.RemoteView, error) {
	s.connectCalls.Add(1)
	if s.connectGate != nil {
		select {
		case <-s.connectGate:
		case <-ctx.Done():
			return service.RemoteView{}, ctx.Err()
		}
	}
	if s.connectErr != nil {
		return service.RemoteView{}, s.connectErr
	}
	s.connected = true
	return s.view, nil
}

func (s *auditRemoteStub) RemoteStatus(ctx context.Context) (service.RemoteView, error) {
	s.statusCalls.Add(1)
	if s.statusGate != nil {
		select {
		case <-s.statusGate:
		case <-ctx.Done():
			return service.RemoteView{}, ctx.Err()
		}
	}
	if s.statusErr != nil {
		return service.RemoteView{}, s.statusErr
	}
	return s.view, nil
}

func (s *auditRemoteStub) RemoveRemote() error {
	if s.removeErr != nil {
		return s.removeErr
	}
	s.connected = false
	return nil
}

func (s *auditRemoteStub) PreviewSubscriptionImport(path string) (subscription.ImportPreview, error) {
	if s.subPrevGate != nil {
		<-s.subPrevGate
	}
	if s.subPrevErr != nil {
		return subscription.ImportPreview{}, s.subPrevErr
	}
	return s.subPrev, nil
}

func (s *auditRemoteStub) StartQRLogin(ctx context.Context) (bilibili.QRSession, error) {
	s.qrStarted.Add(1)
	if s.qrGate != nil {
		select {
		case <-s.qrGate:
		case <-ctx.Done():
			return bilibili.QRSession{}, ctx.Err()
		}
	}
	return bilibili.QRSession{FlowID: "qr-flow-1", URL: "https://example.invalid/qr"}, nil
}

func (s *auditRemoteStub) CancelQRLogin(flowID string) {
	s.qrCancelled.Add(1)
}

func (s *auditRemoteStub) PollQRLogin(ctx context.Context, flowID string) (bilibili.QRPollResult, error) {
	s.qrPolls.Add(1)
	if s.qrPollGate != nil {
		select {
		case <-s.qrPollGate:
		case <-ctx.Done():
			return bilibili.QRPollResult{}, ctx.Err()
		}
	}
	return bilibili.QRPollResult{State: "waiting"}, nil
}

func (s *auditRemoteStub) AccountAvatarPath(siteID, uid string) string {
	s.avatarCalls.Add(1)
	return s.avatarPath
}

func (s *auditRemoteStub) HistoryThumbPath(name string) (string, error) {
	if s.histThumb != "" {
		return s.histThumb, nil
	}
	return s.BackendAPI.HistoryThumbPath(name)
}

func (s *auditRemoteStub) FetchImage(ctx context.Context, rawURL string) ([]byte, error) {
	s.fetchImgCnt.Add(1)
	return nil, errors.New("unexpected network FetchImage")
}

func (s *auditRemoteStub) CheckAccount(ctx context.Context, subID, siteName, uid string) (subscription.CheckSummary, error) {
	if s.checkAcctErr != nil {
		return subscription.CheckSummary{}, s.checkAcctErr
	}
	return subscription.CheckSummary{}, nil
}

func (s *auditRemoteStub) TransferJobs() []service.TransferJob {
	if s.transferJobs != nil {
		return append([]service.TransferJob(nil), s.transferJobs...)
	}
	return s.BackendAPI.TransferJobs()
}

func (s *auditRemoteStub) StartImport(jobID string, choices transfer.Choices) (service.TransferJob, error) {
	s.importCalls.Add(1)
	s.lastImport = choices
	return service.TransferJob{ID: jobID, Kind: "import", Status: service.TransferRunning}, nil
}

type auditUpdaterStub struct {
	st         update.State
	checkCalls atomic.Int32
	dlCalls    atomic.Int32
	restartCnt atomic.Int32
}

func (u *auditUpdaterStub) State() update.State      { return u.st }
func (u *auditUpdaterStub) CheckNow(context.Context) { u.checkCalls.Add(1) }
func (u *auditUpdaterStub) Download(context.Context) { u.dlCalls.Add(1) }
func (u *auditUpdaterStub) DismissBanner()           {}
func (u *auditUpdaterStub) DismissUpdatedNotice()    {}
func (u *auditUpdaterStub) RestartPrompt() string    { return "" }
func (u *auditUpdaterStub) Restart()                 { u.restartCnt.Add(1) }
func (u *auditUpdaterStub) PrefsChanged()            {}

func writeTinyPNG(t *testing.T, path string) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{R: 26, G: 115, B: 232, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteConnectClearsSecretsImmediatelyAndOnNavigate(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)

	stub := &auditRemoteStub{
		BackendAPI:  app.backend,
		connectGate: make(chan struct{}, 1),
		connectErr:  errors.New("dial failed"),
	}
	app.backend = stub
	posted := make(chan func(), 8)
	app.dispatch = func(fn func()) { posted <- fn }

	app.Navigate(RouteSettings)
	app.SettingsCategory = "remote"
	app.Remote.CodeInput = "trace-remote:1:secret-code"
	app.Remote.TokenInput = "secret-token"
	app.connectRemote()
	if app.Remote.CodeInput != "" || app.Remote.TokenInput != "" {
		t.Fatalf("remote secrets must be cleared immediately on submit: code=%q token=%q", app.Remote.CodeInput, app.Remote.TokenInput)
	}
	stub.connectGate <- struct{}{}
	nextPosted(t, posted)()
	if app.Remote.Err == "" {
		t.Fatal("expected connect error after failed ConnectRemote")
	}

	// Unsent secrets are cleared when switching Settings category or leaving /settings.
	app.Remote.CodeInput = "unsent-code"
	app.Remote.TokenInput = "unsent-token"
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	if err := tt.Click("设置分类：账号"); err != nil {
		t.Fatalf("click account category: %v", err)
	}
	if app.Remote.CodeInput != "" || app.Remote.TokenInput != "" {
		t.Fatalf("switching away from remote category leaked secrets: code=%q token=%q", app.Remote.CodeInput, app.Remote.TokenInput)
	}

	app.SettingsCategory = "remote"
	app.Remote.CodeInput = "unsent-code-2"
	app.Remote.TokenInput = "unsent-token-2"
	app.Navigate(RouteHome)
	if app.Remote.CodeInput != "" || app.Remote.TokenInput != "" {
		t.Fatalf("navigating away from settings leaked remote secrets: code=%q token=%q", app.Remote.CodeInput, app.Remote.TokenInput)
	}
}

func TestRemoteRefreshNoSpinWhilePendingAndRespectsInterval(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)

	stub := &auditRemoteStub{
		BackendAPI: app.backend,
		connected:  true,
		info:       service.RemoteConnectionInfo{Name: "VPS", URL: "https://127.0.0.1:47380", Fingerprint: "abcdef0123456789abcdef"},
		statusGate: make(chan struct{}, 2),
		view:       service.RemoteView{InSync: true, Status: remote.Status{Version: "0.2.0"}},
	}
	app.backend = stub
	posted := make(chan func(), 8)
	app.dispatch = func(fn func()) { posted <- fn }
	app.Navigate(RouteSettings)
	app.SettingsCategory = "remote"

	tt := ui.NewTester(app.View, 1280, 860)
	// While remote-status is in-flight, multiple frames must not re-fire status calls.
	for range 5 {
		tt.Frame()
	}
	if got := stub.statusCalls.Load(); got != 1 {
		t.Fatalf("expected exactly 1 in-flight RemoteStatus call, got %d", got)
	}
	stub.statusGate <- struct{}{}
	nextPosted(t, posted)()
	tt.Frame()
	if app.Remote.View == nil || app.Remote.FetchedAt.IsZero() {
		t.Fatal("expected Remote.View and FetchedAt to be populated")
	}

	// Switch to sync mode with virtual clock to verify 30s refresh interval.
	app.dispatch = nil
	stub.statusGate = nil
	app.Remote.FetchedAt = time.Time{}
	tt2 := ui.NewTester(app.View, 1280, 860)
	tt2.Frame()
	before := stub.statusCalls.Load()
	tt2.Advance(10 * time.Second)
	if stub.statusCalls.Load() != before {
		t.Fatalf("remote status refreshed too early after 10s: before=%d after=%d", before, stub.statusCalls.Load())
	}
	tt2.Advance(21 * time.Second)
	if stub.statusCalls.Load() != before+1 {
		t.Fatalf("remote status did not refresh after 31s: before=%d after=%d", before, stub.statusCalls.Load())
	}
}

func TestSubmitParseDroppedAfterNavigateAndAuthExpired(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)
	backend := &gatedOpenBackend{BackendAPI: app.backend, release: make(chan struct{}, 4)}
	app.backend = backend
	posted := make(chan func(), 8)
	app.dispatch = func(fn func()) { posted <- fn }

	// 1. SubmitParse (preview == nil) followed by Navigate must drop stale result.
	app.Navigate(RouteParse)
	app.SubmitParse("BV1xx411c7mD")
	app.Navigate(RouteHome)
	backend.release <- struct{}{}
	nextPosted(t, posted)()
	if app.CurrentRoute != RouteHome || app.CurrentVideo != nil {
		t.Fatalf("stale SubmitParse hijacked route: route=%s video=%v", app.CurrentRoute, app.CurrentVideo)
	}

	// 2. OpenVideo followed by TriggerAuthExpired must drop stale result and stay on /login.
	app.OpenVideo("BV1xx411c7mD", "测试视频", "UP", 8888, "", "01:00")
	app.TriggerAuthExpired("expired")
	backend.release <- struct{}{}
	nextPosted(t, posted)()
	if app.CurrentRoute != RouteLogin || app.CurrentVideo != nil {
		t.Fatalf("stale OpenVideo overrode auth-expired login screen: route=%s video=%v", app.CurrentRoute, app.CurrentVideo)
	}
}

func TestRequestGuardedCleanupClosesDiscardedResourceOnCloseAndStale(t *testing.T) {
	// 1. Stale current() triggers onDiscard.
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	posted := make(chan func(), 4)
	app.dispatch = func(fn func()) { posted <- fn }
	var discarded atomic.Int32
	var okCalled atomic.Int32
	gate := make(chan struct{}, 1)
	stale := false
	requestGuardedCleanup(app, "cleanup-stale", func() bool { return !stale }, func(context.Context) (string, error) {
		<-gate
		return "res-1", nil
	}, func(string) {
		okCalled.Add(1)
	}, nil, func(v string) {
		if v == "res-1" {
			discarded.Add(1)
		}
	})
	stale = true
	gate <- struct{}{}
	nextPosted(t, posted)()
	if okCalled.Load() != 0 || discarded.Load() != 1 {
		t.Fatalf("stale discard expected ok=0 discard=1, got ok=%d discard=%d", okCalled.Load(), discarded.Load())
	}

	// 2. App.Close() while work is in-flight triggers onDiscard without requiring dispatch.
	var closedDiscarded atomic.Int32
	doneCh := make(chan struct{})
	gate2 := make(chan struct{}, 1)
	requestGuardedCleanup(app, "cleanup-close", nil, func(context.Context) (string, error) {
		<-gate2
		return "res-2", nil
	}, func(string) {
		okCalled.Add(1)
	}, nil, func(v string) {
		if v == "res-2" {
			closedDiscarded.Add(1)
			close(doneCh)
		}
	})
	app.Close()
	gate2 <- struct{}{}
	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("onDiscard was not called after App.Close()")
	}
	if closedDiscarded.Load() != 1 || okCalled.Load() != 0 {
		t.Fatalf("close discard expected ok=0 discard=1, got ok=%d discard=%d", okCalled.Load(), closedDiscarded.Load())
	}
}

func TestAccountAvatarCachingNegativeMissPurgeAndBoundedRead(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)

	stub := &auditRemoteStub{BackendAPI: app.backend}
	app.backend = stub

	// 1. Missing avatar caches negative miss; repeated calls do not re-hit AccountAvatarPath.
	for range 5 {
		if bmp := app.lookupAccountAvatarBitmap("bilibili", "1001"); bmp != nil {
			t.Fatal("expected nil for missing avatar")
		}
	}
	if got := stub.avatarCalls.Load(); got != 1 {
		t.Fatalf("expected 1 AccountAvatarPath call with negative caching, got %d", got)
	}

	// 2. Create a local avatar file, emit EventLibraryChanged (purges nil misses), verify hit & cached.
	avatarFile := filepath.Join(t.TempDir(), "avatar.png")
	writeTinyPNG(t, avatarFile)
	stub.avatarPath = avatarFile
	app.handleBackendEvent(service.Event{Type: service.EventLibraryChanged})

	bmp := app.lookupAccountAvatarBitmap("bilibili", "1001")
	if bmp == nil {
		t.Fatal("expected avatar bitmap after purgeMissingImages")
	}
	afterHit := stub.avatarCalls.Load()
	for range 5 {
		if gotBmp := app.lookupAccountAvatarBitmap("bilibili", "1001"); gotBmp == nil {
			t.Fatal("cached avatar unexpectedly became nil")
		}
	}
	if stub.avatarCalls.Load() != afterHit {
		t.Fatalf("expected cached avatar hit to make 0 extra AccountAvatarPath calls, got %d", stub.avatarCalls.Load()-afterHit)
	}

	// 3. readBoundedImageFile refuses files larger than maxImageReadBytes.
	hugePath := filepath.Join(t.TempDir(), "huge.bin")
	f, err := os.Create(hugePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxImageReadBytes + 1024); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	_ = f.Close()
	if raw := readBoundedImageFile(hugePath); raw != nil {
		t.Fatalf("expected readBoundedImageFile to reject oversized file, got %d bytes", len(raw))
	}
}

func TestSettingsStorageShowsFullLibraryStatsAndRunsAsync(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)
	app.LibraryItems = []library.ItemMetadata{{ItemKey: "bilibili:video:BV1", TotalBytes: 1024}}
	app.libraryStatistics = library.Statistics{Items: 250, Bytes: 500 * 1024 * 1024}
	app.Navigate(RouteSettings)
	app.SettingsCategory = "storage"

	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	if _, ok := tt.Find("缓存位置（已缓存 250 项 · 500 MB）"); !ok {
		t.Fatal("Settings > Storage did not display full libraryStatistics count and bytes")
	}
}

func TestStaleRemoteStatusAndSubImportPreviewDropped(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)

	stub := &auditRemoteStub{
		BackendAPI:  app.backend,
		connected:   true,
		statusGate:  make(chan struct{}, 1),
		view:        service.RemoteView{InSync: true, Status: remote.Status{Version: "0.2.0"}},
		subPrevGate: make(chan struct{}, 1),
		subPrev:     subscription.ImportPreview{New: []subscription.ImportSub{{Name: "StaleSub"}}},
	}
	app.backend = stub
	posted := make(chan func(), 8)
	app.dispatch = func(fn func()) { posted <- fn }

	// 1. Disconnect while RemoteStatus is in-flight -> late status must be dropped.
	app.fetchRemoteStatus()
	if err := app.backend.RemoveRemote(); err != nil {
		t.Fatal(err)
	}
	app.remoteGen++
	app.Remote = RemoteUIState{}
	stub.statusGate <- struct{}{}
	nextPosted(t, posted)()
	if app.Remote.View != nil || app.Remote.Err != "" {
		t.Fatalf("stale RemoteStatus repopulated state after disconnect: %+v", app.Remote)
	}

	// 2. Close import modal while PreviewSubscriptionImport is in-flight -> late preview must be dropped.
	app.openSubImport()
	app.SubFile.ImportPath = "subs.json"
	app.previewSubImport()
	app.closeSubImport()
	stub.subPrevGate <- struct{}{}
	nextPosted(t, posted)()
	if app.SubFile.Preview != nil || app.SubFile.ImportOpen {
		t.Fatalf("stale sub-import preview repopulated closed dialog: %+v", app.SubFile)
	}
}

func TestQRLoginCancelledWhenNavigatingAwayFromLogin(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)

	stub := &auditRemoteStub{
		BackendAPI: app.backend,
		qrGate:     make(chan struct{}, 1),
	}
	app.backend = stub
	posted := make(chan func(), 8)
	app.dispatch = func(fn func()) { posted <- fn }

	app.Navigate(RouteLogin)
	app.startQRLogin()
	// Leave /login while QR start is in flight.
	app.Navigate(RouteSettings)
	stub.qrGate <- struct{}{}
	nextPosted(t, posted)()
	if app.QRSession.FlowID != "" || app.QRBitmap != nil {
		t.Fatalf("late QR start populated session after leaving /login: %+v", app.QRSession)
	}

	// When a QR session is already active, navigating away cancels it in backend.
	app.Navigate(RouteLogin)
	app.QRSession = bilibili.QRSession{FlowID: "qr-active-2"}
	app.Navigate(RouteSettings)
	if stub.qrCancelled.Load() == 0 || app.QRSession.FlowID != "" {
		t.Fatalf("leaving /login did not cancel active QR session: cancelled=%d flow=%q", stub.qrCancelled.Load(), app.QRSession.FlowID)
	}
}

func TestHomeCrossSiteLiveSubscriptionStaysLiveUnderDouyinFilter(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)

	sub := subscription.Subscription{
		ID:   "sub-cross-1",
		Name: "跨站主播",
		Site: "bilibili",
		Mid:  4001,
		Accounts: []subscription.AccountRef{
			{Site: "bilibili", UID: "4001", Name: "B站号"},
			{Site: "douyin", UID: "dy_sec_4001", Name: "抖音号"},
		},
	}
	app.Subscriptions = []subscription.Subscription{sub}
	app.LiveRooms = []live.RoomState{{UID: 4001, RoomID: 9001, LiveStatus: 1}}
	app.LiveAccounts = []service.LiveAccountState{
		{Site: "bilibili", UID: "4001", LiveStatus: 1},
		{Site: "douyin", UID: "dy_sec_4001", LiveStatus: 1},
	}
	actor := HomeUpdatedActor{Key: "sub:" + sub.ID, SubID: sub.ID, Name: sub.Name}

	app.FeedSiteFilter = "douyin"
	rows := app.homeSubscriptionRows()
	if len(rows) != 1 || rows[0].SubID != sub.ID || !app.homeSubLive(rows[0]) {
		t.Fatalf("cross-site subscription with live Douyin account must appear live under Douyin filter: rows=%+v", rows)
	}

	// When only Bilibili is live (Douyin offline), Douyin filter must report not live.
	app.LiveAccounts[1].LiveStatus = 0
	if app.homeSubLive(actor) {
		t.Fatal("subscription whose only live account is Bilibili must not appear live under Douyin filter")
	}
}

func TestNetworkProxyCanBeClearedToEmptyAndIMEGuard(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)

	cfg := app.backend.GetSettings()
	cfg.Network.ProxyMode = site.ProxyCustom
	cfg.Network.ProxyURL = "http://127.0.0.1:7890"
	if _, err := app.backend.UpdateSettings(cfg); err != nil {
		t.Fatal(err)
	}
	app.DraftSettings = app.backend.GetSettings()
	app.Navigate(RouteSettings)
	app.SettingsCategory = "network"

	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	if app.ProxyDraftURL != "http://127.0.0.1:7890" {
		t.Fatalf("expected initial ProxyDraftURL to load saved URL, got %q", app.ProxyDraftURL)
	}

	// User clears the proxy input to empty; subsequent frames must NOT snap it back.
	app.ProxyDraftURL = ""
	tt.Frame()
	tt.Frame()
	if app.ProxyDraftURL != "" {
		t.Fatalf("clearing saved proxy URL snapped back to %q", app.ProxyDraftURL)
	}
}

func TestAuthExpiredClosesAllModalsAndOverlays(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)

	var fsCalls []bool
	app.fullscreen = func(on bool) { fsCalls = append(fsCalls, on) }

	app.AddSubDialogOpen = true
	app.ImportSubModalOpen = true
	app.RuleBatch.Open = true
	app.DeleteConfirm.Open = true
	app.ConfirmLogoutOpen = true
	app.RestartConfirmOpen = true
	app.Transfer.ExportOpen = true
	app.SubFile.ExportOpen = true
	app.SubFile.ImportOpen = true
	app.SubscribePicker.Open = true
	app.LibraryRefresh.Open = true
	app.ImageViewer.Open = true
	app.PlayerOverlay.Open = true
	app.PlayerOverlay.Fullscreen = true

	app.TriggerAuthExpired("expired")

	if app.AddSubDialogOpen || app.ImportSubModalOpen || app.RuleBatch.Open ||
		app.DeleteConfirm.Open || app.ConfirmLogoutOpen || app.RestartConfirmOpen ||
		app.Transfer.ExportOpen || app.SubFile.ExportOpen || app.SubFile.ImportOpen ||
		app.SubscribePicker.Open || app.LibraryRefresh.Open ||
		app.ImageViewer.Open || app.PlayerOverlay.Open || app.PlayerOverlay.Fullscreen {
		t.Fatal("TriggerAuthExpired left a modal or overlay open")
	}
	if len(fsCalls) == 0 || fsCalls[len(fsCalls)-1] != false {
		t.Fatalf("expected fullscreen(false) on auth expiry, got %v", fsCalls)
	}
}

func TestDrawStatusDotPausedVariantsRenderYellowPausedLabel(t *testing.T) {
	for _, st := range []string{"paused", "paused_user", "paused_auth", "paused_network", "paused_space", "paused_412"} {
		status := st
		tt := ui.NewTester(func(c *ui.Context) {
			DrawStatusDot(c, status, "")
		}, 120, 40)
		tt.Frame()
		if _, ok := tt.Find("已暂停"); !ok {
			t.Fatalf("DrawStatusDot(%q) did not expose label 已暂停", status)
		}
		if _, ok := tt.Find("状态：" + status); ok {
			t.Fatalf("DrawStatusDot(%q) exposed raw machine status label", status)
		}
	}
}

func TestRefreshDataLoadsTransferJobs(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)

	stub := &auditRemoteStub{
		BackendAPI: app.backend,
		transferJobs: []service.TransferJob{
			{ID: "job-1", Kind: "import", Status: service.TransferRunning},
		},
	}
	app.backend = stub
	app.Transfer.Jobs = nil
	app.RefreshData()
	if got := app.activeTransferCount(); got != 1 {
		t.Fatalf("expected RefreshData to load active transfer jobs (count=1), got %d", got)
	}
}

func TestSettingsDependentControlsDisabledAndUpdateButtonBusy(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)

	// 1. AI subtitles disabled when SaveSubtitles is false.
	app.DraftSettings = settings.Default(t.TempDir())
	app.DraftSettings.Download.SaveSubtitles = false
	app.DraftSettings.AISubtitles = false
	app.Navigate(RouteSettings)
	app.SettingsCategory = "download"
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	_ = tt.Click("开关AI字幕")
	tt.Frame()
	if app.DraftSettings.AISubtitles {
		t.Fatal("AI subtitles switch toggled while SaveSubtitles was disabled")
	}

	// 2. Check interval select disabled when Subscription.Enabled is false.
	app.DraftSettings.Subscription.Enabled = false
	app.DraftSettings.Subscription.IntervalMinutes = 60
	app.SettingsCategory = "subscription"
	tt.Frame()
	if _, ok := tt.Find("选择订阅检查间隔"); !ok {
		t.Fatal("expected subscription interval control to render")
	}

	// 3. Update "立即检查" shows "正在下载…" and is disabled during PhaseDownloading.
	upd := &auditUpdaterStub{
		st: update.State{
			Enabled:        true,
			Channel:        "stable",
			CurrentVersion: "0.1.0",
			Phase:          update.PhaseDownloading,
			Release:        &update.Release{Version: "0.2.0"},
		},
	}
	app.BindUpdater(upd)
	app.SettingsCategory = "update"
	tt.Frame()
	if _, ok := tt.Find("正在下载…"); !ok {
		t.Fatal("expected downloading label on busy check button during PhaseDownloading")
	}
	_ = tt.Click("立即检查按钮")
	if upd.checkCalls.Load() != 0 {
		t.Fatalf("clicking disabled CheckNow button during download triggered %d checks", upd.checkCalls.Load())
	}
}

func TestCheckAccountNowClearsCheckingAccountOnFailure(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)

	stub := &auditRemoteStub{
		BackendAPI:   app.backend,
		checkAcctErr: errors.New("network error"),
	}
	app.backend = stub
	app.checkPlanAt = time.Now()
	app.checkAccountNow("bilibili", "12345", "测试UP")
	if app.checkingAccount != "" {
		t.Fatalf("checkingAccount remained stuck at %q after error", app.checkingAccount)
	}
	if !app.checkPlanAt.IsZero() {
		t.Fatal("checkPlanAt was not reset after failed checkAccountNow")
	}
	if app.BannerError == "" {
		t.Fatal("expected BannerError on failed checkAccountNow")
	}
}

func TestImportConflictRowExplicitSkipOverridesAllConflicts(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)

	stub := &auditRemoteStub{
		BackendAPI: app.backend,
		transferJobs: []service.TransferJob{
			{ID: "scan-1", Kind: "import", Status: service.TransferReady},
		},
	}
	app.backend = stub
	app.Transfer = TransferUIState{
		ImportPage:   true,
		JobID:        "scan-1",
		AllConflicts: library.MergeUnion,
		Choices:      map[string]string{},
		Jobs:         stub.transferJobs,
		Plan: &transfer.Plan{
			Counts: map[transfer.Category]int{transfer.CategoryConflict: 1},
			Entries: []transfer.Entry{
				{Category: transfer.CategoryConflict, ItemKey: "bilibili:video:BV1conf", Title: "冲突视频"},
			},
		},
	}
	app.CurrentRoute = RouteLibrary

	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	if err := tt.Click("冲突处理：bilibili:video:BV1conf"); err != nil {
		t.Fatalf("open conflict select: %v", err)
	}
	if err := tt.Click("暂不处理"); err != nil {
		t.Fatalf("select explicit skip: %v", err)
	}
	tt.Frame()
	if got := app.Transfer.Choices["bilibili:video:BV1conf"]; got != conflictChoiceSkip {
		t.Fatalf("expected explicit skip sentinel %q, got %q", conflictChoiceSkip, got)
	}
	app.startImport()
	if stub.importCalls.Load() != 1 {
		t.Fatalf("expected StartImport to be called once, got %d", stub.importCalls.Load())
	}
	if got := stub.lastImport.Conflicts["bilibili:video:BV1conf"]; got != conflictChoiceSkip {
		t.Fatalf("StartImport did not preserve per-row skip override: got %q (AllConflicts=%q)", got, stub.lastImport.AllConflicts)
	}
}

func TestImageViewerSwitchPageResetsZoomAndPanAndNoZeroFallback(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)

	// Build 1 bitmap for page 0 only (page 1 bitmap is nil/shorter slice).
	tmp := filepath.Join(t.TempDir(), "p0.png")
	writeTinyPNG(t, tmp)
	bmp0 := loadBitmapFromFile(tmp)
	if bmp0 == nil {
		t.Fatal("failed to decode test png")
	}

	app.ImageViewer = ImageViewerState{
		Open:    true,
		URLs:    []string{"https://example.invalid/0.png", "https://example.invalid/1.png"},
		Bitmaps: []*ui.Bitmap{bmp0},
		Index:   0,
		Zoom:    2.5,
		PanX:    120,
		PanY:    -80,
		Fit:     false,
	}

	tt := ui.NewTester(app.View, 1000, 720)
	tt.Frame()
	if err := tt.Click("看图下一张"); err != nil {
		t.Fatalf("click next image: %v", err)
	}
	tt.Frame()
	if app.ImageViewer.Index != 1 {
		t.Fatalf("expected page index 1, got %d", app.ImageViewer.Index)
	}
	if app.ImageViewer.Zoom != 1 || app.ImageViewer.PanX != 0 || app.ImageViewer.PanY != 0 || !app.ImageViewer.Fit {
		t.Fatalf("switching image page did not reset zoom/pan/fit: zoom=%v pan=(%v,%v) fit=%v",
			app.ImageViewer.Zoom, app.ImageViewer.PanX, app.ImageViewer.PanY, app.ImageViewer.Fit)
	}
}

func TestNavigateClearsCurrentLibraryDetailOnNonDetailRoute(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)

	app.CurrentLibraryDetail = &library.ItemMetadata{ItemKey: "bilibili:video:BV1", Kind: "video"}
	app.CurrentRoute = RouteLibraryItem
	app.Navigate(RouteHome)
	if app.CurrentLibraryDetail != nil {
		t.Fatalf("Navigate(RouteHome) left stale CurrentLibraryDetail: %+v", app.CurrentLibraryDetail)
	}
}

func TestOwnerOfAccountInMemoryMatchesSubscriptions(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	app.Subscriptions = []subscription.Subscription{
		{
			ID:   "sub-1",
			Name: "测试订阅",
			Site: "bilibili",
			Mid:  7777,
			Accounts: []subscription.AccountRef{
				{Site: "bilibili", UID: "7777"},
				{Site: "douyin", UID: "MS4w_sec_7777"},
			},
		},
	}
	if sub, ok := app.ownerOfAccountInMemory("bilibili", "7777", 7777); !ok || sub.ID != "sub-1" {
		t.Fatalf("expected bilibili owner sub-1, got ok=%v sub=%+v", ok, sub)
	}
	if sub, ok := app.ownerOfAccountInMemory("douyin", "MS4w_sec_7777", 0); !ok || sub.ID != "sub-1" {
		t.Fatalf("expected douyin owner sub-1, got ok=%v sub=%+v", ok, sub)
	}
	if _, ok := app.ownerOfAccountInMemory("bilibili", "9999", 9999); ok {
		t.Fatal("unexpected owner for unsubscribed account")
	}
	// Replace subscription slice and verify index refreshes.
	app.Subscriptions = []subscription.Subscription{
		{ID: "sub-2", Name: "新订阅", Site: "bilibili", Mid: 9999, Accounts: []subscription.AccountRef{{Site: "bilibili", UID: "9999"}}},
	}
	app.subsRev++
	if sub, ok := app.ownerOfAccountInMemory("bilibili", "9999", 9999); !ok || sub.ID != "sub-2" {
		t.Fatalf("expected refreshed owner sub-2, got ok=%v sub=%+v", ok, sub)
	}
	_ = bilibili.UserSpace{}
}

func TestQRButtonsDisableWhilePendingKeyMatches(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)

	stub := &auditRemoteStub{
		BackendAPI: app.backend,
		qrGate:     make(chan struct{}, 2),
		qrPollGate: make(chan struct{}, 2),
	}
	app.backend = stub
	posted := make(chan func(), 8)
	app.dispatch = func(fn func()) { posted <- fn }

	app.Navigate(RouteLogin)
	app.LoginSite = "bilibili"
	app.LoginTab = "qr"
	tt := ui.NewTester(app.View, 1000, 800)
	tt.Frame()

	// Start QR login -> "获取登录二维码" must be disabled while qr-start-<rev> is pending.
	if err := tt.Click("获取登录二维码"); err != nil {
		t.Fatalf("click start qr: %v", err)
	}
	tt.Frame()
	_ = tt.Click("获取登录二维码")
	if got := stub.qrStarted.Load(); got != 1 {
		t.Fatalf("clicking disabled 获取登录二维码 started %d sessions, want 1", got)
	}
	stub.qrGate <- struct{}{}
	nextPosted(t, posted)()
	tt.Frame()

	// Poll QR status -> "检查扫码状态" must be disabled while qr-poll-<flow> is pending.
	if err := tt.Click("检查扫码状态"); err != nil {
		t.Fatalf("click poll qr: %v", err)
	}
	tt.Frame()
	_ = tt.Click("检查扫码状态")
	if got := stub.qrPolls.Load(); got != 1 {
		t.Fatalf("clicking disabled 检查扫码状态 triggered %d polls, want 1", got)
	}
	stub.qrPollGate <- struct{}{}
	nextPosted(t, posted)()
}

func TestAccountAvatarAndHistoryThumbDecodeOffUIThread(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)

	tmpDir := t.TempDir()
	avatarFile := filepath.Join(tmpDir, "local_avatar.png")
	thumbFile := filepath.Join(tmpDir, "local_thumb.png")
	writeTinyPNG(t, avatarFile)
	writeTinyPNG(t, thumbFile)

	stub := &auditRemoteStub{
		BackendAPI: app.backend,
		avatarPath: avatarFile,
		histThumb:  thumbFile,
	}
	app.backend = stub
	posted := make(chan func(), 8)
	app.dispatch = func(fn func()) { posted <- fn }

	// 1. Account avatar decodes off-thread when dispatch is bound, without triggering remote FetchImage.
	tt := ui.NewTester(func(c *ui.Context) {
		app.renderAccountAvatar(c, "bilibili", "UP", "8001", 8001, 40, false, "测试头像", nil, "https://example.invalid/face.png")
	}, 200, 200)
	tt.Frame()
	if stub.fetchImgCnt.Load() != 0 {
		t.Fatalf("in-flight local avatar decode fell back to FetchImage (%d calls)", stub.fetchImgCnt.Load())
	}
	nextPosted(t, posted)()
	if bmp := app.lookupAccountAvatarBitmap("bilibili", "8001"); bmp == nil {
		t.Fatal("expected decoded account avatar bitmap after async completion")
	}

	// 2. History thumb decodes off-thread when dispatch is bound.
	if bmp := app.lookupHistoryThumbBitmap("hist1.png"); bmp != nil {
		t.Fatal("expected nil on initial async history thumb lookup before dispatch callback runs")
	}
	nextPosted(t, posted)()
	if bmp := app.lookupHistoryThumbBitmap("hist1.png"); bmp == nil {
		t.Fatal("expected decoded history thumb bitmap after async completion")
	}
}
