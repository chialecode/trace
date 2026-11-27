package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/egoist/mygo/ui"
)

func (a *App) lookupCompactLibEntry(itemKey string) (compactLibEntry, bool) {
	if itemKey == "" {
		return compactLibEntry{}, false
	}
	if a.compactLibCache == nil || a.compactLibCacheRev != a.cacheStateRev || len(a.compactLibCache) > 600 {
		a.compactLibCache = make(map[string]compactLibEntry)
		a.compactLibCacheRev = a.cacheStateRev
	}
	if e, ok := a.compactLibCache[itemKey]; ok {
		return e, e.item != nil
	}
	var entry compactLibEntry
	if it, ok, err := a.backend.GetLibraryItem(itemKey); err == nil && ok {
		ds := library.ComputeDotState(it)
		var relCover string
		for _, m := range it.Media {
			if m.Role == "cover" && relCover == "" {
				relCover = m.Path
			}
		}
		itemCopy := it
		entry = compactLibEntry{
			dotStatus: ds.Dot,
			dotLabel:  ds.Label,
			relCover:  relCover,
			item:      &itemCopy,
		}
	}
	a.compactLibCache[itemKey] = entry
	return entry, entry.item != nil
}

// renderVideoRoute draws /video (and Bilibili items under /library/item):
// video & dynamic details, multi-part selection, and quality upgrade (docs/ui.md §3.4).
func (a *App) renderVideoRoute(c *ui.Context) {
	ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
		ui.Column(c).FillWidth().Padding(16).Gap(14).ClipX().Children(func() {
			if a.Opening != nil {
				a.renderOpening(c)
				return
			}
			if a.CurrentLibraryDetail != nil {
				switch a.CurrentLibraryDetail.Kind {
				case "dynamic":
					a.renderPostCard(c, a.cachedDetailPost(*a.CurrentLibraryDetail))
					return
				case "live":
					a.renderRecordingDetail(c, *a.CurrentLibraryDetail)
					return
				}
			}
			if a.CurrentVideo != nil {
				vd := a.CurrentVideo
				itemKey := firstNonEmptyStrUI(a.CurrentVideoItemKey, library.CanonicalItemKey("bilibili", "video", vd.BVID))
				var cachedItem *library.ItemMetadata
				dotStatus := ""
				dotLabel := ""
				var relCover string
				if entry, ok := a.lookupCompactLibEntry(itemKey); ok {
					cachedItem = entry.item
					itemKey = cachedItem.ItemKey
					dotStatus = entry.dotStatus
					dotLabel = entry.dotLabel
					relCover = entry.relCover
				}

				offline := a.CurrentRoute == RouteLibraryItem
				// 1. Creator Row + Cache Button + Status Dot (docs/ui.md §3.4)
				ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(10).ClipX().Children(func() {
					ui.Row(c).AlignItems(ui.Center).Gap(10).Grow(1).ClipX().Children(func() {
						var openAuthor func()
						if !offline {
							openAuthor = func() {
								a.OpenUser(vd.Author.Mid, vd.Author.Name)
							}
						}
						a.renderAvatar(c, vd.Author.Name, vd.Author.Mid, 42, false, "", openAuthor)
						authorBody := func() {
							ui.Column(c).Gap(2).Grow(1).ClipX().Children(func() {
								ui.Text(c, vd.Author.Name).FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
								ui.Row(c).AlignItems(ui.Center).Gap(4).ClipX().Children(func() {
									renderSiteSmallBadge(c, "bilibili", "视频详情站点标记")
									ui.Text(c, fmt.Sprintf("· UID %d", vd.Author.Mid)).FontSize(12).TextColor(ColorTextMuted).SingleLine()
								})
							})
						}
						if !offline {
							author := ui.ButtonBase(c).Label("进入空间").Grow(1).Padding(0).ClipX()
							author.Children(authorBody)
							if author.Clicked() {
								a.OpenUser(vd.Author.Mid, vd.Author.Name)
							}
						} else {
							authorBody()
						}
					})

					ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
						if !offline {
							cacheKey := "cache-vid:" + vd.BVID
							caching := a.isPending(cacheKey)
							upowerLocked := vd.IsUpowerLocked()
							if AccentPillButton(c, "缓存视频", "缓存视频").Disabled(caching || upowerLocked).Clicked() && !caching && !upowerLocked {
								var pages []int
								for _, p := range vd.Pages {
									if a.SelectedPartPages[p.Page] {
										pages = append(pages, p.Page)
									}
								}
								var detailCopy = *vd
								detail := &detailCopy
								if a.CurrentLibraryDetail != nil {
									// A local manifest is not a complete site response.
									// The cache worker must resolve fresh metadata by BVID.
									detail = nil
								}
								req := download.VideoEnqueueRequest{
									BVID:   vd.BVID,
									Pages:  pages,
									Detail: detail,
									ForMid: a.cacheForMid(vd.BVID),
								}
								bvid := vd.BVID
								backend := a.backend
								requestGuarded(a, cacheKey, nil, func(ctx context.Context) (struct{}, error) {
									_, err := backend.EnqueueVideoCache(ctx, req)
									return struct{}{}, err
								}, func(struct{}) {
									a.RefreshData()
									a.BannerInfo = "已加入缓存队列：" + bvid
								}, func(err error) {
									a.showError(err)
								})
							}
						}
						if dotStatus != "" && dotStatus != "none" {
							DrawStatusDot(c, dotStatus, dotLabel)
						}
						if offline && cachedItem != nil {
							a.renderLibraryItemMoreMenu(c, *cachedItem, 32)
						} else if !offline && cachedItem != nil && itemKey != "" {
							menuKey := "bili-video:" + itemKey
							open := a.PostMenuKey == menuKey
							MoreMenuButton(c, "视频详情更多", 32, &open, func(m *ui.Menu) {
								if !cachedItem.IsArchivedSnapshot() {
									if m.Item("强制重新下载").Chosen() {
										a.openForceRecacheConfirm(itemKey, firstNonEmptyStrUI(vd.Title, vd.BVID))
									}
								}
								if vd.BVID != "" {
									targetURL := "https://www.bilibili.com/video/" + vd.BVID
									if m.Item("复制链接").Chosen() {
										c.WriteClipboard(targetURL)
										a.notifyInfo("已复制链接")
									}
									if m.Item("在浏览器中打开").Chosen() {
										if err := a.backend.Desktop().OpenExternalURL(targetURL); err != nil {
											a.showError(err)
										}
									}
								}
								if m.Item("打开文件夹").Chosen() {
									a.showError(a.backend.OpenItemFolder(itemKey))
								}
							})
							keyedMenuOpen(&a.PostMenuKey, menuKey, open)
						}
					})
				})

				// 2. Quality Upgrade Banner (docs/ui.md §3.4)
				if !offline && (a.UpgradeAvailable || (cachedItem != nil && cachedItem.UpgradeAvailable)) {
					hint := "可以升级到更高画质（如 4K / 高码率）"
					if cachedItem != nil && cachedItem.UpgradeHint != "" {
						hint = cachedItem.UpgradeHint
					}
					ui.Row(c).
						FillWidth().
						Padding(10, 14).
						Radius(12).
						Background(ColorWarnBg).
						Border(1, ColorDotYellow).
						AlignItems(ui.Center).
						Justify(ui.SpaceBetween).
						Gap(8).
						ClipX().
						Children(func() {
							ui.Row(c).AlignItems(ui.Center).Gap(8).Grow(1).Children(func() {
								DrawStatusDot(c, "yellow", hint)
								ui.Text(c, hint).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
							})
							upgKey := "upgrade-item:" + itemKey
							upgrading := a.isPending(upgKey)
							if AccentPillButton(c, "升级缓存", "视频详情升级缓存").Disabled(upgrading).Clicked() && !upgrading {
								targetKey, bvid := itemKey, vd.BVID
								backend := a.backend
								requestGuarded(a, upgKey, nil, func(ctx context.Context) (struct{}, error) {
									_, err := backend.Upgrade(ctx, targetKey)
									return struct{}{}, err
								}, func(struct{}) {
									a.BannerInfo = "已加入画质升级任务：" + bvid
									a.RefreshData()
								}, func(err error) {
									a.showError(err)
								})
							}
						})
				}

				// 3. Large Video Title (20px Bold) + Charging-Exclusive (充电专属) Badge / Notice
				ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Wrap().ClipX().Children(func() {
					ui.Text(c, vd.Title).FontSize(20).LineHeight(LineHeightHeading).FontWeight(600).TextColor(ColorTextMain).MaxLines(3)
					if vd.IsUpowerLocked() {
						ui.Box(c).Label("充电专属标记").Padding(2, 8).Radius(999).Background(ColorWarnBg).Border(1, ColorDotYellow).Children(func() {
							ui.Text(c, "充电专属").FontSize(12).FontWeight(600).TextColor(ColorTextMain).SingleLine()
						})
					} else if vd.IsUpowerUnlocked() {
						ui.Box(c).Label("充电专属已解锁标记").Padding(2, 8).Radius(999).Background(ColorAccentBg).Border(1, ColorAccent).Children(func() {
							ui.Text(c, "充电专属（已解锁）").FontSize(12).FontWeight(600).TextColor(ColorAccent).SingleLine()
						})
					}
				})
				if vd.IsUpowerLocked() {
					ui.Row(c).
						Label("充电专属未解锁提示").
						FillWidth().
						Padding(10, 14).
						Radius(12).
						Background(ColorWarnBg).
						Border(1, ColorDotYellow).
						AlignItems(ui.Center).
						Gap(8).
						ClipX().
						Children(func() {
							DrawIcon(c, "alert-circle", 16, ColorTextMain, 1.8)
							ui.Text(c, "充电专属：当前登录账号未开通该 UP 主充电权益，暂不支持缓存").
								FontSize(13).LineHeight(LineHeightCompact).FontWeight(600).TextColor(ColorTextMain).MaxLines(2)
						})
				}

				// 4. 16:9 Large Cover Card (click to play if cached, else view HD cover)
				totalDur := vd.Duration
				if totalDur <= 0 && len(vd.Pages) > 0 {
					for _, p := range vd.Pages {
						totalDur += p.Duration
					}
				}
				durationText := FormatDuration(totalDur)
				if a.CurrentLibraryDetail != nil && vd.Duration <= 0 {
					durationText = ""
				}
				coverRemote := vd.Cover
				if offline {
					coverRemote = ""
				}
				coverBtn := ui.ButtonBase(c).
					Label("视频大封面").
					FillWidth().
					Height(236).
					Radius(12).
					Clip()
				coverBtn.Children(func() {
					// The status dot is beside the cache button above, not on the cover.
					a.renderMediaThumb(c, itemKey, relCover, vd.Title, durationText, "", cachedItem != nil, 0, 236, coverRemote)
				})
				if coverBtn.Clicked() {
					if cachedItem != nil {
						a.OpenPlayerOverlay(*cachedItem)
					} else if !offline && vd.Cover != "" {
						a.OpenImageViewer([]string{vd.Cover}, nil, 0)
					}
				}

				// 5. Expandable Description
				if vd.Desc != "" {
					a.renderExpandableText(c, "video-desc:"+vd.BVID, vd.Desc)
				}

				// 6. Publish Time · Partition · Actions (including cache maintenance: "补存弹幕和字幕")
				metaParts := []string{"BVID: " + vd.BVID}
				if cachedItem != nil && cachedItem.Kind == "live" {
					roomStr := vd.BVID
					if cachedItem.Live != nil && cachedItem.Live.RoomID > 0 {
						roomStr = strconv.FormatInt(cachedItem.Live.RoomID, 10)
					}
					metaParts = []string{"直播间: " + roomStr}
					if liveSpec := formatLiveRecordingSpec(cachedItem.Live, ""); liveSpec != "" {
						metaParts = append(metaParts, liveSpec)
					}
				}
				if vd.PubTs > 0 {
					metaParts = append(metaParts, FormatTimestamp(vd.PubTs))
				}
				if vd.TName != "" {
					metaParts = append(metaParts, vd.TName)
				}
				ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Wrap().Children(func() {
					ui.Text(c, strings.Join(metaParts, " · ")).FontSize(13).TextColor(ColorTextMuted).SingleLine()
					if !offline {
						ui.Row(c).Gap(6).Wrap().Children(func() {
							if cachedItem != nil && cachedItem.Kind == "video" {
								suppKey := "supplement:" + vd.BVID
								supplementing := a.isPending(suppKey)
								if OutlinePillButton(c, "补存弹幕和字幕", "补全："+itemKey).Disabled(supplementing).Clicked() && !supplementing {
									bvid := vd.BVID
									backend := a.backend
									requestGuarded(a, suppKey, nil, func(ctx context.Context) (struct{}, error) {
										_, err := backend.CacheSupplement(ctx, bvid)
										return struct{}{}, err
									}, func(struct{}) {
										a.BannerInfo = "已加入弹幕字幕补存任务：" + bvid
										a.RefreshData()
									}, func(err error) {
										a.showError(err)
									})
								}
							}
							if vd.Cover != "" {
								if OutlinePillButton(c, "查看高清封面", "查看高清封面").Clicked() {
									a.OpenImageViewer([]string{vd.Cover}, nil, 0)
								}
							}
							targetURL := vd.CanonicalURL
							if targetURL == "" {
								targetURL = "https://www.bilibili.com/video/" + vd.BVID
							}
							if OutlinePillButton(c, "在浏览器中打开", "浏览器打开视频").Clicked() {
								if err := a.backend.Desktop().OpenExternalURL(targetURL); err != nil {
									a.showError(err)
								}
							}
						})
					}
				})

				// 7. Interaction Statistics Bar with Linear Icons and 万/亿 Formatting (docs/ui.md §3.4, §7)
				// Counts change all the time and are not kept, so a cached video shows none.
				if a.CurrentLibraryDetail == nil && !offline {
					stats := [7]struct {
						Icon  string
						Label string
						Count int64
					}{
						{"eye", "播放", vd.Stat.View},
						{"message", "弹幕", vd.Stat.Danmaku},
						{"heart", "点赞", vd.Stat.Like},
						{"coin", "投币", vd.Stat.Coin},
						{"star", "收藏", vd.Stat.Favorite},
						{"message", "评论", vd.Stat.Reply},
						{"share", "分享", vd.Stat.Share},
					}
					ui.Row(c).
						FillWidth().
						Padding(10, 4).
						BorderWidth(1, 0, 1, 0).
						BorderColor(ColorBorder).
						AlignItems(ui.Center).
						Justify(ui.SpaceBetween).
						Wrap().
						Gap(10).
						Children(func() {
							for _, st := range stats {
								s := st
								exactTip := fmt.Sprintf("%s %d", s.Label, s.Count)
								ui.Row(c).AlignItems(ui.Center).Gap(5).Tooltip(exactTip).Children(func() {
									DrawIcon(c, s.Icon, 16, ColorTextMuted, 1.75)
									ui.Text(c, fmt.Sprintf("%s %s", s.Label, FormatCount(s.Count))).
										FontSize(13).TextColor(ColorTextMuted).SingleLine()
								})
							}
						})
				}

				// 8. Multi-part (分 P) List with Cached Stream Specs (docs/ui.md §3.4)
				ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
					ui.Text(c, fmt.Sprintf("视频分 P 列表（共 %d P）", len(vd.Pages))).FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
				})

				for _, page := range vd.Pages {
					p := page
					partDuration := FormatDuration(p.Duration)
					if a.CurrentLibraryDetail != nil && p.Duration <= 0 {
						partDuration = "时长未保存"
					}
					selected := a.SelectedPartPages[p.Page]
					var partSpec string
					var partFile string
					var partChapters []library.VideoChapter
					if cachedItem != nil {
						for _, rec := range cachedItem.Parts {
							if rec.Page == p.Page {
								partFile = rec.File
								partSpec = formatPartQualitySpec(rec, true)
								partChapters = rec.Chapters
								break
							}
						}
					}

					ui.Column(c).
						FillWidth().
						Padding(10, 14).
						Radius(12).
						Background(ColorBgMain).
						Border(1, ColorBorder).
						Gap(8).
						ClipX().
						Children(func() {
							ui.Row(c).
								FillWidth().
								AlignItems(ui.Center).
								Justify(ui.SpaceBetween).
								Gap(10).
								ClipX().
								Children(func() {
									ui.Row(c).AlignItems(ui.Center).Gap(10).Grow(1).ClipX().Children(func() {
										ui.Box(c).Padding(2, 8).Radius(6).Background(ColorBgMain).Border(1, ColorBorder).Children(func() {
											ui.Text(c, fmt.Sprintf("P%d", p.Page)).FontSize(12).FontWeight(600).TextColor(ColorTextMuted).SingleLine()
										})
										ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
											ui.Text(c, p.Title).FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
											if partSpec != "" {
												ui.Text(c, "已缓存规格："+partSpec).FontSize(12).FontWeight(600).TextColor(ColorDotGreen).SingleLine()
											}
										})
									})
									ui.Row(c).AlignItems(ui.Center).Gap(10).Children(func() {
										ui.Text(c, partDuration).FontSize(12).TextColor(ColorTextMuted).SingleLine()
										if offline {
											if cachedItem != nil && partFile != "" {
												if OutlinePillButton(c, "播放", fmt.Sprintf("播放分P %d", p.Page)).Clicked() {
													a.openPlayerFile(*cachedItem, partFile)
												}
											}
										} else {
											chk := selected
											if ui.Switch(c, &chk).Label(fmt.Sprintf("选择分P %d", p.Page)).Changed() {
												a.SelectedPartPages[p.Page] = chk
											}
										}
									})
								})

							if len(partChapters) > 0 {
								ui.Column(c).Label(fmt.Sprintf("分P %d 章节看点", p.Page)).FillWidth().Padding(6, 0, 0, 0).BorderWidth(1, 0, 0, 0).BorderColor(ColorBorder).Gap(6).ClipX().Children(func() {
									ui.Text(c, fmt.Sprintf("章节看点（%d）", len(partChapters))).FontSize(12).FontWeight(600).TextColor(ColorTextMuted).SingleLine()
									ui.Row(c).FillWidth().Gap(6).Wrap().ClipX().Children(func() {
										for ci, ch := range partChapters {
											chapter := ch
											label := fmt.Sprintf("视频详情跳转章节：P%d-%d", p.Page, ci+1)
											text := fmt.Sprintf("%s %s", FormatDuration(chapter.FromSec), chapter.Title)
											btn := ui.ButtonBase(c).Label(label).Padding(4, 10).Radius(999).Background(ColorBgSecondary).Border(1, ColorBorder)
											btn.Children(func() {
												ui.Text(c, text).FontSize(12).TextColor(ColorTextMain).SingleLine()
											})
											if btn.Clicked() && cachedItem != nil && partFile != "" {
												a.openPlayerFileAt(*cachedItem, partFile, float64(chapter.FromSec))
											}
										}
									})
								})
							}
						})
				}
				return
			}

			if a.CurrentDynamic != nil {
				dyn := a.CurrentDynamic
				post := a.dynamicItemToPostCard(*dyn, "", "缓存该动态")
				a.renderPostCard(c, post)
				return
			}

			ui.Text(c, "未选择视频或动态，请先通过解析页打开内容。").FontSize(14).TextColor(ColorTextMuted).SingleLine()
		})
	})
}

// compactVideoRow is the data of one compact row (16:9 thumbnail, two-line
// title, one line of details), whatever list it comes from.
type compactVideoRow struct {
	Label, MenuKey, MenuName string
	BVID                     string // set for a Bilibili video: cache key, status dot, "缓存"
	ForMid                   int64  // creator page the row is on: a collaboration video is cached under their folder
	Title, Cover, Duration   string
	Meta                     string
	TypeLabel                string  // 音频、番剧…: shown instead of the play mark
	Badge                    string  // top-left mark on the thumbnail (直播)
	BadgeLive                bool    // the badge uses the live colour
	Progress                 float32 // 0..1, a thin accent line under the thumbnail
	Invalid                  bool    // grey block, not clickable, no "⋯"
	Open                     func()  // nil: the body is not clickable
	BrowserURL               string  // "在浏览器中打开"; "" hides it
	CopyURL                  string  // "复制链接"; "" hides it
	Extra                    func(m *ui.Menu)
}

func (a *App) renderCompactVideoRow(c *ui.Context, r compactVideoRow) {
	itemKey, dotStatus, relCover := "", "", ""
	if r.BVID != "" && !r.Invalid {
		itemKey = library.CanonicalItemKey("bilibili", "video", r.BVID)
		if entry, ok := a.lookupCompactLibEntry(itemKey); ok {
			dotStatus = entry.dotStatus
			relCover = entry.relCover
		}
	}
	titleColor, metaColor := ColorTextMain, ColorTextMuted
	if r.Invalid || r.Open == nil {
		titleColor = ColorTextMuted
	}
	ui.Row(c).
		FillWidth().
		Padding(12, 16).
		Gap(8).
		AlignItems(ui.Center).
		BorderWidth(0, 0, 1, 0).
		BorderColor(ColorBorder).
		ClipX().
		Children(func() {
			content := func() {
				ui.Column(c).Width(144).Shrink(0).Gap(0).Children(func() {
					ui.Box(c).Size(144, 82).Shrink(0).Children(func() {
						if r.Invalid {
							ui.Box(c).Size(144, 82).Radius(12).Background(ColorBgSecondary).Border(1, ColorBorder)
						} else {
							a.renderMediaThumb(c, itemKey, relCover, r.Title, r.Duration, dotStatus, dotStatus == "green" && r.TypeLabel == "", 144, 82, r.Cover)
						}
						if r.Badge != "" {
							bg := ui.RGBA(0, 0, 0, 0.65)
							if r.BadgeLive {
								bg = ColorLive
							}
							ui.Box(c).Absolute().Top(6).Left(6).Padding(2, 6).Radius(4).Background(bg).Children(func() {
								ui.Text(c, r.Badge).FontSize(11).FontWeight(600).TextColor(ColorWhite).SingleLine()
							})
						}
					})
					if r.Progress > 0 && !r.Invalid {
						ui.Box(c).Width(144).Height(3).Radius(2).Background(ColorBorder).Children(func() {
							ui.Box(c).Width(144 * min(r.Progress, 1)).Height(3).Radius(2).Background(ColorAccent)
						})
					}
				})
				ui.Column(c).Grow(1).Gap(5).ClipX().Children(func() {
					ui.Text(c, r.Title).FontSize(15).LineHeight(LineHeightCompact).FontWeight(600).TextColor(titleColor).MaxLines(2)
					meta := r.Meta
					if r.TypeLabel != "" {
						meta = strings.TrimSpace(r.TypeLabel + " · " + meta)
						meta = strings.TrimSuffix(meta, " ·")
					}
					if meta != "" {
						ui.Text(c, meta).FontSize(12).TextColor(metaColor).SingleLine()
					}
				})
			}
			if r.Open != nil && !r.Invalid {
				body := ui.ButtonBase(c).Label(r.Label).Grow(1).Padding(0).Gap(12).AlignItems(ui.Center).Justify(ui.Start).ClipX()
				body.Children(content)
				if body.Clicked() {
					r.Open()
				}
			} else {
				ui.Row(c).Label(r.Label).Grow(1).Gap(12).AlignItems(ui.Center).ClipX().Children(content)
			}
			if r.Invalid {
				return
			}
			browser := r.BrowserURL
			if browser == "" && r.CopyURL == "" && (r.BVID == "" || dotStatus != "") && r.Extra == nil {
				return
			}
			open := a.PostMenuKey == r.MenuKey
			MoreMenuButton(c, r.MenuName, 28, &open, func(m *ui.Menu) {
				if r.BVID != "" && dotStatus == "" && m.Item("缓存").Chosen() {
					bvid, forMid := r.BVID, r.ForMid
					backend := a.backend
					requestGuarded(a, "cache-vid:"+bvid, nil, func(ctx context.Context) (struct{}, error) {
						_, err := backend.EnqueueVideoCache(ctx, download.VideoEnqueueRequest{BVID: bvid, ForMid: forMid})
						return struct{}{}, err
					}, func(struct{}) {
						a.BannerInfo = "已加入缓存队列：" + bvid
						a.RefreshData()
					}, func(err error) {
						a.showError(err)
						a.RefreshData()
					})
				}
				if r.CopyURL != "" && m.Item("复制链接").Chosen() {
					c.WriteClipboard(r.CopyURL)
					a.notifyInfo("已复制链接")
				}
				if browser != "" && m.Item("在浏览器中打开").Chosen() {
					if err := a.backend.Desktop().OpenExternalURL(browser); err != nil {
						a.showError(err)
					}
				}
				if r.Extra != nil {
					r.Extra(m)
				}
			})
			keyedMenuOpen(&a.PostMenuKey, r.MenuKey, open)
		})
}

func taskStatusText(status string) string {
	switch status {
	case "completed":
		return "已完成"
	case "running":
		return "正在缓存"
	case "queued":
		return "排队中"
	case "paused":
		return "已暂停"
	case "failed":
		return "失败"
	case "partial":
		return "部分完成"
	case "canceled":
		return "已取消"
	default:
		return status
	}
}
