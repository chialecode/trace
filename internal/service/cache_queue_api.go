package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/site"
)

// Manual queue reordering directions supported by MoveCacheQueue and MoveDownloadTask.
const (
	QueueMoveTop  = "top"
	QueueMoveUp   = "up"
	QueueMoveDown = "down"
)

// ListCacheQueue returns all background cache queue tasks in queue order.
func (b *Backend) ListCacheQueue() []CacheQueueTask {
	if b.requireAnyLogin() != nil || b.queue == nil {
		return nil
	}
	b.queue.mu.Lock()
	defer b.queue.mu.Unlock()
	out := make([]CacheQueueTask, 0, len(b.queue.tasks))
	for _, e := range b.queue.tasks {
		out = append(out, b.queue.toDTOLocked(e))
	}
	return out
}

// FindCacheQueueTask reports whether (siteID, user, list) already has an unfinished task in the cache queue.
func (b *Backend) FindCacheQueueTask(siteID, user, list string) (CacheQueueTask, bool) {
	if b.requireAnyLogin() != nil || b.queue == nil {
		return CacheQueueTask{}, false
	}
	normSite, normList, normUser, err := normalizeCacheQueueTarget(siteID, list, user)
	if err != nil {
		return CacheQueueTask{}, false
	}
	b.queue.mu.Lock()
	defer b.queue.mu.Unlock()
	for _, e := range b.queue.tasks {
		if e.rec.Site == string(normSite) && e.rec.User == normUser && e.rec.List == normList && isUnfinishedQueueStatus(e.rec.Status) {
			return b.queue.toDTOLocked(e), true
		}
	}
	return CacheQueueTask{}, false
}

// EnqueueCacheQueue creates or deduplicates a background cache queue task for one creator's list.
func (b *Backend) EnqueueCacheQueue(ctx context.Context, req CacheQueueRequest) (CacheQueueTask, error) {
	_ = ctx
	if err := b.requireDesktop(); err != nil {
		return CacheQueueTask{}, err
	}
	normSite, normList, normUser, err := normalizeCacheQueueTarget(req.Site, req.List, req.User)
	if err != nil {
		return CacheQueueTask{}, err
	}
	if err := b.requireSiteLogin(normSite); err != nil {
		return CacheQueueTask{}, err
	}
	loaded, pager, fallbackRefetch, err := b.buildQueueLoadedAndPager(normSite, normList, normUser, req)
	if err != nil {
		return CacheQueueTask{}, err
	}
	if fallbackRefetch {
		req.Cursor = ""
		req.HasMore = true
		req.CompletionNote = ""
		loaded = nil
	}
	if strings.TrimSpace(req.AvatarURL) != "" {
		b.rememberQueueAvatar(string(normSite), normUser, req.AvatarURL)
	}
	return b.enqueueCacheQueueWithPager(normSite, normList, normUser, req, loaded, pager)
}

// queueListLabel names the list; a run a full-cache rule started says so.
func queueListLabel(rec cacheQueueRecord) string {
	if rec.List == CacheQueueListWeiboBackfill || rec.List == CacheQueueListSiteBackfill {
		return "历史缓存补全"
	}
	label := cacheQueueListLabel(rec.Site, rec.List)
	if rec.Origin == QueueOriginFullCache {
		return "定期全部缓存 · " + label
	}
	return label
}

func queueOrigin(o string) string {
	if o == QueueOriginFullCache || o == CacheQueueOriginBackfill {
		return o
	}
	return ""
}

func (b *Backend) enqueueCacheQueueWithPager(
	normSite site.ID,
	normList, normUser string,
	req CacheQueueRequest,
	loaded []queueCandidate,
	pager queuePager,
) (CacheQueueTask, error) {
	if b.queue == nil {
		return CacheQueueTask{}, fmt.Errorf("service: cache queue not initialized")
	}
	m := b.queue
	m.mu.Lock()

	now := m.nowRFC3339()
	avatarKey := avatarFileName(string(normSite), normUser)
	cleanName := sanitizeQueueText(req.UserName)

	// 1. Deduplicate if an unfinished task already exists for (site, user, list).
	for _, e := range m.tasks {
		if e.rec.Site == string(normSite) && e.rec.User == normUser && e.rec.List == normList && isUnfinishedQueueStatus(e.rec.Status) {
			changed := false
			if cleanName != "" && e.rec.UserName != cleanName {
				e.rec.UserName = cleanName
				changed = true
			}
			if avatarKey != "" && e.rec.AvatarKey != avatarKey {
				e.rec.AvatarKey = avatarKey
				changed = true
			}
			if changed {
				e.rec.UpdatedAt = now
				m.saveLoggedLocked("dedup_update")
			}
			dto := m.toDTOLocked(e)
			m.mu.Unlock()
			return dto, nil
		}
	}

	// 2. Remove any previous finished (completed/stopped) entry for the same (site, user, list) so restarting keeps one clean entry.
	prevTasks := append([]*cacheQueueEntry(nil), m.tasks...)
	createdAt := now
	taskID := makeCacheQueueID(string(normSite), normList, normUser)
	for i, e := range m.tasks {
		if e.rec.Site == string(normSite) && e.rec.User == normUser && e.rec.List == normList {
			if cleanName == "" {
				cleanName = e.rec.UserName
			}
			taskID = e.rec.ID
			m.tasks = append(m.tasks[:i], m.tasks[i+1:]...)
			break
		}
	}

	rec := cacheQueueRecord{
		ID:            taskID,
		Site:          string(normSite),
		User:          normUser,
		UserName:      cleanName,
		AvatarKey:     avatarKey,
		List:          normList,
		Origin:        queueOrigin(req.Origin),
		Cursor:        "",
		HasMore:       true,
		RestThreshold: m.pickRestThresholdForSiteLocked(normSite),
		Status:        CacheQueueQueued,
		CreatedAt:     createdAt,
		UpdatedAt:     now,
	}
	rt := cacheQueueRuntime{
		pager:    pager,
		seenKeys: make(map[string]bool),
	}
	if len(loaded) > 0 {
		rt.pageItems = loaded
		rt.pageIdx = 0
		rt.pageNextCursor = sanitizeQueueCursorForSite(normSite, req.Cursor)
		rt.pageNextHasMore = req.HasMore && (normSite == site.Bilibili && normList == "videos" || rt.pageNextCursor != "")
		rt.pageCompletionNote = sanitizeQueueText(req.CompletionNote)
		rt.pageLoaded = true
	} else if cleanCur := sanitizeQueueCursorForSite(normSite, req.Cursor); cleanCur != "" {
		rec.Cursor = cleanCur
		rec.HasMore = req.HasMore
	}

	entry := &cacheQueueEntry{rec: rec, rt: rt}
	m.tasks = append(m.tasks, entry)
	if err := m.saveLocked(); err != nil {
		m.tasks = prevTasks
		m.mu.Unlock()
		return CacheQueueTask{}, err
	}
	notifyRec := entry.rec
	dto := m.toDTOLocked(entry)
	m.mu.Unlock()

	m.notifyState(notifyRec)
	m.wakeSite(normSite)
	return dto, nil
}

// PauseCacheQueue pauses an unfinished background cache queue task and any derived download task in flight.
func (b *Backend) PauseCacheQueue(id string) (CacheQueueTask, error) {
	if err := b.requireAnyLogin(); err != nil {
		return CacheQueueTask{}, err
	}
	if b.queue == nil {
		return CacheQueueTask{}, &site.Error{Kind: site.ErrKindUnavailable, Message: "未找到该缓存队列任务"}
	}
	target := strings.TrimSpace(id)
	m := b.queue
	m.mu.Lock()
	var found *cacheQueueEntry
	for _, e := range m.tasks {
		if e.rec.ID == target {
			found = e
			break
		}
	}
	if found == nil {
		m.mu.Unlock()
		return CacheQueueTask{}, &site.Error{Kind: site.ErrKindUnavailable, Message: "未找到该缓存队列任务"}
	}
	if !isUnfinishedQueueStatus(found.rec.Status) {
		m.mu.Unlock()
		return CacheQueueTask{}, &site.Error{Kind: site.ErrKindBadRequest, Message: "该缓存队列任务已结束"}
	}
	prevRec := found.rec
	prevResume := found.rt.resumePendingDownloads
	activeIDs := append([]string(nil), found.rec.ActiveDownloadIDs...)
	found.rec.Status = CacheQueuePaused
	found.rec.PauseReason = "user"
	found.rec.StopReason = "已手动暂停"
	found.rec.NextRunAt = ""
	found.rec.UpdatedAt = m.nowRFC3339()
	found.rt.resumePendingDownloads = len(activeIDs) > 0
	if err := m.saveLocked(); err != nil {
		found.rec = prevRec
		found.rt.resumePendingDownloads = prevResume
		m.mu.Unlock()
		return CacheQueueTask{}, err
	}
	if found.rt.stepCancel != nil {
		found.rt.stepCancel()
		found.rt.stepCancel = nil
	}
	notifyRec := found.rec
	dto := m.toDTOLocked(found)
	siteID := site.ID(found.rec.Site)
	m.mu.Unlock()

	m.notifyState(notifyRec)
	if b.dl != nil {
		for _, dlID := range activeIDs {
			if t, ok, err := b.dl.GetTask(dlID); err == nil && ok && (t.Status == "queued" || t.Status == "running") {
				_, _ = b.dl.Pause(dlID)
			}
		}
	}
	m.wakeSite(siteID)
	return dto, nil
}

// MoveCacheQueue manually reorders an unfinished background cache queue task
// within its owning site ("top", "up", or "down"), persisting cache-queue.json
// (v3) atomically without bypassing per-site pacing, cooldown, or backoff (DEC-088).
func (b *Backend) MoveCacheQueue(id, direction string) (CacheQueueTask, error) {
	if err := b.requireAnyLogin(); err != nil {
		return CacheQueueTask{}, err
	}
	if b.queue == nil {
		return CacheQueueTask{}, &site.Error{Kind: site.ErrKindUnavailable, Message: "未找到该缓存队列任务"}
	}
	target := strings.TrimSpace(id)
	dir := strings.ToLower(strings.TrimSpace(direction))
	switch dir {
	case QueueMoveTop, QueueMoveUp, QueueMoveDown:
	default:
		return CacheQueueTask{}, &site.Error{Kind: site.ErrKindBadRequest, Message: "不支持的排序方向"}
	}

	m := b.queue
	m.mu.Lock()
	var found *cacheQueueEntry
	for _, e := range m.tasks {
		if e.rec.ID == target {
			found = e
			break
		}
	}
	if found == nil {
		m.mu.Unlock()
		return CacheQueueTask{}, &site.Error{Kind: site.ErrKindUnavailable, Message: "未找到该缓存队列任务"}
	}
	if !isUnfinishedQueueStatus(found.rec.Status) {
		m.mu.Unlock()
		return CacheQueueTask{}, &site.Error{Kind: site.ErrKindBadRequest, Message: "已结束的缓存队列任务不能调整顺序"}
	}

	targetSite := site.ID(found.rec.Site)
	var siteIndices []int
	pos := -1
	for i, e := range m.tasks {
		if e.rec.Site == found.rec.Site && isUnfinishedQueueStatus(e.rec.Status) {
			if e == found {
				pos = len(siteIndices)
			}
			siteIndices = append(siteIndices, i)
		}
	}
	if pos < 0 || len(siteIndices) < 2 {
		dto := m.toDTOLocked(found)
		m.mu.Unlock()
		return dto, nil
	}

	type entrySnapshot struct {
		entry  *cacheQueueEntry
		rec    cacheQueueRecord
		resume bool
	}
	prevTasks := append([]*cacheQueueEntry(nil), m.tasks...)
	prevCooldown := m.siteCooldownUntil[targetSite]
	snaps := make([]entrySnapshot, len(siteIndices))
	for i, idx := range siteIndices {
		e := m.tasks[idx]
		snaps[i] = entrySnapshot{
			entry:  e,
			rec:    e.rec,
			resume: e.rt.resumePendingDownloads,
		}
	}

	moved := false
	switch dir {
	case QueueMoveTop:
		if pos > 0 {
			picked := m.tasks[siteIndices[pos]]
			for k := pos; k > 0; k-- {
				m.tasks[siteIndices[k]] = m.tasks[siteIndices[k-1]]
			}
			m.tasks[siteIndices[0]] = picked
			moved = true
		}
	case QueueMoveUp:
		if pos > 0 {
			iPrev, iCur := siteIndices[pos-1], siteIndices[pos]
			m.tasks[iPrev], m.tasks[iCur] = m.tasks[iCur], m.tasks[iPrev]
			moved = true
		}
	case QueueMoveDown:
		if pos < len(siteIndices)-1 {
			iCur, iNext := siteIndices[pos], siteIndices[pos+1]
			m.tasks[iCur], m.tasks[iNext] = m.tasks[iNext], m.tasks[iCur]
			moved = true
		}
	}
	if !moved {
		dto := m.toDTOLocked(found)
		m.mu.Unlock()
		return dto, nil
	}

	nowStr := m.nowRFC3339()
	found.rec.UpdatedAt = nowStr
	notify := m.reconcileSiteOrderLocked(targetSite)
	if len(notify) == 0 {
		notify = []cacheQueueRecord{found.rec}
	}
	if err := m.saveLocked(); err != nil {
		m.tasks = prevTasks
		if prevCooldown.IsZero() {
			delete(m.siteCooldownUntil, targetSite)
		} else {
			m.siteCooldownUntil[targetSite] = prevCooldown
		}
		for _, s := range snaps {
			s.entry.rec = s.rec
			s.entry.rt.resumePendingDownloads = s.resume
		}
		m.mu.Unlock()
		return CacheQueueTask{}, err
	}
	dto := m.toDTOLocked(found)
	m.mu.Unlock()

	m.notifyStates(notify)
	m.wakeSite(targetSite)
	return dto, nil
}

func isRunnableQueueStatus(status string) bool {
	switch status {
	case CacheQueueQueued, CacheQueueRunning, CacheQueueWaiting, CacheQueueWaitingBackoff:
		return true
	default:
		return false
	}
}

func (m *cacheQueueManager) recordSiteCooldownLocked(siteID site.ID, nextAt time.Time) {
	if nextAt.IsZero() {
		return
	}
	utc := nextAt.UTC()
	if cur, ok := m.siteCooldownUntil[siteID]; !ok || utc.After(cur) {
		m.siteCooldownUntil[siteID] = utc
	}
}

func (m *cacheQueueManager) hasEarlierRunnableOnSiteLocked(siteID site.ID, target *cacheQueueEntry) bool {
	for _, e := range m.tasks {
		if e == target {
			return false
		}
		if e.rec.Site == string(siteID) && isRunnableQueueStatus(e.rec.Status) {
			return true
		}
	}
	return false
}

// reconcileSiteOrderLocked aligns a site's active slot after manual reordering:
//   - Never interrupts an in-flight CacheQueueRunning entry (it yields cleanly in
//     finishItemOutcomeLocked once its current item finishes).
//   - When a waiting / waiting_backoff entry now sits behind an earlier runnable
//     entry on the same site, demotes the waiting entry back to queued (preserving
//     all progress and cursor) and carries forward any remaining pacing cooldown so
//     reordering never bypasses site rate limits.
func (m *cacheQueueManager) reconcileSiteOrderLocked(siteID site.ID) []cacheQueueRecord {
	var (
		firstRunnable *cacheQueueEntry
		activeEntry   *cacheQueueEntry
	)
	for _, e := range m.tasks {
		if e.rec.Site != string(siteID) {
			continue
		}
		if firstRunnable == nil && isRunnableQueueStatus(e.rec.Status) {
			firstRunnable = e
		}
		if activeEntry == nil {
			switch e.rec.Status {
			case CacheQueueRunning, CacheQueueWaiting, CacheQueueWaitingBackoff:
				activeEntry = e
			}
		}
	}
	if activeEntry == nil || firstRunnable == nil || activeEntry == firstRunnable {
		return nil
	}
	if activeEntry.rec.Status != CacheQueueWaiting && activeEntry.rec.Status != CacheQueueWaitingBackoff {
		return nil
	}

	now := m.clock.Now().UTC()
	nowStr := now.Format(time.RFC3339)
	prevStatus := activeEntry.rec.Status
	prevStopReason := activeEntry.rec.StopReason
	if activeEntry.rec.NextRunAt != "" {
		if nextAt, err := time.Parse(time.RFC3339, activeEntry.rec.NextRunAt); err == nil && nextAt.After(now) {
			m.recordSiteCooldownLocked(siteID, nextAt)
		}
	}
	activeEntry.rec.Status = CacheQueueQueued
	activeEntry.rec.PauseReason = ""
	activeEntry.rec.StopReason = ""
	activeEntry.rec.NextRunAt = ""
	activeEntry.rec.UpdatedAt = nowStr
	activeEntry.rt.resumePendingDownloads = len(activeEntry.rec.ActiveDownloadIDs) > 0

	notify := []cacheQueueRecord{activeEntry.rec}
	if firstRunnable.rec.Status == CacheQueueQueued {
		if cooldown := m.siteCooldownUntil[siteID]; cooldown.After(now) {
			delete(m.siteCooldownUntil, siteID)
			if firstRunnable.rec.RestThreshold <= 0 {
				firstRunnable.rec.RestThreshold = m.pickRestThresholdForSiteLocked(siteID)
			}
			firstRunnable.rec.PauseReason = ""
			firstRunnable.rec.NextRunAt = cooldown.Format(time.RFC3339)
			firstRunnable.rec.UpdatedAt = nowStr
			if prevStatus == CacheQueueWaitingBackoff {
				firstRunnable.rec.Status = CacheQueueWaitingBackoff
				firstRunnable.rec.StopReason = firstNonEmpty(prevStopReason, "站点处于退避期，等待退避结束")
			} else {
				firstRunnable.rec.Status = CacheQueueWaiting
				firstRunnable.rec.StopReason = "已优先排至下一位，等待本站点间隔结束"
			}
			notify = append(notify, firstRunnable.rec)
		}
	}
	return notify
}

// ResumeCacheQueue resumes a paused background cache queue task on its site,
// or triggers a one-shot manual probe on a waiting_backoff task (DEC-089).
func (b *Backend) ResumeCacheQueue(id string) (CacheQueueTask, error) {
	if err := b.requireDesktop(); err != nil {
		return CacheQueueTask{}, err
	}
	if err := b.requireAnyLogin(); err != nil {
		return CacheQueueTask{}, err
	}
	if b.queue == nil {
		return CacheQueueTask{}, &site.Error{Kind: site.ErrKindUnavailable, Message: "未找到该缓存队列任务"}
	}
	target := strings.TrimSpace(id)
	m := b.queue
	m.mu.Lock()
	var siteID site.ID
	var found bool
	var needLoginCheck bool
	for _, e := range m.tasks {
		if e.rec.ID == target {
			siteID = site.ID(e.rec.Site)
			found = true
			needLoginCheck = !isWeiboBackfillRecord(e.rec) || e.rec.PauseReason == "auth_required"
			break
		}
	}
	m.mu.Unlock()
	if !found {
		return CacheQueueTask{}, &site.Error{Kind: site.ErrKindUnavailable, Message: "未找到该缓存队列任务"}
	}

	if needLoginCheck {
		if err := b.requireSiteLogin(siteID); err != nil {
			return CacheQueueTask{}, err
		}
	}

	m.mu.Lock()
	var entry *cacheQueueEntry
	for _, e := range m.tasks {
		if e.rec.ID == target {
			entry = e
			break
		}
	}
	if entry == nil {
		m.mu.Unlock()
		return CacheQueueTask{}, &site.Error{Kind: site.ErrKindUnavailable, Message: "未找到该缓存队列任务"}
	}
	if !isUnfinishedQueueStatus(entry.rec.Status) {
		m.mu.Unlock()
		return CacheQueueTask{}, &site.Error{Kind: site.ErrKindBadRequest, Message: "该缓存队列任务已结束，请重新加入队列"}
	}
	if entry.rec.Status != CacheQueuePaused && entry.rec.Status != CacheQueueWaitingBackoff {
		dto := m.toDTOLocked(entry)
		m.mu.Unlock()
		return dto, nil
	}

	type entrySnapshot struct {
		entry       *cacheQueueEntry
		rec         cacheQueueRecord
		resume      bool
		manualProbe bool
	}
	prevCooldown := m.siteCooldownUntil[siteID]
	var snaps []entrySnapshot
	for _, e := range m.tasks {
		if e.rec.Site == string(siteID) && isUnfinishedQueueStatus(e.rec.Status) {
			snaps = append(snaps, entrySnapshot{
				entry:       e,
				rec:         e.rec,
				resume:      e.rt.resumePendingDownloads,
				manualProbe: e.rt.manualProbeOnce,
			})
		}
	}

	wasWaitingBackoff := entry.rec.Status == CacheQueueWaitingBackoff
	var reconciled []cacheQueueRecord
	if wasWaitingBackoff {
		// One-shot manual probe: immediately wake this waiting_backoff task to send
		// a single live probe request while keeping background automation backed off.
		delete(m.siteCooldownUntil, siteID)
		entry.rec.Status = CacheQueueRunning
		entry.rec.PauseReason = ""
		entry.rec.StopReason = ""
		entry.rec.NextRunAt = ""
		entry.rec.UpdatedAt = m.nowRFC3339()
		entry.rt.resumePendingDownloads = len(entry.rec.ActiveDownloadIDs) > 0
		entry.rt.manualProbeOnce = true
	} else {
		entry.rec.Status = CacheQueueQueued
		entry.rec.PauseReason = ""
		entry.rec.StopReason = ""
		entry.rec.NextRunAt = ""
		entry.rec.UpdatedAt = m.nowRFC3339()
		entry.rt.resumePendingDownloads = len(entry.rec.ActiveDownloadIDs) > 0
		reconciled = m.reconcileSiteOrderLocked(siteID)
	}
	if err := m.saveLocked(); err != nil {
		if prevCooldown.IsZero() {
			delete(m.siteCooldownUntil, siteID)
		} else {
			m.siteCooldownUntil[siteID] = prevCooldown
		}
		for _, s := range snaps {
			s.entry.rec = s.rec
			s.entry.rt.resumePendingDownloads = s.resume
			s.entry.rt.manualProbeOnce = s.manualProbe
		}
		m.mu.Unlock()
		return CacheQueueTask{}, err
	}

	notify := make([]cacheQueueRecord, 0, len(reconciled)+1)
	seenSelf := false
	for _, rec := range reconciled {
		if rec.ID == entry.rec.ID {
			seenSelf = true
		}
		notify = append(notify, rec)
	}
	if !seenSelf {
		notify = append(notify, entry.rec)
	}
	dto := m.toDTOLocked(entry)
	m.mu.Unlock()

	m.notifyStates(notify)
	m.wakeSite(siteID)
	return dto, nil
}

// CancelCacheQueue stops scheduling a background cache queue task and cancels its single unfinished
// derived download task (if any) without ever deleting items already cached in the local media library.
func (b *Backend) CancelCacheQueue(id string) (CacheQueueTask, error) {
	if err := b.requireAnyLogin(); err != nil {
		return CacheQueueTask{}, err
	}
	if b.queue == nil {
		return CacheQueueTask{}, &site.Error{Kind: site.ErrKindUnavailable, Message: "未找到该缓存队列任务"}
	}
	target := strings.TrimSpace(id)
	m := b.queue
	m.mu.Lock()
	var found *cacheQueueEntry
	for _, e := range m.tasks {
		if e.rec.ID == target {
			found = e
			break
		}
	}
	if found == nil {
		m.mu.Unlock()
		return CacheQueueTask{}, &site.Error{Kind: site.ErrKindUnavailable, Message: "未找到该缓存队列任务"}
	}
	if !isUnfinishedQueueStatus(found.rec.Status) {
		dto := m.toDTOLocked(found)
		m.mu.Unlock()
		return dto, nil
	}
	prevRec := found.rec
	prevParts := found.rt.nextPartEnqueuers
	prevResume := found.rt.resumePendingDownloads
	activeIDs := append([]string(nil), found.rec.ActiveDownloadIDs...)
	siteID := site.ID(found.rec.Site)
	found.rt.nextPartEnqueuers = nil
	found.rt.resumePendingDownloads = false
	found.rec.ActiveDownloadIDs = nil
	found.rec.ActivePendingPages = nil
	found.rec.ActiveItemKey = ""
	found.rec.ActiveTitle = ""
	found.rec.Status = CacheQueueStopped
	found.rec.PauseReason = ""
	found.rec.StopReason = "已取消"
	found.rec.NextRunAt = ""
	found.rec.UpdatedAt = m.nowRFC3339()
	if err := m.saveLocked(); err != nil {
		found.rec = prevRec
		found.rt.nextPartEnqueuers = prevParts
		found.rt.resumePendingDownloads = prevResume
		m.mu.Unlock()
		return CacheQueueTask{}, err
	}
	if found.rt.stepCancel != nil {
		found.rt.stepCancel()
		found.rt.stepCancel = nil
	}
	notifyRec := found.rec
	dto := m.toDTOLocked(found)
	m.mu.Unlock()

	m.notifyState(notifyRec)
	if b.dl != nil {
		for _, dlID := range activeIDs {
			if t, ok, err := b.dl.GetTask(dlID); err == nil && ok && t.Status != "completed" && t.Status != "partial" && t.Status != "canceled" {
				_, _ = b.dl.Cancel(dlID)
			}
		}
	}
	b.pruneAccountAvatars()
	m.wakeSite(siteID)
	return dto, nil
}

// RemoveCacheQueue deletes a finished (completed, canceled or stopped) cache
// queue task from the task list. Unfinished tasks must be canceled first.
// Items it cached stay in the media library.
func (b *Backend) RemoveCacheQueue(id string) error {
	_, err := b.removeCacheQueues(func(rec cacheQueueRecord) bool { return rec.ID == strings.TrimSpace(id) }, true)
	return err
}

// RemoveFinishedCacheQueues deletes every finished cache queue task and
// returns how many were removed.
func (b *Backend) RemoveFinishedCacheQueues() (int, error) {
	return b.removeCacheQueues(func(rec cacheQueueRecord) bool { return !isUnfinishedQueueStatus(rec.Status) }, false)
}

func (b *Backend) removeCacheQueues(match func(cacheQueueRecord) bool, single bool) (int, error) {
	if b.queue == nil {
		if single {
			return 0, &site.Error{Kind: site.ErrKindUnavailable, Message: "未找到该缓存队列任务"}
		}
		return 0, nil
	}
	m := b.queue
	m.mu.Lock()
	kept := make([]*cacheQueueEntry, 0, len(m.tasks))
	var removed []cacheQueueRecord
	for _, e := range m.tasks {
		if !match(e.rec) {
			kept = append(kept, e)
			continue
		}
		if isUnfinishedQueueStatus(e.rec.Status) {
			if single {
				m.mu.Unlock()
				return 0, &site.Error{Kind: site.ErrKindBadRequest, Message: "缓存队列还在进行，请先取消再移除"}
			}
			kept = append(kept, e)
			continue
		}
		rec := e.rec
		rec.Status = "removed"
		removed = append(removed, rec)
	}
	if len(removed) == 0 {
		m.mu.Unlock()
		if single {
			return 0, &site.Error{Kind: site.ErrKindUnavailable, Message: "未找到该缓存队列任务"}
		}
		return 0, nil
	}
	prev := m.tasks
	m.tasks = kept
	if err := m.saveLocked(); err != nil {
		m.tasks = prev
		m.mu.Unlock()
		return 0, err
	}
	m.mu.Unlock()

	m.notifyStates(removed)
	b.pruneAccountAvatars()
	return len(removed), nil
}

func (b *Backend) snapshotAuthPausedQueueIDs(siteID site.ID) []string {
	if b.queue == nil {
		return nil
	}
	return b.queue.snapshotAuthPausedIDs(siteID)
}

func (b *Backend) resumeAuthPausedQueueIDs(siteID site.ID, ids []string) {
	if b.queue != nil {
		b.queue.resumeAuthPausedIDs(siteID, ids)
	}
}
