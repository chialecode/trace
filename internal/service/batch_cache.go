package service

import (
	"context"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/site/weibo"
	"github.com/chialecode/trace/internal/site/x"
)

type queuePartEnqueuer func(ctx context.Context) ([]string, error)

type queueEnqueued struct {
	taskIDs      []string
	itemKey      string
	title        string
	nextParts    []queuePartEnqueuer
	pendingPages []int
	probedNoTask bool
}

// queueCandidate is one post from a creator's user page list that the background
// cache queue may inspect, skip, or enqueue into the download manager.
type queueCandidate struct {
	key           string
	title         string
	uncacheable   bool
	inspectCached func(meta library.ItemMetadata, queueOrigin string) cachedInspectDecision
	enqueue       func(ctx context.Context) (queueEnqueued, error)
}

type queuePager func(ctx context.Context, cursor string) (items []queueCandidate, nextCursor string, hasMore bool, err error)

type queueStepMeta struct {
	completionNote string
}

type queueStepMetaKey struct{}

func withQueueStepMeta(ctx context.Context) (context.Context, *queueStepMeta) {
	meta := &queueStepMeta{}
	return context.WithValue(ctx, queueStepMetaKey{}, meta), meta
}

func setQueueStepCompletionNote(ctx context.Context, note string) {
	if meta, ok := ctx.Value(queueStepMetaKey{}).(*queueStepMeta); ok && meta != nil {
		meta.completionNote = strings.TrimSpace(note)
	}
}

func normalizeCacheQueueTarget(rawSite, rawList, rawUser string) (site.ID, string, string, error) {
	siteID, err := normalizeSiteID(rawSite)
	if err != nil {
		return "", "", "", err
	}
	list := strings.ToLower(strings.TrimSpace(rawList))
	switch siteID {
	case site.Bilibili:
		if list == "" {
			list = "dynamics"
		}
		if list != "dynamics" && list != "videos" {
			return "", "", "", &site.Error{Site: siteID, Kind: site.ErrKindBadRequest, Message: "不支持的哔哩哔哩列表类型"}
		}
		mid, perr := strconv.ParseInt(strings.TrimSpace(rawUser), 10, 64)
		if perr != nil || mid <= 0 {
			return "", "", "", &site.Error{Site: siteID, Kind: site.ErrKindBadRequest, Message: "用户 UID 无效"}
		}
		return site.Bilibili, list, strconv.FormatInt(mid, 10), nil

	case site.Douyin:
		if list == "" {
			list = "works"
		}
		if list != "works" {
			return "", "", "", &site.Error{Site: siteID, Kind: site.ErrKindBadRequest, Message: "不支持的抖音列表类型"}
		}
		secUID := strings.TrimSpace(rawUser)
		if secUID == "" || strings.ContainsAny(secUID, " \t\r\n?=&;/\\:") {
			return "", "", "", &site.Error{Site: siteID, Kind: site.ErrKindBadRequest, Message: "抖音用户无效"}
		}
		return site.Douyin, "works", secUID, nil

	case site.Weibo:
		if list == "" {
			list = "posts"
		}
		if list != "posts" {
			return "", "", "", &site.Error{Site: siteID, Kind: site.ErrKindBadRequest, Message: "不支持的微博列表类型"}
		}
		normUID, uerr := site.NormalizeUID(site.Weibo, rawUser)
		if uerr != nil {
			return "", "", "", &site.Error{Site: siteID, Kind: site.ErrKindBadRequest, Message: "微博用户 UID 无效"}
		}
		return site.Weibo, "posts", normUID, nil

	case site.X:
		if list == "" {
			list = "posts"
		}
		if list != "posts" {
			return "", "", "", &site.Error{Site: siteID, Kind: site.ErrKindBadRequest, Message: "不支持的 X 列表类型"}
		}
		normUID, uerr := site.NormalizeUID(site.X, rawUser)
		if uerr != nil {
			return "", "", "", &site.Error{Site: siteID, Kind: site.ErrKindBadRequest, Message: "X 用户 rest_id 无效"}
		}
		return site.X, "posts", normUID, nil

	default:
		return "", "", "", &site.Error{Kind: site.ErrKindBadRequest, Message: "不支持的站点"}
	}
}

func cacheQueueListLabel(siteID, list string) string {
	switch strings.ToLower(strings.TrimSpace(siteID)) {
	case string(site.Douyin):
		return "作品"
	case string(site.Weibo):
		return "微博"
	case string(site.X):
		return "帖子"
	default:
		if list == "videos" {
			return "投稿"
		}
		return "动态"
	}
}

func (b *Backend) buildQueueLoadedAndPager(siteID site.ID, list, normUser string, req CacheQueueRequest) ([]queueCandidate, queuePager, error) {
	pager, err := b.buildQueuePager(siteID, list, normUser, req.UserName)
	if err != nil {
		return nil, nil, err
	}
	var loaded []queueCandidate
	switch {
	case siteID == site.Bilibili && list == "videos":
		for _, v := range req.Videos {
			loaded = append(loaded, b.videoItemQueueCandidate(v, queueForMid(normUser)))
		}
	case siteID == site.Bilibili:
		for i := range req.Dynamics {
			loaded = append(loaded, b.dynamicQueueCandidate(req.Dynamics[i]))
		}
	case siteID == site.Douyin:
		for _, w := range req.Works {
			loaded = append(loaded, b.workQueueCandidate(w, normUser, req.UserName))
		}
	case siteID == site.Weibo:
		for _, p := range req.WeiboPosts {
			loaded = append(loaded, b.weiboPostViewQueueCandidate(p))
		}
	case siteID == site.X:
		for _, p := range req.XPosts {
			loaded = append(loaded, b.xPostDTOQueueCandidate(p))
		}
	}
	return loaded, pager, nil
}

// buildQueuePager pages the queue's list; userName is the account's name,
// recorded on co-creations cached for them.
func (b *Backend) buildQueuePager(siteID site.ID, list, normUser, userName string) (queuePager, error) {
	switch {
	case siteID == site.Bilibili && list == "videos":
		mid, err := strconv.ParseInt(normUser, 10, 64)
		if err != nil || mid <= 0 {
			return nil, &site.Error{Site: siteID, Kind: site.ErrKindBadRequest, Message: "用户 UID 无效"}
		}
		return func(ctx context.Context, cursor string) ([]queueCandidate, string, bool, error) {
			page, _ := strconv.Atoi(cursor)
			if page < 1 {
				page = 1
			}
			res, err := b.bili.UserVideos(ctx, mid, page, 30, "pubdate", "")
			if err != nil {
				return nil, "", false, err
			}
			if page == 1 {
				b.recordAccountWorks(site.Bilibili, strconv.FormatInt(mid, 10), res.Total)
			}
			out := make([]queueCandidate, 0, len(res.Items))
			for _, v := range res.Items {
				out = append(out, b.videoItemQueueCandidate(v, mid))
			}
			hasMore := page*max(res.PageSize, 1) < res.Total && len(res.Items) > 0
			return out, strconv.Itoa(page + 1), hasMore, nil
		}, nil

	case siteID == site.Bilibili:
		mid, err := strconv.ParseInt(normUser, 10, 64)
		if err != nil || mid <= 0 {
			return nil, &site.Error{Site: siteID, Kind: site.ErrKindBadRequest, Message: "用户 UID 无效"}
		}
		return func(ctx context.Context, cursor string) ([]queueCandidate, string, bool, error) {
			res, err := b.bili.UserDynamics(ctx, mid, cursor)
			if err != nil {
				return nil, "", false, err
			}
			out := make([]queueCandidate, 0, len(res.Items))
			for i := range res.Items {
				out = append(out, b.dynamicQueueCandidate(res.Items[i]))
			}
			return out, res.Offset, res.HasMore && res.Offset != "", nil
		}, nil

	case siteID == site.Douyin:
		secUID := strings.TrimSpace(normUser)
		if secUID == "" {
			return nil, &site.Error{Site: siteID, Kind: site.ErrKindBadRequest, Message: "抖音用户无效"}
		}
		return func(ctx context.Context, cursor string) ([]queueCandidate, string, bool, error) {
			res, details, err := b.douyinUserWorks(ctx, secUID, cursor, nil)
			if err != nil {
				return nil, "", false, err
			}
			if res.UncertainEmpty {
				return nil, "", false, &site.Error{
					Site:    site.Douyin,
					Kind:    site.ErrKindRiskControl,
					Message: firstNonEmpty(res.UncertainReason, "抖音返回空作品列表，可能被拦截或登录已失效"),
				}
			}
			out := make([]queueCandidate, 0, len(details))
			for i := range details {
				out = append(out, b.douyinDetailQueueCandidate(details[i], secUID, userName))
			}
			return out, res.NextCursor, res.HasMore && res.NextCursor != "", nil
		}, nil

	case siteID == site.Weibo:
		uid, uerr := site.NormalizeUID(site.Weibo, normUser)
		if uerr != nil {
			return nil, &site.Error{Site: siteID, Kind: site.ErrKindBadRequest, Message: "微博用户 UID 无效"}
		}
		return func(ctx context.Context, cursor string) ([]queueCandidate, string, bool, error) {
			feature := weibo.FeatureAll
			if b.weiboFullCacheUseFeatureOriginal() {
				feature = weibo.FeatureOriginal
			}
			pageNum, sinceID := weibo.ParseCursor(cursor)
			page, err := b.weibo.GetUserPosts(ctx, weibo.UserPostsRequest{
				UID:               uid,
				Page:              pageNum,
				SinceID:           sinceID,
				Feature:           feature,
				CrossCheckProfile: pageNum == 1,
				StreamOptions: weibo.StreamSelectOptions{
					PreferredCodec: b.preferredWeiboCodec(),
					MaxResolution:  b.currentWeiboMaxResolution(),
				},
			})
			if err != nil {
				return nil, "", false, err
			}
			if pageNum == 1 {
				if page.Author != nil && page.Author.StatusesCount > 0 {
					b.recordAccountWorks(site.Weibo, uid, page.Author.StatusesCount)
				} else if page.Total > 0 {
					b.recordAccountWorks(site.Weibo, uid, page.Total)
				}
			}
			if page.UncertainEmpty {
				return nil, "", false, &site.Error{
					Site:    site.Weibo,
					Kind:    site.ErrKindRiskControl,
					Message: firstNonEmpty(page.UncertainReason, "微博返回空列表，可能被拦截或登录已失效"),
				}
			}
			if page.BottomTipsVisible {
				setQueueStepCompletionNote(ctx, firstNonEmpty(page.BottomTipsText, "已到达博主设置的可见范围"))
			}
			out := make([]queueCandidate, 0, len(page.Posts))
			for i := range page.Posts {
				out = append(out, b.weiboPostQueueCandidate(page.Posts[i]))
			}
			nextCursor := ""
			hasMore := page.HasMore && !page.BottomTipsVisible
			if hasMore {
				nextCursor = weibo.FormatCursor(page.NextPage, page.NextSinceID)
				if nextCursor == "" {
					hasMore = false
				}
			}
			return out, nextCursor, hasMore, nil
		}, nil

	case siteID == site.X:
		uid, uerr := site.NormalizeUID(site.X, normUser)
		if uerr != nil {
			return nil, &site.Error{Site: siteID, Kind: site.ErrKindBadRequest, Message: "X 用户 rest_id 无效"}
		}
		totalFetched := 0
		return func(ctx context.Context, cursor string) ([]queueCandidate, string, bool, error) {
			cleanCursor := strings.TrimSpace(cursor)
			page, err := b.x.GetUserTweets(ctx, x.UserTweetsRequest{
				RestID:            uid,
				Cursor:            cleanCursor,
				CrossCheckProfile: cleanCursor == "",
				TotalFetched:      totalFetched,
			})
			if err != nil {
				return nil, "", false, err
			}
			totalFetched += len(page.Posts)
			if cleanCursor == "" && page.Author != nil && page.Author.StatusesCount > 0 {
				b.recordAccountWorks(site.X, uid, page.Author.StatusesCount)
			}
			if page.UncertainEmpty {
				return nil, "", false, &site.Error{
					Site:    site.X,
					Kind:    site.ErrKindRiskControl,
					Message: firstNonEmpty(page.UncertainReason, "X 返回空列表，可能被拦截或登录已失效"),
				}
			}
			if page.ReachedVisibleLimit || page.BottomTipsVisible {
				setQueueStepCompletionNote(ctx, firstNonEmpty(page.BottomTipsText, x.VisibleTimelineLimitText))
			}
			out := make([]queueCandidate, 0, len(page.Posts))
			for i := range page.Posts {
				out = append(out, b.xPostQueueCandidate(page.Posts[i]))
			}
			nextCursor := ""
			hasMore := page.HasMore && !page.ReachedVisibleLimit && !page.BottomTipsVisible
			if hasMore {
				nextCursor = strings.TrimSpace(page.NextCursor)
				if nextCursor == "" {
					hasMore = false
				}
			}
			return out, nextCursor, hasMore, nil
		}, nil

	default:
		return nil, &site.Error{Kind: site.ErrKindBadRequest, Message: "不支持的站点"}
	}
}

func queueForMid(normUser string) int64 {
	mid, err := strconv.ParseInt(strings.TrimSpace(normUser), 10, 64)
	if err != nil || mid <= 0 {
		return 0
	}
	return mid
}

// videoQueueCandidate enqueues one 投稿 video for the queue's creator forMid;
// a collaboration video they are a member of is stored under their folder
// (DEC-056).
func (b *Backend) videoQueueCandidate(bvid, fallbackTitle string, forMid int64) queueCandidate {
	return b.videoItemQueueCandidate(bilibili.UserVideoItem{
		BVID:  bvid,
		Title: fallbackTitle,
	}, forMid)
}

func (b *Backend) videoItemQueueCandidate(v bilibili.UserVideoItem, forMid int64) queueCandidate {
	itemCopy := v
	cleanID := strings.TrimSpace(itemCopy.BVID)
	fallbackTitle := itemCopy.Title
	var itemKey string
	if cleanID != "" {
		itemKey = library.CanonicalItemKey("bilibili", "video", cleanID)
	}
	return queueCandidate{
		key:   itemKey,
		title: trimQueueSummaryTitle(fallbackTitle),
		inspectCached: func(meta library.ItemMetadata, queueOrigin string) cachedInspectDecision {
			return b.inspectBilibiliCachedVideo(itemCopy, meta, queueOrigin, forMid)
		},
		enqueue: func(ctx context.Context) (queueEnqueued, error) {
			vd, err := b.bili.VideoDetail(ctx, cleanID)
			if err != nil {
				return queueEnqueued{}, err
			}
			if len(vd.Pages) == 0 {
				return queueEnqueued{}, &site.Error{Site: site.Bilibili, Kind: site.ErrKindUnavailable, Message: "视频无可用分P"}
			}
			detailCopy := vd
			firstPage := vd.Pages[0].Page
			tasks, err := b.EnqueueVideoCache(ctx, download.VideoEnqueueRequest{
				BVID:   cleanID,
				Detail: &detailCopy,
				Pages:  []int{firstPage},
				Source: "queue",
				ForMid: forMid,
			})
			if err != nil {
				return queueEnqueued{}, err
			}
			var ids []string
			for _, t := range tasks {
				ids = append(ids, t.TaskID)
			}
			var nextParts []queuePartEnqueuer
			var pendingPages []int
			if len(vd.Pages) > 1 && b.GetSettings().Download.MultiPart != "first" {
				for _, vp := range vd.Pages[1:] {
					pageNum := vp.Page
					pendingPages = append(pendingPages, pageNum)
					nextParts = append(nextParts, b.makeBilibiliPartEnqueuer(cleanID, &detailCopy, pageNum, forMid))
				}
			}
			title := firstNonEmpty(vd.Title, fallbackTitle, cleanID)
			return queueEnqueued{
				taskIDs:      ids,
				itemKey:      itemKey,
				title:        trimQueueSummaryTitle(title),
				nextParts:    nextParts,
				pendingPages: pendingPages,
			}, nil
		},
	}
}

func (b *Backend) makeBilibiliPartEnqueuer(bvid string, detail *bilibili.VideoDetail, pageNum int, forMid int64) queuePartEnqueuer {
	return func(partCtx context.Context) ([]string, error) {
		ptasks, perr := b.EnqueueVideoCache(partCtx, download.VideoEnqueueRequest{
			BVID:   bvid,
			Detail: detail,
			Pages:  []int{pageNum},
			Source: "queue",
			ForMid: forMid,
		})
		if perr != nil {
			return nil, perr
		}
		pids := make([]string, 0, len(ptasks))
		for _, pt := range ptasks {
			pids = append(pids, pt.TaskID)
		}
		return pids, nil
	}
}

func (b *Backend) buildBilibiliPartEnqueuers(bvid string, pages []int, forMid int64) []queuePartEnqueuer {
	bvid = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(bvid), "bilibili:video:"))
	if bvid == "" || len(pages) == 0 {
		return nil
	}
	out := make([]queuePartEnqueuer, 0, len(pages))
	for _, p := range pages {
		if p > 0 {
			out = append(out, b.makeBilibiliPartEnqueuer(bvid, nil, p, forMid))
		}
	}
	return out
}

func (b *Backend) dynamicQueueCandidate(d bilibili.DynamicItem) queueCandidate {
	item := d
	cleanID := strings.TrimSpace(d.ID)
	var itemKey string
	if cleanID != "" {
		itemKey = library.CanonicalItemKey("bilibili", "dynamic", cleanID)
	}
	title := trimQueueSummaryTitle(firstNonEmpty(d.Title, d.Text, cleanID))
	isLiveRcmd := strings.EqualFold(strings.TrimSpace(d.Type), "DYNAMIC_TYPE_LIVE_RCMD")
	var inspectFn func(meta library.ItemMetadata, queueOrigin string) cachedInspectDecision
	if !isLiveRcmd {
		inspectFn = func(meta library.ItemMetadata, queueOrigin string) cachedInspectDecision {
			return b.inspectBilibiliCachedDynamic(item, meta, queueOrigin)
		}
	}
	return queueCandidate{
		key:           itemKey,
		title:         title,
		uncacheable:   isLiveRcmd,
		inspectCached: inspectFn,
		enqueue: func(ctx context.Context) (queueEnqueued, error) {
			t, err := b.EnqueueDynamicCache(ctx, download.DynamicEnqueueRequest{
				DynamicID: item.ID,
				Item:      &item,
				Source:    "queue",
			})
			if err != nil {
				return queueEnqueued{}, err
			}
			return queueEnqueued{
				taskIDs: []string{t.TaskID},
				itemKey: firstNonEmpty(t.ItemKey, itemKey),
				title:   trimQueueSummaryTitle(firstNonEmpty(t.Title, title, cleanID)),
			}, nil
		},
	}
}

func (b *Backend) douyinDetailQueueCandidate(d douyin.WorkDetail, forUID, forName string) queueCandidate {
	detailCopy := cloneDouyinWorkDetail(&d)
	id := strings.TrimSpace(detailCopy.ID)
	key := douyinCacheKeyForKind(detailCopy.Kind, id)
	title := trimQueueSummaryTitle(firstNonEmpty(detailCopy.Title, detailCopy.Desc, id))
	if b != nil && b.douyinWorkCache != nil {
		b.douyinWorkCache.put(&detailCopy, b.preferredDouyinCodec())
	}
	return queueCandidate{
		key:         key,
		title:       title,
		uncacheable: detailCopy.Uncacheable || detailCopy.Kind == douyin.WorkKindUnsupported,
		inspectCached: func(meta library.ItemMetadata, queueOrigin string) cachedInspectDecision {
			return b.inspectDouyinCachedWork(detailCopy, meta, queueOrigin, forUID, forName)
		},
		enqueue: func(ctx context.Context) (queueEnqueued, error) {
			var planPtr *download.WorkDownloadPlan
			if canReuseDouyinWorkPlan(&detailCopy) {
				if built, err := b.buildDouyinWorkPlanFromDetail(ctx, &detailCopy, b.currentWorkPlanOptions()); err == nil {
					planPtr = &built
				}
			}
			t, err := b.enqueueDouyinWorkWithPlan(ctx, id, "queue", forUID, forName, planPtr)
			if err != nil {
				return queueEnqueued{}, err
			}
			return queueEnqueued{
				taskIDs: []string{t.TaskID},
				itemKey: firstNonEmpty(t.ItemKey, key),
				title:   trimQueueSummaryTitle(firstNonEmpty(t.Title, title, id)),
			}, nil
		},
	}
}

// workQueueCandidate enqueues one work from account forUID's works list; a
// co-creation by someone else is stored under forUID's folder (DEC-063).
func (b *Backend) workQueueCandidate(w DouyinWorkCardDTO, forUID, forName string) queueCandidate {
	id := strings.TrimSpace(w.ID)
	if b != nil && b.douyinWorkCache != nil && id != "" {
		if cached, ok := b.douyinWorkCache.get(id, b.preferredDouyinCodec()); ok {
			return b.douyinDetailQueueCandidate(*cached, forUID, forName)
		}
	}
	key := w.CacheKey
	if key == "" && id != "" {
		key = douyinCacheKeyForKind(w.Kind, id)
	}
	title := trimQueueSummaryTitle(firstNonEmpty(w.Title, w.Desc, id))
	return queueCandidate{
		key:         key,
		title:       title,
		uncacheable: w.Uncacheable || w.Kind == "unsupported",
		enqueue: func(ctx context.Context) (queueEnqueued, error) {
			t, err := b.enqueueDouyinWorkWithSource(ctx, id, "queue", forUID, forName)
			if err != nil {
				return queueEnqueued{}, err
			}
			return queueEnqueued{
				taskIDs: []string{t.TaskID},
				itemKey: firstNonEmpty(t.ItemKey, key),
				title:   trimQueueSummaryTitle(firstNonEmpty(t.Title, title, id)),
			}, nil
		},
	}
}

func isWeiboNonOriginalPost(isRetweet, isFastRetweet bool, hasRetweeted bool, oriMid, ruleType string) bool {
	return isRetweet || isFastRetweet || hasRetweeted || strings.TrimSpace(oriMid) != "" || strings.EqualFold(strings.TrimSpace(ruleType), weibo.RuleTypeForward)
}

// weiboPostQueueCandidate converts a list-fetched Weibo post into a cache queue candidate.
// Full cache only caches original posts (skipping retweets and fast-retweets) and reuses
// the in-memory list payload to build a download plan when complete (zero signed URLs on disk).
func (b *Backend) weiboPostQueueCandidate(p weibo.Post) queueCandidate {
	postCopy := p
	mid := strings.TrimSpace(postCopy.Mid)
	key := ""
	if mid != "" {
		key = weibo.PostItemKey(mid)
	}
	if b != nil && b.weiboPostCache != nil {
		b.weiboPostCache.put(&postCopy, b.preferredWeiboCodec(), b.currentWeiboMaxResolution())
	}
	title := trimQueueSummaryTitle(firstNonEmpty(postCopy.Text, postCopy.TextRaw, mid))
	nonOriginal := isWeiboNonOriginalPost(postCopy.IsRetweet, postCopy.IsFastRetweet, postCopy.Retweeted != nil, postCopy.OriMid, postCopy.RuleType)
	var inspectFn func(meta library.ItemMetadata, queueOrigin string) cachedInspectDecision
	if !nonOriginal {
		inspectFn = func(meta library.ItemMetadata, queueOrigin string) cachedInspectDecision {
			return b.inspectWeiboCachedPost(postCopy, meta, queueOrigin)
		}
	}
	return queueCandidate{
		key:           key,
		title:         title,
		uncacheable:   nonOriginal || postCopy.Unavailable,
		inspectCached: inspectFn,
		enqueue: func(ctx context.Context) (queueEnqueued, error) {
			var planPtr *download.WorkDownloadPlan
			if canReuseWeiboPostPlan(&postCopy) {
				if built, err := b.buildWeiboWorkPlanFromPost(&postCopy); err == nil {
					planPtr = &built
				}
			}
			t, err := b.enqueueWeiboPostWithSource(ctx, mid, "queue", nil, planPtr)
			if err != nil {
				return queueEnqueued{}, err
			}
			return queueEnqueued{
				taskIDs: []string{t.TaskID},
				itemKey: firstNonEmpty(t.ItemKey, key),
				title:   trimQueueSummaryTitle(firstNonEmpty(t.Title, title, mid)),
			}, nil
		},
	}
}

func (b *Backend) weiboPostViewQueueCandidate(p WeiboPostView) queueCandidate {
	mid := strings.TrimSpace(p.Mid)
	if b != nil && b.weiboPostCache != nil && mid != "" {
		if cached, ok := b.weiboPostCache.get(mid, b.preferredWeiboCodec(), b.currentWeiboMaxResolution()); ok {
			return b.weiboPostQueueCandidate(*cached)
		}
	}
	key := p.CacheKey
	if key == "" && mid != "" {
		key = weibo.PostItemKey(mid)
	}
	title := trimQueueSummaryTitle(firstNonEmpty(p.Text, p.TextRaw, mid))
	nonOriginal := isWeiboNonOriginalPost(p.IsRetweet, p.IsFastRetweet, p.Retweeted != nil, p.OriMid, p.RuleType)
	return queueCandidate{
		key:         key,
		title:       title,
		uncacheable: nonOriginal || p.Unavailable,
		enqueue: func(ctx context.Context) (queueEnqueued, error) {
			t, err := b.enqueueWeiboPostWithSource(ctx, mid, "queue", nil, nil)
			if err != nil {
				return queueEnqueued{}, err
			}
			return queueEnqueued{
				taskIDs: []string{t.TaskID},
				itemKey: firstNonEmpty(t.ItemKey, key),
				title:   trimQueueSummaryTitle(firstNonEmpty(t.Title, title, mid)),
			}, nil
		},
	}
}

func isXNonOriginalPost(isRetweet, isQuote, hasRetweeted, hasQuoted bool, ruleType string) bool {
	return isRetweet || isQuote || hasRetweeted || hasQuoted || strings.EqualFold(strings.TrimSpace(ruleType), x.RuleTypeForward)
}

// xPostQueueCandidate converts a list-fetched X post into a cache queue candidate.
// Full cache only caches original posts (skipping retweets and quotes) and reuses
// the in-memory list payload to build a download plan when complete.
func (b *Backend) xPostQueueCandidate(p x.Post) queueCandidate {
	postCopy := p
	id := strings.TrimSpace(postCopy.ID)
	key := ""
	if id != "" {
		key = x.PostItemKey(id)
	}
	if b != nil && b.xPostCache != nil {
		b.xPostCache.put(&postCopy, false)
	}
	title := trimQueueSummaryTitle(firstNonEmpty(postCopy.Text, id))
	nonOriginal := isXNonOriginalPost(postCopy.IsRetweet, postCopy.IsQuote, postCopy.Retweeted != nil, postCopy.Quoted != nil, postCopy.RuleType)
	var inspectFn func(meta library.ItemMetadata, queueOrigin string) cachedInspectDecision
	if !nonOriginal {
		inspectFn = func(meta library.ItemMetadata, queueOrigin string) cachedInspectDecision {
			return b.inspectXCachedPost(postCopy, meta, queueOrigin)
		}
	}
	return queueCandidate{
		key:           key,
		title:         title,
		uncacheable:   nonOriginal || postCopy.Unavailable,
		inspectCached: inspectFn,
		enqueue: func(ctx context.Context) (queueEnqueued, error) {
			var planPtr *download.WorkDownloadPlan
			if canReuseXPostPlan(&postCopy, false) {
				if built, err := b.buildXWorkPlanFromPost(&postCopy); err == nil {
					planPtr = &built
				}
			}
			t, err := b.enqueueXPostWithSource(ctx, id, "queue", nil, planPtr)
			if err != nil {
				return queueEnqueued{}, err
			}
			return queueEnqueued{
				taskIDs: []string{t.TaskID},
				itemKey: firstNonEmpty(t.ItemKey, key),
				title:   trimQueueSummaryTitle(firstNonEmpty(t.Title, title, id)),
			}, nil
		},
	}
}

func (b *Backend) xPostDTOQueueCandidate(p XPostDTO) queueCandidate {
	id := strings.TrimSpace(p.ID)
	if b != nil && b.xPostCache != nil && id != "" {
		if cached, ok := b.xPostCache.get(id); ok {
			return b.xPostQueueCandidate(*cached)
		}
	}
	key := p.CacheKey
	if key == "" && id != "" {
		key = x.PostItemKey(id)
	}
	title := trimQueueSummaryTitle(firstNonEmpty(p.Text, id))
	nonOriginal := isXNonOriginalPost(p.IsRetweet, p.IsQuote, p.Retweeted != nil, p.Quoted != nil, p.RuleType)
	return queueCandidate{
		key:         key,
		title:       title,
		uncacheable: nonOriginal || p.Unavailable,
		enqueue: func(ctx context.Context) (queueEnqueued, error) {
			t, err := b.enqueueXPostWithSource(ctx, id, "queue", nil, nil)
			if err != nil {
				return queueEnqueued{}, err
			}
			return queueEnqueued{
				taskIDs: []string{t.TaskID},
				itemKey: firstNonEmpty(t.ItemKey, key),
				title:   trimQueueSummaryTitle(firstNonEmpty(t.Title, title, id)),
			}, nil
		},
	}
}

func trimQueueSummaryTitle(s string) string {
	s = strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	if utf8.RuneCountInString(s) > 80 {
		r := []rune(s)
		return string(r[:80]) + "…"
	}
	return s
}
