package ui

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

func (a *App) effectiveTasksSection() string {
	switch a.TasksSection {
	case "queue", "live", "incidents", "checks", "transfers":
		return a.TasksSection
	default:
		return "downloads"
	}
}

// effectiveSelectedQueueID is the queue whose card is open on the 缓存队列
// page, "" when none is.
func (a *App) effectiveSelectedQueueID() string {
	if a.SelectedCacheQueueID != "" {
		for _, q := range a.CacheQueue {
			if q.ID == a.SelectedCacheQueueID {
				return q.ID
			}
		}
	}
	return ""
}

func (a *App) selectTasksDownloads() {
	a.TasksSection = "downloads"
	a.closeLivePreview()
}

func (a *App) selectTasksQueue(queueID string) {
	a.TasksSection = "queue"
	if queueID != "" {
		a.SelectedCacheQueueID = queueID
	}
	a.closeLivePreview()
}

func (a *App) selectTasksLive() {
	a.TasksSection = "live"
}

func (a *App) selectTasksIncidents(focusKey string) {
	a.TasksSection = "incidents"
	a.SelectedLiveIncidentKey = strings.TrimSpace(focusKey)
	a.closeLivePreview()
}

// OpenCacheQueueTask navigates to /tasks and focuses the specified cache queue task.
func (a *App) OpenCacheQueueTask(taskID string) {
	a.Navigate(RouteTasks)
	a.selectTasksQueue(taskID)
}

func (a *App) activeLiveRecordingCount() int {
	count := 0
	for _, r := range a.LiveRooms {
		if r.Recording {
			count++
		}
	}
	for _, acct := range a.LiveAccounts {
		if normalizeSiteIDUI(acct.Site) == "douyin" && acct.Recording {
			count++
		}
	}
	return count
}

// cacheQueueNavText counts the cache queues still running, waiting or paused.
func (a *App) cacheQueueNavText() string {
	n := 0
	for _, q := range a.CacheQueue {
		if isUnfinishedCacheQueueStatus(q.Status) {
			n++
		}
	}
	return fmt.Sprintf("缓存队列（%d）", n)
}

// renderTasksPanel renders the context sidebar on RouteTasks: six uniform
// entries 下载任务、缓存队列、直播录制、录制异常、检查计划、导入导出; the queues
// themselves are listed on the 缓存队列 page (DEC-057).
func (a *App) renderTasksPanel(c *ui.Context) {
	sec := a.effectiveTasksSection()
	activeDL := a.activeWorkCount()
	activeLive := a.activeLiveRecordingCount()

	ui.Column(c).Fill().ClipX().Children(func() {
		panelTitle(c, "任务")
		ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
			ui.Column(c).FillWidth().Padding(0, 8, 12, 8).Gap(2).ClipX().Children(func() {
				if panelIconItem(c, "tasks", fmt.Sprintf("下载任务（%d）", activeDL), "任务侧栏：下载任务", sec == "downloads") {
					a.selectTasksDownloads()
				}
				if panelIconItem(c, "clock", a.cacheQueueNavText(), "任务侧栏：缓存队列", sec == "queue") {
					a.selectTasksQueue("")
				}
				if panelIconItem(c, "live", fmt.Sprintf("直播录制（%d）", activeLive), "任务侧栏：直播录制", sec == "live") {
					a.selectTasksLive()
				}
				if panelIconItem(c, "alert-circle", a.liveIncidentsNavText(), "任务侧栏：录制异常", sec == "incidents") {
					a.selectTasksIncidents("")
				}
				if panelIconItem(c, "refresh", "检查计划", "任务侧栏：检查计划", sec == "checks") {
					a.selectTasksChecks()
				}
				if panelIconItem(c, "upload", a.transfersNavText(), "任务侧栏：导入导出", sec == "transfers") {
					a.selectTasksTransfers()
				}
			})
		})
	})
}

func (a *App) renderTasksNarrowSelector(c *ui.Context) {
	sec := a.effectiveTasksSection()
	activeDL := a.activeWorkCount()
	activeLive := a.activeLiveRecordingCount()
	opts := []struct {
		key   string
		text  string
		label string
	}{
		{"downloads", fmt.Sprintf("下载任务（%d）", activeDL), "任务分段：下载任务"},
		{"queue", a.cacheQueueNavText(), "任务分段：缓存队列"},
		{"live", fmt.Sprintf("直播录制（%d）", activeLive), "任务分段：直播录制"},
		{"incidents", a.liveIncidentsNavText(), "任务分段：录制异常"},
		{"checks", "检查计划", "任务分段：检查计划"},
		{"transfers", a.transfersNavText(), "任务分段：导入导出"},
	}
	ui.Row(c).
		FillWidth().
		Padding(8, 12).
		BorderWidth(0, 0, 1, 0).
		BorderColor(ColorBorder).
		Background(ColorBgMain).
		Gap(6).
		Wrap().
		Shrink(0).
		ClipX().
		Children(func() {
			for _, opt := range opts {
				o := opt
				active := sec == o.key
				btn := ui.ButtonBase(c).Label(o.label).Padding(5, 10).Radius(999).Center().Shrink(0)
				if active {
					btn.Background(ColorAccentBg).Border(1, ColorAccent)
				} else {
					btn.Background(ColorBgSecondary).Border(1, ColorBorder)
					if btn.Hovered() {
						btn.Background(ColorHover)
					}
				}
				btn.Children(func() {
					t := ui.Text(c, o.text).FontSize(12).SingleLine()
					if active {
						t.FontWeight(600).TextColor(ColorAccent)
					} else {
						t.FontWeight(500).TextColor(ColorTextMuted)
					}
				})
				if btn.Clicked() {
					switch o.key {
					case "downloads":
						a.selectTasksDownloads()
					case "queue":
						a.selectTasksQueue("")
					case "live":
						a.selectTasksLive()
					case "incidents":
						a.selectTasksIncidents("")
					case "checks":
						a.selectTasksChecks()
					case "transfers":
						a.selectTasksTransfers()
					}
				}
			}
		})
}

// renderTasksRoute renders the active section of RouteTasks (Downloads, Cache Queue,
// Live Recordings, Live Incidents, Check Plan, or Transfers).
func (a *App) renderTasksRoute(c *ui.Context) {
	if !a.panelShown {
		a.renderTasksNarrowSelector(c)
	}
	switch a.effectiveTasksSection() {
	case "queue":
		a.renderTasksQueueView(c)
	case "live":
		a.renderTasksLiveView(c)
	case "incidents":
		a.renderTasksIncidentsView(c)
	case "checks":
		a.renderTasksChecksView(c)
	case "transfers":
		a.renderTasksTransfersView(c)
	default:
		a.renderTasksDownloadsView(c)
	}
}
