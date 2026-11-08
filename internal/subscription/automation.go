package subscription

import (
	"context"
	"time"

	"github.com/chialecode/trace/internal/site/bilibili"
)

func (m *Manager) SetLiveCheck(check func(context.Context, []int64) error) {
	m.mu.Lock()
	m.liveCheck = check
	m.mu.Unlock()
}

func (m *Manager) publishProgress(summary CheckSummary) {
	m.mu.Lock()
	snapshot := summary
	m.lastSummary = &snapshot
	callback := m.onEvent
	m.mu.Unlock()
	if callback != nil {
		callback(summary)
	}
}

// CheckProgress reports a running check independently of the desktop window.
func (m *Manager) CheckProgress() (CheckSummary, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var summary CheckSummary
	if m.lastSummary != nil {
		summary = *m.lastSummary
		summary.Errors = append([]string(nil), summary.Errors...)
		summary.GapAccounts = append([]string(nil), summary.GapAccounts...)
	}
	return summary, m.checking
}

func (m *Manager) CanUseQuotaTokens(n int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.getSchedulerStateLocked()
	cutoff := m.nowFunc().Add(-time.Hour).Unix()
	count := 0
	for _, timestamp := range state.TokenTimestamps {
		if timestamp > cutoff {
			count++
		}
	}
	return count+n <= 200
}

// AutomaticRequestAllowed charges one token immediately before each request.
// Failed persistence denies the request too; restarting cannot replenish quota.
func (m *Manager) AutomaticRequestAllowed() error {
	if active, _ := m.IsInBackoff(); active {
		return &bilibili.SiteError{Kind: bilibili.ErrKindBackoff, Message: "自动请求仍在退避期"}
	}
	if !m.ConsumeQuotaTokens(1) {
		return &bilibili.SiteError{Kind: bilibili.ErrKindQuota, Message: "本小时自动请求额度已用尽"}
	}
	return nil
}

func (m *Manager) RecordAutomaticStop(kind string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.getSchedulerStateLocked()
	now := m.nowFunc().UTC()
	if previous, err := time.Parse(time.RFC3339, state.LastStopAt); err == nil && now.Sub(previous) < time.Second && state.LastStopReason == kind {
		return
	}
	state.ConsecutiveFailures++
	state.LastStopReason, state.LastStopAt = kind, now.Format(time.RFC3339)
	interval := time.Duration(m.subSettings.IntervalMinutes) * time.Minute
	if interval < time.Minute {
		interval = time.Hour
	}
	duration := interval * time.Duration(1<<min(state.ConsecutiveFailures, 10))
	state.BackoffUntil = now.Add(min(duration, 24*time.Hour)).Format(time.RFC3339)
	_ = m.saveSchedulerStateLocked(state)
}

func isRoundStop(err error) bool {
	switch bilibili.KindOf(err) {
	case bilibili.ErrKindRiskControl, bilibili.ErrKindRateLimited, bilibili.ErrKindAuthRequired, bilibili.ErrKindQuota, bilibili.ErrKindBackoff:
		return true
	}
	return false
}
