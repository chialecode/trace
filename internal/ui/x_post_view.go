package ui

import (
	"context"
	"fmt"
	"strings"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/egoist/mygo/ui"
)

func (a *App) cacheXPostNow(statusID string, via *library.ItemVia) {
	statusID = strings.TrimSpace(statusID)
	if statusID == "" {
		return
	}
	backend := a.backend
	var viaCopy *library.ItemVia
	if via != nil {
		cp := *via
		viaCopy = &cp
	}
	requestGuarded(a, "cache-x-post:"+statusID, nil, func(ctx context.Context) (struct{}, error) {
		_, err := backend.CacheXPost(ctx, statusID, viaCopy)
		return struct{}{}, err
	}, func(struct{}) {
		a.BannerInfo = "已加入缓存队列：" + statusID
		a.RefreshData()
	}, func(err error) {
		a.showError(err)
		a.RefreshData()
	})
}

func libraryRichCardToXDTO(card *library.ItemRichCard) *service.XRichCardDTO {
	if card == nil {
		return nil
	}
	if strings.TrimSpace(card.Kind) == "" && strings.TrimSpace(card.Label) == "" &&
		strings.TrimSpace(card.Title) == "" && strings.TrimSpace(card.Desc) == "" &&
		strings.TrimSpace(card.URL) == "" {
		return nil
	}
	return &service.XRichCardDTO{
		Kind:  card.Kind,
		Label: card.Label,
		Title: card.Title,
		Desc:  card.Desc,
		URL:   card.URL,
	}
}

func xMissingHLSNotice(missing []library.MissingRecord) string {
	for _, m := range missing {
		r := strings.TrimSpace(m.Reason)
		if strings.Contains(r, "分段流") || strings.Contains(strings.ToLower(r), "hls") || strings.Contains(strings.ToLower(m.Item), "hls") {
			return firstNonEmptyStrUI(r, "这条视频只有分段流，暂不支持缓存")
		}
	}
	return ""
}

func xPostHLSOnlyNotice(post *service.XPostDTO) string {
	if post == nil {
		return ""
	}
	for _, m := range post.Media {
		if m.HLSOnly {
			return firstNonEmptyStrUI(m.HLSOnlyNotice, "这条视频只有分段流，暂不支持缓存")
		}
	}
	if post.HasHLSOnlyVideo {
		return "这条视频只有分段流，暂不支持缓存"
	}
	return ""
}

func xMediaHasDownloadable(media []service.XMediaItemDTO) bool {
	for _, m := range media {
		if m.HLSOnly {
			continue
		}
		switch m.Kind {
		case "photo":
			if strings.TrimSpace(m.ImageURL) != "" {
				return true
			}
		case "gif", "video":
			if strings.TrimSpace(m.VideoURL) != "" {
				return true
			}
		}
	}
	return false
}

// xPostSuppressCacheButton returns true when the post has ONLY an unsupported
// live/Spaces card or HLS-only video(s) with no downloadable media on the post
// or its retweet/quote origin, so we never show a broken cache button.
func xPostSuppressCacheButton(post *service.XPostDTO) bool {
	if post == nil {
		return true
	}
	if xMediaHasDownloadable(post.Media) {
		return false
	}
	if post.Retweeted != nil && !post.Retweeted.Unavailable && xMediaHasDownloadable(post.Retweeted.Media) {
		return false
	}
	if post.Quoted != nil && !post.Quoted.Unavailable && xMediaHasDownloadable(post.Quoted.Media) {
		return false
	}
	if xPostHLSOnlyNotice(post) != "" {
		return true
	}
	if post.Retweeted != nil && !post.Retweeted.Unavailable && xPostHLSOnlyNotice(post.Retweeted) != "" && strings.TrimSpace(post.Text) == "" {
		return true
	}
	if post.Card != nil && (post.Card.Kind == "live" || post.Card.Kind == "article" || post.Card.Kind == "vote") {
		return true
	}
	if post.Poll != nil && strings.TrimSpace(post.Text) == "" {
		return true
	}
	return false
}

func xCardKindLabel(card *service.XRichCardDTO) string {
	if card == nil {
		return ""
	}
	if l := strings.TrimSpace(card.Label); l != "" {
		return l
	}
	switch card.Kind {
	case "article":
		return "X Articles"
	case "vote":
		return "投票"
	case "live":
		return "直播 / Spaces"
	case "webpage":
		return "链接卡片"
	default:
		return "卡片"
	}
}

func xMediaToNotePagesScoped(media []service.XMediaItemDTO, cachedItem *library.ItemMetadata, fromOrigin bool, offline bool) []douyinNoteRenderPage {
	var localPages []library.NotePageRecord
	if cachedItem != nil && len(cachedItem.NotePages) > 0 {
		useFlags := hasAnyFromOriginFlag(cachedItem)
		for _, rec := range cachedItem.NotePages {
			if useFlags {
				if rec.FromOrigin != fromOrigin {
					continue
				}
			} else if fromOrigin {
				continue
			}
			localPages = append(localPages, rec)
		}
	}

	if offline && len(localPages) > 0 {
		out := make([]douyinNoteRenderPage, 0, len(localPages))
		for _, rec := range localPages {
			kind := rec.Kind
			if kind == "" {
				if rec.Video != "" {
					kind = "video"
				} else {
					kind = "image"
				}
			}
			out = append(out, douyinNoteRenderPage{
				Index:       rec.Index,
				Kind:        kind,
				IsGIF:       rec.GIF,
				ImageRel:    firstNonEmptyStrUI(rec.Image, rec.Cover),
				VideoRel:    rec.Video,
				Width:       rec.Width,
				Height:      rec.Height,
				DurationSec: rec.Duration,
			})
		}
		return out
	}

	if len(media) == 0 {
		return nil
	}

	out := make([]douyinNoteRenderPage, 0, len(media))
	for i, m := range media {
		isGIF := m.GIF || m.Kind == "gif"
		kind := "image"
		switch {
		case m.Kind == "video" || isGIF:
			if m.HLSOnly && strings.TrimSpace(m.VideoURL) == "" {
				kind = "video"
			} else {
				kind = "video"
			}
		default:
			kind = "image"
		}
		pg := douyinNoteRenderPage{
			Index:         i + 1,
			Kind:          kind,
			IsGIF:         isGIF,
			HLSOnly:       m.HLSOnly,
			HLSOnlyNotice: m.HLSOnlyNotice,
			Width:         m.Width,
			Height:        m.Height,
			DurationSec:   m.DurationSec,
		}
		if !offline {
			pg.ImageURL = firstNonEmptyStrUI(m.PublicPreviewURL, m.ImageURL)
			if !m.HLSOnly {
				pg.VideoURL = m.VideoURL
			}
		}
		if i < len(localPages) {
			rec := localPages[i]
			pg.ImageRel = firstNonEmptyStrUI(rec.Image, rec.Cover)
			pg.VideoRel = rec.Video
			if rec.GIF {
				pg.IsGIF = true
			}
			if pg.Width <= 0 {
				pg.Width = rec.Width
			}
			if pg.Height <= 0 {
				pg.Height = rec.Height
			}
			if pg.DurationSec <= 0 {
				pg.DurationSec = rec.Duration
			}
		}
		out = append(out, pg)
	}
	return out
}

func formatXVideoSpec(m service.XMediaItemDTO) string {
	var parts []string
	if m.GIF || m.Kind == "gif" {
		parts = append(parts, "GIF（静音循环）")
	}
	if q := strings.TrimSpace(m.QualityLabel); q != "" {
		parts = append(parts, q)
	} else if m.Width > 0 && m.Height > 0 {
		parts = append(parts, fmt.Sprintf("%dx%d", m.Width, m.Height))
	}
	if m.VideoBitrate > 0 {
		parts = append(parts, fmt.Sprintf("%d kbps", m.VideoBitrate/1000))
	}
	return strings.Join(parts, " · ")
}

func formatXVisibilityNote(post *service.XPostDTO) string {
	if post == nil {
		return ""
	}
	var parts []string
	if s := strings.TrimSpace(post.VisibilityLabel); s != "" {
		parts = append(parts, s)
	}
	if post.PossiblySensitive {
		parts = append(parts, "可能包含敏感内容")
	}
	if len(post.WithheldInCountries) > 0 {
		parts = append(parts, "在部分地区受限："+strings.Join(post.WithheldInCountries, ", "))
	}
	return strings.Join(parts, " · ")
}

func (a *App) renderXHLSOnlyNotice(c *ui.Context, noticeText, postURL string, offline bool) {
	msg := firstNonEmptyStrUI(noticeText, "这条视频只有分段流，暂不支持缓存")
	ui.Row(c).
		Label("X分段流提示").
		FillWidth().
		Padding(10, 12).
		Radius(10).
		Background(ColorWarnBg).
		Border(1, ColorDotYellow).
		AlignItems(ui.Center).
		Justify(ui.SpaceBetween).
		Gap(8).
		ClipX().
		Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(6).Grow(1).ClipX().Children(func() {
				DrawIcon(c, "alert-circle", 15, ColorDotYellow, 1.8)
				ui.Text(c, msg).FontSize(12).LineHeight(LineHeightCompact).TextColor(ColorTextMain).MaxLines(2).Grow(1)
			})
			if !offline && strings.TrimSpace(postURL) != "" {
				if OutlinePillButton(c, "在浏览器中打开", "X分段流浏览器打开").Clicked() {
					if err := a.backend.Desktop().OpenExternalURL(postURL); err != nil {
						a.showError(err)
					}
				}
			}
		})
}

func (a *App) renderXPollCard(c *ui.Context, poll *service.XPollSummaryDTO, fallbackURL string, offline bool) {
	if poll == nil {
		return
	}
	ui.Column(c).
		Label("X投票卡片").
		FillWidth().
		Padding(12, 14).
		Radius(12).
		Background(ColorBgSecondary).
		Border(1, ColorBorder).
		Gap(8).
		ClipX().
		Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
					ui.Box(c).Padding(1, 6).Radius(4).Background(ColorBgMain).Border(1, ColorBorder).Children(func() {
						ui.Text(c, "投票").FontSize(11).FontWeight(600).TextColor(ColorTextMuted).SingleLine()
					})
					status := "进行中"
					if poll.Final {
						status = "已结束"
					}
					meta := fmt.Sprintf("%s · 共 %s 票", status, FormatCount(poll.TotalVotes))
					ui.Text(c, meta).FontSize(12).TextColor(ColorTextMuted).SingleLine()
				})
				if !offline && strings.TrimSpace(fallbackURL) != "" {
					if OutlinePillButton(c, "在浏览器中打开", "X投票浏览器打开").Clicked() {
						if err := a.backend.Desktop().OpenExternalURL(fallbackURL); err != nil {
							a.showError(err)
						}
					}
				}
			})
			for _, ch := range poll.Options {
				pct := 0
				if poll.TotalVotes > 0 {
					pct = int(float64(ch.Votes)*100.0/float64(poll.TotalVotes) + 0.5)
				}
				ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Padding(4, 8).Radius(8).Background(ColorBgMain).Children(func() {
					ui.Text(c, ch.Label).FontSize(13).TextColor(ColorTextMain).SingleLine().Grow(1).Shrink(1)
					ui.Text(c, fmt.Sprintf("%d%% (%s)", pct, FormatCount(ch.Votes))).FontSize(12).TextColor(ColorTextMuted).SingleLine().Shrink(0)
				})
			}
			if poll.EndTime != "" {
				ui.Text(c, "截止时间："+poll.EndTime).FontSize(11).TextColor(ColorTextMuted).SingleLine()
			}
		})
}

// renderXRichCard renders a degraded summary card for X Articles, link cards,
// votes, or live/Spaces cards with "在浏览器中打开" and NO video cache button.
func (a *App) renderXRichCard(c *ui.Context, card *service.XRichCardDTO, fallbackURL string, offline bool) {
	if card == nil {
		return
	}
	badge := xCardKindLabel(card)
	targetURL := firstNonEmptyStrUI(card.URL, fallbackURL)
	ui.Column(c).
		Label("X卡片："+card.Kind).
		FillWidth().
		Padding(12, 14).
		Radius(12).
		Background(ColorBgSecondary).
		Border(1, ColorBorder).
		Gap(6).
		ClipX().
		Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
				ui.Box(c).Padding(1, 6).Radius(4).Background(ColorBgMain).Border(1, ColorBorder).Children(func() {
					ui.Text(c, badge).FontSize(11).FontWeight(600).TextColor(ColorTextMuted).SingleLine()
				})
				if !offline && strings.TrimSpace(targetURL) != "" {
					if OutlinePillButton(c, "在浏览器中打开", "X卡片浏览器打开："+card.Kind).Clicked() {
						if err := a.backend.Desktop().OpenExternalURL(targetURL); err != nil {
							a.showError(err)
						}
					}
				}
			})
			if strings.TrimSpace(card.Title) != "" {
				ui.Text(c, card.Title).FontSize(14).FontWeight(600).TextColor(ColorTextMain).MaxLines(2)
			}
			if strings.TrimSpace(card.Desc) != "" {
				ui.Text(c, card.Desc).FontSize(12).LineHeight(LineHeightCompact).TextColor(ColorTextMuted).MaxLines(3)
			}
			if card.Kind == "live" {
				ui.Text(c, "X 直播与 Spaces 暂不支持录制，可以在浏览器中打开。").FontSize(12).TextColor(ColorTextMuted).MaxLines(2)
			}
		})
}

func (a *App) xPostToPostCard(p service.XPostDTO, pageUser *service.XUserDTO) PostCardData {
	itemKey := p.CacheKey
	if itemKey == "" && p.ID != "" {
		itemKey = library.CanonicalItemKey("x", "dynamic", p.ID)
	}
	dot := p.CacheState.Dot
	cached := p.CachedItem != nil
	cachedItem := p.CachedItem
	if a.backend != nil && itemKey != "" {
		if stMap := a.backend.States([]string{itemKey}); stMap != nil {
			if st, ok := stMap[itemKey]; ok && st.Dot != "" {
				dot = st.Dot
				if st.Dot == "green" || st.Dot == "yellow" {
					cached = true
				}
			}
		}
		if cachedItem == nil {
			if it, ok, err := a.backend.GetLibraryItemMetaOnly(itemKey); err == nil && ok {
				cachedItem = &it
				cached = true
				if dot == "" {
					dot = library.ComputeDotState(it).Dot
				}
			}
		}
	}

	authorUID := strings.TrimSpace(p.Author.RestID)
	authorHandle := strings.TrimPrefix(strings.TrimSpace(p.Author.ScreenName), "@")
	authorName := firstNonEmptyStrUI(p.Author.Name, authorHandle)
	authorFace := p.Author.AvatarURL
	if authorUID == "" && pageUser != nil {
		authorUID = strings.TrimSpace(pageUser.RestID)
		if authorHandle == "" {
			authorHandle = strings.TrimPrefix(strings.TrimSpace(pageUser.ScreenName), "@")
		}
		if authorName == "" {
			authorName = firstNonEmptyStrUI(pageUser.Name, authorHandle)
		}
		if authorFace == "" {
			authorFace = pageUser.AvatarURL
		}
	}

	typeLabel := "帖子"
	if p.IsRetweet || p.Retweeted != nil {
		typeLabel = "转发帖子"
	} else if p.IsQuote || p.Quoted != nil {
		typeLabel = "引用帖子"
	}

	var notePages []douyinNoteRenderPage
	var videoCard *PostVideo
	if len(p.Media) == 1 && (p.Media[0].Kind == "video" || p.Media[0].Kind == "gif" || p.Media[0].GIF) && !p.Media[0].HLSOnly {
		m0 := p.Media[0]
		_, relCover, localDur, _ := cachedItemHasVideoForScope(cachedItem, false, false)
		durSec := m0.DurationSec
		if durSec <= 0 {
			durSec = localDur
		}
		vidTitle := "X 视频"
		if m0.GIF || m0.Kind == "gif" {
			vidTitle = "GIF 动图"
		}
		videoCard = &PostVideo{
			BVID:       p.ID,
			Title:      vidTitle,
			CoverURL:   firstNonEmptyStrUI(m0.PublicPreviewURL, m0.ImageURL),
			RelCover:   relCover,
			Duration:   FormatDuration(durSec),
			ItemKey:    itemKey,
			Cached:     cachedItem != nil && itemHasVideoRole(*cachedItem),
			Status:     dot,
			QualityTag: formatXVideoSpec(m0),
		}
	} else if len(p.Media) > 0 {
		notePages = xMediaToNotePagesScoped(p.Media, cachedItem, false, false)
	}

	var originCard *PostCardData
	originUnavailable := ""
	origDTO := p.Retweeted
	if origDTO == nil {
		origDTO = p.Quoted
	}
	if origDTO != nil {
		if origDTO.Unavailable {
			originUnavailable = firstNonEmptyStrUI(origDTO.UnavailableReason, "原帖已删除或不可查看")
		} else {
			origPages := xMediaToNotePagesScoped(origDTO.Media, cachedItem, true, false)
			var origVid *PostVideo
			if len(origDTO.Media) == 1 && (origDTO.Media[0].Kind == "video" || origDTO.Media[0].Kind == "gif" || origDTO.Media[0].GIF) && !origDTO.Media[0].HLSOnly {
				m0 := origDTO.Media[0]
				_, relCover, localDur, _ := cachedItemHasVideoForScope(cachedItem, true, false)
				durSec := m0.DurationSec
				if durSec <= 0 {
					durSec = localDur
				}
				vidTitle := "X 视频"
				if m0.GIF || m0.Kind == "gif" {
					vidTitle = "GIF 动图"
				}
				origVid = &PostVideo{
					BVID:       origDTO.ID,
					Title:      vidTitle,
					CoverURL:   firstNonEmptyStrUI(m0.PublicPreviewURL, m0.ImageURL),
					RelCover:   relCover,
					Duration:   FormatDuration(durSec),
					ItemKey:    itemKey,
					Cached:     cachedItem != nil && itemHasVideoRole(*cachedItem),
					QualityTag: formatXVideoSpec(m0),
				}
				origPages = nil
			}
			originCard = &PostCardData{
				ItemKey:       itemKey,
				Site:          "x",
				AuthorUID:     origDTO.Author.RestID,
				AuthorName:    firstNonEmptyStrUI(origDTO.Author.Name, origDTO.Author.ScreenName, "原帖作者"),
				AuthorHandle:  origDTO.Author.ScreenName,
				AuthorFace:    origDTO.Author.AvatarURL,
				PubTs:         origDTO.CreatedAt,
				Text:          origDTO.Text,
				Links:         xPostLinksUI(origDTO, cachedItem, true),
				URL:           origDTO.CanonicalURL,
				XStatusID:     origDTO.ID,
				NotePages:     origPages,
				Video:         origVid,
				XCard:         origDTO.Card,
				XPoll:         origDTO.Poll,
				HLSOnlyNotice: xPostHLSOnlyNotice(origDTO),
			}
		}
	} else if p.OriginUnavailable {
		originUnavailable = firstNonEmptyStrUI(p.OriginUnavailableReason, "原帖已删除或不可查看")
	}

	var via *library.ItemVia
	if pageUser != nil {
		via = xCacheViaForContext(&p, pageUser.RestID, firstNonEmptyStrUI(pageUser.Name, pageUser.ScreenName))
	} else {
		via = xCacheViaForContext(&p, "", "")
	}

	postCopy := p
	pageUserCopy := pageUser
	openPost := func() {
		if pageUserCopy != nil {
			a.OpenXPostFor(postCopy.ID, postCopy.CanonicalURL, clampRunes(postCopy.Text, 40), pageUserCopy.RestID, firstNonEmptyStrUI(pageUserCopy.Name, pageUserCopy.ScreenName))
		} else {
			a.OpenXPost(postCopy.ID, clampRunes(postCopy.Text, 40), authorName, authorUID, "", postCopy.CanonicalURL)
		}
	}

	cacheBtnLabel := "缓存帖子：" + p.ID
	if xPostSuppressCacheButton(&p) {
		cacheBtnLabel = ""
	}

	return PostCardData{
		Key:               "x:" + p.ID,
		ItemKey:           itemKey,
		Site:              "x",
		Kind:              "dynamic",
		RawID:             p.ID,
		AuthorUID:         authorUID,
		AuthorName:        authorName,
		AuthorHandle:      authorHandle,
		AuthorFace:        authorFace,
		PubTs:             p.CreatedAt,
		TypeLabel:         typeLabel,
		Text:              p.Text,
		Links:             xPostLinksUI(&p, cachedItem, false),
		URL:               firstNonEmptyStrUI(p.CanonicalURL, "https://x.com/i/web/status/"+p.ID),
		Pinned:            p.Pinned,
		IsEdited:          p.IsEdited,
		VisibleLabel:      formatXVisibilityNote(&p),
		Cached:            cached,
		DotStatus:         dot,
		DotLabel:          "X缓存状态：" + p.ID,
		NotePages:         notePages,
		Video:             videoCard,
		XCard:             p.Card,
		XPoll:             p.Poll,
		XStatusID:         p.ID,
		XCacheVia:         via,
		HLSOnlyNotice:     xPostHLSOnlyNotice(&p),
		ThreadCount:       len(p.Thread),
		Origin:            originCard,
		OriginUnavailable: originUnavailable,
		LibItem:           cachedItem,
		OnOpenPost:        openPost,
		OpenBtnLabel:      "打开X帖子：" + p.ID,
		CacheBtnLabel:     cacheBtnLabel,
	}
}

// renderXPostRoute draws /x/post/:id (online detail) OR /library/item (offline detail).
func (a *App) renderXPostRoute(c *ui.Context, viewportHeight float32) {
	if a.Opening != nil {
		a.renderOpening(c)
		return
	}
	if a.CurrentLibraryDetail != nil && normalizeSiteIDUI(a.CurrentLibraryDetail.Site) == "x" {
		a.renderXOfflineDetail(c, *a.CurrentLibraryDetail)
		return
	}
	if a.CurrentXPost != nil {
		a.renderXPostDetail(c, a.CurrentXPost)
		return
	}
	ui.Column(c).Grow(1).FillWidth().Padding(32, 16).AlignItems(ui.Center).Gap(8).Children(func() {
		ui.Text(c, "未加载 X 帖子内容").FontSize(15).FontWeight(600).TextColor(ColorTextMain)
		ui.Text(c, "可在解析页粘贴 x.com / twitter.com 帖子链接打开。").FontSize(13).TextColor(ColorTextMuted)
	})
}

func (a *App) renderXPostDetail(c *ui.Context, post *service.XPostDTO) {
	itemKey := post.CacheKey
	if itemKey == "" && post.ID != "" {
		itemKey = library.CanonicalItemKey("x", "dynamic", post.ID)
	}
	dot := post.CacheState.Dot
	cachedItem := post.CachedItem
	if a.backend != nil && itemKey != "" {
		if stMap := a.backend.States([]string{itemKey}); stMap != nil {
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

	authorUID := strings.TrimSpace(post.Author.RestID)
	authorHandle := strings.TrimPrefix(strings.TrimSpace(post.Author.ScreenName), "@")
	authorName := firstNonEmptyStrUI(post.Author.Name, authorHandle, "未知用户")
	openAuthor := func() {
		if target := firstNonEmptyStrUI(authorUID, authorHandle); target != "" {
			a.OpenXUser(target, authorName)
		}
	}
	suppressCache := xPostSuppressCacheButton(post)
	targetURL := firstNonEmptyStrUI(post.CanonicalURL, "https://x.com/i/web/status/"+post.ID)

	ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
		ui.Column(c).FillWidth().Padding(16).Gap(14).ClipX().Children(func() {
			// 0. Thread Ancestors (if viewing a reply inside a thread)
			if len(post.ThreadAncestors) > 0 {
				ui.Column(c).Label("X串上下文").FillWidth().Padding(10, 12).Radius(12).Background(ColorBgSecondary).Border(1, ColorBorder).Gap(8).ClipX().Children(func() {
					ui.Text(c, fmt.Sprintf("上下文（%d 条上文）", len(post.ThreadAncestors))).FontSize(12).FontWeight(600).TextColor(ColorTextMuted).SingleLine()
					for _, anc := range post.ThreadAncestors {
						aItem := anc
						ui.Column(c).FillWidth().Gap(4).Padding(0, 0, 6, 0).BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).Children(func() {
							ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
								ui.Text(c, firstNonEmptyStrUI(aItem.Author.Name, aItem.Author.ScreenName, "作者")).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
								if h := strings.TrimPrefix(strings.TrimSpace(aItem.Author.ScreenName), "@"); h != "" {
									ui.Text(c, "@"+h).FontSize(12).TextColor(ColorTextMuted).SingleLine()
								}
								if aItem.CreatedAt > 0 {
									ui.Text(c, "· "+FormatRelativeTime(aItem.CreatedAt)).FontSize(12).TextColor(ColorTextMuted).SingleLine()
								}
							})
							if strings.TrimSpace(aItem.Text) != "" {
								a.renderRichText(c, aItem.Text, xPostLinksUI(&aItem, nil, false), richTextOptions{
									MaxLines:   4,
									FontSize:   13,
									LineHeight: LineHeightBody,
									TextColor:  ColorTextMain,
								})
							}
						})
					}
				})
			}

			// 1. Author & Primary Action Header
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(10).ClipX().Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(10).Grow(1).ClipX().Children(func() {
					a.renderAccountAvatar(c, "x", authorName, authorUID, 0, 44, false, "打开X用户："+authorUID, openAuthor, post.Author.AvatarURL)
					ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
						ui.Row(c).AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
							btn := ui.ButtonBase(c).Label("X详情作者：" + firstNonEmptyStrUI(authorUID, authorHandle)).Padding(0).Children(func() {
								ui.Text(c, authorName).FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
							})
							if btn.Clicked() {
								openAuthor()
							}
							if authorHandle != "" {
								handleBtn := ui.ButtonBase(c).Label("X详情用户名：" + authorHandle).Padding(0).Shrink(1).Children(func() {
									ui.Text(c, "@"+authorHandle).FontSize(13).TextColor(ColorTextMuted).SingleLine()
								})
								if handleBtn.Clicked() {
									openAuthor()
								}
							}
							renderSiteSmallBadgeWithDot(c, "x", "X详情站点标记")
							if post.Pinned {
								ui.Box(c).Padding(1, 6).Radius(4).Background(ColorAccentBg).Children(func() {
									ui.Text(c, "置顶").FontSize(11).FontWeight(600).TextColor(ColorAccent).SingleLine()
								})
							}
							if post.IsEdited {
								ui.Box(c).Label("X已编辑标记").Padding(1, 6).Radius(4).Background(ColorBgSecondary).Children(func() {
									ui.Text(c, "已编辑").FontSize(11).TextColor(ColorTextMuted).SingleLine()
								})
							}
						})
						if post.CreatedAt > 0 {
							ui.Text(c, FormatTimestamp(post.CreatedAt)).FontSize(12).TextColor(ColorTextMuted).SingleLine()
						}
					})
				})

				ui.Row(c).Shrink(0).AlignItems(ui.Center).Gap(8).Children(func() {
					if dot != "" && dot != "none" {
						DrawStatusDot(c, dot, "X缓存状态："+post.ID)
					}
					if !suppressCache && post.ID != "" {
						cacheLabel := "缓存帖子"
						if len(post.Media) == 1 && (post.Media[0].Kind == "video" || post.Media[0].Kind == "gif") && !post.Media[0].HLSOnly {
							cacheLabel = "缓存视频"
							if post.Media[0].GIF || post.Media[0].Kind == "gif" {
								cacheLabel = "缓存动图"
							}
						} else if len(post.Media) > 0 && xMediaHasDownloadable(post.Media) {
							cacheLabel = "缓存图文"
						}
						if dot == "green" {
							cacheLabel = "重新缓存"
						}
						busy := a.isPending("cache-x-post:" + post.ID)
						if AccentPillButton(c, cacheLabel, "缓存当前X帖子").Disabled(busy).Clicked() && !busy {
							a.cacheXPostNow(post.ID, a.xCacheViaFor(post))
						}
					}

					menuKey := "x-detail:" + post.ID
					open := a.PostMenuKey == menuKey
					MoreMenuButton(c, "X详情更多", 32, &open, func(m *ui.Menu) {
						if !suppressCache && post.ID != "" && dot != "green" {
							if m.Item("缓存").Chosen() {
								a.cacheXPostNow(post.ID, a.xCacheViaFor(post))
							}
						}
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
							if m.Item("打开文件夹").Chosen() {
								a.showError(a.backend.OpenItemFolder(itemKey))
							}
						}
					})
					keyedMenuOpen(&a.PostMenuKey, menuKey, open)
				})
			})

			// 2. Visibility / Sensitive / Withheld Note
			if vis := formatXVisibilityNote(post); vis != "" {
				ui.Row(c).
					Label("X可见性说明").
					FillWidth().
					Padding(8, 12).
					Radius(8).
					Background(ColorBgSecondary).
					AlignItems(ui.Center).
					Gap(6).
					Children(func() {
						DrawIcon(c, "eye", 15, ColorTextMuted, 1.7)
						ui.Text(c, vis).FontSize(12).TextColor(ColorTextMuted).MaxLines(2)
					})
			}

			// 3. Full Body Text (note_tweet rendered completely)
			if strings.TrimSpace(post.Text) != "" {
				a.renderRichText(c, post.Text, xPostLinksUI(post, cachedItem, false), richTextOptions{
					Label:      "X帖子正文",
					FontSize:   15,
					LineHeight: LineHeightBody,
					TextColor:  ColorTextMain,
				})
			}

			var xCache previewCacheAction
			if !suppressCache && post.ID != "" {
				postIDCopy := post.ID
				viaCopy := a.xCacheViaFor(post)
				xCache = previewCacheAction{
					ItemKey:    itemKey,
					PendingKey: "cache-x-post:" + postIDCopy,
					OnCache:    func() { a.cacheXPostNow(postIDCopy, viaCopy) },
				}
			}

			// 4. Up to 4 Mixed Media Items (original photos, GIF looping muted MP4, video cover & duration, HLS-only notice)
			if len(post.Media) == 1 && (post.Media[0].Kind == "video" || post.Media[0].Kind == "gif" || post.Media[0].GIF) && !post.Media[0].HLSOnly {
				a.renderXDetailVideoCard(c, post.Media[0], itemKey, cachedItem, false, false, xCache)
			} else if len(post.Media) > 0 {
				pages := xMediaToNotePagesScoped(post.Media, cachedItem, false, false)
				a.renderNotePagesGrid(c, itemKey, "x", pages, true)
			}
			if hlsNotice := xPostHLSOnlyNotice(post); hlsNotice != "" {
				a.renderXHLSOnlyNotice(c, hlsNotice, targetURL, false)
			}

			// 5. Poll / Rich Cards (link card, X Articles, live & Spaces — degraded summary + open in browser, NO video cache)
			if post.Poll != nil {
				a.renderXPollCard(c, post.Poll, targetURL, false)
			}
			if post.Card != nil {
				a.renderXRichCard(c, post.Card, targetURL, false)
			}

			// 6. Retweet & Quote Nested Cards (graceful degraded origin notice when deleted/protected/withheld)
			if post.Retweeted != nil {
				a.renderXOriginCard(c, post.Retweeted, false, itemKey, cachedItem, false, xCache)
			}
			if post.Quoted != nil {
				a.renderXOriginCard(c, post.Quoted, true, itemKey, cachedItem, false, xCache)
			}
			if post.Retweeted == nil && post.Quoted == nil && post.OriginUnavailable {
				ui.Row(c).
					Label("X原帖不可用提示").
					FillWidth().
					Padding(10, 12).
					Radius(12).
					Background(ColorBgSecondary).
					Border(1, ColorBorder).
					AlignItems(ui.Center).
					Gap(6).
					Children(func() {
						DrawIcon(c, "alert-circle", 15, ColorTextMuted, 1.75)
						ui.Text(c, firstNonEmptyStrUI(post.OriginUnavailableReason, "原帖已删除或不可查看")).
							FontSize(13).LineHeight(LineHeightCompact).TextColor(ColorTextMuted).MaxLines(2)
					})
			}

			// 7. Author Self-Reply Thread Section
			if len(post.Thread) > 0 {
				ui.Column(c).
					Label("X作者自回复串").
					FillWidth().
					Padding(12, 14).
					Radius(12).
					Background(ColorBgSecondary).
					Border(1, ColorBorder).
					Gap(10).
					ClipX().
					Children(func() {
						ui.Text(c, fmt.Sprintf("作者自回复串（%d 条）", len(post.Thread))).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
						for idx, part := range post.Thread {
							tp := part
							ui.Column(c).
								Label(fmt.Sprintf("X串条目：%s", firstNonEmptyStrUI(tp.ID, fmt.Sprintf("%d", idx+1)))).
								FillWidth().
								Padding(10, 12).
								Radius(10).
								Background(ColorBgMain).
								Border(1, ColorBorder).
								Gap(6).
								ClipX().
								Children(func() {
									ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
										meta := fmt.Sprintf("#%d", idx+2)
										if tp.CreatedAt > 0 {
											meta += " · " + FormatTimestamp(tp.CreatedAt)
										}
										ui.Text(c, meta).FontSize(12).TextColor(ColorTextMuted).SingleLine()
										if tp.ID != "" {
											if OutlinePillButton(c, "查看该条", "查看X串帖子："+tp.ID).Clicked() {
												a.OpenXPost(tp.ID, clampRunes(tp.Text, 40), authorName, authorUID, "", tp.CanonicalURL)
											}
										}
									})
									if strings.TrimSpace(tp.Text) != "" {
										a.renderRichText(c, tp.Text, xPostLinksUI(&tp, nil, false), richTextOptions{
											FontSize:   14,
											LineHeight: LineHeightBody,
											TextColor:  ColorTextMain,
										})
									}
									if len(tp.Media) > 0 {
										pages := xMediaToNotePagesScoped(tp.Media, nil, false, false)
										a.renderNotePagesGrid(c, itemKey, "x", pages, true)
									}
									if tp.Card != nil {
										a.renderXRichCard(c, tp.Card, tp.CanonicalURL, false)
									}
								})
						}
					})
			}

			// 8. Online-Only Stats Row (never shown in offline /library/item mode)
			ui.Row(c).
				Label("X在线统计").
				FillWidth().
				Padding(10, 0, 0, 0).
				BorderWidth(1, 0, 0, 0).
				BorderColor(ColorBorder).
				Gap(16).
				Wrap().
				AlignItems(ui.Center).
				Children(func() {
					ui.Text(c, "回复 "+FormatCount(int64(post.Stats.ReplyCount))).FontSize(13).TextColor(ColorTextMuted).SingleLine()
					ui.Text(c, "转发 "+FormatCount(int64(post.Stats.RetweetCount))).FontSize(13).TextColor(ColorTextMuted).SingleLine()
					ui.Text(c, "喜欢 "+FormatCount(int64(post.Stats.LikeCount))).FontSize(13).TextColor(ColorTextMuted).SingleLine()
					if post.Stats.QuoteCount > 0 {
						ui.Text(c, "引用 "+FormatCount(int64(post.Stats.QuoteCount))).FontSize(13).TextColor(ColorTextMuted).SingleLine()
					}
					if post.Stats.BookmarkCount > 0 {
						ui.Text(c, "书签 "+FormatCount(int64(post.Stats.BookmarkCount))).FontSize(13).TextColor(ColorTextMuted).SingleLine()
					}
					if post.Stats.ViewCount > 0 {
						ui.Text(c, "浏览 "+FormatCount(post.Stats.ViewCount)).FontSize(13).TextColor(ColorTextMuted).SingleLine()
					}
				})
		})
	})
}

func (a *App) renderXDetailVideoCard(c *ui.Context, m service.XMediaItemDTO, itemKey string, cachedItem *library.ItemMetadata, fromOrigin bool, offline bool, cacheAct previewCacheAction) {
	isGIF := m.GIF || m.Kind == "gif"
	hasLocalVideo, relCover, localDur, videoRel := cachedItemHasVideoForScope(cachedItem, fromOrigin, false)
	coverURL := ""
	if !offline {
		coverURL = firstNonEmptyStrUI(m.PublicPreviewURL, m.ImageURL)
	}
	durSec := m.DurationSec
	if durSec <= 0 {
		durSec = localDur
	}
	dur := FormatDuration(durSec)
	if isGIF {
		dur = "GIF"
	}
	spec := formatXVideoSpec(m)
	hasPreviewURL := !offline && !m.HLSOnly && strings.TrimSpace(m.VideoURL) != ""
	title := "X 视频"
	if isGIF {
		title = "GIF 动图"
	}

	card := ui.ButtonBase(c).
		Label("X视频卡片").
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
				ui.Text(c, title).FontSize(14).LineHeight(LineHeightCompact).FontWeight(600).TextColor(ColorTextMain).MaxLines(2)
				if spec != "" {
					ui.Text(c, spec).Label("X视频规格").FontSize(12).TextColor(ColorTextMuted).SingleLine()
				}
				if hasLocalVideo {
					hint := "已缓存到本地，点击播放"
					if isGIF {
						hint = "已缓存到本地，点击静音循环播放"
					}
					ui.Text(c, hint).FontSize(12).TextColor(ColorDotGreen).SingleLine()
				} else if hasPreviewURL {
					hint := "点击预览播放，或点上方「缓存」保存到本机"
					if isGIF {
						hint = "点击静音循环预览，或点上方「缓存」保存到本机"
					}
					ui.Text(c, hint).FontSize(12).TextColor(ColorInfoBlue).SingleLine()
				} else if !offline {
					ui.Text(c, "点击上方「缓存」后可本机播放").FontSize(12).TextColor(ColorTextMuted).SingleLine()
				}
			})
		})
	})
	if card.Clicked() {
		if hasLocalVideo && cachedItem != nil {
			a.openPlayerFile(*cachedItem, videoRel)
			if isGIF {
				a.PlayerOverlay.LoopMuted = true
				a.player.SetMuted(true)
			}
		} else if hasPreviewURL {
			a.openRemoteVideoPreviewWithCache("x", title, m.VideoURL, isGIF, cacheAct)
		} else if !offline {
			c.Toast("点击上方「缓存」后可本机播放")
			a.BannerInfo = "点击上方「缓存」后可本机播放"
		}
	}
}

func (a *App) renderXOriginCard(c *ui.Context, orig *service.XPostDTO, isQuote bool, outerItemKey string, outerCachedItem *library.ItemMetadata, offline bool, cacheAct previewCacheAction) {
	if orig == nil {
		return
	}
	if orig.Unavailable {
		ui.Row(c).
			Label("X原帖不可用提示").
			FillWidth().
			Padding(10, 12).
			Radius(12).
			Background(ColorBgSecondary).
			Border(1, ColorBorder).
			AlignItems(ui.Center).
			Gap(6).
			Children(func() {
				DrawIcon(c, "alert-circle", 15, ColorTextMuted, 1.75)
				ui.Text(c, firstNonEmptyStrUI(orig.UnavailableReason, "原帖已删除或不可查看")).
					FontSize(13).LineHeight(LineHeightCompact).TextColor(ColorTextMuted).MaxLines(2)
			})
		return
	}

	origUID := strings.TrimSpace(orig.Author.RestID)
	origHandle := strings.TrimPrefix(strings.TrimSpace(orig.Author.ScreenName), "@")
	origName := firstNonEmptyStrUI(orig.Author.Name, origHandle, "原帖作者")
	openOrigAuthor := func() {
		if !offline {
			if target := firstNonEmptyStrUI(origUID, origHandle); target != "" {
				a.OpenXUser(target, origName)
			}
		}
	}
	origURL := firstNonEmptyStrUI(orig.CanonicalURL, "https://x.com/i/web/status/"+orig.ID)
	kindTag := "转发原帖"
	if isQuote {
		kindTag = "引用帖子"
	}

	ui.Column(c).
		Label("X原帖卡片").
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
					a.renderAccountAvatar(c, "x", origName, origUID, 0, 22, false, "", openOrigAuthor, orig.Author.AvatarURL)
					nameBtn := ui.ButtonBase(c).Padding(0).Children(func() {
						ui.Text(c, origName).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					})
					if nameBtn.Clicked() {
						openOrigAuthor()
					}
					if origHandle != "" {
						ui.Text(c, "@"+origHandle).FontSize(12).TextColor(ColorTextMuted).SingleLine().Shrink(1)
					}
					ui.Text(c, "· "+kindTag).FontSize(12).TextColor(ColorTextMuted).SingleLine().Shrink(0)
					if orig.CreatedAt > 0 {
						ui.Text(c, "· "+FormatRelativeTime(orig.CreatedAt)).FontSize(12).TextColor(ColorTextMuted).SingleLine().Shrink(0)
					}
				})
				if !offline && orig.ID != "" {
					if OutlinePillButton(c, "打开原帖", "打开X原帖："+orig.ID).Clicked() {
						a.OpenXPost(orig.ID, clampRunes(orig.Text, 40), origName, origUID, "", origURL)
					}
				}
			})

			if strings.TrimSpace(orig.Text) != "" {
				a.renderRichText(c, orig.Text, xPostLinksUI(orig, outerCachedItem, true), richTextOptions{
					FontSize:   14,
					LineHeight: LineHeightBody,
					TextColor:  ColorTextMain,
				})
			}
			if len(orig.Media) == 1 && (orig.Media[0].Kind == "video" || orig.Media[0].Kind == "gif" || orig.Media[0].GIF) && !orig.Media[0].HLSOnly {
				a.renderXDetailVideoCard(c, orig.Media[0], outerItemKey, outerCachedItem, true, offline, cacheAct)
			} else if len(orig.Media) > 0 {
				pages := xMediaToNotePagesScoped(orig.Media, outerCachedItem, true, offline)
				a.renderNotePagesGrid(c, outerItemKey, "x", pages, true)
			}
			if hlsNotice := xPostHLSOnlyNotice(orig); hlsNotice != "" {
				a.renderXHLSOnlyNotice(c, hlsNotice, origURL, offline)
			}
			if orig.Poll != nil {
				a.renderXPollCard(c, orig.Poll, origURL, offline)
			}
			if orig.Card != nil {
				a.renderXRichCard(c, orig.Card, origURL, offline)
			}
		})
}

// renderXOfflineDetail draws an offline cached X post on /library/item with zero
// network calls: thread, retweet/quote origin box, GIF muted loop, HLS-only missing notice, "X" badge.
func (a *App) renderXOfflineDetail(c *ui.Context, item library.ItemMetadata) {
	authorHandle := strings.TrimPrefix(strings.TrimSpace(item.Author.UniqueID), "@")
	authorName := firstNonEmptyStrUI(item.Author.Name, authorHandle, "未知用户")
	itemCopy := item

	outerPages := xMediaToNotePagesScoped(nil, &itemCopy, false, true)
	origPages := xMediaToNotePagesScoped(nil, &itemCopy, true, true)
	var outerImages, origImages []PostImage
	if len(outerPages) == 0 && len(origPages) == 0 {
		if item.Origin != nil {
			outerImages, origImages = splitLibraryMediaImagesByOrigin(&itemCopy)
		} else {
			for _, m := range item.Media {
				if m.Role == "image" {
					outerImages = append(outerImages, PostImage{
						RelPath: m.Path,
						Width:   m.Width,
						Height:  m.Height,
						IsGIF:   m.GIF || strings.HasSuffix(strings.ToLower(m.Path), ".gif"),
					})
				}
			}
		}
	}
	hasOuterVid, outerRelCover, outerDur, outerVidRel := cachedItemHasVideoForScope(&itemCopy, false, false)
	hasOrigVid, origRelCover, origDur, origVidRel := cachedItemHasVideoForScope(&itemCopy, true, false)

	ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
		ui.Column(c).Label("X离线详情").FillWidth().Padding(16).Gap(14).ClipX().Children(func() {
			// 1. Author Header (local avatar only, zero remote calls)
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(10).ClipX().Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(10).Grow(1).ClipX().Children(func() {
					a.renderAccountAvatar(c, "x", authorName, item.Author.UID, 0, 44, false, "", nil)
					ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
						ui.Row(c).AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
							ui.Text(c, authorName).FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
							if authorHandle != "" {
								ui.Text(c, "@"+authorHandle).FontSize(13).TextColor(ColorTextMuted).SingleLine().Shrink(1)
							}
							renderSiteSmallBadgeWithDot(c, "x", "本机详情站点标记")
						})
						if item.PubTs > 0 {
							ui.Text(c, FormatTimestamp(item.PubTs)).FontSize(12).TextColor(ColorTextMuted).SingleLine()
						}
					})
				})
				a.renderLibraryItemMoreMenu(c, item, 32)
			})

			// 2. Full Body Text
			if body := strings.TrimSpace(firstNonEmptyStrUI(item.Text, item.Desc, item.Title)); body != "" {
				a.renderRichText(c, body, service.MapItemLinksToDTO(item.Links), richTextOptions{
					Label:      "X离线正文",
					FontSize:   15,
					LineHeight: LineHeightBody,
					TextColor:  ColorTextMain,
				})
			}

			// 3. Cached Outer Media
			if len(outerPages) > 0 {
				a.renderNotePagesGrid(c, item.ItemKey, "x", outerPages, true)
			} else if len(outerImages) > 0 {
				a.renderPostImageGrid(c, PostCardData{
					ItemKey: item.ItemKey,
					Site:    "x",
					Offline: true,
					Images:  outerImages,
				})
			} else if hasOuterVid {
				isGIF := isGIFLibrarySegment(itemCopy, outerVidRel)
				a.renderXOfflineVideoCard(c, itemCopy, outerRelCover, outerDur, outerVidRel, isGIF)
			}

			// 4. Offline Missing / HLS-only Notice
			if len(item.Missing) > 0 {
				for idx, miss := range item.Missing {
					reason := firstNonEmptyStrUI(miss.Reason, miss.Item, "部分媒体未能缓存到本地")
					ui.Row(c).
						Label(fmt.Sprintf("X离线缺失提示：%d", idx+1)).
						FillWidth().
						Padding(10, 12).
						Radius(10).
						Background(ColorWarnBg).
						Border(1, ColorDotYellow).
						AlignItems(ui.Center).
						Gap(8).
						ClipX().
						Children(func() {
							DrawIcon(c, "alert-circle", 15, ColorDotYellow, 1.8)
							ui.Text(c, reason).FontSize(12).LineHeight(LineHeightCompact).TextColor(ColorTextMain).MaxLines(2)
						})
				}
			}

			// 5. Offline Degraded Rich Card
			if cardDTO := libraryRichCardToXDTO(item.Card); cardDTO != nil {
				a.renderXRichCard(c, cardDTO, "", true)
			}

			// 6. Cached Retweet / Quote Origin Box
			if item.Origin != nil {
				orig := item.Origin
				if orig.Unavailable {
					ui.Row(c).
						Label("X原帖不可用提示").
						FillWidth().
						Padding(10, 12).
						Radius(12).
						Background(ColorBgSecondary).
						Border(1, ColorBorder).
						AlignItems(ui.Center).
						Gap(6).
						Children(func() {
							DrawIcon(c, "alert-circle", 15, ColorTextMuted, 1.75)
							ui.Text(c, firstNonEmptyStrUI(orig.UnavailableReason, "原帖已删除或不可查看")).
								FontSize(13).LineHeight(LineHeightCompact).TextColor(ColorTextMuted).MaxLines(2)
						})
				} else {
					origHandle := strings.TrimPrefix(strings.TrimSpace(orig.Author.UniqueID), "@")
					origName := firstNonEmptyStrUI(orig.Author.Name, origHandle, "原帖作者")
					ui.Column(c).
						Label("X离线原帖卡片").
						FillWidth().
						Padding(12, 14).
						Radius(12).
						Background(ColorBgSecondary).
						Border(1, ColorBorder).
						Gap(8).
						ClipX().
						Children(func() {
							ui.Row(c).AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
								a.renderAccountAvatar(c, "x", origName, orig.Author.UID, 0, 22, false, "", nil)
								ui.Text(c, origName).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
								if origHandle != "" {
									ui.Text(c, "@"+origHandle).FontSize(12).TextColor(ColorTextMuted).SingleLine().Shrink(1)
								}
								if orig.PubTs > 0 {
									ui.Text(c, "· "+FormatTimestamp(orig.PubTs)).FontSize(12).TextColor(ColorTextMuted).SingleLine()
								}
							})
							if strings.TrimSpace(orig.Text) != "" {
								a.renderRichText(c, orig.Text, service.MapItemLinksToDTO(orig.Links), richTextOptions{
									FontSize:   14,
									LineHeight: LineHeightBody,
									TextColor:  ColorTextMain,
								})
							}
							if len(origPages) > 0 {
								a.renderNotePagesGrid(c, item.ItemKey, "x", origPages, true)
							} else if len(origImages) > 0 {
								a.renderPostImageGrid(c, PostCardData{
									ItemKey: item.ItemKey,
									Site:    "x",
									Offline: true,
									Images:  origImages,
								})
							} else if hasOrigVid {
								isGIF := isGIFLibrarySegment(itemCopy, origVidRel)
								a.renderXOfflineVideoCard(c, itemCopy, origRelCover, origDur, origVidRel, isGIF)
							}
							if origCard := libraryRichCardToXDTO(orig.Card); origCard != nil {
								a.renderXRichCard(c, origCard, "", true)
							}
						})
				}
			}

			// 7. Cached Author Self-Reply Thread Section
			if len(item.Thread) > 0 {
				ui.Column(c).
					Label("X作者自回复串").
					FillWidth().
					Padding(12, 14).
					Radius(12).
					Background(ColorBgSecondary).
					Border(1, ColorBorder).
					Gap(10).
					ClipX().
					Children(func() {
						ui.Text(c, fmt.Sprintf("作者自回复串（%d 条）", len(item.Thread))).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
						for idx, part := range item.Thread {
							tp := part
							ui.Column(c).
								Label(fmt.Sprintf("X离线串条目：%d", idx+1)).
								FillWidth().
								Padding(10, 12).
								Radius(10).
								Background(ColorBgMain).
								Border(1, ColorBorder).
								Gap(6).
								ClipX().
								Children(func() {
									meta := fmt.Sprintf("#%d", idx+2)
									if tp.PubTs > 0 {
										meta += " · " + FormatTimestamp(tp.PubTs)
									}
									ui.Text(c, meta).FontSize(12).TextColor(ColorTextMuted).SingleLine()
									if strings.TrimSpace(tp.Text) != "" {
										a.renderRichText(c, tp.Text, service.MapItemLinksToDTO(tp.Links), richTextOptions{
											FontSize:   14,
											LineHeight: LineHeightBody,
											TextColor:  ColorTextMain,
										})
									}
									if len(tp.MediaIndexes) > 0 {
										var partImgs []PostImage
										for _, mi := range tp.MediaIndexes {
											if mi >= 0 && mi < len(item.Media) {
												m := item.Media[mi]
												if m.Role == "image" && m.Path != "" {
													partImgs = append(partImgs, PostImage{
														RelPath: m.Path,
														Width:   m.Width,
														Height:  m.Height,
														IsGIF:   m.GIF,
													})
												}
											}
										}
										if len(partImgs) > 0 {
											a.renderPostImageGrid(c, PostCardData{
												ItemKey: item.ItemKey,
												Site:    "x",
												Offline: true,
												Images:  partImgs,
											})
										}
									}
								})
						}
					})
			}
		})
	})
}

func (a *App) renderXOfflineVideoCard(c *ui.Context, item library.ItemMetadata, relCover string, durSec int, videoRel string, isGIF bool) {
	dur := FormatDuration(durSec)
	title := "X 视频"
	hint := "已缓存到本地，点击播放"
	if isGIF {
		dur = "GIF"
		title = "GIF 动图"
		hint = "已缓存到本地，点击静音循环播放"
	}
	card := ui.ButtonBase(c).
		Label("X离线视频卡片").
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
			a.renderMediaThumb(c, item.ItemKey, relCover, "", dur, "", true, 200, 112)
			ui.Column(c).Grow(1).Basis(0).Padding(10, 12, 10, 0).Gap(4).ClipX().Children(func() {
				ui.Text(c, title).FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
				ui.Text(c, hint).FontSize(12).TextColor(ColorDotGreen).SingleLine()
			})
		})
	})
	if card.Clicked() {
		a.openPlayerFile(item, videoRel)
		if isGIF {
			a.PlayerOverlay.LoopMuted = true
			a.player.SetMuted(true)
		}
	}
}

func xPostLinksUI(p *service.XPostDTO, cached *library.ItemMetadata, fromOrigin bool) []service.TextLinkDTO {
	if p != nil {
		if len(p.Links) > 0 {
			return p.Links
		}
		if len(p.URLs) > 0 {
			var out []service.TextLinkDTO
			for _, u := range p.URLs {
				exp := firstNonEmptyStrUI(u.ExpandedURL, u.ShortURL)
				cleanU, ok := ValidateSafeJumpURL(exp)
				if !ok {
					continue
				}
				lbl := strings.TrimSpace(u.DisplayURL)
				if lbl == "" || strings.HasPrefix(strings.ToLower(lbl), "http://") || strings.HasPrefix(strings.ToLower(lbl), "https://") {
					lbl = FormatConciseLinkLabel(cleanU)
				}
				out = append(out, service.TextLinkDTO{Label: lbl, URL: cleanU})
			}
			if len(out) > 0 {
				return out
			}
		}
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
