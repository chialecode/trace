package service

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/x"
)

const (
	xPostCacheMaxEntries = 128
	xPostCacheTTL        = 5 * time.Minute
)

type cachedXPostEntry struct {
	post       x.Post
	fromDetail bool
	capturedAt time.Time
}

// xPostPlanCache keeps recently fetched X posts strictly in memory so
// CacheXPost and full-cache queue runs can reuse list/detail payloads without
// issuing duplicate TweetDetail requests or persisting CDN URLs to disk.
type xPostPlanCache struct {
	mu      sync.Mutex
	entries map[string]cachedXPostEntry
	now     func() time.Time
}

func newXPostPlanCache(nowFn func() time.Time) *xPostPlanCache {
	if nowFn == nil {
		nowFn = time.Now
	}
	return &xPostPlanCache{
		entries: make(map[string]cachedXPostEntry),
		now:     nowFn,
	}
}

func (c *xPostPlanCache) put(post *x.Post, fromDetail bool) {
	if c == nil || !canReuseXPostPlan(post, fromDetail) {
		return
	}
	id := strings.TrimSpace(post.ID)
	if id == "" {
		return
	}
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.entries[id]; ok && existing.fromDetail && !fromDetail && now.Sub(existing.capturedAt) <= xPostCacheTTL {
		return
	}
	if len(c.entries) >= xPostCacheMaxEntries {
		c.evictExpiredLocked(now)
		for len(c.entries) >= xPostCacheMaxEntries {
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
	c.entries[id] = cachedXPostEntry{
		post:       *post,
		fromDetail: fromDetail,
		capturedAt: now,
	}
}

func (c *xPostPlanCache) get(statusID string) (*x.Post, bool) {
	if c == nil {
		return nil, false
	}
	id := strings.TrimSpace(statusID)
	if id == "" {
		return nil, false
	}
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	ent, ok := c.entries[id]
	if !ok {
		return nil, false
	}
	if now.Sub(ent.capturedAt) > xPostCacheTTL {
		delete(c.entries, id)
		return nil, false
	}
	cp := ent.post
	return &cp, true
}

func (c *xPostPlanCache) clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	clear(c.entries)
	c.mu.Unlock()
}

func (c *xPostPlanCache) evictExpiredLocked(now time.Time) {
	for k, v := range c.entries {
		if now.Sub(v.capturedAt) > xPostCacheTTL {
			delete(c.entries, k)
		}
	}
}

// canReuseXPostPlan reports whether an already-fetched X Post carries complete
// text, thread parts, and playable media URLs suitable for direct download
// planning without issuing an extra TweetDetail request.
func canReuseXPostPlan(post *x.Post, fromDetail bool) bool {
	if post == nil || strings.TrimSpace(post.ID) == "" || post.Unavailable {
		return false
	}
	if strings.TrimSpace(post.Author.RestID) == "" {
		return false
	}
	if post.IsPreviewDisplay {
		return false
	}
	// Self-reply threads seen on UserTweets need TweetDetail to stitch full parts.
	if !fromDetail && strings.TrimSpace(post.SelfThreadID) != "" && len(post.Thread) == 0 && len(post.ThreadAncestors) == 0 {
		return false
	}
	if post.IsRetweet && post.Retweeted == nil && !post.OriginUnavailable {
		return false
	}
	if post.IsQuote && post.Quoted == nil && !post.OriginUnavailable {
		return false
	}
	if !xMediaListComplete(post.Media) {
		return false
	}
	if post.Retweeted != nil && !post.Retweeted.Unavailable && !xMediaListComplete(post.Retweeted.Media) {
		return false
	}
	if post.Quoted != nil && !post.Quoted.Unavailable && !xMediaListComplete(post.Quoted.Media) {
		return false
	}
	return true
}

func xMediaListComplete(items []x.MediaItem) bool {
	for _, m := range items {
		switch m.Kind {
		case x.MediaKindPhoto:
			if strings.TrimSpace(m.ImageURL) == "" && strings.TrimSpace(m.PublicPreviewURL) == "" {
				return false
			}
		case x.MediaKindGIF:
			if strings.TrimSpace(m.VideoURL) == "" {
				return false
			}
		case x.MediaKindVideo:
			if !m.HLSOnly && strings.TrimSpace(m.VideoURL) == "" {
				return false
			}
		}
	}
	return true
}

type xWorkPlanner struct {
	backend *Backend
}

func (p *xWorkPlanner) SiteID() site.ID {
	return site.X
}

func (p *xWorkPlanner) RequireAuth(_ string) error {
	return p.backend.requireSiteLogin(site.X)
}

func (p *xWorkPlanner) ReportAutomaticError(ctx context.Context, err error) {
	if p.backend != nil && p.backend.x != nil {
		p.backend.x.ReportAutomaticError(ctx, err)
	}
}

// PlanWorkDownload always fetches a fresh GraphQL TweetDetail payload so CDN
// 403/404/410 refreshes in internal/download/x_plan.go obtain fresh URLs.
func (p *xWorkPlanner) PlanWorkDownload(ctx context.Context, targetID string, _ download.WorkPlanOptions) (download.WorkDownloadPlan, error) {
	if err := p.backend.requireSiteLogin(site.X); err != nil {
		return download.WorkDownloadPlan{}, err
	}
	cleanTarget := strings.TrimSpace(targetID)
	if cleanTarget == "" {
		return download.WorkDownloadPlan{}, &site.Error{
			Site:    site.X,
			Kind:    site.ErrKindBadRequest,
			Message: "无效的 X 帖子编号",
		}
	}
	post, err := p.backend.x.GetPost(ctx, cleanTarget)
	if err != nil {
		return download.WorkDownloadPlan{}, err
	}
	return p.backend.buildXWorkPlanFromPost(post)
}

func (b *Backend) buildXWorkPlanFromPost(post *x.Post) (download.WorkDownloadPlan, error) {
	if b == nil || b.xAdapter == nil {
		return download.WorkDownloadPlan{}, &site.Error{
			Site:    site.X,
			Kind:    site.ErrKindUnavailable,
			Message: "X 适配器尚未初始化",
		}
	}
	b.rememberXPostAvatars(post)
	sitePlan, err := b.xAdapter.PlanDownloadFromPost(post)
	if err != nil {
		return download.WorkDownloadPlan{}, err
	}
	plan := download.WorkDownloadPlanFromSite(sitePlan)
	plan.AuthorUniqueID = strings.TrimSpace(post.Author.ScreenName)

	var originPost *x.Post
	if post.IsRetweet && post.Retweeted != nil {
		originPost = post.Retweeted
	} else if post.IsQuote && post.Quoted != nil {
		originPost = post.Quoted
	}
	if originPost != nil {
		plan.OriginAuthorUniqueID = strings.TrimSpace(originPost.Author.ScreenName)
	}

	mediaItems, singleVideo, _ := post.EffectiveMediaWithOrigin()
	hasThreadParts := len(post.ThreadAncestors) > 0 || len(post.Thread) > 0
	hasThreadExtraMedia := false
	for i := range post.ThreadAncestors {
		if len(post.ThreadAncestors[i].Media) > 0 {
			hasThreadExtraMedia = true
			break
		}
	}
	if !hasThreadExtraMedia {
		for i := range post.Thread {
			if len(post.Thread[i].Media) > 0 {
				hasThreadExtraMedia = true
				break
			}
		}
	}

	switch {
	case singleVideo != nil && !hasThreadExtraMedia:
		plan.AvailableGears = collectXVideoGears(singleVideo)
		if plan.VideoGear == "" {
			plan.VideoGear = firstNonEmpty(plan.QualityName, formatXMediaQualityLabel(singleVideo.Width, singleVideo.Height, singleVideo.VideoBitrate))
		}
	case singleVideo != nil && hasThreadExtraMedia:
		// Convert focal single video into a page so thread media can download in order.
		plan.Kind = "image_note"
		plan.Pages = []download.PlannedNotePage{buildXPlannedPageFromMedia(*singleVideo, 1)}
		plan.VideoURLs = nil
	case len(mediaItems) == 1 && mediaItems[0].Kind == x.MediaKindVideo && !mediaItems[0].GIF && mediaItems[0].HLSOnly && !hasThreadExtraMedia:
		// Single HLS-only video routes through executeXSingleVideoTask to record missing video.mp4 cleanly with 0 HLS fetches.
		plan.HLSOnly = true
		plan.Pages = nil
		plan.VideoURLs = nil
		if plan.DowngradeHint == "" {
			plan.DowngradeHint = firstNonEmpty(mediaItems[0].HLSOnlyNotice, x.HLSOnlyNoticeText)
		}
	default:
		for i := 0; i < len(plan.Pages) && i < len(mediaItems); i++ {
			m := mediaItems[i]
			if m.GIF || m.Kind == x.MediaKindGIF {
				plan.Pages[i].Kind = "gif"
			}
			if m.HLSOnly {
				plan.Pages[i].HLSOnly = true
				plan.Pages[i].VideoURLs = nil
			}
		}
	}

	if hasThreadParts {
		focalPages := append([]download.PlannedNotePage(nil), plan.Pages...)
		var combinedPages []download.PlannedNotePage
		threadParts := make([]download.PlannedThreadPart, 0, len(post.ThreadAncestors)+1+len(post.Thread))

		appendThreadPost := func(tp *x.Post, pages []download.PlannedNotePage) {
			if tp == nil {
				return
			}
			var idxs []int
			for _, pg := range pages {
				pg.Index = len(combinedPages) + 1
				combinedPages = append(combinedPages, pg)
				idxs = append(idxs, pg.Index)
				if len(plan.CoverURLs) == 0 && len(pg.ImageURLs) > 0 {
					plan.CoverURLs = []string{pg.ImageURLs[0]}
				}
			}
			threadParts = append(threadParts, download.PlannedThreadPart{
				ID:           strings.TrimSpace(tp.ID),
				PubTs:        tp.CreatedAt,
				Text:         strings.TrimSpace(tp.Text),
				MediaIndexes: idxs,
			})
		}

		for i := range post.ThreadAncestors {
			a := &post.ThreadAncestors[i]
			aPages := make([]download.PlannedNotePage, 0, len(a.Media))
			for j, m := range a.Media {
				aPages = append(aPages, buildXPlannedPageFromMedia(m, j+1))
			}
			appendThreadPost(a, aPages)
		}

		appendThreadPost(post, focalPages)

		for i := range post.Thread {
			r := &post.Thread[i]
			rPages := make([]download.PlannedNotePage, 0, len(r.Media))
			for j, m := range r.Media {
				rPages = append(rPages, buildXPlannedPageFromMedia(m, j+1))
			}
			appendThreadPost(r, rPages)
		}

		plan.Thread = threadParts
		if len(combinedPages) > 0 {
			plan.Pages = combinedPages
		}
	}

	return plan, nil
}

func buildXPlannedPageFromMedia(m x.MediaItem, index int) download.PlannedNotePage {
	kind := "image"
	codec := ""
	switch {
	case m.GIF || m.Kind == x.MediaKindGIF:
		kind = "gif"
		codec = "avc"
	case m.Kind == x.MediaKindVideo:
		kind = "video"
		codec = "avc"
	}
	var imgURLs []string
	seenImg := make(map[string]bool, 3)
	addImg := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw != "" && !seenImg[raw] {
			seenImg[raw] = true
			imgURLs = append(imgURLs, raw)
		}
	}
	addImg(m.ImageURL)
	for _, fb := range m.FallbackImageURLs {
		addImg(fb)
	}
	addImg(m.PublicPreviewURL)

	var vidURLs []string
	if !m.HLSOnly && strings.TrimSpace(m.VideoURL) != "" {
		vidURLs = append(vidURLs, strings.TrimSpace(m.VideoURL))
		for _, fb := range m.VideoFallbackURLs {
			if fb = strings.TrimSpace(fb); fb != "" {
				vidURLs = append(vidURLs, fb)
			}
		}
	}
	return download.PlannedNotePage{
		Index:       index,
		Kind:        kind,
		FromOrigin:  m.FromOrigin,
		HLSOnly:     m.HLSOnly,
		ImageURLs:   imgURLs,
		VideoURLs:   vidURLs,
		Width:       m.Width,
		Height:      m.Height,
		DurationSec: m.DurationSec,
		Codec:       codec,
		QualityName: formatXMediaQualityLabel(m.Width, m.Height, m.VideoBitrate),
		Bitrate:     m.VideoBitrate,
	}
}

func collectXVideoGears(video *x.MediaItem) []string {
	if video == nil || len(video.Variants) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(video.Variants))
	out := make([]string, 0, len(video.Variants))
	for _, v := range video.Variants {
		if !strings.EqualFold(strings.TrimSpace(v.ContentType), "video/mp4") {
			continue
		}
		label := formatXMediaQualityLabel(v.Width, v.Height, v.Bitrate)
		if label != "" && !seen[label] {
			seen[label] = true
			out = append(out, label)
		}
	}
	return out
}
