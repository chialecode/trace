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

func (a *App) libraryTiles() []libraryTile {
	var tiles []libraryTile
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
	return tiles
}

func (a *App) renderLibraryRoute(c *ui.Context) {
	// The kinds and sources are in the context panel; a narrow window
	// without it has them here (DEC-039).
	if !a.panelShown {
		UnderlineTabs(c, a.LibraryKindFilter, []TabOption{
			{Key: "", Text: "全部", AccessLabel: "筛选类型：全部类型"},
			{Key: "image", Text: "图片", AccessLabel: "筛选类型：图片"},
			{Key: "video", Text: "视频", AccessLabel: "筛选类型：视频"},
			{Key: "live", Text: "直播录像", AccessLabel: "筛选类型：直播录制"},
		}, func(key string) { a.LibraryKindFilter = key; a.reloadLibraryItems() })
		ui.Row(c).FillWidth().Padding(6, 16, 0, 16).Children(func() {
			renderThreeSegmentSiteFilter(c, "媒体库站点筛选：", a.LibrarySiteFilter, func(next string) {
				a.LibrarySiteFilter = normalizeSiteFilter(next)
				a.reloadLibraryItems()
			})
		})
	}
	stats := a.libraryStatistics
	ui.Column(c).FillWidth().Padding(10, 16).Gap(8).Shrink(0).BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			input := ui.TextInput(c, &a.LibrarySearch).Label("媒体库搜索框").Placeholder("搜索标题或作者…").Grow(1)
			if input.Changed() {
				a.reloadLibraryItems()
			}
			if !a.panelShown && OutlinePillButton(c, "作者筛选", "媒体库作者筛选").Clicked() {
				a.LibraryAuthorMenu = !a.LibraryAuthorMenu
			}
			sortText := "缓存时间"
			if a.LibrarySort == "pub_ts" {
				sortText = "发布时间"
			}
			if OutlinePillButton(c, sortText, "媒体库排序切换").Clicked() {
				if a.LibrarySort == "cached_at" {
					a.LibrarySort = "pub_ts"
				} else {
					a.LibrarySort = "cached_at"
				}
				a.reloadLibraryItems()
			}
		})
		if a.LibraryAuthorMenu && !a.panelShown {
			ui.Scroll(c).FillWidth().Height(220).Gap(4).Children(func() {
				if OutlinePillButton(c, "全部作者", "清除UP主筛选").Clicked() {
					a.LibraryCreatorMid = 0
					a.LibraryAuthorKey = ""
					a.LibrarySubFilter = ""
					a.LibraryAuthorMenu = false
					a.reloadLibraryItems()
				}
				ownedKeys := make(map[string]bool)
				ownedMids := make(map[int64]bool)
				for _, sub := range a.Subscriptions {
					for _, account := range sub.Accounts {
						normSite := normalizeSiteIDUI(account.Site)
						uid := strings.TrimSpace(account.UID)
						if uid != "" {
							ownedKeys[normSite+":"+uid] = true
						}
						if normSite == "bilibili" {
							if id, err := strconv.ParseInt(uid, 10, 64); err == nil && id > 0 {
								ownedMids[id] = true
							}
						}
					}
					if AccentOutlinePillButton(c, "订阅："+sub.Name, "媒体库订阅筛选："+sub.ID).Clicked() {
						a.LibrarySubFilter = sub.ID
						a.LibraryCreatorMid = 0
						a.LibraryAuthorKey = ""
						a.LibraryAuthorMenu = false
						a.reloadLibraryItems()
					}
				}
				for _, author := range stats.Authors {
					aSite := normalizeSiteIDUI(author.Site)
					aUID := strings.TrimSpace(author.UID)
					if aUID == "" && author.Mid > 0 {
						aUID = strconv.FormatInt(author.Mid, 10)
					}
					aKey := aSite + ":" + aUID
					if ownedKeys[aKey] || (aSite == "bilibili" && ownedMids[author.Mid]) {
						continue
					}
					btnLabel := fmt.Sprintf("按UP主筛选 %d", author.Mid)
					if aSite == "douyin" || author.Mid <= 0 {
						btnLabel = "按作者筛选 " + aKey
					}
					ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
						a.renderAccountAvatar(c, aSite, author.Name, aUID, author.Mid, 28, false, "", nil, author.Face)
						if OutlinePillButton(c, author.Name, btnLabel).Clicked() {
							if aSite == "bilibili" && author.Mid > 0 {
								a.LibraryCreatorMid = author.Mid
								a.LibraryAuthorKey = ""
							} else {
								a.LibraryCreatorMid = 0
								a.LibraryAuthorKey = aKey
							}
							a.LibrarySubFilter = ""
							a.LibraryAuthorMenu = false
							a.reloadLibraryItems()
						}
					})
				}
			})
		}
		if !a.panelShown {
			ui.Text(c, fmt.Sprintf("共 %d 项缓存 · 占用 %s", stats.Items, FormatBytes(stats.Bytes))).FontSize(12).TextColor(ColorTextMuted)
		}
	})
	if a.LibraryError != "" {
		ui.Text(c, a.LibraryError).FontSize(13).TextColor(ColorError)
		if OutlinePillButton(c, "重试", "重试媒体库分页").Clicked() {
			a.loadLibraryPage(a.libraryOffset == 0)
		}
	}
	tiles := a.libraryTiles()
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
	if last >= rows-2 && a.LibraryHasMore && a.LibraryError == "" {
		a.loadLibraryPage(false)
	}
	a.LibraryListState.Key = func(row int) any { return tiles[row*cols].item.ItemKey + ":" + tiles[row*cols].file.Path }
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

func (a *App) renderLibraryTile(c *ui.Context, tile libraryTile, thumbSize float32) {
	item := tile.item
	rel := tile.file.Path
	if tile.video {
		for _, media := range item.Media {
			if media.Role == "cover" {
				rel = media.Path
				break
			}
		}
	}
	tileKey := item.ItemKey
	if tile.index > 0 {
		tileKey += ":" + tile.file.Path
	}
	menuOpen := a.ExpandedItemKey == tileKey || (tile.index == 0 && a.ExpandedItemKey == item.ItemKey)

	ui.Column(c).Grow(1).Basis(0).Gap(8).ClipX().Children(func() {
		label := "媒体缩略图 " + tileKey
		button := ui.ButtonBase(c).Label(label).Tooltip(strings.Split(item.Text+item.Desc, "\n")[0]).FillWidth().Height(thumbSize).Radius(14).Clip()
		button.Children(func() {
			// Everything in the library is cached: no status dot (DEC-039).
			a.renderMediaThumb(c, item.ItemKey, rel, item.Title, FormatDuration(tile.file.Duration), "", tile.video, 0, thumbSize)
		})
		if button.Clicked() {
			a.openLibraryTile(tile)
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
					ui.Text(c, item.Title).FontSize(14).FontWeight(600).MaxLines(2).TextColor(ColorTextMain)
				})
				if title.Clicked() {
					a.openLibraryTile(tile)
				}
				ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(4).ClipX().Children(func() {
					ui.Text(c, authorLine).FontSize(12).SingleLine().TextColor(ColorTextMuted)
					renderSiteSmallBadgeWithDot(c, item.Site, "媒体库站点："+tileKey)
				})
			})
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
	a.DeleteConfirm = DeleteConfirmState{
		Open:        true,
		Item:        item,
		Plan:        plan,
		FileCount:   len(plan.RegisteredFiles),
		TotalBytes:  item.TotalBytes,
		DeleteFiles: true,
	}
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
		a.openDouyinNoteViewer(tile.item.ItemKey, pages, selected)
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

// OpenLibraryItemDetail opens the local detail view for a cached item without making network requests.
func (a *App) OpenLibraryItemDetail(item library.ItemMetadata) {
	if normalizeSiteIDUI(item.Site) == "douyin" {
		if a.CurrentRoute != RouteDouyinWork {
			a.PreviousRoute = a.CurrentRoute
		}
		a.CurrentRoute = RouteDouyinWork
		a.BannerError = ""
		a.CurrentLibraryDetail = &item
		a.CurrentDouyinWork = nil
		return
	}
	if a.CurrentRoute != RouteVideo {
		a.PreviousRoute = a.CurrentRoute
	}
	a.CurrentRoute = RouteVideo
	a.BannerError = ""
	a.CurrentLibraryDetail = &item
	a.CurrentDynamic = nil
	a.SelectedPartPages = make(map[int]bool)
	a.UpgradeAvailable = false
	if item.Kind == "dynamic" || item.Kind == "live" {
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
			Face: item.Author.Face,
		},
		Pages: pages,
	}
	a.CurrentVideoItemKey = item.ItemKey
	a.CurrentDynamic = nil
	a.UpgradeAvailable = item.UpgradeAvailable
}

func (a *App) cachedDetailPost(item library.ItemMetadata) PostCardData {
	post := a.cachedPostCard("detail:"+item.ItemKey, func() PostCardData {
		return a.buildFeedPostCard(library.FeedEntry{
			FeedKey: item.ItemKey, ItemKey: item.ItemKey, Site: item.Site, Kind: item.Kind, RawID: item.RawID,
			Mid: item.Author.Mid, AuthorName: item.Author.Name, AuthorFace: item.Author.Face,
			Title: item.Title, Summary: firstNonEmptyStrUI(item.Text, item.Desc), PubTs: item.PubTs,
			CanonicalURL: item.URL, Cached: true, CacheStatus: library.ComputeDotState(item).Dot,
		})
	})
	post.InHomeFeed = false
	return post
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
