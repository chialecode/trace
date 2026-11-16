package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/player"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/egoist/mygo/ui"
)

func (a *App) effectiveTasksSection() string {
	switch a.TasksSection {
	case "queue", "live", "incidents":
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

func (a *App) lookupFileBitmap(fullPath string) *ui.Bitmap {
	fullPath = strings.TrimSpace(fullPath)
	if fullPath == "" {
		return nil
	}
	cacheKey := "file-bmp::" + fullPath
	a.mu.Lock()
	if bmp, ok := a.images.get(cacheKey); ok {
		a.mu.Unlock()
		return bmp
	}
	a.mu.Unlock()

	bmp := loadBitmapFromFile(fullPath)
	if bmp != nil {
		a.mu.Lock()
		if !a.closed {
			a.images.put(cacheKey, bmp)
		}
		a.mu.Unlock()
	}
	return bmp
}

func (a *App) renderQueueLocalAvatar(c *ui.Context, q service.CacheQueueTask, size float32) {
	avatarPath := strings.TrimSpace(q.AvatarPath)
	if avatarPath == "" && a.backend != nil && q.User != "" {
		avatarPath = a.backend.AccountAvatarPath(normalizeSiteIDUI(q.Site), q.User)
	}
	bmp := a.lookupFileBitmap(avatarPath)
	name := firstNonEmptyStrUI(q.UserName, q.User, "创作者")
	col := AvatarColor(name, 0)
	r := size / 2
	ui.Box(c).Size(size, size).Shrink(0).Radius(r).Background(col).Clip().Center().Children(func() {
		if bmp != nil {
			ui.Image(c, bmp).Size(size, size).Fit(ui.Cover)
			return
		}
		initial := "?"
		for _, ch := range name {
			initial = string(ch)
			break
		}
		ui.Text(c, initial).FontSize(size * 0.42).FontWeight(600).TextColor(ColorWhite).SingleLine()
	})
}

func formatRFC3339Clock(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return raw
	}
	local := t.Local()
	now := time.Now()
	if local.Year() == now.Year() && local.YearDay() == now.YearDay() {
		return local.Format("15:04")
	}
	return local.Format("01-02 15:04")
}

func formatQueueSidebarStatus(q service.CacheQueueTask) string {
	switch q.Status {
	case service.CacheQueueRunning:
		return "进行中"
	case service.CacheQueueQueued:
		return "排队中"
	case service.CacheQueueWaiting:
		if clock := formatRFC3339Clock(q.NextRunAt); clock != "" {
			return "下次 " + clock
		}
		return "等待下一条"
	case service.CacheQueueWaitingBackoff:
		if clock := formatRFC3339Clock(q.NextRunAt); clock != "" {
			return "退避至 " + clock
		}
		return "等待退避结束"
	case service.CacheQueuePaused:
		return "已暂停"
	case service.CacheQueueCompleted:
		return "已完成"
	case service.CacheQueueStopped:
		return firstNonEmptyStrUI(q.StopReason, "已停止")
	default:
		return firstNonEmptyStrUI(q.StatusText, q.Status)
	}
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

// renderTasksPanel renders the context sidebar on RouteTasks: four uniform
// entries 下载任务、缓存队列、直播录制、录制异常; the queues themselves are
// listed on the 缓存队列 page (DEC-057).
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
	}
	ui.Row(c).
		FillWidth().
		Padding(8, 16).
		BorderWidth(0, 0, 1, 0).
		BorderColor(ColorBorder).
		Background(ColorBgMain).
		Shrink(0).
		ClipX().
		Children(func() {
			ui.Row(c).FillWidth().Padding(3).Radius(999).Background(ColorBgSecondary).Gap(4).ClipX().Children(func() {
				for _, opt := range opts {
					o := opt
					active := sec == o.key
					btn := ui.ButtonBase(c).Label(o.label).Grow(1).Padding(6, 8).Radius(999).Center().ClipX()
					if active {
						btn.Background(ColorBgMain).Border(1, ColorBorder)
					} else if btn.Hovered() {
						btn.Background(ColorHover)
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
						}
					}
				}
			})
		})
}

// renderTasksRoute renders the active section of RouteTasks (Downloads, Cache Queue Detail, Live Recordings, or Live Incidents).
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
	default:
		a.renderTasksDownloadsView(c)
	}
}

func (a *App) renderTasksDownloadsView(c *ui.Context) {
	ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
		ui.Column(c).FillWidth().Padding(16).Gap(12).ClipX().Children(func() {
			if len(a.UpgradeCandidates) > 0 {
				ui.Column(c).FillWidth().Padding(14).Radius(16).Background(ColorInfoBlueBg).Border(1, ColorInfoBlue).Gap(8).ClipX().Children(func() {
					ui.Text(c, fmt.Sprintf("可升级画质视频（%d）", len(a.UpgradeCandidates))).FontSize(14).FontWeight(600).TextColor(ColorInfoBlue).SingleLine()
					for _, cand := range a.UpgradeCandidates {
						it := cand
						ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
							ui.Text(c, fmt.Sprintf("%s（%s）", it.Title, it.UpgradeHint)).
								FontSize(13).TextColor(ColorTextMain).SingleLine().Grow(1)
							if AccentPillButton(c, "升级缓存", "升级缓存："+it.RawID).Clicked() {
								if it.ItemKey != "" {
									_, _ = a.backend.Upgrade(context.Background(), it.ItemKey)
								} else {
									_, _ = a.backend.EnqueueVideoCache(context.Background(), download.VideoEnqueueRequest{
										BVID: it.RawID,
									})
								}
								a.RefreshData()
							}
						})
					}
				})
			}

			if a.TaskActionError != "" {
				ui.Text(c, a.TaskActionError).FontSize(13).TextColor(ColorError).MaxLines(2)
			}
			finished := 0
			for _, t := range a.Tasks {
				if t.Status == "completed" || t.Status == "canceled" {
					finished++
				}
			}
			if finished > 0 {
				ui.Row(c).FillWidth().Justify(ui.End).Children(func() {
					if OutlinePillButton(c, fmt.Sprintf("清除已结束（%d）", finished), "清除已结束的任务").Clicked() {
						_, err := a.backend.RemoveFinishedTasks()
						a.finishTaskAction(err)
					}
				})
			}

			if len(a.Tasks) == 0 {
				ui.Column(c).FillWidth().Padding(40, 16).AlignItems(ui.Center).Gap(8).Children(func() {
					ui.Text(c, "当前没有缓存任务").FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					ui.Text(c, "在动态或视频详情上点「缓存」后，下载进度与子文件详情会显示在这里。").FontSize(13).TextColor(ColorTextMuted)
				})
				return
			}

			for _, t := range a.Tasks {
				task := t
				taskSite := normalizeSiteIDUI(task.Site)
				statusLabel := taskStatusText(task.Status)
				barCol := ColorInfoBlue
				switch task.Status {
				case "completed":
					barCol = ColorDotGreen
				case "paused":
					barCol = ColorDotYellow
				case "failed":
					barCol = ColorDotRed
				}
				isExpanded := a.ExpandedTaskID == task.TaskID

				ui.Column(c).
					FillWidth().
					Padding(14).
					Radius(16).
					Background(ColorBgMain).
					Border(1, ColorBorder).
					Gap(8).
					ClipX().
					Children(func() {
						ui.Row(c).FillWidth().AlignItems(ui.Start).Gap(12).ClipX().Children(func() {
							a.renderMediaThumb(c, task.ItemKey, "", task.Title, "", task.Status, false, 96, 58)

							ui.Column(c).Grow(1).Gap(6).ClipX().Children(func() {
								ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
									ui.Row(c).AlignItems(ui.Center).Gap(6).Grow(1).ClipX().Children(func() {
										DrawStatusDotFull(c, task.Status, task.Progress, task.Status == "paused", "任务状态："+task.Status)
										ui.Text(c, fmt.Sprintf("%s (%s)", task.Title, task.TargetID)).
											FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
										renderSiteSmallBadgeWithDot(c, taskSite, "任务站点："+task.TaskID)
										if task.Source == "auto" {
											ui.Box(c).Padding(1, 6).Radius(4).Background(ColorBgMain).Border(1, ColorBorder).Children(func() {
												ui.Text(c, "自动").FontSize(11).FontWeight(600).TextColor(ColorTextMuted).SingleLine()
											})
										}
									})
									ui.Text(c, fmt.Sprintf("%s · %.0f%% (%s / %s)",
										statusLabel,
										task.Progress*100,
										FormatBytes(task.BytesDownloaded),
										FormatBytes(task.TotalBytes))).
										FontSize(12).FontWeight(600).TextColor(ColorTextMuted).SingleLine()
								})

								ui.Box(c).FillWidth().Height(6).Radius(3).Background(ColorBorder).Clip().Draw(func(p *ui.Painter, r ui.Rect) {
									frac := float32(max(0.0, min(1.0, task.Progress)))
									if frac > 0 {
										p.Fill(ui.Rect{X: r.X, Y: r.Y, W: r.W * frac, H: r.H}, barCol, 3)
									}
								})

								if task.QualityHint != "" {
									ui.Text(c, "画质与音轨："+task.QualityHint).FontSize(12).FontWeight(600).TextColor(ColorInfoBlue).SingleLine()
								}
								lastErr := firstNonEmptyStrUI(task.LastError, task.ErrorMessage)
								if lastErr != "" {
									ui.Row(c).FillWidth().Padding(6, 10).Radius(8).Background(ColorErrorBg).AlignItems(ui.Center).Gap(6).Children(func() {
										DrawIcon(c, "alert-circle", 14, ColorError, 1.8)
										errLine := "原因：" + lastErr
										if task.ErrorKind != "" {
											errLine = fmt.Sprintf("原因 [%s]：%s", task.ErrorKind, lastErr)
										}
										ui.Text(c, errLine).
											FontSize(12).TextColor(ColorError).MaxLines(2).Grow(1)
									})
								}

								if a.TaskRemoveConfirmID == task.TaskID {
									ui.Row(c).
										FillWidth().
										Padding(8, 12).
										Radius(10).
										Background(ColorWarnBg).
										Border(1, ColorDotYellow).
										AlignItems(ui.Center).
										Justify(ui.SpaceBetween).
										Gap(8).
										Children(func() {
											ui.Text(c, "确认移除该任务？会删除未完成的临时文件，已缓存完成的文件会保留。").
												FontSize(12).FontWeight(600).TextColor(ColorTextMain).MaxLines(2).Grow(1)
											ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
												if OutlinePillButton(c, "取消", "取消移除任务："+task.TaskID).Clicked() {
													a.TaskRemoveConfirmID = ""
												}
												if DangerOutlinePillButton(c, "确认移除", "确认移除任务："+task.TaskID).Clicked() {
													a.TaskRemoveConfirmID = ""
													a.finishTaskAction(a.backend.RemoveTask(task.TaskID))
												}
											})
										})
								}

								ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Wrap().ClipX().Children(func() {
									if task.Status == "running" || task.Status == "queued" {
										if OutlinePillButton(c, "暂停", "暂停任务："+task.TaskID).Clicked() {
											_, err := a.backend.PauseTask(task.TaskID)
											a.finishTaskAction(err)
										}
									}
									if task.Status == "paused" || strings.HasPrefix(task.Status, "paused_") {
										if AccentPillButton(c, "继续", "继续任务："+task.TaskID).Clicked() {
											_, err := a.backend.ResumeTask(task.TaskID)
											a.finishTaskAction(err)
										}
									}
									if task.Status == "failed" || task.Status == "partial" {
										if AccentPillButton(c, "重试", "重试任务："+task.TaskID).Clicked() {
											_, err := a.backend.RetryTask(task.TaskID)
											a.finishTaskAction(err)
										}
									}
									switch task.Status {
									case "failed", "partial":
										if OutlinePillButton(c, "移除", "移除任务："+task.TaskID).Clicked() {
											a.TaskRemoveConfirmID = task.TaskID
										}
									case "completed", "canceled":
										if OutlinePillButton(c, "移除", "移除任务："+task.TaskID).Clicked() {
											a.finishTaskAction(a.backend.RemoveTask(task.TaskID))
										}
									default:
										if DangerOutlinePillButton(c, "取消", "取消任务："+task.TaskID).Clicked() {
											_, err := a.backend.CancelTask(task.TaskID)
											a.finishTaskAction(err)
										}
									}
									expandLabel := "展开详情"
									if isExpanded {
										expandLabel = "收起详情"
									}
									if OutlinePillButton(c, expandLabel, "展开任务详情："+task.TaskID).Clicked() {
										if isExpanded {
											a.ExpandedTaskID = ""
										} else {
											a.ExpandedTaskID = task.TaskID
										}
									}
								})
							})
						})

						if isExpanded {
							detail, ok, _ := a.backend.TaskDetail(task.TaskID)
							files := task.Files
							attempts := task.Attempts
							if ok {
								if len(detail.Files) > 0 {
									files = detail.Files
								}
								if len(detail.Attempts) > 0 {
									attempts = detail.Attempts
								}
							}

							ui.Column(c).
								FillWidth().
								Padding(10, 12).
								Radius(12).
								Background(ColorBgMain).
								Border(1, ColorBorder).
								Gap(6).
								ClipX().
								Children(func() {
									ui.Text(c, fmt.Sprintf("逐文件进度清单（%d 个文件）", len(files))).
										FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
									if len(files) == 0 {
										ui.Text(c, "P01 视频流 (AVC/AV1) · P01 音轨流 (Hi-Res/192K) · P01.danmaku.xml · cover.png").
											FontSize(12).TextColor(ColorTextMuted).SingleLine()
									}
									for _, f := range files {
										fl := f
										ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
											ui.Text(c, fmt.Sprintf("[%s] %s", fl.Role, fl.Path)).FontSize(12).FontWeight(600).TextColor(ColorTextMain).SingleLine().Grow(1)
											ui.Text(c, fmt.Sprintf("%s · %s / %s",
												fl.Status,
												FormatBytes(fl.DownloadedBytes),
												FormatBytes(fl.TotalBytes))).FontSize(12).TextColor(ColorTextMuted).SingleLine()
										})
									}
									if len(attempts) > 0 {
										ui.Text(c, fmt.Sprintf("最近分段重试记录（已脱敏，共 %d 条）", len(attempts))).
											FontSize(12).FontWeight(600).TextColor(ColorTextMain).SingleLine()
										limit := min(len(attempts), 5)
										for i := 0; i < limit; i++ {
											att := attempts[i]
											ui.Text(c, fmt.Sprintf("%s · 文件 %s · 节点 %s · 偏移 %s · 错误 %s (第 %d 次)",
												att.Time, att.File, att.Host, FormatBytes(att.Offset), att.ErrorKind, att.Attempt)).
												FontSize(11).TextColor(ColorTextMuted).SingleLine()
										}
									}
								})
						}
					})
			}
		})
	})
}

// renderTasksQueueView lists every cache queue as a card like the download
// tasks; a card opens to show what that queue cached most recently (DEC-057).
func (a *App) renderTasksQueueView(c *ui.Context) {
	ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
		ui.Column(c).FillWidth().Padding(16).Gap(12).ClipX().Children(func() {
			if a.TaskActionError != "" {
				ui.Text(c, a.TaskActionError).FontSize(13).TextColor(ColorError).MaxLines(2)
			}
			if len(a.CacheQueue) == 0 {
				ui.Column(c).FillWidth().Padding(40, 16).AlignItems(ui.Center).Gap(8).Children(func() {
					ui.Text(c, "当前没有缓存队列任务").FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					ui.Text(c, "在 UP 主或抖音作者主页右上角点「⋯ → 全部缓存」，会在后台按安全节奏慢慢缓存全部内容。").
						FontSize(13).TextColor(ColorTextMuted).MaxLines(2)
				})
				return
			}

			finished := 0
			for _, q := range a.CacheQueue {
				if !isUnfinishedCacheQueueStatus(q.Status) {
					finished++
				}
			}
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
				ui.Text(c, "取消或移除队列不会删除已缓存到媒体库的内容").FontSize(12).TextColor(ColorTextMuted).SingleLine()
				if finished > 0 {
					if OutlinePillButton(c, fmt.Sprintf("清除已结束（%d）", finished), "清除已结束的缓存队列").Clicked() {
						_, err := a.backend.RemoveFinishedCacheQueues()
						a.finishCacheQueueAction(err)
					}
				}
			})

			selID := a.effectiveSelectedQueueID()
			for _, item := range a.CacheQueue {
				a.renderCacheQueueCard(c, item, item.ID == selID)
			}
		})
	})
}

func (a *App) finishCacheQueueAction(err error) {
	a.finishTaskAction(err)
	if a.backend != nil {
		a.CacheQueue = a.backend.ListCacheQueue()
	}
}

func (a *App) renderCacheQueueCard(c *ui.Context, cur service.CacheQueueTask, expanded bool) {
	qSite := normalizeSiteIDUI(cur.Site)
	unfinished := isUnfinishedCacheQueueStatus(cur.Status)
	borderCol := ColorBorder
	if expanded {
		borderCol = ColorAccent
	} else if cur.Status == service.CacheQueueRunning {
		borderCol = ColorInfoBlue
	}
	ui.Column(c).
		Label("缓存队列卡片："+cur.ID).
		FillWidth().
		Padding(14).
		Radius(16).
		Background(ColorBgMain).
		Border(1, borderCol).
		Gap(10).
		ClipX().
		Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(12).ClipX().Children(func() {
				head := ui.ButtonBase(c).
					Label("展开缓存队列："+cur.ID).
					Grow(1).
					Gap(12).
					Padding(2, 4).
					Radius(10).
					AlignItems(ui.Center).
					Justify(ui.Start).
					ClipX()
				if head.Hovered() {
					head.Background(ColorHover)
				}
				head.Children(func() {
					a.renderQueueLocalAvatar(c, cur, 40)
					ui.Column(c).Grow(1).Gap(4).ClipX().Children(func() {
						ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
							title := firstNonEmptyStrUI(cur.UserName, cur.User)
							if cur.ListLabel != "" {
								title += " · " + cur.ListLabel
							}
							ui.Text(c, title).FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
							renderSiteSmallBadgeWithDot(c, qSite, "队列详情站点："+cur.ID)
						})
						statusLine := formatQueueSidebarStatus(cur)
						if unfinished && cur.NextRunAt != "" && cur.Status != service.CacheQueueWaiting && cur.Status != service.CacheQueueWaitingBackoff {
							if clock := formatRFC3339Clock(cur.NextRunAt); clock != "" {
								statusLine += " · 下次执行 " + clock
							}
						}
						statusColor := ColorTextMuted
						switch cur.Status {
						case service.CacheQueueRunning:
							statusColor = ColorAccent
						case service.CacheQueueWaitingBackoff, service.CacheQueuePaused:
							statusColor = ColorDotYellow
						case service.CacheQueueCompleted:
							statusColor = ColorDotGreen
						}
						ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
							ui.Text(c, statusLine).Label("队列详情状态：" + cur.ID).FontSize(12).FontWeight(600).TextColor(statusColor).SingleLine()
							ui.Text(c, fmt.Sprintf("· 已缓存 %d 条 · 跳过 %d 条", cur.CachedCount, cur.SkippedCount)).
								Label("队列详情计数：" + cur.ID).
								FontSize(12).TextColor(ColorTextMuted).SingleLine()
						})
					})
					icon := "chevron-down"
					if expanded {
						icon = "chevron-up"
					}
					DrawIcon(c, icon, 16, ColorTextMuted, 1.6)
				})
				if head.Clicked() {
					if expanded {
						a.SelectedCacheQueueID = ""
					} else {
						a.SelectedCacheQueueID = cur.ID
					}
				}

				ui.Row(c).Shrink(0).Gap(6).AlignItems(ui.Center).Children(func() {
					if unfinished {
						if cur.Status == service.CacheQueuePaused {
							if AccentPillButton(c, "继续", "继续缓存队列："+cur.ID).Clicked() {
								_, err := a.backend.ResumeCacheQueue(cur.ID)
								a.finishCacheQueueAction(err)
							}
						} else if OutlinePillButton(c, "暂停", "暂停缓存队列："+cur.ID).Clicked() {
							_, err := a.backend.PauseCacheQueue(cur.ID)
							a.finishCacheQueueAction(err)
						}
						if DangerOutlinePillButton(c, "取消", "取消缓存队列："+cur.ID).Clicked() {
							_, err := a.backend.CancelCacheQueue(cur.ID)
							a.finishCacheQueueAction(err)
						}
					} else if OutlinePillButton(c, "移除", "移除缓存队列："+cur.ID).Clicked() {
						if a.SelectedCacheQueueID == cur.ID {
							a.SelectedCacheQueueID = ""
						}
						a.finishCacheQueueAction(a.backend.RemoveCacheQueue(cur.ID))
					}
				})
			})

			if cur.StopReason != "" && !strings.Contains(formatQueueSidebarStatus(cur), cur.StopReason) {
				ui.Text(c, "原因："+cur.StopReason).FontSize(12).TextColor(ColorTextMuted).MaxLines(2)
			}
			if cur.Status == service.CacheQueueRunning && cur.ActiveTitle != "" {
				ui.Text(c, "当前正在缓存："+cur.ActiveTitle).FontSize(12).FontWeight(600).TextColor(ColorAccent).SingleLine()
			}
			if expanded {
				a.renderCacheQueueRecent(c, cur)
			}
		})
}

func (a *App) renderCacheQueueRecent(c *ui.Context, cur service.CacheQueueTask) {
	ui.Column(c).
		FillWidth().
		BorderWidth(1, 0, 0, 0).
		BorderColor(ColorBorder).
		Padding(10, 0, 0, 0).
		Gap(4).
		ClipX().
		Children(func() {
			ui.Text(c, "最近缓存的内容").FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
			if len(cur.RecentItems) == 0 {
				ui.Text(c, "还没有通过这个队列缓存完成的条目。已缓存或不支持的类型会自动跳过。").FontSize(12).TextColor(ColorTextMuted).MaxLines(2)
				return
			}
			for _, recent := range cur.RecentItems {
				ri := recent
				row := ui.ButtonBase(c).
					Label("队列最近缓存："+ri.ItemKey).
					FillWidth().
					Padding(6, 8).
					Radius(10).
					Gap(10).
					AlignItems(ui.Center).
					Justify(ui.Start).
					ClipX()
				if row.Hovered() {
					row.Background(ColorHover)
				}
				row.Children(func() {
					coverKey, coverRel := a.queueRecentCover(ri.ItemKey)
					a.renderMediaThumb(c, coverKey, coverRel, ri.Title, "", "", false, 72, 44)
					ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
						ui.Text(c, firstNonEmptyStrUI(ri.Title, ri.ItemKey)).FontSize(13).FontWeight(500).TextColor(ColorTextMain).SingleLine()
						if ts := formatRFC3339Clock(ri.CachedAt); ts != "" {
							ui.Text(c, "缓存于 "+ts).FontSize(11).TextColor(ColorTextMuted).SingleLine()
						}
					})
				})
				if row.Clicked() && ri.ItemKey != "" && a.backend != nil {
					a.openQueueRecentItem(ri.ItemKey)
				}
			}
		})
}

// queueRecentCover is the item and file that picture a cached item in the
// queue's recent list: its cover or first image, else the cover of the video
// a dynamic refers to (read once per item).
func (a *App) queueRecentCover(itemKey string) (string, string) {
	if itemKey == "" || a.backend == nil {
		return itemKey, ""
	}
	if a.queueCoversRev != a.cacheStateRev {
		a.queueCovers, a.queueCoversRev = nil, a.cacheStateRev
	}
	if got, ok := a.queueCovers[itemKey]; ok {
		return got[0], got[1]
	}
	if a.queueCovers == nil {
		a.queueCovers = make(map[string][2]string)
	}
	got := [2]string{itemKey, ""}
	if item, ok, err := a.backend.GetLibraryItem(itemKey); err == nil && ok {
		got[1] = libraryItemCoverRel(item)
		for _, vid := range item.Videos {
			if got[1] != "" {
				break
			}
			vKey := library.CanonicalItemKey(item.Site, "video", vid)
			if v, ok, err := a.backend.GetLibraryItem(vKey); err == nil && ok {
				if rel := libraryItemCoverRel(v); rel != "" {
					got = [2]string{vKey, rel}
				}
			}
		}
	}
	a.queueCovers[itemKey] = got
	return got[0], got[1]
}

// libraryItemCoverRel picks the image that represents an item.
func libraryItemCoverRel(item library.ItemMetadata) string {
	for _, m := range item.Media {
		if m.Role == "cover" && m.Path != "" {
			return m.Path
		}
	}
	for _, m := range item.Media {
		if m.Role == "image" && m.Path != "" {
			return m.Path
		}
	}
	for _, pg := range item.NotePages {
		for _, m := range []string{pg.Image, pg.Cover} {
			if m != "" {
				return m
			}
		}
	}
	return ""
}

func (a *App) openQueueRecentItem(itemKey string) {
	item, ok, err := a.backend.GetLibraryItem(itemKey)
	if err != nil || !ok {
		return
	}
	hasVid := len(item.Parts) > 0 || item.DouyinVideo != nil
	var imgs []string
	for _, m := range item.Media {
		if m.Role == "video" || m.Role == "live_segment" {
			hasVid = true
		} else if m.Role == "image" && m.Path != "" {
			imgs = append(imgs, m.Path)
		}
	}
	switch {
	case hasVid:
		a.OpenPlayerOverlay(item)
	case len(imgs) > 0:
		a.OpenItemImageViewer(item.ItemKey, nil, imgs, 0)
	default:
		a.OpenLibraryItemDetail(item)
	}
}

type activeLiveRow struct {
	RecKey       string
	Site         string
	BadgeLabel   string
	StopBtnLabel string
	Uname        string
	UID          string
	Mid          int64
	Face         string
	RoomID       int64
	RoomIDStr    string
	Title        string
	QualSpec     string
	DurationSec  int
	Bytes        int64
	SpeedBps     int64
	Segments     int
	ItemDir      string
	Incidents    []library.LiveIncident
}

func (a *App) collectActiveLiveRows() []activeLiveRow {
	var rows []activeLiveRow
	for _, room := range a.LiveRooms {
		if !room.Recording {
			continue
		}
		qualSpec := bilibili.FormatLiveQualitySpec(room.QualityName, room.Quality, room.Codec)
		roomDec := strconv.FormatInt(room.RoomID, 10)
		uidStr := strconv.FormatInt(room.UID, 10)
		rows = append(rows, activeLiveRow{
			RecKey:       fmt.Sprintf("bilibili:live:%d", room.RoomID),
			Site:         "bilibili",
			BadgeLabel:   fmt.Sprintf("录制站点：bilibili:%d", room.RoomID),
			StopBtnLabel: fmt.Sprintf("任务停止录制 %d", room.RoomID),
			Uname:        firstNonEmptyStrUI(room.Uname, fmt.Sprintf("UID %d", room.UID)),
			UID:          uidStr,
			Mid:          room.UID,
			RoomID:       room.RoomID,
			RoomIDStr:    roomDec,
			Title:        firstNonEmptyStrUI(room.Title, "哔哩哔哩直播"),
			QualSpec:     qualSpec,
			DurationSec:  int(room.RecordDurationSec),
			Bytes:        room.RecordBytes,
			SpeedBps:     room.RecordSpeedBps,
			Segments:     max(1, room.RecordSegments),
			ItemDir:      room.RecordItemDir,
			Incidents:    a.resolveActiveRowIncidents("bilibili", roomDec, uidStr, room.RecordItemDir, room.Incidents),
		})
	}
	for _, acct := range a.LiveAccounts {
		if normalizeSiteIDUI(acct.Site) != "douyin" || !acct.Recording {
			continue
		}
		recKey := firstNonEmptyStrUI(acct.RecordingKey, acct.AccountKey, acct.RoomID, acct.UID)
		badgeKey := firstNonEmptyStrUI(acct.AccountKey, "douyin:"+acct.UID)
		rows = append(rows, activeLiveRow{
			RecKey:       recKey,
			Site:         "douyin",
			BadgeLabel:   "录制站点：" + badgeKey,
			StopBtnLabel: "任务停止录制 " + badgeKey,
			Uname:        firstNonEmptyStrUI(acct.Uname, acct.UID),
			UID:          acct.UID,
			Face:         acct.Face,
			RoomIDStr:    acct.RoomID,
			Title:        firstNonEmptyStrUI(acct.Title, "抖音直播"),
			QualSpec:     formatLiveAccountQualitySpec(acct),
			DurationSec:  int(acct.RecordDurationSec),
			Bytes:        acct.RecordBytes,
			SpeedBps:     acct.RecordSpeedBps,
			Segments:     max(1, acct.RecordSegments),
			ItemDir:      acct.RecordItemDir,
			Incidents:    a.resolveActiveRowIncidents("douyin", acct.RoomID, acct.UID, acct.RecordItemDir, acct.Incidents),
		})
	}
	return rows
}

func (a *App) renderTasksLiveView(c *ui.Context) {
	rows := a.collectActiveLiveRows()
	if len(rows) == 0 && a.LivePreviewKey != "" {
		a.closeLivePreview()
	}

	ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
		ui.Column(c).FillWidth().Padding(16).Gap(12).ClipX().Children(func() {
			if len(rows) == 0 {
				ui.Column(c).Label("直播录制空状态").FillWidth().Padding(48, 16).AlignItems(ui.Center).Gap(8).Children(func() {
					ui.Text(c, "现在没有直播在录制").FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					ui.Text(c, "在订阅页、主播空间或直播间点「录制」后，正在录制的直播会显示在这里，点录制行可实时预览画面。").
						FontSize(13).TextColor(ColorTextMuted).MaxLines(2)
				})
			}

			for _, item := range rows {
				row := item
				previewing := a.LivePreviewKey != "" && a.matchesLivePreviewKey(row.RecKey, row.RoomID, row.UID)
				borderCol := ColorLive
				if previewing {
					borderCol = ColorAccent
				}
				ui.Row(c).
					FillWidth().
					Padding(12, 14).
					Radius(16).
					Background(ColorLiveBg).
					Border(1, borderCol).
					AlignItems(ui.Center).
					Justify(ui.SpaceBetween).
					Gap(12).
					ClipX().
					Children(func() {
						clickArea := ui.ButtonBase(c).
							Label("预览直播录制："+row.RecKey).
							Grow(1).
							Gap(12).
							AlignItems(ui.Center).
							Justify(ui.Start).
							Radius(10).
							Padding(2, 4).
							ClipX()
						if clickArea.Hovered() {
							clickArea.Background(ColorHover)
						}
						clickArea.Children(func() {
							if row.Site == "bilibili" {
								a.renderAvatar(c, row.Uname, row.Mid, 42, true, "", nil)
							} else {
								a.renderAccountAvatar(c, "douyin", row.Uname, row.UID, 0, 42, true, "", nil, row.Face)
							}
							ui.Column(c).Grow(1).Gap(4).ClipX().Children(func() {
								ui.Row(c).AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
									ui.Box(c).Shrink(0).Padding(2, 8).Radius(999).Background(ColorLive).Children(func() {
										ui.Text(c, "录制中").FontSize(11).FontWeight(600).TextColor(ColorWhite).SingleLine()
									})
									if previewing {
										ui.Box(c).Shrink(0).Padding(2, 8).Radius(999).Background(ColorAccentBg).Children(func() {
											ui.Text(c, "正在预览").FontSize(11).FontWeight(600).TextColor(ColorAccent).SingleLine()
										})
									}
									ui.Text(c, row.Uname+" · "+row.Title).FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
									renderSiteSmallBadgeWithDot(c, row.Site, row.BadgeLabel)
									if row.QualSpec != "" {
										ui.Text(c, "· "+row.QualSpec).Shrink(0).FontSize(12).FontWeight(600).TextColor(ColorLive).SingleLine()
									}
								})
								recMeta := fmt.Sprintf("已录时长 %s · 大小 %s · 速度 %s/s · 第 %d 段",
									FormatDuration(row.DurationSec),
									FormatBytes(row.Bytes),
									FormatBytes(row.SpeedBps),
									row.Segments)
								if row.QualSpec != "" {
									recMeta += " · " + row.QualSpec
								}
								recMeta += " · 点击实时预览"
								ui.Text(c, recMeta).FontSize(12).TextColor(ColorTextMuted).SingleLine()
							})
						})
						if clickArea.Clicked() {
							a.StartLivePreview(row.RecKey)
						}

						ui.Row(c).AlignItems(ui.Center).Gap(8).Shrink(0).Children(func() {
							if len(row.Incidents) > 0 {
								renderLiveRowIncidentPill(c, row.RecKey, len(row.Incidents), func() {
									a.selectTasksIncidents(row.RecKey)
								})
							}
							if DangerOutlinePillButton(c, "停止录制", row.StopBtnLabel).Clicked() {
								if row.Site == "bilibili" && row.RoomID > 0 {
									a.stopLiveRecording(row.RoomID)
								} else {
									a.stopAccountLiveRecording(row.RecKey)
								}
							}
						})
					})
			}

			if a.LivePreviewKey != "" {
				a.renderLivePreviewCard(c)
			}

			a.renderRecentFinishedLiveSection(c, rows)
		})
	})
}

func (a *App) renderLivePreviewCard(c *ui.Context) {
	var snap player.StateSnapshot
	if a.livePreviewPlayer != nil {
		snap = a.livePreviewPlayer.Snapshot(640, 360)
	}

	ui.Column(c).
		Label("直播实时预览面板").
		FillWidth().
		Padding(14).
		Radius(16).
		Background(ColorBgMain).
		Border(1, ColorBorder).
		Gap(10).
		ClipX().
		Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(8).Grow(1).ClipX().Children(func() {
					ui.Box(c).Label("直播实时预览标记").Shrink(0).Padding(2, 8).Radius(999).Background(ColorLive).Children(func() {
						ui.Text(c, "实时预览").FontSize(11).FontWeight(600).TextColor(ColorWhite).SingleLine()
					})
					ui.Text(c, firstNonEmptyStrUI(a.LivePreviewTitle, "正在录制的直播")).
						FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine().Grow(1)
					if a.LivePreviewPartIndex > 0 {
						ui.Text(c, fmt.Sprintf("第 %d 段", a.LivePreviewPartIndex)).
							FontSize(12).TextColor(ColorTextMuted).SingleLine().Shrink(0)
					}
				})
				if OutlinePillButton(c, "关闭预览", "关闭直播实时预览").Clicked() {
					a.closeLivePreview()
				}
			})

			// Video surface: strictly no danmaku rendering and no danmaku controls (DEC-049 ①).
			ui.Column(c).
				Label("直播实时预览画面").
				FillWidth().
				Height(280).
				Radius(12).
				Background(ColorBlack).
				Clip().
				Center().
				Children(func() {
					switch {
					case a.LivePreviewUnsupported:
						ui.Column(c).Padding(24).AlignItems(ui.Center).Gap(6).Children(func() {
							ui.Text(c, "这场录制暂不支持实时预览").
								Label("直播不支持实时预览提示").
								FontSize(14).FontWeight(600).TextColor(ColorWhite).SingleLine()
							if a.LivePreviewReason != "" && a.LivePreviewReason != "这场录制暂不支持实时预览" {
								ui.Text(c, a.LivePreviewReason).FontSize(12).TextColor(ui.RGBA(255, 255, 255, 0.72)).MaxLines(2)
							}
						})
					case a.LivePreviewError != "":
						ui.Column(c).Padding(24).AlignItems(ui.Center).Gap(10).Children(func() {
							ui.Text(c, a.LivePreviewError).FontSize(13).FontWeight(600).TextColor(ColorError).MaxLines(2)
							if OutlinePillButton(c, "重试", "重试直播实时预览").Clicked() {
								key := a.LivePreviewKey
								a.StartLivePreview(key)
							}
						})
					case a.LivePreviewLoading:
						ui.Column(c).AlignItems(ui.Center).Gap(8).Children(func() {
							ui.Spinner(c).Label("直播预览加载中").Size(24, 24)
							ui.Text(c, "正在读取本机录制分片…").FontSize(12).TextColor(ui.RGBA(255, 255, 255, 0.75)).SingleLine()
						})
					default:
						if a.livePreviewPlayer != nil {
							if bmp := a.livePreviewPlayer.Bitmap(); bmp != nil {
								ui.Image(c, bmp).Fill().Fit(ui.Contain)
							}
						}
					}
				})

			if !a.LivePreviewUnsupported && a.LivePreviewError == "" {
				ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(10).Wrap().ClipX().Children(func() {
					ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
						playText := "暂停"
						if !snap.Playing {
							playText = "继续播放"
						}
						if OutlinePillButton(c, playText, "直播预览播放暂停").Disabled(a.LivePreviewLoading).Clicked() && a.livePreviewPlayer != nil {
							a.livePreviewPlayer.TogglePlay()
						}
						ui.Text(c, fmt.Sprintf("已播 %s · 读取本机录制文件（不另开站点连接）", FormatDuration(int(snap.PositionSec)))).
							FontSize(12).TextColor(ColorTextMuted).SingleLine()
					})

					ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
						muted := snap.Muted
						if ui.Switch(c, &muted).Label("直播预览静音").Changed() && a.livePreviewPlayer != nil {
							a.livePreviewPlayer.SetMuted(muted)
						}
						ui.Text(c, "静音").FontSize(12).TextColor(ColorTextMuted).SingleLine()
						vol := a.PlayerOverlay.Volume
						if ui.Slider(c, &vol, 0, 1).Label("直播预览音量").Width(100).Changed() && a.livePreviewPlayer != nil {
							a.PlayerOverlay.Volume = vol
							a.livePreviewPlayer.SetVolume(vol)
							a.livePreviewPlayer.SetMuted(false)
						}
					})
				})
			}
		})
}

func (a *App) matchesLivePreviewKey(recKey string, roomID int64, uid string) bool {
	if a.LivePreviewKey == "" {
		return false
	}
	if a.LivePreviewKey == recKey {
		return true
	}
	if roomID > 0 {
		dec := strconv.FormatInt(roomID, 10)
		if a.LivePreviewKey == dec || a.LivePreviewKey == "bilibili:live:"+dec {
			return true
		}
	}
	if uid != "" && (a.LivePreviewKey == uid || a.LivePreviewKey == "douyin:"+uid || a.LivePreviewKey == "bilibili:"+uid) {
		return true
	}
	return false
}

func (a *App) lookupLiveRecordingPreview(recKey string) (service.LiveRecordingPreviewInfo, bool) {
	target := strings.TrimSpace(recKey)
	if target == "" {
		return service.LiveRecordingPreviewInfo{}, false
	}
	if a.backend != nil {
		if info, ok := a.backend.LiveRecordingPreview(target); ok {
			return info, true
		}
	}
	// Fallback to in-memory LiveRooms / LiveAccounts when tests or screenshot tools seed state directly.
	for _, r := range a.LiveRooms {
		biliKey := fmt.Sprintf("bilibili:live:%d", r.RoomID)
		roomDec := strconv.FormatInt(r.RoomID, 10)
		acctKey := fmt.Sprintf("bilibili:%d", r.UID)
		if target != biliKey && target != roomDec && target != acctKey {
			continue
		}
		partPath := r.RecordPartPath
		if partPath == "" && r.RecordItemDir != "" {
			partPath = newestPartFileInDir(r.RecordItemDir)
		}
		supported := r.PreviewSupported
		if !supported && !r.AudioOnly && r.PreviewReason == "" && r.Recording && partPath != "" {
			supported = true
		}
		return service.LiveRecordingPreviewInfo{
			RecordingKey:     biliKey,
			Site:             "bilibili",
			AccountUID:       strconv.FormatInt(r.UID, 10),
			RoomID:           roomDec,
			Uname:            r.Uname,
			Title:            r.Title,
			Codec:            r.Codec,
			Quality:          r.Quality,
			QualityName:      r.QualityName,
			Transport:        r.Transport,
			AudioOnly:        r.AudioOnly,
			Recording:        r.Recording,
			PartPath:         partPath,
			PartIndex:        max(1, r.RecordPartIndex),
			PartBytes:        r.RecordPartBytes,
			PartReady:        r.RecordPartReady,
			PreviewSupported: supported,
			PreviewReason:    r.PreviewReason,
			ItemDir:          r.RecordItemDir,
			DurationSec:      r.RecordDurationSec,
			SpeedBps:         r.RecordSpeedBps,
			BytesWritten:     r.RecordBytes,
			Segments:         r.RecordSegments,
			Incidents:        library.SanitizeLiveIncidents(r.Incidents),
		}, true
	}
	for _, st := range a.LiveAccounts {
		if st.RecordingKey != target && st.AccountKey != target && st.RoomID != target && st.UID != target && st.WebRID != target {
			continue
		}
		partPath := st.RecordPartPath
		if partPath == "" && st.RecordItemDir != "" {
			partPath = newestPartFileInDir(st.RecordItemDir)
		}
		supported := st.PreviewSupported
		if !supported && !st.AudioOnly && st.PreviewReason == "" && st.Recording && partPath != "" {
			supported = true
		}
		return service.LiveRecordingPreviewInfo{
			RecordingKey:     firstNonEmptyStrUI(st.RecordingKey, st.AccountKey),
			Site:             st.Site,
			AccountUID:       st.UID,
			RoomID:           st.RoomID,
			WebRID:           st.WebRID,
			Uname:            st.Uname,
			Title:            st.Title,
			Codec:            st.Codec,
			QualityName:      st.QualityName,
			Transport:        st.Transport,
			AudioOnly:        st.AudioOnly,
			Recording:        st.Recording,
			PartPath:         partPath,
			PartIndex:        max(1, st.RecordPartIndex),
			PartBytes:        st.RecordPartBytes,
			PartReady:        st.RecordPartReady,
			PreviewSupported: supported,
			PreviewReason:    st.PreviewReason,
			ItemDir:          st.RecordItemDir,
			DurationSec:      st.RecordDurationSec,
			SpeedBps:         st.RecordSpeedBps,
			BytesWritten:     st.RecordBytes,
			Segments:         st.RecordSegments,
			Incidents:        library.SanitizeLiveIncidents(st.Incidents),
		}, true
	}
	return service.LiveRecordingPreviewInfo{}, false
}

func newestPartFileInDir(dir string) string {
	entries, err := os.ReadDir(strings.TrimSpace(dir))
	if err != nil {
		return ""
	}
	var newest string
	var newestMod time.Time
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(strings.ToLower(e.Name()), ".part") {
			continue
		}
		info, iErr := e.Info()
		if iErr != nil {
			continue
		}
		full := filepath.Join(dir, e.Name())
		if newest == "" || info.ModTime().After(newestMod) {
			newest = full
			newestMod = info.ModTime()
		}
	}
	return newest
}

// StartLivePreview opens the local growing .part file of an active live recording
// for real-time in-page preview without opening any network connection (DEC-049 ①).
func (a *App) StartLivePreview(recKey string) {
	recKey = strings.TrimSpace(recKey)
	if recKey == "" {
		return
	}
	a.closeLivePreview()
	a.Navigate(RouteTasks)
	a.TasksSection = "live"

	info, ok := a.lookupLiveRecordingPreview(recKey)
	if !ok || !info.Recording {
		return
	}

	title := strings.TrimSpace(info.Uname)
	if info.Title != "" {
		if title != "" {
			title += " · " + info.Title
		} else {
			title = info.Title
		}
	}
	a.LivePreviewKey = recKey
	a.LivePreviewTitle = title
	a.LivePreviewPartPath = info.PartPath
	a.LivePreviewPartIndex = max(1, info.PartIndex)
	a.LivePreviewUnsupported = false
	a.LivePreviewReason = ""
	a.LivePreviewError = ""

	if info.AudioOnly || !info.PreviewSupported || strings.TrimSpace(info.PartPath) == "" {
		a.LivePreviewUnsupported = true
		a.LivePreviewReason = info.PreviewReason
		return
	}

	a.livePreviewOpenSeq++
	seq := a.livePreviewOpenSeq
	ctx, cancel := context.WithCancel(a.ctx)
	a.livePreviewCancel = cancel
	a.LivePreviewLoading = true

	partPath := info.PartPath
	backend := a.backend
	opener := a.livePreviewOpener
	if opener == nil {
		opener = player.OpenLivePreview
	}

	opts := player.LivePreviewOptions{
		IsActive: func() bool {
			if ctx.Err() != nil {
				return false
			}
			if backend != nil {
				if cur, exists := backend.LiveRecordingPreview(recKey); exists {
					return cur.Recording && (cur.PartPath == "" || cur.PartPath == partPath)
				}
			}
			return true
		},
	}

	stale := func() bool {
		return a.livePreviewOpenSeq != seq ||
			a.LivePreviewKey != recKey ||
			a.CurrentRoute != RouteTasks ||
			a.effectiveTasksSection() != "live"
	}

	requestGuarded(a, fmt.Sprintf("live-preview-open-%d", seq), nil, func(reqCtx context.Context) (player.MediaDecoder, error) {
		return opener(ctx, partPath, opts)
	}, func(dec player.MediaDecoder) {
		if stale() {
			_ = dec.Close()
			return
		}
		a.LivePreviewLoading = false
		if a.livePreviewPlayer == nil {
			_ = dec.Close()
			return
		}
		if err := a.livePreviewPlayer.LoadLiveDecoder(partPath, dec); err != nil {
			if errors.Is(err, player.ErrLivePreviewUnsupported) {
				a.LivePreviewUnsupported = true
				a.LivePreviewReason = err.Error()
			} else {
				a.LivePreviewError = "无法播放实时预览：" + err.Error()
			}
			return
		}
		a.livePreviewPlayer.SetVolume(a.PlayerOverlay.Volume)
		if a.backend != nil {
			a.livePreviewPlayer.SetMuted(a.backend.GetUIState().Player.Muted)
		}
		a.livePreviewPlayer.Play()
	}, func(err error) {
		if stale() {
			return
		}
		a.LivePreviewLoading = false
		if errors.Is(err, player.ErrLivePreviewUnsupported) {
			a.LivePreviewUnsupported = true
			a.LivePreviewReason = err.Error()
			return
		}
		a.LivePreviewError = "实时预览暂时无法打开：" + userErrorText(err)
	})
}

// OpenLivePreviewWithDecoder loads a pre-opened live decoder synchronously (used by headless screenshot assertions).
func (a *App) OpenLivePreviewWithDecoder(recKey, title, partPath string, partIndex int, dec player.MediaDecoder) error {
	a.closeLivePreview()
	a.CurrentRoute = RouteTasks
	a.TasksSection = "live"
	a.LivePreviewKey = recKey
	a.LivePreviewTitle = title
	a.LivePreviewPartPath = partPath
	a.LivePreviewPartIndex = max(1, partIndex)
	if a.livePreviewPlayer == nil {
		if dec != nil {
			_ = dec.Close()
		}
		return errors.New("live preview player not initialized")
	}
	if err := a.livePreviewPlayer.LoadLiveDecoder(partPath, dec); err != nil {
		return err
	}
	a.livePreviewPlayer.Play()
	a.livePreviewPlayer.Advance(33 * time.Millisecond)
	return nil
}

func (a *App) closeLivePreview() {
	a.livePreviewOpenSeq++
	if a.livePreviewCancel != nil {
		a.livePreviewCancel()
		a.livePreviewCancel = nil
	}
	if a.livePreviewPlayer != nil {
		_ = a.livePreviewPlayer.Close()
	}
	a.LivePreviewKey = ""
	a.LivePreviewTitle = ""
	a.LivePreviewPartPath = ""
	a.LivePreviewPartIndex = 0
	a.LivePreviewLoading = false
	a.LivePreviewUnsupported = false
	a.LivePreviewReason = ""
	a.LivePreviewError = ""
}

func (a *App) closeLivePreviewForRoom(roomID int64) {
	if a.matchesLivePreviewKey(fmt.Sprintf("bilibili:live:%d", roomID), roomID, "") {
		a.closeLivePreview()
	}
}

func (a *App) closeLivePreviewForKey(recKey string) {
	if a.LivePreviewKey == "" {
		return
	}
	if a.LivePreviewKey == recKey {
		a.closeLivePreview()
		return
	}
	for _, acct := range a.LiveAccounts {
		if acct.RecordingKey == recKey || acct.AccountKey == recKey || acct.RoomID == recKey || acct.UID == recKey {
			if a.matchesLivePreviewKey(firstNonEmptyStrUI(acct.RecordingKey, acct.AccountKey), 0, acct.UID) {
				a.closeLivePreview()
				return
			}
		}
	}
}

// syncLivePreviewState closes the preview if its recording stopped, or follows a newly rotated .part segment.
func (a *App) syncLivePreviewState() {
	if a.LivePreviewKey == "" {
		return
	}
	if a.CurrentRoute != RouteTasks || a.effectiveTasksSection() != "live" {
		a.closeLivePreview()
		return
	}
	info, ok := a.lookupLiveRecordingPreview(a.LivePreviewKey)
	if !ok || !info.Recording {
		a.closeLivePreview()
		return
	}
	if info.PartIndex > 0 {
		a.LivePreviewPartIndex = info.PartIndex
	}
	if !a.LivePreviewUnsupported && info.PartPath != "" && a.LivePreviewPartPath != "" && info.PartPath != a.LivePreviewPartPath {
		key := a.LivePreviewKey
		a.StartLivePreview(key)
	}
}
