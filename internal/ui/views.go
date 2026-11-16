package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

type navItem struct {
	Route string
	Icon  string
	Label string
}

// Per docs/ui.md §2 & DEC-014, main navigation has 6 items (我的 since DEC-047); Settings is pinned in the bottom-left row (DEC-019).
var mainNavItems = []navItem{
	{Route: RouteHome, Icon: "home", Label: "主页"},
	{Route: RouteSubscriptions, Icon: "subscriptions", Label: "订阅"},
	{Route: RouteMine, Icon: "mine", Label: "我的"},
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
	return n
}

func (a *App) anyLiveRoomActive() bool {
	for _, r := range a.LiveRooms {
		if r.LiveStatus == 1 {
			return true
		}
	}
	for _, acct := range a.LiveAccounts {
		if acct.LiveStatus == 1 {
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
	// Under the hidden title bar the top bar moves the window and keeps its
	// controls clear of the window buttons (DEC-046 ④).
	rightPad := float32(16)
	if a.titleBar.Right > 0 {
		rightPad = max(rightPad, a.titleBar.Right+8-a.pageGapRight)
	}
	ui.Column(c).
		Label("页面顶部栏").
		FillWidth().
		Padding(10, rightPad, 10, 16).
		Gap(8).
		BorderWidth(0, 0, 1, 0).
		BorderColor(ColorBorder).
		Background(ColorBgMain).
		Shrink(0).
		ClipX().
		DragWindow().
		Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(10).Grow(1).ClipX().Children(func() {
					if a.CurrentRoute == RouteVideo || a.CurrentRoute == RouteUser ||
						a.CurrentRoute == RouteDouyinWork || a.CurrentRoute == RouteDouyinUser ||
						a.CurrentRoute == RouteLibraryItem ||
						(a.CurrentRoute == RouteSubscriptions && a.SubDetailOpen) ||
						(a.CurrentRoute == RouteHome && a.HomeActorFilter != nil) ||
						(a.CurrentRoute == RouteMine && !a.panelShown && a.mineShowsList()) ||
						(a.CurrentRoute == RouteLogin && a.backend.HasAnyLogin()) {
						backLabel := "返回"
						if a.CurrentRoute == RouteSubscriptions && a.SubDetailOpen {
							backLabel = "返回订阅列表"
						} else if a.CurrentRoute == RouteHome && a.HomeActorFilter != nil {
							backLabel = "返回主页时间线"
						} else if a.CurrentRoute == RouteMine {
							backLabel = "返回我的功能列表"
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
							} else if a.CurrentRoute == RouteMine {
								a.Mine.Picker = true
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
						a.renderAccountAvatar(c, actor.Site, actor.Name, actor.UID, actor.Mid, 28, a.isActorLive(actor), "", nil, actor.Face)
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

			// Never show unrelated background loading state on RouteParse.
			if len(a.pending) > 0 && a.CurrentRoute != RouteParse {
				ui.Text(c, "正在加载…").Label("请求进行中").FontSize(12).TextColor(ColorInfoBlue)
			}

			if a.BannerError != "" && !(a.CurrentRoute == RouteParse && a.BannerError == a.LastParseError) {
				ui.Row(c).FillWidth().Padding(8, 12).Radius(10).Background(ColorErrorBg).Border(1, ui.RGBA(180, 35, 24, 0.2)).AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
					DrawIcon(c, "alert-circle", 16, ColorError, 1.8)
					ui.Text(c, a.BannerError).FontSize(13).TextColor(ColorError).MaxLines(2).Grow(1)
					if a.BannerLoginSite != "" && a.CurrentRoute != RouteLogin {
						targetSite := a.BannerLoginSite
						if AccentPillButton(c, "去登录", "提示去登录").Clicked() {
							a.BannerError = ""
							a.BannerLoginSite = ""
							a.OpenLoginForSite(targetSite)
						}
					}
					if OutlinePillButton(c, "关闭", "关闭错误提示").Clicked() {
						a.BannerError = ""
						a.BannerLoginSite = ""
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

	// Without the context panel (a narrow window), the site filter and people
	// with updates appear above the timeline.
	if a.CurrentRoute == RouteHome && !a.panelShown && a.backend.HasAnyLogin() {
		if a.HomeActorFilter == nil {
			ui.Row(c).
				FillWidth().
				Padding(8, 16).
				BorderWidth(0, 0, 1, 0).
				BorderColor(ColorBorder).
				Background(ColorBgMain).
				Shrink(0).
				Children(func() {
					renderThreeSegmentSiteFilter(c, "主页站点筛选：", a.FeedSiteFilter, a.setFeedSiteFilter)
				})
		}
		a.renderHomeUpdatedAvatarsRow(c)
	}

	// 2. Center Body (Virtualized list on /home, or scrollable container on other routes)
	switch a.CurrentRoute {
	case RouteHome:
		a.renderHomeRoute(c)
	case RouteSubscriptions:
		a.renderSubscriptionsRoute(c)
	case RouteMine:
		a.renderMineRoute(c, width)
	case RouteParse:
		a.renderParseRoute(c, width)
	case RouteVideo:
		a.renderVideoRoute(c)
	case RouteUser:
		a.renderUserRoute(c)
	case RouteDouyinWork:
		_, winH := c.Size()
		a.renderDouyinWorkRoute(c, winH)
	case RouteDouyinUser:
		a.renderDouyinUserRoute(c, width)
	case RouteLibrary:
		a.renderLibraryRoute(c)
	case RouteLibraryItem:
		_, winH := c.Size()
		a.renderLibraryItemRoute(c, winH)
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
	case RouteMine:
		return a.mineTitle()
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
	case RouteDouyinWork:
		if a.CurrentLibraryDetail != nil && a.CurrentLibraryDetail.Kind == "live" {
			return "直播录像"
		}
		if (a.Opening != nil && (a.Opening.Kind == "note" || a.Opening.Kind == "dynamic")) ||
			(a.CurrentDouyinWork != nil && a.CurrentDouyinWork.Kind == "note") ||
			(a.CurrentLibraryDetail != nil && a.CurrentLibraryDetail.Kind == "dynamic") {
			return "图文"
		}
		return "视频"
	case RouteDouyinUser:
		if a.Opening != nil {
			return firstNonEmptyStrUI(a.Opening.AuthorName, "抖音作者")
		}
		if a.CurrentDouyinUser != nil && a.CurrentDouyinUser.Nickname != "" {
			return a.CurrentDouyinUser.Nickname
		}
		return "抖音作者"
	case RouteLibrary:
		return "媒体库"
	case RouteLibraryItem:
		if a.CurrentLibraryDetail != nil {
			switch a.CurrentLibraryDetail.Kind {
			case "live":
				return "直播录像"
			case "note":
				return "图文"
			case "dynamic":
				if normalizeSiteIDUI(a.CurrentLibraryDetail.Site) == "douyin" || len(a.CurrentLibraryDetail.NotePages) > 0 {
					return "图文"
				}
				return "动态"
			}
		}
		return "视频"
	case RouteTasks:
		return "任务"
	case RouteSettings:
		return settingsCategoryLabel(a.SettingsCategory)
	case RouteLogin:
		if normalizeSiteIDUI(a.LoginSite) == "douyin" {
			return "登录抖音"
		}
		return "登录哔哩哔哩"
	default:
		return "TRACE"
	}
}

func (a *App) renderRouteHeaderActions(c *ui.Context) {
	switch a.CurrentRoute {
	case RouteMine:
		a.renderMineHeaderActions(c)
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
				if m.Item("从 B 站关注列表导入").Chosen() {
					a.openImportFollowingsModal()
				}
			})
			a.SubMoreMenuOpen = open
		})
	case RouteTasks:
		if a.effectiveTasksSection() != "downloads" {
			return
		}
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
		Background(ColorBgMain).
		Border(1, ColorBorder).
		Gap(8).
		AlignItems(ui.Center).
		ClipX().
		Children(func() {
			DrawIcon(c, "link", 16, ColorTextMuted, 1.6)
			inp := ui.TextInputBase(c, &a.QuickParseInput).
				Placeholder("粘贴链接、分享文本或 UID 解析").
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
// Route: /home (Virtualized Feed List + Updated Avatars Row, docs/ui.md §3.1)
// ============================================================================

// homeStripStride is the width of one entry of the narrow window's avatar
// strip on Home.
const homeStripStride = 76

// renderHomeUpdatedAvatarsRow is the narrow window's form of the Home
// panel's subscriptions (DEC-053 ②): a strip of round avatars with the
// name below, "全部动态" first, live rings and unread dots as in the panel.
// Only the entries scrolled into view are built.
func (a *App) renderHomeUpdatedAvatarsRow(c *ui.Context) {
	rows := a.homeSubscriptionRows()
	if len(rows) == 0 {
		return
	}
	total := len(rows) + 1
	lo := max(0, int(a.homeStripScroll.X/homeStripStride)-2)
	hi := min(total, lo+40)
	ui.Column(c).
		Label("主页订阅头像条").
		FillWidth().
		Padding(10, 8).
		BorderWidth(0, 0, 1, 0).
		BorderColor(ColorBorder).
		Background(ColorBgMain).
		Shrink(0).
		ClipX().
		Children(func() {
			ui.ScrollHorizontal(c).TrackScroll(&a.homeStripScroll).FillWidth().Height(84).Shrink(0).Children(func() {
				ui.Row(c).AlignItems(ui.Start).Children(func() {
					if lo > 0 {
						ui.Box(c).Width(float32(lo * homeStripStride)).Height(1).Shrink(0)
					}
					for i := lo; i < hi; i++ {
						if i == 0 {
							a.renderHomeStripAll(c)
							continue
						}
						actor := rows[i-1]
						a.renderHomeStripEntry(c, actor.Name, a.HomeActorFilter != nil && a.HomeActorFilter.Key == actor.Key, func() {
							a.renderHomeSubAvatar(c, actor, 52, ColorBgMain, func() { a.openHomeActorFeed(actor) })
						}, func() { a.openHomeActorFeed(actor) })
					}
					if hi < total {
						ui.Box(c).Width(float32((total - hi) * homeStripStride)).Height(1).Shrink(0)
					}
				})
			})
		})
}

func (a *App) renderHomeStripAll(c *ui.Context) {
	active := a.HomeActorFilter == nil
	a.renderHomeStripEntry(c, "全部动态", active, func() {
		btn := ui.ButtonBase(c).Label("主页订阅头像：全部动态").Size(52, 52).Radius(26).Shrink(0).Justify(ui.Center).AlignItems(ui.Center)
		if active {
			btn.Background(ColorAccentBg)
		} else {
			btn.Background(ColorBgSecondary)
		}
		btn.Children(func() {
			col := ColorTextMuted
			if active {
				col = ColorAccent
			}
			DrawIcon(c, "home", 22, col, 1.6)
		})
		if btn.Clicked() {
			a.closeHomeActorFeed()
		}
	}, a.closeHomeActorFeed)
}

func (a *App) renderHomeStripEntry(c *ui.Context, name string, active bool, avatar func(), open func()) {
	ui.Column(c).Width(homeStripStride).Shrink(0).AlignItems(ui.Center).Gap(4).ClipX().Children(func() {
		ui.Box(c).Height(58).Width(58).Justify(ui.Center).AlignItems(ui.Center).Shrink(0).Children(avatar)
		nameBtn := ui.ButtonBase(c).Width(homeStripStride - 8).Padding(0).Justify(ui.Center).AlignItems(ui.Center).ClipX()
		nameBtn.Children(func() {
			t := ui.Text(c, name).FontSize(12).SingleLine()
			if active {
				t.TextColor(ColorAccent).FontWeight(600)
			} else {
				t.TextColor(ColorTextMain)
			}
		})
		if nameBtn.Clicked() {
			open()
		}
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
	restoring := a.HomeActorFilter == nil && a.stepHomeRestore(c, first, last)
	if !restoring {
		a.markHomeSeen(first, last)
	}
	// Posts cached while the timeline is open, or newer than the position
	// it opened at, are fetched when it reaches the top, the rows in view
	// staying where they are; nothing is counted or announced as unread
	// (DEC-040, DEC-053).
	if !restoring && a.HomeActorFilter == nil && first >= 0 && first <= 2 && a.feedMoreBefore && a.FeedError == "" {
		a.loadFeedEdge(true)
	}
	if !restoring && last >= len(a.FeedItems)-3 && a.feedMoreAfter && a.FeedError == "" {
		a.loadFeedEdge(false)
	}
	// Where the main timeline is read: the top row's place against the
	// list's, both as the last frame laid them out. The rows are built as
	// the list lays out, so the top row notes it.
	topKey := ""
	if !restoring && a.HomeActorFilter == nil && first >= 0 && first < len(a.FeedItems) && last >= first {
		topKey = a.FeedItems[first].FeedKey
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
			ui.Text(c, "到「解析」里打开一位 UP 主或抖音作者，在动态或作品上点「缓存」").
				FontSize(14).TextColor(ColorTextMuted).MaxLines(2)
			if AccentPillButton(c, "去解析", "去解析链接").Clicked() {
				a.Navigate(RouteParse)
			}
		})
		return
	}

	list := ui.List(c, &a.FeedListState, len(a.FeedItems), func(i int) {
		if i < 0 || i >= len(a.FeedItems) {
			return
		}
		entry := a.FeedItems[i]
		post := a.feedEntryToPostCard(entry)
		row := ui.Column(c).Label("信息流行 " + entry.FeedKey).FillWidth().Children(func() { a.renderPostCard(c, post) })
		if topKey != "" && entry.FeedKey == topKey {
			rb, lb := row.Bounds(), a.homeListBounds
			a.noteHomePosition(c, i, rb.Y, lb.Y, rb.H > 0 && lb.H > 0)
		}
		if i == len(a.FeedItems)-1 && !a.feedMoreAfter {
			ui.Row(c).FillWidth().Padding(18, 16).Justify(ui.Center).Children(func() {
				ui.Text(c, "已经到最早的缓存了").FontSize(13).TextColor(ColorTextMuted).SingleLine()
			})
		}
	})
	list.Label("主页信息流列表").TrackScroll(&a.FeedScroll).Grow(1).FillWidth()
	// As the last frame laid the list out; the rows, built as this frame
	// lays out, read it.
	a.homeListBounds = list.Bounds()
}

// ============================================================================
// Route: /parse (Link & UID Parser + Sanitized History, docs/ui.md §3.2)
// ============================================================================

func (a *App) renderParseRoute(c *ui.Context, width float32) {
	acct := a.backend.StatusOf("bilibili")
	acctSite := "bilibili"
	if !acct.LoggedIn {
		if dy := a.backend.StatusOf("douyin"); dy.LoggedIn {
			acct = dy
			acctSite = "douyin"
		}
	}
	acctName := acct.Uname
	if acctName == "" {
		acctName = "TRACE"
	}

	barW := min(float32(640), max(float32(220), width-32))
	ui.Column(c).
		Grow(1).
		FillWidth().
		Padding(0, 16).
		AlignItems(ui.Center).
		ClipX().
		Children(func() {
			// Place the search-style input slightly above the vertical center (DEC-049 ②).
			// A narrow window has no context panel, so the recent parses fall back under
			// the input (docs/ui.md §1) instead of becoming unreachable.
			narrow := !a.panelShown
			if narrow {
				ui.Box(c).Height(24).Shrink(0)
			} else {
				ui.Box(c).Grow(2)
			}
			ui.Column(c).
				Width(barW).
				Gap(10).
				ClipX().
				Children(func() {
					ui.Row(c).
						Label("解析大输入框容器").
						FillWidth().
						Height(52).
						Padding(6, 6, 6, 10).
						Radius(999).
						Background(ColorBgMain).
						Border(1, ColorBorderStrong).
						Gap(10).
						AlignItems(ui.Center).
						ClipX().
						Children(func() {
							a.renderAccountAvatar(c, acctSite, acctName, acct.UID, acct.Mid, 34, false, "", nil, acct.Face)
							inp := ui.TextInputBase(c, &a.ParsePageInput).
								Placeholder("粘贴哔哩哔哩或抖音链接、分享文本、主页或 UID").
								Label("链接解析输入框").
								FontSize(14).
								Padding(6, 4).
								Grow(1)
							wasComposing := a.ParseInputComposing
							a.ParseInputComposing = inp.Composing()
							if inp.Submitted() && !inp.Composing() && !wasComposing && !a.parseBusy() {
								a.SubmitParse(a.ParsePageInput)
							}
							btnLabel := "立即解析"
							if a.parseBusy() {
								btnLabel = "解析中…"
							}
							if AccentPillButton(c, btnLabel, "立即解析").Disabled(a.parseBusy()).Clicked() {
								a.SubmitParse(a.ParsePageInput)
							}
						})

					if a.LastParseError != "" {
						ui.Row(c).
							FillWidth().
							Padding(4, 12).
							AlignItems(ui.Center).
							Justify(ui.SpaceBetween).
							Gap(8).
							ClipX().
							Children(func() {
								ui.Row(c).AlignItems(ui.Center).Gap(6).Grow(1).ClipX().Children(func() {
									DrawIcon(c, "alert-circle", 15, ColorError, 1.8)
									ui.Text(c, a.LastParseError).FontSize(13).TextColor(ColorError).MaxLines(2).Grow(1)
								})
								if a.BannerLoginSite != "" {
									targetSite := a.BannerLoginSite
									if AccentPillButton(c, "去登录", "解析提示去登录").Clicked() {
										a.LastParseError = ""
										a.BannerError = ""
										a.BannerLoginSite = ""
										a.OpenLoginForSite(targetSite)
									}
								}
							})
					}
				})
			if narrow {
				ui.Column(c).
					Label("解析页最近解析").
					Width(barW).
					Grow(1).
					Margin(12, 0, 12, 0).
					Radius(12).
					Border(1, ColorBorder).
					Clip().
					Children(func() {
						a.renderHistoryPanel(c)
					})
			} else {
				ui.Box(c).Grow(3)
			}
		})
}

// ============================================================================
// Route: /video (Video & Dynamic Detail, Multi-Part Selection, Upgrade, docs/ui.md §3.4)
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

				offline := a.CurrentRoute == RouteLibraryItem
				// 1. Creator Row + Cache Button + Status Dot (docs/ui.md §3.4)
				ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(10).ClipX().Children(func() {
					ui.Row(c).AlignItems(ui.Center).Gap(10).Grow(1).ClipX().Children(func() {
						var openAuthor func()
						if !offline {
							openAuthor = func() {
								a.OpenUser(vd.Author.Mid, vd.Author.Name)
							}
						}
						a.renderAvatar(c, vd.Author.Name, vd.Author.Mid, 42, false, "", openAuthor)
						authorBody := func() {
							ui.Column(c).Gap(2).Grow(1).ClipX().Children(func() {
								ui.Text(c, vd.Author.Name).FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
								ui.Row(c).AlignItems(ui.Center).Gap(4).ClipX().Children(func() {
									renderSiteSmallBadge(c, "bilibili", "视频详情站点标记")
									ui.Text(c, fmt.Sprintf("· UID %d", vd.Author.Mid)).FontSize(12).TextColor(ColorTextMuted).SingleLine()
								})
							})
						}
						if !offline {
							author := ui.ButtonBase(c).Label("进入空间").Grow(1).Padding(0).ClipX()
							author.Children(authorBody)
							if author.Clicked() {
								a.OpenUser(vd.Author.Mid, vd.Author.Name)
							}
						} else {
							authorBody()
						}
					})

					ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
						if !offline {
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
						}
						if dotStatus != "" && dotStatus != "none" {
							DrawStatusDot(c, dotStatus, dotLabel)
						}
						if offline && cachedItem != nil {
							a.renderLibraryItemMoreMenu(c, *cachedItem, 32)
						}
					})
				})

				// 2. Quality Upgrade Banner (docs/ui.md §3.4)
				if !offline && (a.UpgradeAvailable || (cachedItem != nil && cachedItem.UpgradeAvailable)) {
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
				coverRemote := vd.Cover
				if offline {
					coverRemote = ""
				}
				coverBtn := ui.ButtonBase(c).
					Label("视频大封面").
					FillWidth().
					Height(236).
					Radius(12).
					Clip()
				coverBtn.Children(func() {
					// The status dot is beside the cache button above, not on the cover.
					a.renderMediaThumb(c, itemKey, relCover, vd.Title, durationText, "", cachedItem != nil, 0, 236, coverRemote)
				})
				if coverBtn.Clicked() {
					if cachedItem != nil {
						a.OpenPlayerOverlay(*cachedItem)
					} else if !offline && vd.Cover != "" {
						a.OpenImageViewer([]string{vd.Cover}, nil, 0)
					}
				}

				// 5. Expandable Description
				if vd.Desc != "" {
					a.renderExpandableText(c, "video-desc:"+vd.BVID, vd.Desc)
				}

				// 6. Publish Time · Partition · Actions (including cache maintenance: "补存弹幕和字幕")
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
					if !offline {
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
					}
				})

				// 7. Interaction Statistics Bar with Linear Icons and 万/亿 Formatting (docs/ui.md §3.4, §7)
				// Counts change all the time and are not kept, so a cached
				// video shows none (DEC-040).
				if a.CurrentLibraryDetail == nil && !offline {
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

				// 8. Multi-part (分 P) List with Cached Stream Specs (docs/ui.md §3.4)
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
					var partFile string
					if cachedItem != nil {
						for _, rec := range cachedItem.Parts {
							if rec.Page == p.Page {
								partFile = rec.File
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
						Background(ColorBgMain).
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
								if offline {
									if cachedItem != nil && partFile != "" {
										if OutlinePillButton(c, "播放", fmt.Sprintf("播放分P %d", p.Page)).Clicked() {
											a.openPlayerFile(*cachedItem, partFile)
										}
									}
								} else {
									chk := selected
									if ui.Switch(c, &chk).Label(fmt.Sprintf("选择分P %d", p.Page)).Changed() {
										a.SelectedPartPages[p.Page] = chk
									}
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
// Route: /user (Creator Profile, Subscription Badge, Live Banner, Dynamics & Videos Tabs, docs/ui.md §3.4)
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
	// posts and a short window still shows them. 未缓存 filters the loaded
	// pages only (DEC-046 ⑧); with nothing left on them, the footer row
	// offers the next page instead of loading it by itself.
	filtered := a.UserListFilter == userFilterUncached
	if a.UserTab == "videos" {
		all := a.UserVideos
		idx := make([]int, len(all))
		for i := range idx {
			idx[i] = i
		}
		if filtered {
			keys := make([]string, len(all))
			for i, v := range all {
				keys[i] = library.CanonicalItemKey("bilibili", "video", v.BVID)
			}
			idx = a.uncachedIndices(fmt.Sprintf("videos:%d", u.Mid), keys)
		}
		_, last := a.UserVidListState.Visible()
		if len(idx) > 0 && last-1 >= len(idx)-2 && a.UserVideosHasMore && a.UserVideosError == "" && !a.pending[fmt.Sprintf("user-videos-%d", u.Mid)] {
			a.LoadMoreUserVideos()
		}
		rows := len(idx)
		if rows == 0 && filtered && len(all) > 0 {
			rows = 1 // footer only
		}
		a.UserVidListState.Key = func(i int) any {
			if i == 0 || i > len(idx) {
				return "header"
			}
			return all[idx[i-1]].BVID
		}
		ui.List(c, &a.UserVidListState, rows+1, func(i int) {
			if i == 0 {
				a.renderUserHeader(c, u)
				return
			}
			if len(idx) == 0 {
				a.renderUserFilteredEmpty(c)
				a.renderUserListEnd(c, a.UserVideosHasMore, a.UserVideosError, "加载下一页投稿", "已加载全部投稿", a.LoadMoreUserVideos)
				return
			}
			if i > len(idx) {
				return
			}
			a.renderUserVideoRow(c, u, all[idx[i-1]])
			if i == len(idx) {
				a.renderUserListEnd(c, a.UserVideosHasMore, a.UserVideosError, "加载下一页投稿", "已加载全部投稿", a.LoadMoreUserVideos)
			}
		}).Label("UP主投稿虚拟列表").Grow(1).FillWidth()
		return
	}

	all := a.UserDynamics
	idx := make([]int, len(all))
	for i := range idx {
		idx[i] = i
	}
	if filtered {
		keys := make([]string, len(all))
		for i, d := range all {
			keys[i] = library.CanonicalItemKey("bilibili", "dynamic", d.ID)
		}
		idx = a.uncachedIndices(fmt.Sprintf("dynamics:%d", u.Mid), keys)
	}
	_, last := a.UserDynListState.Visible()
	if len(idx) > 0 && last-1 >= len(idx)-2 && a.UserDynamicsHasMore && a.UserDynamicsError == "" && !a.pending[fmt.Sprintf("user-dynamics-%d", u.Mid)] {
		a.LoadMoreUserDynamics()
	}
	rows := len(idx)
	if rows == 0 && filtered && len(all) > 0 {
		rows = 1 // footer only
	}
	a.UserDynListState.Key = func(i int) any {
		if i == 0 || i > len(idx) {
			return "header"
		}
		return all[idx[i-1]].ID
	}
	ui.List(c, &a.UserDynListState, rows+1, func(i int) {
		if i == 0 {
			a.renderUserHeader(c, u)
			return
		}
		if len(idx) == 0 {
			a.renderUserFilteredEmpty(c)
			a.renderUserListEnd(c, a.UserDynamicsHasMore, a.UserDynamicsError, "加载下一页动态", "已加载全部动态", a.LoadMoreUserDynamics)
			return
		}
		if i > len(idx) {
			return
		}
		d := all[idx[i-1]]
		if d.Author.Name == "" {
			d.Author.Name = u.Name
			d.Author.Mid = u.Mid
		}
		a.renderPostCard(c, a.dynamicItemToPostCard(d, "打开动态："+d.ID, "缓存动态："+d.ID))
		if i == len(idx) {
			a.renderUserListEnd(c, a.UserDynamicsHasMore, a.UserDynamicsError, "加载下一页动态", "已加载全部动态", a.LoadMoreUserDynamics)
		}
	}).Label("UP主动态虚拟列表").Grow(1).FillWidth()
}

func (a *App) renderUserFilteredEmpty(c *ui.Context) {
	ui.Row(c).FillWidth().Padding(18, 16, 0, 16).Justify(ui.Center).Children(func() {
		ui.Text(c, "已加载的内容都缓存了").Label("未缓存筛选为空").FontSize(13).TextColor(ColorTextMuted).SingleLine()
	})
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
// errors and tabs: one main button (subscribe), the rest in "⋯".
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
				ui.Row(c).AlignItems(ui.Center).Gap(10).Grow(1).ClipX().Children(func() {
					renderSiteSmallBadge(c, "bilibili", "用户页站点标记")
					ui.Text(c, "·").FontSize(12).TextColor(ColorTextMuted).SingleLine()
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
						a.OpenSubscribePicker(subscription.AccountRef{Site: "bilibili", UID: strconv.FormatInt(u.Mid, 10), Name: u.Name, Face: u.Face})
						a.RefreshData()
					}
					targetURL := u.CanonicalURL
					if targetURL == "" {
						targetURL = fmt.Sprintf("https://space.bilibili.com/%d", u.Mid)
					}
					MoreMenuButton(c, "空间更多", 32, nil, func(m *ui.Menu) {
						a.cacheOlderMenu(m, "bilibili")
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

			// Live banner (docs/ui.md §3.4): the record toggle is its one button.
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

			// Partial-failure error with Retry while keeping page 1.
			if a.UserTab != "videos" && a.UserDynamicsError != "" {
				a.renderUserListError(c, a.UserDynamicsError, "重试加载动态", a.LoadMoreUserDynamics)
			}
			if a.UserTab == "videos" && a.UserVideosError != "" {
				a.renderUserListError(c, a.UserVideosError, "重试加载投稿", a.LoadMoreUserVideos)
			}
		})

		ui.Row(c).FillWidth().AlignItems(ui.End).Gap(12).ClipX().Children(func() {
			ui.Box(c).Grow(1).ClipX().Children(func() {
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
			ui.Box(c).Padding(0, 0, 6, 0).Shrink(0).Children(func() { a.renderUserListFilter(c) })
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
// the browser are in its "⋯".
func (a *App) renderUserVideoRow(c *ui.Context, u *bilibili.UserSpace, v bilibili.UserVideoItem) {
	a.renderCompactVideoRow(c, compactVideoRow{
		Label:      "打开视频：" + v.BVID,
		MenuKey:    "upload:" + v.BVID,
		MenuName:   "投稿更多：" + v.BVID,
		BVID:       v.BVID,
		ForMid:     u.Mid,
		Title:      v.Title,
		Cover:      v.Cover,
		Duration:   v.Length,
		Meta:       fmt.Sprintf("时长 %s · 播放 %s", v.Length, FormatCount(int64(v.PlayCount))),
		BrowserURL: "https://www.bilibili.com/video/" + v.BVID,
		Open:       func() { a.OpenVideo(v.BVID, v.Title, u.Name, u.Mid, v.Cover, v.Length) },
	})
}

// compactVideoRow is the data of one compact row (16:9 thumbnail, two-line
// title, one line of details), whatever list it comes from.
type compactVideoRow struct {
	Label, MenuKey, MenuName string
	BVID                     string // set for a Bilibili video: cache key, status dot, "缓存"
	ForMid                   int64  // creator page the row is on: a collaboration video is cached under their folder (DEC-056)
	Title, Cover, Duration   string
	Meta                     string
	TypeLabel                string  // 音频、番剧…: shown instead of the play mark
	Badge                    string  // top-left mark on the thumbnail (直播)
	BadgeLive                bool    // the badge uses the live colour
	Progress                 float32 // 0..1, a thin accent line under the thumbnail
	Invalid                  bool    // grey block, not clickable, no "⋯"
	Open                     func()  // nil: the body is not clickable
	BrowserURL               string  // "在浏览器中打开"; "" hides it
	CopyURL                  string  // "复制链接"; "" hides it
	Extra                    func(m *ui.Menu)
}

func (a *App) renderCompactVideoRow(c *ui.Context, r compactVideoRow) {
	itemKey, dotStatus, relCover := "", "", ""
	if r.BVID != "" && !r.Invalid {
		itemKey = library.CanonicalItemKey("bilibili", "video", r.BVID)
		if it, ok, err := a.backend.GetLibraryItem(itemKey); err == nil && ok {
			dotStatus = library.ComputeDotState(it).Dot
			for _, m := range it.Media {
				if m.Role == "cover" && relCover == "" {
					relCover = m.Path
				}
			}
		}
	}
	titleColor, metaColor := ColorTextMain, ColorTextMuted
	if r.Invalid || r.Open == nil {
		titleColor = ColorTextMuted
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
			content := func() {
				ui.Column(c).Width(144).Shrink(0).Gap(0).Children(func() {
					ui.Box(c).Size(144, 82).Shrink(0).Children(func() {
						if r.Invalid {
							ui.Box(c).Size(144, 82).Radius(12).Background(ColorBgSecondary).Border(1, ColorBorder)
						} else {
							a.renderMediaThumb(c, itemKey, relCover, r.Title, r.Duration, dotStatus, dotStatus == "green" && r.TypeLabel == "", 144, 82, r.Cover)
						}
						if r.Badge != "" {
							bg := ui.RGBA(0, 0, 0, 0.65)
							if r.BadgeLive {
								bg = ColorLive
							}
							ui.Box(c).Absolute().Top(6).Left(6).Padding(2, 6).Radius(4).Background(bg).Children(func() {
								ui.Text(c, r.Badge).FontSize(11).FontWeight(600).TextColor(ColorWhite).SingleLine()
							})
						}
					})
					if r.Progress > 0 && !r.Invalid {
						ui.Box(c).Width(144).Height(3).Radius(2).Background(ColorBorder).Children(func() {
							ui.Box(c).Width(144 * min(r.Progress, 1)).Height(3).Radius(2).Background(ColorAccent)
						})
					}
				})
				ui.Column(c).Grow(1).Gap(5).ClipX().Children(func() {
					ui.Text(c, r.Title).FontSize(15).FontWeight(600).TextColor(titleColor).MaxLines(2)
					meta := r.Meta
					if r.TypeLabel != "" {
						meta = strings.TrimSpace(r.TypeLabel + " · " + meta)
						meta = strings.TrimSuffix(meta, " ·")
					}
					if meta != "" {
						ui.Text(c, meta).FontSize(12).TextColor(metaColor).SingleLine()
					}
				})
			}
			if r.Open != nil && !r.Invalid {
				body := ui.ButtonBase(c).Label(r.Label).Grow(1).Padding(0).Gap(12).AlignItems(ui.Center).Justify(ui.Start).ClipX()
				body.Children(content)
				if body.Clicked() {
					r.Open()
				}
			} else {
				ui.Row(c).Label(r.Label).Grow(1).Gap(12).AlignItems(ui.Center).ClipX().Children(content)
			}
			if r.Invalid {
				return
			}
			browser := r.BrowserURL
			if browser == "" && r.CopyURL == "" && (r.BVID == "" || dotStatus != "") && r.Extra == nil {
				return
			}
			open := a.PostMenuKey == r.MenuKey
			MoreMenuButton(c, r.MenuName, 28, &open, func(m *ui.Menu) {
				if r.BVID != "" && dotStatus == "" && m.Item("缓存").Chosen() {
					_, err := a.backend.EnqueueVideoCache(context.Background(), download.VideoEnqueueRequest{BVID: r.BVID, ForMid: r.ForMid})
					a.showError(err)
					if err == nil {
						a.BannerInfo = "已加入缓存队列：" + r.BVID
					}
					a.RefreshData()
				}
				if r.CopyURL != "" && m.Item("复制链接").Chosen() {
					c.WriteClipboard(r.CopyURL)
					c.Toast("已复制链接")
				}
				if browser != "" && m.Item("在浏览器中打开").Chosen() {
					_ = a.backend.Desktop().OpenExternalURL(browser)
				}
				if r.Extra != nil {
					r.Extra(m)
				}
			})
			keyedMenuOpen(&a.PostMenuKey, r.MenuKey, open)
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
	{"network", "link", "网络"},
	{"logs", "logs", "日志"},
	{"update", "refresh", "更新"},
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
	ui.Column(c).Fill().Padding(max(10, a.titleBar.Height+2), 8, 10, 8).Gap(2).ClipX().Children(func() {
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
		biliAcct := a.backend.StatusOf("bilibili")
		biliRenew := a.backend.Renewal("bilibili")
		dyAcct := a.backend.StatusOf("douyin")

		ui.Column(c).FillWidth().Gap(0).ClipX().Children(func() {
			// Group 1: 哔哩哔哩 (docs/ui.md §3.7)
			ui.Column(c).
				Label("账号分组：哔哩哔哩").
				FillWidth().
				Padding(0, 0, 18, 0).
				BorderWidth(0, 0, 1, 0).
				BorderColor(ColorBorder).
				Gap(12).
				ClipX().
				Children(func() {
					ui.Text(c, "哔哩哔哩").FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()

					if biliAcct.LoggedIn || biliAcct.State == "expired" {
						ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(12).ClipX().Children(func() {
							a.renderAccountAvatar(c, "bilibili", firstNonEmptyStrUI(biliAcct.Uname, "未登录"), biliAcct.UID, biliAcct.Mid, 48, false, "", nil, biliAcct.Face)
							ui.Column(c).Grow(1).Gap(3).ClipX().Children(func() {
								name := firstNonEmptyStrUI(biliAcct.Uname, fmt.Sprintf("UID %d", biliAcct.Mid))
								ui.Text(c, name).FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
								sourceText := "手动凭证（粘贴 Cookie）"
								if biliAcct.Source == "qr" {
									sourceText = "扫码登录"
								}
								ui.Text(c, fmt.Sprintf("UID %d · 登录方式：%s", biliAcct.Mid, sourceText)).
									FontSize(13).TextColor(ColorTextMuted).SingleLine()
							})
						})

						if biliAcct.LoggedIn && biliAcct.Source == "qr" {
							lastChk := firstNonEmptyStrUI(biliRenew.LastRenewedAt, biliAcct.LastCheckedAt, "刚刚")
							renewLine := "自动续期：可用 · 上次续期 " + lastChk
							if biliRenew.LastError != "" {
								renewLine += "（失败：" + biliRenew.LastError + "）"
							}
							ui.Text(c, renewLine).FontSize(12).TextColor(ColorTextMuted).MaxLines(2)
						} else {
							ui.Text(c, "自动续期：不可用：粘贴的 Cookie 与浏览器共用会话，续期会让浏览器那边掉线。过期后请重新粘贴，或改用扫码登录。").
								FontSize(12).TextColor(ColorTextMuted).MaxLines(3)
						}

						ui.Row(c).Gap(8).Wrap().Children(func() {
							if OutlinePillButton(c, "重新登录", "进入登录/更换账号").Clicked() {
								a.OpenLoginForSite("bilibili")
							}
							if biliAcct.LoggedIn && biliAcct.Source == "qr" {
								if OutlinePillButton(c, "检查并续期 Cookie", "检查并续期 Cookie").Clicked() {
									a.refreshAccount()
								}
							}
							if DangerOutlinePillButton(c, "退出登录", "退出登录").Clicked() {
								a.ConfirmLogoutSite = "bilibili"
								a.ConfirmLogoutOpen = true
							}
						})
					} else {
						ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(12).Children(func() {
							ui.Text(c, "未登录哔哩哔哩账号").FontSize(13).TextColor(ColorTextMuted).SingleLine()
							if AccentPillButton(c, "登录哔哩哔哩", "登录哔哩哔哩").Clicked() {
								a.OpenLoginForSite("bilibili")
							}
						})
					}
				})

			// Group 2: 抖音 (docs/ui.md §3.7)
			ui.Column(c).
				Label("账号分组：抖音").
				FillWidth().
				Padding(18, 0, 0, 0).
				Gap(12).
				ClipX().
				Children(func() {
					ui.Text(c, "抖音").FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()

					if dyAcct.LoggedIn || dyAcct.State == "expired" {
						handleLine := firstNonEmptyStrUI(formatDouyinHandle(dyAcct.UniqueID, "", dyAcct.UID), "抖音号：—")
						ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(12).ClipX().Children(func() {
							a.renderAccountAvatar(c, "douyin", firstNonEmptyStrUI(dyAcct.Uname, "抖音用户"), dyAcct.UID, 0, 48, false, "", nil, dyAcct.Face)
							ui.Column(c).Grow(1).Gap(3).ClipX().Children(func() {
								ui.Text(c, firstNonEmptyStrUI(dyAcct.Uname, "抖音用户")).FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
								ui.Text(c, handleLine+" · 登录方式：粘贴 Cookie").
									FontSize(13).TextColor(ColorTextMuted).SingleLine()
							})
						})

						ui.Text(c, "自动续期：不可用，粘贴的 Cookie 与浏览器共用会话；过期后请重新粘贴").
							FontSize(12).TextColor(ColorTextMuted).MaxLines(2)

						ui.Row(c).Gap(8).Wrap().Children(func() {
							if OutlinePillButton(c, "重新粘贴", "重新粘贴抖音Cookie").Clicked() {
								a.OpenLoginForSite("douyin")
							}
							if DangerOutlinePillButton(c, "退出登录", "退出抖音登录").Clicked() {
								a.ConfirmLogoutSite = "douyin"
								a.ConfirmLogoutOpen = true
							}
						})
					} else {
						ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(12).Children(func() {
							ui.Text(c, "未登录抖音账号").FontSize(13).TextColor(ColorTextMuted).SingleLine()
							if AccentPillButton(c, "登录抖音", "登录抖音").Clicked() {
								a.OpenLoginForSite("douyin")
							}
						})
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
		schedStatuses := a.backend.SchedulerStatuses()
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

			// Per-site scheduler schedule & backoff lines (docs/ui.md §3.7)
			if len(schedStatuses) > 0 {
				var backoffLines []struct {
					SiteID string
					Text   string
				}
				var timeLines []string
				for _, siteID := range site.All() {
					st, ok := schedStatuses[string(siteID)]
					if !ok {
						continue
					}
					if strings.TrimSpace(st.BackoffSummary) != "" {
						backoffLines = append(backoffLines, struct {
							SiteID string
							Text   string
						}{SiteID: string(siteID), Text: strings.TrimSpace(st.BackoffSummary)})
					}
					if st.LastCheckAt != "" || st.NextCheckAt != "" {
						siteLabel := site.DisplayName(string(siteID))
						lastTxt := firstNonEmptyStrUI(st.LastCheckAt, "尚未检查")
						nextTxt := firstNonEmptyStrUI(st.NextCheckAt, "未排程")
						timeLines = append(timeLines, fmt.Sprintf("%s：上次检查 %s · 下次检查 %s", siteLabel, lastTxt, nextTxt))
					}
				}
				if len(timeLines) > 0 || len(backoffLines) > 0 {
					ui.Column(c).FillWidth().Padding(10, 0).BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).Gap(6).ClipX().Children(func() {
						for _, tl := range timeLines {
							line := tl
							ui.Text(c, line).FontSize(12).TextColor(ColorTextMuted).SingleLine()
						}
						for _, bl := range backoffLines {
							item := bl
							ui.Text(c, item.Text).
								Label("退避状态：" + item.SiteID).
								FontSize(12).
								FontWeight(600).
								TextColor(ColorDotYellow).
								MaxLines(2)
						}
					})
				}
			}

			if settingSwitchRow(c, "开播状态检查", "只在有订阅开启开播自动录制时运行", "开关直播轮询", &a.DraftSettings.Live.PollEnabled) {
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

			// Directory overview rows (InstallDir, DataDir, CacheDir, Library usage)
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

			// Add Existing Cache Folder
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

	case "network":
		a.renderNetworkSettings(c)

	case "logs":
		a.refreshRuntimeLogs(c.Now())
		c.After(a.runtimeLogsNextRead.Sub(c.Now()))
		diag := a.runtimeDiagnostics
		logLines := a.runtimeLogLines
		joinedLogs := strings.Join(logLines, "\n")

		ui.Column(c).FillWidth().Gap(14).ClipX().Children(func() {

			// Top: Background Status
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

			// Bottom: Recent Runtime Logs (up to 500 sanitized lines, monospace font, open folder & copy buttons)
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
				ui.Scroll(c).FillWidth().Height(260).Padding(10, 12).Radius(10).Background(ColorBgMain).Border(1, ColorBorder).ClipX().Children(func() {
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

	case "update":
		a.renderUpdateSettings(c)

	case "about":
		a.renderAboutSettings(c)
	}
}

// ============================================================================
// Route: /login (Centered Social Login Card with Two-Segment Site Switcher, docs/ui.md §3.8)
// ============================================================================

func (a *App) renderLoginRoute(c *ui.Context) {
	loginSite := normalizeSiteIDUI(a.LoginSite)
	siteStatus := a.backend.StatusOf(loginSite)

	ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
		ui.Column(c).FillWidth().Padding(20, 18).AlignItems(ui.Center).Gap(14).ClipX().Children(func() {
			if siteStatus.State == "expired" {
				ui.Row(c).FillWidth().Padding(10, 14).Radius(12).Background(ColorWarnBg).Border(1, ColorDotYellow).AlignItems(ui.Center).Gap(8).ClipX().Children(func() {
					DrawIcon(c, "alert-circle", 16, ColorDotYellow, 1.8)
					ui.Text(c, "登录已失效，请重新登录").
						FontSize(13).FontWeight(600).TextColor(ColorTextMain).MaxLines(2)
				})
			}
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
				Background(ColorBgMain).
				Border(1, ColorBorder).
				Gap(16).
				ClipX().
				Children(func() {
					renderTwoSegmentLoginSiteSwitcher(c, loginSite, func(siteID string) {
						if siteID != loginSite {
							a.cancelQRLogin()
							a.LoginSite = siteID
							a.LoginError = ""
						}
					})

					if loginSite == "douyin" {
						ui.Column(c).Gap(4).Children(func() {
							ui.Text(c, "登录抖音").FontSize(20).FontWeight(600).TextColor(ColorTextMain).SingleLine()
							ui.Text(c, "TRACE 需要登录后才能解析和缓存").
								FontSize(13).TextColor(ColorTextMuted).MaxLines(2)
						})

						if siteStatus.LoggedIn {
							handleLine := firstNonEmptyStrUI(formatDouyinHandle(siteStatus.UniqueID, "", siteStatus.UID), "抖音号：—")
							ui.Row(c).
								Label("抖音已登录摘要").
								FillWidth().
								Padding(12, 14).
								Radius(12).
								Background(ColorBgMain).
								Border(1, ColorBorder).
								AlignItems(ui.Center).
								Gap(10).
								ClipX().
								Children(func() {
									a.renderAccountAvatar(c, "douyin", firstNonEmptyStrUI(siteStatus.Uname, "抖音用户"), siteStatus.UID, 0, 40, false, "", nil, siteStatus.Face)
									ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
										ui.Text(c, firstNonEmptyStrUI(siteStatus.Uname, "抖音用户")).FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
										ui.Text(c, handleLine).FontSize(12).TextColor(ColorTextMuted).SingleLine()
									})
								})
						}

						// Douyin has Paste-Cookie ONLY (docs/ui.md §3.8)
						ui.Column(c).
							FillWidth().
							Padding(16).
							Radius(12).
							Background(ColorBgMain).
							Border(1, ColorBorder).
							Gap(10).
							ClipX().
							Children(func() {
								ui.Text(c, "粘贴凭证（浏览器 Cookie）").FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
								ui.Text(c, "1. 在桌面浏览器打开 douyin.com 并登录账号\n2. 按 F12 打开开发者工具 → 网络 (Network)，刷新页面并点开任意请求\n3. 复制请求头 (Request Headers) 中的整段 Cookie 粘贴到下方").
									FontSize(12).TextColor(ColorTextMuted).MaxLines(5)

								inp := ui.TextInput(c, &a.DouyinCookieInput).
									Password().
									Placeholder("粘贴 Cookie（至少包含 sessionid）").
									Label("抖音Cookie输入框").
									FillWidth()
								if inp.Submitted() && !inp.Composing() {
									a.importDouyinCookie()
								}

								ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
									ui.Text(c, "凭证只保存在这台电脑上，并按当前 Windows 用户加密").
										FontSize(12).TextColor(ColorTextMuted).SingleLine()
									label := "验证并登录"
									if a.pending["douyin-cookie-login"] {
										label = "正在验证…"
									}
									if AccentPillButton(c, label, "验证并登录抖音").Disabled(a.pending["douyin-cookie-login"]).Clicked() {
										a.importDouyinCookie()
									}
								})
							})
						return
					}

					ui.Column(c).Gap(4).Children(func() {
						ui.Text(c, "登录哔哩哔哩").FontSize(20).FontWeight(600).TextColor(ColorTextMain).SingleLine()
						ui.Text(c, "TRACE 需要登录后才能解析和缓存").
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

					// 1. QR Code Login Section
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

					// 2. Manual Cookie Paste Section (Masked input, cleared immediately on submit, docs/ui.md §3.8)
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
// card knew, then the failure and a retry if OpenLink fails.
func (a *App) renderOpening(c *ui.Context) {
	p := a.Opening
	ui.Row(c).Label("正在打开").FillWidth().AlignItems(ui.Center).Gap(10).ClipX().Children(func() {
		if p.Mid > 0 || p.AuthorUID != "" || p.AuthorName != "" {
			a.renderAccountAvatar(c, p.Site, firstNonEmptyStrUI(p.AuthorName, "UP"), p.AuthorUID, p.Mid, 42, false, "", nil)
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
