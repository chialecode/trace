package sitecheck

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	}
}
