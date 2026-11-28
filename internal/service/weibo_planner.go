package service

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/weibo"
)

const (
	weiboPostCacheMaxEntries = 128
	weiboPostCacheTTL        = 5 * time.Minute
)

type cachedWeiboPostEntry struct {
	post            weibo.Post
	codec           string
	maxRes          int
	removeWatermark bool
	capturedAt      time.Time
}

type cachedWeiboEditHistoryEntry struct {
	revisions  []weibo.EditRevision
	capturedAt time.Time
}

// weiboPostPlanCache keeps recently fetched Weibo posts and edit histories strictly
// in memory so CacheWeiboPost and full-cache queue runs can reuse list/detail
// payloads without issuing duplicate requests or persisting signed URLs to disk.
type weiboPostPlanCache struct {
	mu          sync.Mutex
	entries     map[string]cachedWeiboPostEntry
	editHistory map[string]cachedWeiboEditHistoryEntry
	now         func() time.Time
}

func newWeiboPostPlanCache(nowFn func() time.Time) *weiboPostPlanCache {
	if nowFn == nil {
		nowFn = time.Now
	}
	return &weiboPostPlanCache{
		entries:     make(map[string]cachedWeiboPostEntry),
		editHistory: make(map[string]cachedWeiboEditHistoryEntry),
		now:         nowFn,
	}
}

func (c *weiboPostPlanCache) put(post *weibo.Post, codec string, maxRes int, removeWatermark bool) {
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
		post:            *post,
		codec:           codec,
		maxRes:          maxRes,
		removeWatermark: removeWatermark,
		capturedAt:      now,
	}
}

func (c *weiboPostPlanCache) get(mid, codec string, maxRes int, removeWatermark bool) (*weibo.Post, bool) {
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
	if ent.codec != codec || ent.maxRes != maxRes || ent.removeWatermark != removeWatermark {
		return nil, false
	}
	cp := ent.post
	return &cp, true
}

func (c *weiboPostPlanCache) putEditHistory(mid string, revs []weibo.EditRevision) {
	if c == nil || len(revs) == 0 {
		return
	}
	mid = strings.TrimSpace(mid)
	if mid == "" {
		return
	}
	now := c.now()
	copied := cloneWeiboEditRevisions(revs)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.editHistory == nil {
		c.editHistory = make(map[string]cachedWeiboEditHistoryEntry)
	}
	if len(c.editHistory) >= weiboPostCacheMaxEntries {
		c.evictExpiredLocked(now)
		for len(c.editHistory) >= weiboPostCacheMaxEntries {
			var oldestKey string
			var oldestAt time.Time
			for k, v := range c.editHistory {
				if oldestKey == "" || v.capturedAt.Before(oldestAt) {
					oldestKey, oldestAt = k, v.capturedAt
				}
			}
			if oldestKey == "" {
				break
			}
			delete(c.editHistory, oldestKey)
		}
	}
	c.editHistory[mid] = cachedWeiboEditHistoryEntry{
		revisions:  copied,
		capturedAt: now,
	}
}

func (c *weiboPostPlanCache) getEditHistory(mid string) ([]weibo.EditRevision, bool) {
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
	ent, ok := c.editHistory[mid]
	if !ok {
		return nil, false
	}
	if now.Sub(ent.capturedAt) > weiboPostCacheTTL {
		delete(c.editHistory, mid)
		return nil, false
	}
	return cloneWeiboEditRevisions(ent.revisions), true
}

func cloneWeiboEditRevisions(src []weibo.EditRevision) []weibo.EditRevision {
	if len(src) == 0 {
		return nil
	}
	out := make([]weibo.EditRevision, len(src))
	for i, rev := range src {
		rev.Links = append([]site.TextLink(nil), rev.Links...)
		if len(rev.Images) > 0 {
			imgs := make([]weibo.ImageAsset, len(rev.Images))
			for j, img := range rev.Images {
				img.FallbackURLs = append([]string(nil), img.FallbackURLs...)
				imgs[j] = img
			}
			rev.Images = imgs
		}
		out[i] = rev
	}
	return out
}

func (c *weiboPostPlanCache) clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	clear(c.entries)
	clear(c.editHistory)
	c.mu.Unlock()
}

func (c *weiboPostPlanCache) evictExpiredLocked(now time.Time) {
	for k, v := range c.entries {
		if now.Sub(v.capturedAt) > weiboPostCacheTTL {
			delete(c.entries, k)
		}
	}
	for k, v := range c.editHistory {
		if now.Sub(v.capturedAt) > weiboPostCacheTTL {
			delete(c.editHistory, k)
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
		var plan download.WorkDownloadPlan
		if vr.HostPost != nil {
			plan, err = p.backend.buildWeiboWorkPlanFromPost(vr.HostPost)
			if err == nil {
				if _, _, stopErr := p.backend.attachWeiboEditHistoryBestEffort(ctx, vr.HostPost, &plan); stopErr != nil && strictWeiboEditHistoryStop(ctx, opts) {
					return download.WorkDownloadPlan{}, stopErr
				}
			}
		} else {
			plan, err = p.backend.buildWeiboWorkPlanFromStandaloneVideo(vr)
		}
		if err != nil {
			return download.WorkDownloadPlan{}, err
		}
		plan.ConflictResolution = download.ConflictResolutionFromContext(ctx)
		return plan, nil
	}

	post, err := p.backend.weibo.GetPost(ctx, cleanTarget, postOpt)
	if err != nil {
		return download.WorkDownloadPlan{}, err
	}
	plan, err := p.backend.buildWeiboWorkPlanFromPost(post)
	if err != nil {
		return download.WorkDownloadPlan{}, err
	}
	if _, _, stopErr := p.backend.attachWeiboEditHistoryBestEffort(ctx, post, &plan); stopErr != nil && strictWeiboEditHistoryStop(ctx, opts) {
		return download.WorkDownloadPlan{}, stopErr
	}
	plan.ConflictResolution = download.ConflictResolutionFromContext(ctx)
	return plan, nil
}

type strictWeiboEditHistoryStopKey struct{}

func withStrictWeiboEditHistoryStop(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, strictWeiboEditHistoryStopKey{}, true)
}

func isStrictWeiboEditHistoryStopContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(strictWeiboEditHistoryStopKey{}).(bool)
	return v
}

func strictWeiboEditHistoryStop(ctx context.Context, opts download.WorkPlanOptions) bool {
	if site.AutomaticRequestSource(ctx) != "" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(opts.Source)) {
	case "queue", "auto":
		return true
	}
	return isStrictWeiboEditHistoryStopContext(ctx)
}

type weiboEditHistoryPlanTrackerKey struct{}

type weiboEditHistoryPlanTracker struct {
	stopErr error
	anyErr  error
}

func withWeiboEditHistoryPlanTracker(ctx context.Context) (context.Context, *weiboEditHistoryPlanTracker) {
	if ctx == nil {
		ctx = context.Background()
	}
	tr := &weiboEditHistoryPlanTracker{}
	return context.WithValue(ctx, weiboEditHistoryPlanTrackerKey{}, tr), tr
}

func recordWeiboEditHistoryTracker(ctx context.Context, anyErr, stopErr error) {
	if ctx == nil {
		return
	}
	tr, _ := ctx.Value(weiboEditHistoryPlanTrackerKey{}).(*weiboEditHistoryPlanTracker)
	if tr == nil {
		return
	}
	if anyErr != nil {
		tr.anyErr = anyErr
	}
	if stopErr != nil {
		tr.stopErr = stopErr
	}
}

func isWeiboEditHistorySiteStopError(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return true
	}
	if site.IsRoundStop(err) {
		return true
	}
	switch site.KindOf(err) {
	case site.ErrKindAuthRequired, site.ErrKindQuota, site.ErrKindBackoff, site.ErrKindRiskControl, site.ErrKindRateLimited:
		return true
	default:
		return false
	}
}

// attachWeiboEditHistoryBestEffort populates plan.EditHistory for edited Weibo
// posts (EditCount > 0), reusing the short-lived in-memory edit-history cache
// when available. When EditCount == 0, it issues zero network requests.
// It returns calledAPI=true whenever it contacted remote GetEditHistory (i.e.
// missed the in-memory cache), editErr!=nil on any remote error, and stopErr!=nil
// only for site-level stop errors (risk control, rate limit, auth required,
// quota, backoff, or context cancel) so background queues and DEC-089 probes can
// halt immediately while ordinary 404/unavailable/parse errors and manual
// single-post caching degrade safely without misclearing backoff (DEC-094).
func (b *Backend) attachWeiboEditHistoryBestEffort(ctx context.Context, post *weibo.Post, plan *download.WorkDownloadPlan) (calledAPI bool, editErr error, stopErr error) {
	if b == nil || b.weibo == nil || post == nil || plan == nil || post.EditCount <= 0 {
		return false, nil, nil
	}
	mid := strings.TrimSpace(post.Mid)
	if mid == "" {
		return false, nil, nil
	}
	var revs []weibo.EditRevision
	if b.weiboPostCache != nil {
		if cached, ok := b.weiboPostCache.getEditHistory(mid); ok {
			revs = cached
		}
	}
	if len(revs) == 0 {
		calledAPI = true
		fetched, err := b.weibo.GetEditHistory(ctx, mid)
		if err != nil {
			b.logError("WeiboEditHistory", err)
			if isWeiboEditHistorySiteStopError(ctx, err) {
				recordWeiboEditHistoryTracker(ctx, err, err)
				return true, err, err
			}
			recordWeiboEditHistoryTracker(ctx, err, nil)
			return true, err, nil
		}
		revs = fetched
		if b.weiboPostCache != nil && len(revs) > 0 {
			b.weiboPostCache.putEditHistory(mid, revs)
		}
	}
	if len(revs) == 0 {
		return calledAPI, nil, nil
	}
	planned := make([]download.PlannedEditRevision, 0, len(revs))
	for _, rev := range revs {
		var imgs []download.PlannedEditImage
		for _, img := range rev.Images {
			var urls []string
			seen := make(map[string]bool)
			addU := func(raw string) {
				if u := weibo.SafePublicImageURL(raw); u != "" && !seen[u] {
					seen[u] = true
					urls = append(urls, u)
				}
			}
			addU(img.URL)
			addU(img.PublicPreviewURL)
			for _, fb := range img.FallbackURLs {
				addU(fb)
			}
			if len(urls) == 0 {
				continue
			}
			imgs = append(imgs, download.PlannedEditImage{
				PicID:     strings.TrimSpace(img.PicID),
				Kind:      strings.TrimSpace(img.Kind),
				Width:     img.Width,
				Height:    img.Height,
				ImageURLs: urls,
			})
		}
		planned = append(planned, download.PlannedEditRevision{
			Version: rev.Version,
			EditTs:  rev.EditTs,
			Text:    rev.Text,
			Links:   append([]site.TextLink(nil), rev.Links...),
			Images:  imgs,
		})
	}
	plan.EditHistory = planned
	return calledAPI, nil, nil
}

func weiboImageMediaID(img weibo.ImageAsset) string {
	picTok := library.SanitizeMediaToken(img.PicID)
	objTok := library.SanitizeMediaToken(img.ObjectID)
	if img.Kind == weibo.MediaKindVideo {
		return firstNonEmpty(objTok, picTok)
	}
	return firstNonEmpty(picTok, objTok)
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
	plan.EditCount = max(0, post.EditCount)
	images, video := post.EffectiveMedia()
	if video != nil {
		plan.AvailableGears = collectWeiboVideoGears(video)
		if video.Selected != nil && plan.VideoGear == "" {
			plan.VideoGear = firstNonEmpty(video.Selected.Label, video.Selected.QualityLabel)
		}
		if len(plan.Pages) == 0 {
			if oid := library.SanitizeMediaToken(video.ObjectID); oid != "" {
				plan.MediaID = oid
				plan.AssetIDs = []string{oid}
			}
		}
	}
	if len(plan.Pages) > 0 {
		var assetIDs []string
		for i := 0; i < len(plan.Pages) && i < len(images); i++ {
			mid := weiboImageMediaID(images[i])
			plan.Pages[i].MediaID = mid
			if mid != "" {
				assetIDs = append(assetIDs, mid)
			}
			if images[i].VideoStream != nil {
				if plan.Pages[i].Gear == "" {
					plan.Pages[i].Gear = firstNonEmpty(images[i].VideoStream.Label, images[i].VideoStream.QualityLabel)
				}
				if plan.Pages[i].Bitrate == 0 {
					plan.Pages[i].Bitrate = images[i].VideoStream.Bitrate
				}
				if plan.Pages[i].FPS == 0 {
					plan.Pages[i].FPS = images[i].VideoStream.FPS
				}
			}
		}
		if len(assetIDs) > 0 {
			plan.AssetIDs = assetIDs
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
	if oid := library.SanitizeMediaToken(firstNonEmpty(vr.Video.ObjectID, vr.ObjectID)); oid != "" {
		plan.MediaID = oid
		plan.AssetIDs = []string{oid}
	}
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

func (b *Backend) weiboRemoveWatermarkEnabled() bool {
	if b == nil || b.settings == nil {
		return true
	}
	return b.settings.Get().WeiboRemoveWatermark
}

func (b *Backend) rewriteWeiboImageURL(_ string, rawURL string) (string, bool) {
	if !b.weiboRemoveWatermarkEnabled() {
		return "", false
	}
	return weibo.RewriteWatermarkFreeImageURL(rawURL)
}

func resolveWeiboCodecFromOrder(order []string, fallback string) string {
	return resolveDouyinCodecFromOrder(order, fallback)
}
