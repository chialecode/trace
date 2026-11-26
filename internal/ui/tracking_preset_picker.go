package ui

import (
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

// TrackingPresetPickerState holds the user's tracking preset + overlayable
// auto-record + optional custom atomic tags selection when creating or
// importing new subscriptions (DEC-080). Its zero value defaults to "完整存档".
type TrackingPresetPickerState struct {
	Initialized    bool
	PresetID       string
	AutoRecord     bool
	CustomExpanded bool
	CustomTags     map[string]bool
}

func (s *TrackingPresetPickerState) ensureInit() {
	if s.Initialized {
		if s.CustomTags == nil {
			s.CustomTags = make(map[string]bool)
		}
		return
	}
	s.ResetDefault()
}

// ResetDefault sets the picker to the default "完整存档" preset with auto-record off.
func (s *TrackingPresetPickerState) ResetDefault() {
	s.Initialized = true
	s.PresetID = subscription.PresetFullArchive
	s.AutoRecord = false
	s.CustomExpanded = false
	s.syncCustomTagsFromPreset()
}

func (s *TrackingPresetPickerState) syncCustomTagsFromPreset() {
	tags, err := subscription.PresetTags(s.PresetID, s.AutoRecord)
	if err != nil {
		tags = subscription.DefaultNewSubTags()
	}
	m := make(map[string]bool, len(tags))
	for _, id := range tags {
		m[id] = true
	}
	s.CustomTags = m
}

// SelectPreset switches to one of the 4 concrete presets while preserving AutoRecord.
func (s *TrackingPresetPickerState) SelectPreset(presetID string) {
	s.ensureInit()
	if _, ok := subscription.TrackingPresetByID(presetID); !ok {
		return
	}
	s.PresetID = presetID
	s.CustomExpanded = false
	s.syncCustomTagsFromPreset()
}

// ToggleAutoRecord flips the overlayable "录直播" toggle.
func (s *TrackingPresetPickerState) ToggleAutoRecord() {
	s.ensureInit()
	s.AutoRecord = !s.AutoRecord
	s.CustomTags[subscription.TagAutoRecord] = s.AutoRecord
}

// ToggleCustomTag toggles one of the 7 atomic tags inside the expanded custom view
// and keeps PresetID / AutoRecord synchronized with the resulting tag set.
func (s *TrackingPresetPickerState) ToggleCustomTag(tagID string) {
	s.ensureInit()
	if !subscription.IsValidTag(tagID) {
		return
	}
	s.CustomTags[tagID] = !s.CustomTags[tagID]
	tags := s.collectCustomTags()
	s.PresetID, s.AutoRecord = subscription.MatchTrackingPreset(tags)
}

func (s *TrackingPresetPickerState) collectCustomTags() []string {
	var out []string
	for _, def := range subscription.AllCapabilityTags() {
		if s.CustomTags[def.ID] {
			out = append(out, def.ID)
		}
	}
	return subscription.NormalizeTags(out)
}

// SelectedTags returns the normalized atomic capability tags represented by s.
func (s *TrackingPresetPickerState) SelectedTags() []string {
	if s == nil || !s.Initialized {
		return subscription.DefaultNewSubTags()
	}
	if s.PresetID == subscription.PresetCustom || s.CustomExpanded {
		return s.collectCustomTags()
	}
	tags, err := subscription.PresetTags(s.PresetID, s.AutoRecord)
	if err != nil {
		return subscription.DefaultNewSubTags()
	}
	return tags
}

// SelectedRules maps SelectedTags to subscription.SubRules for subscription creation/import.
func (s *TrackingPresetPickerState) SelectedRules() subscription.SubRules {
	return subscription.TagsToSubRules(s.SelectedTags())
}

// renderTrackingPresetPicker draws the compact intent-based preset selector
// with overlayable "录直播" toggle, expandable 7 atomic tags ("自定义"), and
// automatic-cache dependency gap warning (DEC-080).
func renderTrackingPresetPicker(c *ui.Context, st *TrackingPresetPickerState, prefix, title string, compact bool) {
	st.ensureInit()
	selectedTags := st.SelectedTags()
	presets := subscription.AllTrackingPresets()

	padV, padH := float32(10), float32(12)
	chipPadV, chipPadH := float32(4), float32(10)
	if compact {
		padV, padH = 8, 10
		chipPadV, chipPadH = 3, 9
	}

	ui.Column(c).
		Label(prefix+"追踪模式选择区").
		FillWidth().
		Padding(padV, padH).
		Radius(12).
		Background(ColorBgMain).
		Border(1, ColorBorder).
		Gap(6).
		ClipX().
		Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Wrap().ClipX().Children(func() {
				ui.Text(c, title).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
				liveChip := toggleChipSized(c, "录直播", prefix+"录直播", st.AutoRecord, 3, 9, 12).
					Tooltip("发现哔哩哔哩或抖音账号正在直播时自动开始录制（可叠加在任意追踪模式上）")
				if liveChip.Clicked() {
					st.ToggleAutoRecord()
				}
			})

			ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Wrap().ClipX().Children(func() {
				for _, item := range presets {
					p := item
					chipText := p.Name
					if p.ID == subscription.PresetFullArchive {
						chipText = "完整存档（默认）"
					}
					on := !st.CustomExpanded && st.PresetID == p.ID
					chip := toggleChipSized(c, chipText, prefix+"预设："+p.ID, on, chipPadV, chipPadH, 12).
						Tooltip(p.Description)
					if chip.Clicked() {
						st.SelectPreset(p.ID)
					}
				}
				customOn := st.CustomExpanded || st.PresetID == subscription.PresetCustom
				customChip := toggleChipSized(c, "自定义", prefix+"自定义", customOn, chipPadV, chipPadH, 12).
					Tooltip("展开全部 7 个原子能力标签进行细粒度组合")
				if customChip.Clicked() {
					if !st.CustomExpanded {
						cur := st.SelectedTags()
						m := make(map[string]bool, len(cur))
						for _, id := range cur {
							m[id] = true
						}
						st.CustomTags = m
					}
					st.CustomExpanded = !st.CustomExpanded
				}
			})

			if !compact {
				desc := subscription.TagsSummary(selectedTags)
				if p, ok := subscription.TrackingPresetByID(st.PresetID); ok {
					desc = p.Description
					if st.AutoRecord {
						desc += "；同时开启开播自动录制"
					}
				}
				ui.Text(c, desc).FontSize(12).LineHeight(LineHeightCompact).TextColor(ColorTextMuted).MaxLines(2)
			}

			if st.CustomExpanded {
				ui.Column(c).
					Label(prefix+"自定义原子标签区").
					FillWidth().
					Padding(8, 0, 0, 0).
					BorderWidth(1, 0, 0, 0).
					BorderColor(ColorBorder).
					Gap(6).
					ClipX().
					Children(func() {
						ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Wrap().ClipX().Children(func() {
							for _, item := range subscription.AllCapabilityTags() {
								tag := item
								on := st.CustomTags[tag.ID]
								chip := toggleChipSized(c, tag.Name, prefix+"原子标签："+tag.ID, on, 3, 9, 12).
									Tooltip(tag.Description)
								if chip.Clicked() {
									st.ToggleCustomTag(tag.ID)
								}
							}
						})
					})
			}

			if subscription.HasAutoCacheWithoutScheduledCheck(selectedTags) {
				renderAutoCacheDependencyWarning(c, prefix+"自动缓存缺少定时检查提示", "", func() {
					if OutlinePillButton(c, "开启定时检查", prefix+"一键开启定时检查").Clicked() {
						st.ToggleCustomTag(subscription.TagScheduledCheck)
					}
				})
			}
		})
}
