package ui

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/player"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

// Application route constants.
const (
	RouteHome          = "/home"
	RouteSubscriptions = "/subscriptions"
	RouteParse         = "/parse"
	RouteVideo         = "/video"
	RouteUser          = "/user"
	RouteLibrary       = "/library"
	RouteTasks         = "/tasks"
	RouteSettings      = "/settings"
	RouteLogin         = "/login"
)

// ImageViewerState holds state for the in-app image overlay (C14, R6).
type ImageViewerState struct {
	Open           bool
	URLs           []string
	LocalPaths     []string
	ItemKey        string
	RelPaths       []string
	Motions        []string // per image: its live photo motion clip, if cached
	PostKey        string
	Index          int
	Zoom           float32
	PanX           float32
	PanY           float32
	Fit            bool
	Dragging       bool
	PointerX       float32
	PointerY       float32
	Bitmaps        []*ui.Bitmap
	Errors         []string
	LastActiveAt   time.Time
	ControlsHidden bool
	LastHoverPos   ui.Point
}

// PlayerOverlayState holds state for the in-app media player overlay (C22, R6).
type PlayerOverlayState struct {
	Open            bool
	ItemKey         string
	Title           string
	MediaPath       string
	Loading         bool
	LoadError       string
	Segment         int
	Segments        []string
	DecoderMissing  bool
	MissingReason   string
	SeekNormalized  float64
	DanmakuEnabled  bool
	DanmakuOpacity  float64
	DanmakuArea     string
	DanmakuDensity  string
	Volume          float64
	VolumeMenuOpen  bool
	SubtitleLang    string
	SubtitleOptions []string
	MoreMenuOpen    bool
	Fullscreen      bool
	LastActiveAt    time.Time
	ControlsHidden  bool
	LastHoverPos    ui.Point
}

// DeleteConfirmState holds state for the library item deletion confirmation modal (C26).
type DeleteConfirmState struct {
	Open        bool
	Item        library.ItemMetadata
	Plan        library.DeletePlan
	FileCount   int
	TotalBytes  int64
	DeleteFiles bool
}

// HomeUpdatedActor represents a subscription or unsubscribed creator with new cached content after the last viewed timestamp on Home (R9, DEC-037).
type HomeUpdatedActor struct {
	Key      string
	SubID    string
	Mid      int64
	Name     string
	Face     string
	Authors  []int64
	LatestTs int64
}

// App manages the MyGo immediate-mode UI tree and state for TRACE.
type App struct {
	mu                  sync.Mutex
	ctx                 context.Context
	cancel              context.CancelFunc
	dispatch            func(func())
	pending             map[string]bool
	closed              bool
	fullscreen          func(bool)
	imageViewerRevision uint64
	imageViewerCancel   context.CancelFunc
	runtimeDiagnostics  service.Diagnostics
	runtimeLogLines     []string
	runtimeLogError     string
	runtimeLogsNextRead time.Time
	visibleLiveNextRead map[int64]time.Time
	visibleLiveStopped  bool
	backend             service.BackendAPI
	player              *player.Controller
	unsub               func()

	// In-memory bitmap cache for local covers/thumbnails, freed on CloseToTray (DEC-030)
	imageCache map[string]*ui.Bitmap
	imageKeys  []string
	imageRetry map[string]imageRetry
	// homeBaselineTs is the newest post seen before this session (the
	// "有更新" baseline); homeSeenTs is the newest seen so far.
	homeBaselineTs     int64
	homeSeenTs         int64
	homeBaselineLoaded bool
	// postCards caches the card built for each feed entry or dynamic, so
	// scrolling does not query the library for every visible row on every
	// frame. Cleared whenever tasks or the library change.
	postCards  map[string]PostCardData
	imageQueue chan struct{}

	// Navigation & Auth Guard (C06, C07)
	CurrentRoute  string
	SavedRoute    string
	PreviousRoute string
	BannerError   string
	BannerInfo    string

	// Expandable post/description states & PostCard "⋯" menu (R8)
	ExpandedPosts map[string]bool
	PostMenuKey   string

	// Quick Parse & Parse Page (C01..C03, C38, R11)
	QuickParseInput         string
	QuickParseComposing     bool
	ParsePageInput          string
	ParseInputComposing     bool
	ParseSubmitting         bool
	LastParseError          string
	HistoryItems            []storage.HistoryEntry
	HistoryHeaderMenuOpen   bool
	HistoryClearConfirmOpen bool
	HistoryItemMenuID       string

	// Login Page (C04, C05)
	LoginTab           string // "qr" | "cookie"
	QRSession          bilibili.QRSession
	QRBitmap           *ui.Bitmap
	QRStatusText       string
	ManualCookieInput  string
	ManualRefreshInput string
	LoginError         string
	qrRevision         uint64
	qrCancel           context.CancelFunc

	// Home Feed (C24, C38, R9, R10)
	FeedItems                 []library.FeedEntry
	FeedListState             ui.ListState
	FeedScroll                ui.ScrollState
	feedMoreBefore            bool
	feedMoreAfter             bool
	FeedError                 string
	feedFailedBefore          bool
	FeedSortOrder             string  // always "newest" (R10)
	FeedFilterScope           string  // "all" | "cached" | "unread"
	FeedAuthorsFilter         []int64 // optional creator filter
	HomeActorFilter           *HomeUpdatedActor
	savedMainFeedItems        []library.FeedEntry
	savedMainFeedListState    ui.ListState
	savedMainFeedScrollY      float32
	savedMainFeedMoreBefore   bool
	savedMainFeedMoreAfter    bool
	savedMainFeedError        string
	savedMainFeedFailedBefore bool

	// Video & Dynamic Detail (C12, C15, C20, R8, R12)
	CurrentVideo         *bilibili.VideoDetail
	CurrentVideoItemKey  string
	CurrentDynamic       *bilibili.DynamicItem
	CurrentLibraryDetail *library.ItemMetadata
	SelectedPartPages    map[int]bool
	UpgradeAvailable     bool

	// Opening is the link being opened from a card or entry: its page shows
	// at once with what the card knew, and fills in when OpenLink returns
	// (F-057). openSeq makes the newest click win.
	// panelShown: this frame has a context panel beside the rail.
	panelShown bool
	// SubPanelListState scrolls the subscriptions in the context panel.
	SubPanelListState ui.ListState

	Opening       *OpeningPreview
	openSeq       int
	playerOpenSeq int

	// User Space Page (C12, C13)
	CurrentUser         *bilibili.UserSpace
	UserTab             string // "dynamics" | "videos"
	UserDynamics        []bilibili.DynamicItem
	UserDynamicsNext    string
	UserDynamicsHasMore bool
	UserDynamicsError   string
	UserVideos          []bilibili.UserVideoItem
	UserVideosPage      int
	UserVideosHasMore   bool
	UserVideosError     string
	UserDynListState    ui.ListState
	UserVidListState    ui.ListState
	UserSubMenuOpen     bool

	// Subscriptions Page (C29, C30, C31, C34, R1, R2)
	Subscriptions      []subscription.Subscription
	SubListState       ui.ListState
	SubSearchQuery     string
	subListQuery       string
	CheckDone          int
	CheckTotal         int
	SelectedSubID      string
	SelectedSubMid     int64
	SubDetailOpen      bool
	SubMoreMenuOpen    bool
	SubManageMenuOpen  bool
	SubManageModalMode string // "" | "rename" | "note" | "avatar" | "delete"
	SubAvatarInput     string
	SubDetailTab       string // "live" | "accounts"
	SubRuleMenuOpen    bool
	// RuleDraft is the cache rule being edited in Settings (ID "" = new).
	RuleDraft           *subscription.CacheRule
	RuleError           string
	AddSubDialogOpen    bool
	AddSubInput         string
	AddSubName          string
	AddSubPreview       *subscription.AccountRef
	AddSubPreviewOwner  *subscription.Subscription
	AddSubPreviewInput  string
	SubName             string
	SubNote             string
	SubAccountInput     string
	SubDeleteID         string
	SubRules            subscription.SubRules
	AccountPreview      *subscription.AccountRef
	AccountPreviewSubID string
	AccountPreviewOwner string
	AddSubScope         string
	AddSubAutoCache     string
	ImportSubModalOpen  bool
	ImportFollowings    []bilibili.FollowingUser
	ImportPage          int
	ImportHasMore       bool
	ImportGroup         string
	ImportGroups        []bilibili.RelationTag
	ImportSearch        string
	ImportError         string
	ImportSelectedMids  map[int64]bool
	SubRuleScope        string
	SubRuleAutoCache    string
	SubRuleLiveNotify   bool
	SubRuleLiveAutoRec  bool
	LiveRooms           []live.RoomState

	// Library Page (C25, C26)
	LibraryItems      []library.ItemMetadata
	LibraryListState  ui.ListState
	LibraryKindFilter string // "" | "image" | "video" | "dynamic" | "live"
	LibraryCreatorMid int64
	LibrarySubFilter  string
	LibrarySort       string // "cached_at" | "pub_ts"
	LibrarySearch     string
	LibraryHasMore    bool
	LibraryError      string
	libraryStatistics library.Statistics
	libraryOffset     int
	LibraryAuthorMenu bool
	ExpandedItemKey   string
	DeleteConfirm     DeleteConfirmState

	// Tasks Page (C17, C18, C20, C23, R7)
	Tasks               []download.Task
	ExpandedTaskID      string
	TaskRemoveConfirmID string
	TaskActionError     string
	UpgradeCheckBusy    bool
	UpgradeCandidates   []library.ItemMetadata
	UpgradeDone         int
	UpgradeTotal        int
	upgradeCancel       context.CancelFunc

	// Settings Page (R5)
	SettingsCategory    string // "account" | "download" | "quality" | "subscription" | "storage" | "general" | "logs" | "about"
	DraftSettings       settings.Settings
	CacheDirInput       string
	AddLibraryRootInput string
	MoveExistingFiles   bool
	ConfirmLogoutOpen   bool

	// Overlays (C14, C22)
	ImageViewer   ImageViewerState
	PlayerOverlay PlayerOverlayState
}

// NewApp creates a new TRACE UI application bound to backend.
// In tests and tools, audioSink is Player NullAudioSink so no audio device is opened (§6.1).
func NewApp(backend service.BackendAPI, audioSink player.AudioSink) *App {
	if audioSink == nil {
		audioSink = player.NewMyGoAudioSink()
	}
	s := backend.GetSettings()
	uiSt := backend.GetUIState()

	ctrl := player.NewController(player.Options{
		AudioSink:      audioSink,
		DanmakuVisible: uiSt.Player.DanmakuVisible,
		DanmakuOpacity: uiSt.Player.DanmakuOpacity,
		DanmakuArea:    uiSt.Player.DanmakuArea,
		DanmakuDensity: uiSt.Player.DanmakuDensity,
		Volume:         uiSt.Player.Volume,
		Muted:          uiSt.Player.Muted,
		SubtitleLang:   uiSt.Player.SubtitleLang,
	})

	initialRoute := uiSt.LastRoute
	if initialRoute == "" || !strings.HasPrefix(initialRoute, "/") {
		initialRoute = RouteHome
	}

	ctx, cancel := context.WithCancel(context.Background())
	app := &App{
		ctx:                ctx,
		cancel:             cancel,
		pending:            make(map[string]bool),
		imageCache:         make(map[string]*ui.Bitmap),
		imageQueue:         make(chan struct{}, 2),
		ExpandedPosts:      make(map[string]bool),
		backend:            backend,
		player:             ctrl,
		CurrentRoute:       initialRoute,
		LoginTab:           "qr",
		FeedSortOrder:      "newest",
		FeedFilterScope:    "cached",
		SelectedPartPages:  make(map[int]bool),
		UserTab:            "dynamics",
		SubDetailTab:       "live",
		AddSubScope:        "both",
		AddSubAutoCache:    "inherit",
		ImportSelectedMids: make(map[int64]bool),
		LibrarySort:        "cached_at",
		SettingsCategory:   "account",
		DraftSettings:      s,
		CacheDirInput:      s.CacheDir,
		MoveExistingFiles:  true,
		PlayerOverlay: PlayerOverlayState{
			DanmakuEnabled:  uiSt.Player.DanmakuVisible,
			DanmakuOpacity:  uiSt.Player.DanmakuOpacity,
			DanmakuArea:     playerAreaName(uiSt.Player.DanmakuArea),
			DanmakuDensity:  playerDensityLabel(uiSt.Player.DanmakuDensity),
			Volume:          uiSt.Player.Volume,
			SubtitleLang:    playerSubtitleLabel(uiSt.Player.SubtitleLang),
			SubtitleOptions: []string{"关闭", "zh-CN", "ai-zh"},
		},
	}
	if !backend.AccountStatus().LoggedIn && initialRoute != RouteSettings && initialRoute != RouteLogin {
		app.SavedRoute = initialRoute
		app.CurrentRoute = RouteLogin
	}
	if app.FeedSortOrder == "" {
		app.FeedSortOrder = "newest"
	}
	app.resetFeedListState()
	app.RefreshData()
	return app
}

func (a *App) resetFeedListState() {
	a.FeedListState = ui.ListState{
		Key: func(i int) any {
			if i >= 0 && i < len(a.FeedItems) {
				return a.FeedItems[i].FeedKey
			}
			return i
		},
	}
}

// Close releases bitmaps, player resources, and event subscriptions (DEC-030, INV-13).
func (a *App) Close() {
	a.setPlayerFullscreen(false)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	a.cancel()
	if a.upgradeCancel != nil {
		a.upgradeCancel()
		a.upgradeCancel = nil
	}
	if a.qrCancel != nil {
		a.qrCancel()
	}
	if a.QRSession.FlowID != "" {
		a.backend.CancelQRLogin(a.QRSession.FlowID)
	}
	if a.unsub != nil {
		a.unsub()
		a.unsub = nil
	}
	a.ReleaseBitmapsLocked()
	if a.player != nil {
		a.player.Close()
	}
}

// ReleaseBitmapsLocked frees any allocated UI bitmaps when closing to tray (DEC-030).
func (a *App) ReleaseBitmapsLocked() {
	a.QRBitmap = nil
	a.ImageViewer.Bitmaps = nil
	a.imageCache = make(map[string]*ui.Bitmap)
	a.imageKeys = nil
	a.postCards = nil
}

// lookupLocalBitmap returns a registered local image as a thumbnail. The
// file is read, decoded and scaled down off the UI thread; until it is ready
// (or when it cannot be read) the caller draws its placeholder. Scrolling
// therefore never waits for a large original to decode.
func (a *App) lookupLocalBitmap(itemKey, relPath string) *ui.Bitmap {
	if a.backend == nil || itemKey == "" || relPath == "" {
		return nil
	}
	key := itemKey + "::" + relPath
	a.mu.Lock()
	if a.imageCache == nil {
		a.imageCache = make(map[string]*ui.Bitmap)
	}
	if bmp, ok := a.imageCache[key]; ok {
		a.mu.Unlock()
		return bmp
	}
	a.rememberImageKeyLocked(key)
	a.mu.Unlock()
	requestGuarded(a, "thumb:"+key, nil, func(ctx context.Context) (*ui.Bitmap, error) {
		select {
		case a.imageQueue <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		defer func() { <-a.imageQueue }()
		fullPath, err := a.backend.MediaPath(itemKey, relPath)
		if err != nil || fullPath == "" {
			return nil, nil
		}
		return loadThumbBitmap(fullPath, thumbMaxEdge), nil
	}, func(bmp *ui.Bitmap) {
		a.mu.Lock()
		if _, ok := a.imageCache[key]; ok && !a.closed {
			a.imageCache[key] = bmp
		}
		a.mu.Unlock()
	}, func(error) {})
	a.mu.Lock()
	bmp := a.imageCache[key]
	a.mu.Unlock()
	return bmp
}

// thumbMaxEdge bounds decoded thumbnails: wide enough for a full-width
// card on a 150% display, far smaller than camera originals.
const thumbMaxEdge = 1280

// rememberImageKeyLocked reserves a cache slot for key (nil until loaded),
// evicting the oldest entry beyond 160.
func (a *App) rememberImageKeyLocked(key string) {
	if len(a.imageKeys) >= 160 {
		delete(a.imageCache, a.imageKeys[0])
		a.imageKeys = a.imageKeys[1:]
	}
	a.imageKeys = append(a.imageKeys, key)
	a.imageCache[key] = nil
}

// lookupHistoryThumbBitmap loads and caches a local parse-history thumbnail from <DataDir>/thumbs/ (R11).
func (a *App) lookupHistoryThumbBitmap(thumbName string) *ui.Bitmap {
	if a.backend == nil || thumbName == "" {
		return nil
	}
	cacheKey := "hist-thumb::" + thumbName
	a.mu.Lock()
	if a.imageCache == nil {
		a.imageCache = make(map[string]*ui.Bitmap)
	}
	if bmp, ok := a.imageCache[cacheKey]; ok {
		a.mu.Unlock()
		return bmp
	}
	a.mu.Unlock()

	fullPath, err := a.backend.HistoryThumbPath(thumbName)
	if err != nil || fullPath == "" {
		return nil
	}
	bmp := loadBitmapFromFile(fullPath)
	if bmp != nil {
		a.mu.Lock()
		if !a.closed {
			if len(a.imageCache) > 128 {
				a.imageCache = make(map[string]*ui.Bitmap)
			}
			a.imageCache[cacheKey] = bmp
		}
		a.mu.Unlock()
	}
	return bmp
}

// BindEventLoop subscribes to backend events and invokes updateFn (typically win.Update) on changes.
func (a *App) BindEventLoop(updateFn func(func())) {
	a.dispatch = updateFn
	if updateFn != nil {
		// A frame decoded off the UI thread after a paused seek needs a
		// redraw; while playing, the view redraws every frame anyway (F-043).
		a.player.SetFrameNotify(func() { updateFn(func() {}) })
	}
	if a.unsub != nil {
		a.unsub()
	}
	a.unsub = a.backend.SubscribeEvents(func(ev service.Event) {
		if updateFn == nil {
			a.handleBackendEvent(ev)
			return
		}
		updateFn(func() {
			if a.closed {
				return
			}
			a.handleBackendEvent(ev)
		})
	})
}

func (a *App) handleBackendEvent(ev service.Event) {
	if ev.Type == service.EventSubscriptionChecked && strings.HasPrefix(ev.Message, "progress:") {
		if a.pending["check-updates"] {
			_, _ = fmt.Sscanf(ev.Message, "progress:%d/%d", &a.CheckDone, &a.CheckTotal)
		}
		return
	}
	switch ev.Type {
	case service.EventAccountChanged:
		if ev.Message == "expired" || ev.Message == "logged_out" {
			a.TriggerAuthExpired("登录已失效，请重新登录")
		}
	case service.EventTaskUpdated, service.EventLibraryChanged, service.EventSubscriptionChecked, service.EventLiveUpdated:
		a.postCards = nil
		if strings.HasSuffix(ev.Message, ":storage") {
			a.BannerError = "任务状态写入失败，请检查存储后重试"
		}
		a.refreshTasks()
		if ev.Type == service.EventLibraryChanged {
			a.refreshLibraryStatistics()
			if len(a.FeedItems) == 0 && a.HomeActorFilter == nil {
				a.loadInitialFeed()
			}
			if a.HomeActorFilter == nil && len(a.FeedItems) > 0 {
				a.feedMoreBefore = true
			}
			a.reloadLibraryItems()
		}
		if ev.Type == service.EventSubscriptionChecked {
			a.refreshSubscriptions()
		}
		if ev.Type == service.EventLiveUpdated {
			a.LiveRooms = a.backend.ListLiveRooms()
		}
	}
}

// TriggerAuthExpired transitions to the login view while preserving the current route and loaded data (FR-AUTH-005, C07).
func (a *App) TriggerAuthExpired(reason string) {
	a.closePlayerOverlay()
	a.closeImageViewer()
	if a.CurrentRoute != RouteLogin {
		a.SavedRoute = a.CurrentRoute
	}
	a.CurrentRoute = RouteLogin
	a.LoginError = reason
	a.BannerError = reason
	a.backend.PauseAllActiveWork()
}

// CompleteLogin transitions back to SavedRoute (or RouteHome) after successful login while keeping loaded data intact (C07).
func (a *App) CompleteLogin() {
	a.LoginError = ""
	a.BannerError = ""
	a.ManualCookieInput = ""
	a.ManualRefreshInput = ""
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

// Navigate switches the active route and persists it in ui-state.json.
func (a *App) Navigate(route string) {
	a.BannerError = ""
	a.Opening = nil
	a.PostMenuKey = ""
	a.HistoryHeaderMenuOpen = false
	a.HistoryClearConfirmOpen = false
	a.HistoryItemMenuID = ""
	if route == RouteHome && a.CurrentRoute == RouteHome && a.HomeActorFilter != nil {
		a.closeHomeActorFeed()
	}
	if route == RouteSubscriptions {
		a.SubDetailOpen = false
		a.SubMoreMenuOpen = false
		a.SubManageMenuOpen = false
		a.SubManageModalMode = ""
	}
	if a.CurrentRoute == RouteHome && route != RouteHome {
		if a.HomeActorFilter != nil {
			a.closeHomeActorFeed()
		}
	}
	if route != RouteLogin && route != RouteSettings && !a.backend.AccountStatus().LoggedIn {
		a.SavedRoute = route
		route = RouteLogin
	}
	if a.CurrentRoute != route && a.CurrentRoute != RouteLogin {
		a.PreviousRoute = a.CurrentRoute
	}
	a.CurrentRoute = route
	if route == RouteHome && a.backend.AccountStatus().LoggedIn {
		if len(a.FeedItems) == 0 {
			a.loadInitialFeed()
		}
		if a.HomeActorFilter == nil && len(a.FeedItems) > 0 {
			a.feedMoreBefore = true
		}
	}
	st := a.backend.GetUIState()
	st.LastRoute = route
	if _, err := a.backend.UpdateUIState(st); err != nil {
		a.showError(err)
	}
}

// OpenSubscriptionDetail opens the /subscriptions/:id detail view for sub (R2).
func (a *App) OpenSubscriptionDetail(sub subscription.Subscription) {
	a.Navigate(RouteSubscriptions)
	a.SelectedSubID = sub.ID
	a.SelectedSubMid = sub.Mid
	a.SubDetailOpen = true
	a.SubMoreMenuOpen = false
	a.SubManageMenuOpen = false
	a.SubManageModalMode = ""
	if a.SubDetailTab != "accounts" {
		a.SubDetailTab = "live"
	}
	a.syncSelectedSubRules(sub)
}

// NavigateBack returns to PreviousRoute or RouteHome.
func (a *App) NavigateBack() {
	target := a.PreviousRoute
	if target == "" || target == a.CurrentRoute {
		target = RouteHome
	}
	a.Navigate(target)
}

// RefreshData reloads lists from local SQLite / stores without making network requests.
func (a *App) RefreshData() {
	if !a.backend.AccountStatus().LoggedIn {
		return
	}
	a.HistoryItems = a.backend.ListHistory()
	a.refreshLibraryStatistics()
	if len(a.FeedItems) == 0 {
		a.loadInitialFeed()
	}
	a.refreshSubscriptions()
	if a.SelectedSubID == "" && len(a.Subscriptions) > 0 {
		a.SelectedSubMid = a.Subscriptions[0].Mid
		a.syncSelectedSubRules(a.Subscriptions[0])
	} else if a.SelectedSubID != "" {
		for _, sub := range a.Subscriptions {
			if sub.ID == a.SelectedSubID {
				a.SubRules = sub.Rules
				break
			}
		}
	}
	a.LiveRooms = a.backend.ListLiveRooms()
	a.reloadLibraryItems()
	a.refreshTasks()
}

func (a *App) refreshSubscriptions() {
	if !a.backend.AccountStatus().LoggedIn {
		return
	}
	subs, err := a.backend.ListSubscriptions()
	if err != nil {
		a.showError(err)
		return
	}
	a.Subscriptions = subs
}

func (a *App) refreshLibraryStatistics() {
	stats, err := a.backend.LibraryStatistics()
	if err != nil {
		a.showError(fmt.Errorf("读取媒体库统计失败：%w", err))
		return
	}
	a.libraryStatistics = stats
}

func (a *App) refreshTasks() {
	if !a.backend.AccountStatus().LoggedIn {
		return
	}
	tasks, err := a.backend.ListDownloadTasks()
	if err != nil {
		a.showError(err)
		return
	}
	a.Tasks = tasks
}

// loadInitialFeed opens the timeline at the newest cached post (DEC-040).
func (a *App) loadInitialFeed() {
	a.FeedSortOrder = "newest"
	a.loadHomeBaseline()
	req := library.FeedPageRequest{Order: "newest", CachedOnly: true, Authors: a.FeedAuthorsFilter, Limit: 60}
	if page, err := a.backend.FeedPage(req); err == nil {
		a.FeedError = ""
		a.FeedItems = page.Items
		a.feedMoreBefore, a.feedMoreAfter = false, page.HasMore
	} else {
		a.FeedError = err.Error()
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
	a.SubRuleScope = sub.Scope
	a.SubRuleAutoCache = sub.AutoCache
	a.SubRuleLiveNotify = sub.LiveNotify
	a.SubRuleLiveAutoRec = sub.LiveAutoRecord
}

func (a *App) reloadLibraryItems() {
	a.libraryOffset = 0
	a.LibraryListState.ScrollTo(0, ui.Start)
	a.loadLibraryPage(true)
}

func (a *App) loadLibraryPage(reset bool) {
	var authors []int64
	if a.LibrarySubFilter != "" {
		for _, sub := range a.Subscriptions {
			if sub.ID == a.LibrarySubFilter {
				for _, acc := range sub.Accounts {
					var id int64
					_, _ = fmt.Sscanf(acc.UID, "%d", &id)
					if id > 0 {
						authors = append(authors, id)
					}
				}
				break
			}
		}
	}
	sortOrder := a.LibrarySort
	if sortOrder == "pub_ts" {
		sortOrder = "newest"
	}
	if a.LibrarySort == "oldest" {
		sortOrder = "oldest"
	}
	items, err := a.backend.ListLibraryItems(library.FeedFilter{
		Kind:    a.LibraryKindFilter,
		Mid:     a.LibraryCreatorMid,
		Authors: authors,
		Keyword: a.LibrarySearch,
		Sort:    sortOrder,
		Limit:   60,
		Offset:  a.libraryOffset,
	})
	if err != nil {
		a.LibraryError = err.Error()
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

// PopulateSyntheticFeed populates n synthetic feed entries for virtualized list performance testing and screenshots (C38).
func (a *App) PopulateSyntheticFeed(n int) {
	authors := []string{"技术频道_Alpha", "独立游戏工坊", "数字影像志", "开源架构师", "极客实验室", "深海摄影师"}
	titles := []string{
		"纯 Go + MyGo 原生桌面架构全链路解析与 4K 合流实测",
		"今日工作室日常：最新摄影样张分享（原图直出无压缩）",
		"深入理解 fMP4 流式分段与无损 FLAC 音轨合成机制",
		"周末硬件折腾记：NAS 万兆存储与本地媒体库归档方案",
	}
	summaries := []string{
		"本期视频详细拆解三栏社交网络信息流布局、虚拟滚动锚点记忆、8 MiB 分段断点续传以及 Windows Media Foundation 原生解码。",
		"分享一组本周拍摄的城市建筑与夜景原图，点击缩略图可在内置看图层按原始分辨率缩放与拖拽查看。",
		"测试高密度 5000 条虚拟列表在零外网请求下的流畅滚动表现，左右两栏保持固定不随中栏滚动。",
	}
	items := make([]library.FeedEntry, n)
	baseTs := int64(1710000000)
	for i := 0; i < n; i++ {
		kind := "video"
		if i%3 == 1 {
			kind = "dynamic"
		}
		cached := true
		status := "green"
		if i%5 == 2 {
			status = "yellow"
		}
		author := authors[i%len(authors)]
		title := fmt.Sprintf("%s #%04d", titles[i%len(titles)], i+1)
		summary := summaries[i%len(summaries)]
		var itemKey, coverURL string
		if len(a.LibraryItems) > 0 && i == 0 {
			itemKey = a.LibraryItems[0].ItemKey
		}
		if kind == "dynamic" {
			coverURL = "synth-grid-3"
		}
		items[i] = library.FeedEntry{
			FeedKey:      fmt.Sprintf("bilibili:%s:SYNTH_%04d", kind, i+1),
			Site:         "bilibili",
			Kind:         kind,
			RawID:        fmt.Sprintf("BV1synth%04d", i+1),
			Mid:          20001 + int64(i%len(authors)),
			AuthorName:   author,
			Title:        title,
			Summary:      summary,
			CoverURL:     coverURL,
			CanonicalURL: fmt.Sprintf("https://www.bilibili.com/video/BV1synth%04d", i+1),
			PubTs:        baseTs - int64(i*360),
			Cached:       cached,
			CacheStatus:  status,
			ItemKey:      itemKey,
		}
	}
	a.FeedItems = items
	a.resetFeedListState()
}

// OpeningPreview is what a clicked card already knows about the link it
// opens. Kind is "video", "dynamic", "user" or "live".
type OpeningPreview struct {
	Kind       string
	Title      string
	AuthorName string
	Mid        int64
	CoverURL   string
	Duration   string
	Err        string

	raw string
	seq int
}

// SubmitParse resolves and opens a link or UID typed by the user (C01..C07,
// C12, C13). The page changes when the link is resolved.
func (a *App) SubmitParse(raw string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		a.LastParseError = "粘贴哔哩哔哩视频链接、用户主页或 UID"
		return
	}
	a.openLink(trimmed, nil)
}

// OpenVideo opens a video from a card: the video page shows at once.
func (a *App) OpenVideo(bvid, title, authorName string, mid int64, coverURL, duration string) {
	if strings.TrimSpace(bvid) == "" {
		return
	}
	a.openLink(bvid, &OpeningPreview{Kind: "video", Title: title, AuthorName: authorName, Mid: mid, CoverURL: coverURL, Duration: duration})
}

// OpenUser opens a creator's space from an avatar or name: the page shows at once.
func (a *App) OpenUser(mid int64, name string) {
	if mid <= 0 {
		return
	}
	a.openLink(fmt.Sprintf("UID:%d", mid), &OpeningPreview{Kind: "user", AuthorName: name, Mid: mid})
}

// RetryOpening opens the link of a failed preview again.
func (a *App) RetryOpening() {
	if a.Opening == nil {
		return
	}
	p := *a.Opening
	p.Err = ""
	a.openLink(p.raw, &p)
}

func (a *App) openLink(raw string, preview *OpeningPreview) {
	a.LastParseError = ""
	a.BannerError = ""
	a.openSeq++
	seq := a.openSeq
	if preview != nil {
		p := *preview
		p.raw, p.seq, p.Err = raw, seq, ""
		route := RouteVideo
		if p.Kind == "user" || p.Kind == "live" {
			route = RouteUser
		}
		if a.CurrentRoute != route {
			a.PreviousRoute = a.CurrentRoute
		}
		a.CurrentRoute = route
		a.Opening = &p
		a.PostMenuKey = ""
	} else {
		a.Opening = nil
	}

	backend := a.backend
	current := func() bool {
		if a.openSeq != seq {
			return false
		}
		// A preview the user left (Navigate clears it) no longer wants its page.
		return preview == nil || a.Opening != nil && a.Opening.seq == seq
	}
	requestGuarded(a, fmt.Sprintf("parse-%d", seq), current, func(ctx context.Context) (*service.OpenLinkResult, error) {
		return backend.OpenLink(ctx, raw)
	}, func(res *service.OpenLinkResult) {
		a.Opening = nil
		a.applyParseResult(res)
		a.afterOpenLink(res)
	}, func(err error) {
		if preview != nil && a.Opening != nil {
			a.Opening.Err = err.Error()
			return
		}
		a.LastParseError = err.Error()
		a.showError(err)
	})
}

// parseBusy reports whether the newest open is still waiting for OpenLink.
func (a *App) parseBusy() bool {
	return a.pending[fmt.Sprintf("parse-%d", a.openSeq)]
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
		requestGuarded(a, "history-thumbs-"+j.Kind+":"+j.ID, nil, func(ctx context.Context) (struct{}, error) {
			return struct{}{}, backend.FetchHistoryThumbs(ctx, j)
		}, func(struct{}) {
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
	a.HistoryItems = a.backend.ListHistory()
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
			a.PreviousRoute = a.CurrentRoute
		}
		a.CurrentRoute = RouteVideo
	case "user", "live":
		a.CurrentUser = res.User
		a.UserTab = "dynamics"
		a.UserDynListState, a.UserVidListState = ui.ListState{}, ui.ListState{}
		a.UserDynamics = nil
		a.UserVideos = nil
		a.UserVideosPage = 0
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
			a.UserVideosPage = 1
			a.UserVideosHasMore = len(res.UserVideos.Items) < res.UserVideos.Total
			a.UserVideosError = ""
		}
		if a.CurrentRoute != RouteUser {
			a.PreviousRoute = a.CurrentRoute
		}
		a.CurrentRoute = RouteUser
		if res.LiveRoom != nil {
			a.LiveRooms = a.backend.ListLiveRooms()
		} else if res.User != nil {
			mid := res.User.Mid
			backend := a.backend
			requestGuarded(a, fmt.Sprintf("user-live-%d", mid), func() bool {
				return a.CurrentRoute == RouteUser && a.CurrentUser != nil && a.CurrentUser.Mid == mid && backend.AccountStatus().LoggedIn
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
			a.PreviousRoute = a.CurrentRoute
		}
		a.CurrentRoute = RouteVideo
	}
}

// LoadMoreUserDynamics loads the next page of creator dynamics while preserving page 1 on error (C13).
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
		seen := make(map[string]bool)
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
	}, func(err error) { a.UserDynamicsError = err.Error(); a.showError(err) })
}

func (a *App) LoadMoreUserVideos() {
	if a.CurrentUser == nil {
		return
	}
	mid, pageNum := a.CurrentUser.Mid, a.UserVideosPage+1
	requestGuarded(a, fmt.Sprintf("user-videos-%d", mid), func() bool { return a.CurrentUser != nil && a.CurrentUser.Mid == mid }, func(ctx context.Context) (bilibili.UserVideosPage, error) {
		return a.backend.UserVideosPage(ctx, mid, pageNum)
	}, func(page bilibili.UserVideosPage) {
		if a.CurrentUser == nil || a.CurrentUser.Mid != mid {
			return
		}
		seen := make(map[string]bool)
		for _, it := range a.UserVideos {
			seen[it.BVID] = true
		}
		for _, it := range page.Items {
			if !seen[it.BVID] {
				a.UserVideos = append(a.UserVideos, it)
				seen[it.BVID] = true
			}
		}
		a.UserVideosError = ""
		a.UserVideosPage = pageNum
		a.UserVideosHasMore = len(a.UserVideos) < page.Total
	}, func(err error) { a.UserVideosError = err.Error(); a.showError(err) })
}

func (a *App) mergeLiveRooms(rooms []live.RoomState) {
	for _, room := range rooms {
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
}

// OpenItemImageViewer resolves registered relative media paths via Backend.MediaPath (INV-08) and opens the image viewer.
func (a *App) OpenItemImageViewer(itemKey string, urls []string, relPaths []string, index int) {
	resolved := make([]string, len(relPaths))
	for i, rel := range relPaths {
		if rel == "" {
			continue
		}
		if a.backend != nil && itemKey != "" {
			if p, err := a.backend.MediaPath(itemKey, rel); err == nil {
				resolved[i] = p
			}
		}
	}
	a.OpenImageViewer(urls, resolved, index)
	a.ImageViewer.ItemKey = itemKey
	a.ImageViewer.RelPaths = relPaths
	a.ImageViewer.Motions = nil
	if a.backend != nil && itemKey != "" {
		if item, ok, err := a.backend.GetLibraryItem(itemKey); err == nil && ok {
			motions := make([]string, len(relPaths))
			for i, rel := range relPaths {
				for _, m := range item.Media {
					if rel != "" && m.Path == rel {
						motions[i] = m.Motion
					}
				}
			}
			a.ImageViewer.Motions = motions
		}
	}
}

// playImageMotion plays the motion clip of the live photo on show.
func (a *App) playImageMotion(rel string) {
	item, ok, err := a.backend.GetLibraryItem(a.ImageViewer.ItemKey)
	if err != nil || !ok {
		a.showError(err)
		return
	}
	a.closeImageViewer()
	a.openPlayerFile(item, rel)
}

// OpenImageViewer opens the in-app image viewer overlay, decoding real local files or fetched image bytes (C14, R-07).
func (a *App) OpenImageViewer(urls []string, localPaths []string, index int) {
	if len(urls) == 0 && len(localPaths) == 0 {
		return
	}
	a.closeImageViewer()
	revision := a.imageViewerRevision
	ctx, cancel := context.WithCancel(a.ctx)
	a.imageViewerCancel = cancel
	// Ensure URLs use https and never leak external Referer (C14).
	cleanURLs := make([]string, len(urls))
	localPaths = append([]string(nil), localPaths...)
	for i, u := range urls {
		if strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "http://127.0.0.1") && !strings.HasPrefix(u, "http://localhost") {
			u = "https://" + strings.TrimPrefix(u, "http://")
		}
		cleanURLs[i] = u
	}
	if index < 0 {
		index = 0
	}
	total := maxInt(len(cleanURLs), len(localPaths))
	index = min(index, total-1)
	a.ImageViewer = ImageViewerState{Open: true, URLs: cleanURLs, LocalPaths: localPaths, Index: index, Zoom: 1, Fit: true}
	backend := a.backend
	requestGuarded(a, fmt.Sprintf("image-%d", revision), func() bool {
		return a.ImageViewer.Open && a.imageViewerRevision == revision && backend.AccountStatus().LoggedIn
	}, func(context.Context) (ImageViewerState, error) {
		defer cancel()
		bitmaps := make([]*ui.Bitmap, total)
		errors := make([]string, total)
		for i := 0; i < total; i++ {
			if ctx.Err() != nil {
				break
			}
			if i < len(localPaths) && localPaths[i] != "" {
				bitmaps[i] = loadBitmapFromFile(localPaths[i])
				if bitmaps[i] == nil {
					errors[i] = "本机图片无法读取或格式不支持"
				}
			} else if i < len(cleanURLs) && cleanURLs[i] != "" {
				raw, err := backend.FetchImage(ctx, cleanURLs[i])
				if err != nil {
					errors[i] = err.Error()
				} else {
					bitmaps[i] = decodeImageBytesToBitmap(raw)
					if bitmaps[i] == nil {
						errors[i] = "图片格式不支持或文件损坏"
					}
				}
			} else {
				errors[i] = "图片未登记或路径不可访问"
			}
		}
		return ImageViewerState{
			Open:       true,
			URLs:       cleanURLs,
			LocalPaths: localPaths,
			Index:      index,
			Zoom:       1,
			Fit:        true,
			Bitmaps:    bitmaps,
			Errors:     errors,
		}, nil
	}, func(state ImageViewerState) {
		a.ImageViewer.Bitmaps = state.Bitmaps
		a.ImageViewer.Errors = state.Errors
		a.imageViewerCancel = nil
	}, nil)
}

func loadBitmapFromFile(path string) *ui.Bitmap {
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
		return nil
	}
	return decodeImageBytesToBitmap(raw)
}

func decodeImageBytesToBitmap(raw []byte) *ui.Bitmap {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 40_000_000 {
		return nil
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil || img == nil {
		return nil
	}
	if rgba, ok := img.(*image.RGBA); ok {
		return ui.NewBitmap(rgba)
	}
	b := img.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), img, b.Min, draw.Src)
	return ui.NewBitmap(rgba)
}

// OpenPlayerOverlay opens the in-app media player overlay for an item, loading its registered MP4, danmaku XML, and subtitles via Backend.MediaPath (C22, R-07, R-08).
func (a *App) OpenPlayerOverlay(item library.ItemMetadata) {
	a.openPlayerFile(item, "")
}

// openPlayerFile selects a registered file before opening the decoder once.
func (a *App) openPlayerFile(item library.ItemMetadata, selected string) {
	a.setPlayerFullscreen(false)
	var segments []string
	seen := make(map[string]bool)
	add := func(path string) {
		if path != "" && !seen[path] {
			segments = append(segments, path)
			seen[path] = true
		}
	}
	for _, part := range item.Parts {
		add(part.File)
	}
	for _, m := range item.Media {
		if m.Role == "video" || m.Role == "live_segment" {
			add(m.Path)
		}
		if m.Role == "motion" && m.Path == selected {
			// A live photo's motion clip plays on its own.
			segments, seen = nil, map[string]bool{}
			add(m.Path)
			break
		}
	}
	a.PlayerOverlay.Segments = segments
	index := 0
	if selected != "" {
		index = -1
		for i, path := range segments {
			if path == selected {
				index = i
				break
			}
		}
	}
	a.openPlayerSegment(item, index)
}

func (a *App) openPlayerSegment(item library.ItemMetadata, index int) {
	_ = a.player.Close()
	a.PlayerOverlay.Open = true
	a.PlayerOverlay.LastActiveAt = time.Time{}
	a.PlayerOverlay.ControlsHidden = false
	a.PlayerOverlay.MoreMenuOpen = false
	a.PlayerOverlay.VolumeMenuOpen = false
	a.PlayerOverlay.ItemKey = item.ItemKey
	a.PlayerOverlay.Title = item.Title
	a.PlayerOverlay.Segment = index
	a.PlayerOverlay.DecoderMissing = false
	a.PlayerOverlay.LoadError = ""
	a.PlayerOverlay.Loading = false
	a.PlayerOverlay.MediaPath = ""
	a.PlayerOverlay.SeekNormalized = 0
	a.PlayerOverlay.SubtitleOptions = []string{"关闭"}
	if index < 0 || index >= len(a.PlayerOverlay.Segments) {
		a.PlayerOverlay.LoadError = "没有已登记的可播放视频，请补全缓存或检查文件。"
		return
	}
	mediaPath, err := a.backend.MediaPath(item.ItemKey, a.PlayerOverlay.Segments[index])
	if err != nil {
		a.PlayerOverlay.LoadError = "视频文件不可访问：" + err.Error()
		return
	}
	a.PlayerOverlay.MediaPath = mediaPath
	var part library.VideoPartRecord
	for _, candidate := range item.Parts {
		if candidate.File == a.PlayerOverlay.Segments[index] {
			part = candidate
			break
		}
	}
	// Opening a file (Media Foundation, D3D11) and parsing its danmaku take
	// up to a few hundred milliseconds: they run off the UI thread, which
	// shows the dark layer and a spinner meanwhile (F-057).
	a.playerOpenSeq++
	seq := a.playerOpenSeq
	a.PlayerOverlay.Loading = true
	itemKey, backend := item.ItemKey, a.backend
	var danmakuPath string
	if part.Danmaku != "" {
		if path, err := backend.MediaPath(itemKey, part.Danmaku); err == nil {
			danmakuPath = path
		}
	}
	type subtitleFile struct{ lang, path string }
	var subtitleFiles []subtitleFile
	for _, rel := range part.Subtitles {
		if strings.ToLower(filepath.Ext(rel)) != ".vtt" {
			continue
		}
		path, err := backend.MediaPath(itemKey, rel)
		if err != nil {
			continue
		}
		lang := strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel))
		if i := strings.IndexByte(lang, '.'); i >= 0 {
			lang = lang[i+1:]
		}
		subtitleFiles = append(subtitleFiles, subtitleFile{lang, path})
	}
	type opened struct {
		dec     player.MediaDecoder
		danmaku *player.DanmakuEngine
		tracks  []player.SubtitleTrack
	}
	stale := func() bool {
		return a.playerOpenSeq != seq || !a.PlayerOverlay.Open || a.PlayerOverlay.MediaPath != mediaPath
	}
	requestGuarded(a, fmt.Sprintf("player-open-%d", seq), nil, func(ctx context.Context) (opened, error) {
		dec, err := player.OpenMediaFile(mediaPath)
		if err != nil {
			return opened{}, err
		}
		out := opened{dec: dec}
		if danmakuPath != "" {
			if raw, err := os.ReadFile(danmakuPath); err == nil {
				if eng, err := player.ParseDanmakuXML(raw); err == nil {
					out.danmaku = eng
				}
			}
		}
		for _, f := range subtitleFiles {
			raw, err := os.ReadFile(f.path)
			if err != nil {
				continue
			}
			if cues := player.ParseWebVTT(raw); len(cues) > 0 {
				out.tracks = append(out.tracks, player.SubtitleTrack{Lang: f.lang, Label: f.lang, Cues: cues})
			}
		}
		return out, nil
	}, func(res opened) {
		if stale() {
			_ = res.dec.Close()
			return
		}
		a.PlayerOverlay.Loading = false
		if err := a.player.LoadDecoder(mediaPath, res.dec); err != nil {
			a.PlayerOverlay.DecoderMissing = player.IsMissingCodecError(err)
			a.PlayerOverlay.MissingReason = err.Error()
			a.PlayerOverlay.LoadError = "无法播放此文件：" + err.Error()
			return
		}
		if res.danmaku != nil {
			a.player.SetDanmakuEngine(res.danmaku)
		}
		for _, t := range res.tracks {
			a.PlayerOverlay.SubtitleOptions = append(a.PlayerOverlay.SubtitleOptions, t.Lang)
		}
		a.player.SetSubtitles(res.tracks)
		a.applyAndPersistPlayerPrefs()
		a.player.Play() // videos start playing as soon as they open (DEC-040)
	}, func(err error) {
		if stale() {
			return
		}
		a.PlayerOverlay.Loading = false
		a.PlayerOverlay.DecoderMissing = player.IsMissingCodecError(err)
		a.PlayerOverlay.MissingReason = err.Error()
		a.PlayerOverlay.LoadError = "无法播放此文件：" + err.Error()
	})
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// View is the root MyGo immediate-mode view function (`func(c *ui.Context)`).
// Implements the 3-column responsive layout per docs/ui.md §1 and C38:
//   - >= 1280: Left 275px, Center 600px, Right 350px
//   - 1000..1279: Left 88px (icons + tooltip/label), Center 600px, Right 300px
//   - 720..999: Left 88px, Center flexible (w - 88), Right hidden (quick-parse moved to Center top)
func (a *App) View(c *ui.Context) {
	c.SetTheme(TraceTheme())

	if a.PlayerOverlay.Open {
		a.player.Advance(0)
		if a.player.Snapshot(1, 1).Playing {
			c.Invalidate()
		}
	}
	w, h := c.Size()
	if w <= 0 {
		w = 1280
	}
	if h <= 0 {
		h = 860
	}

	panelW := a.contextPanelWidth(w)
	a.panelShown = panelW > 0
	contentW := max(w-railWidth-panelW, 1)
	pageW := a.pageWidth(contentW)

	ui.Row(c).Size(w, h).Background(ColorBgMain).Clip().Children(func() {
		// Icon rail and context panel stay put while the page scrolls
		// (NFR-UI-001, DEC-039).
		ui.Column(c).
			Label("左栏导航").
			Width(railWidth).
			FillHeight().
			Shrink(0).
			Background(ColorBgRail).
			BorderWidth(0, 1, 0, 0).
			BorderColor(ColorBorder).
			Children(func() {
				a.renderRail(c)
			})
		if panelW > 0 {
			ui.Column(c).
				Label("上下文侧栏").
				Width(panelW).
				FillHeight().
				Shrink(0).
				Background(ColorBgSecondary).
				BorderWidth(0, 1, 0, 0).
				BorderColor(ColorBorder).
				ClipX().
				Children(func() {
					a.renderContextPanel(c, panelW)
				})
		}
		ui.Column(c).
			Grow(1).
			FillHeight().
			AlignItems(ui.Center).
			Background(ColorBgMain).
			Clip().
			Children(func() {
				ui.Column(c).
					Label("中栏内容").
					Width(pageW).
					FillHeight().
					Shrink(0).
					Background(ColorBgMain).
					Clip().
					Children(func() {
						a.renderCenterColumn(c, pageW)
					})
			})
	})

	// Overlays & Modals
	if a.ConfirmLogoutOpen {
		a.renderLogoutConfirmOverlay(c, w, h)
	}
	if a.DeleteConfirm.Open {
		a.renderDeleteConfirmOverlay(c, w, h)
	}
	if a.AddSubDialogOpen {
		a.renderAddSubOverlay(c, w, h)
	}
	if a.ImportSubModalOpen {
		a.renderImportSubOverlay(c, w, h)
	}
	if a.ImageViewer.Open {
		a.renderImageViewerOverlay(c, w, h)
	}
	if a.PlayerOverlay.Open {
		a.renderPlayerOverlay(c, w, h)
	}
}
