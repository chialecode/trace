package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

func (a *App) closeAddSubOverlay() {
	a.AddSubPreview = nil
	a.AddSubPreviewOwner = nil
	a.AddSubDialogOpen = false
}

func (a *App) renderAddSubOverlay(c *ui.Context, w, h float32) {
	cardW := min(float32(460), max(float32(320), w-40))
	renderDialog(c, &a.AddSubDialogOpen,
		"关闭添加订阅对话框（点击空白处）", "添加订阅对话框",
		cardW, 0, a.closeAddSubOverlay,
		func() {
			ui.Text(c, "添加订阅").FontSize(18).FontWeight(600).TextColor(ColorTextMain).SingleLine()
			ui.Text(c, "建立一个订阅并可选关联哔哩哔哩、抖音、微博或 X 账号（主页链接、分享文本或 UID）。").FontSize(13).TextColor(ColorTextMuted).MaxLines(2)

			ui.TextInput(c, &a.AddSubName).
				Label("新订阅名称").
				Placeholder("订阅名称（必填，≤ 64 字）").
				FillWidth()

			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.TextInput(c, &a.AddSubInput).
					Label("添加订阅UID输入框").
					Placeholder("关联账号：粘贴主页链接、分享文本或 UID（可选）").
					Grow(1)
				if OutlinePillButton(c, "预览账号", "预览新订阅账号").Clicked() && strings.TrimSpace(a.AddSubInput) != "" {
					a.previewNewAccount()
				}
			})

			if a.AddSubPreview != nil {
				acc := *a.AddSubPreview
				accSite := normalizeSiteIDUI(acc.Site)
				var previewMid int64
				if accSite == "bilibili" {
					_, _ = fmt.Sscanf(acc.UID, "%d", &previewMid)
				}
				ui.Column(c).FillWidth().Padding(10, 12).Radius(12).Background(ColorBgMain).Border(1, ColorBorder).Gap(6).Children(func() {
					ui.Row(c).AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
						a.renderAccountAvatar(c, accSite, acc.Name, acc.UID, previewMid, 32, false, "", nil, acc.Face)
						ui.Text(c, firstNonEmptyStrUI(acc.Name, acc.UID)).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine().Shrink(1)
						renderSiteSmallBadgeWithDot(c, accSite, "新增订阅预览站点")
						ui.Text(c, "· "+formatSubscriptionAccountIdent(acc)).FontSize(12).TextColor(ColorTextMuted).SingleLine().Shrink(1)
					})
					if a.AddSubPreviewOwner == nil && len(a.Subscriptions) > 0 && OutlinePillButton(c, "加入现有订阅…", "预览账号加入现有订阅").Clicked() {
						a.AddSubInput, a.AddSubName, a.AddSubPreviewInput = "", "", ""
						a.closeAddSubOverlay()
						a.OpenSubscribePicker(acc)
					}
					if a.AddSubPreviewOwner != nil {
						ui.Text(c, fmt.Sprintf("该账号已属于订阅「%s」，可点击下方按钮移到新订阅。", a.AddSubPreviewOwner.Name)).
							FontSize(12).TextColor(ColorDotYellow).MaxLines(2)
						if AccentOutlinePillButton(c, "创建并移到新订阅", "创建并移到新订阅").Clicked() {
							name := clampRunes(firstNonEmptyStrUI(a.AddSubName, acc.Name, acc.UID), 64)
							created, err := a.backend.CreateSubscription(name, "", nil, subscription.DefaultSubRules())
							if err != nil {
								a.showError(err)
							} else {
								moved, moveErr := a.backend.MoveSubscriptionAccount(acc.Site, acc.UID, created.ID)
								if moveErr != nil {
									_ = a.backend.DeleteSubscription(created.ID)
									a.showError(moveErr)
								} else {
									a.syncSelectedSubRules(moved)
									a.AddSubInput = ""
									a.AddSubName = ""
									a.closeAddSubOverlay()
									a.RefreshData()
								}
							}
						}
					}
				})
			}

			ui.Row(c).FillWidth().Justify(ui.End).Gap(8).Children(func() {
				if OutlinePillButton(c, "取消", "取消添加订阅").Clicked() {
					a.closeAddSubOverlay()
				}
				createText := "确认添加"
				if a.AddSubPreviewOwner != nil {
					createText = "创建并移入账号"
				}
				if AccentPillButton(c, createText, "确认添加订阅").Clicked() {
					a.createPreviewedSubscription()
				}
			})
		})
}

func (a *App) renderImportSubOverlay(c *ui.Context, w, h float32) {
	cardW := min(float32(520), max(float32(320), w-40))
	cardH := max(float32(320), h-90)
	st := a.fs()
	if st.importFollowingsListState == nil {
		st.importFollowingsListState = &ui.ListState{}
	}
	curSite := normalizeImportSite(a.ImportSite)
	siteLoggedIn := a.backend.StatusOf(curSite).LoggedIn

	renderDialog(c, &a.ImportSubModalOpen,
		"关闭关注导入对话框（点击空白处）", "关注导入对话框",
		cardW, cardH, nil,
		func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
				ui.Text(c, "从关注列表导入").FontSize(18).FontWeight(600).TextColor(ColorTextMain).SingleLine()
				renderSegmentedPills(c, curSite, []segmentedChoice{
					{Key: string(site.Bilibili), Text: "哔哩哔哩", Label: "关注导入站点：哔哩哔哩"},
					{Key: string(site.Weibo), Text: "微博", Label: "关注导入站点：微博"},
					{Key: string(site.X), Text: "X", Label: "关注导入站点：X"},
				}, func(next string) {
					if next != curSite {
						a.selectImportSite(next)
					}
				})
			})

			if !siteLoggedIn {
				prompt := "请先登录" + siteFullLabel(curSite)
				if curSite == string(site.X) {
					prompt = "请先登录 X"
				}
				ui.Column(c).Grow(1).FillWidth().Padding(32, 16).AlignItems(ui.Center).Justify(ui.Center).Gap(12).Children(func() {
					ui.Text(c, prompt).Label("关注导入未登录提示").FontSize(14).TextColor(ColorTextMuted).SingleLine()
					if AccentPillButton(c, "去登录", "关注导入去登录").Clicked() {
						a.ImportSubModalOpen = false
						a.OpenLoginForSite(curSite)
					}
				})
				ui.Row(c).FillWidth().Justify(ui.End).Children(func() {
					if OutlinePillButton(c, "关闭", "取消导入关注").Clicked() {
						a.ImportSubModalOpen = false
					}
				})
				return
			}

			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				if len(a.ImportGroupsDTO) > 0 {
					groups := []string{"全部关注"}
					for _, group := range a.ImportGroupsDTO {
						groups = append(groups, group.Name)
					}
					if ui.Select(c, &a.ImportGroup, groups).Label("关注分组").Changed() {
						a.loadImportPage(true)
					}
				}
				inp := ui.TextInput(c, &a.ImportSearch).
					Placeholder("搜索已加载的关注昵称或签名…").
					Label("关注导入搜索框").
					Grow(1)
				if !inp.Composing() {
					st.importSearchApplied = a.ImportSearch
				}
			})

			if a.ImportError != "" {
				renderErrorBanner(c, "", a.ImportError, func() {
					if OutlinePillButton(c, "重试", "重试关注列表").Clicked() {
						a.loadImportPage(a.ImportPage == 0)
					}
				})
			}

			if a.ImportSelectedKeys == nil {
				a.ImportSelectedKeys = make(map[string]bool)
			}
			if a.ImportSelectedMids == nil {
				a.ImportSelectedMids = make(map[int64]bool)
			}

			kw := strings.ToLower(strings.TrimSpace(st.importSearchApplied))
			visible := make([]service.FollowingCreatorDTO, 0, len(a.ImportCreators))
			for _, u := range a.ImportCreators {
				if kw != "" && !strings.Contains(strings.ToLower(u.Name), kw) && !strings.Contains(strings.ToLower(u.Sign), kw) && !strings.Contains(strings.ToLower(u.UniqueID), kw) {
					continue
				}
				visible = append(visible, u)
			}

			isOwned := func(u service.FollowingCreatorDTO) (string, bool) {
				if u.Subscribed {
					return firstNonEmptyStrUI(u.OwnerSubName, "已有订阅"), true
				}
				var mid int64
				if u.Site == string(site.Bilibili) {
					mid, _ = strconv.ParseInt(u.UID, 10, 64)
				}
				if sub, ok := a.ownerOfAccountInMemory(u.Site, u.UID, mid); ok {
					return firstNonEmptyStrUI(sub.Name, sub.ID), true
				}
				return "", false
			}

			totalRows := len(visible)
			if a.ImportHasMore {
				totalRows++
			}
			st.importFollowingsListState.Key = func(i int) any {
				if i >= 0 && i < len(visible) {
					return "imp:" + visible[i].Site + ":" + visible[i].UID
				}
				return "imp-more"
			}

			ui.List(c, st.importFollowingsListState, totalRows, func(i int) {
				if i >= len(visible) {
					ui.Row(c).FillWidth().Padding(8, 0).Justify(ui.Center).Children(func() {
						if OutlinePillButton(c, "加载下一页关注", "加载下一页关注").Clicked() {
							a.loadImportPage(false)
						}
					})
					return
				}
				u := visible[i]
				uSite := normalizeSiteIDUI(firstNonEmptyStrUI(u.Site, curSite))
				uKey := uSite + ":" + u.UID
				var mid int64
				if uSite == string(site.Bilibili) {
					mid, _ = strconv.ParseInt(u.UID, 10, 64)
				}
				ownerName, owned := isOwned(u)
				selected := a.ImportSelectedKeys[uKey]
				if !selected && uSite == string(site.Bilibili) && mid > 0 {
					selected = a.ImportSelectedMids[mid]
				}
				ui.Box(c).FillWidth().Padding(0, 0, 6, 0).ClipX().Children(func() {
					ui.Row(c).
						FillWidth().
						Padding(8, 10).
						Radius(10).
						Background(ColorBgMain).
						Border(1, ColorBorder).
						AlignItems(ui.Center).
						Gap(10).
						ClipX().
						Children(func() {
							a.renderAccountAvatar(c, uSite, u.Name, u.UID, mid, 34, false, "", nil, u.Face)
							ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
								ui.Row(c).AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
									ui.Text(c, firstNonEmptyStrUI(u.Name, u.UniqueID, u.UID)).FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine().Shrink(1)
									if uSite == string(site.X) && strings.TrimSpace(u.UniqueID) != "" {
										ui.Text(c, "@"+strings.TrimPrefix(strings.TrimSpace(u.UniqueID), "@")).FontSize(12).TextColor(ColorTextMuted).SingleLine().Shrink(0)
									}
								})
								if u.Sign != "" {
									ui.Text(c, u.Sign).FontSize(12).TextColor(ColorTextMuted).SingleLine()
								}
							})
							if owned {
								ui.Text(c, fmt.Sprintf("已在订阅「%s」中", ownerName)).FontSize(12).TextColor(ColorTextMuted).SingleLine().Shrink(0)
							} else if ui.Switch(c, &selected).Label("勾选导入 " + u.UID).Changed() {
								a.ImportSelectedKeys[uKey] = selected
								if uSite == string(site.Bilibili) && mid > 0 {
									a.ImportSelectedMids[mid] = selected
								}
							}
						})
				})
			}).Label("关注导入虚拟列表").Grow(1).FillWidth()

			selectedCount := 0
			for _, u := range a.ImportCreators {
				uSite := normalizeSiteIDUI(firstNonEmptyStrUI(u.Site, curSite))
				uKey := uSite + ":" + u.UID
				var mid int64
				if uSite == string(site.Bilibili) {
					mid, _ = strconv.ParseInt(u.UID, 10, 64)
				}
				if _, owned := isOwned(u); owned {
					continue
				}
				if a.ImportSelectedKeys[uKey] || (uSite == string(site.Bilibili) && mid > 0 && a.ImportSelectedMids[mid]) {
					selectedCount++
				}
			}
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
				ui.Text(c, fmt.Sprintf("已选 %d 个", selectedCount)).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
				ui.Row(c).Gap(8).Children(func() {
					if OutlinePillButton(c, "取消", "取消导入关注").Clicked() {
						a.ImportSubModalOpen = false
					}
					if OutlinePillButton(c, "全选已加载", "全选已加载").Clicked() {
						for _, u := range visible {
							if _, owned := isOwned(u); !owned {
								uSite := normalizeSiteIDUI(firstNonEmptyStrUI(u.Site, curSite))
								a.ImportSelectedKeys[uSite+":"+u.UID] = true
								if uSite == string(site.Bilibili) {
									if mid, err := strconv.ParseInt(u.UID, 10, 64); err == nil && mid > 0 {
										a.ImportSelectedMids[mid] = true
									}
								}
							}
						}
					}
					if AccentPillButton(c, fmt.Sprintf("导入 %d 个订阅", selectedCount), "确认导入关注").Disabled(selectedCount == 0).Clicked() && selectedCount > 0 {
						var chosen []service.FollowingCreatorDTO
						for _, u := range a.ImportCreators {
							uSite := normalizeSiteIDUI(firstNonEmptyStrUI(u.Site, curSite))
							uKey := uSite + ":" + u.UID
							var mid int64
							if uSite == string(site.Bilibili) {
								mid, _ = strconv.ParseInt(u.UID, 10, 64)
							}
							if a.ImportSelectedKeys[uKey] || (uSite == string(site.Bilibili) && mid > 0 && a.ImportSelectedMids[mid]) {
								chosen = append(chosen, u)
							}
						}
						res, err := a.backend.ImportFollowingCreators(curSite, chosen, subscription.DefaultSubRules())
						a.showError(err)
						if err == nil {
							a.BannerInfo = fmt.Sprintf("已建立 %d 个订阅", res.Created)
							a.ImportSubModalOpen = false
							a.RefreshData()
						}
					}
				})
			})
		})
}
