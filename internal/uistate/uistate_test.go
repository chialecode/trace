package uistate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestUIStatePersistAndCorruptRecovery(t *testing.T) {
	TestUIStateCorruptRecoveryAndNormalize(t)
}

func TestUIStateSeenMapsNeverAliasCommittedState(t *testing.T) {
	store, _, _, err := Open(filepath.Join(t.TempDir(), "ui-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	st := store.Get()
	st.HomeSeen = map[string]int64{"mid:123": 100}
	updated, err := store.Update(st)
	if err != nil {
		t.Fatal(err)
	}
	st.HomeSeen["mid:123"] = 200
	updated.HomeSeen["mid:123"] = 300
	if got := store.Get().HomeSeen["mid:123"]; got != 100 {
		t.Fatalf("a caller changed committed seen state without an atomic write: %d", got)
	}
	read := store.Get()
	read.HomeSeen["mid:123"] = 400
	if got := store.Get().HomeSeen["mid:123"]; got != 100 {
		t.Fatalf("a read exposed mutable committed state: %d", got)
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
