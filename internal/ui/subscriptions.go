package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

func (a *App) subscriptionGridCols(c *ui.Context) int {
	winW, _ := c.Size()
	if winW <= 0 {
		winW = 1280
	}
	availW := max(winW-railWidth-a.contextPanelWidth(winW)-32, 320)
	cols := int(availW / 160)
	return max(3, min(5, cols))
}

func (a *App) filteredSubscriptions(query string) []subscription.Subscription {
	sig := filteredSubsSig{
		subsRev: a.subsRev,
		subsN:   len(a.Subscriptions),
		search:  query,
		hash:    a.subscriptionsAccountsFingerprint(),
	}
	st := a.fs()
	if st.filteredSubs != nil && st.filteredSubsSig == sig {
		return st.filteredSubs
	}
	filtered := make([]subscription.Subscription, 0, len(a.Subscriptions))
	for _, s := range a.Subscriptions {
		if query != "" {
			matched := strings.Contains(strings.ToLower(s.Name), query) ||
				strings.Contains(strings.ToLower(s.Uname), query) ||
				strings.Contains(strings.ToLower(s.Note), query)
			for _, acc := range s.Accounts {
				if strings.Contains(strings.ToLower(acc.Name), query) ||
					strings.Contains(strings.ToLower(acc.UID), query) ||
					strings.Contains(strings.ToLower(acc.UniqueID), query) {
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
	st.filteredSubsSig = sig
	st.filteredSubs = filtered
	return filtered
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
	if a.SubAccountsOpen {
		a.renderSubscriptionAccountsView(c)
		return
	}

	ui.Column(c).Grow(1).FillWidth().Padding(16).Gap(14).ClipX().Children(func() {
		st := a.fs()
		// Search filter bar (docs/ui.md §3.9)
		ui.Row(c).
			FillWidth().
			Padding(4, 12).
			Radius(999).
			Background(ColorBgMain).
			Border(1, ColorBorder).
			AlignItems(ui.Center).
			Gap(8).
			ClipX().
			Children(func() {
				DrawIcon(c, "search", 16, ColorTextMuted, 1.75)
				inp := ui.TextInputBase(c, &a.SubSearchQuery).
					Placeholder("搜索订阅名称、备注或关联账号昵称…").
					Label("订阅搜索过滤框").
					Padding(6, 4).
					Grow(1)
				if !inp.Composing() {
					st.subSearchApplied = a.SubSearchQuery
				}
				if a.SubSearchQuery != "" {
					if OutlinePillButton(c, "清除", "清除订阅搜索").Clicked() {
						a.SubSearchQuery = ""
						st.subSearchApplied = ""
					}
				}
				// Without the panel, 全部账号 is reached from here.
				if !a.panelShown && len(a.Subscriptions) > 0 {
					if OutlinePillButton(c, "全部账号", "订阅页全部账号").Clicked() {
						a.openSubscriptionAccounts()
					}
				}
			})

		if len(a.Subscriptions) == 0 {
			ui.Column(c).FillWidth().Padding(36, 16).Radius(16).Background(ColorBgMain).Border(1, ColorBorder).AlignItems(ui.Center).Gap(10).Children(func() {
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

		query := strings.ToLower(strings.TrimSpace(st.subSearchApplied))
		if query != a.subListQuery {
			a.SubListState.ScrollTo(0, ui.Start)
			a.subListQuery = query
		}
		filtered := a.filteredSubscriptions(query)

		if len(filtered) == 0 {
			ui.Column(c).FillWidth().Padding(28, 16).Radius(16).Background(ColorBgMain).Border(1, ColorBorder).AlignItems(ui.Center).Gap(6).Children(func() {
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

func renderSubGridLiveBadge(c *ui.Context, label string, recording bool, onClick func()) {
	badgeBtn := ui.ButtonBase(c).
		Absolute().
		Bottom(0).
		Left(16).
		Right(16).
		Height(22).
		Radius(999).
		Label(label).
		Background(ColorLive).
		Border(1.5, ColorWhite).
		Justify(ui.Center).
		AlignItems(ui.Center)
	badgeBtn.Children(func() {
		ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
			if recording {
				ui.Box(c).Size(7, 7).Radius(3.5).Background(ColorDotRed)
				ui.Text(c, "录制中").FontSize(10).FontWeight(600).TextColor(ColorWhite).SingleLine()
			} else {
				ui.Text(c, "直播").FontSize(10).FontWeight(600).TextColor(ColorWhite).SingleLine()
			}
		})
	})
	if badgeBtn.Clicked() && onClick != nil {
		onClick()
	}
}

func (a *App) renderSubscriptionGridCell(c *ui.Context, s subscription.Subscription) {
	liveRoom := a.findSubscriptionLiveRoom(s)
	liveAcct := a.findSubscriptionLiveAccount(s)
	roomActive := liveRoom != nil && (liveRoom.LiveStatus == 1 || liveRoom.Recording)
	acctActive := liveAcct != nil && (liveAcct.LiveStatus == 1 || liveAcct.Recording)
	isLive := roomActive || acctActive

	// Subscription grid cell: 92px circular avatar + name only; NEVER render a site badge here (docs/ui.md §3.4, §3.9).
	ui.Column(c).
		Grow(1).
		Padding(8, 6).
		Radius(16).
		AlignItems(ui.Center).
		Gap(8).
		ClipX().
		Children(func() {
			ui.Box(c).Size(98, 98).Shrink(0).Children(func() {
				pSite, pUID := subscriptionPrimarySiteAndUID(s)
				a.renderAccountAvatar(c, pSite, s.Name, pUID, s.Mid, 92, isLive, "选择订阅 "+s.ID, func() {
					a.OpenSubscriptionDetail(s)
				}, s.Face)

				// Prefer whichever active account is recording, then whichever is live.
				useRoom := false
				if roomActive && acctActive {
					useRoom = liveRoom.Recording || !liveAcct.Recording
				} else {
					useRoom = roomActive
				}
				if useRoom {
					rm := *liveRoom
					badgeLabel := fmt.Sprintf("网格开始录制 %d", rm.RoomID)
					if rm.Recording {
						badgeLabel = fmt.Sprintf("网格停止录制 %d", rm.RoomID)
					}
					renderSubGridLiveBadge(c, badgeLabel, rm.Recording, func() {
						if rm.Recording {
							a.stopLiveRecording(rm.RoomID)
						} else {
							a.startLiveRecording(rm.RoomID)
						}
					})
				} else if acctActive {
					st := *liveAcct
					badgeLabel := "网格开始录制 " + st.AccountKey
					if st.Recording {
						badgeLabel = "网格停止录制 " + st.AccountKey
					}
					renderSubGridLiveBadge(c, badgeLabel, st.Recording, func() {
						if st.Recording {
							a.stopAccountLiveRecording(firstNonEmptyStrUI(st.RecordingKey, st.AccountKey))
						} else {
							a.startAccountLiveRecording(st.Site, st.UID, firstNonEmptyStrUI(st.WebRID, st.RoomID))
						}
					})
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

func (a *App) refreshSingleAccountLive(siteID, uid string) {
	normSite := normalizeSiteIDUI(siteID)
	uid = strings.TrimSpace(uid)
	if uid == "" || a.backend == nil || normSite == "weibo" {
		return
	}
	backend := a.backend
	if normSite == "bilibili" {
		if mid, err := strconv.ParseInt(uid, 10, 64); err == nil && mid > 0 {
			a.visibleLiveStopped = false
			requestGuarded(a, "visible-live:"+normSite+":"+uid, func() bool {
				return a.CurrentRoute == RouteSubscriptions && backend.StatusOf("bilibili").LoggedIn
			}, func(ctx context.Context) ([]live.RoomState, error) {
				return backend.LiveStatus(ctx, []int64{mid})
			}, a.mergeLiveRooms, a.handleVisibleLiveError)
			return
		}
	}
	key := normSite + ":" + uid
	requestGuarded(a, "visible-live:"+key, func() bool {
		return a.CurrentRoute == RouteSubscriptions && backend.StatusOf(normSite).LoggedIn
	}, func(ctx context.Context) ([]service.LiveAccountState, error) {
		return backend.LiveStatuses(ctx, []string{key})
	}, func(states []service.LiveAccountState) {
		a.LiveRooms = bilibiliLiveRooms(backend.ListLiveRooms())
		a.mergeLiveAccountStates(states)
	}, a.showError)
}

// subscriptionForAuthor finds the subscription that owns (siteID, uid) or mid in O(1).
func (a *App) subscriptionForAuthor(siteID, uid string, mid int64) (subscription.Subscription, bool) {
	return a.ownerOfAccountInMemory(siteID, uid, mid)
}

func roomMatchesSubscription(sub subscription.Subscription, rm *live.RoomState) bool {
	if sub.Mid > 0 && rm.UID == sub.Mid {
		return true
	}
	uidStr := strconv.FormatInt(rm.UID, 10)
	for _, acc := range sub.Accounts {
		if normalizeSiteIDUI(acc.Site) == "bilibili" && strings.TrimSpace(acc.UID) == uidStr {
			return true
		}
	}
	return false
}

// findSubscriptionLiveRoom finds the best matching Bilibili room for sub,
// prioritizing recording rooms, then live rooms, then any matching room.
func (a *App) findSubscriptionLiveRoom(sub subscription.Subscription) *live.RoomState {
	var liveMatch, anyMatch *live.RoomState
	for i := range a.LiveRooms {
		rm := &a.LiveRooms[i]
		if !roomMatchesSubscription(sub, rm) {
			continue
		}
		if rm.Recording {
			return rm
		}
		if rm.LiveStatus == 1 && liveMatch == nil {
			liveMatch = rm
		}
		if anyMatch == nil {
			anyMatch = rm
		}
	}
	if liveMatch != nil {
		return liveMatch
	}
	return anyMatch
}

// findSubscriptionLiveAccount finds the best active (recording or live) account
// for sub, prioritizing recording accounts over merely live accounts.
func (a *App) findSubscriptionLiveAccount(sub subscription.Subscription) *service.LiveAccountState {
	var liveMatch *service.LiveAccountState
	for i := range a.LiveAccounts {
		st := &a.LiveAccounts[i]
		if st.LiveStatus != 1 && !st.Recording {
			continue
		}
		matched := false
		for _, acc := range sub.Accounts {
			if strings.EqualFold(st.Site, acc.Site) && st.UID == acc.UID {
				matched = true
				break
			}
		}
		if !matched && len(sub.Accounts) == 0 && strings.EqualFold(st.Site, sub.Site) && st.UID != "" && st.UID == subscriptionPrimaryUID(sub) {
			matched = true
		}
		if !matched {
			continue
		}
		if st.Recording {
			return st
		}
		if liveMatch == nil {
			liveMatch = st
		}
	}
	return liveMatch
}

func (a *App) isSubscriptionLive(sub subscription.Subscription) bool {
	if rm := a.findSubscriptionLiveRoom(sub); rm != nil && (rm.LiveStatus == 1 || rm.Recording) {
		return true
	}
	if st := a.findSubscriptionLiveAccount(sub); st != nil && (st.LiveStatus == 1 || st.Recording) {
		return true
	}
	return false
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

// settingChoiceRow is a setting chosen from named options (enabled wrapper).
func settingChoiceRow(c *ui.Context, title, note, label string, value *string, options []settingChoice) bool {
	return settingChoiceRowDisabled(c, title, note, label, value, options, false)
}

func settingSwitchRow(c *ui.Context, title, desc, switchLabel string, value *bool) bool {
	return settingSwitchRowDisabled(c, title, desc, switchLabel, value, false)
}

func firstNonEmptyStrUI(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
