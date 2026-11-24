package weibo

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/site"
)

func newTestWeiboStore(t *testing.T, rawCookie string) *credential.Store {
	t.Helper()
	store, _, err := credential.OpenWeibo(filepath.Join(t.TempDir(), credential.WeiboFileName))
	if err != nil {
		t.Fatalf("OpenWeibo failed: %v", err)
	}
	if rawCookie != "" {
		cookies, pErr := credential.ParseWeiboCookieInput(rawCookie)
		if pErr != nil {
			t.Fatalf("ParseWeiboCookieInput failed: %v", pErr)
		}
		if _, sErr := store.SaveVerifiedWeiboManual(cookies, credential.WeiboProfile{
			UID:   "1000000001",
			Uname: "合成示例用户",
			Face:  "https://tvax1.sinaimg.cn/crop.0.0.100.100/synthetic.jpg",
		}); sErr != nil {
			t.Fatalf("SaveVerifiedWeiboManual failed: %v", sErr)
		}
	}
	return store
}

func testEndpointsForServer(baseURL string) Endpoints {
	return Endpoints{
		PCBasicInfo:     baseURL + "/ajax/setting/getBasicInfo",
		ProfileInfo:     baseURL + "/ajax/profile/info",
		StatusesShow:    baseURL + "/ajax/statuses/show",
		StatusesLong:    baseURL + "/ajax/statuses/longtext",
		StatusesMyMblog: baseURL + "/ajax/statuses/mymblog",
		TVComponent:     baseURL + "/tv/api/component",
		Friends:         baseURL + "/ajax/friendships/friends",
		FollowContent:   baseURL + "/ajax/profile/followContent",
		AllGroups:       baseURL + "/ajax/feed/allGroups",
		AllFavorites:    baseURL + "/ajax/favorites/all_fav",
		LikeList:        baseURL + "/ajax/statuses/likelist",
	}
}

func newFastTestClient(t *testing.T, baseURL string, store *credential.Store, extra ...func(*ClientOptions)) *Client {
	t.Helper()
	opts := ClientOptions{
		CredStore:      store,
		Endpoints:      testEndpointsForServer(baseURL),
		MinInterval:    -1,
		AllowLocalhost: true,
	}
	for _, fn := range extra {
		fn(&opts)
	}
	return NewClient(opts)
}

func TestClientSerialPacingHeadersAndXSRFSync(t *testing.T) {
	var inFlight atomic.Int32
	var maxInFlight atomic.Int32
	var gotHeadersMu sync.Mutex
	var gotHeaders []http.Header
	var callNum atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := inFlight.Add(1)
		for {
			prev := maxInFlight.Load()
			if cur <= prev || maxInFlight.CompareAndSwap(prev, cur) {
				break
			}
		}
		defer inFlight.Add(-1)

		n := callNum.Add(1)
		gotHeadersMu.Lock()
		gotHeaders = append(gotHeaders, r.Header.Clone())
		gotHeadersMu.Unlock()

		if n == 1 {
			http.SetCookie(w, &http.Cookie{Name: "XSRF-TOKEN", Value: "SYNTHETIC_XSRF_REFRESHED_02"})
			// Non-XSRF Set-Cookie must NOT clobber core SUB in store.
			http.SetCookie(w, &http.Cookie{Name: "SUB", Value: "SHOULD_NOT_OVERWRITE_SUB"})
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":1,"data":{"user":{"idstr":"1000000001","screen_name":"合成示例用户"}}}`))
	}))
	defer srv.Close()

	store := newTestWeiboStore(t, "SUB=SYNTHETIC_SUB_TOKEN_01; SUBP=SYNTHETIC_SUBP_01; XSRF-TOKEN=SYNTHETIC_XSRF_INIT_01")

	var clockMu sync.Mutex
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	var sleeps []time.Duration
	var xsrfUpdates []string

	// Deterministic zero-byte entropy yields 0 jitter -> wait must be exactly 1200ms.
	zeroEntropy := bytes.NewReader(make([]byte, 128))
	client := NewClient(ClientOptions{
		CredStore:      store,
		Endpoints:      testEndpointsForServer(srv.URL),
		AllowLocalhost: true,
		Entropy:        zeroEntropy,
		Now: func() time.Time {
			clockMu.Lock()
			defer clockMu.Unlock()
			return now
		},
		Sleep: func(_ context.Context, d time.Duration) error {
			clockMu.Lock()
			sleeps = append(sleeps, d)
			now = now.Add(d)
			clockMu.Unlock()
			return nil
		},
		OnXSRFToken: func(token string) {
			xsrfUpdates = append(xsrfUpdates, token)
		},
	})

	ctx := context.Background()
	if _, err := client.GetUserProfile(ctx, "1000000001"); err != nil {
		t.Fatalf("first GetUserProfile failed: %v", err)
	}
	if _, err := client.GetUserProfile(ctx, "1000000001"); err != nil {
		t.Fatalf("second GetUserProfile failed: %v", err)
	}

	if maxInFlight.Load() != 1 {
		t.Fatalf("max concurrent requests = %d, want 1", maxInFlight.Load())
	}
	if len(sleeps) != 1 || sleeps[0] != 1200*time.Millisecond {
		t.Fatalf("recorded pacing sleeps = %v, want [1.2s]", sleeps)
	}

	// Verify jitter stays in [1200ms, 1600ms] with non-zero entropy.
	maxEntropy := bytes.NewReader(bytes.Repeat([]byte{0xff}, 64))
	var jitterSleeps []time.Duration
	clientJitter := NewClient(ClientOptions{
		CredStore:      store,
		Endpoints:      testEndpointsForServer(srv.URL),
		AllowLocalhost: true,
		Entropy:        maxEntropy,
		Now: func() time.Time {
			clockMu.Lock()
			defer clockMu.Unlock()
			return now
		},
		Sleep: func(_ context.Context, d time.Duration) error {
			clockMu.Lock()
			jitterSleeps = append(jitterSleeps, d)
			now = now.Add(d)
			clockMu.Unlock()
			return nil
		},
	})
	if _, err := clientJitter.GetUserProfile(ctx, "1000000001"); err != nil {
		t.Fatalf("jitter req 1 failed: %v", err)
	}
	if _, err := clientJitter.GetUserProfile(ctx, "1000000001"); err != nil {
		t.Fatalf("jitter req 2 failed: %v", err)
	}
	if len(jitterSleeps) != 1 || jitterSleeps[0] < 1200*time.Millisecond || jitterSleeps[0] > 1600*time.Millisecond {
		t.Fatalf("jitter sleep = %v, want in [1200ms, 1600ms]", jitterSleeps)
	}

	// Verify PC Ajax headers and XSRF-TOKEN mirroring + Set-Cookie sync.
	gotHeadersMu.Lock()
	h1, h2 := gotHeaders[0], gotHeaders[1]
	gotHeadersMu.Unlock()

	if h1.Get("User-Agent") != DefaultUserAgent {
		t.Fatalf("User-Agent = %q, want %q", h1.Get("User-Agent"), DefaultUserAgent)
	}
	if h1.Get("X-Requested-With") != "XMLHttpRequest" {
		t.Fatalf("X-Requested-With = %q, want XMLHttpRequest", h1.Get("X-Requested-With"))
	}
	if h1.Get("Accept") != "application/json, text/plain, */*" {
		t.Fatalf("Accept = %q", h1.Get("Accept"))
	}
	if !strings.Contains(h1.Get("Accept-Language"), "zh-CN") {
		t.Fatalf("Accept-Language = %q", h1.Get("Accept-Language"))
	}
	if !strings.HasPrefix(h1.Get("Referer"), "https://weibo.com/") {
		t.Fatalf("Referer = %q, want prefix https://weibo.com/", h1.Get("Referer"))
	}
	if h1.Get("X-XSRF-TOKEN") != "SYNTHETIC_XSRF_INIT_01" {
		t.Fatalf("req1 X-XSRF-TOKEN = %q, want SYNTHETIC_XSRF_INIT_01", h1.Get("X-XSRF-TOKEN"))
	}
	if h2.Get("X-XSRF-TOKEN") != "SYNTHETIC_XSRF_REFRESHED_02" {
		t.Fatalf("req2 X-XSRF-TOKEN = %q, want SYNTHETIC_XSRF_REFRESHED_02", h2.Get("X-XSRF-TOKEN"))
	}
	if len(xsrfUpdates) != 1 || xsrfUpdates[0] != "SYNTHETIC_XSRF_REFRESHED_02" {
		t.Fatalf("OnXSRFToken callback = %v, want [SYNTHETIC_XSRF_REFRESHED_02]", xsrfUpdates)
	}
	if store.CookieValue("SUB") != "SYNTHETIC_SUB_TOKEN_01" {
		t.Fatalf("SUB cookie unexpectedly changed to %q", store.CookieValue("SUB"))
	}
	if store.XSRFToken() != "SYNTHETIC_XSRF_REFRESHED_02" {
		t.Fatalf("stored XSRF-TOKEN = %q, want SYNTHETIC_XSRF_REFRESHED_02", store.XSRFToken())
	}
}

func TestClientResponseBodyCapAndNoAutoRedirect(t *testing.T) {
	var redirectHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ajax/profile/info":
			if r.URL.Query().Get("uid") == "1000000002" {
				// Oversized response > 4 MiB cap (tested with smaller MaxBodyBytes override).
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(bytes.Repeat([]byte("a"), 2048))
				return
			}
			redirectHits.Add(1)
			http.Redirect(w, r, "/redirected-never-follow", http.StatusFound)
		case "/redirected-never-follow":
			redirectHits.Add(100)
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	store := newTestWeiboStore(t, "SUB=SYNTHETIC_SUB_01")
	client := newFastTestClient(t, srv.URL, store, func(o *ClientOptions) {
		o.MaxBodyBytes = 1024
	})

	_, err := client.GetUserProfile(context.Background(), "1000000001")
	if !site.IsKind(err, site.ErrKindRiskControl) {
		t.Fatalf("302 unexpected redirect err = %v, want risk_control", err)
	}
	if redirectHits.Load() != 1 {
		t.Fatalf("auto-redirect followed! hits = %d, want 1", redirectHits.Load())
	}

	_, err = client.GetUserProfile(context.Background(), "1000000002")
	if !site.IsKind(err, site.ErrKindBlocked) {
		t.Fatalf("oversized body err = %v, want blocked", err)
	}
}

func TestErrorClassificationMatrixZeroRetryAndCredPreservation(t *testing.T) {
	cases := []struct {
		name             string
		status           int
		headers          map[string]string
		body             string
		wantKind         string
		wantRoundStop    bool
		wantCircuitBreak bool
		wantCredExpired  bool
	}{
		{
			name:             "http_302_login_newlogin",
			status:           http.StatusFound,
			headers:          map[string]string{"Location": "https://weibo.com/newlogin?url=https%3A%2F%2Fweibo.com"},
			wantKind:         site.ErrKindAuthRequired,
			wantRoundStop:    true,
			wantCircuitBreak: false,
			wantCredExpired:  true,
		},
		{
			name:             "http_301_login_sina",
			status:           http.StatusMovedPermanently,
			headers:          map[string]string{"Location": "https://login.sina.com.cn/sso/login.php"},
			wantKind:         site.ErrKindAuthRequired,
			wantRoundStop:    true,
			wantCircuitBreak: false,
			wantCredExpired:  true,
		},
		{
			name:             "http_302_visitor_with_cookie",
			status:           http.StatusFound,
			headers:          map[string]string{"Location": "https://passport.weibo.com/visitor/visitor?entry=miniblog"},
			wantKind:         site.ErrKindRiskControl,
			wantRoundStop:    true,
			wantCircuitBreak: true,
			wantCredExpired:  false,
		},
		{
			name:             "http_302_mobile_visitor_with_cookie",
			status:           http.StatusFound,
			headers:          map[string]string{"Location": "https://visitor.passport.weibo.cn/visitor/visitor"},
			wantKind:         site.ErrKindRiskControl,
			wantRoundStop:    true,
			wantCircuitBreak: true,
			wantCredExpired:  false,
		},
		{
			name:             "http_302_other_location",
			status:           http.StatusFound,
			headers:          map[string]string{"Location": "https://weibo.com/unexpected"},
			wantKind:         site.ErrKindRiskControl,
			wantRoundStop:    true,
			wantCircuitBreak: true,
			wantCredExpired:  false,
		},
		{
			name:             "http_414_rate_limited",
			status:           http.StatusRequestURITooLong,
			body:             `Request-URI Too Large`,
			wantKind:         site.ErrKindRateLimited,
			wantRoundStop:    true,
			wantCircuitBreak: true,
			wantCredExpired:  false,
		},
		{
			name:             "http_418_risk_control",
			status:           http.StatusTeapot,
			body:             `blocked`,
			wantKind:         site.ErrKindRiskControl,
			wantRoundStop:    true,
			wantCircuitBreak: true,
			wantCredExpired:  false,
		},
		{
			name:             "http_429_rate_limited",
			status:           http.StatusTooManyRequests,
			body:             `too many requests`,
			wantKind:         site.ErrKindRateLimited,
			wantRoundStop:    true,
			wantCircuitBreak: true,
			wantCredExpired:  false,
		},
		{
			name:             "http_432_risk_control",
			status:           432,
			body:             `{"ok":0}`,
			wantKind:         site.ErrKindRiskControl,
			wantRoundStop:    true,
			wantCircuitBreak: true,
			wantCredExpired:  false,
		},
		{
			name:             "http_401_auth_required",
			status:           http.StatusUnauthorized,
			body:             `unauthorized`,
			wantKind:         site.ErrKindAuthRequired,
			wantRoundStop:    true,
			wantCircuitBreak: false,
			wantCredExpired:  true,
		},
		{
			name:             "http_403_waf_risk_control",
			status:           http.StatusForbidden,
			body:             `<html>Forbidden</html>`,
			wantKind:         site.ErrKindRiskControl,
			wantRoundStop:    true,
			wantCircuitBreak: true,
			wantCredExpired:  false,
		},
		{
			name:             "http_400_bad_request",
			status:           http.StatusBadRequest,
			body:             `{"ok":0,"msg":"bad param"}`,
			wantKind:         site.ErrKindBadRequest,
			wantRoundStop:    false,
			wantCircuitBreak: false,
			wantCredExpired:  false,
		},
		{
			name:             "http_502_network",
			status:           http.StatusBadGateway,
			body:             `bad gateway`,
			wantKind:         site.ErrKindNetwork,
			wantRoundStop:    false,
			wantCircuitBreak: false,
			wantCredExpired:  false,
		},
		{
			name:             "http_200_zero_byte_risk_control",
			status:           http.StatusOK,
			body:             ``,
			wantKind:         site.ErrKindRiskControl,
			wantRoundStop:    true,
			wantCircuitBreak: true,
			wantCredExpired:  false,
		},
		{
			name:             "http_200_html_sina_visitor_system",
			status:           http.StatusOK,
			headers:          map[string]string{"Content-Type": "text/html; charset=utf-8"},
			body:             `<!doctype html><html><head><title>Sina Visitor System</title></head></html>`,
			wantKind:         site.ErrKindRiskControl,
			wantRoundStop:    true,
			wantCircuitBreak: true,
			wantCredExpired:  false,
		},
		{
			name:             "http_200_html_geetest_slider",
			status:           http.StatusOK,
			headers:          map[string]string{"Content-Type": "text/html"},
			body:             `<html><body>请完成极验 geetest 滑块安全验证</body></html>`,
			wantKind:         site.ErrKindRiskControl,
			wantRoundStop:    true,
			wantCircuitBreak: true,
			wantCredExpired:  false,
		},
		{
			name:             "http_200_html_login_page",
			status:           http.StatusOK,
			headers:          map[string]string{"Content-Type": "text/html"},
			body:             `<html><title>微博登录</title><a href="https://weibo.com/newlogin">登录</a></html>`,
			wantKind:         site.ErrKindAuthRequired,
			wantRoundStop:    true,
			wantCircuitBreak: false,
			wantCredExpired:  true,
		},
		{
			name:             "json_ok_minus_100_auth_required",
			status:           http.StatusOK,
			headers:          map[string]string{"Content-Type": "application/json"},
			body:             `{"ok":-100,"url":"https://passport.weibo.com/sso/login"}`,
			wantKind:         site.ErrKindAuthRequired,
			wantRoundStop:    true,
			wantCircuitBreak: false,
			wantCredExpired:  true,
		},
		{
			name:             "json_ok_0_code_20003_user_not_found",
			status:           http.StatusOK,
			body:             `{"ok":0,"error_code":20003,"msg":"用户不存在"}`,
			wantKind:         site.ErrKindUnavailable,
			wantRoundStop:    false,
			wantCircuitBreak: false,
			wantCredExpired:  false,
		},
		{
			name:             "json_ok_0_code_20101_post_deleted",
			status:           http.StatusOK,
			body:             `{"ok":0,"errno":20101,"msg":"微博不存在"}`,
			wantKind:         site.ErrKindUnavailable,
			wantRoundStop:    false,
			wantCircuitBreak: false,
			wantCredExpired:  false,
		},
		{
			name:             "json_ok_0_code_20112_forbidden",
			status:           http.StatusOK,
			body:             `{"ok":0,"error_code":20112,"msg":"无查看权限"}`,
			wantKind:         site.ErrKindForbidden,
			wantRoundStop:    false,
			wantCircuitBreak: false,
			wantCredExpired:  false,
		},
		{
			name:             "json_ok_0_rate_limit_msg",
			status:           http.StatusOK,
			body:             `{"ok":0,"msg":"请求过于频繁，请稍后再试"}`,
			wantKind:         site.ErrKindRateLimited,
			wantRoundStop:    true,
			wantCircuitBreak: true,
			wantCredExpired:  false,
		},
		{
			name:             "json_ok_0_forbidden_msg",
			status:           http.StatusOK,
			body:             `{"ok":0,"msg":"由于作者设置，你暂时没有这条微博的查看权限，仅粉丝可见"}`,
			wantKind:         site.ErrKindForbidden,
			wantRoundStop:    false,
			wantCircuitBreak: false,
			wantCredExpired:  false,
		},
		{
			name:             "json_ok_0_deleted_msg",
			status:           http.StatusOK,
			body:             `{"ok":0,"msg":"抱歉，此微博已被作者删除"}`,
			wantKind:         site.ErrKindUnavailable,
			wantRoundStop:    false,
			wantCircuitBreak: false,
			wantCredExpired:  false,
		},
		{
			name:             "json_ok_0_captcha_challenge",
			status:           http.StatusOK,
			body:             `{"ok":0,"msg":"请完成安全验证","url":"https://weibo.com/security/verify?type=geetest"}`,
			wantKind:         site.ErrKindRiskControl,
			wantRoundStop:    true,
			wantCircuitBreak: true,
			wantCredExpired:  false,
		},
		{
			name:             "json_malformed_schema_changed",
			status:           http.StatusOK,
			body:             `{"ok":1,`,
			wantKind:         site.ErrKindSchemaChanged,
			wantRoundStop:    false,
			wantCircuitBreak: false,
			wantCredExpired:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				for k, v := range tc.headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			store := newTestWeiboStore(t, "SUB=SYNTHETIC_SUB_KEEP_01")
			client := newFastTestClient(t, srv.URL, store)

			var autoErr error
			client.SetAutomaticRequestHandlers(nil, func(_ string, err error) {
				autoErr = err
			})

			ctx := site.WithAutomaticRequests(context.Background(), "test_round")
			_, err := client.GetUserProfile(ctx, "1000000001")
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if hits.Load() != 1 {
				t.Fatalf("request count = %d, want 1 (zero auto-retry)", hits.Load())
			}
			if site.KindOf(err) != tc.wantKind {
				t.Fatalf("KindOf(err) = %q, want %q (err=%v)", site.KindOf(err), tc.wantKind, err)
			}
			if site.IsRoundStop(err) != tc.wantRoundStop {
				t.Fatalf("IsRoundStop = %v, want %v", site.IsRoundStop(err), tc.wantRoundStop)
			}
			if site.IsCircuitBreak(err) != tc.wantCircuitBreak {
				t.Fatalf("IsCircuitBreak = %v, want %v", site.IsCircuitBreak(err), tc.wantCircuitBreak)
			}
			if autoErr == nil {
				t.Fatalf("expected automatic breaker callback to receive error")
			}

			st := store.Status()
			if tc.wantCredExpired {
				if st.State != "expired" {
					t.Fatalf("credential state = %q, want expired", st.State)
				}
			} else {
				if !st.LoggedIn || st.State != "logged_in" {
					t.Fatalf("credential must stay active on %s, got state=%q loggedIn=%v", tc.wantKind, st.State, st.LoggedIn)
				}
			}
		})
	}
}

func TestFetchImageCookielessAndRejectsDisguisedHTML(t *testing.T) {
	var seenCookie string
	var seenReferer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenCookie = r.Header.Get("Cookie")
		seenReferer = r.Header.Get("Referer")
		switch r.URL.Path {
		case "/ok.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10})
		case "/disguised.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("<!DOCTYPE html><html><body>blocked</body></html>"))
		}
	}))
	defer srv.Close()

	store := newTestWeiboStore(t, "SUB=SYNTHETIC_SUB_IMG_01")
	client := newFastTestClient(t, srv.URL, store)

	data, ct, err := client.FetchImage(context.Background(), srv.URL+"/ok.jpg")
	if err != nil {
		t.Fatalf("FetchImage failed: %v", err)
	}
	if len(data) != 6 || ct != "image/jpeg" {
		t.Fatalf("FetchImage = (%d bytes, %q)", len(data), ct)
	}
	if seenCookie != "" {
		t.Fatalf("CDN request leaked Cookie header: %q", seenCookie)
	}
	if seenReferer != "https://weibo.com/" {
		t.Fatalf("CDN Referer = %q, want https://weibo.com/", seenReferer)
	}

	if _, _, err := client.FetchImage(context.Background(), srv.URL+"/disguised.jpg"); !site.IsKind(err, site.ErrKindBlocked) {
		t.Fatalf("disguised HTML image err = %v, want blocked", err)
	}
	if _, _, err := client.FetchImage(context.Background(), "https://evil.example.com/x.jpg"); !site.IsKind(err, site.ErrKindBlocked) {
		t.Fatalf("non-allowlisted CDN host err = %v, want blocked", err)
	}
}

func TestProbeRawReturnsUnfilteredStatusAndBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" {
			t.Errorf("guestNoCookie probe unexpectedly sent Cookie")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":-100,"url":"https://passport.weibo.com/sso/login"}`))
	}))
	defer srv.Close()

	client := newFastTestClient(t, srv.URL, nil)
	res, err := client.ProbeRaw(context.Background(), http.MethodGet, srv.URL+"/ajax/setting/getBasicInfo", nil, "", true, "")
	if err != nil {
		t.Fatalf("ProbeRaw failed: %v", err)
	}
	if res.StatusCode != http.StatusOK || !strings.Contains(string(res.Body), `"ok":-100`) {
		t.Fatalf("unexpected ProbeRaw result: status=%d body=%s", res.StatusCode, string(res.Body))
	}

	// Ensure context cancellation returns network error cleanly.
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_, cErr := client.ProbeRaw(cancelCtx, http.MethodGet, srv.URL+"/ajax/setting/getBasicInfo", nil, "", true, "")
	if !errors.Is(cErr, context.Canceled) {
		t.Fatalf("cancelled ProbeRaw err = %v, want context.Canceled", cErr)
	}
}

type trackingReadCloser struct {
	io.Reader
	bytesRead atomic.Int64
	closed    atomic.Bool
}

func (r *trackingReadCloser) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.bytesRead.Add(int64(n))
	return n, err
}

func (r *trackingReadCloser) Close() error {
	r.closed.Store(true)
	return nil
}

func TestFetchImageAllowsLargeOriginalsAboveJSONBodyCapAndBoundsOversize(t *testing.T) {
	// 5 MiB synthetic JPEG payload (> 4 MiB defaultMaxBodyBytes) must succeed on default Weibo Client.
	const fiveMiB = 5 * 1024 * 1024
	largeJPEG := make([]byte, fiveMiB)
	copy(largeJPEG, []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10})
	largeJPEG[len(largeJPEG)-2] = 0xff
	largeJPEG[len(largeJPEG)-1] = 0xd9

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/large-original.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			w.Header().Set("Content-Length", strconv.Itoa(len(largeJPEG)))
			_, _ = w.Write(largeJPEG)
		case "/chunked-over-custom-cap.gif":
			w.Header().Set("Content-Type", "image/gif")
			// Flush headers without Content-Length so chunked read path is exercised.
			w.(http.Flusher).Flush()
			_, _ = w.Write(bytes.Repeat([]byte("G"), 2048))
		}
	}))
	defer srv.Close()

	store := newTestWeiboStore(t, "SUB=SYNTHETIC_SUB_IMG_LARGE")
	defaultClient := newFastTestClient(t, srv.URL, store)
	if defaultClient.maxImageBytes != site.DefaultMaxImageBytes {
		t.Fatalf("defaultClient.maxImageBytes = %d, want %d", defaultClient.maxImageBytes, site.DefaultMaxImageBytes)
	}
	if defaultClient.maxBodyBytes != defaultMaxBodyBytes {
		t.Fatalf("defaultClient.maxBodyBytes = %d, want %d", defaultClient.maxBodyBytes, defaultMaxBodyBytes)
	}

	got, ct, err := defaultClient.FetchImage(context.Background(), srv.URL+"/large-original.jpg")
	if err != nil {
		t.Fatalf("FetchImage(5 MiB original) failed: %v", err)
	}
	if len(got) != fiveMiB || ct != "image/jpeg" {
		t.Fatalf("FetchImage(5 MiB original) = (%d bytes, %q), want (%d, image/jpeg)", len(got), ct, fiveMiB)
	}

	// Declared Content-Length exceeding maxBytes must fail fast BEFORE reading response body bytes.
	tracked := &trackingReadCloser{Reader: bytes.NewReader(bytes.Repeat([]byte("X"), 4096))}
	customClient := NewClient(ClientOptions{
		CredStore:      store,
		MinInterval:    -1,
		AllowLocalhost: true,
		MaxImageBytes:  1024,
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode:    http.StatusOK,
				ContentLength: 4096,
				Header:        http.Header{"Content-Type": []string{"image/png"}},
				Body:          tracked,
				Request:       req,
			}, nil
		}),
	})
	_, _, err = customClient.FetchImage(context.Background(), "https://wx1.sinaimg.cn/large/001SynThOversize.png")
	if !site.IsKind(err, site.ErrKindBlocked) || !strings.Contains(err.Error(), "微博图片体积超出安全上限") {
		t.Fatalf("declared oversize FetchImage err = %v, want blocked 安全上限", err)
	}
	if tracked.bytesRead.Load() != 0 || !tracked.closed.Load() {
		t.Fatalf("declared oversize body must close with 0 bytes read, got read=%d closed=%v", tracked.bytesRead.Load(), tracked.closed.Load())
	}

	// Chunked/unknown Content-Length exceeding custom MaxImageBytes also returns blocked 安全上限.
	smallCapClient := NewClient(ClientOptions{
		CredStore:      store,
		MinInterval:    -1,
		AllowLocalhost: true,
		MaxImageBytes:  1024,
	})
	_, _, err = smallCapClient.FetchImage(context.Background(), srv.URL+"/chunked-over-custom-cap.gif")
	if !site.IsKind(err, site.ErrKindBlocked) || !strings.Contains(err.Error(), "微博图片体积超出安全上限") {
		t.Fatalf("chunked oversize FetchImage err = %v, want blocked 安全上限", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
