package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

func (a *App) subscriptionGridCols(c *ui.Context) int {
	winW, _ := c.Size()
	if winW <= 0 {
		winW = 1280
	}
	availW := winW - 88 - 32
	if winW >= 1280 {
		availW = winW - 275 - 350 - 32
	} else if winW >= 1000 {
		availW = winW - 88 - 300 - 32
	}
	cols := int(availW / 132)
	if cols < 3 {
		cols = 3
	}
	if cols > 5 {
		cols = 5
	}
	return cols
}

func (a *App) renderSubscriptionsRoute(c *ui.Context) {
	if a.SubDetailOpen && a.SelectedSubID != "" {
		for i := range a.Subscriptions {
			if a.Subscriptions[i].ID == a.SelectedSubID {
				sub := a.Subscriptions[i]
				a.renderSubscriptionDetailView(c, &sub)
				return
			}
		}
		a.SubDetailOpen = false
	}

	ui.Column(c).Grow(1).FillWidth().Padding(16).Gap(14).ClipX().Children(func() {
		// Search filter bar (docs/ui.md §3.9)
		ui.Row(c).
			FillWidth().
			Padding(4, 12).
			Radius(999).
			Background(ColorBgSecondary).
			Border(1, ColorBorder).
			AlignItems(ui.Center).
			Gap(8).
			ClipX().
			Children(func() {
				DrawIcon(c, "search", 16, ColorTextMuted, 1.75)
				ui.TextInputBase(c, &a.SubSearchQuery).
					Placeholder("搜索订阅名称、备注或关联账号昵称…").
					Label("订阅搜索过滤框").
					Padding(6, 4).
					Grow(1)
				if a.SubSearchQuery != "" {
					if OutlinePillButton(c, "清除", "清除订阅搜索").Clicked() {
						a.SubSearchQuery = ""
					}
				}
			})

		if len(a.Subscriptions) == 0 {
			ui.Column(c).FillWidth().Padding(36, 16).Radius(16).Background(ColorBgSecondary).AlignItems(ui.Center).Gap(10).Children(func() {
				ui.Text(c, "还没有订阅").FontSize(16).FontWeight(600).TextColor(ColorTextMain).SingleLine()
				ui.Text(c, "把你关心的人建成订阅，一个人可以关联他的多个账号。").FontSize(13).TextColor(ColorTextMuted).MaxLines(2)
				ui.Row(c).Gap(10).Children(func() {
					if OutlinePillButton(c, "从关注列表导入", "空状态导入关注").Clicked() {
						a.openImportFollowingsModal()
					}
				})
			})
			return
		}

		query := strings.ToLower(strings.TrimSpace(a.SubSearchQuery))
		if query != a.subListQuery {
			a.SubListState.ScrollTo(0, ui.Start)
			a.subListQuery = query
		}
		var filtered []subscription.Subscription
		for _, subItem := range a.Subscriptions {
			s := subItem
			if query != "" {
				matched := strings.Contains(strings.ToLower(s.Name), query) ||
					strings.Contains(strings.ToLower(s.Note), query)
				for _, acc := range s.Accounts {
					if strings.Contains(strings.ToLower(acc.Name), query) || strings.Contains(strings.ToLower(acc.UID), query) {
						matched = true
						break
					}
				}
				if !matched {
					continue
				}
			}
			filtered = append(filtered, s)
		}

		if len(filtered) == 0 {
			ui.Column(c).FillWidth().Padding(28, 16).Radius(16).Background(ColorBgSecondary).AlignItems(ui.Center).Gap(6).Children(func() {
				ui.Text(c, "没有匹配的订阅").FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
				ui.Text(c, "请尝试其他关键词或清除搜索条件。").FontSize(12).TextColor(ColorTextMuted).SingleLine()
			})
			return
		}

		cols := a.subscriptionGridCols(c)
		rowCount := (len(filtered) + cols - 1) / cols
		a.SubListState.Key = func(rowIdx int) any {
			start := rowIdx * cols
			if start >= 0 && start < len(filtered) {
				return "row:" + filtered[start].ID
			}
			return rowIdx
		}
		ui.List(c, &a.SubListState, rowCount, func(rowIdx int) {
			start := rowIdx * cols
			end := min(start+cols, len(filtered))
			ui.Row(c).
				FillWidth().
				Padding(10, 4).
				Gap(12).
				AlignItems(ui.Start).
				Justify(ui.Start).
				ClipX().
				Children(func() {
					for idx := start; idx < end; idx++ {
						s := filtered[idx]
						a.renderSubscriptionGridCell(c, s)
					}
					for pad := end - start; pad < cols; pad++ {
						ui.Box(c).Grow(1).Height(1)
					}
				})
		}).Label("订阅列表").Grow(1).FillWidth()
		a.refreshGridLive(c, filtered, cols)
	})
}

func (a *App) renderSubscriptionGridCell(c *ui.Context, s subscription.Subscription) {
	liveRoom := a.findSubscriptionLiveRoom(s)
	isLive := liveRoom != nil && liveRoom.LiveStatus == 1
	isRecording := liveRoom != nil && liveRoom.Recording

	ui.Column(c).
		Grow(1).
		Padding(8, 6).
		Radius(16).
		AlignItems(ui.Center).
		Gap(8).
		ClipX().
		Children(func() {
			// 92px circular avatar with an orange ring when live, and live/recording badge overlay
			ui.Box(c).Size(98, 98).Shrink(0).Children(func() {
				a.renderAvatar(c, s.Name, s.Mid, 92, isLive, "选择订阅 "+s.ID, func() {
					a.OpenSubscriptionDetail(s)
				}, s.Face)

				if isLive && liveRoom != nil {
					rm := *liveRoom
					badgeLabel := fmt.Sprintf("网格开始录制 %d", rm.RoomID)
					if isRecording {
						badgeLabel = fmt.Sprintf("网格停止录制 %d", rm.RoomID)
					}
					badgeBtn := ui.ButtonBase(c).
						Absolute().
						Bottom(0).
						Left(16).
						Right(16).
						Height(22).
						Radius(999).
						Label(badgeLabel).
						Background(ColorLive).
						Border(1.5, ColorWhite).
						Justify(ui.Center).
						AlignItems(ui.Center)
					badgeBtn.Children(func() {
						ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
							if isRecording {
								ui.Box(c).Size(7, 7).Radius(3.5).Background(ColorDotRed)
								ui.Text(c, "录制中").FontSize(10).FontWeight(600).TextColor(ColorWhite).SingleLine()
							} else {
								ui.Text(c, "直播").FontSize(10).FontWeight(600).TextColor(ColorWhite).SingleLine()
							}
						})
					})
					if badgeBtn.Clicked() {
						if isRecording {
							a.stopLiveRecording(rm.RoomID)
						} else {
							a.startLiveRecording(rm.RoomID)
						}
					}
				}
			})

			nameBtn := ui.ButtonBase(c).
				FillWidth().
				Padding(2, 4).
				Justify(ui.Center).
				AlignItems(ui.Center).
				ClipX()
			nameBtn.Children(func() {
				ui.Text(c, s.Name).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
			})
			if nameBtn.Clicked() {
				a.OpenSubscriptionDetail(s)
			}
		})
}

func (a *App) renderSubscriptionDetailView(c *ui.Context, sub *subscription.Subscription) {
	a.refreshLiveAccounts(c, subscriptionAuthors(*sub))
	ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
		ui.Column(c).FillWidth().Padding(16).Gap(14).ClipX().Children(func() {
			a.renderSubscriptionDetailHeader(c, sub)
			// The subscription's posts are on Home and in the library;
			// what it does automatically is its rule (DEC-040).
			if a.SubDetailTab == "accounts" {
				a.renderSubscriptionAccountsTab(c, sub)
			} else {
				a.renderSubscriptionLiveTab(c, sub)
			}
		})
	})
}

// renderSubscriptionDetailHeader draws the subscription card, its manage
// forms and the tabs.
func (a *App) renderSubscriptionDetailHeader(c *ui.Context, sub *subscription.Subscription) {
	// Header: avatar, name (+ note), the rule button and the "⋯" menu.
	ui.Column(c).
		FillWidth().
		Padding(4, 0).
		Gap(12).
		ClipX().
		Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(12).ClipX().Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(12).Grow(1).ClipX().Children(func() {
					a.renderAvatar(c, sub.Name, sub.Mid, 64, a.isSubscriptionLive(*sub), "", nil, sub.Face)
					ui.Column(c).Grow(1).Gap(4).ClipX().Children(func() {
						ui.Text(c, sub.Name).FontSize(18).FontWeight(600).TextColor(ColorTextMain).SingleLine()
						if strings.TrimSpace(sub.Note) != "" {
							ui.Text(c, sub.Note).FontSize(13).TextColor(ColorTextMuted).MaxLines(2)
						} else {
							ui.Text(c, fmt.Sprintf("关联 %d 个账号", max(1, len(sub.Accounts)))).FontSize(12).TextColor(ColorTextMuted).SingleLine()
						}
					})
				})

				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					a.renderSubscriptionRuleButton(c, sub)
					if a.pending["check-updates"] {
						if OutlinePillButton(c, "取消检查", "取消检查").Clicked() {
							a.backend.CancelSubscriptionCheck()
						}
					}
					open := a.SubManageMenuOpen
					MoreMenuButton(c, "订阅管理菜单", 32, &open, func(m *ui.Menu) {
						if m.Item("检查更新").Chosen() {
							a.checkSelectedSubscription()
						}
						if m.Item("改名").Chosen() {
							a.SubName = sub.Name
							a.SubManageModalMode = "rename"
						}
						if m.Item("备注").Chosen() {
							a.SubNote = sub.Note
							a.SubManageModalMode = "note"
						}
						if m.Item("更换头像").Chosen() {
							a.SubManageModalMode = "avatar"
						}
						m.Separator()
						if m.Item("删除订阅").Danger().Chosen() {
							a.SubDeleteID = sub.ID
							a.SubManageModalMode = "delete"
						}
					})
					a.SubManageMenuOpen = open
				})
			})

			switch a.SubManageModalMode {
			case "rename":
				ui.Row(c).FillWidth().Padding(10, 12).Radius(12).Background(ColorBgMain).Border(1, ColorBorder).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.TextInput(c, &a.SubName).Label("订阅名称").Placeholder("订阅名称（必填，≤64字）").Grow(1)
					if OutlinePillButton(c, "取消", "取消改名").Clicked() {
						a.SubManageModalMode = ""
					}
					if OutlinePillButton(c, "保存名称", "保存名称和备注").Clicked() {
						sub.Name = a.SubName
						_, err := a.backend.UpsertSubscription(*sub)
						a.showError(err)
						if err == nil {
							a.BannerInfo = "已更新订阅名称"
							a.SubManageModalMode = ""
						}
						a.RefreshData()
					}
				})
			case "note":
				ui.Row(c).FillWidth().Padding(10, 12).Radius(12).Background(ColorBgMain).Border(1, ColorBorder).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.TextInput(c, &a.SubNote).Label("订阅备注").Placeholder("备注说明（可选）").Grow(1)
					if OutlinePillButton(c, "取消", "取消修改备注").Clicked() {
						a.SubManageModalMode = ""
					}
					if OutlinePillButton(c, "保存备注", "保存名称和备注").Clicked() {
						sub.Note = a.SubNote
						_, err := a.backend.UpsertSubscription(*sub)
						a.showError(err)
						if err == nil {
							a.BannerInfo = "已更新订阅备注"
							a.SubManageModalMode = ""
						}
						a.RefreshData()
					}
				})
			case "avatar":
				ui.Column(c).FillWidth().Padding(10, 12).Radius(12).Background(ColorBgMain).Border(1, ColorBorder).Gap(8).Children(func() {
					ui.Text(c, "选择关联账号作为订阅头像").FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					ui.Row(c).Gap(8).Wrap().Children(func() {
						for _, accItem := range sub.Accounts {
							acc := accItem
							if OutlinePillButton(c, "使用 "+firstNonEmptyStrUI(acc.Name, "UID:"+acc.UID), "订阅头像："+acc.UID).Clicked() {
								sub.Avatar = subscription.AvatarRef{Site: acc.Site, UID: acc.UID}
								_, err := a.backend.UpsertSubscription(*sub)
								a.showError(err)
								a.SubManageModalMode = ""
								a.RefreshData()
							}
						}
						if OutlinePillButton(c, "关闭", "关闭更换头像").Clicked() {
							a.SubManageModalMode = ""
						}
					})
				})
			case "delete":
				if a.SubDeleteID == sub.ID {
					ui.Column(c).FillWidth().Padding(12).Radius(12).Background(ColorErrorBg).Border(1, ui.RGBA(180, 35, 24, 0.25)).Gap(8).Children(func() {
						ui.Text(c, fmt.Sprintf("删除订阅「%s」？只删除这个订阅和它的账号关联，不删除已缓存的内容，也不会在哔哩哔哩上取消关注。", sub.Name)).
							FontSize(13).TextColor(ColorError).MaxLines(2)
						ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
							if OutlinePillButton(c, "取消", "取消删除订阅").Clicked() {
								a.SubDeleteID = ""
								a.SubManageModalMode = ""
							}
							if DangerOutlinePillButton(c, "确认删除订阅", "确认删除订阅").Clicked() {
								a.showError(a.backend.DeleteSubscription(sub.ID))
								a.SubDeleteID = ""
								a.SubManageModalMode = ""
								a.SubDetailOpen = false
								a.SelectedSubID = ""
								a.SelectedSubMid = 0
								a.RefreshData()
							}
						})
					})
				}
			}
		})

	UnderlineTabs(c, a.SubDetailTab, []TabOption{
		{Key: "live", Text: "直播", AccessLabel: "订阅标签：直播"},
		{Key: "accounts", Text: "账号", AccessLabel: "订阅标签：账号"},
	}, func(key string) { a.SubDetailTab = key })
}

func (a *App) renderSubscriptionAccountsTab(c *ui.Context, sub *subscription.Subscription) {
	ui.Column(c).
		FillWidth().
		Padding(16).
		Radius(16).
		Background(ColorBgSecondary).
		Border(1, ColorBorder).
		Gap(10).
		ClipX().
		Children(func() {
			ui.Text(c, fmt.Sprintf("关联账号（%d 个）", len(sub.Accounts))).FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
			for _, accItem := range sub.Accounts {
				acc := accItem
				var uidNum int64
				_, _ = fmt.Sscanf(acc.UID, "%d", &uidNum)
				ui.Row(c).
					FillWidth().
					Padding(10, 12).
					Radius(12).
					Background(ColorBgMain).
					Border(1, ColorBorder).
					AlignItems(ui.Center).
					Justify(ui.SpaceBetween).
					Gap(8).
					ClipX().
					Children(func() {
						ui.Row(c).AlignItems(ui.Center).Gap(8).Grow(1).ClipX().Children(func() {
							a.renderAvatar(c, acc.Name, uidNum, 34, false, "", nil, acc.Face)
							ui.Box(c).Padding(1, 6).Radius(4).Background(ColorAccentBg).Children(func() {
								ui.Text(c, "哔哩哔哩").FontSize(11).FontWeight(600).TextColor(ColorAccent).SingleLine()
							})
							ui.Text(c, firstNonEmptyStrUI(acc.Name, "UID "+acc.UID)).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
							ui.Text(c, "UID:"+acc.UID).FontSize(12).TextColor(ColorTextMuted).SingleLine()
						})
						ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
							if OutlinePillButton(c, "打开主页", "订阅账号主页："+acc.UID).Clicked() {
								if mid, err := strconv.ParseInt(acc.UID, 10, 64); err == nil {
									a.OpenUser(mid, acc.Name)
								}
							}
							if OutlinePillButton(c, "用作头像", "订阅头像："+acc.UID).Clicked() {
								sub.Avatar = subscription.AvatarRef{Site: acc.Site, UID: acc.UID}
								_, err := a.backend.UpsertSubscription(*sub)
								a.showError(err)
								a.RefreshData()
							}
							if DangerOutlinePillButton(c, "解除关联", "解除账号："+acc.UID).Clicked() {
								_, err := a.backend.UnlinkSubscriptionAccount(sub.ID, acc.Site, acc.UID)
								a.showError(err)
								a.RefreshData()
							}
						})
					})
			}

			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).ClipX().Children(func() {
				ui.TextInput(c, &a.SubAccountInput).
					Label("关联账号输入").
					Placeholder("关联另一个账号：粘贴主页链接或 UID").
					Grow(1)
				if OutlinePillButton(c, "预览关联账号", "预览关联账号").Clicked() {
					a.previewSubAccount(sub.ID)
				}
			})

			if a.AccountPreview != nil && a.AccountPreviewSubID == sub.ID {
				acc := *a.AccountPreview
				var previewMid int64
				_, _ = fmt.Sscanf(acc.UID, "%d", &previewMid)
				ui.Column(c).
					FillWidth().
					Padding(10, 12).
					Radius(10).
					Background(ColorInfoBlueBg).
					Border(1, ColorInfoBlue).
					Gap(6).
					Children(func() {
						ui.Row(c).AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
							ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
								a.renderAvatar(c, acc.Name, previewMid, 32, false, "", nil, acc.Face)
								ui.Text(c, fmt.Sprintf("%s · UID:%s", acc.Name, acc.UID)).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
							})
							if AccentPillButton(c, "确认关联或迁移", "确认关联或迁移").Clicked() {
								var err error
								if a.AccountPreviewOwner != "" {
									_, err = a.backend.MoveSubscriptionAccount(acc.Site, acc.UID, sub.ID)
								} else {
									_, err = a.backend.LinkSubscriptionAccount(sub.ID, acc.Site, acc.UID, acc.Name, acc.Face)
								}
								a.showError(err)
								if err == nil {
									a.AccountPreview = nil
									a.SubAccountInput = ""
									a.RefreshData()
								}
							}
						})
						if a.AccountPreviewOwner != "" {
							ui.Text(c, fmt.Sprintf("该账号当前已属于订阅「%s」，确认后将迁移至当前订阅。", a.AccountPreviewOwner)).
								FontSize(12).TextColor(ColorInfoBlue).MaxLines(2)
						}
					})
			}
		})
}

func (a *App) findSubscriptionLiveRoom(sub subscription.Subscription) *live.RoomState {
	for i := range a.LiveRooms {
		rm := &a.LiveRooms[i]
		if sub.Mid > 0 && rm.UID == sub.Mid {
			return rm
		}
		for _, acc := range sub.Accounts {
			if strconv.FormatInt(rm.UID, 10) == acc.UID {
				return rm
			}
		}
	}
	return nil
}

func (a *App) isSubscriptionLive(sub subscription.Subscription) bool {
	rm := a.findSubscriptionLiveRoom(sub)
	return rm != nil && rm.LiveStatus == 1
}

// settingChoice is one option of a setting: the value kept and the name shown.
type settingChoice struct{ value, name string }

func numberChoices(values ...string) []settingChoice {
	out := make([]settingChoice, len(values))
	for i, v := range values {
		out[i] = settingChoice{v, v}
	}
	return out
}

// settingChoiceRow is a setting chosen from named options: the drop-down
// shows the names, never the stored values. note is the one line a setting
// that needs explaining has; most have none (docs/ui.md §3.7).
func settingChoiceRow(c *ui.Context, title, note, label string, value *string, options []settingChoice) bool {
	names := make([]string, len(options))
	shown := *value
	for i, o := range options {
		names[i] = o.name
		if o.value == *value {
			shown = o.name
		}
	}
	changed := false
	ui.Row(c).FillWidth().Padding(12, 0).BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(12).ClipX().Children(func() {
		ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
			ui.Text(c, title).FontSize(14).FontWeight(500).TextColor(ColorTextMain).SingleLine()
			if note != "" {
				ui.Text(c, note).FontSize(12).TextColor(ColorTextMuted).MaxLines(2)
			}
		})
		if ui.Select(c, &shown, names).Label(label).Changed() {
			for _, o := range options {
				if o.name == shown && o.value != *value {
					*value, changed = o.value, true
				}
			}
		}
	})
	return changed
}

func settingSwitchRow(c *ui.Context, title, desc, switchLabel string, value *bool) bool {
	changed := false
	ui.Row(c).
		FillWidth().
		Padding(12, 0).
		BorderWidth(0, 0, 1, 0).
		BorderColor(ColorBorder).
		AlignItems(ui.Center).
		Justify(ui.SpaceBetween).
		Gap(12).
		ClipX().
		Children(func() {
			ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
				ui.Text(c, title).FontSize(14).FontWeight(500).TextColor(ColorTextMain).SingleLine()
				if desc != "" {
					ui.Text(c, desc).FontSize(12).TextColor(ColorTextMuted).MaxLines(2)
				}
			})
			changed = ui.Switch(c, value).Label(switchLabel).Changed()
		})
	return changed
}

func (a *App) renderSubscriptionLiveTab(c *ui.Context, sub *subscription.Subscription) {
	ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
		ui.Text(c, "关联账号直播间").FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
		MoreMenuButton(c, "订阅直播更多", 30, nil, func(m *ui.Menu) {
			if m.Item("刷新直播状态").Chosen() {
				a.refreshVisibleLive()
			}
		})
	})

	authors := a.selectedSubscriptionAuthors()
	matchedCount := 0
	for _, rm := range a.LiveRooms {
		room := rm
		matches := false
		for _, id := range authors {
			if id == room.UID {
				matches = true
				break
			}
		}
		if !matches {
			continue
		}
		matchedCount++
		statusText := "未开播"
		badgeBg := ColorBgSecondary
		badgeFg := ColorTextMuted
		if room.LiveStatus == 1 {
			statusText = "直播中"
			badgeBg = ColorLive
			badgeFg = ColorWhite
		} else if room.RoomID == 0 {
			statusText = "未开通直播间"
		}
		qualSpec := bilibili.FormatLiveQualitySpec(room.QualityName, room.Quality, room.Codec)

		ui.Row(c).
			FillWidth().
			Padding(12, 14).
			Radius(14).
			Background(ColorBgSecondary).
			Border(1, ColorBorder).
			AlignItems(ui.Center).
			Justify(ui.SpaceBetween).
			Gap(12).
			ClipX().
			Children(func() {
				a.renderAvatar(c, room.Uname, room.UID, 42, room.LiveStatus == 1, "", nil)
				ui.Column(c).Grow(1).Gap(4).ClipX().Children(func() {
					ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
						ui.Box(c).Shrink(0).Padding(2, 8).Radius(999).Background(badgeBg).Children(func() {
							ui.Text(c, statusText).FontSize(11).FontWeight(600).TextColor(badgeFg).SingleLine()
						})
						ui.Text(c, room.Uname).FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
						if room.RoomID > 0 {
							ui.Text(c, fmt.Sprintf("房间号 %d", room.RoomID)).Shrink(0).FontSize(12).TextColor(ColorTextMuted).SingleLine()
						}
						if qualSpec != "" && (room.LiveStatus == 1 || room.Recording) {
							ui.Text(c, "· "+qualSpec).Shrink(0).FontSize(12).FontWeight(600).TextColor(ColorTextMain).SingleLine()
						}
					})
					if room.Title != "" {
						ui.Text(c, room.Title).FontSize(13).TextColor(ColorTextMain).SingleLine()
					}
					if room.Recording {
						recMeta := fmt.Sprintf("正在录制：%s · %s · %d 段",
							FormatDuration(int(room.RecordDurationSec)),
							FormatBytes(room.RecordBytes),
							room.RecordSegments)
						if qualSpec != "" {
							recMeta += " · " + qualSpec
						}
						ui.Text(c, recMeta).FontSize(12).FontWeight(600).TextColor(ColorLive).SingleLine()
					}
				})
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					if room.Recording {
						if DangerOutlinePillButton(c, "停止录制", fmt.Sprintf("停止录制 %d", room.RoomID)).Clicked() {
							a.stopLiveRecording(room.RoomID)
						}
					} else if room.LiveStatus == 1 {
						if AccentPillButton(c, "录制", fmt.Sprintf("开始录制 %d", room.RoomID)).Clicked() {
							a.startLiveRecording(room.RoomID)
						}
					}
					if room.RoomID > 0 {
						roomID := room.RoomID
						MoreMenuButton(c, fmt.Sprintf("直播间更多 %d", roomID), 28, nil, func(m *ui.Menu) {
							if m.Item("在浏览器中观看").Chosen() {
								_ = a.backend.OpenLiveRoomInBrowser(roomID)
							}
						})
					}
				})
			})
	}
	if matchedCount == 0 {
		ui.Text(c, "暂无直播间状态，正在直播时会在这里显示。").FontSize(13).TextColor(ColorTextMuted)
	}

	// Recorded live items belonging to this subscription's accounts (docs/ui.md §3.9, R4)
	var recordedLives []library.ItemMetadata
	for _, it := range a.LibraryItems {
		if it.Kind != "live" {
			continue
		}
		for _, id := range authors {
			if it.Author.Mid == id {
				recordedLives = append(recordedLives, it)
				break
			}
		}
	}
	if len(recordedLives) > 0 {
		ui.Column(c).FillWidth().Padding(8, 0, 0, 0).Gap(8).ClipX().Children(func() {
			ui.Text(c, fmt.Sprintf("已录制的直播录像（%d）", len(recordedLives))).FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
			for _, rec := range recordedLives {
				recItem := rec
				spec := formatLiveRecordingSpec(recItem.Live, "")
				segCount := 1
				if recItem.Live != nil && len(recItem.Live.Segments) > 0 {
					segCount = len(recItem.Live.Segments)
				}
				metaLine := fmt.Sprintf("时长 %s · %d 段", FormatDuration(recItem.Duration), segCount)
				if spec != "" {
					metaLine += " · " + spec
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
						body := ui.ButtonBase(c).Label("播放订阅录像：" + recItem.ItemKey).Grow(1).Padding(0).ClipX()
						body.Children(func() {
							ui.Column(c).Grow(1).Gap(3).ClipX().Children(func() {
								ui.Text(c, recItem.Title).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
								ui.Text(c, metaLine).FontSize(12).TextColor(ColorTextMuted).SingleLine()
							})
						})
						if body.Clicked() {
							a.OpenPlayerOverlay(recItem)
						}
						MoreMenuButton(c, "录像更多："+recItem.ItemKey, 28, nil, func(m *ui.Menu) {
							if m.Item("详情").Chosen() {
								a.OpenLibraryItemDetail(recItem)
							}
							if m.Item("在资源管理器中显示").Chosen() {
								a.showError(a.backend.OpenItemFolder(recItem.ItemKey))
							}
						})
					})
			}
		})
	}
}

func (a *App) renderAddSubOverlay(c *ui.Context, w, h float32) {
	if c.Shortcut(0, ui.KeyEscape) {
		a.AddSubDialogOpen = false
		return
	}
	ui.Overlay(c, func() {
		ui.Column(c).
			Absolute().
			Top(0).
			Left(0).
			Size(w, h).
			Background(ui.RGBA(15, 20, 25, 0.55)).
			AlignItems(ui.Center).
			Justify(ui.Center).
			Children(func() {
				ui.Column(c).
					Label("添加订阅对话框").
					Width(460).
					Padding(22).
					Radius(16).
					Background(ColorBgMain).
					Border(1, ColorBorder).
					Gap(12).
					ClipX().
					Children(func() {
						ui.Text(c, "添加订阅").FontSize(18).FontWeight(600).TextColor(ColorTextMain).SingleLine()
						ui.Text(c, "建立一个订阅并可选关联哔哩哔哩账号（主页链接或 UID）。").FontSize(13).TextColor(ColorTextMuted).MaxLines(2)

						ui.TextInput(c, &a.AddSubName).
							Label("新订阅名称").
							Placeholder("订阅名称（必填，≤ 64 字）").
							FillWidth()

						ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
							ui.TextInput(c, &a.AddSubInput).
								Label("添加订阅UID输入框").
								Placeholder("关联账号：粘贴主页链接或 UID（可选）").
								Grow(1)
							if OutlinePillButton(c, "预览账号", "预览新订阅账号").Clicked() && strings.TrimSpace(a.AddSubInput) != "" {
								a.previewNewAccount()
							}
						})

						if a.AddSubPreview != nil {
							acc := *a.AddSubPreview
							var previewMid int64
							_, _ = fmt.Sscanf(acc.UID, "%d", &previewMid)
							ui.Column(c).FillWidth().Padding(10, 12).Radius(12).Background(ColorBgSecondary).Border(1, ColorBorder).Gap(6).Children(func() {
								ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
									a.renderAvatar(c, acc.Name, previewMid, 32, false, "", nil)
									ui.Text(c, fmt.Sprintf("%s · UID: %s", acc.Name, acc.UID)).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
								})
								if a.AddSubPreviewOwner != nil {
									ui.Text(c, fmt.Sprintf("该账号已属于订阅「%s」，可点击下方按钮移到新订阅。", a.AddSubPreviewOwner.Name)).
										FontSize(12).TextColor(ColorDotYellow).MaxLines(2)
									if AccentOutlinePillButton(c, "创建并移到新订阅", "创建并移到新订阅").Clicked() {
										name := strings.TrimSpace(a.AddSubName)
										if name == "" {
											name = acc.Name
										}
										sub, err := a.backend.CreateSubscription(name, "", nil, subscription.SubRules{ScheduledCheck: true})
										a.showError(err)
										if err == nil {
											sub, err = a.backend.MoveSubscriptionAccount(acc.Site, acc.UID, sub.ID)
											a.showError(err)
											a.syncSelectedSubRules(sub)
											a.AddSubInput = ""
											a.AddSubName = ""
											a.AddSubPreview = nil
											a.AddSubPreviewOwner = nil
											a.AddSubDialogOpen = false
											a.RefreshData()
										}
									}
								}
							})
						}

						ui.Row(c).FillWidth().Justify(ui.End).Gap(8).Children(func() {
							if OutlinePillButton(c, "取消", "取消添加订阅").Clicked() {
								a.AddSubPreview = nil
								a.AddSubPreviewOwner = nil
								a.AddSubDialogOpen = false
							}
							createText := "确认添加"
							if a.AddSubPreviewOwner != nil {
								createText = "创建并移入账号"
							}
							if AccentPillButton(c, createText, "确认添加订阅").Clicked() {
								a.createPreviewedSubscription()
							}
						})
					})
			})
	})
}

func (a *App) renderImportSubOverlay(c *ui.Context, w, h float32) {
	if c.Shortcut(0, ui.KeyEscape) {
		a.ImportSubModalOpen = false
		return
	}
	ui.Overlay(c, func() {
		ui.Column(c).
			Absolute().
			Top(0).
			Left(0).
			Size(w, h).
			Background(ui.RGBA(15, 20, 25, 0.55)).
			AlignItems(ui.Center).
			Justify(ui.Center).
			Children(func() {
				ui.Column(c).
					Label("关注导入对话框").
					Width(520).
					Height(h-90).
					Padding(20).
					Radius(16).
					Background(ColorBgMain).
					Border(1, ColorBorder).
					Gap(10).
					ClipX().
					Children(func() {
						ui.Text(c, "从关注列表导入").FontSize(18).FontWeight(600).TextColor(ColorTextMain).SingleLine()
						ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
							groups := []string{"全部关注"}
							for _, group := range a.ImportGroups {
								groups = append(groups, group.Name)
							}
							if ui.Select(c, &a.ImportGroup, groups).Label("关注分组").Changed() {
								a.loadImportPage(true)
							}
							ui.TextInput(c, &a.ImportSearch).
								Placeholder("搜索已加载的关注昵称或签名…").
								Label("关注导入搜索框").
								Grow(1)
						})

						if a.ImportError != "" {
							ui.Row(c).FillWidth().Padding(8, 12).Radius(8).Background(ColorErrorBg).AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
								ui.Text(c, a.ImportError).FontSize(12).TextColor(ColorError).MaxLines(2).Grow(1)
								if OutlinePillButton(c, "重试", "重试关注列表").Clicked() {
									a.loadImportPage(a.ImportPage == 0)
								}
							})
						}

						kw := strings.ToLower(strings.TrimSpace(a.ImportSearch))
						ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
							ui.Column(c).FillWidth().Gap(6).Children(func() {
								for _, user := range a.ImportFollowings {
									u := user
									if kw != "" && !strings.Contains(strings.ToLower(u.Uname), kw) && !strings.Contains(strings.ToLower(u.Sign), kw) {
										continue
									}
									ownerSub, owned := a.backend.OwnerOfAccount("bilibili", strconv.FormatInt(u.Mid, 10))
									selected := a.ImportSelectedMids[u.Mid]
									ui.Row(c).
										FillWidth().
										Padding(8, 10).
										Radius(10).
										Background(ColorBgSecondary).
										AlignItems(ui.Center).
										Gap(10).
										ClipX().
										Children(func() {
											a.renderAvatar(c, u.Uname, u.Mid, 34, false, "", nil)
											ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
												ui.Text(c, u.Uname).FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
												if u.Sign != "" {
													ui.Text(c, u.Sign).FontSize(12).TextColor(ColorTextMuted).SingleLine()
												}
											})
											if owned {
												ui.Text(c, fmt.Sprintf("已在订阅「%s」中", ownerSub.Name)).FontSize(12).TextColor(ColorTextMuted).SingleLine()
											} else if ui.Switch(c, &selected).Label(fmt.Sprintf("勾选导入 %d", u.Mid)).Changed() {
												a.ImportSelectedMids[u.Mid] = selected
											}
										})
								}
								if a.ImportHasMore {
									if OutlinePillButton(c, "加载下一页关注", "加载下一页关注").Clicked() {
										a.loadImportPage(false)
									}
								}
							})
						})

						selectedCount := 0
						for _, v := range a.ImportSelectedMids {
							if v {
								selectedCount++
							}
						}
						ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
							ui.Text(c, fmt.Sprintf("已选 %d 个", selectedCount)).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
							ui.Row(c).Gap(8).Children(func() {
								if OutlinePillButton(c, "取消", "取消导入关注").Clicked() {
									a.ImportSubModalOpen = false
								}
								if OutlinePillButton(c, "全选已加载", "全选已加载").Clicked() {
									for _, u := range a.ImportFollowings {
										if _, owned := a.backend.OwnerOfAccount("bilibili", strconv.FormatInt(u.Mid, 10)); !owned {
											a.ImportSelectedMids[u.Mid] = true
										}
									}
								}
								if AccentPillButton(c, fmt.Sprintf("导入 %d 个订阅", selectedCount), "确认导入关注").Clicked() {
									var users []bilibili.FollowingUser
									for _, u := range a.ImportFollowings {
										if a.ImportSelectedMids[u.Mid] {
											users = append(users, u)
										}
									}
									n, err := a.backend.ImportCreators(users, "both", "inherit", false, false)
									a.showError(err)
									if err == nil {
										a.BannerInfo = fmt.Sprintf("已建立 %d 个订阅", n)
										a.ImportSubModalOpen = false
										a.RefreshData()
									}
								}
							})
						})
					})
			})
	})
}

func firstNonEmptyStrUI(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
