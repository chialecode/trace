package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/sitecheck"
)

func setupAct026Root(t *testing.T, rawCookie string) string {
	t.Helper()
	root := t.TempDir()
	secDir := filepath.Join(root, ".local", "site-secrets")
	if err := os.MkdirAll(secDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cookieFile := filepath.Join(secDir, "douyin-cookie.txt")
	if err := os.WriteFile(cookieFile, []byte(rawCookie), 0o600); err != nil {
		t.Fatal(err)
	}
	envLine := "TRACE_TEST_DOUYIN_COOKIE_FILE=.local/site-secrets/douyin-cookie.txt\n"
	if err := os.WriteFile(filepath.Join(root, ".env.local"), []byte(envLine), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func synthAct026WorkNode(id, title string) string {
	return fmt.Sprintf(`{
		"aweme_id": %q,
		"aweme_type": 0,
		"desc": %q,
		"create_time": 1763000000,
		"author": {
			"uid": "880000000011",
			"sec_uid": "MS4wLjABAAAA_SYNTH_AUTHOR_ACT026_01",
			"nickname": "合成作者敏感昵称",
			"unique_id": "synth_author_26",
			"avatar_thumb": {"url_list": ["https://p3-pc.douyinpic.com/aweme/100x100/avatar.jpeg"]}
		},
		"video": {
			"duration": 12000,
			"width": 1080,
			"height": 1920,
			"cover": {"url_list": ["https://p3-pc.douyinpic.com/obj/cover.jpeg"]},
			"play_addr": {
				"uri": "v0200fg10000synth26",
				"data_size": 1048576,
				"url_list": ["https://v26-web.douyinvod.com/video/tos/cn/synth.mp4"]
			},
			"bit_rate": [{
				"gear_name": "normal_1080_0",
				"quality_type": 10,
				"bit_rate": 2000000,
				"FPS": 30,
				"is_bytevc1": 0,
				"play_addr": {
					"uri": "v0200fg10000synth26",
					"width": 1080,
					"height": 1920,
					"data_size": 1048576,
					"url_list": ["https://v26-web.douyinvod.com/video/tos/cn/synth_1080.mp4"]
				}
			}]
		}
	}`, id, title)
}

func TestAct026LimitsAndCategoryAllowlist(t *testing.T) {
	sum := 0
	for _, n := range act026Limits {
		sum += n
	}
	if sum != act026MaxAPI || act026MaxAPI != 9 || act026MaxRuns != 1 {
		t.Fatalf("unexpected act026 quota constants: sum=%d maxAPI=%d maxRuns=%d", sum, act026MaxAPI, act026MaxRuns)
	}

	gate, err := sitecheck.OpenGate(t.TempDir(), sitecheck.Limits{
		ActID: act026ID, MaxRuns: act026MaxRuns, MaxAPICalls: act026MaxAPI, MaxByCategory: act026Limits,
	})
	if err != nil {
		t.Fatal(err)
	}
	var upstream atomic.Int64
	ck := &act026Checker{
		gate:      gate,
		apiLimits: act026Limits,
		statuses:  map[string]int{},
		lastTel:   map[string]act026RespTelemetry{},
	}
	rt := ck.wrap(roundTrip(func(r *http.Request) (*http.Response, error) {
		upstream.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: http.NoBody}, nil
	}))

	// Disallowed endpoints (including media CDN, aweme/post, aweme/detail, following) are blocked BEFORE ledger & network.
	for _, rawURL := range []string{
		"https://p3-pc.douyinpic.com/obj/cover.jpeg",
		"https://v26-web.douyinvod.com/video/tos/cn/synth.mp4",
		"https://www.douyin.com/aweme/v1/web/aweme/post/",
		"https://www.douyin.com/aweme/v1/web/aweme/detail/",
		"https://www.douyin.com/aweme/v1/web/user/following/list/",
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

func TestAct026HappyPathZeroByteIsolationStopAndPanicReceipt(t *testing.T) {
	const rawCookie = "sessionid=synth_sess_secret_2601; sid_ucp_v1=1.0.0-synth_ucp_secret_2602; s_v_web_id=verify_synth_fp_2603; UIFID=synth_uifid_secret_2604; d_ticket=synth_dticket_2605; custom_ext=ext4; is_staff_user=false; bd_ticket_guard_client_data=poison_2606"

	t.Run("full_happy_path_and_single_run_cap", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			switch r.URL.Path {
			case "/aweme/v1/web/user/profile/self/":
				// Verify extended cookies were preserved and poison cookies stripped.
				ck := r.Header.Get("Cookie")
				if !strings.Contains(ck, "sid_ucp_v1=1.0.0-synth_ucp_secret_2602") || strings.Contains(ck, "bd_ticket_guard") {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				_, _ = w.Write([]byte(`{"status_code":0,"user":{"uid":"770000000099","sec_uid":"MS4wLjABAAAA_SYNTH_SELF_ACT026_USER","unique_id":"self_handle_26","nickname":"测试本人敏感昵称"}}`))
			case "/aweme/v1/web/aweme/favorite/":
				if r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != "same-origin" || r.URL.Query().Get("version_code") != "170400" {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if r.URL.Query().Get("max_cursor") == "0" {
					_, _ = w.Write([]byte(`{"status_code":0,"aweme_list":[` + synthAct026WorkNode("7300000000000000101", "喜欢作品敏感标题一") + `],"has_more":1,"max_cursor":1700}`))
				} else {
					_, _ = w.Write([]byte(`{"status_code":0,"aweme_list":[` + synthAct026WorkNode("7300000000000000102", "喜欢作品敏感标题二") + `],"has_more":0,"max_cursor":0}`))
				}
			case "/aweme/v1/web/collects/list/":
				_, _ = w.Write([]byte(`{"status_code":0,"collects_list":[{"collects_id_str":"7500000000000000888","collects_name":"私密收藏夹敏感名","total_number":2}],"has_more":0,"cursor":0}`))
			case "/aweme/v1/web/collects/video/list/":
				_, _ = w.Write([]byte(`{"status_code":0,"aweme_list":[` + synthAct026WorkNode("7300000000000000201", "收藏夹作品敏感标题") + `],"has_more":0,"cursor":0}`))
			case "/aweme/v1/web/history/read/":
				_, _ = w.Write([]byte(`{"status_code":0,"aweme_list":[` + synthAct026WorkNode("7300000000000000301", "观看历史敏感标题") + `],"has_more":0,"max_cursor":0}`))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		t.Cleanup(srv.Close)

		root := setupAct026Root(t, rawCookie)
		outPath := filepath.Join(root, "artifacts", "site-check", "act026.json")
		eps := douyin.Endpoints{
			ProfileSelf: srv.URL + "/aweme/v1/web/user/profile/self/",
			AwemePost:   srv.URL + "/aweme/v1/web/aweme/post/",
			AwemeDetail: srv.URL + "/aweme/v1/web/aweme/detail/",
		}

		rc, err := runAct026(context.Background(), act026Config{
			RootDir:        root,
			OutPath:        outPath,
			MinSpacing:     -1,
			Endpoints:      eps,
			AllowLocalhost: true,
		})
		if err != nil || !rc.Passed || !rc.SecretScanClean || rc.APIUsage.APICalls != 6 {
			t.Fatalf("runAct026 happy path failed: err=%v passed=%v api=%d steps=%+v", err, rc.Passed, rc.APIUsage.APICalls, rc.Steps)
		}

		rawJSONBytes, err := os.ReadFile(outPath)
		if err != nil {
			t.Fatal(err)
		}
		var diskRC act026Receipt
		if uErr := json.Unmarshal(rawJSONBytes, &diskRC); uErr != nil || diskRC.Act != "ACT-026" || !diskRC.Passed {
			t.Fatalf("disk receipt unmarshal failed: err=%v rc=%+v", uErr, diskRC)
		}
		rawJSON := string(rawJSONBytes)
		for _, forbidden := range []string{
			"synth_sess_secret_2601", "synth_ucp_secret_2602", "verify_synth_fp_2603",
			"synth_uifid_secret_2604", "synth_dticket_2605", "ext4", "poison_2606",
			"测试本人敏感昵称", "喜欢作品敏感标题一", "私密收藏夹敏感名", "观看历史敏感标题",
			"MS4wLjABAAAA_SYNTH_SELF_ACT026_USER", "7500000000000000888",
		} {
			if strings.Contains(rawJSON, forbidden) {
				t.Fatalf("receipt leaked sensitive string %q:\n%s", forbidden, rawJSON)
			}
		}

		// Second run must be blocked by maxRuns=1 BEFORE any network call.
		if _, err2 := runAct026(context.Background(), act026Config{
			RootDir:        root,
			OutPath:        outPath,
			MinSpacing:     -1,
			Endpoints:      eps,
			AllowLocalhost: true,
		}); !errors.Is(err2, sitecheck.ErrQuotaExceeded) {
			t.Fatalf("second run err = %v, want ErrQuotaExceeded", err2)
		}
	})

	t.Run("zero_byte_favorite_records_guard_headers_without_blocking_dv4_dv5", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/aweme/v1/web/user/profile/self/":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"status_code":0,"user":{"uid":"770000000099","sec_uid":"MS4wLjABAAAA_SYNTH_SELF_ACT026_USER","nickname":"测试昵称"}}`))
			case "/aweme/v1/web/aweme/favorite/":
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.Header().Set("bd-ticket-guard-result", "1")
				w.WriteHeader(http.StatusOK)
			case "/aweme/v1/web/collects/list/":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"status_code":0,"collects_list":[],"has_more":0,"cursor":0}`))
			case "/aweme/v1/web/history/read/":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"status_code":0}`))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		t.Cleanup(srv.Close)

		root := setupAct026Root(t, rawCookie)
		outPath := filepath.Join(root, "artifacts", "site-check", "act026.json")
		rc, err := runAct026(context.Background(), act026Config{
			RootDir:    root,
			OutPath:    outPath,
			MinSpacing: -1,
			Endpoints: douyin.Endpoints{
				ProfileSelf: srv.URL + "/aweme/v1/web/user/profile/self/",
				AwemePost:   srv.URL + "/aweme/v1/web/aweme/post/",
				AwemeDetail: srv.URL + "/aweme/v1/web/aweme/detail/",
			},
			AllowLocalhost: true,
		})
		if err == nil || rc.Passed {
			t.Fatalf("expected non-passing receipt when D-V2_likes returns 0B unavailable, got err=%v passed=%v", err, rc.Passed)
		}
		if rc.StoppedBy != "" || rc.Verdicts["D-V4_collect_folders"] != "pass" || rc.APIUsage.APICalls != 4 {
			t.Fatalf("0-byte favorite should not halt D-V4/D-V5: stoppedBy=%q verdicts=%v api=%d", rc.StoppedBy, rc.Verdicts, rc.APIUsage.APICalls)
		}
		var likesFacts map[string]any
		for _, s := range rc.Steps {
			if s.Name == "D-V2_likes" {
				likesFacts = s.Facts
			}
		}
		if likesFacts["emptyBody"] != true || likesFacts["hasBDTicketGuardResult"] != true || likesFacts["hasBDTuringVerify"] != false || likesFacts["contentType"] != "text/plain" {
			t.Fatalf("unexpected D-V2_likes 0-byte facts: %+v", likesFacts)
		}
		if _, statErr := os.Stat(outPath); statErr != nil {
			t.Fatalf("clean receipt not written on 0-byte favorite: %v", statErr)
		}
	})

	t.Run("auth_expired_and_panic_still_write_desensitized_receipt", func(t *testing.T) {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status_code":2483,"status_msg":"未登录"}`))
		}))
		t.Cleanup(srv.Close)

		root := setupAct026Root(t, rawCookie)
		outPath := filepath.Join(root, "artifacts", "site-check", "act026.json")
		rc, err := runAct026(context.Background(), act026Config{
			RootDir:    root,
			OutPath:    outPath,
			MinSpacing: -1,
			Endpoints: douyin.Endpoints{
				ProfileSelf: srv.URL + "/aweme/v1/web/user/profile/self/",
				AwemePost:   srv.URL + "/aweme/v1/web/aweme/post/",
			},
			AllowLocalhost: true,
		})
		if err == nil || calls.Load() != 1 || rc.StoppedBy != "auth_required" {
			t.Fatalf("expected immediate stop after 1 login call, got err=%v calls=%d stoppedBy=%q", err, calls.Load(), rc.StoppedBy)
		}
		if _, statErr := os.Stat(outPath); statErr != nil {
			t.Fatalf("receipt missing after auth_required stop: %v", statErr)
		}

		// Panic recovery test: panic carrying raw Cookie secret in StepHook still writes scrubbed receipt!
		rootPanic := setupAct026Root(t, rawCookie)
		outPanic := filepath.Join(rootPanic, "artifacts", "site-check", "act026-panic.json")
		rcPanic, errPanic := runAct026(context.Background(), act026Config{
			RootDir:    rootPanic,
			OutPath:    outPanic,
			MinSpacing: -1,
			StepHook: func(stepName string) {
				if stepName == "step0_cookie_presence" {
					panic("unexpected crash with secret ext4 and synth_ucp_secret_2602")
				}
			},
		})
		if errPanic == nil || !rcPanic.SecretScanClean || rcPanic.StoppedBy != "panic" {
			t.Fatalf("expected recovered panic with clean receipt, got err=%v rc=%+v", errPanic, rcPanic)
		}
		rawPanicBytes, rErr := os.ReadFile(outPanic)
		if rErr != nil {
			t.Fatalf("panic receipt not written: %v", rErr)
		}
		if strings.Contains(string(rawPanicBytes), "ext4") || strings.Contains(string(rawPanicBytes), "synth_ucp_secret_2602") {
			t.Fatalf("panic receipt leaked secret: %s", string(rawPanicBytes))
		}

		// Secret scanner rejects leaked 4-char cookie value.
		if vErr := verifyAct026CookieValuesAbsent(`{"leak":"ext4"}`, rawCookie); vErr == nil {
			t.Fatal("verifyAct026CookieValuesAbsent failed to catch 4-char cookie value leak")
		}
	})
}
