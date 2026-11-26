package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

// RuleBatchDialogState holds UI state for the per-tag "管理订阅…" batch
// subscription assignment modal (DEC-051 ⑧, DEC-070).
type RuleBatchDialogState struct {
	Open            bool
	TagID           string
	TagName         string
	TagDescription  string
	SearchInput     string
	CommittedQuery  string
	SearchComposing bool
	Selected        map[string]bool
	ListState       ui.ListState
	Error           string
}

// MultiTagBatchDialogState holds UI state for the cross-subscription multi-tag
// batch setup modal ("批量设置缓存标签": add / remove / replace tags across
// multiple selected subscriptions at once, DEC-070).
type MultiTagBatchDialogState struct {
	Open            bool
	Mode            string // "add" | "remove" | "replace"
	SelectedTags    map[string]bool
	SearchInput     string
	CommittedQuery  string
	SearchComposing bool
	SelectedSubs    map[string]bool
	ListState       ui.ListState
	Error           string
}

func ruleBatchSubKey(sub subscription.Subscription) string {
	return firstNonEmptyStrUI(sub.ID, sub.SubID)
}

func (a *App) openRuleBatchDialog(tagID, tagName, tagDesc string) {
	if subs, err := a.backend.ListSubscriptions(); err == nil {
		a.Subscriptions = subs
		a.subsRev++
	}
	if tagDef, ok := subscription.CapabilityTagByID(tagID); ok {
		if tagName == "" {
			tagName = tagDef.Name
		}
		if tagDesc == "" {
			tagDesc = tagDef.Description
		}
	}
	selected := make(map[string]bool, len(a.Subscriptions))
	for _, sub := range a.Subscriptions {
		key := ruleBatchSubKey(sub)
		if key != "" && subscription.HasTag(effectiveSubTags(sub), tagID) {
			selected[key] = true
		}
	}
	a.RuleBatch = RuleBatchDialogState{
		Open:           true,
		TagID:          tagID,
		TagName:        firstNonEmptyStrUI(tagName, tagID),
		TagDescription: tagDesc,
		Selected:       selected,
	}
}

func (a *App) closeRuleBatchDialog() {
	a.RuleBatch.Open = false
	a.RuleBatch.Error = ""
	a.RuleBatch.SearchComposing = false
}

func (a *App) openMultiTagBatchDialog() {
	if subs, err := a.backend.ListSubscriptions(); err == nil {
		a.Subscriptions = subs
		a.subsRev++
	}
	a.MultiTagBatch = MultiTagBatchDialogState{
		Open:         true,
		Mode:         subscription.TagBatchAdd,
		SelectedTags: make(map[string]bool),
		SelectedSubs: make(map[string]bool, len(a.Subscriptions)),
	}
}

func (a *App) openMultiTagBatchDialogWithPreset(presetID string) {
	a.openMultiTagBatchDialog()
	a.MultiTagBatch.Mode = subscription.TagBatchReplace
	if tags, err := subscription.PresetTags(presetID, false); err == nil {
		for _, id := range tags {
			a.MultiTagBatch.SelectedTags[id] = true
		}
	}
}

func (a *App) closeMultiTagBatchDialog() {
	a.MultiTagBatch.Open = false
	a.MultiTagBatch.Error = ""
	a.MultiTagBatch.SearchComposing = false
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
	tagDef, ok := subscription.CapabilityTagByID(a.RuleBatch.TagID)
	tagName := firstNonEmptyStrUI(a.RuleBatch.TagName, tagDef.Name, a.RuleBatch.TagID)
	tagDesc := firstNonEmptyStrUI(a.RuleBatch.TagDescription, tagDef.Description)
	if !ok && tagName == "" {
		tagName = "缓存标签"
	}

	ui.Column(c).FillWidth().Gap(4).Children(func() {
		ui.Text(c, fmt.Sprintf("管理「%s」的订阅", tagName)).
			FontSize(18).FontWeight(600).TextColor(ColorTextMain).SingleLine()
		if tagDesc != "" {
			ui.Text(c, tagDesc).
				FontSize(12).LineHeight(LineHeightCompact).TextColor(ColorTextMuted).MaxLines(2)
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
				checked := key != "" && a.RuleBatch.Selected[key]
				curTags := effectiveSubTags(sub)

				row := ui.CheckboxBase(c, &checked).
					Label("勾选规则订阅："+subName).
					FillWidth().
					Padding(9, 12).
					Gap(10).
					AlignItems(ui.Center).
					BorderWidth(0, 0, 1, 0).
					BorderColor(ColorBorder).
					ClipX()
				if row.Hovered() {
					row.Background(ColorHover)
				}
				row.Children(func() {
					renderRuleBatchCheckIndicator(c, checked, false)
					pSite, pUID := subscriptionPrimarySiteAndUID(sub)
					a.renderAccountAvatar(c, pSite, subName, pUID, sub.Mid, 32, false, "", nil, sub.Face)
					ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
						ui.Text(c, subName).FontSize(14).FontWeight(500).TextColor(ColorTextMain).SingleLine()
						if note := strings.TrimSpace(sub.Note); note != "" {
							ui.Text(c, note).FontSize(12).TextColor(ColorTextMuted).SingleLine()
						}
					})
					ui.Text(c, subscription.TagsSummary(curTags)).
						FontSize(12).TextColor(ColorTextMuted).SingleLine().Shrink(1)
				})
				if row.Changed() && key != "" {
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

func (a *App) renderMultiTagBatchDialogBody(c *ui.Context) {
	if a.MultiTagBatch.SelectedTags == nil {
		a.MultiTagBatch.SelectedTags = make(map[string]bool)
	}
	if a.MultiTagBatch.SelectedSubs == nil {
		a.MultiTagBatch.SelectedSubs = make(map[string]bool)
	}
	if a.MultiTagBatch.Mode == "" {
		a.MultiTagBatch.Mode = subscription.TagBatchAdd
	}
	allTags := a.backend.ListCapabilityTags()

	ui.Column(c).FillWidth().Gap(4).Children(func() {
		ui.Text(c, "批量设置缓存标签").
			FontSize(18).FontWeight(600).TextColor(ColorTextMain).SingleLine()
		modeHint := "为勾选的订阅批量添加以下标签，每个订阅原有的其他标签保留不变。"
		switch a.MultiTagBatch.Mode {
		case subscription.TagBatchRemove:
			modeHint = "从勾选的订阅中移除以下标签，其余标签保留不变。"
		case subscription.TagBatchReplace:
			modeHint = "将勾选订阅的标签统一替换为以下所选标签；不选任何标签即清空全部标签。"
		}
		ui.Text(c, modeHint).
			FontSize(12).LineHeight(LineHeightCompact).TextColor(ColorTextMuted).MaxLines(2)
	})

	modes := [3]struct {
		mode  string
		title string
	}{
		{subscription.TagBatchAdd, "添加标签"},
		{subscription.TagBatchRemove, "移除标签"},
		{subscription.TagBatchReplace, "替换标签"},
	}
	ui.Row(c).
		FillWidth().
		Padding(3).
		Radius(10).
		Background(ColorBgSecondary).
		Gap(4).
		Children(func() {
			for _, m := range modes {
				opt := m
				active := a.MultiTagBatch.Mode == opt.mode
				seg := ui.ButtonBase(c).
					Label("批量模式："+opt.mode).
					Grow(1).
					Basis(0).
					Padding(6, 8).
					Radius(8).
					Center()
				if active {
					seg.Background(ColorBgMain).Border(1, ColorBorder)
				} else if seg.Hovered() {
					seg.Background(ColorHover)
				}
				seg.Children(func() {
					col := ColorTextMuted
					weight := 500
					if active {
						col = ColorAccent
						weight = 600
					}
					ui.Text(c, opt.title).FontSize(13).FontWeight(weight).TextColor(col).SingleLine()
				})
				if seg.Clicked() && a.MultiTagBatch.Mode != opt.mode {
					a.MultiTagBatch.Mode = opt.mode
					a.MultiTagBatch.Error = ""
				}
			}
		})

	selectedTagCount := 0
	selectedTagsSlice := make([]string, 0, len(allTags))
	for _, t := range allTags {
		if a.MultiTagBatch.SelectedTags[t.ID] {
			selectedTagCount++
			selectedTagsSlice = append(selectedTagsSlice, t.ID)
		}
	}
	matchedPreset, _ := subscription.MatchTrackingPreset(selectedTagsSlice)

	ui.Row(c).Label("批量快速预设栏").FillWidth().Gap(6).AlignItems(ui.Center).Wrap().ClipX().Children(func() {
		ui.Text(c, "快速预设：").FontSize(12).TextColor(ColorTextMuted).SingleLine()
		for _, item := range subscription.AllTrackingPresets() {
			p := item
			on := matchedPreset == p.ID && (p.ID != subscription.PresetManual || a.MultiTagBatch.Mode == subscription.TagBatchReplace)
			chip := toggleChipSized(c, p.Name, "批量选择预设："+p.ID, on, 3, 9, 12).Tooltip(p.Description)
			if chip.Clicked() {
				keepAutoRec := a.MultiTagBatch.SelectedTags[subscription.TagAutoRecord]
				a.MultiTagBatch.SelectedTags = make(map[string]bool, len(p.Tags)+1)
				for _, tid := range p.Tags {
					a.MultiTagBatch.SelectedTags[tid] = true
				}
				if keepAutoRec {
					a.MultiTagBatch.SelectedTags[subscription.TagAutoRecord] = true
				}
				if p.ID == subscription.PresetManual && a.MultiTagBatch.Mode != subscription.TagBatchReplace {
					a.MultiTagBatch.Mode = subscription.TagBatchReplace
				}
				a.MultiTagBatch.Error = ""
			}
		}
	})

	ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Wrap().Children(func() {
		for _, item := range allTags {
			tag := item
			on := a.MultiTagBatch.SelectedTags[tag.ID]
			chip := toggleChipSized(c, tag.Name, "批量选择标签："+tag.Name, on, 4, 10, 12).Tooltip(tag.Description)
			if chip.Clicked() {
				a.MultiTagBatch.SelectedTags[tag.ID] = !on
				a.MultiTagBatch.Error = ""
			}
		}
	})

	if a.MultiTagBatch.Mode != subscription.TagBatchRemove && subscription.HasAutoCacheWithoutScheduledCheck(selectedTagsSlice) {
		renderAutoCacheDependencyWarning(c, "批量设置自动缓存缺少定时检查提示", "", func() {
			if OutlinePillButton(c, "开启定时检查", "批量一键开启定时检查").Clicked() {
				a.MultiTagBatch.SelectedTags[subscription.TagScheduledCheck] = true
				a.MultiTagBatch.Error = ""
			}
		})
	} else if a.MultiTagBatch.Mode == subscription.TagBatchRemove && a.MultiTagBatch.SelectedTags[subscription.TagScheduledCheck] {
		warnRemove := false
		for _, sub := range a.Subscriptions {
			key := ruleBatchSubKey(sub)
			if !a.MultiTagBatch.SelectedSubs[key] {
				continue
			}
			var remaining []string
			for _, tid := range effectiveSubTags(sub) {
				if !a.MultiTagBatch.SelectedTags[tid] {
					remaining = append(remaining, tid)
				}
			}
			if subscription.HasAutoCacheWithoutScheduledCheck(remaining) {
				warnRemove = true
				break
			}
		}
		if warnRemove {
			renderAutoCacheDependencyWarning(c, "批量设置自动缓存缺少定时检查提示", "移除「定时检查」后，部分已开启自动缓存的订阅将仅在手动检查更新时生效。", func() {
				if OutlinePillButton(c, "保留定时检查", "批量保留定时检查").Clicked() {
					a.MultiTagBatch.SelectedTags[subscription.TagScheduledCheck] = false
					a.MultiTagBatch.Error = ""
				}
			})
		}
	}

	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		inp := ui.TextInput(c, &a.MultiTagBatch.SearchInput).
			Placeholder("搜索要批量设置的订阅…").
			Label("批量订阅搜索框").
			Grow(1)
		a.MultiTagBatch.SearchComposing = inp.Composing()
		if !a.MultiTagBatch.SearchComposing && a.MultiTagBatch.CommittedQuery != a.MultiTagBatch.SearchInput {
			a.MultiTagBatch.CommittedQuery = a.MultiTagBatch.SearchInput
			a.MultiTagBatch.ListState = ui.ListState{}
		}
		if a.MultiTagBatch.SearchInput != "" {
			if OutlinePillButton(c, "清空", "清空批量订阅搜索").Clicked() {
				a.MultiTagBatch.SearchInput = ""
				a.MultiTagBatch.CommittedQuery = ""
				a.MultiTagBatch.SearchComposing = false
				a.MultiTagBatch.ListState = ui.ListState{}
			}
		}
	})

	filtered := make([]subscription.Subscription, 0, len(a.Subscriptions))
	selectedSubCount := 0
	for _, sub := range a.Subscriptions {
		key := ruleBatchSubKey(sub)
		if key != "" && a.MultiTagBatch.SelectedSubs[key] {
			selectedSubCount++
		}
		if ruleBatchMatchesQuery(sub, a.MultiTagBatch.CommittedQuery) {
			filtered = append(filtered, sub)
		}
	}

	ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if OutlinePillButton(c, "全选", "批量订阅全选").Disabled(len(filtered) == 0).Clicked() {
				for _, sub := range filtered {
					if key := ruleBatchSubKey(sub); key != "" {
						a.MultiTagBatch.SelectedSubs[key] = true
					}
				}
				a.MultiTagBatch.Error = ""
			}
			if OutlinePillButton(c, "全不选", "批量订阅全不选").Disabled(len(filtered) == 0).Clicked() {
				for _, sub := range filtered {
					if key := ruleBatchSubKey(sub); key != "" {
						a.MultiTagBatch.SelectedSubs[key] = false
					}
				}
				a.MultiTagBatch.Error = ""
			}
		})
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if strings.TrimSpace(a.MultiTagBatch.CommittedQuery) != "" {
				ui.Text(c, fmt.Sprintf("匹配 %d 个", len(filtered))).FontSize(12).TextColor(ColorTextMuted).SingleLine()
			}
			ui.Text(c, fmt.Sprintf("已选 %d 个订阅", selectedSubCount)).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
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
			a.MultiTagBatch.ListState.Key = func(idx int) any {
				if idx < 0 || idx >= len(filtered) {
					return idx
				}
				return ruleBatchSubKey(filtered[idx])
			}
			ui.List(c, &a.MultiTagBatch.ListState, len(filtered), func(idx int) {
				if idx < 0 || idx >= len(filtered) {
					return
				}
				sub := filtered[idx]
				key := ruleBatchSubKey(sub)
				subName := firstNonEmptyStrUI(sub.Name, sub.Uname, key)
				checked := key != "" && a.MultiTagBatch.SelectedSubs[key]
				curTags := effectiveSubTags(sub)

				row := ui.CheckboxBase(c, &checked).
					Label("勾选批量订阅："+subName).
					FillWidth().
					Padding(9, 12).
					Gap(10).
					AlignItems(ui.Center).
					BorderWidth(0, 0, 1, 0).
					BorderColor(ColorBorder).
					ClipX()
				if row.Hovered() {
					row.Background(ColorHover)
				}
				row.Children(func() {
					renderRuleBatchCheckIndicator(c, checked, false)
					pSite, pUID := subscriptionPrimarySiteAndUID(sub)
					a.renderAccountAvatar(c, pSite, subName, pUID, sub.Mid, 32, false, "", nil, sub.Face)
					ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
						ui.Text(c, subName).FontSize(14).FontWeight(500).TextColor(ColorTextMain).SingleLine()
						if note := strings.TrimSpace(sub.Note); note != "" {
							ui.Text(c, note).FontSize(12).TextColor(ColorTextMuted).SingleLine()
						}
					})
					ui.Text(c, subscription.TagsSummary(curTags)).
						FontSize(12).TextColor(ColorTextMuted).SingleLine().Shrink(1)
				})
				if row.Changed() && key != "" {
					a.MultiTagBatch.SelectedSubs[key] = checked
					a.MultiTagBatch.Error = ""
				}
			}).Grow(1).FillWidth()
		}
	})

	if a.MultiTagBatch.Error != "" {
		renderErrorBanner(c, "", a.MultiTagBatch.Error, nil)
	}

	var applyText string
	switch a.MultiTagBatch.Mode {
	case subscription.TagBatchRemove:
		applyText = fmt.Sprintf("从 %d 个订阅移除标签", selectedSubCount)
	case subscription.TagBatchReplace:
		if selectedTagCount == 0 {
			applyText = fmt.Sprintf("清空 %d 个订阅的标签", selectedSubCount)
		} else {
			applyText = fmt.Sprintf("替换 %d 个订阅的标签", selectedSubCount)
		}
	default:
		applyText = fmt.Sprintf("为 %d 个订阅添加标签", selectedSubCount)
	}
	canApply := selectedSubCount > 0 && (a.MultiTagBatch.Mode == subscription.TagBatchReplace || selectedTagCount > 0)

	ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.End).Gap(8).Children(func() {
		if OutlinePillButton(c, "取消", "取消批量设置标签").Clicked() {
			a.closeMultiTagBatchDialog()
		}
		if AccentPillButton(c, applyText, "确认批量设置标签").Disabled(!canApply).Clicked() && canApply {
			a.applyMultiTagBatchSelection()
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

func (a *App) syncSelectedSubAfterTagBatch() {
	if subs, err := a.backend.ListSubscriptions(); err == nil {
		a.Subscriptions = subs
		a.subsRev++
		a.checkPlanAt = time.Time{}
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

func (a *App) applyRuleBatchSelection() {
	selectedIDs := make([]string, 0, len(a.Subscriptions))
	for _, sub := range a.Subscriptions {
		key := ruleBatchSubKey(sub)
		if key != "" && a.RuleBatch.Selected[key] {
			selectedIDs = append(selectedIDs, key)
		}
	}
	if err := a.backend.SetTagSubscriptions(a.RuleBatch.TagID, selectedIDs); err != nil {
		a.RuleBatch.Error = userErrorText(err)
		return
	}
	tagName := a.RuleBatch.TagName
	a.closeRuleBatchDialog()
	a.syncSelectedSubAfterTagBatch()
	a.BannerInfo = fmt.Sprintf("已更新标签「%s」的关联订阅（共 %d 个）", tagName, len(selectedIDs))
}

func (a *App) applyMultiTagBatchSelection() {
	selectedSubIDs := make([]string, 0, len(a.Subscriptions))
	for _, sub := range a.Subscriptions {
		key := ruleBatchSubKey(sub)
		if key != "" && a.MultiTagBatch.SelectedSubs[key] {
			selectedSubIDs = append(selectedSubIDs, key)
		}
	}
	var chosenTags []string
	for _, t := range a.backend.ListCapabilityTags() {
		if a.MultiTagBatch.SelectedTags[t.ID] {
			chosenTags = append(chosenTags, t.ID)
		}
	}
	mode := a.MultiTagBatch.Mode
	if err := a.backend.BatchUpdateSubscriptionsTags(selectedSubIDs, mode, chosenTags); err != nil {
		a.MultiTagBatch.Error = userErrorText(err)
		return
	}
	a.closeMultiTagBatchDialog()
	a.syncSelectedSubAfterTagBatch()
	switch mode {
	case subscription.TagBatchRemove:
		a.BannerInfo = fmt.Sprintf("已从 %d 个订阅移除所选缓存标签", len(selectedSubIDs))
	case subscription.TagBatchReplace:
		if len(chosenTags) == 0 {
			a.BannerInfo = fmt.Sprintf("已清空 %d 个订阅的全部缓存标签", len(selectedSubIDs))
		} else {
			a.BannerInfo = fmt.Sprintf("已更新 %d 个订阅的缓存标签", len(selectedSubIDs))
		}
	default:
		a.BannerInfo = fmt.Sprintf("已为 %d 个订阅添加所选缓存标签", len(selectedSubIDs))
	}
}
