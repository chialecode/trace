package main

import (
	"context"
	"encoding/base64"
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

func TestAct027LimitsAndCategoryAllowlist(t *testing.T) {
	sum := 0
	for _, n := range act027Limits {
		sum += n
	}
	if sum != act027MaxAPI || act027MaxAPI != 6 || act027MaxRuns != 1 {
		t.Fatalf("unexpected act027 quota constants: sum=%d maxAPI=%d maxRuns=%d", sum, act027MaxAPI, act027MaxRuns)
	}

	gate, err := sitecheck.OpenGate(t.TempDir(), sitecheck.Limits{
		ActID: act027ID, MaxRuns: act027MaxRuns, MaxAPICalls: act027MaxAPI, MaxByCategory: act027Limits,
	})
	if err != nil {
		t.Fatal(err)
	}
	var upstream atomic.Int64
	ck := &act027Checker{
		gate:      gate,
		apiLimits: act027Limits,
		statuses:  map[string]int{},
		lastTel:   map[string]act027RespTelemetry{},
	}
	rt := ck.wrap(roundTrip(func(r *http.Request) (*http.Response, error) {
		upstream.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: http.NoBody}, nil
	}))

	// Disallowed endpoints (including media CDN, aweme/post, collects/list, history/read) are blocked BEFORE ledger & network.
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

func TestAct027HandshakeSignedLikesSecretScanAndPanicReceipt(t *testing.T) {
	const rawCookie = "sessionid=synth_sess_secret_2701; sid_ucp_v1=1.0.0-synth_ucp_secret_2702; UIFID=synth_uifid_secret_2704; d_ticket=synth_dticket_2705; bd_ticket_guard_client_data=poison_2706"
	const issuedTicket = "hash.SYNTH_ACT027_ISSUED_TICKET"
	const issuedTsSign = "ts.2.SYNTH_ACT027_ISSUED_TSSIGN"
	serverDataB64 := base64.StdEncoding.EncodeToString([]byte(`{"ticket":"` + issuedTicket + `","ts_sign":"` + issuedTsSign + `"}`))

	t.Run("handshake_and_signed_favorites_two_pages_zero_secret_leak", func(t *testing.T) {
		var seenPub string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			switch r.URL.Path {
			case "/aweme/v1/web/user/profile/self/":
				if r.Header.Get(guard.HeaderREEPublicKey) != "" || r.Header.Get(guard.HeaderVersion) != "" || strings.Contains(r.Header.Get("Cookie"), "bd_ticket_guard") {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				w.Header().Set("bd-ticket-guard-result", "0")
				w.Header().Set("bd-ticket-guard-server-data", serverDataB64)
				_, _ = w.Write([]byte(`{"status_code":0,"user":{"uid":"770000000027","sec_uid":"MS4wLjABAAAA_SYNTH_SELF_ACT027_USER","unique_id":"self_handle_27","nickname":"测试本人敏感昵称27"}}`))
			case "/aweme/v1/web/aweme/favorite/":
				pub := r.Header.Get(guard.HeaderREEPublicKey)
				if pub == "" || (seenPub != "" && pub != seenPub) || r.Header.Get(guard.HeaderClientData) == "" || r.Header.Get(guard.HeaderWebSignType) != "1" {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				seenPub = pub
				w.Header().Set("bd-ticket-guard-result", "0")
				if r.URL.Query().Get("max_cursor") == "0" {
					_, _ = w.Write([]byte(`{"status_code":0,"aweme_list":[` + synthAct026WorkNode("7300000000000000701", "喜欢作品敏感标题27A") + `],"has_more":1,"max_cursor":1700}`))
				} else {
					_, _ = w.Write([]byte(`{"status_code":0,"aweme_list":[` + synthAct026WorkNode("7300000000000000702", "喜欢作品敏感标题27B") + `],"has_more":0,"max_cursor":0}`))
				}
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		t.Cleanup(srv.Close)

		root := setupAct026Root(t, rawCookie)
		outPath := filepath.Join(root, "artifacts", "site-check", "act027.json")
		eps := douyin.Endpoints{
			ProfileSelf: srv.URL + "/aweme/v1/web/user/profile/self/",
			AwemePost:   srv.URL + "/aweme/v1/web/aweme/post/",
		}

		rc, err := runAct027(context.Background(), act027Config{
			RootDir:        root,
			OutPath:        outPath,
			MinSpacing:     -1,
			Endpoints:      eps,
			AllowLocalhost: true,
		})
		if err != nil || !rc.Passed || !rc.SecretScanClean || rc.APIUsage.APICalls != 3 {
			t.Fatalf("runAct027 happy path failed: err=%v passed=%v api=%d steps=%+v", err, rc.Passed, rc.APIUsage.APICalls, rc.Steps)
		}

		rawJSONBytes, err := os.ReadFile(outPath)
		if err != nil {
			t.Fatal(err)
		}
		rawJSON := string(rawJSONBytes)
		for _, forbidden := range []string{
			"synth_sess_secret_2701", "synth_ucp_secret_2702", "synth_uifid_secret_2704",
			"synth_dticket_2705", "poison_2706", issuedTicket, issuedTsSign, seenPub,
			"测试本人敏感昵称27", "喜欢作品敏感标题27A", "喜欢作品敏感标题27B",
			"MS4wLjABAAAA_SYNTH_SELF_ACT027_USER",
		} {
			if strings.Contains(rawJSON, forbidden) {
				t.Fatalf("act027 receipt leaked sensitive value %q:\n%s", forbidden, rawJSON)
			}
		}

		// Verify single-run ledger cap blocks any second run before hitting network.
		if _, err2 := runAct027(context.Background(), act027Config{
			RootDir:        root,
			OutPath:        outPath,
			MinSpacing:     -1,
			Endpoints:      eps,
			AllowLocalhost: true,
		}); !errors.Is(err2, sitecheck.ErrQuotaExceeded) {
			t.Fatalf("second run err = %v, want ErrQuotaExceeded", err2)
		}
	})

	t.Run("zero_byte_favorite_and_panic_recovery_write_clean_receipt", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/aweme/v1/web/user/profile/self/":
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("bd-ticket-guard-result", "1101")
				_, _ = w.Write([]byte(`{"status_code":0,"user":{"uid":"770000000027","sec_uid":"MS4wLjABAAAA_SYNTH_SELF_ACT027_USER","nickname":"测试27"}}`))
			case "/aweme/v1/web/aweme/favorite/":
				w.Header().Set("Content-Type", "text/plain")
				w.Header().Set("bd-ticket-guard-result", "-1")
				w.WriteHeader(http.StatusOK)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		t.Cleanup(srv.Close)

		root := setupAct026Root(t, rawCookie)
		outPath := filepath.Join(root, "artifacts", "site-check", "act027-0b.json")
		rc, err := runAct027(context.Background(), act027Config{
			RootDir:    root,
			OutPath:    outPath,
			MinSpacing: -1,
			Endpoints: douyin.Endpoints{
				ProfileSelf: srv.URL + "/aweme/v1/web/user/profile/self/",
				AwemePost:   srv.URL + "/aweme/v1/web/aweme/post/",
			},
			AllowLocalhost: true,
		})
		if err == nil || rc.Passed || !rc.SecretScanClean || rc.APIUsage.APICalls != 2 {
			t.Fatalf("unexpected 0B favorite receipt: err=%v passed=%v clean=%v api=%d", err, rc.Passed, rc.SecretScanClean, rc.APIUsage.APICalls)
		}
		var likesFacts map[string]any
		for _, s := range rc.Steps {
			if s.Name == "D-V2_likes" {
				likesFacts = s.Facts
			}
		}
		if likesFacts["bdTicketGuardResultCode"] != -1 || likesFacts["sentGuardHandshake"] != false || likesFacts["sentGuardSigned"] != false {
			t.Fatalf("unexpected D-V2_likes guard facts: %+v", likesFacts)
		}

		// Panic recovery test: panic carrying guard secret writes scrubbed receipt.
		rootPanic := setupAct026Root(t, rawCookie)
		outPanic := filepath.Join(rootPanic, "artifacts", "site-check", "act027-panic.json")
		rcPanic, errPanic := runAct027(context.Background(), act027Config{
			RootDir:    rootPanic,
			OutPath:    outPanic,
			MinSpacing: -1,
			StepHook: func(stepName string) {
				if stepName == "step0_cookie_presence" {
					panic("boom with synth_ucp_secret_2702")
				}
			},
		})
		if errPanic == nil || !rcPanic.SecretScanClean || rcPanic.StoppedBy != "panic" {
			t.Fatalf("expected clean panic receipt, got err=%v rc=%+v", errPanic, rcPanic)
		}
		if verifyAct027GuardSecretsAbsent(`{"leak":"`+issuedTsSign+`"}`, []string{issuedTsSign}) == nil {
			t.Fatal("verifyAct027GuardSecretsAbsent failed to catch leaked guard secret")
		}
	})
}
