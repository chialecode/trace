package ui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/transfer"
	"github.com/egoist/mygo/ui"
)

// TransferUIState is 媒体库 › 导入… / 导出… (docs/design/remote.md §6.3,
// §6.4): the import page (folder → scan → preview → start), the export
// dialog and the jobs listed on the Tasks page under 导入导出.
type TransferUIState struct {
	MenuOpen bool

	// Import page.
	ImportPage   bool
	ImportPath   string
	JobID        string // the scan shown on the import page
	Plan         *transfer.Plan
	Filter       transfer.Category // "" = every category
	Choices      map[string]string // item key → library.Merge* or conflictChoiceSkip; "" = inherit AllConflicts
	AllConflicts string
	DeleteSource bool
	ImportErr    string

	// Export dialog.
	ExportOpen  bool
	ExportPath  string
	ExportScope string // "all" or "filter"
	ExportErr   string

	Jobs []service.TransferJob
}

// conflictChoiceSkip explicitly marks a conflict row as skipped even when
// AllConflicts is set to a merge strategy.
const conflictChoiceSkip = "skip"

// conflictChoices are the three ways to handle a conflict, plus skipping it.
var conflictChoices = []struct{ value, text string }{
	{"", "暂不处理"},
	{library.MergeKeepLocal, "保留本地"},
	{library.MergeUseImported, "用导入的"},
	{library.MergeUnion, "合并"},
}

var conflictChoiceNames = []string{"暂不处理", "保留本地", "用导入的", "合并"}

func conflictChoiceText(v string) string {
	for _, o := range conflictChoices {
		if o.value == v {
			return o.text
		}
	}
	return "暂不处理"
}

func transferCategoryText(cat transfer.Category) string {
	switch cat {
	case transfer.CategoryNew:
		return "新条目"
	case transfer.CategorySupplement:
		return "可补充"
	case transfer.CategorySame:
		return "相同"
	case transfer.CategoryConflict:
		return "冲突"
	case transfer.CategoryIncomplete:
		return "未传完"
	case transfer.CategoryUnrecognized:
		return "无法识别"
	}
	return string(cat)
}

func (a *App) openImportPage() {
	a.Navigate(RouteLibrary)
	a.Transfer.ImportPage = true
	a.Transfer.ImportErr = ""
	if a.Transfer.JobID != "" && a.Transfer.Plan == nil {
		a.loadImportPlan(a.Transfer.JobID)
	}
}

func (a *App) openExportDialog() {
	a.Transfer.ExportOpen = true
	a.Transfer.ExportErr = ""
	if a.Transfer.ExportScope == "" {
		a.Transfer.ExportScope = "all"
	}
}

// libraryExportFilter is the media library's current filter without paging.
func (a *App) libraryExportFilter() library.FeedFilter {
	f := library.FeedFilter{
		Site:           normalizeSiteFilter(a.LibrarySiteFilter),
		Kind:           a.LibraryKindFilter,
		ExcludeForward: a.LibraryKindFilter == "image" || a.LibraryKindFilter == "video",
		Mid:            a.LibraryCreatorMid,
		Keyword:        a.LibrarySearch,
	}
	f.Authors, f.AuthorRefs = a.libraryAuthorFilter()
	return f
}

// libraryFilterActive reports whether the media library shows less than everything.
func (a *App) libraryFilterActive() bool {
	site := normalizeSiteFilter(a.LibrarySiteFilter)
	return (site != "" && site != "all") ||
		(a.LibraryKindFilter != "" && a.LibraryKindFilter != "all") ||
		a.LibraryCreatorMid != 0 ||
		a.LibrarySubFilter != "" ||
		a.LibraryAuthorKey != "" ||
		strings.TrimSpace(a.LibrarySearch) != ""
}

func (a *App) startImportScan() {
	path := strings.Trim(strings.TrimSpace(a.Transfer.ImportPath), `"`)
	job, err := a.backend.StartImportScan(path)
	if err != nil {
		a.Transfer.ImportErr = userErrorText(err)
		return
	}
	a.Transfer.ImportErr = ""
	a.Transfer.JobID = job.ID
	a.Transfer.Plan = nil
	a.planBytesPlan = nil
	a.Transfer.Filter = ""
	a.Transfer.Choices = map[string]string{}
	a.Transfer.AllConflicts = ""
	a.refreshTransferJobs()
}

func (a *App) loadImportPlan(jobID string) {
	plan, ok := a.backend.ImportPlan(jobID)
	if !ok {
		return
	}
	a.Transfer.Plan = &plan
	a.planBytesPlan = nil
	if a.Transfer.Choices == nil {
		a.Transfer.Choices = map[string]string{}
	}
}

func (a *App) planCopyBytes(plan *transfer.Plan) int64 {
	if plan == nil {
		return 0
	}
	if a.planBytesPlan == plan {
		return a.planBytesVal
	}
	var total int64
	for _, e := range plan.Entries {
		if e.Category == transfer.CategoryNew || e.Category == transfer.CategorySupplement || e.Category == transfer.CategoryIncomplete {
			total += e.CopyBytes
		}
	}
	a.planBytesPlan = plan
	a.planBytesVal = total
	return total
}

func (a *App) refreshTransferJobs() {
	a.Transfer.Jobs = a.backend.TransferJobs()
}

// onTransferUpdated follows a transfer.updated event.
func (a *App) onTransferUpdated(jobID string) {
	a.refreshTransferJobs()
	if jobID != "" && jobID == a.Transfer.JobID && a.Transfer.Plan == nil {
		if j, ok := a.transferJob(jobID); ok && j.Status == service.TransferReady {
			a.loadImportPlan(jobID)
		}
	}
}

func (a *App) transferJob(id string) (service.TransferJob, bool) {
	for _, j := range a.Transfer.Jobs {
		if j.ID == id {
			return j, true
		}
	}
	return service.TransferJob{}, false
}

func (a *App) activeTransferCount() int {
	n := 0
	for _, j := range a.Transfer.Jobs {
		if j.Active() || j.Status == service.TransferReady {
			n++
		}
	}
	return n
}

func (a *App) startImport() {
	choices := transfer.Choices{
		Conflicts:    map[string]string{},
		AllConflicts: a.Transfer.AllConflicts,
		DeleteSource: a.Transfer.DeleteSource,
	}
	for k, v := range a.Transfer.Choices {
		if v != "" {
			choices.Conflicts[k] = v
		}
	}
	if _, err := a.backend.StartImport(a.Transfer.JobID, choices); err != nil {
		a.Transfer.ImportErr = userErrorText(err)
		return
	}
	a.Transfer.ImportPage = false
	a.Transfer.Plan = nil
	a.planBytesPlan = nil
	a.Transfer.JobID = ""
	a.Transfer.DeleteSource = false
	a.refreshTransferJobs()
	a.Navigate(RouteTasks)
	a.selectTasksTransfers()
}

// renderLibraryMoreMenu is 媒体库 › ⋯ (导入… / 导出…).
func (a *App) renderLibraryMoreMenu(c *ui.Context) {
	open := a.Transfer.MenuOpen
	MoreMenuButton(c, "媒体库更多菜单", 32, &open, func(m *ui.Menu) {
		if m.Item("导入…").Chosen() {
			a.openImportPage()
		}
		if m.Item("导出…").Chosen() {
			a.openExportDialog()
		}
	})
	a.Transfer.MenuOpen = open
}

// renderImportPage is the import page inside the media library: pick a
// folder, scan (read-only), preview and answer conflicts, start.
func (a *App) renderImportPage(c *ui.Context) {
	t := &a.Transfer
	job, hasJob := a.transferJob(t.JobID)
	ready := hasJob && job.Status == service.TransferReady && t.Plan != nil
	ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
		ui.Column(c).FillWidth().Padding(16).Gap(14).ClipX().Children(func() {
			ui.Column(c).FillWidth().Gap(6).ClipX().Children(func() {
				ui.Text(c, "选择要导入的文件夹").FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
				ui.Text(c, "从网盘下载的文件夹、另一台 TRACE 的缓存或导出文件夹都可以。先只读扫描，确认后才写入媒体库。").
					FontSize(12).TextColor(ColorTextMuted).MaxLines(3)
				ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).ClipX().Children(func() {
					ui.TextInput(c, &t.ImportPath).Label("导入文件夹路径").Placeholder(`例如 D:\Downloads\trace`).Grow(1)
					scanning := hasJob && job.Status == service.TransferScanning
					text := "扫描"
					if scanning {
						text = "正在扫描…"
					}
					if AccentPillButton(c, text, "扫描导入文件夹").Disabled(scanning).Clicked() && !scanning {
						a.startImportScan()
					}
				})
				if t.ImportErr != "" && !ready {
					ui.Text(c, t.ImportErr).FontSize(12).TextColor(ColorError).MaxLines(3)
				}
			})
			if !hasJob {
				return
			}
			switch job.Status {
			case service.TransferScanning:
				ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).ClipX().Children(func() {
					ui.Text(c, fmt.Sprintf("正在扫描：已检查 %d 个条目", job.Progress.Done)).FontSize(13).TextColor(ColorTextMuted).SingleLine().Grow(1)
					if OutlinePillButton(c, "取消", "取消扫描").Clicked() {
						if err := a.backend.CancelTransfer(job.ID); err != nil {
							a.showError(err)
						}
					}
				})
				return
			case service.TransferFailed:
				ui.Text(c, "扫描失败："+job.Error).FontSize(13).TextColor(ColorError).MaxLines(3)
				return
			case service.TransferCanceled:
				ui.Text(c, "扫描已取消").FontSize(13).TextColor(ColorTextMuted).SingleLine()
				return
			}
			if t.Plan == nil {
				return
			}
			a.renderImportPreview(c, t.Plan)
		})
	})
	if ready {
		a.renderImportActions(c)
	}
}

// renderImportActions is the bar under the preview: delete the source or
// not, give up, start.
func (a *App) renderImportActions(c *ui.Context) {
	t := &a.Transfer
	ui.Column(c).Label("导入操作栏").FillWidth().Padding(4, 16, 12, 16).Gap(6).Shrink(0).BorderWidth(1, 0, 0, 0).BorderColor(ColorBorder).Background(ColorBgMain).ClipX().Children(func() {
		settingSwitchRow(c, "导入后删除来源里已导入的文件", "只删校验通过并已导入的文件和变空的文件夹；默认不动来源。", "导入后删除来源开关", &t.DeleteSource)
		if t.ImportErr != "" {
			ui.Text(c, t.ImportErr).FontSize(12).TextColor(ColorError).MaxLines(3)
		}
		ui.Row(c).FillWidth().Gap(8).Justify(ui.End).Children(func() {
			if OutlinePillButton(c, "放弃", "放弃这次导入").Clicked() {
				if err := a.backend.CancelTransfer(t.JobID); err != nil {
					a.showError(err)
				}
				t.JobID, t.Plan = "", nil
				a.planBytesPlan = nil
				a.refreshTransferJobs()
			}
			if AccentPillButton(c, "开始导入", "开始导入").Clicked() {
				a.startImport()
			}
		})
	})
}

func (a *App) renderImportPreview(c *ui.Context, plan *transfer.Plan) {
	t := &a.Transfer
	conflicts := plan.Counts[transfer.CategoryConflict]
	toImport := plan.Counts[transfer.CategoryNew] + plan.Counts[transfer.CategorySupplement] + plan.Counts[transfer.CategoryIncomplete]
	copyBytes := a.planCopyBytes(plan)
	ui.Text(c, fmt.Sprintf("扫描到 %d 个条目：%d 个会导入（约 %s），%d 个冲突等你决定", len(plan.Entries), toImport, FormatBytes(copyBytes), conflicts)).
		FontSize(13).FontWeight(600).TextColor(ColorTextMain).MaxLines(2)

	// Category chips: every category with its count; click to filter.
	ui.Row(c).FillWidth().Gap(6).Wrap().ClipX().Children(func() {
		chip := func(cat transfer.Category, text string) {
			active := t.Filter == cat
			btn := ui.ButtonBase(c).Label("导入预览筛选："+text).Padding(4, 10).Radius(999).Border(1, ColorBorder)
			if active {
				btn.Background(ColorAccentBg).BorderColor(ColorAccent)
			} else if btn.Hovered() {
				btn.Background(ColorHover)
			}
			btn.Children(func() {
				col := ColorTextMuted
				if active {
					col = ColorAccent
				}
				ui.Text(c, text).FontSize(12).FontWeight(600).TextColor(col).SingleLine()
			})
			if btn.Clicked() {
				t.Filter = cat
			}
		}
		chip("", fmt.Sprintf("全部 %d", len(plan.Entries)))
		for _, cat := range transfer.Categories {
			chip(cat, fmt.Sprintf("%s %d", transferCategoryText(cat), plan.Counts[cat]))
		}
	})

	if conflicts > 0 {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).ClipX().Children(func() {
			ui.Text(c, "全部冲突都：").FontSize(13).TextColor(ColorTextMain).SingleLine()
			shown := conflictChoiceText(t.AllConflicts)
			if ui.Select(c, &shown, conflictChoiceNames).Label("全部冲突的处理").Changed() {
				for _, o := range conflictChoices {
					if o.text == shown {
						t.AllConflicts = o.value
						break
					}
				}
			}
			ui.Text(c, "逐条选的优先").FontSize(12).TextColor(ColorTextMuted).SingleLine()
		})
	}

	const maxRows = 200
	shownRows := 0
	ui.Column(c).FillWidth().Gap(8).ClipX().Children(func() {
		for i := range plan.Entries {
			e := plan.Entries[i]
			if t.Filter != "" && e.Category != t.Filter {
				continue
			}
			if shownRows == maxRows {
				ui.Text(c, fmt.Sprintf("只显示前 %d 条；导入会处理全部条目。", maxRows)).FontSize(12).TextColor(ColorTextMuted).SingleLine()
				break
			}
			shownRows++
			a.renderImportEntry(c, i, e)
		}
		if shownRows == 0 {
			ui.Text(c, "这一类没有条目").FontSize(13).TextColor(ColorTextMuted).SingleLine()
		}
	})
}

func (a *App) renderImportEntry(c *ui.Context, idx int, e transfer.Entry) {
	t := &a.Transfer
	ui.Column(c).Label(fmt.Sprintf("导入预览条目 %d", idx+1)).FillWidth().Padding(10, 12).Radius(12).Border(1, ColorBorder).Background(ColorBgMain).Gap(4).ClipX().Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).ClipX().Children(func() {
			badgeBg, badgeFg := ColorBgSecondary, ColorTextMuted
			switch e.Category {
			case transfer.CategoryConflict, transfer.CategoryUnrecognized:
				badgeBg, badgeFg = ColorWarnBg, ColorTextMain
			case transfer.CategoryNew, transfer.CategorySupplement:
				badgeBg, badgeFg = ColorInfoBlueBg, ColorInfoBlue
			}
			ui.Box(c).Padding(1, 6).Radius(4).Background(badgeBg).Shrink(0).Children(func() {
				ui.Text(c, transferCategoryText(e.Category)).FontSize(11).FontWeight(600).TextColor(badgeFg).SingleLine()
			})
			title := firstNonEmptyStrUI(e.Title, e.ItemKey, filepath.Base(e.SourceDir))
			ui.Text(c, title).FontSize(13).FontWeight(500).TextColor(ColorTextMain).SingleLine().Grow(1)
			if e.Category == transfer.CategoryConflict && e.ItemKey != "" {
				eff := t.AllConflicts
				if v, ok := t.Choices[e.ItemKey]; ok && v != "" {
					eff = v
				}
				shown := conflictChoiceText(eff)
				if ui.Select(c, &shown, conflictChoiceNames).Label("冲突处理：" + e.ItemKey).Changed() {
					if t.Choices == nil {
						t.Choices = map[string]string{}
					}
					for _, o := range conflictChoices {
						if o.text == shown {
							if o.value == "" {
								t.Choices[e.ItemKey] = conflictChoiceSkip
							} else {
								t.Choices[e.ItemKey] = o.value
							}
							break
						}
					}
				}
			}
		})
		var parts []string
		if e.Author != "" {
			parts = append(parts, e.Author)
		}
		if e.CachedBy != "" {
			parts = append(parts, "来自 "+e.CachedBy)
		}
		if e.CopyFiles > 0 {
			parts = append(parts, fmt.Sprintf("复制 %d 个文件 · %s", e.CopyFiles, FormatBytes(e.CopyBytes)))
		}
		if e.Unverified > 0 {
			parts = append(parts, fmt.Sprintf("%d 个文件缺失或不一致，先导入校验通过的", e.Unverified))
		}
		if len(e.Conflicts) > 0 {
			parts = append(parts, "内容不同："+joinLimited(baseNames(e.Conflicts), 3))
		}
		if e.OtherFolder {
			parts = append(parts, "本地在另一个作者文件夹")
		}
		if e.Reason != "" {
			parts = append(parts, e.Reason)
		}
		if len(parts) > 0 {
			ui.Text(c, strings.Join(parts, " · ")).FontSize(12).TextColor(ColorTextMuted).MaxLines(3)
		}
		ui.Text(c, e.SourceDir).FontSize(11).TextColor(ColorTextMuted).SingleLine()
	})
}

func baseNames(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = filepath.Base(p)
	}
	return out
}

// renderExportOverlay is 媒体库 › 导出…: range and folder.
func (a *App) renderExportOverlay(c *ui.Context, w, h float32) {
	t := &a.Transfer
	cardW := min(float32(520), max(float32(320), w-40))
	filtered := a.libraryFilterActive()
	renderDialog(c, &t.ExportOpen, "关闭导出对话框（点击空白处）", "导出媒体库对话框", cardW, 0, nil, func() {
		ui.Text(c, "导出媒体库").FontSize(18).FontWeight(600).TextColor(ColorTextMain).SingleLine()
		ui.Text(c, "复制已完成的文件和 item.json，另一台 TRACE 可以直接导入。正在缓存或录制的条目只导出已完成的部分。").
			FontSize(12).TextColor(ColorTextMuted).MaxLines(3)
		scope := "全部"
		if t.ExportScope == "filter" {
			scope = "当前筛选"
		}
		opts := []string{"全部"}
		if filtered {
			opts = append(opts, "当前筛选")
		} else if scope == "当前筛选" {
			scope = "全部"
		}
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "范围").FontSize(13).TextColor(ColorTextMain).SingleLine()
			if ui.Select(c, &scope, opts).Label("导出范围").Changed() {
				if scope == "当前筛选" {
					t.ExportScope = "filter"
				} else {
					t.ExportScope = "all"
				}
			}
		})
		ui.TextInput(c, &t.ExportPath).Label("导出到的文件夹").Placeholder(`例如 E:\trace-export`).FillWidth()
		if t.ExportErr != "" {
			ui.Text(c, t.ExportErr).FontSize(12).TextColor(ColorError).MaxLines(3)
		}
		ui.Row(c).FillWidth().Gap(8).Justify(ui.End).Children(func() {
			if OutlinePillButton(c, "取消", "取消导出").Clicked() {
				t.ExportOpen = false
			}
			if AccentPillButton(c, "开始导出", "开始导出").Clicked() {
				filter := library.FeedFilter{}
				if scope == "当前筛选" {
					filter = a.libraryExportFilter()
				}
				path := strings.Trim(strings.TrimSpace(t.ExportPath), `"`)
				if _, err := a.backend.StartExport(path, filter); err != nil {
					t.ExportErr = userErrorText(err)
				} else {
					t.ExportOpen, t.ExportErr = false, ""
					a.refreshTransferJobs()
					a.Navigate(RouteTasks)
					a.selectTasksTransfers()
				}
			}
		})
	})
}

func (a *App) selectTasksTransfers() {
	a.TasksSection = "transfers"
	a.closeLivePreview()
	a.refreshTransferJobs()
}

func (a *App) transfersNavText() string {
	if n := a.activeTransferCount(); n > 0 {
		return fmt.Sprintf("导入导出（%d）", n)
	}
	return "导入导出"
}

func transferStatusText(j service.TransferJob) string {
	switch j.Status {
	case service.TransferScanning:
		return "正在扫描"
	case service.TransferReady:
		return "等待确认"
	case service.TransferRunning:
		if j.Kind == "export" {
			return "正在导出"
		}
		return "正在导入"
	case service.TransferDone:
		return "已完成"
	case service.TransferFailed:
		return "失败"
	case service.TransferCanceled:
		return "已取消"
	}
	return j.Status
}

// transferResultText is the summary line of a finished job.
func transferResultText(j service.TransferJob) string {
	if s := j.Summary; s != nil {
		line := fmt.Sprintf("新增 %d · 补充 %d · 跳过 %d · 处理冲突 %d · 未传完 %d · 复制 %d 个文件（%s）",
			s.Imported, s.Supplemented, s.Skipped, s.Conflicts, s.Incomplete, s.Files, FormatBytes(s.Bytes))
		if s.SourceFiles > 0 {
			line += fmt.Sprintf(" · 删除来源 %d 个文件", s.SourceFiles)
		}
		return line
	}
	if s := j.Export; s != nil {
		line := fmt.Sprintf("导出 %d 个条目 · %d 个文件（%s）", s.Items, s.Files, FormatBytes(s.Bytes))
		if s.Skipped > 0 {
			line += fmt.Sprintf(" · %d 个文件本地已缺失", s.Skipped)
		}
		return line
	}
	if len(j.Counts) > 0 {
		var parts []string
		for _, cat := range transfer.Categories {
			if n := j.Counts[cat]; n > 0 {
				parts = append(parts, fmt.Sprintf("%s %d", transferCategoryText(cat), n))
			}
		}
		return strings.Join(parts, " · ")
	}
	return ""
}

func transferErrors(j service.TransferJob) []string {
	if j.Summary != nil {
		return j.Summary.Errors
	}
	if j.Export != nil {
		return j.Export.Errors
	}
	return nil
}

// renderTasksTransfersView is 任务 › 导入导出.
func (a *App) renderTasksTransfersView(c *ui.Context) {
	ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
		ui.Column(c).FillWidth().Padding(16).Gap(12).ClipX().Children(func() {
			if len(a.Transfer.Jobs) == 0 {
				ui.Column(c).FillWidth().Padding(40, 16).AlignItems(ui.Center).Gap(8).Children(func() {
					ui.Text(c, "还没有导入或导出").FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					ui.Text(c, "在媒体库右上角“⋯”选择导入或导出，进度和结果会显示在这里。").FontSize(13).TextColor(ColorTextMuted)
				})
				return
			}
			for _, job := range a.Transfer.Jobs {
				a.renderTransferJob(c, job)
			}
		})
	})
}

func (a *App) renderTransferJob(c *ui.Context, j service.TransferJob) {
	kind := "导入"
	path := j.Source
	if j.Kind == "export" {
		kind, path = "导出", j.Target
	}
	ui.Column(c).Label("导入导出任务："+j.ID).FillWidth().Padding(14).Radius(16).Background(ColorBgMain).Border(1, ColorBorder).Gap(8).ClipX().Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).ClipX().Children(func() {
			DrawIcon(c, "upload", 16, ColorTextMuted, 1.8)
			ui.Text(c, kind+" · "+path).FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine().Grow(1)
			ui.Text(c, transferStatusText(j)).FontSize(12).FontWeight(600).TextColor(ColorTextMuted).SingleLine()
		})
		p := j.Progress
		if j.Status == service.TransferRunning || j.Status == service.TransferScanning {
			frac := float32(0)
			detail := fmt.Sprintf("%d / %d 个条目", p.Done, p.Total)
			if p.TotalBytes > 0 {
				frac = float32(float64(p.Bytes) / float64(p.TotalBytes))
				detail = fmt.Sprintf("%s / %s", FormatBytes(p.Bytes), FormatBytes(p.TotalBytes))
			} else if p.Total > 0 {
				frac = float32(p.Done) / float32(p.Total)
			}
			if j.SpeedBps > 0 {
				detail += " · " + FormatBytes(j.SpeedBps) + "/s"
			}
			ui.Box(c).FillWidth().Height(6).Radius(3).Background(ColorBorder).Clip().Draw(func(pt *ui.Painter, r ui.Rect) {
				f := max(float32(0), min(float32(1), frac))
				if f > 0 {
					pt.Fill(ui.Rect{X: r.X, Y: r.Y, W: r.W * f, H: r.H}, ColorInfoBlue, 3)
				}
			})
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).ClipX().Children(func() {
				ui.Text(c, detail).FontSize(12).TextColor(ColorTextMuted).SingleLine().Grow(1)
				if OutlinePillButton(c, "取消", "取消导入导出："+j.ID).Clicked() {
					if err := a.backend.CancelTransfer(j.ID); err != nil {
						a.showError(err)
					}
				}
			})
			if p.Current != "" {
				ui.Text(c, p.Current).FontSize(11).TextColor(ColorTextMuted).SingleLine()
			}
		}
		if line := transferResultText(j); line != "" {
			ui.Text(c, line).FontSize(12).TextColor(ColorTextMain).MaxLines(3)
		}
		if j.Error != "" {
			ui.Text(c, "原因："+j.Error).FontSize(12).TextColor(ColorError).MaxLines(3)
		}
		if errs := transferErrors(j); len(errs) > 0 {
			ui.Text(c, fmt.Sprintf("%d 个条目没有完成：%s", len(errs), joinLimited(errs, 3))).FontSize(12).TextColor(ColorError).MaxLines(4)
		}
		if j.Status == service.TransferReady {
			ui.Row(c).FillWidth().Gap(8).Justify(ui.End).Children(func() {
				if OutlinePillButton(c, "放弃", "放弃导入："+j.ID).Clicked() {
					if err := a.backend.CancelTransfer(j.ID); err != nil {
						a.showError(err)
					}
					if a.Transfer.JobID == j.ID {
						a.Transfer.JobID, a.Transfer.Plan = "", nil
						a.planBytesPlan = nil
					}
				}
				if AccentPillButton(c, "查看预览", "查看导入预览："+j.ID).Clicked() {
					if a.Transfer.JobID != j.ID {
						a.Transfer.JobID, a.Transfer.Plan = j.ID, nil
						a.planBytesPlan = nil
						a.Transfer.Choices, a.Transfer.AllConflicts, a.Transfer.Filter = map[string]string{}, "", ""
					}
					a.openImportPage()
				}
			})
		}
	})
}
