package library

import (
	"fmt"
	"strings"

	"github.com/chialecode/trace/cmd/trace-mobile/internal/mediaview"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/pagekit"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/theme"
	"github.com/chialecode/trace/internal/companionapi"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/mobile"
)

func (p *Page) renderDetailRoute(c *ui.Context, h pagekit.Host, r *ui.Router, route *ui.Route, itemKey string) {
	p.mu.Lock()
	if itemKey != "" && p.detailKey != itemKey {
		p.detailKey = itemKey
		p.detail = nil
		p.detailEditHistoryCollapsed = false
		gen := p.gen
		p.mu.Unlock()
		p.fetchDetail(h, itemKey, gen)
		p.mu.Lock()
	}
	det := p.detail
	loading := p.detailLoading
	errMsg := p.detailErr
	gen := p.gen
	p.mu.Unlock()

	pageTitle := "内容详情"
	if det != nil && det.Kind == "live" {
		pageTitle = "直播录像详情"
	}
	route.Title(pageTitle)

	ui.Column(c).Fill().Background(theme.ColorBgMain).Children(func() {
		if !r.InSystemStack(c) {
			mobile.TopBar(c, mobile.TopBarOptions{
				Title: pageTitle,
				Leading: func() {
					backBtn := ui.Button(c, "").
						Label("返回媒体库").
						Size(44, 44).Radius(22).
						Background(ui.Color{}).
						Justify(ui.Center).AlignItems(ui.Center)
					backBtn.Children(func() {
						theme.DrawIcon(c, "arrow-left", 20, theme.ColorTextMain, 1.8)
					})
					if backBtn.Clicked() {
						h.Back()
					}
				},
			})
		}

		switch {
		case loading && det == nil:
			ui.Column(c).Grow(1).Basis(0).FillWidth().Padding(16, 16).Gap(12).Children(func() {
				ui.Box(c).FillWidth().Height(22).Radius(6).Background(theme.ColorBgSecondary)
				ui.Box(c).WidthPercent(60).Height(14).Radius(5).Background(theme.ColorBgSecondary)
				ui.Box(c).FillWidth().AspectRatio(16.0 / 9.0).Radius(12).Background(theme.ColorBgSecondary)
			})
		case errMsg != "" && det == nil:
			ui.Column(c).Grow(1).Basis(0).FillWidth().Justify(ui.Center).AlignItems(ui.Center).Padding(32, 24).Gap(12).Children(func() {
				theme.DrawIcon(c, "alert-circle", 32, theme.ColorError, 1.7)
				ui.Text(c, errMsg).FontSize(14).TextColor(theme.ColorTextMain).TextAlign(ui.Center)
				if theme.PrimaryPillButton(c, "重试", "重试加载详情").Clicked() {
					p.fetchDetail(h, itemKey, gen)
				}
			})
		case det == nil:
			ui.Column(c).Grow(1).Basis(0).FillWidth().Justify(ui.Center).AlignItems(ui.Center).Padding(32, 24).Children(func() {
				ui.Text(c, "找不到该条目").FontSize(15).TextColor(theme.ColorTextMuted)
			})
		default:
			p.renderDetailBody(c, h, *det)
		}
	})
}

func (p *Page) renderDetailBody(c *ui.Context, h pagekit.Host, det companionapi.LibraryItemDetail) {
	if parts, focalIdx, origImages, ok := resolveMobileDetailThread(det); ok {
		p.renderDetailThreadBody(c, h, det, parts, focalIdx, origImages)
		return
	}

	images := collectDetailImages(det)
	vidReq, indexMap, hasVideo := buildDetailVideoRequest(det)

	ui.Scroll(c).Grow(1).Basis(0).FillWidth().Children(func() {
		ui.Column(c).FillWidth().Padding(16, 16).Gap(14).Children(func() {
			// Read-only offline badge + forward attribution.
			if det.IsForward && det.Via != nil && det.Via.Name != "" && det.Via.UID != det.AuthorUID {
				ui.Row(c).FillWidth().Padding(6, 10).Radius(8).Background(theme.ColorBgPanel).Children(func() {
					ui.Text(c, "转发内容 · 由 "+det.Via.Name+" 转发").FontSize(12).TextColor(theme.ColorTextMuted).SingleLine()
				})
			}

			// Author row (zero volatile counts, read-only).
			ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
				p.renderDetailAvatar(c, h, det.AuthorAvatarPath, det.AuthorName, det.AuthorUID+det.AuthorName)
				ui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
					ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
						name := det.AuthorName
						if name == "" {
							name = det.AuthorUID
						}
						ui.Text(c, name).FontSize(15).FontWeight(600).TextColor(theme.ColorTextMain).SingleLine()
						if det.SubName != "" && det.SubName != det.AuthorName {
							ui.Text(c, det.SubName).FontSize(12).FontWeight(500).TextColor(theme.ColorAccent).SingleLine()
						}
					})
					meta := make([]string, 0, 4)
					if det.AuthorUniqueID != "" {
						meta = append(meta, "@"+det.AuthorUniqueID)
					}
					if s := mediaview.SiteLabel(det.Site); s != "" {
						meta = append(meta, s)
					}
					if t := mediaview.FormatPubTime(det.PubTs, p.nowFn()); t != "" {
						meta = append(meta, t)
					}
					if det.EditCount > 0 {
						meta = append(meta, fmt.Sprintf("已编辑 %d 次", det.EditCount))
					}
					if sz := mediaview.FormatBytes(det.TotalBytes); sz != "" {
						meta = append(meta, sz)
					}
					if len(meta) > 0 {
						ui.Text(c, strings.Join(meta, " · ")).FontSize(12).TextColor(theme.ColorTextMuted).SingleLine()
					}
				})
			})

			// Concise reply context hint above body when replying to another post (DEC-091).
			if hint := formatDetailReplyHint(det.InReplyTo, nil); hint != "" {
				ui.Text(c, hint).FontSize(12).TextColor(theme.ColorTextMuted).SingleLine()
			}

			// Full offline X Article rendering (DEC-093): strictly zero network requests.
			hasArticle := det.Article != nil && (strings.TrimSpace(det.Article.Title) != "" || len(det.Article.Blocks) > 0 || strings.TrimSpace(det.Article.PlainText) != "")
			if hasArticle {
				p.renderDetailArticleSection(c, h, det)
			} else {
				// Title.
				if title := strings.TrimSpace(det.Title); title != "" {
					ui.Text(c, title).FontSize(18).FontWeight(600).LineHeight(theme.LineHeightHeading).TextColor(theme.ColorTextMain)
				}

				// Body text & description.
				body := strings.TrimSpace(det.Text)
				if body != "" && body != strings.TrimSpace(det.Title) {
					ui.Text(c, body).FontSize(14).LineHeight(theme.LineHeightBody).TextColor(theme.ColorTextMain)
				}
				if desc := strings.TrimSpace(det.Desc); desc != "" && desc != body && desc != strings.TrimSpace(det.Title) {
					ui.Text(c, desc).FontSize(13).LineHeight(theme.LineHeightBody).TextColor(theme.ColorTextMuted)
				}

				// Clickable http(s) links.
				if links := mediaview.ExtractLinks(body+"\n"+det.Desc, det.Links); len(links) > 0 {
					p.renderDetailLinks(c, h, links)
				}
			}

			// Video cover / play CTA when playable.
			if hasVideo {
				p.renderDetailVideoCard(c, h, det, vidReq)
			}

			// Multi-P list (tap any cached P to play in-place; uncached parts are disabled).
			if len(det.Parts) > 1 {
				ui.Column(c).FillWidth().Gap(8).Children(func() {
					ui.Text(c, fmt.Sprintf("分 P 列表（%d）", len(det.Parts))).FontSize(14).FontWeight(600).TextColor(theme.ColorTextMain)
					for i, part := range det.Parts {
						trackIdx, playable := indexMap[i]
						if !hasVideo || part.File == "" {
							playable = false
						}
						lbl := fmt.Sprintf("P%d", part.Page)
						if part.Title != "" {
							lbl += " · " + part.Title
						}
						meta := make([]string, 0, 3)
						if d := mediaview.FormatDuration(part.Duration); d != "" {
							meta = append(meta, d)
						}
						if part.QualityName != "" {
							meta = append(meta, part.QualityName)
						}
						if part.Codecs != "" {
							meta = append(meta, part.Codecs)
						}
						if !playable {
							meta = append(meta, "未缓存")
						}
						titleCol := theme.ColorTextMain
						if !playable {
							titleCol = theme.ColorTextMuted
						}
						rowBtn := ui.Button(c.Key("part-"+fmt.Sprint(i)), "").
							Label("播放 "+lbl).
							FillWidth().MinHeight(46).Padding(10, 12).Radius(10).
							Background(theme.ColorBgPanel).Border(1, theme.ColorBorder).
							Justify(ui.Start).AlignItems(ui.Center).
							Disabled(!playable)
						rowBtn.Children(func() {
							ui.Column(c).FillWidth().Gap(2).Children(func() {
								ui.Text(c, lbl).FontSize(13).FontWeight(500).TextColor(titleCol).SingleLine()
								if len(meta) > 0 {
									ui.Text(c, strings.Join(meta, " · ")).FontSize(11).TextColor(theme.ColorTextMuted).SingleLine()
								}
							})
						})
						if playable && rowBtn.Clicked() {
							req := vidReq
							req.StartIndex = trackIdx
							p.triggerOpenVideo(h, req)
						}
					}
				})
			}

			// Live recording info & segments list (tap any cached segment to play sequentially from there).
			if det.Live != nil {
				p.renderDetailLiveSection(c, h, det, vidReq, indexMap, hasVideo)
			}

			// Images gallery (tap any image to open ImageViewer; skipped for articles whose inline images render in place).
			if !hasArticle && len(images) > 0 {
				ui.Column(c).FillWidth().Gap(8).Children(func() {
					ui.Text(c, fmt.Sprintf("图片（%d）", len(images))).FontSize(14).FontWeight(600).TextColor(theme.ColorTextMain)
					p.renderDetailImageGrid(c, h, images)
				})
			}

			// Music bar.
			if det.Music != nil && (det.Music.Title != "" || det.Music.Author != "") {
				lbl := "♪ " + strings.TrimSpace(det.Music.Title)
				if det.Music.Author != "" {
					if det.Music.Title != "" {
						lbl += " · " + det.Music.Author
					} else {
						lbl = "♪ " + det.Music.Author
					}
				}
				ui.Row(c).FillWidth().Padding(8, 12).Radius(8).Background(theme.ColorBgSecondary).Children(func() {
					ui.Text(c, lbl).FontSize(12).TextColor(theme.ColorTextMuted).SingleLine()
				})
			}

			// Origin quoted post.
			if det.Origin != nil {
				p.renderDetailOriginBox(c, h, det.Origin, nil)
			}

			// Rich card summary (read-only; skip redundant degraded article card when full Article is cached).
			if det.Card != nil && !(hasArticle && det.Card.Kind == "article") {
				p.renderDetailRichCard(c, det.Card)
			}

			// Cached edit history revisions (zero external network requests, DEC-094).
			if len(det.EditHistory) > 0 {
				p.renderDetailEditHistorySection(c, h, det)
			}

			// Missing parts note (honest partial status).
			if len(det.Missing) > 0 {
				p.renderDetailMissingBox(c, det.Missing)
			}

			// Bottom read-only footer note (FR-MOB-025).
			ui.Text(c, "本机只读详情 · 删除或管理文件请在电脑端 TRACE 中操作").
				FontSize(12).TextColor(theme.ColorTextFaint).TextAlign(ui.Center).FillWidth()
		})
	})
}

type detailThreadVideo struct {
	Title        string
	CoverRelPath string
	DurationSec  int
	QualitySpec  string
	Req          mediaview.VideoRequest
}

type detailThreadPart struct {
	ID      string
	PubTs   int64
	Text    string
	Links   []companionapi.TextLink
	Images  []mediaview.ImageItem
	Videos  []detailThreadVideo
	Origin  *companionapi.ItemOriginDTO
	IsFocal bool
}

func (p *Page) renderDetailThreadBody(
	c *ui.Context,
	h pagekit.Host,
	det companionapi.LibraryItemDetail,
	parts []detailThreadPart,
	focalIdx int,
	origImages []mediaview.ImageItem,
) {
	now := p.nowFn()
	authorName := strings.TrimSpace(det.AuthorName)
	if authorName == "" {
		authorName = strings.TrimSpace(det.AuthorUID)
	}
	if authorName == "" {
		authorName = "未知用户"
	}
	authorHandle := strings.TrimPrefix(strings.TrimSpace(det.AuthorUniqueID), "@")

	threadIDs := make([]string, 0, len(parts))
	for _, pt := range parts {
		if id := strings.TrimSpace(pt.ID); id != "" {
			threadIDs = append(threadIDs, id)
		}
	}
	rootReplyHint := formatDetailReplyHint(det.InReplyTo, threadIDs)

	ui.Scroll(c).Grow(1).Basis(0).FillWidth().Children(func() {
		ui.Column(c).FillWidth().Padding(14, 14).Gap(12).ClipX().Children(func() {
			if det.IsForward && det.Via != nil && det.Via.Name != "" && det.Via.UID != det.AuthorUID {
				ui.Row(c).FillWidth().Padding(6, 10).Radius(8).Background(theme.ColorBgPanel).Children(func() {
					ui.Text(c, "转发内容 · 由 "+det.Via.Name+" 转发").FontSize(12).TextColor(theme.ColorTextMuted).SingleLine()
				})
			}

			ui.Column(c).Label("X离线自回复串").FillWidth().Gap(0).ClipX().Children(func() {
				for idx, part := range parts {
					tp := part
					isFirst := idx == 0
					isLast := idx == len(parts)-1
					bottomPad := float32(0)
					if !isLast {
						bottomPad = 14
					}

					ui.Row(c.Key(idx)).
						Label(fmt.Sprintf("X离线串条目 %d", idx+1)).
						FillWidth().
						Gap(10).
						AlignItems(ui.Stretch).
						ClipX().
						Children(func() {
							ui.Column(c).Width(36).Shrink(0).AlignItems(ui.Center).Children(func() {
								p.renderDetailAvatarSized(c, h, det.AuthorAvatarPath, authorName, det.AuthorUID+authorName, 36)
								if !isLast {
									ui.Box(c).
										Label(fmt.Sprintf("X离线串连线 %d", idx+1)).
										Width(36).
										Grow(1).
										MinHeight(14).
										Shrink(0).
										Draw(func(painter *ui.Painter, r ui.Rect) {
											lineTop := r.Y + 4
											lineH := r.H - 8
											if lineH > 2 {
												painter.Fill(ui.Rect{
													X: r.X + (r.W-2)/2,
													Y: lineTop,
													W: 2,
													H: lineH,
												}, theme.ColorBorderStrong, 1)
											}
										})
								}
							})

							ui.Column(c).Grow(1).Basis(0).Gap(8).Padding(0, 0, bottomPad, 0).ClipX().Children(func() {
								if isFirst {
									ui.Column(c).FillWidth().Gap(2).ClipX().Children(func() {
										ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).ClipX().Children(func() {
											ui.Text(c, authorName).FontSize(15).FontWeight(600).TextColor(theme.ColorTextMain).SingleLine()
											if authorHandle != "" {
												ui.Text(c, "@"+authorHandle).FontSize(12).TextColor(theme.ColorTextMuted).SingleLine().Shrink(1)
											}
											if det.SubName != "" && det.SubName != authorName {
												ui.Text(c, det.SubName).FontSize(12).FontWeight(500).TextColor(theme.ColorAccent).SingleLine().Shrink(1)
											}
										})
										pubTs := tp.PubTs
										if pubTs <= 0 {
											pubTs = det.PubTs
										}
										meta := make([]string, 0, 3)
										if s := mediaview.SiteLabel(det.Site); s != "" {
											meta = append(meta, s)
										}
										if t := mediaview.FormatPubTime(pubTs, now); t != "" {
											meta = append(meta, t)
										}
										if sz := mediaview.FormatBytes(det.TotalBytes); sz != "" {
											meta = append(meta, sz)
										}
										if len(meta) > 0 {
											ui.Text(c, strings.Join(meta, " · ")).FontSize(12).TextColor(theme.ColorTextMuted).SingleLine()
										}
									})
								} else {
									ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).ClipX().Children(func() {
										ui.Text(c, authorName).FontSize(14).FontWeight(600).TextColor(theme.ColorTextMain).SingleLine()
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
									ui.Text(c, body).FontSize(14).LineHeight(theme.LineHeightBody).TextColor(theme.ColorTextMain)
									if links := mediaview.ExtractLinks(body, tp.Links); len(links) > 0 {
										p.renderDetailLinks(c, h, links)
									}
								}
								if len(tp.Images) > 0 {
									p.renderDetailImageGrid(c, h, tp.Images)
								}
								for vi, vid := range tp.Videos {
									p.renderDetailThreadVideoCard(c, h, det.ItemKey, vid, vi)
								}

								if idx == focalIdx {
									if det.Origin != nil {
										p.renderDetailOriginBox(c, h, det.Origin, origImages)
									}
									if det.Card != nil {
										p.renderDetailRichCard(c, det.Card)
									}
									if len(det.Missing) > 0 {
										p.renderDetailMissingBox(c, det.Missing)
									}
								} else if tp.Origin != nil && !sameDetailOriginDTO(tp.Origin, det.Origin) {
									p.renderDetailOriginBox(c, h, tp.Origin, nil)
								}
							})
						})
				}
			})

			// Bottom read-only footer note (FR-MOB-025).
			ui.Text(c, "本机只读详情 · 删除或管理文件请在电脑端 TRACE 中操作").
				FontSize(12).TextColor(theme.ColorTextFaint).TextAlign(ui.Center).FillWidth()
		})
	})
}

func (p *Page) renderDetailArticleSection(c *ui.Context, h pagekit.Host, det companionapi.LibraryItemDetail) {
	art := det.Article
	if art == nil {
		return
	}
	mediaDims := make(map[string][2]int, len(det.Media))
	for _, m := range det.Media {
		if m.Path != "" && m.Width > 0 && m.Height > 0 {
			mediaDims[m.Path] = [2]int{m.Width, m.Height}
		}
	}
	var gallery []mediaview.ImageItem
	coverIdx := -1
	if rel := strings.TrimSpace(art.CoverRel); rel != "" {
		coverIdx = len(gallery)
		d := mediaDims[rel]
		gallery = append(gallery, mediaview.ImageItem{
			ItemKey: det.ItemKey,
			RelPath: rel,
			Width:   d[0],
			Height:  d[1],
		})
	}
	blockImgIdx := make(map[int]int, len(art.Blocks))
	for bi, blk := range art.Blocks {
		if rel := strings.TrimSpace(blk.MediaRel); rel != "" {
			blockImgIdx[bi] = len(gallery)
			d := mediaDims[rel]
			gallery = append(gallery, mediaview.ImageItem{
				ItemKey: det.ItemKey,
				RelPath: rel,
				Width:   d[0],
				Height:  d[1],
			})
		}
	}

	renderArtImage := func(keySuffix, label, rel string, galleryIdx int) {
		src, _, _ := p.loader.GetOrLoad(h, mediaview.MediaThumbPath(det.ItemKey, rel))
		aspect := float32(16.0 / 9.0)
		if d := mediaDims[rel]; d[0] > 0 && d[1] > 0 {
			aspect = float32(d[0]) / float32(d[1])
			if aspect < 0.65 {
				aspect = 0.65
			}
			if aspect > 2.4 {
				aspect = 2.4
			}
		}
		tile := ui.Box(c.Key(keySuffix)).
			FillWidth().
			AspectRatio(aspect).
			Radius(10).
			Clip().
			Background(theme.ColorBgSecondary).
			Label(label)
		tile.Children(func() {
			if src != nil {
				ui.Image(c, src).Fit(ui.Cover).Fill()
			}
		})
		if tile.Clicked() && galleryIdx >= 0 && galleryIdx < len(gallery) {
			p.triggerOpenImages(h, gallery, galleryIdx)
		}
	}

	ui.Column(c).Label("X离线文章正文").FillWidth().Gap(12).ClipX().Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
			ui.Box(c).Padding(2, 8).Radius(4).Background(theme.ColorAccentBg).Children(func() {
				ui.Text(c, "X 文章").FontSize(11).FontWeight(600).TextColor(theme.ColorAccent).SingleLine()
			})
		})
		if title := firstNonEmptyDetail(art.Title, det.Title); title != "" {
			ui.Text(c, title).FontSize(19).FontWeight(700).LineHeight(theme.LineHeightHeading).TextColor(theme.ColorTextMain)
		}
		if coverIdx >= 0 {
			renderArtImage("art-cover", "查看文章封面", strings.TrimSpace(art.CoverRel), coverIdx)
		}
		if len(art.Blocks) == 0 {
			if plain := firstNonEmptyDetail(art.PlainText, det.Text); plain != "" {
				ui.Text(c, plain).FontSize(14).LineHeight(theme.LineHeightBody).TextColor(theme.ColorTextMain)
			}
			return
		}
		ord := 0
		imgSeq := 0
		for bi, blk := range art.Blocks {
			if blk.Type != "ordered-list-item" {
				ord = 0
			}
			switch blk.Type {
			case "header-one":
				if txt := strings.TrimSpace(blk.Text); txt != "" {
					ui.Text(c, txt).FontSize(17).FontWeight(700).LineHeight(theme.LineHeightHeading).TextColor(theme.ColorTextMain)
				}
			case "header-two":
				if txt := strings.TrimSpace(blk.Text); txt != "" {
					ui.Text(c, txt).FontSize(15).FontWeight(600).LineHeight(theme.LineHeightHeading).TextColor(theme.ColorTextMain)
				}
			case "blockquote":
				if txt := strings.TrimSpace(blk.Text); txt != "" {
					ui.Column(c).FillWidth().Padding(8, 12).Radius(6).Background(theme.ColorBgSecondary).BorderWidth(0, 0, 0, 3).BorderColor(theme.ColorAccent).Gap(6).Children(func() {
						ui.Text(c, txt).FontSize(14).LineHeight(theme.LineHeightBody).TextColor(theme.ColorTextMuted)
						if links := mediaview.ExtractLinks(txt, blk.Links); len(links) > 0 {
							p.renderDetailLinks(c, h, links)
						}
					})
				}
			case "ordered-list-item":
				ord++
				if txt := strings.TrimSpace(blk.Text); txt != "" {
					ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Start).Children(func() {
						ui.Text(c, fmt.Sprintf("%d.", ord)).FontSize(14).FontWeight(600).LineHeight(theme.LineHeightBody).TextColor(theme.ColorTextMuted).SingleLine().Shrink(0)
						ui.Column(c).Grow(1).Basis(0).Gap(4).Children(func() {
							ui.Text(c, txt).FontSize(14).LineHeight(theme.LineHeightBody).TextColor(theme.ColorTextMain)
							if links := mediaview.ExtractLinks(txt, blk.Links); len(links) > 0 {
								p.renderDetailLinks(c, h, links)
							}
						})
					})
				}
			case "unordered-list-item":
				if txt := strings.TrimSpace(blk.Text); txt != "" {
					ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Start).Children(func() {
						ui.Text(c, "•").FontSize(14).FontWeight(700).LineHeight(theme.LineHeightBody).TextColor(theme.ColorTextMuted).SingleLine().Shrink(0)
						ui.Column(c).Grow(1).Basis(0).Gap(4).Children(func() {
							ui.Text(c, txt).FontSize(14).LineHeight(theme.LineHeightBody).TextColor(theme.ColorTextMain)
							if links := mediaview.ExtractLinks(txt, blk.Links); len(links) > 0 {
								p.renderDetailLinks(c, h, links)
							}
						})
					})
				}
			case "atomic":
				if gIdx, ok := blockImgIdx[bi]; ok {
					imgSeq++
					renderArtImage(fmt.Sprintf("art-img-%d", bi), fmt.Sprintf("查看文章图片 %d", imgSeq), strings.TrimSpace(blk.MediaRel), gIdx)
				}
				if tid := strings.TrimSpace(blk.TweetID); tid != "" {
					ui.Row(c).FillWidth().Padding(8, 10).Radius(8).Background(theme.ColorBgPanel).Border(1, theme.ColorBorder).Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Text(c, "引用帖子 · ID "+tid).FontSize(12).TextColor(theme.ColorTextMuted).SingleLine()
					})
				}
			default:
				if txt := strings.TrimSpace(blk.Text); txt != "" {
					ui.Column(c).FillWidth().Gap(6).Children(func() {
						ui.Text(c, txt).FontSize(14).LineHeight(theme.LineHeightBody).TextColor(theme.ColorTextMain)
						if links := mediaview.ExtractLinks(txt, blk.Links); len(links) > 0 {
							p.renderDetailLinks(c, h, links)
						}
					})
				}
			}
		}
	})
}

func (p *Page) renderDetailLinks(c *ui.Context, h pagekit.Host, links []companionapi.TextLink) {
	ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
		for i, lnk := range links {
			u := lnk.URL
			btn := ui.Button(c.Key(i), "").
				Label("打开链接 "+lnk.Label).
				MinHeight(34).Padding(4, 10).Radius(999).
				Background(theme.ColorAccentBg)
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

func (p *Page) renderDetailImageGrid(c *ui.Context, h pagekit.Host, images []mediaview.ImageItem) {
	cols := 3
	if len(images) == 1 {
		cols = 1
	} else if len(images) == 2 || len(images) == 4 {
		cols = 2
	}
	ui.Grid(c).FillWidth().Columns(cols).Gap(6).Children(func() {
		for i, img := range images {
			idx := i
			im := img
			src, _, _ := p.loader.GetOrLoad(h, mediaview.MediaThumbPath(im.ItemKey, im.RelPath))
			aspect := float32(1)
			if cols == 1 {
				aspect = 16.0 / 9.0
				if im.Width > 0 && im.Height > 0 {
					aspect = float32(im.Width) / float32(im.Height)
					if aspect < 0.5 {
						aspect = 0.5
					}
					if aspect > 2.4 {
						aspect = 2.4
					}
				}
			}
			tile := ui.Box(c.Key(idx)).
				AspectRatio(aspect).
				Radius(10).
				Clip().
				Background(theme.ColorBgSecondary).
				Label(fmt.Sprintf("查看图片 %d / %d", idx+1, len(images)))
			tile.Children(func() {
				if src != nil {
					ui.Image(c, src).Fit(ui.Cover).Fill()
				}
				if im.MotionRelPath != "" && !im.GIF {
					ui.Box(c).Absolute().Top(6).Left(6).Padding(2, 6).Radius(4).Background(ui.RGBA(0, 0, 0, 0.62)).Children(func() {
						ui.Text(c, "实况").FontSize(10).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()
					})
				} else if im.GIF {
					ui.Box(c).Absolute().Top(6).Left(6).Padding(2, 6).Radius(4).Background(ui.RGBA(0, 0, 0, 0.62)).Children(func() {
						ui.Text(c, "GIF").FontSize(10).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()
					})
				}
			})
			if tile.Clicked() {
				p.triggerOpenImages(h, images, idx)
			}
		}
	})
}

func (p *Page) renderDetailThreadVideoCard(c *ui.Context, h pagekit.Host, itemKey string, vid detailThreadVideo, idx int) {
	var coverSrc ui.ImageSource
	if vid.CoverRelPath != "" {
		coverSrc, _, _ = p.loader.GetOrLoad(h, mediaview.MediaThumbPath(itemKey, vid.CoverRelPath))
	}
	durTxt := mediaview.FormatDuration(vid.DurationSec)
	cardTitle := strings.TrimSpace(vid.Title)
	if cardTitle == "" {
		cardTitle = "X 视频"
	}
	card := ui.Row(c.Key("dtv-"+fmt.Sprint(idx))).
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
				ui.Box(c).Size(26, 26).Radius(13).ZIndex(2).Background(ui.RGBA(0, 0, 0, 0.55)).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
					mediaview.DrawIcon(c, "play", 12, theme.ColorWhite, 1.8)
				})
				if durTxt != "" {
					ui.Box(c).Absolute().Right(4).Bottom(4).ZIndex(2).Padding(1, 4).Radius(4).Background(ui.RGBA(0, 0, 0, 0.7)).Children(func() {
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

func formatDetailReplyHint(target *companionapi.ItemReplyTargetDTO, threadIDs []string) string {
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

func sameDetailOriginDTO(a, b *companionapi.ItemOriginDTO) bool {
	if a == nil || b == nil {
		return false
	}
	return strings.TrimSpace(a.ID) != "" && strings.TrimSpace(a.ID) == strings.TrimSpace(b.ID)
}

func (p *Page) renderDetailOriginBox(c *ui.Context, h pagekit.Host, orig *companionapi.ItemOriginDTO, origImages []mediaview.ImageItem) {
	p.renderDetailOriginBoxDepth(c, h, orig, origImages, 0)
}

func (p *Page) renderDetailOriginBoxDepth(c *ui.Context, h pagekit.Host, orig *companionapi.ItemOriginDTO, origImages []mediaview.ImageItem, depth int) {
	if orig == nil {
		return
	}
	bg := theme.ColorBgPanel
	pad := float32(12)
	radius := float32(10)
	if depth > 0 {
		bg = theme.ColorBgMain
		pad = 9
		radius = 8
	}
	ui.Column(c).FillWidth().Padding(pad, pad).Radius(radius).Background(bg).Border(1, theme.ColorBorder).Gap(6).ClipX().Children(func() {
		if orig.Unavailable {
			msg := orig.UnavailableReason
			if msg == "" {
				msg = "原内容已删除或不可见"
			}
			ui.Text(c, msg).FontSize(13).TextColor(theme.ColorTextMuted)
		} else {
			authorMeta := strings.TrimSpace(orig.AuthorName)
			if handle := strings.TrimPrefix(strings.TrimSpace(orig.AuthorUniqueID), "@"); handle != "" {
				if authorMeta != "" {
					authorMeta += " @" + handle
				} else {
					authorMeta = "@" + handle
				}
			}
			if authorMeta != "" {
				ui.Text(c, authorMeta).FontSize(13).FontWeight(600).TextColor(theme.ColorTextMuted).SingleLine()
			}
			if replyHint := formatDetailReplyHint(orig.InReplyTo, nil); replyHint != "" {
				ui.Text(c, replyHint).FontSize(11).TextColor(theme.ColorTextMuted).SingleLine()
			}
			if orig.Text != "" {
				el := ui.Text(c, orig.Text).FontSize(13).LineHeight(theme.LineHeightBody).TextColor(theme.ColorTextMain)
				if depth > 0 {
					el.MaxLines(4)
				}
			}
			if depth == 0 {
				if len(origImages) > 0 {
					p.renderDetailImageGrid(c, h, origImages)
				}
				if orig.Origin != nil {
					p.renderDetailOriginBoxDepth(c, h, orig.Origin, nil, depth+1)
				}
			}
		}
	})
}

func (p *Page) renderDetailEditHistorySection(c *ui.Context, h pagekit.Host, det companionapi.LibraryItemDetail) {
	if len(det.EditHistory) == 0 {
		return
	}
	p.mu.Lock()
	collapsed := p.detailEditHistoryCollapsed
	p.mu.Unlock()

	mediaDims := make(map[string][2]int, len(det.Media))
	for _, m := range det.Media {
		if m.Path != "" && m.Width > 0 && m.Height > 0 {
			mediaDims[strings.ToLower(strings.TrimSpace(m.Path))] = [2]int{m.Width, m.Height}
		}
	}

	title := fmt.Sprintf("编辑历史（%d 个版本）", len(det.EditHistory))
	if det.EditCount > 0 {
		title = fmt.Sprintf("已编辑 %d 次 · %d 个版本", det.EditCount, len(det.EditHistory))
	}

	ui.Column(c).Label("移动端离线编辑历史").FillWidth().Gap(8).ClipX().Children(func() {
		toggleBtn := ui.Button(c.Key("eh-toggle"), "").
			Label("切换编辑历史").
			FillWidth().MinHeight(40).Padding(8, 12).Radius(10).
			Background(theme.ColorBgPanel).Border(1, theme.ColorBorder).
			Justify(ui.SpaceBetween).AlignItems(ui.Center)
		toggleBtn.Children(func() {
			ui.Text(c, title).FontSize(13).FontWeight(600).TextColor(theme.ColorTextMain).SingleLine().Grow(1)
			stateTxt := "收起"
			if collapsed {
				stateTxt = "展开"
			}
			ui.Text(c, stateTxt).FontSize(12).TextColor(theme.ColorTextMuted).SingleLine().Shrink(0)
		})
		if toggleBtn.Clicked() {
			p.mu.Lock()
			p.detailEditHistoryCollapsed = !p.detailEditHistoryCollapsed
			p.mu.Unlock()
		}

		if collapsed {
			return
		}

		now := p.nowFn()
		for idx, rev := range det.EditHistory {
			badge := fmt.Sprintf("第 %d 版", rev.Version)
			if idx == 0 && len(det.EditHistory) > 1 {
				badge = fmt.Sprintf("最新版本 · 第 %d 版", max(rev.Version, 1))
			} else if rev.Version <= 1 {
				badge = "原始版本 · 第 1 版"
			}
			ui.Column(c.Key(fmt.Sprintf("eh-rev-%d-%d", rev.Version, idx))).
				Label(fmt.Sprintf("编辑历史版本 %d", rev.Version)).
				FillWidth().Padding(10, 12).Radius(10).
				Background(theme.ColorBgPanel).Border(1, theme.ColorBorder).
				Gap(6).ClipX().
				Children(func() {
					ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(6).ClipX().Children(func() {
						ui.Box(c).Padding(2, 6).Radius(4).Background(theme.ColorAccentBg).Shrink(0).Children(func() {
							ui.Text(c, badge).FontSize(11).FontWeight(600).TextColor(theme.ColorAccent).SingleLine()
						})
						if ts := mediaview.FormatPubTime(rev.EditTs, now); ts != "" {
							ui.Text(c, ts).FontSize(11).TextColor(theme.ColorTextMuted).SingleLine()
						}
					})
					if txt := strings.TrimSpace(rev.Text); txt != "" {
						ui.Text(c, txt).FontSize(13).LineHeight(theme.LineHeightBody).TextColor(theme.ColorTextMain)
					}
					if links := mediaview.ExtractLinks(rev.Text, rev.Links); len(links) > 0 {
						p.renderDetailLinks(c, h, links)
					}
					if len(rev.MediaPaths) > 0 {
						revImgs := make([]mediaview.ImageItem, 0, len(rev.MediaPaths))
						for _, rel := range rev.MediaPaths {
							rel = strings.TrimSpace(rel)
							if rel == "" {
								continue
							}
							d := mediaDims[strings.ToLower(rel)]
							revImgs = append(revImgs, mediaview.ImageItem{
								ItemKey: det.ItemKey,
								RelPath: rel,
								GIF:     strings.HasSuffix(strings.ToLower(rel), ".gif"),
								Width:   d[0],
								Height:  d[1],
							})
						}
						if len(revImgs) > 0 {
							ui.Row(c).FillWidth().Wrap().Gap(6).ClipX().Children(func() {
								for imgIdx, im := range revImgs {
									ii := imgIdx
									itemImg := im
									src, _, _ := p.loader.GetOrLoad(h, mediaview.MediaThumbPath(itemImg.ItemKey, itemImg.RelPath))
									tile := ui.Box(c.Key(fmt.Sprintf("eh-img-%d-%d", rev.Version, ii))).
										Size(64, 64).
										Radius(8).
										Clip().
										Background(theme.ColorBgSecondary).
										Border(1, theme.ColorBorder).
										Shrink(0).
										Label(fmt.Sprintf("查看编辑历史第 %d 版图片 %d", rev.Version, ii+1))
									tile.Children(func() {
										if src != nil {
											ui.Image(c, src).Fit(ui.Cover).Fill()
										}
										if itemImg.GIF {
											ui.Box(c).Absolute().Top(4).Left(4).Padding(1, 4).Radius(3).Background(ui.RGBA(0, 0, 0, 0.62)).Children(func() {
												ui.Text(c, "GIF").FontSize(9).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()
											})
										}
									})
									if tile.Clicked() {
										p.triggerOpenImages(h, revImgs, ii)
									}
								}
							})
						}
					}
				})
		}
	})
}

func (p *Page) renderDetailRichCard(c *ui.Context, card *companionapi.ItemRichCardDTO) {
	ui.Column(c).FillWidth().Padding(10, 12).Radius(10).Background(theme.ColorBgPanel).Border(1, theme.ColorBorder).Gap(4).ClipX().Children(func() {
		if card.Label != "" {
			ui.Text(c, card.Label).FontSize(11).FontWeight(600).TextColor(theme.ColorTextMuted)
		}
		if card.Title != "" {
			ui.Text(c, card.Title).FontSize(13).FontWeight(600).TextColor(theme.ColorTextMain)
		}
		if card.Desc != "" {
			ui.Text(c, card.Desc).FontSize(12).TextColor(theme.ColorTextMuted)
		}
	})
}

func (p *Page) renderDetailMissingBox(c *ui.Context, missing []companionapi.MissingItemDTO) {
	ui.Column(c).FillWidth().Padding(10, 12).Radius(10).Background(theme.ColorWarnBg).Gap(4).ClipX().Children(func() {
		ui.Text(c, "部分内容未缓存").FontSize(13).FontWeight(600).TextColor(theme.ColorWarnText)
		for i, m := range missing {
			ui.Text(c.Key(i), fmt.Sprintf("• %s：%s", m.Item, m.Reason)).FontSize(12).TextColor(theme.ColorWarnText)
		}
	})
}

func (p *Page) renderDetailAvatar(c *ui.Context, h pagekit.Host, avatarPath, name, seed string) {
	p.renderDetailAvatarSized(c, h, avatarPath, name, seed, 40)
}

func (p *Page) renderDetailAvatarSized(c *ui.Context, h pagekit.Host, avatarPath, name, seed string, size float32) {
	var src ui.ImageSource
	if avatarPath != "" {
		src, _, _ = p.loader.GetOrLoad(h, avatarPath)
	}
	ui.Box(c).
		Size(size, size).Radius(size / 2).Clip().Shrink(0).
		Background(mediaview.AvatarPlaceholderColor(seed)).
		Justify(ui.Center).AlignItems(ui.Center).
		Children(func() {
			if src != nil {
				ui.Image(c, src).Fit(ui.Cover).Fill()
			} else {
				ui.Text(c, mediaview.AvatarInitial(name)).FontSize(size * 0.42).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()
			}
		})
}

func extractDetailRawID(id, itemKey string) string {
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

func stripDetailThreadSuffix(base string, conts []companionapi.ThreadPartDTO) string {
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

func joinDetailThreadTexts(parts []companionapi.ThreadPartDTO) string {
	var segs []string
	for _, tp := range parts {
		if t := strings.TrimSpace(tp.Text); t != "" {
			segs = append(segs, t)
		}
	}
	return strings.Join(segs, "\n\n")
}

func resolveMobileDetailThread(det companionapi.LibraryItemDetail) ([]detailThreadPart, int, []mediaview.ImageItem, bool) {
	if !strings.EqualFold(strings.TrimSpace(det.Site), "x") || len(det.Thread) == 0 {
		return nil, -1, nil, false
	}

	rawID := extractDetailRawID(det.ID, det.ItemKey)
	rawSegs := make([]companionapi.ThreadPartDTO, 0, len(det.Thread))
	for _, seg := range det.Thread {
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
	} else if len(rawSegs) >= 2 && strings.TrimSpace(det.Text) != "" && strings.TrimSpace(det.Text) == joinDetailThreadTexts(rawSegs) {
		segments = rawSegs
		focalIdx = 0
	} else {
		baseText := strings.TrimSpace(det.Text)
		if baseText == "" {
			baseText = strings.TrimSpace(det.Desc)
		}
		if baseText == "" {
			baseText = strings.TrimSpace(det.Title)
		}
		focalSeg := companionapi.ThreadPartDTO{
			ID:    rawID,
			PubTs: det.PubTs,
			Text:  stripDetailThreadSuffix(baseText, rawSegs),
			Links: det.Links,
		}
		segments = make([]companionapi.ThreadPartDTO, 0, 1+len(rawSegs))
		segments = append(segments, focalSeg)
		segments = append(segments, rawSegs...)
		focalIdx = 0
	}
	if len(segments) < 2 {
		return nil, -1, nil, false
	}

	hasFromOrigin := det.Origin != nil && det.Origin.VideoFromOrigin
	if !hasFromOrigin {
		for _, np := range det.NotePages {
			if np.FromOrigin {
				hasFromOrigin = true
				break
			}
		}
	}
	if !hasFromOrigin {
		for _, m := range det.Media {
			if m.FromOrigin {
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

	for i, np := range det.NotePages {
		if np.Video != "" {
			notePageVideos[np.Video] = true
		}
		if hasFromOrigin && np.FromOrigin {
			if rel := firstNonEmptyDetail(np.Image, np.Cover); rel != "" {
				isGIF := np.GIF || strings.EqualFold(np.Kind, "gif")
				motion := np.Motion
				if motion == "" && isGIF && np.Video != "" {
					motion = np.Video
				}
				appendOrigImage(mediaview.ImageItem{
					ItemKey:            det.ItemKey,
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
	if len(det.NotePages) == 0 {
		for _, m := range det.Media {
			if m.Role != "image" || m.Path == "" {
				continue
			}
			img := mediaview.ImageItem{
				ItemKey:       det.ItemKey,
				RelPath:       m.Path,
				MotionRelPath: m.Motion,
				GIF:           m.GIF,
				Width:         m.Width,
				Height:        m.Height,
			}
			if hasFromOrigin && m.FromOrigin {
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

	attachPage := func(dst *detailThreadPart, np companionapi.NotePageDTO) {
		isGIF := np.GIF || strings.EqualFold(np.Kind, "gif")
		if np.Video != "" && !isGIF {
			codec := ""
			spec := ""
			w, h := np.Width, np.Height
			if np.VideoInfo != nil {
				codec = np.VideoInfo.Codec
				spec = strings.TrimSpace(strings.Trim(np.VideoInfo.QualityName+" · "+np.VideoInfo.Codec, " ·"))
				if w == 0 {
					w = np.VideoInfo.Width
				}
				if h == 0 {
					h = np.VideoInfo.Height
				}
			}
			dst.Videos = append(dst.Videos, detailThreadVideo{
				Title:        "X 视频",
				CoverRelPath: firstNonEmptyDetail(np.Cover, np.Image),
				DurationSec:  np.Duration,
				QualitySpec:  spec,
				Req: mediaview.VideoRequest{
					ItemKey: det.ItemKey,
					Title:   firstNonEmptyDetail(dst.Text, det.Title, "X 视频"),
					Tracks: []mediaview.VideoTrack{{
						Label:          "X 视频",
						RelPath:        np.Video,
						DurationSec:    np.Duration,
						CodecHint:      codec,
						Width:          w,
						Height:         h,
						QualitySummary: spec,
					}},
				},
			})
			return
		}
		if rel := firstNonEmptyDetail(np.Image, np.Cover); rel != "" {
			motion := np.Motion
			if motion == "" && isGIF && np.Video != "" {
				motion = np.Video
			}
			dst.Images = append(dst.Images, mediaview.ImageItem{
				ItemKey:            det.ItemKey,
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
	parts := make([]detailThreadPart, len(segments))

	for i, seg := range segments {
		isFocal := i == focalIdx
		pubTs := seg.PubTs
		if pubTs <= 0 && isFocal {
			pubTs = det.PubTs
		}
		links := seg.Links
		if len(links) == 0 && isFocal && len(det.Links) > 0 {
			links = det.Links
		}
		parts[i] = detailThreadPart{
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
					attachPage(&parts[i], np)
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

	if len(outerPageKeys) > 0 {
		for _, idx := range outerPageKeys {
			if !claimedPages[idx] {
				attachPage(&parts[focalIdx], outerPageByIdx[idx])
			}
		}
	} else if len(outerImages) > 0 {
		for i, img := range outerImages {
			if !claimedImages[i] {
				parts[focalIdx].Images = append(parts[focalIdx].Images, img)
			}
		}
	}

	// Single non-NotePage outer video belongs to focal.
	originOwnsVideo := det.Origin != nil && det.Origin.VideoFromOrigin
	if len(outerPageKeys) == 0 && !originOwnsVideo {
		var coverRel string
		for _, m := range det.Media {
			if m.Role == "cover" && m.Path != "" && (!hasFromOrigin || !m.FromOrigin) {
				coverRel = m.Path
				break
			}
		}
		for _, m := range det.Media {
			if m.Role == "video" && m.Path != "" && !notePageVideos[m.Path] && (!hasFromOrigin || !m.FromOrigin) {
				dur := m.Duration
				if dur <= 0 {
					dur = det.Duration
				}
				parts[focalIdx].Videos = append(parts[focalIdx].Videos, detailThreadVideo{
					Title:        "X 视频",
					CoverRelPath: coverRel,
					DurationSec:  dur,
					Req: mediaview.VideoRequest{
						ItemKey: det.ItemKey,
						Title:   firstNonEmptyDetail(parts[focalIdx].Text, det.Title, "X 视频"),
						Tracks: []mediaview.VideoTrack{{
							Label:       "X 视频",
							RelPath:     m.Path,
							DurationSec: dur,
							Width:       m.Width,
							Height:      m.Height,
						}},
					},
				})
				break
			}
		}
	}

	return parts, focalIdx, origImages, true
}

func firstNonEmptyDetail(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func (p *Page) renderDetailVideoCard(c *ui.Context, h pagekit.Host, det companionapi.LibraryItemDetail, req mediaview.VideoRequest) {
	coverRel := findDetailCover(det)
	var coverSrc ui.ImageSource
	if coverRel != "" {
		coverSrc, _, _ = p.loader.GetOrLoad(h, mediaview.MediaThumbPath(det.ItemKey, coverRel))
	}
	durTxt := mediaview.FormatDuration(det.Duration)
	if durTxt == "" && len(req.Tracks) > 0 {
		durTxt = mediaview.FormatDuration(req.Tracks[0].DurationSec)
	}

	box := ui.Box(c).
		FillWidth().
		AspectRatio(16.0 / 9.0).
		Radius(12).
		Clip().
		Background(theme.ColorBgSecondary).
		Justify(ui.Center).
		AlignItems(ui.Center).
		Label("播放视频")
	box.Children(func() {
		if coverSrc != nil {
			ui.Image(c, coverSrc).Fit(ui.Cover).Absolute().Top(0).Left(0).Right(0).Bottom(0)
		}
		ui.Box(c).Size(48, 48).Radius(24).ZIndex(2).Background(ui.RGBA(0, 0, 0, 0.58)).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
			mediaview.DrawIcon(c, "play", 22, theme.ColorWhite, 2)
		})
		if durTxt != "" {
			ui.Box(c).Absolute().Right(8).Bottom(8).Padding(3, 8).Radius(6).Background(ui.RGBA(0, 0, 0, 0.72)).Children(func() {
				ui.Text(c, durTxt).FontSize(12).FontWeight(500).TextColor(theme.ColorWhite).SingleLine()
			})
		}
	})
	if box.Clicked() {
		p.triggerOpenVideo(h, req)
	}
}

func (p *Page) renderDetailLiveSection(c *ui.Context, h pagekit.Host, det companionapi.LibraryItemDetail, req mediaview.VideoRequest, indexMap map[int]int, hasVideo bool) {
	lv := det.Live
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		meta := make([]string, 0, 3)
		if lv.RoomID != "" {
			meta = append(meta, "直播间 "+lv.RoomID)
		}
		if lv.QualityName != "" {
			meta = append(meta, lv.QualityName)
		}
		if lv.Codec != "" {
			meta = append(meta, lv.Codec)
		}
		if len(meta) > 0 {
			ui.Text(c, strings.Join(meta, " · ")).FontSize(13).TextColor(theme.ColorTextMuted)
		}
		if len(lv.Segments) > 0 {
			ui.Text(c, fmt.Sprintf("录像分段（%d）", len(lv.Segments))).FontSize(14).FontWeight(600).TextColor(theme.ColorTextMain)
			for i, seg := range lv.Segments {
				trackIdx, playable := indexMap[i]
				if !hasVideo || seg.File == "" {
					playable = false
				}
				lbl := fmt.Sprintf("分段 %d", i+1)
				segMeta := make([]string, 0, 3)
				if d := mediaview.FormatDuration(int(seg.DurationSec)); d != "" {
					segMeta = append(segMeta, d)
				}
				if sz := mediaview.FormatBytes(seg.Bytes); sz != "" {
					segMeta = append(segMeta, sz)
				}
				if seg.QualityName != "" || seg.Codec != "" {
					segMeta = append(segMeta, strings.TrimSpace(seg.QualityName+" "+seg.Codec))
				}
				if !playable {
					segMeta = append(segMeta, "未缓存")
				}
				titleCol := theme.ColorTextMain
				iconCol := theme.ColorAccent
				if !playable {
					titleCol = theme.ColorTextMuted
					iconCol = theme.ColorTextFaint
				}
				rowBtn := ui.Button(c.Key("seg-"+fmt.Sprint(i)), "").
					Label("播放 "+lbl).
					FillWidth().MinHeight(46).Padding(10, 12).Radius(10).
					Background(theme.ColorBgPanel).Border(1, theme.ColorBorder).
					Justify(ui.Start).AlignItems(ui.Center).
					Disabled(!playable)
				rowBtn.Children(func() {
					ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
						mediaview.DrawIcon(c, "play", 14, iconCol, 1.8)
						ui.Text(c, lbl).FontSize(13).FontWeight(600).TextColor(titleCol)
						ui.Text(c, strings.Join(segMeta, " · ")).FontSize(12).TextColor(theme.ColorTextMuted).SingleLine().Grow(1).Basis(0)
					})
				})
				if playable && rowBtn.Clicked() {
					segReq := req
					segReq.StartIndex = trackIdx
					p.triggerOpenVideo(h, segReq)
				}
			}
		}
	})
}

func (p *Page) openDetailPrimaryMedia(h pagekit.Host, det companionapi.LibraryItemDetail) {
	if det.Kind == "video" || det.Kind == "live" {
		if req, _, ok := buildDetailVideoRequest(det); ok {
			p.triggerOpenVideo(h, req)
			return
		}
	}
	if imgs := collectDetailImages(det); len(imgs) > 0 {
		p.triggerOpenImages(h, imgs, 0)
		return
	}
	if req, _, ok := buildDetailVideoRequest(det); ok {
		p.triggerOpenVideo(h, req)
	}
}

func collectDetailImages(det companionapi.LibraryItemDetail) []mediaview.ImageItem {
	out := make([]mediaview.ImageItem, 0, len(det.NotePages)+len(det.Media))
	seen := make(map[string]bool)
	nowmByBase := make(map[string]string)
	for _, m := range det.Media {
		if m.Role == "image_nowm" && m.Path != "" {
			base := strings.TrimSuffix(m.Path, "_nowm"+extOf(m.Path))
			nowmByBase[base] = m.Path
		}
	}
	add := func(im mediaview.ImageItem) {
		if im.RelPath == "" || seen[im.RelPath] {
			return
		}
		seen[im.RelPath] = true
		out = append(out, im)
	}
	for _, np := range det.NotePages {
		if np.Image != "" {
			add(mediaview.ImageItem{
				ItemKey:            det.ItemKey,
				RelPath:            np.Image,
				NoWatermarkRelPath: np.NoWatermark,
				MotionRelPath:      np.Motion,
				GIF:                np.GIF,
				Width:              np.Width,
				Height:             np.Height,
			})
		}
	}
	for _, m := range det.Media {
		if m.Role == "image" && m.Path != "" {
			base := strings.TrimSuffix(m.Path, extOf(m.Path))
			add(mediaview.ImageItem{
				ItemKey:            det.ItemKey,
				RelPath:            m.Path,
				NoWatermarkRelPath: nowmByBase[base],
				MotionRelPath:      m.Motion,
				GIF:                m.GIF,
				Width:              m.Width,
				Height:             m.Height,
			})
		}
	}
	return out
}

func buildDetailVideoRequest(det companionapi.LibraryItemDetail) (mediaview.VideoRequest, map[int]int, bool) {
	title := strings.TrimSpace(det.Title)
	if title == "" {
		title = "视频"
	}
	if det.Live != nil && len(det.Live.Segments) > 0 {
		tracks := make([]mediaview.VideoTrack, 0, len(det.Live.Segments))
		idxMap := make(map[int]int, len(det.Live.Segments))
		for i, seg := range det.Live.Segments {
			if seg.File == "" {
				continue
			}
			codec := seg.Codec
			if codec == "" {
				codec = det.Live.Codec
			}
			spec := strings.TrimSpace(strings.Trim(seg.QualityName+" · "+codec, " ·"))
			idxMap[i] = len(tracks)
			tracks = append(tracks, mediaview.VideoTrack{
				Label:          fmt.Sprintf("第 %d 段", i+1),
				RelPath:        seg.File,
				DurationSec:    int(seg.DurationSec),
				CodecHint:      codec,
				Width:          seg.Width,
				Height:         seg.Height,
				QualitySummary: spec,
			})
		}
		if len(tracks) > 0 {
			return mediaview.VideoRequest{
				ItemKey:         det.ItemKey,
				Title:           title,
				IsLiveRecording: true,
				Tracks:          tracks,
			}, idxMap, true
		}
	}

	if len(det.Parts) > 0 {
		tracks := make([]mediaview.VideoTrack, 0, len(det.Parts))
		idxMap := make(map[int]int, len(det.Parts))
		for i, part := range det.Parts {
			if part.File == "" {
				continue
			}
			lbl := part.Title
			if lbl == "" {
				lbl = fmt.Sprintf("P%d", part.Page)
			}
			spec := strings.TrimSpace(strings.Trim(part.QualityName+" · "+part.Codecs, " ·"))
			idxMap[i] = len(tracks)
			tracks = append(tracks, mediaview.VideoTrack{
				Label:          lbl,
				RelPath:        part.File,
				DurationSec:    part.Duration,
				CodecHint:      part.Codecs,
				AudioCodecHint: part.AudioName,
				Width:          part.Width,
				Height:         part.Height,
				QualitySummary: spec,
			})
		}
		if len(tracks) > 0 {
			return mediaview.VideoRequest{
				ItemKey: det.ItemKey,
				Title:   title,
				Tracks:  tracks,
			}, idxMap, true
		}
	}

	if det.VideoInfo != nil && det.VideoInfo.File != "" {
		spec := strings.TrimSpace(strings.Trim(det.VideoInfo.QualityName+" · "+det.VideoInfo.Codec, " ·"))
		return mediaview.VideoRequest{
			ItemKey: det.ItemKey,
			Title:   title,
			Tracks: []mediaview.VideoTrack{{
				Label:          title,
				RelPath:        det.VideoInfo.File,
				DurationSec:    det.Duration,
				CodecHint:      det.VideoInfo.Codec,
				Width:          det.VideoInfo.Width,
				Height:         det.VideoInfo.Height,
				QualitySummary: spec,
			}},
		}, nil, true
	}

	for _, m := range det.Media {
		if m.Role == "video" && m.Path != "" {
			return mediaview.VideoRequest{
				ItemKey: det.ItemKey,
				Title:   title,
				Tracks: []mediaview.VideoTrack{{
					Label:       title,
					RelPath:     m.Path,
					DurationSec: m.Duration,
					Width:       m.Width,
					Height:      m.Height,
				}},
			}, nil, true
		}
	}

	tracks := make([]mediaview.VideoTrack, 0)
	for i, np := range det.NotePages {
		if np.Video == "" {
			continue
		}
		codec := ""
		spec := ""
		if np.VideoInfo != nil {
			codec = np.VideoInfo.Codec
			spec = strings.TrimSpace(strings.Trim(np.VideoInfo.QualityName+" · "+np.VideoInfo.Codec, " ·"))
		}
		tracks = append(tracks, mediaview.VideoTrack{
			Label:          fmt.Sprintf("视频 %d", i+1),
			RelPath:        np.Video,
			DurationSec:    np.Duration,
			CodecHint:      codec,
			Width:          np.Width,
			Height:         np.Height,
			QualitySummary: spec,
		})
	}
	if len(tracks) > 0 {
		return mediaview.VideoRequest{
			ItemKey: det.ItemKey,
			Title:   title,
			Tracks:  tracks,
		}, nil, true
	}
	return mediaview.VideoRequest{}, nil, false
}

func findDetailCover(det companionapi.LibraryItemDetail) string {
	for _, m := range det.Media {
		if m.Role == "cover" && m.Path != "" {
			return m.Path
		}
	}
	if det.Article != nil && det.Article.CoverRel != "" {
		return det.Article.CoverRel
	}
	for _, np := range det.NotePages {
		if np.Cover != "" {
			return np.Cover
		}
		if np.Image != "" {
			return np.Image
		}
	}
	for _, m := range det.Media {
		if m.Role == "image" && m.Path != "" {
			return m.Path
		}
	}
	return ""
}

func extOf(path string) string {
	idx := strings.LastIndexByte(path, '.')
	if idx < 0 {
		return ""
	}
	return path[idx:]
}
