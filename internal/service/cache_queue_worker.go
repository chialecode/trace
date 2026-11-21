package service

import (
	"context"
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
			if !loggedIn {
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
				startupUntil := now.Add(m.randDurationLocked(m.pacing.StartupMinDelay, m.pacing.StartupMaxDelay))
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

func (m *cacheQueueManager) siteBackoffOrQuotaEnd(siteID site.ID, now time.Time) (bool, time.Time) {
	if m.b == nil || m.b.subs == nil {
		return false, time.Time{}
	}
	nowUTC := now.UTC()
	var blocked bool
	var end time.Time

	if inBackoff, until := m.b.subs.IsSiteInBackoff(string(siteID)); inBackoff && until.After(nowUTC) {
		blocked = true
		end = until.UTC()
	}
	if !m.b.subs.CanUseSiteQuotaTokens(string(siteID), 1) {
		blocked = true
		st := m.b.subs.GetSiteSchedulerState(string(siteID))
		cutoff := nowUTC.Add(-time.Hour).Unix()
		var oldest int64
		for _, ts := range st.TokenTimestamps {
			if ts > cutoff && (oldest == 0 || ts < oldest) {
				oldest = ts
			}
		}
		quotaEnd := nowUTC.Add(time.Hour)
		if oldest > 0 {
			quotaEnd = time.Unix(oldest+3600, 0).UTC()
		}
		if quotaEnd.After(end) {
			end = quotaEnd
		}
	}
	return blocked, end
}

func (m *cacheQueueManager) backoffResumeTimeLocked(siteID site.ID, now time.Time) time.Time {
	nowUTC := now.UTC()
	_, baseEnd := m.siteBackoffOrQuotaEnd(siteID, nowUTC)
	if baseEnd.Before(nowUTC) {
		baseEnd = nowUTC
	}
	cool := m.randDurationLocked(m.pacing.BackoffCoolMinDelay, m.pacing.BackoffCoolMaxDelay)
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
	resumeAt := m.backoffResumeTimeLocked(siteID, now)
	e.rec.Status = CacheQueueWaitingBackoff
	e.rec.PauseReason = ""
	e.rec.StopReason = backoffReasonText(kind)
	e.rec.NextRunAt = resumeAt.Format(time.RFC3339)
	e.rec.UpdatedAt = now.Format(time.RFC3339)
	e.rt.resumePendingDownloads = len(e.rec.ActiveDownloadIDs) > 0
	m.saveLoggedLocked("enter_backoff")
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
		if !m.hasSiteLogin(siteID) {
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
			e.rec.RestThreshold = m.pickRestThresholdLocked()
		}
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

			if !m.hasSiteLogin(siteID) {
				m.mu.Unlock()
				m.pauseForSite(siteID, "auth_required", "未登录或登录已失效，重新登录后自动继续")
				continue
			}
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
			entry.rt.resumePendingDownloads = false
			m.mu.Unlock()

			if needResume && m.b != nil && m.b.dl != nil {
				for _, dlID := range activeIDs {
					if t, ok, err := m.b.dl.GetTask(dlID); err == nil && ok && t.Status == "paused" {
						_, _ = m.b.dl.Resume(dlID)
					}
				}
			}

			stillRunning := false
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
					case "queued", "running", "downloading", "muxing":
						stillRunning = true
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
			if authStopped {
				m.mu.Unlock()
				m.pauseForSite(siteID, "auth_required", "登录已失效，重新登录后自动继续")
				continue
			}
			if backoffKind != "" {
				rec := m.enterWaitingBackoffLocked(entry, siteID, backoffKind)
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
		entry.rec.Cursor = sanitizeQueueCursor(entry.rt.pageNextCursor)
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

	if entry.rec.RestThreshold <= 0 {
		entry.rec.RestThreshold = m.pickRestThresholdLocked()
	}
	var delay time.Duration
	reason := "等待下一条"
	if cached && entry.rec.SinceRestCount >= entry.rec.RestThreshold {
		delay = m.randDurationLocked(m.pacing.LongRestMinDelay, m.pacing.LongRestMaxDelay)
		entry.rec.SinceRestCount = 0
		entry.rec.RestThreshold = m.pickRestThresholdLocked()
		reason = "已完成一批缓存，长歇等待中"
	} else {
		delay = m.randDurationLocked(m.pacing.ItemMinDelay, m.pacing.ItemMaxDelay)
	}
	nextAt := m.clock.Now().UTC().Add(delay)
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
		entry.rec.Cursor = sanitizeQueueCursor(entry.rt.pageNextCursor)
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
		if blocked, _ := m.siteBackoffOrQuotaEnd(siteID, m.clock.Now()); blocked {
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
		stepCtx, stepCancel := context.WithCancel(site.WithAutomaticRequests(ctx, "cache_queue"))
		stepCtx, stepMeta := withQueueStepMeta(stepCtx)
		entry.rt.stepCancel = stepCancel
		m.mu.Unlock()

		items, nextCursor, nextMore, err := pager(stepCtx, cursor)

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
				rec := m.enterWaitingBackoffLocked(entry, siteID, kind)
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
			cleanNext := sanitizeQueueCursor(nextCursor)
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
		if cand.uncacheable || !m.b.queueShouldCache(cand.key, states[cand.key]) {
			entry.rt.seenKeys[cand.key] = true
			entry.rt.pageIdx++
			entry.rec.SkippedCount++
			if !slices.Contains(entry.rec.ProcessedKeys, cand.key) {
				entry.rec.ProcessedKeys = append(entry.rec.ProcessedKeys, cand.key)
			}
			skippedAny = true
			continue
		}

		// Found an uncached candidate! Check backoff/quota before calling cand.enqueue.
		if blocked, _ := m.siteBackoffOrQuotaEnd(siteID, m.clock.Now()); blocked {
			if skippedAny {
				entry.rec.UpdatedAt = m.nowRFC3339()
			}
			kind := site.ErrKindBackoff
			if !m.b.subs.CanUseSiteQuotaTokens(string(siteID), 1) {
				kind = site.ErrKindQuota
			}
			rec := m.enterWaitingBackoffLocked(entry, siteID, kind)
			m.mu.Unlock()
			m.notifyState(rec)
			return
		}

		stepCtx, stepCancel := context.WithCancel(site.WithAutomaticRequests(ctx, "cache_queue"))
		entry.rt.stepCancel = stepCancel
		if skippedAny {
			entry.rec.UpdatedAt = m.nowRFC3339()
			m.saveLoggedLocked("skip_candidates")
			skippedAny = false
		}
		m.mu.Unlock()

		enq, err := cand.enqueue(stepCtx)

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
				rec := m.enterWaitingBackoffLocked(entry, siteID, kind)
				m.mu.Unlock()
				m.notifyState(rec)
				return
			}
			// Ordinary per-item failure (e.g. deleted/unavailable). The attempt already sent
			// site requests, so count it as skipped and keep the normal random gap before the
			// next candidate: a run of failing items must not turn into back-to-back requests.
			entry.rt.seenKeys[cand.key] = true
			entry.rt.pageIdx++
			entry.rec.ActiveItemKey = cand.key
			entry.rec.ActiveTitle = ""
			rec := m.finishItemOutcomeLocked(entry, false)
			m.mu.Unlock()
			m.notifyState(rec)
			return
		}

		// Enqueue succeeded!
		entry.rt.seenKeys[cand.key] = true
		entry.rt.pageIdx++
		entry.rec.ActiveItemKey = sanitizeItemKey(firstNonEmpty(enq.itemKey, cand.key))
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
		entry.rt.resumePendingDownloads = false
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
	if notify != nil {
		m.notifyState(*notify)
	}
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
