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

func (p *Page) renderPostCard(c *ui.Context, h pagekit.Host, item companionapi.FeedItem, inSubView bool) {
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

			// 6. Author self-reply thread summary (X).
			if len(item.Thread) > 0 {
				p.renderThreadParts(c, h, item.Thread)
			}

			// 7. Forward / Quoted Origin box.
			if item.Origin != nil {
				p.renderOriginBox(c, h, item.Origin)
			}

			// 8. Non-media rich card summary (read-only, no cache button).
			if item.Card != nil {
				p.renderRichCard(c, h, item.Card)
			}
		})
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
	if img.MotionRelPath != "" {
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

func (p *Page) renderThreadParts(c *ui.Context, h pagekit.Host, thread []companionapi.ThreadPartDTO) {
	ui.Column(c).FillWidth().Gap(6).Padding(0, 0).Children(func() {
		for i, part := range thread {
			if strings.TrimSpace(part.Text) == "" {
				continue
			}
			ui.Column(c.Key(i)).
				FillWidth().
				Padding(8, 10).
				Radius(8).
				Background(theme.ColorBgPanel).
				Border(1, theme.ColorBorder).
				Gap(4).
				Children(func() {
					ui.Text(c, part.Text).FontSize(13).LineHeight(theme.LineHeightBody).TextColor(theme.ColorTextMain).MaxLines(6)
					if links := mediaview.ExtractLinks(part.Text, part.Links); len(links) > 0 {
						p.renderExternalLinks(c, h, links)
					}
				})
		}
	})
}

func (p *Page) renderOriginBox(c *ui.Context, h pagekit.Host, orig *companionapi.ItemOriginDTO) {
	ui.Column(c).
		FillWidth().
		Padding(10, 12).
		Radius(10).
		Background(theme.ColorBgMain).
		Border(1, theme.ColorBorder).
		Gap(6).
		Children(func() {
			if orig.Unavailable {
				reason := firstNonEmpty(orig.UnavailableReason, "原内容已删除或不可见")
				ui.Text(c, reason).FontSize(13).TextColor(theme.ColorTextMuted)
				return
			}
			if orig.AuthorName != "" {
				meta := orig.AuthorName
				if orig.Site != "" {
					meta += " · " + mediaview.SiteLabel(orig.Site)
				}
				ui.Text(c, meta).FontSize(12).FontWeight(600).TextColor(theme.ColorTextMuted).SingleLine()
			}
			if orig.Title != "" && orig.Title != orig.Text {
				ui.Text(c, orig.Title).FontSize(13).FontWeight(600).TextColor(theme.ColorTextMain).MaxLines(2)
			}
			if orig.Text != "" {
				ui.Text(c, orig.Text).FontSize(13).LineHeight(theme.LineHeightBody).TextColor(theme.ColorTextMain).MaxLines(6)
			}
			if links := mediaview.ExtractLinks(orig.Text, orig.Links); len(links) > 0 {
				p.renderExternalLinks(c, h, links)
			}
			if orig.Card != nil {
				p.renderRichCard(c, h, orig.Card)
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
