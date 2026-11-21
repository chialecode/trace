package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/chialecode/trace/internal/site"
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
	loaded, pager, err := b.buildQueueLoadedAndPager(normSite, normList, normUser, req)
	if err != nil {
		return CacheQueueTask{}, err
	}
	if strings.TrimSpace(req.AvatarURL) != "" {
		b.rememberQueueAvatar(string(normSite), normUser, req.AvatarURL)
	}
	return b.enqueueCacheQueueWithPager(normSite, normList, normUser, req, loaded, pager)
}

// queueListLabel names the list; a run a full-cache rule started says so.
func queueListLabel(rec cacheQueueRecord) string {
	label := cacheQueueListLabel(rec.Site, rec.List)
	if rec.Origin == QueueOriginFullCache {
		return "定期全部缓存 · " + label
	}
	return label
}

func queueOrigin(o string) string {
	if o == QueueOriginFullCache {
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
		RestThreshold: m.pickRestThresholdLocked(),
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
		rt.pageNextCursor = sanitizeQueueCursor(req.Cursor)
		rt.pageNextHasMore = req.HasMore && (normSite == site.Bilibili && normList == "videos" || rt.pageNextCursor != "")
		rt.pageCompletionNote = sanitizeQueueText(req.CompletionNote)
		rt.pageLoaded = true
	} else if cleanCur := sanitizeQueueCursor(req.Cursor); cleanCur != "" {
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

// ResumeCacheQueue resumes a paused background cache queue task on its site.
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
	for _, e := range m.tasks {
		if e.rec.ID == target {
			siteID = site.ID(e.rec.Site)
			found = true
			break
		}
	}
	m.mu.Unlock()
	if !found {
		return CacheQueueTask{}, &site.Error{Kind: site.ErrKindUnavailable, Message: "未找到该缓存队列任务"}
	}

	if err := b.requireSiteLogin(siteID); err != nil {
		return CacheQueueTask{}, err
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
	if entry.rec.Status != CacheQueuePaused {
		dto := m.toDTOLocked(entry)
		m.mu.Unlock()
		return dto, nil
	}
	prevRec := entry.rec
	prevResume := entry.rt.resumePendingDownloads
	entry.rec.Status = CacheQueueQueued
	entry.rec.PauseReason = ""
	entry.rec.StopReason = ""
	entry.rec.NextRunAt = ""
	entry.rec.UpdatedAt = m.nowRFC3339()
	entry.rt.resumePendingDownloads = len(entry.rec.ActiveDownloadIDs) > 0
	if err := m.saveLocked(); err != nil {
		entry.rec = prevRec
		entry.rt.resumePendingDownloads = prevResume
		m.mu.Unlock()
		return CacheQueueTask{}, err
	}
	notifyRec := entry.rec
	dto := m.toDTOLocked(entry)
	m.mu.Unlock()

	m.notifyState(notifyRec)
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
