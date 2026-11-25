package credential

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestV011BilibiliFixtureCompatibilityAndEntropyIsolation(t *testing.T) {
	credDir := filepath.Join(t.TempDir(), "credentials")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Synthetic v0.1.1 bilibili.bin payload (without "site" field, using legacy camelCase refresh keys)
	// encrypted with the v0.1.1 Bilibili entropy ("trace:bilibili:credential:v1").
	v011JSON := []byte(`{
		"version": 1,
		"cookies": {
			"SESSDATA": "v011_synth_sessdata_token",
			"bili_jct": "v011_synth_bili_jct_token",
			"DedeUserID": "100200300"
		},
		"refreshToken": "v011_synth_refresh_token",
		"confirmPending": true,
		"pendingOldRefreshToken": "v011_synth_old_refresh_token",
		"source": "qr",
		"updatedAt": "2026-11-10T08:00:00Z",
		"mid": 100200300,
		"uname": "SyntheticV011User",
		"state": "logged_in"
	}`)
	biliPath := filepath.Join(credDir, BilibiliFileName)
	biliCipher, err := protectFor(biliPath, v011JSON, []byte("trace:bilibili:credential:v1"))
	if err != nil {
		t.Fatalf("encrypt v0.1.1 synthetic fixture: %v", err)
	}
	if err := os.WriteFile(biliPath, biliCipher, 0o600); err != nil {
		t.Fatal(err)
	}

	mgr, backups, err := OpenManager(credDir)
	if err != nil {
		t.Fatalf("OpenManager failed: %v", err)
	}
	if len(backups) != 0 {
		t.Fatalf("unexpected corrupt backups: %v", backups)
	}
	biliSt := mgr.Status(SiteBilibili)
	if !biliSt.LoggedIn || biliSt.Mid != 100200300 || biliSt.Uname != "SyntheticV011User" || !biliSt.ConfirmPending {
		t.Fatalf("v0.1.1 bilibili status mismatch: %+v", biliSt)
	}
	if got := mgr.Bilibili().Cookies()["SESSDATA"]; got != "v011_synth_sessdata_token" {
		t.Fatalf("v0.1.1 SESSDATA = %q", got)
	}

	// Save Douyin credential to douyin.bin using DouyinEntropy ("trace:douyin:credential:v1").
	dyPath := filepath.Join(credDir, DouyinFileName)
	if _, err := mgr.Douyin().SaveVerifiedDouyinManual(map[string]string{
		"sessionid":    "dy_synth_session_alpha_9988",
		"sessionid_ss": "dy_synth_session_alpha_9988",
		"ttwid":        "dy_synth_ttwid_ctx_1122",
		"UIFID":        "dy_synth_uifid_ctx_3344",
	}, DouyinProfile{
		UID:      "1000000000000001",
		SecUID:   "MS4wLjABAAAA__Synthetic_Self_01",
		UniqueID: "synth_handle",
		Uname:    "合成抖音账号",
	}); err != nil {
		t.Fatalf("SaveVerifiedDouyinManual failed: %v", err)
	}

	rawDyCipher, err := os.ReadFile(dyPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(rawDyCipher, []byte("dy_synth_session_alpha_9988")) {
		t.Fatal("douyin.bin leaked plaintext sessionid")
	}

	// Prove wrong-entropy decryption fails: opening douyin.bin with Bilibili Open (trace:bilibili:credential:v1)
	// must fail decryption and degrade to decrypt_failed.
	wrongCopy := filepath.Join(t.TempDir(), "wrong-entropy.bin")
	if err := os.WriteFile(wrongCopy, rawDyCipher, 0o600); err != nil {
		t.Fatal(err)
	}
	wrongStore, wrongBackup, err := Open(wrongCopy)
	if err != nil {
		t.Fatalf("Open with wrong entropy should degrade cleanly: %v", err)
	}
	if wrongBackup == "" || wrongStore.Status().State != "decrypt_failed" || wrongStore.HasActiveCredential() {
		t.Fatalf("expected decrypt_failed on wrong entropy, got backup=%q status=%+v", wrongBackup, wrongStore.Status())
	}
}

func TestDouyinCookieCleanerRulesAndBounds(t *testing.T) {
	// 1. Over 8 KiB input rejected without echoing secret.
	oversized := "sessionid=" + strings.Repeat("f", MaxCookieInputBytes+64)
	if _, err := ParseDouyinCookieInput(oversized); !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("expected ErrInputTooLarge, got %v", err)
	} else if strings.Contains(err.Error(), "ffff") {
		t.Fatalf("error echoed secret value: %v", err)
	}

	// 2. Guest-only ttwid + msToken without sessionid/sessionid_ss must be rejected.
	guestOnly := "ttwid=guest_ttwid_secret_12345; msToken=guest_mstoken_secret_67890; UIFID=guest_uifid_000"
	if _, err := ParseDouyinCookieInput(guestOnly); !errors.Is(err, ErrMissingDouyinSession) {
		t.Fatalf("expected ErrMissingDouyinSession for guest cookies, got %v", err)
	} else if strings.Contains(err.Error(), "guest_ttwid_secret_12345") || strings.Contains(err.Error(), "guest_mstoken_secret_67890") {
		t.Fatalf("error echoed guest cookie values: %v", err)
	}

	// 3. Realistic browser header (no msToken, includes UCP/SSO, risk-control context, dynamic bd_ticket_guard_* and __security_*).
	parts := []string{
		"sessionid=synth_sess_000111222333",
		"sessionid_ss=synth_sess_000111222333",
		"sid_tt=synth_sid_tt_1",
		"uid_tt=synth_uid_tt_2",
		"uid_tt_ss=synth_uid_tt_2",
		"sid_guard=synth_guard_token%7C1760000000%7C5184000%7CSun",
		"odin_tt=synth_odin_tt_3",
		"passport_csrf_token=synth_csrf_4",
		"passport_csrf_token_default=synth_csrf_4",
		"is_staff_user=false",
		"login_time=1760000000",
		"ttwid=synth_ttwid_live_5",
		"UIFID=synth_upper_uifid_6",
		"UIFID_TEMP=synth_temp_uifid_7",
		"uifid=synth_lower_uifid_8",
		"s_v_web_id=verify_synth_9",
		"__ac_nonce=067000000000000000001",
		"__ac_signature=_02B4Z6wo00f01synth",
		// Extended ByteDance UCP/SSO & gateway context cookies that MUST be retained with original case:
		"sid_ucp_v1=1.0.0-synth_sid_ucp_v1_token",
		"ssid_ucp_v1=1.0.0-synth_ssid_ucp_v1_token",
		"sso_uid_tt=synth_sso_uid_10",
		"sso_uid_tt_ss=synth_sso_uid_10",
		"passport_auth_status=synth_passport_auth_11",
		"passport_auth_status_ss=synth_passport_auth_11",
		"n_mh=synth_nmh_12",
		"d_ticket=synth_dticket_13",
		"x-web-secsdk-uid=synth_secsdk_uid_14",
		"_waftokenid=synth_waftoken_15",
		"UIFIDTEMP=synth_uifidtemp_upper_16",
		"uifidtemp=synth_uifidtemp_lower_17",
		"IsDouyinActive=true",
		// Dynamic browser-bound tickets that MUST be stripped:
		"bd_ticket_guard_client_data=strip_me_guard_client",
		"bd_ticket_guard_web_version=1",
		"bd_ticket_guard_ree_public_key=strip_me_pubkey",
		"BD_TICKET_GUARD_ITERATION_VERSION=1",
		"bd_ticket_guard_web_sign_type=1",
		"_bd_ticket_crypt_cookie=strip_me_crypt",
		"__security_mc_1_s_sdk=strip_me_sec_sdk",
		"__security_server_data_status=1",
		"x_tt_token=strip_me_x_tt",
		"sdk_source_info=strip_me_sdk_src",
		"fpk1=strip_me_fpk1",
		"fpk2=strip_me_fpk2",
	}
	for i := 1; i <= 10; i++ {
		parts = append(parts, fmt.Sprintf("browser_misc_field_%02d=misc_val_%02d", i, i))
	}

	cleaned, err := ParseDouyinCookieInput("Cookie: " + strings.Join(parts, "; "))
	if err != nil {
		t.Fatalf("ParseDouyinCookieInput full header failed: %v", err)
	}
	if cleaned["sessionid"] != "synth_sess_000111222333" || cleaned["ttwid"] != "synth_ttwid_live_5" {
		t.Fatalf("missing retained identity/context cookies: %+v", cleaned)
	}
	if cleaned["UIFID"] != "synth_upper_uifid_6" || cleaned["uifid"] != "synth_lower_uifid_8" {
		t.Fatalf("UIFID / uifid case collision: UIFID=%q uifid=%q", cleaned["UIFID"], cleaned["uifid"])
	}
	for wantKey, wantVal := range map[string]string{
		"sid_ucp_v1":              "1.0.0-synth_sid_ucp_v1_token",
		"ssid_ucp_v1":             "1.0.0-synth_ssid_ucp_v1_token",
		"sso_uid_tt":              "synth_sso_uid_10",
		"sso_uid_tt_ss":           "synth_sso_uid_10",
		"passport_auth_status":    "synth_passport_auth_11",
		"passport_auth_status_ss": "synth_passport_auth_11",
		"n_mh":                    "synth_nmh_12",
		"d_ticket":                "synth_dticket_13",
		"x-web-secsdk-uid":        "synth_secsdk_uid_14",
		"_waftokenid":             "synth_waftoken_15",
		"UIFIDTEMP":               "synth_uifidtemp_upper_16",
		"uifidtemp":               "synth_uifidtemp_lower_17",
		"IsDouyinActive":          "true",
		"browser_misc_field_01":   "misc_val_01",
	} {
		if got := cleaned[wantKey]; got != wantVal {
			t.Fatalf("expected retained cookie %s=%q, got %q", wantKey, wantVal, got)
		}
	}
	if _, hasMS := cleaned["msToken"]; hasMS {
		t.Fatal("msToken should not be fabricated when absent from pasted Cookie")
	}
	for _, stripped := range []string{
		"bd_ticket_guard_client_data",
		"bd_ticket_guard_web_version",
		"bd_ticket_guard_ree_public_key",
		"BD_TICKET_GUARD_ITERATION_VERSION",
		"bd_ticket_guard_iteration_version",
		"_bd_ticket_crypt_cookie",
		"__security_mc_1_s_sdk",
		"__security_server_data_status",
		"x_tt_token",
		"sdk_source_info",
		"fpk1",
		"fpk2",
	} {
		if _, exists := cleaned[stripped]; exists {
			t.Fatalf("stripped key %q remained in cleaned map", stripped)
		}
	}

	// 4. Unsafe RFC 6265 keys and values are rejected without clobbering safe pairs.
	dirtyMap := CleanDouyinCookieMap(map[string]string{
		"sessionid":                 "synth_clean_session_ok",
		"bad space":                 "val1",
		"bad:colon":                 "val2",
		"bad/slash":                 "val3",
		"bad@at":                    "val4",
		strings.Repeat("k", 129):    "overlong_key",
		strings.Repeat("k", 128):    "max_128_key_ok",
		"ctrl_val":                  "abc\x00def",
		"space_val":                 "abc def",
		"semi_val":                  "abc;evil=1",
		"non_ascii_val":             "中文值",
		"bd_ticket_guard_custom_v9": "must_strip",
	})
	if len(dirtyMap) != 2 || dirtyMap["sessionid"] != "synth_clean_session_ok" || dirtyMap[strings.Repeat("k", 128)] != "max_128_key_ok" {
		t.Fatalf("unexpected dirtyMap filtering result: %+v", dirtyMap)
	}

	// 5. sessionid_ss alone is also accepted.
	ssOnly, err := ParseDouyinCookieInput("sessionid_ss=synth_ss_only_token")
	if err != nil || ssOnly["sessionid_ss"] != "synth_ss_only_token" {
		t.Fatalf("sessionid_ss-only input failed: (%+v, %v)", ssOnly, err)
	}
}

func TestDouyinExtendedCookieSerializationUIFIDMergeAndSentinels(t *testing.T) {
	credDir := filepath.Join(t.TempDir(), "credentials")
	mgr, _, err := OpenManager(credDir)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Ordered serialization: AllowedDouyinCookieKeys first, then sorted extension keys.
	raw := strings.Join([]string{
		"z_custom=z_secret_val_901",
		"sid_ucp_v1=ucp_secret_val_902",
		"d_ticket=dticket_secret_val_903",
		"ttwid=ttwid_secret_val_904",
		"sessionid=sess_secret_val_905",
		"x-web-secsdk-uid=secsdk_secret_val_906",
		"_waftokenid=waf_secret_val_907",
		"bd_ticket_guard_client_data=poison_secret_908",
	}, "; ")
	if _, err := mgr.Douyin().SaveDouyinManual(raw); err != nil {
		t.Fatalf("SaveDouyinManual failed: %v", err)
	}
	wantHdr := "sessionid=sess_secret_val_905; ttwid=ttwid_secret_val_904; _waftokenid=waf_secret_val_907; d_ticket=dticket_secret_val_903; sid_ucp_v1=ucp_secret_val_902; x-web-secsdk-uid=secsdk_secret_val_906; z_custom=z_secret_val_901"
	if got := mgr.Douyin().CookieHeader(); got != wantHdr {
		t.Fatalf("Douyin().CookieHeader() =\n  %s\nwant:\n  %s", got, wantHdr)
	}

	// Re-open from disk to ensure filterCookies on load preserves all extended keys.
	reopened, warn, err := OpenDouyin(filepath.Join(credDir, DouyinFileName))
	if err != nil || warn != "" {
		t.Fatalf("OpenDouyin reopen failed: err=%v warn=%q", err, warn)
	}
	if got := reopened.CookieHeader(); got != wantHdr {
		t.Fatalf("reopened CookieHeader() =\n  %s\nwant:\n  %s", got, wantHdr)
	}

	// 2. Sentinel scan: extended cookie values must never leak in ciphertext, Statuses, or RedactString.
	cipherBytes, err := os.ReadFile(filepath.Join(credDir, DouyinFileName))
	if err != nil {
		t.Fatal(err)
	}
	statusesJSON, _ := json.Marshal(mgr.Statuses())
	sampleLog := "err: " + wantHdr
	redactedLog := mgr.RedactString(sampleLog)
	for _, secret := range []string{
		"sess_secret_val_905",
		"ttwid_secret_val_904",
		"_waftokenid=waf_secret_val_907",
		"waf_secret_val_907",
		"dticket_secret_val_903",
		"ucp_secret_val_902",
		"secsdk_secret_val_906",
		"z_secret_val_901",
	} {
		if bytes.Contains(cipherBytes, []byte(secret)) {
			t.Fatalf("douyin.bin leaked plaintext secret %q", secret)
		}
		if bytes.Contains(statusesJSON, []byte(secret)) {
			t.Fatalf("Statuses JSON leaked secret %q", secret)
		}
		if strings.Contains(redactedLog, secret) {
			t.Fatalf("RedactString leaked extended secret %q: %s", secret, redactedLog)
		}
	}

	// 3. MergeContextCookies updates context keys while strictly protecting core identity keys.
	if err := mgr.Douyin().MergeContextCookies(map[string]string{
		"d_ticket":             "dticket_rotated_911",
		"x-web-secsdk-uid":     "secsdk_rotated_912",
		"_waftokenid":          "waf_rotated_913",
		"UIFIDTEMP":            "uifidtemp_rotated_914",
		"uifidtemp":            "uifidtemp_lower_915",
		"uifid_temp":           "uifid_temp_lower_916",
		"sessionid":            "evil_overwrite_session",
		"sessionid_ss":         "evil_overwrite_session_ss",
		"sid_tt":               "evil_overwrite_sid_tt",
		"uid_tt":               "evil_overwrite_uid_tt",
		"uid_tt_ss":            "evil_overwrite_uid_tt_ss",
		"sid_ucp_v1":           "evil_overwrite_sid_ucp",
		"ssid_ucp_v1":          "evil_overwrite_ssid_ucp",
		"sso_uid_tt":           "evil_overwrite_sso_uid",
		"sso_uid_tt_ss":        "evil_overwrite_sso_uid_ss",
		"passport_auth_status": "evil_overwrite_passport",
		"n_mh":                 "evil_overwrite_nmh",
	}); err != nil {
		t.Fatalf("MergeContextCookies failed: %v", err)
	}
	after := mgr.Douyin().Cookies()
	if after["d_ticket"] != "dticket_rotated_911" ||
		after["x-web-secsdk-uid"] != "secsdk_rotated_912" ||
		after["_waftokenid"] != "waf_rotated_913" ||
		after["UIFIDTEMP"] != "uifidtemp_rotated_914" ||
		after["uifidtemp"] != "uifidtemp_lower_915" ||
		after["uifid_temp"] != "uifid_temp_lower_916" {
		t.Fatalf("MergeContextCookies did not update expected context keys: %+v", after)
	}
	if after["sessionid"] != "sess_secret_val_905" || after["sid_ucp_v1"] != "ucp_secret_val_902" {
		t.Fatalf("MergeContextCookies overwrote core identity keys: %+v", after)
	}
	for _, protected := range []string{"sessionid_ss", "sid_tt", "uid_tt", "uid_tt_ss", "ssid_ucp_v1", "sso_uid_tt", "sso_uid_tt_ss", "passport_auth_status", "n_mh"} {
		if _, exists := after[protected]; exists {
			t.Fatalf("MergeContextCookies injected protected identity key %q: %+v", protected, after)
		}
	}

	// 4. Store.UIFID() checks all 6 candidate keys in exact priority order:
	// uifid -> uifid_temp -> uifidtemp -> UIFID -> UIFID_TEMP -> UIFIDTEMP.
	priorityOrder := []string{"uifid", "uifid_temp", "uifidtemp", "UIFID", "UIFID_TEMP", "UIFIDTEMP"}
	for i, targetKey := range priorityOrder {
		m := map[string]string{"sessionid": "sess_uifid_test"}
		for j := i; j < len(priorityOrder); j++ {
			m[priorityOrder[j]] = "val_for_" + priorityOrder[j]
		}
		if _, err := mgr.Douyin().SaveVerifiedDouyinManual(m, DouyinProfile{UID: "1", SecUID: "MS4wLjABAAAA_UIFID_TEST_0001", Uname: "u"}); err != nil {
			t.Fatal(err)
		}
		if got := mgr.Douyin().UIFID(); got != "val_for_"+targetKey {
			t.Fatalf("UIFID priority[%d=%s] = %q, want %q", i, targetKey, got, "val_for_"+targetKey)
		}
	}

	// 5. Other 3 sites (Bilibili, Weibo, X) strict whitelists remain 100% unchanged.
	if _, err := mgr.Bilibili().SaveManual("SESSDATA=bili_s_1234; bili_jct=bili_c_5678; extra_bili_misc=drop_me", ""); err != nil {
		t.Fatal(err)
	}
	if got := mgr.Bilibili().CookieHeader(); strings.Contains(got, "extra_bili_misc") {
		t.Fatalf("Bilibili whitelist unexpectedly retained extra cookie: %s", got)
	}
	if _, err := mgr.Weibo().SaveWeiboManual("SUB=weibo_sub_12345678; SUBP=weibo_subp_12345678; extra_wb_misc=drop_me"); err != nil {
		t.Fatal(err)
	}
	if got := mgr.Weibo().CookieHeader(); strings.Contains(got, "extra_wb_misc") {
		t.Fatalf("Weibo whitelist unexpectedly retained extra cookie: %s", got)
	}
	if _, err := mgr.X().SaveXManual("auth_token=xauth1234567890abcdef; ct0=xct01234567890abcdef; extra_x_misc=drop_me"); err != nil {
		t.Fatal(err)
	}
	if got := mgr.X().CookieHeader(); strings.Contains(got, "extra_x_misc") {
		t.Fatalf("X whitelist unexpectedly retained extra cookie: %s", got)
	}
}

func TestManagerPerSiteIsolationAndMultiSiteRedaction(t *testing.T) {
	credDir := filepath.Join(t.TempDir(), "credentials")
	mgr, _, err := OpenManager(credDir)
	if err != nil {
		t.Fatal(err)
	}

	biliSess := "bili_secret_sessdata_value_111"
	biliCSRF := "bili_secret_csrf_value_222"
	if _, err := mgr.Bilibili().SaveManual("SESSDATA="+biliSess+"; bili_jct="+biliCSRF, ""); err != nil {
		t.Fatal(err)
	}

	dySess := "douyin_secret_sessionid_value_333"
	dyUIFID := "douyin_secret_uifid_value_444"
	if _, err := mgr.Douyin().SaveDouyinManual("sessionid=" + dySess + "; UIFID=" + dyUIFID + "; ttwid=dy_ttwid_555; is_staff_user=false; login_time=1731400000000"); err != nil {
		t.Fatal(err)
	}

	if got := mgr.Douyin().UIFID(); got != dyUIFID {
		t.Fatalf("Douyin().UIFID() = %q, want %q", got, dyUIFID)
	}

	// Manager.RedactString must scrub secrets from BOTH sites simultaneously.
	leakedLog := fmt.Sprintf("headers: SESSDATA=%s; sessionid=%s; uifid=%s", biliSess, dySess, dyUIFID)
	redacted := mgr.RedactString(leakedLog)
	for _, sec := range []string{biliSess, dySess, dyUIFID} {
		if strings.Contains(redacted, sec) {
			t.Fatalf("Manager.RedactString failed to scrub %q: %s", sec, redacted)
		}
	}

	// Non-secret context values (is_staff_user flag, login_time timestamp) must not turn
	// ordinary log text such as boolean fields into redaction noise.
	plainLog := `ok=false recording=false at=1731400000000`
	if got := mgr.RedactString(plainLog); got != plainLog {
		t.Fatalf("Manager.RedactString redacted non-secret text: %q", got)
	}

	// Statuses JSON must never contain raw secrets.
	statusesJSON, _ := json.Marshal(mgr.Statuses())
	for _, sec := range []string{biliSess, biliCSRF, dySess, dyUIFID} {
		if bytes.Contains(statusesJSON, []byte(sec)) {
			t.Fatalf("Statuses JSON leaked secret %q", sec)
		}
	}

	// Expiring Douyin must NOT affect Bilibili.
	if _, err := mgr.MarkExpired(SiteDouyin, "抖音登录已过期"); err != nil {
		t.Fatal(err)
	}
	if mgr.HasActiveCredential(SiteDouyin) || mgr.Douyin().CookieHeader() != "" {
		t.Fatal("expired Douyin store remained active")
	}
	if !mgr.HasActiveCredential(SiteBilibili) || !mgr.Status(SiteBilibili).LoggedIn {
		t.Fatal("expiring Douyin unexpectedly affected Bilibili")
	}

	// Clearing Douyin deletes douyin.bin while bilibili.bin stays intact.
	if err := mgr.Clear(SiteDouyin); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(credDir, DouyinFileName)); !os.IsNotExist(err) {
		t.Fatalf("expected douyin.bin deleted, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(credDir, BilibiliFileName)); err != nil {
		t.Fatalf("bilibili.bin should remain intact after clearing Douyin: %v", err)
	}
	if !mgr.HasActiveCredential(SiteBilibili) {
		t.Fatal("Bilibili credential lost after clearing Douyin")
	}
}
