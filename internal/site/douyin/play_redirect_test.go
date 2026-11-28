package douyin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chialecode/trace/internal/site"
)

func TestParseAwemeWorkPropagatesNeedSetToken(t *testing.T) {
	parse := func(raw string) *WorkDetail {
		t.Helper()
		var node rawAwemeNode
		if err := json.Unmarshal([]byte(raw), &node); err != nil {
			t.Fatal(err)
		}
		w, err := parseAwemeWork(&node, StreamSelectOptions{}, false, false)
		if err != nil {
			t.Fatal(err)
		}
		return w
	}

	// 1. Standalone video with need_set_token=true on bit_rate.
	vid := parse(`{
		"aweme_id": "7600000000000000010",
		"desc": "需要播放跳转的视频",
		"video": {
			"need_set_token": true,
			"bit_rate": [{
				"gear_name": "normal_1080_0",
				"quality_type": 10,
				"is_h265": 0,
				"bit_rate": 2000000,
				"play_addr": {
					"uri": "v0200fg10000synthvid",
					"width": 1080,
					"height": 1920,
					"url_list": [
						"https://v26-webf.douyinvod.com/clip.mp4?ply_type=3",
						"https://www.douyin.com/aweme/v1/play/?video_id=v0200fg10000synthvid&line=0"
					]
				}
			}]
		}
	}`)
	if vid.Video == nil || !vid.Video.NeedSetToken {
		t.Fatalf("video Video.NeedSetToken = false (stream=%+v), want true", vid.Video)
	}

	// 2. Mixed note with video page (need_set_token=true) and live-photo page (is_need_set_cookie=true)
	// plus a normal video page (neither flag set).
	note := parse(`{
		"aweme_id": "7600000000000000011",
		"desc": "混合图文",
		"images": [
			{
				"uri": "img_video_1",
				"width": 1080,
				"height": 1920,
				"url_list": ["https://p3-sign.douyinpic.com/p1.webp"],
				"clip_type": 4,
				"video": {
					"need_set_token": true,
					"play_addr": {
						"uri": "v0200fg10000synthpage1",
						"width": 1080,
						"height": 1920,
						"url_list": [
							"https://v26-web.douyinvod.com/p1.mp4",
							"https://www.douyin.com/aweme/v1/play/?video_id=v0200fg10000synthpage1&line=0"
						]
					}
				}
			},
			{
				"uri": "img_live_2",
				"width": 1080,
				"height": 1440,
				"url_list": ["https://p3-sign.douyinpic.com/p2.webp"],
				"clip_type": 3,
				"video": {
					"is_need_set_cookie": true,
					"bit_rate": [{
						"gear_name": "normal_720_0",
						"play_addr": {
							"uri": "v0200fg10000synthpage2",
							"url_list": [
								"https://v26-webf.douyinvod.com/p2.mp4",
								"https://www.douyin.com/aweme/v1/play/?video_id=v0200fg10000synthpage2&line=0"
							]
						}
					}]
				}
			},
			{
				"uri": "img_normal_3",
				"width": 1080,
				"height": 1440,
				"url_list": ["https://p3-sign.douyinpic.com/p3.webp"],
				"clip_type": 3,
				"video": {
					"play_addr": {
						"uri": "v0200fg10000synthpage3",
						"url_list": ["https://v3-web.douyinvod.com/p3.mp4"]
					}
				}
			}
		]
	}`)
	if note.Kind != WorkKindNote || len(note.Pages) != 3 {
		t.Fatalf("note kind=%q pages=%d, want note with 3 pages", note.Kind, len(note.Pages))
	}
	if note.Pages[0].VideoStream == nil || !note.Pages[0].VideoStream.NeedSetToken {
		t.Fatalf("page 1 VideoStream.NeedSetToken = false (%+v), want true", note.Pages[0].VideoStream)
	}
	if note.Pages[1].VideoStream == nil || !note.Pages[1].VideoStream.NeedSetToken {
		t.Fatalf("page 2 VideoStream.NeedSetToken = false (%+v), want true", note.Pages[1].VideoStream)
	}
	if note.Pages[2].VideoStream == nil || note.Pages[2].VideoStream.NeedSetToken {
		t.Fatalf("page 3 VideoStream.NeedSetToken = true (%+v), want false", note.Pages[2].VideoStream)
	}
}

func TestResolvePlayRedirectSecurityAndBehavior(t *testing.T) {
	ctx := context.Background()

	t.Run("resolves_cookieless_cdn_without_following_redirect", func(t *testing.T) {
		store := openTestDouyinCredStore(t, true)
		var reqCount atomic.Int32
		var sawCookie string
		var sawHost string
		var sawHeaders http.Header

		client := NewClient(ClientOptions{
			CredStore:   store,
			MinInterval: -1,
			Entropy:     newDeterministicEntropy(0x21),
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				reqCount.Add(1)
				sawHost = req.URL.Hostname()
				sawCookie = req.Header.Get("Cookie")
				sawHeaders = req.Header.Clone()
				return jsonHTTPResponse(http.StatusFound, "", map[string]string{
					"Location": "https://v5-gz-a.douyinvod.com/synthetic/clip.mp4?expires=1999999999",
				}), nil
			}),
		})

		got, err := client.ResolvePlayRedirect(ctx, "https://www.douyin.com/aweme/v1/play/?video_id=v0200fg10000synth&line=0")
		if err != nil {
			t.Fatalf("ResolvePlayRedirect unexpected error: %v", err)
		}
		if got != "https://v5-gz-a.douyinvod.com/synthetic/clip.mp4?expires=1999999999" {
			t.Fatalf("resolved = %q", got)
		}
		if reqCount.Load() != 1 {
			t.Fatalf("request count = %d, want 1 (must not follow redirect with Cookie)", reqCount.Load())
		}
		if sawHost != "www.douyin.com" || !strings.Contains(sawCookie, "sessionid=SYNTH_ACTIVE_SESSION_SEED") {
			t.Fatalf("unexpected request host=%q cookie=%q", sawHost, sawCookie)
		}
		if sawHeaders.Get("Accept") != "*/*" {
			t.Fatalf("Accept = %q, want */*", sawHeaders.Get("Accept"))
		}
		if sawHeaders.Get("Accept-Language") != "zh-CN,zh;q=0.9" {
			t.Fatalf("Accept-Language = %q, want zh-CN,zh;q=0.9", sawHeaders.Get("Accept-Language"))
		}
		if sawHeaders.Get("Referer") != DefaultReferer {
			t.Fatalf("Referer = %q, want %q", sawHeaders.Get("Referer"), DefaultReferer)
		}
		if sawHeaders.Get("Sec-Fetch-Mode") != "no-cors" || sawHeaders.Get("Sec-Fetch-Dest") != "video" {
			t.Fatalf("Sec-Fetch-Mode=%q Sec-Fetch-Dest=%q, want no-cors / video", sawHeaders.Get("Sec-Fetch-Mode"), sawHeaders.Get("Sec-Fetch-Dest"))
		}
		if sawHeaders.Get("Sec-Fetch-Site") != "same-origin" {
			t.Fatalf("Sec-Fetch-Site = %q, want same-origin", sawHeaders.Get("Sec-Fetch-Site"))
		}
		if sawHeaders.Get("Origin") != "" {
			t.Fatalf("Origin must be empty on ResolvePlayRedirect, got %q", sawHeaders.Get("Origin"))
		}
		if !strings.Contains(sawHeaders.Get("Sec-Ch-Ua"), `"144"`) {
			t.Fatalf("Sec-Ch-Ua = %q, want Chrome 144", sawHeaders.Get("Sec-Ch-Ua"))
		}
	})

	t.Run("rejects_bad_paths_watermark_hosts_and_unauthenticated_with_zero_requests", func(t *testing.T) {
		store := openTestDouyinCredStore(t, true)
		var reqCount atomic.Int32
		client := NewClient(ClientOptions{
			CredStore:   store,
			MinInterval: -1,
			Entropy:     newDeterministicEntropy(0x22),
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				reqCount.Add(1)
				return jsonHTTPResponse(http.StatusOK, "{}", nil), nil
			}),
		})

		blockedCases := []string{
			"https://www.douyin.com/aweme/v1/playwm/?video_id=1",
			"https://www.douyin.com/aweme/v1/play/?video_id=1&watermark=1",
			"https://www.douyin.com/aweme/v1/web/aweme/detail/?aweme_id=1",
			"https://evil.example.com/aweme/v1/play/?video_id=1",
			"http://www.douyin.com/aweme/v1/play/?video_id=1",
		}
		for _, u := range blockedCases {
			if _, err := client.ResolvePlayRedirect(ctx, u); !site.IsKind(err, site.ErrKindBlocked) {
				t.Errorf("ResolvePlayRedirect(%q) err = %v, want blocked", u, err)
			}
		}
		if reqCount.Load() != 0 {
			t.Fatalf("blocked cases sent %d requests, want 0", reqCount.Load())
		}

		// Unauthenticated client sends 0 requests.
		loggedOutStore := openTestDouyinCredStore(t, false)
		loggedOutClient := NewClient(ClientOptions{
			CredStore:   loggedOutStore,
			MinInterval: -1,
			Entropy:     newDeterministicEntropy(0x23),
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				reqCount.Add(1)
				return jsonHTTPResponse(http.StatusOK, "{}", nil), nil
			}),
		})
		if _, err := loggedOutClient.ResolvePlayRedirect(ctx, "https://www.douyin.com/aweme/v1/play/?video_id=1"); !site.IsKind(err, site.ErrKindAuthRequired) {
			t.Fatalf("unauthenticated ResolvePlayRedirect err = %v, want auth_required", err)
		}
		if reqCount.Load() != 0 {
			t.Fatalf("unauthenticated client sent %d requests, want 0", reqCount.Load())
		}
	})

	t.Run("classifies_auth_risk_unsafe_redirect_and_404", func(t *testing.T) {
		cases := []struct {
			name     string
			status   int
			location string
			wantKind string
		}{
			{"login_redirect", http.StatusFound, "https://www.douyin.com/passport/web/login", site.ErrKindAuthRequired},
			{"verify_redirect", http.StatusFound, "https://www.douyin.com/verify/captcha?id=1", site.ErrKindRiskControl},
			{"plain_http_location", http.StatusFound, "http://v5-gz-a.douyinvod.com/clip.mp4", site.ErrKindBlocked},
			{"ip_literal_location", http.StatusFound, "https://203.0.113.10/clip.mp4", site.ErrKindBlocked},
			{"non_443_location", http.StatusFound, "https://v5-gz-a.douyinvod.com:8443/clip.mp4", site.ErrKindBlocked},
			{"local_host_location", http.StatusFound, "https://cdn.local/clip.mp4", site.ErrKindBlocked},
			{"not_found_404", http.StatusNotFound, "", site.ErrKindUnavailable},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				store := openTestDouyinCredStore(t, true)
				client := NewClient(ClientOptions{
					CredStore:   store,
					MinInterval: -1,
					Entropy:     newDeterministicEntropy(0x24),
					Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
						hdrs := map[string]string{}
						if tc.location != "" {
							hdrs["Location"] = tc.location
						}
						return jsonHTTPResponse(tc.status, "", hdrs), nil
					}),
				})
				_, err := client.ResolvePlayRedirect(ctx, "https://www.douyin.com/aweme/v1/play/?video_id=1&line=0")
				if !site.IsKind(err, tc.wantKind) {
					t.Fatalf("err = %v, want kind %s", err, tc.wantKind)
				}
			})
		}
	})
}
