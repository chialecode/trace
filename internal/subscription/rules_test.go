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

// DEC-040: subscriptions created with per-subscription settings (as before
// shared rules existed) are bound to shared rules with exactly their
// settings; nothing changes what they do.
func TestExistingSubscriptionsMigrateToSharedRulesUnchanged(t *testing.T) {
	tmp := t.TempDir()
	m := openRuleTestManager(t, tmp)
	video := SubRules{ScheduledCheck: true, AutoCache: AutoCacheRule{Enabled: true, Types: []string{"video"}}}
	a, err := m.Create("A", "", []AccountRef{{Site: "bilibili", UID: "1001"}}, video)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := m.Create("B", "", []AccountRef{{Site: "bilibili", UID: "1002"}}, video)
	c, _ := m.Create("C", "", []AccountRef{{Site: "bilibili", UID: "1003"}}, DefaultSubRules())
	sa, sb, sc := subByID(t, m, a.ID), subByID(t, m, b.ID), subByID(t, m, c.ID)
	if sa.RuleID == "" || sa.RuleID != sb.RuleID || sc.RuleID != DefaultRuleID {
		t.Fatalf("rules: A=%q B=%q C=%q", sa.RuleID, sb.RuleID, sc.RuleID)
	}
	if !sa.Rules.AutoCache.Enabled || sa.Rules.AutoCache.Since == "" {
		t.Fatal("binding a rule changed the subscription's own settings")
	}

	// A file written before rules were shared: drop ruleId and cacheRules.
	path := filepath.Join(tmp, "subscriptions.json")
	raw, _ := os.ReadFile(path)
	var sf map[string]any
	_ = json.Unmarshal(raw, &sf)
	delete(sf, "cacheRules")
	for _, s := range sf["subscriptions"].([]any) {
		delete(s.(map[string]any), "ruleId")
	}
	raw, _ = json.Marshal(sf)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.SetSubscriptionsPath(path); err != nil {
		t.Fatal(err)
	}
	sa2, sb2, sc2 := subByID(t, m, a.ID), subByID(t, m, b.ID), subByID(t, m, c.ID)
	if sa2.RuleID == "" || sa2.RuleID != sb2.RuleID || sc2.RuleID != DefaultRuleID {
		t.Fatalf("migrated rules: A=%q B=%q C=%q", sa2.RuleID, sb2.RuleID, sc2.RuleID)
	}
	if sa2.Rules.AutoCache.Since != sa.Rules.AutoCache.Since || !sa2.Enabled {
		t.Fatal("migration changed what a subscription does")
	}
	rules := m.ListRules()
	if len(rules) != 2 || rules[0].ID != DefaultRuleID {
		t.Fatalf("rules after migration: %+v", rules)
	}
	if got := RuleSummary(rules[1]); got != "定时检查 · 自动缓存视频" {
		t.Fatalf("migrated rule summary %q", got)
	}
}

// Editing a shared rule changes every subscription using it; choosing a
// rule applies it; deleting a rule moves its subscriptions to the default.
func TestSharedRuleEditsApplyToAllItsSubscriptions(t *testing.T) {
	m := openRuleTestManager(t, t.TempDir())
	m.SetNowFunc(func() time.Time { return time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC) })
	a, _ := m.Create("A", "", []AccountRef{{Site: "bilibili", UID: "2001"}}, DefaultSubRules())
	b, _ := m.Create("B", "", []AccountRef{{Site: "bilibili", UID: "2002"}}, DefaultSubRules())

	rule, err := m.SaveRule(CacheRule{Name: "全部自动", ScheduledCheck: true, AutoCache: true, Types: []string{"draw", "video"}, AutoRecord: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.SaveRule(CacheRule{Name: "全部自动"}); err == nil {
		t.Fatal("two rules may not share a name")
	}
	for _, id := range []string{a.ID, b.ID} {
		if _, err := m.SetSubscriptionRule(id, rule.ID); err != nil {
			t.Fatal(err)
		}
	}
	sa := subByID(t, m, a.ID)
	if sa.RuleID != rule.ID || !sa.Rules.AutoCache.Enabled || !sa.LiveAutoRecord || sa.Rules.AutoCache.Since == "" {
		t.Fatalf("choosing a rule did not apply it: %+v", sa.Rules)
	}
	since := sa.Rules.AutoCache.Since

	m.SetNowFunc(func() time.Time { return time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC) })
	rule.ScheduledCheck = false
	rule.Types = []string{"video"}
	if _, err := m.SaveRule(rule); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a.ID, b.ID} {
		s := subByID(t, m, id)
		if s.Enabled || s.Rules.ScheduledCheck || len(s.Rules.AutoCache.Types) != 1 || s.RuleID != rule.ID {
			t.Fatalf("editing the rule did not reach %s: %+v", id, s.Rules)
		}
	}
	if s := subByID(t, m, a.ID); s.Rules.AutoCache.Since != since {
		t.Fatal("editing a rule restarted automatic caching from now")
	}

	if err := m.DeleteRule(DefaultRuleID); err == nil {
		t.Fatal("the default rule was deleted")
	}
	if err := m.DeleteRule(rule.ID); err != nil {
		t.Fatal(err)
	}
	s := subByID(t, m, b.ID)
	// The built-in rule has every option off (DEC-054).
	if s.RuleID != DefaultRuleID || s.Rules.AutoCache.Enabled || s.Enabled || s.Rules.ScheduledCheck || s.LiveAutoRecord {
		t.Fatalf("a deleted rule's subscription did not fall back to the default: %q %+v", s.RuleID, s.Rules)
	}
	if len(m.ListRules()) != 1 {
		t.Fatal("the deleted rule is still listed")
	}
}

// SetSubscriptionsRule updates newly selected subscriptions, reverts
// unselected former members of a non-default rule to DefaultRuleID, leaves
// subscriptions on other rules untouched, writes subscriptions.json once, and
// rolls back completely on validation or write errors.
func TestSetSubscriptionsRuleBatchAtomicAndRollback(t *testing.T) {
	tmp := t.TempDir()
	m := openRuleTestManager(t, tmp)

	t0 := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	t1 := time.Date(2026, 11, 8, 11, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	m.SetNowFunc(func() time.Time { return t0 })

	ruleVideo, err := m.SaveRule(CacheRule{
		Name:           "只存视频",
		ScheduledCheck: true,
		AutoCache:      true,
		Types:          []string{"video"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ruleLive, err := m.SaveRule(CacheRule{
		Name:           "自动录播",
		ScheduledCheck: true,
		AutoCache:      true,
		Types:          []string{"video", "draw"},
		AutoRecord:     true,
	})
	if err != nil {
		t.Fatal(err)
	}

	s1, err := m.Create("Sub1", "", []AccountRef{{Site: "bilibili", UID: "3001"}}, SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatal(err)
	}
	s2, _ := m.Create("Sub2", "", []AccountRef{{Site: "bilibili", UID: "3002"}}, SubRules{ScheduledCheck: true})
	s3, _ := m.Create("Sub3", "", []AccountRef{{Site: "bilibili", UID: "3003"}}, SubRules{ScheduledCheck: true})
	s4, _ := m.Create("Sub4", "", []AccountRef{{Site: "bilibili", UID: "3004"}}, SubRules{ScheduledCheck: true})
	s5, _ := m.Create("Sub5", "", []AccountRef{{Site: "bilibili", UID: "3005"}}, SubRules{ScheduledCheck: true})

	for _, id := range []string{s2.ID, s3.ID} {
		if _, err := m.SetSubscriptionRule(id, ruleVideo.ID); err != nil {
			t.Fatal(err)
		}
	}
	m.SetNowFunc(func() time.Time { return t1 })
	for _, id := range []string{s4.ID, s5.ID} {
		if _, err := m.SetSubscriptionRule(id, ruleLive.ID); err != nil {
			t.Fatal(err)
		}
	}

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
	m.SetNowFunc(func() time.Time { return t2 })

	// Select 3 subscriptions (s1 from default, s2 kept on ruleVideo, s4 from ruleLive);
	// s3 (formerly ruleVideo) is unchecked -> falls back to DefaultRuleID;
	// s5 (ruleLive) is unchecked -> stays on ruleLive.
	if err := m.SetSubscriptionsRule(ruleVideo.ID, []string{s1.ID, s2.ID, s4.ID}); err != nil {
		t.Fatalf("SetSubscriptionsRule: %v", err)
	}
	if writes != 1 {
		t.Fatalf("expected exactly 1 disk write for batch update, got %d", writes)
	}
	bakAfter, err := os.ReadFile(subsPath + ".bak")
	if err != nil || !bytes.Equal(bakAfter, rawBefore) {
		t.Fatalf("expected subscriptions.json.bak to match pre-batch snapshot (single atomic write), err=%v", err)
	}

	got1 := subByID(t, m, s1.ID)
	got2 := subByID(t, m, s2.ID)
	got3 := subByID(t, m, s3.ID)
	got4 := subByID(t, m, s4.ID)
	got5 := subByID(t, m, s5.ID)

	if got1.RuleID != ruleVideo.ID || !got1.Rules.AutoCache.Enabled || got1.Rules.AutoCache.Since != t2.Format(time.RFC3339) {
		t.Fatalf("s1 expected newly enabled ruleVideo at t2, got rule=%q rules=%+v", got1.RuleID, got1.Rules)
	}
	if got2.RuleID != ruleVideo.ID || got2.Rules.AutoCache.Since != t0.Format(time.RFC3339) {
		t.Fatalf("s2 expected preserved AutoCache.Since=%s, got rule=%q rules=%+v", t0.Format(time.RFC3339), got2.RuleID, got2.Rules)
	}
	if got3.RuleID != DefaultRuleID || got3.Rules.AutoCache.Enabled || got3.Rules.AutoCache.Since != "" {
		t.Fatalf("s3 expected fallback to DefaultRuleID with cleared Since, got rule=%q rules=%+v", got3.RuleID, got3.Rules)
	}
	if got4.RuleID != ruleVideo.ID || got4.LiveAutoRecord || !slices.Equal(got4.Rules.AutoCache.Types, []string{"video"}) || got4.Rules.AutoCache.Since != t1.Format(time.RFC3339) {
		t.Fatalf("s4 expected ruleVideo with preserved Since=%s, got rule=%q rec=%v rules=%+v", t1.Format(time.RFC3339), got4.RuleID, got4.LiveAutoRecord, got4.Rules)
	}
	if got5.RuleID != ruleLive.ID || !got5.LiveAutoRecord || !slices.Equal(got5.Rules.AutoCache.Types, []string{"video", "draw"}) || got5.Rules.AutoCache.Since != t1.Format(time.RFC3339) {
		t.Fatalf("s5 expected untouched ruleLive, got rule=%q rec=%v rules=%+v", got5.RuleID, got5.LiveAutoRecord, got5.Rules)
	}

	// Reload from disk to confirm persisted subscriptions.json matches in-memory/SQLite state.
	reloaded := openRuleTestManager(t, tmp)
	if r3, r5 := subByID(t, reloaded, s3.ID), subByID(t, reloaded, s5.ID); r3.RuleID != DefaultRuleID || r5.RuleID != ruleLive.ID {
		t.Fatalf("reloaded mismatch: s3=%q s5=%q", r3.RuleID, r5.RuleID)
	}

	// DefaultRuleID batch pull-back: pulls s1 and s5 back to DefaultRuleID while keeping existing DefaultRuleID member s3 on DefaultRuleID.
	if err := m.SetSubscriptionsRule(DefaultRuleID, []string{s1.ID, s5.ID}); err != nil {
		t.Fatalf("SetSubscriptionsRule(DefaultRuleID): %v", err)
	}
	if writes != 2 {
		t.Fatalf("expected 2 total disk writes, got %d", writes)
	}
	for _, id := range []string{s1.ID, s3.ID, s5.ID} {
		if g := subByID(t, m, id); g.RuleID != DefaultRuleID || g.Rules.AutoCache.Enabled || g.LiveAutoRecord {
			t.Fatalf("%s expected DefaultRuleID after pull-back, got rule=%q rec=%v rules=%+v", id, g.RuleID, g.LiveAutoRecord, g.Rules)
		}
	}
	for _, id := range []string{s2.ID, s4.ID} {
		if g := subByID(t, m, id); g.RuleID != ruleVideo.ID {
			t.Fatalf("%s expected to remain on ruleVideo, got %q", id, g.RuleID)
		}
	}

	// Validation errors do not touch disk or state.
	if err := m.SetSubscriptionsRule("rule_missing", []string{s1.ID}); !errors.Is(err, ErrRuleNotFound) {
		t.Fatalf("expected ErrRuleNotFound, got %v", err)
	}
	if err := m.SetSubscriptionsRule(ruleLive.ID, []string{s1.ID, "sub_does_not_exist"}); err == nil {
		t.Fatal("expected unknown subID error")
	}
	if writes != 2 || subByID(t, m, s1.ID).RuleID != DefaultRuleID {
		t.Fatalf("validation error mutated disk or state: writes=%d s1=%q", writes, subByID(t, m, s1.ID).RuleID)
	}

	// Simulated disk write failure rolls back SQLite and keeps subscriptions.json intact.
	snapshotBeforeFail, err := os.ReadFile(subsPath)
	if err != nil {
		t.Fatal(err)
	}
	hookErr = errors.New("simulated disk full")
	if err := m.SetSubscriptionsRule(ruleLive.ID, []string{s1.ID, s2.ID}); err == nil {
		t.Fatal("expected disk write error")
	}
	hookErr = nil
	if subByID(t, m, s1.ID).RuleID != DefaultRuleID || subByID(t, m, s2.ID).RuleID != ruleVideo.ID {
		t.Fatalf("failed batch was not rolled back in SQLite: s1=%q s2=%q", subByID(t, m, s1.ID).RuleID, subByID(t, m, s2.ID).RuleID)
	}
	snapshotAfterFail, err := os.ReadFile(subsPath)
	if err != nil || !bytes.Equal(snapshotAfterFail, snapshotBeforeFail) {
		t.Fatal("failed batch modified subscriptions.json on disk")
	}
}

// DEC-054: the built-in rule has every option off, cannot be edited, and is
// what new subscriptions (created, subscribed from a user page or imported)
// start on.
func TestDefaultRuleIsLockedAndAllOffForNewSubscriptions(t *testing.T) {
	m := openRuleTestManager(t, t.TempDir())
	def := m.ListRules()[0]
	if def.ID != DefaultRuleID || def.ScheduledCheck || def.AutoCache || def.AutoRecord || RuleSummary(def) != "只手动检查" {
		t.Fatalf("default rule %+v", def)
	}
	if _, err := m.SaveRule(CacheRule{ID: DefaultRuleID, Name: "默认", ScheduledCheck: true}); !errors.Is(err, ErrDefaultRuleLocked) {
		t.Fatalf("editing the default rule: %v", err)
	}
	if _, err := m.Create("新订阅", "", []AccountRef{{Site: "bilibili", UID: "3001"}}, DefaultSubRules()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Upsert(Subscription{Site: "bilibili", Mid: 3002, Name: "用户页订阅", Scope: "both", AutoCache: "inherit", Rules: DefaultSubRules()}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ImportCreators([]bilibili.FollowingUser{{Mid: 3003, Uname: "导入"}}, "both", "inherit", false, false); err != nil {
		t.Fatal(err)
	}
	subs, _ := m.List()
	if len(subs) != 3 {
		t.Fatalf("want 3 subscriptions, got %d", len(subs))
	}
	for _, s := range subs {
		if s.RuleID != DefaultRuleID || s.Enabled || s.Rules.ScheduledCheck || s.Rules.AutoCache.Enabled || s.LiveAutoRecord {
			t.Fatalf("new subscription %s not on the all-off default: %q %+v", s.Name, s.RuleID, s.Rules)
		}
	}
	if len(m.ListRules()) != 1 {
		t.Fatalf("new subscriptions created extra rules: %+v", m.ListRules())
	}
}

// An edited default rule (here renamed "自定义" with everything on) is reset;
// its subscriptions keep their settings on a rule that keeps its name.
func TestEditedDefaultRuleMovesToANamedRule(t *testing.T) {
	tmp := t.TempDir()
	m := openRuleTestManager(t, tmp)
	all := SubRules{ScheduledCheck: true, AutoRecord: true, AutoCache: AutoCacheRule{Enabled: true, Types: []string{"video", "draw", "word", "forward"}}}
	sub, err := m.Create("A", "", []AccountRef{{Site: "bilibili", UID: "4001"}}, all)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(tmp, "subscriptions.json")
	raw, _ := os.ReadFile(path)
	var sf map[string]any
	_ = json.Unmarshal(raw, &sf)
	sf["cacheRules"] = []any{map[string]any{"id": "default", "name": "自定义", "scheduledCheck": true, "autoCache": true, "types": []string{"video", "draw", "word", "forward"}, "autoRecord": true}}
	for _, s := range sf["subscriptions"].([]any) {
		s.(map[string]any)["ruleId"] = "default"
	}
	raw, _ = json.Marshal(sf)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	m2 := openRuleTestManager(t, tmp)
	rules := m2.ListRules()
	if len(rules) != 2 || rules[0].ScheduledCheck || rules[1].Name != "自定义" || !rules[1].AutoRecord || !rules[1].AutoCache {
		t.Fatalf("rules after reset: %+v", rules)
	}
	got := subByID(t, m2, sub.ID)
	if got.RuleID != rules[1].ID || !got.Rules.ScheduledCheck || !got.LiveAutoRecord || !got.Rules.AutoCache.Enabled {
		t.Fatalf("subscription changed behaviour: %q %+v", got.RuleID, got.Rules)
	}
	written, _ := os.ReadFile(path)
	if !strings.Contains(string(written), rules[1].ID) {
		t.Fatal("the reset was not written back")
	}
}
