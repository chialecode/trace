package ui

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
)

type bgRefreshSpec struct {
	backend              service.BackendAPI
	wantStats            bool
	statsDirtyGen        uint64
	wantBackfill         bool
	backfillDirtyGen     uint64
	wantAccountWorks     bool
	accountWorksDirtyGen uint64
	wantLibraryItems     bool
	libraryItemsGen      uint64
	libraryFilter        library.FeedFilter
	libraryDetailKey     string
}

type bgRefreshResult struct {
	backend              service.BackendAPI
	hasStats             bool
	statsDirtyGen        uint64
	stats                library.Statistics
	statsErr             error
	hasBackfill          bool
	backfillDirtyGen     uint64
	backfill             service.BackfillSummaryDTO
	backfillErr          error
	hasAccountWorks      bool
	accountWorksDirtyGen uint64
	accountWorks         map[string]service.AccountWorks
	hasLibraryItems      bool
	libraryItemsGen      uint64
	libraryFilter        library.FeedFilter
	libraryItems         []library.ItemMetadata
	libraryItemsErr      error
	hasLibraryDetail     bool
	libraryDetailKey     string
	libraryDetail        library.ItemMetadata
	libraryDetailOK      bool
}

// backendEventCoalesceKey maps events with equivalent UI refresh semantics to a
// shared key so bursts collapse in-place before reaching the UI main thread.
// Distinct semantic events (e.g. :storage errors, feed_seen:, account loss,
// subscription progress vs completion) use separate keys so none are lost.
func backendEventCoalesceKey(ev service.Event) string {
	switch ev.Type {
	case service.EventLibraryChanged:
		if strings.HasPrefix(ev.Message, "feed_seen:") {
			return "library:feed_seen"
		}
		if ev.Message == "refreshed" {
			return "library:refreshed"
		}
		return "library:change"
	case service.EventTaskUpdated:
		if strings.HasSuffix(ev.Message, ":storage") {
			return "task:storage"
		}
		return "task:normal"
	case service.EventSubscriptionChecked:
		if strings.HasPrefix(ev.Message, "progress:") {
			return "sub:progress"
		}
		return "sub:checked"
	case service.EventCacheQueueUpdated:
		return "cache_queue"
	case service.EventCacheConflictsChanged:
		return "cache_conflicts"
	case service.EventLiveUpdated:
		return "live"
	case service.EventSettingsChanged:
		return "settings"
	case service.EventTransferUpdated:
		return "transfer:" + ev.Message
	case service.EventAccountChanged:
		return "account:" + strings.ToLower(strings.TrimSpace(ev.Site)) + ":" + ev.Message
	default:
		return ""
	}
}

func (a *App) clearPendingEventLoopState() {
	a.eventMu.Lock()
	a.pendingEvents = nil
	a.eventFlushQueued = false
	a.eventMu.Unlock()

	a.bgRefreshMu.Lock()
	a.bgRefreshPending = false
	a.bgRefreshNext = bgRefreshSpec{}
	a.bgRefreshPendingRes = nil
	a.bgRefreshApplyQueued = false
	a.bgRefreshMu.Unlock()

	a.libraryStatsRequestedGen = 0
	a.backfillSummaryRequestedGen = 0
	a.accountWorksRequestedGen = 0
}

// enqueueBackendEvent coalesces incoming backend events and ensures at most one
// flush closure is queued into MyGo's mainLoop at a time. When an incoming event
// matches an existing coalescing key, the older slot is removed and the latest
// event is appended to the tail so arrival order across interleaved keys is preserved.
func (a *App) enqueueBackendEvent(ev service.Event, updateFn func(func())) {
	if updateFn == nil {
		a.handleBackendEvent(ev)
		return
	}
	if a.isClosed() {
		return
	}
	key := backendEventCoalesceKey(ev)
	a.eventMu.Lock()
	if key != "" {
		for i := range a.pendingEvents {
			if backendEventCoalesceKey(a.pendingEvents[i]) == key {
				a.pendingEvents = append(a.pendingEvents[:i], a.pendingEvents[i+1:]...)
				break
			}
		}
	}
	a.pendingEvents = append(a.pendingEvents, ev)
	shouldSchedule := !a.eventFlushQueued
	if shouldSchedule {
		a.eventFlushQueued = true
	}
	a.eventMu.Unlock()

	if shouldSchedule {
		updateFn(a.drainPendingBackendEvents)
	}
}

func (a *App) drainPendingBackendEvents() {
	a.eventMu.Lock()
	batch := a.pendingEvents
	a.pendingEvents = nil
	a.eventFlushQueued = false
	a.eventMu.Unlock()

	if a.isClosed() || len(batch) == 0 {
		return
	}
	prevDefer := a.bgRefreshDefer
	a.bgRefreshDefer = true
	defer func() {
		a.bgRefreshDefer = prevDefer
		if !prevDefer {
			deferred := a.bgRefreshDeferred
			a.bgRefreshDeferred = bgRefreshSpec{}
			if !a.isClosed() {
				a.scheduleAsyncRefresh(deferred)
			}
		}
	}()
	for _, ev := range batch {
		if a.isClosed() {
			return
		}
		a.handleBackendEvent(ev)
	}
}

func (a *App) isLibraryStatisticsVisible() bool {
	switch a.CurrentRoute {
	case RouteHome, RouteLibrary, RouteLibraryItem:
		return true
	case RouteSettings:
		return a.SettingsCategory == "storage"
	default:
		return false
	}
}

func (a *App) isBackfillSummaryVisible() bool {
	if _, hasActive := a.activeWeiboBackfillQueue(); hasActive {
		return false
	}
	if a.CurrentRoute == RouteSettings && a.SettingsCategory == "storage" {
		return true
	}
	if a.CurrentRoute == RouteTasks && a.effectiveTasksSection() == TasksSectionActive && a.effectiveTasksActiveTab() == "queue" {
		return true
	}
	return false
}

func (a *App) isAccountWorksVisible() bool {
	return a.CurrentRoute == RouteSubscriptions &&
		(a.SubAccountsOpen || (a.SubDetailOpen && normalizeSubDetailTab(a.SubDetailTab) == "accounts"))
}

func (a *App) markLibraryStatsDirty() {
	a.libraryStatsDirty = true
	a.libraryStatsDirtyGen++
}

func (a *App) markBackfillSummaryDirty() {
	a.backfillSummaryDirty = true
	a.backfillSummaryDirtyGen++
}

func (a *App) markAccountWorksDirty() {
	a.accountWorksDirty = true
	a.accountWorksDirtyGen++
}

func (a *App) ensureLibraryStatisticsFresh() {
	if !a.libraryStatsDirty || a.backend == nil || !a.backend.HasAnyLogin() || !a.isLibraryStatisticsVisible() {
		return
	}
	if !a.hasDispatcher() {
		a.refreshLibraryStatistics()
		return
	}
	if a.libraryStatsRequestedGen == a.libraryStatsDirtyGen {
		return
	}
	a.libraryStatsRequestedGen = a.libraryStatsDirtyGen
	a.scheduleAsyncRefresh(bgRefreshSpec{
		wantStats:     true,
		statsDirtyGen: a.libraryStatsDirtyGen,
	})
}

func (a *App) ensureBackfillSummaryFresh() {
	if !a.backfillSummaryDirty || a.backend == nil || !a.isBackfillSummaryVisible() {
		return
	}
	if !a.hasDispatcher() {
		a.refreshBackfillSummary()
		return
	}
	if a.backfillSummaryRequestedGen == a.backfillSummaryDirtyGen {
		return
	}
	a.backfillSummaryRequestedGen = a.backfillSummaryDirtyGen
	a.scheduleAsyncRefresh(bgRefreshSpec{
		wantBackfill:     true,
		backfillDirtyGen: a.backfillSummaryDirtyGen,
	})
}

func (a *App) ensureAccountWorksFresh() {
	if !a.accountWorksDirty || a.backend == nil || !a.backend.HasAnyLogin() || !a.isAccountWorksVisible() {
		return
	}
	if !a.hasDispatcher() {
		a.refreshAccountWorks()
		return
	}
	if a.accountWorksRequestedGen == a.accountWorksDirtyGen {
		return
	}
	a.accountWorksRequestedGen = a.accountWorksDirtyGen
	a.scheduleAsyncRefresh(bgRefreshSpec{
		wantAccountWorks:     true,
		accountWorksDirtyGen: a.accountWorksDirtyGen,
	})
}

func (a *App) ensureVisibleDerivedDataFresh() {
	a.ensureLibraryStatisticsFresh()
	a.ensureBackfillSummaryFresh()
	a.ensureAccountWorksFresh()
}

// scheduleLibraryChangedRefresh marks derived library/subscription/backfill
// caches dirty and refreshes only the datasets visible on the current route
// (asynchronously when a UI dispatcher is bound, synchronously in headless tests).
func (a *App) scheduleLibraryChangedRefresh() {
	a.markLibraryStatsDirty()
	a.markBackfillSummaryDirty()
	a.markAccountWorksDirty()

	needStats := a.isLibraryStatisticsVisible()
	needBackfill := a.isBackfillSummaryVisible()
	needWorks := a.isAccountWorksVisible()
	needLibItems := a.CurrentRoute == RouteLibrary || a.CurrentRoute == RouteLibraryItem
	if !needLibItems {
		a.libraryItemsDirty = true
	}
	if a.hasDispatcher() {
		spec := bgRefreshSpec{}
		if needStats {
			a.libraryStatsRequestedGen = a.libraryStatsDirtyGen
			spec.wantStats = true
			spec.statsDirtyGen = a.libraryStatsDirtyGen
		}
		if needBackfill {
			a.backfillSummaryRequestedGen = a.backfillSummaryDirtyGen
			spec.wantBackfill = true
			spec.backfillDirtyGen = a.backfillSummaryDirtyGen
		}
		if needWorks {
			a.accountWorksRequestedGen = a.accountWorksDirtyGen
			spec.wantAccountWorks = true
			spec.accountWorksDirtyGen = a.accountWorksDirtyGen
		}
		if needLibItems {
			a.libraryItemsDirty = false
			spec.wantLibraryItems = true
			spec.libraryItemsGen = a.libraryItemsGen
			spec.libraryFilter = a.currentLibraryFeedFilter(a.libraryInPlaceLimit(), 0)
			if a.CurrentRoute == RouteLibraryItem && a.CurrentLibraryDetail != nil {
				spec.libraryDetailKey = a.CurrentLibraryDetail.ItemKey
			}
		}
		a.scheduleAsyncRefresh(spec)
		return
	}
	if needStats {
		a.refreshLibraryStatistics()
	}
	if needBackfill {
		a.refreshBackfillSummary()
	}
	if needWorks {
		a.refreshAccountWorks()
	}
	switch a.CurrentRoute {
	case RouteLibraryItem:
		a.libraryItemsDirty = false
		a.libraryOffset = 0
		a.loadLibraryPage(true)
		if a.CurrentLibraryDetail != nil {
			if it, ok, err := a.backend.GetLibraryItem(a.CurrentLibraryDetail.ItemKey); err == nil && ok {
				a.CurrentLibraryDetail = &it
			}
		}
	case RouteLibrary:
		a.libraryItemsDirty = false
		a.reloadLibraryItemsInPlace()
	}
}

func (a *App) hasDispatcher() bool {
	a.mu.Lock()
	ok := a.dispatch != nil
	a.mu.Unlock()
	return ok
}

func (a *App) currentLibraryFeedFilter(limit, offset int) library.FeedFilter {
	authors, authorRefs := a.libraryAuthorFilter()
	sortOrder := "newest"
	if a.LibrarySort == "oldest" {
		sortOrder = "oldest"
	}
	return library.FeedFilter{
		Site:           normalizeSiteFilter(a.LibrarySiteFilter),
		Kind:           a.LibraryKindFilter,
		ExcludeForward: a.LibraryKindFilter == "image" || a.LibraryKindFilter == "video",
		Mid:            a.LibraryCreatorMid,
		Authors:        authors,
		AuthorRefs:     authorRefs,
		Keyword:        a.LibrarySearch,
		Sort:           sortOrder,
		Limit:          limit,
		Offset:         offset,
	}
}

func libraryFeedFilterCriteriaEqual(x, y library.FeedFilter) bool {
	return x.Site == y.Site &&
		x.Kind == y.Kind &&
		x.ExcludeForward == y.ExcludeForward &&
		x.Mid == y.Mid &&
		x.Keyword == y.Keyword &&
		x.Sort == y.Sort &&
		x.Offset == y.Offset &&
		slices.Equal(x.Authors, y.Authors) &&
		slices.Equal(x.AuthorRefs, y.AuthorRefs)
}

func (a *App) libraryInPlaceLimit() int {
	n := len(a.LibraryItems)
	if n < 60 {
		return 60
	}
	if n > 600 {
		return 600
	}
	return n
}

func (a *App) scheduleAsyncRefresh(spec bgRefreshSpec) {
	if !spec.wantStats && !spec.wantBackfill && !spec.wantAccountWorks && !spec.wantLibraryItems && spec.libraryDetailKey == "" {
		return
	}
	if a.bgRefreshDefer {
		mergeBgRefreshSpec(&a.bgRefreshDeferred, spec)
		return
	}
	backend := a.backend
	if backend == nil {
		return
	}
	a.mu.Lock()
	closed := a.closed
	dispatch := a.dispatch
	ctx := a.ctx
	a.mu.Unlock()
	if closed || dispatch == nil {
		return
	}
	spec.backend = backend

	a.bgRefreshMu.Lock()
	mergeBgRefreshSpec(&a.bgRefreshNext, spec)
	if a.bgRefreshRunning {
		a.bgRefreshPending = true
		a.bgRefreshMu.Unlock()
		return
	}
	a.bgRefreshRunning = true
	a.bgRefreshPending = false
	runSpec := a.bgRefreshNext
	a.bgRefreshNext = bgRefreshSpec{}
	a.bgRefreshMu.Unlock()

	go a.runBgRefreshLoop(ctx, dispatch, runSpec)
}

func mergeBgRefreshSpec(dst *bgRefreshSpec, src bgRefreshSpec) {
	if src.backend != nil {
		dst.backend = src.backend
	}
	if src.wantStats {
		dst.wantStats = true
		if src.statsDirtyGen >= dst.statsDirtyGen {
			dst.statsDirtyGen = src.statsDirtyGen
		}
	}
	if src.wantBackfill {
		dst.wantBackfill = true
		if src.backfillDirtyGen >= dst.backfillDirtyGen {
			dst.backfillDirtyGen = src.backfillDirtyGen
		}
	}
	if src.wantAccountWorks {
		dst.wantAccountWorks = true
		if src.accountWorksDirtyGen >= dst.accountWorksDirtyGen {
			dst.accountWorksDirtyGen = src.accountWorksDirtyGen
		}
	}
	if src.wantLibraryItems {
		dst.wantLibraryItems = true
		dst.libraryItemsGen = src.libraryItemsGen
		dst.libraryFilter = src.libraryFilter
	}
	if src.libraryDetailKey != "" {
		dst.libraryDetailKey = src.libraryDetailKey
	}
}

func mergeBgRefreshResult(dst *bgRefreshResult, src bgRefreshResult) {
	if src.backend != nil && dst.backend != src.backend {
		*dst = src
		return
	}
	if src.hasStats {
		dst.hasStats = true
		dst.statsDirtyGen = src.statsDirtyGen
		dst.stats = src.stats
		dst.statsErr = src.statsErr
	}
	if src.hasBackfill {
		dst.hasBackfill = true
		dst.backfillDirtyGen = src.backfillDirtyGen
		dst.backfill = src.backfill
		dst.backfillErr = src.backfillErr
	}
	if src.hasAccountWorks {
		dst.hasAccountWorks = true
		dst.accountWorksDirtyGen = src.accountWorksDirtyGen
		dst.accountWorks = src.accountWorks
	}
	if src.hasLibraryItems {
		dst.hasLibraryItems = true
		dst.libraryItemsGen = src.libraryItemsGen
		dst.libraryFilter = src.libraryFilter
		dst.libraryItems = src.libraryItems
		dst.libraryItemsErr = src.libraryItemsErr
	}
	if src.hasLibraryDetail {
		dst.hasLibraryDetail = true
		dst.libraryDetailKey = src.libraryDetailKey
		dst.libraryDetail = src.libraryDetail
		dst.libraryDetailOK = src.libraryDetailOK
	}
}

func (a *App) runBgRefreshLoop(ctx context.Context, dispatch func(func()), spec bgRefreshSpec) {
	for {
		if ctx.Err() != nil || a.isClosed() {
			a.bgRefreshMu.Lock()
			a.bgRefreshRunning = false
			a.bgRefreshPending = false
			a.bgRefreshNext = bgRefreshSpec{}
			a.bgRefreshMu.Unlock()
			return
		}
		res := executeBgRefresh(ctx, spec)
		if ctx.Err() != nil || a.isClosed() {
			a.bgRefreshMu.Lock()
			a.bgRefreshRunning = false
			a.bgRefreshPending = false
			a.bgRefreshNext = bgRefreshSpec{}
			a.bgRefreshMu.Unlock()
			return
		}
		a.postBgRefreshResult(dispatch, res)

		if ctx.Err() != nil || a.isClosed() {
			a.bgRefreshMu.Lock()
			a.bgRefreshRunning = false
			a.bgRefreshPending = false
			a.bgRefreshNext = bgRefreshSpec{}
			a.bgRefreshMu.Unlock()
			return
		}
		a.bgRefreshMu.Lock()
		if !a.bgRefreshPending {
			a.bgRefreshRunning = false
			a.bgRefreshMu.Unlock()
			return
		}
		a.bgRefreshPending = false
		spec = a.bgRefreshNext
		a.bgRefreshNext = bgRefreshSpec{}
		a.bgRefreshMu.Unlock()
	}
}

func executeBgRefresh(ctx context.Context, spec bgRefreshSpec) bgRefreshResult {
	res := bgRefreshResult{backend: spec.backend}
	if spec.backend == nil {
		return res
	}
	if spec.wantStats && ctx.Err() == nil {
		stats, err := spec.backend.LibraryStatistics()
		res.hasStats = true
		res.statsDirtyGen = spec.statsDirtyGen
		res.stats = stats
		res.statsErr = err
	}
	if spec.wantBackfill && ctx.Err() == nil {
		sum, err := spec.backend.BackfillSummary()
		res.hasBackfill = true
		res.backfillDirtyGen = spec.backfillDirtyGen
		res.backfill = sum
		res.backfillErr = err
	}
	if spec.wantAccountWorks && ctx.Err() == nil {
		res.hasAccountWorks = true
		res.accountWorksDirtyGen = spec.accountWorksDirtyGen
		res.accountWorks = spec.backend.SubscriptionAccountWorks()
	}
	if spec.wantLibraryItems && ctx.Err() == nil {
		items, err := spec.backend.ListLibraryItems(spec.libraryFilter)
		res.hasLibraryItems = true
		res.libraryItemsGen = spec.libraryItemsGen
		res.libraryFilter = spec.libraryFilter
		res.libraryItems = items
		res.libraryItemsErr = err
	}
	if spec.libraryDetailKey != "" && ctx.Err() == nil {
		it, ok, err := spec.backend.GetLibraryItem(spec.libraryDetailKey)
		if err == nil {
			res.hasLibraryDetail = true
			res.libraryDetailKey = spec.libraryDetailKey
			res.libraryDetail = it
			res.libraryDetailOK = ok
		}
	}
	return res
}

func (a *App) postBgRefreshResult(dispatch func(func()), res bgRefreshResult) {
	if a.isClosed() || dispatch == nil {
		return
	}
	a.bgRefreshMu.Lock()
	if a.bgRefreshPendingRes == nil {
		cp := res
		a.bgRefreshPendingRes = &cp
	} else {
		mergeBgRefreshResult(a.bgRefreshPendingRes, res)
	}
	shouldSchedule := !a.bgRefreshApplyQueued
	if shouldSchedule {
		a.bgRefreshApplyQueued = true
	}
	a.bgRefreshMu.Unlock()

	if shouldSchedule {
		dispatch(a.applyPendingBgRefreshResult)
	}
}

func (a *App) applyPendingBgRefreshResult() {
	a.bgRefreshMu.Lock()
	res := a.bgRefreshPendingRes
	a.bgRefreshPendingRes = nil
	a.bgRefreshApplyQueued = false
	a.bgRefreshMu.Unlock()

	if res == nil || a.isClosed() || res.backend != a.backend {
		return
	}
	if res.hasStats {
		if res.statsErr != nil {
			a.showError(fmt.Errorf("读取媒体库统计失败：%w", res.statsErr))
		} else {
			a.libraryStatistics = res.stats
			a.statsRev++
			if res.statsDirtyGen >= a.libraryStatsDirtyGen {
				a.libraryStatsDirty = false
			}
		}
	}
	if res.hasBackfill {
		if res.backfillErr == nil {
			a.BackfillSummary = res.backfill
			if res.backfillDirtyGen >= a.backfillSummaryDirtyGen {
				a.backfillSummaryDirty = false
			}
		}
	}
	if res.hasAccountWorks {
		a.accountWorks = res.accountWorks
		if res.accountWorksDirtyGen >= a.accountWorksDirtyGen {
			a.accountWorksDirty = false
		}
	}
	if res.hasLibraryItems &&
		(a.CurrentRoute == RouteLibrary || a.CurrentRoute == RouteLibraryItem) &&
		res.libraryItemsGen == a.libraryItemsGen {
		curLimit := a.libraryInPlaceLimit()
		curFilter := a.currentLibraryFeedFilter(curLimit, 0)
		if libraryFeedFilterCriteriaEqual(curFilter, res.libraryFilter) && res.libraryFilter.Limit >= curLimit {
			if res.libraryItemsErr != nil {
				a.LibraryError = userErrorText(res.libraryItemsErr)
			} else {
				a.LibraryError = ""
				a.LibraryItems = res.libraryItems
				a.libraryOffset = len(res.libraryItems)
				a.LibraryHasMore = len(res.libraryItems) == res.libraryFilter.Limit
				a.libraryItemsDirty = false
			}
		}
	}
	if res.hasLibraryDetail && res.libraryDetailOK && a.CurrentRoute == RouteLibraryItem && a.CurrentLibraryDetail != nil && a.CurrentLibraryDetail.ItemKey == res.libraryDetailKey {
		it := res.libraryDetail
		a.CurrentLibraryDetail = &it
	}
}
