package home

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/chialecode/trace/cmd/trace-mobile/internal/mediaview"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/pagekit"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/theme"
	"github.com/chialecode/trace/internal/companionapi"
	"github.com/egoist/mygo/ui"
)

const (
	xThreadMaxInline        = 4
	xThreadCollapsedPreview = 3
)

type mobileThreadPart struct {
	ID      string
	PubTs   int64
	Text    string
	Links   []companionapi.TextLink
	Images  []mediaview.ImageItem
	Videos  []mobileThreadVideo
	Origin  *companionapi.ItemOriginDTO
	IsFocal bool
}

type mobileThreadVideo struct {
	Title        string
	CoverRelPath string
	DurationSec  int
	QualitySpec  string
	Req          mediaview.VideoRequest
}

func (p *Page) renderPostCard(c *ui.Context, h pagekit.Host, item companionapi.FeedItem, inSubView bool) {
	if parts, focalIdx, origImages, ok := resolveMobileFeedThread(item); ok {
		p.renderThreadPostCard(c, h, item, inSubView, parts, focalIdx, origImages)
		return
	}

	ui.Column(c).
		FillWidth().
		Background(theme.ColorBgMain).
		Padding(14, 14).
		Gap(10).
		BorderWidth(0, 0, 1, 0).
		Border(1, theme.ColorBorder).
		Children(func() {
			// 1. Author row (avatar, name + accent subName, time · site · type).
			p.renderPostAuthorRow(c, h, item, inSubView)

			// 1b. Concise reply context hint above body when replying to another post (DEC-091).
			if hint := formatMobileReplyHint(item.InReplyTo, nil); hint != "" {
				ui.Text(c, hint).FontSize(12).TextColor(theme.ColorTextMuted).SingleLine()
			}

			// 2. Title (when distinct from body text) and body text with expand/collapse + clickable links.
			p.renderPostText(c, h, item)

			// 3. Images / NotePages gallery.
			images := collectFeedViewerImages(item)
			if len(images) > 0 {
				p.renderPostImages(c, h, images)
			}

			// 4. Video / Live recording cover card (horizontal compact layout, fits 360dp).
			if vidReq, hasVideo := buildFeedVideoRequest(item); hasVideo {
				p.renderPostVideoCard(c, h, item, vidReq)
			}

			// 5. Douyin note music strip.
			if item.Music != nil && (item.Music.Title != "" || item.Music.Author != "") {
				p.renderMusicBar(c, item.Music)
			}

			// 6. Forward / Quoted Origin box.
			if item.Origin != nil {
				p.renderOriginBox(c, h, item.Origin, nil)
			}

			// 7. Non-media rich card summary (read-only, no cache button).
			if item.Card != nil {
				p.renderRichCard(c, h, item.Card)
			}
		})
}

func (p *Page) renderThreadPostCard(
	c *ui.Context,
	h pagekit.Host,
	item companionapi.FeedItem,
	inSubView bool,
	parts []mobileThreadPart,
	focalIdx int,
	origImages []mediaview.ImageItem,
) {
	now := p.nowFn()
	siteTxt := mediaview.SiteLabel(item.Site)
	typeTxt := postTypeLabel(item)
	canOpenSub := !inSubView && item.SubID != ""
	authorName := firstNonEmpty(item.AuthorName, item.AuthorUID, "未知用户")
	authorHandle := strings.TrimPrefix(strings.TrimSpace(item.AuthorUniqueID), "@")

	key := firstNonEmpty(item.FeedKey, item.ItemKey)
	expandKey := "thread:" + key
	p.mu.Lock()
	expanded := p.expandedPosts[expandKey]
	p.mu.Unlock()

	visibleParts := parts
	hiddenCount := 0
	if len(parts) > xThreadMaxInline && !expanded {
		visibleParts = parts[:xThreadCollapsedPreview]
		hiddenCount = len(parts) - xThreadCollapsedPreview
	}

	threadIDs := make([]string, 0, len(parts))
	for _, pt := range parts {
		if id := strings.TrimSpace(pt.ID); id != "" {
			threadIDs = append(threadIDs, id)
		}
	}
	rootReplyHint := formatMobileReplyHint(item.InReplyTo, threadIDs)

	ui.Column(c).
		Label("X自回复串卡片 "+key).
		FillWidth().
		Background(theme.ColorBgMain).
		Padding(14, 14).
		Gap(0).
		BorderWidth(0, 0, 1, 0).
		Border(1, theme.ColorBorder).
		ClipX().
		Children(func() {
			for idx, part := range visibleParts {
				tp := part
				isFirst := idx == 0
				isLastVisible := idx == len(visibleParts)-1
				hasLineBelow := !isLastVisible || hiddenCount > 0
				bottomPad := float32(0)
				if !isLastVisible {
					bottomPad = 12
				} else if hiddenCount > 0 {
					bottomPad = 8
				}

				ui.Row(c.Key(idx)).
					Label(fmt.Sprintf("X串节点 %d", idx+1)).
					FillWidth().
					Gap(10).
					AlignItems(ui.Stretch).
					ClipX().
					Children(func() {
						// Left avatar column (36dp circular avatar + 2px vertical connector when not last).
						ui.Column(c).Width(36).Shrink(0).AlignItems(ui.Center).Children(func() {
							av := ui.Box(c).Shrink(0)
							av.Children(func() {
								p.renderCircleAvatar(c, h, item.AuthorAvatarPath, authorName, item.AuthorUID+authorName, 36)
							})
							if canOpenSub {
								if isFirst {
									av.Label("打开订阅 " + firstNonEmpty(item.SubName, authorName))
								}
								if av.Clicked() {
									p.openSubscription(h, item.SubID, firstNonEmpty(item.SubName, authorName), item.AuthorAvatarPath, item.PubTs)
								}
							}
							if hasLineBelow {
								renderMobileThreadConnector(c, 36, fmt.Sprintf("X串连线 %d", idx+1))
							}
						})

						// Right content column (fits 360dp cleanly via Grow(1).Basis(0).ClipX()).
						ui.Column(c).Grow(1).Basis(0).Gap(6).Padding(0, 0, bottomPad, 0).ClipX().Children(func() {
							if isFirst {
								pubTs := tp.PubTs
								if pubTs <= 0 {
									pubTs = item.PubTs
								}
								header := ui.Column(c).FillWidth().Gap(2).ClipX()
								header.Children(func() {
									ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).ClipX().Children(func() {
										ui.Text(c, authorName).FontSize(14).FontWeight(600).TextColor(theme.ColorTextMain).SingleLine()
										if authorHandle != "" {
											ui.Text(c, "@"+authorHandle).FontSize(12).TextColor(theme.ColorTextMuted).SingleLine().Shrink(1)
										}
										if item.SubName != "" && item.SubName != authorName {
											ui.Text(c, item.SubName).FontSize(12).FontWeight(500).TextColor(theme.ColorAccent).SingleLine().Shrink(1)
										}
									})
									metaParts := make([]string, 0, 3)
									if t := mediaview.FormatPubTime(pubTs, now); t != "" {
										metaParts = append(metaParts, t)
									}
									if siteTxt != "" {
										metaParts = append(metaParts, siteTxt)
									}
									if typeTxt != "" {
										metaParts = append(metaParts, typeTxt)
									}
									if len(metaParts) > 0 {
										ui.Text(c, strings.Join(metaParts, " · ")).FontSize(12).TextColor(theme.ColorTextMuted).SingleLine()
									}
								})
								if canOpenSub && header.Clicked() {
									p.openSubscription(h, item.SubID, firstNonEmpty(item.SubName, authorName), item.AuthorAvatarPath, item.PubTs)
								}
							} else {
								ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).ClipX().Children(func() {
									ui.Text(c, authorName).FontSize(13).FontWeight(600).TextColor(theme.ColorTextMain).SingleLine()
									if authorHandle != "" {
										ui.Text(c, "@"+authorHandle).FontSize(12).TextColor(theme.ColorTextMuted).SingleLine().Shrink(1)
									}
									if t := mediaview.FormatPubTime(tp.PubTs, now); t != "" {
										ui.Text(c, "· "+t).FontSize(12).TextColor(theme.ColorTextMuted).SingleLine().Shrink(0)
									}
								})
							}

							if isFirst && rootReplyHint != "" {
								ui.Text(c, rootReplyHint).FontSize(12).TextColor(theme.ColorTextMuted).SingleLine()
							}
							if body := strings.TrimSpace(tp.Text); body != "" {
								p.renderSegmentText(c, h, fmt.Sprintf("%s:part:%d", key, idx), body, tp.Links)
							}
							if len(tp.Images) > 0 {
								p.renderPostImages(c, h, tp.Images)
							}
							for vi, vid := range tp.Videos {
								p.renderCompactThreadVideoCard(c, h, item.ItemKey, vid, vi)
							}

							if idx == focalIdx {
								if item.Origin != nil {
									p.renderOriginBox(c, h, item.Origin, origImages)
								}
								if item.Card != nil {
									p.renderRichCard(c, h, item.Card)
								}
							} else if tp.Origin != nil && !sameOriginDTO(tp.Origin, item.Origin) {
								p.renderOriginBox(c, h, tp.Origin, nil)
							}
						})
					})
			}

			if hiddenCount > 0 {
				ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).ClipX().Children(func() {
					renderMobileThreadDots(c, 36)
					btn := ui.Button(c, "").
						Label("展开自回复串 "+key).
						Background(ui.Color{}).
						Padding(4, 0).
						AlignSelf(ui.Start)
					btn.Children(func() {
						ui.Text(c, fmt.Sprintf("展开其余 %d 条自回复", hiddenCount)).
							FontSize(13).FontWeight(600).TextColor(theme.ColorAccent).SingleLine()
					})
					if btn.Clicked() {
						p.mu.Lock()
						p.expandedPosts[expandKey] = true
						p.mu.Unlock()
					}
				})
			} else if len(parts) > xThreadMaxInline && expanded {
				ui.Row(c).FillWidth().Padding(6, 0, 0, 46).AlignItems(ui.Center).ClipX().Children(func() {
					btn := ui.Button(c, "").
						Label("收起自回复串 "+key).
						Background(ui.Color{}).
						Padding(2, 0).
						AlignSelf(ui.Start)
					btn.Children(func() {
						ui.Text(c, "收起自回复串").
							FontSize(13).FontWeight(600).TextColor(theme.ColorAccent).SingleLine()
					})
					if btn.Clicked() {
						p.mu.Lock()
						p.expandedPosts[expandKey] = false
						p.mu.Unlock()
					}
				})
			}
		})
}

func renderMobileThreadConnector(c *ui.Context, colWidth float32, label string) {
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
				}, theme.ColorBorderStrong, 1)
			}
		})
	if label != "" {
		box.Label(label)
	}
}

func renderMobileThreadDots(c *ui.Context, colWidth float32) {
	ui.Box(c).
		Width(colWidth).
		Height(20).
		Shrink(0).
		Draw(func(p *ui.Painter, r ui.Rect) {
			cx := r.X + r.W/2
			for i := 0; i < 3; i++ {
				cy := r.Y + 3 + float32(i)*5.5
				p.Fill(ui.Rect{X: cx - 1.5, Y: cy, W: 3, H: 3}, theme.ColorBorderStrong, 1.5)
			}
		})
}

func (p *Page) renderSegmentText(c *ui.Context, h pagekit.Host, expandKey, body string, rawLinks []companionapi.TextLink) {
	p.mu.Lock()
	expanded := p.expandedPosts[expandKey]
	p.mu.Unlock()

	isLong := utf8.RuneCountInString(body) > 180 || strings.Count(body, "\n") >= 6
	el := ui.Text(c, body).FontSize(14).LineHeight(theme.LineHeightBody).TextColor(theme.ColorTextMain)
	if isLong && !expanded {
		el.MaxLines(6)
	}
	if isLong {
		lbl := "展开"
		if expanded {
			lbl = "收起"
		}
		toggleBtn := ui.Button(c, "").
			Label(lbl+"正文").
			Background(ui.Color{}).
			Padding(2, 0).
			AlignSelf(ui.Start)
		toggleBtn.Children(func() {
			ui.Text(c, lbl).FontSize(13).FontWeight(500).TextColor(theme.ColorAccent)
		})
		if toggleBtn.Clicked() {
			p.mu.Lock()
			p.expandedPosts[expandKey] = !expanded
			p.mu.Unlock()
		}
	}
	if links := mediaview.ExtractLinks(body, rawLinks); len(links) > 0 {
		p.renderExternalLinks(c, h, links)
	}
}

func (p *Page) renderCompactThreadVideoCard(c *ui.Context, h pagekit.Host, itemKey string, vid mobileThreadVideo, idx int) {
	var coverSrc ui.ImageSource
	if vid.CoverRelPath != "" {
		coverSrc, _, _ = p.loader.GetOrLoad(h, mediaview.MediaThumbPath(itemKey, vid.CoverRelPath))
	}
	durTxt := mediaview.FormatDuration(vid.DurationSec)
	cardTitle := firstNonEmpty(vid.Title, "X 视频")

	card := ui.Row(c.Key("tv-"+fmt.Sprint(idx))).
		FillWidth().
		Padding(8, 8).
		Gap(10).
		Radius(10).
		Background(theme.ColorBgMain).
		Border(1, theme.ColorBorder).
		AlignItems(ui.Center).
		ClipX().
		Label("播放 " + cardTitle)
	card.Children(func() {
		ui.Box(c).
			Size(112, 63).
			Radius(8).
			Clip().
			Shrink(0).
			Background(theme.ColorBgSecondary).
			Justify(ui.Center).
			AlignItems(ui.Center).
			Children(func() {
				if coverSrc != nil {
					ui.Image(c, coverSrc).Fit(ui.Cover).Absolute().Top(0).Left(0).Right(0).Bottom(0)
				}
				ui.Box(c).
					Size(26, 26).Radius(13).ZIndex(2).
					Background(ui.RGBA(0, 0, 0, 0.55)).
					Justify(ui.Center).AlignItems(ui.Center).
					Children(func() {
						mediaview.DrawIcon(c, "play", 12, theme.ColorWhite, 1.8)
					})
				if durTxt != "" {
					ui.Box(c).
						Absolute().Right(4).Bottom(4).ZIndex(2).
						Padding(1, 4).Radius(4).
						Background(ui.RGBA(0, 0, 0, 0.7)).
						Children(func() {
							ui.Text(c, durTxt).FontSize(10).FontWeight(500).TextColor(theme.ColorWhite).SingleLine()
						})
				}
			})

		ui.Column(c).Grow(1).Basis(0).Gap(3).ClipX().Children(func() {
			ui.Text(c, cardTitle).FontSize(13).FontWeight(600).LineHeight(theme.LineHeightHeading).TextColor(theme.ColorTextMain).MaxLines(2)
			if vid.QualitySpec != "" {
				ui.Text(c, vid.QualitySpec).FontSize(11).TextColor(theme.ColorTextMuted).SingleLine()
			}
		})
	})
	if card.Clicked() {
		p.triggerOpenVideo(h, vid.Req)
	}
}

func extractMobileRawID(id, itemKey string) string {
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

func stripMobileThreadSuffix(base string, conts []companionapi.ThreadPartDTO) string {
	cur := strings.TrimSpace(base)
	if cur == "" || len(conts) == 0 {
		return cur
	}
	for i := len(conts) - 1; i >= 0; i-- {
		seg := strings.TrimSpace(conts[i].Text)
		if seg == "" {
			continue
		}
		if strings.HasSuffix(cur, "\n\n"+seg) {
			cur = strings.TrimSpace(strings.TrimSuffix(cur, "\n\n"+seg))
		}
	}
	return cur
}

func joinMobileThreadTexts(parts []companionapi.ThreadPartDTO) string {
	var segs []string
	for _, tp := range parts {
		if t := strings.TrimSpace(tp.Text); t != "" {
			segs = append(segs, t)
		}
	}
	return strings.Join(segs, "\n\n")
}

func resolveMobileFeedThread(item companionapi.FeedItem) ([]mobileThreadPart, int, []mediaview.ImageItem, bool) {
	if !strings.EqualFold(strings.TrimSpace(item.Site), "x") || len(item.Thread) == 0 {
		return nil, -1, nil, false
	}

	rawID := extractMobileRawID("", item.ItemKey)
	rawSegs := make([]companionapi.ThreadPartDTO, 0, len(item.Thread))
	for _, seg := range item.Thread {
		if strings.TrimSpace(seg.ID) == "" && strings.TrimSpace(seg.Text) == "" && len(seg.Links) == 0 && len(seg.MediaIndexes) == 0 && seg.Origin == nil {
			continue
		}
		rawSegs = append(rawSegs, seg)
	}
	if len(rawSegs) == 0 {
		return nil, -1, nil, false
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

	var segments []companionapi.ThreadPartDTO
	if focalIdx >= 0 {
		if len(rawSegs) < 2 {
			return nil, -1, nil, false
		}
		segments = rawSegs
	} else if len(rawSegs) >= 2 && strings.TrimSpace(item.Text) != "" && strings.TrimSpace(item.Text) == joinMobileThreadTexts(rawSegs) {
		segments = rawSegs
		focalIdx = 0
	} else {
		focalText := stripMobileThreadSuffix(firstNonEmpty(item.Text, item.Desc, item.Title), rawSegs)
		focalSeg := companionapi.ThreadPartDTO{
			ID:    rawID,
			PubTs: item.PubTs,
			Text:  focalText,
			Links: item.Links,
		}
		segments = make([]companionapi.ThreadPartDTO, 0, 1+len(rawSegs))
		segments = append(segments, focalSeg)
		segments = append(segments, rawSegs...)
		focalIdx = 0
	}
	if len(segments) < 2 {
		return nil, -1, nil, false
	}

	hasFromOrigin := false
	for _, np := range item.NotePages {
		if np.FromOrigin {
			hasFromOrigin = true
			break
		}
	}
	if !hasFromOrigin {
		for _, im := range item.Images {
			if im.FromOrigin {
				hasFromOrigin = true
				break
			}
		}
	}

	outerPageByIdx := make(map[int]companionapi.NotePageDTO)
	var outerPageKeys []int
	var origImages []mediaview.ImageItem
	seenOrigImg := make(map[string]bool)
	appendOrigImage := func(img mediaview.ImageItem) {
		if img.RelPath == "" || seenOrigImg[img.RelPath] {
			return
		}
		seenOrigImg[img.RelPath] = true
		origImages = append(origImages, img)
	}
	notePageVideos := make(map[string]bool)

	for i, np := range item.NotePages {
		if np.Video != "" {
			notePageVideos[np.Video] = true
		}
		if hasFromOrigin && np.FromOrigin {
			if rel := firstNonEmpty(np.Image, np.Cover); rel != "" {
				isGIF := np.GIF || strings.EqualFold(np.Kind, "gif")
				motion := np.Motion
				if motion == "" && isGIF && np.Video != "" {
					motion = np.Video
				}
				appendOrigImage(mediaview.ImageItem{
					ItemKey:            item.ItemKey,
					RelPath:            rel,
					NoWatermarkRelPath: np.NoWatermark,
					MotionRelPath:      motion,
					GIF:                isGIF,
					Width:              np.Width,
					Height:             np.Height,
				})
			}
			continue
		}
		idx := np.Index
		if idx <= 0 {
			idx = i + 1
		}
		if _, exists := outerPageByIdx[idx]; !exists {
			outerPageByIdx[idx] = np
			outerPageKeys = append(outerPageKeys, idx)
		}
	}

	var outerImages []mediaview.ImageItem
	if len(item.NotePages) == 0 {
		for _, im := range item.Images {
			if im.Path == "" {
				continue
			}
			img := mediaview.ImageItem{
				ItemKey:            item.ItemKey,
				RelPath:            im.Path,
				NoWatermarkRelPath: im.NoWatermark,
				MotionRelPath:      im.Motion,
				GIF:                im.GIF,
				Width:              im.Width,
				Height:             im.Height,
			}
			if hasFromOrigin && im.FromOrigin {
				appendOrigImage(img)
			} else {
				outerImages = append(outerImages, img)
			}
		}
	}

	zeroBasedImages := false
	if len(outerPageKeys) == 0 && len(outerImages) > 0 {
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

	attachNotePage := func(dst *mobileThreadPart, np companionapi.NotePageDTO) {
		isGIF := np.GIF || strings.EqualFold(np.Kind, "gif")
		if np.Video != "" && !isGIF {
			spec := ""
			codec := ""
			w, h := np.Width, np.Height
			if np.VideoInfo != nil {
				codec = np.VideoInfo.Codec
				spec = strings.TrimSpace(strings.Join(nonEmptyStrings(np.VideoInfo.QualityName, np.VideoInfo.Codec), " · "))
				if w == 0 {
					w = np.VideoInfo.Width
				}
				if h == 0 {
					h = np.VideoInfo.Height
				}
			}
			title := firstNonEmpty(dst.Text, "X 视频")
			dst.Videos = append(dst.Videos, mobileThreadVideo{
				Title:        "X 视频",
				CoverRelPath: firstNonEmpty(np.Cover, np.Image),
				DurationSec:  np.Duration,
				QualitySpec:  firstNonEmpty(spec, item.QualitySummary),
				Req: mediaview.VideoRequest{
					ItemKey: item.ItemKey,
					Title:   title,
					Tracks: []mediaview.VideoTrack{{
						Label:          "X 视频",
						RelPath:        np.Video,
						DurationSec:    np.Duration,
						CodecHint:      firstNonEmpty(codec, item.QualitySummary),
						Width:          w,
						Height:         h,
						QualitySummary: firstNonEmpty(spec, item.QualitySummary),
					}},
				},
			})
			return
		}
		if rel := firstNonEmpty(np.Image, np.Cover); rel != "" {
			motion := np.Motion
			if motion == "" && isGIF && np.Video != "" {
				motion = np.Video
			}
			dst.Images = append(dst.Images, mediaview.ImageItem{
				ItemKey:            item.ItemKey,
				RelPath:            rel,
				NoWatermarkRelPath: np.NoWatermark,
				MotionRelPath:      motion,
				GIF:                isGIF,
				Width:              np.Width,
				Height:             np.Height,
			})
		}
	}

	claimedPages := make(map[int]bool)
	claimedImages := make(map[int]bool)
	parts := make([]mobileThreadPart, len(segments))

	for i, seg := range segments {
		isFocal := i == focalIdx
		pubTs := seg.PubTs
		if pubTs <= 0 && isFocal {
			pubTs = item.PubTs
		}
		links := seg.Links
		if len(links) == 0 && isFocal && len(item.Links) > 0 {
			links = item.Links
		}
		parts[i] = mobileThreadPart{
			ID:      strings.TrimSpace(seg.ID),
			PubTs:   pubTs,
			Text:    strings.TrimSpace(seg.Text),
			Links:   links,
			Origin:  seg.Origin,
			IsFocal: isFocal,
		}
		if len(outerPageKeys) > 0 {
			for _, mi := range seg.MediaIndexes {
				if np, ok := outerPageByIdx[mi]; ok && !claimedPages[mi] {
					attachNotePage(&parts[i], np)
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
					parts[i].Images = append(parts[i].Images, outerImages[imgIdx])
					claimedImages[imgIdx] = true
				}
			}
		}
	}

	// Attach unclaimed outer media to focal.
	if len(outerPageKeys) > 0 {
		for _, idx := range outerPageKeys {
			if !claimedPages[idx] {
				attachNotePage(&parts[focalIdx], outerPageByIdx[idx])
			}
		}
	} else if len(outerImages) > 0 {
		for i, img := range outerImages {
			if !claimedImages[i] {
				parts[focalIdx].Images = append(parts[focalIdx].Images, img)
			}
		}
	}

	// Single non-NotePage focal video (e.g. single-video X post + text-only replies).
	originOwnsVideo := item.Origin != nil && item.Origin.VideoFromOrigin
	if len(outerPageKeys) == 0 && !originOwnsVideo && item.VideoRelPath != "" && !notePageVideos[item.VideoRelPath] {
		parts[focalIdx].Videos = append(parts[focalIdx].Videos, mobileThreadVideo{
			Title:        "X 视频",
			CoverRelPath: item.CoverRelPath,
			DurationSec:  item.Duration,
			QualitySpec:  item.QualitySummary,
			Req: mediaview.VideoRequest{
				ItemKey: item.ItemKey,
				Title:   firstNonEmpty(parts[focalIdx].Text, item.Title, "X 视频"),
				Tracks: []mediaview.VideoTrack{{
					Label:          "X 视频",
					RelPath:        item.VideoRelPath,
					DurationSec:    item.Duration,
					CodecHint:      item.QualitySummary,
					QualitySummary: item.QualitySummary,
				}},
			},
		})
	}

	return parts, focalIdx, origImages, true
}

func (p *Page) renderPostAuthorRow(c *ui.Context, h pagekit.Host, item companionapi.FeedItem, inSubView bool) {
	now := p.nowFn()
	timeTxt := mediaview.FormatPubTime(item.PubTs, now)
	siteTxt := mediaview.SiteLabel(item.Site)
	typeTxt := postTypeLabel(item)

	metaParts := make([]string, 0, 3)
	if timeTxt != "" {
		metaParts = append(metaParts, timeTxt)
	}
	if siteTxt != "" {
		metaParts = append(metaParts, siteTxt)
	}
	if typeTxt != "" {
		metaParts = append(metaParts, typeTxt)
	}

	canOpenSub := !inSubView && item.SubID != ""
	row := ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center)
	row.Children(func() {
		p.renderCircleAvatar(c, h, item.AuthorAvatarPath, item.AuthorName, item.AuthorUID+item.AuthorName, 36)

		ui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
			ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
				name := item.AuthorName
				if name == "" {
					name = item.AuthorUID
				}
				ui.Text(c, name).FontSize(14).FontWeight(600).TextColor(theme.ColorTextMain).SingleLine()
				if item.SubName != "" && item.SubName != item.AuthorName {
					ui.Text(c, item.SubName).FontSize(12).FontWeight(500).TextColor(theme.ColorAccent).SingleLine()
				}
			})
			if len(metaParts) > 0 {
				ui.Text(c, strings.Join(metaParts, " · ")).FontSize(12).TextColor(theme.ColorTextMuted).SingleLine()
			}
		})
	})
	if canOpenSub {
		row.Label("打开订阅 " + firstNonEmpty(item.SubName, item.AuthorName))
		if row.Clicked() {
			p.openSubscription(h, item.SubID, firstNonEmpty(item.SubName, item.AuthorName), item.AuthorAvatarPath, item.PubTs)
		}
	}
}

func (p *Page) renderCircleAvatar(c *ui.Context, h pagekit.Host, avatarPath, name, seed string, size float32) {
	var src ui.ImageSource
	if avatarPath != "" {
		src, _, _ = p.loader.GetOrLoad(h, avatarPath)
	}
	ui.Box(c).
		Size(size, size).
		Radius(size / 2).
		Clip().
		Shrink(0).
		Background(mediaview.AvatarPlaceholderColor(seed)).
		Justify(ui.Center).
		AlignItems(ui.Center).
		Children(func() {
			if src != nil {
				ui.Image(c, src).Fit(ui.Cover).Fill()
			} else {
				ui.Text(c, mediaview.AvatarInitial(name)).FontSize(size * 0.42).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()
			}
		})
}

func (p *Page) renderPostText(c *ui.Context, h pagekit.Host, item companionapi.FeedItem) {
	title := strings.TrimSpace(item.Title)
	body := strings.TrimSpace(item.Text)
	if body == "" {
		body = strings.TrimSpace(item.Desc)
	}
	// Avoid duplicating title when video card already displays it, or when body equals title.
	_, hasVideo := buildFeedVideoRequest(item)
	if title != "" && title != body && !(hasVideo && item.Kind == "video") {
		ui.Text(c, title).FontSize(15).FontWeight(600).LineHeight(theme.LineHeightHeading).TextColor(theme.ColorTextMain).MaxLines(3)
	}

	if body != "" {
		key := firstNonEmpty(item.FeedKey, item.ItemKey)
		p.mu.Lock()
		expanded := p.expandedPosts[key]
		p.mu.Unlock()

		isLong := utf8.RuneCountInString(body) > 180 || strings.Count(body, "\n") >= 6
		el := ui.Text(c, body).FontSize(14).LineHeight(theme.LineHeightBody).TextColor(theme.ColorTextMain)
		if isLong && !expanded {
			el.MaxLines(6)
		}
		if isLong {
			lbl := "展开"
			if expanded {
				lbl = "收起"
			}
			toggleBtn := ui.Button(c, "").
				Label(lbl+"正文").
				Background(ui.Color{}).
				Padding(2, 0).
				AlignSelf(ui.Start)
			toggleBtn.Children(func() {
				ui.Text(c, lbl).FontSize(13).FontWeight(500).TextColor(theme.ColorAccent)
			})
			if toggleBtn.Clicked() {
				p.mu.Lock()
				p.expandedPosts[key] = !expanded
				p.mu.Unlock()
			}
		}
	}

	links := mediaview.ExtractLinks(body, item.Links)
	if len(links) > 0 {
		p.renderExternalLinks(c, h, links)
	}
}

func (p *Page) renderExternalLinks(c *ui.Context, h pagekit.Host, links []companionapi.TextLink) {
	ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
		for i, lnk := range links {
			u := lnk.URL
			btn := ui.Button(c.Key(i), "").
				Label("打开链接 "+lnk.Label).
				MinHeight(32).
				Padding(4, 10).
				Radius(999).
				Background(theme.ColorAccentBg).
				Justify(ui.Center).
				AlignItems(ui.Center)
			btn.Children(func() {
				ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
					mediaview.DrawIcon(c, "external-link", 12, theme.ColorAccent, 1.8)
					ui.Text(c, lnk.Label).FontSize(12).FontWeight(500).TextColor(theme.ColorAccent).SingleLine()
				})
			})
			if btn.Clicked() {
				_ = h.OpenExternal(u)
			}
		}
	})
}

func (p *Page) renderPostImages(c *ui.Context, h pagekit.Host, images []mediaview.ImageItem) {
	if len(images) == 1 {
		img := images[0]
		aspect := float32(16.0 / 9.0)
		if img.Width > 0 && img.Height > 0 {
			aspect = float32(img.Width) / float32(img.Height)
			if aspect < 0.5 {
				aspect = 0.5
			}
			if aspect > 2.4 {
				aspect = 2.4
			}
		}
		tile := ui.Box(c).
			FillWidth().
			MaxHeight(320).
			AspectRatio(aspect).
			Radius(12).
			Clip().
			Background(theme.ColorBgSecondary).
			Label("查看图片 1 / 1")
		tile.Children(func() {
			p.renderImageTileContent(c, h, img, 0)
		})
		if tile.Clicked() {
			p.triggerOpenImages(h, images, 0)
		}
		return
	}

	cols := 3
	if len(images) == 2 || len(images) == 4 {
		cols = 2
	}
	shown := len(images)
	overflow := 0
	if shown > 9 {
		overflow = shown - 9
		shown = 9
	}

	ui.Grid(c).FillWidth().Columns(cols).Gap(4).Children(func() {
		for i := 0; i < shown; i++ {
			idx := i
			img := images[idx]
			extra := 0
			if idx == shown-1 && overflow > 0 {
				extra = overflow
			}
			tile := ui.Box(c.Key(idx)).
				AspectRatio(1).
				Radius(8).
				Clip().
				Background(theme.ColorBgSecondary).
				Label(fmt.Sprintf("查看图片 %d / %d", idx+1, len(images)))
			tile.Children(func() {
				p.renderImageTileContent(c, h, img, extra)
			})
			if tile.Clicked() {
				p.triggerOpenImages(h, images, idx)
			}
		}
	})
}

func (p *Page) renderImageTileContent(c *ui.Context, h pagekit.Host, img mediaview.ImageItem, extraOverflow int) {
	thumbPath := mediaview.MediaThumbPath(img.ItemKey, img.RelPath)
	src, _, _ := p.loader.GetOrLoad(h, thumbPath)
	if src != nil {
		ui.Image(c, src).Fit(ui.Cover).Fill()
	}
	if img.MotionRelPath != "" && !img.GIF {
		ui.Box(c).
			Absolute().Top(6).Left(6).ZIndex(2).
			Padding(2, 6).Radius(4).
			Background(ui.RGBA(0, 0, 0, 0.62)).
			Children(func() {
				ui.Text(c, "实况").FontSize(10).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()
			})
	} else if img.GIF {
		ui.Box(c).
			Absolute().Top(6).Left(6).ZIndex(2).
			Padding(2, 6).Radius(4).
			Background(ui.RGBA(0, 0, 0, 0.62)).
			Children(func() {
				ui.Text(c, "GIF").FontSize(10).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()
			})
	}
	if extraOverflow > 0 {
		ui.Box(c).
			Absolute().Top(0).Left(0).Right(0).Bottom(0).ZIndex(3).
			Background(ui.RGBA(0, 0, 0, 0.48)).
			Justify(ui.Center).
			AlignItems(ui.Center).
			Children(func() {
				ui.Text(c, fmt.Sprintf("+%d", extraOverflow)).FontSize(20).FontWeight(700).TextColor(theme.ColorWhite)
			})
	}
}

func (p *Page) renderPostVideoCard(c *ui.Context, h pagekit.Host, item companionapi.FeedItem, req mediaview.VideoRequest) {
	coverPath := ""
	if item.CoverRelPath != "" {
		coverPath = mediaview.MediaThumbPath(item.ItemKey, item.CoverRelPath)
	}
	var coverSrc ui.ImageSource
	if coverPath != "" {
		coverSrc, _, _ = p.loader.GetOrLoad(h, coverPath)
	}
	durTxt := mediaview.FormatDuration(item.Duration)
	if durTxt == "" && len(req.Tracks) > 0 {
		durTxt = mediaview.FormatDuration(req.Tracks[0].DurationSec)
	}

	cardTitle := firstNonEmpty(item.Title, item.Text, "视频")
	if item.Kind == "live" && cardTitle == "视频" {
		cardTitle = "直播录像"
	}

	card := ui.Row(c).
		FillWidth().
		Padding(8, 8).
		Gap(10).
		Radius(12).
		Background(theme.ColorBgMain).
		Border(1, theme.ColorBorder).
		AlignItems(ui.Center).
		Label("播放 " + cardTitle)
	card.Children(func() {
		// Left 16:9 thumbnail (compact 132x74 so 360dp phone never overflows).
		ui.Box(c).
			Size(132, 74).
			Radius(8).
			Clip().
			Shrink(0).
			Background(theme.ColorBgSecondary).
			Justify(ui.Center).
			AlignItems(ui.Center).
			Children(func() {
				if coverSrc != nil {
					ui.Image(c, coverSrc).Fit(ui.Cover).Absolute().Top(0).Left(0).Right(0).Bottom(0)
				}
				ui.Box(c).
					Size(30, 30).Radius(15).ZIndex(2).
					Background(ui.RGBA(0, 0, 0, 0.55)).
					Justify(ui.Center).AlignItems(ui.Center).
					Children(func() {
						mediaview.DrawIcon(c, "play", 14, theme.ColorWhite, 1.8)
					})
				if durTxt != "" {
					ui.Box(c).
						Absolute().Right(5).Bottom(5).ZIndex(2).
						Padding(2, 5).Radius(4).
						Background(ui.RGBA(0, 0, 0, 0.7)).
						Children(func() {
							ui.Text(c, durTxt).FontSize(10).FontWeight(500).TextColor(theme.ColorWhite).SingleLine()
						})
				}
			})

		ui.Column(c).Grow(1).Basis(0).Gap(4).Children(func() {
			ui.Text(c, cardTitle).FontSize(14).FontWeight(600).LineHeight(theme.LineHeightHeading).TextColor(theme.ColorTextMain).MaxLines(2)
			if desc := strings.TrimSpace(item.Desc); desc != "" && desc != cardTitle {
				ui.Text(c, desc).FontSize(12).LineHeight(theme.LineHeightCompact).TextColor(theme.ColorTextMuted).MaxLines(2)
			}
			specParts := make([]string, 0, 3)
			if item.Kind == "live" {
				specParts = append(specParts, "直播录像")
			}
			if len(req.Tracks) > 1 {
				if item.Kind == "live" {
					specParts = append(specParts, fmt.Sprintf("%d 段", len(req.Tracks)))
				} else {
					specParts = append(specParts, fmt.Sprintf("%d P", len(req.Tracks)))
				}
			}
			if item.QualitySummary != "" {
				specParts = append(specParts, item.QualitySummary)
			}
			if len(specParts) > 0 {
				ui.Text(c, strings.Join(specParts, " · ")).FontSize(11).TextColor(theme.ColorTextMuted).SingleLine()
			}
		})
	})
	if card.Clicked() {
		p.triggerOpenVideo(h, req)
	}
}

func (p *Page) renderMusicBar(c *ui.Context, m *companionapi.MusicDTO) {
	label := "♪ " + m.Title
	if m.Author != "" {
		if m.Title != "" {
			label += " · " + m.Author
		} else {
			label = "♪ " + m.Author
		}
	}
	ui.Row(c).FillWidth().Padding(6, 10).Radius(8).Background(theme.ColorBgSecondary).Children(func() {
		ui.Text(c, label).FontSize(12).TextColor(theme.ColorTextMuted).SingleLine()
	})
}

func formatMobileReplyHint(target *companionapi.ItemReplyTargetDTO, threadIDs []string) string {
	if target == nil {
		return ""
	}
	sid := strings.TrimSpace(target.StatusID)
	if sid != "" {
		for _, id := range threadIDs {
			if id == sid {
				return ""
			}
		}
	}
	handle := strings.TrimPrefix(strings.TrimSpace(target.ScreenName), "@")
	if handle != "" {
		return "回复 @" + handle
	}
	if sid != "" || strings.TrimSpace(target.UserID) != "" {
		return "回复一条帖子"
	}
	return ""
}

func sameOriginDTO(a, b *companionapi.ItemOriginDTO) bool {
	if a == nil || b == nil {
		return false
	}
	return strings.TrimSpace(a.ID) != "" && strings.TrimSpace(a.ID) == strings.TrimSpace(b.ID)
}

func (p *Page) renderOriginBox(c *ui.Context, h pagekit.Host, orig *companionapi.ItemOriginDTO, origImages []mediaview.ImageItem) {
	p.renderOriginBoxDepth(c, h, orig, origImages, 0)
}

func (p *Page) renderOriginBoxDepth(c *ui.Context, h pagekit.Host, orig *companionapi.ItemOriginDTO, origImages []mediaview.ImageItem, depth int) {
	if orig == nil {
		return
	}
	bg := theme.ColorBgMain
	padV, padH := float32(10), float32(12)
	radius := float32(10)
	if depth > 0 {
		bg = theme.ColorBgPanel
		padV, padH = 8, 10
		radius = 8
	}
	ui.Column(c).
		FillWidth().
		Padding(padV, padH).
		Radius(radius).
		Background(bg).
		Border(1, theme.ColorBorder).
		Gap(6).
		ClipX().
		Children(func() {
			if orig.Unavailable {
				reason := firstNonEmpty(orig.UnavailableReason, "原内容已删除或不可见")
				ui.Text(c, reason).FontSize(13).TextColor(theme.ColorTextMuted)
				return
			}
			authorMeta := strings.TrimSpace(orig.AuthorName)
			if handle := strings.TrimPrefix(strings.TrimSpace(orig.AuthorUniqueID), "@"); handle != "" {
				if authorMeta != "" {
					authorMeta += " @" + handle
				} else {
					authorMeta = "@" + handle
				}
			}
			if authorMeta != "" {
				if orig.Site != "" && depth == 0 {
					authorMeta += " · " + mediaview.SiteLabel(orig.Site)
				}
				ui.Text(c, authorMeta).FontSize(12).FontWeight(600).TextColor(theme.ColorTextMuted).SingleLine()
			}
			if replyHint := formatMobileReplyHint(orig.InReplyTo, nil); replyHint != "" {
				ui.Text(c, replyHint).FontSize(11).TextColor(theme.ColorTextMuted).SingleLine()
			}
			if orig.Title != "" && orig.Title != orig.Text {
				ui.Text(c, orig.Title).FontSize(13).FontWeight(600).TextColor(theme.ColorTextMain).MaxLines(2)
			}
			if orig.Text != "" {
				maxLines := 6
				if depth > 0 {
					maxLines = 4
				}
				ui.Text(c, orig.Text).FontSize(13).LineHeight(theme.LineHeightBody).TextColor(theme.ColorTextMain).MaxLines(maxLines)
			}
			if depth == 0 {
				if links := mediaview.ExtractLinks(orig.Text, orig.Links); len(links) > 0 {
					p.renderExternalLinks(c, h, links)
				}
				if len(origImages) > 0 {
					p.renderPostImages(c, h, origImages)
				}
				if orig.Card != nil {
					p.renderRichCard(c, h, orig.Card)
				}
				if orig.Origin != nil {
					p.renderOriginBoxDepth(c, h, orig.Origin, nil, depth+1)
				}
			}
		})
}

func (p *Page) renderRichCard(c *ui.Context, h pagekit.Host, card *companionapi.ItemRichCardDTO) {
	ui.Column(c).
		FillWidth().
		Padding(10, 12).
		Radius(10).
		Background(theme.ColorBgPanel).
		Border(1, theme.ColorBorder).
		Gap(4).
		Children(func() {
			if card.Label != "" {
				ui.Text(c, card.Label).FontSize(11).FontWeight(600).TextColor(theme.ColorTextMuted).SingleLine()
			}
			if card.Title != "" {
				ui.Text(c, card.Title).FontSize(13).FontWeight(600).TextColor(theme.ColorTextMain).MaxLines(2)
			}
			if card.Desc != "" {
				ui.Text(c, card.Desc).FontSize(12).LineHeight(theme.LineHeightCompact).TextColor(theme.ColorTextMuted).MaxLines(2)
			}
			if mediaview.IsSafeExternalURL(card.URL) {
				u := card.URL
				btn := ui.Button(c, "").
					Label("打开外链 "+firstNonEmpty(card.Title, card.Label)).
					Background(ui.Color{}).Padding(2, 0).AlignSelf(ui.Start)
				btn.Children(func() {
					ui.Text(c, "在浏览器中打开").FontSize(12).FontWeight(500).TextColor(theme.ColorAccent)
				})
				if btn.Clicked() {
					_ = h.OpenExternal(u)
				}
			}
		})
}

func (p *Page) triggerOpenImages(h pagekit.Host, items []mediaview.ImageItem, idx int) {
	if p.openImageFn != nil {
		p.openImageFn(h, items, idx)
		return
	}
	mediaview.OpenImageViewer(h, items, idx)
}

func (p *Page) triggerOpenVideo(h pagekit.Host, req mediaview.VideoRequest) {
	if p.openVideoFn != nil {
		p.openVideoFn(h, req)
		return
	}
	mediaview.OpenVideoPlayer(h, req)
}

func postTypeLabel(item companionapi.FeedItem) string {
	if item.TypeLabel != "" {
		return item.TypeLabel
	}
	if item.IsForward {
		return "转发"
	}
	switch item.Kind {
	case "live":
		return "直播录像"
	case "video":
		return "视频"
	case "dynamic":
		return "动态"
	default:
		return ""
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
