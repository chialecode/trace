package live

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/subscription"
)

// Douyin live checks follow the interval setting: a round checks every
// account it can with the live check gap between requests, those checked
// longest ago first, so an account's interval stays the round's until the
// accounts no longer fit in one round, and grows by about the gap per
// further account, not with a share of a fixed hourly budget (dev
// 2026-11-18: 17 accounts were about 18 minutes apart). An account newly
// linked to an auto-record subscription is checked in the next round.
func TestDouyinLiveChecksPacedThroughRound(t *testing.T) {
	mgr, _, subMgr, _, _ := setupLiveTestHarness(t, http.NewServeMux())
	provider := newFakeDouyinLiveProvider()
	mgr.RegisterSiteProvider(provider)
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	mgr.SetNowFunc(func() time.Time { return now })
	var waits []time.Duration
	mgr.douyinGapWait = func(_ context.Context, d time.Duration) bool {
		waits = append(waits, d)
		return true
	}
	if got := mgr.DouyinLiveGap(); got != DefaultDouyinLiveGap {
		t.Fatalf("default gap %v", got)
	}
	// The default 3-minute round fits 30 accounts at the 6-second gap.
	if every := mgr.DouyinLiveCheckEvery(17); every != 3*time.Minute {
		t.Fatalf("17 accounts estimated every %v, want the 3-minute round", every)
	}
	if every := mgr.DouyinLiveCheckEvery(30); every != 3*time.Minute {
		t.Fatalf("30 accounts estimated every %v, want the 3-minute round", every)
	}

	// A wider gap fits 3 checks in a round, to see the rotation.
	mgr.SetDouyinLiveGap(time.Minute)
	uid := func(i int) string { return fmt.Sprintf("MS4wLjABAAAA_budget_%02d", i) }
	var accounts []subscription.AccountRef
	for i := 0; i < 17; i++ {
		accounts = append(accounts, subscription.AccountRef{Site: "douyin", UID: uid(i), Name: uid(i)})
	}
	rules := subscription.SubRules{ScheduledCheck: true, AutoRecord: true}
	if _, err := subMgr.Create("first", "", accounts[:10], rules); err != nil {
		t.Fatal(err)
	}
	if _, err := subMgr.Create("second", "", accounts[10:16], rules); err != nil {
		t.Fatal(err)
	}
	calls := func() int {
		provider.mu.Lock()
		defer provider.mu.Unlock()
		n := 0
		for _, c := range provider.statusCalls {
			n += c
		}
		return n
	}
	const perRound = 3
	round := func() {
		waits = nil
		if _, err := mgr.PollDouyinAutoRecordNow(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(waits) != perRound-1 {
			t.Fatalf("%d waits between %d checks", len(waits), perRound)
		}
		for _, w := range waits {
			if w != time.Minute {
				t.Fatalf("waited %v between checks, want the gap", w)
			}
		}
		now = now.Add(3 * time.Minute)
	}
	for r := 1; r <= 16/perRound; r++ {
		round()
		if got := calls(); got != r*perRound {
			t.Fatalf("after round %d: %d live checks, want %d", r, got, r*perRound)
		}
	}
	provider.mu.Lock()
	for i := 0; i < 16; i++ {
		if provider.statusCalls[uid(i)] > 1 {
			t.Errorf("%s checked %d times before every account was checked once", uid(i), provider.statusCalls[uid(i)])
		}
	}
	provider.mu.Unlock()

	subs, _ := subMgr.List()
	if _, err := subMgr.Link(subs[0].ID, "douyin", uid(16), uid(16), ""); err != nil {
		t.Fatal(err)
	}
	before := calls()
	round()
	provider.mu.Lock()
	newChecked := provider.statusCalls[uid(16)]
	provider.mu.Unlock()
	if newChecked != 1 || calls()-before != perRound {
		t.Fatalf("new account checked %d times, round made %d checks", newChecked, calls()-before)
	}
	// 17 accounts at 3 a round: 6 rounds of 3 minutes.
	if every := mgr.DouyinLiveCheckEvery(17); every != 18*time.Minute {
		t.Fatalf("17 accounts at 3 a round estimated every %v", every)
	}
}
