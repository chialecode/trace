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

func (a *App) ruleUseCount(id string) int {
	n := 0
	for _, sub := range a.Subscriptions {
		if sub.RuleID == id {
			n++
		}
	}
	return n
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
			if editing {
				a.renderRuleEditor(c)
			}
		}
		if a.RuleDraft != nil && a.RuleDraft.ID == "" {
			a.renderRuleEditor(c)
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

func (a *App) renderRuleEditor(c *ui.Context) {
	d := a.RuleDraft
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
		settingSwitchRow(c, "开播自动录制", "", "规则：开播自动录制", &d.AutoRecord)
		if a.RuleError != "" {
			ui.Text(c, a.RuleError).FontSize(12).TextColor(ColorError).MaxLines(2)
		}
		ui.Row(c).FillWidth().Padding(12, 0, 0, 0).Gap(8).AlignItems(ui.Center).Children(func() {
			if d.ID != "" && d.ID != subscription.DefaultRuleID {
				if DangerOutlinePillButton(c, "删除", "删除缓存规则").Clicked() {
					if err := a.backend.DeleteCacheRule(d.ID); err != nil {
						a.RuleError = err.Error()
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
					a.RuleError = err.Error()
				} else {
					a.RuleDraft, a.RuleError = nil, ""
					a.refreshSubscriptions()
				}
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
	current := "默认"
	for _, r := range rules {
		if r.ID == sub.RuleID {
			current = r.Name
		}
	}
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
			item := m.Item(rule.Name).Subtitle(subscription.RuleSummary(rule)).Checked(rule.ID == sub.RuleID)
			if item.Chosen() && rule.ID != sub.RuleID {
				_, err := a.backend.SetSubscriptionRule(sub.ID, rule.ID)
				a.showError(err)
				if err == nil {
					a.BannerInfo = fmt.Sprintf("「%s」改用规则「%s」", sub.Name, rule.Name)
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
