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
					if sz := mediaview.FormatBytes(det.TotalBytes); sz != "" {
						meta = append(meta, sz)
					}
					if len(meta) > 0 {
						ui.Text(c, strings.Join(meta, " · ")).FontSize(12).TextColor(theme.ColorTextMuted).SingleLine()
					}
				})
			})

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

			// Images gallery (tap any image to open ImageViewer).
			if len(images) > 0 {
				ui.Column(c).FillWidth().Gap(8).Children(func() {
					ui.Text(c, fmt.Sprintf("图片（%d）", len(images))).FontSize(14).FontWeight(600).TextColor(theme.ColorTextMain)
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
								if im.MotionRelPath != "" {
									ui.Box(c).Absolute().Top(6).Left(6).Padding(2, 6).Radius(4).Background(ui.RGBA(0, 0, 0, 0.62)).Children(func() {
										ui.Text(c, "实况").FontSize(10).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()
									})
								}
							})
							if tile.Clicked() {
								p.triggerOpenImages(h, images, idx)
							}
						}
					})
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

			// Thread parts.
			if len(det.Thread) > 0 {
				ui.Column(c).FillWidth().Gap(6).Children(func() {
					ui.Text(c, "连续帖").FontSize(14).FontWeight(600).TextColor(theme.ColorTextMain)
					for i, tp := range det.Thread {
						if strings.TrimSpace(tp.Text) == "" {
							continue
						}
						ui.Column(c.Key(i)).FillWidth().Padding(10, 12).Radius(10).Background(theme.ColorBgPanel).Border(1, theme.ColorBorder).Children(func() {
							ui.Text(c, tp.Text).FontSize(13).LineHeight(theme.LineHeightBody).TextColor(theme.ColorTextMain)
						})
					}
				})
			}

			// Origin quoted post.
			if det.Origin != nil {
				ui.Column(c).FillWidth().Padding(12, 12).Radius(10).Background(theme.ColorBgPanel).Border(1, theme.ColorBorder).Gap(6).Children(func() {
					if det.Origin.Unavailable {
						msg := det.Origin.UnavailableReason
						if msg == "" {
							msg = "原内容已删除或不可见"
						}
						ui.Text(c, msg).FontSize(13).TextColor(theme.ColorTextMuted)
					} else {
						if det.Origin.AuthorName != "" {
							ui.Text(c, det.Origin.AuthorName).FontSize(13).FontWeight(600).TextColor(theme.ColorTextMuted)
						}
						if det.Origin.Text != "" {
							ui.Text(c, det.Origin.Text).FontSize(13).LineHeight(theme.LineHeightBody).TextColor(theme.ColorTextMain)
						}
					}
				})
			}

			// Rich card summary (read-only).
			if det.Card != nil {
				ui.Column(c).FillWidth().Padding(10, 12).Radius(10).Background(theme.ColorBgPanel).Border(1, theme.ColorBorder).Gap(4).Children(func() {
					if det.Card.Label != "" {
						ui.Text(c, det.Card.Label).FontSize(11).FontWeight(600).TextColor(theme.ColorTextMuted)
					}
					if det.Card.Title != "" {
						ui.Text(c, det.Card.Title).FontSize(13).FontWeight(600).TextColor(theme.ColorTextMain)
					}
					if det.Card.Desc != "" {
						ui.Text(c, det.Card.Desc).FontSize(12).TextColor(theme.ColorTextMuted)
					}
				})
			}

			// Missing parts note (honest partial status).
			if len(det.Missing) > 0 {
				ui.Column(c).FillWidth().Padding(10, 12).Radius(10).Background(theme.ColorWarnBg).Gap(4).Children(func() {
					ui.Text(c, "部分内容未缓存").FontSize(13).FontWeight(600).TextColor(theme.ColorWarnText)
					for i, m := range det.Missing {
						ui.Text(c.Key(i), fmt.Sprintf("• %s：%s", m.Item, m.Reason)).FontSize(12).TextColor(theme.ColorWarnText)
					}
				})
			}

			// Bottom read-only footer note (FR-MOB-025).
			ui.Text(c, "本机只读详情 · 删除或管理文件请在电脑端 TRACE 中操作").
				FontSize(12).TextColor(theme.ColorTextFaint).TextAlign(ui.Center).FillWidth()
		})
	})
}

func (p *Page) renderDetailAvatar(c *ui.Context, h pagekit.Host, avatarPath, name, seed string) {
	var src ui.ImageSource
	if avatarPath != "" {
		src, _, _ = p.loader.GetOrLoad(h, avatarPath)
	}
	ui.Box(c).
		Size(40, 40).Radius(20).Clip().Shrink(0).
		Background(mediaview.AvatarPlaceholderColor(seed)).
		Justify(ui.Center).AlignItems(ui.Center).
		Children(func() {
			if src != nil {
				ui.Image(c, src).Fit(ui.Cover).Fill()
			} else {
				ui.Text(c, mediaview.AvatarInitial(name)).FontSize(16).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()
			}
		})
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
