package ui

import (
	"context"
	"fmt"
	"strings"

	"github.com/chialecode/trace/internal/service"
	"github.com/egoist/mygo/ui"
)

// ConflictDiffModalState holds UI state for the side-by-side content conflict diff modal (#86).
type ConflictDiffModalState struct {
	Open           bool
	ConflictID     string
	Loading        bool
	Error          string
	Diff           *service.CacheConflictDiffDTO
	ReplaceConfirm bool
}

var (
	colorDiffRemovedBg   = ui.RGBA(217, 31, 55, 0.14)
	colorDiffRemovedText = ui.RGB(180, 24, 42)
	colorDiffAddedBg     = ui.RGBA(10, 135, 84, 0.14)
	colorDiffAddedText   = ui.RGB(8, 110, 68)
	colorDiffChangedBg   = ui.RGBA(243, 161, 21, 0.16)
	colorDiffChangedText = ui.RGB(154, 91, 0)
	colorDiffUpgradeBg   = ui.RGBA(26, 115, 232, 0.12)
)

// OpenConflictDiffModal opens the side-by-side diff dialog for conflict and
// loads fresh remote comparison details asynchronously while showing local V1 immediately.
func (a *App) OpenConflictDiffModal(conflict service.CacheConflictDTO) {
	id := strings.TrimSpace(conflict.ID)
	if id == "" {
		return
	}
	initial := &service.CacheConflictDiffDTO{
		Conflict: conflict,
		Local: service.ConflictDiffSideDTO{
			Heading: "本地已缓存（旧版 V1）",
			Summary: firstNonEmptyStrUI(conflict.LocalSummary, "本机已缓存完整版本"),
			Title:   conflict.Title,
		},
		Remote: service.ConflictDiffSideDTO{
			Heading: "源站当前（新版 V2）",
			Summary: firstNonEmptyStrUI(conflict.RemoteSummary, "正在读取源站最新状态…"),
		},
	}
	a.ConflictDiff = ConflictDiffModalState{
		Open:       true,
		ConflictID: id,
		Loading:    a.backend != nil,
		Diff:       initial,
	}
	a.loadConflictDiff(id)
}

// OpenConflictDiffModalWithData opens the diff modal directly with precomputed data (used by tests & uishots).
func (a *App) OpenConflictDiffModalWithData(diff service.CacheConflictDiffDTO) {
	cp := diff
	a.ConflictDiff = ConflictDiffModalState{
		Open:       true,
		ConflictID: diff.Conflict.ID,
		Loading:    false,
		Diff:       &cp,
	}
}

func (a *App) closeConflictDiffModal() {
	a.ConflictDiff = ConflictDiffModalState{}
}

func (a *App) loadConflictDiff(id string) {
	if a.backend == nil || strings.TrimSpace(id) == "" {
		return
	}
	backend := a.backend
	a.ConflictDiff.Loading = true
	a.ConflictDiff.Error = ""
	requestGuarded(a, "conflict-diff:"+id, nil, func(ctx context.Context) (service.CacheConflictDiffDTO, error) {
		return backend.InspectCacheConflictDiff(ctx, id)
	}, func(dto service.CacheConflictDiffDTO) {
		if !a.ConflictDiff.Open || a.ConflictDiff.ConflictID != id {
			return
		}
		cp := dto
		a.ConflictDiff.Loading = false
		a.ConflictDiff.Error = ""
		a.ConflictDiff.Diff = &cp
	}, func(err error) {
		if !a.ConflictDiff.Open || a.ConflictDiff.ConflictID != id {
			return
		}
		a.backend.LogUserError(err)
		a.ConflictDiff.Loading = false
		a.ConflictDiff.Error = "读取对比详情失败：" + userErrorText(err)
	})
}

func (a *App) renderConflictDiffOverlay(c *ui.Context, w, h float32) {
	if !a.ConflictDiff.Open || a.ConflictDiff.Diff == nil {
		return
	}
	diff := a.ConflictDiff.Diff
	conflict := diff.Conflict
	siteID := normalizeSiteIDUI(conflict.Site)
	reasonText := firstNonEmptyStrUI(conflict.ReasonLabel, service.ConflictReasonLabel(conflict.ReasonKind))
	title := firstNonEmptyStrUI(conflict.Title, conflict.TargetID, conflict.ItemKey)

	resolving := conflict.State == service.ConflictStateResolving ||
		a.isPending("conflict-resolve:"+conflict.ID) ||
		a.isPending("conflict-batch")

	topClearance := float32(36)
	if a.titleBar.Height > 0 {
		topClearance = max(topClearance, a.titleBar.Height+12)
	}
	modalW := min(float32(940), max(float32(640), w-48))
	if w < 700 {
		modalW = max(float32(320), w-24)
	}
	modalH := min(float32(740), max(float32(420), h-topClearance-36))

	renderDialog(c, &a.ConflictDiff.Open,
		"关闭内容冲突对比弹窗（点击空白处）",
		"内容冲突对比弹窗",
		modalW, modalH, a.closeConflictDiffModal,
		func() {
			// Header
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Shrink(0).ClipX().Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(8).Grow(1).Shrink(1).ClipX().Children(func() {
					ui.Box(c).
						Label("对比弹窗冲突原因").
						Padding(2, 8).
						Radius(6).
						Background(ColorWarnBg).
						Border(1, ui.RGBA(243, 161, 21, 0.35)).
						Shrink(0).
						Children(func() {
							ui.Text(c, reasonText).FontSize(11).FontWeight(600).TextColor(ColorTextMain).SingleLine()
						})
					ui.Text(c, "差异对比 · "+title).
						FontSize(16).
						FontWeight(600).
						TextColor(ColorTextMain).
						SingleLine().
						Shrink(1)
					renderSiteSmallBadgeWithDot(c, siteID, "对比弹窗站点")
				})
				ui.Row(c).AlignItems(ui.Center).Gap(6).Shrink(0).Children(func() {
					refreshText := "刷新对比"
					if a.ConflictDiff.Loading {
						refreshText = "刷新中…"
					}
					if OutlinePillButton(c, refreshText, "刷新冲突差异对比").Disabled(a.ConflictDiff.Loading).Clicked() && !a.ConflictDiff.Loading {
						a.loadConflictDiff(conflict.ID)
					}
					if OutlinePillButton(c, "关闭", "关闭内容冲突对比弹窗").Clicked() {
						a.closeConflictDiffModal()
					}
				})
			})

			// Pinned Side-by-Side Column Headers
			ui.Row(c).Label("内容冲突并列栏头").FillWidth().Gap(10).Shrink(0).ClipX().Children(func() {
				renderConflictDiffColumnHeader(c, "本地已缓存（旧版 V1）", firstNonEmptyStrUI(diff.Local.Summary, conflict.LocalSummary), false, "", "左侧列头：本地已缓存")
				renderConflictDiffColumnHeader(c, "源站当前（新版 V2）", firstNonEmptyStrUI(diff.Remote.Summary, conflict.RemoteSummary), diff.Remote.Unavailable, diff.Remote.Note, "右侧列头：源站当前")
			})

			// Loading / Error banners
			if a.ConflictDiff.Loading {
				ui.Row(c).Label("冲突对比加载提示").FillWidth().Padding(8, 12).Radius(10).Background(ColorBgSecondary).AlignItems(ui.Center).Gap(8).Shrink(0).Children(func() {
					ui.Text(c, "正在拉取源站最新文字与媒体信息，下方先展示本机已缓存内容…").
						FontSize(12).TextColor(ColorTextMuted).MaxLines(2)
				})
			}
			if a.ConflictDiff.Error != "" {
				renderErrorBanner(c, "冲突对比加载失败", a.ConflictDiff.Error, func() {
					if OutlinePillButton(c, "重试", "重试加载冲突对比").Clicked() {
						a.loadConflictDiff(conflict.ID)
					}
				})
			} else if strings.TrimSpace(diff.RemoteError) != "" {
				renderErrorBanner(c, "源站对比受限提示", diff.RemoteError, nil)
			}

			// Scrollable Side-by-Side Body
			ui.Scroll(c).Label("内容冲突对比主体").Grow(1).FillWidth().ClipX().Children(func() {
				ui.Column(c).FillWidth().Gap(12).Padding(0, 2, 4, 2).ClipX().Children(func() {
					hasTitle := len(diff.LocalTitle) > 0 || len(diff.RemoteTitle) > 0 || strings.TrimSpace(diff.Local.Title) != "" || strings.TrimSpace(diff.Remote.Title) != ""
					hasText := len(diff.LocalText) > 0 || len(diff.RemoteText) > 0 || strings.TrimSpace(diff.Local.Text) != "" || strings.TrimSpace(diff.Remote.Text) != ""

					if hasTitle || hasText {
						a.renderConflictTextDiffSection(c, diff, hasTitle, hasText)
					}

					if len(diff.MediaPairs) > 0 {
						a.renderConflictMediaDiffSection(c, diff)
					} else if !a.ConflictDiff.Loading {
						ui.Box(c).FillWidth().Padding(10, 12).Radius(10).Background(ColorBgSecondary).Children(func() {
							ui.Text(c, "该条目无图片或视频附件差异（纯文字或元信息变更）。").
								FontSize(12).TextColor(ColorTextMuted).MaxLines(2)
						})
					}
				})
			})

			// Optional inline confirmation for destructive replacement inside the modal
			if a.ConflictDiff.ReplaceConfirm && !resolving {
				ui.Row(c).
					Label("对比弹窗确认替换栏").
					FillWidth().
					Padding(8, 12).
					Radius(10).
					Background(ColorWarnBg).
					Border(1, ColorDotYellow).
					AlignItems(ui.Center).
					Justify(ui.SpaceBetween).
					Gap(8).
					Wrap().
					Shrink(0).
					ClipX().
					Children(func() {
						ui.Text(c, "确认用源站新版替换本地已缓存内容？将先校验新版，若源站失效会自动拦截，替换成功后本地旧版将被移除。").
							FontSize(12).FontWeight(600).TextColor(ColorTextMain).MaxLines(2).Grow(1).Shrink(1)
						ui.Row(c).Gap(6).AlignItems(ui.Center).Shrink(0).Children(func() {
							if OutlinePillButton(c, "取消", "对比弹窗取消替换").Clicked() {
								a.ConflictDiff.ReplaceConfirm = false
							}
							if DangerOutlinePillButton(c, "确认替换", "对比弹窗确认替换").Clicked() {
								a.ConflictDiff.ReplaceConfirm = false
								a.closeConflictDiffModal()
								a.startResolveConflict(conflict.ID, service.ConflictUseRemote)
							}
						})
					})
			}

			// Footer resolution actions (max 1 AccentPillButton!)
			ui.Row(c).
				Label("内容冲突对比底部操作栏").
				FillWidth().
				AlignItems(ui.Center).
				Justify(ui.SpaceBetween).
				Gap(8).
				Wrap().
				Shrink(0).
				BorderWidth(1, 0, 0, 0).
				BorderColor(ColorBorder).
				Padding(10, 0, 0, 0).
				ClipX().
				Children(func() {
					ui.Row(c).Gap(6).AlignItems(ui.Center).Wrap().Children(func() {
						if OutlinePillButton(c, "查看本地详情", "对比弹窗查看本地详情").Disabled(resolving).Clicked() && !resolving {
							itemKey := conflict.ItemKey
							a.closeConflictDiffModal()
							a.openConflictLocalItem(itemKey)
						}
					})
					ui.Row(c).Gap(6).AlignItems(ui.Center).Wrap().Children(func() {
						if OutlinePillButton(c, "保留本地", "对比弹窗保留本地").Disabled(resolving).Clicked() && !resolving {
							a.closeConflictDiffModal()
							a.startResolveConflict(conflict.ID, service.ConflictKeepLocal)
						}
						if DangerOutlinePillButton(c, "用新版替换", "对比弹窗用新版替换").Disabled(resolving).Clicked() && !resolving {
							a.ConflictDiff.ReplaceConfirm = true
						}
						keepText := "保留两版（推荐）"
						if resolving {
							keepText = "处理中…"
						}
						if AccentPillButton(c, keepText, "对比弹窗保留两版").Disabled(resolving).Clicked() && !resolving {
							a.closeConflictDiffModal()
							a.startResolveConflict(conflict.ID, service.ConflictKeepBoth)
						}
					})
				})
		},
	)
}

func renderConflictDiffColumnHeader(c *ui.Context, heading, summary string, unavailable bool, note, label string) {
	border := ColorBorder
	bg := ColorBgSecondary
	if unavailable {
		border = ui.RGBA(180, 35, 24, 0.28)
		bg = ColorErrorBg
	}
	ui.Column(c).
		Label(label).
		Grow(1).
		Shrink(1).
		Padding(8, 12).
		Radius(10).
		Background(bg).
		Border(1, border).
		Gap(3).
		ClipX().
		Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(6).ClipX().Children(func() {
				ui.Text(c, heading).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine().Shrink(1)
				if unavailable {
					ui.Text(c, "源站失效/受限").FontSize(11).FontWeight(600).TextColor(ColorError).SingleLine().Shrink(0)
				}
			})
			sub := summary
			if unavailable && strings.TrimSpace(note) != "" {
				sub = note
			}
			ui.Text(c, sub).FontSize(12).TextColor(ColorTextMuted).MaxLines(2)
		})
}

func (a *App) renderConflictTextDiffSection(c *ui.Context, diff *service.CacheConflictDiffDTO, hasTitle, hasText bool) {
	ui.Column(c).Label("文字差异对比区").FillWidth().Gap(8).ClipX().Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Wrap().ClipX().Children(func() {
			ui.Text(c, "文字差异对比").FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
			ui.Row(c).AlignItems(ui.Center).Gap(6).Wrap().Children(func() {
				if diff.TitleChanged || diff.TextChanged {
					renderDiffLegendBadge(c, "淡红：本地旧文字", colorDiffRemovedBg, colorDiffRemovedText)
					renderDiffLegendBadge(c, "淡绿：源站新文字", colorDiffAddedBg, colorDiffAddedText)
				} else {
					renderDiffLegendBadge(c, "文字内容一致", ColorBgSecondary, ColorTextMuted)
				}
			})
		})

		if hasTitle {
			localTitle := diff.LocalTitle
			if len(localTitle) == 0 && diff.Local.Title != "" {
				localTitle = []service.ConflictTextSegment{{Text: diff.Local.Title, Kind: "same"}}
			}
			remoteTitle := diff.RemoteTitle
			if len(remoteTitle) == 0 && diff.Remote.Title != "" {
				remoteTitle = []service.ConflictTextSegment{{Text: diff.Remote.Title, Kind: "same"}}
			}
			renderSideBySideTextDiffPair(c, "标题", localTitle, remoteTitle, diff.TitleChanged, diff.Remote.Unavailable, "标题文字对比")
		}

		if hasText {
			localText := diff.LocalText
			if len(localText) == 0 && diff.Local.Text != "" {
				localText = []service.ConflictTextSegment{{Text: diff.Local.Text, Kind: "same"}}
			}
			remoteText := diff.RemoteText
			if len(remoteText) == 0 && diff.Remote.Text != "" {
				remoteText = []service.ConflictTextSegment{{Text: diff.Remote.Text, Kind: "same"}}
			}
			renderSideBySideTextDiffPair(c, "正文 / 简介", localText, remoteText, diff.TextChanged, diff.Remote.Unavailable, "正文文字对比")
		}
	})
}

func renderDiffLegendBadge(c *ui.Context, text string, bg, fg ui.Color) {
	ui.Box(c).Padding(2, 7).Radius(6).Background(bg).Shrink(0).Children(func() {
		ui.Text(c, text).FontSize(11).FontWeight(500).TextColor(fg).SingleLine()
	})
}

func renderSideBySideTextDiffPair(c *ui.Context, fieldName string, localSegs, remoteSegs []service.ConflictTextSegment, changed, remoteUnavailable bool, label string) {
	ui.Column(c).Label(label).FillWidth().Gap(4).ClipX().Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
			ui.Text(c, fieldName).FontSize(12).FontWeight(600).TextColor(ColorTextMuted).SingleLine()
			if changed {
				renderDiffLegendBadge(c, "有差异", colorDiffChangedBg, colorDiffChangedText)
			} else {
				renderDiffLegendBadge(c, "一致", ColorBgSecondary, ColorTextMuted)
			}
		})
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Stretch).ClipX().Children(func() {
			// Left: Local V1
			ui.Column(c).
				Label(label+"（本地）").
				Grow(1).
				Shrink(1).
				Padding(10).
				Radius(10).
				Background(ColorBgMain).
				Border(1, ColorBorder).
				Gap(4).
				ClipX().
				Children(func() {
					if len(localSegs) == 0 {
						ui.Text(c, "（空）").FontSize(12).TextColor(ColorTextMuted).SingleLine()
					} else {
						renderHighlightedDiffText(c, localSegs)
					}
				})

			// Right: Remote V2
			ui.Column(c).
				Label(label+"（源站）").
				Grow(1).
				Shrink(1).
				Padding(10).
				Radius(10).
				Background(ColorBgMain).
				Border(1, ColorBorder).
				Gap(4).
				ClipX().
				Children(func() {
					if remoteUnavailable {
						ui.Text(c, "（源站不可用或已失效）").FontSize(12).TextColor(ColorError).SingleLine()
					} else if len(remoteSegs) == 0 {
						ui.Text(c, "（空）").FontSize(12).TextColor(ColorTextMuted).SingleLine()
					} else {
						renderHighlightedDiffText(c, remoteSegs)
					}
				})
		})
	})
}

// renderHighlightedDiffText splits diff segments by newline and renders each line
// as tightly wrapped inline spans with soft red/green highlight pills on changed parts.
func renderHighlightedDiffText(c *ui.Context, segs []service.ConflictTextSegment) {
	type lineSpan struct {
		text string
		kind string
	}
	var lines [][]lineSpan
	var cur []lineSpan
	for _, seg := range segs {
		parts := strings.Split(strings.ReplaceAll(seg.Text, "\r\n", "\n"), "\n")
		for idx, p := range parts {
			if p != "" {
				cur = append(cur, lineSpan{text: p, kind: seg.Kind})
			}
			if idx < len(parts)-1 {
				lines = append(lines, cur)
				cur = nil
			}
		}
	}
	if len(cur) > 0 || len(lines) == 0 {
		lines = append(lines, cur)
	}

	for _, line := range lines {
		if len(line) == 0 {
			ui.Box(c).Height(6)
			continue
		}
		ui.Row(c).FillWidth().Wrap().AlignItems(ui.Center).Gap(2).ClipX().Children(func() {
			for _, sp := range line {
				// Break very long unchanged/changed runs into short visual chunks so wrapped rows never clip horizontally.
				for _, chunk := range chunkDiffDisplayText(sp.text, 24) {
					switch sp.kind {
					case "removed":
						ui.Box(c).Padding(1, 4).Radius(4).Background(colorDiffRemovedBg).Shrink(1).ClipX().Children(func() {
							ui.Text(c, chunk).FontSize(13).FontWeight(600).TextColor(colorDiffRemovedText).LineHeight(LineHeightCompact)
						})
					case "added":
						ui.Box(c).Padding(1, 4).Radius(4).Background(colorDiffAddedBg).Shrink(1).ClipX().Children(func() {
							ui.Text(c, chunk).FontSize(13).FontWeight(600).TextColor(colorDiffAddedText).LineHeight(LineHeightCompact)
						})
					default:
						ui.Text(c, chunk).FontSize(13).TextColor(ColorTextMain).LineHeight(LineHeightCompact).Shrink(1)
					}
				}
			}
		})
	}
}

func chunkDiffDisplayText(s string, maxRunes int) []string {
	runes := []rune(s)
	if len(runes) <= maxRunes || maxRunes <= 0 {
		return []string{s}
	}
	var out []string
	for start := 0; start < len(runes); {
		end := min(len(runes), start+maxRunes)
		// Prefer splitting on space when available near the end of an ASCII chunk.
		if end < len(runes) {
			for k := end; k > start+maxRunes/2; k-- {
				if runes[k-1] == ' ' {
					end = k
					break
				}
			}
		}
		out = append(out, string(runes[start:end]))
		start = end
	}
	return out
}

func (a *App) renderConflictMediaDiffSection(c *ui.Context, diff *service.CacheConflictDiffDTO) {
	ui.Column(c).Label("图片与视频并行视图区").FillWidth().Gap(8).ClipX().Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Wrap().ClipX().Children(func() {
			ui.Text(c, fmt.Sprintf("图片与视频并列对比（共 %d 项）", len(diff.MediaPairs))).
				FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
			ui.Row(c).AlignItems(ui.Center).Gap(6).Wrap().Children(func() {
				renderDiffLegendBadge(c, "已变更", colorDiffChangedBg, colorDiffChangedText)
				renderDiffLegendBadge(c, "仅本地有", colorDiffRemovedBg, colorDiffRemovedText)
				renderDiffLegendBadge(c, "源站新增", colorDiffAddedBg, colorDiffAddedText)
				renderDiffLegendBadge(c, "相同", ColorBgSecondary, ColorTextMuted)
			})
		})

		for _, pair := range diff.MediaPairs {
			a.renderConflictMediaPairRow(c, pair)
		}
	})
}

func conflictPairStatusStyle(status string) (bg, fg, border ui.Color) {
	switch status {
	case service.ConflictDiffStatusChanged:
		return colorDiffChangedBg, colorDiffChangedText, ui.RGBA(243, 161, 21, 0.45)
	case service.ConflictDiffStatusLocalOnly:
		return colorDiffRemovedBg, colorDiffRemovedText, ui.RGBA(217, 31, 55, 0.35)
	case service.ConflictDiffStatusRemoteOnly:
		return colorDiffAddedBg, colorDiffAddedText, ui.RGBA(10, 135, 84, 0.35)
	case service.ConflictDiffStatusUpgradeable:
		return colorDiffUpgradeBg, ColorAccent, ui.RGBA(26, 115, 232, 0.35)
	default:
		return ColorBgSecondary, ColorTextMuted, ColorBorder
	}
}

func (a *App) renderConflictMediaPairRow(c *ui.Context, pair service.ConflictMediaPairDTO) {
	stBg, stFg, stBorder := conflictPairStatusStyle(pair.Status)
	statusText := firstNonEmptyStrUI(pair.StatusText, "已变更")
	kindLabel := "图片"
	if pair.Kind == "video" {
		kindLabel = "视频"
	}

	ui.Column(c).
		Label(fmt.Sprintf("媒体并列对比项：%d", pair.Index)).
		FillWidth().
		Padding(10).
		Radius(12).
		Background(ColorBgMain).
		Border(1, stBorder).
		Gap(8).
		ClipX().
		Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
				ui.Text(c, fmt.Sprintf("#%d · %s", pair.Index, kindLabel)).
					FontSize(12).FontWeight(600).TextColor(ColorTextMuted).SingleLine()
				ui.Box(c).Padding(2, 8).Radius(6).Background(stBg).Shrink(0).Children(func() {
					ui.Text(c, statusText).FontSize(11).FontWeight(600).TextColor(stFg).SingleLine()
				})
			})

			ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Stretch).ClipX().Children(func() {
				a.renderConflictMediaSideCard(c, pair.Local, false, pair.Status, fmt.Sprintf("媒体并列项 %d（本地）", pair.Index))
				a.renderConflictMediaSideCard(c, pair.Remote, true, pair.Status, fmt.Sprintf("媒体并列项 %d（源站）", pair.Index))
			})
		})
}

func (a *App) renderConflictMediaSideCard(c *ui.Context, side *service.ConflictMediaSideDTO, isRemote bool, pairStatus, label string) {
	if side == nil {
		msg := "本地无此项"
		bg := ColorBgSecondary
		fg := ColorTextMuted
		if isRemote {
			msg = "源站已移除或不可用（本地保留有效文件）"
			bg = colorDiffRemovedBg
			fg = colorDiffRemovedText
		}
		ui.Box(c).
			Label(label).
			Grow(1).
			Shrink(1).
			Padding(12).
			Radius(10).
			Background(bg).
			Border(1, ColorBorder).
			AlignItems(ui.Center).
			Justify(ui.Center).
			ClipX().
			Children(func() {
				ui.Text(c, msg).FontSize(12).TextColor(fg).MaxLines(2)
			})
		return
	}

	cardBg := ColorBgSecondary
	if isRemote && pairStatus == service.ConflictDiffStatusRemoteOnly {
		cardBg = colorDiffAddedBg
	} else if !isRemote && pairStatus == service.ConflictDiffStatusLocalOnly {
		cardBg = colorDiffRemovedBg
	}

	isVideo := side.Kind == "video"
	durText := ""
	if side.DurationSec > 0 {
		durText = FormatDuration(side.DurationSec)
	}

	ui.Row(c).
		Label(label).
		Grow(1).
		Shrink(1).
		Padding(8).
		Radius(10).
		Background(cardBg).
		AlignItems(ui.Center).
		Gap(10).
		ClipX().
		Children(func() {
			var thumb ui.Element
			if !isRemote {
				thumb = a.renderMediaThumbEdge(c, side.LocalItemKey, side.LocalRelPath, gridThumbEdge, side.Label, durText, "", isVideo, 112, 72)
			} else {
				var urls []string
				if strings.TrimSpace(side.RemoteURL) != "" {
					urls = append(urls, side.RemoteURL)
				}
				thumb = a.renderMediaThumbEdge(c, "", "", gridThumbEdge, side.Label, durText, "", isVideo, 112, 72, urls...)
			}
			if thumb.Clicked() {
				a.openConflictMediaPreview(*side, isRemote)
			}

			ui.Column(c).Grow(1).Shrink(1).AlignItems(ui.Start).Gap(4).ClipX().Children(func() {
				ui.Text(c, firstNonEmptyStrUI(side.Label, "媒体附件")).
					FontSize(13).FontWeight(600).TextColor(ColorTextMain).MaxLines(2)
				if strings.TrimSpace(side.Sublabel) != "" {
					ui.Text(c, side.Sublabel).
						FontSize(11).TextColor(ColorTextMuted).MaxLines(2)
				}
				if !isRemote && isVideo && side.PlayRelPath != "" && side.LocalItemKey != "" {
					if OutlinePillButton(c, "播放本地", label+"播放本地").Clicked() {
						a.openConflictMediaPreview(*side, false)
					}
				} else if !isRemote && !isVideo && side.LocalRelPath != "" && side.LocalItemKey != "" {
					if OutlinePillButton(c, "查看大图", label+"查看大图").Clicked() {
						a.openConflictMediaPreview(*side, false)
					}
				} else if isRemote && !isVideo && strings.TrimSpace(side.RemoteURL) != "" {
					if OutlinePillButton(c, "预览原图", label+"预览原图").Clicked() {
						a.openConflictMediaPreview(*side, true)
					}
				}
			})
		})
}

func (a *App) openConflictMediaPreview(side service.ConflictMediaSideDTO, isRemote bool) {
	if !isRemote {
		if side.Kind == "video" && side.LocalItemKey != "" && a.backend != nil {
			if item, ok, err := a.backend.GetLibraryItem(side.LocalItemKey); err == nil && ok {
				if side.PlayRelPath != "" {
					a.openPlayerFile(item, side.PlayRelPath)
				} else {
					a.OpenPlayerOverlay(item)
				}
			}
			return
		}
		if side.LocalItemKey != "" && side.LocalRelPath != "" {
			a.OpenItemImageViewer(side.LocalItemKey, nil, []string{side.LocalRelPath}, 0)
		}
		return
	}
	if strings.TrimSpace(side.RemoteURL) != "" {
		a.OpenImageViewer([]string{side.RemoteURL}, nil, 0)
	}
}
