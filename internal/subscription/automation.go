package subscription

import (
	"context"
	"maps"

	"github.com/chialecode/trace/internal/site"
)

func (m *Manager) SetLiveCheck(check func(context.Context, []int64) error) {
	m.mu.Lock()
	m.liveCheck = check
	m.mu.Unlock()
}

func (m *Manager) publishProgress(summary CheckSummary) {
	m.mu.Lock()
	snapshot := cloneCheckSummary(summary)
	m.lastSummary = &snapshot
	callback := m.onEvent
	m.mu.Unlock()
	if callback != nil {
		callback(cloneCheckSummary(summary))
	}
}

func cloneCheckSummary(summary CheckSummary) CheckSummary {
	summary.Errors = append([]string(nil), summary.Errors...)
	summary.GapAccounts = append([]string(nil), summary.GapAccounts...)
	summary.SiteDeferredByQuota = append([]string(nil), summary.SiteDeferredByQuota...)
	summary.SiteDeferredByBackoff = append([]string(nil), summary.SiteDeferredByBackoff...)
	summary.SkippedUnauthSites = append([]string(nil), summary.SkippedUnauthSites...)
	summary.IncompleteAccounts = append([]string(nil), summary.IncompleteAccounts...)
	if summary.SiteStopped != nil {
		summary.SiteStopped = maps.Clone(summary.SiteStopped)
	}
	return summary
}

// CheckProgress reports a running check independently of the desktop window.
func (m *Manager) CheckProgress() (CheckSummary, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var summary CheckSummary
	if m.lastSummary != nil {
		summary = cloneCheckSummary(*m.lastSummary)
	}
	return summary, m.checking
}

func (m *Manager) CanUseQuotaTokens(n int) bool {
	return m.CanUseSiteQuotaTokens("bilibili", n)
}

// AutomaticRequestAllowed charges one token immediately before each request.
// Failed persistence denies the request too; restarting cannot replenish quota.
func (m *Manager) AutomaticRequestAllowed() error {
	return m.SiteAutomaticRequestAllowed("bilibili")
}

func (m *Manager) RecordAutomaticStop(kind string) {
	m.RecordSiteAutomaticStop("bilibili", kind)
}

func isRoundStop(err error) bool {
	switch site.KindOf(err) {
	case site.ErrKindRiskControl, site.ErrKindRateLimited, site.ErrKindAuthRequired, site.ErrKindQuota, site.ErrKindBackoff:
		return true
	}
	return false
}
