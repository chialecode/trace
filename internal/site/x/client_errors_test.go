package x

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/site"
)

func TestPacingSerialGateRedirectDisableAndBodyCap(t *testing.T) {
	store := newTestXStore(t, "auth_token=SYNTH_AUTH_PACE; ct0=SynthCT0Pace0001")

	var curTimeMu sync.Mutex
	curTime := time.Unix(1795327200, 0).UTC()
	var slept []time.Duration
	var inFlight atomic.Int32
	var maxInFlight atomic.Int32

	_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
		cur := inFlight.Add(1)
		for {
			prev := maxInFlight.Load()
			if cur <= prev || maxInFlight.CompareAndSwap(prev, cur) {
				break
			}
		}
		time.Sleep(15 * time.Millisecond)
		inFlight.Add(-1)

		switch {
		case strings.Contains(r.URL.Path, "/"+OpUserByRestId):
			if strings.Contains(r.URL.RawQuery, "999999999999999999") {
				// Oversized > 4 MiB body test.
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(bytes.Repeat([]byte("a"), int(DefaultMaxBodyBytes)+64))
				return
			}
			if strings.Contains(r.URL.RawQuery, "888888888888888888") {
				// 302 redirect MUST NOT be followed!
				w.Header().Set("Location", "https://x.com/i/flow/login")
				w.WriteHeader(http.StatusFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(synthUserByScreenNamePayload("1000000000000000001", "example_user", "Example", "")))
		default:
			http.NotFound(w, r)
		}
	}, store, func(opts *ClientOptions) {
		opts.MinInterval = DefaultMinInterval
		opts.JitterMax = DefaultJitterMax
		opts.Entropy = bytes.NewReader(bytes.Repeat([]byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, 16))
		opts.Now = func() time.Time {
			curTimeMu.Lock()
			defer curTimeMu.Unlock()
			return curTime
		}
		opts.Sleep = func(_ context.Context, d time.Duration) error {
			curTimeMu.Lock()
			slept = append(slept, d)
			curTime = curTime.Add(d)
			curTimeMu.Unlock()
			return nil
		}
	})

	// Run 2 concurrent UserByRestId calls -> must serialize (maxInFlight == 1) and sleep >= 4000ms between calls.
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, errs[idx] = client.GetUserByRestId(context.Background(), "1000000000000000001")
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent call %d failed: %v", i, err)
		}
	}
	if maxInFlight.Load() != 1 {
		t.Fatalf("maxInFlight = %d, want 1 (strict serial gate)", maxInFlight.Load())
	}
	if len(slept) == 0 {
		t.Fatalf("expected pacing sleeps between requests")
	}
	for _, d := range slept {
		if d < DefaultMinInterval || d > DefaultMinInterval+DefaultJitterMax {
			t.Fatalf("sleep duration %v outside [4000ms, 6000ms]", d)
		}
	}

	// Verify 4 MiB body cap rejects oversized responses cleanly.
	if _, err := client.GetUserByRestId(context.Background(), "999999999999999999"); !site.IsKind(err, site.ErrKindBlocked) {
		t.Fatalf("oversized body err = %v, want blocked", err)
	}

	// Verify auto-redirect is disabled on API requests (302 to login -> auth_required immediately).
	if _, err := client.GetUserByRestId(context.Background(), "888888888888888888"); !site.IsKind(err, site.ErrKindAuthRequired) {
		t.Fatalf("302 login redirect err = %v, want auth_required", err)
	}
}

func TestCT0PassiveSyncInvalidRejectionAnd353SingleReplay(t *testing.T) {
	t.Run("passive Set-Cookie ct0 sync and invalid ct0 ignored", func(t *testing.T) {
		store := newTestXStore(t, "auth_token=SYNTH_AUTH_CT0; ct0=SynthCT0Init0001")
		var callCount atomic.Int32
		var seenCookies []string
		var seenCSRFs []string
		var notified []string

		_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Path, "/"+OpUserByRestId) {
				http.NotFound(w, r)
				return
			}
			n := callCount.Add(1)
			seenCookies = append(seenCookies, r.Header.Get("Cookie"))
			seenCSRFs = append(seenCSRFs, r.Header.Get("x-csrf-token"))
			switch n {
			case 1:
				// Valid rotation.
				http.SetCookie(w, &http.Cookie{Name: "ct0", Value: "SynthCT0Rotated0002", Path: "/"})
			case 2:
				// Invalid chars in Set-Cookie ct0 MUST be rejected without mutating store or header!
				http.SetCookie(w, &http.Cookie{Name: "ct0", Value: "bad!invalid_ct0", Path: "/"})
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(synthUserByScreenNamePayload("1000000000000000001", "example_user", "Example", "")))
		}, store, func(opts *ClientOptions) {
			opts.OnCT0Updated = func(ct0 string) { notified = append(notified, ct0) }
		})

		for i := 0; i < 3; i++ {
			if _, err := client.GetUserByRestId(context.Background(), "1000000000000000001"); err != nil {
				t.Fatalf("call %d failed: %v", i+1, err)
			}
		}

		if store.CT0() != "SynthCT0Rotated0002" {
			t.Fatalf("store CT0 = %q, want SynthCT0Rotated0002", store.CT0())
		}
		if len(notified) != 1 || notified[0] != "SynthCT0Rotated0002" {
			t.Fatalf("OnCT0Updated calls = %v, want [SynthCT0Rotated0002]", notified)
		}
		if seenCSRFs[0] != "SynthCT0Init0001" || seenCSRFs[1] != "SynthCT0Rotated0002" || seenCSRFs[2] != "SynthCT0Rotated0002" {
			t.Fatalf("x-csrf-token progression = %v", seenCSRFs)
		}
		for idx, ch := range seenCookies {
			if strings.Count(ch, "ct0=") != 1 {
				t.Fatalf("call %d sent duplicate or missing ct0 in Cookie: %q", idx+1, ch)
			}
		}
		if !strings.Contains(seenCookies[1], "ct0=SynthCT0Rotated0002") || !strings.Contains(seenCookies[2], "ct0=SynthCT0Rotated0002") {
			t.Fatalf("rotated ct0 not reflected in Cookie header: %v", seenCookies)
		}
	})

	t.Run("403 code 353 with valid new ct0 replays ONCE and succeeds", func(t *testing.T) {
		store := newTestXStore(t, "auth_token=SYNTH_AUTH_353; ct0=SynthCT0Stale0001")
		var attempts atomic.Int32
		var seenCSRFs []string
		var seenCookies []string

		_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Path, "/"+OpUserByRestId) {
				http.NotFound(w, r)
				return
			}
			n := attempts.Add(1)
			seenCSRFs = append(seenCSRFs, r.Header.Get("x-csrf-token"))
			seenCookies = append(seenCookies, r.Header.Get("Cookie"))
			if n == 1 {
				http.SetCookie(w, &http.Cookie{Name: "ct0", Value: "SynthCT0Fresh0002", Path: "/"})
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"errors":[{"code":353,"message":"This request requires a matching csrf cookie and header."}]}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(synthUserByScreenNamePayload("1000000000000000001", "example_user", "Example", "")))
		}, store, nil)

		prof, err := client.GetUserByRestId(context.Background(), "1000000000000000001")
		if err != nil {
			t.Fatalf("353 single replay failed: %v", err)
		}
		if prof.RestID != "1000000000000000001" {
			t.Fatalf("unexpected profile: %+v", prof)
		}
		if attempts.Load() != 2 {
			t.Fatalf("attempts = %d, want 2 (initial + 1 replay)", attempts.Load())
		}
		if seenCSRFs[0] != "SynthCT0Stale0001" || seenCSRFs[1] != "SynthCT0Fresh0002" {
			t.Fatalf("seenCSRFs = %v", seenCSRFs)
		}
		if strings.Count(seenCookies[1], "ct0=") != 1 || !strings.Contains(seenCookies[1], "ct0=SynthCT0Fresh0002") {
			t.Fatalf("replay Cookie must have single updated ct0: %q", seenCookies[1])
		}
		if store.CT0() != "SynthCT0Fresh0002" {
			t.Fatalf("store CT0 not persisted after 353 replay: %q", store.CT0())
		}
	})

	t.Run("403 code 353 without valid new ct0 does 0 replays and returns auth_required", func(t *testing.T) {
		store := newTestXStore(t, "auth_token=SYNTH_AUTH_353B; ct0=SynthCT0Stale0001")
		var attempts atomic.Int32

		_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Path, "/"+OpUserByRestId) {
				http.NotFound(w, r)
				return
			}
			attempts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"errors":[{"code":353,"message":"This request requires a matching csrf cookie and header."}]}`))
		}, store, nil)

		_, err := client.GetUserByRestId(context.Background(), "1000000000000000001")
		if !site.IsKind(err, site.ErrKindAuthRequired) {
			t.Fatalf("err = %v, want auth_required", err)
		}
		if attempts.Load() != 1 {
			t.Fatalf("attempts = %d, want 1 (zero replay when no new ct0)", attempts.Load())
		}
	})

	t.Run("403 code 353 repeating on replay stops at 2 total attempts and returns auth_required", func(t *testing.T) {
		store := newTestXStore(t, "auth_token=SYNTH_AUTH_353C; ct0=SynthCT0Stale0001")
		var attempts atomic.Int32

		_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Path, "/"+OpUserByRestId) {
				http.NotFound(w, r)
				return
			}
			n := attempts.Add(1)
			http.SetCookie(w, &http.Cookie{Name: "ct0", Value: "SynthCT0Repeat000" + strconv.Itoa(int(n)), Path: "/"})
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"errors":[{"code":353,"message":"This request requires a matching csrf cookie and header."}]}`))
		}, store, nil)

		_, err := client.GetUserByRestId(context.Background(), "1000000000000000001")
		if !site.IsKind(err, site.ErrKindAuthRequired) {
			t.Fatalf("err = %v, want auth_required", err)
		}
		if attempts.Load() != 2 {
			t.Fatalf("attempts = %d, want exactly 2 (at most one replay)", attempts.Load())
		}
	})
}

func TestPerOperationRateLimitBrakeZeroUpstreamRequests(t *testing.T) {
	store := newTestXStore(t, "auth_token=SYNTH_AUTH_RL; ct0=SynthCT0RL0001")

	var clockMu sync.Mutex
	curTime := time.Unix(1795327200, 0).UTC()
	resetTime := curTime.Add(5 * time.Minute)
	var userHits atomic.Int32
	var screenHits atomic.Int32

	_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/"+OpUserByRestId):
			n := userHits.Add(1)
			w.Header().Set("x-rate-limit-limit", "50")
			if n == 1 {
				w.Header().Set("x-rate-limit-remaining", "1")
			} else {
				w.Header().Set("x-rate-limit-remaining", "49")
			}
			w.Header().Set("x-rate-limit-reset", strconv.FormatInt(resetTime.Unix(), 10))
			_, _ = w.Write([]byte(synthUserByScreenNamePayload("1000000000000000001", "example_user", "Example", "")))
		case strings.HasSuffix(r.URL.Path, "/"+OpUserByScreenName):
			screenHits.Add(1)
			w.Header().Set("x-rate-limit-limit", "95")
			w.Header().Set("x-rate-limit-remaining", "90")
			w.Header().Set("x-rate-limit-reset", strconv.FormatInt(resetTime.Unix(), 10))
			_, _ = w.Write([]byte(synthUserByScreenNamePayload("1000000000000000001", "example_user", "Example", "")))
		default:
			http.NotFound(w, r)
		}
	}, store, func(opts *ClientOptions) {
		opts.Now = func() time.Time {
			clockMu.Lock()
			defer clockMu.Unlock()
			return curTime
		}
		// Deterministic 30s jitter on top of reset (using 8-byte little-endian value = 30s in ns).
		opts.RateLimitJitterMax = 60 * time.Second
		jitterBuf := make([]byte, 8)
		v := uint64((30 * time.Second).Nanoseconds())
		for i := 0; i < 8; i++ {
			jitterBuf[i] = byte(v >> (8 * i))
		}
		opts.Entropy = bytes.NewReader(jitterBuf)
	})

	// 1st call succeeds and returns remaining=1 -> arms proactive brake until reset + 30s.
	if _, err := client.GetUserByRestId(context.Background(), "1000000000000000001"); err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	if userHits.Load() != 1 {
		t.Fatalf("userHits = %d, want 1", userHits.Load())
	}

	// 2nd call to SAME operation before reset+30s -> 0 upstream requests, clear Chinese message + RetryAfter/ResetAt!
	_, err := client.GetUserByRestId(context.Background(), "1000000000000000001")
	if !site.IsKind(err, site.ErrKindRateLimited) {
		t.Fatalf("braked err = %v, want rate_limited", err)
	}
	if userHits.Load() != 1 {
		t.Fatalf("braked call hit upstream! userHits = %d", userHits.Load())
	}
	if !strings.Contains(err.Error(), "X 限流，约 6 分钟后再试") {
		t.Fatalf("unexpected rate-limit message: %q", err.Error())
	}
	rl, ok := RateLimitDetails(err)
	if !ok || rl.Operation != OpUserByRestId || !rl.ResetAt.Equal(resetTime) || rl.RetryAfter != 5*time.Minute+30*time.Second {
		t.Fatalf("RateLimitDetails = (%+v, %v)", rl, ok)
	}
	if !rl.RateLimitResetAt().Equal(resetTime) {
		t.Fatalf("RateLimitResetAt = %v, want %v", rl.RateLimitResetAt(), resetTime)
	}

	// Another operation (UserByScreenName) has its own independent bucket and still succeeds.
	if _, err := client.GetUserByScreenName(context.Background(), "example_user"); err != nil {
		t.Fatalf("unrelated operation failed under UserByRestId brake: %v", err)
	}
	if screenHits.Load() != 1 {
		t.Fatalf("screenHits = %d, want 1", screenHits.Load())
	}

	// Advance clock to reset + 31s -> brake releases and request succeeds.
	clockMu.Lock()
	curTime = resetTime.Add(31 * time.Second)
	clockMu.Unlock()

	if _, err := client.GetUserByRestId(context.Background(), "1000000000000000001"); err != nil {
		t.Fatalf("call after reset+jitter failed: %v", err)
	}
	if userHits.Load() != 2 {
		t.Fatalf("userHits after reset = %d, want 2", userHits.Load())
	}
}

func TestErrorClassificationEveryRowAndZeroAutomaticRetries(t *testing.T) {
	now := time.Unix(1795327200, 0).UTC()

	// Part 1: Direct unit table covering EVERY row of docs/research/x.md §10.1.
	rows := []struct {
		name          string
		status        int
		headers       map[string]string
		body          string
		wantKind      string
		wantRoundStop bool
		wantCircBreak bool
		wantCode      int
	}{
		{"401 code 32", 401, nil, `{"errors":[{"code":32,"message":"Could not authenticate you"}]}`, site.ErrKindAuthRequired, true, false, 32},
		{"401 code 89", 401, nil, `{"errors":[{"code":89,"message":"Invalid or expired token"}]}`, site.ErrKindAuthRequired, true, false, 89},
		{"400 code 215", 400, nil, `{"errors":[{"code":215,"message":"Bad Authentication data"}]}`, site.ErrKindAuthRequired, true, false, 215},
		{"400 code 239", 400, nil, `{"errors":[{"code":239,"message":"Bad guest token"}]}`, site.ErrKindAuthRequired, true, false, 239},
		{"403 code 353", 403, nil, `{"errors":[{"code":353,"message":"This request requires a matching csrf cookie and header"}]}`, site.ErrKindAuthRequired, true, false, 353},
		{"403 code 64 suspended", 403, nil, `{"errors":[{"code":64,"message":"Your account is suspended"}]}`, site.ErrKindRiskControl, true, true, 64},
		{"403 code 326 locked", 403, nil, `{"errors":[{"code":326,"message":"Account temporarily locked"}]}`, site.ErrKindRiskControl, true, true, 326},
		{"403 code 226 automated", 403, nil, `{"errors":[{"code":226,"message":"This request looks like it might be automated"}]}`, site.ErrKindRiskControl, true, true, 226},
		{"429 code 88 remaining 0", 429, map[string]string{"x-rate-limit-remaining": "0", "x-rate-limit-reset": "1795328100"}, `{"errors":[{"code":88,"message":"Rate limit exceeded"}]}`, site.ErrKindRateLimited, true, true, 88},
		{"429 code 88 remaining > 0 abuse risk", 429, map[string]string{"x-rate-limit-remaining": "42"}, `{"errors":[{"code":88,"message":"Rate limit exceeded"}]}`, site.ErrKindRiskControl, true, true, 88},
		{"200 code 88 headerless abuse risk", 200, nil, `{"errors":[{"code":88,"message":"Rate limit exceeded"}]}`, site.ErrKindRiskControl, true, true, 88},
		{"200 fake empty user shell", 200, nil, `{"data":{"user":{}}}`, site.ErrKindRateLimited, true, true, 0},
		{"empty 404 drift", 404, nil, "", site.ErrKindSchemaChanged, false, false, 0},
		{"empty 410 drift", 410, nil, "", site.ErrKindSchemaChanged, false, false, 0},
		{"400 features cannot be null", 400, nil, `{"errors":[{"message":"The following features cannot be null: rweb_tipjar"}]}`, site.ErrKindSchemaChanged, false, false, 0},
		{"400 Query Unspecified", 400, nil, `{"errors":[{"message":"Query: Unspecified"}]}`, site.ErrKindSchemaChanged, false, false, 0},
		{"400 code 214 bad cursor", 400, nil, `{"errors":[{"code":214,"message":"Bad cursor"}]}`, site.ErrKindBadRequest, false, false, 214},
		{"403 code 37 single target forbidden", 403, nil, `{"errors":[{"code":37,"message":"Not permitted"}]}`, site.ErrKindForbidden, false, false, 37},
		{"403 code 37 accompanied by 326 -> risk_control", 403, nil, `{"errors":[{"code":37,"message":"Denied"},{"code":326,"message":"Locked"}]}`, site.ErrKindRiskControl, true, true, 326},
		{"403 code 63 target suspended", 403, nil, `{"errors":[{"code":63,"message":"User has been suspended"}]}`, site.ErrKindUnavailable, false, false, 63},
		{"403 code 136 blocked", 403, nil, `{"errors":[{"code":136,"message":"You have been blocked"}]}`, site.ErrKindForbidden, false, false, 136},
		{"403 code 161", 403, nil, `{"errors":[{"code":161,"message":"Not allowed"}]}`, site.ErrKindForbidden, false, false, 161},
		{"403 code 179 protected", 403, nil, `{"errors":[{"code":179,"message":"Not authorized to see this status"}]}`, site.ErrKindForbidden, false, false, 179},
		{"403 code 219", 403, nil, `{"errors":[{"code":219,"message":"Visibility restricted"}]}`, site.ErrKindForbidden, false, false, 219},
		{"404 code 34", 404, nil, `{"errors":[{"code":34,"message":"Sorry, that page does not exist"}]}`, site.ErrKindUnavailable, false, false, 34},
		{"404 code 50", 404, nil, `{"errors":[{"code":50,"message":"User not found"}]}`, site.ErrKindUnavailable, false, false, 50},
		{"404 code 144", 404, nil, `{"errors":[{"code":144,"message":"No status found with that ID"}]}`, site.ErrKindUnavailable, false, false, 144},
		{"503 server error", 503, nil, `{"errors":[{"code":131,"message":"Internal error"}]}`, site.ErrKindNetwork, false, false, 131},
		{"200 LoadShed -1", 200, nil, `{"errors":[{"code":-1,"name":"LoadShed","message":"Over capacity"}]}`, site.ErrKindNetwork, false, false, -1},
		{"200 Cloudflare Turnstile HTML", 200, map[string]string{"Content-Type": "text/html"}, `<html><head><title>Just a moment...</title></head><body><div class="cf-turnstile"></div></body></html>`, site.ErrKindRiskControl, true, true, 0},
		{"200 Arkose FunCaptcha HTML", 200, map[string]string{"Content-Type": "text/html"}, `<html><body>ArkoseLabs FunCaptcha fc-token challenge</body></html>`, site.ErrKindRiskControl, true, true, 0},
		{"200 X Login HTML", 200, map[string]string{"Content-Type": "text/html"}, `<html><body><form action="/i/flow/login">Sign in to X</form></body></html>`, site.ErrKindAuthRequired, true, false, 0},
	}

	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			hdr := make(http.Header)
			for k, v := range tc.headers {
				hdr.Set(k, v)
			}
			se := ClassifyHTTPAndEnvelopeError(EnvelopeErrorContext{
				Endpoint:   "https://x.com/i/api/graphql/qid/UserTweets",
				Operation:  OpUserTweets,
				StatusCode: tc.status,
				Header:     hdr,
				Body:       []byte(tc.body),
				HasCookie:  true,
				Now:        now,
			})
			if se == nil {
				t.Fatalf("expected error kind %q, got nil", tc.wantKind)
			}
			if se.Kind != tc.wantKind {
				t.Errorf("kind = %q, want %q (msg=%q)", se.Kind, tc.wantKind, se.Message)
			}
			if tc.wantCode != 0 && se.Code != tc.wantCode {
				t.Errorf("code = %d, want %d", se.Code, tc.wantCode)
			}
			if site.IsRoundStop(se) != tc.wantRoundStop {
				t.Errorf("IsRoundStop = %v, want %v", site.IsRoundStop(se), tc.wantRoundStop)
			}
			if site.IsCircuitBreak(se) != tc.wantCircBreak {
				t.Errorf("IsCircuitBreak = %v, want %v", site.IsCircuitBreak(se), tc.wantCircBreak)
			}
		})
	}

	// Part 2: End-to-end client verification that risk_control & rate_limited NEVER clear credentials
	// and perform ZERO automatic retries!
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		wantKind string
	}{
		{"risk_control 326 no retry no cred clear", 403, `{"errors":[{"code":326,"message":"Locked"}]}`, site.ErrKindRiskControl},
		{"risk_control 226 no retry no cred clear", 403, `{"errors":[{"code":226,"message":"Automated"}]}`, site.ErrKindRiskControl},
		{"risk_control 64 no retry no cred clear", 403, `{"errors":[{"code":64,"message":"Suspended"}]}`, site.ErrKindRiskControl},
		{"rate_limited 429 no retry no cred clear", 429, `{"errors":[{"code":88,"message":"Rate limit"}]}`, site.ErrKindRateLimited},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestXStore(t, "auth_token=SYNTH_KEEP_AUTH; ct0=SynthKeepCT0001")
			var apiHits atomic.Int32
			_, client := newSynthFrontendHarnessWithOpts(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/"+OpUserByRestId) {
					apiHits.Add(1)
					w.Header().Set("Content-Type", "application/json")
					if tc.status == 429 {
						w.Header().Set("x-rate-limit-remaining", "0")
						w.Header().Set("x-rate-limit-reset", "1795328100")
					}
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
					return
				}
				http.NotFound(w, r)
			}, store, nil)

			_, err := client.GetUserByRestId(context.Background(), "1000000000000000001")
			if !site.IsKind(err, tc.wantKind) {
				t.Fatalf("err = %v, want %q", err, tc.wantKind)
			}
			if apiHits.Load() != 1 {
				t.Fatalf("apiHits = %d, want 1 (ZERO automatic retries)", apiHits.Load())
			}
			if !store.HasActiveCredential() || store.Status().State != "logged_in" {
				t.Fatalf("credentials MUST NOT be cleared or expired on %s: status=%+v", tc.wantKind, store.Status())
			}
		})
	}
}

func TestClientFrontendDriftRefreshCooldownQuotaAndBearerSentinel(t *testing.T) {
	homeV1 := mustReadTestdata(t, "webpack_home.html")
	mainV1 := mustReadTestdata(t, "webpack_main.js")
	ondemandV1 := mustReadTestdata(t, "webpack_ondemand.js")

	// Version 2 rotates queryIds and Bearer sentinel.
	bearerV2 := "SYNTHETIC_ROTATED_BEARER_SENTINEL_V2"
	mainV2 := bytes.ReplaceAll(mainV1, []byte("SYNTHETIC_WEB_BEARER_TOKEN_0001"), []byte(bearerV2))
	mainV2 = bytes.ReplaceAll(mainV2, []byte("SynthQidUserByRestId02"), []byte("SynthQidUserByRestIdV2"))

	var version atomic.Int32
	version.Store(1)
	var homeHits, mainHits, ondemandHits, gqlV1Hits, gqlV2Hits atomic.Int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/home":
			homeHits.Add(1)
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write(homeV1)
		case "/responsive-web/client-web/main.synth01a.js":
			mainHits.Add(1)
			if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
				t.Errorf("main.js leaked auth headers")
			}
			if version.Load() == 1 {
				_, _ = w.Write(mainV1)
			} else {
				_, _ = w.Write(mainV2)
			}
		case "/responsive-web/client-web/ondemand.s.synth02a.js":
			ondemandHits.Add(1)
			_, _ = w.Write(ondemandV1)
		case "/i/api/graphql/SynthQidUserByRestId02/UserByRestId":
			gqlV1Hits.Add(1)
			// Simulate empty 404 drift on stale queryId!
			w.WriteHeader(http.StatusNotFound)
		case "/i/api/graphql/SynthQidUserByRestIdV2/UserByRestId":
			n := gqlV2Hits.Add(1)
			if n == 2 {
				// Second call triggers 400 features cannot be null within 10m cooldown!
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"errors":[{"message":"The following features cannot be null: new_flag"}]}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(synthUserByScreenNamePayload("1000000000000000001", "example_user", "Example", "")))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	var clockMu sync.Mutex
	curTime := time.Unix(1795327200, 0).UTC()
	var budgetCharged []string
	var frontendKinds []FrontendRequestKind

	store := newTestXStore(t, "auth_token=SYNTH_AUTH_DRIFT; ct0=SynthCT0Drift0001")
	client := NewClient(ClientOptions{
		CredStore: store,
		Endpoints: Endpoints{
			HomeURL:         ts.URL + "/home",
			AccountSettings: ts.URL + "/i/api/1.1/account/settings.json",
			GraphQLBase:     ts.URL + "/i/api/graphql",
		},
		MinInterval:             -1,
		RateLimitJitterMax:      -1,
		AllowLocalhost:          true,
		AssetTransport:          newAbsRewriteTransport(t, ts),
		FrontendRefreshCooldown: DefaultFrontendRefreshCooldown,
		Now: func() time.Time {
			clockMu.Lock()
			defer clockMu.Unlock()
			return curTime
		},
		OnFrontendCharge: func(_ context.Context, kind FrontendRequestKind, _ string) error {
			frontendKinds = append(frontendKinds, kind)
			return nil
		},
	})
	client.SetAutomaticRequestHandlers(func(_ context.Context, source, endpoint string) error {
		budgetCharged = append(budgetCharged, source+":"+endpoint)
		return nil
	}, nil)

	// Switch server bundle to V2 so Refresh discovers the new queryId!
	// First initial Ensure loads V1, GraphQL hits stale V1 -> empty 404 -> Refresh loads V2 -> replays ONCE -> succeeds!
	autoCtx := site.WithAutomaticRequests(context.Background(), "subscription_check")
	if _, err := client.EnsureFrontendMetadata(autoCtx); err != nil {
		t.Fatalf("initial Ensure failed: %v", err)
	}
	if len(frontendKinds) != 3 || frontendKinds[0] != FrontendRequestHomeHTML || frontendKinds[1] != FrontendRequestMainBundle || frontendKinds[2] != FrontendRequestOndemandJS {
		t.Fatalf("frontendKinds = %v, want 3 static fetches", frontendKinds)
	}
	if len(budgetCharged) != 3 {
		t.Fatalf("all 3 static fetches must be charged to X hourly quota hook, got %d (%v)", len(budgetCharged), budgetCharged)
	}

	version.Store(2)
	prof, err := client.GetUserByRestId(autoCtx, "1000000000000000001")
	if err != nil {
		t.Fatalf("empty 404 drift recovery failed: %v", err)
	}
	if prof.RestID != "1000000000000000001" {
		t.Fatalf("unexpected profile: %+v", prof)
	}
	if gqlV1Hits.Load() != 1 || gqlV2Hits.Load() != 1 || homeHits.Load() != 2 || mainHits.Load() != 2 || ondemandHits.Load() != 2 {
		t.Fatalf("unexpected hit counts: v1=%d v2=%d home=%d main=%d ondemand=%d",
			gqlV1Hits.Load(), gqlV2Hits.Load(), homeHits.Load(), mainHits.Load(), ondemandHits.Load())
	}

	// Second drift signal (400 features cannot be null) within 10m cooldown -> 0 discovery hits, returns schema_changed!
	clockMu.Lock()
	curTime = curTime.Add(3 * time.Minute)
	clockMu.Unlock()

	_, err = client.GetUserByRestId(autoCtx, "1000000000000000001")
	if !site.IsKind(err, site.ErrKindSchemaChanged) {
		t.Fatalf("2nd drift within cooldown err = %v, want schema_changed", err)
	}
	if homeHits.Load() != 2 || mainHits.Load() != 2 || ondemandHits.Load() != 2 {
		t.Fatalf("2nd drift within 10m cooldown hit discovery upstream!")
	}
	// Bearer sentinel must NEVER appear in errors or marshaled metadata.
	if strings.Contains(err.Error(), bearerV2) || strings.Contains(err.Error(), "SYNTHETIC_WEB_BEARER_TOKEN_0001") {
		t.Fatalf("Bearer sentinel leaked into error: %q", err.Error())
	}
	metaJSON, _ := json.Marshal(client.Discovery().Current())
	if strings.Contains(string(metaJSON), bearerV2) {
		t.Fatalf("Bearer sentinel leaked into marshaled metadata")
	}

	// Disallowed operation (e.g., SearchTimeline) rejected locally with 0 HTTP requests!
	if _, err := client.doGraphQL(context.Background(), graphqlRequest{operation: "SearchTimeline"}); !site.IsKind(err, site.ErrKindBadRequest) {
		t.Fatalf("disallowed operation err = %v, want bad_request", err)
	}
}
