package ui

import (
	"context"
	"fmt"
	"strings"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

// XListViewState holds in-memory browsing state for the read-only X List detail page (/x/list, DEC-093).
type XListViewState struct {
	List              *service.XListDTO
	Tab               string // "posts" | "members"
	Posts             []service.XPostDTO
	PostsNextCursor   string
	PostsHasMore      bool
	PostsError        string
	PostsLoaded       bool
	PostsListState    *ui.ListState
	Members           []service.XListMemberDTO
	MembersNextCursor string
	MembersHasMore    bool
	MembersError      string
	MembersLoaded     bool
	MembersListState  *ui.ListState
	HeaderMenuOpen    bool
}

// XUserListsModalState holds modal state for viewing an X user's public Lists.
type XUserListsModalState struct {
	Open       bool
	UserRestID string
	UserName   string
	Lists      []service.XListDTO
	Error      string
}

var xListTabs = []TabOption{
	{Key: "posts", Text: "帖子", AccessLabel: "X列表标签：帖子"},
	{Key: "members", Text: "成员", AccessLabel: "X列表标签：成员"},
}

func normalizeXListTabUI(tab string) string {
	if strings.EqualFold(strings.TrimSpace(tab), "members") {
		return "members"
	}
	return "posts"
}

// OpenXList opens a public X List detail page by numeric list ID or URL.
func (a *App) OpenXList(listID, name string) {
	raw := strings.TrimSpace(listID)
	if raw == "" {
		return
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "https://x.com/i/lists/" + raw
	}
	a.XUserLists.Open = false
	a.openLink(raw, &OpeningPreview{
		Site:  string(site.X),
		Kind:  "x_list",
		Title: firstNonEmptyStrUI(strings.TrimSpace(name), "X 列表"),
	})
}

// SelectXListTab switches between "posts" and "members" on /x/list, lazily loading members on first view.
func (a *App) SelectXListTab(tab string) {
	nextTab := normalizeXListTabUI(tab)
	a.XList.Tab = nextTab
	a.XList.HeaderMenuOpen = false
	if nextTab == "members" && a.XList.List != nil && !a.XList.MembersLoaded && len(a.XList.Members) == 0 && a.XList.MembersError == "" {
		a.LoadMoreXListMembers()
	}
}

func (a *App) applyXListParseResult(res *service.OpenLinkResult) {
	tab := normalizeXListTabUI(res.XListTab)
	a.XList = XListViewState{
		List:             res.XList,
		Tab:              tab,
		PostsError:       res.XListTweetsError,
		PostsLoaded:      res.XListTweets != nil || res.XListTweetsError != "",
		PostsListState:   &ui.ListState{},
		MembersListState: &ui.ListState{},
	}
	if res.XListTweets != nil {
		a.XList.Posts = res.XListTweets.Posts
		a.XList.PostsNextCursor = res.XListTweets.NextCursor
		a.XList.PostsHasMore = res.XListTweets.HasMore
	}
	if a.CurrentRoute != RouteXList {
		a.pushPreviousRoute(a.currentReturnRoute())
	}
	a.CurrentRoute = RouteXList
	if tab == "members" && res.XList != nil {
		a.LoadMoreXListMembers()
	}
}

// LoadMoreXListTweets fetches the next page of tweets for the currently open X List.
func (a *App) LoadMoreXListTweets() {
	if a.XList.List == nil || a.XList.List.ID == "" {
		return
	}
	listID := a.XList.List.ID
	cursor := a.XList.PostsNextCursor
	pendingKey := "x-list-tweets-" + listID

	requestGuarded(a, pendingKey, func() bool {
		return a.XList.List != nil && a.XList.List.ID == listID
	}, func(ctx context.Context) (*service.XListTweetsPageDTO, error) {
		return a.backend.XListTweets(ctx, listID, cursor)
	}, func(page *service.XListTweetsPageDTO) {
		if a.XList.List == nil || a.XList.List.ID != listID || page == nil {
			return
		}
		a.XList.PostsLoaded = true
		seen := make(map[string]bool, len(a.XList.Posts))
		for _, p := range a.XList.Posts {
			seen[p.ID] = true
		}
		for _, p := range page.Posts {
			if p.ID != "" && !seen[p.ID] {
				a.XList.Posts = append(a.XList.Posts, p)
				seen[p.ID] = true
			}
		}
		a.XList.PostsNextCursor = page.NextCursor
		a.XList.PostsHasMore = page.HasMore
		a.XList.PostsError = ""
	}, func(err error) {
		if a.XList.List == nil || a.XList.List.ID != listID {
			return
		}
		a.XList.PostsLoaded = true
		a.XList.PostsError = userErrorText(err)
	})
}

// LoadMoreXListMembers fetches the next page of members for the currently open X List.
func (a *App) LoadMoreXListMembers() {
	if a.XList.List == nil || a.XList.List.ID == "" {
		return
	}
	listID := a.XList.List.ID
	cursor := a.XList.MembersNextCursor
	pendingKey := "x-list-members-" + listID

	requestGuarded(a, pendingKey, func() bool {
		return a.XList.List != nil && a.XList.List.ID == listID
	}, func(ctx context.Context) (*service.XListMembersPageDTO, error) {
		return a.backend.XListMembers(ctx, listID, cursor)
	}, func(page *service.XListMembersPageDTO) {
		if a.XList.List == nil || a.XList.List.ID != listID || page == nil {
			return
		}
		a.XList.MembersLoaded = true
		seen := make(map[string]bool, len(a.XList.Members))
		for _, m := range a.XList.Members {
			seen[m.RestID] = true
		}
		for _, m := range page.Members {
			if m.RestID != "" && !seen[m.RestID] {
				a.XList.Members = append(a.XList.Members, m)
				seen[m.RestID] = true
			}
		}
		a.XList.MembersNextCursor = page.NextCursor
		a.XList.MembersHasMore = page.HasMore
		a.XList.MembersError = ""
	}, func(err error) {
		if a.XList.List == nil || a.XList.List.ID != listID {
			return
		}
		a.XList.MembersLoaded = true
		a.XList.MembersError = userErrorText(err)
	})
}

func (a *App) countXListCacheableUncached() int {
	if len(a.XList.Posts) == 0 {
		return 0
	}
	keys := a.xPostKeys(a.XList.Posts)
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
	for i := range a.XList.Posts {
		p := &a.XList.Posts[i]
		if service.IsXPostDTOUncacheable(p) {
			continue
		}
		if st, ok := states[keys[i]]; ok && (st.Dot == "green" || st.Dot == "ring") {
			continue
		}
		count++
	}
	return count
}

// CacheXListCurrentPageUncached enqueues uncached, cacheable posts currently loaded in /x/list.
func (a *App) CacheXListCurrentPageUncached() {
	if a.XList.List == nil || len(a.XList.Posts) == 0 {
		a.notifyInfo("当前页没有可缓存的未缓存条目")
		return
	}
	listID := a.XList.List.ID
	postsCopy := append([]service.XPostDTO(nil), a.XList.Posts...)
	req := service.XBatchCachePageRequest{
		Tab:   "list",
		Posts: postsCopy,
		Limit: service.MaxXCurrentPageBatchCache,
	}
	requestGuarded(a, "x-list-batch-cache-"+listID, nil, func(ctx context.Context) (service.XBatchCachePageResult, error) {
		return a.backend.CacheXPostsPage(ctx, req)
	}, func(res service.XBatchCachePageResult) {
		a.notifyInfo(formatXBatchCacheSummary(res))
		a.RefreshData()
	}, func(err error) {
		a.showError(err)
		a.RefreshData()
	})
}

func (a *App) unsubscribedXListMembers() []service.FollowingCreatorDTO {
	var out []service.FollowingCreatorDTO
	seen := make(map[string]bool, len(a.XList.Members))
	for _, m := range a.XList.Members {
		uid := strings.TrimSpace(m.RestID)
		if uid == "" || seen[uid] {
			continue
		}
		seen[uid] = true
		if _, owned := a.ownerOfAccountInMemory("x", uid, 0); owned {
			continue
		}
		if m.Subscribed {
			continue
		}
		handle := strings.TrimPrefix(strings.TrimSpace(m.ScreenName), "@")
		out = append(out, service.FollowingCreatorDTO{
			Site:     string(site.X),
			UID:      uid,
			UniqueID: handle,
			Name:     firstNonEmptyStrUI(strings.TrimSpace(m.Name), handle, uid),
			Face:     m.AvatarURL,
			Sign:     m.Description,
		})
	}
	return out
}

// ImportUnsubscribedXListMembers batch-imports all currently loaded, unsubscribed
// X List members into TRACE subscriptions using the DEC-080 default preset (完整存档).
func (a *App) ImportUnsubscribedXListMembers() {
	if a.backend == nil {
		return
	}
	candidates := a.unsubscribedXListMembers()
	if len(candidates) == 0 {
		a.notifyInfo("当前已加载成员均已订阅")
		return
	}
	res, err := a.backend.ImportFollowingCreators(string(site.X), candidates, subscription.DefaultSubRules())
	if err != nil {
		a.showError(err)
		return
	}
	a.RefreshData()
	for i := range a.XList.Members {
		if sub, owned := a.ownerOfAccountInMemory("x", a.XList.Members[i].RestID, 0); owned {
			a.XList.Members[i].Subscribed = true
			a.XList.Members[i].OwnerSubID = sub.ID
			a.XList.Members[i].OwnerSubName = sub.Name
		}
	}
	if res.Created > 0 {
		a.notifyInfo(fmt.Sprintf("已批量导入 %d 个订阅（默认完整存档）", res.Created))
	} else {
		a.notifyInfo("所选成员已存在于订阅中")
	}
}

// OpenXUserListsModal opens a read-only modal displaying an X user's public Lists.
func (a *App) OpenXUserListsModal(restID, userName string) {
	uid := strings.TrimSpace(restID)
	if uid == "" {
		return
	}
	a.XUserLists = XUserListsModalState{
		Open:       true,
		UserRestID: uid,
		UserName:   firstNonEmptyStrUI(strings.TrimSpace(userName), uid),
	}
	requestGuarded(a, "x-user-lists-"+uid, func() bool {
		return a.XUserLists.Open && a.XUserLists.UserRestID == uid
	}, func(ctx context.Context) (*service.XUserListsPageDTO, error) {
		return a.backend.XUserLists(ctx, uid)
	}, func(page *service.XUserListsPageDTO) {
		if !a.XUserLists.Open || a.XUserLists.UserRestID != uid {
			return
		}
		if page != nil {
			a.XUserLists.Lists = page.Lists
		}
		a.XUserLists.Error = ""
	}, func(err error) {
		if !a.XUserLists.Open || a.XUserLists.UserRestID != uid {
			return
		}
		a.XUserLists.Error = userErrorText(err)
	})
}

func (a *App) renderXListRoute(c *ui.Context) {
	if a.Opening != nil {
		a.renderOpening(c)
		return
	}
	ls := a.XList.List
	if ls == nil {
		ui.Column(c).Grow(1).FillWidth().Padding(32, 16).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "未加载 X 列表").FontSize(15).FontWeight(600).TextColor(ColorTextMain)
			ui.Text(c, "可在解析页粘贴 x.com/i/lists/… 公开列表链接打开。").FontSize(13).TextColor(ColorTextMuted)
		})
		return
	}
	activeTab := normalizeXListTabUI(a.XList.Tab)
	if activeTab == "members" {
		a.renderXListMembersTab(c, ls)
		return
	}
	a.renderXListPostsTab(c, ls)
}

func (a *App) renderXListHeader(c *ui.Context, ls *service.XListDTO, activeTab string) {
	listName := firstNonEmptyStrUI(strings.TrimSpace(ls.Name), "X 列表 "+ls.ID)
	canonURL := firstNonEmptyStrUI(strings.TrimSpace(ls.CanonicalURL), "https://x.com/i/lists/"+ls.ID)
	creatorHandle := strings.TrimPrefix(strings.TrimSpace(ls.Creator.ScreenName), "@")
	creatorName := firstNonEmptyStrUI(strings.TrimSpace(ls.Creator.Name), creatorHandle)
	openCreator := func() {
		if target := firstNonEmptyStrUI(strings.TrimSpace(ls.Creator.RestID), creatorHandle); target != "" {
			a.OpenXUser(target, creatorName)
		}
	}

	ui.Column(c).Label("X列表头部").FillWidth().BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).ClipX().Children(func() {
		ui.Column(c).FillWidth().Padding(14, 16, 10, 16).Gap(8).ClipX().Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(8).Grow(1).Basis(0).ClipX().Children(func() {
					renderSiteSmallBadge(c, "x", "X列表站点标记")
					ui.Box(c).Padding(1, 6).Radius(4).Background(ColorBgSecondary).Shrink(0).Children(func() {
						ui.Text(c, "列表").FontSize(11).FontWeight(600).TextColor(ColorTextMuted).SingleLine()
					})
					ui.Box(c).Label("X列表名称").Shrink(1).ClipX().Children(func() {
						ui.Text(c, listName).FontSize(18).FontWeight(700).TextColor(ColorTextMain).SingleLine()
					})
				})
				ui.Row(c).Shrink(0).AlignItems(ui.Center).Gap(6).Children(func() {
					if OutlinePillButton(c, "在浏览器中打开", "X列表浏览器打开").Clicked() {
						if err := a.backend.Desktop().OpenExternalURL(canonURL); err != nil {
							a.showError(err)
						}
					}
					open := a.XList.HeaderMenuOpen
					MoreMenuButton(c, "X列表更多", 32, &open, func(m *ui.Menu) {
						if activeTab == "posts" {
							uncachedCnt := a.countXListCacheableUncached()
							busy := a.isPending("x-list-batch-cache-" + ls.ID)
							item := m.Item("缓存当前页未缓存").Disabled(uncachedCnt == 0 || busy)
							if uncachedCnt == 0 {
								item.Tooltip("当前页没有可缓存的未缓存条目")
							}
							if item.Chosen() {
								a.CacheXListCurrentPageUncached()
							}
						}
						if m.Item("复制链接").Chosen() {
							c.WriteClipboard(canonURL)
							a.notifyInfo("已复制列表链接")
						}
					})
					a.XList.HeaderMenuOpen = open
				})
			})

			if desc := strings.TrimSpace(ls.Description); desc != "" {
				ui.Text(c, desc).FontSize(13).LineHeight(LineHeightCompact).TextColor(ColorTextMuted).MaxLines(3)
			}

			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(10).Wrap().ClipX().Children(func() {
				if creatorName != "" {
					btn := ui.ButtonBase(c).Label("打开X列表创建者").Padding(0).Children(func() {
						lbl := creatorName
						if creatorHandle != "" && !strings.EqualFold(creatorName, creatorHandle) {
							lbl = creatorName + " (@" + creatorHandle + ")"
						} else if creatorHandle != "" {
							lbl = "@" + creatorHandle
						}
						ui.Text(c, "创建者 "+lbl).FontSize(12).FontWeight(500).TextColor(ColorTextMuted).SingleLine()
					})
					if btn.Clicked() {
						openCreator()
					}
					ui.Text(c, "·").FontSize(12).TextColor(ColorTextMuted).SingleLine()
				}
				ui.Box(c).Label("X列表成员数").Shrink(0).Children(func() {
					ui.Text(c, fmt.Sprintf("成员 %s", FormatCount(int64(ls.MemberCount)))).
						FontSize(12).
						FontWeight(600).
						TextColor(ColorTextMain).
						SingleLine()
				})
				if ls.SubscriberCount > 0 {
					ui.Text(c, fmt.Sprintf("· 关注 %s", FormatCount(int64(ls.SubscriberCount)))).
						FontSize(12).
						TextColor(ColorTextMuted).
						SingleLine()
				}
			})
		})

		UnderlineTabs(c, activeTab, xListTabs, func(key string) {
			a.SelectXListTab(key)
		})
	})
}

func (a *App) renderXListPostsTab(c *ui.Context, ls *service.XListDTO) {
	if a.XList.PostsListState == nil {
		a.XList.PostsListState = &ui.ListState{}
	}
	posts := a.XList.Posts
	pendingKey := "x-list-tweets-" + ls.ID
	busy := a.isPending(pendingKey)

	_, last := a.XList.PostsListState.Visible()
	if last >= 0 && last >= len(posts)-1 && a.XList.PostsHasMore && a.XList.PostsError == "" && !busy {
		a.LoadMoreXListTweets()
	}

	totalRows := len(posts) + 2
	a.XList.PostsListState.Key = func(i int) any {
		if i == 0 {
			return "x-list-header:" + ls.ID + ":posts"
		}
		if i > 0 && i <= len(posts) {
			return "x-list-post:" + ls.ID + ":" + posts[i-1].ID
		}
		return "x-list-posts-footer:" + ls.ID
	}

	ui.List(c, a.XList.PostsListState, totalRows, func(i int) {
		if i == 0 {
			a.renderXListHeader(c, ls, "posts")
			return
		}
		if i > 0 && i <= len(posts) {
			a.renderPostCard(c, a.xPostToPostCard(posts[i-1], nil))
			return
		}
		ui.Column(c).FillWidth().Padding(14, 16).Gap(10).AlignItems(ui.Center).ClipX().Children(func() {
			if a.XList.PostsError != "" {
				renderErrorBanner(c, "X列表帖子错误", a.XList.PostsError, func() {
					if OutlinePillButton(c, "重试", "重试加载X列表帖子").Clicked() {
						a.LoadMoreXListTweets()
					}
				})
				return
			}
			if busy && len(posts) == 0 {
				ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
					ui.Spinner(c).Size(14, 14)
					ui.Text(c, "正在加载列表帖子…").FontSize(13).TextColor(ColorTextMuted).SingleLine()
				})
				return
			}
			if a.XList.PostsHasMore {
				if busy {
					ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
						ui.Spinner(c).Size(14, 14)
						ui.Text(c, "正在加载更多帖子…").FontSize(13).TextColor(ColorTextMuted).SingleLine()
					})
				} else if OutlinePillButton(c, "加载更多帖子", "加载更多X列表帖子").Clicked() {
					a.LoadMoreXListTweets()
				}
				return
			}
			if len(posts) == 0 {
				ui.Text(c, "该列表暂无帖子").FontSize(13).TextColor(ColorTextMuted).SingleLine()
				return
			}
			ui.Text(c, "已经到底了").FontSize(13).TextColor(ColorTextMuted).SingleLine()
		})
	}).Label("X列表帖子流").Grow(1).FillWidth()
}

func (a *App) renderXListMembersTab(c *ui.Context, ls *service.XListDTO) {
	if a.XList.MembersListState == nil {
		a.XList.MembersListState = &ui.ListState{}
	}
	members := a.XList.Members
	pendingKey := "x-list-members-" + ls.ID
	busy := a.isPending(pendingKey)
	unsubscribed := a.unsubscribedXListMembers()

	// Row 0 = header, Row 1 = batch import bar, rows 2..len(members)+1 = members, last = footer.
	totalRows := len(members) + 3
	a.XList.MembersListState.Key = func(i int) any {
		switch {
		case i == 0:
			return "x-list-header:" + ls.ID + ":members"
		case i == 1:
			return "x-list-members-bar:" + ls.ID
		case i >= 2 && i < len(members)+2:
			return "x-list-member:" + ls.ID + ":" + members[i-2].RestID
		default:
			return "x-list-members-footer:" + ls.ID
		}
	}

	ui.List(c, a.XList.MembersListState, totalRows, func(i int) {
		switch {
		case i == 0:
			a.renderXListHeader(c, ls, "members")
			return
		case i == 1:
			ui.Row(c).
				Label("X列表成员操作栏").
				FillWidth().
				Padding(10, 16).
				BorderWidth(0, 0, 1, 0).
				BorderColor(ColorBorder).
				Background(ColorBgPanel).
				AlignItems(ui.Center).
				Justify(ui.SpaceBetween).
				Gap(8).
				ClipX().
				Children(func() {
					summary := fmt.Sprintf("已加载 %d 位成员", len(members))
					if len(members) > 0 {
						summary = fmt.Sprintf("已加载 %d 位成员 · %d 位未订阅", len(members), len(unsubscribed))
					}
					ui.Text(c, summary).FontSize(12).TextColor(ColorTextMuted).SingleLine().Shrink(1)
					btn := AccentPillButton(c, "批量导入未订阅成员", "批量导入X列表未订阅成员").Disabled(len(unsubscribed) == 0)
					if btn.Clicked() && len(unsubscribed) > 0 {
						a.ImportUnsubscribedXListMembers()
					}
				})
			return
		case i >= 2 && i < len(members)+2:
			a.renderXListMemberRow(c, members[i-2])
			return
		default:
			ui.Column(c).FillWidth().Padding(14, 16).Gap(10).AlignItems(ui.Center).ClipX().Children(func() {
				if a.XList.MembersError != "" {
					renderErrorBanner(c, "X列表成员错误", a.XList.MembersError, func() {
						if OutlinePillButton(c, "重试", "重试加载X列表成员").Clicked() {
							a.LoadMoreXListMembers()
						}
					})
					return
				}
				if busy && len(members) == 0 {
					ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
						ui.Spinner(c).Size(14, 14)
						ui.Text(c, "正在加载成员…").FontSize(13).TextColor(ColorTextMuted).SingleLine()
					})
					return
				}
				if a.XList.MembersHasMore {
					if busy {
						ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
							ui.Spinner(c).Size(14, 14)
							ui.Text(c, "正在加载更多成员…").FontSize(13).TextColor(ColorTextMuted).SingleLine()
						})
					} else if OutlinePillButton(c, "加载更多成员", "加载更多X列表成员").Clicked() {
						a.LoadMoreXListMembers()
					}
					return
				}
				if len(members) == 0 {
					ui.Text(c, "该列表暂无成员").FontSize(13).TextColor(ColorTextMuted).SingleLine()
					return
				}
				ui.Text(c, "已加载全部成员").FontSize(13).TextColor(ColorTextMuted).SingleLine()
			})
		}
	}).Label("X列表成员列表").Grow(1).FillWidth()
}

func (a *App) renderXListMemberRow(c *ui.Context, m service.XListMemberDTO) {
	handle := strings.TrimPrefix(strings.TrimSpace(m.ScreenName), "@")
	displayName := firstNonEmptyStrUI(strings.TrimSpace(m.Name), handle, m.RestID)
	openUser := func() {
		if target := firstNonEmptyStrUI(strings.TrimSpace(m.RestID), handle); target != "" {
			a.OpenXUser(target, displayName)
		}
	}
	ownerSub, owned := a.ownerOfAccountInMemory("x", m.RestID, 0)
	if !owned && m.Subscribed {
		owned = true
		ownerSub = subscription.Subscription{ID: m.OwnerSubID, Name: m.OwnerSubName}
	}

	ui.Row(c).
		Label("X列表成员："+m.RestID).
		FillWidth().
		Padding(12, 16).
		Gap(12).
		AlignItems(ui.Center).
		BorderWidth(0, 0, 1, 0).
		BorderColor(ColorBorder).
		Background(ColorBgMain).
		ClipX().
		Children(func() {
			a.renderAccountAvatar(c, "x", displayName, m.RestID, 0, 40, false, "打开X用户："+m.RestID, openUser, m.AvatarURL)
			ui.Column(c).Grow(1).Basis(0).Gap(3).ClipX().Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
					nameBtn := ui.ButtonBase(c).Padding(0).Children(func() {
						ui.Text(c, displayName).FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					})
					if nameBtn.Clicked() {
						openUser()
					}
					if handle != "" {
						ui.Text(c, "@"+handle).FontSize(12).TextColor(ColorTextMuted).SingleLine().Shrink(1)
					}
				})
				if desc := strings.TrimSpace(m.Description); desc != "" {
					ui.Text(c, desc).FontSize(12).LineHeight(LineHeightCompact).TextColor(ColorTextMuted).MaxLines(2)
				}
			})
			ui.Row(c).Shrink(0).AlignItems(ui.Center).Children(func() {
				if owned {
					if OutlinePillButton(c, "已订阅", "X列表成员已订阅："+m.RestID).Clicked() && ownerSub.ID != "" {
						a.OpenSubscriptionDetail(ownerSub)
					}
				} else if OutlinePillButton(c, "订阅", "订阅X列表成员："+m.RestID).Clicked() {
					a.OpenSubscribePicker(subscription.AccountRef{
						Site:     string(site.X),
						UID:      m.RestID,
						UniqueID: handle,
						Name:     displayName,
						Face:     m.AvatarURL,
					})
				}
			})
		})
}

func (a *App) renderXUserListsOverlay(c *ui.Context, w, h float32) {
	if !a.XUserLists.Open {
		return
	}
	cardW := min(float32(500), max(float32(320), w-40))
	cardH := min(float32(520), max(float32(300), h-100))
	busy := a.isPending("x-user-lists-" + a.XUserLists.UserRestID)

	renderDialog(c, &a.XUserLists.Open,
		"关闭X用户公开列表对话框（点击空白处）", "X用户公开列表对话框",
		cardW, cardH, func() { a.XUserLists.Open = false },
		func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
				title := "公开列表"
				if a.XUserLists.UserName != "" {
					title = a.XUserLists.UserName + " 的公开列表"
				}
				ui.Text(c, title).FontSize(17).FontWeight(600).TextColor(ColorTextMain).SingleLine().Shrink(1)
				if OutlinePillButton(c, "关闭", "关闭X用户公开列表").Clicked() {
					a.XUserLists.Open = false
				}
			})

			if a.XUserLists.Error != "" {
				renderErrorBanner(c, "X用户公开列表错误", a.XUserLists.Error, func() {
					if OutlinePillButton(c, "重试", "重试加载X用户公开列表").Clicked() {
						a.OpenXUserListsModal(a.XUserLists.UserRestID, a.XUserLists.UserName)
					}
				})
				return
			}

			if busy && len(a.XUserLists.Lists) == 0 {
				ui.Row(c).Grow(1).FillWidth().Justify(ui.Center).AlignItems(ui.Center).Gap(8).Children(func() {
					ui.Spinner(c).Size(16, 16)
					ui.Text(c, "正在加载公开列表…").FontSize(13).TextColor(ColorTextMuted).SingleLine()
				})
				return
			}

			if len(a.XUserLists.Lists) == 0 {
				ui.Column(c).Grow(1).FillWidth().Justify(ui.Center).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "该用户暂无公开列表").FontSize(13).TextColor(ColorTextMuted).SingleLine()
				})
				return
			}

			ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
				ui.Column(c).FillWidth().Gap(8).ClipX().Children(func() {
					for _, item := range a.XUserLists.Lists {
						ls := item
						rowBtn := ui.ButtonBase(c).
							Label("打开X公开列表："+ls.ID).
							FillWidth().
							Padding(10, 12).
							Radius(10).
							Background(ColorBgSecondary).
							Border(1, ColorBorder).
							ClipX()
						if rowBtn.Hovered() {
							rowBtn.Background(ColorHover)
						}
						rowBtn.Children(func() {
							ui.Column(c).FillWidth().Gap(3).ClipX().Children(func() {
								ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
									ui.Text(c, firstNonEmptyStrUI(ls.Name, "X 列表 "+ls.ID)).FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine().Shrink(1)
									ui.Text(c, fmt.Sprintf("%d 位成员", ls.MemberCount)).FontSize(12).TextColor(ColorTextMuted).SingleLine().Shrink(0)
								})
								if strings.TrimSpace(ls.Description) != "" {
									ui.Text(c, ls.Description).FontSize(12).LineHeight(LineHeightCompact).TextColor(ColorTextMuted).MaxLines(2)
								}
							})
						})
						if rowBtn.Clicked() {
							a.OpenXList(ls.ID, ls.Name)
						}
					}
				})
			})
		})
}
