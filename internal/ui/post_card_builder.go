package ui

import (
	"strings"

	"github.com/chialecode/trace/internal/library"
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
	var notePages []douyinNoteRenderPage
	authorUID := strings.TrimSpace(entry.AuthorUID)

	if entry.Cached && entry.ItemKey != "" {
		if item, ok, err := a.backend.GetLibraryItem(entry.ItemKey); err == nil && ok {
			libPtr = &item
			if authorUID == "" {
				authorUID = strings.TrimSpace(item.Author.UID)
			}
			if item.TypeLabel != "" {
				typeLabel = item.TypeLabel
			} else if normalizeSiteIDUI(entry.Site) == "douyin" && entry.Kind == "dynamic" {
				typeLabel = "图文"
			}
			for _, m := range item.Media {
				if m.Role == "cover" && relCover == "" {
					relCover = m.Path
				} else if m.Role == "image" {
					images = append(images, PostImage{
						RelPath: m.Path,
						Width:   m.Width,
						Height:  m.Height,
						Live:    m.Motion != "",
					})
				}
			}
			if len(item.NotePages) > 0 {
				notePages = buildDouyinNoteRenderPages(nil, &item)
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
			}
		}
	}

	var videoCard *PostVideo
	text := entry.Summary
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
	} else if libPtr != nil && len(libPtr.Videos) > 0 {
		refKey := library.CanonicalItemKey(entry.Site, "video", libPtr.Videos[0])
		if refItem, ok, err := a.backend.GetLibraryItem(refKey); err == nil && ok {
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

	return PostCardData{
		Key:        entry.FeedKey,
		ItemKey:    entry.ItemKey,
		Site:       entry.Site,
		Kind:       entry.Kind,
		RawID:      entry.RawID,
		Mid:        entry.Mid,
		AuthorUID:  authorUID,
		AuthorName: entry.AuthorName,
		AuthorFace: entry.AuthorFace,
		PubTs:      entry.PubTs,
		TypeLabel:  typeLabel,
		Title:      entry.Title,
		Text:       text,
		URL:        entry.CanonicalURL,
		Cached:     entry.Cached,
		DotStatus:  dot,
		DotLabel:   "已缓存：" + entry.RawID,
		Images:     images,
		NotePages:  notePages,
		Video:      videoCard,
		LibItem:    libPtr,
		InHomeFeed: true,
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
			if vItem, ok, err := a.backend.GetLibraryItem(vKey); err == nil && ok {
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
		orig = &o
	}

	dotStatus := ""
	itemKey := library.CanonicalItemKey("bilibili", "dynamic", d.ID)
	var hasItem bool
	if it, ok, err := a.backend.GetLibraryItem(itemKey); err == nil && ok {
		hasItem = true
		dotStatus = library.ComputeDotState(it).Dot
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
		URL:           d.CanonicalURL,
		Pinned:        d.Pinned,
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
