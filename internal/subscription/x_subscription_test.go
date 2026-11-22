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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
)

type syntheticResetRateLimitErr struct {
	siteErr *site.Error
	resetAt time.Time
}

func (e *syntheticResetRateLimitErr) Error() string               { return e.siteErr.Error() }
func (e *syntheticResetRateLimitErr) Unwrap() error               { return e.siteErr }
func (e *syntheticResetRateLimitErr) RateLimitResetAt() time.Time { return e.resetAt }

func saveTestAccountState(t *testing.T, mgr *Manager, st AccountState) {
	t.Helper()
	mgr.mu.Lock()
	err := mgr.saveAccountStateLocked(st)
	mgr.mu.Unlock()
	if err != nil {
		t.Fatalf("saveAccountStateLocked(%s:%s): %v", st.Site, st.UID, err)
	}
}

func listTestUpdateItems(t *testing.T, mgr *Manager, siteName, uid string) []UpdateItem {
	t.Helper()
	rows, err := mgr.updatesDB.Query(
		`SELECT site, uid, dyn_id, pub_ts, type, snapshot, discovered_at, auto_task_id FROM update_item WHERE site = ? AND uid = ? ORDER BY dyn_id DESC`,
		siteName, uid,
	)
	if err != nil {
		t.Fatalf("query update_item(%s:%s): %v", siteName, uid, err)
	}
	defer rows.Close()
	var out []UpdateItem
	for rows.Next() {
		var it UpdateItem
		if err := rows.Scan(&it.Site, &it.UID, &it.DynID, &it.PubTs, &it.Type, &it.Snapshot, &it.DiscoveredAt, &it.AutoTaskID); err != nil {
			t.Fatalf("scan update_item: %v", err)
		}
		out = append(out, it)
	}
	return out
}

func TestSubscriptionsV3ToV4MigrationBackupFailureIdempotencyAndDowngrade(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	cacheDir := filepath.Join(root, "cache")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("mkdir dataDir: %v", err)
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatalf("mkdir cacheDir: %v", err)
	}

	subsPath := filepath.Join(dataDir, "subscriptions.json")
	v3Raw := `{
  "version": 3,
  "subscriptions": [
    {
      "id": "sub_v3_multi_0001",
      "name": "三站创作者",
      "note": "v3 fixture",
      "avatar": {"site": "weibo", "uid": "1000000001"},
      "createdAt": "2026-11-20T08:00:00Z",
      "accounts": [
        {"site": "bilibili", "uid": "1001", "name": "B站号", "face": "https://example.invalid/b.jpg", "addedAt": "2026-11-20T08:00:00Z"},
        {"site": "douyin", "uid": "MS4wLjABAAAA__V3Fixture_01", "uniqueId": "dy_v3", "webRid": "778899", "name": "抖音号", "face": "", "addedAt": "2026-11-20T08:00:00Z"},
        {"site": "weibo", "uid": "1000000001", "name": "微博号", "face": "https://wx1.sinaimg.cn/large/wb_v3.jpg", "addedAt": "2026-11-20T08:00:00Z"}
      ],
      "ruleId": "rule-v3-sched",
      "rules": {"scheduledCheck": true, "autoCache": {"enabled": false, "types": ["video", "draw"], "since": ""}, "autoRecord": false}
    }
  ],
  "cacheRules": [
    {"id": "default", "name": "默认", "scheduledCheck": false, "autoCache": false, "autoRecord": false},
    {"id": "rule-v3-sched", "name": "定时检查", "scheduledCheck": true, "autoCache": false, "autoRecord": false}
  ]
}
`
	if err := os.WriteFile(subsPath, []byte(v3Raw), 0o600); err != nil {
		t.Fatalf("write v3 subscriptions.json: %v", err)
	}
	staleBak := subsPath + ".premigrate-2"
	if err := os.WriteFile(staleBak, []byte(`{"version":2,"subscriptions":[]}`), 0o600); err != nil {
		t.Fatalf("write stale .premigrate-2: %v", err)
	}

	libDB, _, _, err := storage.OpenSQLite(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("open library.db: %v", err)
	}
	defer libDB.Close()
	updDB, _, _, err := storage.OpenSQLite(filepath.Join(dataDir, "updates.db"))
	if err != nil {
		t.Fatalf("open updates.db: %v", err)
	}
	defer updDB.Close()

	// 1. Mid-migration failure right before atomic rename must leave BOTH original v3 and .premigrate-3 intact.
	restoreHook := storage.SetBeforeMigrationRenameHookForTest(func(targetPath, tmpPath string) error {
		if filepath.Clean(targetPath) == filepath.Clean(subsPath) {
			return errors.New("simulated pre-rename failure")
		}
		return nil
	})
	if _, err := NewManagerWithUpdatesDB(libDB, updDB, subsPath, nil, nil, nil, settings.Default(cacheDir).Subscription, nil); err == nil {
		restoreHook()
		t.Fatalf("expected simulated mid-migration failure")
	}
	restoreHook()

	if gotOrig, err := os.ReadFile(subsPath); err != nil || string(gotOrig) != v3Raw {
		t.Fatalf("original v3 subscriptions.json altered on mid-migration failure: err=%v", err)
	}
	bakPath := subsPath + ".premigrate-3"
	if gotBak, err := os.ReadFile(bakPath); err != nil || string(gotBak) != v3Raw {
		t.Fatalf("expected intact .premigrate-3 backup after mid-migration failure: err=%v", err)
	}
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatalf("readdir dataDir: %v", err)
	}
	for _, ent := range entries {
		if storage.IsTempName(ent.Name()) {
			t.Fatalf("leftover temp file after failed migration: %s", ent.Name())
		}
	}

	// 2. Clean migration v3 -> v4 succeeds, preserves all 3 existing accounts, leaves updates.db at v2.
	mgr, err := NewManagerWithUpdatesDB(libDB, updDB, subsPath, nil, nil, nil, settings.Default(cacheDir).Subscription, nil)
	if err != nil {
		t.Fatalf("NewManagerWithUpdatesDB v3->v4: %v", err)
	}
	if SubscriptionsSchemaVersion != 4 {
		t.Fatalf("SubscriptionsSchemaVersion = %d, want 4", SubscriptionsSchemaVersion)
	}
	if UpdatesDBSchemaVersion != 2 {
		t.Fatalf("UpdatesDBSchemaVersion = %d, want 2", UpdatesDBSchemaVersion)
	}
	if _, statErr := os.Stat(staleBak); !os.IsNotExist(statErr) {
		t.Fatalf("expected stale .premigrate-2 to be pruned when creating .premigrate-3")
	}
	if _, statErr := os.Stat(filepath.Join(dataDir, "updates.db.premigrate-2")); !os.IsNotExist(statErr) {
		t.Fatalf("updates.db must stay v2 with zero .premigrate-2 backup")
	}
	var updVer int
	if err := updDB.QueryRow(`PRAGMA user_version`).Scan(&updVer); err != nil || updVer != 2 {
		t.Fatalf("updates.db user_version = %d (err=%v), want 2", updVer, err)
	}

	rawAfter, err := os.ReadFile(subsPath)
	if err != nil {
		t.Fatalf("read migrated subscriptions.json: %v", err)
	}
	var diskDoc subscriptionsFile
	if err := json.Unmarshal(rawAfter, &diskDoc); err != nil {
		t.Fatalf("unmarshal migrated subscriptions.json: %v", err)
	}
	if diskDoc.Version != 4 {
		t.Fatalf("migrated subscriptions.json version = %d, want 4", diskDoc.Version)
	}
	if len(diskDoc.Subscriptions) != 1 || len(diskDoc.Subscriptions[0].Accounts) != 3 {
		t.Fatalf("existing v3 accounts lost during migration: %+v", diskDoc.Subscriptions)
	}

	// Link an X account (leading zeros + screen_name + clean public pbs.twimg.com face) and verify updates.db v2 works for X.
	const synthXUID = "180000000000000001"
	linked, err := mgr.LinkAccount("sub_v3_multi_0001", AccountRef{
		Site:     "x",
		UID:      "00" + synthXUID,
		UniqueID: "@Synth_X_Creator",
		Name:     "合成X创作者",
		Face:     "https://pbs.twimg.com/profile_images/1800000001/avatar.jpg?format=jpg&name=orig",
	})
	if err != nil {
		t.Fatalf("LinkAccount x: %v", err)
	}
	if len(linked.Accounts) != 4 {
		t.Fatalf("expected 4 accounts after linking X, got %d", len(linked.Accounts))
	}
	xAcc := linked.Accounts[3]
	if xAcc.Site != "x" || xAcc.UID != synthXUID || xAcc.UniqueID != "Synth_X_Creator" || xAcc.WebRID != "" || xAcc.Face != "https://pbs.twimg.com/profile_images/1800000001/avatar.jpg?format=jpg&name=orig" {
		t.Fatalf("unexpected linked X account: %+v", xAcc)
	}
	saveTestAccountState(t, mgr, AccountState{
		Site:       "x",
		UID:        synthXUID,
		BaselineAt: "2026-11-22T09:00:00Z",
		NewestID:   "1800000000000000500",
		NewestTs:   1710000500,
		LastKind:   "ok",
	})
	if gotSt, ok, err := mgr.GetAccountState("x", synthXUID); err != nil || !ok || gotSt.NewestID != "1800000000000000500" {
		t.Fatalf("GetAccountState(x) on updates.db v2 failed: ok=%v st=%+v err=%v", ok, gotSt, err)
	}

	// 3. Idempotent reload keeps v4 X account and leaves .premigrate-3 untouched.
	mgr2, err := NewManagerWithUpdatesDB(libDB, updDB, subsPath, nil, nil, nil, settings.Default(cacheDir).Subscription, nil)
	if err != nil {
		t.Fatalf("idempotent reload: %v", err)
	}
	if owner, acc, ok := mgr2.GetAccount("x", "00"+synthXUID); !ok || owner.ID != "sub_v3_multi_0001" || acc.UniqueID != "Synth_X_Creator" {
		t.Fatalf("expected X account preserved across restart, got ok=%v owner=%+v acc=%+v", ok, owner, acc)
	}
	if bakAfter, _ := os.ReadFile(bakPath); string(bakAfter) != v3Raw {
		t.Fatalf("idempotent reload modified .premigrate-3")
	}

	// 4. Snapshot round-trip preserves all 4 sites including X account + UniqueID + updates.db watermark.
	snap, err := mgr2.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	snapBytes, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("Marshal snapshot: %v", err)
	}
	parsedSnap, err := ParseSnapshot(snapBytes)
	if err != nil {
		t.Fatalf("ParseSnapshot with X account: %v", err)
	}
	if _, accCount := parsedSnap.Counts(); accCount != 4 {
		t.Fatalf("expected 4 accounts in snapshot, got %d", accCount)
	}
	foundX := false
	for _, a := range parsedSnap.Subscriptions[0].Accounts {
		if a.Site == "x" && a.UID == synthXUID && a.UniqueID == "Synth_X_Creator" {
			foundX = true
		}
	}
	if !foundX {
		t.Fatalf("parsed snapshot missing X account or UniqueID: %+v", parsedSnap.Subscriptions[0].Accounts)
	}

	// 5. Downgrade protection: simulated v3 build (maxSchemaVersion=3) AND storage.GuardDataSchemas(v3)
	// must reject v4 subscriptions.json with NewerDataError and zero disk writes.
	v4DiskBefore, err := os.ReadFile(subsPath)
	if err != nil {
		t.Fatalf("read v4 subscriptions.json: %v", err)
	}
	oldMgr := &Manager{
		db:               libDB,
		updatesDB:        updDB,
		subsPath:         subsPath,
		maxSchemaVersion: 3,
		nowFunc:          time.Now,
	}
	oldMgr.mu.Lock()
	downgradeErr := oldMgr.loadSubscriptionsFromDiskLocked()
	oldMgr.mu.Unlock()
	if !storage.IsNewerData(downgradeErr) {
		t.Fatalf("expected NewerDataError when simulated v3 manager loads v4 subscriptions.json, got %v", downgradeErr)
	}
	v3Guard := []storage.SchemaFile{
		{Name: "subscriptions.json", Kind: storage.SchemaJSON, Current: 3},
		{Name: "updates.db", Kind: storage.SchemaSQLite, Current: 2},
	}
	if _, err := storage.GuardDataSchemas(dataDir, v3Guard); !storage.IsNewerData(err) {
		t.Fatalf("expected NewerDataError when v3 storage guard inspects v4 subscriptions.json, got %v", err)
	}
	v4DiskAfter, _ := os.ReadFile(subsPath)
	if string(v4DiskBefore) != string(v4DiskAfter) {
		t.Fatalf("downgrade check modified v4 subscriptions.json on disk")
	}

	// 6. Future v5 subscriptions.json is rejected with NewerDataError and zero disk mutation.
	v5Dir := t.TempDir()
	v5SubsPath := filepath.Join(v5Dir, "subscriptions.json")
	v5Raw := `{"version":5,"subscriptions":[]}`
	if err := os.WriteFile(v5SubsPath, []byte(v5Raw), 0o600); err != nil {
		t.Fatal(err)
	}
	v5DB, _, _, err := storage.OpenSQLite(filepath.Join(v5Dir, "updates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer v5DB.Close()
	if _, err := NewManagerWithUpdatesDB(v5DB, v5DB, v5SubsPath, nil, nil, nil, settings.Default(v5Dir).Subscription, nil); !storage.IsNewerData(err) {
		t.Fatalf("expected NewerDataError for v5 subscriptions.json, got %v", err)
	}
	if got, _ := os.ReadFile(v5SubsPath); string(got) != v5Raw {
		t.Fatalf("v5 subscriptions.json was modified on disk")
	}
}

func TestXPerSiteBranchesValidationAndFourWayIsolation(t *testing.T) {
	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	updatesDB, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "updates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer updatesDB.Close()
	lib, err := library.Open(db, filepath.Join(tmp, "cache"))
	if err != nil {
		t.Fatal(err)
	}

	cfg := settings.Default(tmp).Subscription
	cfg.Enabled = true
	cfg.IntervalMinutes = 60
	mgr, err := NewManagerWithUpdatesDB(db, updatesDB, filepath.Join(tmp, "subscriptions.json"), lib, nil, nil, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	mgr.SetNowFunc(func() time.Time { return now })

	// 1. Table-driven per-site constants for all 4 sites + zero-fallback unknown rejection.
	cases := []struct {
		siteName string
		wantNorm string
		quota    int
		roundCap int
		interval time.Duration
	}{
		{"bilibili", "bilibili", DefaultBilibiliHourlyQuota, DefaultBilibiliRoundAccountCap, DefaultBilibiliAccountInterval},
		{"douyin", "douyin", DefaultDouyinHourlyQuota, DefaultDouyinRoundAccountCap, DefaultDouyinAccountInterval},
		{"weibo", "weibo", DefaultWeiboHourlyQuota, DefaultWeiboRoundAccountCap, DefaultWeiboAccountInterval},
		{"x", "x", DefaultXHourlyQuota, DefaultXRoundAccountCap, DefaultXAccountInterval},
		{"X", "x", 60, 15, 5 * time.Second},
		{"", "", 0, 0, 0},
		{"unknown", "", 0, 0, 0},
		{"twitter", "", 0, 0, 0},
	}
	for _, tc := range cases {
		t.Run("branch_"+tc.siteName, func(t *testing.T) {
			if got := normalizeSchedulerSite(tc.siteName); got != tc.wantNorm {
				t.Errorf("normalizeSchedulerSite(%q) = %q, want %q", tc.siteName, got, tc.wantNorm)
			}
			if got := SiteHourlyQuotaLimit(tc.siteName); got != tc.quota {
				t.Errorf("SiteHourlyQuotaLimit(%q) = %d, want %d", tc.siteName, got, tc.quota)
			}
			if got := SiteScheduledRoundCap(tc.siteName); got != tc.roundCap {
				t.Errorf("SiteScheduledRoundCap(%q) = %d, want %d", tc.siteName, got, tc.roundCap)
			}
			if got := mgr.SiteAccountInterval(tc.siteName); got != tc.interval {
				t.Errorf("SiteAccountInterval(%q) = %v, want %v", tc.siteName, got, tc.interval)
			}
		})
	}

	// 2. ListSiteSchedulerStates covers all 4 sites including X.
	states := mgr.ListSiteSchedulerStates()
	for _, id := range site.All() {
		if st, ok := states[string(id)]; !ok || st.Site != string(id) {
			t.Fatalf("ListSiteSchedulerStates missing site %q: %+v", id, states)
		}
	}

	// 3. X account normalization, screen_name snapshot, face sanitization, and fallback naming.
	const normUID = "18000000000000009"
	normX, err := NormalizeAndValidateAccount(AccountRef{
		Site:     "X",
		UID:      "000" + normUID,
		UniqueID: "@Synth_Handle_01",
		WebRID:   "should_be_cleared",
		Face:     "https://pbs.twimg.com/profile_images/9/pic.png?format=png&name=orig",
	})
	if err != nil {
		t.Fatalf("NormalizeAndValidateAccount(x): %v", err)
	}
	if normX.Site != "x" || normX.UID != normUID || normX.UniqueID != "Synth_Handle_01" || normX.WebRID != "" || normX.Face != "https://pbs.twimg.com/profile_images/9/pic.png?format=png&name=orig" {
		t.Fatalf("unexpected normalized X account: %+v", normX)
	}
	for _, badFace := range []string{
		"https://video.twimg.com/ext_tw_video/1/vid.mp4?tag=12",
		"https://pbs.twimg.com/profile_images/9/pic.png?sig=SYNTH_SECRET",
		"https://example.invalid/x.jpg?auth_token=SYNTH_TOKEN&ct0=SYNTH_CT0",
	} {
		got, err := NormalizeAndValidateAccount(AccountRef{Site: "x", UID: "12345", Face: badFace})
		if err != nil {
			t.Fatalf("NormalizeAndValidateAccount(x, face=%q): %v", badFace, err)
		}
		if got.Face != "" {
			t.Fatalf("expected unsafe/ephemeral X face %q stripped, got %q", badFace, got.Face)
		}
	}
	for _, badUID := range []string{"", "0", "-1", "abc", "18446744073709551616"} {
		if _, err := NormalizeAndValidateAccount(AccountRef{Site: "x", UID: badUID}); err == nil {
			t.Fatalf("NormalizeAndValidateAccount(x, uid=%q) should fail", badUID)
		}
	}
	if _, err := NormalizeAndValidateAccount(AccountRef{Site: "x", UID: "12345", UniqueID: "bad handle/slash"}); err == nil {
		t.Fatalf("NormalizeAndValidateAccount(x) should reject invalid screen_name")
	}

	subFallbackHandle := Subscription{Accounts: []AccountRef{{Site: "x", UID: "12345", UniqueID: "SynthHandle"}}}
	syncSubFields(&subFallbackHandle, now)
	if subFallbackHandle.Name != "@SynthHandle" {
		t.Fatalf("expected X fallback name @SynthHandle, got %q", subFallbackHandle.Name)
	}
	subFallbackUID := Subscription{Accounts: []AccountRef{{Site: "x", UID: "12345"}}}
	syncSubFields(&subFallbackUID, now)
	if subFallbackUID.Name != "X 用户 12345" {
		t.Fatalf("expected X fallback name 'X 用户 12345', got %q", subFallbackUID.Name)
	}

	// 4. findCachedLibraryKey matches x:dynamic:<id> and never crosses site boundaries.
	xDir := filepath.Join(tmp, "cache", "x", "1800000000000000009", "posts", "1800000000000009001")
	biliDir := filepath.Join(tmp, "cache", "bilibili", "1001", "dynamics", "1800000000000009002")
	_ = os.MkdirAll(xDir, 0o700)
	_ = os.MkdirAll(biliDir, 0o700)
	if _, err := lib.SaveItem(xDir, library.ItemMetadata{
		Version: 1, Site: "x", Kind: "dynamic", ID: "1800000000000009001", Status: "complete",
	}); err != nil {
		t.Fatalf("SaveItem x: %v", err)
	}
	if _, err := lib.SaveItem(biliDir, library.ItemMetadata{
		Version: 1, Site: "bilibili", Kind: "dynamic", ID: "1800000000000009002", Status: "complete",
	}); err != nil {
		t.Fatalf("SaveItem bilibili: %v", err)
	}
	if got := mgr.findCachedLibraryKey("x", "1800000000000009001"); got != "x:dynamic:1800000000000009001" {
		t.Fatalf("findCachedLibraryKey(x) = %q, want x:dynamic:1800000000000009001", got)
	}
	if got := mgr.findCachedLibraryKey("x", "1800000000000009002"); got != "" {
		t.Fatalf("findCachedLibraryKey(x) leaked bilibili item: %q", got)
	}

	// 5. Four-way bidirectional backoff & quota isolation.
	mgr.RecordSiteAutomaticStop("x", site.ErrKindRiskControl)
	if inB, _ := mgr.IsSiteInBackoff("x"); !inB {
		t.Fatalf("expected X in backoff")
	}
	for _, other := range []string{"bilibili", "douyin", "weibo"} {
		if inB, _ := mgr.IsSiteInBackoff(other); inB {
			t.Fatalf("X backoff leaked into %s", other)
		}
	}
	if !mgr.ConsumeSiteQuotaTokens("x", DefaultXHourlyQuota) {
		t.Fatalf("expected X to consume full 60/hr bucket")
	}
	if mgr.CanUseSiteQuotaTokens("x", 1) {
		t.Fatalf("expected X quota exhausted at 60/60")
	}
	for _, other := range []string{"bilibili", "douyin", "weibo"} {
		if !mgr.CanUseSiteQuotaTokens(other, 1) {
			t.Fatalf("X quota exhaustion leaked into %s", other)
		}
	}

	// Reverse direction: bilibili/douyin/weibo backoff & quota exhaustion leave X intact after X backoff expires.
	now = now.Add(3 * time.Hour)
	for _, other := range []string{"bilibili", "douyin", "weibo"} {
		mgr.RecordSiteAutomaticStop(other, site.ErrKindRiskControl)
		if !mgr.ConsumeSiteQuotaTokens(other, SiteHourlyQuotaLimit(other)) {
			t.Fatalf("consume full quota for %s", other)
		}
	}
	if inB, _ := mgr.IsSiteInBackoff("x"); inB {
		t.Fatalf("other sites' backoff leaked into X")
	}
	if err := mgr.SiteAutomaticRequestAllowed("x"); err != nil {
		t.Fatalf("X automatic request should stay allowed when other sites are exhausted/backed off: %v", err)
	}
}

func TestMixedFourSiteSubscriptionRoundXBaselineDeltaPinnedPromotedTypesAndFaultIsolation(t *testing.T) {
	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	updatesDB, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "updates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer updatesDB.Close()
	lib, err := library.Open(db, filepath.Join(tmp, "cache"))
	if err != nil {
		t.Fatal(err)
	}

	// Synthetic Bilibili httptest server.
	var biliMu sync.Mutex
	biliCalls := 0
	biliDynItems := []map[string]any{
		{
			"id_str": "900000000000000101",
			"type":   "DYNAMIC_TYPE_WORD",
			"modules": map[string]any{
				"module_author": map[string]any{"mid": 1001, "name": "B站测试号", "pub_ts": 1710000100},
				"module_dynamic": map[string]any{
					"desc": map[string]any{"text": "B站基线动态"},
				},
			},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		biliMu.Lock()
		biliCalls++
		items := append([]map[string]any(nil), biliDynItems...)
		biliMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"has_more": false, "offset": "", "items": items},
		})
	}))
	defer srv.Close()

	cred, _, err := credential.Open(filepath.Join(tmp, "bilibili.bin"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = cred.SaveManual("SESSDATA=synth_bili_sess; bili_jct=synth_csrf", "rt")
	biliClient := bilibili.NewClient(bilibili.ClientOptions{
		CredStore:      cred,
		Endpoints:      bilibili.Endpoints{APIBase: srv.URL},
		PacingInterval: -1,
		AllowLocalhost: true,
	})
	biliClient.SetWBIKeys("7cd084941338484aae1ad9425b84077c", "4932caff0ff746eab6f01bf08b70ac45")

	cfg := settings.Default(tmp).Subscription
	cfg.Enabled = true
	cfg.IntervalMinutes = 60
	mgr, err := NewManagerWithUpdatesDB(db, updatesDB, filepath.Join(tmp, "subscriptions.json"), lib, nil, biliClient, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	mgr.SetNowFunc(func() time.Time { return now })
	mgr.SetAccountInterval(0)

	const (
		dyUID = "MS4wLjABAAAA__Mixed4_DY_01"
		wbUID = "1000000001"
		xUID1 = "1800000000000000001"
		xUID2 = "1800000000000000002"
	)

	var mu sync.Mutex
	phase := "baseline"
	authMap := map[site.ID]bool{
		site.Bilibili: true,
		site.Douyin:   true,
		site.Weibo:    true,
		site.X:        true,
	}
	mgr.SetSiteAuthCheck(func(id site.ID) bool {
		mu.Lock()
		defer mu.Unlock()
		return authMap[id]
	})

	liveHintCount := 0
	mgr.SetOnLiveHint(func(acc AccountRef, roomID, webRID string) {
		mu.Lock()
		liveHintCount++
		mu.Unlock()
	})
	var postRoundLiveSites []string
	mgr.SetSiteLiveCheck(func(ctx context.Context, accounts []AccountRef) error {
		mu.Lock()
		for _, a := range accounts {
			postRoundLiveSites = append(postRoundLiveSites, a.Site+":"+a.UID)
		}
		mu.Unlock()
		return nil
	})

	var autoEnqueued []UpdateItem
	mgr.RegisterAutoCacheEnqueuer(site.X, func(ctx context.Context, item UpdateItem) (string, error) {
		mu.Lock()
		autoEnqueued = append(autoEnqueued, item)
		id := fmt.Sprintf("task_x_%d", len(autoEnqueued))
		mu.Unlock()
		return id, nil
	})

	dyFetcher := &syntheticSiteFetcher{
		siteID: site.Douyin,
		handler: func(ctx context.Context, uid, cursor string) (site.CreatorPostsPage, error) {
			return site.CreatorPostsPage{
				AuthorName: "抖音测试号",
				Posts: []site.CreatorPost{
					{ID: "7300000000000000001", PubTs: 1710000100, Type: "video", Title: "抖音基线"},
				},
			}, nil
		},
	}
	wbFetcher := &syntheticSiteFetcher{
		siteID: site.Weibo,
		handler: func(ctx context.Context, uid, cursor string) (site.CreatorPostsPage, error) {
			return site.CreatorPostsPage{
				AuthorName: "微博测试号",
				Posts: []site.CreatorPost{
					{ID: "5000000000000001", PubTs: 1710000100, Type: "word", Title: "微博基线"},
				},
			}, nil
		},
	}
	xFetcher := &syntheticSiteFetcher{
		siteID: site.X,
		handler: func(ctx context.Context, uid, cursor string) (site.CreatorPostsPage, error) {
			mu.Lock()
			curPhase := phase
			mu.Unlock()
			switch curPhase {
			case "baseline":
				if uid == xUID1 {
					return site.CreatorPostsPage{
						AuthorName: "合成X甲",
						UniqueID:   "@SynthXAlpha",
						AuthorFace: "https://pbs.twimg.com/profile_images/1/a.jpg?format=jpg&name=orig",
						TotalWorks: 42,
						LiveRoomID: "should_be_ignored",
						LiveWebRID: "should_be_ignored",
						Posts: []site.CreatorPost{
							{ID: "1700000000000000010", PubTs: 1700000010, Type: "word", Title: "旧置顶帖", IsTop: true},
							{ID: "promoted-tweet-1", PubTs: 1710000999, Type: "word", Title: "推广卡片"},
							{ID: "1800000000000000100", PubTs: 1710000100, Type: "word", Title: "基线帖"},
						},
					}, nil
				}
				return site.CreatorPostsPage{
					AuthorName: "合成X乙",
					UniqueID:   "SynthXBeta",
					Posts: []site.CreatorPost{
						{ID: "1800000000000000200", PubTs: 1710000200, Type: "word", Title: "乙基线帖"},
					},
				}, nil

			case "delta":
				if uid == xUID2 {
					return site.CreatorPostsPage{
						Posts: []site.CreatorPost{
							{ID: "1800000000000000200", PubTs: 1710000200, Type: "word", Title: "乙基线帖"},
						},
					}, nil
				}
				if cursor == "" {
					return site.CreatorPostsPage{
						AuthorName: "合成X甲改名",
						UniqueID:   "SynthXAlphaRenamed",
						HasMore:    true,
						// Cursor intentionally contains ":" to ensure Weibo cursor stagnation logic does NOT stop X.
						NextCursor: "V2:opaque:page2",
						Posts: []site.CreatorPost{
							// Old pinned post MUST NOT stop scan early or re-enter update_item.
							{ID: "1700000000000000010", PubTs: 1700000010, Type: "word", Title: "旧置顶帖", IsTop: true},
							// Retweet: outer rest_id > baseline, inner ephemeral URL/stats/cookie scrubbed, pbs.twimg.com kept.
							{
								ID:       "1800000000000000106",
								PubTs:    1710000160,
								Type:     "retweet",
								Title:    "转帖",
								CoverURL: "https://pbs.twimg.com/media/SYNTH106.jpg?format=jpg&name=orig",
								Snapshot: `{"id":"1800000000000000106","cover":"https://pbs.twimg.com/media/SYNTH106.jpg?format=jpg&name=orig","videoUrl":"https://video.twimg.com/ext_tw_video/1/vid.mp4?tag=12","favorite_count":99,"cookie":"auth_token=SYNTH_COOKIE_SENTINEL","note":"auth_token=SYNTH_COOKIE_SENTINEL"}`,
							},
							// Quote -> forward
							{ID: "1800000000000000105", PubTs: 1710000150, Type: "quote", Title: "引用帖"},
							// Pre-cached video -> update_item video, auto-cache skips already-cached x:dynamic:<id>
							{ID: "1800000000000000104", PubTs: 1710000140, Type: "video", Title: "已缓存视频"},
							// Promoted post disguised with numeric ID + promotedMetadata -> skipped
							{ID: "1800000000000000999", PubTs: 1710000999, Type: "word", Title: "推广", Snapshot: `{"promotedMetadata":{"impressionId":"synth"}}`},
						},
					}, nil
				}
				if cursor == "V2:opaque:page2" {
					return site.CreatorPostsPage{
						HasMore:    true,
						NextCursor: "V2:opaque:page3",
						Posts: []site.CreatorPost{
							{ID: "1800000000000000103", PubTs: 1710000130, Type: "gif", Title: "动图帖"},
							{ID: "1800000000000000102", PubTs: 1710000120, Type: "mixed", Title: "混排帖"},
							// Same PubTs as baseline 1800000000000000100, higher uint64 rest_id -> detected as NEW!
							{ID: "1800000000000000101", PubTs: 1710000100, Type: "poll", Title: "投票帖"},
							// Baseline hit -> stops cleanly.
							{ID: "1800000000000000100", PubTs: 1710000100, Type: "word", Title: "基线帖"},
						},
					}, nil
				}
				t.Errorf("X fetcher must not request beyond page 2, got cursor=%q", cursor)
				return site.CreatorPostsPage{}, nil

			case "gap_two_pages":
				if uid == xUID2 {
					return site.CreatorPostsPage{
						Posts: []site.CreatorPost{{ID: "1800000000000000200", PubTs: 1710000200, Type: "word"}},
					}, nil
				}
				if cursor == "" {
					return site.CreatorPostsPage{
						HasMore:    true,
						NextCursor: "cursor_p2",
						Posts:      []site.CreatorPost{{ID: "1800000000000000120", PubTs: 1710000220, Type: "word", Title: "新1"}},
					}, nil
				}
				if cursor == "cursor_p2" {
					return site.CreatorPostsPage{
						HasMore:    true,
						NextCursor: "cursor_p3",
						Posts:      []site.CreatorPost{{ID: "1800000000000000119", PubTs: 1710000219, Type: "word", Title: "新2"}},
					}, nil
				}
				t.Errorf("X incremental check exceeded 2 pages (cursor=%q)", cursor)
				return site.CreatorPostsPage{}, nil

			case "uncertain_empty":
				if uid == xUID1 {
					return site.CreatorPostsPage{
						UncertainEmpty:  true,
						UncertainReason: "返回假空壳，未移动起点",
					}, nil
				}
				return site.CreatorPostsPage{
					Posts: []site.CreatorPost{{ID: "1800000000000000200", PubTs: 1710000200, Type: "word"}},
				}, nil

			case "risk_control":
				return site.CreatorPostsPage{}, &site.Error{Site: site.X, Kind: site.ErrKindRiskControl, Message: "X 触发风控"}

			case "schema_changed":
				return site.CreatorPostsPage{}, &site.Error{Site: site.X, Kind: site.ErrKindSchemaChanged, Message: "X 前端契约变化"}
			}
			return site.CreatorPostsPage{}, nil
		},
	}

	mgr.RegisterFetcher(dyFetcher)
	mgr.RegisterFetcher(wbFetcher)
	mgr.RegisterFetcher(xFetcher)

	ruleAll, err := mgr.SaveRule(CacheRule{
		Name:           "X全类型自动缓存",
		ScheduledCheck: true,
		AutoCache:      true,
		Types:          []string{"video", "draw", "word", "forward"},
		AutoRecord:     true,
	})
	if err != nil {
		t.Fatalf("SaveRule: %v", err)
	}
	ruleSched, err := mgr.SaveRule(CacheRule{
		Name:           "仅定时检查",
		ScheduledCheck: true,
	})
	if err != nil {
		t.Fatalf("SaveRule sched: %v", err)
	}

	// Set clock earlier than post PubTs (1710000100) so ruleAutoCache.Since covers new posts.
	now = time.Unix(1710000050, 0).UTC()
	sub1, err := mgr.Create("四站综合创作者", "", []AccountRef{
		{Site: "bilibili", UID: "1001", Name: "B站测试号"},
		{Site: "douyin", UID: dyUID, Name: "抖音测试号"},
		{Site: "weibo", UID: wbUID, Name: "微博测试号"},
		{Site: "x", UID: xUID1, Name: "合成X甲"},
	}, SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatalf("Create 4-site sub: %v", err)
	}
	if _, err := mgr.SetSubscriptionRule(sub1.ID, ruleAll.ID); err != nil {
		t.Fatalf("SetSubscriptionRule sub1: %v", err)
	}
	sub2, err := mgr.Create("X二号创作者", "", []AccountRef{
		{Site: "x", UID: xUID2, Name: "合成X乙"},
	}, SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatalf("Create X2 sub: %v", err)
	}
	if _, err := mgr.SetSubscriptionRule(sub2.ID, ruleSched.ID); err != nil {
		t.Fatalf("SetSubscriptionRule sub2: %v", err)
	}

	// Pre-cache one X video in library to verify findCachedLibraryKey("x", id) prevents duplicate auto-cache.
	cachedXDir := filepath.Join(tmp, "cache", "x", xUID1, "posts", "1800000000000000104")
	_ = os.MkdirAll(cachedXDir, 0o700)
	if _, err := lib.SaveItem(cachedXDir, library.ItemMetadata{
		Version: 1, Site: "x", Kind: "dynamic", ID: "1800000000000000104", Status: "complete",
	}); err != nil {
		t.Fatalf("SaveItem pre-cached X video: %v", err)
	}

	ctx := context.Background()

	// --- Round 1: Baseline ---
	now = time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	sumBase, err := mgr.RunScheduledRound(ctx)
	if err != nil {
		t.Fatalf("Round 1 baseline: %v", err)
	}
	if sumBase.BaselineInitialized != 5 || sumBase.NewVideos != 0 || sumBase.NewDynamics != 0 {
		t.Fatalf("unexpected baseline summary: %+v", sumBase)
	}
	stX1, ok, err := mgr.GetAccountState("x", xUID1)
	if err != nil || !ok || stX1.NewestID != "1800000000000000100" || stX1.BaselineAt == "" {
		t.Fatalf("unexpected X1 baseline state: ok=%v st=%+v err=%v", ok, stX1, err)
	}
	if !mgr.wasPinnedOnBaseline("x", xUID1, "1700000000000000010") {
		t.Fatalf("expected X1 pinned post recorded in account_baseline_pinned")
	}
	if items := listTestUpdateItems(t, mgr, "x", xUID1); len(items) != 0 {
		t.Fatalf("baseline must not insert update_item rows, got %d", len(items))
	}
	if _, accX1, ok := mgr.GetAccount("x", xUID1); !ok || accX1.UniqueID != "SynthXAlpha" || accX1.WebRID != "" {
		t.Fatalf("expected X1 UniqueID updated on page 1 without WebRID, got %+v", accX1)
	}
	if liveHintCount != 0 {
		t.Fatalf("X page 1 must never fire onLiveHint, got %d", liveHintCount)
	}
	for _, entry := range postRoundLiveSites {
		if strings.HasPrefix(entry, "x:") || strings.HasPrefix(entry, "weibo:") {
			t.Fatalf("post-round live check must exclude X and Weibo, got %v", postRoundLiveSites)
		}
	}

	// --- Round 2: Incremental delta (2 pages, pinned non-stop, uint64 tie-break, types, scrubbing, auto-cache) ---
	mu.Lock()
	phase = "delta"
	mu.Unlock()
	xFetcher.resetCalls()
	now = now.Add(time.Hour)

	sumDelta, err := mgr.RunScheduledRound(ctx)
	if err != nil {
		t.Fatalf("Round 2 delta: %v", err)
	}
	if len(sumDelta.GapAccounts) != 0 {
		t.Fatalf("expected clean baseline hit on page 2 with no gap, got %v", sumDelta.GapAccounts)
	}
	x1Calls := 0
	for _, c := range xFetcher.callSnapshot() {
		if strings.HasPrefix(c, xUID1+"@") {
			x1Calls++
		}
	}
	if x1Calls != 2 {
		t.Fatalf("expected 2 pages fetched for X1, got %d (%v)", x1Calls, xFetcher.callSnapshot())
	}

	stX1, _, _ = mgr.GetAccountState("x", xUID1)
	if stX1.NewestID != "1800000000000000106" {
		t.Fatalf("X1 watermark = %q, want 1800000000000000106 (must not regress to pinned or advance to promoted)", stX1.NewestID)
	}
	items := listTestUpdateItems(t, mgr, "x", xUID1)
	gotTypes := make(map[string]string, len(items))
	var snap106 string
	for _, it := range items {
		gotTypes[it.DynID] = it.Type
		if it.DynID == "1800000000000000106" {
			snap106 = it.Snapshot
		}
	}
	wantTypes := map[string]string{
		"1800000000000000106": "forward",
		"1800000000000000105": "forward",
		"1800000000000000104": "video",
		"1800000000000000103": "draw",
		"1800000000000000102": "draw",
		"1800000000000000101": "word",
	}
	if len(gotTypes) != len(wantTypes) {
		t.Fatalf("unexpected X update_item set: got %v, want %v", gotTypes, wantTypes)
	}
	for id, wantTyp := range wantTypes {
		if gotTypes[id] != wantTyp {
			t.Errorf("update_item[%s].Type = %q, want %q", id, gotTypes[id], wantTyp)
		}
	}
	var snapObj map[string]any
	if err := json.Unmarshal([]byte(snap106), &snapObj); err != nil {
		t.Fatalf("unmarshal X snapshot: %v (raw=%s)", err, snap106)
	}
	if snapObj["cover"] != "https://pbs.twimg.com/media/SYNTH106.jpg?format=jpg&name=orig" {
		t.Fatalf("expected public pbs.twimg.com image URL preserved in X snapshot, got %v (raw=%s)", snapObj["cover"], snap106)
	}
	for _, forbidden := range []string{"video.twimg.com", "favorite_count", "SYNTH_COOKIE_SENTINEL", "auth_token"} {
		if strings.Contains(snap106, forbidden) {
			t.Fatalf("X snapshot leaked forbidden token %q: %s", forbidden, snap106)
		}
	}

	// Auto-cache must enqueue the 5 uncached X items and skip pre-cached 1800000000000000104.
	enqueuedIDs := make([]string, 0, len(autoEnqueued))
	for _, it := range autoEnqueued {
		enqueuedIDs = append(enqueuedIDs, it.DynID)
	}
	if len(enqueuedIDs) != 5 || slices.Contains(enqueuedIDs, "1800000000000000104") {
		t.Fatalf("expected 5 uncached X items enqueued (skipping pre-cached 104), got %v", enqueuedIDs)
	}

	// Verify feed canonical URL for X uses https://x.com/i/web/status/<id>.
	feedPage, err := lib.FeedPage(library.FeedPageRequest{Site: "x", Limit: 20})
	if err != nil {
		t.Fatalf("FeedPage(x): %v", err)
	}
	foundFeed106 := false
	for _, fe := range feedPage.Items {
		if fe.RawID == "1800000000000000106" && fe.CanonicalURL == "https://x.com/i/web/status/1800000000000000106" && fe.Kind == "dynamic" {
			foundFeed106 = true
		}
	}
	if !foundFeed106 {
		t.Fatalf("expected X feed entry with canonical status URL, got %+v", feedPage.Items)
	}

	if _, accX1, ok := mgr.GetAccount("x", xUID1); !ok || accX1.UniqueID != "SynthXAlphaRenamed" {
		t.Fatalf("expected X1 UniqueID updated on incremental check, got %+v", accX1)
	}

	// --- Round 2b: Hard cap at 2 pages + gapDetected ---
	mu.Lock()
	phase = "gap_two_pages"
	mu.Unlock()
	xFetcher.resetCalls()
	now = now.Add(time.Hour)
	sumGap, err := mgr.RunScheduledRound(ctx)
	if err != nil {
		t.Fatalf("Round 2b gap: %v", err)
	}
	if !slices.Contains(sumGap.GapAccounts, "x:"+xUID1) {
		t.Fatalf("expected GapAccounts to include x:%s when 2-page cap reached before baseline, got %v", xUID1, sumGap.GapAccounts)
	}

	// --- Round 3: UncertainEmpty fake shell leaves watermark unchanged ---
	beforeSt, _, _ := mgr.GetAccountState("x", xUID1)
	mu.Lock()
	phase = "uncertain_empty"
	mu.Unlock()
	now = now.Add(time.Hour)
	sumUnc, err := mgr.RunScheduledRound(ctx)
	if err != nil {
		t.Fatalf("Round 3 uncertain_empty: %v", err)
	}
	if !slices.Contains(sumUnc.IncompleteAccounts, "x:"+xUID1) {
		t.Fatalf("expected IncompleteAccounts to include x:%s, got %+v", xUID1, sumUnc)
	}
	afterSt, _, _ := mgr.GetAccountState("x", xUID1)
	if afterSt.LastKind != "incomplete" || !strings.HasPrefix(afterSt.Note, "本轮结果不完整") {
		t.Fatalf("unexpected X1 incomplete state: %+v", afterSt)
	}
	if afterSt.NewestID != beforeSt.NewestID || afterSt.NewestTs != beforeSt.NewestTs {
		t.Fatalf("UncertainEmpty must not move X watermark: before=%+v after=%+v", beforeSt, afterSt)
	}

	// --- Round 4a: X schema_changed stops X only, does NOT set exponential backoff, other 3 sites proceed ---
	mu.Lock()
	phase = "schema_changed"
	mu.Unlock()
	biliMu.Lock()
	biliBefore := biliCalls
	biliMu.Unlock()
	dyFetcher.resetCalls()
	wbFetcher.resetCalls()
	xFetcher.resetCalls()
	now = now.Add(time.Hour)

	sumSchema, err := mgr.RunScheduledRound(ctx)
	if err != nil {
		t.Fatalf("Round 4a schema_changed: %v", err)
	}
	if sumSchema.SiteStopped["x"] != site.ErrKindSchemaChanged {
		t.Fatalf("expected SiteStopped[x]=schema_changed, got %+v", sumSchema.SiteStopped)
	}
	if len(xFetcher.callSnapshot()) != 1 {
		t.Fatalf("expected X to halt after first account on schema_changed, got calls=%v", xFetcher.callSnapshot())
	}
	if inB, _ := mgr.IsSiteInBackoff("x"); inB {
		t.Fatalf("schema_changed must not put X into exponential backoff")
	}
	if st := mgr.GetSiteSchedulerState("x"); st.LastStopReason != site.ErrKindSchemaChanged {
		t.Fatalf("expected LastStopReason=schema_changed, got %+v", st)
	}
	biliMu.Lock()
	biliDelta := biliCalls - biliBefore
	biliMu.Unlock()
	if biliDelta == 0 || len(dyFetcher.callSnapshot()) == 0 || len(wbFetcher.callSnapshot()) == 0 {
		t.Fatalf("X schema_changed must not block bilibili/douyin/weibo: bili=%d dy=%v wb=%v", biliDelta, dyFetcher.callSnapshot(), wbFetcher.callSnapshot())
	}

	// --- Round 4b: X risk_control stops remaining X accounts & sets X backoff while other 3 sites proceed ---
	mu.Lock()
	phase = "risk_control"
	mu.Unlock()
	biliMu.Lock()
	biliBefore = biliCalls
	biliMu.Unlock()
	dyFetcher.resetCalls()
	wbFetcher.resetCalls()
	xFetcher.resetCalls()
	now = now.Add(time.Hour)

	sumRisk, err := mgr.RunScheduledRound(ctx)
	if err != nil {
		t.Fatalf("Round 4b risk_control: %v", err)
	}
	if sumRisk.SiteStopped["x"] != site.ErrKindRiskControl {
		t.Fatalf("expected SiteStopped[x]=risk_control, got %+v", sumRisk.SiteStopped)
	}
	if len(xFetcher.callSnapshot()) != 1 {
		t.Fatalf("expected X to halt after first account on risk_control, got calls=%v", xFetcher.callSnapshot())
	}
	if inB, _ := mgr.IsSiteInBackoff("x"); !inB {
		t.Fatalf("expected X in backoff after risk_control")
	}
	biliMu.Lock()
	biliDelta = biliCalls - biliBefore
	biliMu.Unlock()
	if biliDelta == 0 || len(dyFetcher.callSnapshot()) == 0 || len(wbFetcher.callSnapshot()) == 0 {
		t.Fatalf("X risk_control must not stop bilibili/douyin/weibo: bili=%d dy=%v wb=%v", biliDelta, dyFetcher.callSnapshot(), wbFetcher.callSnapshot())
	}

	// --- Round 5: Unlogged X skipped cleanly with 0 requests while other 3 sites proceed ---
	now = now.Add(4 * time.Hour)
	mu.Lock()
	authMap[site.X] = false
	mu.Unlock()
	xFetcher.resetCalls()
	dyFetcher.resetCalls()
	wbFetcher.resetCalls()

	sumUnauth, err := mgr.RunScheduledRound(ctx)
	if err != nil {
		t.Fatalf("Round 5 unlogged X: %v", err)
	}
	if !slices.Contains(sumUnauth.SkippedUnauthSites, "x") {
		t.Fatalf("expected SkippedUnauthSites to include x, got %+v", sumUnauth)
	}
	if len(xFetcher.callSnapshot()) != 0 {
		t.Fatalf("unlogged X must make 0 fetcher calls, got %v", xFetcher.callSnapshot())
	}
	if st, _, _ := mgr.GetAccountState("x", xUID1); st.LastKind != "skipped_unauth" || st.Note != "未登录 X，已跳过" {
		t.Fatalf("unexpected unlogged X account state: %+v", st)
	}
	if len(dyFetcher.callSnapshot()) == 0 || len(wbFetcher.callSnapshot()) == 0 {
		t.Fatalf("unlogged X must not skip other logged-in sites")
	}
}

func TestXSchedulerQuotaRoundCapPacingResetBackoffAndManualCheckRules(t *testing.T) {
	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	updatesDB, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "updates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer updatesDB.Close()

	cfg := settings.Default(tmp).Subscription
	cfg.Enabled = true
	cfg.IntervalMinutes = 60
	mgr, err := NewManagerWithUpdatesDB(db, updatesDB, filepath.Join(tmp, "subscriptions.json"), nil, nil, nil, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	mgr.SetNowFunc(func() time.Time { return now })
	mgr.SetRateLimitJitterFunc(func() time.Duration { return 23 * time.Second })

	var sleepLog []time.Duration
	mgr.SetSleepFunc(func(ctx context.Context, d time.Duration) error {
		sleepLog = append(sleepLog, d)
		return nil
	})
	mgr.SetSiteAuthCheck(func(id site.ID) bool { return id == site.X })

	var failMu sync.Mutex
	var nextErr error
	xFetcher := &syntheticSiteFetcher{
		siteID: site.X,
		handler: func(ctx context.Context, uid, cursor string) (site.CreatorPostsPage, error) {
			failMu.Lock()
			errToReturn := nextErr
			failMu.Unlock()
			if errToReturn != nil {
				return site.CreatorPostsPage{}, errToReturn
			}
			return site.CreatorPostsPage{
				AuthorName: "X_" + uid,
				Posts: []site.CreatorPost{
					{ID: "1800000000000000500", PubTs: 1710000500, Type: "word", Title: "帖"},
				},
			}, nil
		},
	}
	mgr.RegisterFetcher(xFetcher)

	ruleSched, err := mgr.SaveRule(CacheRule{Name: "X定时", ScheduledCheck: true})
	if err != nil {
		t.Fatalf("SaveRule: %v", err)
	}

	xUID := func(i int) string { return fmt.Sprintf("18000000000000010%02d", i) }
	for i := 0; i < 20; i++ {
		sub, err := mgr.Create(
			fmt.Sprintf("X创作者%02d", i),
			"",
			[]AccountRef{{Site: "x", UID: xUID(i)}},
			SubRules{ScheduledCheck: true},
		)
		if err != nil {
			t.Fatalf("Create X sub %d: %v", i, err)
		}
		if _, err := mgr.SetSubscriptionRule(sub.ID, ruleSched.ID); err != nil {
			t.Fatalf("SetSubscriptionRule %d: %v", i, err)
		}
	}

	ctx := context.Background()

	// 1. Scheduled round caps at 15 accounts with 14 sleeps of 5s.
	sum1, err := mgr.RunScheduledRound(ctx)
	if err != nil {
		t.Fatalf("RunScheduledRound 1: %v", err)
	}
	if sum1.CheckedCreators != DefaultXRoundAccountCap {
		t.Fatalf("CheckedCreators = %d, want %d", sum1.CheckedCreators, DefaultXRoundAccountCap)
	}
	if len(sleepLog) != DefaultXRoundAccountCap-1 {
		t.Fatalf("expected %d inter-account sleeps, got %d (%v)", DefaultXRoundAccountCap-1, len(sleepLog), sleepLog)
	}
	for i, d := range sleepLog {
		if d != DefaultXAccountInterval {
			t.Fatalf("sleep[%d] = %v, want %v", i, d, DefaultXAccountInterval)
		}
	}

	// 2. Pre-flight requiredTokens = 2*15 = 30 against 60/hr bucket.
	// Using 31 tokens leaves 29 (< 30) -> scheduled round must defer X with 0 calls.
	if !mgr.ConsumeSiteQuotaTokens("x", 31) {
		t.Fatalf("consume 31 tokens failed")
	}
	xFetcher.resetCalls()
	sumQuota, err := mgr.RunScheduledRound(ctx)
	if err != nil {
		t.Fatalf("RunScheduledRound under quota: %v", err)
	}
	if !sumQuota.DeferredByQuota || !slices.Contains(sumQuota.SiteDeferredByQuota, "x") || len(xFetcher.callSnapshot()) != 0 {
		t.Fatalf("expected X deferred by quota with 0 calls, got sum=%+v calls=%v", sumQuota, xFetcher.callSnapshot())
	}

	// Fill bucket completely to 60/60 -> manual check still succeeds and adds 0 quota tokens!
	if !mgr.ConsumeSiteQuotaTokens("x", DefaultXHourlyQuota-31) {
		t.Fatalf("fill remaining X tokens failed")
	}
	if mgr.CanUseSiteQuotaTokens("x", 1) {
		t.Fatalf("expected X bucket full at 60/60")
	}
	owner0, ok := mgr.OwnerOf("x", xUID(0))
	if !ok {
		t.Fatalf("OwnerOf(x0) not found")
	}
	xFetcher.resetCalls()
	sumManual, err := mgr.CheckAccount(ctx, owner0.ID, "x", xUID(0))
	if err != nil || sumManual.CheckedCreators != 1 || len(xFetcher.callSnapshot()) != 1 {
		t.Fatalf("manual X check must bypass 60/hr quota, got sum=%+v calls=%v err=%v", sumManual, xFetcher.callSnapshot(), err)
	}
	if st := mgr.GetSiteSchedulerState("x"); len(st.TokenTimestamps) != DefaultXHourlyQuota {
		t.Fatalf("manual X check must not consume/add hourly quota tokens, got %d", len(st.TokenTimestamps))
	}

	// 3. Rate-limit error with RateLimitResetAt() backs off until reset + deterministic 23s jitter,
	// AND blocks BOTH scheduled and manual checks until backoff expires!
	now = now.Add(2 * time.Hour)
	resetAt := now.Add(15 * time.Minute)
	failMu.Lock()
	nextErr = &syntheticResetRateLimitErr{
		siteErr: &site.Error{Site: site.X, Kind: site.ErrKindRateLimited, Message: "HTTP 429"},
		resetAt: resetAt,
	}
	failMu.Unlock()
	sumRL, err := mgr.RunScheduledRound(ctx)
	if err != nil {
		t.Fatalf("RunScheduledRound rate_limited: %v", err)
	}
	if sumRL.SiteStopped["x"] != site.ErrKindRateLimited {
		t.Fatalf("expected SiteStopped[x]=rate_limited, got %+v", sumRL.SiteStopped)
	}
	inB, until := mgr.IsSiteInBackoff("x")
	wantUntil := resetAt.Add(23 * time.Second)
	if !inB || !until.Equal(wantUntil) {
		t.Fatalf("rate_limited backoff = (%v, %v), want (true, %v)", inB, until, wantUntil)
	}

	// Manual check while rate-limit brake is active must make 0 calls and report DeferredByBackoff.
	failMu.Lock()
	nextErr = nil
	failMu.Unlock()
	xFetcher.resetCalls()
	sumManualBlocked, err := mgr.CheckAccount(ctx, owner0.ID, "x", xUID(0))
	if err != nil {
		t.Fatalf("manual check under X backoff returned unexpected err: %v", err)
	}
	if !sumManualBlocked.DeferredByBackoff || !slices.Contains(sumManualBlocked.SiteDeferredByBackoff, "x") || len(xFetcher.callSnapshot()) != 0 {
		t.Fatalf("manual X check must honor rate-limit brake with 0 calls, got sum=%+v calls=%v", sumManualBlocked, xFetcher.callSnapshot())
	}

	// Once clock passes reset + jitter, manual check resumes cleanly.
	now = wantUntil.Add(time.Second)
	xFetcher.resetCalls()
	sumManualResumed, err := mgr.CheckAccount(ctx, owner0.ID, "x", xUID(0))
	if err != nil || sumManualResumed.CheckedCreators != 1 || len(xFetcher.callSnapshot()) != 1 {
		t.Fatalf("manual X check should resume after reset+jitter, got sum=%+v calls=%v err=%v", sumManualResumed, xFetcher.callSnapshot(), err)
	}

	// 4. Risk-control exponential backoff doubles up to 24h cap and also blocks manual check.
	wantHours := []time.Duration{2 * time.Hour, 4 * time.Hour, 8 * time.Hour, 16 * time.Hour, 24 * time.Hour, 24 * time.Hour}
	for i, wantDur := range wantHours {
		now = now.Add(2 * time.Second)
		mgr.RecordSiteAutomaticStop("x", site.ErrKindRiskControl)
		inB, until := mgr.IsSiteInBackoff("x")
		if !inB || !until.Equal(now.Add(wantDur)) {
			t.Fatalf("risk_control step %d: backoff=(%v, %v), want %v", i+1, inB, until, now.Add(wantDur))
		}
	}
	xFetcher.resetCalls()
	sumRiskBlocked, err := mgr.CheckAccount(ctx, owner0.ID, "x", xUID(0))
	if err != nil || !sumRiskBlocked.DeferredByBackoff || len(xFetcher.callSnapshot()) != 0 {
		t.Fatalf("manual X check must honor risk_control circuit breaker with 0 calls, got sum=%+v calls=%v err=%v", sumRiskBlocked, xFetcher.callSnapshot(), err)
	}
}

func TestXCheckPlanLiveUnsupportedNoteAndFollowingImport(t *testing.T) {
	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	updatesDB, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "updates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer updatesDB.Close()

	cfg := settings.Default(tmp).Subscription
	cfg.Enabled = true
	cfg.IntervalMinutes = 60
	mgr, err := NewManagerWithUpdatesDB(db, updatesDB, filepath.Join(tmp, "subscriptions.json"), nil, nil, nil, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 22, 14, 0, 0, 0, time.UTC)
	mgr.SetNowFunc(func() time.Time { return now })

	xAuthed := true
	mgr.SetSiteAuthCheck(func(id site.ID) bool {
		if id == site.X {
			return xAuthed
		}
		return id == site.Bilibili
	})

	ruleLive, err := mgr.SaveRule(CacheRule{Name: "定时含录播", ScheduledCheck: true, AutoRecord: true})
	if err != nil {
		t.Fatalf("SaveRule live: %v", err)
	}
	ruleSched, err := mgr.SaveRule(CacheRule{Name: "仅定时", ScheduledCheck: true})
	if err != nil {
		t.Fatalf("SaveRule sched: %v", err)
	}

	// Create Bilibili sub with AutoRecord=true, plus 16 X subs (first with AutoRecord=true, rest false).
	biliSub, err := mgr.Create("B站播主", "", []AccountRef{{Site: "bilibili", UID: "1001", Name: "B站播主"}}, SubRules{ScheduledCheck: true, AutoRecord: true})
	if err != nil {
		t.Fatalf("Create bilibili sub: %v", err)
	}
	if _, err := mgr.SetSubscriptionRule(biliSub.ID, ruleLive.ID); err != nil {
		t.Fatalf("SetSubscriptionRule bilibili: %v", err)
	}
	for i := 0; i < 16; i++ {
		uid := fmt.Sprintf("18000000000000020%02d", i)
		sub, err := mgr.Create(
			fmt.Sprintf("X账号%02d", i),
			"",
			[]AccountRef{{Site: "x", UID: uid, UniqueID: fmt.Sprintf("synth_plan_%02d", i)}},
			SubRules{ScheduledCheck: true, AutoRecord: i == 0},
		)
		if err != nil {
			t.Fatalf("Create X sub %d: %v", i, err)
		}
		targetRule := ruleSched.ID
		if i == 0 {
			targetRule = ruleLive.ID
		}
		if _, err := mgr.SetSubscriptionRule(sub.ID, targetRule); err != nil {
			t.Fatalf("SetSubscriptionRule X %d: %v", i, err)
		}
	}

	// 1. ListAutoRecordAccounts excludes X while retaining Bilibili.
	if xAuto := mgr.ListAutoRecordAccounts("x"); len(xAuto) != 0 {
		t.Fatalf("ListAutoRecordAccounts(x) must be empty, got %+v", xAuto)
	}
	allAuto := mgr.ListAutoRecordAccounts("")
	if len(allAuto) != 1 || allAuto[0].Site != "bilibili" {
		t.Fatalf("ListAutoRecordAccounts(\"\") must only contain bilibili, got %+v", allAuto)
	}

	// 2. CheckPlan lists all 16 X accounts, schedules #16 into round 2 (cap=15), and sets XLiveUnsupportedMessage.
	nextTick := now.Add(30 * time.Minute)
	mgr.mu.Lock()
	mgr.nextRunAt = nextTick
	mgr.mu.Unlock()
	plan := mgr.CheckPlan()
	xEntries := 0
	foundRound2 := false
	foundLiveNote := false
	for _, entry := range plan {
		if entry.Site != "x" {
			continue
		}
		xEntries++
		if entry.AutoRecord {
			t.Fatalf("X CheckPlan entry must never report AutoRecord=true: %+v", entry)
		}
		if entry.UID == "1800000000000002000" {
			if entry.LiveNote != XLiveUnsupportedMessage {
				t.Fatalf("X account with AutoRecord rule missing LiveNote: got %q want %q", entry.LiveNote, XLiveUnsupportedMessage)
			}
			foundLiveNote = true
		} else if entry.LiveNote != "" {
			t.Fatalf("X account without AutoRecord rule should have empty LiveNote, got %q", entry.LiveNote)
		}
		if at, parseErr := time.Parse(time.RFC3339, entry.NextAt); parseErr == nil && at.Equal(nextTick.Add(60*time.Minute)) {
			foundRound2 = true
		}
	}
	if xEntries != 16 || !foundLiveNote || !foundRound2 {
		t.Fatalf("unexpected X CheckPlan coverage: entries=%d foundLiveNote=%v foundRound2=%v", xEntries, foundLiveNote, foundRound2)
	}

	// Backoff pushes X CheckPlan entries past BackoffUntil with AfterBackoff=true.
	mgr.RecordSiteAutomaticStop("x", site.ErrKindRiskControl)
	_, until := mgr.IsSiteInBackoff("x")
	for _, entry := range mgr.CheckPlan() {
		if entry.Site == "x" {
			at, _ := time.Parse(time.RFC3339, entry.NextAt)
			if !entry.AfterBackoff || at.Before(until) {
				t.Fatalf("expected X CheckPlan pushed past backoff %v, got %+v", until, entry)
			}
		}
	}

	// Logged-out X shows PlanUnauth in CheckPlan.
	xAuthed = false
	for _, entry := range mgr.CheckPlan() {
		if entry.Site == "x" && (entry.Plan != PlanUnauth || entry.NextAt != "") {
			t.Fatalf("expected X CheckPlan Plan=%q with empty NextAt when logged out, got %+v", PlanUnauth, entry)
		}
	}

	// 3. ImportAccountRefs for X following: 0 requests, normalizes IDs, preserves existing, rolls back on invalid ref.
	saveTestAccountState(t, mgr, AccountState{
		Site: "x", UID: "1800000000000002000", BaselineAt: "2026-11-22T09:00:00Z", NewestID: "1800000000000009999", NewestTs: 1710009999, LastKind: "ok",
	})
	const (
		followUID1 = "180000000000003001"
		followUID2 = "180000000000003002"
	)
	if _, err := mgr.ImportAccountRefs([]AccountRef{
		{Site: "x", UID: followUID1, Name: "有效待导入"},
		{Site: "x", UID: "not_numeric", Name: "非法ID"},
	}, DefaultSubRules()); err == nil {
		t.Fatalf("expected ImportAccountRefs to reject invalid X UID")
	}
	if _, ok := mgr.OwnerOf("x", followUID1); ok {
		t.Fatalf("ImportAccountRefs rollback failed: %s leaked", followUID1)
	}

	res, err := mgr.ImportAccountRefs([]AccountRef{
		{Site: "x", UID: "00" + followUID1, UniqueID: "@SynthFollowOne", Name: "合成关注一", Face: "https://pbs.twimg.com/profile_images/1/f.jpg?format=jpg&name=orig"},
		{Site: "x", UID: followUID2, UniqueID: "SynthFollowTwo", Name: "", Face: "https://video.twimg.com/ext_tw_video/1/bad.mp4"},
		{Site: "x", UID: "00" + followUID2, UniqueID: "DupInBatch", Name: "同批重复"},
		{Site: "x", UID: "1800000000000002000", UniqueID: "OverwriteAttempt", Name: "不应覆盖已有"},
	}, DefaultSubRules())
	if err != nil {
		t.Fatalf("ImportAccountRefs(x): %v", err)
	}
	if res.Created != 2 || res.Skipped != 2 {
		t.Fatalf("ImportAccountRefs(x) result = %+v, want {Created:2 Skipped:2}", res)
	}
	subOne, accOne, ok := mgr.GetAccount("x", followUID1)
	if !ok || subOne.Name != "合成关注一" || accOne.UniqueID != "SynthFollowOne" || accOne.Face != "https://pbs.twimg.com/profile_images/1/f.jpg?format=jpg&name=orig" {
		t.Fatalf("unexpected imported X subOne: sub=%+v acc=%+v ok=%v", subOne, accOne, ok)
	}
	subTwo, accTwo, ok := mgr.GetAccount("x", followUID2)
	if !ok || subTwo.Name != "@SynthFollowTwo" || accTwo.Face != "" {
		t.Fatalf("unexpected imported X subTwo: sub=%+v acc=%+v ok=%v", subTwo, accTwo, ok)
	}
	existingSub, ok := mgr.OwnerOf("x", "1800000000000002000")
	if !ok || existingSub.Name != "X账号00" {
		t.Fatalf("already-subscribed X account was overwritten: %+v", existingSub)
	}
	if st, _, _ := mgr.GetAccountState("x", "1800000000000002000"); st.NewestID != "1800000000000009999" {
		t.Fatalf("existing X watermark overwritten on import: %+v", st)
	}
}
