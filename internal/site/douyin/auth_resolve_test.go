package douyin

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/site"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func jsonHTTPResponse(status int, body string, headers map[string]string) *http.Response {
	hdr := make(http.Header)
	hdr.Set("Content-Type", "application/json; charset=utf-8")
	for k, v := range headers {
		hdr.Set(k, v)
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     hdr,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func openTestDouyinCredStore(t *testing.T, seedLoggedIn bool) *credential.Store {
	t.Helper()
	storePath := filepath.Join(t.TempDir(), credential.DouyinFileName)
	store, warn, err := credential.OpenDouyin(storePath)
	if err != nil {
		t.Fatalf("OpenDouyin failed: %v (warn=%q)", err, warn)
	}
	if seedLoggedIn {
		_, err := store.SaveVerifiedDouyinManual(map[string]string{
			"sessionid":  "SYNTH_ACTIVE_SESSION_SEED",
			"ttwid":      "SYNTH_ACTIVE_TTWID_SEED",
			"UIFID":      "SYNTH_ACTIVE_UIFID_SEED",
			"s_v_web_id": "verify_synth_seed_fp",
			"msToken":    "SYNTH_ACTIVE_MSTOKEN_SEED",
		}, credential.DouyinProfile{
			UID:      "900000000001",
			SecUID:   "MS4wLjABAAAA_EXISTING_SEED_USER_01",
			UniqueID: "existing_seed_01",
			Uname:    "已登录种子账号",
			Face:     "https://p3-pc.douyinpic.com/aweme/100x100/seed_face.jpeg",
		})
		if err != nil {
			t.Fatalf("SaveVerifiedDouyinManual seed failed: %v", err)
		}
	}
	return store
}

func newDeterministicEntropy(seed byte) io.Reader {
	buf := bytes.Repeat([]byte{seed, seed ^ 0x5A, seed + 13, seed ^ 0xA5}, 4096)
	return bytes.NewReader(buf)
}

// TestLoginWithCookieFailureModesAndSuccess verifies M3-K04:
//   - 3 distinct failure modes (invalid_input with 0 requests, auth_required, risk_control)
//   - existing active credentials are never overwritten or expired on candidate login failure
//   - raw secret Cookie values are never echoed in error messages
//   - strict 3-condition profile/self check (uid != "0", valid MS4wLjAB... sec_uid, non-empty nickname)
//   - browser-bound dynamic tickets stripped before sending profile/self
func TestLoginWithCookieFailureModesAndSuccess(t *testing.T) {
	ctx := context.Background()

	t.Run("mode1_invalid_input_zero_requests_no_secret_echo", func(t *testing.T) {
		store := openTestDouyinCredStore(t, true)
		var reqCount atomic.Int32

		client := NewClient(ClientOptions{
			CredStore:   store,
			MinInterval: -1,
			Entropy:     newDeterministicEntropy(0x11),
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				reqCount.Add(1)
				t.Fatalf("unexpected HTTP request for invalid Cookie input: %s", req.URL.String())
				return nil, nil
			}),
		})

		cases := []struct {
			name   string
			input  string
			secret string
		}{
			{
				name:   "guest_only_ttwid_and_mstoken",
				input:  "ttwid=SYNTH_GUEST_ONLY_TTWID_SECRET; msToken=SYNTH_GUEST_ONLY_MS_SECRET; UIFID=SYNTH_UIFID_GUEST",
				secret: "SYNTH_GUEST_ONLY_TTWID_SECRET",
			},
			{
				name:   "empty_sessionid_value",
				input:  "sessionid=; sessionid_ss=\"\"; ttwid=SYNTH_EMPTY_SESS_SECRET",
				secret: "SYNTH_EMPTY_SESS_SECRET",
			},
			{
				name:   "oversized_cookie_above_8kib",
				input:  "sessionid=SYNTH_OVERSIZED_SECRET_TOKEN; pad=" + strings.Repeat("x", credential.MaxCookieInputBytes+64),
				secret: "SYNTH_OVERSIZED_SECRET_TOKEN",
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := client.LoginWithCookie(ctx, tc.input)
				if !site.IsKind(err, site.ErrKindInvalidInput) {
					t.Fatalf("LoginWithCookie(%s) kind = %q, want %q (err=%v)", tc.name, site.KindOf(err), site.ErrKindInvalidInput, err)
				}
				if strings.Contains(err.Error(), tc.secret) {
					t.Fatalf("error echoed raw Cookie secret %q: %v", tc.secret, err)
				}
				if got := reqCount.Load(); got != 0 {
					t.Fatalf("HTTP request count = %d, want 0", got)
				}
				st := store.Status()
				if !st.LoggedIn || st.State != "logged_in" || st.SecUID != "MS4wLjABAAAA_EXISTING_SEED_USER_01" {
					t.Fatalf("existing active credential was mutated: %+v", st)
				}
			})
		}
	})

	t.Run("mode2_auth_required_strict_three_conditions_preserves_existing_cred", func(t *testing.T) {
		payloads := []struct {
			name string
			body string
		}{
			{
				name: "status_code_2483_unauthenticated",
				body: `{"status_code":2483,"status_msg":"未登录"}`,
			},
			{
				name: "status_zero_null_user",
				body: `{"status_code":0,"status_msg":"","user":null}`,
			},
			{
				name: "status_zero_uid_zero_shell",
				body: `{"status_code":0,"user":{"uid":"0","sec_uid":"MS4wLjABAAAA_SYNTH_CANDIDATE_01","nickname":"访客占位"}}`,
			},
			{
				name: "status_zero_invalid_sec_uid",
				body: `{"status_code":0,"user":{"uid":"10002222","sec_uid":"bad_sec_uid","nickname":"测试昵称"}}`,
			},
			{
				name: "status_zero_empty_nickname",
				body: `{"status_code":0,"user":{"uid":"10002222","sec_uid":"MS4wLjABAAAA_SYNTH_CANDIDATE_01","nickname":"   "}}`,
			},
		}

		for _, tc := range payloads {
			t.Run(tc.name, func(t *testing.T) {
				store := openTestDouyinCredStore(t, true)
				expiredCalled := false
				client := NewClient(ClientOptions{
					CredStore:   store,
					MinInterval: -1,
					Entropy:     newDeterministicEntropy(0x22),
					Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
						return jsonHTTPResponse(http.StatusOK, tc.body, nil), nil
					}),
				})
				client.SetAuthExpiredHandler(func(string) {
					expiredCalled = true
				})

				candSecret := "SYNTH_CANDIDATE_EXPIRED_SESSION_SECRET"
				_, err := client.LoginWithCookie(ctx, "sessionid="+candSecret+"; ttwid=SYNTH_CAND_TTWID")
				if !site.IsKind(err, site.ErrKindAuthRequired) {
					t.Fatalf("kind = %q, want %q (err=%v)", site.KindOf(err), site.ErrKindAuthRequired, err)
				}
				if strings.Contains(err.Error(), candSecret) {
					t.Fatalf("error echoed candidate session secret: %v", err)
				}
				if expiredCalled {
					t.Fatalf("candidate check failure must not trigger authExpired handler on existing store")
				}
				st := store.Status()
				if !st.LoggedIn || st.State != "logged_in" || st.SecUID != "MS4wLjABAAAA_EXISTING_SEED_USER_01" {
					t.Fatalf("existing active credential was altered on candidate failure: %+v", st)
				}
			})
		}
	})

	t.Run("mode3_risk_control_does_not_overwrite_or_echo", func(t *testing.T) {
		challenges := []struct {
			name   string
			status int
			body   string
		}{
			{
				name:   "html_verify_middle_page",
				status: http.StatusOK,
				body:   `<!DOCTYPE html><html><head><title>验证码中间页</title></head><body>verify</body></html>`,
			},
			{
				name:   "json_verify_center_decision_conf",
				status: http.StatusOK,
				body:   `{"status_code":0,"verify_center_decision_conf":"{\"code\":10000,\"subtype\":\"slide\"}"}`,
			},
			{
				name:   "http_403_argus_gateway_block",
				status: http.StatusForbidden,
				body:   `Blocked by ArgusSecurityPlugin`,
			},
		}

		for _, tc := range challenges {
			t.Run(tc.name, func(t *testing.T) {
				store := openTestDouyinCredStore(t, true)
				client := NewClient(ClientOptions{
					CredStore:   store,
					MinInterval: -1,
					Entropy:     newDeterministicEntropy(0x33),
					Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
						return jsonHTTPResponse(tc.status, tc.body, nil), nil
					}),
				})

				candSecret := "SYNTH_RISK_CANDIDATE_SESSION_SECRET"
				_, err := client.LoginWithCookie(ctx, "sessionid_ss="+candSecret+"; ttwid=SYNTH_RISK_TTWID")
				if !site.IsKind(err, site.ErrKindRiskControl) {
					t.Fatalf("kind = %q, want %q (err=%v)", site.KindOf(err), site.ErrKindRiskControl, err)
				}
				if strings.Contains(err.Error(), candSecret) {
					t.Fatalf("error echoed candidate session secret: %v", err)
				}
				st := store.Status()
				if !st.LoggedIn || st.State != "logged_in" || st.SecUID != "MS4wLjABAAAA_EXISTING_SEED_USER_01" {
					t.Fatalf("existing credential altered on risk_control: %+v", st)
				}
			})
		}
	})

	t.Run("valid_cookie_strips_dynamic_tickets_signs_and_persists_profile", func(t *testing.T) {
		store := openTestDouyinCredStore(t, false)
		var capturedCookie string
		var capturedABogus string
		var capturedMSToken string
		var capturedUA string
		var capturedCHUA string

		client := NewClient(ClientOptions{
			CredStore:   store,
			WebID:       "7399999999999999999",
			MinInterval: -1,
			Now: func() time.Time {
				return time.UnixMilli(1763000000000)
			},
			Entropy: newDeterministicEntropy(0x44),
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				capturedCookie = req.Header.Get("Cookie")
				capturedABogus = req.URL.Query().Get("a_bogus")
				capturedMSToken = req.URL.Query().Get("msToken")
				capturedUA = req.Header.Get("User-Agent")
				capturedCHUA = req.Header.Get("Sec-Ch-Ua")

				resp := jsonHTTPResponse(http.StatusOK, `{
					"status_code": 0,
					"status_msg": "",
					"user": {
						"uid": 880000000019,
						"sec_uid": "MS4wLjABAAAA_SYNTH_NEW_LOGGED_IN_USER",
						"unique_id": "",
						"short_id": "66778899",
						"nickname": "新登录合成账号",
						"avatar_larger": {
							"url_list": ["http://p3-pc.douyinpic.com/aweme/1080x1080/new_face.jpeg"]
						}
					}
				}`, map[string]string{
					"X-Ms-Token": "SYNTH_SERVER_ISSUED_MSTOKEN",
				})
				resp.Header.Add("Set-Cookie", "ttwid=SYNTH_REFRESHED_TTWID_02; Path=/; Secure; HttpOnly")
				return resp, nil
			}),
		})

		rawInput := strings.Join([]string{
			"Cookie: sessionid=SYNTH_NEW_SESSION_01",
			"sessionid_ss=SYNTH_NEW_SESSION_SS_01",
			"ttwid=SYNTH_INITIAL_TTWID_01",
			"UIFID=SYNTH_NEW_UIFID_01",
			"s_v_web_id=verify_synth_fp_01",
			"bd_ticket_guard_client_data=MUST_STRIP_GUARD_DATA",
			"_bd_ticket_crypt_cookie=MUST_STRIP_CRYPT_COOKIE",
			"__security_mc_1_s_sdk=MUST_STRIP_SECURITY_SDK",
			"x_tt_token=MUST_STRIP_X_TT_TOKEN",
			"sdk_source_info=MUST_STRIP_SDK_SOURCE",
			"fpk1=MUST_STRIP_FPK1",
			"fpk2=MUST_STRIP_FPK2",
		}, "; ")

		st, err := client.LoginWithCookie(ctx, rawInput)
		if err != nil {
			t.Fatalf("LoginWithCookie failed: %v", err)
		}
		if !st.LoggedIn || st.State != "logged_in" {
			t.Fatalf("Status = %+v, want logged_in", st)
		}
		if st.UID != "880000000019" || st.SecUID != "MS4wLjABAAAA_SYNTH_NEW_LOGGED_IN_USER" {
			t.Fatalf("unexpected identity in Status: %+v", st)
		}
		if st.UniqueID != "66778899" {
			t.Fatalf("UniqueID fallback from short_id = %q, want 66778899", st.UniqueID)
		}
		if st.Uname != "新登录合成账号" || st.Face != "https://p3-pc.douyinpic.com/aweme/1080x1080/new_face.jpeg" {
			t.Fatalf("unexpected Uname/Face in Status: %+v", st)
		}

		for _, forbidden := range []string{
			"bd_ticket_guard",
			"_bd_ticket_crypt_cookie",
			"__security_",
			"x_tt_token",
			"sdk_source_info",
			"fpk1",
			"fpk2",
		} {
			if strings.Contains(capturedCookie, forbidden) {
				t.Fatalf("outbound Cookie leaked dynamic ticket %q: %s", forbidden, capturedCookie)
			}
		}
		if !strings.Contains(capturedCookie, "sessionid=SYNTH_NEW_SESSION_01") || !strings.Contains(capturedCookie, "UIFID=SYNTH_NEW_UIFID_01") {
			t.Fatalf("outbound Cookie missing expected identity/context keys: %s", capturedCookie)
		}
		if capturedABogus == "" {
			t.Fatalf("outbound profile/self request missing a_bogus signature")
		}
		if len(capturedMSToken) != 128 {
			t.Fatalf("generated fallback msToken length = %d, want 128", len(capturedMSToken))
		}
		if capturedUA != DefaultUserAgent || !strings.Contains(capturedCHUA, `"136"`) {
			t.Fatalf("unexpected UA/Sec-Ch-Ua headers: UA=%q CHUA=%q", capturedUA, capturedCHUA)
		}
		if got := store.TTWID(); got != "SYNTH_REFRESHED_TTWID_02" {
			t.Fatalf("refreshed Set-Cookie ttwid = %q, want SYNTH_REFRESHED_TTWID_02", got)
		}
		if got := store.MSToken(); got != "SYNTH_SERVER_ISSUED_MSTOKEN" {
			t.Fatalf("refreshed X-Ms-Token = %q, want SYNTH_SERVER_ISSUED_MSTOKEN", got)
		}
	})
}

func TestVerifyCredentialExpiryPreFlightAndGuestControl(t *testing.T) {
	ctx := context.Background()
	store := openTestDouyinCredStore(t, true)

	var reqCount atomic.Int32
	var mode atomic.Int32 // 1 = risk_control, 2 = 2483 expired
	var expiredReasons []string

	client := NewClient(ClientOptions{
		CredStore:   store,
		MinInterval: -1,
		Entropy:     newDeterministicEntropy(0x55),
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			reqCount.Add(1)
			switch mode.Load() {
			case 1:
				return jsonHTTPResponse(http.StatusOK, `<html><title>验证码中间页</title></html>`, nil), nil
			case 2:
				return jsonHTTPResponse(http.StatusOK, `{"status_code":2483,"status_msg":"未登录"}`, nil), nil
			default:
				if c := req.Header.Get("Cookie"); c != "" {
					t.Fatalf("CheckProfileSelfGuest sent non-empty Cookie header: %q", c)
				}
				return jsonHTTPResponse(http.StatusOK, `{"status_code":2483,"status_msg":"未登录"}`, nil), nil
			}
		}),
	})
	client.SetAuthExpiredHandler(func(reason string) {
		expiredReasons = append(expiredReasons, reason)
	})

	// 1. Risk-control challenge during VerifyCredential MUST NOT expire stored credential.
	mode.Store(1)
	st, err := client.VerifyCredential(ctx)
	if !site.IsKind(err, site.ErrKindRiskControl) {
		t.Fatalf("VerifyCredential risk_control kind = %q, want %q", site.KindOf(err), site.ErrKindRiskControl)
	}
	if !st.LoggedIn || st.State != "logged_in" {
		t.Fatalf("stored credential expired on risk_control: %+v", st)
	}
	if len(expiredReasons) != 0 {
		t.Fatalf("authExpired handler fired on risk_control: %v", expiredReasons)
	}

	// 2. Authentic 2483 response marks credential expired and fires authExpired callback.
	mode.Store(2)
	st, err = client.VerifyCredential(ctx)
	if !site.IsKind(err, site.ErrKindAuthRequired) {
		t.Fatalf("VerifyCredential 2483 kind = %q, want %q", site.KindOf(err), site.ErrKindAuthRequired)
	}
	if st.LoggedIn || st.State != "expired" {
		t.Fatalf("stored credential state = %+v, want expired", st)
	}
	if len(expiredReasons) != 1 {
		t.Fatalf("authExpired callback count = %d, want 1", len(expiredReasons))
	}

	// 3. Subsequent normal API requests fail pre-flight with 0 outbound HTTP requests!
	before := reqCount.Load()
	if _, err := client.GetWorkDetail(ctx, "7300000000000000001", StreamSelectOptions{}); !site.IsKind(err, site.ErrKindAuthRequired) {
		t.Fatalf("pre-flight GetWorkDetail kind = %q, want auth_required", site.KindOf(err))
	}
	if after := reqCount.Load(); after != before {
		t.Fatalf("pre-flight auth_required issued %d HTTP requests, want 0", after-before)
	}

	// 4. Guest control request works without stored credentials and sends zero Cookie header.
	mode.Store(0)
	if err := client.CheckProfileSelfGuest(ctx); !site.IsKind(err, site.ErrKindAuthRequired) {
		t.Fatalf("CheckProfileSelfGuest kind = %q, want auth_required (err=%v)", site.KindOf(err), err)
	}

	// 5. Logout clears stored record.
	if err := client.LogoutCredential(); err != nil {
		t.Fatalf("LogoutCredential failed: %v", err)
	}
	if store.Status().State != "logged_out" {
		t.Fatalf("Status after logout = %+v, want logged_out", store.Status())
	}
}

// TestExtractShareURLAndResolvePriority verifies M3-K06:
//   - mixed Chinese/Emoji share text & glued /7.46 oral-code stripping
//   - <= 5 HTTPS whitelisted hops stopping immediately once target ID is extracted
//   - modal_id/vid priority BEFORE /user/ and discovery paths
//   - sec_uid preserving -, _, and __
//   - 0-request local rejection of bare 19-digit numbers & mall/collection/hashtag/search/discover/lvdetail
//   - /user/self -> logged-in sec_uid & bare MS4wLjAB... -> 0 requests
func TestExtractShareURLAndResolvePriority(t *testing.T) {
	ctx := context.Background()

	t.Run("share_text_extraction_and_glued_oral_code", func(t *testing.T) {
		cases := []struct {
			in   string
			want string
		}{
			{
				in:   "7.46 复制打开抖音，看看【合成作者的作品】太好看了😀 https://v.douyin.com/iSynthAbc_1/7.46 快来围观！",
				want: "https://v.douyin.com/iSynthAbc_1/",
			},
			{
				in:   "长按复制此条消息，打开抖音搜索 v.douyin.com/iSynthXyz9/8.15，查看详情。",
				want: "https://v.douyin.com/iSynthXyz9/",
			},
			{
				in:   "正在直播：https://live.douyin.com/10008888?enter_from_merge=link_share。",
				want: "https://live.douyin.com/10008888?enter_from_merge=link_share",
			},
		}
		for _, tc := range cases {
			got, ok := ExtractShareURL(tc.in)
			if !ok || got != tc.want {
				t.Fatalf("ExtractShareURL(%q) = (%q, %v), want (%q, true)", tc.in, got, ok, tc.want)
			}
		}
	})

	t.Run("short_link_hops_early_stop_and_hop_security", func(t *testing.T) {
		store := openTestDouyinCredStore(t, true)
		var hopRequests []string
		var mu sync.Mutex

		recordReq := func(u string) {
			mu.Lock()
			hopRequests = append(hopRequests, u)
			mu.Unlock()
		}
		resetReqs := func() {
			mu.Lock()
			hopRequests = nil
			mu.Unlock()
		}

		redirectResp := func(loc string) *http.Response {
			hdr := make(http.Header)
			hdr.Set("Location", loc)
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     hdr,
				Body:       io.NopCloser(strings.NewReader("")),
			}
		}

		client := NewClient(ClientOptions{
			CredStore:   store,
			MinInterval: -1,
			Entropy:     newDeterministicEntropy(0x66),
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				full := req.URL.String()
				recordReq(full)
				switch full {
				case "https://v.douyin.com/iHopTwo/":
					return redirectResp("https://v.amemv.com/iHopTwoStep2/"), nil
				case "https://v.amemv.com/iHopTwoStep2/":
					return redirectResp("https://www.iesdouyin.com/share/video/7300000000000000055/?region=CN"), nil
				case "https://www.iesdouyin.com/share/video/7300000000000000055/?region=CN":
					t.Fatalf("resolver must early-stop on Location extraction and NOT fetch destination HTML")
					return nil, nil

				case "https://v.douyin.com/iTooMany1/":
					return redirectResp("https://v.douyin.com/iTooMany2/"), nil
				case "https://v.douyin.com/iTooMany2/":
					return redirectResp("https://v.douyin.com/iTooMany3/"), nil
				case "https://v.douyin.com/iTooMany3/":
					return redirectResp("https://v.douyin.com/iTooMany4/"), nil
				case "https://v.douyin.com/iTooMany4/":
					return redirectResp("https://v.douyin.com/iTooMany5/"), nil
				case "https://v.douyin.com/iTooMany5/":
					return redirectResp("https://v.douyin.com/iTooMany6/"), nil

				case "https://v.douyin.com/iDowngradeHTTP/":
					return redirectResp("http://www.douyin.com/video/7300000000000000055"), nil

				case "https://v.douyin.com/iExternalHost/":
					return redirectResp("https://evil.example.com/video/7300000000000000055"), nil

				case "https://v.douyin.com/iDirectIP/":
					return redirectResp("https://203.0.113.10/video/7300000000000000055"), nil

				default:
					return jsonHTTPResponse(http.StatusNotFound, `{}`, nil), nil
				}
			}),
		})

		reg := site.NewRegistry(NewAdapter(client))

		// 1. Two-hop short link in pasted share text with glued /7.46 -> early-stops at hop 2!
		resetReqs()
		target, err := reg.Resolve(ctx, "4.12 复制打开抖音 https://v.douyin.com/iHopTwo/7.46 看视频！")
		if err != nil {
			t.Fatalf("Resolve 2-hop short link failed: %v", err)
		}
		if target.Site != site.Douyin || target.Kind != "video" || target.ID != "7300000000000000055" {
			t.Fatalf("unexpected ResolveTarget: %+v", target)
		}
		if len(hopRequests) != 2 {
			t.Fatalf("short link hop count = %d (%v), want exact 2 early-stop hops", len(hopRequests), hopRequests)
		}

		// 2. > 5 hops -> ErrKindBlocked after 5 hops.
		resetReqs()
		if _, err := reg.Resolve(ctx, "https://v.douyin.com/iTooMany1/"); !site.IsKind(err, site.ErrKindBlocked) {
			t.Fatalf(">5 hops kind = %q, want blocked (err=%v)", site.KindOf(err), err)
		}
		if len(hopRequests) != 5 {
			t.Fatalf(">5 hops issued %d requests, want max 5", len(hopRequests))
		}

		// 3. Downgrade to http://, external host, and direct IP redirects -> ErrKindBlocked.
		for _, badShort := range []string{
			"https://v.douyin.com/iDowngradeHTTP/",
			"https://v.douyin.com/iExternalHost/",
			"https://v.douyin.com/iDirectIP/",
		} {
			if _, err := reg.Resolve(ctx, badShort); !site.IsKind(err, site.ErrKindBlocked) {
				t.Fatalf("Resolve(%s) kind = %q, want blocked (err=%v)", badShort, site.KindOf(err), err)
			}
		}
	})

	t.Run("route_priority_and_zero_request_unsupported", func(t *testing.T) {
		store := openTestDouyinCredStore(t, true)
		var reqCount atomic.Int32

		client := NewClient(ClientOptions{
			CredStore:   store,
			MinInterval: -1,
			Entropy:     newDeterministicEntropy(0x77),
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				reqCount.Add(1)
				t.Fatalf("unexpected network request for local route resolution: %s", req.URL.String())
				return nil, nil
			}),
		})
		reg := site.NewRegistry(NewAdapter(client))

		positiveCases := []struct {
			name   string
			input  string
			kind   string
			id     string
			secUID string
			canon  string
		}{
			{
				name:  "modal_id_on_user_page_wins_as_video",
				input: "https://www.douyin.com/user/MS4wLjABAAAA_SYNTH_USER_99?modal_id=7300000000000000099",
				kind:  "video",
				id:    "7300000000000000099",
				canon: "https://www.douyin.com/video/7300000000000000099",
			},
			{
				name:  "modal_id_on_discover_page_wins_as_video",
				input: "https://www.douyin.com/discover?modal_id=7300000000000000088",
				kind:  "video",
				id:    "7300000000000000088",
				canon: "https://www.douyin.com/video/7300000000000000088",
			},
			{
				name:  "vid_on_jingxuan_page_wins_as_video",
				input: "https://www.douyin.com/jingxuan?vid=7300000000000000077",
				kind:  "video",
				id:    "7300000000000000077",
				canon: "https://www.douyin.com/video/7300000000000000077",
			},
			{
				name:  "note_and_slides_paths",
				input: "https://www.iesdouyin.com/share/slides/7300000000000000066/?did=0",
				kind:  "note",
				id:    "7300000000000000066",
				canon: "https://www.douyin.com/note/7300000000000000066",
			},
			{
				name:   "share_user_query_sec_uid_preserves_dash_underscore_double_underscore",
				input:  "https://www.iesdouyin.com/share/user/?sec_uid=MS4wLjABAAAA_SYNTH_A__B-1&u_code=0",
				kind:   "user",
				id:     "MS4wLjABAAAA_SYNTH_A__B-1",
				secUID: "MS4wLjABAAAA_SYNTH_A__B-1",
				canon:  "https://www.douyin.com/user/MS4wLjABAAAA_SYNTH_A__B-1",
			},
			{
				name:   "user_self_url_maps_to_logged_in_sec_uid",
				input:  "https://www.douyin.com/user/self?from_tab_name=main",
				kind:   "user",
				id:     "MS4wLjABAAAA_EXISTING_SEED_USER_01",
				secUID: "MS4wLjABAAAA_EXISTING_SEED_USER_01",
				canon:  "https://www.douyin.com/user/MS4wLjABAAAA_EXISTING_SEED_USER_01",
			},
			{
				name:   "literal_user_self_token",
				input:  "/user/self",
				kind:   "user",
				id:     "MS4wLjABAAAA_EXISTING_SEED_USER_01",
				secUID: "MS4wLjABAAAA_EXISTING_SEED_USER_01",
				canon:  "https://www.douyin.com/user/MS4wLjABAAAA_EXISTING_SEED_USER_01",
			},
			{
				name:   "bare_sec_uid_token",
				input:  "MS4wLjABAAAA_SYNTH_BARE_USER_01",
				kind:   "user",
				id:     "MS4wLjABAAAA_SYNTH_BARE_USER_01",
				secUID: "MS4wLjABAAAA_SYNTH_BARE_USER_01",
				canon:  "https://www.douyin.com/user/MS4wLjABAAAA_SYNTH_BARE_USER_01",
			},
		}

		for _, tc := range positiveCases {
			t.Run(tc.name, func(t *testing.T) {
				got, err := reg.Resolve(ctx, tc.input)
				if err != nil {
					t.Fatalf("Resolve(%q) failed: %v", tc.input, err)
				}
				if got.Site != site.Douyin || got.Kind != tc.kind || got.ID != tc.id || got.SecUID != tc.secUID || got.CanonicalURL != tc.canon {
					t.Fatalf("Resolve(%q) = %+v, want kind=%s id=%s secUID=%s canon=%s",
						tc.input, got, tc.kind, tc.id, tc.secUID, tc.canon)
				}
			})
		}

		unsupportedZeroReqInputs := []string{
			"7300000000000000001", // bare 19-digit pure number
			"https://mall.douyin.com/goods/7300000000000000001",
			"https://haohuo.jinritemai.com/views/product/item2?id=12345",
			"https://www.douyin.com/shop/7300000000000000001",
			"https://www.douyin.com/collection/7300000000000000011",
			"https://www.douyin.com/mix/7300000000000000011",
			"https://www.douyin.com/?mix_id=7300000000000000011",
			"https://www.douyin.com/hashtag/7300000000000000011",
			"https://www.douyin.com/challenge/7300000000000000011",
			"https://www.douyin.com/search/%E5%90%88%E6%88%90",
			"https://www.douyin.com/discover",
			"https://www.douyin.com/lvdetail/7300000000000000011",
			"https://www.douyin.com/microapp/ttc000000001",
		}
		for _, badInput := range unsupportedZeroReqInputs {
			_, err := reg.Resolve(ctx, badInput)
			if err == nil || !site.IsUnsupported(err) {
				t.Fatalf("Resolve(%q) err = %v, want Unsupported=true", badInput, err)
			}
			// Also verify direct Client.ResolveLink rejects with 0 requests.
			_, err2 := client.ResolveLink(ctx, badInput)
			if err2 == nil || !site.IsUnsupported(err2) {
				t.Fatalf("Client.ResolveLink(%q) err = %v, want Unsupported=true", badInput, err2)
			}
		}

		if got := reqCount.Load(); got != 0 {
			t.Fatalf("expected 0 HTTP requests across local route & unsupported tests, got %d", got)
		}
	})
}

func TestClientPacingBodyCapAndCDNImageIsolation(t *testing.T) {
	ctx := context.Background()
	store := openTestDouyinCredStore(t, true)

	now := time.UnixMilli(1763000000000)
	var clockMu sync.Mutex
	var sleeps []time.Duration

	client := NewClient(ClientOptions{
		CredStore:    store,
		MinInterval:  1000 * time.Millisecond,
		JitterMax:    300 * time.Millisecond,
		MaxBodyBytes: 1024, // 1 KiB cap for bounded-read test
		Now: func() time.Time {
			clockMu.Lock()
			defer clockMu.Unlock()
			return now
		},
		Sleep: func(_ context.Context, d time.Duration) error {
			clockMu.Lock()
			sleeps = append(sleeps, d)
			now = now.Add(d)
			clockMu.Unlock()
			return nil
		},
		Entropy: newDeterministicEntropy(0x88),
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if strings.Contains(req.URL.Host, "douyinpic.com") {
				if c := req.Header.Get("Cookie"); c != "" {
					t.Fatalf("CDN FetchImage leaked Cookie header: %q", c)
				}
				if !strings.Contains(req.URL.Path, "@2x.jpeg") {
					t.Fatalf("CDN FetchImage altered '@' in path: %s", req.URL.Path)
				}
				if req.URL.Query().Get("disguised") == "html" {
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
						Body:       io.NopCloser(strings.NewReader("<html>verify</html>")),
					}, nil
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"image/jpeg"}},
					Body:       io.NopCloser(bytes.NewReader([]byte{0xFF, 0xD8, 0xFF, 0xD9})),
				}, nil
			}

			if req.URL.Query().Get("oversize") == "1" {
				return jsonHTTPResponse(http.StatusOK, `{"status_code":0,"pad":"`+strings.Repeat("A", 2048)+`"}`, nil), nil
			}
			return jsonHTTPResponse(http.StatusOK, `{"status_code":0,"user":{"uid":"900000000001","sec_uid":"MS4wLjABAAAA_EXISTING_SEED_USER_01","nickname":"已登录种子账号"}}`, nil), nil
		}),
	})

	// Two consecutive API requests -> first has 0 sleep, second sleeps in [1000ms, 1300ms].
	if _, err := client.GetSelfProfile(ctx); err != nil {
		t.Fatalf("first GetSelfProfile failed: %v", err)
	}
	clockMu.Lock()
	now = now.Add(100 * time.Millisecond) // 100ms elapsed before second call
	clockMu.Unlock()

	if _, err := client.GetSelfProfile(ctx); err != nil {
		t.Fatalf("second GetSelfProfile failed: %v", err)
	}
	if len(sleeps) != 1 {
		t.Fatalf("sleep count = %d, want 1", len(sleeps))
	}
	// Remaining sleep after 100ms elapsed must be in [900ms, 1200ms] (since total target interval is [1000ms, 1300ms]).
	if sleeps[0] < 900*time.Millisecond || sleeps[0] > 1200*time.Millisecond {
		t.Fatalf("paced sleep duration = %v, want within [900ms, 1200ms]", sleeps[0])
	}

	// Body exceeding MaxBodyBytes returns ErrKindBlocked.
	oversizeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status_code":0,"pad":"` + strings.Repeat("B", 2048) + `"}`))
	}))
	defer oversizeSrv.Close()

	capClient := NewClient(ClientOptions{
		CredStore:      store,
		AllowLocalhost: true,
		MinInterval:    -1,
		MaxBodyBytes:   512,
		Endpoints: Endpoints{
			ProfileSelf: oversizeSrv.URL + "/aweme/v1/web/user/profile/self/",
		},
	})
	if _, err := capClient.GetSelfProfile(ctx); !site.IsKind(err, site.ErrKindBlocked) {
		t.Fatalf("oversize response kind = %q, want blocked (err=%v)", site.KindOf(err), err)
	}

	// CDN FetchImage upgrades http:// -> https://, strips Cookie, and preserves '@' + query params.
	imgBytes, ct, err := client.FetchImage(ctx, "http://p3-pc-sign.douyinpic.com/tos-cn-i-0813/cover@2x.jpeg?x-expires=1763099999")
	if err != nil {
		t.Fatalf("FetchImage failed: %v", err)
	}
	if ct != "image/jpeg" || len(imgBytes) != 4 {
		t.Fatalf("FetchImage returned ct=%q len=%d, want image/jpeg len=4", ct, len(imgBytes))
	}

	// Disguised HTML response on CDN image rejected with ErrKindBlocked.
	if _, _, err := client.FetchImage(ctx, "https://p3-pc-sign.douyinpic.com/tos-cn-i-0813/cover@2x.jpeg?disguised=html"); !site.IsKind(err, site.ErrKindBlocked) {
		t.Fatalf("disguised HTML image kind = %q, want blocked (err=%v)", site.KindOf(err), err)
	}

	// Non-whitelisted CDN host rejected with 0 requests (ErrKindBlocked).
	if _, _, err := client.FetchImage(ctx, "https://evil-cdn.example.com/cover@2x.jpeg"); !site.IsKind(err, site.ErrKindBlocked) {
		t.Fatalf("non-whitelisted CDN host kind = %q, want blocked (err=%v)", site.KindOf(err), err)
	}
}
