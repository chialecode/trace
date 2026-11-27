package subscription

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/storage"
)

func TestWeiboBackoffLadderDecoupledDecayAndAutoSuccessReset(t *testing.T) {
	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	updatesDB, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "updates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer updatesDB.Close()

	cfg := settings.Default(tmp).Subscription
	cfg.Enabled = true
	cfg.IntervalMinutes = 120 // 2h user check interval — Weibo ladder must stay decoupled!
	mgr, err := NewManagerWithUpdatesDB(db, updatesDB, filepath.Join(tmp, "subscriptions.json"), nil, nil, nil, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	mgr.SetNowFunc(func() time.Time { return now })

	// 1. Weibo ladder: 15m -> 30m -> 1h -> 2h -> 6h -> 6h (independent of IntervalMinutes=120).
	wantWeibo := []time.Duration{
		15 * time.Minute,
		30 * time.Minute,
		1 * time.Hour,
		2 * time.Hour,
		6 * time.Hour,
		6 * time.Hour,
	}
	for i, wantDur := range wantWeibo {
		mgr.RecordSiteAutomaticStop("weibo", site.ErrKindRiskControl)
		active, until := mgr.IsSiteInBackoff("weibo")
		if !active || !until.Equal(now.Add(wantDur)) {
			t.Fatalf("weibo step %d: backoff=(%v, %v), want (true, %v)", i+1, active, until, now.Add(wantDur))
		}
		if gotDur := SiteBackoffDuration("weibo", i+1, cfg.IntervalMinutes); gotDur != wantDur {
			t.Fatalf("SiteBackoffDuration(weibo, %d, 120) = %v, want %v", i+1, gotDur, wantDur)
		}
		now = now.Add(time.Second)
	}

	// 2. Other sites still follow interval * 2^n up to 24h (here IntervalMinutes=120 -> 4h at level 1).
	if got := SiteBackoffDuration("bilibili", 1, cfg.IntervalMinutes); got != 4*time.Hour {
		t.Fatalf("SiteBackoffDuration(bilibili, 1, 120) = %v, want 4h", got)
	}

	// 3. Auto-decay: 6h quiet since LastStopAt resets Weibo BackoffLevel to 0 before next stop.
	now = now.Add(6*time.Hour + time.Minute)
	if st := mgr.GetSiteSchedulerState("weibo"); st.BackoffLevel != 0 {
		t.Fatalf("expected Weibo BackoffLevel decayed to 0 after 6h quiet, got %+v", st)
	}
	mgr.RecordSiteAutomaticStop("weibo", site.ErrKindRateLimited)
	if active, until := mgr.IsSiteInBackoff("weibo"); !active || !until.Equal(now.Add(15*time.Minute)) {
		t.Fatalf("after 6h decay, next Weibo stop should restart at 15m, got (%v, %v)", active, until)
	}

	// 4. Clean automatic success after backoff elapses resets Weibo BackoffLevel to 0.
	now = now.Add(20 * time.Minute)
	mgr.RecordSiteAutomaticSuccess("weibo")
	if st := mgr.GetSiteSchedulerState("weibo"); st.BackoffLevel != 0 || st.ConsecutiveFailures != 0 {
		t.Fatalf("RecordSiteAutomaticSuccess did not reset Weibo state: %+v", st)
	}
}

func TestClearSiteBackoffCallbackAndManualProbeNonEscalation(t *testing.T) {
	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	updatesDB, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "updates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer updatesDB.Close()

	cfg := settings.Default(tmp).Subscription
	cfg.IntervalMinutes = 60
	mgr, err := NewManagerWithUpdatesDB(db, updatesDB, filepath.Join(tmp, "subscriptions.json"), nil, nil, nil, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	mgr.SetNowFunc(func() time.Time { return now })

	var clearedSites []site.ID
	mgr.SetOnBackoffCleared(func(sid site.ID) {
		// Verify callback runs without holding mgr.mu by calling back into mgr!
		_ = mgr.CanUseSiteQuotaTokens(string(sid), 1)
		clearedSites = append(clearedSites, sid)
	})

	// Put Weibo into level-1 backoff (15m).
	mgr.RecordSiteAutomaticStop("weibo", site.ErrKindRiskControl)
	origState := mgr.GetSiteSchedulerState("weibo")
	origUntil, _ := time.Parse(time.RFC3339, origState.BackoffUntil)

	// Failed manual probe while already in active backoff MUST NOT escalate BackoffLevel or shorten/double BackoffUntil.
	now = now.Add(2 * time.Minute)
	mgr.RecordSiteManualProbeFailure("weibo", site.ErrKindRiskControl, time.Time{})
	afterProbe := mgr.GetSiteSchedulerState("weibo")
	if afterProbe.BackoffLevel != origState.BackoffLevel || afterProbe.BackoffUntil != origState.BackoffUntil {
		t.Fatalf("failed manual probe mutated active backoff: before=%+v after=%+v", origState, afterProbe)
	}

	// Failed manual probe with a LATER explicit resetAt (e.g. X rate-limit reset) extends BackoffUntil (with 0..60s jitter) without escalating level.
	laterReset := origUntil.Add(20 * time.Minute)
	mgr.RecordSiteManualProbeFailure("weibo", site.ErrKindRateLimited, laterReset)
	afterLater := mgr.GetSiteSchedulerState("weibo")
	gotUntil, err := time.Parse(time.RFC3339, afterLater.BackoffUntil)
	if err != nil || afterLater.BackoffLevel != origState.BackoffLevel || gotUntil.Before(laterReset) || gotUntil.After(laterReset.Add(60*time.Second)) {
		t.Fatalf("manual probe with later resetAt: got %+v, want level=%d until in [%v, %v]", afterLater, origState.BackoffLevel, laterReset, laterReset.Add(60*time.Second))
	}

	// ClearSiteBackoff resets state and fires callback outside lock.
	mgr.ClearSiteBackoff("weibo")
	if active, _ := mgr.IsSiteInBackoff("weibo"); active {
		t.Fatal("ClearSiteBackoff did not clear active backoff")
	}
	if len(clearedSites) != 1 || clearedSites[0] != site.Weibo {
		t.Fatalf("clearedSites = %v, want [weibo]", clearedSites)
	}
}
