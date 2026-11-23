package ui

import (
	"fmt"
	"strings"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

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

func (a *App) filteredXPostIndices(uid string) []int {
	n := len(a.CurrentXPosts)
	if a.UserListFilter != userFilterUncached {
		idx := make([]int, n)
		for i := range n {
			idx[i] = i
		}
		return idx
	}
	return a.uncachedIndices("x:posts:"+uid, a.xPostKeys(a.CurrentXPosts))
}

// renderXUserRoute draws /x/user/:uid (docs/ui.md, docs/research/x.md §12.3):
// header matching Bilibili/Weibo user page structure, single "帖子" list with
// "全部 / 未缓存" filter, pinned badge, visible-limit hint, protected notice,
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

	idx := a.filteredXPostIndices(u.RestID)
	a.xFilteredEmpty = a.UserListFilter == userFilterUncached && len(a.CurrentXPosts) > 0 && len(idx) == 0

	// Auto-load next page when scrolled near the bottom and no error/fake-shell blocks it.
	_, last := a.XUserListState.Visible()
	if last >= 0 && last >= len(idx)-1 && a.XPostsHasMore && a.XPostsError == "" && !a.XPostsAuthExpired && !a.XPostsUncertainEmpty && !a.isPending("x-posts-"+u.RestID) {
		a.LoadMoreXPosts()
	}

	// Row 0: header; rows 1..len(idx): posts; final row: footer/empty/end state.
	totalRows := len(idx) + 2
	a.XUserListState.Key = func(i int) any {
		if i == 0 {
			return "x-user-header:" + u.RestID
		}
		if i > 0 && i <= len(idx) {
			return "x-post:" + a.CurrentXPosts[idx[i-1]].ID
		}
		return "x-user-footer:" + u.RestID
	}

	ui.List(c, a.XUserListState, totalRows, func(i int) {
		if i == 0 {
			a.renderXUserHeader(c, u)
			return
		}
		if i > 0 && i <= len(idx) {
			p := a.CurrentXPosts[idx[i-1]]
			a.renderPostCard(c, a.xPostToPostCard(p, u))
			return
		}
		a.renderXUserFooter(c, u, len(idx))
	}).Label("X用户列表").Grow(1).FillWidth()
}

// renderXUserHeader mirrors Bilibili/Weibo user page header structure:
// gradient banner with avatar/name/@handle/verified, stats bar with "X" site badge +
// "帖子 N" + subscribe button + "⋯" menu, bio, protected notice, and single "帖子" + "全部 / 未缓存".
func (a *App) renderXUserHeader(c *ui.Context, u *service.XUserDTO) {
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

		ui.Column(c).FillWidth().Padding(10, 16).Gap(8).ClipX().Children(func() {
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
					targetURL := firstNonEmptyStrUI(u.CanonicalURL, func() string {
						if handle != "" {
							return "https://x.com/" + handle
						}
						return "https://x.com/i/user/" + u.RestID
					}())
					open := a.XUserSubMenuOpen
					MoreMenuButton(c, "X主页更多", 32, &open, func(m *ui.Menu) {
						a.cacheOlderMenu(m, "x")
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

			// Single "帖子" list heading + "全部 / 未缓存" filter (no tabs, no live banner)
			ui.Row(c).FillWidth().Padding(4, 0, 0, 0).AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
				ui.Text(c, "帖子").FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
				a.renderUserListFilter(c)
			})
		})
	})
}

func (a *App) renderXUserFooter(c *ui.Context, u *service.XUserDTO, visibleCount int) {
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

		if a.XPostsHasMore {
			busy := a.isPending("x-posts-" + u.RestID)
			if busy {
				ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
					ui.Spinner(c).Size(14, 14)
					ui.Text(c, "正在加载更多帖子…").FontSize(13).TextColor(ColorTextMuted).SingleLine()
				})
			} else if OutlinePillButton(c, "加载更多帖子", "加载更多X帖子").Clicked() {
				a.LoadMoreXPosts()
			}
			return
		}

		if len(a.CurrentXPosts) == 0 {
			emptyMsg := "暂无帖子"
			if u.Protected {
				emptyMsg = "该账号帖子受保护，当前不可查看"
			}
			ui.Text(c, emptyMsg).FontSize(13).TextColor(ColorTextMuted).SingleLine()
			return
		}

		if visibleCount > 0 {
			ui.Text(c, "已经到底了").FontSize(13).TextColor(ColorTextMuted).SingleLine()
		}
	})
}
