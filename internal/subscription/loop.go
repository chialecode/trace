package subscription

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site"
)

// hasScheduledSubscriptionLocked reports whether at least one subscription has
// scheduled_check enabled (DEC-080).
func (m *Manager) hasScheduledSubscriptionLocked() bool {
	subs, err := m.listSubsOnlyLocked()
	if err != nil {
		return false
	}
	for _, sub := range subs {
		if sub.Rules.ScheduledCheck {
			return true
		}
	}
	return false
}

// Start launches the background schedule loop (and optional checkOnStartup).
func (m *Manager) Start(parentCtx context.Context) {
	m.mu.Lock()
	if m.schedCancel != nil {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parentCtx)
	m.schedCancel = cancel
	checkStartup := m.subSettings.CheckOnStartup &&
		!strings.EqualFold(strings.TrimSpace(m.subSettings.Mode), "manual") &&
		m.hasScheduledSubscriptionLocked()
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

func scheduleConfigEqual(a, b settings.SubscriptionSettings) bool {
	if !strings.EqualFold(strings.TrimSpace(a.Mode), strings.TrimSpace(b.Mode)) {
		return false
	}
	if a.IntervalMinutes != b.IntervalMinutes {
		return false
	}
	if strings.TrimSpace(a.DailyTime) != strings.TrimSpace(b.DailyTime) {
		return false
	}
	if strings.TrimSpace(a.WeeklyTime) != strings.TrimSpace(b.WeeklyTime) {
		return false
	}
	da := append([]int(nil), a.WeeklyDays...)
	db := append([]int(nil), b.WeeklyDays...)
	slices.Sort(da)
	slices.Sort(db)
	return slices.Equal(da, db)
}

// syncSchedulerActiveLocked wakes the schedule loop only when the active
// scheduled-check state transitions (0 <-> >=1 scheduled subscriptions), so
// routine subscription writes never starve or postpone an armed timer (DEC-080).
func (m *Manager) syncSchedulerActiveLocked() {
	hasScheduled := m.hasScheduledSubscriptionLocked()
	manualMode := strings.EqualFold(strings.TrimSpace(m.subSettings.Mode), "manual")
	if !hasScheduled || manualMode {
		if !m.nextRunAt.IsZero() {
			m.nextRunAt = time.Time{}
			m.WakeScheduler()
		}
		return
	}
	if m.nextRunAt.IsZero() {
		m.WakeScheduler()
	}
}

// UpdateSettings updates the subscription schedule configuration and wakes the
// timer loop when the schedule parameters or active state change.
func (m *Manager) UpdateSettings(s settings.SubscriptionSettings) {
	m.mu.Lock()
	changed := !scheduleConfigEqual(m.subSettings, s)
	m.subSettings = s
	hasScheduled := m.hasScheduledSubscriptionLocked()
	manualMode := strings.EqualFold(strings.TrimSpace(s.Mode), "manual")
	shouldWake := false
	if manualMode || !hasScheduled {
		if !m.nextRunAt.IsZero() {
			m.nextRunAt = time.Time{}
			shouldWake = true
		}
	} else if changed {
		m.nextRunAt = time.Time{}
		shouldWake = true
	} else if m.nextRunAt.IsZero() {
		shouldWake = true
	}
	m.mu.Unlock()
	if shouldWake {
		m.WakeScheduler()
	}
}

// WakeScheduler makes the schedule loop recompute its next round (after
// subscriptions, tags, settings, or Cookies changed).
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
		hasScheduled := m.hasScheduledSubscriptionLocked()
		var nextTime time.Time
		var ok bool
		if hasScheduled {
			if candidate, schedulable := NextRunTime(cfg, now); schedulable {
				if !m.nextRunAt.IsZero() && m.nextRunAt.After(now) {
					nextTime, ok = m.nextRunAt, true
				} else {
					nextTime, ok = candidate, true
				}
			}
		}
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
			m.mu.Lock()
			m.nextRunAt = time.Time{}
			m.mu.Unlock()
			if m.hasAnySiteAuth() {
				_, _ = m.RunScheduledRound(ctx)
			}
			// Drain any wakeup queued during the round before computing the next run.
			select {
			case <-m.schedWake:
			default:
			}
		}
	}
}

// RunScheduledRound executes a scheduled check round across enabled sites whenever at least one
// subscription has scheduled_check enabled (DEC-080), respecting per-site backoff,
// per-site account caps (Bilibili <=30, Douyin <=20, Weibo <=20, X <=15), per-site hourly token buckets
// (Bilibili 200/hr, Douyin 120/hr, Weibo 100/hr, X 60/hr), and rule-based auto-cache.
func (m *Manager) RunScheduledRound(ctx context.Context) (CheckSummary, error) {
	m.mu.Lock()
	mode := strings.ToLower(strings.TrimSpace(m.subSettings.Mode))
	hasScheduled := m.hasScheduledSubscriptionLocked()
	m.mu.Unlock()
	if mode == "manual" || !hasScheduled {
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
	siteID, ok := site.ParseID(siteName)
	if !ok {
		return CheckSummary{}, fmt.Errorf("subscription: 不支持的站点 %q", siteName)
	}
	if normUID, err := site.NormalizeUID(siteID, uid); err == nil {
		uid = normUID
	}
	return m.executeMultiSiteRound(ctx, 0, false, subID, string(siteID), uid)
}
