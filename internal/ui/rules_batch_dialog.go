package ui

import (
	"fmt"
	"strings"

	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

// RuleBatchDialogState holds UI state for the "选择订阅…" batch rule assignment modal (DEC-051 ⑧).
type RuleBatchDialogState struct {
	Open            bool
	RuleID          string
	RuleName        string
	SearchInput     string
	CommittedQuery  string
	SearchComposing bool
	Selected        map[string]bool
	ListState       ui.ListState
	Error           string
}

func ruleBatchSubKey(sub subscription.Subscription) string {
	return firstNonEmptyStrUI(sub.ID, sub.SubID)
}

func (a *App) openRuleBatchDialog(ruleID, ruleName string) {
	if subs, err := a.backend.ListSubscriptions(); err == nil {
		a.Subscriptions = subs
		a.subsRev++
	}
	selected := make(map[string]bool, len(a.Subscriptions))
	for _, sub := range a.Subscriptions {
		key := ruleBatchSubKey(sub)
		if key != "" && effectiveSubRuleID(sub) == ruleID {
			selected[key] = true
		}
	}
	a.RuleBatch = RuleBatchDialogState{
		Open:     true,
		RuleID:   ruleID,
		RuleName: firstNonEmptyStrUI(ruleName, "默认"),
		Selected: selected,
	}
}

func (a *App) closeRuleBatchDialog() {
	a.RuleBatch.Open = false
	a.RuleBatch.Error = ""
	a.RuleBatch.SearchComposing = false
}

func ruleBatchMatchesQuery(sub subscription.Subscription, query string) bool {
	kw := strings.ToLower(strings.TrimSpace(query))
	if kw == "" {
		return true
	}
	if strings.Contains(strings.ToLower(sub.Name), kw) ||
		strings.Contains(strings.ToLower(sub.Uname), kw) ||
		strings.Contains(strings.ToLower(sub.Note), kw) {
		return true
	}
	for _, acct := range sub.Accounts {
		if strings.Contains(strings.ToLower(acct.Name), kw) {
			return true
		}
	}
	return false
}

func (a *App) renderRuleBatchDialogBody(c *ui.Context) {
	if a.RuleBatch.Selected == nil {
		a.RuleBatch.Selected = make(map[string]bool)
	}
	rules := a.backend.ListCacheRules()
	ruleNames := make(map[string]string, len(rules)+1)
	ruleNames[subscription.DefaultRuleID] = "默认"
	for _, r := range rules {
		if r.ID != "" && r.Name != "" {
			ruleNames[r.ID] = r.Name
		}
	}
	ruleName := firstNonEmptyStrUI(ruleNames[a.RuleBatch.RuleID], a.RuleBatch.RuleName, "默认")
	isDefaultRule := a.RuleBatch.RuleID == subscription.DefaultRuleID

	ui.Column(c).FillWidth().Gap(4).Children(func() {
		ui.Text(c, fmt.Sprintf("选择使用「%s」的订阅", ruleName)).
			FontSize(18).FontWeight(600).TextColor(ColorTextMain).SingleLine()
		if isDefaultRule {
			ui.Text(c, "已在用「默认」规则的订阅保持不变；勾选其他订阅可一并改回「默认」规则").
				FontSize(12).TextColor(ColorTextMuted).MaxLines(2)
		}
	})

	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		inp := ui.TextInput(c, &a.RuleBatch.SearchInput).
			Placeholder("搜索订阅名称、备注或账号昵称…").
			Label("规则订阅搜索框").
			Grow(1)
		a.RuleBatch.SearchComposing = inp.Composing()
		if !a.RuleBatch.SearchComposing && a.RuleBatch.CommittedQuery != a.RuleBatch.SearchInput {
			a.RuleBatch.CommittedQuery = a.RuleBatch.SearchInput
			a.RuleBatch.ListState = ui.ListState{}
		}
		if a.RuleBatch.SearchInput != "" {
			if OutlinePillButton(c, "清空", "清空规则订阅搜索").Clicked() {
				a.RuleBatch.SearchInput = ""
				a.RuleBatch.CommittedQuery = ""
				a.RuleBatch.SearchComposing = false
				a.RuleBatch.ListState = ui.ListState{}
			}
		}
	})

	filtered := make([]subscription.Subscription, 0, len(a.Subscriptions))
	selectedCount := 0
	for _, sub := range a.Subscriptions {
		key := ruleBatchSubKey(sub)
		if key != "" && isDefaultRule && effectiveSubRuleID(sub) == subscription.DefaultRuleID {
			a.RuleBatch.Selected[key] = true
		}
		if key != "" && a.RuleBatch.Selected[key] {
			selectedCount++
		}
		if ruleBatchMatchesQuery(sub, a.RuleBatch.CommittedQuery) {
			filtered = append(filtered, sub)
		}
	}

	ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if OutlinePillButton(c, "全选", "全选").Disabled(len(filtered) == 0).Clicked() {
				for _, sub := range filtered {
					if key := ruleBatchSubKey(sub); key != "" {
						a.RuleBatch.Selected[key] = true
					}
				}
				a.RuleBatch.Error = ""
			}
			if OutlinePillButton(c, "全不选", "全不选").Disabled(len(filtered) == 0).Clicked() {
				for _, sub := range filtered {
					if isDefaultRule && effectiveSubRuleID(sub) == subscription.DefaultRuleID {
						continue
					}
					if key := ruleBatchSubKey(sub); key != "" {
						a.RuleBatch.Selected[key] = false
					}
				}
				a.RuleBatch.Error = ""
			}
		})
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if strings.TrimSpace(a.RuleBatch.CommittedQuery) != "" {
				ui.Text(c, fmt.Sprintf("匹配 %d 个", len(filtered))).FontSize(12).TextColor(ColorTextMuted).SingleLine()
			}
			ui.Text(c, fmt.Sprintf("已选 %d 个", selectedCount)).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
		})
	})

	ui.Column(c).Grow(1).FillWidth().Border(1, ColorBorder).Radius(12).Background(ColorBgMain).Clip().Children(func() {
		switch {
		case len(a.Subscriptions) == 0:
			ui.Column(c).Grow(1).FillWidth().Center().Padding(24).Children(func() {
				ui.Text(c, "还没有订阅").FontSize(13).TextColor(ColorTextMuted).SingleLine()
			})
		case len(filtered) == 0:
			ui.Column(c).Grow(1).FillWidth().Center().Padding(24).Children(func() {
				ui.Text(c, "没有匹配的订阅").FontSize(13).TextColor(ColorTextMuted).SingleLine()
			})
		default:
			a.RuleBatch.ListState.Key = func(idx int) any {
				if idx < 0 || idx >= len(filtered) {
					return idx
				}
				return ruleBatchSubKey(filtered[idx])
			}
			ui.List(c, &a.RuleBatch.ListState, len(filtered), func(idx int) {
				if idx < 0 || idx >= len(filtered) {
					return
				}
				sub := filtered[idx]
				key := ruleBatchSubKey(sub)
				subName := firstNonEmptyStrUI(sub.Name, sub.Uname, key)
				origRuleID := effectiveSubRuleID(sub)
				origRuleName := firstNonEmptyStrUI(ruleNames[origRuleID], "默认")
				lockedDefault := isDefaultRule && origRuleID == subscription.DefaultRuleID
				checked := (key != "" && a.RuleBatch.Selected[key]) || lockedDefault

				row := ui.CheckboxBase(c, &checked).
					Label("勾选规则订阅："+subName).
					Disabled(lockedDefault).
					FillWidth().
					Padding(9, 12).
					Gap(10).
					AlignItems(ui.Center).
					BorderWidth(0, 0, 1, 0).
					BorderColor(ColorBorder).
					ClipX()
				if row.Hovered() && !lockedDefault {
					row.Background(ColorHover)
				}
				row.Children(func() {
					renderRuleBatchCheckIndicator(c, checked, lockedDefault)
					pSite, pUID := subscriptionPrimarySiteAndUID(sub)
					a.renderAccountAvatar(c, pSite, subName, pUID, sub.Mid, 32, false, "", nil, sub.Face)
					ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
						ui.Text(c, subName).FontSize(14).FontWeight(500).TextColor(ColorTextMain).SingleLine()
						if note := strings.TrimSpace(sub.Note); note != "" {
							ui.Text(c, note).FontSize(12).TextColor(ColorTextMuted).SingleLine()
						}
					})
					if origRuleID != a.RuleBatch.RuleID {
						ui.Text(c, "当前："+origRuleName).FontSize(12).TextColor(ColorTextMuted).SingleLine().Shrink(0)
					} else if lockedDefault {
						ui.Text(c, "当前：默认").FontSize(12).TextColor(ColorTextMuted).SingleLine().Shrink(0)
					}
				})
				if row.Changed() && !lockedDefault && key != "" {
					a.RuleBatch.Selected[key] = checked
					a.RuleBatch.Error = ""
				}
			}).Grow(1).FillWidth()
		}
	})

	if a.RuleBatch.Error != "" {
		renderErrorBanner(c, "", a.RuleBatch.Error, nil)
	}

	applyLabel := fmt.Sprintf("应用到 %d 个订阅", selectedCount)
	ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.End).Gap(8).Children(func() {
		if OutlinePillButton(c, "取消", "取消选择规则订阅").Clicked() {
			a.closeRuleBatchDialog()
		}
		if AccentPillButton(c, applyLabel, applyLabel).Clicked() {
			a.applyRuleBatchSelection()
		}
	})
}

func renderRuleBatchCheckIndicator(c *ui.Context, checked, locked bool) {
	box := ui.Box(c).Size(18, 18).Radius(5).Shrink(0)
	switch {
	case checked && locked:
		box.Background(ColorAccent.Mix(ColorBgMain, 0.55))
	case checked:
		box.Background(ColorAccent)
	default:
		box.Border(1.5, ColorBorderStrong).Background(ColorBgMain)
	}
	if checked {
		box.Draw(func(p *ui.Painter, r ui.Rect) {
			s := func(v float32) float32 { return v * r.W / 18.0 }
			mark := (&ui.Path{}).
				MoveTo(r.X+s(4.2), r.Y+s(9.2)).
				LineTo(r.X+s(7.6), r.Y+s(12.6)).
				LineTo(r.X+s(13.8), r.Y+s(5.6))
			p.StrokePath(mark, 2.0, ColorWhite)
		})
	}
}

func (a *App) applyRuleBatchSelection() {
	isDefaultRule := a.RuleBatch.RuleID == subscription.DefaultRuleID
	selectedIDs := make([]string, 0, len(a.Subscriptions))
	for _, sub := range a.Subscriptions {
		key := ruleBatchSubKey(sub)
		if key == "" {
			continue
		}
		lockedDefault := isDefaultRule && effectiveSubRuleID(sub) == subscription.DefaultRuleID
		if a.RuleBatch.Selected[key] || lockedDefault {
			selectedIDs = append(selectedIDs, key)
		}
	}
	if err := a.backend.SetSubscriptionsRule(a.RuleBatch.RuleID, selectedIDs); err != nil {
		a.RuleBatch.Error = userErrorText(err)
		return
	}
	a.closeRuleBatchDialog()
	if subs, err := a.backend.ListSubscriptions(); err == nil {
		a.Subscriptions = subs
		a.subsRev++
	} else {
		a.refreshSubscriptions()
	}
	if a.SelectedSubID != "" {
		for _, sub := range a.Subscriptions {
			if sub.ID == a.SelectedSubID || sub.SubID == a.SelectedSubID {
				a.SubRules = sub.Rules
				break
			}
		}
	}
}
