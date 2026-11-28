package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/chialecode/trace/internal/uistate"
	"github.com/egoist/mygo/ui"
)

func normalizeAppRoute(route string) string {
	trimmed := strings.TrimSpace(route)
	if q := strings.IndexByte(trimmed, '?'); q >= 0 {
		trimmed = strings.TrimSpace(trimmed[:q])
	}
	switch {
	case trimmed == "" || !strings.HasPrefix(trimmed, "/"):
		return RouteHome
	case strings.HasPrefix(trimmed, RouteDouyinWork+"/"):
		return RouteDouyinWork
	case strings.HasPrefix(trimmed, RouteDouyinUser+"/"):
		return RouteDouyinUser
	case strings.HasPrefix(trimmed, RouteWeiboPost+"/"):
		return RouteWeiboPost
	case strings.HasPrefix(trimmed, RouteWeiboUser+"/"):
		return RouteWeiboUser
	case strings.HasPrefix(trimmed, RouteXPost+"/"):
		return RouteXPost
	case strings.HasPrefix(trimmed, RouteXUser+"/"):
		return RouteXUser
	case strings.HasPrefix(trimmed, RouteXList+"/"):
		return RouteXList
	case strings.HasPrefix(trimmed, RouteSettings+"/"):
		return RouteSettings
	case strings.HasPrefix(trimmed, RouteSubscriptions+"/"):
		return RouteSubscriptions
	default:
		return trimmed
	}
}

func normalizeSiteFilter(siteFilter string) string {
	switch strings.ToLower(strings.TrimSpace(siteFilter)) {
	case string(site.Bilibili):
		return string(site.Bilibili)
	case string(site.Douyin):
		return string(site.Douyin)
	case string(site.Weibo):
		return string(site.Weibo)
	case string(site.X):
		return string(site.X)
	default:
		return "all"
	}
}

func routeExclusiveSite(route string) string {
	switch normalizeAppRoute(route) {
	case RouteDouyinWork, RouteDouyinUser:
		return string(site.Douyin)
	case RouteWeiboPost, RouteWeiboUser:
		return string(site.Weibo)
	case RouteXPost, RouteXUser, RouteXList:
		return string(site.X)
	case RouteVideo, RouteUser:
		return string(site.Bilibili)
	default:
		return ""
	}
}

func siteExpiredLoginPrompt(siteID string) string {
	switch siteID {
	case string(site.Douyin):
		return "抖音登录已失效，请重新粘贴 Cookie"
	case string(site.Weibo):
		return "微博登录已失效，请重新粘贴 Cookie"
	case string(site.X):
		return "X 登录已失效，请重新粘贴 Cookie"
	default:
		return "登录已失效，请重新登录"
	}
}

func siteExpiredTopNotice(siteID string) string {
	switch siteID {
	case string(site.Douyin):
		return "抖音登录已失效，抖音相关任务与录制已暂停；可前往设置重新粘贴 Cookie"
	case string(site.Weibo):
		return "微博登录已失效，微博相关任务已暂停；可前往设置重新粘贴 Cookie"
	case string(site.X):
		return "X 登录已失效，X 相关任务已暂停；可前往设置重新粘贴 Cookie"
	case string(site.Bilibili):
		return "哔哩哔哩登录已失效，B 站相关任务与录制已暂停；可前往设置重新登录"
	default:
		return "登录已失效，请重新登录"
	}
}

func (a *App) currentRouteRequiresSite(siteID string) bool {
	siteID = strings.ToLower(strings.TrimSpace(siteID))
	switch siteID {
	case string(site.Douyin):
		if a.CurrentRoute == RouteDouyinUser {
			return true
		}
		if a.CurrentRoute == RouteDouyinWork && a.CurrentLibraryDetail == nil {
			return true
		}
	case string(site.Weibo):
		if a.CurrentRoute == RouteWeiboUser {
			return true
		}
		if a.CurrentRoute == RouteWeiboPost && a.CurrentLibraryDetail == nil {
			return true
		}
	case string(site.X):
		if a.CurrentRoute == RouteXUser || a.CurrentRoute == RouteXList {
			return true
		}
		if a.CurrentRoute == RouteXPost && a.CurrentLibraryDetail == nil {
			return true
		}
	case string(site.Bilibili):
		if a.CurrentRoute == RouteUser {
			return true
		}
		if a.CurrentRoute == RouteVideo && a.CurrentLibraryDetail == nil {
			return true
		}
	}
	return false
}

// closeAllModalsAndOverlays closes all active modals, dialogs, and media
// layers (and restores native fullscreen). Shared by TriggerAuthExpired and
// single-site expiry redirect so nothing stays painted over /login.
func (a *App) closeAllModalsAndOverlays() {
	a.closePlayerOverlay()
	a.closeImageViewer()
	a.closeLivePreview()
	a.stopBGM()
	a.ImportSubModalOpen = false
	a.ConfirmLogoutOpen = false
	a.Remote.ConfirmPushXOpen = false
	a.Mobile.PairDialogOpen = false
	a.Mobile.ConfirmResetOpen = false
	a.Mobile.ConfirmRevokeID = ""
	a.DeleteConfirm.Open = false
	a.ForceRecacheConfirm.Open = false
	a.RestartConfirmOpen = false
	a.AddSubDialogOpen = false
	a.SubLinks.Open = false
	a.RuleBatch.Open = false
	a.MultiTagBatch.Open = false
	a.SubscribePicker.Open = false
	a.LibraryRefresh.Open = false
	a.Transfer.ExportOpen = false
	a.SubFile.ExportOpen = false
	a.SubFile.ImportOpen = false
	a.ConflictDiff.Open = false
	a.XUserLists.Open = false
	a.subImportGen++
	a.exitLibrarySelectMode()
}

func (a *App) handleSiteAccountLoss(evSite, kind string) {
	if a.ImportSubModalOpen && (evSite == normalizeSiteIDUI(a.ImportSite) || evSite == "") {
		a.ImportSubModalOpen = false
	}
	if !a.backend.HasAnyLogin() {
		reason := ""
		if kind == "expired" {
			reason = siteExpiredLoginPrompt(evSite)
		}
		if id, ok := site.ParseID(evSite); ok {
			a.LoginSite = string(id)
		} else if excl := routeExclusiveSite(a.CurrentRoute); excl != "" {
			a.LoginSite = excl
		}
		a.TriggerAuthExpired(reason)
		return
	}
	// At least one other site remains logged in: refresh local lists without forcing full-app logout.
	if a.hasDispatcher() {
		a.postCards = nil
		a.cacheStateRev++
		a.libraryRev++
		a.subFeedDirty = true
		a.cachedDiagAt = time.Time{}
		a.HistoryItems = a.backend.ListHistory()
		a.refreshSubscriptionsListOnly()
		a.LiveRooms = bilibiliLiveRooms(a.backend.ListLiveRooms())
		a.syncLiveAccountsFromBackend()
		a.liveLibItemsCacheRev = 0
		a.refreshTasks()
		a.refreshCacheConflicts()
		a.refreshTransferJobs()
		a.scheduleLibraryChangedRefresh()
	} else {
		a.RefreshData()
	}
	if kind != "expired" {
		return
	}
	if a.currentRouteRequiresSite(evSite) {
		a.closeAllModalsAndOverlays()
		a.openSeq++
		a.Opening = nil
		a.SavedRoute = a.CurrentRoute
		if evSite != "" {
			a.LoginSite = evSite
		}
		a.CurrentRoute = RouteLogin
		a.setLoginError(siteExpiredLoginPrompt(evSite))
		a.notifyError(a.LoginError, "")
		return
	}
	if a.expiredNoticeShown == nil {
		a.expiredNoticeShown = make(map[string]bool)
	}
	noticeKey := evSite
	if noticeKey == "" {
		noticeKey = "default"
	}
	if !a.expiredNoticeShown[noticeKey] {
		a.expiredNoticeShown[noticeKey] = true
		a.notifyError(siteExpiredTopNotice(evSite), evSite)
	}
}

// TriggerAuthExpired transitions to the login view while preserving the current route and loaded data.
func (a *App) TriggerAuthExpired(reason string) {
	a.closeAllModalsAndOverlays()
	a.openSeq++
	a.Opening = nil
	a.cancelQRLogin()
	if a.CurrentRoute != RouteLogin {
		a.SavedRoute = a.CurrentRoute
	}
	if excl := routeExclusiveSite(a.SavedRoute); excl != "" {
		a.LoginSite = excl
	}
	a.CurrentRoute = RouteLogin
	a.setLoginError(reason)
	a.notifyError(reason, "")
	a.backend.PauseAllActiveWork()
}

// CompleteLogin transitions back to SavedRoute (or RouteHome) after successful login while keeping loaded data intact.
func (a *App) CompleteLogin() {
	a.setLoginError("")
	a.clearErrorNotifications()
	a.clearUnsentLoginInputs()
	if a.expiredNoticeShown != nil {
		for _, id := range site.All() {
			if a.backend.StatusOf(string(id)).LoggedIn {
				delete(a.expiredNoticeShown, string(id))
			}
		}
	}
	a.visibleLiveNextRead = nil
	a.visibleLiveStopped = false
	if a.SavedRoute != "" && a.SavedRoute != RouteLogin {
		a.CurrentRoute = a.SavedRoute
		a.SavedRoute = ""
	} else if a.CurrentRoute == RouteLogin {
		a.CurrentRoute = RouteHome
	}
	a.RefreshData()
}

// OpenLoginForSite navigates to /login pre-selecting siteID ("bilibili" or "douyin").
func (a *App) OpenLoginForSite(siteID string) {
	a.closeAllModalsAndOverlays()
	if id, ok := site.ParseID(siteID); ok {
		a.LoginSite = string(id)
	}
	if a.CurrentRoute != RouteLogin {
		a.SavedRoute = a.CurrentRoute
	}
	a.setLoginError("")
	a.Navigate(RouteLogin)
}

// currentReturnRoute encodes the active route plus sub-page context (such as a
// specific subscription detail view and its tab) so NavigateBack restores it.
func (a *App) currentReturnRoute() string {
	if a.CurrentRoute == RouteSubscriptions {
		if a.SubDetailOpen && a.SelectedSubID != "" {
			return fmt.Sprintf("%s/%s?tab=%s", RouteSubscriptions, a.SelectedSubID, normalizeSubDetailTab(a.SubDetailTab))
		}
		if a.SubAccountsOpen {
			return RouteSubscriptions + "/accounts"
		}
		if a.SubChecksOpen {
			return RouteSubscriptions + "/checks"
		}
	}
	return a.CurrentRoute
}

func (a *App) pushPreviousRoute(from string) {
	if from == "" || normalizeAppRoute(from) == RouteLogin {
		return
	}
	a.PreviousRoute = from
	if n := len(a.navBackStack); n == 0 || a.navBackStack[n-1] != from {
		a.navBackStack = append(a.navBackStack, from)
		if len(a.navBackStack) > 24 {
			a.navBackStack = a.navBackStack[len(a.navBackStack)-24:]
		}
	}
}

// Navigate switches the active route and persists it in ui-state.json.
func (a *App) Navigate(route string) {
	a.navigate(route, false)
}

func (a *App) navigate(rawRoute string, fromBack bool) {
	from := a.currentReturnRoute()
	prevRoute := a.CurrentRoute
	route := normalizeAppRoute(rawRoute)
	if prevRoute == RouteLogin && route != RouteLogin {
		a.setLoginError("")
	}
	if prevRoute == RouteParse && route != RouteParse {
		a.setLastParseError("")
	}
	a.openSeq++
	a.Opening = nil
	a.PostMenuKey = ""
	a.SubAccountMenuKey = ""
	a.DouyinCardMenuID = ""
	a.DouyinUserSubMenuOpen = false
	a.WeiboUserSubMenuOpen = false
	a.XUserSubMenuOpen = false
	a.HistoryHeaderMenuOpen = false
	a.HistoryClearConfirmOpen = false
	a.HistoryItemMenuID = ""
	if a.CurrentRoute == RouteLogin && route != RouteLogin {
		a.cancelQRLogin()
		a.clearUnsentLoginInputs()
	}
	if a.CurrentRoute == RouteSettings && route != RouteSettings {
		a.Remote.CodeInput = ""
		a.Remote.TokenInput = ""
		a.Remote.ConfirmPushXOpen = false
		a.Mobile.PairDialogOpen = false
		a.Mobile.ConfirmResetOpen = false
		a.Mobile.ConfirmRevokeID = ""
	}
	if route != RouteLibraryItem {
		a.CurrentLibraryDetail = nil
	}
	if prevRoute == RouteLibrary && route != RouteLibrary {
		a.exitLibrarySelectMode()
	}
	if route == RouteHome && a.CurrentRoute == RouteHome && a.HomeActorFilter != nil {
		a.closeHomeActorFeed()
	}
	if route == RouteSubscriptions {
		a.SubDetailOpen = false
		a.SubAccountsOpen = false
		a.SubChecksOpen = false
		a.SubMoreMenuOpen = false
		a.SubManageMenuOpen = false
		a.SubManageModalMode = ""
		a.applySubscriptionRouteNavigate(rawRoute)
	}
	if a.CurrentRoute == RouteHome && route != RouteHome {
		a.flushHomePosition()
		if a.HomeActorFilter != nil {
			a.closeHomeActorFeed()
		}
	}
	if route == RouteHome && a.CurrentRoute != RouteHome {
		a.enterHomeSubscriptions()
	}
	if route != RouteLogin && route != RouteSettings && !a.backend.HasAnyLogin() {
		a.SavedRoute = route
		if excl := routeExclusiveSite(route); excl != "" {
			a.LoginSite = excl
		}
		route = RouteLogin
	}
	if route != RouteTasks {
		a.closeLivePreview()
	}
	if !fromBack && prevRoute != route && prevRoute != RouteLogin {
		a.pushPreviousRoute(from)
	}
	a.CurrentRoute = route
	if (route == RouteLibrary || route == RouteLibraryItem) && a.libraryItemsDirty && a.backend.HasAnyLogin() {
		a.libraryItemsDirty = false
		a.reloadLibraryItems()
	}
	a.ensureVisibleDerivedDataFresh()
	if route == RouteHome && a.backend.HasAnyLogin() {
		if len(a.FeedItems) == 0 {
			a.loadInitialFeed()
		}
		if a.HomeActorFilter == nil && len(a.FeedItems) > 0 {
			a.feedMoreBefore = true
		}
	}
	if _, err := a.backend.MutateUIState(func(st *uistate.State) { st.LastRoute = route }); err != nil {
		a.showError(err)
	}
}

// OpenSubscriptionDetail opens the /subscriptions/:id detail view for sub.
func (a *App) OpenSubscriptionDetail(sub subscription.Subscription) {
	a.Navigate(RouteSubscriptions)
	a.SelectedSubID = sub.ID
	a.SelectedSubMid = sub.Mid
	a.SubDetailOpen = true
	a.SubAccountsOpen = false
	a.SubChecksOpen = false
	a.SubMoreMenuOpen = false
	a.SubManageMenuOpen = false
	a.SubAccountMenuKey = ""
	a.SubManageModalMode = ""
	a.closeSubLinksModal()
	a.SubDetailTab = normalizeSubDetailTab(a.SubDetailTab)
	a.refreshAccountWorks()
	a.syncSelectedSubRules(sub)
	a.ensureSubscriptionFeedLoaded(&sub)
}

// NavigateBack returns to PreviousRoute or RouteHome (or RouteLibrary from RouteLibraryItem).
func (a *App) NavigateBack() {
	target := a.PreviousRoute
	if n := len(a.navBackStack); n > 0 && a.navBackStack[n-1] == target {
		a.navBackStack = a.navBackStack[:n-1]
		if len(a.navBackStack) > 0 {
			a.PreviousRoute = a.navBackStack[len(a.navBackStack)-1]
		} else {
			a.PreviousRoute = ""
		}
	} else {
		a.PreviousRoute = ""
	}
	if target == "" || target == a.currentReturnRoute() {
		if a.CurrentRoute == RouteLibraryItem {
			target = RouteLibrary
		} else {
			target = RouteHome
		}
	}
	a.navigate(target, true)
}

func (a *App) syncLiveAccountsFromBackend() {
	accounts := a.backend.ListLiveAccounts()
	if len(accounts) == 0 && len(a.LiveAccounts) == 0 {
		return
	}
	// Preserve previously fetched Douyin live snapshots that aren't in Bilibili's ListLiveAccounts unless replaced.
	var keptDouyin []service.LiveAccountState
	for _, prev := range a.LiveAccounts {
		if prev.Site == string(site.Douyin) {
			replaced := false
			for _, cur := range accounts {
				if cur.Site == prev.Site && ((cur.UID != "" && cur.UID == prev.UID) || (cur.RoomID != "" && cur.RoomID == prev.RoomID)) {
					replaced = true
					break
				}
			}
			if !replaced {
				keptDouyin = append(keptDouyin, prev)
			}
		}
	}
	a.LiveAccounts = append(accounts, keptDouyin...)
}

func (a *App) mergeLiveAccountStates(states []service.LiveAccountState) {
	for _, st := range states {
		found := false
		for i, prev := range a.LiveAccounts {
			if prev.Site == st.Site && ((st.AccountKey != "" && prev.AccountKey == st.AccountKey) || (st.UID != "" && prev.UID == st.UID) || (st.RoomID != "" && prev.RoomID == st.RoomID)) {
				a.LiveAccounts[i] = st
				found = true
				break
			}
		}
		if !found {
			a.LiveAccounts = append(a.LiveAccounts, st)
		}
		if a.CurrentDouyinUser != nil && st.Site == string(site.Douyin) && (st.UID == a.CurrentDouyinUser.SecUID || (st.RoomID != "" && st.RoomID == a.CurrentDouyinUser.RoomID)) {
			cp := st
			a.CurrentDouyinUser.LiveRoom = &cp
			a.CurrentDouyinUser.LiveStatus = st.LiveStatus
		}
	}
}

func (a *App) refreshSubscriptionsListOnly() {
	if !a.backend.HasAnyLogin() {
		return
	}
	subs, err := a.backend.ListSubscriptions()
	if err != nil {
		a.showError(err)
		return
	}
	a.Subscriptions = subs
	a.subsRev++
	a.checkPlanAt = time.Time{}
}

func (a *App) refreshSubscriptions() {
	if !a.backend.HasAnyLogin() {
		return
	}
	a.refreshSubscriptionsListOnly()
	a.refreshAccountWorks()
}

// ownerOfAccountInMemory resolves subscription ownership from in-memory
// a.Subscriptions in O(1), avoiding per-frame SQLite queries in View().
func (a *App) ownerOfAccountInMemory(siteID, uid string, mid int64) (subscription.Subscription, bool) {
	var firstPtr *subscription.Subscription
	if len(a.Subscriptions) > 0 {
		firstPtr = &a.Subscriptions[0]
	}
	if a.subOwnerByKey == nil || a.subOwnerIndexRev != a.subsRev || a.subOwnerIndexN != len(a.Subscriptions) || a.subOwnerIndexFirst != firstPtr {
		a.subOwnerIndexRev = a.subsRev
		a.subOwnerIndexN = len(a.Subscriptions)
		a.subOwnerIndexFirst = firstPtr
		a.subOwnerByKey = make(map[string]subscription.Subscription, len(a.Subscriptions))
		a.subOwnerByMid = make(map[int64]subscription.Subscription, len(a.Subscriptions))
		for _, sub := range a.Subscriptions {
			if sub.Mid > 0 {
				a.subOwnerByMid[sub.Mid] = sub
			}
			for _, acc := range sub.Accounts {
				normSite := normalizeSiteIDUI(acc.Site)
				normUID := strings.TrimSpace(acc.UID)
				if normUID != "" {
					a.subOwnerByKey[normSite+":"+normUID] = sub
				}
				if normSite == "bilibili" {
					if id, err := strconv.ParseInt(normUID, 10, 64); err == nil && id > 0 {
						a.subOwnerByMid[id] = sub
					}
				}
			}
		}
	}
	normSite := normalizeSiteIDUI(siteID)
	normUID := strings.TrimSpace(uid)
	if normUID != "" {
		if sub, ok := a.subOwnerByKey[normSite+":"+normUID]; ok {
			return sub, true
		}
	}
	if normSite == "bilibili" && mid > 0 {
		if sub, ok := a.subOwnerByMid[mid]; ok {
			return sub, true
		}
	}
	return subscription.Subscription{}, false
}

// refreshAccountWorks reloads the works numbers of subscribed accounts. It
// reads only local data: totals come from earlier parses (DEC-061).
func (a *App) refreshAccountWorks() {
	if !a.backend.HasAnyLogin() {
		return
	}
	a.accountWorks = a.backend.SubscriptionAccountWorks()
	a.accountWorksDirty = false
	a.accountWorksRequestedGen = a.accountWorksDirtyGen
}

func (a *App) refreshLibraryStatistics() {
	stats, err := a.backend.LibraryStatistics()
	if err != nil {
		a.showError(fmt.Errorf("读取媒体库统计失败：%w", err))
		return
	}
	a.libraryStatistics = stats
	a.statsRev++
	a.libraryStatsDirty = false
	a.libraryStatsRequestedGen = a.libraryStatsDirtyGen
}

func (a *App) refreshTasks() {
	if !a.backend.HasAnyLogin() {
		return
	}
	a.CacheQueue = a.backend.ListCacheQueue()
	tasks, err := a.backend.ListDownloadTasks()
	if err != nil {
		a.showError(err)
		return
	}
	a.Tasks = tasks
}

// loadInitialFeed opens the timeline: the first time a site filter shows
// in a session, where it was read last (DEC-053 ①), otherwise or without a
// position at the newest cached post.
func (a *App) loadInitialFeed() {
	a.FeedSortOrder = "newest"
	a.loadHomeBaseline()
	a.homeRestorePhase = homeRestoreNone
	if a.HomeActorFilter == nil {
		site := normalizeSiteFilter(a.FeedSiteFilter)
		if !a.homePosRestored[site] {
			if a.homePosRestored == nil {
				a.homePosRestored = make(map[string]bool)
			}
			a.homePosRestored[site] = true
			if a.restoreHomePosition() {
				return
			}
		}
	}
	req := library.FeedPageRequest{
		Order:      "newest",
		CachedOnly: true,
		Site:       feedSiteParam(a.FeedSiteFilter),
		Authors:    a.FeedAuthorsFilter,
		AuthorRefs: a.FeedAuthorRefsFilter,
		Limit:      60,
	}
	if page, err := a.backend.FeedPage(req); err == nil {
		a.FeedError = ""
		a.FeedItems = page.Items
		a.feedMoreBefore, a.feedMoreAfter = false, page.HasMore
	} else {
		a.FeedError = userErrorText(err)
		a.showError(err)
	}
}

func (a *App) syncSelectedSubRules(sub subscription.Subscription) {
	a.SelectedSubID = sub.ID
	a.SubName = sub.Name
	a.SubNote = sub.Note
	a.SubRules = sub.Rules
	if len(a.SubRules.AutoCache.Types) == 0 {
		a.SubRules.AutoCache.Types = []string{"video", "draw"}
	}
}

func (a *App) selectLibraryFilter() {
	if a.CurrentRoute == RouteLibraryItem {
		a.CurrentRoute = RouteLibrary
		a.CurrentLibraryDetail = nil
	}
	a.clearLibrarySelection()
	a.reloadLibraryItems()
}

func (a *App) reloadLibraryItems() {
	a.libraryItemsGen++
	a.libraryOffset = 0
	a.LibraryListState.ScrollTo(0, ui.Start)
	a.loadLibraryPage(true)
}

// reloadLibraryItemsInPlace refreshes visible library items on background library
// changes without violently resetting the user's scroll position to the top.
func (a *App) reloadLibraryItemsInPlace() {
	a.libraryItemsGen++
	limit := a.libraryInPlaceLimit()
	items, err := a.backend.ListLibraryItems(a.currentLibraryFeedFilter(limit, 0))
	if err != nil {
		a.LibraryError = userErrorText(err)
		return
	}
	a.LibraryError = ""
	a.LibraryItems = items
	a.libraryOffset = len(items)
	a.LibraryHasMore = len(items) == limit
}

// libraryAuthorFilter is the authors of the media library's subscription
// or author filter.
func (a *App) libraryAuthorFilter() (authors []int64, authorRefs []library.AuthorRef) {
	if a.LibrarySubFilter != "" {
		for _, sub := range a.Subscriptions {
			if sub.ID == a.LibrarySubFilter {
				for _, acc := range sub.Accounts {
					accSite := firstNonEmptyStrUI(acc.Site, string(site.Bilibili))
					if acc.UID != "" {
						authorRefs = append(authorRefs, library.AuthorRef{Site: accSite, UID: acc.UID})
					}
					if accSite == string(site.Bilibili) {
						var id int64
						_, _ = fmt.Sscanf(acc.UID, "%d", &id)
						if id > 0 {
							authors = append(authors, id)
						}
					}
				}
				break
			}
		}
	}
	if a.LibraryAuthorKey != "" {
		if siteID, uid, err := site.ParseAccountKey(a.LibraryAuthorKey); err == nil {
			authorRefs = append(authorRefs, library.AuthorRef{Site: string(siteID), UID: uid})
		}
	}
	return authors, authorRefs
}

func (a *App) loadLibraryPage(reset bool) {
	a.libraryItemsGen++
	items, err := a.backend.ListLibraryItems(a.currentLibraryFeedFilter(60, a.libraryOffset))
	if err != nil {
		a.LibraryError = userErrorText(err)
		return
	}
	a.LibraryError = ""
	if reset {
		a.LibraryItems = nil
	}
	a.LibraryItems = append(a.LibraryItems, items...)
	a.libraryOffset += len(items)
	a.LibraryHasMore = len(items) == 60
}

// videoCacheContext is the creator page a video was opened from.
type videoCacheContext struct {
	BVID   string
	ForMid int64
}

// douyinCacheContext is the creator page a Douyin work was opened from (DEC-063).
type douyinCacheContext struct {
	AwemeID string
	ForUID  string
	ForName string
}

// weiboCacheContext is the creator page a Weibo post was opened from so caching
// a retweet or fast-retweet stores it under that creator's forwards/ folder.
type weiboCacheContext struct {
	Mid     string
	ForUID  string
	ForName string
}

// xCacheContext is the creator page an X post was opened from so caching a
// retweet stores it under that creator's forwards/ folder.
type xCacheContext struct {
	StatusID string
	ForUID   string
	ForName  string
}

// cacheForMid is the creator a video page caches bvid for (0: its uploader).
func (a *App) cacheForMid(bvid string) int64 {
	if a.videoCacheFor.BVID != "" && strings.EqualFold(a.videoCacheFor.BVID, strings.TrimSpace(bvid)) {
		return a.videoCacheFor.ForMid
	}
	return 0
}

// douyinWorkCacheFor is the creator a Douyin work page caches awemeID for (empty: its publisher).
func (a *App) douyinWorkCacheFor(awemeID string) (string, string) {
	if a.douyinCacheFor.AwemeID != "" && a.douyinCacheFor.AwemeID == strings.TrimSpace(awemeID) {
		return a.douyinCacheFor.ForUID, a.douyinCacheFor.ForName
	}
	return "", ""
}

// weiboCacheContextFor returns the active forwarder/profile context matching post.
func (a *App) weiboCacheContextFor(post *service.WeiboPostView) (string, string) {
	if post == nil {
		return "", ""
	}
	forUID := strings.TrimSpace(a.weiboCacheFor.ForUID)
	if forUID == "" {
		return "", ""
	}
	if ctxMid := strings.TrimSpace(a.weiboCacheFor.Mid); ctxMid != "" &&
		ctxMid != strings.TrimSpace(post.Mid) &&
		ctxMid != strings.TrimSpace(post.MblogID) {
		return "", ""
	}
	return forUID, strings.TrimSpace(a.weiboCacheFor.ForName)
}

// weiboCacheViaFor resolves the ItemVia forwarder attribution when caching post
// from the current detail page (using active weiboCacheFor or URL ForwarderHintUID).
func (a *App) weiboCacheViaFor(post *service.WeiboPostView) *library.ItemVia {
	forUID, forName := a.weiboCacheContextFor(post)
	return weiboCacheViaForContext(post, forUID, forName)
}

// weiboCacheViaForContext resolves ItemVia for caching a Weibo post either from
// an explicit creator profile (forUID/forName) or from a direct URL ForwarderHintUID.
func weiboCacheViaForContext(post *service.WeiboPostView, forUID, forName string) *library.ItemVia {
	if post == nil {
		return nil
	}
	forUID = strings.TrimSpace(forUID)
	forName = strings.TrimSpace(forName)
	authorUID := strings.TrimSpace(post.Author.UID)
	if forUID != "" && (post.IsRetweet || post.IsFastRetweet || post.Retweeted != nil || authorUID != forUID) {
		return &library.ItemVia{
			Reason:    library.ViaForward,
			Site:      string(site.Weibo),
			UID:       forUID,
			Name:      firstNonEmptyStrUI(forName, forUID),
			DynamicID: strings.TrimSpace(post.Mid),
		}
	}
	hintUID := strings.TrimSpace(post.ForwarderHintUID)
	if hintUID != "" && (post.IsFastRetweet || (authorUID != "" && authorUID != hintUID)) {
		return &library.ItemVia{
			Reason:    library.ViaForward,
			Site:      string(site.Weibo),
			UID:       hintUID,
			Name:      firstNonEmptyStrUI(post.ForwarderHintName, hintUID),
			DynamicID: strings.TrimSpace(post.Mid),
		}
	}
	return nil
}

// xCacheContextFor returns the active forwarder/profile context matching post.
func (a *App) xCacheContextFor(post *service.XPostDTO) (string, string) {
	if post == nil {
		return "", ""
	}
	forUID := strings.TrimSpace(a.xCacheFor.ForUID)
	if forUID == "" {
		return "", ""
	}
	if ctxID := strings.TrimSpace(a.xCacheFor.StatusID); ctxID != "" && ctxID != strings.TrimSpace(post.ID) {
		return "", ""
	}
	return forUID, strings.TrimSpace(a.xCacheFor.ForName)
}

// xCacheViaFor resolves the ItemVia forwarder attribution when caching post
// from the current detail page (using active xCacheFor or URL ForwarderHintUID).
func (a *App) xCacheViaFor(post *service.XPostDTO) *library.ItemVia {
	forUID, forName := a.xCacheContextFor(post)
	return xCacheViaForContext(post, forUID, forName)
}

// xCacheViaForContext resolves ItemVia for caching an X post either from an
// explicit creator profile (forUID/forName) or from a direct URL ForwarderHintUID.
func xCacheViaForContext(post *service.XPostDTO, forUID, forName string) *library.ItemVia {
	if post == nil {
		return nil
	}
	forUID = strings.TrimSpace(forUID)
	forName = strings.TrimSpace(forName)
	authorUID := strings.TrimSpace(post.Author.RestID)
	if forUID != "" && (post.IsRetweet || post.Retweeted != nil || (authorUID != "" && authorUID != forUID)) {
		return &library.ItemVia{
			Reason:    library.ViaForward,
			Site:      string(site.X),
			UID:       forUID,
			Name:      firstNonEmptyStrUI(forName, forUID),
			DynamicID: strings.TrimSpace(post.ID),
		}
	}
	hintUID := strings.TrimSpace(post.ForwarderHintUID)
	if hintUID != "" && (post.IsRetweet || (authorUID != "" && authorUID != hintUID)) {
		return &library.ItemVia{
			Reason:    library.ViaForward,
			Site:      string(site.X),
			UID:       hintUID,
			Name:      firstNonEmptyStrUI(post.ForwarderHintName, hintUID),
			DynamicID: strings.TrimSpace(post.ID),
		}
	}
	return nil
}

// OpeningPreview is what a clicked card already knows about the link it
// opens. Kind is "video", "note", "dynamic", "user" or "live".
type OpeningPreview struct {
	Site       string
	Kind       string
	Title      string
	AuthorName string
	AuthorUID  string
	Mid        int64
	CoverURL   string
	Duration   string
	Err        string

	raw string
	seq int
}

// SubmitParse resolves and opens a link, share text, or UID typed by the user.
func (a *App) SubmitParse(raw string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		a.setLastParseError("粘贴链接、分享文本或 UID")
		return
	}
	a.openLink(trimmed, nil)
}

// OpenVideo opens a video from a card: the video page shows at once.
func (a *App) OpenVideo(bvid, title, authorName string, mid int64, coverURL, duration string) {
	a.openVideoFor(bvid, title, authorName, mid, coverURL, duration, 0)
}

// openVideoFor opens a video from creator forMid's page: caching it there
// stores a collaboration video under forMid's folder, as caching from the
// page's list does (DEC-061).
func (a *App) openVideoFor(bvid, title, authorName string, mid int64, coverURL, duration string, forMid int64) {
	if strings.TrimSpace(bvid) == "" {
		return
	}
	a.videoCacheFor = videoCacheContext{BVID: strings.TrimSpace(bvid), ForMid: forMid}
	a.openLink(bvid, &OpeningPreview{Site: string(site.Bilibili), Kind: "video", Title: title, AuthorName: authorName, Mid: mid, CoverURL: coverURL, Duration: duration})
}

// OpenUser opens a creator's space from an avatar or name: the page shows at once.
func (a *App) OpenUser(mid int64, name string) {
	if mid <= 0 {
		return
	}
	a.openLink(fmt.Sprintf("UID:%d", mid), &OpeningPreview{Site: string(site.Bilibili), Kind: "user", AuthorName: name, Mid: mid})
}

// OpenDouyinWork opens a Douyin video or note work from a card or history item.
func (a *App) OpenDouyinWork(awemeID, kind, title, authorName, secUID, coverURL, duration string) {
	a.openDouyinWorkFor(awemeID, kind, title, authorName, secUID, coverURL, duration, "", "")
}

// openDouyinWorkFor opens a Douyin work from creator forUID's user page: caching
// it on the work detail page stores a co-creation under forUID's folder (DEC-063).
func (a *App) openDouyinWorkFor(awemeID, kind, title, authorName, secUID, coverURL, duration, forUID, forName string) {
	awemeID = strings.TrimSpace(awemeID)
	if awemeID == "" {
		return
	}
	a.douyinCacheFor = douyinCacheContext{
		AwemeID: awemeID,
		ForUID:  strings.TrimSpace(forUID),
		ForName: strings.TrimSpace(forName),
	}
	rawURL := "https://www.douyin.com/video/" + awemeID
	previewKind := "video"
	if kind == "note" || kind == "dynamic" {
		rawURL = "https://www.douyin.com/note/" + awemeID
		previewKind = "note"
	}
	a.openLink(rawURL, &OpeningPreview{
		Site:       string(site.Douyin),
		Kind:       previewKind,
		Title:      title,
		AuthorName: authorName,
		AuthorUID:  secUID,
		CoverURL:   coverURL,
		Duration:   duration,
	})
}

// OpenDouyinUser opens a Douyin creator's profile page by sec_uid or canonical URL.
func (a *App) OpenDouyinUser(secUID, name string) {
	secUID = strings.TrimSpace(secUID)
	if secUID == "" {
		return
	}
	raw := secUID
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "https://www.douyin.com/user/" + raw
	}
	a.openLink(raw, &OpeningPreview{
		Site:       string(site.Douyin),
		Kind:       "user",
		AuthorName: name,
		AuthorUID:  secUID,
	})
}

// OpenWeiboPost opens a single Weibo post detail page by mid or canonical URL.
// Direct opening clears any prior creator-page weiboCacheFor context.
func (a *App) OpenWeiboPost(mid, title, authorName, authorUID, coverURL, canonicalURL string) {
	a.openWeiboPostForPreview(mid, title, authorName, authorUID, coverURL, canonicalURL, "", "")
}

// OpenWeiboPostFor opens a single Weibo post from creator forUID's profile page
// so caching a retweet or fast-retweet on the detail page places it under forUID.
func (a *App) OpenWeiboPostFor(mid, canonicalURL, title, forUID, forName string) {
	a.openWeiboPostForPreview(mid, title, forName, forUID, "", canonicalURL, forUID, forName)
}

func (a *App) openWeiboPostForPreview(mid, title, authorName, authorUID, coverURL, canonicalURL, forUID, forName string) {
	mid = strings.TrimSpace(mid)
	forUID = strings.TrimSpace(forUID)
	forName = strings.TrimSpace(forName)
	raw := strings.TrimSpace(canonicalURL)
	if raw == "" {
		if mid == "" {
			return
		}
		owner := firstNonEmptyStrUI(forUID, authorUID)
		if owner != "" {
			raw = fmt.Sprintf("https://weibo.com/%s/%s", strings.TrimSpace(owner), mid)
		} else {
			raw = "https://weibo.com/detail/" + mid
		}
	}
	if forUID != "" {
		a.weiboCacheFor = weiboCacheContext{Mid: mid, ForUID: forUID, ForName: forName}
	} else {
		a.weiboCacheFor = weiboCacheContext{}
	}
	a.openLinkCore(raw, &OpeningPreview{
		Site:       string(site.Weibo),
		Kind:       "dynamic",
		Title:      title,
		AuthorName: authorName,
		AuthorUID:  authorUID,
		CoverURL:   coverURL,
	})
}

// OpenWeiboVideo opens a Weibo video page by oid or canonical URL.
func (a *App) OpenWeiboVideo(oid, title, authorName, authorUID, coverURL, duration string) {
	oid = strings.TrimSpace(oid)
	if oid == "" {
		return
	}
	raw := oid
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "https://weibo.com/tv/show/" + oid
	}
	a.openLink(raw, &OpeningPreview{
		Site:       string(site.Weibo),
		Kind:       "video",
		Title:      title,
		AuthorName: authorName,
		AuthorUID:  authorUID,
		CoverURL:   coverURL,
		Duration:   duration,
	})
}

// OpenWeiboUser opens a Weibo user's profile page by numeric UID or canonical URL.
func (a *App) OpenWeiboUser(uid, name string) {
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return
	}
	raw := uid
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "https://weibo.com/u/" + uid
	}
	a.openLink(raw, &OpeningPreview{
		Site:       string(site.Weibo),
		Kind:       "user",
		AuthorName: name,
		AuthorUID:  uid,
	})
}

// OpenXPost opens a single X post detail page by status ID or canonical URL.
func (a *App) OpenXPost(statusID, title, authorName, authorUID, coverURL, canonicalURL string) {
	a.openXPostForPreview(statusID, title, authorName, authorUID, coverURL, canonicalURL, "", "")
}

// OpenXPostFor opens an X post from creator forUID's profile page so caching a
// retweet on the detail page attributes it under forUID.
func (a *App) OpenXPostFor(statusID, canonicalURL, title, forUID, forName string) {
	a.openXPostForPreview(statusID, title, forName, forUID, "", canonicalURL, forUID, forName)
}

func (a *App) openXPostForPreview(statusID, title, authorName, authorUID, coverURL, canonicalURL, forUID, forName string) {
	statusID = strings.TrimSpace(statusID)
	forUID = strings.TrimSpace(forUID)
	forName = strings.TrimSpace(forName)
	raw := strings.TrimSpace(canonicalURL)
	if raw == "" {
		if statusID == "" {
			return
		}
		raw = "https://x.com/i/web/status/" + statusID
	}
	if forUID != "" {
		a.xCacheFor = xCacheContext{StatusID: statusID, ForUID: forUID, ForName: forName}
	} else {
		a.xCacheFor = xCacheContext{}
	}
	a.openLinkCore(raw, &OpeningPreview{
		Site:       string(site.X),
		Kind:       "dynamic",
		Title:      title,
		AuthorName: authorName,
		AuthorUID:  authorUID,
		CoverURL:   coverURL,
	})
}

func isAllDigitsUI(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// OpenXUser opens an X user's profile page by numeric rest_id, @screen_name, or URL.
func (a *App) OpenXUser(uidOrHandle, name string) {
	raw := strings.TrimSpace(uidOrHandle)
	if raw == "" {
		return
	}
	authorUID := raw
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		if isAllDigitsUI(raw) {
			raw = "https://x.com/i/user/" + raw
		} else {
			handle := strings.TrimPrefix(raw, "@")
			raw = "https://x.com/" + handle
		}
	}
	a.openLink(raw, &OpeningPreview{
		Site:       string(site.X),
		Kind:       "user",
		AuthorName: name,
		AuthorUID:  authorUID,
	})
}

// RetryOpening opens the link of a failed preview again.
func (a *App) RetryOpening() {
	if a.Opening == nil {
		return
	}
	p := *a.Opening
	p.Err = ""
	a.openLinkCore(p.raw, &p)
}

func (a *App) openLink(raw string, preview *OpeningPreview) {
	a.weiboCacheFor = weiboCacheContext{}
	a.xCacheFor = xCacheContext{}
	a.openLinkCore(raw, preview)
}

func (a *App) openLinkCore(raw string, preview *OpeningPreview) {
	a.setLastParseError("")
	a.openSeq++
	seq := a.openSeq
	if preview != nil {
		p := *preview
		p.raw, p.seq, p.Err = raw, seq, ""
		route := RouteVideo
		switch p.Site {
		case string(site.Douyin):
			route = RouteDouyinWork
			if p.Kind == "user" || p.Kind == "live" {
				route = RouteDouyinUser
			}
		case string(site.Weibo):
			route = RouteWeiboPost
			if p.Kind == "user" {
				route = RouteWeiboUser
			}
		case string(site.X):
			route = RouteXPost
			if p.Kind == "user" {
				route = RouteXUser
			} else if p.Kind == "x_list" || p.Kind == "list" {
				route = RouteXList
			}
		default:
			if p.Kind == "user" || p.Kind == "live" {
				route = RouteUser
			}
		}
		if a.CurrentRoute == RouteHome && route != RouteHome {
			a.flushHomePosition()
		}
		if route != RouteTasks {
			a.closeLivePreview()
		}
		if a.CurrentRoute != route {
			a.pushPreviousRoute(a.currentReturnRoute())
		}
		a.CurrentRoute = route
		a.CurrentLibraryDetail = nil
		a.Opening = &p
		a.PostMenuKey = ""
		a.DouyinCardMenuID = ""
		a.WeiboUserSubMenuOpen = false
		a.XUserSubMenuOpen = false
		a.HistoryHeaderMenuOpen = false
		a.HistoryClearConfirmOpen = false
		a.HistoryItemMenuID = ""
	} else {
		a.Opening = nil
	}

	backend := a.backend
	current := func() bool {
		if a.openSeq != seq || !backend.HasAnyLogin() {
			return false
		}
		// A preview the user left (Navigate clears it) no longer wants its page.
		return preview == nil || (a.Opening != nil && a.Opening.seq == seq)
	}
	requestGuarded(a, fmt.Sprintf("parse-%d", seq), current, func(ctx context.Context) (*service.OpenLinkResult, error) {
		return backend.OpenLink(ctx, raw)
	}, func(res *service.OpenLinkResult) {
		a.Opening = nil
		a.applyParseResult(res)
		a.afterOpenLink(res)
	}, func(err error) {
		msg := userErrorText(err)
		if preview != nil && a.Opening != nil {
			a.Opening.Err = msg
			return
		}
		a.setLastParseError(msg)
		a.showError(err)
	})
}

// parseBusy reports whether the newest open is still waiting for OpenLink.
func (a *App) parseBusy() bool {
	return a.isPending(fmt.Sprintf("parse-%d", a.openSeq))
}

// afterOpenLink runs the optional work OpenLink no longer waits for: the
// recent-parse thumbnails and the upgrade check of a cached video.
func (a *App) afterOpenLink(res *service.OpenLinkResult) {
	if res == nil {
		return
	}
	backend := a.backend
	if job := res.HistoryThumbs; job != nil {
		j := *job
		jobSite := firstNonEmptyStrUI(j.Site, string(site.Bilibili))
		requestGuarded(a, "history-thumbs-"+jobSite+":"+j.Kind+":"+j.ID, nil, func(ctx context.Context) (struct{}, error) {
			return struct{}{}, backend.FetchHistoryThumbs(ctx, j)
		}, func(struct{}) {
			a.purgeMissingImages()
			a.HistoryItems = backend.ListHistory()
		}, func(err error) {
			a.HistoryItems = backend.ListHistory()
		})
	}
	if res.Video != nil && res.CachedItem != nil && backend.GetSettings().UpgradeCheckOnOpen {
		bvid := res.Video.BVID
		requestGuarded(a, "open-upgrade-"+bvid, func() bool {
			return a.CurrentRoute == RouteVideo && a.CurrentVideo != nil && a.CurrentVideo.BVID == bvid
		}, func(ctx context.Context) ([]library.ItemMetadata, error) {
			return backend.CheckUpgradeKeys(ctx, []string{bvid}, false)
		}, func(items []library.ItemMetadata) {
			for _, it := range items {
				if it.RawID == bvid || it.ID == bvid {
					a.UpgradeAvailable = it.UpgradeAvailable
				}
			}
		}, func(error) {})
	}
}

func (a *App) applyParseResult(res *service.OpenLinkResult) {
	if res == nil || !a.backend.HasAnyLogin() {
		return
	}
	a.HistoryItems = a.backend.ListHistory()
	if res.Site == string(site.Douyin) || res.DouyinWork != nil || res.DouyinUser != nil || res.DouyinLive != nil {
		a.applyDouyinParseResult(res)
		return
	}
	if res.Site == string(site.Weibo) || res.WeiboPost != nil || res.WeiboVideo != nil || res.WeiboUser != nil {
		a.applyWeiboParseResult(res)
		return
	}
	if res.Site == string(site.X) || res.XPost != nil || res.XUser != nil || res.XUserPosts != nil || res.XList != nil {
		a.applyXParseResult(res)
		return
	}
	switch res.Target.Kind {
	case "video":
		a.CurrentLibraryDetail = nil
		a.CurrentVideo = res.Video
		a.CurrentVideoItemKey = ""
		a.CurrentDynamic = nil
		a.UpgradeAvailable = res.CachedItem != nil && res.CachedItem.UpgradeAvailable
		a.SelectedPartPages = make(map[int]bool)
		if res.Video != nil {
			for _, p := range res.Video.Pages {
				a.SelectedPartPages[p.Page] = true
			}
		}
		if a.CurrentRoute != RouteVideo {
			a.pushPreviousRoute(a.currentReturnRoute())
		}
		a.CurrentRoute = RouteVideo
	case "user", "live":
		a.CurrentUser = res.User
		a.UserTab = "dynamics"
		a.UserDynListState, a.UserVidListState = &ui.ListState{}, &ui.ListState{}
		a.UserDynamics = nil
		a.UserVideos = nil
		a.UserVideosPage = 0
		a.UserVideosOrder = "pubdate"
		a.UserVideosKeywordInput = ""
		a.UserVideosKeywordComposing = false
		a.UserVideosAppliedKeyword = ""
		a.UserVideosTID = 0
		a.UserVideosZones = nil
		a.userVideosSeq++
		a.UserDynamicsNext = ""
		a.UserDynamicsHasMore = false
		a.UserVideosHasMore = false
		a.UserDynamicsError = res.UserDynamicsError
		a.UserVideosError = res.UserVideosError
		if res.UserDynamics != nil {
			a.UserDynamics = res.UserDynamics.Items
			a.UserDynamicsNext = res.UserDynamics.Offset
			a.UserDynamicsHasMore = res.UserDynamics.HasMore
		}
		if res.UserVideos != nil {
			a.UserVideos = res.UserVideos.Items
			a.UserVideosZones = res.UserVideos.Zones
			a.UserVideosPage = 1
			a.UserVideosHasMore = len(res.UserVideos.Items) < res.UserVideos.Total
			a.UserVideosError = ""
		}
		if a.CurrentRoute != RouteUser {
			a.pushPreviousRoute(a.currentReturnRoute())
		}
		a.CurrentRoute = RouteUser
		if res.LiveRoom != nil {
			a.LiveRooms = bilibiliLiveRooms(a.backend.ListLiveRooms())
		} else if res.User != nil {
			mid := res.User.Mid
			backend := a.backend
			requestGuarded(a, fmt.Sprintf("user-live-%d", mid), func() bool {
				return a.CurrentRoute == RouteUser && a.CurrentUser != nil && a.CurrentUser.Mid == mid && backend.StatusOf(string(site.Bilibili)).LoggedIn
			}, func(ctx context.Context) ([]live.RoomState, error) {
				return backend.LiveStatus(ctx, []int64{mid})
			}, a.mergeLiveRooms, a.handleVisibleLiveError)
		}
	case "dynamic":
		a.CurrentLibraryDetail = nil
		a.CurrentVideoItemKey = ""
		a.CurrentDynamic = res.Dynamic
		a.CurrentVideo = nil
		if a.CurrentRoute != RouteVideo {
			a.pushPreviousRoute(a.currentReturnRoute())
		}
		a.CurrentRoute = RouteVideo
	}
}

func (a *App) applyDouyinParseResult(res *service.OpenLinkResult) {
	kind := res.Target.Kind
	if res.SiteTarget != nil && res.SiteTarget.Kind != "" {
		kind = res.SiteTarget.Kind
	}
	switch kind {
	case "video", "note":
		a.CurrentLibraryDetail = nil
		a.CurrentDouyinWork = res.DouyinWork
		if a.CurrentRoute != RouteDouyinWork {
			a.pushPreviousRoute(a.currentReturnRoute())
		}
		a.CurrentRoute = RouteDouyinWork
	case "user", "live":
		uv := res.DouyinUser
		if uv == nil && res.DouyinLive != nil {
			lv := res.DouyinLive
			uv = &service.DouyinUserView{
				SecUID:       lv.OwnerSecUID,
				UID:          lv.OwnerUID,
				Nickname:     lv.OwnerName,
				AvatarURL:    lv.OwnerAvatar,
				LiveStatus:   lv.LiveStatus,
				RoomID:       lv.RoomID,
				WebRID:       lv.WebRID,
				CanonicalURL: lv.CanonicalURL,
				LiveRoom:     lv.State,
			}
		}
		a.CurrentDouyinUser = uv
		a.DouyinUserListState = &ui.ListState{}
		a.DouyinCardMenuID = ""
		a.DouyinUserSubMenuOpen = false
		a.CurrentDouyinWorks = nil
		a.DouyinWorksNextCursor = ""
		a.DouyinWorksHasMore = false
		a.DouyinWorksError = res.DouyinUserWorksError
		a.DouyinWorksAuthExpired = false
		if res.DouyinUserWorks != nil {
			a.CurrentDouyinWorks = res.DouyinUserWorks.Works
			a.DouyinWorksNextCursor = res.DouyinUserWorks.NextCursor
			a.DouyinWorksHasMore = res.DouyinUserWorks.HasMore
			if res.DouyinUserWorks.UncertainEmpty {
				a.DouyinWorksAuthExpired = false
				a.DouyinWorksHasMore = false
				a.DouyinWorksError = firstNonEmptyStrUI(res.DouyinUserWorks.UncertainReason, "首屏无可浏览公开作品（可能为仅好友可见或受限），可稍后重试")
			}
		}
		if uv != nil && uv.LiveRoom != nil {
			a.mergeLiveAccountStates([]service.LiveAccountState{*uv.LiveRoom})
		}
		if a.CurrentRoute != RouteDouyinUser {
			a.pushPreviousRoute(a.currentReturnRoute())
		}
		a.CurrentRoute = RouteDouyinUser
	}
}

// LoadMoreDouyinWorks fetches the next page of a Douyin creator's works grid via max_cursor,
// deduplicating across pages and preserving already loaded works on risk-control or session-expiry errors.
func (a *App) LoadMoreDouyinWorks() {
	if a.CurrentDouyinUser == nil || a.CurrentDouyinUser.SecUID == "" {
		return
	}
	secUID, cursor := a.CurrentDouyinUser.SecUID, a.DouyinWorksNextCursor
	requestGuarded(a, "douyin-works-"+secUID, func() bool {
		return a.CurrentDouyinUser != nil && a.CurrentDouyinUser.SecUID == secUID
	}, func(ctx context.Context) (*service.DouyinUserWorksPage, error) {
		return a.backend.DouyinUserWorks(ctx, secUID, cursor)
	}, func(page *service.DouyinUserWorksPage) {
		if a.CurrentDouyinUser == nil || a.CurrentDouyinUser.SecUID != secUID || page == nil {
			return
		}
		if page.UncertainEmpty {
			a.DouyinWorksHasMore = false
			if cursor == "" {
				a.DouyinWorksAuthExpired = false
				a.DouyinWorksError = firstNonEmptyStrUI(page.UncertainReason, "首屏无可浏览公开作品（可能为仅好友可见或受限），可稍后重试")
			} else {
				a.DouyinWorksAuthExpired = true
				a.DouyinWorksError = ""
			}
			return
		}
		seen := make(map[string]bool, len(a.CurrentDouyinWorks))
		for _, w := range a.CurrentDouyinWorks {
			seen[w.ID] = true
		}
		for _, w := range page.Works {
			if w.ID != "" && !seen[w.ID] {
				a.CurrentDouyinWorks = append(a.CurrentDouyinWorks, w)
				seen[w.ID] = true
			}
		}
		// A private account whose first page failed and now shows works
		// is visible to the signed-in account.
		if a.CurrentDouyinUser.IsPrivate && len(page.Works) > 0 {
			u := *a.CurrentDouyinUser
			u.IsPrivate, u.PrivateVisible = false, true
			a.CurrentDouyinUser = &u
		}
		a.DouyinWorksAuthExpired = false
		a.DouyinWorksError = ""
		a.DouyinWorksNextCursor = page.NextCursor
		a.DouyinWorksHasMore = page.HasMore
	}, func(err error) {
		if site.IsKind(err, site.ErrKindAuthRequired) {
			a.DouyinWorksAuthExpired = true
			a.DouyinWorksHasMore = false
			a.DouyinWorksError = ""
			return
		}
		a.DouyinWorksError = userErrorText(err)
	})
}

func (a *App) applyWeiboParseResult(res *service.OpenLinkResult) {
	kind := res.Target.Kind
	if res.SiteTarget != nil && res.SiteTarget.Kind != "" {
		kind = res.SiteTarget.Kind
	}
	if kind == "user" || (res.WeiboUser != nil && res.WeiboPost == nil && res.WeiboVideo == nil) {
		a.CurrentWeiboUser = res.WeiboUser
		a.WeiboUserListState = &ui.ListState{}
		a.WeiboUserSubMenuOpen = false
		a.CurrentWeiboPosts = nil
		a.WeiboPostsNextCursor = ""
		a.WeiboPostsHasMore = false
		a.WeiboPostsError = res.WeiboUserPostsError
		a.WeiboPostsAuthExpired = false
		a.WeiboPostsUncertainEmpty = false
		a.WeiboBottomTipsVisible = false
		a.WeiboBottomTipsText = ""
		if res.WeiboUserPosts != nil {
			a.CurrentWeiboPosts = res.WeiboUserPosts.Posts
			a.WeiboPostsNextCursor = res.WeiboUserPosts.NextCursor
			a.WeiboPostsHasMore = res.WeiboUserPosts.HasMore
			a.WeiboBottomTipsVisible = res.WeiboUserPosts.BottomTipsVisible
			a.WeiboBottomTipsText = res.WeiboUserPosts.BottomTipsText
			if res.WeiboUserPosts.UncertainEmpty {
				a.WeiboPostsUncertainEmpty = true
				a.WeiboPostsHasMore = false
				a.WeiboPostsError = firstNonEmptyStrUI(res.WeiboUserPosts.UncertainReason, "第 1 页暂无可见微博（可能为全私密、超出可见时间窗或账号受限），可稍后重试")
			}
		}
		if a.CurrentRoute != RouteWeiboUser {
			a.pushPreviousRoute(a.currentReturnRoute())
		}
		a.CurrentRoute = RouteWeiboUser
		return
	}
	a.CurrentLibraryDetail = nil
	a.CurrentWeiboPost = res.WeiboPost
	a.CurrentWeiboVideo = res.WeiboVideo
	newMid := ""
	if res.WeiboPost != nil {
		newMid = strings.TrimSpace(res.WeiboPost.Mid)
	}
	if newMid == "" || newMid != a.WeiboEditHistoryMid {
		a.WeiboEditHistoryExpanded = false
		a.WeiboEditHistoryMid = ""
		a.WeiboEditHistoryError = ""
		a.WeiboEditHistoryItems = nil
	}
	if a.CurrentRoute != RouteWeiboPost {
		a.pushPreviousRoute(a.currentReturnRoute())
	}
	a.CurrentRoute = RouteWeiboPost
}

// LoadMoreWeiboPosts fetches the next page of a Weibo creator's timeline while
// preserving already loaded posts on risk-control, empty-shell, or auth errors.
func (a *App) LoadMoreWeiboPosts() {
	if a.CurrentWeiboUser == nil || a.CurrentWeiboUser.UID == "" {
		return
	}
	uid, cursor := a.CurrentWeiboUser.UID, a.WeiboPostsNextCursor
	requestGuarded(a, "weibo-posts-"+uid, func() bool {
		return a.CurrentWeiboUser != nil && a.CurrentWeiboUser.UID == uid
	}, func(ctx context.Context) (*service.WeiboUserPostsPage, error) {
		return a.backend.WeiboUserPosts(ctx, uid, cursor)
	}, func(page *service.WeiboUserPostsPage) {
		if a.CurrentWeiboUser == nil || a.CurrentWeiboUser.UID != uid || page == nil {
			return
		}
		if page.UncertainEmpty {
			a.WeiboPostsUncertainEmpty = true
			a.WeiboPostsHasMore = false
			a.WeiboPostsError = firstNonEmptyStrUI(page.UncertainReason, "第 1 页暂无可见微博（可能为全私密、超出可见时间窗或账号受限），可稍后重试")
			return
		}
		seen := make(map[string]bool, len(a.CurrentWeiboPosts))
		for _, p := range a.CurrentWeiboPosts {
			seen[p.Mid] = true
		}
		for _, p := range page.Posts {
			if p.Mid != "" && !seen[p.Mid] {
				a.CurrentWeiboPosts = append(a.CurrentWeiboPosts, p)
				seen[p.Mid] = true
			}
		}
		a.WeiboPostsAuthExpired = false
		a.WeiboPostsUncertainEmpty = false
		a.WeiboPostsError = ""
		a.WeiboPostsNextCursor = page.NextCursor
		a.WeiboPostsHasMore = page.HasMore
		if page.BottomTipsVisible {
			a.WeiboBottomTipsVisible = true
			if page.BottomTipsText != "" {
				a.WeiboBottomTipsText = page.BottomTipsText
			}
		}
	}, func(err error) {
		if site.IsKind(err, site.ErrKindAuthRequired) {
			a.WeiboPostsAuthExpired = true
			a.WeiboPostsHasMore = false
			a.WeiboPostsError = ""
			return
		}
		a.WeiboPostsError = userErrorText(err)
	})
}

func normalizeXUserTabUI(tab string) string {
	switch strings.ToLower(strings.TrimSpace(tab)) {
	case "replies", "with_replies":
		return "replies"
	case "media":
		return "media"
	case "highlights":
		return "highlights"
	case "articles":
		return "articles"
	default:
		return "posts"
	}
}

func (a *App) activeXUserTab() string {
	return normalizeXUserTabUI(a.XUserTab)
}

func (a *App) xPostsPendingKey(uid, tab string) string {
	return "x-posts-" + strings.TrimSpace(uid) + "-" + normalizeXUserTabUI(tab)
}

func (a *App) ensureXUserTabsFor(uid string) {
	uid = strings.TrimSpace(uid)
	if a.XUserTabs == nil || a.xUserTabsOwner != uid {
		a.XUserTabs = make(map[string]*XUserTabState, 4)
		a.xUserTabsOwner = uid
	}
}

func (a *App) getOrCreateXUserTabState(uid, tab string) *XUserTabState {
	a.ensureXUserTabsFor(uid)
	tab = normalizeXUserTabUI(tab)
	st := a.XUserTabs[tab]
	if st == nil {
		st = &XUserTabState{ListState: &ui.ListState{}}
		a.XUserTabs[tab] = st
	}
	if st.ListState == nil {
		st.ListState = &ui.ListState{}
	}
	return st
}

func (a *App) syncActiveFieldsToXUserTab(uid, tab string) *XUserTabState {
	st := a.getOrCreateXUserTabState(uid, tab)
	st.Posts = a.CurrentXPosts
	st.NextCursor = a.XPostsNextCursor
	st.HasMore = a.XPostsHasMore
	st.Error = a.XPostsError
	st.AuthExpired = a.XPostsAuthExpired
	st.UncertainEmpty = a.XPostsUncertainEmpty
	st.BottomTips = a.XBottomTipsVisible
	st.BottomTipsText = a.XBottomTipsText
	if a.XUserListState != nil {
		st.ListState = a.XUserListState
	}
	if len(st.Posts) > 0 || st.Error != "" || st.AuthExpired || st.UncertainEmpty || st.BottomTips {
		st.Loaded = true
	}
	return st
}

func (a *App) applyXUserTabToActiveFields(st *XUserTabState) {
	if st == nil {
		a.CurrentXPosts = nil
		a.XPostsNextCursor = ""
		a.XPostsHasMore = false
		a.XPostsError = ""
		a.XPostsAuthExpired = false
		a.XPostsUncertainEmpty = false
		a.XBottomTipsVisible = false
		a.XBottomTipsText = ""
		a.XUserListState = &ui.ListState{}
		return
	}
	if st.ListState == nil {
		st.ListState = &ui.ListState{}
	}
	a.CurrentXPosts = st.Posts
	a.XPostsNextCursor = st.NextCursor
	a.XPostsHasMore = st.HasMore
	a.XPostsError = st.Error
	a.XPostsAuthExpired = st.AuthExpired
	a.XPostsUncertainEmpty = st.UncertainEmpty
	a.XBottomTipsVisible = st.BottomTips
	a.XBottomTipsText = st.BottomTipsText
	a.XUserListState = st.ListState
}

// SelectXUserTab switches the active X user profile tab ("posts" | "replies" | "media" | "highlights"),
// preserving already-loaded pages per tab and lazily loading the newly selected tab on demand (DEC-092).
func (a *App) SelectXUserTab(tab string) {
	nextTab := normalizeXUserTabUI(tab)
	if a.CurrentXUser == nil || a.CurrentXUser.RestID == "" {
		a.XUserTab = nextTab
		return
	}
	uid := a.CurrentXUser.RestID
	curTab := a.activeXUserTab()
	a.syncActiveFieldsToXUserTab(uid, curTab)
	a.XUserTab = nextTab
	a.XUserSubMenuOpen = false
	nextState := a.getOrCreateXUserTabState(uid, nextTab)
	a.applyXUserTabToActiveFields(nextState)
	if !nextState.Loaded && len(nextState.Posts) == 0 && nextState.Error == "" && !nextState.AuthExpired && !nextState.UncertainEmpty {
		a.LoadMoreXPosts()
	}
}

func (a *App) applyXParseResult(res *service.OpenLinkResult) {
	kind := res.Target.Kind
	if res.SiteTarget != nil && res.SiteTarget.Kind != "" {
		kind = res.SiteTarget.Kind
	}
	if kind == "x_list" || res.XList != nil {
		a.applyXListParseResult(res)
		return
	}
	if kind == "user" || (res.XUser != nil && res.XPost == nil) {
		initialTab := res.XUserTab
		if initialTab == "" && res.SiteTarget != nil {
			initialTab = res.SiteTarget.Tab
		}
		initialTab = normalizeXUserTabUI(initialTab)

		a.CurrentXUser = res.XUser
		a.XUserTab = initialTab
		a.XUserTabs = make(map[string]*XUserTabState, 4)
		if res.XUser != nil {
			a.xUserTabsOwner = strings.TrimSpace(res.XUser.RestID)
		} else {
			a.xUserTabsOwner = ""
		}
		a.XUserListState = &ui.ListState{}
		a.XUserSubMenuOpen = false
		a.CurrentXPosts = nil
		a.XPostsNextCursor = ""
		a.XPostsHasMore = false
		a.XPostsError = res.XUserPostsError
		a.XPostsAuthExpired = false
		a.XPostsUncertainEmpty = false
		a.XBottomTipsVisible = false
		a.XBottomTipsText = ""
		if res.XUserPosts != nil {
			a.CurrentXPosts = res.XUserPosts.Posts
			a.XPostsNextCursor = res.XUserPosts.NextCursor
			a.XPostsHasMore = res.XUserPosts.HasMore
			if res.XUserPosts.ReachedVisibleLimit || res.XUserPosts.BottomTipsVisible {
				a.XBottomTipsVisible = true
				a.XBottomTipsText = firstNonEmptyStrUI(res.XUserPosts.BottomTipsText, "已到达 X 时间线可见上限")
			}
			if res.XUserPosts.UncertainEmpty {
				a.XPostsUncertainEmpty = true
				a.XPostsHasMore = false
				a.XPostsError = firstNonEmptyStrUI(res.XUserPosts.UncertainReason, "X 返回了异常空列表，可能是限流、风控或受限，可稍后重试")
			}
		}
		if a.xUserTabsOwner != "" {
			st := a.syncActiveFieldsToXUserTab(a.xUserTabsOwner, initialTab)
			st.Loaded = res.XUserPosts != nil || res.XUserPostsError != ""
		}
		if a.CurrentRoute != RouteXUser {
			a.pushPreviousRoute(a.currentReturnRoute())
		}
		a.CurrentRoute = RouteXUser
		if res.XUserLists != nil || res.XUserListsError != "" || (res.SiteTarget != nil && strings.EqualFold(strings.TrimSpace(res.SiteTarget.Tab), "lists")) {
			uid := ""
			name := ""
			if res.XUser != nil {
				uid = res.XUser.RestID
				name = firstNonEmptyStrUI(res.XUser.Name, res.XUser.ScreenName, res.XUser.RestID)
			}
			var lists []service.XListDTO
			if res.XUserLists != nil {
				lists = res.XUserLists.Lists
			}
			a.XUserLists = XUserListsModalState{
				Open:       true,
				UserRestID: uid,
				UserName:   name,
				Lists:      lists,
				Error:      res.XUserListsError,
			}
		}
		return
	}
	a.CurrentLibraryDetail = nil
	a.CurrentXPost = res.XPost
	if a.CurrentRoute != RouteXPost {
		a.pushPreviousRoute(a.currentReturnRoute())
	}
	a.CurrentRoute = RouteXPost
}

// LoadMoreXPosts fetches the next page of the active X creator profile tab while
// preserving already loaded posts on rate-limit, risk-control, fake-shell, or auth errors.
func (a *App) LoadMoreXPosts() {
	if a.CurrentXUser == nil || a.CurrentXUser.RestID == "" {
		return
	}
	uid := a.CurrentXUser.RestID
	tab := a.activeXUserTab()
	st := a.syncActiveFieldsToXUserTab(uid, tab)
	cursor := st.NextCursor
	pendingKey := a.xPostsPendingKey(uid, tab)

	requestGuarded(a, pendingKey, func() bool {
		return a.CurrentXUser != nil && a.CurrentXUser.RestID == uid
	}, func(ctx context.Context) (*service.XUserPostsPageDTO, error) {
		return a.backend.XUserTabPosts(ctx, uid, tab, cursor)
	}, func(page *service.XUserPostsPageDTO) {
		if a.CurrentXUser == nil || a.CurrentXUser.RestID != uid || page == nil {
			return
		}
		target := a.getOrCreateXUserTabState(uid, tab)
		if a.activeXUserTab() == tab {
			target = a.syncActiveFieldsToXUserTab(uid, tab)
		}
		target.Loaded = true
		if page.UncertainEmpty {
			target.UncertainEmpty = true
			target.HasMore = false
			target.Error = firstNonEmptyStrUI(page.UncertainReason, "X 返回了异常空列表，可能是限流、风控或受限，可稍后重试")
			if a.activeXUserTab() == tab {
				a.applyXUserTabToActiveFields(target)
			}
			return
		}
		seen := make(map[string]bool, len(target.Posts))
		for _, p := range target.Posts {
			seen[p.ID] = true
		}
		for _, p := range page.Posts {
			if p.ID != "" && !seen[p.ID] {
				target.Posts = append(target.Posts, p)
				seen[p.ID] = true
			}
		}
		target.AuthExpired = false
		target.UncertainEmpty = false
		target.Error = ""
		target.NextCursor = page.NextCursor
		target.HasMore = page.HasMore
		if page.ReachedVisibleLimit || page.BottomTipsVisible {
			target.BottomTips = true
			target.BottomTipsText = firstNonEmptyStrUI(page.BottomTipsText, "已到达 X 时间线可见上限")
		}
		if a.activeXUserTab() == tab {
			a.applyXUserTabToActiveFields(target)
		}
	}, func(err error) {
		if a.CurrentXUser == nil || a.CurrentXUser.RestID != uid {
			return
		}
		target := a.getOrCreateXUserTabState(uid, tab)
		if a.activeXUserTab() == tab {
			target = a.syncActiveFieldsToXUserTab(uid, tab)
		}
		target.Loaded = true
		if site.IsKind(err, site.ErrKindAuthRequired) {
			target.AuthExpired = true
			target.HasMore = false
			target.Error = ""
		} else {
			target.Error = userErrorText(err)
		}
		if a.activeXUserTab() == tab {
			a.applyXUserTabToActiveFields(target)
		}
	})
}

// CacheXCurrentTabUncached enqueues uncached, cacheable posts from the currently
// loaded X replies/media/highlights tab directly into download_tasks (DEC-092).
func (a *App) CacheXCurrentTabUncached() {
	if a.CurrentXUser == nil || a.CurrentXUser.RestID == "" {
		return
	}
	u := a.CurrentXUser
	tab := a.activeXUserTab()
	if tab == "posts" {
		a.enqueueCacheAll("x")
		return
	}
	if len(a.CurrentXPosts) == 0 {
		a.notifyInfo("当前页没有可缓存的未缓存条目")
		return
	}
	uid := u.RestID
	userName := firstNonEmptyStrUI(u.Name, u.ScreenName, u.RestID)
	postsCopy := append([]service.XPostDTO(nil), a.CurrentXPosts...)
	req := service.XBatchCachePageRequest{
		UserUID:  uid,
		UserName: userName,
		Tab:      tab,
		Posts:    postsCopy,
		Limit:    service.MaxXCurrentPageBatchCache,
	}
	requestGuarded(a, "x-batch-cache-"+uid+"-"+tab, nil, func(ctx context.Context) (service.XBatchCachePageResult, error) {
		return a.backend.CacheXPostsPage(ctx, req)
	}, func(res service.XBatchCachePageResult) {
		a.notifyInfo(formatXBatchCacheSummary(res))
		a.RefreshData()
	}, func(err error) {
		a.showError(err)
		a.RefreshData()
	})
}

func formatXBatchCacheSummary(res service.XBatchCachePageResult) string {
	if res.Enqueued == 0 {
		if res.SkippedUnsupported > 0 && res.SkippedCached == 0 {
			return fmt.Sprintf("当前页无可缓存条目（已跳过 %d 条纯分段流或不支持卡片）", res.SkippedUnsupported)
		}
		if res.SkippedUnsupported > 0 {
			return fmt.Sprintf("当前页已全部缓存（另有 %d 条纯分段流或不支持卡片已跳过）", res.SkippedUnsupported)
		}
		return "当前页没有可缓存的未缓存条目"
	}
	var notes []string
	if res.SkippedUnsupported > 0 {
		notes = append(notes, fmt.Sprintf("跳过 %d 条纯分段流/不支持内容", res.SkippedUnsupported))
	}
	if res.SkippedCapped > 0 {
		notes = append(notes, fmt.Sprintf("单次上限 %d 条，剩余 %d 条可稍后继续", service.MaxXCurrentPageBatchCache, res.SkippedCapped))
	}
	if len(notes) == 0 {
		return fmt.Sprintf("已将 %d 条未缓存帖子加入下载任务", res.Enqueued)
	}
	return fmt.Sprintf("已将 %d 条未缓存帖子加入下载任务（%s）", res.Enqueued, strings.Join(notes, "；"))
}

// LoadMoreUserDynamics loads the next page of creator dynamics while preserving page 1 on error.
func (a *App) LoadMoreUserDynamics() {
	if a.CurrentUser == nil {
		return
	}
	mid, offset := a.CurrentUser.Mid, a.UserDynamicsNext
	requestGuarded(a, fmt.Sprintf("user-dynamics-%d", mid), func() bool { return a.CurrentUser != nil && a.CurrentUser.Mid == mid }, func(ctx context.Context) (bilibili.UserDynamicsPage, error) {
		return a.backend.UserDynamicsPage(ctx, mid, offset)
	}, func(page bilibili.UserDynamicsPage) {
		if a.CurrentUser == nil || a.CurrentUser.Mid != mid {
			return
		}
		seen := make(map[string]bool, len(a.UserDynamics))
		for _, it := range a.UserDynamics {
			seen[it.ID] = true
		}
		for _, it := range page.Items {
			if !seen[it.ID] {
				a.UserDynamics = append(a.UserDynamics, it)
				seen[it.ID] = true
			}
		}
		a.UserDynamicsError = ""
		a.UserDynamicsNext = page.Offset
		a.UserDynamicsHasMore = page.HasMore
	}, func(err error) { a.UserDynamicsError = userErrorText(err); a.showError(err) })
}

func (a *App) resetAndLoadUserVideos() {
	if a.CurrentUser == nil {
		return
	}
	a.userVideosSeq++
	a.UserVideos = nil
	a.UserVideosPage = 0
	a.UserVideosHasMore = false
	a.UserVideosError = ""
	a.UserVidListState = &ui.ListState{}
	a.userIdxCacheKey = ""
	a.LoadMoreUserVideos()
}

// SubmitUserVideosSearch explicitly runs a Bilibili space upload search for the current input.
func (a *App) SubmitUserVideosSearch() {
	if a.CurrentUser == nil {
		return
	}
	a.UserVideosAppliedKeyword = strings.TrimSpace(a.UserVideosKeywordInput)
	a.resetAndLoadUserVideos()
}

// ClearUserVideosSearch clears the space upload search input and reloads when a keyword was active.
func (a *App) ClearUserVideosSearch() {
	hadApplied := strings.TrimSpace(a.UserVideosAppliedKeyword) != ""
	a.UserVideosKeywordInput = ""
	a.UserVideosKeywordComposing = false
	a.UserVideosAppliedKeyword = ""
	if hadApplied {
		a.resetAndLoadUserVideos()
	}
}

// SetUserVideosOrder changes the Bilibili space upload sort order and reloads from page 1.
func (a *App) SetUserVideosOrder(order string) {
	next := bilibili.NormalizeUserVideosOrder(order)
	cur := bilibili.NormalizeUserVideosOrder(a.UserVideosOrder)
	if next == cur && a.UserVideosPage > 0 {
		return
	}
	a.UserVideosOrder = next
	a.resetAndLoadUserVideos()
}

// SetUserVideosTID changes the Bilibili space upload partition filter and reloads from page 1.
func (a *App) SetUserVideosTID(tid int) {
	if tid < 0 {
		tid = 0
	}
	if tid == a.UserVideosTID && a.UserVideosPage > 0 {
		return
	}
	a.UserVideosTID = tid
	a.resetAndLoadUserVideos()
}

func (a *App) LoadMoreUserVideos() {
	if a.CurrentUser == nil {
		return
	}
	mid := a.CurrentUser.Mid
	pageNum := a.UserVideosPage + 1
	seq := a.userVideosSeq
	order := bilibili.NormalizeUserVideosOrder(a.UserVideosOrder)
	keyword := strings.TrimSpace(a.UserVideosAppliedKeyword)
	tid := a.UserVideosTID
	if tid < 0 {
		tid = 0
	}
	reqKey := fmt.Sprintf("user-videos-%d-%d", mid, seq)
	requestGuarded(a, reqKey, func() bool { return a.CurrentUser != nil && a.CurrentUser.Mid == mid && a.userVideosSeq == seq }, func(ctx context.Context) (bilibili.UserVideosPage, error) {
		return a.backend.UserVideosPage(ctx, mid, pageNum, service.UserVideosPageOptions{
			Order:   order,
			Keyword: keyword,
			TID:     tid,
		})
	}, func(page bilibili.UserVideosPage) {
		if a.CurrentUser == nil || a.CurrentUser.Mid != mid || a.userVideosSeq != seq {
			return
		}
		seen := make(map[string]bool, len(a.UserVideos))
		for _, it := range a.UserVideos {
			seen[it.BVID] = true
		}
		for _, it := range page.Items {
			if !seen[it.BVID] {
				a.UserVideos = append(a.UserVideos, it)
				seen[it.BVID] = true
			}
		}
		if len(page.Zones) > 0 && ((tid == 0 && keyword == "") || len(a.UserVideosZones) == 0) {
			a.UserVideosZones = page.Zones
		}
		a.UserVideosError = ""
		a.UserVideosPage = pageNum
		a.UserVideosHasMore = len(a.UserVideos) < page.Total
	}, func(err error) {
		if a.CurrentUser == nil || a.CurrentUser.Mid != mid || a.userVideosSeq != seq {
			return
		}
		a.UserVideosError = userErrorText(err)
		a.showError(err)
	})
}

// bilibiliLiveRooms keeps only Bilibili rooms. The live manager's room list
// also carries Douyin states; the UI shows those from LiveAccounts, and every
// LiveRooms reader treats its entries as Bilibili rooms, so a Douyin state
// left here would show again as a Bilibili recording.
func bilibiliLiveRooms(rooms []live.RoomState) []live.RoomState {
	out := rooms[:0:0]
	for _, room := range rooms {
		if s := normalizeSiteIDUI(room.Site); s == "" || s == "bilibili" {
			out = append(out, room)
		}
	}
	return out
}

func (a *App) mergeLiveRooms(rooms []live.RoomState) {
	for _, room := range bilibiliLiveRooms(rooms) {
		found := false
		for i, previous := range a.LiveRooms {
			if previous.UID == room.UID {
				a.LiveRooms[i], found = room, true
				break
			}
		}
		if !found {
			a.LiveRooms = append(a.LiveRooms, room)
		}
	}
	a.syncLiveAccountsFromBackend()
}
