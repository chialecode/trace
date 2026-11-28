package tasks

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/chialecode/trace/cmd/trace-mobile/internal/pagekit"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/theme"
	"github.com/chialecode/trace/internal/companionapi"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/mobile"
)

func (p *Page) renderActiveSegment(c *ui.Context, h pagekit.Host) {
	if p.downloadsLoading && !p.downloadsLoaded && p.queuesLoading && !p.queuesLoaded {
		p.renderLoadingSkeleton(c)
		return
	}
	if p.downloadsErr != "" && len(p.downloads) == 0 && len(p.queues) == 0 {
		p.renderLoadError(c, p.downloadsErr, func() {
			p.loadDownloads(h)
			p.loadQueues(h)
		})
		return
	}

	sc := ui.Scroll(c).Grow(1).Basis(0).FillWidth().Background(theme.ColorBgPanel).ClipX()
	if sc.PullToRefresh(&p.activeRefresh) {
		p.loadDownloads(h)
		p.loadQueues(h)
	}
	sc.Children(func() {
		ui.Column(c).FillWidth().Padding(12, 14, 28, 14).Gap(16).ClipX().Children(func() {
			// Section 1: 下载任务
			p.renderDownloadsSection(c, h)

			// Section 2: 缓存队列
			p.renderQueuesSection(c, h)
		})
	})
}

func (p *Page) renderDownloadsSection(c *ui.Context, h pagekit.Host) {
	finished := 0
	for _, t := range p.downloads {
		if t.Status == "completed" || t.Status == "canceled" {
			finished++
		}
	}

	ui.Column(c).FillWidth().Gap(8).ClipX().Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
			ui.Text(c, fmt.Sprintf("下载任务（%d）", len(p.downloads))).
				FontSize(14).FontWeight(600).TextColor(theme.ColorTextMain).SingleLine()
			if finished > 0 {
				btn := ui.Button(c, "").
					Label("清除已结束的下载任务").
					Radius(999).
					Padding(6, 12).
					MinHeight(36).
					Background(theme.ColorBgMain).
					Border(1, theme.ColorBorderStrong).
					Disabled(p.busyActionKey != "").
					Shrink(0).
					Center()
				btn.Children(func() {
					ui.Text(c, fmt.Sprintf("清除已结束（%d）", finished)).FontSize(12).FontWeight(600).TextColor(theme.ColorTextMain).SingleLine()
				})
				if btn.Clicked() && p.busyActionKey == "" {
					p.removeConfirmID = ""
					p.clearFinishedDownloads(h)
				}
			}
		})

		if p.downloadsErr != "" {
			ui.Text(c, p.downloadsErr).FontSize(12).TextColor(theme.ColorError).MaxLines(2)
		}

		if len(p.downloads) == 0 {
			ui.Column(c).FillWidth().Padding(20, 14).Radius(12).Background(theme.ColorBgMain).Border(1, theme.ColorBorder).AlignItems(ui.Center).Gap(4).Children(func() {
				ui.Text(c, "当前没有下载任务").FontSize(14).FontWeight(600).TextColor(theme.ColorTextMain)
				ui.Text(c, "电脑端开始缓存后，下载进度与逐文件详情会显示在这里。").
					FontSize(12).TextColor(theme.ColorTextMuted).TextAlign(ui.Center)
			})
			return
		}

		for _, t := range p.downloads {
			p.renderDownloadTaskCard(c, h, t)
		}
	})
}

func (p *Page) downloadSwipeActions(h pagekit.Host, task companionapi.DownloadTaskDTO) []mobile.SwipeAction {
	allowed := AllowedDownloadActions(task.Status)
	actions := make([]mobile.SwipeAction, 0, len(allowed))
	for _, act := range allowed {
		switch act {
		case "pause":
			actions = append(actions, mobile.SwipeAction{
				Label: "暂停",
				OnTap: func() { p.runDownloadAction(h, task, "pause") },
			})
		case "resume":
			actions = append(actions, mobile.SwipeAction{
				Label: "继续",
				OnTap: func() { p.runDownloadAction(h, task, "resume") },
			})
		case "cancel":
			actions = append(actions, mobile.SwipeAction{
				Label:       "取消",
				Destructive: true,
				OnTap:       func() { p.runDownloadAction(h, task, "cancel") },
			})
		case "retry":
			actions = append(actions, mobile.SwipeAction{
				Label: "重试",
				OnTap: func() { p.runDownloadAction(h, task, "retry") },
			})
		case "remove":
			if task.Status == "failed" || task.Status == "partial" {
				actions = append(actions, mobile.SwipeAction{
					Label:       "移除",
					Destructive: true,
					OnTap:       func() { p.removeConfirmID = task.TaskID },
				})
			} else {
				actions = append(actions, mobile.SwipeAction{
					Label:       "移除",
					Destructive: true,
					OnTap:       func() { p.runDownloadAction(h, task, "remove") },
				})
			}
		}
	}
	return actions
}

func (p *Page) renderDownloadTaskCard(c *ui.Context, h pagekit.Host, task companionapi.DownloadTaskDTO) {
	isPaused := isPausedDownloadStatus(task.Status)
	statusText := downloadStatusLabel(task.Status)
	barCol := theme.ColorAccent
	switch {
	case task.Status == "completed":
		barCol = theme.ColorDotGreen
	case isPaused || task.Status == "partial":
		barCol = theme.ColorDotYellow
	case task.Status == "failed":
		barCol = theme.ColorDotRed
	case task.Status == "canceled":
		barCol = theme.ColorTextFaint
	}

	tCopy := task
	mobile.SwipeRow(c.Key("dl-swipe-"+task.TaskID), p.swipeStateFor("dl:"+task.TaskID), mobile.SwipeOptions{
		Trailing: p.downloadSwipeActions(h, tCopy),
	}, func() {
		ui.Column(c).
			Label("下载任务卡片："+task.TaskID).
			FillWidth().
			Padding(12, 14).
			Radius(12).
			Background(theme.ColorBgMain).
			Border(1, theme.ColorBorder).
			Gap(8).
			ClipX().
			Children(func() {
				// Main clickable body -> opens /download/{id} per-file progress detail.
				ui.Row(c).FillWidth().AlignItems(ui.Start).Gap(10).ClipX().Children(func() {
					bodyBtn := ui.ButtonBase(c).
						Label("查看下载详情：" + firstNonEmpty(task.Title, task.TargetID)).
						Grow(1).
						Shrink(1).
						ClipX()
					bodyBtn.Children(func() {
						ui.Column(c).FillWidth().Gap(6).ClipX().Children(func() {
							ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
								title := firstNonEmpty(task.Title, task.TargetID)
								if task.PartTitle != "" {
									title += " · " + task.PartTitle
								}
								ui.Text(c, title).FontSize(14).FontWeight(600).TextColor(theme.ColorTextMain).SingleLine().Shrink(1)
								renderSiteTag(c, task.Site)
								if task.Source == "auto" {
									ui.Box(c).Padding(1, 5).Radius(4).Background(theme.ColorBgSecondary).Border(1, theme.ColorBorder).Shrink(0).Children(func() {
										ui.Text(c, "自动").FontSize(10).FontWeight(600).TextColor(theme.ColorTextMuted).SingleLine()
									})
								}
							})

							// Status, percent, bytes, speed.
							var meta []string
							meta = append(meta, statusText)
							if task.TotalBytes > 0 {
								meta = append(meta, fmt.Sprintf("%.0f%% (%s / %s)", task.Progress*100, formatBytes(task.BytesDownloaded), formatBytes(task.TotalBytes)))
							} else if task.BytesDownloaded > 0 {
								meta = append(meta, formatBytes(task.BytesDownloaded))
							}
							if task.Status == "running" && task.SpeedBps > 0 {
								meta = append(meta, formatSpeed(task.SpeedBps))
							}
							ui.Text(c, strings.Join(meta, " · ")).FontSize(12).TextColor(theme.ColorTextMuted).SingleLine()

							// Progress bar.
							ui.Box(c).FillWidth().Height(6).Radius(3).Background(theme.ColorBorder).Clip().Draw(func(painter *ui.Painter, r ui.Rect) {
								frac := float32(max(0.0, min(1.0, task.Progress)))
								if frac > 0 {
									painter.Fill(ui.Rect{X: r.X, Y: r.Y, W: r.W * frac, H: r.H}, barCol, 3)
								}
							})

							if task.QualityHint != "" {
								ui.Text(c, "画质与音轨："+task.QualityHint).FontSize(11).TextColor(theme.ColorAccent).SingleLine()
							}
						})
					})
					if bodyBtn.Clicked() {
						p.openDownloadDetail(h, task.TaskID)
						h.PushRoute("/download/" + url.PathEscape(task.TaskID))
					}

					// Trailing "⋯" button opens ActionSheet with strictly state-appropriate actions.
					moreBtn := ui.Button(c, "").
						Label("下载任务更多操作："+task.TaskID).
						Radius(999).
						Size(40, 40).
						Background(theme.ColorBgMain).
						Border(1, theme.ColorBorderStrong).
						Shrink(0).
						Center()
					moreBtn.Children(func() {
						ui.Text(c, "⋯").FontSize(16).FontWeight(700).TextColor(theme.ColorTextMain).SingleLine()
					})
					if moreBtn.Clicked() {
						cp := task
						p.downloadSheetTask = &cp
						p.downloadSheetOpen = true
					}
				})

				// Failure or non-user pause reason banner.
				reason := strings.TrimSpace(task.ErrorMessage)
				if reason == "" && isPaused {
					reason = pauseReasonLabel(task.PauseReason)
				}
				if reason != "" {
					bg := theme.ColorErrorBg
					fg := theme.ColorError
					if task.Status == "partial" || isPaused {
						bg = theme.ColorWarnBg
						fg = theme.ColorWarnText
					}
					ui.Row(c).FillWidth().Padding(6, 10).Radius(8).Background(bg).AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
						theme.DrawIcon(c, "alert-circle", 14, fg, 1.8)
						line := "原因：" + reason
						if task.ErrorKind != "" {
							line = fmt.Sprintf("原因 [%s]：%s", task.ErrorKind, reason)
						}
						ui.Text(c, line).FontSize(12).LineHeight(theme.LineHeightCompact).TextColor(fg).MaxLines(2).Grow(1).Shrink(1)
					})
				}

				// Inline remove confirmation for failed / partial download tasks (same rule as desktop).
				if p.removeConfirmID == task.TaskID {
					p.renderRemoveConfirmBanner(c, h, task, "")
				}
			})
	})
}

func (p *Page) renderRemoveConfirmBanner(c *ui.Context, h pagekit.Host, task companionapi.DownloadTaskDTO, prefix string) {
	ui.Column(c).FillWidth().Padding(10, 12).Radius(10).Background(theme.ColorWarnBg).Border(1, theme.ColorDotYellow).Gap(8).ClipX().Children(func() {
		ui.Text(c, "确认移除该任务？会删除未完成的临时文件，已缓存完成的文件会保留。").
			FontSize(12).FontWeight(600).LineHeight(theme.LineHeightCompact).TextColor(theme.ColorTextMain)
		ui.Row(c).FillWidth().Justify(ui.End).Gap(8).Children(func() {
			cancelBtn := ui.Button(c, "").Label(prefix+"取消移除任务").Radius(999).Padding(6, 14).MinHeight(36).Background(theme.ColorBgMain).Border(1, theme.ColorBorderStrong).Center()
			cancelBtn.Children(func() {
				ui.Text(c, "取消").FontSize(12).FontWeight(600).TextColor(theme.ColorTextMain).SingleLine()
			})
			if cancelBtn.Clicked() {
				p.removeConfirmID = ""
			}
			confirmBtn := ui.Button(c, "").Label(prefix+"确认移除任务："+task.TaskID).Radius(999).Padding(6, 14).MinHeight(36).Background(theme.ColorBgMain).Border(1, ui.RGBA(196, 24, 46, 0.35)).Center()
			confirmBtn.Children(func() {
				ui.Text(c, "确认移除").FontSize(12).FontWeight(600).TextColor(theme.ColorError).SingleLine()
			})
			if confirmBtn.Clicked() {
				p.removeConfirmID = ""
				p.runDownloadAction(h, task, "remove")
			}
		})
	})
}

func (p *Page) renderDownloadActionSheet(c *ui.Context, h pagekit.Host) {
	if p.downloadSheetTask == nil {
		p.downloadSheetOpen = false
		return
	}
	task := *p.downloadSheetTask
	allowed := AllowedDownloadActions(task.Status)
	actions := make([]mobile.Action, 0, len(allowed)+1)
	for _, act := range allowed {
		switch act {
		case "pause":
			actions = append(actions, mobile.Action{
				Label: "暂停",
				OnTap: func() { p.runDownloadAction(h, task, "pause") },
			})
		case "resume":
			actions = append(actions, mobile.Action{
				Label: "继续",
				OnTap: func() { p.runDownloadAction(h, task, "resume") },
			})
		case "cancel":
			actions = append(actions, mobile.Action{
				Label:       "取消",
				Destructive: true,
				OnTap:       func() { p.runDownloadAction(h, task, "cancel") },
			})
		case "retry":
			actions = append(actions, mobile.Action{
				Label: "重试",
				OnTap: func() { p.runDownloadAction(h, task, "retry") },
			})
		case "remove":
			if task.Status == "failed" || task.Status == "partial" {
				actions = append(actions, mobile.Action{
					Label:       "移除",
					Destructive: true,
					OnTap:       func() { p.removeConfirmID = task.TaskID },
				})
			} else {
				actions = append(actions, mobile.Action{
					Label:       "移除",
					Destructive: true,
					OnTap:       func() { p.runDownloadAction(h, task, "remove") },
				})
			}
		}
	}
	actions = append(actions, mobile.Action{
		Label: "查看逐文件详情",
		OnTap: func() {
			p.openDownloadDetail(h, task.TaskID)
			h.PushRoute("/download/" + url.PathEscape(task.TaskID))
		},
	})

	mobile.ActionSheet(c, &p.downloadSheetOpen, mobile.ActionSheetOptions{
		Title:   firstNonEmpty(task.Title, task.TargetID),
		Message: downloadStatusLabel(task.Status),
		Cancel:  "关闭",
	}, actions...)
}

func (p *Page) renderQueuesSection(c *ui.Context, h pagekit.Host) {
	ui.Column(c).FillWidth().Gap(8).ClipX().Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
			ui.Text(c, fmt.Sprintf("缓存队列（%d）", len(p.queues))).
				FontSize(14).FontWeight(600).TextColor(theme.ColorTextMain).SingleLine()
		})
		ui.Text(c, "取消或移除队列不会删除已缓存到媒体库的内容").FontSize(12).TextColor(theme.ColorTextMuted)

		if p.queuesErr != "" {
			ui.Text(c, p.queuesErr).FontSize(12).TextColor(theme.ColorError).MaxLines(2)
		}

		if len(p.queues) == 0 {
			ui.Column(c).FillWidth().Padding(20, 14).Radius(12).Background(theme.ColorBgMain).Border(1, theme.ColorBorder).AlignItems(ui.Center).Gap(4).Children(func() {
				ui.Text(c, "当前没有缓存队列").FontSize(14).FontWeight(600).TextColor(theme.ColorTextMain)
				ui.Text(c, "电脑端开启「全部缓存」后，队列进度与最近缓存内容会显示在这里。").
					FontSize(12).TextColor(theme.ColorTextMuted).TextAlign(ui.Center)
			})
			return
		}

		for _, q := range p.queues {
			p.renderCacheQueueCard(c, h, q)
		}
	})
}

func (p *Page) queueSwipeActions(h pagekit.Host, q companionapi.CacheQueueDTO) []mobile.SwipeAction {
	allowed := AllowedQueueActions(q.Status)
	acts := make([]mobile.SwipeAction, 0, len(allowed))
	for _, act := range allowed {
		switch act {
		case "pause":
			acts = append(acts, mobile.SwipeAction{
				Label: "暂停",
				OnTap: func() { p.runQueueAction(h, q, "pause") },
			})
		case "resume":
			acts = append(acts, mobile.SwipeAction{
				Label: "继续",
				OnTap: func() { p.runQueueAction(h, q, "resume") },
			})
		case "cancel":
			acts = append(acts, mobile.SwipeAction{
				Label:       "取消",
				Destructive: true,
				OnTap:       func() { p.runQueueAction(h, q, "cancel") },
			})
		case "remove":
			acts = append(acts, mobile.SwipeAction{
				Label:       "移除",
				Destructive: true,
				OnTap:       func() { p.runQueueAction(h, q, "remove") },
			})
		}
	}
	return acts
}

func (p *Page) renderCacheQueueCard(c *ui.Context, h pagekit.Host, q companionapi.CacheQueueDTO) {
	expanded := p.expandedQueues[q.ID]
	qCopy := q
	title := firstNonEmpty(q.UserName, q.User)
	if q.Kind == "weibo_backfill" || q.List == "weibo_backfill" || q.Kind == "site_backfill" || q.List == "site_backfill" {
		title = "历史缓存补全 · " + siteShortLabel(q.Site)
	} else if q.ListLabel != "" {
		title += " · " + q.ListLabel
	}

	statusCol := theme.ColorTextMuted
	switch q.Status {
	case "running":
		statusCol = theme.ColorAccent
	case "waiting_backoff", "paused":
		statusCol = theme.ColorWarnText
	case "completed":
		statusCol = theme.ColorDotGreen
	}

	mobile.SwipeRow(c.Key("q-swipe-"+q.ID), p.swipeStateFor("q:"+q.ID), mobile.SwipeOptions{
		Trailing: p.queueSwipeActions(h, qCopy),
	}, func() {
		ui.Column(c).
			Label("缓存队列卡片："+q.ID).
			FillWidth().
			Padding(12, 14).
			Radius(12).
			Background(theme.ColorBgMain).
			Border(1, theme.ColorBorder).
			Gap(8).
			ClipX().
			Children(func() {
				ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).ClipX().Children(func() {
					headBtn := ui.ButtonBase(c).
						Label("展开或收起缓存队列：" + q.ID).
						Grow(1).
						Shrink(1).
						ClipX()
					headBtn.Children(func() {
						ui.Column(c).FillWidth().Gap(4).ClipX().Children(func() {
							ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
								ui.Text(c, title).FontSize(14).FontWeight(600).TextColor(theme.ColorTextMain).SingleLine().Shrink(1)
								renderSiteTag(c, q.Site)
							})
							ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).Wrap().ClipX().Children(func() {
								ui.Text(c, queueStatusLabel(q)).FontSize(12).FontWeight(600).TextColor(statusCol).SingleLine()
								ui.Text(c, fmt.Sprintf("· 已缓存 %d 条 · 跳过 %d 条", q.CachedCount, q.SkippedCount)).
									FontSize(12).TextColor(theme.ColorTextMuted).SingleLine()
							})
						})
					})
					if headBtn.Clicked() {
						p.expandedQueues[q.ID] = !expanded
					}

					moreBtn := ui.Button(c, "").
						Label("缓存队列更多操作："+q.ID).
						Radius(999).
						Size(40, 40).
						Background(theme.ColorBgMain).
						Border(1, theme.ColorBorderStrong).
						Shrink(0).
						Center()
					moreBtn.Children(func() {
						ui.Text(c, "⋯").FontSize(16).FontWeight(700).TextColor(theme.ColorTextMain).SingleLine()
					})
					if moreBtn.Clicked() {
						cp := q
						p.queueSheetItem = &cp
						p.queueSheetOpen = true
					}
				})

				if q.Status == "running" && q.ActiveTitle != "" {
					ui.Text(c, "当前正在缓存："+q.ActiveTitle).FontSize(12).FontWeight(600).TextColor(theme.ColorAccent).SingleLine()
				}
				if q.PauseReason != "" {
					ui.Text(c, "暂停原因："+pauseReasonLabel(q.PauseReason)).FontSize(12).TextColor(theme.ColorWarnText).MaxLines(2)
				}
				if q.StopReason != "" && q.StopReason != queueStatusLabel(q) {
					ui.Text(c, "原因："+q.StopReason).FontSize(12).TextColor(theme.ColorTextMuted).MaxLines(2)
				}
				if q.Note != "" && q.Note != q.StopReason {
					ui.Text(c, q.Note).FontSize(12).TextColor(theme.ColorTextMuted).MaxLines(2)
				}

				if expanded {
					ui.Column(c).FillWidth().Padding(8, 0, 0, 0).BorderWidth(1, 0, 0, 0).BorderColor(theme.ColorBorder).Gap(6).ClipX().Children(func() {
						ui.Text(c, "最近缓存的内容").FontSize(12).FontWeight(600).TextColor(theme.ColorTextMain).SingleLine()
						if len(q.RecentItems) == 0 {
							ui.Text(c, "还没有通过这个队列缓存完成的条目。").FontSize(12).TextColor(theme.ColorTextMuted)
						} else {
							for idx, ri := range q.RecentItems {
								ui.Row(c.Key(idx)).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(8).ClipX().Children(func() {
									ui.Text(c, firstNonEmpty(ri.Title, ri.ItemKey)).
										FontSize(12).TextColor(theme.ColorTextMain).SingleLine().Grow(1).Shrink(1)
									if ts := formatTimeShort(ri.CachedAt); ts != "" {
										ui.Text(c, ts).FontSize(11).TextColor(theme.ColorTextFaint).SingleLine().Shrink(0)
									}
								})
							}
						}
					})
				}
			})
	})
}

func (p *Page) renderQueueActionSheet(c *ui.Context, h pagekit.Host) {
	if p.queueSheetItem == nil {
		p.queueSheetOpen = false
		return
	}
	q := *p.queueSheetItem
	allowed := AllowedQueueActions(q.Status)
	actions := make([]mobile.Action, 0, len(allowed))
	for _, act := range allowed {
		switch act {
		case "pause":
			actions = append(actions, mobile.Action{
				Label: "暂停",
				OnTap: func() { p.runQueueAction(h, q, "pause") },
			})
		case "resume":
			actions = append(actions, mobile.Action{
				Label: "继续",
				OnTap: func() { p.runQueueAction(h, q, "resume") },
			})
		case "cancel":
			actions = append(actions, mobile.Action{
				Label:       "取消",
				Destructive: true,
				OnTap:       func() { p.runQueueAction(h, q, "cancel") },
			})
		case "remove":
			actions = append(actions, mobile.Action{
				Label:       "移除",
				Destructive: true,
				OnTap:       func() { p.runQueueAction(h, q, "remove") },
			})
		}
	}

	mobile.ActionSheet(c, &p.queueSheetOpen, mobile.ActionSheetOptions{
		Title:   firstNonEmpty(q.UserName, q.User),
		Message: queueStatusLabel(q),
		Cancel:  "关闭",
	}, actions...)
}

// renderDownloadDetailView renders /download/{id} with per-file progress and sanitized retry attempts (FR-MOB-016 / FR-CACHE-009).
func (p *Page) renderDownloadDetailView(c *ui.Context, h pagekit.Host, r *ui.Router, title string) {
	ui.Column(c).Fill().Background(theme.ColorBgPanel).ClipX().Children(func() {
		if !r.InSystemStack(c) {
			mobile.TopBar(c, mobile.TopBarOptions{
				Title: title,
				Leading: func() {
					backBtn := ui.ButtonBase(c).Label("返回任务列表").Size(44, 44).Center()
					backBtn.Children(func() {
						theme.DrawIcon(c, "arrow-left", 20, theme.ColorTextMain, 1.8)
					})
					if backBtn.Clicked() {
						h.Back()
					}
				},
			})
		}

		if p.detailLoading && p.detailResp == nil {
			p.renderLoadingSkeleton(c)
			return
		}
		if p.detailErr != "" && p.detailResp == nil {
			p.renderLoadError(c, p.detailErr, func() { p.loadDownloadDetail(h, p.detailTaskID) })
			return
		}
		if p.detailResp == nil {
			p.renderLoadError(c, "找不到这个下载任务", func() { p.loadDownloadDetail(h, p.detailTaskID) })
			return
		}

		resp := *p.detailResp
		task := resp.Task
		isPaused := isPausedDownloadStatus(task.Status)

		ui.Scroll(c).Grow(1).Basis(0).FillWidth().ClipX().Children(func() {
			ui.Column(c).FillWidth().Padding(14, 14, 28, 14).Gap(12).ClipX().Children(func() {
				p.renderFeedbackBanners(c)

				// Summary card.
				ui.Column(c).FillWidth().Padding(14).Radius(12).Background(theme.ColorBgMain).Border(1, theme.ColorBorder).Gap(8).ClipX().Children(func() {
					ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
						ui.Text(c, firstNonEmpty(task.Title, task.TargetID)).
							FontSize(16).FontWeight(600).TextColor(theme.ColorTextMain).MaxLines(2).Grow(1).Shrink(1)
						renderSiteTag(c, task.Site)
					})
					var meta []string
					meta = append(meta, downloadStatusLabel(task.Status))
					if task.TotalBytes > 0 {
						meta = append(meta, fmt.Sprintf("%.0f%% (%s / %s)", task.Progress*100, formatBytes(task.BytesDownloaded), formatBytes(task.TotalBytes)))
					}
					if task.Status == "running" && task.SpeedBps > 0 {
						meta = append(meta, formatSpeed(task.SpeedBps))
					}
					ui.Text(c, strings.Join(meta, " · ")).FontSize(13).TextColor(theme.ColorTextMuted)
					if task.QualityHint != "" {
						ui.Text(c, "画质与音轨："+task.QualityHint).FontSize(12).TextColor(theme.ColorAccent)
					}
					if task.ErrorMessage != "" {
						ui.Text(c, "原因："+task.ErrorMessage).FontSize(12).TextColor(theme.ColorError)
					} else if isPaused && pauseReasonLabel(task.PauseReason) != "" {
						ui.Text(c, "原因："+pauseReasonLabel(task.PauseReason)).FontSize(12).TextColor(theme.ColorWarnText)
					}

					// State-strict control buttons.
					ui.Row(c).FillWidth().Gap(8).Wrap().ClipX().Children(func() {
						switch {
						case task.Status == "running" || task.Status == "queued":
							if theme.SecondaryPillButton(c, "暂停", "详情暂停任务").Clicked() {
								p.runDownloadAction(h, task, "pause")
							}
							if theme.DangerOutlinePillButton(c, "取消", "详情取消任务").Clicked() {
								p.runDownloadAction(h, task, "cancel")
							}
						case isPaused:
							if theme.PrimaryPillButton(c, "继续", "详情继续任务").Clicked() {
								p.runDownloadAction(h, task, "resume")
							}
							if theme.DangerOutlinePillButton(c, "取消", "详情取消任务").Clicked() {
								p.runDownloadAction(h, task, "cancel")
							}
						case task.Status == "failed" || task.Status == "partial":
							if theme.PrimaryPillButton(c, "重试", "详情重试任务").Clicked() {
								p.removeConfirmID = ""
								p.runDownloadAction(h, task, "retry")
							}
							if theme.DangerOutlinePillButton(c, "移除", "详情移除任务").Clicked() {
								p.removeConfirmID = task.TaskID
							}
						case task.Status == "completed" || task.Status == "canceled":
							if theme.DangerOutlinePillButton(c, "移除", "详情移除任务").Clicked() {
								p.removeConfirmID = ""
								p.runDownloadAction(h, task, "remove")
							}
						}
					})

					if p.removeConfirmID == task.TaskID {
						p.renderRemoveConfirmBanner(c, h, task, "详情")
					}
				})

				// Per-file progress list.
				ui.Column(c).FillWidth().Padding(14).Radius(12).Background(theme.ColorBgMain).Border(1, theme.ColorBorder).Gap(8).ClipX().Children(func() {
					ui.Text(c, fmt.Sprintf("逐文件进度（%d 个文件）", len(resp.Files))).
						FontSize(14).FontWeight(600).TextColor(theme.ColorTextMain)
					if len(resp.Files) == 0 {
						ui.Text(c, "暂无逐文件进度明细").FontSize(12).TextColor(theme.ColorTextMuted)
					} else {
						for idx, f := range resp.Files {
							ui.Column(c.Key(idx)).FillWidth().Padding(8, 0).BorderWidth(1, 0, 0, 0).BorderColor(theme.ColorBorder).Gap(3).ClipX().Children(func() {
								ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(8).ClipX().Children(func() {
									ui.Text(c, fmt.Sprintf("[%s] %s", fileRoleLabel(f.Role), f.Path)).
										FontSize(13).FontWeight(500).TextColor(theme.ColorTextMain).SingleLine().Grow(1).Shrink(1)
									ui.Text(c, fileStatusLabel(f.Status)).
										FontSize(12).FontWeight(600).TextColor(theme.ColorTextMuted).SingleLine().Shrink(0)
								})
								szLine := fmt.Sprintf("%s / %s", formatBytes(f.DownloadedBytes), formatBytes(f.TotalBytes))
								if f.SpeedBps > 0 {
									szLine += " · " + formatSpeed(f.SpeedBps)
								}
								ui.Text(c, szLine).FontSize(12).TextColor(theme.ColorTextMuted).SingleLine()
							})
						}
					}
				})

				// Sanitized retry attempts (host only, never signed URLs).
				if len(resp.Attempts) > 0 {
					ui.Column(c).FillWidth().Padding(14).Radius(12).Background(theme.ColorBgMain).Border(1, theme.ColorBorder).Gap(6).ClipX().Children(func() {
						ui.Text(c, fmt.Sprintf("分段重试记录（已脱敏，%d 条）", len(resp.Attempts))).
							FontSize(14).FontWeight(600).TextColor(theme.ColorTextMain)
						for idx, att := range resp.Attempts {
							line := fmt.Sprintf("%s · %s · 节点 %s · 偏移 %s · %s（第 %d 次）",
								formatTimeShort(att.Time), att.File, att.Host, formatBytes(att.Offset), firstNonEmpty(att.ErrorKind, "异常"), att.Attempt)
							ui.Text(c.Key(idx), line).FontSize(11).LineHeight(theme.LineHeightCompact).TextColor(theme.ColorTextMuted).MaxLines(2)
						}
					})
				}
			})
		})
	})
}
