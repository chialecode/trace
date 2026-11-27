package ui

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/uistate"
	"github.com/egoist/mygo/ui"
)

type slowEventCountingBackend struct {
	service.BackendAPI
	mu            sync.Mutex
	subs          []func(service.Event)
	statsCalls    atomic.Int32
	backfillCalls atomic.Int32
	worksCalls    atomic.Int32
	listLibCalls  atomic.Int32
	taskListCalls atomic.Int32
	queueCalls    atomic.Int32

	statsEntered chan struct{}
	statsRelease chan struct{}
	statsOnce    sync.Once

	listAllEntered chan struct{}
	listAllRelease chan struct{}
	listAllOnce    sync.Once

	queueState      []service.CacheQueueTask
	backfillSummary service.BackfillSummaryDTO
	listAllItems    []library.ItemMetadata
	listWeiboItems  []library.ItemMetadata
	listPageHook    func(f library.FeedFilter) ([]library.ItemMetadata, bool, error)
}

func (b *slowEventCountingBackend) SubscribeEvents(fn func(service.Event)) func() {
	b.mu.Lock()
	idx := len(b.subs)
	b.subs = append(b.subs, fn)
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		b.subs[idx] = nil
		b.mu.Unlock()
	}
}

func (b *slowEventCountingBackend) emit(ev service.Event) {
	b.mu.Lock()
	subs := append(([]func(service.Event))(nil), b.subs...)
	b.mu.Unlock()
	for _, fn := range subs {
		if fn != nil {
			fn(ev)
		}
	}
}

func (b *slowEventCountingBackend) LibraryStatistics() (library.Statistics, error) {
	b.statsCalls.Add(1)
	if b.statsEntered != nil {
		b.statsOnce.Do(func() { close(b.statsEntered) })
	}
	if b.statsRelease != nil {
		<-b.statsRelease
	}
	return b.BackendAPI.LibraryStatistics()
}

func (b *slowEventCountingBackend) BackfillSummary() (service.BackfillSummaryDTO, error) {
	b.backfillCalls.Add(1)
	b.mu.Lock()
	sum := b.backfillSummary
	b.mu.Unlock()
	return sum, nil
}

func (b *slowEventCountingBackend) SubscriptionAccountWorks() map[string]service.AccountWorks {
	b.worksCalls.Add(1)
	return b.BackendAPI.SubscriptionAccountWorks()
}

func (b *slowEventCountingBackend) ListLibraryItems(f library.FeedFilter) ([]library.ItemMetadata, error) {
	b.listLibCalls.Add(1)
	if b.listPageHook != nil {
		if items, handled, err := b.listPageHook(f); handled {
			return items, err
		}
	}
	if f.Site == "all" && b.listAllEntered != nil {
		b.listAllOnce.Do(func() { close(b.listAllEntered) })
		if b.listAllRelease != nil {
			<-b.listAllRelease
		}
		b.mu.Lock()
		items := append([]library.ItemMetadata(nil), b.listAllItems...)
		b.mu.Unlock()
		if len(items) > 0 {
			return items, nil
		}
	}
	if f.Site == "weibo" {
		b.mu.Lock()
		items := append([]library.ItemMetadata(nil), b.listWeiboItems...)
		b.mu.Unlock()
		if len(items) > 0 {
			return items, nil
		}
	}
	return b.BackendAPI.ListLibraryItems(f)
}

func (b *slowEventCountingBackend) ListCacheQueue() []service.CacheQueueTask {
	b.queueCalls.Add(1)
	b.mu.Lock()
	out := append([]service.CacheQueueTask(nil), b.queueState...)
	b.mu.Unlock()
	return out
}

func (b *slowEventCountingBackend) ListDownloadTasks() ([]download.Task, error) {
	b.taskListCalls.Add(1)
	return b.BackendAPI.ListDownloadTasks()
}

// TestBindEventLoopCoalescesBurstAndPreventsMainThreadStarvation reproduces the
// exact live freeze pattern (250+ burst EventLibraryChanged + 100 EventCacheQueueUpdated
// while SQLite statistics queries are slow) and verifies:
// 1. BindEventLoop enqueues at most 1 flush closure into the UI queue (never 350+).
// 2. Draining the UI queue finishes immediately even while LibraryStatistics() is blocked.
// 3. Critical distinct events (:storage error, feed_seen:, account expired) are never dropped.
// 4. Off-route BackfillSummary and SubscriptionAccountWorks scans stay at 0.
func TestBindEventLoopCoalescesBurstAndPreventsMainThreadStarvation(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 5)
	app.Navigate(RouteLibrary)

	statsEntered := make(chan struct{})
	statsRelease := make(chan struct{})
	defer func() {
		select {
		case <-statsRelease:
		default:
			close(statsRelease)
		}
	}()

	stub := &slowEventCountingBackend{
		BackendAPI:   app.backend,
		statsEntered: statsEntered,
		statsRelease: statsRelease,
	}
	app.backend = stub

	// Prepare updated HomeSeen in ui-state so feed_seen: sync is observable.
	if _, err := app.backend.MutateUIState(func(st *uistate.State) {
		if st.HomeSeen == nil {
			st.HomeSeen = map[string]int64{}
		}
		st.HomeSeen["sub-burst"] = 777
	}); err != nil {
		t.Fatalf("MutateUIState: %v", err)
	}

	mainQueue := make(chan func(), 512)
	var updatePosts atomic.Int32
	app.BindEventLoop(func(fn func()) {
		updatePosts.Add(1)
		mainQueue <- fn
	})

	// Burst 250 EventLibraryChanged + 100 EventCacheQueueUpdated + 50 EventTaskUpdated
	// plus critical semantic events while the UI thread has not drained yet.
	for i := 0; i < 250; i++ {
		stub.emit(service.Event{
			Type:    service.EventLibraryChanged,
			Site:    "weibo",
			Message: fmt.Sprintf("weibo:dynamic:%04d", i),
		})
	}
	for i := 0; i < 100; i++ {
		stub.emit(service.Event{
			Type:    service.EventCacheQueueUpdated,
			Site:    "weibo",
			Message: fmt.Sprintf("cq_1:running:%d", i),
		})
	}
	for i := 0; i < 50; i++ {
		stub.emit(service.Event{
			Type:    service.EventTaskUpdated,
			Site:    "weibo",
			Message: fmt.Sprintf("task_%d:running", i),
		})
	}
	stub.emit(service.Event{Type: service.EventTaskUpdated, Site: "weibo", Message: "task_err:storage"})
	stub.emit(service.Event{Type: service.EventLibraryChanged, Message: "feed_seen:sub-burst"})
	stub.emit(service.Event{Type: service.EventAccountChanged, Site: "weibo", Message: "expired"})

	// 1. All 403 events must coalesce into a SINGLE queued UI callback!
	if got := updatePosts.Load(); got != 1 {
		t.Fatalf("BindEventLoop posted %d updateFn callbacks for 403 burst events, want 1", got)
	}

	// 2. Drain the UI callback WHILE LibraryStatistics() is intentionally blocked.
	// If handleBackendEvent ran LibraryStatistics synchronously on the UI thread,
	// this would hang until the 2s timeout and fail immediately.
	drainDone := make(chan struct{})
	go func() {
		fn := <-mainQueue
		fn()
		close(drainDone)
	}()
	select {
	case <-drainDone:
	case <-time.After(2 * time.Second):
		t.Fatal("UI main thread blocked waiting for slow LibraryStatistics() during event drain")
	}

	// Wait for the background worker goroutine to enter the slow LibraryStatistics call.
	select {
	case <-statsEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("expected single-flight background refresh to invoke LibraryStatistics off the UI thread")
	}

	// 3. Critical distinct events must have been preserved and processed.
	if app.homeSeenLocal["sub-burst"] != 777 {
		t.Fatalf("feed_seen event lost during coalescing: homeSeenLocal=%v", app.homeSeenLocal)
	}
	if app.BannerError == "" {
		t.Fatal("expected :storage error or account expired notice to be preserved during coalescing")
	}

	// Release slow SQLite scan and drain the single async apply callback.
	close(statsRelease)
	select {
	case applyFn := <-mainQueue:
		applyFn()
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for async refresh result to dispatch back to UI")
	}

	// 4. Verify bounded query counts: never 250 scans, and 0 off-route scans!
	if got := stub.statsCalls.Load(); got != 1 {
		t.Fatalf("LibraryStatistics called %d times for 250 burst events, want 1", got)
	}
	if got := stub.backfillCalls.Load(); got != 0 {
		t.Fatalf("off-route BackfillSummary called %d times, want 0", got)
	}
	if got := stub.worksCalls.Load(); got != 0 {
		t.Fatalf("off-route SubscriptionAccountWorks called %d times, want 0", got)
	}
	if got := stub.listLibCalls.Load(); got != 1 {
		t.Fatalf("ListLibraryItems called %d times for 250 burst events, want 1", got)
	}
}

// TestEventCacheQueueUpdatedSkipsBackfillSummaryAndRefreshesLazilyWhenVisible ensures
// queue state ticks during active Weibo backfill never trigger BackfillSummary() full scans,
// and BackfillSummary refreshes once when the user opens Settings > Storage after completion.
func TestEventCacheQueueUpdatedSkipsBackfillSummaryAndRefreshesLazilyWhenVisible(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 2)
	app.Navigate(RouteLibrary)

	stub := &slowEventCountingBackend{
		BackendAPI: app.backend,
		queueState: []service.CacheQueueTask{{
			ID:     "cq_bf_1",
			Kind:   service.CacheQueueKindWeiboBackfill,
			Site:   "weibo",
			List:   service.CacheQueueListWeiboBackfill,
			Status: service.CacheQueueRunning,
		}},
		backfillSummary: service.BackfillSummaryDTO{TotalItems: 7, WeiboMissingNoWM: 7},
	}
	app.backend = stub
	app.CacheQueue = stub.ListCacheQueue()
	stub.queueCalls.Store(0)

	// 50 running/waiting queue ticks must invoke BackfillSummary ZERO times.
	for i := 0; i < 50; i++ {
		app.handleBackendEvent(service.Event{
			Type:    service.EventCacheQueueUpdated,
			Site:    "weibo",
			Message: fmt.Sprintf("cq_bf_1:waiting:%d", i),
		})
	}
	if got := stub.backfillCalls.Load(); got != 0 {
		t.Fatalf("EventCacheQueueUpdated during active backfill called BackfillSummary %d times, want 0", got)
	}

	// Backfill completes while user is still on /library -> marks dirty, still 0 scans!
	stub.mu.Lock()
	stub.queueState[0].Status = service.CacheQueueCompleted
	stub.backfillSummary = service.BackfillSummaryDTO{TotalItems: 0}
	stub.mu.Unlock()
	app.handleBackendEvent(service.Event{
		Type:    service.EventCacheQueueUpdated,
		Site:    "weibo",
		Message: "cq_bf_1:completed",
	})
	if got := stub.backfillCalls.Load(); got != 0 {
		t.Fatalf("off-route backfill completion called BackfillSummary %d times, want 0", got)
	}
	if !app.backfillSummaryDirty {
		t.Fatal("expected backfillSummaryDirty = true after backfill transitioned from active to completed")
	}

	// Opening Settings > Storage lazily refreshes BackfillSummary once (not per frame!).
	app.Navigate(RouteSettings)
	app.SettingsCategory = "storage"
	tt := ui.NewTester(app.View, 1280, 860)
	for i := 0; i < 5; i++ {
		tt.Frame()
	}
	if got := stub.backfillCalls.Load(); got != 1 {
		t.Fatalf("Settings > Storage lazy refresh called BackfillSummary %d times across 5 frames, want 1", got)
	}
	if app.backfillSummaryDirty {
		t.Fatal("expected backfillSummaryDirty = false after Settings > Storage refreshed")
	}
}

// TestEventLibraryChangedOffRouteLazyDirtyRefreshAndFeedSeenFastPath verifies:
//  1. feed_seen: EventLibraryChanged skips all heavy library/task SQLite scans.
//  2. Regular EventLibraryChanged while on /tasks (downloads) skips LibraryStatistics,
//     BackfillSummary, SubscriptionAccountWorks, and ListLibraryItems, refreshing each
//     lazily only when the corresponding view becomes visible.
func TestEventLibraryChangedOffRouteLazyDirtyRefreshAndFeedSeenFastPath(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 3)

	stub := &slowEventCountingBackend{
		BackendAPI:      app.backend,
		backfillSummary: service.BackfillSummaryDTO{TotalItems: 2, WeiboLegacyLinks: 2},
	}
	app.backend = stub

	// 1. feed_seen: fast path on Home does ZERO heavy scans.
	app.Navigate(RouteHome)
	if _, err := app.backend.MutateUIState(func(st *uistate.State) {
		if st.HomeSeen == nil {
			st.HomeSeen = map[string]int64{}
		}
		st.HomeSeen["sub-fast"] = 12345
	}); err != nil {
		t.Fatal(err)
	}
	stub.statsCalls.Store(0)
	stub.backfillCalls.Store(0)
	stub.worksCalls.Store(0)
	stub.listLibCalls.Store(0)
	stub.taskListCalls.Store(0)

	app.handleBackendEvent(service.Event{Type: service.EventLibraryChanged, Message: "feed_seen:sub-fast"})
	if app.homeSeenLocal["sub-fast"] != 12345 {
		t.Fatalf("homeSeenLocal[sub-fast] = %d, want 12345", app.homeSeenLocal["sub-fast"])
	}
	if stub.statsCalls.Load() != 0 || stub.backfillCalls.Load() != 0 || stub.worksCalls.Load() != 0 || stub.listLibCalls.Load() != 0 || stub.taskListCalls.Load() != 0 {
		t.Fatalf("feed_seen triggered heavy queries: stats=%d backfill=%d works=%d lib=%d tasks=%d",
			stub.statsCalls.Load(), stub.backfillCalls.Load(), stub.worksCalls.Load(), stub.listLibCalls.Load(), stub.taskListCalls.Load())
	}

	// 2. Off-route EventLibraryChanged on /tasks (downloads) triggers 0 heavy library scans.
	app.Navigate(RouteTasks)
	app.TasksSection = TasksSectionActive
	app.TasksActiveTab = "downloads"
	stub.statsCalls.Store(0)
	stub.backfillCalls.Store(0)
	stub.worksCalls.Store(0)
	stub.listLibCalls.Store(0)

	app.handleBackendEvent(service.Event{Type: service.EventLibraryChanged, Site: "weibo", Message: "weibo:dynamic:1"})
	if stub.statsCalls.Load() != 0 || stub.backfillCalls.Load() != 0 || stub.worksCalls.Load() != 0 || stub.listLibCalls.Load() != 0 {
		t.Fatalf("off-route EventLibraryChanged on /tasks(downloads) ran heavy scans: stats=%d backfill=%d works=%d lib=%d",
			stub.statsCalls.Load(), stub.backfillCalls.Load(), stub.worksCalls.Load(), stub.listLibCalls.Load())
	}

	// Switching to /home refreshes LibraryStatistics lazily.
	app.Navigate(RouteHome)
	if stub.statsCalls.Load() != 1 {
		t.Fatalf("navigating to /home called LibraryStatistics %d times, want 1", stub.statsCalls.Load())
	}
	if stub.backfillCalls.Load() != 0 || stub.worksCalls.Load() != 0 {
		t.Fatalf("navigating to /home unnecessarily queried backfill=%d works=%d", stub.backfillCalls.Load(), stub.worksCalls.Load())
	}

	// Switching to Tasks > Queue refreshes BackfillSummary lazily.
	app.Navigate(RouteTasks)
	app.TasksSection = TasksSectionActive
	app.TasksActiveTab = "queue"
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	if stub.backfillCalls.Load() != 1 {
		t.Fatalf("opening Tasks > Queue called BackfillSummary %d times, want 1", stub.backfillCalls.Load())
	}
	if app.BackfillSummary.TotalItems != 2 {
		t.Fatalf("BackfillSummary.TotalItems = %d, want 2", app.BackfillSummary.TotalItems)
	}

	// Switching to Subscriptions > Accounts refreshes SubscriptionAccountWorks lazily.
	app.Navigate(RouteSubscriptions)
	app.SubAccountsOpen = true
	tt.Frame()
	if stub.worksCalls.Load() != 1 {
		t.Fatalf("opening Subscriptions > Accounts called SubscriptionAccountWorks %d times, want 1", stub.worksCalls.Load())
	}
}

// TestAsyncLibraryRefreshDiscardsStaleFilterResult ensures an in-flight async
// library reload for an older filter cannot overwrite results after the user
// switches the library site filter.
func TestAsyncLibraryRefreshDiscardsStaleFilterResult(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 1)
	app.Navigate(RouteLibrary)
	app.LibrarySiteFilter = "all"

	listAllEntered := make(chan struct{})
	listAllRelease := make(chan struct{})
	defer func() {
		select {
		case <-listAllRelease:
		default:
			close(listAllRelease)
		}
	}()

	stub := &slowEventCountingBackend{
		BackendAPI:     app.backend,
		listAllEntered: listAllEntered,
		listAllRelease: listAllRelease,
		listAllItems:   []library.ItemMetadata{{ItemKey: "bilibili:video:BVstale", Site: "bilibili", Title: "旧全部列表"}},
		listWeiboItems: []library.ItemMetadata{{ItemKey: "weibo:dynamic:wb_fresh", Site: "weibo", Title: "微博新筛选"}},
	}
	app.backend = stub

	mainQueue := make(chan func(), 64)
	app.BindEventLoop(func(fn func()) { mainQueue <- fn })

	// Trigger EventLibraryChanged while filter is "all".
	stub.emit(service.Event{Type: service.EventLibraryChanged, Site: "weibo", Message: "weibo:dynamic:wb_fresh"})
	fn := <-mainQueue
	fn()

	select {
	case <-listAllEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for async ListLibraryItems(all) to start")
	}

	// User switches filter to "weibo" while the "all" query is still running.
	app.LibrarySiteFilter = "weibo"
	app.selectLibraryFilter()
	if len(app.LibraryItems) != 1 || app.LibraryItems[0].ItemKey != "weibo:dynamic:wb_fresh" {
		t.Fatalf("expected immediate weibo filter items, got %+v", app.LibraryItems)
	}

	// Release stale "all" query and drain UI apply callback.
	close(listAllRelease)
	select {
	case applyFn := <-mainQueue:
		applyFn()
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stale apply callback")
	}

	if len(app.LibraryItems) != 1 || app.LibraryItems[0].ItemKey != "weibo:dynamic:wb_fresh" {
		t.Fatalf("stale async 'all' result overwrote 'weibo' library items: %+v", app.LibraryItems)
	}
}

// TestBindEventLoopSynchronousUpdateFnDoesNotSelfDeadlock verifies that binding
// an inline synchronous updateFn (as used in re-entrancy tests) never deadlocks
// even if handling one event synchronously publishes another event.
func TestBindEventLoopSynchronousUpdateFnDoesNotSelfDeadlock(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 1)

	stub := &slowEventCountingBackend{BackendAPI: app.backend}
	app.backend = stub
	app.BindEventLoop(func(fn func()) { fn() })

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20; i++ {
			stub.emit(service.Event{Type: service.EventCacheQueueUpdated, Site: "weibo", Message: fmt.Sprintf("q:%d", i)})
			stub.emit(service.Event{Type: service.EventSettingsChanged, Message: "updated"})
		}
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("deadlock in synchronous BindEventLoop dispatch")
	}
}

// TestAsyncLibraryRefreshRejectsStalePaginationAndSyncReload verifies that an
// in-flight async ListLibraryItems result cannot truncate a user's paginated
// /library list (e.g. 60 -> 80 items) or resurrect items removed by a newer
// synchronous reload under the exact same filter.
func TestAsyncLibraryRefreshRejectsStalePaginationAndSyncReload(t *testing.T) {
	t.Run("pagination_not_truncated", func(t *testing.T) {
		app, _, cleanup := newTestUIApp(t)
		defer cleanup()
		seedUIFeed(t, app, 1)
		app.Navigate(RouteLibrary)
		app.LibrarySiteFilter = "all"

		page1 := make([]library.ItemMetadata, 60)
		for i := range page1 {
			page1[i] = library.ItemMetadata{ItemKey: fmt.Sprintf("weibo:dynamic:p1_%03d", i), Site: "weibo", Title: fmt.Sprintf("P1-%d", i)}
		}
		page2 := make([]library.ItemMetadata, 20)
		for i := range page2 {
			page2[i] = library.ItemMetadata{ItemKey: fmt.Sprintf("weibo:dynamic:p2_%03d", i), Site: "weibo", Title: fmt.Sprintf("P2-%d", i)}
		}

		staleEntered := make(chan struct{})
		staleRelease := make(chan struct{})
		var staleOnce sync.Once
		defer func() {
			select {
			case <-staleRelease:
			default:
				close(staleRelease)
			}
		}()

		stub := &slowEventCountingBackend{
			BackendAPI: app.backend,
			listPageHook: func(f library.FeedFilter) ([]library.ItemMetadata, bool, error) {
				if f.Offset == 0 && f.Limit == 60 {
					staleOnce.Do(func() { close(staleEntered) })
					<-staleRelease
					return append([]library.ItemMetadata(nil), page1...), true, nil
				}
				if f.Offset == 60 && f.Limit == 60 {
					return append([]library.ItemMetadata(nil), page2...), true, nil
				}
				return nil, false, nil
			},
		}
		app.backend = stub
		app.LibraryItems = append([]library.ItemMetadata(nil), page1...)
		app.libraryOffset = len(page1)
		app.LibraryHasMore = true

		mainQueue := make(chan func(), 64)
		app.BindEventLoop(func(fn func()) { mainQueue <- fn })

		// Background EventLibraryChanged starts an async limit=60 reload.
		stub.emit(service.Event{Type: service.EventLibraryChanged, Site: "weibo", Message: "weibo:dynamic:bg"})
		fn := <-mainQueue
		fn()

		select {
		case <-staleEntered:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for async ListLibraryItems(limit=60) to enter")
		}

		// User scrolls down and loads page 2 (60 -> 80 items) while async query is in-flight.
		app.loadLibraryPage(false)
		if len(app.LibraryItems) != 80 || app.libraryOffset != 80 {
			t.Fatalf("expected 80 paginated items before async reply, got len=%d offset=%d", len(app.LibraryItems), app.libraryOffset)
		}

		// Release older limit=60 async result and drain UI apply callback.
		close(staleRelease)
		select {
		case applyFn := <-mainQueue:
			applyFn()
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for stale apply callback")
		}

		if len(app.LibraryItems) != 80 || app.libraryOffset != 80 {
			t.Fatalf("stale async limit=60 result truncated paginated /library list: len=%d offset=%d, want 80", len(app.LibraryItems), app.libraryOffset)
		}
	})

	t.Run("sync_reload_after_delete_not_overwritten", func(t *testing.T) {
		app, _, cleanup := newTestUIApp(t)
		defer cleanup()
		seedUIFeed(t, app, 1)
		app.Navigate(RouteLibrary)
		app.LibrarySiteFilter = "all"

		itemA := library.ItemMetadata{ItemKey: "weibo:dynamic:keep_a", Site: "weibo", Title: "保留条目A"}
		itemB := library.ItemMetadata{ItemKey: "weibo:dynamic:del_b", Site: "weibo", Title: "已删条目B"}

		staleEntered := make(chan struct{})
		staleRelease := make(chan struct{})
		var staleOnce sync.Once
		var blockFirst atomic.Bool
		blockFirst.Store(true)
		var mu sync.Mutex
		currentDB := []library.ItemMetadata{itemA, itemB}
		defer func() {
			select {
			case <-staleRelease:
			default:
				close(staleRelease)
			}
		}()

		stub := &slowEventCountingBackend{
			BackendAPI: app.backend,
			listPageHook: func(f library.FeedFilter) ([]library.ItemMetadata, bool, error) {
				if f.Offset == 0 && blockFirst.CompareAndSwap(true, false) {
					staleOnce.Do(func() { close(staleEntered) })
					<-staleRelease
					return []library.ItemMetadata{itemA, itemB}, true, nil
				}
				mu.Lock()
				snap := append([]library.ItemMetadata(nil), currentDB...)
				mu.Unlock()
				return snap, true, nil
			},
		}
		app.backend = stub
		app.LibraryItems = []library.ItemMetadata{itemA, itemB}
		app.libraryOffset = 2

		mainQueue := make(chan func(), 64)
		app.BindEventLoop(func(fn func()) { mainQueue <- fn })

		// Start async reload that captures pre-delete snapshot [A, B].
		stub.emit(service.Event{Type: service.EventLibraryChanged, Site: "weibo", Message: "weibo:dynamic:pre"})
		fn := <-mainQueue
		fn()

		select {
		case <-staleEntered:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for pre-delete async ListLibraryItems to enter")
		}

		// User deletes itemB and synchronously reloads /library under the SAME filter.
		mu.Lock()
		currentDB = []library.ItemMetadata{itemA}
		mu.Unlock()
		app.reloadLibraryItems()
		if len(app.LibraryItems) != 1 || app.LibraryItems[0].ItemKey != itemA.ItemKey {
			t.Fatalf("expected post-delete list [keep_a], got %+v", app.LibraryItems)
		}

		// Release stale pre-delete snapshot [A, B] and drain apply callback.
		close(staleRelease)
		select {
		case applyFn := <-mainQueue:
			applyFn()
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for stale pre-delete apply callback")
		}

		if len(app.LibraryItems) != 1 || app.LibraryItems[0].ItemKey != itemA.ItemKey {
			t.Fatalf("stale pre-delete async snapshot resurrected deleted item: %+v", app.LibraryItems)
		}
	})
}

// TestBindEventLoopCoalescingPreservesArrivalOrderAcrossInterleavedKeys ensures
// coalescing moves a duplicate key to the tail instead of mutating its older slot
// in place, so interleaved related events (expired -> logged_in -> expired and
// progress -> checked -> progress) always leave the UI in the latest arrival state.
func TestBindEventLoopCoalescingPreservesArrivalOrderAcrossInterleavedKeys(t *testing.T) {
	t.Run("account_expired_logged_in_expired_keeps_final_expired", func(t *testing.T) {
		app, _, cleanup := newTestUIApp(t)
		defer cleanup()
		seedUIFeed(t, app, 1)
		app.Navigate(RouteHome)

		stub := &slowEventCountingBackend{BackendAPI: app.backend}
		app.backend = stub

		mainQueue := make(chan func(), 16)
		app.BindEventLoop(func(fn func()) { mainQueue <- fn })

		// Interleave expired -> logged_in -> expired before UI drains.
		stub.emit(service.Event{Type: service.EventAccountChanged, Site: "weibo", Message: "expired"})
		stub.emit(service.Event{Type: service.EventAccountChanged, Site: "weibo", Message: "logged_in"})
		stub.emit(service.Event{Type: service.EventAccountChanged, Site: "weibo", Message: "expired"})

		if len(mainQueue) != 1 {
			t.Fatalf("expected 1 coalesced flush callback, got %d", len(mainQueue))
		}
		fn := <-mainQueue
		fn()

		if !app.expiredNoticeShown["weibo"] || app.BannerError == "" {
			t.Fatalf("interleaved expired -> logged_in -> expired reversed order: expiredNoticeShown=%v BannerError=%q",
				app.expiredNoticeShown, app.BannerError)
		}
	})

	t.Run("subscription_progress_checked_progress_preserves_tail_order", func(t *testing.T) {
		app, _, cleanup := newTestUIApp(t)
		defer cleanup()
		seedUIFeed(t, app, 1)

		stub := &slowEventCountingBackend{BackendAPI: app.backend}
		app.backend = stub
		app.mu.Lock()
		app.pending["check-updates"] = true
		app.mu.Unlock()

		mainQueue := make(chan func(), 16)
		app.BindEventLoop(func(fn func()) { mainQueue <- fn })

		stub.emit(service.Event{Type: service.EventSubscriptionChecked, Message: "progress:1/5"})
		stub.emit(service.Event{Type: service.EventSubscriptionChecked, Message: "checked=5"})
		stub.emit(service.Event{Type: service.EventSubscriptionChecked, Message: "progress:2/7"})

		// Inspect queued order directly before drain: checked=5 must precede progress:2/7.
		app.eventMu.Lock()
		gotOrder := make([]string, len(app.pendingEvents))
		for i, ev := range app.pendingEvents {
			gotOrder[i] = ev.Message
		}
		app.eventMu.Unlock()
		if len(gotOrder) != 2 || gotOrder[0] != "checked=5" || gotOrder[1] != "progress:2/7" {
			t.Fatalf("coalesced queue order = %v, want [checked=5 progress:2/7]", gotOrder)
		}

		fn := <-mainQueue
		fn()
		if app.CheckDone != 2 || app.CheckTotal != 7 {
			t.Fatalf("CheckDone/CheckTotal = %d/%d, want 2/7", app.CheckDone, app.CheckTotal)
		}
	})
}
