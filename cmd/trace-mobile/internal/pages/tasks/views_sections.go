package tasks

import (
	"fmt"
	"strings"

	"github.com/chialecode/trace/cmd/trace-mobile/internal/pagekit"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/theme"
	"github.com/chialecode/trace/internal/companionapi"
	"github.com/egoist/mygo/ui"
)

func (p *Page) renderLiveSegment(c *ui.Context, h pagekit.Host) {
	if p.recordingsLoading && !p.recordingsLoaded {
		p.renderLoadingSkeleton(c)
		return
	}
	if p.recordingsErr != "" && len(p.recordings) == 0 {
		p.renderLoadError(c, p.recordingsErr, func() { p.loadLiveRecordings(h) })
		return
	}

	sc := ui.Scroll(c).Grow(1).Basis(0).FillWidth().Background(theme.ColorBgPanel).ClipX()
	if sc.PullToRefresh(&p.liveRefresh) {
		p.loadLiveRecordings(h)
	}
	sc.Children(func() {
		ui.Column(c).FillWidth().Padding(12, 14, 28, 14).Gap(10).ClipX().Children(func() {
			if len(p.recordings) == 0 {
				ui.Column(c).FillWidth().Padding(40, 20).Radius(12).Background(theme.ColorBgMain).Border(1, theme.ColorBorder).AlignItems(ui.Center).Gap(6).Children(func() {
					ui.Text(c, "现在没有直播在录制").FontSize(15).FontWeight(600).TextColor(theme.ColorTextMain)
					ui.Text(c, "在订阅的「账号」里可以对正在直播的 B 站或抖音账号开始录制。").
						FontSize(12).LineHeight(theme.LineHeightCompact).TextColor(theme.ColorTextMuted).TextAlign(ui.Center)
				})
				return
			}

			var active, recent []companionapi.LiveRecordingDTO
			for _, rec := range p.recordings {
				if rec.Recording {
					active = append(active, rec)
				} else {
					recent = append(recent, rec)
				}
			}

			if len(active) > 0 {
				ui.Text(c, fmt.Sprintf("正在录制（%d）", len(active))).FontSize(13).FontWeight(600).TextColor(theme.ColorTextMuted).Padding(0, 2)
				for _, rec := range active {
					p.renderLiveRecordingCard(c, h, rec)
				}
			}
			if len(recent) > 0 {
				ui.Text(c, fmt.Sprintf("最近结束的录制（%d）", len(recent))).FontSize(13).FontWeight(600).TextColor(theme.ColorTextMuted).Padding(6, 2, 0, 2)
				for _, rec := range recent {
					p.renderLiveRecordingCard(c, h, rec)
				}
			}
		})
	})
}

func (p *Page) renderLiveRecordingCard(c *ui.Context, h pagekit.Host, rec companionapi.LiveRecordingDTO) {
	recKey := firstNonEmpty(rec.RecordingKey, rec.ItemKey, rec.AccountKey)
	expanded := p.expandedLiveIncs[recKey]
	busy := p.busyActionKey == "live:stop:"+recKey

	borderCol := theme.ColorBorder
	if rec.Recording {
		borderCol = ui.RGBA(194, 65, 12, 0.35)
	}

	ui.Column(c.Key("live-"+recKey)).
		Label("直播录制卡片："+recKey).
		FillWidth().
		Padding(12, 14).
		Radius(12).
		Background(theme.ColorBgMain).
		Border(1, borderCol).
		Gap(8).
		ClipX().
		Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
				ui.Column(c).Grow(1).Shrink(1).Gap(4).ClipX().Children(func() {
					ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
						if rec.Recording {
							ui.Box(c).Padding(2, 8).Radius(999).Background(theme.ColorLive).Shrink(0).Children(func() {
								ui.Text(c, "录制中").FontSize(11).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()
							})
						} else {
							ui.Box(c).Padding(2, 8).Radius(999).Background(theme.ColorBgSecondary).Shrink(0).Children(func() {
								ui.Text(c, "已结束").FontSize(11).TextColor(theme.ColorTextMuted).SingleLine()
							})
						}
						ui.Text(c, firstNonEmpty(rec.Uname, rec.UID)).
							FontSize(14).FontWeight(600).TextColor(theme.ColorTextMain).SingleLine().Shrink(1)
						renderSiteTag(c, rec.Site)
					})
					if rec.Title != "" {
						ui.Text(c, rec.Title).FontSize(13).TextColor(theme.ColorTextMain).MaxLines(2)
					}
				})

				// "停止录制" action ONLY on active recordings (FR-MOB-021, ui.md §3.12).
				if rec.Recording {
					lbl := "停止录制"
					if busy {
						lbl = "停止中…"
					}
					btn := theme.DangerOutlinePillButton(c, lbl, "停止直播录制："+firstNonEmpty(rec.Uname, recKey)).
						Disabled(busy).Shrink(0)
					if btn.Clicked() && !busy {
						p.stopLiveRecording(h, rec)
					}
				}
			})

			// Metrics: duration, size, speed, segments, quality & codec.
			var metrics []string
			if rec.DurationSec > 0 {
				metrics = append(metrics, "时长 "+formatDurationFloat(rec.DurationSec))
			}
			if rec.Bytes > 0 {
				metrics = append(metrics, formatBytes(rec.Bytes))
			}
			if rec.Recording && rec.SpeedBps > 0 {
				metrics = append(metrics, formatSpeed(rec.SpeedBps))
			}
			if rec.Segments > 0 {
				metrics = append(metrics, fmt.Sprintf("%d 段", rec.Segments))
			}
			qual := strings.TrimSpace(strings.Join(nonEmpty(rec.QualityName, rec.Codec), " · "))
			if qual != "" {
				metrics = append(metrics, qual)
			}
			if len(metrics) > 0 {
				ui.Text(c, strings.Join(metrics, " · ")).FontSize(12).TextColor(theme.ColorTextMuted).MaxLines(2)
			}

			if rec.LastError != "" {
				ui.Text(c, "错误："+rec.LastError).FontSize(12).TextColor(theme.ColorError).MaxLines(2)
			} else if rec.StopReason != "" && !rec.Recording {
				ui.Text(c, "结束原因："+rec.StopReason).FontSize(12).TextColor(theme.ColorTextMuted).SingleLine()
			}

			// Inline expandable incidents pill ("⚠ N 处异常", DEC-081, ui.md §3.6 & §3.12).
			if len(rec.Incidents) > 0 {
				toggleBtn := ui.Button(c, "").
					Label(fmt.Sprintf("展开或收起录制异常：%s", recKey)).
					Radius(999).
					Padding(5, 10).
					MinHeight(34).
					Background(theme.ColorWarnBg).
					Border(1, ui.RGBA(243, 161, 21, 0.40)).
					Center()
				toggleBtn.Children(func() {
					arrow := "▾"
					if expanded {
						arrow = "▴"
					}
					ui.Text(c, fmt.Sprintf("⚠ %d 处异常 %s", len(rec.Incidents), arrow)).
						FontSize(12).FontWeight(600).TextColor(theme.ColorLive).SingleLine()
				})
				if toggleBtn.Clicked() {
					p.expandedLiveIncs[recKey] = !expanded
				}

				if expanded {
					renderIncidentListBox(c, rec.Incidents)
				}
			}
		})
}

func (p *Page) renderPendingSegment(c *ui.Context, h pagekit.Host) {
	if p.conflictsLoading && !p.conflictsLoaded && p.incidentsLoading && !p.incidentsLoaded && p.downloadsLoading && !p.downloadsLoaded {
		p.renderLoadingSkeleton(c)
		return
	}

	var failedDownloads []companionapi.DownloadTaskDTO
	for _, t := range p.downloads {
		if isRetryableDownloadTask(t) {
			failedDownloads = append(failedDownloads, t)
		}
	}

	sc := ui.Scroll(c).Grow(1).Basis(0).FillWidth().Background(theme.ColorBgPanel).ClipX()
	if sc.PullToRefresh(&p.pendingRefresh) {
		p.loadConflicts(h)
		p.loadDownloads(h)
		p.loadLiveIncidents(h)
	}
	sc.Children(func() {
		ui.Column(c).FillWidth().Padding(12, 14, 28, 14).Gap(16).ClipX().Children(func() {
			if len(p.conflicts) == 0 && len(failedDownloads) == 0 && len(p.incidents) == 0 {
				ui.Column(c).FillWidth().Padding(40, 20).Radius(12).Background(theme.ColorBgMain).Border(1, theme.ColorBorder).AlignItems(ui.Center).Gap(6).Children(func() {
					ui.Text(c, "目前没有需要处理的事项").FontSize(15).FontWeight(600).TextColor(theme.ColorTextMain)
					ui.Text(c, "内容冲突、失败下载任务与近期录制异常会汇总在这里。").
						FontSize(12).TextColor(theme.ColorTextMuted).TextAlign(ui.Center)
				})
				return
			}

			// 1. Read-only content conflicts with top notice "在电脑上处理" (ZERO resolve actions — FR-MOB-016).
			if len(p.conflicts) > 0 {
				ui.Column(c).FillWidth().Gap(8).ClipX().Children(func() {
					ui.Text(c, fmt.Sprintf("内容冲突（%d）", len(p.conflicts))).FontSize(14).FontWeight(600).TextColor(theme.ColorTextMain)
					ui.Row(c).
						Label("内容冲突只读提示").
						FillWidth().
						Padding(10, 12).
						Radius(10).
						Background(theme.ColorWarnBg).
						Border(1, ui.RGBA(243, 161, 21, 0.35)).
						AlignItems(ui.Center).
						Gap(8).
						ClipX().
						Children(func() {
							theme.DrawIcon(c, "alert-circle", 16, theme.ColorWarnText, 1.8)
							ui.Text(c, "在电脑上处理：移动端仅只读查看内容冲突，请前往电脑端 TRACE 任务页处理。").
								FontSize(12).LineHeight(theme.LineHeightCompact).TextColor(theme.ColorWarnText).MaxLines(3).Grow(1).Shrink(1)
						})

					for _, cf := range p.conflicts {
						renderReadOnlyConflictCard(c, cf)
					}
				})
			}

			// 2. Failed / incomplete download tasks (retry / remove).
			if len(failedDownloads) > 0 {
				ui.Column(c).FillWidth().Gap(8).ClipX().Children(func() {
					ui.Text(c, fmt.Sprintf("失败 / 未完成的下载任务（%d）", len(failedDownloads))).FontSize(14).FontWeight(600).TextColor(theme.ColorTextMain)
					for _, t := range failedDownloads {
						p.renderPendingDownloadCard(c, h, t)
					}
				})
			}

			// 3. Recent live incidents (read-only on mobile).
			if len(p.incidents) > 0 {
				ui.Column(c).FillWidth().Gap(8).ClipX().Children(func() {
					ui.Text(c, fmt.Sprintf("近期录制异常（%d 场）", len(p.incidents))).FontSize(14).FontWeight(600).TextColor(theme.ColorTextMain)
					for _, sess := range p.incidents {
						renderLiveIncidentSessionCard(c, sess)
					}
				})
			}
		})
	})
}

func renderReadOnlyConflictCard(c *ui.Context, cf companionapi.ConflictDTO) {
	ui.Column(c.Key("conflict-"+cf.ID)).
		Label("只读内容冲突卡片："+cf.ID).
		FillWidth().
		Padding(12, 14).
		Radius(12).
		Background(theme.ColorBgMain).
		Border(1, theme.ColorBorder).
		Gap(6).
		ClipX().
		Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).Wrap().ClipX().Children(func() {
				if cf.ReasonLabel != "" {
					ui.Box(c).Padding(2, 8).Radius(999).Background(theme.ColorWarnBg).Shrink(0).Children(func() {
						ui.Text(c, cf.ReasonLabel).FontSize(11).FontWeight(600).TextColor(theme.ColorWarnText).SingleLine()
					})
				}
				renderSiteTag(c, cf.Site)
				if cf.AuthorName != "" {
					ui.Text(c, cf.AuthorName).FontSize(12).TextColor(theme.ColorTextMuted).SingleLine()
				}
			})
			ui.Text(c, firstNonEmpty(cf.Title, cf.ItemKey)).
				FontSize(14).FontWeight(600).TextColor(theme.ColorTextMain).MaxLines(2)
			if cf.LocalSummary != "" {
				ui.Text(c, "本地："+cf.LocalSummary).FontSize(12).LineHeight(theme.LineHeightCompact).TextColor(theme.ColorTextMuted).MaxLines(2)
			}
			if cf.RemoteSummary != "" {
				ui.Text(c, "源站："+cf.RemoteSummary).FontSize(12).LineHeight(theme.LineHeightCompact).TextColor(theme.ColorTextMuted).MaxLines(2)
			}
			if cf.LastError != "" {
				ui.Text(c, "上次处理失败："+cf.LastError).FontSize(12).TextColor(theme.ColorError).MaxLines(2)
			}
			if ts := formatTimeShort(cf.DetectedAt); ts != "" {
				ui.Text(c, "发现于 "+ts).FontSize(11).TextColor(theme.ColorTextFaint).SingleLine()
			}
		})
}

func (p *Page) renderPendingDownloadCard(c *ui.Context, h pagekit.Host, task companionapi.DownloadTaskDTO) {
	isPaused := isPausedDownloadStatus(task.Status)
	ui.Column(c.Key("pend-dl-"+task.TaskID)).
		Label("待处理下载任务："+task.TaskID).
		FillWidth().
		Padding(12, 14).
		Radius(12).
		Background(theme.ColorBgMain).
		Border(1, theme.ColorBorder).
		Gap(8).
		ClipX().
		Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
				ui.Text(c, firstNonEmpty(task.Title, task.TargetID)).
					FontSize(14).FontWeight(600).TextColor(theme.ColorTextMain).SingleLine().Grow(1).Shrink(1)
				renderSiteTag(c, task.Site)
			})
			ui.Text(c, fmt.Sprintf("%s · %.0f%% (%s / %s)",
				downloadStatusLabel(task.Status), task.Progress*100, formatBytes(task.BytesDownloaded), formatBytes(task.TotalBytes))).
				FontSize(12).TextColor(theme.ColorTextMuted).SingleLine()

			reason := firstNonEmpty(task.ErrorMessage, pauseReasonLabel(task.PauseReason))
			if reason != "" {
				ui.Text(c, "原因："+reason).FontSize(12).LineHeight(theme.LineHeightCompact).TextColor(theme.ColorError).MaxLines(2)
			}

			ui.Row(c).FillWidth().Gap(8).Wrap().ClipX().Children(func() {
				if isPaused {
					if theme.PrimaryPillButton(c, "继续", "待处理继续任务："+task.TaskID).Clicked() {
						p.runDownloadAction(h, task, "resume")
					}
					if theme.DangerOutlinePillButton(c, "取消", "待处理取消任务："+task.TaskID).Clicked() {
						p.runDownloadAction(h, task, "cancel")
					}
				} else {
					if theme.PrimaryPillButton(c, "重试", "待处理重试任务："+task.TaskID).Clicked() {
						p.runDownloadAction(h, task, "retry")
					}
					if theme.DangerOutlinePillButton(c, "移除", "待处理移除任务："+task.TaskID).Clicked() {
						p.runDownloadAction(h, task, "remove")
					}
				}
			})
		})
}

func renderLiveIncidentSessionCard(c *ui.Context, sess companionapi.LiveIncidentSessionDTO) {
	key := firstNonEmpty(sess.RecordingKey, sess.ItemKey, sess.Site+":"+sess.UID+":"+sess.StartedAt)
	ui.Column(c.Key("inc-sess-"+key)).
		Label("录制异常场次："+key).
		FillWidth().
		Padding(12, 14).
		Radius(12).
		Background(theme.ColorBgMain).
		Border(1, theme.ColorBorder).
		Gap(8).
		ClipX().
		Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).Wrap().ClipX().Children(func() {
				if sess.Recording {
					ui.Box(c).Padding(2, 8).Radius(999).Background(theme.ColorLive).Shrink(0).Children(func() {
						ui.Text(c, "录制中").FontSize(11).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()
					})
				} else {
					ui.Box(c).Padding(2, 8).Radius(999).Background(theme.ColorBgSecondary).Shrink(0).Children(func() {
						ui.Text(c, "已结束").FontSize(11).TextColor(theme.ColorTextMuted).SingleLine()
					})
				}
				ui.Text(c, firstNonEmpty(sess.Uname, sess.UID)).
					FontSize(14).FontWeight(600).TextColor(theme.ColorTextMain).SingleLine().Shrink(1)
				renderSiteTag(c, sess.Site)
				ui.Box(c).Padding(2, 8).Radius(999).Background(theme.ColorWarnBg).Shrink(0).Children(func() {
					ui.Text(c, fmt.Sprintf("⚠ %d 处异常", len(sess.Incidents))).
						FontSize(11).FontWeight(600).TextColor(theme.ColorLive).SingleLine()
				})
			})
			if sess.Title != "" {
				ui.Text(c, sess.Title).FontSize(13).TextColor(theme.ColorTextMain).MaxLines(2)
			}
			if sess.StartedAt != "" {
				ui.Text(c, "开始时间 "+formatTimeShort(sess.StartedAt)).FontSize(11).TextColor(theme.ColorTextMuted).SingleLine()
			}
			renderIncidentListBox(c, sess.Incidents)
		})
}

func renderIncidentListBox(c *ui.Context, incidents []companionapi.LiveIncident) {
	ui.Column(c).FillWidth().Padding(10, 12).Radius(10).Background(theme.ColorBgPanel).Border(1, theme.ColorBorder).Gap(6).ClipX().Children(func() {
		for idx, inc := range incidents {
			var parts []string
			if ts := formatTimeShort(inc.At); ts != "" {
				parts = append(parts, ts)
			}
			parts = append(parts, incidentKindLabel(inc.Kind))
			if inc.DurationSec > 0 {
				parts = append(parts, "影响 "+formatDurationFloat(inc.DurationSec))
			}
			if inc.LostSegments > 0 {
				parts = append(parts, fmt.Sprintf("丢失 %d 片", inc.LostSegments))
			}
			if inc.Count > 1 {
				parts = append(parts, fmt.Sprintf("共 %d 次", inc.Count))
			}
			if inc.Reason != "" {
				parts = append(parts, inc.Reason)
			}
			ui.Text(c.Key(idx), strings.Join(parts, " · ")).
				FontSize(12).LineHeight(theme.LineHeightCompact).TextColor(theme.ColorTextMain).MaxLines(3)
		}
	})
}

// renderHistorySegment renders read-only import/export transfer jobs (ZERO actions — FR-MOB-016 / FR-MOB-025).
func (p *Page) renderHistorySegment(c *ui.Context, h pagekit.Host) {
	if p.transfersLoading && !p.transfersLoaded {
		p.renderLoadingSkeleton(c)
		return
	}
	if p.transfersErr != "" && len(p.transfers) == 0 {
		p.renderLoadError(c, p.transfersErr, func() { p.loadTransfers(h) })
		return
	}

	sc := ui.Scroll(c).Grow(1).Basis(0).FillWidth().Background(theme.ColorBgPanel).ClipX()
	if sc.PullToRefresh(&p.historyRefresh) {
		p.loadTransfers(h)
	}
	sc.Children(func() {
		ui.Column(c).FillWidth().Padding(12, 14, 28, 14).Gap(10).ClipX().Children(func() {
			ui.Text(c, "导入与导出仅在电脑端发起和管理，此处只读查看记录。").
				FontSize(12).TextColor(theme.ColorTextMuted).Padding(0, 2)

			if len(p.transfers) == 0 {
				ui.Column(c).FillWidth().Padding(40, 20).Radius(12).Background(theme.ColorBgMain).Border(1, theme.ColorBorder).AlignItems(ui.Center).Gap(6).Children(func() {
					ui.Text(c, "还没有导入或导出记录").FontSize(15).FontWeight(600).TextColor(theme.ColorTextMain)
					ui.Text(c, "在电脑端媒体库发起导入或导出后，进度与结果会在这里只读显示。").
						FontSize(12).TextColor(theme.ColorTextMuted).TextAlign(ui.Center)
				})
				return
			}

			for _, tr := range p.transfers {
				renderReadOnlyTransferCard(c, tr)
			}
		})
	})
}

func renderReadOnlyTransferCard(c *ui.Context, tr companionapi.TransferJobDTO) {
	kindLabel := "导入"
	if strings.EqualFold(tr.Kind, "export") {
		kindLabel = "导出"
	}
	statusLbl := transferStatusLabel(tr.Status)
	statusCol := theme.ColorTextMuted
	switch strings.ToLower(tr.Status) {
	case "importing", "exporting", "running", "scanning":
		statusCol = theme.ColorAccent
	case "completed", "done":
		statusCol = theme.ColorDotGreen
	case "failed":
		statusCol = theme.ColorError
	}

	var frac float64
	if tr.TotalBytes > 0 {
		frac = float64(tr.DoneBytes) / float64(tr.TotalBytes)
	} else if tr.TotalItems > 0 {
		frac = float64(tr.DoneItems) / float64(tr.TotalItems)
	}
	frac = max(0.0, min(1.0, frac))

	ui.Column(c.Key("tr-"+tr.ID)).
		Label("只读导入导出记录："+tr.ID).
		FillWidth().
		Padding(12, 14).
		Radius(12).
		Background(theme.ColorBgMain).
		Border(1, theme.ColorBorder).
		Gap(6).
		ClipX().
		Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
				ui.Text(c, fmt.Sprintf("%s任务 · %s", kindLabel, tr.ID)).
					FontSize(14).FontWeight(600).TextColor(theme.ColorTextMain).SingleLine().Grow(1).Shrink(1)
				ui.Text(c, statusLbl).FontSize(12).FontWeight(600).TextColor(statusCol).SingleLine().Shrink(0)
			})

			if tr.TotalItems > 0 || tr.TotalBytes > 0 {
				ui.Box(c).FillWidth().Height(6).Radius(3).Background(theme.ColorBorder).Clip().Draw(func(painter *ui.Painter, r ui.Rect) {
					if frac > 0 {
						painter.Fill(ui.Rect{X: r.X, Y: r.Y, W: r.W * float32(frac), H: r.H}, statusCol, 3)
					}
				})
			}

			var meta []string
			if tr.TotalItems > 0 {
				meta = append(meta, fmt.Sprintf("%d / %d 条", tr.DoneItems, tr.TotalItems))
			}
			if tr.TotalBytes > 0 {
				meta = append(meta, fmt.Sprintf("%s / %s", formatBytes(tr.DoneBytes), formatBytes(tr.TotalBytes)))
			}
			if tr.SpeedBps > 0 {
				meta = append(meta, formatSpeed(tr.SpeedBps))
			}
			if len(meta) > 0 {
				ui.Text(c, strings.Join(meta, " · ")).FontSize(12).TextColor(theme.ColorTextMuted).SingleLine()
			}
			if tr.Error != "" {
				ui.Text(c, "失败原因："+tr.Error).FontSize(12).TextColor(theme.ColorError).MaxLines(2)
			}
			var times []string
			if ts := formatTimeShort(tr.StartedAt); ts != "" {
				times = append(times, "开始 "+ts)
			}
			if ts := formatTimeShort(tr.FinishedAt); ts != "" {
				times = append(times, "结束 "+ts)
			}
			if len(times) > 0 {
				ui.Text(c, strings.Join(times, " · ")).FontSize(11).TextColor(theme.ColorTextFaint).SingleLine()
			}
		})
}

func renderSiteTag(c *ui.Context, site string) {
	label := siteShortLabel(site)
	if label == "" {
		return
	}
	ui.Text(c, label).FontSize(12).TextColor(theme.ColorTextMuted).SingleLine().Shrink(0)
}

func (p *Page) renderLoadingSkeleton(c *ui.Context) {
	ui.Column(c).Grow(1).Basis(0).FillWidth().Padding(14).Gap(10).Background(theme.ColorBgPanel).ClipX().Children(func() {
		for i := 0; i < 4; i++ {
			ui.Column(c.Key(i)).FillWidth().Padding(14).Radius(12).Background(theme.ColorBgMain).Border(1, theme.ColorBorder).Gap(8).Children(func() {
				ui.Box(c).FillWidth().Height(14).Radius(4).Background(theme.ColorBgSecondary)
				ui.Box(c).Width(160).Height(10).Radius(4).Background(theme.ColorBgSecondary)
				ui.Box(c).FillWidth().Height(6).Radius(3).Background(theme.ColorBgSecondary)
			})
		}
	})
}

func (p *Page) renderLoadError(c *ui.Context, msg string, onRetry func()) {
	ui.Column(c).Grow(1).Basis(0).FillWidth().Padding(40, 24).Gap(12).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
		theme.DrawIcon(c, "alert-circle", 32, theme.ColorError, 1.8)
		ui.Text(c, msg).FontSize(14).LineHeight(theme.LineHeightCompact).TextColor(theme.ColorTextMain).TextAlign(ui.Center)
		if theme.PrimaryPillButton(c, "重试", "重试加载任务").Clicked() && onRetry != nil {
			onRetry()
		}
	})
}

func nonEmpty(vals ...string) []string {
	var out []string
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			out = append(out, s)
		}
	}
	return out
}
