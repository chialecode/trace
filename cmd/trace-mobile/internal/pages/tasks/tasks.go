// Package tasks implements the 任务 tab for TRACE mobile
// (FR-MOB-016, FR-MOB-020, FR-MOB-021, FR-MOB-025, FR-CACHE-003, DEC-081, docs/ui.md §3.12).
package tasks

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/cmd/trace-mobile/internal/mediaview"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/pagekit"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/pastecache"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/theme"
	"github.com/chialecode/trace/internal/companionapi"
	"github.com/chialecode/trace/internal/companionclient"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/mobile"
)

const (
	segActive = iota
	segLive
	segPending
	segHistory
)

const defaultRefreshThrottle = time.Second

// Page implements pagekit.TabPage for 任务.
type Page struct {
	// RefreshThrottle bounds WebSocket-triggered refreshes (<= 1/sec, FR-MOB-016).
	// Zero uses defaultRefreshThrottle (1s).
	RefreshThrottle time.Duration

	activeSeg     int
	initTriggered bool
	sessionGen    uint64

	// Data snapshots (in-memory only, cleared on OnDisconnected).
	downloads        []companionapi.DownloadTaskDTO
	downloadsLoading bool
	downloadsLoaded  bool
	downloadsErr     string

	queues        []companionapi.CacheQueueDTO
	queuesLoading bool
	queuesLoaded  bool
	queuesErr     string

	recordings        []companionapi.LiveRecordingDTO
	recordingsLoading bool
	recordingsLoaded  bool
	recordingsErr     string

	conflicts        []companionapi.ConflictDTO
	conflictsLoading bool
	conflictsLoaded  bool
	conflictsErr     string

	incidents        []companionapi.LiveIncidentSessionDTO
	incidentsLoading bool
	incidentsLoaded  bool
	incidentsErr     string

	transfers        []companionapi.TransferJobDTO
	transfersLoading bool
	transfersLoaded  bool
	transfersErr     string

	// UI state.
	activeRefresh  ui.RefreshState
	liveRefresh    ui.RefreshState
	pendingRefresh ui.RefreshState
	historyRefresh ui.RefreshState

	expandedQueues    map[string]bool
	expandedLiveIncs  map[string]bool
	removeConfirmID   string
	conflictConfirmID string
	actionErr         string
	actionInfo        string
	busyActionKey     string
	swipeStates       map[string]*mobile.SwipeState
	downloadSheetOpen bool
	downloadSheetTask *companionapi.DownloadTaskDTO
	queueSheetOpen    bool
	queueSheetItem    *companionapi.CacheQueueDTO
	pasteSheet        pastecache.Sheet

	// Optional test hook for live recording real-time preview.
	openLivePreviewFn func(h pagekit.Host, rec companionapi.LiveRecordingDTO)

	// Download detail route state (/download/{id}).
	detailTaskID  string
	detailResp    *companionapi.DownloadTaskDetailResponse
	detailLoading bool
	detailErr     string

	// WebSocket event throttling (<= 1/sec).
	evMu            sync.Mutex
	lastEventLoad   time.Time
	wantDownloads   bool
	wantQueues      bool
	wantLive        bool
	wantConflicts   bool
	wantTransfers   bool
	throttleTimer   *time.Timer
	lastBadgePosted int
}

// New returns the 任务 tab page.
func New() *Page {
	return &Page{
		expandedQueues:   make(map[string]bool),
		expandedLiveIncs: make(map[string]bool),
		swipeStates:      make(map[string]*mobile.SwipeState),
		lastBadgePosted:  -1,
	}
}

func (p *Page) ID() pagekit.TabID { return pagekit.TabTasks }
func (p *Page) Title() string     { return "任务" }

func (p *Page) throttleInterval() time.Duration {
	if p.RefreshThrottle > 0 {
		return p.RefreshThrottle
	}
	return defaultRefreshThrottle
}

// OnConnected resets transient errors and loads all task groups.
func (p *Page) OnConnected(h pagekit.Host) {
	p.sessionGen++
	p.initTriggered = true
	p.actionErr = ""
	p.actionInfo = ""
	p.removeConfirmID = ""
	p.conflictConfirmID = ""
	p.downloadSheetOpen = false
	p.queueSheetOpen = false
	p.lastBadgePosted = -1
	p.loadAll(h)
	if p.isDetailActive(h) {
		p.loadDownloadDetail(h, p.detailTaskID)
	}
}

// OnDisconnected immediately clears all in-memory state, images, and pending timers (FR-MOB-006 / NFR-MOB-DATA-001).
func (p *Page) OnDisconnected() {
	p.sessionGen++
	p.initTriggered = false
	p.cancelThrottleTimer()
	mediaview.CloseAllActive()
	p.pasteSheet.Reset()

	p.downloads = nil
	p.downloadsLoaded = false
	p.downloadsLoading = false
	p.downloadsErr = ""

	p.queues = nil
	p.queuesLoaded = false
	p.queuesLoading = false
	p.queuesErr = ""

	p.recordings = nil
	p.recordingsLoaded = false
	p.recordingsLoading = false
	p.recordingsErr = ""

	p.conflicts = nil
	p.conflictsLoaded = false
	p.conflictsLoading = false
	p.conflictsErr = ""

	p.incidents = nil
	p.incidentsLoaded = false
	p.incidentsLoading = false
	p.incidentsErr = ""

	p.transfers = nil
	p.transfersLoaded = false
	p.transfersLoading = false
	p.transfersErr = ""

	p.activeRefresh.Refreshing = false
	p.liveRefresh.Refreshing = false
	p.pendingRefresh.Refreshing = false
	p.historyRefresh.Refreshing = false

	clear(p.expandedQueues)
	clear(p.expandedLiveIncs)
	clear(p.swipeStates)
	p.removeConfirmID = ""
	p.conflictConfirmID = ""
	p.actionErr = ""
	p.actionInfo = ""
	p.busyActionKey = ""
	p.downloadSheetOpen = false
	p.downloadSheetTask = nil
	p.queueSheetOpen = false
	p.queueSheetItem = nil

	p.detailTaskID = ""
	p.detailResp = nil
	p.detailLoading = false
	p.detailErr = ""
	p.lastBadgePosted = -1
}

// OnEvent handles live WebSocket events throttled to <= 1 refresh/sec (FR-MOB-016).
func (p *Page) OnEvent(h pagekit.Host, ev companionclient.EventFrame) {
	if h == nil || h.ConnState() != pagekit.ConnConnected {
		return
	}
	switch ev.Type {
	case companionapi.EventTaskChanged, companionapi.EventCacheChanged:
		p.scheduleThrottledReload(h, true, false, false, false, false)
	case companionapi.EventCacheQueueUpdated:
		p.scheduleThrottledReload(h, true, true, false, false, false)
	case companionapi.EventLiveChanged:
		p.scheduleThrottledReload(h, false, false, true, false, false)
	case companionapi.EventSubsCheck:
		// Auto-cache during subscription checks may enqueue downloads or queues.
		p.scheduleThrottledReload(h, true, true, false, false, false)
	case companionapi.EventConflictsChanged:
		p.scheduleThrottledReload(h, false, false, false, true, false)
	case companionapi.EventTransfersChanged:
		p.scheduleThrottledReload(h, false, false, false, false, true)
	case companionapi.EventResync:
		p.scheduleThrottledReload(h, true, true, true, true, true)
	}
}

func (p *Page) scheduleThrottledReload(h pagekit.Host, dl, q, live, conf, tr bool) {
	p.evMu.Lock()
	p.wantDownloads = p.wantDownloads || dl
	p.wantQueues = p.wantQueues || q
	p.wantLive = p.wantLive || live
	p.wantConflicts = p.wantConflicts || conf
	p.wantTransfers = p.wantTransfers || tr

	interval := p.throttleInterval()
	now := time.Now()
	elapsed := now.Sub(p.lastEventLoad)
	if elapsed >= interval {
		d, qu, lv, cf, tf := p.wantDownloads, p.wantQueues, p.wantLive, p.wantConflicts, p.wantTransfers
		p.wantDownloads, p.wantQueues, p.wantLive, p.wantConflicts, p.wantTransfers = false, false, false, false, false
		p.lastEventLoad = now
		p.evMu.Unlock()
		p.flushEventReload(h, d, qu, lv, cf, tf)
		return
	}
	if p.throttleTimer == nil {
		wait := interval - elapsed
		gen := p.sessionGen
		p.throttleTimer = time.AfterFunc(wait, func() {
			p.evMu.Lock()
			p.throttleTimer = nil
			d, qu, lv, cf, tf := p.wantDownloads, p.wantQueues, p.wantLive, p.wantConflicts, p.wantTransfers
			p.wantDownloads, p.wantQueues, p.wantLive, p.wantConflicts, p.wantTransfers = false, false, false, false, false
			p.lastEventLoad = time.Now()
			p.evMu.Unlock()
			if !d && !qu && !lv && !cf && !tf {
				return
			}
			h.Update(func() {
				if p.sessionGen != gen || h.ConnState() != pagekit.ConnConnected {
					return
				}
				p.flushEventReload(h, d, qu, lv, cf, tf)
			})
		})
	}
	p.evMu.Unlock()
}

func (p *Page) isDetailActive(h pagekit.Host) bool {
	if p.detailTaskID == "" || h == nil || h.Router() == nil {
		return false
	}
	return strings.HasPrefix(h.Router().Path(), "/download/")
}

func (p *Page) flushEventReload(h pagekit.Host, dl, q, live, conf, tr bool) {
	if dl {
		p.loadDownloads(h)
		if p.isDetailActive(h) {
			p.loadDownloadDetail(h, p.detailTaskID)
		}
	}
	if q {
		p.loadQueues(h)
	}
	if live {
		p.loadLiveRecordings(h)
		p.loadLiveIncidents(h)
	}
	if conf {
		p.loadConflicts(h)
	}
	if tr {
		p.loadTransfers(h)
	}
}

func (p *Page) cancelThrottleTimer() {
	p.evMu.Lock()
	if p.throttleTimer != nil {
		p.throttleTimer.Stop()
		p.throttleTimer = nil
	}
	p.wantDownloads = false
	p.wantQueues = false
	p.wantLive = false
	p.wantConflicts = false
	p.wantTransfers = false
	p.evMu.Unlock()
}

// View renders the 任务 tab with SystemStack routing.
func (p *Page) View(c *ui.Context, h pagekit.Host) {
	if !p.initTriggered && h.ConnState() == pagekit.ConnConnected && h.Client() != nil {
		p.initTriggered = true
		p.loadAll(h)
	}

	r := h.Router()
	r.SystemStack = true
	detailRendered := false
	ui.Column(c).Fill().Background(theme.ColorBgMain).Children(func() {
		r.View(c, func(route *ui.Route) {
			switch {
			case route.Match("/download/{id}"):
				detailRendered = true
				rawID := route.Param("id")
				taskID, err := url.PathUnescape(rawID)
				if err != nil || taskID == "" {
					taskID = rawID
				}
				if p.detailTaskID != taskID {
					p.openDownloadDetail(h, taskID)
				}
				title := "下载任务详情"
				if p.detailResp != nil && p.detailResp.Task.Title != "" {
					title = p.detailResp.Task.Title
				}
				route.Title(title)
				p.renderDownloadDetailView(c, h, r, title)
			default:
				route.Title(p.Title())
				p.renderMainView(c, h, r)
			}
		}).Grow(1).Basis(0).FillWidth()
	})
	if !detailRendered && !strings.HasPrefix(r.Path(), "/download/") && p.detailTaskID != "" {
		p.clearDetailState()
	}
}

func (p *Page) clearDetailState() {
	p.detailTaskID = ""
	p.detailResp = nil
	p.detailLoading = false
	p.detailErr = ""
}

func (p *Page) renderMainView(c *ui.Context, h pagekit.Host, r *ui.Router) {
	ui.Column(c).Fill().Background(theme.ColorBgMain).ClipX().Children(func() {
		if !r.InSystemStack(c) {
			mobile.TopBar(c, mobile.TopBarOptions{Title: p.Title()})
		}

		// Top 4-way segment matching latest desktop FR-CACHE-003 & DEC-081:
		// 进行中 / 直播 / 待处理 / 记录
		ui.Column(c).FillWidth().Padding(8, 14, 10, 14).Gap(8).BorderWidth(0, 0, 1, 0).BorderColor(theme.ColorBorder).Shrink(0).ClipX().Children(func() {
			prev := p.activeSeg
			ui.Segmented(c, &p.activeSeg, "进行中", "直播", "待处理", "记录").FillWidth().Label("任务分组切换")
			if p.activeSeg != prev {
				p.actionErr = ""
				p.actionInfo = ""
				p.removeConfirmID = ""
				p.conflictConfirmID = ""
				switch p.activeSeg {
				case segActive:
					if !p.downloadsLoaded && !p.downloadsLoading {
						p.loadDownloads(h)
					}
					if !p.queuesLoaded && !p.queuesLoading {
						p.loadQueues(h)
					}
				case segLive:
					if !p.recordingsLoaded && !p.recordingsLoading {
						p.loadLiveRecordings(h)
					}
				case segPending:
					if !p.conflictsLoaded && !p.conflictsLoading {
						p.loadConflicts(h)
					}
					if !p.downloadsLoaded && !p.downloadsLoading {
						p.loadDownloads(h)
					}
					if !p.incidentsLoaded && !p.incidentsLoading {
						p.loadLiveIncidents(h)
					}
				case segHistory:
					if !p.transfersLoaded && !p.transfersLoading {
						p.loadTransfers(h)
					}
				}
			}

			// Compact in-app "粘贴链接缓存" entry (FR-MOB-030, always visible including under Android SystemStack).
			pasteTrigger := ui.Button(c, "").
				Label("粘贴链接让电脑缓存").
				FillWidth().
				MinHeight(40).
				Padding(8, 12).
				Radius(10).
				Background(theme.ColorAccentBg).
				Border(1, ui.RGBA(26, 115, 232, 0.25)).
				Justify(ui.SpaceBetween).
				AlignItems(ui.Center)
			pasteTrigger.Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(6).Grow(1).Shrink(1).ClipX().Children(func() {
					ui.Text(c, "＋ 粘贴链接缓存").FontSize(13).FontWeight(600).TextColor(theme.ColorAccent).SingleLine()
					ui.Text(c, "发送链接让电脑解析并加入队列").FontSize(11).TextColor(theme.ColorTextMuted).SingleLine().Shrink(1)
				})
				mediaview.DrawIcon(c, "chevron-right", 14, theme.ColorAccent, 1.8)
			})
			if pasteTrigger.Clicked() {
				p.pasteSheet.SetOnQueued(func() { p.loadDownloads(h) })
				p.pasteSheet.Open("")
			}

			p.renderFeedbackBanners(c)
		})

		switch p.activeSeg {
		case segActive:
			p.renderActiveSegment(c, h)
		case segLive:
			p.renderLiveSegment(c, h)
		case segPending:
			p.renderPendingSegment(c, h)
		case segHistory:
			p.renderHistorySegment(c, h)
		}

		p.renderDownloadActionSheet(c, h)
		p.renderQueueActionSheet(c, h)
		p.pasteSheet.View(c, h)
	})
}

func (p *Page) renderFeedbackBanners(c *ui.Context) {
	if p.actionErr != "" {
		ui.Row(c).
			Label("任务错误提示").
			FillWidth().
			Padding(10, 12).
			Radius(10).
			Background(theme.ColorErrorBg).
			Border(1, ui.RGBA(196, 24, 46, 0.25)).
			AlignItems(ui.Center).
			Justify(ui.SpaceBetween).
			Gap(8).
			ClipX().
			Children(func() {
				theme.DrawIcon(c, "alert-circle", 16, theme.ColorError, 1.8)
				ui.Text(c, p.actionErr).
					FontSize(13).LineHeight(theme.LineHeightCompact).TextColor(theme.ColorError).MaxLines(3).Grow(1).Shrink(1)
				btn := ui.ButtonBase(c).Label("关闭错误提示").Padding(4, 8).MinHeight(32).Shrink(0).Center()
				btn.Children(func() {
					ui.Text(c, "关闭").FontSize(12).TextColor(theme.ColorError).SingleLine()
				})
				if btn.Clicked() {
					p.actionErr = ""
				}
			})
	}
	if p.actionInfo != "" {
		ui.Row(c).
			Label("任务状态提示").
			FillWidth().
			Padding(8, 12).
			Radius(10).
			Background(theme.ColorBgSecondary).
			Border(1, theme.ColorBorder).
			AlignItems(ui.Center).
			Justify(ui.SpaceBetween).
			Gap(8).
			ClipX().
			Children(func() {
				ui.Text(c, p.actionInfo).
					FontSize(12).TextColor(theme.ColorTextMuted).MaxLines(2).Grow(1).Shrink(1)
				btn := ui.ButtonBase(c).Label("关闭提示").Padding(4, 8).MinHeight(32).Shrink(0).Center()
				btn.Children(func() {
					ui.Text(c, "关闭").FontSize(12).TextColor(theme.ColorTextMuted).SingleLine()
				})
				if btn.Clicked() {
					p.actionInfo = ""
				}
			})
	}
}

// ActiveDownloadBadgeCount counts ONLY queued and running download tasks (FR-MOB-010, FR-CACHE-003, DEC-072).
// Live recordings, cache queues, conflicts, failed tasks, and incidents are explicitly excluded.
func ActiveDownloadBadgeCount(tasks []companionapi.DownloadTaskDTO) int {
	n := 0
	for _, t := range tasks {
		if t.Status == "queued" || t.Status == "running" {
			n++
		}
	}
	return n
}

func (p *Page) syncTasksBadge(h pagekit.Host) {
	if h == nil {
		return
	}
	count := ActiveDownloadBadgeCount(p.downloads)
	if count != p.lastBadgePosted {
		p.lastBadgePosted = count
		h.SetTasksBadge(count)
	}
}

// Data loaders (read-only companion API calls; zero direct site requests).

func (p *Page) loadAll(h pagekit.Host) {
	p.loadDownloads(h)
	p.loadQueues(h)
	p.loadLiveRecordings(h)
	p.loadConflicts(h)
	p.loadLiveIncidents(h)
	p.loadTransfers(h)
}

func (p *Page) loadDownloads(h pagekit.Host) {
	cli := h.Client()
	if cli == nil {
		return
	}
	gen := p.sessionGen
	p.downloadsLoading = true
	p.downloadsErr = ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var resp companionapi.DownloadTasksResponse
		err := cli.DoJSON(ctx, "GET", companionapi.PathTasksDownloads, nil, &resp)
		h.Update(func() {
			if p.sessionGen != gen {
				return
			}
			p.downloadsLoading = false
			p.activeRefresh.Refreshing = false
			p.pendingRefresh.Refreshing = false
			if err != nil {
				p.downloadsErr = formatActionError(err, "")
				return
			}
			p.downloads = resp.Items
			p.downloadsLoaded = true
			p.downloadsErr = ""
			p.syncTasksBadge(h)
		})
	}()
}

func (p *Page) openDownloadDetail(h pagekit.Host, taskID string) {
	p.detailTaskID = taskID
	p.detailResp = nil
	p.detailErr = ""
	p.loadDownloadDetail(h, taskID)
}

func (p *Page) loadDownloadDetail(h pagekit.Host, taskID string) {
	cli := h.Client()
	if cli == nil || taskID == "" {
		return
	}
	gen := p.sessionGen
	p.detailLoading = true
	p.detailErr = ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var resp companionapi.DownloadTaskDetailResponse
		err := cli.DoJSON(ctx, "GET", companionapi.DownloadTaskPath(taskID), nil, &resp)
		h.Update(func() {
			if p.sessionGen != gen || p.detailTaskID != taskID {
				return
			}
			p.detailLoading = false
			if err != nil {
				p.detailErr = formatActionError(err, "")
				return
			}
			p.detailResp = &resp
			p.detailErr = ""
		})
	}()
}

func (p *Page) loadQueues(h pagekit.Host) {
	cli := h.Client()
	if cli == nil {
		return
	}
	gen := p.sessionGen
	p.queuesLoading = true
	p.queuesErr = ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var resp companionapi.CacheQueuesResponse
		err := cli.DoJSON(ctx, "GET", companionapi.PathTasksQueues, nil, &resp)
		h.Update(func() {
			if p.sessionGen != gen {
				return
			}
			p.queuesLoading = false
			p.activeRefresh.Refreshing = false
			if err != nil {
				p.queuesErr = formatActionError(err, "")
				return
			}
			p.queues = resp.Items
			p.queuesLoaded = true
			p.queuesErr = ""
		})
	}()
}

func (p *Page) loadLiveRecordings(h pagekit.Host) {
	cli := h.Client()
	if cli == nil {
		return
	}
	gen := p.sessionGen
	p.recordingsLoading = true
	p.recordingsErr = ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var resp companionapi.LiveRecordingsResponse
		err := cli.DoJSON(ctx, "GET", companionapi.PathLiveRecordings, nil, &resp)
		h.Update(func() {
			if p.sessionGen != gen {
				return
			}
			p.recordingsLoading = false
			p.liveRefresh.Refreshing = false
			if err != nil {
				p.recordingsErr = formatActionError(err, "")
				return
			}
			p.recordings = resp.Items
			p.recordingsLoaded = true
			p.recordingsErr = ""
		})
	}()
}

func (p *Page) loadConflicts(h pagekit.Host) {
	cli := h.Client()
	if cli == nil {
		return
	}
	gen := p.sessionGen
	p.conflictsLoading = true
	p.conflictsErr = ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var resp companionapi.ConflictsResponse
		err := cli.DoJSON(ctx, "GET", companionapi.PathConflicts, nil, &resp)
		h.Update(func() {
			if p.sessionGen != gen {
				return
			}
			p.conflictsLoading = false
			p.pendingRefresh.Refreshing = false
			if err != nil {
				p.conflictsErr = formatActionError(err, "")
				return
			}
			p.conflicts = resp.Items
			p.conflictsLoaded = true
			p.conflictsErr = ""
		})
	}()
}

func (p *Page) loadLiveIncidents(h pagekit.Host) {
	cli := h.Client()
	if cli == nil {
		return
	}
	gen := p.sessionGen
	p.incidentsLoading = true
	p.incidentsErr = ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var resp companionapi.LiveIncidentsResponse
		err := cli.DoJSON(ctx, "GET", companionapi.PathLiveIncidents, nil, &resp)
		h.Update(func() {
			if p.sessionGen != gen {
				return
			}
			p.incidentsLoading = false
			p.pendingRefresh.Refreshing = false
			if err != nil {
				p.incidentsErr = formatActionError(err, "")
				return
			}
			p.incidents = resp.Items
			p.incidentsLoaded = true
			p.incidentsErr = ""
		})
	}()
}

func (p *Page) loadTransfers(h pagekit.Host) {
	cli := h.Client()
	if cli == nil {
		return
	}
	gen := p.sessionGen
	p.transfersLoading = true
	p.transfersErr = ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var resp companionapi.TransfersResponse
		err := cli.DoJSON(ctx, "GET", companionapi.PathTransfers, nil, &resp)
		h.Update(func() {
			if p.sessionGen != gen {
				return
			}
			p.transfersLoading = false
			p.historyRefresh.Refreshing = false
			if err != nil {
				p.transfersErr = formatActionError(err, "")
				return
			}
			p.transfers = resp.Items
			p.transfersLoaded = true
			p.transfersErr = ""
		})
	}()
}

// Control commands (FR-MOB-020, FR-MOB-021).

func (p *Page) runDownloadAction(h pagekit.Host, task companionapi.DownloadTaskDTO, action string) {
	cli := h.Client()
	if cli == nil || p.busyActionKey != "" {
		return
	}
	key := "dl:" + task.TaskID + ":" + action
	gen := p.sessionGen
	p.busyActionKey = key
	p.actionErr = ""
	p.actionInfo = ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		path := companionapi.DownloadTaskActionPath(task.TaskID, action)
		var err error
		if action == "remove" {
			var resp companionapi.ActionResponse
			err = cli.DoJSON(ctx, "POST", path, nil, &resp)
		} else {
			var updated companionapi.DownloadTaskDTO
			err = cli.DoJSON(ctx, "POST", path, nil, &updated)
		}
		h.Update(func() {
			if p.sessionGen != gen {
				return
			}
			p.busyActionKey = ""
			if err != nil {
				p.actionErr = formatActionError(err, task.Site)
				return
			}
			if action == "remove" && p.detailTaskID == task.TaskID {
				h.Back()
			}
			p.loadDownloads(h)
			if p.detailTaskID == task.TaskID && action != "remove" {
				p.loadDownloadDetail(h, task.TaskID)
			}
		})
	}()
}

func (p *Page) clearFinishedDownloads(h pagekit.Host) {
	cli := h.Client()
	if cli == nil || p.busyActionKey != "" {
		return
	}
	gen := p.sessionGen
	p.busyActionKey = "dl:clear-finished"
	p.actionErr = ""
	p.actionInfo = ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var resp companionapi.ActionResponse
		err := cli.DoJSON(ctx, "POST", companionapi.PathTasksDownloadsClearFinished, nil, &resp)
		h.Update(func() {
			if p.sessionGen != gen {
				return
			}
			p.busyActionKey = ""
			if err != nil {
				p.actionErr = formatActionError(err, "")
				return
			}
			if resp.Count > 0 {
				p.actionInfo = fmt.Sprintf("已清除 %d 个已结束的下载任务", resp.Count)
			}
			p.loadDownloads(h)
		})
	}()
}

func (p *Page) runQueueAction(h pagekit.Host, q companionapi.CacheQueueDTO, action string) {
	cli := h.Client()
	if cli == nil || p.busyActionKey != "" {
		return
	}
	key := "q:" + q.ID + ":" + action
	gen := p.sessionGen
	p.busyActionKey = key
	p.actionErr = ""
	p.actionInfo = ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		path := companionapi.CacheQueueActionPath(q.ID, action)
		var err error
		if action == "remove" {
			var resp companionapi.ActionResponse
			err = cli.DoJSON(ctx, "POST", path, nil, &resp)
		} else {
			var updated companionapi.CacheQueueDTO
			err = cli.DoJSON(ctx, "POST", path, nil, &updated)
		}
		h.Update(func() {
			if p.sessionGen != gen {
				return
			}
			p.busyActionKey = ""
			if err != nil {
				p.actionErr = formatActionError(err, q.Site)
				return
			}
			p.loadQueues(h)
		})
	}()
}

func (p *Page) stopLiveRecording(h pagekit.Host, rec companionapi.LiveRecordingDTO) {
	if !rec.Recording {
		return
	}
	cli := h.Client()
	if cli == nil || p.busyActionKey != "" {
		return
	}
	recKey := firstNonEmpty(rec.RecordingKey, rec.AccountKey)
	if recKey == "" {
		return
	}
	gen := p.sessionGen
	p.busyActionKey = "live:stop:" + recKey
	p.actionErr = ""
	p.actionInfo = ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		var updated companionapi.LiveRecordingDTO
		err := cli.DoJSON(ctx, "POST", companionapi.LiveRecordingStopPath(recKey), nil, &updated)
		h.Update(func() {
			if p.sessionGen != gen {
				return
			}
			p.busyActionKey = ""
			if err != nil {
				p.actionErr = formatActionError(err, rec.Site)
				return
			}
			p.actionInfo = "已停止录制：" + firstNonEmpty(rec.Uname, rec.UID)
			p.loadLiveRecordings(h)
		})
	}()
}

func (p *Page) resolveConflict(h pagekit.Host, cf companionapi.ConflictDTO, choice string) {
	cli := h.Client()
	if cli == nil || p.busyActionKey != "" || strings.TrimSpace(cf.ID) == "" {
		return
	}
	gen := p.sessionGen
	p.busyActionKey = "conflict:" + cf.ID
	p.conflictConfirmID = ""
	p.actionErr = ""
	p.actionInfo = ""
	go func() {
		// Match companionclient.Client.longClient (180s) because desktop ResolveCacheConflict
		// waits synchronously for re-download tasks to finish.
		ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
		defer cancel()
		resp, err := cli.ResolveConflict(ctx, cf.ID, choice)
		h.Update(func() {
			if p.sessionGen != gen {
				return
			}
			p.busyActionKey = ""
			if err != nil {
				p.actionErr = "冲突处理未完成：" + formatActionError(err, cf.Site)
				return
			}
			msg := strings.TrimSpace(resp.Message)
			if msg == "" {
				switch choice {
				case companionapi.ConflictKeepBoth:
					msg = "已保留本地旧版并缓存源站新版"
				case companionapi.ConflictKeepLocal:
					msg = "已保留本地版本，本次不再提示"
				case companionapi.ConflictUseRemote:
					msg = "已用源站新版更新本地缓存"
				default:
					msg = "已处理内容冲突"
				}
			}
			p.actionInfo = msg
			p.loadConflicts(h)
			p.loadDownloads(h)
		})
	}()
}

func (p *Page) openLivePreview(h pagekit.Host, rec companionapi.LiveRecordingDTO) {
	if !rec.Recording || h == nil {
		return
	}
	if p.openLivePreviewFn != nil {
		p.openLivePreviewFn(h, rec)
		return
	}
	recKey := firstNonEmpty(rec.RecordingKey, rec.AccountKey)
	if recKey == "" {
		return
	}
	title := firstNonEmpty(rec.Title, rec.Uname, "直播实时预览")
	qual := strings.TrimSpace(strings.Join(nonEmpty(rec.QualityName, rec.Codec), " · "))
	mediaview.OpenVideoPlayer(h, mediaview.VideoRequest{
		Title:            title,
		IsLivePreview:    true,
		LiveRecordingKey: recKey,
		Tracks: []mediaview.VideoTrack{{
			Label:          title,
			CodecHint:      rec.Codec,
			QualitySummary: qual,
		}},
	})
}

func (p *Page) swipeStateFor(key string) *mobile.SwipeState {
	if p.swipeStates == nil {
		p.swipeStates = make(map[string]*mobile.SwipeState)
	}
	if st, ok := p.swipeStates[key]; ok {
		return st
	}
	st := &mobile.SwipeState{}
	p.swipeStates[key] = st
	return st
}

// Formatting & status classification helpers.

// AllowedDownloadActions returns the exact set of control actions permitted
// for a download task in the given status (FR-MOB-020, matching desktop FR-CACHE-003).
func AllowedDownloadActions(status string) []string {
	switch {
	case status == "running" || status == "queued":
		return []string{"pause", "cancel"}
	case isPausedDownloadStatus(status):
		return []string{"resume", "cancel"}
	case status == "failed" || status == "partial":
		return []string{"retry", "remove"}
	case status == "completed" || status == "canceled":
		return []string{"remove"}
	default:
		return nil
	}
}

// AllowedQueueActions returns the exact set of control actions permitted
// for a cache queue in the given status (FR-MOB-020, matching desktop FR-CACHE-003).
func AllowedQueueActions(status string) []string {
	if isUnfinishedQueueStatus(status) {
		if status == "paused" {
			return []string{"resume", "cancel"}
		}
		return []string{"pause", "cancel"}
	}
	return []string{"remove"}
}

func isPausedDownloadStatus(status string) bool {
	return status == "paused" || strings.HasPrefix(status, "paused_")
}

func isRetryableDownloadTask(t companionapi.DownloadTaskDTO) bool {
	switch t.Status {
	case "failed", "partial":
		return true
	}
	if isPausedDownloadStatus(t.Status) && strings.TrimSpace(t.PauseReason) != "user" {
		return true
	}
	return false
}

func isUnfinishedQueueStatus(status string) bool {
	switch status {
	case "running", "queued", "waiting", "waiting_backoff", "paused":
		return true
	default:
		return false
	}
}

func downloadStatusLabel(status string) string {
	switch status {
	case "queued":
		return "排队中"
	case "running":
		return "下载中"
	case "paused":
		return "已暂停"
	case "paused_network":
		return "已暂停（网络）"
	case "paused_risk":
		return "已暂停（风控）"
	case "paused_auth":
		return "已暂停（未登录）"
	case "paused_rate":
		return "已暂停（限流）"
	case "completed":
		return "已完成"
	case "partial":
		return "部分完成"
	case "failed":
		return "失败"
	case "canceled":
		return "已取消"
	default:
		if strings.HasPrefix(status, "paused_") {
			return "已暂停"
		}
		return status
	}
}

func pauseReasonLabel(reason string) string {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "network":
		return "网络连接中断，已自动暂停"
	case "risk", "risk_control":
		return "触发站点风控，已暂停"
	case "rate", "rate_limited":
		return "触发站点限流，已暂停"
	case "auth", "auth_required":
		return "站点未登录或登录已失效"
	case "app_exit", "restart":
		return "电脑端上次退出时未完成，点继续从断点续传"
	case "user", "":
		return ""
	default:
		return reason
	}
}

func queueStatusLabel(q companionapi.CacheQueueDTO) string {
	switch q.Status {
	case "running":
		return "进行中"
	case "queued":
		return "排队中"
	case "waiting":
		if ts := formatTimeShort(q.NextRunAt); ts != "" {
			return "下次 " + ts
		}
		return "等待下一条"
	case "waiting_backoff":
		if ts := formatTimeShort(q.NextRunAt); ts != "" {
			return "退避至 " + ts
		}
		return "等待退避结束"
	case "paused":
		return "已暂停"
	case "completed":
		return "已完成"
	case "stopped":
		return firstNonEmpty(q.StopReason, "已停止")
	case "canceled":
		return "已取消"
	default:
		return firstNonEmpty(q.StatusText, q.Status)
	}
}

func fileRoleLabel(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "video":
		return "视频"
	case "audio":
		return "音频"
	case "image":
		return "图片"
	case "cover":
		return "封面"
	case "danmaku":
		return "弹幕"
	case "subtitle":
		return "字幕"
	case "merged":
		return "合并文件"
	case "music":
		return "配乐"
	case "motion":
		return "实况"
	default:
		if role == "" {
			return "文件"
		}
		return role
	}
}

func fileStatusLabel(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "queued", "pending":
		return "等待中"
	case "running", "downloading":
		return "下载中"
	case "completed", "done":
		return "已完成"
	case "failed":
		return "失败"
	case "paused":
		return "已暂停"
	case "canceled":
		return "已取消"
	default:
		return status
	}
}

func incidentKindLabel(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "disconnect", "reconnect", "stream_dropped":
		return "断流重连"
	case "segment_lost", "lost_segments", "hls_404":
		return "丢片"
	case "quality_changed", "stream_changed", "codec_changed":
		return "画质/编码切换"
	case "split", "segment_split":
		return "自动分段"
	case "convert_failed", "remux_failed":
		return "封装异常"
	case "disk_full", "io_error":
		return "磁盘写入异常"
	case "risk_control":
		return "站点风控"
	case "rate_limited":
		return "站点限流"
	case "auth_required":
		return "登录失效"
	default:
		return firstNonEmpty(kind, "录制异常")
	}
}

func transferStatusLabel(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "scanning":
		return "正在扫描"
	case "pending_confirm", "waiting_confirm", "preview":
		return "等待确认"
	case "importing", "running":
		return "进行中"
	case "exporting":
		return "正在导出"
	case "completed", "done":
		return "已完成"
	case "failed":
		return "失败"
	case "canceled":
		return "已取消"
	default:
		return status
	}
}

func siteShortLabel(site string) string {
	switch strings.ToLower(strings.TrimSpace(site)) {
	case "bilibili":
		return "B 站"
	case "douyin":
		return "抖音"
	case "weibo":
		return "微博"
	case "x":
		return "X"
	default:
		return site
	}
}

func siteFullLabel(site string) string {
	switch strings.ToLower(strings.TrimSpace(site)) {
	case "bilibili":
		return "哔哩哔哩"
	case "douyin":
		return "抖音"
	case "weibo":
		return "微博"
	case "x":
		return "X"
	default:
		return site
	}
}

func formatAuthRequiredMessage(site, serverMsg string) string {
	if s := strings.TrimSpace(serverMsg); s != "" && strings.Contains(s, "请先在电脑上登录") {
		return s
	}
	if full := siteFullLabel(site); full != "" {
		return "请先在电脑上登录" + full
	}
	return "请先在电脑上登录对应站点"
}

func formatActionError(err error, fallbackSite string) string {
	if err == nil {
		return ""
	}
	var ce *companionclient.Error
	if errors.As(err, &ce) && ce != nil {
		code := strings.ToLower(strings.TrimSpace(ce.Code))
		switch code {
		case companionapi.CodeAuthRequired:
			return formatAuthRequiredMessage(fallbackSite, ce.Message)
		case companionapi.CodeRiskControl:
			return firstNonEmpty(ce.Message, "触发站点风控，请稍后再试")
		case companionapi.CodeRateLimited:
			return firstNonEmpty(ce.Message, "触发站点限流，请稍后再试")
		case companionapi.CodeUnsupported:
			return firstNonEmpty(ce.Message, "该操作暂不支持")
		case companionapi.CodeConflict:
			return firstNonEmpty(ce.Message, "当前状态冲突，请刷新后重试")
		case companionapi.CodeUnavailable, companionapi.CodeNotFound:
			return firstNonEmpty(ce.Message, "目标任务不存在或不可用")
		}
		if ce.Kind == companionclient.KindRateLimited {
			return firstNonEmpty(ce.Message, "触发站点限流，请稍后再试")
		}
		if ce.Message != "" {
			return ce.Message
		}
		return ce.UserMessage()
	}
	return err.Error()
}

func formatBytes(n int64) string {
	if n < 0 {
		n = 0
	}
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case n >= gb:
		return fmt.Sprintf("%.2f GB", float64(n)/float64(gb))
	case n >= mb:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(mb))
	case n >= kb:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(kb))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func formatSpeed(bps int64) string {
	if bps <= 0 {
		return ""
	}
	return formatBytes(bps) + "/s"
}

func formatDurationFloat(sec float64) string {
	total := int(sec)
	if total < 0 {
		total = 0
	}
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

func formatTimeShort(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return raw
	}
	lt := t.Local()
	now := time.Now()
	if lt.Year() == now.Year() && lt.YearDay() == now.YearDay() {
		return "今天 " + lt.Format("15:04")
	}
	return lt.Format("01-02 15:04")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
