package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/sitecheck"
)

func setupAct030Root(t *testing.T, rawCookie string, videoMid, dynamicMid int64) string {
	t.Helper()
	root := t.TempDir()
	secDir := filepath.Join(root, ".local", "site-secrets")
	checkDir := filepath.Join(root, ".local", "site-check")
	if err := os.MkdirAll(secDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(checkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cookiePath := filepath.Join(secDir, "bilibili.cookie")
	if err := os.WriteFile(cookiePath, []byte(rawCookie), 0o600); err != nil {
		t.Fatal(err)
	}
	envLine := "TRACE_TEST_BILIBILI_COOKIE_FILE=.local/site-secrets/bilibili.cookie\n"
	if err := os.WriteFile(filepath.Join(root, ".env.local"), []byte(envLine), 0o600); err != nil {
		t.Fatal(err)
	}
	tgBytes, err := json.Marshal(targets{
		LiveRoom:   998877,
		DynamicMid: dynamicMid,
		VideoMid:   videoMid,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkDir, "targets-act007.json"), tgBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestAct030LimitsAndFailClosedAllowlist(t *testing.T) {
	sum := 0
	for _, n := range act030Limits {
		sum += n
	}
	if sum != act030MaxAPI || act030MaxAPI != 10 || act030MaxRuns != 1 {
		t.Fatalf("unexpected act030 quota constants: sum=%d maxAPI=%d maxRuns=%d", sum, act030MaxAPI, act030MaxRuns)
	}

	gate, err := sitecheck.OpenGate(t.TempDir(), sitecheck.Limits{
		ActID:         act030ID,
		MaxRuns:       act030MaxRuns,
		MaxAPICalls:   act030MaxAPI,
		MaxMediaFiles: 0,
		MaxMediaBytes: 0,
		MaxByCategory: act030Limits,
	})
	if err != nil {
		t.Fatal(err)
	}
	var upstream atomic.Int64
	ck := &act030Checker{
		gate:      gate,
		apiLimits: act030Limits,
		statuses:  map[string]int{},
		lastTel:   map[string]act030RespTelemetry{},
	}
	rt := ck.wrap(roundTrip(func(r *http.Request) (*http.Response, error) {
		upstream.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: http.NoBody}, nil
	}))

	// Disallowed endpoints (media CDN, playurl, danmaku, dynamic detail, live) must be rejected before ledger & network.
	for _, rawURL := range []string{
		"https://i0.hdslb.com/bfs/archive/cover.jpg",
		"https://aisubtitle.hdslb.com/bfs/ai_subtitle/prod/sub.json",
		"https://upos-sz-mirrorcos.bilivideo.com/upgcxcode/video.m4s",
		"https://api.bilibili.com/x/player/wbi/playurl?bvid=BV1xx411c7mD&cid=123",
		"https://api.bilibili.com/x/v2/dm/wbi/web/seg.so?type=1&oid=123",
		"https://api.bilibili.com/x/polymer/web-dynamic/v1/detail?id=999",
		"https://api.live.bilibili.com/room/v1/Room/room_init?id=123",
	} {
		req, _ := http.NewRequest(http.MethodGet, rawURL, nil)
		if _, err := rt.RoundTrip(req); !errors.Is(err, sitecheck.ErrQuotaExceeded) {
			t.Fatalf("%s: expected ErrQuotaExceeded, got %v", rawURL, err)
		}
	}
	if upstream.Load() != 0 || gate.Usage().APICalls != 0 {
		t.Fatalf("disallowed requests hit upstream (%d) or ledger (%d)", upstream.Load(), gate.Usage().APICalls)
	}
}

func TestAct030HappyPathZeroLeakAndQuotaCap(t *testing.T) {
	const (
		rawCookie    = "SESSDATA=synth_bili_sess_act030_secret; bili_jct=synth_bili_jct_act030; DedeUserID=55667788"
		videoMid     = int64(23456789)
		dynamicMid   = int64(34567890)
		sampleBVID   = "BV1p1Act030Ab"
		sampleCID    = int64(88990011)
		secretTitle  = "【测试】星海漫游实战演示"
		secretAuthor = "合成测试作者零三零"
		secretChap1  = "敏感章节看点一"
		secretChap2  = "敏感章节看点二"
	)

	var (
		cdnHits          atomic.Int64
		subtitleListHits atomic.Int64
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/x/web-interface/nav", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"isLogin": true,
				"mid":     55667788,
				"uname":   "敏感登录昵称零三零",
				"face":    "https://i0.hdslb.com/bfs/face/self_secret.jpg",
				"wbi_img": map[string]any{
					"img_url": "https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png",
					"sub_url": "https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png",
				},
			},
		})
	})
	mux.HandleFunc("/space/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<script id="__RENDER_DATA__" type="application/json">%7B%22access_id%22%3A%22synth_webid_030%22%7D</script>`))
	})
	mux.HandleFunc("/x/space/wbi/arc/search", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		tid := q.Get("tid")
		order := q.Get("order")
		keyword := q.Get("keyword")
		if q.Get("w_rid") == "" || q.Get("wts") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		tlist := map[string]any{
			"36":  map[string]any{"tid": 36, "name": "知识分区敏感名", "count": 4},
			"188": map[string]any{"tid": 188, "name": "科技分区敏感名", "count": 9},
		}
		switch {
		case tid == "188" && order == "click":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"list": map[string]any{
						"tlist": tlist,
						"vlist": []map[string]any{
							{"bvid": sampleBVID, "aid": 10030, "title": secretTitle, "author": secretAuthor, "mid": videoMid, "length": "05:20", "created": 1700000100, "play": 5000},
						},
					},
					"page": map[string]any{"pn": 1, "ps": 30, "count": 9},
				},
			})
		case keyword == "测试":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"list": map[string]any{
						"tlist": tlist,
						"vlist": []map[string]any{
							{"bvid": sampleBVID, "aid": 10030, "title": secretTitle, "author": secretAuthor, "mid": videoMid, "length": "05:20", "created": 1700000100, "play": 5000},
						},
					},
					"page": map[string]any{"pn": 1, "ps": 30, "count": 1},
				},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"list": map[string]any{
						"tlist": tlist,
						"vlist": []map[string]any{
							{"bvid": sampleBVID, "aid": 10030, "title": secretTitle, "author": secretAuthor, "mid": videoMid, "length": "05:20", "created": 1700000100, "play": 5000},
							{"bvid": "BV1p1Act030Cd", "aid": 10031, "title": "第二条合成视频", "author": secretAuthor, "mid": videoMid, "length": "03:10", "created": 1700000050, "play": 2000},
						},
					},
					"page": map[string]any{"pn": 1, "ps": 30, "count": 13},
				},
			})
		}
	})
	mux.HandleFunc("/x/web-interface/wbi/view", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"bvid":                sampleBVID,
				"aid":                 10030,
				"title":               secretTitle,
				"desc":                "敏感简介正文零三零",
				"pic":                 "https://i0.hdslb.com/bfs/archive/secret_cover.jpg",
				"pubdate":             1700000100,
				"duration":            320,
				"cid":                 sampleCID,
				"is_upower_exclusive": false,
				"is_upower_play":      false,
				"is_upower_preview":   false,
				"owner":               map[string]any{"mid": videoMid, "name": secretAuthor, "face": "https://i0.hdslb.com/bfs/face/owner.jpg"},
				"pages": []map[string]any{
					{"cid": sampleCID, "page": 1, "part": "P1 敏感分P名", "duration": 320},
				},
			},
		})
	})
	mux.HandleFunc("/x/player/wbi/v2", func(w http.ResponseWriter, r *http.Request) {
		subtitleListHits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"bvid": sampleBVID,
				"cid":  sampleCID,
				"subtitle": map[string]any{
					"subtitles": []map[string]any{
						{
							"id":           1,
							"lan":          "zh-CN",
							"lan_doc":      "中文敏感字幕标签",
							"subtitle_url": "http://" + r.Host + "/bfs/subtitle/body.json",
						},
					},
				},
				"view_points": []map[string]any{
					{"type": 2, "from": 120, "to": 240, "content": secretChap2},
					{"type": 2, "from": 0, "to": 120, "content": secretChap1},
				},
			},
		})
	})
	mux.HandleFunc("/bfs/subtitle/body.json", func(w http.ResponseWriter, r *http.Request) {
		cdnHits.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/x/polymer/web-dynamic/v1/feed/space", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Query().Get("features"), "onlyfansAssetsV2") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"has_more": true,
				"offset":   "900000000000000001",
				"items": []map[string]any{
					{
						"id_str": "900000000000000002",
						"type":   "DYNAMIC_TYPE_WORD",
						"basic":  map[string]any{"is_only_fans": true},
						"modules": map[string]any{
							"module_author": map[string]any{"mid": dynamicMid, "name": "动态作者敏感名", "pub_ts": 1700000200},
							"module_dynamic": map[string]any{
								"desc":  map[string]any{"text": "充电专属动态正文敏感词"},
								"major": map[string]any{"type": "MAJOR_TYPE_BLOCKED", "blocked": map[string]any{"hint_message": "成为充电会员后查看"}},
							},
						},
					},
					{
						"id_str": "900000000000000001",
						"type":   "DYNAMIC_TYPE_WORD",
						"modules": map[string]any{
							"module_author": map[string]any{"mid": dynamicMid, "name": "动态作者敏感名", "pub_ts": 1700000100},
							"module_dynamic": map[string]any{
								"desc": map[string]any{"text": "普通公开动态正文"},
							},
						},
					},
				},
			},
		})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	root := setupAct030Root(t, rawCookie, videoMid, dynamicMid)
	outPath := filepath.Join(root, "artifacts", "site-check", "act030.json")
	eps := bilibili.Endpoints{
		APIBase:      srv.URL,
		PassportBase: srv.URL,
		WWWBase:      srv.URL,
		SpaceBase:    srv.URL + "/space",
		DynamicBase:  srv.URL,
		LiveAPIBase:  srv.URL,
		LiveWebBase:  srv.URL,
	}

	rc, err := runAct030(context.Background(), act030Config{
		RootDir:        root,
		OutPath:        outPath,
		MinSpacing:     -1,
		Endpoints:      eps,
		AllowLocalhost: true,
	})
	if err != nil || !rc.Passed || !rc.SecretScanClean || rc.StoppedBy != "" {
		t.Fatalf("runAct030 happy path failed: err=%v passed=%v clean=%v stoppedBy=%q steps=%+v", err, rc.Passed, rc.SecretScanClean, rc.StoppedBy, rc.Steps)
	}
	if len(rc.Steps) != 7 {
		t.Fatalf("expected 7 steps, got %d", len(rc.Steps))
	}
	if rc.APIUsage.APICalls != 9 || rc.APIUsage.Categories["nav"] != 2 || rc.APIUsage.Categories["arc_search"] != 4 || rc.APIUsage.Categories["video_view"] != 1 || rc.APIUsage.Categories["subtitle_list"] != 1 || rc.APIUsage.Categories["dynamic_feed"] != 1 || subtitleListHits.Load() != 1 {
		t.Fatalf("unexpected API usage: total=%d cats=%+v subtitleHits=%d", rc.APIUsage.APICalls, rc.APIUsage.Categories, subtitleListHits.Load())
	}
	if cdnHits.Load() != 0 || rc.MediaUsage.Requests != 0 {
		t.Fatalf("expected 0 media/CDN hits, got cdnHits=%d media=%+v", cdnHits.Load(), rc.MediaUsage)
	}

	// Verify key step facts.
	stepByName := map[string]step{}
	for _, s := range rc.Steps {
		stepByName[s.Name] = s
	}
	if stepByName["p1_space_default_and_tlist"].Facts["zonesCount"] != 2 || stepByName["p1_space_default_and_tlist"].Facts["zonesSortedDesc"] != true {
		t.Fatalf("unexpected step 2 facts: %+v", stepByName["p1_space_default_and_tlist"].Facts)
	}
	if stepByName["p1_space_zone_and_sort"].Facts["worksTotalPreserved"] != true {
		t.Fatalf("step 3 did not preserve works total: %+v", stepByName["p1_space_zone_and_sort"].Facts)
	}
	if stepByName["p1_space_keyword_search"].Facts["worksTotalPreserved"] != true {
		t.Fatalf("step 4 did not preserve works total: %+v", stepByName["p1_space_keyword_search"].Facts)
	}
	if stepByName["p1_video_view_and_upower"].Facts["upowerFieldsParsed"] != true || stepByName["p1_video_view_and_upower"].Facts["hasCID"] != true {
		t.Fatalf("unexpected step 5 facts: %+v", stepByName["p1_video_view_and_upower"].Facts)
	}
	if stepByName["p1_zero_extra_req_chapters"].Facts["chaptersCount"] != 2 || stepByName["p1_zero_extra_req_chapters"].Facts["chaptersOrdered"] != true || stepByName["p1_zero_extra_req_chapters"].Facts["subtitlesCount"] != 1 {
		t.Fatalf("unexpected step 6 facts: %+v", stepByName["p1_zero_extra_req_chapters"].Facts)
	}
	if stepByName["p1_dynamic_onlyfans_flag"].Facts["onlyFansCount"] != 1 || stepByName["p1_dynamic_onlyfans_flag"].Facts["blockedMajorCount"] != 1 || stepByName["p1_dynamic_onlyfans_flag"].Facts["degradedCount"] != 0 {
		t.Fatalf("unexpected step 7 facts: %+v", stepByName["p1_dynamic_onlyfans_flag"].Facts)
	}

	// Verify zero sensitive leakage in written receipt JSON.
	rawBytes, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	rawJSON := string(rawBytes)
	for _, forbidden := range []string{
		"synth_bili_sess_act030_secret", "synth_bili_jct_act030", "55667788",
		"23456789", "34567890", sampleBVID, "88990011",
		secretTitle, secretAuthor, secretChap1, secretChap2,
		"测试", "敏感登录昵称零三零", "知识分区敏感名", "科技分区敏感名",
		"动态作者敏感名", "充电专属动态正文敏感词", "中文敏感字幕标签",
	} {
		if strings.Contains(rawJSON, forbidden) {
			t.Fatalf("act030 receipt leaked sensitive value %q:\n%s", forbidden, rawJSON)
		}
	}

	// Verify second run is blocked by persistent single-run ledger before network.
	if _, err2 := runAct030(context.Background(), act030Config{
		RootDir:        root,
		OutPath:        outPath,
		MinSpacing:     -1,
		Endpoints:      eps,
		AllowLocalhost: true,
	}); !errors.Is(err2, sitecheck.ErrQuotaExceeded) {
		t.Fatalf("second run err = %v, want ErrQuotaExceeded", err2)
	}
}

func TestAct030ImmediateStopOnHTTP412AndRiskCode352(t *testing.T) {
	const (
		rawCookie  = "SESSDATA=synth_bili_sess_stop; bili_jct=synth_bili_jct_stop; DedeUserID=55667799"
		videoMid   = int64(23456781)
		dynamicMid = int64(34567891)
	)

	t.Run("stops_immediately_on_bili_minus_352_without_wbi_retry", func(t *testing.T) {
		var navCalls, arcCalls atomic.Int64
		mux := http.NewServeMux()
		mux.HandleFunc("/x/web-interface/nav", func(w http.ResponseWriter, r *http.Request) {
			navCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"isLogin": true, "mid": 55667799, "uname": "测试号352",
					"wbi_img": map[string]any{
						"img_url": "https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png",
						"sub_url": "https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png",
					},
				},
			})
		})
		mux.HandleFunc("/space/", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`<html></html>`))
		})
		mux.HandleFunc("/x/space/wbi/arc/search", func(w http.ResponseWriter, r *http.Request) {
			arcCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": -352, "message": "风控校验失败"})
		})
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)

		root := setupAct030Root(t, rawCookie, videoMid, dynamicMid)
		outPath := filepath.Join(root, "artifacts", "site-check", "act030-352.json")
		rc, err := runAct030(context.Background(), act030Config{
			RootDir:    root,
			OutPath:    outPath,
			MinSpacing: -1,
			Endpoints: bilibili.Endpoints{
				APIBase: srv.URL, PassportBase: srv.URL, WWWBase: srv.URL,
				SpaceBase: srv.URL + "/space", DynamicBase: srv.URL, LiveAPIBase: srv.URL, LiveWebBase: srv.URL,
			},
			AllowLocalhost: true,
		})
		if err == nil || rc.Passed || rc.StoppedBy != "bili_-352" || !rc.SecretScanClean {
			t.Fatalf("expected immediate stop on -352, got err=%v passed=%v stoppedBy=%q clean=%v", err, rc.Passed, rc.StoppedBy, rc.SecretScanClean)
		}
		if navCalls.Load() != 2 || arcCalls.Load() != 1 {
			t.Fatalf("expected zero retries after -352 (2 initial nav: login+wbi, 1 arc_search): navCalls=%d arcCalls=%d", navCalls.Load(), arcCalls.Load())
		}
		// Remaining 5 steps must be skipped.
		skipped := 0
		for _, s := range rc.Steps {
			if s.Skipped {
				skipped++
			}
		}
		if skipped != 5 {
			t.Fatalf("expected 5 skipped steps after -352, got %d (steps=%+v)", skipped, rc.Steps)
		}
	})

	t.Run("stops_immediately_on_http_412", func(t *testing.T) {
		var arcCalls atomic.Int64
		mux := http.NewServeMux()
		mux.HandleFunc("/x/web-interface/nav", func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"isLogin": true, "mid": 55667799, "uname": "测试号412",
					"wbi_img": map[string]any{
						"img_url": "https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png",
						"sub_url": "https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png",
					},
				},
			})
		})
		mux.HandleFunc("/space/", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`<html></html>`))
		})
		mux.HandleFunc("/x/space/wbi/arc/search", func(w http.ResponseWriter, r *http.Request) {
			arcCalls.Add(1)
			w.WriteHeader(http.StatusPreconditionFailed)
		})
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)

		root := setupAct030Root(t, rawCookie, videoMid, dynamicMid)
		outPath := filepath.Join(root, "artifacts", "site-check", "act030-412.json")
		rc, err := runAct030(context.Background(), act030Config{
			RootDir:    root,
			OutPath:    outPath,
			MinSpacing: -1,
			Endpoints: bilibili.Endpoints{
				APIBase: srv.URL, PassportBase: srv.URL, WWWBase: srv.URL,
				SpaceBase: srv.URL + "/space", DynamicBase: srv.URL, LiveAPIBase: srv.URL, LiveWebBase: srv.URL,
			},
			AllowLocalhost: true,
		})
		if err == nil || rc.Passed || rc.StoppedBy != "http_412" || !rc.SecretScanClean {
			t.Fatalf("expected immediate stop on HTTP 412, got err=%v passed=%v stoppedBy=%q clean=%v", err, rc.Passed, rc.StoppedBy, rc.SecretScanClean)
		}
		if arcCalls.Load() != 1 {
			t.Fatalf("expected zero retries after HTTP 412: arcCalls=%d", arcCalls.Load())
		}
	})
}
