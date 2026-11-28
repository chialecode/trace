package subscriptions

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/chialecode/trace/cmd/trace-mobile/internal/pagekit"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/theme"
	"github.com/chialecode/trace/internal/companionapi"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/mobile"
)

func (p *Page) openTagsEditor(sub companionapi.SubscriptionDTO) {
	p.tagsDraft = normalizeMobileTags(sub.Tags)
	p.tagsShowAdvanced = false
	p.tagsSaving = false
	p.tagsSaveErr = ""
	p.tagsSheetOpen = true
}

func (p *Page) saveDetailTags(h pagekit.Host, sub companionapi.SubscriptionDTO) {
	cli := h.Client()
	if cli == nil || p.tagsSaving {
		return
	}
	gen := p.sessionGen
	subID := sub.ID
	desired := normalizeMobileTags(p.tagsDraft)
	if desired == nil {
		// Non-nil empty slice explicitly requests "仅手动" (zero tags).
		desired = []string{}
	}
	p.tagsSaving = true
	p.tagsSaveErr = ""
	p.actionErr = ""
	p.actionInfo = ""

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		resp, err := cli.UpdateSubscriptionTags(ctx, subID, companionapi.UpdateSubscriptionTagsRequest{Tags: desired})
		h.Update(func() {
			if p.sessionGen != gen {
				return
			}
			p.tagsSaving = false
			if err != nil {
				p.tagsSaveErr = "保存缓存标签失败：" + formatActionError(err, "")
				return
			}
			updated := resp.Subscription
			if updated.ID == "" {
				updated = sub
				updated.Tags = desired
				updated.PresetLabel = mobilePresetLabelForTags(desired)
				updated.TagLabels = mobileTagLabels(desired)
			}
			if p.detailSubID == subID {
				cp := updated
				p.detailSub = &cp
			}
			for i := range p.subs {
				if p.subs[i].ID == subID {
					p.subs[i].Tags = updated.Tags
					p.subs[i].PresetLabel = updated.PresetLabel
					p.subs[i].TagLabels = updated.TagLabels
					break
				}
			}
			p.tagsSheetOpen = false
			p.tagsSaveErr = ""
			p.actionInfo = "已更新缓存标签：" + firstNonEmpty(updated.PresetLabel, mobilePresetLabelForTags(updated.Tags))
			p.loadCheckPlan(h)
		})
	}()
}

func (p *Page) renderTagsEditorSheet(c *ui.Context, h pagekit.Host, sub companionapi.SubscriptionDTO) {
	bg := theme.ColorBgMain
	mobile.Sheet(c, &p.tagsSheetOpen, mobile.SheetOptions{
		Detents:    []float32{0.78, 0.94},
		Background: &bg,
		Label:      "编辑订阅缓存标签",
	}, func() {
		draft := normalizeMobileTags(p.tagsDraft)
		activePresetID, autoRecordOn := matchMobileTrackingPreset(draft)
		activePresetName := mobilePresetLabelByID(activePresetID)
		depWarn := hasAutoCacheWithoutScheduledCheck(draft)
		hasLiveAccount := false
		for _, acc := range sub.Accounts {
			if isLiveRecordableSite(acc.Site) && acc.LiveSupported {
				hasLiveAccount = true
				break
			}
		}

		ui.Scroll(c).Fill().Children(func() {
			ui.Column(c).FillWidth().Padding(10, 16, 28, 16).Gap(12).ClipX().Children(func() {
				// Header row.
				ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
					ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
						ui.Text(c, "编辑缓存标签").FontSize(17).FontWeight(600).TextColor(theme.ColorTextMain).SingleLine()
						ui.Text(c, sub.Name).FontSize(12).TextColor(theme.ColorTextMuted).SingleLine()
					})
					closeBtn := ui.ButtonBase(c).Label("关闭标签编辑").Padding(6, 10).MinHeight(36).Radius(999).Background(theme.ColorBgSecondary).Shrink(0)
					closeBtn.Children(func() {
						ui.Text(c, "取消").FontSize(12).FontWeight(500).TextColor(theme.ColorTextMuted).SingleLine()
					})
					if closeBtn.Clicked() && !p.tagsSaving {
						p.tagsSheetOpen = false
					}
				})

				// 1. Tracking presets (4 presets matching desktop TrackingPresetPicker).
				ui.Column(c).FillWidth().Gap(6).ClipX().Children(func() {
					ui.Text(c, "追踪预设").FontSize(13).FontWeight(600).TextColor(theme.ColorTextMuted)
					for _, preset := range mobileTrackingPresets {
						pr := preset
						selected := activePresetID == pr.ID
						cardBg := theme.ColorBgMain
						borderCol := theme.ColorBorder
						titleCol := theme.ColorTextMain
						if selected {
							cardBg = theme.ColorAccentBg
							borderCol = theme.ColorAccent
							titleCol = theme.ColorAccent
						}
						btn := ui.Button(c.Key("preset-"+pr.ID), "").
							Label("选择追踪预设："+pr.Name).
							FillWidth().
							MinHeight(52).
							Padding(10, 12).
							Radius(10).
							Background(cardBg).
							Border(1, borderCol).
							Justify(ui.Start).
							AlignItems(ui.Center).
							Disabled(p.tagsSaving)
						btn.Children(func() {
							ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
								ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
									ui.Text(c, pr.Name).FontSize(14).FontWeight(600).TextColor(titleCol).SingleLine()
									ui.Text(c, pr.Description).FontSize(12).LineHeight(theme.LineHeightCompact).TextColor(theme.ColorTextMuted).MaxLines(2)
								})
								if selected {
									ui.Box(c).Padding(2, 8).Radius(999).Background(theme.ColorAccent).Shrink(0).Children(func() {
										ui.Text(c, "当前").FontSize(11).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()
									})
								}
							})
						})
						if btn.Clicked() && !p.tagsSaving {
							p.tagsDraft = applyMobilePreset(pr.ID, autoRecordOn)
							p.tagsSaveErr = ""
						}
					}
				})

				// 2. Overlayable "开播录制" toggle (preserves preset).
				ui.Row(c).
					FillWidth().
					Padding(12, 14).
					Radius(10).
					Background(theme.ColorBgPanel).
					Border(1, theme.ColorBorder).
					AlignItems(ui.Center).
					Justify(ui.SpaceBetween).
					Gap(10).
					ClipX().
					Children(func() {
						ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
							ui.Text(c, "开播录制").FontSize(14).FontWeight(600).TextColor(theme.ColorTextMain).SingleLine()
							hint := "可叠加在任意追踪预设之上，对 B 站 / 抖音关联账号开播时自动录制。"
							if !hasLiveAccount && len(sub.Accounts) > 0 {
								hint = "当前订阅暂无支持录制的 B 站或抖音账号，开启后不会对微博或 X 发直播请求。"
							}
							ui.Text(c, hint).FontSize(12).LineHeight(theme.LineHeightCompact).TextColor(theme.ColorTextMuted).MaxLines(2)
						})
						recVal := autoRecordOn
						if ui.Switch(c, &recVal).Label("开关开播录制").Disabled(p.tagsSaving).Changed() {
							p.tagsDraft = setMobileTag(draft, tagAutoRecord, recVal)
							p.tagsSaveErr = ""
						}
					})

				// 3. Gentle dependency warning when auto-cache is on without scheduled_check.
				if depWarn {
					ui.Column(c).
						Label("定时检查依赖提示").
						FillWidth().
						Padding(10, 12).
						Radius(10).
						Background(theme.ColorWarnBg).
						Border(1, ui.RGBA(243, 161, 21, 0.40)).
						Gap(8).
						ClipX().
						Children(func() {
							ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
								theme.DrawIcon(c, "alert-circle", 16, theme.ColorWarnText, 1.8)
								ui.Text(c, "已开启自动缓存，但未开启「定时检查」，仅在手动检查更新时才会自动缓存。").
									FontSize(12).LineHeight(theme.LineHeightCompact).TextColor(theme.ColorWarnText).MaxLines(3).Grow(1).Shrink(1)
							})
							if theme.SecondaryPillButton(c, "同时开启定时检查", "开启定时检查").Disabled(p.tagsSaving).Clicked() && !p.tagsSaving {
								p.tagsDraft = setMobileTag(draft, tagScheduledCheck, true)
								p.tagsSaveErr = ""
							}
						})
				}

				// 4. Expandable atomic capability tags fine-tuning.
				advLbl := "展开按能力细调（7 项标签） ▾"
				if p.tagsShowAdvanced {
					advLbl = "收起按能力细调 ▴"
				}
				advToggle := ui.ButtonBase(c).
					Label("展开或收起缓存能力细调").
					FillWidth().
					MinHeight(40).
					Padding(8, 12).
					Radius(10).
					Background(theme.ColorBgSecondary).
					Justify(ui.SpaceBetween).
					AlignItems(ui.Center)
				advToggle.Children(func() {
					ui.Text(c, advLbl).FontSize(13).FontWeight(500).TextColor(theme.ColorTextMain).SingleLine()
				})
				if advToggle.Clicked() {
					p.tagsShowAdvanced = !p.tagsShowAdvanced
				}

				if p.tagsShowAdvanced {
					ui.Column(c).FillWidth().Padding(8, 12).Radius(10).Background(theme.ColorBgPanel).Border(1, theme.ColorBorder).Gap(8).ClipX().Children(func() {
						for _, def := range mobileCapabilityTags {
							d := def
							on := hasMobileTag(draft, d.ID)
							ui.Row(c.Key("tag-" + d.ID)).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
								ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
									ui.Text(c, d.Name).FontSize(13).FontWeight(600).TextColor(theme.ColorTextMain).SingleLine()
									ui.Text(c, d.Description).FontSize(11).LineHeight(theme.LineHeightCompact).TextColor(theme.ColorTextMuted).MaxLines(2)
								})
								val := on
								if ui.Switch(c, &val).Label("切换标签：" + d.Name).Disabled(p.tagsSaving).Changed() {
									p.tagsDraft = setMobileTag(draft, d.ID, val)
									p.tagsSaveErr = ""
								}
							})
						}
					})
				}

				if p.tagsSaveErr != "" {
					ui.Text(c, p.tagsSaveErr).Label("保存标签错误提示").FontSize(12).LineHeight(theme.LineHeightCompact).TextColor(theme.ColorError).MaxLines(3)
				}

				// Save button.
				changed := !slices.Equal(normalizeMobileTags(sub.Tags), draft)
				saveText := "保存更改"
				if p.tagsSaving {
					saveText = "正在保存…"
				} else if !changed {
					saveText = fmt.Sprintf("保存（%s）", activePresetName)
				}
				if theme.PrimaryPillButton(c, saveText, "保存订阅缓存标签").FillWidth().Disabled(p.tagsSaving).Clicked() && !p.tagsSaving {
					p.saveDetailTags(h, sub)
				}
			})
		})
	})
}

func setMobileTag(tags []string, id string, on bool) []string {
	if hasMobileTag(tags, id) == on {
		return normalizeMobileTags(tags)
	}
	return toggleMobileTag(tags, id)
}

func mobilePresetLabelForTags(tags []string) string {
	id, _ := matchMobileTrackingPreset(tags)
	return mobilePresetLabelByID(id)
}

func mobilePresetLabelByID(id string) string {
	for _, p := range mobileTrackingPresets {
		if p.ID == id {
			return p.Name
		}
	}
	return "自定义"
}

func mobileTagLabels(tags []string) []string {
	norm := normalizeMobileTags(tags)
	out := make([]string, 0, len(norm))
	for _, t := range norm {
		if lbl := mobileTagLabel(t); lbl != "" {
			out = append(out, lbl)
		}
	}
	return out
}

func mobileTagLabel(id string) string {
	for _, d := range mobileCapabilityTags {
		if d.ID == id {
			return d.Name
		}
	}
	return strings.TrimSpace(id)
}
