package ui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/player"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/chialecode/trace/internal/uistate"
	"github.com/egoist/mygo/ui"
)

type spyCloseDecoder struct {
	*player.SynthDecoder
	closed atomic.Int32
}

func (d *spyCloseDecoder) Close() error {
	d.closed.Add(1)
	return d.SynthDecoder.Close()
}

type featuresAuditStub struct {
	service.BackendAPI

	mu               sync.Mutex
	livePreviewInfo  service.LiveRecordingPreviewInfo
	livePreviewOK    bool
	mediaPathCalls   []string
	upgradeCtxHasEnd bool
	upgradeErr       error
	upgradeCalls     int
	cacheDYCtxHasEnd bool
	cacheDYCalls     int
	liveStatusErr    error
	deleteSubErr     error
	deletedSubIDs    []string
	moveSubErr       error
	createdSubs      []subscription.Subscription
	mutateUIErr      error
	listLibLiveErr   error
	listLibLiveCalls atomic.Int32
	clearIncCalls    int
	enqueueQueueReqs []service.CacheQueueRequest
	statesOverride   map[string]library.DotState
	dyLoggedIn       bool
}

func (s *featuresAuditStub) StatusOf(siteID string) credential.Status {
	if normalizeSiteIDUI(siteID) == "douyin" && s.dyLoggedIn {
		return credential.Status{Site: "douyin", State: "logged_in", LoggedIn: true, SecUID: "sec_self"}
	}
	return s.BackendAPI.StatusOf(siteID)
}

func (s *featuresAuditStub) LiveRecordingPreview(recKey string) (service.LiveRecordingPreviewInfo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.livePreviewInfo, s.livePreviewOK
}

func (s *featuresAuditStub) MediaPath(itemKey, rel string) (string, error) {
	s.mu.Lock()
	s.mediaPathCalls = append(s.mediaPathCalls, itemKey+"::"+rel)
	s.mu.Unlock()
	return s.BackendAPI.MediaPath(itemKey, rel)
}

func (s *featuresAuditStub) Upgrade(ctx context.Context, itemKey string) (download.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upgradeCalls++
	s.upgradeCtxHasEnd = ctx.Done() != nil
	if s.upgradeErr != nil {
		return download.Task{}, s.upgradeErr
	}
	return download.Task{TaskID: "up-" + itemKey, Status: "queued"}, nil
}

func (s *featuresAuditStub) CacheDouyinWork(ctx context.Context, awemeID string) (download.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cacheDYCalls++
	s.cacheDYCtxHasEnd = ctx.Done() != nil
	return download.Task{TaskID: "dy-" + awemeID, Status: "queued"}, nil
}

func (s *featuresAuditStub) CacheDouyinWorkFor(ctx context.Context, awemeID, forUID, forName string) (download.Task, error) {
	return s.CacheDouyinWork(ctx, awemeID)
}

func (s *featuresAuditStub) LiveStatuses(ctx context.Context, accountKeys []string) ([]service.LiveAccountState, error) {
	s.mu.Lock()
	err := s.liveStatusErr
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return s.BackendAPI.LiveStatuses(ctx, accountKeys)
}

func (s *featuresAuditStub) CreateSubscription(name, note string, accounts []subscription.AccountRef, rules subscription.SubRules) (subscription.Subscription, error) {
	sub, err := s.BackendAPI.CreateSubscription(name, note, accounts, rules)
	if err == nil {
		s.mu.Lock()
		s.createdSubs = append(s.createdSubs, sub)
		s.mu.Unlock()
	}
	return sub, err
}

func (s *featuresAuditStub) MoveSubscriptionAccount(siteID, uid, targetSubID string) (subscription.Subscription, error) {
	s.mu.Lock()
	err := s.moveSubErr
	s.mu.Unlock()
	if err != nil {
		return subscription.Subscription{}, err
	}
	return s.BackendAPI.MoveSubscriptionAccount(siteID, uid, targetSubID)
}

func (s *featuresAuditStub) DeleteSubscription(subID string) error {
	s.mu.Lock()
	s.deletedSubIDs = append(s.deletedSubIDs, subID)
	err := s.deleteSubErr
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.BackendAPI.DeleteSubscription(subID)
}

func (s *featuresAuditStub) MutateUIState(fn func(*uistate.State)) (uistate.State, error) {
	s.mu.Lock()
	err := s.mutateUIErr
	s.mu.Unlock()
	if err != nil {
		return uistate.State{}, err
	}
	return s.BackendAPI.MutateUIState(fn)
}

func (s *featuresAuditStub) ListLibraryItems(f library.FeedFilter) ([]library.ItemMetadata, error) {
	if f.Kind == "live" {
		s.listLibLiveCalls.Add(1)
		s.mu.Lock()
		err := s.listLibLiveErr
		s.mu.Unlock()
		if err != nil {
			return nil, err
		}
	}
	return s.BackendAPI.ListLibraryItems(f)
}

func (s *featuresAuditStub) ClearLiveIncidents(itemKey, siteID, roomID, uid string) error {
	s.mu.Lock()
	s.clearIncCalls++
	s.mu.Unlock()
	return nil
}

func (s *featuresAuditStub) EnqueueCacheQueue(ctx context.Context, req service.CacheQueueRequest) (service.CacheQueueTask, error) {
	s.mu.Lock()
	s.enqueueQueueReqs = append(s.enqueueQueueReqs, req)
	s.mu.Unlock()
	return service.CacheQueueTask{ID: "cq-1", Site: req.Site, User: req.User, List: req.List, Status: service.CacheQueueQueued}, nil
}

func (s *featuresAuditStub) States(keys []string) map[string]library.DotState {
	s.mu.Lock()
	ov := s.statesOverride
	s.mu.Unlock()
	if ov != nil {
		out := make(map[string]library.DotState, len(keys))
		for _, k := range keys {
			if st, ok := ov[k]; ok {
				out[k] = st
			}
		}
		return out
	}
	return s.BackendAPI.States(keys)
}

func TestLivePreviewDecoderClosedOnAppCloseAndIsActiveStopsWhenBackendDrops(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)

	stub := &featuresAuditStub{
		BackendAPI: app.backend,
		livePreviewInfo: service.LiveRecordingPreviewInfo{
			RecordingKey:     "bilibili:live:9001",
			Site:             "bilibili",
			RoomID:           "9001",
			Uname:            "测试主播",
			Title:            "测试直播",
			Recording:        true,
			PartPath:         "part-001.mp4.part",
			PartIndex:        1,
			PreviewSupported: true,
		},
		livePreviewOK: true,
	}
	app.backend = stub

	posted := make(chan func(), 16)
	app.dispatch = func(fn func()) { posted <- fn }

	started := make(chan struct{})
	release := make(chan struct{})
	var gotOpts player.LivePreviewOptions
	dec := &spyCloseDecoder{SynthDecoder: player.NewSynthDecoder(16, 16, time.Second, 15)}

	app.livePreviewOpener = func(ctx context.Context, path string, opts player.LivePreviewOptions) (player.MediaDecoder, error) {
		gotOpts = opts
		close(started)
		<-release
		return dec, nil
	}

	app.StartLivePreview("bilibili:live:9001")
	<-started

	// While backend still reports the recording as active, IsActive is true.
	if !gotOpts.IsActive() {
		t.Fatal("expected IsActive() == true while backend reports active recording")
	}
	// Once backend removes the finished recording, IsActive immediately turns false.
	stub.mu.Lock()
	stub.livePreviewOK = false
	stub.mu.Unlock()
	if gotOpts.IsActive() {
		t.Fatal("expected IsActive() == false immediately after backend removes recording")
	}

	// Closing the App while open is still in flight must close the returned decoder cleanly.
	app.Close()
	close(release)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && dec.closed.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if dec.closed.Load() == 0 {
		t.Fatal("expected MediaDecoder.Close() to be called when App closes during async live preview open")
	}
}

func TestLibraryCoverlessVideoTileDoesNotReadMP4AsImage(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)

	videoTile := libraryTile{
		item: library.ItemMetadata{
			ItemKey: "bilibili:video:BV1NoCover",
			Site:    "bilibili",
			Kind:    "video",
			Title:   "无封面视频",
			Media:   []library.MediaFile{{Role: "video", Path: "P01.mp4"}},
		},
		file:  library.MediaFile{Role: "video", Path: "P01.mp4"},
		video: true,
	}
	if got := libraryTileCoverRel(videoTile); got != "" {
		t.Fatalf("coverless video tile cover rel = %q, want empty", got)
	}

	liveTile := libraryTile{
		item: library.ItemMetadata{
			ItemKey: "bilibili:live:9001",
			Site:    "bilibili",
			Kind:    "live",
			Title:   "无封面直播录像",
			Media:   []library.MediaFile{{Role: "live_segment", Path: "part-001.mp4"}},
		},
		file:  library.MediaFile{Role: "live_segment", Path: "part-001.mp4"},
		video: true,
	}
	if got := libraryTileCoverRel(liveTile); got != "" {
		t.Fatalf("coverless live tile cover rel = %q, want empty", got)
	}

	withCover := videoTile
	withCover.item.Media = append(withCover.item.Media, library.MediaFile{Role: "cover", Path: "cover.png"})
	if got := libraryTileCoverRel(withCover); got != "cover.png" {
		t.Fatalf("video tile with cover rel = %q, want cover.png", got)
	}

	// Render the coverless video item on /library and assert MediaPath is never called with P01.mp4.
	item := seedLibraryControlItem(t, app, library.ItemMetadata{
		Site: "bilibili", Kind: "video", ID: "BV1NoCoverRender", Title: "无封面渲染视频",
		Media: []library.MediaFile{{Role: "video", Path: "P01.mp4"}},
	})
	stub := &featuresAuditStub{BackendAPI: app.backend}
	app.backend = stub
	app.Navigate(RouteLibrary)
	app.reloadLibraryItems()
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()

	stub.mu.Lock()
	calls := append([]string(nil), stub.mediaPathCalls...)
	stub.mu.Unlock()
	for _, call := range calls {
		if strings.HasSuffix(call, "::P01.mp4") {
			t.Fatalf("library tile passed raw video path to image lookup: %s (item=%s)", call, item.ItemKey)
		}
	}
}

func TestUpgradeCandidateAsyncReportsErrorAndRemovesOnSuccess(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)

	stub := &featuresAuditStub{
		BackendAPI: app.backend,
		upgradeErr: errors.New("模拟升级失败"),
	}
	app.backend = stub
	app.UpgradeCandidates = []library.ItemMetadata{{
		ItemKey:     "bilibili:video:BV1Up01",
		RawID:       "BV1Up01",
		Title:       "待升级视频",
		UpgradeHint: "可升至 4K",
	}}
	app.Navigate(RouteTasks)
	app.selectTasksDownloads()
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()

	// 1. Failure surfaces error and preserves candidate.
	if err := tt.Click("升级缓存：BV1Up01"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !stub.upgradeCtxHasEnd {
		t.Fatal("expected Upgrade to receive cancellable app context, not context.Background()")
	}
	if !strings.Contains(app.TaskActionError, "模拟升级失败") || !strings.Contains(app.BannerError, "模拟升级失败") {
		t.Fatalf("expected upgrade error in TaskActionError & BannerError, got task=%q banner=%q", app.TaskActionError, app.BannerError)
	}
	if len(app.UpgradeCandidates) != 1 {
		t.Fatalf("expected failed upgrade candidate to stay listed, got %d", len(app.UpgradeCandidates))
	}

	// 2. Success clears error and removes candidate.
	stub.upgradeErr = nil
	if err := tt.Click("升级缓存：BV1Up01"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(app.UpgradeCandidates) != 0 {
		t.Fatalf("expected upgraded candidate to be removed, got %d", len(app.UpgradeCandidates))
	}
	if app.TaskActionError != "" {
		t.Fatalf("expected TaskActionError cleared on success, got %q", app.TaskActionError)
	}
}

func TestCacheDouyinWorkAsyncUsesAppContext(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)

	stub := &featuresAuditStub{BackendAPI: app.backend}
	app.backend = stub

	posted := make(chan func(), 8)
	app.dispatch = func(fn func()) { posted <- fn }

	app.cacheDouyinWorkNow("7399001")
	select {
	case fn := <-posted:
		fn()
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for async cacheDouyinWorkNow callback")
	}
	if stub.cacheDYCalls != 1 || !stub.cacheDYCtxHasEnd {
		t.Fatalf("expected async CacheDouyinWork with app ctx, calls=%d ctxHasEnd=%v", stub.cacheDYCalls, stub.cacheDYCtxHasEnd)
	}
	if !strings.Contains(app.BannerInfo, "7399001") {
		t.Fatalf("expected BannerInfo to confirm enqueue, got %q", app.BannerInfo)
	}
}

func TestMineClearSiteRemovesOrphanFolderSelectionHandlesEmptySiteAndNewAccountFolders(t *testing.T) {
	app, stub, cleanup := newMineApp(t)
	defer cleanup()

	// Populate folders and select a private folder, then simulate failed refresh deleting Folders[fk]
	// while Folder[fk] still holds the private folder ID.
	app.Mine.ensure()
	fk := mineFnKey("bilibili", service.AccountFavorites)
	app.Mine.Folders[fk] = []service.AccountFolder{{ID: "priv-123", Title: "私密夹"}}
	app.Mine.Folder[fk] = "priv-123"
	app.Mine.owner["bilibili"] = app.mineAccountID("bilibili")
	delete(app.Mine.Folders, fk)

	// Clearing site must still purge orphaned Mine.Folder[fk].
	app.mineClearSite("bilibili")
	if got := app.Mine.Folder[fk]; got != "" {
		t.Fatalf("orphaned private folder selection survived mineClearSite: %q", got)
	}

	// mineOnAccountChanged("") must check all sites and clear signed-out ones.
	app.Mine.owner["douyin"] = app.mineAccountID("douyin")
	app.Mine.Lists["douyin/likes/"] = &mineList{Loaded: true}
	stub.mu.Lock()
	stub.logged["douyin"] = false
	stub.mu.Unlock()
	app.mineOnAccountChanged("")
	if len(app.Mine.Lists) != 0 {
		t.Fatalf("mineOnAccountChanged(\"\") did not clear signed-out douyin lists: %+v", app.Mine.Lists)
	}

	// Switching accounts while old folder request is in flight must not deduplicate/drop new account's folder load.
	posted := make(chan func(), 16)
	app.dispatch = func(fn func()) { posted <- fn }
	blockOld := make(chan struct{})
	stub.mu.Lock()
	stub.logged["bilibili"] = true
	stub.mu.Unlock()
	app.Mine.ensure()
	// Hold first AccountFolders call by registering a pending key for the old gen directly.
	oldGen := app.Mine.clearGen
	app.mu.Lock()
	app.pending["mine-folders:"+fk+"#"+strings.TrimSpace(string(rune('0'+oldGen)))] = true
	app.mu.Unlock()
	_ = blockOld
	app.mineClearSite("bilibili")
	beforeN := stub.folderN[fk]
	app.mineLoadFolders("bilibili", service.AccountFavorites)
	select {
	case fn := <-posted:
		fn()
	case <-time.After(3 * time.Second):
		t.Fatal("new account mineLoadFolders was dropped behind old clearGen pending key")
	}
	if stub.folderN[fk] != beforeN+1 {
		t.Fatalf("expected new account AccountFolders call, before=%d after=%d", beforeN, stub.folderN[fk])
	}

	// Duplicate folder display names must be disambiguated for ui.Select.
	names := mineFolderDisplayNames([]service.AccountFolder{
		{ID: "f1", Title: "默认收藏夹", Count: 5},
		{ID: "f2", Title: "默认收藏夹", Count: 5},
	})
	if len(names) != 2 || names[0] == names[1] {
		t.Fatalf("expected disambiguated folder names, got %v", names)
	}
}

func TestSubscriptionGridColsMonotonicAcrossWidths(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)
	app.Navigate(RouteSubscriptions)

	measure := func(w int) int {
		var cols int
		tt := ui.NewTester(func(c *ui.Context) {
			cols = app.subscriptionGridCols(c)
		}, w, 860)
		tt.Frame()
		return cols
	}
	c720 := measure(720)
	c1000 := measure(1000)
	c1280 := measure(1280)
	if !(c720 <= c1000 && c1000 < c1280 && c1000 == 4 && c1280 == 5) {
		t.Fatalf("subscriptionGridCols not monotonic (want 3/4/5): 720=%d 1000=%d 1280=%d", c720, c1000, c1280)
	}
}

func TestMultiAccountSubscriptionLivePriorityAndGridBadge(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)

	sub := subscription.Subscription{
		ID:   "sub-multi",
		Name: "跨站多账号主播",
		Mid:  1001,
		Accounts: []subscription.AccountRef{
			{Site: "bilibili", UID: "1001", Name: "离线B站号"},
			{Site: "bilibili", UID: "1002", Name: "开播B站号"},
			{Site: "douyin", UID: "sec_rec", Name: "录制中抖音号"},
		},
	}
	app.Subscriptions = []subscription.Subscription{sub}
	app.subsRev++
	app.LiveRooms = []live.RoomState{
		{UID: 1001, RoomID: 9001, LiveStatus: 0, Recording: false},
		{UID: 1002, RoomID: 9002, LiveStatus: 1, Recording: false},
	}
	app.LiveAccounts = []service.LiveAccountState{
		{Site: "douyin", AccountKey: "douyin:sec_rec", RecordingKey: "douyin:live:777", UID: "sec_rec", LiveStatus: 1, Recording: true},
	}

	rm := app.findSubscriptionLiveRoom(sub)
	if rm == nil || rm.RoomID != 9002 {
		t.Fatalf("findSubscriptionLiveRoom picked %+v, want live room 9002 over offline room 9001", rm)
	}
	if !app.isSubscriptionLive(sub) {
		t.Fatal("expected isSubscriptionLive == true")
	}

	app.Navigate(RouteSubscriptions)
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	if _, ok := tt.Find("网格停止录制 douyin:sec_rec"); !ok {
		t.Fatal("expected grid badge to reflect recording Douyin account rather than non-recording Bilibili room")
	}
}

func TestDouyinLiveRefreshErrorDoesNotStopBilibiliVisibleLive(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)

	stub := &featuresAuditStub{
		BackendAPI:    app.backend,
		dyLoggedIn:    true,
		liveStatusErr: &site.Error{Site: site.Douyin, Kind: site.ErrKindRateLimited, Message: "抖音限流"},
	}
	app.backend = stub
	app.Navigate(RouteSubscriptions)
	app.visibleLiveStopped = false

	app.refreshSingleAccountLive("douyin", "sec_err")
	if app.visibleLiveStopped {
		t.Fatal("Douyin live refresh error must NOT set visibleLiveStopped for Bilibili")
	}
	if !strings.Contains(app.BannerError, "抖音限流") {
		t.Fatalf("expected Douyin refresh error in BannerError, got %q", app.BannerError)
	}
}

func TestSubscriptionDeleteFailureKeepsDetailAndMoveFailureRollsBack(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)

	sub, err := app.backend.CreateSubscription("保留订阅", "", []subscription.AccountRef{
		{Site: "bilibili", UID: "5501", Name: "保留UP"},
	}, subscription.DefaultSubRules())
	if err != nil {
		t.Fatal(err)
	}
	app.RefreshData()

	stub := &featuresAuditStub{
		BackendAPI:   app.backend,
		deleteSubErr: errors.New("模拟删除失败"),
	}
	app.backend = stub

	// 1. DeleteSubscription error keeps detail open & selection intact.
	app.OpenSubscriptionDetail(sub)
	app.SubDeleteID = sub.ID
	app.SubManageModalMode = "delete"
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	if err := tt.Click("确认删除订阅"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !app.SubDetailOpen || app.SelectedSubID != sub.ID {
		t.Fatalf("delete failure must keep detail open: open=%v id=%q", app.SubDetailOpen, app.SelectedSubID)
	}
	if !strings.Contains(app.BannerError, "模拟删除失败") {
		t.Fatalf("expected delete error in BannerError, got %q", app.BannerError)
	}

	// 2. MoveSubscriptionAccount failure in "创建并移到新订阅" rolls back newly created subscription.
	stub.deleteSubErr = nil
	stub.deletedSubIDs = nil
	stub.moveSubErr = errors.New("模拟迁移失败")
	app.AddSubDialogOpen = true
	app.AddSubName = "新迁移目标"
	app.AddSubPreview = &subscription.AccountRef{Site: "bilibili", UID: "5501", Name: "保留UP"}
	app.AddSubPreviewOwner = &sub
	tt.Frame()
	if err := tt.Click("创建并移到新订阅"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !app.AddSubDialogOpen {
		t.Fatal("dialog must remain open when MoveSubscriptionAccount fails")
	}
	if len(stub.createdSubs) == 0 || len(stub.deletedSubIDs) == 0 || stub.deletedSubIDs[len(stub.deletedSubIDs)-1] != stub.createdSubs[len(stub.createdSubs)-1].ID {
		t.Fatalf("expected rollback DeleteSubscription on created orphan sub: created=%+v deleted=%+v", stub.createdSubs, stub.deletedSubIDs)
	}
}

func TestCacheAllHasMoreWhenPageOneErroredAndPerUserPendingKey(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)

	stub := &featuresAuditStub{BackendAPI: app.backend}
	app.backend = stub

	// Bilibili dynamics: empty slice + page 1 error -> HasMore must be true.
	app.CurrentUser = &bilibili.UserSpace{Mid: 7001, Name: "失败分页UP"}
	app.UserTab = "dynamics"
	app.UserDynamics = nil
	app.UserDynamicsHasMore = false
	app.UserDynamicsError = "请求触发风控"
	app.enqueueCacheAll("bilibili")

	// Douyin works: empty slice + page 1 error -> HasMore must be true.
	app.CurrentDouyinUser = &service.DouyinUserView{SecUID: "sec_fail_1", Nickname: "失败分页抖音"}
	app.CurrentDouyinWorks = nil
	app.DouyinWorksHasMore = false
	app.DouyinWorksError = "请求失败"
	app.enqueueCacheAll("douyin")

	if len(stub.enqueueQueueReqs) != 2 {
		t.Fatalf("expected 2 EnqueueCacheQueue requests, got %d", len(stub.enqueueQueueReqs))
	}
	if !stub.enqueueQueueReqs[0].HasMore || !stub.enqueueQueueReqs[1].HasMore {
		t.Fatalf("expected HasMore=true when page 1 errored: %+v", stub.enqueueQueueReqs)
	}
}

func TestLiveIncidentsDismissRollbackClearPreservesDouyinAndMemoizedSessions(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)

	stub := &featuresAuditStub{
		BackendAPI:     app.backend,
		mutateUIErr:    errors.New("写入失败"),
		listLibLiveErr: errors.New("查询失败"),
	}
	app.backend = stub

	// 1. dismissLiveSessions must not mutate in-memory dismissedLive on MutateUIState error.
	app.dismissLiveSessions("douyin:live:888")
	if app.dismissedLive["douyin:live:888"] {
		t.Fatal("dismissLiveSessions mutated dismissedLive despite MutateUIState error")
	}

	// 2. localLiveLibraryItems caches errors for the current rev instead of re-querying SQLite every frame.
	app.liveLibItemsCacheRev = 0
	_ = app.localLiveLibraryItems()
	_ = app.localLiveLibraryItems()
	_ = app.localLiveLibraryItems()
	if got := stub.listLibLiveCalls.Load(); got != 1 {
		t.Fatalf("expected localLiveLibraryItems to cache error per rev (1 query), got %d", got)
	}

	// 3. clearLiveSessionIncidents preserves kept Douyin live snapshots via RefreshData().
	stub.listLibLiveErr = nil
	app.LiveAccounts = []service.LiveAccountState{
		{Site: "douyin", AccountKey: "douyin:sec_keep", UID: "sec_keep", LiveStatus: 1, Title: "保留的抖音直播"},
	}
	app.clearLiveSessionIncidents(liveIncidentSession{Site: "bilibili", RoomID: "9001", UID: "20001"})
	foundKept := false
	for _, st := range app.LiveAccounts {
		if st.Site == "douyin" && st.UID == "sec_keep" {
			foundKept = true
			break
		}
	}
	if !foundKept {
		t.Fatal("clearLiveSessionIncidents wiped previously fetched Douyin LiveAccounts snapshot")
	}
}

func TestCloseLivePreviewForKeyMatchesBilibiliRoomIDAndPreservesMuteOnRotate(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)

	// 1. closeLivePreviewForKey resolves roomID from LiveAccounts to match "bilibili:live:<roomID>".
	app.LivePreviewKey = "bilibili:live:9002"
	app.LiveAccounts = []service.LiveAccountState{
		{Site: "bilibili", AccountKey: "bilibili:20001", UID: "20001", RoomID: "9002", Recording: true},
	}
	app.closeLivePreviewForKey("bilibili:20001")
	if app.LivePreviewKey != "" {
		t.Fatalf("closeLivePreviewForKey did not close bilibili:live:9002 preview: %q", app.LivePreviewKey)
	}

	// 2. Segment rotation via syncLivePreviewState preserves user's mute override.
	stub := &featuresAuditStub{
		BackendAPI: app.backend,
		livePreviewInfo: service.LiveRecordingPreviewInfo{
			RecordingKey:     "bilibili:live:9002",
			Site:             "bilibili",
			RoomID:           "9002",
			Recording:        true,
			PartPath:         "part-001.mp4.part",
			PartIndex:        1,
			PreviewSupported: true,
		},
		livePreviewOK: true,
	}
	app.backend = stub
	app.livePreviewOpener = func(ctx context.Context, path string, opts player.LivePreviewOptions) (player.MediaDecoder, error) {
		return player.NewSynthDecoder(16, 16, time.Second, 15), nil
	}
	app.StartLivePreview("bilibili:live:9002")
	// User toggles mute on inside the live preview card.
	st := app.fs()
	st.livePreviewMutedSet = true
	st.livePreviewMuted = true
	app.livePreviewPlayer.SetMuted(true)

	// Backend rotates to part-002.mp4.part.
	stub.mu.Lock()
	stub.livePreviewInfo.PartPath = "part-002.mp4.part"
	stub.livePreviewInfo.PartIndex = 2
	stub.mu.Unlock()

	app.syncLivePreviewState()
	if !app.livePreviewPlayer.Snapshot(1, 1).Muted {
		t.Fatal("segment rotation reset live preview mute preference")
	}
}

func TestFormatPlanTimeDSTAndEarliestMixedOffset(t *testing.T) {
	// 23-hour spring-forward boundary simulation: local calendar day delta must still be "明天".
	// Even with standard local zone, 23 hours across midnight is the next calendar day.
	now := time.Date(2026, 3, 8, 2, 30, 0, 0, time.Local)
	nextDay23h := now.Add(23 * time.Hour)
	if got := formatPlanTime(nextDay23h.UTC().Format(time.RFC3339), now); !strings.HasPrefix(got, "明天 ") {
		t.Fatalf("formatPlanTime(+23h across midnight) = %q, want 明天 ...", got)
	}

	// Mixed RFC3339 offsets: 2026-11-18T12:00:00+08:00 (04:00Z) is EARLIER than 2026-11-18T05:00:00Z,
	// even though lexicographically "2026-11-18T05:00:00Z" < "2026-11-18T12:00:00+08:00".
	rows := []service.AccountCheckRow{
		{Plan: subscription.PlanScheduled, NextAt: "2026-11-18T05:00:00Z"},
		{Plan: subscription.PlanScheduled, NextAt: "2026-11-18T12:00:00+08:00"},
	}
	if got := earliestScheduledNextAt(rows); got != "2026-11-18T12:00:00+08:00" {
		t.Fatalf("earliestScheduledNextAt(mixed offsets) = %q, want 2026-11-18T12:00:00+08:00", got)
	}
}

func TestSubscribeAsNewClampsLongAccountName(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)

	longName := strings.Repeat("长昵称测试", 20) // 100 runes > 64-rune limit
	app.OpenSubscribePicker(subscription.AccountRef{Site: "douyin", UID: "sec_long", Name: longName})
	app.subscribeAsNew()
	if app.SubscribePicker.Open || app.SubscribePicker.Error != "" {
		t.Fatalf("subscribeAsNew with long name failed: open=%v err=%q", app.SubscribePicker.Open, app.SubscribePicker.Error)
	}
	owner, ok := app.ownerOfAccountInMemory("douyin", "sec_long", 0)
	if !ok {
		t.Fatal("expected newly created subscription for sec_long")
	}
	if n := utf8.RuneCountInString(owner.Name); n > 64 {
		t.Fatalf("clamped subscription name exceeded 64 runes: %d", n)
	}
}

func TestDouyinWorkDetailShowsActiveDownloadDotAndDisablesButton(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)

	itemKey := library.CanonicalItemKey("douyin", "video", "7399099")
	stub := &featuresAuditStub{
		BackendAPI: app.backend,
		statesOverride: map[string]library.DotState{
			itemKey: {Dot: "blue", Label: "下载中"},
		},
	}
	app.backend = stub
	app.CurrentLibraryDetail = nil
	app.CurrentDouyinWork = &service.DouyinWorkView{
		ID:       "7399099",
		Kind:     "video",
		Title:    "正在下载的抖音视频",
		CacheKey: itemKey,
		Author:   service.DouyinAuthorDTO{SecUID: "sec_dl", Nickname: "作者A"},
	}
	app.CurrentRoute = RouteDouyinWork

	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	if _, ok := tt.Find("抖音作品缓存状态"); !ok {
		t.Fatal("expected active download status dot on /douyin/work")
	}
	if !tt.HasText("缓存中…") {
		t.Fatal("expected cache button to show 缓存中… while download is active")
	}
	// Clicking disabled button must not enqueue a duplicate download.
	_ = tt.Click("缓存视频")
	tt.Frame()
	if stub.cacheDYCalls != 0 {
		t.Fatalf("clicking disabled cache button during active download called CacheDouyinWork %d times", stub.cacheDYCalls)
	}
}

func TestFeatureStateCachesInvalidateOnMiddleElementMutation(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	seedUIFeed(t, app, 0)

	// 1. libraryTiles(): [A, X, B] -> [A, Y, B] without incrementing cacheStateRev.
	itemA := library.ItemMetadata{
		ItemKey: "bilibili:dynamic:A", Title: "A",
		Media: []library.MediaFile{{Role: "image", Path: "a.png"}},
	}
	itemX := library.ItemMetadata{
		ItemKey: "bilibili:dynamic:X", Title: "X",
		Media: []library.MediaFile{{Role: "image", Path: "x.png"}},
	}
	itemY := library.ItemMetadata{
		ItemKey: "bilibili:dynamic:Y", Title: "Y",
		Media: []library.MediaFile{{Role: "image", Path: "y.png"}},
	}
	itemB := library.ItemMetadata{
		ItemKey: "bilibili:dynamic:B", Title: "B",
		Media: []library.MediaFile{{Role: "image", Path: "b.png"}},
	}
	app.LibraryItems = []library.ItemMetadata{itemA, itemX, itemB}
	tiles1 := app.libraryTiles()
	if len(tiles1) != 3 || tiles1[1].item.ItemKey != "bilibili:dynamic:X" {
		t.Fatalf("initial libraryTiles = %+v, want middle X", tiles1)
	}
	app.LibraryItems = []library.ItemMetadata{itemA, itemY, itemB}
	tiles2 := app.libraryTiles()
	if len(tiles2) != 3 || tiles2[1].item.ItemKey != "bilibili:dynamic:Y" {
		t.Fatalf("stale libraryTiles returned after middle-element replacement without cacheStateRev bump: got %q, want Y", tiles2[1].item.ItemKey)
	}

	// 2. filteredSubscriptions() & subscriptionAccountRows(): middle element mutated without subsRev bump.
	app.Subscriptions = []subscription.Subscription{
		{ID: "sub-a", Name: "甲", Accounts: []subscription.AccountRef{{Site: "bilibili", UID: "1", Name: "甲"}}},
		{ID: "sub-x", Name: "旧中间", Accounts: []subscription.AccountRef{{Site: "bilibili", UID: "2", Name: "旧中间"}}},
		{ID: "sub-b", Name: "乙", Accounts: []subscription.AccountRef{{Site: "bilibili", UID: "3", Name: "乙"}}},
	}
	f1 := app.filteredSubscriptions("")
	r1 := app.subscriptionAccountRows("all")
	if len(f1) != 3 || f1[1].Name != "旧中间" || len(r1) != 3 {
		t.Fatalf("initial subs cache mismatch: f=%d r=%d", len(f1), len(r1))
	}
	app.Subscriptions[1] = subscription.Subscription{
		ID: "sub-y", Name: "新中间", Accounts: []subscription.AccountRef{{Site: "bilibili", UID: "9", Name: "新中间"}},
	}
	f2 := app.filteredSubscriptions("")
	if len(f2) != 3 || f2[1].ID != "sub-y" {
		t.Fatalf("stale filteredSubscriptions after middle mutation: got %q, want sub-y", f2[1].ID)
	}
	r2 := app.subscriptionAccountRows("all")
	foundNew := false
	for _, row := range r2 {
		if row.UID == "9" && row.Name == "新中间" {
			foundNew = true
			break
		}
	}
	if !foundNew {
		t.Fatalf("stale subscriptionAccountRows after middle mutation: %+v", r2)
	}
}
