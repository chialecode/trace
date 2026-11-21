package douyin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/douyin/sign"
)

func TestDouyinSecurityAndProtocolRegressions(t *testing.T) {
	t.Run("FetchImageLimited enforces INV-07 RequireAuth with zero outbound requests", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("img-bytes"))
		}))
		defer srv.Close()

		store, _, err := credential.OpenDouyin(filepath.Join(t.TempDir(), "douyin.bin"))
		if err != nil {
			t.Fatal(err)
		}
		client := NewClient(ClientOptions{
			CredStore:      store,
			AllowLocalhost: true,
			MinInterval:    -1,
		})
		if _, _, err := client.FetchImageLimited(context.Background(), srv.URL+"/obj/cover.jpg", 1024); !site.IsKind(err, site.ErrKindAuthRequired) {
			t.Fatalf("logged-out FetchImageLimited err = %v, want auth_required", err)
		}
		if hits.Load() != 0 {
			t.Fatalf("logged-out FetchImageLimited sent %d requests, want 0", hits.Load())
		}

		// After login, FetchImageLimited succeeds.
		if _, err := store.SaveDouyinManual("sessionid=synth_sess_img_01; ttwid=synth_ttwid"); err != nil {
			t.Fatal(err)
		}
		data, _, err := client.FetchImageLimited(context.Background(), srv.URL+"/obj/cover.jpg", 1024)
		if err != nil || string(data) != "img-bytes" {
			t.Fatalf("logged-in FetchImageLimited = (%q, %v)", string(data), err)
		}
	})

	t.Run("classifyHTTPAndEnvelopeError inspects nested data challenge even when status_msg is non-empty", func(t *testing.T) {
		store, _, err := credential.OpenDouyin(filepath.Join(t.TempDir(), "douyin.bin"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.SaveDouyinManual("sessionid=synth_sess_02; ttwid=synth_ttwid"); err != nil {
			t.Fatal(err)
		}
		client := NewClient(ClientOptions{
			CredStore:   store,
			MinInterval: -1,
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				body := `{"status_code":0,"status_msg":"ok","data":{"status_code":0,"verify_center_decision_conf":"{\"type\":\"captcha\"}"}}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(body)),
				}, nil
			}),
		})
		if _, err := client.EnterLiveRoom(context.Background(), "123456", ""); !site.IsKind(err, site.ErrKindRiskControl) {
			t.Fatalf("nested challenge with non-empty status_msg err = %v, want risk_control", err)
		}
	})

	t.Run("doAPIRequest checks auth and context before burning automatic budget", func(t *testing.T) {
		var budgetChecks atomic.Int32
		store, _, err := credential.OpenDouyin(filepath.Join(t.TempDir(), "douyin.bin"))
		if err != nil {
			t.Fatal(err)
		}
		client := NewClient(ClientOptions{
			CredStore:   store,
			MinInterval: -1,
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				t.Fatal("unexpected network request")
				return nil, nil
			}),
		})
		client.SetAutomaticRequestHandlers(func(context.Context, string, string) error {
			budgetChecks.Add(1)
			return &site.Error{Site: site.Douyin, Kind: site.ErrKindQuota, Message: "quota"}
		}, nil)

		autoCtx := site.WithAutomaticRequests(context.Background(), "auto-cache")
		if _, err := client.GetWorkDetail(autoCtx, "7300000000000000001", StreamSelectOptions{}); !site.IsKind(err, site.ErrKindAuthRequired) {
			t.Fatalf("logged-out auto request err = %v, want auth_required", err)
		}
		if budgetChecks.Load() != 0 {
			t.Fatalf("budget checked %d times while logged out, want 0", budgetChecks.Load())
		}

		// When logged in but ctx already canceled, do not burn budget either.
		if _, err := store.SaveDouyinManual("sessionid=synth_sess_03; ttwid=synth_ttwid"); err != nil {
			t.Fatal(err)
		}
		canceledCtx, cancel := context.WithCancel(autoCtx)
		cancel()
		if _, err := client.GetWorkDetail(canceledCtx, "7300000000000000001", StreamSelectOptions{}); err == nil {
			t.Fatal("expected canceled error")
		}
		if budgetChecks.Load() != 0 {
			t.Fatalf("budget checked %d times on canceled ctx, want 0", budgetChecks.Load())
		}
	})

	t.Run("SelectLiveStream and CheckLiveStatus honor round-stop without extra requests or cache wipe", func(t *testing.T) {
		var calls atomic.Int32
		store, _, err := credential.OpenDouyin(filepath.Join(t.TempDir(), "douyin.bin"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.SaveDouyinManual("sessionid=synth_sess_04; ttwid=synth_ttwid"); err != nil {
			t.Fatal(err)
		}
		client := NewClient(ClientOptions{
			CredStore:   store,
			MinInterval: -1,
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				if strings.Contains(req.URL.Path, "reflow/info") {
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"application/json"}},
						Body:       io.NopCloser(strings.NewReader(`{"status_code":0,"verify_center_decision_conf":"challenge"}`)),
					}, nil
				}
				if strings.Contains(req.URL.Path, "user/profile/other") {
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"application/json"}},
						Body:       io.NopCloser(strings.NewReader(`{"status_code":0,"user":{"sec_uid":"MS4wLjABAAAA_synth_live_secuid_01","nickname":"Anchor","room_id_str":"7300000000000000019"}}`)),
					}, nil
				}
				return &http.Response{
					StatusCode: http.StatusForbidden,
					Body:       io.NopCloser(strings.NewReader("ArgusSecurityPlugin")),
				}, nil
			}),
		})
		// ReflowRoomInfo risk_control must NOT fall through to EnterLiveRoom.
		if _, err := client.SelectLiveStream(context.Background(), "MS4wLjABAAAA_synth_live_secuid_01", "7300000000000000019", nil); !site.IsKind(err, site.ErrKindRiskControl) {
			t.Fatalf("SelectLiveStream reflow risk_control err = %v, want risk_control", err)
		}
		if calls.Load() != 1 {
			t.Fatalf("SelectLiveStream made %d calls after reflow risk_control, want 1", calls.Load())
		}

		// checkLiveViaProfile must propagate ReflowRoomInfo risk_control instead of returning LiveStateLive, nil.
		if _, err := client.CheckLiveStatus(context.Background(), "MS4wLjABAAAA_synth_live_secuid_01", ""); !site.IsKind(err, site.ErrKindRiskControl) {
			t.Fatalf("CheckLiveStatus reflow risk_control err = %v, want risk_control", err)
		}

		// Cached webRID must NOT be wiped on backoff error.
		client.rememberWebRID("MS4wLjABAAAA_synth_live_secuid_01", "888888", true)
		client.SetAutomaticRequestHandlers(func(context.Context, string, string) error {
			return &site.Error{Site: site.Douyin, Kind: site.ErrKindBackoff, Message: "backoff"}
		}, nil)
		autoCtx := site.WithAutomaticRequests(context.Background(), "auto-cache")
		if _, err := client.CheckLiveStatus(autoCtx, "MS4wLjABAAAA_synth_live_secuid_01", "888888"); !site.IsKind(err, site.ErrKindBackoff) {
			t.Fatalf("CheckLiveStatus under backoff err = %v, want backoff", err)
		}
		if got := client.CachedWebRID("MS4wLjABAAAA_synth_live_secuid_01"); got != "888888" {
			t.Fatalf("CachedWebRID was wiped on backoff: got %q, want 888888", got)
		}
	})

	t.Run("expandShortLink classifies 429 and 403 and reports automatic error", func(t *testing.T) {
		var reported atomic.Int32
		store, _, err := credential.OpenDouyin(filepath.Join(t.TempDir(), "douyin.bin"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.SaveDouyinManual("sessionid=synth_sess_05; ttwid=synth_ttwid"); err != nil {
			t.Fatal(err)
		}
		client := NewClient(ClientOptions{
			CredStore:   store,
			MinInterval: -1,
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Body:       io.NopCloser(strings.NewReader("too many requests")),
				}, nil
			}),
		})
		client.SetAutomaticRequestHandlers(nil, func(_ string, err error) {
			if site.IsKind(err, site.ErrKindRateLimited) {
				reported.Add(1)
			}
		})
		autoCtx := site.WithAutomaticRequests(context.Background(), "auto-cache")
		if _, err := client.ResolveLink(autoCtx, "https://v.douyin.com/rate429/"); !site.IsKind(err, site.ErrKindRateLimited) {
			t.Fatalf("short link 429 err = %v, want rate_limited", err)
		}
		if reported.Load() != 1 {
			t.Fatalf("ReportAutomaticError calls on shortlink 429 = %d, want 1", reported.Load())
		}
	})

	t.Run("WorkDetail JSON omits AvatarURL and CoverURL, detects BitRate DRM, and normalizes sub-second duration", func(t *testing.T) {
		w := WorkDetail{
			ID:   "7300000000000000001",
			Kind: WorkKindVideo,
			Author: AuthorSummary{
				SecUID:    "MS4wLjABAAAA_synth_01",
				Nickname:  "Tester",
				AvatarURL: "https://p3-pc.douyinpic.com/avatar.jpeg?x-signature=SECRET_SIG",
			},
			Music: &MusicInfo{
				Title:    "Song",
				CoverURL: "https://p3-pc.douyinpic.com/cover.jpeg?x-signature=SECRET_COVER",
			},
		}
		raw, err := json.Marshal(w)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "SECRET_SIG") || strings.Contains(string(raw), "SECRET_COVER") {
			t.Fatalf("WorkDetail JSON leaked signed URLs: %s", string(raw))
		}

		node := &rawAwemeNode{
			Video: &rawVideoNode{
				BitRate: []rawBitRateEntry{{
					PlayAddr: &rawURLContainer{URLList: []string{"https://v3-web.douyinvod.com/v.mp4?drm_type=widevine"}},
				}},
			},
		}
		if blocked, _ := detectPaidOrEncrypted(node); !blocked {
			t.Fatal("expected BitRate DRM URL to be detected")
		}
		if got := normalizeDurationSec(800, 0); got != 1 {
			t.Fatalf("normalizeDurationSec(800, 0) = %d, want 1", got)
		}
	})

	t.Run("AllowLocalhost wraps real transport with LoopbackTransport", func(t *testing.T) {
		store, _, err := credential.OpenDouyin(filepath.Join(t.TempDir(), "douyin.bin"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.SaveDouyinManual("sessionid=synth_sess_06; ttwid=synth_ttwid"); err != nil {
			t.Fatal(err)
		}
		// No Endpoint override: defaults point to real www.douyin.com, which LoopbackTransport must block.
		client := NewClient(ClientOptions{
			CredStore:      store,
			AllowLocalhost: true,
			MinInterval:    -1,
		})
		_, err = client.GetWorkDetail(context.Background(), "7300000000000000001", StreamSelectOptions{})
		if err == nil {
			t.Fatal("expected AllowLocalhost client to reject non-loopback default endpoint")
		}
		var se *site.Error
		if !errors.As(err, &se) || se.Err == nil || !strings.Contains(se.Err.Error(), "non-loopback") {
			t.Fatalf("expected non-loopback rejection error, got %v (inner=%v)", err, se)
		}
	})

	t.Run("sign Stage-2 preserves RawQuery bytes and decodeQueryComponent omits raw secret", func(t *testing.T) {
		signer := sign.NewSigner(sign.Options{})
		res, err := signer.Sign(context.Background(), sign.SignInput{
			Endpoint:  "/aweme/v1/web/aweme/post/",
			RawQuery:  "device_platform=webapp&aid=6383&sec_user_id=MS4wLjAB_test&custom=a%2fb",
			UserAgent: DefaultUserAgent,
			UIFID:     "uifid_test_value",
		})
		if err != nil {
			t.Fatalf("Stage-2 RawQuery Sign: %v", err)
		}
		if !strings.HasPrefix(res.SignedQuery, "device_platform=webapp&aid=6383&sec_user_id=MS4wLjAB_test&custom=a%2fb&a_bogus=") {
			t.Fatalf("Stage-2 altered Stage-1 RawQuery prefix: %q", res.SignedQuery)
		}
		_, badErr := signer.Sign(context.Background(), sign.SignInput{
			Endpoint:  "/aweme/v1/web/aweme/post/",
			RawQuery:  "msToken=TOP_SECRET_VALUE%ZZ",
			UserAgent: DefaultUserAgent,
			UIFID:     "uifid_test_value",
		})
		if badErr == nil || strings.Contains(badErr.Error(), "TOP_SECRET_VALUE") {
			t.Fatalf("decodeQueryComponent error leaked secret or succeeded: %v", badErr)
		}
	})
}
