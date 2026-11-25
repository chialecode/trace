package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
)

func openFixesTestManager(t *testing.T, client *bilibili.Client) (*Manager, string) {
	t.Helper()
	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	lib, err := library.Open(db, filepath.Join(tmp, "cache"))
	if err != nil {
		t.Fatalf("library.Open: %v", err)
	}
	if client == nil {
		cred, _, _ := credential.Open(filepath.Join(tmp, "cred.dpapi"))
		client = bilibili.NewClient(bilibili.ClientOptions{
			CredStore:      cred,
			PacingInterval: -1,
			AllowLocalhost: true,
		})
	}
	cfg := settings.Default(filepath.Join(tmp, "cache")).Subscription
	mgr, err := NewManager(db, lib, nil, client, cfg, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	mgr.SetAccountInterval(0)
	return mgr, tmp
}

func TestDeleteUnknownNonNumericIDPreservesDouyinSubscriptions(t *testing.T) {
	mgr, _ := openFixesTestManager(t, nil)

	dy1, err := mgr.Create("抖音创作者一", "", []AccountRef{
		{Site: "douyin", UID: "MS4wLjABAAAA_DeleteGuard_01", Name: "抖音一"},
	}, SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatalf("Create dy1: %v", err)
	}
	dy2, err := mgr.Create("抖音创作者二", "", []AccountRef{
		{Site: "douyin", UID: "MS4wLjABAAAA_DeleteGuard_02", Name: "抖音二"},
	}, SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatalf("Create dy2: %v", err)
	}

	// Deleting a missing non-numeric ID must be a no-op, NOT match mid = 0 rows.
	if err := mgr.Delete("sub_does_not_exist"); err != nil {
		t.Fatalf("Delete unknown id: %v", err)
	}

	subs, err := mgr.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(subs) != 2 {
		t.Fatalf("expected both Douyin subscriptions preserved, got %d: %+v", len(subs), subs)
	}
	if _, ok := mgr.OwnerOf("douyin", dy1.Accounts[0].UID); !ok {
		t.Fatalf("dy1 disappeared after unknown Delete")
	}
	if _, ok := mgr.OwnerOf("douyin", dy2.Accounts[0].UID); !ok {
		t.Fatalf("dy2 disappeared after unknown Delete")
	}
}

func TestUnlinkAndMovePreventSubIDCollision(t *testing.T) {
	mgr, _ := openFixesTestManager(t, nil)

	// New subscriptions must default SubID to ID rather than "bilibili:<mid>".
	fresh, err := mgr.Create("FreshCreator", "", []AccountRef{
		{Site: "bilibili", UID: "1001", Name: "UP1001"},
	}, SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatalf("Create fresh: %v", err)
	}
	if fresh.SubID != fresh.ID {
		t.Fatalf("fresh.SubID = %q, want %q", fresh.SubID, fresh.ID)
	}

	// Simulate a legacy subscription whose sub_id was "bilibili:2001" while owning
	// both bilibili:2001 and bilibili:2002.
	legacy, err := mgr.Create("LegacyCreator", "", []AccountRef{
		{Site: "bilibili", UID: "2001", Name: "UP2001"},
		{Site: "bilibili", UID: "2002", Name: "UP2002"},
	}, SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatalf("Create legacy: %v", err)
	}
	if _, err := mgr.db.Exec(`UPDATE subscriptions SET sub_id = ? WHERE sub_id = ?`, "bilibili:2001", legacy.SubID); err != nil {
		t.Fatalf("seed legacy sub_id: %v", err)
	}
	mgr.mu.Lock()
	mgr.invalidateSubsCacheLocked()
	mgr.mu.Unlock()

	// Unlinking 2001 must rekey legacy's sub_id so creating a new subscription
	// with 2001 never collides on UNIQUE(sub_id).
	afterUnlink, err := mgr.Unlink(legacy.ID, "bilibili", "2001")
	if err != nil {
		t.Fatalf("Unlink 2001: %v", err)
	}
	if afterUnlink.SubID == "bilibili:2001" {
		t.Fatalf("expected legacy sub_id rekeyed away from bilibili:2001, got %q", afterUnlink.SubID)
	}
	recreated, err := mgr.Create("Recreated2001", "", []AccountRef{
		{Site: "bilibili", UID: "2001", Name: "UP2001"},
	}, SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatalf("Create 2001 after Unlink: %v", err)
	}

	// Seed legacy sub_id again on recreated, add 2003, then Move 2001 to another sub.
	if _, err := mgr.Link(recreated.ID, "bilibili", "2003", "UP2003", ""); err != nil {
		t.Fatalf("Link 2003: %v", err)
	}
	if _, err := mgr.db.Exec(`UPDATE subscriptions SET sub_id = ? WHERE sub_id = ?`, "bilibili:2001", recreated.SubID); err != nil {
		t.Fatalf("seed legacy sub_id 2: %v", err)
	}
	mgr.mu.Lock()
	mgr.invalidateSubsCacheLocked()
	mgr.mu.Unlock()

	if _, err := mgr.Move("bilibili", "2001", fresh.ID); err != nil {
		t.Fatalf("Move 2001: %v", err)
	}
	owner, ok := mgr.OwnerOf("bilibili", "2001")
	if !ok || owner.ID != fresh.ID {
		t.Fatalf("OwnerOf(2001) = %+v ok=%v, want %s", owner, ok, fresh.ID)
	}
	// Upsert by Mid on an existing multi-account subscription must not collide on sub_id.
	if _, err := mgr.Upsert(Subscription{Mid: 2001, Uname: "UP2001_Upserted", Enabled: true}); err != nil {
		t.Fatalf("Upsert existing mid 2001: %v", err)
	}
}

func TestAuthRequiredDoesNotTriggerBackoff(t *testing.T) {
	mgr, tmp := openFixesTestManager(t, nil)
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	mgr.SetNowFunc(func() time.Time { return now })
	mgr.SetSiteAuthCheck(func(s site.ID) bool { return s == site.Douyin })

	dyFetcher := &syntheticDouyinFetcher{
		handler: func(ctx context.Context, uid, cursor string) (site.CreatorPostsPage, error) {
			return site.CreatorPostsPage{}, &site.Error{
				Site:    site.Douyin,
				Kind:    site.ErrKindAuthRequired,
				Message: "login expired",
			}
		},
	}
	mgr.RegisterFetcher(dyFetcher)

	cfg := settings.Default(filepath.Join(tmp, "cache")).Subscription
	cfg.Enabled = true
	cfg.IntervalMinutes = 60
	mgr.UpdateSettings(cfg)

	if _, err := mgr.Create("抖音鉴权失效", "", []AccountRef{
		{Site: "douyin", UID: "MS4wLjABAAAA_AuthNoBackoff_01", Name: "抖音号"},
	}, SubRules{ScheduledCheck: true}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := mgr.RunScheduledRound(context.Background()); err != nil {
		t.Fatalf("RunScheduledRound: %v", err)
	}
	st := mgr.GetSiteSchedulerState("douyin")
	if st.LastStopReason != string(site.ErrKindAuthRequired) {
		t.Fatalf("LastStopReason = %q, want auth_required", st.LastStopReason)
	}
	if st.BackoffLevel != 0 || st.BackoffUntil != "" {
		t.Fatalf("auth_required must not trigger exponential backoff: %+v", st)
	}
	if inBackoff, _ := mgr.IsSiteInBackoff("douyin"); inBackoff {
		t.Fatalf("expected IsSiteInBackoff=false after auth_required")
	}

	// Direct RecordSiteAutomaticStop must behave identically.
	mgr.RecordSiteAutomaticStop("bilibili", site.ErrKindAuthRequired)
	biliSt := mgr.GetSiteSchedulerState("bilibili")
	if biliSt.LastStopReason != string(site.ErrKindAuthRequired) || biliSt.BackoffLevel != 0 || biliSt.BackoffUntil != "" {
		t.Fatalf("RecordSiteAutomaticStop(auth_required) set backoff: %+v", biliSt)
	}
}

func TestAutoCacheStopsSiteOnRiskControlAndMarksUnavailable(t *testing.T) {
	mgr, tmp := openFixesTestManager(t, nil)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	mgr.SetNowFunc(func() time.Time { return now })
	mgr.SetSiteAuthCheck(func(s site.ID) bool { return s == site.Douyin })

	cfg := settings.Default(filepath.Join(tmp, "cache")).Subscription
	cfg.Enabled = true
	cfg.IntervalMinutes = 60
	mgr.UpdateSettings(cfg)

	uid := "MS4wLjABAAAA_AutoCacheStop_01"
	sub, err := mgr.Create("抖音自动缓存风控", "", []AccountRef{
		{Site: "douyin", UID: uid, Name: "创作者"},
	}, SubRules{
		ScheduledCheck: true,
		AutoCache: AutoCacheRule{
			Enabled: true,
			Types:   []string{"video"},
			Since:   now.Add(-1 * time.Hour).Format(time.RFC3339),
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Establish baseline so the next round sees 3 new posts.
	mgr.mu.Lock()
	mgr.saveAccountStateLocked(AccountState{
		Site:          "douyin",
		UID:           uid,
		BaselineAt:    now.Add(-2 * time.Hour).Format(time.RFC3339),
		LastCheckedAt: now.Add(-2 * time.Hour).Format(time.RFC3339),
		NewestID:      "aweme_base",
		NewestTs:      now.Add(-2 * time.Hour).Unix(),
	})
	mgr.mu.Unlock()

	mgr.RegisterFetcher(&syntheticDouyinFetcher{
		handler: func(ctx context.Context, gotUID, cursor string) (site.CreatorPostsPage, error) {
			return site.CreatorPostsPage{
				Posts: []site.CreatorPost{
					{ID: "aweme_3", Type: "video", Title: "Third", PubTs: now.Add(-10 * time.Minute).Unix()},
					{ID: "aweme_2", Type: "video", Title: "Second", PubTs: now.Add(-20 * time.Minute).Unix()},
					{ID: "aweme_1", Type: "video", Title: "First", PubTs: now.Add(-30 * time.Minute).Unix()},
				},
			}, nil
		},
	})

	var attempted []string
	mgr.RegisterAutoCacheEnqueuer(site.Douyin, func(ctx context.Context, item UpdateItem) (string, error) {
		attempted = append(attempted, item.DynID)
		switch item.DynID {
		case "aweme_1":
			return "", &site.Error{Site: site.Douyin, Kind: site.ErrKindUnavailable, Message: "deleted"}
		case "aweme_2":
			return "", &site.Error{Site: site.Douyin, Kind: site.ErrKindRiskControl, Message: "captcha"}
		default:
			return "task_unexpected", nil
		}
	})

	sum, err := mgr.RunScheduledRound(context.Background())
	if err != nil {
		t.Fatalf("RunScheduledRound: %v", err)
	}
	if !sum.StoppedByRiskControl {
		t.Fatalf("expected StoppedByRiskControl=true when auto-cache hits risk_control, got %+v", sum)
	}
	// Ordered oldest first (pub_ts ASC): aweme_1 (unavailable), aweme_2 (risk_control -> STOP), aweme_3 NOT attempted.
	if len(attempted) != 2 || attempted[0] != "aweme_1" || attempted[1] != "aweme_2" {
		t.Fatalf("attempted = %v, want [aweme_1 aweme_2] (aweme_3 must be skipped after risk_control)", attempted)
	}
	if inBackoff, _ := mgr.IsSiteInBackoff("douyin"); !inBackoff {
		t.Fatalf("expected douyin backoff to be set after auto-cache risk_control")
	}

	// Verify aweme_1 was marked unavailable in update_item.auto_task_id.
	updates, err := mgr.ListUpdates(sub.ID)
	if err != nil {
		t.Fatalf("ListUpdates: %v", err)
	}
	var st1 string
	for _, u := range updates {
		if u.DynID == "aweme_1" {
			st1 = u.AutoTaskID
		}
	}
	if st1 != "unavailable" {
		t.Fatalf("aweme_1 AutoTaskID = %q, want unavailable", st1)
	}
}

func TestAutoCacheQuotaAndSkippedUnauthDoNotBackoffOrPolluteSiteStopped(t *testing.T) {
	mgr, tmp := openFixesTestManager(t, nil)
	now := time.Date(2026, 11, 20, 12, 30, 0, 0, time.UTC)
	mgr.SetNowFunc(func() time.Time { return now })
	// Douyin signed in; Bilibili unauthenticated (will be marked skipped_unauth in abortedSites).
	mgr.SetSiteAuthCheck(func(s site.ID) bool { return s == site.Douyin })

	cfg := settings.Default(filepath.Join(tmp, "cache")).Subscription
	cfg.Enabled = true
	cfg.IntervalMinutes = 60
	mgr.UpdateSettings(cfg)

	// Unauthenticated Bilibili subscription.
	if _, err := mgr.Create("未登录B站", "", []AccountRef{
		{Site: "bilibili", UID: "9901", Name: "UP9901"},
	}, SubRules{ScheduledCheck: true}); err != nil {
		t.Fatalf("Create bilibili sub: %v", err)
	}

	// Authenticated Douyin subscription with AutoCache enabled.
	uid := "MS4wLjABAAAA_AutoCacheQuota_01"
	if _, err := mgr.Create("抖音额度耗尽", "", []AccountRef{
		{Site: "douyin", UID: uid, Name: "创作者"},
	}, SubRules{
		ScheduledCheck: true,
		AutoCache: AutoCacheRule{
			Enabled: true,
			Types:   []string{"video"},
			Since:   now.Add(-1 * time.Hour).Format(time.RFC3339),
		},
	}); err != nil {
		t.Fatalf("Create douyin sub: %v", err)
	}
	mgr.mu.Lock()
	mgr.saveAccountStateLocked(AccountState{
		Site:          "douyin",
		UID:           uid,
		BaselineAt:    now.Add(-2 * time.Hour).Format(time.RFC3339),
		LastCheckedAt: now.Add(-2 * time.Hour).Format(time.RFC3339),
		NewestID:      "aweme_base",
		NewestTs:      now.Add(-2 * time.Hour).Unix(),
	})
	mgr.mu.Unlock()

	mgr.RegisterFetcher(&syntheticDouyinFetcher{
		handler: func(ctx context.Context, gotUID, cursor string) (site.CreatorPostsPage, error) {
			return site.CreatorPostsPage{
				Posts: []site.CreatorPost{
					{ID: "aweme_q2", Type: "video", Title: "Second", PubTs: now.Add(-10 * time.Minute).Unix()},
					{ID: "aweme_q1", Type: "video", Title: "First", PubTs: now.Add(-20 * time.Minute).Unix()},
				},
			}, nil
		},
	})

	var attempted []string
	mgr.RegisterAutoCacheEnqueuer(site.Douyin, func(ctx context.Context, item UpdateItem) (string, error) {
		attempted = append(attempted, item.DynID)
		return "", &site.Error{Site: site.Douyin, Kind: site.ErrKindQuota, Message: "hourly quota exhausted"}
	})

	sum, err := mgr.RunScheduledRound(context.Background())
	if err != nil {
		t.Fatalf("RunScheduledRound: %v", err)
	}
	// Quota halts Douyin auto-cache immediately after aweme_q1, sets DeferredByQuota,
	// does NOT trigger backoff, and does NOT pollute SiteStopped with "quota" or "skipped_unauth".
	if len(attempted) != 1 || attempted[0] != "aweme_q1" {
		t.Fatalf("attempted = %v, want [aweme_q1]", attempted)
	}
	if !sum.DeferredByQuota {
		t.Fatalf("expected DeferredByQuota=true, got %+v", sum)
	}
	if sum.StoppedByRiskControl {
		t.Fatalf("quota must not set StoppedByRiskControl=true: %+v", sum)
	}
	if len(sum.SiteStopped) != 0 {
		t.Fatalf("SiteStopped must stay empty for quota/skipped_unauth, got %+v", sum.SiteStopped)
	}
	if inBackoff, _ := mgr.IsSiteInBackoff("douyin"); inBackoff {
		t.Fatalf("quota exhaustion must not trigger exponential backoff")
	}
	st := mgr.GetSiteSchedulerState("douyin")
	if st.BackoffLevel != 0 || st.BackoffUntil != "" {
		t.Fatalf("unexpected douyin backoff state after quota: %+v", st)
	}

	// Defensive check: direct RecordSiteAutomaticStop with quota/backoff never advances backoff.
	mgr.RecordSiteAutomaticStop("douyin", site.ErrKindQuota)
	mgr.RecordSiteAutomaticStop("douyin", site.ErrKindBackoff)
	st2 := mgr.GetSiteSchedulerState("douyin")
	if st2.BackoffLevel != 0 || st2.BackoffUntil != "" {
		t.Fatalf("RecordSiteAutomaticStop(quota/backoff) wrongly set backoff: %+v", st2)
	}
}

func TestBilibiliBaselineAllPinnedCursorStallAndSanitizedSnapshot(t *testing.T) {
	var phase atomic.Int32
	var reqCount atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/x/polymer/web-dynamic/v1/feed/space", func(w http.ResponseWriter, r *http.Request) {
		reqCount.Add(1)
		switch phase.Load() {
		case 1:
			// Baseline page 1 contains ONLY a pinned dynamic.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"has_more": false,
					"offset":   "",
					"items": []map[string]any{
						{
							"id_str": "pin_100",
							"type":   "DYNAMIC_TYPE_WORD",
							"modules": map[string]any{
								"module_tag":     map[string]any{"text": "置顶"},
								"module_author":  map[string]any{"mid": 7001, "name": "UP7001", "pub_ts": 1700000100},
								"module_dynamic": map[string]any{"desc": map[string]any{"text": "Pinned baseline"}},
							},
						},
					},
				},
			})
		case 2:
			// Incremental check: returns a new dynamic with signed image URL AND stalls cursor (offset repeats).
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"has_more": true,
					"offset":   "stall_cursor",
					"items": []map[string]any{
						{
							"id_str": "new_200",
							"type":   "DYNAMIC_TYPE_DRAW",
							"modules": map[string]any{
								"module_author": map[string]any{
									"mid":    7001,
									"name":   "UP7001",
									"face":   "https://i0.hdslb.com/face.jpg?auth_key=SECRET_FACE",
									"pub_ts": 1700000500,
								},
								"module_dynamic": map[string]any{
									"desc": map[string]any{"text": "New post with signed img"},
									"major": map[string]any{
										"type": "MAJOR_TYPE_DRAW",
										"draw": map[string]any{
											"items": []map[string]any{
												{"src": "https://i0.hdslb.com/img.jpg?x-expires=9999&token=SECRET_TOKEN"},
											},
										},
									},
								},
							},
						},
						{
							"id_str": "pin_100",
							"type":   "DYNAMIC_TYPE_WORD",
							"modules": map[string]any{
								"module_tag":     map[string]any{"text": "置顶"},
								"module_author":  map[string]any{"mid": 7001, "name": "UP7001", "pub_ts": 1700000100},
								"module_dynamic": map[string]any{"desc": map[string]any{"text": "Pinned baseline"}},
							},
						},
					},
				},
			})
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	lib, _ := library.Open(db, filepath.Join(tmp, "cache"))
	cred, _, _ := credential.Open(filepath.Join(tmp, "cred.dpapi"))
	_, _ = cred.SaveManual("SESSDATA=s; bili_jct=c", "")
	client := bilibili.NewClient(bilibili.ClientOptions{
		CredStore:      cred,
		Endpoints:      bilibili.Endpoints{APIBase: srv.URL},
		PacingInterval: -1,
		AllowLocalhost: true,
	})
	client.SetWBIKeys("7cd084941338484aae1ad9425b84077c", "4932caff0ff746eab6f01bf08b70ac45")
	mgr, err := NewManager(db, lib, nil, client, settings.Default(filepath.Join(tmp, "cache")).Subscription, nil)
	if err != nil {
		t.Fatal(err)
	}
	mgr.SetAccountInterval(0)

	sub, err := mgr.Upsert(Subscription{Mid: 7001, Uname: "UP7001", Enabled: true, Scope: "dynamic"})
	if err != nil {
		t.Fatal(err)
	}

	// Phase 1: all-pinned baseline must establish fallback watermark and record pinned baseline ID.
	phase.Store(1)
	sum1, err := mgr.CheckNow(context.Background(), 7001)
	if err != nil || sum1.BaselineInitialized != 1 {
		t.Fatalf("baseline check = %+v err=%v", sum1, err)
	}
	st, ok, _ := mgr.GetAccountState("bilibili", "7001")
	if !ok || st.NewestID != "pin_100" || st.NewestTs != 1700000100 {
		t.Fatalf("all-pinned baseline fallback watermark not set: %+v", st)
	}
	if !mgr.wasPinnedOnBaseline("bilibili", "7001", "pin_100") {
		t.Fatalf("expected pin_100 recorded in account_baseline_pinned")
	}

	// Phase 2: incremental check must stop on stalled cursor (<=2 requests: offset="" and offset="stall_cursor"),
	// detect new_200, and strip signed query strings from snapshot.
	reqCount.Store(0)
	phase.Store(2)
	sum2, err := mgr.CheckNow(context.Background(), 7001)
	if err != nil || sum2.NewDynamics != 1 {
		t.Fatalf("incremental check = %+v err=%v", sum2, err)
	}
	if reqCount.Load() > 2 {
		t.Fatalf("cursor stall did not stop pagination: reqCount=%d", reqCount.Load())
	}
	updates, err := mgr.ListUpdates(sub.ID)
	if err != nil || len(updates) != 1 || updates[0].DynID != "new_200" {
		t.Fatalf("unexpected updates: %+v err=%v", updates, err)
	}
	if strings.Contains(updates[0].Snapshot, "SECRET_TOKEN") || strings.Contains(updates[0].Snapshot, "SECRET_FACE") {
		t.Fatalf("snapshot leaked signed query params: %s", updates[0].Snapshot)
	}
}

func TestSnapshotDuplicateSubIDAndImportRuleNameAndCapOverflow(t *testing.T) {
	// 1. ParseSnapshot rejects duplicate subscription IDs.
	dupJSON := fmt.Sprintf(`{"format":%q,"version":%d,"subscriptions":[{"id":"s1","name":"A","accounts":[{"site":"bilibili","uid":"1"}]},{"id":"s1","name":"B","accounts":[{"site":"bilibili","uid":"2"}]}]}`, SnapshotFormat, SnapshotVersion)
	if _, err := ParseSnapshot([]byte(dupJSON)); err == nil {
		t.Fatalf("expected ParseSnapshot to reject duplicate sub.ID")
	}

	// 2. ReplaceFromSnapshot stamps unlinked_at on removed accounts and clears it when re-added.
	mgr, _ := openFixesTestManager(t, nil)
	now := time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)
	mgr.SetNowFunc(func() time.Time { return now })

	if err := mgr.ReplaceFromSnapshot(syncedSnapshot("101", "102")); err != nil {
		t.Fatalf("ReplaceFromSnapshot 1: %v", err)
	}
	mgr.mu.Lock()
	mgr.saveAccountStateLocked(AccountState{Site: "bilibili", UID: "101", BaselineAt: now.Format(time.RFC3339), NewestID: "x"})
	mgr.saveAccountStateLocked(AccountState{Site: "bilibili", UID: "102", BaselineAt: now.Format(time.RFC3339), NewestID: "y"})
	mgr.mu.Unlock()

	// Remove 102 via snapshot replace -> unlinked_at must be populated.
	if err := mgr.ReplaceFromSnapshot(syncedSnapshot("101")); err != nil {
		t.Fatalf("ReplaceFromSnapshot 2: %v", err)
	}
	st102, ok, _ := mgr.GetAccountState("bilibili", "102")
	if !ok || st102.UnlinkedAt == "" {
		t.Fatalf("expected unlinked_at stamped for removed account 102, got %+v ok=%v", st102, ok)
	}
	// Re-add 102 -> unlinked_at must be cleared.
	if err := mgr.ReplaceFromSnapshot(syncedSnapshot("101", "102")); err != nil {
		t.Fatalf("ReplaceFromSnapshot 3: %v", err)
	}
	st102, ok, _ = mgr.GetAccountState("bilibili", "102")
	if !ok || st102.UnlinkedAt != "" {
		t.Fatalf("expected unlinked_at cleared for re-added account 102, got %+v ok=%v", st102, ok)
	}

	// 3. ApplyImport merges tags when requested AND preserves >20 accounts on MergeExisting.
	mgr2, _ := openFixesTestManager(t, nil)
	var localAccs []AccountRef
	for i := 1; i <= 15; i++ {
		localAccs = append(localAccs, AccountRef{Site: "bilibili", UID: strconv.Itoa(3000 + i), Name: fmt.Sprintf("L%d", i)})
	}
	localSub, err := mgr2.Create("大订阅", "", localAccs, SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatalf("Create localSub: %v", err)
	}
	if _, err := mgr2.SetSubscriptionTags(localSub.ID, []string{TagScheduledCheck, TagCacheVideo}); err != nil {
		t.Fatalf("SetSubscriptionTags: %v", err)
	}

	var impAccs []SnapshotAccount
	// Overlap with local account 3001 + 10 brand new accounts -> total 25 (> 20 cap).
	impAccs = append(impAccs, SnapshotAccount{Site: "bilibili", UID: "3001", Name: "L1"})
	for i := 1; i <= 10; i++ {
		impAccs = append(impAccs, SnapshotAccount{Site: "bilibili", UID: strconv.Itoa(4000 + i), Name: fmt.Sprintf("N%d", i)})
	}
	snap := Snapshot{
		Format:  SnapshotFormat,
		Version: SnapshotVersion,
		Subscriptions: []SnapshotSubscription{
			{ID: "imp-big", Name: "导入大订阅", Tags: []string{TagScheduledCheck, TagCacheDraw}, Accounts: impAccs},
		},
	}
	if _, err := mgr2.ApplyImport(snap, ImportChoices{MergeExisting: true, MergeTags: true}); err != nil {
		t.Fatalf("ApplyImport: %v", err)
	}

	// All 25 accounts (15 local + 10 new) must be preserved without dropping any,
	// with localSub capped at 20 (with unioned tags) and the remaining 5 placed in an overflow subscription.
	subs, err := mgr2.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	totalAccounts := 0
	var localCount int
	for _, s := range subs {
		if len(s.Accounts) > 20 {
			t.Fatalf("subscription %s exceeded cap: %d", s.ID, len(s.Accounts))
		}
		totalAccounts += len(s.Accounts)
		if s.ID == localSub.ID {
			localCount = len(s.Accounts)
			if !HasTag(s.Tags, TagCacheVideo) || !HasTag(s.Tags, TagCacheDraw) {
				t.Fatalf("merged subscription missing unioned tags: %+v", s.Tags)
			}
		}
	}
	if localCount != 20 || totalAccounts != 25 || len(subs) != 2 {
		t.Fatalf("overflow merge wrong: subs=%d localCount=%d totalAccounts=%d", len(subs), localCount, totalAccounts)
	}
	for i := 1; i <= 10; i++ {
		if _, ok := mgr2.OwnerOf("bilibili", strconv.Itoa(4000+i)); !ok {
			t.Fatalf("new account %d was dropped during MergeExisting cap overflow", 4000+i)
		}
	}
}

func TestOwnerOfCacheConsistencyCheckPlanDisabledAndUpsertToggle(t *testing.T) {
	mgr, tmp := openFixesTestManager(t, nil)
	now := time.Date(2026, 11, 20, 16, 0, 0, 0, time.UTC)
	mgr.SetNowFunc(func() time.Time { return now })
	mgr.SetSiteAuthCheck(func(s site.ID) bool { return true })

	// 1. CheckPlan clears stale nextRunAt immediately when Enabled == false.
	cfg := settings.Default(filepath.Join(tmp, "cache")).Subscription
	cfg.Enabled = true
	cfg.Mode = "interval"
	cfg.IntervalMinutes = 30
	mgr.UpdateSettings(cfg)

	sub, err := mgr.Create("缓存一致性测试", "", []AccountRef{
		{Site: "bilibili", UID: "8001", Name: "UP8001"},
	}, SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	planEnabled := mgr.CheckPlan()
	if len(planEnabled) != 1 || planEnabled[0].Plan != PlanScheduled || planEnabled[0].NextAt == "" {
		t.Fatalf("expected PlanScheduled with NextAt when enabled: %+v", planEnabled)
	}
	// Seed a stale nextRunAt, then disable scheduling.
	mgr.mu.Lock()
	mgr.nextRunAt = now.Add(30 * time.Minute)
	mgr.mu.Unlock()
	cfg.Enabled = false
	mgr.UpdateSettings(cfg)
	planDisabled := mgr.CheckPlan()
	if len(planDisabled) != 1 || planDisabled[0].Plan != PlanOff || planDisabled[0].NextAt != "" {
		t.Fatalf("expected PlanOff and empty NextAt when disabled, got %+v", planDisabled)
	}

	// 2. OwnerOf O(1) cache consistency across Link, Move, Unlink, Delete.
	sub2, err := mgr.Create("目标订阅", "", []AccountRef{
		{Site: "bilibili", UID: "8002", Name: "UP8002"},
	}, SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatalf("Create sub2: %v", err)
	}
	if _, err := mgr.Link(sub.ID, "bilibili", "8003", "UP8003", ""); err != nil {
		t.Fatalf("Link 8003: %v", err)
	}
	if o, ok := mgr.OwnerOf("bilibili", "8003"); !ok || o.ID != sub.ID {
		t.Fatalf("OwnerOf after Link: %+v ok=%v", o, ok)
	}
	if _, err := mgr.Move("bilibili", "8003", sub2.ID); err != nil {
		t.Fatalf("Move 8003: %v", err)
	}
	if o, ok := mgr.OwnerOf("bilibili", "8003"); !ok || o.ID != sub2.ID {
		t.Fatalf("OwnerOf after Move: %+v ok=%v", o, ok)
	}
	if _, err := mgr.Unlink(sub2.ID, "bilibili", "8003"); err != nil {
		t.Fatalf("Unlink 8003: %v", err)
	}
	if _, ok := mgr.OwnerOf("bilibili", "8003"); ok {
		t.Fatalf("OwnerOf returned true after Unlink")
	}

	// 3. Upsert allows explicitly disabling Enabled and LiveAutoRecord while preserving AutoCache.Since.
	since := "2026-11-01T00:00:00Z"
	sub2, err = mgr.SetRules(sub2.ID, SubRules{
		ScheduledCheck: true,
		AutoRecord:     true,
		AutoCache: AutoCacheRule{
			Enabled: true,
			Types:   []string{"video"},
			Since:   since,
		},
	})
	if err != nil {
		t.Fatalf("SetRules: %v", err)
	}
	updated, err := mgr.Upsert(Subscription{
		ID:             sub2.ID,
		Mid:            8002,
		Uname:          "UP8002",
		Enabled:        false,
		LiveAutoRecord: false,
		AutoCache:      "video",
	})
	if err != nil {
		t.Fatalf("Upsert disable: %v", err)
	}
	if updated.Enabled || updated.Rules.ScheduledCheck || updated.LiveAutoRecord || updated.Rules.AutoRecord {
		t.Fatalf("Upsert did not honor explicit false toggles: %+v", updated)
	}
	if updated.Rules.AutoCache.Since != since {
		t.Fatalf("Upsert clobbered AutoCache.Since: got %q want %q", updated.Rules.AutoCache.Since, since)
	}
	if _, err := mgr.Link(sub2.ID, "bilibili", "8001", "UP8001", ""); !errors.Is(err, ErrAccountConflict) {
		t.Fatalf("expected ErrAccountConflict linking 8001 to sub2, got %v", err)
	}
	if err := mgr.Delete(sub2.ID); err != nil {
		t.Fatalf("Delete sub2: %v", err)
	}
	if _, ok := mgr.OwnerOf("bilibili", "8002"); ok {
		t.Fatalf("OwnerOf returned true after Delete")
	}
}
