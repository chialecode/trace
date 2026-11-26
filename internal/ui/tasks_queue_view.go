package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
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

// renderTasksQueueView lists every cache queue as a card like the download
// tasks; a card opens to show what that queue cached most recently (DEC-057).
func (a *App) renderTasksQueueView(c *ui.Context) {
	ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
		ui.Column(c).FillWidth().Padding(16).Gap(12).ClipX().Children(func() {
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
					a.renderMediaThumbEdge(c, coverKey, coverRel, avatarThumbEdge, ri.Title, "", "", false, 72, 44)
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
