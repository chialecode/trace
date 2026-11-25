package subscription

import (
	"context"
	"database/sql"
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

type syntheticSiteFetcher struct {
	siteID  site.ID
	mu      sync.Mutex
	calls   []string
	handler func(ctx context.Context, uid, cursor string) (site.CreatorPostsPage, error)
}

func (f *syntheticSiteFetcher) SiteID() site.ID {
	return f.siteID
}

func (f *syntheticSiteFetcher) FetchCreatorPosts(ctx context.Context, uid, cursor string) (site.CreatorPostsPage, error) {
	f.mu.Lock()
	f.calls = append(f.calls, uid+"@"+cursor)
	fn := f.handler
	f.mu.Unlock()
	if fn == nil {
		return site.CreatorPostsPage{}, nil
	}
	return fn(ctx, uid, cursor)
}

func (f *syntheticSiteFetcher) callSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *syntheticSiteFetcher) resetCalls() {
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
}

func TestSubscriptionsV2ToV3MigrationBackupAndNewerRejection(t *testing.T) {
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
	v2Raw := `{
  "version": 2,
  "subscriptions": [
    {
      "id": "sub_v2_alpha_0001",
      "name": "双站创作者",
      "note": "v2 fixture",
      "avatar": {"site": "bilibili", "uid": "1001"},
      "createdAt": "2026-11-15T08:00:00Z",
      "accounts": [
        {"site": "bilibili", "uid": "1001", "name": "B站号", "face": "https://example.invalid/b.jpg", "addedAt": "2026-11-15T08:00:00Z"},
        {"site": "douyin", "uid": "MS4wLjABAAAA__V2Fixture_01", "uniqueId": "dy_v2", "webRid": "778899", "name": "抖音号", "face": "", "addedAt": "2026-11-15T08:00:00Z"}
      ],
      "ruleId": "rule-v2-sched",
      "rules": {"scheduledCheck": true, "autoCache": {"enabled": false, "types": ["video", "draw"], "since": ""}, "autoRecord": false}
    }
  ],
  "cacheRules": [
    {"id": "default", "name": "默认", "scheduledCheck": false, "autoCache": false, "autoRecord": false},
    {"id": "rule-v2-sched", "name": "定时检查", "scheduledCheck": true, "autoCache": false, "autoRecord": false}
  ]
}
`
	if err := os.WriteFile(subsPath, []byte(v2Raw), 0o600); err != nil {
		t.Fatalf("write v2 subscriptions.json: %v", err)
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

	// 1. First startup migrates subscriptions.json v2 -> v3 with .premigrate-2 backup,
	// while updates.db stays at PRAGMA user_version = 2.
	mgr, err := NewManagerWithUpdatesDB(libDB, updDB, subsPath, nil, nil, nil, settings.Default(cacheDir).Subscription, nil)
	if err != nil {
		t.Fatalf("first startup v2->v3 migration: %v", err)
	}

	bakPath := subsPath + ".premigrate-2"
	bakBytes, err := os.ReadFile(bakPath)
	if err != nil {
		t.Fatalf("expected subscriptions.json.premigrate-2 backup: %v", err)
	}
	if string(bakBytes) != v2Raw {
		t.Fatalf("premigrate-2 backup content mismatch")
	}
	if _, statErr := os.Stat(filepath.Join(dataDir, "updates.db.premigrate-2")); !os.IsNotExist(statErr) {
		t.Fatalf("updates.db should remain v2 without premigrate-2 backup")
	}

	var updVer int
	if err := updDB.QueryRow(`PRAGMA user_version`).Scan(&updVer); err != nil || updVer != UpdatesDBSchemaVersion {
		t.Fatalf("updates.db user_version = %d (err=%v), want %d", updVer, err, UpdatesDBSchemaVersion)
	}
	if UpdatesDBSchemaVersion != 2 {
		t.Fatalf("UpdatesDBSchemaVersion = %d, want 2", UpdatesDBSchemaVersion)
	}

	rawAfter, err := os.ReadFile(subsPath)
	if err != nil {
		t.Fatalf("read migrated subscriptions.json: %v", err)
	}
	var diskDoc subscriptionsFile
	if err := json.Unmarshal(rawAfter, &diskDoc); err != nil {
		t.Fatalf("unmarshal migrated subscriptions.json: %v", err)
	}
	if diskDoc.Version != SubscriptionsSchemaVersion {
		t.Fatalf("migrated subscriptions.json version = %d, want %d", diskDoc.Version, SubscriptionsSchemaVersion)
	}
	if SubscriptionsSchemaVersion != 5 {
		t.Fatalf("SubscriptionsSchemaVersion = %d, want 5", SubscriptionsSchemaVersion)
	}

	// Link a Weibo account (with leading zeros and signed face URL) and verify persistence.
	linked, err := mgr.LinkAccount("sub_v2_alpha_0001", AccountRef{
		Site: "weibo",
		UID:  "001000000001",
		Name: "微博号",
		Face: "https://tvax1.sinaimg.cn/crop.0.0.180.180.180/001.jpg?KID=imgbed,tva&Expires=1790000000&ssig=SYNTHSIG",
	})
	if err != nil {
		t.Fatalf("LinkAccount weibo: %v", err)
	}
	if len(linked.Accounts) != 3 || linked.Accounts[2].Site != "weibo" || linked.Accounts[2].UID != "1000000001" || linked.Accounts[2].Face != "" {
		t.Fatalf("unexpected linked Weibo account: %+v", linked.Accounts)
	}

	// 2. Second startup is idempotent: preserves Weibo account & leaves .premigrate-2 untouched.
	mgr2, err := NewManagerWithUpdatesDB(libDB, updDB, subsPath, nil, nil, nil, settings.Default(cacheDir).Subscription, nil)
	if err != nil {
		t.Fatalf("second startup: %v", err)
	}
	if owner, ok := mgr2.OwnerOf("weibo", "001000000001"); !ok || owner.ID != "sub_v2_alpha_0001" {
		t.Fatalf("expected migrated Weibo account preserved on restart, got ok=%v owner=%+v", ok, owner)
	}
	bakBytes2, _ := os.ReadFile(bakPath)
	if string(bakBytes2) != v2Raw {
		t.Fatalf("idempotent second startup modified subscriptions.json.premigrate-2")
	}

	// 3. Snapshot round-trip preserves Weibo accounts and account_state start points.
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
		t.Fatalf("ParseSnapshot with Weibo account: %v", err)
	}
	if _, accCount := parsedSnap.Counts(); accCount != 3 {
		t.Fatalf("expected 3 accounts in parsed snapshot (bilibili+douyin+weibo), got %d", accCount)
	}

	// 4. Older v2-only schema guard rejects v3 subscriptions.json without modifying disk.
	v3Before, _ := os.ReadFile(subsPath)
	v2GuardSchemas := []storage.SchemaFile{
		{Name: "subscriptions.json", Kind: storage.SchemaJSON, Current: 2},
		{Name: "updates.db", Kind: storage.SchemaSQLite, Current: 2},
	}
	if _, err := storage.GuardDataSchemas(dataDir, v2GuardSchemas); !storage.IsNewerData(err) {
		t.Fatalf("expected NewerDataError when v2 guard opens v3 subscriptions.json, got %v", err)
	}
	v3After, _ := os.ReadFile(subsPath)
	if string(v3Before) != string(v3After) {
		t.Fatalf("v2 guard modified v3 subscriptions.json on disk")
	}

	// 5. Reject subscriptions.json version > current with NewerDataError and zero disk mutation.
	vNextDir := t.TempDir()
	vNextSubsPath := filepath.Join(vNextDir, "subscriptions.json")
	vNextRaw := fmt.Sprintf(`{"version":%d,"subscriptions":[]}`, SubscriptionsSchemaVersion+1)
	if err := os.WriteFile(vNextSubsPath, []byte(vNextRaw), 0o600); err != nil {
		t.Fatal(err)
	}
	vNextDB, _, _, err := storage.OpenSQLite(filepath.Join(vNextDir, "updates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer vNextDB.Close()
	if _, err := NewManagerWithUpdatesDB(vNextDB, vNextDB, vNextSubsPath, nil, nil, nil, settings.Default(vNextDir).Subscription, nil); !storage.IsNewerData(err) {
		t.Fatalf("expected NewerDataError for subscriptions.json version %d, got %v", SubscriptionsSchemaVersion+1, err)
	}
	if got, _ := os.ReadFile(vNextSubsPath); string(got) != vNextRaw {
		t.Fatalf("version %d subscriptions.json was modified on disk", SubscriptionsSchemaVersion+1)
	}

	// 6. Reject updates.db user_version > 2 with NewerDataError.
	newerUpdDir := t.TempDir()
	newerUpdDB, _, _, err := storage.OpenSQLite(filepath.Join(newerUpdDir, "updates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer newerUpdDB.Close()
	if _, err := newerUpdDB.Exec(`PRAGMA user_version = 3`); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManagerWithUpdatesDB(newerUpdDB, newerUpdDB, filepath.Join(newerUpdDir, "subscriptions.json"), nil, nil, nil, settings.Default(newerUpdDir).Subscription, nil); !storage.IsNewerData(err) {
		t.Fatalf("expected NewerDataError for updates.db user_version 3, got %v", err)
	}
}

func TestPerSiteBranchValuesUnknownSiteRejectionAndThreeWayIsolation(t *testing.T) {
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
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	mgr.SetNowFunc(func() time.Time { return now })

	// Table-driven per-site constants & zero Bilibili fallback on unknown/empty sites.
	cases := []struct {
		siteName string
		wantNorm string
		quota    int
		roundCap int
		interval time.Duration
	}{
		{"bilibili", "bilibili", DefaultBilibiliHourlyQuota, DefaultBilibiliRoundAccountCap, 2 * time.Second},
		{"douyin", "douyin", DefaultDouyinHourlyQuota, DefaultDouyinRoundAccountCap, 2500 * time.Millisecond},
		{"weibo", "weibo", DefaultWeiboHourlyQuota, DefaultWeiboRoundAccountCap, 3 * time.Second},
		{"WEIBO", "weibo", 100, 20, 3 * time.Second},
		{"", "", 0, 0, 0},
		{"unknown", "", 0, 0, 0},
		{"twitter", "", 0, 0, 0},
	}
	for _, tc := range cases {
		t.Run("site_"+tc.siteName, func(t *testing.T) {
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

	// ListSiteSchedulerStates must cover all 3 supported sites (including weibo).
	states := mgr.ListSiteSchedulerStates()
	for _, id := range site.All() {
		if st, ok := states[string(id)]; !ok || st.Site != string(id) {
			t.Fatalf("ListSiteSchedulerStates missing site %q: %+v", id, states)
		}
	}

	// Unknown / empty sites must NEVER fall back to Bilibili in any scheduler, lookup, or validation API.
	for _, badSite := range []string{"", "unknown", "unknown_site"} {
		if _, err := NormalizeAndValidateAccount(AccountRef{Site: badSite, UID: "1000000001"}); err == nil {
			t.Fatalf("NormalizeAndValidateAccount(%q) should fail", badSite)
		}
		if err := mgr.SaveSiteSchedulerState(badSite, SchedulerState{LastStopReason: "risk_control"}); err == nil {
			t.Fatalf("SaveSiteSchedulerState(%q) should fail", badSite)
		}
		if mgr.CanUseSiteQuotaTokens(badSite, 1) {
			t.Fatalf("CanUseSiteQuotaTokens(%q, 1) should be false", badSite)
		}
		if mgr.ConsumeSiteQuotaTokens(badSite, 1) {
			t.Fatalf("ConsumeSiteQuotaTokens(%q, 1) should be false", badSite)
		}
		if err := mgr.SiteAutomaticRequestAllowed(badSite); err == nil {
			t.Fatalf("SiteAutomaticRequestAllowed(%q) should fail", badSite)
		}
		if err := mgr.SiteAutomaticRequestAllowedFor(badSite, "live"); err == nil {
			t.Fatalf("SiteAutomaticRequestAllowedFor(%q, live) should fail", badSite)
		}
		mgr.RecordSiteAutomaticStop(badSite, site.ErrKindRiskControl)
		if inBackoff, _ := mgr.IsSiteInBackoff("bilibili"); inBackoff {
			t.Fatalf("RecordSiteAutomaticStop(%q) leaked into Bilibili backoff!", badSite)
		}
		if _, err := mgr.CheckAccount(context.Background(), "sub1", badSite, "1001"); err == nil {
			t.Fatalf("CheckAccount(%q) should reject unknown/empty site", badSite)
		}
	}

	// findCachedLibraryKey: Weibo matches weibo:dynamic / weibo:video; unknown site never matches Bilibili item!
	biliDir := filepath.Join(tmp, "cache", "bilibili", "1001", "dynamics", "5000000000000001")
	wbDir := filepath.Join(tmp, "cache", "weibo", "1000000001", "posts", "5000000000000002")
	_ = os.MkdirAll(biliDir, 0o700)
	_ = os.MkdirAll(wbDir, 0o700)
	if _, err := lib.SaveItem(biliDir, library.ItemMetadata{
		Version: 1, Site: "bilibili", Kind: "dynamic", ID: "5000000000000001", Status: "complete",
	}); err != nil {
		t.Fatalf("SaveItem bilibili: %v", err)
	}
	if _, err := lib.SaveItem(wbDir, library.ItemMetadata{
		Version: 1, Site: "weibo", Kind: "dynamic", ID: "5000000000000002", Status: "complete",
	}); err != nil {
		t.Fatalf("SaveItem weibo: %v", err)
	}
	if got := mgr.findCachedLibraryKey("weibo", "5000000000000002"); got != "weibo:dynamic:5000000000000002" {
		t.Fatalf("findCachedLibraryKey(weibo) = %q, want weibo:dynamic:5000000000000002", got)
	}
	if got := mgr.findCachedLibraryKey("unknown", "5000000000000001"); got != "" {
		t.Fatalf("findCachedLibraryKey(unknown) fell back to Bilibili: %q", got)
	}
	if got := mgr.findCachedLibraryKey("", "5000000000000001"); got != "" {
		t.Fatalf("findCachedLibraryKey(\"\") fell back to Bilibili: %q", got)
	}

	// Weibo UID normalization, face sanitization, and syncSubFields default name fallback (never "UID 0").
	for _, badUID := range []string{"", "1234", "00000", "abc12345", "100000000000000000000"} {
		if _, err := NormalizeAndValidateAccount(AccountRef{Site: "weibo", UID: badUID}); err == nil {
			t.Fatalf("expected invalid Weibo UID %q rejected", badUID)
		}
	}
	pubFace := "https://wx1.sinaimg.cn/large/001SynThPic01.jpg"
	normWB, err := NormalizeAndValidateAccount(AccountRef{
		Site: "weibo", UID: "001000000001", Face: pubFace,
	})
	if err != nil || normWB.UID != "1000000001" || normWB.Face != pubFace {
		t.Fatalf("NormalizeAndValidateAccount clean Weibo failed: norm=%+v err=%v", normWB, err)
	}
	normWBSigned, err := NormalizeAndValidateAccount(AccountRef{
		Site: "weibo", UID: "1000000001",
		Face: "https://tvax1.sinaimg.cn/crop.0.0.180.180.180/001.jpg?KID=imgbed,tva&Expires=1790000000&ssig=SYNTH",
	})
	if err != nil || normWBSigned.Face != "" {
		t.Fatalf("expected signed Weibo face stripped, got norm=%+v err=%v", normWBSigned, err)
	}
	wbUnnamed := Subscription{
		Accounts: []AccountRef{{Site: "weibo", UID: "1000000001"}},
	}
	syncSubFields(&wbUnnamed, now)
	if wbUnnamed.Name == "" || wbUnnamed.Name == "UID 0" || !strings.Contains(wbUnnamed.Name, "1000000001") {
		t.Fatalf("syncSubFields Weibo default name = %q, must never be 'UID 0'", wbUnnamed.Name)
	}

	// Three-way bidirectional backoff & quota isolation (Bilibili <-> Douyin <-> Weibo).
	mgr.RecordSiteAutomaticStop("weibo", site.ErrKindRiskControl)
	if active, _ := mgr.IsSiteInBackoff("weibo"); !active {
		t.Fatalf("expected Weibo in backoff")
	}
	if active, _ := mgr.IsSiteInBackoff("bilibili"); active {
		t.Fatalf("Weibo backoff leaked to Bilibili")
	}
	if active, _ := mgr.IsSiteInBackoff("douyin"); active {
		t.Fatalf("Weibo backoff leaked to Douyin")
	}
	if err := mgr.SiteAutomaticRequestAllowed("weibo"); !site.IsKind(err, site.ErrKindBackoff) {
		t.Fatalf("expected Weibo backoff error, got %v", err)
	}
	if err := mgr.SiteAutomaticRequestAllowed("bilibili"); err != nil {
		t.Fatalf("Bilibili should remain allowed while Weibo is in backoff: %v", err)
	}
	if err := mgr.SiteAutomaticRequestAllowed("douyin"); err != nil {
		t.Fatalf("Douyin should remain allowed while Weibo is in backoff: %v", err)
	}

	// Advance past Weibo backoff, then trigger Bilibili & Douyin backoff -> Weibo remains unaffected!
	now = now.Add(3 * time.Hour)
	mgr.RecordSiteAutomaticStop("bilibili", site.ErrKindRateLimited)
	mgr.RecordSiteAutomaticStop("douyin", site.ErrKindRiskControl)
	if err := mgr.SiteAutomaticRequestAllowed("weibo"); err != nil {
		t.Fatalf("Weibo should remain allowed while Bilibili & Douyin are in backoff: %v", err)
	}

	// Exhaust Weibo's 100/hr quota (1 already consumed above + 99 more = 100); 101st must fail with ErrKindQuota.
	if !mgr.ConsumeSiteQuotaTokens("weibo", 99) {
		t.Fatalf("expected remaining 99 Weibo tokens to fit in 100/hr bucket")
	}
	if err := mgr.SiteAutomaticRequestAllowed("weibo"); !site.IsKind(err, site.ErrKindQuota) {
		t.Fatalf("expected 101st Weibo request rejected with quota error, got %v", err)
	}
	// Advance past Bilibili/Douyin backoff without expiring Weibo tokens: Bilibili (200/hr) and Douyin (120/hr) still have quota!
	now = now.Add(30 * time.Minute)
	if !mgr.CanUseSiteQuotaTokens("bilibili", 190) {
		t.Fatalf("Bilibili 200/hr quota should be unaffected by Weibo quota exhaustion")
	}
	if !mgr.CanUseSiteQuotaTokens("douyin", 110) {
		t.Fatalf("Douyin 120/hr quota should be unaffected by Weibo quota exhaustion")
	}
}

func TestMixedThreeSiteSubscriptionBaselinePinnedFastRetweetUncertainEmptyAndAutoCache(t *testing.T) {
	var biliMu sync.Mutex
	biliDynamics := map[string][]map[string]any{
		"1001": {
			{
				"id_str": "81001",
				"type":   "DYNAMIC_TYPE_AV",
				"modules": map[string]any{
					"module_author":  map[string]any{"mid": 1001, "name": "BiliCreator", "pub_ts": 1700001000},
					"module_dynamic": map[string]any{"desc": map[string]any{"text": "Bili baseline"}},
				},
			},
		},
	}
	biliCalls := 0
	biliSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		biliMu.Lock()
		biliCalls++
		items := biliDynamics[r.URL.Query().Get("host_mid")]
		biliMu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"has_more": false, "items": items},
		})
	}))
	defer biliSrv.Close()

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
	cred, _, err := credential.Open(filepath.Join(tmp, "bilibili.bin"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = cred.SaveManual("SESSDATA=synth_bili_sess; bili_jct=synth_csrf", "rt")
	biliClient := bilibili.NewClient(bilibili.ClientOptions{
		CredStore:      cred,
		Endpoints:      bilibili.Endpoints{APIBase: biliSrv.URL},
		PacingInterval: -1,
		AllowLocalhost: true,
	})
	biliClient.SetWBIKeys("7cd084941338484aae1ad9425b84077c", "4932caff0ff746eab6f01bf08b70ac45")

	cfg := settings.Default(tmp).Subscription
	cfg.Enabled = true
	cfg.IntervalMinutes = 30
	now := time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC)

	dyFetcher := &syntheticSiteFetcher{siteID: site.Douyin}
	wbFetcher := &syntheticSiteFetcher{siteID: site.Weibo}

	biliAuth, dyAuth, wbAuth := true, true, true
	var wbAutoCached, dyAutoCached, biliAutoCached []UpdateItem
	var liveHintSites []string
	var postRoundLiveSites []string

	openMgr := func() *Manager {
		t.Helper()
		m, err := NewManagerWithUpdatesDB(db, updatesDB, filepath.Join(tmp, "subscriptions.json"), lib, nil, biliClient, cfg, nil)
		if err != nil {
			t.Fatalf("NewManagerWithUpdatesDB: %v", err)
		}
		m.SetAccountInterval(0)
		m.SetNowFunc(func() time.Time { return now })
		m.SetSiteAuthCheck(func(s site.ID) bool {
			switch s {
			case site.Bilibili:
				return biliAuth
			case site.Douyin:
				return dyAuth
			case site.Weibo:
				return wbAuth
			default:
				return false
			}
		})
		m.RegisterFetcher(dyFetcher)
		m.RegisterFetcher(wbFetcher)
		m.RegisterAutoCacheEnqueuer(site.Bilibili, func(_ context.Context, it UpdateItem) (string, error) {
			biliAutoCached = append(biliAutoCached, it)
			return "bili-auto-" + it.DynID, nil
		})
		m.RegisterAutoCacheEnqueuer(site.Douyin, func(_ context.Context, it UpdateItem) (string, error) {
			dyAutoCached = append(dyAutoCached, it)
			return "dy-auto-" + it.DynID, nil
		})
		m.RegisterAutoCacheEnqueuer(site.Weibo, func(_ context.Context, it UpdateItem) (string, error) {
			wbAutoCached = append(wbAutoCached, it)
			return "wb-auto-" + it.DynID, nil
		})
		m.SetOnLiveHint(func(acc AccountRef, _, _ string) {
			liveHintSites = append(liveHintSites, acc.Site+":"+acc.UID)
		})
		m.SetSiteLiveCheck(func(_ context.Context, accounts []AccountRef) error {
			for _, a := range accounts {
				postRoundLiveSites = append(postRoundLiveSites, a.Site+":"+a.UID)
			}
			return nil
		})
		return m
	}

	mgr := openMgr()

	allTags := []string{
		TagScheduledCheck,
		TagCacheVideo,
		TagCacheDraw,
		TagCacheWord,
		TagCacheForward,
		TagAutoRecord,
	}

	dyUID := "MS4wLjABAAAA__ThreeSite_01"
	wbUID1 := "1000000001"
	wbUID2 := "1000000002"

	mixedSub, err := mgr.Create("三站创作者", "", []AccountRef{
		{Site: "bilibili", UID: "1001", Name: "BiliCreator"},
		{Site: "douyin", UID: dyUID, Name: "DyCreator"},
		{Site: "weibo", UID: wbUID1, Name: "WeiboCreatorA"},
	}, SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatalf("Create mixed 3-site subscription: %v", err)
	}
	now = time.Unix(1700001500, 0).UTC()
	if _, err := mgr.SetSubscriptionTags(mixedSub.ID, allTags); err != nil {
		t.Fatalf("SetSubscriptionTags mixedSub: %v", err)
	}
	wbSub2, err := mgr.Create("第二微博博主", "", []AccountRef{
		{Site: "weibo", UID: wbUID2, Name: "WeiboCreatorB"},
	}, SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatalf("Create wbSub2: %v", err)
	}
	if _, err := mgr.SetSubscriptionTags(wbSub2.ID, allTags); err != nil {
		t.Fatalf("SetSubscriptionTags wbSub2: %v", err)
	}

	// Pre-cache one Weibo item to confirm auto-cache skips already-cached weibo:dynamic:<mid>.
	cachedWBDir := filepath.Join(tmp, "cache", "weibo", wbUID1, "posts", "5000000000000017")
	_ = os.MkdirAll(cachedWBDir, 0o700)
	if _, err := lib.SaveItem(cachedWBDir, library.ItemMetadata{
		Version: 1, Site: "weibo", Kind: "dynamic", ID: "5000000000000017", Status: "complete",
	}); err != nil {
		t.Fatalf("SaveItem pre-cached Weibo: %v", err)
	}

	dyFetcher.handler = func(_ context.Context, uid, cursor string) (site.CreatorPostsPage, error) {
		return site.CreatorPostsPage{
			AuthorName: "DyCreator",
			Posts:      []site.CreatorPost{{ID: "7100000000000000010", PubTs: 1700001000, Type: "video", Title: "dy base"}},
		}, nil
	}

	// Phase 1: Baseline check (page 1 ONLY, cursor=="", records BOTH pinned AND fast-retweet IDs,
	// watermark from newest regular post, 0 update_item rows, Weibo excluded from live checks/hints).
	wbFetcher.handler = func(_ context.Context, uid, cursor string) (site.CreatorPostsPage, error) {
		if cursor != "" {
			t.Fatalf("Weibo baseline must request page 1 with empty cursor, got uid=%s cursor=%q", uid, cursor)
		}
		if uid == wbUID1 {
			return site.CreatorPostsPage{
				AuthorName: "WeiboCreatorA",
				AuthorFace: "https://tvax1.sinaimg.cn/crop.0.0.180.180.180/a.jpg?KID=imgbed,tva&Expires=1790000000&ssig=SYNTH_WB_SIG_01",
				TotalWorks: 321,
				HasMore:    true,
				NextCursor: "2:5000000000000010",
				LiveRoomID: "1022:2321325000000000000001", // must NEVER trigger live hint for Weibo!
				Posts: []site.CreatorPost{
					{ID: "5000000000000099", PubTs: 1700001900, Type: "word", IsTop: true, Title: "置顶微博"},
					{ID: "5000000000000088", PubTs: 1700001850, Type: "forward", IsFastRetweet: true, OriginID: "4999999999990001", Title: "基线快转"},
					{ID: "5000000000000010", PubTs: 1700001200, Type: "draw", Title: "常规基线微博", Snapshot: `{"mid":"5000000000000010","mblogid":"O00000010"}`},
				},
			}, nil
		}
		return site.CreatorPostsPage{
			AuthorName: "WeiboCreatorB",
			HasMore:    false,
			Posts: []site.CreatorPost{
				{ID: "5000000000000001", PubTs: 1700001100, Type: "word", IsTop: true, Title: "全置顶微博基线"},
			},
		}, nil
	}

	ctx := context.Background()
	sum1, err := mgr.CheckNow(ctx, 0)
	if err != nil {
		t.Fatalf("Phase 1 CheckNow: %v", err)
	}
	if sum1.BaselineInitialized != 4 || sum1.AutoCacheTasks != 0 {
		t.Fatalf("expected 4 baselines initialized (bili+dy+2wb) and 0 auto-cache tasks, got %+v", sum1)
	}
	var itemCount int
	if err := updatesDB.QueryRow(`SELECT COUNT(*) FROM update_item`).Scan(&itemCount); err != nil || itemCount != 0 {
		t.Fatalf("expected 0 update_item rows after baseline, got %d (err=%v)", itemCount, err)
	}
	stWB1, ok, _ := mgr.GetAccountState("weibo", wbUID1)
	if !ok || stWB1.NewestID != "5000000000000010" || stWB1.NewestTs != 1700001200 {
		t.Fatalf("expected Weibo watermark from newest regular post 5000000000000010@1700001200, got %+v", stWB1)
	}
	if !mgr.wasPinnedOnBaseline("weibo", wbUID1, "5000000000000099") || !mgr.wasPinnedOnBaseline("weibo", wbUID1, "5000000000000088") {
		t.Fatalf("expected both pinned and fast-retweet baseline IDs recorded in account_baseline_pinned")
	}
	worksMap, _ := mgr.ListAccountWorks()
	if w := worksMap["weibo:"+wbUID1]; w.Total != 321 {
		t.Fatalf("expected Weibo TotalWorks 321 recorded in account_works, got %+v", w)
	}
	// Verify Weibo feed entry uses kind="dynamic" and canonical Weibo URL (never Douyin note/ URL!).
	var wbFeedKind, wbFeedURL string
	if err := db.QueryRow(`SELECT kind, canonical_url FROM feed_items WHERE site = 'weibo' AND raw_id = '5000000000000010'`).Scan(&wbFeedKind, &wbFeedURL); err != nil {
		t.Fatalf("query Weibo feed_items: %v", err)
	}
	if wbFeedKind != "dynamic" || wbFeedURL != "https://weibo.com/1000000001/O00000010" {
		t.Fatalf("Weibo feed entry (kind, canonical_url) = (%q, %q), want (dynamic, https://weibo.com/1000000001/O00000010)", wbFeedKind, wbFeedURL)
	}
	// Verify Weibo NEVER triggered live hints or post-round live checks!
	for _, hint := range liveHintSites {
		if strings.HasPrefix(hint, "weibo:") {
			t.Fatalf("Weibo unexpectedly triggered live hint: %v", liveHintSites)
		}
	}
	for _, liveSite := range postRoundLiveSites {
		if strings.HasPrefix(liveSite, "weibo:") {
			t.Fatalf("Weibo unexpectedly entered post-round live check: %v", postRoundLiveSites)
		}
	}

	// Reopen manager across restart before Phase 2.
	mgr = openMgr()

	// Phase 2: Incremental check for Weibo:
	// - Page 1: baseline pinned, old fast-retweet (PubTs < baseline, must NOT stop early or move watermark!),
	//   4 new rule types (video, draw, word, forward) + already-cached post + new fast-retweet (PubTs > baseline),
	//   with signed video/livephoto sentinels and public sinaimg.cn image in snapshot.
	// - Page 2: cross-page duplicate + regular baseline hit (stops scan) + older post (ignored).
	// - wbUID2: UncertainEmpty -> "本轮结果不完整", baseline untouched.
	sigSentinel := "SYNTH_WB_SIG_SENTINEL_7744"
	publicImg := "https://wx1.sinaimg.cn/large/001SynThPic01.jpg"
	now = now.Add(15 * time.Minute)
	wbFetcher.resetCalls()
	wbFetcher.handler = func(_ context.Context, uid, cursor string) (site.CreatorPostsPage, error) {
		if uid == wbUID1 {
			if cursor == "" {
				return site.CreatorPostsPage{
					AuthorName: "WeiboCreatorA",
					HasMore:    true,
					NextCursor: "2:5000000000000018",
					Posts: []site.CreatorPost{
						{ID: "5000000000000099", PubTs: 1700001900, Type: "word", IsTop: true, Title: "基线置顶"},
						{ID: "5000000000000002", PubTs: 1700000200, Type: "forward", IsFastRetweet: true, OriginID: "4999999999990002", Title: "旧微博快转"},
						{
							ID:            "5000000000000022",
							PubTs:         1700002600,
							Type:          "word",
							IsFastRetweet: true,
							OriginID:      "4999999999990022",
							Title:         "新快转应归类为forward且不移水位",
						},
						{
							ID:       "5000000000000021",
							PubTs:    1700002400,
							Type:     "video",
							Title:    "新视频微博",
							CoverURL: publicImg,
							Snapshot: fmt.Sprintf(`{"mid":"5000000000000021","mblogid":"O00000021","publicImage":%q,"stats":{"repostsCount":99,"commentsCount":88,"attitudesCount":77},"videoUrl":"https://f.video.weibocdn.com/o0/v.mp4?Expires=1790000000&ssig=%s&KID=unistore,video","livePhotoUrl":"https://video.weibo.com/media/play?livephoto=https%%3A%%2F%%2Flivephoto.us.sinaimg.cn%%2F001.mov%%3Fssig%%3D%s","nested":{"rawVideo":"https://f.video.weibocdn.com/o0/plain.mp4","rawLive":"https://livephoto.us.sinaimg.cn/001.mov","encodedSigned":"https://tvax1.sinaimg.cn/a.jpg%%3Fssig%%3D%s","canonicalUrl":"https://weibo.com/1000000001/O00000021"}}`, publicImg, sigSentinel, sigSentinel, sigSentinel),
						},
						{ID: "5000000000000020", PubTs: 1700002300, Type: "draw", Title: "新图文微博", CoverURL: publicImg},
						{ID: "5000000000000019", PubTs: 1700002200, Type: "word", Title: "新纯文字微博"},
						{ID: "5000000000000018", PubTs: 1700002100, Type: "forward", Title: "新转发微博"},
					},
				}, nil
			}
			if cursor == "2:5000000000000018" {
				return site.CreatorPostsPage{
					AuthorName: "WeiboCreatorA",
					HasMore:    true,
					NextCursor: "3:5000000000000005",
					Posts: []site.CreatorPost{
						{ID: "5000000000000018", PubTs: 1700002100, Type: "forward", Title: "跨页重复转发"},
						{ID: "5000000000000017", PubTs: 1700001800, Type: "draw", Title: "已缓存图文"},
						{ID: "5000000000000010", PubTs: 1700001200, Type: "draw", Title: "常规基线命中"},
						{ID: "5000000000000005", PubTs: 1700000500, Type: "word", Title: "基线前旧微博"},
					},
				}, nil
			}
			t.Fatalf("unexpected 3rd page request cursor=%q", cursor)
		}
		return site.CreatorPostsPage{
			UncertainEmpty:  true,
			UncertainReason: "第 1 页空列表但资料显示有微博",
		}, nil
	}

	stWB2Before, _, _ := mgr.GetAccountState("weibo", wbUID2)
	sum2, err := mgr.CheckNow(ctx, 0)
	if err != nil {
		t.Fatalf("Phase 2 CheckNow: %v", err)
	}
	if sum2.NewVideos != 1 || sum2.NewDynamics != 5 {
		t.Fatalf("expected 1 new Weibo video + 5 new Weibo dynamics (draw+word+forward+fastRetweet+precached), got %+v", sum2)
	}
	if len(sum2.GapAccounts) != 0 {
		t.Fatalf("expected baseline hit on page 2 with no gap, got %+v", sum2.GapAccounts)
	}

	// Watermark must advance to newest regular post (5000000000000021@1700002400),
	// NOT the newer fast-retweet (5000000000000022@1700002600)!
	stWB1After, _, _ := mgr.GetAccountState("weibo", wbUID1)
	if stWB1After.NewestID != "5000000000000021" || stWB1After.NewestTs != 1700002400 {
		t.Fatalf("expected Weibo watermark 5000000000000021@1700002400 (fast-retweet must not move watermark), got %+v", stWB1After)
	}

	// Verify auto-cache enqueued all 5 uncached new items (18,19,20,21,22) with exact rule types,
	// skipping already-cached 5000000000000017!
	if len(wbAutoCached) != 5 {
		t.Fatalf("expected 5 Weibo auto-cache items (skipping already-cached 17), got %+v", wbAutoCached)
	}
	gotTypes := map[string]string{}
	for _, it := range wbAutoCached {
		gotTypes[it.DynID] = it.Type
	}
	wantTypes := map[string]string{
		"5000000000000018": "forward",
		"5000000000000019": "word",
		"5000000000000020": "draw",
		"5000000000000021": "video",
		"5000000000000022": "forward",
	}
	for id, want := range wantTypes {
		if gotTypes[id] != want {
			t.Fatalf("Weibo post %s rule type = %q, want %q (all=%+v)", id, gotTypes[id], want, gotTypes)
		}
	}

	// Verify public sinaimg.cn image is preserved in snapshot while ALL signed/video/livephoto URLs are scrubbed!
	var snap21 string
	if err := updatesDB.QueryRow(`SELECT snapshot FROM update_item WHERE site = 'weibo' AND dyn_id = '5000000000000021'`).Scan(&snap21); err != nil {
		t.Fatalf("query Weibo snapshot: %v", err)
	}
	if !strings.Contains(snap21, publicImg) {
		t.Fatalf("expected public sinaimg.cn image preserved in snapshot, got %s", snap21)
	}
	for _, forbidden := range []string{sigSentinel, "ssig=", "ssig%3D", "Expires=", "KID=", "weibocdn.com", "video.weibo.com", "livephoto.us.sinaimg.cn", ".mp4", ".mov", "repostsCount", "commentsCount", "attitudesCount", `"stats"`} {
		if strings.Contains(snap21, forbidden) {
			t.Fatalf("forbidden ephemeral/signed/stats marker %q leaked into Weibo snapshot: %s", forbidden, snap21)
		}
	}

	// Verify UncertainEmpty on wbUID2 recorded "本轮结果不完整" and left baseline untouched.
	stWB2After, _, _ := mgr.GetAccountState("weibo", wbUID2)
	if stWB2After.LastKind != "incomplete" || !strings.HasPrefix(stWB2After.Note, "本轮结果不完整") {
		t.Fatalf("expected incomplete note on wbUID2, got %+v", stWB2After)
	}
	if stWB2After.BaselineAt != stWB2Before.BaselineAt || stWB2After.NewestID != stWB2Before.NewestID || stWB2After.NewestTs != stWB2Before.NewestTs {
		t.Fatalf("UncertainEmpty moved baseline: before=%+v after=%+v", stWB2Before, stWB2After)
	}

	// Verify visibility window (bottom_tips_visible=true) ends normally without gapDetected.
	now = now.Add(5 * time.Minute)
	wbFetcher.handler = func(_ context.Context, uid, _ string) (site.CreatorPostsPage, error) {
		return site.CreatorPostsPage{
			HasMore:           false,
			BottomTipsVisible: true,
			BottomTipsText:    "博主设置仅展示半年内的微博",
			Posts:             nil,
		}, nil
	}
	sumWin, err := mgr.CheckAccount(ctx, wbSub2.ID, "weibo", wbUID2)
	if err != nil || len(sumWin.GapAccounts) != 0 || len(sumWin.IncompleteAccounts) != 0 {
		t.Fatalf("expected bottom_tips_visible to finish cleanly, got sum=%+v err=%v", sumWin, err)
	}

	// Phase 3: Weibo hits risk_control -> remaining Weibo account stops & Weibo enters backoff,
	// while Bilibili AND Douyin in the SAME scheduled round proceed unaffected!
	now = now.Add(10 * time.Minute)
	biliMu.Lock()
	biliDynamics["1001"] = append([]map[string]any{{
		"id_str": "81099",
		"type":   "DYNAMIC_TYPE_AV",
		"modules": map[string]any{
			"module_author":  map[string]any{"mid": 1001, "name": "BiliCreator", "pub_ts": 1700003000},
			"module_dynamic": map[string]any{"desc": map[string]any{"text": "Bili during Weibo risk"}},
		},
	}}, biliDynamics["1001"]...)
	biliMu.Unlock()
	dyFetcher.handler = func(_ context.Context, _, _ string) (site.CreatorPostsPage, error) {
		return site.CreatorPostsPage{
			Posts: []site.CreatorPost{
				{ID: "7100000000000000099", PubTs: 1700003000, Type: "video", Title: "Dy during Weibo risk"},
				{ID: "7100000000000000010", PubTs: 1700001000, Type: "video", Title: "dy base"},
			},
		}, nil
	}
	wbFetcher.resetCalls()
	wbFetcher.handler = func(_ context.Context, _, _ string) (site.CreatorPostsPage, error) {
		return site.CreatorPostsPage{}, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindRiskControl,
			Message: "触发微博风控校验",
		}
	}

	sum3, err := mgr.RunScheduledRound(ctx)
	if err != nil {
		t.Fatalf("Phase 3 RunScheduledRound: %v", err)
	}
	if sum3.SiteStopped["weibo"] != site.ErrKindRiskControl {
		t.Fatalf("expected SiteStopped[weibo]=risk_control, got %+v", sum3)
	}
	if calls := wbFetcher.callSnapshot(); len(calls) != 1 {
		t.Fatalf("expected 2nd Weibo account skipped immediately after risk_control, got calls=%v", calls)
	}
	if len(biliAutoCached) != 1 || biliAutoCached[0].DynID != "81099" {
		t.Fatalf("expected Bilibili unaffected during Weibo risk_control, got %+v", biliAutoCached)
	}
	if len(dyAutoCached) != 1 || dyAutoCached[0].DynID != "7100000000000000099" {
		t.Fatalf("expected Douyin unaffected during Weibo risk_control, got %+v", dyAutoCached)
	}
	if active, _ := mgr.IsSiteInBackoff("weibo"); !active {
		t.Fatalf("expected Weibo in backoff after risk_control")
	}
	if active, _ := mgr.IsSiteInBackoff("bilibili"); active {
		t.Fatalf("Bilibili must not enter backoff on Weibo risk_control")
	}
	if active, _ := mgr.IsSiteInBackoff("douyin"); active {
		t.Fatalf("Douyin must not enter backoff on Weibo risk_control")
	}

	// Phase 4: Unlogged Weibo skips Weibo accounts cleanly with "未登录微博，已跳过" and 0 calls,
	// while Bilibili & Douyin proceed unaffected!
	now = now.Add(3 * time.Hour)
	wbAuth = false
	wbFetcher.resetCalls()
	dyFetcher.resetCalls()
	biliBefore := biliCalls
	sum4, err := mgr.CheckNow(ctx, 0)
	if err != nil {
		t.Fatalf("Phase 4 unlogged Weibo CheckNow: %v", err)
	}
	if !slices.Contains(sum4.SkippedUnauthSites, "weibo") || len(wbFetcher.callSnapshot()) != 0 {
		t.Fatalf("expected Weibo skipped with 0 calls, got sum=%+v calls=%v", sum4, wbFetcher.callSnapshot())
	}
	if biliCalls <= biliBefore || len(dyFetcher.callSnapshot()) == 0 {
		t.Fatalf("expected Bilibili & Douyin still checked while Weibo unlogged")
	}
	stUnauth, _, _ := mgr.GetAccountState("weibo", wbUID1)
	if stUnauth.Note != "未登录微博，已跳过" {
		t.Fatalf("expected Weibo unlogged skip note '未登录微博，已跳过', got %q", stUnauth.Note)
	}
}

func TestWeiboHourlyQuotaRoundCapPacingMaxPagesManualBypassAndCheckPlan(t *testing.T) {
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
	cfg.Mode = "interval"
	cfg.IntervalMinutes = 60
	mgr, err := NewManagerWithUpdatesDB(db, updatesDB, filepath.Join(tmp, "subscriptions.json"), nil, nil, nil, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	mgr.SetNowFunc(func() time.Time { return now })

	var sleepsMu sync.Mutex
	var recordedSleeps []time.Duration
	mgr.SetSleepFunc(func(_ context.Context, d time.Duration) error {
		sleepsMu.Lock()
		recordedSleeps = append(recordedSleeps, d)
		sleepsMu.Unlock()
		return nil
	})

	wbAuth := true
	mgr.SetSiteAuthCheck(func(s site.ID) bool {
		if s == site.Weibo {
			return wbAuth
		}
		return true
	})

	wbFetcher := &syntheticSiteFetcher{
		siteID: site.Weibo,
		handler: func(ctx context.Context, uid, cursor string) (site.CreatorPostsPage, error) {
			if site.AutomaticRequestSource(ctx) != "" {
				if err := mgr.SiteAutomaticRequestAllowed("weibo"); err != nil {
					return site.CreatorPostsPage{}, err
				}
			}
			if cursor == "" {
				return site.CreatorPostsPage{
					HasMore:    true,
					NextCursor: "2:5000000000000010",
					Posts:      []site.CreatorPost{{ID: "5000000000000020", PubTs: 1700002000, Type: "word", Title: "p1"}},
				}, nil
			}
			if cursor == "2:5000000000000010" {
				return site.CreatorPostsPage{
					HasMore:    true,
					NextCursor: "3:5000000000000001",
					Posts:      []site.CreatorPost{{ID: "5000000000000015", PubTs: 1700001500, Type: "word", Title: "p2"}},
				}, nil
			}
			t.Fatalf("Weibo incremental check exceeded max 2 pages per account: uid=%s cursor=%q", uid, cursor)
			return site.CreatorPostsPage{}, nil
		},
	}
	mgr.RegisterFetcher(wbFetcher)

	// Create 25 Weibo accounts with ScheduledCheck + AutoRecord enabled, plus 1 Weibo account without AutoRecord.
	wbUID := func(i int) string { return fmt.Sprintf("%d", 1000000100+i) }
	for i := 0; i < 25; i++ {
		if _, err := mgr.Create(fmt.Sprintf("Weibo-%02d", i), "", []AccountRef{
			{Site: "weibo", UID: wbUID(i), Name: fmt.Sprintf("WB%02d", i)},
		}, SubRules{ScheduledCheck: true, AutoRecord: true}); err != nil {
			t.Fatalf("create weibo %d: %v", i, err)
		}
	}
	noLiveSub, err := mgr.Create("Weibo-NoAutoRec", "", []AccountRef{
		{Site: "weibo", UID: "1000000999", Name: "WBNoLive"},
	}, SubRules{ScheduledCheck: false, AutoRecord: false})
	if err != nil {
		t.Fatalf("create noLiveSub: %v", err)
	}

	// 1. CheckPlan lists Weibo accounts with round-cap 20 estimates, NO live-check cadence (AutoRecord=false),
	// and LiveNote="微博直播暂不支持录制" ONLY when rule has AutoRecord enabled!
	plans := mgr.CheckPlan()
	planByUID := make(map[string]AccountCheckPlan, len(plans))
	for _, p := range plans {
		planByUID[p.UID] = p
	}
	next1 := now.Add(time.Hour).Format(time.RFC3339)
	next2 := now.Add(2 * time.Hour).Format(time.RFC3339)
	for i := 0; i < 25; i++ {
		p := planByUID[wbUID(i)]
		wantNext := next1
		if i >= 20 {
			wantNext = next2
		}
		if p.Plan != PlanScheduled || p.NextAt != wantNext {
			t.Fatalf("Weibo CheckPlan[%d] = %+v, want Plan=scheduled NextAt=%s", i, p, wantNext)
		}
		if p.AutoRecord {
			t.Fatalf("Weibo CheckPlan[%d] must have AutoRecord=false (no live cadence), got %+v", i, p)
		}
		if p.LiveNote != WeiboLiveUnsupportedMessage {
			t.Fatalf("Weibo CheckPlan[%d] LiveNote = %q, want %q", i, p.LiveNote, WeiboLiveUnsupportedMessage)
		}
	}
	if p := planByUID["1000000999"]; p.Plan != PlanManual || p.AutoRecord || p.LiveNote != "" {
		t.Fatalf("Weibo manual non-autoRecord plan = %+v, want manual with empty LiveNote", p)
	}
	_ = noLiveSub

	// 2. Scheduled round checks <= 20 Weibo accounts with 19 inter-account sleeps of 3s!
	ctx := context.Background()
	sum1, err := mgr.RunScheduledRound(ctx)
	if err != nil {
		t.Fatalf("RunScheduledRound 1: %v", err)
	}
	if sum1.CheckedCreators != 20 || len(wbFetcher.callSnapshot()) != 20 {
		t.Fatalf("expected 20 Weibo accounts checked (cap 20), got sum=%+v calls=%d", sum1, len(wbFetcher.callSnapshot()))
	}
	if len(recordedSleeps) != 19 {
		t.Fatalf("expected 19 Weibo inter-account sleeps, got %d", len(recordedSleeps))
	}
	if DefaultWeiboAccountInterval != 3*time.Second {
		t.Fatalf("DefaultWeiboAccountInterval = %v, want 3s", DefaultWeiboAccountInterval)
	}
	for _, d := range recordedSleeps {
		if d != DefaultWeiboAccountInterval {
			t.Fatalf("expected %v Weibo pacing sleep, got %v", DefaultWeiboAccountInterval, d)
		}
	}

	// 3. Max 2 pages per account on incremental check (account 0 now has baseline@1700002000;
	// force older watermark so both pages are newer and HasMore stays true on page 2 -> stops at 2 pages + gapDetected!).
	mgr.mu.Lock()
	_ = mgr.saveAccountStateLocked(AccountState{
		Site:          "weibo",
		UID:           wbUID(0),
		BaselineAt:    now.Format(time.RFC3339),
		LastCheckedAt: now.Add(-2 * time.Hour).Format(time.RFC3339),
		NewestID:      "5000000000000001",
		NewestTs:      1700000001,
	})
	mgr.mu.Unlock()
	wbFetcher.resetCalls()
	owner0, _ := mgr.OwnerOf("weibo", wbUID(0))
	sum2Pages, err := mgr.CheckAccount(ctx, owner0.ID, "weibo", wbUID(0))
	if err != nil {
		t.Fatalf("CheckAccount max 2 pages: %v", err)
	}
	if calls := wbFetcher.callSnapshot(); len(calls) != 2 {
		t.Fatalf("expected exactly 2 page fetches for incremental Weibo check, got %v", calls)
	}
	if !slices.Contains(sum2Pages.GapAccounts, "weibo:"+wbUID(0)) {
		t.Fatalf("expected gap recorded when 2 pages do not reach baseline, got %+v", sum2Pages)
	}

	// 4. Hourly 100 quota exhaustion defers scheduled Weibo round, while manual check bypasses quota!
	// 20 tokens already used in round 1; consume 65 more -> 85 used (15 left < 2*20=40 required).
	if !mgr.ConsumeSiteQuotaTokens("weibo", 65) {
		t.Fatalf("expected 65 Weibo tokens to fit within remaining 80")
	}
	if mgr.ConsumeSiteQuotaTokens("weibo", 16) {
		t.Fatalf("85 + 16 = 101 must exceed Weibo 100/hr bucket")
	}
	wbFetcher.resetCalls()
	sumQuota, err := mgr.RunScheduledRound(ctx)
	if err != nil {
		t.Fatalf("RunScheduledRound quota deferred: %v", err)
	}
	if !sumQuota.DeferredByQuota || !slices.Contains(sumQuota.SiteDeferredByQuota, "weibo") || len(wbFetcher.callSnapshot()) != 0 {
		t.Fatalf("expected Weibo deferred by 100/hr quota with 0 calls, got sum=%+v calls=%v", sumQuota, wbFetcher.callSnapshot())
	}

	// Exhaust remaining 15 tokens completely (100/100 used), then verify manual check STILL succeeds without consuming quota!
	if !mgr.ConsumeSiteQuotaTokens("weibo", 15) {
		t.Fatalf("expected final 15 tokens to succeed")
	}
	if mgr.CanUseSiteQuotaTokens("weibo", 1) {
		t.Fatalf("expected Weibo bucket completely full at 100/100")
	}
	wbFetcher.resetCalls()
	sumManual, err := mgr.CheckAccount(ctx, owner0.ID, "weibo", wbUID(0))
	if err != nil || sumManual.CheckedCreators != 1 || sumManual.FailedCreators != 0 {
		t.Fatalf("manual Weibo check must bypass 100/hr quota, got sum=%+v err=%v", sumManual, err)
	}
	if st := mgr.GetSiteSchedulerState("weibo"); len(st.TokenTimestamps) != 100 {
		t.Fatalf("manual Weibo check must not add quota tokens, got %d tokens", len(st.TokenTimestamps))
	}

	// 5. CheckPlan reflects Weibo backoff & unauthenticated state accurately.
	mgr.RecordSiteAutomaticStop("weibo", site.ErrKindRateLimited)
	_, until := mgr.IsSiteInBackoff("weibo")
	for _, p := range mgr.CheckPlan() {
		if p.UID == wbUID(0) {
			at, _ := time.Parse(time.RFC3339, p.NextAt)
			if !p.AfterBackoff || at.Before(until) {
				t.Fatalf("expected Weibo CheckPlan pushed past backoff %v, got %+v", until, p)
			}
		}
	}
	wbAuth = false
	for _, p := range mgr.CheckPlan() {
		if p.UID == wbUID(0) && (p.Plan != PlanUnauth || p.NextAt != "") {
			t.Fatalf("expected Weibo PlanUnauth when logged out, got %+v", p)
		}
	}
}

func TestWeiboFollowingImportPreservesExistingAndExcludesAutoRecord(t *testing.T) {
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

	subsPath := filepath.Join(tmp, "subscriptions.json")
	mgr, err := NewManagerWithUpdatesDB(db, updatesDB, subsPath, nil, nil, nil, settings.Default(tmp).Subscription, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Existing subscribed Weibo account (with a custom name, rule, and baseline watermark).
	existingSub, err := mgr.Create("已存在微博博主", "keep me", []AccountRef{
		{Site: "weibo", UID: "1000000003", Name: "已存在微博博主"},
	}, SubRules{ScheduledCheck: true, AutoRecord: true})
	if err != nil {
		t.Fatalf("Create existing Weibo sub: %v", err)
	}
	mgr.mu.Lock()
	_ = mgr.saveAccountStateLocked(AccountState{
		Site:       "weibo",
		UID:        "1000000003",
		BaselineAt: "2026-11-21T01:00:00Z",
		NewestID:   "5000000000000099",
		NewestTs:   1700009999,
	})
	mgr.mu.Unlock()

	// Reject batch containing unknown site or invalid UID before modifying state.
	if _, err := mgr.ImportAccountRefs([]AccountRef{
		{Site: "weibo", UID: "1000000001", Name: "Valid"},
		{Site: "unknown", UID: "1000000002", Name: "InvalidSite"},
	}, DefaultSubRules()); err == nil {
		t.Fatalf("expected unknown site in ImportAccountRefs rejected")
	}
	if _, ok := mgr.OwnerOf("weibo", "1000000001"); ok {
		t.Fatalf("failed ImportAccountRefs must not partially commit accounts")
	}

	// Disk write failure rolls back atomically.
	mgr.diskWriteHook = func() error { return errors.New("synthetic disk write error") }
	if _, err := mgr.ImportAccountRefs([]AccountRef{
		{Site: "weibo", UID: "1000000001", Name: "RollbackTest"},
	}, DefaultSubRules()); err == nil {
		t.Fatalf("expected ImportAccountRefs error on disk failure")
	}
	mgr.diskWriteHook = nil
	if _, ok := mgr.OwnerOf("weibo", "1000000001"); ok {
		t.Fatalf("disk write failure did not roll back ImportAccountRefs")
	}

	// Successful Weibo following import: 0 network requests, normalizes leading zeros,
	// strips signed face URLs, skips in-batch duplicates & already-subscribed accounts,
	// and avoids "UID 0" default name!
	res, err := mgr.ImportAccountRefs([]AccountRef{
		{Site: "weibo", UID: "001000000001", Name: "合成博主甲", Face: "https://wx1.sinaimg.cn/large/face1.jpg"},
		{Site: "weibo", UID: "1000000002", Name: "", Face: "https://tvax1.sinaimg.cn/crop.0.0.180.180.180/f.jpg?KID=imgbed,tva&Expires=1790000000&ssig=SYNTH"},
		{Site: "weibo", UID: "001000000002", Name: "同批重复"},
		{Site: "weibo", UID: "1000000003", Name: "改名不应覆盖已有订阅"},
	}, DefaultSubRules())
	if err != nil {
		t.Fatalf("ImportAccountRefs: %v", err)
	}
	if res.Created != 2 || res.Skipped != 2 {
		t.Fatalf("ImportAccountRefs result = %+v, want {Created:2 Skipped:2}", res)
	}

	sub1, acc1, ok := mgr.GetAccount("weibo", "1000000001")
	if !ok || sub1.Name != "合成博主甲" || acc1.Face != "https://wx1.sinaimg.cn/large/face1.jpg" || sub1.Rules.ScheduledCheck {
		t.Fatalf("unexpected imported Weibo sub1: sub=%+v acc=%+v ok=%v", sub1, acc1, ok)
	}
	sub2, acc2, ok := mgr.GetAccount("weibo", "1000000002")
	if !ok || sub2.Name == "UID 0" || !strings.Contains(sub2.Name, "1000000002") || acc2.Face != "" {
		t.Fatalf("unexpected imported Weibo sub2: sub=%+v acc=%+v ok=%v", sub2, acc2, ok)
	}
	sub3, ok := mgr.OwnerOf("weibo", "1000000003")
	if !ok || sub3.ID != existingSub.ID || sub3.Name != "已存在微博博主" || !sub3.Rules.ScheduledCheck {
		t.Fatalf("already-subscribed Weibo account was unexpectedly overwritten: %+v", sub3)
	}
	st3, _, _ := mgr.GetAccountState("weibo", "1000000003")
	if st3.NewestID != "5000000000000099" || st3.NewestTs != 1700009999 {
		t.Fatalf("existing Weibo watermark was overwritten: %+v", st3)
	}

	// Existing Bilibili ImportCreators behavior stays intact alongside Weibo subscriptions.
	biliCount, err := mgr.ImportCreators([]bilibili.FollowingUser{
		{Mid: 77001, Uname: "B站关注甲", Face: "https://example.invalid/bili.jpg"},
	}, "both", "inherit", true, false)
	if err != nil || biliCount != 1 {
		t.Fatalf("Bilibili ImportCreators = (%d, %v), want (1, nil)", biliCount, err)
	}
	if _, ok := mgr.OwnerOf("bilibili", "77001"); !ok {
		t.Fatalf("expected Bilibili ImportCreators to create bilibili:77001")
	}

	// ListAutoRecordAccounts excludes Weibo even when a Weibo subscription has AutoRecord=true!
	if _, err := mgr.Create("可录制B站", "", []AccountRef{{Site: "bilibili", UID: "88001"}}, SubRules{AutoRecord: true}); err != nil {
		t.Fatal(err)
	}
	if wbAuto := mgr.ListAutoRecordAccounts("weibo"); len(wbAuto) != 0 {
		t.Fatalf("ListAutoRecordAccounts(weibo) must be empty, got %+v", wbAuto)
	}
	allAuto := mgr.ListAutoRecordAccounts("")
	for _, a := range allAuto {
		if a.Site == "weibo" {
			t.Fatalf("ListAutoRecordAccounts(\"\") leaked Weibo account: %+v", allAuto)
		}
	}
	if len(allAuto) != 1 || allAuto[0].Site != "bilibili" || allAuto[0].UID != "88001" {
		t.Fatalf("expected only Bilibili auto-record account, got %+v", allAuto)
	}

	// Ensure no SQLite rows are open/corrupt by running a quick query.
	var totalSubs int
	if err := db.QueryRow(`SELECT COUNT(*) FROM subscriptions`).Scan(&totalSubs); err != nil || totalSubs != 5 {
		t.Fatalf("expected 5 total subscriptions (3 weibo + 2 bilibili), got %d (err=%v)", totalSubs, err)
	}
	_ = sql.ErrNoRows
}
