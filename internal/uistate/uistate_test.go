package uistate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/storage"
)

func TestUIStateCorruptRecoveryAndNormalize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ui-state.json")

	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatalf("write corrupt ui-state: %v", err)
	}
	store, recovered, backup, err := Open(path)
	if err != nil {
		t.Fatalf("Open corrupt ui-state: %v", err)
	}
	if !recovered || !strings.Contains(filepath.Base(backup), "ui-state.json.corrupt-") {
		t.Fatalf("expected corrupt recovery, got recovered=%v backup=%q", recovered, backup)
	}
	st := store.Get()
	if st.Window.Width != 1280 || st.Window.Height != 820 {
		t.Fatalf("unexpected default window size: %+v", st.Window)
	}

	st.Window.Width = 300 // too small, should clamp to 1280
	st.LastRoute = "/settings"
	updated, err := store.Update(st)
	if err != nil {
		t.Fatalf("Update ui-state: %v", err)
	}
	if updated.Window.Width != 1280 || updated.LastRoute != "/settings" {
		t.Fatalf("unexpected normalized state: %+v", updated)
	}

	// Legacy order="oldest" in ui-state.json normalizes to "newest" while preserving Anchor Key/PubTs/OffsetPx and HomeSeen.
	legacyJSON := `{"version":1,"lastRoute":"/home","anchor":{"order":"oldest","key":"bilibili:dynamic:1001","pubTs":1700000123,"offsetPx":48},"homeSeen":{"sub:alpha":1700000500}}`
	if err := os.WriteFile(path, []byte(legacyJSON), 0o600); err != nil {
		t.Fatalf("write legacy ui-state: %v", err)
	}
	store2, recovered2, _, err := Open(path)
	if err != nil || recovered2 {
		t.Fatalf("Open legacy ui-state: recovered=%v err=%v", recovered2, err)
	}
	st2 := store2.Get()
	if st2.Anchor.Order != "newest" || st2.Anchor.Key != "bilibili:dynamic:1001" || st2.Anchor.PubTs != 1700000123 || st2.Anchor.OffsetPx != 48 {
		t.Fatalf("expected legacy anchor to normalize Order=newest while preserving Key/PubTs/OffsetPx, got %+v", st2.Anchor)
	}
	if st2.HomeSeen["sub:alpha"] != 1700000500 {
		t.Fatalf("expected HomeSeen[sub:alpha]=1700000500, got %+v", st2.HomeSeen)
	}
}

func TestUIStateSeenMapsNeverAliasCommittedState(t *testing.T) {
	store, _, _, err := Open(filepath.Join(t.TempDir(), "ui-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	st := store.Get()
	st.HomeSeen = map[string]int64{"mid:123": 100}
	st.Update.Restart = &UpdateRestart{Reason: "update", FromVersion: "1.2.3"}
	updated, err := store.Update(st)
	if err != nil {
		t.Fatal(err)
	}
	st.HomeSeen["mid:123"] = 200
	updated.HomeSeen["mid:123"] = 300
	st.Update.Restart.FromVersion = "mutated-input"
	updated.Update.Restart.FromVersion = "mutated-return"
	if got := store.Get().HomeSeen["mid:123"]; got != 100 {
		t.Fatalf("a caller changed committed seen state without an atomic write: %d", got)
	}
	if got := store.Get().Update.Restart; got == nil || got.FromVersion != "1.2.3" {
		t.Fatalf("a caller mutated committed Update.Restart pointer: %+v", got)
	}
	read := store.Get()
	read.HomeSeen["mid:123"] = 400
	read.Update.Restart.FromVersion = "mutated-read"
	if got := store.Get().HomeSeen["mid:123"]; got != 100 {
		t.Fatalf("a read exposed mutable committed state: %d", got)
	}
	if got := store.Get().Update.Restart; got == nil || got.FromVersion != "1.2.3" {
		t.Fatalf("a read exposed mutable committed Update.Restart: %+v", got)
	}
}

func TestUIStateV1ToV2MigrationIdempotencyAndPreRenameFault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ui-state.json")
	v1Raw := `{"version":1,"lastRoute":"/library","feedFilter":{"kind":"video","site":"bilibili"}}`
	if err := os.WriteFile(path, []byte(v1Raw), 0o600); err != nil {
		t.Fatal(err)
	}

	// Fault before JSON rename leaves original v1 file untouched.
	restore := storage.SetBeforeMigrationRenameHookForTest(func(targetPath, tmpPath string) error {
		return errors.New("simulated pre-rename failure")
	})
	if _, _, _, err := Open(path); err == nil {
		restore()
		t.Fatal("expected pre-rename fault to abort Open")
	}
	restore()

	diskAfterFault, _ := os.ReadFile(path)
	if string(diskAfterFault) != v1Raw {
		t.Fatalf("v1 ui-state.json modified on failed migration: %s", diskAfterFault)
	}

	// Clean upgrade converts "bilibili" -> "all" once and stamps current version (3) on disk.
	store, recovered, _, err := Open(path)
	if err != nil || recovered {
		t.Fatalf("Open v1 ui-state: recovered=%v err=%v", recovered, err)
	}
	st := store.Get()
	if st.Version != CurrentSchemaVersion || st.FeedFilter.Site != "all" || st.FeedFilter.Kind != "video" || st.LastRoute != "/library" {
		t.Fatalf("unexpected migrated state: %+v", st)
	}
	v, exists, err := storage.ReadSchemaVersion(path, storage.SchemaJSON)
	if err != nil || !exists || v != CurrentSchemaVersion {
		t.Fatalf("expected on-disk version %d, got v=%d exists=%v err=%v", CurrentSchemaVersion, v, exists, err)
	}

	// Subsequent user selection of "bilibili", "douyin", or "weibo" is preserved across restarts.
	for _, chosen := range []string{"bilibili", "douyin", "weibo"} {
		if _, err := store.Mutate(func(s *State) {
			s.FeedFilter.Site = chosen
		}); err != nil {
			t.Fatal(err)
		}
		reopened, _, _, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := reopened.Get().FeedFilter.Site; got != chosen {
			t.Fatalf("reopen overwrote user choice of %q: got %q", chosen, got)
		}
		store = reopened
	}

	// Empty site in v3 defaults to "all".
	v3EmptySite := `{"version":3,"feedFilter":{"kind":"all","site":""}}`
	if err := os.WriteFile(path, []byte(v3EmptySite), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened3, _, _, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened3.Get().FeedFilter.Site; got != "all" {
		t.Fatalf("empty site in v3 should default to all, got %q", got)
	}

	// Newer version (>3) is rejected without writing.
	v4Raw := `{"version":4,"feedFilter":{"kind":"all","site":"weibo"}}`
	if err := os.WriteFile(path, []byte(v4Raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := Open(path); !storage.IsNewerData(err) {
		t.Fatalf("expected NewerDataError on v4 ui-state, got %v", err)
	}
	afterNewer, _ := os.ReadFile(path)
	if string(afterNewer) != v4Raw {
		t.Fatalf("v4 ui-state.json modified on NewerDataError: %s", afterNewer)
	}
}

func TestUIStateSubDetailTabNormalization(t *testing.T) {
	if CurrentSchemaVersion != 3 {
		t.Fatalf("CurrentSchemaVersion = %d, want 3", CurrentSchemaVersion)
	}
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"", "content"},
		{"content", "content"},
		{"CONTENT", "content"},
		{"unknown", "content"},
		{"live", "accounts"},
		{" LIVE ", "accounts"},
		{"accounts", "accounts"},
	} {
		if got := NormalizeSubDetailTab(tc.in); got != tc.want {
			t.Fatalf("NormalizeSubDetailTab(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "ui-state.json")

	// Legacy subDetailTab="live" in v3 ui-state.json maps to "accounts".
	if err := os.WriteFile(path, []byte(`{"version":3,"lastRoute":"/subscriptions","subDetailTab":"live"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, recovered, _, err := Open(path)
	if err != nil || recovered {
		t.Fatalf("Open legacy subDetailTab=live: recovered=%v err=%v", recovered, err)
	}
	if got := store.Get(); got.SubDetailTab != "accounts" || got.Version != 3 {
		t.Fatalf("expected SubDetailTab=accounts and Version=3, got %+v", got)
	}

	// Route query param ?tab=live also maps to "accounts" when subDetailTab is omitted.
	if err := os.WriteFile(path, []byte(`{"version":3,"lastRoute":"/subscriptions/sub-1?tab=live"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	storeRoute, _, _, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := storeRoute.Get().SubDetailTab; got != "accounts" {
		t.Fatalf("expected LastRoute ?tab=live to normalize SubDetailTab=accounts, got %q", got)
	}

	// Omitted or unknown subDetailTab defaults to "content".
	if err := os.WriteFile(path, []byte(`{"version":3,"lastRoute":"/subscriptions","subDetailTab":"legacy-updates"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	storeDefault, _, _, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := storeDefault.Get().SubDetailTab; got != "content" {
		t.Fatalf("expected unknown SubDetailTab to default to content, got %q", got)
	}
}

// homePositions is optional and keeps per-site positions across all supported site filters,
// dropping empty or unsupported site keys.
func TestHomePositionsAreOptionalAndPerSiteFilter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ui-state.json")
	if err := os.WriteFile(path, []byte(`{"version":3,"lastRoute":"/home","anchor":{"order":"newest","key":"k1","pubTs":10}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, recovered, _, err := Open(path)
	if err != nil || recovered {
		t.Fatalf("Open without homePositions: recovered=%v err=%v", recovered, err)
	}
	if got := store.Get(); got.HomePositions != nil || got.Anchor.Key != "k1" || got.Version != 3 {
		t.Fatalf("old file read differently: %+v", got)
	}
	next, err := store.Mutate(func(st *State) {
		st.HomePositions = map[string]HomePosition{
			"all":         {Key: "a", PubTs: 5, OffsetPx: 12},
			"Bilibili":    {Key: "b", PubTs: 4, OffsetPx: -3},
			"douyin":      {},
			"Weibo":       {Key: "weibo:dynamic:5000000000000001", PubTs: 1700000001, OffsetPx: 24},
			"unknownsite": {Key: "u", PubTs: 1},
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.HomePositions) != 3 || next.HomePositions["all"].OffsetPx != 12 || next.HomePositions["bilibili"].OffsetPx != 0 || next.HomePositions["weibo"].Key != "weibo:dynamic:5000000000000001" {
		t.Fatalf("unexpected normalized positions: %+v", next.HomePositions)
	}
	reopened, _, _, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.Get()
	if got.Version != 3 ||
		got.HomePositions["all"] != (HomePosition{Key: "a", PubTs: 5, OffsetPx: 12}) ||
		got.HomePositions["weibo"] != (HomePosition{Key: "weibo:dynamic:5000000000000001", PubTs: 1700000001, OffsetPx: 24}) {
		t.Fatalf("positions not kept: %+v", got.HomePositions)
	}
	got.HomePositions["all"] = HomePosition{Key: "changed"}
	if reopened.Get().HomePositions["all"].Key != "a" {
		t.Fatal("Get must return a copy of homePositions")
	}
}

func TestUIStateV2ToV3MigrationBackupIdempotencyAndWeiboPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ui-state.json")
	v2Raw := `{"version":2,"lastRoute":"/home","feedFilter":{"kind":"dynamic","site":"bilibili"},"homePositions":{"all":{"key":"k1","pubTs":100,"offsetPx":10},"bilibili":{"key":"k2","pubTs":90,"offsetPx":5}}}`
	if err := os.WriteFile(path, []byte(v2Raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".premigrate-1", []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Pre-rename fault preserves original v2 file on disk.
	restore := storage.SetBeforeMigrationRenameHookForTest(func(targetPath, tmpPath string) error {
		return errors.New("simulated v2->v3 pre-rename fault")
	})
	if _, _, _, err := Open(path); err == nil {
		restore()
		t.Fatal("expected pre-rename fault to abort Open")
	}
	restore()

	afterFault, err := os.ReadFile(path)
	if err != nil || string(afterFault) != v2Raw {
		t.Fatalf("v2 file altered on pre-rename fault: %s err=%v", afterFault, err)
	}

	// Successful v2->v3 migration creates .premigrate-2, prunes .premigrate-1, and preserves v2 user choice "bilibili".
	store, recovered, _, err := Open(path)
	if err != nil || recovered {
		t.Fatalf("Open v2->v3: recovered=%v err=%v", recovered, err)
	}
	st := store.Get()
	if st.Version != 3 || st.FeedFilter.Site != "bilibili" || st.FeedFilter.Kind != "dynamic" {
		t.Fatalf("unexpected v2->v3 migrated state: %+v", st)
	}
	if st.HomePositions["bilibili"].Key != "k2" {
		t.Fatalf("v2->v3 dropped bilibili homePosition: %+v", st.HomePositions)
	}
	matches, _ := filepath.Glob(path + ".premigrate-*")
	if len(matches) != 1 || filepath.Base(matches[0]) != "ui-state.json.premigrate-2" {
		t.Fatalf("expected only ui-state.json.premigrate-2, got %v", matches)
	}
	bakBytes, err := os.ReadFile(matches[0])
	if err != nil || string(bakBytes) != v2Raw {
		t.Fatalf("premigrate-2 backup content = %q err=%v, want %q", bakBytes, err, v2Raw)
	}

	// Persisting "weibo" filter and reading position survives reopen cleanly and idempotently.
	if _, err := store.Mutate(func(s *State) {
		s.FeedFilter.Site = "weibo"
		if s.HomePositions == nil {
			s.HomePositions = make(map[string]HomePosition)
		}
		s.HomePositions["weibo"] = HomePosition{Key: "weibo:dynamic:5000000000000001", PubTs: 1730000000, OffsetPx: 36}
	}); err != nil {
		t.Fatal(err)
	}
	reopened, _, _, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.Get()
	if got.Version != 3 || got.FeedFilter.Site != "weibo" || got.HomePositions["weibo"].Key != "weibo:dynamic:5000000000000001" || got.HomePositions["weibo"].OffsetPx != 36 {
		t.Fatalf("weibo state not persisted across reopen: %+v", got)
	}
	// Idempotent reopen leaves .premigrate-2 intact.
	bakAfterReopen, _ := os.ReadFile(matches[0])
	if string(bakAfterReopen) != v2Raw {
		t.Fatalf("idempotent reopen altered .premigrate-2")
	}

	// Directory blocking .premigrate-2 must fail closed without mutating the v2 file.
	blockedDir := t.TempDir()
	blockedPath := filepath.Join(blockedDir, "ui-state.json")
	if err := os.WriteFile(blockedPath, []byte(v2Raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(blockedPath+".premigrate-2", 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := Open(blockedPath); err == nil {
		t.Fatal("expected migration to fail closed when .premigrate-2 is a directory")
	}
	stillV2, err := os.ReadFile(blockedPath)
	if err != nil || string(stillV2) != v2Raw {
		t.Fatalf("v2 file mutated after blocked migration backup: %q err=%v", stillV2, err)
	}
}

// 最近结束的录制 that were removed stay hidden for eight days; older or
// malformed entries are dropped (DEC-057).
func TestDismissedLiveSessionsExpire(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	got := normalizeDismissedLiveSessions(map[string]string{
		"bilibili:live:1": now.Add(-time.Hour).Format(time.RFC3339),
		"bilibili:live:2": now.Add(-9 * 24 * time.Hour).Format(time.RFC3339),
		"bilibili:live:3": "yesterday",
		"  ":              now.Format(time.RFC3339),
	}, now)
	if len(got) != 1 || got["bilibili:live:1"] == "" {
		t.Fatalf("dismissed = %v, want only the recent entry", got)
	}
	if normalizeDismissedLiveSessions(map[string]string{"k": "bad"}, now) != nil {
		t.Fatal("an all-invalid map was kept")
	}
}
