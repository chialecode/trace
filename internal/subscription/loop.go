package subscription

import (
	"context"
	"fmt"
	"time"

	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site"
)

// Start launches the background schedule loop (and optional checkOnStartup).
func (m *Manager) Start(parentCtx context.Context) {
	m.mu.Lock()
	if m.schedCancel != nil {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parentCtx)
	m.schedCancel = cancel
	checkStartup := m.subSettings.Enabled && m.subSettings.CheckOnStartup
	m.mu.Unlock()

	m.wg.Add(1)
	go m.scheduleLoop(ctx, checkStartup)
}

// Stop stops the background schedule loop and waits for it to exit.
func (m *Manager) Stop() {
	m.mu.Lock()
	cancel := m.schedCancel
	m.schedCancel = nil
	if m.checkCancel != nil {
		m.checkCancel()
	}
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.wg.Wait()
}

// CancelCheck cancels an in-flight subscription check round after the current request finishes.
func (m *Manager) CancelCheck() {
	m.mu.Lock()
	if m.checkCancel != nil {
		m.checkCancel()
	}
	m.mu.Unlock()
}

// UpdateSettings updates the subscription schedule configuration and wakes the timer loop.
func (m *Manager) UpdateSettings(s settings.SubscriptionSettings) {
	m.mu.Lock()
	m.subSettings = s
	if !s.Enabled {
		m.nextRunAt = time.Time{}
	}
	m.mu.Unlock()
	select {
	case m.schedWake <- struct{}{}:
	default:
	}
}

// WakeScheduler makes the schedule loop recompute its next round (after the
// remote's subscriptions or Cookies changed).
func (m *Manager) WakeScheduler() {
	select {
	case m.schedWake <- struct{}{}:
	default:
	}
}

func (m *Manager) scheduleLoop(ctx context.Context, checkOnStartup bool) {
	defer m.wg.Done()
	if checkOnStartup && m.hasAnySiteAuth() {
		_, _ = m.RunScheduledRound(ctx)
	}

	for {
		m.mu.Lock()
		cfg := m.subSettings
		now := m.nowFunc()
		m.mu.Unlock()

		nextTime, ok := NextRunTime(cfg, now)
		m.mu.Lock()
		m.nextRunAt = nextTime
		m.mu.Unlock()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-m.schedWake:
				continue
			}
		}

		waitDur := nextTime.Sub(now)
		if waitDur < time.Second {
			waitDur = time.Second
		}
		timer := time.NewTimer(waitDur)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-m.schedWake:
			timer.Stop()
			continue
		case <-timer.C:
			if m.hasAnySiteAuth() {
				_, _ = m.RunScheduledRound(ctx)
			}
		}
	}
}

// RunScheduledRound executes a scheduled check round across enabled sites, respecting per-site backoff,
// per-site account caps (Bilibili <=30, Douyin <=20), per-site hourly token buckets (Bilibili 200/hr, Douyin 120/hr),
// per-site exponential backoff, and rule-based auto-cache.
func (m *Manager) RunScheduledRound(ctx context.Context) (CheckSummary, error) {
	m.mu.Lock()
	enabled := m.subSettings.Enabled
	m.mu.Unlock()
	if !enabled {
		return CheckSummary{}, nil
	}
	return m.runRoundInternal(site.WithAutomaticRequests(ctx, "subscription"), 0, true)
}

// CheckNow runs an immediate manual subscription check (for targetMid if > 0, or all enabled subscriptions).
// Enforces a single-flight lock so concurrent checks never overlap.
func (m *Manager) CheckNow(ctx context.Context, targetMid int64) (CheckSummary, error) {
	return m.runRoundInternal(ctx, targetMid, false)
}

func (m *Manager) runRoundInternal(ctx context.Context, targetMid int64, isScheduled bool) (CheckSummary, error) {
	return m.runRoundForSubscription(ctx, targetMid, isScheduled, "")
}

func (m *Manager) CheckSubscription(ctx context.Context, subID string) (CheckSummary, error) {
	if subID == "" {
		return CheckSummary{}, fmt.Errorf("subscription: no subscription selected")
	}
	return m.runRoundForSubscription(ctx, 0, false, subID)
}

func (m *Manager) runRoundForSubscription(ctx context.Context, targetMid int64, isScheduled bool, subID string) (CheckSummary, error) {
	return m.executeMultiSiteRound(ctx, targetMid, isScheduled, subID, "", "")
}

// CheckAccount checks one account of a subscription for updates at once
// (Tasks › 检查计划), as a manual check of its subscription would.
func (m *Manager) CheckAccount(ctx context.Context, subID, siteName, uid string) (CheckSummary, error) {
	if subID == "" || uid == "" {
		return CheckSummary{}, fmt.Errorf("subscription: no account selected")
	}
	return m.executeMultiSiteRound(ctx, 0, false, subID, normalizeSchedulerSite(siteName), uid)
}
