package subscription

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	c, _ := m.Create("C", "", []AccountRef{{Site: "bilibili", UID: "1003"}}, SubRules{ScheduledCheck: true})
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
	a, _ := m.Create("A", "", []AccountRef{{Site: "bilibili", UID: "2001"}}, SubRules{ScheduledCheck: true})
	b, _ := m.Create("B", "", []AccountRef{{Site: "bilibili", UID: "2002"}}, SubRules{ScheduledCheck: true})

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
	if s.RuleID != DefaultRuleID || s.Rules.AutoCache.Enabled || !s.Enabled || s.LiveAutoRecord {
		t.Fatalf("a deleted rule's subscription did not fall back to the default: %q %+v", s.RuleID, s.Rules)
	}
	if len(m.ListRules()) != 1 {
		t.Fatal("the deleted rule is still listed")
	}
}
