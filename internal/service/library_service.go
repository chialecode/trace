package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/uistate"
)

// EnqueueVideoCache enqueues Bilibili video parts for local caching.
func (b *Backend) EnqueueVideoCache(ctx context.Context, req download.VideoEnqueueRequest) ([]download.Task, error) {
	if err := b.requireDesktop(); err != nil {
		return nil, err
	}
	if err := b.requireSiteLogin(site.Bilibili); err != nil {
		return nil, err
	}
	if req.Detail != nil && req.Detail.Author.Mid > 0 && req.Detail.Author.Face != "" {
		b.rememberAccountAvatar(string(site.Bilibili), strconv.FormatInt(req.Detail.Author.Mid, 10), req.Detail.Author.Face)
	}
	tasks, err := b.dl.EnqueueVideo(ctx, req)
	if err != nil {
		b.logError("EnqueueVideoCache", err)
		return nil, err
	}
	b.events.PublishSite(EventTaskUpdated, string(site.Bilibili), "enqueued:video:"+req.BVID)
	return tasks, nil
}

// EnqueueDynamicCache enqueues a Bilibili dynamic post for local caching.
func (b *Backend) EnqueueDynamicCache(ctx context.Context, req download.DynamicEnqueueRequest) (download.Task, error) {
	if err := b.requireDesktop(); err != nil {
		return download.Task{}, err
	}
	if err := b.requireSiteLogin(site.Bilibili); err != nil {
		return download.Task{}, err
	}
	if req.Item != nil {
		if req.Item.Author.Mid > 0 && req.Item.Author.Face != "" {
			b.rememberAccountAvatar(string(site.Bilibili), strconv.FormatInt(req.Item.Author.Mid, 10), req.Item.Author.Face)
		}
		if req.Item.Origin != nil && req.Item.Origin.Author.Mid > 0 && req.Item.Origin.Author.Face != "" {
			b.rememberAccountAvatar(string(site.Bilibili), strconv.FormatInt(req.Item.Origin.Author.Mid, 10), req.Item.Origin.Author.Face)
		}
	}
	task, err := b.dl.EnqueueDynamic(ctx, req)
	if err != nil {
		b.logError("EnqueueDynamicCache", err)
		return download.Task{}, err
	}
	b.events.PublishSite(EventTaskUpdated, string(site.Bilibili), "enqueued:dynamic:"+req.DynamicID)
	return task, nil
}

// CacheDouyinWork enqueues a Douyin video or mixed note work for local caching.
func (b *Backend) CacheDouyinWork(ctx context.Context, awemeID string) (download.Task, error) {
	if err := b.requireDesktop(); err != nil {
		return download.Task{}, err
	}
	if err := b.requireSiteLogin(site.Douyin); err != nil {
		return download.Task{}, err
	}
	return b.enqueueDouyinWorkWithSource(ctx, awemeID, "manual", "", "")
}

// CacheDouyinWorkFor caches a work from account forUID's works list (their
// user page): a co-creation by someone else is stored under forUID's folder
// (DEC-063).
func (b *Backend) CacheDouyinWorkFor(ctx context.Context, awemeID, forUID, forName string) (download.Task, error) {
	if err := b.requireDesktop(); err != nil {
		return download.Task{}, err
	}
	if err := b.requireSiteLogin(site.Douyin); err != nil {
		return download.Task{}, err
	}
	return b.enqueueDouyinWorkWithSource(ctx, awemeID, "manual", forUID, forName)
}

// enqueueDouyinWorkWithSource enqueues a work; forUID is the account whose
// works list it came from (empty when unknown, e.g. a parsed link).
func (b *Backend) enqueueDouyinWorkWithSource(ctx context.Context, awemeID, source, forUID, forName string) (download.Task, error) {
	if err := b.requireSiteLogin(site.Douyin); err != nil {
		return download.Task{}, err
	}
	t, err := b.dl.EnqueueDouyinWork(ctx, download.DouyinWorkEnqueueRequest{
		AwemeID: strings.TrimSpace(awemeID),
		Source:  source,
		ForUID:  strings.TrimSpace(forUID),
		ForName: strings.TrimSpace(forName),
	})
	if err != nil {
		b.logError("CacheDouyinWork", err)
		return download.Task{}, err
	}
	b.events.PublishSite(EventTaskUpdated, string(site.Douyin), "enqueued:"+t.TargetID)
	return t, nil
}

// CacheWeiboPost enqueues a Weibo post (mid or mblogid) for local caching,
// reusing an in-memory plan when available (zero signed URLs on disk).
// via optionally supplies forwarding placement (DEC-056).
func (b *Backend) CacheWeiboPost(ctx context.Context, mid string, via *library.ItemVia) (download.Task, error) {
	if err := b.requireDesktop(); err != nil {
		return download.Task{}, err
	}
	if err := b.requireSiteLogin(site.Weibo); err != nil {
		return download.Task{}, err
	}
	return b.enqueueWeiboPostWithSource(ctx, mid, "manual", via, nil)
}

// CacheWeiboVideo enqueues a standalone 1034:<id> Weibo video page for local caching.
// When the video has a host post, the planner normalizes it to weibo:dynamic:<mid>;
// otherwise it caches under weibo:video:1034_<id>.
func (b *Backend) CacheWeiboVideo(ctx context.Context, oid string) (download.Task, error) {
	if err := b.requireDesktop(); err != nil {
		return download.Task{}, err
	}
	if err := b.requireSiteLogin(site.Weibo); err != nil {
		return download.Task{}, err
	}
	cleanOID := strings.TrimSpace(oid)
	if strings.HasPrefix(cleanOID, "1034_") {
		cleanOID = "1034:" + strings.TrimPrefix(cleanOID, "1034_")
	}
	t, err := b.dl.EnqueueWeiboWork(ctx, download.WeiboWorkEnqueueRequest{
		TargetID: cleanOID,
		Kind:     "video",
		Source:   "manual",
	})
	if err != nil {
		b.logError("CacheWeiboVideo", err)
		return download.Task{}, err
	}
	b.events.PublishSite(EventTaskUpdated, string(site.Weibo), "enqueued:"+t.TargetID)
	return t, nil
}

func (b *Backend) enqueueWeiboPostWithSource(
	ctx context.Context,
	mid, source string,
	via *library.ItemVia,
	explicitPlan *download.WorkDownloadPlan,
) (download.Task, error) {
	if err := b.requireSiteLogin(site.Weibo); err != nil {
		return download.Task{}, err
	}
	cleanMid := strings.TrimSpace(mid)
	plan := explicitPlan
	if plan == nil && b.weiboPostCache != nil {
		if cachedPost, ok := b.weiboPostCache.get(cleanMid, b.preferredWeiboCodec(), b.currentWeiboMaxResolution()); ok {
			if built, bErr := b.buildWeiboWorkPlanFromPost(cachedPost); bErr == nil {
				plan = &built
			}
		}
	}
	t, err := b.dl.EnqueueWeiboWork(ctx, download.WeiboWorkEnqueueRequest{
		TargetID: cleanMid,
		Kind:     "dynamic",
		Source:   source,
		Plan:     plan,
		Via:      via,
	})
	if err != nil {
		b.logError("CacheWeiboPost", err)
		return download.Task{}, err
	}
	b.events.PublishSite(EventTaskUpdated, string(site.Weibo), "enqueued:"+t.TargetID)
	return t, nil
}

// CacheXPost enqueues an X post (Snowflake status_id) for local caching,
// reusing a complete in-memory plan when available (zero CDN URLs persisted).
// via optionally supplies forwarding placement (DEC-056).
func (b *Backend) CacheXPost(ctx context.Context, statusID string, via *library.ItemVia) (download.Task, error) {
	if err := b.requireDesktop(); err != nil {
		return download.Task{}, err
	}
	if err := b.requireSiteLogin(site.X); err != nil {
		return download.Task{}, err
	}
	return b.enqueueXPostWithSource(ctx, statusID, "manual", via, nil)
}

func (b *Backend) enqueueXPostWithSource(
	ctx context.Context,
	statusID, source string,
	via *library.ItemVia,
	explicitPlan *download.WorkDownloadPlan,
) (download.Task, error) {
	if err := b.requireSiteLogin(site.X); err != nil {
		return download.Task{}, err
	}
	cleanID := strings.TrimSpace(statusID)
	plan := explicitPlan
	if plan == nil && b.xPostCache != nil {
		if cachedPost, ok := b.xPostCache.get(cleanID); ok {
			if built, bErr := b.buildXWorkPlanFromPost(cachedPost); bErr == nil {
				plan = &built
			}
		}
	}
	t, err := b.dl.EnqueueXWork(ctx, download.XWorkEnqueueRequest{
		TargetID: cleanID,
		Source:   source,
		Plan:     plan,
		Via:      via,
	})
	if err != nil {
		b.logError("CacheXPost", err)
		return download.Task{}, err
	}
	b.events.PublishSite(EventTaskUpdated, string(site.X), "enqueued:"+t.TargetID)
	return t, nil
}

// CacheSupplement re-downloads missing danmaku/subtitle/cover sidecars for an already cached Bilibili video without re-downloading video/audio.
func (b *Backend) CacheSupplement(ctx context.Context, bvid string) (download.Task, error) {
	if err := b.requireDesktop(); err != nil {
		return download.Task{}, err
	}
	if err := b.requireSiteLogin(site.Bilibili); err != nil {
		return download.Task{}, err
	}
	task, err := b.dl.CacheSupplement(ctx, bvid)
	if err != nil {
		b.logError("CacheSupplement", err)
		return download.Task{}, err
	}
	b.events.PublishSite(EventTaskUpdated, string(site.Bilibili), "enqueued:supplement:"+bvid)
	return task, nil
}

// Upgrade enqueues a quality upgrade download task for a cached Bilibili video item.
func (b *Backend) Upgrade(ctx context.Context, key string) (download.Task, error) {
	if err := b.requireDesktop(); err != nil {
		return download.Task{}, err
	}
	if err := b.requireSiteLogin(site.Bilibili); err != nil {
		return download.Task{}, err
	}
	task, err := b.dl.Upgrade(ctx, key)
	if err != nil {
		b.logError("Upgrade", err)
		return download.Task{}, err
	}
	b.events.PublishSite(EventTaskUpdated, string(site.Bilibili), "enqueued:upgrade:"+key)
	return task, nil
}

// ListDownloadTasks returns download task summaries across sites (use TaskDetail for Files/Attempts).
func (b *Backend) ListDownloadTasks() ([]download.Task, error) {
	if err := b.requireAnyLogin(); err != nil {
		return nil, err
	}
	return b.dl.ListTasksSummary()
}

// TaskDetail returns per-file progress and sanitized attempt history for a download task.
func (b *Backend) TaskDetail(taskID string) (download.TaskDetail, bool, error) {
	if err := b.requireAnyLogin(); err != nil {
		return download.TaskDetail{}, false, err
	}
	return b.dl.TaskDetail(taskID)
}

// PauseTask pauses a download task.
func (b *Backend) PauseTask(taskID string) (download.Task, error) {
	if err := b.requireAnyLogin(); err != nil {
		return download.Task{}, err
	}
	return b.dl.Pause(taskID)
}

func (b *Backend) requireTaskSiteLogin(taskID string) error {
	if err := b.requireAnyLogin(); err != nil {
		return err
	}
	detail, ok, err := b.dl.TaskDetail(taskID)
	if err != nil {
		return err
	}
	if ok {
		tSite, sErr := normalizeSiteID(detail.Task.Site)
		if sErr != nil {
			return sErr
		}
		return b.requireSiteLogin(tSite)
	}
	return nil
}

// ResumeTask resumes a paused download task after verifying its owning site is logged in.
func (b *Backend) ResumeTask(taskID string) (download.Task, error) {
	if err := b.requireTaskSiteLogin(taskID); err != nil {
		return download.Task{}, err
	}
	return b.dl.Resume(taskID)
}

// RetryTask retries a failed or partial download task after verifying its owning site is logged in.
func (b *Backend) RetryTask(taskID string) (download.Task, error) {
	if err := b.requireTaskSiteLogin(taskID); err != nil {
		return download.Task{}, err
	}
	return b.dl.Retry(taskID)
}

// RetryAllTasks re-queues all actionable failed, partial, and non-user-paused
// download tasks whose owning site is currently logged in.
func (b *Backend) RetryAllTasks() (download.BatchRetryResult, error) {
	if err := b.requireAnyLogin(); err != nil {
		return download.BatchRetryResult{}, err
	}
	allowedSites := make(map[string]bool, len(site.All()))
	for _, sid := range site.All() {
		allowedSites[string(sid)] = b.requireSiteLogin(sid) == nil
	}
	res, err := b.dl.RetryActionable(allowedSites)
	if err != nil {
		return res, err
	}
	if res.Retried == 0 && res.SkippedAuth > 0 {
		if len(res.MissingAuthSites) == 1 {
			if sid, ok := site.ParseID(res.MissingAuthSites[0]); ok {
				return res, b.requireSiteLogin(sid)
			}
		}
		names := make([]string, 0, len(res.MissingAuthSites))
		for _, raw := range res.MissingAuthSites {
			if name := site.DisplayName(raw); name != "" {
				names = append(names, name)
			}
		}
		msg := "请先登录对应站点后再重试"
		if len(names) > 0 {
			msg = "请先登录" + strings.Join(names, "、") + "后再重试"
		}
		return res, &site.Error{
			Kind:    site.ErrKindAuthRequired,
			Message: msg,
		}
	}
	return res, nil
}

// CancelTask cancels a download task and removes incomplete .part files.
func (b *Backend) CancelTask(taskID string) (download.Task, error) {
	if err := b.requireAnyLogin(); err != nil {
		return download.Task{}, err
	}
	return b.dl.Cancel(taskID)
}

// RemoveTask deletes a finished task record; cached files stay in the library.
func (b *Backend) RemoveTask(taskID string) error {
	if err := b.requireAnyLogin(); err != nil {
		return err
	}
	if err := b.dl.Remove(taskID); err != nil {
		return err
	}
	b.events.Publish(EventTaskUpdated, "removed:"+taskID)
	return nil
}

// RemoveFinishedTasks clears every completed or canceled task record.
func (b *Backend) RemoveFinishedTasks() (int, error) {
	if err := b.requireAnyLogin(); err != nil {
		return 0, err
	}
	n, err := b.dl.RemoveFinished()
	if n > 0 {
		b.events.Publish(EventTaskUpdated, "removed:finished")
	}
	return n, err
}

// QueryFeed queries the unified feed (cached + online-discovered items) ordered by pub_ts DESC.
func (b *Backend) QueryFeed(filter library.FeedFilter) ([]library.FeedEntry, error) {
	if err := b.requireAnyLogin(); err != nil {
		return nil, err
	}
	return b.lib.QueryFeed(filter)
}

// FeedPage queries a keyset-paginated page of the home feed.
func (b *Backend) FeedPage(req library.FeedPageRequest) (library.FeedPageResult, error) {
	if err := b.requireAnyLogin(); err != nil {
		return library.FeedPageResult{}, err
	}
	return b.lib.FeedPage(req)
}

// Anchor returns the persisted home feed reading anchor from ui-state.json.
func (b *Backend) Anchor() uistate.FeedAnchor {
	if b.requireAnyLogin() != nil {
		return uistate.FeedAnchor{}
	}
	return b.uiState.Get().Anchor
}

// SaveAnchor persists the home feed reading anchor into ui-state.json.
func (b *Backend) SaveAnchor(anchor uistate.FeedAnchor) error {
	if err := b.requireAnyLogin(); err != nil {
		return err
	}
	_, err := b.uiState.Mutate(func(st *uistate.State) { st.Anchor = anchor })
	return err
}

// NewerCount returns the number of feed entries strictly newer than anchor.
func (b *Backend) NewerCount(anchor uistate.FeedAnchor) (int, error) {
	if err := b.requireAnyLogin(); err != nil {
		return 0, err
	}
	return b.lib.NewerCachedCount(anchor.PubTs, anchor.Key)
}

// LibraryStatistics returns local library counts and total size.
func (b *Backend) LibraryStatistics() (library.Statistics, error) {
	if err := b.requireAnyLogin(); err != nil {
		return library.Statistics{}, err
	}
	return b.lib.Statistics()
}

// ListLibraryItems returns locally cached items matching filter.
func (b *Backend) ListLibraryItems(filter library.FeedFilter) ([]library.ItemMetadata, error) {
	if err := b.requireAnyLogin(); err != nil {
		return nil, err
	}
	return b.lib.ListItems(filter)
}

// GetLibraryItem returns a single cached item by itemKey or rawID.
func (b *Backend) GetLibraryItem(keyOrID string) (library.ItemMetadata, bool, error) {
	if err := b.requireAnyLogin(); err != nil {
		return library.ItemMetadata{}, false, err
	}
	return b.lib.GetItem(keyOrID)
}

// GetLibraryItemMetaOnly returns cached item metadata from the SQLite index without
// per-item filesystem symlink/stat resolution (for card covers & feed summaries).
func (b *Backend) GetLibraryItemMetaOnly(keyOrID string) (library.ItemMetadata, bool, error) {
	if err := b.requireAnyLogin(); err != nil {
		return library.ItemMetadata{}, false, err
	}
	return b.lib.GetItemMetaOnly(keyOrID)
}

// MediaPath validates and resolves a registered relative file path within a cached item.
func (b *Backend) MediaPath(key, relPath string) (string, error) {
	if err := b.requireAnyLogin(); err != nil {
		return "", err
	}
	return b.lib.MediaPath(key, relPath)
}

// States computes the canonical cache status dots for the given item keys.
func (b *Backend) States(keys []string) map[string]library.DotState {
	if b.requireAnyLogin() != nil {
		out := make(map[string]library.DotState, len(keys))
		for _, key := range keys {
			out[key] = library.DotState{Key: key, Dot: "none", Label: "登录后查看缓存状态"}
		}
		return out
	}
	out := b.lib.States(keys)
	activeProgress := make(map[string]float64)
	latest := make(map[string]download.Task)
	if tasks, err := b.dl.ListNonTerminalTasks(); err == nil {
		for _, t := range tasks {
			for _, key := range []string{t.ItemKey, t.TargetID} {
				if key == "" {
					continue
				}
				if _, exists := latest[key]; !exists {
					latest[key] = t
				}
			}
			if t.Status == "queued" || t.Status == "downloading" || t.Status == "muxing" || t.Status == "running" {
				prog := t.Progress
				if prog == 0 && t.TotalBytes > 0 {
					prog = float64(t.BytesDownloaded) / float64(t.TotalBytes)
				}
				if t.ItemKey != "" {
					activeProgress[t.ItemKey] = prog
				}
				if t.TargetID != "" {
					activeProgress[t.TargetID] = prog
				}
			}
		}
	}
	for _, k := range keys {
		if prog, active := activeProgress[k]; active {
			out[k] = library.DotState{
				Key:      k,
				Dot:      "ring",
				Label:    "缓存中",
				Progress: prog,
			}
			continue
		}
		if task, exists := latest[k]; exists {
			switch task.Status {
			case "paused", "partial":
				dot := out[k]
				dot.Dot, dot.Label = "yellow", "部分缓存，重试可继续"
				if task.Status == "paused" {
					dot.Label = "已暂停，可继续"
				}
				out[k] = dot
			case "failed":
				dot := out[k]
				dot.Dot, dot.Label = "red", "缓存失败，可重试"
				out[k] = dot
			}
		}
	}
	return out
}

// PlanDelete computes the safe file deletion plan for an item key.
func (b *Backend) PlanDelete(key string) (library.DeletePlan, error) {
	if err := b.requireAnyLogin(); err != nil {
		return library.DeletePlan{}, err
	}
	return b.lib.PlanDelete(key)
}

// Delete executes a safe manifest-only file deletion for an item key.
func (b *Backend) Delete(key string) error {
	return b.DeleteLibraryItem(key, true)
}

// RebuildLibraryIndex scans CacheDir for item.json files and rebuilds the SQLite index.
func (b *Backend) RebuildLibraryIndex() (library.RebuildReport, error) {
	if err := b.requireAnyLogin(); err != nil {
		return library.RebuildReport{}, err
	}
	rep, err := b.lib.RebuildIndex()
	if len(rep.CorruptBackedUp) > 0 {
		b.logMu.Lock()
		b.corruptFiles = append(b.corruptFiles, rep.CorruptBackedUp...)
		b.logMu.Unlock()
	}
	b.events.Publish(EventLibraryChanged, "rebuilt")
	return rep, err
}

// RefreshLibrary is the media library's "刷新": a quick rescan that drops
// items whose folder was deleted and marks items with missing files. Only
// local files are read; no site request is made.
func (b *Backend) RefreshLibrary() (library.RebuildReport, error) {
	if err := b.requireAnyLogin(); err != nil {
		return library.RebuildReport{}, err
	}
	rep, err := b.lib.RefreshIndex()
	if len(rep.CorruptBackedUp) > 0 {
		b.logMu.Lock()
		b.corruptFiles = append(b.corruptFiles, rep.CorruptBackedUp...)
		b.logMu.Unlock()
	}
	if err != nil {
		b.logError("RefreshLibrary", err)
	} else {
		b.logInfo(fmt.Sprintf("library refresh: scanned=%d removed=%d newly_missing=%d", rep.Scanned, len(rep.Removed), len(rep.NewlyMissing)))
	}
	b.events.Publish(EventLibraryChanged, "refreshed")
	return rep, err
}

// AddLibraryRoot adds an existing cache root or legacy trace-data directory and indexes its manifests.
func (b *Backend) AddLibraryRoot(selectedDir string) (library.AddRootReport, error) {
	if err := b.requireAnyLogin(); err != nil {
		return library.AddRootReport{}, err
	}
	rep, err := b.lib.AddLibraryRoot(selectedDir)
	if err == nil {
		b.events.Publish(EventLibraryChanged, "root_added")
	}
	return rep, err
}

// DeleteLibraryItem removes an item from the index and optionally deletes its registered files from disk.
func (b *Backend) DeleteLibraryItem(itemKey string, deleteFiles bool) error {
	if err := b.requireAnyLogin(); err != nil {
		return err
	}
	if err := b.lib.DeleteItem(itemKey, deleteFiles); err != nil {
		return err
	}
	b.pruneAccountAvatars()
	b.events.Publish(EventLibraryChanged, "deleted:"+itemKey)
	return nil
}

// MigrateCacheDir changes the active cache directory, optionally moves existing files, and updates settings.json.
func (b *Backend) MigrateCacheDir(newDir string, moveExisting bool) (library.RebuildReport, error) {
	if err := b.requireDesktop(); err != nil {
		return library.RebuildReport{}, err
	}
	if err := b.requireAnyLogin(); err != nil {
		return library.RebuildReport{}, err
	}
	if !moveExisting {
		cur := b.settings.Get()
		cur.CacheRoot, cur.CacheDir = newDir, newDir
		_, err := b.UpdateSettings(cur)
		return library.RebuildReport{}, err
	}
	if _, recordings := b.ActiveWork(); recordings > 0 {
		return library.RebuildReport{}, &site.Error{Kind: site.ErrKindBadRequest, Message: "有直播正在录制，请先停止录制后再迁移缓存目录"}
	}
	if b.hasActiveTransferJobs() {
		return library.RebuildReport{}, &site.Error{Kind: site.ErrKindBadRequest, Message: "有导入或导出任务正在进行，请等待完成后再迁移缓存目录"}
	}
	b.settingsMu.Lock()
	cur := b.settings.Get()
	cur.CacheDir, cur.CacheRoot = newDir, newDir
	cur, err := b.settings.PreviewUpdate(cur)
	if err != nil {
		b.settingsMu.Unlock()
		return library.RebuildReport{}, err
	}
	var rep library.RebuildReport
	err = b.dl.WithIdleTasks(func() error {
		var migrationErr error
		rep, migrationErr = b.lib.MigrateCacheDirWithPersistence(newDir, true, func() error { _, err := b.settings.Update(cur); return err })
		return migrationErr
	})
	b.settingsMu.Unlock()
	if err != nil {
		return rep, err
	}
	b.events.Publish(EventLibraryChanged, "migrated")
	return rep, nil
}

// UpgradeCandidates returns up to limit cached Bilibili video items ordered by oldest UpgradeCheckedAt.
func (b *Backend) UpgradeCandidates(limit int) ([]library.ItemMetadata, error) {
	if err := b.requireAnyLogin(); err != nil {
		return nil, err
	}
	return b.lib.UpgradeCandidates(limit)
}

// CheckUpgradeKeys checks the specified Bilibili video keys (or up to 50 candidates when empty) for quality upgrades.
func (b *Backend) CheckUpgradeKeys(ctx context.Context, keys []string, force bool) ([]library.ItemMetadata, error) {
	if err := b.requireSiteLogin(site.Bilibili); err != nil {
		return nil, err
	}
	dlCfg := b.settings.Get().Download
	opts := bilibili.PlaySelectionOptions{
		CodecOrder:          append([]string(nil), dlCfg.CodecOrder...),
		PreferredCodec:      dlCfg.PreferredVideoCodec,
		PreferLosslessAudio: dlCfg.PreferLosslessAudio,
		PreferDolbyAudio:    dlCfg.PreferDolbyAudio,
		TargetQn:            127,
	}
	items, err := b.lib.CheckUpgradeKeys(ctx, b.bili, keys, force, opts)
	if err != nil {
		b.logError("CheckUpgradeKeys", err)
		return items, err
	}
	b.events.PublishSite(EventLibraryChanged, string(site.Bilibili), "upgrade_checked")
	return items, nil
}

// CheckQualityUpgrade checks cached Bilibili videos against PlayURL for higher video/audio quality.
func (b *Backend) CheckQualityUpgrade(ctx context.Context) ([]library.ItemMetadata, error) {
	return b.CheckUpgradeKeys(ctx, nil, true)
}

// OpenItemFolder opens or reveals a cached item's folder on disk via Desktop.
func (b *Backend) OpenItemFolder(itemKey string) error {
	if err := b.requireAnyLogin(); err != nil {
		return err
	}
	item, ok, err := b.lib.GetItem(itemKey)
	if err != nil {
		return err
	}
	if !ok || item.ItemDir == "" {
		return fmt.Errorf("service: item folder not found for %s", itemKey)
	}
	return b.desktop.OpenPath(item.ItemDir)
}
