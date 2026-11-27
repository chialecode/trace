package ui

import (
	"fmt"
	"strings"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

var xUserTabs = []TabOption{
	{Key: "posts", Text: "帖子", AccessLabel: "X用户标签：帖子"},
	{Key: "replies", Text: "回复", AccessLabel: "X用户标签：回复"},
	{Key: "media", Text: "媒体", AccessLabel: "X用户标签：媒体"},
	{Key: "highlights", Text: "精选", AccessLabel: "X用户标签：精选"},
	{Key: "articles", Text: "文章", AccessLabel: "X用户标签：文章"},
}

func xUserTabDisplayLabel(tab string) string {
	switch normalizeXUserTabUI(tab) {
	case "replies":
		return "回复"
	case "media":
		return "媒体"
	case "highlights":
		return "精选"
	case "articles":
		return "文章"
	default:
		return "帖子"
	}
}

func xUserTabEmptyText(tab string, protected bool) string {
	if protected {
		return "该账号帖子受保护，当前不可查看"
	}
	switch normalizeXUserTabUI(tab) {
	case "replies":
		return "暂无回复"
	case "media":
		return "暂无媒体帖子"
	case "highlights":
		return "暂无精选帖子"
	case "articles":
		return "暂无文章"
	default:
		return "暂无帖子"
	}
}

func xUserTabBrowserURL(u *service.XUserDTO, tab string) string {
	if u == nil {
		return "https://x.com/"
	}
	handle := strings.TrimPrefix(strings.TrimSpace(u.ScreenName), "@")
	normTab := normalizeXUserTabUI(tab)
	if handle != "" {
		switch normTab {
		case "replies":
			return "https://x.com/" + handle + "/with_replies"
		case "media":
			return "https://x.com/" + handle + "/media"
		case "highlights":
			return "https://x.com/" + handle + "/highlights"
		case "articles":
			return "https://x.com/" + handle + "/articles"
		default:
			return "https://x.com/" + handle
		}
	}
	base := firstNonEmptyStrUI(u.CanonicalURL, "https://x.com/i/user/"+u.RestID)
	return base
}

func (a *App) xPostKeys(posts []service.XPostDTO) []string {
	keys := make([]string, len(posts))
	for i, p := range posts {
		if p.CacheKey != "" {
			keys[i] = p.CacheKey
		} else if p.ID != "" {
			keys[i] = library.CanonicalItemKey("x", "dynamic", p.ID)
		}
	}
	return keys
}

func (a *App) filteredXPostIndices(uid, tab string) []int {
	n := len(a.CurrentXPosts)
	if a.UserListFilter != userFilterUncached {
		idx := make([]int, n)
		for i := range n {
			idx[i] = i
		}
		return idx
	}
	return a.uncachedIndices("x:"+normalizeXUserTabUI(tab)+":"+uid, a.xPostKeys(a.CurrentXPosts))
}

func (a *App) countXCurrentTabCacheableUncached() int {
	if len(a.CurrentXPosts) == 0 {
		return 0
	}
	keys := a.xPostKeys(a.CurrentXPosts)
	var states map[string]library.DotState
	if a.backend != nil {
		var ask []string
		for _, k := range keys {
			if k != "" {
				ask = append(ask, k)
			}
		}
		if len(ask) > 0 {
			states = a.backend.States(ask)
		}
	}
	count := 0
	for i := range a.CurrentXPosts {
		p := &a.CurrentXPosts[i]
		if service.IsXPostDTOUncacheable(p) {
			continue
		}
		k := keys[i]
		if st, ok := states[k]; ok && (st.Dot == "green" || st.Dot == "ring") {
			continue
		}
		count++
	}
	return count
}

// renderXUserRoute draws /x/user/:uid (docs/ui.md §3.4, DEC-092):
// header matching Bilibili/Weibo user page structure, 4 tabs ("帖子" / "回复" / "媒体" / "精选")
// with right-side "全部 / 未缓存" filter, pinned badge, visible-limit hint, protected notice,
// and fake-shell/risk errors staying in place with a retry button.
func (a *App) renderXUserRoute(c *ui.Context) {
	if a.Opening != nil {
		a.renderOpening(c)
		return
	}
	u := a.CurrentXUser
	if u == nil {
		ui.Column(c).Grow(1).FillWidth().Padding(32, 16).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "未加载 X 用户主页").FontSize(15).FontWeight(600).TextColor(ColorTextMain)
			ui.Text(c, "可在解析页粘贴 X 用户主页链接或 @用户名 打开。").FontSize(13).TextColor(ColorTextMuted)
		})
		return
	}
	if a.XUserListState == nil {
		a.XUserListState = &ui.ListState{}
	}

	activeTab := a.activeXUserTab()
	pendingKey := a.xPostsPendingKey(u.RestID, activeTab)
	posts := a.CurrentXPosts
	idx := a.filteredXPostIndices(u.RestID, activeTab)
	a.xFilteredEmpty = a.UserListFilter == userFilterUncached && len(posts) > 0 && len(idx) == 0

	// Auto-load next page when scrolled near the bottom and no error/fake-shell blocks it.
	_, last := a.XUserListState.Visible()
	if last >= 0 && last >= len(idx)-1 && a.XPostsHasMore && a.XPostsError == "" && !a.XPostsAuthExpired && !a.XPostsUncertainEmpty && !a.isPending(pendingKey) {
		a.LoadMoreXPosts()
	}

	// Row 0: header; rows 1..len(idx): posts; final row: footer/empty/end state.
	// Use the frame-local posts snapshot so a mid-frame tab switch in row 0
	// cannot invalidate indices for rows 1..len(idx) before the next frame.
	totalRows := len(idx) + 2
	a.XUserListState.Key = func(i int) any {
		if i == 0 {
			return "x-user-header:" + u.RestID + ":" + activeTab
		}
		if i > 0 && i <= len(idx) {
			if pIdx := idx[i-1]; pIdx >= 0 && pIdx < len(posts) {
				return "x-post:" + activeTab + ":" + posts[pIdx].ID
			}
		}
		return "x-user-footer:" + u.RestID + ":" + activeTab
	}

	ui.List(c, a.XUserListState, totalRows, func(i int) {
		if i == 0 {
			a.renderXUserHeader(c, u, activeTab)
			return
		}
		if i > 0 && i <= len(idx) {
			if pIdx := idx[i-1]; pIdx >= 0 && pIdx < len(posts) {
				a.renderPostCard(c, a.xPostToPostCard(posts[pIdx], u))
			}
			return
		}
		a.renderXUserFooter(c, u, activeTab, len(idx))
	}).Label("X用户列表").Grow(1).FillWidth()
}

// renderXUserHeader mirrors Bilibili/Weibo user page header structure:
// gradient banner with avatar/name/@handle/verified, stats bar with "X" site badge +
// "帖子 N" + subscribe button + "⋯" menu, bio, protected notice, and 4 profile tabs + "全部 / 未缓存".
func (a *App) renderXUserHeader(c *ui.Context, u *service.XUserDTO, activeTab string) {
	handle := strings.TrimPrefix(strings.TrimSpace(u.ScreenName), "@")
	name := firstNonEmptyStrUI(u.Name, handle, "X 用户")
	seed := accountSeedHash("x", firstNonEmptyStrUI(u.RestID, handle, name))
	pal := AvatarColor(name, seed)

	ui.Column(c).FillWidth().BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).ClipX().Children(func() {
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
				ui.Row(c).Fill().Padding(12, 16).AlignItems(ui.Center).Gap(12).ClipX().Children(func() {
					ui.Box(c).Padding(3).Radius(999).Background(ColorWhite).Shrink(0).Children(func() {
						a.renderAccountAvatar(c, "x", name, u.RestID, 0, 54, false, "", nil, u.AvatarURL)
					})
					ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
						ui.Row(c).AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
							ui.Text(c, name).FontSize(18).FontWeight(600).TextColor(ColorWhite).SingleLine().Shrink(1)
							if u.Verified || u.IsBlueVerified {
								ui.Box(c).Label("X认证标记").Padding(1, 6).Radius(999).Background(ui.RGBA(255, 255, 255, 0.22)).Shrink(0).Children(func() {
									ui.Text(c, "已认证").FontSize(11).FontWeight(600).TextColor(ColorWhite).SingleLine()
								})
							}
						})
						var subParts []string
						if handle != "" {
							subParts = append(subParts, "@"+handle)
						}
						if u.RestID != "" {
							subParts = append(subParts, "ID "+u.RestID)
						}
						if len(subParts) > 0 {
							ui.Text(c, strings.Join(subParts, " · ")).FontSize(12).FontWeight(600).TextColor(ui.RGBA(255, 255, 255, 0.88)).SingleLine()
						}
					})
				})
			})

		ui.Column(c).FillWidth().Padding(10, 16, 6, 16).Gap(8).ClipX().Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(10).Grow(1).ClipX().Children(func() {
					renderSiteSmallBadge(c, "x", "用户页站点标记")
					ui.Text(c, "·").FontSize(12).TextColor(ColorTextMuted).SingleLine()
					ui.Text(c, fmt.Sprintf("关注 %s", FormatCount(int64(u.FriendsCount)))).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					ui.Text(c, fmt.Sprintf("粉丝 %s", FormatCount(int64(u.FollowersCount)))).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					ui.Box(c).Label("X帖子总数").Children(func() {
						ui.Text(c, fmt.Sprintf("帖子 %s", FormatCount(int64(u.StatusesCount)))).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					})
				})
				ui.Row(c).Shrink(0).Gap(6).AlignItems(ui.Center).Children(func() {
					ownerSub, owned := a.ownerOfAccountInMemory("x", u.RestID, 0)
					if owned {
						if OutlinePillButton(c, "已订阅", "进入所属订阅").Clicked() {
							a.OpenSubscriptionDetail(ownerSub)
						}
					} else if AccentPillButton(c, "订阅", "订阅该X用户").Clicked() {
						a.OpenSubscribePicker(subscription.AccountRef{
							Site:     "x",
							UID:      u.RestID,
							UniqueID: handle,
							Name:     name,
							Face:     u.AvatarURL,
						})
					}
					targetURL := xUserTabBrowserURL(u, activeTab)
					open := a.XUserSubMenuOpen
					MoreMenuButton(c, "X主页更多", 32, &open, func(m *ui.Menu) {
						if activeTab == "posts" {
							a.cacheOlderMenu(m, "x")
						} else if !(u.Protected && len(a.CurrentXPosts) == 0) {
							uncachedCnt := a.countXCurrentTabCacheableUncached()
							busy := a.isPending("x-batch-cache-" + u.RestID + "-" + activeTab)
							item := m.Item("缓存当前页未缓存").Disabled(uncachedCnt == 0 || busy)
							if uncachedCnt == 0 {
								item.Tooltip("当前页没有可缓存的未缓存条目")
							}
							if item.Chosen() {
								a.CacheXCurrentTabUncached()
							}
						}
						if u.RestID != "" && m.Item("查看公开列表").Chosen() {
							a.OpenXUserListsModal(u.RestID, name)
						}
						if m.Item("在浏览器中打开").Chosen() {
							if err := a.backend.Desktop().OpenExternalURL(targetURL); err != nil {
								a.showError(err)
							}
						}
					})
					a.XUserSubMenuOpen = open
				})
			})

			if strings.TrimSpace(u.Description) != "" {
				ui.Text(c, u.Description).FontSize(13).LineHeight(LineHeightCompact).TextColor(ColorTextMuted).MaxLines(3)
			}
			if u.Protected {
				ui.Row(c).Label("X受保护账号提示").FillWidth().Padding(8, 12).Radius(8).Background(ColorWarnBg).Border(1, ColorDotYellow).AlignItems(ui.Center).Gap(8).ClipX().Children(func() {
					DrawIcon(c, "alert-circle", 15, ColorDotYellow, 1.8)
					ui.Text(c, "这是一个受保护的账号，只有已获批准的关注者才能查看其帖子。").FontSize(12).LineHeight(LineHeightCompact).TextColor(ColorTextMain).MaxLines(2)
				})
			}
		})

		// 4 profile tabs ("帖子" / "回复" / "媒体" / "精选") + right-side "全部 / 未缓存" filter (DEC-092).
		ui.Row(c).FillWidth().AlignItems(ui.End).Gap(8).ClipX().Children(func() {
			ui.Box(c).Grow(1).ClipX().Children(func() {
				UnderlineTabs(c, activeTab, xUserTabs, func(key string) {
					a.SelectXUserTab(key)
				})
			})
			ui.Box(c).Padding(0, 16, 6, 0).Shrink(0).Children(func() { a.renderUserListFilter(c) })
		})
	})
}

func (a *App) renderXUserFooter(c *ui.Context, u *service.XUserDTO, activeTab string, visibleCount int) {
	tabLabel := xUserTabDisplayLabel(activeTab)
	busy := a.isPending(a.xPostsPendingKey(u.RestID, activeTab))

	ui.Column(c).FillWidth().Padding(14, 16).Gap(10).AlignItems(ui.Center).ClipX().Children(func() {
		if a.xFilteredEmpty {
			ui.Text(c, "已加载的内容都缓存了").Label("未缓存筛选为空").FontSize(13).TextColor(ColorTextMuted).SingleLine()
		}

		if a.XPostsAuthExpired {
			renderErrorBanner(c, "X失效提示", "X 登录已失效，请重新粘贴 Cookie", func() {
				if AccentPillButton(c, "去登录", "X失效去登录").Clicked() {
					a.OpenLoginForSite("x")
				}
			})
			return
		}

		if a.XPostsUncertainEmpty {
			msg := firstNonEmptyStrUI(a.XPostsError, "X 返回了异常空列表，可能是限流、风控或受限，可稍后重试")
			renderErrorBanner(c, "X空壳提示", msg, func() {
				if OutlinePillButton(c, "重试", "重试加载X帖子").Clicked() {
					a.LoadMoreXPosts()
				}
			})
			return
		}

		if a.XPostsError != "" {
			renderErrorBanner(c, "X列表错误", a.XPostsError, func() {
				if OutlinePillButton(c, "重试", "重试加载X帖子").Clicked() {
					a.LoadMoreXPosts()
				}
			})
			return
		}

		if a.XBottomTipsVisible {
			hint := firstNonEmptyStrUI(a.XBottomTipsText, "已到达 X 时间线可见上限")
			ui.Row(c).
				Label("X时间线可见上限提示").
				FillWidth().
				Padding(10, 14).
				Radius(10).
				Background(ColorBgSecondary).
				Justify(ui.Center).
				AlignItems(ui.Center).
				Children(func() {
					ui.Text(c, hint).FontSize(13).LineHeight(LineHeightCompact).TextColor(ColorTextMuted).MaxLines(2)
				})
			return
		}

		if busy && len(a.CurrentXPosts) == 0 {
			ui.Row(c).Label("X标签加载中").AlignItems(ui.Center).Gap(6).Children(func() {
				ui.Spinner(c).Size(14, 14)
				ui.Text(c, "正在加载"+tabLabel+"…").FontSize(13).TextColor(ColorTextMuted).SingleLine()
			})
			return
		}

		if a.XPostsHasMore {
			if busy {
				ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
					ui.Spinner(c).Size(14, 14)
					ui.Text(c, "正在加载更多"+tabLabel+"…").FontSize(13).TextColor(ColorTextMuted).SingleLine()
				})
			} else if OutlinePillButton(c, "加载更多"+tabLabel, "加载更多X帖子").Clicked() {
				a.LoadMoreXPosts()
			}
			return
		}

		if len(a.CurrentXPosts) == 0 {
			ui.Text(c, xUserTabEmptyText(activeTab, u.Protected)).FontSize(13).TextColor(ColorTextMuted).SingleLine()
			return
		}

		if visibleCount > 0 {
			ui.Text(c, "已经到底了").FontSize(13).TextColor(ColorTextMuted).SingleLine()
		}
	})
}
