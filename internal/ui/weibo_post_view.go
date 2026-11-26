package ui

import (
	"context"
	"fmt"
	"strings"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/egoist/mygo/ui"
)

func (a *App) cacheWeiboPostNow(mid string, via *library.ItemVia) {
	mid = strings.TrimSpace(mid)
	if mid == "" {
		return
	}
	backend := a.backend
	var viaCopy *library.ItemVia
	if via != nil {
		cp := *via
		viaCopy = &cp
	}
	requestGuarded(a, "cache-weibo-post:"+mid, nil, func(ctx context.Context) (struct{}, error) {
		_, err := backend.CacheWeiboPost(ctx, mid, viaCopy)
		return struct{}{}, err
	}, func(struct{}) {
		a.BannerInfo = "已加入缓存队列：" + mid
		a.RefreshData()
	}, func(err error) {
		a.showError(err)
		a.RefreshData()
	})
}

func (a *App) cacheWeiboVideoNow(oid string) {
	oid = strings.TrimSpace(oid)
	if oid == "" {
		return
	}
	backend := a.backend
	requestGuarded(a, "cache-weibo-video:"+oid, nil, func(ctx context.Context) (struct{}, error) {
		_, err := backend.CacheWeiboVideo(ctx, oid)
		return struct{}{}, err
	}, func(struct{}) {
		a.BannerInfo = "已加入缓存队列：" + oid
		a.RefreshData()
	}, func(err error) {
		a.showError(err)
		a.RefreshData()
	})
}

func formatWeiboVideoSpec(v *service.WeiboVideoDTO) string {
	if v == nil {
		return ""
	}
	var parts []string
	if q := firstNonEmptyStrUI(v.QualityLabel, v.QualityDesc); q != "" {
		parts = append(parts, q)
	} else if v.Width > 0 && v.Height > 0 {
		parts = append(parts, fmt.Sprintf("%dx%d", v.Width, v.Height))
	}
	if codec := strings.ToUpper(strings.TrimSpace(v.Codec)); codec != "" {
		switch codec {
		case "H264", "AVC1":
			codec = "AVC"
		case "H265", "HEV1", "HVC1":
			codec = "HEVC"
		}
		parts = append(parts, codec)
	}
	if v.FPS > 0 {
		parts = append(parts, fmt.Sprintf("%dfps", v.FPS))
	}
	return strings.Join(parts, " · ")
}

func isWeiboGIFURL(u string) bool {
	lower := strings.ToLower(strings.TrimSpace(u))
	if q := strings.IndexByte(lower, '?'); q >= 0 {
		lower = lower[:q]
	}
	return strings.HasSuffix(lower, ".gif")
}

func libraryRichCardToWeiboDTO(card *library.ItemRichCard) *service.WeiboRichCardDTO {
	if card == nil {
		return nil
	}
	if strings.TrimSpace(card.Kind) == "" && strings.TrimSpace(card.Label) == "" &&
		strings.TrimSpace(card.Title) == "" && strings.TrimSpace(card.Desc) == "" &&
		strings.TrimSpace(card.URL) == "" {
		return nil
	}
	return &service.WeiboRichCardDTO{
		Kind:  card.Kind,
		Label: card.Label,
		Title: card.Title,
		Desc:  card.Desc,
		URL:   card.URL,
	}
}

func isGenericWeiboRetweetText(s string) bool {
	switch strings.TrimSpace(s) {
	case "", "转发微博", "快转微博", "转发动态", "Repost", "repost":
		return true
	}
	return false
}

func hasAnyFromOriginFlag(item *library.ItemMetadata) bool {
	if item == nil {
		return false
	}
	if item.Origin != nil && item.Origin.VideoFromOrigin {
		return true
	}
	for _, p := range item.NotePages {
		if p.FromOrigin {
			return true
		}
	}
	for _, m := range item.Media {
		if m.FromOrigin {
			return true
		}
	}
	return false
}

func hasVideoNotePage(pages []douyinNoteRenderPage) bool {
	for _, pg := range pages {
		if pg.Kind == "video" {
			return true
		}
	}
	return false
}

func itemVideoBelongsToOrigin(item *library.ItemMetadata) bool {
	if item == nil || item.Origin == nil || item.Origin.Unavailable {
		return false
	}
	if item.Origin.VideoFromOrigin {
		return true
	}
	var hasOuterVid, hasOrigVid bool
	for _, m := range item.Media {
		if m.Role == "video" && m.Path != "" {
			if m.FromOrigin {
				hasOrigVid = true
			} else {
				hasOuterVid = true
			}
		}
	}
	if hasOrigVid && !hasOuterVid {
		return true
	}
	if !hasAnyFromOriginFlag(item) && isGenericWeiboRetweetText(item.Text) && itemHasVideoRole(*item) {
		return true
	}
	return false
}

func cachedItemHasVideoForScope(cached *library.ItemMetadata, fromOrigin bool, hasOtherSideVideo bool) (hasVideo bool, relCover string, duration int, videoRel string) {
	if cached == nil {
		return false, "", 0, ""
	}
	var anyVideo, scopedVideo bool
	var anyVideoRel, scopedVideoRel string
	var anyVideoDur, scopedVideoDur int
	var anyCover, scopedCover string
	for _, m := range cached.Media {
		if m.Role == "cover" && m.Path != "" {
			if anyCover == "" {
				anyCover = m.Path
			}
			if m.FromOrigin == fromOrigin && scopedCover == "" {
				scopedCover = m.Path
			}
		}
		if m.Role == "video" && m.Path != "" {
			anyVideo = true
			if anyVideoRel == "" {
				anyVideoRel = m.Path
			}
			if anyVideoDur == 0 && m.Duration > 0 {
				anyVideoDur = m.Duration
			}
			if m.FromOrigin == fromOrigin {
				scopedVideo = true
				if scopedVideoRel == "" {
					scopedVideoRel = m.Path
				}
				if scopedVideoDur == 0 && m.Duration > 0 {
					scopedVideoDur = m.Duration
				}
			}
		}
	}
	relCover = firstNonEmptyStrUI(scopedCover, anyCover)
	pickDur := func(d int) int {
		if d > 0 {
			return d
		}
		return cached.Duration
	}
	if scopedVideo {
		return true, relCover, pickDur(scopedVideoDur), scopedVideoRel
	}
	if fromOrigin && cached.Origin != nil && cached.Origin.VideoFromOrigin && anyVideo {
		return true, relCover, pickDur(anyVideoDur), anyVideoRel
	}
	if anyVideo && !hasOtherSideVideo {
		if fromOrigin && cached.Origin != nil && !cached.Origin.Unavailable && !hasAnyFromOriginFlag(cached) {
			return true, relCover, pickDur(anyVideoDur), anyVideoRel
		}
		if !fromOrigin && (cached.Origin == nil || !cached.Origin.VideoFromOrigin) {
			return true, relCover, pickDur(anyVideoDur), anyVideoRel
		}
	}
	return false, relCover, 0, ""
}

func splitLibraryMediaImagesByOrigin(item *library.ItemMetadata) (outer []PostImage, origin []PostImage) {
	if item == nil {
		return nil, nil
	}
	hasTagged := hasAnyFromOriginFlag(item)
	legacyOrigin := !hasTagged && item.Origin != nil && !item.Origin.Unavailable && isGenericWeiboRetweetText(item.Text)
	for _, m := range item.Media {
		if m.Role != "image" {
			continue
		}
		img := PostImage{
			RelPath: m.Path,
			Width:   m.Width,
			Height:  m.Height,
			Live:    m.Motion != "",
			IsGIF:   strings.HasSuffix(strings.ToLower(m.Path), ".gif"),
		}
		if m.FromOrigin || legacyOrigin {
			origin = append(origin, img)
		} else {
			outer = append(outer, img)
		}
	}
	return outer, origin
}

func weiboMediaFallbackURLs(it service.WeiboMediaItemDTO, primary string) []string {
	primary = strings.TrimSpace(primary)
	var out []string
	seen := map[string]bool{}
	if primary != "" {
		seen[primary] = true
	}
	add := func(u string) {
		u = strings.TrimSpace(u)
		if u != "" && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	for _, fb := range it.FallbackURLs {
		add(fb)
	}
	add(it.PublicPreviewURL)
	return out
}

func weiboMediaItemsToRenderPages(items []service.WeiboMediaItemDTO, offline bool) []douyinNoteRenderPage {
	if len(items) == 0 {
		return nil
	}
	out := make([]douyinNoteRenderPage, 0, len(items))
	for i, it := range items {
		idx := it.Index
		if idx <= 0 {
			idx = i + 1
		}
		kind := strings.ToLower(strings.TrimSpace(it.Kind))
		isGIF := kind == "gif" || isWeiboGIFURL(it.ImageURL)
		switch kind {
		case "live_photo", "livephoto", "motion":
			kind = "live_photo"
		case "video":
			kind = "video"
		default:
			kind = "image"
		}
		pg := douyinNoteRenderPage{
			Index:       idx,
			Kind:        kind,
			IsGIF:       isGIF,
			Width:       it.Width,
			Height:      it.Height,
			DurationSec: it.DurationSec,
		}
		if !offline {
			pg.ImageURL = firstNonEmptyStrUI(it.ImageURL, it.PublicPreviewURL)
			pg.FallbackImageURLs = weiboMediaFallbackURLs(it, pg.ImageURL)
			if kind == "live_photo" {
				pg.MotionURL = it.MotionURL
			} else if kind == "video" {
				pg.VideoURL = it.MotionURL
			}
		}
		out = append(out, pg)
	}
	return out
}

func weiboMediaToNotePages(items []service.WeiboMediaItemDTO, cached *library.ItemMetadata, offline bool) []douyinNoteRenderPage {
	return weiboMediaToNotePagesScoped(items, cached, false, len(items), offline)
}

// weiboMediaToNotePagesScoped builds render pages for either outer (fromOrigin=false)
// or origin (fromOrigin=true) media, resolving locally cached files from the outer
// cached item via FromOrigin tags (with outerImageCount / legacy fallback).
func weiboMediaToNotePagesScoped(items []service.WeiboMediaItemDTO, cached *library.ItemMetadata, fromOrigin bool, outerImageCount int, offline bool) []douyinNoteRenderPage {
	if cached != nil && len(cached.NotePages) > 0 {
		var outerRecs, origRecs []library.NotePageRecord
		hasTagged := false
		for _, rec := range cached.NotePages {
			if rec.FromOrigin {
				hasTagged = true
				break
			}
		}
		if hasTagged {
			for _, rec := range cached.NotePages {
				if rec.FromOrigin {
					origRecs = append(origRecs, rec)
				} else {
					outerRecs = append(outerRecs, rec)
				}
			}
		} else if !offline && (fromOrigin || outerImageCount < len(cached.NotePages)) {
			splitAt := min(max(0, outerImageCount), len(cached.NotePages))
			outerRecs = cached.NotePages[:splitAt]
			origRecs = cached.NotePages[splitAt:]
		} else if offline && cached.Origin != nil && !cached.Origin.Unavailable && isGenericWeiboRetweetText(cached.Text) {
			origRecs = cached.NotePages
		} else {
			outerRecs = cached.NotePages
		}

		recs := outerRecs
		if fromOrigin {
			recs = origRecs
		}
		if len(recs) > 0 {
			scopedItem := library.ItemMetadata{NotePages: recs, Media: cached.Media}
			pages := buildDouyinNoteRenderPages(nil, &scopedItem)
			for i := range pages {
				if isWeiboGIFURL(pages[i].ImageRel) {
					pages[i].IsGIF = true
				}
				if !offline && i < len(items) {
					it := items[i]
					if pages[i].ImageURL == "" {
						pages[i].ImageURL = firstNonEmptyStrUI(it.ImageURL, it.PublicPreviewURL)
						pages[i].FallbackImageURLs = weiboMediaFallbackURLs(it, pages[i].ImageURL)
					}
					if pages[i].Kind == "live_photo" && pages[i].MotionURL == "" {
						pages[i].MotionURL = it.MotionURL
					}
					if pages[i].Kind == "video" && pages[i].VideoURL == "" {
						pages[i].VideoURL = it.MotionURL
					}
					if it.Kind == "gif" || isWeiboGIFURL(it.ImageURL) {
						pages[i].IsGIF = true
					}
				}
			}
			if !offline && len(items) > len(pages) {
				pages = append(pages, weiboMediaItemsToRenderPages(items[len(pages):], false)...)
			}
			return pages
		}
	}

	if cached != nil && len(cached.NotePages) == 0 && len(cached.Media) > 0 {
		var outerMedia, origMedia []library.MediaFile
		hasTagged := false
		for _, m := range cached.Media {
			if m.Role == "image" && m.FromOrigin {
				hasTagged = true
				break
			}
		}
		var allImgs []library.MediaFile
		for _, m := range cached.Media {
			if m.Role == "image" {
				allImgs = append(allImgs, m)
			}
		}
		if hasTagged {
			for _, m := range allImgs {
				if m.FromOrigin {
					origMedia = append(origMedia, m)
				} else {
					outerMedia = append(outerMedia, m)
				}
			}
		} else if !offline && (fromOrigin || outerImageCount < len(allImgs)) {
			splitAt := min(max(0, outerImageCount), len(allImgs))
			outerMedia = allImgs[:splitAt]
			origMedia = allImgs[splitAt:]
		} else if offline && cached.Origin != nil && !cached.Origin.Unavailable && isGenericWeiboRetweetText(cached.Text) {
			origMedia = allImgs
		} else {
			outerMedia = allImgs
		}
		chosen := outerMedia
		if fromOrigin {
			chosen = origMedia
		}
		if len(chosen) > 0 {
			pages := buildDouyinNoteRenderPages(nil, &library.ItemMetadata{Media: chosen})
			for i := range pages {
				if pages[i].NoWatermarkRel == "" {
					pages[i].NoWatermarkRel = findCompanionNoWatermarkRel(cached, pages[i].ImageRel)
				}
				if isWeiboGIFURL(pages[i].ImageRel) {
					pages[i].IsGIF = true
				}
				if !offline && i < len(items) {
					it := items[i]
					if pages[i].ImageURL == "" {
						pages[i].ImageURL = firstNonEmptyStrUI(it.ImageURL, it.PublicPreviewURL)
						pages[i].FallbackImageURLs = weiboMediaFallbackURLs(it, pages[i].ImageURL)
					}
					if pages[i].Kind == "live_photo" && pages[i].MotionURL == "" {
						pages[i].MotionURL = it.MotionURL
					}
					if it.Kind == "gif" || isWeiboGIFURL(it.ImageURL) {
						pages[i].IsGIF = true
					}
				}
			}
			if !offline && len(items) > len(pages) {
				pages = append(pages, weiboMediaItemsToRenderPages(items[len(pages):], false)...)
			}
			return pages
		}
	}

	return weiboMediaItemsToRenderPages(items, offline)
}

func weiboCardKindLabel(card *service.WeiboRichCardDTO) string {
	if card == nil {
		return ""
	}
	if strings.TrimSpace(card.Label) != "" {
		return strings.TrimSpace(card.Label)
	}
	switch strings.ToLower(strings.TrimSpace(card.Kind)) {
	case "article":
		return "头条文章"
	case "vote":
		return "投票"
	case "music":
		return "音乐"
	case "live":
		return "微博直播"
	default:
		return "外链卡片"
	}
}

func (a *App) renderWeiboRichCard(c *ui.Context, card *service.WeiboRichCardDTO, offline bool) {
	if card == nil {
		return
	}
	kind := strings.ToLower(strings.TrimSpace(card.Kind))
	label := weiboCardKindLabel(card)
	ui.Column(c).
		Label("微博卡片："+kind).
		FillWidth().
		Padding(12, 14).
		Radius(12).
		Background(ColorBgSecondary).
		Border(1, ColorBorder).
		Gap(8).
		ClipX().
		Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(10).ClipX().Children(func() {
				if !offline && strings.TrimSpace(card.CoverURL) != "" {
					if bmp := a.lookupRemoteBitmap(card.CoverURL); bmp != nil {
						ui.Box(c).Size(64, 48).Radius(8).Clip().Shrink(0).Children(func() {
							ui.Image(c, bmp).Size(64, 48).Fit(ui.Cover)
						})
					}
				}
				ui.Column(c).Grow(1).Gap(3).ClipX().Children(func() {
					ui.Text(c, label).FontSize(11).FontWeight(600).TextColor(ColorTextMuted).SingleLine()
					if strings.TrimSpace(card.Title) != "" {
						ui.Text(c, card.Title).FontSize(14).LineHeight(LineHeightCompact).FontWeight(600).TextColor(ColorTextMain).MaxLines(2)
					}
					if strings.TrimSpace(card.Desc) != "" {
						ui.Text(c, card.Desc).FontSize(12).LineHeight(LineHeightCompact).TextColor(ColorTextMuted).MaxLines(2)
					}
				})
			})
			if kind == "live" {
				ui.Box(c).Label("微博直播不支持提示").FillWidth().Children(func() {
					ui.Text(c, "微博直播暂不支持录制，可以在浏览器中打开").
						FontSize(12).LineHeight(LineHeightCompact).TextColor(ColorTextMuted).MaxLines(2)
				})
			}
			if strings.TrimSpace(card.URL) != "" {
				targetURL := card.URL
				ui.Row(c).FillWidth().Justify(ui.End).Children(func() {
					if OutlinePillButton(c, "在浏览器中打开", "微博卡片在浏览器中打开："+kind).Clicked() {
						if err := a.backend.Desktop().OpenExternalURL(targetURL); err != nil {
							a.showError(err)
						}
					}
				})
			}
		})
}

func weiboPostTypeLabel(p service.WeiboPostView) string {
	if p.IsFastRetweet {
		return "快转"
	}
	if p.IsRetweet || p.Retweeted != nil {
		return "转发微博"
	}
	if p.Video != nil {
		return "视频"
	}
	if len(p.Images) > 0 {
		return "图文"
	}
	return "微博"
}

func (a *App) weiboPostToPostCard(p service.WeiboPostView, fallbackUser *service.WeiboUserView) PostCardData {
	authorUID := strings.TrimSpace(p.Author.UID)
	authorName := strings.TrimSpace(p.Author.ScreenName)
	authorFace := strings.TrimSpace(p.Author.AvatarURL)
	var forUID, forName string
	if fallbackUser != nil {
		forUID = strings.TrimSpace(fallbackUser.UID)
		forName = strings.TrimSpace(fallbackUser.ScreenName)
		if authorUID == "" {
			authorUID = forUID
		}
		if authorName == "" {
			authorName = forName
		}
		if authorFace == "" {
			authorFace = strings.TrimSpace(fallbackUser.AvatarURL)
		}
	}

	itemKey := p.CacheKey
	if itemKey == "" && p.Mid != "" {
		itemKey = library.CanonicalItemKey("weibo", "dynamic", p.Mid)
	}
	dot := p.CacheState.Dot
	cached := dot == "green" || dot == "yellow"
	var cachedItem *library.ItemMetadata
	if p.CachedItem != nil {
		cachedItem = p.CachedItem
	}
	if itemKey != "" && a.backend != nil {
		if stMap := a.backend.States([]string{itemKey}); len(stMap) > 0 {
			if st, ok := stMap[itemKey]; ok && st.Dot != "" {
				dot = st.Dot
				cached = dot == "green" || dot == "yellow"
			}
		}
		if cachedItem == nil && cached {
			if it, ok, err := a.backend.GetLibraryItem(itemKey); err == nil && ok {
				cachedItem = &it
			}
		}
	}

	var notePages []douyinNoteRenderPage
	if len(p.Images) > 0 {
		notePages = weiboMediaToNotePagesScoped(p.Images, cachedItem, false, len(p.Images), false)
	}

	var videoCard *PostVideo
	if p.Video != nil {
		hasLocalVid, relCover, _, _ := cachedItemHasVideoForScope(cachedItem, false, p.Retweeted != nil && p.Retweeted.Video != nil)
		videoCard = &PostVideo{
			BVID:       firstNonEmptyStrUI(p.Video.ObjectID, p.Mid),
			Title:      firstNonEmptyStrUI(p.Video.Title, "微博视频"),
			CoverURL:   firstNonEmptyStrUI(p.Video.CoverURL, p.Video.PublicCoverURL),
			RelCover:   relCover,
			Duration:   FormatDuration(p.Video.DurationSec),
			ItemKey:    itemKey,
			Cached:     cached && hasLocalVid,
			Status:     dot,
			QualityTag: formatWeiboVideoSpec(p.Video),
		}
	}

	var origin *PostCardData
	originUnavailable := ""
	if p.Retweeted != nil {
		rt := p.Retweeted
		if rt.Unavailable {
			originUnavailable = firstNonEmptyStrUI(rt.UnavailableReason, "原微博已删除或不可查看")
		} else {
			var origNotePages []douyinNoteRenderPage
			if len(rt.Images) > 0 {
				origNotePages = weiboMediaToNotePagesScoped(rt.Images, cachedItem, true, len(p.Images), false)
			}
			var origVideo *PostVideo
			if rt.Video != nil {
				hasOrigVid, origRelCover, _, _ := cachedItemHasVideoForScope(cachedItem, true, p.Video != nil)
				origVideo = &PostVideo{
					BVID:       firstNonEmptyStrUI(rt.Video.ObjectID, rt.Mid, p.Mid),
					Title:      firstNonEmptyStrUI(rt.Video.Title, "微博视频"),
					CoverURL:   firstNonEmptyStrUI(rt.Video.CoverURL, rt.Video.PublicCoverURL),
					RelCover:   origRelCover,
					Duration:   FormatDuration(rt.Video.DurationSec),
					ItemKey:    itemKey,
					Cached:     cached && hasOrigVid,
					Status:     dot,
					QualityTag: formatWeiboVideoSpec(rt.Video),
				}
			}
			origin = &PostCardData{
				ItemKey:    itemKey,
				Site:       "weibo",
				AuthorUID:  rt.Author.UID,
				AuthorName: firstNonEmptyStrUI(rt.Author.ScreenName, "原博主"),
				AuthorFace: rt.Author.AvatarURL,
				PubTs:      rt.CreatedAt,
				TypeLabel:  weiboPostTypeLabel(*rt),
				Text:       firstNonEmptyStrUI(rt.Text, rt.TextRaw),
				Links:      weiboPostLinksUI(rt, cachedItem, true),
				NotePages:  origNotePages,
				Video:      origVideo,
				WeiboCard:  rt.Card,
				WeiboMid:   rt.Mid,
				URL:        rt.CanonicalURL,
				Incomplete: p.OriginIncomplete,
			}
		}
	}

	postCopy := p
	coverPreview := ""
	if p.Video != nil {
		coverPreview = firstNonEmptyStrUI(p.Video.CoverURL, p.Video.PublicCoverURL)
	} else if len(p.Images) > 0 {
		coverPreview = firstNonEmptyStrUI(p.Images[0].ImageURL, p.Images[0].PublicPreviewURL)
	} else if p.Retweeted != nil && !p.Retweeted.Unavailable {
		if p.Retweeted.Video != nil {
			coverPreview = firstNonEmptyStrUI(p.Retweeted.Video.CoverURL, p.Retweeted.Video.PublicCoverURL)
		} else if len(p.Retweeted.Images) > 0 {
			coverPreview = firstNonEmptyStrUI(p.Retweeted.Images[0].ImageURL, p.Retweeted.Images[0].PublicPreviewURL)
		}
	}
	text := firstNonEmptyStrUI(p.Text, p.TextRaw)
	cacheVia := weiboCacheViaForContext(&postCopy, forUID, forName)

	return PostCardData{
		Key:               "wb:" + p.Mid,
		ItemKey:           itemKey,
		Site:              "weibo",
		Kind:              "dynamic",
		RawID:             p.Mid,
		AuthorUID:         authorUID,
		AuthorName:        authorName,
		AuthorFace:        authorFace,
		PubTs:             p.CreatedAt,
		TypeLabel:         weiboPostTypeLabel(p),
		Text:              text,
		Links:             weiboPostLinksUI(&p, cachedItem, false),
		URL:               p.CanonicalURL,
		Pinned:            p.IsTop,
		VisibleLabel:      p.VisibleLabel,
		Cached:            cached,
		DotStatus:         dot,
		DotLabel:          "已缓存：" + p.Mid,
		NotePages:         notePages,
		Video:             videoCard,
		WeiboCard:         p.Card,
		WeiboMid:          p.Mid,
		WeiboCacheVia:     cacheVia,
		Origin:            origin,
		OriginUnavailable: originUnavailable,
		LibItem:           cachedItem,
		OpenBtnLabel:      "打开微博：" + p.Mid,
		CacheBtnLabel:     "缓存微博：" + p.Mid,
		OnOpenPost: func() {
			if forUID != "" {
				a.openWeiboPostForPreview(postCopy.Mid, clampRunes(text, 40), authorName, authorUID, coverPreview, postCopy.CanonicalURL, forUID, forName)
			} else {
				a.OpenWeiboPost(postCopy.Mid, clampRunes(text, 40), authorName, authorUID, coverPreview, postCopy.CanonicalURL)
			}
		},
	}
}

// renderWeiboPostRoute draws /weibo/post/:mid, standalone /weibo/video, or
// offline /library/item for cached Weibo items (FR-WB-003).
func (a *App) renderWeiboPostRoute(c *ui.Context, viewportHeight float32) {
	if a.Opening != nil {
		a.renderOpening(c)
		return
	}
	offline := a.CurrentRoute == RouteLibraryItem && a.CurrentLibraryDetail != nil
	if offline {
		a.renderWeiboOfflineDetail(c, *a.CurrentLibraryDetail)
		return
	}
	if a.CurrentWeiboPost == nil && a.CurrentWeiboVideo != nil {
		a.renderWeiboStandaloneVideo(c, a.CurrentWeiboVideo)
		return
	}
	if a.CurrentWeiboPost == nil {
		ui.Column(c).Grow(1).FillWidth().Padding(32, 16).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "未加载微博内容").FontSize(15).FontWeight(600).TextColor(ColorTextMain)
			ui.Text(c, "可在解析页粘贴微博链接或分享文本打开。").FontSize(13).TextColor(ColorTextMuted)
		})
		return
	}
	a.renderWeiboOnlinePostDetail(c, a.CurrentWeiboPost)
}

func (a *App) renderWeiboOnlinePostDetail(c *ui.Context, post *service.WeiboPostView) {
	itemKey := post.CacheKey
	if itemKey == "" && post.Mid != "" {
		itemKey = library.CanonicalItemKey("weibo", "dynamic", post.Mid)
	}
	var cachedItem *library.ItemMetadata
	if post.CachedItem != nil {
		cachedItem = post.CachedItem
	}
	dot := post.CacheState.Dot
	if itemKey != "" && a.backend != nil {
		if stMap := a.backend.States([]string{itemKey}); len(stMap) > 0 {
			if st, ok := stMap[itemKey]; ok && st.Dot != "" {
				dot = st.Dot
			}
		}
		if it, ok, err := a.backend.GetLibraryItem(itemKey); err == nil && ok {
			cachedItem = &it
			if dot == "" {
				dot = library.ComputeDotState(it).Dot
			}
		}
	}

	authorUID := strings.TrimSpace(post.Author.UID)
	authorName := firstNonEmptyStrUI(post.Author.ScreenName, "未知博主")
	openAuthor := func() {
		if authorUID != "" {
			a.OpenWeiboUser(authorUID, authorName)
		}
	}

	// Article / vote / live degraded cards MUST NOT show a video cache button
	// unless the retweeted origin carries cacheable images or video.
	hasRetweetMedia := post.Retweeted != nil && !post.Retweeted.Unavailable &&
		(len(post.Retweeted.Images) > 0 || post.Retweeted.Video != nil)
	hasDegradedNoVideoCard := post.Card != nil && post.Video == nil && len(post.Images) == 0 && !hasRetweetMedia &&
		(post.Card.Kind == "article" || post.Card.Kind == "vote" || post.Card.Kind == "live")

	ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
		ui.Column(c).FillWidth().Padding(16).Gap(14).ClipX().Children(func() {
			// 1. Author & Primary Action Header
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(10).ClipX().Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(10).Grow(1).ClipX().Children(func() {
					a.renderAccountAvatar(c, "weibo", authorName, authorUID, 0, 44, false, "打开微博博主："+authorUID, openAuthor, post.Author.AvatarURL)
					ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
						ui.Row(c).AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
							btn := ui.ButtonBase(c).Label("微博详情作者：" + authorUID).Padding(0).Children(func() {
								ui.Text(c, authorName).FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
							})
							if btn.Clicked() {
								openAuthor()
							}
							renderSiteSmallBadgeWithDot(c, "weibo", "微博详情站点标记")
							if post.IsTop {
								ui.Box(c).Padding(1, 6).Radius(4).Background(ColorAccentBg).Children(func() {
									ui.Text(c, "置顶").FontSize(11).FontWeight(600).TextColor(ColorAccent).SingleLine()
								})
							}
						})
						var metaParts []string
						if post.CreatedAt > 0 {
							metaParts = append(metaParts, FormatTimestamp(post.CreatedAt))
						}
						if s := strings.TrimSpace(post.RegionName); s != "" {
							metaParts = append(metaParts, s)
						}
						if s := strings.TrimSpace(post.Source); s != "" {
							metaParts = append(metaParts, "来自 "+s)
						}
						if len(metaParts) > 0 {
							ui.Text(c, strings.Join(metaParts, " · ")).FontSize(12).TextColor(ColorTextMuted).SingleLine()
						}
						if vr := strings.TrimSpace(post.Author.VerifiedReason); vr != "" {
							ui.Text(c, vr).FontSize(12).TextColor(ColorTextMuted).SingleLine()
						}
					})
				})

				ui.Row(c).Shrink(0).AlignItems(ui.Center).Gap(8).Children(func() {
					if dot != "" && dot != "none" {
						DrawStatusDot(c, dot, "微博缓存状态："+post.Mid)
					}
					if !hasDegradedNoVideoCard && post.Mid != "" {
						cacheLabel := "缓存微博"
						if (post.Video != nil && len(post.Images) == 0) ||
							(post.Retweeted != nil && !post.Retweeted.Unavailable && post.Retweeted.Video != nil && len(post.Images) == 0 && len(post.Retweeted.Images) == 0) {
							cacheLabel = "缓存视频"
						} else if len(post.Images) > 0 || (post.Retweeted != nil && !post.Retweeted.Unavailable && len(post.Retweeted.Images) > 0) {
							cacheLabel = "缓存图文"
						}
						if dot == "green" {
							cacheLabel = "重新缓存"
						}
						busy := a.isPending("cache-weibo-post:" + post.Mid)
						if AccentPillButton(c, cacheLabel, "缓存当前微博").Disabled(busy).Clicked() && !busy {
							a.cacheWeiboPostNow(post.Mid, a.weiboCacheViaFor(post))
						}
					}

					menuKey := "wb-detail:" + post.Mid
					open := a.PostMenuKey == menuKey
					MoreMenuButton(c, "微博详情更多", 32, &open, func(m *ui.Menu) {
						if !hasDegradedNoVideoCard && post.Mid != "" && dot != "green" {
							if m.Item("缓存").Chosen() {
								a.cacheWeiboPostNow(post.Mid, a.weiboCacheViaFor(post))
							}
						}
						targetURL := firstNonEmptyStrUI(post.CanonicalURL, "https://weibo.com/detail/"+post.Mid)
						if targetURL != "" {
							if m.Item("复制链接").Chosen() {
								c.WriteClipboard(targetURL)
								c.Toast("已复制链接")
								a.BannerInfo = "已复制链接"
							}
							if m.Item("在浏览器中打开").Chosen() {
								if err := a.backend.Desktop().OpenExternalURL(targetURL); err != nil {
									a.showError(err)
								}
							}
						}
						if cachedItem != nil && itemKey != "" {
							if !cachedItem.IsArchivedSnapshot() {
								if m.Item("强制重新下载").Chosen() {
									a.openForceRecacheConfirm(itemKey, firstNonEmptyStrUI(cachedItem.Title, post.Text, post.Mid))
								}
							}
							if m.Item("打开文件夹").Chosen() {
								a.showError(a.backend.OpenItemFolder(itemKey))
							}
						}
					})
					keyedMenuOpen(&a.PostMenuKey, menuKey, open)
				})
			})

			// 2. Visible-Range Hint
			if strings.TrimSpace(post.VisibleLabel) != "" {
				ui.Row(c).
					Label("微博可见范围标记").
					FillWidth().
					Padding(8, 12).
					Radius(8).
					Background(ColorBgSecondary).
					AlignItems(ui.Center).
					Gap(6).
					Children(func() {
						DrawIcon(c, "eye", 15, ColorTextMuted, 1.7)
						ui.Text(c, post.VisibleLabel).FontSize(12).TextColor(ColorTextMuted).SingleLine()
					})
			}

			// 3. Full Body Text (Longtext rendered completely)
			body := firstNonEmptyStrUI(post.Text, post.TextRaw)
			if strings.TrimSpace(body) != "" {
				a.renderRichText(c, body, weiboPostLinksUI(post, cachedItem, false), richTextOptions{
					Label:      "微博正文",
					FontSize:   15,
					LineHeight: LineHeightBody,
					TextColor:  ColorTextMain,
				})
			}

			// 4. Multi-image (>9 images shown in full) / GIF / LivePhoto / Mixed media in original order
			if len(post.Images) > 0 {
				pages := weiboMediaToNotePagesScoped(post.Images, cachedItem, false, len(post.Images), false)
				a.renderNotePagesGrid(c, itemKey, "weibo", pages, true)
			}

			var weiboCache previewCacheAction
			if !hasDegradedNoVideoCard && post.Mid != "" {
				midCopy := post.Mid
				viaCopy := a.weiboCacheViaFor(post)
				weiboCache = previewCacheAction{
					ItemKey:    itemKey,
					PendingKey: "cache-weibo-post:" + midCopy,
					OnCache:    func() { a.cacheWeiboPostNow(midCopy, viaCopy) },
				}
			}

			// 5. Single Video Card (cover, duration, resolution/codec)
			if post.Video != nil {
				a.renderWeiboDetailVideoCard(c, post.Video, itemKey, cachedItem, false, post.Retweeted != nil && post.Retweeted.Video != nil, false, weiboCache)
			}

			// 6. Degraded Rich Card (article, vote, music, webpage, live)
			if post.Card != nil {
				a.renderWeiboRichCard(c, post.Card, false)
			}

			// 7. Retweet Origin Card (or degraded prompt when deleted/unavailable)
			if post.Retweeted != nil {
				a.renderWeiboRetweetCard(c, post.Retweeted, itemKey, cachedItem, len(post.Images), post.Video != nil, false, weiboCache)
			}

			// 8. Online-Only Stats Row (never shown in offline /library/item mode)
			ui.Row(c).
				Label("微博在线统计").
				FillWidth().
				Padding(10, 0, 0, 0).
				BorderWidth(1, 0, 0, 0).
				BorderColor(ColorBorder).
				Gap(18).
				AlignItems(ui.Center).
				Children(func() {
					ui.Text(c, "转发 "+FormatCount(int64(post.Stats.RepostsCount))).FontSize(13).TextColor(ColorTextMuted).SingleLine()
					ui.Text(c, "评论 "+FormatCount(int64(post.Stats.CommentsCount))).FontSize(13).TextColor(ColorTextMuted).SingleLine()
					ui.Text(c, "赞 "+FormatCount(int64(post.Stats.AttitudesCount))).FontSize(13).TextColor(ColorTextMuted).SingleLine()
				})
		})
	})
}

func (a *App) renderWeiboDetailVideoCard(c *ui.Context, v *service.WeiboVideoDTO, itemKey string, cachedItem *library.ItemMetadata, fromOrigin bool, hasOtherSideVideo bool, offline bool, cacheAct previewCacheAction) {
	if v == nil {
		return
	}
	hasLocalVideo, relCover, _, videoRel := cachedItemHasVideoForScope(cachedItem, fromOrigin, hasOtherSideVideo)
	coverURL := ""
	if !offline {
		coverURL = firstNonEmptyStrUI(v.CoverURL, v.PublicCoverURL)
	}
	dur := FormatDuration(v.DurationSec)
	spec := formatWeiboVideoSpec(v)
	hasPreviewURL := !offline && strings.TrimSpace(v.VideoURL) != ""

	card := ui.ButtonBase(c).
		Label("微博视频卡片").
		FillWidth().
		Radius(12).
		Border(1, ColorBorder).
		Background(ColorBgMain).
		Clip()
	if card.Hovered() {
		card.Background(ColorHover)
	}
	card.Children(func() {
		ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Start).ClipX().Children(func() {
			a.renderMediaThumb(c, itemKey, relCover, "", dur, "", hasLocalVideo || hasPreviewURL, 200, 112, coverURL)
			ui.Column(c).Grow(1).Basis(0).Padding(10, 12, 10, 0).Gap(4).ClipX().Children(func() {
				ui.Text(c, firstNonEmptyStrUI(v.Title, "微博视频")).FontSize(14).LineHeight(LineHeightCompact).FontWeight(600).TextColor(ColorTextMain).MaxLines(2)
				if spec != "" {
					ui.Text(c, spec).Label("微博视频规格").FontSize(12).TextColor(ColorTextMuted).SingleLine()
				}
				if hasLocalVideo {
					ui.Text(c, "已缓存到本地，点击播放").FontSize(12).TextColor(ColorDotGreen).SingleLine()
				} else if hasPreviewURL {
					ui.Text(c, "点击预览播放，或点上方「缓存」保存到本机").FontSize(12).TextColor(ColorInfoBlue).SingleLine()
				} else if !offline {
					ui.Box(c).Label("微博视频缓存提示").FillWidth().Children(func() {
						ui.Text(c, "点击上方「缓存」后可本机播放").FontSize(12).TextColor(ColorTextMuted).SingleLine()
					})
				}
			})
		})
	})
	if card.Clicked() {
		if hasLocalVideo && cachedItem != nil {
			a.openPlayerFile(*cachedItem, videoRel)
		} else if hasPreviewURL {
			a.openRemoteVideoPreviewWithCache("weibo", firstNonEmptyStrUI(v.Title, "微博视频"), v.VideoURL, false, cacheAct)
		} else if !offline {
			c.Toast("点击上方「缓存」后可本机播放")
			a.BannerInfo = "点击上方「缓存」后可本机播放"
		}
	}
}

func (a *App) renderWeiboRetweetCard(c *ui.Context, rt *service.WeiboPostView, outerItemKey string, outerCachedItem *library.ItemMetadata, outerImageCount int, hasOuterVideo bool, offline bool, cacheAct previewCacheAction) {
	if rt == nil {
		return
	}
	if rt.Unavailable {
		ui.Row(c).
			Label("转发原微博不可用提示").
			FillWidth().
			Padding(12, 14).
			Radius(12).
			Background(ColorBgSecondary).
			Border(1, ColorBorder).
			AlignItems(ui.Center).
			Gap(8).
			ClipX().
			Children(func() {
				DrawIcon(c, "alert-circle", 16, ColorTextMuted, 1.75)
				ui.Text(c, firstNonEmptyStrUI(rt.UnavailableReason, "原微博已删除或不可查看")).
					FontSize(13).LineHeight(LineHeightCompact).TextColor(ColorTextMuted).MaxLines(2)
			})
		return
	}
	rtAuthorUID := strings.TrimSpace(rt.Author.UID)
	rtAuthorName := firstNonEmptyStrUI(rt.Author.ScreenName, "原博主")
	bindKey := firstNonEmptyStrUI(outerItemKey, rt.CacheKey)
	bindCached := outerCachedItem
	if bindCached == nil {
		bindCached = rt.CachedItem
	}
	ui.Column(c).
		Label("转发原微博卡片").
		FillWidth().
		Padding(12, 14).
		Radius(12).
		Background(ColorBgSecondary).
		Border(1, ColorBorder).
		Gap(8).
		ClipX().
		Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(6).Grow(1).ClipX().Children(func() {
					var openOrig func()
					if !offline && rtAuthorUID != "" {
						openOrig = func() { a.OpenWeiboUser(rtAuthorUID, rtAuthorName) }
					}
					face := rt.Author.AvatarURL
					if offline {
						face = ""
					}
					a.renderAccountAvatar(c, "weibo", rtAuthorName, rtAuthorUID, 0, 24, false, "", openOrig, face)
					if openOrig != nil {
						btn := ui.ButtonBase(c).Padding(0).Children(func() {
							ui.Text(c, "@"+rtAuthorName).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
						})
						if btn.Clicked() {
							openOrig()
						}
					} else {
						ui.Text(c, "@"+rtAuthorName).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					}
					if rt.CreatedAt > 0 {
						ui.Text(c, "· "+FormatRelativeTime(rt.CreatedAt)).
							FontSize(12).
							TextColor(ColorTextMuted).
							Tooltip(FormatTimestamp(rt.CreatedAt)).
							SingleLine()
					}
				})
				if !offline && strings.TrimSpace(rt.Mid) != "" {
					openOrigBtn := ui.ButtonBase(c).
						Label("打开原微博").
						Shrink(0).
						Padding(3, 10).
						Radius(999).
						Background(ColorBgMain).
						Border(1, ColorBorder)
					if openOrigBtn.Hovered() {
						openOrigBtn.Background(ColorHover)
					}
					openOrigBtn.Children(func() {
						ui.Text(c, "打开原微博").FontSize(12).FontWeight(600).TextColor(ColorInfoBlue).SingleLine()
					})
					if openOrigBtn.Clicked() {
						origText := firstNonEmptyStrUI(rt.Text, rt.TextRaw)
						a.OpenWeiboPost(rt.Mid, clampRunes(origText, 40), rtAuthorName, rtAuthorUID, "", rt.CanonicalURL)
					}
				}
			})
			if txt := firstNonEmptyStrUI(rt.Text, rt.TextRaw); strings.TrimSpace(txt) != "" {
				a.renderRichText(c, txt, weiboPostLinksUI(rt, bindCached, true), richTextOptions{
					Label:      "转发原微博正文",
					FontSize:   14,
					LineHeight: LineHeightBody,
					TextColor:  ColorTextMain,
				})
			}
			if len(rt.Images) > 0 || (offline && bindCached != nil) {
				pages := weiboMediaToNotePagesScoped(rt.Images, bindCached, true, outerImageCount, offline)
				if len(pages) > 0 {
					a.renderNotePagesGrid(c, bindKey, "weibo", pages, true)
				}
			}
			if rt.Video != nil {
				a.renderWeiboDetailVideoCard(c, rt.Video, bindKey, bindCached, true, hasOuterVideo, offline, cacheAct)
			}
			if rt.Card != nil {
				a.renderWeiboRichCard(c, rt.Card, offline)
			}
		})
}

func (a *App) renderWeiboStandaloneVideo(c *ui.Context, vv *service.WeiboVideoView) {
	itemKey := vv.CacheKey
	if itemKey == "" && vv.ObjectID != "" {
		itemKey = library.CanonicalItemKey("weibo", "video", vv.ObjectID)
	}
	var cachedItem *library.ItemMetadata
	if vv.CachedItem != nil {
		cachedItem = vv.CachedItem
	}
	dot := vv.CacheState.Dot
	if itemKey != "" && a.backend != nil {
		if stMap := a.backend.States([]string{itemKey}); len(stMap) > 0 {
			if st, ok := stMap[itemKey]; ok && st.Dot != "" {
				dot = st.Dot
			}
		}
		if it, ok, err := a.backend.GetLibraryItem(itemKey); err == nil && ok {
			cachedItem = &it
			if dot == "" {
				dot = library.ComputeDotState(it).Dot
			}
		}
	}

	authorUID := strings.TrimSpace(vv.AuthorUID)
	authorName := firstNonEmptyStrUI(vv.AuthorName, "微博作者")
	openAuthor := func() {
		if authorUID != "" {
			a.OpenWeiboUser(authorUID, authorName)
		}
	}

	ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
		ui.Column(c).FillWidth().Padding(16).Gap(14).ClipX().Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(10).ClipX().Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(10).Grow(1).ClipX().Children(func() {
					a.renderAccountAvatar(c, "weibo", authorName, authorUID, 0, 44, false, "", openAuthor)
					ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
						ui.Row(c).AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
							ui.Text(c, authorName).FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
							renderSiteSmallBadgeWithDot(c, "weibo", "微博视频详情站点标记")
						})
					})
				})
				ui.Row(c).Shrink(0).AlignItems(ui.Center).Gap(8).Children(func() {
					if dot != "" && dot != "none" {
						DrawStatusDot(c, dot, "微博视频缓存状态："+vv.ObjectID)
					}
					if vv.ObjectID != "" {
						label := "缓存视频"
						if dot == "green" {
							label = "重新缓存"
						}
						busy := a.isPending("cache-weibo-video:" + vv.ObjectID)
						if AccentPillButton(c, label, "缓存当前微博视频").Disabled(busy).Clicked() && !busy {
							a.cacheWeiboVideoNow(vv.ObjectID)
						}
					}
					menuKey := "wb-video:" + vv.ObjectID
					open := a.PostMenuKey == menuKey
					MoreMenuButton(c, "微博视频详情更多", 32, &open, func(m *ui.Menu) {
						targetURL := firstNonEmptyStrUI(vv.CanonicalURL, "https://weibo.com/tv/show/"+vv.ObjectID)
						if targetURL != "" {
							if m.Item("复制链接").Chosen() {
								c.WriteClipboard(targetURL)
								c.Toast("已复制链接")
								a.BannerInfo = "已复制链接"
							}
							if m.Item("在浏览器中打开").Chosen() {
								if err := a.backend.Desktop().OpenExternalURL(targetURL); err != nil {
									a.showError(err)
								}
							}
						}
						if cachedItem != nil && itemKey != "" {
							if !cachedItem.IsArchivedSnapshot() {
								if m.Item("强制重新下载").Chosen() {
									a.openForceRecacheConfirm(itemKey, firstNonEmptyStrUI(cachedItem.Title, vv.Title, vv.ObjectID))
								}
							}
							if m.Item("打开文件夹").Chosen() {
								a.showError(a.backend.OpenItemFolder(itemKey))
							}
						}
					})
					keyedMenuOpen(&a.PostMenuKey, menuKey, open)
				})
			})

			if strings.TrimSpace(vv.Title) != "" {
				ui.Text(c, vv.Title).FontSize(16).LineHeight(LineHeightCompact).FontWeight(600).TextColor(ColorTextMain)
			}
			if vv.Video != nil {
				var videoCache previewCacheAction
				if vv.ObjectID != "" {
					oidCopy := vv.ObjectID
					videoCache = previewCacheAction{
						ItemKey:    itemKey,
						PendingKey: "cache-weibo-video:" + oidCopy,
						OnCache:    func() { a.cacheWeiboVideoNow(oidCopy) },
					}
				}
				a.renderWeiboDetailVideoCard(c, vv.Video, itemKey, cachedItem, false, false, false, videoCache)
			}
		})
	})
}

func (a *App) renderWeiboOfflineDetail(c *ui.Context, item library.ItemMetadata) {
	outerPages := weiboMediaToNotePagesScoped(nil, &item, false, 0, true)
	originPages := weiboMediaToNotePagesScoped(nil, &item, true, 0, true)
	hasVideo := itemHasVideoRole(item)
	videoFromOrigin := itemVideoBelongsToOrigin(&item)
	dur := item.Duration
	for _, m := range item.Media {
		if m.Role == "video" && dur == 0 && m.Duration > 0 {
			dur = m.Duration
		}
	}
	isForward := item.IsForwardContent() || item.Origin != nil
	crossAccountForward := item.Via != nil && item.Via.Reason == library.ViaForward &&
		strings.TrimSpace(item.Via.UID) != "" &&
		strings.TrimSpace(item.Via.UID) != strings.TrimSpace(item.Author.UID)

	ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
		ui.Column(c).FillWidth().Padding(16).Gap(14).ClipX().Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(10).ClipX().Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(10).Grow(1).ClipX().Children(func() {
					a.renderAccountAvatar(c, "weibo", firstNonEmptyStrUI(item.Author.Name, "微博博主"), item.Author.UID, 0, 44, false, "", nil)
					ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
						ui.Row(c).AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
							ui.Text(c, firstNonEmptyStrUI(item.Author.Name, "微博博主")).FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
							renderSiteSmallBadgeWithDot(c, "weibo", "本机微博详情站点标记")
							if isForward {
								ui.Box(c).Label("本机转发微博标记").Padding(1, 6).Radius(4).Background(ColorBgSecondary).Children(func() {
									ui.Text(c, "转发微博").FontSize(11).FontWeight(600).TextColor(ColorTextMuted).SingleLine()
								})
							}
						})
						if item.PubTs > 0 {
							ui.Text(c, FormatTimestamp(item.PubTs)).FontSize(12).TextColor(ColorTextMuted).SingleLine()
						}
					})
				})
				a.renderLibraryItemMoreMenu(c, item, 32)
			})

			if crossAccountForward {
				ui.Box(c).Label("本机转发来源标记").FillWidth().Children(func() {
					ui.Text(c, "由 "+firstNonEmptyStrUI(item.Via.Name, item.Via.UID)+" 转发").
						FontSize(12).
						TextColor(ColorTextMuted).
						SingleLine()
				})
			}
			if body := firstNonEmptyStrUI(item.Text, item.Desc, item.Title); strings.TrimSpace(body) != "" {
				a.renderRichText(c, body, service.MapItemLinksToDTO(item.Links), richTextOptions{
					Label:      "微博正文",
					FontSize:   15,
					LineHeight: LineHeightBody,
					TextColor:  ColorTextMain,
				})
			}
			if len(outerPages) > 0 {
				a.renderNotePagesGrid(c, item.ItemKey, "weibo", outerPages, true)
			}
			if hasVideo && !videoFromOrigin && !hasVideoNotePage(outerPages) {
				a.renderWeiboDetailVideoCard(c, &service.WeiboVideoDTO{
					Title:       firstNonEmptyStrUI(item.Title, "微博视频"),
					DurationSec: dur,
				}, item.ItemKey, &item, false, false, true, previewCacheAction{})
			}
			if outerCard := libraryRichCardToWeiboDTO(item.Card); outerCard != nil {
				a.renderWeiboRichCard(c, outerCard, true)
			}
			if item.Origin != nil {
				orig := item.Origin
				rtView := &service.WeiboPostView{
					Mid:               orig.ID,
					MblogID:           orig.MblogID,
					Author:            service.WeiboAuthorDTO{UID: orig.Author.UID, ScreenName: orig.Author.Name},
					CreatedAt:         orig.PubTs,
					Text:              orig.Text,
					Links:             service.MapItemLinksToDTO(orig.Links),
					Card:              libraryRichCardToWeiboDTO(orig.Card),
					Unavailable:       orig.Unavailable,
					UnavailableReason: orig.UnavailableReason,
					CanonicalURL:      orig.CanonicalURL,
					CacheKey:          item.ItemKey,
				}
				if videoFromOrigin && hasVideo && !hasVideoNotePage(originPages) {
					rtView.Video = &service.WeiboVideoDTO{
						Title:       firstNonEmptyStrUI(orig.Title, item.Title, "微博视频"),
						DurationSec: dur,
					}
				}
				a.renderWeiboRetweetCard(c, rtView, item.ItemKey, &item, 0, false, true, previewCacheAction{})
			}
		})
	})
}

func weiboPostLinksUI(p *service.WeiboPostView, cached *library.ItemMetadata, fromOrigin bool) []service.TextLinkDTO {
	if p != nil && len(p.Links) > 0 {
		return p.Links
	}
	if cached != nil {
		if fromOrigin {
			if cached.Origin != nil && len(cached.Origin.Links) > 0 {
				return service.MapItemLinksToDTO(cached.Origin.Links)
			}
		} else if len(cached.Links) > 0 {
			return service.MapItemLinksToDTO(cached.Links)
		}
	}
	return nil
}
