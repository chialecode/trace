package ui

import (
	"fmt"
	"slices"
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

// SubLinksModalState holds the modal state for adding, editing, and removing
// custom external links attached to a subscription (DEC-074).
type SubLinksModalState struct {
	Open          bool
	SubID         string
	SubName       string
	Links         []subscription.CustomLink
	NewLabel      string
	NewURL        string
	NewComposing  bool
	EditIndex     int
	EditLabel     string
	EditURL       string
	EditComposing bool
	Error         string
}

// OpenSubLinksModal opens the custom external links management dialog for sub.
func (a *App) OpenSubLinksModal(sub subscription.Subscription) {
	a.openSubLinksModal(sub)
}

func (a *App) openSubLinksModal(sub subscription.Subscription) {
	a.SubManageModalMode = ""
	a.SubLinks = SubLinksModalState{
		Open:      true,
		SubID:     sub.ID,
		SubName:   firstNonEmptyStrUI(sub.Name, sub.Uname, sub.ID),
		Links:     slices.Clone(sub.Links),
		EditIndex: -1,
	}
}

func (a *App) closeSubLinksModal() {
	a.SubLinks.Open = false
	a.SubLinks.Error = ""
	a.SubLinks.NewComposing = false
	a.SubLinks.EditComposing = false
	a.SubLinks.EditIndex = -1
}

func (a *App) persistSubLinksModal(next []subscription.CustomLink, successBanner string) bool {
	if a.backend == nil {
		return false
	}
	norm, err := subscription.NormalizeAndValidateCustomLinks(next)
	if err != nil {
		a.SubLinks.Error = userErrorText(err)
		return false
	}
	updated, err := a.backend.SetSubscriptionLinks(a.SubLinks.SubID, norm)
	if err != nil {
		a.SubLinks.Error = userErrorText(err)
		return false
	}
	a.SubLinks.Links = slices.Clone(updated.Links)
	a.SubLinks.Error = ""
	if successBanner != "" {
		a.BannerInfo = successBanner
	}
	a.RefreshData()
	return true
}

func (a *App) addSubCustomLink() {
	st := &a.SubLinks
	norm, err := subscription.NormalizeAndValidateCustomLink(subscription.CustomLink{
		Label: st.NewLabel,
		URL:   st.NewURL,
	})
	if err != nil {
		st.Error = userErrorText(err)
		return
	}
	for _, existing := range st.Links {
		if existing.URL == norm.URL {
			st.Error = "该链接已存在"
			return
		}
	}
	if len(st.Links) >= subscription.MaxSubscriptionCustomLinks {
		st.Error = fmt.Sprintf("一个订阅最多添加 %d 个自定义链接", subscription.MaxSubscriptionCustomLinks)
		return
	}
	next := append(slices.Clone(st.Links), norm)
	if a.persistSubLinksModal(next, "已添加自定义链接") {
		st.NewLabel = ""
		st.NewURL = ""
		st.NewComposing = false
	}
}

func (a *App) saveEditedSubCustomLink() {
	st := &a.SubLinks
	if st.EditIndex < 0 || st.EditIndex >= len(st.Links) {
		st.EditIndex = -1
		return
	}
	norm, err := subscription.NormalizeAndValidateCustomLink(subscription.CustomLink{
		Label: st.EditLabel,
		URL:   st.EditURL,
	})
	if err != nil {
		st.Error = userErrorText(err)
		return
	}
	for i, existing := range st.Links {
		if i != st.EditIndex && existing.URL == norm.URL {
			st.Error = "该链接已存在"
			return
		}
	}
	next := slices.Clone(st.Links)
	next[st.EditIndex] = norm
	if a.persistSubLinksModal(next, "已更新自定义链接") {
		st.EditIndex = -1
		st.EditLabel = ""
		st.EditURL = ""
		st.EditComposing = false
	}
}

func (a *App) removeSubCustomLink(idx int) {
	st := &a.SubLinks
	if idx < 0 || idx >= len(st.Links) {
		return
	}
	next := make([]subscription.CustomLink, 0, len(st.Links)-1)
	next = append(next, st.Links[:idx]...)
	next = append(next, st.Links[idx+1:]...)
	if a.persistSubLinksModal(next, "已移除自定义链接") {
		if st.EditIndex == idx {
			st.EditIndex = -1
			st.EditLabel = ""
			st.EditURL = ""
			st.EditComposing = false
		} else if st.EditIndex > idx {
			st.EditIndex--
		}
	}
}

func (a *App) renderSubLinksOverlay(c *ui.Context, w, h float32) {
	if !a.SubLinks.Open {
		return
	}
	cardW := min(float32(520), max(float32(320), w-40))
	maxListH := max(float32(140), min(float32(280), h-360))
	st := &a.SubLinks

	renderDialog(c, &a.SubLinks.Open,
		"关闭自定义链接对话框（点击空白处）", "订阅自定义链接对话框",
		cardW, 0, a.closeSubLinksModal,
		func() {
			ui.Column(c).FillWidth().Gap(4).ClipX().Children(func() {
				ui.Text(c, "自定义链接").FontSize(18).FontWeight(600).TextColor(ColorTextMain).SingleLine()
				ui.Text(c, fmt.Sprintf("为「%s」保存聚合页或个人主页（如 link3.cc、linktr.ee，仅支持 http:// 或 https://）。", st.SubName)).
					FontSize(13).LineHeight(LineHeightCompact).TextColor(ColorTextMuted).MaxLines(2)
			})

			if len(st.Links) == 0 {
				ui.Column(c).
					FillWidth().
					Padding(18, 12).
					Radius(12).
					Background(ColorBgMain).
					Border(1, ColorBorder).
					AlignItems(ui.Center).
					Children(func() {
						ui.Text(c, "还没有添加自定义链接").FontSize(13).TextColor(ColorTextMuted).SingleLine()
					})
			} else {
				linksSnap := slices.Clone(st.Links)
				ui.Scroll(c).FillWidth().MaxHeight(maxListH).ClipX().Children(func() {
					ui.Column(c).FillWidth().Gap(8).ClipX().Children(func() {
						for idx, lnkItem := range linksSnap {
							i := idx
							lnk := lnkItem
							if st.EditIndex == i {
								ui.Column(c).
									FillWidth().
									Padding(10, 12).
									Radius(12).
									Background(ColorBgMain).
									Border(1, ColorAccent).
									Gap(8).
									ClipX().
									Children(func() {
										editLabelInp := ui.TextInput(c, &st.EditLabel).
											Label("编辑订阅链接名称").
											Placeholder("显示名称（可选，留空自动取域名与路径）").
											FillWidth()
										editURLInp := ui.TextInput(c, &st.EditURL).
											Label("编辑订阅链接地址").
											Placeholder("https://link3.cc/...").
											FillWidth()
										wasEditComposing := st.EditComposing
										st.EditComposing = editLabelInp.Composing() || editURLInp.Composing()
										submitEdit := (editLabelInp.Submitted() || editURLInp.Submitted()) && !st.EditComposing && !wasEditComposing
										ui.Row(c).FillWidth().Justify(ui.End).Gap(8).Children(func() {
											if OutlinePillButton(c, "取消", "取消编辑订阅链接").Clicked() {
												st.EditIndex = -1
												st.EditLabel = ""
												st.EditURL = ""
												st.EditComposing = false
												st.Error = ""
											}
											if OutlinePillButton(c, "保存", "保存编辑订阅链接").Clicked() || submitEdit {
												a.saveEditedSubCustomLink()
											}
										})
									})
								continue
							}

							display := lnk.DisplayLabel()
							ui.Row(c).
								FillWidth().
								Padding(8, 12).
								Radius(12).
								Background(ColorBgMain).
								Border(1, ColorBorder).
								AlignItems(ui.Center).
								Justify(ui.SpaceBetween).
								Gap(8).
								ClipX().
								Children(func() {
									openBtn := ui.ButtonBase(c).
										Label(fmt.Sprintf("对话框打开订阅链接 %d", i+1)).
										Tooltip(lnk.URL).
										Grow(1).
										Shrink(1).
										Padding(2, 0).
										Gap(8).
										AlignItems(ui.Center).
										Justify(ui.Start).
										ClipX()
									openBtn.Children(func() {
										DrawIcon(c, "external-link", 14, ColorAccent, 1.75)
										ui.Column(c).Grow(1).Shrink(1).Gap(2).ClipX().Children(func() {
											ui.Text(c, display).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
											ui.Text(c, lnk.URL).FontSize(12).TextColor(ColorTextMuted).SingleLine()
										})
									})
									if openBtn.Clicked() {
										a.openSubscriptionCustomLink(lnk.URL)
									}

									ui.Row(c).Gap(6).AlignItems(ui.Center).Shrink(0).Children(func() {
										if OutlinePillButton(c, "编辑", fmt.Sprintf("编辑订阅链接 %d", i+1)).Clicked() {
											st.EditIndex = i
											st.EditLabel = lnk.Label
											st.EditURL = lnk.URL
											st.EditComposing = false
											st.Error = ""
										}
										if DangerOutlinePillButton(c, "删除", fmt.Sprintf("删除订阅链接 %d", i+1)).Clicked() {
											a.removeSubCustomLink(i)
										}
									})
								})
						}
					})
				})
			}

			ui.Column(c).
				FillWidth().
				Padding(12).
				Radius(12).
				Background(ColorBgMain).
				Border(1, ColorBorder).
				Gap(8).
				ClipX().
				Children(func() {
					ui.Text(c, "添加链接").FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					newLabelInp := ui.TextInput(c, &st.NewLabel).
						Label("新订阅链接名称").
						Placeholder("显示名称（可选，如 Link3 / 个人主页；留空自动生成）").
						FillWidth()
					var urlComposing, urlSubmitted, addClicked bool
					ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).ClipX().Children(func() {
						newURLInp := ui.TextInput(c, &st.NewURL).
							Label("新订阅链接地址").
							Placeholder("https://link3.cc/...").
							Grow(1)
						urlComposing = newURLInp.Composing()
						urlSubmitted = newURLInp.Submitted()
						addClicked = AccentPillButton(c, "添加", "确认添加订阅链接").Clicked()
					})
					wasNewComposing := st.NewComposing
					st.NewComposing = newLabelInp.Composing() || urlComposing
					if addClicked || ((newLabelInp.Submitted() || urlSubmitted) && !st.NewComposing && !wasNewComposing) {
						a.addSubCustomLink()
					}
				})

			if st.Error != "" {
				renderErrorBanner(c, "订阅自定义链接错误", st.Error, nil)
			}

			ui.Row(c).FillWidth().Justify(ui.End).Gap(8).Children(func() {
				if OutlinePillButton(c, "关闭", "关闭自定义链接对话框").Clicked() {
					a.closeSubLinksModal()
				}
			})
		})
}
