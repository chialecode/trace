package credential

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManualCookieParserWhitelistAndBounds(t *testing.T) {
	// 1. Over 64 KiB raw input is rejected without echoing input.
	hugeSecret := "SESSDATA=" + strings.Repeat("A", MaxCookieInputBytes+100) + "; bili_jct=csrf123"
	if _, _, err := ParseManualCookieInput(hugeSecret, ""); !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("expected ErrInputTooLarge, got %v", err)
	} else if strings.Contains(err.Error(), "AAAA") {
		t.Fatalf("error echoed secret input: %v", err)
	}

	// 1b. Over 8 KiB raw input with large non-whitelisted browser cookies succeeds after filtering.
	over8KWithNoise := "SESSDATA=sess_after_noise_ok; bili_jct=csrf_ok; large_noise=" + strings.Repeat("N", 12000)
	if len(over8KWithNoise) <= 8192 {
		t.Fatalf("test fixture must exceed 8 KiB, got %d", len(over8KWithNoise))
	}
	if cleaned, _, err := ParseManualCookieInput(over8KWithNoise, ""); err != nil {
		t.Fatalf("expected >8 KiB raw input with noise to succeed after filtering, got %v", err)
	} else if cleaned["SESSDATA"] != "sess_after_noise_ok" || len(cleaned) != 2 {
		t.Fatalf("unexpected cleaned cookies: %+v", cleaned)
	}

	// 1c. Cleaned outbound header exceeding 32 KiB is rejected without echoing secret.
	oversizedCleaned := "SESSDATA=" + strings.Repeat("B", MaxCleanedCookieHeaderBytes+64)
	if _, _, err := ParseManualCookieInput(oversizedCleaned, ""); !errors.Is(err, ErrCleanedCookieTooLarge) {
		t.Fatalf("expected ErrCleanedCookieTooLarge, got %v", err)
	} else if strings.Contains(err.Error(), "BBBB") {
		t.Fatalf("error echoed secret input: %v", err)
	}

	// 2. Missing SESSDATA is rejected without echoing bili_jct value.
	noSess := "bili_jct=super_secret_bili_jct_value_999; other_key=1"
	if _, _, err := ParseManualCookieInput(noSess, ""); !errors.Is(err, ErrMissingRequiredKeys) {
		t.Fatalf("expected ErrMissingRequiredKeys, got %v", err)
	} else if strings.Contains(err.Error(), "super_secret_bili_jct_value_999") {
		t.Fatalf("error echoed secret bili_jct: %v", err)
	}

	// 2b. Optional device cookies (buvid3/buvid4/b_nut) alone without SESSDATA must still be rejected.
	buvidOnly := "buvid3=SYNTH_BUVID3_ONLY; buvid4=SYNTH_BUVID4_ONLY; b_nut=1700000001"
	if _, _, err := ParseManualCookieInput(buvidOnly, ""); !errors.Is(err, ErrMissingRequiredKeys) {
		t.Fatalf("expected ErrMissingRequiredKeys for buvid-only input, got %v", err)
	} else if strings.Contains(err.Error(), "SYNTH_BUVID3_ONLY") {
		t.Fatalf("error echoed buvid3 secret: %v", err)
	}

	// 3. Valid Cookie header preserves the 8 allowed keys (including optional buvid3/buvid4/b_nut)
	// and strips non-whitelisted keys (_uuid, b_lsid, CURRENT_FNVAL).
	raw := "Cookie: SESSDATA=sess_val_1; bili_jct=csrf_val_2; DedeUserID=12345; DedeUserID__ckMd5=md5_val; sid=sid_val; buvid3=SYNTH_BUVID3_OK; buvid4=SYNTH_BUVID4_OK; b_nut=1700000000; _uuid=ignore_uuid; b_lsid=ignore_lsid; CURRENT_FNVAL=4048"
	cookies, rt, err := ParseManualCookieInput(raw, "  refresh_tok_abc  ")
	if err != nil {
		t.Fatalf("ParseManualCookieInput failed: %v", err)
	}
	if rt != "refresh_tok_abc" {
		t.Fatalf("refreshToken = %q, want refresh_tok_abc", rt)
	}
	if len(cookies) != 8 {
		t.Fatalf("len(cookies) = %d, want 8: %+v", len(cookies), cookies)
	}
	if cookies["buvid3"] != "SYNTH_BUVID3_OK" || cookies["buvid4"] != "SYNTH_BUVID4_OK" || cookies["b_nut"] != "1700000000" {
		t.Fatalf("expected buvid3/buvid4/b_nut preserved, got %+v", cookies)
	}
	for _, stripped := range []string{"_uuid", "b_lsid", "CURRENT_FNVAL"} {
		if _, exists := cookies[stripped]; exists {
			t.Fatalf("unexpected non-whitelisted key %s in cookies", stripped)
		}
	}
}

func TestDPAPIStoreRoundTripCorruptAndConfirmState(t *testing.T) {
	dir := t.TempDir()
	credPath := filepath.Join(dir, "credentials", "bilibili.dpapi")

	store, backup, err := Open(credPath)
	if err != nil {
		t.Fatalf("Open empty store: %v", err)
	}
	if backup != "" || store.Status().LoggedIn {
		t.Fatalf("expected logged_out initial state, got %+v", store.Status())
	}

	// Save manual cookies + refresh token.
	secretSess := "my_ultra_secret_sessdata_token_value"
	secretCsrf := "my_ultra_secret_csrf_jct_value"
	secretRT := "my_ultra_secret_refresh_token_value"
	st, err := store.SaveManual("SESSDATA="+secretSess+"; bili_jct="+secretCsrf+"; DedeUserID=8888", secretRT)
	if err != nil {
		t.Fatalf("SaveManual: %v", err)
	}
	if !st.LoggedIn || st.Source != "manual" || !st.HasRefreshToken {
		t.Fatalf("unexpected status after SaveManual: %+v", st)
	}

	// Verify on-disk ciphertext does NOT contain plaintext secrets.
	diskBytes, err := os.ReadFile(credPath)
	if err != nil {
		t.Fatalf("read dpapi file: %v", err)
	}
	for _, secret := range []string{secretSess, secretCsrf, secretRT} {
		if bytes.Contains(diskBytes, []byte(secret)) {
			t.Fatalf("bilibili.dpapi leaked plaintext secret %q", secret)
		}
	}

	// Verify Status JSON does NOT contain plaintext secrets.
	statusJSON, _ := json.Marshal(store.Status())
	for _, secret := range []string{secretSess, secretCsrf, secretRT} {
		if bytes.Contains(statusJSON, []byte(secret)) {
			t.Fatalf("Status JSON leaked secret %q: %s", secret, string(statusJSON))
		}
	}

	// Re-open from disk and verify decryption round-trip.
	store2, _, err := Open(credPath)
	if err != nil {
		t.Fatalf("re-open store: %v", err)
	}
	if got := store2.Cookies()["SESSDATA"]; got != secretSess {
		t.Fatalf("decrypted SESSDATA = %q, want %q", got, secretSess)
	}

	// Verify partial confirmation state persists between refresh step 4 and step 5.
	// Only a QR credential may renew; manual credentials retain their source.
	if _, err := store2.SaveQR(store2.Cookies(), secretRT, 8888, "Synthetic QR", ""); err != nil {
		t.Fatalf("SaveQR before renewal: %v", err)
	}
	newCookies := map[string]string{
		"SESSDATA": "new_sess_step4",
		"bili_jct": "new_csrf_step4",
	}
	if err := store2.CommitRefreshStep4(newCookies, "new_rt_step4", secretRT); err != nil {
		t.Fatalf("CommitRefreshStep4: %v", err)
	}
	store3, _, err := Open(credPath)
	if err != nil {
		t.Fatalf("open after step 4: %v", err)
	}
	rt3, pending3, oldRT3 := store3.RefreshState()
	if rt3 != "new_rt_step4" || !pending3 || oldRT3 != secretRT {
		t.Fatalf("unexpected refresh state after step 4: rt=%q pending=%v oldRT=%q", rt3, pending3, oldRT3)
	}
	if err := store3.CompleteRefreshStep5(); err != nil {
		t.Fatalf("CompleteRefreshStep5: %v", err)
	}
	_, pendingAfter5, oldRTAfter5 := store3.RefreshState()
	if pendingAfter5 || oldRTAfter5 != "" {
		t.Fatalf("expected confirmPending cleared after step 5, got pending=%v oldRT=%q", pendingAfter5, oldRTAfter5)
	}

	// Clear removes file.
	if err := store3.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, err := os.Stat(credPath); !os.IsNotExist(err) {
		t.Fatalf("expected dpapi file removed after Clear, stat err=%v", err)
	}

	// Corrupt file recovery backs up and degrades to decrypt_failed without crashing.
	if err := os.WriteFile(credPath, []byte("corrupted-dpapi-bytes"), 0o600); err != nil {
		t.Fatalf("write corrupt dpapi: %v", err)
	}
	storeCorrupt, backupPath, err := Open(credPath)
	if err != nil {
		t.Fatalf("Open corrupt dpapi should not fail: %v", err)
	}
	if backupPath == "" || !strings.Contains(filepath.Base(backupPath), "bilibili.dpapi.corrupt-") {
		t.Fatalf("expected backup file for corrupt dpapi, got %q", backupPath)
	}
	if storeCorrupt.Status().State != "decrypt_failed" || storeCorrupt.Status().LoggedIn {
		t.Fatalf("expected decrypt_failed state, got %+v", storeCorrupt.Status())
	}
}

func TestAtomicReplaceCredential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials", "bilibili.bin")
	store, _, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveQR(map[string]string{"SESSDATA": "synthetic-old", "bili_jct": "synthetic-csrf"}, "old-refresh", 42, "Original", ""); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	before := store.Status()
	// A file cannot be the parent directory of the new destination. This fails
	// before replacing either the encrypted original or the in-memory record.
	store.path = filepath.Join(path, "cannot-write.bin")
	operations := []func() error{
		func() error {
			_, err := store.SaveQR(map[string]string{"SESSDATA": "new", "bili_jct": "new-csrf"}, "new-refresh", 99, "Replacement", "")
			return err
		},
		func() error { _, err := store.UpdateProfile(99, "Replacement", ""); return err },
		func() error { _, err := store.MarkState("refresh_failed", "Synthetic failure"); return err },
		func() error {
			return store.CommitRefreshStep4(map[string]string{"SESSDATA": "new", "bili_jct": "new-csrf"}, "new-refresh", "old-refresh")
		},
	}
	for i, operation := range operations {
		if err := operation(); err == nil {
			t.Fatalf("operation %d unexpectedly succeeded", i)
		}
		if store.Status() != before || store.Cookies()["SESSDATA"] != "synthetic-old" {
			t.Fatalf("operation %d replaced memory despite failed persistence: %+v", i, store.Status())
		}
		current, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(current, original) {
			t.Fatalf("operation %d changed original ciphertext: %v", i, err)
		}
	}
}

func TestExpiredCredentialIsBlockedWhenStatusCannotPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bilibili.bin")
	store, _, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveManual("SESSDATA=synthetic; bili_jct=csrf", ""); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	store.path = filepath.Join(path, "cannot-write")
	status, err := store.MarkState("expired", "登录已失效")
	if err == nil || status.LoggedIn || store.HasActiveCredential() {
		t.Fatalf("expired credential remained usable: %+v err=%v", status, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("expiration damaged encrypted credential: %v", err)
	}
	if store.Cookies() != nil || store.CSRF() != "" {
		t.Fatalf("expired store still returned Cookies=%v CSRF=%q", store.Cookies(), store.CSRF())
	}
}

func TestRedactEncodedSecretsAndCookieInjectionGuard(t *testing.T) {
	dir := t.TempDir()
	biliStore, _, err := Open(filepath.Join(dir, "bilibili.bin"))
	if err != nil {
		t.Fatal(err)
	}
	rawSess := "abc,def%2Cxyz|123"
	rawCSRF := "csrf_token_9999"
	if _, err := biliStore.SaveVerifiedManual(map[string]string{
		"SESSDATA":   rawSess,
		"bili_jct":   rawCSRF,
		"sid":        "bad\r\nInjected: 1",
		"DedeUserID": "123;evil=1",
	}, "", 1, "u", ""); err != nil {
		t.Fatal(err)
	}
	gotCookies := biliStore.Cookies()
	if gotCookies["sid"] != "" || gotCookies["DedeUserID"] != "" {
		t.Fatalf("CR/LF/semicolon cookie values were not rejected: %+v", gotCookies)
	}
	logLine := "raw=" + rawSess + " enc=abc%2Cdef%252Cxyz%7C123 dec=abc,def,xyz|123 csrf=" + rawCSRF
	redacted := biliStore.RedactString(logLine)
	for _, leak := range []string{"abc,def", "abc%2Cdef", "csrf_token_9999"} {
		if strings.Contains(redacted, leak) {
			t.Fatalf("RedactString leaked %q in %q", leak, redacted)
		}
	}

	// Cross-site save must be rejected.
	if _, err := biliStore.SaveVerifiedDouyinManual(map[string]string{"sessionid": "synth_dy"}, DouyinProfile{}); err == nil {
		t.Fatal("expected Bilibili store to reject SaveVerifiedDouyinManual")
	}
	dyStore, _, err := OpenDouyin(filepath.Join(dir, "douyin.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dyStore.SaveVerifiedManual(map[string]string{"SESSDATA": "synth_bili"}, "", 1, "u", ""); err == nil {
		t.Fatal("expected Douyin store to reject SaveVerifiedManual")
	}
}

func TestBilibiliBuvidAndBNutSentinelRedaction(t *testing.T) {
	dir := t.TempDir()
	mgr, _, err := OpenManager(dir)
	if err != nil {
		t.Fatalf("OpenManager: %v", err)
	}
	biliStore := mgr.Store(SiteBilibili)

	buvid3Sentinel := "SYNTH-BUVID3-SENTINEL-8888infoc"
	buvid4Sentinel := "SYNTH-BUVID4-SENTINEL-9999-token"
	bnutSentinel := "1799887766"
	raw := "SESSDATA=SYNTH_SESS_99; bili_jct=SYNTH_CSRF_99; buvid3=" + buvid3Sentinel + "; buvid4=" + buvid4Sentinel + "; b_nut=" + bnutSentinel
	if _, err := biliStore.SaveManual(raw, ""); err != nil {
		t.Fatalf("SaveManual: %v", err)
	}

	// On-disk ciphertext must not contain any buvid3/buvid4/b_nut sentinel.
	rawBin, err := os.ReadFile(filepath.Join(dir, BilibiliFileName))
	if err != nil {
		t.Fatalf("read bilibili.bin: %v", err)
	}
	for _, sec := range []string{buvid3Sentinel, buvid4Sentinel, bnutSentinel} {
		if bytes.Contains(rawBin, []byte(sec)) {
			t.Fatalf("bilibili.bin leaked plaintext %q", sec)
		}
	}

	// Both Store.RedactString and Manager.RedactString must scrub stored and transient buvid3/buvid4/b_nut values.
	sample := "Cookie: buvid3=" + buvid3Sentinel + "; buvid4=" + buvid4Sentinel + "; b_nut=" + bnutSentinel +
		" transient buvid3=TRANSIENT_B3_VALUE&buvid4%3DTRANSIENT_B4_ENC%26b_nut%3D1700999888" +
		` {"buvid3":"JSON_B3_VAL","buvid4":"JSON_B4_VAL","b_nut":"1700111222"}`
	for label, redacted := range map[string]string{
		"store":   biliStore.RedactString(sample),
		"manager": mgr.RedactString(sample),
	} {
		for _, leak := range []string{
			buvid3Sentinel, buvid4Sentinel, bnutSentinel,
			"TRANSIENT_B3_VALUE", "TRANSIENT_B4_ENC", "1700999888",
			"JSON_B3_VAL", "JSON_B4_VAL", "1700111222",
		} {
			if strings.Contains(redacted, leak) {
				t.Fatalf("%s RedactString leaked %q in %q", label, leak, redacted)
			}
		}
	}
}

func TestDouyinWebIDPersistenceAndBackwardsCompat(t *testing.T) {
	dir := t.TempDir()
	dyPath := filepath.Join(dir, DouyinFileName)

	// 1. Backwards-compatibility: a legacy encrypted Douyin record without douyinWebId loads cleanly.
	store, _, err := OpenDouyin(dyPath)
	if err != nil {
		t.Fatalf("OpenDouyin: %v", err)
	}
	if _, err := store.SaveVerifiedDouyinManual(
		map[string]string{"sessionid": "SYNTH_DY_WEBID_SESS_1"},
		DouyinProfile{SecUID: "MS4wLjABAAAA_WEBID_01", Uname: "测试用户"},
	); err != nil {
		t.Fatalf("SaveVerifiedDouyinManual: %v", err)
	}
	if got := store.DouyinWebID(); got != "" {
		t.Fatalf("initial DouyinWebID = %q, want empty", got)
	}

	// 2. EnsureDouyinWebID generates once, persists, and returns the same ID on subsequent calls and restarts.
	genCalls := 0
	id1, err := store.EnsureDouyinWebID(func() (string, error) {
		genCalls++
		return "7412345678901234567", nil
	})
	if err != nil {
		t.Fatalf("EnsureDouyinWebID: %v", err)
	}
	if id1 != "7412345678901234567" || genCalls != 1 {
		t.Fatalf("id1=%q genCalls=%d", id1, genCalls)
	}
	id2, err := store.EnsureDouyinWebID(func() (string, error) {
		genCalls++
		return "7499999999999999999", nil
	})
	if err != nil || id2 != id1 || genCalls != 1 {
		t.Fatalf("second EnsureDouyinWebID regenerated: id2=%q genCalls=%d err=%v", id2, genCalls, err)
	}

	// Re-pasting expired/new cookie preserves persisted DouyinWebID.
	if _, err := store.SaveVerifiedDouyinManual(
		map[string]string{"sessionid": "SYNTH_DY_WEBID_SESS_2"},
		DouyinProfile{SecUID: "MS4wLjABAAAA_WEBID_01", Uname: "测试用户"},
	); err != nil {
		t.Fatalf("re-save cookie: %v", err)
	}
	if got := store.DouyinWebID(); got != id1 {
		t.Fatalf("DouyinWebID lost after cookie re-paste: got %q want %q", got, id1)
	}

	// Re-open from disk (simulated restart) recovers the exact same webid.
	reopened, _, err := OpenDouyin(dyPath)
	if err != nil {
		t.Fatalf("re-open douyin.bin: %v", err)
	}
	if got := reopened.DouyinWebID(); got != id1 {
		t.Fatalf("reopened DouyinWebID = %q, want %q", got, id1)
	}
	// Cross-site guard rejects SaveDouyinWebID on Bilibili store.
	biliStore, _, err := Open(filepath.Join(dir, BilibiliFileName))
	if err != nil {
		t.Fatal(err)
	}
	if err := biliStore.SaveDouyinWebID("7412345678901234567"); err == nil {
		t.Fatal("expected SaveDouyinWebID rejected on Bilibili store")
	}
}
