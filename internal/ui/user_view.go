package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

var userTabs = []TabOption{
	{Key: "dynamics", Text: "动态", AccessLabel: "空间动态标签"},
	{Key: "videos", Text: "投稿", AccessLabel: "空间投稿标签"},
}

func (a *App) userListIndices(tab string, mid int64, filtered bool, count int, keyAt func(int) string) []int {
	if count == 0 {
		return nil
	}
	firstKey, lastKey := keyAt(0), keyAt(count-1)
	var cacheKey string
	if filtered {
		cacheKey = fmt.Sprintf("flt:%s:%d:%s:%s", tab, mid, firstKey, lastKey)
	} else {
		cacheKey = fmt.Sprintf("all:%s:%d:%s:%s", tab, mid, firstKey, lastKey)
	}
	if a.userIdxCacheKey == cacheKey && a.userIdxCacheLen == count && (!filtered || a.userIdxCacheRev == a.cacheStateRev) {
		return a.userIdxCache
	}
	var idx []int
	if filtered {
		keys := make([]string, count)
		for i := range count {
			keys[i] = keyAt(i)
		}
		idx = a.uncachedIndices(fmt.Sprintf("%s:%d", tab, mid), keys)
	} else {
		idx = make([]int, count)
		for i := range idx {
			idx[i] = i
		}
	}
	a.userIdxCacheKey = cacheKey
	a.userIdxCacheRev = a.cacheStateRev
	a.userIdxCacheLen = count
	a.userIdxCache = idx
	return idx
}

// renderUserRoute draws /user (creator profile, subscription badge, live banner,
// dynamics & videos tabs, docs/ui.md §3.4).
func (a *App) renderUserRoute(c *ui.Context) {
	if a.Opening != nil {
		ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
			ui.Column(c).FillWidth().Padding(16).Gap(14).ClipX().Children(func() {
				a.renderOpening(c)
			})
		})
		return
	}
	if a.CurrentUser == nil {
		ui.Column(c).Grow(1).FillWidth().Padding(32).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "未加载 UP 主空间信息").FontSize(15).TextColor(ColorTextMuted).SingleLine()
		})
		return
	}
	u := a.CurrentUser

	// The profile header is row 0 of the list, so it scrolls away with the
	// posts and a short window still shows them. 未缓存 filters the loaded
	// pages only; with nothing left on them, the footer row offers the next
	// page instead of loading it by itself.
	filtered := a.UserListFilter == userFilterUncached
	if a.UserTab == "videos" {
		all := a.UserVideos
		idx := a.userListIndices("videos", u.Mid, filtered, len(all), func(i int) string {
			return library.CanonicalItemKey("bilibili", "video", all[i].BVID)
		})
		_, last := a.UserVidListState.Visible()
		pendingVidKey := fmt.Sprintf("user-videos-%d-%d", u.Mid, a.userVideosSeq)
		if len(idx) > 0 && last > 0 && last-1 >= len(idx)-2 && a.UserVideosHasMore && a.UserVideosError == "" && !a.isPending(pendingVidKey) {
			a.LoadMoreUserVideos()
		}
		rows := len(idx)
		emptyQueryResult := len(all) == 0 && a.UserVideosPage > 0 && a.UserVideosError == "" && !a.isPending(pendingVidKey)
		if (rows == 0 && filtered && len(all) > 0) || emptyQueryResult {
			rows = 1 // footer or empty-state row
		}
		a.UserVidListState.Key = func(i int) any {
			if i == 0 {
				return "header"
			}
			if i < 0 || i > len(idx) {
				return "footer"
			}
			return all[idx[i-1]].BVID
		}
		ui.List(c, a.UserVidListState, rows+1, func(i int) {
			if i == 0 {
				a.renderUserHeader(c, u)
				return
			}
			if len(idx) == 0 {
				if emptyQueryResult {
					msg := "暂无投稿"
					if strings.TrimSpace(a.UserVideosAppliedKeyword) != "" || a.UserVideosTID > 0 {
						msg = "未找到匹配的投稿"
					}
					ui.Row(c).FillWidth().Padding(24, 16).Justify(ui.Center).Children(func() {
						ui.Text(c, msg).Label("空间投稿结果为空").FontSize(13).TextColor(ColorTextMuted).SingleLine()
					})
					return
				}
				a.renderUserFilteredEmpty(c)
				a.renderUserListEnd(c, a.UserVideosHasMore, a.UserVideosError, "加载下一页投稿", "已加载全部投稿", a.LoadMoreUserVideos)
				return
			}
			if i < 0 || i > len(idx) {
				return
			}
			a.renderUserVideoRow(c, u, all[idx[i-1]])
			if i == len(idx) {
				a.renderUserListEnd(c, a.UserVideosHasMore, a.UserVideosError, "加载下一页投稿", "已加载全部投稿", a.LoadMoreUserVideos)
			}
		}).Label("UP主投稿虚拟列表").Grow(1).FillWidth()
		return
	}

	all := a.UserDynamics
	idx := a.userListIndices("dynamics", u.Mid, filtered, len(all), func(i int) string {
		return library.CanonicalItemKey("bilibili", "dynamic", all[i].ID)
	})
	_, last := a.UserDynListState.Visible()
	if len(idx) > 0 && last > 0 && last-1 >= len(idx)-2 && a.UserDynamicsHasMore && a.UserDynamicsError == "" && !a.isPending(fmt.Sprintf("user-dynamics-%d", u.Mid)) {
		a.LoadMoreUserDynamics()
	}
	rows := len(idx)
	if rows == 0 && filtered && len(all) > 0 {
		rows = 1 // footer only
	}
	a.UserDynListState.Key = func(i int) any {
		if i == 0 {
			return "header"
		}
		if i < 0 || i > len(idx) {
			return "footer"
		}
		return all[idx[i-1]].ID
	}
	ui.List(c, a.UserDynListState, rows+1, func(i int) {
		if i == 0 {
			a.renderUserHeader(c, u)
			return
		}
		if len(idx) == 0 {
			a.renderUserFilteredEmpty(c)
			a.renderUserListEnd(c, a.UserDynamicsHasMore, a.UserDynamicsError, "加载下一页动态", "已加载全部动态", a.LoadMoreUserDynamics)
			return
		}
		if i < 0 || i > len(idx) {
			return
		}
		d := all[idx[i-1]]
		if d.Author.Name == "" {
			d.Author.Name = u.Name
			d.Author.Mid = u.Mid
		}
		a.renderPostCard(c, a.dynamicItemToPostCard(d, "打开动态："+d.ID, "缓存动态："+d.ID))
		if i == len(idx) {
			a.renderUserListEnd(c, a.UserDynamicsHasMore, a.UserDynamicsError, "加载下一页动态", "已加载全部动态", a.LoadMoreUserDynamics)
		}
	}).Label("UP主动态虚拟列表").Grow(1).FillWidth()
}

func (a *App) renderUserFilteredEmpty(c *ui.Context) {
	ui.Row(c).FillWidth().Padding(18, 16, 0, 16).Justify(ui.Center).Children(func() {
		ui.Text(c, "已加载的内容都缓存了").Label("未缓存筛选为空").FontSize(13).TextColor(ColorTextMuted).SingleLine()
	})
}

func (a *App) renderUserListEnd(c *ui.Context, hasMore bool, loadErr, moreText, doneText string, more func()) {
	ui.Row(c).FillWidth().Padding(14, 16).Justify(ui.Center).Children(func() {
		if hasMore {
			if OutlinePillButton(c, moreText, moreText).Clicked() {
				more()
			}
		} else if loadErr == "" {
			ui.Text(c, doneText).FontSize(13).TextColor(ColorTextMuted)
		}
	})
}

// renderUserHeader draws the creator's banner, stats, live banner, list
// errors and tabs: one main button (subscribe), the rest in "⋯".
func (a *App) renderUserHeader(c *ui.Context, u *bilibili.UserSpace) {
	var liveRoom *live.RoomState
	for i := range a.LiveRooms {
		if a.LiveRooms[i].UID == u.Mid && a.LiveRooms[i].LiveStatus == 1 {
			liveRoom = &a.LiveRooms[i]
			break
		}
	}
	ui.Column(c).FillWidth().BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).ClipX().Children(func() {
		pal := AvatarColor(u.Name, u.Mid)
		ui.Box(c).
			FillWidth().
			Height(92).
			Draw(func(p *ui.Painter, r ui.Rect) {
				p.FillGradient(r, ui.LinearGradient{
					From:  pal.Mix(ColorTextMain, 0.25),
					To:    pal.Mix(ColorInfoBlue, 0.45),
					Angle: 120,
				}, 0)
			}).
			Children(func() {
				ui.Row(c).Fill().Padding(12, 16).AlignItems(ui.Center).Gap(12).Children(func() {
					ui.Box(c).Padding(3).Radius(999).Background(ColorWhite).Children(func() {
						a.renderAvatar(c, u.Name, u.Mid, 54, liveRoom != nil, "", nil)
					})
					ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
						ui.Text(c, u.Name).FontSize(18).FontWeight(600).TextColor(ColorWhite).SingleLine()
						ui.Text(c, fmt.Sprintf("UID %d · Lv.%d", u.Mid, u.Level)).FontSize(12).FontWeight(600).TextColor(ui.RGBA(255, 255, 255, 0.88)).SingleLine()
					})
				})
			})

		ui.Column(c).FillWidth().Padding(10, 16).Gap(8).ClipX().Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(10).Grow(1).ClipX().Children(func() {
					renderSiteSmallBadge(c, "bilibili", "用户页站点标记")
					ui.Text(c, "·").FontSize(12).TextColor(ColorTextMuted).SingleLine()
					ui.Text(c, fmt.Sprintf("关注 %s", FormatCount(int64(u.Friend)))).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					ui.Text(c, fmt.Sprintf("粉丝 %s", FormatCount(int64(u.Fans)))).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					ui.Text(c, fmt.Sprintf("投稿 %s", FormatCount(int64(u.ArchiveCount)))).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
				})
				ui.Row(c).Shrink(0).Gap(6).AlignItems(ui.Center).Children(func() {
					ownerSub, owned := a.ownerOfAccountInMemory("bilibili", strconv.FormatInt(u.Mid, 10), u.Mid)
					if owned {
						if OutlinePillButton(c, "已订阅", "进入所属订阅").Clicked() {
							a.OpenSubscriptionDetail(ownerSub)
						}
					} else if AccentPillButton(c, "订阅", "订阅该 UP 主").Clicked() {
						a.OpenSubscribePicker(subscription.AccountRef{Site: "bilibili", UID: strconv.FormatInt(u.Mid, 10), Name: u.Name, Face: u.Face})
					}
					targetURL := u.CanonicalURL
					if targetURL == "" {
						targetURL = fmt.Sprintf("https://space.bilibili.com/%d", u.Mid)
					}
					MoreMenuButton(c, "空间更多", 32, nil, func(m *ui.Menu) {
						a.cacheOlderMenu(m, "bilibili")
						if m.Item("在浏览器中打开空间").Chosen() {
							if err := a.backend.Desktop().OpenExternalURL(targetURL); err != nil {
								a.showError(err)
							}
						}
						if liveRoom != nil {
							roomID := liveRoom.RoomID
							if m.Item("在浏览器中观看直播").Chosen() {
								if err := a.backend.OpenLiveRoomInBrowser(roomID); err != nil {
									a.showError(err)
								}
							}
						}
					})
				})
			})

			if u.Sign != "" {
				ui.Text(c, u.Sign).FontSize(13).LineHeight(LineHeightCompact).TextColor(ColorTextMuted).MaxLines(2)
			}

			// Live banner (docs/ui.md §3.4): the record toggle is its one button.
			if liveRoom != nil {
				room := *liveRoom
				qualSpec := bilibili.FormatLiveQualitySpec(room.QualityName, room.Quality, room.Codec)
				ui.Row(c).
					FillWidth().
					Padding(8, 12).
					Radius(10).
					Background(ColorLiveBg).
					Border(1, ColorLive).
					AlignItems(ui.Center).
					Justify(ui.SpaceBetween).
					Gap(8).
					ClipX().
					Children(func() {
						ui.Row(c).AlignItems(ui.Center).Gap(8).Grow(1).ClipX().Children(func() {
							ui.Box(c).Shrink(0).Padding(2, 8).Radius(999).Background(ColorLive).Children(func() {
								ui.Text(c, "直播中").FontSize(11).FontWeight(600).TextColor(ColorWhite).SingleLine()
							})
							ui.Text(c, room.Title).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine().Tooltip(fmt.Sprintf("房间号 %d", room.RoomID))
							if qualSpec != "" {
								ui.Text(c, "· "+qualSpec).Shrink(0).FontSize(12).FontWeight(600).TextColor(ColorLive).SingleLine()
							}
						})
						if room.Recording {
							if DangerOutlinePillButton(c, "停止录制", "空间停止录制").Clicked() {
								a.stopLiveRecording(room.RoomID)
							}
						} else if AccentPillButton(c, "录制", "空间开始录制").Clicked() {
							a.startLiveRecording(room.RoomID)
						}
					})
			}

			// Partial-failure error with Retry while keeping page 1.
			if a.UserTab != "videos" && a.UserDynamicsError != "" {
				a.renderUserListError(c, a.UserDynamicsError, "重试加载动态", a.LoadMoreUserDynamics)
			}
			if a.UserTab == "videos" && a.UserVideosError != "" {
				a.renderUserListError(c, a.UserVideosError, "重试加载投稿", a.LoadMoreUserVideos)
			}
		})

		ui.Row(c).FillWidth().AlignItems(ui.End).Gap(12).ClipX().Children(func() {
			ui.Box(c).Grow(1).ClipX().Children(func() {
				UnderlineTabs(c, a.UserTab, userTabs, func(key string) {
					a.UserTab = key
					if key == "videos" && a.UserVideosPage == 0 && a.UserVideosError == "" {
						a.LoadMoreUserVideos()
					}
				})
			})
			ui.Box(c).Padding(0, 0, 6, 0).Shrink(0).Children(func() { a.renderUserListFilter(c) })
		})
		if a.UserTab == "videos" {
			a.renderUserVideosToolbar(c, u)
		}
	})
}

func (a *App) renderUserVideosToolbar(c *ui.Context, u *bilibili.UserSpace) {
	pendingVidKey := fmt.Sprintf("user-videos-%d-%d", u.Mid, a.userVideosSeq)
	loading := a.isPending(pendingVidKey)
	curOrder := bilibili.NormalizeUserVideosOrder(a.UserVideosOrder)
	ui.Column(c).FillWidth().Padding(8, 16, 10, 16).Gap(8).BorderWidth(1, 0, 0, 0).BorderColor(ColorBorder).ClipX().Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Wrap().ClipX().Children(func() {
			ui.Row(c).
				Grow(1).
				MinWidth(200).
				Padding(4, 6, 4, 10).
				Radius(999).
				Background(ColorBgMain).
				Border(1, ColorBorderStrong).
				AlignItems(ui.Center).
				Gap(6).
				ClipX().
				Children(func() {
					inp := ui.TextInputBase(c, &a.UserVideosKeywordInput).
						Placeholder("搜索该 UP 主投稿…").
						Label("空间投稿搜索框").
						FontSize(13).
						Padding(2, 4).
						Grow(1)
					wasComposing := a.UserVideosKeywordComposing
					a.UserVideosKeywordComposing = inp.Composing()
					// Strictly submit only on Enter (outside IME composition) or Search button click.
					if inp.Submitted() && !inp.Composing() && !wasComposing && !loading {
						a.SubmitUserVideosSearch()
					}
					if strings.TrimSpace(a.UserVideosKeywordInput) != "" || strings.TrimSpace(a.UserVideosAppliedKeyword) != "" {
						if OutlinePillButton(c, "清空", "清空空间投稿搜索").Disabled(loading).Clicked() {
							a.ClearUserVideosSearch()
						}
					}
					if AccentPillButton(c, "搜索", "搜索空间投稿").Disabled(loading).Clicked() {
						a.SubmitUserVideosSearch()
					}
				})

			renderSegmentedPills(c, curOrder, []segmentedChoice{
				{Key: "pubdate", Text: "最新发布", Label: "投稿排序：最新发布"},
				{Key: "click", Text: "最多播放", Label: "投稿排序：最多播放"},
				{Key: "stow", Text: "最多收藏", Label: "投稿排序：最多收藏"},
			}, func(next string) {
				a.SetUserVideosOrder(next)
			})
		})

		if len(a.UserVideosZones) > 0 {
			ui.Row(c).Label("空间投稿分区筛选").FillWidth().AlignItems(ui.Center).Gap(6).Wrap().ClipX().Children(func() {
				renderZonePill := func(tid int, text, label string) {
					active := a.UserVideosTID == tid
					btn := ui.ButtonBase(c).Label(label).Padding(4, 10).Radius(999)
					if active {
						btn.Background(ColorAccentBg).Border(1, ColorAccent)
					} else {
						btn.Background(ColorBgSecondary).Border(1, ColorBorder)
					}
					btn.Children(func() {
						tc := ColorTextMain
						fw := 500
						if active {
							tc = ColorAccent
							fw = 600
						}
						ui.Text(c, text).FontSize(12).FontWeight(fw).TextColor(tc).SingleLine()
					})
					if btn.Clicked() {
						a.SetUserVideosTID(tid)
					}
				}
				renderZonePill(0, "全部分区", "投稿分区：全部")
				for _, z := range a.UserVideosZones {
					renderZonePill(z.TID, fmt.Sprintf("%s %d", z.Name, z.Count), "投稿分区："+z.Name)
				}
			})
		}
	})
}

func (a *App) renderUserListError(c *ui.Context, msg, retryLabel string, retry func()) {
	renderErrorBanner(c, "", msg, func() {
		if OutlinePillButton(c, "重试", retryLabel).Clicked() {
			retry()
		}
	})
}

// renderUserVideoRow is one upload: clicking it opens the video; caching and
// the browser are in its "⋯".
func (a *App) renderUserVideoRow(c *ui.Context, u *bilibili.UserSpace, v bilibili.UserVideoItem) {
	a.renderCompactVideoRow(c, compactVideoRow{
		Label:      "打开视频：" + v.BVID,
		MenuKey:    "upload:" + v.BVID,
		MenuName:   "投稿更多：" + v.BVID,
		BVID:       v.BVID,
		ForMid:     u.Mid,
		Title:      v.Title,
		Cover:      v.Cover,
		Duration:   v.Length,
		Meta:       fmt.Sprintf("时长 %s · 播放 %s", v.Length, FormatCount(int64(v.PlayCount))),
		BrowserURL: "https://www.bilibili.com/video/" + v.BVID,
		Open:       func() { a.openVideoFor(v.BVID, v.Title, u.Name, u.Mid, v.Cover, v.Length, u.Mid) },
	})
}
