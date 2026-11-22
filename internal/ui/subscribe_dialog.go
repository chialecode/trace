package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

// SubscribePickerState is the dialog that subscribes to an account
// (DEC-058) or reassigns an already-subscribed account to another
// subscription ("更改关联订阅"): it joins/moves into an existing
// subscription found by search, or starts a new one.
// When FromSubID is empty, it runs in add mode (the user pages' 订阅,
// 我的 › 加入订阅 and the previewed account in 添加订阅).
// When FromSubID is non-empty, it runs in move mode (订阅详情 › 账号 and
// 订阅 › 全部账号).
type SubscribePickerState struct {
	Open            bool
	Account         subscription.AccountRef
	FromSubID       string
	FromSubName     string
	SearchInput     string
	CommittedQuery  string
	SearchComposing bool
	ListState       ui.ListState
	Error           string
}

func (s SubscribePickerState) isMoveMode() bool {
	return strings.TrimSpace(s.FromSubID) != ""
}

// OpenSubscribePicker opens the dialog for an account not in any
// subscription yet.
func (a *App) OpenSubscribePicker(acct subscription.AccountRef) {
	if subs, err := a.backend.ListSubscriptions(); err == nil {
		a.Subscriptions = subs
		a.subsRev++
	}
	acct.Site = normalizeSiteIDUI(acct.Site)
	acct.Name = firstNonEmptyStrUI(acct.Name, acct.UniqueID, acct.UID)
	a.SubscribePicker = SubscribePickerState{Open: true, Account: acct}
}

// OpenMoveAccountPicker opens the dialog in move mode to reassign an
// already-subscribed account from fromSub to another existing subscription
// or a newly created one without manually unlinking first.
func (a *App) OpenMoveAccountPicker(fromSub subscription.Subscription, acct subscription.AccountRef) {
	if subs, err := a.backend.ListSubscriptions(); err == nil {
		a.Subscriptions = subs
		a.subsRev++
	}
	acct.Site = normalizeSiteIDUI(acct.Site)
	acct.Name = firstNonEmptyStrUI(acct.Name, acct.UniqueID, acct.UID)
	fromName := firstNonEmptyStrUI(fromSub.Name, fromSub.Uname, fromSub.ID)
	a.SubscribePicker = SubscribePickerState{
		Open:        true,
		Account:     acct,
		FromSubID:   fromSub.ID,
		FromSubName: fromName,
	}
}

// closeSubscribePicker hides the dialog. Its list state stays as it is: the
// list may have been built in this very frame; opening resets it.
func (a *App) closeSubscribePicker() {
	a.SubscribePicker.Open = false
	a.SubscribePicker.Error = ""
	a.SubscribePicker.SearchComposing = false
}

// refreshSelectedSubscriptionFeed immediately reloads the open subscription
// detail content feed from local SQLite (0 network calls) when the current
// subscription's linked accounts may have changed.
func (a *App) refreshSelectedSubscriptionFeed() {
	if !a.SubDetailOpen || a.SelectedSubID == "" {
		return
	}
	for i := range a.Subscriptions {
		if a.Subscriptions[i].ID == a.SelectedSubID {
			sub := a.Subscriptions[i]
			a.SelectedSubMid = sub.Mid
			a.ensureSubscriptionFeedLoaded(&sub)
			return
		}
	}
}

// subscribeAsNew starts a subscription named after the account (or atomically
// moves the account into a newly created subscription in move mode).
func (a *App) subscribeAsNew() {
	st := &a.SubscribePicker
	acct := st.Account
	subName := clampRunes(firstNonEmptyStrUI(acct.Name, acct.UniqueID, acct.UID), 64)
	if st.isMoveMode() {
		if _, err := a.backend.MoveSubscriptionAccountToNew(acct.Site, acct.UID, subName, "", subscription.DefaultSubRules()); err != nil {
			a.SubscribePicker.Error = userErrorText(err)
			return
		}
		a.closeSubscribePicker()
		a.BannerInfo = "已移到新建订阅：" + subName
		a.RefreshData()
		a.refreshSelectedSubscriptionFeed()
		return
	}
	if _, err := a.backend.CreateSubscription(subName, "", []subscription.AccountRef{acct}, subscription.DefaultSubRules()); err != nil {
		a.SubscribePicker.Error = userErrorText(err)
		return
	}
	a.closeSubscribePicker()
	a.BannerInfo = "已新建订阅：" + subName
	a.RefreshData()
}

// subscribeInto adds the account to an existing subscription (or atomically
// moves the account to sub in move mode).
func (a *App) subscribeInto(sub subscription.Subscription) {
	st := &a.SubscribePicker
	acct := st.Account
	targetName := firstNonEmptyStrUI(sub.Name, sub.Uname, sub.ID)
	if st.isMoveMode() {
		if _, err := a.backend.MoveSubscriptionAccount(acct.Site, acct.UID, sub.ID); err != nil {
			a.SubscribePicker.Error = userErrorText(err)
			return
		}
		a.closeSubscribePicker()
		a.BannerInfo = fmt.Sprintf("已把 %s 移到订阅“%s”", acct.Name, targetName)
		a.RefreshData()
		a.refreshSelectedSubscriptionFeed()
		return
	}
	if _, err := a.backend.LinkSubscriptionAccountRef(sub.ID, acct); err != nil {
		a.SubscribePicker.Error = userErrorText(err)
		return
	}
	a.closeSubscribePicker()
	a.BannerInfo = fmt.Sprintf("已把 %s 加入订阅“%s”", acct.Name, targetName)
	a.RefreshData()
}

// subscriptionAccountsSummary is a short line of a subscription's accounts,
// such as "B 站 2 · 抖音 1".
func subscriptionAccountsSummary(sub subscription.Subscription) string {
	counts := map[string]int{}
	for _, acc := range sub.Accounts {
		counts[normalizeSiteIDUI(acc.Site)]++
	}
	var parts []string
	if n := counts["bilibili"]; n > 0 {
		parts = append(parts, "B 站 "+strconv.Itoa(n))
	}
	if n := counts["douyin"]; n > 0 {
		parts = append(parts, "抖音 "+strconv.Itoa(n))
	}
	if n := counts["weibo"]; n > 0 {
		parts = append(parts, "微博 "+strconv.Itoa(n))
	}
	if n := counts["x"]; n > 0 {
		parts = append(parts, "X "+strconv.Itoa(n))
	}
	if len(parts) == 0 {
		return "没有关联账号"
	}
	return strings.Join(parts, " · ")
}

func (a *App) renderSubscribePickerOverlay(c *ui.Context, w, h float32) {
	if !a.SubscribePicker.Open {
		return
	}
	cardW := min(float32(520), max(float32(320), w-40))
	cardH := min(float32(560), max(float32(320), h-80))
	backdropLabel := "关闭加入订阅对话框（点击空白处）"
	panelLabel := "加入订阅对话框"
	if a.SubscribePicker.isMoveMode() {
		backdropLabel = "关闭更改关联订阅对话框（点击空白处）"
		panelLabel = "更改关联订阅对话框"
	}
	renderDialog(c, &a.SubscribePicker.Open,
		backdropLabel, panelLabel,
		cardW, cardH, a.closeSubscribePicker,
		func() {
			a.renderSubscribePickerBody(c)
		})
}

func (a *App) renderSubscribePickerBody(c *ui.Context) {
	st := &a.SubscribePicker
	acct := st.Account
	moveMode := st.isMoveMode()
	var mid int64
	if acct.Site == "bilibili" {
		mid, _ = strconv.ParseInt(acct.UID, 10, 64)
	}

	title := "订阅 " + acct.Name
	siteBadgeLabel := "加入订阅账号站点"
	subtitle := "加入一个现有订阅（同一个人的多个账号放在一起），或者新建一个订阅。"
	newLabel := "新建订阅“" + acct.Name + "”"
	newBtnLabel := "新建订阅"
	sectionTitle := "或加入现有订阅"
	searchLabel := "加入订阅搜索框"
	clearLabel := "清空加入订阅搜索"
	emptyAllText := "还没有订阅，可以新建一个"
	rowLabelPrefix := "加入现有订阅："
	rowActionText := "加入"
	listLabel := "加入订阅列表"
	errLabel := "加入订阅错误"
	cancelLabel := "取消加入订阅"

	if moveMode {
		title = "更改关联：" + acct.Name
		siteBadgeLabel = "更改关联账号站点"
		subtitle = fmt.Sprintf("当前属于订阅“%s”。选择另一个订阅，或移到新建订阅（保留已缓存内容与检查起点）。", st.FromSubName)
		newLabel = "移到新建订阅“" + acct.Name + "”"
		newBtnLabel = "移到新建订阅"
		sectionTitle = "或移到其他订阅"
		searchLabel = "更改关联搜索框"
		clearLabel = "清空更改关联搜索"
		emptyAllText = "还没有其他订阅，可以移到新建订阅"
		rowLabelPrefix = "移到现有订阅："
		rowActionText = "移入"
		listLabel = "更改关联订阅列表"
		errLabel = "更改关联订阅错误"
		cancelLabel = "取消更改关联订阅"
	}

	ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(10).ClipX().Children(func() {
		a.renderAccountAvatar(c, acct.Site, acct.Name, acct.UID, mid, 40, false, "", nil, acct.Face)
		ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
				ui.Text(c, title).FontSize(17).FontWeight(600).TextColor(ColorTextMain).SingleLine().Shrink(1)
				renderSiteSmallBadgeWithDot(c, acct.Site, siteBadgeLabel)
			})
			ui.Text(c, subtitle).FontSize(12).TextColor(ColorTextMuted).MaxLines(3)
		})
	})

	if AccentPillButton(c, newLabel, newBtnLabel).Clicked() {
		a.subscribeAsNew()
	}

	ui.Text(c, sectionTitle).FontSize(12).FontWeight(600).TextColor(ColorTextMuted).SingleLine()
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		inp := ui.TextInput(c, &st.SearchInput).
			Placeholder("搜索订阅名称、备注或账号昵称…").
			Label(searchLabel).
			Grow(1)
		st.SearchComposing = inp.Composing()
		if !st.SearchComposing && st.CommittedQuery != st.SearchInput {
			st.CommittedQuery = st.SearchInput
			st.ListState = ui.ListState{}
		}
		if st.SearchInput != "" && OutlinePillButton(c, "清空", clearLabel).Clicked() {
			st.SearchInput, st.CommittedQuery, st.SearchComposing = "", "", false
			st.ListState = ui.ListState{}
		}
	})

	rawFiltered := a.filteredSubscriptions(strings.ToLower(strings.TrimSpace(st.CommittedQuery)))
	filtered := rawFiltered
	totalCandidates := len(a.Subscriptions)
	if moveMode {
		totalCandidates = 0
		for _, s := range a.Subscriptions {
			if s.ID != st.FromSubID {
				totalCandidates++
			}
		}
		filtered = make([]subscription.Subscription, 0, len(rawFiltered))
		for _, s := range rawFiltered {
			if s.ID != st.FromSubID {
				filtered = append(filtered, s)
			}
		}
	}

	ui.Column(c).Grow(1).FillWidth().Border(1, ColorBorder).Radius(12).Background(ColorBgMain).Clip().Children(func() {
		switch {
		case totalCandidates == 0:
			ui.Column(c).Grow(1).FillWidth().Center().Padding(24).Children(func() {
				ui.Text(c, emptyAllText).FontSize(13).TextColor(ColorTextMuted).SingleLine()
			})
		case len(filtered) == 0:
			ui.Column(c).Grow(1).FillWidth().Center().Padding(24).Children(func() {
				ui.Text(c, "没有匹配的订阅").FontSize(13).TextColor(ColorTextMuted).SingleLine()
			})
		default:
			st.ListState.Key = func(i int) any {
				if i < 0 || i >= len(filtered) {
					return i
				}
				return filtered[i].ID
			}
			ui.List(c, &st.ListState, len(filtered), func(i int) {
				if i < 0 || i >= len(filtered) {
					return
				}
				sub := filtered[i]
				name := firstNonEmptyStrUI(sub.Name, sub.Uname, sub.ID)
				row := ui.ButtonBase(c).
					Label(rowLabelPrefix+sub.ID).
					FillWidth().
					Padding(9, 12).
					Gap(10).
					AlignItems(ui.Center).
					Justify(ui.Start).
					BorderWidth(0, 0, 1, 0).
					BorderColor(ColorBorder).
					ClipX()
				if row.Hovered() {
					row.Background(ColorHover)
				}
				row.Children(func() {
					pSite, pUID := subscriptionPrimarySiteAndUID(sub)
					a.renderAccountAvatar(c, pSite, name, pUID, sub.Mid, 32, false, "", nil, sub.Face)
					ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
						ui.Text(c, name).FontSize(14).FontWeight(500).TextColor(ColorTextMain).SingleLine()
						line := subscriptionAccountsSummary(sub)
						if note := strings.TrimSpace(sub.Note); note != "" {
							line += " · " + note
						}
						ui.Text(c, line).FontSize(12).TextColor(ColorTextMuted).SingleLine()
					})
					ui.Text(c, rowActionText).FontSize(13).FontWeight(600).TextColor(ColorAccent).SingleLine().Shrink(0)
				})
				if row.Clicked() {
					a.subscribeInto(sub)
				}
			}).Label(listLabel).Grow(1).FillWidth()
		}
	})

	if st.Error != "" {
		renderErrorBanner(c, errLabel, st.Error, nil)
	}
	ui.Row(c).FillWidth().Justify(ui.End).Children(func() {
		if OutlinePillButton(c, "取消", cancelLabel).Clicked() {
			a.closeSubscribePicker()
		}
	})
}
