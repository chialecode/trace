package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/douyin"
)

// ParseAndCacheResult summarizes a single-item parse & cache enqueue triggered
// from the desktop companion API (FR-MOB-030). It intentionally omits online
// detail previews, remote media URLs, cookies, and local filesystem paths.
type ParseAndCacheResult struct {
	Site          string          `json:"site"`
	Kind          string          `json:"kind"` // "video" or "dynamic"
	TargetID      string          `json:"targetId"`
	ItemKey       string          `json:"itemKey,omitempty"`
	Title         string          `json:"title,omitempty"`
	AuthorName    string          `json:"authorName,omitempty"`
	AlreadyCached bool            `json:"alreadyCached,omitempty"`
	Tasks         []download.Task `json:"tasks,omitempty"`
	Message       string          `json:"message,omitempty"`
}

// ParseAndCache recognizes a shared link/text across all 4 supported sites,
// enforces desktop login and site pacing/backoff rules, fetches only the target
// item's metadata (never user timelines or live rooms), and enqueues local cache
// task(s) with Source="manual".
func (b *Backend) ParseAndCache(ctx context.Context, rawInput string) (ParseAndCacheResult, error) {
	if err := b.requireDesktop(); err != nil {
		return ParseAndCacheResult{}, err
	}
	if err := b.requireAnyLogin(); err != nil {
		return ParseAndCacheResult{}, err
	}
	trimmed := strings.TrimSpace(rawInput)
	if trimmed == "" {
		return ParseAndCacheResult{}, &site.Error{
			Kind:    site.ErrKindBadRequest,
			Message: "请输入要缓存的链接或分享文案",
		}
	}

	siteID, normalizedInput, err := b.registry.DetectSite(trimmed)
	if err != nil {
		return ParseAndCacheResult{}, err
	}
	if err := b.requireSiteLogin(siteID); err != nil {
		return ParseAndCacheResult{}, err
	}

	switch siteID {
	case site.Bilibili:
		return b.parseAndCacheBilibili(ctx, normalizedInput)
	case site.Douyin:
		return b.parseAndCacheDouyin(ctx, normalizedInput)
	case site.Weibo:
		return b.parseAndCacheWeibo(ctx, normalizedInput)
	case site.X:
		return b.parseAndCacheX(ctx, normalizedInput)
	default:
		return ParseAndCacheResult{}, &site.Error{
			Site:        siteID,
			Kind:        site.ErrKindUnsupportedLink,
			Unsupported: true,
			Message:     "不支持的站点链接：" + string(siteID),
		}
	}
}

func (b *Backend) parseAndCacheBilibili(ctx context.Context, normalizedInput string) (ParseAndCacheResult, error) {
	probeCtx := b.manualProbeContext(ctx)
	target, err := b.bili.ResolveLink(probeCtx, normalizedInput)
	if err != nil {
		err = b.finishManualSiteRequest(ctx, site.Bilibili, err)
		b.logError("ParseAndCache.BiliResolve", err)
		return ParseAndCacheResult{}, err
	}
	if target.Unsupported {
		return ParseAndCacheResult{}, &site.Error{
			Site:        site.Bilibili,
			Kind:        site.ErrKindUnsupportedLink,
			Unsupported: true,
			Message:     firstNonEmpty(target.Reason, "暂不支持缓存这种哔哩哔哩链接"),
		}
	}

	switch target.Kind {
	case "video":
		vd, err := b.bili.VideoDetail(probeCtx, target.ID)
		if err != nil {
			err = b.finishManualSiteRequest(ctx, site.Bilibili, err)
			b.logError("ParseAndCache.BiliVideoDetail", err)
			return ParseAndCacheResult{}, err
		}
		_ = b.finishManualSiteRequest(ctx, site.Bilibili, nil)
		if vd.Author.Mid > 0 && vd.Author.Face != "" {
			b.rememberAccountAvatar(string(site.Bilibili), strconv.FormatInt(vd.Author.Mid, 10), vd.Author.Face)
		}
		if vd.IsUpowerLocked() {
			return ParseAndCacheResult{}, &site.Error{
				Site:    site.Bilibili,
				Kind:    site.ErrKindForbidden,
				Message: "该视频为充电专属内容，当前账号无法缓存",
			}
		}
		if item, ok, _ := b.lib.GetItem(vd.BVID); ok && item.Status == "complete" {
			return ParseAndCacheResult{
				Site:          string(site.Bilibili),
				Kind:          "video",
				TargetID:      vd.BVID,
				ItemKey:       item.ItemKey,
				Title:         firstNonEmpty(vd.Title, item.Title),
				AuthorName:    firstNonEmpty(vd.Author.Name, item.Author.Name),
				AlreadyCached: true,
				Message:       "该视频已在媒体库中完成缓存",
			}, nil
		}
		tasks, err := b.EnqueueVideoCache(ctx, download.VideoEnqueueRequest{
			BVID:   vd.BVID,
			Source: "manual",
			Detail: &vd,
		})
		if err != nil {
			return ParseAndCacheResult{}, err
		}
		itemKey := library.CanonicalItemKey(string(site.Bilibili), "video", vd.BVID)
		if len(tasks) > 0 && tasks[0].ItemKey != "" {
			itemKey = tasks[0].ItemKey
		}
		return ParseAndCacheResult{
			Site:       string(site.Bilibili),
			Kind:       "video",
			TargetID:   vd.BVID,
			ItemKey:    itemKey,
			Title:      vd.Title,
			AuthorName: vd.Author.Name,
			Tasks:      tasks,
			Message:    fmt.Sprintf("已加入缓存队列（%d 项）", len(tasks)),
		}, nil

	case "dynamic":
		dyn, err := b.bili.DynamicDetail(probeCtx, target.ID)
		if err != nil {
			err = b.finishManualSiteRequest(ctx, site.Bilibili, err)
			b.logError("ParseAndCache.BiliDynamicDetail", err)
			return ParseAndCacheResult{}, err
		}
		_ = b.finishManualSiteRequest(ctx, site.Bilibili, nil)
		if dyn.Author.Mid > 0 && dyn.Author.Face != "" {
			b.rememberAccountAvatar(string(site.Bilibili), strconv.FormatInt(dyn.Author.Mid, 10), dyn.Author.Face)
		}
		if dyn.Blocked {
			return ParseAndCacheResult{}, &site.Error{
				Site:    site.Bilibili,
				Kind:    site.ErrKindForbidden,
				Message: firstNonEmpty(dyn.BlockedHint, "该动态为专属受限内容，当前账号无法缓存"),
			}
		}
		if strings.EqualFold(strings.TrimSpace(dyn.Type), "DYNAMIC_TYPE_LIVE_RCMD") {
			return ParseAndCacheResult{}, &site.Error{
				Site:        site.Bilibili,
				Kind:        site.ErrKindUnsupportedLink,
				Unsupported: true,
				Message:     "直播推送动态不支持作为内容缓存",
			}
		}
		itemKey := library.CanonicalItemKey(string(site.Bilibili), "dynamic", dyn.ID)
		title := firstNonEmpty(dyn.Title, dyn.Text, "哔哩哔哩动态 "+dyn.ID)
		if item, ok, _ := b.lib.GetItem(itemKey); ok && item.Status == "complete" {
			return ParseAndCacheResult{
				Site:          string(site.Bilibili),
				Kind:          "dynamic",
				TargetID:      dyn.ID,
				ItemKey:       item.ItemKey,
				Title:         firstNonEmpty(title, item.Title),
				AuthorName:    firstNonEmpty(dyn.Author.Name, item.Author.Name),
				AlreadyCached: true,
				Message:       "该动态已在媒体库中完成缓存",
			}, nil
		}
		task, err := b.EnqueueDynamicCache(ctx, download.DynamicEnqueueRequest{
			DynamicID: dyn.ID,
			Source:    "manual",
			Item:      &dyn,
		})
		if err != nil {
			return ParseAndCacheResult{}, err
		}
		if task.ItemKey != "" {
			itemKey = task.ItemKey
		}
		return ParseAndCacheResult{
			Site:       string(site.Bilibili),
			Kind:       "dynamic",
			TargetID:   dyn.ID,
			ItemKey:    itemKey,
			Title:      title,
			AuthorName: dyn.Author.Name,
			Tasks:      []download.Task{task},
			Message:    "已加入缓存队列",
		}, nil

	default:
		return ParseAndCacheResult{}, &site.Error{
			Site:        site.Bilibili,
			Kind:        site.ErrKindUnsupportedLink,
			Unsupported: true,
			Message:     "手机端仅支持缓存单个视频或动态链接，用户主页与直播间请在电脑端操作",
		}
	}
}

func (b *Backend) parseAndCacheDouyin(ctx context.Context, normalizedInput string) (ParseAndCacheResult, error) {
	probeCtx := b.manualProbeContext(ctx)
	target, err := b.douyin.ResolveLink(probeCtx, normalizedInput)
	if err != nil {
		err = b.finishManualSiteRequest(ctx, site.Douyin, err)
		b.logError("ParseAndCache.DouyinResolve", err)
		return ParseAndCacheResult{}, err
	}
	if target.Unsupported || (target.Kind != "video" && target.Kind != "note") {
		return ParseAndCacheResult{}, &site.Error{
			Site:        site.Douyin,
			Kind:        site.ErrKindUnsupportedLink,
			Unsupported: true,
			Message:     firstNonEmpty(target.Reason, "手机端仅支持缓存单个抖音视频或图文作品，用户主页与直播间请在电脑端操作"),
		}
	}

	wv, err := b.DouyinWork(ctx, target.ID)
	if err != nil {
		return ParseAndCacheResult{}, err
	}
	if wv.Uncacheable || wv.Kind == douyin.WorkKindUnsupported {
		return ParseAndCacheResult{}, &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindUnavailable,
			Message: firstNonEmpty(wv.UncacheableReason, "该抖音作品暂不支持缓存"),
		}
	}
	outKind := "video"
	if wv.Kind == douyin.WorkKindNote {
		outKind = "dynamic"
	}
	title := firstNonEmpty(wv.Title, wv.Desc, "抖音作品 "+wv.ID)
	if wv.CachedItem != nil && wv.CachedItem.Status == "complete" {
		return ParseAndCacheResult{
			Site:          string(site.Douyin),
			Kind:          outKind,
			TargetID:      wv.ID,
			ItemKey:       wv.CachedItem.ItemKey,
			Title:         title,
			AuthorName:    wv.Author.Nickname,
			AlreadyCached: true,
			Message:       "该作品已在媒体库中完成缓存",
		}, nil
	}
	task, err := b.CacheDouyinWork(ctx, wv.ID)
	if err != nil {
		return ParseAndCacheResult{}, err
	}
	return ParseAndCacheResult{
		Site:       string(site.Douyin),
		Kind:       outKind,
		TargetID:   wv.ID,
		ItemKey:    firstNonEmpty(task.ItemKey, wv.CacheKey),
		Title:      title,
		AuthorName: wv.Author.Nickname,
		Tasks:      []download.Task{task},
		Message:    "已加入缓存队列",
	}, nil
}

func (b *Backend) parseAndCacheWeibo(ctx context.Context, normalizedInput string) (ParseAndCacheResult, error) {
	probeCtx := b.manualProbeContext(ctx)
	target, err := b.weibo.ResolveLink(probeCtx, normalizedInput)
	if err != nil {
		err = b.finishManualSiteRequest(ctx, site.Weibo, err)
		b.logError("ParseAndCache.WeiboResolve", err)
		return ParseAndCacheResult{}, err
	}
	if target.Unsupported {
		return ParseAndCacheResult{}, &site.Error{
			Site:        site.Weibo,
			Kind:        site.ErrKindUnsupportedLink,
			Unsupported: true,
			Message:     firstNonEmpty(target.Reason, "暂不支持缓存这种微博链接"),
		}
	}

	switch target.Kind {
	case "dynamic":
		pv, err := b.WeiboPost(ctx, target.ID)
		if err != nil {
			return ParseAndCacheResult{}, err
		}
		if pv.Unavailable {
			return ParseAndCacheResult{}, &site.Error{
				Site:    site.Weibo,
				Kind:    site.ErrKindUnavailable,
				Message: firstNonEmpty(pv.UnavailableReason, "该微博不可访问或已删除"),
			}
		}
		title := weiboPostSummaryTitle(pv)
		if pv.CachedItem != nil && pv.CachedItem.Status == "complete" {
			return ParseAndCacheResult{
				Site:          string(site.Weibo),
				Kind:          "dynamic",
				TargetID:      pv.Mid,
				ItemKey:       pv.CachedItem.ItemKey,
				Title:         title,
				AuthorName:    pv.Author.ScreenName,
				AlreadyCached: true,
				Message:       "该微博已在媒体库中完成缓存",
			}, nil
		}
		var via *library.ItemVia
		hintUID := firstNonEmpty(target.HintAuthorUID, target.AuthorUID)
		if hintUID != "" && hintUID != pv.Author.UID {
			via = &library.ItemVia{
				Reason: "forward",
				Site:   string(site.Weibo),
				UID:    hintUID,
				Name:   b.resolveWeiboForwarderHintName(hintUID, pv.Author.UID, pv.Author.ScreenName),
			}
		}
		task, err := b.CacheWeiboPost(ctx, pv.Mid, via)
		if err != nil {
			return ParseAndCacheResult{}, err
		}
		return ParseAndCacheResult{
			Site:       string(site.Weibo),
			Kind:       "dynamic",
			TargetID:   pv.Mid,
			ItemKey:    firstNonEmpty(task.ItemKey, pv.CacheKey),
			Title:      title,
			AuthorName: pv.Author.ScreenName,
			Tasks:      []download.Task{task},
			Message:    "已加入缓存队列",
		}, nil

	case "video":
		vv, err := b.WeiboVideo(ctx, target.ID)
		if err != nil {
			return ParseAndCacheResult{}, err
		}
		if vv.HostPost != nil {
			pv := vv.HostPost
			if pv.Unavailable {
				return ParseAndCacheResult{}, &site.Error{
					Site:    site.Weibo,
					Kind:    site.ErrKindUnavailable,
					Message: firstNonEmpty(pv.UnavailableReason, "该微博视频对应的原微博不可访问"),
				}
			}
			title := weiboPostSummaryTitle(pv)
			if pv.CachedItem != nil && pv.CachedItem.Status == "complete" {
				return ParseAndCacheResult{
					Site:          string(site.Weibo),
					Kind:          "dynamic",
					TargetID:      pv.Mid,
					ItemKey:       pv.CachedItem.ItemKey,
					Title:         title,
					AuthorName:    pv.Author.ScreenName,
					AlreadyCached: true,
					Message:       "该微博已在媒体库中完成缓存",
				}, nil
			}
			task, err := b.CacheWeiboPost(ctx, pv.Mid, nil)
			if err != nil {
				return ParseAndCacheResult{}, err
			}
			return ParseAndCacheResult{
				Site:       string(site.Weibo),
				Kind:       "dynamic",
				TargetID:   pv.Mid,
				ItemKey:    firstNonEmpty(task.ItemKey, pv.CacheKey),
				Title:      title,
				AuthorName: pv.Author.ScreenName,
				Tasks:      []download.Task{task},
				Message:    "已加入缓存队列",
			}, nil
		}
		title := firstNonEmpty(vv.Title, "微博视频 "+vv.ObjectID)
		if vv.CachedItem != nil && vv.CachedItem.Status == "complete" {
			return ParseAndCacheResult{
				Site:          string(site.Weibo),
				Kind:          "video",
				TargetID:      vv.ObjectID,
				ItemKey:       vv.CachedItem.ItemKey,
				Title:         title,
				AuthorName:    vv.AuthorName,
				AlreadyCached: true,
				Message:       "该微博视频已在媒体库中完成缓存",
			}, nil
		}
		task, err := b.CacheWeiboVideo(ctx, vv.ObjectID)
		if err != nil {
			return ParseAndCacheResult{}, err
		}
		return ParseAndCacheResult{
			Site:       string(site.Weibo),
			Kind:       "video",
			TargetID:   vv.ObjectID,
			ItemKey:    firstNonEmpty(task.ItemKey, vv.CacheKey),
			Title:      title,
			AuthorName: vv.AuthorName,
			Tasks:      []download.Task{task},
			Message:    "已加入缓存队列",
		}, nil

	default:
		return ParseAndCacheResult{}, &site.Error{
			Site:        site.Weibo,
			Kind:        site.ErrKindUnsupportedLink,
			Unsupported: true,
			Message:     "手机端仅支持缓存单条微博或微博视频链接，用户主页与直播间请在电脑端操作",
		}
	}
}

func (b *Backend) parseAndCacheX(ctx context.Context, normalizedInput string) (ParseAndCacheResult, error) {
	probeCtx := b.manualProbeContext(ctx)
	target, err := b.x.ResolveLink(probeCtx, normalizedInput)
	if err != nil {
		err = b.finishManualSiteRequest(ctx, site.X, err)
		b.logError("ParseAndCache.XResolve", err)
		return ParseAndCacheResult{}, err
	}
	if target.Unsupported || target.Kind != "dynamic" {
		return ParseAndCacheResult{}, &site.Error{
			Site:        site.X,
			Kind:        site.ErrKindUnsupportedLink,
			Unsupported: true,
			Message:     firstNonEmpty(target.Reason, "手机端仅支持缓存单条 X 帖子链接，用户主页与列表请在电脑端操作"),
		}
	}

	pv, err := b.XPost(ctx, target.ID)
	if err != nil {
		return ParseAndCacheResult{}, err
	}
	if pv.Unavailable {
		return ParseAndCacheResult{}, &site.Error{
			Site:    site.X,
			Kind:    site.ErrKindUnavailable,
			Message: firstNonEmpty(pv.UnavailableReason, "该 X 帖子不可访问或已删除"),
		}
	}
	if pv.IsPreviewDisplay {
		return ParseAndCacheResult{}, &site.Error{
			Site:    site.X,
			Kind:    site.ErrKindUnavailable,
			Message: "该 X 帖子仅返回受限预览，暂无法缓存",
		}
	}
	if IsXPostDTOUncacheable(pv) {
		return ParseAndCacheResult{}, &site.Error{
			Site:        site.X,
			Kind:        site.ErrKindUnsupportedLink,
			Unsupported: true,
			Message:     "该 X 帖子不含可缓存媒体或正文",
		}
	}
	title := xPostSummaryTitle(pv)
	authorName := xPostAuthorDisplayName(pv.Author)
	if pv.CachedItem != nil && pv.CachedItem.Status == "complete" {
		return ParseAndCacheResult{
			Site:          string(site.X),
			Kind:          "dynamic",
			TargetID:      pv.ID,
			ItemKey:       pv.CachedItem.ItemKey,
			Title:         title,
			AuthorName:    authorName,
			AlreadyCached: true,
			Message:       "该帖子已在媒体库中完成缓存",
		}, nil
	}
	task, err := b.CacheXPost(ctx, pv.ID, nil)
	if err != nil {
		return ParseAndCacheResult{}, err
	}
	return ParseAndCacheResult{
		Site:       string(site.X),
		Kind:       "dynamic",
		TargetID:   pv.ID,
		ItemKey:    firstNonEmpty(task.ItemKey, pv.CacheKey),
		Title:      title,
		AuthorName: authorName,
		Tasks:      []download.Task{task},
		Message:    "已加入缓存队列",
	}, nil
}
