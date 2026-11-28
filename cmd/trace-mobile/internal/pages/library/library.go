// Package library implements the 媒体库 tab (responsive 2/3/4-column virtualized
// grid, bottom-sheet type/site/author filters, pull-to-refresh, tap-to-view/play,
// and read-only offline item detail /library/item) for TRACE mobile (FR-MOB-012,
// NFR-MOB-PERF-001, FR-MOB-025).
package library

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/cmd/trace-mobile/internal/mediaview"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/pagekit"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/theme"
	"github.com/chialecode/trace/internal/companionapi"
	"github.com/chialecode/trace/internal/companionclient"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/mobile"
)

type filterOption struct {
	Key   string
	Label string
}

var typeOptions = []filterOption{
	{Key: "all", Label: "全部"},
	{Key: "video", Label: "视频"},
	{Key: "image", Label: "图片"},
	{Key: "live", Label: "直播录像"},
	{Key: "forward", Label: "转发"},
}

var siteOptions = []filterOption{
	{Key: "all", Label: "全部"},
	{Key: "bilibili", Label: "B 站"},
	{Key: "douyin", Label: "抖音"},
	{Key: "weibo", Label: "微博"},
	{Key: "x", Label: "X"},
}

// ColumnsForWidth returns the responsive library grid column count per
// docs/design/mobile.md §7.2: <600dp → 2 cols, 600..900dp → 3 cols, >900dp → 4 cols.
func ColumnsForWidth(width float32) int {
	switch {
	case width > 900:
		return 4
	case width >= 600:
		return 3
	default:
		return 2
	}
}

type filterState struct {
	Type       string // "all", "video", "image", "live", "forward"
	Site       string // "all", "bilibili", "douyin", "weibo", "x"
	SubID      string
	AuthorSite string
	AuthorUID  string
	SourceName string
}

func (f filterState) activeCount() int {
	n := 0
	if f.Type != "" && f.Type != "all" {
		n++
	}
	if f.Site != "" && f.Site != "all" {
		n++
	}
	if f.SubID != "" || f.AuthorUID != "" {
		n++
	}
	return n
}

func (f filterState) summaryText() string {
	parts := make([]string, 0, 3)
	if f.Type != "" && f.Type != "all" {
		parts = append(parts, labelFor(typeOptions, f.Type))
	}
	if f.Site != "" && f.Site != "all" {
		parts = append(parts, labelFor(siteOptions, f.Site))
	}
	if f.SourceName != "" {
		parts = append(parts, f.SourceName)
	}
	return strings.Join(parts, " · ")
}

// Page implements pagekit.TabPage for 媒体库 (FR-MOB-012).
type Page struct {
	mu     sync.Mutex
	gen    uint64
	loader *mediaview.ImageLoader

	filter       filterState
	sheetOpen    bool
	items        []companionapi.LibraryItemSummary
	authors      []companionapi.LibraryAuthorSummary
	total        int
	hasMore      bool
	nextOffset   int
	loading      bool
	loadingMore  bool
	loadedOnce   bool
	errMsg       string
	noticeMsg    string
	listState    ui.ListState
	refreshState ui.RefreshState

	// Read-only detail route state (/library/item/{key}).
	detailKey                  string
	detail                     *companionapi.LibraryItemDetail
	detailEditHistoryCollapsed bool
	detailLoading              bool
	detailErr                  string

	// Optional test hooks.
	openImageFn func(h pagekit.Host, items []mediaview.ImageItem, startIdx int)
	openVideoFn func(h pagekit.Host, req mediaview.VideoRequest)
	nowFn       func() time.Time
}

// New returns the 媒体库 tab page.
func New() *Page {
	return &Page{
		loader: mediaview.NewImageLoader(120),
		filter: filterState{Type: "all", Site: "all"},
		nowFn:  time.Now,
	}
}

func (p *Page) ID() pagekit.TabID { return pagekit.TabLibrary }
func (p *Page) Title() string     { return "媒体库" }

func (p *Page) OnConnected(h pagekit.Host) {
	if h == nil || h.Client() == nil {
		return
	}
	p.mu.Lock()
	p.gen++
	gen := p.gen
	p.mu.Unlock()
	p.loadPage(h, gen, true)
}

func (p *Page) OnDisconnected() {
	mediaview.CloseAllActive()
	p.loader.Clear()

	p.mu.Lock()
	p.gen++
	p.sheetOpen = false
	p.items = nil
	p.authors = nil
	p.total = 0
	p.hasMore = false
	p.nextOffset = 0
	p.loading = false
	p.loadingMore = false
	p.loadedOnce = false
	p.errMsg = ""
	p.noticeMsg = ""
	p.detailKey = ""
	p.detail = nil
	p.detailLoading = false
	p.detailErr = ""
	p.mu.Unlock()
}

func (p *Page) OnEvent(h pagekit.Host, ev companionclient.EventFrame) {
	if h == nil || h.Client() == nil {
		return
	}
	switch ev.Type {
	case companionapi.EventCacheChanged, companionapi.EventResync:
		p.mu.Lock()
		gen := p.gen
		p.mu.Unlock()
		p.loadPage(h, gen, true)
	}
}

func (p *Page) loadPage(h pagekit.Host, gen uint64, reset bool) {
	cli := h.Client()
	if cli == nil {
		return
	}
	p.mu.Lock()
	if p.gen != gen {
		p.mu.Unlock()
		return
	}
	f := p.filter
	offset := 0
	if reset {
		p.loading = true
		p.errMsg = ""
		p.noticeMsg = ""
	} else {
		if p.loadingMore || !p.hasMore {
			p.mu.Unlock()
			return
		}
		p.loadingMore = true
		p.errMsg = ""
		offset = p.nextOffset
	}
	p.mu.Unlock()
	h.Update(nil)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		q := url.Values{}
		q.Set("limit", "40")
		if offset > 0 {
			q.Set("offset", strconv.Itoa(offset))
		}
		if f.Type != "" && f.Type != "all" {
			q.Set("type", f.Type)
		}
		if f.Site != "" && f.Site != "all" {
			q.Set("site", f.Site)
		}
		wantAuthors := reset && f.SubID == "" && f.AuthorUID == ""
		if wantAuthors {
			q.Set("includeAuthors", "1")
		}
		if f.SubID != "" {
			q.Set("subId", f.SubID)
		} else if f.AuthorUID != "" {
			q.Set("authorUid", f.AuthorUID)
			if f.AuthorSite != "" {
				q.Set("authorSite", f.AuthorSite)
			}
		}

		path := companionapi.PathLibrary + "?" + q.Encode()
		var resp companionapi.LibraryListResponse
		err := cli.DoJSON(ctx, http.MethodGet, path, nil, &resp)

		p.mu.Lock()
		if p.gen != gen || p.filter != f {
			p.mu.Unlock()
			return
		}
		p.loading = false
		p.loadingMore = false
		p.loadedOnce = true
		p.refreshState.Refreshing = false
		if err != nil {
			p.errMsg = formatClientError(err, "读取媒体库失败")
		} else {
			p.errMsg = ""
			if reset {
				p.items = resp.Items
			} else {
				p.items = appendUniqueLibraryItems(p.items, resp.Items)
			}
			if wantAuthors {
				p.authors = resp.Authors
			}
			p.total = resp.Total
			p.hasMore = resp.HasMore
			if resp.NextOffset > 0 {
				p.nextOffset = resp.NextOffset
			} else {
				p.nextOffset = len(p.items)
			}
		}
		p.mu.Unlock()
		h.Update(nil)
	}()
}

func (p *Page) applyFilter(h pagekit.Host, next filterState) {
	p.mu.Lock()
	if p.filter == next {
		p.mu.Unlock()
		return
	}
	p.filter = next
	p.items = nil
	p.nextOffset = 0
	p.hasMore = false
	p.loadedOnce = false
	p.listState = ui.ListState{}
	gen := p.gen
	p.mu.Unlock()
	p.loadPage(h, gen, true)
}

func (p *Page) openItemMedia(h pagekit.Host, item companionapi.LibraryItemSummary) {
	cli := h.Client()
	if cli == nil {
		return
	}
	p.mu.Lock()
	gen := p.gen
	p.noticeMsg = ""
	p.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var det companionapi.LibraryItemDetail
		err := cli.DoJSON(ctx, http.MethodGet, companionapi.LibraryItemPath(item.ItemKey), nil, &det)

		p.mu.Lock()
		if p.gen != gen {
			p.mu.Unlock()
			return
		}
		p.mu.Unlock()

		if err == nil {
			h.Update(func() {
				p.openDetailPrimaryMedia(h, det)
			})
			return
		}
		// Fallback to summary primary media if detail fetch failed.
		if item.PrimaryRelPath != "" {
			h.Update(func() {
				if item.PrimaryKind == "image" {
					p.triggerOpenImages(h, []mediaview.ImageItem{{
						ItemKey: item.ItemKey,
						RelPath: item.PrimaryRelPath,
					}}, 0)
				} else {
					p.triggerOpenVideo(h, mediaview.VideoRequest{
						ItemKey:         item.ItemKey,
						Title:           item.Title,
						IsLiveRecording: item.Kind == "live",
						Tracks: []mediaview.VideoTrack{{
							Label:          item.Title,
							RelPath:        item.PrimaryRelPath,
							DurationSec:    item.Duration,
							CodecHint:      item.QualitySummary,
							QualitySummary: item.QualitySummary,
						}},
					})
				}
			})
			return
		}
		p.mu.Lock()
		p.noticeMsg = formatClientError(err, "无法打开媒体文件")
		p.mu.Unlock()
		h.Update(nil)
	}()
}

func (p *Page) openItemDetail(h pagekit.Host, itemKey string) {
	if itemKey == "" || h == nil {
		return
	}
	p.mu.Lock()
	p.detailKey = itemKey
	p.detail = nil
	p.detailLoading = true
	p.detailErr = ""
	gen := p.gen
	p.mu.Unlock()

	h.PushRoute("/library/item/" + url.PathEscape(itemKey))
	p.fetchDetail(h, itemKey, gen)
}

func (p *Page) fetchDetail(h pagekit.Host, itemKey string, gen uint64) {
	cli := h.Client()
	if cli == nil || itemKey == "" {
		return
	}
	p.mu.Lock()
	p.detailLoading = true
	p.detailErr = ""
	p.mu.Unlock()
	h.Update(nil)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		var det companionapi.LibraryItemDetail
		err := cli.DoJSON(ctx, http.MethodGet, companionapi.LibraryItemPath(itemKey), nil, &det)

		p.mu.Lock()
		if p.gen != gen || p.detailKey != itemKey {
			p.mu.Unlock()
			return
		}
		p.detailLoading = false
		if err != nil {
			p.detailErr = formatClientError(err, "读取条目详情失败")
		} else {
			p.detailErr = ""
			p.detail = &det
		}
		p.mu.Unlock()
		h.Update(nil)
	}()
}

// View renders the 媒体库 tab and its /library/item read-only detail route.
func (p *Page) View(c *ui.Context, h pagekit.Host) {
	r := h.Router()
	r.SystemStack = true

	ui.Column(c).Fill().Background(theme.ColorBgMain).Children(func() {
		r.View(c, func(route *ui.Route) {
			switch {
			case route.Match("/library/item/{key}"):
				key, _ := url.PathUnescape(route.Param("key"))
				p.renderDetailRoute(c, h, r, route, key)
			case route.Match("/library/item"):
				key := route.Query("key")
				p.renderDetailRoute(c, h, r, route, key)
			default:
				p.renderGridRoute(c, h, r, route)
			}
		}).Grow(1).Basis(0).FillWidth()
	})
}

func (p *Page) renderGridRoute(c *ui.Context, h pagekit.Host, r *ui.Router, route *ui.Route) {
	route.Title(p.Title())

	if h.Client() == nil {
		ui.Column(c).Fill().Background(theme.ColorBgMain).Children(func() {
			if !r.InSystemStack(c) {
				mobile.TopBar(c, mobile.TopBarOptions{Title: p.Title()})
			}
			ui.Scroll(c).Grow(1).Basis(0).FillWidth().Children(func() {
				ui.Column(c).FillWidth().Padding(32, 20).Gap(10).AlignItems(ui.Center).Children(func() {
					theme.DrawIcon(c, "library", 32, theme.ColorTextMuted, 1.7)
					ui.Text(c, "媒体库").FontSize(17).FontWeight(600).TextColor(theme.ColorTextMain)
					if name := h.Desktop().ComputerName; name != "" {
						ui.Text(c, "已连接："+name).FontSize(13).TextColor(theme.ColorAccent)
					}
					ui.Text(c, "正在等待电脑端连接就绪…").FontSize(13).TextColor(theme.ColorTextMuted)
				})
			})
		})
		return
	}

	p.mu.Lock()
	if !p.loadedOnce && !p.loading {
		gen := p.gen
		p.mu.Unlock()
		p.loadPage(h, gen, true)
		p.mu.Lock()
	}
	f := p.filter
	items := p.items
	authors := append([]companionapi.LibraryAuthorSummary(nil), p.authors...)
	total := p.total
	hasMore := p.hasMore
	loading := p.loading
	loadingMore := p.loadingMore
	loadedOnce := p.loadedOnce
	errMsg := p.errMsg
	noticeMsg := p.noticeMsg
	gen := p.gen
	p.mu.Unlock()

	activeFilters := f.activeCount()
	winW, _ := c.Size()
	cols := ColumnsForWidth(winW)

	ui.Box(c).Fill().Background(theme.ColorBgMain).Children(func() {
		ui.Column(c).Fill().Background(theme.ColorBgMain).Children(func() {
			if !r.InSystemStack(c) {
				mobile.TopBar(c, mobile.TopBarOptions{
					Title: p.Title(),
					Trailing: func() {
						p.renderFilterButton(c, activeFilters)
					},
				})
			} else {
				// Compact filter bar when SystemStack owns the top navigation bar on Android.
				ui.Row(c).
					FillWidth().
					Padding(8, 12).
					Gap(8).
					AlignItems(ui.Center).
					BorderWidth(0, 0, 1, 0).
					Border(1, theme.ColorBorder).
					Children(func() {
						summary := "全部已缓存内容"
						if activeFilters > 0 {
							summary = f.summaryText()
						}
						ui.Text(c, summary).FontSize(13).TextColor(theme.ColorTextMuted).SingleLine().Grow(1).Basis(0)
						p.renderFilterButton(c, activeFilters)
					})
			}

			// Active filter bar when TopBar is shown and filters are active.
			if !r.InSystemStack(c) && activeFilters > 0 {
				ui.Row(c).
					FillWidth().
					Padding(6, 12).
					Gap(8).
					Background(theme.ColorBgPanel).
					BorderWidth(0, 0, 1, 0).
					Border(1, theme.ColorBorder).
					AlignItems(ui.Center).
					Children(func() {
						ui.Text(c, f.summaryText()).FontSize(12).FontWeight(500).TextColor(theme.ColorAccent).SingleLine().Grow(1).Basis(0)
						clearBtn := ui.Button(c, "").
							Label("清除筛选").
							MinHeight(32).Padding(4, 10).Radius(999).
							Background(theme.ColorBgMain).Border(1, theme.ColorBorderStrong)
						clearBtn.Children(func() {
							ui.Text(c, "清除").FontSize(12).TextColor(theme.ColorTextMain).SingleLine()
						})
						if clearBtn.Clicked() {
							p.applyFilter(h, filterState{Type: "all", Site: "all"})
						}
					})
			}

			if noticeMsg != "" {
				ui.Row(c).FillWidth().Background(theme.ColorErrorBg).Padding(8, 12).Gap(8).AlignItems(ui.Center).Children(func() {
					theme.DrawIcon(c, "alert-circle", 16, theme.ColorError, 1.8)
					ui.Text(c, noticeMsg).FontSize(12).TextColor(theme.ColorError).Grow(1).Basis(0)
				})
			}

			switch {
			case loading && len(items) == 0:
				p.renderGridSkeleton(c, cols)
			case errMsg != "" && len(items) == 0:
				ui.Column(c).Grow(1).Basis(0).FillWidth().Justify(ui.Center).AlignItems(ui.Center).Padding(32, 24).Gap(12).Children(func() {
					theme.DrawIcon(c, "alert-circle", 32, theme.ColorError, 1.7)
					ui.Text(c, errMsg).FontSize(14).LineHeight(theme.LineHeightCompact).TextColor(theme.ColorTextMain).TextAlign(ui.Center)
					if theme.PrimaryPillButton(c, "重试", "重试加载媒体库").Clicked() {
						p.loadPage(h, gen, true)
					}
				})
			case loadedOnce && len(items) == 0:
				msg := "媒体库还是空的"
				sub := "在电脑端 TRACE 缓存视频、图片或直播录像后，这里就能离线浏览。"
				if activeFilters > 0 {
					msg = "当前筛选条件下没有内容"
					sub = "试试切换其他类型、站点或来源。"
				}
				ui.Column(c).Grow(1).Basis(0).FillWidth().Justify(ui.Center).AlignItems(ui.Center).Padding(36, 24).Gap(10).Children(func() {
					theme.DrawIcon(c, "library", 32, theme.ColorTextFaint, 1.6)
					ui.Text(c, msg).FontSize(16).FontWeight(600).TextColor(theme.ColorTextMain)
					ui.Text(c, sub).FontSize(13).LineHeight(theme.LineHeightCompact).TextColor(theme.ColorTextMuted).TextAlign(ui.Center)
					if activeFilters > 0 {
						if theme.SecondaryPillButton(c, "清除筛选", "清除全部筛选").Clicked() {
							p.applyFilter(h, filterState{Type: "all", Site: "all"})
						}
					}
				})
			default:
				p.renderVirtualizedGrid(c, h, items, cols, total, hasMore, loadingMore, errMsg, gen)
			}
		})

		// Filter bottom sheet (ui/mobile.Sheet).
		p.renderFilterSheet(c, h, f, authors)
	})
}

func (p *Page) renderFilterButton(c *ui.Context, activeCount int) {
	bg := theme.ColorBgSecondary
	txtCol := theme.ColorTextMain
	text := "筛选"
	if activeCount > 0 {
		bg = theme.ColorAccentBg
		txtCol = theme.ColorAccent
		text = fmt.Sprintf("筛选 (%d)", activeCount)
	}
	btn := ui.Button(c, "").
		Label("打开筛选").
		MinHeight(36).
		Padding(6, 12).
		Radius(999).
		Background(bg).
		Justify(ui.Center).
		AlignItems(ui.Center).
		Shrink(0)
	btn.Children(func() {
		ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
			mediaview.DrawIcon(c, "filter", 15, txtCol, 1.8)
			ui.Text(c, text).FontSize(13).FontWeight(600).TextColor(txtCol).SingleLine()
		})
	})
	if btn.Clicked() {
		p.sheetOpen = true
	}
}

func (p *Page) renderVirtualizedGrid(
	c *ui.Context,
	h pagekit.Host,
	items []companionapi.LibraryItemSummary,
	cols int,
	total int,
	hasMore bool,
	loadingMore bool,
	errMsg string,
	gen uint64,
) {
	gridRows := (len(items) + cols - 1) / cols
	totalRows := gridRows + 1 // +1 footer row

	p.listState.Key = func(row int) any {
		if row < gridRows {
			firstIdx := row * cols
			if firstIdx < len(items) {
				return fmt.Sprintf("r-%d-%s", cols, items[firstIdx].ItemKey)
			}
		}
		return "lib-footer"
	}
	p.listState.NearEnd = 3
	p.listState.EndReached = func(int) {
		p.loadPage(h, gen, false)
	}

	listEl := ui.List(c, &p.listState, totalRows, func(row int) {
		if row < gridRows {
			start := row * cols
			ui.Row(c).FillWidth().Padding(6, 10).Gap(10).AlignItems(ui.Start).Children(func() {
				for col := 0; col < cols; col++ {
					idx := start + col
					if idx < len(items) {
						p.renderGridCard(c, h, items[idx])
					} else {
						ui.Box(c.Key("empty-" + strconv.Itoa(col))).Grow(1).Basis(0)
					}
				}
			})
			return
		}
		// Footer row.
		ui.Column(c).FillWidth().Padding(16, 16).AlignItems(ui.Center).Gap(8).Children(func() {
			switch {
			case loadingMore:
				ui.Text(c, "正在加载更多…").FontSize(13).TextColor(theme.ColorTextMuted)
			case errMsg != "":
				ui.Text(c, errMsg).FontSize(13).TextColor(theme.ColorError).TextAlign(ui.Center)
				if theme.SecondaryPillButton(c, "重试", "重试加载下一页").Clicked() {
					p.loadPage(h, gen, false)
				}
			case !hasMore:
				count := len(items)
				if total > count {
					count = total
				}
				ui.Text(c, fmt.Sprintf("共 %d 项 · 已显示全部", count)).FontSize(12).TextColor(theme.ColorTextFaint)
			default:
				ui.Box(c).Height(12)
			}
		})
	}).Grow(1).Basis(0).FillWidth()

	if listEl.PullToRefresh(&p.refreshState) {
		p.refreshState.Refreshing = true
		p.loadPage(h, gen, true)
	}
}

func (p *Page) renderGridCard(c *ui.Context, h pagekit.Host, item companionapi.LibraryItemSummary) {
	coverRel := item.CoverRelPath
	if coverRel == "" {
		coverRel = item.PrimaryRelPath
	}
	var coverSrc ui.ImageSource
	if coverRel != "" {
		coverSrc, _, _ = p.loader.GetOrLoad(h, mediaview.MediaThumbPath(item.ItemKey, coverRel))
	}
	title := strings.TrimSpace(item.Title)
	if title == "" {
		title = item.AuthorName
	}
	if title == "" {
		title = "未命名条目"
	}

	isVideoLike := item.Kind == "video" || item.Kind == "live" || item.PrimaryKind == "video" || item.PrimaryKind == "live"
	durTxt := mediaview.FormatDuration(item.Duration)

	card := ui.Column(c.Key(item.ItemKey)).
		Grow(1).Basis(0).
		Gap(6).
		Background(theme.ColorBgMain)

	card.Children(func() {
		// 16:9 thumbnail box (tap to open viewer/player, long-press to open /library/item).
		thumb := ui.Box(c).
			FillWidth().
			AspectRatio(16.0 / 9.0).
			Radius(12).
			Clip().
			Background(theme.ColorBgSecondary).
			Justify(ui.Center).
			AlignItems(ui.Center).
			Label("打开 " + title)
		thumb.Children(func() {
			if coverSrc != nil {
				ui.Image(c, coverSrc).Fit(ui.Cover).Absolute().Top(0).Left(0).Right(0).Bottom(0)
			}
			if isVideoLike {
				ui.Box(c).
					Size(32, 32).Radius(16).ZIndex(2).
					Background(ui.RGBA(0, 0, 0, 0.52)).
					Justify(ui.Center).AlignItems(ui.Center).
					Children(func() {
						mediaview.DrawIcon(c, "play", 14, theme.ColorWhite, 1.8)
					})
			}
			if item.Kind == "live" {
				ui.Box(c).
					Absolute().Top(6).Left(6).ZIndex(2).
					Padding(2, 6).Radius(4).
					Background(theme.ColorLive).
					Children(func() {
						ui.Text(c, "直播录像").FontSize(10).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()
					})
			}
			if durTxt != "" {
				ui.Box(c).
					Absolute().Right(6).Bottom(6).ZIndex(2).
					Padding(2, 5).Radius(4).
					Background(ui.RGBA(0, 0, 0, 0.7)).
					Children(func() {
						ui.Text(c, durTxt).FontSize(10).FontWeight(500).TextColor(theme.ColorWhite).SingleLine()
					})
			} else if item.ImageCount > 1 {
				ui.Box(c).
					Absolute().Right(6).Bottom(6).ZIndex(2).
					Padding(2, 5).Radius(4).
					Background(ui.RGBA(0, 0, 0, 0.65)).
					Children(func() {
						ui.Text(c, fmt.Sprintf("%d 图", item.ImageCount)).FontSize(10).FontWeight(500).TextColor(theme.ColorWhite).SingleLine()
					})
			}
		})
		if thumb.LongPressed() {
			p.openItemDetail(h, item.ItemKey)
		} else if thumb.Clicked() {
			p.openItemMedia(h, item)
		}

		// 2-line title (tap opens viewer/player; long-press opens detail).
		titleBox := ui.Box(c).FillWidth().Label("标题 " + title)
		titleBox.Children(func() {
			ui.Text(c, title).
				FontSize(13).
				FontWeight(600).
				LineHeight(theme.LineHeightHeading).
				TextColor(theme.ColorTextMain).
				MaxLines(2)
		})
		if titleBox.LongPressed() {
			p.openItemDetail(h, item.ItemKey)
		} else if titleBox.Clicked() {
			p.openItemMedia(h, item)
		}

		// Meta row + "⋯" button opening read-only /library/item detail.
		metaParts := make([]string, 0, 3)
		if item.AuthorName != "" {
			metaParts = append(metaParts, item.AuthorName)
		}
		if s := mediaview.SiteLabel(item.Site); s != "" {
			metaParts = append(metaParts, s)
		}
		if item.Kind == "live" && item.QualitySummary != "" {
			metaParts = append(metaParts, item.QualitySummary)
		}

		ui.Row(c).FillWidth().Gap(4).AlignItems(ui.Center).Children(func() {
			ui.Text(c, strings.Join(metaParts, " · ")).
				FontSize(11).
				TextColor(theme.ColorTextMuted).
				SingleLine().
				Grow(1).Basis(0)

			moreBtn := ui.Button(c, "").
				Label("详情 "+title).
				Size(32, 32).
				Radius(16).
				Background(ui.Color{}).
				Border(0, ui.Color{}).
				Justify(ui.Center).
				AlignItems(ui.Center).
				Shrink(0)
			moreBtn.Children(func() {
				mediaview.DrawIcon(c, "more", 16, theme.ColorTextMuted, 1.8)
			})
			if moreBtn.Clicked() {
				p.openItemDetail(h, item.ItemKey)
			}
		})
	})
}

func (p *Page) renderFilterSheet(
	c *ui.Context,
	h pagekit.Host,
	cur filterState,
	authors []companionapi.LibraryAuthorSummary,
) {
	bg := theme.ColorBgMain
	mobile.Sheet(c, &p.sheetOpen, mobile.SheetOptions{
		Detents:    []float32{0.68, 0.92},
		Background: &bg,
		Label:      "筛选媒体库",
	}, func() {
		ui.Column(c).Fill().Padding(8, 16).Gap(12).Children(func() {
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Shrink(0).Children(func() {
				ui.Text(c, "筛选媒体库").FontSize(16).FontWeight(600).TextColor(theme.ColorTextMain).Grow(1).Basis(0)
				if cur.activeCount() > 0 {
					resetBtn := ui.Button(c, "").
						Label("重置筛选").
						MinHeight(38).Padding(4, 12).Radius(999).
						Background(theme.ColorBgSecondary)
					resetBtn.Children(func() {
						ui.Text(c, "重置").FontSize(13).FontWeight(500).TextColor(theme.ColorTextMain)
					})
					if resetBtn.Clicked() {
						p.applyFilter(h, filterState{Type: "all", Site: "all"})
					}
				}
				doneBtn := ui.PrimaryButton(c, "").
					Label("完成筛选").
					MinHeight(38).Padding(4, 14).Radius(999).
					Background(theme.ColorAccent).TextColor(theme.ColorWhite).
					Justify(ui.Center).AlignItems(ui.Center)
				doneBtn.Children(func() {
					ui.Text(c, "完成").FontSize(13).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()
				})
				if doneBtn.Clicked() {
					p.sheetOpen = false
				}
			})

			ui.Scroll(c).Grow(1).Basis(0).FillWidth().Children(func() {
				ui.Column(c).FillWidth().Gap(16).Padding(0, 0).Children(func() {
					// Type filter chips.
					ui.Column(c).FillWidth().Gap(8).Children(func() {
						ui.Text(c, "类型").FontSize(13).FontWeight(600).TextColor(theme.ColorTextMuted)
						ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
							for _, opt := range typeOptions {
								selected := cur.Type == opt.Key
								if filterChip(c, "type-"+opt.Key, opt.Label, "类型 "+opt.Label, selected).Clicked() {
									next := cur
									next.Type = opt.Key
									p.applyFilter(h, next)
								}
							}
						})
					})

					// Site filter chips.
					ui.Column(c).FillWidth().Gap(8).Children(func() {
						ui.Text(c, "站点").FontSize(13).FontWeight(600).TextColor(theme.ColorTextMuted)
						ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
							for _, opt := range siteOptions {
								selected := cur.Site == opt.Key
								if filterChip(c, "site-"+opt.Key, opt.Label, "站点 "+opt.Label, selected).Clicked() {
									next := cur
									next.Site = opt.Key
									p.applyFilter(h, next)
								}
							}
						})
					})

					// Subscription / Author source list (scrollable inside Sheet for 30+ sources).
					ui.Column(c).FillWidth().Gap(8).Children(func() {
						ui.Text(c, "订阅 / 作者").FontSize(13).FontWeight(600).TextColor(theme.ColorTextMuted)
						allSel := cur.SubID == "" && cur.AuthorUID == ""
						if sourceRow(c, "src-all", "全部来源", "", allSel).Clicked() {
							next := cur
							next.SubID = ""
							next.AuthorSite = ""
							next.AuthorUID = ""
							next.SourceName = ""
							p.applyFilter(h, next)
						}
						for i, au := range authors {
							selected := (au.SubID != "" && cur.SubID == au.SubID) ||
								(au.SubID == "" && au.UID != "" && cur.AuthorUID == au.UID && cur.AuthorSite == au.Site)
							meta := fmt.Sprintf("%d 项", au.ItemCount)
							if sz := mediaview.FormatBytes(au.TotalBytes); sz != "" {
								meta += " · " + sz
							}
							if au.Site != "" && au.SubID == "" {
								meta = mediaview.SiteLabel(au.Site) + " · " + meta
							}
							if sourceRow(c, fmt.Sprintf("src-%d", i), au.Name, meta, selected).Clicked() {
								next := cur
								if au.SubID != "" {
									next.SubID = au.SubID
									next.AuthorSite = ""
									next.AuthorUID = ""
								} else {
									next.SubID = ""
									next.AuthorSite = au.Site
									next.AuthorUID = au.UID
								}
								next.SourceName = au.Name
								p.applyFilter(h, next)
							}
						}
					})
				})
			})
		})
	})
}

func filterChip(c *ui.Context, key, text, label string, selected bool) ui.Element {
	bg := theme.ColorBgSecondary
	txtCol := theme.ColorTextMain
	borderCol := theme.ColorBorder
	weight := 500
	if selected {
		bg = theme.ColorAccentBg
		txtCol = theme.ColorAccent
		borderCol = theme.ColorAccent
		weight = 600
	}
	btn := ui.Button(c.Key(key), "").
		Label(label).
		MinHeight(38).
		Padding(6, 14).
		Radius(999).
		Background(bg).
		Border(1, borderCol).
		Justify(ui.Center).
		AlignItems(ui.Center)
	btn.Children(func() {
		ui.Text(c, text).FontSize(13).FontWeight(weight).TextColor(txtCol).SingleLine()
	})
	return btn
}

func sourceRow(c *ui.Context, key, name, meta string, selected bool) ui.Element {
	bg := theme.ColorBgMain
	borderCol := theme.ColorBorder
	txtCol := theme.ColorTextMain
	if selected {
		bg = theme.ColorAccentBg
		borderCol = theme.ColorAccent
		txtCol = theme.ColorAccent
	}
	btn := ui.Button(c.Key(key), "").
		Label("来源 "+name).
		FillWidth().
		MinHeight(44).
		Padding(8, 12).
		Radius(10).
		Background(bg).
		Border(1, borderCol).
		Justify(ui.Start).
		AlignItems(ui.Center)
	btn.Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, name).FontSize(14).FontWeight(500).TextColor(txtCol).SingleLine().Grow(1).Basis(0)
			if meta != "" {
				ui.Text(c, meta).FontSize(12).TextColor(theme.ColorTextMuted).SingleLine()
			}
		})
	})
	return btn
}

func (p *Page) renderGridSkeleton(c *ui.Context, cols int) {
	ui.Column(c).Grow(1).Basis(0).FillWidth().Padding(10, 10).Gap(12).Children(func() {
		for r := 0; r < 3; r++ {
			ui.Row(c.Key(r)).FillWidth().Gap(10).Children(func() {
				for col := 0; col < cols; col++ {
					ui.Column(c.Key(col)).Grow(1).Basis(0).Gap(6).Children(func() {
						ui.Box(c).FillWidth().AspectRatio(16.0 / 9.0).Radius(12).Background(theme.ColorBgSecondary)
						ui.Box(c).FillWidth().Height(14).Radius(5).Background(theme.ColorBgSecondary)
						ui.Box(c).WidthPercent(65).Height(11).Radius(5).Background(theme.ColorBgSecondary)
					})
				}
			})
		}
	})
}

func (p *Page) triggerOpenImages(h pagekit.Host, items []mediaview.ImageItem, idx int) {
	if p.openImageFn != nil {
		p.openImageFn(h, items, idx)
		return
	}
	mediaview.OpenImageViewer(h, items, idx)
}

func (p *Page) triggerOpenVideo(h pagekit.Host, req mediaview.VideoRequest) {
	if p.openVideoFn != nil {
		p.openVideoFn(h, req)
		return
	}
	mediaview.OpenVideoPlayer(h, req)
}

func labelFor(opts []filterOption, key string) string {
	for _, o := range opts {
		if o.Key == key {
			return o.Label
		}
	}
	return key
}

func appendUniqueLibraryItems(base, extra []companionapi.LibraryItemSummary) []companionapi.LibraryItemSummary {
	seen := make(map[string]bool, len(base))
	for _, it := range base {
		seen[it.ItemKey] = true
	}
	out := base
	for _, it := range extra {
		if seen[it.ItemKey] {
			continue
		}
		seen[it.ItemKey] = true
		out = append(out, it)
	}
	return out
}

func formatClientError(err error, fallback string) string {
	if ce, ok := err.(*companionclient.Error); ok && ce.Message != "" {
		return ce.Message
	}
	if err == nil {
		return fallback
	}
	return fmt.Sprintf("%s：%s", fallback, err.Error())
}
