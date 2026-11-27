package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/egoist/mygo/ui"
)

// lookupAsyncFileBitmap decodes and caches an explicit local image file off the
// UI thread (with negative caching so missing/unreadable files never re-hit
// disk on subsequent frames).
func (a *App) lookupAsyncFileBitmap(fullPath string) *ui.Bitmap {
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
	a.images.reserve(cacheKey)
	a.mu.Unlock()

	requestGuarded(a, cacheKey, nil, func(ctx context.Context) (*ui.Bitmap, error) {
		select {
		case a.imageQueue <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		defer func() { <-a.imageQueue }()
		return loadThumbBitmap(fullPath, avatarThumbEdge), nil
	}, func(bmp *ui.Bitmap) {
		a.mu.Lock()
		if !a.closed {
			a.images.fill(cacheKey, bmp)
		}
		a.mu.Unlock()
	}, func(error) {})

	a.mu.Lock()
	bmp, _ := a.images.get(cacheKey)
	a.mu.Unlock()
	return bmp
}

func (a *App) renderQueueLocalAvatar(c *ui.Context, q service.CacheQueueTask, size float32) {
	name := firstNonEmptyStrUI(q.UserName, q.User, "创作者")
	var mid int64
	if normalizeSiteIDUI(q.Site) == "bilibili" {
		mid, _ = strconv.ParseInt(strings.TrimSpace(q.User), 10, 64)
	}
	var bmp *ui.Bitmap
	if avatarPath := strings.TrimSpace(q.AvatarPath); avatarPath != "" {
		bmp = a.lookupAsyncFileBitmap(avatarPath)
	} else {
		bmp = a.lookupAccountAvatarBitmap(q.Site, q.User)
	}
	a.pendingLocalAvatar = false
	col := AvatarColor(name, mid)
	r := size / 2
	ui.Box(c).Size(size, size).Shrink(0).Radius(r).Background(col).Clip().Center().Children(func() {
		if bmp != nil {
			ui.Image(c, bmp).Size(size, size).Fit(ui.Cover)
			return
		}
		ui.Text(c, FirstGlyph(name)).FontSize(size * 0.42).FontWeight(600).TextColor(ColorWhite).SingleLine()
	})
}

func formatQueueSidebarStatus(q service.CacheQueueTask) string {
	switch q.Status {
	case service.CacheQueueRunning:
		return "进行中"
	case service.CacheQueueQueued:
		return "排队中"
	case service.CacheQueueWaiting:
		if clock := formatRFC3339Clock(q.NextRunAt, false); clock != "" {
			return "下次 " + clock
		}
		return "等待下一条"
	case service.CacheQueueWaitingBackoff:
		if clock := formatRFC3339Clock(q.NextRunAt, false); clock != "" {
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

// compactOrderButton renders a subtle compact reorder button ("置顶"/"上移"/"下移")
// that fits cleanly at 720/1000/1280 widths without competing with primary actions.
func compactOrderButton(c *ui.Context, text, label string, disabled bool) bool {
	bg := ColorBgSecondary
	txtColor := ColorTextMain
	if disabled {
		bg = ColorBgMain
		txtColor = ColorTextMuted
	}
	btn := ui.ButtonBase(c).
		Label(label).
		Padding(3, 8).
		Radius(999).
		Border(1, ColorBorder).
		Background(bg).
		Center().
		Shrink(0).
		Disabled(disabled)
	if !disabled && btn.Hovered() {
		btn.Background(ColorHover)
	}
	btn.Children(func() {
		ui.Text(c, text).FontSize(11).FontWeight(500).TextColor(txtColor).SingleLine()
	})
	return !disabled && btn.Clicked()
}

func (a *App) effectiveTasksQueueSiteFilter() string {
	switch strings.ToLower(strings.TrimSpace(a.TasksQueueSiteFilter)) {
	case "bilibili", "douyin", "weibo", "x":
		return strings.ToLower(strings.TrimSpace(a.TasksQueueSiteFilter))
	default:
		return "all"
	}
}

type siteQueueSummaryInfo struct {
	statusText      string
	statusColor     ui.Color
	dotColor        ui.Color
	unfinishedCount int
	finishedCount   int
	hasRunning      bool
}

func summarizeSiteCacheQueue(tasks []service.CacheQueueTask) siteQueueSummaryInfo {
	info := siteQueueSummaryInfo{
		statusText:  "空闲",
		statusColor: ColorTextMuted,
		dotColor:    ColorBorder,
	}
	var (
		hasRunning bool
		hasWaiting bool
		waitNext   string
		hasBackoff bool
		hasPaused  bool
		hasQueued  bool
	)
	for _, q := range tasks {
		if isUnfinishedCacheQueueStatus(q.Status) {
			info.unfinishedCount++
		} else {
			info.finishedCount++
		}
		switch q.Status {
		case service.CacheQueueRunning:
			hasRunning = true
		case service.CacheQueueWaiting:
			hasWaiting = true
			if waitNext == "" && q.NextRunAt != "" {
				waitNext = formatRFC3339Clock(q.NextRunAt, false)
			}
		case service.CacheQueueWaitingBackoff:
			hasBackoff = true
		case service.CacheQueuePaused:
			hasPaused = true
		case service.CacheQueueQueued:
			hasQueued = true
		}
	}
	info.hasRunning = hasRunning
	switch {
	case hasRunning:
		info.statusText = "进行中 · 独立并行"
		info.statusColor = ColorAccent
		info.dotColor = ColorAccent
	case hasWaiting:
		if waitNext != "" {
			info.statusText = "等待下一条（" + waitNext + "）· 独立并行"
		} else {
			info.statusText = "等待下一条 · 独立并行"
		}
		info.statusColor = ColorInfoBlue
		info.dotColor = ColorInfoBlue
	case hasBackoff:
		info.statusText = "退避中 · 不影响其他站点"
		info.statusColor = ColorDotYellow
		info.dotColor = ColorDotYellow
	case hasPaused:
		info.statusText = "已暂停"
		info.statusColor = ColorDotYellow
		info.dotColor = ColorDotYellow
	case hasQueued:
		info.statusText = "排队中"
		info.statusColor = ColorTextMuted
		info.dotColor = ColorInfoBlue
	case len(tasks) > 0:
		info.statusText = "已全部结束"
		info.statusColor = ColorDotGreen
		info.dotColor = ColorDotGreen
	}
	return info
}

func queueFilterPillLabel(siteID string) string {
	switch normalizeSiteIDUI(siteID) {
	case "bilibili":
		return "B站"
	case "douyin":
		return "抖音"
	case "weibo":
		return "微博"
	case "x":
		return "X"
	default:
		return "全部"
	}
}

// renderTasksQueueView lists background cache queues grouped by site so each
// site's independent running/backoff status and manual reorder controls are clear (DEC-088).
func (a *App) renderTasksQueueView(c *ui.Context) {
	ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
		ui.Column(c).FillWidth().Padding(10, 16).Gap(6).ClipX().Children(func() {
			if a.TaskActionError != "" {
				renderErrorBanner(c, "", a.TaskActionError, nil)
			}
			_, hasActiveBackfill := a.activeWeiboBackfillQueue()
			if !hasActiveBackfill && a.BackfillSummary.TotalItems > 0 {
				startingBackfill := a.isPending("start-weibo-backfill")
				ui.Row(c).
					Label("历史缓存补全提示条").
					FillWidth().
					Padding(10, 14).
					Radius(12).
					Background(ColorBgSecondary).
					Border(1, ColorBorder).
					AlignItems(ui.Center).
					Justify(ui.SpaceBetween).
					Gap(10).
					ClipX().
					Children(func() {
						ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
							ui.Text(c, fmt.Sprintf("发现 %d 条历史微博可补全", a.BackfillSummary.TotalItems)).
								FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
							if detail := formatBackfillSummaryBreakdown(a.BackfillSummary); detail != "" {
								ui.Text(c, detail).
									FontSize(12).TextColor(ColorTextMuted).SingleLine()
							}
						})
						btnText := "一键补全"
						if startingBackfill {
							btnText = "正在启动…"
						}
						if AccentPillButton(c, btnText, "一键补全历史微博缓存").Disabled(startingBackfill).Clicked() && !startingBackfill {
							a.startWeiboBackfill()
						}
					})
			}
			if len(a.CacheQueue) == 0 {
				ui.Column(c).FillWidth().Padding(40, 16).AlignItems(ui.Center).Gap(8).Children(func() {
					ui.Text(c, "当前没有缓存队列任务").FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					ui.Text(c, "在 UP 主、抖音作者、微博博主或 X 用户主页右上角点「⋯ → 全部缓存」，会在后台按安全节奏慢慢缓存全部内容。").
						FontSize(13).TextColor(ColorTextMuted).MaxLines(2)
				})
				return
			}

			bySite := make(map[string][]service.CacheQueueTask, len(site.All()))
			finished := 0
			totalUnfinished := 0
			for _, q := range a.CacheQueue {
				sid := normalizeSiteIDUI(q.Site)
				bySite[sid] = append(bySite[sid], q)
				if isUnfinishedCacheQueueStatus(q.Status) {
					totalUnfinished++
				} else {
					finished++
				}
			}

			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Wrap().ClipX().Children(func() {
				ui.Text(c, "各站点独立并行推进，单站退避不影响其他站点；取消或移除队列不会删除已缓存到媒体库的内容").
					FontSize(12).TextColor(ColorTextMuted).MaxLines(2).Grow(1).Shrink(1)
				if finished > 0 {
					if OutlinePillButton(c, fmt.Sprintf("清除已结束（%d）", finished), "清除已结束的缓存队列").Clicked() {
						_, err := a.backend.RemoveFinishedCacheQueues()
						a.finishCacheQueueAction(err)
					}
				}
			})

			// Per-site filter bar showing independent per-site status dots and unfinished counts.
			activeFilter := a.effectiveTasksQueueSiteFilter()
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).Wrap().ClipX().Children(func() {
				renderFilterPill := func(key, text string, dotColor *ui.Color) {
					active := activeFilter == key
					btn := ui.ButtonBase(c).
						Label("缓存队列站点筛选："+key).
						Padding(4, 10).
						Radius(999).
						Gap(5).
						AlignItems(ui.Center).
						Center().
						Shrink(0)
					if active {
						btn.Background(ColorAccentBg).Border(1, ColorAccent)
					} else {
						btn.Background(ColorBgSecondary).Border(1, ColorBorder)
						if btn.Hovered() {
							btn.Background(ColorHover)
						}
					}
					btn.Children(func() {
						if dotColor != nil {
							ui.Box(c).Size(6, 6).Radius(3).Background(*dotColor).Shrink(0)
						}
						t := ui.Text(c, text).FontSize(12).SingleLine()
						if active {
							t.FontWeight(600).TextColor(ColorAccent)
						} else {
							t.FontWeight(500).TextColor(ColorTextMuted)
						}
					})
					if btn.Clicked() {
						a.TasksQueueSiteFilter = key
					}
				}

				allText := "全部"
				if totalUnfinished > 0 {
					allText = fmt.Sprintf("全部（%d）", totalUnfinished)
				}
				renderFilterPill("all", allText, nil)
				for _, sid := range site.All() {
					key := string(sid)
					sum := summarizeSiteCacheQueue(bySite[key])
					txt := queueFilterPillLabel(key)
					if sum.unfinishedCount > 0 {
						txt = fmt.Sprintf("%s · %d", txt, sum.unfinishedCount)
					}
					var dot *ui.Color
					if len(bySite[key]) > 0 {
						dc := sum.dotColor
						dot = &dc
					}
					renderFilterPill(key, txt, dot)
				}
			})

			selID := a.effectiveSelectedQueueID()
			renderedAny := false
			for _, sid := range site.All() {
				key := string(sid)
				if activeFilter != "all" && activeFilter != key {
					continue
				}
				siteTasks := bySite[key]
				if len(siteTasks) == 0 {
					continue
				}
				renderedAny = true
				sum := summarizeSiteCacheQueue(siteTasks)

				// Keep unfinished tasks in their exact dispatch order, followed by finished tasks.
				unfinishedList := make([]service.CacheQueueTask, 0, sum.unfinishedCount)
				finishedList := make([]service.CacheQueueTask, 0, sum.finishedCount)
				for _, q := range siteTasks {
					if isUnfinishedCacheQueueStatus(q.Status) {
						unfinishedList = append(unfinishedList, q)
					} else {
						finishedList = append(finishedList, q)
					}
				}

				ui.Column(c).FillWidth().Gap(5).ClipX().Children(func() {
					ui.Row(c).
						Label("缓存队列站点分组："+key).
						FillWidth().
						Padding(2, 2).
						AlignItems(ui.Center).
						Justify(ui.SpaceBetween).
						Gap(8).
						Wrap().
						ClipX().
						Children(func() {
							ui.Row(c).AlignItems(ui.Center).Gap(8).Grow(1).Shrink(1).ClipX().Children(func() {
								renderSiteSmallBadgeWithDot(c, key, "")
								ui.Text(c, sum.statusText).FontSize(12).FontWeight(600).TextColor(sum.statusColor).SingleLine().Shrink(1)
							})
							countSummary := fmt.Sprintf("共 %d 个", len(siteTasks))
							if sum.unfinishedCount > 0 && sum.finishedCount > 0 {
								countSummary = fmt.Sprintf("未结束 %d · 已结束 %d", sum.unfinishedCount, sum.finishedCount)
							} else if sum.unfinishedCount > 0 {
								countSummary = fmt.Sprintf("未结束 %d 个", sum.unfinishedCount)
							}
							ui.Text(c, countSummary).FontSize(11).TextColor(ColorTextMuted).SingleLine().Shrink(0)
						})

					for idx, item := range unfinishedList {
						a.renderCacheQueueCard(c, item, item.ID == selID, idx, len(unfinishedList), sum.hasRunning)
					}
					for _, item := range finishedList {
						a.renderCacheQueueCard(c, item, item.ID == selID, -1, len(unfinishedList), sum.hasRunning)
					}
				})
			}

			if !renderedAny && activeFilter != "all" {
				ui.Column(c).FillWidth().Padding(32, 16).AlignItems(ui.Center).Gap(6).Children(func() {
					ui.Text(c, siteFullLabel(activeFilter)+"当前没有缓存队列任务").
						FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					if OutlinePillButton(c, "查看全部站点", "重置缓存队列站点筛选").Clicked() {
						a.TasksQueueSiteFilter = "all"
					}
				})
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

func (a *App) renderCacheQueueCard(c *ui.Context, cur service.CacheQueueTask, expanded bool, orderIdx, siteUnfinishedCount int, siteHasRunning bool) {
	qSite := normalizeSiteIDUI(cur.Site)
	isBackfill := cur.Kind == service.CacheQueueKindWeiboBackfill || cur.List == service.CacheQueueListWeiboBackfill
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
		Padding(8, 12).
		Radius(14).
		Background(ColorBgMain).
		Border(1, borderCol).
		Gap(5).
		ClipX().
		Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).ClipX().Children(func() {
				head := ui.ButtonBase(c).
					Label("展开缓存队列："+cur.ID).
					Grow(1).
					Shrink(1).
					Gap(10).
					Padding(2, 4).
					Radius(10).
					AlignItems(ui.Center).
					Justify(ui.Start).
					ClipX()
				if head.Hovered() {
					head.Background(ColorHover)
				}
				head.Children(func() {
					a.renderQueueLocalAvatar(c, cur, 34)
					ui.Column(c).Grow(1).Shrink(1).Gap(2).ClipX().Children(func() {
						ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
							title := firstNonEmptyStrUI(cur.UserName, cur.User)
							if isBackfill {
								title = "历史缓存补全 · 微博"
							} else if cur.ListLabel != "" {
								title += " · " + cur.ListLabel
							}
							ui.Text(c, title).FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine().Shrink(1)
							renderSiteSmallBadgeWithDot(c, qSite, "队列详情站点："+cur.ID)
						})
						statusLine := formatQueueSidebarStatus(cur)
						if unfinished && cur.NextRunAt != "" && cur.Status != service.CacheQueueWaiting && cur.Status != service.CacheQueueWaitingBackoff {
							if clock := formatRFC3339Clock(cur.NextRunAt, false); clock != "" {
								statusLine += " · 下次执行 " + clock
							}
						}
						if unfinished && orderIdx >= 0 && siteUnfinishedCount >= 2 {
							statusLine = fmt.Sprintf("#%d · %s", orderIdx+1, statusLine)
							if cur.Status == service.CacheQueueQueued && orderIdx == 0 && siteHasRunning {
								statusLine += "（下条优先）"
							} else if cur.Status == service.CacheQueueRunning && orderIdx > 0 {
								statusLine += "（本条后让位）"
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
							countLine := fmt.Sprintf("· 已缓存 %d 条 · 跳过 %d 条", cur.CachedCount, cur.SkippedCount)
							if isBackfill {
								if cur.BackfillTotal > 0 {
									countLine = fmt.Sprintf("· 已补全 %d / %d 条 · 跳过 %d 条", cur.CachedCount, cur.BackfillTotal, cur.SkippedCount)
								} else {
									countLine = fmt.Sprintf("· 已补全 %d 条 · 跳过 %d 条", cur.CachedCount, cur.SkippedCount)
								}
							}
							ui.Text(c, countLine).
								Label("队列详情计数：" + cur.ID).
								FontSize(12).TextColor(ColorTextMuted).SingleLine().Shrink(1)
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

				splitOrderRow := unfinished && orderIdx >= 0 && siteUnfinishedCount >= 2 && cur.Status == service.CacheQueueWaitingBackoff
				ui.Row(c).Shrink(0).Gap(4).AlignItems(ui.Center).Children(func() {
					if unfinished && orderIdx >= 0 && siteUnfinishedCount >= 2 && !splitOrderRow {
						if compactOrderButton(c, "置顶", "置顶缓存队列："+cur.ID, orderIdx == 0) {
							_, err := a.backend.MoveCacheQueue(cur.ID, service.QueueMoveTop)
							a.finishCacheQueueAction(err)
						}
						if compactOrderButton(c, "上移", "上移缓存队列："+cur.ID, orderIdx == 0) {
							_, err := a.backend.MoveCacheQueue(cur.ID, service.QueueMoveUp)
							a.finishCacheQueueAction(err)
						}
						if compactOrderButton(c, "下移", "下移缓存队列："+cur.ID, orderIdx >= siteUnfinishedCount-1) {
							_, err := a.backend.MoveCacheQueue(cur.ID, service.QueueMoveDown)
							a.finishCacheQueueAction(err)
						}
					}
					if unfinished {
						if cur.Status == service.CacheQueuePaused {
							if AccentPillButton(c, "继续", "继续缓存队列："+cur.ID).Clicked() {
								_, err := a.backend.ResumeCacheQueue(cur.ID)
								a.finishCacheQueueAction(err)
							}
						} else {
							if cur.Status == service.CacheQueueWaitingBackoff {
								if AccentPillButton(c, "立即重试", "立即重试缓存队列："+cur.ID).Clicked() {
									_, err := a.backend.ResumeCacheQueue(cur.ID)
									a.finishCacheQueueAction(err)
								}
							}
							if OutlinePillButton(c, "暂停", "暂停缓存队列："+cur.ID).Clicked() {
								_, err := a.backend.PauseCacheQueue(cur.ID)
								a.finishCacheQueueAction(err)
							}
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
			if unfinished && orderIdx >= 0 && siteUnfinishedCount >= 2 && cur.Status == service.CacheQueueWaitingBackoff {
				ui.Row(c).FillWidth().Justify(ui.End).AlignItems(ui.Center).Gap(4).Wrap().ClipX().Children(func() {
					if compactOrderButton(c, "置顶", "置顶缓存队列："+cur.ID, orderIdx == 0) {
						_, err := a.backend.MoveCacheQueue(cur.ID, service.QueueMoveTop)
						a.finishCacheQueueAction(err)
					}
					if compactOrderButton(c, "上移", "上移缓存队列："+cur.ID, orderIdx == 0) {
						_, err := a.backend.MoveCacheQueue(cur.ID, service.QueueMoveUp)
						a.finishCacheQueueAction(err)
					}
					if compactOrderButton(c, "下移", "下移缓存队列："+cur.ID, orderIdx >= siteUnfinishedCount-1) {
						_, err := a.backend.MoveCacheQueue(cur.ID, service.QueueMoveDown)
						a.finishCacheQueueAction(err)
					}
				})
			}

			if cur.StopReason != "" && !strings.Contains(formatQueueSidebarStatus(cur), cur.StopReason) {
				ui.Text(c, "原因："+cur.StopReason).FontSize(12).TextColor(ColorTextMuted).MaxLines(2)
			}
			if cur.Note != "" && cur.Note != cur.StopReason {
				ui.Text(c, cur.Note).FontSize(12).TextColor(ColorTextMuted).MaxLines(2)
			}
			if cur.Status == service.CacheQueueRunning && cur.ActiveTitle != "" {
				prefix := "当前正在缓存："
				if isBackfill {
					prefix = "当前正在补全："
				}
				ui.Text(c, prefix+cur.ActiveTitle).FontSize(12).FontWeight(600).TextColor(ColorAccent).SingleLine()
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
		Padding(6, 0, 0, 0).
		Gap(4).
		ClipX().
		Children(func() {
			ui.Text(c, "最近缓存的内容").FontSize(12).FontWeight(600).TextColor(ColorTextMain).SingleLine()
			if len(cur.RecentItems) == 0 {
				ui.Text(c, "还没有通过这个队列缓存完成的条目。已缓存或不支持的类型会自动跳过。").FontSize(12).TextColor(ColorTextMuted).MaxLines(2)
				return
			}
			for _, recent := range cur.RecentItems {
				ri := recent
				row := ui.ButtonBase(c).
					Label("队列最近缓存："+ri.ItemKey).
					FillWidth().
					Padding(4, 6).
					Radius(8).
					Gap(8).
					AlignItems(ui.Center).
					Justify(ui.Start).
					ClipX()
				if row.Hovered() {
					row.Background(ColorHover)
				}
				row.Children(func() {
					coverKey, coverRel := a.queueRecentCover(ri.ItemKey)
					a.renderMediaThumbEdge(c, coverKey, coverRel, avatarThumbEdge, ri.Title, "", "", false, 60, 36)
					ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
						ui.Text(c, firstNonEmptyStrUI(ri.Title, ri.ItemKey)).FontSize(13).FontWeight(500).TextColor(ColorTextMain).SingleLine()
						if ts := formatRFC3339Clock(ri.CachedAt, false); ts != "" {
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
// queue's recent list or downloads list: its cover or first image, else the
// cover of the video a dynamic refers to (cached per libraryRev, capped at 500
// entries, and queried via GetLibraryItemMetaOnly to avoid disk EvalSymlinks).
func (a *App) queueRecentCover(itemKey string) (string, string) {
	if itemKey == "" || a.backend == nil {
		return itemKey, ""
	}
	if a.queueCoversRev != a.libraryRev {
		a.queueCovers, a.queueCoversRev = nil, a.libraryRev
	}
	if got, ok := a.queueCovers[itemKey]; ok {
		return got[0], got[1]
	}
	if a.queueCovers == nil {
		a.queueCovers = make(map[string][2]string)
	} else if len(a.queueCovers) >= 500 {
		clear(a.queueCovers)
	}
	got := [2]string{itemKey, ""}
	if item, ok, err := a.backend.GetLibraryItemMetaOnly(itemKey); err == nil && ok {
		got[1] = libraryItemCoverRel(item)
		for _, vid := range item.Videos {
			if got[1] != "" {
				break
			}
			vKey := library.CanonicalItemKey(item.Site, "video", vid)
			if v, ok, err := a.backend.GetLibraryItemMetaOnly(vKey); err == nil && ok {
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
	if len(item.NotePages) > 0 {
		a.OpenDouyinNoteViewer(nil, &item, 0)
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
