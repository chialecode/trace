package ui

import (
	"context"
	"fmt"
	"strings"

	"github.com/chialecode/trace/internal/service"
	"github.com/egoist/mygo/ui"
)

const conflictProtectionNotice = "源站内容与本地已缓存版本不一致时不会自动覆盖，默认可同时保留本地旧版与源站新版"

var batchConflictChoices = []struct {
	value string
	text  string
}{
	{service.ConflictKeepBoth, "保留两版（推荐）"},
	{service.ConflictKeepLocal, "保留本地"},
	{service.ConflictUseRemote, "用新版替换"},
}

var batchConflictChoiceNames = []string{
	"保留两版（推荐）",
	"保留本地",
	"用新版替换",
}

func batchConflictChoiceText(choice string) string {
	for _, opt := range batchConflictChoices {
		if opt.value == choice {
			return opt.text
		}
	}
	return "保留两版（推荐）"
}

func (a *App) effectiveConflictBatchChoice() string {
	switch a.ConflictBatchChoice {
	case service.ConflictKeepBoth, service.ConflictKeepLocal, service.ConflictUseRemote:
		return a.ConflictBatchChoice
	default:
		return service.DefaultConflictChoice
	}
}

func (a *App) selectTasksConflicts() {
	a.TasksSection = "conflicts"
	a.closeLivePreview()
	a.refreshCacheConflicts()
	a.conflictsLoadedOnTasks = true
}

// OpenTasksConflicts navigates to /tasks and opens the 内容冲突 section.
func (a *App) OpenTasksConflicts() {
	a.Navigate(RouteTasks)
	a.selectTasksConflicts()
}

func (a *App) refreshCacheConflicts() {
	if a.backend == nil {
		a.CacheConflicts = nil
		a.CacheConflictPendingCount = 0
		a.ConflictBatchConfirmOpen = false
		a.ConflictReplaceConfirmID = ""
		return
	}
	if list, err := a.backend.ListCacheConflicts(); err == nil {
		a.CacheConflicts = list
	}
	a.CacheConflictPendingCount = a.backend.PendingCacheConflictCount()
	a.conflictsLoadedBackend = a.backend
	if a.ConflictReplaceConfirmID != "" {
		found := false
		for _, item := range a.CacheConflicts {
			if item.ID == a.ConflictReplaceConfirmID {
				found = true
				break
			}
		}
		if !found {
			a.ConflictReplaceConfirmID = ""
		}
	}
	if len(a.CacheConflicts) == 0 {
		a.ConflictBatchConfirmOpen = false
	}
}

func (a *App) effectiveConflictCount() int {
	if a.CacheConflictPendingCount > 0 {
		return a.CacheConflictPendingCount
	}
	n := 0
	for _, item := range a.CacheConflicts {
		if item.State != service.ConflictStateResolving {
			n++
		}
	}
	return n
}

func (a *App) conflictsNavText() string {
	if n := a.effectiveConflictCount(); n > 0 {
		return fmt.Sprintf("内容冲突（%d）", n)
	}
	return "内容冲突"
}

func (a *App) actionableConflictCount() int {
	n := 0
	for _, item := range a.CacheConflicts {
		if item.State != service.ConflictStateResolving && !a.isPending("conflict-resolve:"+item.ID) {
			n++
		}
	}
	return n
}

func conflictResolvedNotice(choice string) string {
	switch choice {
	case service.ConflictKeepBoth:
		return "已保留本地旧版并缓存源站新版"
	case service.ConflictKeepLocal:
		return "已保留本地版本，当前源站修订不再重复提示"
	case service.ConflictUseRemote:
		return "已用源站新版更新本地缓存"
	default:
		return ""
	}
}

func formatConflictBatchInfo(res service.ConflictBatchResult) string {
	var parts []string
	if res.Applied > 0 {
		parts = append(parts, fmt.Sprintf("已处理 %d 项内容冲突", res.Applied))
	}
	if res.Failed > 0 {
		parts = append(parts, fmt.Sprintf("%d 项未完成（本地原文件未改动）", res.Failed))
	}
	if res.SkippedAuth > 0 {
		parts = append(parts, fmt.Sprintf("%d 项需先登录对应站点", res.SkippedAuth))
	}
	if res.SkippedBusy > 0 {
		parts = append(parts, fmt.Sprintf("跳过 %d 项正在处理的条目", res.SkippedBusy))
	}
	return strings.Join(parts, "；")
}

func (a *App) startResolveConflict(id, choice string) {
	id = strings.TrimSpace(id)
	if a.backend == nil || id == "" {
		return
	}
	reqKey := "conflict-resolve:" + id
	if a.isPending(reqKey) || a.isPending("conflict-batch") {
		return
	}
	a.ConflictActionError = ""
	backend := a.backend
	requestGuarded(a, reqKey, nil, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, backend.ResolveCacheConflict(ctx, id, choice)
	}, func(struct{}) {
		a.ConflictActionError = ""
		a.BannerInfo = conflictResolvedNotice(choice)
		a.cacheStateRev++
		a.refreshTasks()
		a.refreshCacheConflicts()
	}, func(err error) {
		a.backend.LogUserError(err)
		a.cacheStateRev++
		a.refreshCacheConflicts()
		hasCardErr := false
		for _, item := range a.CacheConflicts {
			if item.ID == id && strings.TrimSpace(item.LastError) != "" {
				hasCardErr = true
				break
			}
		}
		if !hasCardErr {
			a.ConflictActionError = "冲突处理未完成：" + userErrorText(err)
		}
	})
}

func (a *App) startResolveAllConflicts(choice string) {
	if a.backend == nil {
		return
	}
	if choice == "" {
		choice = service.DefaultConflictChoice
	}
	if a.isPending("conflict-batch") {
		return
	}
	a.ConflictActionError = ""
	a.ConflictBatchConfirmOpen = false
	a.ConflictReplaceConfirmID = ""
	backend := a.backend
	requestGuarded(a, "conflict-batch", nil, func(ctx context.Context) (service.ConflictBatchResult, error) {
		return backend.ResolveAllCacheConflicts(ctx, choice)
	}, func(res service.ConflictBatchResult) {
		a.ConflictActionError = ""
		a.BannerInfo = formatConflictBatchInfo(res)
		a.cacheStateRev++
		a.refreshTasks()
		a.refreshCacheConflicts()
	}, func(err error) {
		a.backend.LogUserError(err)
		a.BannerInfo = ""
		a.ConflictActionError = "批量处理未完成：" + userErrorText(err)
		a.cacheStateRev++
		a.refreshCacheConflicts()
	})
}

func (a *App) openConflictLocalItem(itemKey string) {
	itemKey = strings.TrimSpace(itemKey)
	if itemKey == "" || a.backend == nil {
		return
	}
	item, ok, err := a.backend.GetLibraryItem(itemKey)
	if err != nil {
		a.backend.LogUserError(err)
		a.ConflictActionError = "无法打开本地条目：" + userErrorText(err)
		return
	}
	if !ok {
		if meta, metaOK, metaErr := a.backend.GetLibraryItemMetaOnly(itemKey); metaErr == nil && metaOK {
			item = meta
			ok = true
		}
	}
	if !ok {
		a.ConflictActionError = "未找到对应的本地已缓存条目"
		return
	}
	a.ConflictActionError = ""
	a.OpenLibraryItemDetail(item)
}

func (a *App) renderTasksConflictsView(c *ui.Context) {
	conflicts := a.CacheConflicts
	batchChoice := a.effectiveConflictBatchChoice()
	batchBusy := a.isPending("conflict-batch")
	actionable := a.actionableConflictCount()

	ui.Column(c).Grow(1).FillWidth().ClipX().Children(func() {
		ui.Column(c).
			Label("内容冲突顶部栏").
			FillWidth().
			Padding(16, 16, 8, 16).
			Gap(8).
			Shrink(0).
			ClipX().
			Children(func() {
				ui.Row(c).
					FillWidth().
					AlignItems(ui.Center).
					Justify(ui.SpaceBetween).
					Gap(8).
					Wrap().
					ClipX().
					Children(func() {
						ui.Box(c).Label("内容冲突保护说明").Shrink(1).ClipX().Children(func() {
							ui.Text(c, conflictProtectionNotice).
								FontSize(12).
								LineHeight(LineHeightCompact).
								TextColor(ColorTextMuted).
								MaxLines(2)
						})

						if len(conflicts) > 0 {
							ui.Row(c).AlignItems(ui.Center).Gap(8).Wrap().Shrink(0).ClipX().Children(func() {
								ui.Text(c, "全部待处理：").FontSize(13).TextColor(ColorTextMain).SingleLine()
								shown := batchConflictChoiceText(batchChoice)
								if ui.Select(c, &shown, batchConflictChoiceNames).Label("全部待处理方式").Changed() {
									for _, opt := range batchConflictChoices {
										if opt.text == shown {
											a.ConflictBatchChoice = opt.value
											break
										}
									}
									if a.effectiveConflictBatchChoice() != service.ConflictUseRemote {
										a.ConflictBatchConfirmOpen = false
									}
								}
								batchBtnText := "批量应用"
								if batchBusy {
									batchBtnText = "处理中…"
								}
								disabled := batchBusy || actionable == 0
								if AccentPillButton(c, batchBtnText, "批量应用冲突处理").Disabled(disabled).Clicked() && !disabled {
									if a.effectiveConflictBatchChoice() == service.ConflictUseRemote {
										a.ConflictReplaceConfirmID = ""
										a.ConflictBatchConfirmOpen = true
									} else {
										a.startResolveAllConflicts(a.effectiveConflictBatchChoice())
									}
								}
							})
						}
					})

				if len(conflicts) > 0 && a.ConflictBatchConfirmOpen && a.effectiveConflictBatchChoice() == service.ConflictUseRemote && !batchBusy {
					ui.Row(c).
						Label("确认批量替换冲突栏").
						FillWidth().
						Padding(8, 12).
						Radius(10).
						Background(ColorWarnBg).
						Border(1, ColorDotYellow).
						AlignItems(ui.Center).
						Justify(ui.SpaceBetween).
						Gap(8).
						Wrap().
						ClipX().
						Children(func() {
							ui.Text(c, "确认用源站新版批量替换全部待处理冲突的本地已缓存内容？").
								FontSize(12).
								FontWeight(600).
								TextColor(ColorTextMain).
								MaxLines(2).
								Grow(1).
								Shrink(1)
							ui.Row(c).Gap(6).AlignItems(ui.Center).Shrink(0).Children(func() {
								if OutlinePillButton(c, "取消", "取消批量替换冲突").Clicked() {
									a.ConflictBatchConfirmOpen = false
								}
								if DangerOutlinePillButton(c, "确认替换", "确认批量替换冲突").Clicked() {
									a.ConflictBatchConfirmOpen = false
									a.startResolveAllConflicts(service.ConflictUseRemote)
								}
							})
						})
				}

				if a.ConflictActionError != "" {
					renderErrorBanner(c, "冲突操作错误", a.ConflictActionError, func() {
						if OutlinePillButton(c, "关闭", "关闭冲突错误提示").Clicked() {
							a.ConflictActionError = ""
						}
					})
				}
			})

		if len(conflicts) == 0 {
			ui.Column(c).Label("内容冲突空状态").FillWidth().Padding(40, 16).AlignItems(ui.Center).Gap(8).Children(func() {
				ui.Text(c, "当前没有待处理的内容冲突").FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
				ui.Text(c, "当全部缓存或更新检查发现源站版本与本机已缓存内容不一致时，会在这里等你决定。").
					FontSize(13).
					LineHeight(LineHeightCompact).
					TextColor(ColorTextMuted).
					MaxLines(2)
			})
			return
		}

		ui.Scroll(c).Label("内容冲突列表").Grow(1).FillWidth().ClipX().Children(func() {
			ui.Column(c).FillWidth().Padding(2, 16, 12, 16).Gap(8).ClipX().Children(func() {
				for _, item := range conflicts {
					a.renderCacheConflictCard(c, item)
				}
			})
		})
	})
}

func (a *App) renderCacheConflictCard(c *ui.Context, conflict service.CacheConflictDTO) {
	siteID := normalizeSiteIDUI(conflict.Site)
	reasonText := strings.TrimSpace(conflict.ReasonLabel)
	if reasonText == "" {
		reasonText = service.ConflictReasonLabel(conflict.ReasonKind)
	}
	title := firstNonEmptyStrUI(conflict.Title, conflict.TargetID, conflict.ItemKey)
	resolving := conflict.State == service.ConflictStateResolving ||
		a.isPending("conflict-resolve:"+conflict.ID) ||
		a.isPending("conflict-batch")

	ui.Column(c).
		Label("内容冲突卡片："+conflict.ID).
		FillWidth().
		Padding(14).
		Radius(16).
		Background(ColorBgMain).
		Border(1, ColorBorder).
		Gap(8).
		ClipX().
		Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(12).ClipX().Children(func() {
				// Strictly offline local thumbnail via GetLibraryItemMetaOnly + MediaPath (0 network calls).
				coverKey, coverRel := a.queueRecentCover(conflict.ItemKey)
				a.renderMediaThumbEdge(c, coverKey, coverRel, avatarThumbEdge, title, "", "", false, 74, 46)

				ui.Column(c).Grow(1).Shrink(1).Gap(4).ClipX().Children(func() {
					ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
						ui.Box(c).
							Label("冲突原因："+conflict.ID).
							Padding(2, 7).
							Radius(6).
							Background(ColorWarnBg).
							Border(1, ui.RGBA(243, 161, 21, 0.35)).
							Shrink(0).
							Children(func() {
								ui.Text(c, reasonText).FontSize(11).FontWeight(600).TextColor(ColorTextMain).SingleLine()
							})
						ui.Text(c, title).
							FontSize(14).
							FontWeight(600).
							TextColor(ColorTextMain).
							SingleLine().
							Shrink(1)
						renderSiteSmallBadgeWithDot(c, siteID, "冲突站点："+conflict.ID)
					})

					var metaParts []string
					if author := firstNonEmptyStrUI(conflict.AuthorName, conflict.AuthorUID); author != "" {
						metaParts = append(metaParts, author)
					}
					if ts := formatRFC3339Clock(conflict.DetectedAt, false); ts != "" {
						metaParts = append(metaParts, "发现于 "+ts)
					} else if strings.TrimSpace(conflict.DetectedAt) != "" {
						metaParts = append(metaParts, "发现于 "+strings.TrimSpace(conflict.DetectedAt))
					}
					if conflict.TargetID != "" && conflict.Title != "" && !strings.Contains(conflict.Title, conflict.TargetID) {
						metaParts = append(metaParts, conflict.TargetID)
					}
					if len(metaParts) > 0 {
						ui.Text(c, strings.Join(metaParts, " · ")).
							FontSize(12).
							TextColor(ColorTextMuted).
							SingleLine().
							Shrink(1)
					}
				})
			})

			localSum := firstNonEmptyStrUI(conflict.LocalSummary, "本机已缓存完整版本")
			remoteSum := firstNonEmptyStrUI(conflict.RemoteSummary, "源站当前修订版本")
			ui.Column(c).
				Label("冲突对比："+conflict.ID).
				FillWidth().
				Padding(8, 10).
				Radius(10).
				Background(ColorBgSecondary).
				Gap(4).
				ClipX().
				Children(func() {
					ui.Text(c, "本地已缓存："+localSum).
						FontSize(12).
						LineHeight(LineHeightCompact).
						TextColor(ColorTextMain).
						MaxLines(2)
					ui.Text(c, "源站当前："+remoteSum).
						FontSize(12).
						LineHeight(LineHeightCompact).
						TextColor(ColorTextMuted).
						MaxLines(2)
				})

			if conflict.State == service.ConflictStateFailed || strings.TrimSpace(conflict.LastError) != "" {
				errMsg := firstNonEmptyStrUI(conflict.LastError, "上次处理未完成（本地原文件未改动）")
				ui.Row(c).
					Label("冲突失败提示："+conflict.ID).
					FillWidth().
					Padding(6, 10).
					Radius(8).
					Background(ColorErrorBg).
					Border(1, ui.RGBA(180, 35, 24, 0.22)).
					AlignItems(ui.Center).
					Gap(6).
					ClipX().
					Children(func() {
						DrawIcon(c, "alert-circle", 14, ColorError, 1.8)
						ui.Text(c, errMsg).
							FontSize(12).
							LineHeight(LineHeightCompact).
							TextColor(ColorError).
							MaxLines(2).
							Grow(1).
							Shrink(1)
					})
			}

			if a.ConflictReplaceConfirmID == conflict.ID && !resolving {
				ui.Row(c).
					Label("确认替换冲突栏："+conflict.ID).
					FillWidth().
					Padding(8, 12).
					Radius(10).
					Background(ColorWarnBg).
					Border(1, ColorDotYellow).
					AlignItems(ui.Center).
					Justify(ui.SpaceBetween).
					Gap(8).
					Wrap().
					ClipX().
					Children(func() {
						ui.Text(c, "确认用源站新版替换本地已缓存内容？").
							FontSize(12).
							FontWeight(600).
							TextColor(ColorTextMain).
							MaxLines(2).
							Grow(1).
							Shrink(1)
						ui.Row(c).Gap(6).AlignItems(ui.Center).Shrink(0).Children(func() {
							if OutlinePillButton(c, "取消", "取消替换冲突："+conflict.ID).Clicked() {
								a.ConflictReplaceConfirmID = ""
							}
							if DangerOutlinePillButton(c, "确认替换", "确认替换冲突："+conflict.ID).Clicked() {
								a.ConflictReplaceConfirmID = ""
								a.startResolveConflict(conflict.ID, service.ConflictUseRemote)
							}
						})
					})
			}

			ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Wrap().ClipX().Children(func() {
				keepBothText := "保留两版"
				if resolving {
					keepBothText = "处理中…"
				}
				if AccentPillButton(c, keepBothText, "保留两版："+conflict.ID).Disabled(resolving).Clicked() && !resolving {
					if a.ConflictReplaceConfirmID == conflict.ID {
						a.ConflictReplaceConfirmID = ""
					}
					a.startResolveConflict(conflict.ID, service.ConflictKeepBoth)
				}
				if OutlinePillButton(c, "保留本地", "保留本地："+conflict.ID).Disabled(resolving).Clicked() && !resolving {
					if a.ConflictReplaceConfirmID == conflict.ID {
						a.ConflictReplaceConfirmID = ""
					}
					a.startResolveConflict(conflict.ID, service.ConflictKeepLocal)
				}
				if DangerOutlinePillButton(c, "用新版替换", "用新版替换："+conflict.ID).Disabled(resolving).Clicked() && !resolving {
					a.ConflictBatchConfirmOpen = false
					a.ConflictReplaceConfirmID = conflict.ID
				}
				localDisabled := resolving || strings.TrimSpace(conflict.ItemKey) == ""
				if OutlinePillButton(c, "查看本地", "查看本地冲突条目："+conflict.ID).Disabled(localDisabled).Clicked() && !localDisabled {
					a.openConflictLocalItem(conflict.ItemKey)
				}
			})
		})
}
