package library

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
)

func regressionStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(dir, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := OpenWithDataDir(db, filepath.Join(dir, "cache"), dir)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func regressionItem(t *testing.T, store *Store, id string) ItemMetadata {
	t.Helper()
	dir := filepath.Join(store.CacheDir(), id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "P01.mp4"), []byte("synthetic media"), 0600); err != nil {
		t.Fatal(err)
	}
	item, err := store.SaveItem(dir, ItemMetadata{Site: "bilibili", Kind: "video", ID: id, Media: []MediaFile{{Role: "video", Path: "P01.mp4", Bytes: int64(len("synthetic media"))}}})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func TestRebuildFailurePreservesRowsAwaitingStaleCleanup(t *testing.T) {
	store := regressionStore(t)
	regressionItem(t, store, "BV1xx411c7mD")
	stale := regressionItem(t, store, "BV17x411w7KC")
	if err := os.Remove(filepath.Join(stale.ItemDir, "item.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TEMP TRIGGER fail_reindex BEFORE UPDATE ON library_items BEGIN SELECT RAISE(ABORT, 'synthetic index failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RebuildIndex(); err == nil {
		t.Fatal("rebuild hid index write failure")
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM library_items WHERE item_key = ?`, stale.ItemKey).Scan(&count); err != nil || count != 1 {
		t.Fatalf("failed scan removed unrelated stale row: %d %v", count, err)
	}
}

func TestUnreadableDeletionLedgerStopsMutation(t *testing.T) {
	store := regressionStore(t)
	item := regressionItem(t, store, "BV1xx411c7mD")
	broken := []byte("{corrupt pending deletion")
	if err := os.WriteFile(store.pendingDeletesPath, broken, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Delete(item.ItemKey); err == nil {
		t.Fatal("delete replaced corrupt ledger")
	}
	if _, err := store.RebuildIndex(); err == nil {
		t.Fatal("rebuild treated corrupt ledger as empty")
	}
	if _, ok, err := store.GetItem(item.ItemKey); err != nil || !ok {
		t.Fatalf("item lost: %v", err)
	}
	if raw, err := os.ReadFile(filepath.Join(item.ItemDir, "P01.mp4")); err != nil || string(raw) != "synthetic media" {
		t.Fatal("registered media changed")
	}
	if raw, err := os.ReadFile(store.pendingDeletesPath); err != nil || string(raw) != string(broken) {
		t.Fatal("original ledger was not preserved")
	}
}

func TestRootRegistrationFailureDoesNotImportRows(t *testing.T) {
	store := regressionStore(t)
	other := regressionStore(t)
	item := regressionItem(t, other, "BV17x411w7KC")
	store.rootsFilePath = t.TempDir()
	if _, err := store.AddLibraryRoot(other.CacheDir()); err == nil {
		t.Fatal("root ledger failure was hidden")
	}
	if _, ok, err := store.GetItem(item.ItemKey); err != nil || ok {
		t.Fatalf("failed registration imported a row: %v", err)
	}
	if len(store.extraRoots) != 0 {
		t.Fatal("failed registration changed roots in memory")
	}
}

func TestMigrationPersistsSettingsBeforeRemovingOriginals(t *testing.T) {
	store := regressionStore(t)
	item := regressionItem(t, store, "BV1xx411c7mD")
	oldRoot, dest := store.CacheDir(), filepath.Join(t.TempDir(), "destination")
	if err := os.WriteFile(filepath.Join(item.ItemDir, "personal.txt"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MigrateCacheDirWithPersistence(dest, true, func() error { return errors.New("synthetic settings failure") }); err == nil {
		t.Fatal("settings failure was hidden")
	}
	if store.CacheDir() != oldRoot {
		t.Fatal("failed migration changed active root")
	}
	for _, name := range []string{"P01.mp4", "item.json", "personal.txt"} {
		if _, err := os.Stat(filepath.Join(item.ItemDir, name)); err != nil {
			t.Fatalf("old file %s lost: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, item.RawID, "P01.mp4")); !os.IsNotExist(err) {
		t.Fatalf("failed migration left new copy: %v", err)
	}
}

func TestSaveItemPreservesUnclaimedManifest(t *testing.T) {
	store := regressionStore(t)
	dir := filepath.Join(store.CacheDir(), "unclaimed")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "item.json")
	if err := os.WriteFile(path, []byte("personal data"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveItem(dir, ItemMetadata{Site: "bilibili", Kind: "dynamic", ID: "123"}); err == nil {
		t.Fatal("unclaimed manifest was replaced")
	}
	if raw, _ := os.ReadFile(path); string(raw) != "personal data" {
		t.Fatal("user manifest changed")
	}
}

func TestUpgradeProbeReturnsStorageFailure(t *testing.T) {
	store := regressionStore(t)
	item := regressionItem(t, store, "BV1xx411c7mD")
	item.Parts = []VideoPartRecord{{CID: 1, Page: 1, File: "P01.mp4", Video: PartVideoInfo{Quality: 64}}}
	if _, err := store.SaveItem(item.ItemDir, item); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"dash":{"video":[{"id":120,"codecid":7,"codecs":"avc1","baseUrl":"https://upos-sz-mirrorcos.bilivideo.com/fixture.m4s"}]}}}`))
	}))
	defer server.Close()
	creds, _, err := credential.Open(filepath.Join(t.TempDir(), "credential.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := creds.SaveManual("SESSDATA=fixture", ""); err != nil {
		t.Fatal(err)
	}
	client := bilibili.NewClient(bilibili.ClientOptions{CredStore: creds, Endpoints: bilibili.Endpoints{APIBase: server.URL}, PacingInterval: -1, AllowLocalhost: true})
	client.SetWBIKeys("7cd084941338484aae1ad9425b84077c", "4932caff0ff746eab6f01bf08b70ac45")
	if _, err := store.db.Exec(`CREATE TEMP TRIGGER fail_upgrade_save BEFORE UPDATE ON library_items BEGIN SELECT RAISE(ABORT, 'synthetic upgrade save failure'); END`); err != nil {
		t.Fatal(err)
	}
	items, err := store.CheckUpgradeKeys(context.Background(), client, []string{item.ItemKey}, true, bilibili.PlaySelectionOptions{})
	if !bilibili.IsKind(err, "storage") || len(items) != 0 {
		t.Fatalf("failed upgrade persistence was reported as success: %+v, %v", items, err)
	}
}

func TestDeletePlanPreservesUnregisteredPartAndRejectsAbsolutePaths(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "private.txt")
	_ = os.WriteFile(outside, []byte("keep"), 0600)
	_ = os.WriteFile(filepath.Join(root, "video.mp4"), []byte("registered"), 0600)
	_ = os.WriteFile(filepath.Join(root, "my-work.part"), []byte("user"), 0600)
	registered, user := collectRegisteredAndUserFiles(ItemMetadata{ItemDir: root, Media: []MediaFile{{Path: "video.mp4"}, {Path: outside}, {Path: "../private.txt"}}})
	for _, path := range registered {
		if path == outside || filepath.Base(path) == "my-work.part" {
			t.Fatalf("unsafe file in delete plan: %s", path)
		}
	}
	if len(user) != 1 || user[0] != "my-work.part" {
		t.Fatalf("user file not preserved: %v", user)
	}
}
