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

func (a *App) presetUseCount(presetID string) int {
	n := 0
	for _, sub := range a.Subscriptions {
		matched, _ := subscription.MatchTrackingPreset(effectiveSubTags(sub))
		if matched == presetID {
			n++
		}
	}
	return n
}

func (a *App) applySubscriptionPreset(sub *subscription.Subscription, presetID string) {
	if sub == nil || a.backend == nil {
		return
	}
	_, autoRec := subscription.MatchTrackingPreset(effectiveSubTags(*sub))
	next, err := subscription.PresetTags(presetID, autoRec)
	if err != nil {
		a.showError(err)
		return
	}
	updated, err := a.backend.SetSubscriptionTags(sub.ID, next)
	a.showError(err)
	if err == nil {
		*sub = updated
		a.SubRules = updated.Rules
		subDisplay := firstNonEmptyStrUI(sub.Name, sub.Uname, sub.ID)
		a.notifyInfo(fmt.Sprintf("已将「%s」切换为「%s」", subDisplay, subscription.TrackingModeLabel(updated.Tags)))
	}
	a.refreshSubscriptions()
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
			a.notifyInfo(fmt.Sprintf("已为「%s」移除标签「%s」", subDisplay, tagLabel))
		} else {
			a.notifyInfo(fmt.Sprintf("已为「%s」开启标签「%s」", subDisplay, tagLabel))
		}
	}
	a.refreshSubscriptions()
}

// renderCacheRulesSettings renders Settings → 缓存标签: a top row with quick
// tracking preset triggers and the right-aligned batch-set button, followed by
// each capability tag's name, subscription count, and per-tag "管理订阅…" dialog
// trigger without verbose explanatory subtitles (DEC-038 ⑤, DEC-070, DEC-080).
func (a *App) renderCacheRulesSettings(c *ui.Context) {
	tags := a.backend.ListCapabilityTags()
	presets := subscription.AllTrackingPresets()
	ui.Column(c).FillWidth().Gap(0).ClipX().Children(func() {
		ui.Row(c).
			FillWidth().
			Padding(0, 0, 10, 0).
			AlignItems(ui.Center).
			Justify(ui.SpaceBetween).
			Gap(12).
			ClipX().
			Children(func() {
				ui.Text(c, "快速预设").FontSize(13).FontWeight(600).TextColor(ColorTextMuted).SingleLine()
				if AccentPillButton(c, "批量设置标签…", "打开批量设置订阅标签").Clicked() {
					a.openMultiTagBatchDialog()
				}
			})

		ui.Row(c).
			Label("缓存标签快速预设栏").
			FillWidth().
			Padding(0, 0, 12, 0).
			BorderWidth(0, 0, 1, 0).
			BorderColor(ColorBorder).
			Gap(8).
			AlignItems(ui.Center).
			Wrap().
			ClipX().
			Children(func() {
				for _, item := range presets {
					p := item
					btnText := fmt.Sprintf("%s · %d", p.Name, a.presetUseCount(p.ID))
					if OutlinePillButton(c, btnText, "快速应用预设："+p.ID).Tooltip(p.Description).Clicked() {
						a.openMultiTagBatchDialogWithPreset(p.ID)
					}
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

// renderSubscriptionTagChipsBar renders the subscription's current tracking mode
// (preset name or "自定义") plus "录直播" when enabled, a dropdown to switch
// presets / toggle 录直播 / expand the 7 atomic tags / clear / batch-manage,
// optional expanded atomic tag toggles, and a calm dependency gap warning when
// auto-cache is enabled without scheduled_check (DEC-070, DEC-080).
func (a *App) renderSubscriptionTagChipsBar(c *ui.Context, sub *subscription.Subscription) {
	if sub == nil {
		return
	}
	activeTags := effectiveSubTags(*sub)
	presetID, autoRec := subscription.MatchTrackingPreset(activeTags)
	expanded := a.SubCustomTagsSubID == sub.ID

	ui.Column(c).
		Label("订阅详情缓存标签栏").
		FillWidth().
		Gap(6).
		ClipX().
		Children(func() {
			ui.Row(c).
				FillWidth().
				Gap(6).
				AlignItems(ui.Center).
				Wrap().
				ClipX().
				Children(func() {
					a.renderSubscriptionRuleButton(c, sub, activeTags, presetID, autoRec, expanded)
					if autoRec {
						liveChip := toggleChipSized(c, "录直播", "订阅缓存标签：开播录制", true, 4, 10, 12).
							Tooltip("已开启开播录制，点击关闭")
						if liveChip.Clicked() {
							a.toggleSubscriptionTag(sub, subscription.TagAutoRecord)
						}
					}
					if presetID == subscription.PresetCustom {
						for _, tagID := range activeTags {
							if tagID == subscription.TagAutoRecord {
								continue
							}
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
				})

			if expanded {
				allTags := a.backend.ListCapabilityTags()
				ui.Column(c).
					Label("订阅详情自定义原子标签栏").
					FillWidth().
					Padding(8, 10).
					Radius(10).
					Background(ColorBgMain).
					Border(1, ColorBorder).
					Gap(6).
					ClipX().
					Children(func() {
						ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
							ui.Text(c, "自定义原子标签").FontSize(12).FontWeight(600).TextColor(ColorTextMuted).SingleLine()
							collapseBtn := ui.ButtonBase(c).
								Label("收起原子标签").
								Padding(2, 8).
								Radius(999).
								Border(1, ColorBorder)
							if collapseBtn.Hovered() {
								collapseBtn.Background(ColorHover)
							}
							collapseBtn.Children(func() {
								ui.Text(c, "收起").FontSize(11).TextColor(ColorTextMuted).SingleLine()
							})
							if collapseBtn.Clicked() {
								a.SubCustomTagsSubID = ""
							}
						})
						ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Wrap().ClipX().Children(func() {
							for _, item := range allTags {
								tag := item
								on := subscription.HasTag(activeTags, tag.ID)
								chip := toggleChipSized(c, tag.Name, "订阅原子标签："+tag.Name, on, 4, 10, 12).
									Tooltip(tag.Description)
								if chip.Clicked() {
									a.toggleSubscriptionTag(sub, tag.ID)
								}
							}
						})
					})
			}

			if subscription.HasAutoCacheWithoutScheduledCheck(activeTags) {
				renderAutoCacheDependencyWarning(c, "自动缓存缺少定时检查提示", "", func() {
					if OutlinePillButton(c, "开启定时检查", "一键开启定时检查").Clicked() {
						a.toggleSubscriptionTag(sub, subscription.TagScheduledCheck)
					}
				})
			}
		})
}

// renderSubscriptionRuleButton renders the tracking mode dropdown button in
// Subscription Detail: one-click preset switch, overlayable 录直播 toggle,
// expandable 7 atomic tags, clear all tags, and jump to Settings → 缓存标签.
func (a *App) renderSubscriptionRuleButton(c *ui.Context, sub *subscription.Subscription, activeTags []string, presetID string, autoRec, expanded bool) {
	presets := subscription.AllTrackingPresets()
	hasAny := len(activeTags) > 0
	buttonText := "追踪：" + subscription.TrackingPresetName(presetID)
	open := a.SubRuleMenuOpen
	btn := ui.ButtonBase(c).
		Label("订阅缓存标签菜单").
		Tooltip(subscription.TagsSummary(activeTags)).
		Padding(4, 10).
		Gap(4).
		AlignItems(ui.Center).
		Radius(999).
		Shrink(0)
	if presetID != subscription.PresetManual {
		btn.Background(ColorAccentBg).Border(1, ColorAccent)
	} else {
		btn.Border(1, ColorBorderStrong)
		if btn.Hovered() || open {
			btn.Background(ColorHover)
		}
	}
	textCol := ColorTextMain
	iconCol := ColorTextMuted
	if presetID != subscription.PresetManual {
		textCol = ColorAccent
		iconCol = ColorAccent
	}
	btn.Children(func() {
		ui.Text(c, buttonText).FontSize(12).FontWeight(600).TextColor(textCol).SingleLine()
		DrawIcon(c, "chevron-down", 13, iconCol, 1.6)
	})
	btn.PopoverMenuButton(func(m *ui.Menu) {
		for _, item := range presets {
			p := item
			mi := m.Item(p.Name).Subtitle(p.Description).Checked(presetID == p.ID)
			if mi.Chosen() {
				a.applySubscriptionPreset(sub, p.ID)
			}
		}
		m.Separator()
		if m.Item("录直播").Subtitle("发现哔哩哔哩或抖音账号正在直播时自动开始录制").Checked(autoRec).Chosen() {
			a.toggleSubscriptionTag(sub, subscription.TagAutoRecord)
		}
		expandTitle := "自定义原子标签"
		if expanded {
			expandTitle = "收起原子标签"
		}
		if m.Item(expandTitle).Subtitle("展开全部 7 个原子能力标签细粒度开关").Checked(expanded).Chosen() {
			if expanded {
				a.SubCustomTagsSubID = ""
			} else {
				a.SubCustomTagsSubID = sub.ID
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
					a.notifyInfo(fmt.Sprintf("已清空「%s」的全部缓存标签", subDisplay))
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
