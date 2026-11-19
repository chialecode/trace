package ui

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

type navItem struct {
	Route string
	Icon  string
	Label string
}

// Main navigation has 6 items; Settings is pinned in the bottom-left rail.
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
	// controls clear of the window buttons.
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
						(a.CurrentRoute == RouteLibrary && a.Transfer.ImportPage) ||
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
						} else if a.CurrentRoute == RouteLibrary {
							backLabel = "返回媒体库"
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
							} else if a.CurrentRoute == RouteLibrary {
								a.Transfer.ImportPage = false
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
			if a.hasForegroundPending() && a.CurrentRoute != RouteParse {
				ui.Text(c, "正在加载…").Label("请求进行中").FontSize(12).TextColor(ColorInfoBlue)
			}

			if a.BannerError != "" && !(a.CurrentRoute == RouteParse && a.BannerError == a.LastParseError) {
				renderErrorBanner(c, "", a.BannerError, func() {
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

	// Center Body (Virtualized list on /home, or scrollable container on other routes)
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
		a.renderSettingsRoute(c)
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
		if a.Transfer.ImportPage {
			return "导入"
		}
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
			checkingUpdates := a.isPending("check-updates")
			if checkingUpdates {
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
				if m.Item("检查全部").Disabled(checkingUpdates).Chosen() {
					a.checkAllSubscriptions()
				}
				if m.Item("从 B 站关注列表导入").Chosen() {
					a.openImportFollowingsModal()
				}
				if m.Item("导出订阅…").Chosen() {
					a.openSubExport()
				}
				if m.Item("导入订阅…").Chosen() {
					a.openSubImport()
				}
			})
			a.SubMoreMenuOpen = open
		})
	case RouteLibrary:
		if !a.Transfer.ImportPage {
			a.renderLibraryMoreMenu(c)
		}
	case RouteTasks:
		if a.effectiveTasksSection() != "downloads" {
			return
		}
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			checkingUpgrades := a.isPending("upgrade-check")
			btnText := "检查画质更新"
			if checkingUpgrades {
				btnText = fmt.Sprintf("检查 %d/%d · 取消", a.UpgradeDone, a.UpgradeTotal)
			}
			if OutlinePillButton(c, btnText, "检查画质升级").Clicked() {
				if checkingUpgrades {
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

// finishTaskAction shows a sanitized failed task action and logs the raw error.
func (a *App) finishTaskAction(err error) {
	a.TaskActionError = ""
	if err != nil {
		a.backend.LogUserError(err)
		a.TaskActionError = "操作没有完成：" + userErrorText(err)
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
		renderErrorBanner(c, "", p.Err, func() {
			if OutlinePillButton(c, "重试", "重试打开").Clicked() {
				a.RetryOpening()
			}
		})
	}
}
