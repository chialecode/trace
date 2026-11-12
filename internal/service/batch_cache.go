package service

import (
	"context"
	"strconv"
	"strings"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
)

// CacheOlderChoices are the batch sizes the user page offers (DEC-046 ⑥).
var CacheOlderChoices = []int{10, 20, 50}

// DefaultCacheOlderLimit is the batch size chosen unless the user picks another.
const DefaultCacheOlderLimit = 20

// cacheOlderMaxPages bounds the pages one batch may fetch past the loaded list.
const cacheOlderMaxPages = 8

// CacheOlderRequest asks to queue the next uncached posts of one user's list,
// walking from the newest loaded item to older ones and past the loaded list
// with Cursor (DEC-046 ⑥). The loaded items cost no request.
type CacheOlderRequest struct {
	Site  string `json:"site"`  // "bilibili" or "douyin"
	List  string `json:"list"`  // "dynamics" or "videos" (Bilibili), "works" (Douyin)
	User  string `json:"user"`  // Bilibili mid or Douyin sec_uid
	Limit int    `json:"limit"` // 10, 20 or 50

	Dynamics []bilibili.DynamicItem   `json:"-"`
	Videos   []bilibili.UserVideoItem `json:"-"`
	Works    []DouyinWorkCardDTO      `json:"-"`
	Cursor   string                   `json:"cursor,omitempty"` // next page after the loaded list
	HasMore  bool                     `json:"hasMore"`
}

// CacheOlderResult tells what one batch did.
type CacheOlderResult struct {
	Enqueued  int    `json:"enqueued"`
	Skipped   int    `json:"skipped"` // already cached, caching or not cacheable
	Pages     int    `json:"pages"`   // pages fetched past the loaded list
	Exhausted bool   `json:"exhausted"`
	Stopped   string `json:"stopped,omitempty"` // why the batch stopped early (risk control, quota, backoff)
	// Next* let the next batch continue where this one ended.
	NextCursor string `json:"nextCursor,omitempty"`
}

// batchCandidate is one post of the list.
type batchCandidate struct {
	key         string // library item key
	uncacheable bool
	enqueue     func(ctx context.Context) error
}

// batchPager fetches the next page past the loaded list.
type batchPager func(ctx context.Context, cursor string) (items []batchCandidate, next string, more bool, err error)

// CacheOlderUserItems queues up to Limit uncached posts of a user's list
// (DEC-046 ⑥). Requests run as automatic work, so they spend the site's
// hourly quota and stop at backoff; risk control, a rate limit, an exhausted
// quota or backoff ends the batch with what was queued so far.
func (b *Backend) CacheOlderUserItems(ctx context.Context, req CacheOlderRequest) (CacheOlderResult, error) {
	siteID, err := normalizeSiteID(req.Site)
	if err != nil {
		return CacheOlderResult{}, err
	}
	if err := b.requireSiteLogin(siteID); err != nil {
		return CacheOlderResult{}, err
	}
	limit := DefaultCacheOlderLimit
	for _, n := range CacheOlderChoices {
		if req.Limit == n {
			limit = n
		}
	}
	ctx = site.WithAutomaticRequests(ctx, "batch_cache")

	var loaded []batchCandidate
	var pager batchPager
	switch {
	case siteID == site.Bilibili && req.List == "videos":
		mid, perr := strconv.ParseInt(strings.TrimSpace(req.User), 10, 64)
		if perr != nil || mid <= 0 {
			return CacheOlderResult{}, &site.Error{Site: siteID, Kind: site.ErrKindBadRequest, Message: "用户 UID 无效"}
		}
		for _, v := range req.Videos {
			loaded = append(loaded, b.videoCandidate(v.BVID))
		}
		pager = func(ctx context.Context, cursor string) ([]batchCandidate, string, bool, error) {
			page, _ := strconv.Atoi(cursor)
			if page < 1 {
				page = 1
			}
			res, err := b.bili.UserVideos(ctx, mid, page, 30, "pubdate", "")
			if err != nil {
				return nil, "", false, err
			}
			var out []batchCandidate
			for _, v := range res.Items {
				out = append(out, b.videoCandidate(v.BVID))
			}
			return out, strconv.Itoa(page + 1), page*max(res.PageSize, 1) < res.Total && len(res.Items) > 0, nil
		}
	case siteID == site.Bilibili:
		mid, perr := strconv.ParseInt(strings.TrimSpace(req.User), 10, 64)
		if perr != nil || mid <= 0 {
			return CacheOlderResult{}, &site.Error{Site: siteID, Kind: site.ErrKindBadRequest, Message: "用户 UID 无效"}
		}
		for i := range req.Dynamics {
			loaded = append(loaded, b.dynamicCandidate(req.Dynamics[i]))
		}
		pager = func(ctx context.Context, cursor string) ([]batchCandidate, string, bool, error) {
			res, err := b.bili.UserDynamics(ctx, mid, cursor)
			if err != nil {
				return nil, "", false, err
			}
			var out []batchCandidate
			for i := range res.Items {
				out = append(out, b.dynamicCandidate(res.Items[i]))
			}
			return out, res.Offset, res.HasMore && res.Offset != "", nil
		}
	default:
		secUID := strings.TrimSpace(req.User)
		if secUID == "" {
			return CacheOlderResult{}, &site.Error{Site: siteID, Kind: site.ErrKindBadRequest, Message: "抖音用户无效"}
		}
		for _, w := range req.Works {
			loaded = append(loaded, b.workCandidate(w))
		}
		pager = func(ctx context.Context, cursor string) ([]batchCandidate, string, bool, error) {
			res, err := b.DouyinUserWorks(ctx, secUID, cursor)
			if err != nil {
				return nil, "", false, err
			}
			var out []batchCandidate
			for _, w := range res.Works {
				out = append(out, b.workCandidate(w))
			}
			return out, res.NextCursor, res.HasMore && res.NextCursor != "", nil
		}
	}
	res := b.runCacheOlder(ctx, limit, loaded, req.Cursor, req.HasMore, pager)
	b.events.PublishSite(EventTaskUpdated, string(siteID), "enqueued:batch")
	return res, nil
}

func (b *Backend) dynamicCandidate(d bilibili.DynamicItem) batchCandidate {
	item := d
	return batchCandidate{
		key: library.CanonicalItemKey("bilibili", "dynamic", d.ID),
		enqueue: func(ctx context.Context) error {
			_, err := b.EnqueueDynamicCache(ctx, download.DynamicEnqueueRequest{DynamicID: item.ID, Item: &item, Source: "batch"})
			return err
		},
	}
}

func (b *Backend) videoCandidate(bvid string) batchCandidate {
	return batchCandidate{
		key: library.CanonicalItemKey("bilibili", "video", bvid),
		enqueue: func(ctx context.Context) error {
			_, err := b.EnqueueVideoCache(ctx, download.VideoEnqueueRequest{BVID: bvid})
			return err
		},
	}
}

func (b *Backend) workCandidate(w DouyinWorkCardDTO) batchCandidate {
	id := w.ID
	return batchCandidate{
		key:         w.CacheKey,
		uncacheable: w.Uncacheable || w.Kind == "unsupported",
		enqueue: func(ctx context.Context) error {
			_, err := b.enqueueDouyinWorkWithSource(ctx, id, "batch")
			return err
		},
	}
}

// runCacheOlder walks the loaded items and then older pages, queueing only
// posts that are neither cached nor being cached, until limit are queued.
func (b *Backend) runCacheOlder(ctx context.Context, limit int, loaded []batchCandidate, cursor string, more bool, pager batchPager) CacheOlderResult {
	var res CacheOlderResult
	seen := map[string]bool{}
	// take queues what it can of items; it reports false when the batch ends.
	take := func(items []batchCandidate) bool {
		var keys []string
		for _, it := range items {
			if it.key != "" {
				keys = append(keys, it.key)
			}
		}
		states := b.States(keys)
		for _, it := range items {
			if res.Enqueued >= limit {
				return false
			}
			if it.key == "" || seen[it.key] {
				continue
			}
			seen[it.key] = true
			if st, ok := states[it.key]; it.uncacheable || (ok && st.Dot != "" && st.Dot != "none") {
				res.Skipped++
				continue
			}
			if err := it.enqueue(ctx); err != nil {
				if reason := batchStopReason(err); reason != "" {
					res.Stopped = reason
					return false
				}
				res.Skipped++ // this one cannot be cached; go on with the next
				continue
			}
			res.Enqueued++
		}
		return res.Enqueued < limit
	}
	if !take(loaded) {
		res.NextCursor = cursor
		return res
	}
	for more && res.Pages < cacheOlderMaxPages {
		if err := ctx.Err(); err != nil {
			res.Stopped = "已取消"
			break
		}
		items, next, hasMore, err := pager(ctx, cursor)
		if err != nil {
			res.Stopped = batchStopReason(err)
			if res.Stopped == "" {
				res.Stopped = "读取列表失败：" + err.Error()
			}
			break
		}
		res.Pages++
		cursor, more = next, hasMore
		if !take(items) {
			break
		}
	}
	res.NextCursor = cursor
	res.Exhausted = !more && res.Stopped == ""
	return res
}

// batchStopReason names the errors that end a batch at once.
func batchStopReason(err error) string {
	switch site.KindOf(err) {
	case site.ErrKindRiskControl:
		return "遇到站点风控，已停止"
	case site.ErrKindRateLimited:
		return "站点限流，已停止"
	case site.ErrKindQuota:
		return "本小时自动请求额度已用尽，已停止"
	case site.ErrKindBackoff:
		return "站点仍在退避期，已停止"
	case site.ErrKindAuthRequired:
		return "登录已失效，已停止"
	}
	return ""
}
