package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/site/weibo"
)

type backfillMatchedItem struct {
	itemKey     string
	title       string
	overrides   []download.WeiboBackfillSlotOverride
	remoteLinks []TextLinkDTO
}

type weiboBackfillBuckets struct {
	offlineLinkOnly []library.ItemMetadata
	fastCDN         []library.ItemMetadata
	subscribedByUID map[string][]library.ItemMetadata
	subscribedUIDs  []string
	fallbackDetail  []library.ItemMetadata
	summary         BackfillSummaryDTO
}

func weiboNeedsLegacyLinkUpgrade(meta library.ItemMetadata) bool {
	if !strings.EqualFold(strings.TrimSpace(meta.Site), string(site.Weibo)) || meta.IsArchivedSnapshot() {
		return false
	}
	if meta.Status != "complete" && meta.Status != "partial" {
		return false
	}
	_, hadLegacy := cleanLegacyInlineLinksInItem(meta, nil)
	return hadLegacy
}

func (b *Backend) subscribedWeiboUIDSet() map[string]bool {
	out := make(map[string]bool)
	if b == nil || b.subs == nil {
		return out
	}
	subs, err := b.subs.List()
	if err != nil {
		return out
	}
	for _, sub := range subs {
		for _, acc := range sub.Accounts {
			if strings.EqualFold(strings.TrimSpace(acc.Site), string(site.Weibo)) {
				if norm, uErr := site.NormalizeUID(site.Weibo, acc.UID); uErr == nil && norm != "" {
					out[norm] = true
				}
			}
		}
	}
	return out
}

func (b *Backend) scanWeiboBackfillBuckets(excludeKeys map[string]bool) (weiboBackfillBuckets, error) {
	var out weiboBackfillBuckets
	out.subscribedByUID = make(map[string][]library.ItemMetadata)
	if b == nil || b.profile == ProfileRemote || b.lib == nil {
		return out, nil
	}
	items, err := b.lib.ListItemsMetaOnlyBySite(string(site.Weibo))
	if err != nil {
		return out, err
	}
	removeWM := b.weiboRemoveWatermarkEnabled()
	subUIDs := b.subscribedWeiboUIDSet()

	for _, meta := range items {
		if meta.IsArchivedSnapshot() || (meta.Status != "complete" && meta.Status != "partial") {
			continue
		}
		if len(excludeKeys) > 0 && excludeKeys[meta.ItemKey] {
			continue
		}
		missingNoWM := library.WeiboNeedsNoWMBackfill(meta, removeWM)
		legacyLinks := weiboNeedsLegacyLinkUpgrade(meta)
		if !missingNoWM && !legacyLinks {
			continue
		}
		out.summary.TotalItems++
		if missingNoWM {
			out.summary.WeiboMissingNoWM++
		}
		if legacyLinks {
			out.summary.WeiboLegacyLinks++
		}

		if !missingNoWM {
			out.offlineLinkOnly = append(out.offlineLinkOnly, meta)
			continue
		}
		if library.WeiboHasAllFastCDNPicIDs(meta) {
			out.summary.WeiboFastCDNCandidates++
			out.fastCDN = append(out.fastCDN, meta)
			continue
		}
		authorUID, uErr := site.NormalizeUID(site.Weibo, itemAuthorUIDString(meta))
		if !library.WeiboIsForwardItem(meta) && uErr == nil && subUIDs[authorUID] {
			out.summary.WeiboSubscribedCount++
			out.subscribedByUID[authorUID] = append(out.subscribedByUID[authorUID], meta)
		} else {
			out.summary.WeiboFallbackDetailCount++
			out.fallbackDetail = append(out.fallbackDetail, meta)
		}
	}
	if len(out.subscribedByUID) > 0 {
		out.subscribedUIDs = make([]string, 0, len(out.subscribedByUID))
		for uid := range out.subscribedByUID {
			out.subscribedUIDs = append(out.subscribedUIDs, uid)
		}
		sort.Strings(out.subscribedUIDs)
	}
	return out, nil
}

// BackfillSummary computes offline statistics of historical cache items eligible
// for standard upgrades (missing Weibo /oslarge/ companions or pre-DEC-073 inline
// URLs) with zero network requests (DEC-079).
func (b *Backend) BackfillSummary() (BackfillSummaryDTO, error) {
	if b == nil || b.profile == ProfileRemote || b.lib == nil {
		return BackfillSummaryDTO{}, nil
	}
	buckets, err := b.scanWeiboBackfillBuckets(nil)
	if err != nil {
		return BackfillSummaryDTO{}, err
	}
	return buckets.summary, nil
}

// StartWeiboBackfill enqueues or deduplicates the persistent historical Weibo
// cache backfill queue in cache-queue.json (v3, DEC-079). Offline legacy-link
// upgrades and pure-CDN /oslarge/ items (with known pic_id) run with 0 Weibo
// JSON API calls; items needing pic_id lookup run under Weibo API login/quota.
func (b *Backend) StartWeiboBackfill(ctx context.Context) (CacheQueueTask, error) {
	_ = ctx
	if err := b.requireDesktop(); err != nil {
		return CacheQueueTask{}, err
	}
	if b.queue == nil {
		return CacheQueueTask{}, fmt.Errorf("service: cache queue not initialized")
	}

	// Return any existing unfinished backfill queue task without duplicating.
	if existing, ok := b.FindCacheQueueTask(string(site.Weibo), CacheQueueBackfillUser, CacheQueueListWeiboBackfill); ok {
		return existing, nil
	}

	buckets, err := b.scanWeiboBackfillBuckets(nil)
	if err != nil {
		return CacheQueueTask{}, err
	}
	if buckets.summary.TotalItems == 0 {
		return CacheQueueTask{}, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBadRequest,
			Message: "历史微博缓存已全部符合最新标准",
		}
	}
	// If every pending item requires Weibo API lookup (no offline-only or fast-CDN items),
	// fail fast with auth_required when Weibo is not logged in.
	if len(buckets.offlineLinkOnly) == 0 && len(buckets.fastCDN) == 0 {
		if authErr := b.requireSiteLogin(site.Weibo); authErr != nil {
			return CacheQueueTask{}, authErr
		}
	}

	m := b.queue
	m.mu.Lock()
	now := m.nowRFC3339()

	for _, e := range m.tasks {
		if isWeiboBackfillRecord(e.rec) && isUnfinishedQueueStatus(e.rec.Status) {
			dto := m.toDTOLocked(e)
			m.mu.Unlock()
			return dto, nil
		}
	}

	prevTasks := append([]*cacheQueueEntry(nil), m.tasks...)
	taskID := makeCacheQueueID(string(site.Weibo), CacheQueueListWeiboBackfill, CacheQueueBackfillUser)
	for i, e := range m.tasks {
		if isWeiboBackfillRecord(e.rec) {
			taskID = e.rec.ID
			m.tasks = append(m.tasks[:i], m.tasks[i+1:]...)
			break
		}
	}

	rec := cacheQueueRecord{
		ID:              taskID,
		Site:            string(site.Weibo),
		User:            CacheQueueBackfillUser,
		UserName:        "历史缓存补全",
		List:            CacheQueueListWeiboBackfill,
		Kind:            CacheQueueKindWeiboBackfill,
		Origin:          CacheQueueOriginBackfill,
		BackfillTotal:   buckets.summary.TotalItems,
		BackfillSubUIDs: append([]string(nil), buckets.subscribedUIDs...),
		HasMore:         true,
		RestThreshold:   m.pickRestThresholdLocked(),
		Status:          CacheQueueQueued,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	entry := &cacheQueueEntry{
		rec: rec,
		rt: cacheQueueRuntime{
			seenKeys: make(map[string]bool),
		},
	}
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
	m.wakeSite(site.Weibo)
	return dto, nil
}

func isWeiboBackfillRecord(r cacheQueueRecord) bool {
	return strings.EqualFold(strings.TrimSpace(r.Site), string(site.Weibo)) &&
		strings.EqualFold(strings.TrimSpace(r.List), CacheQueueListWeiboBackfill)
}

func (m *cacheQueueManager) siteRiskBackoffOnly(siteID site.ID, now time.Time) (bool, time.Time) {
	if m.b == nil || m.b.subs == nil {
		return false, time.Time{}
	}
	nowUTC := now.UTC()
	if inBackoff, until := m.b.subs.IsSiteInBackoff(string(siteID)); inBackoff && until.After(nowUTC) {
		return true, until.UTC()
	}
	return false, time.Time{}
}

func buildWeiboOverridesFromPost(post weibo.Post) ([]download.WeiboBackfillSlotOverride, []TextLinkDTO) {
	images, _ := post.EffectiveMedia()
	var overrides []download.WeiboBackfillSlotOverride
	for i, img := range images {
		if img.Kind == weibo.MediaKindGIF || img.Kind == weibo.MediaKindVideo {
			continue
		}
		idx := img.Index
		if idx <= 0 {
			idx = i + 1
		}
		wmURL := strings.TrimSpace(img.WatermarkFreeURL)
		if wmURL == "" && img.URL != "" {
			if rewritten, ok := weibo.RewriteWatermarkFreeImageURL(img.URL); ok {
				wmURL = rewritten
			}
		}
		overrides = append(overrides, download.WeiboBackfillSlotOverride{
			DisplayIndex:     idx,
			PicID:            library.SanitizeMediaToken(img.PicID),
			WatermarkFreeURL: wmURL,
		})
	}
	return overrides, MapSiteTextLinks(post.Links)
}

func (m *cacheQueueManager) appendBackfillRecentLocked(entry *cacheQueueEntry, itemKey, title, nowStr string) {
	if itemKey == "" {
		return
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

func (m *cacheQueueManager) markBackfillProcessedLocked(entry *cacheQueueEntry, itemKey string) {
	if itemKey == "" {
		return
	}
	if entry.rt.seenKeys == nil {
		entry.rt.seenKeys = make(map[string]bool)
	}
	entry.rt.seenKeys[itemKey] = true
	if !slices.Contains(entry.rec.ProcessedKeys, itemKey) {
		entry.rec.ProcessedKeys = append(entry.rec.ProcessedKeys, itemKey)
	}
}

func (m *cacheQueueManager) completeBackfillLocked(entry *cacheQueueEntry) cacheQueueRecord {
	nowStr := m.nowRFC3339()
	entry.rec.Status = CacheQueueCompleted
	entry.rec.PauseReason = ""
	entry.rec.StopReason = ""
	entry.rec.NextRunAt = ""
	entry.rec.ActiveItemKey = ""
	entry.rec.ActiveTitle = ""
	entry.rec.HasMore = false
	entry.rec.BackfillPhase = ""
	entry.rec.BackfillSubCursor = ""
	entry.rec.Note = fmt.Sprintf("已补全 %d 条，跳过 %d 条", entry.rec.CachedCount, entry.rec.SkippedCount)
	entry.rec.UpdatedAt = nowStr
	entry.rt.backfillMatched = nil
	entry.rt.backfillPendingCursor = ""
	entry.rt.backfillPendingUIDDone = false
	m.saveLoggedLocked("backfill_complete")
	return entry.rec
}

func (m *cacheQueueManager) scheduleFastCDNWaitLocked(entry *cacheQueueEntry, phase, reason string) cacheQueueRecord {
	delay := m.fastCDNDelayLocked()
	now := m.clock.Now().UTC()
	nextAt := now.Add(delay)
	entry.rec.BackfillPhase = phase
	entry.rec.PauseReason = ""
	entry.rec.ActiveItemKey = ""
	entry.rec.ActiveTitle = ""
	entry.rec.UpdatedAt = now.Format(time.RFC3339)
	if m.hasEarlierRunnableOnSiteLocked(site.Weibo, entry) {
		entry.rec.Status = CacheQueueQueued
		entry.rec.StopReason = ""
		entry.rec.NextRunAt = ""
		m.saveLoggedLocked("backfill_yield_reordered")
		return entry.rec
	}
	entry.rec.Status = CacheQueueWaiting
	entry.rec.StopReason = reason
	entry.rec.NextRunAt = nextAt.Format(time.RFC3339)
	m.saveLoggedLocked("backfill_fast_wait")
	return entry.rec
}

func isBackfillStorageError(err error) bool {
	if err == nil {
		return false
	}
	var pathErr *os.PathError
	var linkErr *os.LinkError
	return site.KindOf(err) == site.ErrKindStorage ||
		errors.As(err, &pathErr) ||
		errors.As(err, &linkErr) ||
		bilibili.IsKind(err, "storage")
}

func (m *cacheQueueManager) scheduleSlowAPIWaitLocked(entry *cacheQueueEntry, phase, defaultReason string, cached bool) cacheQueueRecord {
	if entry.rec.RestThreshold <= 0 {
		entry.rec.RestThreshold = m.pickRestThresholdLocked()
	}
	if cached {
		entry.rec.SinceRestCount++
	}
	var delay time.Duration
	reason := defaultReason
	if reason == "" {
		reason = "等待下一条"
	}
	if cached && entry.rec.SinceRestCount >= entry.rec.RestThreshold {
		delay = m.randDurationLocked(m.pacing.LongRestMinDelay, m.pacing.LongRestMaxDelay)
		entry.rec.SinceRestCount = 0
		entry.rec.RestThreshold = m.pickRestThresholdLocked()
		reason = "已完成一批补全，长歇等待中"
	} else {
		delay = m.randDurationLocked(m.pacing.ItemMinDelay, m.pacing.ItemMaxDelay)
	}
	if phase == "" || phase == backfillPhaseFastCDN || phase == backfillPhaseOfflineLinks {
		phase = backfillPhaseFallbackDetail
	}
	now := m.clock.Now().UTC()
	nextAt := now.Add(delay)
	entry.rec.BackfillPhase = phase
	entry.rec.PauseReason = ""
	entry.rec.ActiveItemKey = ""
	entry.rec.ActiveTitle = ""
	entry.rec.UpdatedAt = now.Format(time.RFC3339)
	if m.hasEarlierRunnableOnSiteLocked(site.Weibo, entry) {
		m.recordSiteCooldownLocked(site.Weibo, nextAt)
		entry.rec.Status = CacheQueueQueued
		entry.rec.StopReason = ""
		entry.rec.NextRunAt = ""
		m.saveLoggedLocked("backfill_api_yield_reordered")
		return entry.rec
	}
	entry.rec.Status = CacheQueueWaiting
	entry.rec.StopReason = reason
	entry.rec.NextRunAt = nextAt.Format(time.RFC3339)
	m.saveLoggedLocked("backfill_api_wait")
	return entry.rec
}

// advanceWeiboBackfillStep executes one unit of work for the 3-tier historical
// Weibo backfill queue (Tier A offline links -> Tier B pure CDN 2-5s -> Tier C1
// subscribed mymblog -> Tier C2 fallback statuses/show).
// Caller holds m.mu on entry; this method unlocks m.mu before returning.
func (m *cacheQueueManager) advanceWeiboBackfillStep(ctx context.Context, entry *cacheQueueEntry) {
	if m.b == nil || m.b.lib == nil || m.b.dl == nil {
		rec := m.completeBackfillLocked(entry)
		m.mu.Unlock()
		m.notifyState(rec)
		return
	}

	processed := make(map[string]bool, len(entry.rec.ProcessedKeys))
	for _, k := range entry.rec.ProcessedKeys {
		if k != "" {
			processed[k] = true
		}
	}
	for k := range entry.rt.seenKeys {
		processed[k] = true
	}

	// If we have buffered matched posts from the last mymblog page, drain one
	// at 2–5s fast CDN pacing before fetching the next page!
	if len(entry.rt.backfillMatched) > 0 {
		if inBackoff, _ := m.siteRiskBackoffOnly(site.Weibo, m.clock.Now()); inBackoff {
			rec := m.enterWaitingBackoffLocked(entry, site.Weibo, site.ErrKindBackoff)
			m.mu.Unlock()
			m.notifyState(rec)
			return
		}
		match := entry.rt.backfillMatched[0]
		entry.rt.backfillMatched = entry.rt.backfillMatched[1:]
		entry.rec.BackfillPhase = backfillPhaseFastCDN
		entry.rec.ActiveItemKey = match.itemKey
		entry.rec.ActiveTitle = sanitizeQueueText(match.title)
		entry.rec.UpdatedAt = m.nowRFC3339()
		stepCtx, stepCancel := context.WithCancel(ctx)
		entry.rt.stepCancel = stepCancel
		m.mu.Unlock()

		res, bErr := m.b.dl.BackfillWeiboNoWMCompanions(
			stepCtx,
			match.itemKey,
			m.b.weibo.MediaTransport(),
			match.overrides,
			func(meta library.ItemMetadata) (library.ItemMetadata, bool) {
				return cleanLegacyInlineLinksInItem(meta, match.remoteLinks)
			},
		)

		m.mu.Lock()
		stepCancel()
		entry.rt.stepCancel = nil
		if entry.rec.Status != CacheQueueRunning || ctx.Err() != nil {
			// Put item back if paused/canceled mid-step so it is not lost.
			if bErr != nil && ctx.Err() == nil {
				entry.rt.backfillMatched = append([]backfillMatchedItem{match}, entry.rt.backfillMatched...)
			}
			m.mu.Unlock()
			return
		}
		if bErr != nil {
			kind := site.KindOf(bErr)
			if kind == site.ErrKindAuthRequired {
				entry.rt.backfillMatched = append([]backfillMatchedItem{match}, entry.rt.backfillMatched...)
				m.mu.Unlock()
				m.pauseForSite(site.Weibo, "auth_required", "微博登录已失效，重新登录后自动继续")
				return
			}
			if isBackoffOrQuotaKind(kind) {
				entry.rt.backfillMatched = append([]backfillMatchedItem{match}, entry.rt.backfillMatched...)
				rec := m.enterWaitingBackoffLocked(entry, site.Weibo, kind)
				m.mu.Unlock()
				m.notifyState(rec)
				return
			}
			if isBackfillStorageError(bErr) {
				entry.rt.backfillMatched = append([]backfillMatchedItem{match}, entry.rt.backfillMatched...)
				entry.rec.Status = CacheQueuePaused
				entry.rec.PauseReason = "storage"
				entry.rec.StopReason = "存储空间不足或写入失败，已暂停补全"
				entry.rec.NextRunAt = ""
				entry.rec.UpdatedAt = m.nowRFC3339()
				m.saveLoggedLocked("backfill_storage_pause")
				rec := entry.rec
				m.mu.Unlock()
				m.notifyState(rec)
				return
			}
			m.markBackfillProcessedLocked(entry, match.itemKey)
			entry.rec.SkippedCount++
		} else {
			m.markBackfillProcessedLocked(entry, match.itemKey)
			if res.AddedCompanions > 0 || res.LinksCleaned {
				entry.rec.CachedCount++
				m.appendBackfillRecentLocked(entry, match.itemKey, match.title, m.nowRFC3339())
			} else {
				entry.rec.SkippedCount++
			}
		}
		// Once the buffered page matches drain completely, commit the pending mymblog cursor advance
		// and pace the next JSON API call under slow API pacing (40–150s), NOT 2–5s CDN pacing!
		var rec cacheQueueRecord
		if len(entry.rt.backfillMatched) > 0 {
			rec = m.scheduleFastCDNWaitLocked(entry, backfillPhaseFastCDN, "纯 CDN 快速补全中")
		} else {
			if entry.rt.backfillPendingUIDDone {
				entry.rec.BackfillSubIndex++
				entry.rec.BackfillSubCursor = ""
			} else if entry.rt.backfillPendingCursor != "" {
				entry.rec.BackfillSubCursor = sanitizeQueueCursor(entry.rt.backfillPendingCursor)
			}
			entry.rt.backfillPendingCursor = ""
			entry.rt.backfillPendingUIDDone = false
			rec = m.scheduleSlowAPIWaitLocked(entry, backfillPhaseSubscribedTimeline, "等待下一次微博列表检查", false)
		}
		m.mu.Unlock()
		if bErr == nil && m.b.events != nil {
			m.b.events.PublishSite(EventLibraryChanged, string(site.Weibo), match.itemKey)
		}
		m.notifyState(rec)
		return
	}

	// Scan current offline library state (outside lock so SQLite never blocks queue lock).
	m.mu.Unlock()
	buckets, scanErr := m.b.scanWeiboBackfillBuckets(processed)
	m.mu.Lock()
	if entry.rec.Status != CacheQueueRunning || ctx.Err() != nil {
		m.mu.Unlock()
		return
	}
	if scanErr != nil {
		entry.rec.Status = CacheQueuePaused
		entry.rec.PauseReason = "storage"
		entry.rec.StopReason = sanitizeQueueText("读取本地媒体库失败：" + scanErr.Error())
		entry.rec.UpdatedAt = m.nowRFC3339()
		m.saveLoggedLocked("backfill_scan_err")
		rec := entry.rec
		m.mu.Unlock()
		m.notifyState(rec)
		return
	}

	doneSoFar := entry.rec.CachedCount + entry.rec.SkippedCount
	if total := doneSoFar + buckets.summary.TotalItems; total > entry.rec.BackfillTotal {
		entry.rec.BackfillTotal = total
	}

	if buckets.summary.TotalItems == 0 {
		rec := m.completeBackfillLocked(entry)
		m.mu.Unlock()
		m.notifyState(rec)
		if m.b.events != nil {
			m.b.events.PublishSite(EventLibraryChanged, string(site.Weibo), "backfill:completed")
		}
		return
	}

	// --- Tier A: Pure offline legacy inline-URL cleanup (0 network requests, 0 delay) ---
	if len(buckets.offlineLinkOnly) > 0 {
		batch := buckets.offlineLinkOnly
		entry.rec.BackfillPhase = backfillPhaseOfflineLinks
		entry.rec.UpdatedAt = m.nowRFC3339()
		m.mu.Unlock()

		removeWM := m.b.weiboRemoveWatermarkEnabled()
		var updatedKeys []string
		var processedKeys []string
		var recentAdded []CacheQueueRecentItem
		var storagePauseErr error
		nowStr := m.nowRFC3339()
		for _, item := range batch {
			if ctx.Err() != nil {
				break
			}
			fresh, ok, gErr := m.b.lib.GetItem(item.ItemKey)
			if gErr != nil {
				if isBackfillStorageError(gErr) {
					storagePauseErr = gErr
					break
				}
				processedKeys = append(processedKeys, item.ItemKey)
				continue
			}
			if !ok || strings.TrimSpace(fresh.ItemDir) == "" {
				processedKeys = append(processedKeys, item.ItemKey)
				continue
			}
			cleaned, hadLegacy := cleanLegacyInlineLinksInItem(fresh, nil)
			if !hadLegacy {
				processedKeys = append(processedKeys, item.ItemKey)
				continue
			}
			if removeWM && !library.WeiboNeedsNoWMBackfill(cleaned, true) && cleaned.CacheStandardVersion < library.WeiboCacheStandardVersion {
				cleaned.CacheStandardVersion = library.WeiboCacheStandardVersion
			}
			if _, sErr := m.b.lib.SaveItemMetadataOnly(fresh.ItemDir, cleaned); sErr != nil {
				m.b.logError("backfill.offline_links", sErr)
				if isBackfillStorageError(sErr) {
					storagePauseErr = sErr
					break
				}
				processedKeys = append(processedKeys, item.ItemKey)
				continue
			}
			updatedKeys = append(updatedKeys, item.ItemKey)
			processedKeys = append(processedKeys, item.ItemKey)
			recentAdded = append(recentAdded, CacheQueueRecentItem{
				ItemKey:  item.ItemKey,
				Title:    sanitizeQueueText(trimQueueSummaryTitle(firstNonEmpty(cleaned.Title, cleaned.Text, item.RawID))),
				CachedAt: nowStr,
			})
		}

		m.mu.Lock()
		for _, k := range processedKeys {
			m.markBackfillProcessedLocked(entry, k)
		}
		entry.rec.CachedCount += len(updatedKeys)
		entry.rec.SkippedCount += len(processedKeys) - len(updatedKeys)
		for _, r := range recentAdded {
			m.appendBackfillRecentLocked(entry, r.ItemKey, r.Title, r.CachedAt)
		}
		entry.rec.UpdatedAt = m.nowRFC3339()
		if storagePauseErr != nil {
			entry.rec.Status = CacheQueuePaused
			entry.rec.PauseReason = "storage"
			entry.rec.StopReason = "存储空间不足或写入失败，已暂停补全"
			entry.rec.NextRunAt = ""
			m.saveLoggedLocked("backfill_storage_pause")
			rec := entry.rec
			m.mu.Unlock()
			if m.b.events != nil {
				for _, k := range updatedKeys {
					m.b.events.PublishSite(EventLibraryChanged, string(site.Weibo), k)
				}
			}
			m.notifyState(rec)
			return
		}
		// If nothing remains after Tier A, complete right away!
		if len(buckets.fastCDN) == 0 && len(buckets.subscribedUIDs) == 0 && len(buckets.fallbackDetail) == 0 {
			rec := m.completeBackfillLocked(entry)
			m.mu.Unlock()
			if m.b.events != nil {
				for _, k := range updatedKeys {
					m.b.events.PublishSite(EventLibraryChanged, string(site.Weibo), k)
				}
			}
			m.notifyState(rec)
			return
		}
		m.saveLoggedLocked("backfill_tier_a")
		rec := entry.rec
		m.mu.Unlock()
		if m.b.events != nil {
			for _, k := range updatedKeys {
				m.b.events.PublishSite(EventLibraryChanged, string(site.Weibo), k)
			}
		}
		m.notifyState(rec)
		return
	}

	// --- Tier B: Pure CDN /oslarge/ fast backfill (0 Weibo JSON API calls, 2–5s pacing, no 100/h quota, no 10–25m rest) ---
	if len(buckets.fastCDN) > 0 {
		if inBackoff, _ := m.siteRiskBackoffOnly(site.Weibo, m.clock.Now()); inBackoff {
			rec := m.enterWaitingBackoffLocked(entry, site.Weibo, site.ErrKindBackoff)
			m.mu.Unlock()
			m.notifyState(rec)
			return
		}
		item := buckets.fastCDN[0]
		title := trimQueueSummaryTitle(firstNonEmpty(item.Title, item.Text, item.RawID))
		entry.rec.BackfillPhase = backfillPhaseFastCDN
		entry.rec.ActiveItemKey = item.ItemKey
		entry.rec.ActiveTitle = sanitizeQueueText(title)
		entry.rec.UpdatedAt = m.nowRFC3339()
		stepCtx, stepCancel := context.WithCancel(ctx)
		entry.rt.stepCancel = stepCancel
		m.mu.Unlock()

		res, bErr := m.b.dl.BackfillWeiboNoWMCompanions(
			stepCtx,
			item.ItemKey,
			m.b.weibo.MediaTransport(),
			nil,
			func(meta library.ItemMetadata) (library.ItemMetadata, bool) {
				return cleanLegacyInlineLinksInItem(meta, nil)
			},
		)

		m.mu.Lock()
		stepCancel()
		entry.rt.stepCancel = nil
		if entry.rec.Status != CacheQueueRunning || ctx.Err() != nil {
			m.mu.Unlock()
			return
		}
		if bErr != nil {
			kind := site.KindOf(bErr)
			if kind == site.ErrKindAuthRequired {
				m.mu.Unlock()
				m.pauseForSite(site.Weibo, "auth_required", "微博登录已失效，重新登录后自动继续")
				return
			}
			if isBackoffOrQuotaKind(kind) {
				rec := m.enterWaitingBackoffLocked(entry, site.Weibo, kind)
				m.mu.Unlock()
				m.notifyState(rec)
				return
			}
			if isBackfillStorageError(bErr) {
				entry.rec.Status = CacheQueuePaused
				entry.rec.PauseReason = "storage"
				entry.rec.StopReason = "存储空间不足或写入失败，已暂停补全"
				entry.rec.NextRunAt = ""
				entry.rec.UpdatedAt = m.nowRFC3339()
				m.saveLoggedLocked("backfill_storage_pause")
				rec := entry.rec
				m.mu.Unlock()
				m.notifyState(rec)
				return
			}
			m.markBackfillProcessedLocked(entry, item.ItemKey)
			entry.rec.SkippedCount++
		} else {
			m.markBackfillProcessedLocked(entry, item.ItemKey)
			if res.AddedCompanions > 0 || res.LinksCleaned {
				entry.rec.CachedCount++
				m.appendBackfillRecentLocked(entry, item.ItemKey, title, m.nowRFC3339())
			} else {
				entry.rec.SkippedCount++
			}
		}
		var rec cacheQueueRecord
		switch {
		case len(buckets.fastCDN) > 1:
			rec = m.scheduleFastCDNWaitLocked(entry, backfillPhaseFastCDN, "纯 CDN 快速补全中")
		case len(buckets.subscribedUIDs) > 0:
			rec = m.scheduleSlowAPIWaitLocked(entry, backfillPhaseSubscribedTimeline, "准备按已订阅博主列表补全历史条目", false)
		case len(buckets.fallbackDetail) > 0:
			rec = m.scheduleSlowAPIWaitLocked(entry, backfillPhaseFallbackDetail, "准备按单条详情补全历史条目", false)
		default:
			rec = m.completeBackfillLocked(entry)
		}
		m.mu.Unlock()
		if bErr == nil && m.b.events != nil {
			m.b.events.PublishSite(EventLibraryChanged, string(site.Weibo), item.ItemKey)
		}
		m.notifyState(rec)
		return
	}

	// --- Tier C: Items missing pic_id require Weibo API (strict login + 100/hour quota + backoff) ---
	if !m.hasSiteLogin(site.Weibo) {
		entry.rec.Status = CacheQueuePaused
		entry.rec.PauseReason = "auth_required"
		entry.rec.StopReason = "剩余历史条目需查询微博接口，请先登录微博后继续"
		entry.rec.NextRunAt = ""
		entry.rec.ActiveItemKey = ""
		entry.rec.ActiveTitle = ""
		entry.rec.UpdatedAt = m.nowRFC3339()
		m.saveLoggedLocked("backfill_auth_pause")
		rec := entry.rec
		m.mu.Unlock()
		m.notifyState(rec)
		return
	}

	// --- Tier C1: Batch-match subscribed Weibo authors via mymblog ---
	if len(entry.rec.BackfillSubUIDs) == 0 && len(buckets.subscribedUIDs) > 0 {
		entry.rec.BackfillSubUIDs = append([]string(nil), buckets.subscribedUIDs...)
		entry.rec.BackfillSubIndex = 0
		entry.rec.BackfillSubCursor = ""
	}
	for entry.rec.BackfillSubIndex < len(entry.rec.BackfillSubUIDs) {
		uid := entry.rec.BackfillSubUIDs[entry.rec.BackfillSubIndex]
		if len(buckets.subscribedByUID[uid]) > 0 {
			break
		}
		entry.rec.BackfillSubIndex++
		entry.rec.BackfillSubCursor = ""
	}
	if entry.rec.BackfillSubIndex < len(entry.rec.BackfillSubUIDs) {
		if blocked, _ := m.siteBackoffOrQuotaEnd(site.Weibo, m.clock.Now()); blocked {
			kind := site.ErrKindBackoff
			if m.b.subs != nil && !m.b.subs.CanUseSiteQuotaTokens(string(site.Weibo), 1) {
				kind = site.ErrKindQuota
			}
			rec := m.enterWaitingBackoffLocked(entry, site.Weibo, kind)
			m.mu.Unlock()
			m.notifyState(rec)
			return
		}
		uid := entry.rec.BackfillSubUIDs[entry.rec.BackfillSubIndex]
		cur := entry.rec.BackfillSubCursor
		pendingForUID := make(map[string]library.ItemMetadata, len(buckets.subscribedByUID[uid]))
		for _, item := range buckets.subscribedByUID[uid] {
			pendingForUID[strings.TrimSpace(item.RawID)] = item
		}

		entry.rec.BackfillPhase = backfillPhaseSubscribedTimeline
		entry.rec.ActiveItemKey = ""
		entry.rec.ActiveTitle = sanitizeQueueText("查询已订阅微博列表 · " + uid)
		entry.rec.UpdatedAt = m.nowRFC3339()
		stepCtx, stepCancel := context.WithCancel(site.WithAutomaticRequests(ctx, "cache_queue"))
		entry.rt.stepCancel = stepCancel
		m.mu.Unlock()

		pageNum, sinceID := weibo.ParseCursor(cur)
		pg, fetchErr := m.b.weibo.GetUserPosts(stepCtx, weibo.UserPostsRequest{
			UID:     uid,
			Page:    pageNum,
			SinceID: sinceID,
			Feature: weibo.FeatureAll,
			StreamOptions: weibo.StreamSelectOptions{
				PreferredCodec: m.b.preferredWeiboCodec(),
				MaxResolution:  m.b.currentWeiboMaxResolution(),
			},
		})
		if fetchErr == nil && pg.UncertainEmpty {
			fetchErr = &site.Error{
				Site:    site.Weibo,
				Kind:    site.ErrKindRiskControl,
				Message: firstNonEmpty(pg.UncertainReason, "微博返回空列表，可能被拦截或登录已失效"),
			}
		}

		m.mu.Lock()
		stepCancel()
		entry.rt.stepCancel = nil
		if entry.rec.Status != CacheQueueRunning || ctx.Err() != nil {
			m.mu.Unlock()
			return
		}
		if fetchErr != nil {
			kind := site.KindOf(fetchErr)
			if kind == site.ErrKindAuthRequired {
				m.mu.Unlock()
				m.pauseForSite(site.Weibo, "auth_required", "微博登录已失效，重新登录后自动继续")
				return
			}
			if isBackoffOrQuotaKind(kind) {
				rec := m.enterWaitingBackoffLocked(entry, site.Weibo, kind)
				m.mu.Unlock()
				m.notifyState(rec)
				return
			}
			// Account unavailable/schema/network on timeline: skip remaining pages for this UID
			// so unmatched items automatically fall back to Tier C2 statuses/show, while strictly
			// pacing the next Weibo JSON API request under slow API intervals (40–150s).
			entry.rec.BackfillSubIndex++
			entry.rec.BackfillSubCursor = ""
			rec := m.scheduleSlowAPIWaitLocked(entry, backfillPhaseSubscribedTimeline, "跳过异常博主，等待下一次微博列表检查", false)
			m.mu.Unlock()
			m.notifyState(rec)
			return
		}

		var matched []backfillMatchedItem
		matchedCount := 0
		for _, post := range pg.Posts {
			meta, ok := pendingForUID[strings.TrimSpace(post.Mid)]
			if !ok {
				continue
			}
			delete(pendingForUID, strings.TrimSpace(post.Mid))
			overrides, remoteLinks := buildWeiboOverridesFromPost(post)
			matched = append(matched, backfillMatchedItem{
				itemKey:     meta.ItemKey,
				title:       trimQueueSummaryTitle(firstNonEmpty(meta.Title, meta.Text, post.Text, meta.RawID)),
				overrides:   overrides,
				remoteLinks: remoteLinks,
			})
			matchedCount++
		}

		nextCur := ""
		if pg.HasMore && !pg.BottomTipsVisible {
			nextCur = weibo.FormatCursor(pg.NextPage, pg.NextSinceID)
		}
		uidDone := !pg.HasMore || pg.BottomTipsVisible || nextCur == "" || nextCur == cur || len(pendingForUID) == 0

		if matchedCount > 0 {
			entry.rt.backfillMatched = matched
			entry.rt.backfillPendingCursor = nextCur
			entry.rt.backfillPendingUIDDone = uidDone
			entry.rec.UpdatedAt = m.nowRFC3339()
			m.saveLoggedLocked("backfill_sub_matched")
			rec := entry.rec
			m.mu.Unlock()
			m.notifyState(rec)
			return
		}

		if uidDone {
			entry.rec.BackfillSubIndex++
			entry.rec.BackfillSubCursor = ""
		} else {
			entry.rec.BackfillSubCursor = sanitizeQueueCursor(nextCur)
		}
		// Next step calls a Weibo JSON API (mymblog or statuses/show), so strictly pace at 40–150s.
		rec := m.scheduleSlowAPIWaitLocked(entry, backfillPhaseSubscribedTimeline, "正在按已订阅博主列表匹配历史条目", false)
		m.mu.Unlock()
		m.notifyState(rec)
		return
	}

	// --- Tier C2: Fallback per-item GetPost (statuses/show) under strict Weibo slow pacing & 100/h quota ---
	var remaining []library.ItemMetadata
	remaining = append(remaining, buckets.fallbackDetail...)
	for _, uid := range buckets.subscribedUIDs {
		remaining = append(remaining, buckets.subscribedByUID[uid]...)
	}
	if len(remaining) == 0 {
		rec := m.completeBackfillLocked(entry)
		m.mu.Unlock()
		m.notifyState(rec)
		if m.b.events != nil {
			m.b.events.PublishSite(EventLibraryChanged, string(site.Weibo), "backfill:completed")
		}
		return
	}

	if blocked, _ := m.siteBackoffOrQuotaEnd(site.Weibo, m.clock.Now()); blocked {
		kind := site.ErrKindBackoff
		if m.b.subs != nil && !m.b.subs.CanUseSiteQuotaTokens(string(site.Weibo), 1) {
			kind = site.ErrKindQuota
		}
		rec := m.enterWaitingBackoffLocked(entry, site.Weibo, kind)
		m.mu.Unlock()
		m.notifyState(rec)
		return
	}

	item := remaining[0]
	title := trimQueueSummaryTitle(firstNonEmpty(item.Title, item.Text, item.RawID))
	entry.rec.BackfillPhase = backfillPhaseFallbackDetail
	entry.rec.ActiveItemKey = item.ItemKey
	entry.rec.ActiveTitle = sanitizeQueueText(title)
	entry.rec.UpdatedAt = m.nowRFC3339()
	stepCtx, stepCancel := context.WithCancel(site.WithAutomaticRequests(ctx, "cache_queue"))
	entry.rt.stepCancel = stepCancel
	m.mu.Unlock()

	postOpt := weibo.PostOptions{
		StreamOptions: weibo.StreamSelectOptions{
			PreferredCodec: m.b.preferredWeiboCodec(),
			MaxResolution:  m.b.currentWeiboMaxResolution(),
		},
	}
	post, getErr := m.b.weibo.GetPost(stepCtx, item.RawID, postOpt)

	var backfillRes download.WeiboBackfillResult
	var backfillErr error
	var didCache bool
	if getErr == nil && post != nil && !post.Unavailable {
		overrides, remoteLinks := buildWeiboOverridesFromPost(*post)
		backfillRes, backfillErr = m.b.dl.BackfillWeiboNoWMCompanions(
			stepCtx,
			item.ItemKey,
			m.b.weibo.MediaTransport(),
			overrides,
			func(meta library.ItemMetadata) (library.ItemMetadata, bool) {
				return cleanLegacyInlineLinksInItem(meta, remoteLinks)
			},
		)
		if backfillErr == nil && (backfillRes.AddedCompanions > 0 || backfillRes.LinksCleaned) {
			didCache = true
		}
	}

	m.mu.Lock()
	stepCancel()
	entry.rt.stepCancel = nil
	if entry.rec.Status != CacheQueueRunning || ctx.Err() != nil {
		m.mu.Unlock()
		return
	}
	opErr := getErr
	if opErr == nil {
		opErr = backfillErr
	}
	if opErr != nil {
		kind := site.KindOf(opErr)
		if kind == site.ErrKindAuthRequired {
			m.mu.Unlock()
			m.pauseForSite(site.Weibo, "auth_required", "微博登录已失效，重新登录后自动继续")
			return
		}
		if isBackoffOrQuotaKind(kind) {
			rec := m.enterWaitingBackoffLocked(entry, site.Weibo, kind)
			m.mu.Unlock()
			m.notifyState(rec)
			return
		}
		if isBackfillStorageError(opErr) {
			entry.rec.Status = CacheQueuePaused
			entry.rec.PauseReason = "storage"
			entry.rec.StopReason = "存储空间不足或写入失败，已暂停补全"
			entry.rec.NextRunAt = ""
			entry.rec.UpdatedAt = m.nowRFC3339()
			m.saveLoggedLocked("backfill_storage_pause")
			rec := entry.rec
			m.mu.Unlock()
			m.notifyState(rec)
			return
		}
		// Post deleted / unavailable / forbidden: skip without modifying local files!
		m.markBackfillProcessedLocked(entry, item.ItemKey)
		entry.rec.SkippedCount++
	} else if post == nil || post.Unavailable {
		m.markBackfillProcessedLocked(entry, item.ItemKey)
		entry.rec.SkippedCount++
	} else {
		m.markBackfillProcessedLocked(entry, item.ItemKey)
		if didCache {
			entry.rec.CachedCount++
			m.appendBackfillRecentLocked(entry, item.ItemKey, title, m.nowRFC3339())
		} else {
			entry.rec.SkippedCount++
		}
	}

	var rec cacheQueueRecord
	if len(remaining) <= 1 {
		rec = m.completeBackfillLocked(entry)
	} else {
		rec = m.scheduleSlowAPIWaitLocked(entry, backfillPhaseFallbackDetail, "等待下一条", didCache)
	}
	m.mu.Unlock()
	if didCache && m.b.events != nil {
		m.b.events.PublishSite(EventLibraryChanged, string(site.Weibo), item.ItemKey)
	}
	m.notifyState(rec)
}
