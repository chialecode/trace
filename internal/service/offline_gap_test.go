package service

import (
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/site/weibo"
	"github.com/chialecode/trace/internal/site/x"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/chialecode/trace/internal/uistate"
)

type countingFailTransport struct {
	calls atomic.Int64
}

func (c *countingFailTransport) RoundTrip(*http.Request) (*http.Response, error) {
	c.calls.Add(1)
	return nil, errors.New("unexpected outbound network request during offline gap check")
}

func TestHasAutoRecordLiveSubscriptions(t *testing.T) {
	if HasAutoRecordLiveSubscriptions(nil) {
		t.Fatal("empty subscriptions must return false")
	}

	// Non-auto-record live subscriptions -> false.
	noAuto := []subscription.Subscription{
		{
			ID:       "sub-1",
			Accounts: []subscription.AccountRef{{Site: "bilibili", UID: "1001"}},
			Rules:    subscription.SubRules{ScheduledCheck: true},
		},
	}
	if HasAutoRecordLiveSubscriptions(noAuto) {
		t.Fatal("subscription without AutoRecord must return false")
	}

	// Weibo-only and X-only subscriptions with AutoRecord -> false (no live recording on those sites).
	nonLiveSites := []subscription.Subscription{
		{
			ID:       "sub-wb",
			Accounts: []subscription.AccountRef{{Site: "weibo", UID: "5001"}},
			Rules:    subscription.SubRules{AutoRecord: true},
		},
		{
			ID:       "sub-x",
			Accounts: []subscription.AccountRef{{Site: "x", UID: "9001"}},
			Tags:     []string{subscription.TagAutoRecord},
		},
	}
	if HasAutoRecordLiveSubscriptions(nonLiveSites) {
		t.Fatal("weibo/x-only subscriptions must not trigger live offline gap notice")
	}

	// Bilibili with Rules.AutoRecord -> true.
	biliAuto := []subscription.Subscription{
		{
			ID:       "sub-bili",
			Accounts: []subscription.AccountRef{{Site: "bilibili", UID: "2001"}},
			Rules:    subscription.SubRules{AutoRecord: true},
		},
	}
	if !HasAutoRecordLiveSubscriptions(biliAuto) {
		t.Fatal("bilibili subscription with Rules.AutoRecord should return true")
	}

	// Douyin with TagAutoRecord -> true.
	dyAuto := []subscription.Subscription{
		{
			ID:       "sub-dy",
			Accounts: []subscription.AccountRef{{Site: "douyin", UID: "MS4wLjAB_demo"}},
			Tags:     []string{subscription.TagAutoRecord},
		},
	}
	if !HasAutoRecordLiveSubscriptions(dyAuto) {
		t.Fatal("douyin subscription with TagAutoRecord should return true")
	}

	// Legacy Bilibili Mid with LiveAutoRecord -> true.
	legacyAuto := []subscription.Subscription{
		{
			ID:             "sub-legacy",
			Mid:            3001,
			LiveAutoRecord: true,
		},
	}
	if !HasAutoRecordLiveSubscriptions(legacyAuto) {
		t.Fatal("legacy bilibili subscription with LiveAutoRecord should return true")
	}
}

func TestEvaluateOfflineLiveGap(t *testing.T) {
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name          string
		lastActiveAt  string
		hasAutoRecord bool
		hasAnyLogin   bool
		validRestart  bool
		wantOK        bool
	}{
		{
			name:          "first_run_empty_timestamp",
			lastActiveAt:  "",
			hasAutoRecord: true,
			hasAnyLogin:   true,
			wantOK:        false,
		},
		{
			name:          "invalid_timestamp",
			lastActiveAt:  "not-a-date",
			hasAutoRecord: true,
			hasAnyLogin:   true,
			wantOK:        false,
		},
		{
			name:          "clock_moved_backward",
			lastActiveAt:  now.Add(10 * time.Minute).Format(time.RFC3339),
			hasAutoRecord: true,
			hasAnyLogin:   true,
			wantOK:        false,
		},
		{
			name:          "short_restart_below_threshold",
			lastActiveAt:  now.Add(-29 * time.Minute).Format(time.RFC3339),
			hasAutoRecord: true,
			hasAnyLogin:   true,
			wantOK:        false,
		},
		{
			name:          "valid_update_restart_suppressed",
			lastActiveAt:  now.Add(-2 * time.Hour).Format(time.RFC3339),
			hasAutoRecord: true,
			hasAnyLogin:   true,
			validRestart:  true,
			wantOK:        false,
		},
		{
			name:          "logged_out_suppressed",
			lastActiveAt:  now.Add(-2 * time.Hour).Format(time.RFC3339),
			hasAutoRecord: true,
			hasAnyLogin:   false,
			wantOK:        false,
		},
		{
			name:          "no_auto_record_subs_suppressed",
			lastActiveAt:  now.Add(-2 * time.Hour).Format(time.RFC3339),
			hasAutoRecord: false,
			hasAnyLogin:   true,
			wantOK:        false,
		},
		{
			name:          "at_exact_threshold_triggers",
			lastActiveAt:  now.Add(-OfflineLiveGapThreshold).Format(time.RFC3339),
			hasAutoRecord: true,
			hasAnyLogin:   true,
			wantOK:        true,
		},
		{
			name:          "multi_hour_gap_triggers",
			lastActiveAt:  now.Add(-3*time.Hour - 15*time.Minute).Format(time.RFC3339),
			hasAutoRecord: true,
			hasAnyLogin:   true,
			wantOK:        true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			notice, ok := EvaluateOfflineLiveGap(
				tc.lastActiveAt,
				now,
				tc.hasAutoRecord,
				tc.hasAnyLogin,
				tc.validRestart,
				OfflineLiveGapThreshold,
			)
			if ok != tc.wantOK {
				t.Fatalf("EvaluateOfflineLiveGap = ok:%v (notice=%+v), want %v", ok, notice, tc.wantOK)
			}
			if tc.wantOK {
				if notice.Message != OfflineLiveGapMessage {
					t.Fatalf("Message = %q, want %q", notice.Message, OfflineLiveGapMessage)
				}
				if notice.Gap < OfflineLiveGapThreshold {
					t.Fatalf("Gap = %v, want >= %v", notice.Gap, OfflineLiveGapThreshold)
				}
			}
		})
	}
}

func TestRuntimeHeartbeatAndExitRecordLastActiveAtWithZeroSiteRequests(t *testing.T) {
	loc := testLocations(t)
	guard := &countingFailTransport{}
	wrap := func(http.RoundTripper) http.RoundTripper { return guard }

	b, err := NewBackend(Options{
		Locations:         loc,
		Desktop:           NewMockDesktop(),
		DisableBgWorkers:  false,
		HeartbeatInterval: 20 * time.Millisecond,
		BilibiliOpts:      bilibili.ClientOptions{WrapTransport: wrap},
		DouyinOpts:        douyin.ClientOptions{WrapTransport: wrap},
		WeiboOpts:         weibo.ClientOptions{WrapTransport: wrap},
		XOpts:             x.ClientOptions{WrapTransport: wrap},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Add a live auto-record subscription locally and verify HasAutoRecordLiveSubscriptions
	// returns true while making zero network requests.
	if _, err := b.subs.Create("测试主播", "", []subscription.AccountRef{
		{Site: "bilibili", UID: "42001", Name: "测试主播"},
	}, subscription.SubRules{AutoRecord: true}); err != nil {
		b.Close()
		t.Fatal(err)
	}
	if !b.HasAutoRecordLiveSubscriptions() {
		b.Close()
		t.Fatal("expected HasAutoRecordLiveSubscriptions() == true")
	}

	// Wait for the lightweight local runtime heartbeat to write LastActiveAt.
	deadline := time.Now().Add(2 * time.Second)
	var firstStamp string
	for time.Now().Before(deadline) {
		if st := b.GetUIState(); st.LastActiveAt != "" {
			firstStamp = st.LastActiveAt
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if firstStamp == "" {
		b.Close()
		t.Fatal("runtime heartbeat did not persist LastActiveAt to ui-state.json")
	}
	if _, err := time.Parse(time.RFC3339, firstStamp); err != nil {
		b.Close()
		t.Fatalf("LastActiveAt is not RFC3339: %q (%v)", firstStamp, err)
	}

	// Overwrite LastActiveAt to an older marker, then verify PauseForExit updates it.
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	if _, err := b.MutateUIState(func(st *uistate.State) { st.LastActiveAt = old }); err != nil {
		b.Close()
		t.Fatal(err)
	}
	b.PauseForExit()
	afterExit := b.GetUIState().LastActiveAt
	if afterExit == "" || afterExit == old {
		b.Close()
		t.Fatalf("PauseForExit did not update LastActiveAt: got %q", afterExit)
	}

	if err := b.Close(); err != nil {
		t.Fatal(err)
	}

	if calls := guard.calls.Load(); calls != 0 {
		t.Fatalf("expected ZERO outbound site requests, got %d", calls)
	}
}
