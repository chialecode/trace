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

	// Clean upgrade converts "bilibili" -> "all" once and stamps version 2 on disk.
	store, recovered, _, err := Open(path)
	if err != nil || recovered {
		t.Fatalf("Open v1 ui-state: recovered=%v err=%v", recovered, err)
	}
	st := store.Get()
	if st.Version != 2 || st.FeedFilter.Site != "all" || st.FeedFilter.Kind != "video" || st.LastRoute != "/library" {
		t.Fatalf("unexpected migrated state: %+v", st)
	}
	v, exists, err := storage.ReadSchemaVersion(path, storage.SchemaJSON)
	if err != nil || !exists || v != 2 {
		t.Fatalf("expected on-disk version 2, got v=%d exists=%v err=%v", v, exists, err)
	}

	// Subsequent user selection of "bilibili" or "douyin" in v2+ is preserved across restarts.
	if _, err := store.Mutate(func(s *State) {
		s.FeedFilter.Site = "bilibili"
	}); err != nil {
		t.Fatal(err)
	}
	reopened, _, _, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Get().FeedFilter.Site; got != "bilibili" {
		t.Fatalf("v2 reopen overwrote user choice of bilibili: got %q", got)
	}

	if _, err := reopened.Mutate(func(s *State) {
		s.FeedFilter.Site = "douyin"
	}); err != nil {
		t.Fatal(err)
	}
	reopened2, _, _, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened2.Get().FeedFilter.Site; got != "douyin" {
		t.Fatalf("v2 reopen overwrote user choice of douyin: got %q", got)
	}

	// Empty site in v2+ defaults to "all".
	v2EmptySite := `{"version":2,"feedFilter":{"kind":"all","site":""}}`
	if err := os.WriteFile(path, []byte(v2EmptySite), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened3, _, _, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened3.Get().FeedFilter.Site; got != "all" {
		t.Fatalf("empty site in v2 should default to all, got %q", got)
	}

	// Newer version is rejected without writing.
	v9Raw := `{"version":9,"feedFilter":{"kind":"all","site":"douyin"}}`
	if err := os.WriteFile(path, []byte(v9Raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := Open(path); !storage.IsNewerData(err) {
		t.Fatalf("expected NewerDataError on v9 ui-state, got %v", err)
	}
}

func TestUIStateSubDetailTabNormalization(t *testing.T) {
	if CurrentSchemaVersion != 2 {
		t.Fatalf("CurrentSchemaVersion = %d, want 2", CurrentSchemaVersion)
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

	// Legacy subDetailTab="live" in v2 ui-state.json maps to "accounts" without bumping schema version.
	if err := os.WriteFile(path, []byte(`{"version":2,"lastRoute":"/subscriptions","subDetailTab":"live"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, recovered, _, err := Open(path)
	if err != nil || recovered {
		t.Fatalf("Open legacy subDetailTab=live: recovered=%v err=%v", recovered, err)
	}
	if got := store.Get(); got.SubDetailTab != "accounts" || got.Version != 2 {
		t.Fatalf("expected SubDetailTab=accounts and Version=2, got %+v", got)
	}

	// Route query param ?tab=live also maps to "accounts" when subDetailTab is omitted.
	if err := os.WriteFile(path, []byte(`{"version":2,"lastRoute":"/subscriptions/sub-1?tab=live"}`), 0o600); err != nil {
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
	if err := os.WriteFile(path, []byte(`{"version":2,"lastRoute":"/subscriptions","subDetailTab":"legacy-updates"}`), 0o600); err != nil {
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

// DEC-053 ①: homePositions is optional in schema version 2; a file without
// it reads as before, and invalid entries are dropped.
func TestHomePositionsAreOptionalAndPerSiteFilter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ui-state.json")
	if err := os.WriteFile(path, []byte(`{"version":2,"lastRoute":"/home","anchor":{"order":"newest","key":"k1","pubTs":10}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, recovered, _, err := Open(path)
	if err != nil || recovered {
		t.Fatalf("Open without homePositions: recovered=%v err=%v", recovered, err)
	}
	if got := store.Get(); got.HomePositions != nil || got.Anchor.Key != "k1" || got.Version != 2 {
		t.Fatalf("old file read differently: %+v", got)
	}
	next, err := store.Mutate(func(st *State) {
		st.HomePositions = map[string]HomePosition{
			"all":      {Key: "a", PubTs: 5, OffsetPx: 12},
			"Bilibili": {Key: "b", PubTs: 4, OffsetPx: -3},
			"douyin":   {},
			"weibo":    {Key: "w", PubTs: 1},
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.HomePositions) != 2 || next.HomePositions["all"].OffsetPx != 12 || next.HomePositions["bilibili"].OffsetPx != 0 {
		t.Fatalf("unexpected normalized positions: %+v", next.HomePositions)
	}
	reopened, _, _, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.Get()
	if got.Version != 2 || got.HomePositions["all"] != (HomePosition{Key: "a", PubTs: 5, OffsetPx: 12}) {
		t.Fatalf("positions not kept: %+v", got.HomePositions)
	}
	got.HomePositions["all"] = HomePosition{Key: "changed"}
	if reopened.Get().HomePositions["all"].Key != "a" {
		t.Fatal("Get must return a copy of homePositions")
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
