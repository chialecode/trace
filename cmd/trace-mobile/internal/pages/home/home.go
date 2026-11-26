// Package home implements the 主页 tab (cached home feed, site filter,
// horizontal subscription avatar strip, shared read state, and per-device
// reading position persistence) for TRACE mobile (FR-MOB-011, NFR-MOB-PERF-001).
package home

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

type siteFilterOption struct {
	Key   string
	Label string
}

var siteFilters = []siteFilterOption{
	{Key: "all", Label: "全部"},
	{Key: "bilibili", Label: "B 站"},
	{Key: "douyin", Label: "抖音"},
	{Key: "weibo", Label: "微博"},
	{Key: "x", Label: "X"},
}

type feedSliceState struct {
	items           []companionapi.FeedItem
	nextCursor      *companionapi.FeedCursor
	hasMore         bool
	hasNewer        bool
	loading         bool
	loadingMore     bool
	loadedOnce      bool
	errMsg          string
	listState       ui.ListState
	refreshState    ui.RefreshState
	restoredPos     bool
	lastTrackedKey  string
	pendingScrollTo int // >= 0 when awaiting initial scroll restore
}

// Page implements pagekit.TabPage for 主页 (FR-MOB-011).
type Page struct {
	mu     sync.Mutex
	gen    uint64
	loader *mediaview.ImageLoader

	activeSite     string
	feeds          map[string]*feedSliceState // keyed by site ("all", "bilibili", ...)
	subs           []companionapi.FeedSubscriptionRow
	subsSiteLoaded string
	subsLoading    bool
	subsErr        string
	seenErr        string

	savedPositions map[string]companionapi.HomePosition
	positionsReady bool
	posSaveTimer   *time.Timer
	posSaveDelay   time.Duration

	// Single-subscription view state.
	activeSubID   string
	activeSubRow  companionapi.FeedSubscriptionRow
	subFeed       feedSliceState
	expandedPosts map[string]bool

	// Optional test hooks.
	openImageFn func(h pagekit.Host, items []mediaview.ImageItem, startIdx int)
	openVideoFn func(h pagekit.Host, req mediaview.VideoRequest)
	nowFn       func() time.Time
}

// New returns the 主页 tab page.
func New() *Page {
	p := &Page{
		loader:         mediaview.NewImageLoader(120),
		activeSite:     "all",
		feeds:          make(map[string]*feedSliceState),
		savedPositions: make(map[string]companionapi.HomePosition),
		expandedPosts:  make(map[string]bool),
		posSaveDelay:   900 * time.Millisecond,
		nowFn:          time.Now,
	}
	p.subFeed.pendingScrollTo = -1
	return p
}

func (p *Page) ID() pagekit.TabID { return pagekit.TabHome }
func (p *Page) Title() string     { return "主页" }

func (p *Page) feedForLocked(site string) *feedSliceState {
	if site == "" {
		site = "all"
	}
	st, ok := p.feeds[site]
	if !ok {
		st = &feedSliceState{pendingScrollTo: -1}
		p.feeds[site] = st
	}
	return st
}

// OnConnected loads saved reading positions, subscription strip, and initial feed page.
func (p *Page) OnConnected(h pagekit.Host) {
	if h == nil || h.Client() == nil {
		return
	}
	p.mu.Lock()
	p.gen++
	gen := p.gen
	site := p.activeSite
	if site == "" {
		site = "all"
		p.activeSite = site
	}
	p.positionsReady = false
	p.mu.Unlock()

	p.fetchSubscriptions(h, site, gen, false)
	p.fetchPositionsAndInitialFeed(h, site, gen)
}

// OnDisconnected immediately clears all in-memory feed data, decoded images,
// and closes any active viewer/player (FR-MOB-006, NFR-MOB-DATA-001).
func (p *Page) OnDisconnected() {
	mediaview.CloseAllActive()
	p.loader.Clear()

	p.mu.Lock()
	p.gen++
	if p.posSaveTimer != nil {
		p.posSaveTimer.Stop()
		p.posSaveTimer = nil
	}
	clear(p.feeds)
	p.subs = nil
	p.subsSiteLoaded = ""
	p.subsLoading = false
	p.subsErr = ""
	p.seenErr = ""
	clear(p.savedPositions)
	p.positionsReady = false
	p.activeSubID = ""
	p.activeSubRow = companionapi.FeedSubscriptionRow{}
	p.subFeed = feedSliceState{pendingScrollTo: -1}
	clear(p.expandedPosts)
	p.mu.Unlock()
}

// OnEvent responds to WebSocket events from desktop (feed:changed, live:changed, cache:changed).
func (p *Page) OnEvent(h pagekit.Host, ev companionclient.EventFrame) {
	if h == nil || h.Client() == nil {
		return
	}
	switch ev.Type {
	case companionapi.EventFeedChanged:
		if strings.HasPrefix(ev.Key, "sub:") {
			subID := strings.TrimPrefix(ev.Key, "sub:")
			p.mu.Lock()
			for i := range p.subs {
				if p.subs[i].SubID == subID {
					p.subs[i].Unread = false
				}
			}
			p.mu.Unlock()
			h.Update(nil)
			return
		}
		p.mu.Lock()
		gen := p.gen
		site := p.activeSite
		p.mu.Unlock()
		p.fetchSubscriptions(h, site, gen, true)
	case companionapi.EventLiveChanged, companionapi.EventSubsChanged, companionapi.EventCacheChanged, companionapi.EventResync:
		p.mu.Lock()
		gen := p.gen
		site := p.activeSite
		p.mu.Unlock()
		p.fetchSubscriptions(h, site, gen, true)
	}
}

func (p *Page) fetchPositionsAndInitialFeed(h pagekit.Host, site string, gen uint64) {
	cli := h.Client()
	if cli == nil {
		return
	}
	p.mu.Lock()
	st := p.feedForLocked(site)
	st.loading = true
	st.errMsg = ""
	p.mu.Unlock()
	h.Update(nil)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var posResp companionapi.HomePositionsResponse
		if err := cli.DoJSON(ctx, http.MethodGet, companionapi.PathFeedPosition, nil, &posResp); err == nil {
			p.mu.Lock()
			if p.gen == gen {
				p.savedPositions = make(map[string]companionapi.HomePosition, len(posResp.Positions))
				for k, v := range posResp.Positions {
					p.savedPositions[k] = v
				}
				p.positionsReady = true
			}
			p.mu.Unlock()
		}
		p.loadFeedPage(h, site, gen, true, false)
	}()
}

func (p *Page) loadFeedPage(h pagekit.Host, site string, gen uint64, reset bool, userRefresh bool) {
	cli := h.Client()
	if cli == nil {
		return
	}

	p.mu.Lock()
	if p.gen != gen {
		p.mu.Unlock()
		return
	}
	st := p.feedForLocked(site)
	var cursor *companionapi.FeedCursor
	var savedPos companionapi.HomePosition
	wantRestore := false
	if reset {
		st.loading = true
		st.errMsg = ""
		if !userRefresh && !st.restoredPos {
			if pos, ok := p.savedPositions[site]; ok && pos.Key != "" {
				savedPos = pos
				wantRestore = true
			}
		}
	} else {
		if st.loadingMore || !st.hasMore || st.nextCursor == nil {
			p.mu.Unlock()
			return
		}
		st.loadingMore = true
		st.errMsg = ""
		cCopy := *st.nextCursor
		cursor = &cCopy
	}
	p.mu.Unlock()
	h.Update(nil)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		resp, err := fetchFeed(ctx, cli, site, "", cursor, "older", 30)
		if err != nil {
			p.mu.Lock()
			if p.gen == gen {
				st := p.feedForLocked(site)
				st.loading = false
				st.loadingMore = false
				st.refreshState.Refreshing = false
				st.errMsg = formatClientError(err, "读取主页信息流失败")
			}
			p.mu.Unlock()
			h.Update(nil)
			return
		}

		// If restoring a saved reading position that is older than page 1, jump directly
		// to the saved timestamp cursor so restoration finishes well within 1-2s even on 5000 items.
		restoreIdx := -1
		hasNewer := false
		if reset && wantRestore {
			restoreIdx = findFeedIndex(resp.Items, savedPos.Key, savedPos.PubTs)
			if restoreIdx < 0 && savedPos.PubTs > 0 && len(resp.Items) > 0 && resp.Items[len(resp.Items)-1].PubTs > savedPos.PubTs {
				jumpCursor := &companionapi.FeedCursor{PubTs: savedPos.PubTs + 1, Key: ""}
				if jumped, jErr := fetchFeed(ctx, cli, site, "", jumpCursor, "older", 30); jErr == nil && len(jumped.Items) > 0 {
					resp = jumped
					restoreIdx = findFeedIndex(resp.Items, savedPos.Key, savedPos.PubTs)
					if restoreIdx < 0 {
						restoreIdx = 0
					}
					hasNewer = true
				}
			}
		}

		p.mu.Lock()
		if p.gen != gen {
			p.mu.Unlock()
			return
		}
		st := p.feedForLocked(site)
		st.loading = false
		st.loadingMore = false
		st.loadedOnce = true
		st.refreshState.Refreshing = false
		st.errMsg = ""
		if reset {
			st.items = resp.Items
			st.hasNewer = hasNewer
			if wantRestore {
				st.restoredPos = true
				if restoreIdx >= 0 && restoreIdx < len(st.items) {
					st.pendingScrollTo = restoreIdx
					st.lastTrackedKey = st.items[restoreIdx].FeedKey
				}
			}
		} else {
			st.items = appendUniqueFeedItems(st.items, resp.Items)
		}
		st.nextCursor = resp.NextCursor
		st.hasMore = resp.HasMore
		p.mu.Unlock()
		h.Update(nil)
	}()
}

func (p *Page) fetchSubscriptions(h pagekit.Host, site string, gen uint64, preserveOrder bool) {
	cli := h.Client()
	if cli == nil {
		return
	}
	p.mu.Lock()
	if p.gen != gen {
		p.mu.Unlock()
		return
	}
	p.subsLoading = true
	p.subsErr = ""
	p.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		q := url.Values{}
		if site != "" && site != "all" {
			q.Set("site", site)
		}
		path := companionapi.PathFeedSubscriptions
		if encoded := q.Encode(); encoded != "" {
			path += "?" + encoded
		}
		var resp companionapi.FeedSubscriptionsResponse
		err := cli.DoJSON(ctx, http.MethodGet, path, nil, &resp)

		p.mu.Lock()
		if p.gen != gen {
			p.mu.Unlock()
			return
		}
		p.subsLoading = false
		if err != nil {
			p.subsErr = formatClientError(err, "读取订阅列表失败")
		} else {
			p.subsErr = ""
			p.subsSiteLoaded = site
			if preserveOrder && len(p.subs) > 0 {
				// Per docs/ui.md §3.1: keep existing row order during stay on home;
				// only update live/unread badges and append newly appearing subs at the end.
				freshByID := make(map[string]companionapi.FeedSubscriptionRow, len(resp.Items))
				for _, r := range resp.Items {
					freshByID[r.SubID] = r
				}
				merged := make([]companionapi.FeedSubscriptionRow, 0, len(resp.Items))
				seen := make(map[string]bool, len(p.subs))
				for _, old := range p.subs {
					if upd, ok := freshByID[old.SubID]; ok {
						merged = append(merged, upd)
						seen[old.SubID] = true
					}
				}
				for _, r := range resp.Items {
					if !seen[r.SubID] {
						merged = append(merged, r)
					}
				}
				p.subs = merged
			} else {
				p.subs = resp.Items
			}
		}
		p.mu.Unlock()
		h.Update(nil)
	}()
}

func (p *Page) openSubscription(h pagekit.Host, subID, name, avatarPath string, latestPubTs int64) {
	if subID == "" || h == nil {
		return
	}
	p.flushCurrentPosition(h)

	p.mu.Lock()
	gen := p.gen
	site := p.activeSite
	var row companionapi.FeedSubscriptionRow
	found := false
	for i := range p.subs {
		if p.subs[i].SubID == subID {
			row = p.subs[i]
			p.subs[i].Unread = false // clear unread dot immediately without reordering
			found = true
			break
		}
	}
	if !found {
		row = companionapi.FeedSubscriptionRow{
			SubID:       subID,
			Name:        name,
			AvatarPath:  avatarPath,
			LatestPubTs: latestPubTs,
		}
	}
	if row.LatestPubTs <= 0 {
		row.LatestPubTs = latestPubTs
	}
	p.activeSubID = subID
	p.activeSubRow = row
	p.seenErr = ""
	p.subFeed = feedSliceState{pendingScrollTo: -1, loading: true}
	p.mu.Unlock()

	h.PushRoute("/sub/" + url.PathEscape(subID))

	// Load single-subscription feed & mark shared read via POST /api/v1/feed/seen.
	p.loadSubFeedPage(h, subID, site, gen, true)
	p.markSubscriptionSeen(h, subID, row.LatestPubTs, gen)
}

func (p *Page) markSubscriptionSeen(h pagekit.Host, subID string, seenTs int64, gen uint64) {
	cli := h.Client()
	if cli == nil || subID == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := cli.DoJSON(ctx, http.MethodPost, companionapi.PathFeedSeen, companionapi.FeedSeenRequest{
			SubID:  subID,
			SeenTs: seenTs,
		}, nil)
		if err != nil {
			p.mu.Lock()
			if p.gen == gen {
				p.seenErr = formatClientError(err, "保存已读状态失败")
			}
			p.mu.Unlock()
			h.Update(nil)
		}
	}()
}

func (p *Page) loadSubFeedPage(h pagekit.Host, subID, site string, gen uint64, reset bool) {
	cli := h.Client()
	if cli == nil {
		return
	}
	p.mu.Lock()
	if p.gen != gen || p.activeSubID != subID {
		p.mu.Unlock()
		return
	}
	var cursor *companionapi.FeedCursor
	if reset {
		p.subFeed.loading = true
		p.subFeed.errMsg = ""
	} else {
		if p.subFeed.loadingMore || !p.subFeed.hasMore || p.subFeed.nextCursor == nil {
			p.mu.Unlock()
			return
		}
		p.subFeed.loadingMore = true
		p.subFeed.errMsg = ""
		cCopy := *p.subFeed.nextCursor
		cursor = &cCopy
	}
	p.mu.Unlock()
	h.Update(nil)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		resp, err := fetchFeed(ctx, cli, site, subID, cursor, "older", 30)
		p.mu.Lock()
		if p.gen != gen || p.activeSubID != subID {
			p.mu.Unlock()
			return
		}
		p.subFeed.loading = false
		p.subFeed.loadingMore = false
		p.subFeed.loadedOnce = true
		p.subFeed.refreshState.Refreshing = false
		if err != nil {
			p.subFeed.errMsg = formatClientError(err, "读取订阅内容失败")
		} else {
			p.subFeed.errMsg = ""
			if reset {
				p.subFeed.items = resp.Items
			} else {
				p.subFeed.items = appendUniqueFeedItems(p.subFeed.items, resp.Items)
			}
			p.subFeed.nextCursor = resp.NextCursor
			p.subFeed.hasMore = resp.HasMore
		}
		p.mu.Unlock()
		h.Update(nil)
	}()
}

func (p *Page) selectSite(h pagekit.Host, site string) {
	p.flushCurrentPosition(h)

	p.mu.Lock()
	if p.activeSite == site {
		p.mu.Unlock()
		return
	}
	p.activeSite = site
	gen := p.gen
	st := p.feedForLocked(site)
	needLoad := !st.loadedOnce && !st.loading
	p.mu.Unlock()

	p.fetchSubscriptions(h, site, gen, true)
	if needLoad {
		p.loadFeedPage(h, site, gen, true, false)
	}
}

func (p *Page) trackVisiblePosition(h pagekit.Host, site string, st *feedSliceState) {
	if len(st.items) == 0 {
		return
	}
	first, _ := st.listState.Visible()
	if first < 0 || first >= len(st.items) {
		return
	}
	item := st.items[first]
	if item.FeedKey == "" || item.FeedKey == st.lastTrackedKey {
		return
	}
	st.lastTrackedKey = item.FeedKey
	pos := companionapi.HomePosition{
		Site:     site,
		Key:      item.FeedKey,
		PubTs:    item.PubTs,
		OffsetPx: 0,
	}
	p.savedPositions[site] = pos

	delay := p.posSaveDelay
	if p.posSaveTimer != nil {
		p.posSaveTimer.Stop()
	}
	gen := p.gen
	p.posSaveTimer = time.AfterFunc(delay, func() {
		p.savePosition(h, pos, gen)
	})
}

func (p *Page) flushCurrentPosition(h pagekit.Host) {
	p.mu.Lock()
	if p.posSaveTimer != nil {
		p.posSaveTimer.Stop()
		p.posSaveTimer = nil
	}
	site := p.activeSite
	pos, ok := p.savedPositions[site]
	gen := p.gen
	p.mu.Unlock()
	if ok && pos.Key != "" {
		p.savePosition(h, pos, gen)
	}
}

func (p *Page) savePosition(h pagekit.Host, pos companionapi.HomePosition, gen uint64) {
	if h == nil {
		return
	}
	cli := h.Client()
	if cli == nil || pos.Key == "" {
		return
	}
	p.mu.Lock()
	if p.gen != gen {
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = cli.DoJSON(ctx, http.MethodPut, companionapi.PathFeedPosition, pos, nil)
	}()
}

// View renders the 主页 tab including its nested SystemStack routes ("/" and "/sub/{id}").
func (p *Page) View(c *ui.Context, h pagekit.Host) {
	r := h.Router()
	r.SystemStack = true

	ui.Column(c).Fill().Background(theme.ColorBgMain).Children(func() {
		r.View(c, func(route *ui.Route) {
			switch {
			case route.Match("/sub/{id}"):
				subID, _ := url.PathUnescape(route.Param("id"))
				p.renderSubRoute(c, h, r, route, subID)
			default:
				p.renderMainRoute(c, h, r, route)
			}
		}).Grow(1).Basis(0).FillWidth()
	})
}

func (p *Page) renderMainRoute(c *ui.Context, h pagekit.Host, r *ui.Router, route *ui.Route) {
	route.Title(p.Title())

	// When connected without a live Client (e.g., static shell snapshot in app_test.go),
	// keep a clean ready state that includes "主页信息流" and zero overflow.
	if h.Client() == nil {
		ui.Column(c).Fill().Background(theme.ColorBgMain).Children(func() {
			if !r.InSystemStack(c) {
				mobile.TopBar(c, mobile.TopBarOptions{Title: p.Title()})
			}
			ui.Scroll(c).Grow(1).Basis(0).FillWidth().Children(func() {
				ui.Column(c).FillWidth().Padding(32, 20).Gap(10).AlignItems(ui.Center).Children(func() {
					theme.DrawIcon(c, "home", 32, theme.ColorTextMuted, 1.7)
					ui.Text(c, "主页信息流").FontSize(17).FontWeight(600).TextColor(theme.ColorTextMain)
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
	site := p.activeSite
	st := p.feedForLocked(site)
	if !st.loadedOnce && !st.loading {
		gen := p.gen
		p.mu.Unlock()
		p.fetchSubscriptions(h, site, gen, false)
		p.fetchPositionsAndInitialFeed(h, site, gen)
		p.mu.Lock()
		st = p.feedForLocked(site)
	}
	subs := append([]companionapi.FeedSubscriptionRow(nil), p.subs...)
	seenErr := p.seenErr
	p.mu.Unlock()

	ui.Column(c).Fill().Background(theme.ColorBgMain).Children(func() {
		if !r.InSystemStack(c) {
			mobile.TopBar(c, mobile.TopBarOptions{Title: p.Title()})
		}

		// Site filter strip: 全部 / B 站 / 抖音 / 微博 / X (fits 360dp with zero overflow).
		p.renderSiteFilterBar(c, h, site)

		// Horizontal subscription avatar strip.
		p.renderSubAvatarStrip(c, h, subs)

		if seenErr != "" {
			p.renderInlineNotice(c, seenErr, true)
		}

		// Main virtualized feed list.
		p.renderFeedList(c, h, site, st, false)
	})
}

func (p *Page) renderSubRoute(c *ui.Context, h pagekit.Host, r *ui.Router, route *ui.Route, subID string) {
	p.mu.Lock()
	if subID != "" && p.activeSubID != subID {
		gen := p.gen
		site := p.activeSite
		p.activeSubID = subID
		p.subFeed = feedSliceState{pendingScrollTo: -1}
		for _, row := range p.subs {
			if row.SubID == subID {
				p.activeSubRow = row
				break
			}
		}
		p.mu.Unlock()
		p.loadSubFeedPage(h, subID, site, gen, true)
		p.mu.Lock()
	}
	row := p.activeSubRow
	site := p.activeSite
	seenErr := p.seenErr
	p.mu.Unlock()

	title := row.Name
	if title == "" {
		title = "订阅动态"
	}
	route.Title(title)

	ui.Column(c).Fill().Background(theme.ColorBgMain).Children(func() {
		if !r.InSystemStack(c) {
			mobile.TopBar(c, mobile.TopBarOptions{
				Title: title,
				Leading: func() {
					backBtn := ui.Button(c, "").
						Label("返回主页").
						Size(44, 44).Radius(22).
						Background(ui.Color{}).
						Justify(ui.Center).AlignItems(ui.Center)
					backBtn.Children(func() {
						theme.DrawIcon(c, "arrow-left", 20, theme.ColorTextMain, 1.8)
					})
					if backBtn.Clicked() {
						h.Back()
					}
				},
			})
		}

		if seenErr != "" {
			p.renderInlineNotice(c, seenErr, true)
		}

		p.renderFeedList(c, h, site, &p.subFeed, true)
	})
}

func (p *Page) renderSiteFilterBar(c *ui.Context, h pagekit.Host, activeSite string) {
	ui.Row(c).
		FillWidth().
		Padding(8, 12).
		Gap(6).
		Background(theme.ColorBgMain).
		BorderWidth(0, 0, 1, 0).
		Border(1, theme.ColorBorder).
		Children(func() {
			for _, opt := range siteFilters {
				selected := activeSite == opt.Key
				bg := theme.ColorBgSecondary
				txtCol := theme.ColorTextMuted
				weight := 500
				if selected {
					bg = theme.ColorAccentBg
					txtCol = theme.ColorAccent
					weight = 600
				}
				btn := ui.Button(c.Key("site-"+opt.Key), "").
					Label("筛选 "+opt.Label).
					Grow(1).Basis(0).
					MinHeight(36).
					Padding(6, 4).
					Radius(999).
					Background(bg).
					Justify(ui.Center).
					AlignItems(ui.Center)
				btn.Children(func() {
					ui.Text(c, opt.Label).FontSize(13).FontWeight(weight).TextColor(txtCol).SingleLine()
				})
				if btn.Clicked() {
					p.selectSite(h, opt.Key)
				}
			}
		})
}

func (p *Page) renderSubAvatarStrip(c *ui.Context, h pagekit.Host, subs []companionapi.FeedSubscriptionRow) {
	if len(subs) == 0 {
		return
	}
	ui.Box(c).
		FillWidth().
		Background(theme.ColorBgMain).
		BorderWidth(0, 0, 1, 0).
		Border(1, theme.ColorBorder).
		Children(func() {
			ui.ScrollHorizontal(c).FillWidth().Scrollbars(false).Children(func() {
				ui.Row(c).Padding(10, 12).Gap(10).AlignItems(ui.Start).Children(func() {
					// "全部动态" chip at the front of the avatar strip (docs/ui.md §3.1).
					allBtn := ui.Button(c.Key("sub-all"), "").
						Label("全部动态").
						Width(58).
						Background(ui.Color{}).
						Border(0, ui.Color{}).
						Padding(0, 0).
						Children(func() {
							ui.Column(c).Width(58).Gap(4).AlignItems(ui.Center).Children(func() {
								ui.Box(c).
									Size(48, 48).
									Radius(24).
									Background(theme.ColorAccentBg).
									Border(2, theme.ColorAccent).
									Justify(ui.Center).
									AlignItems(ui.Center).
									Children(func() {
										theme.DrawIcon(c, "home", 20, theme.ColorAccent, 1.8)
									})
								ui.Text(c, "全部动态").FontSize(11).FontWeight(600).TextColor(theme.ColorAccent).SingleLine()
							})
						})
					if allBtn.Clicked() {
						p.mu.Lock()
						site := p.activeSite
						st := p.feedForLocked(site)
						st.listState.ScrollTo(0, ui.Start)
						p.mu.Unlock()
					}

					for _, row := range subs {
						r := row
						accLabel := r.Name
						if r.Live {
							accLabel += "，直播中"
						}
						if r.Unread {
							accLabel += "，有未读"
						}
						itemBtn := ui.Button(c.Key("sub-"+r.SubID), "").
							Label(accLabel).
							Width(58).
							Background(ui.Color{}).
							Border(0, ui.Color{}).
							Padding(0, 0).
							Children(func() {
								ui.Column(c).Width(58).Gap(4).AlignItems(ui.Center).Children(func() {
									p.renderSubStripAvatar(c, h, r)
									ui.Text(c, r.Name).FontSize(11).FontWeight(500).TextColor(theme.ColorTextMain).SingleLine()
								})
							})
						if itemBtn.Clicked() {
							p.openSubscription(h, r.SubID, r.Name, r.AvatarPath, r.LatestPubTs)
						}
					}
				})
			})
		})
}

func (p *Page) renderSubStripAvatar(c *ui.Context, h pagekit.Host, row companionapi.FeedSubscriptionRow) {
	ui.Box(c).Size(48, 48).Children(func() {
		ringCol := theme.ColorBorder
		ringW := float32(1)
		if row.Live {
			ringCol = theme.ColorLive
			ringW = 2.5
		}
		ui.Box(c).
			Size(48, 48).
			Radius(24).
			Border(ringW, ringCol).
			Padding(2, 2).
			Justify(ui.Center).
			AlignItems(ui.Center).
			Children(func() {
				p.renderCircleAvatar(c, h, row.AvatarPath, row.Name, row.SubID, 40)
			})
		if row.Unread {
			ui.Box(c).
				Absolute().Right(1).Bottom(1).ZIndex(5).
				Size(11, 11).
				Radius(5.5).
				Background(theme.ColorAccent).
				Border(2, theme.ColorBgMain)
		}
	})
}

func (p *Page) renderFeedList(c *ui.Context, h pagekit.Host, site string, st *feedSliceState, isSub bool) {
	p.mu.Lock()
	items := st.items
	loading := st.loading
	loadingMore := st.loadingMore
	loadedOnce := st.loadedOnce
	hasMore := st.hasMore
	hasNewer := st.hasNewer
	errMsg := st.errMsg
	gen := p.gen
	subID := p.activeSubID
	if st.pendingScrollTo >= 0 && st.pendingScrollTo < len(items) {
		target := st.pendingScrollTo
		st.pendingScrollTo = -1
		st.listState.ScrollTo(target, ui.Start)
	}
	p.mu.Unlock()

	if loading && len(items) == 0 {
		p.renderFeedSkeleton(c)
		return
	}

	if errMsg != "" && len(items) == 0 {
		ui.Column(c).Grow(1).Basis(0).FillWidth().Justify(ui.Center).AlignItems(ui.Center).Padding(32, 24).Gap(12).Children(func() {
			theme.DrawIcon(c, "alert-circle", 32, theme.ColorError, 1.7)
			ui.Text(c, errMsg).FontSize(14).LineHeight(theme.LineHeightCompact).TextColor(theme.ColorTextMain).TextAlign(ui.Center)
			if theme.PrimaryPillButton(c, "重试", "重试加载主页").Clicked() {
				if isSub {
					p.loadSubFeedPage(h, subID, site, gen, true)
				} else {
					p.loadFeedPage(h, site, gen, true, true)
				}
			}
		})
		return
	}

	if loadedOnce && len(items) == 0 {
		emptyText := "还没有缓存的内容"
		hintText := "在电脑端 TRACE 缓存视频、图文或直播录像后，这里会自动同步显示。"
		if isSub {
			emptyText = "这个订阅还没有已缓存的内容"
			hintText = "可以在“订阅”里检查更新，或在电脑端缓存它的内容。"
		}
		ui.Column(c).Grow(1).Basis(0).FillWidth().Justify(ui.Center).AlignItems(ui.Center).Padding(36, 24).Gap(10).Children(func() {
			theme.DrawIcon(c, "home", 32, theme.ColorTextFaint, 1.6)
			ui.Text(c, emptyText).FontSize(16).FontWeight(600).TextColor(theme.ColorTextMain)
			ui.Text(c, hintText).FontSize(13).LineHeight(theme.LineHeightCompact).TextColor(theme.ColorTextMuted).TextAlign(ui.Center)
		})
		return
	}

	// Row 0 optional "加载更新内容" banner when jumped to older saved position,
	// then len(items) feed cards, then 1 footer row.
	topOffset := 0
	if hasNewer && !isSub {
		topOffset = 1
	}
	totalRows := topOffset + len(items) + 1

	st.listState.Key = func(row int) any {
		if topOffset == 1 && row == 0 {
			return "home-load-newer"
		}
		idx := row - topOffset
		if idx >= 0 && idx < len(items) {
			if items[idx].FeedKey != "" {
				return items[idx].FeedKey
			}
			return items[idx].ItemKey
		}
		return "home-footer"
	}
	st.listState.NearEnd = 5
	st.listState.EndReached = func(int) {
		if isSub {
			p.loadSubFeedPage(h, subID, site, gen, false)
		} else {
			p.loadFeedPage(h, site, gen, false, false)
		}
	}

	listEl := ui.List(c, &st.listState, totalRows, func(row int) {
		if topOffset == 1 && row == 0 {
			ui.Row(c).FillWidth().Padding(10, 14).Justify(ui.Center).Background(theme.ColorAccentBg).Children(func() {
				btn := ui.Button(c, "").
					Label("回到最新内容").
					MinHeight(36).Padding(6, 14).Radius(999).Background(theme.ColorBgMain).Border(1, theme.ColorAccent)
				btn.Children(func() {
					ui.Text(c, "回到最新内容").FontSize(13).FontWeight(600).TextColor(theme.ColorAccent)
				})
				if btn.Clicked() {
					p.loadFeedPage(h, site, gen, true, true)
				}
			})
			return
		}
		idx := row - topOffset
		if idx >= 0 && idx < len(items) {
			p.renderPostCard(c, h, items[idx], isSub)
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
					if isSub {
						p.loadSubFeedPage(h, subID, site, gen, false)
					} else {
						p.loadFeedPage(h, site, gen, false, false)
					}
				}
			case !hasMore:
				ui.Text(c, "已经到最早的缓存了").FontSize(13).TextColor(theme.ColorTextFaint)
			default:
				ui.Box(c).Height(12)
			}
		})
	}).Grow(1).Basis(0).FillWidth()

	if listEl.PullToRefresh(&st.refreshState) {
		st.refreshState.Refreshing = true
		if isSub {
			p.loadSubFeedPage(h, subID, site, gen, true)
		} else {
			p.fetchSubscriptions(h, site, gen, false)
			p.loadFeedPage(h, site, gen, true, true)
		}
	}

	// Track per-device reading position on main timeline only (FR-MOB-011).
	if !isSub {
		p.mu.Lock()
		p.trackVisiblePosition(h, site, st)
		p.mu.Unlock()
	}
}

func (p *Page) renderFeedSkeleton(c *ui.Context) {
	ui.Column(c).Grow(1).Basis(0).FillWidth().Padding(14, 14).Gap(16).Children(func() {
		for i := 0; i < 4; i++ {
			ui.Column(c.Key(i)).FillWidth().Gap(8).Children(func() {
				ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
					ui.Box(c).Size(36, 36).Radius(18).Background(theme.ColorBgSecondary)
					ui.Column(c).Grow(1).Basis(0).Gap(5).Children(func() {
						ui.Box(c).Width(110).Height(13).Radius(6).Background(theme.ColorBgSecondary)
						ui.Box(c).Width(150).Height(11).Radius(5).Background(theme.ColorBgSecondary)
					})
				})
				ui.Box(c).FillWidth().Height(14).Radius(6).Background(theme.ColorBgSecondary)
				ui.Box(c).FillWidth().Height(100).Radius(12).Background(theme.ColorBgSecondary)
			})
		}
	})
}

func (p *Page) renderInlineNotice(c *ui.Context, msg string, isErr bool) {
	bg := theme.ColorWarnBg
	txtCol := theme.ColorWarnText
	if isErr {
		bg = theme.ColorErrorBg
		txtCol = theme.ColorError
	}
	ui.Row(c).FillWidth().Background(bg).Padding(8, 14).Gap(8).AlignItems(ui.Center).Children(func() {
		theme.DrawIcon(c, "alert-circle", 16, txtCol, 1.8)
		ui.Text(c, msg).FontSize(12).TextColor(txtCol).Grow(1).Basis(0)
	})
}

func fetchFeed(
	ctx context.Context,
	cli *companionclient.Client,
	site, subID string,
	cursor *companionapi.FeedCursor,
	direction string,
	limit int,
) (companionapi.FeedPageResponse, error) {
	q := url.Values{}
	if site != "" && site != "all" {
		q.Set("site", site)
	}
	if subID != "" {
		q.Set("subId", subID)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if direction != "" {
		q.Set("direction", direction)
	}
	if cursor != nil {
		if cursor.Key != "" {
			q.Set("cursorKey", cursor.Key)
		}
		if cursor.PubTs > 0 {
			q.Set("cursorPubTs", strconv.FormatInt(cursor.PubTs, 10))
		}
	}
	path := companionapi.PathFeed
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var resp companionapi.FeedPageResponse
	err := cli.DoJSON(ctx, http.MethodGet, path, nil, &resp)
	return resp, err
}

func findFeedIndex(items []companionapi.FeedItem, targetKey string, targetPubTs int64) int {
	for i, it := range items {
		if targetKey != "" && it.FeedKey == targetKey {
			return i
		}
	}
	// If the saved post was deleted, stop at the nearest item not newer than it (docs/ui.md §3.1).
	if targetPubTs > 0 {
		for i, it := range items {
			if it.PubTs <= targetPubTs {
				return i
			}
		}
	}
	return -1
}

func appendUniqueFeedItems(base, extra []companionapi.FeedItem) []companionapi.FeedItem {
	seen := make(map[string]bool, len(base))
	for _, it := range base {
		k := it.FeedKey
		if k == "" {
			k = it.ItemKey
		}
		seen[k] = true
	}
	out := base
	for _, it := range extra {
		k := it.FeedKey
		if k == "" {
			k = it.ItemKey
		}
		if k != "" && seen[k] {
			continue
		}
		seen[k] = true
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
