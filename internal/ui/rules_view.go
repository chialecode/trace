package ui

import (
	"fmt"
	"slices"

	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

// Atomic cache capability tags (DEC-070): one tag equals one cache capability.
// Settings → 缓存标签 manages subscriptions per tag and provides cross-subscription
// multi-tag batch updates; each subscription toggles its own tags directly.

func effectiveSubTags(sub subscription.Subscription) []string {
	if sub.Tags != nil {
		return subscription.NormalizeTags(sub.Tags)
	}
	return subscription.SubRulesToTags(sub.Rules)
}

func (a *App) tagUseCount(tagID string) int {
	n := 0
	for _, sub := range a.Subscriptions {
		if subscription.HasTag(effectiveSubTags(sub), tagID) {
			n++
		}
	}
	return n
}

func (a *App) toggleSubscriptionTag(sub *subscription.Subscription, tagID string) {
	if sub == nil || a.backend == nil {
		return
	}
	cur := effectiveSubTags(*sub)
	var next []string
	removed := false
	for _, id := range cur {
		if id == tagID {
			removed = true
			continue
		}
		next = append(next, id)
	}
	if !removed {
		next = append(slices.Clone(cur), tagID)
	}
	next = subscription.NormalizeTags(next)
	updated, err := a.backend.SetSubscriptionTags(sub.ID, next)
	a.showError(err)
	if err == nil {
		*sub = updated
		a.SubRules = updated.Rules
		tagDef, _ := subscription.CapabilityTagByID(tagID)
		tagLabel := firstNonEmptyStrUI(tagDef.Name, tagID)
		subDisplay := firstNonEmptyStrUI(sub.Name, sub.Uname, sub.ID)
		if removed {
			a.BannerInfo = fmt.Sprintf("已为「%s」移除标签「%s」", subDisplay, tagLabel)
		} else {
			a.BannerInfo = fmt.Sprintf("已为「%s」开启标签「%s」", subDisplay, tagLabel)
		}
	}
	a.refreshSubscriptions()
}

// renderCacheRulesSettings renders Settings → 缓存标签: a right-aligned batch-set
// button at the top, followed by each capability tag's name, subscription count,
// and per-tag "管理订阅…" dialog trigger (DEC-038 ⑤, DEC-070).
func (a *App) renderCacheRulesSettings(c *ui.Context) {
	tags := a.backend.ListCapabilityTags()
	ui.Column(c).FillWidth().Gap(0).ClipX().Children(func() {
		ui.Row(c).
			FillWidth().
			Padding(0, 0, 12, 0).
			BorderWidth(0, 0, 1, 0).
			BorderColor(ColorBorder).
			AlignItems(ui.Center).
			Justify(ui.End).
			ClipX().
			Children(func() {
				if AccentPillButton(c, "批量设置标签…", "打开批量设置订阅标签").Clicked() {
					a.openMultiTagBatchDialog()
				}
			})

		for _, item := range tags {
			tag := item
			count := a.tagUseCount(tag.ID)
			ui.Row(c).
				Label("缓存标签行："+tag.ID).
				FillWidth().
				Padding(12, 0).
				BorderWidth(0, 0, 1, 0).
				BorderColor(ColorBorder).
				AlignItems(ui.Center).
				Justify(ui.SpaceBetween).
				Gap(12).
				ClipX().
				Children(func() {
					ui.Row(c).Grow(1).AlignItems(ui.Center).Gap(8).ClipX().Children(func() {
						ui.Text(c, tag.Name).FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
						ui.Text(c, fmt.Sprintf("%d 个订阅", count)).
							Label("标签订阅数：" + tag.ID).
							FontSize(12).
							TextColor(ColorTextMuted).
							SingleLine()
					})
					ui.Row(c).Shrink(0).Children(func() {
						if OutlinePillButton(c, "管理订阅…", "管理标签订阅："+tag.ID).Clicked() {
							a.openRuleBatchDialog(tag.ID, tag.Name, tag.Description)
						}
					})
				})
		}
	})
}

// ToggleChip is a small pill that is on or off.
func ToggleChip(c *ui.Context, text, label string, on bool) ui.Element {
	return toggleChipSized(c, text, label, on, 5, 12, 13)
}

func toggleChipSized(c *ui.Context, text, label string, on bool, padV, padH, fontSize float32) ui.Element {
	btn := ui.ButtonBase(c).Label(label).Padding(padV, padH).Radius(999).AlignItems(ui.Center)
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
		ui.Text(c, text).FontSize(fontSize).FontWeight(500).TextColor(col).SingleLine()
	})
	return btn
}

// renderSubscriptionTagChipsBar renders the subscription's active capability
// tags as removable accent chips (or "无缓存标签" when empty) alongside the
// "+ 标签" / "设置标签" popover selector so tags can be toggled directly in
// Subscription Detail without showing inactive capabilities (DEC-070).
func (a *App) renderSubscriptionTagChipsBar(c *ui.Context, sub *subscription.Subscription) {
	if sub == nil {
		return
	}
	activeTags := effectiveSubTags(*sub)
	ui.Row(c).
		Label("订阅详情缓存标签栏").
		FillWidth().
		Gap(6).
		AlignItems(ui.Center).
		Wrap().
		Children(func() {
			if len(activeTags) == 0 {
				ui.Text(c, "无缓存标签").FontSize(12).TextColor(ColorTextMuted).SingleLine()
			} else {
				for _, tagID := range activeTags {
					id := tagID
					tagDef, _ := subscription.CapabilityTagByID(id)
					tagName := firstNonEmptyStrUI(tagDef.Name, id)
					chip := toggleChipSized(c, tagName, "订阅缓存标签："+tagName, true, 4, 10, 12).
						Tooltip(firstNonEmptyStrUI(tagDef.Description, tagName))
					if chip.Clicked() {
						a.toggleSubscriptionTag(sub, id)
					}
				}
			}
			a.renderSubscriptionRuleButton(c, sub, activeTags)
		})
}

// renderSubscriptionRuleButton renders the inline tag-selector popover button
// ("设置标签 ▾" when empty, or "+ 标签 ▾" when active tags are shown) to toggle
// any capability tag, clear all tags, or jump to Settings → 缓存标签.
func (a *App) renderSubscriptionRuleButton(c *ui.Context, sub *subscription.Subscription, activeTags []string) {
	allTags := a.backend.ListCapabilityTags()
	hasAny := len(activeTags) > 0
	buttonText := "设置标签"
	if hasAny {
		buttonText = "+ 标签"
	}
	open := a.SubRuleMenuOpen
	btn := ui.ButtonBase(c).
		Label("订阅缓存标签菜单").
		Tooltip(subscription.TagsSummary(activeTags)).
		Padding(4, 10).
		Gap(4).
		AlignItems(ui.Center).
		Radius(999).
		Border(1, ColorBorderStrong).
		Shrink(0)
	if btn.Hovered() || open {
		btn.Background(ColorHover)
	}
	btn.Children(func() {
		ui.Text(c, buttonText).FontSize(12).FontWeight(500).TextColor(ColorTextMain).SingleLine()
		DrawIcon(c, "chevron-down", 13, ColorTextMuted, 1.6)
	})
	btn.PopoverMenuButton(func(m *ui.Menu) {
		for _, item := range allTags {
			tag := item
			checked := subscription.HasTag(activeTags, tag.ID)
			mi := m.Item(tag.Name).Subtitle(tag.Description).Checked(checked)
			if mi.Chosen() {
				a.toggleSubscriptionTag(sub, tag.ID)
			}
		}
		if hasAny {
			m.Separator()
			if m.Item("清空全部标签").Chosen() {
				updated, err := a.backend.SetSubscriptionTags(sub.ID, []string{})
				a.showError(err)
				if err == nil {
					*sub = updated
					a.SubRules = updated.Rules
					subDisplay := firstNonEmptyStrUI(sub.Name, sub.Uname, sub.ID)
					a.BannerInfo = fmt.Sprintf("已清空「%s」的全部缓存标签", subDisplay)
				}
				a.refreshSubscriptions()
			}
		}
		m.Separator()
		if m.Item("批量管理缓存标签").Chosen() {
			a.SettingsCategory = "rules"
			a.Navigate(RouteSettings)
		}
	}, ui.PopoverMenuButtonOptions{At: ui.AnchorBottomStart, Self: ui.AnchorTopStart, Open: &open})
	a.SubRuleMenuOpen = open
}
