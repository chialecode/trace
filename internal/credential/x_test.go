package credential

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestXStoreEntropyIsolationAndBackwardCompatibility(t *testing.T) {
	credDir := filepath.Join(t.TempDir(), "credentials")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Pre-create existing bilibili.bin, douyin.bin, and weibo.bin with their independent entropies.
	biliInit, _, err := Open(filepath.Join(credDir, BilibiliFileName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := biliInit.SaveVerifiedManual(map[string]string{
		"SESSDATA": "bili_existing_sess_001",
		"bili_jct": "bili_existing_csrf_002",
	}, "", 100200, "合成B站账号", ""); err != nil {
		t.Fatal(err)
	}

	dyInit, _, err := OpenDouyin(filepath.Join(credDir, DouyinFileName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dyInit.SaveVerifiedDouyinManual(map[string]string{
		"sessionid": "dy_existing_sess_003",
		"ttwid":     "dy_existing_ttwid_004",
	}, DouyinProfile{
		UID:    "1000000000000001",
		SecUID: "MS4wLjABAAAA__Existing_Dy_01",
		Uname:  "合成抖音账号",
	}); err != nil {
		t.Fatal(err)
	}

	wbInit, _, err := OpenWeibo(filepath.Join(credDir, WeiboFileName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wbInit.SaveVerifiedWeiboManual(map[string]string{
		"SUB":        "_2A25existingWeiboSub==",
		"XSRF-TOKEN": "wb_existing_xsrf_005",
	}, WeiboProfile{
		UID:   "1000000001",
		Uname: "合成微博账号",
	}); err != nil {
		t.Fatal(err)
	}

	// OpenManager must read existing bilibili.bin, douyin.bin, and weibo.bin unchanged alongside x.bin.
	mgr, backups, err := OpenManager(credDir)
	if err != nil {
		t.Fatalf("OpenManager failed: %v", err)
	}
	if len(backups) != 0 {
		t.Fatalf("unexpected backups: %v", backups)
	}
	if !mgr.HasActiveCredential(SiteBilibili) || mgr.Bilibili().Cookies()["SESSDATA"] != "bili_existing_sess_001" {
		t.Fatalf("existing bilibili.bin did not load cleanly: %+v", mgr.Status(SiteBilibili))
	}
	if !mgr.HasActiveCredential(SiteDouyin) || mgr.Douyin().Cookies()["sessionid"] != "dy_existing_sess_003" {
		t.Fatalf("existing douyin.bin did not load cleanly: %+v", mgr.Status(SiteDouyin))
	}
	if !mgr.HasActiveCredential(SiteWeibo) || mgr.Weibo().Cookies()["SUB"] != "_2A25existingWeiboSub==" {
		t.Fatalf("existing weibo.bin did not load cleanly: %+v", mgr.Status(SiteWeibo))
	}
	if mgr.HasActiveCredential(SiteX) || mgr.Status(SiteX).State != "logged_out" {
		t.Fatalf("expected initial x store logged_out, got %+v", mgr.Status(SiteX))
	}

	// Save verified X manual credential.
	xAuth := "0123456789abcdef0123456789abcdef01234567"
	xCT0 := "ABCDEF0123456789synthCt0TokenValue0001"
	xTWID := "u%3D1000000000000000001"
	xGuestID := "v1%3A170000000000000001"
	xKDT := "synthKdtDeviceToken0001"
	st, err := mgr.X().SaveVerifiedXManual(map[string]string{
		"auth_token":         xAuth,
		"ct0":                xCT0,
		"twid":               xTWID,
		"guest_id":           xGuestID,
		"kdt":                xKDT,
		"att":                "strip_att_temp",
		"gt":                 "strip_gt_guest",
		"personalization_id": "strip_pid",
		"__cf_bm":            "strip_cf_bm",
	}, XProfile{
		RestID:     "1000000000000000001",
		ScreenName: "@example_user",
		Name:       "Synthetic Creator",
		Face:       "https://pbs.twimg.com/profile_images/1000000000000000001/synth_400x400.jpg",
	})
	if err != nil {
		t.Fatalf("SaveVerifiedXManual failed: %v", err)
	}
	if !st.LoggedIn || st.Site != SiteX || st.State != "logged_in" || st.Source != "manual" {
		t.Fatalf("unexpected X status: %+v", st)
	}
	if st.UID != "1000000000000000001" || st.UniqueID != "example_user" || st.Uname != "Synthetic Creator" ||
		st.Face != "https://pbs.twimg.com/profile_images/1000000000000000001/synth_400x400.jpg" {
		t.Fatalf("unexpected X profile in status: %+v", st)
	}

	// Renewal must be unavailable with the exact required reason.
	ren := mgr.X().Renewal()
	if ren.Available || ren.Site != SiteX || ren.Reason != "粘贴的 Cookie 不续期" {
		t.Fatalf("X.Renewal() = %+v, want unavailable with reason %q", ren, "粘贴的 Cookie 不续期")
	}
	if renMgr := mgr.Renewal(SiteX); renMgr != ren {
		t.Fatalf("Manager.Renewal(x) = %+v, want %+v", renMgr, ren)
	}

	// Verify x.bin ciphertext does not contain plaintext secrets.
	xPath := filepath.Join(credDir, XFileName)
	rawXCipher, err := os.ReadFile(xPath)
	if err != nil {
		t.Fatalf("read x.bin: %v", err)
	}
	for _, secret := range []string{xAuth, xCT0, xTWID, xGuestID, xKDT} {
		if bytes.Contains(rawXCipher, []byte(secret)) {
			t.Fatalf("x.bin leaked plaintext secret %q", secret)
		}
	}

	// Re-open all four stores via OpenManager and verify round-trip.
	mgr2, backups2, err := OpenManager(credDir)
	if err != nil || len(backups2) != 0 {
		t.Fatalf("reopen OpenManager = (backups=%v, err=%v)", backups2, err)
	}
	gotX := mgr2.X().Cookies()
	if gotX["auth_token"] != xAuth || gotX["ct0"] != xCT0 || gotX["twid"] != xTWID || gotX["guest_id"] != xGuestID || gotX["kdt"] != xKDT {
		t.Fatalf("reopened X cookies mismatch: %+v", gotX)
	}
	for _, stripped := range []string{"att", "gt", "personalization_id", "__cf_bm"} {
		if _, leaked := gotX[stripped]; leaked {
			t.Fatalf("stripped cookie %q persisted in x.bin", stripped)
		}
	}
	if got := mgr2.X().CT0(); got != xCT0 {
		t.Fatalf("CT0() = %q, want %q", got, xCT0)
	}
	if !mgr2.HasActiveCredential(SiteBilibili) || !mgr2.HasActiveCredential(SiteDouyin) || !mgr2.HasActiveCredential(SiteWeibo) {
		t.Fatal("existing site stores lost after saving x.bin")
	}

	// Verify wrong-entropy decryption fails in every cross-site combination involving X.
	wrongEntropyChecks := []struct {
		name      string
		srcFile   string
		openWrong func(string) (*Store, string, error)
	}{
		{"x opened with bilibili entropy", xPath, Open},
		{"x opened with douyin entropy", xPath, OpenDouyin},
		{"x opened with weibo entropy", xPath, OpenWeibo},
		{"bilibili opened with x entropy", filepath.Join(credDir, BilibiliFileName), OpenX},
		{"douyin opened with x entropy", filepath.Join(credDir, DouyinFileName), OpenX},
		{"weibo opened with x entropy", filepath.Join(credDir, WeiboFileName), OpenX},
	}
	for _, tc := range wrongEntropyChecks {
		t.Run(tc.name, func(t *testing.T) {
			data, rErr := os.ReadFile(tc.srcFile)
			if rErr != nil {
				t.Fatal(rErr)
			}
			tmpCopy := filepath.Join(t.TempDir(), "wrong.bin")
			if wErr := os.WriteFile(tmpCopy, data, 0o600); wErr != nil {
				t.Fatal(wErr)
			}
			badStore, badBackup, oErr := tc.openWrong(tmpCopy)
			if oErr != nil {
				t.Fatalf("wrong-entropy open should degrade cleanly, got err=%v", oErr)
			}
			if badBackup == "" || badStore.Status().State != "decrypt_failed" || badStore.HasActiveCredential() {
				t.Fatalf("expected decrypt_failed on wrong entropy, got backup=%q status=%+v", badBackup, badStore.Status())
			}
		})
	}

	// Verify OpenSite supports x and rejects unknown sites.
	openedX, _, err := OpenSite(SiteX, xPath)
	if err != nil || !openedX.HasActiveCredential() || openedX.Site() != SiteX {
		t.Fatalf("OpenSite(x) = (site=%q active=%v err=%v)", openedX.Site(), openedX.HasActiveCredential(), err)
	}
	if _, _, err := OpenSite("unknown", filepath.Join(credDir, "unknown.bin")); !errors.Is(err, ErrUnknownSite) {
		t.Fatalf("OpenSite(unknown) err = %v, want ErrUnknownSite", err)
	}

	// Cross-site writes must be rejected.
	if _, err := mgr2.Bilibili().SaveVerifiedXManual(map[string]string{"auth_token": xAuth, "ct0": xCT0}, XProfile{}); err == nil {
		t.Fatal("bilibili store accepted SaveVerifiedXManual")
	}
	if _, err := mgr2.Douyin().SaveVerifiedXManual(map[string]string{"auth_token": xAuth, "ct0": xCT0}, XProfile{}); err == nil {
		t.Fatal("douyin store accepted SaveVerifiedXManual")
	}
	if _, err := mgr2.Weibo().SaveVerifiedXManual(map[string]string{"auth_token": xAuth, "ct0": xCT0}, XProfile{}); err == nil {
		t.Fatal("weibo store accepted SaveVerifiedXManual")
	}
	if _, err := mgr2.X().SaveVerifiedManual(map[string]string{"SESSDATA": "x"}, "", 1, "u", ""); err == nil {
		t.Fatal("x store accepted SaveVerifiedManual")
	}
	if _, err := mgr2.X().SaveVerifiedDouyinManual(map[string]string{"sessionid": "x"}, DouyinProfile{}); err == nil {
		t.Fatal("x store accepted SaveVerifiedDouyinManual")
	}
	if _, err := mgr2.X().SaveVerifiedWeiboManual(map[string]string{"SUB": "x"}, WeiboProfile{}); err == nil {
		t.Fatal("x store accepted SaveVerifiedWeiboManual")
	}
}

func TestXCookieParserWhitelistControlCharsAndEdgeCases(t *testing.T) {
	// 1. Over 64 KiB raw input rejected without echoing secret.
	oversized := "auth_token=" + strings.Repeat("Z", MaxCookieInputBytes+32) + "; ct0=synthCT0123"
	if _, err := ParseXCookieInput(oversized); !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("expected ErrInputTooLarge, got %v", err)
	} else if strings.Contains(err.Error(), "ZZZZ") {
		t.Fatalf("error echoed secret input: %v", err)
	}

	// 1b. Over 8 KiB raw input with non-whitelisted tracking noise succeeds after cleaning.
	over8KNoise := "auth_token=synth_auth_over8k; ct0=synthCT0over8k; __cf_bm=" + strings.Repeat("C", 12000)
	if cleaned, err := ParseXCookieInput(over8KNoise); err != nil {
		t.Fatalf("expected >8 KiB X cookie with noise to succeed, got %v", err)
	} else if cleaned["auth_token"] != "synth_auth_over8k" || cleaned["ct0"] != "synthCT0over8k" || len(cleaned) != 2 {
		t.Fatalf("unexpected cleaned X cookies: %+v", cleaned)
	}

	// 1c. Cleaned outbound header > 32 KiB rejected without echoing secret.
	oversizedCleaned := "auth_token=" + strings.Repeat("X", MaxCleanedCookieHeaderBytes+64) + "; ct0=synthCT0123"
	if _, err := ParseXCookieInput(oversizedCleaned); !errors.Is(err, ErrCleanedCookieTooLarge) {
		t.Fatalf("expected ErrCleanedCookieTooLarge, got %v", err)
	} else if strings.Contains(err.Error(), "XXXX") {
		t.Fatalf("error echoed secret input: %v", err)
	}

	// 2. Control characters & header-injection attempts rejected without echoing secret.
	secretProbe := "SYNTH_X_PROBE_NEVER_ECHO_9911"
	ctlCases := []struct {
		name string
		raw  string
	}{
		{"nul byte", "auth_token=" + secretProbe + "\x00; ct0=synthCT0123"},
		{"bell ctl", "auth_token=" + secretProbe + "\x07; ct0=synthCT0123"},
		{"escape ctl", "auth_token=" + secretProbe + "\x1b[31m; ct0=synthCT0123"},
		{"del char", "auth_token=" + secretProbe + "\x7f; ct0=synthCT0123"},
		{"c1 ctl", "auth_token=" + secretProbe + "\u0085; ct0=synthCT0123"},
		{"bare cr", "auth_token=" + secretProbe + "\rX-Evil: 1"},
		{"crlf inside Cookie header", "Cookie: auth_token=" + secretProbe + "\r\nX-Injected: evil"},
		{"lf inside Cookie header second cookie", "Cookie: auth_token=" + secretProbe + "\nct0=synthCT0123"},
		{"multiline header injection with colon", "auth_token=" + secretProbe + "\r\nX-Injected: evil"},
		{"multiline header injection with equals", "auth_token=" + secretProbe + "\r\nX-Injected: foo=bar"},
		{"single line header injection with colon in key", "auth_token=" + secretProbe + "; X-Injected: foo=bar"},
	}
	for _, tc := range ctlCases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseXCookieInput(tc.raw); !errors.Is(err, ErrInvalidCookieControlChar) {
				t.Fatalf("ParseXCookieInput(%s) err = %v, want ErrInvalidCookieControlChar", tc.name, err)
			} else if strings.Contains(err.Error(), secretProbe) {
				t.Fatalf("error echoed raw secret: %v", err)
			}
		})
	}

	// 3. Missing auth_token or wrong-case AUTH_TOKEN rejected without echoing input.
	for _, raw := range []string{
		"",
		"   ",
		"ct0=synthCT09911; twid=u%3D1000000000000000001; guest_id=v1%3A170000000000000001",
		"AUTH_TOKEN=upper_auth_9911; ct0=synthCT09911",
		"Auth_Token=mixed_auth_9911; ct0=synthCT09911",
		"auth_token=; ct0=synthCT09911",
	} {
		if _, err := ParseXCookieInput(raw); !errors.Is(err, ErrMissingXAuthToken) {
			t.Fatalf("ParseXCookieInput(%q) err = %v, want ErrMissingXAuthToken", raw, err)
		} else if strings.Contains(err.Error(), "9911") {
			t.Fatalf("error echoed cookie secret: %v", err)
		}
	}

	// 4. Missing ct0 or wrong-case CT0 rejected without echoing input.
	for _, raw := range []string{
		"auth_token=synth_auth_9911; twid=u%3D1000000000000000001",
		"auth_token=synth_auth_9911; CT0=synthCT09911",
		"auth_token=synth_auth_9911; ct0=",
	} {
		if _, err := ParseXCookieInput(raw); !errors.Is(err, ErrMissingXCT0) {
			t.Fatalf("ParseXCookieInput(%q) err = %v, want ErrMissingXCT0", raw, err)
		} else if strings.Contains(err.Error(), "9911") {
			t.Fatalf("error echoed cookie secret: %v", err)
		}
	}

	// 5. Malformed ct0 (non-alphanumeric) rejected locally without echoing input.
	for _, raw := range []string{
		"auth_token=synth_auth_9911; ct0=bad-ct0-9911",
		"auth_token=synth_auth_9911; ct0=bad_ct0_9911",
		"auth_token=synth_auth_9911; ct0=bad=ct0=9911",
		"auth_token=synth_auth_9911; ct0=bad%20ct09911",
		"auth_token=synth_auth_9911; ct0=bad ct0 9911",
		"auth_token=synth_auth_9911; ct0=goodAlnum123; ct0=bad-last-9911",
	} {
		if _, err := ParseXCookieInput(raw); !errors.Is(err, ErrInvalidXCT0) {
			t.Fatalf("ParseXCookieInput(%q) err = %v, want ErrInvalidXCT0", raw, err)
		} else if strings.Contains(err.Error(), "9911") {
			t.Fatalf("error echoed cookie secret: %v", err)
		}
	}

	// 6. Full browser header: Cookie: prefix, trailing CRLF, '=' inside values (twid=u=...),
	// duplicate keys (last non-empty wins), strict case sensitivity, all 5 whitelisted keys kept,
	// and all temporary/tracking/WAF cookies stripped.
	rawHeader := "Cookie: " + strings.Join([]string{
		"auth_token=firstOldAuthToken",
		"auth_token=",
		"auth_token=finalValidAuthToken0123456789abcdef",
		"AUTH_TOKEN=wrong_case_ignored",
		"ct0=bad-old-ct0",
		"ct0=finalValidCt0Alnum0123456789ABCDEF",
		"ct0=",
		"CT0=wrong_case_ct0",
		`twid="u=1000000000000000001"`,
		"guest_id=v1%3A170000000000000001",
		"kdt=synthKdtDeviceToken001",
		// Temporary / guest / tracking / WAF cookies that MUST be stripped:
		"att=1-synthAttToken",
		"gt=1700000000000000099",
		`personalization_id="v1_synthPid=="`,
		"guest_id_marketing=v1%3A170000000000000001",
		"guest_id_ads=v1%3A170000000000000001",
		"lang=en",
		"external_referer=padhuUp37zjgzgv1mFWxJ12Ozwit7owX",
		"__cf_bm=synthCfBmShortLivedToken",
		"night_mode=2",
		"d_prefs=MToxLGNvbnNlbnRfdmVyc2lvbjoy",
	}, "; ") + "\r\n"

	parsed, err := ParseXCookieInput(rawHeader)
	if err != nil {
		t.Fatalf("ParseXCookieInput valid header failed: %v", err)
	}
	wantAll := map[string]string{
		"auth_token": "finalValidAuthToken0123456789abcdef",
		"ct0":        "finalValidCt0Alnum0123456789ABCDEF",
		"twid":       "u=1000000000000000001",
		"guest_id":   "v1%3A170000000000000001",
		"kdt":        "synthKdtDeviceToken001",
	}
	if len(parsed) != len(wantAll) {
		t.Fatalf("parsed key count = %d, want %d; got %+v", len(parsed), len(wantAll), parsed)
	}
	for k, wantV := range wantAll {
		if parsed[k] != wantV {
			t.Fatalf("parsed[%q] = %q, want %q", k, parsed[k], wantV)
		}
	}

	// 7. Multiline paste (no Cookie: prefix, CRLF & LF).
	multiline := "auth_token=multiAuth01\r\nct0=multiCt002\ntwid=u%3D1000000000000000001\n__cf_bm=dropme\n"
	parsedML, err := ParseXCookieInput(multiline)
	if err != nil {
		t.Fatalf("ParseXCookieInput multiline failed: %v", err)
	}
	if len(parsedML) != 3 || parsedML["auth_token"] != "multiAuth01" || parsedML["ct0"] != "multiCt002" || parsedML["twid"] != "u%3D1000000000000000001" {
		t.Fatalf("multiline parsed = %+v", parsedML)
	}

	// 8. ExtractXSetCookies and ExtractXCT0FromSetCookies.
	extracted := ExtractXSetCookies([]*http.Cookie{
		{Name: "ct0", Value: "freshCt0Alnum999"},
		{Name: "CT0", Value: "wrongCase"},
		{Name: "__cf_bm", Value: "cfIgnored"},
		{Name: "twid", Value: "u%3D1000000000000000001"},
		{Name: "guest_id", Value: "deleted", MaxAge: -1},
	})
	if len(extracted) != 2 || extracted["ct0"] != "freshCt0Alnum999" || extracted["twid"] != "u%3D1000000000000000001" {
		t.Fatalf("ExtractXSetCookies = %+v", extracted)
	}
	if ct0, ok := ExtractXCT0FromSetCookies([]*http.Cookie{
		{Name: "ct0", Value: "", MaxAge: -1},
		{Name: "ct0", Value: "oldCt0111"},
		{Name: "ct0", Value: "rotatedCt0222"},
	}); !ok || ct0 != "rotatedCt0222" {
		t.Fatalf("ExtractXCT0FromSetCookies valid = (%q, %v), want (rotatedCt0222, true)", ct0, ok)
	}
	if ct0, ok := ExtractXCT0FromSetCookies([]*http.Cookie{
		{Name: "ct0", Value: "bad-ct0-with-dash"},
	}); ok || ct0 != "" {
		t.Fatalf("ExtractXCT0FromSetCookies invalid = (%q, %v), want empty false", ct0, ok)
	}
}

func TestXTWIDParsingAndCT0SyncAndPerSiteIsolation(t *testing.T) {
	// 1. ParseXTWID valid forms.
	validTWID := map[string]string{
		"u=1":                         "1",
		"u=00012":                     "12",
		"u=1000000000000000001":       "1000000000000000001",
		"u%3D1000000000000000001":     "1000000000000000001",
		"u%3d001000000000000000001":   "1000000000000000001",
		`"u=1000000000000000001"`:     "1000000000000000001",
		`"u%3D18446744073709551615"`:  "18446744073709551615",
		"  u%3D1000000000000000001  ": "1000000000000000001",
	}
	for raw, want := range validTWID {
		got, err := ParseXTWID(raw)
		if err != nil || got != want {
			t.Fatalf("ParseXTWID(%q) = (%q, %v), want (%q, nil)", raw, got, err, want)
		}
	}

	// 2. ParseXTWID invalid forms must reject without echoing input.
	for _, bad := range []string{
		"",
		"   ",
		"u=",
		"u=0",
		"u=0000",
		"u=18446744073709551616",
		"u=99999999999999999999",
		"u=100000000000000000000",
		"u=abc9911",
		"v=1000000000000000001",
		"U=1000000000000000001",
		"1000000000000000001",
		"u%253D1000000000000000001",
		"u%ZZ1000000000000000001",
		"u=1000000000000000001\r\n",
		"u=1000000000000000001;x=1",
	} {
		got, err := ParseXTWID(bad)
		if !errors.Is(err, ErrInvalidXTWID) || got != "" {
			t.Fatalf("ParseXTWID(%q) = (%q, %v), want (\"\", ErrInvalidXTWID)", bad, got, err)
		}
		if strings.Contains(err.Error(), "9911") || strings.Contains(err.Error(), "1000000000000000001") {
			t.Fatalf("ParseXTWID error echoed raw input: %v", err)
		}
	}

	// 3. Per-site isolation, SyncXCT0, MergeContextCookies, UpdateXProfile, expire, clear.
	credDir := filepath.Join(t.TempDir(), "credentials")
	mgr, _, err := OpenManager(credDir)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := mgr.Bilibili().SaveManual("SESSDATA=bili_iso_sess_11; bili_jct=bili_iso_csrf_12", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Douyin().SaveDouyinManual("sessionid=dy_iso_sess_21; ttwid=dy_iso_ttwid_22"); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Weibo().SaveWeiboManual("SUB=_2A25wb_iso_sub_31==; XSRF-TOKEN=wb_xsrf_32"); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.X().SaveXManual("auth_token=x_iso_auth_41; ct0=xIsoCt0Old42; twid=u%3D1000000000000000001; guest_id=v1%3A170000000000000001; kdt=xIsoKdt43"); err != nil {
		t.Fatal(err)
	}

	// MergeContextCookies on X MUST update ct0 ONLY and never overwrite auth_token/twid/guest_id/kdt.
	if err := mgr.X().MergeContextCookies(map[string]string{
		"ct0":        "xIsoCt0Rotated44",
		"auth_token": "must_not_overwrite_auth",
		"twid":       "u%3D999999",
		"guest_id":   "v1%3A999999",
		"kdt":        "must_not_overwrite_kdt",
		"__cf_bm":    "must_not_add_cf",
	}); err != nil {
		t.Fatalf("X MergeContextCookies failed: %v", err)
	}
	xCookies := mgr.X().Cookies()
	if xCookies["ct0"] != "xIsoCt0Rotated44" {
		t.Fatalf("ct0 = %q, want xIsoCt0Rotated44", xCookies["ct0"])
	}
	if xCookies["auth_token"] != "x_iso_auth_41" || xCookies["twid"] != "u%3D1000000000000000001" ||
		xCookies["guest_id"] != "v1%3A170000000000000001" || xCookies["kdt"] != "xIsoKdt43" {
		t.Fatalf("MergeContextCookies mutated non-ct0 X cookies: %+v", xCookies)
	}
	if _, added := xCookies["__cf_bm"]; added {
		t.Fatal("MergeContextCookies unexpectedly added __cf_bm")
	}

	// MergeContextCookies without ct0 is a no-op.
	if err := mgr.X().MergeContextCookies(map[string]string{"twid": "u%3D888888"}); err != nil {
		t.Fatalf("MergeContextCookies without ct0 failed: %v", err)
	}
	if mgr.X().Cookies()["twid"] != "u%3D1000000000000000001" {
		t.Fatal("MergeContextCookies without ct0 mutated twid")
	}

	// SyncXCT0 and MergeContextCookies reject invalid ct0 without mutating memory or disk.
	for _, badCT0 := range []string{"", "bad-dash", "bad_under", "bad=eq", "bad\r\n"} {
		if err := mgr.X().SyncXCT0(badCT0); !errors.Is(err, ErrInvalidXCT0) {
			t.Fatalf("SyncXCT0(%q) err = %v, want ErrInvalidXCT0", badCT0, err)
		}
		if err := mgr.X().MergeContextCookies(map[string]string{"ct0": badCT0}); !errors.Is(err, ErrInvalidXCT0) {
			t.Fatalf("MergeContextCookies(ct0=%q) err = %v, want ErrInvalidXCT0", badCT0, err)
		}
	}
	if got := mgr.X().CT0(); got != "xIsoCt0Rotated44" {
		t.Fatalf("ct0 mutated after rejected sync: %q", got)
	}

	// Direct SyncXCT0 persists to x.bin across reopen.
	if err := mgr.X().SyncXCT0("xIsoCt0Synced45"); err != nil {
		t.Fatalf("SyncXCT0 valid failed: %v", err)
	}
	reopenedX, _, err := OpenX(filepath.Join(credDir, XFileName))
	if err != nil {
		t.Fatal(err)
	}
	if got := reopenedX.CT0(); got != "xIsoCt0Synced45" {
		t.Fatalf("persisted CT0() = %q, want xIsoCt0Synced45", got)
	}

	// UpdateXProfile updates metadata and preserves manual source + cookies.
	profSt, err := mgr.X().UpdateXProfile(XProfile{
		RestID:     "1000000000000000002",
		ScreenName: "synthetic_creator",
		Name:       "更新后的合成作者",
		Face:       "https://pbs.twimg.com/profile_images/1000000000000000002/new_400x400.jpg",
	})
	if err != nil || profSt.UID != "1000000000000000002" || profSt.UniqueID != "synthetic_creator" ||
		profSt.Uname != "更新后的合成作者" || profSt.Source != "manual" {
		t.Fatalf("UpdateXProfile = (%+v, %v)", profSt, err)
	}

	// Expiring X must NOT affect Bilibili, Douyin, or Weibo.
	if _, err := mgr.MarkExpired(SiteX, "X 登录已失效"); err != nil {
		t.Fatal(err)
	}
	if mgr.HasActiveCredential(SiteX) || mgr.X().CookieHeader() != "" || mgr.X().CT0() != "" {
		t.Fatal("expired X store remained active")
	}
	if err := mgr.X().SyncXCT0("xShouldFailExpired46"); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("SyncXCT0 on expired X store err = %v, want ErrNotLoggedIn", err)
	}
	if !mgr.HasActiveCredential(SiteBilibili) || !mgr.HasActiveCredential(SiteDouyin) || !mgr.HasActiveCredential(SiteWeibo) {
		t.Fatal("expiring X unexpectedly affected other sites")
	}

	// Clearing X deletes x.bin only; bilibili.bin, douyin.bin, and weibo.bin remain intact.
	if err := mgr.Clear(SiteX); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(credDir, XFileName)); !os.IsNotExist(err) {
		t.Fatalf("expected x.bin removed, stat err=%v", err)
	}
	for _, fn := range []string{BilibiliFileName, DouyinFileName, WeiboFileName} {
		if _, err := os.Stat(filepath.Join(credDir, fn)); err != nil {
			t.Fatalf("%s damaged after clearing X: %v", fn, err)
		}
	}
	if !mgr.HasActiveCredential(SiteBilibili) || !mgr.HasActiveCredential(SiteDouyin) || !mgr.HasActiveCredential(SiteWeibo) {
		t.Fatal("other site credentials lost after clearing X")
	}
}

func TestManagerRedactStringCoversAllFourSitesAndXSensitiveFields(t *testing.T) {
	credDir := filepath.Join(t.TempDir(), "credentials")
	mgr, _, err := OpenManager(credDir)
	if err != nil {
		t.Fatal(err)
	}

	biliSess := "bili_four_sess_101"
	biliCSRF := "bili_four_csrf_102"
	if _, err := mgr.Bilibili().SaveManual("SESSDATA="+biliSess+"; bili_jct="+biliCSRF, ""); err != nil {
		t.Fatal(err)
	}

	dySess := "dy_four_sess_201"
	dyUIFID := "dy_four_uifid_202"
	if _, err := mgr.Douyin().SaveDouyinManual("sessionid=" + dySess + "; UIFID=" + dyUIFID + "; ttwid=dy_four_ttwid_203; is_staff_user=false; login_time=1731400000000"); err != nil {
		t.Fatal(err)
	}

	wbSub := "_2A25wb_four_sub_301=="
	wbXsrf := "wb_four_xsrf_302"
	if _, err := mgr.Weibo().SaveVerifiedWeiboManual(map[string]string{
		"SUB":        wbSub,
		"XSRF-TOKEN": wbXsrf,
	}, WeiboProfile{UID: "1000000001", Uname: "合成微博用户"}); err != nil {
		t.Fatal(err)
	}

	xSecrets := map[string]string{
		"auth_token": "synthXAuthSecretValue401abcdef",
		"ct0":        "synthXCt0SecretValue402ABCDEF",
		"twid":       "u%3D1000000000000000403",
		"guest_id":   "v1%3A170000000000000404",
		"kdt":        "synthXKdtSecretValue405",
	}
	if _, err := mgr.X().SaveVerifiedXManual(xSecrets, XProfile{
		RestID:     "1000000000000000403",
		ScreenName: "example_user",
		Name:       "Synthetic Creator",
	}); err != nil {
		t.Fatal(err)
	}

	twidDecoded, _ := url.QueryUnescape(xSecrets["twid"])
	guestDecoded, _ := url.QueryUnescape(xSecrets["guest_id"])

	ephemAuth := "ephemXAuthToken501"
	ephemCT0 := "ephemXCt0Token502"
	ephemKDT := "ephemXKdtToken503"
	ephemCSRFHdr := "ephemXCsrfHeader504"
	ephemBearerHdr := "SYNTH_BEARER_HEADER_505%2B%3D%3D"
	ephemBearerJSON := "SYNTH_BEARER_JSON_506%2F%3D"
	ephemBearerEnc := "SYNTH_BEARER_ENC_507"
	ephemEncAuth := "ephemEncAuth508"
	ephemEncCT0 := "ephemEncCt0509"
	ephemEncKDT := "ephemEncKdt510"

	leaked := strings.Join([]string{
		fmt.Sprintf("bili=%s csrf=%s dy=%s uifid=%s wb=%s wbxsrf=%s", biliSess, biliCSRF, dySess, dyUIFID, wbSub, wbXsrf),
		"bare_x_auth=" + xSecrets["auth_token"] + " enc_x_auth=" + url.QueryEscape(xSecrets["auth_token"]),
		"bare_x_ct0=" + xSecrets["ct0"] + " bare_x_kdt=" + xSecrets["kdt"],
		"bare_x_twid=" + xSecrets["twid"] + " enc_x_twid=" + url.QueryEscape(xSecrets["twid"]) + " dec_x_twid=" + twidDecoded,
		"bare_x_guest=" + xSecrets["guest_id"] + " enc_x_guest=" + url.QueryEscape(xSecrets["guest_id"]) + " dec_x_guest=" + guestDecoded,
		fmt.Sprintf("Cookie: auth_token=%s; ct0=%s; kdt=%s", ephemAuth, ephemCT0, ephemKDT),
		fmt.Sprintf(`json={"auth_token":"%s","ct0":"%s","kdt":"%s","x-csrf-token":"%s","Authorization":"Bearer %s"}`,
			ephemAuth, ephemCT0, ephemKDT, ephemCSRFHdr, ephemBearerJSON),
		"x-csrf-token: " + ephemCSRFHdr,
		"Authorization: Bearer " + ephemBearerHdr,
		url.QueryEscape("Authorization: Bearer " + ephemBearerEnc),
		fmt.Sprintf("encoded=auth_token%%3D%s%%26ct0%%3D%s%%26kdt%%3D%s", ephemEncAuth, ephemEncCT0, ephemEncKDT),
	}, "\n")

	redacted := mgr.RedactString(leaked)
	mustNotAppear := []string{
		biliSess, biliCSRF, dySess, dyUIFID, wbSub, wbXsrf,
		xSecrets["auth_token"], url.QueryEscape(xSecrets["auth_token"]),
		xSecrets["ct0"], xSecrets["kdt"],
		xSecrets["twid"], url.QueryEscape(xSecrets["twid"]), twidDecoded,
		xSecrets["guest_id"], url.QueryEscape(xSecrets["guest_id"]), guestDecoded,
		ephemAuth, ephemCT0, ephemKDT,
		ephemCSRFHdr, ephemBearerHdr, ephemBearerJSON, ephemBearerEnc,
		ephemEncAuth, ephemEncCT0, ephemEncKDT,
	}
	for _, needle := range mustNotAppear {
		if strings.Contains(redacted, needle) {
			t.Fatalf("Manager.RedactString leaked %q in:\n%s", needle, redacted)
		}
	}

	// Ordinary benign logs must stay untouched.
	plainLog := `ok=false recording=false at=1731400000000 status=200 host=x.com count=20 cursor=bottom alt=json`
	if got := mgr.RedactString(plainLog); got != plainLog {
		t.Fatalf("Manager.RedactString altered benign log:\ngot:  %q\nwant: %q", got, plainLog)
	}

	// Statuses JSON across all 4 sites must never leak any secret.
	statuses := mgr.Statuses()
	if len(statuses) != 4 {
		t.Fatalf("len(mgr.Statuses()) = %d, want 4", len(statuses))
	}
	statusesJSON, _ := json.Marshal(statuses)
	for _, sec := range []string{biliSess, dySess, wbSub, xSecrets["auth_token"], xSecrets["ct0"], xSecrets["kdt"]} {
		if bytes.Contains(statusesJSON, []byte(sec)) {
			t.Fatalf("Statuses JSON leaked secret %q", sec)
		}
	}

	// Even when logged out of X, regex redaction must work across Manager and all 4 per-site Stores.
	if err := mgr.Clear(SiteX); err != nil {
		t.Fatal(err)
	}
	extraCases := []struct {
		name    string
		in      string
		secrets []string
	}{
		{
			name:    "unsaved X kv pairs",
			in:      `Cookie: auth_token=unsaved_auth_601; ct0="unsaved_ct0_602"; kdt='unsaved_kdt_603'`,
			secrets: []string{"unsaved_auth_601", "unsaved_ct0_602", "unsaved_kdt_603"},
		},
		{
			name:    "url.QueryEscape X cookie header",
			in:      url.QueryEscape("Cookie: auth_token=qe_auth_604; ct0=qe_ct0_605; kdt=qe_kdt_606"),
			secrets: []string{"qe_auth_604", "qe_ct0_605", "qe_kdt_606"},
		},
		{
			name:    "inline x-csrf-token headers",
			in:      `err: (x-csrf-token: hdr_ct0_607; X-CSRF-Token: "hdr_ct0_608")`,
			secrets: []string{"hdr_ct0_607", "hdr_ct0_608"},
		},
		{
			name:    "url.QueryEscape x-csrf-token header",
			in:      url.QueryEscape("x-csrf-token: qe_hdr_ct0_609"),
			secrets: []string{"qe_hdr_ct0_609"},
		},
		{
			name:    "Authorization Bearer header and JSON",
			in:      `req Authorization: Bearer hdr_bearer_610 json={"authorization":"Bearer json_bearer_611"} quoted Authorization: "Bearer q_bearer_612"`,
			secrets: []string{"hdr_bearer_610", "json_bearer_611", "q_bearer_612"},
		},
	}

	redactors := []struct {
		label string
		fn    func(string) string
	}{
		{"manager", mgr.RedactString},
		{"bilibili-store", mgr.Bilibili().RedactString},
		{"douyin-store", mgr.Douyin().RedactString},
		{"weibo-store", mgr.Weibo().RedactString},
		{"x-store", mgr.X().RedactString},
	}
	for _, r := range redactors {
		for _, tc := range extraCases {
			t.Run(r.label+"/"+tc.name, func(t *testing.T) {
				got := r.fn(tc.in)
				for _, sec := range tc.secrets {
					if strings.Contains(got, sec) {
						t.Fatalf("%s RedactString(%s) leaked %q in %q", r.label, tc.name, sec, got)
					}
				}
				if again := r.fn(got); again != got {
					t.Fatalf("%s RedactString(%s) not idempotent:\nfirst:  %q\nsecond: %q", r.label, tc.name, got, again)
				}
			})
		}
	}
}
