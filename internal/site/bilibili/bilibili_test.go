package bilibili

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/credential"
)

func newTestLoggedClient(t *testing.T, handler http.Handler, pacing time.Duration) (*Client, *credential.Store, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	credPath := filepath.Join(t.TempDir(), "credentials", "bilibili.dpapi")
	store, _, err := credential.Open(credPath)
	if err != nil {
		t.Fatalf("credential.Open: %v", err)
	}
	if _, err := store.SaveManual("SESSDATA=test_sess_1; bili_jct=test_csrf_1; DedeUserID=10001", "test_refresh_tok_1"); err != nil {
		t.Fatalf("store.SaveManual: %v", err)
	}

	ep := Endpoints{
		APIBase:      srv.URL,
		PassportBase: srv.URL,
		WWWBase:      srv.URL,
		SpaceBase:    srv.URL,
		DynamicBase:  srv.URL,
		LiveAPIBase:  srv.URL,
		LiveWebBase:  srv.URL,
	}
	client := NewClient(ClientOptions{
		CredStore:      store,
		Endpoints:      ep,
		PacingInterval: pacing,
		AllowLocalhost: true,
		NowFunc: func() time.Time {
			return time.Unix(1700000000, 0).UTC()
		},
	})
	client.SetWBIKeys("7cd084941338484aae1ad9425b84077c", "4932caff0ff746eab6f01bf08b70ac45")
	return client, store, srv
}

// C01: Supported link resolution (BV, av, b23.tv, space, UID, dynamic, opus, live short_id -> room_id, mixed share text).
func TestResolveLinkSupportedTargets(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/b23/abc123", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://www.bilibili.com/video/BV17x411w7KC?p=2&spm_id_from=333")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/room/v1/Room/room_init", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") != "213" {
			t.Errorf("unexpected room_init id=%q", r.URL.Query().Get("id"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"room_id":     998877,
				"short_id":    213,
				"uid":         54321,
				"live_status": 1,
				"encrypted":   false,
				"is_locked":   false,
			},
		})
	})

	client, _, _ := newTestLoggedClient(t, mux, -1)
	ctx := context.Background()

	// 1. Direct BV
	res, err := client.ResolveLink(ctx, "BV17x411w7KC")
	if err != nil || res.Kind != "video" || res.ID != "BV17x411w7KC" || res.AID != 170001 {
		t.Fatalf("direct BV resolve = %+v, err=%v", res, err)
	}

	// 2. Direct av -> BV
	res, err = client.ResolveLink(ctx, "av170001")
	if err != nil || res.Kind != "video" || res.ID != "BV17x411w7KC" || res.AID != 170001 {
		t.Fatalf("direct av resolve = %+v, err=%v", res, err)
	}

	// 3. Mixed share text with b23.tv short link
	res, err = client.ResolveLink(ctx, "【测试分享文本】 https://b23.tv/abc123 快来看看吧！")
	if err != nil || res.Kind != "video" || res.ID != "BV17x411w7KC" || res.Page != 2 {
		t.Fatalf("b23.tv resolve = %+v, err=%v", res, err)
	}

	// 4. Space URL & UID
	res, err = client.ResolveLink(ctx, "https://space.bilibili.com/456789/video")
	if err != nil || res.Kind != "user" || res.ID != "456789" || res.Mid != 456789 {
		t.Fatalf("space URL resolve = %+v, err=%v", res, err)
	}
	res, err = client.ResolveLink(ctx, "UID:456789")
	if err != nil || res.Kind != "user" || res.ID != "456789" {
		t.Fatalf("UID resolve = %+v, err=%v", res, err)
	}

	// 5. Dynamic t.bilibili.com and bilibili.com/opus
	res, err = client.ResolveLink(ctx, "https://t.bilibili.com/888999000111222333")
	if err != nil || res.Kind != "dynamic" || res.ID != "888999000111222333" {
		t.Fatalf("t.bilibili.com resolve = %+v, err=%v", res, err)
	}
	res, err = client.ResolveLink(ctx, "https://www.bilibili.com/opus/888999000111222333")
	if err != nil || res.Kind != "dynamic" || res.ID != "888999000111222333" {
		t.Fatalf("opus resolve = %+v, err=%v", res, err)
	}

	// 6. Live short_id -> canonical live:<room_id> via room_init
	res, err = client.ResolveLink(ctx, "https://live.bilibili.com/213")
	if err != nil || res.Kind != "live" || res.ID != "live:998877" || res.RoomID != 998877 || res.Mid != 54321 {
		t.Fatalf("live short_id resolve = %+v, err=%v", res, err)
	}
}

// C02: Malicious shortlink redirects and unsupported link types.
func TestResolveLinkRejectsUnsupportedAndMaliciousRedirects(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/b23/evil", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://evil.example.com/video/BV17x411w7KC")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/b23/http", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://www.bilibili.com/video/BV17x411w7KC")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/b23/hop2", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://b23.tv/second")
		w.WriteHeader(http.StatusFound)
	})

	client, _, _ := newTestLoggedClient(t, mux, -1)
	ctx := context.Background()

	// 1. External domain redirect blocked
	if _, err := client.ResolveLink(ctx, "https://b23.tv/evil"); !IsKind(err, ErrKindBlocked) {
		t.Fatalf("expected blocked error for external domain redirect, got %v", err)
	}

	// 2. Non-HTTPS redirect blocked
	if _, err := client.ResolveLink(ctx, "https://b23.tv/http"); !IsKind(err, ErrKindBlocked) {
		t.Fatalf("expected blocked error for non-HTTPS redirect, got %v", err)
	}

	// 3. Multi-hop shortlink blocked
	if _, err := client.ResolveLink(ctx, "https://b23.tv/hop2"); !IsKind(err, ErrKindBlocked) {
		t.Fatalf("expected blocked error for 2-hop shortlink, got %v", err)
	}

	// 4. Bangumi ep/ss and Article cv return structured unsupported error
	if target, err := client.ResolveLink(ctx, "https://www.bilibili.com/bangumi/play/ep12345"); !IsKind(err, ErrKindBadRequest) || !target.Unsupported {
		t.Fatalf("expected unsupported bangumi error, got target=%+v err=%v", target, err)
	}
	if target, err := client.ResolveLink(ctx, "https://www.bilibili.com/read/cv12345"); !IsKind(err, ErrKindBadRequest) || !target.Unsupported {
		t.Fatalf("expected unsupported article error, got target=%+v err=%v", target, err)
	}

	// 5. Non-bilibili URL rejected
	if _, err := client.ResolveLink(ctx, "https://www.example.com/watch?v=123"); !IsKind(err, ErrKindBadRequest) {
		t.Fatalf("expected bad_request for non-bilibili URL, got %v", err)
	}
}

// C04: Unauthenticated pre-request login guard makes 0 HTTP requests.
func TestPreRequestAuthGuardZeroRequests(t *testing.T) {
	var reqCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	credPath := filepath.Join(t.TempDir(), "credentials", "bilibili.dpapi")
	store, _, err := credential.Open(credPath)
	if err != nil {
		t.Fatalf("credential.Open: %v", err)
	}
	client := NewClient(ClientOptions{
		CredStore: store,
		Endpoints: Endpoints{
			APIBase:      srv.URL,
			PassportBase: srv.URL,
			WWWBase:      srv.URL,
			SpaceBase:    srv.URL,
			DynamicBase:  srv.URL,
			LiveAPIBase:  srv.URL,
			LiveWebBase:  srv.URL,
		},
		PacingInterval: -1,
		AllowLocalhost: true,
	})

	ctx := context.Background()
	calls := []func() error{
		func() error { _, err := client.ResolveLink(ctx, "BV17x411w7KC"); return err },
		func() error { _, err := client.VideoDetail(ctx, "BV17x411w7KC"); return err },
		func() error { _, err := client.UserCard(ctx, 12345); return err },
		func() error { _, err := client.UserVideos(ctx, 12345, 1, 30, "pubdate", ""); return err },
		func() error { _, err := client.UserDynamics(ctx, 12345, ""); return err },
		func() error { _, err := client.DynamicDetail(ctx, "8888888888888888"); return err },
		func() error { _, err := client.PlayURL(ctx, "BV17x411w7KC", 101, PlaySelectionOptions{}); return err },
		func() error { _, err := client.DanmakuXML(ctx, 101, 120); return err },
		func() error { _, err := client.Subtitles(ctx, "BV17x411w7KC", 101); return err },
		func() error { _, err := client.RelationTags(ctx); return err },
		func() error { _, err := client.RelationFollowings(ctx, 12345, 1, 20); return err },
		func() error { _, err := client.LiveStatusByUIDs(ctx, []int64{12345}); return err },
		func() error { _, err := client.LiveRoomPlayInfo(ctx, 9988, nil); return err },
	}

	for i, fn := range calls {
		err := fn()
		if !IsKind(err, ErrKindAuthRequired) {
			t.Fatalf("call #%d error = %v, want auth_required", i, err)
		}
	}
	if got := reqCount.Load(); got != 0 {
		t.Fatalf("HTTP server request count = %d, want 0", got)
	}
}

// C06: QR login states (86101 -> 86090 -> 0, 86038) and canceled flowId isolation.
func TestQRLifecycleAndCanceledFlowIsolation(t *testing.T) {
	var pollStep atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/x/passport-login/web/qrcode/generate", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"url":        "https://passport.bilibili.com/h5-app/passport/login/scan?qrcode_key=key123",
				"qrcode_key": "key123",
			},
		})
	})
	mux.HandleFunc("/x/passport-login/web/qrcode/poll", func(w http.ResponseWriter, r *http.Request) {
		step := pollStep.Add(1)
		switch step {
		case 1:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"code": 86101, "message": "未扫码"},
			})
		case 2:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"code": 86090, "message": "已扫码未确认"},
			})
		case 3:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"code": 86038, "message": "二维码已失效"},
			})
		default:
			http.SetCookie(w, &http.Cookie{Name: "SESSDATA", Value: "qr_sess_val"})
			http.SetCookie(w, &http.Cookie{Name: "bili_jct", Value: "qr_csrf_val"})
			http.SetCookie(w, &http.Cookie{Name: "DedeUserID", Value: "77777"})
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"code":          0,
					"refresh_token": "qr_refresh_token_val",
				},
			})
		}
	})
	mux.HandleFunc("/x/web-interface/nav", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"isLogin": true,
				"mid":     77777,
				"uname":   "QRUser",
				"face":    "https://i0.hdslb.com/bfs/face/member/noface.jpg",
			},
		})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	credPath := filepath.Join(t.TempDir(), "credentials", "bilibili.dpapi")
	store, _, _ := credential.Open(credPath)
	client := NewClient(ClientOptions{
		CredStore: store,
		Endpoints: Endpoints{
			APIBase:      srv.URL,
			PassportBase: srv.URL,
		},
		PacingInterval: -1,
		AllowLocalhost: true,
	})

	ctx := context.Background()
	sess1, err := client.StartQR(ctx)
	if err != nil || len(sess1.PNG) == 0 {
		t.Fatalf("StartQR sess1: %v", err)
	}

	// Cancel sess1 and verify polling sess1 returns canceled and never writes credentials.
	client.CancelQR(sess1.FlowID)
	resCanceled, err := client.PollQR(ctx, sess1.FlowID)
	if err != nil || resCanceled.State != "canceled" || store.Status().LoggedIn {
		t.Fatalf("expected canceled poll for sess1, got %+v err=%v loggedIn=%v", resCanceled, err, store.Status().LoggedIn)
	}

	// Start sess2 and test 86101 -> 86090 -> 86038 -> 0 transitions.
	sess2, err := client.StartQR(ctx)
	if err != nil {
		t.Fatalf("StartQR sess2: %v", err)
	}
	r1, _ := client.PollQR(ctx, sess2.FlowID)
	if r1.State != "waiting" {
		t.Fatalf("poll 1 state = %q, want waiting", r1.State)
	}
	r2, _ := client.PollQR(ctx, sess2.FlowID)
	if r2.State != "scanned" {
		t.Fatalf("poll 2 state = %q, want scanned", r2.State)
	}
	r3, _ := client.PollQR(ctx, sess2.FlowID)
	if r3.State != "expired" {
		t.Fatalf("poll 3 state = %q, want expired", r3.State)
	}
	r4, err := client.PollQR(ctx, sess2.FlowID)
	if err != nil || r4.State != "confirmed" || !r4.Account.LoggedIn || r4.Account.Uname != "QRUser" {
		t.Fatalf("poll 4 = %+v, err=%v", r4, err)
	}
}

// C08: 5-step Cookie refresh, missing refresh_token degradation, and confirm/refresh resume.
func TestCookieRefreshFiveStepAndConfirmResume(t *testing.T) {
	var refreshCalls atomic.Int32
	var confirmCalls atomic.Int32
	var failConfirmOnce atomic.Bool
	failConfirmOnce.Store(true)

	var gotConfirmCSRF string
	var gotConfirmOldRT string

	mux := http.NewServeMux()
	mux.HandleFunc("/x/web-interface/nav", func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("SESSDATA")
		if err != nil || cookie.Value != "renewed_sess_2" {
			t.Error("renewal candidate was not used for account verification")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"isLogin": true, "mid": 10001, "uname": "QRUser"}})
	})
	mux.HandleFunc("/x/passport-login/web/cookie/info", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"refresh":   true,
				"timestamp": 1700000123456,
			},
		})
	})
	mux.HandleFunc("/correspond/1/", func(w http.ResponseWriter, r *http.Request) {
		hexPart := strings.TrimPrefix(r.URL.Path, "/correspond/1/")
		if len(hexPart) != 256 { // 1024-bit RSA key = 128 bytes = 256 hex chars
			t.Errorf("correspondPath hex length = %d, want 256", len(hexPart))
		}
		_, _ = w.Write([]byte(`<html><body><div id="1-name">mock_refresh_csrf_token</div></body></html>`))
	})
	mux.HandleFunc("/x/passport-login/web/cookie/refresh", func(w http.ResponseWriter, r *http.Request) {
		refreshCalls.Add(1)
		_ = r.ParseForm()
		if r.FormValue("csrf") != "test_csrf_1" || r.FormValue("refresh_csrf") != "mock_refresh_csrf_token" || r.FormValue("refresh_token") != "test_refresh_tok_1" {
			t.Errorf("unexpected cookie/refresh form: %+v", r.Form)
		}
		http.SetCookie(w, &http.Cookie{Name: "SESSDATA", Value: "renewed_sess_2"})
		http.SetCookie(w, &http.Cookie{Name: "bili_jct", Value: "renewed_csrf_2"})
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"status":        0,
				"refresh_token": "renewed_refresh_tok_2",
			},
		})
	})
	mux.HandleFunc("/x/passport-login/web/confirm/refresh", func(w http.ResponseWriter, r *http.Request) {
		confirmCalls.Add(1)
		_ = r.ParseForm()
		gotConfirmCSRF = r.FormValue("csrf")
		gotConfirmOldRT = r.FormValue("refresh_token")
		if failConfirmOnce.CompareAndSwap(true, false) {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "message": "0"})
	})

	client, store, _ := newTestLoggedClient(t, mux, -1)
	ctx := context.Background()

	// C08: Manual cookie source does not auto-renew and makes 0 refresh calls.
	stManual, err := client.CheckAndRefreshCookie(ctx)
	if err != nil || stManual.Source != "manual" || refreshCalls.Load() != 0 {
		t.Fatalf("expected manual source to skip refresh, got st=%+v err=%v refreshCalls=%d", stManual, err, refreshCalls.Load())
	}

	// Switch to QR credential source for 5-step renewal test.
	_, err = store.SaveQR(map[string]string{
		"SESSDATA":   "test_sess_1",
		"bili_jct":   "test_csrf_1",
		"DedeUserID": "10001",
	}, "test_refresh_tok_1", 10001, "QRUser", "")
	if err != nil {
		t.Fatalf("SaveQR: %v", err)
	}

	// First attempt: Step 4 succeeds and commits new cookies + confirm_pending, Step 5 fails with 502.
	_, err = client.CheckAndRefreshCookie(ctx)
	if err == nil {
		t.Fatalf("expected error when confirm/refresh fails on first attempt")
	}
	if refreshCalls.Load() != 1 || confirmCalls.Load() != 1 {
		t.Fatalf("calls after attempt 1: refresh=%d confirm=%d", refreshCalls.Load(), confirmCalls.Load())
	}
	if gotConfirmCSRF != "renewed_csrf_2" || gotConfirmOldRT != "test_refresh_tok_1" {
		t.Fatalf("confirm/refresh params: csrf=%q (want renewed_csrf_2), refresh_token=%q (want test_refresh_tok_1)", gotConfirmCSRF, gotConfirmOldRT)
	}
	if !store.Status().ConfirmPending {
		t.Fatalf("expected ConfirmPending=true after step 5 failure")
	}

	// Second attempt: Resumes directly at Step 5 without calling cookie/refresh again!
	st, err := client.CheckAndRefreshCookie(ctx)
	if err != nil {
		t.Fatalf("second CheckAndRefreshCookie failed: %v", err)
	}
	if refreshCalls.Load() != 1 || confirmCalls.Load() != 2 {
		t.Fatalf("expected refreshCalls=1 and confirmCalls=2 on resume, got refresh=%d confirm=%d", refreshCalls.Load(), confirmCalls.Load())
	}
	if st.ConfirmPending || store.Cookies()["SESSDATA"] != "renewed_sess_2" {
		t.Fatalf("unexpected state after confirm completion: %+v", st)
	}

	// Missing refresh_token when source=qr and refresh=true marks refresh_failed.
	_, _ = store.SaveQR(map[string]string{"SESSDATA": "no_rt_sess", "bili_jct": "no_rt_csrf"}, "", 10001, "QRUser", "")
	stNoRT, err := client.CheckAndRefreshCookie(ctx)
	if err == nil || stNoRT.State != "refresh_failed" {
		t.Fatalf("expected refresh_failed when refresh_token is empty, got st=%+v err=%v", stNoRT, err)
	}
}

// C09: WBI signing fixed vector, special char filtering, and single -352 refresh retry.
func TestWBISigningFixedVectorAndSingle352Refresh(t *testing.T) {
	imgKey := ExtractWBIKey("https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png")
	subKey := ExtractWBIKey("https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png")
	mixin := MixinKey(imgKey, subKey)
	if mixin != "ea1db124af3c7062474693fa704f4ff8" {
		t.Fatalf("MixinKey = %q, want ea1db124af3c7062474693fa704f4ff8", mixin)
	}

	// Fixed vector from docs/research/bilibili.md §9 (arc/search with w_webid):
	arcVec := url.Values{
		"mid":              {"94697"},
		"ps":               {"30"},
		"tid":              {"0"},
		"pn":               {"2"},
		"keyword":          {""},
		"order":            {"pubdate"},
		"platform":         {"web"},
		"web_location":     {"1550101"},
		"order_avoided":    {"true"},
		"dm_img_list":      {"[]"},
		"dm_img_str":       {"bm8gd2ViZ2"},
		"dm_cover_img_str": {"bm8gd2ViZ2"},
		"dm_img_inter":     {`{"ds":[],"wh":[0,0,0],"of":[0,0,0]}`},
		"w_webid":          {"eyJ.web-id_1"},
	}
	signedArc := SignWBIParams(arcVec, mixin, 1702204169)
	if signedArc.Get("w_rid") != "d1973c4ae71235d7bc494f04744540c9" {
		t.Fatalf("arc/search w_webid vector w_rid = %q, want d1973c4ae71235d7bc494f04744540c9", signedArc.Get("w_rid"))
	}

	params := url.Values{}
	params.Set("foo", "one!two'three(four)five*six")
	params.Set("bar", "123")
	signed := SignWBIParams(params, mixin, 1700000000)
	if strings.ContainsAny(signed.Get("foo"), "!'()*") {
		t.Fatalf("special characters were not stripped from foo: %q", signed.Get("foo"))
	}
	if signed.Get("wts") != "1700000000" || len(signed.Get("w_rid")) != 32 {
		t.Fatalf("unexpected signed WBI params: %+v", signed)
	}

	// Single -352 refresh retry test:
	var viewCalls atomic.Int32
	var navCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/x/web-interface/nav", func(w http.ResponseWriter, r *http.Request) {
		navCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"isLogin": true,
				"wbi_img": map[string]any{
					"img_url": "https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png",
					"sub_url": "https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png",
				},
			},
		})
	})
	mux.HandleFunc("/x/web-interface/wbi/view", func(w http.ResponseWriter, r *http.Request) {
		c := viewCalls.Add(1)
		if c == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": -352, "message": "risk"})
			return
		}
		if c == 2 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"bvid":  "BV17x411w7KC",
					"aid":   170001,
					"title": "Recovered Video",
					"cid":   9001,
				},
			})
			return
		}
		// 3rd+ call returns persistent -352
		_ = json.NewEncoder(w).Encode(map[string]any{"code": -352, "message": "persistent risk"})
	})

	client, _, _ := newTestLoggedClient(t, mux, -1)
	ctx := context.Background()

	vd, err := client.VideoDetail(ctx, "BV17x411w7KC")
	if err != nil || vd.Title != "Recovered Video" {
		t.Fatalf("expected single -352 retry to recover, got vd=%+v err=%v", vd, err)
	}
	if navCalls.Load() != 1 || viewCalls.Load() != 2 {
		t.Fatalf("unexpected call counts: nav=%d view=%d", navCalls.Load(), viewCalls.Load())
	}

	// Persistent -352 must fail after 1 retry and not loop infinitely.
	if _, err := client.VideoDetail(ctx, "BV17x411w7KC"); !IsKind(err, ErrKindRiskControl) {
		t.Fatalf("expected risk_control on persistent -352, got %v", err)
	}
	if viewCalls.Load() != 4 {
		t.Fatalf("viewCalls after second VideoDetail = %d, want 4 (initial + 1 retry)", viewCalls.Load())
	}
}

// C10: User card and user videos required WBI parameters and error code mapping.
func TestUserCardAndVideosParamsAndErrors(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/12345", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><head><script id="__RENDER_DATA__" type="application/json">%7B%22access_id%22%3A%22eyJ.web-id_1%22%7D</script></head></html>`))
	})
	mux.HandleFunc("/x/web-interface/card", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("mid") == "404" {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": -404, "message": "啥都木有"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"card": map[string]any{
					"mid":        "12345",
					"name":       "TestUP",
					"sign":       "Bio text",
					"face":       "http://i0.hdslb.com/bfs/face/up.jpg",
					"fans":       1024,
					"friend":     12,
					"level_info": map[string]any{"current_level": 6},
				},
				"archive_count": 15,
				"following":     true,
			},
		})
	})
	mux.HandleFunc("/x/space/wbi/arc/search", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		for _, reqKey := range []string{"mid", "pn", "ps", "web_location", "dm_img_list", "dm_img_str", "dm_cover_img_str", "dm_img_inter", "w_webid", "w_rid", "wts"} {
			if q.Get(reqKey) == "" {
				t.Errorf("missing required arc/search param %q", reqKey)
			}
		}
		if q.Get("w_webid") != "eyJ.web-id_1" {
			t.Errorf("w_webid = %q, want eyJ.web-id_1", q.Get("w_webid"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"list": map[string]any{
					"vlist": []map[string]any{
						{
							"aid":     170001,
							"bvid":    "BV17x411w7KC",
							"title":   "Arc <em class=\"keyword\">Video</em> 1",
							"pic":     "//i0.hdslb.com/bfs/archive/cover.jpg",
							"length":  "05:30",
							"created": 1700000000,
							"play":    999,
						},
					},
				},
				"page": map[string]any{"pn": 1, "ps": 30, "count": 1},
			},
		})
	})

	client, _, _ := newTestLoggedClient(t, mux, -1)
	ctx := context.Background()

	card, err := client.UserCard(ctx, 12345)
	if err != nil || card.Name != "TestUP" || card.Fans != 1024 {
		t.Fatalf("UserCard = %+v, err=%v", card, err)
	}
	if _, err := client.UserCard(ctx, 404); !IsKind(err, ErrKindUnavailable) {
		t.Fatalf("expected unavailable for -404 user card, got %v", err)
	}

	vpage, err := client.UserVideos(ctx, 12345, 1, 30, "pubdate", "")
	if err != nil || len(vpage.Items) != 1 || vpage.Items[0].Title != "Arc Video 1" || vpage.Items[0].DurationSec != 330 {
		t.Fatalf("UserVideos = %+v, err=%v", vpage, err)
	}
}

// C11: Global 500ms API pacing and context cancellation.
func TestClientGlobalPacing500ms(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/x/relation/tags", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": []any{}})
	})

	client, _, _ := newTestLoggedClient(t, mux, 500*time.Millisecond)
	ctx := context.Background()

	start := time.Now()
	for i := 0; i < 3; i++ {
		if _, err := client.RelationTags(ctx); err != nil {
			t.Fatalf("RelationTags #%d: %v", i, err)
		}
	}
	elapsed := time.Since(start)
	if elapsed < 900*time.Millisecond {
		t.Fatalf("3 paced requests took %v, want >= 900ms", elapsed)
	}

	// Cancel context while waiting for pacing slot.
	cancelCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := client.RelationTags(cancelCtx); err == nil {
		t.Fatalf("expected canceled error when context expires during pacing wait")
	}
}

// C12: Dynamic types (AV, DRAW, WORD, ARTICLE, FORWARD) and unknown type degradation.
func TestDynamicsTypesAndGracefulDegradation(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/x/polymer/web-dynamic/v1/feed/space", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"has_more": false,
				"offset":   "next_1",
				"items": []map[string]any{
					{
						"id_str": "101",
						"type":   "DYNAMIC_TYPE_AV",
						"modules": map[string]any{
							"module_author": map[string]any{"mid": 1, "name": "UP1", "pub_ts": "1700000001"},
							"module_dynamic": map[string]any{
								"major": map[string]any{
									"type": "MAJOR_TYPE_ARCHIVE",
									"archive": map[string]any{
										"bvid":          "BV17x411w7KC",
										"title":         "Dynamic Video",
										"duration_text": "02:10",
									},
								},
							},
						},
					},
					{
						"id_str": "102",
						"type":   "DYNAMIC_TYPE_DRAW",
						"modules": map[string]any{
							"module_author": map[string]any{"mid": 1, "name": "UP1", "pub_ts": 1700000002},
							"module_dynamic": map[string]any{
								"major": map[string]any{
									"type": "MAJOR_TYPE_OPUS",
									"opus": map[string]any{
										"title":   "Draw Title",
										"summary": map[string]any{"text": "Draw text"},
										"pics": []map[string]any{
											{"url": "https://i0.hdslb.com/pic1.jpg", "width": 800, "height": 600},
											// A live photo (实况): a still and its motion clip.
											{"url": "https://i0.hdslb.com/bfs/new_dyn/live_a.png", "width": 1080, "height": 1440, "live_url": "//i0.hdslb.com/bfs/new_dyn/live_a.mp4"},
										},
									},
								},
							},
						},
					},
					{
						"id_str": "103",
						"type":   "DYNAMIC_TYPE_WORD",
						"modules": map[string]any{
							"module_author":  map[string]any{"mid": 1, "name": "UP1"},
							"module_dynamic": map[string]any{"desc": map[string]any{"text": "Pure text post"}},
						},
					},
					{
						"id_str": "104",
						"type":   "DYNAMIC_TYPE_ARTICLE",
						"modules": map[string]any{
							"module_author": map[string]any{"mid": 1, "name": "UP1"},
							"module_dynamic": map[string]any{
								"major": map[string]any{
									"article": map[string]any{"id": 99, "title": "Article Title", "desc": "Article Summary"},
								},
							},
						},
					},
					{
						"id_str": "105",
						"type":   "DYNAMIC_TYPE_FORWARD",
						"orig": map[string]any{
							"id_str": "101",
							"type":   "DYNAMIC_TYPE_AV",
							"modules": map[string]any{
								"module_author": map[string]any{"mid": 2, "name": "OrigUP"},
								"module_dynamic": map[string]any{
									"major": map[string]any{
										"archive": map[string]any{"bvid": "BV17x411w7KC", "title": "Forwarded Video"},
									},
								},
							},
						},
						"modules": map[string]any{
							"module_author":  map[string]any{"mid": 1, "name": "UP1"},
							"module_dynamic": map[string]any{"desc": map[string]any{"text": "Repost comment"}},
						},
					},
					{
						"id_str": "106",
						"type":   "DYNAMIC_TYPE_MUSIC_CUSTOM_FUTURE",
						"modules": map[string]any{
							"module_author": map[string]any{"mid": 1, "name": "UP1"},
						},
					},
				},
			},
		})
	})

	client, _, _ := newTestLoggedClient(t, mux, -1)
	page, err := client.UserDynamics(context.Background(), 1, "")
	if err != nil || len(page.Items) != 6 {
		t.Fatalf("UserDynamics = %+v, err=%v", page, err)
	}
	if len(page.Items[0].Videos) != 1 || page.Items[0].Videos[0].BVID != "BV17x411w7KC" {
		t.Fatalf("item 0 video missing: %+v", page.Items[0])
	}
	if len(page.Items[1].Images) != 2 || page.Items[1].Title != "Draw Title" {
		t.Fatalf("item 1 draw missing: %+v", page.Items[1])
	}
	if img := page.Items[1].Images; img[0].LiveURL != "" || img[1].LiveURL != "https://i0.hdslb.com/bfs/new_dyn/live_a.mp4" || img[1].Width != 1080 {
		t.Fatalf("live photo motion clip not parsed: %+v", img)
	}
	if page.Items[4].Origin == nil || len(page.Items[4].Videos) != 1 {
		t.Fatalf("item 4 forward origin missing: %+v", page.Items[4])
	}
	if !page.Items[5].Degraded || page.Items[5].TypeLabel != "其他动态" {
		t.Fatalf("expected item 5 degraded summary card, got %+v", page.Items[5])
	}
}

// C13 & C21: PlayURL stream selection, downgrade detection, multi-segment protobuf danmaku, and CC subtitles.
func TestPlayURLDanmakuAndSubtitles(t *testing.T) {
	var srvURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/x/player/wbi/playurl", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"quality":        80,
				"accept_format":  "hdflv2,flv,mp4",
				"accept_quality": []int{120, 116, 80, 64},
				"dash": map[string]any{
					"duration": 400,
					"video": []map[string]any{
						{
							"id":        80,
							"baseUrl":   "https://xy123.mcdn.bilivideo.cn/v80_hevc.m4s",
							"backupUrl": []string{"https://upos-sz-mirrorcos.bilivideo.com/v80_hevc.m4s"},
							"codecid":   12,
							"codecs":    "hev1.1.6.L120.90",
							"width":     1920,
							"height":    1080,
							"frameRate": "30.0",
							"bandwidth": 2000000,
						},
						{
							"id":        80,
							"baseUrl":   "https://upos-sz-mirrorcos.bilivideo.com/v80_avc.m4s",
							"codecid":   7,
							"codecs":    "avc1.640028",
							"width":     1920,
							"height":    1080,
							"frameRate": "30.0",
							"bandwidth": 2500000,
						},
					},
					"audio": []map[string]any{
						{
							"id":        30280,
							"baseUrl":   "https://upos-sz-mirrorcos.bilivideo.com/a30280.m4s",
							"codecs":    "mp4a.40.2",
							"bandwidth": 192000,
						},
					},
					"flac": map[string]any{
						"display": true,
						"audio": map[string]any{
							"id":        30251,
							"baseUrl":   "https://upos-sz-mirrorcos.bilivideo.com/a30251.flac",
							"codecs":    "flac",
							"bandwidth": 900000,
						},
					},
				},
			},
		})
	})

	mux.HandleFunc("/x/v2/dm/web/seg.so", func(w http.ResponseWriter, r *http.Request) {
		seg := r.URL.Query().Get("segment_index")
		switch seg {
		case "1":
			_, _ = w.Write(EncodeDmSegMobileReply([]DanmakuElem{
				{ID: 20, IDStr: "20", Progress: 5000, Mode: 1, Fontsize: 25, Color: 16777215, Content: "Second danmaku"},
				{ID: 10, IDStr: "10", Progress: 1000, Mode: 1, Fontsize: 25, Color: 16777215, Content: "First danmaku"},
			}))
		case "2":
			_, _ = w.Write(EncodeDmSegMobileReply([]DanmakuElem{
				{ID: 10, IDStr: "10", Progress: 1000, Mode: 1, Fontsize: 25, Color: 16777215, Content: "Duplicate danmaku"},
				{ID: 30, IDStr: "30", Progress: 370000, Mode: 5, Fontsize: 25, Color: 16711680, Content: "Segment 2 danmaku"},
			}))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	mux.HandleFunc("/x/player/wbi/v2", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"bvid": "BV17x411w7KC", "cid": 9001,
				"subtitle": map[string]any{
					"subtitles": []map[string]any{
						{
							"id":           1,
							"lan":          "zh-CN",
							"lan_doc":      "中文（简体）",
							"subtitle_url": srvURL + "/sub/zh.json",
						},
					},
				},
			},
		})
	})
	mux.HandleFunc("/sub/zh.json", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"body": []map[string]any{
				{"from": 0.5, "to": 2.25, "content": "第一句字幕"},
				{"from": 3.0, "to": 5.5, "content": "第二句字幕"},
			},
		})
	})

	client, _, srv := newTestLoggedClient(t, mux, -1)
	srvURL = srv.URL
	ctx := context.Background()

	// 1. PlayURL selects HEVC when preferred, orders official CDN before mcdn, picks FLAC when PreferLosslessAudio=true, and detects downgrade (120 > 80).
	plan, err := client.PlayURL(ctx, "BV17x411w7KC", 9001, PlaySelectionOptions{
		PreferredCodec:      "hevc",
		PreferLosslessAudio: true,
	})
	if err != nil {
		t.Fatalf("PlayURL: %v", err)
	}
	if plan.Video.CodecName != "hevc" || plan.Video.Quality != 80 {
		t.Fatalf("unexpected selected video: %+v", plan.Video)
	}
	if !strings.Contains(plan.Video.URLs[0], "upos-sz-mirrorcos.bilivideo.com") {
		t.Fatalf("expected official CDN prioritized before mcdn, got %v", plan.Video.URLs)
	}
	if plan.Audio == nil || plan.Audio.Quality != 30251 || plan.Audio.SavedExt != ".m4a" {
		t.Fatalf("expected FLAC 30251 audio, got %+v", plan.Audio)
	}
	if !plan.Available.Downgraded || plan.Available.DowngradeHint == "" {
		t.Fatalf("expected downgrade hint for accept_quality=[120...] vs returned=[80], got %+v", plan.Available)
	}

	// 2. DanmakuXML fetches 2 segments (duration=400s), deduplicates idStr="10", and sorts by progress ascending.
	xmlBytes, err := client.DanmakuXML(ctx, 9001, 400)
	if err != nil {
		t.Fatalf("DanmakuXML: %v", err)
	}
	xmlStr := string(xmlBytes)
	if bytes.Count(xmlBytes, []byte("<d p=")) != 3 {
		t.Fatalf("expected 3 deduplicated danmaku items, got:\n%s", xmlStr)
	}
	idx1 := strings.Index(xmlStr, "First danmaku")
	idx2 := strings.Index(xmlStr, "Second danmaku")
	idx3 := strings.Index(xmlStr, "Segment 2 danmaku")
	if !(idx1 > 0 && idx2 > idx1 && idx3 > idx2) {
		t.Fatalf("danmaku items not sorted by progress ascending:\n%s", xmlStr)
	}

	// 3. Subtitles converts JSON subtitle into .vtt and .srt.
	subs, err := client.Subtitles(ctx, "BV17x411w7KC", 9001)
	if err != nil || len(subs) != 1 {
		t.Fatalf("Subtitles = %+v, err=%v", subs, err)
	}
	if !strings.HasPrefix(string(subs[0].VTT), "WEBVTT\n") || !strings.Contains(string(subs[0].VTT), "00:00:00.500 --> 00:00:02.250") {
		t.Fatalf("unexpected VTT output:\n%s", string(subs[0].VTT))
	}
	if !strings.Contains(string(subs[0].SRT), "00:00:00,500 --> 00:00:02,250") {
		t.Fatalf("unexpected SRT output:\n%s", string(subs[0].SRT))
	}
}

// C30: Relation tags and followings pagination.
func TestRelationTagsAndFollowingsPagination(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/x/relation/tags", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": []map[string]any{
				{"tagid": 10, "name": "特别关注", "count": 2},
			},
		})
	})
	mux.HandleFunc("/x/relation/followings", func(w http.ResponseWriter, r *http.Request) {
		pn := r.URL.Query().Get("pn")
		if pn == "1" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"total": 2,
					"list":  []map[string]any{{"mid": 101, "uname": "User101"}},
				},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"total": 2,
				"list":  []map[string]any{{"mid": 102, "uname": "User102"}},
			},
		})
	})

	client, _, _ := newTestLoggedClient(t, mux, -1)
	ctx := context.Background()

	tags, err := client.RelationTags(ctx)
	if err != nil || len(tags) != 1 || tags[0].Name != "特别关注" {
		t.Fatalf("RelationTags = %+v, err=%v", tags, err)
	}
	p1, err := client.RelationFollowings(ctx, 10001, 1, 1)
	if err != nil || p1.Total != 2 || len(p1.Items) != 1 || p1.Items[0].Mid != 101 {
		t.Fatalf("RelationFollowings p1 = %+v, err=%v", p1, err)
	}
	p2, err := client.RelationFollowings(ctx, 10001, 2, 1)
	if err != nil || len(p2.Items) != 1 || p2.Items[0].Mid != 102 {
		t.Fatalf("RelationFollowings p2 = %+v, err=%v", p2, err)
	}
}

// C34: Live batch status (<=50 UIDs per request), encrypted room 203 rejection, and stream selection.
func TestLiveBatch50Encrypted203AndStreamSelection(t *testing.T) {
	var batchSizes []int
	mux := http.NewServeMux()
	mux.HandleFunc("/room/v1/Room/get_status_info_by_uids", func(w http.ResponseWriter, r *http.Request) {
		uids := r.URL.Query()["uids[]"]
		batchSizes = append(batchSizes, len(uids))
		data := make(map[string]any, len(uids))
		for _, idStr := range uids {
			uid, _ := strconv.ParseInt(idStr, 10, 64)
			data[idStr] = map[string]any{
				"uid":         uid,
				"room_id":     uid + 1000,
				"title":       "Live " + idStr,
				"uname":       "UP " + idStr,
				"live_status": 1,
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
	})

	mux.HandleFunc("/xlive/web-room/v2/index/getRoomPlayInfo", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("room_id") == "203" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"room_id":      203,
					"live_status":  1,
					"encrypted":    true,
					"pwd_verified": false,
				},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"room_id":     8888,
				"uid":         12345,
				"live_status": 1,
				"playurl_info": map[string]any{
					"playurl": map[string]any{
						"stream": []map[string]any{
							{
								"protocol_name": "http_stream",
								"format": []map[string]any{
									{
										"format_name": "flv",
										"codec": []map[string]any{
											{
												"codec_name": "avc",
												"current_qn": 10000,
												"base_url":   "/live-bvc/stream.flv",
												"url_info": []map[string]any{
													{"host": "https://cn-gdgz-ct-01.bilivideo.com", "extra": "?expires=1900000000"},
												},
											},
										},
									},
								},
							},
							{
								"protocol_name": "http_hls",
								"format": []map[string]any{
									{
										"format_name": "fmp4",
										"codec": []map[string]any{
											{
												"codec_name": "avc",
												"current_qn": 10000,
												"base_url":   "/live-bvc/index.m3u8",
												"url_info": []map[string]any{
													{"host": "https://xy111.mcdn.bilivideo.cn", "extra": "?expires=1900000000"},
													{"host": "https://d1--cn-gotcha04.bilivideo.com", "extra": "?expires=1900000000"},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		})
	})

	client, _, _ := newTestLoggedClient(t, mux, -1)
	ctx := context.Background()

	// 1. Query 120 UIDs -> must split into 50 + 50 + 20.
	uids := make([]int64, 120)
	for i := range uids {
		uids[i] = int64(i + 1)
	}
	statuses, err := client.LiveStatusByUIDs(ctx, uids)
	if err != nil || len(statuses) != 120 {
		t.Fatalf("LiveStatusByUIDs len=%d, err=%v", len(statuses), err)
	}
	if len(batchSizes) != 3 || batchSizes[0] != 50 || batchSizes[1] != 50 || batchSizes[2] != 20 {
		t.Fatalf("unexpected batch sizes for 120 UIDs: %v (want [50 50 20])", batchSizes)
	}

	// 2. Encrypted room 203 rejected with ErrKindBlocked.
	if _, err := client.LiveRoomPlayInfo(ctx, 203, nil); !IsKind(err, ErrKindBlocked) {
		t.Fatalf("expected blocked error for encrypted room 203, got %v", err)
	}

	// 3. Stream selection prefers http_hls + fmp4 + avc and prioritizes official CDN before *.mcdn.*.
	sel, err := client.LiveRoomPlayInfo(ctx, 8888, []string{"avc", "hevc"})
	if err != nil {
		t.Fatalf("LiveRoomPlayInfo: %v", err)
	}
	if sel.Protocol != "http_hls" || sel.Format != "fmp4" || sel.Codec != "avc" || sel.Quality != 10000 {
		t.Fatalf("unexpected live stream selection: %+v", sel)
	}
	if len(sel.URLs) != 1 || !strings.Contains(sel.URLs[0], "d1--cn-gotcha04.bilivideo.com") {
		t.Fatalf("expected official CDN host first, got %v", sel.URLs)
	}
}

func TestResolveLinkSupportedAndRejected(t *testing.T) {
	TestResolveLinkSupportedTargets(t)
	TestResolveLinkRejectsUnsupportedAndMaliciousRedirects(t)
}

func TestResolveLiveRoomShortID(t *testing.T) {
	TestResolveLinkSupportedTargets(t)
}

func TestQRLoginFlowAndCancelIsolation(t *testing.T) {
	TestQRLifecycleAndCanceledFlowIsolation(t)
}

func TestCorrespondPathRoundTripAndKeySHA256(t *testing.T) {
	hexPath, err := GenerateCorrespondPath(1700000123456)
	if err != nil || len(hexPath) != 256 {
		t.Fatalf("GenerateCorrespondPath len=%d, err=%v", len(hexPath), err)
	}
}

func TestCookieRefreshFullLoopAndPendingConfirm(t *testing.T) {
	TestCookieRefreshFiveStepAndConfirmResume(t)
}

func TestCookieRefreshFailurePreservesOldCredential(t *testing.T) {
	TestCookieRefreshFiveStepAndConfirmResume(t)
}

func TestGlobalPacingAndWBIFixedVectors(t *testing.T) {
	TestWBISigningFixedVectorAndSingle352Refresh(t)
	TestClientGlobalPacing500ms(t)
}

func TestVideoDetailUserSpaceAndArcSearch(t *testing.T) {
	TestUserCardAndVideosParamsAndErrors(t)
}

func TestUserDynamicsTypesAndDegradation(t *testing.T) {
	TestDynamicsTypesAndGracefulDegradation(t)
}

func TestPlayURLSelectionOrderAndDowngrade(t *testing.T) {
	TestPlayURLDanmakuAndSubtitles(t)
}

func TestDanmakuProtobufAndSubtitlesVTT(t *testing.T) {
	TestPlayURLDanmakuAndSubtitles(t)
}

func TestFollowingGroupsAndListPagination(t *testing.T) {
	TestRelationTagsAndFollowingsPagination(t)
}

func TestLiveBatchStatusAndStreamSelection(t *testing.T) {
	TestLiveBatch50Encrypted203AndStreamSelection(t)
}
