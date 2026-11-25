package subscription

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
)

func TestAutoCacheSkipsUpowerLockedAndOnlyFansBlockedWithoutRetry(t *testing.T) {
	var step atomic.Int32
	var viewCalls atomic.Int32
	var playurlCalls atomic.Int32
	var dynDetailCalls atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/x/space/wbi/arc/search", func(w http.ResponseWriter, r *http.Request) {
		if step.Load() == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"list": map[string]any{
						"vlist": []map[string]any{
							{"bvid": "BV1base000001", "aid": 1, "title": "Baseline", "created": 1700000100},
						},
					},
					"page": map[string]any{"pn": 1, "ps": 30, "count": 1},
				},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"list": map[string]any{
					"vlist": []map[string]any{
						{"bvid": "BV1upLock0001", "aid": 2, "title": "充电专属视频", "created": 1700000500},
						{"bvid": "BV1base000001", "aid": 1, "title": "Baseline", "created": 1700000100},
					},
				},
				"page": map[string]any{"pn": 1, "ps": 30, "count": 2},
			},
		})
	})
	mux.HandleFunc("/x/polymer/web-dynamic/v1/feed/space", func(w http.ResponseWriter, r *http.Request) {
		if step.Load() == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"has_more": false,
					"items": []map[string]any{
						{
							"id_str": "10000001",
							"type":   "DYNAMIC_TYPE_WORD",
							"modules": map[string]any{
								"module_author":  map[string]any{"mid": 1002, "name": "UP1002", "pub_ts": 1700000100},
								"module_dynamic": map[string]any{"desc": map[string]any{"text": "基线动态"}},
							},
						},
					},
				},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"has_more": false,
				"items": []map[string]any{
					{
						"id_str": "10000002",
						"type":   "DYNAMIC_TYPE_DRAW",
						"basic":  map[string]any{"is_only_fans": true},
						"modules": map[string]any{
							"module_author": map[string]any{"mid": 1002, "name": "UP1002", "pub_ts": 1700000600},
							"module_dynamic": map[string]any{
								"desc": map[string]any{"text": "充电专属图文动态"},
								"major": map[string]any{
									"type":    "MAJOR_TYPE_BLOCKED",
									"blocked": map[string]any{"hint_message": "成为充电会员后查看"},
								},
							},
						},
					},
					{
						"id_str": "10000001",
						"type":   "DYNAMIC_TYPE_WORD",
						"modules": map[string]any{
							"module_author":  map[string]any{"mid": 1002, "name": "UP1002", "pub_ts": 1700000100},
							"module_dynamic": map[string]any{"desc": map[string]any{"text": "基线动态"}},
						},
					},
				},
			},
		})
	})
	mux.HandleFunc("/x/web-interface/wbi/view", func(w http.ResponseWriter, r *http.Request) {
		viewCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"bvid":                r.URL.Query().Get("bvid"),
				"aid":                 2,
				"title":               "充电专属视频",
				"is_upower_exclusive": true,
				"is_upower_play":      false,
				"owner":               map[string]any{"mid": 1001, "name": "UP1001"},
				"pages":               []map[string]any{{"cid": 2001, "page": 1, "part": "P1", "duration": 60}},
			},
		})
	})
	mux.HandleFunc("/x/player/wbi/playurl", func(w http.ResponseWriter, r *http.Request) {
		playurlCalls.Add(1)
		t.Errorf("unexpected playurl request for locked upower video in auto-cache")
		w.WriteHeader(http.StatusForbidden)
	})
	mux.HandleFunc("/x/polymer/web-dynamic/v1/detail", func(w http.ResponseWriter, r *http.Request) {
		dynDetailCalls.Add(1)
		t.Errorf("unexpected dynamic detail request for blocked onlyfans snapshot")
		w.WriteHeader(http.StatusForbidden)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cacheDir := filepath.Join(tmp, "cache")
	lib, err := library.Open(db, cacheDir)
	if err != nil {
		t.Fatalf("library.Open: %v", err)
	}
	cred, _, err := credential.Open(filepath.Join(tmp, "cred.dpapi"))
	if err != nil {
		t.Fatalf("credential.Open: %v", err)
	}
	_, _ = cred.SaveManual("SESSDATA=s; bili_jct=c; DedeUserID=1001", "rt")

	client := bilibili.NewClient(bilibili.ClientOptions{
		CredStore:      cred,
		Endpoints:      bilibili.Endpoints{APIBase: srv.URL},
		PacingInterval: -1,
		AllowLocalhost: true,
	})
	client.SetWBIKeys("7cd084941338484aae1ad9425b84077c", "4932caff0ff746eab6f01bf08b70ac45")

	dlMgr, err := download.NewManager(db, lib, client, settings.Default(cacheDir).Download, nil)
	if err != nil {
		t.Fatalf("download.NewManager: %v", err)
	}
	subCfg := settings.Default(cacheDir).Subscription
	subCfg.AutoCacheScope = "both"
	mgr, err := NewManager(db, lib, dlMgr, client, subCfg, nil)
	if err != nil {
		t.Fatalf("subscription.NewManager: %v", err)
	}
	mgr.SetAccountInterval(0)

	if _, err := mgr.Upsert(Subscription{Mid: 1001, Uname: "UP1001", Enabled: true, Scope: "video", AutoCache: "video"}); err != nil {
		t.Fatalf("Upsert video sub: %v", err)
	}
	if _, err := mgr.Upsert(Subscription{Mid: 1002, Uname: "UP1002", Enabled: true, Scope: "dynamic", AutoCache: "dynamic"}); err != nil {
		t.Fatalf("Upsert dynamic sub: %v", err)
	}

	ctx := context.Background()
	step.Store(1)
	if _, err := mgr.CheckNow(ctx, 0); err != nil {
		t.Fatalf("CheckNow baseline: %v", err)
	}

	// Second check detects the locked upower video and blocked onlyfans dynamic.
	step.Store(2)
	sum2, err := mgr.CheckNow(ctx, 0)
	if err != nil {
		t.Fatalf("CheckNow incremental: %v", err)
	}
	if sum2.AutoCacheTasks != 0 {
		t.Fatalf("expected 0 auto-cache tasks created for locked items, got %+v", sum2)
	}
	if playurlCalls.Load() != 0 || dynDetailCalls.Load() != 0 {
		t.Fatalf("expected 0 playurl and 0 dynamic detail calls, got playurl=%d detail=%d", playurlCalls.Load(), dynDetailCalls.Load())
	}
	if viewCalls.Load() != 1 {
		t.Fatalf("expected exactly 1 view call for upower check, got %d", viewCalls.Load())
	}

	// Verify both items were marked "forbidden" so subsequent auto-cache evaluations do not retry them.
	created, backlog, err := mgr.evaluateAutoCacheRulesForSites(ctx, nil)
	if err != nil {
		t.Fatalf("re-evaluate auto-cache: %v", err)
	}
	if created != 0 || backlog != 0 {
		t.Fatalf("expected 0 created and 0 backlog on re-eval, got created=%d backlog=%d", created, backlog)
	}
	if viewCalls.Load() != 1 {
		t.Fatalf("expected locked upower video not to be retried on re-eval, got viewCalls=%d", viewCalls.Load())
	}
}
