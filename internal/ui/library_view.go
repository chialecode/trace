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

func (a *App) renderLibraryRoute(c *ui.Context) {
	if a.Transfer.ImportPage {
		a.renderImportPage(c)
		return
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
			st := a.fs()
			if input.Changed() {
				if input.Composing() {
					st.librarySearchDirty = true
				} else {
					st.librarySearchDirty = false
					a.reloadLibraryItems()
				}
			} else if st.librarySearchDirty && !input.Composing() {
				st.librarySearchDirty = false
				a.reloadLibraryItems()
			}
			a.renderLibraryRefreshButton(c)
			if !a.panelShown && OutlinePillButton(c, "作者筛选", "媒体库作者筛选").Clicked() {
				a.LibraryAuthorMenu = !a.LibraryAuthorMenu
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
						// Offline library view: use local avatar or initial fallback only (0 remote requests).
						a.renderAccountAvatar(c, aSite, author.Name, aUID, author.Mid, 28, false, "", nil)
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
		ui.Box(c).FillWidth().Padding(8, 16, 0, 16).Children(func() {
			renderErrorBanner(c, "", a.LibraryError, func() {
				if OutlinePillButton(c, "重试", "重试媒体库分页").Clicked() {
					a.loadLibraryPage(a.libraryOffset == 0)
				}
			})
		})
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

	ui.Column(c).Grow(1).Basis(0).Gap(8).ClipX().Children(func() {
		label := "媒体缩略图 " + tileKey
		tipLine, _, _ := strings.Cut(firstNonEmptyStrUI(item.Text, item.Desc, item.Title), "\n")
		button := ui.ButtonBase(c).Label(label).Tooltip(tipLine).FillWidth().Height(thumbSize).Radius(14).Clip()
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
					ui.Text(c, authorLine).FontSize(12).SingleLine().TextColor(ColorTextMuted).Shrink(1)
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

// OpenLibraryItemDetail opens the local detail view on RouteLibraryItem without making network requests.
func (a *App) OpenLibraryItemDetail(item library.ItemMetadata) {
	a.closeLivePreview()
	if a.CurrentRoute != RouteLibraryItem {
		a.PreviousRoute = a.CurrentRoute
	}
	a.CurrentRoute = RouteLibraryItem
	a.BannerError = ""
	a.Opening = nil
	a.PostMenuKey = ""
	a.CurrentLibraryDetail = &item
	a.CurrentDouyinWork = nil
	a.CurrentDynamic = nil
	a.SelectedPartPages = make(map[int]bool)
	a.UpgradeAvailable = false
	if normalizeSiteIDUI(item.Site) == "douyin" || item.Kind == "dynamic" || item.Kind == "live" {
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
	if a.CurrentLibraryDetail != nil && normalizeSiteIDUI(a.CurrentLibraryDetail.Site) == "douyin" && a.CurrentLibraryDetail.Kind != "live" {
		a.renderDouyinWorkRoute(c, viewportHeight)
		return
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
