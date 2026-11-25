package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/site/bilibili"
)

func TestUserVideosPageOptionsAndCacheQueueSkipsUpowerAndBlocked(t *testing.T) {
	var seenOrder, seenKeyword, seenTID string
	var playurlCalls atomic.Int32
	var dynDetailCalls atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/x/web-interface/nav", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"isLogin": true, "mid": 778899, "uname": "测试账号",
				"wbi_img": map[string]any{
					"img_url": "https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png",
					"sub_url": "https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png",
				},
			},
		})
	})
	mux.HandleFunc("/x/space/wbi/arc/search", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		seenOrder = q.Get("order")
		seenKeyword = q.Get("keyword")
		seenTID = q.Get("tid")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"list": map[string]any{
					"tlist": map[string]any{
						"36":  map[string]any{"tid": 36, "name": "知识", "count": 5},
						"188": map[string]any{"tid": 188, "name": "科技", "count": 12},
					},
					"vlist": []map[string]any{
						{"bvid": "BV1upLock1111", "aid": 11, "title": "充电专属测试", "play": 100, "length": "02:00", "created": 1700000200},
					},
				},
				"page": map[string]any{"pn": 1, "ps": 30, "count": 1},
			},
		})
	})
	mux.HandleFunc("/x/web-interface/wbi/view", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"bvid":                r.URL.Query().Get("bvid"),
				"aid":                 11,
				"title":               "充电专属测试",
				"is_upower_exclusive": true,
				"is_upower_play":      false,
				"owner":               map[string]any{"mid": 888, "name": "Creator888"},
				"pages":               []map[string]any{{"cid": 1101, "page": 1, "part": "P1", "duration": 60}},
			},
		})
	})
	mux.HandleFunc("/x/player/wbi/playurl", func(w http.ResponseWriter, r *http.Request) {
		playurlCalls.Add(1)
		t.Errorf("unexpected playurl call in cache queue for locked upower video")
		w.WriteHeader(http.StatusForbidden)
	})
	mux.HandleFunc("/x/polymer/web-dynamic/v1/detail", func(w http.ResponseWriter, r *http.Request) {
		dynDetailCalls.Add(1)
		t.Errorf("unexpected dynamic detail call in cache queue for blocked onlyfans dynamic")
		w.WriteHeader(http.StatusForbidden)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	clock := newFakeQueueClock(time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC))
	tmp := t.TempDir()
	loc, err := appdirs.ResolveWithOptions(appdirs.Options{
		Channel:   "dev",
		ForceMode: "portable",
		DataDir:   filepath.Join(tmp, "trace-data"),
		CacheDir:  filepath.Join(tmp, "trace-cache"),
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewBackend(Options{
		Locations:        loc,
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
		CacheQueueClock:  clock,
		CacheQueueRand:   func(int) int { return 0 },
		BilibiliOpts: bilibili.ClientOptions{
			Endpoints: bilibili.Endpoints{
				APIBase: srv.URL, PassportBase: srv.URL, WWWBase: srv.URL,
				SpaceBase: srv.URL, DynamicBase: srv.URL, LiveAPIBase: srv.URL, LiveWebBase: srv.URL,
			},
			PacingInterval: time.Millisecond,
			AllowLocalhost: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	if _, err := b.ImportCookie(context.Background(), "SESSDATA=s; bili_jct=j; DedeUserID=778899", ""); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	// 1. UserVideosPage forwards order/keyword/tid and returns sorted Zones.
	page, err := b.UserVideosPage(ctx, 888, 1, UserVideosPageOptions{
		Order:   "stow",
		Keyword: "实战",
		TID:     36,
	})
	if err != nil {
		t.Fatalf("UserVideosPage: %v", err)
	}
	if seenOrder != "stow" || seenKeyword != "实战" || seenTID != "36" {
		t.Fatalf("unexpected query params: order=%q keyword=%q tid=%q", seenOrder, seenKeyword, seenTID)
	}
	if len(page.Zones) != 2 || page.Zones[0].TID != 188 || page.Zones[1].TID != 36 {
		t.Fatalf("expected zones sorted count desc (188 then 36), got %+v", page.Zones)
	}

	// 2. Slow cache queue skips locked upower video cleanly (0 playurl calls, probedNoTask).
	vidCand := b.videoItemQueueCandidate(bilibili.UserVideoItem{BVID: "BV1upLock1111", Title: "充电专属测试"}, 888)
	enq, err := vidCand.enqueue(ctx)
	if err != nil {
		t.Fatalf("videoItemQueueCandidate enqueue should cleanly skip locked upower without error, got %v", err)
	}
	if !enq.probedNoTask || len(enq.taskIDs) != 0 {
		t.Fatalf("expected probedNoTask=true and 0 tasks for locked upower video, got %+v", enq)
	}
	if playurlCalls.Load() != 0 {
		t.Fatalf("expected 0 playurl calls, got %d", playurlCalls.Load())
	}

	// 3. Slow cache queue marks blocked onlyfans dynamic uncacheable (0 detail requests).
	dynCand := b.dynamicQueueCandidate(bilibili.DynamicItem{
		ID:          "888999",
		IsOnlyFans:  true,
		Blocked:     true,
		BlockedHint: "成为充电会员后查看",
	})
	if !dynCand.uncacheable {
		t.Fatalf("expected blocked onlyfans dynamic to be marked uncacheable in cache queue")
	}
	if dynDetailCalls.Load() != 0 {
		t.Fatalf("expected 0 dynamic detail calls, got %d", dynDetailCalls.Load())
	}
}
