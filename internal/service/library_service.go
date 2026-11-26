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
	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/site/weibo"
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
	return b.enqueueDouyinWorkWithPlan(ctx, awemeID, source, forUID, forName, nil)
}

func (b *Backend) enqueueDouyinWorkWithPlan(
	ctx context.Context,
	awemeID, source, forUID, forName string,
	explicitPlan *download.WorkDownloadPlan,
) (download.Task, error) {
	if err := b.requireSiteLogin(site.Douyin); err != nil {
		return download.Task{}, err
	}
	cleanID := strings.TrimSpace(awemeID)
	mode := download.ConflictResolutionFromContext(ctx)
	force := download.ForceOverwriteFromContext(ctx)
	plan := explicitPlan
	if plan == nil && mode == "" && !force && b.douyinWorkCache != nil {
		if cachedDetail, ok := b.douyinWorkCache.get(cleanID, b.preferredDouyinCodec()); ok {
			if built, bErr := b.buildDouyinWorkPlanFromDetail(ctx, cachedDetail, b.currentWorkPlanOptions()); bErr == nil {
				plan = &built
			}
		}
	}
	if plan != nil && (mode != "" || force) {
		cp := *plan
		if mode != "" {
			cp.ConflictResolution = mode
		}
		if force {
			cp.ForceOverwrite = true
		}
		plan = &cp
	}
	t, err := b.dl.EnqueueDouyinWork(ctx, download.DouyinWorkEnqueueRequest{
		AwemeID: cleanID,
		Source:  source,
		Plan:    plan,
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
	mode := download.ConflictResolutionFromContext(ctx)
	force := download.ForceOverwriteFromContext(ctx)
	plan := explicitPlan
	if plan == nil && mode == "" && !force && b.weiboPostCache != nil {
		if cachedPost, ok := b.weiboPostCache.get(cleanMid, b.preferredWeiboCodec(), b.currentWeiboMaxResolution(), b.weiboRemoveWatermarkEnabled()); ok {
			if built, bErr := b.buildWeiboWorkPlanFromPost(cachedPost); bErr == nil {
				plan = &built
			}
		}
	}
	if plan != nil && (mode != "" || force) {
		cp := *plan
		if mode != "" {
			cp.ConflictResolution = mode
		}
		if force {
			cp.ForceOverwrite = true
		}
		plan = &cp
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
	mode := download.ConflictResolutionFromContext(ctx)
	force := download.ForceOverwriteFromContext(ctx)
	plan := explicitPlan
	if plan == nil && mode == "" && !force && b.xPostCache != nil {
		if cachedPost, ok := b.xPostCache.get(cleanID); ok {
			if built, bErr := b.buildXWorkPlanFromPost(cachedPost); bErr == nil {
				plan = &built
			}
		}
	}
	if plan != nil && (mode != "" || force) {
		cp := *plan
		if mode != "" {
			cp.ConflictResolution = mode
		}
		if force {
			cp.ForceOverwrite = true
		}
		plan = &cp
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

// ForceRecacheItem triggers an explicit manual full redownload of an already
// cached item across all 4 sites (DEC-079). Media is freshly downloaded and
// verified inside .trace-stage-* without reusing local files, then atomically
// swapped via .trace-prev-*; if the remote item is deleted, unavailable,
// degraded, or placeholder-only, ErrRemoteDegradedRefused aborts cleanly and
// preserves the local files untouched.
func (b *Backend) ForceRecacheItem(ctx context.Context, itemKey string) (download.Task, error) {
	if err := b.requireDesktop(); err != nil {
		return download.Task{}, err
	}
	if b.lib == nil || b.dl == nil {
		return download.Task{}, &site.Error{Kind: site.ErrKindUnavailable, Message: "下载服务尚未初始化"}
	}
	cleanKey := strings.TrimSpace(itemKey)
	if cleanKey == "" {
		return download.Task{}, &site.Error{Kind: site.ErrKindBadRequest, Message: "条目键不能为空"}
	}
	meta, ok, err := b.lib.GetItem(cleanKey)
	if err != nil {
		return download.Task{}, err
	}
	if !ok {
		return download.Task{}, &site.Error{Kind: site.ErrKindUnavailable, Message: "未找到该已缓存条目"}
	}
	if meta.Kind == "live" {
		return download.Task{}, &site.Error{Kind: site.ErrKindBadRequest, Message: "直播录像不支持强制重新下载"}
	}
	if meta.IsArchivedSnapshot() {
		return download.Task{}, &site.Error{Kind: site.ErrKindBadRequest, Message: "旧版归档条目不支持强制重新下载"}
	}
	siteID, sErr := normalizeExplicitSiteID(meta.Site)
	if sErr != nil {
		return download.Task{}, sErr
	}
	if err := b.requireSiteLogin(siteID); err != nil {
		return download.Task{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	forceCtx := download.WithForceOverwrite(ctx)
	localHadMedia := hasValidLocalMedia(meta)
	var via *library.ItemVia
	if meta.Via != nil {
		cp := *meta.Via
		via = &cp
	}

	switch siteID {
	case site.Bilibili:
		if meta.Kind == "video" {
			vd, err := b.bili.VideoDetail(forceCtx, meta.RawID)
			if err != nil {
				return download.Task{}, wrapDegradedIfRemoteInvalid(err)
			}
			if vd.IsUpowerLocked() {
				return download.Task{}, wrapDegradedIfRemoteInvalid(bilibili.NewUpowerLockedError("/x/web-interface/wbi/view"))
			}
			if len(vd.Pages) == 0 {
				return download.Task{}, fmt.Errorf("%w：源站视频已无可用分P", ErrRemoteDegradedRefused)
			}
			if _, pErr := b.bili.PlayURL(forceCtx, vd.BVID, vd.Pages[0].CID, b.bilibiliPlaySelectionOpts()); pErr != nil {
				return download.Task{}, wrapDegradedIfRemoteInvalid(pErr)
			}
			var forMid int64
			if via != nil && via.Reason == library.ViaCollab {
				forMid, _ = strconv.ParseInt(strings.TrimSpace(via.UID), 10, 64)
			}
			tasks, err := b.EnqueueVideoCache(forceCtx, download.VideoEnqueueRequest{
				BVID:   vd.BVID,
				Detail: &vd,
				Source: "manual",
				ForMid: forMid,
			})
			if err != nil {
				return download.Task{}, wrapDegradedIfRemoteInvalid(err)
			}
			if len(tasks) == 0 {
				return download.Task{}, fmt.Errorf("未创建强制重新下载任务")
			}
			return tasks[0], nil
		}

		dyn, err := b.bili.DynamicDetail(forceCtx, meta.RawID)
		if err != nil {
			return download.Task{}, wrapDegradedIfRemoteInvalid(err)
		}
		if dyn.Degraded || dyn.Blocked {
			return download.Task{}, fmt.Errorf("%w：源站动态已不可用或受限", ErrRemoteDegradedRefused)
		}
		if hasSnapshotMedia(meta, "orig.json") && dyn.Origin == nil {
			return download.Task{}, fmt.Errorf("%w：转发原动态已在源站失效", ErrRemoteDegradedRefused)
		}
		effImgs, effVids := effectiveBiliDynamicMedia(dyn)
		if localHadMedia && len(effImgs) == 0 && len(effVids) == 0 {
			return download.Task{}, fmt.Errorf("%w：源站动态已无有效媒体附件", ErrRemoteDegradedRefused)
		}
		return b.EnqueueDynamicCache(forceCtx, download.DynamicEnqueueRequest{
			DynamicID: dyn.ID,
			Item:      &dyn,
			Source:    "manual",
		})

	case site.Douyin:
		detail, err := b.douyin.GetWorkDetail(forceCtx, meta.RawID, douyin.StreamSelectOptions{
			PreferredCodec: b.preferredDouyinCodec(),
		})
		if err != nil {
			return download.Task{}, wrapDegradedIfRemoteInvalid(err)
		}
		if detail == nil || detail.Uncacheable || detail.Kind == douyin.WorkKindUnsupported {
			reason := "源站作品当前受限或不可缓存"
			if detail != nil && strings.TrimSpace(detail.UncacheableReason) != "" {
				reason = detail.UncacheableReason
			}
			return download.Task{}, fmt.Errorf("%w：%s", ErrRemoteDegradedRefused, reason)
		}
		if localHadMedia {
			if detail.Kind == douyin.WorkKindNote && len(detail.Pages) == 0 {
				return download.Task{}, fmt.Errorf("%w：源站图文已无有效页面", ErrRemoteDegradedRefused)
			}
			if detail.Kind == douyin.WorkKindVideo && (detail.Video == nil || len(detail.Video.URLs) == 0) {
				return download.Task{}, fmt.Errorf("%w：源站视频已无可用播放流", ErrRemoteDegradedRefused)
			}
		}
		plan, err := b.buildDouyinWorkPlanFromDetail(forceCtx, detail, b.currentWorkPlanOptions())
		if err != nil {
			return download.Task{}, wrapDegradedIfRemoteInvalid(err)
		}
		plan.ForceOverwrite = true
		var forUID, forName string
		if via != nil && via.Reason == library.ViaCollab {
			forUID, forName = via.UID, via.Name
		}
		return b.enqueueDouyinWorkWithPlan(forceCtx, meta.RawID, "manual", forUID, forName, &plan)

	case site.Weibo:
		postOpt := weibo.PostOptions{
			StreamOptions: weibo.StreamSelectOptions{
				PreferredCodec: b.preferredWeiboCodec(),
				MaxResolution:  b.currentWeiboMaxResolution(),
			},
		}
		if meta.Kind == "video" {
			oid := meta.RawID
			if strings.HasPrefix(oid, "1034_") {
				oid = "1034:" + strings.TrimPrefix(oid, "1034_")
			}
			vr, err := b.weibo.GetVideo(forceCtx, oid, postOpt)
			if err != nil {
				return download.Task{}, wrapDegradedIfRemoteInvalid(err)
			}
			if vr == nil ||
				(vr.HostPost != nil && vr.HostPost.Unavailable) ||
				(vr.HostPost == nil && (vr.Video == nil || vr.Video.Selected == nil || strings.TrimSpace(vr.Video.Selected.URL) == "")) {
				return download.Task{}, fmt.Errorf("%w：源站微博视频已失效", ErrRemoteDegradedRefused)
			}
			var plan download.WorkDownloadPlan
			if vr.HostPost != nil {
				plan, err = b.buildWeiboWorkPlanFromPost(vr.HostPost)
			} else {
				plan, err = b.buildWeiboWorkPlanFromStandaloneVideo(vr)
			}
			if err != nil {
				return download.Task{}, wrapDegradedIfRemoteInvalid(err)
			}
			plan.ForceOverwrite = true
			t, err := b.dl.EnqueueWeiboWork(forceCtx, download.WeiboWorkEnqueueRequest{
				TargetID: oid,
				Kind:     "video",
				Source:   "manual",
				Via:      via,
				Plan:     &plan,
			})
			if err != nil {
				b.logError("ForceRecacheItem.weibo_video", err)
				return download.Task{}, err
			}
			b.events.PublishSite(EventTaskUpdated, string(site.Weibo), "enqueued:"+t.TargetID)
			return t, nil
		}
		post, err := b.weibo.GetPost(forceCtx, meta.RawID, postOpt)
		if err != nil {
			return download.Task{}, wrapDegradedIfRemoteInvalid(err)
		}
		if post == nil || post.Unavailable {
			reason := "源站微博已不可见或已删除"
			if post != nil && strings.TrimSpace(post.UnavailableReason) != "" {
				reason = post.UnavailableReason
			}
			return download.Task{}, fmt.Errorf("%w：%s", ErrRemoteDegradedRefused, reason)
		}
		if post.Retweeted != nil && post.Retweeted.Unavailable && meta.Origin != nil && !meta.Origin.Unavailable {
			return download.Task{}, fmt.Errorf("%w：源站转发原帖已失效", ErrRemoteDegradedRefused)
		}
		imgs, vid := post.EffectiveMedia()
		if localHadMedia && len(imgs) == 0 && (vid == nil || vid.Selected == nil || strings.TrimSpace(vid.Selected.URL) == "") {
			return download.Task{}, fmt.Errorf("%w：源站微博已无有效媒体附件", ErrRemoteDegradedRefused)
		}
		plan, err := b.buildWeiboWorkPlanFromPost(post)
		if err != nil {
			return download.Task{}, wrapDegradedIfRemoteInvalid(err)
		}
		plan.ForceOverwrite = true
		return b.enqueueWeiboPostWithSource(forceCtx, meta.RawID, "manual", via, &plan)

	case site.X:
		post, err := b.x.GetPost(forceCtx, meta.RawID)
		if err != nil {
			return download.Task{}, wrapDegradedIfRemoteInvalid(err)
		}
		if post == nil || post.Unavailable || post.IsPreviewDisplay {
			reason := "源站帖子已不可用或仅展示付费预览"
			if post != nil && strings.TrimSpace(post.UnavailableReason) != "" {
				reason = post.UnavailableReason
			}
			return download.Task{}, fmt.Errorf("%w：%s", ErrRemoteDegradedRefused, reason)
		}
		if post.OriginUnavailable && meta.Origin != nil && !meta.Origin.Unavailable {
			return download.Task{}, fmt.Errorf("%w：源站引用或转推原帖已失效", ErrRemoteDegradedRefused)
		}
		if localHadMedia && !xPostHasValidDownloadableMedia(post) {
			return download.Task{}, fmt.Errorf("%w：源站帖子已无有效可下载媒体", ErrRemoteDegradedRefused)
		}
		plan, err := b.buildXWorkPlanFromPost(post)
		if err != nil {
			return download.Task{}, wrapDegradedIfRemoteInvalid(err)
		}
		plan.ForceOverwrite = true
		return b.enqueueXPostWithSource(forceCtx, meta.RawID, "manual", via, &plan)

	default:
		return download.Task{}, &site.Error{Site: siteID, Kind: site.ErrKindBadRequest, Message: "不支持的站点"}
	}
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

// Upgrade enqueues a quality upgrade download task for a cached item across supported sites.
func (b *Backend) Upgrade(ctx context.Context, key string) (download.Task, error) {
	if err := b.requireDesktop(); err != nil {
		return download.Task{}, err
	}
	targetSite := site.Bilibili
	if parts := strings.SplitN(strings.TrimSpace(key), ":", 3); len(parts) == 3 {
		if parsed, sErr := normalizeExplicitSiteID(parts[0]); sErr == nil {
			targetSite = parsed
		}
	}
	if err := b.requireSiteLogin(targetSite); err != nil {
		return download.Task{}, err
	}
	task, err := b.dl.Upgrade(ctx, key)
	if err != nil {
		b.logError("Upgrade", err)
		return download.Task{}, err
	}
	b.events.PublishSite(EventTaskUpdated, string(targetSite), "enqueued:upgrade:"+key)
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
