package ui

import (
	"fmt"
	"slices"

	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

// Shared cache rules (DEC-040): rules are kept in Settings → 缓存规则, and a
// subscription only chooses one.

var ruleTypeChoices = []struct{ code, name string }{
	{"video", "视频"}, {"draw", "图文"}, {"word", "文字"}, {"forward", "转发"},
}

func effectiveSubRuleID(sub subscription.Subscription) string {
	if sub.RuleID == "" {
		return subscription.DefaultRuleID
	}
	return sub.RuleID
}

func (a *App) ruleUseCount(id string) int {
	n := 0
	for _, sub := range a.Subscriptions {
		if effectiveSubRuleID(sub) == id {
			n++
		}
	}
	return n
}

func savedRuleNameFromList(rules []subscription.CacheRule, id, fallback string) string {
	for _, r := range rules {
		if r.ID == id && r.Name != "" {
			return r.Name
		}
	}
	if fallback != "" {
		return fallback
	}
	return "默认"
}

// renderCacheRulesSettings lists the rules; choosing one opens its editor in
// place, and "新建规则" adds one.
func (a *App) renderCacheRulesSettings(c *ui.Context) {
	rules := a.backend.ListCacheRules()
	ui.Column(c).FillWidth().Gap(0).ClipX().Children(func() {
		for _, r := range rules {
			rule := r
			editing := a.RuleDraft != nil && a.RuleDraft.ID == rule.ID
			row := ui.ButtonBase(c).Label("缓存规则："+rule.Name).FillWidth().Padding(12, 0).Gap(12).AlignItems(ui.Center).
				BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder)
			row.Children(func() {
				ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
					ui.Text(c, rule.Name).FontSize(14).FontWeight(500).TextColor(ColorTextMain).SingleLine()
					ui.Text(c, fmt.Sprintf("%s · %d 个订阅", subscription.RuleSummary(rule), a.ruleUseCount(rule.ID))).
						FontSize(12).TextColor(ColorTextMuted).SingleLine()
				})
				icon := "chevron-down"
				if editing {
					icon = "chevron-up"
				}
				DrawIcon(c, icon, 16, ColorTextMuted, 1.6)
			})
			if row.Clicked() {
				if editing {
					a.RuleDraft = nil
				} else {
					draft := rule
					draft.Types = slices.Clone(rule.Types)
					a.RuleDraft = &draft
				}
				a.RuleError = ""
			}
			// Read the draft again: the click above may have just closed it.
			if a.RuleDraft != nil && a.RuleDraft.ID == rule.ID {
				a.renderRuleEditor(c, rules)
			}
		}
		if a.RuleDraft != nil && a.RuleDraft.ID == "" {
			a.renderRuleEditor(c, rules)
		} else {
			ui.Row(c).FillWidth().Padding(14, 0).Children(func() {
				if OutlinePillButton(c, "新建规则", "新建缓存规则").Clicked() {
					a.RuleDraft = &subscription.CacheRule{ScheduledCheck: true, Types: []string{"video", "draw"}}
					a.RuleError = ""
				}
			})
		}
	})
}

func (a *App) renderRuleEditor(c *ui.Context, rules []subscription.CacheRule) {
	d := a.RuleDraft
	if d == nil {
		return
	}
	if d.ID == subscription.DefaultRuleID {
		a.renderDefaultRuleDetails(c, rules)
		return
	}
	ui.Column(c).FillWidth().Padding(4, 0, 14, 16).Gap(0).BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).ClipX().Children(func() {
		ui.Row(c).FillWidth().Padding(10, 0).Gap(12).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "名称").FontSize(14).FontWeight(500).TextColor(ColorTextMain).SingleLine()
			ui.TextInput(c, &d.Name).Label("规则名称").Placeholder("例如：只存视频").Grow(1)
		})
		settingSwitchRow(c, "定时检查", "", "规则：定时检查", &d.ScheduledCheck)
		settingSwitchRow(c, "自动缓存新动态", "只缓存选用这条规则之后发布的动态", "规则：自动缓存新动态", &d.AutoCache)
		if d.AutoCache {
			ui.Row(c).FillWidth().Padding(10, 0).Gap(8).AlignItems(ui.Center).Wrap().Children(func() {
				ui.Text(c, "类型").FontSize(14).FontWeight(500).TextColor(ColorTextMain).SingleLine()
				for _, tc := range ruleTypeChoices {
					t := tc
					on := slices.Contains(d.Types, t.code)
					if ToggleChip(c, t.name, "规则类型："+t.name, on).Clicked() {
						if on {
							d.Types = slices.DeleteFunc(slices.Clone(d.Types), func(x string) bool { return x == t.code })
						} else {
							d.Types = append(d.Types, t.code)
						}
					}
				}
			})
		}
		settingSwitchRow(c, "全部缓存", "每 7 天检查一次每个账号的全部作品（哔哩哔哩为投稿视频），没缓存的放进缓存队列慢慢缓存", "规则：全部缓存", &d.FullCache)
		settingSwitchRow(c, "开播自动录制", "", "规则：开播自动录制", &d.AutoRecord)
		if d.ID != "" {
			ui.Row(c).FillWidth().Padding(10, 0).Gap(12).AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
				ui.Text(c, fmt.Sprintf("使用这条规则的订阅（%d）", a.ruleUseCount(d.ID))).
					FontSize(14).FontWeight(500).TextColor(ColorTextMain).SingleLine()
				if OutlinePillButton(c, "选择订阅…", "选择订阅…").Clicked() {
					a.openRuleBatchDialog(d.ID, savedRuleNameFromList(rules, d.ID, d.Name))
				}
			})
		}
		if a.RuleError != "" {
			ui.Text(c, a.RuleError).FontSize(12).TextColor(ColorError).MaxLines(2)
		}
		ui.Row(c).FillWidth().Padding(12, 0, 0, 0).Gap(8).AlignItems(ui.Center).Children(func() {
			if d.ID != "" && d.ID != subscription.DefaultRuleID {
				if DangerOutlinePillButton(c, "删除", "删除缓存规则").Clicked() {
					if err := a.backend.DeleteCacheRule(d.ID); err != nil {
						a.RuleError = userErrorText(err)
					} else {
						a.RuleDraft = nil
						a.refreshSubscriptions()
					}
					return
				}
			}
			ui.Box(c).Grow(1)
			if OutlinePillButton(c, "取消", "取消编辑缓存规则").Clicked() {
				a.RuleDraft, a.RuleError = nil, ""
				return
			}
			if AccentPillButton(c, "保存", "保存缓存规则").Clicked() {
				if _, err := a.backend.SaveCacheRule(*d); err != nil {
					a.RuleError = userErrorText(err)
				} else {
					a.RuleDraft, a.RuleError = nil, ""
					a.refreshSubscriptions()
				}
			}
		})
	})
}

// renderDefaultRuleDetails shows the built-in rule, which cannot be edited
// (DEC-054): what it does, and which subscriptions use it.
func (a *App) renderDefaultRuleDetails(c *ui.Context, rules []subscription.CacheRule) {
	ui.Column(c).FillWidth().Padding(4, 0, 14, 16).Gap(0).BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).ClipX().Children(func() {
		ui.Text(c, "不定时检查、不自动缓存、不录制直播；新订阅默认使用，不能修改。").
			Label("默认规则说明").FontSize(13).TextColor(ColorTextMuted).MaxLines(2)
		ui.Row(c).FillWidth().Padding(10, 0, 0, 0).Gap(12).AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
			ui.Text(c, fmt.Sprintf("使用这条规则的订阅（%d）", a.ruleUseCount(subscription.DefaultRuleID))).
				FontSize(14).FontWeight(500).TextColor(ColorTextMain).SingleLine()
			if OutlinePillButton(c, "选择订阅…", "选择订阅…").Clicked() {
				a.openRuleBatchDialog(subscription.DefaultRuleID, savedRuleNameFromList(rules, subscription.DefaultRuleID, "默认"))
			}
		})
	})
}

// ToggleChip is a small pill that is on or off.
func ToggleChip(c *ui.Context, text, label string, on bool) ui.Element {
	btn := ui.ButtonBase(c).Label(label).Padding(5, 12).Radius(999).AlignItems(ui.Center)
	if on {
		btn.Background(ColorAccentBg).Border(1, ColorAccent)
	} else {
		btn.Border(1, ColorBorderStrong)
		if btn.Hovered() {
			btn.Background(ColorHover)
		}
	}
	btn.Children(func() {
		col := ColorTextMain
		if on {
			col = ColorAccent
		}
		ui.Text(c, text).FontSize(13).FontWeight(500).TextColor(col).SingleLine()
	})
	return btn
}

// renderSubscriptionRuleButton is the subscription's one control for its
// automation: the rule it uses, chosen from a menu.
func (a *App) renderSubscriptionRuleButton(c *ui.Context, sub *subscription.Subscription) {
	rules := a.backend.ListCacheRules()
	curRuleID := effectiveSubRuleID(*sub)
	current := savedRuleNameFromList(rules, curRuleID, "默认")
	open := a.SubRuleMenuOpen
	btn := ui.ButtonBase(c).Label("订阅缓存规则："+current).Tooltip("缓存规则").Padding(6, 12).Gap(6).AlignItems(ui.Center).Radius(999).Border(1, ColorBorderStrong).Shrink(0)
	if btn.Hovered() || open {
		btn.Background(ColorHover)
	}
	btn.Children(func() {
		ui.Text(c, "规则："+current).FontSize(13).FontWeight(500).TextColor(ColorTextMain).SingleLine()
		DrawIcon(c, "chevron-down", 14, ColorTextMuted, 1.6)
	})
	btn.PopoverMenuButton(func(m *ui.Menu) {
		for _, r := range rules {
			rule := r
			item := m.Item(rule.Name).Subtitle(subscription.RuleSummary(rule)).Checked(rule.ID == curRuleID)
			if item.Chosen() && rule.ID != curRuleID {
				_, err := a.backend.SetSubscriptionRule(sub.ID, rule.ID)
				a.showError(err)
				if err == nil {
					subDisplay := firstNonEmptyStrUI(sub.Name, sub.Uname, sub.ID)
					a.BannerInfo = fmt.Sprintf("「%s」改用规则「%s」", subDisplay, rule.Name)
				}
				a.refreshSubscriptions()
			}
		}
		m.Separator()
		if m.Item("管理缓存规则").Chosen() {
			a.SettingsCategory = "rules"
			a.Navigate(RouteSettings)
		}
	}, ui.PopoverMenuButtonOptions{At: ui.AnchorBottomEnd, Self: ui.AnchorTopEnd, Open: &open})
	a.SubRuleMenuOpen = open
}
