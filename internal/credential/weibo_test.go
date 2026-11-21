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

func TestWeiboStoreEntropyIsolationAndBackwardCompatibility(t *testing.T) {
	credDir := filepath.Join(t.TempDir(), "credentials")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Pre-create existing bilibili.bin and douyin.bin encrypted with their independent entropies.
	biliStoreInit, _, err := Open(filepath.Join(credDir, BilibiliFileName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := biliStoreInit.SaveVerifiedManual(map[string]string{
		"SESSDATA": "bili_existing_sess_001",
		"bili_jct": "bili_existing_csrf_002",
	}, "", 100200, "合成B站账号", ""); err != nil {
		t.Fatal(err)
	}

	dyStoreInit, _, err := OpenDouyin(filepath.Join(credDir, DouyinFileName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dyStoreInit.SaveVerifiedDouyinManual(map[string]string{
		"sessionid": "dy_existing_sess_003",
		"ttwid":     "dy_existing_ttwid_004",
	}, DouyinProfile{
		UID:    "1000000000000001",
		SecUID: "MS4wLjABAAAA__Existing_Dy_01",
		Uname:  "合成抖音账号",
	}); err != nil {
		t.Fatal(err)
	}

	// OpenManager must read existing bilibili.bin and douyin.bin unchanged alongside weibo.bin.
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
	if mgr.HasActiveCredential(SiteWeibo) || mgr.Status(SiteWeibo).State != "logged_out" {
		t.Fatalf("expected initial weibo store logged_out, got %+v", mgr.Status(SiteWeibo))
	}

	// Save verified Weibo manual credential.
	wbSub := "_2A25synthWeiboSubToken=="
	wbSubp := "0033WrSXqPxfM725synthSubp"
	wbXsrf := "synthXsrfToken_Weibo_001"
	wbWbp := "synthWbpSess=Pad=="
	st, err := mgr.Weibo().SaveVerifiedWeiboManual(map[string]string{
		"SUB":        wbSub,
		"SUBP":       wbSubp,
		"XSRF-TOKEN": wbXsrf,
		"WBPSESS":    wbWbp,
		"SINAGLOBAL": "should_be_stripped_stat",
	}, WeiboProfile{
		UID:   "1000000001",
		Uname: "合成示例用户",
		Face:  "https://tvax1.sinaimg.cn/synth_avatar.jpg",
	})
	if err != nil {
		t.Fatalf("SaveVerifiedWeiboManual failed: %v", err)
	}
	if !st.LoggedIn || st.Site != SiteWeibo || st.State != "logged_in" || st.Source != "manual" {
		t.Fatalf("unexpected Weibo status: %+v", st)
	}
	if st.UID != "1000000001" || st.Uname != "合成示例用户" || st.Face != "https://tvax1.sinaimg.cn/synth_avatar.jpg" {
		t.Fatalf("unexpected Weibo profile in status: %+v", st)
	}

	// Renewal must be unavailable with the exact required reason.
	ren := mgr.Weibo().Renewal()
	if ren.Available || ren.Site != SiteWeibo || ren.Reason != "粘贴的 Cookie 不续期" {
		t.Fatalf("Weibo.Renewal() = %+v, want unavailable with reason %q", ren, "粘贴的 Cookie 不续期")
	}
	if renMgr := mgr.Renewal(SiteWeibo); renMgr != ren {
		t.Fatalf("Manager.Renewal(weibo) = %+v, want %+v", renMgr, ren)
	}

	// Verify weibo.bin ciphertext does not contain plaintext secrets.
	wbPath := filepath.Join(credDir, WeiboFileName)
	rawWbCipher, err := os.ReadFile(wbPath)
	if err != nil {
		t.Fatalf("read weibo.bin: %v", err)
	}
	for _, secret := range []string{wbSub, wbSubp, wbXsrf, wbWbp} {
		if bytes.Contains(rawWbCipher, []byte(secret)) {
			t.Fatalf("weibo.bin leaked plaintext secret %q", secret)
		}
	}

	// Re-open all three stores via OpenManager and verify round-trip.
	mgr2, backups2, err := OpenManager(credDir)
	if err != nil || len(backups2) != 0 {
		t.Fatalf("reopen OpenManager = (backups=%v, err=%v)", backups2, err)
	}
	if got := mgr2.Weibo().Cookies(); got["SUB"] != wbSub || got["XSRF-TOKEN"] != wbXsrf || got["WBPSESS"] != wbWbp {
		t.Fatalf("reopened Weibo cookies mismatch: %+v", got)
	}
	if _, leakedStat := mgr2.Weibo().Cookies()["SINAGLOBAL"]; leakedStat {
		t.Fatal("SINAGLOBAL stat cookie persisted in weibo.bin")
	}
	if got := mgr2.Weibo().XSRFToken(); got != wbXsrf {
		t.Fatalf("XSRFToken() = %q, want %q", got, wbXsrf)
	}

	// Verify wrong-entropy decryption fails in every cross-site combination.
	wrongEntropyChecks := []struct {
		name      string
		srcFile   string
		openWrong func(string) (*Store, string, error)
	}{
		{"weibo opened with bilibili entropy", wbPath, Open},
		{"weibo opened with douyin entropy", wbPath, OpenDouyin},
		{"bilibili opened with weibo entropy", filepath.Join(credDir, BilibiliFileName), OpenWeibo},
		{"douyin opened with weibo entropy", filepath.Join(credDir, DouyinFileName), OpenWeibo},
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

	// Verify OpenSite supports weibo and rejects unknown sites.
	openedWb, _, err := OpenSite(SiteWeibo, wbPath)
	if err != nil || !openedWb.HasActiveCredential() || openedWb.Site() != SiteWeibo {
		t.Fatalf("OpenSite(weibo) = (site=%q active=%v err=%v)", openedWb.Site(), openedWb.HasActiveCredential(), err)
	}
	if _, _, err := OpenSite("unknown", filepath.Join(credDir, "unknown.bin")); !errors.Is(err, ErrUnknownSite) {
		t.Fatalf("OpenSite(unknown) err = %v, want ErrUnknownSite", err)
	}

	// Cross-site writes must be rejected.
	if _, err := mgr2.Bilibili().SaveVerifiedWeiboManual(map[string]string{"SUB": wbSub}, WeiboProfile{}); err == nil {
		t.Fatal("bilibili store accepted SaveVerifiedWeiboManual")
	}
	if _, err := mgr2.Douyin().SaveVerifiedWeiboManual(map[string]string{"SUB": wbSub}, WeiboProfile{}); err == nil {
		t.Fatal("douyin store accepted SaveVerifiedWeiboManual")
	}
	if _, err := mgr2.Weibo().SaveVerifiedManual(map[string]string{"SESSDATA": "x"}, "", 1, "u", ""); err == nil {
		t.Fatal("weibo store accepted SaveVerifiedManual")
	}
	if _, err := mgr2.Weibo().SaveVerifiedDouyinManual(map[string]string{"sessionid": "x"}, DouyinProfile{}); err == nil {
		t.Fatal("weibo store accepted SaveVerifiedDouyinManual")
	}
}

func TestWeiboCookieParserWhitelistControlCharsAndEdgeCases(t *testing.T) {
	// 1. Over 8 KiB input rejected without echoing secret.
	oversized := "SUB=" + strings.Repeat("Z", MaxCookieInputBytes+32)
	if _, err := ParseWeiboCookieInput(oversized); !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("expected ErrInputTooLarge, got %v", err)
	} else if strings.Contains(err.Error(), "ZZZZ") {
		t.Fatalf("error echoed secret input: %v", err)
	}

	// 2. Control characters & header-injection attempts rejected without echoing secret.
	secretProbe := "SYNTH_PROBE_NEVER_ECHO_9911"
	ctlCases := []struct {
		name string
		raw  string
	}{
		{"nul byte", "SUB=" + secretProbe + "\x00; SUBP=ok"},
		{"bell ctl", "SUB=" + secretProbe + "\x07"},
		{"escape ctl", "SUB=" + secretProbe + "\x1b[31m"},
		{"del char", "SUB=" + secretProbe + "\x7f"},
		{"c1 ctl", "SUB=" + secretProbe + "\u0085"},
		{"bare cr", "SUB=" + secretProbe + "\rX-Evil: 1"},
		{"crlf inside Cookie header", "Cookie: SUB=" + secretProbe + "\r\nX-Injected: evil"},
		{"lf inside Cookie header second cookie", "Cookie: SUB=" + secretProbe + "\nSUBP=second"},
		{"multiline header injection with colon", "SUB=" + secretProbe + "\r\nX-Injected: evil"},
		{"multiline header injection with equals", "SUB=" + secretProbe + "\r\nX-Injected: foo=bar"},
		{"single line header injection with colon in key", "SUB=" + secretProbe + "; X-Injected: foo=bar"},
	}
	for _, tc := range ctlCases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseWeiboCookieInput(tc.raw); !errors.Is(err, ErrInvalidCookieControlChar) {
				t.Fatalf("ParseWeiboCookieInput(%s) err = %v, want ErrInvalidCookieControlChar", tc.name, err)
			} else if strings.Contains(err.Error(), secretProbe) {
				t.Fatalf("error echoed raw secret: %v", err)
			}
		})
	}

	// 3. Missing SUB or wrong-case sub rejected without echoing input.
	for _, raw := range []string{
		"",
		"   ",
		"SUBP=synth_subp_9911; XSRF-TOKEN=synth_xsrf_9911; SINAGLOBAL=123",
		"sub=lowercase_sub_secret_9911; SUBP=synth_subp",
		"Sub=mixed_sub_secret_9911",
		"SUB=",
	} {
		if _, err := ParseWeiboCookieInput(raw); !errors.Is(err, ErrMissingWeiboSub) {
			t.Fatalf("ParseWeiboCookieInput(%q) err = %v, want ErrMissingWeiboSub", raw, err)
		} else if strings.Contains(err.Error(), "9911") {
			t.Fatalf("error echoed cookie secret: %v", err)
		}
	}

	// 4. Full browser header: Cookie: prefix, trailing CRLF, '=' inside values, duplicate keys (last non-empty wins),
	// strict case sensitivity, all 11 whitelisted keys kept, all stats/UI fields stripped.
	rawHeader := "Cookie: " + strings.Join([]string{
		"SUB=_2A25firstOldSub==",
		"SUB=",
		"SUB=_2A25finalValidSub=with=equals==",
		"sub=wrong_case_ignored",
		"SUBP=0033WrSXqPxfM725synthSubp",
		"ALF=1799999999",
		"SCF=synthScfToken==",
		"WBPSESS=synthWbp=Part1=Part2==",
		"XSRF-TOKEN=synthXsrfOld",
		"XSRF-TOKEN=synthXsrfFinal",
		"XSRF-TOKEN=",
		"xsrf-token=wrong_case_xsrf",
		"SSOLoginState=1760000001",
		"SUHB=0synthSuhb",
		"PC_TOKEN=synthPcToken",
		"MLOGIN=1",
		"_T_WM=synthTwm12345",
		// Stats / UI fields that MUST be stripped:
		"SINAGLOBAL=1.2.3.4",
		"UOR=,,www.example.com",
		"_s_tentry=weibo.com",
		"Apache=5.6.7.8",
		"ULV=1760000001:1:1:1",
		"WBStorage=abc123",
		"wb_view_log_1000000001=1920*10801",
		"webim_unReadCount=%7B%22time%22%3A1%7D",
		"login_sid_t=strip_me",
		"cross_origin_proto=SSL",
		"WEIBOCN_FROM=1110006030",
		"M_WEIBOCN_PARAMS=luicode%3D10000011",
		"ariaDefaultTheme=default",
		"ariaFixed=true",
		"ariaStatus=false",
		"s=strip_s",
		"wvr=6",
		"un=strip_un",
	}, "; ") + "\r\n"

	parsed, err := ParseWeiboCookieInput(rawHeader)
	if err != nil {
		t.Fatalf("ParseWeiboCookieInput valid header failed: %v", err)
	}
	wantAll := map[string]string{
		"SUB":           "_2A25finalValidSub=with=equals==",
		"SUBP":          "0033WrSXqPxfM725synthSubp",
		"ALF":           "1799999999",
		"SCF":           "synthScfToken==",
		"WBPSESS":       "synthWbp=Part1=Part2==",
		"XSRF-TOKEN":    "synthXsrfFinal",
		"SSOLoginState": "1760000001",
		"SUHB":          "0synthSuhb",
		"PC_TOKEN":      "synthPcToken",
		"MLOGIN":        "1",
		"_T_WM":         "synthTwm12345",
	}
	if len(parsed) != len(wantAll) {
		t.Fatalf("parsed key count = %d, want %d; got %+v", len(parsed), len(wantAll), parsed)
	}
	for k, wantV := range wantAll {
		if parsed[k] != wantV {
			t.Fatalf("parsed[%q] = %q, want %q", k, parsed[k], wantV)
		}
	}

	// 5. Multiline paste (no Cookie: header prefix, LF & CRLF).
	multiline := "SUB=_2A25multiSub==\r\nSUBP=multiSubp\nXSRF-TOKEN=multiXsrf\nSINAGLOBAL=dropme\n"
	parsedML, err := ParseWeiboCookieInput(multiline)
	if err != nil {
		t.Fatalf("ParseWeiboCookieInput multiline failed: %v", err)
	}
	if len(parsedML) != 3 || parsedML["SUB"] != "_2A25multiSub==" || parsedML["SUBP"] != "multiSubp" || parsedML["XSRF-TOKEN"] != "multiXsrf" {
		t.Fatalf("multiline parsed = %+v", parsedML)
	}

	// 6. ExtractWeiboSetCookies filters Set-Cookie headers by whitelist and case.
	extracted := ExtractWeiboSetCookies([]*http.Cookie{
		{Name: "XSRF-TOKEN", Value: "freshXsrf_999"},
		{Name: "xsrf-token", Value: "wrongCase"},
		{Name: "SINAGLOBAL", Value: "statIgnored"},
		{Name: "SUB", Value: "_2A25setCookieSub=="},
	})
	if len(extracted) != 2 || extracted["XSRF-TOKEN"] != "freshXsrf_999" || extracted["SUB"] != "_2A25setCookieSub==" {
		t.Fatalf("ExtractWeiboSetCookies = %+v", extracted)
	}
}

func TestWeiboMergeContextCookiesAndPerSiteIsolation(t *testing.T) {
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
	if _, err := mgr.Weibo().SaveWeiboManual("SUB=_2A25wb_iso_sub_31==; SUBP=wb_iso_subp_32; XSRF-TOKEN=wb_xsrf_old_33"); err != nil {
		t.Fatal(err)
	}

	// MergeContextCookies on Weibo MUST update XSRF-TOKEN only and never overwrite SUB/SUBP/etc.
	if err := mgr.Weibo().MergeContextCookies(map[string]string{
		"XSRF-TOKEN":    "wb_xsrf_new_44",
		"SUB":           "must_not_overwrite_sub",
		"SUBP":          "must_not_overwrite_subp",
		"WBPSESS":       "must_not_add_wbpsess",
		"SSOLoginState": "must_not_add_sso",
		"SINAGLOBAL":    "must_not_add_stat",
	}); err != nil {
		t.Fatalf("Weibo MergeContextCookies failed: %v", err)
	}
	wbCookies := mgr.Weibo().Cookies()
	if wbCookies["XSRF-TOKEN"] != "wb_xsrf_new_44" {
		t.Fatalf("XSRF-TOKEN = %q, want wb_xsrf_new_44", wbCookies["XSRF-TOKEN"])
	}
	if wbCookies["SUB"] != "_2A25wb_iso_sub_31==" || wbCookies["SUBP"] != "wb_iso_subp_32" {
		t.Fatalf("MergeContextCookies mutated identity cookies: %+v", wbCookies)
	}
	if _, added := wbCookies["WBPSESS"]; added {
		t.Fatal("MergeContextCookies unexpectedly added WBPSESS")
	}

	// Re-open from disk to confirm XSRF-TOKEN persisted.
	reopenedWb, _, err := OpenWeibo(filepath.Join(credDir, WeiboFileName))
	if err != nil {
		t.Fatal(err)
	}
	if got := reopenedWb.XSRFToken(); got != "wb_xsrf_new_44" {
		t.Fatalf("persisted XSRFToken() = %q, want wb_xsrf_new_44", got)
	}

	// UpdateWeiboProfile updates metadata and keeps manual source + cookies.
	profSt, err := mgr.Weibo().UpdateWeiboProfile(WeiboProfile{
		UID:   "1000000002",
		Uname: "更新后的合成昵称",
		Face:  "https://tvax2.sinaimg.cn/new.jpg",
	})
	if err != nil || profSt.UID != "1000000002" || profSt.Uname != "更新后的合成昵称" || profSt.Source != "manual" {
		t.Fatalf("UpdateWeiboProfile = (%+v, %v)", profSt, err)
	}

	// Expiring Weibo must NOT affect Bilibili or Douyin.
	if _, err := mgr.MarkExpired(SiteWeibo, "微博登录已失效"); err != nil {
		t.Fatal(err)
	}
	if mgr.HasActiveCredential(SiteWeibo) || mgr.Weibo().CookieHeader() != "" || mgr.Weibo().XSRFToken() != "" {
		t.Fatal("expired Weibo store remained active")
	}
	if err := mgr.Weibo().MergeContextCookies(map[string]string{"XSRF-TOKEN": "should_fail_expired"}); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("MergeContextCookies on expired Weibo store err = %v, want ErrNotLoggedIn", err)
	}
	if !mgr.HasActiveCredential(SiteBilibili) || !mgr.HasActiveCredential(SiteDouyin) {
		t.Fatal("expiring Weibo unexpectedly affected Bilibili or Douyin")
	}

	// Clearing Weibo deletes weibo.bin only; bilibili.bin and douyin.bin stay intact.
	if err := mgr.Clear(SiteWeibo); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(credDir, WeiboFileName)); !os.IsNotExist(err) {
		t.Fatalf("expected weibo.bin removed, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(credDir, BilibiliFileName)); err != nil {
		t.Fatalf("bilibili.bin damaged after clearing Weibo: %v", err)
	}
	if _, err := os.Stat(filepath.Join(credDir, DouyinFileName)); err != nil {
		t.Fatalf("douyin.bin damaged after clearing Weibo: %v", err)
	}
	if !mgr.HasActiveCredential(SiteBilibili) || !mgr.HasActiveCredential(SiteDouyin) {
		t.Fatal("Bilibili or Douyin lost after clearing Weibo")
	}
}

func TestManagerRedactStringCoversAllThreeSitesAndWeiboSensitiveFields(t *testing.T) {
	credDir := filepath.Join(t.TempDir(), "credentials")
	mgr, _, err := OpenManager(credDir)
	if err != nil {
		t.Fatal(err)
	}

	biliSess := "bili_sentinel_sess_101"
	biliCSRF := "bili_sentinel_csrf_102"
	if _, err := mgr.Bilibili().SaveManual("SESSDATA="+biliSess+"; bili_jct="+biliCSRF, ""); err != nil {
		t.Fatal(err)
	}

	dySess := "dy_sentinel_sess_201"
	dyUIFID := "dy_sentinel_uifid_202"
	if _, err := mgr.Douyin().SaveDouyinManual("sessionid=" + dySess + "; UIFID=" + dyUIFID + "; ttwid=dy_sentinel_ttwid_203; is_staff_user=false; login_time=1731400000000"); err != nil {
		t.Fatal(err)
	}

	wbSecrets := map[string]string{
		"SUB":           "_2A25wb_sent_sub+plus==",
		"SUBP":          "0033wb_sent_subp_302",
		"SCF":           "wb_sent_scf_303==",
		"ALF":           "1799999304",
		"WBPSESS":       "wb_sent_wbpsess_305==",
		"XSRF-TOKEN":    "wb_sent_xsrf_306",
		"SSOLoginState": "1760000307",
		"SUHB":          "0wb_sent_suhb_308",
		"PC_TOKEN":      "wb_sent_pctoken_309",
		"_T_WM":         "wb_sent_twm_310",
	}
	if _, err := mgr.Weibo().SaveVerifiedWeiboManual(wbSecrets, WeiboProfile{UID: "1000000001", Uname: "合成用户"}); err != nil {
		t.Fatal(err)
	}

	// Build a composite log string containing:
	// - Bilibili & Douyin secrets
	// - Bare stored Weibo secrets (without key names) and URL-encoded forms
	// - Unstored Weibo fields (ALC, st, alt, ticket) in query, JSON, header, and adjacent URL-encoded forms
	ephemeralALC := "wb_ephem_alc_401"
	ephemeralST := "wb_ephem_st_402"
	ephemeralALT := "wb_ephem_alt_403"
	ephemeralTicket := "wb_ephem_ticket_404"
	ephemeralHeaderXSRF := "wb_hdr_xsrf_405"
	ephemeralEncSUB := "wb_enc_sub_406"
	ephemeralEncTicket := "wb_enc_ticket_407"
	ephemeralEncALT := "wb_enc_alt_408"
	ephemeralEncST := "wb_enc_st_409"

	leaked := strings.Join([]string{
		fmt.Sprintf("bili=%s csrf=%s dy=%s uifid=%s", biliSess, biliCSRF, dySess, dyUIFID),
		"bare_sub=" + wbSecrets["SUB"] + " enc_sub=" + url.QueryEscape(wbSecrets["SUB"]),
		"bare_wbpsess=" + wbSecrets["WBPSESS"] + " bare_scf=" + wbSecrets["SCF"],
		"bare_subp=" + wbSecrets["SUBP"] + " bare_alf=" + wbSecrets["ALF"],
		"bare_sso=" + wbSecrets["SSOLoginState"] + " bare_suhb=" + wbSecrets["SUHB"],
		"bare_pctok=" + wbSecrets["PC_TOKEN"] + " bare_twm=" + wbSecrets["_T_WM"],
		fmt.Sprintf("Cookie: ALC=%s; st=%s; alt=%s; ticket=%s", ephemeralALC, ephemeralST, ephemeralALT, ephemeralTicket),
		fmt.Sprintf(`json={"ALC":"%s","st":"%s","alt":"%s","ticket":"%s"}`, ephemeralALC, ephemeralST, ephemeralALT, ephemeralTicket),
		"X-XSRF-TOKEN: " + ephemeralHeaderXSRF,
		fmt.Sprintf("encoded=SUB%%3D%s%%26st%%3D%s%%26alt%%3D%s%%26ticket%%3D%s", ephemeralEncSUB, ephemeralEncST, ephemeralEncALT, ephemeralEncTicket),
	}, "\n")

	redacted := mgr.RedactString(leaked)
	mustNotAppear := []string{
		biliSess, biliCSRF, dySess, dyUIFID,
		wbSecrets["SUB"], url.QueryEscape(wbSecrets["SUB"]),
		wbSecrets["SUBP"], wbSecrets["SCF"], wbSecrets["ALF"], wbSecrets["WBPSESS"],
		wbSecrets["XSRF-TOKEN"], wbSecrets["SSOLoginState"], wbSecrets["SUHB"],
		wbSecrets["PC_TOKEN"], wbSecrets["_T_WM"],
		ephemeralALC, ephemeralST, ephemeralALT, ephemeralTicket,
		ephemeralHeaderXSRF, ephemeralEncSUB, ephemeralEncTicket, ephemeralEncALT, ephemeralEncST,
	}
	for _, needle := range mustNotAppear {
		if strings.Contains(redacted, needle) {
			t.Fatalf("Manager.RedactString leaked %q in:\n%s", needle, redacted)
		}
	}

	// Ordinary non-secret logs and standard Google Drive alt=json must remain intact.
	plainLog := `ok=false recording=false at=1731400000000 status=200 host=weibo.com last=5 alt=json`
	if got := mgr.RedactString(plainLog); got != plainLog {
		t.Fatalf("Manager.RedactString altered benign log:\ngot:  %q\nwant: %q", got, plainLog)
	}

	// Statuses JSON across all 3 sites must never leak any secret.
	statusesJSON, _ := json.Marshal(mgr.Statuses())
	for _, sec := range []string{biliSess, dySess, wbSecrets["SUB"], wbSecrets["XSRF-TOKEN"], wbSecrets["WBPSESS"]} {
		if bytes.Contains(statusesJSON, []byte(sec)) {
			t.Fatalf("Statuses JSON leaked secret %q", sec)
		}
	}

	// Even when logged out of Weibo, key=value Weibo sensitive fields (including url.QueryEscape,
	// tight ":"/"=" prefixes, quoted values, and inline X-XSRF-TOKEN headers) are still redacted.
	if err := mgr.Clear(SiteWeibo); err != nil {
		t.Fatal(err)
	}
	extraLeaks := []struct {
		name    string
		in      string
		secrets []string
	}{
		{
			name:    "plain unsaved kv",
			in:      "failed login SUB=unsaved_sub_501; XSRF-TOKEN=unsaved_xsrf_502; ticket=unsaved_tkt_503",
			secrets: []string{"unsaved_sub_501", "unsaved_xsrf_502", "unsaved_tkt_503"},
		},
		{
			name:    "url.QueryEscape full Cookie header with spaces (+)",
			in:      url.QueryEscape("Cookie: SUB=qe_sub_601+plus==; SUBP=qe_subp_602; XSRF-TOKEN=qe_xsrf_603"),
			secrets: []string{"qe_sub_601", "qe_subp_602", "qe_xsrf_603"},
		},
		{
			name:    "url.QueryEscape without space after colon",
			in:      url.QueryEscape("Cookie:SUB=qe_nospace_604;SUBP=qe_nospace_605"),
			secrets: []string{"qe_nospace_604", "qe_nospace_605"},
		},
		{
			name:    "url.QueryEscape space-separated short tokens",
			in:      url.QueryEscape("SUB=qe_sp_606 st=qe_st_607 alt=qe_alt_608 ticket=qe_tkt_609"),
			secrets: []string{"qe_sp_606", "qe_st_607", "qe_alt_608", "qe_tkt_609"},
		},
		{
			name:    "url.QueryEscape quoted value",
			in:      url.QueryEscape(`SUB="qe_quoted_610"`),
			secrets: []string{"qe_quoted_610"},
		},
		{
			name:    "tight colon and equals prefixes plus quoted values",
			in:      `Cookie:SUB=tight_sub_701 cookie=SUBP=tight_subp_702 WBPSESS="quoted_wbp_703" SCF='quoted_scf_704'`,
			secrets: []string{"tight_sub_701", "tight_subp_702", "quoted_wbp_703", "quoted_scf_704"},
		},
		{
			name:    "inline X-XSRF-TOKEN header in log line",
			in:      `err: request failed (X-XSRF-TOKEN: inline_xsrf_801; x-xsrf-token: "inline_xsrf_802")`,
			secrets: []string{"inline_xsrf_801", "inline_xsrf_802"},
		},
	}
	for _, tc := range extraLeaks {
		t.Run(tc.name, func(t *testing.T) {
			got := mgr.RedactString(tc.in)
			for _, sec := range tc.secrets {
				if strings.Contains(got, sec) {
					t.Fatalf("RedactString(%s) leaked %q in %q", tc.name, sec, got)
				}
			}
			// Idempotence check: redacting an already-redacted string leaves it unchanged.
			if again := mgr.RedactString(got); again != got {
				t.Fatalf("RedactString(%s) not idempotent:\nfirst:  %q\nsecond: %q", tc.name, got, again)
			}
		})
	}
}
