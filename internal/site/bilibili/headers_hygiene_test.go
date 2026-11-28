package bilibili

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/credential"
)

func TestBilibiliTransportsEnableHTTP2(t *testing.T) {
	c := NewClient(ClientOptions{PacingInterval: -1})
	apiTr, ok := c.apiClient.Transport.(*http.Transport)
	if !ok || apiTr == nil || !apiTr.ForceAttemptHTTP2 {
		t.Fatalf("apiClient Transport ForceAttemptHTTP2 = false (type=%T), want true", c.apiClient.Transport)
	}
	mediaTr, ok := c.mediaClient.Transport.(*http.Transport)
	if !ok || mediaTr == nil || !mediaTr.ForceAttemptHTTP2 {
		t.Fatalf("mediaClient Transport ForceAttemptHTTP2 = false (type=%T), want true", c.mediaClient.Transport)
	}
}

func TestBilibiliDynamicHeadersOriginAndNonChromiumGuard(t *testing.T) {
	var mu sync.Mutex
	seen := make(map[string]http.Header)
	record := func(key string, r *http.Request) {
		mu.Lock()
		seen[key] = r.Header.Clone()
		mu.Unlock()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/10001", func(w http.ResponseWriter, r *http.Request) {
		record("space_html", r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<html><script id="__RENDER_DATA__" type="application/json">%7B%22access_id%22%3A%22SYNTH_ACCESS_ID_1%22%7D</script></html>`)
	})
	mux.HandleFunc("/x/space/wbi/arc/search", func(w http.ResponseWriter, r *http.Request) {
		record("arc_search", r)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"list": map[string]any{"vlist": []any{}},
				"page": map[string]any{"pn": 1, "ps": 30, "count": 0},
			},
		})
	})
	mux.HandleFunc("/x/polymer/web-dynamic/v1/feed/space", func(w http.ResponseWriter, r *http.Request) {
		record("feed_space", r)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"has_more": false, "offset": "", "items": []any{}},
		})
	})
	mux.HandleFunc("/x/relation/followings", func(w http.ResponseWriter, r *http.Request) {
		record("followings", r)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"list": []any{}, "total": 0},
		})
	})
	mux.HandleFunc("/room/v1/Room/room_init", func(w http.ResponseWriter, r *http.Request) {
		record("live_room_init", r)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"room_id": 223344, "short_id": 223, "uid": 10001, "live_status": 0},
		})
	})
	mux.HandleFunc("/x/player/wbi/playurl", func(w http.ResponseWriter, r *http.Request) {
		record("playurl", r)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"quality": 64,
				"durl": []map[string]any{{
					"order": 1, "length": 1000, "size": 1024,
					"url": "https://upos-sz-mirrorcos.bilivideo.com/synth.mp4",
				}},
			},
		})
	})
	mux.HandleFunc("/x/player/wbi/v2", func(w http.ResponseWriter, r *http.Request) {
		record("player_v2", r)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"bvid":     "BV17x411w7KC",
				"cid":      556677,
				"subtitle": map[string]any{"subtitles": []any{}},
			},
		})
	})
	mux.HandleFunc("/x/v2/dm/web/seg.so", func(w http.ResponseWriter, r *http.Request) {
		record("danmaku_seg", r)
		_, _ = w.Write(EncodeDmSegMobileReply([]DanmakuElem{{ID: 1, Progress: 1000, Content: "hi"}}))
	})
	mux.HandleFunc("/b23/hygiene", func(w http.ResponseWriter, r *http.Request) {
		record("b23", r)
		w.Header().Set("Location", "https://www.bilibili.com/video/BV17x411w7KC")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/x/passport-login/web/cookie/info", func(w http.ResponseWriter, r *http.Request) {
		record("cookie_info", r)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"refresh": true, "timestamp": int64(1700000000123)},
		})
	})
	mux.HandleFunc("/correspond/1/", func(w http.ResponseWriter, r *http.Request) {
		record("correspond", r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<html><body><div id="1-name">SYNTH_REFRESH_CSRF</div></body></html>`)
	})
	mux.HandleFunc("/x/passport-login/web/cookie/refresh", func(w http.ResponseWriter, r *http.Request) {
		record("cookie_refresh", r)
		http.SetCookie(w, &http.Cookie{Name: "SESSDATA", Value: "SYNTH_SESS_NEW"})
		http.SetCookie(w, &http.Cookie{Name: "bili_jct", Value: "SYNTH_CSRF_NEW"})
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"status": 0, "refresh_token": "SYNTH_RT_NEW"},
		})
	})
	mux.HandleFunc("/x/web-interface/nav", func(w http.ResponseWriter, r *http.Request) {
		record("nav", r)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"isLogin": true,
				"mid":     10001,
				"uname":   "合成用户",
				"face":    "https://i0.hdslb.com/bfs/face/synth.jpg",
			},
		})
	})
	mux.HandleFunc("/x/passport-login/web/confirm/refresh", func(w http.ResponseWriter, r *http.Request) {
		record("confirm_refresh", r)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{}})
	})

	client, store, srv := newTestLoggedClient(t, mux, -1)
	if _, err := store.SaveQR(
		map[string]string{"SESSDATA": "SYNTH_SESS_OLD", "bili_jct": "SYNTH_CSRF_OLD", "DedeUserID": "10001"},
		"SYNTH_RT_OLD", 10001, "合成用户", "",
	); err != nil {
		t.Fatalf("SaveQR: %v", err)
	}

	ctx := context.Background()
	if _, err := client.UserVideos(ctx, 10001, 1, 30, "pubdate", ""); err != nil {
		t.Fatalf("UserVideos: %v", err)
	}
	if _, err := client.UserDynamics(ctx, 10001, ""); err != nil {
		t.Fatalf("UserDynamics: %v", err)
	}
	if _, err := client.RelationFollowings(ctx, 10001, 1, 20); err != nil {
		t.Fatalf("RelationFollowings: %v", err)
	}
	if _, err := client.RoomInit(ctx, 223); err != nil {
		t.Fatalf("RoomInit: %v", err)
	}
	if _, err := client.PlayURL(ctx, "BV17x411w7KC", 556677, PlaySelectionOptions{}); err != nil {
		t.Fatalf("PlayURL: %v", err)
	}
	if _, err := client.MissingSubtitlesAndChapters(ctx, "BV17x411w7KC", 556677, false, nil); err != nil {
		t.Fatalf("MissingSubtitlesAndChapters: %v", err)
	}
	if _, err := client.DanmakuXML(ctx, 556677, 120, "BV17x411w7KC"); err != nil {
		t.Fatalf("DanmakuXML: %v", err)
	}
	if _, err := client.ResolveLink(ctx, "https://b23.tv/hygiene"); err != nil {
		t.Fatalf("ResolveLink(b23): %v", err)
	}
	if _, err := client.CheckAndRefreshCookie(ctx); err != nil {
		t.Fatalf("CheckAndRefreshCookie: %v", err)
	}

	assertCommonChrome := func(key string, h http.Header) {
		t.Helper()
		if h == nil {
			t.Fatalf("missing recorded request for %s", key)
		}
		if h.Get("User-Agent") != DefaultUserAgent {
			t.Errorf("%s UA = %q, want %q", key, h.Get("User-Agent"), DefaultUserAgent)
		}
		if h.Get("Accept-Language") != "zh-CN,zh;q=0.9" {
			t.Errorf("%s Accept-Language = %q", key, h.Get("Accept-Language"))
		}
		if !strings.Contains(h.Get("Sec-Ch-Ua"), `"144"`) || h.Get("Sec-Ch-Ua-Mobile") != "?0" || h.Get("Sec-Ch-Ua-Platform") != `"Windows"` {
			t.Errorf("%s Sec-Ch-Ua headers = (%q, %q, %q)", key, h.Get("Sec-Ch-Ua"), h.Get("Sec-Ch-Ua-Mobile"), h.Get("Sec-Ch-Ua-Platform"))
		}
	}

	for _, k := range []string{"space_html", "arc_search", "feed_space", "followings", "live_room_init", "playurl", "player_v2", "danmaku_seg", "b23", "correspond", "cookie_refresh", "confirm_refresh"} {
		assertCommonChrome(k, seen[k])
	}

	// Dynamic Referer checks.
	for _, k := range []string{"space_html", "arc_search", "feed_space", "followings"} {
		if got := seen[k].Get("Referer"); got != "https://space.bilibili.com/10001/" {
			t.Errorf("%s Referer = %q, want https://space.bilibili.com/10001/", k, got)
		}
	}
	if got := seen["live_room_init"].Get("Referer"); got != DefaultLiveReferer {
		t.Errorf("live_room_init Referer = %q, want %q", got, DefaultLiveReferer)
	}
	for _, k := range []string{"playurl", "player_v2", "danmaku_seg"} {
		if got := seen[k].Get("Referer"); got != "https://www.bilibili.com/video/BV17x411w7KC/" {
			t.Errorf("%s Referer = %q, want video referer", k, got)
		}
	}

	// Accept & Sec-Fetch checks by request kind.
	for _, k := range []string{"space_html", "correspond", "b23"} {
		h := seen[k]
		if !strings.Contains(h.Get("Accept"), "text/html") {
			t.Errorf("%s Accept = %q, want HTML Accept", k, h.Get("Accept"))
		}
		if h.Get("Sec-Fetch-Mode") != "navigate" || h.Get("Sec-Fetch-Dest") != "document" || h.Get("Sec-Fetch-User") != "?1" {
			t.Errorf("%s navigation Sec-Fetch = (%q, %q, %q)", k, h.Get("Sec-Fetch-Mode"), h.Get("Sec-Fetch-Dest"), h.Get("Sec-Fetch-User"))
		}
	}
	if got := seen["b23"].Get("Referer"); got != "" {
		t.Errorf("b23 Referer = %q, want empty", got)
	}
	if got := seen["b23"].Get("Cookie"); got != "" {
		t.Errorf("b23 Cookie = %q, want empty", got)
	}
	if got := seen["b23"].Get("Sec-Fetch-Site"); got != "none" {
		t.Errorf("b23 Sec-Fetch-Site = %q, want none", got)
	}
	if got := seen["danmaku_seg"].Get("Accept"); got != "*/*" {
		t.Errorf("danmaku_seg Accept = %q, want */*", got)
	}
	for _, k := range []string{"arc_search", "feed_space", "followings", "live_room_init", "playurl", "player_v2"} {
		h := seen[k]
		if h.Get("Accept") != "application/json, text/plain, */*" {
			t.Errorf("%s Accept = %q", k, h.Get("Accept"))
		}
		if h.Get("Sec-Fetch-Mode") != "cors" || h.Get("Sec-Fetch-Dest") != "empty" {
			t.Errorf("%s Sec-Fetch = (%q, %q)", k, h.Get("Sec-Fetch-Mode"), h.Get("Sec-Fetch-Dest"))
		}
		if h.Get("Sec-Fetch-Site") != "same-site" {
			t.Errorf("%s Sec-Fetch-Site = %q, want same-site", k, h.Get("Sec-Fetch-Site"))
		}
		if h.Get("Origin") != "" {
			t.Errorf("GET %s should not send Origin, got %q", k, h.Get("Origin"))
		}
	}
	// POST refresh endpoints MUST include Origin: https://www.bilibili.com.
	for _, k := range []string{"cookie_refresh", "confirm_refresh"} {
		if got := seen[k].Get("Origin"); got != DefaultOrigin {
			t.Errorf("POST %s Origin = %q, want %q", k, got, DefaultOrigin)
		}
	}

	// Non-Chromium (Firefox) UA omits Sec-Ch-Ua* headers.
	ffStore, _, err := credential.Open(filepath.Join(t.TempDir(), "bili.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ffStore.SaveManual("SESSDATA=s1; bili_jct=c1; DedeUserID=10001", ""); err != nil {
		t.Fatal(err)
	}
	ffClient := NewClient(ClientOptions{
		CredStore:      ffStore,
		UserAgent:      "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:135.0) Gecko/20100101 Firefox/135.0",
		PacingInterval: -1,
		AllowLocalhost: true,
		Endpoints: Endpoints{
			APIBase:     srv.URL,
			LiveAPIBase: srv.URL,
		},
	})
	if _, err := ffClient.RoomInit(ctx, 223); err != nil {
		t.Fatalf("Firefox RoomInit: %v", err)
	}
	for _, k := range []string{"Sec-Ch-Ua", "Sec-Ch-Ua-Mobile", "Sec-Ch-Ua-Platform"} {
		if got := seen["live_room_init"].Get(k); got != "" {
			t.Fatalf("Firefox UA unexpectedly emitted %s=%q", k, got)
		}
	}
}

func TestBilibiliPacingJitterInjectable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"room_id": 223344, "short_id": 223, "uid": 10001, "live_status": 0},
		})
	}))
	defer srv.Close()

	store, _, err := credential.Open(filepath.Join(t.TempDir(), "bili.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveManual("SESSDATA=s1; bili_jct=c1; DedeUserID=10001", ""); err != nil {
		t.Fatal(err)
	}

	var jitterCalls int
	client := NewClient(ClientOptions{
		CredStore:       store,
		Endpoints:       Endpoints{LiveAPIBase: srv.URL},
		PacingInterval:  15 * time.Millisecond,
		PacingJitterMax: 25 * time.Millisecond,
		PacingJitterFunc: func(maxJitter time.Duration) time.Duration {
			jitterCalls++
			if maxJitter != 25*time.Millisecond {
				t.Errorf("maxJitter = %v, want 25ms", maxJitter)
			}
			return 20 * time.Millisecond
		},
		AllowLocalhost: true,
	})

	start := time.Now()
	if _, err := client.RoomInit(context.Background(), 223); err != nil {
		t.Fatalf("first RoomInit: %v", err)
	}
	if _, err := client.RoomInit(context.Background(), 223); err != nil {
		t.Fatalf("second RoomInit: %v", err)
	}
	elapsed := time.Since(start)
	if jitterCalls != 1 {
		t.Fatalf("jitterCalls = %d, want 1", jitterCalls)
	}
	if elapsed < 30*time.Millisecond {
		t.Fatalf("elapsed = %v, want >= 30ms (15ms base + 20ms injected jitter)", elapsed)
	}
}

func TestBilibiliPacingJitterDefaultsScaleWithCustomInterval(t *testing.T) {
	cases := []struct {
		name       string
		pacing     time.Duration
		jitter     time.Duration
		wantPacing time.Duration
		wantJitter time.Duration
	}{
		{"production_default", 0, 0, DefaultPacingMinWait, DefaultPacingJitterMax},
		{"custom_small_1ms", time.Millisecond, 0, time.Millisecond, 400 * time.Microsecond},
		{"custom_medium_100ms", 100 * time.Millisecond, 0, 100 * time.Millisecond, 40 * time.Millisecond},
		{"custom_large_1s_capped", time.Second, 0, time.Second, DefaultPacingJitterMax},
		{"disabled_pacing", -1, 0, -1, 0},
		{"disabled_jitter", 500 * time.Millisecond, -1, 500 * time.Millisecond, 0},
		{"explicit_jitter_honored", 15 * time.Millisecond, 25 * time.Millisecond, 15 * time.Millisecond, 25 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewClient(ClientOptions{
				PacingInterval:  tc.pacing,
				PacingJitterMax: tc.jitter,
			})
			if c.pacingInterval != tc.wantPacing {
				t.Errorf("pacingInterval = %v, want %v", c.pacingInterval, tc.wantPacing)
			}
			if c.pacingJitterMax != tc.wantJitter {
				t.Errorf("pacingJitterMax = %v, want %v", c.pacingJitterMax, tc.wantJitter)
			}
		})
	}
}
