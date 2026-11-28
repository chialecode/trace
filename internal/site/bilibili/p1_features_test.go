package bilibili

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chialecode/trace/internal/site"
)

func TestUserVideosTIDWBISignAndTListSortingAndClamping(t *testing.T) {
	mixin := MixinKey("7cd084941338484aae1ad9425b84077c", "4932caff0ff746eab6f01bf08b70ac45")
	var seenQueries []url.Values
	mux := http.NewServeMux()
	mux.HandleFunc("/x/space/wbi/arc/search", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		seenQueries = append(seenQueries, q)

		// Verify WBI signature includes tid/order/keyword properly.
		gotRid := q.Get("w_rid")
		if gotRid == "" || q.Get("wts") == "" {
			t.Errorf("missing w_rid or wts in query: %v", q)
		}
		verifyParams := url.Values{}
		for k, vals := range q {
			if k == "w_rid" || k == "wts" || len(vals) == 0 {
				continue
			}
			verifyParams.Set(k, vals[0])
		}
		signed := SignWBIParams(verifyParams, mixin, 1700000000)
		if signed.Get("w_rid") != gotRid {
			t.Errorf("w_rid mismatch: got %q want %q for params %v", gotRid, signed.Get("w_rid"), verifyParams)
		}

		switch q.Get("tid") {
		case "36":
			// Test empty array tlist (Bilibili returns [] when empty).
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"list": map[string]any{
						"tlist": []any{},
						"vlist": []map[string]any{
							{"bvid": "BV1aa411c7m1", "aid": 11, "title": "知识分区结果", "play": 123, "length": "03:20", "created": 1700000100},
						},
					},
					"page": map[string]any{"pn": 1, "ps": 30, "count": 1},
				},
			})
		case "0":
			if q.Get("keyword") == "nullzones" {
				// Test null tlist.
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 0,
					"data": map[string]any{
						"list": map[string]any{
							"tlist": nil,
							"vlist": []any{},
						},
						"page": map[string]any{"pn": 1, "ps": 30, "count": 0},
					},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"list": map[string]any{
						"tlist": map[string]any{
							"36":  map[string]any{"tid": 36, "count": 12, "name": "知识"},
							"188": map[string]any{"tid": 188, "count": 12, "name": "科技"},
							"4":   map[string]any{"tid": 4, "count": int64(math.MaxInt32) + 9999, "name": "游戏"},
							"bad": map[string]any{"tid": 0, "count": 5, "name": "无效分区"},
						},
						"vlist": []map[string]any{
							{"bvid": "BV1bb411c7m2", "aid": 22, "title": "全部投稿结果", "play": int64(math.MaxInt32) + 50, "length": "05:00", "created": 1700000200},
						},
					},
					"page": map[string]any{"pn": 1, "ps": 30, "count": 1},
				},
			})
		default:
			t.Errorf("unexpected tid=%q", q.Get("tid"))
		}
	})

	client, _, _ := newTestLoggedClient(t, mux, -1)
	ctx := context.Background()

	// 1. Default tid=0, sort=click, keyword="教程"
	page1, err := client.UserVideos(ctx, 8888, 1, 30, "click", "教程", 0)
	if err != nil {
		t.Fatalf("UserVideos tid=0: %v", err)
	}
	if len(page1.Zones) != 3 {
		t.Fatalf("expected 3 valid zones, got %+v", page1.Zones)
	}
	// Sorted by count desc, then tid asc: 游戏 (clamped MaxInt32) -> 知识 (12, tid=36) -> 科技 (12, tid=188)
	if page1.Zones[0].TID != 4 || page1.Zones[0].Count != math.MaxInt32 || page1.Zones[0].Name != "游戏" {
		t.Fatalf("unexpected zone[0]: %+v", page1.Zones[0])
	}
	if page1.Zones[1].TID != 36 || page1.Zones[1].Count != 12 || page1.Zones[2].TID != 188 || page1.Zones[2].Count != 12 {
		t.Fatalf("expected tie-break by tid asc (36 before 188), got %+v", page1.Zones)
	}
	if len(page1.Items) != 1 || page1.Items[0].PlayCount != math.MaxInt32 {
		t.Fatalf("expected play count clamped to MaxInt32, got %+v", page1.Items)
	}
	if seenQueries[0].Get("order") != "click" || seenQueries[0].Get("keyword") != "教程" || seenQueries[0].Get("tid") != "0" {
		t.Fatalf("unexpected query params: %v", seenQueries[0])
	}

	// 2. Specific tid=36, sort=stow, tlist=[]
	page2, err := client.UserVideos(ctx, 8888, 1, 30, "stow", "", 36)
	if err != nil {
		t.Fatalf("UserVideos tid=36: %v", err)
	}
	if len(page2.Zones) != 0 || len(page2.Items) != 1 || page2.Items[0].BVID != "BV1aa411c7m1" {
		t.Fatalf("unexpected tid=36 response: %+v", page2)
	}
	if seenQueries[1].Get("order") != "stow" || seenQueries[1].Get("tid") != "36" {
		t.Fatalf("unexpected query params[1]: %v", seenQueries[1])
	}

	// 3. tlist=null tolerated cleanly
	page3, err := client.UserVideos(ctx, 8888, 1, 30, "invalid-order", "nullzones")
	if err != nil {
		t.Fatalf("UserVideos nullzones: %v", err)
	}
	if len(page3.Zones) != 0 {
		t.Fatalf("expected empty zones for null tlist, got %+v", page3.Zones)
	}
	if seenQueries[2].Get("order") != "pubdate" {
		t.Fatalf("expected invalid order normalized to pubdate, got %q", seenQueries[2].Get("order"))
	}
}

func TestMissingSubtitlesAndChaptersZeroExtraRequests(t *testing.T) {
	var playerV2Calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/x/player/wbi/v2", func(w http.ResponseWriter, r *http.Request) {
		playerV2Calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"bvid": "BV17x411w7KC",
				"cid":  5001,
				"subtitle": map[string]any{
					"subtitles": []map[string]any{
						{"lan": "zh-CN", "lan_doc": "中文", "subtitle_url": ""},
					},
				},
				"view_points": []map[string]any{
					{"type": 2, "from": 120, "to": 260, "content": "  第二章 实战演示  "},
					{"type": 2, "from": 0, "to": 120, "content": "第一章 开场导览"},
					{"type": 2, "from": 0, "to": 120, "content": "第一章 开场导览"}, // duplicate
					{"type": 2, "from": -5, "to": 10, "content": "非法负起点"},    // invalid from < 0
					{"type": 2, "from": 50, "to": 50, "content": "零长度章节"},    // invalid to <= from
					{"type": 2, "from": 260, "to": 300, "content": "   "},    // invalid empty content
				},
			},
		})
	})

	client, _, _ := newTestLoggedClient(t, mux, -1)
	extras, err := client.MissingSubtitlesAndChapters(context.Background(), "BV17x411w7KC", 5001, false, nil)
	if err != nil {
		t.Fatalf("MissingSubtitlesAndChapters: %v", err)
	}
	if playerV2Calls.Load() != 1 {
		t.Fatalf("expected exactly 1 /x/player/wbi/v2 request, got %d", playerV2Calls.Load())
	}
	if len(extras.Chapters) != 2 {
		t.Fatalf("expected 2 valid deduplicated chapters, got %+v", extras.Chapters)
	}
	if extras.Chapters[0] != (VideoChapter{FromSec: 0, ToSec: 120, Title: "第一章 开场导览"}) {
		t.Fatalf("unexpected chapter[0]: %+v", extras.Chapters[0])
	}
	if extras.Chapters[1] != (VideoChapter{FromSec: 120, ToSec: 260, Title: "第二章 实战演示"}) {
		t.Fatalf("unexpected chapter[1]: %+v", extras.Chapters[1])
	}
}

func TestMissingSubtitlesAndChaptersSkipSubtitleBodiesReturnsChapters(t *testing.T) {
	var playerV2Calls, subtitleBodyCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/subtitle/zh.json", func(w http.ResponseWriter, r *http.Request) {
		subtitleBodyCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"body": []map[string]any{{"from": 0.0, "to": 1.5, "content": "不应下载正文"}},
		})
	})
	var srvURL string
	mux.HandleFunc("/x/player/wbi/v2", func(w http.ResponseWriter, r *http.Request) {
		playerV2Calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"bvid": "BV17x411w7KC",
				"cid":  5002,
				"subtitle": map[string]any{
					"subtitles": []map[string]any{
						{"lan": "zh-CN", "lan_doc": "中文", "subtitle_url": srvURL + "/subtitle/zh.json"},
					},
				},
				"view_points": []map[string]any{
					{"type": 2, "from": 0, "to": 45, "content": "开场"},
					{"type": 2, "from": 45, "to": 120, "content": "正片"},
				},
			},
		})
	})

	client, _, srv := newTestLoggedClient(t, mux, -1)
	srvURL = srv.URL
	extras, err := client.MissingSubtitlesAndChapters(context.Background(), "BV17x411w7KC", 5002, true, nil, false)
	if err != nil {
		t.Fatalf("MissingSubtitlesAndChapters(fetchSubtitleBodies=false): %v", err)
	}
	if playerV2Calls.Load() != 1 {
		t.Fatalf("expected 1 /x/player/wbi/v2 call, got %d", playerV2Calls.Load())
	}
	if subtitleBodyCalls.Load() != 0 {
		t.Fatalf("expected 0 subtitle body downloads when fetchSubtitleBodies=false, got %d", subtitleBodyCalls.Load())
	}
	if len(extras.Subtitles) != 0 {
		t.Fatalf("expected 0 subtitle tracks returned when fetchSubtitleBodies=false, got %+v", extras.Subtitles)
	}
	if len(extras.Chapters) != 2 || extras.Chapters[0].Title != "开场" || extras.Chapters[1].Title != "正片" {
		t.Fatalf("expected 2 chapters returned, got %+v", extras.Chapters)
	}
}

func TestVideoDetailUpowerFlagsAndLockedHelper(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/x/web-interface/wbi/view", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("bvid") {
		case "BV1locked1111":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"bvid":                "BV1locked1111",
					"aid":                 101,
					"title":               "充电专属未解锁视频",
					"is_upower_exclusive": true,
					"is_upower_play":      false,
					"is_upower_preview":   true,
					"owner":               map[string]any{"mid": 888, "name": "UP主"},
					"pages":               []map[string]any{{"cid": 1001, "page": 1, "part": "P1", "duration": 60}},
				},
			})
		case "BV1unlock2222":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"bvid":                "BV1unlock2222",
					"aid":                 102,
					"title":               "充电专属已解锁视频",
					"is_upower_exclusive": 1,
					"is_upower_play":      1,
					"is_upower_preview":   0,
					"owner":               map[string]any{"mid": 888, "name": "UP主"},
					"pages":               []map[string]any{{"cid": 1002, "page": 1, "part": "P1", "duration": 60}},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	client, _, _ := newTestLoggedClient(t, mux, -1)
	ctx := context.Background()

	locked, err := client.VideoDetail(ctx, "BV1locked1111")
	if err != nil {
		t.Fatalf("VideoDetail locked: %v", err)
	}
	if !locked.IsUpowerExclusive || locked.IsUpowerPlay || !locked.IsUpowerPreview {
		t.Fatalf("unexpected locked flags: %+v", locked)
	}
	if !locked.IsUpowerLocked() || locked.IsUpowerUnlocked() {
		t.Fatalf("expected IsUpowerLocked=true and IsUpowerUnlocked=false")
	}
	upErr := NewUpowerLockedError("BV1locked1111")
	if site.KindOf(upErr) != site.ErrKindForbidden {
		t.Fatalf("expected ErrKindForbidden, got %v", upErr)
	}

	unlocked, err := client.VideoDetail(ctx, "BV1unlock2222")
	if err != nil {
		t.Fatalf("VideoDetail unlocked: %v", err)
	}
	if !unlocked.IsUpowerExclusive || !unlocked.IsUpowerPlay || unlocked.IsUpowerPreview {
		t.Fatalf("unexpected unlocked flags: %+v", unlocked)
	}
	if unlocked.IsUpowerLocked() || !unlocked.IsUpowerUnlocked() {
		t.Fatalf("expected IsUpowerUnlocked=true and IsUpowerLocked=false")
	}
}

func TestDynamicOnlyFansBlockedParsingAndFeatures(t *testing.T) {
	var seenFeedFeatures, seenDetailFeatures string
	mux := http.NewServeMux()
	blockedItem := map[string]any{
		"id_str": "999888777666",
		"type":   "DYNAMIC_TYPE_DRAW",
		"basic": map[string]any{
			"is_only_fans": true,
		},
		"modules": map[string]any{
			"module_author": map[string]any{
				"mid":      888,
				"name":     "充电UP",
				"pub_ts":   1700001234,
				"pub_time": "2026-01-01",
			},
			"module_dynamic": map[string]any{
				"desc": map[string]any{"text": "充电专属预告说明"},
				"major": map[string]any{
					"type": "MAJOR_TYPE_BLOCKED",
					"blocked": map[string]any{
						"hint_message": "成为高档位充电会员后即可查看完整图文",
						"sub_title":    "充电专属动态",
					},
				},
			},
		},
	}
	mux.HandleFunc("/x/polymer/web-dynamic/v1/feed/space", func(w http.ResponseWriter, r *http.Request) {
		seenFeedFeatures = r.URL.Query().Get("features")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"has_more": false,
				"items":    []any{blockedItem},
			},
		})
	})
	mux.HandleFunc("/x/polymer/web-dynamic/v1/detail", func(w http.ResponseWriter, r *http.Request) {
		seenDetailFeatures = r.URL.Query().Get("features")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"item": blockedItem,
			},
		})
	})

	client, _, _ := newTestLoggedClient(t, mux, -1)
	ctx := context.Background()

	page, err := client.UserDynamics(ctx, 888, "")
	if err != nil {
		t.Fatalf("UserDynamics: %v", err)
	}
	if !strings.Contains(seenFeedFeatures, "onlyfansAssetsV2") {
		t.Fatalf("expected feed features to contain onlyfansAssetsV2, got %q", seenFeedFeatures)
	}
	if len(page.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(page.Items))
	}
	it := page.Items[0]
	if !it.IsOnlyFans || !it.Blocked || it.BlockedHint != "成为高档位充电会员后即可查看完整图文" {
		t.Fatalf("unexpected onlyfans blocked item: %+v", it)
	}
	if it.PubTs != 1700001234 || it.Text != "充电专属预告说明" || it.Title != "充电专属预告说明" {
		t.Fatalf("expected outer pub_ts/text/title preserved, got %+v", it)
	}
	if it.Degraded {
		t.Fatalf("blocked onlyfans dynamic should not be marked Degraded")
	}

	det, err := client.DynamicDetail(ctx, "999888777666")
	if err != nil {
		t.Fatalf("DynamicDetail: %v", err)
	}
	if !strings.Contains(seenDetailFeatures, "onlyfansAssetsV2") {
		t.Fatalf("expected detail features to contain onlyfansAssetsV2, got %q", seenDetailFeatures)
	}
	if !det.IsOnlyFans || !det.Blocked || det.BlockedHint != "成为高档位充电会员后即可查看完整图文" || det.Degraded {
		t.Fatalf("unexpected detail item: %+v", det)
	}
}
