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
	// 1. Over 8 KiB input is rejected without echoing input.
	hugeSecret := "SESSDATA=" + strings.Repeat("A", MaxCookieInputBytes+100) + "; bili_jct=csrf123"
	if _, _, err := ParseManualCookieInput(hugeSecret, ""); !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("expected ErrInputTooLarge, got %v", err)
	} else if strings.Contains(err.Error(), "AAAA") {
		t.Fatalf("error echoed secret input: %v", err)
	}

	// 2. Missing SESSDATA is rejected without echoing bili_jct value.
	noSess := "bili_jct=super_secret_bili_jct_value_999; other_key=1"
	if _, _, err := ParseManualCookieInput(noSess, ""); !errors.Is(err, ErrMissingRequiredKeys) {
		t.Fatalf("expected ErrMissingRequiredKeys, got %v", err)
	} else if strings.Contains(err.Error(), "super_secret_bili_jct_value_999") {
		t.Fatalf("error echoed secret bili_jct: %v", err)
	}

	// 3. Valid Cookie header with extra non-whitelisted fields filters strictly to the 5 allowed keys.
	raw := "Cookie: SESSDATA=sess_val_1; bili_jct=csrf_val_2; DedeUserID=12345; DedeUserID__ckMd5=md5_val; sid=sid_val; buvid3=ignore_me; _uuid=ignore_uuid"
	cookies, rt, err := ParseManualCookieInput(raw, "  refresh_tok_abc  ")
	if err != nil {
		t.Fatalf("ParseManualCookieInput failed: %v", err)
	}
	if rt != "refresh_tok_abc" {
		t.Fatalf("refreshToken = %q, want refresh_tok_abc", rt)
	}
	if len(cookies) != 5 {
		t.Fatalf("len(cookies) = %d, want 5: %+v", len(cookies), cookies)
	}
	if _, exists := cookies["buvid3"]; exists {
		t.Fatalf("unexpected non-whitelisted key buvid3 in cookies")
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

	// Verify on-disk ciphertext does NOT contain plaintext secrets (C37).
	diskBytes, err := os.ReadFile(credPath)
	if err != nil {
		t.Fatalf("read dpapi file: %v", err)
	}
	for _, secret := range []string{secretSess, secretCsrf, secretRT} {
		if bytes.Contains(diskBytes, []byte(secret)) {
			t.Fatalf("bilibili.dpapi leaked plaintext secret %q", secret)
		}
	}

	// Verify Status JSON does NOT contain plaintext secrets (C37).
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

	// Test Step 4 -> Step 5 partial confirmation persistence (C08).
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

func TestManualCookieParseWhitelistAndLimit(t *testing.T) {
	TestManualCookieParserWhitelistAndBounds(t)
}

func TestDPAPIRoundTripAndCorruptBackup(t *testing.T) {
	TestDPAPIStoreRoundTripCorruptAndConfirmState(t)
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
}
