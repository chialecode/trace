package ui

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/chialecode/trace/internal/uistate"
	"github.com/egoist/mygo/ui"
)

func normalizeSubDetailTab(tab string) string {
	return uistate.NormalizeSubDetailTab(tab)
}

func (a *App) setSubDetailTab(tab string) {
	norm := normalizeSubDetailTab(tab)
	a.SubDetailTab = norm
	if a.backend != nil {
		if a.backend.GetUIState().SubDetailTab != norm {
			if _, err := a.backend.MutateUIState(func(st *uistate.State) { st.SubDetailTab = norm }); err != nil {
				a.showError(err)
			}
		}
	}
}

func parseSubscriptionRouteParams(rawRoute string) (subID string, tab string, hasTab bool, accountKey string) {
	r := strings.TrimSpace(rawRoute)
	if !strings.HasPrefix(r, RouteSubscriptions) {
		return "", "", false, ""
	}
	if qIdx := strings.IndexByte(r, '?'); qIdx >= 0 {
		query := r[qIdx+1:]
		for _, part := range strings.Split(query, "&") {
			if k, v, ok := strings.Cut(part, "="); ok {
				switch strings.ToLower(strings.TrimSpace(k)) {
				case "tab":
					tab = strings.TrimSpace(v)
					hasTab = true
				case "account":
					rawVal := strings.TrimSpace(v)
					if unesc, err := url.QueryUnescape(rawVal); err == nil {
						accountKey = strings.TrimSpace(unesc)
					} else {
						accountKey = rawVal
					}
				}
			}
		}
		r = r[:qIdx]
	}
	rest := strings.Trim(strings.TrimPrefix(r, RouteSubscriptions), "/")
	if rest != "" {
		parts := strings.Split(rest, "/")
		if !strings.EqualFold(parts[0], "import") {
			subID = parts[0]
			if len(parts) >= 2 && !hasTab {
				switch strings.ToLower(strings.TrimSpace(parts[len(parts)-1])) {
				case "content", "accounts", "live":
					tab = parts[len(parts)-1]
					hasTab = true
				}
			}
		}
	}
	return subID, tab, hasTab, accountKey
}

func (a *App) applySubscriptionRouteNavigate(rawRoute string) {
	subID, tab, hasTab, accountKey := parseSubscriptionRouteParams(rawRoute)
	if hasTab {
		a.setSubDetailTab(tab)
	}
	if subID == "" {
		return
	}
	if strings.EqualFold(subID, "accounts") {
		a.SubDetailOpen = false
		a.SubAccountsOpen = true
		a.SubChecksOpen = false
		a.refreshAccountWorks()
		return
	}
	if strings.EqualFold(subID, "checks") {
		a.openSubscriptionChecks()
		return
	}
	for i := range a.Subscriptions {
		if a.Subscriptions[i].ID == subID {
			sub := a.Subscriptions[i]
			a.SelectedSubID = sub.ID
			a.SelectedSubMid = sub.Mid
			a.SubDetailOpen = true
			a.SubAccountsOpen = false
			a.SubChecksOpen = false
			a.SubDetailTab = normalizeSubDetailTab(a.SubDetailTab)
			a.SubFeedAccountKey = ""
			if accountKey != "" {
				if acc, ok := resolveSubFeedSelectedAccount(sub, accountKey); ok {
					a.SubFeedAccountKey = subscriptionAccountKey(acc)
				}
			}
			a.refreshAccountWorks()
			a.syncSelectedSubRules(sub)
			a.ensureSubscriptionFeedLoaded(&sub)
			return
		}
	}
}

func (a *App) applyInitialSubscriptionRoute(rawRoute string) {
	if strings.HasPrefix(strings.TrimSpace(rawRoute), RouteSubscriptions) {
		a.applySubscriptionRouteNavigate(rawRoute)
	}
}

func (a *App) resetSubFeedListState() {
	a.SubFeedListState = &ui.ListState{
		Key: func(i int) any {
			if i >= 0 && i < len(a.SubFeedItems) {
				return a.SubFeedItems[i].FeedKey
			}
			return i
		},
	}
}

func subscriptionAuthorRefsKey(refs []library.AuthorRef) string {
	if len(refs) == 0 {
		return ""
	}
	parts := make([]string, len(refs))
	for i, r := range refs {
		parts[i] = r.Site + ":" + r.UID
	}
	return strings.Join(parts, "|")
}

func subscriptionAccountKey(acc subscription.AccountRef) string {
	accSite := normalizeSiteIDUI(acc.Site)
	uid := strings.TrimSpace(acc.UID)
	if uid == "" && accSite == "x" {
		uid = strings.TrimSpace(acc.UniqueID)
	}
	if uid == "" {
		return ""
	}
	return accSite + ":" + uid
}

func resolveSubFeedSelectedAccount(sub subscription.Subscription, key string) (subscription.AccountRef, bool) {
	want := strings.TrimSpace(key)
	if want == "" {
		return subscription.AccountRef{}, false
	}
	for _, acc := range subscriptionEffectiveAccounts(sub) {
		if strings.EqualFold(subscriptionAccountKey(acc), want) {
			return acc, true
		}
	}
	return subscription.AccountRef{}, false
}

func subscriptionActiveFeedRefs(sub subscription.Subscription, accountKey string) []library.AuthorRef {
	if acc, ok := resolveSubFeedSelectedAccount(sub, accountKey); ok {
		accSite := normalizeSiteIDUI(acc.Site)
		uid := strings.TrimSpace(acc.UID)
		if uid == "" && accSite == "x" {
			uid = strings.TrimSpace(acc.UniqueID)
		}
		normSite, normUID := library.NormalizeAuthorUID(accSite, uid, parseBilibiliMidUI(accSite, uid))
		if normUID != "" {
			return []library.AuthorRef{{Site: normSite, UID: normUID}}
		}
	}
	return subscriptionAuthorRefs(sub)
}

// SelectSubFeedAccount switches the active single-account filter on the current
// subscription detail's "内容" tab (empty string selects "全部") (DEC-095).
func (a *App) SelectSubFeedAccount(accountKey string) {
	if a.SelectedSubID == "" {
		a.SubFeedAccountKey = ""
		return
	}
	for i := range a.Subscriptions {
		if a.Subscriptions[i].ID == a.SelectedSubID {
			sub := a.Subscriptions[i]
			a.selectSubFeedAccount(&sub, accountKey)
			return
		}
	}
	a.SubFeedAccountKey = ""
}

func (a *App) selectSubFeedAccount(sub *subscription.Subscription, accountKey string) {
	if sub == nil {
		return
	}
	next := ""
	if strings.TrimSpace(accountKey) != "" {
		if acc, ok := resolveSubFeedSelectedAccount(*sub, accountKey); ok {
			next = subscriptionAccountKey(acc)
		}
	}
	if a.SubFeedAccountKey == next && a.SubFeedSubID == sub.ID && !a.subFeedDirty {
		return
	}
	a.SubFeedAccountKey = next
	a.ensureSubscriptionFeedLoaded(sub)
}

func (a *App) ensureSubscriptionFeedLoaded(sub *subscription.Subscription) {
	if sub == nil || a.backend == nil {
		return
	}
	if a.SubFeedAccountKey != "" {
		if acc, ok := resolveSubFeedSelectedAccount(*sub, a.SubFeedAccountKey); ok {
			a.SubFeedAccountKey = subscriptionAccountKey(acc)
		} else {
			a.SubFeedAccountKey = ""
		}
	}
	refs := subscriptionActiveFeedRefs(*sub, a.SubFeedAccountKey)
	refsKey := subscriptionAuthorRefsKey(refs)
	if !a.subFeedDirty && a.SubFeedSubID == sub.ID && a.SubFeedRefsKey == refsKey {
		return
	}
	subChanged := a.SubFeedSubID != sub.ID || a.SubFeedRefsKey != refsKey
	a.loadSubscriptionFeed(sub, refs, refsKey, subChanged)
}

func (a *App) loadSubscriptionFeed(sub *subscription.Subscription, refs []library.AuthorRef, refsKey string, resetScroll bool) {
	a.subFeedDirty = false
	a.SubFeedSubID = sub.ID
	a.SubFeedRefsKey = refsKey
	// Guard: a subscription with no linked accounts MUST return an empty feed
	// immediately and never query FeedPage without author clauses.
	if len(refs) == 0 {
		a.SubFeedItems = nil
		a.SubFeedMoreAfter = false
		a.SubFeedError = ""
		a.resetSubFeedListState()
		return
	}
	page, err := a.backend.FeedPage(library.FeedPageRequest{
		Order:      "newest",
		CachedOnly: true,
		AuthorRefs: refs,
		Limit:      60,
	})
	if err != nil {
		a.SubFeedError = userErrorText(err)
		if resetScroll {
			a.SubFeedItems = nil
			a.SubFeedMoreAfter = false
			a.resetSubFeedListState()
		}
		a.showError(err)
		return
	}
	a.SubFeedItems = page.Items
	a.SubFeedMoreAfter = page.HasMore
	a.SubFeedError = ""
	if resetScroll {
		a.resetSubFeedListState()
		a.SubFeedListState.ScrollTo(0, ui.Start)
	}
}

func (a *App) loadMoreSubscriptionFeed(sub *subscription.Subscription) {
	if sub == nil || a.backend == nil || len(a.SubFeedItems) == 0 {
		return
	}
	refs := subscriptionActiveFeedRefs(*sub, a.SubFeedAccountKey)
	if len(refs) == 0 {
		return
	}
	edge := a.SubFeedItems[len(a.SubFeedItems)-1]
	page, err := a.backend.FeedPage(library.FeedPageRequest{
		Cursor:     &library.FeedCursor{Key: edge.FeedKey, PubTs: edge.PubTs},
		Order:      "newest",
		Direction:  "older",
		CachedOnly: true,
		AuthorRefs: refs,
		Limit:      30,
	})
	if err != nil {
		a.SubFeedError = userErrorText(err)
		a.showError(err)
		return
	}
	a.SubFeedError = ""
	a.SubFeedMoreAfter = page.HasMore
	if len(page.Items) == 0 {
		return
	}
	seen := make(map[string]bool, len(a.SubFeedItems))
	for _, e := range a.SubFeedItems {
		seen[e.FeedKey] = true
	}
	for _, e := range page.Items {
		if !seen[e.FeedKey] {
			a.SubFeedItems = append(a.SubFeedItems, e)
			seen[e.FeedKey] = true
		}
	}
}

func subscriptionEffectiveAccounts(sub subscription.Subscription) []subscription.AccountRef {
	if len(sub.Accounts) > 0 {
		out := append([]subscription.AccountRef(nil), sub.Accounts...)
		subscription.SortAccountsBySiteOrder(out)
		return out
	}
	pSite, pUID := subscriptionPrimarySiteAndUID(sub)
	if pUID != "" {
		return []subscription.AccountRef{{
			Site: pSite,
			UID:  pUID,
			Name: sub.Name,
			Face: sub.Face,
		}}
	}
	return nil
}

func (a *App) renderSubscriptionDetailView(c *ui.Context, sub *subscription.Subscription) {
	a.SubDetailTab = normalizeSubDetailTab(a.SubDetailTab)
	if a.SubDetailTab == "accounts" {
		a.ensureAccountWorksFresh()
		a.refreshLiveAccounts(c, subscriptionAuthors(*sub))
		ui.Column(c).Grow(1).FillWidth().ClipX().Children(func() {
			ui.Column(c).FillWidth().Padding(16, 16, 0, 16).ClipX().Children(func() {
				a.renderSubscriptionDetailHeader(c, sub)
			})
			ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
				ui.Column(c).FillWidth().Padding(16).Gap(14).ClipX().Children(func() {
					a.renderSubscriptionAccountsTab(c, sub)
				})
			})
		})
		return
	}

	// Default "content" tab: strictly local index & manifest reads, zero site HTTP requests (DEC-051 ⑦).
	a.ensureSubscriptionFeedLoaded(sub)
	ui.Column(c).Grow(1).FillWidth().ClipX().Children(func() {
		ui.Column(c).FillWidth().Padding(10, 16, 0, 16).ClipX().Children(func() {
			a.renderSubscriptionDetailHeader(c, sub)
		})
		a.renderSubscriptionContentTab(c, sub)
	})
}

// renderSubscriptionDetailHeader draws the subscription card, its manage
// forms and the tabs.
func (a *App) renderSubscriptionDetailHeader(c *ui.Context, sub *subscription.Subscription) {
	// Header: avatar, name (+ note), capability tags, custom links and the "⋯" menu.
	ui.Column(c).
		FillWidth().
		Padding(2, 0, 8, 0).
		Gap(8).
		ClipX().
		Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(12).ClipX().Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(12).Grow(1).ClipX().Children(func() {
					pSite, pUID := subscriptionPrimarySiteAndUID(*sub)
					a.renderAccountAvatar(c, pSite, sub.Name, pUID, sub.Mid, 64, a.isSubscriptionLive(*sub), "", nil, sub.Face)
					ui.Column(c).Grow(1).Gap(4).ClipX().Children(func() {
						ui.Text(c, sub.Name).FontSize(18).FontWeight(600).TextColor(ColorTextMain).SingleLine()
						if strings.TrimSpace(sub.Note) != "" {
							ui.Text(c, sub.Note).FontSize(13).LineHeight(LineHeightCompact).TextColor(ColorTextMuted).MaxLines(2)
						} else {
							ui.Text(c, fmt.Sprintf("关联 %d 个账号", len(subscriptionEffectiveAccounts(*sub)))).FontSize(12).TextColor(ColorTextMuted).SingleLine()
						}
					})
				})

				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					if a.isPending("check-updates") {
						if OutlinePillButton(c, "取消检查", "取消检查").Clicked() {
							a.backend.CancelSubscriptionCheck()
						}
					}
					open := a.SubManageMenuOpen
					MoreMenuButton(c, "订阅管理菜单", 32, &open, func(m *ui.Menu) {
						if m.Item("检查更新").Chosen() {
							a.checkSelectedSubscription()
						}
						if m.Item("改名").Chosen() {
							a.SubName = sub.Name
							a.SubManageModalMode = "rename"
						}
						if m.Item("备注").Chosen() {
							a.SubNote = sub.Note
							a.SubManageModalMode = "note"
						}
						if m.Item("自定义链接").Chosen() {
							a.openSubLinksModal(*sub)
						}
						if m.Item("更换头像").Chosen() {
							a.SubManageModalMode = "avatar"
						}
						m.Separator()
						if m.Item("删除订阅").Danger().Chosen() {
							a.SubDeleteID = sub.ID
							a.SubManageModalMode = "delete"
						}
					})
					a.SubManageMenuOpen = open
				})
			})

			if len(sub.Links) > 0 {
				a.renderSubscriptionCustomLinksRow(c, sub)
			}
			a.renderSubscriptionTagChipsBar(c, sub)

			switch a.SubManageModalMode {
			case "rename":
				ui.Row(c).FillWidth().Padding(10, 12).Radius(12).Background(ColorBgMain).Border(1, ColorBorder).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.TextInput(c, &a.SubName).Label("订阅名称").Placeholder("订阅名称（必填，≤64字）").Grow(1)
					if OutlinePillButton(c, "取消", "取消改名").Clicked() {
						a.SubManageModalMode = ""
					}
					if OutlinePillButton(c, "保存名称", "保存名称和备注").Clicked() {
						sub.Name = a.SubName
						_, err := a.backend.UpsertSubscription(*sub)
						a.showError(err)
						if err == nil {
							a.notifyInfo("已更新订阅名称")
							a.SubManageModalMode = ""
							a.RefreshData()
						}
					}
				})
			case "note":
				ui.Row(c).FillWidth().Padding(10, 12).Radius(12).Background(ColorBgMain).Border(1, ColorBorder).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.TextInput(c, &a.SubNote).Label("订阅备注").Placeholder("备注说明（可选）").Grow(1)
					if OutlinePillButton(c, "取消", "取消修改备注").Clicked() {
						a.SubManageModalMode = ""
					}
					if OutlinePillButton(c, "保存备注", "保存名称和备注").Clicked() {
						sub.Note = a.SubNote
						_, err := a.backend.UpsertSubscription(*sub)
						a.showError(err)
						if err == nil {
							a.notifyInfo("已更新订阅备注")
							a.SubManageModalMode = ""
							a.RefreshData()
						}
					}
				})
			case "avatar":
				ui.Column(c).FillWidth().Padding(10, 12).Radius(12).Background(ColorBgMain).Border(1, ColorBorder).Gap(8).Children(func() {
					ui.Text(c, "选择关联账号作为订阅头像").FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					ui.Row(c).Gap(8).Wrap().Children(func() {
						for _, accItem := range subscriptionEffectiveAccounts(*sub) {
							acc := accItem
							accSite := normalizeSiteIDUI(acc.Site)
							choiceText := "使用 " + firstNonEmptyStrUI(acc.Name, "UID:"+acc.UID) + " · " + siteDisplayName(accSite)
							if OutlinePillButton(c, choiceText, "订阅头像："+accSite+":"+acc.UID).Clicked() {
								sub.Avatar = subscription.AvatarRef{Site: acc.Site, UID: acc.UID}
								_, err := a.backend.UpsertSubscription(*sub)
								a.showError(err)
								if err == nil {
									a.SubManageModalMode = ""
									a.RefreshData()
								}
							}
						}
						if OutlinePillButton(c, "关闭", "关闭更换头像").Clicked() {
							a.SubManageModalMode = ""
						}
					})
				})
			case "delete":
				if a.SubDeleteID == sub.ID {
					ui.Column(c).FillWidth().Padding(12).Radius(12).Background(ColorErrorBg).Border(1, ui.RGBA(180, 35, 24, 0.25)).Gap(8).Children(func() {
						ui.Text(c, fmt.Sprintf("删除订阅「%s」？只删除这个订阅和它的账号关联，不删除已缓存的内容，也不会在哔哩哔哩或抖音上取消关注。", sub.Name)).
							FontSize(13).TextColor(ColorError).MaxLines(2)
						ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
							if OutlinePillButton(c, "取消", "取消删除订阅").Clicked() {
								a.SubDeleteID = ""
								a.SubManageModalMode = ""
							}
							if DangerOutlinePillButton(c, "确认删除订阅", "确认删除订阅").Clicked() {
								err := a.backend.DeleteSubscription(sub.ID)
								a.showError(err)
								if err == nil {
									a.SubDeleteID = ""
									a.SubManageModalMode = ""
									a.SubDetailOpen = false
									a.SelectedSubID = ""
									a.SelectedSubMid = 0
									a.RefreshData()
								}
							}
						})
					})
				}
			}
		})

	UnderlineTabs(c, a.SubDetailTab, []TabOption{
		{Key: "content", Text: "内容", AccessLabel: "订阅标签：内容"},
		{Key: "accounts", Text: "账号", AccessLabel: "订阅标签：账号"},
	}, func(key string) { a.setSubDetailTab(key) })
}

func (a *App) openSubscriptionCustomLink(rawURL string) {
	if a.backend == nil {
		return
	}
	if err := a.backend.OpenExternalLink(rawURL); err != nil {
		a.showError(err)
	}
}

func (a *App) renderSubscriptionCustomLinksRow(c *ui.Context, sub *subscription.Subscription) {
	seenLabels := make(map[string]int, len(sub.Links))
	ui.Row(c).
		Label("订阅自定义链接栏").
		FillWidth().
		Gap(6).
		Wrap().
		AlignItems(ui.Center).
		ClipX().
		Children(func() {
			for _, item := range sub.Links {
				lnk := item
				display := lnk.DisplayLabel()
				if display == "" {
					display = lnk.URL
				}
				seenLabels[display]++
				chipLabel := "打开订阅链接：" + display
				if seenLabels[display] > 1 {
					chipLabel = fmt.Sprintf("%s #%d", chipLabel, seenLabels[display])
				}
				chip := ui.ButtonBase(c).
					Label(chipLabel).
					Tooltip(lnk.URL).
					Padding(4, 10).
					Radius(999).
					Background(ColorBgMain).
					Border(1, ColorBorder).
					Gap(5).
					AlignItems(ui.Center).
					MaxWidth(260).
					ClipX()
				if chip.Hovered() {
					chip.Background(ColorAccentBg).BorderColor(ColorAccent)
				}
				chip.Children(func() {
					DrawIcon(c, "external-link", 12, ColorAccent, 1.75)
					ui.Text(c, display).FontSize(12).FontWeight(500).TextColor(ColorAccent).SingleLine().Shrink(1)
				})
				if chip.Clicked() {
					a.openSubscriptionCustomLink(lnk.URL)
				}
			}

			manageBtn := ui.ButtonBase(c).
				Label("管理订阅自定义链接").
				Tooltip("添加或编辑自定义链接").
				Padding(4, 10).
				Radius(999).
				Background(ColorBgMain).
				Border(1, ColorBorder).
				AlignItems(ui.Center)
			if manageBtn.Hovered() {
				manageBtn.Background(ColorHover)
			}
			manageBtn.Children(func() {
				ui.Text(c, "管理链接").FontSize(12).TextColor(ColorTextMuted).SingleLine()
			})
			if manageBtn.Clicked() {
				a.openSubLinksModal(*sub)
			}
		})
}

// renderAccountAvatarCircle draws a non-interactive circular avatar box so it
// can sit inside a clickable row or filter capsule without nesting buttons.
// When allowRemote is false, it strictly uses local avatar bitmaps or colored
// initials with zero remote requests.
func (a *App) renderAccountAvatarCircle(c *ui.Context, siteID, name, uid string, mid int64, size float32, liveRing bool, allowRemote bool, faces ...string) {
	if size <= 0 {
		size = 24
	}
	normSite := normalizeSiteIDUI(siteID)
	seed := mid
	if seed <= 0 {
		seed = accountSeedHash(normSite, firstNonEmptyStrUI(uid, name))
	}
	lookupUID := strings.TrimSpace(uid)
	if lookupUID == "" && normSite == "bilibili" && mid > 0 {
		lookupUID = strconv.FormatInt(mid, 10)
	}
	bmp := a.lookupAccountAvatarBitmap(normSite, lookupUID)
	skipRemote := a.pendingLocalAvatar
	a.pendingLocalAvatar = false
	if bmp == nil && allowRemote && !skipRemote {
		face := firstImageURL(faces)
		if face == "" {
			if normSite == "bilibili" && mid > 0 {
				face = a.faceURL(mid)
			} else {
				face = a.faceURLForAccount(normSite, uid)
			}
		}
		bmp = a.lookupRemoteBitmap(face)
	}

	bg := AvatarColor(name, seed)
	glyph := FirstGlyph(name)
	fontSz := max(10, size*0.44)
	outerSize := size
	if liveRing {
		outerSize = size + 6
	}
	box := ui.Box(c).
		Size(outerSize, outerSize).
		Radius(outerSize / 2).
		Clip().
		Shrink(0).
		Justify(ui.Center).
		AlignItems(ui.Center)
	box.Draw(func(p *ui.Painter, r ui.Rect) {
		if liveRing {
			p.Stroke(r, ColorLive, r.W/2, 2.0)
			inner := ui.Rect{X: r.X + 3, Y: r.Y + 3, W: r.W - 6, H: r.H - 6}
			p.Fill(inner, bg, inner.W/2)
		} else {
			p.Fill(r, bg, r.W/2)
		}
	})
	box.Children(func() {
		if bmp != nil {
			ui.Image(c, bmp).Size(size, size).Fit(ui.Cover).Radius(size / 2)
		} else {
			ui.Text(c, glyph).FontSize(fontSz).FontWeight(600).TextColor(ColorWhite).SingleLine()
		}
	})
}

func accountOpenTarget(acc subscription.AccountRef) (string, string, string) {
	accSite := normalizeSiteIDUI(acc.Site)
	targetID := strings.TrimSpace(acc.UID)
	if accSite == "x" {
		targetID = firstNonEmptyStrUI(acc.UID, acc.UniqueID)
	}
	return accSite, targetID, firstNonEmptyStrUI(acc.Name, acc.UniqueID, acc.UID)
}

func (a *App) renderOpenAccountHomeButton(c *ui.Context, acc subscription.AccountRef, text, label string) {
	accSite, targetID, displayName := accountOpenTarget(acc)
	btn := ui.ButtonBase(c).
		Label(label).
		Tooltip("联网打开该账号的站点主页").
		Padding(3, 10).
		Radius(999).
		Background(ColorBgMain).
		Border(1, ColorAccent).
		Gap(4).
		AlignItems(ui.Center).
		Shrink(0)
	if btn.Hovered() {
		btn.Background(ColorAccentBg)
	}
	btn.Children(func() {
		DrawIcon(c, "external-link", 12, ColorAccent, 1.75)
		ui.Text(c, text).FontSize(12).FontWeight(600).TextColor(ColorAccent).SingleLine()
	})
	if btn.Clicked() {
		a.openAccountUserPage(accSite, targetID, displayName)
	}
}

// renderSubscriptionContentAccountBar draws the compact single-account filter
// and direct online profile entry at the top of Subscription Detail › 内容 (DEC-095).
func (a *App) renderSubscriptionContentAccountBar(c *ui.Context, sub *subscription.Subscription) {
	accounts := subscriptionEffectiveAccounts(*sub)
	if len(accounts) == 0 {
		return
	}

	if len(accounts) == 1 {
		acc := accounts[0]
		accSite := normalizeSiteIDUI(acc.Site)
		accKey := subscriptionAccountKey(acc)
		displayName := firstNonEmptyStrUI(acc.Name, acc.UniqueID, acc.UID)
		siteShort := firstNonEmptyStrUI(site.DisplayName(accSite), "B 站")

		ui.Row(c).
			Label("订阅内容账号切换栏").
			FillWidth().
			Padding(6, 16).
			BorderWidth(0, 0, 1, 0).
			BorderColor(ColorBorder).
			Background(ColorBgMain).
			AlignItems(ui.Center).
			Justify(ui.SpaceBetween).
			Gap(8).
			Wrap().
			Shrink(0).
			ClipX().
			Children(func() {
				ui.Row(c).
					Label("订阅内容单账号：" + accKey).
					AlignItems(ui.Center).
					Gap(6).
					Shrink(1).
					ClipX().
					Children(func() {
						a.renderAccountAvatarCircle(c, accSite, displayName, acc.UID, parseBilibiliMidUI(accSite, acc.UID), 20, false, false)
						ui.Text(c, displayName).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine().Shrink(1)
						ui.Text(c, "· "+siteShort).FontSize(12).TextColor(ColorTextMuted).SingleLine().Shrink(0)
					})
				a.renderOpenAccountHomeButton(c, acc, "打开账号主页", "打开账号主页")
			})
		return
	}

	selectedAcc, hasSelected := resolveSubFeedSelectedAccount(*sub, a.SubFeedAccountKey)
	ui.Row(c).
		Label("订阅内容账号切换栏").
		FillWidth().
		Padding(6, 16).
		BorderWidth(0, 0, 1, 0).
		BorderColor(ColorBorder).
		Background(ColorBgMain).
		AlignItems(ui.Center).
		Gap(5).
		Wrap().
		Shrink(0).
		ClipX().
		Children(func() {
			allActive := !hasSelected
			allBtn := ui.ButtonBase(c).
				Label("订阅内容账号筛选：全部").
				Tooltip("查看该订阅全部关联账号的已缓存内容").
				Padding(3, 10).
				Radius(999).
				AlignItems(ui.Center).
				Shrink(0)
			if allActive {
				allBtn.Background(ColorAccentBg).Border(1, ColorAccent)
			} else {
				allBtn.Background(ColorBgMain).Border(1, ColorBorderStrong)
				if allBtn.Hovered() {
					allBtn.Background(ColorHover)
				}
			}
			allBtn.Children(func() {
				col := ColorTextMain
				weight := 500
				if allActive {
					col = ColorAccent
					weight = 600
				}
				ui.Text(c, "全部").FontSize(12).FontWeight(weight).TextColor(col).SingleLine()
			})
			if allBtn.Clicked() && !allActive {
				a.selectSubFeedAccount(sub, "")
			}

			for _, accItem := range accounts {
				acc := accItem
				accKey := subscriptionAccountKey(acc)
				accSite := normalizeSiteIDUI(acc.Site)
				displayName := firstNonEmptyStrUI(acc.Name, acc.UniqueID, acc.UID)
				siteShort := firstNonEmptyStrUI(site.DisplayName(accSite), "B 站")
				active := hasSelected && strings.EqualFold(subscriptionAccountKey(selectedAcc), accKey)

				chip := ui.ButtonBase(c).
					Label("订阅内容账号筛选："+accKey).
					Tooltip(fmt.Sprintf("只看 %s（%s）的已缓存内容", displayName, siteShort)).
					Padding(3, 8, 3, 4).
					Radius(999).
					Gap(4).
					AlignItems(ui.Center).
					MaxWidth(210).
					Shrink(0).
					ClipX()
				if active {
					chip.Background(ColorAccentBg).Border(1, ColorAccent)
				} else {
					chip.Background(ColorBgMain).Border(1, ColorBorderStrong)
					if chip.Hovered() {
						chip.Background(ColorHover)
					}
				}
				chip.Children(func() {
					a.renderAccountAvatarCircle(c, accSite, displayName, acc.UID, parseBilibiliMidUI(accSite, acc.UID), 18, false, false)
					nameCol := ColorTextMain
					siteCol := ColorTextMuted
					weight := 500
					if active {
						nameCol = ColorAccent
						siteCol = ColorAccent
						weight = 600
					}
					ui.Text(c, displayName).FontSize(12).FontWeight(weight).TextColor(nameCol).SingleLine().Shrink(1)
					ui.Text(c, "· "+siteShort).FontSize(11).TextColor(siteCol).SingleLine().Shrink(0)
				})
				if chip.Clicked() && !active {
					a.selectSubFeedAccount(sub, accKey)
				}
			}

			if hasSelected {
				a.renderOpenAccountHomeButton(c, selectedAcc, "打开账号主页", "打开账号主页")
			}
		})
}

func (a *App) renderSubscriptionContentTab(c *ui.Context, sub *subscription.Subscription) {
	a.renderSubscriptionContentAccountBar(c, sub)

	if a.SubFeedError != "" {
		ui.Box(c).FillWidth().Padding(8, 16).Children(func() {
			renderErrorBanner(c, "", a.SubFeedError, func() {
				if OutlinePillButton(c, "重试", "重试订阅内容分页").Clicked() {
					if len(a.SubFeedItems) == 0 {
						a.subFeedDirty = true
						a.ensureSubscriptionFeedLoaded(sub)
					} else {
						a.loadMoreSubscriptionFeed(sub)
					}
				}
			})
		})
	}

	_, last := a.SubFeedListState.Visible()
	if len(a.SubFeedItems) > 0 && last >= 0 && last >= len(a.SubFeedItems)-3 && a.SubFeedMoreAfter && a.SubFeedError == "" {
		a.loadMoreSubscriptionFeed(sub)
	}

	if len(a.SubFeedItems) == 0 {
		accounts := subscriptionEffectiveAccounts(*sub)
		selectedAcc, hasSelected := resolveSubFeedSelectedAccount(*sub, a.SubFeedAccountKey)
		emptyTitle := "还没有缓存这个订阅的内容"
		emptyHint := subscriptionEmptyStateHint(*sub)
		if hasSelected && len(accounts) >= 2 {
			accName := firstNonEmptyStrUI(selectedAcc.Name, selectedAcc.UniqueID, selectedAcc.UID)
			emptyTitle = fmt.Sprintf("还没有缓存「%s」的内容", accName)
			emptyHint = "可以点击上方「打开账号主页」联网浏览并手动缓存，或切回「全部」查看该订阅其他关联账号的内容。"
		}
		ui.Column(c).Label("订阅内容空状态说明").Grow(1).FillWidth().Padding(48, 24).AlignItems(ui.Center).Gap(10).ClipX().Children(func() {
			ui.Text(c, emptyTitle).FontSize(17).FontWeight(600).TextColor(ColorTextMain).SingleLine()
			ui.Text(c, emptyHint).
				FontSize(14).LineHeight(LineHeightCompact).TextColor(ColorTextMuted).MaxLines(3)
		})
		return
	}

	ui.List(c, a.SubFeedListState, len(a.SubFeedItems), func(i int) {
		if i < 0 || i >= len(a.SubFeedItems) {
			return
		}
		entry := a.SubFeedItems[i]
		post := a.feedEntryToPostCard(entry)
		// Subscription Content tab is strictly local/zero-network: suppress remote avatar fetches
		// while preserving InHomeFeed site-badge and dotless card layout.
		post.AuthorFace = ""
		post.LocalManage = true
		if post.Origin != nil {
			orig := *post.Origin
			orig.AuthorFace = ""
			post.Origin = &orig
		}
		ui.Column(c).Label("订阅内容行 " + entry.FeedKey).FillWidth().Children(func() {
			a.renderPostCard(c, post)
		})
		if i == len(a.SubFeedItems)-1 && !a.SubFeedMoreAfter {
			ui.Row(c).FillWidth().Padding(18, 16).Justify(ui.Center).Children(func() {
				ui.Text(c, "已经到最早的缓存了").FontSize(13).TextColor(ColorTextMuted).SingleLine()
			})
		}
	}).Label("订阅内容信息流列表").Grow(1).FillWidth()
}

// subscriptionEmptyStateHint tailors the empty content-tab explanation to the
// subscription's actual capability tags without issuing any site requests (DEC-080).
func subscriptionEmptyStateHint(sub subscription.Subscription) string {
	if len(subscriptionEffectiveAccounts(sub)) == 0 {
		return "还没有关联站点账号。切换到「账号」标签关联账号后，即可开始追踪和缓存内容。"
	}
	tags := effectiveSubTags(sub)
	hasFull := subscription.HasTag(tags, subscription.TagFullCache)
	hasAutoCache := subscription.HasAnyAutoCacheTag(tags)
	hasSched := subscription.HasTag(tags, subscription.TagScheduledCheck)
	switch {
	case hasFull:
		return "已开启「全部缓存」，后台慢速缓存队列会陆续补齐该订阅的历史作品；也可以切到「账号」打开主页，立即浏览并手动缓存。"
	case hasAutoCache && hasSched:
		return "已开启自动缓存，之后新发布的内容会自动追踪并缓存到这里；以前的作品可以切到「账号」打开主页浏览或手动缓存。"
	case hasAutoCache && !hasSched:
		return "已开启自动缓存但未开启「定时检查」，仅在手动检查更新时才会追踪新内容；可在上方切换追踪模式，或切到「账号」打开主页手动缓存。"
	case hasSched:
		return "当前仅检查新动态、未开启自动缓存。可在上方切换追踪模式以自动缓存，或切到「账号」打开主页浏览并手动缓存。"
	default:
		return "当前未开启自动缓存。可在上方选择追踪模式（如「完整存档」或「追更并保存」），或切到「账号」打开主页浏览并手动缓存。"
	}
}

func formatSubscriptionAccountIdent(acc subscription.AccountRef) string {
	switch normalizeSiteIDUI(acc.Site) {
	case "douyin":
		if acc.UniqueID != "" {
			return "抖音号：" + acc.UniqueID
		}
		return "ID：" + acc.UID
	case "x":
		if h := strings.TrimPrefix(strings.TrimSpace(acc.UniqueID), "@"); h != "" {
			return "@" + h
		}
		return "ID:" + acc.UID
	default:
		return "UID:" + acc.UID
	}
}

type subAccountLiveView struct {
	noRoom       bool
	isLive       bool
	isRecording  bool
	hasRoom      bool
	title        string
	qualSpec     string
	recDurSec    int
	recBytes     int64
	biliRoomID   int64
	roomOrWebRID string
	accountKey   string
	recordingKey string
	startLabel   string
	stopLabel    string
}

func (a *App) lookupSubAccountLiveView(acc subscription.AccountRef) subAccountLiveView {
	accSite := normalizeSiteIDUI(acc.Site)
	uid := strings.TrimSpace(acc.UID)
	accountKey := accSite + ":" + uid
	lv := subAccountLiveView{
		accountKey:   accountKey,
		recordingKey: accountKey,
		startLabel:   "开始录制 " + accountKey,
		stopLabel:    "停止录制 " + accountKey,
	}
	if accSite == "weibo" || accSite == "x" {
		return lv
	}

	if accSite == "bilibili" {
		mid, _ := strconv.ParseInt(uid, 10, 64)
		for _, rm := range a.LiveRooms {
			if mid > 0 && rm.UID == mid {
				lv.biliRoomID = rm.RoomID
				lv.isLive = rm.LiveStatus == 1
				lv.isRecording = rm.Recording
				lv.noRoom = rm.RoomID == 0 && !lv.isLive && !lv.isRecording
				lv.hasRoom = rm.RoomID > 0 || lv.isLive
				lv.title = strings.TrimSpace(rm.Title)
				lv.qualSpec = bilibili.FormatLiveQualitySpec(rm.QualityName, rm.Quality, rm.Codec)
				lv.recDurSec = int(rm.RecordDurationSec)
				lv.recBytes = rm.RecordBytes
				if rm.RoomID > 0 {
					lv.startLabel = fmt.Sprintf("开始录制 %d", rm.RoomID)
					lv.stopLabel = fmt.Sprintf("停止录制 %d", rm.RoomID)
				}
				return lv
			}
		}
		for _, st := range a.LiveAccounts {
			if strings.EqualFold(st.Site, "bilibili") && st.UID == uid {
				roomID, _ := strconv.ParseInt(st.RoomID, 10, 64)
				lv.biliRoomID = roomID
				lv.roomOrWebRID = firstNonEmptyStrUI(st.WebRID, st.RoomID)
				lv.isLive = st.LiveStatus == 1
				lv.isRecording = st.Recording
				lv.noRoom = st.NoRoom || (roomID == 0 && st.RoomID == "" && !lv.isLive && !lv.isRecording)
				lv.hasRoom = roomID > 0 || st.RoomID != "" || lv.isLive
				lv.title = strings.TrimSpace(st.Title)
				lv.qualSpec = formatLiveAccountQualitySpec(st)
				lv.recDurSec = int(st.RecordDurationSec)
				lv.recBytes = st.RecordBytes
				lv.recordingKey = firstNonEmptyStrUI(st.RecordingKey, st.AccountKey, accountKey)
				if roomID > 0 {
					lv.startLabel = fmt.Sprintf("开始录制 %d", roomID)
					lv.stopLabel = fmt.Sprintf("停止录制 %d", roomID)
				}
				return lv
			}
		}
		return lv
	}

	for _, st := range a.LiveAccounts {
		if strings.EqualFold(st.Site, accSite) && st.UID == uid {
			lv.roomOrWebRID = firstNonEmptyStrUI(st.WebRID, st.RoomID)
			lv.accountKey = firstNonEmptyStrUI(st.AccountKey, accountKey)
			lv.recordingKey = firstNonEmptyStrUI(st.RecordingKey, lv.accountKey)
			lv.isLive = st.LiveStatus == 1
			lv.isRecording = st.Recording
			lv.noRoom = st.NoRoom
			lv.hasRoom = !st.NoRoom && (lv.roomOrWebRID != "" || lv.isLive)
			lv.title = strings.TrimSpace(st.Title)
			lv.qualSpec = formatLiveAccountQualitySpec(st)
			lv.recDurSec = int(st.RecordDurationSec)
			lv.recBytes = st.RecordBytes
			lv.startLabel = "开始录制 " + lv.accountKey
			lv.stopLabel = "停止录制 " + lv.accountKey
			return lv
		}
	}
	return lv
}

func formatSubAccountLiveStatus(lv subAccountLiveView) (string, ui.Color, int) {
	if lv.isRecording {
		parts := []string{
			"录制中",
			"已录 " + FormatDuration(lv.recDurSec),
			FormatBytes(lv.recBytes),
		}
		if lv.qualSpec != "" {
			parts = append(parts, lv.qualSpec)
		}
		return strings.Join(parts, " · "), ColorLive, 600
	}
	if lv.isLive {
		parts := []string{"直播中"}
		if lv.title != "" {
			parts = append(parts, lv.title)
		}
		if lv.qualSpec != "" {
			parts = append(parts, lv.qualSpec)
		}
		return strings.Join(parts, " · "), ColorLive, 600
	}
	if lv.noRoom {
		return "未开通直播间", ColorTextMuted, 400
	}
	return "未开播", ColorTextMuted, 400
}

func (a *App) openAccountUserPage(accSite, uid, name string) {
	switch normalizeSiteIDUI(accSite) {
	case "douyin":
		a.OpenDouyinUser(uid, name)
	case "weibo":
		a.OpenWeiboUser(uid, name)
	case "x":
		a.OpenXUser(uid, name)
	default:
		if mid, err := strconv.ParseInt(uid, 10, 64); err == nil && mid > 0 {
			a.OpenUser(mid, name)
		}
	}
}

func (a *App) renderSubscriptionAccountsTab(c *ui.Context, sub *subscription.Subscription) {
	accounts := subscriptionEffectiveAccounts(*sub)
	ui.Column(c).
		FillWidth().
		Padding(16).
		Radius(16).
		Background(ColorBgMain).
		Border(1, ColorBorder).
		Gap(10).
		ClipX().
		Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
				ui.Text(c, fmt.Sprintf("关联账号（%d 个）", len(accounts))).FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
				secOpen := a.SubAccountMenuKey == "section:live"
				MoreMenuButton(c, "订阅直播更多", 30, &secOpen, func(m *ui.Menu) {
					if m.Item("刷新直播状态").Chosen() {
						a.refreshVisibleLive()
					}
				})
				keyedMenuOpen(&a.SubAccountMenuKey, "section:live", secOpen)
			})

			if len(accounts) == 0 {
				ui.Text(c, "暂未关联站点账号，可在下方粘贴主页链接、分享文本或 UID 关联。").
					FontSize(13).TextColor(ColorTextMuted).MaxLines(2)
			}

			for _, accItem := range accounts {
				acc := accItem
				accSite := normalizeSiteIDUI(acc.Site)
				isNoLive := accSite == "weibo" || accSite == "x"
				var uidNum int64
				if accSite == "bilibili" {
					_, _ = fmt.Sscanf(acc.UID, "%d", &uidNum)
				}
				lv := a.lookupSubAccountLiveView(acc)
				liveText, liveColor, liveWeight := formatSubAccountLiveStatus(lv)
				menuKey := "acct:" + accSite + ":" + acc.UID
				openAccountPage := func() {
					targetSite, targetID, targetName := accountOpenTarget(acc)
					a.openAccountUserPage(targetSite, targetID, targetName)
				}

				ui.Row(c).
					FillWidth().
					Padding(8, 10).
					Radius(12).
					Background(ColorBgMain).
					Border(1, ColorBorder).
					AlignItems(ui.Center).
					Justify(ui.SpaceBetween).
					Gap(8).
					ClipX().
					Children(func() {
						bodyBtn := ui.ButtonBase(c).
							Label("订阅账号主页："+accSite+":"+acc.UID).
							Tooltip("联网打开该账号的站点主页").
							Grow(1).
							Padding(4, 6).
							Radius(8).
							AlignItems(ui.Center).
							Justify(ui.Start).
							Gap(10).
							ClipX()
						if bodyBtn.Hovered() {
							bodyBtn.Background(ColorHover)
						}
						bodyBtn.Children(func() {
							a.renderAccountAvatarCircle(c, accSite, acc.Name, acc.UID, uidNum, 40, !isNoLive && (lv.isLive || lv.isRecording), true, acc.Face)
							ui.Column(c).Grow(1).Gap(4).ClipX().Children(func() {
								ui.Row(c).AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
									ui.Text(c, firstNonEmptyStrUI(acc.Name, acc.UniqueID, acc.UID)).FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine().Shrink(1)
									renderSiteSmallBadgeWithDot(c, accSite, "订阅详情账号站点："+accSite+":"+acc.UID)
									ui.Text(c, "· "+formatSubscriptionAccountIdent(acc)).FontSize(12).TextColor(ColorTextMuted).SingleLine().Shrink(1)
								})
								if !isNoLive {
									ui.Text(c, liveText).FontSize(12).FontWeight(liveWeight).TextColor(liveColor).SingleLine()
								}
								if works := a.accountWorksText(accSite, acc.UID); works != "" {
									ui.Text(c, works).FontSize(12).TextColor(ColorTextMuted).SingleLine()
								}
							})
						})
						if bodyBtn.Clicked() {
							openAccountPage()
						}

						ui.Row(c).Gap(6).AlignItems(ui.Center).Shrink(0).Children(func() {
							openHomeBtn := ui.ButtonBase(c).
								Label("打开关联账号主页："+accSite+":"+acc.UID).
								Tooltip("联网打开该账号的站点主页").
								Padding(4, 10).
								Radius(999).
								Background(ColorBgMain).
								Border(1, ColorBorder).
								Gap(4).
								AlignItems(ui.Center).
								Shrink(0)
							if openHomeBtn.Hovered() {
								openHomeBtn.Background(ColorAccentBg).BorderColor(ColorAccent)
							}
							openHomeBtn.Children(func() {
								col := ColorTextMuted
								if openHomeBtn.Hovered() {
									col = ColorAccent
								}
								DrawIcon(c, "external-link", 12, col, 1.7)
								ui.Text(c, "打开主页").FontSize(12).FontWeight(500).TextColor(col).SingleLine()
							})
							if openHomeBtn.Clicked() {
								openAccountPage()
							}

							if !isNoLive {
								if lv.isRecording {
									if DangerOutlinePillButton(c, "停止录制", lv.stopLabel).Clicked() {
										if accSite == "bilibili" && lv.biliRoomID > 0 {
											a.stopLiveRecording(lv.biliRoomID)
										} else {
											a.stopAccountLiveRecording(lv.recordingKey)
										}
									}
								} else if lv.isLive {
									if AccentPillButton(c, "录制", lv.startLabel).Clicked() {
										if accSite == "bilibili" && lv.biliRoomID > 0 {
											a.startLiveRecording(lv.biliRoomID)
										} else {
											a.startAccountLiveRecording(accSite, acc.UID, lv.roomOrWebRID)
										}
									}
								}
							}

							open := a.SubAccountMenuKey == menuKey
							MoreMenuButton(c, "订阅账号更多："+accSite+":"+acc.UID, 30, &open, func(m *ui.Menu) {
								if m.Item("打开账号主页").Chosen() {
									openAccountPage()
								}
								if !isNoLive && lv.hasRoom {
									if m.Item("在浏览器中观看").Chosen() {
										if accSite == "douyin" {
											a.showError(a.backend.OpenLiveInBrowser("douyin", lv.roomOrWebRID))
										} else if lv.biliRoomID > 0 {
											a.showError(a.backend.OpenLiveRoomInBrowser(lv.biliRoomID))
										}
									}
								}
								if m.Item("用作头像").Chosen() {
									sub.Avatar = subscription.AvatarRef{Site: acc.Site, UID: acc.UID}
									_, err := a.backend.UpsertSubscription(*sub)
									a.showError(err)
									if err == nil {
										a.RefreshData()
									}
								}
								if !isNoLive && m.Item("刷新直播状态").Chosen() {
									a.refreshSingleAccountLive(accSite, acc.UID)
								}
								if m.Item("更改关联订阅").Chosen() {
									a.OpenMoveAccountPicker(*sub, acc)
								}
								m.Separator()
								if m.Item("解除关联").Danger().Chosen() {
									_, err := a.backend.UnlinkSubscriptionAccount(sub.ID, acc.Site, acc.UID)
									a.showError(err)
									if err == nil {
										a.RefreshData()
									}
								}
							})
							keyedMenuOpen(&a.SubAccountMenuKey, menuKey, open)
						})
					})
			}

			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).ClipX().Children(func() {
				ui.TextInput(c, &a.SubAccountInput).
					Label("关联账号输入").
					Placeholder("关联另一个账号：粘贴主页链接、分享文本或 UID").
					Grow(1)
				if OutlinePillButton(c, "预览关联账号", "预览关联账号").Clicked() {
					a.previewSubAccount(sub.ID)
				}
			})

			if a.AccountPreview != nil && a.AccountPreviewSubID == sub.ID {
				acc := *a.AccountPreview
				accSite := normalizeSiteIDUI(acc.Site)
				var previewMid int64
				if accSite == "bilibili" {
					_, _ = fmt.Sscanf(acc.UID, "%d", &previewMid)
				}
				ui.Column(c).
					FillWidth().
					Padding(10, 12).
					Radius(10).
					Background(ColorInfoBlueBg).
					Border(1, ColorInfoBlue).
					Gap(6).
					Children(func() {
						ui.Row(c).AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
							ui.Row(c).AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
								a.renderAccountAvatar(c, accSite, acc.Name, acc.UID, previewMid, 32, false, "", nil, acc.Face)
								ui.Text(c, firstNonEmptyStrUI(acc.Name, acc.UID)).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine().Shrink(1)
								renderSiteSmallBadgeWithDot(c, accSite, "关联预览站点")
								ui.Text(c, "· "+formatSubscriptionAccountIdent(acc)).FontSize(12).TextColor(ColorTextMuted).SingleLine().Shrink(1)
							})
							if AccentPillButton(c, "确认关联或迁移", "确认关联或迁移").Clicked() {
								var err error
								if a.AccountPreviewOwner != "" {
									_, err = a.backend.MoveSubscriptionAccount(acc.Site, acc.UID, sub.ID)
								} else {
									_, err = a.backend.LinkSubscriptionAccountRef(sub.ID, acc)
								}
								a.showError(err)
								if err == nil {
									a.AccountPreview = nil
									a.SubAccountInput = ""
									a.RefreshData()
								}
							}
						})
						if a.AccountPreviewOwner != "" {
							ui.Text(c, fmt.Sprintf("该账号当前已属于订阅「%s」，确认后将迁移至当前订阅。", a.AccountPreviewOwner)).
								FontSize(12).TextColor(ColorInfoBlue).MaxLines(2)
						}
					})
			}
		})
}
