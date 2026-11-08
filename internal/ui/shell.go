package ui

import (
	"fmt"
	"strconv"

	"github.com/egoist/mygo/ui"
)

// The window is an icon rail, a context panel for the page, and the page
// (DEC-039, docs/ui.md §1): the rail and the panel never scroll with the
// page (NFR-UI-001).
const railWidth = 64

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
	case RouteHome, RouteSubscriptions, RouteLibrary, RouteVideo, RouteUser:
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
	case RouteHome, RouteVideo, RouteUser, RouteParse, RouteLogin:
		return min(contentW, 680)
	case RouteSubscriptions:
		if a.SubDetailOpen {
			return min(contentW, 680)
		}
	case RouteSettings:
		return min(contentW, 860)
	}
	return contentW
}

// renderRail draws the icon rail: the pages, then the account and Settings
// at the bottom. Every entry is an icon with its name as tooltip and label.
func (a *App) renderRail(c *ui.Context) {
	workCount := a.activeWorkCount()
	anyLive := a.anyLiveRoomActive()
	ui.Column(c).Fill().Padding(12, 0).AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
		ui.Column(c).AlignItems(ui.Center).Gap(6).Children(func() {
			brand := ui.ButtonBase(c).Label("TRACE 主页").Tooltip("TRACE").Size(44, 40).Radius(12).Center()
			if brand.Hovered() {
				brand.Background(ColorHover)
			}
			brand.Children(func() {
				ui.Row(c).AlignItems(ui.Baseline).Children(func() {
					ui.Text(c, "T").FontSize(21).FontWeight(700).TextColor(ColorTextMain).SingleLine()
					ui.Text(c, ".").FontSize(23).FontWeight(700).TextColor(ColorAccent).SingleLine()
				})
			})
			if brand.Clicked() {
				a.Navigate(RouteHome)
			}
			ui.Box(c).Height(8)
			for _, item := range mainNavItems {
				nav := item
				active := a.CurrentRoute == nav.Route ||
					(nav.Route == RouteParse && (a.CurrentRoute == RouteVideo || a.CurrentRoute == RouteUser))
				badge, dot := 0, false
				switch nav.Route {
				case RouteSubscriptions:
					dot = anyLive
				case RouteTasks:
					badge = workCount
				}
				if railButton(c, nav.Icon, nav.Label, active, badge, dot) {
					a.Navigate(nav.Route)
				}
			}
		})

		acct := a.backend.AccountStatus()
		name := acct.Uname
		switch {
		case name != "":
		case acct.LoggedIn:
			name = fmt.Sprintf("UID %d", acct.Mid)
		case acct.State == "expired":
			name = "登录已过期"
		default:
			name = "未登录"
		}
		tip := name
		if acct.Mid > 0 {
			tip = fmt.Sprintf("%s (UID %d)", name, acct.Mid)
		}
		ui.Column(c).AlignItems(ui.Center).Gap(10).Children(func() {
			a.renderAvatar(c, name, acct.Mid, 32, false, "账号："+tip, func() {
				if acct.LoggedIn {
					a.SettingsCategory = "account"
					a.Navigate(RouteSettings)
				} else {
					a.Navigate(RouteLogin)
				}
			})
			if railButton(c, "settings", "设置", a.CurrentRoute == RouteSettings, 0, false) {
				a.Navigate(RouteSettings)
			}
		})
	})
}

func railButton(c *ui.Context, icon, label string, active bool, badge int, dot bool) bool {
	btn := ui.ButtonBase(c).Label(label).Tooltip(label).Size(44, 44).Radius(12).Center()
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
			ui.Box(c).Absolute().Top(9).Right(9).Size(8, 8).Radius(4).Background(ColorLive)
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
	case RouteLibrary:
		a.renderLibraryPanel(c)
	case RouteVideo, RouteUser:
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

// renderHomePanel: the whole timeline, the people with updates since you
// last looked at them, and who is live now.
func (a *App) renderHomePanel(c *ui.Context) {
	ui.Column(c).Fill().ClipX().Children(func() {
		panelTitle(c, "主页")
		ui.Scroll(c).Grow(1).FillWidth().Children(func() {
			ui.Column(c).FillWidth().Padding(0, 8, 12, 8).Gap(2).ClipX().Children(func() {
				if panelIconItem(c, "home", "全部动态", "主页全部动态", a.HomeActorFilter == nil) && a.HomeActorFilter != nil {
					a.closeHomeActorFeed()
				}
				if actors := a.homeUpdatedActors(); len(actors) > 0 {
					panelSection(c, "有更新")
					ui.Column(c).Label("主页有更新头像行").FillWidth().Gap(2).ClipX().Children(func() {
						for _, act := range actors {
							actor := act
							active := a.HomeActorFilter != nil && a.HomeActorFilter.Key == actor.Key
							if panelItem(c, "主页更新头像："+actor.Name, active, func() {
								a.renderAvatar(c, actor.Name, actor.Mid, 26, a.isActorLive(actor), "", nil, actor.Face)
								panelItemText(c, actor.Name, active)
							}) && !active {
								a.openHomeActorFeed(actor)
							}
						}
					})
				}
				a.renderLivePanelSection(c)
			})
		})
	})
}

// renderLivePanelSection lists who is live now (at most eight); a row opens
// their space, where the live banner records.
func (a *App) renderLivePanelSection(c *ui.Context) {
	shown := 0
	for _, room := range a.LiveRooms {
		if room.LiveStatus != 1 || shown >= 8 {
			continue
		}
		if shown == 0 {
			panelSection(c, "正在直播")
		}
		shown++
		rm := room
		if panelItem(c, fmt.Sprintf("侧栏直播 %d", rm.RoomID), false, func() {
			a.renderAvatar(c, rm.Uname, rm.UID, 26, true, "", nil)
			ui.Column(c).Grow(1).Gap(1).ClipX().Children(func() {
				ui.Text(c, rm.Uname).FontSize(14).FontWeight(500).TextColor(ColorTextMain).SingleLine()
				status := rm.Title
				if rm.Recording {
					status = "录制中 · " + rm.Title
				}
				ui.Text(c, status).FontSize(12).TextColor(ColorTextMuted).SingleLine()
			})
		}) {
			a.OpenUser(rm.UID, rm.Uname)
		}
	}
}

// renderSubscriptionsPanel lists the subscriptions, virtualized for a
// thousand of them (R2); "全部订阅" is the avatar grid.
func (a *App) renderSubscriptionsPanel(c *ui.Context) {
	ui.Column(c).Fill().ClipX().Children(func() {
		panelTitle(c, "订阅")
		ui.Column(c).FillWidth().Padding(0, 8, 4, 8).Shrink(0).Children(func() {
			if panelIconItem(c, "grid", "全部订阅", "订阅侧栏：全部订阅", !a.SubDetailOpen) {
				a.SubDetailOpen = false
				a.SubManageMenuOpen = false
				a.SubManageModalMode = ""
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
					a.renderAvatar(c, s.Name, s.Mid, 26, a.isSubscriptionLive(s), "", nil, s.Face)
					panelItemText(c, s.Name, active)
				}) {
					a.OpenSubscriptionDetail(s)
				}
			})
		}).Label("订阅侧栏列表").Grow(1).FillWidth()
	})
}

// renderLibraryPanel holds the library's filters: the kind, then a
// subscription or an UP 主, as the folders of a media library.
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
				} {
					kind := k
					if panelIconItem(c, kind.icon, kind.text, kind.label, a.LibraryKindFilter == kind.key) {
						a.LibraryKindFilter = kind.key
						a.reloadLibraryItems()
					}
				}
				owned := make(map[int64]bool)
				for _, sub := range a.Subscriptions {
					for _, account := range sub.Accounts {
						id, _ := strconv.ParseInt(account.UID, 10, 64)
						owned[id] = true
					}
				}
				var others int
				for _, author := range stats.Authors {
					if !owned[author.Mid] {
						others++
					}
				}
				if len(a.Subscriptions) == 0 && others == 0 {
					return
				}
				panelSection(c, "来源")
				if panelIconItem(c, "subscriptions", "全部来源", "清除UP主筛选", a.LibraryCreatorMid == 0 && a.LibrarySubFilter == "") {
					a.LibraryCreatorMid, a.LibrarySubFilter = 0, ""
					a.reloadLibraryItems()
				}
				for _, s := range a.Subscriptions {
					sub := s
					active := a.LibrarySubFilter == sub.ID
					if panelItem(c, "媒体库订阅筛选："+sub.ID, active, func() {
						a.renderAvatar(c, sub.Name, sub.Mid, 24, false, "", nil, sub.Face)
						panelItemText(c, sub.Name, active)
					}) {
						a.LibrarySubFilter, a.LibraryCreatorMid = sub.ID, 0
						a.reloadLibraryItems()
					}
				}
				for _, au := range stats.Authors {
					author := au
					if owned[author.Mid] {
						continue
					}
					active := a.LibraryCreatorMid == author.Mid
					if panelItem(c, fmt.Sprintf("按UP主筛选 %d", author.Mid), active, func() {
						a.renderAvatar(c, author.Name, author.Mid, 24, false, "", nil, author.Face)
						panelItemText(c, author.Name, active)
					}) {
						a.LibraryCreatorMid, a.LibrarySubFilter = author.Mid, ""
						a.reloadLibraryItems()
					}
				}
			})
		})
		ui.Row(c).FillWidth().Padding(10, 18, 14, 18).Shrink(0).BorderWidth(1, 0, 0, 0).BorderColor(ColorBorder).Children(func() {
			ui.Text(c, fmt.Sprintf("%d 项 · %s", stats.Items, FormatBytes(stats.Bytes))).FontSize(12).TextColor(ColorTextMuted).SingleLine()
		})
	})
}

// renderHistoryPanel lists recent parses beside a video or a space, to go
// from one to the next.
func (a *App) renderHistoryPanel(c *ui.Context) {
	ui.Column(c).Fill().ClipX().Children(func() {
		panelTitle(c, "最近解析")
		ui.Scroll(c).Grow(1).FillWidth().Children(func() {
			ui.Column(c).FillWidth().Padding(0, 8, 12, 8).Gap(2).ClipX().Children(func() {
				if len(a.HistoryItems) == 0 {
					ui.Text(c, "还没有解析过链接").FontSize(13).TextColor(ColorTextMuted).Padding(8, 10)
				}
				for i, h := range a.HistoryItems {
					if i >= 50 {
						break
					}
					entry := h
					var mid int64
					_, _ = fmt.Sscanf(entry.ID, "%d", &mid)
					author := entry.Subtitle
					if entry.Kind == "user" || author == "" {
						author = entry.Title
					}
					title := entry.Title
					if entry.Kind == "user" {
						title = firstNonEmptyStrUI(entry.Subtitle, entry.Title)
					}
					if panelItem(c, "侧栏历史："+entry.Kind+":"+entry.ID, a.isCurrentHistoryEntry(entry.Kind, entry.ID), func() {
						a.renderAvatarWithBitmap(c, author, mid, 28, false, "", nil, a.lookupHistoryThumbBitmap(entry.AvatarThumb))
						ui.Column(c).Grow(1).Gap(1).ClipX().Children(func() {
							ui.Text(c, title).FontSize(13).FontWeight(500).TextColor(ColorTextMain).SingleLine()
							ui.Text(c, author).FontSize(12).TextColor(ColorTextMuted).SingleLine()
						})
					}) {
						preview := &OpeningPreview{Kind: entry.Kind, Title: title, AuthorName: author}
						if entry.Kind == "user" {
							preview.Title, preview.Mid = "", mid
						}
						a.openLink(entry.CanonicalURL, preview)
					}
				}
			})
		})
	})
}

// isCurrentHistoryEntry reports whether the page shows this history entry.
func (a *App) isCurrentHistoryEntry(kind, id string) bool {
	switch kind {
	case "video":
		return a.CurrentRoute == RouteVideo && a.CurrentVideo != nil && a.CurrentVideo.BVID == id
	case "user":
		return a.CurrentRoute == RouteUser && a.CurrentUser != nil && strconv.FormatInt(a.CurrentUser.Mid, 10) == id
	}
	return false
}
