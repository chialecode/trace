package ui

import (
	"strings"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site/bilibili"
)

// formatPartQualitySpec joins a cached video part's quality, optional codec,
// and audio description with " · ".
func formatPartQualitySpec(p library.VideoPartRecord, includeCodecs bool) string {
	var parts []string
	if p.Video.QualityName != "" {
		parts = append(parts, p.Video.QualityName)
	}
	if includeCodecs && p.Video.Codecs != "" {
		parts = append(parts, strings.ToUpper(p.Video.Codecs))
	}
	if p.Audio.Name != "" {
		parts = append(parts, p.Audio.Name)
	}
	return strings.Join(parts, " · ")
}

// formatLiveRecordingSpec formats the live stream quality and codec (e.g. "原画 · AVC")
// from LiveRecordingInfo, falling back to Segments[i] when needed.
func formatLiveRecordingSpec(liveInfo *library.LiveRecordingInfo, segPath string) string {
	if liveInfo == nil {
		return ""
	}
	qName, qn, codec := liveInfo.QualityName, liveInfo.Quality, liveInfo.Codec
	if segPath != "" {
		for _, seg := range liveInfo.Segments {
			if seg.File == segPath {
				if seg.QualityName != "" || seg.Quality > 0 || seg.Codec != "" {
					qName, qn, codec = seg.QualityName, seg.Quality, seg.Codec
				}
				break
			}
		}
	}
	if qName == "" && qn == 0 && codec == "" && len(liveInfo.Segments) > 0 {
		s0 := liveInfo.Segments[0]
		qName, qn, codec = s0.QualityName, s0.Quality, s0.Codec
	}
	return bilibili.FormatLiveQualitySpec(qName, qn, codec)
}

// itemHasVideoRole reports whether a cached library item contains a primary video file.
func itemHasVideoRole(item library.ItemMetadata) bool {
	for _, m := range item.Media {
		if m.Role == "video" && m.Path != "" {
			return true
		}
	}
	return false
}

// itemKindLabel names a cached item's kind for people, never the internal value.
func itemKindLabel(kind string) string {
	switch kind {
	case "dynamic":
		return "动态"
	case "live":
		return "直播录像"
	}
	return "视频"
}

// feedEntryToPostCard returns the card for a feed entry, built once until
// tasks or the library change (see App.postCards).
func (a *App) feedEntryToPostCard(entry library.FeedEntry) PostCardData {
	return a.cachedPostCard("feed:"+entry.FeedKey, func() PostCardData { return a.buildFeedPostCard(entry) })
}

func (a *App) cachedPostCard(key string, build func() PostCardData) PostCardData {
	if card, ok := a.postCards[key]; ok {
		return card
	}
	card := build()
	if a.postCards == nil {
		a.postCards = make(map[string]PostCardData)
	}
	if len(a.postCards) > 600 {
		clear(a.postCards)
	}
	a.postCards[key] = card
	return card
}

func (a *App) buildFeedPostCard(entry library.FeedEntry) PostCardData {
	typeLabel := itemKindLabel(entry.Kind)
	dot := entry.CacheStatus
	if entry.Cached && dot == "" {
		dot = "green"
	}

	var libPtr *library.ItemMetadata
	var relCover string
	var qualityTag string
	var durationStr string
	var images []PostImage
	var origImages []PostImage
	var notePages []douyinNoteRenderPage
	var origNotePages []douyinNoteRenderPage
	var weiboCard *service.WeiboRichCardDTO
	var xCard *service.XRichCardDTO
	var hlsOnlyNotice string
	var threadCount int
	authorUID := strings.TrimSpace(entry.AuthorUID)
	authorHandle := ""
	normSite := normalizeSiteIDUI(entry.Site)

	if entry.Cached && entry.ItemKey != "" {
		if item, ok, err := a.backend.GetLibraryItemMetaOnly(entry.ItemKey); err == nil && ok {
			libPtr = &item
			if authorUID == "" {
				authorUID = strings.TrimSpace(item.Author.UID)
			}
			authorHandle = strings.TrimSpace(item.Author.UniqueID)
			threadCount = len(item.Thread)
			if item.TypeLabel != "" {
				typeLabel = item.TypeLabel
			} else if normSite == "douyin" && entry.Kind == "dynamic" {
				typeLabel = "图文"
			} else if normSite == "weibo" && entry.Kind == "dynamic" {
				if item.Via != nil && item.Via.Reason == library.ViaForward || item.Origin != nil {
					typeLabel = "转发微博"
				} else {
					typeLabel = "微博"
				}
			} else if normSite == "x" && entry.Kind == "dynamic" {
				if item.Origin != nil && strings.TrimSpace(item.Text) != "" {
					typeLabel = "引用帖子"
				} else if item.Via != nil && item.Via.Reason == library.ViaForward || item.Origin != nil {
					typeLabel = "转发帖子"
				} else {
					typeLabel = "帖子"
				}
			}
			if normSite == "x" {
				xCard = libraryRichCardToXDTO(item.Card)
				hlsOnlyNotice = xMissingHLSNotice(item.Missing)
			} else {
				weiboCard = libraryRichCardToWeiboDTO(item.Card)
			}
			var hasLocalVideo bool
			var localVideoDur int
			for _, m := range item.Media {
				if m.Role == "cover" && relCover == "" && !m.FromOrigin {
					relCover = m.Path
				} else if m.Role == "video" {
					hasLocalVideo = true
					if localVideoDur == 0 && m.Duration > 0 {
						localVideoDur = m.Duration
					}
				}
			}
			if relCover == "" {
				for _, m := range item.Media {
					if m.Role == "cover" && m.Path != "" {
						relCover = m.Path
						break
					}
				}
			}
			if len(item.NotePages) > 0 {
				if item.Origin != nil {
					notePages = weiboMediaToNotePagesScoped(nil, &item, false, 0, true)
					origNotePages = weiboMediaToNotePagesScoped(nil, &item, true, 0, true)
				} else {
					notePages = buildDouyinNoteRenderPages(nil, &item)
				}
			} else if item.Origin != nil {
				images, origImages = splitLibraryMediaImagesByOrigin(&item)
			} else {
				for _, m := range item.Media {
					if m.Role == "image" {
						images = append(images, PostImage{
							RelPath: m.Path,
							Width:   m.Width,
							Height:  m.Height,
							Live:    m.Motion != "",
							IsGIF:   m.GIF || strings.HasSuffix(strings.ToLower(m.Path), ".gif"),
						})
					}
				}
			}
			if len(item.Parts) > 0 {
				p0 := item.Parts[0]
				if p0.Duration > 0 {
					durationStr = FormatDuration(p0.Duration)
				}
				qualityTag = formatPartQualitySpec(p0, false)
			} else if item.DouyinVideo != nil {
				qualityTag = formatDouyinVideoSpec(item.DouyinVideo, "", "")
			} else if item.Live != nil {
				qualityTag = formatLiveRecordingSpec(item.Live, "")
			} else if hasLocalVideo {
				if localVideoDur > 0 {
					durationStr = FormatDuration(localVideoDur)
				} else if item.Duration > 0 {
					durationStr = FormatDuration(item.Duration)
				}
			}
		}
	}

	var videoCard *PostVideo
	var origVideo *PostVideo
	var origin *PostCardData
	originUnavailable := ""
	text := entry.Summary
	title := entry.Title
	videoBelongsToOrigin := itemVideoBelongsToOrigin(libPtr)

	if entry.Kind == "video" || entry.Kind == "live" {
		// A video's description belongs beside its cover, not above it.
		text = ""
		if durationStr == "" && libPtr != nil && libPtr.Duration > 0 {
			durationStr = FormatDuration(libPtr.Duration)
		}
		videoCard = &PostVideo{
			BVID:       entry.RawID,
			Title:      entry.Title,
			Desc:       entry.Summary,
			CoverURL:   entry.CoverURL,
			RelCover:   relCover,
			Duration:   durationStr,
			ItemKey:    entry.ItemKey,
			Cached:     entry.Cached,
			Status:     dot,
			QualityTag: qualityTag,
		}
	} else if (normSite == "weibo" || normSite == "x") && libPtr != nil && itemHasVideoRole(*libPtr) && videoBelongsToOrigin && !hasVideoNotePage(origNotePages) {
		_, origRelCover, origDur, _ := cachedItemHasVideoForScope(libPtr, true, false)
		origDurStr := durationStr
		if origDur > 0 {
			origDurStr = FormatDuration(origDur)
		} else if origDurStr == "" && libPtr.Duration > 0 {
			origDurStr = FormatDuration(libPtr.Duration)
		}
		origTitle := ""
		origID := entry.RawID
		if libPtr.Origin != nil {
			origTitle = libPtr.Origin.Title
			origID = firstNonEmptyStrUI(libPtr.Origin.ID, entry.RawID)
		}
		defaultVidTitle := "微博视频"
		if normSite == "x" {
			defaultVidTitle = "X 视频"
		}
		origVideo = &PostVideo{
			BVID:       origID,
			Title:      firstNonEmptyStrUI(origTitle, defaultVidTitle),
			Desc:       "",
			CoverURL:   "",
			RelCover:   origRelCover,
			Duration:   origDurStr,
			ItemKey:    entry.ItemKey,
			Cached:     entry.Cached,
			Status:     dot,
			QualityTag: qualityTag,
		}
	} else if (normSite == "weibo" || normSite == "x") && libPtr != nil && len(notePages) == 0 && len(images) == 0 && itemHasVideoRole(*libPtr) && !videoBelongsToOrigin {
		if durationStr == "" && libPtr.Duration > 0 {
			durationStr = FormatDuration(libPtr.Duration)
		}
		defaultVidTitle := "微博视频"
		if normSite == "x" {
			defaultVidTitle = "X 视频"
		}
		videoCard = &PostVideo{
			BVID:       entry.RawID,
			Title:      firstNonEmptyStrUI(libPtr.Title, entry.Title, defaultVidTitle),
			Desc:       "",
			CoverURL:   entry.CoverURL,
			RelCover:   relCover,
			Duration:   durationStr,
			ItemKey:    entry.ItemKey,
			Cached:     entry.Cached,
			Status:     dot,
			QualityTag: qualityTag,
		}
	} else if libPtr != nil && len(libPtr.Videos) > 0 {
		refKey := library.CanonicalItemKey(entry.Site, "video", libPtr.Videos[0])
		if refItem, ok, err := a.backend.GetLibraryItemMetaOnly(refKey); err == nil && ok {
			var refCover, refDur, refQTag string
			for _, m := range refItem.Media {
				if m.Role == "cover" && refCover == "" {
					refCover = m.Path
				}
			}
			if len(refItem.Parts) > 0 {
				p0 := refItem.Parts[0]
				if p0.Duration > 0 {
					refDur = FormatDuration(p0.Duration)
				}
				refQTag = formatPartQualitySpec(p0, false)
			}
			if refDur == "" && refItem.Duration > 0 {
				refDur = FormatDuration(refItem.Duration)
			}
			refDot := library.ComputeDotState(refItem).Dot
			videoCard = &PostVideo{
				BVID:       refItem.RawID,
				Title:      firstNonEmptyStrUI(refItem.Title, entry.Title),
				Desc:       refItem.Desc,
				RelCover:   refCover,
				Duration:   refDur,
				ItemKey:    refItem.ItemKey,
				Cached:     true,
				Status:     refDot,
				QualityTag: refQTag,
			}
		}
	}

	if (normSite == "weibo" || normSite == "x") && entry.Kind == "dynamic" && text != "" && title != "" {
		cleanTitle := strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(title), "…"), "..."))
		if cleanTitle != "" && strings.HasPrefix(strings.TrimSpace(text), cleanTitle) {
			title = ""
		}
	}
	if libPtr != nil && libPtr.Origin != nil {
		orig := libPtr.Origin
		if normSite == "weibo" || normSite == "x" {
			text = firstNonEmptyStrUI(libPtr.Text, entry.Summary)
			cleanTitle := strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(title), "…"), "..."))
			if title == text || (cleanTitle != "" && strings.HasPrefix(strings.TrimSpace(text), cleanTitle)) || (orig.Title != "" && title == orig.Title) {
				title = ""
			}
		}
		if orig.Unavailable {
			fallback := "原微博已删除或不可查看"
			if normSite == "x" {
				fallback = "原帖已删除或不可查看"
			}
			originUnavailable = firstNonEmptyStrUI(orig.UnavailableReason, fallback)
		} else {
			origSite := firstNonEmptyStrUI(orig.Site, entry.Site)
			origNorm := normalizeSiteIDUI(origSite)
			origType := ""
			switch {
			case origVideo != nil:
				origType = "视频"
			case len(origNotePages) > 0 || len(origImages) > 0:
				origType = "图文"
			case origNorm == "weibo":
				origType = "微博"
			case origNorm == "x":
				origType = "帖子"
			}
			var origWeiboCard *service.WeiboRichCardDTO
			var origXCard *service.XRichCardDTO
			if origNorm == "x" {
				origXCard = libraryRichCardToXDTO(orig.Card)
			} else {
				origWeiboCard = libraryRichCardToWeiboDTO(orig.Card)
			}
			defaultOrigAuthor := "原博主"
			if origNorm == "x" {
				defaultOrigAuthor = "原帖作者"
			}
			origin = &PostCardData{
				ItemKey:      entry.ItemKey,
				Site:         origSite,
				AuthorUID:    orig.Author.UID,
				AuthorName:   firstNonEmptyStrUI(orig.Author.Name, orig.Author.UniqueID, defaultOrigAuthor),
				AuthorHandle: orig.Author.UniqueID,
				AuthorFace:   "",
				PubTs:        orig.PubTs,
				TypeLabel:    origType,
				Title:        orig.Title,
				Text:         orig.Text,
				Links:        service.MapItemLinksToDTO(orig.Links),
				URL:          orig.CanonicalURL,
				WeiboMid:     orig.ID,
				XStatusID:    orig.ID,
				NotePages:    origNotePages,
				Images:       origImages,
				Video:        origVideo,
				WeiboCard:    origWeiboCard,
				XCard:        origXCard,
				Offline:      true,
			}
		}
	}

	var outerLinks []service.TextLinkDTO
	if libPtr != nil {
		outerLinks = service.MapItemLinksToDTO(libPtr.Links)
		if text == "" && entry.Kind == "dynamic" {
			text = libPtr.Text
		}
	}

	var threadParts []PostThreadPart
	if libPtr != nil && normSite == "x" && len(libPtr.Thread) > 0 {
		var focalVideo *PostVideo
		if !videoBelongsToOrigin {
			focalVideo = videoCard
		}
		if resolved, focalIdx, ok := resolveCachedXThreadParts(*libPtr, focalVideo, xCard, hlsOnlyNotice, entry.AuthorFace); ok {
			threadParts = resolved
			threadCount = len(resolved)
			if focalIdx >= 0 && focalIdx < len(threadParts) {
				// Media, video, rich card, and HLS notice are attached to their
				// respective thread nodes so renderPostCardThread never duplicates them.
				images = nil
				notePages = nil
				videoCard = nil
				xCard = nil
				hlsOnlyNotice = ""
			}
		} else {
			threadCount = 0
		}
	}

	return PostCardData{
		Key:               entry.FeedKey,
		ItemKey:           entry.ItemKey,
		Site:              entry.Site,
		Kind:              entry.Kind,
		RawID:             entry.RawID,
		Mid:               entry.Mid,
		AuthorUID:         authorUID,
		AuthorName:        entry.AuthorName,
		AuthorHandle:      authorHandle,
		AuthorFace:        entry.AuthorFace,
		PubTs:             entry.PubTs,
		TypeLabel:         typeLabel,
		Title:             title,
		Text:              text,
		Links:             outerLinks,
		URL:               entry.CanonicalURL,
		Cached:            entry.Cached,
		DotStatus:         dot,
		DotLabel:          "已缓存：" + entry.RawID,
		Images:            images,
		NotePages:         notePages,
		Video:             videoCard,
		WeiboCard:         weiboCard,
		WeiboMid:          entry.RawID,
		XCard:             xCard,
		XStatusID:         entry.RawID,
		HLSOnlyNotice:     hlsOnlyNotice,
		ThreadCount:       threadCount,
		ThreadParts:       threadParts,
		Origin:            origin,
		OriginUnavailable: originUnavailable,
		LibItem:           libPtr,
		InHomeFeed:        true,
	}
}

// dynamicItemToPostCard converts a bilibili.DynamicItem into a PostCardData for /user and /subscriptions.
func (a *App) dynamicItemToPostCard(d bilibili.DynamicItem, openLabel, cacheLabel string) PostCardData {
	return a.cachedPostCard("dyn:"+d.ID+"|"+openLabel+"|"+cacheLabel, func() PostCardData {
		return a.buildDynamicPostCard(d, openLabel, cacheLabel)
	})
}

func (a *App) buildDynamicPostCard(d bilibili.DynamicItem, openLabel, cacheLabel string) PostCardData {
	typeLabel := d.TypeLabel
	if typeLabel == "" {
		typeLabel = d.Type
	}
	var imgs []PostImage
	for _, im := range d.Images {
		imgs = append(imgs, PostImage{
			URL:     im.URL,
			Width:   im.Width,
			Height:  im.Height,
			Live:    im.LiveURL != "",
			LiveURL: im.LiveURL,
		})
	}
	var vid *PostVideo
	if len(d.Videos) > 0 {
		v0 := d.Videos[0]
		dur := v0.DurationText
		if dur == "" && v0.DurationSec > 0 {
			dur = FormatDuration(v0.DurationSec)
		}
		vid = &PostVideo{
			BVID:     v0.BVID,
			Title:    v0.Title,
			Desc:     v0.Desc,
			CoverURL: v0.Cover,
			Duration: dur,
		}
		if v0.BVID != "" {
			vKey := library.CanonicalItemKey("bilibili", "video", v0.BVID)
			if vItem, ok, err := a.backend.GetLibraryItemMetaOnly(vKey); err == nil && ok {
				vid.ItemKey = vItem.ItemKey
				vid.Cached = true
				vid.Status = library.ComputeDotState(vItem).Dot
				for _, m := range vItem.Media {
					if m.Role == "cover" && vid.RelCover == "" {
						vid.RelCover = m.Path
					}
				}
			}
		}
	}
	var orig *PostCardData
	if d.Origin != nil {
		o := a.buildDynamicPostCard(*d.Origin, "", "")
		// Bilibili's dynamic parser hoists origin images/videos onto the outer
		// DynamicItem; avoid rendering them twice inside the origin quote box.
		if len(imgs) > 0 && len(o.Images) > 0 {
			o.Images = nil
		}
		if vid != nil && o.Video != nil && vid.BVID == o.Video.BVID {
			o.Video = nil
		}
		orig = &o
	}

	dotStatus := ""
	itemKey := library.CanonicalItemKey("bilibili", "dynamic", d.ID)
	var hasItem bool
	var dynLinks []service.TextLinkDTO
	if it, ok, err := a.backend.GetLibraryItemMetaOnly(itemKey); err == nil && ok {
		hasItem = true
		dotStatus = library.ComputeDotState(it).Dot
		dynLinks = service.MapItemLinksToDTO(it.Links)
	}
	if st, ok := a.backend.States([]string{itemKey})[itemKey]; ok && st.Dot != "" && st.Dot != "none" {
		dotStatus = st.Dot
	}

	dCopy := d
	return PostCardData{
		Key:           "dyn:" + d.ID,
		ItemKey:       itemKey,
		Site:          "bilibili",
		Kind:          "dynamic",
		RawID:         d.ID,
		Mid:           d.Author.Mid,
		AuthorName:    d.Author.Name,
		AuthorFace:    d.Author.Face,
		PubTs:         d.PubTs,
		TypeLabel:     typeLabel,
		Title:         d.Title,
		Text:          d.Text,
		Links:         dynLinks,
		URL:           d.CanonicalURL,
		Pinned:        d.Pinned,
		IsOnlyFans:    d.IsOnlyFans,
		Blocked:       d.Blocked,
		BlockedHint:   d.BlockedHint,
		Degraded:      d.Degraded,
		Cached:        hasItem || (dotStatus != "" && dotStatus != "none"),
		DotStatus:     dotStatus,
		Images:        imgs,
		Video:         vid,
		Origin:        orig,
		DynItem:       &dCopy,
		OpenBtnLabel:  openLabel,
		CacheBtnLabel: cacheLabel,
	}
}
