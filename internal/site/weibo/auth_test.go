package weibo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/site"
)

// syntheticBasicInfoFixture mirrors the exact 18-field PC /ajax/setting/getBasicInfo response
// observed during ACT-022 login confirmation (screen_name + domain present, NO uid or avatar).
const syntheticBasicInfoFixture = `{
  "ok": 1,
  "data": {
    "description": "合成简介",
    "auth_realname": "",
    "gender": "m",
    "province": "11",
    "eduction": "",
    "auth_career": "",
    "tips_url": "",
    "screen_name": "合成示例用户",
    "birthday_visible": 0,
    "city": "1",
    "hometown_province": "",
    "single": 0,
    "career": "",
    "auth_status": 0,
    "domain": "synthetic_creator",
    "birthday": "",
    "hometown_city": "",
    "auth_tips": ""
  }
}`

func TestLoginWithCookiePCFlowSavesVerifiedProfileAndStripsStats(t *testing.T) {
	var basicInfoCookie string
	var profileCookie string
	var queriedNick string
	var xsrfHeaders []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		xsrfHeaders = append(xsrfHeaders, r.Header.Get("X-XSRF-TOKEN"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/ajax/setting/getBasicInfo":
			basicInfoCookie = r.Header.Get("Cookie")
			http.SetCookie(w, &http.Cookie{Name: "XSRF-TOKEN", Value: "SYNTHETIC_XSRF_STEP1"})
			_, _ = w.Write([]byte(syntheticBasicInfoFixture))
		case "/ajax/profile/info":
			profileCookie = r.Header.Get("Cookie")
			queriedNick = r.URL.Query().Get("screen_name")
			_, _ = w.Write([]byte(`{
				"ok": 1,
				"data": {
					"user": {
						"id": 1000000001,
						"idstr": "1000000001",
						"screen_name": "合成示例用户",
						"avatar_hd": "https://tvax1.sinaimg.cn/crop.0.0.180.180/synthetic_hd.jpg",
						"avatar_large": "https://tvax1.sinaimg.cn/crop.0.0.100.100/synthetic_lg.jpg",
						"domain": "synthetic_creator"
					}
				}
			}`))
		default:
			t.Errorf("unexpected endpoint hit: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	store := newTestWeiboStore(t, "")
	client := newFastTestClient(t, srv.URL, store)

	rawInput := "Cookie: SINAGLOBAL=track_drop_me; SUB=SYNTHETIC_SUB_VALID_001; SUBP=SYNTHETIC_SUBP_001; XSRF-TOKEN=SYNTHETIC_XSRF_000; UOR=ad_drop_me; WBStorage=ui_drop_me"
	st, err := client.LoginWithCookie(context.Background(), rawInput)
	if err != nil {
		t.Fatalf("LoginWithCookie failed: %v", err)
	}
	if !st.LoggedIn || st.State != "logged_in" || st.Source != "manual" {
		t.Fatalf("unexpected login status: %+v", st)
	}
	if st.UID != "1000000001" || st.Uname != "合成示例用户" {
		t.Fatalf("unexpected profile: uid=%q uname=%q", st.UID, st.Uname)
	}
	if st.Face != "https://tvax1.sinaimg.cn/crop.0.0.180.180/synthetic_hd.jpg" {
		t.Fatalf("unexpected avatar: %q", st.Face)
	}
	if queriedNick != "合成示例用户" {
		t.Fatalf("profile/info queried screen_name = %q, want 合成示例用户", queriedNick)
	}
	// Verify stats/UI cookies were stripped before network call!
	for _, stripped := range []string{"SINAGLOBAL", "UOR", "WBStorage"} {
		if strings.Contains(basicInfoCookie, stripped) || strings.Contains(profileCookie, stripped) {
			t.Fatalf("stats cookie %s leaked to upstream request", stripped)
		}
	}
	// Verify refreshed XSRF-TOKEN from step 1 was forwarded to step 2 and saved in store!
	if len(xsrfHeaders) != 2 || xsrfHeaders[0] != "SYNTHETIC_XSRF_000" || xsrfHeaders[1] != "SYNTHETIC_XSRF_STEP1" {
		t.Fatalf("X-XSRF-TOKEN progression = %v, want [SYNTHETIC_XSRF_000 SYNTHETIC_XSRF_STEP1]", xsrfHeaders)
	}
	if store.XSRFToken() != "SYNTHETIC_XSRF_STEP1" {
		t.Fatalf("stored XSRF-TOKEN = %q, want SYNTHETIC_XSRF_STEP1", store.XSRFToken())
	}
}

func TestLoginWithCookieFailureClassesPreserveExistingCredAndNeverEchoSecret(t *testing.T) {
	t.Run("invalid_input_zero_requests_no_echo", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		store := newTestWeiboStore(t, "SUB=EXISTING_VALID_SUB_KEEP")
		client := newFastTestClient(t, srv.URL, store)

		inputs := []struct {
			name   string
			raw    string
			secret string
		}{
			{name: "missing_sub", raw: "SUBP=SECRET_SUBP_NEVER_ECHO; XSRF-TOKEN=SECRET_XSRF_NEVER_ECHO", secret: "SECRET_SUBP_NEVER_ECHO"},
			{name: "over_64kib", raw: "SUB=SECRET_OVER_64K_" + strings.Repeat("x", credential.MaxCookieInputBytes+64), secret: "SECRET_OVER_64K_"},
			{name: "cleaned_over_32kib", raw: "SUB=SECRET_CLEAN_32K_" + strings.Repeat("y", credential.MaxCleanedCookieHeaderBytes+64), secret: "SECRET_CLEAN_32K_"},
			{name: "control_char_nul", raw: "SUB=SECRET_CTL_\x00_VALUE", secret: "SECRET_CTL_"},
			{name: "cookie_header_crlf_injection", raw: "Cookie: SUB=SECRET_INJ_VAL\r\nX-Evil: 1", secret: "SECRET_INJ_VAL"},
		}
		for _, tc := range inputs {
			_, err := client.LoginWithCookie(context.Background(), tc.raw)
			if !site.IsKind(err, site.ErrKindInvalidInput) {
				t.Fatalf("%s: err = %v, want invalid_input", tc.name, err)
			}
			if strings.Contains(err.Error(), tc.secret) {
				t.Fatalf("%s: error echoed secret value: %v", tc.name, err)
			}
		}
		if hits.Load() != 0 {
			t.Fatalf("invalid_input issued %d network requests, want 0", hits.Load())
		}
		if store.CookieValue("SUB") != "EXISTING_VALID_SUB_KEEP" {
			t.Fatalf("existing credential was modified")
		}
	})

	t.Run("unauthenticated_guest_and_mismatch_fail_as_auth_required", func(t *testing.T) {
		cases := []struct {
			name        string
			basicStatus int
			basicLoc    string
			basicBody   string
			profileBody string
			wantHits    int32
		}{
			{
				name:        "guest_sub_ok_minus_100",
				basicStatus: http.StatusOK,
				basicBody:   `{"ok":-100,"url":"https://passport.weibo.com/sso/login"}`,
				wantHits:    1,
			},
			{
				name:        "guest_sub_302_login",
				basicStatus: http.StatusFound,
				basicLoc:    "https://weibo.com/newlogin",
				wantHits:    1,
			},
			{
				name:        "guest_sub_empty_screen_name",
				basicStatus: http.StatusOK,
				basicBody:   `{"ok":1,"data":{"screen_name":""}}`,
				wantHits:    1,
			},
			{
				name:        "screen_name_mismatch_not_saved",
				basicStatus: http.StatusOK,
				basicBody:   syntheticBasicInfoFixture,
				profileBody: `{"ok":1,"data":{"user":{"idstr":"1000000099","screen_name":"别的账号昵称"}}}`,
				wantHits:    2,
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				var hits atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					w.Header().Set("Content-Type", "application/json")
					if r.URL.Path == "/ajax/setting/getBasicInfo" {
						if tc.basicLoc != "" {
							w.Header().Set("Location", tc.basicLoc)
						}
						w.WriteHeader(tc.basicStatus)
						_, _ = w.Write([]byte(tc.basicBody))
						return
					}
					_, _ = w.Write([]byte(tc.profileBody))
				}))
				defer srv.Close()

				store := newTestWeiboStore(t, "SUB=EXISTING_VALID_SUB_KEEP")
				client := newFastTestClient(t, srv.URL, store)

				_, err := client.LoginWithCookie(context.Background(), "SUB=SYNTHETIC_GUEST_SUB_001")
				if !site.IsKind(err, site.ErrKindAuthRequired) {
					t.Fatalf("err = %v, want auth_required", err)
				}
				if hits.Load() != tc.wantHits {
					t.Fatalf("hits = %d, want %d (must not fall back to another endpoint)", hits.Load(), tc.wantHits)
				}
				if store.CookieValue("SUB") != "EXISTING_VALID_SUB_KEEP" || !store.Status().LoggedIn {
					t.Fatalf("existing credential was unexpectedly altered: %+v", store.Status())
				}
			})
		}
	})

	t.Run("risk_control_and_rate_limit_preserve_existing_cred", func(t *testing.T) {
		cases := []struct {
			name     string
			status   int
			ct       string
			body     string
			wantKind string
		}{
			{name: "http_432", status: 432, ct: "application/json", body: `{"ok":0}`, wantKind: site.ErrKindRiskControl},
			{name: "http_418", status: http.StatusTeapot, ct: "text/plain", body: `blocked`, wantKind: site.ErrKindRiskControl},
			{name: "html_verify_page", status: http.StatusOK, ct: "text/html", body: `<html><body>geetest 滑块验证码</body></html>`, wantKind: site.ErrKindRiskControl},
			{name: "http_414", status: http.StatusRequestURITooLong, ct: "text/plain", body: `rate limit`, wantKind: site.ErrKindRateLimited},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				var hits atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					hits.Add(1)
					w.Header().Set("Content-Type", tc.ct)
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
				}))
				defer srv.Close()

				store := newTestWeiboStore(t, "SUB=EXISTING_VALID_SUB_KEEP")
				client := newFastTestClient(t, srv.URL, store)

				_, err := client.LoginWithCookie(context.Background(), "SUB=SYNTHETIC_NEW_SUB_001")
				if !site.IsKind(err, tc.wantKind) {
					t.Fatalf("err = %v, want %s", err, tc.wantKind)
				}
				if hits.Load() != 1 {
					t.Fatalf("hits = %d, want 1", hits.Load())
				}
				if store.CookieValue("SUB") != "EXISTING_VALID_SUB_KEEP" || !store.Status().LoggedIn {
					t.Fatalf("existing credential was modified on %s", tc.wantKind)
				}
			})
		}
	})
}

func TestGuestControlAndVerifyCredentialLifecycle(t *testing.T) {
	var mode atomic.Int32 // 0 = ok, 1 = 432 risk control, 2 = ok:-100 expired
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") == "" {
			// Unauthenticated control request must fail as auth_required.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":-100,"url":"https://passport.weibo.com/sso/login"}`))
			return
		}
		switch mode.Load() {
		case 1:
			w.WriteHeader(432)
			_, _ = w.Write([]byte(`{"ok":0}`))
			return
		case 2:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":-100}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/ajax/setting/getBasicInfo" {
			_, _ = w.Write([]byte(syntheticBasicInfoFixture))
			return
		}
		_, _ = w.Write([]byte(`{"ok":1,"data":{"user":{"idstr":"1000000001","screen_name":"合成示例用户","avatar_hd":"https://tvax1.sinaimg.cn/avatar.jpg"}}}`))
	}))
	defer srv.Close()

	store := newTestWeiboStore(t, "SUB=SYNTHETIC_ACTIVE_SUB")
	client := newFastTestClient(t, srv.URL, store)

	// No-cookie control fails as auth_required without expiring stored credential.
	if _, err := client.CheckBasicInfoGuest(context.Background()); !site.IsKind(err, site.ErrKindAuthRequired) {
		t.Fatalf("CheckBasicInfoGuest err = %v, want auth_required", err)
	}
	if !store.Status().LoggedIn {
		t.Fatalf("guest control must not expire stored credential")
	}

	// VerifyCredential succeeds on PC 2-step flow.
	st, err := client.VerifyCredential(context.Background())
	if err != nil || !st.LoggedIn || st.UID != "1000000001" {
		t.Fatalf("VerifyCredential = (%+v, %v)", st, err)
	}

	// VerifyCredential on 432 risk_control preserves active credential.
	mode.Store(1)
	if _, err := client.VerifyCredential(context.Background()); !site.IsKind(err, site.ErrKindRiskControl) {
		t.Fatalf("VerifyCredential(432) err = %v, want risk_control", err)
	}
	if !store.Status().LoggedIn {
		t.Fatalf("risk_control cleared or expired active credential")
	}

	// VerifyCredential on ok:-100 marks credential expired.
	mode.Store(2)
	if _, err := client.VerifyCredential(context.Background()); !site.IsKind(err, site.ErrKindAuthRequired) {
		t.Fatalf("VerifyCredential(-100) err = %v, want auth_required", err)
	}
	if store.Status().State != "expired" {
		t.Fatalf("expected credential state=expired, got %q", store.Status().State)
	}

	// Logout clears credential cleanly.
	if err := client.LogoutCredential(); err != nil {
		t.Fatalf("LogoutCredential failed: %v", err)
	}
	if client.HasAuth() {
		t.Fatalf("HasAuth should be false after logout")
	}
}
