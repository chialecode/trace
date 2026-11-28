package ui

import (
	"testing"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/uistate"
	"github.com/egoist/mygo/ui"
)

func TestFormatAccountVerifiedTime(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 11, 28, 15, 30, 0, 0, loc)

	cases := []struct {
		raw  string
		want string
	}{
		{"", "尚未验证"},
		{"invalid", "尚未验证"},
		{time.Date(2026, 11, 28, 9, 5, 0, 0, loc).UTC().Format(time.RFC3339), "今天 09:05"},
		{time.Date(2026, 11, 27, 22, 45, 0, 0, loc).UTC().Format(time.RFC3339), "昨天 22:45"},
		{time.Date(2026, 11, 16, 8, 12, 0, 0, loc).UTC().Format(time.RFC3339), "11月16日 08:12"},
		{time.Date(2025, 12, 31, 23, 59, 0, 0, loc).UTC().Format(time.RFC3339), "2025年12月31日 23:59"},
	}
	for _, tc := range cases {
		if got := formatAccountVerifiedTime(tc.raw, now); got != tc.want {
			t.Fatalf("formatAccountVerifiedTime(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}

	// Explicit negative-offset timezone (UTC-8) cross-day check.
	pst := time.FixedZone("PST", -8*3600)
	nowPST := time.Date(2026, 11, 28, 2, 0, 0, 0, pst) // 2026-11-28 10:00 UTC
	prevDayUTC := "2026-11-28T06:30:00Z"               // 2026-11-27 22:30 PST (yesterday in PST, same UTC day)
	if got := formatAccountVerifiedTime(prevDayUTC, nowPST); got != "昨天 22:30" {
		t.Fatalf("formatAccountVerifiedTime(PST cross-day) = %q, want 昨天 22:30", got)
	}
}

func TestCookieLoginUXPrecheckNonBlockingAndSentinelIsolation(t *testing.T) {
	t.Setenv("TRACE_QUIET", "1")
	app, stub, _, cleanup := newDouyinUITestHarness(t)
	defer cleanup()

	const sentinel = "SENTINEL_UI_COOKIE_NEVER_RENDER_135"

	// 1. All 4 sites on /login show step-by-step copy guidance (Bilibili under cookie tab).
	app.Navigate(RouteLogin)
	tt := ui.NewTester(app.View, 1280, 860)

	app.LoginSite = string(site.Bilibili)
	app.LoginTab = "cookie"
	app.ManualCookieInput = "SESSDATA=" + sentinel
	tt.Frame()
	if _, ok := tt.Find("Cookie获取步骤：bilibili"); !ok {
		t.Fatal("expected bilibili step-by-step copy guide on /login cookie tab")
	}
	if _, ok := tt.Find("Cookie预检提示：bilibili"); !ok {
		t.Fatal("expected bilibili precheck warning when bili_jct/DedeUserID missing")
	}
	if tt.HasText(sentinel) {
		t.Fatal("bilibili sentinel leaked into rendered UI")
	}

	// 2. Weibo precheck warning + guide + sentinel isolation (under Cookie tab).
	app.LoginSite = string(site.Weibo)
	app.WeiboLoginTab = "cookie"
	app.WeiboCookieInput = "SUB=" + sentinel
	tt.Frame()
	if _, ok := tt.Find("Cookie获取步骤：weibo"); !ok {
		t.Fatal("expected weibo copy guide")
	}
	if _, ok := tt.Find("Cookie预检提示：weibo"); !ok || !tt.HasText("XSRF-TOKEN") {
		t.Fatal("expected weibo precheck warning naming XSRF-TOKEN")
	}
	if tt.HasText(sentinel) {
		t.Fatal("weibo sentinel leaked into rendered UI")
	}

	// 3. X precheck warning + guide + risk disclosure above submit + sentinel isolation.
	app.LoginSite = string(site.X)
	app.XCookieInput = "auth_token=" + sentinel + "; ct0=ValidCT01234"
	tt.Frame()
	if _, ok := tt.Find("Cookie获取步骤：x"); !ok {
		t.Fatal("expected x copy guide")
	}
	if _, ok := tt.Find("Cookie预检提示：x"); !ok || !tt.HasText("twid") {
		t.Fatal("expected x precheck warning naming twid")
	}
	if _, ok := tt.Find("X登录风险提示"); !ok {
		t.Fatal("expected X risk disclosure visible")
	}
	if tt.HasText(sentinel) {
		t.Fatal("x sentinel leaked into rendered UI")
	}

	// 4. Douyin live precheck transitions (warning -> ok -> error) AND stays strictly non-blocking on submit.
	app.LoginSite = string(site.Douyin)
	app.DouyinCookieInput = "sessionid=" + sentinel + "; ttwid=synth_ttwid; UIFID=synth_uifid"
	tt.Frame()
	if _, ok := tt.Find("Cookie获取步骤：douyin"); !ok {
		t.Fatal("expected douyin copy guide")
	}
	if _, ok := tt.Find("Cookie预检提示：douyin"); !ok || !tt.HasText("s_v_web_id") {
		t.Fatal("expected douyin precheck warning naming s_v_web_id")
	}
	if tt.HasText(sentinel) {
		t.Fatal("douyin sentinel leaked into rendered UI during warning")
	}

	// Complete cookie switches to OK banner.
	app.DouyinCookieInput = "sessionid=" + sentinel + "; s_v_web_id=verify_synth; ttwid=synth_ttwid; UIFID=synth_uifid"
	tt.Frame()
	if _, ok := tt.Find("Cookie预检状态：douyin"); !ok {
		t.Fatal("expected douyin precheck OK status when all key fields present")
	}
	if tt.HasText(sentinel) {
		t.Fatal("douyin sentinel leaked into rendered UI during ok")
	}

	// Missing s_v_web_id warning must NOT block clicking submit; input clears immediately and backend succeeds.
	stub.loginMode = "ok"
	app.DouyinCookieInput = "sessionid=" + sentinel
	tt.Frame()
	if _, ok := tt.Find("Cookie预检提示：douyin"); !ok {
		t.Fatal("expected douyin warning before non-blocking submit")
	}
	if err := tt.Click("验证并登录抖音"); err != nil {
		t.Fatalf("expected submit button to remain clickable when precheck shows warning: %v", err)
	}
	if app.DouyinCookieInput != "" {
		t.Fatalf("expected DouyinCookieInput to clear immediately on submit, got %q", app.DouyinCookieInput)
	}
	if !stub.StatusOf(string(site.Douyin)).LoggedIn {
		t.Fatal("expected non-blocking submit to complete backend Douyin login")
	}
}

func TestSettingsAccountVerifiedTimeInlinePasteAndRailExpiryIndicator(t *testing.T) {
	t.Setenv("TRACE_QUIET", "1")
	app, stub, _, cleanup := newDouyinUITestHarness(t)
	defer cleanup()

	const sentinel = "SENTINEL_SETTINGS_PASTE_SECRET_135"
	verifiedTS := time.Now().UTC().Add(-20 * time.Minute).Format(time.RFC3339)

	// Seed logged-in Douyin + AuthVerifiedAt in ui-state.json.
	stub.mu.Lock()
	stub.dyStatus = credential.Status{
		Site:          string(site.Douyin),
		LoggedIn:      true,
		State:         "logged_in",
		Source:        "manual",
		UID:           "900086",
		SecUID:        "MS4wLjABAAAA_test_self",
		UniqueID:      "synth_dy_user",
		Uname:         "合成抖音账号",
		LastCheckedAt: verifiedTS,
	}
	stub.mu.Unlock()
	if _, err := app.backend.MutateUIState(func(s *uistate.State) {
		if s.AuthVerifiedAt == nil {
			s.AuthVerifiedAt = make(map[string]string)
		}
		s.AuthVerifiedAt["bilibili"] = verifiedTS
		s.AuthVerifiedAt["douyin"] = verifiedTS
	}); err != nil {
		t.Fatal(err)
	}

	// 1. Healthy accounts -> NO rail expiry badge, NO settings sidebar red dot.
	app.Navigate(RouteHome)
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	if _, ok := tt.Find("账号失效提示"); ok {
		t.Fatal("expected no rail expiry indicator when all logged-in accounts are valid")
	}

	// 2. Expire Douyin while Bilibili remains logged in (partial expiry) -> rail shows indicator + direct jump to Settings › Account.
	stub.mu.Lock()
	stub.dyStatus.LoggedIn = false
	stub.dyStatus.State = "expired"
	stub.mu.Unlock()

	tt.Frame()
	if _, ok := tt.Find("账号失效提示"); !ok {
		t.Fatal("expected rail bottom account expiry indicator when Douyin is expired")
	}
	if err := tt.Click("账号失效提示"); err != nil {
		t.Fatalf("expected rail expiry indicator to be clickable: %v", err)
	}
	if app.CurrentRoute != RouteSettings || app.SettingsCategory != "account" {
		t.Fatalf("expected direct jump to Settings › Account, got route=%q category=%q", app.CurrentRoute, app.SettingsCategory)
	}

	// 3. Settings › Account displays expired badge, preserved lastVerifiedAt, and sidebar red dot.
	tt.Frame()
	if _, ok := tt.Find("设置账号失效红点"); !ok {
		t.Fatal("expected settings sidebar account expiry red dot")
	}
	if _, ok := tt.Find("账号状态标签：douyin"); !ok || !tt.HasText("登录已失效") {
		t.Fatal("expected expired badge on Douyin account group")
	}
	if _, ok := tt.Find("账号验证时间：douyin"); !ok || !tt.HasText("上次验证有效：") {
		t.Fatal("expected preserved last verified time on expired Douyin account group")
	}
	if _, ok := tt.Find("账号失效说明：douyin"); !ok {
		t.Fatal("expected expiry explanation on Douyin account group")
	}

	// 4. Clicking "重新粘贴" opens inline paste editor right inside Settings › Account with guide + live precheck.
	if err := tt.Click("重新粘贴抖音Cookie"); err != nil {
		t.Fatalf("expected 重新粘贴抖音Cookie button to be clickable: %v", err)
	}
	tt.Frame()
	if app.CurrentRoute != RouteSettings || app.SettingsPasteSite != "douyin" {
		t.Fatalf("expected inline paste to stay on Settings › Account, got route=%q pasteSite=%q", app.CurrentRoute, app.SettingsPasteSite)
	}
	if _, ok := tt.Find("设置内联粘贴区：douyin"); !ok {
		t.Fatal("expected inline paste panel for douyin")
	}
	if _, ok := tt.Find("Cookie获取步骤：douyin"); !ok {
		t.Fatal("expected step-by-step copy guide inside inline paste panel")
	}

	// Type incomplete cookie -> live precheck warning, zero sentinel leak.
	app.SettingsPasteInput = "sessionid=" + sentinel + "; ttwid=synth_ttwid"
	tt.Frame()
	if _, ok := tt.Find("Cookie预检提示：douyin"); !ok || !tt.HasText("s_v_web_id") {
		t.Fatal("expected live precheck warning naming s_v_web_id in Settings inline paste")
	}
	if tt.HasText(sentinel) {
		t.Fatal("sentinel cookie value leaked in Settings inline paste UI")
	}

	// Switching settings category clears unsent inline paste input.
	if err := tt.Click("设置分类：常规"); err != nil {
		t.Fatalf("expected switching settings category to succeed: %v", err)
	}
	if app.SettingsPasteInput != "" || app.SettingsPasteSite != "" {
		t.Fatalf("expected switching settings category to clear inline paste state, got site=%q inputLen=%d", app.SettingsPasteSite, len(app.SettingsPasteInput))
	}

	// Return to account category, re-open inline paste, and submit to recover Douyin.
	app.SettingsCategory = "account"
	tt.Frame()
	if err := tt.Click("重新粘贴抖音Cookie"); err != nil {
		t.Fatalf("expected reopening inline paste panel: %v", err)
	}
	stub.loginMode = "ok"
	app.SettingsPasteInput = "sessionid=" + sentinel + "; s_v_web_id=verify_synth; ttwid=synth_ttwid; UIFID=synth_uifid"
	tt.Frame()
	if _, ok := tt.Find("Cookie预检状态：douyin"); !ok {
		t.Fatal("expected OK precheck banner in Settings inline paste")
	}
	if err := tt.Click("验证并更新Cookie：douyin"); err != nil {
		t.Fatalf("expected submit button in Settings inline paste to be clickable: %v", err)
	}
	if app.SettingsPasteInput != "" {
		t.Fatal("expected SettingsPasteInput to clear immediately on submit")
	}
	if app.SettingsPasteSite != "" {
		t.Fatalf("expected inline paste panel to collapse after successful re-login, got %q", app.SettingsPasteSite)
	}
	tt.Frame()
	if _, ok := tt.Find("账号失效提示"); ok {
		t.Fatal("expected rail expiry indicator to disappear after Douyin re-login")
	}
	if _, ok := tt.Find("设置账号失效红点"); ok {
		t.Fatal("expected settings sidebar red dot to disappear after Douyin re-login")
	}
}

func TestRailExpiryDirectJumpWhenAllSitesExpired(t *testing.T) {
	t.Setenv("TRACE_QUIET", "1")
	app, stub, _, cleanup := newDouyinUITestHarness(t)
	defer cleanup()

	// Log out Bilibili and expire Douyin so HasAnyLogin() == false.
	if _, err := stub.raw.Logout(); err != nil {
		t.Fatal(err)
	}
	stub.mu.Lock()
	stub.dyStatus = credential.Status{
		Site:     string(site.Douyin),
		LoggedIn: false,
		State:    "expired",
		Source:   "manual",
		UID:      "900086",
		Uname:    "已过期抖音号",
	}
	stub.mu.Unlock()

	if stub.HasAnyLogin() {
		t.Fatal("expected HasAnyLogin() == false in all-expired scenario")
	}
	app.Navigate(RouteLogin)
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	if _, ok := tt.Find("账号失效提示"); !ok {
		t.Fatal("expected rail expiry indicator even when all sites are expired/logged-out")
	}
	if err := tt.Click("账号失效提示"); err != nil {
		t.Fatalf("expected rail expiry indicator clickable on /login: %v", err)
	}
	if app.CurrentRoute != RouteSettings || app.SettingsCategory != "account" {
		t.Fatalf("expected clicking rail expiry badge when all sites expired to jump to Settings › Account, got route=%q cat=%q", app.CurrentRoute, app.SettingsCategory)
	}
}
