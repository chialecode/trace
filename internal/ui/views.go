package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

type navItem struct {
	Route string
	Icon  string
	Label string
}

// Per docs/ui.md §2 & DEC-014, main navigation has 5 items; Settings is pinned in the bottom-left row (DEC-019).
var mainNavItems = []navItem{
	{Route: RouteHome, Icon: "home", Label: "主页"},
	{Route: RouteSubscriptions, Icon: "subscriptions", Label: "订阅"},
	{Route: RouteParse, Icon: "parse", Label: "解析"},
	{Route: RouteLibrary, Icon: "library", Label: "媒体库"},
	{Route: RouteTasks, Icon: "tasks", Label: "任务"},
}

func (a *App) activeWorkCount() int {
	n := 0
	for _, t := range a.Tasks {
		if t.Status == "running" || t.Status == "queued" {
			n++
		}
	}
	for _, r := range a.LiveRooms {
		if r.Recording {
			n++
		}
	}
	return n
}

func (a *App) anyLiveRoomActive() bool {
	for _, r := range a.LiveRooms {
		if r.LiveStatus == 1 {
			return true
		}
	}
	return false
}

// renderCenterColumn draws the page: a top bar (back, title, the paste box,
// the page's actions) that never scrolls, then the page body.
func (a *App) renderCenterColumn(c *ui.Context, width float32) {
	parseBox := a.CurrentRoute != RouteParse && a.CurrentRoute != RouteLogin && a.CurrentRoute != RouteSettings
	parseInline := parseBox && width >= 640
	ui.Column(c).
		FillWidth().
		Padding(10, 16).
		Gap(8).
		BorderWidth(0, 0, 1, 0).
		BorderColor(ColorBorder).
		Background(ColorBgMain).
		Shrink(0).
		ClipX().
		Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(10).Grow(1).ClipX().Children(func() {
					if a.CurrentRoute == RouteVideo || a.CurrentRoute == RouteUser ||
						(a.CurrentRoute == RouteSubscriptions && a.SubDetailOpen) ||
						(a.CurrentRoute == RouteHome && a.HomeActorFilter != nil) {
						backLabel := "返回"
						if a.CurrentRoute == RouteSubscriptions && a.SubDetailOpen {
							backLabel = "返回订阅列表"
						} else if a.CurrentRoute == RouteHome && a.HomeActorFilter != nil {
							backLabel = "返回主页时间线"
						}
						backBtn := ui.ButtonBase(c).
							Label(backLabel).
							Tooltip(backLabel).
							Size(34, 34).
							Radius(999).
							Center()
						if backBtn.Hovered() {
							backBtn.Background(ColorHover)
						}
						backBtn.Children(func() {
							DrawIcon(c, "arrow-left", 18, ColorTextMain, 2.0)
						})
						if backBtn.Clicked() {
							if a.CurrentRoute == RouteHome && a.HomeActorFilter != nil {
								a.closeHomeActorFeed()
							} else if a.CurrentRoute == RouteSubscriptions && a.SubDetailOpen {
								a.SubDetailOpen = false
								a.SubManageMenuOpen = false
								a.SubManageModalMode = ""
							} else {
								a.NavigateBack()
							}
						}
					}
					if a.CurrentRoute == RouteHome && a.HomeActorFilter != nil {
						actor := *a.HomeActorFilter
						a.renderAvatar(c, actor.Name, actor.Mid, 28, a.isActorLive(actor), "", nil, actor.Face)
					}
					ui.Text(c, a.routeTitle()).FontSize(18).FontWeight(600).TextColor(ColorTextMain).SingleLine()
				})
				if parseInline {
					ui.Box(c).Width(min(360, width*0.46)).Shrink(1).Children(func() {
						a.renderQuickParseBox(c, "快速解析输入框")
					})
				}
				a.renderRouteHeaderActions(c)
			})
			if parseBox && !parseInline {
				a.renderQuickParseBox(c, "快速解析输入框")
			}

			// R5: Never show unrelated background loading state on RouteParse
			if len(a.pending) > 0 && a.CurrentRoute != RouteParse {
				ui.Text(c, "正在加载…").Label("请求进行中").FontSize(12).TextColor(ColorInfoBlue)
			}

			if a.BannerError != "" {
				ui.Row(c).FillWidth().Padding(8, 12).Radius(10).Background(ColorErrorBg).Border(1, ui.RGBA(180, 35, 24, 0.2)).AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
					DrawIcon(c, "alert-circle", 16, ColorError, 1.8)
					ui.Text(c, a.BannerError).FontSize(13).TextColor(ColorError).MaxLines(2).Grow(1)
					if OutlinePillButton(c, "关闭", "关闭错误提示").Clicked() {
						a.BannerError = ""
					}
				})
			}
			a.renderUpdateBanner(c)
			if a.BannerInfo != "" {
				ui.Row(c).FillWidth().Padding(8, 12).Radius(10).Background(ColorInfoBlueBg).AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
					ui.Text(c, a.BannerInfo).FontSize(13).TextColor(ColorInfoBlue).MaxLines(2).Grow(1)
					if OutlinePillButton(c, "知道了", "关闭消息提示").Clicked() {
						a.BannerInfo = ""
					}
				})
			}
		})

	// Without the context panel (a narrow window), the people with updates
	// are a row of avatars above the timeline.
	if a.CurrentRoute == RouteHome && a.HomeActorFilter == nil && !a.panelShown {
		a.renderHomeUpdatedAvatarsRow(c)
	}

	// 2. Center Body (Virtualized list on /home, or scrollable container on other routes)
	switch a.CurrentRoute {
	case RouteHome:
		a.renderHomeRoute(c)
	case RouteSubscriptions:
		a.renderSubscriptionsRoute(c)
	case RouteParse:
		a.renderParseRoute(c)
	case RouteVideo:
		a.renderVideoRoute(c)
	case RouteUser:
		a.renderUserRoute(c)
	case RouteLibrary:
		a.renderLibraryRoute(c)
	case RouteTasks:
		a.renderTasksRoute(c)
	case RouteSettings:
		a.renderSettingsRoute(c, width)
	case RouteLogin:
		a.renderLoginRoute(c)
	default:
		a.renderHomeRoute(c)
	}
}

func (a *App) routeTitle() string {
	switch a.CurrentRoute {
	case RouteHome:
		if a.HomeActorFilter != nil && a.HomeActorFilter.Name != "" {
			return a.HomeActorFilter.Name
		}
		return "主页"
	case RouteSubscriptions:
		if a.SubDetailOpen && a.SelectedSubID != "" {
			for _, s := range a.Subscriptions {
				if s.ID == a.SelectedSubID && s.Name != "" {
					return s.Name
				}
			}
			return "订阅详情"
		}
		return "订阅"
	case RouteParse:
		return "解析"
	case RouteVideo:
		if a.Opening != nil {
			if a.Opening.Kind == "dynamic" {
				return "动态"
			}
			return "视频"
		}
		if a.CurrentLibraryDetail != nil && a.CurrentLibraryDetail.Kind == "live" {
			return "直播录像"
		}
		if a.CurrentLibraryDetail != nil && a.CurrentLibraryDetail.Kind == "dynamic" {
			return "动态"
		}
		if a.CurrentDynamic != nil && a.CurrentVideo == nil {
			return "动态"
		}
		return "视频"
	case RouteUser:
		if a.Opening != nil {
			return firstNonEmptyStrUI(a.Opening.AuthorName, "UP 主空间")
		}
		if a.CurrentUser != nil && a.CurrentUser.Name != "" {
			return a.CurrentUser.Name
		}
		return "UP 主空间"
	case RouteLibrary:
		return "媒体库"
	case RouteTasks:
		return "任务"
	case RouteSettings:
		return settingsCategoryLabel(a.SettingsCategory)
	case RouteLogin:
		return "登录哔哩哔哩"
	default:
		return "TRACE"
	}
}

func (a *App) renderRouteHeaderActions(c *ui.Context) {
	switch a.CurrentRoute {
	case RouteSubscriptions:
		if a.SubDetailOpen {
			return
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if a.pending["check-updates"] {
				ui.Text(c, fmt.Sprintf("正在检查 %d / %d", a.CheckDone, a.CheckTotal)).FontSize(12).FontWeight(600).TextColor(ColorInfoBlue).SingleLine()
				if OutlinePillButton(c, "取消", "立即检查更新").Clicked() {
					a.backend.CancelSubscriptionCheck()
				}
			}
			if AccentPillButton(c, "添加订阅", "添加订阅").Clicked() {
				a.AddSubDialogOpen = true
			}
			open := a.SubMoreMenuOpen
			MoreMenuButton(c, "订阅更多菜单", 32, &open, func(m *ui.Menu) {
				if m.Item("检查全部").Disabled(a.pending["check-updates"]).Chosen() {
					a.checkAllSubscriptions()
				}
				if m.Item("从关注列表导入").Chosen() {
					a.openImportFollowingsModal()
				}
			})
			a.SubMoreMenuOpen = open
		})
	case RouteTasks:
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			btnText := "检查画质更新"
			if a.pending["upgrade-check"] {
				btnText = fmt.Sprintf("检查 %d/%d · 取消", a.UpgradeDone, a.UpgradeTotal)
			}
			if OutlinePillButton(c, btnText, "检查画质升级").Clicked() {
				if a.pending["upgrade-check"] {
					if a.upgradeCancel != nil {
						a.upgradeCancel()
					}
				} else {
					a.checkQualityUpgrades()
				}
			}
		})
	}
}

func (a *App) renderQuickParseBox(c *ui.Context, inputLabel string) {
	ui.Row(c).
		FillWidth().
		Height(36).
		Padding(0, 4, 0, 12).
		Radius(999).
		Background(ColorBgSecondary).
		Border(1, ColorBorder).
		Gap(8).
		AlignItems(ui.Center).
		ClipX().
		Children(func() {
			DrawIcon(c, "link", 16, ColorTextMuted, 1.6)
			inp := ui.TextInputBase(c, &a.QuickParseInput).
				Placeholder("粘贴链接或 UID 解析").
				Label(inputLabel).
				Padding(4, 2).
				Grow(1)
			wasComposing := a.QuickParseComposing
			a.QuickParseComposing = inp.Composing()
			if inp.Submitted() && !inp.Composing() && !wasComposing {
				q := a.QuickParseInput
				a.QuickParseInput = ""
				a.SubmitParse(q)
			}
			if AccentPillButton(c, "解析", "快速解析提交").Clicked() {
				q := a.QuickParseInput
				a.QuickParseInput = ""
				a.SubmitParse(q)
			}
		})
}

// ============================================================================
// Route: /home (Virtualized Feed List + Updated Avatars Row, C24, C38, R8..R10, docs/ui.md §3.1)
// ============================================================================

func (a *App) renderHomeUpdatedAvatarsRow(c *ui.Context) {
	actors := a.homeUpdatedActors()
	if len(actors) == 0 {
		return
	}
	ui.Column(c).
		Label("主页有更新头像行").
		FillWidth().
		Padding(10, 16).
		BorderWidth(0, 0, 1, 0).
		BorderColor(ColorBorder).
		Background(ColorBgMain).
		Shrink(0).
		ClipX().
		Children(func() {
			ui.ScrollHorizontal(c).FillWidth().Children(func() {
				ui.Row(c).Gap(14).AlignItems(ui.Start).Children(func() {
					for _, act := range actors {
						actor := act
						liveRing := a.isActorLive(actor)
						ui.Column(c).
							Width(72).
							Shrink(0).
							AlignItems(ui.Center).
							Gap(4).
							ClipX().
							Children(func() {
								a.renderAvatar(c, actor.Name, actor.Mid, 56, liveRing, "主页更新头像："+actor.Name, func() {
									a.openHomeActorFeed(actor)
								}, actor.Face)
								nameBtn := ui.ButtonBase(c).
									FillWidth().
									Padding(0).
									Justify(ui.Center).
									AlignItems(ui.Center).
									ClipX()
								nameBtn.Children(func() {
									ui.Text(c, actor.Name).FontSize(12).TextColor(ColorTextMain).SingleLine()
								})
								if nameBtn.Clicked() {
									a.openHomeActorFeed(actor)
								}
							})
					}
				})
			})
		})
}

func (a *App) renderHomeRoute(c *ui.Context) {
	if a.FeedError != "" {
		ui.Row(c).FillWidth().Padding(8, 16).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, a.FeedError).FontSize(13).TextColor(ColorError).MaxLines(2).Grow(1)
			if OutlinePillButton(c, "重试", "重试主页分页").Clicked() {
				if len(a.FeedItems) == 0 {
					a.loadInitialFeed()
				} else {
					a.loadFeedEdge(a.feedFailedBefore)
				}
			}
		})
	}
	first, last := a.FeedListState.Visible()
	a.markHomeSeen(first, last)
	// Posts cached while the timeline is open are fetched when it reaches
	// the top; nothing is counted or announced as unread (DEC-040).
	if a.HomeActorFilter == nil && first >= 0 && first <= 2 && a.feedMoreBefore && a.FeedError == "" {
		a.loadFeedEdge(true)
	}
	if last >= len(a.FeedItems)-3 && a.feedMoreAfter && a.FeedError == "" {
		a.loadFeedEdge(false)
	}

	if len(a.FeedItems) == 0 {
		ui.Column(c).Grow(1).FillWidth().Padding(48, 24).AlignItems(ui.Center).Gap(12).ClipX().Children(func() {
			if a.HomeActorFilter != nil {
				ui.Text(c, "该订阅下还没有本机已缓存的内容").FontSize(17).FontWeight(600).TextColor(ColorTextMain).SingleLine()
				if OutlinePillButton(c, "返回主时间线", "空状态返回主页时间线").Clicked() {
					a.closeHomeActorFeed()
				}
				return
			}
			ui.Text(c, "还没有缓存的内容").FontSize(17).FontWeight(600).TextColor(ColorTextMain).SingleLine()
			ui.Text(c, "到「解析」里打开一位 UP 主，在动态上点「缓存」").
				FontSize(14).TextColor(ColorTextMuted).MaxLines(2)
			if AccentPillButton(c, "去解析", "去解析链接").Clicked() {
				a.Navigate(RouteParse)
			}
		})
		return
	}

	ui.List(c, &a.FeedListState, len(a.FeedItems), func(i int) {
		if i < 0 || i >= len(a.FeedItems) {
			return
		}
		entry := a.FeedItems[i]
		post := a.feedEntryToPostCard(entry)
		ui.Column(c).Label("信息流行 " + entry.FeedKey).FillWidth().Children(func() { a.renderPostCard(c, post) })
		if i == len(a.FeedItems)-1 && !a.feedMoreAfter {
			ui.Row(c).FillWidth().Padding(18, 16).Justify(ui.Center).Children(func() {
				ui.Text(c, "已经到最早的缓存了").FontSize(13).TextColor(ColorTextMuted).SingleLine()
			})
		}
	}).Label("主页信息流列表").TrackScroll(&a.FeedScroll).Grow(1).FillWidth()
}

// ============================================================================
// Route: /parse (Link & UID Parser + Sanitized History, C01..C03, C38, docs/ui.md §3.2)
// ============================================================================

func (a *App) renderParseRoute(c *ui.Context) {
	acct := a.backend.AccountStatus()
	acctName := acct.Uname
	if acctName == "" {
		acctName = "TRACE"
	}

	ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
		ui.Column(c).FillWidth().Gap(0).ClipX().Children(func() {
			// Social Composer-style Input Box (docs/ui.md §3.2)
			ui.Row(c).
				FillWidth().
				Padding(16).
				Gap(12).
				AlignItems(ui.Start).
				BorderWidth(0, 0, 1, 0).
				BorderColor(ColorBorder).
				Background(ColorBgMain).
				ClipX().
				Children(func() {
					a.renderAvatar(c, acctName, acct.Mid, 42, false, "", nil)
					ui.Column(c).Grow(1).Gap(10).ClipX().Children(func() {
						inp := ui.TextInput(c, &a.ParsePageInput).
							Placeholder("粘贴哔哩哔哩视频链接、用户主页或 UID").
							Label("链接解析输入框").
							FillWidth()
						wasComposing := a.ParseInputComposing
						a.ParseInputComposing = inp.Composing()
						if inp.Submitted() && !inp.Composing() && !wasComposing {
							a.SubmitParse(a.ParsePageInput)
						}

						ui.Row(c).FillWidth().Padding(4, 0, 0, 0).AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
							ui.Text(c, "支持视频链接、短链、空间主页和 UID").FontSize(13).TextColor(ColorTextMuted).SingleLine()
							btnLabel := "立即解析"
							if a.parseBusy() {
								btnLabel = "解析中…"
							}
							if AccentPillButton(c, btnLabel, "立即解析").Disabled(a.parseBusy()).Clicked() {
								a.SubmitParse(a.ParsePageInput)
							}
						})

						if a.LastParseError != "" {
							ui.Row(c).FillWidth().Padding(8, 12).Radius(8).Background(ColorErrorBg).AlignItems(ui.Center).Gap(6).Children(func() {
								DrawIcon(c, "alert-circle", 16, ColorError, 1.8)
								ui.Text(c, a.LastParseError).FontSize(13).TextColor(ColorError).MaxLines(2).Grow(1)
							})
						}
					})
				})

			// Recent Parse History Header (C03, R11, docs/ui.md §3.2)
			ui.Row(c).FillWidth().Padding(14, 16, 8, 16).AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
				ui.Text(c, fmt.Sprintf("最近解析（%d）", len(a.HistoryItems))).FontSize(16).FontWeight(600).TextColor(ColorTextMain).SingleLine()
				if len(a.HistoryItems) > 0 {
					open := a.HistoryHeaderMenuOpen
					MoreMenuButton(c, "最近解析更多", 30, &open, func(m *ui.Menu) {
						if m.Item("全部清空").Danger().Chosen() {
							a.HistoryClearConfirmOpen = true
						}
					})
					a.HistoryHeaderMenuOpen = open
				}
			})

			if len(a.HistoryItems) > 0 && a.HistoryClearConfirmOpen {
				ui.Row(c).
					Label("确认清空最近解析栏").
					FillWidth().
					Padding(10, 16).
					Background(ColorWarnBg).
					BorderWidth(0, 0, 1, 0).
					BorderColor(ColorDotYellow).
					AlignItems(ui.Center).
					Justify(ui.SpaceBetween).
					Gap(8).
					ClipX().
					Children(func() {
						ui.Text(c, "确认清空全部最近解析记录？同时会清理本机缓存的缩略图。").
							FontSize(13).FontWeight(600).TextColor(ColorTextMain).MaxLines(2).Grow(1)
						ui.Row(c).Gap(8).AlignItems(ui.Center).Shrink(0).Children(func() {
							if OutlinePillButton(c, "取消", "取消清空历史").Clicked() {
								a.HistoryClearConfirmOpen = false
								a.HistoryHeaderMenuOpen = false
							}
							if DangerOutlinePillButton(c, "确认清空", "确认清空历史").Clicked() {
								if err := a.backend.ClearHistory(); err != nil {
									a.showError(err)
									a.HistoryItems = a.backend.ListHistory()
									return
								}
								a.HistoryItems = a.backend.ListHistory()
								a.HistoryClearConfirmOpen = false
								a.HistoryHeaderMenuOpen = false
								a.HistoryItemMenuID = ""
							}
						})
					})
			}

			if len(a.HistoryItems) == 0 {
				ui.Column(c).FillWidth().Padding(40, 16).AlignItems(ui.Center).Gap(6).Children(func() {
					ui.Text(c, "粘贴一个链接开始").FontSize(15).TextColor(ColorTextMuted).SingleLine()
				})
				return
			}

			for _, h := range a.HistoryItems {
				entry := h
				var midNum int64
				_, _ = fmt.Sscanf(entry.ID, "%d", &midNum)
				authorLabel := entry.Subtitle
				if entry.Kind == "user" || authorLabel == "" {
					authorLabel = entry.Title
				}
				bodyTitle := entry.Title
				if entry.Kind == "user" {
					bodyTitle = entry.Subtitle
				}
				kindLabel := "视频"
				switch entry.Kind {
				case "user":
					kindLabel = "UP 主空间"
				case "dynamic":
					kindLabel = "动态"
				case "live":
					kindLabel = "直播间"
				}
				itemMenuKey := entry.Kind + ":" + entry.ID
				itemMenuOpen := a.HistoryItemMenuID == itemMenuKey || a.HistoryItemMenuID == entry.ID
				avatarBmp := a.lookupHistoryThumbBitmap(entry.AvatarThumb)
				coverBmp := a.lookupHistoryThumbBitmap(entry.CoverThumb)

				ui.Column(c).
					FillWidth().
					Padding(14, 16).
					Gap(8).
					BorderWidth(0, 0, 1, 0).
					BorderColor(ColorBorder).
					ClipX().
					Children(func() {
						ui.Row(c).
							FillWidth().
							Gap(12).
							AlignItems(ui.Start).
							ClipX().
							Children(func() {
								// R11 / DEC-037: Click entry body to open directly; right side has only a "⋯" button.
								bodyBtn := ui.ButtonBase(c).
									Label("历史打开：" + entry.ID).
									Grow(1).
									Padding(0).
									Gap(12).
									AlignItems(ui.Start).
									Justify(ui.Start).
									ClipX()
								bodyBtn.Children(func() {
									a.renderAvatarWithBitmap(c, authorLabel, midNum, 40, false, "", nil, avatarBmp)
									ui.Column(c).Grow(1).Gap(6).ClipX().Children(func() {
										ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
											ui.Text(c, authorLabel).FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
											ui.Text(c, fmt.Sprintf("· %s · %s", kindLabel, entry.ID)).FontSize(12).TextColor(ColorTextMuted).SingleLine()
										})
										if bodyTitle != "" {
											ui.Text(c, bodyTitle).FontSize(15).FontWeight(600).TextColor(ColorTextMain).MaxLines(2)
										}

										if entry.Kind == "video" || entry.CoverThumb != "" {
											a.renderMediaThumbWithBitmap(c, coverBmp, "", "", entry.Title, entry.DurationText, "", entry.Kind == "video", 0, 148)
										}
									})
								})
								if bodyBtn.Clicked() {
									preview := &OpeningPreview{Kind: entry.Kind, Title: bodyTitle, AuthorName: authorLabel}
									if entry.Kind == "user" {
										preview.Title, preview.Mid = "", midNum
									}
									a.openLink(entry.CanonicalURL, preview)
								}

								open := itemMenuOpen
								MoreMenuButton(c, "历史更多："+entry.ID, 30, &open, func(m *ui.Menu) {
									if entry.CanonicalURL != "" && m.Item("在浏览器中打开").Chosen() {
										_ = a.backend.Desktop().OpenExternalURL(entry.CanonicalURL)
									}
									if m.Item("删除").Danger().Chosen() {
										a.showError(a.backend.RemoveHistory(entry.Kind, entry.ID))
										a.HistoryItems = a.backend.ListHistory()
									}
								})
								if open {
									a.HistoryItemMenuID = itemMenuKey
								} else if itemMenuOpen {
									a.HistoryItemMenuID = ""
								}
							})
					})
			}
		})
	})
}

// ============================================================================
// Route: /video (Video & Dynamic Detail, Multi-Part Selection, Upgrade, C12, C15, C20, docs/ui.md §3.4)
// ============================================================================

func (a *App) renderVideoRoute(c *ui.Context) {
	ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
		ui.Column(c).FillWidth().Padding(16).Gap(14).ClipX().Children(func() {
			if a.Opening != nil {
				a.renderOpening(c)
				return
			}
			if a.CurrentLibraryDetail != nil {
				switch a.CurrentLibraryDetail.Kind {
				case "dynamic":
					a.renderPostCard(c, a.cachedDetailPost(*a.CurrentLibraryDetail))
					return
				case "live":
					a.renderRecordingDetail(c, *a.CurrentLibraryDetail)
					return
				}
			}
			if a.CurrentVideo != nil {
				vd := a.CurrentVideo
				itemKey := firstNonEmptyStrUI(a.CurrentVideoItemKey, library.CanonicalItemKey("bilibili", "video", vd.BVID))
				var cachedItem *library.ItemMetadata
				dotStatus := ""
				dotLabel := ""
				var relCover string
				if it, ok, err := a.backend.GetLibraryItem(itemKey); err == nil && ok {
					cachedItem = &it
					itemKey = it.ItemKey
					ds := library.ComputeDotState(it)
					dotStatus = ds.Dot
					dotLabel = ds.Label
					for _, m := range it.Media {
						if m.Role == "cover" && relCover == "" {
							relCover = m.Path
						}
					}
				}

				// 1. Creator Row + Cache Button + Status Dot (docs/ui.md §3.4)
				ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(10).ClipX().Children(func() {
					ui.Row(c).AlignItems(ui.Center).Gap(10).Grow(1).ClipX().Children(func() {
						a.renderAvatar(c, vd.Author.Name, vd.Author.Mid, 42, false, "", func() {
							a.OpenUser(vd.Author.Mid, vd.Author.Name)
						})
						author := ui.ButtonBase(c).Label("进入空间").Grow(1).Padding(0).ClipX()
						author.Children(func() {
							ui.Column(c).Gap(2).Grow(1).ClipX().Children(func() {
								ui.Text(c, vd.Author.Name).FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
								ui.Text(c, fmt.Sprintf("UID %d", vd.Author.Mid)).FontSize(12).TextColor(ColorTextMuted).SingleLine()
							})
						})
						if author.Clicked() {
							a.OpenUser(vd.Author.Mid, vd.Author.Name)
						}
					})

					ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
						if AccentPillButton(c, "缓存视频", "缓存视频").Clicked() {
							var pages []int
							for _, p := range vd.Pages {
								if a.SelectedPartPages[p.Page] {
									pages = append(pages, p.Page)
								}
							}
							detail := vd
							if a.CurrentLibraryDetail != nil {
								// A local manifest is not a complete site response.
								// The cache worker must resolve fresh metadata by BVID.
								detail = nil
							}
							_, err := a.backend.EnqueueVideoCache(context.Background(), download.VideoEnqueueRequest{
								BVID:   vd.BVID,
								Pages:  pages,
								Detail: detail,
							})
							if err != nil {
								a.BannerError = err.Error()
							} else {
								a.RefreshData()
								a.BannerInfo = "已加入缓存队列：" + vd.BVID
							}
						}
						if dotStatus != "" && dotStatus != "none" {
							DrawStatusDot(c, dotStatus, dotLabel)
						}
					})
				})

				// 2. Quality Upgrade Banner (docs/ui.md §3.4)
				if a.UpgradeAvailable || (cachedItem != nil && cachedItem.UpgradeAvailable) {
					hint := "可以升级到更高画质（如 4K / 高码率）"
					if cachedItem != nil && cachedItem.UpgradeHint != "" {
						hint = cachedItem.UpgradeHint
					}
					ui.Row(c).
						FillWidth().
						Padding(10, 14).
						Radius(12).
						Background(ColorWarnBg).
						Border(1, ColorDotYellow).
						AlignItems(ui.Center).
						Justify(ui.SpaceBetween).
						Gap(8).
						ClipX().
						Children(func() {
							ui.Row(c).AlignItems(ui.Center).Gap(8).Grow(1).Children(func() {
								DrawStatusDot(c, "yellow", hint)
								ui.Text(c, hint).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
							})
							if AccentPillButton(c, "升级缓存", "视频详情升级缓存").Clicked() {
								_, err := a.backend.Upgrade(context.Background(), itemKey)
								a.showError(err)
								if err == nil {
									a.BannerInfo = "已加入画质升级任务：" + vd.BVID
									a.RefreshData()
								}
							}
						})
				}

				// 3. Large Video Title (20px Bold)
				ui.Text(c, vd.Title).FontSize(20).FontWeight(600).TextColor(ColorTextMain).MaxLines(3)

				// 4. 16:9 Large Cover Card (click to play if cached, else view HD cover)
				totalDur := vd.Duration
				if totalDur <= 0 && len(vd.Pages) > 0 {
					for _, p := range vd.Pages {
						totalDur += p.Duration
					}
				}
				durationText := FormatDuration(totalDur)
				if a.CurrentLibraryDetail != nil && vd.Duration <= 0 {
					durationText = ""
				}
				coverBtn := ui.ButtonBase(c).
					Label("视频大封面").
					FillWidth().
					Height(236).
					Radius(12).
					Clip()
				coverBtn.Children(func() {
					// The status dot is beside the cache button above, not on the cover.
					a.renderMediaThumb(c, itemKey, relCover, vd.Title, durationText, "", cachedItem != nil, 0, 236, vd.Cover)
				})
				if coverBtn.Clicked() {
					if cachedItem != nil {
						a.OpenPlayerOverlay(*cachedItem)
					} else if vd.Cover != "" {
						a.OpenImageViewer([]string{vd.Cover}, nil, 0)
					}
				}

				// 5. Expandable Description
				if vd.Desc != "" {
					a.renderExpandableText(c, "video-desc:"+vd.BVID, vd.Desc)
				}

				// 6. Publish Time · Partition · Actions (including R8 cache maintenance: "补存弹幕和字幕")
				metaParts := []string{"BVID: " + vd.BVID}
				if cachedItem != nil && cachedItem.Kind == "live" {
					roomStr := vd.BVID
					if cachedItem.Live != nil && cachedItem.Live.RoomID > 0 {
						roomStr = strconv.FormatInt(cachedItem.Live.RoomID, 10)
					}
					metaParts = []string{"直播间: " + roomStr}
					if liveSpec := formatLiveRecordingSpec(cachedItem.Live, ""); liveSpec != "" {
						metaParts = append(metaParts, liveSpec)
					}
				}
				if vd.PubTs > 0 {
					metaParts = append(metaParts, FormatTimestamp(vd.PubTs))
				}
				if vd.TName != "" {
					metaParts = append(metaParts, vd.TName)
				}
				ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Wrap().Children(func() {
					ui.Text(c, strings.Join(metaParts, " · ")).FontSize(13).TextColor(ColorTextMuted).SingleLine()
					ui.Row(c).Gap(6).Wrap().Children(func() {
						if cachedItem != nil && cachedItem.Kind == "video" {
							if OutlinePillButton(c, "补存弹幕和字幕", "补全："+itemKey).Clicked() {
								_, err := a.backend.CacheSupplement(context.Background(), vd.BVID)
								a.showError(err)
								if err == nil {
									a.BannerInfo = "已加入弹幕字幕补存任务：" + vd.BVID
									a.RefreshData()
								}
							}
						}
						if vd.Cover != "" {
							if OutlinePillButton(c, "查看高清封面", "查看高清封面").Clicked() {
								a.OpenImageViewer([]string{vd.Cover}, nil, 0)
							}
						}
						targetURL := vd.CanonicalURL
						if targetURL == "" {
							targetURL = "https://www.bilibili.com/video/" + vd.BVID
						}
						if OutlinePillButton(c, "在浏览器中打开", "浏览器打开视频").Clicked() {
							_ = a.backend.Desktop().OpenExternalURL(targetURL)
						}
					})
				})

				// 7. Interaction Statistics Bar with Linear Icons and 万/亿 Formatting (docs/ui.md §3.4, §7)
				// Counts change all the time and are not kept, so a cached
				// video shows none (DEC-040).
				if a.CurrentLibraryDetail == nil {
					stats := []struct {
						Icon  string
						Label string
						Count int64
					}{
						{"eye", "播放", vd.Stat.View},
						{"message", "弹幕", vd.Stat.Danmaku},
						{"heart", "点赞", vd.Stat.Like},
						{"coin", "投币", vd.Stat.Coin},
						{"star", "收藏", vd.Stat.Favorite},
						{"message", "评论", vd.Stat.Reply},
						{"share", "分享", vd.Stat.Share},
					}
					ui.Row(c).
						FillWidth().
						Padding(10, 4).
						BorderWidth(1, 0, 1, 0).
						BorderColor(ColorBorder).
						AlignItems(ui.Center).
						Justify(ui.SpaceBetween).
						Wrap().
						Gap(10).
						Children(func() {
							for _, st := range stats {
								s := st
								exactTip := fmt.Sprintf("%s %d", s.Label, s.Count)
								ui.Row(c).AlignItems(ui.Center).Gap(5).Tooltip(exactTip).Children(func() {
									DrawIcon(c, s.Icon, 16, ColorTextMuted, 1.75)
									ui.Text(c, fmt.Sprintf("%s %s", s.Label, FormatCount(s.Count))).
										FontSize(13).TextColor(ColorTextMuted).SingleLine()
								})
							}
						})
				}

				// 8. Multi-part (分 P) List with Cached Stream Specs (C12, C15, docs/ui.md §3.4)
				ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
					ui.Text(c, fmt.Sprintf("视频分 P 列表（共 %d P）", len(vd.Pages))).FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
				})

				for _, page := range vd.Pages {
					p := page
					partDuration := FormatDuration(p.Duration)
					if a.CurrentLibraryDetail != nil && p.Duration <= 0 {
						partDuration = "时长未保存"
					}
					selected := a.SelectedPartPages[p.Page]
					var partSpec string
					if cachedItem != nil {
						for _, rec := range cachedItem.Parts {
							if rec.Page == p.Page {
								var specs []string
								if rec.Video.QualityName != "" {
									specs = append(specs, rec.Video.QualityName)
								}
								if rec.Video.Codecs != "" {
									specs = append(specs, strings.ToUpper(rec.Video.Codecs))
								}
								if rec.Audio.Name != "" {
									specs = append(specs, rec.Audio.Name)
								}
								partSpec = strings.Join(specs, " · ")
								break
							}
						}
					}

					ui.Row(c).
						FillWidth().
						Padding(10, 14).
						Radius(12).
						Background(ColorBgSecondary).
						Border(1, ColorBorder).
						AlignItems(ui.Center).
						Justify(ui.SpaceBetween).
						Gap(10).
						ClipX().
						Children(func() {
							ui.Row(c).AlignItems(ui.Center).Gap(10).Grow(1).ClipX().Children(func() {
								ui.Box(c).Padding(2, 8).Radius(6).Background(ColorBgMain).Border(1, ColorBorder).Children(func() {
									ui.Text(c, fmt.Sprintf("P%d", p.Page)).FontSize(12).FontWeight(600).TextColor(ColorTextMuted).SingleLine()
								})
								ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
									ui.Text(c, p.Title).FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
									if partSpec != "" {
										ui.Text(c, "已缓存规格："+partSpec).FontSize(12).FontWeight(600).TextColor(ColorDotGreen).SingleLine()
									}
								})
							})
							ui.Row(c).AlignItems(ui.Center).Gap(10).Children(func() {
								ui.Text(c, partDuration).FontSize(12).TextColor(ColorTextMuted).SingleLine()
								chk := selected
								if ui.Switch(c, &chk).Label(fmt.Sprintf("选择分P %d", p.Page)).Changed() {
									a.SelectedPartPages[p.Page] = chk
								}
							})
						})
				}
				return
			}

			if a.CurrentDynamic != nil {
				dyn := a.CurrentDynamic
				post := a.dynamicItemToPostCard(*dyn, "", "缓存该动态")
				a.renderPostCard(c, post)
				return
			}

			ui.Text(c, "未选择视频或动态，请先通过解析页打开内容。").FontSize(14).TextColor(ColorTextMuted).SingleLine()
		})
	})
}

// ============================================================================
// Route: /user (Creator Profile, Subscription Badge, Live Banner, Dynamics & Videos Tabs, C12, C13, docs/ui.md §3.4)
// ============================================================================

func (a *App) renderUserRoute(c *ui.Context) {
	if a.Opening != nil {
		ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
			ui.Column(c).FillWidth().Padding(16).Gap(14).ClipX().Children(func() {
				a.renderOpening(c)
			})
		})
		return
	}
	if a.CurrentUser == nil {
		ui.Column(c).Grow(1).FillWidth().Padding(32).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "未加载 UP 主空间信息").FontSize(15).TextColor(ColorTextMuted).SingleLine()
		})
		return
	}
	u := a.CurrentUser

	// The profile header is row 0 of the list, so it scrolls away with the
	// posts and a short window still shows them (F-058).
	if a.UserTab == "videos" {
		_, last := a.UserVidListState.Visible()
		if last-1 >= len(a.UserVideos)-2 && len(a.UserVideos) > 0 && a.UserVideosHasMore && a.UserVideosError == "" && !a.pending[fmt.Sprintf("user-videos-%d", u.Mid)] {
			a.LoadMoreUserVideos()
		}
		videos := a.UserVideos
		a.UserVidListState.Key = func(i int) any {
			if i == 0 || i > len(videos) {
				return "header"
			}
			return videos[i-1].BVID
		}
		ui.List(c, &a.UserVidListState, len(videos)+1, func(i int) {
			if i == 0 {
				a.renderUserHeader(c, u)
				return
			}
			if i > len(videos) {
				return
			}
			a.renderUserVideoRow(c, u, videos[i-1])
			if i == len(videos) {
				a.renderUserListEnd(c, a.UserVideosHasMore, a.UserVideosError, "加载下一页投稿", "已加载全部投稿", a.LoadMoreUserVideos)
			}
		}).Label("UP主投稿虚拟列表").Grow(1).FillWidth()
		return
	}

	_, last := a.UserDynListState.Visible()
	if last-1 >= len(a.UserDynamics)-2 && len(a.UserDynamics) > 0 && a.UserDynamicsHasMore && a.UserDynamicsError == "" && !a.pending[fmt.Sprintf("user-dynamics-%d", u.Mid)] {
		a.LoadMoreUserDynamics()
	}
	dynamics := a.UserDynamics
	a.UserDynListState.Key = func(i int) any {
		if i == 0 || i > len(dynamics) {
			return "header"
		}
		return dynamics[i-1].ID
	}
	ui.List(c, &a.UserDynListState, len(dynamics)+1, func(i int) {
		if i == 0 {
			a.renderUserHeader(c, u)
			return
		}
		if i > len(dynamics) {
			return
		}
		d := dynamics[i-1]
		if d.Author.Name == "" {
			d.Author.Name = u.Name
			d.Author.Mid = u.Mid
		}
		a.renderPostCard(c, a.dynamicItemToPostCard(d, "打开动态："+d.ID, "缓存动态："+d.ID))
		if i == len(dynamics) {
			a.renderUserListEnd(c, a.UserDynamicsHasMore, a.UserDynamicsError, "加载下一页动态", "已加载全部动态", a.LoadMoreUserDynamics)
		}
	}).Label("UP主动态虚拟列表").Grow(1).FillWidth()
}

func (a *App) renderUserListEnd(c *ui.Context, hasMore bool, loadErr, moreText, doneText string, more func()) {
	ui.Row(c).FillWidth().Padding(14, 16).Justify(ui.Center).Children(func() {
		if hasMore {
			if OutlinePillButton(c, moreText, moreText).Clicked() {
				more()
			}
		} else if loadErr == "" {
			ui.Text(c, doneText).FontSize(13).TextColor(ColorTextMuted)
		}
	})
}

// renderUserHeader draws the creator's banner, stats, live banner, list
// errors and tabs: one main button (subscribe), the rest in "⋯" (R13).
func (a *App) renderUserHeader(c *ui.Context, u *bilibili.UserSpace) {
	var liveRoom *live.RoomState
	for i := range a.LiveRooms {
		if a.LiveRooms[i].UID == u.Mid && a.LiveRooms[i].LiveStatus == 1 {
			liveRoom = &a.LiveRooms[i]
			break
		}
	}
	ui.Column(c).FillWidth().BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).ClipX().Children(func() {
		pal := AvatarColor(u.Name, u.Mid)
		ui.Box(c).
			FillWidth().
			Height(92).
			Draw(func(p *ui.Painter, r ui.Rect) {
				p.FillGradient(r, ui.LinearGradient{
					From:  pal.Mix(ColorTextMain, 0.25),
					To:    pal.Mix(ColorInfoBlue, 0.45),
					Angle: 120,
				}, 0)
			}).
			Children(func() {
				ui.Row(c).Fill().Padding(12, 16).AlignItems(ui.Center).Gap(12).Children(func() {
					ui.Box(c).Padding(3).Radius(999).Background(ColorWhite).Children(func() {
						a.renderAvatar(c, u.Name, u.Mid, 54, liveRoom != nil, "", nil)
					})
					ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
						ui.Text(c, u.Name).FontSize(18).FontWeight(600).TextColor(ColorWhite).SingleLine()
						ui.Text(c, fmt.Sprintf("UID %d · Lv.%d", u.Mid, u.Level)).FontSize(12).FontWeight(600).TextColor(ui.RGBA(255, 255, 255, 0.88)).SingleLine()
					})
				})
			})

		ui.Column(c).FillWidth().Padding(10, 16).Gap(8).ClipX().Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(12).Grow(1).ClipX().Children(func() {
					ui.Text(c, fmt.Sprintf("关注 %s", FormatCount(int64(u.Friend)))).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					ui.Text(c, fmt.Sprintf("粉丝 %s", FormatCount(int64(u.Fans)))).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					ui.Text(c, fmt.Sprintf("投稿 %s", FormatCount(int64(u.ArchiveCount)))).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
				})
				ui.Row(c).Shrink(0).Gap(6).AlignItems(ui.Center).Children(func() {
					ownerSub, owned := a.backend.OwnerOfAccount("bilibili", strconv.FormatInt(u.Mid, 10))
					if !owned {
						for _, s := range a.Subscriptions {
							if s.Mid == u.Mid {
								ownerSub, owned = s, true
								break
							}
						}
					}
					if owned {
						if OutlinePillButton(c, "已订阅", "进入所属订阅").Clicked() {
							a.OpenSubscriptionDetail(ownerSub)
						}
					} else if AccentPillButton(c, "订阅", "订阅该 UP 主").Clicked() {
						_, err := a.backend.UpsertSubscription(subscription.Subscription{
							Site:      "bilibili",
							Mid:       u.Mid,
							Uname:     u.Name,
							Name:      u.Name,
							Scope:     "both",
							AutoCache: "inherit",
							Enabled:   true,
						})
						a.showError(err)
						a.RefreshData()
					}
					targetURL := u.CanonicalURL
					if targetURL == "" {
						targetURL = fmt.Sprintf("https://space.bilibili.com/%d", u.Mid)
					}
					MoreMenuButton(c, "空间更多", 32, nil, func(m *ui.Menu) {
						if m.Item("在浏览器中打开空间").Chosen() {
							_ = a.backend.Desktop().OpenExternalURL(targetURL)
						}
						if liveRoom != nil {
							roomID := liveRoom.RoomID
							if m.Item("在浏览器中观看直播").Chosen() {
								_ = a.backend.OpenLiveRoomInBrowser(roomID)
							}
						}
					})
				})
			})

			if u.Sign != "" {
				ui.Text(c, u.Sign).FontSize(13).TextColor(ColorTextMuted).MaxLines(2)
			}

			// Live banner (R4, docs/ui.md §3.4): the record toggle is its one button.
			if liveRoom != nil {
				room := *liveRoom
				qualSpec := bilibili.FormatLiveQualitySpec(room.QualityName, room.Quality, room.Codec)
				ui.Row(c).
					FillWidth().
					Padding(8, 12).
					Radius(10).
					Background(ColorLiveBg).
					Border(1, ColorLive).
					AlignItems(ui.Center).
					Justify(ui.SpaceBetween).
					Gap(8).
					ClipX().
					Children(func() {
						ui.Row(c).AlignItems(ui.Center).Gap(8).Grow(1).ClipX().Children(func() {
							ui.Box(c).Shrink(0).Padding(2, 8).Radius(999).Background(ColorLive).Children(func() {
								ui.Text(c, "直播中").FontSize(11).FontWeight(600).TextColor(ColorWhite).SingleLine()
							})
							ui.Text(c, room.Title).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine().Tooltip(fmt.Sprintf("房间号 %d", room.RoomID))
							if qualSpec != "" {
								ui.Text(c, "· "+qualSpec).Shrink(0).FontSize(12).FontWeight(600).TextColor(ColorLive).SingleLine()
							}
						})
						if room.Recording {
							if DangerOutlinePillButton(c, "停止录制", "空间停止录制").Clicked() {
								a.stopLiveRecording(room.RoomID)
							}
						} else if AccentPillButton(c, "录制", "空间开始录制").Clicked() {
							a.startLiveRecording(room.RoomID)
						}
					})
			}

			// Partial-failure error with Retry while keeping page 1 (C13)
			if a.UserTab != "videos" && a.UserDynamicsError != "" {
				a.renderUserListError(c, a.UserDynamicsError, "重试加载动态", a.LoadMoreUserDynamics)
			}
			if a.UserTab == "videos" && a.UserVideosError != "" {
				a.renderUserListError(c, a.UserVideosError, "重试加载投稿", a.LoadMoreUserVideos)
			}
		})

		UnderlineTabs(c, a.UserTab, []TabOption{
			{Key: "dynamics", Text: "动态", AccessLabel: "空间动态标签"},
			{Key: "videos", Text: "投稿", AccessLabel: "空间投稿标签"},
		}, func(key string) {
			a.UserTab = key
			if key == "videos" && a.UserVideosPage == 0 && a.UserVideosError == "" {
				a.LoadMoreUserVideos()
			}
		})
	})
}

func (a *App) renderUserListError(c *ui.Context, msg, retryLabel string, retry func()) {
	ui.Row(c).FillWidth().Padding(8, 12).Radius(10).Background(ColorErrorBg).Border(1, ui.RGBA(180, 35, 24, 0.25)).AlignItems(ui.Center).Gap(8).ClipX().Children(func() {
		DrawIcon(c, "alert-circle", 16, ColorError, 1.8)
		ui.Text(c, msg).FontSize(12).TextColor(ColorError).MaxLines(2).Grow(1)
		if OutlinePillButton(c, "重试", retryLabel).Clicked() {
			retry()
		}
	})
}

// renderUserVideoRow is one upload: clicking it opens the video; caching and
// the browser are in its "⋯" (R13, F-056).
func (a *App) renderUserVideoRow(c *ui.Context, u *bilibili.UserSpace, v bilibili.UserVideoItem) {
	itemKey := library.CanonicalItemKey("bilibili", "video", v.BVID)
	dotStatus := ""
	var relCover string
	if it, ok, err := a.backend.GetLibraryItem(itemKey); err == nil && ok {
		dotStatus = library.ComputeDotState(it).Dot
		for _, m := range it.Media {
			if m.Role == "cover" && relCover == "" {
				relCover = m.Path
			}
		}
	}
	ui.Row(c).
		FillWidth().
		Padding(12, 16).
		Gap(8).
		AlignItems(ui.Center).
		BorderWidth(0, 0, 1, 0).
		BorderColor(ColorBorder).
		ClipX().
		Children(func() {
			body := ui.ButtonBase(c).Label("打开视频：" + v.BVID).Grow(1).Padding(0).Gap(12).AlignItems(ui.Center).Justify(ui.Start).ClipX()
			body.Children(func() {
				a.renderMediaThumb(c, itemKey, relCover, v.Title, v.Length, dotStatus, dotStatus == "green", 144, 82, v.Cover)
				ui.Column(c).Grow(1).Gap(5).ClipX().Children(func() {
					ui.Text(c, v.Title).FontSize(15).FontWeight(600).TextColor(ColorTextMain).MaxLines(2)
					ui.Text(c, fmt.Sprintf("时长 %s · 播放 %s", v.Length, FormatCount(int64(v.PlayCount)))).
						FontSize(12).TextColor(ColorTextMuted).SingleLine()
				})
			})
			if body.Clicked() {
				a.OpenVideo(v.BVID, v.Title, u.Name, u.Mid, v.Cover, v.Length)
			}
			open := a.PostMenuKey == "upload:"+v.BVID
			MoreMenuButton(c, "投稿更多："+v.BVID, 28, &open, func(m *ui.Menu) {
				if dotStatus == "" && m.Item("缓存").Chosen() {
					_, err := a.backend.EnqueueVideoCache(context.Background(), download.VideoEnqueueRequest{BVID: v.BVID})
					a.showError(err)
					if err == nil {
						a.BannerInfo = "已加入缓存队列：" + v.BVID
					}
					a.RefreshData()
				}
				if m.Item("在浏览器中打开").Chosen() {
					_ = a.backend.Desktop().OpenExternalURL("https://www.bilibili.com/video/" + v.BVID)
				}
			})
			keyedMenuOpen(&a.PostMenuKey, "upload:"+v.BVID, open)
		})
}

// ============================================================================
// Route: /subscriptions helper
// ============================================================================

// ============================================================================
// Route: /library (Local Media Library, Filtering, Delete Confirmation, C25, C26, docs/ui.md §3.6)
// ============================================================================

func (a *App) renderTasksRoute(c *ui.Context) {
	ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
		ui.Column(c).FillWidth().Padding(16).Gap(12).ClipX().Children(func() {
			// 1. Upgrade Candidates Banner
			if len(a.UpgradeCandidates) > 0 {
				ui.Column(c).FillWidth().Padding(14).Radius(16).Background(ColorInfoBlueBg).Border(1, ColorInfoBlue).Gap(8).ClipX().Children(func() {
					ui.Text(c, fmt.Sprintf("可升级画质视频（%d）", len(a.UpgradeCandidates))).FontSize(14).FontWeight(600).TextColor(ColorInfoBlue).SingleLine()
					for _, cand := range a.UpgradeCandidates {
						it := cand
						ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
							ui.Text(c, fmt.Sprintf("%s（%s）", it.Title, it.UpgradeHint)).
								FontSize(13).TextColor(ColorTextMain).SingleLine().Grow(1)
							if AccentPillButton(c, "升级缓存", "升级缓存："+it.RawID).Clicked() {
								if it.ItemKey != "" {
									_, _ = a.backend.Upgrade(context.Background(), it.ItemKey)
								} else {
									_, _ = a.backend.EnqueueVideoCache(context.Background(), download.VideoEnqueueRequest{
										BVID: it.RawID,
									})
								}
								a.RefreshData()
							}
						})
					}
				})
			}

			// 2. Active Live Recordings at the top (R4, docs/ui.md §3.6)
			for _, roomItem := range a.LiveRooms {
				room := roomItem
				if room.Recording {
					qualSpec := bilibili.FormatLiveQualitySpec(room.QualityName, room.Quality, room.Codec)
					ui.Row(c).
						FillWidth().
						Padding(14).
						Radius(16).
						Background(ColorLiveBg).
						Border(1, ColorLive).
						AlignItems(ui.Center).
						Justify(ui.SpaceBetween).
						Gap(12).
						ClipX().
						Children(func() {
							a.renderAvatar(c, room.Uname, room.UID, 42, true, "", nil)
							ui.Column(c).Grow(1).Gap(4).ClipX().Children(func() {
								ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
									ui.Box(c).Shrink(0).Padding(2, 8).Radius(999).Background(ColorLive).Children(func() {
										ui.Text(c, "录制中").FontSize(11).FontWeight(600).TextColor(ColorWhite).SingleLine()
									})
									ui.Text(c, room.Uname+" · "+room.Title).FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
									if qualSpec != "" {
										ui.Text(c, "· "+qualSpec).Shrink(0).FontSize(12).FontWeight(600).TextColor(ColorLive).SingleLine()
									}
								})
								recMeta := fmt.Sprintf("已录时长 %s · 大小 %s · 速度 %s/s · 第 %d 段",
									FormatDuration(int(room.RecordDurationSec)),
									FormatBytes(room.RecordBytes),
									FormatBytes(room.RecordSpeedBps),
									max(1, room.RecordSegments))
								if qualSpec != "" {
									recMeta += " · " + qualSpec
								}
								ui.Text(c, recMeta).FontSize(12).TextColor(ColorTextMuted).SingleLine()
							})
							if DangerOutlinePillButton(c, "停止录制", fmt.Sprintf("任务停止录制 %d", room.RoomID)).Clicked() {
								a.stopLiveRecording(room.RoomID)
							}
						})
				}
			}

			if a.TaskActionError != "" {
				ui.Text(c, a.TaskActionError).FontSize(13).TextColor(ColorError).MaxLines(2)
			}
			finished := 0
			for _, t := range a.Tasks {
				if t.Status == "completed" || t.Status == "canceled" {
					finished++
				}
			}
			if finished > 0 {
				ui.Row(c).FillWidth().Justify(ui.End).Children(func() {
					if OutlinePillButton(c, fmt.Sprintf("清除已结束（%d）", finished), "清除已结束的任务").Clicked() {
						_, err := a.backend.RemoveFinishedTasks()
						a.finishTaskAction(err)
					}
				})
			}

			if len(a.Tasks) == 0 {
				ui.Column(c).FillWidth().Padding(40, 16).AlignItems(ui.Center).Gap(8).Children(func() {
					ui.Text(c, "当前没有缓存任务").FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					ui.Text(c, "在动态或视频详情上点「缓存」后，下载进度与子文件详情会显示在这里。").FontSize(13).TextColor(ColorTextMuted)
				})
				return
			}

			// 3. Download Task Cards with Visual Progress Bar & Expandable Per-File Detail (FR-CACHE-009)
			for _, t := range a.Tasks {
				task := t
				statusLabel := taskStatusText(task.Status)
				barCol := ColorInfoBlue
				switch task.Status {
				case "completed":
					barCol = ColorDotGreen
				case "paused":
					barCol = ColorDotYellow
				case "failed":
					barCol = ColorDotRed
				}
				isExpanded := a.ExpandedTaskID == task.TaskID

				ui.Column(c).
					FillWidth().
					Padding(14).
					Radius(16).
					Background(ColorBgMain).
					Border(1, ColorBorder).
					Gap(8).
					ClipX().
					Children(func() {
						ui.Row(c).FillWidth().AlignItems(ui.Start).Gap(12).ClipX().Children(func() {
							a.renderMediaThumb(c, task.ItemKey, "", task.Title, "", task.Status, false, 96, 58)

							ui.Column(c).Grow(1).Gap(6).ClipX().Children(func() {
								ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
									ui.Row(c).AlignItems(ui.Center).Gap(6).Grow(1).ClipX().Children(func() {
										DrawStatusDotFull(c, task.Status, task.Progress, task.Status == "paused", "任务状态："+task.Status)
										ui.Text(c, fmt.Sprintf("%s (%s)", task.Title, task.TargetID)).
											FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
										if task.Source == "auto" {
											ui.Box(c).Padding(1, 6).Radius(4).Background(ColorBgSecondary).Border(1, ColorBorder).Children(func() {
												ui.Text(c, "自动").FontSize(11).FontWeight(600).TextColor(ColorTextMuted).SingleLine()
											})
										}
									})
									ui.Text(c, fmt.Sprintf("%s · %.0f%% (%s / %s)",
										statusLabel,
										task.Progress*100,
										FormatBytes(task.BytesDownloaded),
										FormatBytes(task.TotalBytes))).
										FontSize(12).FontWeight(600).TextColor(ColorTextMuted).SingleLine()
								})

								// Visual progress bar
								ui.Box(c).FillWidth().Height(6).Radius(3).Background(ColorBorder).Clip().Draw(func(p *ui.Painter, r ui.Rect) {
									frac := float32(max(0.0, min(1.0, task.Progress)))
									if frac > 0 {
										p.Fill(ui.Rect{X: r.X, Y: r.Y, W: r.W * frac, H: r.H}, barCol, 3)
									}
								})

								if task.QualityHint != "" {
									ui.Text(c, "画质与音轨："+task.QualityHint).FontSize(12).FontWeight(600).TextColor(ColorInfoBlue).SingleLine()
								}
								lastErr := firstNonEmptyStrUI(task.LastError, task.ErrorMessage)
								if lastErr != "" {
									ui.Row(c).FillWidth().Padding(6, 10).Radius(8).Background(ColorErrorBg).AlignItems(ui.Center).Gap(6).Children(func() {
										DrawIcon(c, "alert-circle", 14, ColorError, 1.8)
										errLine := "原因：" + lastErr
										if task.ErrorKind != "" {
											errLine = fmt.Sprintf("原因 [%s]：%s", task.ErrorKind, lastErr)
										}
										ui.Text(c, errLine).
											FontSize(12).TextColor(ColorError).MaxLines(2).Grow(1)
									})
								}

								if a.TaskRemoveConfirmID == task.TaskID {
									ui.Row(c).
										FillWidth().
										Padding(8, 12).
										Radius(10).
										Background(ColorWarnBg).
										Border(1, ColorDotYellow).
										AlignItems(ui.Center).
										Justify(ui.SpaceBetween).
										Gap(8).
										Children(func() {
											ui.Text(c, "确认移除该任务？会删除未完成的临时文件，已缓存完成的文件会保留。").
												FontSize(12).FontWeight(600).TextColor(ColorTextMain).MaxLines(2).Grow(1)
											ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
												if OutlinePillButton(c, "取消", "取消移除任务："+task.TaskID).Clicked() {
													a.TaskRemoveConfirmID = ""
												}
												if DangerOutlinePillButton(c, "确认移除", "确认移除任务："+task.TaskID).Clicked() {
													a.TaskRemoveConfirmID = ""
													a.finishTaskAction(a.backend.RemoveTask(task.TaskID))
												}
											})
										})
								}

								// Task Actions Row
								ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Wrap().ClipX().Children(func() {
									if task.Status == "running" || task.Status == "queued" {
										if OutlinePillButton(c, "暂停", "暂停任务："+task.TaskID).Clicked() {
											_, err := a.backend.PauseTask(task.TaskID)
											a.finishTaskAction(err)
										}
									}
									if task.Status == "paused" || strings.HasPrefix(task.Status, "paused_") {
										if AccentPillButton(c, "继续", "继续任务："+task.TaskID).Clicked() {
											_, err := a.backend.ResumeTask(task.TaskID)
											a.finishTaskAction(err)
										}
									}
									if task.Status == "failed" || task.Status == "partial" {
										if AccentPillButton(c, "重试", "重试任务："+task.TaskID).Clicked() {
											_, err := a.backend.RetryTask(task.TaskID)
											a.finishTaskAction(err)
										}
									}
									switch task.Status {
									case "failed", "partial":
										if OutlinePillButton(c, "移除", "移除任务："+task.TaskID).Clicked() {
											a.TaskRemoveConfirmID = task.TaskID
										}
									case "completed", "canceled":
										// Removing a completed/canceled task clears the record directly; cached files stay in the library.
										if OutlinePillButton(c, "移除", "移除任务："+task.TaskID).Clicked() {
											a.finishTaskAction(a.backend.RemoveTask(task.TaskID))
										}
									default:
										if DangerOutlinePillButton(c, "取消", "取消任务："+task.TaskID).Clicked() {
											_, err := a.backend.CancelTask(task.TaskID)
											a.finishTaskAction(err)
										}
									}
									expandLabel := "展开详情"
									if isExpanded {
										expandLabel = "收起详情"
									}
									if OutlinePillButton(c, expandLabel, "展开任务详情："+task.TaskID).Clicked() {
										if isExpanded {
											a.ExpandedTaskID = ""
										} else {
											a.ExpandedTaskID = task.TaskID
										}
									}
								})
							})
						})

						// Expanded Per-File Progress & Desensitized Attempt Log (FR-CACHE-009)
						if isExpanded {
							detail, ok, _ := a.backend.TaskDetail(task.TaskID)
							files := task.Files
							attempts := task.Attempts
							if ok {
								if len(detail.Files) > 0 {
									files = detail.Files
								}
								if len(detail.Attempts) > 0 {
									attempts = detail.Attempts
								}
							}

							ui.Column(c).
								FillWidth().
								Padding(10, 12).
								Radius(12).
								Background(ColorBgSecondary).
								Border(1, ColorBorder).
								Gap(6).
								ClipX().
								Children(func() {
									ui.Text(c, fmt.Sprintf("逐文件进度清单（%d 个文件）", len(files))).
										FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
									if len(files) == 0 {
										ui.Text(c, "P01 视频流 (AVC/AV1) · P01 音轨流 (Hi-Res/192K) · P01.danmaku.xml · cover.png").
											FontSize(12).TextColor(ColorTextMuted).SingleLine()
									}
									for _, f := range files {
										fl := f
										ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
											ui.Text(c, fmt.Sprintf("[%s] %s", fl.Role, fl.Path)).FontSize(12).FontWeight(600).TextColor(ColorTextMain).SingleLine().Grow(1)
											ui.Text(c, fmt.Sprintf("%s · %s / %s",
												fl.Status,
												FormatBytes(fl.DownloadedBytes),
												FormatBytes(fl.TotalBytes))).FontSize(12).TextColor(ColorTextMuted).SingleLine()
										})
									}
									if len(attempts) > 0 {
										ui.Text(c, fmt.Sprintf("最近分段重试记录（已脱敏，共 %d 条）", len(attempts))).
											FontSize(12).FontWeight(600).TextColor(ColorTextMain).SingleLine()
										limit := min(len(attempts), 5)
										for i := 0; i < limit; i++ {
											att := attempts[i]
											ui.Text(c, fmt.Sprintf("%s · 文件 %s · 节点 %s · 偏移 %s · 错误 %s (第 %d 次)",
												att.Time, att.File, att.Host, FormatBytes(att.Offset), att.ErrorKind, att.Attempt)).
												FontSize(11).TextColor(ColorTextMuted).SingleLine()
										}
									}
								})
						}
					})
			}
		})
	})
}

func taskStatusText(status string) string {
	switch status {
	case "completed":
		return "已完成"
	case "running":
		return "正在缓存"
	case "queued":
		return "排队中"
	case "paused":
		return "已暂停"
	case "failed":
		return "失败"
	case "partial":
		return "部分完成"
	case "canceled":
		return "已取消"
	default:
		return status
	}
}

// ============================================================================
// Route: /settings (7 Categories, 2-Column on >=1000, Secondary Nav on <1000, docs/ui.md §3.7)
// ============================================================================

var settingsCategories = []struct {
	Key   string
	Icon  string
	Label string
}{
	{"account", "user", "账号"},
	{"download", "download", "缓存与下载"},
	{"quality", "play", "画质与音质"},
	{"rules", "grid", "缓存规则"},
	{"subscription", "refresh", "订阅与直播"},
	{"storage", "folder", "存储"},
	{"general", "sliders", "常规"},
	{"logs", "logs", "日志"},
	{"about", "check-circle", "关于"},
}

func settingsCategoryLabel(key string) string {
	for _, cat := range settingsCategories {
		if cat.Key == key {
			return cat.Label
		}
	}
	return "设置"
}

// renderSettingsRoute shows the chosen category; the categories themselves
// are the left sidebar while Settings is open (docs/ui.md §3.7).
func (a *App) renderSettingsRoute(c *ui.Context, centerWidth float32) {
	ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
		ui.Column(c).FillWidth().Padding(18, 20).Gap(14).ClipX().Children(func() {
			a.renderSettingsCategoryContent(c)
		})
	})
}

// renderSettingsSidebar replaces the main navigation while Settings is open:
// a way back, then one entry per category, each an icon and a name only, as
// the main navigation's entries.
func (a *App) renderSettingsSidebar(c *ui.Context, compact bool) {
	ui.Column(c).Fill().Padding(10, 8).Gap(2).ClipX().Children(func() {
		back := ui.ButtonBase(c).Label("返回").Tooltip("返回").FillWidth().Radius(8).Padding(8, 10).Gap(10).AlignItems(ui.Center)
		if compact {
			back.Justify(ui.Center)
		} else {
			back.Justify(ui.Start)
		}
		if back.Hovered() {
			back.Background(ColorHover)
		}
		back.Children(func() {
			DrawIcon(c, "arrow-left", 18, ColorTextMain, 1.8)
			if !compact {
				ui.Text(c, "设置").FontSize(16).FontWeight(600).TextColor(ColorTextMain).SingleLine()
			}
		})
		if back.Clicked() {
			a.NavigateBack()
		}
		ui.Box(c).Height(8)
		for _, cat := range settingsCategories {
			ct := cat
			active := a.SettingsCategory == ct.Key
			var clicked bool
			if compact {
				clicked = panelItem(c, "设置分类："+ct.Label, active, func() {
					col := ColorTextMuted
					if active {
						col = ColorAccent
					}
					ui.Row(c).Grow(1).Justify(ui.Center).Children(func() { DrawIcon(c, ct.Icon, 18, col, 1.6) })
				})
			} else {
				clicked = panelIconItem(c, ct.Icon, ct.Label, "设置分类："+ct.Label, active)
			}
			if clicked {
				a.SettingsCategory = ct.Key
			}
		}
	})
}

func (a *App) renderSettingsCategoryContent(c *ui.Context) {
	switch a.SettingsCategory {
	case "rules":
		a.renderCacheRulesSettings(c)
	case "account":
		acct := a.backend.AccountStatus()
		ui.Column(c).FillWidth().Gap(14).ClipX().Children(func() {

			// Profile row
			ui.Row(c).FillWidth().Padding(14, 0).BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).AlignItems(ui.Center).Gap(12).Children(func() {
				a.renderAvatar(c, firstNonEmptyStrUI(acct.Uname, "未登录"), acct.Mid, 48, false, "", nil)
				ui.Column(c).Grow(1).Gap(3).Children(func() {
					name := acct.Uname
					if name == "" {
						if acct.LoggedIn {
							name = fmt.Sprintf("UID %d", acct.Mid)
						} else {
							name = "未登录"
						}
					}
					ui.Text(c, name).FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					sourceText := "未登录"
					if acct.Source == "qr" {
						sourceText = "扫码登录"
					} else if acct.Source == "manual" {
						sourceText = "手动凭证（粘贴 Cookie）"
					}
					ui.Text(c, fmt.Sprintf("UID %d · %s", acct.Mid, sourceText)).
						FontSize(13).TextColor(ColorTextMuted).SingleLine()
				})
			})

			// Auto-renewal status row (docs/ui.md §3.7, DEC-031, H-001 step 22)
			ui.Column(c).FillWidth().Padding(14, 0).BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).Gap(6).Children(func() {
				ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
					ui.Text(c, "自动续期").FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					if acct.LoggedIn && acct.Source == "qr" {
						ui.Box(c).Padding(2, 8).Radius(999).Background(ColorInfoBlueBg).Children(func() {
							ui.Text(c, "可用 · 自动续期已开启").FontSize(12).FontWeight(600).TextColor(ColorInfoBlue).SingleLine()
						})
					} else {
						ui.Box(c).Padding(2, 8).Radius(999).Background(ColorWarnBg).Children(func() {
							ui.Text(c, "不可用").FontSize(12).FontWeight(600).TextColor(ColorDotYellow).SingleLine()
						})
					}
				})
				if acct.LoggedIn && acct.Source == "qr" {
					lastChk := acct.LastCheckedAt
					if lastChk == "" {
						lastChk = "刚刚"
					}
					ui.Text(c, "扫码登录支持后台自动续期 · 上次检查："+lastChk).FontSize(12).TextColor(ColorTextMuted).MaxLines(2)
				} else {
					ui.Text(c, "不可用：粘贴的 Cookie 与浏览器共用会话，续期会让浏览器那边掉线。过期后请重新粘贴，或改用扫码登录。").
						FontSize(12).TextColor(ColorTextMuted).MaxLines(3)
				}
			})

			// Actions row
			ui.Row(c).Gap(8).Wrap().Children(func() {
				if AccentPillButton(c, "重新登录 / 更换账号", "进入登录/更换账号").Clicked() {
					a.Navigate(RouteLogin)
				}
				if acct.LoggedIn && acct.Source == "qr" {
					if OutlinePillButton(c, "检查并续期 Cookie", "检查并续期 Cookie").Clicked() {
						a.refreshAccount()
					}
				}
				if acct.LoggedIn || acct.State == "expired" {
					if DangerOutlinePillButton(c, "退出登录", "退出登录").Clicked() {
						a.ConfirmLogoutOpen = true
					}
				}
			})
		})

	case "download":
		ui.Column(c).FillWidth().Gap(12).ClipX().Children(func() {

			concStr := strconv.Itoa(a.DraftSettings.Download.MaxConcurrentTasks)
			if concStr == "0" {
				concStr = "2"
			}
			if settingChoiceRow(c, "同时下载数", "", "选择并发任务数", &concStr, numberChoices("1", "2", "3", "4")) {
				if v, err := strconv.Atoi(concStr); err == nil {
					a.DraftSettings.Download.MaxConcurrentTasks = v
					a.DraftSettings.Concurrency = v
					a.saveSettings()
				}
			}

			if a.DraftSettings.MultiPart == "" {
				a.DraftSettings.MultiPart = "all"
			}
			if settingChoiceRow(c, "多 P 视频", "", "选择多P策略", &a.DraftSettings.MultiPart, []settingChoice{{"all", "缓存全部分 P"}, {"first", "只缓存第 1 P"}}) {
				a.saveSettings()
			}

			if settingSwitchRow(c, "缓存弹幕", "", "开关缓存弹幕", &a.DraftSettings.Download.SaveDanmaku) {
				a.DraftSettings.Danmaku = a.DraftSettings.Download.SaveDanmaku
				a.saveSettings()
			}
			if settingSwitchRow(c, "缓存字幕", "", "开关缓存字幕", &a.DraftSettings.Download.SaveSubtitles) {
				a.DraftSettings.Subtitles = a.DraftSettings.Download.SaveSubtitles
				a.saveSettings()
			}
			if settingSwitchRow(c, "包括 AI 字幕", "", "开关AI字幕", &a.DraftSettings.AISubtitles) {
				a.saveSettings()
			}
			if settingSwitchRow(c, "自动保存视频封面", "", "开关保存封面", &a.DraftSettings.Download.SaveCover) {
				a.saveSettings()
			}
		})

	case "quality":
		ui.Column(c).FillWidth().Gap(12).ClipX().Children(func() {

			if a.DraftSettings.VideoQuality == "" {
				a.DraftSettings.VideoQuality = "best"
			}
			if settingChoiceRow(c, "视频清晰度", "", "选择首选画质", &a.DraftSettings.VideoQuality, []settingChoice{{"best", "能拿到的最高（推荐）"}, {"2160", "不超过 4K"}, {"1080", "不超过 1080P"}, {"720", "不超过 720P"}}) {
				a.saveSettings()
			}

			// Codec Preference Order List (AVC / AV1 / HEVC, docs/ui.md §3.7)
			if len(a.DraftSettings.CodecOrder) == 0 {
				a.DraftSettings.CodecOrder = []string{"avc", "av1", "hevc"}
			}
			ui.Column(c).FillWidth().Padding(14, 0).BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).Gap(8).Children(func() {
				ui.Text(c, "编码偏好顺序").FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
				codecDesc := map[string]string{
					"avc":  "AVC (H.264)",
					"av1":  "AV1",
					"hevc": "HEVC (H.265) · 部分电脑无法在应用内播放",
				}
				for idx, cCode := range a.DraftSettings.CodecOrder {
					i := idx
					code := cCode
					label := codecDesc[code]
					if label == "" {
						label = strings.ToUpper(code)
					}
					ui.Row(c).FillWidth().Padding(6, 0).AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
						ui.Text(c, fmt.Sprintf("%d. %s", i+1, label)).FontSize(13).TextColor(ColorTextMain).SingleLine().Grow(1)
						ui.Row(c).Gap(4).Children(func() {
							if OutlinePillButton(c, "↑ 上移", fmt.Sprintf("编码上移 %s", code)).Disabled(i == 0).Clicked() {
								a.DraftSettings.CodecOrder[i-1], a.DraftSettings.CodecOrder[i] = a.DraftSettings.CodecOrder[i], a.DraftSettings.CodecOrder[i-1]
								a.DraftSettings.Download.PreferredVideoCodec = a.DraftSettings.CodecOrder[0]
								a.saveSettings()
							}
							if OutlinePillButton(c, "↓ 下移", fmt.Sprintf("编码下移 %s", code)).Disabled(i+1 >= len(a.DraftSettings.CodecOrder)).Clicked() {
								a.DraftSettings.CodecOrder[i+1], a.DraftSettings.CodecOrder[i] = a.DraftSettings.CodecOrder[i], a.DraftSettings.CodecOrder[i+1]
								a.DraftSettings.Download.PreferredVideoCodec = a.DraftSettings.CodecOrder[0]
								a.saveSettings()
							}
						})
					})
				}
			})

			if a.DraftSettings.AudioQuality == "" {
				a.DraftSettings.AudioQuality = "best"
			}
			if settingChoiceRow(c, "音质", "", "选择首选音质", &a.DraftSettings.AudioQuality, []settingChoice{{"best", "能拿到的最高（推荐）"}, {"192k", "192K"}}) {
				a.saveSettings()
			}

			if settingSwitchRow(c, "优先 Hi-Res 无损音轨", "", "开关无损音轨", &a.DraftSettings.Download.PreferLosslessAudio) {
				a.saveSettings()
			}
			if settingSwitchRow(c, "优先杜比全景声", "", "开关杜比全景声", &a.DraftSettings.Download.PreferDolbyAudio) {
				a.saveSettings()
			}
			if settingSwitchRow(c, "打开已缓存视频时检查画质更新", "", "开关打开时检查升级", &a.DraftSettings.UpgradeCheckOnOpen) {
				a.saveSettings()
			}
		})

	case "subscription":
		ui.Column(c).FillWidth().Gap(12).ClipX().Children(func() {

			if settingSwitchRow(c, "定时检查更新", "只在 TRACE 打开时检查；遇到风控会自动放慢", "开关定时订阅", &a.DraftSettings.Subscription.Enabled) {
				a.DraftSettings.ScheduledCheck = a.DraftSettings.Subscription.Enabled
				a.saveSettings()
			}

			chkMinStr := strconv.Itoa(a.DraftSettings.Subscription.IntervalMinutes)
			if chkMinStr == "0" {
				chkMinStr = "60"
			}
			if settingChoiceRow(c, "检查间隔", "", "选择订阅检查间隔", &chkMinStr, []settingChoice{{"30", "30 分钟"}, {"60", "1 小时"}, {"120", "2 小时"}, {"360", "6 小时"}}) {
				if v, err := strconv.Atoi(chkMinStr); err == nil {
					a.DraftSettings.Subscription.IntervalMinutes = v
					a.DraftSettings.CheckIntervalMin = v
					a.saveSettings()
				}
			}

			if settingSwitchRow(c, "开播状态检查", "", "开关直播轮询", &a.DraftSettings.Live.PollEnabled) {
				a.saveSettings()
			}

			liveMinStr := strconv.Itoa(a.DraftSettings.LiveCheckIntervalMin)
			if liveMinStr == "0" {
				liveMinStr = "3"
			}
			if settingChoiceRow(c, "开播检查间隔", "", "选择直播轮询间隔", &liveMinStr, []settingChoice{{"1", "1 分钟"}, {"3", "3 分钟"}, {"5", "5 分钟"}, {"10", "10 分钟"}}) {
				if v, err := strconv.Atoi(liveMinStr); err == nil {
					a.DraftSettings.LiveCheckIntervalMin = v
					a.DraftSettings.Live.PollIntervalSeconds = v * 60
					a.saveSettings()
				}
			}

			maxRecStr := strconv.Itoa(a.DraftSettings.MaxRecordings)
			if maxRecStr == "0" {
				maxRecStr = "2"
			}
			if settingChoiceRow(c, "同时录制数", "", "选择最大录制数", &maxRecStr, numberChoices("1", "2", "3", "4")) {
				if v, err := strconv.Atoi(maxRecStr); err == nil {
					a.DraftSettings.MaxRecordings = v
					a.DraftSettings.Live.MaxConcurrentRecordings = v
					a.saveSettings()
				}
			}
		})

	case "storage":
		diag := a.backend.DiagnosticsSnapshot()
		loc := a.backend.Locations()
		var totalBytes int64
		for _, it := range a.LibraryItems {
			totalBytes += it.TotalBytes
		}
		editionLabel := "正式版 (stable)"
		if diag.Channel == "dev" {
			editionLabel = "开发版 (trace-dev)"
		}

		ui.Column(c).FillWidth().Gap(12).ClipX().Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
				ui.Box(c)
				ui.Box(c).Padding(2, 10).Radius(999).Background(ColorAccentBg).Children(func() {
					ui.Text(c, editionLabel).FontSize(12).FontWeight(600).TextColor(ColorAccent).SingleLine()
				})
			})

			// Directory overview cards (InstallDir, DataDir, CacheDir, Library usage - H-001 step 24)
			ui.Column(c).FillWidth().Padding(14, 0).BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).Gap(8).ClipX().Children(func() {
				ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
					ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
						ui.Text(c, "安装位置").FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
						ui.Text(c, firstNonEmptyStrUI(loc.InstallDir, ".")).FontSize(12).TextColor(ColorTextMuted).SingleLine()
					})
					if OutlinePillButton(c, "打开文件夹", "打开安装目录").Clicked() {
						_ = a.backend.Desktop().OpenPath(firstNonEmptyStrUI(loc.InstallDir, "."))
					}
				})
				ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
					ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
						ui.Text(c, "数据目录").FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
						ui.Text(c, diag.DataDir).FontSize(12).TextColor(ColorTextMuted).SingleLine()
					})
					if OutlinePillButton(c, "打开数据文件夹", "打开数据文件夹").Clicked() {
						_ = a.backend.OpenDataFolder("data")
					}
				})
				ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
					ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
						ui.Text(c, fmt.Sprintf("缓存位置（已缓存 %d 项 · %s）", len(a.LibraryItems), FormatBytes(totalBytes))).
							FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
						ui.Text(c, diag.CacheDir).FontSize(12).TextColor(ColorTextMuted).SingleLine()
					})
					ui.Row(c).Gap(6).Wrap().Children(func() {
						if OutlinePillButton(c, "打开缓存文件夹", "打开缓存文件夹").Clicked() {
							_ = a.backend.OpenDataFolder("cache")
						}
						if OutlinePillButton(c, "打开日志文件夹", "打开日志文件夹").Clicked() {
							_ = a.backend.OpenDataFolder("logs")
						}
						if OutlinePillButton(c, "重建索引", "重建索引").Clicked() {
							rep, err := a.backend.RebuildLibraryIndex()
							if err != nil {
								a.BannerError = err.Error()
							} else {
								a.RefreshData()
								a.BannerInfo = fmt.Sprintf("索引重建完成：共 %d 条内容", rep.Indexed)
							}
						}
					})
				})
			})

			// Change Cache Directory
			ui.Column(c).FillWidth().Padding(14, 0).BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).Gap(8).ClipX().Children(func() {
				ui.Text(c, "缓存保存位置").FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
				ui.Text(c, "只影响之后的缓存。").FontSize(12).TextColor(ColorTextMuted).SingleLine()
				ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).ClipX().Children(func() {
					ui.TextInput(c, &a.CacheDirInput).Placeholder("新缓存目录路径").Label("缓存目录输入框").Grow(1)
					if AccentPillButton(c, "应用缓存目录", "应用缓存目录").Clicked() {
						rep, err := a.backend.MigrateCacheDir(a.CacheDirInput, a.MoveExistingFiles)
						if err != nil {
							a.BannerError = err.Error()
						} else {
							a.RefreshData()
							a.BannerInfo = fmt.Sprintf("已切换缓存目录，索引 %d 项", rep.Indexed)
						}
					}
				})
				ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
					ui.Text(c, "同时迁移已有缓存文件（关闭则将旧缓存留在原处继续访问）").FontSize(12).TextColor(ColorTextMuted).SingleLine()
					ui.Switch(c, &a.MoveExistingFiles).Label("迁移已有文件开关")
				})
			})

			// Add Existing Cache Folder (H-001 step 25)
			ui.Column(c).FillWidth().Padding(14, 0).BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).Gap(8).ClipX().Children(func() {
				ui.Text(c, "添加已有的缓存文件夹").FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
				ui.Text(c, "扫描后接入媒体库与主页，文件留在原处。").FontSize(12).TextColor(ColorTextMuted).MaxLines(2)
				ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).ClipX().Children(func() {
					ui.TextInput(c, &a.AddLibraryRootInput).
						Placeholder("输入已有缓存根目录或 trace-data 文件夹路径").
						Label("添加已有文件夹输入框").
						Grow(1)
					if AccentOutlinePillButton(c, "从已有文件夹添加", "从已有文件夹添加").Clicked() {
						rep, err := a.backend.AddLibraryRoot(a.AddLibraryRootInput)
						if err != nil {
							a.BannerError = err.Error()
						} else {
							a.AddLibraryRootInput = ""
							a.RefreshData()
							a.BannerInfo = fmt.Sprintf("接入了 %d 条，跳过 %d 条（已存在）", rep.Added, rep.Skipped)
						}
					}
				})
			})
		})

	case "general":
		ui.Column(c).FillWidth().Gap(12).ClipX().Children(func() {
			if settingChoiceRow(c, "关闭窗口时", "最小化到托盘时，缓存、定时检查和录制在后台继续，界面占用的内存会释放。", "关闭窗口动作选择", &a.DraftSettings.Window.CloseAction, []settingChoice{{"tray", "最小化到托盘（推荐）"}, {"quit", "退出"}}) {
				a.DraftSettings.CloseAction = a.DraftSettings.Window.CloseAction
				a.saveSettings()
			}
		})

	case "logs":
		a.refreshRuntimeLogs(c.Now())
		c.After(a.runtimeLogsNextRead.Sub(c.Now()))
		diag := a.runtimeDiagnostics
		logLines := a.runtimeLogLines
		joinedLogs := strings.Join(logLines, "\n")

		ui.Column(c).FillWidth().Gap(14).ClipX().Children(func() {

			// Top: Background Status Card (索引重建、检查更新、自动缓存、录制当前情况 - R5)
			ui.Column(c).FillWidth().Padding(14, 0).BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).Gap(8).ClipX().Children(func() {
				ui.Text(c, "后台状态").FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()

				subStatus := firstNonEmptyStrUI(diag.SubscriptionCheckStatus, "空闲")
				if a.pending["check-updates"] {
					subStatus = fmt.Sprintf("正在检查更新 %d / %d", a.CheckDone, a.CheckTotal)
				}
				statusRows := []struct {
					Title string
					Value string
				}{
					{"索引重建", firstNonEmptyStrUI(diag.IndexStatus, "正常")},
					{"检查更新", subStatus},
					{"自动缓存", firstNonEmptyStrUI(diag.AutoCacheStatus, "空闲")},
					{"直播录制", firstNonEmptyStrUI(diag.LiveRecordStatus, "空闲")},
				}
				for _, sr := range statusRows {
					row := sr
					ui.Row(c).FillWidth().Padding(6, 0).AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
						ui.Text(c, row.Title).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
						ui.Text(c, row.Value).FontSize(13).TextColor(ColorTextMuted).SingleLine()
					})
				}
			})

			// Bottom: Recent Runtime Logs (up to 500 sanitized lines, monospace font, open folder & copy buttons - R5)
			ui.Column(c).FillWidth().Padding(14, 0).BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).Gap(10).ClipX().Children(func() {
				ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Wrap().Children(func() {
					ui.Text(c, fmt.Sprintf("最近运行日志（已脱敏 · 共 %d 行）", len(logLines))).FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						if OutlinePillButton(c, "打开日志文件夹", "日志页打开日志文件夹").Clicked() {
							_ = a.backend.OpenDataFolder("logs")
						}
						if OutlinePillButton(c, "复制", "复制运行日志").Clicked() {
							c.WriteClipboard(joinedLogs)
							c.Toast("已复制运行日志")
							a.BannerInfo = "已复制运行日志"
						}
					})
				})

				if a.runtimeLogError != "" {
					ui.Text(c, a.runtimeLogError).Label("运行日志读取错误").FontSize(13).TextColor(ColorError).MaxLines(3)
				}
				ui.Scroll(c).FillWidth().Height(260).Padding(10, 12).Radius(10).Background(ColorBgSecondary).Border(1, ColorBorder).ClipX().Children(func() {
					ui.Column(c).FillWidth().Gap(4).ClipX().Children(func() {
						if len(logLines) == 0 {
							if a.runtimeLogError == "" {
								ui.Text(c, "暂无运行日志记录").FontSize(12).Font("monospace").TextColor(ColorTextMuted).SingleLine()
							}
						} else {
							for _, ln := range logLines {
								lineText := ln
								ui.Text(c, lineText).FontSize(12).Font("monospace").TextColor(ColorTextMain).MaxLines(2)
							}
						}
					})
				})
			})
		})

	case "about":
		a.renderAboutSettings(c)
	}
}

// ============================================================================
// Route: /login (Centered Social Login Card: QR Code + Masked Cookie Input cleared immediately on submit, C04, C05, C07, docs/ui.md §3.8)
// ============================================================================

func (a *App) renderLoginRoute(c *ui.Context) {
	ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
		ui.Column(c).FillWidth().Padding(20, 18).AlignItems(ui.Center).Gap(14).ClipX().Children(func() {
			if a.SavedRoute != "" {
				ui.Row(c).FillWidth().Padding(10, 14).Radius(12).Background(ColorInfoBlueBg).AlignItems(ui.Center).Gap(8).ClipX().Children(func() {
					ui.Text(c, "需要重新登录，登录完成后将回到之前的页面并恢复已加载内容").
						FontSize(13).FontWeight(600).TextColor(ColorInfoBlue).MaxLines(2)
				})
			}
			if a.LoginError != "" {
				ui.Row(c).FillWidth().Padding(10, 14).Radius(12).Background(ColorErrorBg).Border(1, ui.RGBA(180, 35, 24, 0.25)).AlignItems(ui.Center).Gap(8).ClipX().Children(func() {
					DrawIcon(c, "alert-circle", 16, ColorError, 1.8)
					ui.Text(c, a.LoginError).FontSize(13).FontWeight(600).TextColor(ColorError).MaxLines(2).Grow(1)
				})
			}

			// Centered Main Login Card (docs/ui.md §3.8)
			ui.Column(c).
				FillWidth().
				Padding(20).
				Radius(16).
				Background(ColorBgSecondary).
				Border(1, ColorBorder).
				Gap(16).
				ClipX().
				Children(func() {
					ui.Column(c).Gap(4).Children(func() {
						ui.Text(c, "登录哔哩哔哩").FontSize(20).FontWeight(600).TextColor(ColorTextMain).SingleLine()
						ui.Text(c, "TRACE 需要登录后才能解析视频、查看 UP 主动态和缓存最高画质媒体。").
							FontSize(13).TextColor(ColorTextMuted).MaxLines(2)
					})

					UnderlineTabs(c, a.LoginTab, []TabOption{
						{Key: "qr", Text: "扫码登录（推荐）", AccessLabel: "登录标签：扫码"},
						{Key: "cookie", Text: "粘贴凭证", AccessLabel: "登录标签：粘贴凭证"},
					}, func(key string) {
						if key == "cookie" && a.LoginTab != key {
							a.cancelQRLogin()
						}
						a.LoginTab = key
					})

					// 1. QR Code Login Section (C04)
					if a.LoginTab == "qr" {
						ui.Column(c).
							FillWidth().
							Padding(16).
							Radius(12).
							Background(ColorBgMain).
							Border(1, ColorBorder).
							Gap(10).
							ClipX().
							Children(func() {
								ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
									ui.Text(c, "方式一：哔哩哔哩手机客户端扫码登录").FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
									ui.Box(c).Padding(2, 8).Radius(999).Background(ColorAccentBg).Children(func() {
										ui.Text(c, "支持自动续期").FontSize(11).FontWeight(600).TextColor(ColorAccent).SingleLine()
									})
								})

								if a.QRBitmap != nil {
									ui.Row(c).FillWidth().Justify(ui.Center).Padding(6, 0).Children(func() {
										ui.Box(c).Padding(8).Radius(12).Background(ColorWhite).Border(1, ColorBorder).Children(func() {
											ui.Image(c, a.QRBitmap).Size(168, 168).Fit(ui.Contain)
										})
									})
								}
								if a.QRStatusText != "" {
									ui.Row(c).FillWidth().Justify(ui.Center).Children(func() {
										ui.Text(c, "扫码状态："+a.QRStatusText).FontSize(13).FontWeight(600).TextColor(ColorInfoBlue).SingleLine()
									})
								}

								ui.Row(c).FillWidth().Justify(ui.Center).Gap(8).Wrap().Children(func() {
									if AccentPillButton(c, "获取登录二维码", "获取登录二维码").Clicked() {
										a.startQRLogin()
									}
									if a.QRSession.FlowID != "" {
										if OutlinePillButton(c, "检查扫码状态", "检查扫码状态").Clicked() {
											a.pollQRLogin()
										}
										if OutlinePillButton(c, "取消扫码", "取消扫码").Clicked() {
											a.cancelQRLogin()
											a.QRStatusText = "已取消"
										}
									}
								})
							})
					}

					// 2. Manual Cookie Paste Section (Masked input, cleared immediately on submit - C05, docs/ui.md §3.8)
					if a.LoginTab == "cookie" {
						ui.Column(c).
							FillWidth().
							Padding(16).
							Radius(12).
							Background(ColorBgMain).
							Border(1, ColorBorder).
							Gap(10).
							ClipX().
							Children(func() {
								ui.Text(c, "方式二：粘贴凭证（浏览器 Cookie）").FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
								ui.Text(c, "在浏览器打开 bilibili.com 并登录 → 按 F12 打开开发者工具 → 网络 (Network) → 复制请求头中的 Cookie（至少包含 SESSDATA）。").
									FontSize(12).TextColor(ColorTextMuted).MaxLines(2)

								ui.TextInput(c, &a.ManualCookieInput).
									Password().
									Placeholder("粘贴 Cookie（至少包含 SESSDATA）").
									Label("手动Cookie输入框").
									FillWidth()

								ui.TextInput(c, &a.ManualRefreshInput).
									Password().
									Placeholder("可选：ac_time_value / refresh_token").
									Label("手动RefreshToken输入框").
									FillWidth()

								ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
									ui.Text(c, "凭证只保存在这台电脑上，并按当前 Windows 用户加密").
										FontSize(12).TextColor(ColorTextMuted).SingleLine()
									label := "验证并登录"
									if a.pending["manual-login"] {
										label = "正在验证…"
									}
									if AccentPillButton(c, label, "验证并保存凭证").Disabled(a.pending["manual-login"]).Clicked() {
										a.importManualCookie()
									}
								})
							})
					}
				})
		})
	})
}

// finishTaskAction shows a failed task action instead of silently ignoring it.
func (a *App) finishTaskAction(err error) {
	a.TaskActionError = ""
	if err != nil {
		a.TaskActionError = "操作没有完成：" + err.Error()
	}
	a.RefreshData()
}

// renderOpening shows the page of a link being opened with what the clicked
// card knew, then the failure and a retry if OpenLink fails (F-057).
func (a *App) renderOpening(c *ui.Context) {
	p := a.Opening
	ui.Row(c).Label("正在打开").FillWidth().AlignItems(ui.Center).Gap(10).ClipX().Children(func() {
		if p.Mid > 0 || p.AuthorName != "" {
			a.renderAvatar(c, firstNonEmptyStrUI(p.AuthorName, "UP"), p.Mid, 42, false, "", nil)
		}
		ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
			if p.AuthorName != "" {
				ui.Text(c, p.AuthorName).FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
			}
			if p.Err == "" {
				ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
					ui.Spinner(c).Size(14, 14)
					ui.Text(c, "正在打开…").FontSize(12).TextColor(ColorTextMuted).SingleLine()
				})
			}
		})
	})
	if p.Title != "" {
		ui.Text(c, p.Title).FontSize(18).FontWeight(600).TextColor(ColorTextMain).MaxLines(2)
	}
	if p.Kind == "video" {
		a.renderMediaThumb(c, "", "", p.Title, p.Duration, "", false, 0, 300, p.CoverURL)
	}
	if p.Err != "" {
		ui.Row(c).FillWidth().Padding(10, 12).Radius(10).Background(ColorErrorBg).AlignItems(ui.Center).Gap(8).ClipX().Children(func() {
			DrawIcon(c, "alert-circle", 16, ColorError, 1.8)
			ui.Text(c, p.Err).FontSize(13).TextColor(ColorError).MaxLines(3).Grow(1)
			if OutlinePillButton(c, "重试", "重试打开").Clicked() {
				a.RetryOpening()
			}
		})
	}
}
