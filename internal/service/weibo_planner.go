package service

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/weibo"
)

const (
	weiboPostCacheMaxEntries = 128
	weiboPostCacheTTL        = 5 * time.Minute
)

type cachedWeiboPostEntry struct {
	post       weibo.Post
	codec      string
	maxRes     int
	capturedAt time.Time
}

// weiboPostPlanCache keeps recently fetched Weibo posts strictly in memory so
// CacheWeiboPost and full-cache queue runs can reuse list/detail payloads without
// issuing duplicate statuses/show requests or persisting signed URLs to disk.
type weiboPostPlanCache struct {
	mu      sync.Mutex
	entries map[string]cachedWeiboPostEntry
	now     func() time.Time
}

func newWeiboPostPlanCache(nowFn func() time.Time) *weiboPostPlanCache {
	if nowFn == nil {
		nowFn = time.Now
	}
	return &weiboPostPlanCache{
		entries: make(map[string]cachedWeiboPostEntry),
		now:     nowFn,
	}
}

func (c *weiboPostPlanCache) put(post *weibo.Post, codec string, maxRes int) {
	if c == nil || !canReuseWeiboPostPlan(post) {
		return
	}
	mid := strings.TrimSpace(post.Mid)
	if mid == "" {
		return
	}
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= weiboPostCacheMaxEntries {
		c.evictExpiredLocked(now)
		for len(c.entries) >= weiboPostCacheMaxEntries {
			var oldestKey string
			var oldestAt time.Time
			for k, v := range c.entries {
				if oldestKey == "" || v.capturedAt.Before(oldestAt) {
					oldestKey, oldestAt = k, v.capturedAt
				}
			}
			if oldestKey == "" {
				break
			}
			delete(c.entries, oldestKey)
		}
	}
	c.entries[mid] = cachedWeiboPostEntry{
		post:       *post,
		codec:      codec,
		maxRes:     maxRes,
		capturedAt: now,
	}
}

func (c *weiboPostPlanCache) get(mid, codec string, maxRes int) (*weibo.Post, bool) {
	if c == nil {
		return nil, false
	}
	mid = strings.TrimSpace(mid)
	if mid == "" {
		return nil, false
	}
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	ent, ok := c.entries[mid]
	if !ok {
		return nil, false
	}
	if now.Sub(ent.capturedAt) > weiboPostCacheTTL {
		delete(c.entries, mid)
		return nil, false
	}
	if ent.codec != codec || ent.maxRes != maxRes {
		return nil, false
	}
	cp := ent.post
	return &cp, true
}

func (c *weiboPostPlanCache) clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	clear(c.entries)
	c.mu.Unlock()
}

func (c *weiboPostPlanCache) evictExpiredLocked(now time.Time) {
	for k, v := range c.entries {
		if now.Sub(v.capturedAt) > weiboPostCacheTTL {
			delete(c.entries, k)
		}
	}
}

// canReuseWeiboPostPlan reports whether an already-fetched Weibo Post carries
// complete text and playable media URLs suitable for direct download planning
// without issuing an extra statuses/show request.
func canReuseWeiboPostPlan(post *weibo.Post) bool {
	if post == nil || strings.TrimSpace(post.Mid) == "" || post.Unavailable {
		return false
	}
	if strings.TrimSpace(post.Author.UID) == "" {
		return false
	}
	if post.MediaIncomplete || post.OriginIncomplete || post.IsFastRetweet || (post.IsRetweet && post.Retweeted == nil) {
		return false
	}
	if post.Retweeted != nil && post.Retweeted.MediaIncomplete {
		return false
	}
	for _, img := range post.Images {
		if img.Kind == weibo.MediaKindVideo && (img.VideoStream == nil || strings.TrimSpace(img.VideoStream.URL) == "") {
			return false
		}
	}
	if post.Retweeted != nil {
		for _, img := range post.Retweeted.Images {
			if img.Kind == weibo.MediaKindVideo && (img.VideoStream == nil || strings.TrimSpace(img.VideoStream.URL) == "") {
				return false
			}
		}
	}
	if post.IsLongText && !post.LongTextFetched {
		return false
	}
	if post.Retweeted != nil && post.Retweeted.IsLongText && !post.Retweeted.LongTextFetched {
		return false
	}
	if post.Video != nil && (post.Video.Selected == nil || strings.TrimSpace(post.Video.Selected.URL) == "") {
		return false
	}
	if post.Retweeted != nil && post.Retweeted.Video != nil && (post.Retweeted.Video.Selected == nil || strings.TrimSpace(post.Retweeted.Video.Selected.URL) == "") {
		return false
	}
	return true
}

type weiboWorkPlanner struct {
	backend *Backend
}

func (p *weiboWorkPlanner) SiteID() site.ID {
	return site.Weibo
}

func (p *weiboWorkPlanner) RequireAuth(_ string) error {
	return p.backend.requireSiteLogin(site.Weibo)
}

func (p *weiboWorkPlanner) ReportAutomaticError(ctx context.Context, err error) {
	if p.backend != nil && p.backend.weibo != nil {
		p.backend.weibo.ReportAutomaticError(ctx, err)
	}
}

func (p *weiboWorkPlanner) PlanWorkDownload(ctx context.Context, targetID string, opts download.WorkPlanOptions) (download.WorkDownloadPlan, error) {
	if err := p.backend.requireSiteLogin(site.Weibo); err != nil {
		return download.WorkDownloadPlan{}, err
	}
	cleanTarget := strings.TrimSpace(targetID)
	if cleanTarget == "" {
		return download.WorkDownloadPlan{}, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBadRequest,
			Message: "无效的微博目标 ID",
		}
	}

	prefCodec := resolveWeiboCodecFromOrder(opts.CodecOrder, p.backend.preferredWeiboCodec())
	maxRes := parseDouyinQualityCap(opts.VideoQuality)
	postOpt := weibo.PostOptions{
		StreamOptions: weibo.StreamSelectOptions{
			PreferredCodec: prefCodec,
			MaxResolution:  maxRes,
		},
	}

	if strings.HasPrefix(cleanTarget, "1034:") || strings.HasPrefix(cleanTarget, "1034_") {
		oid := cleanTarget
		if strings.HasPrefix(oid, "1034_") {
			oid = "1034:" + strings.TrimPrefix(oid, "1034_")
		}
		vr, err := p.backend.weibo.GetVideo(ctx, oid, postOpt)
		if err != nil {
			return download.WorkDownloadPlan{}, err
		}
		if vr.HostPost != nil {
			return p.backend.buildWeiboWorkPlanFromPost(vr.HostPost)
		}
		return p.backend.buildWeiboWorkPlanFromStandaloneVideo(vr)
	}

	post, err := p.backend.weibo.GetPost(ctx, cleanTarget, postOpt)
	if err != nil {
		return download.WorkDownloadPlan{}, err
	}
	return p.backend.buildWeiboWorkPlanFromPost(post)
}

func (b *Backend) buildWeiboWorkPlanFromPost(post *weibo.Post) (download.WorkDownloadPlan, error) {
	if b == nil || b.weiboAdapter == nil {
		return download.WorkDownloadPlan{}, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindUnavailable,
			Message: "微博适配器尚未初始化",
		}
	}
	b.rememberWeiboPostAvatars(post)
	sitePlan, err := b.weiboAdapter.PlanDownloadFromPost(post)
	if err != nil {
		return download.WorkDownloadPlan{}, err
	}
	plan := download.WorkDownloadPlanFromSite(sitePlan)
	_, video := post.EffectiveMedia()
	if video != nil {
		plan.AvailableGears = collectWeiboVideoGears(video)
		if video.Selected != nil && plan.VideoGear == "" {
			plan.VideoGear = firstNonEmpty(video.Selected.Label, video.Selected.QualityLabel)
		}
	}
	return plan, nil
}

func (b *Backend) buildWeiboWorkPlanFromStandaloneVideo(vr *weibo.VideoPageResult) (download.WorkDownloadPlan, error) {
	if vr == nil || vr.Video == nil || vr.Video.Selected == nil || strings.TrimSpace(vr.Video.Selected.URL) == "" {
		return download.WorkDownloadPlan{}, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindUnavailable,
			Message: "该微博视频未提供可缓存的视频流",
		}
	}
	sel := vr.Video.Selected
	seenURL := make(map[string]bool, len(vr.Video.Streams)+1)
	videoURLs := make([]string, 0, len(vr.Video.Streams)+1)
	if sel.URL != "" {
		seenURL[sel.URL] = true
		videoURLs = append(videoURLs, sel.URL)
	}
	for _, s := range vr.Video.Streams {
		if s.URL != "" && !seenURL[s.URL] {
			seenURL[s.URL] = true
			videoURLs = append(videoURLs, s.URL)
		}
	}
	var coverURLs []string
	if vr.Video.CoverURL != "" {
		coverURLs = []string{vr.Video.CoverURL}
	}
	sitePlan := site.DownloadPlan{
		Site:         site.Weibo,
		ItemKey:      weibo.VideoItemKey(vr.ObjectID),
		TargetID:     vr.ObjectID,
		Kind:         "video",
		CanonicalURL: vr.CanonicalURL,
		AuthorUID:    vr.AuthorUID,
		AuthorName:   vr.AuthorName,
		Title:        vr.Title,
		DurationSec:  vr.Video.DurationSec,
		VideoCodec:   sel.Codec,
		QualityLabel: sel.QualityLabel,
		Width:        sel.Width,
		Height:       sel.Height,
		FPS:          sel.FPS,
		Bitrate:      sel.Bitrate,
		ExpectedSize: sel.Size,
		VideoURLs:    videoURLs,
		CoverURLs:    coverURLs,
		Media:        b.weibo.MediaTransport(),
	}
	plan := download.WorkDownloadPlanFromSite(sitePlan)
	plan.AvailableGears = collectWeiboVideoGears(vr.Video)
	plan.VideoGear = firstNonEmpty(sel.Label, sel.QualityLabel)
	return plan, nil
}

func collectWeiboVideoGears(video *weibo.VideoAsset) []string {
	if video == nil || len(video.Streams) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(video.Streams))
	out := make([]string, 0, len(video.Streams))
	for _, s := range video.Streams {
		label := firstNonEmpty(s.QualityLabel, s.QualityDesc, s.Label)
		if label != "" && !seen[label] {
			seen[label] = true
			out = append(out, label)
		}
	}
	return out
}

func (b *Backend) preferredWeiboCodec() string {
	return b.preferredDouyinCodec()
}

func (b *Backend) currentWeiboMaxResolution() int {
	if b == nil || b.settings == nil {
		return 0
	}
	return parseDouyinQualityCap(b.settings.Get().Download.VideoQuality)
}

func resolveWeiboCodecFromOrder(order []string, fallback string) string {
	return resolveDouyinCodecFromOrder(order, fallback)
}
