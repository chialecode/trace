package ui

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/storage"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

type douyinStubBackend struct {
	service.BackendAPI
	raw *service.Backend

	mu             sync.Mutex
	dyStatus       credential.Status
	loginGate      chan struct{}
	loginMode      string // "ok" | "missing_session" | "unauth" | "verify"
	douyinNetCalls atomic.Int32

	users      map[string]*service.DouyinUserView
	userPages  map[string]map[string]douyinPageFixture
	works      map[string]*service.DouyinWorkView
	lives      map[string]*service.DouyinLiveView
	liveStates []service.LiveAccountState
	schedulers map[string]service.SiteSchedulerStatus
	cachedWork string
}

type douyinPageFixture struct {
	page service.DouyinUserWorksPage
	err  error
}

func newDouyinUITestHarness(t *testing.T) (*App, *douyinStubBackend, *atomic.Int32, func()) {
	t.Helper()
	baseApp, _, baseCleanup := newTestUIApp(t)
	rawBackend := baseApp.backend.(*service.Backend)

	var biliNetCalls atomic.Int32
	stub := &douyinStubBackend{
		BackendAPI: rawBackend,
		raw:        rawBackend,
		dyStatus: credential.Status{
			Site:  string(site.Douyin),
			State: "logged_out",
		},
		loginMode: "ok",
		users:     make(map[string]*service.DouyinUserView),
		userPages: make(map[string]map[string]douyinPageFixture),
		works:     make(map[string]*service.DouyinWorkView),
		lives:     make(map[string]*service.DouyinLiveView),
	}
	baseApp.backend = stub
	baseApp.LoginSite = string(site.Bilibili)
	return baseApp, stub, &biliNetCalls, baseCleanup
}

func (s *douyinStubBackend) Statuses() map[string]credential.Status {
	s.mu.Lock()
	dy := s.dyStatus
	s.mu.Unlock()
	bili := s.BackendAPI.StatusOf(string(site.Bilibili))
	return map[string]credential.Status{
		string(site.Bilibili): bili,
		string(site.Douyin):   dy,
	}
}

func (s *douyinStubBackend) StatusOf(siteID string) credential.Status {
	if strings.EqualFold(strings.TrimSpace(siteID), string(site.Douyin)) {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.dyStatus
	}
	return s.BackendAPI.StatusOf(siteID)
}

func (s *douyinStubBackend) HasAnyLogin() bool {
	if s.BackendAPI.StatusOf(string(site.Bilibili)).LoggedIn {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dyStatus.LoggedIn
}

func (s *douyinStubBackend) MediaPath(key, relPath string) (string, error) {
	if !s.HasAnyLogin() {
		return "", &site.Error{Kind: site.ErrKindAuthRequired, Message: "请先登录账号"}
	}
	return s.raw.LibraryStore().MediaPath(key, relPath)
}

func (s *douyinStubBackend) LoginWithCookie(ctx context.Context, siteID, rawCookie, rawRefreshToken string) (credential.Status, error) {
	if !strings.EqualFold(strings.TrimSpace(siteID), string(site.Douyin)) {
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
	case "missing_session":
		return credential.Status{}, &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindInvalidInput,
			Message: "未检测到 sessionid",
		}
	case "unauth":
		s.douyinNetCalls.Add(1)
		return credential.Status{}, &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindAuthRequired,
			Message: "profile/self returned unauthenticated",
		}
	case "verify":
		s.douyinNetCalls.Add(1)
		return credential.Status{}, &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindRiskControl,
			Message: "captcha required",
		}
	default:
		s.douyinNetCalls.Add(1)
		st := credential.Status{
			Site:     string(site.Douyin),
			LoggedIn: true,
			State:    "logged_in",
			Source:   "cookie",
			UID:      "900001",
			SecUID:   "MS4wLjABAAAA_synth_self_user_0001",
			UniqueID: "trace_dy_demo",
			Uname:    "抖音测试账号",
		}
		s.mu.Lock()
		s.dyStatus = st
		s.mu.Unlock()
		return st, nil
	}
}

func (s *douyinStubBackend) LogoutSite(siteID string) (credential.Status, error) {
	if strings.EqualFold(strings.TrimSpace(siteID), string(site.Douyin)) {
		st := credential.Status{
			Site:  string(site.Douyin),
			State: "logged_out",
		}
		s.mu.Lock()
		s.dyStatus = st
		s.mu.Unlock()
		return st, nil
	}
	return s.BackendAPI.LogoutSite(siteID)
}

func (s *douyinStubBackend) SchedulerStatuses() map[string]service.SiteSchedulerStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.schedulers) > 0 {
		out := make(map[string]service.SiteSchedulerStatus, len(s.schedulers))
		for k, v := range s.schedulers {
			out[k] = v
		}
		return out
	}
	return s.BackendAPI.SchedulerStatuses()
}

func (s *douyinStubBackend) ListLiveAccounts() []service.LiveAccountState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.liveStates) > 0 {
		out := make([]service.LiveAccountState, len(s.liveStates))
		copy(out, s.liveStates)
		return out
	}
	return s.BackendAPI.ListLiveAccounts()
}

func (s *douyinStubBackend) LiveStatuses(ctx context.Context, accountKeys []string) ([]service.LiveAccountState, error) {
	s.mu.Lock()
	if len(s.liveStates) > 0 {
		out := make([]service.LiveAccountState, len(s.liveStates))
		copy(out, s.liveStates)
		s.mu.Unlock()
		return out, nil
	}
	s.mu.Unlock()
	return s.BackendAPI.LiveStatuses(ctx, accountKeys)
}

func (s *douyinStubBackend) PreviewAccount(ctx context.Context, input string) (subscription.AccountRef, *subscription.Subscription, error) {
	trimmed := strings.TrimSpace(input)
	if strings.Contains(trimmed, "douyin.com") || strings.HasPrefix(trimmed, "MS4wLjAB") {
		if !s.StatusOf(string(site.Douyin)).LoggedIn {
			return subscription.AccountRef{}, nil, &site.Error{
				Site:    site.Douyin,
				Kind:    site.ErrKindAuthRequired,
				Message: "请先登录抖音账号",
			}
		}
		s.douyinNetCalls.Add(1)
		secUID := "MS4wLjABAAAA_synth_creator_alpha"
		if strings.HasPrefix(trimmed, "MS4wLjAB") {
			secUID = trimmed
		}
		acct := subscription.AccountRef{
			Site:     string(site.Douyin),
			UID:      secUID,
			UniqueID: "dy_creator_alpha",
			WebRID:   "88001122",
			Name:     "抖音影像创作者",
		}
		owner, ok := s.BackendAPI.OwnerOfAccount(string(site.Douyin), secUID)
		if ok {
			return acct, &owner, nil
		}
		return acct, nil, nil
	}
	return s.BackendAPI.PreviewAccount(ctx, input)
}

func (s *douyinStubBackend) OpenLink(ctx context.Context, rawInput string) (*service.OpenLinkResult, error) {
	if !s.HasAnyLogin() {
		return nil, &site.Error{Kind: site.ErrKindAuthRequired, Message: "请先登录账号"}
	}
	trimmed := strings.TrimSpace(rawInput)

	// Unsupported 19-digit bare integer check (0 network requests).
	if len(trimmed) == 19 && isAllASCIIUI(trimmed) {
		return nil, &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindInvalidInput,
			Message: "19 位纯数字无法区分抖音作品与直播间，请粘贴完整链接或分享文本",
		}
	}

	isDouyin := strings.Contains(trimmed, "douyin.com") || strings.Contains(trimmed, "iesdouyin.com") || strings.HasPrefix(trimmed, "MS4wLjAB")
	if isDouyin {
		if strings.Contains(trimmed, "/collection/") || strings.Contains(trimmed, "/hashtag/") || strings.Contains(trimmed, "/mall") {
			return nil, &site.Error{
				Site:    site.Douyin,
				Kind:    site.ErrKindInvalidInput,
				Message: "暂不支持抖音合集、话题或商城链接",
			}
		}
		if !s.StatusOf(string(site.Douyin)).LoggedIn {
			return nil, &site.Error{
				Site:    site.Douyin,
				Kind:    site.ErrKindAuthRequired,
				Message: "请先在登录页粘贴 Cookie 登录抖音",
			}
		}
		s.douyinNetCalls.Add(1)

		// Live link -> resolves to DouyinUser + DouyinLive
		if strings.Contains(trimmed, "live.douyin.com/") {
			parts := strings.Split(strings.TrimRight(trimmed, "/"), "/")
			webRID := parts[len(parts)-1]
			s.mu.Lock()
			lv := s.lives[webRID]
			var uv *service.DouyinUserView
			var wp service.DouyinUserWorksPage
			if lv != nil {
				uv = s.users[lv.OwnerSecUID]
				if pages, ok := s.userPages[lv.OwnerSecUID]; ok {
					wp = pages[""].page
				}
			}
			s.mu.Unlock()
			if lv == nil || uv == nil {
				return nil, &site.Error{Site: site.Douyin, Kind: "not_found", Message: "未找到直播间"}
			}
			_ = s.raw.HistoryStore().Record(storage.HistoryEntry{
				Site:         string(site.Douyin),
				Kind:         "live",
				ID:           uv.SecUID,
				Title:        lv.Title,
				Subtitle:     uv.Nickname,
				CanonicalURL: lv.CanonicalURL,
			})
			return &service.OpenLinkResult{
				Site:            string(site.Douyin),
				SiteTarget:      &site.ResolveTarget{Site: site.Douyin, Kind: "live", ID: webRID, CanonicalURL: lv.CanonicalURL},
				DouyinLive:      lv,
				DouyinUser:      uv,
				DouyinUserWorks: &wp,
			}, nil
		}

		// User link or sec_uid
		if strings.Contains(trimmed, "/user/") || strings.HasPrefix(trimmed, "MS4wLjAB") {
			secUID := trimmed
			if idx := strings.Index(trimmed, "/user/"); idx >= 0 {
				secUID = trimmed[idx+len("/user/"):]
				if q := strings.IndexAny(secUID, "?/ "); q >= 0 {
					secUID = secUID[:q]
				}
			}
			s.mu.Lock()
			uv := s.users[secUID]
			var wp service.DouyinUserWorksPage
			var wpErr string
			if pages, ok := s.userPages[secUID]; ok {
				fix := pages[""]
				wp = fix.page
				if fix.err != nil {
					wpErr = userErrorText(fix.err)
				}
			}
			s.mu.Unlock()
			if uv == nil {
				return nil, &site.Error{Site: site.Douyin, Kind: "not_found", Message: "未找到该抖音作者"}
			}
			_ = s.raw.HistoryStore().Record(storage.HistoryEntry{
				Site:         string(site.Douyin),
				Kind:         "user",
				ID:           uv.SecUID,
				Title:        uv.Nickname,
				Subtitle:     uv.Signature,
				CanonicalURL: uv.CanonicalURL,
			})
			return &service.OpenLinkResult{
				Site:                 string(site.Douyin),
				SiteTarget:           &site.ResolveTarget{Site: site.Douyin, Kind: "user", ID: uv.SecUID, CanonicalURL: uv.CanonicalURL},
				DouyinUser:           uv,
				DouyinUserWorks:      &wp,
				DouyinUserWorksError: wpErr,
			}, nil
		}

		// Video or Note work (including share text containing URL)
		var awemeID string
		for _, marker := range []string{"/video/", "/note/"} {
			if idx := strings.Index(trimmed, marker); idx >= 0 {
				rest := trimmed[idx+len(marker):]
				end := strings.IndexAny(rest, "?/ \t\r\n")
				if end >= 0 {
					rest = rest[:end]
				}
				awemeID = rest
				break
			}
		}
		s.mu.Lock()
		wv := s.works[awemeID]
		s.mu.Unlock()
		if wv == nil {
			return nil, &site.Error{Site: site.Douyin, Kind: "not_found", Message: "未找到该抖音作品"}
		}
		histKind := "video"
		if wv.Kind == "note" {
			histKind = "note"
		}
		_ = s.raw.HistoryStore().Record(storage.HistoryEntry{
			Site:         string(site.Douyin),
			Kind:         histKind,
			ID:           wv.ID,
			Title:        wv.Title,
			Subtitle:     wv.Author.Nickname,
			CanonicalURL: wv.CanonicalURL,
		})
		return &service.OpenLinkResult{
			Site:       string(site.Douyin),
			SiteTarget: &site.ResolveTarget{Site: site.Douyin, Kind: histKind, ID: wv.ID, CanonicalURL: wv.CanonicalURL},
			DouyinWork: wv,
			CachedItem: wv.CachedItem,
		}, nil
	}

	// Bilibili target: enforce Bilibili login before delegating to real Backend.
	if !s.BackendAPI.StatusOf(string(site.Bilibili)).LoggedIn {
		return nil, &site.Error{
			Site:    site.Bilibili,
			Kind:    site.ErrKindAuthRequired,
			Message: "请先登录哔哩哔哩账号",
		}
	}
	return s.BackendAPI.OpenLink(ctx, rawInput)
}

func (s *douyinStubBackend) DouyinUserWorks(_ context.Context, secUID, cursor string) (*service.DouyinUserWorksPage, error) {
	s.douyinNetCalls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if pages, ok := s.userPages[secUID]; ok {
		if fix, ok2 := pages[cursor]; ok2 {
			if fix.err != nil {
				return nil, fix.err
			}
			cp := fix.page
			return &cp, nil
		}
	}
	return &service.DouyinUserWorksPage{}, nil
}

func (s *douyinStubBackend) CacheDouyinWork(_ context.Context, awemeID string) (download.Task, error) {
	s.mu.Lock()
	s.cachedWork = awemeID
	s.mu.Unlock()
	return download.Task{
		TaskID:   "task_dy_" + awemeID,
		Site:     string(site.Douyin),
		Kind:     "video",
		TargetID: awemeID,
		Title:    "抖音作品 " + awemeID,
		Status:   "queued",
	}, nil
}

func isAllASCIIUI(str string) bool {
	for i := 0; i < len(str); i++ {
		if str[i] < '0' || str[i] > '9' {
			return false
		}
	}
	return len(str) > 0
}

func waitUIIdle(t *testing.T, app *App, posted chan func()) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		drained := false
		for !drained {
			select {
			case fn := <-posted:
				fn()
			default:
				drained = true
			}
		}
		if len(app.pending) == 0 && !app.BGMLoading && !app.ImageViewer.InlineLoading {
			return
		}
		select {
		case fn := <-posted:
			fn()
		case <-time.After(15 * time.Millisecond):
		}
	}
	t.Fatalf("timed out waiting for UI async work: pending=%v bgmLoading=%v inlineLoading=%v",
		app.pending, app.BGMLoading, app.ImageViewer.InlineLoading)
}

func buildTestWAVBytes(sampleRate, channels, numFrames int) []byte {
	bytesPerSample := 2
	dataBytes := numFrames * channels * bytesPerSample
	buf := make([]byte, 44+dataBytes)
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:8], uint32(36+dataBytes))
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:20], 16)
	binary.LittleEndian.PutUint16(buf[20:22], 1)
	binary.LittleEndian.PutUint16(buf[22:24], uint16(channels))
	binary.LittleEndian.PutUint32(buf[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(buf[28:32], uint32(sampleRate*channels*bytesPerSample))
	binary.LittleEndian.PutUint16(buf[32:34], uint16(channels*bytesPerSample))
	binary.LittleEndian.PutUint16(buf[34:36], 16)
	copy(buf[36:40], "data")
	binary.LittleEndian.PutUint32(buf[40:44], uint32(dataBytes))
	off := 44
	for f := 0; f < numFrames; f++ {
		sample := int16(3000 * math.Sin(2*math.Pi*440*float64(f)/float64(sampleRate)))
		for c := 0; c < channels; c++ {
			binary.LittleEndian.PutUint16(buf[off:off+2], uint16(sample))
			off += 2
		}
	}
	return buf
}

func buildTestMP4Bytes(t *testing.T, dir string) []byte {
	t.Helper()
	vBytes, err := download.BuildSyntheticVideoM4S(360, 480)
	if err != nil {
		t.Fatal(err)
	}
	aBytes, err := download.BuildSyntheticAudioM4S(15)
	if err != nil {
		t.Fatal(err)
	}
	vPath := filepath.Join(dir, "tmp_v.m4s")
	aPath := filepath.Join(dir, "tmp_a.m4s")
	outPath := filepath.Join(dir, "tmp_mux.mp4")
	if err := os.WriteFile(vPath, vBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(aPath, aBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := download.MuxDASHFiles(vPath, aPath, outPath); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(vPath)
	_ = os.Remove(aPath)
	_ = os.Remove(outPath)
	return raw
}

func buildTestPNGBytes() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 120, 160))
	for y := 0; y < 160; y++ {
		for x := 0; x < 120; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 60, G: 110, B: 180, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// TestDouyinLoginCookieClearAndThreeFailureModes verifies /login two-segment switcher,
// paste-Cookie-only Douyin form, immediate input clearing before async completion,
// three distinct failure messages without echoing input, and Douyin-only app entry.
func TestDouyinLoginCookieClearAndThreeFailureModes(t *testing.T) {
	app, stub, _, cleanup := newDouyinUITestHarness(t)
	defer cleanup()

	posted := make(chan func(), 16)
	app.dispatch = func(fn func()) { posted <- fn }
	app.CurrentRoute = RouteLogin

	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	if _, ok := tt.Find("登录站点：哔哩哔哩"); !ok {
		t.Fatal("expected Bilibili login site segment")
	}
	if _, ok := tt.Find("登录站点：抖音"); !ok {
		t.Fatal("expected Douyin login site segment")
	}

	// Switch to Douyin segment: must show Paste-Cookie ONLY (never QR refresh button).
	if err := tt.Click("登录站点：抖音"); err != nil {
		t.Fatalf("click Douyin login segment: %v", err)
	}
	tt.Frame()
	if app.LoginSite != "douyin" {
		t.Fatalf("expected LoginSite=douyin, got %q", app.LoginSite)
	}
	if _, ok := tt.Find("抖音Cookie输入框"); !ok {
		t.Fatal("expected Douyin Cookie password input")
	}
	if _, ok := tt.Find("刷新登录二维码"); ok {
		t.Fatal("Douyin login page must not show QR code controls")
	}

	// Gate async LoginWithCookie to prove DouyinCookieInput is cleared IMMEDIATELY upon submit.
	gate := make(chan struct{})
	stub.mu.Lock()
	stub.loginGate = gate
	stub.loginMode = "missing_session"
	stub.mu.Unlock()

	sentinel := "SENTINEL_RAW_COOKIE_VALUE_DO_NOT_ECHO"
	app.DouyinCookieInput = sentinel
	tt.Frame()
	if err := tt.Click("验证并登录抖音"); err != nil {
		t.Fatalf("click submit Douyin cookie: %v", err)
	}
	if app.DouyinCookieInput != "" {
		t.Fatalf("DouyinCookieInput must be cleared immediately on submit before async return, got %q", app.DouyinCookieInput)
	}
	close(gate)
	nextPosted(t, posted)()
	if app.LoginError != "缺少 sessionid" {
		t.Fatalf("failure mode 1 mismatch: got %q, want %q", app.LoginError, "缺少 sessionid")
	}
	if strings.Contains(app.LoginError, sentinel) {
		t.Fatalf("error echoed Cookie sentinel: %q", app.LoginError)
	}

	// Failure mode 2: profile/self unauthenticated
	stub.mu.Lock()
	stub.loginGate = nil
	stub.loginMode = "unauth"
	stub.mu.Unlock()
	app.DouyinCookieInput = sentinel
	tt.Frame()
	_ = tt.Click("验证并登录抖音")
	nextPosted(t, posted)()
	wantUnauth := "抖音认为这个 Cookie 没有登录或已过期"
	if app.LoginError != wantUnauth || strings.Contains(app.LoginError, sentinel) {
		t.Fatalf("failure mode 2 mismatch: got %q, want %q", app.LoginError, wantUnauth)
	}

	// Failure mode 3: gateway verification / captcha
	stub.mu.Lock()
	stub.loginMode = "verify"
	stub.mu.Unlock()
	app.DouyinCookieInput = sentinel
	tt.Frame()
	_ = tt.Click("验证并登录抖音")
	nextPosted(t, posted)()
	wantVerify := "抖音要求验证，请先在浏览器里正常打开一次抖音再复制"
	if app.LoginError != wantVerify || strings.Contains(app.LoginError, sentinel) {
		t.Fatalf("failure mode 3 mismatch: got %q, want %q", app.LoginError, wantVerify)
	}

	// Success: enters app even when Bilibili is logged out; /login shows Douyin summary card.
	stub.mu.Lock()
	stub.loginMode = "ok"
	stub.mu.Unlock()
	app.DouyinCookieInput = "valid_synth_input"
	tt.Frame()
	_ = tt.Click("验证并登录抖音")
	nextPosted(t, posted)()
	waitUIIdle(t, app, posted)

	if app.CurrentRoute != RouteHome {
		t.Fatalf("expected Douyin-only login to enter RouteHome, got %s", app.CurrentRoute)
	}
	if app.backend.StatusOf("bilibili").LoggedIn {
		t.Fatal("expected Bilibili to remain logged out")
	}
	if !strings.Contains(app.BannerInfo, "抖音测试账号") || !strings.Contains(app.BannerInfo, "trace_dy_demo") {
		t.Fatalf("expected Douyin account summary banner on login, got %q", app.BannerInfo)
	}

	app.OpenLoginForSite("douyin")
	tt.Frame()
	if _, ok := tt.Find("抖音已登录摘要"); !ok {
		t.Fatal("expected Douyin logged-in account summary card on /login")
	}
}

// TestPerSiteAuthRoutingExpirySettingsAndSubscriptionImport verifies per-site pre-auth gates,
// single-site vs dual-site expiry routing, /settings account two-group layout & per-site logout,
// per-site scheduler backoff display, and Bilibili-only followings import gate.
func TestPerSiteAuthRoutingExpirySettingsAndSubscriptionImport(t *testing.T) {
	app, stub, _, cleanup := newDouyinUITestHarness(t)
	defer cleanup()

	posted := make(chan func(), 32)
	app.dispatch = func(fn func()) { posted <- fn }
	var exitedFullscreen bool
	app.fullscreen = func(full bool) {
		if !full {
			exitedFullscreen = true
		}
	}

	// 1. Log into Douyin ONLY.
	stub.dyStatus = credential.Status{
		Site:     "douyin",
		LoggedIn: true,
		State:    "logged_in",
		Source:   "cookie",
		UID:      "900001",
		SecUID:   "MS4wLjABAAAA_synth_self_user_0001",
		UniqueID: "trace_dy_demo",
		Uname:    "抖音测试账号",
	}
	app.CompleteLogin()
	waitUIIdle(t, app, posted)

	tt := ui.NewTester(app.View, 1280, 860)

	// Pasting a Bilibili link while only Douyin is logged in must prompt Bilibili login and jump to /login pre-selecting Bilibili.
	app.Navigate(RouteParse)
	app.ParsePageInput = "https://www.bilibili.com/video/BV1xx411c7mD"
	tt.Frame()
	_ = tt.Click("立即解析")
	nextPosted(t, posted)()
	tt.Frame()
	if app.BannerLoginSite != "bilibili" || !strings.Contains(app.LastParseError, "哔哩哔哩") {
		t.Fatalf("expected Bilibili login prompt, got BannerLoginSite=%q LastParseError=%q", app.BannerLoginSite, app.LastParseError)
	}
	if err := tt.Click("解析提示去登录"); err != nil {
		t.Fatalf("click jump to login from parse prompt: %v", err)
	}
	if app.CurrentRoute != RouteLogin || app.LoginSite != "bilibili" {
		t.Fatalf("expected /login pre-selecting bilibili, got route=%s site=%s", app.CurrentRoute, app.LoginSite)
	}

	// Clicking "从 B 站关注列表导入" while Bilibili is logged out prompts Bilibili login without opening modal.
	app.Navigate(RouteSubscriptions)
	tt.Frame()
	if err := tt.Click("空状态导入关注"); err != nil {
		t.Fatalf("click empty-state import Bilibili followings: %v", err)
	}
	if app.ImportSubModalOpen || app.CurrentRoute != RouteLogin || app.LoginSite != "bilibili" {
		t.Fatalf("expected redirect to Bilibili login instead of opening import modal: modal=%v route=%s site=%s", app.ImportSubModalOpen, app.CurrentRoute, app.LoginSite)
	}

	// 2. Now log into Bilibili ONLY and paste a Douyin link -> 0 Douyin network calls!
	_, _ = stub.LogoutSite("douyin")
	_, err := stub.raw.ImportCookie(context.Background(), "SESSDATA=fixture; bili_jct=csrf; DedeUserID=12345", "")
	if err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()
	waitUIIdle(t, app, posted)
	stub.douyinNetCalls.Store(0)

	app.Navigate(RouteParse)
	app.ParsePageInput = "https://www.douyin.com/video/7300000000000000001"
	tt.Frame()
	_ = tt.Click("立即解析")
	nextPosted(t, posted)()
	tt.Frame()
	if got := stub.douyinNetCalls.Load(); got != 0 {
		t.Fatalf("expected 0 Douyin requests when not logged into Douyin, got %d", got)
	}
	if app.BannerLoginSite != "douyin" {
		t.Fatalf("expected BannerLoginSite=douyin, got %q", app.BannerLoginSite)
	}
	_ = tt.Click("解析提示去登录")
	if app.CurrentRoute != RouteLogin || app.LoginSite != "douyin" {
		t.Fatalf("expected /login pre-selecting douyin, got route=%s site=%s", app.CurrentRoute, app.LoginSite)
	}

	// 3. Log into BOTH sites and test single-site expiry routing.
	stub.mu.Lock()
	stub.dyStatus = credential.Status{
		Site:     "douyin",
		LoggedIn: true,
		State:    "logged_in",
		Source:   "cookie",
		UID:      "900001",
		SecUID:   "MS4wLjABAAAA_synth_self_user_0001",
		UniqueID: "trace_dy_demo",
		Uname:    "抖音测试账号",
	}
	stub.schedulers = map[string]service.SiteSchedulerStatus{
		"bilibili": {Site: "bilibili", DisplayName: "哔哩哔哩", LastCheckAt: "2026-11-12T10:00:00Z"},
		"douyin":   {Site: "douyin", DisplayName: "抖音", BackingOff: true, BackoffSummary: "抖音：因风控放慢到 1 小时，11 月 13 日 12:00 恢复"},
	}
	stub.mu.Unlock()
	app.Navigate(RouteHome)
	waitUIIdle(t, app, posted)

	// Douyin expires while user is on /home (shared route): stay on /home and show top notice ONCE.
	stub.mu.Lock()
	stub.dyStatus.LoggedIn = false
	stub.dyStatus.State = "expired"
	stub.mu.Unlock()
	app.handleBackendEvent(service.Event{Type: service.EventAccountChanged, Site: "douyin", Message: "expired"})
	waitUIIdle(t, app, posted)
	if app.CurrentRoute != RouteHome {
		t.Fatalf("single-site expiry on /home must stay on /home, got %s", app.CurrentRoute)
	}
	if !strings.Contains(app.BannerError, "抖音登录已失效") {
		t.Fatalf("expected single-site expiry top notice, got %q", app.BannerError)
	}
	// Dismiss notice; duplicate expiry event must NOT re-pop notice.
	app.BannerError = ""
	app.handleBackendEvent(service.Event{Type: service.EventAccountChanged, Site: "douyin", Message: "expired"})
	waitUIIdle(t, app, posted)
	if app.BannerError != "" {
		t.Fatalf("duplicate single-site expiry notice should be shown only once, got %q", app.BannerError)
	}

	// Re-login Douyin, open /douyin/user, then expire Douyin -> MUST redirect to /login pre-selecting douyin.
	stub.mu.Lock()
	stub.dyStatus.LoggedIn = true
	stub.dyStatus.State = "logged_in"
	stub.mu.Unlock()
	app.handleBackendEvent(service.Event{Type: service.EventAccountChanged, Site: "douyin", Message: "logged_in"})
	app.CurrentDouyinUser = &service.DouyinUserView{SecUID: "MS4wLjABAAAA_u1", Nickname: "作者A"}
	app.CurrentRoute = RouteDouyinUser

	stub.mu.Lock()
	stub.dyStatus.LoggedIn = false
	stub.dyStatus.State = "expired"
	stub.mu.Unlock()
	app.handleBackendEvent(service.Event{Type: service.EventAccountChanged, Site: "douyin", Message: "expired"})
	waitUIIdle(t, app, posted)
	if app.CurrentRoute != RouteLogin || app.LoginSite != "douyin" || app.SavedRoute != RouteDouyinUser {
		t.Fatalf("expiry on /douyin/user must redirect to /login(douyin) saving RouteDouyinUser, got route=%s site=%s saved=%s",
			app.CurrentRoute, app.LoginSite, app.SavedRoute)
	}

	// 4. Settings -> 账号 two flat groups & per-site logout isolation + Settings -> 订阅与直播 backoff line.
	stub.mu.Lock()
	stub.dyStatus.LoggedIn = true
	stub.dyStatus.State = "logged_in"
	stub.mu.Unlock()
	app.SettingsCategory = "account"
	app.Navigate(RouteSettings)
	tt.Frame()
	if _, ok := tt.Find("账号分组：哔哩哔哩"); !ok {
		t.Fatal("missing Bilibili account group in Settings")
	}
	if _, ok := tt.Find("账号分组：抖音"); !ok {
		t.Fatal("missing Douyin account group in Settings")
	}
	if err := tt.Click("退出抖音登录"); err != nil {
		t.Fatalf("click Douyin logout: %v", err)
	}
	tt.Frame()
	if _, ok := tt.Find("退出登录确认对话框"); !ok || app.ConfirmLogoutSite != "douyin" {
		t.Fatalf("expected Douyin logout confirm dialog, site=%q", app.ConfirmLogoutSite)
	}
	if err := tt.Click("确认退出登录"); err != nil {
		t.Fatalf("confirm Douyin logout: %v", err)
	}
	waitUIIdle(t, app, posted)
	tt.Frame()
	if app.backend.StatusOf("douyin").LoggedIn {
		t.Fatal("expected Douyin logged out")
	}
	if !app.backend.StatusOf("bilibili").LoggedIn || app.CurrentRoute != RouteSettings {
		t.Fatalf("logging out Douyin must leave Bilibili logged in and keep user on /settings, route=%s", app.CurrentRoute)
	}
	if _, ok := tt.Find("登录抖音"); !ok {
		t.Fatal("expected 登录抖音 button when Douyin group is logged out")
	}

	app.SettingsCategory = "subscription"
	tt.Frame()
	if _, ok := tt.Find("退避状态：douyin"); !ok {
		t.Fatal("expected per-site Douyin backoff line in Settings -> 订阅与直播")
	}

	// 5. When BOTH sites become logged out/expired, overlays close, fullscreen exits, and route redirects to /login.
	app.Navigate(RouteLibrary)
	app.ImageViewer.Open = true
	app.PlayerOverlay.Open = true
	app.PlayerOverlay.Fullscreen = true
	_, _ = stub.raw.Logout()
	app.handleBackendEvent(service.Event{Type: service.EventAccountChanged, Site: "bilibili", Message: "expired"})
	if app.CurrentRoute != RouteLogin || app.SavedRoute != RouteLibrary {
		t.Fatalf("dual logout must redirect to /login saving RouteLibrary, got route=%s saved=%s", app.CurrentRoute, app.SavedRoute)
	}
	if app.ImageViewer.Open || app.PlayerOverlay.Open || !exitedFullscreen {
		t.Fatalf("dual logout must close overlays and exit fullscreen: viewer=%v player=%v exitedFS=%v",
			app.ImageViewer.Open, app.PlayerOverlay.Open, exitedFullscreen)
	}
}

// TestDouyinParseDispatchAndUserPagePaginationExpiredRetryPrivate verifies parse dispatching
// for Douyin targets + unsupported links (0 requests) and /douyin/user pagination, dedup,
// page-2 empty-shell "登录已失效", retryable risk_control error, and private account state.
func TestDouyinParseDispatchAndUserPagePaginationExpiredRetryPrivate(t *testing.T) {
	app, stub, _, cleanup := newDouyinUITestHarness(t)
	defer cleanup()

	posted := make(chan func(), 32)
	app.dispatch = func(fn func()) { posted <- fn }
	stub.dyStatus = credential.Status{
		Site: "douyin", LoggedIn: true, State: "logged_in", Source: "cookie",
		UID: "900001", SecUID: "MS4wLjABAAAA_synth_self_user_0001", Uname: "抖音测试账号",
	}

	secUID := "MS4wLjABAAAA_synth_creator_01"
	liveState := &service.LiveAccountState{
		Site:        "douyin",
		AccountKey:  "douyin:" + secUID,
		UID:         secUID,
		RoomID:      "7399001122",
		WebRID:      "668899",
		Uname:       "极光纪实",
		Title:       "高原星空实拍直播",
		LiveStatus:  1,
		StatusText:  "直播中",
		QualityName: "原画",
		Codec:       "hevc",
	}
	stub.users[secUID] = &service.DouyinUserView{
		SecUID:         secUID,
		UID:            "100200",
		UniqueID:       "aurora_doc",
		Nickname:       "极光纪实",
		Signature:      "长线自然影像记录",
		AwemeCount:     3,
		FollowerCount:  128000,
		FollowingCount: 42,
		TotalFavorited: 960000,
		LiveStatus:     1,
		RoomID:         "7399001122",
		WebRID:         "668899",
		CanonicalURL:   "https://www.douyin.com/user/" + secUID,
		LiveRoom:       liveState,
	}
	stub.lives["668899"] = &service.DouyinLiveView{
		OwnerSecUID:  secUID,
		OwnerName:    "极光纪实",
		WebRID:       "668899",
		RoomID:       "7399001122",
		LiveStatus:   1,
		StatusText:   "直播中",
		Title:        "高原星空实拍直播",
		CanonicalURL: "https://live.douyin.com/668899",
		State:        liveState,
	}
	stub.userPages[secUID] = map[string]douyinPageFixture{
		"": {
			page: service.DouyinUserWorksPage{
				Works: []service.DouyinWorkCardDTO{
					{ID: "730001", Kind: "video", Title: "置顶代表作", DurationSec: 95, IsTop: true, CacheKey: "douyin:video:730001", CanonicalURL: "https://www.douyin.com/video/730001"},
					{ID: "730002", Kind: "note", Title: "山谷图文随记", PageCount: 3, CacheKey: "douyin:dynamic:730002", CanonicalURL: "https://www.douyin.com/note/730002"},
				},
				NextCursor: "cur_page_2",
				HasMore:    true,
			},
		},
		"cur_page_2": {
			err: &site.Error{Site: site.Douyin, Kind: site.ErrKindRiskControl, Message: "请求过快触发风控"},
		},
	}
	stub.works["730001"] = &service.DouyinWorkView{
		ID:           "730001",
		Kind:         "video",
		Title:        "置顶代表作\n山野延时摄影 #延时摄影 #风光",
		Desc:         "置顶代表作\n山野延时摄影 #延时摄影 #风光",
		Hashtags:     []string{"延时摄影", "风光"},
		CreateTime:   1710000000,
		DurationSec:  95,
		Author:       service.DouyinAuthorDTO{SecUID: secUID, UniqueID: "aurora_doc", Nickname: "极光纪实"},
		QualityLabel: "1080P",
		VideoCodec:   "hevc",
		Music:        &service.DouyinMusicDTO{Title: "夜航星", Author: "极光纪实"},
		Stats:        service.DouyinStatsDTO{DiggCount: 5200, CommentCount: 310, CollectCount: 890, ShareCount: 120},
		CanonicalURL: "https://www.douyin.com/video/730001",
		CacheKey:     "douyin:video:730001",
	}

	app.Navigate(RouteParse)
	tt := ui.NewTester(app.View, 1280, 860)

	// 1. Unsupported inputs must make 0 network calls and show clear Chinese messages.
	for _, bad := range []string{
		"7300000000000000001",
		"https://www.douyin.com/collection/7300001",
		"https://www.douyin.com/hashtag/7300002",
	} {
		stub.douyinNetCalls.Store(0)
		app.ParsePageInput = bad
		tt.Frame()
		_ = tt.Click("立即解析")
		nextPosted(t, posted)()
		if stub.douyinNetCalls.Load() != 0 {
			t.Fatalf("unsupported input %q must make 0 network calls, got %d", bad, stub.douyinNetCalls.Load())
		}
		if app.CurrentRoute != RouteParse || app.LastParseError == "" {
			t.Fatalf("unsupported input %q must stay on /parse with error, route=%s err=%q", bad, app.CurrentRoute, app.LastParseError)
		}
	}

	// 2. Share text -> RouteDouyinWork
	app.SubmitParse("7.42 复制打开抖音，看看【极光纪实的作品】 https://www.douyin.com/video/730001 快来围观")
	nextPosted(t, posted)()
	waitUIIdle(t, app, posted)
	if app.CurrentRoute != RouteDouyinWork || app.CurrentDouyinWork == nil || app.CurrentDouyinWork.ID != "730001" {
		t.Fatalf("share text did not open RouteDouyinWork 730001: route=%s work=%+v", app.CurrentRoute, app.CurrentDouyinWork)
	}

	// 3. Live link -> RouteDouyinUser with live banner + no Bilibili tabs
	app.SubmitParse("https://live.douyin.com/668899")
	nextPosted(t, posted)()
	waitUIIdle(t, app, posted)
	tt.Frame()
	if app.CurrentRoute != RouteDouyinUser || app.CurrentDouyinUser == nil || app.CurrentDouyinUser.SecUID != secUID {
		t.Fatalf("live link did not open creator's RouteDouyinUser: route=%s user=%+v", app.CurrentRoute, app.CurrentDouyinUser)
	}
	for _, reqLabel := range []string{
		"用户页站点标记",
		"抖音主页直播横幅",
		"抖音主页开始录制",
		"抖音置顶标记：730001",
		"抖音图文标记：730002",
		"作品更多：730001",
	} {
		if _, ok := tt.Find(reqLabel); !ok {
			t.Fatalf("missing expected control %q on /douyin/user", reqLabel)
		}
	}
	if _, ok := tt.Find("UP主动态虚拟列表"); ok {
		t.Fatal("/douyin/user must not render Bilibili tabs")
	}

	// 4. Page-2 risk_control error preserves page-1 works and offers retry.
	app.LoadMoreDouyinWorks()
	waitUIIdle(t, app, posted)
	tt.Frame()
	if len(app.CurrentDouyinWorks) != 2 || app.DouyinWorksError == "" {
		t.Fatalf("expected 2 preserved works + risk_control error, got len=%d err=%q", len(app.CurrentDouyinWorks), app.DouyinWorksError)
	}
	if _, ok := tt.Find("重试加载抖音作品"); !ok {
		t.Fatal("expected retry button on risk_control pagination error")
	}

	// Retry succeeds, deduplicating overlapping 730002 and appending 730003, with cur_page_3 remaining.
	stub.mu.Lock()
	stub.userPages[secUID]["cur_page_2"] = douyinPageFixture{
		page: service.DouyinUserWorksPage{
			Works: []service.DouyinWorkCardDTO{
				{ID: "730002", Kind: "note", Title: "山谷图文随记"},
				{ID: "730003", Kind: "video", Title: "冰川航拍短片", DurationSec: 48},
			},
			NextCursor: "cur_page_3",
			HasMore:    true,
		},
	}
	stub.userPages[secUID]["cur_page_3"] = douyinPageFixture{
		page: service.DouyinUserWorksPage{
			UncertainEmpty: true,
		},
	}
	stub.mu.Unlock()

	if err := tt.Click("重试加载抖音作品"); err != nil {
		t.Fatalf("click retry Douyin works: %v", err)
	}
	waitUIIdle(t, app, posted)
	tt.Frame()
	if len(app.CurrentDouyinWorks) != 3 {
		t.Fatalf("expected deduplicated 3 works after page 2, got %d", len(app.CurrentDouyinWorks))
	}

	// 5. Page-3 UncertainEmpty empty shell shows "登录已失效" ("抖音失效去登录") instead of "已经到底了", keeping 3 works.
	app.LoadMoreDouyinWorks()
	waitUIIdle(t, app, posted)
	tt.Frame()
	if len(app.CurrentDouyinWorks) != 3 || !app.DouyinWorksAuthExpired {
		t.Fatalf("expected UncertainEmpty to set DouyinWorksAuthExpired while keeping 3 works, len=%d expired=%v",
			len(app.CurrentDouyinWorks), app.DouyinWorksAuthExpired)
	}
	if _, ok := tt.Find("抖音失效去登录"); !ok {
		t.Fatal("expected 登录已失效 re-login action on page-3 empty shell")
	}

	// 6. Private account shows exact Chinese message.
	privUID := "MS4wLjABAAAA_synth_private_user"
	stub.mu.Lock()
	stub.users[privUID] = &service.DouyinUserView{
		SecUID:       privUID,
		Nickname:     "私密测试号",
		IsPrivate:    true,
		CanonicalURL: "https://www.douyin.com/user/" + privUID,
	}
	stub.mu.Unlock()
	app.SubmitParse(privUID)
	nextPosted(t, posted)()
	tt.Frame()
	if _, ok := tt.Find("私密账号提示"); !ok {
		t.Fatal("expected 私密账号提示 on private Douyin account")
	}
}

// TestDouyinWorkVideoNoteBGMAndViewerInlinePlayback verifies video detail (no play count,
// cached spec without Bilibili upgrade/supplement controls, 0 offline requests) and mixed note
// detail (pure-audio BGM playback, inline video/live-photo playback in ImageViewer immediately
// pausing BGM and never auto-resuming BGM on stop/close).
func TestDouyinWorkVideoNoteBGMAndViewerInlinePlayback(t *testing.T) {
	app, stub, _, cleanup := newDouyinUITestHarness(t)
	defer cleanup()

	posted := make(chan func(), 32)
	app.dispatch = func(fn func()) { posted <- fn }
	stub.dyStatus = credential.Status{
		Site: "douyin", LoggedIn: true, State: "logged_in", Source: "cookie",
		UID: "900001", SecUID: "MS4wLjABAAAA_synth_self_user_0001", Uname: "抖音测试账号",
	}

	rootDir := stub.raw.GetSettings().CacheDir
	if err := os.MkdirAll(rootDir, 0o700); err != nil {
		t.Fatal(err)
	}
	mp4Bytes := buildTestMP4Bytes(t, rootDir)
	pngBytes := buildTestPNGBytes()
	wavBytes := buildTestWAVBytes(48000, 2, 48000*3) // 3s stereo PCM WAV

	// 1. Seed a cached local Douyin video (even with UpgradeAvailable=true to verify NO upgrade banner renders for Douyin).
	vidDir := filepath.Join(rootDir, "douyin", "creator_a", "video_731001")
	if err := os.MkdirAll(vidDir, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(vidDir, "video.mp4"), mp4Bytes, 0o600)
	_ = os.WriteFile(filepath.Join(vidDir, "cover.png"), pngBytes, 0o600)
	savedVideo, err := stub.raw.LibraryStore().SaveItem(vidDir, library.ItemMetadata{
		Version:  1,
		ID:       "731001",
		RawID:    "731001",
		Site:     "douyin",
		Kind:     "video",
		Status:   "complete",
		Title:    "竖屏雪山延时记录",
		Desc:     "竖屏雪山延时记录\n#雪山 #摄影",
		URL:      "https://www.douyin.com/video/731001",
		PubTs:    1710001000,
		CachedAt: "2026-11-12T10:00:00Z",
		Duration: 45,
		Author: library.ItemAuthor{
			Site:     "douyin",
			UID:      "MS4wLjABAAAA_creator_a",
			UniqueID: "snow_lens",
			Name:     "雪山镜头",
		},
		Media: []library.MediaFile{
			{Role: "video", Path: "video.mp4", Duration: 45},
			{Role: "cover", Path: "cover.png", Width: 1080, Height: 1440},
		},
		DouyinVideo: &library.DouyinVideoInfo{
			File:        "video.mp4",
			QualityName: "1080P",
			Codec:       "hevc",
			Width:       1080,
			Height:      1440,
		},
		Music:            &library.MusicInfo{Title: "高原风声", Author: "雪山镜头", Original: true},
		UpgradeAvailable: true,
		UpgradeHint:      "不应显示升级横幅",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Open offline from Library -> 0 network requests, RouteDouyinWork, cached spec shown, NO Bilibili controls.
	stub.douyinNetCalls.Store(0)
	app.OpenLibraryItemDetail(savedVideo)
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	if stub.douyinNetCalls.Load() != 0 {
		t.Fatalf("offline Douyin detail must make 0 network calls, got %d", stub.douyinNetCalls.Load())
	}
	if app.CurrentRoute != RouteDouyinWork {
		t.Fatalf("expected RouteDouyinWork for cached Douyin video, got %s", app.CurrentRoute)
	}
	for _, label := range []string{"抖音作品作者站点标记", "缓存视频", "抖音视频封面播放", "抖音本机缓存规格"} {
		if _, ok := tt.Find(label); !ok {
			t.Fatalf("missing %q on cached Douyin video detail", label)
		}
	}
	for _, forbidden := range []string{"升级缓存", "补存弹幕和字幕", "抖音作品统计行"} {
		if _, ok := tt.Find(forbidden); ok {
			t.Fatalf("cached Douyin video must NOT render %q", forbidden)
		}
	}
	// Clicking cached cover opens PlayerOverlay on the video file.
	if err := tt.Click("抖音视频封面播放"); err != nil {
		t.Fatalf("click Douyin cached video cover: %v", err)
	}
	waitUIIdle(t, app, posted)
	if !app.PlayerOverlay.Open {
		t.Fatal("expected PlayerOverlay to open when clicking cached Douyin video cover")
	}
	app.closePlayerOverlay()

	// 2. Seed a cached mixed Douyin note (page 1: image, page 2: live_photo motion, page 3: video, + pure-audio music.wav).
	noteDir := filepath.Join(rootDir, "douyin", "creator_a", "note_731002")
	if err := os.MkdirAll(noteDir, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(noteDir, "p01.png"), pngBytes, 0o600)
	_ = os.WriteFile(filepath.Join(noteDir, "p02.png"), pngBytes, 0o600)
	_ = os.WriteFile(filepath.Join(noteDir, "p02_motion.mp4"), mp4Bytes, 0o600)
	_ = os.WriteFile(filepath.Join(noteDir, "p03_cover.png"), pngBytes, 0o600)
	_ = os.WriteFile(filepath.Join(noteDir, "p03_video.mp4"), mp4Bytes, 0o600)
	_ = os.WriteFile(filepath.Join(noteDir, "music.wav"), wavBytes, 0o600)

	savedNote, err := stub.raw.LibraryStore().SaveItem(noteDir, library.ItemMetadata{
		Version:  1,
		ID:       "731002",
		RawID:    "731002",
		Site:     "douyin",
		Kind:     "dynamic",
		Status:   "complete",
		Title:    "图文实况视频混排手记",
		Text:     "图文实况视频混排手记\n记录三段旅途瞬间 #混排图文 #实况",
		URL:      "https://www.douyin.com/note/731002",
		PubTs:    1710002000,
		CachedAt: "2026-11-12T11:00:00Z",
		Author: library.ItemAuthor{
			Site:     "douyin",
			UID:      "MS4wLjABAAAA_creator_a",
			UniqueID: "snow_lens",
			Name:     "雪山镜头",
		},
		Media: []library.MediaFile{
			{Role: "image", Path: "p01.png", Width: 1080, Height: 1440},
			{Role: "image", Path: "p02.png", Motion: "p02_motion.mp4", Width: 1080, Height: 1440},
			{Role: "motion", Path: "p02_motion.mp4", Duration: 3},
			{Role: "cover", Path: "p03_cover.png", Width: 1080, Height: 1440},
			{Role: "video", Path: "p03_video.mp4", Duration: 12},
			{Role: "audio", Path: "music.wav", Duration: 3},
		},
		NotePages: []library.NotePageRecord{
			{Index: 1, Kind: "image", Image: "p01.png", Width: 1080, Height: 1440},
			{Index: 2, Kind: "motion", Image: "p02.png", Motion: "p02_motion.mp4", Width: 1080, Height: 1440, Duration: 3},
			{Index: 3, Kind: "video", Cover: "p03_cover.png", Video: "p03_video.mp4", Width: 1080, Height: 1440, Duration: 12},
		},
		Music: &library.MusicInfo{
			Title:    "山间晨雾",
			Author:   "雪山镜头",
			Duration: 3,
			File:     "music.wav",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	app.OpenLibraryItemDetail(savedNote)
	tt.Frame()

	bgmKey := "douyin-note:731002"
	for _, label := range []string{
		"缓存图文",
		"查看图文页 1",
		"查看图文页 2",
		"实况标记 2",
		"查看图文页 3",
		"视频页标记 3",
		"配乐栏：" + bgmKey,
		"配乐播放暂停：" + bgmKey,
		"配乐进度条：" + bgmKey,
	} {
		if _, ok := tt.Find(label); !ok {
			t.Fatalf("missing %q on mixed Douyin note detail", label)
		}
	}

	// Start BGM playback on pure-audio music.wav.
	if err := tt.Click("配乐播放暂停：" + bgmKey); err != nil {
		t.Fatalf("click BGM play: %v", err)
	}
	nextPosted(t, posted)()
	waitUIIdle(t, app, posted)
	if !playerLoaded(app.bgmPlayer) || !playerPlaying(app.bgmPlayer) || app.bgmPlayer.HasVideo() {
		t.Fatalf("expected bgmPlayer playing pure-audio file: loaded=%v playing=%v hasVideo=%v err=%q",
			playerLoaded(app.bgmPlayer), playerPlaying(app.bgmPlayer), app.bgmPlayer.HasVideo(), app.BGMError)
	}

	// Click mixed video page 3 -> opens ImageViewer, starts viewerPlayer inline, and IMMEDIATELY pauses bgmPlayer!
	if err := tt.Click("查看图文页 3"); err != nil {
		t.Fatalf("click mixed video page 3: %v", err)
	}
	nextPosted(t, posted)()
	waitUIIdle(t, app, posted)
	tt.Frame()
	if !app.ImageViewer.Open || app.ImageViewer.Index != 2 {
		t.Fatalf("expected ImageViewer open at index 2, got open=%v index=%d", app.ImageViewer.Open, app.ImageViewer.Index)
	}
	if runtime.GOOS != "windows" {
		t.Skip("the rest plays the video page inline, which needs Media Foundation")
	}
	if !playerPlaying(app.viewerPlayer) {
		t.Fatalf("expected viewerPlayer playing inline video page, err=%q", app.ImageViewer.InlineError)
	}
	if playerPlaying(app.bgmPlayer) {
		t.Fatal("BGM bar MUST pause immediately when mixed video page starts playing")
	}
	if _, ok := tt.Find("看图内嵌播放进度栏"); !ok {
		t.Fatal("expected inline progress bar in ImageViewer for mixed video page")
	}

	// Navigate to live-photo page 2 -> stops viewerPlayer, and BGM must NOT auto-resume!
	if err := tt.Click("看图上一张"); err != nil {
		t.Fatalf("click previous page in ImageViewer: %v", err)
	}
	waitUIIdle(t, app, posted)
	tt.Frame()
	if playerLoaded(app.viewerPlayer) {
		t.Fatal("expected viewerPlayer stopped after switching away from video page")
	}
	if playerPlaying(app.bgmPlayer) {
		t.Fatal("BGM must NOT auto-resume after inline video page stops")
	}

	// Resume BGM manually, then trigger live-photo motion playback on page 2 -> pauses BGM, and closing viewer keeps BGM paused.
	app.bgmPlayer.Play()
	if !playerPlaying(app.bgmPlayer) {
		t.Fatal("expected bgmPlayer resumed for live-photo test")
	}
	if err := tt.Click("看图播放实况"); err != nil {
		t.Fatalf("click play live photo in ImageViewer: %v", err)
	}
	nextPosted(t, posted)()
	waitUIIdle(t, app, posted)
	if !playerPlaying(app.viewerPlayer) {
		t.Fatal("expected viewerPlayer playing live-photo motion clip")
	}
	if playerPlaying(app.bgmPlayer) {
		t.Fatal("BGM must pause immediately when live-photo motion starts playing")
	}
	if err := tt.Click("关闭看图"); err != nil {
		t.Fatalf("click close ImageViewer: %v", err)
	}
	if app.ImageViewer.Open || playerLoaded(app.viewerPlayer) || playerPlaying(app.bgmPlayer) {
		t.Fatalf("closing ImageViewer must stop viewerPlayer and keep BGM paused: viewer=%v vLoaded=%v bgmPlaying=%v",
			app.ImageViewer.Open, playerLoaded(app.viewerPlayer), playerPlaying(app.bgmPlayer))
	}
}

// TestSiteFiltersPersistenceAndSixSiteBadges verifies Home (wide + narrow) and Library
// 3-segment site filters with ui-state.json persistence, Douyin subscription account preview,
// all 6 required small-text site badge locations, and ZERO site badges on subscription grid avatars.
func TestSiteFiltersPersistenceAndSixSiteBadges(t *testing.T) {
	app, stub, _, cleanup := newDouyinUITestHarness(t)
	defer cleanup()

	posted := make(chan func(), 32)
	app.dispatch = func(fn func()) { posted <- fn }
	_, _ = stub.raw.ImportCookie(context.Background(), "SESSDATA=fixture; bili_jct=csrf; DedeUserID=12345", "")
	stub.dyStatus = credential.Status{
		Site: "douyin", LoggedIn: true, State: "logged_in", Source: "cookie",
		UID: "900001", SecUID: "MS4wLjABAAAA_synth_self_user_0001", Uname: "抖音测试账号",
	}
	app.CompleteLogin()
	waitUIIdle(t, app, posted)

	// Seed mixed Bilibili + Douyin subscriptions (and one multi-account subscription).
	subMixed, err := stub.raw.CreateSubscription("跨站摄影师", "双站同步更新", []subscription.AccountRef{
		{Site: "bilibili", UID: "20001", Name: "摄影师_B站"},
		{Site: "douyin", UID: "MS4wLjABAAAA_synth_creator_alpha", UniqueID: "dy_creator_alpha", Name: "抖音影像创作者"},
	}, subscription.SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = stub.raw.CreateSubscription("纯抖音创作者", "", []subscription.AccountRef{
		{Site: "douyin", UID: "MS4wLjABAAAA_synth_creator_beta", UniqueID: "dy_beta", Name: "极简短片"},
	}, subscription.SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatal(err)
	}

	// Seed mixed Bilibili + Douyin library items & feed entries + history entries.
	rootDir := stub.raw.GetSettings().CacheDir
	if err := os.MkdirAll(rootDir, 0o700); err != nil {
		t.Fatal(err)
	}
	pngBytes := buildTestPNGBytes()
	biliDir := filepath.Join(rootDir, "bili_item")
	dyDir := filepath.Join(rootDir, "dy_item")
	_ = os.MkdirAll(biliDir, 0o700)
	_ = os.MkdirAll(dyDir, 0o700)
	_ = os.WriteFile(filepath.Join(biliDir, "p1.png"), pngBytes, 0o600)
	_ = os.WriteFile(filepath.Join(dyDir, "p1.png"), pngBytes, 0o600)

	biliItem, err := stub.raw.LibraryStore().SaveItem(biliDir, library.ItemMetadata{
		Version: 1, ID: "dyn_bili_1", RawID: "dyn_bili_1", Site: "bilibili", Kind: "dynamic", Status: "complete",
		Title: "B站图文动态测试", Text: "B站正文", PubTs: 1710005000, CachedAt: "2026-11-12T10:00:00Z",
		Author: library.ItemAuthor{Site: "bilibili", UID: "20001", Mid: 20001, Name: "摄影师_B站"},
		Media:  []library.MediaFile{{Role: "image", Path: "p1.png", Width: 120, Height: 160}},
	})
	if err != nil {
		t.Fatal(err)
	}
	dyItem, err := stub.raw.LibraryStore().SaveItem(dyDir, library.ItemMetadata{
		Version: 1, ID: "732001", RawID: "732001", Site: "douyin", Kind: "dynamic", Status: "complete",
		Title: "抖音图文作品测试", Text: "抖音正文", PubTs: 1710006000, CachedAt: "2026-11-12T10:05:00Z",
		Author:    library.ItemAuthor{Site: "douyin", UID: "MS4wLjABAAAA_synth_creator_alpha", UniqueID: "dy_creator_alpha", Name: "抖音影像创作者"},
		Media:     []library.MediaFile{{Role: "image", Path: "p1.png", Width: 120, Height: 160}},
		NotePages: []library.NotePageRecord{{Index: 1, Kind: "image", Image: "p1.png", Width: 120, Height: 160}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = stub.raw.LibraryStore().UpsertFeedEntry(library.FeedEntry{
		Site: "bilibili", Kind: "dynamic", RawID: "dyn_bili_1", AuthorUID: "20001", Mid: 20001,
		AuthorName: "摄影师_B站", Title: "B站图文动态测试", Summary: "B站正文", PubTs: 1710005000, Cached: true, ItemKey: biliItem.ItemKey,
	})
	_ = stub.raw.LibraryStore().UpsertFeedEntry(library.FeedEntry{
		Site: "douyin", Kind: "dynamic", RawID: "732001", AuthorUID: "MS4wLjABAAAA_synth_creator_alpha",
		AuthorName: "抖音影像创作者", Title: "抖音图文作品测试", Summary: "抖音正文", PubTs: 1710006000, Cached: true, ItemKey: dyItem.ItemKey,
	})
	_ = stub.raw.HistoryStore().Record(storage.HistoryEntry{
		Site: "bilibili", Kind: "video", ID: "BV1xx411c7mD", Title: "B站历史视频", Subtitle: "摄影师_B站", CanonicalURL: "https://www.bilibili.com/video/BV1xx411c7mD",
	})
	_ = stub.raw.HistoryStore().Record(storage.HistoryEntry{
		Site: "douyin", Kind: "video", ID: "732009", Title: "抖音历史视频", Subtitle: "抖音影像创作者", CanonicalURL: "https://www.douyin.com/video/732009",
	})

	app.RefreshData()
	app.RestartHomeSession()
	waitUIIdle(t, app, posted)

	// 1. Home wide (1280x860): verify Place #3 (Home feed author site marks) + 3-segment filter + persistence.
	ttWide := ui.NewTester(app.View, 1280, 860)
	ttWide.Frame()
	if _, ok := ttWide.Find("主页站点筛选：全部"); !ok {
		t.Fatal("missing Home 3-segment site filter")
	}
	if _, ok := ttWide.Find("主页作者站点：douyin:dynamic:732001"); !ok {
		t.Fatal("Place #3 missing: Home feed Douyin post author site badge")
	}
	if _, ok := ttWide.Find("主页作者站点：bilibili:dynamic:dyn_bili_1"); !ok {
		t.Fatal("Place #3 missing: Home feed Bilibili post author site badge")
	}

	// Filter Home to Douyin -> persists to ui-state.json and hides Bilibili post!
	if err := ttWide.Click("主页站点筛选：抖音"); err != nil {
		t.Fatalf("click Home Douyin filter: %v", err)
	}
	waitUIIdle(t, app, posted)
	ttWide.Frame()
	if got := stub.raw.GetUIState().FeedFilter.Site; got != "douyin" {
		t.Fatalf("expected persisted FeedFilter.Site=douyin in ui-state.json, got %q", got)
	}
	if _, ok := ttWide.Find("主页作者站点：bilibili:dynamic:dyn_bili_1"); ok {
		t.Fatal("Home Douyin filter should exclude Bilibili posts")
	}
	if _, ok := ttWide.Find("主页作者站点：douyin:dynamic:732001"); !ok {
		t.Fatal("Home Douyin filter should keep Douyin posts")
	}

	// Also verify narrow width (720x860) renders the 3-segment site filter above timeline!
	ttNarrow := ui.NewTester(app.View, 720, 860)
	ttNarrow.Frame()
	if _, ok := ttNarrow.Find("主页站点筛选：全部"); !ok {
		t.Fatal("missing narrow-window Home 3-segment site filter")
	}
	_ = ttNarrow.Click("主页站点筛选：全部")
	waitUIIdle(t, app, posted)

	// 2. Place #4 (Library card meta row site badge) + Library 3-segment site filter.
	app.Navigate(RouteLibrary)
	waitUIIdle(t, app, posted)
	ttWide.Frame()
	if _, ok := ttWide.Find("媒体库站点筛选：抖音"); !ok {
		t.Fatal("missing Library 3-segment site filter")
	}
	if _, ok := ttWide.Find("媒体库站点：" + dyItem.ItemKey + "#p1.png"); !ok {
		// Check either per-image tile key or base item key
		if _, okBase := ttWide.Find("媒体库站点：" + dyItem.ItemKey); !okBase {
			t.Fatal("Place #4 missing: Library Douyin card meta site badge")
		}
	}
	_ = ttWide.Click("媒体库站点筛选：抖音")
	waitUIIdle(t, app, posted)
	ttWide.Frame()
	for _, it := range app.LibraryItems {
		if it.Site != "douyin" {
			t.Fatalf("Library Douyin filter returned non-Douyin item: %s (%s)", it.ItemKey, it.Site)
		}
	}

	// 3. Subscription Grid NEGATIVE check (NEVER render site badges on subscription grid avatars!)
	// + Add Subscription Douyin preview + Place #2 (Subscription detail 账号 list site badges).
	app.Navigate(RouteSubscriptions)
	ttWide.Frame()
	if _, ok := ttWide.Find("选择订阅 " + subMixed.ID); !ok {
		t.Fatal("expected subscription avatar cell on /subscriptions grid")
	}
	for _, forbidden := range []string{
		"订阅详情账号站点：bilibili:20001",
		"订阅详情账号站点：douyin:MS4wLjABAAAA_synth_creator_alpha",
		"用户页站点标记",
	} {
		if _, ok := ttWide.Find(forbidden); ok {
			t.Fatalf("subscription grid avatars MUST NOT render site badge %q", forbidden)
		}
	}

	// Test Add Subscription Douyin preview badge
	app.AddSubDialogOpen = true
	app.AddSubInput = "https://www.douyin.com/user/MS4wLjABAAAA_synth_new_dy"
	app.previewNewAccount()
	waitUIIdle(t, app, posted)
	ttWide.Frame()
	if _, ok := ttWide.Find("新增订阅预览站点"); !ok {
		t.Fatal("expected Douyin site badge in Add Subscription account preview")
	}
	app.AddSubDialogOpen = false

	// Open Subscription Detail -> 账号 tab -> Place #2
	app.OpenSubscriptionDetail(subMixed)
	app.SubDetailTab = "accounts"
	ttWide.Frame()
	if _, ok := ttWide.Find("订阅详情账号站点：bilibili:20001"); !ok {
		t.Fatal("Place #2 missing: Subscription detail Bilibili account site badge")
	}
	if _, ok := ttWide.Find("订阅详情账号站点：douyin:MS4wLjABAAAA_synth_creator_alpha"); !ok {
		t.Fatal("Place #2 missing: Subscription detail Douyin account site badge")
	}

	// 4. Place #5: Task row + Active Douyin live recording row site badges.
	app.Navigate(RouteTasks)
	app.LiveAccounts = []service.LiveAccountState{
		{
			Site: "douyin", AccountKey: "douyin:MS4wLjABAAAA_rec", RecordingKey: "douyin:live:739988",
			UID: "MS4wLjABAAAA_rec", RoomID: "739988", Uname: "抖音直播主", Title: "正在录制抖音直播",
			LiveStatus: 1, Recording: true, QualityName: "原画", Codec: "hevc",
		},
	}
	app.Tasks = []download.Task{
		{TaskID: "t_bili", Site: "bilibili", Kind: "video", TargetID: "BV1a", Title: "B站任务", Status: "running", Progress: 0.4},
		{TaskID: "t_dy", Site: "douyin", Kind: "video", TargetID: "7301", Title: "抖音任务", Status: "running", Progress: 0.6},
	}
	ttWide.Frame()
	for _, label := range []string{"录制站点：douyin:MS4wLjABAAAA_rec", "任务站点：t_bili", "任务站点：t_dy"} {
		if _, ok := ttWide.Find(label); !ok {
			t.Fatalf("Place #5 missing: %q on /tasks", label)
		}
	}

	// 5. Place #6: Recent parse history item site badges (/parse).
	app.Navigate(RouteParse)
	app.HistoryItems = stub.raw.ListHistory()
	ttWide.Frame()
	for _, label := range []string{"历史站点：bilibili:BV1xx411c7mD", "历史站点：douyin:732009"} {
		if _, ok := ttWide.Find(label); !ok {
			t.Fatalf("Place #6 missing: %q on /parse recent history", label)
		}
	}

	// 6. Place #1: User page header site badge on /user (B 站) as well (/douyin/user already checked above).
	app.CurrentUser = nil
	app.CurrentRoute = RouteHome
	app.CurrentDouyinUser = &service.DouyinUserView{SecUID: "MS4wLjABAAAA_u_mark", Nickname: "标记作者"}
	app.CurrentRoute = RouteDouyinUser
	ttWide.Frame()
	if _, ok := ttWide.Find("用户页站点标记"); !ok {
		t.Fatal("Place #1 missing: /douyin/user header site badge")
	}
}
