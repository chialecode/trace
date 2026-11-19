package subscription

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/storage"
)

// Tasks › 检查计划: each account's last check and the scheduled round that
// should check it next — the oldest-checked first up to the site's round
// cap, the rest a round later, after the site's backoff, and none for a
// manual rule, an unsigned site or scheduled checking turned off.
func TestCheckPlanPerAccount(t *testing.T) {
	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := settings.Default(tmp).Subscription
	cfg.Enabled, cfg.Mode, cfg.IntervalMinutes = true, "interval", 60
	mgr, err := NewManagerWithUpdatesDB(db, db, filepath.Join(tmp, "subscriptions.json"), nil, nil, nil, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	mgr.SetNowFunc(func() time.Time { return now })
	douyinAuth := true
	mgr.SetSiteAuthCheck(func(s site.ID) bool { return s == site.Bilibili || douyinAuth })

	uid := func(i int) string { return fmt.Sprintf("MS4wLjABAAAA_plan_%02d", i) }
	var dy []AccountRef
	for i := 0; i < 25; i++ {
		dy = append(dy, AccountRef{Site: "douyin", UID: uid(i), Name: uid(i)})
	}
	scheduled := SubRules{ScheduledCheck: true, AutoRecord: true}
	if _, err := mgr.Create("一", "", dy[:15], scheduled); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Create("二", "", dy[15:], scheduled); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Create("手动", "", []AccountRef{{Site: "bilibili", UID: "4242", Name: "manual"}}, SubRules{}); err != nil {
		t.Fatal(err)
	}
	// Accounts 0..19 were checked, in order; 20..24 never were.
	mgr.mu.Lock()
	for i := 0; i < 20; i++ {
		_ = mgr.saveAccountStateLocked(AccountState{Site: "douyin", UID: uid(i), LastCheckedAt: now.Add(-time.Duration(60-i) * time.Minute).Format(time.RFC3339), LastKind: "ok"})
	}
	mgr.mu.Unlock()

	byUID := func() map[string]AccountCheckPlan {
		out := map[string]AccountCheckPlan{}
		for _, p := range mgr.CheckPlan() {
			out[p.UID] = p
		}
		return out
	}
	next := now.Add(time.Hour).Format(time.RFC3339)
	later := now.Add(2 * time.Hour).Format(time.RFC3339)
	plan := byUID()
	if len(plan) != 26 {
		t.Fatalf("%d accounts planned, want 26", len(plan))
	}
	// A round takes 20 Douyin accounts: the 5 never checked and the 15
	// checked longest ago; the 5 checked last wait a round.
	for i := 0; i < 25; i++ {
		want := next
		if i >= 15 && i < 20 {
			want = later
		}
		if p := plan[uid(i)]; p.Plan != PlanScheduled || p.NextAt != want || !p.AutoRecord {
			t.Errorf("%s: %+v, want next %s", uid(i), p, want)
		}
	}
	if p := plan[uid(3)]; p.LastCheckedAt == "" || p.LastKind != "ok" {
		t.Errorf("last check missing: %+v", p)
	}
	if p := plan["4242"]; p.Plan != PlanManual || p.NextAt != "" {
		t.Errorf("manual rule: %+v", p)
	}

	// A backoff (2 hours after a first stop) pushes the site past it.
	mgr.RecordSiteAutomaticStop("douyin", site.ErrKindRateLimited)
	_, until := mgr.IsSiteInBackoff("douyin")
	p := byUID()[uid(20)]
	at, _ := time.Parse(time.RFC3339, p.NextAt)
	if !p.AfterBackoff || at.Before(until) {
		t.Fatalf("backoff until %v, next check %+v", until, p)
	}

	douyinAuth = false
	if p := byUID()[uid(0)]; p.Plan != PlanUnauth || p.NextAt != "" {
		t.Fatalf("unsigned site: %+v", p)
	}
	cfg.Enabled = false
	mgr.UpdateSettings(cfg)
	if p := byUID()["4242"]; p.Plan != PlanManual {
		t.Fatalf("manual stays manual: %+v", p)
	}
	douyinAuth = true
	if p := byUID()[uid(0)]; p.Plan != PlanOff || p.NextAt != "" {
		t.Fatalf("scheduled checking off: %+v", p)
	}
}
