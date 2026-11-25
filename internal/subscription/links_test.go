package subscription

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/storage"
)

func TestCustomLinkValidationAndDerivedLabels(t *testing.T) {
	// 1. Strict HTTP/HTTPS validation and scheme/credential/secret rejection.
	validCases := []struct {
		raw       CustomLink
		wantURL   string
		wantLabel string
		wantDisp  string
	}{
		{
			raw:       CustomLink{URL: "  https://WWW.Linktr.ee/Creator_01/  "},
			wantURL:   "https://www.linktr.ee/Creator_01/",
			wantLabel: "",
			wantDisp:  "linktr.ee/Creator_01",
		},
		{
			raw:       CustomLink{Label: "  个人汇总页  ", URL: "HTTPS://Link3.cc/alpha"},
			wantURL:   "https://link3.cc/alpha",
			wantLabel: "个人汇总页",
			wantDisp:  "个人汇总页",
		},
		{
			raw:       CustomLink{URL: "http://blog.example.org/"},
			wantURL:   "http://blog.example.org/",
			wantLabel: "",
			wantDisp:  "blog.example.org",
		},
		{
			raw:       CustomLink{URL: "https://example.com:8443/docs/guide?utm_source=x#top"},
			wantURL:   "https://example.com:8443/docs/guide?utm_source=x#top",
			wantLabel: "",
			wantDisp:  "example.com:8443/docs/guide",
		},
	}
	for _, tc := range validCases {
		got, err := NormalizeAndValidateCustomLink(tc.raw)
		if err != nil {
			t.Fatalf("NormalizeAndValidateCustomLink(%+v) unexpected error: %v", tc.raw, err)
		}
		if got.URL != tc.wantURL || got.Label != tc.wantLabel {
			t.Fatalf("NormalizeAndValidateCustomLink(%+v) = %+v, want url=%q label=%q", tc.raw, got, tc.wantURL, tc.wantLabel)
		}
		if disp := got.DisplayLabel(); disp != tc.wantDisp {
			t.Fatalf("DisplayLabel(%+v) = %q, want %q", got, disp, tc.wantDisp)
		}
	}

	rejectedURLs := []string{
		"",
		"   ",
		"javascript:alert(1)",
		"JAVASCRIPT:void(0)",
		"vbscript:msgbox(1)",
		"data:text/html,<script>alert(1)</script>",
		"file:///C:/Windows/System32/calc.exe",
		"file://localhost/etc/passwd",
		"mailto:test@example.com",
		"link3.cc/missing-scheme",
		"//link3.cc/protocol-relative",
		`\\server\share\path`,
		"https://",
		"http:///path-only",
		"https://user:secret@example.com/profile",
		"https://link3.cc/has space",
		"https://link3.cc/has\nnewline",
		"https://example.com/?SESSDATA=synth_cookie_val",
		"https://example.com/?auth_token=synth_x_tok",
		"https://example.com/?a_bogus=synth_sig_val",
		"https://example.com/" + strings.Repeat("a", MaxCustomLinkURLBytes),
	}
	for _, bad := range rejectedURLs {
		if _, err := ValidateCustomLinkURL(bad); err == nil {
			t.Fatalf("expected invalid URL %q to be rejected", bad)
		}
	}

	// Reject control characters or >48 runes in Label.
	if _, err := NormalizeAndValidateCustomLink(CustomLink{Label: "bad\nlabel", URL: "https://link3.cc/a"}); err == nil {
		t.Fatal("expected newline in label rejected")
	}
	if _, err := NormalizeAndValidateCustomLink(CustomLink{Label: strings.Repeat("字", MaxCustomLinkLabelRunes+1), URL: "https://link3.cc/a"}); err == nil {
		t.Fatal("expected >48 rune label rejected")
	}

	// Long path truncation in DeriveCustomLinkLabel stays bounded.
	longDerived := DeriveCustomLinkLabel("https://link3.cc/" + strings.Repeat("segment_", 10))
	if len([]rune(longDerived)) != MaxDerivedCustomLinkLabelRunes || !strings.HasSuffix(longDerived, "…") {
		t.Fatalf("unexpected clamped derived label %q (runes=%d)", longDerived, len([]rune(longDerived)))
	}

	// Cap >20 custom links rejected.
	tooMany := make([]CustomLink, MaxSubscriptionCustomLinks+1)
	for i := range tooMany {
		tooMany[i] = CustomLink{URL: "https://link3.cc/u" + string(rune('a'+i))}
	}
	if _, err := NormalizeAndValidateCustomLinks(tooMany); err == nil {
		t.Fatal("expected >20 custom links rejected")
	}
}

// DEC-074: Custom links are persisted as an optional backward-compatible field
// without bumping SubscriptionsSchemaVersion, isolated across clones, preserved
// across SQLite raw_json, disk reloads, legacy Upsert, Snapshot/ParseSnapshot/Hash,
// ReplaceFromSnapshot (remote sync), and ApplyImport (including MergeExisting).
func TestSubscriptionCustomLinksPersistenceSnapshotImportAndRollback(t *testing.T) {
	if SubscriptionsSchemaVersion != 5 {
		t.Fatalf("SubscriptionsSchemaVersion = %d, want 5", SubscriptionsSchemaVersion)
	}

	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	subsPath := filepath.Join(tmp, "subscriptions.json")
	mgr, err := NewManagerWithUpdatesDB(db, db, subsPath, nil, nil, nil, settings.Default(tmp).Subscription, nil)
	if err != nil {
		t.Fatal(err)
	}

	sub, err := mgr.Create("自定义链接订阅", "备注", []AccountRef{
		{Site: "bilibili", UID: "12345", Name: "UP主甲"},
	}, DefaultSubRules())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Empty links omit "links" key on disk.
	rawEmpty, err := os.ReadFile(subsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rawEmpty), `"links"`) {
		t.Fatalf("expected empty links omitted via omitempty, got:\n%s", string(rawEmpty))
	}

	links := []CustomLink{
		{URL: "https://link3.cc/creator_alpha"},
		{Label: "合集导航", URL: "https://linktr.ee/creator_alpha"},
		{Label: "合集导航覆盖名", URL: "https://link3.cc/creator_alpha"}, // duplicate URL deduplicated, fills empty label
	}
	updated, err := mgr.SetLinks(sub.ID, links)
	if err != nil {
		t.Fatalf("SetLinks: %v", err)
	}
	wantLinks := []CustomLink{
		{Label: "合集导航覆盖名", URL: "https://link3.cc/creator_alpha"},
		{Label: "合集导航", URL: "https://linktr.ee/creator_alpha"},
	}
	if !reflect.DeepEqual(updated.Links, wantLinks) {
		t.Fatalf("updated.Links = %+v, want %+v", updated.Links, wantLinks)
	}

	// Verify cloneSubscription isolates caller slice mutations.
	updated.Links[0].Label = "外部篡改"
	listed, err := mgr.List()
	if err != nil || len(listed) != 1 || !reflect.DeepEqual(listed[0].Links, wantLinks) {
		t.Fatalf("in-memory Links mutated through returned slice: %+v", listed)
	}

	// Disk write failure rolls back SQLite and in-memory Links completely.
	mgr.diskWriteHook = func() error { return errors.New("synthetic disk full") }
	if _, err := mgr.SetLinks(sub.ID, []CustomLink{{URL: "https://example.com/should-rollback"}}); err == nil {
		t.Fatal("expected SetLinks to fail on disk error")
	}
	mgr.diskWriteHook = nil
	listedAfterErr, _ := mgr.List()
	if !reflect.DeepEqual(listedAfterErr[0].Links, wantLinks) {
		t.Fatalf("failed SetLinks did not roll back Links: %+v", listedAfterErr[0].Links)
	}

	// Legacy Bilibili Mid-only Upsert without Links preserves existing custom links.
	afterLegacyUpsert, err := mgr.Upsert(Subscription{
		Site:  "bilibili",
		Mid:   12345,
		Uname: "UP主甲新名",
		Name:  "自定义链接订阅",
	})
	if err != nil {
		t.Fatalf("legacy Upsert: %v", err)
	}
	if !reflect.DeepEqual(afterLegacyUpsert.Links, wantLinks) {
		t.Fatalf("legacy Mid Upsert dropped Links: %+v", afterLegacyUpsert.Links)
	}

	// Reload from disk (including hand-injected unsafe javascript:/file: entries) sanitizes safely.
	var doc map[string]any
	rawDisk, err := os.ReadFile(subsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rawDisk, &doc); err != nil {
		t.Fatal(err)
	}
	subsArr := doc["subscriptions"].([]any)
	subMap := subsArr[0].(map[string]any)
	subMap["links"] = []map[string]any{
		{"url": "javascript:alert(1)"},
		{"url": "file:///C:/secret.txt"},
		{"url": "https://link3.cc/creator_alpha"},
		{"label": "个人站", "url": "https://creator.example.com"},
	}
	tamperedBytes, _ := json.MarshalIndent(doc, "", "  ")
	if err := os.WriteFile(subsPath, tamperedBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	mgrReloaded, err := NewManagerWithUpdatesDB(db, db, subsPath, nil, nil, nil, settings.Default(tmp).Subscription, nil)
	if err != nil {
		t.Fatalf("reload Manager: %v", err)
	}
	reloadedSubs, err := mgrReloaded.List()
	if err != nil || len(reloadedSubs) != 1 {
		t.Fatalf("List after reload: %+v err=%v", reloadedSubs, err)
	}
	wantSanitized := []CustomLink{
		{URL: "https://link3.cc/creator_alpha"},
		{Label: "个人站", URL: "https://creator.example.com"},
	}
	if !reflect.DeepEqual(reloadedSubs[0].Links, wantSanitized) {
		t.Fatalf("reloaded Links = %+v, want %+v", reloadedSubs[0].Links, wantSanitized)
	}

	// Snapshot, ParseSnapshot, Hash, ReplaceFromSnapshot (remote sync), and ApplyImport (MergeExisting).
	snap1, err := mgrReloaded.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	snapBytes, err := json.Marshal(snap1)
	if err != nil {
		t.Fatal(err)
	}
	parsedSnap, err := ParseSnapshot(snapBytes)
	if err != nil {
		t.Fatalf("ParseSnapshot: %v", err)
	}
	if !reflect.DeepEqual(parsedSnap.Subscriptions[0].Links, wantSanitized) {
		t.Fatalf("ParseSnapshot Links = %+v, want %+v", parsedSnap.Subscriptions[0].Links, wantSanitized)
	}
	hashBefore := parsedSnap.Hash()
	parsedSnap.Subscriptions[0].Links = append(parsedSnap.Subscriptions[0].Links, CustomLink{Label: "新增", URL: "https://linktr.ee/extra"})
	if parsedSnap.Hash() == hashBefore {
		t.Fatal("expected Snapshot.Hash() to change when subscription Links change")
	}

	// Remote ReplaceFromSnapshot preserves custom links.
	remTmp := t.TempDir()
	remDB, _, _, err := storage.OpenSQLite(filepath.Join(remTmp, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer remDB.Close()
	remMgr, err := NewManagerWithUpdatesDB(remDB, remDB, filepath.Join(remTmp, "subscriptions.json"), nil, nil, nil, settings.Default(remTmp).Subscription, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := remMgr.ReplaceFromSnapshot(parsedSnap); err != nil {
		t.Fatalf("ReplaceFromSnapshot: %v", err)
	}
	remSubs, _ := remMgr.List()
	if len(remSubs) != 1 || len(remSubs[0].Links) != 3 {
		t.Fatalf("remote synced Links = %+v, want 3 links", remSubs)
	}

	// ApplyImport with MergeExisting merges new custom links into existing subscription.
	impTmp := t.TempDir()
	impDB, _, _, err := storage.OpenSQLite(filepath.Join(impTmp, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer impDB.Close()
	impMgr, err := NewManagerWithUpdatesDB(impDB, impDB, filepath.Join(impTmp, "subscriptions.json"), nil, nil, nil, settings.Default(impTmp).Subscription, nil)
	if err != nil {
		t.Fatal(err)
	}
	localSub, err := impMgr.Create("本地已有", "", []AccountRef{{Site: "bilibili", UID: "12345", Name: "UP"}}, DefaultSubRules())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := impMgr.SetLinks(localSub.ID, []CustomLink{{Label: "本地名", URL: "https://link3.cc/creator_alpha"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := impMgr.ApplyImport(parsedSnap, ImportChoices{MergeExisting: true}); err != nil {
		t.Fatalf("ApplyImport(MergeExisting): %v", err)
	}
	mergedSubs, _ := impMgr.List()
	wantMerged := []CustomLink{
		{Label: "本地名", URL: "https://link3.cc/creator_alpha"},
		{Label: "个人站", URL: "https://creator.example.com"},
		{Label: "新增", URL: "https://linktr.ee/extra"},
	}
	if len(mergedSubs) != 1 || !reflect.DeepEqual(mergedSubs[0].Links, wantMerged) {
		t.Fatalf("merged Links = %+v, want %+v", mergedSubs, wantMerged)
	}

	// Clearing all links via SetLinks(id, nil) removes "links" cleanly.
	cleared, err := mgrReloaded.SetLinks(sub.ID, nil)
	if err != nil || len(cleared.Links) != 0 {
		t.Fatalf("SetLinks(nil) = %+v err=%v", cleared.Links, err)
	}
}
