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
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/sitecheck"
)

func TestCategoryAllowlistBlocksDisallowedEndpointsBeforeGate(t *testing.T) {
	noopOK := func(counter *atomic.Int64) http.RoundTripper {
		return roundTrip(func(r *http.Request) (*http.Response, error) {
			counter.Add(1)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       http.NoBody,
			}, nil
		})
	}

	t.Run("act019 only permits nav and dynamic_detail", func(t *testing.T) {
		gate, err := sitecheck.OpenGate(t.TempDir(), sitecheck.Limits{
			ActID: act019ID, MaxRuns: 1, MaxAPICalls: act019MaxAPI, MaxByCategory: act019APILimits,
		})
		if err != nil {
			t.Fatal(err)
		}
		var upstream atomic.Int64
		ck := &checker{
			gate:      gate,
			apiLimits: act019APILimits,
			vod:       &mediaBudget{name: "cdn", maxRequests: 1, maxBytes: 1024},
			live:      &mediaBudget{name: "live"},
			statuses:  map[string]int{},
		}
		rt := ck.wrap(noopOK(&upstream))

		disallowed := []string{
			"https://api.bilibili.com/x/web-interface/view",
			"https://api.bilibili.com/x/polymer/web-dynamic/v1/feed/space",
			"https://api.bilibili.com/x/player/wbi/playurl",
			"https://api.bilibili.com/x/space/wbi/arc/search",
			"https://api.bilibili.com/x/unknown/endpoint",
		}
		for _, rawURL := range disallowed {
			req, _ := http.NewRequest(http.MethodGet, rawURL, nil)
			if _, err := rt.RoundTrip(req); !errors.Is(err, sitecheck.ErrQuotaExceeded) {
				t.Fatalf("act019 %s: expected ErrQuotaExceeded, got %v", rawURL, err)
			}
		}
		if upstream.Load() != 0 || gate.Usage().APICalls != 0 {
			t.Fatalf("disallowed requests hit upstream (%d) or ledger (%d)", upstream.Load(), gate.Usage().APICalls)
		}

		for _, rawURL := range []string{
			"https://api.bilibili.com/x/web-interface/nav",
			"https://api.bilibili.com/x/polymer/web-dynamic/v1/detail",
		} {
			req, _ := http.NewRequest(http.MethodGet, rawURL, nil)
			if _, err := rt.RoundTrip(req); err != nil {
				t.Fatalf("act019 allowed %s failed: %v", rawURL, err)
			}
		}
		if upstream.Load() != 2 || gate.Usage().APICalls != 2 {
			t.Fatalf("expected 2 allowed calls, got upstream=%d ledger=%d", upstream.Load(), gate.Usage().APICalls)
		}
	})

	t.Run("act006 and act007 block dynamic_detail", func(t *testing.T) {
		for _, id := range []string{"act006", "act007"} {
			q := acts[id]
			gate, err := sitecheck.OpenGate(t.TempDir(), sitecheck.Limits{
				ActID: id, MaxRuns: q.maxRuns, MaxAPICalls: q.maxAPI, MaxByCategory: q.api,
			})
			if err != nil {
				t.Fatal(err)
			}
			var upstream atomic.Int64
			ck := &checker{
				gate:      gate,
				apiLimits: q.api,
				vod:       &mediaBudget{name: "vod", maxRequests: 1, maxBytes: 1024},
				live:      &mediaBudget{name: "live", maxRequests: 1, maxBytes: 1024},
				statuses:  map[string]int{},
			}
			rt := ck.wrap(noopOK(&upstream))
			req, _ := http.NewRequest(http.MethodGet, "https://api.bilibili.com/x/polymer/web-dynamic/v1/detail", nil)
			if _, err := rt.RoundTrip(req); !errors.Is(err, sitecheck.ErrQuotaExceeded) {
				t.Fatalf("%s dynamic_detail: expected ErrQuotaExceeded, got %v", id, err)
			}
			if upstream.Load() != 0 || gate.Usage().APICalls != 0 {
				t.Fatalf("%s disallowed dynamic_detail hit upstream (%d) or ledger (%d)", id, upstream.Load(), gate.Usage().APICalls)
			}
		}
	})

	t.Run("act012 and act014 block reflow_info", func(t *testing.T) {
		for _, fu := range []liveFollowup{act012Followup, act014Followup} {
			gate, err := sitecheck.OpenGate(t.TempDir(), sitecheck.Limits{
				ActID: fu.id, MaxRuns: 1, MaxAPICalls: fu.maxAPICalls, MaxByCategory: fu.apiLimits,
			})
			if err != nil {
				t.Fatal(err)
			}
			var upstream atomic.Int64
			ck := &act010Checker{
				gate:           gate,
				apiLimits:      fu.apiLimits,
				vod:            &mediaBudget{name: "vod"},
				live:           &mediaBudget{name: "live", maxRequests: fu.maxLiveReqs, maxBytes: act010MaxLiveBytes},
				hls:            &mediaBudget{name: "hls_playlist", maxRequests: fu.maxHLSReqs, maxBytes: act011MaxHLSBytes},
				statuses:       map[string]int{},
				apiStatusCodes: map[string]map[int]int{},
			}
			rt := ck.wrap(noopOK(&upstream))
			for _, rawURL := range []string{
				"https://webcast.amemv.com/webcast/room/reflow/info/",
				"https://www.douyin.com/aweme/v1/web/aweme/post/",
				"https://www.douyin.com/aweme/v1/web/user/profile/other/",
				"https://www.douyin.com/aweme/v1/web/aweme/detail/",
			} {
				req, _ := http.NewRequest(http.MethodGet, rawURL, nil)
				if _, err := rt.RoundTrip(req); !errors.Is(err, sitecheck.ErrQuotaExceeded) {
					t.Fatalf("%s %s: expected ErrQuotaExceeded, got %v", fu.id, rawURL, err)
				}
			}
			if upstream.Load() != 0 || gate.Usage().APICalls != 0 {
				t.Fatalf("%s disallowed calls hit upstream (%d) or ledger (%d)", fu.id, upstream.Load(), gate.Usage().APICalls)
			}
		}
	})

	t.Run("act015-act018 block aweme_post web_enter profile_other reflow_info hls_playlist", func(t *testing.T) {
		gate, err := sitecheck.OpenGate(t.TempDir(), sitecheck.Limits{
			ActID: act015ID, MaxRuns: 1, MaxAPICalls: act015MaxAPICalls, MaxByCategory: act015APILimits,
		})
		if err != nil {
			t.Fatal(err)
		}
		var upstream atomic.Int64
		ck := &act010Checker{
			gate:           gate,
			apiLimits:      act015APILimits,
			vod:            &mediaBudget{name: "vod", maxRequests: act015MaxVODRequests, maxBytes: act015MaxVODBytes},
			live:           &mediaBudget{name: "live"},
			statuses:       map[string]int{},
			apiStatusCodes: map[string]map[int]int{},
		}
		rt := ck.wrap(noopOK(&upstream))
		for _, rawURL := range []string{
			"https://www.douyin.com/aweme/v1/web/aweme/post/",
			"https://live.douyin.com/webcast/room/web/enter/",
			"https://www.douyin.com/aweme/v1/web/user/profile/other/",
			"https://webcast.amemv.com/webcast/room/reflow/info/",
			"https://pull-hls.douyincdn.com/live/origin.m3u8",
		} {
			req, _ := http.NewRequest(http.MethodGet, rawURL, nil)
			if _, err := rt.RoundTrip(req); !errors.Is(err, sitecheck.ErrQuotaExceeded) {
				t.Fatalf("act015 %s: expected ErrQuotaExceeded, got %v", rawURL, err)
			}
		}
		if upstream.Load() != 0 || gate.Usage().APICalls != 0 {
			t.Fatalf("act015 disallowed calls hit upstream (%d) or ledger (%d)", upstream.Load(), gate.Usage().APICalls)
		}
	})

	t.Run("act020 blocks categories not enabled in apiLimits", func(t *testing.T) {
		restricted := map[string]int{"bili_nav": 1}
		gate, err := sitecheck.OpenGate(t.TempDir(), sitecheck.Limits{
			ActID: act020ID, MaxRuns: 1, MaxAPICalls: 5, MaxByCategory: restricted,
		})
		if err != nil {
			t.Fatal(err)
		}
		var upstream atomic.Int64
		ck := &act020Checker{
			gate:      gate,
			apiLimits: restricted,
			stopped:   map[string]string{},
			statuses:  map[string]int{},
		}
		rt := ck.wrap(noopOK(&upstream))
		req, _ := http.NewRequest(http.MethodGet, "https://api.bilibili.com/x/v3/fav/resource/list", nil)
		if _, err := rt.RoundTrip(req); !errors.Is(err, sitecheck.ErrQuotaExceeded) {
			t.Fatalf("act020 disallowed category: expected ErrQuotaExceeded, got %v", err)
		}
		if upstream.Load() != 0 || gate.Usage().APICalls != 0 {
			t.Fatalf("act020 disallowed call hit upstream (%d) or ledger (%d)", upstream.Load(), gate.Usage().APICalls)
		}
	})
}

func TestLiveFLVReconnectRetryAfterRedirectedAttemptFailure(t *testing.T) {
	env := setupAct010MockEnv(t, true)
	var originHits atomic.Int64

	// Attempt 1: 302 redirect to a local edge that fails with HTTP 500, consuming 2 FLV requests
	// (== act010MaxLiveConns). Previously the reconnect condition checked `< act010MaxLiveConns`
	// instead of `< fu.maxLiveReqs` (4), preventing the authorized reconnect spare from firing.
	// Attempt 2 (after web_enter refresh): 302 redirect to the healthy /cdn/live/edge.flv (2 more
	// FLV requests, total 4 == fu.maxLiveReqs).
	env.liveFLVHook = func(w http.ResponseWriter, r *http.Request) bool {
		n := originHits.Add(1)
		if n == 1 {
			w.Header().Set("Location", "/cdn/live/origin.flv?stage=fail_edge")
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusFound)
			_, _ = w.Write([]byte(tengine302Body))
			return true
		}
		if r.URL.Query().Get("stage") == "fail_edge" {
			w.Header().Set("Content-Type", "video/x-flv")
			w.WriteHeader(http.StatusBadGateway)
			return true
		}
		w.Header().Set("Location", "/cdn/live/edge.flv?expire=2&sign=synth_refreshed")
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusFound)
		_, _ = w.Write([]byte(tengine302Body))
		return true
	}

	outPath := filepath.Join(env.rootDir, "artifacts", "site-check", "act014.json")
	rc, err := runLiveFollowup(context.Background(), act014Followup, act010Config{
		RootDir:        env.rootDir,
		OutPath:        outPath,
		RecordDuration: 5 * time.Second,
		MinSpacing:     -1,
		Endpoints:      env.endpoints,
		AllowLocalhost: true,
	})
	if err != nil {
		t.Fatalf("runLiveFollowup(act014) should succeed via reconnect spare: %v (steps=%+v)", err, rc.Steps)
	}
	if !rc.Passed || rc.StoppedBy != "" {
		t.Fatalf("unexpected receipt state: passed=%v stoppedBy=%q", rc.Passed, rc.StoppedBy)
	}
	if rc.APIUsage.Categories["web_enter"] != 2 {
		t.Fatalf("expected 2 web_enter calls (initial + reconnect spare), got %d", rc.APIUsage.Categories["web_enter"])
	}
	if rc.LiveMedia.Requests != 4 {
		t.Fatalf("expected 4 FLV requests (2 failed redirected + 2 succeeded redirected), got %d", rc.LiveMedia.Requests)
	}
	v07, _ := rc.Verifications["V-07"].(map[string]any)
	if v07["status"] != "completed" || v07["moovBeforeMdat"] != true {
		t.Fatalf("unexpected V-07 verification: %+v", v07)
	}
}

func TestProbeOrDownloadMP4Rejects200OnNonZeroRangeOffset(t *testing.T) {
	mp4Data := buildSyntheticProgressiveMP4(t, 2, (8<<20)+(256<<10)) // ~8.25 MiB -> 2 chunks

	t.Run("chunk 2 returns 200 OK instead of 206", func(t *testing.T) {
		var calls atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n := calls.Add(1)
			if n == 1 {
				serveBytesWithRange(w, r, "video/mp4", mp4Data)
				return
			}
			// Misbehaving origin ignores Range on chunk 2 and returns 200 OK with full file.
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(mp4Data)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mp4Data)
		}))
		t.Cleanup(srv.Close)

		ck := &act010Checker{
			vod:            &mediaBudget{name: "vod", maxRequests: 10, maxBytes: 64 << 20},
			live:           &mediaBudget{name: "live"},
			statuses:       map[string]int{},
			apiStatusCodes: map[string]map[int]int{},
		}
		spec := site.MediaTransportSpec{AllowLocalhost: true}
		_, err := probeOrDownloadMP4(context.Background(), srv.Client(), spec, t.TempDir(), "test.mp4",
			srv.URL+"/clip.mp4", int64(len(mp4Data)), 2.0, true, ck)
		if err == nil || !strings.Contains(err.Error(), "206") {
			t.Fatalf("expected error requiring HTTP 206 on offset > 0, got: %v", err)
		}
	})

	t.Run("large file tail probe returns 200 OK instead of 206", func(t *testing.T) {
		small := buildSyntheticProgressiveMP4(t, 2, 1024)
		large := newVirtualLargeMP4(t, small, 30<<20)
		var calls atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				large.serveRange(w, r, nil)
				return
			}
			w.Header().Set("Content-Type", "video/mp4")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(small)
		}))
		t.Cleanup(srv.Close)

		ck := &act010Checker{
			vod:            &mediaBudget{name: "vod", maxRequests: 10, maxBytes: 64 << 20},
			live:           &mediaBudget{name: "live"},
			statuses:       map[string]int{},
			apiStatusCodes: map[string]map[int]int{},
		}
		spec := site.MediaTransportSpec{AllowLocalhost: true}
		_, err := probeOrDownloadMP4(context.Background(), srv.Client(), spec, t.TempDir(), "large.mp4",
			srv.URL+"/large.mp4", 30<<20, 2.0, false, ck)
		if err == nil || !strings.Contains(err.Error(), "206") {
			t.Fatalf("expected error requiring HTTP 206 on tail probe, got: %v", err)
		}
	})
}

func TestSortMotionClipsByBytesNumeric(t *testing.T) {
	clips := []map[string]any{
		{"ext": ".mp4", "bytes": int64(100000)},
		{"ext": ".mp4", "bytes": int64(20000)},
		{"ext": ".mp4", "bytes": int64(1500000)},
		{"ext": ".mp4", "bytes": int64(9000)},
	}
	sortMotionClipsByBytes(clips)

	want := []int64{9000, 20000, 100000, 1500000}
	for i, w := range want {
		got := clipBytesInt64(clips[i])
		if got != w {
			t.Fatalf("clips[%d] bytes = %d, want %d (full=%v)", i, got, w, clips)
		}
	}
}

func TestUnifiedRedactionPreservesHTTPStatusAndTrailingText(t *testing.T) {
	cookie := "SESSDATA=secret_sess_987654; bili_jct=csrf_token_1234"
	msg := `bilibili [risk_control http=200]: GET "https://api.bilibili.com/x/v1?SESSDATA=secret_sess_987654&a=1": context deadline exceeded`
	got := redact(msg, cookie)
	want := `bilibili [risk_control http=200]: GET "[URL]": context deadline exceeded`
	if got != want {
		t.Fatalf("redact =\n  %q\nwant:\n  %q", got, want)
	}

	red := newAct010Redactor()
	red.addCookieHeader(synthCookieRaw)
	dyMsg := `douyin [risk_control http=200]: fetch https://www.douyin.com/aweme/v1/web/aweme/detail/?a_bogus=xyz123 failed after 2s`
	dyGot := red.scrubError(dyMsg)
	dyWant := `douyin [risk_control http=200]: fetch [URL] failed after 2s`
	if dyGot != dyWant {
		t.Fatalf("scrubError =\n  %q\nwant:\n  %q", dyGot, dyWant)
	}
}

func TestFirstCachedPartFileGuard(t *testing.T) {
	if _, err := firstCachedPartFile(library.ItemMetadata{}); err == nil {
		t.Fatal("expected error when item.Parts is empty")
	}
	if _, err := firstCachedPartFile(library.ItemMetadata{Parts: []library.VideoPartRecord{{File: ""}}}); err == nil {
		t.Fatal("expected error when first part file is empty")
	}
	got, err := firstCachedPartFile(library.ItemMetadata{Parts: []library.VideoPartRecord{{File: "P01.mp4"}}})
	if err != nil || got != "P01.mp4" {
		t.Fatalf("firstCachedPartFile = %q, %v; want P01.mp4, nil", got, err)
	}
}

func TestWriteCleanReceiptScansSecretsAndWritesAtomically(t *testing.T) {
	dir := t.TempDir()
	leakPath := filepath.Join(dir, "leaked.json")
	leakedReceipt := receipt{
		Act:   "ACT-TEST",
		Notes: []string{"https://www.douyin.com/api?a_bogus=signed12345"},
	}
	if err := writeCleanReceipt(leakPath, leakedReceipt); err == nil {
		t.Fatal("expected writeCleanReceipt to reject signed URL via sitecheck.ScanTextForSecrets")
	}
	if _, err := os.Stat(leakPath); !os.IsNotExist(err) {
		t.Fatal("leaked receipt must not be written to disk")
	}

	cookieLeakPath := filepath.Join(dir, "cookie-leaked.json")
	cookieReceipt := receipt{
		Act:   "ACT-TEST",
		Notes: []string{"raw=secret_sess_987654"},
	}
	if err := writeCleanReceipt(cookieLeakPath, cookieReceipt, func(raw string) error {
		return verifyCookieValuesAbsent(raw, "SESSDATA=secret_sess_987654", "SESSDATA")
	}); err == nil {
		t.Fatal("expected writeCleanReceipt to reject cookie secret value")
	}
	if _, err := os.Stat(cookieLeakPath); !os.IsNotExist(err) {
		t.Fatal("cookie-leaked receipt must not be written to disk")
	}

	cleanPath := filepath.Join(dir, "sub", "clean.json")
	cleanReceipt := receipt{Act: "ACT-TEST", Passed: true}
	if err := writeCleanReceipt(cleanPath, cleanReceipt); err != nil {
		t.Fatalf("writeCleanReceipt clean failed: %v", err)
	}
	raw, err := os.ReadFile(cleanPath)
	if err != nil {
		t.Fatalf("read written receipt: %v", err)
	}
	var parsed receipt
	if err := json.Unmarshal(raw, &parsed); err != nil || !parsed.Passed || parsed.Act != "ACT-TEST" {
		t.Fatalf("unexpected written receipt: %s (err=%v)", raw, err)
	}
}
