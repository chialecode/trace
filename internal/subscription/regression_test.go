package subscription

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
)

func regressionSubscriptions(t *testing.T) *Manager {
	t.Helper()
	dir := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(dir, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mgr, err := NewManagerWithUpdatesDB(db, db, filepath.Join(dir, "subscriptions.json"), nil, nil, bilibili.NewClient(bilibili.ClientOptions{AllowLocalhost: true, PacingInterval: -1}), settings.Default(dir).Subscription, nil)
	if err != nil {
		t.Fatal(err)
	}
	return mgr
}

func TestSubscriptionFileFailureRestoresOnlyChangedRows(t *testing.T) {
	mgr := regressionSubscriptions(t)
	first, err := mgr.Create("first", "", []AccountRef{{Site: "bilibili", UID: "1001"}}, SubRules{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := mgr.Create("second", "", []AccountRef{{Site: "bilibili", UID: "2002"}}, SubRules{})
	if err != nil {
		t.Fatal(err)
	}
	mgr.subsPath = t.TempDir()
	if _, err := mgr.Move("bilibili", "1001", second.ID); err == nil {
		t.Fatal("failed file write reported success")
	}
	owner, ok := mgr.OwnerOf("bilibili", "1001")
	if !ok || owner.ID != first.ID {
		t.Fatalf("failed move lost original owner: %+v", owner)
	}
	list, err := mgr.List()
	if err != nil || len(list) != 2 {
		t.Fatalf("failed move changed other rows: %+v %v", list, err)
	}
}

func TestMoveSQLFailurePreservesBothSubscriptions(t *testing.T) {
	mgr := regressionSubscriptions(t)
	first, err := mgr.Create("first", "", []AccountRef{{Site: "bilibili", UID: "1001"}}, SubRules{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := mgr.Create("second", "", []AccountRef{{Site: "bilibili", UID: "2002"}}, SubRules{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.db.Exec(`CREATE TEMP TRIGGER fail_destination BEFORE UPDATE ON subscriptions WHEN NEW.sub_id = '` + second.SubID + `' BEGIN SELECT RAISE(ABORT, 'synthetic move failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Move("bilibili", "1001", second.ID); err == nil {
		t.Fatal("destination write failure was hidden")
	}
	owner, ok := mgr.OwnerOf("bilibili", "1001")
	if !ok || owner.ID != first.ID {
		t.Fatalf("intermediate SQL failure lost owner: %+v", owner)
	}
	if owner, ok := mgr.OwnerOf("bilibili", "2002"); !ok || owner.ID != second.ID {
		t.Fatal("unchanged destination was deleted during rollback")
	}
}

func TestSubscriptionUpdatePaginationFiltersBeforeLimit(t *testing.T) {
	mgr := regressionSubscriptions(t)
	selected, err := mgr.Create("selected", "", []AccountRef{{Site: "bilibili", UID: "1001"}}, SubRules{})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := mgr.updatesDB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := 0; i < 450; i++ {
		uid := "2002"
		if i < 230 {
			uid = "1001"
		}
		if _, err := tx.Exec(`INSERT INTO update_item (site, uid, dyn_id, pub_ts, snapshot) VALUES ('bilibili', ?, ?, ?, '{}')`, uid, strconv.Itoa(i+1), i); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	first, err := mgr.ListUpdatesPage(selected.ID, 200, 0)
	if err != nil || len(first) != 200 {
		t.Fatalf("other accounts crowded out selected updates: %d %v", len(first), err)
	}
	second, err := mgr.ListUpdatesPage(selected.ID, 200, 200)
	if err != nil || len(second) != 30 || first[199].DynID == second[0].DynID {
		t.Fatalf("next page incomplete or duplicated: %d %v", len(second), err)
	}
}

func TestSubscriptionReloadFailureDoesNotEmptyExistingView(t *testing.T) {
	mgr := regressionSubscriptions(t)
	if _, err := mgr.Create("first", "", []AccountRef{{Site: "bilibili", UID: "1001"}}, SubRules{}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(mgr.subsPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.db.Exec(`CREATE TEMP TRIGGER fail_import BEFORE INSERT ON subscriptions BEGIN SELECT RAISE(ABORT, 'synthetic import failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := mgr.SetSubscriptionsPath(mgr.subsPath); err == nil {
		t.Fatal("reload hid SQL failure")
	}
	if list, err := mgr.List(); err != nil || len(list) != 1 {
		t.Fatalf("failed reload emptied existing view: %d %v", len(list), err)
	}
	if after, _ := os.ReadFile(mgr.subsPath); string(after) != string(raw) {
		t.Fatal("reload changed authoritative JSON")
	}
}

func TestNewlyLinkedAccountGetsIndependentBaseline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/x/polymer/web-dynamic/v1/feed/space" {
			t.Errorf("unexpected discovery request: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		uid := r.URL.Query().Get("host_mid")
		_, _ = fmt.Fprintf(w, `{"code":0,"data":{"has_more":false,"items":[{"id_str":"%s001","type":"DYNAMIC_TYPE_WORD","modules":{"module_author":{"pub_ts":100},"module_dynamic":{"desc":{"text":"old baseline"}}}}]}}`, uid)
	}))
	defer server.Close()
	root := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(root, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	lib, err := library.Open(db, filepath.Join(root, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	cred, _, err := credential.Open(filepath.Join(root, "credential.bin"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = cred.SaveManual("SESSDATA=fixture", "")
	client := bilibili.NewClient(bilibili.ClientOptions{CredStore: cred, Endpoints: bilibili.Endpoints{APIBase: server.URL}, AllowLocalhost: true, PacingInterval: -1})
	client.SetWBIKeys("7cd084941338484aae1ad9425b84077c", "4932caff0ff746eab6f01bf08b70ac45")
	mgr, err := NewManager(db, lib, nil, client, settings.Default(lib.CacheDir()).Subscription, nil)
	if err != nil {
		t.Fatal(err)
	}
	mgr.SetAccountInterval(0)
	sub, err := mgr.Create("person", "", []AccountRef{{Site: "bilibili", UID: "1", Name: "first"}}, SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatal(err)
	}
	if sum, err := mgr.CheckSubscription(context.Background(), sub.ID); err != nil || sum.BaselineInitialized != 1 {
		t.Fatalf("first baseline: %+v %v", sum, err)
	}
	if _, err := mgr.Link(sub.ID, "bilibili", "2", "second", ""); err != nil {
		t.Fatal(err)
	}
	sum, err := mgr.CheckSubscription(context.Background(), sub.ID)
	if err != nil || sum.BaselineInitialized != 1 {
		t.Fatalf("new account baseline: %+v %v", sum, err)
	}
	updates, err := mgr.ListUpdates(sub.ID)
	if err != nil || len(updates) != 0 {
		t.Fatalf("existing posts entered update list on baseline: %d %v", len(updates), err)
	}
}

func TestCanonicalUIDCannotBelongToTwoSubscriptions(t *testing.T) {
	mgr := regressionSubscriptions(t)
	if _, err := mgr.Create("first", "", []AccountRef{{Site: "bilibili", UID: "1001"}}, SubRules{}); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Create("duplicate", "", []AccountRef{{Site: "bilibili", UID: "01001"}}, SubRules{}); err == nil {
		t.Fatal("leading zero bypassed account ownership")
	}
}

func TestLegacyBaselineSnapshotsStayOutOfUpdatesWithoutDeletingRows(t *testing.T) {
	mgr := regressionSubscriptions(t)
	sub, err := mgr.Create("person", "", []AccountRef{{Site: "bilibili", UID: "1001"}}, SubRules{})
	if err != nil {
		t.Fatal(err)
	}
	// Recreate the exact legacy table, including an already-viewed incremental
	// update. Migration must distinguish the initial snapshot without using seen_at.
	if _, err := mgr.updatesDB.Exec(`DROP TABLE update_item;
		CREATE TABLE update_item (
			site TEXT NOT NULL, uid TEXT NOT NULL, dyn_id TEXT NOT NULL,
			pub_ts INTEGER NOT NULL DEFAULT 0, type TEXT NOT NULL DEFAULT '',
			snapshot TEXT NOT NULL DEFAULT '', discovered_at TEXT NOT NULL DEFAULT '',
			seen_at TEXT NOT NULL DEFAULT '', auto_task_id TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (site, uid, dyn_id));
		INSERT INTO account_state (site, uid, baseline_at, newest_ts) VALUES ('bilibili', '1001', '2026-11-01T12:00:03Z', 200);
		INSERT INTO update_item (site, uid, dyn_id, pub_ts, snapshot, discovered_at, seen_at) VALUES
			('bilibili', '1001', 'baseline', 100, 'initial snapshot', '2026-11-01T12:00:00Z', '2026-11-01T12:00:00Z'),
			('bilibili', '1001', 'incremental', 200, 'new update', '2026-11-01T13:00:00Z', '2026-11-01T13:30:00Z');`); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		reopened, err := NewManagerWithUpdatesDB(mgr.db, mgr.updatesDB, mgr.subsPath, nil, nil, mgr.client, mgr.subSettings, nil)
		if err != nil {
			t.Fatal(err)
		}
		updates, err := reopened.ListUpdatesPage(sub.ID, 200, 0)
		if err != nil || len(updates) != 1 || updates[0].DynID != "incremental" {
			t.Fatalf("legacy baseline appeared as a new update: %+v err=%v", updates, err)
		}
		list, err := reopened.List()
		if err != nil || len(list) != 1 || list[0].LatestUpdateTs != 200 {
			t.Fatalf("latest-update ordering lost the incremental update: %+v err=%v", list, err)
		}
	}
	var count int
	var snapshot, viewedAt string
	if err := mgr.updatesDB.QueryRow(`SELECT COUNT(*) FROM update_item`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("migration deleted old records: count=%d err=%v", count, err)
	}
	if err := mgr.updatesDB.QueryRow(`SELECT snapshot, seen_at FROM update_item WHERE dyn_id = 'baseline'`).Scan(&snapshot, &viewedAt); err != nil || snapshot != "initial snapshot" || viewedAt != "2026-11-01T12:00:00Z" {
		t.Fatalf("migration rewrote legacy data: snapshot=%q seen_at=%q err=%v", snapshot, viewedAt, err)
	}
}

func TestSubscriptionListReportsUpdateIndexFailure(t *testing.T) {
	mgr := regressionSubscriptions(t)
	if _, err := mgr.Create("person", "", []AccountRef{{Site: "bilibili", UID: "1001"}}, SubRules{}); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.updatesDB.Exec(`DROP TABLE update_item`); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.List(); err == nil {
		t.Fatal("a broken update index was reported as subscriptions having no recent updates")
	}
}

func TestBaselineMigrationFailureRollsBackSchemaAndRecords(t *testing.T) {
	mgr := regressionSubscriptions(t)
	if _, err := mgr.updatesDB.Exec(`ALTER TABLE update_item DROP COLUMN baseline_snapshot;
		INSERT INTO account_state (site, uid, baseline_at) VALUES ('bilibili', '1', '2026-11-01T12:00:03Z');
		INSERT INTO update_item (site, uid, dyn_id, discovered_at) VALUES ('bilibili', '1', 'fixture', '2026-11-01T12:00:00Z');
		CREATE TEMP TRIGGER fail_migration BEFORE UPDATE ON update_item BEGIN SELECT RAISE(ABORT, 'synthetic migration failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if err := mgr.initSchema(); err == nil {
		t.Fatal("failed legacy migration reported success")
	}
	if _, err := mgr.updatesDB.Exec(`SELECT baseline_snapshot FROM update_item`); err == nil {
		t.Fatal("migration committed its schema marker despite failing to classify legacy rows")
	}
	var count int
	if err := mgr.updatesDB.QueryRow(`SELECT COUNT(*) FROM update_item`).Scan(&count); err != nil || count != 1 {
		t.Fatal("failed migration lost original records")
	}
	if _, err := mgr.updatesDB.Exec(`DROP TRIGGER fail_migration`); err != nil {
		t.Fatal(err)
	}
	if err := mgr.initSchema(); err != nil {
		t.Fatalf("migration could not recover after the storage error was removed: %v", err)
	}
	updates, err := mgr.ListUpdates("")
	if err != nil || len(updates) != 0 {
		t.Fatalf("recovered migration showed its baseline snapshot: %+v %v", updates, err)
	}
}
