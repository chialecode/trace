package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
)

// The window is an icon rail, a context panel for the page, and the page
// (DEC-039, docs/ui.md §1): the rail and the panel never scroll with the page.
// The rail is 52 wide (DEC-046 ④).
const railWidth = 52

// contextPanelWidth is the width of the panel beside the rail on the current
// route, or 0 when the route has none or the window is too narrow for one.
// Settings keeps its categories at any width, as icons when narrow.
func (a *App) contextPanelWidth(w float32) float32 {
	switch a.CurrentRoute {
	case RouteSettings:
		if w >= 1000 {
			return 232
		}
		return 72
	case RouteHome, RouteSubscriptions, RouteMine, RouteLibrary, RouteLibraryItem, RouteTasks, RouteParse, RouteVideo, RouteUser, RouteDouyinWork, RouteDouyinUser:
		if w >= 1280 {
			return 272
		}
		if w >= 1000 {
			return 240
		}
	}
	return 0
}

// pageWidth keeps posts and details at a readable width in a wide window;
// grids and lists of rows use the whole width.
func (a *App) pageWidth(contentW float32) float32 {
	switch a.CurrentRoute {
	case RouteHome, RouteVideo, RouteUser, RouteDouyinWork, RouteLibraryItem, RouteLogin:
		return min(contentW, 680)
	case RouteSubscriptions:
		if a.SubDetailOpen {
			return min(contentW, 680)
		}
	case RouteSettings:
		return min(contentW, 860)
	case RouteMine:
		// Rows read like the user page's uploads; the Douyin grid uses the width.
		if !mineIsWorkGrid(a.Mine.Site, a.Mine.Kind) || !a.mineShowsList() {
			return min(contentW, 680)
		}
	}
	return contentW
}

// renderRail draws the icon rail: the pages, then the account and Settings
// at the bottom. Every entry is an icon with its name as tooltip and label.
func (a *App) renderRail(c *ui.Context) {
	workCount := a.activeWorkCount()
	anyLive := a.anyLiveRoomActive()
	// The mark sits level with the panel's title, in the strip that moves
	// the window under a hidden title bar (it is not a button), so the
	// first page button lines up with the panel's first row (DEC-057).
	ui.Column(c).Fill().Padding(12, 0, 12, 0).AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
		ui.Column(c).AlignItems(ui.Center).Gap(6).Children(func() {
			ui.Box(c).Label("TRACE 标志").Size(40, 32).Center().Children(func() {
				ui.Text(c, "T").FontSize(20).FontWeight(700).TextColor(ColorTextMain).SingleLine()
			})
			for _, item := range mainNavItems {
				nav := item
				active := a.CurrentRoute == nav.Route ||
					(nav.Route == RouteLibrary && a.CurrentRoute == RouteLibraryItem) ||
					(nav.Route == RouteParse && (a.CurrentRoute == RouteVideo || a.CurrentRoute == RouteUser || a.CurrentRoute == RouteDouyinWork || a.CurrentRoute == RouteDouyinUser))
				badge, dot := 0, false
				tip := nav.Label
				switch nav.Route {
				case RouteSubscriptions:
					// The orange dot only means someone subscribed is live now
					// (not unread); the tooltip says so (2026-11-15 feedback).
					dot = anyLive
					if dot {
						tip = "订阅 · 有订阅正在直播"
					}
				case RouteTasks:
					badge = workCount
					if badge > 0 {
						tip = fmt.Sprintf("任务 · %d 个下载排队或进行中", badge)
					}
				}
				if railButtonTip(c, nav.Icon, nav.Label, tip, active, badge, dot) {
					a.Navigate(nav.Route)
				}
			}
		})

		acct := a.backend.StatusOf("bilibili")
		acctSite := "bilibili"
		if !acct.LoggedIn {
			if dy := a.backend.StatusOf("douyin"); dy.LoggedIn {
				acct = dy
				acctSite = "douyin"
			}
		}
		name := acct.Uname
		switch {
		case name != "":
		case acct.LoggedIn && acctSite == "bilibili":
			name = fmt.Sprintf("UID %d", acct.Mid)
		case acct.LoggedIn && acctSite == "douyin":
			name = firstNonEmptyStrUI(acct.UniqueID, acct.UID, "抖音账号")
		case acct.State == "expired":
			name = "登录已过期"
		default:
			name = "未登录"
		}
		tip := name
		if acctSite == "bilibili" && acct.Mid > 0 {
			tip = fmt.Sprintf("%s (UID %d)", name, acct.Mid)
		} else if acctSite == "douyin" && acct.UniqueID != "" {
			tip = fmt.Sprintf("%s (抖音号 %s)", name, acct.UniqueID)
		}
		ui.Column(c).AlignItems(ui.Center).Gap(10).Children(func() {
			a.renderAccountAvatar(c, acctSite, name, firstNonEmptyStrUI(acct.SecUID, acct.UID), acct.Mid, 32, false, "账号："+tip, func() {
				if a.backend.HasAnyLogin() {
					a.SettingsCategory = "account"
					a.Navigate(RouteSettings)
				} else {
					a.Navigate(RouteLogin)
				}
			}, acct.Face)
			if railButton(c, "settings", "设置", a.CurrentRoute == RouteSettings, 0, false) {
				a.Navigate(RouteSettings)
			}
		})
	})
}

func railButton(c *ui.Context, icon, label string, active bool, badge int, dot bool) bool {
	return railButtonTip(c, icon, label, label, active, badge, dot)
}

// railButtonTip is railButton with a tooltip that can explain the mark.
func railButtonTip(c *ui.Context, icon, label, tooltip string, active bool, badge int, dot bool) bool {
	btn := ui.ButtonBase(c).Label(label).Tooltip(tooltip).Size(40, 40).Radius(10).Center()
	if active {
		btn.Background(ColorAccentBg)
	} else if btn.Hovered() {
		btn.Background(ColorHover)
	}
	btn.Children(func() {
		DrawNavIcon(c, icon, active)
		switch {
		case badge > 0:
			text := strconv.Itoa(badge)
			if badge > 99 {
				text = "99+"
			}
			ui.Box(c).Absolute().Top(3).Right(1).Padding(1, 5).Radius(999).Background(ColorAccent).Children(func() {
				ui.Text(c, text).FontSize(10).FontWeight(600).TextColor(ColorWhite).SingleLine()
			})
		case dot:
			ui.Box(c).Absolute().Top(7).Right(7).Size(8, 8).Radius(4).Background(ColorLive)
		}
	})
	return btn.Clicked()
}

// renderContextPanel draws the panel beside the rail: what to look at
// within the page, as the folders of a library or a chat list.
func (a *App) renderContextPanel(c *ui.Context, width float32) {
	switch a.CurrentRoute {
	case RouteSettings:
		a.renderSettingsSidebar(c, width < 100)
	case RouteHome:
		a.renderHomePanel(c)
	case RouteSubscriptions:
		a.renderSubscriptionsPanel(c)
	case RouteLibrary, RouteLibraryItem:
		a.renderLibraryPanel(c)
	case RouteTasks:
		a.renderTasksPanel(c)
	case RouteMine:
		a.renderMinePanel(c)
	case RouteParse, RouteVideo, RouteUser, RouteDouyinWork, RouteDouyinUser:
		a.renderHistoryPanel(c)
	}
}

func panelTitle(c *ui.Context, text string) {
	ui.Row(c).FillWidth().Padding(18, 18, 10, 18).Shrink(0).Children(func() {
		ui.Text(c, text).FontSize(18).FontWeight(600).TextColor(ColorTextMain).SingleLine()
	})
}

func panelSection(c *ui.Context, text string) {
	ui.Row(c).FillWidth().Padding(14, 10, 4, 10).Children(func() {
		ui.Text(c, text).FontSize(12).FontWeight(500).TextColor(ColorTextMuted).SingleLine()
	})
}

// panelItem is one row of the panel: an icon or avatar and a name, the
// chosen one tinted with the accent.
func panelItem(c *ui.Context, label string, active bool, content func()) bool {
	btn := ui.ButtonBase(c).Label(label).FillWidth().Radius(8).Padding(7, 10).Gap(10).AlignItems(ui.Center).Justify(ui.Start).ClipX()
	if active {
		btn.Background(ColorAccentBg)
	} else if btn.Hovered() {
		btn.Background(ColorHover)
	}
	btn.Children(content)
	return btn.Clicked()
}

func panelItemText(c *ui.Context, text string, active bool) {
	t := ui.Text(c, text).FontSize(14).FontWeight(500).SingleLine().Grow(1)
	if active {
		t.TextColor(ColorAccent)
	} else {
		t.TextColor(ColorTextMain)
	}
}

func panelIconItem(c *ui.Context, icon, text, label string, active bool) bool {
	return panelItem(c, label, active, func() {
		col := ColorTextMuted
		if active {
			col = ColorAccent
		}
		DrawIcon(c, icon, 18, col, 1.6)
		panelItemText(c, text, active)
	})
}

// renderHomePanel: the whole timeline, the 3-segment site filter right below
// "全部动态", then the subscriptions without any section title (DEC-053 ②):
// live first (ringed), then unread (a dot), then read. The list is
// virtualized for a thousand subscriptions.
func (a *App) renderHomePanel(c *ui.Context) {
	ui.Column(c).Fill().ClipX().Children(func() {
		panelTitle(c, "主页")
		ui.Column(c).FillWidth().Padding(0, 8, 4, 8).Gap(2).Shrink(0).ClipX().Children(func() {
			if panelIconItem(c, "home", "全部动态", "主页全部动态", a.HomeActorFilter == nil) && a.HomeActorFilter != nil {
				a.closeHomeActorFeed()
			}
			ui.Row(c).FillWidth().Padding(4, 2, 2, 2).Children(func() {
				renderThreeSegmentSiteFilter(c, "主页站点筛选：", a.FeedSiteFilter, func(next string) {
					a.setFeedSiteFilter(next)
				})
			})
		})
		rows := a.homeSubscriptionRows()
		if len(rows) == 0 {
			return
		}
		a.HomeSubListState.Key = func(i int) any {
			if i >= 0 && i < len(rows) {
				return rows[i].Key
			}
			return i
		}
		ui.List(c, &a.HomeSubListState, len(rows), func(i int) {
			if i < 0 || i >= len(rows) {
				return
			}
			actor := rows[i]
			ui.Column(c).FillWidth().Padding(1, 8).Children(func() {
				active := a.HomeActorFilter != nil && a.HomeActorFilter.Key == actor.Key
				if panelItem(c, "主页订阅："+actor.Name, active, func() {
					a.renderHomeSubAvatar(c, actor, 26, ColorBgPanel, nil)
					panelItemText(c, actor.Name, active)
				}) && !active {
					a.openHomeActorFeed(actor)
				}
			})
		}).Label("主页订阅列表").Grow(1).FillWidth()
	})
}

// renderSubscriptionsPanel lists the subscriptions, virtualized for a
// thousand of them; "全部订阅" is the avatar grid.
func (a *App) renderSubscriptionsPanel(c *ui.Context) {
	ui.Column(c).Fill().ClipX().Children(func() {
		panelTitle(c, "订阅")
		ui.Column(c).FillWidth().Padding(0, 8, 4, 8).Shrink(0).Children(func() {
			if panelIconItem(c, "grid", "全部订阅", "订阅侧栏：全部订阅", !a.SubDetailOpen && !a.SubAccountsOpen) {
				a.SubDetailOpen = false
				a.SubAccountsOpen = false
				a.SubManageMenuOpen = false
				a.SubManageModalMode = ""
			}
			if panelIconItem(c, "user", "全部账号", "订阅侧栏：全部账号", !a.SubDetailOpen && a.SubAccountsOpen) {
				a.openSubscriptionAccounts()
			}
			if len(a.Subscriptions) > 0 {
				panelSection(c, fmt.Sprintf("订阅 · %d", len(a.Subscriptions)))
			}
		})
		subs := a.Subscriptions
		a.SubPanelListState.Key = func(i int) any { return subs[i].ID }
		ui.List(c, &a.SubPanelListState, len(subs), func(i int) {
			s := subs[i]
			ui.Column(c).FillWidth().Padding(1, 8).Children(func() {
				active := a.SubDetailOpen && a.SelectedSubID == s.ID
				if panelItem(c, "订阅侧栏："+s.ID, active, func() {
					pSite, pUID := subscriptionPrimarySiteAndUID(s)
					a.renderAccountAvatar(c, pSite, s.Name, pUID, s.Mid, 26, a.isSubscriptionLive(s), "", nil, s.Face)
					panelItemText(c, s.Name, active)
				}) {
					a.OpenSubscriptionDetail(s)
				}
			})
		}).Label("订阅侧栏列表").Grow(1).FillWidth()
	})
}

// renderLibraryPanel holds the library's filters: the kind, then the 来源
// section (site 3-segment switcher + subscription or creator).
func (a *App) renderLibraryPanel(c *ui.Context) {
	stats := a.libraryStatistics
	ui.Column(c).Fill().ClipX().Children(func() {
		panelTitle(c, "媒体库")
		ui.Scroll(c).Grow(1).FillWidth().Children(func() {
			ui.Column(c).FillWidth().Padding(0, 8, 12, 8).Gap(2).ClipX().Children(func() {
				for _, k := range []struct{ key, icon, text, label string }{
					{"", "library", "全部", "筛选类型：全部类型"},
					{"video", "video", "视频", "筛选类型：视频"},
					{"image", "image", "图片", "筛选类型：图片"},
					{"live", "live", "直播录像", "筛选类型：直播录制"},
					{"forward", "share", "转发", "筛选类型：转发"},
				} {
					kind := k
					if panelIconItem(c, kind.icon, kind.text, kind.label, a.LibraryKindFilter == kind.key) {
						a.LibraryKindFilter = kind.key
						a.selectLibraryFilter()
					}
				}

				panelSection(c, "来源")
				ui.Row(c).FillWidth().Padding(2, 2, 4, 2).Children(func() {
					renderThreeSegmentSiteFilter(c, "媒体库站点筛选：", a.LibrarySiteFilter, func(next string) {
						a.LibrarySiteFilter = normalizeSiteFilter(next)
						a.selectLibraryFilter()
					})
				})

				ownedKeys := make(map[string]bool)
				ownedMids := make(map[int64]bool)
				for _, sub := range a.Subscriptions {
					for _, account := range sub.Accounts {
						normSite := normalizeSiteIDUI(account.Site)
						uid := strings.TrimSpace(account.UID)
						if uid != "" {
							ownedKeys[normSite+":"+uid] = true
						}
						if normSite == "bilibili" {
							if id, err := strconv.ParseInt(uid, 10, 64); err == nil && id > 0 {
								ownedMids[id] = true
							}
						}
					}
				}
				var others int
				for _, author := range stats.Authors {
					aSite := normalizeSiteIDUI(author.Site)
					aUID := strings.TrimSpace(author.UID)
					if aUID == "" && author.Mid > 0 {
						aUID = strconv.FormatInt(author.Mid, 10)
					}
					if !ownedKeys[aSite+":"+aUID] && !(aSite == "bilibili" && ownedMids[author.Mid]) {
						others++
					}
				}
				if len(a.Subscriptions) == 0 && others == 0 && a.LibraryCreatorMid == 0 && a.LibraryAuthorKey == "" && a.LibrarySubFilter == "" {
					return
				}
				if panelIconItem(c, "subscriptions", "全部来源", "清除UP主筛选", a.LibraryCreatorMid == 0 && a.LibraryAuthorKey == "" && a.LibrarySubFilter == "") {
					a.LibraryCreatorMid, a.LibraryAuthorKey, a.LibrarySubFilter = 0, "", ""
					a.selectLibraryFilter()
				}
				for _, s := range a.Subscriptions {
					sub := s
					active := a.LibrarySubFilter == sub.ID
					if panelItem(c, "媒体库订阅筛选："+sub.ID, active, func() {
						pSite, pUID := subscriptionPrimarySiteAndUID(sub)
						a.renderAccountAvatar(c, pSite, sub.Name, pUID, sub.Mid, 24, false, "", nil, sub.Face)
						panelItemText(c, sub.Name, active)
					}) {
						a.LibrarySubFilter, a.LibraryCreatorMid, a.LibraryAuthorKey = sub.ID, 0, ""
						a.selectLibraryFilter()
					}
				}
				for _, au := range stats.Authors {
					author := au
					aSite := normalizeSiteIDUI(author.Site)
					aUID := strings.TrimSpace(author.UID)
					if aUID == "" && author.Mid > 0 {
						aUID = strconv.FormatInt(author.Mid, 10)
					}
					aKey := aSite + ":" + aUID
					if ownedKeys[aKey] || (aSite == "bilibili" && ownedMids[author.Mid]) {
						continue
					}
					label := fmt.Sprintf("按UP主筛选 %d", author.Mid)
					active := aSite == "bilibili" && author.Mid > 0 && a.LibraryCreatorMid == author.Mid && a.LibraryAuthorKey == ""
					if aSite == "douyin" || author.Mid <= 0 {
						label = "按作者筛选 " + aKey
						active = a.LibraryAuthorKey == aKey
					} else if a.LibraryAuthorKey == aKey {
						active = true
					}
					if panelItem(c, label, active, func() {
						a.renderAccountAvatar(c, aSite, author.Name, aUID, author.Mid, 24, false, "", nil, author.Face)
						panelItemText(c, author.Name, active)
					}) {
						if aSite == "bilibili" && author.Mid > 0 {
							a.LibraryCreatorMid, a.LibraryAuthorKey, a.LibrarySubFilter = author.Mid, "", ""
						} else {
							a.LibraryCreatorMid, a.LibraryAuthorKey, a.LibrarySubFilter = 0, aKey, ""
						}
						a.selectLibraryFilter()
					}
				}
			})
		})
		ui.Row(c).FillWidth().Padding(10, 18, 14, 18).Shrink(0).BorderWidth(1, 0, 0, 0).BorderColor(ColorBorder).Children(func() {
			ui.Text(c, fmt.Sprintf("%d 项 · %s", stats.Items, FormatBytes(stats.Bytes))).FontSize(12).TextColor(ColorTextMuted).SingleLine()
		})
	})
}

// renderHistoryPanel lists recent parses on /parse and beside video, work, or creator pages.
func (a *App) renderHistoryPanel(c *ui.Context) {
	ui.Column(c).Fill().ClipX().Children(func() {
		ui.Row(c).FillWidth().Padding(14, 12, 8, 18).Shrink(0).AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(6).ClipX().Children(func() {
			ui.Text(c, "最近解析").FontSize(18).FontWeight(600).TextColor(ColorTextMain).SingleLine()
			if len(a.HistoryItems) > 0 {
				open := a.HistoryHeaderMenuOpen
				MoreMenuButton(c, "最近解析更多", 28, &open, func(m *ui.Menu) {
					if m.Item("全部清空").Danger().Chosen() {
						a.HistoryClearConfirmOpen = true
					}
				})
				a.HistoryHeaderMenuOpen = open
			}
		})

		if len(a.HistoryItems) > 0 && a.HistoryClearConfirmOpen {
			ui.Column(c).
				Label("确认清空最近解析栏").
				FillWidth().
				Padding(8, 12).
				Background(ColorWarnBg).
				BorderWidth(1, 0, 1, 0).
				BorderColor(ColorDotYellow).
				Gap(8).
				Shrink(0).
				ClipX().
				Children(func() {
					ui.Text(c, "确认清空全部最近解析记录？同时清理缓存的缩略图。").
						FontSize(12).FontWeight(600).TextColor(ColorTextMain).MaxLines(2)
					ui.Row(c).FillWidth().Justify(ui.End).Gap(6).AlignItems(ui.Center).Children(func() {
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

		ui.Scroll(c).Grow(1).FillWidth().Children(func() {
			ui.Column(c).FillWidth().Padding(2, 8, 12, 8).Gap(2).ClipX().Children(func() {
				if len(a.HistoryItems) == 0 {
					ui.Text(c, "还没有解析过链接").FontSize(13).TextColor(ColorTextMuted).Padding(8, 10)
				}
				for i, h := range a.HistoryItems {
					if i >= 50 {
						break
					}
					entry := h
					entrySite := normalizeSiteIDUI(entry.Site)
					var mid int64
					if entrySite == "bilibili" {
						_, _ = fmt.Sscanf(entry.ID, "%d", &mid)
					} else {
						mid = accountSeedHash(entrySite, entry.ID)
					}
					author := entry.Subtitle
					if entry.Kind == "user" || author == "" {
						author = entry.Title
					}
					title := firstNonEmptyStrUI(entry.Title, entry.Subtitle, entry.ID)
					kindLabel := "视频"
					switch entry.Kind {
					case "user":
						kindLabel = "用户"
					case "note":
						kindLabel = "图文"
					case "dynamic":
						if entrySite == "douyin" {
							kindLabel = "图文"
						} else {
							kindLabel = "动态"
						}
					case "live":
						kindLabel = "直播"
					}

					active := a.isCurrentHistoryEntry(entrySite, entry.Kind, entry.ID)
					itemMenuKey := entrySite + ":" + entry.Kind + ":" + entry.ID
					legacyMenuKey := entry.Kind + ":" + entry.ID
					itemMenuOpen := a.HistoryItemMenuID == itemMenuKey || a.HistoryItemMenuID == legacyMenuKey || a.HistoryItemMenuID == entry.ID
					avatarBmp := a.lookupHistoryThumbBitmap(entry.AvatarThumb)
					coverBmp := a.lookupHistoryThumbBitmap(entry.CoverThumb)

					row := ui.Row(c).FillWidth().Radius(8).Padding(5, 6, 5, 8).Gap(6).AlignItems(ui.Center).ClipX()
					if active {
						row.Background(ColorAccentBg)
					} else if row.Hovered() {
						row.Background(ColorHover)
					}
					rowHovered := row.Hovered()
					row.Children(func() {
						bodyBtn := ui.ButtonBase(c).
							Label("历史打开：" + entry.ID).
							Grow(1).
							Padding(0).
							Gap(8).
							AlignItems(ui.Center).
							Justify(ui.Start).
							ClipX()
						bodyBtn.Children(func() {
							if entry.Kind == "user" || (entry.CoverThumb == "" && entry.Kind != "video" && entry.Kind != "note" && entry.Kind != "live") {
								a.renderAvatarWithBitmap(c, author, mid, 32, false, "", nil, avatarBmp)
							} else {
								ui.Box(c).Size(44, 30).Radius(5).Background(ColorBgMain).Border(1, ColorBorder).Clip().Shrink(0).Center().Children(func() {
									if coverBmp != nil {
										ui.Image(c, coverBmp).Size(44, 30).Fit(ui.Cover)
									} else if avatarBmp != nil {
										ui.Image(c, avatarBmp).Size(44, 30).Fit(ui.Cover)
									} else {
										iconName := "video"
										if entry.Kind == "note" || entry.Kind == "dynamic" {
											iconName = "image"
										} else if entry.Kind == "live" {
											iconName = "live"
										}
										DrawIcon(c, iconName, 14, ColorTextMuted, 1.7)
									}
								})
							}
							ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
								titleCol := ColorTextMain
								weight := 500
								if active {
									titleCol = ColorAccent
									weight = 600
								}
								ui.Text(c, title).FontSize(13).FontWeight(weight).TextColor(titleCol).SingleLine()
								ui.Row(c).AlignItems(ui.Center).Gap(4).ClipX().Children(func() {
									ui.Text(c, kindLabel).FontSize(11).TextColor(ColorTextMuted).SingleLine().Shrink(0)
									renderSiteSmallBadgeWithDot(c, entrySite, "历史站点："+entrySite+":"+entry.ID)
								})
							})
						})
						if bodyBtn.Clicked() {
							preview := &OpeningPreview{Site: entrySite, Kind: entry.Kind, Title: title, AuthorName: author}
							if entry.Kind == "user" {
								preview.Title = ""
								if entrySite == "bilibili" {
									preview.Mid = mid
								} else {
									preview.AuthorUID = entry.ID
								}
							}
							a.openLink(entry.CanonicalURL, preview)
						}

						open := itemMenuOpen
						moreBtn := ui.ButtonBase(c).
							Label("历史更多："+entry.ID).
							Tooltip("更多操作").
							Size(26, 26).
							Radius(13).
							Shrink(0).
							Center()
						showDots := rowHovered || open || moreBtn.Hovered() || moreBtn.Focused()
						if moreBtn.Hovered() || open {
							moreBtn.Background(ColorHover)
						}
						moreBtn.Children(func() {
							if showDots {
								DrawIcon(c, "more", 15, ColorTextMuted, 1.8)
							}
						})
						moreBtn.PopoverMenuButton(func(m *ui.Menu) {
							if entry.CanonicalURL != "" && m.Item("在浏览器中打开").Chosen() {
								_ = a.backend.Desktop().OpenExternalURL(entry.CanonicalURL)
							}
							if m.Item("删除").Danger().Chosen() {
								a.showError(a.backend.RemoveHistoryForSite(entrySite, entry.Kind, entry.ID))
								a.HistoryItems = a.backend.ListHistory()
							}
						}, ui.PopoverMenuButtonOptions{At: ui.AnchorBottomEnd, Self: ui.AnchorTopEnd, Open: &open})
						if open {
							a.HistoryItemMenuID = itemMenuKey
						} else if itemMenuOpen {
							a.HistoryItemMenuID = ""
						}
					})
				}
			})
		})
	})
}

// isCurrentHistoryEntry reports whether the page shows this history entry.
func (a *App) isCurrentHistoryEntry(siteID, kind, id string) bool {
	if normalizeSiteIDUI(siteID) == "douyin" {
		switch kind {
		case "video", "note", "dynamic":
			return a.CurrentRoute == RouteDouyinWork && a.CurrentDouyinWork != nil && a.CurrentDouyinWork.ID == id
		case "user", "live":
			return a.CurrentRoute == RouteDouyinUser && a.CurrentDouyinUser != nil && (a.CurrentDouyinUser.SecUID == id || a.CurrentDouyinUser.WebRID == id)
		}
		return false
	}
	switch kind {
	case "video":
		return a.CurrentRoute == RouteVideo && a.CurrentVideo != nil && a.CurrentVideo.BVID == id
	case "dynamic":
		return a.CurrentRoute == RouteVideo && a.CurrentDynamic != nil && a.CurrentDynamic.ID == id
	case "user", "live":
		return a.CurrentRoute == RouteUser && a.CurrentUser != nil && strconv.FormatInt(a.CurrentUser.Mid, 10) == id
	}
	return false
}
