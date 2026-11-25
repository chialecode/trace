package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/site/douyin/guard"
	"github.com/chialecode/trace/internal/sitecheck"
)

func TestAct029LimitsAndCategoryAllowlist(t *testing.T) {
	sum := 0
	for _, n := range act029Limits {
		sum += n
	}
	if sum != act029MaxAPI || act029MaxAPI != 5 || act029MaxRuns != 1 {
		t.Fatalf("unexpected act029 quota constants: sum=%d maxAPI=%d maxRuns=%d", sum, act029MaxAPI, act029MaxRuns)
	}
	if act029Limits["dy_profile_self"] != 1 || act029Limits["dy_favorite"] != 3 || act029Limits["dy_spare"] != 1 {
		t.Fatalf("unexpected act029 category limits: %+v", act029Limits)
	}

	gate, err := sitecheck.OpenGate(t.TempDir(), sitecheck.Limits{
		ActID: act029ID, MaxRuns: act029MaxRuns, MaxAPICalls: act029MaxAPI, MaxByCategory: act029Limits,
	})
	if err != nil {
		t.Fatal(err)
	}
	var upstream atomic.Int64
	ck := &act029Checker{
		gate:      gate,
		apiLimits: act029Limits,
		statuses:  map[string]int{},
		lastTel:   map[string]act029RespTelemetry{},
	}
	rt := ck.wrap(roundTrip(func(r *http.Request) (*http.Response, error) {
		upstream.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: http.NoBody}, nil
	}))

	// Disallowed endpoints (media CDN, aweme/post, collects/list, history/read) are blocked BEFORE ledger & network.
	for _, rawURL := range []string{
		"https://p3-pc.douyinpic.com/obj/cover.jpeg",
		"https://v26-web.douyinvod.com/video/tos/cn/synth.mp4",
		"https://www.douyin.com/aweme/v1/web/aweme/post/",
		"https://www.douyin.com/aweme/v1/web/collects/list/",
		"https://www.douyin.com/aweme/v1/web/history/read/",
	} {
		req, _ := http.NewRequest(http.MethodGet, rawURL, nil)
		if _, err := rt.RoundTrip(req); !errors.Is(err, sitecheck.ErrQuotaExceeded) {
			t.Fatalf("%s: expected ErrQuotaExceeded, got %v", rawURL, err)
		}
	}
	if upstream.Load() != 0 || gate.Usage().APICalls != 0 {
		t.Fatalf("disallowed requests hit upstream (%d) or ledger (%d)", upstream.Load(), gate.Usage().APICalls)
	}
}

func TestAct029ProdChainDPAPIReloadLikesSecretScanAndPanicReceipt(t *testing.T) {
	const rawCookie = "sessionid=synth_sess_secret_2901; sid_ucp_v1=1.0.0-synth_ucp_secret_2902; s_v_web_id=verify_synth_2903; UIFID=synth_uifid_secret_2904; d_ticket=synth_dticket_2905; bd_ticket_guard_client_data=poison_2906"

	t.Run("prod_login_dpapi_reload_and_two_pages_zero_secret_leak", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			switch r.URL.Path {
			case "/aweme/v1/web/user/profile/self/":
				if r.Header.Get(guard.HeaderREEPublicKey) != "" || strings.Contains(r.Header.Get("Cookie"), "bd_ticket_guard") {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				w.Header().Set("bd-ticket-guard-result", "1101")
				_, _ = w.Write([]byte(`{"status_code":0,"user":{"uid":"770000000029","sec_uid":"MS4wLjABAAAA_SYNTH_SELF_ACT029_USER","unique_id":"self_handle_29","nickname":"测试本人敏感昵称29"}}`))
			case "/aweme/v1/web/aweme/favorite/":
				if !strings.Contains(r.Header.Get("Cookie"), "s_v_web_id=verify_synth_2903") ||
					!strings.Contains(r.Header.Get("Cookie"), "sid_ucp_v1=1.0.0-synth_ucp_secret_2902") ||
					r.Header.Get("x-secsdk-web-signature") == "" ||
					r.URL.Query().Get("verifyFp") != "verify_synth_2903" {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				w.Header().Set("bd-ticket-guard-result", "1101")
				if r.URL.Query().Get("max_cursor") == "0" {
					_, _ = w.Write([]byte(`{"status_code":0,"aweme_list":[` + synthAct026WorkNode("7300000000000000901", "喜欢作品敏感标题29A") + `],"has_more":1,"max_cursor":1900}`))
				} else {
					_, _ = w.Write([]byte(`{"status_code":0,"aweme_list":[` + synthAct026WorkNode("7300000000000000902", "喜欢作品敏感标题29B") + `],"has_more":0,"max_cursor":0}`))
				}
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		t.Cleanup(srv.Close)

		root := setupAct026Root(t, rawCookie)
		outPath := filepath.Join(root, "artifacts", "site-check", "act029.json")
		eps := douyin.Endpoints{
			ProfileSelf: srv.URL + "/aweme/v1/web/user/profile/self/",
			AwemePost:   srv.URL + "/aweme/v1/web/aweme/post/",
		}

		rc, err := runAct029(context.Background(), act029Config{
			RootDir:        root,
			OutPath:        outPath,
			MinSpacing:     -1,
			Endpoints:      eps,
			AllowLocalhost: true,
		})
		if err != nil || !rc.Passed || !rc.SecretScanClean || rc.APIUsage.APICalls != 3 {
			t.Fatalf("runAct029 happy path failed: err=%v passed=%v api=%d steps=%+v", err, rc.Passed, rc.APIUsage.APICalls, rc.Steps)
		}

		rawJSONBytes, err := os.ReadFile(outPath)
		if err != nil {
			t.Fatal(err)
		}
		rawJSON := string(rawJSONBytes)
		for _, forbidden := range []string{
			"synth_sess_secret_2901", "synth_ucp_secret_2902", "verify_synth_2903",
			"synth_uifid_secret_2904", "synth_dticket_2905", "poison_2906",
			"测试本人敏感昵称29", "喜欢作品敏感标题29A", "喜欢作品敏感标题29B",
			"MS4wLjABAAAA_SYNTH_SELF_ACT029_USER",
		} {
			if strings.Contains(rawJSON, forbidden) {
				t.Fatalf("act029 receipt leaked sensitive value %q:\n%s", forbidden, rawJSON)
			}
		}

		// Verify single-run ledger cap blocks any second run before hitting network.
		if _, err2 := runAct029(context.Background(), act029Config{
			RootDir:        root,
			OutPath:        outPath,
			MinSpacing:     -1,
			Endpoints:      eps,
			AllowLocalhost: true,
		}); !errors.Is(err2, sitecheck.ErrQuotaExceeded) {
			t.Fatalf("second run err = %v, want ErrQuotaExceeded", err2)
		}
	})

	t.Run("panic_recovery_writes_scrubbed_receipt", func(t *testing.T) {
		rootPanic := setupAct026Root(t, rawCookie)
		outPanic := filepath.Join(rootPanic, "artifacts", "site-check", "act029-panic.json")
		rcPanic, errPanic := runAct029(context.Background(), act029Config{
			RootDir:    rootPanic,
			OutPath:    outPanic,
			MinSpacing: -1,
			StepHook: func(stepName string) {
				if stepName == "step0_cookie_offline_check" {
					panic("boom with synth_ucp_secret_2902")
				}
			},
		})
		if errPanic == nil || !rcPanic.SecretScanClean || rcPanic.StoppedBy != "panic" {
			t.Fatalf("expected clean panic receipt, got err=%v rc=%+v", errPanic, rcPanic)
		}
	})
}
