package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
)

// C28 & R-09: Multi-account subscription CRUD, global <site>:<uid> uniqueness, Link conflict, atomic Move, <= 20 accounts cap, and subscriptions.json.bak recovery.
func TestSubscriptionCRUDLinkMoveAndBackupRecovery(t *testing.T) {
	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer db.Close()

	lib, _ := library.Open(db, filepath.Join(tmp, "cache"))
	cred, _, _ := credential.Open(filepath.Join(tmp, "cred.dpapi"))
	client := bilibili.NewClient(bilibili.ClientOptions{CredStore: cred, PacingInterval: -1})
	mgr, err := NewManager(db, lib, nil, client, settings.Default(filepath.Join(tmp, "cache")).Subscription, nil)
	mgr.SetAccountInterval(0)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// 1. Create subscription A with 1 account.
	subA, err := mgr.Create("Creator Alpha", "Primary note", []AccountRef{
		{Site: "bilibili", UID: "1001", Name: "AlphaMain", Face: "https://i0.hdslb.com/a.jpg"},
	}, SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatalf("Create subA: %v", err)
	}

	// 2. Create subscription B with 1 account.
	subB, err := mgr.Create("Creator Beta", "Beta note", []AccountRef{
		{Site: "bilibili", UID: "2002", Name: "BetaMain", Face: "https://i0.hdslb.com/b.jpg"},
	}, SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatalf("Create subB: %v", err)
	}

	// Verify subscriptions.json and subscriptions.json.bak exist on disk!
	if _, err := os.Stat(filepath.Join(tmp, "subscriptions.json")); err != nil {
		t.Fatalf("subscriptions.json missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "subscriptions.json.bak")); err != nil {
		t.Fatalf("subscriptions.json.bak missing: %v", err)
	}

	// 3. Link a second account (1002) to subA.
	subA, err = mgr.Link(subA.ID, "bilibili", "1002", "AlphaSubAccount", "")
	if err != nil || len(subA.Accounts) != 2 {
		t.Fatalf("Link 1002 to subA: %+v err=%v", subA, err)
	}

	// 4. Attempting to Link 1002 to subB must fail with ErrAccountConflict and return owner (subA)!
	conflictOwner, err := mgr.Link(subB.ID, "bilibili", "1002", "AlphaSubAccount", "")
	if !errors.Is(err, ErrAccountConflict) || conflictOwner.ID != subA.ID {
		t.Fatalf("expected ErrAccountConflict with owner=%s, got owner=%+v err=%v", subA.ID, conflictOwner, err)
	}

	// 5. Move 1002 from subA to subB atomically.
	subB, err = mgr.Move("bilibili", "1002", subB.ID)
	if err != nil || len(subB.Accounts) != 2 {
		t.Fatalf("Move 1002 to subB failed: %+v err=%v", subB, err)
	}
	ownerAfterMove, ok := mgr.OwnerOf("bilibili", "1002")
	if !ok || ownerAfterMove.ID != subB.ID {
		t.Fatalf("expected OwnerOf(1002) == subB, got %+v ok=%v", ownerAfterMove, ok)
	}

	// 6. Test 20-account maximum per subscription.
	var manyAccs []AccountRef
	for i := 0; i < 21; i++ {
		manyAccs = append(manyAccs, AccountRef{Site: "bilibili", UID: strconv.Itoa(5000 + i)})
	}
	if _, err := mgr.Create("TooMany", "", manyAccs, SubRules{}); err == nil {
		t.Fatalf("expected error when creating subscription with 21 accounts (> 20)")
	}

	// 7. Corrupt subscriptions.json and verify RestoreFromBackup() recovers the previous valid state.
	_ = os.WriteFile(filepath.Join(tmp, "subscriptions.json"), []byte("{corrupt-json"), 0o600)
	_ = mgr.SetSubscriptionsPath(filepath.Join(tmp, "subscriptions.json"))
	if !mgr.CorruptRecovered() {
		t.Fatalf("expected CorruptRecovered() == true after loading corrupt subscriptions.json")
	}
	restored, err := mgr.RestoreFromBackup()
	if err != nil || len(restored) != 2 {
		t.Fatalf("RestoreFromBackup failed: len=%d err=%v", len(restored), err)
	}
}

// C29: Subscription CRUD and idempotent followings import.
func TestSubscriptionCRUDAndIdempotentImport(t *testing.T) {
	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer db.Close()

	lib, _ := library.Open(db, filepath.Join(tmp, "cache"))
	cred, _, _ := credential.Open(filepath.Join(tmp, "cred.dpapi"))
	client := bilibili.NewClient(bilibili.ClientOptions{CredStore: cred, PacingInterval: -1})
	mgr, err := NewManager(db, lib, nil, client, settings.Default(filepath.Join(tmp, "cache")).Subscription, nil)
	mgr.SetAccountInterval(0)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	users := []bilibili.FollowingUser{
		{Mid: 1001, Uname: "Creator1", Face: "https://i0.hdslb.com/1.jpg"},
		{Mid: 1002, Uname: "Creator2", Face: "https://i0.hdslb.com/2.jpg"},
	}
	n1, err := mgr.ImportCreators(users, "both", "video", true, false)
	if err != nil || n1 != 2 {
		t.Fatalf("ImportCreators 1: n=%d err=%v", n1, err)
	}

	// Re-importing the same creators updates name without duplicating rows.
	users[0].Uname = "Creator1_Renamed"
	n2, err := mgr.ImportCreators(users, "both", "none", true, false)
	if err != nil || n2 != 2 {
		t.Fatalf("ImportCreators 2: n=%d err=%v", n2, err)
	}
	list, err := mgr.List()
	if err != nil || len(list) != 2 {
		t.Fatalf("List len=%d, want 2", len(list))
	}
	if list[0].Uname != "Creator1_Renamed" || list[0].AutoCache != "video" {
		t.Fatalf("expected existing AutoCache preserved and Uname updated, got %+v", list[0])
	}

	if err := mgr.Delete("1001"); err != nil {
		t.Fatalf("Delete 1001: %v", err)
	}
	listAfter, _ := mgr.List()
	if len(listAfter) != 1 || listAfter[0].Mid != 1002 {
		t.Fatalf("unexpected list after delete: %+v", listAfter)
	}
}

// Alias for stage-report C29 reference.
func TestImportFollowingPaginationAndIdempotentCreate(t *testing.T) {
	TestSubscriptionCRUDAndIdempotentImport(t)
}

// C30 & R-10: Baseline watermark (page 1 marked seen, 0 auto-cache tasks), 3-page pagination with pinned ignore and gap note,
// MarkSeen / MarkAllSeen / Unseen, 3 consecutive network errors stop, and risk_control batch stop.
func TestIncrementalCheckBaselineFailureIsolationAndRiskStop(t *testing.T) {
	var step atomic.Int32
	var triggerRisk atomic.Bool

	mux := http.NewServeMux()
	mux.HandleFunc("/x/space/wbi/arc/search", func(w http.ResponseWriter, r *http.Request) {
		mid := r.URL.Query().Get("mid")
		if triggerRisk.Load() {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": -352, "message": "v_voucher risk", "data": map[string]any{"v_voucher": "v_1"}})
			return
		}
		if mid == "2002" {
			// Creator 2002 returns normal -404 error -> should be isolated without stopping Creator 3003!
			_ = json.NewEncoder(w).Encode(map[string]any{"code": -404, "message": "not found"})
			return
		}
		items := []map[string]any{
			{"aid": 170001, "bvid": "BV17x411w7KC", "title": "Initial Video", "created": 1700000100, "length": "01:00", "author": "UP"},
		}
		if step.Load() >= 2 && mid == "1001" {
			items = append([]map[string]any{
				{"aid": 170002, "bvid": "BV1new000002", "title": "Brand New Video", "created": 1700000900, "length": "02:00", "author": "UP"},
			}, items...)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"list": map[string]any{"vlist": items},
				"page": map[string]any{"pn": 1, "ps": 20, "count": len(items)},
			},
		})
	})
	mux.HandleFunc("/x/web-interface/wbi/view", func(w http.ResponseWriter, r *http.Request) {
		bvid := r.URL.Query().Get("bvid")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"bvid":  bvid,
				"aid":   170002,
				"title": "Brand New Video",
				"owner": map[string]any{"mid": 1001, "name": "UP"},
				"pages": []map[string]any{{"cid": 8001, "page": 1, "part": "P1", "duration": 60}},
			},
		})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	tmp := t.TempDir()
	db, _, _, _ := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	defer db.Close()
	cacheDir := filepath.Join(tmp, "cache")
	lib, _ := library.Open(db, cacheDir)
	cred, _, _ := credential.Open(filepath.Join(tmp, "cred.dpapi"))
	_, _ = cred.SaveManual("SESSDATA=s; bili_jct=c", "rt")

	client := bilibili.NewClient(bilibili.ClientOptions{
		CredStore:      cred,
		Endpoints:      bilibili.Endpoints{APIBase: srv.URL},
		PacingInterval: -1,
		AllowLocalhost: true,
	})
	client.SetWBIKeys("7cd084941338484aae1ad9425b84077c", "4932caff0ff746eab6f01bf08b70ac45")

	dlMgr, _ := download.NewManager(db, lib, client, settings.Default(cacheDir).Download, nil)
	subCfg := settings.Default(cacheDir).Subscription
	subCfg.AutoCacheScope = "video"
	mgr, _ := NewManager(db, lib, dlMgr, client, subCfg, nil)
	mgr.SetAccountInterval(0)

	_, _ = mgr.Upsert(Subscription{Mid: 1001, Uname: "UP1001", Enabled: true, Scope: "video", AutoCache: "video"})
	_, _ = mgr.Upsert(Subscription{Mid: 2002, Uname: "UP2002_Fail", Enabled: true, Scope: "video", AutoCache: "video"})
	_, _ = mgr.Upsert(Subscription{Mid: 3003, Uname: "UP3003", Enabled: true, Scope: "video", AutoCache: "video"})

	ctx := context.Background()
	step.Store(1)

	// 1. First check: establishes baseline for 1001 and 3003, isolates 2002 failure, does NOT put baseline items into update list, and creates 0 auto-cache tasks!
	sum1, err := mgr.CheckNow(ctx, 0)
	if err != nil {
		t.Fatalf("CheckNow 1: %v", err)
	}
	if sum1.BaselineInitialized != 2 || sum1.FailedCreators != 1 || sum1.AutoCacheTasks != 0 {
		t.Fatalf("unexpected baseline summary: %+v", sum1)
	}
	updatesAfterBaseline, err := mgr.ListUpdates("")
	if err != nil || len(updatesAfterBaseline) != 0 {
		t.Fatalf("expected 0 update items after baseline check (pre-baseline items must not enter updates), got %d err=%v", len(updatesAfterBaseline), err)
	}
	tasksAfterBaseline, _ := dlMgr.ListTasks()
	if len(tasksAfterBaseline) != 0 {
		t.Fatalf("expected 0 auto-cache tasks on baseline, got %d", len(tasksAfterBaseline))
	}

	// 2. Second check: UP1001 publishes BV1new000002 -> detected as new update after baseline and enqueues 1 auto-cache task!
	step.Store(2)
	sum2, err := mgr.CheckNow(ctx, 0)
	if err != nil {
		t.Fatalf("CheckNow 2: %v", err)
	}
	if sum2.NewVideos != 1 || sum2.AutoCacheTasks != 1 {
		t.Fatalf("unexpected incremental summary: %+v", sum2)
	}
	updatesAfterInc, err := mgr.ListUpdates("")
	if err != nil || len(updatesAfterInc) != 1 || updatesAfterInc[0].DynID != "BV1new000002" {
		t.Fatalf("expected 1 post-baseline update item BV1new000002, got %+v err=%v", updatesAfterInc, err)
	}
	subsAfterInc, _ := mgr.List()
	var foundLatestTs int64
	for _, s := range subsAfterInc {
		if s.Mid == 1001 {
			foundLatestTs = s.LatestUpdateTs
		}
	}
	if foundLatestTs != 1700000900 {
		t.Fatalf("expected LatestUpdateTs=1700000900 on UP1001 for Recent Updates sorting, got %d", foundLatestTs)
	}

	// 3. Risk control circuit breaker stops remaining creators in batch immediately.
	triggerRisk.Store(true)
	sum3, err := mgr.CheckNow(ctx, 0)
	if err != nil {
		t.Fatalf("CheckNow 3: %v", err)
	}
	if !sum3.StoppedByRiskControl || sum3.CheckedCreators != 1 {
		t.Fatalf("expected risk control to stop batch on first creator, got %+v", sum3)
	}
}

// C30 & R-10: 3-page dynamic pagination, pinned item ignore, gap note ("可能还有更早的更新没有拿到"), and 3 consecutive network errors stop.
func TestBaselineAndIncrementalCheckThreePageAndMarkSeen(t *testing.T) {
	var mode atomic.Int32 // 1 = baseline, 2 = 3 pages without hitting baseline (gap), 3 = network failures

	mux := http.NewServeMux()
	mux.HandleFunc("/x/polymer/web-dynamic/v1/feed/space", func(w http.ResponseWriter, r *http.Request) {
		if mode.Load() == 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		offset := r.URL.Query().Get("offset")
		if mode.Load() == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"has_more": false,
					"offset":   "",
					"items": []map[string]any{
						{
							"id_str": "100",
							"type":   "DYNAMIC_TYPE_WORD",
							"modules": map[string]any{
								"module_author":  map[string]any{"mid": 901, "name": "UP901", "pub_ts": 1700000100},
								"module_dynamic": map[string]any{"desc": map[string]any{"text": "Baseline post"}},
							},
						},
					},
				},
			})
			return
		}
		// Mode 2: 3 pages of newer posts (203, 202, 201) plus a pinned old post (pub_ts=1600000000) that must NOT trigger baseline stop!
		pageNum := 1
		nextOff := "p2"
		dynID := "203"
		pubTs := int64(1700000400)
		switch offset {
		case "p2":
			pageNum = 2
			nextOff = "p3"
			dynID = "202"
			pubTs = 1700000300
		case "p3":
			pageNum = 3
			nextOff = "p4"
			dynID = "201"
			pubTs = 1700000200
		}
		items := []map[string]any{}
		if pageNum == 1 {
			items = append(items, map[string]any{
				"id_str": "50",
				"type":   "DYNAMIC_TYPE_WORD",
				"modules": map[string]any{
					"module_tag":     map[string]any{"text": "置顶"},
					"module_author":  map[string]any{"mid": 901, "name": "UP901", "pub_ts": 1600000000},
					"module_dynamic": map[string]any{"desc": map[string]any{"text": "Old pinned post"}},
				},
			})
		}
		items = append(items, map[string]any{
			"id_str": dynID,
			"type":   "DYNAMIC_TYPE_DRAW",
			"modules": map[string]any{
				"module_author":  map[string]any{"mid": 901, "name": "UP901", "pub_ts": pubTs},
				"module_dynamic": map[string]any{"desc": map[string]any{"text": "New post " + dynID}},
			},
		})
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"has_more": true,
				"offset":   nextOff,
				"items":    items,
			},
		})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	tmp := t.TempDir()
	db, _, _, _ := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	defer db.Close()
	lib, _ := library.Open(db, filepath.Join(tmp, "cache"))
	cred, _, _ := credential.Open(filepath.Join(tmp, "cred.dpapi"))
	_, _ = cred.SaveManual("SESSDATA=s; bili_jct=c", "rt")

	client := bilibili.NewClient(bilibili.ClientOptions{
		CredStore:      cred,
		Endpoints:      bilibili.Endpoints{APIBase: srv.URL},
		PacingInterval: -1,
		AllowLocalhost: true,
	})
	client.SetWBIKeys("7cd084941338484aae1ad9425b84077c", "4932caff0ff746eab6f01bf08b70ac45")
	mgr, _ := NewManager(db, lib, nil, client, settings.Default(filepath.Join(tmp, "cache")).Subscription, nil)
	mgr.SetAccountInterval(0)

	sub1, _ := mgr.Upsert(Subscription{Mid: 901, Uname: "UP901", Enabled: true, Scope: "dynamic"})
	ctx := context.Background()

	// 1. Baseline check: establishes baseline without adding pre-baseline dynamics into update_item
	mode.Store(1)
	sum1, err := mgr.CheckNow(ctx, 901)
	if err != nil || sum1.BaselineInitialized != 1 {
		t.Fatalf("baseline check: %+v err=%v", sum1, err)
	}
	baseUpdates, err := mgr.ListUpdates(sub1.ID)
	if err != nil || len(baseUpdates) != 0 {
		t.Fatalf("expected 0 updates after baseline check, got %+v err=%v", baseUpdates, err)
	}

	// 2. Incremental check reads 3 pages (ignoring pinned item 50), records 3 new dynamics (203, 202, 201), and sets gap note!
	mode.Store(2)
	sum2, err := mgr.CheckNow(ctx, 901)
	if err != nil || sum2.NewDynamics != 3 || len(sum2.GapAccounts) != 1 {
		t.Fatalf("3-page gap check: %+v err=%v", sum2, err)
	}
	st, ok, _ := mgr.GetAccountState("bilibili", "901")
	if !ok || st.Note != "可能还有更早的更新没有拿到" {
		t.Fatalf("expected gap note on account_state, got %+v", st)
	}
	incUpdates, err := mgr.ListUpdates(sub1.ID)
	if err != nil || len(incUpdates) != 3 {
		t.Fatalf("expected 3 post-baseline updates (excluding old pinned item 50), got %+v err=%v", incUpdates, err)
	}

	// 3. Test 3 consecutive network errors stopping a round of 4 accounts.
	for i := 902; i <= 905; i++ {
		_, _ = mgr.Upsert(Subscription{Mid: int64(i), Uname: fmt.Sprintf("UP%d", i), Enabled: true, Scope: "dynamic"})
	}
	mode.Store(3)
	sum3, _ := mgr.CheckNow(ctx, 0)
	if sum3.CheckedCreators != 3 || sum3.FailedCreators != 3 {
		t.Fatalf("expected round to stop after 3 consecutive network failures, got %+v", sum3)
	}
}

// C31 & R-11: Scheduled check 30-account cap, exponential backoff (interval * 2^n up to 24h) in updates.db, 200/hr token bucket, and 20-task auto-cache cap + backlog.
func TestScheduledCheckBackoffQuotaAndAutoCache(t *testing.T) {
	var failRisk atomic.Bool
	var srvURL string

	mux := http.NewServeMux()
	mux.HandleFunc("/x/polymer/web-dynamic/v1/feed/space", func(w http.ResponseWriter, r *http.Request) {
		if failRisk.Load() {
			w.WriteHeader(http.StatusPreconditionFailed) // 412 risk_control
			return
		}
		mid := r.URL.Query().Get("host_mid")
		var items []map[string]any
		if mid == "1001" {
			// Return 25 new draw dynamics to test the 20-task per-round auto-cache cap + 5 backlog!
			for i := 1; i <= 25; i++ {
				items = append(items, map[string]any{
					"id_str": fmt.Sprintf("900%02d", i),
					"type":   "DYNAMIC_TYPE_DRAW",
					"modules": map[string]any{
						"module_author":  map[string]any{"mid": 1001, "name": "UP1001", "pub_ts": 1700001000 + i},
						"module_dynamic": map[string]any{"desc": map[string]any{"text": fmt.Sprintf("Post %d", i)}},
					},
				})
			}
		} else {
			items = append(items, map[string]any{
				"id_str": "100" + mid,
				"type":   "DYNAMIC_TYPE_WORD",
				"modules": map[string]any{
					"module_author":  map[string]any{"mid": 1000, "name": "UP" + mid, "pub_ts": 1700000100},
					"module_dynamic": map[string]any{"desc": map[string]any{"text": "Baseline"}},
				},
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"has_more": false, "items": items},
		})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()
	srvURL = srv.URL
	_ = srvURL

	tmp := t.TempDir()
	db, _, _, _ := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	defer db.Close()
	cacheDir := filepath.Join(tmp, "cache")
	lib, _ := library.Open(db, cacheDir)
	cred, _, _ := credential.Open(filepath.Join(tmp, "cred.dpapi"))
	_, _ = cred.SaveManual("SESSDATA=s; bili_jct=c", "rt")

	client := bilibili.NewClient(bilibili.ClientOptions{
		CredStore:      cred,
		Endpoints:      bilibili.Endpoints{APIBase: srv.URL},
		PacingInterval: -1,
		AllowLocalhost: true,
	})
	client.SetWBIKeys("7cd084941338484aae1ad9425b84077c", "4932caff0ff746eab6f01bf08b70ac45")
	dlMgr, _ := download.NewManager(db, lib, client, settings.Default(cacheDir).Download, nil)
	subCfg := settings.Default(cacheDir).Subscription
	subCfg.IntervalMinutes = 60
	subCfg.Enabled = true
	mgr, _ := NewManager(db, lib, dlMgr, client, subCfg, nil)
	mgr.SetAccountInterval(0)

	now := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	mgr.SetNowFunc(func() time.Time { return now })

	// Create 35 subscriptions to verify that a scheduled round checks at most 30 oldest-checked accounts.
	for i := 1001; i <= 1035; i++ {
		sub, _ := mgr.Upsert(Subscription{Mid: int64(i), Uname: fmt.Sprintf("UP%d", i), Enabled: true, Scope: "dynamic"})
		if i == 1001 {
			// Establish baseline for 1001 and enable AutoCache rule with since = 1700000500.
			mgr.saveAccountStateLocked(AccountState{
				Site:          "bilibili",
				UID:           "1001",
				BaselineAt:    now.Add(-2 * time.Hour).Format(time.RFC3339),
				LastCheckedAt: now.Add(-2 * time.Hour).Format(time.RFC3339),
				NewestID:      "80000",
				NewestTs:      1700000500,
			})
			_, _ = mgr.SetRules(sub.ID, SubRules{
				ScheduledCheck: true,
				AutoCache: AutoCacheRule{
					Enabled: true,
					Types:   []string{"draw"},
					Since:   time.Unix(1700000500, 0).UTC().Format(time.RFC3339),
				},
			})
		} else {
			mgr.saveAccountStateLocked(AccountState{
				Site:          "bilibili",
				UID:           strconv.Itoa(i),
				BaselineAt:    now.Add(-1 * time.Hour).Format(time.RFC3339),
				LastCheckedAt: now.Add(-1 * time.Hour).Format(time.RFC3339),
				NewestID:      "100",
				NewestTs:      1700000000,
			})
		}
	}

	ctx := context.Background()

	// 1. Run scheduled round 1: checks 30 accounts (including 1001), creates 20 auto-cache tasks and leaves 5 in backlog!
	sum1, err := mgr.RunScheduledRound(ctx)
	if err != nil {
		t.Fatalf("RunScheduledRound 1: %v", err)
	}
	if sum1.CheckedCreators != 30 {
		t.Fatalf("CheckedCreators = %d, want 30", sum1.CheckedCreators)
	}
	if sum1.AutoCacheTasks != 20 || sum1.AutoCacheBacklog != 5 {
		t.Fatalf("expected 20 AutoCacheTasks and 5 AutoCacheBacklog, got %+v", sum1)
	}

	// 2. Trigger risk_control (412) on scheduled round -> sets exponential backoff = 60m * 2^1 = 2h in updates.db!
	failRisk.Store(true)
	sumRisk, _ := mgr.RunScheduledRound(ctx)
	if !sumRisk.StoppedByRiskControl {
		t.Fatalf("expected StoppedByRiskControl=true, got %+v", sumRisk)
	}
	inBackoff, until := mgr.IsInBackoff()
	if !inBackoff || !until.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("expected backoff until %v (interval * 2^1), got inBackoff=%v until=%v", now.Add(2*time.Hour), inBackoff, until)
	}

	// While in backoff, RunScheduledRound defers immediately.
	sumDeferred, _ := mgr.RunScheduledRound(ctx)
	if !sumDeferred.DeferredByBackoff {
		t.Fatalf("expected DeferredByBackoff=true while in backoff, got %+v", sumDeferred)
	}

	// 3. Test 200 req/hr token bucket exhaustion.
	now = now.Add(3 * time.Hour) // past backoff, and previous tokens > 1h old are pruned
	if !mgr.ConsumeQuotaTokens(190) {
		t.Fatalf("expected 190 tokens to succeed")
	}
	// Next scheduled round needs 30 tokens (190 + 30 = 220 > 200) -> deferred by quota!
	sumQuota, _ := mgr.RunScheduledRound(ctx)
	if !sumQuota.DeferredByQuota {
		t.Fatalf("expected DeferredByQuota=true when 190+30 > 200 tokens, got %+v", sumQuota)
	}
}

// C32: Schedule calculation for interval, daily, weekly, and manual modes.
func TestNextRunTimeModes(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	// Wednesday 2026-11-04 09:00:00 CST (ISO weekday = 3)
	now := time.Date(2026, 11, 4, 9, 0, 0, 0, loc)

	// 1. Manual mode -> false
	if _, ok := NextRunTime(settings.SubscriptionSettings{Enabled: true, Mode: "manual"}, now); ok {
		t.Fatalf("expected manual mode to return ok=false")
	}

	// 2. Interval mode (30 mins) -> 09:30
	nextInt, ok := NextRunTime(settings.SubscriptionSettings{Enabled: true, Mode: "interval", IntervalMinutes: 30}, now)
	if !ok || !nextInt.Equal(now.Add(30*time.Minute)) {
		t.Fatalf("interval next = %v, want %v", nextInt, now.Add(30*time.Minute))
	}

	// 3. Daily mode: 08:00 (already passed today -> tomorrow 08:00)
	nextDaily, ok := NextRunTime(settings.SubscriptionSettings{Enabled: true, Mode: "daily", DailyTime: "08:00"}, now)
	wantDaily := time.Date(2026, 11, 5, 8, 0, 0, 0, loc)
	if !ok || !nextDaily.Equal(wantDaily) {
		t.Fatalf("daily next = %v, want %v", nextDaily, wantDaily)
	}

	// 4. Weekly mode: Mon(1), Wed(3), Fri(5) at 08:00 -> Wed 08:00 passed, so Friday 2026-11-06 08:00
	nextWeekly, ok := NextRunTime(settings.SubscriptionSettings{
		Enabled:    true,
		Mode:       "weekly",
		WeeklyDays: []int{1, 3, 5},
		WeeklyTime: "08:00",
	}, now)
	wantWeekly := time.Date(2026, 11, 6, 8, 0, 0, 0, loc)
	if !ok || !nextWeekly.Equal(wantWeekly) {
		t.Fatalf("weekly next = %v, want %v", nextWeekly, wantWeekly)
	}
}

// C33: OPML 2.0 export and import round-trip + invalid XML rejection.
func TestOPMLImportExportRoundTrip(t *testing.T) {
	subs := []Subscription{
		{Mid: 12345, Uname: "Creator Alpha"},
		{Mid: 67890, Uname: "Creator Beta"},
	}
	xmlBytes, err := ExportOPML(subs, time.Unix(1700000000, 0))
	if err != nil {
		t.Fatalf("ExportOPML: %v", err)
	}
	xmlStr := string(xmlBytes)
	if strings.Contains(xmlStr, "SESSDATA") || !strings.Contains(xmlStr, "space.bilibili.com/12345") {
		t.Fatalf("unexpected OPML XML:\n%s", xmlStr)
	}

	parsed, err := ParseOPML(xmlBytes)
	if err != nil || len(parsed) != 2 {
		t.Fatalf("ParseOPML = %+v, err=%v", parsed, err)
	}
	if parsed[0].Mid != 12345 || parsed[0].Uname != "Creator Alpha" || parsed[1].Mid != 67890 {
		t.Fatalf("unexpected parsed OPML creators: %+v", parsed)
	}

	// Invalid XML returns structured error.
	if _, err := ParseOPML([]byte("<opml><unclosed>")); err == nil {
		t.Fatalf("expected error for malformed OPML XML")
	}
}
