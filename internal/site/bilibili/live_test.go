package bilibili

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/chialecode/trace/internal/site"
)

func TestLivePlayInfoHTTPFLVFallbackAndCDNOrdering(t *testing.T) {
	client, _, _ := newTestLoggedClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/room/v1/Room/room_init":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"room_id":     9201,
					"uid":         10001,
					"live_status": 1,
					"live_time":   1700001000,
				},
			})
		case "/xlive/web-room/v2/index/getRoomPlayInfo":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"room_id":     9201,
					"uid":         10001,
					"live_status": 1,
					"playurl_info": map[string]any{
						"playurl": map[string]any{
							"g_qn_desc": []map[string]any{
								{"qn": 10000, "desc": "原画"},
							},
							"stream": []map[string]any{
								{
									"protocol_name": "http_stream",
									"format": []map[string]any{
										{
											"format_name": "flv",
											"codec": []map[string]any{
												{
													"codec_name": "hevc",
													"current_qn": 10000,
													"base_url":   "/live-bvc/stream_9201.flv",
													"url_info": []map[string]string{
														{"host": "https://xy123.mcdn.bilivideo.cn", "extra": "?tok=mcdn"},
														{"host": "https://cn-gdgz-fx-01.bilivideo.cn", "extra": "?tok=other"},
														{"host": "https://d1--cn-gotcha04.bilivideo.com", "extra": "?tok=official"},
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
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}), -1)

	sel, err := client.LiveRoomPlayInfo(context.Background(), 9201, []string{"avc", "hevc"})
	if err != nil {
		t.Fatalf("LiveRoomPlayInfo FLV fallback: %v", err)
	}
	if sel.Protocol != "http_stream" || sel.Format != "flv" || sel.Codec != "hevc" || sel.Quality != 10000 || sel.QualityName != "原画" {
		t.Fatalf("unexpected FLV selection: %+v", sel)
	}
	if len(sel.URLs) != 2 {
		t.Fatalf("expected 2 non-mcdn URLs, got %v", sel.URLs)
	}
	for _, u := range sel.URLs {
		if strings.Contains(u, ".mcdn.") {
			t.Fatalf("mcdn URL must be excluded: %s", u)
		}
	}
	if !strings.HasPrefix(sel.URLs[0], "https://cn-gdgz-fx-01.bilivideo.cn/") || !strings.HasPrefix(sel.URLs[1], "https://d1--cn-gotcha04.bilivideo.com/") {
		t.Fatalf("official CDN order should be preserved with mcdn stripped, got %v", sel.URLs)
	}

	adapter := NewAdapter(client)
	plan, err := adapter.SelectLiveStream(context.Background(), "10001", "9201", []string{"avc", "hevc"})
	if err != nil {
		t.Fatalf("Adapter.SelectLiveStream: %v", err)
	}
	if plan.Transport != site.LiveTransportHTTPFLV {
		t.Fatalf("expected LiveTransportHTTPFLV, got %q", plan.Transport)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://d1--cn-gotcha04.bilivideo.com/live-bvc/stream_9201.flv", nil)
	req.Header.Set("Cookie", "SESSDATA=secret")
	plan.Media.ApplyHeaders(req)
	if plan.Media.Referer != "https://live.bilibili.com/" || plan.Media.Origin != "https://live.bilibili.com" || req.Header.Get("Cookie") != "" {
		t.Fatalf("unexpected live media transport spec/headers: spec=%+v headers=%+v", plan.Media, req.Header)
	}
}

func TestLivePlayInfoPrefersHLSFMP4OverHTTPFLV(t *testing.T) {
	client, _, _ := newTestLoggedClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/room/v1/Room/room_init":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"room_id": 9202, "uid": 10001, "live_status": 1, "live_time": 1700002000},
			})
		case "/xlive/web-room/v2/index/getRoomPlayInfo":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"room_id":     9202,
					"uid":         10001,
					"live_status": 1,
					"playurl_info": map[string]any{
						"playurl": map[string]any{
							"g_qn_desc": []map[string]any{
								{"qn": 10000, "desc": "原画"},
								{"qn": 400, "desc": "蓝光"},
							},
							"stream": []map[string]any{
								{
									"protocol_name": "http_stream",
									"format": []map[string]any{{
										"format_name": "flv",
										"codec": []map[string]any{{
											"codec_name": "hevc",
											"current_qn": 10000,
											"base_url":   "/live-bvc/9202.flv",
											"url_info":   []map[string]string{{"host": "https://d1--cn-gotcha04.bilivideo.com", "extra": "?tok=flv"}},
										}},
									}},
								},
								{
									"protocol_name": "http_hls",
									"format": []map[string]any{{
										"format_name": "fmp4",
										"codec": []map[string]any{{
											"codec_name": "avc",
											"current_qn": 400,
											"base_url":   "/live-bvc/9202.m3u8",
											"url_info":   []map[string]string{{"host": "https://d1--cn-gotcha04.bilivideo.com", "extra": "?tok=hls"}},
										}},
									}},
								},
							},
						},
					},
				},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}), -1)

	sel, err := client.LiveRoomPlayInfo(context.Background(), 9202, []string{"hevc", "avc"})
	if err != nil {
		t.Fatalf("LiveRoomPlayInfo: %v", err)
	}
	if sel.Protocol != "http_hls" || sel.Format != "fmp4" {
		t.Fatalf("expected http_hls+fmp4 to beat http_stream+flv, got %+v", sel)
	}

	plan, err := NewAdapter(client).SelectLiveStream(context.Background(), "10001", "9202", []string{"hevc", "avc"})
	if err != nil {
		t.Fatalf("Adapter.SelectLiveStream: %v", err)
	}
	if plan.Transport != site.LiveTransportHLSFMP4 {
		t.Fatalf("expected LiveTransportHLSFMP4, got %q", plan.Transport)
	}
}

func TestLivePlayInfoRejectsUnsupportedFormatsAndMCDNOnly(t *testing.T) {
	for _, tc := range []struct {
		name    string
		streams []map[string]any
	}{
		{
			name: "hls ts only",
			streams: []map[string]any{{
				"protocol_name": "http_hls",
				"format": []map[string]any{{
					"format_name": "ts",
					"codec": []map[string]any{{
						"codec_name": "avc",
						"current_qn": 10000,
						"base_url":   "/live-bvc/9203.m3u8",
						"url_info":   []map[string]string{{"host": "https://d1--cn-gotcha04.bilivideo.com", "extra": "?tok=ts"}},
					}},
				}},
			}},
		},
		{
			name: "flv mcdn only",
			streams: []map[string]any{{
				"protocol_name": "http_stream",
				"format": []map[string]any{{
					"format_name": "flv",
					"codec": []map[string]any{{
						"codec_name": "avc",
						"current_qn": 10000,
						"base_url":   "/live-bvc/9203.flv",
						"url_info":   []map[string]string{{"host": "https://xy1.mcdn.bilivideo.cn", "extra": "?tok=mcdn"}},
					}},
				}},
			}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _, _ := newTestLoggedClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 0,
					"data": map[string]any{
						"room_id":     9203,
						"uid":         10001,
						"live_status": 1,
						"playurl_info": map[string]any{
							"playurl": map[string]any{"stream": tc.streams},
						},
					},
				})
			}), -1)

			_, err := client.LiveRoomPlayInfo(context.Background(), 9203, []string{"avc", "hevc"})
			if err == nil {
				t.Fatal("expected unavailable error, got nil")
			}
			if KindOf(err) != ErrKindUnavailable || !strings.Contains(err.Error(), "没有提供可录制的流格式") {
				t.Fatalf("unexpected error: kind=%q err=%v", KindOf(err), err)
			}
		})
	}
}
