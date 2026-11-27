package ui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/egoist/mygo/ui"
)

type libraryTile struct {
	item  library.ItemMetadata
	file  library.MediaFile
	index int
	video bool
}

func (a *App) libraryTilesFingerprint() uint64 {
	h := newFNV64a()
	for i := range a.LibraryItems {
		it := &a.LibraryItems[i]
		fnvWriteStr(h, it.ItemKey)
		fnvWriteStr(h, it.Title)
		fnvWriteStr(h, it.Author.Name)
		fnvWriteInt64(h, int64(len(it.Media)))
		for j := range it.Media {
			m := &it.Media[j]
			fnvWriteStr(h, m.Role)
			fnvWriteStr(h, m.Path)
			fnvWriteInt64(h, int64(m.Duration))
		}
	}
	return h.Sum64()
}

func (a *App) libraryTiles() []libraryTile {
	sig := libraryTilesSig{
		rev:  a.cacheStateRev,
		n:    len(a.LibraryItems),
		kind: a.LibraryKindFilter,
		hash: a.libraryTilesFingerprint(),
	}
	st := a.fs()
	if st.libTiles != nil && st.libTilesSig == sig {
		return st.libTiles
	}
	tiles := make([]libraryTile, 0, len(a.LibraryItems))
	for _, item := range a.LibraryItems {
		index := 0
		for _, media := range item.Media {
			if media.Role == "image" && a.LibraryKindFilter != "video" && a.LibraryKindFilter != "live" {
				tiles = append(tiles, libraryTile{item: item, file: media, index: index})
				index++
			}
			if (media.Role == "video" || media.Role == "live_segment") && a.LibraryKindFilter != "image" {
				tiles = append(tiles, libraryTile{item: item, file: media, index: index, video: true})
				index++
			}
		}
	}
	st.libTilesSig = sig
	st.libTiles = tiles
	return tiles
}

func (a *App) enterLibrarySelectMode() {
	a.LibrarySelectMode = true
	if a.LibrarySelectedKeys == nil {
		a.LibrarySelectedKeys = make(map[string]bool)
	}
	a.ExpandedItemKey = ""
	a.Transfer.MenuOpen = false
}

func (a *App) exitLibrarySelectMode() {
	a.LibrarySelectMode = false
	a.clearLibrarySelection()
}

func (a *App) clearLibrarySelection() {
	if a.LibrarySelectedKeys != nil {
		clear(a.LibrarySelectedKeys)
	}
}

func (a *App) toggleLibraryItemSelection(itemKey string) {
	itemKey = strings.TrimSpace(itemKey)
	if itemKey == "" {
		return
	}
	if a.LibrarySelectedKeys == nil {
		a.LibrarySelectedKeys = make(map[string]bool)
	}
	if a.LibrarySelectedKeys[itemKey] {
		delete(a.LibrarySelectedKeys, itemKey)
	} else {
		a.LibrarySelectedKeys[itemKey] = true
	}
}

func (a *App) libraryVisibleItemKeys(tiles []libraryTile) []string {
	keys := make([]string, 0, len(tiles))
	seen := make(map[string]bool, len(tiles))
	for _, t := range tiles {
		if t.item.ItemKey == "" || seen[t.item.ItemKey] {
			continue
		}
		seen[t.item.ItemKey] = true
		keys = append(keys, t.item.ItemKey)
	}
	return keys
}

func (a *App) selectedLibraryItemKeys() []string {
	if len(a.LibrarySelectedKeys) == 0 {
		return nil
	}
	out := make([]string, 0, len(a.LibrarySelectedKeys))
	seen := make(map[string]bool, len(a.LibrarySelectedKeys))
	for _, it := range a.LibraryItems {
		if a.LibrarySelectedKeys[it.ItemKey] && !seen[it.ItemKey] {
			seen[it.ItemKey] = true
			out = append(out, it.ItemKey)
		}
	}
	var extra []string
	for k, ok := range a.LibrarySelectedKeys {
		if ok && k != "" && !seen[k] {
			extra = append(extra, k)
		}
	}
	if len(extra) > 1 {
		sort.Strings(extra)
	}
	out = append(out, extra...)
	return out
}

func (a *App) librarySelectedCount() int {
	n := 0
	for k, ok := range a.LibrarySelectedKeys {
		if ok && k != "" {
			n++
		}
	}
	return n
}

func (a *App) librarySelectedStats() (count int, totalBytes int64) {
	if len(a.LibrarySelectedKeys) == 0 {
		return 0, 0
	}
	seen := make(map[string]bool, len(a.LibrarySelectedKeys))
	for _, it := range a.LibraryItems {
		if a.LibrarySelectedKeys[it.ItemKey] && !seen[it.ItemKey] {
			seen[it.ItemKey] = true
			count++
			totalBytes += it.TotalBytes
		}
	}
	for k, ok := range a.LibrarySelectedKeys {
		if ok && k != "" && !seen[k] {
			seen[k] = true
			count++
		}
	}
	return count, totalBytes
}

func (a *App) renderLibraryBatchBar(c *ui.Context, tiles []libraryTile) {
	selectedCount, selectedBytes := a.librarySelectedStats()
	visibleKeys := a.libraryVisibleItemKeys(tiles)
	allLoadedSelected := len(visibleKeys) > 0
	for _, k := range visibleKeys {
		if !a.LibrarySelectedKeys[k] {
			allLoadedSelected = false
			break
		}
	}
	summaryText := "已选 0 项"
	if selectedCount > 0 {
		summaryText = fmt.Sprintf("已选 %d 项 · %s", selectedCount, FormatBytes(selectedBytes))
	}
	ui.Row(c).
		Label("媒体库批量操作栏").
		FillWidth().
		Padding(8, 16).
		Gap(8).
		Wrap().
		Shrink(0).
		AlignItems(ui.Center).
		Justify(ui.SpaceBetween).
		Background(ColorBgPanel).
		BorderWidth(0, 0, 1, 0).
		BorderColor(ColorBorder).
		ClipX().
		Children(func() {
			ui.Text(c, summaryText).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
			ui.Row(c).Gap(6).Wrap().AlignItems(ui.Center).ClipX().Children(func() {
				if allLoadedSelected {
					if OutlinePillButton(c, "取消全选", "媒体库取消全选").Clicked() {
						a.clearLibrarySelection()
					}
				} else {
					if OutlinePillButton(c, "全选已加载", "媒体库全选已加载").Disabled(len(visibleKeys) == 0).Clicked() && len(visibleKeys) > 0 {
						if a.LibrarySelectedKeys == nil {
							a.LibrarySelectedKeys = make(map[string]bool, len(visibleKeys))
						}
						for _, k := range visibleKeys {
							a.LibrarySelectedKeys[k] = true
						}
					}
				}
				if AccentPillButton(c, "批量导出", "媒体库批量导出").Disabled(selectedCount == 0).Clicked() && selectedCount > 0 {
					a.openBatchExportDialog()
				}
				if DangerOutlinePillButton(c, "批量删除", "媒体库批量删除").Disabled(selectedCount == 0).Clicked() && selectedCount > 0 {
					a.requestLibraryBatchDelete()
				}
				if OutlinePillButton(c, "退出多选", "退出多选").Clicked() {
					a.exitLibrarySelectMode()
				}
			})
		})
}

func (a *App) renderLibraryRoute(c *ui.Context) {
	if a.Transfer.ImportPage {
		a.renderImportPage(c)
		return
	}
	if a.LibrarySelectMode && !a.hasOpenModalOverlay() && !a.Transfer.MenuOpen && a.ExpandedItemKey == "" {
		if c.Shortcut(0, ui.KeyEscape) {
			a.exitLibrarySelectMode()
		}
	}
	// The kinds and sources are in the context panel; a narrow window
	// without it has them here (DEC-039).
	if !a.panelShown {
		UnderlineTabs(c, a.LibraryKindFilter, []TabOption{
			{Key: "", Text: "全部", AccessLabel: "筛选类型：全部类型"},
			{Key: "image", Text: "图片", AccessLabel: "筛选类型：图片"},
			{Key: "video", Text: "视频", AccessLabel: "筛选类型：视频"},
			{Key: "live", Text: "直播录像", AccessLabel: "筛选类型：直播录制"},
			{Key: "forward", Text: "转发", AccessLabel: "筛选类型：转发"},
		}, func(key string) { a.LibraryKindFilter = key; a.selectLibraryFilter() })
		ui.Row(c).FillWidth().Padding(6, 16, 0, 16).Children(func() {
			renderThreeSegmentSiteFilter(c, "媒体库站点筛选：", a.LibrarySiteFilter, func(next string) {
				a.LibrarySiteFilter = normalizeSiteFilter(next)
				a.selectLibraryFilter()
			})
		})
	}
	stats := a.libraryStatistics
	ui.Column(c).FillWidth().Padding(10, 16).Gap(8).Shrink(0).BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			input := ui.TextInput(c, &a.LibrarySearch).Label("媒体库搜索框").Placeholder("搜索标题或作者…").Grow(1)
			st := a.fs()
			if input.Changed() {
				if input.Composing() {
					st.librarySearchDirty = true
				} else {
					st.librarySearchDirty = false
					a.clearLibrarySelection()
					a.reloadLibraryItems()
				}
			} else if st.librarySearchDirty && !input.Composing() {
				st.librarySearchDirty = false
				a.clearLibrarySelection()
				a.reloadLibraryItems()
			}
			a.renderLibraryRefreshButton(c)
			if !a.panelShown && OutlinePillButton(c, "作者筛选", "媒体库作者筛选").Clicked() {
				a.LibraryAuthorMenu = !a.LibraryAuthorMenu
			}
		})
		if a.LibraryAuthorMenu && !a.panelShown {
			panelRows := a.libraryPanelRows()
			ui.Column(c).FillWidth().Gap(6).ClipX().Children(func() {
				ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
					if OutlinePillButton(c, "全部作者", "清除UP主筛选").Clicked() {
						a.LibraryCreatorMid = 0
						a.LibraryAuthorKey = ""
						a.LibrarySubFilter = ""
						a.LibraryAuthorMenu = false
						a.selectLibraryFilter()
					}
					a.renderLibrarySourceSortToggle(c)
				})
				ui.Scroll(c).FillWidth().Height(220).Gap(4).Children(func() {
					for _, row := range panelRows {
						usageText := librarySourceUsageText(row.itemCount, row.totalBytes)
						if row.isSub {
							sub := row.sub
							active := a.LibrarySubFilter == sub.ID
							pSite, pUID := subscriptionPrimarySiteAndUID(sub)
							if panelItem(c, "媒体库订阅筛选："+sub.ID, active, func() {
								a.renderOfflineAccountAvatar(c, pSite, sub.Name, pUID, sub.Mid, 24, "", nil)
								ui.Column(c).Grow(1).Gap(1).ClipX().Children(func() {
									nameCol := ColorTextMain
									if active {
										nameCol = ColorAccent
									}
									ui.Text(c, "订阅："+sub.Name).FontSize(13).FontWeight(500).TextColor(nameCol).SingleLine()
									ui.Text(c, usageText).FontSize(11).TextColor(ColorTextMuted).SingleLine()
								})
							}) {
								a.LibrarySubFilter = sub.ID
								a.LibraryCreatorMid = 0
								a.LibraryAuthorKey = ""
								a.LibraryAuthorMenu = false
								a.selectLibraryFilter()
							}
							continue
						}
						author := row.author
						aSite := row.site
						aKey := row.authorKey
						btnLabel := fmt.Sprintf("按UP主筛选 %d", author.Mid)
						active := aSite == "bilibili" && author.Mid > 0 && a.LibraryCreatorMid == author.Mid && a.LibraryAuthorKey == ""
						if aSite != "bilibili" || author.Mid <= 0 {
							btnLabel = "按作者筛选 " + aKey
							active = a.LibraryAuthorKey == aKey
						} else if a.LibraryAuthorKey == aKey {
							active = true
						}
						if panelItem(c, btnLabel, active, func() {
							a.renderOfflineAccountAvatar(c, aSite, author.Name, row.uid, author.Mid, 24, "", nil)
							ui.Column(c).Grow(1).Gap(1).ClipX().Children(func() {
								nameCol := ColorTextMain
								if active {
									nameCol = ColorAccent
								}
								ui.Text(c, author.Name).FontSize(13).FontWeight(500).TextColor(nameCol).SingleLine()
								ui.Text(c, usageText).FontSize(11).TextColor(ColorTextMuted).SingleLine()
							})
						}) {
							if aSite == "bilibili" && author.Mid > 0 {
								a.LibraryCreatorMid = author.Mid
								a.LibraryAuthorKey = ""
							} else {
								a.LibraryCreatorMid = 0
								a.LibraryAuthorKey = aKey
							}
							a.LibrarySubFilter = ""
							a.LibraryAuthorMenu = false
							a.selectLibraryFilter()
						}
					}
				})
			})
		}
		if !a.panelShown {
			ui.Text(c, fmt.Sprintf("共 %d 项缓存 · 占用 %s", stats.Items, FormatBytes(stats.Bytes))).FontSize(12).TextColor(ColorTextMuted)
		}
	})
	tiles := a.libraryTiles()
	if a.LibrarySelectMode {
		a.renderLibraryBatchBar(c, tiles)
	}
	if a.LibraryError != "" {
		ui.Box(c).FillWidth().Padding(8, 16, 0, 16).Children(func() {
			renderErrorBanner(c, "", a.LibraryError, func() {
				if OutlinePillButton(c, "重试", "重试媒体库分页").Clicked() {
					a.loadLibraryPage(a.libraryOffset == 0)
				}
			})
		})
	}
	if len(tiles) == 0 {
		ui.Column(c).FillWidth().Grow(1).Padding(40, 16).Gap(10).Center().Children(func() {
			ui.Text(c, "还没有符合筛选的媒体").FontSize(16).FontWeight(600).TextColor(ColorTextMain)
			ui.Text(c, "缓存图片、视频或直播录像后，它们会出现在这里。").FontSize(13).TextColor(ColorTextMuted)
			if a.LibraryHasMore && OutlinePillButton(c, "继续加载", "媒体库继续加载").Clicked() {
				a.loadLibraryPage(false)
			}
		})
		return
	}
	// Cards about 240px wide fill the page, 16:9 like a video player
	// (DEC-039).
	width, _ := c.Size()
	contentWidth := max(width-railWidth-a.contextPanelWidth(width), 320)
	if bounds := a.LibraryListState.Handle.Bounds(c); bounds.W > 0 {
		contentWidth = bounds.W
	}
	cols := max(2, min(6, int((contentWidth-32+16)/(240+16))))
	tileWidth := max(float32(120), (contentWidth-32-float32(cols-1)*16)/float32(cols))
	thumbSize := tileWidth * 9 / 16
	rows := (len(tiles) + cols - 1) / cols
	_, last := a.LibraryListState.Visible()
	if last >= 0 && last >= rows-2 && a.LibraryHasMore && a.LibraryError == "" {
		a.loadLibraryPage(false)
	}
	a.LibraryListState.Key = func(row int) any {
		idx := row * cols
		if idx < 0 || idx >= len(tiles) {
			return row
		}
		return tiles[idx].item.ItemKey + ":" + tiles[idx].file.Path
	}
	ui.List(c, &a.LibraryListState, rows, func(row int) {
		ui.Row(c).FillWidth().Padding(10, 16).Gap(16).AlignItems(ui.Start).Children(func() {
			for col := 0; col < cols; col++ {
				i := row*cols + col
				if i >= len(tiles) {
					ui.Box(c).Grow(1).Basis(0)
					continue
				}
				tile := tiles[i]
				a.renderLibraryTile(c, tile, thumbSize)
			}
		})
	}).Label("媒体库网格列表").Grow(1).FillWidth()
}

func libraryTileCoverRel(tile libraryTile) string {
	if !tile.video {
		return tile.file.Path
	}
	for _, media := range tile.item.Media {
		if media.Role == "cover" && media.Path != "" {
			return media.Path
		}
	}
	// Never pass a raw video/live_segment (.mp4) path into image decoding.
	return ""
}

func (a *App) renderLibraryTile(c *ui.Context, tile libraryTile, thumbSize float32) {
	item := tile.item
	rel := libraryTileCoverRel(tile)
	tileKey := item.ItemKey
	if tile.index > 0 {
		tileKey += ":" + tile.file.Path
	}
	menuOpen := a.ExpandedItemKey == tileKey || (tile.index == 0 && a.ExpandedItemKey == item.ItemKey)
	selected := a.LibrarySelectMode && a.LibrarySelectedKeys[item.ItemKey]

	ui.Column(c).Grow(1).Basis(0).Gap(8).ClipX().Children(func() {
		label := "媒体缩略图 " + tileKey
		tipLine, _, _ := strings.Cut(firstNonEmptyStrUI(item.Text, item.Desc, item.Title), "\n")
		button := ui.ButtonBase(c).Label(label).Tooltip(tipLine).FillWidth().Height(thumbSize).Radius(14).Clip()
		button.Children(func() {
			// Everything in the library is cached: no status dot (DEC-039).
			a.renderMediaThumb(c, item.ItemKey, rel, item.Title, FormatDuration(tile.file.Duration), "", tile.video, 0, thumbSize)
			if a.LibrarySelectMode {
				if selected {
					ui.Box(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).Radius(12).
						Background(ColorAccent.Alpha(0.10)).
						Border(2.5, ColorAccent)
				}
				badge := ui.Box(c).Absolute().Top(8).Left(8).Size(22, 22).Radius(6)
				if selected {
					badge.Label("已选媒体："+tileKey).Background(ColorAccent).Border(1.5, ColorWhite)
					badge.Draw(func(p *ui.Painter, r ui.Rect) {
						s := func(v float32) float32 { return v * r.W / 18.0 }
						mark := (&ui.Path{}).
							MoveTo(r.X+s(4.2), r.Y+s(9.2)).
							LineTo(r.X+s(7.6), r.Y+s(12.6)).
							LineTo(r.X+s(13.8), r.Y+s(5.6))
						p.StrokePath(mark, 2.2, ColorWhite)
					})
				} else {
					badge.Label("未选媒体："+tileKey).Background(ui.RGBA(0, 0, 0, 0.36)).Border(1.5, ColorWhite)
				}
			}
		})
		if button.Clicked() {
			if a.LibrarySelectMode {
				a.toggleLibraryItemSelection(item.ItemKey)
			} else {
				a.openLibraryTile(tile)
			}
		}

		authorLine := item.Author.Name
		var liveSpec string
		if item.Live != nil {
			liveSpec = formatLiveRecordingSpec(item.Live, tile.file.Path)
			if liveSpec != "" {
				authorLine = strings.TrimSpace(authorLine + " · " + liveSpec)
			}
		}

		// Only thumbnail, title, 1-line meta info (with 12px site mark), and a bottom-right "⋯" button (DEC-037).
		ui.Row(c).FillWidth().AlignItems(ui.Start).Justify(ui.SpaceBetween).Gap(6).ClipX().Children(func() {
			ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
				title := ui.ButtonBase(c).Label("打开媒体：" + tileKey).FillWidth().Justify(ui.Start).AlignItems(ui.Start)
				title.Children(func() {
					titleCol := ColorTextMain
					if selected {
						titleCol = ColorAccent
					}
					ui.Text(c, item.Title).FontSize(14).LineHeight(LineHeightHeading).FontWeight(600).MaxLines(2).TextColor(titleCol)
				})
				if title.Clicked() {
					if a.LibrarySelectMode {
						a.toggleLibraryItemSelection(item.ItemKey)
					} else {
						a.openLibraryTile(tile)
					}
				}
				ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(4).ClipX().Children(func() {
					ui.Text(c, authorLine).FontSize(12).SingleLine().TextColor(ColorTextMuted).Shrink(1)
					renderSiteSmallBadgeWithDot(c, item.Site, "媒体库站点："+tileKey)
				})
			})
			if !a.LibrarySelectMode {
				open := menuOpen
				MoreMenuButton(c, "媒体更多："+tileKey, 28, &open, func(m *ui.Menu) {
					if m.Item("详情").Chosen() {
						a.OpenLibraryItemDetail(item)
					}
					if m.Item("在资源管理器中显示").Chosen() {
						a.showError(a.backend.OpenItemFolder(item.ItemKey))
					}
					m.Separator()
					if m.Item("删除").Danger().Chosen() {
						a.requestLibraryDelete(item)
					}
				})
				keyedMenuOpen(&a.ExpandedItemKey, tileKey, open)
			}
		})
	})
}

// requestLibraryDelete opens the delete confirmation with its plan.
func (a *App) requestLibraryDelete(item library.ItemMetadata) {
	plan, err := a.backend.PlanDelete(item.ItemKey)
	if err != nil {
		a.showError(err)
		return
	}
	total := item.TotalBytes
	// A dynamic takes along the videos only it references.
	for _, key := range plan.ItemKeys {
		if key == item.ItemKey {
			continue
		}
		if ref, ok, err := a.backend.GetLibraryItem(key); err == nil && ok {
			total += ref.TotalBytes
		}
	}
	a.DeleteConfirm = DeleteConfirmState{
		Open:        true,
		Item:        item,
		TargetKeys:  append([]string(nil), plan.TargetKeys...),
		Plan:        plan,
		FileCount:   len(plan.RegisteredFiles),
		TotalBytes:  total,
		DeleteFiles: true,
	}
}

// requestLibraryBatchDelete builds a unified DeletePlan for all selected
// library items (handling shared referenced videos across the entire batch)
// and opens the consolidated confirmation modal.
func (a *App) requestLibraryBatchDelete() {
	keys := a.selectedLibraryItemKeys()
	if len(keys) == 0 {
		return
	}
	plan, err := a.backend.PlanDeleteBatch(keys)
	if err != nil {
		a.showError(err)
		return
	}
	loadedBytes := make(map[string]int64, len(a.LibraryItems))
	for _, it := range a.LibraryItems {
		loadedBytes[it.ItemKey] = it.TotalBytes
	}
	var total int64
	for _, key := range plan.ItemKeys {
		if b, ok := loadedBytes[key]; ok {
			total += b
			continue
		}
		if ref, ok, err := a.backend.GetLibraryItemMetaOnly(key); err == nil && ok {
			total += ref.TotalBytes
		}
	}
	a.DeleteConfirm = DeleteConfirmState{
		Open:        true,
		Batch:       true,
		TargetKeys:  append([]string(nil), plan.TargetKeys...),
		Plan:        plan,
		FileCount:   len(plan.RegisteredFiles),
		TotalBytes:  total,
		DeleteFiles: true,
	}
}

// renderCachedPostMenu is the "⋯" of a cached post in a subscription's 内容
// tab: local actions only, no site requests.
func (a *App) renderCachedPostMenu(c *ui.Context, postKey string, item library.ItemMetadata) {
	menuKey := "cached-post:" + postKey
	open := a.PostMenuKey == menuKey
	MoreMenuButton(c, "帖子更多："+postKey, 28, &open, func(m *ui.Menu) {
		if m.Item("在资源管理器中显示").Chosen() {
			a.showError(a.backend.OpenItemFolder(item.ItemKey))
		}
		m.Separator()
		if m.Item("删除").Danger().Chosen() {
			a.requestLibraryDelete(item)
		}
	})
	keyedMenuOpen(&a.PostMenuKey, menuKey, open)
}

// deleteConfirmTitle names the item in the delete dialog; a dynamic often
// has no title, so its text stands in.
func deleteConfirmTitle(item library.ItemMetadata) string {
	if t := strings.TrimSpace(item.Title); t != "" {
		return t
	}
	line, _, _ := strings.Cut(strings.TrimSpace(firstNonEmptyStrUI(item.Text, item.Desc)), "\n")
	if r := []rune(line); len(r) > 40 {
		line = string(r[:40]) + "…"
	}
	if line != "" {
		return line
	}
	return itemKindLabel(item.Kind)
}

func (a *App) openLibraryTile(tile libraryTile) {
	if len(tile.item.NotePages) > 0 {
		pages := buildDouyinNoteRenderPages(nil, &tile.item)
		selected := 0
		for i, pg := range pages {
			if pg.ImageRel == tile.file.Path || pg.VideoRel == tile.file.Path {
				selected = i
				break
			}
		}
		a.openNotePagesViewer(tile.item.ItemKey, normalizeSiteIDUI(tile.item.Site), pages, selected)
		return
	}
	if tile.video {
		a.openPlayerFile(tile.item, tile.file.Path)
		return
	}
	var images []string
	selected := 0
	for _, media := range tile.item.Media {
		if media.Role == "image" {
			if media.Path == tile.file.Path {
				selected = len(images)
			}
			images = append(images, media.Path)
		}
	}
	a.OpenItemImageViewer(tile.item.ItemKey, nil, images, selected)
}

// OpenLibraryItemDetail opens the local detail view on RouteLibraryItem without making network requests.
func (a *App) OpenLibraryItemDetail(item library.ItemMetadata) {
	a.closeLivePreview()
	if a.CurrentRoute != RouteLibraryItem {
		a.pushPreviousRoute(a.currentReturnRoute())
	}
	a.CurrentRoute = RouteLibraryItem
	a.clearErrorNotifications()
	a.Opening = nil
	a.PostMenuKey = ""
	a.CurrentLibraryDetail = &item
	a.CurrentDouyinWork = nil
	a.CurrentWeiboPost = nil
	a.CurrentWeiboVideo = nil
	a.CurrentXPost = nil
	a.CurrentDynamic = nil
	a.SelectedPartPages = make(map[int]bool)
	a.UpgradeAvailable = false
	if siteID := normalizeSiteIDUI(item.Site); siteID == "douyin" || siteID == "weibo" || siteID == "x" || item.Kind == "dynamic" || item.Kind == "live" {
		a.CurrentVideo = nil
		a.CurrentVideoItemKey = ""
		return
	}

	var pages []bilibili.VideoPage
	knownPages := make(map[int]bool)
	for _, p := range item.Parts {
		if p.Page <= 0 || knownPages[p.Page] {
			continue
		}
		knownPages[p.Page] = true
		pages = append(pages, bilibili.VideoPage{
			CID:      p.CID,
			Page:     p.Page,
			Title:    p.Title,
			Duration: p.Duration,
		})
	}
	for _, page := range item.ExpectedPages {
		if page > 0 && !knownPages[page] {
			pages = append(pages, bilibili.VideoPage{Page: page, Title: fmt.Sprintf("分P %d", page)})
			knownPages[page] = true
		}
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].Page < pages[j].Page })
	a.SelectedPartPages = make(map[int]bool, len(pages))
	for _, p := range pages {
		a.SelectedPartPages[p.Page] = true
	}
	a.CurrentVideo = &bilibili.VideoDetail{
		BVID:         item.RawID,
		Title:        item.Title,
		Desc:         firstNonEmptyStrUI(item.Desc, item.Text),
		PubTs:        item.PubTs,
		Duration:     item.Duration,
		CanonicalURL: item.URL,
		Author: bilibili.AuthorInfo{
			Mid:  item.Author.Mid,
			Name: item.Author.Name,
		},
		Pages: pages,
	}
	a.CurrentVideoItemKey = item.ItemKey
}

func (a *App) cachedDetailPost(item library.ItemMetadata) PostCardData {
	offline := a.CurrentRoute == RouteLibraryItem
	cacheKey := "detail:" + item.ItemKey
	if offline {
		cacheKey = "detail-offline:" + item.ItemKey
	}
	post := a.cachedPostCard(cacheKey, func() PostCardData {
		authorFace := item.Author.Face
		if offline {
			authorFace = ""
		}
		p := a.buildFeedPostCard(library.FeedEntry{
			FeedKey: item.ItemKey, ItemKey: item.ItemKey, Site: item.Site, Kind: item.Kind, RawID: item.RawID,
			Mid: item.Author.Mid, AuthorName: item.Author.Name, AuthorFace: authorFace,
			Title: item.Title, Summary: firstNonEmptyStrUI(item.Text, item.Desc), PubTs: item.PubTs,
			CanonicalURL: item.URL, Cached: true, CacheStatus: library.ComputeDotState(item).Dot,
		})
		if p.LibItem == nil {
			cp := item
			p.LibItem = &cp
		}
		if offline {
			p.Offline = true
			p.AuthorFace = ""
			if len(p.Images) > 0 {
				imgs := make([]PostImage, len(p.Images))
				for i, img := range p.Images {
					img.URL = ""
					img.LiveURL = ""
					imgs[i] = img
				}
				p.Images = imgs
			}
			if len(p.NotePages) > 0 {
				pages := make([]douyinNoteRenderPage, len(p.NotePages))
				for i, pg := range p.NotePages {
					pg.ImageURL = ""
					pg.MotionURL = ""
					pg.VideoURL = ""
					pages[i] = pg
				}
				p.NotePages = pages
			}
			if p.Video != nil {
				v := *p.Video
				v.CoverURL = ""
				v.BVID = ""
				p.Video = &v
			}
		}
		return p
	})
	post.InHomeFeed = false
	post.Offline = offline
	if post.LibItem == nil {
		cp := item
		post.LibItem = &cp
	}
	return post
}

func libraryItemParseInput(item library.ItemMetadata) string {
	if u := strings.TrimSpace(item.URL); u != "" {
		return u
	}
	siteID := normalizeSiteIDUI(item.Site)
	rawID := strings.TrimSpace(firstNonEmptyStrUI(item.RawID, item.ID))
	if siteID == "douyin" {
		switch item.Kind {
		case "dynamic", "note":
			if rawID != "" {
				return "https://www.douyin.com/note/" + rawID
			}
		case "live":
			if item.Live != nil && strings.TrimSpace(item.Live.WebRID) != "" {
				return "https://live.douyin.com/" + strings.TrimSpace(item.Live.WebRID)
			}
			if strings.TrimSpace(item.Author.UID) != "" {
				return "https://www.douyin.com/user/" + strings.TrimSpace(item.Author.UID)
			}
		default:
			if rawID != "" {
				return "https://www.douyin.com/video/" + rawID
			}
		}
		return rawID
	}
	if siteID == "weibo" {
		if item.Kind == "video" && rawID != "" {
			return "https://weibo.com/tv/show/" + rawID
		}
		if rawID != "" {
			return "https://weibo.com/detail/" + rawID
		}
		return rawID
	}
	if siteID == "x" {
		if rawID != "" {
			return "https://x.com/i/web/status/" + rawID
		}
		return rawID
	}
	switch item.Kind {
	case "dynamic":
		if rawID != "" {
			return "https://t.bilibili.com/" + rawID
		}
	case "live":
		if item.Live != nil && item.Live.RoomID > 0 {
			return fmt.Sprintf("https://live.bilibili.com/%d", item.Live.RoomID)
		}
		if item.Author.Mid > 0 {
			return fmt.Sprintf("https://space.bilibili.com/%d", item.Author.Mid)
		}
	default:
		if rawID != "" {
			return "https://www.bilibili.com/video/" + rawID
		}
	}
	return rawID
}

func (a *App) openInParseFromLibrary(item library.ItemMetadata) {
	target := libraryItemParseInput(item)
	a.Navigate(RouteParse)
	a.ParsePageInput = target
	if target != "" {
		a.SubmitParse(target)
	}
}

func (a *App) renderLibraryItemMoreMenu(c *ui.Context, item library.ItemMetadata, size float32) {
	menuKey := "lib-detail:" + item.ItemKey
	open := a.PostMenuKey == menuKey
	MoreMenuButton(c, "本机详情更多", size, &open, func(m *ui.Menu) {
		if m.Item("在解析页打开").Chosen() {
			a.openInParseFromLibrary(item)
		}
	})
	keyedMenuOpen(&a.PostMenuKey, menuKey, open)
}

func (a *App) renderLibraryItemRoute(c *ui.Context, viewportHeight float32) {
	if a.CurrentLibraryDetail != nil {
		switch normalizeSiteIDUI(a.CurrentLibraryDetail.Site) {
		case "douyin":
			if a.CurrentLibraryDetail.Kind != "live" {
				a.renderDouyinWorkRoute(c, viewportHeight)
				return
			}
		case "weibo":
			a.renderWeiboPostRoute(c, viewportHeight)
			return
		case "x":
			a.renderXPostRoute(c, viewportHeight)
			return
		}
	}
	a.renderVideoRoute(c)
}

func (a *App) renderRecordingDetail(c *ui.Context, item library.ItemMetadata) {
	ui.Text(c, "直播录像详情").FontSize(15).FontWeight(600).TextColor(ColorTextMain)
	post := a.cachedDetailPost(item)
	// The recording card uses its manifest and registered media, without BVID actions.
	if post.Video != nil {
		post.Video.BVID = ""
	}
	a.renderPostCard(c, post)
	room := item.RawID
	if item.Live != nil {
		if item.Live.RoomID > 0 {
			room = strconv.FormatInt(item.Live.RoomID, 10)
		} else if item.Live.WebRID != "" {
			room = item.Live.WebRID
		} else if item.Live.RoomIDStr != "" {
			room = item.Live.RoomIDStr
		}
	}
	meta := "直播间: " + room
	if spec := formatLiveRecordingSpec(item.Live, ""); spec != "" {
		meta += " · " + spec
	}
	ui.Text(c, meta).FontSize(13).TextColor(ColorTextMuted)
	segment := 0
	for _, media := range item.Media {
		if media.Role != "live_segment" {
			continue
		}
		segment++
		file := media.Path
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			if OutlinePillButton(c, fmt.Sprintf("播放片段 %d", segment), "播放录像片段："+item.ItemKey+":"+file).Clicked() {
				a.openPlayerFile(item, file)
			}
			info := FormatDuration(media.Duration)
			if spec := formatLiveRecordingSpec(item.Live, file); spec != "" {
				info += " · " + spec
			}
			ui.Text(c, info).FontSize(12).TextColor(ColorTextMuted)
		})
	}
}
