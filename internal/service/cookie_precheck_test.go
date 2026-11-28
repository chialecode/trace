package service

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/uistate"
)

func hasPrecheckField(fields []CookiePrecheckField, key string) bool {
	for _, f := range fields {
		if f.Key == key {
			return true
		}
	}
	return false
}

func TestPrecheckCookieInputAllSites(t *testing.T) {
	// Empty input produces empty severity across all sites.
	for _, s := range []string{"bilibili", "douyin", "weibo", "x"} {
		res := PrecheckCookieInput(s, "   \t\n  ")
		if res.Severity != CookiePrecheckEmpty {
			t.Fatalf("%s empty input: severity=%q, want empty", s, res.Severity)
		}
	}

	// Oversized input (>64 KiB) returns error without echoing input.
	huge := "sessionid=" + strings.Repeat("a", credential.MaxCookieInputBytes+10)
	if res := PrecheckCookieInput("douyin", huge); res.Severity != CookiePrecheckError || !strings.Contains(res.Summary, "64 KiB") {
		t.Fatalf("oversized douyin precheck = %+v, want 64 KiB error", res)
	}

	// --- Douyin ---
	// Complete with any of the 6 UIFID variants -> OK.
	for _, uifidKey := range douyinUIFIDKeyVariants {
		raw := "sessionid=synth_sid; s_v_web_id=verify_synth; ttwid=synth_ttwid; " + uifidKey + "=synth_uifid"
		res := PrecheckCookieInput("douyin", raw)
		if res.Severity != CookiePrecheckOK {
			t.Fatalf("douyin complete (%s): severity=%q summary=%q missingOpt=%+v", uifidKey, res.Severity, res.Summary, res.MissingOptional)
		}
	}
	// Missing s_v_web_id, ttwid, UIFID -> Warning naming all 3.
	if res := PrecheckCookieInput("douyin", "sessionid=synth_sid"); res.Severity != CookiePrecheckWarning ||
		!hasPrecheckField(res.MissingOptional, "s_v_web_id") ||
		!hasPrecheckField(res.MissingOptional, "ttwid") ||
		!hasPrecheckField(res.MissingOptional, "UIFID") ||
		res.Guidance == "" {
		t.Fatalf("douyin missing all auxiliary fields = %+v", res)
	}
	// Missing only s_v_web_id -> Warning mentioning 喜欢.
	if res := PrecheckCookieInput("douyin", "sessionid_ss=synth_sid; ttwid=synth_t; UIFID=synth_u"); res.Severity != CookiePrecheckWarning ||
		len(res.MissingOptional) != 1 || res.MissingOptional[0].Key != "s_v_web_id" ||
		!strings.Contains(res.MissingOptional[0].Reason, "喜欢") {
		t.Fatalf("douyin missing s_v_web_id = %+v", res)
	}
	// Guest-only cookie -> Error with guest notice.
	if res := PrecheckCookieInput("douyin", "ttwid=synth_guest; msToken=synth_ms"); res.Severity != CookiePrecheckError ||
		!hasPrecheckField(res.MissingRequired, "sessionid / sessionid_ss") ||
		len(res.Notices) == 0 || !strings.Contains(res.Notices[0], "访客") {
		t.Fatalf("douyin guest-only = %+v", res)
	}
	// Non key=value gibberish -> Error with format notice.
	if res := PrecheckCookieInput("douyin", "not_a_cookie_pair"); res.Severity != CookiePrecheckError ||
		len(res.Notices) == 0 || !strings.Contains(res.Notices[0], "key=value") {
		t.Fatalf("douyin non-kv = %+v", res)
	}

	// --- Weibo ---
	if res := PrecheckCookieInput("weibo", "SUB=synth_sub; SUBP=synth_subp; XSRF-TOKEN=synth_xsrf; WBPSESS=synth_wbp"); res.Severity != CookiePrecheckOK {
		t.Fatalf("weibo complete = %+v, want ok", res)
	}
	if res := PrecheckCookieInput("weibo", "SUB=synth_sub"); res.Severity != CookiePrecheckWarning ||
		!hasPrecheckField(res.MissingOptional, "SUBP") ||
		!hasPrecheckField(res.MissingOptional, "XSRF-TOKEN") ||
		!hasPrecheckField(res.MissingOptional, "WBPSESS") {
		t.Fatalf("weibo missing auxiliary = %+v", res)
	}
	if res := PrecheckCookieInput("weibo", "sub=synth_lower; SUBP=synth_subp"); res.Severity != CookiePrecheckError ||
		!hasPrecheckField(res.MissingRequired, "SUB") ||
		len(res.Notices) == 0 || !strings.Contains(res.Notices[0], "区分大小写") {
		t.Fatalf("weibo lowercase sub = %+v", res)
	}
	if res := PrecheckCookieInput("weibo", "Cookie: SUB=a\nInjected: 1"); res.Severity != CookiePrecheckError ||
		!strings.Contains(res.Summary, "控制字符") {
		t.Fatalf("weibo control char = %+v", res)
	}

	// --- X ---
	if res := PrecheckCookieInput("x", "auth_token=synth_auth; ct0=AbCd1234EfGh; twid=u%3D123456789; guest_id=v1%3A12345"); res.Severity != CookiePrecheckOK {
		t.Fatalf("x complete = %+v, want ok", res)
	}
	if res := PrecheckCookieInput("x", "auth_token=synth_auth; ct0=AbCd1234EfGh"); res.Severity != CookiePrecheckWarning ||
		!hasPrecheckField(res.MissingOptional, "twid") ||
		!hasPrecheckField(res.MissingOptional, "guest_id") {
		t.Fatalf("x missing twid/guest_id = %+v", res)
	}
	if res := PrecheckCookieInput("x", "guest_id=v1%3A99"); res.Severity != CookiePrecheckError ||
		!hasPrecheckField(res.MissingRequired, "auth_token") ||
		!hasPrecheckField(res.MissingRequired, "ct0") {
		t.Fatalf("x missing auth_token and ct0 = %+v", res)
	}
	if res := PrecheckCookieInput("x", "auth_token=synth_auth; ct0=bad-ct0!"); res.Severity != CookiePrecheckError ||
		!strings.Contains(res.Summary, "ct0") {
		t.Fatalf("x invalid ct0 = %+v", res)
	}
	if res := PrecheckCookieInput("x", "auth_token=synth_auth; ct0=AbCd1234; twid=bad_twid; guest_id=v1%3A1"); res.Severity != CookiePrecheckError ||
		!strings.Contains(res.Summary, "twid") {
		t.Fatalf("x invalid twid = %+v", res)
	}

	// --- Bilibili ---
	if res := PrecheckCookieInput("bilibili", "SESSDATA=synth_sess; bili_jct=synth_jct; DedeUserID=778899"); res.Severity != CookiePrecheckOK {
		t.Fatalf("bilibili complete = %+v, want ok", res)
	}
	if res := PrecheckCookieInput("bilibili", "SESSDATA=synth_sess"); res.Severity != CookiePrecheckWarning ||
		!hasPrecheckField(res.MissingOptional, "bili_jct") ||
		!hasPrecheckField(res.MissingOptional, "DedeUserID") {
		t.Fatalf("bilibili missing auxiliary = %+v", res)
	}
	if res := PrecheckCookieInput("bilibili", "bili_jct=synth_jct"); res.Severity != CookiePrecheckError ||
		!hasPrecheckField(res.MissingRequired, "SESSDATA") {
		t.Fatalf("bilibili missing SESSDATA = %+v", res)
	}
}

func TestPrecheckCookieInputSentinelIsolation(t *testing.T) {
	const sentinel = "SENTINEL_PRECHECK_SECRET_987654321"
	encodedSentinel := url.QueryEscape(sentinel)

	cases := []struct {
		site string
		raw  string
	}{
		{"douyin", sentinel},
		{"douyin", "sessionid=" + sentinel},
		{"douyin", "sessionid=" + sentinel + "; s_v_web_id=" + sentinel + "; ttwid=" + sentinel + "; UIFID=" + sentinel},
		{"douyin", "ttwid=" + sentinel + "; msToken=" + sentinel},
		{"douyin", "sessionid=" + sentinel + strings.Repeat("x", credential.MaxCookieInputBytes)},
		{"weibo", sentinel},
		{"weibo", "sub=" + sentinel},
		{"weibo", "SUB=" + sentinel + "; SUBP=" + sentinel + "; XSRF-TOKEN=" + sentinel + "; WBPSESS=" + sentinel},
		{"weibo", "Cookie: SUB=" + sentinel + "\nExtra: " + sentinel},
		{"x", sentinel},
		{"x", "auth_token=" + sentinel + "; ct0=" + sentinel + "!"},
		{"x", "auth_token=" + sentinel + "; ct0=ValidAlnum123; twid=" + sentinel},
		{"x", "auth_token=" + sentinel + "; ct0=ValidAlnum123; twid=u%3D12345; guest_id=" + sentinel},
		{"bilibili", sentinel},
		{"bilibili", "SESSDATA=" + sentinel},
		{"bilibili", "SESSDATA=" + sentinel + "; bili_jct=" + sentinel + "; DedeUserID=" + sentinel},
	}

	for _, tc := range cases {
		res := PrecheckCookieInput(tc.site, tc.raw)
		rawJSON, err := json.Marshal(res)
		if err != nil {
			t.Fatalf("marshal precheck result: %v", err)
		}
		text := string(rawJSON)
		if strings.Contains(text, sentinel) || strings.Contains(text, encodedSentinel) {
			t.Fatalf("sentinel leaked in PrecheckCookieInput(%s): %s", tc.site, text)
		}
	}
}

func TestAuthVerifiedAtLifecycleAndSecretIsolation(t *testing.T) {
	h := newDualSiteTestHarness(t)
	ctx := context.Background()

	const (
		biliSentinel = "SENTINEL_BILI_SESSDATA_LIFECYCLE_135"
		dySentinel   = "SENTINEL_DY_SESSIONID_LIFECYCLE_135"
	)

	// 1. Login Bilibili & Douyin -> AuthVerifiedAt recorded for both sites.
	if _, err := h.backend.ImportCookie(ctx, "SESSDATA="+biliSentinel+"; bili_jct=synth_jct; DedeUserID=778899", ""); err != nil {
		t.Fatalf("ImportCookie bilibili: %v", err)
	}
	if _, err := h.backend.LoginWithCookie(ctx, string(site.Douyin), "sessionid="+dySentinel+"; s_v_web_id=verify_synth; ttwid=synth_ttwid; UIFID=synth_uifid", ""); err != nil {
		t.Fatalf("LoginWithCookie douyin: %v", err)
	}

	uiSt := h.backend.GetUIState()
	biliVerified := uiSt.AuthVerifiedAt["bilibili"]
	dyVerified := uiSt.AuthVerifiedAt["douyin"]
	if _, err := time.Parse(time.RFC3339, biliVerified); err != nil {
		t.Fatalf("expected valid RFC3339 AuthVerifiedAt[bilibili], got %q (%v)", biliVerified, err)
	}
	if _, err := time.Parse(time.RFC3339, dyVerified); err != nil {
		t.Fatalf("expected valid RFC3339 AuthVerifiedAt[douyin], got %q (%v)", dyVerified, err)
	}

	// Set a distinct earlier verified timestamp for Douyin to prove expired/unknown checks preserve it.
	const earlierVerified = "2026-11-20T08:30:00Z"
	if _, err := h.backend.MutateUIState(func(s *uistate.State) {
		if s.AuthVerifiedAt == nil {
			s.AuthVerifiedAt = make(map[string]string)
		}
		s.AuthVerifiedAt["douyin"] = earlierVerified
	}); err != nil {
		t.Fatal(err)
	}

	// 2. Transient risk-control/captcha error during BootstrapAuth marks "unknown" WITHOUT erasing AuthVerifiedAt.
	h.dyMode.Store("captcha")
	_, _ = h.backend.BootstrapAuth(ctx)
	if got := h.backend.StatusOf(string(site.Douyin)); got.State != "unknown" {
		t.Fatalf("expected douyin state=unknown on captcha bootstrap, got %+v", got)
	}
	if got := h.backend.GetUIState().AuthVerifiedAt["douyin"]; got != earlierVerified {
		t.Fatalf("unknown state overwrote AuthVerifiedAt[douyin]: got %q, want %q", got, earlierVerified)
	}

	// 3. Auth expiry marks "expired" WITHOUT erasing AuthVerifiedAt.
	h.dyMode.Store("expired")
	_, _ = h.backend.BootstrapAuth(ctx)
	if got := h.backend.StatusOf(string(site.Douyin)); got.State != "expired" || got.LoggedIn {
		t.Fatalf("expected douyin state=expired, got %+v", got)
	}
	if got := h.backend.GetUIState().AuthVerifiedAt["douyin"]; got != earlierVerified {
		t.Fatalf("expired state erased AuthVerifiedAt[douyin]: got %q, want %q", got, earlierVerified)
	}

	// 4. Re-login updates AuthVerifiedAt[douyin] to a fresh timestamp.
	h.dyMode.Store("ok")
	if _, err := h.backend.LoginWithCookie(ctx, string(site.Douyin), "sessionid="+dySentinel+"; s_v_web_id=verify_synth; ttwid=synth_ttwid; UIFID=synth_uifid", ""); err != nil {
		t.Fatalf("re-login douyin: %v", err)
	}
	refreshed := h.backend.GetUIState().AuthVerifiedAt["douyin"]
	if refreshed == "" || refreshed == earlierVerified {
		t.Fatalf("expected re-login to refresh AuthVerifiedAt[douyin], got %q", refreshed)
	}

	// 5. LogoutSite("douyin") clears ONLY douyin's AuthVerifiedAt, keeping bilibili untouched.
	if _, err := h.backend.LogoutSite(string(site.Douyin)); err != nil {
		t.Fatalf("LogoutSite douyin: %v", err)
	}
	afterLogout := h.backend.GetUIState().AuthVerifiedAt
	if afterLogout["douyin"] != "" {
		t.Fatalf("expected LogoutSite(douyin) to clear AuthVerifiedAt[douyin], got %q", afterLogout["douyin"])
	}
	if afterLogout["bilibili"] == "" {
		t.Fatalf("LogoutSite(douyin) cleared unrelated AuthVerifiedAt[bilibili]")
	}

	// 6. Strict sentinel check: ui-state.json and trace.log must NEVER contain raw cookies.
	for _, p := range []string{
		filepath.Join(h.loc.DataDir, "ui-state.json"),
		filepath.Join(h.loc.LogsDir, "trace.log"),
		filepath.Join(h.loc.DataDir, "history.json"),
	} {
		raw, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("read %s: %v", p, err)
		}
		text := string(raw)
		if strings.Contains(text, biliSentinel) || strings.Contains(text, dySentinel) {
			t.Fatalf("cookie sentinel leaked into %s", p)
		}
	}
}
