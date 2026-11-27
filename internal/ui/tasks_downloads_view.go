package ui

import (
	"context"
	"fmt"
	"strings"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/egoist/mygo/ui"
)

func (a *App) lookupTaskDetailCached(task download.Task) (download.TaskDetail, bool) {
	if a.backend == nil || task.TaskID == "" {
		return download.TaskDetail{}, false
	}
	st := a.fs()
	revMatch := task.UpdatedAt != "" || st.taskDetailRev == a.cacheStateRev
	if st.taskDetailID == task.TaskID &&
		revMatch &&
		st.taskDetailUpdatedAt == task.UpdatedAt &&
		st.taskDetailStatus == task.Status &&
		st.taskDetailBytes == task.BytesDownloaded &&
		st.taskDetailFileN == len(task.Files) {
		return st.taskDetail, st.taskDetailOK
	}
	detail, ok, _ := a.backend.TaskDetail(task.TaskID)
	st.taskDetailID = task.TaskID
	st.taskDetailRev = a.cacheStateRev
	st.taskDetailUpdatedAt = task.UpdatedAt
	st.taskDetailStatus = task.Status
	st.taskDetailBytes = task.BytesDownloaded
	st.taskDetailFileN = len(task.Files)
	st.taskDetail = detail
	st.taskDetailOK = ok
	return detail, ok
}

func (a *App) startUpgradeCandidate(it library.ItemMetadata) {
	if a.backend == nil {
		return
	}
	reqKey := "upgrade-cand:" + firstNonEmptyStrUI(it.ItemKey, it.RawID)
	if a.isPending(reqKey) {
		return
	}
	backend := a.backend
	itemKey := it.ItemKey
	rawID := it.RawID
	requestGuarded(a, reqKey, nil, func(ctx context.Context) (struct{}, error) {
		var err error
		if itemKey != "" {
			_, err = backend.Upgrade(ctx, itemKey)
		} else {
			_, err = backend.EnqueueVideoCache(ctx, download.VideoEnqueueRequest{
				BVID: rawID,
			})
		}
		return struct{}{}, err
	}, func(struct{}) {
		a.TaskActionError = ""
		filtered := a.UpgradeCandidates[:0]
		for _, c := range a.UpgradeCandidates {
			if (itemKey != "" && c.ItemKey == itemKey) || (itemKey == "" && c.RawID == rawID) {
				continue
			}
			filtered = append(filtered, c)
		}
		a.UpgradeCandidates = filtered
		a.RefreshData()
	}, func(err error) {
		a.finishTaskAction(err)
		a.showError(err)
	})
}

func (a *App) retryAllTasksAction() {
	a.TaskRemoveConfirmID = ""
	res, err := a.backend.RetryAllTasks()
	a.finishTaskAction(err)
	if err != nil {
		a.clearInfoNotifications()
	} else {
		a.notifyInfo(formatBatchRetryInfo(res))
	}
}

func (a *App) renderTasksDownloadsView(c *ui.Context) {
	ui.Column(c).Grow(1).FillWidth().ClipX().Children(func() {
		retryable := 0
		finished := 0
		for _, t := range a.Tasks {
			if download.IsRetryableTask(t) {
				retryable++
			}
			if t.Status == "completed" || t.Status == "canceled" {
				finished++
			}
		}
		hasToolbar := len(a.UpgradeCandidates) > 0 || a.TaskActionError != "" || retryable > 0 || finished > 0
		if hasToolbar {
			ui.Column(c).FillWidth().Padding(16, 16, 8, 16).Gap(10).Shrink(0).ClipX().Children(func() {
				if len(a.UpgradeCandidates) > 0 {
					ui.Scroll(c).FillWidth().MaxHeight(220).ClipX().Children(func() {
						ui.Column(c).FillWidth().Padding(14).Radius(16).Background(ColorInfoBlueBg).Border(1, ColorInfoBlue).Gap(8).ClipX().Children(func() {
							ui.Text(c, fmt.Sprintf("可升级画质视频（%d）", len(a.UpgradeCandidates))).FontSize(14).FontWeight(600).TextColor(ColorInfoBlue).SingleLine()
							for _, cand := range a.UpgradeCandidates {
								it := cand
								reqKey := "upgrade-cand:" + firstNonEmptyStrUI(it.ItemKey, it.RawID)
								busy := a.isPending(reqKey)
								ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
									ui.Text(c, fmt.Sprintf("%s（%s）", it.Title, it.UpgradeHint)).
										FontSize(13).TextColor(ColorTextMain).SingleLine().Grow(1).Shrink(1)
									btnText := "升级缓存"
									if busy {
										btnText = "提交中…"
									}
									if AccentPillButton(c, btnText, "升级缓存："+it.RawID).Disabled(busy).Clicked() && !busy {
										a.startUpgradeCandidate(it)
									}
								})
							}
						})
					})
				}

				if a.TaskActionError != "" {
					renderErrorBanner(c, "", a.TaskActionError, nil)
				}
				if retryable > 0 || finished > 0 {
					ui.Row(c).FillWidth().Justify(ui.End).AlignItems(ui.Center).Gap(8).Wrap().Children(func() {
						if retryable > 0 {
							if AccentPillButton(c, fmt.Sprintf("批量重试（%d）", retryable), "批量重试失败或已暂停的任务").Clicked() {
								a.retryAllTasksAction()
							}
						}
						if finished > 0 {
							if OutlinePillButton(c, fmt.Sprintf("清除已结束（%d）", finished), "清除已结束的任务").Clicked() {
								a.TaskRemoveConfirmID = ""
								_, err := a.backend.RemoveFinishedTasks()
								a.finishTaskAction(err)
							}
						}
					})
				}
			})
		}

		if len(a.Tasks) == 0 {
			ui.Column(c).FillWidth().Padding(40, 16).AlignItems(ui.Center).Gap(8).Children(func() {
				ui.Text(c, "当前没有缓存任务").FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
				ui.Text(c, "在动态、帖子或视频详情上点「缓存」后，下载进度与子文件详情会显示在这里。").FontSize(13).TextColor(ColorTextMuted)
				if len(a.CacheQueue) > 0 {
					if OutlinePillButton(c, fmt.Sprintf("查看缓存队列（%d）", len(a.CacheQueue)), "下载空态跳转缓存队列").Clicked() {
						a.selectTasksQueue("")
					}
				}
			})
			return
		}

		tasks := a.Tasks
		queuedOrderPos := make(map[string]int)
		queuedTotal := 0
		for _, tk := range tasks {
			if tk.Status == "queued" {
				queuedOrderPos[tk.TaskID] = queuedTotal
				queuedTotal++
			}
		}
		a.TasksListState.Key = func(i int) any {
			if i >= 0 && i < len(tasks) {
				return tasks[i].TaskID
			}
			return i
		}
		topPad := float32(16)
		if hasToolbar {
			topPad = 4
		}
		ui.List(c, &a.TasksListState, len(tasks), func(i int) {
			if i < 0 || i >= len(tasks) {
				return
			}
			task := tasks[i]
			padTop := float32(0)
			if i == 0 {
				padTop = topPad
			}
			padBottom := float32(12)
			if i == len(tasks)-1 {
				padBottom = 16
			}
			qPos := -1
			if p, ok := queuedOrderPos[task.TaskID]; ok {
				qPos = p
			}
			ui.Column(c).FillWidth().Padding(padTop, 16, padBottom, 16).ClipX().Children(func() {
				a.renderDownloadTaskCardOrdered(c, task, qPos, queuedTotal)
			})
		}).Label("下载任务列表").Grow(1).FillWidth()
	})
}

func formatBatchRetrySites(sites []string) string {
	var labels []string
	seen := make(map[string]bool, len(sites))
	for _, s := range sites {
		label := siteFullLabel(s)
		if label == "" || seen[label] {
			continue
		}
		seen[label] = true
		labels = append(labels, label)
	}
	if len(labels) == 0 {
		return "对应站点"
	}
	return strings.Join(labels, "、")
}

func formatBatchRetryInfo(res download.BatchRetryResult) string {
	switch {
	case res.Retried > 0 && res.SkippedAuth > 0:
		return fmt.Sprintf("已重新排队 %d 个任务；另有 %d 个任务需先登录%s", res.Retried, res.SkippedAuth, formatBatchRetrySites(res.MissingAuthSites))
	case res.Retried > 0 && res.SkippedDuplicate > 0:
		return fmt.Sprintf("已重新排队 %d 个任务（跳过 %d 个已在队列中的重复任务）", res.Retried, res.SkippedDuplicate)
	case res.Retried > 0:
		return fmt.Sprintf("已重新排队 %d 个任务", res.Retried)
	case res.Retried == 0 && res.SkippedDuplicate > 0:
		return fmt.Sprintf("对应作品已在队列中（跳过 %d 个重复任务）", res.SkippedDuplicate)
	default:
		return ""
	}
}

func (a *App) renderDownloadTaskCard(c *ui.Context, task download.Task) {
	a.renderDownloadTaskCardOrdered(c, task, -1, 0)
}

func (a *App) renderDownloadTaskCardOrdered(c *ui.Context, task download.Task, queuedPos, queuedTotal int) {
	taskSite := normalizeSiteIDUI(task.Site)
	statusLabel := taskStatusText(task.Status)
	isPaused := task.Status == "paused" || strings.HasPrefix(task.Status, "paused_")
	barCol := ColorInfoBlue
	switch {
	case task.Status == "completed":
		barCol = ColorDotGreen
	case isPaused || task.Status == "partial":
		barCol = ColorDotYellow
	case task.Status == "failed":
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
				coverKey, coverRel := a.queueRecentCover(task.ItemKey)
				a.renderMediaThumbEdge(c, coverKey, coverRel, avatarThumbEdge, task.Title, "", task.Status, false, 96, 58)

				ui.Column(c).Grow(1).Shrink(1).Gap(6).ClipX().Children(func() {
					ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
						ui.Row(c).AlignItems(ui.Center).Gap(6).Grow(1).Shrink(1).ClipX().Children(func() {
							DrawStatusDotFull(c, task.Status, task.Progress, isPaused, "任务状态："+statusLabel)
							ui.Text(c, fmt.Sprintf("%s (%s)", task.Title, task.TargetID)).
								FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine().Shrink(1)
							renderSiteSmallBadgeWithDot(c, taskSite, "任务站点："+task.TaskID)
							if task.Source == "auto" {
								ui.Box(c).Shrink(0).Padding(1, 6).Radius(4).Background(ColorBgMain).Border(1, ColorBorder).Children(func() {
									ui.Text(c, "自动").FontSize(11).FontWeight(600).TextColor(ColorTextMuted).SingleLine()
								})
							}
							if task.Status == "queued" && queuedPos >= 0 && queuedTotal >= 2 {
								ui.Box(c).Shrink(0).Padding(1, 6).Radius(4).Background(ColorBgSecondary).Border(1, ColorBorder).Children(func() {
									ui.Text(c, fmt.Sprintf("排队第 %d 位", queuedPos+1)).FontSize(11).FontWeight(500).TextColor(ColorTextMuted).SingleLine()
								})
							}
						})
						ui.Text(c, fmt.Sprintf("%s · %.0f%% (%s / %s)",
							statusLabel,
							task.Progress*100,
							FormatBytes(task.BytesDownloaded),
							FormatBytes(task.TotalBytes))).
							FontSize(12).FontWeight(600).TextColor(ColorTextMuted).SingleLine().Shrink(0)
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
					if lastErr == "" && isPaused && strings.TrimSpace(task.PauseReason) != "" && strings.TrimSpace(task.PauseReason) != "user" {
						lastErr = taskPauseReasonText(task.PauseReason)
					}
					if lastErr != "" {
						bannerBg := ColorErrorBg
						bannerIconCol := ColorError
						bannerTextCol := ColorError
						if task.Status == "partial" || isPaused {
							bannerBg = ColorWarnBg
							bannerIconCol = ColorDotYellow
							bannerTextCol = ColorTextMain
						}
						ui.Row(c).FillWidth().Padding(6, 10).Radius(8).Background(bannerBg).AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
							DrawIcon(c, "alert-circle", 14, bannerIconCol, 1.8)
							errLine := "原因：" + lastErr
							if task.ErrorKind != "" {
								errLine = fmt.Sprintf("原因 [%s]：%s", task.ErrorKind, lastErr)
							}
							ui.Text(c, errLine).
								FontSize(12).TextColor(bannerTextCol).MaxLines(2).Grow(1).Shrink(1)
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
								if a.TaskRemoveConfirmID == task.TaskID {
									a.TaskRemoveConfirmID = ""
								}
								_, err := a.backend.PauseTask(task.TaskID)
								a.finishTaskAction(err)
							}
						}
						if isPaused {
							if AccentPillButton(c, "继续", "继续任务："+task.TaskID).Clicked() {
								if a.TaskRemoveConfirmID == task.TaskID {
									a.TaskRemoveConfirmID = ""
								}
								_, err := a.backend.ResumeTask(task.TaskID)
								a.finishTaskAction(err)
							}
						}
						if task.Status == "failed" || task.Status == "partial" {
							if AccentPillButton(c, "重试", "重试任务："+task.TaskID).Clicked() {
								if a.TaskRemoveConfirmID == task.TaskID {
									a.TaskRemoveConfirmID = ""
								}
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
								if a.TaskRemoveConfirmID == task.TaskID {
									a.TaskRemoveConfirmID = ""
								}
								a.finishTaskAction(a.backend.RemoveTask(task.TaskID))
							}
						default:
							if DangerOutlinePillButton(c, "取消", "取消任务："+task.TaskID).Clicked() {
								if a.TaskRemoveConfirmID == task.TaskID {
									a.TaskRemoveConfirmID = ""
								}
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
						if task.Status == "queued" && queuedPos >= 0 && queuedTotal >= 2 {
							if compactOrderButton(c, "置顶", "置顶下载任务："+task.TaskID, queuedPos == 0) {
								_, err := a.backend.MoveDownloadTask(task.TaskID, service.QueueMoveTop)
								a.finishTaskAction(err)
							}
							if compactOrderButton(c, "上移", "上移下载任务："+task.TaskID, queuedPos == 0) {
								_, err := a.backend.MoveDownloadTask(task.TaskID, service.QueueMoveUp)
								a.finishTaskAction(err)
							}
							if compactOrderButton(c, "下移", "下移下载任务："+task.TaskID, queuedPos >= queuedTotal-1) {
								_, err := a.backend.MoveDownloadTask(task.TaskID, service.QueueMoveDown)
								a.finishTaskAction(err)
							}
						}
					})
				})
			})

			if isExpanded {
				a.renderDownloadTaskDetail(c, task)
			}
		})
}

func (a *App) renderDownloadTaskDetail(c *ui.Context, task download.Task) {
	detail, ok := a.lookupTaskDetailCached(task)
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
				emptyMsg := "正在准备文件清单…"
				if task.Status == "completed" || task.Status == "canceled" || task.Status == "failed" {
					emptyMsg = "暂无逐文件明细"
				}
				ui.Text(c, emptyMsg).FontSize(12).TextColor(ColorTextMuted).SingleLine()
			}
			for _, f := range files {
				fl := f
				ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
					ui.Text(c, fmt.Sprintf("[%s] %s", taskFileRoleLabel(fl.Role), fl.Path)).
						FontSize(12).FontWeight(600).TextColor(ColorTextMain).SingleLine().Grow(1).Shrink(1)
					ui.Text(c, fmt.Sprintf("%s · %s / %s",
						taskFileStatusLabel(fl.Status),
						FormatBytes(fl.DownloadedBytes),
						FormatBytes(fl.TotalBytes))).FontSize(12).TextColor(ColorTextMuted).SingleLine().Shrink(0)
				})
			}
			if len(attempts) > 0 {
				ui.Text(c, fmt.Sprintf("最近分段重试记录（已脱敏，共 %d 条）", len(attempts))).
					FontSize(12).FontWeight(600).TextColor(ColorTextMain).SingleLine()
				limit := min(len(attempts), 5)
				for i := 0; i < limit; i++ {
					att := attempts[i]
					errKind := firstNonEmptyStrUI(downloadErrorKindLabel(att.ErrorKind), "异常")
					ui.Text(c, fmt.Sprintf("%s · 文件 %s · 节点 %s · 偏移 %s · %s (第 %d 次)",
						att.Time, att.File, att.Host, FormatBytes(att.Offset), errKind, att.Attempt)).
						FontSize(11).TextColor(ColorTextMuted).SingleLine()
				}
			}
		})
}
