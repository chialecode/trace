package ui

import (
	"fmt"
	"strings"

	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

// ============================================================================
// Route: /douyin/user (Neutral Header + Single 3:4 Works Grid, docs/ui.md §3.4)
// ============================================================================

func douyinUserGridColumns(contentWidth float32) int {
	switch {
	case contentWidth >= 820:
		return 5
	case contentWidth >= 600:
		return 4
	default:
		return 3
	}
}

func (a *App) findDouyinUserLiveState(u *service.DouyinUserView) *service.LiveAccountState {
	if u == nil {
		return nil
	}
	for i := range a.LiveAccounts {
		st := &a.LiveAccounts[i]
		if strings.EqualFold(st.Site, "douyin") && (st.UID == u.SecUID || (u.RoomID != "" && st.RoomID == u.RoomID)) {
			if st.LiveStatus == 1 || st.Recording {
				return st
			}
		}
	}
	if u.LiveRoom != nil && (u.LiveRoom.LiveStatus == 1 || u.LiveRoom.Recording) {
		return u.LiveRoom
	}
	if u.LiveStatus == 1 {
		return &service.LiveAccountState{
			Site:       "douyin",
			AccountKey: "douyin:" + u.SecUID,
			UID:        u.SecUID,
			RoomID:     u.RoomID,
			WebRID:     u.WebRID,
			Uname:      u.Nickname,
			Face:       u.AvatarURL,
			LiveStatus: 1,
			StatusText: "直播中",
		}
	}
	return nil
}

func (a *App) renderDouyinUserRoute(c *ui.Context, centerWidth float32) {
	if a.Opening != nil {
		ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
			ui.Column(c).FillWidth().Padding(16).Gap(14).ClipX().Children(func() {
				a.renderOpening(c)
			})
		})
		return
	}
	if a.CurrentDouyinUser == nil {
		ui.Column(c).Grow(1).FillWidth().Padding(32).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "未加载抖音作者主页信息").FontSize(15).TextColor(ColorTextMuted).SingleLine()
		})
		return
	}

	u := a.CurrentDouyinUser
	cols := douyinUserGridColumns(centerWidth)
	works := a.CurrentDouyinWorks
	if a.UserListFilter == userFilterUncached && len(works) > 0 {
		// 未缓存 filters the loaded pages only (DEC-046 ⑧).
		keys := make([]string, len(works))
		for i, w := range works {
			keys[i] = w.CacheKey
		}
		idx := a.uncachedIndices("douyin:"+u.SecUID, keys)
		kept := make([]service.DouyinWorkCardDTO, 0, len(idx))
		for _, i := range idx {
			kept = append(kept, works[i])
		}
		works = kept
	}
	a.douyinFilteredEmpty = len(works) == 0
	gridRows := 0
	if !u.IsPrivate && len(works) > 0 {
		gridRows = (len(works) + cols - 1) / cols
	}
	// Row 0 = header; Rows 1..gridRows = 3:4 card rows; final row = footer/pagination/auth-expired/retry.
	totalRows := 1 + gridRows + 1

	_, last := a.DouyinUserListState.Visible()
	if !u.IsPrivate && gridRows > 0 && last > 0 && last >= gridRows && a.DouyinWorksHasMore && a.DouyinWorksError == "" && !a.DouyinWorksAuthExpired && !a.isPending("douyin-works-"+u.SecUID) {
		a.LoadMoreDouyinWorks()
	}

	a.DouyinUserListState.Key = func(i int) any {
		if i == 0 {
			return "douyin-header"
		}
		if i > 0 && i <= gridRows {
			idx := (i - 1) * cols
			if idx >= 0 && idx < len(works) {
				return "douyin-row-" + works[idx].ID
			}
		}
		return "douyin-footer"
	}

	ui.List(c, a.DouyinUserListState, totalRows, func(i int) {
		if i == 0 {
			a.renderDouyinUserHeader(c, u)
			return
		}
		if i <= gridRows {
			start := (i - 1) * cols
			end := min(len(works), start+cols)
			rowCards := works[start:end]
			rowKeys := make([]string, len(rowCards))
			for j, w := range rowCards {
				rowKeys[j] = w.CacheKey
			}
			locals := a.workCardLocals(rowKeys)
			ui.Row(c).FillWidth().Padding(6, 16).Gap(10).AlignItems(ui.Start).ClipX().Children(func() {
				for slot := 0; slot < cols; slot++ {
					if slot < len(rowCards) {
						card := rowCards[slot]
						a.renderDouyinWorkGridCard(c, u.Nickname, u.SecUID, card, locals[card.CacheKey])
					} else {
						ui.Box(c).Grow(1).Basis(0)
					}
				}
			})
			return
		}
		a.renderDouyinUserFooter(c, u)
	}).Label("抖音作品虚拟列表").Grow(1).FillWidth()
}

func (a *App) renderDouyinUserHeader(c *ui.Context, u *service.DouyinUserView) {
	liveState := a.findDouyinUserLiveState(u)
	ui.Column(c).FillWidth().BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).ClipX().Children(func() {
		// Neutral background header block, never Bilibili's gradient banner (docs/ui.md §3.4).
		ui.Column(c).
			FillWidth().
			Padding(16).
			Background(ColorBgSecondary).
			Gap(10).
			ClipX().
			Children(func() {
				ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(12).ClipX().Children(func() {
					a.renderAccountAvatar(c, "douyin", u.Nickname, u.SecUID, 0, 56, liveState != nil, "抖音作者头像", nil, u.AvatarURL)
					ui.Column(c).Grow(1).Gap(4).ClipX().Children(func() {
						ui.Row(c).AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
							ui.Text(c, firstNonEmptyStrUI(u.Nickname, "抖音作者")).FontSize(18).FontWeight(600).TextColor(ColorTextMain).SingleLine().Shrink(1)
							renderSiteSmallBadgeWithDot(c, "douyin", "用户页站点标记")
						})
						if handle := formatDouyinHandle(u.UniqueID, u.ShortID, u.UID); handle != "" {
							ui.Text(c, handle).FontSize(12).TextColor(ColorTextMuted).SingleLine()
						}
					})
					ui.Row(c).Shrink(0).Gap(6).AlignItems(ui.Center).Children(func() {
						ownerSub, owned := a.ownerOfAccountInMemory("douyin", u.SecUID, 0)
						if owned {
							if OutlinePillButton(c, "已订阅", "进入所属订阅").Clicked() {
								a.OpenSubscriptionDetail(ownerSub)
							}
						} else if AccentPillButton(c, "订阅", "订阅该抖音作者").Clicked() {
							acct := subscription.AccountRef{
								Site:     "douyin",
								UID:      u.SecUID,
								UniqueID: firstNonEmptyStrUI(u.UniqueID, u.ShortID),
								WebRID:   u.WebRID,
								Name:     firstNonEmptyStrUI(u.Nickname, u.SecUID),
								Face:     u.AvatarURL,
							}
							a.OpenSubscribePicker(acct)
						}
						targetURL := firstNonEmptyStrUI(u.CanonicalURL, "https://www.douyin.com/user/"+u.SecUID)
						MoreMenuButton(c, "抖音作者更多", 32, &a.DouyinUserSubMenuOpen, func(m *ui.Menu) {
							if !u.IsPrivate {
								a.cacheOlderMenu(m, "douyin")
							}
							if m.Item("在浏览器中打开").Chosen() {
								a.showError(a.backend.Desktop().OpenExternalURL(targetURL))
							}
							if liveState != nil {
								roomOrWebRID := firstNonEmptyStrUI(liveState.WebRID, u.WebRID, liveState.RoomID, u.RoomID)
								if roomOrWebRID != "" && m.Item("在浏览器中观看直播").Chosen() {
									a.showError(a.backend.OpenLiveInBrowser("douyin", roomOrWebRID))
								}
							}
						})
					})
				})

				ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(12).Wrap().ClipX().Children(func() {
					ui.Text(c, fmt.Sprintf("关注 %s · 粉丝 %s · 获赞 %s · 作品 %s",
						FormatCount(u.FollowingCount),
						FormatCount(u.FollowerCount),
						FormatCount(u.TotalFavorited),
						FormatCount(int64(u.AwemeCount)))).
						FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
				})

				if strings.TrimSpace(u.Signature) != "" {
					ui.Text(c, strings.TrimSpace(u.Signature)).FontSize(13).LineHeight(LineHeightCompact).TextColor(ColorTextMuted).MaxLines(3)
				}

				if liveState != nil {
					st := *liveState
					qualSpec := formatLiveAccountQualitySpec(st)
					ui.Row(c).
						Label("抖音主页直播横幅").
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
								title := firstNonEmptyStrUI(st.Title, u.Nickname+"的直播间")
								ui.Text(c, title).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine().Shrink(1)
								if qualSpec != "" {
									ui.Text(c, "· "+qualSpec).Shrink(0).FontSize(12).FontWeight(600).TextColor(ColorLive).SingleLine()
								}
							})
							if st.Recording {
								recKey := firstNonEmptyStrUI(st.RecordingKey, "douyin:"+u.SecUID)
								if DangerOutlinePillButton(c, "停止录制", "抖音主页停止录制").Clicked() {
									a.stopAccountLiveRecording(recKey)
								}
							} else if AccentPillButton(c, "录制", "抖音主页开始录制").Clicked() {
								a.startAccountLiveRecording("douyin", u.SecUID, firstNonEmptyStrUI(st.WebRID, u.WebRID, st.RoomID, u.RoomID))
							}
						})
				}
			})

		// Single "作品" section heading (no tabs).
		ui.Row(c).FillWidth().Padding(10, 16, 6, 16).AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
				ui.Text(c, "作品").FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
				if u.AwemeCount > 0 {
					ui.Text(c, fmt.Sprintf("共 %d 个", u.AwemeCount)).FontSize(12).TextColor(ColorTextMuted).SingleLine()
				}
			})
			if !u.IsPrivate {
				a.renderUserListFilter(c)
			}
		})
	})
}

// workCardLocal is what a work card shows from the library: its status dot
// and local cover.
type workCardLocal struct{ dot, cover string }

// workCardLocals returns the library side of the cards with these keys.
// Grids ask per visible row; answers are kept until tasks or the library
// change (cacheStateRev), so frames do not read the library.
func (a *App) workCardLocals(keys []string) map[string]workCardLocal {
	if a.cardLocals == nil || a.cardLocalsRev != a.cacheStateRev || len(a.cardLocals) > 4000 {
		a.cardLocals = make(map[string]workCardLocal)
		a.cardLocalsRev = a.cacheStateRev
	}
	var missing []string
	for _, k := range keys {
		if _, ok := a.cardLocals[k]; k != "" && !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) == 0 {
		return a.cardLocals
	}
	states := a.backend.States(missing)
	for _, k := range missing {
		loc := workCardLocal{dot: states[k].Dot}
		// Only a cached work has a local cover to look up.
		if loc.dot != "" && loc.dot != "none" {
			if it, ok, err := a.backend.GetLibraryItem(k); err == nil && ok {
				for _, m := range it.Media {
					if (m.Role == "cover" || m.Role == "image") && loc.cover == "" {
						loc.cover = m.Path
					}
				}
				if loc.cover == "" && len(it.NotePages) > 0 {
					loc.cover = firstNonEmptyStrUI(it.NotePages[0].Image, it.NotePages[0].Cover)
				}
			}
		}
		a.cardLocals[k] = loc
	}
	return a.cardLocals
}

func douyinWorkFallbackURL(kind, id string) string {
	if id == "" {
		return ""
	}
	if kind == "note" {
		return "https://www.douyin.com/note/" + id
	}
	return "https://www.douyin.com/video/" + id
}

func dotIsCachedOrDownloading(dot string) bool {
	switch dot {
	case "green", "blue", "gray", "queued", "running":
		return true
	default:
		return false
	}
}

// renderDouyinWorkGridCard is one 3:4 work card; the user page and 我的 share
// it, so it only needs the author's name and sec_uid for the opening preview.
func (a *App) renderDouyinWorkGridCard(c *ui.Context, authorName, authorSecUID string, card service.DouyinWorkCardDTO, local workCardLocal) {
	dotStatus, relCover := local.dot, local.cover
	bmp := a.lookupLocalBitmap(card.CacheKey, relCover)
	if bmp == nil {
		bmp = a.lookupRemoteBitmap(card.CoverURL)
	}
	pal := AvatarColor(card.Title+card.ID, accountSeedHash("douyin", card.ID))

	ui.Column(c).Grow(1).Basis(0).Gap(5).ClipX().Children(func() {
		coverBox := ui.Box(c).
			FillWidth().
			AspectRatio(0.75). // 3:4 vertical card
			Radius(10).
			Border(1, ColorBorder).
			Background(ColorBgSecondary).
			Clip()
		coverBox.Draw(func(p *ui.Painter, r ui.Rect) {
			if bmp == nil {
				p.FillGradient(r, ui.LinearGradient{
					From:  pal.Mix(ColorTextMain, 0.35),
					To:    pal.Mix(ColorInfoBlue, 0.45),
					Angle: 135,
				}, 10)
			}
		})
		coverBox.Children(func() {
			openBtn := ui.ButtonBase(c).
				Label("打开作品：" + card.ID).
				Absolute().
				Left(0).
				Top(0).
				Right(0).
				Bottom(0).
				Padding(0)
			openBtn.Children(func() {
				if bmp != nil {
					ui.Image(c, bmp).Absolute().Left(0).Top(0).Right(0).Bottom(0).Fit(ui.Cover)
				}
			})
			if openBtn.Clicked() {
				dur := ""
				if card.DurationSec > 0 {
					dur = FormatDuration(card.DurationSec)
				}
				a.openDouyinWorkFor(card.ID, card.Kind, card.Title, authorName, authorSecUID, card.CoverURL, dur, authorSecUID, authorName)
			}

			// Top-left badges: 置顶 & 图文
			if card.IsTop || card.Kind == "note" {
				ui.Row(c).Absolute().Top(6).Left(6).Gap(4).AlignItems(ui.Center).Children(func() {
					if card.IsTop {
						ui.Box(c).Label("抖音置顶标记："+card.ID).Padding(2, 6).Radius(4).Background(ColorAccent).Children(func() {
							ui.Text(c, "置顶").FontSize(11).FontWeight(600).TextColor(ColorWhite).SingleLine()
						})
					}
					if card.Kind == "note" {
						ui.Box(c).Label("抖音图文标记："+card.ID).Padding(2, 6).Radius(4).Background(ui.RGBA(0, 0, 0, 0.65)).Children(func() {
							ui.Text(c, "图文").FontSize(11).FontWeight(600).TextColor(ColorWhite).SingleLine()
						})
					}
				})
			}

			// Top-right 8px status dot
			if dotStatus != "" && dotStatus != "none" {
				ui.Box(c).Absolute().Top(6).Right(6).Padding(2).Radius(999).Background(ui.RGBA(255, 255, 255, 0.92)).Children(func() {
					DrawStatusDot(c, dotStatus, "作品状态："+card.ID)
				})
			}

			// Bottom-left video duration (keeps bottom-right clear for floating ⋯ button)
			if card.Kind == "video" && card.DurationSec > 0 {
				ui.Box(c).Absolute().Bottom(6).Left(6).Padding(2, 6).Radius(4).Background(ui.RGBA(0, 0, 0, 0.72)).Children(func() {
					ui.Text(c, FormatDuration(card.DurationSec)).FontSize(11).FontWeight(600).TextColor(ColorWhite).SingleLine()
				})
			}

			// Bottom-right floating ⋯ menu ("缓存" / "复制链接" / "在浏览器中打开")
			menuOpen := a.DouyinCardMenuID == card.ID
			ui.Box(c).Absolute().Bottom(6).Right(6).Radius(14).Background(ui.RGBA(255, 255, 255, 0.88)).Children(func() {
				MoreMenuButton(c, "作品更多："+card.ID, 26, &menuOpen, func(m *ui.Menu) {
					if !card.Uncacheable && !dotIsCachedOrDownloading(dotStatus) && m.Item("缓存").Chosen() {
						a.cacheDouyinWorkFor(card.ID, authorSecUID, authorName)
					}
					canonURL := firstNonEmptyStrUI(card.CanonicalURL, douyinWorkFallbackURL(card.Kind, card.ID))
					if m.Item("复制链接").Chosen() {
						c.WriteClipboard(canonURL)
						c.Toast("已复制链接")
					}
					if m.Item("在浏览器中打开").Chosen() {
						a.showError(a.backend.Desktop().OpenExternalURL(canonURL))
					}
				})
			})
			keyedMenuOpen(&a.DouyinCardMenuID, card.ID, menuOpen)
		})

		caption := firstNonEmptyStrUI(card.Title, card.Desc, card.ID)
		ui.Text(c, caption).FontSize(13).LineHeight(LineHeightHeading).FontWeight(500).TextColor(ColorTextMain).MaxLines(2)
	})
}

func (a *App) renderDouyinUserFooter(c *ui.Context, u *service.DouyinUserView) {
	ui.Column(c).FillWidth().Padding(14, 16, 24, 16).AlignItems(ui.Center).Gap(8).ClipX().Children(func() {
		if u.IsPrivate && a.DouyinWorksError != "" {
			a.renderUserListError(c, a.DouyinWorksError, "重试加载抖音作品", a.LoadMoreDouyinWorks)
			return
		}
		if u.IsPrivate {
			ui.Text(c, "这个账号是私密账号，当前登录的抖音账号看不到作品").
				Label("私密账号提示").
				FontSize(14).
				TextColor(ColorTextMuted).
				SingleLine()
			return
		}
		if u.PrivateVisible {
			ui.Text(c, "私密账号：当前登录的抖音账号可以看到这些作品").
				Label("私密账号可见提示").
				FontSize(12).
				TextColor(ColorTextMuted).
				SingleLine()
		}
		if a.UserListFilter == userFilterUncached && len(a.CurrentDouyinWorks) > 0 && a.douyinFilteredEmpty {
			ui.Text(c, "已加载的作品都缓存了").Label("未缓存筛选为空").FontSize(13).TextColor(ColorTextMuted).SingleLine()
		}
		if a.DouyinWorksAuthExpired {
			ui.Row(c).
				FillWidth().
				Padding(10, 14).
				Radius(10).
				Background(ColorWarnBg).
				Border(1, ColorDotYellow).
				AlignItems(ui.Center).
				Justify(ui.SpaceBetween).
				Gap(8).
				Children(func() {
					ui.Text(c, "登录已失效").FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine().Grow(1)
					if AccentPillButton(c, "去重新登录", "抖音失效去登录").Clicked() {
						a.OpenLoginForSite("douyin")
					}
				})
			return
		}
		if a.DouyinWorksError != "" {
			a.renderUserListError(c, a.DouyinWorksError, "重试加载抖音作品", a.LoadMoreDouyinWorks)
			return
		}
		if a.isPending("douyin-works-" + u.SecUID) {
			ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
				ui.Spinner(c).Size(16, 16)
				ui.Text(c, "正在加载作品…").FontSize(13).TextColor(ColorTextMuted).SingleLine()
			})
			return
		}
		if a.DouyinWorksHasMore {
			if OutlinePillButton(c, "加载更多作品", "加载更多抖音作品").Clicked() {
				a.LoadMoreDouyinWorks()
			}
			return
		}
		if len(a.CurrentDouyinWorks) == 0 {
			ui.Text(c, "还没有发布公开作品").FontSize(13).TextColor(ColorTextMuted).SingleLine()
		} else {
			ui.Text(c, "已经到底了").FontSize(13).TextColor(ColorTextMuted).SingleLine()
		}
	})
}
