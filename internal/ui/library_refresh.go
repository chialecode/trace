package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/chialecode/trace/internal/library"
	"github.com/egoist/mygo/ui"
)

// LibraryRefreshState is the media library's "刷新": a rescan for files
// deleted in Explorer, then a dialog listing what it found.
type LibraryRefreshState struct {
	Running bool
	Open    bool
	Report  library.RebuildReport
}

func (a *App) renderLibraryRefreshButton(c *ui.Context) {
	running := a.LibraryRefresh.Running || a.isPending("library-refresh")
	text := "刷新"
	if running {
		text = "正在刷新…"
	}
	if OutlinePillButton(c, text, "刷新媒体库").Disabled(running).Clicked() && !running {
		a.refreshLibraryFromDisk()
	}
}

// refreshLibraryFromDisk rescans the library folders in the background (only
// local files, no site requests) and opens the result dialog.
func (a *App) refreshLibraryFromDisk() {
	if a.LibraryRefresh.Running || a.isPending("library-refresh") {
		return
	}
	a.LibraryRefresh.Running = true
	backend := a.backend
	requestGuarded(a, "library-refresh", nil, func(context.Context) (library.RebuildReport, error) {
		return backend.RefreshLibrary()
	}, func(rep library.RebuildReport) {
		a.LibraryRefresh = LibraryRefreshState{Open: true, Report: rep}
		a.libraryRev++
		a.libraryItemsDirty = false
		a.refreshLibraryStatistics()
		a.refreshAccountWorks()
		a.reloadLibraryItems()
	}, func(err error) {
		a.LibraryRefresh.Running = false
		a.showError(fmt.Errorf("刷新媒体库失败：%w", err))
	})
}

func (a *App) renderLibraryRefreshOverlay(c *ui.Context, w, h float32) {
	rep := a.LibraryRefresh.Report
	cardW := min(float32(560), max(float32(320), w-40))
	maxListH := max(float32(120), min(float32(420), h-260))
	renderDialog(c, &a.LibraryRefresh.Open,
		"关闭媒体库刷新结果（点击空白处）", "媒体库刷新结果",
		cardW, 0, nil,
		func() {
			ui.Text(c, "媒体库已刷新").FontSize(18).FontWeight(600).TextColor(ColorTextMain).SingleLine()
			if len(rep.Removed) == 0 && len(rep.NewlyMissing) == 0 {
				ui.Text(c, fmt.Sprintf("检查了 %d 个条目，没有发现被删除的文件。", rep.Scanned)).
					FontSize(13).TextColor(ColorTextMuted).MaxLines(2)
			} else {
				ui.Scroll(c).FillWidth().MaxHeight(maxListH).Children(func() {
					ui.Column(c).FillWidth().Gap(14).ClipX().Children(func() {
						if len(rep.Removed) > 0 {
							libraryRefreshSection(c, fmt.Sprintf("文件夹已删除，已从媒体库移除（%d）", len(rep.Removed)), rep.Removed, false)
						}
						if len(rep.NewlyMissing) > 0 {
							libraryRefreshSection(c, fmt.Sprintf("缺少文件（%d）", len(rep.NewlyMissing)), rep.NewlyMissing, true)
						}
					})
				})
			}
			ui.Row(c).FillWidth().Justify(ui.End).Children(func() {
				if AccentPillButton(c, "知道了", "关闭媒体库刷新结果").Clicked() {
					a.LibraryRefresh.Open = false
				}
			})
		})
}

// libraryRefreshSection lists changed items: the title, then the folder (for
// removed items) or the missing file names.
func libraryRefreshSection(c *ui.Context, heading string, items []library.ChangedItem, showFiles bool) {
	ui.Column(c).FillWidth().Gap(8).ClipX().Children(func() {
		ui.Text(c, heading).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
		for _, item := range items {
			it := item
			ui.Column(c).Label("刷新结果条目：" + it.ItemKey).FillWidth().Gap(2).ClipX().Children(func() {
				ui.Text(c, firstNonEmptyStrUI(it.Title, it.ItemKey)).FontSize(13).TextColor(ColorTextMain).SingleLine()
				detail := it.ItemDir
				if showFiles {
					var names []string
					for _, f := range it.Files {
						names = append(names, filepath.Base(f))
					}
					detail = joinLimited(names, 6)
					if detail == "" {
						detail = "没有可用的媒体文件"
					}
				}
				ui.Text(c, detail).FontSize(12).TextColor(ColorTextMuted).MaxLines(2)
			})
		}
	})
}

// joinLimited joins at most n names and says how many there are in all.
func joinLimited(names []string, n int) string {
	if len(names) <= n {
		return strings.Join(names, "、")
	}
	return fmt.Sprintf("%s 等 %d 个文件", strings.Join(names[:n], "、"), len(names))
}
