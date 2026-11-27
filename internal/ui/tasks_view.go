package ui

import (
	"fmt"
	"strings"

	"github.com/chialecode/trace/internal/download"
	"github.com/egoist/mygo/ui"
)

// Canonical Tasks page section keys (DEC-081).
const (
	TasksSectionActive    = "active"
	TasksSectionLive      = "live"
	TasksSectionAttention = "attention"
	TasksSectionRecords   = "records"
)

// effectiveTasksSection normalizes canonical and legacy TasksSection keys
// into the 4 sections (DEC-081) while staying backward-compatible with old state/callers.
func (a *App) effectiveTasksSection() string {
	switch a.TasksSection {
	case TasksSectionLive:
		return TasksSectionLive
	case TasksSectionAttention, "conflicts", "incidents":
		return TasksSectionAttention
	case TasksSectionRecords, "transfers":
		return TasksSectionRecords
	case TasksSectionActive, "", "downloads", "queue", "checks":
		return TasksSectionActive
	default:
		return TasksSectionActive
	}
}

// effectiveTasksActiveTab returns which sub-view ("downloads" or "queue") is
// active inside the 进行中 ("active") section, honoring legacy direct writes
// to TasksSection ("downloads" / "queue").
func (a *App) effectiveTasksActiveTab() string {
	switch a.TasksSection {
	case "queue":
		return "queue"
	case "downloads":
		return "downloads"
	}
	if a.TasksActiveTab == "queue" {
		return "queue"
	}
	return "downloads"
}

// effectiveSelectedQueueID is the queue whose card is open on the 缓存队列
// view, "" when none is.
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

func (a *App) selectTasksActive() {
	a.TasksSection = TasksSectionActive
	a.closeLivePreview()
}

func (a *App) selectTasksDownloads() {
	a.TasksSection = TasksSectionActive
	a.TasksActiveTab = "downloads"
	a.closeLivePreview()
}

func (a *App) selectTasksQueue(queueID string) {
	a.TasksSection = TasksSectionActive
	a.TasksActiveTab = "queue"
	if queueID != "" {
		a.SelectedCacheQueueID = queueID
		if filter := strings.TrimSpace(a.TasksQueueSiteFilter); filter != "" && filter != "all" {
			for _, q := range a.CacheQueue {
				if q.ID == queueID && normalizeSiteIDUI(q.Site) != filter {
					a.TasksQueueSiteFilter = "all"
					break
				}
			}
		}
	}
	a.closeLivePreview()
}

func (a *App) selectTasksLive() {
	a.TasksSection = TasksSectionLive
}

func (a *App) selectTasksAttention() {
	a.TasksSection = TasksSectionAttention
	a.closeLivePreview()
	a.refreshCacheConflicts()
	a.conflictsLoadedOnTasks = true
}

func (a *App) selectTasksIncidents(focusKey string) {
	a.SelectedLiveIncidentKey = strings.TrimSpace(focusKey)
	a.selectTasksAttention()
}

func (a *App) selectTasksRecords() {
	a.TasksSection = TasksSectionRecords
	a.closeLivePreview()
	a.refreshTransferJobs()
}

// OpenCacheQueueTask navigates to /tasks ("active") and expands the specified cache queue task.
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

func (a *App) unfinishedCacheQueueCount() int {
	n := 0
	for _, q := range a.CacheQueue {
		if isUnfinishedCacheQueueStatus(q.Status) {
			n++
		}
	}
	return n
}

// activeTasksSectionCount counts queued/running downloads plus unfinished cache queues
// for the Tasks › 进行中 section label. The left icon rail "任务" badge intentionally
// uses activeWorkCount() (queued/running downloads only, DEC-072).
func (a *App) activeTasksSectionCount() int {
	return a.activeWorkCount() + a.unfinishedCacheQueueCount()
}

func (a *App) retryableTaskCount() int {
	n := 0
	for _, t := range a.Tasks {
		if download.IsRetryableTask(t) {
			n++
		}
	}
	return n
}

func (a *App) attentionItemCount() int {
	return a.effectiveConflictCount() + a.retryableTaskCount() + a.recentLiveIncidentSessionCount()
}

func (a *App) attentionNavText() string {
	if n := a.attentionItemCount(); n > 0 {
		return fmt.Sprintf("待处理（%d）", n)
	}
	return "待处理"
}

func (a *App) recordsNavText() string {
	if n := a.activeTransferCount(); n > 0 {
		return fmt.Sprintf("记录（%d）", n)
	}
	return "记录"
}

// renderTasksPanel renders the context sidebar on RouteTasks: 4 consolidated
// sections 进行中、直播、待处理、记录 (DEC-081).
func (a *App) renderTasksPanel(c *ui.Context) {
	sec := a.effectiveTasksSection()
	activeCount := a.activeTasksSectionCount()
	activeLive := a.activeLiveRecordingCount()

	ui.Column(c).Fill().ClipX().Children(func() {
		panelTitle(c, "任务")
		ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
			ui.Column(c).FillWidth().Padding(0, 8, 12, 8).Gap(2).ClipX().Children(func() {
				if panelIconItem(c, "tasks", fmt.Sprintf("进行中（%d）", activeCount), "任务侧栏：进行中", sec == TasksSectionActive) {
					a.selectTasksActive()
				}
				if panelIconItem(c, "live", fmt.Sprintf("直播（%d）", activeLive), "任务侧栏：直播", sec == TasksSectionLive) {
					a.selectTasksLive()
				}
				if panelIconItem(c, "alert-circle", a.attentionNavText(), "任务侧栏：待处理", sec == TasksSectionAttention) {
					a.selectTasksAttention()
				}
				if panelIconItem(c, "history", a.recordsNavText(), "任务侧栏：记录", sec == TasksSectionRecords) {
					a.selectTasksRecords()
				}
			})
		})
	})
}

func (a *App) renderTasksNarrowSelector(c *ui.Context) {
	sec := a.effectiveTasksSection()
	activeCount := a.activeTasksSectionCount()
	activeLive := a.activeLiveRecordingCount()
	opts := []struct {
		key   string
		text  string
		label string
	}{
		{TasksSectionActive, fmt.Sprintf("进行中（%d）", activeCount), "任务分段：进行中"},
		{TasksSectionLive, fmt.Sprintf("直播（%d）", activeLive), "任务分段：直播"},
		{TasksSectionAttention, a.attentionNavText(), "任务分段：待处理"},
		{TasksSectionRecords, a.recordsNavText(), "任务分段：记录"},
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
					case TasksSectionActive:
						a.selectTasksActive()
					case TasksSectionLive:
						a.selectTasksLive()
					case TasksSectionAttention:
						a.selectTasksAttention()
					case TasksSectionRecords:
						a.selectTasksRecords()
					}
				}
			}
		})
}

// renderTasksActiveView renders the 进行中 section combining 下载任务 and 缓存队列.
func (a *App) renderTasksActiveView(c *ui.Context) {
	subTab := a.effectiveTasksActiveTab()
	activeDL := a.activeWorkCount()
	unfinishedQ := a.unfinishedCacheQueueCount()

	ui.Column(c).Grow(1).FillWidth().ClipX().Children(func() {
		ui.Row(c).
			Label("进行中子标签栏").
			FillWidth().
			Padding(10, 16, 8, 16).
			BorderWidth(0, 0, 1, 0).
			BorderColor(ColorBorder).
			Background(ColorBgMain).
			AlignItems(ui.Center).
			Justify(ui.SpaceBetween).
			Gap(8).
			Wrap().
			Shrink(0).
			ClipX().
			Children(func() {
				renderSegmentedPills(c, subTab, []segmentedChoice{
					{Key: "downloads", Text: fmt.Sprintf("下载任务（%d）", activeDL), Label: "进行中子标签：下载任务"},
					{Key: "queue", Text: fmt.Sprintf("缓存队列（%d）", unfinishedQ), Label: "进行中子标签：缓存队列"},
				}, func(next string) {
					if next == "queue" {
						a.selectTasksQueue("")
					} else {
						a.selectTasksDownloads()
					}
				})
			})

		if subTab == "queue" {
			a.renderTasksQueueView(c)
			return
		}
		a.renderTasksDownloadsView(c)
	})
}

// renderTasksRoute renders the active section of RouteTasks (进行中, 直播, 待处理, or 记录).
func (a *App) renderTasksRoute(c *ui.Context) {
	if !a.panelShown {
		a.renderTasksNarrowSelector(c)
	}
	switch a.effectiveTasksSection() {
	case TasksSectionLive:
		a.renderTasksLiveView(c)
	case TasksSectionAttention:
		a.renderTasksAttentionView(c)
	case TasksSectionRecords:
		a.renderTasksTransfersView(c)
	default:
		a.renderTasksActiveView(c)
	}
}
