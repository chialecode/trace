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
)

type queuePartEnqueuer func(ctx context.Context) ([]string, error)

type queueEnqueued struct {
	taskIDs   []string
	itemKey   string
	title     string
	nextParts []queuePartEnqueuer
}

// queueCandidate is one post from a creator's user page list that the background
// cache queue may inspect, skip, or enqueue into the download manager.
type queueCandidate struct {
	key         string
	title       string
	uncacheable bool
	enqueue     func(ctx context.Context) (queueEnqueued, error)
}

type queuePager func(ctx context.Context, cursor string) (items []queueCandidate, nextCursor string, hasMore bool, err error)

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

	default:
		return "", "", "", &site.Error{Kind: site.ErrKindBadRequest, Message: "不支持的站点"}
	}
}

func cacheQueueListLabel(siteID, list string) string {
	if siteID == string(site.Douyin) {
		return "作品"
	}
	if list == "videos" {
		return "投稿"
	}
	return "动态"
}

func (b *Backend) buildQueueLoadedAndPager(siteID site.ID, list, normUser string, req CacheQueueRequest) ([]queueCandidate, queuePager, error) {
	pager, err := b.buildQueuePager(siteID, list, normUser)
	if err != nil {
		return nil, nil, err
	}
	var loaded []queueCandidate
	switch {
	case siteID == site.Bilibili && list == "videos":
		for _, v := range req.Videos {
			loaded = append(loaded, b.videoQueueCandidate(v.BVID, v.Title, queueForMid(normUser)))
		}
	case siteID == site.Bilibili:
		for i := range req.Dynamics {
			loaded = append(loaded, b.dynamicQueueCandidate(req.Dynamics[i]))
		}
	case siteID == site.Douyin:
		for _, w := range req.Works {
			loaded = append(loaded, b.workQueueCandidate(w))
		}
	}
	return loaded, pager, nil
}

func (b *Backend) buildQueuePager(siteID site.ID, list, normUser string) (queuePager, error) {
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
			out := make([]queueCandidate, 0, len(res.Items))
			for _, v := range res.Items {
				out = append(out, b.videoQueueCandidate(v.BVID, v.Title, mid))
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
			res, err := b.DouyinUserWorks(ctx, secUID, cursor)
			if err != nil {
				return nil, "", false, err
			}
			out := make([]queueCandidate, 0, len(res.Works))
			for _, w := range res.Works {
				out = append(out, b.workQueueCandidate(w))
			}
			return out, res.NextCursor, res.HasMore && res.NextCursor != "", nil
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
	cleanID := strings.TrimSpace(bvid)
	var itemKey string
	if cleanID != "" {
		itemKey = library.CanonicalItemKey("bilibili", "video", cleanID)
	}
	return queueCandidate{
		key:   itemKey,
		title: trimQueueSummaryTitle(fallbackTitle),
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
			if len(vd.Pages) > 1 && b.GetSettings().Download.MultiPart != "first" {
				for _, vp := range vd.Pages[1:] {
					pageNum := vp.Page
					nextParts = append(nextParts, func(partCtx context.Context) ([]string, error) {
						ptasks, perr := b.EnqueueVideoCache(partCtx, download.VideoEnqueueRequest{
							BVID:   cleanID,
							Detail: &detailCopy,
							Pages:  []int{pageNum},
							Source: "queue",
							ForMid: forMid,
						})
						if perr != nil {
							return nil, perr
						}
						var pids []string
						for _, pt := range ptasks {
							pids = append(pids, pt.TaskID)
						}
						return pids, nil
					})
				}
			}
			title := firstNonEmpty(vd.Title, fallbackTitle, cleanID)
			return queueEnqueued{
				taskIDs:   ids,
				itemKey:   itemKey,
				title:     trimQueueSummaryTitle(title),
				nextParts: nextParts,
			}, nil
		},
	}
}

func (b *Backend) dynamicQueueCandidate(d bilibili.DynamicItem) queueCandidate {
	item := d
	cleanID := strings.TrimSpace(d.ID)
	var itemKey string
	if cleanID != "" {
		itemKey = library.CanonicalItemKey("bilibili", "dynamic", cleanID)
	}
	title := trimQueueSummaryTitle(firstNonEmpty(d.Title, d.Text, cleanID))
	return queueCandidate{
		key:   itemKey,
		title: title,
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

func (b *Backend) workQueueCandidate(w DouyinWorkCardDTO) queueCandidate {
	id := strings.TrimSpace(w.ID)
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
			t, err := b.enqueueDouyinWorkWithSource(ctx, id, "queue")
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
