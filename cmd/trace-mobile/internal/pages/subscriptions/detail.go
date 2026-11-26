package subscriptions

import (
	"fmt"
	"strings"

	"github.com/chialecode/trace/cmd/trace-mobile/internal/pagekit"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/theme"
	"github.com/chialecode/trace/internal/companionapi"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/mobile"
)

func (p *Page) renderDetailView(c *ui.Context, h pagekit.Host, r *ui.Router, title string) {
	ui.Column(c).Fill().Background(theme.ColorBgMain).ClipX().Children(func() {
		if !r.InSystemStack(c) {
			mobile.TopBar(c, mobile.TopBarOptions{
				Title: title,
				Leading: func() {
					backBtn := ui.ButtonBase(c).Label("返回订阅列表").Size(44, 44).Center()
					backBtn.Children(func() {
						theme.DrawIcon(c, "arrow-left", 20, theme.ColorTextMain, 1.8)
					})
					if backBtn.Clicked() {
						h.Back()
					}
				},
			})
		}

		if p.detailLoading && p.detailSub == nil {
			p.renderLoadingSkeleton(c)
			return
		}
		if p.detailErr != "" && p.detailSub == nil {
			p.renderLoadError(c, p.detailErr, func() { p.loadDetail(h, p.detailSubID, true) })
			return
		}
		if p.detailSub == nil {
			p.renderLoadError(c, "找不到这个订阅", func() { p.loadDetail(h, p.detailSubID, true) })
			return
		}

		sub := *p.detailSub

		// Detail header (not nested gray card; white header with hairline divider).
		ui.Column(c).
			FillWidth().
			Padding(12, 14, 10, 14).
			Gap(10).
			Background(theme.ColorBgMain).
			BorderWidth(0, 0, 1, 0).
			BorderColor(theme.ColorBorder).
			Shrink(0).
			ClipX().
			Children(func() {
				// Avatar, Name, Note, and "⋯" action sheet button.
				ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(12).ClipX().Children(func() {
					p.renderCircleAvatar(c, h, sub.AvatarPath, sub.Name, sub.ID, 54, sub.Live || sub.Recording)
					ui.Column(c).Grow(1).Shrink(1).Gap(4).ClipX().Children(func() {
						ui.Text(c, sub.Name).FontSize(18).FontWeight(600).TextColor(theme.ColorTextMain).SingleLine()
						if sub.Note != "" {
							ui.Text(c, sub.Note).FontSize(13).LineHeight(theme.LineHeightCompact).TextColor(theme.ColorTextMuted).MaxLines(2)
						}
					})
					moreBtn := ui.Button(c, "").
						Label("订阅更多操作").
						Radius(999).
						Size(44, 44).
						Background(theme.ColorBgMain).
						Border(1, theme.ColorBorderStrong).
						Shrink(0).
						Center()
					moreBtn.Children(func() {
						ui.Text(c, "⋯").FontSize(16).FontWeight(700).TextColor(theme.ColorTextMain).SingleLine()
					})
					if moreBtn.Clicked() {
						p.detailMenuOpen = true
					}
				})

				// Read-only tracking preset & capability tag pills (FR-MOB-015, ui.md §3.12).
				preset := firstNonEmpty(sub.PresetLabel, "仅手动")
				tagLabels := sub.TagLabels
				if len(tagLabels) == 0 && len(sub.Tags) > 0 {
					tagLabels = sub.Tags
				}
				ui.Row(c).FillWidth().Gap(6).Wrap().AlignItems(ui.Center).ClipX().Children(func() {
					ui.Box(c).Padding(4, 10).Radius(999).Background(theme.ColorAccentBg).Shrink(0).Children(func() {
						ui.Text(c, "追踪："+preset).FontSize(12).FontWeight(600).TextColor(theme.ColorAccent).SingleLine()
					})
					for idx, lbl := range tagLabels {
						if strings.TrimSpace(lbl) == "" {
							continue
						}
						ui.Box(c.Key(idx)).Padding(4, 10).Radius(999).Background(theme.ColorBgSecondary).Border(1, theme.ColorBorder).Shrink(0).Children(func() {
							ui.Text(c, lbl).FontSize(12).TextColor(theme.ColorTextMuted).SingleLine()
						})
					}
				})

				// Clickable custom external links via Host.OpenExternal (FR-MOB-015).
				if len(sub.Links) > 0 {
					ui.Row(c).FillWidth().Gap(6).Wrap().AlignItems(ui.Center).ClipX().Children(func() {
						for idx, lnk := range sub.Links {
							link := lnk
							lbl := firstNonEmpty(link.Label, link.URL)
							linkBtn := ui.Button(c.Key("link-"+fmt.Sprint(idx)), "").
								Label("打开外部链接："+lbl).
								Radius(999).
								Padding(6, 12).
								MinHeight(34).
								Background(theme.ColorBgMain).
								Border(1, theme.ColorBorderStrong).
								Center()
							linkBtn.Children(func() {
								ui.Text(c, "↗ "+lbl).FontSize(12).FontWeight(500).TextColor(theme.ColorAccent).SingleLine()
							})
							if linkBtn.Clicked() {
								if err := h.OpenExternal(link.URL); err != nil {
									p.actionErr = "无法打开链接：" + err.Error()
								}
							}
						}
					})
				}

				p.renderCheckProgressBanner(c, h)
				p.renderFeedbackBanners(c)

				// 2-way segment: 内容 / 账号 (FR-MOB-015, ui.md §3.12).
				ui.Segmented(c, &p.detailTab, "内容", "账号").FillWidth().Label("订阅详情标签切换")
			})

		// Tab content.
		switch p.detailTab {
		case detailTabContent:
			p.renderDetailContentTab(c, h, sub)
		case detailTabAccounts:
			p.renderDetailAccountsTab(c, h, sub)
		}

		// "⋯" ActionSheet: strictly allowed actions only (FR-MOB-022 / FR-MOB-025).
		var sheetActions []mobile.Action
		if p.checking {
			sheetActions = append(sheetActions, mobile.Action{
				Label:       "取消检查",
				Destructive: true,
				OnTap:       func() { p.cancelCheck(h) },
			})
		} else {
			sheetActions = append(sheetActions, mobile.Action{
				Label: "检查更新",
				OnTap: func() { p.startCheckSubscription(h, sub) },
			})
		}
		sheetActions = append(sheetActions, mobile.Action{
			Label: "刷新页面",
			OnTap: func() { p.loadDetail(h, sub.ID, true) },
		})
		mobile.ActionSheet(c, &p.detailMenuOpen, mobile.ActionSheetOptions{
			Title:  sub.Name,
			Cancel: "取消",
		}, sheetActions...)
	})
}

func (p *Page) renderDetailContentTab(c *ui.Context, h pagekit.Host, sub companionapi.SubscriptionDTO) {
	if p.detailFeedLoading && len(p.detailFeed) == 0 {
		p.renderLoadingSkeleton(c)
		return
	}
	if p.detailFeedErr != "" && len(p.detailFeed) == 0 {
		p.renderLoadError(c, p.detailFeedErr, func() { p.loadDetailFeed(h, sub.ID, nil, false) })
		return
	}
	if len(p.detailFeed) == 0 {
		sc := ui.Scroll(c).Grow(1).Basis(0).FillWidth().ClipX()
		if sc.PullToRefresh(&p.detailFeedRefresh) {
			p.loadDetailFeed(h, sub.ID, nil, false)
		}
		sc.Children(func() {
			ui.Column(c).FillWidth().Padding(48, 24).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "还没有缓存这个订阅的内容").FontSize(15).FontWeight(600).TextColor(theme.ColorTextMain)
				ui.Text(c, "电脑端缓存该订阅的作品、动态或直播录像后，会在这里按发布时间从新到旧显示。").
					FontSize(13).LineHeight(theme.LineHeightCompact).TextColor(theme.ColorTextMuted).TextAlign(ui.Center)
			})
		})
		return
	}

	totalRows := len(p.detailFeed)
	if p.detailFeedHasMore || p.detailFeedErr != "" {
		totalRows++
	}
	p.detailListSt.Key = func(i int) any {
		if i < len(p.detailFeed) {
			return firstNonEmpty(p.detailFeed[i].FeedKey, p.detailFeed[i].ItemKey, fmt.Sprint(i))
		}
		return "feed-footer"
	}
	list := ui.List(c, &p.detailListSt, totalRows, func(i int) {
		if i == len(p.detailFeed) {
			ui.Column(c).FillWidth().Padding(12, 14, 24, 14).AlignItems(ui.Center).Gap(8).Children(func() {
				if p.detailFeedErr != "" {
					ui.Text(c, p.detailFeedErr).FontSize(12).TextColor(theme.ColorError).TextAlign(ui.Center)
				}
				if p.detailFeedHasMore {
					lbl := "加载更多"
					if p.detailFeedMoreing {
						lbl = "正在加载…"
					}
					if theme.SecondaryPillButton(c, lbl, "加载更多订阅内容").Disabled(p.detailFeedMoreing).Clicked() && !p.detailFeedMoreing {
						p.loadDetailFeed(h, sub.ID, p.detailFeedNext, true)
					}
				}
			})
			return
		}
		ui.Column(c).FillWidth().Padding(6, 14).ClipX().Children(func() {
			p.renderFeedItemCard(c, h, p.detailFeed[i])
		})
	}).Label("订阅已缓存内容列表").Grow(1).Basis(0).FillWidth()
	if list.PullToRefresh(&p.detailFeedRefresh) {
		p.loadDetailFeed(h, sub.ID, nil, false)
	}
}

func (p *Page) renderFeedItemCard(c *ui.Context, h pagekit.Host, item companionapi.FeedItem) {
	ui.Column(c).
		Label("内容卡片："+firstNonEmpty(item.Title, item.ItemKey)).
		FillWidth().
		Padding(12, 14).
		Radius(12).
		Background(theme.ColorBgMain).
		Border(1, theme.ColorBorder).
		Gap(8).
		ClipX().
		Children(func() {
			// Author & timestamp row (no mutable engagement numbers, ui.md §3.12).
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).ClipX().Children(func() {
				p.renderCircleAvatar(c, h, item.AuthorAvatarPath, firstNonEmpty(item.AuthorName, item.AuthorUID), item.Site+":"+item.AuthorUID, 32, false)
				ui.Column(c).Grow(1).Shrink(1).Gap(2).ClipX().Children(func() {
					ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
						ui.Text(c, firstNonEmpty(item.AuthorName, item.AuthorUID)).
							FontSize(13).FontWeight(600).TextColor(theme.ColorTextMain).SingleLine().Shrink(1)
						renderSiteTag(c, item.Site)
						if item.Kind == "live" {
							ui.Box(c).Padding(1, 6).Radius(4).Background(theme.ColorLiveBg).Shrink(0).Children(func() {
								ui.Text(c, "直播录像").FontSize(11).FontWeight(600).TextColor(theme.ColorLive).SingleLine()
							})
						} else if item.IsForward {
							ui.Box(c).Padding(1, 6).Radius(4).Background(theme.ColorBgSecondary).Shrink(0).Children(func() {
								ui.Text(c, "转发").FontSize(11).TextColor(theme.ColorTextMuted).SingleLine()
							})
						}
					})
					if ts := formatPubTs(item.PubTs); ts != "" {
						ui.Text(c, ts).FontSize(11).TextColor(theme.ColorTextFaint).SingleLine()
					}
				})
			})

			// Title & body text.
			if item.Title != "" {
				ui.Text(c, item.Title).
					FontSize(15).FontWeight(600).LineHeight(theme.LineHeightHeading).TextColor(theme.ColorTextMain).MaxLines(3)
			}
			body := firstNonEmpty(item.Text, item.Desc)
			if body != "" && body != item.Title {
				ui.Text(c, body).
					FontSize(14).LineHeight(theme.LineHeightBody).TextColor(theme.ColorTextMain).MaxLines(6)
			}

			// Text links -> system browser.
			if len(item.Links) > 0 {
				ui.Row(c).FillWidth().Gap(6).Wrap().ClipX().Children(func() {
					for idx, lnk := range item.Links {
						l := lnk
						btn := ui.ButtonBase(c.Key("item-link-"+fmt.Sprint(idx))).
							Label("打开正文链接："+firstNonEmpty(l.Label, l.URL)).
							Padding(4, 8).Radius(6).Background(theme.ColorAccentBg)
						btn.Children(func() {
							ui.Text(c, "↗ "+firstNonEmpty(l.Label, l.URL)).FontSize(12).TextColor(theme.ColorAccent).SingleLine()
						})
						if btn.Clicked() {
							_ = h.OpenExternal(l.URL)
						}
					}
				})
			}

			// Forwarded origin quote.
			if item.Origin != nil {
				ui.Column(c).FillWidth().Padding(10, 12).Radius(10).Background(theme.ColorBgPanel).Border(1, theme.ColorBorder).Gap(4).ClipX().Children(func() {
					if item.Origin.AuthorName != "" {
						ui.Text(c, "@"+item.Origin.AuthorName).FontSize(12).FontWeight(600).TextColor(theme.ColorTextMuted).SingleLine()
					}
					if item.Origin.Unavailable {
						ui.Text(c, firstNonEmpty(item.Origin.UnavailableReason, "原内容已不可见")).
							FontSize(12).TextColor(theme.ColorTextMuted).MaxLines(2)
					} else if txt := firstNonEmpty(item.Origin.Text, item.Origin.Title); txt != "" {
						ui.Text(c, txt).FontSize(13).LineHeight(theme.LineHeightCompact).TextColor(theme.ColorTextMain).MaxLines(4)
					}
				})
			}

			// Media section: video/live playback or image gallery.
			videos := collectPlayableVideos(item)
			if len(videos) > 0 {
				p.renderVideoPreviewCard(c, h, item, videos)
			}

			imgs := collectFeedImages(item)
			if len(imgs) > 0 {
				p.renderImageGrid(c, h, item, imgs)
			}
		})
}

func (p *Page) renderVideoPreviewCard(c *ui.Context, h pagekit.Host, item companionapi.FeedItem, videos []playableSegment) {
	coverRel := item.CoverRelPath
	var bmp *ui.Bitmap
	if coverRel != "" && item.ItemKey != "" {
		bmp = p.lookupBitmap(h, companionapi.MediaPath(item.ItemKey, coverRel)+"?w=480", 480)
	}

	btn := ui.ButtonBase(c).
		Label("播放视频：" + firstNonEmpty(item.Title, item.ItemKey)).
		FillWidth().
		Height(180).
		Radius(12).
		Background(ui.Hex("#18181b")).
		Clip()
	btn.Children(func() {
		if bmp != nil {
			ui.Image(c, bmp).Absolute().Left(0).Top(0).Right(0).Bottom(0).Fit(ui.Cover)
		}
		// Center play badge & bottom metadata strip layered over the full-bleed cover.
		ui.Column(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).Justify(ui.SpaceBetween).Padding(10).Children(func() {
			ui.Row(c).FillWidth().Justify(ui.End).Children(func() {
				if item.QualitySummary != "" {
					ui.Box(c).Padding(3, 8).Radius(6).Background(ui.RGBA(0, 0, 0, 0.65)).Children(func() {
						ui.Text(c, item.QualitySummary).FontSize(11).TextColor(theme.ColorWhite).SingleLine()
					})
				}
			})
			ui.Row(c).FillWidth().Justify(ui.Center).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Size(48, 48).Radius(24).Background(ui.RGBA(0, 0, 0, 0.62)).Center().Children(func() {
					ui.Text(c, "▶").FontSize(18).TextColor(theme.ColorWhite).SingleLine()
				})
			})
			ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
				segHint := ""
				if len(videos) > 1 {
					if item.Kind == "live" {
						segHint = fmt.Sprintf("共 %d 段录像", len(videos))
					} else {
						segHint = fmt.Sprintf("共 %d P", len(videos))
					}
				}
				if segHint != "" {
					ui.Box(c).Padding(2, 7).Radius(6).Background(ui.RGBA(0, 0, 0, 0.65)).Children(func() {
						ui.Text(c, segHint).FontSize(11).TextColor(theme.ColorWhite).SingleLine()
					})
				} else {
					ui.Box(c)
				}
				if dur := formatDurationSec(item.Duration); dur != "" {
					ui.Box(c).Padding(2, 7).Radius(6).Background(ui.RGBA(0, 0, 0, 0.65)).Children(func() {
						ui.Text(c, dur).FontSize(11).TextColor(theme.ColorWhite).SingleLine()
					})
				}
			})
		})
	})
	if btn.Clicked() {
		p.openVideoPlayer(h, item, videos)
	}
}

func (p *Page) renderImageGrid(c *ui.Context, h pagekit.Host, item companionapi.FeedItem, imgs []viewableImage) {
	cols := 3
	if len(imgs) == 1 {
		cols = 1
	} else if len(imgs) == 2 || len(imgs) == 4 {
		cols = 2
	}
	shown := imgs
	if len(shown) > 9 {
		shown = shown[:9]
	}
	rows := (len(shown) + cols - 1) / cols
	cellH := float32(104)
	if cols == 1 {
		cellH = 200
	} else if cols == 2 {
		cellH = 140
	}

	ui.Column(c).FillWidth().Gap(6).ClipX().Children(func() {
		for r := 0; r < rows; r++ {
			ui.Row(c.Key(r)).FillWidth().Gap(6).ClipX().Children(func() {
				for col := 0; col < cols; col++ {
					idx := r*cols + col
					if idx >= len(shown) {
						ui.Box(c).Grow(1).Basis(0)
						continue
					}
					img := shown[idx]
					tapIdx := idx
					thumbPath := companionapi.MediaPath(item.ItemKey, img.Path) + "?w=480"
					bmp := p.lookupBitmap(h, thumbPath, 480)
					tile := ui.ButtonBase(c.Key(idx)).
						Label(fmt.Sprintf("查看大图 %d/%d", idx+1, len(imgs))).
						Grow(1).
						Basis(0).
						Height(cellH).
						Radius(10).
						Background(theme.ColorBgSecondary).
						Clip()
					tile.Children(func() {
						if bmp != nil {
							ui.Image(c, bmp).Absolute().Left(0).Top(0).Right(0).Bottom(0).Fit(ui.Cover)
						} else {
							ui.Box(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).Center().Children(func() {
								ui.Text(c, "图片").FontSize(12).TextColor(theme.ColorTextFaint).SingleLine()
							})
						}
						if idx == len(shown)-1 && len(imgs) > len(shown) {
							ui.Box(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).Background(ui.RGBA(0, 0, 0, 0.45)).Center().Children(func() {
								ui.Text(c, fmt.Sprintf("+%d", len(imgs)-len(shown))).FontSize(16).FontWeight(700).TextColor(theme.ColorWhite)
							})
						}
					})
					if tile.Clicked() {
						p.openImageViewer(h, item, imgs, tapIdx)
					}
				}
			})
		}
	})
}

func (p *Page) renderDetailAccountsTab(c *ui.Context, h pagekit.Host, sub companionapi.SubscriptionDTO) {
	accounts := sortAccountsBySiteOrder(sub.Accounts)
	if len(accounts) == 0 {
		ui.Column(c).Grow(1).Basis(0).FillWidth().Padding(48, 24).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "这个订阅还没有关联账号").FontSize(15).FontWeight(600).TextColor(theme.ColorTextMain)
		})
		return
	}

	ui.Scroll(c).Grow(1).Basis(0).FillWidth().Background(theme.ColorBgPanel).ClipX().Children(func() {
		ui.Column(c).FillWidth().Padding(12, 14, 24, 14).Gap(10).ClipX().Children(func() {
			ui.Text(c, "开播状态为电脑端最近一次记录，不额外向站点发请求。").
				FontSize(12).TextColor(theme.ColorTextMuted).Padding(0, 2)

			for _, acc := range accounts {
				p.renderDetailAccountCard(c, h, acc)
			}
		})
	})
}

func (p *Page) renderDetailAccountCard(c *ui.Context, h pagekit.Host, acc companionapi.SubscriptionAccountDTO) {
	acctKey := firstNonEmpty(acc.AccountKey, normalizeSite(acc.Site)+":"+strings.TrimSpace(acc.UID))
	liveEligible := isLiveRecordableSite(acc.Site) && acc.LiveSupported
	busy := p.busyLiveKey == acctKey

	ui.Column(c.Key(acctKey)).
		Label("订阅关联账号卡片："+acctKey).
		FillWidth().
		Padding(12, 14).
		Radius(12).
		Background(theme.ColorBgMain).
		Border(1, theme.ColorBorder).
		Gap(8).
		ClipX().
		Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(10).ClipX().Children(func() {
				p.renderCircleAvatar(c, h, acc.AvatarPath, firstNonEmpty(acc.Name, acc.UID), acctKey, 44, liveEligible && (acc.LiveStatus == 1 || acc.Recording))

				ui.Column(c).Grow(1).Shrink(1).Gap(3).ClipX().Children(func() {
					ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
						ui.Text(c, firstNonEmpty(acc.Name, acc.UID)).
							FontSize(15).FontWeight(600).TextColor(theme.ColorTextMain).SingleLine().Shrink(1)
						renderSiteTag(c, acc.Site)
					})
					meta := make([]string, 0, 2)
					if handle := formatAccountHandle(acc); handle != "" {
						meta = append(meta, handle)
					}
					meta = append(meta, formatWorksText(acc))
					ui.Text(c, strings.Join(meta, " · ")).
						FontSize(12).TextColor(theme.ColorTextMuted).SingleLine()
				})

				// Live recording controls ONLY for B站 and 抖音 (hidden for Weibo & X — FR-MOB-021).
				if liveEligible {
					if acc.Recording {
						btnText := "停止录制"
						if busy {
							btnText = "停止中…"
						}
						btn := theme.DangerOutlinePillButton(c, btnText, "停止录制："+firstNonEmpty(acc.Name, acc.UID)).
							Disabled(busy).Shrink(0)
						if btn.Clicked() && !busy {
							p.stopLiveRecording(h, acc)
						}
					} else if acc.LiveStatus == 1 {
						btnText := "开始录制"
						if busy {
							btnText = "启动中…"
						}
						btn := theme.PrimaryPillButton(c, btnText, "开始录制："+firstNonEmpty(acc.Name, acc.UID)).
							Disabled(busy).Shrink(0)
						if btn.Clicked() && !busy {
							p.startLiveRecording(h, acc)
						}
					}
				}
			})

			// Last-known live status line (ONLY for B站 & 抖音; hidden for Weibo & X per ui.md §3.9 / FR-MOB-021).
			if liveEligible {
				ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).Wrap().ClipX().Children(func() {
					switch {
					case acc.Recording:
						ui.Box(c).Padding(2, 8).Radius(999).Background(theme.ColorError).Shrink(0).Children(func() {
							ui.Text(c, "录制中").FontSize(11).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()
						})
						info := make([]string, 0, 3)
						if acc.LiveTitle != "" {
							info = append(info, acc.LiveTitle)
						}
						if acc.QualityName != "" {
							info = append(info, acc.QualityName)
						}
						if acc.Codec != "" {
							info = append(info, acc.Codec)
						}
						if len(info) > 0 {
							ui.Text(c, strings.Join(info, " · ")).FontSize(12).TextColor(theme.ColorTextMuted).MaxLines(2).Grow(1).Shrink(1)
						}
					case acc.LiveStatus == 1:
						ui.Box(c).Padding(2, 8).Radius(999).Background(theme.ColorLiveBg).Shrink(0).Children(func() {
							ui.Text(c, "直播中").FontSize(11).FontWeight(600).TextColor(theme.ColorLive).SingleLine()
						})
						if acc.LiveTitle != "" {
							ui.Text(c, acc.LiveTitle).FontSize(12).TextColor(theme.ColorTextMain).MaxLines(2).Grow(1).Shrink(1)
						}
					case acc.LiveStatus == 2:
						ui.Text(c, "轮播中").FontSize(12).TextColor(theme.ColorTextMuted).SingleLine()
					default:
						ui.Text(c, firstNonEmpty(acc.LiveStatusTxt, "未开播")).FontSize(12).TextColor(theme.ColorTextMuted).SingleLine()
					}
				})
			}
		})
}
