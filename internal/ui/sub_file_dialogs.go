package ui

import (
	"context"
	"fmt"

	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

// SubFileUIState is 订阅 › ⋯ › 导出订阅… / 导入订阅… (remote.md §6.4).
type SubFileUIState struct {
	ExportOpen bool
	ExportPath string
	ImportOpen bool
	ImportPath string
	Preview    *subscription.ImportPreview
	Merge      bool
	MergeTags  bool
	Err        string
}

func (a *App) openSubExport() {
	a.subExportGen++
	a.SubFile = SubFileUIState{ExportOpen: true, ExportPath: a.SubFile.ExportPath, ImportPath: a.SubFile.ImportPath}
}

func (a *App) closeSubExport() {
	a.subExportGen++
	a.SubFile.ExportOpen = false
	a.SubFile.Err = ""
}

func (a *App) openSubImport() {
	a.subImportGen++
	a.SubFile = SubFileUIState{ImportOpen: true, ExportPath: a.SubFile.ExportPath, ImportPath: a.SubFile.ImportPath}
}

func (a *App) closeSubImport() {
	a.subImportGen++
	a.SubFile.ImportOpen = false
	a.SubFile.Preview = nil
	a.SubFile.Err = ""
}

func (a *App) renderSubExportOverlay(c *ui.Context, w, h float32) {
	s := &a.SubFile
	cardW := min(float32(500), max(float32(320), w-40))
	renderDialog(c, &s.ExportOpen, "关闭导出订阅对话框（点击空白处）", "导出订阅对话框", cardW, 0, a.closeSubExport, func() {
		ui.Text(c, "导出订阅").FontSize(18).FontWeight(600).TextColor(ColorTextMain).SingleLine()
		ui.Text(c, "写一个 trace-subscriptions.json：订阅、关联账号、缓存标签和部分设置，不含 Cookie。填文件夹时文件名自动补上。").
			FontSize(12).TextColor(ColorTextMuted).MaxLines(3)
		picking := a.fileDialogBusy()
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).ClipX().Children(func() {
			ui.TextInput(c, &s.ExportPath).Label("订阅导出位置").Placeholder(`例如 D:\backup\trace-subscriptions.json`).Grow(1)
			if OutlinePillButton(c, "选择位置…", "选择订阅导出位置").Shrink(0).Disabled(picking).Clicked() && !picking {
				a.pickSubExportFile()
			}
		})
		if s.Err != "" {
			ui.Text(c, s.Err).FontSize(12).TextColor(ColorError).MaxLines(3)
		}
		ui.Row(c).FillWidth().Gap(8).Justify(ui.End).Children(func() {
			if OutlinePillButton(c, "取消", "取消导出订阅").Clicked() {
				a.closeSubExport()
			}
			if AccentPillButton(c, "导出", "确认导出订阅").Clicked() {
				written, err := a.backend.ExportSubscriptions(s.ExportPath)
				if err != nil {
					s.Err = userErrorText(err)
				} else {
					a.closeSubExport()
					a.BannerInfo = "订阅已导出到 " + written
				}
			}
		})
	})
}

func (a *App) previewSubImport() {
	s := &a.SubFile
	backend := a.backend
	path := s.ImportPath
	gen := a.subImportGen
	current := func() bool {
		return a.SubFile.ImportOpen && a.subImportGen == gen && a.SubFile.ImportPath == path
	}
	requestGuarded(a, "sub-import-preview", current, func(context.Context) (subscription.ImportPreview, error) {
		return backend.PreviewSubscriptionImport(path)
	}, func(p subscription.ImportPreview) {
		s.Preview, s.Err, s.Merge, s.MergeTags = &p, "", false, false
	}, func(err error) {
		s.Preview, s.Err = nil, userErrorText(err)
	})
}

func (a *App) applySubImport() {
	s := &a.SubFile
	choices := subscription.ImportChoices{
		MergeExisting: s.Merge,
		MergeTags:     s.Merge && s.MergeTags,
	}
	backend := a.backend
	path := s.ImportPath
	gen := a.subImportGen
	current := func() bool {
		return a.SubFile.ImportOpen && a.subImportGen == gen && a.SubFile.ImportPath == path
	}
	requestGuarded(a, "sub-import-apply", current, func(context.Context) (subscription.ImportPreview, error) {
		return backend.ApplySubscriptionImport(path, choices)
	}, func(p subscription.ImportPreview) {
		a.subImportGen++
		s.ImportOpen, s.Preview, s.Err = false, nil, ""
		a.refreshSubscriptions()
		a.BannerInfo = fmt.Sprintf("已导入订阅：新增 %d 个", len(p.New))
	}, func(err error) {
		s.Err = userErrorText(err)
	})
}

func (a *App) renderSubImportOverlay(c *ui.Context, w, h float32) {
	s := &a.SubFile
	cardW := min(float32(580), max(float32(320), w-40))
	maxListH := max(float32(120), min(float32(420), h-300))
	renderDialog(c, &s.ImportOpen, "关闭导入订阅对话框（点击空白处）", "导入订阅对话框", cardW, 0, a.closeSubImport, func() {
		ui.Text(c, "导入订阅").FontSize(18).FontWeight(600).TextColor(ColorTextMain).SingleLine()
		previewing := a.isPending("sub-import-preview")
		applying := a.isPending("sub-import-apply")
		picking := a.fileDialogBusy()
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).ClipX().Children(func() {
			if ui.TextInput(c, &s.ImportPath).Label("订阅文件路径").Placeholder(`trace-subscriptions.json 或它所在的文件夹`).Grow(1).Changed() {
				a.subImportGen++
				s.Preview = nil
				s.Err = ""
			}
			if OutlinePillButton(c, "选择文件…", "选择订阅文件").Shrink(0).Disabled(previewing || applying || picking).Clicked() && !previewing && !applying && !picking {
				a.pickSubImportFile()
			}
			text := "预览"
			if previewing {
				text = "正在读取…"
			}
			if OutlinePillButton(c, text, "预览导入订阅").Shrink(0).Disabled(previewing || applying).Clicked() && !previewing && !applying {
				a.previewSubImport()
			}
		})
		if p := s.Preview; p != nil {
			ui.Scroll(c).FillWidth().MaxHeight(maxListH).Children(func() {
				ui.Column(c).FillWidth().Gap(12).ClipX().Children(func() {
					a.renderSubImportPreview(c, p)
				})
			})
		}
		if s.Err != "" {
			ui.Text(c, s.Err).FontSize(12).TextColor(ColorError).MaxLines(3)
		}
		ui.Row(c).FillWidth().Gap(8).Justify(ui.End).Children(func() {
			if OutlinePillButton(c, "取消", "取消导入订阅").Clicked() {
				a.closeSubImport()
			}
			if s.Preview != nil {
				applyText := "确认导入"
				if applying {
					applyText = "正在导入…"
				}
				if AccentPillButton(c, applyText, "确认导入订阅").Disabled(applying).Clicked() && !applying {
					a.applySubImport()
				}
			}
		})
	})
}

func (a *App) renderSubImportPreview(c *ui.Context, p *subscription.ImportPreview) {
	s := &a.SubFile
	ui.Text(c, fmt.Sprintf("新订阅 %d 个 · 已订阅的账号 %d 个 · 已全部订阅 %d 个",
		len(p.New), len(p.Existing), p.Unchanged)).FontSize(13).FontWeight(600).TextColor(ColorTextMain).MaxLines(2)
	if len(p.New) > 0 {
		ui.Column(c).FillWidth().Gap(4).ClipX().Children(func() {
			ui.Text(c, "新订阅").FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
			for _, sub := range p.New {
				summary := firstNonEmptyStrUI(sub.TagsSummary, subscription.TagsSummary(sub.Tags))
				line := fmt.Sprintf("%s · %d 个账号 · %s", sub.Name, len(sub.Accounts), summary)
				ui.Text(c, line).FontSize(12).TextColor(ColorTextMuted).SingleLine()
			}
		})
	}
	if len(p.Existing) > 0 {
		ui.Column(c).FillWidth().Gap(4).ClipX().Children(func() {
			ui.Text(c, "已在订阅里的账号").FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
			for _, acc := range p.Existing {
				ui.Text(c, fmt.Sprintf("%s（%s）· 本地在“%s”", firstNonEmptyStrUI(acc.Name, acc.UID), siteDisplayName(acc.Site), acc.LocalSub)).
					FontSize(12).TextColor(ColorTextMuted).SingleLine()
			}
			settingSwitchRow(c, "把导入订阅里的新账号合并进这些已有订阅", "关闭时跳过这些订阅里已订阅的账号，其余账号建成新订阅。", "合并进已有订阅开关", &s.Merge)
			settingSwitchRowDisabled(c, "合并时一并加上导入订阅的缓存标签", "关闭时保留本地已有订阅原本的缓存标签。", "合并导入缓存标签开关", &s.MergeTags, !s.Merge)
		})
	}
}
