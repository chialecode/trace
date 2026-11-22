package sitecheck

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
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
