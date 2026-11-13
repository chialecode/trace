package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

// SubscribePickerState is the dialog that subscribes to an account
// (DEC-058): it joins an existing subscription, found by search, or starts
// a new one. The user pages' 订阅, 我的 › 加入订阅 and the previewed account
// in 添加订阅 open it.
type SubscribePickerState struct {
	Open            bool
	Account         subscription.AccountRef
	SearchInput     string
	CommittedQuery  string
	SearchComposing bool
	ListState       ui.ListState
	Error           string
}

// OpenSubscribePicker opens the dialog for an account not in any
// subscription yet.
func (a *App) OpenSubscribePicker(acct subscription.AccountRef) {
	if subs, err := a.backend.ListSubscriptions(); err == nil {
		a.Subscriptions = subs
	}
	acct.Site = normalizeSiteIDUI(acct.Site)
	acct.Name = firstNonEmptyStrUI(acct.Name, acct.UniqueID, acct.UID)
	a.SubscribePicker = SubscribePickerState{Open: true, Account: acct}
}

// closeSubscribePicker hides the dialog. Its list state stays as it is: the
// list may have been built in this very frame; opening resets it.
func (a *App) closeSubscribePicker() {
	a.SubscribePicker.Open = false
	a.SubscribePicker.Error = ""
	a.SubscribePicker.SearchComposing = false
}

// subscribeAsNew starts a subscription named after the account.
func (a *App) subscribeAsNew() {
	acct := a.SubscribePicker.Account
	if _, err := a.backend.CreateSubscription(acct.Name, "", []subscription.AccountRef{acct}, subscription.DefaultSubRules()); err != nil {
		a.SubscribePicker.Error = userErrorText(err)
		return
	}
	a.closeSubscribePicker()
	a.BannerInfo = "已新建订阅：" + acct.Name
	a.RefreshData()
}

// subscribeInto adds the account to an existing subscription.
func (a *App) subscribeInto(sub subscription.Subscription) {
	acct := a.SubscribePicker.Account
	if _, err := a.backend.LinkSubscriptionAccountRef(sub.ID, acct); err != nil {
		a.SubscribePicker.Error = userErrorText(err)
		return
	}
	a.closeSubscribePicker()
	a.BannerInfo = fmt.Sprintf("已把 %s 加入订阅“%s”", acct.Name, firstNonEmptyStrUI(sub.Name, sub.Uname, sub.ID))
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
	if len(parts) == 0 {
		return "没有关联账号"
	}
	return strings.Join(parts, " · ")
}

func (a *App) renderSubscribePickerOverlay(c *ui.Context, w, h float32) {
	if !a.SubscribePicker.Open {
		return
	}
	if c.Shortcut(0, ui.KeyEscape) {
		a.closeSubscribePicker()
		return
	}
	cardW := min(float32(520), max(float32(320), w-40))
	cardH := min(float32(560), max(float32(320), h-80))
	dlg := ui.DialogBase(c, &a.SubscribePicker.Open, func(backdrop, panel ui.Element) {
		backdrop.Label("关闭加入订阅对话框（点击空白处）").Background(ui.RGBA(15, 20, 25, 0.55))
		panel.
			Label("加入订阅对话框").
			Column().
			Size(cardW, cardH).
			Padding(20).
			Radius(16).
			Background(ColorBgMain).
			Border(1, ColorBorder).
			Shadow(0, 12, 40, 0, ui.RGBA(0, 0, 0, 0.24)).
			Gap(12).
			Clip().
			Children(func() {
				a.renderSubscribePickerBody(c)
			})
	})
	if dlg.Dismissed() {
		a.closeSubscribePicker()
	}
}

func (a *App) renderSubscribePickerBody(c *ui.Context) {
	st := &a.SubscribePicker
	acct := st.Account
	var mid int64
	if acct.Site == "bilibili" {
		mid, _ = strconv.ParseInt(acct.UID, 10, 64)
	}

	ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(10).ClipX().Children(func() {
		a.renderAccountAvatar(c, acct.Site, acct.Name, acct.UID, mid, 40, false, "", nil, acct.Face)
		ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
				ui.Text(c, "订阅 "+acct.Name).FontSize(17).FontWeight(600).TextColor(ColorTextMain).SingleLine()
				renderSiteSmallBadgeWithDot(c, acct.Site, "加入订阅账号站点")
			})
			ui.Text(c, "加入一个现有订阅（同一个人的多个账号放在一起），或者新建一个订阅。").FontSize(12).TextColor(ColorTextMuted).MaxLines(2)
		})
	})

	newLabel := "新建订阅“" + acct.Name + "”"
	if AccentPillButton(c, newLabel, "新建订阅").Clicked() {
		a.subscribeAsNew()
	}

	ui.Text(c, "或加入现有订阅").FontSize(12).FontWeight(600).TextColor(ColorTextMuted).SingleLine()
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		inp := ui.TextInput(c, &st.SearchInput).
			Placeholder("搜索订阅名称、备注或账号昵称…").
			Label("加入订阅搜索框").
			Grow(1)
		st.SearchComposing = inp.Composing()
		if !st.SearchComposing && st.CommittedQuery != st.SearchInput {
			st.CommittedQuery = st.SearchInput
			st.ListState = ui.ListState{}
		}
		if st.SearchInput != "" && OutlinePillButton(c, "清空", "清空加入订阅搜索").Clicked() {
			st.SearchInput, st.CommittedQuery, st.SearchComposing = "", "", false
			st.ListState = ui.ListState{}
		}
	})

	filtered := make([]subscription.Subscription, 0, len(a.Subscriptions))
	for _, sub := range a.Subscriptions {
		if ruleBatchMatchesQuery(sub, st.CommittedQuery) {
			filtered = append(filtered, sub)
		}
	}
	ui.Column(c).Grow(1).FillWidth().Border(1, ColorBorder).Radius(12).Background(ColorBgMain).Clip().Children(func() {
		switch {
		case len(a.Subscriptions) == 0:
			ui.Column(c).Grow(1).FillWidth().Center().Padding(24).Children(func() {
				ui.Text(c, "还没有订阅，可以新建一个").FontSize(13).TextColor(ColorTextMuted).SingleLine()
			})
		case len(filtered) == 0:
			ui.Column(c).Grow(1).FillWidth().Center().Padding(24).Children(func() {
				ui.Text(c, "没有匹配的订阅").FontSize(13).TextColor(ColorTextMuted).SingleLine()
			})
		default:
			ui.List(c, &st.ListState, len(filtered), func(i int) {
				sub := filtered[i]
				name := firstNonEmptyStrUI(sub.Name, sub.Uname, sub.ID)
				row := ui.ButtonBase(c).
					Label("加入现有订阅："+sub.ID).
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
					ui.Text(c, "加入").FontSize(13).FontWeight(600).TextColor(ColorAccent).SingleLine().Shrink(0)
				})
				if row.Clicked() {
					a.subscribeInto(sub)
				}
			}).Label("加入订阅列表").Grow(1).FillWidth()
		}
	})

	if st.Error != "" {
		ui.Row(c).FillWidth().Padding(8, 12).Radius(8).Background(ColorErrorBg).AlignItems(ui.Center).Gap(6).Children(func() {
			DrawIcon(c, "alert-circle", 15, ColorError, 1.8)
			ui.Text(c, st.Error).Label("加入订阅错误").FontSize(12).TextColor(ColorError).MaxLines(2).Grow(1)
		})
	}
	ui.Row(c).FillWidth().Justify(ui.End).Children(func() {
		if OutlinePillButton(c, "取消", "取消加入订阅").Clicked() {
			a.closeSubscribePicker()
		}
	})
}
