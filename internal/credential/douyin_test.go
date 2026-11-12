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
	biliCipher, err := encryptBytesWithEntropy(v011JSON, []byte("trace:bilibili:credential:v1"))
	if err != nil {
		t.Fatalf("encrypt v0.1.1 synthetic fixture: %v", err)
	}
	biliPath := filepath.Join(credDir, BilibiliFileName)
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

	// 3. Realistic 53-field synthetic browser header (no msToken, includes dynamic bd_ticket_guard_* and __security_*).
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
		// Dynamic browser-bound tickets that MUST be stripped:
		"bd_ticket_guard_client_data=strip_me_guard_client",
		"bd_ticket_guard_web_version=1",
		"bd_ticket_guard_ree_public_key=strip_me_pubkey",
		"bd_ticket_guard_iteration_version=1",
		"bd_ticket_guard_web_sign_type=1",
		"_bd_ticket_crypt_cookie=strip_me_crypt",
		"__security_mc_1_s_sdk=strip_me_sec_sdk",
		"__security_server_data_status=1",
		"x_tt_token=strip_me_x_tt",
		"sdk_source_info=strip_me_sdk_src",
		"fpk1=strip_me_fpk1",
		"fpk2=strip_me_fpk2",
	}
	// Pad with 23 synthetic browser UI/analytics cookies to reach 53 total fields.
	for i := 1; i <= 23; i++ {
		parts = append(parts, fmt.Sprintf("browser_misc_field_%02d=misc_val_%02d", i, i))
	}
	if len(parts) != 53 {
		t.Fatalf("synthetic header count = %d, want 53", len(parts))
	}

	cleaned, err := ParseDouyinCookieInput("Cookie: " + strings.Join(parts, "; "))
	if err != nil {
		t.Fatalf("ParseDouyinCookieInput 53-field header failed: %v", err)
	}
	if cleaned["sessionid"] != "synth_sess_000111222333" || cleaned["ttwid"] != "synth_ttwid_live_5" {
		t.Fatalf("missing retained identity/context cookies: %+v", cleaned)
	}
	if cleaned["UIFID"] != "synth_upper_uifid_6" || cleaned["uifid"] != "synth_lower_uifid_8" {
		t.Fatalf("UIFID / uifid case collision: UIFID=%q uifid=%q", cleaned["UIFID"], cleaned["uifid"])
	}
	if _, hasMS := cleaned["msToken"]; hasMS {
		t.Fatal("msToken should not be fabricated when absent from pasted Cookie")
	}
	for _, stripped := range []string{
		"bd_ticket_guard_client_data",
		"bd_ticket_guard_web_version",
		"bd_ticket_guard_ree_public_key",
		"_bd_ticket_crypt_cookie",
		"__security_mc_1_s_sdk",
		"__security_server_data_status",
		"x_tt_token",
		"sdk_source_info",
		"fpk1",
		"fpk2",
		"browser_misc_field_01",
	} {
		if _, exists := cleaned[stripped]; exists {
			t.Fatalf("stripped key %q remained in cleaned map", stripped)
		}
	}

	// 4. sessionid_ss alone is also accepted.
	ssOnly, err := ParseDouyinCookieInput("sessionid_ss=synth_ss_only_token")
	if err != nil || ssOnly["sessionid_ss"] != "synth_ss_only_token" {
		t.Fatalf("sessionid_ss-only input failed: (%+v, %v)", ssOnly, err)
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
