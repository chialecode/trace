package service

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
)

func (m *cacheQueueManager) wakeSite(siteID site.ID) {
	if ch, ok := m.siteWake[siteID]; ok {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (m *cacheQueueManager) onSiteBackoffCleared(siteID site.ID) {
	if m == nil || !siteID.Valid() {
		return
	}
	m.mu.Lock()
	delete(m.siteCooldownUntil, siteID)
	now := m.nowRFC3339()
	quotaStillBlocked := m.b != nil && m.b.subs != nil && !m.b.subs.CanUseSiteQuotaTokens(string(siteID), 1)
	var notify []cacheQueueRecord
	changed := false
	for _, e := range m.tasks {
		if e.rec.Site != string(siteID) || e.rec.Status != CacheQueueWaitingBackoff {
			continue
		}
		if quotaStillBlocked && e.rec.StopReason == backoffReasonText(site.ErrKindQuota) {
			continue
		}
		e.rec.Status = CacheQueueQueued
		e.rec.PauseReason = ""
		e.rec.StopReason = ""
		e.rec.NextRunAt = ""
		e.rec.UpdatedAt = now
		e.rt.resumePendingDownloads = len(e.rec.ActiveDownloadIDs) > 0
		changed = true
		notify = append(notify, e.rec)
	}
	if changed {
		m.saveLoggedLocked("backoff_cleared")
	}
	m.mu.Unlock()
	m.notifyStates(notify)
	m.wakeSite(siteID)
}

func (m *cacheQueueManager) onDownloadUpdated(siteName string) {
	if sid, err := normalizeSiteID(siteName); err == nil {
		m.wakeSite(sid)
	}
}

// Start launches the independent per-site queue workers and schedules the 1–5m startup delay
// for any unfinished tasks recovered from the previous session.
func (m *cacheQueueManager) Start(ctx context.Context) {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	loopCtx, cancel := context.WithCancel(ctx)
	m.stopCancel = cancel
	m.started = true

	now := m.clock.Now().UTC()
	changed := false
	for _, sid := range site.All() {
		loggedIn := m.hasSiteLogin(sid)
		assignedActive := false
		for _, e := range m.tasks {
			if e.rec.Site != string(sid) || !isUnfinishedQueueStatus(e.rec.Status) {
				continue
			}
			if e.rec.Status == CacheQueuePaused && e.rec.PauseReason == "user" {
				continue
			}
			if !loggedIn && !isWeiboBackfillRecord(e.rec) {
				if e.rec.Status != CacheQueuePaused || e.rec.PauseReason != "auth_required" {
					e.rec.Status = CacheQueuePaused
					e.rec.PauseReason = "auth_required"
					e.rec.StopReason = "未登录或登录已失效，重新登录后自动继续"
					e.rec.NextRunAt = ""
					e.rec.UpdatedAt = now.Format(time.RFC3339)
					e.rt.resumePendingDownloads = len(e.rec.ActiveDownloadIDs) > 0
					changed = true
				}
				continue
			}
			if e.rec.Status == CacheQueuePaused && e.rec.PauseReason == "auth_required" {
				// A task paused for an expired login resumes on the next login event (or by hand),
				// not merely because some credential is present at startup.
				continue
			}

			e.rt.resumePendingDownloads = len(e.rec.ActiveDownloadIDs) > 0
			if !assignedActive {
				assignedActive = true
				p := m.pacingForSite(sid)
				startupUntil := now.Add(m.randDurationLocked(p.StartupMinDelay, p.StartupMaxDelay))
				if e.rec.Status == CacheQueueWaitingBackoff {
					if existing, err := time.Parse(time.RFC3339, e.rec.NextRunAt); err != nil || existing.Before(startupUntil) {
						e.rec.NextRunAt = startupUntil.Format(time.RFC3339)
					}
				} else {
					if existing, err := time.Parse(time.RFC3339, e.rec.NextRunAt); err == nil && existing.After(startupUntil) {
						startupUntil = existing
					}
					e.rec.Status = CacheQueueWaiting
					e.rec.PauseReason = ""
					e.rec.StopReason = "应用启动后等待继续"
					e.rec.NextRunAt = startupUntil.Format(time.RFC3339)
				}
				e.rec.UpdatedAt = now.Format(time.RFC3339)
				changed = true
			} else if e.rec.Status != CacheQueueQueued {
				e.rec.Status = CacheQueueQueued
				e.rec.PauseReason = ""
				e.rec.StopReason = ""
				e.rec.NextRunAt = ""
				e.rec.UpdatedAt = now.Format(time.RFC3339)
				changed = true
			}
		}
	}
	if changed {
		m.saveLoggedLocked("start")
	}
	m.mu.Unlock()

	for _, sid := range site.All() {
		siteCopy := sid
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.runSiteLoop(loopCtx, siteCopy)
		}()
	}
}

// Stop halts background queue workers cleanly and flushes state to disk for restart recovery.
func (m *cacheQueueManager) Stop() {
	m.mu.Lock()
	cancel := m.stopCancel
	m.stopCancel = nil
	m.started = false
	for _, e := range m.tasks {
		if e.rt.stepCancel != nil {
			e.rt.stepCancel()
			e.rt.stepCancel = nil
		}
	}
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	m.wg.Wait()

	m.mu.Lock()
	m.saveLoggedLocked("stop")
	m.mu.Unlock()
}

func (m *cacheQueueManager) pauseForExit() {
	m.Stop()
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.nowRFC3339()
	changed := false
	for _, e := range m.tasks {
		if !isUnfinishedQueueStatus(e.rec.Status) {
			continue
		}
		if e.rec.Status == CacheQueuePaused {
			continue
		}
		if e.rec.Status == CacheQueueWaitingBackoff {
			// Keep waiting_backoff intact across exit so backoff cooldown is never shortened.
			continue
		}
		e.rec.Status = CacheQueuePaused
		e.rec.PauseReason = "app_closed"
		e.rec.StopReason = "已暂停（应用关闭）"
		e.rec.UpdatedAt = now
		changed = true
	}
	if changed {
		m.saveLoggedLocked("pause_for_exit")
	}
}

func (m *cacheQueueManager) pauseForSite(siteID site.ID, reasonCode, reasonMsg string) {
	m.mu.Lock()
	now := m.nowRFC3339()
	changed := false
	var notify []cacheQueueRecord
	for _, e := range m.tasks {
		if e.rec.Site != string(siteID) || !isUnfinishedQueueStatus(e.rec.Status) {
			continue
		}
		if e.rec.Status == CacheQueuePaused && e.rec.PauseReason == "user" {
			continue
		}
		if e.rt.stepCancel != nil {
			e.rt.stepCancel()
			e.rt.stepCancel = nil
		}
		if reasonCode == "app_closed" && e.rec.Status == CacheQueueWaitingBackoff {
			continue
		}
		e.rec.Status = CacheQueuePaused
		e.rec.PauseReason = sanitizeCursor(reasonCode)
		e.rec.StopReason = sanitizeQueueText(reasonMsg)
		if reasonCode != "app_closed" {
			e.rec.NextRunAt = ""
		}
		e.rec.UpdatedAt = now
		e.rt.resumePendingDownloads = len(e.rec.ActiveDownloadIDs) > 0
		changed = true
		notify = append(notify, e.rec)
	}
	if changed {
		m.saveLoggedLocked("pause_for_site")
	}
	m.mu.Unlock()
	m.notifyStates(notify)
	m.wakeSite(siteID)
}

func (m *cacheQueueManager) snapshotAuthPausedIDs(siteID site.ID) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []string
	for _, e := range m.tasks {
		if e.rec.Site == string(siteID) && e.rec.Status == CacheQueuePaused && e.rec.PauseReason == "auth_required" {
			ids = append(ids, e.rec.ID)
		}
	}
	return ids
}

func (m *cacheQueueManager) resumeAuthPausedForSite(siteID site.ID) {
	m.resumeAuthPausedFilter(siteID, nil)
}

func (m *cacheQueueManager) resumeAuthPausedIDs(siteID site.ID, allowed []string) {
	if len(allowed) == 0 {
		return
	}
	m.resumeAuthPausedFilter(siteID, allowed)
}

func (m *cacheQueueManager) resumeAuthPausedFilter(siteID site.ID, onlyIDs []string) {
	m.mu.Lock()
	now := m.nowRFC3339()
	changed := false
	var notify []cacheQueueRecord
	for _, e := range m.tasks {
		if e.rec.Site != string(siteID) || e.rec.Status != CacheQueuePaused || e.rec.PauseReason != "auth_required" {
			continue
		}
		if onlyIDs != nil && !slices.Contains(onlyIDs, e.rec.ID) {
			continue
		}
		e.rec.Status = CacheQueueQueued
		e.rec.PauseReason = ""
		e.rec.StopReason = ""
		e.rec.NextRunAt = ""
		e.rec.UpdatedAt = now
		e.rt.resumePendingDownloads = len(e.rec.ActiveDownloadIDs) > 0
		changed = true
		notify = append(notify, e.rec)
	}
	if changed {
		m.saveLoggedLocked("resume_auth_paused")
	}
	m.mu.Unlock()
	m.notifyStates(notify)
	if changed {
		m.wakeSite(siteID)
	}
}

func (m *cacheQueueManager) hasSiteLogin(siteID site.ID) bool {
	if m.b == nil || m.b.credMgr == nil {
		return false
	}
	return m.b.credMgr.HasActiveCredential(string(siteID))
}

func (m *cacheQueueManager) siteBackoffAndQuotaEnds(siteID site.ID, now time.Time) (inBackoff bool, backoffEnd time.Time, quotaExhausted bool, quotaEnd time.Time) {
	if m.b == nil || m.b.subs == nil {
		return false, time.Time{}, false, time.Time{}
	}
	nowUTC := now.UTC()
	if active, until := m.b.subs.IsSiteInBackoff(string(siteID)); active && until.After(nowUTC) {
		inBackoff = true
		backoffEnd = until.UTC()
	}
	if !m.b.subs.CanUseSiteQuotaTokens(string(siteID), 1) {
		quotaExhausted = true
		st := m.b.subs.GetSiteSchedulerState(string(siteID))
		cutoff := nowUTC.Add(-time.Hour).Unix()
		var oldest int64
		for _, ts := range st.TokenTimestamps {
			if ts > cutoff && (oldest == 0 || ts < oldest) {
				oldest = ts
			}
		}
		quotaEnd = nowUTC.Add(time.Hour)
		if oldest > 0 {
			quotaEnd = time.Unix(oldest+3600, 0).UTC()
		}
	}
	return inBackoff, backoffEnd, quotaExhausted, quotaEnd
}

func (m *cacheQueueManager) siteBackoffOrQuotaEnd(siteID site.ID, now time.Time) (bool, time.Time) {
	inBackoff, backoffEnd, quotaExhausted, quotaEnd := m.siteBackoffAndQuotaEnds(siteID, now)
	if !inBackoff && !quotaExhausted {
		return false, time.Time{}
	}
	end := backoffEnd
	if quotaExhausted && quotaEnd.After(end) {
		end = quotaEnd
	}
	return true, end
}

func (m *cacheQueueManager) backoffResumeTimeLocked(siteID site.ID, kind string, now time.Time) time.Time {
	nowUTC := now.UTC()
	inBackoff, backoffEnd, quotaExhausted, quotaEnd := m.siteBackoffAndQuotaEnds(siteID, nowUTC)
	p := m.pacingForSite(siteID)
	if kind == site.ErrKindQuota && !inBackoff {
		baseEnd := quotaEnd
		if baseEnd.Before(nowUTC) {
			baseEnd = nowUTC
		}
		coolMin, coolMax := p.QuotaCoolMinDelay, p.QuotaCoolMaxDelay
		if coolMin <= 0 && coolMax <= 0 && m.customPacing {
			coolMin, coolMax = p.BackoffCoolMinDelay, p.BackoffCoolMaxDelay
		}
		return baseEnd.Add(m.randDurationLocked(coolMin, coolMax))
	}
	baseEnd := backoffEnd
	if quotaExhausted && quotaEnd.After(baseEnd) {
		baseEnd = quotaEnd
	}
	if baseEnd.Before(nowUTC) {
		baseEnd = nowUTC
	}
	cool := m.randDurationLocked(p.BackoffCoolMinDelay, p.BackoffCoolMaxDelay)
	return baseEnd.Add(cool)
}

func backoffReasonText(kind string) string {
	switch kind {
	case site.ErrKindRiskControl:
		return "遇到站点风控，等待退避结束"
	case site.ErrKindRateLimited:
		return "遇到站点限流，等待退避结束"
	case site.ErrKindQuota:
		return "本小时自动请求额度已用完，等待额度恢复"
	default:
		return "站点处于退避期，等待退避结束"
	}
}

func isBackoffOrQuotaKind(kind string) bool {
	switch kind {
	case site.ErrKindRiskControl, site.ErrKindRateLimited, site.ErrKindQuota, site.ErrKindBackoff:
		return true
	}
	return false
}

func (m *cacheQueueManager) enterWaitingBackoffLocked(e *cacheQueueEntry, siteID site.ID, kind string) cacheQueueRecord {
	if (kind == site.ErrKindRiskControl || kind == site.ErrKindRateLimited) && m.b != nil && m.b.subs != nil {
		if inBackoff, _ := m.b.subs.IsSiteInBackoff(string(siteID)); !inBackoff {
			m.b.subs.RecordSiteAutomaticStop(string(siteID), kind)
		}
	}
	now := m.clock.Now().UTC()
	resumeAt := m.backoffResumeTimeLocked(siteID, kind, now)
	e.rec.Status = CacheQueueWaitingBackoff
	e.rec.PauseReason = ""
	e.rec.StopReason = backoffReasonText(kind)
	e.rec.NextRunAt = resumeAt.Format(time.RFC3339)
	e.rec.UpdatedAt = now.Format(time.RFC3339)
	e.rt.resumePendingDownloads = len(e.rec.ActiveDownloadIDs) > 0
	if m.b != nil {
		level := 0
		if m.b.subs != nil {
			level = m.b.subs.GetSiteSchedulerState(string(siteID)).BackoffLevel
		}
		m.b.logInfo(fmt.Sprintf("cache_queue.backoff site=%s id=%s kind=%s level=%d next=%s",
			siteID, e.rec.ID, kind, level, resumeAt.Format(time.RFC3339)))
	}
	m.saveLoggedLocked("enter_backoff")
	return e.rec
}

func (m *cacheQueueManager) enterWaitingBackoffFromProbeLocked(e *cacheQueueEntry, siteID site.ID, kind string, probeErr error) cacheQueueRecord {
	if (kind == site.ErrKindRiskControl || kind == site.ErrKindRateLimited) && m.b != nil && probeErr != nil {
		m.b.recordManualSiteProbeFailure(siteID, probeErr)
	}
	now := m.clock.Now().UTC()
	resumeAt := m.backoffResumeTimeLocked(siteID, kind, now)
	e.rec.Status = CacheQueueWaitingBackoff
	e.rec.PauseReason = ""
	if kind == site.ErrKindQuota {
		e.rec.StopReason = backoffReasonText(kind)
	} else {
		e.rec.StopReason = "探测仍处于风控或限流中，等待退避结束"
	}
	e.rec.NextRunAt = resumeAt.Format(time.RFC3339)
	e.rec.UpdatedAt = now.Format(time.RFC3339)
	e.rt.resumePendingDownloads = len(e.rec.ActiveDownloadIDs) > 0
	if m.b != nil {
		level := 0
		if m.b.subs != nil {
			level = m.b.subs.GetSiteSchedulerState(string(siteID)).BackoffLevel
		}
		m.b.logInfo(fmt.Sprintf("cache_queue.backoff_probe site=%s id=%s kind=%s level=%d next=%s",
			siteID, e.rec.ID, kind, level, resumeAt.Format(time.RFC3339)))
	}
	m.saveLoggedLocked("enter_backoff_probe")
	return e.rec
}

func (m *cacheQueueManager) selectActiveSiteEntryLocked(siteID site.ID) (*cacheQueueEntry, []cacheQueueRecord) {
	for _, e := range m.tasks {
		if e.rec.Site != string(siteID) {
			continue
		}
		switch e.rec.Status {
		case CacheQueueRunning, CacheQueueWaiting, CacheQueueWaitingBackoff:
			return e, nil
		}
	}
	var notify []cacheQueueRecord
	for _, e := range m.tasks {
		if e.rec.Site != string(siteID) || e.rec.Status != CacheQueueQueued {
			continue
		}
		if !m.hasSiteLogin(siteID) && !isWeiboBackfillRecord(e.rec) {
			e.rec.Status = CacheQueuePaused
			e.rec.PauseReason = "auth_required"
			e.rec.StopReason = "未登录或登录已失效，重新登录后自动继续"
			e.rec.NextRunAt = ""
			e.rec.UpdatedAt = m.nowRFC3339()
			m.saveLoggedLocked("select_auth_pause")
			notify = append(notify, e.rec)
			continue
		}
		if e.rec.RestThreshold <= 0 {
			e.rec.RestThreshold = m.pickRestThresholdForSiteLocked(siteID)
		}
		now := m.clock.Now().UTC()
		if cooldown := m.siteCooldownUntil[siteID]; cooldown.After(now) {
			delete(m.siteCooldownUntil, siteID)
			e.rec.Status = CacheQueueWaiting
			e.rec.PauseReason = ""
			e.rec.StopReason = "等待本站点间隔结束"
			e.rec.NextRunAt = cooldown.Format(time.RFC3339)
			e.rec.UpdatedAt = now.Format(time.RFC3339)
			m.saveLoggedLocked("select_waiting_cooldown")
			notify = append(notify, e.rec)
			return e, notify
		}
		delete(m.siteCooldownUntil, siteID)
		e.rec.Status = CacheQueueRunning
		e.rec.PauseReason = ""
		e.rec.StopReason = ""
		e.rec.NextRunAt = ""
		e.rec.UpdatedAt = m.nowRFC3339()
		m.saveLoggedLocked("select_running")
		notify = append(notify, e.rec)
		return e, notify
	}
	return nil, notify
}

func (m *cacheQueueManager) runSiteLoop(ctx context.Context, siteID site.ID) {
	wakeCh := m.siteWake[siteID]
	for {
		if ctx.Err() != nil {
			return
		}

		m.mu.Lock()
		entry, pendingNotify := m.selectActiveSiteEntryLocked(siteID)
		if entry == nil {
			m.mu.Unlock()
			m.notifyStates(pendingNotify)
			select {
			case <-ctx.Done():
				return
			case <-wakeCh:
				continue
			}
		}
		if len(pendingNotify) > 0 {
			m.mu.Unlock()
			m.notifyStates(pendingNotify)
			continue
		}

		// Phase A: Scheduled wait (inter-item delay, long rest, startup delay, or backoff cooldown).
		if entry.rec.Status == CacheQueueWaiting || entry.rec.Status == CacheQueueWaitingBackoff {
			nextAt, pErr := time.Parse(time.RFC3339, entry.rec.NextRunAt)
			now := m.clock.Now().UTC()
			if pErr == nil && now.Before(nextAt) {
				select {
				case <-wakeCh:
				default:
				}
				waitDur := nextAt.Sub(now)
				tm := m.clock.NewTimer(waitDur)
				m.mu.Unlock()

				select {
				case <-ctx.Done():
					tm.Stop()
					return
				case <-wakeCh:
					tm.Stop()
					continue
				case <-tm.C():
				}
				continue
			}

			isBackfill := isWeiboBackfillRecord(entry.rec)
			if !isBackfill && !m.hasSiteLogin(siteID) {
				m.mu.Unlock()
				m.pauseForSite(siteID, "auth_required", "未登录或登录已失效，重新登录后自动继续")
				continue
			}
			pureCDNWaiting := isBackfill && entry.rec.Status == CacheQueueWaiting &&
				(entry.rec.BackfillPhase == backfillPhaseFastCDN || entry.rec.BackfillPhase == backfillPhaseOfflineLinks)
			if !pureCDNWaiting {
				if blocked, _ := m.siteBackoffOrQuotaEnd(siteID, m.clock.Now()); blocked {
					kind := site.ErrKindBackoff
					if !m.b.subs.CanUseSiteQuotaTokens(string(siteID), 1) {
						kind = site.ErrKindQuota
					}
					rec := m.enterWaitingBackoffLocked(entry, siteID, kind)
					m.mu.Unlock()
					m.notifyState(rec)
					continue
				}
			}
			entry.rec.Status = CacheQueueRunning
			entry.rec.PauseReason = ""
			entry.rec.StopReason = ""
			entry.rec.NextRunAt = ""
			entry.rec.UpdatedAt = m.nowRFC3339()
			m.saveLoggedLocked("wake_running")
			rec := entry.rec
			m.mu.Unlock()
			m.notifyState(rec)
			continue
		}

		if isWeiboBackfillRecord(entry.rec) {
			m.advanceWeiboBackfillStep(ctx, entry)
			continue
		}

		if !m.hasSiteLogin(siteID) {
			m.mu.Unlock()
			m.pauseForSite(siteID, "auth_required", "未登录或登录已失效，重新登录后自动继续")
			continue
		}

		// Phase B: Monitor or resume the single active derived download item.
		if len(entry.rec.ActiveDownloadIDs) > 0 {
			activeIDs := append([]string(nil), entry.rec.ActiveDownloadIDs...)
			activeItemKey := entry.rec.ActiveItemKey
			needResume := entry.rt.resumePendingDownloads
			isProbe := entry.rt.manualProbeOnce
			entry.rt.resumePendingDownloads = false
			m.mu.Unlock()

			if needResume && m.b != nil && m.b.dl != nil {
				for _, dlID := range activeIDs {
					if t, ok, err := m.b.dl.GetTask(dlID); err == nil && ok {
						if isProbe && (t.Status == "paused" || t.Status == "queued" || t.Status == "failed") {
							_, _ = m.b.dl.ResumeManualProbe(dlID)
						} else if t.Status == "paused" {
							_, _ = m.b.dl.Resume(dlID)
						}
					}
				}
			}

			stillRunning := false
			hasActiveExecution := false
			var queuedDLIDs []string
			var authStopped bool
			var backoffKind string
			var appClosed bool
			var userPaused bool
			allSucceeded := true

			if m.b != nil && m.b.dl != nil {
				for _, dlID := range activeIDs {
					t, ok, err := m.b.dl.GetTask(dlID)
					if err != nil || !ok {
						allSucceeded = false
						continue
					}
					switch t.Status {
					case "queued":
						stillRunning = true
						queuedDLIDs = append(queuedDLIDs, dlID)
					case "running", "downloading", "muxing":
						stillRunning = true
						hasActiveExecution = true
					case "completed":
						// Part succeeded.
					case "partial":
						if m.b != nil && m.b.lib != nil {
							checkKey := firstNonEmpty(t.ItemKey, activeItemKey)
							if _, exists, lErr := m.b.lib.GetItem(checkKey); lErr != nil || !exists {
								allSucceeded = false
							}
						}
					case "paused":
						allSucceeded = false
						switch {
						case t.PauseReason == "auth_required" || t.ErrorKind == site.ErrKindAuthRequired:
							authStopped = true
						case t.PauseReason == "risk_control" || isBackoffOrQuotaKind(t.PauseReason) || isBackoffOrQuotaKind(t.ErrorKind):
							backoffKind = firstNonEmpty(t.ErrorKind, t.PauseReason)
						case t.PauseReason == "app_closed":
							appClosed = true
						default:
							userPaused = true
						}
					default: // "failed", "canceled"
						allSucceeded = false
						if t.ErrorKind == site.ErrKindAuthRequired {
							authStopped = true
						} else if isBackoffOrQuotaKind(t.ErrorKind) {
							backoffKind = t.ErrorKind
						}
					}
				}
			}

			// If derived download(s) are still queued (not yet running) while this site is
			// already in backoff, pause those queued tasks for backoff and transition this
			// queue entry to waiting_backoff so the UI stays accurate and other sites run freely.
			// Skip this short-circuit while a one-shot manual probe is in flight for this entry.
			if stillRunning && !hasActiveExecution && len(queuedDLIDs) > 0 && !isProbe {
				if inBackoff, _ := m.siteRiskBackoffOnly(siteID, m.clock.Now()); inBackoff {
					if m.b != nil && m.b.dl != nil {
						for _, dlID := range queuedDLIDs {
							m.b.dl.PauseQueuedForBackoff(dlID, "站点处于退避期，等待退避结束")
						}
					}
					m.mu.Lock()
					var notify *cacheQueueRecord
					if entry.rec.Status == CacheQueueRunning && slices.Equal(entry.rec.ActiveDownloadIDs, activeIDs) {
						rec := m.enterWaitingBackoffLocked(entry, siteID, site.ErrKindBackoff)
						notify = &rec
					}
					m.mu.Unlock()
					if notify != nil {
						m.notifyState(*notify)
					}
					continue
				}
			}

			if stillRunning {
				select {
				case <-ctx.Done():
					return
				case <-wakeCh:
					continue
				}
			}
			if ctx.Err() != nil {
				return
			}
			if appClosed {
				m.mu.Lock()
				var notify *cacheQueueRecord
				if entry.rec.Status == CacheQueueRunning && slices.Equal(entry.rec.ActiveDownloadIDs, activeIDs) {
					entry.rec.Status = CacheQueuePaused
					entry.rec.PauseReason = "app_closed"
					entry.rec.StopReason = "已暂停（应用关闭）"
					entry.rec.UpdatedAt = m.nowRFC3339()
					entry.rt.resumePendingDownloads = true
					m.saveLoggedLocked("pause_app_closed")
					r := entry.rec
					notify = &r
				}
				m.mu.Unlock()
				if notify != nil {
					m.notifyState(*notify)
				}
				continue
			}

			m.mu.Lock()
			if entry.rec.Status != CacheQueueRunning || !slices.Equal(entry.rec.ActiveDownloadIDs, activeIDs) {
				m.mu.Unlock()
				continue
			}
			wasProbe := entry.rt.manualProbeOnce
			entry.rt.manualProbeOnce = false
			if authStopped {
				m.mu.Unlock()
				m.pauseForSite(siteID, "auth_required", "登录已失效，重新登录后自动继续")
				continue
			}
			if backoffKind != "" {
				var rec cacheQueueRecord
				if wasProbe {
					rec = m.enterWaitingBackoffFromProbeLocked(entry, siteID, backoffKind, nil)
				} else {
					rec = m.enterWaitingBackoffLocked(entry, siteID, backoffKind)
				}
				m.mu.Unlock()
				m.notifyState(rec)
				continue
			}
			if userPaused {
				entry.rec.Status = CacheQueuePaused
				entry.rec.PauseReason = "user"
				entry.rec.StopReason = "已手动暂停"
				entry.rec.NextRunAt = ""
				entry.rec.UpdatedAt = m.nowRFC3339()
				entry.rt.resumePendingDownloads = true
				m.saveLoggedLocked("pause_user_dl")
				rec := entry.rec
				m.mu.Unlock()
				m.notifyState(rec)
				continue
			}

			// Reconstruct remaining multi-part video enqueuers across app restarts from persisted page numbers.
			if allSucceeded && len(entry.rt.nextPartEnqueuers) == 0 && len(entry.rec.ActivePendingPages) > 0 && m.b != nil {
				if bvid, ok := bilibiliVideoIDFromItemKey(entry.rec.ActiveItemKey); ok {
					entry.rt.nextPartEnqueuers = m.b.buildBilibiliPartEnqueuers(bvid, entry.rec.ActivePendingPages, queueForMid(entry.rec.User))
				} else {
					entry.rec.ActivePendingPages = nil
				}
			}

			// If this was a multi-part video with remaining parts, enqueue the next part before marking the video done.
			if allSucceeded && len(entry.rt.nextPartEnqueuers) > 0 {
				m.advanceMultiPartLocked(ctx, siteID, entry)
				continue
			}

			rec := m.finishItemOutcomeLocked(entry, allSucceeded)
			m.mu.Unlock()
			if allSucceeded && wasProbe && m.b != nil {
				m.b.onManualSiteRequestSuccess(siteID)
			}
			m.notifyState(rec)
			continue
		}

		// Phase C: Inspect candidates or fetch next page, skipping cached/uncacheable items without waiting.
		m.advanceCandidateStep(ctx, siteID, entry)
	}
}

// advanceMultiPartLocked enqueues remaining parts of a multi-part video one by one.
// Caller holds m.mu on entry; this method unlocks m.mu before returning.
func (m *cacheQueueManager) advanceMultiPartLocked(ctx context.Context, siteID site.ID, entry *cacheQueueEntry) {
	for len(entry.rt.nextPartEnqueuers) > 0 {
		nextPart := entry.rt.nextPartEnqueuers[0]
		stepCtx, stepCancel := context.WithCancel(site.WithAutomaticRequests(ctx, "cache_queue"))
		entry.rt.stepCancel = stepCancel
		m.mu.Unlock()

		pids, pErr := nextPart(stepCtx)

		m.mu.Lock()
		stepCancel()
		if entry.rt.stepCancel != nil {
			entry.rt.stepCancel = nil
		}
		if entry.rec.Status != CacheQueueRunning || ctx.Err() != nil {
			m.mu.Unlock()
			return
		}
		if pErr != nil {
			kind := site.KindOf(pErr)
			if kind == site.ErrKindAuthRequired {
				m.mu.Unlock()
				m.pauseForSite(siteID, "auth_required", "登录已失效，重新登录后自动继续")
				return
			}
			if isBackoffOrQuotaKind(kind) {
				rec := m.enterWaitingBackoffLocked(entry, siteID, kind)
				m.mu.Unlock()
				m.notifyState(rec)
				return
			}
			entry.rt.nextPartEnqueuers = nil
			entry.rec.ActivePendingPages = nil
			rec := m.finishItemOutcomeLocked(entry, false)
			m.mu.Unlock()
			m.notifyState(rec)
			return
		}
		entry.rt.nextPartEnqueuers = entry.rt.nextPartEnqueuers[1:]
		if len(entry.rec.ActivePendingPages) > 0 {
			entry.rec.ActivePendingPages = entry.rec.ActivePendingPages[1:]
		}
		if len(pids) > 0 {
			entry.rec.ActiveDownloadIDs = pids
			entry.rec.UpdatedAt = m.nowRFC3339()
			m.saveLoggedLocked("next_part")
			m.mu.Unlock()
			return
		}
	}
	// Every remaining part was already complete / produced zero download tasks.
	rec := m.finishItemOutcomeLocked(entry, true)
	m.mu.Unlock()
	m.notifyState(rec)
}

func (m *cacheQueueManager) finishItemOutcomeLocked(entry *cacheQueueEntry, cached bool) cacheQueueRecord {
	nowStr := m.nowRFC3339()
	itemKey := entry.rec.ActiveItemKey
	title := entry.rec.ActiveTitle
	entry.rec.ActiveDownloadIDs = nil
	entry.rec.ActivePendingPages = nil
	entry.rec.ActiveItemKey = ""
	entry.rec.ActiveTitle = ""
	entry.rt.nextPartEnqueuers = nil

	if itemKey != "" && !slices.Contains(entry.rec.ProcessedKeys, itemKey) {
		entry.rec.ProcessedKeys = append(entry.rec.ProcessedKeys, itemKey)
	}

	if cached {
		entry.rec.CachedCount++
		entry.rec.SinceRestCount++
		if itemKey != "" {
			if title == "" && m.b != nil && m.b.lib != nil {
				if meta, ok, err := m.b.lib.GetItem(itemKey); err == nil && ok && meta.Title != "" {
					title = trimQueueSummaryTitle(meta.Title)
				}
			}
			summary := CacheQueueRecentItem{
				ItemKey:  itemKey,
				Title:    sanitizeQueueText(firstNonEmpty(title, itemKey)),
				CachedAt: nowStr,
			}
			entry.rec.RecentItems = append([]CacheQueueRecentItem{summary}, entry.rec.RecentItems...)
			if len(entry.rec.RecentItems) > maxCacheQueueRecentItems {
				entry.rec.RecentItems = entry.rec.RecentItems[:maxCacheQueueRecentItems]
			}
		}
	} else {
		entry.rec.SkippedCount++
	}

	// If the current page buffer is exhausted and there are no further pages, complete immediately.
	if entry.rt.pageLoaded && entry.rt.pageIdx >= len(entry.rt.pageItems) && !entry.rt.pageNextHasMore {
		entry.rec.Cursor = sanitizeQueueCursorForSite(site.ID(entry.rec.Site), entry.rt.pageNextCursor)
		entry.rec.HasMore = false
		entry.rec.ProcessedKeys = nil
		if entry.rt.pageCompletionNote != "" {
			entry.rec.Note = sanitizeQueueText(entry.rt.pageCompletionNote)
		}
		entry.rt.pageLoaded = false
		entry.rt.pageItems = nil
		entry.rt.pageIdx = 0
		entry.rt.pageCompletionNote = ""
		entry.rec.Status = CacheQueueCompleted
		entry.rec.PauseReason = ""
		entry.rec.StopReason = ""
		entry.rec.NextRunAt = ""
		entry.rec.UpdatedAt = nowStr
		m.saveLoggedLocked("complete_item")
		return entry.rec
	}

	siteID := site.ID(entry.rec.Site)
	p := m.pacingForSite(siteID)
	zeroAPI := entry.rt.lastItemZeroAPI
	entry.rt.lastItemZeroAPI = false
	if entry.rec.RestThreshold <= 0 {
		entry.rec.RestThreshold = m.pickRestThresholdForSiteLocked(siteID)
	}
	var delay time.Duration
	reason := "等待下一条"
	if cached && entry.rec.SinceRestCount >= entry.rec.RestThreshold {
		delay = m.randDurationLocked(p.LongRestMinDelay, p.LongRestMaxDelay)
		entry.rec.SinceRestCount = 0
		entry.rec.RestThreshold = m.pickRestThresholdForSiteLocked(siteID)
		reason = "已完成一批缓存，长歇等待中"
	} else if zeroAPI && (p.ZeroAPIItemMinDelay > 0 || p.ZeroAPIItemMaxDelay > 0) {
		delay = m.randDurationLocked(p.ZeroAPIItemMinDelay, p.ZeroAPIItemMaxDelay)
	} else {
		delay = m.randDurationLocked(p.ItemMinDelay, p.ItemMaxDelay)
	}
	nextAt := m.clock.Now().UTC().Add(delay)
	if m.hasEarlierRunnableOnSiteLocked(siteID, entry) {
		m.recordSiteCooldownLocked(siteID, nextAt)
		entry.rec.Status = CacheQueueQueued
		entry.rec.PauseReason = ""
		entry.rec.StopReason = ""
		entry.rec.NextRunAt = ""
		entry.rec.UpdatedAt = nowStr
		m.saveLoggedLocked("yield_reordered")
		return entry.rec
	}
	entry.rec.Status = CacheQueueWaiting
	entry.rec.PauseReason = ""
	entry.rec.StopReason = reason
	entry.rec.NextRunAt = nextAt.Format(time.RFC3339)
	entry.rec.UpdatedAt = nowStr
	m.saveLoggedLocked("wait_next")
	return entry.rec
}

func (m *cacheQueueManager) advanceCandidateStep(ctx context.Context, siteID site.ID, entry *cacheQueueEntry) {
	// Caller holds m.mu on entry; this method unlocks m.mu before returning!
	if isSiteBackfillRecord(entry.rec) {
		m.advanceSiteBackfillStep(ctx, entry)
		return
	}
	if entry.rt.pager == nil && m.b != nil {
		pager, err := m.b.buildQueuePager(siteID, entry.rec.List, entry.rec.User, entry.rec.UserName)
		if err != nil {
			entry.rec.Status = CacheQueueStopped
			entry.rec.StopReason = sanitizeQueueText(err.Error())
			entry.rec.UpdatedAt = m.nowRFC3339()
			m.saveLoggedLocked("build_pager_err")
			rec := entry.rec
			m.mu.Unlock()
			m.notifyState(rec)
			return
		}
		entry.rt.pager = pager
	}

	// Exhausted current buffered page? Advance persistent cursor.
	if entry.rt.pageLoaded && entry.rt.pageIdx >= len(entry.rt.pageItems) {
		entry.rec.Cursor = sanitizeQueueCursorForSite(siteID, entry.rt.pageNextCursor)
		entry.rec.HasMore = entry.rt.pageNextHasMore
		entry.rec.ProcessedKeys = nil
		completionNote := entry.rt.pageCompletionNote
		entry.rt.pageLoaded = false
		entry.rt.pageItems = nil
		entry.rt.pageIdx = 0
		entry.rt.pageCompletionNote = ""
		entry.rec.UpdatedAt = m.nowRFC3339()
		if !entry.rec.HasMore {
			if completionNote != "" {
				entry.rec.Note = sanitizeQueueText(completionNote)
			}
			entry.rec.Status = CacheQueueCompleted
			entry.rec.PauseReason = ""
			entry.rec.StopReason = ""
			entry.rec.NextRunAt = ""
			m.saveLoggedLocked("complete_page")
			rec := entry.rec
			m.mu.Unlock()
			m.notifyState(rec)
			return
		}
		m.saveLoggedLocked("advance_cursor")
	}

	// Fetch next page if no buffered page is loaded.
	if !entry.rt.pageLoaded {
		if !entry.rec.HasMore {
			entry.rec.Status = CacheQueueCompleted
			entry.rec.PauseReason = ""
			entry.rec.StopReason = ""
			entry.rec.NextRunAt = ""
			entry.rec.UpdatedAt = m.nowRFC3339()
			m.saveLoggedLocked("complete_no_more")
			rec := entry.rec
			m.mu.Unlock()
			m.notifyState(rec)
			return
		}
		isProbe := entry.rt.manualProbeOnce
		if isProbe {
			entry.rt.manualProbeOnce = false
		} else if blocked, _ := m.siteBackoffOrQuotaEnd(siteID, m.clock.Now()); blocked {
			kind := site.ErrKindBackoff
			if !m.b.subs.CanUseSiteQuotaTokens(string(siteID), 1) {
				kind = site.ErrKindQuota
			}
			rec := m.enterWaitingBackoffLocked(entry, siteID, kind)
			m.mu.Unlock()
			m.notifyState(rec)
			return
		}

		cursor := entry.rec.Cursor
		pager := entry.rt.pager
		baseStepCtx := site.WithAutomaticRequests(ctx, "cache_queue")
		if isProbe {
			baseStepCtx = site.WithManualProbe(ctx)
		}
		stepCtx, stepCancel := context.WithCancel(baseStepCtx)
		stepCtx, stepMeta := withQueueStepMeta(stepCtx)
		entry.rt.stepCancel = stepCancel
		m.mu.Unlock()

		items, nextCursor, nextMore, err := pager(stepCtx, cursor)
		if err == nil && m.b != nil {
			if isProbe {
				m.b.onManualSiteRequestSuccess(siteID)
			} else if siteID == site.Weibo && m.b.subs != nil {
				m.b.subs.RecordSiteAutomaticSuccess(string(site.Weibo))
			}
		}

		m.mu.Lock()
		stepCancel()
		if entry.rt.stepCancel != nil {
			entry.rt.stepCancel = nil
		}
		if entry.rec.Status != CacheQueueRunning || ctx.Err() != nil {
			m.mu.Unlock()
			return
		}
		if err != nil {
			kind := site.KindOf(err)
			if kind == site.ErrKindAuthRequired {
				m.mu.Unlock()
				m.pauseForSite(siteID, "auth_required", "登录已失效，重新登录后自动继续")
				return
			}
			if isBackoffOrQuotaKind(kind) {
				var rec cacheQueueRecord
				if isProbe {
					rec = m.enterWaitingBackoffFromProbeLocked(entry, siteID, kind, err)
				} else {
					rec = m.enterWaitingBackoffLocked(entry, siteID, kind)
				}
				m.mu.Unlock()
				m.notifyState(rec)
				return
			}
			entry.rec.Status = CacheQueueStopped
			entry.rec.StopReason = sanitizeQueueText("读取列表失败：" + err.Error())
			entry.rec.NextRunAt = ""
			entry.rec.UpdatedAt = m.nowRFC3339()
			m.saveLoggedLocked("pager_err")
			rec := entry.rec
			m.mu.Unlock()
			m.notifyState(rec)
			return
		}
		if len(items) == 0 {
			cleanNext := sanitizeQueueCursorForSite(siteID, nextCursor)
			if nextMore && cleanNext != "" && cleanNext != entry.rec.Cursor {
				entry.rec.Cursor = cleanNext
				entry.rec.HasMore = true
				entry.rec.ProcessedKeys = nil
				entry.rec.UpdatedAt = m.nowRFC3339()
				m.saveLoggedLocked("advance_empty_page")
				m.mu.Unlock()
				return
			}
			entry.rec.Cursor = cleanNext
			entry.rec.HasMore = false
			entry.rec.ProcessedKeys = nil
			if stepMeta.completionNote != "" {
				entry.rec.Note = sanitizeQueueText(stepMeta.completionNote)
			}
			entry.rec.Status = CacheQueueCompleted
			entry.rec.PauseReason = ""
			entry.rec.StopReason = ""
			entry.rec.NextRunAt = ""
			entry.rec.UpdatedAt = m.nowRFC3339()
			m.saveLoggedLocked("complete_empty_page")
			rec := entry.rec
			m.mu.Unlock()
			m.notifyState(rec)
			return
		}
		entry.rt.pageItems = items
		entry.rt.pageIdx = 0
		entry.rt.pageNextCursor = nextCursor
		entry.rt.pageNextHasMore = nextMore
		entry.rt.pageCompletionNote = sanitizeQueueText(stepMeta.completionNote)
		entry.rt.pageLoaded = true
	}

	// Scan buffered page items: skip cached/uncacheable items immediately without waiting.
	var keysToCheck []string
	for i := entry.rt.pageIdx; i < len(entry.rt.pageItems); i++ {
		if k := entry.rt.pageItems[i].key; k != "" {
			keysToCheck = append(keysToCheck, k)
		}
	}
	var states map[string]library.DotState
	if m.b != nil && len(keysToCheck) > 0 {
		states = m.b.States(keysToCheck)
	}

	skippedAny := false
	var conflictNotifications []cacheConflictRecord
	var postInspectActions []func()
	publishConflicts := func() {
		if len(postInspectActions) > 0 {
			actions := postInspectActions
			postInspectActions = nil
			for _, fn := range actions {
				fn()
			}
		}
		if m.b == nil || m.b.events == nil || len(conflictNotifications) == 0 {
			return
		}
		if len(conflictNotifications) == 1 {
			c := conflictNotifications[0]
			m.b.events.PublishSite(EventCacheConflictsChanged, c.Site, c.ItemKey)
		} else {
			batchSite := conflictNotifications[0].Site
			for _, c := range conflictNotifications[1:] {
				if c.Site != batchSite {
					batchSite = ""
					break
				}
			}
			m.b.events.PublishSite(EventCacheConflictsChanged, batchSite, fmt.Sprintf("batch:%d", len(conflictNotifications)))
		}
		conflictNotifications = nil
	}

	for entry.rt.pageIdx < len(entry.rt.pageItems) {
		cand := entry.rt.pageItems[entry.rt.pageIdx]
		if cand.key == "" {
			entry.rt.pageIdx++
			entry.rec.SkippedCount++
			skippedAny = true
			continue
		}
		if entry.rt.seenKeys[cand.key] || slices.Contains(entry.rec.ProcessedKeys, cand.key) {
			entry.rt.seenKeys[cand.key] = true
			entry.rt.pageIdx++
			continue
		}

		st := states[cand.key]
		if st.Dot == "ring" || (st.Dot == "yellow" && strings.HasPrefix(st.Label, "已暂停")) {
			entry.rt.seenKeys[cand.key] = true
			entry.rt.pageIdx++
			entry.rec.SkippedCount++
			if !slices.Contains(entry.rec.ProcessedKeys, cand.key) {
				entry.rec.ProcessedKeys = append(entry.rec.ProcessedKeys, cand.key)
			}
			skippedAny = true
			continue
		}

		runEnqueue := cand.enqueue
		handledCached := false
		if cand.inspectCached != nil && m.b != nil && m.b.profile != ProfileRemote && m.b.lib != nil {
			if localMeta, exists := lookupQueueExistingItem(m.b, cand.key); exists {
				if localMeta.IsArchivedSnapshot() {
					entry.rt.seenKeys[cand.key] = true
					entry.rt.pageIdx++
					entry.rec.SkippedCount++
					if !slices.Contains(entry.rec.ProcessedKeys, cand.key) {
						entry.rec.ProcessedKeys = append(entry.rec.ProcessedKeys, cand.key)
					}
					skippedAny = true
					continue
				}
				if localMeta.Status != "failed" && localMeta.Status != "missing_files" && localMeta.Status != "corrupt" {
					dec := cand.inspectCached(localMeta, entry.rec.Origin)
					if dec.postInspect != nil {
						postInspectActions = append(postInspectActions, dec.postInspect)
					}
					switch dec.action {
					case inspectSkipNoOp:
						entry.rt.seenKeys[cand.key] = true
						entry.rt.pageIdx++
						entry.rec.SkippedCount++
						if !slices.Contains(entry.rec.ProcessedKeys, cand.key) {
							entry.rec.ProcessedKeys = append(entry.rec.ProcessedKeys, cand.key)
						}
						skippedAny = true
						continue
					case inspectConflict:
						if m.b.conflicts != nil {
							if saved, changed, cErr := m.b.conflicts.recordOrUpdate(dec.conflict); cErr == nil && changed {
								conflictNotifications = append(conflictNotifications, saved)
							}
						}
						entry.rt.seenKeys[cand.key] = true
						entry.rt.pageIdx++
						entry.rec.SkippedCount++
						if !slices.Contains(entry.rec.ProcessedKeys, cand.key) {
							entry.rec.ProcessedKeys = append(entry.rec.ProcessedKeys, cand.key)
						}
						skippedAny = true
						continue
					case inspectEnqueue, inspectProbePaced:
						handledCached = true
						if dec.enqueue != nil {
							runEnqueue = dec.enqueue
						}
					}
				}
			}
		}

		if !handledCached && (cand.uncacheable || !m.b.queueShouldCache(cand.key, st)) {
			entry.rt.seenKeys[cand.key] = true
			entry.rt.pageIdx++
			entry.rec.SkippedCount++
			if !slices.Contains(entry.rec.ProcessedKeys, cand.key) {
				entry.rec.ProcessedKeys = append(entry.rec.ProcessedKeys, cand.key)
			}
			skippedAny = true
			continue
		}

		// Found an actionable candidate! Check backoff/quota before calling runEnqueue.
		isProbe := entry.rt.manualProbeOnce
		if isProbe {
			entry.rt.manualProbeOnce = false
		} else if blocked, _ := m.siteBackoffOrQuotaEnd(siteID, m.clock.Now()); blocked {
			if skippedAny {
				entry.rec.UpdatedAt = m.nowRFC3339()
			}
			kind := site.ErrKindBackoff
			if !m.b.subs.CanUseSiteQuotaTokens(string(siteID), 1) {
				kind = site.ErrKindQuota
			}
			rec := m.enterWaitingBackoffLocked(entry, siteID, kind)
			m.mu.Unlock()
			publishConflicts()
			m.notifyState(rec)
			return
		}

		baseStepCtx := site.WithAutomaticRequests(ctx, "cache_queue")
		if isProbe {
			baseStepCtx = site.WithManualProbe(ctx)
		}
		stepCtx, stepCancel := context.WithCancel(baseStepCtx)
		entry.rt.stepCancel = stepCancel
		if skippedAny {
			entry.rec.UpdatedAt = m.nowRFC3339()
			m.saveLoggedLocked("skip_candidates")
			skippedAny = false
		}
		m.mu.Unlock()
		publishConflicts()

		enq, err := runEnqueue(stepCtx)
		if err == nil && m.b != nil {
			if isProbe && (!enq.reusedListPlan || len(enq.taskIDs) == 0) {
				m.b.onManualSiteRequestSuccess(siteID)
			} else if !isProbe && siteID == site.Weibo && !enq.reusedListPlan && m.b.subs != nil {
				m.b.subs.RecordSiteAutomaticSuccess(string(site.Weibo))
			}
		}

		m.mu.Lock()
		stepCancel()
		if entry.rt.stepCancel != nil {
			entry.rt.stepCancel = nil
		}
		if entry.rec.Status != CacheQueueRunning || ctx.Err() != nil {
			m.mu.Unlock()
			return
		}
		if err != nil {
			kind := site.KindOf(err)
			if kind == site.ErrKindAuthRequired {
				m.mu.Unlock()
				m.pauseForSite(siteID, "auth_required", "登录已失效，重新登录后自动继续")
				return
			}
			if isBackoffOrQuotaKind(kind) {
				var rec cacheQueueRecord
				if isProbe {
					rec = m.enterWaitingBackoffFromProbeLocked(entry, siteID, kind, err)
				} else {
					rec = m.enterWaitingBackoffLocked(entry, siteID, kind)
				}
				m.mu.Unlock()
				m.notifyState(rec)
				return
			}
			// Ordinary per-item failure (e.g. deleted/unavailable). The attempt already sent
			// site requests, so count it as skipped and keep the normal random gap before the
			// next candidate: a run of failing items must not turn into back-to-back requests.
			entry.rt.seenKeys[cand.key] = true
			entry.rt.pageIdx++
			entry.rt.lastItemZeroAPI = false
			entry.rec.ActiveItemKey = cand.key
			entry.rec.ActiveTitle = ""
			rec := m.finishItemOutcomeLocked(entry, false)
			m.mu.Unlock()
			m.notifyState(rec)
			return
		}

		// Enqueue / probe succeeded!
		entry.rt.seenKeys[cand.key] = true
		entry.rt.pageIdx++
		entry.rt.lastItemZeroAPI = enq.reusedListPlan
		entry.rec.ActiveItemKey = sanitizeItemKey(firstNonEmpty(enq.itemKey, cand.key))
		if enq.probedNoTask {
			entry.rec.ActiveTitle = ""
			rec := m.finishItemOutcomeLocked(entry, false)
			m.mu.Unlock()
			m.notifyState(rec)
			return
		}
		entry.rec.ActiveTitle = sanitizeQueueText(firstNonEmpty(enq.title, cand.title))
		if len(enq.taskIDs) == 0 {
			// Synchronous completion (used by synthetic candidate tests with no async download task).
			rec := m.finishItemOutcomeLocked(entry, true)
			m.mu.Unlock()
			m.notifyState(rec)
			return
		}
		entry.rec.ActiveDownloadIDs = append([]string(nil), enq.taskIDs...)
		entry.rec.ActivePendingPages = append([]int(nil), enq.pendingPages...)
		entry.rt.nextPartEnqueuers = enq.nextParts
		if isProbe && enq.reusedListPlan {
			// List-reused enqueue made 0 network requests; let Phase B run the derived download as the single manual probe.
			entry.rt.manualProbeOnce = true
			entry.rt.resumePendingDownloads = true
		} else {
			entry.rt.resumePendingDownloads = false
		}
		entry.rec.UpdatedAt = m.nowRFC3339()
		m.saveLoggedLocked("enqueued_item")
		rec := entry.rec
		m.mu.Unlock()
		m.notifyState(rec)
		return
	}

	var notify *cacheQueueRecord
	if skippedAny {
		entry.rec.UpdatedAt = m.nowRFC3339()
		m.saveLoggedLocked("skip_page")
		r := entry.rec
		notify = &r
	}
	m.mu.Unlock()
	publishConflicts()
	if notify != nil {
		m.notifyState(*notify)
	}
}

func lookupQueueExistingItem(b *Backend, key string) (library.ItemMetadata, bool) {
	if b == nil || b.lib == nil || key == "" {
		return library.ItemMetadata{}, false
	}
	if meta, ok, err := b.lib.GetItem(key); err == nil && ok {
		return meta, true
	}
	if strings.HasPrefix(key, "douyin:video:") {
		alt := "douyin:dynamic:" + strings.TrimPrefix(key, "douyin:video:")
		if meta, ok, err := b.lib.GetItem(alt); err == nil && ok {
			return meta, true
		}
	} else if strings.HasPrefix(key, "douyin:dynamic:") {
		alt := "douyin:video:" + strings.TrimPrefix(key, "douyin:dynamic:")
		if meta, ok, err := b.lib.GetItem(alt); err == nil && ok {
			return meta, true
		}
	}
	return library.ItemMetadata{}, false
}

// queueShouldCache tells whether a cache queue (全部缓存, 定期全部缓存)
// caches an item of the list: one not cached yet or failed, and one cached
// but incomplete (partial, missing records, pages without a file), whose
// missing parts caching again fills while valid files are kept. Items being
// cached, complete ones, paused tasks (continued from the task page) and
// items that only have a better quality available are skipped.
func (b *Backend) queueShouldCache(key string, st library.DotState) bool {
	switch st.Dot {
	case "ring", "green":
		return false
	case "yellow":
		if strings.HasPrefix(st.Label, "已暂停") || b == nil || b.lib == nil {
			return false
		}
		meta, ok, err := b.lib.GetItem(key)
		if err != nil {
			return false
		}
		// A partial task without an indexed item yet still needs caching.
		return !ok || library.NeedsCompletion(meta)
	}
	return true
}
