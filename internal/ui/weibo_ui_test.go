package ui

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/remote"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/storage"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

type weiboStubBackend struct {
	service.BackendAPI
	raw *service.Backend

	mu              sync.Mutex
	dyStatus        credential.Status
	wbStatus        credential.Status
	loginGate       chan struct{}
	loginMode       string // "ok" | "missing_sub" | "unauth" | "risk"
	wbQRStartPNG    []byte
	wbQRStartErr    error
	wbQRPollResults []service.WeiboQRPollResultDTO
	wbQRPollErr     error
	wbQRPollIdx     int
	wbQRCancelled   atomic.Int32
	biliCalls       atomic.Int32
	dyCalls         atomic.Int32
	wbCalls         atomic.Int32
	liveCalls       atomic.Int32
	cachedMid       string
	cachedOID       string
	queuedReqs      []service.CacheQueueRequest

	wbUsers        map[string]*service.WeiboUserView
	wbUserPages    map[string]map[string]weiboPageFixture
	wbPosts        map[string]*service.WeiboPostView
	wbVideos       map[string]*service.WeiboVideoView
	accountWorks   map[string]service.AccountWorks
	checkPlanRows  []service.AccountCheckRow
	followGroups   map[string][]service.FollowingGroupDTO
	followPages    map[string]map[int]service.FollowingCreatorsPageDTO
	mineSections   map[string][]service.AccountSection
	mineFolders    map[string][]service.AccountFolder
	minePages      map[string]service.AccountPage
	importedSite   string
	importedCount  int
	remoteView     *service.RemoteView
	remoteConnInfo *service.RemoteConnectionInfo
}

type weiboPageFixture struct {
	page service.WeiboUserPostsPage
	err  error
}

func newWeiboUITestHarness(t *testing.T) (*App, *weiboStubBackend, func()) {
	t.Helper()
	app, _, cleanup := newTestUIApp(t)
	raw := app.backend.(*service.Backend)
	stub := &weiboStubBackend{
		BackendAPI:   raw,
		raw:          raw,
		dyStatus:     credential.Status{Site: string(site.Douyin), State: "logged_out"},
		wbStatus:     credential.Status{Site: string(site.Weibo), State: "logged_out"},
		loginMode:    "ok",
		wbUsers:      make(map[string]*service.WeiboUserView),
		wbUserPages:  map[string]map[string]weiboPageFixture{},
		wbPosts:      make(map[string]*service.WeiboPostView),
		wbVideos:     make(map[string]*service.WeiboVideoView),
		followGroups: map[string][]service.FollowingGroupDTO{},
		followPages:  map[string]map[int]service.FollowingCreatorsPageDTO{},
		mineSections: map[string][]service.AccountSection{},
		mineFolders:  map[string][]service.AccountFolder{},
		minePages:    map[string]service.AccountPage{},
	}
	app.backend = stub
	return app, stub, cleanup
}

func (s *weiboStubBackend) Statuses() map[string]credential.Status {
	s.mu.Lock()
	dy, wb := s.dyStatus, s.wbStatus
	s.mu.Unlock()
	return map[string]credential.Status{
		string(site.Bilibili): s.BackendAPI.StatusOf(string(site.Bilibili)),
		string(site.Douyin):   dy,
		string(site.Weibo):    wb,
	}
}

func (s *weiboStubBackend) StatusOf(siteID string) credential.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch strings.ToLower(strings.TrimSpace(siteID)) {
	case string(site.Douyin):
		return s.dyStatus
	case string(site.Weibo):
		return s.wbStatus
	default:
		return s.BackendAPI.StatusOf(siteID)
	}
}

func (s *weiboStubBackend) HasAnyLogin() bool {
	if s.BackendAPI.StatusOf(string(site.Bilibili)).LoggedIn {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dyStatus.LoggedIn || s.wbStatus.LoggedIn
}

func (s *weiboStubBackend) LoginWithCookie(ctx context.Context, siteID, rawCookie, rawRefreshToken string) (credential.Status, error) {
	if !strings.EqualFold(strings.TrimSpace(siteID), string(site.Weibo)) {
		return s.BackendAPI.LoginWithCookie(ctx, siteID, rawCookie, rawRefreshToken)
	}
	s.mu.Lock()
	gate := s.loginGate
	mode := s.loginMode
	s.mu.Unlock()

	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return credential.Status{}, ctx.Err()
		}
	}

	switch mode {
	case "missing_sub":
		return credential.Status{}, &site.Error{Site: site.Weibo, Kind: site.ErrKindInvalidInput, Message: "未检测到 SUB"}
	case "unauth":
		s.wbCalls.Add(1)
		return credential.Status{}, &site.Error{Site: site.Weibo, Kind: site.ErrKindAuthRequired, Message: "微博登录态无效"}
	case "risk":
		s.wbCalls.Add(1)
		return credential.Status{}, &site.Error{Site: site.Weibo, Kind: site.ErrKindRiskControl, Message: "微博触发风控验证"}
	default:
		s.wbCalls.Add(1)
		st := credential.Status{
			Site:     string(site.Weibo),
			LoggedIn: true,
			State:    "logged_in",
			Source:   "manual",
			UID:      "500000100",
			Uname:    "微博合成测试号",
		}
		s.mu.Lock()
		s.wbStatus = st
		s.mu.Unlock()
		return st, nil
	}
}

func (s *weiboStubBackend) LogoutSite(siteID string) (credential.Status, error) {
	switch strings.ToLower(strings.TrimSpace(siteID)) {
	case string(site.Weibo):
		s.mu.Lock()
		s.wbStatus = credential.Status{Site: string(site.Weibo), State: "logged_out"}
		st := s.wbStatus
		s.mu.Unlock()
		return st, nil
	case string(site.Douyin):
		s.mu.Lock()
		s.dyStatus = credential.Status{Site: string(site.Douyin), State: "logged_out"}
		st := s.dyStatus
		s.mu.Unlock()
		return st, nil
	}
	return s.BackendAPI.LogoutSite(siteID)
}

func (s *weiboStubBackend) StartWeiboQRLogin(_ context.Context) (service.WeiboQRSessionDTO, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wbQRStartErr != nil {
		return service.WeiboQRSessionDTO{}, s.wbQRStartErr
	}
	pngBytes := s.wbQRStartPNG
	if len(pngBytes) == 0 {
		img := image.NewRGBA(image.Rect(0, 0, 32, 32))
		for y := 0; y < 32; y++ {
			for x := 0; x < 32; x++ {
				img.Set(x, y, color.Black)
			}
		}
		var buf bytes.Buffer
		_ = png.Encode(&buf, img)
		pngBytes = buf.Bytes()
	}
	return service.WeiboQRSessionDTO{
		FlowID:    "wbqr-ui-test-1",
		PNG:       pngBytes,
		ExpiresAt: time.Now().Add(180 * time.Second),
	}, nil
}

func (s *weiboStubBackend) PollWeiboQRLogin(_ context.Context, flowID string) (service.WeiboQRPollResultDTO, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wbQRPollErr != nil {
		return service.WeiboQRPollResultDTO{}, s.wbQRPollErr
	}
	if s.wbQRPollIdx < len(s.wbQRPollResults) {
		res := s.wbQRPollResults[s.wbQRPollIdx]
		s.wbQRPollIdx++
		if res.FlowID == "" {
			res.FlowID = flowID
		}
		if res.State == "confirmed" {
			if !res.Account.LoggedIn {
				res.Account = credential.Status{
					Site:     string(site.Weibo),
					LoggedIn: true,
					State:    "logged_in",
					Source:   "qr",
					UID:      "500000188",
					Uname:    "微博扫码测试号",
				}
			}
			s.wbStatus = res.Account
		}
		return res, nil
	}
	return service.WeiboQRPollResultDTO{
		FlowID:  flowID,
		State:   "waiting",
		Message: "请使用微博 App 扫码登录",
	}, nil
}

func (s *weiboStubBackend) CancelWeiboQRLogin(_ string) {
	s.wbQRCancelled.Add(1)
}

func (s *weiboStubBackend) OpenLink(ctx context.Context, rawInput string) (*service.OpenLinkResult, error) {
	trimmed := strings.TrimSpace(rawInput)
	switch {
	case strings.Contains(trimmed, "weibo.com/l/wblive"):
		return nil, &site.Error{Site: site.Weibo, Kind: site.ErrKindUnsupportedLink, Unsupported: true, Message: "微博直播暂不支持录制，可以在浏览器中打开"}
	case strings.Contains(trimmed, "weibo.com"):
		if !s.StatusOf(string(site.Weibo)).LoggedIn {
			return nil, &site.Error{Site: site.Weibo, Kind: site.ErrKindAuthRequired, Message: "请先登录微博"}
		}
		s.wbCalls.Add(1)
		s.mu.Lock()
		defer s.mu.Unlock()
		if strings.Contains(trimmed, "/u/") {
			uid := strings.TrimPrefix(trimmed[strings.Index(trimmed, "/u/"):], "/u/")
			u := s.wbUsers[uid]
			var page *service.WeiboUserPostsPage
			var postsErr string
			if pf, ok := s.wbUserPages[uid][""]; ok {
				if pf.err != nil {
					postsErr = pf.err.Error()
				} else {
					cp := pf.page
					page = &cp
				}
			}
			return &service.OpenLinkResult{
				Site:                string(site.Weibo),
				SiteTarget:          &site.ResolveTarget{Site: site.Weibo, Kind: "user", ID: uid, CanonicalURL: trimmed},
				WeiboUser:           u,
				WeiboUserPosts:      page,
				WeiboUserPostsError: postsErr,
			}, nil
		}
		if strings.Contains(trimmed, "/tv/show/") {
			oid := strings.TrimPrefix(trimmed[strings.Index(trimmed, "/tv/show/"):], "/tv/show/")
			return &service.OpenLinkResult{
				Site:       string(site.Weibo),
				SiteTarget: &site.ResolveTarget{Site: site.Weibo, Kind: "video", ID: oid, CanonicalURL: trimmed},
				WeiboVideo: s.wbVideos[oid],
			}, nil
		}
		for mid, p := range s.wbPosts {
			if strings.Contains(trimmed, mid) {
				cp := *p
				return &service.OpenLinkResult{
					Site:       string(site.Weibo),
					SiteTarget: &site.ResolveTarget{Site: site.Weibo, Kind: "dynamic", ID: mid, CanonicalURL: cp.CanonicalURL},
					WeiboPost:  &cp,
				}, nil
			}
		}
		return nil, fmt.Errorf("unknown weibo stub link %q", rawInput)
	case strings.Contains(trimmed, "douyin.com"):
		if !s.StatusOf(string(site.Douyin)).LoggedIn {
			return nil, &site.Error{Site: site.Douyin, Kind: site.ErrKindAuthRequired, Message: "请先登录抖音"}
		}
		s.dyCalls.Add(1)
		return nil, fmt.Errorf("unexpected douyin link in weibo stub")
	case strings.Contains(trimmed, "bilibili.com") || strings.HasPrefix(trimmed, "BV"):
		if !s.StatusOf(string(site.Bilibili)).LoggedIn {
			return nil, &site.Error{Site: site.Bilibili, Kind: site.ErrKindAuthRequired, Message: "请先登录哔哩哔哩"}
		}
		s.biliCalls.Add(1)
		return s.BackendAPI.OpenLink(ctx, rawInput)
	}
	return s.BackendAPI.OpenLink(ctx, rawInput)
}

func (s *weiboStubBackend) WeiboUserPosts(_ context.Context, uid, cursor string) (*service.WeiboUserPostsPage, error) {
	s.wbCalls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if pf, ok := s.wbUserPages[uid][cursor]; ok {
		if pf.err != nil {
			return nil, pf.err
		}
		cp := pf.page
		return &cp, nil
	}
	return &service.WeiboUserPostsPage{}, nil
}

func (s *weiboStubBackend) CacheWeiboPost(_ context.Context, mid string, _ *library.ItemVia) (download.Task, error) {
	s.mu.Lock()
	s.cachedMid = mid
	s.mu.Unlock()
	return download.Task{TaskID: "wb-task-" + mid, Site: "weibo", Kind: "dynamic", TargetID: mid, Status: "queued"}, nil
}

func (s *weiboStubBackend) CacheWeiboVideo(_ context.Context, oid string) (download.Task, error) {
	s.mu.Lock()
	s.cachedOID = oid
	s.mu.Unlock()
	return download.Task{TaskID: "wb-vid-" + oid, Site: "weibo", Kind: "video", TargetID: oid, Status: "queued"}, nil
}

func (s *weiboStubBackend) EnqueueCacheQueue(_ context.Context, req service.CacheQueueRequest) (service.CacheQueueTask, error) {
	s.mu.Lock()
	s.queuedReqs = append(s.queuedReqs, req)
	s.mu.Unlock()
	return service.CacheQueueTask{ID: "cq-" + req.Site + "-" + req.User, Site: req.Site, User: req.User, List: req.List, Status: service.CacheQueueQueued}, nil
}

func (s *weiboStubBackend) SubscriptionAccountWorks() map[string]service.AccountWorks {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.accountWorks) > 0 {
		out := make(map[string]service.AccountWorks, len(s.accountWorks))
		for k, v := range s.accountWorks {
			out[k] = v
		}
		return out
	}
	return s.BackendAPI.SubscriptionAccountWorks()
}

func (s *weiboStubBackend) CheckPlan() []service.AccountCheckRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.checkPlanRows) > 0 {
		return append([]service.AccountCheckRow(nil), s.checkPlanRows...)
	}
	return s.BackendAPI.CheckPlan()
}

func (s *weiboStubBackend) LiveStatuses(_ context.Context, _ []string) ([]service.LiveAccountState, error) {
	s.liveCalls.Add(1)
	return nil, nil
}

func (s *weiboStubBackend) FollowingGroups(_ context.Context, siteID string) ([]service.FollowingGroupDTO, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]service.FollowingGroupDTO(nil), s.followGroups[siteID]...), nil
}

func (s *weiboStubBackend) FollowingCreatorsPage(_ context.Context, siteID, _ string, page int) (service.FollowingCreatorsPageDTO, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pages, ok := s.followPages[siteID]; ok {
		if p, ok2 := pages[page]; ok2 {
			return p, nil
		}
	}
	return service.FollowingCreatorsPageDTO{Site: siteID, Page: page}, nil
}

func (s *weiboStubBackend) ImportFollowingCreators(siteID string, creators []service.FollowingCreatorDTO, rules subscription.SubRules) (subscription.ImportAccountRefsResult, error) {
	s.mu.Lock()
	s.importedSite = siteID
	s.importedCount = len(creators)
	s.mu.Unlock()
	return s.BackendAPI.ImportFollowingCreators(siteID, creators, rules)
}

func (s *weiboStubBackend) AccountSections(siteID string) []service.AccountSection {
	s.mu.Lock()
	defer s.mu.Unlock()
	if secs, ok := s.mineSections[siteID]; ok {
		return append([]service.AccountSection(nil), secs...)
	}
	return s.BackendAPI.AccountSections(siteID)
}

func (s *weiboStubBackend) AccountFolders(_ context.Context, siteID, kind string) ([]service.AccountFolder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if fs, ok := s.mineFolders[siteID+"/"+kind]; ok {
		return append([]service.AccountFolder(nil), fs...), nil
	}
	return nil, nil
}

func (s *weiboStubBackend) AccountPage(_ context.Context, siteID, kind, _, _ string) (service.AccountPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pg, ok := s.minePages[siteID+"/"+kind]; ok {
		return pg, nil
	}
	return service.AccountPage{}, nil
}

func (s *weiboStubBackend) RemoteConnection() (service.RemoteConnectionInfo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.remoteConnInfo != nil {
		return *s.remoteConnInfo, true
	}
	return s.BackendAPI.RemoteConnection()
}

func (s *weiboStubBackend) RemoteStatus(context.Context) (service.RemoteView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.remoteView != nil {
		return *s.remoteView, nil
	}
	return s.BackendAPI.RemoteStatus(context.Background())
}

func TestWeiboLoginFormErrorsAndSingleSiteEntry(t *testing.T) {
	app, stub, cleanup := newWeiboUITestHarness(t)
	defer cleanup()

	posted := make(chan func(), 32)
	app.dispatch = func(fn func()) { posted <- fn }

	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()

	for _, lbl := range []string{"登录站点：哔哩哔哩", "登录站点：抖音", "登录站点：微博"} {
		if _, ok := tt.Find(lbl); !ok {
			t.Fatalf("expected 3-site login tab %q", lbl)
		}
	}
	if err := tt.Click("登录站点：微博"); err != nil {
		t.Fatalf("switch to Weibo login tab: %v", err)
	}
	tt.Frame()
	if app.LoginSite != "weibo" {
		t.Fatalf("LoginSite = %q, want weibo", app.LoginSite)
	}
	if _, ok := tt.Find("微博登录标签：扫码"); !ok {
		t.Fatal("expected Weibo QR login tab")
	}
	if err := tt.Click("微博登录标签：粘贴Cookie"); err != nil {
		t.Fatalf("switch to Weibo Cookie login tab: %v", err)
	}
	tt.Frame()

	// Input clears immediately on submit even while validation is still in flight.
	gate := make(chan struct{})
	stub.mu.Lock()
	stub.loginGate = gate
	stub.loginMode = "missing_sub"
	stub.mu.Unlock()

	app.WeiboCookieInput = "XSRF-TOKEN=synth_xsrf_only"
	tt.Frame()
	if err := tt.Click("验证并登录微博"); err != nil {
		t.Fatalf("click submit Weibo cookie: %v", err)
	}
	if app.WeiboCookieInput != "" {
		t.Fatalf("expected WeiboCookieInput cleared immediately on submit, got %q", app.WeiboCookieInput)
	}
	close(gate)
	nextPosted(t, posted)()
	tt.Frame()
	if !strings.Contains(app.LoginError, "SUB") {
		t.Fatalf("expected missing SUB error, got %q", app.LoginError)
	}

	// Unlogged/guest Cookie error.
	stub.mu.Lock()
	stub.loginGate = nil
	stub.loginMode = "unauth"
	stub.mu.Unlock()
	app.WeiboCookieInput = "SUB=synth_guest_sub"
	tt.Frame()
	_ = tt.Click("验证并登录微博")
	nextPosted(t, posted)()
	tt.Frame()
	if !strings.Contains(app.LoginError, "访客") && !strings.Contains(app.LoginError, "过期") {
		t.Fatalf("expected distinct unlogged/guest error, got %q", app.LoginError)
	}

	// Risk-control error.
	stub.mu.Lock()
	stub.loginMode = "risk"
	stub.mu.Unlock()
	app.WeiboCookieInput = "SUB=synth_risk_sub"
	tt.Frame()
	_ = tt.Click("验证并登录微博")
	nextPosted(t, posted)()
	tt.Frame()
	if !strings.Contains(app.LoginError, "验证") && !strings.Contains(app.LoginError, "风控") {
		t.Fatalf("expected distinct risk-control error, got %q", app.LoginError)
	}

	// Successful Weibo-only login enters the app.
	stub.mu.Lock()
	stub.loginMode = "ok"
	stub.mu.Unlock()
	app.WeiboCookieInput = "SUB=synth_valid_sub; SUBP=synth_subp"
	tt.Frame()
	_ = tt.Click("验证并登录微博")
	nextPosted(t, posted)()
	tt.Frame()
	if app.CurrentRoute == RouteLogin {
		t.Fatalf("expected entering app with Weibo-only login, still on %s", app.CurrentRoute)
	}

	// While only Weibo is logged in, pasting Bilibili or Douyin link makes 0 network calls.
	stub.biliCalls.Store(0)
	stub.dyCalls.Store(0)
	app.Navigate(RouteParse)
	app.ParsePageInput = "https://www.bilibili.com/video/BV1xx411c7mD"
	tt.Frame()
	_ = tt.Click("立即解析")
	nextPosted(t, posted)()
	tt.Frame()
	if stub.biliCalls.Load() != 0 {
		t.Fatalf("expected 0 Bilibili network calls when logged out, got %d", stub.biliCalls.Load())
	}
	if !strings.Contains(app.LastParseError, "请先登录哔哩哔哩") {
		t.Fatalf("expected Bilibili login prompt, got %q", app.LastParseError)
	}

	app.ParsePageInput = "https://www.douyin.com/video/7300000000000000001"
	tt.Frame()
	_ = tt.Click("立即解析")
	nextPosted(t, posted)()
	tt.Frame()
	if stub.dyCalls.Load() != 0 {
		t.Fatalf("expected 0 Douyin network calls when logged out, got %d", stub.dyCalls.Load())
	}
	if !strings.Contains(app.LastParseError, "请先登录抖音") {
		t.Fatalf("expected Douyin login prompt, got %q", app.LastParseError)
	}

	// Log out Weibo, log in Bilibili only -> pasting Weibo link shows "请先登录微博" with 0 Weibo calls.
	_, _ = stub.LogoutSite("weibo")
	if _, err := stub.raw.ImportCookie(context.Background(), "SESSDATA=synth_bili; bili_jct=csrf; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()
	waitUIIdle(t, app, posted)
	stub.wbCalls.Store(0)

	app.Navigate(RouteParse)
	app.ParsePageInput = "https://weibo.com/500000001/5000000000000001"
	tt.Frame()
	_ = tt.Click("立即解析")
	nextPosted(t, posted)()
	tt.Frame()
	if stub.wbCalls.Load() != 0 {
		t.Fatalf("expected 0 Weibo network calls when logged out, got %d", stub.wbCalls.Load())
	}
	if app.LastParseError != "请先登录微博" {
		t.Fatalf("LastParseError = %q, want 请先登录微博", app.LastParseError)
	}
}

func TestWeiboQRLoginFlowTabsCancelAndSettings(t *testing.T) {
	app, stub, cleanup := newWeiboUITestHarness(t)
	defer cleanup()

	tt := ui.NewTester(app.View, 1280, 860)
	app.LoginSite = "weibo"
	app.WeiboLoginTab = "qr"
	app.Navigate(RouteLogin)
	tt.Frame()

	for _, lbl := range []string{"微博登录标签：扫码", "微博登录标签：粘贴Cookie", "获取微博登录二维码"} {
		if _, ok := tt.Find(lbl); !ok {
			t.Fatalf("expected Weibo QR login control %q", lbl)
		}
	}

	// 1. Start QR -> renders bitmap -> poll waiting -> scanned -> expired -> refresh -> cancel on tab switch.
	stub.mu.Lock()
	stub.wbQRPollResults = []service.WeiboQRPollResultDTO{
		{State: "waiting", Message: "请使用微博 App 扫码登录"},
		{State: "scanned", Message: "已扫码，请在微博 App 确认登录"},
		{State: "expired", Message: "二维码已过期，请刷新二维码"},
	}
	stub.mu.Unlock()

	if err := tt.Click("获取微博登录二维码"); err != nil {
		t.Fatalf("click start Weibo QR: %v", err)
	}
	tt.Frame()
	if app.WeiboQRBitmap == nil || app.WeiboQRSession.FlowID == "" {
		t.Fatal("expected WeiboQRBitmap and active FlowID")
	}
	if _, ok := tt.Find("微博登录二维码"); !ok {
		t.Fatal("expected rendered 微博登录二维码 widget")
	}

	if err := tt.Click("检查微博扫码状态"); err != nil {
		t.Fatalf("poll waiting: %v", err)
	}
	tt.Frame()
	if app.WeiboQRState != "waiting" {
		t.Fatalf("WeiboQRState = %q, want waiting", app.WeiboQRState)
	}

	if err := tt.Click("检查微博扫码状态"); err != nil {
		t.Fatalf("poll scanned: %v", err)
	}
	tt.Frame()
	if app.WeiboQRState != "scanned" || !tt.HasText("已扫码，请在微博 App 确认登录") {
		t.Fatalf("expected scanned status, got state=%q text=%q", app.WeiboQRState, app.WeiboQRStatusText)
	}

	if err := tt.Click("检查微博扫码状态"); err != nil {
		t.Fatalf("poll expired: %v", err)
	}
	tt.Frame()
	if app.WeiboQRState != "expired" || app.WeiboQRSession.FlowID != "" || !tt.HasText("刷新二维码") {
		t.Fatalf("expected expired state with refresh button, got state=%q flow=%q", app.WeiboQRState, app.WeiboQRSession.FlowID)
	}

	// 2. Refresh QR and verify switching to Cookie tab cancels the active QR session
	// and leaves NO stale QR status text when returning to the QR tab.
	if err := tt.Click("获取微博登录二维码"); err != nil {
		t.Fatalf("refresh Weibo QR: %v", err)
	}
	tt.Frame()
	beforeCancel := stub.wbQRCancelled.Load()
	if err := tt.Click("微博登录标签：粘贴Cookie"); err != nil {
		t.Fatalf("switch to Cookie tab: %v", err)
	}
	tt.Frame()
	if stub.wbQRCancelled.Load() <= beforeCancel || app.WeiboQRSession.FlowID != "" || app.WeiboQRBitmap != nil || app.WeiboQRState != "" || app.WeiboQRStatusText != "" {
		t.Fatalf("switching from QR tab to Cookie tab must cancel flow and clear QR status, got state=%q text=%q", app.WeiboQRState, app.WeiboQRStatusText)
	}

	// 3. Returning to QR tab shows clean idle state (no stale status); risk-control and blocked errors both stop polling.
	if err := tt.Click("微博登录标签：扫码"); err != nil {
		t.Fatalf("switch back to QR tab: %v", err)
	}
	tt.Frame()
	if tt.HasText("扫码状态：") {
		t.Fatalf("returning to QR tab after tab-switch cancel must not show stale QR status, got state=%q text=%q", app.WeiboQRState, app.WeiboQRStatusText)
	}
	stub.mu.Lock()
	stub.wbQRPollErr = &site.Error{Site: site.Weibo, Kind: site.ErrKindRiskControl, Message: "微博扫码触发风控"}
	stub.mu.Unlock()
	_ = tt.Click("获取微博登录二维码")
	tt.Frame()
	_ = tt.Click("检查微博扫码状态")
	tt.Frame()
	if app.WeiboQRSession.FlowID != "" || !strings.Contains(app.LoginError, "风控") {
		t.Fatalf("risk control must cancel active flow and display error: flow=%q err=%q", app.WeiboQRSession.FlowID, app.LoginError)
	}

	stub.mu.Lock()
	stub.wbQRPollErr = &site.Error{Site: site.Weibo, Kind: site.ErrKindBlocked, Message: "已拦截微博扫码请求指向内网或回环地址"}
	stub.mu.Unlock()
	_ = tt.Click("获取微博登录二维码")
	tt.Frame()
	beforeBlockedCancel := stub.wbQRCancelled.Load()
	_ = tt.Click("检查微博扫码状态")
	tt.Frame()
	if app.WeiboQRSession.FlowID != "" || stub.wbQRCancelled.Load() <= beforeBlockedCancel || app.WeiboQRState != "error" || !strings.Contains(app.LoginError, "内网或回环") {
		t.Fatalf("ErrKindBlocked must stop active QR flow and surface error: flow=%q state=%q err=%q", app.WeiboQRSession.FlowID, app.WeiboQRState, app.LoginError)
	}

	// 4. Confirmed QR login enters the app and Settings › Account displays QR source + both re-login buttons.
	stub.mu.Lock()
	stub.wbQRPollErr = nil
	stub.wbQRPollIdx = 0
	stub.wbQRPollResults = []service.WeiboQRPollResultDTO{
		{
			State:   "confirmed",
			Message: "扫码登录成功",
			Account: credential.Status{
				Site:     string(site.Weibo),
				LoggedIn: true,
				State:    "logged_in",
				Source:   "qr",
				UID:      "500000188",
				Uname:    "微博扫码测试号",
			},
		},
	}
	stub.mu.Unlock()
	_ = tt.Click("获取微博登录二维码")
	tt.Frame()
	_ = tt.Click("检查微博扫码状态")
	tt.Frame()
	if app.CurrentRoute == RouteLogin {
		t.Fatalf("expected leaving /login after confirmed Weibo QR login, still on %s", app.CurrentRoute)
	}

	app.SettingsCategory = "account"
	app.Navigate(RouteSettings)
	tt.Frame()
	if !tt.HasText("登录方式：扫码登录") || !tt.HasText("微博扫码登录暂不支持自动续期") {
		t.Fatal("expected Settings › Account Weibo section to show QR login source and non-renewable notice")
	}
	for _, lbl := range []string{"扫码重新登录微博", "重新粘贴微博Cookie", "退出微博登录"} {
		if _, ok := tt.Find(lbl); !ok {
			t.Fatalf("expected Settings › Account Weibo action %q", lbl)
		}
	}

	// Clicking "扫码重新登录微博" opens /login on weibo QR tab; "重新粘贴微博Cookie" opens cookie tab.
	if err := tt.Click("扫码重新登录微博"); err != nil {
		t.Fatalf("click 扫码重新登录微博: %v", err)
	}
	tt.Frame()
	if app.CurrentRoute != RouteLogin || app.LoginSite != "weibo" || app.WeiboLoginTab != "qr" {
		t.Fatalf("expected /login weibo qr tab, got route=%s site=%s tab=%s", app.CurrentRoute, app.LoginSite, app.WeiboLoginTab)
	}

	// Navigating away from /login cancels an active Weibo QR flow and clears stale status.
	_ = tt.Click("获取微博登录二维码")
	tt.Frame()
	beforeNavCancel := stub.wbQRCancelled.Load()
	app.Navigate(RouteSettings)
	tt.Frame()
	if stub.wbQRCancelled.Load() <= beforeNavCancel || app.WeiboQRSession.FlowID != "" || app.WeiboQRState != "" || app.WeiboQRStatusText != "" {
		t.Fatalf("leaving /login did not cancel active Weibo QR session and clear status: flow=%q state=%q text=%q", app.WeiboQRSession.FlowID, app.WeiboQRState, app.WeiboQRStatusText)
	}
	// Re-entering /login → weibo QR tab renders no stale status banner.
	if err := tt.Click("扫码重新登录微博"); err != nil {
		t.Fatalf("re-open /login weibo qr tab: %v", err)
	}
	tt.Frame()
	if tt.HasText("扫码状态：") {
		t.Fatalf("returning to /login after route-leave cancel must not show stale status, got state=%q text=%q", app.WeiboQRState, app.WeiboQRStatusText)
	}
	app.Navigate(RouteSettings)
	tt.Frame()

	// Clicking "重新粘贴微博Cookie" opens the inline Cookie paste panel right inside Settings › Account (#195).
	if err := tt.Click("重新粘贴微博Cookie"); err != nil {
		t.Fatalf("click 重新粘贴微博Cookie: %v", err)
	}
	tt.Frame()
	if app.CurrentRoute != RouteSettings || app.SettingsPasteSite != "weibo" {
		t.Fatalf("expected inline paste panel on Settings › Account for weibo, got route=%s pasteSite=%q", app.CurrentRoute, app.SettingsPasteSite)
	}
	if _, ok := tt.Find("设置内联粘贴区：weibo"); !ok {
		t.Fatal("expected 设置内联粘贴区：weibo inside Settings › Account")
	}
	if _, ok := tt.Find("Cookie获取步骤：weibo"); !ok {
		t.Fatal("expected Cookie获取步骤：weibo inside Settings › Account inline paste")
	}

	// When logged out of Weibo, Settings › Account offers "粘贴Cookie登录微博" -> /login weibo cookie tab.
	stub.mu.Lock()
	stub.wbStatus = credential.Status{Site: string(site.Weibo), LoggedIn: false, State: "logged_out"}
	stub.mu.Unlock()
	tt.Frame()
	if err := tt.Click("粘贴Cookie登录微博"); err != nil {
		t.Fatalf("click 粘贴Cookie登录微博: %v", err)
	}
	tt.Frame()
	if app.CurrentRoute != RouteLogin || app.LoginSite != "weibo" || app.WeiboLoginTab != "cookie" {
		t.Fatalf("expected /login weibo cookie tab, got route=%s site=%s tab=%s", app.CurrentRoute, app.LoginSite, app.WeiboLoginTab)
	}
}

func TestSettingsAccountThreeGroupsRemoteAndCheckPlanWeibo(t *testing.T) {
	app, stub, cleanup := newWeiboUITestHarness(t)
	defer cleanup()

	if _, err := stub.raw.ImportCookie(context.Background(), "SESSDATA=synth_bili; bili_jct=csrf; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	stub.mu.Lock()
	stub.dyStatus = credential.Status{Site: "douyin", LoggedIn: true, State: "logged_in", UID: "9001", Uname: "抖音号"}
	stub.wbStatus = credential.Status{Site: "weibo", LoggedIn: true, State: "logged_in", Source: "manual", UID: "500000100", Uname: "微博演示号"}
	nowStr := time.Now().UTC().Format(time.RFC3339)
	stub.remoteConnInfo = &service.RemoteConnectionInfo{ID: "r1", Name: "VPS", URL: "https://203.0.113.10:47380"}
	stub.remoteView = &service.RemoteView{
		Connection: *stub.remoteConnInfo,
		Status: remote.Status{
			Version: "0.5.0",
			Sites: []remote.SiteStatus{
				{Site: "bilibili", State: remote.SiteValid, Uname: "B站号"},
				{Site: "douyin", State: remote.SiteNone},
				{Site: "weibo", State: remote.SiteValid, Uname: "微博演示号", CookieSyncedAt: nowStr},
			},
		},
	}
	stub.checkPlanRows = []service.AccountCheckRow{
		{
			AccountCheckPlan: subscription.AccountCheckPlan{
				SubID: "sub-wb", SubName: "微博订阅", Site: "weibo", UID: "500000001", Name: "微博博主",
				Plan: subscription.PlanScheduled, NextAt: time.Now().Add(20 * time.Minute).UTC().Format(time.RFC3339),
				AutoRecord: true,
				LiveNote:   "微博直播暂不支持录制",
			},
		},
	}
	stub.mu.Unlock()
	app.CompleteLogin()

	posted := make(chan func(), 32)
	app.dispatch = func(fn func()) { posted <- fn }

	tt := ui.NewTester(app.View, 1280, 860)
	app.SettingsCategory = "account"
	app.Navigate(RouteSettings)
	tt.Frame()

	for _, lbl := range []string{"账号分组：哔哩哔哩", "账号分组：抖音", "账号分组：微博"} {
		if _, ok := tt.Find(lbl); !ok {
			t.Fatalf("expected account settings group %q", lbl)
		}
	}
	if !tt.HasText("粘贴的 Cookie 不续期") {
		t.Fatal("expected Weibo account group to explain pasted cookies do not renew")
	}

	// Independent Weibo logout dialog leaves Bilibili & Douyin logged in.
	if err := tt.Click("退出微博登录"); err != nil {
		t.Fatalf("click Weibo logout button: %v", err)
	}
	tt.Frame()
	if !app.ConfirmLogoutOpen || app.ConfirmLogoutSite != "weibo" {
		t.Fatalf("expected Weibo logout confirm dialog, got open=%v site=%q", app.ConfirmLogoutOpen, app.ConfirmLogoutSite)
	}
	if !tt.HasText("退出微博登录？") {
		t.Fatal("expected Weibo-specific logout confirmation title")
	}
	if err := tt.Click("确认退出登录"); err != nil {
		t.Fatalf("confirm Weibo logout: %v", err)
	}
	waitUIIdle(t, app, posted)
	tt.Frame()
	if stub.StatusOf("weibo").LoggedIn {
		t.Fatal("expected Weibo logged out after confirming dialog")
	}
	if !stub.StatusOf("bilibili").LoggedIn || !stub.StatusOf("douyin").LoggedIn {
		t.Fatal("expected Bilibili and Douyin to stay logged in after Weibo logout")
	}

	// Settings › Remote renders "微博" site row cleanly.
	app.SettingsCategory = "remote"
	app.Remote.View = stub.remoteView
	app.Remote.FetchedAt = time.Now()
	tt.Frame()
	if !tt.HasText("微博") || siteDisplayName("weibo") != "微博" {
		t.Fatal("expected Settings › Remote to display 微博 login status row")
	}

	// 订阅 › 检查计划 lists Weibo account & displays LiveNote when rule enables auto-record.
	app.OpenSubscriptionChecks()
	app.checkPlanAt = time.Time{}
	tt.Frame()
	if _, ok := tt.Find("检查计划行 weibo:500000001"); !ok {
		t.Fatal("expected Weibo account row in 订阅 › 检查计划")
	}
	if _, ok := tt.Find("检查计划直播说明：weibo:500000001"); !ok || !tt.HasText("微博直播暂不支持录制") {
		t.Fatal("expected Weibo LiveNote (微博直播暂不支持录制) in 订阅 › 检查计划")
	}
}

func TestWeiboUserPagePinnedPaginationVisibilityWindowAndEmptyShell(t *testing.T) {
	app, stub, cleanup := newWeiboUITestHarness(t)
	defer cleanup()

	uid := "500000001"
	stub.mu.Lock()
	stub.wbStatus = credential.Status{Site: "weibo", LoggedIn: true, State: "logged_in", UID: "500000100", Uname: "测试号"}
	stub.wbUsers[uid] = &service.WeiboUserView{
		UID: uid, ScreenName: "科技博主WB", Description: "系统与图形开发笔记",
		StatusesCount: 42, FollowersCount: 12000, FriendsCount: 88,
		Verified: true, VerifiedReason: "科技博主", CanonicalURL: "https://weibo.com/u/" + uid,
	}
	stub.wbUserPages[uid] = map[string]weiboPageFixture{
		"": {page: service.WeiboUserPostsPage{
			Posts: []service.WeiboPostView{
				{Mid: "5001", Author: service.WeiboAuthorDTO{UID: uid, ScreenName: "科技博主WB"}, Text: "置顶微博内容", IsTop: true, CacheKey: "weibo:dynamic:5001"},
				{Mid: "5002", Author: service.WeiboAuthorDTO{UID: uid, ScreenName: "科技博主WB"}, Text: "第二条微博", CacheKey: "weibo:dynamic:5002"},
			},
			NextCursor: "2:5002",
			HasMore:    true,
		}},
		"2:5002": {err: &site.Error{Site: site.Weibo, Kind: site.ErrKindRiskControl, Message: "请求频繁触发风控"}},
	}
	stub.mu.Unlock()
	app.CompleteLogin()

	posted := make(chan func(), 32)
	app.dispatch = func(fn func()) { posted <- fn }
	tt := ui.NewTester(app.View, 1280, 860)

	app.OpenWeiboUser(uid, "科技博主WB")
	waitUIIdle(t, app, posted)
	tt.Frame()

	if app.CurrentRoute != RouteWeiboUser {
		t.Fatalf("CurrentRoute = %s, want %s", app.CurrentRoute, RouteWeiboUser)
	}
	if _, ok := tt.Find("用户页站点标记"); !ok {
		t.Fatal("expected 用户页站点标记 on Weibo user page")
	}
	if !tt.HasText("微博 42") || !tt.HasText("置顶") {
		t.Fatal("expected 微博 42 count and 置顶 badge on Weibo user page")
	}

	// Page-2 risk_control preserves page-1 posts and allows retry in place.
	app.LoadMoreWeiboPosts()
	waitUIIdle(t, app, posted)
	tt.Frame()
	if len(app.CurrentWeiboPosts) != 2 || !strings.Contains(app.WeiboPostsError, "风控") {
		t.Fatalf("expected 2 posts preserved with risk error, got len=%d err=%q", len(app.CurrentWeiboPosts), app.WeiboPostsError)
	}

	// Retry page 2 succeeds and reaches visibility-window bottomTipsText.
	stub.mu.Lock()
	stub.wbUserPages[uid]["2:5002"] = weiboPageFixture{page: service.WeiboUserPostsPage{
		Posts: []service.WeiboPostView{
			{Mid: "5002", Author: service.WeiboAuthorDTO{UID: uid, ScreenName: "科技博主WB"}, Text: "第二条微博", CacheKey: "weibo:dynamic:5002"},
			{Mid: "5003", Author: service.WeiboAuthorDTO{UID: uid, ScreenName: "科技博主WB"}, Text: "第三条微博", CacheKey: "weibo:dynamic:5003"},
		},
		HasMore:           false,
		BottomTipsVisible: true,
		BottomTipsText:    "博主设置了仅展示半年内的微博",
	}}
	stub.mu.Unlock()

	if err := tt.Click("重试加载微博"); err != nil {
		t.Fatalf("click retry Weibo posts: %v", err)
	}
	waitUIIdle(t, app, posted)
	tt.Frame()
	if len(app.CurrentWeiboPosts) != 3 {
		t.Fatalf("expected deduplicated 3 posts after retry, got %d", len(app.CurrentWeiboPosts))
	}
	if !app.WeiboBottomTipsVisible || !tt.HasText("博主设置了仅展示半年内的微博") {
		t.Fatal("expected visibility-window bottomTipsText rendered on Weibo user page")
	}

	// "⋯ → 全部缓存" enqueues posts queue with CompletionNote.
	app.enqueueCacheAll("weibo")
	waitUIIdle(t, app, posted)
	stub.mu.Lock()
	nQ := len(stub.queuedReqs)
	var lastReq service.CacheQueueRequest
	if nQ > 0 {
		lastReq = stub.queuedReqs[nQ-1]
	}
	stub.mu.Unlock()
	if nQ != 1 || lastReq.Site != "weibo" || lastReq.List != "posts" || lastReq.CompletionNote != "博主设置了仅展示半年内的微博" {
		t.Fatalf("unexpected Weibo cache-all queue request: %+v", lastReq)
	}

	// Filter "全部 / 未缓存" switches without network calls.
	beforeCalls := stub.wbCalls.Load()
	if err := tt.Click("用户页筛选：未缓存"); err != nil {
		t.Fatalf("click 未缓存 filter: %v", err)
	}
	tt.Frame()
	if stub.wbCalls.Load() != beforeCalls {
		t.Fatalf("switching 未缓存 filter made network calls: %d -> %d", beforeCalls, stub.wbCalls.Load())
	}

	// Empty-shell uncertain empty renders distinct error (NOT genuine 0 posts).
	emptyShellUID := "500000008"
	stub.mu.Lock()
	stub.wbUsers[emptyShellUID] = &service.WeiboUserView{UID: emptyShellUID, ScreenName: "空壳账号", StatusesCount: 99}
	stub.wbUserPages[emptyShellUID] = map[string]weiboPageFixture{
		"": {page: service.WeiboUserPostsPage{UncertainEmpty: true, UncertainReason: "返回异常空列表，疑似风控"}},
	}
	stub.mu.Unlock()
	app.OpenWeiboUser(emptyShellUID, "空壳账号")
	waitUIIdle(t, app, posted)
	tt.Frame()
	if _, ok := tt.Find("微博空壳提示"); !ok {
		t.Fatal("expected distinct empty-shell error banner")
	}
	if _, ok := tt.Find("微博零内容提示"); ok {
		t.Fatal("must NOT show genuine 0-post message for empty-shell error")
	}

	// Genuine 0-post Weibo user renders "还没有发布公开微博".
	zeroUID := "500000009"
	stub.mu.Lock()
	stub.wbUsers[zeroUID] = &service.WeiboUserView{UID: zeroUID, ScreenName: "零微博新号", StatusesCount: 0}
	stub.wbUserPages[zeroUID] = map[string]weiboPageFixture{"": {page: service.WeiboUserPostsPage{}}}
	stub.mu.Unlock()
	app.OpenWeiboUser(zeroUID, "零微博新号")
	waitUIIdle(t, app, posted)
	tt.Frame()
	if _, ok := tt.Find("微博零内容提示"); !ok || !tt.HasText("还没有发布公开微博") {
		t.Fatal("expected genuine 0-post message 还没有发布公开微博")
	}
}

func TestWeiboPostDetailMediaRetweetDegradedCardsAndLiveHint(t *testing.T) {
	app, stub, cleanup := newWeiboUITestHarness(t)
	defer cleanup()

	stub.mu.Lock()
	stub.wbStatus = credential.Status{Site: "weibo", LoggedIn: true, State: "logged_in", UID: "500000100", Uname: "测试号"}
	stub.mu.Unlock()
	app.CompleteLogin()

	posted := make(chan func(), 32)
	app.dispatch = func(fn func()) { posted <- fn }
	tt := ui.NewTester(app.View, 1280, 1400)

	// 1. Multi-image (>9 = 11 items), GIF, LivePhoto, mixed video, longtext, deleted retweet, visible range, online stats.
	imgs := make([]service.WeiboMediaItemDTO, 11)
	for i := range imgs {
		k := "image"
		motion := ""
		if i == 1 {
			k = "live_photo"
			motion = "https://video.weibo.com/media/play?livephoto=synth"
		} else if i == 2 {
			k = "gif"
		} else if i == 3 {
			k = "video"
			motion = "https://f.video.weibocdn.com/synth_mixed.mp4"
		}
		imgs[i] = service.WeiboMediaItemDTO{Index: i + 1, Kind: k, ImageURL: fmt.Sprintf("https://wx1.sinaimg.cn/large/synth_%02d.jpg", i+1), MotionURL: motion, Width: 800, Height: 800, DurationSec: 6}
	}
	app.CurrentWeiboPost = &service.WeiboPostView{
		Mid:             "5000000000000101",
		Author:          service.WeiboAuthorDTO{UID: "500000001", ScreenName: "微博作者"},
		CreatedAt:       1710004800,
		Text:            "完整长微博全文内容展示",
		IsLongText:      true,
		LongTextFetched: true,
		VisibleLabel:    "仅粉丝可见",
		Images:          imgs,
		Retweeted:       &service.WeiboPostView{Unavailable: true, UnavailableReason: "原微博已删除或不可查看"},
		Stats:           service.WeiboStatsDTO{RepostsCount: 12, CommentsCount: 34, AttitudesCount: 56},
		CanonicalURL:    "https://weibo.com/500000001/5000000000000101",
		CacheKey:        "weibo:dynamic:5000000000000101",
	}
	app.CurrentRoute = RouteWeiboPost
	tt.Frame()

	for _, lbl := range []string{"微博可见范围标记", "微博正文", "查看图文页 1", "实况标记 2", "动图标记 3", "视频页标记 4", "查看图文页 11", "转发原微博不可用提示", "微博在线统计"} {
		if _, ok := tt.Find(lbl); !ok {
			t.Fatalf("expected Weibo post detail control %q", lbl)
		}
	}
	// Clicking LivePhoto opens ImageViewer with InlineSite == "weibo".
	if err := tt.Click("查看图文页 2"); err != nil {
		t.Fatalf("click LivePhoto page 2: %v", err)
	}
	if !app.ImageViewer.Open || app.ImageViewer.InlineSite != "weibo" || len(app.ImageViewer.URLs) != 11 {
		t.Fatalf("expected ImageViewer open with InlineSite=weibo and 11 pages, got open=%v site=%q len=%d",
			app.ImageViewer.Open, app.ImageViewer.InlineSite, len(app.ImageViewer.URLs))
	}
	app.closeImageViewer()

	// 2. Degraded article/vote/live cards show "在浏览器中打开" and NO video cache button.
	for _, cardKind := range []string{"article", "vote", "live"} {
		app.CurrentWeiboPost = &service.WeiboPostView{
			Mid:          "5000000000000102",
			Author:       service.WeiboAuthorDTO{UID: "500000001", ScreenName: "微博作者"},
			Text:         "降级卡片测试：" + cardKind,
			Card:         &service.WeiboRichCardDTO{Kind: cardKind, Title: "卡片标题", Desc: "卡片简介", URL: "https://weibo.com/card/" + cardKind, Unsupported: true},
			CanonicalURL: "https://weibo.com/500000001/5000000000000102",
		}
		tt.Frame()
		if _, ok := tt.Find("微博卡片：" + cardKind); !ok {
			t.Fatalf("expected degraded card %q", cardKind)
		}
		if _, ok := tt.Find("微博卡片在浏览器中打开：" + cardKind); !ok {
			t.Fatalf("expected open-in-browser button for card %q", cardKind)
		}
		if _, ok := tt.Find("缓存当前微博"); ok {
			t.Fatalf("must NOT show cache button on degraded %q card", cardKind)
		}
		if cardKind == "live" && !tt.HasText("微博直播暂不支持录制，可以在浏览器中打开") {
			t.Fatal("expected unsupported live message on Weibo live card")
		}
	}

	// 3. Standalone WeiboVideo renders and triggers CacheWeiboVideo.
	app.CurrentWeiboPost = nil
	app.CurrentWeiboVideo = &service.WeiboVideoView{
		ObjectID:     "1034:5000000000000999",
		Title:        "独立微博视频页",
		AuthorUID:    "500000001",
		AuthorName:   "微博作者",
		Video:        &service.WeiboVideoDTO{ObjectID: "1034:5000000000000999", Title: "独立微博视频页", DurationSec: 75, QualityLabel: "1080P", Codec: "hevc"},
		CanonicalURL: "https://weibo.com/tv/show/1034:5000000000000999",
	}
	tt.Frame()
	if err := tt.Click("缓存当前微博视频"); err != nil {
		t.Fatalf("click cache standalone Weibo video: %v", err)
	}
	waitUIIdle(t, app, posted)
	if stub.cachedOID != "1034:5000000000000999" {
		t.Fatalf("cachedOID = %q, want 1034:5000000000000999", stub.cachedOID)
	}

	// 4. Weibo live link on Parse page shows exact unsupported hint with 0 network calls.
	stub.wbCalls.Store(0)
	app.Navigate(RouteParse)
	app.ParsePageInput = "https://weibo.com/l/wblive/p/show/1022:2321325000000000000099"
	tt.Frame()
	_ = tt.Click("立即解析")
	nextPosted(t, posted)()
	tt.Frame()
	if stub.wbCalls.Load() != 0 {
		t.Fatalf("expected 0 Weibo network calls for live link, got %d", stub.wbCalls.Load())
	}
	if app.LastParseError != "微博直播暂不支持录制，可以在浏览器中打开" {
		t.Fatalf("LastParseError = %q, want 微博直播暂不支持录制，可以在浏览器中打开", app.LastParseError)
	}
}

func TestMineWeiboFollowingFavoritesAndFlags(t *testing.T) {
	app, stub, cleanup := newWeiboUITestHarness(t)
	defer cleanup()

	uid := "500000002"
	mid := "5000000000000201"
	stub.mu.Lock()
	stub.wbStatus = credential.Status{Site: "weibo", LoggedIn: true, State: "logged_in", UID: "500000100", Uname: "测试号"}
	stub.wbUsers[uid] = &service.WeiboUserView{UID: uid, ScreenName: "被关注的博主"}
	stub.wbPosts[mid] = &service.WeiboPostView{
		Mid: mid, Author: service.WeiboAuthorDTO{UID: uid, ScreenName: "被关注的博主"},
		Text: "收藏微博正文内容", CanonicalURL: "https://weibo.com/" + uid + "/" + mid,
	}
	// Likes & follow groups are off by default (AccountSections omits likes; AccountFolders returns nil).
	stub.mineSections["weibo"] = []service.AccountSection{
		{Site: "weibo", Kind: service.AccountFollowing, Label: "关注", Available: true},
		{Site: "weibo", Kind: service.AccountFavorites, Label: "收藏", Available: true},
	}
	stub.minePages["weibo/following"] = service.AccountPage{
		Items: []service.AccountItem{
			{Kind: service.AccountItemUser, Site: "weibo", ID: uid, Title: "被关注的博主", Signature: "签名", BrowserURL: "https://weibo.com/u/" + uid},
		},
	}
	stub.minePages["weibo/favorites"] = service.AccountPage{
		Items: []service.AccountItem{
			{Kind: service.AccountItemPost, Site: "weibo", ID: mid, Title: "收藏微博正文内容", Author: "被关注的博主", AuthorID: uid, BrowserURL: "https://weibo.com/" + uid + "/" + mid, Post: stub.wbPosts[mid]},
		},
	}
	stub.mu.Unlock()
	app.CompleteLogin()

	posted := make(chan func(), 32)
	app.dispatch = func(fn func()) { posted <- fn }
	tt := ui.NewTester(app.View, 1280, 860)

	app.OpenMine("weibo", "")
	tt.Frame()
	if _, ok := tt.Find("我的站点：微博"); !ok {
		t.Fatal("expected 3rd accordion group 我的站点：微博")
	}
	if _, ok := tt.Find("我的功能：微博 关注"); !ok {
		t.Fatal("expected 我的 › 微博 › 关注")
	}
	if _, ok := tt.Find("我的功能：微博 收藏"); !ok {
		t.Fatal("expected 我的 › 微博 › 收藏")
	}
	if _, ok := tt.Find("我的功能：微博 赞过"); ok {
		t.Fatal("expected 赞过 hidden when flag is off")
	}

	// Open Following: no group dropdown when AccountFolders returns empty; clicking row opens Weibo user page.
	_ = tt.Click("我的功能：微博 关注")
	waitUIIdle(t, app, posted)
	tt.Frame()
	if _, ok := tt.Find("关注分组选择"); ok {
		t.Fatal("expected follow groups selector hidden when AccountFolders returns empty")
	}
	if err := tt.Click("我的用户：被关注的博主"); err != nil {
		t.Fatalf("click Weibo followed user: %v", err)
	}
	waitUIIdle(t, app, posted)
	if app.CurrentRoute != RouteWeiboUser || app.CurrentWeiboUser == nil || app.CurrentWeiboUser.UID != uid {
		t.Fatalf("expected opening Weibo user detail, got route=%s user=%+v", app.CurrentRoute, app.CurrentWeiboUser)
	}

	// Open Favorites: clicking post opens Weibo post detail; "⋯" supports cache & subscribe picker.
	app.OpenMine("weibo", service.AccountFavorites)
	waitUIIdle(t, app, posted)
	tt.Frame()
	if err := tt.Click("我的微博：" + mid); err != nil {
		t.Fatalf("click favorite Weibo post: %v", err)
	}
	waitUIIdle(t, app, posted)
	if app.CurrentRoute != RouteWeiboPost || app.CurrentWeiboPost == nil || app.CurrentWeiboPost.Mid != mid {
		t.Fatalf("expected opening Weibo post detail, got route=%s post=%+v", app.CurrentRoute, app.CurrentWeiboPost)
	}

	// Narrow window picker includes Weibo.
	app.OpenMine("", "")
	app.Mine.Picker = true
	ttNarrow := ui.NewTester(app.View, 720, 860)
	ttNarrow.Frame()
	if _, ok := ttNarrow.Find("我的站点：微博"); !ok {
		t.Fatal("expected narrow-window picker in 我的 to include 微博")
	}
}

func TestFollowingImportSiteSwitcherWeiboAndGroupsFlag(t *testing.T) {
	app, stub, cleanup := newWeiboUITestHarness(t)
	defer cleanup()

	if _, err := stub.raw.ImportCookie(context.Background(), "SESSDATA=synth_bili; bili_jct=csrf; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	stub.mu.Lock()
	stub.wbStatus = credential.Status{Site: "weibo", LoggedIn: true, State: "logged_in", UID: "500000100", Uname: "测试号"}
	stub.followGroups["bilibili"] = []service.FollowingGroupDTO{{ID: "-10", Name: "特别关注", Count: 1}}
	stub.followPages["bilibili"] = map[int]service.FollowingCreatorsPageDTO{
		1: {Site: "bilibili", Page: 1, GroupsEnabled: true, Items: []service.FollowingCreatorDTO{{Site: "bilibili", UID: "20001", Name: "B站UP"}}},
	}
	stub.followGroups["weibo"] = nil // flags off -> empty groups
	stub.followPages["weibo"] = map[int]service.FollowingCreatorsPageDTO{
		1: {Site: "weibo", Page: 1, HasMore: true, GroupsEnabled: false, Items: []service.FollowingCreatorDTO{
			{Site: "weibo", UID: "500000001", Name: "已订阅博主", Subscribed: true, OwnerSubName: "现有订阅"},
			{Site: "weibo", UID: "500000002", Name: "待导入博主一", Sign: "签名一"},
		}},
		2: {Site: "weibo", Page: 2, HasMore: false, GroupsEnabled: false, Items: []service.FollowingCreatorDTO{
			{Site: "weibo", UID: "500000003", Name: "待导入博主二", Sign: "签名二"},
		}},
	}
	stub.mu.Unlock()
	app.CompleteLogin()

	posted := make(chan func(), 32)
	app.dispatch = func(fn func()) { posted <- fn }
	tt := ui.NewTester(app.View, 1280, 860)

	app.Navigate(RouteSubscriptions)
	app.openImportFollowingsModal()
	waitUIIdle(t, app, posted)
	tt.Frame()

	// Bilibili shows groups selector; switch to Weibo hides groups selector.
	if _, ok := tt.Find("关注分组"); !ok {
		t.Fatal("expected group selector shown when FollowingGroups returns non-empty for Bilibili")
	}
	if err := tt.Click("关注导入站点：微博"); err != nil {
		t.Fatalf("switch import modal to Weibo: %v", err)
	}
	waitUIIdle(t, app, posted)
	tt.Frame()

	if _, ok := tt.Find("关注分组"); ok {
		t.Fatal("expected group selector hidden when FollowingGroups returns empty for Weibo")
	}
	if _, ok := tt.Find("勾选导入 500000001"); ok {
		t.Fatal("already-subscribed Weibo creator must not be selectable")
	}
	if _, ok := tt.Find("勾选导入 500000002"); !ok {
		t.Fatal("expected unsubscribed Weibo creator to have toggle")
	}

	// Load page 2 and import both unsubscribed Weibo creators.
	if err := tt.Click("加载下一页关注"); err != nil {
		t.Fatalf("load next page of Weibo followings: %v", err)
	}
	waitUIIdle(t, app, posted)
	tt.Frame()
	if len(app.ImportCreators) != 3 {
		t.Fatalf("expected 3 total loaded Weibo creators after page 2, got %d", len(app.ImportCreators))
	}
	_ = tt.Click("全选已加载")
	tt.Frame()
	if err := tt.Click("确认导入关注"); err != nil {
		t.Fatalf("confirm import Weibo followings: %v", err)
	}
	if stub.importedSite != "weibo" || stub.importedCount != 2 {
		t.Fatalf("expected importing 2 Weibo creators, got site=%q count=%d", stub.importedSite, stub.importedCount)
	}
}

func TestMultiSiteBadgesFourWayFilterAndWeiboAccountRowNoRecord(t *testing.T) {
	app, stub, cleanup := newWeiboUITestHarness(t)
	defer cleanup()

	if _, err := stub.raw.ImportCookie(context.Background(), "SESSDATA=synth_bili; bili_jct=csrf; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	stub.mu.Lock()
	stub.wbStatus = credential.Status{Site: "weibo", LoggedIn: true, State: "logged_in", UID: "500000100", Uname: "测试号"}
	stub.accountWorks = map[string]service.AccountWorks{
		"weibo:500000001": {Total: 35, Cached: 7},
	}
	stub.mu.Unlock()

	sub, err := stub.raw.CreateSubscription("多站订阅", "", []subscription.AccountRef{
		{Site: "weibo", UID: "500000001", Name: "微博创作者"},
	}, subscription.SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatal(err)
	}
	wbDir := filepath.Join(stub.raw.LibraryStore().CacheDir(), "weibo", "500000001", "posts", "5000000000000501")
	_ = os.MkdirAll(wbDir, 0o700)
	_ = os.WriteFile(filepath.Join(wbDir, "img_01.png"), buildTestPNGBytes(), 0o600)
	wbItem, err := stub.raw.LibraryStore().SaveItem(wbDir, library.ItemMetadata{
		Version: 1, ID: "5000000000000501", RawID: "5000000000000501", Site: "weibo", Kind: "dynamic", Status: "complete",
		Title: "微博多站筛选测试", Text: "微博多站筛选测试", PubTs: 1710005000, CachedAt: "2026-03-02T12:00:00Z",
		Author: library.ItemAuthor{Site: "weibo", UID: "500000001", Name: "微博创作者"},
		Media:  []library.MediaFile{{Role: "image", Path: "img_01.png", Width: 64, Height: 64}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = stub.raw.HistoryStore().Record(storage.HistoryEntry{
		Site: "weibo", Kind: "dynamic", ID: "5000000000000501", Title: "微博最近解析", Subtitle: "微博创作者", CanonicalURL: "https://weibo.com/500000001/5000000000000501",
	})
	app.CompleteLogin()

	// 1. 4-way site filter on Home at 720w fits without overflow & filters to Weibo.
	tt720 := ui.NewTester(app.View, 720, 860)
	app.Navigate(RouteHome)
	tt720.Frame()
	for _, lbl := range []string{"主页站点筛选：全部", "主页站点筛选：B 站", "主页站点筛选：抖音", "主页站点筛选：微博"} {
		r, ok := tt720.Find(lbl)
		if !ok || r.X+r.W > 720.5 {
			t.Fatalf("expected 4-way Home filter %q visible within 720w, ok=%v rect=%+v", lbl, ok, r)
		}
	}
	if err := tt720.Click("主页站点筛选：微博"); err != nil {
		t.Fatalf("click Home Weibo filter: %v", err)
	}
	tt720.Frame()
	if app.FeedSiteFilter != "weibo" || len(app.FeedItems) != 1 || app.FeedItems[0].Site != "weibo" {
		t.Fatalf("expected Home filtered to 1 Weibo item, got filter=%q len=%d", app.FeedSiteFilter, len(app.FeedItems))
	}
	if _, ok := tt720.Find("主页作者站点：" + wbItem.ItemKey); !ok {
		t.Fatal("expected 微博 site badge on Home post")
	}

	// 2. 4-way site filter on Media Library & badge on library card.
	app.Navigate(RouteLibrary)
	tt720.Frame()
	for _, lbl := range []string{"媒体库站点筛选：全部", "媒体库站点筛选：B 站", "媒体库站点筛选：抖音", "媒体库站点筛选：微博"} {
		r, ok := tt720.Find(lbl)
		if !ok || r.X+r.W > 720.5 {
			t.Fatalf("expected 4-way Library filter %q visible within 720w, ok=%v rect=%+v", lbl, ok, r)
		}
	}
	if _, ok := tt720.Find("媒体库站点：" + wbItem.ItemKey); !ok {
		t.Fatal("expected 微博 site badge on Library card")
	}

	// 3. Subscription grid has NO site badge.
	tt1280 := ui.NewTester(app.View, 1280, 860)
	app.Navigate(RouteSubscriptions)
	tt1280.Frame()
	if _, ok := tt1280.Find("订阅详情账号站点：weibo:500000001"); ok {
		t.Fatal("subscription grid avatar must NOT display site badge")
	}

	// 4. Subscription detail accounts tab: Weibo row has "微博" badge, "微博 35 · 已缓存 7", NO live status & NO record button.
	app.SubDetailTab = "accounts"
	app.OpenSubscriptionDetail(sub)
	app.SubDetailTab = "accounts"
	tt1280.Frame()
	if _, ok := tt1280.Find("订阅详情账号站点：weibo:500000001"); !ok {
		t.Fatal("expected 微博 site badge on subscription detail account row")
	}
	if !tt1280.HasText("微博 35 · 已缓存 7") {
		t.Fatal("expected 微博 35 · 已缓存 7 works summary on Weibo account row")
	}
	if tt1280.HasText("未开播") || tt1280.HasText("直播中") || tt1280.HasText("录制") {
		t.Fatal("Weibo account row must NOT show live status or record button")
	}
	if _, ok := tt1280.Find("开始录制 weibo:500000001"); ok {
		t.Fatal("Weibo account row must NOT have record button")
	}

	// 5. Recent parse sidebar has "微博" badge.
	app.Navigate(RouteParse)
	tt1280.Frame()
	if _, ok := tt1280.Find("历史站点：weibo:5000000000000501"); !ok {
		t.Fatal("expected 微博 site badge in recent parse sidebar")
	}

	// 6. Task row has "微博" badge.
	app.Navigate(RouteTasks)
	app.TasksSection = "downloads"
	app.Tasks = []download.Task{{TaskID: "wb-t1", Site: "weibo", Kind: "dynamic", TargetID: "5001", Title: "微博下载任务", Status: "running"}}
	tt1280.Frame()
	if _, ok := tt1280.Find("任务站点：wb-t1"); !ok {
		t.Fatal("expected 微博 site badge on download task row")
	}
}
