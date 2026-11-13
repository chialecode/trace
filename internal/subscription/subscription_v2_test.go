package subscription

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
	"github.com/chialecode/trace/internal/uistate"
)

func TestDouyinSubscriptionValidationOwnershipAndWebRID(t *testing.T) {
	mgr := regressionSubscriptions(t)

	validSecUID := "MS4wLjABAAAA__Synthetic_CaseSensitive_01"
	diffCaseSecUID := "ms4wljabaaaa__synthetic_casesensitive_01"

	for _, badUID := range []string{
		"",
		"   ",
		" leading_space",
		"trailing_space ",
		"inner space",
		"inner\ttab",
		"slash/segment",
		`backslash\segment`,
		"colon:segment",
		"..",
		"MS4w..bad",
	} {
		if _, err := mgr.Create("Invalid", "", []AccountRef{{Site: "douyin", UID: badUID}}, SubRules{}); err == nil {
			t.Fatalf("expected invalid Douyin sec_uid %q to be rejected", badUID)
		}
	}
	for _, badWebRID := range []string{"room space", "room/1", `room\1`, "room:1", ".."} {
		if _, err := mgr.Create("InvalidWebRID", "", []AccountRef{{
			Site:   "douyin",
			UID:    validSecUID,
			WebRID: badWebRID,
		}}, SubRules{}); err == nil {
			t.Fatalf("expected invalid Douyin webRid %q to be rejected", badWebRID)
		}
	}

	subA, err := mgr.Create("MixedCreator", "Tracks both sites", []AccountRef{
		{Site: "bilibili", UID: "01001", Name: "BiliHandle"},
		{
			Site:     "douyin",
			UID:      validSecUID,
			UniqueID: "dy_handle_01",
			WebRID:   "88001122",
			Name:     "DouyinHandle",
			Face:     "https://p3-synthetic.douyinpic.com/face.jpeg?x-expires=1700000000&x-signature=secret",
		},
	}, SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatalf("Create mixed subscription: %v", err)
	}
	if len(subA.Accounts) != 2 {
		t.Fatalf("expected 2 accounts, got %+v", subA.Accounts)
	}
	if subA.Accounts[0].UID != "1001" {
		t.Fatalf("expected canonical Bilibili UID 1001, got %q", subA.Accounts[0].UID)
	}
	if subA.Accounts[1].UID != validSecUID {
		t.Fatalf("expected Douyin sec_uid preserved verbatim (%q), got %q", validSecUID, subA.Accounts[1].UID)
	}
	if subA.Accounts[1].UniqueID != "dy_handle_01" || subA.Accounts[1].WebRID != "88001122" {
		t.Fatalf("expected uniqueId and webRid preserved, got %+v", subA.Accounts[1])
	}
	if subA.Accounts[1].Face != "" {
		t.Fatalf("expected signed Douyin face URL stripped, got %q", subA.Accounts[1].Face)
	}

	// Ownership is <site>:<uid> case-sensitive for Douyin and independent of webRid / uniqueId.
	subB, err := mgr.Create("LowerCaseDistinct", "", []AccountRef{{
		Site:     "douyin",
		UID:      diffCaseSecUID,
		UniqueID: "dy_handle_lower",
	}}, SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatalf("case-distinct Douyin sec_uid should be distinct owner: %v", err)
	}

	// Linking validSecUID with a different webRid to subB must fail with ErrAccountConflict!
	conflictOwner, err := mgr.LinkAccount(subB.ID, AccountRef{
		Site:     "douyin",
		UID:      validSecUID,
		UniqueID: "changed_handle",
		WebRID:   "99990000",
	})
	if !errors.Is(err, ErrAccountConflict) || conflictOwner.ID != subA.ID {
		t.Fatalf("expected ErrAccountConflict owned by subA regardless of webRid, got owner=%+v err=%v", conflictOwner, err)
	}

	// Updating/invalidating cached webRid keeps ownership intact and persists version: 2 on disk.
	if err := mgr.UpdateAccountWebRID("douyin", validSecUID, ""); err != nil {
		t.Fatalf("clear webRid: %v", err)
	}
	if err := mgr.UpdateAccountWebRID("douyin", validSecUID, "77665544"); err != nil {
		t.Fatalf("update webRid: %v", err)
	}
	ownerAfterUpdate, ok := mgr.OwnerOf("douyin", validSecUID)
	if !ok || ownerAfterUpdate.ID != subA.ID {
		t.Fatalf("expected owner unchanged after webRid update, got %+v ok=%v", ownerAfterUpdate, ok)
	}
	if ownerAfterUpdate.Accounts[1].WebRID != "77665544" {
		t.Fatalf("expected updated webRid 77665544, got %+v", ownerAfterUpdate.Accounts[1])
	}

	// Move Douyin account between subscriptions and verify UniqueID & WebRID stay intact.
	movedDest, err := mgr.Move("douyin", validSecUID, subB.ID)
	if err != nil {
		t.Fatalf("Move Douyin account: %v", err)
	}
	if len(movedDest.Accounts) != 2 || movedDest.Accounts[1].WebRID != "77665544" || movedDest.Accounts[1].UniqueID != "dy_handle_01" {
		t.Fatalf("Move lost Douyin account metadata: %+v", movedDest.Accounts)
	}

	rawDisk, err := os.ReadFile(mgr.subsPath)
	if err != nil {
		t.Fatalf("ReadFile subscriptions.json: %v", err)
	}
	if strings.Contains(string(rawDisk), "x-signature") {
		t.Fatalf("signed URL leaked into subscriptions.json: %s", string(rawDisk))
	}
	var diskDoc subscriptionsFile
	if err := json.Unmarshal(rawDisk, &diskDoc); err != nil {
		t.Fatalf("Unmarshal subscriptions.json: %v", err)
	}
	if diskDoc.Version != 2 {
		t.Fatalf("subscriptions.json version = %d, want 2", diskDoc.Version)
	}
}

func TestSiteSchedulerStateIsolationAndQuotas(t *testing.T) {
	mgr := regressionSubscriptions(t)
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	mgr.SetNowFunc(func() time.Time { return now })

	if got := SiteHourlyQuotaLimit("bilibili"); got != 200 {
		t.Fatalf("Bilibili quota = %d, want 200", got)
	}
	if got := SiteHourlyQuotaLimit("douyin"); got != 120 {
		t.Fatalf("Douyin quota = %d, want 120", got)
	}

	// Trigger backoff on Douyin only; Bilibili must remain completely unaffected.
	mgr.RecordSiteAutomaticStop("douyin", site.ErrKindRiskControl)
	dyBackoff, dyUntil := mgr.IsSiteInBackoff("douyin")
	biliBackoff, _ := mgr.IsSiteInBackoff("bilibili")
	if !dyBackoff || !dyUntil.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("expected Douyin backoff until +2h, got active=%v until=%v", dyBackoff, dyUntil)
	}
	if biliBackoff {
		t.Fatalf("Douyin backoff leaked to Bilibili")
	}
	if err := mgr.SiteAutomaticRequestAllowed("douyin"); !site.IsKind(err, site.ErrKindBackoff) {
		t.Fatalf("expected Douyin backoff error, got %v", err)
	}
	if err := mgr.SiteAutomaticRequestAllowed("bilibili"); err != nil {
		t.Fatalf("expected Bilibili automatic request allowed while Douyin is in backoff, got %v", err)
	}

	// Advance past backoff and exhaust Douyin's 120/hr quota; Bilibili's 200/hr quota stays available.
	now = now.Add(3 * time.Hour)
	if !mgr.ConsumeSiteQuotaTokens("douyin", 119) {
		t.Fatalf("expected 119 Douyin tokens to succeed")
	}
	if err := mgr.SiteAutomaticRequestAllowed("douyin"); err != nil {
		t.Fatalf("expected 120th Douyin token to succeed, got %v", err)
	}
	if mgr.CanUseSiteQuotaTokens("douyin", 1) {
		t.Fatalf("expected Douyin bucket full at 120/hr")
	}
	if err := mgr.SiteAutomaticRequestAllowed("douyin"); !site.IsKind(err, site.ErrKindQuota) {
		t.Fatalf("expected Douyin quota error at 121st request, got %v", err)
	}
	if !mgr.CanUseSiteQuotaTokens("bilibili", 150) {
		t.Fatalf("expected Bilibili 200/hr bucket unaffected by Douyin token consumption")
	}
}

type v1DataDirEnv struct {
	dataDir  string
	cacheDir string
}

func materializeV1DataDir(t *testing.T) v1DataDirEnv {
	t.Helper()
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	cacheDir := filepath.Join(root, "cache")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("mkdir dataDir: %v", err)
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatalf("mkdir cacheDir: %v", err)
	}

	fixtureDir := filepath.Join("testdata", "v1")
	for _, name := range []string{"subscriptions.json", "ui-state.json", "settings.json", "history.json"} {
		raw, err := os.ReadFile(filepath.Join(fixtureDir, name))
		if err != nil {
			t.Fatalf("read fixture %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dataDir, name), raw, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	// Build v1 cached item directories & manifests matching library_v1.sql.
	vidBytes := []byte("synthetic-v1-mp4-payload")
	vidSum := sha256.Sum256(vidBytes)
	vidHash := hex.EncodeToString(vidSum[:])
	vidDir := filepath.Join(cacheDir, "bilibili", "1001", "videos", "BV17x411w7KC")
	if err := os.MkdirAll(vidDir, 0o700); err != nil {
		t.Fatalf("mkdir vidDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(vidDir, "P01.mp4"), vidBytes, 0o600); err != nil {
		t.Fatalf("write P01.mp4: %v", err)
	}
	vidManifest := fmt.Sprintf(`{
  "version": 1,
  "itemKey": "bilibili:video:BV17x411w7KC",
  "site": "bilibili",
  "kind": "video",
  "id": "BV17x411w7KC",
  "rawId": "BV17x411w7KC",
  "title": "V1 Cached Video",
  "summary": "Summary A",
  "status": "complete",
  "pubTs": 1700000300,
  "cachedAt": "2026-11-05T08:01:00Z",
  "directCached": true,
  "author": {"mid": 1001, "name": "SyntheticCreatorA"},
  "media": [{"role": "video", "path": "P01.mp4", "size": %d, "sha256": %q}],
  "parts": [{"cid": 9001, "page": 1, "title": "P1", "file": "P01.mp4", "sha256": %q}]
}`, len(vidBytes), vidHash, vidHash)
	if err := os.WriteFile(filepath.Join(vidDir, "item.json"), []byte(vidManifest), 0o600); err != nil {
		t.Fatalf("write video item.json: %v", err)
	}

	dynBytes := []byte("synthetic-v1-png-payload")
	dynSum := sha256.Sum256(dynBytes)
	dynHash := hex.EncodeToString(dynSum[:])
	dynDir := filepath.Join(cacheDir, "bilibili", "2002", "dynamics", "88001")
	if err := os.MkdirAll(dynDir, 0o700); err != nil {
		t.Fatalf("mkdir dynDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dynDir, "01.png"), dynBytes, 0o600); err != nil {
		t.Fatalf("write 01.png: %v", err)
	}
	dynManifest := fmt.Sprintf(`{
  "version": 1,
  "itemKey": "bilibili:dynamic:88001",
  "site": "bilibili",
  "kind": "dynamic",
  "id": "88001",
  "rawId": "88001",
  "title": "V1 Cached Dynamic",
  "summary": "Summary B",
  "status": "complete",
  "pubTs": 1700000400,
  "cachedAt": "2026-11-05T09:01:00Z",
  "directCached": true,
  "author": {"mid": 2002, "name": "SyntheticCreatorB"},
  "media": [{"role": "image", "path": "01.png", "size": %d, "sha256": %q}]
}`, len(dynBytes), dynHash)
	if err := os.WriteFile(filepath.Join(dynDir, "item.json"), []byte(dynManifest), 0o600); err != nil {
		t.Fatalf("write dynamic item.json: %v", err)
	}

	// Build library.db from frozen library_v1.sql.
	libSQL, err := os.ReadFile(filepath.Join(fixtureDir, "library_v1.sql"))
	if err != nil {
		t.Fatalf("read library_v1.sql: %v", err)
	}
	libPath := filepath.Join(dataDir, "library.db")
	libDB, _, _, err := storage.OpenSQLite(libPath)
	if err != nil {
		t.Fatalf("OpenSQLite library.db: %v", err)
	}
	if _, err := libDB.Exec(string(libSQL)); err != nil {
		_ = libDB.Close()
		t.Fatalf("exec library_v1.sql: %v", err)
	}
	if _, err := libDB.Exec(`
		INSERT INTO library_items (item_key, site, kind, raw_id, mid, author_name, title, summary, canonical_url, pub_ts, cached_at, status, item_dir, total_bytes, json_data)
		VALUES
			('bilibili:video:BV17x411w7KC', 'bilibili', 'video', 'BV17x411w7KC', 1001, 'SyntheticCreatorA', 'V1 Cached Video', 'Summary A', 'https://www.bilibili.com/video/BV17x411w7KC', 1700000300, '2026-11-05T08:01:00Z', 'complete', ?, ?, ?),
			('bilibili:dynamic:88001', 'bilibili', 'dynamic', '88001', 2002, 'SyntheticCreatorB', 'V1 Cached Dynamic', 'Summary B', 'https://t.bilibili.com/88001', 1700000400, '2026-11-05T09:01:00Z', 'complete', ?, ?, ?);
	`, vidDir, len(vidBytes), vidManifest, dynDir, len(dynBytes), dynManifest); err != nil {
		_ = libDB.Close()
		t.Fatalf("populate v1 library_items: %v", err)
	}
	_ = libDB.Close()

	// Build updates.db from frozen updates_v1.sql.
	updSQL, err := os.ReadFile(filepath.Join(fixtureDir, "updates_v1.sql"))
	if err != nil {
		t.Fatalf("read updates_v1.sql: %v", err)
	}
	updPath := filepath.Join(dataDir, "updates.db")
	updDB, _, _, err := storage.OpenSQLite(updPath)
	if err != nil {
		t.Fatalf("OpenSQLite updates.db: %v", err)
	}
	if _, err := updDB.Exec(string(updSQL)); err != nil {
		_ = updDB.Close()
		t.Fatalf("exec updates_v1.sql: %v", err)
	}
	_ = updDB.Close()

	return v1DataDirEnv{dataDir: dataDir, cacheDir: cacheDir}
}

type openedDataDirServices struct {
	uiState   *uistate.Store
	history   *storage.HistoryStore
	libDB     *sql.DB
	updatesDB *sql.DB
	lib       *library.Store
	subs      *Manager
}

func (s *openedDataDirServices) closeAll() {
	if s.subs != nil {
		s.subs.Stop()
	}
	if s.updatesDB != nil {
		_ = s.updatesDB.Close()
	}
	if s.libDB != nil {
		_ = s.libDB.Close()
	}
}

func runDataDirStartup(dataDir, cacheDir string) (*openedDataDirServices, error) {
	if _, err := storage.GuardDataSchemas(dataDir, storage.DataSchemas); err != nil {
		return nil, err
	}
	uiStore, _, _, err := uistate.Open(filepath.Join(dataDir, "ui-state.json"))
	if err != nil {
		return nil, err
	}
	histStore, _, err := storage.OpenHistory(filepath.Join(dataDir, "history.json"))
	if err != nil {
		return nil, err
	}
	libDB, _, _, err := storage.OpenSQLite(filepath.Join(dataDir, "library.db"))
	if err != nil {
		return nil, err
	}
	libStore, err := library.OpenWithDataDir(libDB, cacheDir, dataDir)
	if err != nil {
		_ = libDB.Close()
		return nil, err
	}
	updDB, _, _, err := storage.OpenSQLite(filepath.Join(dataDir, "updates.db"))
	if err != nil {
		_ = libDB.Close()
		return nil, err
	}
	cred, _, err := credential.Open(filepath.Join(dataDir, "bilibili.bin"))
	if err != nil {
		_ = updDB.Close()
		_ = libDB.Close()
		return nil, err
	}
	client := bilibili.NewClient(bilibili.ClientOptions{CredStore: cred, PacingInterval: -1})
	subMgr, err := NewManagerWithUpdatesDB(
		libDB,
		updDB,
		filepath.Join(dataDir, "subscriptions.json"),
		libStore,
		nil,
		client,
		settings.Default(cacheDir).Subscription,
		nil,
	)
	if err != nil {
		_ = updDB.Close()
		_ = libDB.Close()
		return nil, err
	}
	return &openedDataDirServices{
		uiState:   uiStore,
		history:   histStore,
		libDB:     libDB,
		updatesDB: updDB,
		lib:       libStore,
		subs:      subMgr,
	}, nil
}

func TestFullV1DirectoryMigrationIdempotencyAndRetention(t *testing.T) {
	env := materializeV1DataDir(t)

	// Create synthetic -wal and -shm sidecar files on updates.db before startup to verify BackupBeforeMigration copies sidecars too.
	// Note: open updates.db in a separate read connection during GuardDataSchemas? Even simpler: an empty -wal / -shm file next to v1 SQLite is valid and backed up verbatim!
	if err := os.WriteFile(filepath.Join(env.dataDir, "updates.db-wal"), nil, 0o600); err != nil {
		t.Fatalf("create empty updates.db-wal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(env.dataDir, "updates.db-shm"), nil, 0o600); err != nil {
		t.Fatalf("create empty updates.db-shm: %v", err)
	}

	origSubsRaw, _ := os.ReadFile(filepath.Join(env.dataDir, "subscriptions.json"))
	origUIRaw, _ := os.ReadFile(filepath.Join(env.dataDir, "ui-state.json"))

	// 1. First startup upgrades v1 -> v2 across all 4 target files and creates .premigrate-1 backups.
	svcs, err := runDataDirStartup(env.dataDir, env.cacheDir)
	if err != nil {
		t.Fatalf("first startup migration failed: %v", err)
	}

	for _, migratedFile := range []string{"library.db", "updates.db", "subscriptions.json", "ui-state.json"} {
		bakPath := filepath.Join(env.dataDir, migratedFile+".premigrate-1")
		if _, err := os.Stat(bakPath); err != nil {
			t.Fatalf("missing backup %s: %v", bakPath, err)
		}
	}
	for _, sidecar := range []string{"updates.db.premigrate-1-wal", "updates.db.premigrate-1-shm"} {
		if _, err := os.Stat(filepath.Join(env.dataDir, sidecar)); err != nil {
			t.Fatalf("missing SQLite sidecar backup %s: %v", sidecar, err)
		}
	}
	for _, untouchedV1 := range []string{"settings.json", "history.json"} {
		bakPath := filepath.Join(env.dataDir, untouchedV1+".premigrate-1")
		if _, err := os.Stat(bakPath); !os.IsNotExist(err) {
			t.Fatalf("unexpected backup for v1-only file %s", untouchedV1)
		}
	}

	// Verify SQLite user_version == 2 on both library.db and updates.db.
	for name, db := range map[string]*sql.DB{"library.db": svcs.libDB, "updates.db": svcs.updatesDB} {
		var uv int
		if err := db.QueryRow(`PRAGMA user_version`).Scan(&uv); err != nil || uv != 2 {
			t.Fatalf("%s user_version = %d (err=%v), want 2", name, uv, err)
		}
	}

	// Verify library.db author_uid backfill, preserved mid, cached items, Statistics, LatestPubByAuthor, and homeSeen lookup.
	var itemMid int64
	var itemAuthorUID string
	if err := svcs.libDB.QueryRow(`SELECT mid, author_uid FROM library_items WHERE item_key = 'bilibili:video:BV17x411w7KC'`).Scan(&itemMid, &itemAuthorUID); err != nil {
		t.Fatalf("query migrated library_items: %v", err)
	}
	if itemMid != 1001 || itemAuthorUID != "1001" {
		t.Fatalf("library_items (mid, author_uid) = (%d, %q), want (1001, '1001')", itemMid, itemAuthorUID)
	}
	var taskAuthorUID string
	if err := svcs.libDB.QueryRow(`SELECT author_uid FROM download_tasks WHERE task_id = 'task-v1-001'`).Scan(&taskAuthorUID); err != nil || taskAuthorUID != "1001" {
		t.Fatalf("download_tasks author_uid = %q (err=%v), want '1001'", taskAuthorUID, err)
	}
	stats, err := svcs.lib.Statistics()
	if err != nil || stats.Items != 2 || len(stats.Authors) != 2 {
		t.Fatalf("migrated library Statistics = %+v, err=%v", stats, err)
	}
	latestAuthors, err := svcs.lib.LatestPubByAuthor()
	if err != nil || len(latestAuthors) != 2 {
		t.Fatalf("LatestPubByAuthor = %+v, err=%v", latestAuthors, err)
	}
	latestMap := make(map[string]int64, len(latestAuthors))
	for _, au := range latestAuthors {
		latestMap[au.Site+":"+au.UID] = au.LatestPubTs
	}
	if latestMap["bilibili:1001"] != 1700000300 || latestMap["bilibili:2002"] != 1700000400 {
		t.Fatalf("unexpected LatestPubByAuthor map: %+v", latestMap)
	}
	uiSnap := svcs.uiState.Get()
	if uiSnap.Version != 2 || uiSnap.FeedFilter.Site != "all" {
		t.Fatalf("expected ui-state upgraded to v2 with feedFilter.site='all', got %+v", uiSnap)
	}
	if seenTs := library.LookupAuthorHomeSeen(uiSnap.HomeSeen, "bilibili", "2002"); seenTs != 1700000200 {
		t.Fatalf("expected legacy mid:2002 homeSeen preserved & matched, got ts=%d", seenTs)
	}

	// Verify updates.db site_scheduler_state migrated from scheduler_state and legacy scheduler_state kept intact without further writes.
	biliSched := svcs.subs.GetSiteSchedulerState("bilibili")
	if biliSched.BackoffUntil != "2026-11-12T12:00:00Z" ||
		biliSched.BackoffLevel != 2 ||
		biliSched.ConsecutiveFailures != 2 ||
		biliSched.LastStopReason != "risk_control" ||
		len(biliSched.TokenTimestamps) != 2 {
		t.Fatalf("unexpected migrated bilibili site_scheduler_state: %+v", biliSched)
	}
	if err := svcs.subs.SaveSiteSchedulerState("bilibili", SchedulerState{
		BackoffUntil:        "2026-11-12T18:00:00Z",
		BackoffLevel:        3,
		ConsecutiveFailures: 3,
		LastStopReason:      "rate_limited",
	}); err != nil {
		t.Fatalf("SaveSiteSchedulerState: %v", err)
	}
	var legacyFailures int
	var legacyReason string
	if err := svcs.updatesDB.QueryRow(`SELECT consecutive_failures, last_stop_reason FROM scheduler_state WHERE id = 1`).Scan(&legacyFailures, &legacyReason); err != nil {
		t.Fatalf("query legacy scheduler_state: %v", err)
	}
	if legacyFailures != 2 || legacyReason != "risk_control" {
		t.Fatalf("legacy scheduler_state was modified after v2 migration: failures=%d reason=%q", legacyFailures, legacyReason)
	}

	// Verify subscriptions.json upgraded to version 2 with accounts, rules, and update items intact.
	subsList, err := svcs.subs.List()
	if err != nil || len(subsList) != 2 {
		t.Fatalf("migrated subscriptions List = %+v, err=%v", subsList, err)
	}
	rulesList := svcs.subs.ListRules()
	if len(rulesList) != 2 {
		t.Fatalf("expected 2 shared cacheRules preserved, got %+v", rulesList)
	}
	updItems, err := svcs.subs.ListUpdates(subsList[0].ID)
	if err != nil || len(updItems) != 1 || updItems[0].DynID != "99001" {
		t.Fatalf("migrated update_item lost: %+v err=%v", updItems, err)
	}

	// User explicitly selects "bilibili" in v2 UI state -> must be retained on subsequent startups!
	if _, err := svcs.uiState.Mutate(func(s *uistate.State) {
		s.FeedFilter.Site = "bilibili"
	}); err != nil {
		t.Fatalf("uiState.Mutate(bilibili): %v", err)
	}
	svcs.closeAll()

	// 2. Second startup: idempotent (no re-migration, .premigrate-1 still matches original v1 bytes, and "bilibili" site choice is retained!).
	svcs2, err := runDataDirStartup(env.dataDir, env.cacheDir)
	if err != nil {
		t.Fatalf("second startup failed: %v", err)
	}
	if gotSite := svcs2.uiState.Get().FeedFilter.Site; gotSite != "bilibili" {
		svcs2.closeAll()
		t.Fatalf("expected user-selected feedFilter.site='bilibili' retained in v2, got %q", gotSite)
	}
	bakSubsRaw, _ := os.ReadFile(filepath.Join(env.dataDir, "subscriptions.json.premigrate-1"))
	bakUIRaw, _ := os.ReadFile(filepath.Join(env.dataDir, "ui-state.json.premigrate-1"))
	if string(bakSubsRaw) != string(origSubsRaw) {
		svcs2.closeAll()
		t.Fatalf("second startup overwrote subscriptions.json.premigrate-1")
	}
	if string(bakUIRaw) != string(origUIRaw) {
		svcs2.closeAll()
		t.Fatalf("second startup overwrote ui-state.json.premigrate-1")
	}
	svcs2.closeAll()

	// 3. Simulate v0.1.1 (all-v1 DataSchemas) opening the v2 data directory: rejected without writing any file!
	v011Schemas := []storage.SchemaFile{
		{Name: "settings.json", Kind: storage.SchemaJSON, Current: 1},
		{Name: "ui-state.json", Kind: storage.SchemaJSON, Current: 1},
		{Name: "history.json", Kind: storage.SchemaJSON, Current: 1},
		{Name: "subscriptions.json", Kind: storage.SchemaJSON, Current: 1},
		{Name: "library.db", Kind: storage.SchemaSQLite, Current: 1},
		{Name: "updates.db", Kind: storage.SchemaSQLite, Current: 1},
	}
	beforeUI, _ := os.ReadFile(filepath.Join(env.dataDir, "ui-state.json"))
	beforeSubs, _ := os.ReadFile(filepath.Join(env.dataDir, "subscriptions.json"))
	_, err = storage.GuardDataSchemas(env.dataDir, v011Schemas)
	if !storage.IsNewerData(err) {
		t.Fatalf("expected NewerDataError from all-v1 DataSchemas on v2 directory, got %v", err)
	}
	afterUI, _ := os.ReadFile(filepath.Join(env.dataDir, "ui-state.json"))
	afterSubs, _ := os.ReadFile(filepath.Join(env.dataDir, "subscriptions.json"))
	if string(beforeUI) != string(afterUI) || string(beforeSubs) != string(afterSubs) {
		t.Fatalf("all-v1 schema check modified v2 files on disk")
	}
}

func TestSchemaV2FaultInjectionMatrixAndBackupAbort(t *testing.T) {
	t.Run("BackupFailureAbortsBeforeMigrating", func(t *testing.T) {
		env := materializeV1DataDir(t)
		origUI, _ := os.ReadFile(filepath.Join(env.dataDir, "ui-state.json"))
		origSubs, _ := os.ReadFile(filepath.Join(env.dataDir, "subscriptions.json"))

		// Block creation of updates.db.premigrate-1 with a non-empty directory.
		blocker := filepath.Join(env.dataDir, "updates.db.premigrate-1", "keep")
		if err := os.MkdirAll(blocker, 0o700); err != nil {
			t.Fatalf("mkdir blocker: %v", err)
		}

		if _, err := runDataDirStartup(env.dataDir, env.cacheDir); err == nil {
			t.Fatalf("expected startup to abort when backup fails")
		}
		afterUI, _ := os.ReadFile(filepath.Join(env.dataDir, "ui-state.json"))
		afterSubs, _ := os.ReadFile(filepath.Join(env.dataDir, "subscriptions.json"))
		if string(origUI) != string(afterUI) || string(origSubs) != string(afterSubs) {
			t.Fatalf("files were migrated despite backup failure")
		}
		libDB, _, _, err := storage.OpenSQLite(filepath.Join(env.dataDir, "library.db"))
		if err != nil {
			t.Fatalf("open library.db: %v", err)
		}
		var uv int
		_ = libDB.QueryRow(`PRAGMA user_version`).Scan(&uv)
		_ = libDB.Close()
		if uv != 1 {
			t.Fatalf("library.db user_version = %d after backup failure, want 1", uv)
		}

		// Clear blocker -> next startup completes cleanly.
		_ = os.RemoveAll(filepath.Join(env.dataDir, "updates.db.premigrate-1"))
		svcs, err := runDataDirStartup(env.dataDir, env.cacheDir)
		if err != nil {
			t.Fatalf("recovery startup after backup failure: %v", err)
		}
		svcs.closeAll()
	})

	t.Run("Fault1_AfterBackupBeforeMigration", func(t *testing.T) {
		env := materializeV1DataDir(t)
		origUI, _ := os.ReadFile(filepath.Join(env.dataDir, "ui-state.json"))

		restore := storage.SetAfterBackupHookForTest(func(srcPath, backupPath string) error {
			if filepath.Base(srcPath) == "subscriptions.json" {
				return errors.New("injected fault right after backup")
			}
			return nil
		})
		_, err := runDataDirStartup(env.dataDir, env.cacheDir)
		restore()
		if err == nil || !strings.Contains(err.Error(), "injected fault right after backup") {
			t.Fatalf("expected injected after-backup error, got %v", err)
		}
		if _, statErr := os.Stat(filepath.Join(env.dataDir, "subscriptions.json.premigrate-1")); statErr != nil {
			t.Fatalf("expected subscriptions.json.premigrate-1 to exist: %v", statErr)
		}
		afterUI, _ := os.ReadFile(filepath.Join(env.dataDir, "ui-state.json"))
		if string(origUI) != string(afterUI) {
			t.Fatalf("ui-state.json was modified despite GuardDataSchemas aborting")
		}

		svcs, err := runDataDirStartup(env.dataDir, env.cacheDir)
		if err != nil {
			t.Fatalf("next startup after Fault 1 failed: %v", err)
		}
		svcs.closeAll()
	})

	t.Run("Fault2a_MidSQLTransaction_LibraryDB", func(t *testing.T) {
		env := materializeV1DataDir(t)
		restore := library.SetMigrateV2HookForTest(func(tx *sql.Tx) error {
			return errors.New("injected library.db mid-tx fault")
		})
		_, err := runDataDirStartup(env.dataDir, env.cacheDir)
		restore()
		if err == nil || !strings.Contains(err.Error(), "injected library.db mid-tx fault") {
			t.Fatalf("expected library.db mid-tx error, got %v", err)
		}

		// Verify library.db rolled back to v1 and backup remains intact.
		libDB, _, _, err := storage.OpenSQLite(filepath.Join(env.dataDir, "library.db"))
		if err != nil {
			t.Fatalf("open library.db: %v", err)
		}
		var uv int
		_ = libDB.QueryRow(`PRAGMA user_version`).Scan(&uv)
		_ = libDB.Close()
		if uv != 1 {
			t.Fatalf("library.db user_version = %d after rollback, want 1", uv)
		}
		if _, statErr := os.Stat(filepath.Join(env.dataDir, "library.db.premigrate-1")); statErr != nil {
			t.Fatalf("library.db.premigrate-1 missing: %v", statErr)
		}

		svcs, err := runDataDirStartup(env.dataDir, env.cacheDir)
		if err != nil {
			t.Fatalf("next startup after library.db mid-tx fault failed: %v", err)
		}
		svcs.closeAll()
	})

	t.Run("Fault2b_MidSQLTransaction_UpdatesDB", func(t *testing.T) {
		env := materializeV1DataDir(t)
		restore := SetMigrateUpdatesV2HookForTest(func(tx *sql.Tx) error {
			return errors.New("injected updates.db mid-tx fault")
		})
		_, err := runDataDirStartup(env.dataDir, env.cacheDir)
		restore()
		if err == nil || !strings.Contains(err.Error(), "injected updates.db mid-tx fault") {
			t.Fatalf("expected updates.db mid-tx error, got %v", err)
		}

		// Verify updates.db rolled back completely to v1 without site_scheduler_state.
		updDB, _, _, err := storage.OpenSQLite(filepath.Join(env.dataDir, "updates.db"))
		if err != nil {
			t.Fatalf("open updates.db: %v", err)
		}
		var uv, tableCount int
		_ = updDB.QueryRow(`PRAGMA user_version`).Scan(&uv)
		_ = updDB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'site_scheduler_state'`).Scan(&tableCount)
		_ = updDB.Close()
		if uv != 1 || tableCount != 0 {
			t.Fatalf("updates.db not rolled back cleanly: user_version=%d site_scheduler_state=%d", uv, tableCount)
		}
		if _, statErr := os.Stat(filepath.Join(env.dataDir, "updates.db.premigrate-1")); statErr != nil {
			t.Fatalf("updates.db.premigrate-1 missing: %v", statErr)
		}

		// Next startup skips already-v2 files (ui-state.json, library.db) and finishes updates.db + subscriptions.json!
		svcs, err := runDataDirStartup(env.dataDir, env.cacheDir)
		if err != nil {
			t.Fatalf("next startup after updates.db mid-tx fault failed: %v", err)
		}
		if got := svcs.subs.GetSiteSchedulerState("bilibili").LastStopReason; got != "risk_control" {
			svcs.closeAll()
			t.Fatalf("expected migrated risk_control reason after recovery, got %q", got)
		}
		svcs.closeAll()
	})

	t.Run("Fault3_BeforeJSONRename_UIStateAndSubscriptions", func(t *testing.T) {
		for _, targetFile := range []string{"ui-state.json", "subscriptions.json"} {
			t.Run(targetFile, func(t *testing.T) {
				env := materializeV1DataDir(t)
				origBytes, _ := os.ReadFile(filepath.Join(env.dataDir, targetFile))

				restore := storage.SetBeforeMigrationRenameHookForTest(func(path, tmpPath string) error {
					if filepath.Base(path) == targetFile {
						return fmt.Errorf("injected pre-rename fault on %s", targetFile)
					}
					return nil
				})
				_, err := runDataDirStartup(env.dataDir, env.cacheDir)
				restore()
				if err == nil || !strings.Contains(err.Error(), "injected pre-rename fault") {
					t.Fatalf("expected pre-rename error on %s, got %v", targetFile, err)
				}

				afterBytes, _ := os.ReadFile(filepath.Join(env.dataDir, targetFile))
				if string(origBytes) != string(afterBytes) {
					t.Fatalf("%s changed despite pre-rename fault", targetFile)
				}
				bakBytes, bakErr := os.ReadFile(filepath.Join(env.dataDir, targetFile+".premigrate-1"))
				if bakErr != nil || string(bakBytes) != string(origBytes) {
					t.Fatalf("%s.premigrate-1 missing or mismatched: %v", targetFile, bakErr)
				}
				matches, _ := filepath.Glob(filepath.Join(env.dataDir, targetFile+".tmp-*"))
				if len(matches) != 0 {
					t.Fatalf("temp files leaked after pre-rename fault: %v", matches)
				}

				svcs, err := runDataDirStartup(env.dataDir, env.cacheDir)
				if err != nil {
					t.Fatalf("next startup after %s pre-rename fault failed: %v", targetFile, err)
				}
				svcs.closeAll()
			})
		}
	})

	// Verify mixed-site CheckNow skips Douyin gracefully while checking Bilibili.
	t.Run("MixedSiteCheckSkipsDouyinUntilFetcherWired", func(t *testing.T) {
		dir := t.TempDir()
		db, _, _, err := storage.OpenSQLite(filepath.Join(dir, "library.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		cred, _, err := credential.Open(filepath.Join(dir, "bilibili.bin"))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = cred.SaveManual("SESSDATA=synthetic_sess; bili_jct=synthetic_csrf", "")
		client := bilibili.NewClient(bilibili.ClientOptions{CredStore: cred, AllowLocalhost: true, PacingInterval: -1})
		mgr, err := NewManagerWithUpdatesDB(db, db, filepath.Join(dir, "subscriptions.json"), nil, nil, client, settings.Default(dir).Subscription, nil)
		if err != nil {
			t.Fatal(err)
		}
		sub, err := mgr.Create("MixedCheck", "", []AccountRef{
			{Site: "douyin", UID: "MS4wLjABAAAA__Synthetic_Only"},
		}, SubRules{ScheduledCheck: true})
		if err != nil {
			t.Fatalf("Create Douyin-only subscription: %v", err)
		}
		sum, err := mgr.CheckSubscription(context.Background(), sub.ID)
		if err != nil || sum.FailedCreators != 0 {
			t.Fatalf("expected Douyin account check to skip cleanly without mid=0 error, got %+v err=%v", sum, err)
		}
	})
}
