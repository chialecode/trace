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
		a.notifyInfo("已加入缓存队列：" + statusID)
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
			isGIF := rec.GIF || strings.EqualFold(kind, "gif")
			if strings.EqualFold(kind, "gif") {
				if rec.Video != "" {
					kind = "video"
				} else {
					kind = "image"
				}
			} else if kind == "" {
				if rec.Video != "" {
					kind = "video"
				} else {
					kind = "image"
				}
			}
			out = append(out, douyinNoteRenderPage{
				Index:       rec.Index,
				Kind:        kind,
				IsGIF:       isGIF,
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

	origDTO := p.Retweeted
	if origDTO == nil {
		origDTO = p.Quoted
	}
	originCard, originUnavailable := xPostDTOToOriginCard(origDTO, p.OriginUnavailable, p.OriginUnavailableReason, itemKey, cachedItem, 0)

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

	cardXCard := p.Card
	cardXPoll := p.Poll
	cardHLSNotice := xPostHLSOnlyNotice(&p)
	threadCount := len(p.Thread)
	var threadParts []PostThreadPart
	if len(p.ThreadAncestors) > 0 || len(p.Thread) > 0 {
		threadParts = a.buildOnlineXThreadParts(&postCopy, pageUserCopy, cachedItem, itemKey, dot, videoCard, notePages, openPost)
		if len(threadParts) >= 2 {
			threadCount = len(threadParts)
			notePages = nil
			videoCard = nil
			cardXCard = nil
			cardXPoll = nil
			cardHLSNotice = ""
		} else {
			threadParts = nil
		}
	}

	replyTarget := xPostDTOToReplyTarget(&p)
	if len(p.ThreadAncestors) > 0 {
		replyTarget = xPostDTOToReplyTarget(&p.ThreadAncestors[0])
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
		XCard:             cardXCard,
		XPoll:             cardXPoll,
		XStatusID:         p.ID,
		XCacheVia:         via,
		HLSOnlyNotice:     cardHLSNotice,
		ThreadCount:       threadCount,
		ThreadParts:       threadParts,
		Origin:            originCard,
		OriginUnavailable: originUnavailable,
		InReplyTo:         replyTarget,
		LibItem:           cachedItem,
		OnOpenPost:        openPost,
		OpenBtnLabel:      "打开X帖子：" + p.ID,
		CacheBtnLabel:     cacheBtnLabel,
	}
}

func xPostDTOToReplyTarget(p *service.XPostDTO) *library.ItemReplyTarget {
	if p == nil {
		return nil
	}
	return library.SanitizeItemReplyTarget(&library.ItemReplyTarget{
		StatusID:   p.InReplyToStatusID,
		UserID:     p.InReplyToUserID,
		ScreenName: p.InReplyToScreenName,
	}, "x")
}

func xPostDTOToOriginCard(
	origDTO *service.XPostDTO,
	outerUnavailable bool,
	outerUnavailableReason string,
	itemKey string,
	cachedItem *library.ItemMetadata,
	depth int,
) (*PostCardData, string) {
	if origDTO == nil {
		if outerUnavailable {
			return nil, firstNonEmptyStrUI(outerUnavailableReason, "原帖已删除或不可查看")
		}
		return nil, ""
	}
	if origDTO.Unavailable {
		return nil, firstNonEmptyStrUI(origDTO.UnavailableReason, outerUnavailableReason, "原帖已删除或不可查看")
	}
	var origPages []douyinNoteRenderPage
	var origVid *PostVideo
	if depth == 0 {
		origPages = xMediaToNotePagesScoped(origDTO.Media, cachedItem, true, false)
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
	}
	var nestedOrigin *PostCardData
	var nestedUnavailable string
	if depth < 1 {
		innerDTO := origDTO.Quoted
		if innerDTO == nil {
			innerDTO = origDTO.Retweeted
		}
		if innerDTO != nil || origDTO.OriginUnavailable {
			nestedOrigin, nestedUnavailable = xPostDTOToOriginCard(innerDTO, origDTO.OriginUnavailable, origDTO.OriginUnavailableReason, itemKey, nil, depth+1)
		}
	}
	return &PostCardData{
		ItemKey:           itemKey,
		Site:              "x",
		AuthorUID:         origDTO.Author.RestID,
		AuthorName:        firstNonEmptyStrUI(origDTO.Author.Name, origDTO.Author.ScreenName, "原帖作者"),
		AuthorHandle:      origDTO.Author.ScreenName,
		AuthorFace:        origDTO.Author.AvatarURL,
		PubTs:             origDTO.CreatedAt,
		Text:              origDTO.Text,
		Links:             xPostLinksUI(origDTO, cachedItem, depth == 0),
		URL:               origDTO.CanonicalURL,
		XStatusID:         origDTO.ID,
		NotePages:         origPages,
		Video:             origVid,
		XCard:             origDTO.Card,
		XPoll:             origDTO.Poll,
		HLSOnlyNotice:     xPostHLSOnlyNotice(origDTO),
		InReplyTo:         xPostDTOToReplyTarget(origDTO),
		Origin:            nestedOrigin,
		OriginUnavailable: nestedUnavailable,
	}, ""
}

// renderXThreadConnectorLine draws the centered 2px vertical thread line
// beneath an avatar in an X self-reply thread (DEC-090).
func renderXThreadConnectorLine(c *ui.Context, colWidth float32, label string) {
	box := ui.Box(c).
		Width(colWidth).
		Grow(1).
		MinHeight(14).
		Shrink(0).
		Draw(func(p *ui.Painter, r ui.Rect) {
			lineTop := r.Y + 4
			lineH := r.H - 8
			if lineH > 2 {
				p.Fill(ui.Rect{
					X: r.X + (r.W-2)/2,
					Y: lineTop,
					W: 2,
					H: lineH,
				}, ColorBorderStrong, 1)
			}
		})
	if label != "" {
		box.Label(label)
	}
}

// renderXThreadEllipsisDots draws three vertical connector dots beside the
// collapsed long-thread "展开其余 N 条自回复" link.
func renderXThreadEllipsisDots(c *ui.Context, colWidth float32) {
	ui.Box(c).
		Width(colWidth).
		Height(22).
		Shrink(0).
		Draw(func(p *ui.Painter, r ui.Rect) {
			cx := r.X + r.W/2
			for i := 0; i < 3; i++ {
				cy := r.Y + 4 + float32(i)*6
				p.Fill(ui.Rect{X: cx - 1.5, Y: cy, W: 3, H: 3}, ColorBorderStrong, 1.5)
			}
		})
}

func normalizeXItemRawID(rawID, id, itemKey string) string {
	if s := strings.TrimSpace(rawID); s != "" {
		return s
	}
	if key := strings.TrimSpace(itemKey); key != "" {
		if first := strings.IndexByte(key, ':'); first >= 0 {
			rest := key[first+1:]
			if second := strings.IndexByte(rest, ':'); second >= 0 {
				if tail := strings.TrimSpace(rest[second+1:]); tail != "" {
					return tail
				}
			}
		}
	}
	s := strings.TrimSpace(id)
	s = strings.TrimPrefix(s, "dynamic:")
	s = strings.TrimPrefix(s, "video:")
	return strings.TrimSpace(s)
}

// stripThreadSuffixText removes trailing "\n\n<seg.Text>" blocks from a focal
// item's concatenated item.Text when synthesizing the focal segment for legacy
// or synthetic item.Thread payloads that omit the focal tweet itself.
func stripThreadSuffixText(base string, continuations []library.ItemThreadPart) string {
	cur := strings.TrimSpace(base)
	if cur == "" || len(continuations) == 0 {
		return cur
	}
	for i := len(continuations) - 1; i >= 0; i-- {
		seg := strings.TrimSpace(continuations[i].Text)
		if seg == "" {
			continue
		}
		if strings.HasSuffix(cur, "\n\n"+seg) {
			cur = strings.TrimSpace(strings.TrimSuffix(cur, "\n\n"+seg))
		}
	}
	return cur
}

func joinLibraryThreadTexts(parts []library.ItemThreadPart) string {
	var segs []string
	for _, tp := range parts {
		if t := strings.TrimSpace(tp.Text); t != "" {
			segs = append(segs, t)
		}
	}
	return strings.Join(segs, "\n\n")
}

// resolveCachedXThreadParts normalizes a cached X library item's item.Thread
// into chronological PostThreadPart nodes, supporting both:
//  1. Real cached payloads (internal/service/x_planner.go) where item.Thread
//     contains [ancestors..., focal(ID==RawID), continuations...] with 1-based
//     MediaIndexes referencing item.NotePages[].Index (excluding FromOrigin).
//  2. Synthetic/historical payloads where item.Thread only lists continuation
//     replies after the focal tweet.
//
// It returns (parts, focalIdx, true) only when there are at least 2 distinct
// thread segments; single-tweet items return (nil, -1, false).
func resolveCachedXThreadParts(
	item library.ItemMetadata,
	focalVideo *PostVideo,
	focalCard *service.XRichCardDTO,
	focalHLS string,
	fallbackFace string,
) ([]PostThreadPart, int, bool) {
	if normalizeSiteIDUI(item.Site) != "x" || len(item.Thread) == 0 {
		return nil, -1, false
	}

	rawID := normalizeXItemRawID(item.RawID, item.ID, item.ItemKey)
	rawSegs := make([]library.ItemThreadPart, 0, len(item.Thread))
	for _, seg := range item.Thread {
		if strings.TrimSpace(seg.ID) == "" && strings.TrimSpace(seg.Text) == "" && len(seg.Links) == 0 && len(seg.MediaIndexes) == 0 && seg.Origin == nil {
			continue
		}
		rawSegs = append(rawSegs, seg)
	}
	if len(rawSegs) == 0 {
		return nil, -1, false
	}

	focalIdx := -1
	if rawID != "" {
		for i, seg := range rawSegs {
			if strings.TrimSpace(seg.ID) == rawID {
				focalIdx = i
				break
			}
		}
	}

	var segments []library.ItemThreadPart
	if focalIdx >= 0 {
		// Real cached form: item.Thread already includes the focal tweet.
		if len(rawSegs) < 2 {
			return nil, -1, false
		}
		segments = rawSegs
	} else if len(rawSegs) >= 2 && strings.TrimSpace(item.Text) != "" && strings.TrimSpace(item.Text) == joinLibraryThreadTexts(rawSegs) {
		// Real cached form where RawID did not match (e.g. empty RawID) but
		// item.Text is the exact concatenation of rawSegs.
		segments = rawSegs
		focalIdx = 0
	} else {
		// Synthetic/historical form: item.Thread only holds continuation replies.
		focalText := stripThreadSuffixText(firstNonEmptyStrUI(item.Text, item.Desc, item.Title), rawSegs)
		focalSeg := library.ItemThreadPart{
			ID:    rawID,
			PubTs: item.PubTs,
			Text:  focalText,
			Links: item.Links,
		}
		segments = make([]library.ItemThreadPart, 0, 1+len(rawSegs))
		segments = append(segments, focalSeg)
		segments = append(segments, rawSegs...)
		focalIdx = 0
	}

	if len(segments) < 2 {
		return nil, -1, false
	}

	itemCopy := item
	outerPages := xMediaToNotePagesScoped(nil, &itemCopy, false, true)
	pageByIdx := make(map[int]douyinNoteRenderPage, len(outerPages))
	pageKeys := make([]int, 0, len(outerPages))
	for i, pg := range outerPages {
		idx := pg.Index
		if idx <= 0 {
			idx = i + 1
		}
		if _, exists := pageByIdx[idx]; !exists {
			pageByIdx[idx] = pg
			pageKeys = append(pageKeys, idx)
		}
	}

	var outerImages []PostImage
	if len(outerPages) == 0 {
		if item.Origin != nil {
			outerImages, _ = splitLibraryMediaImagesByOrigin(&itemCopy)
		} else {
			for _, m := range item.Media {
				if m.Role == "image" && m.Path != "" {
					outerImages = append(outerImages, PostImage{
						RelPath: m.Path,
						Width:   m.Width,
						Height:  m.Height,
						Live:    m.Motion != "",
						IsGIF:   m.GIF || strings.HasSuffix(strings.ToLower(m.Path), ".gif"),
					})
				}
			}
		}
	}

	// Detect rare 0-based legacy image indexes only when NotePages is empty and an index is 0.
	zeroBasedImages := false
	if len(outerPages) == 0 && len(outerImages) > 0 {
		for _, seg := range segments {
			for _, mi := range seg.MediaIndexes {
				if mi == 0 {
					zeroBasedImages = true
					break
				}
			}
			if zeroBasedImages {
				break
			}
		}
	}

	authorUID := strings.TrimSpace(item.Author.UID)
	authorHandle := strings.TrimPrefix(strings.TrimSpace(item.Author.UniqueID), "@")
	authorName := firstNonEmptyStrUI(item.Author.Name, authorHandle, "未知用户")

	claimedPages := make(map[int]bool, len(outerPages))
	claimedImages := make(map[int]bool, len(outerImages))
	parts := make([]PostThreadPart, len(segments))

	for i, seg := range segments {
		isFocal := i == focalIdx
		pubTs := seg.PubTs
		if pubTs <= 0 && isFocal {
			pubTs = item.PubTs
		}
		links := service.MapItemLinksToDTO(seg.Links)
		if len(links) == 0 && isFocal && len(item.Links) > 0 {
			links = service.MapItemLinksToDTO(item.Links)
		}
		partURL := ""
		if isFocal {
			partURL = item.URL
		}
		if partURL == "" && strings.TrimSpace(seg.ID) != "" {
			partURL = "https://x.com/i/web/status/" + strings.TrimSpace(seg.ID)
		}

		var partPages []douyinNoteRenderPage
		var partImgs []PostImage
		if len(outerPages) > 0 {
			for _, mi := range seg.MediaIndexes {
				if pg, exists := pageByIdx[mi]; exists && !claimedPages[mi] {
					partPages = append(partPages, pg)
					claimedPages[mi] = true
				}
			}
		} else if len(outerImages) > 0 {
			for _, mi := range seg.MediaIndexes {
				imgIdx := mi - 1
				if zeroBasedImages {
					imgIdx = mi
				}
				if imgIdx >= 0 && imgIdx < len(outerImages) && !claimedImages[imgIdx] {
					partImgs = append(partImgs, outerImages[imgIdx])
					claimedImages[imgIdx] = true
				}
			}
		}

		var partOriginCard *PostCardData
		var partOriginUnavail string
		var partLibOrigin *library.ItemOrigin
		if !isFocal && seg.Origin != nil {
			partLibOrigin = seg.Origin
			partOriginCard, partOriginUnavail = buildCachedOriginCard(item.ItemKey, "x", seg.Origin, nil, nil, nil, 0)
		}

		parts[i] = PostThreadPart{
			ID:                strings.TrimSpace(seg.ID),
			AuthorUID:         authorUID,
			AuthorName:        authorName,
			AuthorHandle:      authorHandle,
			AuthorFace:        fallbackFace,
			PubTs:             pubTs,
			Text:              strings.TrimSpace(seg.Text),
			Links:             links,
			URL:               partURL,
			Images:            partImgs,
			NotePages:         partPages,
			Origin:            partOriginCard,
			OriginUnavailable: partOriginUnavail,
			LibOrigin:         partLibOrigin,
			IsFocal:           isFocal,
		}
	}

	// Never drop any unclaimed outer media: attach to the focal tweet.
	if len(outerPages) > 0 {
		for _, idx := range pageKeys {
			if !claimedPages[idx] {
				parts[focalIdx].NotePages = append(parts[focalIdx].NotePages, pageByIdx[idx])
			}
		}
	} else if len(outerImages) > 0 {
		for i, img := range outerImages {
			if !claimedImages[i] {
				parts[focalIdx].Images = append(parts[focalIdx].Images, img)
			}
		}
	}

	// Single non-NotePage video (when focal is a single video and thread replies
	// have no extra media), rich card, and HLS notice belong to the focal node.
	if focalVideo != nil && len(outerPages) == 0 {
		parts[focalIdx].Video = focalVideo
	}
	if focalCard != nil {
		parts[focalIdx].XCard = focalCard
	}
	if focalHLS != "" {
		parts[focalIdx].HLSOnlyNotice = focalHLS
	}

	return parts, focalIdx, true
}

// xCachedNotePagesForPart resolves the locally cached non-origin NotePageRecords
// belonging to one specific tweet in a cached X thread (matching 1-based MediaIndexes).
func xCachedNotePagesForPart(cached *library.ItemMetadata, partID string, isFocal bool) []library.NotePageRecord {
	if cached == nil || len(cached.NotePages) == 0 {
		return nil
	}
	useFlags := hasAnyFromOriginFlag(cached)
	outerByIndex := make(map[int]library.NotePageRecord, len(cached.NotePages))
	var outerOrdered []library.NotePageRecord
	for i, rec := range cached.NotePages {
		if useFlags && rec.FromOrigin {
			continue
		}
		idx := rec.Index
		if idx <= 0 {
			idx = i + 1
		}
		outerByIndex[idx] = rec
		outerOrdered = append(outerOrdered, rec)
	}
	if len(outerOrdered) == 0 {
		return nil
	}
	if len(cached.Thread) == 0 {
		if isFocal {
			return outerOrdered
		}
		return nil
	}
	pid := strings.TrimSpace(partID)
	if pid != "" {
		for _, seg := range cached.Thread {
			if strings.TrimSpace(seg.ID) == pid {
				if len(seg.MediaIndexes) == 0 {
					return nil
				}
				out := make([]library.NotePageRecord, 0, len(seg.MediaIndexes))
				for _, mi := range seg.MediaIndexes {
					if rec, ok := outerByIndex[mi]; ok {
						out = append(out, rec)
					}
				}
				return out
			}
		}
	}
	if isFocal {
		// Synthetic cached thread lacking focal in cached.Thread: return unclaimed outer pages.
		claimed := make(map[int]bool)
		for _, seg := range cached.Thread {
			for _, mi := range seg.MediaIndexes {
				claimed[mi] = true
			}
		}
		var out []library.NotePageRecord
		for i, rec := range outerOrdered {
			idx := rec.Index
			if idx <= 0 {
				idx = i + 1
			}
			if !claimed[idx] {
				out = append(out, rec)
			}
		}
		return out
	}
	return nil
}

func xMediaToNotePagesForPart(media []service.XMediaItemDTO, cached *library.ItemMetadata, partID string, isFocal bool) []douyinNoteRenderPage {
	if len(media) == 0 {
		return nil
	}
	localPages := xCachedNotePagesForPart(cached, partID, isFocal)
	if len(localPages) > 0 {
		scoped := &library.ItemMetadata{NotePages: localPages}
		return xMediaToNotePagesScoped(media, scoped, false, false)
	}
	return xMediaToNotePagesScoped(media, nil, false, false)
}

func (a *App) buildOnlineXThreadParts(
	p *service.XPostDTO,
	pageUser *service.XUserDTO,
	cachedItem *library.ItemMetadata,
	itemKey string,
	dot string,
	focalVideo *PostVideo,
	focalPages []douyinNoteRenderPage,
	openFocal func(),
) []PostThreadPart {
	if p == nil || (len(p.ThreadAncestors) == 0 && len(p.Thread) == 0) {
		return nil
	}
	fallbackUID := strings.TrimSpace(p.Author.RestID)
	fallbackHandle := strings.TrimPrefix(strings.TrimSpace(p.Author.ScreenName), "@")
	fallbackName := firstNonEmptyStrUI(p.Author.Name, fallbackHandle, "未知用户")
	fallbackFace := p.Author.AvatarURL
	if pageUser != nil {
		if fallbackUID == "" {
			fallbackUID = strings.TrimSpace(pageUser.RestID)
		}
		if fallbackHandle == "" {
			fallbackHandle = strings.TrimPrefix(strings.TrimSpace(pageUser.ScreenName), "@")
		}
		if fallbackName == "" || fallbackName == "未知用户" {
			fallbackName = firstNonEmptyStrUI(pageUser.Name, fallbackHandle, "未知用户")
		}
		if fallbackFace == "" {
			fallbackFace = pageUser.AvatarURL
		}
	}

	buildSeg := func(seg service.XPostDTO, isFocal bool) PostThreadPart {
		segCopy := seg
		uID := firstNonEmptyStrUI(seg.Author.RestID, fallbackUID)
		handle := strings.TrimPrefix(strings.TrimSpace(firstNonEmptyStrUI(seg.Author.ScreenName, fallbackHandle)), "@")
		name := firstNonEmptyStrUI(seg.Author.Name, handle, fallbackName)
		face := firstNonEmptyStrUI(seg.Author.AvatarURL, fallbackFace)
		segURL := firstNonEmptyStrUI(seg.CanonicalURL, func() string {
			if seg.ID != "" {
				return "https://x.com/i/web/status/" + seg.ID
			}
			return ""
		}())

		var segPages []douyinNoteRenderPage
		var segVideo *PostVideo
		if isFocal {
			segPages = focalPages
			segVideo = focalVideo
			if len(segPages) > 0 && cachedItem != nil && len(cachedItem.Thread) > 0 {
				segPages = xMediaToNotePagesForPart(seg.Media, cachedItem, seg.ID, true)
			}
		} else if len(seg.Media) == 1 && (seg.Media[0].Kind == "video" || seg.Media[0].Kind == "gif" || seg.Media[0].GIF) && !seg.Media[0].HLSOnly {
			m0 := seg.Media[0]
			localRecs := xCachedNotePagesForPart(cachedItem, seg.ID, false)
			relCover := ""
			localCached := false
			durSec := m0.DurationSec
			if len(localRecs) > 0 {
				relCover = firstNonEmptyStrUI(localRecs[0].Cover, localRecs[0].Image)
				localCached = localRecs[0].Video != ""
				if durSec <= 0 {
					durSec = localRecs[0].Duration
				}
			}
			vidTitle := "X 视频"
			if m0.GIF || m0.Kind == "gif" {
				vidTitle = "GIF 动图"
			}
			segVideo = &PostVideo{
				BVID:       seg.ID,
				Title:      vidTitle,
				CoverURL:   firstNonEmptyStrUI(m0.PublicPreviewURL, m0.ImageURL),
				RelCover:   relCover,
				Duration:   FormatDuration(durSec),
				ItemKey:    itemKey,
				Cached:     localCached,
				Status:     dot,
				QualityTag: formatXVideoSpec(m0),
			}
		} else if len(seg.Media) > 0 {
			segPages = xMediaToNotePagesForPart(seg.Media, cachedItem, seg.ID, false)
		}

		var onOpen func()
		if isFocal {
			onOpen = openFocal
		} else if segCopy.ID != "" {
			onOpen = func() {
				a.OpenXPost(segCopy.ID, clampRunes(segCopy.Text, 40), name, uID, "", segURL)
			}
		}

		var links []service.TextLinkDTO
		if isFocal {
			links = xPostLinksUI(&segCopy, cachedItem, false)
		} else {
			links = xPostLinksUI(&segCopy, nil, false)
		}

		var segOriginCard *PostCardData
		var segOriginUnavail string
		if !isFocal && (segCopy.Quoted != nil || segCopy.OriginUnavailable) {
			segOriginCard, segOriginUnavail = xPostDTOToOriginCard(segCopy.Quoted, segCopy.OriginUnavailable, segCopy.OriginUnavailableReason, itemKey, nil, 0)
		}

		return PostThreadPart{
			ID:                segCopy.ID,
			AuthorUID:         uID,
			AuthorName:        name,
			AuthorHandle:      handle,
			AuthorFace:        face,
			PubTs:             segCopy.CreatedAt,
			InReplyTo:         xPostDTOToReplyTarget(&segCopy),
			Text:              segCopy.Text,
			Links:             links,
			URL:               segURL,
			NotePages:         segPages,
			Video:             segVideo,
			XCard:             segCopy.Card,
			XPoll:             segCopy.Poll,
			HLSOnlyNotice:     xPostHLSOnlyNotice(&segCopy),
			Origin:            segOriginCard,
			OriginUnavailable: segOriginUnavail,
			IsFocal:           isFocal,
			OnOpen:            onOpen,
		}
	}

	out := make([]PostThreadPart, 0, len(p.ThreadAncestors)+1+len(p.Thread))
	for _, anc := range p.ThreadAncestors {
		out = append(out, buildSeg(anc, false))
	}
	out = append(out, buildSeg(*p, true))
	for _, cont := range p.Thread {
		out = append(out, buildSeg(cont, false))
	}
	return out
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

	var xCache previewCacheAction
	if !suppressCache && post.ID != "" {
		postIDCopy := post.ID
		var viaCopy *library.ItemVia
		if v := a.xCacheViaFor(post); v != nil {
			cp := *v
			viaCopy = &cp
		}
		backend := a.backend
		xCache = previewCacheAction{
			ItemKey:    itemKey,
			PendingKey: "cache-x-post:" + postIDCopy,
			Enqueue: func(ctx context.Context) error {
				if backend == nil {
					return fmt.Errorf("后端服务未初始化")
				}
				_, err := backend.CacheXPost(ctx, postIDCopy, viaCopy)
				return err
			},
		}
	}

	renderFocalActions := func() {
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
						a.notifyInfo("已复制链接")
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
							a.openForceRecacheConfirm(itemKey, firstNonEmptyStrUI(cachedItem.Title, post.Text, post.ID))
						}
					}
					if m.Item("打开文件夹").Chosen() {
						a.showError(a.backend.OpenItemFolder(itemKey))
					}
				}
			})
			keyedMenuOpen(&a.PostMenuKey, menuKey, open)
		})
	}

	renderFocalMetaText := func() {
		ui.Column(c).Grow(1).Basis(0).Gap(2).ClipX().Children(func() {
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
	}

	threadIDs := make([]string, 0, len(post.ThreadAncestors)+1+len(post.Thread))
	for _, anc := range post.ThreadAncestors {
		if id := strings.TrimSpace(anc.ID); id != "" {
			threadIDs = append(threadIDs, id)
		}
	}
	if id := strings.TrimSpace(post.ID); id != "" {
		threadIDs = append(threadIDs, id)
	}
	for _, cont := range post.Thread {
		if id := strings.TrimSpace(cont.ID); id != "" {
			threadIDs = append(threadIDs, id)
		}
	}

	renderFocalBodyAndMedia := func(inThread bool) {
		// Visibility / Sensitive / Withheld Note
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

		// Concise reply context hint above body (suppressed for internal self-reply thread continuations).
		if !inThread || len(post.ThreadAncestors) == 0 {
			if hint := formatReplyContextHint(xPostDTOToReplyTarget(post), threadIDs); hint != "" {
				renderReplyContextHintLine(c, hint, "X回复提示", 12)
			}
		}

		// Full Body Text (note_tweet rendered completely)
		if strings.TrimSpace(post.Text) != "" {
			a.renderRichText(c, post.Text, xPostLinksUI(post, cachedItem, false), richTextOptions{
				Label:      "X帖子正文",
				FontSize:   15,
				LineHeight: LineHeightBody,
				TextColor:  ColorTextMain,
			})
		}

		// Up to 4 Mixed Media Items (original photos, GIF looping muted MP4, video cover & duration, HLS-only notice)
		if len(post.Media) == 1 && (post.Media[0].Kind == "video" || post.Media[0].Kind == "gif" || post.Media[0].GIF) && !post.Media[0].HLSOnly {
			a.renderXDetailVideoCardForPart(c, post.Media[0], itemKey, cachedItem, post.ID, true, false, false, xCache)
		} else if len(post.Media) > 0 {
			var pages []douyinNoteRenderPage
			if inThread {
				pages = xMediaToNotePagesForPart(post.Media, cachedItem, post.ID, true)
			} else {
				pages = xMediaToNotePagesScoped(post.Media, cachedItem, false, false)
			}
			a.renderNotePagesGrid(c, itemKey, "x", pages, true)
		}
		if hlsNotice := xPostHLSOnlyNotice(post); hlsNotice != "" {
			a.renderXHLSOnlyNotice(c, hlsNotice, targetURL, false)
		}

		// Poll / Rich Cards (link card, X Articles, live & Spaces — degraded summary + open in browser, NO video cache)
		if post.Poll != nil {
			a.renderXPollCard(c, post.Poll, targetURL, false)
		}
		if post.Card != nil {
			a.renderXRichCard(c, post.Card, targetURL, false)
		}

		// Retweet & Quote Nested Cards (graceful degraded origin notice when deleted/protected/withheld)
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
	}

	renderOnlineThreadNode := func(seg service.XPostDTO, rowLabel, lineLabel string, hasLineBelow bool, showRootReplyHint bool) {
		tp := seg
		segUID := firstNonEmptyStrUI(tp.Author.RestID, authorUID)
		segHandle := strings.TrimPrefix(strings.TrimSpace(firstNonEmptyStrUI(tp.Author.ScreenName, authorHandle)), "@")
		segName := firstNonEmptyStrUI(tp.Author.Name, segHandle, authorName)
		segAvatarURL := firstNonEmptyStrUI(tp.Author.AvatarURL, post.Author.AvatarURL)
		segURL := firstNonEmptyStrUI(tp.CanonicalURL, func() string {
			if tp.ID != "" {
				return "https://x.com/i/web/status/" + tp.ID
			}
			return targetURL
		}())
		openSegAuthor := func() {
			if target := firstNonEmptyStrUI(segUID, segHandle); target != "" {
				a.OpenXUser(target, segName)
			}
		}
		bottomPad := float32(0)
		if hasLineBelow {
			bottomPad = 14
		}

		ui.Row(c).Label(rowLabel).FillWidth().Gap(12).AlignItems(ui.Stretch).ClipX().Children(func() {
			ui.Column(c).Width(44).Shrink(0).AlignItems(ui.Center).Children(func() {
				a.renderAccountAvatar(c, "x", segName, segUID, 0, 40, false, "", openSegAuthor, segAvatarURL)
				if hasLineBelow {
					renderXThreadConnectorLine(c, 44, lineLabel)
				}
			})

			ui.Column(c).Grow(1).Basis(0).Gap(8).Padding(0, 0, bottomPad, 0).ClipX().Children(func() {
				ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
					ui.Row(c).AlignItems(ui.Center).Gap(6).Grow(1).Basis(0).ClipX().Children(func() {
						nameBtn := ui.ButtonBase(c).Padding(0).Children(func() {
							ui.Text(c, segName).FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
						})
						if nameBtn.Clicked() {
							openSegAuthor()
						}
						if segHandle != "" {
							ui.Text(c, "@"+segHandle).FontSize(13).TextColor(ColorTextMuted).SingleLine().Shrink(1)
						}
						if tp.CreatedAt > 0 {
							ui.Text(c, "· "+FormatTimestamp(tp.CreatedAt)).FontSize(12).TextColor(ColorTextMuted).SingleLine()
						}
					})
					if tp.ID != "" {
						if OutlinePillButton(c, "查看该条", "查看X串帖子："+tp.ID).Clicked() {
							a.OpenXPost(tp.ID, clampRunes(tp.Text, 40), segName, segUID, "", segURL)
						}
					}
				})

				if showRootReplyHint {
					if hint := formatReplyContextHint(xPostDTOToReplyTarget(&tp), threadIDs); hint != "" {
						renderReplyContextHintLine(c, hint, "X回复提示", 12)
					}
				}
				if strings.TrimSpace(tp.Text) != "" {
					a.renderRichText(c, tp.Text, xPostLinksUI(&tp, nil, false), richTextOptions{
						FontSize:   15,
						LineHeight: LineHeightBody,
						TextColor:  ColorTextMain,
					})
				}
				if len(tp.Media) == 1 && (tp.Media[0].Kind == "video" || tp.Media[0].Kind == "gif" || tp.Media[0].GIF) && !tp.Media[0].HLSOnly {
					a.renderXDetailVideoCardForPart(c, tp.Media[0], itemKey, cachedItem, tp.ID, false, false, false, xCache)
				} else if len(tp.Media) > 0 {
					pages := xMediaToNotePagesForPart(tp.Media, cachedItem, tp.ID, false)
					a.renderNotePagesGrid(c, itemKey, "x", pages, true)
				}
				if hlsNotice := xPostHLSOnlyNotice(&tp); hlsNotice != "" {
					a.renderXHLSOnlyNotice(c, hlsNotice, segURL, false)
				}
				if tp.Poll != nil {
					a.renderXPollCard(c, tp.Poll, segURL, false)
				}
				if tp.Card != nil {
					a.renderXRichCard(c, tp.Card, segURL, false)
				}
				if tp.Quoted != nil {
					a.renderXOriginCard(c, tp.Quoted, true, itemKey, nil, false, xCache)
				} else if tp.OriginUnavailable {
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
							ui.Text(c, firstNonEmptyStrUI(tp.OriginUnavailableReason, "原帖已删除或不可查看")).
								FontSize(13).LineHeight(LineHeightCompact).TextColor(ColorTextMuted).MaxLines(2)
						})
				}
			})
		})
	}

	hasThread := len(post.ThreadAncestors) > 0 || len(post.Thread) > 0

	ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
		ui.Column(c).FillWidth().Padding(16).Gap(14).ClipX().Children(func() {
			if hasThread {
				ui.Column(c).Label("X作者自回复串").FillWidth().Gap(0).ClipX().Children(func() {
					// 0. Ancestor tweets above focal, vertically connected down into focal.
					for idx, anc := range post.ThreadAncestors {
						ancID := firstNonEmptyStrUI(anc.ID, fmt.Sprintf("anc-%d", idx+1))
						renderOnlineThreadNode(anc, "X串上文："+ancID, "X串连线："+ancID, true, idx == 0)
					}

					// 1. Focal tweet with primary header, actions, full body, media, and origin card.
					focalHasLineBelow := len(post.Thread) > 0
					focalBottomPad := float32(0)
					if focalHasLineBelow {
						focalBottomPad = 16
					}
					ui.Row(c).Label("X串主帖").FillWidth().Gap(12).AlignItems(ui.Stretch).ClipX().Children(func() {
						ui.Column(c).Width(44).Shrink(0).AlignItems(ui.Center).Children(func() {
							a.renderAccountAvatar(c, "x", authorName, authorUID, 0, 44, false, "打开X用户："+authorUID, openAuthor, post.Author.AvatarURL)
							if focalHasLineBelow {
								renderXThreadConnectorLine(c, 44, "X串连线："+firstNonEmptyStrUI(post.ID, "focal"))
							}
						})
						ui.Column(c).Grow(1).Basis(0).Gap(12).Padding(0, 0, focalBottomPad, 0).ClipX().Children(func() {
							ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(10).ClipX().Children(func() {
								renderFocalMetaText()
								renderFocalActions()
							})
							renderFocalBodyAndMedia(true)
						})
					})

					// 2. Author self-reply continuations vertically connected below focal.
					for idx, part := range post.Thread {
						partID := firstNonEmptyStrUI(part.ID, fmt.Sprintf("%d", idx+1))
						isLast := idx == len(post.Thread)-1
						renderOnlineThreadNode(part, "X串条目："+partID, "X串连线："+partID, !isLast, false)
					}
				})
			} else {
				// Standalone single-tweet detail layout.
				ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(10).ClipX().Children(func() {
					ui.Row(c).AlignItems(ui.Center).Gap(10).Grow(1).Basis(0).ClipX().Children(func() {
						a.renderAccountAvatar(c, "x", authorName, authorUID, 0, 44, false, "打开X用户："+authorUID, openAuthor, post.Author.AvatarURL)
						renderFocalMetaText()
					})
					renderFocalActions()
				})
				renderFocalBodyAndMedia(false)
			}

			// Online-Only Stats Row (never shown in offline /library/item mode)
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
	a.renderXDetailVideoCardForPart(c, m, itemKey, cachedItem, "", !fromOrigin, fromOrigin, offline, cacheAct)
}

func (a *App) renderXDetailVideoCardForPart(c *ui.Context, m service.XMediaItemDTO, itemKey string, cachedItem *library.ItemMetadata, partID string, isFocal bool, fromOrigin bool, offline bool, cacheAct previewCacheAction) {
	isGIF := m.GIF || m.Kind == "gif"
	var hasLocalVideo bool
	var relCover string
	var localDur int
	var videoRel string
	if !fromOrigin && cachedItem != nil && len(cachedItem.Thread) > 0 && len(cachedItem.NotePages) > 0 {
		if recs := xCachedNotePagesForPart(cachedItem, partID, isFocal); len(recs) > 0 && recs[0].Video != "" {
			hasLocalVideo = true
			relCover = firstNonEmptyStrUI(recs[0].Cover, recs[0].Image)
			localDur = recs[0].Duration
			videoRel = recs[0].Video
		}
	}
	if !hasLocalVideo && (isFocal || fromOrigin || cachedItem == nil || len(cachedItem.Thread) == 0) {
		hasLocalVideo, relCover, localDur, videoRel = cachedItemHasVideoForScope(cachedItem, fromOrigin, false)
	}
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
			a.notifyInfo("点击上方「缓存」后可本机播放")
		}
	}
}

func (a *App) renderXOriginCard(c *ui.Context, orig *service.XPostDTO, isQuote bool, outerItemKey string, outerCachedItem *library.ItemMetadata, offline bool, cacheAct previewCacheAction) {
	a.renderXOriginCardDepth(c, orig, isQuote, outerItemKey, outerCachedItem, offline, cacheAct, 0)
}

func (a *App) renderXOriginCardDepth(c *ui.Context, orig *service.XPostDTO, isQuote bool, outerItemKey string, outerCachedItem *library.ItemMetadata, offline bool, cacheAct previewCacheAction, depth int) {
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

	cardLabel := "X原帖卡片"
	bg := ColorBgSecondary
	padV, padH := float32(12), float32(14)
	radius := float32(12)
	if depth > 0 {
		cardLabel = "X内层引用卡片"
		bg = ColorBgMain
		padV, padH = 9, 11
		radius = 8
	}

	ui.Column(c).
		Label(cardLabel).
		FillWidth().
		Padding(padV, padH).
		Radius(radius).
		Background(bg).
		Border(1, ColorBorder).
		Gap(8).
		ClipX().
		Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(6).Grow(1).ClipX().Children(func() {
					avatarSize := float32(22)
					if depth > 0 {
						avatarSize = 18
					}
					a.renderAccountAvatar(c, "x", origName, origUID, 0, avatarSize, false, "", openOrigAuthor, orig.Author.AvatarURL)
					nameBtn := ui.ButtonBase(c).Padding(0).Children(func() {
						ui.Text(c, origName).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					})
					if nameBtn.Clicked() {
						openOrigAuthor()
					}
					if origHandle != "" {
						ui.Text(c, "@"+origHandle).FontSize(12).TextColor(ColorTextMuted).SingleLine().Shrink(1)
					}
					if depth == 0 {
						ui.Text(c, "· "+kindTag).FontSize(12).TextColor(ColorTextMuted).SingleLine().Shrink(0)
					}
					if orig.CreatedAt > 0 {
						ui.Text(c, "· "+FormatRelativeTime(orig.CreatedAt)).FontSize(12).TextColor(ColorTextMuted).SingleLine().Shrink(0)
					}
				})
				if depth == 0 && !offline && orig.ID != "" {
					if OutlinePillButton(c, "打开原帖", "打开X原帖："+orig.ID).Clicked() {
						a.OpenXPost(orig.ID, clampRunes(orig.Text, 40), origName, origUID, "", origURL)
					}
				}
			})

			if replyHint := formatReplyContextHint(xPostDTOToReplyTarget(orig), nil); replyHint != "" {
				ui.Text(c, replyHint).FontSize(11).TextColor(ColorTextMuted).SingleLine()
			}
			if strings.TrimSpace(orig.Text) != "" {
				if depth > 0 {
					ui.Text(c, orig.Text).FontSize(13).LineHeight(LineHeightCompact).TextColor(ColorTextMain).MaxLines(4)
				} else {
					a.renderRichText(c, orig.Text, xPostLinksUI(orig, outerCachedItem, true), richTextOptions{
						FontSize:   14,
						LineHeight: LineHeightBody,
						TextColor:  ColorTextMain,
					})
				}
			}
			if depth == 0 {
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
				// Strictly cap nested origin display at 1 level (DEC-091).
				inner := orig.Quoted
				if inner == nil {
					inner = orig.Retweeted
				}
				if inner != nil {
					a.renderXOriginCardDepth(c, inner, orig.Quoted != nil, outerItemKey, nil, offline, previewCacheAction{}, depth+1)
				} else if orig.OriginUnavailable {
					ui.Row(c).
						Label("X内层引用不可用提示").
						FillWidth().
						Padding(8, 10).
						Radius(8).
						Background(ColorBgMain).
						Border(1, ColorBorder).
						AlignItems(ui.Center).
						Gap(6).
						Children(func() {
							DrawIcon(c, "alert-circle", 14, ColorTextMuted, 1.75)
							ui.Text(c, firstNonEmptyStrUI(orig.OriginUnavailableReason, "原帖已删除或不可查看")).
								FontSize(12).LineHeight(LineHeightCompact).TextColor(ColorTextMuted).MaxLines(2)
						})
				}
			}
		})
}

// renderXOfflineDetail draws an offline cached X post on /library/item with zero
// network calls: thread (vertically connected avatar line), retweet/quote origin box,
// GIF muted loop, HLS-only missing notice, "X" badge.
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
	videoBelongsToOrigin := itemVideoBelongsToOrigin(&itemCopy)

	threadParts, focalIdx, isThread := resolveCachedXThreadParts(itemCopy, nil, libraryRichCardToXDTO(item.Card), "", "")

	renderOfflineMissingAndExtras := func() {
		// Offline Missing / HLS-only Notice
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

		// Offline Degraded Rich Card
		if cardDTO := libraryRichCardToXDTO(item.Card); cardDTO != nil {
			a.renderXRichCard(c, cardDTO, "", true)
		}

		// Cached Retweet / Quote Origin Box
		if item.Origin != nil {
			a.renderXOfflineOriginBoxDepth(c, itemCopy, item.Origin, origPages, origImages, hasOrigVid, origRelCover, origDur, origVidRel, "X离线原帖卡片", 0)
		}
	}

	threadIDs := make([]string, 0, len(threadParts))
	for _, pt := range threadParts {
		if id := strings.TrimSpace(pt.ID); id != "" {
			threadIDs = append(threadIDs, id)
		}
	}
	offlineRootReplyHint := formatReplyContextHint(item.InReplyTo, threadIDs)
	offlineTypeLabel := strings.TrimSpace(item.TypeLabel)
	if (offlineTypeLabel == "" || offlineTypeLabel == "转发") && item.Origin != nil && strings.TrimSpace(item.Text) != "" {
		offlineTypeLabel = "引用帖子"
	}
	formatOfflineMetaLine := func(pubTs int64) string {
		var parts []string
		if pubTs > 0 {
			parts = append(parts, FormatTimestamp(pubTs))
		}
		if offlineTypeLabel != "" {
			parts = append(parts, offlineTypeLabel)
		}
		return strings.Join(parts, " · ")
	}

	ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
		ui.Column(c).Label("X离线详情").FillWidth().Padding(16).Gap(14).ClipX().Children(func() {
			if isThread {
				ui.Column(c).Label("X作者自回复串").FillWidth().Gap(0).ClipX().Children(func() {
					for idx, part := range threadParts {
						tp := part
						isFirst := idx == 0
						isLast := idx == len(threadParts)-1
						isFocal := idx == focalIdx
						bottomPad := float32(0)
						if !isLast {
							bottomPad = 14
						}
						nodeLabel := fmt.Sprintf("X离线串条目：%d", idx+1)
						ui.Row(c).Label(nodeLabel).FillWidth().Gap(12).AlignItems(ui.Stretch).ClipX().Children(func() {
							ui.Column(c).Width(44).Shrink(0).AlignItems(ui.Center).Children(func() {
								avatarSz := float32(40)
								if isFirst {
									avatarSz = 44
								}
								a.renderAccountAvatar(c, "x", authorName, item.Author.UID, 0, avatarSz, false, "", nil)
								if !isLast {
									renderXThreadConnectorLine(c, 44, fmt.Sprintf("X离线串连线：%d", idx+1))
								}
							})

							ui.Column(c).Grow(1).Basis(0).Gap(8).Padding(0, 0, bottomPad, 0).ClipX().Children(func() {
								if isFirst {
									ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(10).ClipX().Children(func() {
										ui.Column(c).Grow(1).Basis(0).Gap(2).ClipX().Children(func() {
											ui.Row(c).AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
												ui.Text(c, authorName).FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
												if authorHandle != "" {
													ui.Text(c, "@"+authorHandle).FontSize(13).TextColor(ColorTextMuted).SingleLine().Shrink(1)
												}
												renderSiteSmallBadgeWithDot(c, "x", "本机详情站点标记")
											})
											pubTs := tp.PubTs
											if pubTs <= 0 {
												pubTs = item.PubTs
											}
											if metaLine := formatOfflineMetaLine(pubTs); metaLine != "" {
												ui.Text(c, metaLine).FontSize(12).TextColor(ColorTextMuted).SingleLine()
											}
										})
										a.renderLibraryItemMoreMenu(c, item, 32)
									})
								} else {
									ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
										ui.Text(c, authorName).FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
										if authorHandle != "" {
											ui.Text(c, "@"+authorHandle).FontSize(13).TextColor(ColorTextMuted).SingleLine().Shrink(1)
										}
										if tp.PubTs > 0 {
											ui.Text(c, "· "+FormatTimestamp(tp.PubTs)).FontSize(12).TextColor(ColorTextMuted).SingleLine()
										}
									})
								}

								if isFirst && offlineRootReplyHint != "" {
									renderReplyContextHintLine(c, offlineRootReplyHint, "X离线回复提示", 12)
								}
								if strings.TrimSpace(tp.Text) != "" {
									bodyLabel := ""
									if isFocal {
										bodyLabel = "X离线正文"
									}
									a.renderRichText(c, tp.Text, tp.Links, richTextOptions{
										Label:      bodyLabel,
										FontSize:   15,
										LineHeight: LineHeightBody,
										TextColor:  ColorTextMain,
									})
								}

								if len(tp.NotePages) > 0 {
									a.renderNotePagesGrid(c, item.ItemKey, "x", tp.NotePages, true)
								} else if len(tp.Images) > 0 {
									a.renderPostImageGrid(c, PostCardData{
										ItemKey: item.ItemKey,
										Site:    "x",
										Offline: true,
										Images:  tp.Images,
									})
								} else if isFocal && hasOuterVid && !videoBelongsToOrigin && len(outerPages) == 0 {
									isGIF := isGIFLibrarySegment(itemCopy, outerVidRel)
									a.renderXOfflineVideoCard(c, itemCopy, outerRelCover, outerDur, outerVidRel, isGIF)
								}

								if isFocal {
									renderOfflineMissingAndExtras()
								} else if tp.LibOrigin != nil {
									a.renderXOfflineOriginBoxDepth(c, itemCopy, tp.LibOrigin, nil, nil, false, "", 0, "", fmt.Sprintf("X离线串引用：%d", idx+1), 0)
								}
							})
						})
					}
				})
				return
			}

			// Single-tweet offline layout (zero remote calls).
			// 1. Author Header
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(10).ClipX().Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(10).Grow(1).Basis(0).ClipX().Children(func() {
					a.renderAccountAvatar(c, "x", authorName, item.Author.UID, 0, 44, false, "", nil)
					ui.Column(c).Grow(1).Basis(0).Gap(2).ClipX().Children(func() {
						ui.Row(c).AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
							ui.Text(c, authorName).FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
							if authorHandle != "" {
								ui.Text(c, "@"+authorHandle).FontSize(13).TextColor(ColorTextMuted).SingleLine().Shrink(1)
							}
							renderSiteSmallBadgeWithDot(c, "x", "本机详情站点标记")
						})
						if metaLine := formatOfflineMetaLine(item.PubTs); metaLine != "" {
							ui.Text(c, metaLine).FontSize(12).TextColor(ColorTextMuted).SingleLine()
						}
					})
				})
				a.renderLibraryItemMoreMenu(c, item, 32)
			})

			// 2. Concise reply context hint & Full Body Text
			if hint := formatReplyContextHint(item.InReplyTo, nil); hint != "" {
				renderReplyContextHintLine(c, hint, "X离线回复提示", 12)
			}
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
			} else if hasOuterVid && !videoBelongsToOrigin {
				isGIF := isGIFLibrarySegment(itemCopy, outerVidRel)
				a.renderXOfflineVideoCard(c, itemCopy, outerRelCover, outerDur, outerVidRel, isGIF)
			}

			// 4-6. Missing notices, degraded rich card, origin box
			renderOfflineMissingAndExtras()
		})
	})
}

func (a *App) renderXOfflineOriginBoxDepth(
	c *ui.Context,
	item library.ItemMetadata,
	orig *library.ItemOrigin,
	origPages []douyinNoteRenderPage,
	origImages []PostImage,
	hasOrigVid bool,
	origRelCover string,
	origDur int,
	origVidRel string,
	label string,
	depth int,
) {
	if orig == nil {
		return
	}
	if orig.Unavailable {
		unavailLabel := "X原帖不可用提示"
		if depth > 0 {
			unavailLabel = "X离线内层引用不可用提示"
		}
		bg := ColorBgSecondary
		if depth > 0 {
			bg = ColorBgMain
		}
		ui.Row(c).
			Label(unavailLabel).
			FillWidth().
			Padding(10, 12).
			Radius(10).
			Background(bg).
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
	origHandle := strings.TrimPrefix(strings.TrimSpace(orig.Author.UniqueID), "@")
	origName := firstNonEmptyStrUI(orig.Author.Name, origHandle, "原帖作者")
	bg := ColorBgSecondary
	padV, padH := float32(12), float32(14)
	radius := float32(12)
	avatarSize := float32(22)
	if depth > 0 {
		bg = ColorBgMain
		padV, padH = 9, 11
		radius = 8
		avatarSize = 18
	}
	ui.Column(c).
		Label(label).
		FillWidth().
		Padding(padV, padH).
		Radius(radius).
		Background(bg).
		Border(1, ColorBorder).
		Gap(8).
		ClipX().
		Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
				a.renderAccountAvatar(c, "x", origName, orig.Author.UID, 0, avatarSize, false, "", nil)
				ui.Text(c, origName).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
				if origHandle != "" {
					ui.Text(c, "@"+origHandle).FontSize(12).TextColor(ColorTextMuted).SingleLine().Shrink(1)
				}
				if orig.PubTs > 0 {
					ui.Text(c, "· "+FormatTimestamp(orig.PubTs)).FontSize(12).TextColor(ColorTextMuted).SingleLine()
				}
			})
			if replyHint := formatReplyContextHint(orig.InReplyTo, nil); replyHint != "" {
				ui.Text(c, replyHint).FontSize(11).TextColor(ColorTextMuted).SingleLine()
			}
			if strings.TrimSpace(orig.Text) != "" {
				if depth > 0 {
					ui.Text(c, orig.Text).FontSize(13).LineHeight(LineHeightCompact).TextColor(ColorTextMain).MaxLines(4)
				} else {
					a.renderRichText(c, orig.Text, service.MapItemLinksToDTO(orig.Links), richTextOptions{
						FontSize:   14,
						LineHeight: LineHeightBody,
						TextColor:  ColorTextMain,
					})
				}
			}
			if depth == 0 {
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
					isGIF := isGIFLibrarySegment(item, origVidRel)
					a.renderXOfflineVideoCard(c, item, origRelCover, origDur, origVidRel, isGIF)
				}
				if origCard := libraryRichCardToXDTO(orig.Card); origCard != nil {
					a.renderXRichCard(c, origCard, "", true)
				}
				// Strictly cap nested origin rendering at 1 level (DEC-091).
				if orig.Origin != nil {
					a.renderXOfflineOriginBoxDepth(c, item, orig.Origin, nil, nil, false, "", 0, "", "X离线内层引用卡片", depth+1)
				}
			}
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
