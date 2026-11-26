package subscription

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
)

func openRuleTestManager(t *testing.T, tmp string) *Manager {
	t.Helper()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	lib, _ := library.Open(db, filepath.Join(tmp, "cache"))
	cred, _, _ := credential.Open(filepath.Join(tmp, "cred.dpapi"))
	client := bilibili.NewClient(bilibili.ClientOptions{CredStore: cred, PacingInterval: -1})
	mgr, err := NewManager(db, lib, nil, client, settings.Default(filepath.Join(tmp, "cache")).Subscription, nil)
	if err != nil {
		t.Fatal(err)
	}
	return mgr
}

func subByID(t *testing.T, m *Manager, id string) Subscription {
	t.Helper()
	subs, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range subs {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("subscription %s missing", id)
	return Subscription{}
}

// DEC-070: 1 capability tag = 1 cache capability, mapping 1-to-1 with SubRules
// in canonical order and rejecting unknown tag IDs.
func TestCapabilityTagsBijectionWithSubRules(t *testing.T) {
	all := AllCapabilityTags()
	if len(all) != 7 {
		t.Fatalf("expected 7 capability tags, got %d: %+v", len(all), all)
	}
	wantOrder := []string{
		TagScheduledCheck,
		TagCacheVideo,
		TagCacheDraw,
		TagCacheWord,
		TagCacheForward,
		TagFullCache,
		TagAutoRecord,
	}
	for i, id := range wantOrder {
		if all[i].ID != id || all[i].Name == "" || all[i].Description == "" {
			t.Fatalf("tag[%d] = %+v, want ID %q with name & description", i, all[i], id)
		}
	}

	// Empty tags <-> EmptySubRules(); DefaultSubRules() -> "完整存档" (DEC-080).
	if r := TagsToSubRules(nil); r.ScheduledCheck || r.AutoCache.Enabled || r.FullCache || r.AutoRecord {
		t.Fatalf("empty tags produced non-empty SubRules: %+v", r)
	}
	if got := SubRulesToTags(EmptySubRules()); len(got) != 0 {
		t.Fatalf("EmptySubRules produced tags %v, want empty", got)
	}
	if got := SubRulesToTags(DefaultSubRules()); !slices.Equal(got, DefaultNewSubTags()) {
		t.Fatalf("DefaultSubRules produced tags %v, want %v", got, DefaultNewSubTags())
	}
	if got := TagsSummary(nil); got != "无缓存标签" {
		t.Fatalf("TagsSummary(nil) = %q, want 无缓存标签", got)
	}

	// Out-of-order + duplicate input normalizes to canonical order and round-trips through SubRules.
	messy := []string{TagAutoRecord, TagCacheWord, TagCacheVideo, TagAutoRecord, TagFullCache, TagScheduledCheck}
	norm, err := ValidateTags(messy)
	if err != nil {
		t.Fatalf("ValidateTags(%v): %v", messy, err)
	}
	wantNorm := []string{TagScheduledCheck, TagCacheVideo, TagCacheWord, TagFullCache, TagAutoRecord}
	if !slices.Equal(norm, wantNorm) {
		t.Fatalf("NormalizeTags = %v, want %v", norm, wantNorm)
	}
	rules := TagsToSubRules(norm)
	if !rules.ScheduledCheck || !rules.AutoCache.Enabled || !slices.Equal(rules.AutoCache.Types, []string{"video", "word"}) || !rules.FullCache || !rules.AutoRecord {
		t.Fatalf("TagsToSubRules(%v) = %+v", norm, rules)
	}
	roundTrip := SubRulesToTags(rules)
	if !slices.Equal(roundTrip, wantNorm) {
		t.Fatalf("SubRulesToTags round-trip = %v, want %v", roundTrip, wantNorm)
	}

	// Every individual tag round-trips 1-to-1.
	for _, id := range wantOrder {
		single := SubRulesToTags(TagsToSubRules([]string{id}))
		if !slices.Equal(single, []string{id}) {
			t.Fatalf("single tag %q round-trip = %v", id, single)
		}
	}

	if _, err := ValidateTags([]string{TagCacheVideo, "not_a_tag"}); !errors.Is(err, ErrTagNotFound) {
		t.Fatalf("expected ErrTagNotFound for unknown tag, got %v", err)
	}
}

// DEC-080: newly created, user-page subscribed, split (MoveToNew), and imported
// subscriptions default to "完整存档" (scheduled_check + all 4 cache_* + full_cache),
// while EmptySubRules() stays empty and existing saved subscriptions with empty
// tags on disk are never altered retroactively on reload.
func TestNewSubscriptionsDefaultToFullArchiveAndExistingEmptyPreserved(t *testing.T) {
	tmp := t.TempDir()
	m := openRuleTestManager(t, tmp)

	cSub, err := m.Create("新建订阅", "", []AccountRef{
		{Site: "bilibili", UID: "3001"},
		{Site: "bilibili", UID: "3004"},
	}, DefaultSubRules())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.MoveToNew("bilibili", "3004", "拆分订阅", "", DefaultSubRules()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Upsert(Subscription{Site: "bilibili", Mid: 3002, Name: "用户页订阅", Scope: "both", AutoCache: "inherit", Rules: DefaultSubRules()}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ImportAccountRefs([]AccountRef{{Site: "weibo", UID: "500000009", Name: "微博导入"}}, DefaultSubRules()); err != nil {
		t.Fatal(err)
	}
	emptySub, err := m.Create("仅手动订阅", "", []AccountRef{{Site: "bilibili", UID: "3005"}}, EmptySubRules())
	if err != nil {
		t.Fatal(err)
	}
	_ = cSub

	subs, err := m.List()
	if err != nil || len(subs) != 5 {
		t.Fatalf("want 5 subscriptions, got %d (err=%v)", len(subs), err)
	}
	wantDefault := DefaultNewSubTags()
	for _, s := range subs {
		if s.ID == emptySub.ID {
			if len(s.Tags) != 0 || s.Enabled || s.Rules.ScheduledCheck || s.Rules.AutoCache.Enabled || s.Rules.FullCache || s.Rules.AutoRecord || s.LiveAutoRecord {
				t.Fatalf("empty subscription %s has non-empty capabilities: tags=%v rules=%+v", s.Name, s.Tags, s.Rules)
			}
			continue
		}
		if !slices.Equal(s.Tags, wantDefault) || !s.Enabled || !s.Rules.ScheduledCheck || !s.Rules.AutoCache.Enabled || s.Rules.AutoCache.Since == "" || !s.Rules.FullCache || s.Rules.AutoRecord || s.LiveAutoRecord {
			t.Fatalf("new subscription %s did not default to 完整存档: tags=%v rules=%+v enabled=%v rec=%v",
				s.Name, s.Tags, s.Rules, s.Enabled, s.LiveAutoRecord)
		}
	}

	raw, err := os.ReadFile(filepath.Join(tmp, "subscriptions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"tags": []`) || strings.Contains(string(raw), `"cacheRules"`) || strings.Contains(string(raw), `"ruleId"`) {
		t.Fatalf("unexpected v5 subscriptions.json payload:\n%s", raw)
	}

	// Reloading from disk MUST preserve existing empty-tag subscriptions untouched.
	reloaded := openRuleTestManager(t, tmp)
	gotEmpty := subByID(t, reloaded, emptySub.ID)
	if len(gotEmpty.Tags) != 0 || gotEmpty.Rules.ScheduledCheck || gotEmpty.Rules.AutoCache.Enabled || gotEmpty.Rules.FullCache {
		t.Fatalf("existing empty subscription was altered retroactively on reload: tags=%v rules=%+v", gotEmpty.Tags, gotEmpty.Rules)
	}
}

// SetSubscriptionTags toggles atomic capability tags on a single subscription,
// sets AutoCache.Since when transitioning 0 -> >=1 cache_* tags, preserves
// AutoCache.Since while >=1 cache_* tags remain active, clears AutoCache.Since
// when all cache_* tags are removed, and rolls back on errors.
func TestSetSubscriptionTagsBaselineSinceAndRollback(t *testing.T) {
	tmp := t.TempDir()
	m := openRuleTestManager(t, tmp)

	t0 := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	t1 := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 11, 10, 11, 0, 0, 0, time.UTC)
	m.SetNowFunc(func() time.Time { return t0 })

	sub, err := m.Create("单订阅标签", "", []AccountRef{{Site: "bilibili", UID: "2001"}}, DefaultSubRules())
	if err != nil {
		t.Fatal(err)
	}

	// Non-cache_* tags do not arm AutoCache.Since.
	updated, err := m.SetSubscriptionTags(sub.ID, []string{TagScheduledCheck, TagAutoRecord, TagFullCache})
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Enabled || !updated.LiveAutoRecord || !updated.Rules.FullCache || updated.Rules.AutoCache.Enabled || updated.Rules.AutoCache.Since != "" {
		t.Fatalf("non-cache tags armed AutoCache unexpectedly: %+v", updated)
	}

	// Enabling first cache_* tag at t0 sets AutoCache.Since = t0.
	updated, err = m.SetSubscriptionTags(sub.ID, []string{TagScheduledCheck, TagCacheVideo})
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Rules.AutoCache.Enabled || !slices.Equal(updated.Rules.AutoCache.Types, []string{"video"}) || updated.Rules.AutoCache.Since != t0.Format(time.RFC3339) {
		t.Fatalf("expected AutoCache.Since=%s on first cache_* tag, got %+v", t0.Format(time.RFC3339), updated.Rules)
	}

	// Adding cache_draw and toggling scheduled_check off at t1 preserves AutoCache.Since = t0.
	m.SetNowFunc(func() time.Time { return t1 })
	updated, err = m.SetSubscriptionTags(sub.ID, []string{TagCacheVideo, TagCacheDraw})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Enabled || !slices.Equal(updated.Rules.AutoCache.Types, []string{"video", "draw"}) || updated.Rules.AutoCache.Since != t0.Format(time.RFC3339) {
		t.Fatalf("expected preserved Since=%s when adding second cache_* tag, got enabled=%v rules=%+v", t0.Format(time.RFC3339), updated.Enabled, updated.Rules)
	}

	// Removing cache_video while keeping cache_draw at t2 still preserves AutoCache.Since = t0.
	m.SetNowFunc(func() time.Time { return t2 })
	updated, err = m.SetSubscriptionTags(sub.ID, []string{TagCacheDraw})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(updated.Rules.AutoCache.Types, []string{"draw"}) || updated.Rules.AutoCache.Since != t0.Format(time.RFC3339) {
		t.Fatalf("expected preserved Since=%s when switching cache_* subset, got %+v", t0.Format(time.RFC3339), updated.Rules)
	}

	// Removing all cache_* tags clears AutoCache.Since; re-enabling at t3 sets new Since = t3.
	updated, err = m.SetSubscriptionTags(sub.ID, []string{TagScheduledCheck})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Rules.AutoCache.Enabled || updated.Rules.AutoCache.Since != "" {
		t.Fatalf("expected cleared AutoCache.Since when 0 cache_* tags remain, got %+v", updated.Rules)
	}
	m.SetNowFunc(func() time.Time { return t3 })
	updated, err = m.SetSubscriptionTags(sub.ID, []string{TagScheduledCheck, TagCacheForward})
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Rules.AutoCache.Enabled || updated.Rules.AutoCache.Since != t3.Format(time.RFC3339) {
		t.Fatalf("expected fresh Since=%s after re-enabling cache_* tag, got %+v", t3.Format(time.RFC3339), updated.Rules)
	}

	// Validation & disk failure rollback.
	if _, err := m.SetSubscriptionTags(sub.ID, []string{"bad_tag"}); !errors.Is(err, ErrTagNotFound) {
		t.Fatalf("expected ErrTagNotFound, got %v", err)
	}
	subsPath := filepath.Join(tmp, "subscriptions.json")
	beforeFail, _ := os.ReadFile(subsPath)
	m.diskWriteHook = func() error { return errors.New("synthetic disk full") }
	if _, err := m.SetSubscriptionTags(sub.ID, []string{}); err == nil {
		t.Fatal("expected disk failure")
	}
	m.diskWriteHook = nil
	afterFail, _ := os.ReadFile(subsPath)
	if !bytes.Equal(beforeFail, afterFail) || !HasTag(subByID(t, m, sub.ID).Tags, TagCacheForward) {
		t.Fatalf("disk failure did not roll back SetSubscriptionTags")
	}
}

// SetTagSubscriptions batch-manages which subscriptions hold a single capability
// tag while leaving every subscription's other tags intact, writing disk once,
// and rolling back on validation or write errors.
func TestSetTagSubscriptionsPerTagBatchAtomicAndRollback(t *testing.T) {
	tmp := t.TempDir()
	m := openRuleTestManager(t, tmp)

	t0 := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	t1 := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	m.SetNowFunc(func() time.Time { return t0 })

	s1, _ := m.Create("Sub1", "", []AccountRef{{Site: "bilibili", UID: "3001"}}, DefaultSubRules())
	s2, _ := m.Create("Sub2", "", []AccountRef{{Site: "bilibili", UID: "3002"}}, DefaultSubRules())
	s3, _ := m.Create("Sub3", "", []AccountRef{{Site: "bilibili", UID: "3003"}}, DefaultSubRules())
	s4, _ := m.Create("Sub4", "", []AccountRef{{Site: "bilibili", UID: "3004"}}, DefaultSubRules())
	s5, _ := m.Create("Sub5", "", []AccountRef{{Site: "bilibili", UID: "3005"}}, DefaultSubRules())

	// Seed initial tags at t0:
	// s1: [scheduled_check] (no cache_*)
	// s2: [scheduled_check, cache_video] (Since = t0)
	// s3: [scheduled_check, cache_video] (Since = t0)
	// s4: [scheduled_check, cache_draw, auto_record] (Since = t0)
	// s5: [auto_record] (untouched)
	_, _ = m.SetSubscriptionTags(s1.ID, []string{TagScheduledCheck})
	_, _ = m.SetSubscriptionTags(s2.ID, []string{TagScheduledCheck, TagCacheVideo})
	_, _ = m.SetSubscriptionTags(s3.ID, []string{TagScheduledCheck, TagCacheVideo})
	_, _ = m.SetSubscriptionTags(s4.ID, []string{TagScheduledCheck, TagCacheDraw, TagAutoRecord})
	_, _ = m.SetSubscriptionTags(s5.ID, []string{TagAutoRecord})

	subsPath := filepath.Join(tmp, "subscriptions.json")
	rawBefore, err := os.ReadFile(subsPath)
	if err != nil {
		t.Fatal(err)
	}

	writes := 0
	var hookErr error
	m.diskWriteHook = func() error {
		writes++
		return hookErr
	}
	m.SetNowFunc(func() time.Time { return t1 })

	// Assign TagCacheVideo to {s1, s2, s4}; unassign from s3; leave s5 untouched.
	if err := m.SetTagSubscriptions(TagCacheVideo, []string{s1.ID, s2.ID, s4.ID}); err != nil {
		t.Fatalf("SetTagSubscriptions: %v", err)
	}
	if writes != 1 {
		t.Fatalf("expected 1 atomic disk write, got %d", writes)
	}
	bakAfter, err := os.ReadFile(subsPath + ".bak")
	if err != nil || !bytes.Equal(bakAfter, rawBefore) {
		t.Fatalf("expected subscriptions.json.bak to match pre-batch snapshot, err=%v", err)
	}

	got1 := subByID(t, m, s1.ID)
	got2 := subByID(t, m, s2.ID)
	got3 := subByID(t, m, s3.ID)
	got4 := subByID(t, m, s4.ID)
	got5 := subByID(t, m, s5.ID)

	// s1 gained cache_video while keeping scheduled_check; Since = t1.
	if !slices.Equal(got1.Tags, []string{TagScheduledCheck, TagCacheVideo}) || got1.Rules.AutoCache.Since != t1.Format(time.RFC3339) {
		t.Fatalf("s1 mismatch: tags=%v rules=%+v", got1.Tags, got1.Rules)
	}
	// s2 kept cache_video; Since preserved at t0.
	if !slices.Equal(got2.Tags, []string{TagScheduledCheck, TagCacheVideo}) || got2.Rules.AutoCache.Since != t0.Format(time.RFC3339) {
		t.Fatalf("s2 mismatch: tags=%v rules=%+v", got2.Tags, got2.Rules)
	}
	// s3 lost cache_video BUT kept scheduled_check; Since cleared because 0 cache_* remain.
	if !slices.Equal(got3.Tags, []string{TagScheduledCheck}) || got3.Rules.AutoCache.Enabled || got3.Rules.AutoCache.Since != "" {
		t.Fatalf("s3 mismatch: tags=%v rules=%+v", got3.Tags, got3.Rules)
	}
	// s4 gained cache_video alongside cache_draw + auto_record; Since preserved at t0!
	if !slices.Equal(got4.Tags, []string{TagScheduledCheck, TagCacheVideo, TagCacheDraw, TagAutoRecord}) || !got4.LiveAutoRecord || got4.Rules.AutoCache.Since != t0.Format(time.RFC3339) {
		t.Fatalf("s4 mismatch: tags=%v rec=%v rules=%+v", got4.Tags, got4.LiveAutoRecord, got4.Rules)
	}
	// s5 untouched.
	if !slices.Equal(got5.Tags, []string{TagAutoRecord}) || !got5.LiveAutoRecord {
		t.Fatalf("s5 mismatch: tags=%v rec=%v", got5.Tags, got5.LiveAutoRecord)
	}

	// Reload from disk verifies persistence.
	reloaded := openRuleTestManager(t, tmp)
	if !slices.Equal(subByID(t, reloaded, s4.ID).Tags, got4.Tags) || !slices.Equal(subByID(t, reloaded, s3.ID).Tags, got3.Tags) {
		t.Fatalf("reloaded tags mismatch")
	}

	// Validation & disk failure rollback.
	if err := m.SetTagSubscriptions("invalid_tag", []string{s1.ID}); !errors.Is(err, ErrTagNotFound) {
		t.Fatalf("expected ErrTagNotFound, got %v", err)
	}
	if err := m.SetTagSubscriptions(TagAutoRecord, []string{s1.ID, "missing_sub"}); err == nil {
		t.Fatal("expected unknown subID error")
	}
	if writes != 1 {
		t.Fatalf("validation error wrote to disk: writes=%d", writes)
	}

	snapBeforeFail, _ := os.ReadFile(subsPath)
	hookErr = errors.New("simulated disk full")
	if err := m.SetTagSubscriptions(TagAutoRecord, []string{s1.ID}); err == nil {
		t.Fatal("expected disk failure")
	}
	hookErr = nil
	snapAfterFail, _ := os.ReadFile(subsPath)
	if !bytes.Equal(snapBeforeFail, snapAfterFail) || HasTag(subByID(t, m, s1.ID).Tags, TagAutoRecord) || !HasTag(subByID(t, m, s5.ID).Tags, TagAutoRecord) {
		t.Fatalf("disk failure did not roll back SetTagSubscriptions")
	}
}

// BatchUpdateSubscriptionsTags supports batch-adding, batch-removing, and
// batch-replacing (including clearing) multiple capability tags across multiple
// subscriptions in a single atomic write.
func TestBatchUpdateSubscriptionsTagsAddRemoveReplaceAndRollback(t *testing.T) {
	tmp := t.TempDir()
	m := openRuleTestManager(t, tmp)

	t0 := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	t1 := time.Date(2026, 11, 10, 11, 0, 0, 0, time.UTC)
	m.SetNowFunc(func() time.Time { return t0 })

	s1, _ := m.Create("Alpha", "", []AccountRef{{Site: "bilibili", UID: "4101"}}, EmptySubRules())
	s2, _ := m.Create("Beta", "", []AccountRef{{Site: "bilibili", UID: "4102"}}, EmptySubRules())
	s3, _ := m.Create("Gamma", "", []AccountRef{{Site: "bilibili", UID: "4103"}}, EmptySubRules())

	_, _ = m.SetSubscriptionTags(s2.ID, []string{TagCacheVideo, TagAutoRecord})
	_, _ = m.SetSubscriptionTags(s3.ID, []string{TagFullCache})

	writes := 0
	var hookErr error
	m.diskWriteHook = func() error {
		writes++
		return hookErr
	}
	m.SetNowFunc(func() time.Time { return t1 })

	// 1. Batch add [scheduled_check, cache_draw] to s1 and s2 (s3 untouched).
	if err := m.BatchUpdateSubscriptionsTags([]string{s1.ID, s2.ID}, TagBatchAdd, []string{TagScheduledCheck, TagCacheDraw}); err != nil {
		t.Fatalf("BatchUpdateSubscriptionsTags(add): %v", err)
	}
	if writes != 1 {
		t.Fatalf("expected 1 write on batch add, got %d", writes)
	}
	if g1 := subByID(t, m, s1.ID); !slices.Equal(g1.Tags, []string{TagScheduledCheck, TagCacheDraw}) || g1.Rules.AutoCache.Since != t1.Format(time.RFC3339) {
		t.Fatalf("s1 after add: tags=%v rules=%+v", g1.Tags, g1.Rules)
	}
	if g2 := subByID(t, m, s2.ID); !slices.Equal(g2.Tags, []string{TagScheduledCheck, TagCacheVideo, TagCacheDraw, TagAutoRecord}) || g2.Rules.AutoCache.Since != t0.Format(time.RFC3339) {
		t.Fatalf("s2 after add: tags=%v rules=%+v", g2.Tags, g2.Rules)
	}
	if g3 := subByID(t, m, s3.ID); !slices.Equal(g3.Tags, []string{TagFullCache}) {
		t.Fatalf("s3 should be untouched after add: %v", g3.Tags)
	}

	// 2. Batch remove [cache_draw, auto_record] from s1 and s2.
	if err := m.BatchUpdateSubscriptionsTags([]string{s1.ID, s2.ID}, TagBatchRemove, []string{TagCacheDraw, TagAutoRecord}); err != nil {
		t.Fatalf("BatchUpdateSubscriptionsTags(remove): %v", err)
	}
	if writes != 2 {
		t.Fatalf("expected 2 writes total, got %d", writes)
	}
	if g1 := subByID(t, m, s1.ID); !slices.Equal(g1.Tags, []string{TagScheduledCheck}) || g1.Rules.AutoCache.Enabled || g1.Rules.AutoCache.Since != "" {
		t.Fatalf("s1 after remove: tags=%v rules=%+v", g1.Tags, g1.Rules)
	}
	if g2 := subByID(t, m, s2.ID); !slices.Equal(g2.Tags, []string{TagScheduledCheck, TagCacheVideo}) || g2.LiveAutoRecord || g2.Rules.AutoCache.Since != t0.Format(time.RFC3339) {
		t.Fatalf("s2 after remove: tags=%v rec=%v rules=%+v", g2.Tags, g2.LiveAutoRecord, g2.Rules)
	}

	// 3. Batch replace s1 and s2 with empty tags (clears all capabilities on selected subs; s3 still has full_cache).
	if err := m.BatchUpdateSubscriptionsTags([]string{s1.ID, s2.ID}, TagBatchReplace, []string{}); err != nil {
		t.Fatalf("BatchUpdateSubscriptionsTags(replace empty): %v", err)
	}
	if len(subByID(t, m, s1.ID).Tags) != 0 || len(subByID(t, m, s2.ID).Tags) != 0 || !slices.Equal(subByID(t, m, s3.ID).Tags, []string{TagFullCache}) {
		t.Fatalf("unexpected tags after replace-empty: s1=%v s2=%v s3=%v", subByID(t, m, s1.ID).Tags, subByID(t, m, s2.ID).Tags, subByID(t, m, s3.ID).Tags)
	}

	// 4. Validation errors and disk failure rollback.
	if err := m.BatchUpdateSubscriptionsTags([]string{s1.ID}, TagBatchAdd, nil); err == nil {
		t.Fatal("expected error when adding 0 tags")
	}
	if err := m.BatchUpdateSubscriptionsTags([]string{s1.ID}, "bogus_mode", []string{TagScheduledCheck}); err == nil {
		t.Fatal("expected error for unknown batch mode")
	}
	if err := m.BatchUpdateSubscriptionsTags([]string{s1.ID}, TagBatchAdd, []string{"unknown_tag"}); !errors.Is(err, ErrTagNotFound) {
		t.Fatalf("expected ErrTagNotFound, got %v", err)
	}
	hookErr = errors.New("disk write fail")
	if err := m.BatchUpdateSubscriptionsTags([]string{s1.ID}, TagBatchReplace, []string{TagScheduledCheck}); err == nil {
		t.Fatal("expected disk write failure")
	}
	hookErr = nil
	if len(subByID(t, m, s1.ID).Tags) != 0 {
		t.Fatalf("failed BatchUpdateSubscriptionsTags mutated s1: %v", subByID(t, m, s1.ID).Tags)
	}
}

// Existing v1..v4 subscriptions.json files migrate losslessly to v5 capability
// tags, creating .premigrate-<oldVersion>, pruning stale backups, preserving
// original v4 file on mid-migration failure, staying idempotent on reload, and
// rejecting v5 files in simulated v4 builds with NewerDataError.
func TestV1ToV4MigrationsPreserveAllCapabilitiesAndBackupV4(t *testing.T) {
	// Case A: Pre-shared-rules v1 file (no cacheRules, no ruleId; uses SubRules + legacy AutoCache scope).
	v1Dir := t.TempDir()
	v1SubsPath := filepath.Join(v1Dir, "subscriptions.json")
	v1Raw := `{
  "version": 1,
  "subscriptions": [
    {
      "id": "sub_v1_rules",
      "name": "V1RulesCreator",
      "createdAt": "2026-11-01T08:00:00Z",
      "accounts": [{"site": "bilibili", "uid": "1001", "name": "UP1001"}],
      "rules": {
        "scheduledCheck": true,
        "autoCache": {"enabled": true, "types": ["video", "word"], "since": "2026-11-01T08:00:00Z"},
        "autoRecord": true
      }
    },
    {
      "id": "sub_v1_legacy_scope",
      "name": "V1LegacyScope",
      "createdAt": "2026-11-01T09:00:00Z",
      "enabled": true,
      "autoCache": "video",
      "accounts": [{"site": "bilibili", "uid": "1002", "name": "UP1002"}]
    }
  ]
}`
	if err := os.WriteFile(v1SubsPath, []byte(v1Raw), 0o600); err != nil {
		t.Fatal(err)
	}
	v1DB, _, _, err := storage.OpenSQLite(filepath.Join(v1Dir, "updates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer v1DB.Close()
	mV1, err := NewManagerWithUpdatesDB(v1DB, v1DB, v1SubsPath, nil, nil, nil, settings.Default(v1Dir).Subscription, nil)
	if err != nil {
		t.Fatalf("migrate v1->v5: %v", err)
	}
	if _, err := os.Stat(v1SubsPath + ".premigrate-1"); err != nil {
		t.Fatalf("missing .premigrate-1 backup: %v", err)
	}
	gotV1A := subByID(t, mV1, "sub_v1_rules")
	if !slices.Equal(gotV1A.Tags, []string{TagScheduledCheck, TagCacheVideo, TagCacheWord, TagAutoRecord}) || gotV1A.Rules.AutoCache.Since != "2026-11-01T08:00:00Z" {
		t.Fatalf("v1 rules sub lost capabilities: tags=%v rules=%+v", gotV1A.Tags, gotV1A.Rules)
	}
	gotV1B := subByID(t, mV1, "sub_v1_legacy_scope")
	if !slices.Equal(gotV1B.Tags, []string{TagScheduledCheck, TagCacheVideo}) || gotV1B.Rules.AutoCache.Since != "1970-01-01T00:00:00Z" {
		t.Fatalf("v1 legacy scope sub lost capabilities: tags=%v rules=%+v", gotV1B.Tags, gotV1B.Rules)
	}

	// Case B: v3 file with an edited "default" rule (DEC-040..DEC-054 era) preserves all active capabilities.
	v3Dir := t.TempDir()
	v3SubsPath := filepath.Join(v3Dir, "subscriptions.json")
	v3Raw := `{
  "version": 3,
  "subscriptions": [
    {
      "id": "sub_v3_edited_default",
      "name": "EditedDefaultSub",
      "createdAt": "2026-11-05T10:00:00Z",
      "ruleId": "default",
      "accounts": [{"site": "weibo", "uid": "500000001", "name": "WB1"}]
    }
  ],
  "cacheRules": [
    {
      "id": "default",
      "name": "自定义默认",
      "scheduledCheck": true,
      "autoCache": true,
      "types": ["video", "draw", "word", "forward"],
      "autoRecord": true
    }
  ]
}`
	if err := os.WriteFile(v3SubsPath, []byte(v3Raw), 0o600); err != nil {
		t.Fatal(err)
	}
	v3DB, _, _, err := storage.OpenSQLite(filepath.Join(v3Dir, "updates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer v3DB.Close()
	mV3, err := NewManagerWithUpdatesDB(v3DB, v3DB, v3SubsPath, nil, nil, nil, settings.Default(v3Dir).Subscription, nil)
	if err != nil {
		t.Fatalf("migrate v3->v5: %v", err)
	}
	gotV3 := subByID(t, mV3, "sub_v3_edited_default")
	wantEditedTags := []string{TagScheduledCheck, TagCacheVideo, TagCacheDraw, TagCacheWord, TagCacheForward, TagAutoRecord}
	if !slices.Equal(gotV3.Tags, wantEditedTags) || !gotV3.Enabled || !gotV3.LiveAutoRecord || !gotV3.Rules.AutoCache.Enabled {
		t.Fatalf("v3 edited default lost capabilities: tags=%v rules=%+v", gotV3.Tags, gotV3.Rules)
	}

	// Case C: v4 -> v5 migration with stale .premigrate-3 pruning, mid-migration failure safety, idempotency, and v4 downgrade guard.
	v4Dir := t.TempDir()
	v4SubsPath := filepath.Join(v4Dir, "subscriptions.json")
	staleBak := v4SubsPath + ".premigrate-3"
	if err := os.WriteFile(staleBak, []byte(`{"version":3}`), 0o600); err != nil {
		t.Fatal(err)
	}
	v4Raw := `{
  "version": 4,
  "subscriptions": [
    {
      "id": "sub_v4_default_off",
      "name": "AllOffSub",
      "createdAt": "2026-11-15T08:00:00Z",
      "ruleId": "default",
      "accounts": [{"site": "x", "uid": "180000000000000001", "uniqueId": "synth_x_1", "name": "X1"}],
      "rules": {"scheduledCheck": false, "autoCache": {"enabled": false}}
    },
    {
      "id": "sub_v4_custom_media",
      "name": "MediaSub",
      "createdAt": "2026-11-15T09:00:00Z",
      "ruleId": "rule_media",
      "accounts": [{"site": "bilibili", "uid": "2001", "name": "Bili2001"}],
      "rules": {
        "scheduledCheck": true,
        "autoCache": {"enabled": true, "types": ["video", "draw"], "since": "2026-11-16T00:00:00Z"},
        "fullCache": true,
        "autoRecord": true
      }
    }
  ],
  "cacheRules": [
    {"id": "default", "name": "默认"},
    {
      "id": "rule_media",
      "name": "全能归档",
      "scheduledCheck": true,
      "autoCache": true,
      "types": ["video", "draw"],
      "fullCache": true,
      "autoRecord": true
    }
  ]
}`
	if err := os.WriteFile(v4SubsPath, []byte(v4Raw), 0o600); err != nil {
		t.Fatal(err)
	}
	v4DB, _, _, err := storage.OpenSQLite(filepath.Join(v4Dir, "updates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer v4DB.Close()

	// Simulated mid-migration failure before atomic rename leaves v4 file untouched on disk.
	restore := storage.SetBeforeMigrationRenameHookForTest(func(targetPath, tmpPath string) error {
		if targetPath == v4SubsPath {
			return errors.New("simulated v4->v5 rename error")
		}
		return nil
	})
	_, failErr := NewManagerWithUpdatesDB(v4DB, v4DB, v4SubsPath, nil, nil, nil, settings.Default(v4Dir).Subscription, nil)
	restore()
	if failErr == nil {
		t.Fatal("expected mid-migration failure")
	}
	if diskStillV4, _ := os.ReadFile(v4SubsPath); string(diskStillV4) != v4Raw {
		t.Fatalf("mid-migration failure altered v4 subscriptions.json")
	}

	// Clean v4 -> v5 migration succeeds, creates .premigrate-4, and prunes .premigrate-3.
	mV4, err := NewManagerWithUpdatesDB(v4DB, v4DB, v4SubsPath, nil, nil, nil, settings.Default(v4Dir).Subscription, nil)
	if err != nil {
		t.Fatalf("clean v4->v5 migration: %v", err)
	}
	if _, err := os.Stat(staleBak); !os.IsNotExist(err) {
		t.Fatalf("expected stale .premigrate-3 pruned when creating .premigrate-4")
	}
	bakV4Bytes, err := os.ReadFile(v4SubsPath + ".premigrate-4")
	if err != nil || string(bakV4Bytes) != v4Raw {
		t.Fatalf(".premigrate-4 mismatch: err=%v", err)
	}

	gotOff := subByID(t, mV4, "sub_v4_default_off")
	if len(gotOff.Tags) != 0 || gotOff.Enabled || gotOff.Rules.AutoCache.Enabled || gotOff.Rules.FullCache || gotOff.LiveAutoRecord {
		t.Fatalf("v4 default-off sub gained capabilities: tags=%v rules=%+v", gotOff.Tags, gotOff.Rules)
	}
	gotMedia := subByID(t, mV4, "sub_v4_custom_media")
	wantMediaTags := []string{TagScheduledCheck, TagCacheVideo, TagCacheDraw, TagFullCache, TagAutoRecord}
	if !slices.Equal(gotMedia.Tags, wantMediaTags) || gotMedia.Rules.AutoCache.Since != "2026-11-16T00:00:00Z" || !gotMedia.Rules.FullCache || !gotMedia.LiveAutoRecord {
		t.Fatalf("v4 custom media sub lost capabilities: tags=%v rules=%+v", gotMedia.Tags, gotMedia.Rules)
	}

	v5DiskBytes, err := os.ReadFile(v4SubsPath)
	if err != nil {
		t.Fatal(err)
	}
	var diskDoc subscriptionsFile
	if err := json.Unmarshal(v5DiskBytes, &diskDoc); err != nil || diskDoc.Version != 5 {
		t.Fatalf("expected disk version 5, got %+v err=%v", diskDoc, err)
	}
	if strings.Contains(string(v5DiskBytes), `"cacheRules"`) || strings.Contains(string(v5DiskBytes), `"ruleId"`) {
		t.Fatalf("v5 subscriptions.json still contains legacy rule keys:\n%s", v5DiskBytes)
	}

	// Idempotent second startup leaves v5 file and .premigrate-4 untouched.
	mV4Reload, err := NewManagerWithUpdatesDB(v4DB, v4DB, v4SubsPath, nil, nil, nil, settings.Default(v4Dir).Subscription, nil)
	if err != nil {
		t.Fatalf("idempotent v5 reload: %v", err)
	}
	if !slices.Equal(subByID(t, mV4Reload, "sub_v4_custom_media").Tags, wantMediaTags) {
		t.Fatalf("idempotent reload changed tags")
	}

	// Simulated v4 build refuses v5 subscriptions.json with NewerDataError and zero disk writes.
	oldV4Mgr := &Manager{
		db:               v4DB,
		updatesDB:        v4DB,
		subsPath:         v4SubsPath,
		maxSchemaVersion: 4,
		nowFunc:          time.Now,
	}
	oldV4Mgr.mu.Lock()
	downErr := oldV4Mgr.loadSubscriptionsFromDiskLocked()
	oldV4Mgr.mu.Unlock()
	if !storage.IsNewerData(downErr) {
		t.Fatalf("expected NewerDataError from v4 manager on v5 file, got %v", downErr)
	}
	afterDownBytes, _ := os.ReadFile(v4SubsPath)
	if !bytes.Equal(v5DiskBytes, afterDownBytes) {
		t.Fatalf("v4 downgrade check mutated v5 subscriptions.json")
	}
}

func TestTrackingPresetsRoundTripAndMatching(t *testing.T) {
	presets := AllTrackingPresets()
	wantOrder := []string{PresetFullArchive, PresetFollowCache, PresetCheckOnly, PresetManual}
	if len(presets) != len(wantOrder) {
		t.Fatalf("preset count=%d want %d", len(presets), len(wantOrder))
	}
	for i, wantID := range wantOrder {
		if presets[i].ID != wantID {
			t.Fatalf("preset[%d].ID=%q want %q", i, presets[i].ID, wantID)
		}
		if _, ok := TrackingPresetByID(wantID); !ok {
			t.Fatalf("TrackingPresetByID(%q) returned false", wantID)
		}
	}

	if !slices.Equal(DefaultNewSubTags(), presets[0].Tags) {
		t.Fatalf("DefaultNewSubTags()=%v want full_archive %v", DefaultNewSubTags(), presets[0].Tags)
	}
	if !slices.Equal(SubRulesToTags(DefaultSubRules()), DefaultNewSubTags()) {
		t.Fatalf("DefaultSubRules() tags=%v want %v", SubRulesToTags(DefaultSubRules()), DefaultNewSubTags())
	}
	if got := SubRulesToTags(EmptySubRules()); len(got) != 0 {
		t.Fatalf("EmptySubRules() tags=%v want empty", got)
	}

	for _, p := range presets {
		for _, autoRec := range []bool{false, true} {
			tags, err := PresetTags(p.ID, autoRec)
			if err != nil {
				t.Fatalf("PresetTags(%q, %v): %v", p.ID, autoRec, err)
			}
			gotID, gotRec := MatchTrackingPreset(tags)
			if gotID != p.ID || gotRec != autoRec {
				t.Fatalf("MatchTrackingPreset(%v) = (%q, %v), want (%q, %v)", tags, gotID, gotRec, p.ID, autoRec)
			}
			rules, err := PresetSubRules(p.ID, autoRec)
			if err != nil {
				t.Fatalf("PresetSubRules(%q, %v): %v", p.ID, autoRec, err)
			}
			if !slices.Equal(SubRulesToTags(rules), tags) {
				t.Fatalf("PresetSubRules round-trip mismatch: got %v want %v", SubRulesToTags(rules), tags)
			}
			label := TrackingModeLabel(tags)
			if autoRec && !strings.HasSuffix(label, " + 录直播") {
				t.Fatalf("TrackingModeLabel(%v) = %q, want suffix + 录直播", tags, label)
			}
			if !autoRec && label != p.Name {
				t.Fatalf("TrackingModeLabel(%v) = %q, want %q", tags, label, p.Name)
			}
		}
	}

	// Custom combinations match PresetCustom while preserving autoRecord flag.
	customCases := []struct {
		tags      []string
		wantRec   bool
		wantLabel string
		wantGap   bool
	}{
		{[]string{TagScheduledCheck, TagCacheVideo}, false, "自定义", false},
		{[]string{TagScheduledCheck, TagCacheVideo, TagAutoRecord}, true, "自定义 + 录直播", false},
		{[]string{TagCacheVideo}, false, "自定义", true},
		{[]string{TagCacheDraw, TagFullCache, TagAutoRecord}, true, "自定义 + 录直播", true},
		{[]string{TagFullCache}, false, "自定义", false},
	}
	for _, tc := range customCases {
		gotID, gotRec := MatchTrackingPreset(tc.tags)
		if gotID != PresetCustom || gotRec != tc.wantRec {
			t.Fatalf("MatchTrackingPreset(%v) = (%q, %v), want (%q, %v)", tc.tags, gotID, gotRec, PresetCustom, tc.wantRec)
		}
		if gotLabel := TrackingModeLabel(tc.tags); gotLabel != tc.wantLabel {
			t.Fatalf("TrackingModeLabel(%v) = %q, want %q", tc.tags, gotLabel, tc.wantLabel)
		}
		if gotGap := HasAutoCacheWithoutScheduledCheck(tc.tags); gotGap != tc.wantGap {
			t.Fatalf("HasAutoCacheWithoutScheduledCheck(%v) = %v, want %v", tc.tags, gotGap, tc.wantGap)
		}
	}

	if _, err := PresetTags("unknown_preset", false); !errors.Is(err, ErrPresetNotFound) {
		t.Fatalf("PresetTags(unknown) err=%v want ErrPresetNotFound", err)
	}
}

func TestPerSubscriptionSchedulerIgnoresLegacyGlobalEnabledAndWakes(t *testing.T) {
	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	lib, _ := library.Open(db, filepath.Join(tmp, "cache"))
	cred, _, _ := credential.Open(filepath.Join(tmp, "cred.dpapi"))
	client := bilibili.NewClient(bilibili.ClientOptions{CredStore: cred, PacingInterval: -1})

	// Legacy v1 settings.json often has Enabled=false from before DEC-080.
	legacyCfg := settings.SubscriptionSettings{
		Enabled:         false,
		IntervalMinutes: 60,
		Mode:            "interval",
	}
	mgr, err := NewManager(db, lib, nil, client, legacyCfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	mgr.SetSiteAuthCheck(func(s site.ID) bool { return s == site.Bilibili })

	t0 := time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)
	mgr.SetNowFunc(func() time.Time { return t0 })

	drainWake := func() {
		for {
			select {
			case <-mgr.schedWake:
			default:
				return
			}
		}
	}
	expectWoken := func(step string) {
		t.Helper()
		select {
		case <-mgr.schedWake:
		default:
			t.Fatalf("%s: expected WakeScheduler signal on schedWake", step)
		}
	}

	// 1. Only a manual subscription exists -> not scheduled.
	drainWake()
	manualSub, err := mgr.Create("ManualOnly", "", []AccountRef{{Site: "bilibili", UID: "6001", Name: "UP6001"}}, EmptySubRules())
	if err != nil {
		t.Fatal(err)
	}
	expectWoken("create manual sub")
	mgr.mu.Lock()
	hasSched := mgr.hasScheduledSubscriptionLocked()
	mgr.mu.Unlock()
	if hasSched {
		t.Fatal("expected hasScheduledSubscriptionLocked()=false when only manual sub exists")
	}
	plan := mgr.CheckPlan()
	if len(plan) != 1 || plan[0].Plan != PlanManual {
		t.Fatalf("manual sub plan=%+v want PlanManual", plan)
	}

	// 2. Create a new subscription with DefaultSubRules() -> full archive, wakes scheduler, scheduled even though legacy Enabled=false!
	drainWake()
	fullSub, err := mgr.Create("DefaultFull", "", []AccountRef{{Site: "bilibili", UID: "6002", Name: "UP6002"}}, DefaultSubRules())
	if err != nil {
		t.Fatal(err)
	}
	expectWoken("create full-archive sub")
	if !slices.Equal(fullSub.Tags, DefaultNewSubTags()) || fullSub.Rules.AutoCache.Since != t0.Format(time.RFC3339) {
		t.Fatalf("new default sub tags=%v rules=%+v", fullSub.Tags, fullSub.Rules)
	}
	if _, ok := NextRunTime(legacyCfg, t0); !ok {
		t.Fatal("NextRunTime should return ok=true when Mode!=manual even if legacy Enabled=false")
	}
	mgr.mu.Lock()
	hasSched = mgr.hasScheduledSubscriptionLocked()
	mgr.mu.Unlock()
	if !hasSched {
		t.Fatal("expected hasScheduledSubscriptionLocked()=true after creating default sub")
	}
	plan = mgr.CheckPlan()
	foundFull := false
	for _, row := range plan {
		if row.SubID == fullSub.ID {
			foundFull = true
			if row.Plan != PlanScheduled || row.NextAt == "" {
				t.Fatalf("expected PlanScheduled with legacy Enabled=false, got %+v", row)
			}
		}
	}
	if !foundFull {
		t.Fatalf("missing fullSub in CheckPlan: %+v", plan)
	}

	// 3. Upsert creating a brand new subscription with DefaultSubRules() also records AutoCache.Since.
	drainWake()
	upserted, err := mgr.Upsert(Subscription{
		Name:     "UpsertFull",
		Accounts: []AccountRef{{Site: "bilibili", UID: "6003", Name: "UP6003"}},
		Rules:    DefaultSubRules(),
	})
	if err != nil {
		t.Fatal(err)
	}
	expectWoken("upsert new sub")
	if upserted.Rules.AutoCache.Since != t0.Format(time.RFC3339) || !slices.Equal(upserted.Tags, DefaultNewSubTags()) {
		t.Fatalf("upsert new default sub tags=%v rules=%+v", upserted.Tags, upserted.Rules)
	}

	// 4. Updating tags wakes scheduler.
	drainWake()
	if _, err := mgr.SetSubscriptionTags(manualSub.ID, []string{TagScheduledCheck}); err != nil {
		t.Fatal(err)
	}
	expectWoken("set subscription tags")

	// 5. Explicit Mode="manual" still pauses scheduling (PlanOff).
	mgr.UpdateSettings(settings.SubscriptionSettings{
		Enabled:         false,
		IntervalMinutes: 60,
		Mode:            "manual",
	})
	if _, ok := NextRunTime(settings.SubscriptionSettings{Mode: "manual", IntervalMinutes: 60}, t0); ok {
		t.Fatal("NextRunTime should return false when Mode=manual")
	}
	for _, row := range mgr.CheckPlan() {
		if row.SubID == fullSub.ID && row.Plan != PlanOff {
			t.Fatalf("expected PlanOff when Mode=manual, got %+v", row)
		}
	}
}
