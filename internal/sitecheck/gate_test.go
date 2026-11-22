package sitecheck

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestQuotaGateBlocksBeforeNetworkRequestAndScansReceipts(t *testing.T) {
	ledgerDir := t.TempDir()
	limits := Limits{
		ActID:         "ACT-001",
		MaxRuns:       2,
		MaxAPICalls:   3,
		MaxMediaFiles: 1,
		MaxMediaBytes: 1024,
		MaxByCategory: map[string]int{
			"nav":          2,
			"dynamic_feed": 1,
		},
	}

	gate, err := OpenGate(ledgerDir, limits)
	if err != nil {
		t.Fatalf("OpenGate: %v", err)
	}

	if err := gate.BeginRun(); err != nil {
		t.Fatalf("BeginRun 1: %v", err)
	}

	var serverHits atomic.Int32
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen 127.0.0.1:0: %v", err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverHits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	srv.Listener = ln
	srv.Start()
	defer srv.Close()

	client := &http.Client{
		Transport: gate.WrapTransport(http.DefaultTransport, func(r *http.Request) string {
			if r.URL.Path == "/nav" {
				return "nav"
			}
			return "dynamic_feed"
		}),
	}

	// 2 allowed nav calls
	for i := 0; i < 2; i++ {
		resp, err := client.Get(srv.URL + "/nav")
		if err != nil {
			t.Fatalf("nav call %d failed: %v", i+1, err)
		}
		_ = resp.Body.Close()
	}
	if got := serverHits.Load(); got != 2 {
		t.Fatalf("serverHits = %d, want 2", got)
	}

	// 3rd nav call exceeds category limit (2/2): must be blocked BEFORE network request
	if _, err := client.Get(srv.URL + "/nav"); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("3rd nav call err = %v, want ErrQuotaExceeded", err)
	}
	if got := serverHits.Load(); got != 2 {
		t.Fatalf("serverHits after blocked category call = %d, want 2 (0 new requests sent)", got)
	}

	// 1 allowed dynamic_feed call brings total APICalls to 3/3
	resp, err := client.Get(srv.URL + "/feed")
	if err != nil {
		t.Fatalf("feed call 1 failed: %v", err)
	}
	_ = resp.Body.Close()
	if got := serverHits.Load(); got != 3 {
		t.Fatalf("serverHits = %d, want 3", got)
	}

	// Next call exceeds MaxAPICalls (3/3): must be blocked BEFORE network request
	if _, err := client.Get(srv.URL + "/feed"); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("4th API call err = %v, want ErrQuotaExceeded", err)
	}
	if got := serverHits.Load(); got != 3 {
		t.Fatalf("serverHits after MaxAPICalls exhausted = %d, want 3 (0 new requests sent)", got)
	}

	// Verify persistence across reopen
	reopened, err := OpenGate(ledgerDir, limits)
	if err != nil {
		t.Fatalf("reopen OpenGate: %v", err)
	}
	u := reopened.Usage()
	if u.Runs != 1 || u.APICalls != 3 || u.NavCalls != 2 || u.DynamicFeeds != 1 {
		t.Fatalf("reopened usage mismatch: %+v", u)
	}
	if err := reopened.BeginRun(); err != nil {
		t.Fatalf("BeginRun 2: %v", err)
	}
	if err := reopened.BeginRun(); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("BeginRun 3 err = %v, want ErrQuotaExceeded", err)
	}

	// Media quota gate
	if err := reopened.AcquireMedia(512); err != nil {
		t.Fatalf("AcquireMedia 1: %v", err)
	}
	if err := reopened.AcquireMedia(128); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("AcquireMedia 2 err = %v, want ErrQuotaExceeded", err)
	}

	// Scan repo artifacts/site-check (if present)
	repoReceiptDir := filepath.Join("..", "..", "artifacts", "site-check")
	findings, err := ScanReceiptsForSecrets(repoReceiptDir)
	if err != nil {
		t.Fatalf("ScanReceiptsForSecrets(%s): %v", repoReceiptDir, err)
	}
	if len(findings) != 0 {
		t.Fatalf("unexpected secret findings in %s: %v", repoReceiptDir, findings)
	}

	// Negative tests for ScanReceiptsForSecrets (Bilibili + Douyin Cookie markers & signed URLs)
	badCases := []struct {
		name    string
		payload string
	}{
		{"bili-sessdata", `{"cookie":"SESSDATA=secret_leak"}`},
		{"douyin-sessionid", `{"cookie":"sessionid=0123456789abcdef"}`},
		{"douyin-sessionid-ss", `{"cookie":"sessionid_ss=0123456789abcdef"}`},
		{"douyin-sid-guard", `{"cookie":"sid_guard=tok%7C1760000000%7C5184000"}`},
		{"douyin-mstoken", `{"cookie":"msToken=AbCdEfGh12345678"}`},
		{"douyin-abogus-url", `{"endpoint":"https://www.douyin.com/aweme/v1/web/aweme/detail/?aweme_id=7300000000000000001&a_bogus=dJUnkqSiQoWb"}`},
		{"douyin-x-signature-url", `{"media":"https://v3-web.douyinvod.com/video/tos/cn/1/?x-expires=1763003600&x-signature=9f8e7d6c5b4a"}`},
	}
	for _, tc := range badCases {
		badDir := t.TempDir()
		badFile := filepath.Join(badDir, tc.name+".json")
		if err := os.WriteFile(badFile, []byte(tc.payload), 0o600); err != nil {
			t.Fatalf("write bad receipt %s: %v", tc.name, err)
		}
		badFindings, err := ScanReceiptsForSecrets(badDir)
		if err != nil {
			t.Fatalf("ScanReceiptsForSecrets(%s): %v", tc.name, err)
		}
		if len(badFindings) == 0 {
			t.Fatalf("expected secret finding for %s, got none", tc.name)
		}
		for _, f := range badFindings {
			if strings.Contains(f, "dJUnkqSiQoWb") || strings.Contains(f, "9f8e7d6c5b4a") {
				t.Fatalf("ScanReceiptsForSecrets(%s) echoed raw secret in finding: %s", tc.name, f)
			}
		}
	}
}

func TestGateCategoryStrictnessAndErrors(t *testing.T) {
	dir := t.TempDir()
	gate, err := OpenGate(dir, Limits{
		ActID:       "ACT-STRICT",
		MaxRuns:     1,
		MaxAPICalls: 5,
		MaxByCategory: map[string]int{
			"allowed": 1,
			"zero":    0,
		},
	})
	if err != nil {
		t.Fatalf("OpenGate: %v", err)
	}
	if err := gate.AcquireAPI("unlisted"); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("unlisted category err = %v, want ErrQuotaExceeded", err)
	}
	if err := gate.AcquireAPI("zero"); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("zero-limit category err = %v, want ErrQuotaExceeded", err)
	}
	if err := gate.AcquireAPI("allowed"); err != nil {
		t.Fatalf("allowed category err = %v, want nil", err)
	}
	if err := gate.AcquireMedia(-1); err == nil {
		t.Fatal("AcquireMedia(-1) succeeded, want error")
	}

	// Non-NotExist read error (ledger path is a directory) must fail OpenGate.
	badDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(badDir, "usage-ACT-BAD.json"), 0o700); err != nil {
		t.Fatalf("mkdir ledger dir: %v", err)
	}
	if _, err := OpenGate(badDir, Limits{ActID: "ACT-BAD"}); err == nil {
		t.Fatal("OpenGate on directory ledger succeeded, want error")
	}
}

func TestScanSecretsWeiboCookiesSignedURLsAndPlaceholders(t *testing.T) {
	badCases := []struct {
		name      string
		payload   string
		rawSecret string
	}{
		{"weibo-sub", `{"cookie":"SUB=_2A25SynthSubToken0001abcdef"}`, "_2A25SynthSubToken0001abcdef"},
		{"weibo-subp", `{"cookie":"SUBP=0033WrSXqPxfM725Ws9jqgMF55529P9D"}`, "0033WrSXqPxfM725Ws9jqgMF55529P9D"},
		{"weibo-xsrf-token", `{"cookie":"XSRF-TOKEN=AbCdEfGh123456_7890-XyZ"}`, "AbCdEfGh123456_7890-XyZ"},
		{"weibo-wbpsess", `{"cookie":"WBPSESS=Dt2hbAUaXSynthWbpsess0123=="}`, "Dt2hbAUaXSynthWbpsess0123=="},
		{"weibo-scf", `{"cookie":"SCF=AlSynthScfToken0123456789._-"}`, "AlSynthScfToken0123456789._-"},
		{"weibo-alf", `{"cookie":"ALF=1795000000"}`, "1795000000"},
		{"weibo-sso-login-state", `{"cookie":"SSOLoginState=1763700000"}`, "1763700000"},
		{"weibo-suhb", `{"cookie":"SUHB=02M_SynthSuhb001"}`, "02M_SynthSuhb001"},
		{"weibo-pc-token", `{"cookie":"PC_TOKEN=0123456789abcdef0123456789abcdef"}`, "0123456789abcdef0123456789abcdef"},
		{"weibo-t-wm", `{"cookie":"_T_WM=fedcba9876543210fedcba9876543210"}`, "fedcba9876543210fedcba9876543210"},
		{"weibo-video-signed-url", `{"media":"https://f.video.weibocdn.com/o0/synth01.mp4?Expires=1763700000&ssig=AbCdEf%2B12%3D&KID=unistore,video"}`, "AbCdEf%2B12%3D"},
		{"weibo-video-signed-url-json-escaped", `{"media":"https://f.video.weibocdn.com/o0/synth01.mp4?Expires=1763700000\u0026ssig=JsonEscSig987\u0026KID=unistore,video"}`, "JsonEscSig987"},
		{"weibo-livephoto-signed-url", `{"media":"https://livephoto.us.sinaimg.cn/001synth.mov?ssig=9f8e7d6c5b&Expires=1763700000&KID=unistore,video"}`, "9f8e7d6c5b"},
		{"weibo-expires-kid-url", `{"media":"https://f.video.weibocdn.com/o0/synth02.mp4?Expires=1763700000&KID=unistore,video"}`, "1763700000"},
		{"weibo-kid-expires-url-json-escaped", `{"media":"https://f.video.weibocdn.com/o0/synth03.mp4?KID=unistore,video\u0026Expires=1763700000"}`, "1763700000"},
		{"weibo-bare-signed-query", `{"query":"Expires=1763700000&ssig=BareQuerySig1&KID=unistore,video"}`, "BareQuerySig1"},
		{"weibo-qmarkless-amp-url", `{"media":"https://f.video.weibocdn.com/o0/synth01.mp4&ssig=SecretSig1234"}`, "SecretSig1234"},
		{"weibo-livephoto-encoded-url", `{"media":"https://video.weibo.com/media/play?livephoto=https%3A%2F%2Flivephoto.us.sinaimg.cn%2F001synth.mov%3FExpires%3D1763700000%26ssig%3DEncLiveSig1234%26KID%3Dunistore%2Cvideo"}`, "EncLiveSig1234"},
		{"weibo-livephoto-encoded-exp-kid", `{"media":"https://video.weibo.com/media/play?livephoto=https%3A%2F%2Flivephoto.us.sinaimg.cn%2F001synth.mov%3FExpires%3D1763700001%26KID%3Dunistore%2Cvideo"}`, "1763700001"},
		{"weibo-livephoto-encoded-kid-exp", `{"media":"https://video.weibo.com/media/play?livephoto=https%3A%2F%2Flivephoto.us.sinaimg.cn%2F001synth.mov%3FKID%3Dunistore%2Cvideo%26Expires%3D1763700002"}`, "1763700002"},
	}

	for _, tc := range badCases {
		t.Run(tc.name, func(t *testing.T) {
			badDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(badDir, tc.name+".json"), []byte(tc.payload), 0o600); err != nil {
				t.Fatalf("write bad receipt %s: %v", tc.name, err)
			}
			findings, err := ScanReceiptsForSecrets(badDir)
			if err != nil {
				t.Fatalf("ScanReceiptsForSecrets(%s): %v", tc.name, err)
			}
			if len(findings) == 0 {
				t.Fatalf("expected secret finding for %s, got none", tc.name)
			}
			for _, f := range findings {
				if strings.Contains(f, tc.rawSecret) {
					t.Fatalf("ScanReceiptsForSecrets(%s) echoed raw secret %q in finding: %s", tc.name, tc.rawSecret, f)
				}
			}
		})
	}

	safeCases := []struct {
		name    string
		payload string
	}{
		{"placeholders-angle-zh", `{"note":"SUB=<合成占位>; XSRF-TOKEN=<合成占位>; WBPSESS=<合成占位>"}`},
		{"placeholders-angle-en", `{"note":"SUB=<synthetic>; SUBP=<synthetic>; SCF=<synthetic>; ALF=<synthetic>; SSOLoginState=<synthetic>"}`},
		{"placeholders-json-escaped", `{"note":"SUB=\u003c合成占位\u003e; Expires=\u003cUnix秒\u003e\u0026ssig=\u003c合成签名\u003e\u0026KID=unistore,video"}`},
		{"placeholders-url-signed", `{"note":"https://f.video.weibocdn.com/o0/demo.mp4?Expires=<Unix秒>&ssig=<合成签名>&KID=unistore,video"}`},
		{"placeholders-url-reordered", `{"note":"https://f.video.weibocdn.com/o0/demo.mp4?KID=unistore,video&Expires=<Unix秒>&ssig=<合成签名>"}`},
		{"placeholders-livephoto-encoded", `{"note":"https://video.weibo.com/media/play?livephoto=https%3A%2F%2Flivephoto.us.sinaimg.cn%2F001synth.mov%3FExpires%3D<Unix秒>%26ssig%3D<合成签名>%26KID%3Dunistore%2Cvideo"}`},
		{"bare-key-names", `{"note":"SUB= SUBP= XSRF-TOKEN= WBPSESS= SUB=... SUB=[REDACTED]"}`},
		{"lowercase-sub-query", `{"route":"/library?type=&sub=subscription_123456&mid="}`},
		{"public-image-url", `{"image":"https://wx1.sinaimg.cn/large/001SynThPic01ly1synthetic00001.jpg"}`},
	}
	for _, tc := range safeCases {
		t.Run("safe_"+tc.name, func(t *testing.T) {
			if findings := ScanTextForSecrets(tc.payload); len(findings) != 0 {
				t.Fatalf("false positive on %s: %v", tc.name, findings)
			}
		})
	}

	t.Run("safe_actual_weibo_md", func(t *testing.T) {
		weiboDocPath := filepath.Join("..", "..", "docs", "research", "weibo.md")
		raw, err := os.ReadFile(weiboDocPath)
		if err != nil {
			t.Fatalf("read %s: %v", weiboDocPath, err)
		}
		if findings := ScanTextForSecrets(string(raw)); len(findings) != 0 {
			t.Fatalf("false positive on docs/research/weibo.md: %v", findings)
		}
	})
}

func TestOpenGatePreservesPreExistingLedgerCounts(t *testing.T) {
	ledgerDir := t.TempDir()
	preExisting := `{
  "actId": "act024",
  "updatedAt": "2026-11-22T12:00:00Z",
  "runs": 0,
  "apiCalls": 1,
  "navCalls": 0,
  "dynamicFeeds": 0,
  "videoViews": 0,
  "playUrlCalls": 0,
  "dynamicDetails": 0,
  "spaceReads": 0,
  "qrCalls": 0,
  "mediaFiles": 0,
  "mediaBytes": 0,
  "categories": {
    "login": 1
  }
}`
	ledgerPath := filepath.Join(ledgerDir, "usage-act024.json")
	if err := os.WriteFile(ledgerPath, []byte(preExisting), 0o600); err != nil {
		t.Fatalf("write pre-existing ledger: %v", err)
	}

	limits := Limits{
		ActID:       "act024",
		MaxRuns:     1,
		MaxAPICalls: 32,
		MaxByCategory: map[string]int{
			"login":  5,
			"detail": 3,
			"other":  30,
		},
	}
	gate, err := OpenGate(ledgerDir, limits)
	if err != nil {
		t.Fatalf("OpenGate: %v", err)
	}
	u := gate.Usage()
	if u.ActID != "act024" || u.Runs != 0 || u.APICalls != 1 || u.Categories["login"] != 1 {
		t.Fatalf("OpenGate did not preserve pre-existing ledger counts: %+v", u)
	}

	if err := gate.BeginRun(); err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	u = gate.Usage()
	if u.Runs != 1 || u.APICalls != 1 || u.Categories["login"] != 1 {
		t.Fatalf("BeginRun altered pre-existing api/category counts: %+v", u)
	}
	if err := gate.BeginRun(); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("2nd BeginRun err = %v, want ErrQuotaExceeded", err)
	}

	var serverHits atomic.Int32
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen 127.0.0.1:0: %v", err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverHits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	srv.Listener = ln
	srv.Start()
	defer srv.Close()

	client := &http.Client{
		Transport: gate.WrapTransport(http.DefaultTransport, func(r *http.Request) string {
			if r.URL.Path == "/login" {
				return "login"
			}
			return "other"
		}),
	}

	// Only 4 additional login calls are allowed (1 preflight + 4 = 5).
	for i := 0; i < 4; i++ {
		resp, err := client.Get(srv.URL + "/login")
		if err != nil {
			t.Fatalf("login call %d failed: %v", i+1, err)
		}
		_ = resp.Body.Close()
	}
	if got := serverHits.Load(); got != 4 {
		t.Fatalf("serverHits after 4 login calls = %d, want 4", got)
	}
	if _, err := client.Get(srv.URL + "/login"); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("5th new login call err = %v, want ErrQuotaExceeded", err)
	}
	if got := serverHits.Load(); got != 4 {
		t.Fatalf("serverHits after blocked login call = %d, want 4 (0 new requests sent)", got)
	}

	// Reopening must retain Runs=1, APICalls=5, login=5.
	reopened, err := OpenGate(ledgerDir, limits)
	if err != nil {
		t.Fatalf("reopen OpenGate: %v", err)
	}
	u = reopened.Usage()
	if u.Runs != 1 || u.APICalls != 5 || u.Categories["login"] != 5 {
		t.Fatalf("reopened usage mismatch: %+v", u)
	}

	// Remaining total API budget is 32 - 5 = 27 calls across other categories.
	client2 := &http.Client{
		Transport: reopened.WrapTransport(http.DefaultTransport, func(r *http.Request) string {
			return "other"
		}),
	}
	for i := 0; i < 27; i++ {
		resp, err := client2.Get(srv.URL + "/other")
		if err != nil {
			t.Fatalf("other call %d failed: %v", i+1, err)
		}
		_ = resp.Body.Close()
	}
	if got := serverHits.Load(); got != 31 {
		t.Fatalf("serverHits after 27 other calls = %d, want 31", got)
	}
	if _, err := client2.Get(srv.URL + "/other"); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("33rd total API call err = %v, want ErrQuotaExceeded", err)
	}
	if got := serverHits.Load(); got != 31 {
		t.Fatalf("serverHits after total quota exhaustion = %d, want 31 (0 new requests sent)", got)
	}
}

func TestScanSecretsXCookiesHeadersBearerSignedURLsAndPlaceholders(t *testing.T) {
	synthAuth := "0123456789abcdef0123456789abcdef01234567"
	synthCT0 := "abcdef0123456789abcdef0123456789"
	synthCT0Long := strings.Repeat("a1b2c3d4e5f60718", 10)
	synthKDT := "synthKdtDeviceToken000123456789"
	synthWebBearer := "AAAAAAAAAAAAAAAAAAAAASynthWebBearer0001%3D%3D"

	badCases := []struct {
		name      string
		payload   string
		rawSecret string
	}{
		{"x-auth-token-raw", `{"cookie":"auth_token=` + synthAuth + `"}`, synthAuth},
		{"x-auth-token-quoted", `{"cookie":"auth_token='` + synthAuth + `'"}`, synthAuth},
		{"x-auth-token-json-escaped-quote", `{"cookie":"auth_token=\"` + synthAuth + `\""}`, synthAuth},
		{"x-auth-token-encoded", `{"q":"auth_token%3D` + synthAuth + `"}`, synthAuth},
		{"x-auth-token-query-escaped", `{"q":"` + url.QueryEscape("Cookie: auth_token="+synthAuth+"; ct0="+synthCT0+"; kdt="+synthKDT) + `"}`, synthAuth},
		{"x-auth-token-path-escaped", `{"q":"` + url.PathEscape("Cookie: auth_token="+synthAuth+"; ct0="+synthCT0) + `"}`, synthCT0},
		{"x-auth-token-json", `{"auth_token":"` + synthAuth + `"}`, synthAuth},
		{"x-auth-token-escaped-json", `{"err":"\"auth_token\":\"` + synthAuth + `\""}`, synthAuth},
		{"x-ct0-raw", `{"cookie":"ct0=` + synthCT0 + `"}`, synthCT0},
		{"x-ct0-long-hex", `{"cookie":"ct0=` + synthCT0Long + `"}`, synthCT0Long},
		{"x-ct0-encoded", `{"q":"ct0%3d` + synthCT0 + `"}`, synthCT0},
		{"x-ct0-json", `{"ct0":"` + synthCT0 + `"}`, synthCT0},
		{"x-kdt-raw", `{"cookie":"kdt=` + synthKDT + `"}`, synthKDT},
		{"x-kdt-encoded", `{"q":"kdt%3D` + synthKDT + `"}`, synthKDT},
		{"x-kdt-json", `{"kdt":"` + synthKDT + `"}`, synthKDT},
		{"x-csrf-header", `{"header":"x-csrf-token: ` + synthCT0 + `"}`, synthCT0},
		{"x-csrf-header-upper-quoted", `{"header":"X-CSRF-Token: \"` + synthCT0 + `\""}`, synthCT0},
		{"x-csrf-header-go-map", `{"header":"map[X-Csrf-Token:[` + synthCT0 + `]]"}`, synthCT0},
		{"x-csrf-kv", `{"header":"x-csrf-token=` + synthCT0 + `"}`, synthCT0},
		{"x-csrf-kv-encoded", `{"header":"x-csrf-token%3D` + synthCT0 + `"}`, synthCT0},
		{"x-csrf-header-query-escaped", `{"header":"` + url.QueryEscape("x-csrf-token: "+synthCT0) + `"}`, synthCT0},
		{"x-csrf-header-path-escaped", `{"header":"` + url.PathEscape("x-csrf-token: "+synthCT0) + `"}`, synthCT0},
		{"x-csrf-json", `{"x-csrf-token":"` + synthCT0 + `"}`, synthCT0},
		{"x-bearer-header", `{"header":"Authorization: Bearer ` + synthWebBearer + `"}`, synthWebBearer},
		{"x-bearer-header-quoted", `{"header":"Authorization: 'Bearer synth_bearer_header_001'"}`, "synth_bearer_header_001"},
		{"x-bearer-header-json-escaped-quote", `{"header":"Authorization: \"Bearer synth_bearer_esc_001\""}`, "synth_bearer_esc_001"},
		{"x-bearer-header-go-map", `{"header":"map[Authorization:[Bearer synth_bearer_map_002]]"}`, "synth_bearer_map_002"},
		{"x-bearer-json", `{"Authorization":"Bearer AAAAAAAAAAAAAAAAAAAAASynthJsonBearer0002%2F%3D"}`, "AAAAAAAAAAAAAAAAAAAAASynthJsonBearer0002%2F%3D"},
		{"x-bearer-query-escaped", `{"header":"` + url.QueryEscape("Authorization: Bearer SYNTH_BEARER_ENC_507") + `"}`, "SYNTH_BEARER_ENC_507"},
		{"x-bearer-path-escaped", `{"header":"` + url.PathEscape("Authorization: Bearer SYNTH_BEARER_PATH_508") + `"}`, "SYNTH_BEARER_PATH_508"},
		{"x-bearer-bare", `{"note":"Bearer AAAAAAAAAAAAAAAAAAAAASynthBareBearer0003"}`, "AAAAAAAAAAAAAAAAAAAAASynthBareBearer0003"},
		{"x-bearer-json-key", `{"bearerToken":"AAAAAAAAAAAAAAAAAAAAASynthKeyBearer0004"}`, "AAAAAAAAAAAAAAAAAAAAASynthKeyBearer0004"},
		{"x-bearer-raw-web-literal", `{"const":"AAAAAAAAAAAAAAAAAAAAASynthRawWebBearer0005%3D%3D"}`, "AAAAAAAAAAAAAAAAAAAAASynthRawWebBearer0005%3D%3D"},
		{"x-signed-hls-token", `{"media":"https://video.twimg.com/ext_tw_video/1000000000000000009/pu/pl/synth.m3u8?tag=12&token=synth_hls_sig_01234567"}`, "synth_hls_sig_01234567"},
		{"x-signed-hls-json-escaped", `{"media":"https://video.twimg.com/ext_tw_video/1000000000000000009/pu/pl/synth.m3u8?tag=12\u0026token=synth_hls_json_01234567"}`, "synth_hls_json_01234567"},
		{"x-signed-cloudfront-mp4", `{"media":"https://video.twimg.com/amplify_video/1000000000000000009/vid/synth.mp4?Expires=1763700000&Signature=AbCdEfGh12345678&Key-Pair-Id=APKASYNTH0001"}`, "AbCdEfGh12345678"},
		{"x-signed-hdnts-mp4", `{"media":"https://video-s.twimg.com/ext_tw_video/1000000000000000009/pu/vid/synth.mp4?hdnts=exp=1763700000~acl=/ext_tw_video/*~hmac=0123456789abcdef0123456789abcdef"}`, "0123456789abcdef0123456789abcdef"},
		{"x-signed-encoded-url", `{"media":"https%3A%2F%2Fvideo.twimg.com%2Fext_tw_video%2F1000000000000000009%2Fpu%2Fpl%2Fsynth.m3u8%3Ftag%3D12%26token%3Dsynth_enc_sig_01234567"}`, "synth_enc_sig_01234567"},
		{"x-signed-pscp-live", `{"media":"https://prod-fastly-us.video.pscp.tv/Transcoding/v1/hls/synth/playlist.m3u8?token=synth_pscp_sig_01234567"}`, "synth_pscp_sig_01234567"},
		{"x-private-ton-x", `{"media":"https://ton.x.com/i/ton/data/dm/1000000000000000009/synth_private_dm.jpg"}`, "synth_private_dm.jpg"},
		{"x-private-ton-twitter", `{"media":"https://ton.twitter.com/i/ton/data/dm/1000000000000000009/synth_private_dm2.jpg"}`, "synth_private_dm2.jpg"},
	}

	for _, tc := range badCases {
		t.Run(tc.name, func(t *testing.T) {
			badDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(badDir, tc.name+".json"), []byte(tc.payload), 0o600); err != nil {
				t.Fatalf("write bad receipt %s: %v", tc.name, err)
			}
			findings, err := ScanReceiptsForSecrets(badDir)
			if err != nil {
				t.Fatalf("ScanReceiptsForSecrets(%s): %v", tc.name, err)
			}
			if len(findings) == 0 {
				t.Fatalf("expected secret finding for %s, got none", tc.name)
			}
			for _, f := range findings {
				if strings.Contains(f, tc.rawSecret) {
					t.Fatalf("ScanReceiptsForSecrets(%s) echoed raw secret %q in finding: %s", tc.name, tc.rawSecret, f)
				}
			}
		})
	}

	t.Run("combined_x_cookies_report_all_keys", func(t *testing.T) {
		payload := `{"cookie":"auth_token=` + synthAuth + `; ct0=` + synthCT0 + `; kdt=` + synthKDT + `"}`
		joined := strings.Join(ScanTextForSecrets(payload), "\n")
		for _, want := range []string{`"auth_token="`, `"ct0="`, `"kdt="`} {
			if !strings.Contains(joined, want) {
				t.Fatalf("expected %s in findings, got:\n%s", want, joined)
			}
		}
		for _, sec := range []string{synthAuth, synthCT0, synthKDT} {
			if strings.Contains(joined, sec) {
				t.Fatalf("raw secret %q echoed in findings:\n%s", sec, joined)
			}
		}
	})

	t.Run("json_escaped_multiline_headers_caught", func(t *testing.T) {
		payload := `{"raw":"GET /i/api/1.1/account/settings.json HTTP/1.1\r\nx-csrf-token: ` + synthCT0 + `\r\nAuthorization: Bearer synth_hdr_001\r\nCookie: auth_token=` + synthAuth + `"}`
		joined := strings.Join(ScanTextForSecrets(payload), "\n")
		for _, want := range []string{`"auth_token="`, `"x-csrf-token"`, `"Authorization: Bearer"`} {
			if !strings.Contains(joined, want) {
				t.Fatalf("expected %s in findings, got:\n%s", want, joined)
			}
		}
		for _, sec := range []string{synthAuth, synthCT0, "synth_hdr_001"} {
			if strings.Contains(joined, sec) {
				t.Fatalf("raw secret %q echoed in findings:\n%s", sec, joined)
			}
		}
	})

	safeCases := []struct {
		name    string
		payload string
	}{
		{"placeholders-angle-zh", `{"note":"auth_token=<合成占位>; ct0=<合成占位>; kdt=<合成占位>; twid=<合成占位>; guest_id=<合成占位>"}`},
		{"placeholders-angle-en", `{"note":"auth_token=<synthetic>; ct0=<synthetic>; kdt=<synthetic>; x-csrf-token: <ct0>"}`},
		{"placeholders-json-escaped", `{"note":"auth_token=\u003c合成占位\u003e; ct0=\u003c合成占位\u003e; kdt=\u003c合成占位\u003e","x-csrf-token":"\u003cct0\u003e","Authorization":"Bearer \u003c公开网页端固定 Bearer 令牌\u003e"}`},
		{"placeholders-url-encoded", `{"note":"auth_token%3D%3C%E5%90%88%E6%88%90%E5%8D%A0%E4%BD%8D%3E%26ct0%3D%3Csynthetic%3E%26kdt%3D%5BREDACTED%5D"}`},
		{"placeholders-bearer-zh", `{"header":"Authorization: Bearer <公开网页端固定 Bearer 令牌>","short":"Bearer <公开网页端固定 Bearer 令牌>"}`},
		{"placeholders-bearer-redacted", `{"header":"Authorization: Bearer [REDACTED]","alt":"Authorization: Bearer <令牌>"}`},
		{"placeholders-csrf-prose", `{"header":"x-csrf-token: [REDACTED]","msg":"x-csrf-token: required","err":"x-csrf-token: mismatch"}`},
		{"bare-keys-and-prose", `{"note":"auth_token= ct0= kdt= auth_token=... ct0=[REDACTED] App-Only Bearer Token Bearer authentication-mechanism"}`},
		{"public-pbs-image-url", `{"image":"https://pbs.twimg.com/media/Synthetic01?format=jpg&name=orig","avatar":"https://pbs.twimg.com/profile_images/1000000000000000001/synth_400x400.jpg"}`},
		{"public-video-and-gif-url", `{"gif":"https://video.twimg.com/tweet_video/SyntheticGif01.mp4","video":"https://video.twimg.com/ext_tw_video/1000000000000000009/pu/vid/avc1/1280x720/synth.mp4?tag=12"}`},
		{"public-abs-script-url", `{"script":"https://abs.twimg.com/responsive-web/client-web/ondemand.s.synth1a.js"}`},
		{"signed-url-placeholders", `{"hls":"https://video.twimg.com/ext_tw_video/1000000000000000009/pu/pl/synth.m3u8?tag=12&token=<合成签名>","syn":"https://cdn.syndication.twimg.com/tweet-result?id=<id>&token=<token>","ton":"https://ton.x.com/i/ton/data/<合成路径>"}`},
		{"clean-act024-receipt", `{"act":"act024","passed":true,"apiUsage":{"actId":"act024","runs":1,"apiCalls":12,"categories":{"login":2,"frontend":3,"detail":3}},"facts":{"hasCoreUser":true,"hasNoteTweet":true,"txidRequired":true,"xpffRequired":false,"range206":true,"hasHLSOnly":false,"mp4ffValid":true}}`},
	}
	for _, tc := range safeCases {
		t.Run("safe_"+tc.name, func(t *testing.T) {
			if findings := ScanTextForSecrets(tc.payload); len(findings) != 0 {
				t.Fatalf("false positive on %s: %v", tc.name, findings)
			}
		})
	}

	t.Run("safe_actual_x_md", func(t *testing.T) {
		xDocPath := filepath.Join("..", "..", "docs", "research", "x.md")
		raw, err := os.ReadFile(xDocPath)
		if err != nil {
			t.Fatalf("read %s: %v", xDocPath, err)
		}
		if findings := ScanTextForSecrets(string(raw)); len(findings) != 0 {
			t.Fatalf("false positive on docs/research/x.md: %v", findings)
		}
	})
}
