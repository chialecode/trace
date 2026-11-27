package bilibili

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chialecode/trace/internal/site"
)

func TestBilibiliSecurityAndProtocolRegressions(t *testing.T) {
	t.Run("ValidateMediaURL enforces port 443 and AllowLocalhost probe stays compatible", func(t *testing.T) {
		prod := NewClient(ClientOptions{AllowLocalhost: false})
		if err := prod.ValidateMediaURL("https://upos-sz-mirror.bilivideo.com:8443/v.m4s"); err == nil {
			t.Fatal("expected non-443 port media URL to be rejected")
		}
		if err := prod.ValidateMediaURL("https://user:pass@upos-sz-mirror.bilivideo.com/v.m4s"); err == nil {
			t.Fatal("expected userinfo media URL to be rejected")
		}
		if err := prod.ValidateMediaURL("https://upos-sz-mirror.bilivideo.com/v.m4s"); err != nil {
			t.Fatalf("valid CDN URL rejected: %v", err)
		}

		local := NewClient(ClientOptions{AllowLocalhost: true})
		if !local.AllowLocalhost() {
			t.Fatal("AllowLocalhost() = false, want true")
		}
		if err := local.ValidateMediaURL("http://127.0.0.1:1/test.flv"); err != nil {
			t.Fatalf("local loopback probe rejected: %v", err)
		}

		// Live media validation keeps official allowlist on initial playurl hop while
		// permitting scheduled public HTTPS:443 vendor CDN targets (e.g. gotcha multivariant/302).
		vendorURL := "https://edge-1.h2.smtcdns.net/live-bvc/index.m3u8"
		if err := prod.ValidateLiveMediaURL(vendorURL, false); !IsKind(err, ErrKindBlocked) {
			t.Fatalf("ValidateLiveMediaURL(initial vendor) = %v, want blocked", err)
		}
		if err := prod.ValidateLiveMediaURL(vendorURL, true); err != nil {
			t.Fatalf("ValidateLiveMediaURL(scheduled vendor) rejected: %v", err)
		}
		for _, bad := range []string{
			"http://edge-1.h2.smtcdns.net/live/index.m3u8",
			"https://edge-1.h2.smtcdns.net:8443/live/index.m3u8",
			"https://203.0.113.7/live/index.m3u8",
			"https://localhost/live/index.m3u8",
			"https://user:pass@edge-1.h2.smtcdns.net/live/index.m3u8",
		} {
			if err := prod.ValidateLiveMediaURL(bad, true); err == nil {
				t.Fatalf("ValidateLiveMediaURL(%q, true) must be blocked", bad)
			}
		}

		liveClient, _ := prod.LiveMediaHTTPClient()
		if liveClient == nil || liveClient.CheckRedirect == nil {
			t.Fatal("LiveMediaHTTPClient missing CheckRedirect policy")
		}
		redirReq, err := http.NewRequest(http.MethodGet, vendorURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		redirReq.Header.Set("Cookie", "SESSDATA=secret")
		redirReq.Header.Set("Authorization", "Bearer secret")
		if err := liveClient.CheckRedirect(redirReq, []*http.Request{redirReq}); err != nil {
			t.Fatalf("LiveMediaHTTPClient.CheckRedirect rejected valid scheduled vendor CDN: %v", err)
		}
		if redirReq.Header.Get("Cookie") != "" || redirReq.Header.Get("Authorization") != "" {
			t.Fatalf("LiveMediaHTTPClient.CheckRedirect leaked credentials: %+v", redirReq.Header)
		}
		if redirReq.Header.Get("Referer") != "https://live.bilibili.com/" || redirReq.Header.Get("Origin") != "https://live.bilibili.com" {
			t.Fatalf("LiveMediaHTTPClient.CheckRedirect headers = %+v", redirReq.Header)
		}
		badRedirReq, _ := http.NewRequest(http.MethodGet, "http://edge-1.h2.smtcdns.net/live/index.m3u8", nil)
		if err := liveClient.CheckRedirect(badRedirReq, []*http.Request{redirReq}); !IsKind(err, ErrKindBlocked) {
			t.Fatalf("LiveMediaHTTPClient.CheckRedirect(http) = %v, want blocked", err)
		}
		fiveHops := []*http.Request{redirReq, redirReq, redirReq, redirReq, redirReq}
		if err := liveClient.CheckRedirect(redirReq, fiveHops); !IsKind(err, ErrKindBlocked) {
			t.Fatalf("LiveMediaHTTPClient.CheckRedirect(5 hops) = %v, want blocked", err)
		}
	})

	t.Run("SiteError strips csrf, qrcode_key, w_rid from Endpoint and Err", func(t *testing.T) {
		client, store, _ := newTestLoggedClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/x/passport-login/web/cookie/info":
				_, _ = w.Write([]byte(`{"code":0,"data":{"refresh":true,"timestamp":1700000123456}}`))
			case "/x/web-interface/nav":
				_, _ = w.Write([]byte(`{"code":0,"data":{"isLogin":true,"mid":1001,"wbi_img":{"img_url":"https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png","sub_url":"https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png"}}}`))
			default:
				w.WriteHeader(http.StatusPreconditionFailed)
			}
		}), -1)
		if _, err := store.SaveQR(map[string]string{"SESSDATA": "sess_tok_1", "bili_jct": "SECRET_JCT_999"}, "", 1001, "u", ""); err != nil {
			t.Fatal(err)
		}
		_, err := client.CheckAndRefreshCookie(context.Background())
		if err == nil {
			t.Fatal("expected missing refresh_token error")
		}
		var se *SiteError
		if !errors.As(err, &se) {
			t.Fatalf("expected SiteError, got %T", err)
		}
		if strings.Contains(se.Endpoint, "SECRET_JCT_999") || strings.Contains(se.Endpoint, "?") {
			t.Fatalf("SiteError.Endpoint leaked query/csrf: %q", se.Endpoint)
		}
		// Trigger 412 on WBI request and verify w_rid does not appear in Endpoint.
		_, wbiErr := client.UserVideos(context.Background(), 1001, 1, 10, "", "hello world")
		if !errors.As(wbiErr, &se) {
			t.Fatalf("expected SiteError, got %v", wbiErr)
		}
		if strings.Contains(se.Endpoint, "w_rid") || strings.Contains(se.Endpoint, "?") {
			t.Fatalf("WBI SiteError.Endpoint leaked query: %q", se.Endpoint)
		}
	})

	t.Run("CheckAndRefreshCookie skips remote_push and invokes authExpired on 86095", func(t *testing.T) {
		var hits atomic.Int32
		client, store, _ := newTestLoggedClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			switch {
			case r.URL.Path == "/x/passport-login/web/cookie/info":
				_, _ = w.Write([]byte(`{"code":0,"data":{"refresh":true,"timestamp":1700000123456}}`))
			case strings.HasPrefix(r.URL.Path, "/correspond/1/"):
				_, _ = w.Write([]byte(`<div id="1-name">csrf_step3</div>`))
			case r.URL.Path == "/x/passport-login/web/cookie/refresh":
				_, _ = w.Write([]byte(`{"code":86095,"message":"refresh_csrf mismatch"}`))
			default:
				http.NotFound(w, r)
			}
		}), -1)
		if _, err := store.MarkPushed("remote_push"); err != nil {
			t.Fatal(err)
		}
		if _, err := client.CheckAndRefreshCookie(context.Background()); err != nil {
			t.Fatalf("remote_push should skip refresh cleanly, got %v", err)
		}
		if hits.Load() != 0 {
			t.Fatalf("remote_push made %d refresh HTTP calls, want 0", hits.Load())
		}

		// Now switch to QR credential and verify 86095 triggers authExpired callback.
		var expiredCalled atomic.Int32
		client.SetAuthExpiredHandler(func() { expiredCalled.Add(1) })
		if _, err := store.SaveQR(map[string]string{"SESSDATA": "sess_qr", "bili_jct": "jct_qr"}, "rt_1", 1001, "u", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := client.CheckAndRefreshCookie(context.Background()); err == nil {
			t.Fatal("expected error on 86095")
		}
		if expiredCalled.Load() != 1 {
			t.Fatalf("authExpired called %d times on 86095, want 1", expiredCalled.Load())
		}
	})

	t.Run("doWBIGet automatic single -352 retry succeeds without premature backoff", func(t *testing.T) {
		var arcCalls, navCalls, breakerReports atomic.Int32
		client, _, _ := newTestLoggedClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/x/web-interface/nav":
				navCalls.Add(1)
				_, _ = w.Write([]byte(`{"code":0,"data":{"isLogin":true,"mid":1001,"wbi_img":{"img_url":"https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png","sub_url":"https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png"}}}`))
			case "/x/space/wbi/arc/search":
				if arcCalls.Add(1) == 1 {
					_, _ = w.Write([]byte(`{"code":-352,"message":"风控校验失败"}`))
					return
				}
				_, _ = w.Write([]byte(`{"code":0,"data":{"list":{"vlist":[]},"page":{"pn":1,"ps":10,"count":0}}}`))
			default:
				http.NotFound(w, r)
			}
		}), -1)
		var tripped atomic.Bool
		client.SetAutomaticRequestHandlers(
			func() error {
				if tripped.Load() {
					return &SiteError{Kind: ErrKindBackoff, Message: "backoff"}
				}
				return nil
			},
			func(kind string) {
				breakerReports.Add(1)
				tripped.Store(true)
			},
		)
		ctx := site.WithAutomaticRequests(context.Background(), "auto-cache")
		if _, err := client.UserVideos(ctx, 1001, 1, 10, "", ""); err != nil {
			t.Fatalf("automatic single -352 retry failed: %v", err)
		}
		if arcCalls.Load() != 2 || breakerReports.Load() != 0 {
			t.Fatalf("arcCalls=%d breakerReports=%d, want (2, 0)", arcCalls.Load(), breakerReports.Load())
		}
	})

	t.Run("fetchNav expires auth on -101 even when wbi_img is present", func(t *testing.T) {
		var expired atomic.Int32
		client, _, _ := newTestLoggedClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"code":-101,"message":"not logged in","data":{"isLogin":false,"wbi_img":{"img_url":"https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png","sub_url":"https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png"}}}`))
		}), -1)
		client.SetAuthExpiredHandler(func() { expired.Add(1) })
		if _, err := client.fetchNav(context.Background()); !IsKind(err, ErrKindAuthRequired) {
			t.Fatalf("fetchNav(-101) err = %v, want auth_required", err)
		}
		if expired.Load() != 1 {
			t.Fatalf("authExpired calls = %d, want 1", expired.Load())
		}
	})

	t.Run("DanmakuXML classifies JSON error envelope on seg 2 instead of silently truncating", func(t *testing.T) {
		var expired atomic.Int32
		client, _, _ := newTestLoggedClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("segment_index") == "1" {
				_, _ = w.Write(EncodeDmSegMobileReply([]DanmakuElem{{ID: 1, IDStr: "1", Progress: 1000, Content: "hello"}}))
				return
			}
			_, _ = w.Write([]byte(`{"code":-101,"message":"账号未登录"}`))
		}), -1)
		client.SetAuthExpiredHandler(func() { expired.Add(1) })
		if _, err := client.DanmakuXML(context.Background(), 12345, 700); !IsKind(err, ErrKindAuthRequired) {
			t.Fatalf("DanmakuXML seg 2 -101 err = %v, want auth_required", err)
		}
		if expired.Load() != 1 {
			t.Fatalf("authExpired calls = %d, want 1", expired.Load())
		}
	})

	t.Run("resolveShortLink rejects non-HTTPS/port and preserves 429 and Unsupported flag", func(t *testing.T) {
		client, store, _ := newTestLoggedClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		}), -1)
		prod := NewClient(ClientOptions{CredStore: store, AllowLocalhost: false})
		if _, err := prod.ResolveLink(context.Background(), "http://b23.tv:8443/abc"); !IsKind(err, ErrKindBlocked) {
			t.Fatalf("non-HTTPS/non-443 b23.tv err = %v, want blocked", err)
		}
		if _, err := prod.ResolveLink(context.Background(), "https://www.bilibili.com/bangumi/play/ep12345"); !site.IsUnsupported(err) {
			t.Fatalf("bangumi err = %v, want site.IsUnsupported == true", err)
		}
		if _, err := client.ResolveLink(context.Background(), "https://b23.tv/rate-limited"); !IsKind(err, ErrKindRateLimited) {
			t.Fatalf("b23.tv 429 err = %v, want rate_limited", err)
		}
	})

	t.Run("PollQR stale flow does not clear active Polling and partial Set-Cookie merges URL fallback", func(t *testing.T) {
		client, store, _ := newTestLoggedClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/x/passport-login/web/qrcode/generate":
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"url": "https://passport.bilibili.com/qr", "qrcode_key": "qk1"}})
			case "/x/passport-login/web/qrcode/poll":
				http.SetCookie(w, &http.Cookie{Name: "SESSDATA", Value: "from_set_cookie"})
				_, _ = w.Write([]byte(`{"code":0,"data":{"code":0,"refresh_token":"rt_merged","url":"https://www.bilibili.com/?bili_jct=from_url_fallback&DedeUserID=9999"}}`))
			case "/x/web-interface/nav":
				_, _ = w.Write([]byte(`{"code":0,"data":{"isLogin":true,"mid":9999,"uname":"Merged"}}`))
			}
		}), -1)
		s1, err := client.StartQR(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		s2, err := client.StartQR(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		// Mark s2 as actively polling; polling stale s1 must NOT flip s2.Polling to false.
		client.qrMu.Lock()
		client.activeQR.Polling = true
		client.qrMu.Unlock()
		resStale, err := client.PollQR(context.Background(), s1.FlowID)
		if err != nil || resStale.State != "canceled" {
			t.Fatalf("stale PollQR = (%+v, %v)", resStale, err)
		}
		client.qrMu.Lock()
		stillPolling := client.activeQR.Polling
		client.activeQR.Polling = false
		client.qrMu.Unlock()
		if !stillPolling {
			t.Fatal("polling canceled flow cleared active flow's Polling flag")
		}

		res2, err := client.PollQR(context.Background(), s2.FlowID)
		if err != nil || res2.State != "confirmed" || store.Cookies()["bili_jct"] != "from_url_fallback" {
			t.Fatalf("partial Set-Cookie fallback failed: res=%+v err=%v cookies=%v", res2, err, store.Cookies())
		}
	})

	t.Run("EncodeWBIQuery encodes spaces as %20 and VerifyCredential handles nil store", func(t *testing.T) {
		p := url.Values{}
		p.Set("keyword", "hello world")
		signed := SignWBIParams(p, "ea1db124af3c7062474693fa704f4ff8", 1700000000)
		encoded := EncodeWBIQuery(signed)
		if strings.Contains(encoded, "hello+world") || !strings.Contains(encoded, "hello%20world") {
			t.Fatalf("EncodeWBIQuery = %q, want %%20 space encoding", encoded)
		}

		nilClient := NewClient(ClientOptions{})
		if _, err := nilClient.VerifyCredential(context.Background()); !IsKind(err, ErrKindAuthRequired) {
			t.Fatalf("VerifyCredential(nil credStore) err = %v, want auth_required", err)
		}

		vtt, srt := ConvertSubtitleToVTTAndSRT([]SubtitleBodyItem{{From: 0, To: 1, Content: `It's "quoted" <ok>`}})
		if !strings.Contains(string(vtt), "&#34;") || !strings.Contains(string(srt), `It's "quoted"`) {
			t.Fatalf("unexpected VTT/SRT escaping: vtt=%q srt=%q", string(vtt), string(srt))
		}
	})
}
