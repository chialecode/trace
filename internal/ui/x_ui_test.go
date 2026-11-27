package ui

import (
	"context"
	"fmt"
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

type xRateLimitSyntheticErr struct {
	resetAt time.Time
}

func (e *xRateLimitSyntheticErr) Error() string               { return "rate limited" }
func (e *xRateLimitSyntheticErr) RateLimitResetAt() time.Time { return e.resetAt }

type xPageFixture struct {
	page service.XUserPostsPageDTO
	err  error
}

type xStubBackend struct {
	service.BackendAPI
	raw *service.Backend

	mu         sync.Mutex
	dyStatus   credential.Status
	wbStatus   credential.Status
	xStatus    credential.Status
	loginGate  chan struct{}
	onLogin    func()
	loginMode  string // "ok" | "missing" | "unauth" | "rate_limit" | "risk"
	xCalls     atomic.Int32
	liveCalls  atomic.Int32
	pushCalls  map[string]int
	cachedID   string
	queuedReqs []service.CacheQueueRequest

	xUsers         map[string]*service.XUserDTO
	xUserPages     map[string]map[string]xPageFixture
	xUserTabPages  map[string]map[string]map[string]xPageFixture
	xTabCalls      map[string]int
	xBatchPageReqs []service.XBatchCachePageRequest
	xBatchPageRes  *service.XBatchCachePageResult
	xBatchPageErr  error
	xPosts         map[string]*service.XPostDTO
	accountWorks   map[string]service.AccountWorks
	checkPlan      []service.AccountCheckRow
	followCursor   map[string]service.FollowingCreatorsPageDTO
	followGroups   int
	mineSections   map[string][]service.AccountSection
	minePages      map[string]service.AccountPage
	mineErrors     map[string]error
	importedSite   string
	importedCnt    int

	remoteView *service.RemoteView
	remoteInfo *service.RemoteConnectionInfo
}

func newXUITestHarness(t *testing.T) (*App, *xStubBackend, func()) {
	t.Helper()
	app, _, cleanup := newTestUIApp(t)
	raw := app.backend.(*service.Backend)
	if _, err := raw.ImportCookie(context.Background(), "SESSDATA=synth_bili; bili_jct=csrf; DedeUserID=12345", ""); err != nil {
		t.Fatalf("seed bilibili login: %v", err)
	}
	stub := &xStubBackend{
		BackendAPI:    raw,
		raw:           raw,
		dyStatus:      credential.Status{Site: string(site.Douyin), State: "logged_out"},
		wbStatus:      credential.Status{Site: string(site.Weibo), State: "logged_out"},
		xStatus:       credential.Status{Site: string(site.X), State: "logged_out"},
		loginMode:     "ok",
		pushCalls:     map[string]int{},
		xUsers:        map[string]*service.XUserDTO{},
		xUserPages:    map[string]map[string]xPageFixture{},
		xUserTabPages: map[string]map[string]map[string]xPageFixture{},
		xTabCalls:     map[string]int{},
		xPosts:        map[string]*service.XPostDTO{},
		followCursor:  map[string]service.FollowingCreatorsPageDTO{},
		mineSections:  map[string][]service.AccountSection{},
		minePages:     map[string]service.AccountPage{},
		mineErrors:    map[string]error{},
	}
	app.backend = stub
	return app, stub, cleanup
}

func (s *xStubBackend) Statuses() map[string]credential.Status {
	s.mu.Lock()
	dy, wb, x := s.dyStatus, s.wbStatus, s.xStatus
	s.mu.Unlock()
	return map[string]credential.Status{
		string(site.Bilibili): s.BackendAPI.StatusOf(string(site.Bilibili)),
		string(site.Douyin):   dy,
		string(site.Weibo):    wb,
		string(site.X):        x,
	}
}

func (s *xStubBackend) StatusOf(siteID string) credential.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch strings.ToLower(strings.TrimSpace(siteID)) {
	case string(site.Douyin):
		return s.dyStatus
	case string(site.Weibo):
		return s.wbStatus
	case string(site.X):
		return s.xStatus
	default:
		return s.BackendAPI.StatusOf(siteID)
	}
}

func (s *xStubBackend) HasAnyLogin() bool {
	if s.BackendAPI.StatusOf(string(site.Bilibili)).LoggedIn {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dyStatus.LoggedIn || s.wbStatus.LoggedIn || s.xStatus.LoggedIn
}

func (s *xStubBackend) LoginWithCookie(ctx context.Context, siteID, rawCookie, rawRefreshToken string) (credential.Status, error) {
	if !strings.EqualFold(strings.TrimSpace(siteID), string(site.X)) {
		return s.BackendAPI.LoginWithCookie(ctx, siteID, rawCookie, rawRefreshToken)
	}
	s.mu.Lock()
	gate := s.loginGate
	onLogin := s.onLogin
	mode := s.loginMode
	s.mu.Unlock()
	if onLogin != nil {
		onLogin()
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return credential.Status{}, ctx.Err()
		}
	}
	switch mode {
	case "missing":
		return credential.Status{}, &site.Error{Site: site.X, Kind: site.ErrKindInvalidInput, Message: "缺少 auth_token 或 ct0"}
	case "unauth":
		s.xCalls.Add(1)
		return credential.Status{}, &site.Error{Site: site.X, Kind: site.ErrKindAuthRequired, Message: "请先登录 X"}
	case "rate_limit":
		s.xCalls.Add(1)
		return credential.Status{}, &site.Error{
			Site:    site.X,
			Kind:    site.ErrKindRateLimited,
			Message: "X 限流，约 12 分钟后再试",
			Err:     &xRateLimitSyntheticErr{resetAt: time.Now().Add(12 * time.Minute)},
		}
	case "risk":
		s.xCalls.Add(1)
		return credential.Status{}, &site.Error{Site: site.X, Kind: site.ErrKindRiskControl, Message: "challenge required"}
	default:
		s.xCalls.Add(1)
		st := credential.Status{
			Site:     string(site.X),
			LoggedIn: true,
			State:    "logged_in",
			Source:   "manual",
			UID:      "900001001",
			UniqueID: "synth_x_tester",
			Uname:    "合成X测试号",
		}
		s.mu.Lock()
		s.xStatus = st
		s.mu.Unlock()
		return st, nil
	}
}

func (s *xStubBackend) LogoutSite(siteID string) (credential.Status, error) {
	if strings.EqualFold(strings.TrimSpace(siteID), string(site.X)) {
		st := credential.Status{Site: string(site.X), State: "logged_out"}
		s.mu.Lock()
		s.xStatus = st
		s.mu.Unlock()
		return st, nil
	}
	return s.BackendAPI.LogoutSite(siteID)
}

func (s *xStubBackend) OpenLink(ctx context.Context, raw string) (*service.OpenLinkResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.Contains(raw, "x.com/i/user/") || strings.Contains(raw, "x.com/synth_creator") {
		s.xCalls.Add(1)
		tab := "posts"
		lowerRaw := strings.ToLower(raw)
		switch {
		case strings.Contains(lowerRaw, "/with_replies"):
			tab = "replies"
		case strings.Contains(lowerRaw, "/media"):
			tab = "media"
		case strings.Contains(lowerRaw, "/highlights"):
			tab = "highlights"
		}
		for uid, u := range s.xUsers {
			if strings.Contains(raw, uid) || (u.ScreenName != "" && strings.Contains(raw, u.ScreenName)) {
				res := &service.OpenLinkResult{
					Site:       string(site.X),
					SiteTarget: &site.ResolveTarget{Site: site.X, Kind: "user", ID: uid, Tab: tab, CanonicalURL: raw},
					XUser:      u,
					XUserTab:   tab,
				}
				s.xTabCalls[uid+"|"+tab+"|"]++
				if fix, ok := s.lookupXTabFixtureLocked(uid, tab, ""); ok {
					if fix.err != nil {
						res.XUserPostsError = fix.err.Error()
					} else {
						cp := fix.page
						if cp.Tab == "" {
							cp.Tab = tab
						}
						res.XUserPosts = &cp
					}
				}
				return res, nil
			}
		}
	}
	if strings.Contains(raw, "/status/") {
		s.xCalls.Add(1)
		for id, p := range s.xPosts {
			if strings.Contains(raw, id) {
				cp := *p
				return &service.OpenLinkResult{
					Site:       string(site.X),
					SiteTarget: &site.ResolveTarget{Site: site.X, Kind: "dynamic", ID: id, CanonicalURL: cp.CanonicalURL},
					XPost:      &cp,
				}, nil
			}
		}
	}
	return s.BackendAPI.OpenLink(ctx, raw)
}

func (s *xStubBackend) lookupXTabFixtureLocked(restID, tab, cursor string) (xPageFixture, bool) {
	if tab == "" {
		tab = "posts"
	}
	if tabs, ok := s.xUserTabPages[restID]; ok {
		if pages, ok := tabs[tab]; ok {
			if fix, ok := pages[cursor]; ok {
				return fix, true
			}
		}
	}
	if tab == "posts" {
		if pages, ok := s.xUserPages[restID]; ok {
			if fix, ok := pages[cursor]; ok {
				return fix, true
			}
		}
	}
	return xPageFixture{}, false
}

func (s *xStubBackend) XUserPosts(ctx context.Context, restID, cursor string) (*service.XUserPostsPageDTO, error) {
	return s.XUserTabPosts(ctx, restID, "posts", cursor)
}

func (s *xStubBackend) XUserTabPosts(_ context.Context, restID, tab, cursor string) (*service.XUserPostsPageDTO, error) {
	if tab == "" {
		tab = "posts"
	}
	s.xCalls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.xTabCalls[restID+"|"+tab+"|"+cursor]++
	if fix, ok := s.lookupXTabFixtureLocked(restID, tab, cursor); ok {
		if fix.err != nil {
			return nil, fix.err
		}
		cp := fix.page
		if cp.Tab == "" {
			cp.Tab = tab
		}
		return &cp, nil
	}
	return &service.XUserPostsPageDTO{Tab: tab}, nil
}

func (s *xStubBackend) CacheXPostsPage(_ context.Context, req service.XBatchCachePageRequest) (service.XBatchCachePageResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.xBatchPageReqs = append(s.xBatchPageReqs, req)
	if s.xBatchPageErr != nil {
		var partial service.XBatchCachePageResult
		if s.xBatchPageRes != nil {
			partial = *s.xBatchPageRes
		}
		return partial, s.xBatchPageErr
	}
	if s.xBatchPageRes != nil {
		return *s.xBatchPageRes, nil
	}
	res := service.XBatchCachePageResult{}
	for i := range req.Posts {
		p := &req.Posts[i]
		if service.IsXPostDTOUncacheable(p) {
			res.SkippedUnsupported++
			continue
		}
		if res.Enqueued >= service.MaxXCurrentPageBatchCache {
			res.SkippedCapped++
			continue
		}
		res.Enqueued++
		res.Tasks = append(res.Tasks, download.Task{
			TaskID:   "task-x-" + p.ID,
			Site:     string(site.X),
			TargetID: p.ID,
			Status:   "queued",
		})
	}
	return res, nil
}

func (s *xStubBackend) CacheXPost(_ context.Context, statusID string, _ *library.ItemVia) (download.Task, error) {
	s.mu.Lock()
	s.cachedID = statusID
	s.mu.Unlock()
	return download.Task{TaskID: "task-x-" + statusID, Site: string(site.X), TargetID: statusID, Status: "queued"}, nil
}

func (s *xStubBackend) EnqueueCacheQueue(_ context.Context, req service.CacheQueueRequest) (service.CacheQueueTask, error) {
	s.mu.Lock()
	s.queuedReqs = append(s.queuedReqs, req)
	s.mu.Unlock()
	return service.CacheQueueTask{ID: "cq-x-1", Site: req.Site, User: req.User, List: req.List, Status: service.CacheQueueQueued}, nil
}

func (s *xStubBackend) LiveStatuses(ctx context.Context, accountKeys []string) ([]service.LiveAccountState, error) {
	s.liveCalls.Add(1)
	return s.BackendAPI.LiveStatuses(ctx, accountKeys)
}

func (s *xStubBackend) SubscriptionAccountWorks() map[string]service.AccountWorks {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.accountWorks != nil {
		return s.accountWorks
	}
	return s.BackendAPI.SubscriptionAccountWorks()
}

func (s *xStubBackend) CheckPlan() []service.AccountCheckRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.checkPlan != nil {
		return s.checkPlan
	}
	return s.BackendAPI.CheckPlan()
}

func (s *xStubBackend) FollowingGroups(ctx context.Context, siteID string) ([]service.FollowingGroupDTO, error) {
	if strings.EqualFold(siteID, string(site.X)) {
		s.mu.Lock()
		s.followGroups++
		s.mu.Unlock()
		return nil, nil
	}
	return s.BackendAPI.FollowingGroups(ctx, siteID)
}

func (s *xStubBackend) FollowingCreatorsPageWithCursor(_ context.Context, siteID, _ string, cursor string) (service.FollowingCreatorsPageDTO, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pg, ok := s.followCursor[siteID+"|"+cursor]; ok {
		return pg, nil
	}
	return service.FollowingCreatorsPageDTO{Site: siteID}, nil
}

func (s *xStubBackend) ImportFollowingCreators(siteID string, creators []service.FollowingCreatorDTO, _ subscription.SubRules) (subscription.ImportAccountRefsResult, error) {
	s.mu.Lock()
	s.importedSite = siteID
	s.importedCnt = len(creators)
	s.mu.Unlock()
	return subscription.ImportAccountRefsResult{Created: len(creators)}, nil
}

func (s *xStubBackend) AccountSections(siteID string) []service.AccountSection {
	s.mu.Lock()
	secs, ok := s.mineSections[strings.ToLower(strings.TrimSpace(siteID))]
	s.mu.Unlock()
	if ok {
		return secs
	}
	return s.BackendAPI.AccountSections(siteID)
}

func (s *xStubBackend) AccountPage(_ context.Context, siteID, kind, folderID, cursor string) (service.AccountPage, error) {
	k := strings.ToLower(strings.TrimSpace(siteID)) + "/" + kind + "|" + cursor
	s.mu.Lock()
	err := s.mineErrors[k]
	pg, ok := s.minePages[k]
	s.mu.Unlock()
	if err != nil {
		return service.AccountPage{}, err
	}
	if ok {
		return pg, nil
	}
	return service.AccountPage{}, nil
}

func (s *xStubBackend) RemoteConnection() (service.RemoteConnectionInfo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.remoteInfo != nil {
		return *s.remoteInfo, true
	}
	return s.BackendAPI.RemoteConnection()
}

func (s *xStubBackend) RemoteStatus(ctx context.Context) (service.RemoteView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.remoteView != nil {
		return *s.remoteView, nil
	}
	return s.BackendAPI.RemoteStatus(ctx)
}

func (s *xStubBackend) PushCredentialToRemote(_ context.Context, siteID string) (remote.SiteStatus, error) {
	s.mu.Lock()
	s.pushCalls[strings.ToLower(siteID)]++
	s.mu.Unlock()
	return remote.SiteStatus{Site: siteID, State: remote.SiteValid}, nil
}

// ============================================================================
// 1. X Login Risk Disclosure (above submit), Input Clear, Error Copy,
//    Settings › Accounts 4th Group & Independent Logout
// ============================================================================

func TestXLoginRiskDisclosureSettingsAndIndependentLogout(t *testing.T) {
	app, stub, cleanup := newXUITestHarness(t)
	defer cleanup()

	app.OpenLoginForSite(string(site.X))
	tt := ui.NewTester(app.View, 1000, 860)
	tt.Frame()

	// Verify 4-site login switcher and first-appearance title "登录 X（原 Twitter）".
	for _, lbl := range []string{"登录站点：哔哩哔哩", "登录站点：抖音", "登录站点：微博", "登录站点：X"} {
		if _, ok := tt.Find(lbl); !ok {
			t.Fatalf("missing login switcher button %q", lbl)
		}
	}
	if !tt.HasText("登录 X（原 Twitter）") {
		t.Fatal("missing first-appearance X title '登录 X（原 Twitter）'")
	}

	// Risk disclosure box MUST appear strictly ABOVE submit button!
	riskBounds, ok := tt.Find("X登录风险提示")
	if !ok {
		t.Fatal("missing X login risk disclosure box")
	}
	submitBounds, ok := tt.Find("验证并登录X")
	if !ok {
		t.Fatal("missing X login submit button")
	}
	if riskBounds.Y+riskBounds.H > submitBounds.Y+1 {
		t.Fatalf("risk disclosure (bottom=%.1f) must be strictly above submit button (top=%.1f)", riskBounds.Y+riskBounds.H, submitBounds.Y)
	}
	for _, want := range []string{"不是官方开放 API", "服务条款", "限流", "验证", "临时锁号", "封号", "专门账号", "只读不写", "不保存账号密码", "自动续期"} {
		if !tt.HasText(want) {
			t.Fatalf("X login risk disclosure missing required phrase %q", want)
		}
	}

	// Verify input clears immediately on submit even before validation runs.
	var inputSeenDuringLogin string
	var onLoginHook func()
	stub.mu.Lock()
	stub.loginMode = "risk"
	stub.mu.Unlock()
	onLoginHook = func() { inputSeenDuringLogin = app.XCookieInput }
	stub.onLogin = onLoginHook

	app.XCookieInput = "auth_token=SYNTH_FAKE_TOKEN_0001; ct0=SYNTH_FAKE_CT0_0001"
	if err := tt.Click("验证并登录X"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if inputSeenDuringLogin != "" || app.XCookieInput != "" {
		t.Fatalf("XCookieInput was not cleared before backend LoginWithCookie: during=%q after=%q", inputSeenDuringLogin, app.XCookieInput)
	}
	stub.onLogin = nil
	wantRisk := "X 要求验证或账号被临时锁定，TRACE 已停止 X 的全部自动请求，请先在浏览器里处理"
	if app.LoginError != wantRisk {
		t.Fatalf("risk LoginError = %q, want %q", app.LoginError, wantRisk)
	}

	// Verify rate-limit error includes estimated recovery time.
	stub.mu.Lock()
	stub.loginGate = nil
	stub.loginMode = "rate_limit"
	stub.mu.Unlock()
	app.XCookieInput = "auth_token=SYNTH_FAKE_TOKEN_0002; ct0=SYNTH_FAKE_CT0_0002"
	app.importXCookie()
	tt.Frame()
	if !strings.Contains(app.LoginError, "12 分钟") {
		t.Fatalf("rate-limit LoginError = %q, want estimated recovery time", app.LoginError)
	}

	// Verify successful login and Settings › Accounts 4th group.
	stub.mu.Lock()
	stub.loginMode = "ok"
	stub.mu.Unlock()
	app.XCookieInput = "auth_token=SYNTH_FAKE_TOKEN_0003; ct0=SYNTH_FAKE_CT0_0003"
	app.importXCookie()
	tt.Frame()
	if !stub.StatusOf("x").LoggedIn {
		t.Fatal("expected X to be logged in")
	}

	app.SettingsCategory = "account"
	app.Navigate(RouteSettings)
	tt.Frame()
	if _, ok := tt.Find("账号分组：X"); !ok {
		t.Fatal("missing Settings › Accounts 4th group for X")
	}
	for _, want := range []string{"X（原 Twitter）", "@synth_x_tester", "粘贴的 Cookie 不续期", "风险说明"} {
		if !tt.HasText(want) {
			t.Fatalf("Settings › Accounts X group missing %q", want)
		}
	}

	// Independent logout logs out X only, preserving Bilibili login.
	if err := tt.Click("退出X登录"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("退出 X 登录？") {
		t.Fatal("missing X logout confirmation title")
	}
	if err := tt.Click("确认退出登录"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if stub.StatusOf("x").LoggedIn {
		t.Fatal("expected X to be logged out after independent logout")
	}
	if !stub.StatusOf("bilibili").LoggedIn {
		t.Fatal("independent X logout unexpectedly logged out Bilibili")
	}
}

// ============================================================================
// 2. Five-Segment Site Filter at 720px/1000px/1280px & Exact 6 Site Badge Places
// ============================================================================

func TestXSiteFilterFiveSegmentsAt720pxAndSixSiteBadgePlaces(t *testing.T) {
	app, stub, cleanup := newXUITestHarness(t)
	defer cleanup()

	stub.mu.Lock()
	stub.xStatus = credential.Status{Site: string(site.X), LoggedIn: true, State: "logged_in", UID: "900001001", UniqueID: "synth_x_me", Uname: "合成X号"}
	stub.mu.Unlock()

	// Seed an X cached library item, subscription, task, and history entry.
	lib := stub.raw.LibraryStore()
	xKey := library.CanonicalItemKey("x", "dynamic", "1900000000000000001")
	xDir := filepath.Join(lib.CacheDir(), "x", "900002002_synth", "1900000000000000001")
	if err := os.MkdirAll(xDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := lib.SaveItem(xDir, library.ItemMetadata{
		ItemKey: xKey,
		Site:    "x",
		Kind:    "dynamic",
		ID:      "1900000000000000001",
		RawID:   "1900000000000000001",
		Title:   "合成X信息流帖子",
		Text:    "合成X信息流帖子正文内容",
		PubTs:   time.Now().Unix() - 60,
		Author:  library.ItemAuthor{Site: "x", UID: "900002002", UniqueID: "synth_x_author", Name: "合成X作者"},
		Media:   []library.MediaFile{{Role: "image", Path: "img_1.jpg"}},
	}); err != nil {
		t.Fatal(err)
	}
	sub, err := stub.raw.CreateSubscription("合成X订阅", "", []subscription.AccountRef{{
		Site:     "x",
		UID:      "900002002",
		UniqueID: "synth_x_author",
		Name:     "合成X作者",
	}}, subscription.DefaultSubRules())
	if err != nil {
		t.Fatal(err)
	}
	app.HistoryItems = []storage.HistoryEntry{{
		Site:         "x",
		Kind:         "dynamic",
		ID:           "1900000000000000001",
		Title:        "合成X历史帖子",
		CanonicalURL: "https://x.com/synth_x_author/status/1900000000000000001",
	}, {
		Site:         "x",
		Kind:         "user",
		ID:           "900002002",
		Title:        "合成X作者",
		CanonicalURL: "https://x.com/i/user/900002002",
	}}
	app.Tasks = []download.Task{{
		TaskID:   "task-x-badge-1",
		Site:     "x",
		ItemKey:  xKey,
		TargetID: "1900000000000000001",
		Title:    "合成X下载任务",
		Status:   "running",
		Progress: 0.4,
	}}
	app.RefreshData()

	// Check 5-segment filter zero horizontal overflow at 720px, 1000px, and 1280px on Home and Library!
	for _, winW := range []int{720, 1000, 1280} {
		for _, tc := range []struct {
			route  string
			prefix string
		}{
			{RouteHome, "主页站点筛选："},
			{RouteLibrary, "媒体库站点筛选："},
		} {
			app.Navigate(tc.route)
			tt := ui.NewTester(app.View, winW, 860)
			tt.Frame()
			prevRight := float32(-1)
			for _, seg := range []string{"全部", "B 站", "抖音", "微博", "X"} {
				b, ok := tt.Find(tc.prefix + seg)
				if !ok {
					t.Fatalf("route=%s width=%d: missing site filter segment %q", tc.route, winW, seg)
				}
				if b.W <= 12 {
					t.Fatalf("route=%s width=%d: segment %q too narrow: W=%.1f", tc.route, winW, seg, b.W)
				}
				if b.X+b.W > float32(winW)+0.5 {
					t.Fatalf("route=%s width=%d: segment %q overflows viewport: right=%.1f > %d", tc.route, winW, seg, b.X+b.W, winW)
				}
				if prevRight >= 0 && b.X < prevRight-0.5 {
					t.Fatalf("route=%s width=%d: segment %q overlaps previous segment: X=%.1f < prevRight=%.1f", tc.route, winW, seg, b.X, prevRight)
				}
				prevRight = b.X + b.W
			}
		}
	}

	// Verify 6 prescribed site badge places and NEVER on subscription grid avatars:
	tt := ui.NewTester(app.View, 1280, 860)

	// Place 1: X User page header
	app.CurrentXUser = &service.XUserDTO{RestID: "900002002", ScreenName: "synth_x_author", Name: "合成X作者", StatusesCount: 12}
	app.CurrentRoute = RouteXUser
	tt.Frame()
	if _, ok := tt.Find("用户页站点标记"); !ok {
		t.Fatal("missing site badge place #1 (X user header)")
	}

	// Place 2: Subscription detail › Accounts + Subscriptions › All Accounts
	app.OpenSubscriptionDetail(sub)
	app.SubDetailTab = "accounts"
	tt.Frame()
	if _, ok := tt.Find("订阅详情账号站点：x:900002002"); !ok {
		t.Fatal("missing site badge place #2a (Subscription detail › Accounts)")
	}
	app.openSubscriptionAccounts()
	tt.Frame()
	if _, ok := tt.Find("订阅账号站点：x:900002002"); !ok {
		t.Fatal("missing site badge place #2b (Subscriptions › All Accounts)")
	}

	// Subscription grid avatars MUST NOT have site badge!
	app.SubDetailOpen = false
	app.SubAccountsOpen = false
	tt.Frame()
	if _, ok := tt.Find("订阅网格站点"); ok {
		t.Fatal("subscription grid avatar must NEVER show site badge")
	}

	// Place 3: Home feed post card author row
	app.Navigate(RouteHome)
	app.setFeedSiteFilter("x")
	tt.Frame()
	if _, ok := tt.Find("主页作者站点：" + xKey); !ok {
		t.Fatal("missing site badge place #3 (Home feed post card)")
	}

	// Place 4: Library card metadata row
	app.Navigate(RouteLibrary)
	app.LibrarySiteFilter = "x"
	app.reloadLibraryItems()
	tt.Frame()
	if _, ok := tt.Find("媒体库站点：" + xKey); !ok {
		t.Fatal("missing site badge place #4 (Library card)")
	}

	// Place 5: Task row
	app.Navigate(RouteTasks)
	app.TasksSection = "downloads"
	app.Tasks = []download.Task{{
		TaskID:   "task-x-badge-1",
		Site:     "x",
		ItemKey:  xKey,
		TargetID: "1900000000000000001",
		Title:    "合成X下载任务",
		Status:   "running",
		Progress: 0.4,
	}}
	tt.Frame()
	if _, ok := tt.Find("任务站点：task-x-badge-1"); !ok {
		t.Fatal("missing site badge place #5 (Task row)")
	}

	// Place 6: Recent parse sidebar entry + labels "帖子" & "X 用户"
	app.Navigate(RouteParse)
	app.HistoryItems = []storage.HistoryEntry{{
		Site:         "x",
		Kind:         "dynamic",
		ID:           "1900000000000000001",
		Title:        "合成X历史帖子",
		CanonicalURL: "https://x.com/synth_x_author/status/1900000000000000001",
	}, {
		Site:         "x",
		Kind:         "user",
		ID:           "900002002",
		Title:        "合成X作者",
		CanonicalURL: "https://x.com/i/user/900002002",
	}}
	tt.Frame()
	if _, ok := tt.Find("历史站点：x:1900000000000000001"); !ok {
		t.Fatal("missing site badge place #6 (Recent parse sidebar)")
	}
	if !tt.HasText("帖子") || !tt.HasText("X 用户") {
		t.Fatal("recent parse sidebar missing '帖子' or 'X 用户' kind label")
	}
}

// ============================================================================
// 3. X User View: Header, Pinned, Visible Limit, Protected, Fake-Shell Retry, Cache All
// ============================================================================

func TestXUserPageHeaderFilterVisibleLimitProtectedAndFakeShellRetry(t *testing.T) {
	app, stub, cleanup := newXUITestHarness(t)
	defer cleanup()

	uid := "900003003"
	stub.xUsers[uid] = &service.XUserDTO{
		RestID:         uid,
		ScreenName:     "synth_creator",
		Name:           "合成X博主",
		Description:    "合成个人简介文本",
		IsBlueVerified: true,
		Protected:      true,
		FriendsCount:   88,
		FollowersCount: 12340,
		StatusesCount:  456,
		CanonicalURL:   "https://x.com/synth_creator",
	}
	stub.xUserPages[uid] = map[string]xPageFixture{
		"": {
			page: service.XUserPostsPageDTO{
				Posts: []service.XPostDTO{{
					ID:        "1900000000000000101",
					Author:    service.XAuthorDTO{RestID: uid, ScreenName: "synth_creator", Name: "合成X博主"},
					CreatedAt: time.Now().Unix() - 120,
					Text:      "置顶合成帖子正文",
					Pinned:    true,
				}},
				NextCursor: "cur-2",
				HasMore:    true,
			},
		},
		"cur-2": {
			page: service.XUserPostsPageDTO{
				UncertainEmpty:  true,
				UncertainReason: "X 返回了异常空列表，请稍后重试",
			},
		},
	}

	app.OpenXUser(uid, "合成X博主")
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	tt.Frame()

	for _, want := range []string{"合成X博主", "@synth_creator", "已认证", "帖子 456", "合成个人简介文本", "受保护的账号", "置顶", "置顶合成帖子正文"} {
		if !tt.HasText(want) {
			t.Fatalf("X user page missing %q", want)
		}
	}

	// Page 2 returns fake-shell UncertainEmpty: already loaded post stays in place and retry button renders.
	app.LoadMoreXPosts()
	tt.Frame()
	if len(app.CurrentXPosts) != 1 {
		t.Fatalf("fake-shell error cleared loaded posts: got %d", len(app.CurrentXPosts))
	}
	if _, ok := tt.Find("X空壳提示"); !ok {
		t.Fatal("missing fake-shell error banner on X user page")
	}
	if _, ok := tt.Find("重试加载X帖子"); !ok {
		t.Fatal("missing retry button on fake-shell error")
	}

	// Retry succeeds with visible limit reached.
	stub.mu.Lock()
	stub.xUserPages[uid]["cur-2"] = xPageFixture{
		page: service.XUserPostsPageDTO{
			Posts: []service.XPostDTO{{
				ID:        "1900000000000000102",
				Author:    service.XAuthorDTO{RestID: uid, ScreenName: "synth_creator", Name: "合成X博主"},
				CreatedAt: time.Now().Unix() - 240,
				Text:      "第二页合成帖子",
			}},
			ReachedVisibleLimit: true,
			HasMore:             false,
		},
	}
	stub.mu.Unlock()

	if err := tt.Click("重试加载X帖子"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("已到达 X 时间线可见上限") {
		t.Fatal("missing X visible limit bottom hint")
	}

	// "⋯ → 全部缓存" enqueues X cache queue.
	app.enqueueCacheAll("x")
	tt.Frame()
	stub.mu.Lock()
	qLen := len(stub.queuedReqs)
	var qReq service.CacheQueueRequest
	if qLen > 0 {
		qReq = stub.queuedReqs[0]
	}
	stub.mu.Unlock()
	if qLen != 1 || qReq.Site != "x" || qReq.List != "posts" || len(qReq.XPosts) != 2 {
		t.Fatalf("unexpected X cache queue request: n=%d req=%+v", qLen, qReq)
	}
}

// ============================================================================
// 4. X Post View (Online & Offline): Mixed Media, GIF Muted Loop, HLS-only,
//    Degraded Cards (Poll/Link/Article/Live-Spaces), Retweet/Quote, Thread
// ============================================================================

func TestXPostViewOnlineAndOfflineMediaGIFLoopHLSDegradedCardsAndThread(t *testing.T) {
	app, stub, cleanup := newXUITestHarness(t)
	defer cleanup()

	// 4a. Online rich X post: note_tweet, 4 mixed media (photo + GIF + video), edit badge,
	// visibility note, quote with deleted origin, self-reply thread, poll, link card, online stats.
	post := &service.XPostDTO{
		ID:                  "1900000000000000201",
		Author:              service.XAuthorDTO{RestID: "900004004", ScreenName: "synth_writer", Name: "合成作者"},
		CreatedAt:           time.Now().Unix() - 300,
		Text:                "超长合成 note_tweet 完整正文内容，不截断。",
		IsLongText:          true,
		IsEdited:            true,
		VisibilityLabel:     "仅关注者可回复",
		PossiblySensitive:   true,
		WithheldInCountries: []string{"XX"},
		CanonicalURL:        "https://x.com/synth_writer/status/1900000000000000201",
		Media: []service.XMediaItemDTO{
			{Index: 1, Kind: "photo", ImageURL: "https://pbs.twimg.com/media/synth1.jpg?name=orig", Width: 1200, Height: 900},
			{Index: 2, Kind: "gif", GIF: true, ImageURL: "https://pbs.twimg.com/tweet_video_thumb/synth2.jpg", VideoURL: "https://video.twimg.com/tweet_video/synth2.mp4", Width: 640, Height: 360},
			{Index: 3, Kind: "video", ImageURL: "https://pbs.twimg.com/ext_tw_video_thumb/synth3.jpg", VideoURL: "https://video.twimg.com/ext_tw_video/synth3.mp4", DurationSec: 45, Width: 1280, Height: 720},
			{Index: 4, Kind: "photo", ImageURL: "https://pbs.twimg.com/media/synth4.jpg?name=orig", Width: 1000, Height: 1000},
		},
		Poll: &service.XPollSummaryDTO{
			Options:    []service.XPollOptionDTO{{Position: 1, Label: "选项甲", Votes: 70}, {Position: 2, Label: "选项乙", Votes: 30}},
			TotalVotes: 100,
			Final:      true,
		},
		Card: &service.XRichCardDTO{
			Kind:  "article",
			Title: "合成 X Articles 长文标题",
			Desc:  "文章摘要预览内容",
			URL:   "https://x.com/i/article/999",
		},
		Quoted: &service.XPostDTO{
			ID:                "1900000000000000200",
			Unavailable:       true,
			UnavailableReason: "原帖已删除或受保护，无法查看",
		},
		Thread: []service.XPostDTO{{
			ID:        "1900000000000000202",
			Author:    service.XAuthorDTO{RestID: "900004004", ScreenName: "synth_writer", Name: "合成作者"},
			CreatedAt: time.Now().Unix() - 200,
			Text:      "作者自回复第二条正文",
		}},
		Stats: service.XStatsDTO{ReplyCount: 12, RetweetCount: 34, LikeCount: 567, QuoteCount: 5, BookmarkCount: 89, ViewCount: 12345},
	}
	app.CurrentXPost = post
	app.CurrentLibraryDetail = nil
	app.CurrentRoute = RouteXPost

	tt := ui.NewTester(app.View, 1280, 1200)
	tt.Frame()

	for _, want := range []string{
		"合成作者", "@synth_writer", "已编辑", "仅关注者可回复",
		"超长合成 note_tweet 完整正文内容，不截断。",
		"GIF", "选项甲", "合成 X Articles 长文标题",
		"原帖已删除或受保护，无法查看",
		"作者自回复第二条正文",
	} {
		if !tt.HasText(want) {
			t.Fatalf("online X post detail missing %q", want)
		}
	}
	if _, ok := tt.Find("X作者自回复串"); !ok {
		t.Fatal("online X post detail missing X作者自回复串 container")
	}
	if _, ok := tt.Find("X串连线：1900000000000000201"); !ok {
		t.Fatal("online X post detail missing connector line under focal tweet")
	}
	if _, ok := tt.Find("X串连线：1900000000000000202"); ok {
		t.Fatal("online X post detail must NOT draw connector line under final thread tweet")
	}
	if _, ok := tt.Find("X在线统计"); !ok {
		t.Fatal("online X post detail missing online stats row")
	}

	// Opening the GIF page (index 1) in ImageViewer marks InlineLoopMuted[1] = true!
	pages := xMediaToNotePagesScoped(post.Media, nil, false, false)
	app.openNotePagesViewer("", "x", pages, 1)
	if !app.viewerInlinePageIsLoopMuted(1) {
		t.Fatal("expected GIF page (index 1) to enable muted loop playback in ImageViewer")
	}
	if app.viewerInlinePageIsLoopMuted(0) || app.viewerInlinePageIsLoopMuted(2) {
		t.Fatal("non-GIF pages must not set InlineLoopMuted")
	}
	app.closeImageViewer()

	// 4b. HLS-only video post MUST show notice and MUST NOT show a broken cache button!
	app.CurrentXPost = &service.XPostDTO{
		ID:              "1900000000000000203",
		Author:          service.XAuthorDTO{RestID: "900004004", ScreenName: "synth_writer", Name: "合成作者"},
		Text:            "纯分段流视频帖",
		HasHLSOnlyVideo: true,
		CanonicalURL:    "https://x.com/synth_writer/status/1900000000000000203",
		Media: []service.XMediaItemDTO{{
			Index:         1,
			Kind:          "video",
			HLSOnly:       true,
			HLSOnlyNotice: "这条视频只有分段流，暂不支持缓存",
		}},
	}
	tt.Frame()
	if _, ok := tt.Find("X分段流提示"); !ok || !tt.HasText("这条视频只有分段流，暂不支持缓存") {
		t.Fatal("HLS-only X post missing notice")
	}
	if _, ok := tt.Find("缓存当前X帖子"); ok {
		t.Fatal("HLS-only X post must NOT show a broken cache button")
	}

	// 4c. Live / Spaces degraded card MUST show notice + open in browser, NO video cache button!
	app.CurrentXPost = &service.XPostDTO{
		ID:           "1900000000000000204",
		Author:       service.XAuthorDTO{RestID: "900004004", ScreenName: "synth_writer", Name: "合成作者"},
		Text:         "正在开 Spaces 语音直播",
		CanonicalURL: "https://x.com/i/spaces/synth_space_1",
		Card: &service.XRichCardDTO{
			Kind:        "live",
			Title:       "合成 Spaces 直播间",
			URL:         "https://x.com/i/spaces/synth_space_1",
			Unsupported: true,
		},
	}
	tt.Frame()
	if !tt.HasText("X 直播与 Spaces 暂不支持录制，可以在浏览器中打开。") {
		t.Fatal("live/Spaces X post missing non-recording notice")
	}
	if _, ok := tt.Find("X卡片浏览器打开：live"); !ok {
		t.Fatal("live/Spaces X post missing open-in-browser button")
	}
	if _, ok := tt.Find("缓存当前X帖子"); ok {
		t.Fatal("live/Spaces X post must NOT show a video cache button")
	}

	// 4d. Offline library detail for X: thread, origin box, GIF muted loop, HLS-only missing notice, "X" badge, ZERO online stats & ZERO network calls!
	callsBefore := stub.xCalls.Load()
	offlineItem := library.ItemMetadata{
		ItemKey: "x:dynamic:1900000000000000205",
		Site:    "x",
		Kind:    "dynamic",
		ID:      "1900000000000000205",
		RawID:   "1900000000000000205",
		Title:   "离线X帖子",
		Text:    "离线缓存的 X 帖子完整正文",
		PubTs:   time.Now().Unix() - 1000,
		Author:  library.ItemAuthor{Site: "x", UID: "900004004", UniqueID: "synth_writer", Name: "合成作者"},
		NotePages: []library.NotePageRecord{{
			Index: 1, Kind: "video", GIF: true, Video: "media/gif1.mp4", Cover: "media/gif1.jpg",
		}},
		Origin: &library.ItemOrigin{
			Site:   "x",
			ID:     "1900000000000000199",
			Author: library.ItemAuthor{Site: "x", UID: "900005005", UniqueID: "orig_x_user", Name: "原帖作者甲"},
			Text:   "离线原帖正文内容",
		},
		Thread: []library.ItemThreadPart{{
			ID:    "1900000000000000206",
			PubTs: time.Now().Unix() - 900,
			Text:  "离线保存的作者自回复串内容",
		}},
		Missing: []library.MissingRecord{{
			Item:   "hls_video_2",
			Reason: "这条视频只有分段流，暂不支持缓存",
		}},
	}
	app.OpenLibraryItemDetail(offlineItem)
	tt.Frame()

	for _, want := range []string{
		"离线缓存的 X 帖子完整正文",
		"@synth_writer",
		"这条视频只有分段流，暂不支持缓存",
		"原帖作者甲", "@orig_x_user", "离线原帖正文内容",
		"离线保存的作者自回复串内容",
	} {
		if !tt.HasText(want) {
			t.Fatalf("offline X detail missing %q", want)
		}
	}
	if _, ok := tt.Find("本机详情站点标记"); !ok {
		t.Fatal("offline X detail missing 'X' site badge")
	}
	if _, ok := tt.Find("X在线统计"); ok {
		t.Fatal("offline X detail MUST NOT show online stats")
	}
	if stub.xCalls.Load() != callsBefore {
		t.Fatalf("offline X detail made %d network calls (want 0)", stub.xCalls.Load()-callsBefore)
	}
	if !isGIFLibrarySegment(offlineItem, "media/gif1.mp4") {
		t.Fatal("expected offline GIF segment to be recognized for muted loop playback")
	}
}

// ============================================================================
// 5. Mine › X, Subscriptions No-Live, Check Plan & X Follow Import (Cursor)
// ============================================================================

func TestXMineSubscriptionsNoLiveCheckPlanAndFollowImport(t *testing.T) {
	app, stub, cleanup := newXUITestHarness(t)
	defer cleanup()

	// 5a. Mine › X unlogged state
	stub.mineSections["x"] = []service.AccountSection{{Site: "x", Available: false, Reason: "未登录"}}
	app.OpenMine("x", service.AccountBookmarks)
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	if !tt.HasText("请先登录 X 后才能查看") {
		t.Fatal("Mine › X unlogged missing '请先登录 X' prompt")
	}

	// 5b. Mine › X logged-in: 关注 / 书签 / 喜欢 (no folders/lists when switches off)
	stub.mu.Lock()
	stub.xStatus = credential.Status{Site: string(site.X), LoggedIn: true, State: "logged_in", UID: "900001001", UniqueID: "synth_x_me", Uname: "合成X号"}
	stub.mineSections["x"] = []service.AccountSection{
		{Site: "x", Kind: service.AccountFollowing, Label: "关注", Available: true},
		{Site: "x", Kind: service.AccountBookmarks, Label: "书签", Available: true},
		{Site: "x", Kind: service.AccountLikes, Label: "喜欢", Available: true},
	}
	stub.minePages["x/bookmarks|"] = service.AccountPage{
		HasMore: false,
		Items: []service.AccountItem{{
			Kind:       service.AccountItemPost,
			Site:       "x",
			ID:         "1900000000000000301",
			Title:      "书签里的合成X帖子",
			Author:     "书签作者",
			AuthorID:   "900006006",
			BrowserURL: "https://x.com/bookmark_author/status/1900000000000000301",
			XPost: &service.XPostDTO{
				ID:           "1900000000000000301",
				Author:       service.XAuthorDTO{RestID: "900006006", ScreenName: "bookmark_author", Name: "书签作者"},
				Text:         "书签里的合成X帖子正文",
				CanonicalURL: "https://x.com/bookmark_author/status/1900000000000000301",
			},
		}},
	}
	stub.minePages["x/likes|"] = service.AccountPage{HasMore: false, Items: nil}
	stub.mineErrors["x/following|"] = &site.Error{Site: site.X, Kind: site.ErrKindRiskControl, Message: "locked"}
	stub.mu.Unlock()

	app.mineClearSite("x")
	app.OpenMine("x", service.AccountBookmarks)
	tt.Frame()
	tt.Frame()
	if _, ok := tt.Find("我的功能：X 列表"); ok {
		t.Fatal("Mine › X must not show Lists when disabled")
	}
	if _, ok := tt.Find("我的没有更多"); !ok || !tt.HasText("书签里的合成X帖子正文") || !tt.HasText("@bookmark_author") {
		t.Fatal("Mine › X bookmarks row or end state missing")
	}

	// Empty Likes
	app.OpenMine("x", service.AccountLikes)
	tt.Frame()
	tt.Frame()
	if _, ok := tt.Find("我的列表为空"); !ok {
		t.Fatal("Mine › X empty likes missing empty state")
	}

	// Risk-control error on Following
	app.OpenMine("x", service.AccountFollowing)
	tt.Frame()
	tt.Frame()
	if _, ok := tt.Find("我的提示：X 要求验证或账号被临时锁定，TRACE 已停止 X 的全部自动请求，请先在浏览器里处理"); !ok {
		t.Fatal("Mine › X risk-control error missing exact FR-ERR-001 copy")
	}

	// 5c. Subscriptions no-live row + Check Plan + accountWorks noun "帖子"
	sub, err := stub.raw.CreateSubscription("合成X创作者", "", []subscription.AccountRef{{
		Site:     "x",
		UID:      "900007007",
		UniqueID: "synth_sub_x",
		Name:     "合成X创作者",
	}}, subscription.DefaultSubRules())
	if err != nil {
		t.Fatal(err)
	}
	stub.mu.Lock()
	stub.accountWorks = map[string]service.AccountWorks{
		"x:900007007": {Total: 88, Cached: 5},
	}
	stub.checkPlan = []service.AccountCheckRow{{
		SubID:      sub.ID,
		SubName:    sub.Name,
		Site:       "x",
		UID:        "900007007",
		Name:       "合成X创作者",
		AutoRecord: true,
		LiveNote:   "X 直播与 Spaces 暂不支持录制",
		Plan:       subscription.PlanScheduled,
	}}
	stub.mu.Unlock()
	app.RefreshData()

	liveBefore := stub.liveCalls.Load()
	app.OpenSubscriptionDetail(sub)
	app.SubDetailTab = "accounts"
	app.refreshVisibleLive()
	tt.Frame()
	if stub.liveCalls.Load() != liveBefore {
		t.Fatalf("X-only subscription triggered %d live status calls (want 0)", stub.liveCalls.Load()-liveBefore)
	}
	if tt.HasText("未开播") || tt.HasText("直播中") {
		t.Fatal("X subscription account row must NOT show live status")
	}
	if _, ok := tt.Find("开始录制 x:900007007"); ok {
		t.Fatal("X subscription account row must NOT show record button")
	}
	if !tt.HasText("@synth_sub_x") || !tt.HasText("帖子 88 · 已缓存 5") {
		t.Fatal("X subscription account row missing @handle or '帖子' noun")
	}

	app.OpenSubscriptionChecks()
	tt.Frame()
	wantCap := fmt.Sprintf("X %d 个", subscription.DefaultXRoundAccountCap)
	if !tt.HasText(wantCap) {
		t.Fatalf("Check Plan head missing %q", wantCap)
	}
	if !tt.HasText("X 直播与 Spaces 暂不支持录制") {
		t.Fatal("Check Plan row missing X LiveNote")
	}

	// 5d. X Follow Import (cursor pagination, no groups, already-subscribed disabled)
	stub.mu.Lock()
	stub.followCursor["x|"] = service.FollowingCreatorsPageDTO{
		Site:       "x",
		NextCursor: "x-cur-2",
		HasMore:    true,
		Items: []service.FollowingCreatorDTO{
			{Site: "x", UID: "900007007", UniqueID: "synth_sub_x", Name: "合成X创作者", Subscribed: true, OwnerSubName: "合成X创作者"},
			{Site: "x", UID: "900008008", UniqueID: "synth_new_x1", Name: "未订阅X作者1", Sign: "简介1"},
		},
	}
	stub.followCursor["x|x-cur-2"] = service.FollowingCreatorsPageDTO{
		Site:    "x",
		HasMore: false,
		Items: []service.FollowingCreatorDTO{
			{Site: "x", UID: "900009009", UniqueID: "synth_new_x2", Name: "未订阅X作者2", Sign: "简介2"},
		},
	}
	stub.mu.Unlock()

	app.ImportSubModalOpen = true
	app.selectImportSite("x")
	tt.Frame()
	tt.Frame()

	if _, ok := tt.Find("关注分组"); ok {
		t.Fatal("X follow import must NOT show group dropdown")
	}
	if !tt.HasText("已在订阅「合成X创作者」中") {
		t.Fatal("already-subscribed X creator must show disabled owned notice")
	}
	if _, ok := tt.Find("勾选导入 900007007"); ok {
		t.Fatal("already-subscribed X creator must NOT have a selectable toggle")
	}
	if err := tt.Click("加载下一页关注"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("未订阅X作者2") || !tt.HasText("@synth_new_x2") {
		t.Fatal("cursor pagination did not load page 2 of X followings")
	}
	app.ImportSubModalOpen = false
}

// ============================================================================
// 6. Remote X Manual Push Confirmation Modal (0 req on cancel, per-push only)
// ============================================================================

func TestXRemoteManualPushRequiresPerPushRiskConfirmationModal(t *testing.T) {
	app, stub, cleanup := newXUITestHarness(t)
	defer cleanup()

	stub.mu.Lock()
	stub.xStatus = credential.Status{Site: string(site.X), LoggedIn: true, State: "logged_in", UID: "900001001", UniqueID: "synth_x_me", Uname: "合成X号"}
	stub.remoteInfo = &service.RemoteConnectionInfo{URL: "https://remote.example.test"}
	stub.remoteView = &service.RemoteView{
		Status: remote.Status{
			Sites: []remote.SiteStatus{
				{Site: "bilibili", State: remote.SiteValid, Uname: "B站号"},
				{Site: "x", State: remote.SiteNone},
			},
		},
	}
	stub.mu.Unlock()

	app.SettingsCategory = "remote"
	app.Navigate(RouteSettings)
	app.Remote.View = stub.remoteView
	app.Remote.FetchedAt = time.Now()

	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()

	if !tt.HasText("X") {
		t.Fatal("Remote settings missing X site row")
	}

	// Click 1: opens confirmation modal, sends 0 push requests!
	if err := tt.Click("同步Cookie到远程：x"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !app.Remote.ConfirmPushXOpen {
		t.Fatal("clicking Push Cookie on X did not open confirmation modal")
	}
	if !tt.HasText("在云服务器的网络上用网页 Cookie 访问 X") {
		t.Fatal("X remote push confirmation modal missing VPS datacenter IP risk disclosure")
	}
	if stub.pushCalls["x"] != 0 {
		t.Fatalf("unconfirmed click sent %d push requests (want 0)", stub.pushCalls["x"])
	}

	// Cancel: closes modal and sends 0 push requests!
	if err := tt.Click("取消同步XCookie到远程"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if app.Remote.ConfirmPushXOpen {
		t.Fatal("cancel did not close X push confirmation modal")
	}
	if stub.pushCalls["x"] != 0 {
		t.Fatalf("cancelling modal sent %d push requests (want 0)", stub.pushCalls["x"])
	}

	// Click 2 + Confirm: sends exactly 1 push request!
	if err := tt.Click("同步Cookie到远程：x"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("确认同步XCookie到远程"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if stub.pushCalls["x"] != 1 {
		t.Fatalf("confirmed push sent %d requests (want 1)", stub.pushCalls["x"])
	}

	// Click 3: confirmation was valid for THAT single push only; must re-open modal and send 0 new requests!
	if err := tt.Click("同步Cookie到远程：x"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !app.Remote.ConfirmPushXOpen {
		t.Fatal("second push attempt must require explicit re-confirmation")
	}
	if stub.pushCalls["x"] != 1 {
		t.Fatalf("second unconfirmed click sent push immediately: got %d, want 1", stub.pushCalls["x"])
	}
}

// ============================================================================
// 7. Part B Performance Fixes: Virtualized Downloads, libraryRev queueCovers,
//    finishTaskAction refreshTasks, Off-Route EventLibraryChanged,
//    lookupTaskDetailCached UpdatedAt Invalidation & Deduplicated AccountWorks
// ============================================================================

type perfCountingBackend struct {
	service.BackendAPI
	mu            sync.Mutex
	metaOnlyCalls int
	fullItemCalls int
	listLibCalls  int
	historyCalls  int
	statsCalls    int
	subsCalls     int
	worksCalls    int
	taskListCalls int
	detailCalls   int
	details       map[string]download.TaskDetail
}

func (b *perfCountingBackend) GetLibraryItem(key string) (library.ItemMetadata, bool, error) {
	b.mu.Lock()
	b.fullItemCalls++
	b.mu.Unlock()
	return b.BackendAPI.GetLibraryItem(key)
}

func (b *perfCountingBackend) GetLibraryItemMetaOnly(key string) (library.ItemMetadata, bool, error) {
	b.mu.Lock()
	b.metaOnlyCalls++
	b.mu.Unlock()
	return b.BackendAPI.GetLibraryItemMetaOnly(key)
}

func (b *perfCountingBackend) ListLibraryItems(f library.FeedFilter) ([]library.ItemMetadata, error) {
	b.mu.Lock()
	b.listLibCalls++
	b.mu.Unlock()
	return b.BackendAPI.ListLibraryItems(f)
}

func (b *perfCountingBackend) ListHistory() []storage.HistoryEntry {
	b.mu.Lock()
	b.historyCalls++
	b.mu.Unlock()
	return b.BackendAPI.ListHistory()
}

func (b *perfCountingBackend) LibraryStatistics() (library.Statistics, error) {
	b.mu.Lock()
	b.statsCalls++
	b.mu.Unlock()
	return b.BackendAPI.LibraryStatistics()
}

func (b *perfCountingBackend) ListSubscriptions() ([]subscription.Subscription, error) {
	b.mu.Lock()
	b.subsCalls++
	b.mu.Unlock()
	return b.BackendAPI.ListSubscriptions()
}

func (b *perfCountingBackend) SubscriptionAccountWorks() map[string]service.AccountWorks {
	b.mu.Lock()
	b.worksCalls++
	b.mu.Unlock()
	return b.BackendAPI.SubscriptionAccountWorks()
}

func (b *perfCountingBackend) ListDownloadTasks() ([]download.Task, error) {
	b.mu.Lock()
	b.taskListCalls++
	b.mu.Unlock()
	return b.BackendAPI.ListDownloadTasks()
}

func (b *perfCountingBackend) TaskDetail(taskID string) (download.TaskDetail, bool, error) {
	b.mu.Lock()
	b.detailCalls++
	d, ok := b.details[taskID]
	b.mu.Unlock()
	if ok {
		return d, true, nil
	}
	return b.BackendAPI.TaskDetail(taskID)
}

func TestPerfVirtualizedDownloadsQueueCoversOffRouteLibraryAndFinishTaskAction(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()

	seedUIFeed(t, app, 10)
	perf := &perfCountingBackend{
		BackendAPI: app.backend,
		details:    map[string]download.TaskDetail{},
	}
	app.backend = perf

	// 1. RefreshData deduplicates SubscriptionAccountWorks() (called ONCE, not twice!).
	perf.worksCalls = 0
	app.RefreshData()
	if perf.worksCalls != 1 {
		t.Fatalf("RefreshData called SubscriptionAccountWorks %d times (want 1)", perf.worksCalls)
	}

	// 2. Home card building uses GetLibraryItemMetaOnly (0 full GetLibraryItem calls!).
	perf.fullItemCalls = 0
	perf.metaOnlyCalls = 0
	app.Navigate(RouteHome)
	app.postCards = nil
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	if perf.fullItemCalls != 0 {
		t.Fatalf("Home card rendering called full GetLibraryItem %d times (want 0)", perf.fullItemCalls)
	}
	if perf.metaOnlyCalls == 0 {
		t.Fatal("expected Home card rendering to use GetLibraryItemMetaOnly")
	}

	// 3. Off-route EventLibraryChanged skips reloadLibraryItems and lazily reloads on navigating to /library.
	perf.listLibCalls = 0
	app.handleBackendEvent(service.Event{Type: service.EventLibraryChanged})
	if perf.listLibCalls != 0 {
		t.Fatalf("off-route EventLibraryChanged called ListLibraryItems %d times (want 0)", perf.listLibCalls)
	}
	if !app.libraryItemsDirty {
		t.Fatal("expected libraryItemsDirty = true after off-route EventLibraryChanged")
	}
	app.Navigate(RouteLibrary)
	if perf.listLibCalls == 0 {
		t.Fatal("navigating to /library did not lazily reload dirty library items")
	}
	if app.libraryItemsDirty {
		t.Fatal("expected libraryItemsDirty = false after entering /library")
	}

	// 4. Virtualized Downloads list with 320 tasks renders only visible subset (< 40 rows), keeping toolbar accessible!
	tasks := make([]download.Task, 320)
	for i := range tasks {
		st := "completed"
		if i == 0 {
			st = "failed"
		}
		tasks[i] = download.Task{
			TaskID:    fmt.Sprintf("perf-task-%04d", i),
			Site:      "x",
			ItemKey:   fmt.Sprintf("x:dynamic:%04d", i),
			TargetID:  fmt.Sprintf("1900000000%04d", i),
			Title:     fmt.Sprintf("批量任务 #%04d", i),
			Status:    st,
			Progress:  1.0,
			UpdatedAt: "2026-11-22T01:00:00Z",
		}
	}
	app.Navigate(RouteTasks)
	app.TasksSection = "downloads"
	app.Tasks = tasks
	tt.Frame()

	if _, ok := tt.Find("批量重试失败或已暂停的任务"); !ok {
		t.Fatal("pinned batch retry button missing on virtualized downloads view")
	}
	if _, ok := tt.Find("清除已结束的任务"); !ok {
		t.Fatal("pinned remove finished button missing on virtualized downloads view")
	}
	renderedCards := 0
	for i := range tasks {
		if _, ok := tt.Find("展开任务详情：" + tasks[i].TaskID); ok {
			renderedCards++
		}
	}
	if renderedCards == 0 || renderedCards >= 40 {
		t.Fatalf("expected virtualized Downloads list to render small visible subset (1..39), got %d of %d", renderedCards, len(tasks))
	}

	// 5. queueCovers survives 1s EventTaskUpdated ticks (keyed on libraryRev), refreshes on EventLibraryChanged!
	app.queueCovers = nil
	perf.metaOnlyCalls = 0
	app.queueRecentCover("x:dynamic:0000")
	afterFirst := perf.metaOnlyCalls
	if afterFirst == 0 {
		t.Fatal("expected initial queueRecentCover to query GetLibraryItemMetaOnly")
	}
	for i := 0; i < 10; i++ {
		app.handleBackendEvent(service.Event{Type: service.EventTaskUpdated})
		app.queueRecentCover("x:dynamic:0000")
	}
	if perf.metaOnlyCalls != afterFirst {
		t.Fatalf("queueCovers was wiped on EventTaskUpdated ticks: metaOnlyCalls grew from %d to %d", afterFirst, perf.metaOnlyCalls)
	}
	app.handleBackendEvent(service.Event{Type: service.EventLibraryChanged})
	app.queueRecentCover("x:dynamic:0000")
	if perf.metaOnlyCalls <= afterFirst {
		t.Fatal("queueCovers did not invalidate after EventLibraryChanged")
	}

	// 6. finishTaskAction calls refreshTasks() ONLY (NOT full RefreshData()!).
	perf.historyCalls = 0
	perf.statsCalls = 0
	perf.subsCalls = 0
	perf.taskListCalls = 0
	app.finishTaskAction(nil)
	if perf.taskListCalls != 1 {
		t.Fatalf("finishTaskAction taskListCalls = %d, want 1", perf.taskListCalls)
	}
	if perf.historyCalls != 0 || perf.statsCalls != 0 || perf.subsCalls != 0 {
		t.Fatalf("finishTaskAction triggered full RefreshData work: history=%d stats=%d subs=%d", perf.historyCalls, perf.statsCalls, perf.subsCalls)
	}

	// 7. lookupTaskDetailCached invalidates on UpdatedAt / Status / BytesDownloaded even when summary task.Files is empty.
	perf.detailCalls = 0
	perf.details["perf-task-0001"] = download.TaskDetail{
		Task:  download.Task{TaskID: "perf-task-0001"},
		Files: []download.TaskFileProgress{{Role: "video", Path: "v.mp4", Status: "running", DownloadedBytes: 100, TotalBytes: 1000}},
	}
	summary := download.Task{TaskID: "perf-task-0001", Status: "running", BytesDownloaded: 100, UpdatedAt: "2026-11-22T01:00:01Z"}
	_, ok := app.lookupTaskDetailCached(summary)
	if !ok || perf.detailCalls != 1 {
		t.Fatalf("first lookupTaskDetailCached: ok=%v calls=%d", ok, perf.detailCalls)
	}
	// Same summary again -> cache hit (0 new calls).
	_, _ = app.lookupTaskDetailCached(summary)
	if perf.detailCalls != 1 {
		t.Fatalf("unchanged summary re-queried TaskDetail: calls=%d", perf.detailCalls)
	}
	// UpdatedAt changes (while len(Files) stays 0) -> cache invalidated!
	summary.UpdatedAt = "2026-11-22T01:00:02Z"
	_, _ = app.lookupTaskDetailCached(summary)
	if perf.detailCalls != 2 {
		t.Fatalf("UpdatedAt change did not invalidate TaskDetail cache: calls=%d", perf.detailCalls)
	}
}
