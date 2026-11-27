package ui

import (
	"fmt"
	"strings"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

func (a *App) weiboPostKeys(posts []service.WeiboPostView) []string {
	keys := make([]string, len(posts))
	for i, p := range posts {
		if p.CacheKey != "" {
			keys[i] = p.CacheKey
		} else if p.Mid != "" {
			keys[i] = library.CanonicalItemKey("weibo", "dynamic", p.Mid)
		}
	}
	return keys
}

func (a *App) filteredWeiboPostIndices(uid string) []int {
	n := len(a.CurrentWeiboPosts)
	if a.UserListFilter != userFilterUncached {
		idx := make([]int, n)
		for i := range n {
			idx[i] = i
		}
		return idx
	}
	return a.uncachedIndices("weibo:posts:"+uid, a.weiboPostKeys(a.CurrentWeiboPosts))
}

// renderWeiboUserRoute draws /weibo/user/:uid (docs/ui.md):
// header matching Bilibili user page structure, single "微博" list with
// "全部 / 未缓存" filter, pinned badge, visibility-window hint, and distinct
// empty-shell vs genuine 0 posts vs normal end states.
func (a *App) renderWeiboUserRoute(c *ui.Context) {
	if a.Opening != nil {
		a.renderOpening(c)
		return
	}
	u := a.CurrentWeiboUser
	if u == nil {
		ui.Column(c).Grow(1).FillWidth().Padding(32, 16).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "未加载微博博主主页").FontSize(15).FontWeight(600).TextColor(ColorTextMain)
			ui.Text(c, "可在解析页粘贴微博用户主页链接或分享文本打开。").FontSize(13).TextColor(ColorTextMuted)
		})
		return
	}
	if a.WeiboUserListState == nil {
		a.WeiboUserListState = &ui.ListState{}
	}

	idx := a.filteredWeiboPostIndices(u.UID)
	a.weiboFilteredEmpty = a.UserListFilter == userFilterUncached && len(a.CurrentWeiboPosts) > 0 && len(idx) == 0

	// Auto-load next page when scrolled near the bottom and no error/empty-shell blocks it.
	_, last := a.WeiboUserListState.Visible()
	if last >= 0 && last >= len(idx)-1 && a.WeiboPostsHasMore && a.WeiboPostsError == "" && !a.WeiboPostsAuthExpired && !a.WeiboPostsUncertainEmpty && !a.isPending("weibo-posts-"+u.UID) {
		a.LoadMoreWeiboPosts()
	}

	// Row 0: header; rows 1..len(idx): posts; final row: footer/empty/end state.
	totalRows := len(idx) + 2
	a.WeiboUserListState.Key = func(i int) any {
		if i == 0 {
			return "wb-user-header:" + u.UID
		}
		if i > 0 && i <= len(idx) {
			return "wb-post:" + a.CurrentWeiboPosts[idx[i-1]].Mid
		}
		return "wb-user-footer:" + u.UID
	}

	ui.List(c, a.WeiboUserListState, totalRows, func(i int) {
		if i == 0 {
			a.renderWeiboUserHeader(c, u)
			return
		}
		if i > 0 && i <= len(idx) {
			p := a.CurrentWeiboPosts[idx[i-1]]
			a.renderPostCard(c, a.weiboPostToPostCard(p, u))
			return
		}
		a.renderWeiboUserFooter(c, u, len(idx))
	}).Label("微博用户列表").Grow(1).FillWidth()
}

// renderWeiboUserHeader mirrors Bilibili user page header structure:
// gradient banner with avatar/name/UID, stats bar with "微博" site badge +
// "微博 N" + subscribe button + "⋯" menu, bio, and single "微博" heading + "全部 / 未缓存".
func (a *App) renderWeiboUserHeader(c *ui.Context, u *service.WeiboUserView) {
	name := firstNonEmptyStrUI(u.ScreenName, "微博用户")
	seed := accountSeedHash("weibo", firstNonEmptyStrUI(u.UID, name))
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
						a.renderAccountAvatar(c, "weibo", name, u.UID, 0, 54, false, "", nil, u.AvatarURL)
					})
					ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
						ui.Text(c, name).FontSize(18).FontWeight(600).TextColor(ColorWhite).SingleLine()
						subLine := "UID " + u.UID
						if strings.TrimSpace(u.Domain) != "" {
							subLine += " · @" + strings.TrimSpace(u.Domain)
						}
						if strings.TrimSpace(u.VerifiedReason) != "" {
							subLine += " · " + strings.TrimSpace(u.VerifiedReason)
						}
						ui.Text(c, subLine).FontSize(12).FontWeight(600).TextColor(ui.RGBA(255, 255, 255, 0.88)).SingleLine()
					})
				})
			})

		ui.Column(c).FillWidth().Padding(10, 16).Gap(8).ClipX().Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(10).Grow(1).ClipX().Children(func() {
					renderSiteSmallBadge(c, "weibo", "用户页站点标记")
					ui.Text(c, "·").FontSize(12).TextColor(ColorTextMuted).SingleLine()
					ui.Text(c, fmt.Sprintf("关注 %s", FormatCount(u.FriendsCount))).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					ui.Text(c, fmt.Sprintf("粉丝 %s", FormatCount(u.FollowersCount))).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					ui.Box(c).Label("微博总数").Children(func() {
						ui.Text(c, fmt.Sprintf("微博 %s", FormatCount(int64(u.StatusesCount)))).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					})
				})
				ui.Row(c).Shrink(0).Gap(6).AlignItems(ui.Center).Children(func() {
					ownerSub, owned := a.ownerOfAccountInMemory("weibo", u.UID, 0)
					if owned {
						if OutlinePillButton(c, "已订阅", "进入所属订阅").Clicked() {
							a.OpenSubscriptionDetail(ownerSub)
						}
					} else if AccentPillButton(c, "订阅", "订阅该微博博主").Clicked() {
						a.OpenSubscribePicker(subscription.AccountRef{
							Site: "weibo",
							UID:  u.UID,
							Name: name,
							Face: u.AvatarURL,
						})
					}
					targetURL := firstNonEmptyStrUI(u.CanonicalURL, "https://weibo.com/u/"+u.UID)
					open := a.WeiboUserSubMenuOpen
					MoreMenuButton(c, "微博主页更多", 32, &open, func(m *ui.Menu) {
						a.cacheOlderMenu(m, "weibo")
						if m.Item("在浏览器中打开").Chosen() {
							if err := a.backend.Desktop().OpenExternalURL(targetURL); err != nil {
								a.showError(err)
							}
						}
					})
					a.WeiboUserSubMenuOpen = open
				})
			})

			if strings.TrimSpace(u.Description) != "" {
				ui.Text(c, u.Description).FontSize(13).LineHeight(LineHeightCompact).TextColor(ColorTextMuted).MaxLines(2)
			}
			if strings.TrimSpace(u.BlockText) != "" {
				ui.Row(c).Label("微博封禁或受限提示").FillWidth().Padding(8, 12).Radius(8).Background(ColorWarnBg).Border(1, ColorDotYellow).AlignItems(ui.Center).Gap(8).ClipX().Children(func() {
					DrawIcon(c, "alert-circle", 15, ColorDotYellow, 1.8)
					ui.Text(c, u.BlockText).FontSize(12).LineHeight(LineHeightCompact).TextColor(ColorTextMain).MaxLines(2)
				})
			} else if u.IsMuteUser {
				ui.Row(c).Label("微博禁言提示").FillWidth().Padding(8, 12).Radius(8).Background(ColorWarnBg).Border(1, ColorDotYellow).AlignItems(ui.Center).Gap(8).ClipX().Children(func() {
					DrawIcon(c, "alert-circle", 15, ColorDotYellow, 1.8)
					ui.Text(c, "该微博用户目前处于禁言状态，暂无法查看微博").FontSize(12).LineHeight(LineHeightCompact).TextColor(ColorTextMain).MaxLines(2)
				})
			}

			// Single "微博" list heading + "全部 / 未缓存" filter (no tabs, no live banner)
			ui.Row(c).FillWidth().Padding(4, 0, 0, 0).AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
				ui.Text(c, "微博").FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
				a.renderUserListFilter(c)
			})
		})
	})
}

func (a *App) renderWeiboUserFooter(c *ui.Context, u *service.WeiboUserView, visibleCount int) {
	ui.Column(c).FillWidth().Padding(14, 16).Gap(10).AlignItems(ui.Center).ClipX().Children(func() {
		if a.weiboFilteredEmpty {
			ui.Text(c, "已加载的内容都缓存了").Label("未缓存筛选为空").FontSize(13).TextColor(ColorTextMuted).SingleLine()
		}

		if a.WeiboPostsAuthExpired {
			renderErrorBanner(c, "微博失效提示", "微博登录已失效，请重新粘贴 Cookie", func() {
				if AccentPillButton(c, "去登录", "微博失效去登录").Clicked() {
					a.OpenLoginForSite("weibo")
				}
			})
			return
		}

		if u.IsMuteUser && len(a.CurrentWeiboPosts) == 0 && !a.WeiboPostsHasMore {
			ui.Row(c).
				Label("微博禁言空态提示").
				FillWidth().
				Padding(10, 14).
				Radius(10).
				Background(ColorBgSecondary).
				Justify(ui.Center).
				AlignItems(ui.Center).
				Children(func() {
					ui.Text(c, "该微博用户目前处于禁言状态，暂无法查看微博").FontSize(13).LineHeight(LineHeightCompact).TextColor(ColorTextMuted).MaxLines(2)
				})
			return
		}

		if a.WeiboPostsUncertainEmpty {
			msg := firstNonEmptyStrUI(a.WeiboPostsError, "第 1 页暂无可见微博（可能为全私密、超出可见时间窗或账号受限），可稍后重试")
			renderErrorBanner(c, "微博空壳提示", msg, func() {
				if OutlinePillButton(c, "重试", "重试加载微博").Clicked() {
					a.LoadMoreWeiboPosts()
				}
			})
			return
		}

		if a.WeiboPostsError != "" {
			renderErrorBanner(c, "微博列表错误", a.WeiboPostsError, func() {
				if OutlinePillButton(c, "重试", "重试加载微博").Clicked() {
					a.LoadMoreWeiboPosts()
				}
			})
			return
		}

		if a.WeiboBottomTipsVisible {
			hint := firstNonEmptyStrUI(a.WeiboBottomTipsText, "博主设置了微博可见范围，更早的微博不可查看")
			ui.Row(c).
				Label("微博可见范围提示").
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

		if a.WeiboPostsHasMore {
			busy := a.isPending("weibo-posts-" + u.UID)
			btnText := "加载更多微博"
			if busy {
				btnText = "正在加载…"
			}
			if OutlinePillButton(c, btnText, "加载更多微博").Disabled(busy).Clicked() && !busy {
				a.LoadMoreWeiboPosts()
			}
			return
		}

		if len(a.CurrentWeiboPosts) == 0 {
			ui.Box(c).Label("微博零内容提示").Children(func() {
				ui.Text(c, "还没有发布公开微博").FontSize(13).TextColor(ColorTextMuted).SingleLine()
			})
			return
		}

		if visibleCount > 0 {
			ui.Text(c, "已经到底了").FontSize(13).TextColor(ColorTextMuted).SingleLine()
		}
	})
}
