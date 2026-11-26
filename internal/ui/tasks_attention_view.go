package ui

import (
	"fmt"
	"strings"

	"github.com/chialecode/trace/internal/download"
	"github.com/egoist/mygo/ui"
)

func (a *App) retryableDownloadTasks() []download.Task {
	var out []download.Task
	for _, t := range a.Tasks {
		if download.IsRetryableTask(t) {
			out = append(out, t)
		}
	}
	return out
}

func taskPauseReasonText(reason string) string {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "network", "timeout", "http", "io":
		return "因网络中断暂停，可继续下载"
	case "app_closed", "shutdown", "restart":
		return "上次关闭应用时未完成，可继续下载"
	case "auth_required", "auth", "expired":
		return "登录状态失效导致暂停，重新登录后可继续"
	case "rate_limited", "risk_control", "challenge":
		return "触发风控或限流已暂停，请稍后再试"
	case "disk_full", "no_space":
		return "磁盘空间不足导致暂停"
	default:
		if strings.TrimSpace(reason) != "" {
			return "已暂停（" + strings.TrimSpace(reason) + "），可继续下载"
		}
		return ""
	}
}

// renderTasksAttentionView aggregates all items requiring user action on Tasks › 待处理:
// 1. 内容冲突 (cache conflicts)
// 2. 失败 / 未完成的下载任务 (retryable failed/partial/error-paused tasks)
// 3. 近期录制异常 (recent 7-day & active live recording incident sessions)
func (a *App) renderTasksAttentionView(c *ui.Context) {
	conflicts := a.CacheConflicts
	retryable := a.retryableDownloadTasks()
	recentIncidents := a.recentLiveIncidentSessions()

	hasAny := len(conflicts) > 0 || len(retryable) > 0 || len(recentIncidents) > 0

	ui.Scroll(c).Label("待处理列表").Grow(1).FillWidth().ClipX().Children(func() {
		ui.Column(c).FillWidth().Padding(16).Gap(18).ClipX().Children(func() {
			if a.ConflictActionError != "" {
				renderErrorBanner(c, "冲突操作错误", a.ConflictActionError, func() {
					if OutlinePillButton(c, "关闭", "关闭冲突错误提示").Clicked() {
						a.ConflictActionError = ""
					}
				})
			}
			if a.TaskActionError != "" {
				renderErrorBanner(c, "", a.TaskActionError, nil)
			}

			if !hasAny {
				ui.Column(c).Label("待处理空状态").FillWidth().Padding(48, 16).AlignItems(ui.Center).Gap(8).Children(func() {
					ui.Text(c, "目前没有需要处理的事项").FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					ui.Text(c, "内容冲突、失败或未完成的下载任务，以及最近 7 天的录制异常会汇总在这里。").
						FontSize(13).
						LineHeight(LineHeightCompact).
						TextColor(ColorTextMuted).
						MaxLines(2)
				})
				return
			}

			sectionsRendered := 0
			addDividerIfNeeded := func() {
				if sectionsRendered > 0 {
					ui.Box(c).FillWidth().Height(1).Background(ColorBorder)
				}
				sectionsRendered++
			}

			if len(conflicts) > 0 {
				addDividerIfNeeded()
				a.renderAttentionConflictsGroup(c, conflicts)
			}

			if len(retryable) > 0 {
				addDividerIfNeeded()
				a.renderAttentionFailedTasksGroup(c, retryable)
			}

			if len(recentIncidents) > 0 {
				addDividerIfNeeded()
				a.renderAttentionLiveIncidentsGroup(c, recentIncidents)
			}
		})
	})
}

func (a *App) renderAttentionFailedTasksGroup(c *ui.Context, retryable []download.Task) {
	ui.Column(c).Label("待处理分节：失败下载").FillWidth().Gap(10).ClipX().Children(func() {
		ui.Row(c).
			FillWidth().
			AlignItems(ui.Center).
			Justify(ui.SpaceBetween).
			Gap(8).
			Wrap().
			ClipX().
			Children(func() {
				ui.Text(c, fmt.Sprintf("失败 / 未完成的下载任务（%d）", len(retryable))).
					FontSize(15).
					FontWeight(600).
					TextColor(ColorTextMain).
					SingleLine()
				if AccentPillButton(c, fmt.Sprintf("批量重试（%d）", len(retryable)), "待处理：批量重试").Clicked() {
					a.retryAllTasksAction()
				}
			})

		ui.Column(c).Label("待处理失败任务列表").FillWidth().Gap(10).ClipX().Children(func() {
			for _, task := range retryable {
				t := task
				ui.Box(c).Label("待处理下载卡片：" + t.TaskID).FillWidth().ClipX().Children(func() {
					a.renderDownloadTaskCard(c, t)
				})
			}
		})
	})
}

func (a *App) renderAttentionLiveIncidentsGroup(c *ui.Context, sessions []liveIncidentSession) {
	ui.Column(c).Label("待处理分节：录制异常").FillWidth().Gap(10).ClipX().Children(func() {
		ui.Row(c).
			FillWidth().
			AlignItems(ui.Center).
			Justify(ui.SpaceBetween).
			Gap(8).
			ClipX().
			Children(func() {
				ui.Text(c, fmt.Sprintf("近期录制异常（%d）", len(sessions))).
					FontSize(15).
					FontWeight(600).
					TextColor(ColorTextMain).
					SingleLine()
				ui.Text(c, "最近 7 天").
					FontSize(12).
					TextColor(ColorTextMuted).
					SingleLine()
			})

		ui.Column(c).Label("待处理录制异常列表").FillWidth().Gap(10).ClipX().Children(func() {
			for _, item := range sessions {
				a.renderLiveIncidentSessionCard(c, item)
			}
		})
	})
}
