package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/douyin"
)

const (
	douyinWorkCacheMaxEntries = 128
	douyinWorkCacheTTL        = 5 * time.Minute
)

type cachedDouyinWorkEntry struct {
	detail     douyin.WorkDetail
	codec      string
	capturedAt time.Time
}

// douyinWorkPlanCache keeps recently fetched Douyin work details strictly in memory
// so CacheDouyinWork and full-cache queue runs can reuse list/detail payloads without
// issuing duplicate aweme/detail requests or persisting CDN URLs to disk.
type douyinWorkPlanCache struct {
	mu      sync.Mutex
	entries map[string]cachedDouyinWorkEntry
	now     func() time.Time
}

func newDouyinWorkPlanCache(nowFn func() time.Time) *douyinWorkPlanCache {
	if nowFn == nil {
		nowFn = time.Now
	}
	return &douyinWorkPlanCache{
		entries: make(map[string]cachedDouyinWorkEntry),
		now:     nowFn,
	}
}

func cloneDouyinWorkDetail(d *douyin.WorkDetail) douyin.WorkDetail {
	if d == nil {
		return douyin.WorkDetail{}
	}
	cp := *d
	cp.CoverURLs = append([]string(nil), d.CoverURLs...)
	cp.OriginalURLs = append([]string(nil), d.OriginalURLs...)
	cp.Hashtags = append([]string(nil), d.Hashtags...)
	if d.Video != nil {
		v := *d.Video
		v.URLs = append([]string(nil), d.Video.URLs...)
		cp.Video = &v
	}
	if len(d.AvailableStreams) > 0 {
		cp.AvailableStreams = make([]douyin.SelectedVODStream, len(d.AvailableStreams))
		for i, s := range d.AvailableStreams {
			s.URLs = append([]string(nil), s.URLs...)
			cp.AvailableStreams[i] = s
		}
	}
	if len(d.Pages) > 0 {
		cp.Pages = make([]douyin.NotePage, len(d.Pages))
		for i, pg := range d.Pages {
			pg.ImageURLs = append([]string(nil), pg.ImageURLs...)
			if pg.VideoStream != nil {
				vs := *pg.VideoStream
				vs.URLs = append([]string(nil), pg.VideoStream.URLs...)
				pg.VideoStream = &vs
			}
			cp.Pages[i] = pg
		}
	}
	if d.Music != nil {
		m := *d.Music
		m.PlayURLs = append([]string(nil), d.Music.PlayURLs...)
		cp.Music = &m
	}
	return cp
}

func canReuseDouyinWorkPlan(detail *douyin.WorkDetail) bool {
	if detail == nil || strings.TrimSpace(detail.ID) == "" || detail.Uncacheable || detail.Kind == douyin.WorkKindUnsupported {
		return false
	}
	if strings.TrimSpace(detail.Author.SecUID) == "" {
		return false
	}
	if detail.Kind == douyin.WorkKindNote || len(detail.Pages) > 0 {
		if len(detail.Pages) == 0 {
			return false
		}
		for _, pg := range detail.Pages {
			switch pg.Kind {
			case douyin.NotePageLivePhoto:
				if len(pg.ImageURLs) == 0 || pg.VideoStream == nil || len(pg.VideoStream.URLs) == 0 {
					return false
				}
			case douyin.NotePageVideo:
				if pg.VideoStream == nil || len(pg.VideoStream.URLs) == 0 {
					return false
				}
			default:
				if len(pg.ImageURLs) == 0 {
					return false
				}
			}
		}
		return true
	}
	return detail.Video != nil && len(detail.Video.URLs) > 0
}

func (c *douyinWorkPlanCache) put(detail *douyin.WorkDetail, codec string) {
	if c == nil || !canReuseDouyinWorkPlan(detail) {
		return
	}
	id := strings.TrimSpace(detail.ID)
	if id == "" {
		return
	}
	now := c.now()
	cp := cloneDouyinWorkDetail(detail)
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= douyinWorkCacheMaxEntries {
		c.evictExpiredLocked(now)
		for len(c.entries) >= douyinWorkCacheMaxEntries {
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
	c.entries[id] = cachedDouyinWorkEntry{
		detail:     cp,
		codec:      strings.ToLower(strings.TrimSpace(codec)),
		capturedAt: now,
	}
}

func (c *douyinWorkPlanCache) get(awemeID, codec string) (*douyin.WorkDetail, bool) {
	if c == nil {
		return nil, false
	}
	id := strings.TrimSpace(awemeID)
	if id == "" {
		return nil, false
	}
	now := c.now()
	wantCodec := strings.ToLower(strings.TrimSpace(codec))
	c.mu.Lock()
	defer c.mu.Unlock()
	ent, ok := c.entries[id]
	if !ok {
		return nil, false
	}
	if now.Sub(ent.capturedAt) > douyinWorkCacheTTL {
		delete(c.entries, id)
		return nil, false
	}
	if wantCodec != "" && ent.codec != "" && ent.codec != wantCodec {
		return nil, false
	}
	cp := cloneDouyinWorkDetail(&ent.detail)
	return &cp, true
}

func (c *douyinWorkPlanCache) clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	clear(c.entries)
	c.mu.Unlock()
}

func (c *douyinWorkPlanCache) evictExpiredLocked(now time.Time) {
	for k, v := range c.entries {
		if now.Sub(v.capturedAt) > douyinWorkCacheTTL {
			delete(c.entries, k)
		}
	}
}

type douyinWorkPlanner struct {
	backend *Backend
}

func (p *douyinWorkPlanner) SiteID() site.ID {
	return site.Douyin
}

func (p *douyinWorkPlanner) RequireAuth(_ string) error {
	return p.backend.requireSiteLogin(site.Douyin)
}

func (p *douyinWorkPlanner) ReportAutomaticError(ctx context.Context, err error) {
	if p.backend != nil && p.backend.douyin != nil {
		p.backend.douyin.ReportAutomaticError(ctx, err)
	}
}

func (p *douyinWorkPlanner) ResolvePlayRedirect(ctx context.Context, playURL string) (string, error) {
	if err := p.backend.requireSiteLogin(site.Douyin); err != nil {
		return "", err
	}
	return p.backend.douyin.ResolvePlayRedirect(ctx, playURL)
}

func (p *douyinWorkPlanner) PlanWorkDownload(ctx context.Context, targetID string, opts download.WorkPlanOptions) (download.WorkDownloadPlan, error) {
	if err := p.backend.requireSiteLogin(site.Douyin); err != nil {
		return download.WorkDownloadPlan{}, err
	}
	prefCodec := resolveDouyinCodecFromOrder(opts.CodecOrder, p.backend.preferredDouyinCodec())
	detail, err := p.backend.douyin.GetWorkDetail(ctx, strings.TrimSpace(targetID), douyin.StreamSelectOptions{
		PreferredCodec: prefCodec,
	})
	if err != nil {
		return download.WorkDownloadPlan{}, err
	}
	return p.backend.buildDouyinWorkPlanFromDetail(ctx, detail, opts)
}

func (b *Backend) currentWorkPlanOptions() download.WorkPlanOptions {
	if b == nil || b.settings == nil {
		return download.WorkPlanOptions{}
	}
	dl := b.settings.Get().Download
	return download.WorkPlanOptions{
		VideoQuality: dl.VideoQuality,
		CodecOrder:   append([]string(nil), dl.CodecOrder...),
	}
}

func douyinDetailMediaID(detail *douyin.WorkDetail) string {
	if detail == nil {
		return ""
	}
	if tok := library.SanitizeMediaToken(detail.MediaID); tok != "" {
		return tok
	}
	if detail.Video != nil {
		if tok := library.SanitizeMediaToken(detail.Video.URI); tok != "" {
			return tok
		}
	}
	return ""
}

func douyinPageMediaID(pg douyin.NotePage) string {
	imgTok := library.SanitizeMediaToken(pg.URI)
	var vidTok string
	if pg.VideoStream != nil {
		vidTok = library.SanitizeMediaToken(pg.VideoStream.URI)
	}
	if imgTok != "" && vidTok != "" && imgTok != vidTok {
		if comb := library.SanitizeMediaToken(imgTok + ":" + vidTok); comb != "" {
			return comb
		}
	}
	return firstNonEmpty(imgTok, vidTok)
}

func (b *Backend) buildDouyinWorkPlanFromDetail(ctx context.Context, detail *douyin.WorkDetail, opts download.WorkPlanOptions) (download.WorkDownloadPlan, error) {
	if detail == nil || detail.Uncacheable || detail.Kind == douyin.WorkKindUnsupported {
		msg := "该抖音作品当前不可缓存"
		if detail != nil && strings.TrimSpace(detail.UncacheableReason) != "" {
			msg = detail.UncacheableReason
		}
		return download.WorkDownloadPlan{}, &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindUnavailable,
			Message: msg,
		}
	}

	prefCodec := resolveDouyinCodecFromOrder(opts.CodecOrder, b.preferredDouyinCodec())
	kind := "video"
	if detail.Kind == douyin.WorkKindNote || len(detail.Pages) > 0 {
		kind = "dynamic"
	}

	var gears []string
	seenGear := make(map[string]bool)
	for _, s := range detail.AvailableStreams {
		label := firstNonEmpty(s.QualityLabel, s.GearName)
		if label != "" && !seenGear[label] {
			seenGear[label] = true
			gears = append(gears, label)
		}
	}

	if detail.Author.SecUID != "" && detail.Author.AvatarURL != "" {
		b.rememberAccountAvatar(string(site.Douyin), detail.Author.SecUID, detail.Author.AvatarURL)
	}

	var transport site.MediaTransportSpec
	if b != nil && b.douyin != nil {
		transport = b.douyin.VODTransport()
	}

	plan := download.WorkDownloadPlan{
		Site:               site.Douyin,
		Kind:               kind,
		TargetID:           detail.ID,
		Title:              detail.Title,
		Desc:               detail.Desc,
		CanonicalURL:       detail.CanonicalURL,
		PubTs:              detail.CreateTime,
		DurationSec:        detail.DurationSec,
		AuthorUID:          detail.Author.SecUID,
		AuthorUniqueID:     firstNonEmpty(detail.Author.UniqueID, detail.Author.ShortID),
		AuthorName:         detail.Author.Nickname,
		AuthorFace:         detail.Author.AvatarURL,
		CoverURLs:          append([]string(nil), detail.CoverURLs...),
		AvailableGears:     gears,
		ConflictResolution: download.ConflictResolutionFromContext(ctx),
		Transport:          transport,
	}

	if kind == "video" {
		selected, hint := selectDouyinVODStream(detail, opts.VideoQuality, prefCodec)
		if selected == nil || len(selected.URLs) == 0 {
			return download.WorkDownloadPlan{}, &site.Error{
				Site:    site.Douyin,
				Kind:    site.ErrKindUnavailable,
				Message: "未获取到可用的无水印视频流地址",
			}
		}
		plan.VideoURLs = append([]string(nil), selected.URLs...)
		plan.NeedSetToken = selected.NeedSetToken
		plan.VideoGear = selected.GearName
		plan.QualityName = selected.QualityLabel
		plan.Codec = selected.Codec
		plan.Width = selected.Width
		plan.Height = selected.Height
		plan.FPS = selected.FPS
		plan.Bitrate = selected.BitRate
		plan.DowngradeHint = hint
		mediaID := firstNonEmpty(library.SanitizeMediaToken(selected.URI), douyinDetailMediaID(detail))
		plan.MediaID = mediaID
		if mediaID != "" {
			plan.AssetIDs = []string{mediaID}
		}
		// The untranscoded source (原画) has no resolution cap of its own, so it is
		// only tried when the quality setting asks for the highest available.
		if parseDouyinQualityCap(opts.VideoQuality) <= 0 {
			plan.OriginalURLs = append([]string(nil), detail.OriginalURLs...)
		}
		if plan.DurationSec <= 0 && selected.DurationSec > 0 {
			plan.DurationSec = selected.DurationSec
		}
	}

	if len(detail.Pages) > 0 {
		pages := make([]download.PlannedNotePage, 0, len(detail.Pages))
		var assetIDs []string
		for _, pg := range detail.Pages {
			mediaID := douyinPageMediaID(pg)
			item := download.PlannedNotePage{
				Index:         pg.Index,
				Kind:          pg.Kind,
				MediaID:       mediaID,
				UnknownReason: pg.UnknownReason,
				Width:         pg.Width,
				Height:        pg.Height,
				ImageURLs:     append([]string(nil), pg.ImageURLs...),
			}
			if mediaID != "" {
				assetIDs = append(assetIDs, mediaID)
			}
			if pg.UnknownKind {
				item.UnknownKind = firstNonEmpty(pg.Kind, "unknown")
			}
			if pg.VideoStream != nil {
				item.VideoURLs = append([]string(nil), pg.VideoStream.URLs...)
				item.NeedSetToken = pg.VideoStream.NeedSetToken
				item.DurationSec = pg.VideoStream.DurationSec
				item.Gear = pg.VideoStream.GearName
				item.QualityName = pg.VideoStream.QualityLabel
				item.Codec = pg.VideoStream.Codec
				item.FPS = pg.VideoStream.FPS
				item.Bitrate = pg.VideoStream.BitRate
				if item.Width == 0 {
					item.Width = pg.VideoStream.Width
				}
				if item.Height == 0 {
					item.Height = pg.VideoStream.Height
				}
			}
			pages = append(pages, item)
		}
		plan.Pages = pages
		if len(assetIDs) > 0 {
			plan.AssetIDs = assetIDs
		}
	}

	if detail.Music != nil && (detail.Music.Title != "" || len(detail.Music.PlayURLs) > 0) {
		var covers []string
		if detail.Music.CoverURL != "" {
			covers = []string{detail.Music.CoverURL}
		}
		plan.Music = &download.PlannedMusic{
			ID:          detail.Music.ID,
			Title:       detail.Music.Title,
			Author:      detail.Music.Author,
			DurationSec: detail.Music.DurationSec,
			Original:    detail.Music.IsOriginalSound,
			CoverURLs:   covers,
			PlayURLs:    append([]string(nil), detail.Music.PlayURLs...),
		}
	}

	return plan, nil
}

func resolveDouyinCodecFromOrder(order []string, fallback string) string {
	for _, c := range order {
		switch strings.ToLower(strings.TrimSpace(c)) {
		case "hevc", "h265", "bytevc1":
			return "hevc"
		case "avc", "h264":
			return "avc"
		}
	}
	if strings.EqualFold(fallback, "hevc") {
		return "hevc"
	}
	return "avc"
}

func parseDouyinQualityCap(videoQuality string) int {
	switch strings.ToLower(strings.TrimSpace(videoQuality)) {
	case "2160", "2160p", "4k":
		return 2160
	case "1440", "1440p", "2k":
		return 1440
	case "1080", "1080p":
		return 1080
	case "720", "720p":
		return 720
	case "540", "540p":
		return 540
	case "480", "480p":
		return 480
	case "360", "360p":
		return 360
	default:
		return 0
	}
}

func selectDouyinVODStream(detail *douyin.WorkDetail, videoQuality, prefCodec string) (*douyin.SelectedVODStream, string) {
	if detail == nil {
		return nil, ""
	}
	capTier := parseDouyinQualityCap(videoQuality)
	if len(detail.AvailableStreams) == 0 {
		return detail.Video, ""
	}

	var candidates []douyin.SelectedVODStream
	for _, s := range detail.AvailableStreams {
		if len(s.URLs) == 0 {
			continue
		}
		if capTier <= 0 || s.Tier <= 0 || s.Tier <= capTier {
			candidates = append(candidates, s)
		}
	}

	if len(candidates) > 0 {
		best := pickBestDouyinStream(candidates, prefCodec, false)
		hint := ""
		if capTier > 0 && best.Tier > 0 && best.Tier < capTier {
			hint = fmt.Sprintf("最高仅提供 %s，已按可用最高档位缓存", firstNonEmpty(best.QualityLabel, fmt.Sprintf("%dP", best.Tier)))
		}
		return &best, hint
	}

	// Every available stream exceeds the requested cap; pick the lowest available tier.
	var valid []douyin.SelectedVODStream
	for _, s := range detail.AvailableStreams {
		if len(s.URLs) > 0 {
			valid = append(valid, s)
		}
	}
	if len(valid) == 0 {
		return detail.Video, ""
	}
	lowest := pickBestDouyinStream(valid, prefCodec, true)
	hint := fmt.Sprintf("未提供 %dP 及以下档位，已使用最低可用档位 %s", capTier, firstNonEmpty(lowest.QualityLabel, fmt.Sprintf("%dP", lowest.Tier)))
	return &lowest, hint
}

func pickBestDouyinStream(streams []douyin.SelectedVODStream, prefCodec string, preferLowestTier bool) douyin.SelectedVODStream {
	best := streams[0]
	for _, cand := range streams[1:] {
		if preferLowestTier {
			if cand.Tier > 0 && (best.Tier == 0 || cand.Tier < best.Tier) {
				best = cand
				continue
			}
			if cand.Tier != best.Tier {
				continue
			}
		} else {
			if cand.Tier > best.Tier {
				best = cand
				continue
			}
			if cand.Tier < best.Tier {
				continue
			}
		}
		candPref := strings.EqualFold(cand.Codec, prefCodec)
		bestPref := strings.EqualFold(best.Codec, prefCodec)
		if candPref && !bestPref {
			best = cand
			continue
		}
		if !candPref && bestPref {
			continue
		}
		if cand.BitRate > best.BitRate {
			best = cand
		}
	}
	return best
}
