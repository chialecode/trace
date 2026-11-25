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
	"strings"
	"sync"
	"testing"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
)

// snapshotEnv is a subscription manager on a fake dynamics feed whose
// posts per account the test controls.
type snapshotEnv struct {
	mgr   *Manager
	mu    sync.Mutex
	posts map[string][]string // uid -> post IDs, newest first
	asked map[string]int      // uid -> feed requests
}

func newSnapshotEnv(t *testing.T) *snapshotEnv {
	t.Helper()
	e := &snapshotEnv{posts: map[string][]string{}, asked: map[string]int{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/x/polymer/web-dynamic/v1/feed/space" {
			http.NotFound(w, r)
			return
		}
		uid := r.URL.Query().Get("host_mid")
		e.mu.Lock()
		e.asked[uid]++
		ids := append([]string(nil), e.posts[uid]...)
		e.mu.Unlock()
		var items []string
		for _, id := range ids {
			ts := 1000
			if strings.HasSuffix(id, "new") {
				ts = 2000
			}
			items = append(items, fmt.Sprintf(`{"id_str":%q,"type":"DYNAMIC_TYPE_WORD","modules":{"module_author":{"pub_ts":%d},"module_dynamic":{"desc":{"text":"合成动态"}}}}`, id, ts))
		}
		_, _ = fmt.Fprintf(w, `{"code":0,"data":{"has_more":false,"items":[%s]}}`, strings.Join(items, ","))
	}))
	t.Cleanup(server.Close)
	root := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(root, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
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
	e.mgr, err = NewManager(db, lib, nil, client, settings.Default(lib.CacheDir()).Subscription, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.mgr.SetAccountInterval(0)
	return e
}

func (e *snapshotEnv) setPosts(uid string, ids ...string) {
	e.mu.Lock()
	e.posts[uid] = ids
	e.mu.Unlock()
}

func (e *snapshotEnv) askedFor(uid string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.asked[uid]
}

func syncedSnapshot(accounts ...string) Snapshot {
	snap := Snapshot{Format: SnapshotFormat, Version: SnapshotVersion}
	sub := SnapshotSubscription{ID: "s1", Name: "合成订阅", Tags: []string{TagScheduledCheck}}
	for _, uid := range accounts {
		sub.Accounts = append(sub.Accounts, SnapshotAccount{Site: "bilibili", UID: uid, Name: "UP" + uid})
	}
	snap.Subscriptions = []SnapshotSubscription{sub}
	return snap
}

// An account synced to the remote first gets
// a start point; content after it is an update; a later sync keeps the
// start point of accounts it keeps, starts new accounts fresh and stops
// checking accounts it leaves out, deleting no file.
func TestSnapshotReplaceKeepsStartPointsAndStopsRemovedAccounts(t *testing.T) {
	e := newSnapshotEnv(t)
	ctx := context.Background()
	e.setPosts("1", "1-old")
	if err := e.mgr.ReplaceFromSnapshot(syncedSnapshot("1")); err != nil {
		t.Fatal(err)
	}
	sum, err := e.mgr.CheckSubscription(ctx, "s1")
	if err != nil || sum.BaselineInitialized != 1 {
		t.Fatalf("first check = %+v %v, want a start point", sum, err)
	}
	if ups, _ := e.mgr.ListUpdates("s1"); len(ups) != 0 {
		t.Fatalf("content before the start point became %d updates", len(ups))
	}

	e.setPosts("1", "1-new", "1-old")
	e.setPosts("2", "2-old")
	if err := e.mgr.ReplaceFromSnapshot(syncedSnapshot("1", "2")); err != nil {
		t.Fatal(err)
	}
	sum, err = e.mgr.CheckSubscription(ctx, "s1")
	if err != nil || sum.BaselineInitialized != 1 {
		t.Fatalf("after adding account 2: %+v %v, want only account 2 to get a start point", sum, err)
	}
	ups, _ := e.mgr.ListUpdates("s1")
	if len(ups) != 1 || ups[0].DynID != "1-new" {
		t.Fatalf("updates = %+v, want only 1-new", ups)
	}

	// A file in the save location stays when its account is removed.
	keep := filepath.Join(t.TempDir(), "kept.mp4")
	_ = os.WriteFile(keep, []byte("x"), 0o600)
	before := e.askedFor("1")
	if err := e.mgr.ReplaceFromSnapshot(syncedSnapshot("2")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.mgr.CheckSubscription(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if e.askedFor("1") != before {
		t.Fatal("an account left out of the sync was still checked")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("a sync deleted a file")
	}
}

// A replace that fails to write leaves the previous
// subscriptions as they were.
func TestSnapshotReplaceFailureKeepsPrevious(t *testing.T) {
	e := newSnapshotEnv(t)
	if err := e.mgr.ReplaceFromSnapshot(syncedSnapshot("1")); err != nil {
		t.Fatal(err)
	}
	e.mgr.diskWriteHook = func() error { return errors.New("synthetic disk failure") }
	if err := e.mgr.ReplaceFromSnapshot(syncedSnapshot("7", "8")); err == nil {
		t.Fatal("the replace did not report the failure")
	}
	e.mgr.diskWriteHook = nil
	subs, _ := e.mgr.List()
	if len(subs) != 1 || len(subs[0].Accounts) != 1 || subs[0].Accounts[0].UID != "1" {
		t.Fatalf("subscriptions after a failed replace = %+v", subs)
	}
	if _, err := e.mgr.ApplyImport(syncedSnapshot("9"), ImportChoices{}); err != nil {
		t.Fatal(err)
	}
}

// The preview separates new subscriptions and accounts that are already
// subscribed; applying writes once; MergeTags controls whether imported tags
// are unioned into merged subscriptions; legacy rule-based snapshots parse
// losslessly into capability tags; the snapshot never holds a Cookie; the hash
// ignores display names.
func TestSnapshotImportPreviewAndApply(t *testing.T) {
	e := newSnapshotEnv(t)
	local, err := e.mgr.Create("本地订阅", "", []AccountRef{{Site: "bilibili", UID: "1", Name: "UP1"}}, SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatal(err)
	}
	imported := Snapshot{
		Format:  SnapshotFormat,
		Version: SnapshotVersion,
		Subscriptions: []SnapshotSubscription{
			{ID: "x1", Name: "混合", Tags: []string{TagScheduledCheck, TagAutoRecord}, Accounts: []SnapshotAccount{{Site: "bilibili", UID: "1"}, {Site: "bilibili", UID: "2"}}},
			{ID: "x2", Name: "全新", Tags: []string{TagScheduledCheck}, Accounts: []SnapshotAccount{{Site: "douyin", UID: "MS4wLjABAAAA_synth_creator_acct_000002"}}},
			{ID: local.ID, Name: "同 ID 的另一个订阅", Tags: []string{TagCacheVideo}, Accounts: []SnapshotAccount{{Site: "bilibili", UID: "3"}}},
		},
	}
	pre, err := e.mgr.PreviewImport(imported)
	if err != nil {
		t.Fatal(err)
	}
	if len(pre.New) != 3 || len(pre.Existing) != 1 || pre.Existing[0].UID != "1" || pre.Existing[0].LocalSubID != local.ID {
		t.Fatalf("preview = %+v", pre)
	}
	if subs, _ := e.mgr.List(); len(subs) != 1 {
		t.Fatal("the preview wrote subscriptions")
	}

	// Merge into the subscription that has account 1 without MergeTags: keep local tags unchanged.
	if _, err := e.mgr.ApplyImport(imported, ImportChoices{MergeExisting: true, MergeTags: false}); err != nil {
		t.Fatal(err)
	}
	subs, _ := e.mgr.List()
	if len(subs) != 3 {
		t.Fatalf("after import: %d subscriptions, want local (merged) + 全新 + renamed-ID copy", len(subs))
	}
	var merged Subscription
	ids := map[string]bool{}
	for _, s := range subs {
		ids[s.ID] = true
		if s.ID == local.ID {
			merged = s
		}
	}
	if len(merged.Accounts) != 2 {
		t.Fatalf("merged subscription accounts = %+v, want 1 and 2", merged.Accounts)
	}
	if HasTag(merged.Tags, TagAutoRecord) || !HasTag(merged.Tags, TagScheduledCheck) {
		t.Fatalf("merged subscription tags changed when MergeTags=false: %+v", merged.Tags)
	}
	if len(ids) != 3 {
		t.Fatal("an imported subscription reused a local ID")
	}

	// Legacy pre-DEC-070 v1 snapshot with rules + ruleId parses cleanly into capability tags.
	legacySnapRaw := `{"format":"trace-subscriptions","version":1,"rules":[{"id":"r_legacy","name":"旧规则","scheduledCheck":true,"autoCache":true,"types":["video","draw"],"autoRecord":true}],"subscriptions":[{"id":"s_leg","name":"旧快照订阅","ruleId":"r_legacy","accounts":[{"site":"bilibili","uid":"99","name":"UP99"}]}]}`
	legacyParsed, err := ParseSnapshot([]byte(legacySnapRaw))
	if err != nil {
		t.Fatalf("ParseSnapshot legacy v1: %v", err)
	}
	if len(legacyParsed.Subscriptions) != 1 || !HasTag(legacyParsed.Subscriptions[0].Tags, TagScheduledCheck) || !HasTag(legacyParsed.Subscriptions[0].Tags, TagCacheVideo) || !HasTag(legacyParsed.Subscriptions[0].Tags, TagCacheDraw) || !HasTag(legacyParsed.Subscriptions[0].Tags, TagAutoRecord) {
		t.Fatalf("legacy snapshot tags mismatch: %+v", legacyParsed.Subscriptions)
	}

	snap, err := e.mgr.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := marshalForTest(snap)
	if strings.Contains(strings.ToLower(raw), "sessdata") || strings.Contains(raw, "fixture") {
		t.Fatal("the snapshot holds a Cookie")
	}
	h := snap.Hash()
	for i := range snap.Subscriptions {
		for j := range snap.Subscriptions[i].Accounts {
			snap.Subscriptions[i].Accounts[j].Name = "改过的显示名"
			snap.Subscriptions[i].Accounts[j].Face = "https://example.invalid/face.jpg"
		}
	}
	if snap.Hash() != h {
		t.Fatal("the hash changed with display names only")
	}
	snap.Subscriptions[0].Name = "改名"
	if snap.Hash() == h {
		t.Fatal("the hash did not change with a rename")
	}

	if _, err := ParseSnapshot([]byte(`{"format":"other","version":1}`)); err == nil {
		t.Fatal("a file of another format was accepted")
	}
	if _, err := ParseSnapshot([]byte(`{"format":"trace-subscriptions","version":2}`)); err == nil {
		t.Fatal("a newer snapshot version was accepted")
	}
}

func marshalForTest(s Snapshot) (string, error) {
	raw, err := json.Marshal(s)
	return string(raw), err
}
