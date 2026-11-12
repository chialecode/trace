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
	"github.com/chialecode/trace/internal/site"
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
	RouteDouyinWork    = "/douyin/work"
	RouteDouyinUser    = "/douyin/user"
	RouteLibrary       = "/library"
	RouteTasks         = "/tasks"
	RouteSettings      = "/settings"
	RouteLogin         = "/login"
)

// ImageViewerState holds state for the in-app image and mixed-page viewer overlay.
type ImageViewerState struct {
	Open             bool
	URLs             []string
	LocalPaths       []string
	ItemKey          string
	RelPaths         []string
	InlineSite       string   // site whose CDN serves InlineMediaURLs ("bilibili" or "douyin")
	InlinePageKinds  []string // per page: "image" | "live_photo" | "video" | "unknown"
	InlineMediaPaths []string // per page: resolved local motion/video file path
	InlineMediaURLs  []string // per page: online motion/video URL for uncached preview
	InlineDurations  []int    // per page: duration in seconds when known
	InlineActive     bool
	InlinePageIndex  int
	InlineLoading    bool
	InlineError      string
	InlineMediaPath  string
	PostKey          string
	Index            int
	Zoom             float32
	PanX             float32
	PanY             float32
	Fit              bool
	Dragging         bool
	PointerX         float32
	PointerY         float32
	Bitmaps          []*ui.Bitmap
	Errors           []string
	LastActiveAt     time.Time
	ControlsHidden   bool
	LastHoverPos     ui.Point
}

// PlayerOverlayState holds state for the in-app media player overlay.
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

// DeleteConfirmState holds state for the library item deletion confirmation modal.
type DeleteConfirmState struct {
	Open        bool
	Item        library.ItemMetadata
	Plan        library.DeletePlan
	FileCount   int
	TotalBytes  int64
	DeleteFiles bool
}

// HomeUpdatedActor represents a subscription or unsubscribed creator with new cached content after the last viewed timestamp on Home (DEC-037).
type HomeUpdatedActor struct {
	Key        string
	SubID      string
	Site       string
	UID        string
	Mid        int64
	Name       string
	Face       string
	Authors    []int64
	AuthorRefs []library.AuthorRef
	LatestTs   int64
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
	bgmPlayer           *player.Controller
	viewerPlayer        *player.Controller
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

	// Navigation & per-site auth guard
	CurrentRoute       string
	SavedRoute         string
	PreviousRoute      string
	BannerError        string
	BannerLoginSite    string
	BannerInfo         string
	expiredNoticeShown map[string]bool

	// Expandable post/description states & PostCard "⋯" menu
	ExpandedPosts map[string]bool
	PostMenuKey   string

	// Quick Parse & Parse Page
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

	// Login Page
	LoginSite          string // "bilibili" | "douyin"
	LoginTab           string // "qr" | "cookie"
	QRSession          bilibili.QRSession
	QRBitmap           *ui.Bitmap
	QRStatusText       string
	ManualCookieInput  string
	ManualRefreshInput string
	DouyinCookieInput  string
	LoginError         string
	qrRevision         uint64
	qrCancel           context.CancelFunc

	// Home Feed (DEC-037, DEC-040)
	FeedItems                 []library.FeedEntry
	FeedListState             ui.ListState
	FeedScroll                ui.ScrollState
	feedMoreBefore            bool
	feedMoreAfter             bool
	FeedError                 string
	feedFailedBefore          bool
	FeedSortOrder             string // always "newest"
	FeedFilterScope           string // "all" | "cached" | "unread"
	FeedSiteFilter            string // "all" | "bilibili" | "douyin"
	FeedAuthorsFilter         []int64
	FeedAuthorRefsFilter      []library.AuthorRef
	HomeActorFilter           *HomeUpdatedActor
	savedMainFeedItems        []library.FeedEntry
	savedMainFeedListState    ui.ListState
	savedMainFeedScrollY      float32
	savedMainFeedMoreBefore   bool
	savedMainFeedMoreAfter    bool
	savedMainFeedError        string
	savedMainFeedFailedBefore bool

	// Video & Dynamic Detail
	CurrentVideo         *bilibili.VideoDetail
	CurrentVideoItemKey  string
	CurrentDynamic       *bilibili.DynamicItem
	CurrentLibraryDetail *library.ItemMetadata
	SelectedPartPages    map[int]bool
	UpgradeAvailable     bool

	// Douyin Work & Creator Browsing
	CurrentDouyinWork      *service.DouyinWorkView
	CurrentDouyinUser      *service.DouyinUserView
	CurrentDouyinWorks     []service.DouyinWorkCardDTO
	DouyinWorksNextCursor  string
	DouyinWorksHasMore     bool
	DouyinWorksError       string
	DouyinWorksAuthExpired bool
	DouyinUserListState    ui.ListState
	DouyinUserSubMenuOpen  bool
	DouyinCardMenuID       string
	DouyinDescExpanded     bool

	// Pure-audio BGM bar playback state for Douyin notes
	BGMActiveKey      string
	BGMTitle          string
	BGMAuthor         string
	BGMPath           string
	BGMDurationSec    int
	BGMLoading        bool
	BGMError          string
	BGMSeekNormalized float64
	bgmOpenSeq        int
	viewerOpenSeq     int

	// Opening is the link being opened from a card or entry: its page shows
	// at once with what the card knew, and fills in when OpenLink returns.
	// openSeq makes the newest click win.
	// panelShown: this frame has a context panel beside the rail.
	panelShown bool
	// titleBar is the room the window buttons take under the hidden title
	// bar (DEC-046 ④); zero in tests, full screen and framed windows.
	titleBar ui.TitleBar
	// pageGapRight is the white space right of the centered page column.
	pageGapRight float32
	// SubPanelListState scrolls the subscriptions in the context panel.
	SubPanelListState ui.ListState

	Opening       *OpeningPreview
	openSeq       int
	playerOpenSeq int

	// User Space Page
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
	// UserListFilter is 全部 ("all") or 未缓存 ("uncached") on user pages (DEC-046 ⑧).
	UserListFilter  string
	userFilterCache uncachedFilterCache
	// douyinFilteredEmpty: 未缓存 left no loaded work on the Douyin user page.
	douyinFilteredEmpty bool
	// cacheStateRev changes whenever tasks or the library change.
	cacheStateRev int

	// Subscriptions Page
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
	LiveAccounts        []service.LiveAccountState

	// Library Page
	LibraryItems      []library.ItemMetadata
	LibraryListState  ui.ListState
	LibraryKindFilter string // "" | "image" | "video" | "dynamic" | "live"
	LibrarySiteFilter string // "all" | "bilibili" | "douyin"
	LibraryCreatorMid int64
	LibraryAuthorKey  string // "<site>:<uid>"
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

	// Tasks Page
	Tasks               []download.Task
	ExpandedTaskID      string
	TaskRemoveConfirmID string
	TaskActionError     string
	UpgradeCheckBusy    bool
	UpgradeCandidates   []library.ItemMetadata
	UpgradeDone         int
	UpgradeTotal        int
	upgradeCancel       context.CancelFunc

	// Settings Page
	SettingsCategory string // "account" | "download" | "quality" | "subscription" | "storage" | "general" | "logs" | "about"
	DraftSettings    settings.Settings
	// Settings → 网络: the custom proxy address being typed and why it was refused.
	ProxyDraftURL       string
	ProxyError          string
	proxyEditing        bool
	CacheDirInput       string
	AddLibraryRootInput string
	MoveExistingFiles   bool
	ConfirmLogoutOpen   bool
	ConfirmLogoutSite   string

	// Updates of TRACE (FR-UPD-002).
	updater            UpdaterAPI
	UpdateNotesOpen    bool
	RestartConfirmOpen bool
	RestartConfirmText string

	// Overlays
	ImageViewer   ImageViewerState
	PlayerOverlay PlayerOverlayState
}

func newCompanionAudioSink(primary player.AudioSink) player.AudioSink {
	if _, ok := primary.(*player.MyGoAudioSink); ok && os.Getenv("TRACE_QUIET") == "" {
		return player.NewMyGoAudioSink()
	}
	return player.NewNullAudioSink()
}

func normalizeAppRoute(route string) string {
	trimmed := strings.TrimSpace(route)
	switch {
	case trimmed == "" || !strings.HasPrefix(trimmed, "/"):
		return RouteHome
	case strings.HasPrefix(trimmed, RouteDouyinWork+"/"):
		return RouteDouyinWork
	case strings.HasPrefix(trimmed, RouteDouyinUser+"/"):
		return RouteDouyinUser
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
	default:
		return "all"
	}
}

func routeExclusiveSite(route string) string {
	switch normalizeAppRoute(route) {
	case RouteDouyinWork, RouteDouyinUser:
		return string(site.Douyin)
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
	case string(site.Bilibili):
		return "登录已失效，请重新登录"
	default:
		return "登录已失效，请重新登录"
	}
}

func siteExpiredTopNotice(siteID string) string {
	switch siteID {
	case string(site.Douyin):
		return "抖音登录已失效，抖音相关任务与录制已暂停；可前往设置重新粘贴 Cookie"
	case string(site.Bilibili):
		return "哔哩哔哩登录已失效，B 站相关任务与录制已暂停；可前往设置重新登录"
	default:
		return "登录已失效，请重新登录"
	}
}

// NewApp creates a new TRACE UI application bound to backend.
// In tests and tools, audioSink is Player NullAudioSink so no audio device is opened.
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
	bgmCtrl := player.NewController(player.Options{
		AudioSink: newCompanionAudioSink(audioSink),
		Volume:    uiSt.Player.Volume,
		Muted:     uiSt.Player.Muted,
	})
	viewerCtrl := player.NewController(player.Options{
		AudioSink: newCompanionAudioSink(audioSink),
		Volume:    uiSt.Player.Volume,
		Muted:     uiSt.Player.Muted,
	})

	initialRoute := normalizeAppRoute(uiSt.LastRoute)
	loginSite := string(site.Bilibili)
	if routeExclusiveSite(initialRoute) == string(site.Douyin) {
		loginSite = string(site.Douyin)
	}

	ctx, cancel := context.WithCancel(context.Background())
	app := &App{
		ctx:                ctx,
		cancel:             cancel,
		pending:            make(map[string]bool),
		imageCache:         make(map[string]*ui.Bitmap),
		imageQueue:         make(chan struct{}, 2),
		ExpandedPosts:      make(map[string]bool),
		expiredNoticeShown: make(map[string]bool),
		backend:            backend,
		player:             ctrl,
		bgmPlayer:          bgmCtrl,
		viewerPlayer:       viewerCtrl,
		CurrentRoute:       initialRoute,
		LoginSite:          loginSite,
		LoginTab:           "qr",
		FeedSortOrder:      "newest",
		FeedFilterScope:    "cached",
		FeedSiteFilter:     normalizeSiteFilter(uiSt.FeedFilter.Site),
		SelectedPartPages:  make(map[int]bool),
		UserTab:            "dynamics",
		SubDetailTab:       "live",
		AddSubScope:        "both",
		AddSubAutoCache:    "inherit",
		ImportSelectedMids: make(map[int64]bool),
		LibrarySiteFilter:  "all",
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
	if !backend.HasAnyLogin() && initialRoute != RouteSettings && initialRoute != RouteLogin {
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
		_ = a.player.Close()
	}
	if a.bgmPlayer != nil {
		_ = a.bgmPlayer.Close()
	}
	if a.viewerPlayer != nil {
		_ = a.viewerPlayer.Close()
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

// lookupHistoryThumbBitmap loads and caches a local parse-history thumbnail from <DataDir>/thumbs/.
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
		// redraw; while playing, the view redraws every frame anyway.
		notify := func() { updateFn(func() {}) }
		a.player.SetFrameNotify(notify)
		if a.bgmPlayer != nil {
			a.bgmPlayer.SetFrameNotify(notify)
		}
		if a.viewerPlayer != nil {
			a.viewerPlayer.SetFrameNotify(notify)
		}
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

func (a *App) handleBackendEvent(ev service.Event) {
	if ev.Type == service.EventSubscriptionChecked && strings.HasPrefix(ev.Message, "progress:") {
		if a.pending["check-updates"] {
			_, _ = fmt.Sscanf(ev.Message, "progress:%d/%d", &a.CheckDone, &a.CheckTotal)
		}
		return
	}
	switch ev.Type {
	case service.EventAccountChanged:
		evSite := strings.ToLower(strings.TrimSpace(ev.Site))
		if ev.Message == "logged_in" || ev.Message == "active" {
			if a.expiredNoticeShown != nil && evSite != "" {
				delete(a.expiredNoticeShown, evSite)
			}
			return
		}
		if ev.Message == "expired" || ev.Message == "logged_out" {
			a.handleSiteAccountLoss(evSite, ev.Message)
		}
	case service.EventTaskUpdated, service.EventLibraryChanged, service.EventSubscriptionChecked, service.EventLiveUpdated:
		a.postCards = nil
		a.cacheStateRev++
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
			a.syncLiveAccountsFromBackend()
		}
	}
}

func (a *App) handleSiteAccountLoss(evSite, kind string) {
	if evSite == string(site.Bilibili) && a.ImportSubModalOpen {
		a.ImportSubModalOpen = false
	}
	if !a.backend.HasAnyLogin() {
		reason := ""
		if kind == "expired" {
			reason = siteExpiredLoginPrompt(evSite)
		}
		if evSite == string(site.Douyin) || (evSite == "" && routeExclusiveSite(a.CurrentRoute) == string(site.Douyin)) {
			a.LoginSite = string(site.Douyin)
		} else if evSite == string(site.Bilibili) {
			a.LoginSite = string(site.Bilibili)
		}
		a.TriggerAuthExpired(reason)
		return
	}
	// At least one other site remains logged in: refresh local lists without forcing full-app logout.
	a.RefreshData()
	if kind != "expired" {
		return
	}
	if a.currentRouteRequiresSite(evSite) {
		a.SavedRoute = a.CurrentRoute
		if evSite != "" {
			a.LoginSite = evSite
		}
		a.CurrentRoute = RouteLogin
		a.LoginError = siteExpiredLoginPrompt(evSite)
		a.BannerError = a.LoginError
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
		a.BannerError = siteExpiredTopNotice(evSite)
		a.BannerLoginSite = evSite
	}
}

// TriggerAuthExpired transitions to the login view while preserving the current route and loaded data.
func (a *App) TriggerAuthExpired(reason string) {
	a.closePlayerOverlay()
	a.closeImageViewer()
	a.stopBGM()
	a.ImportSubModalOpen = false
	a.ConfirmLogoutOpen = false
	if a.CurrentRoute != RouteLogin {
		a.SavedRoute = a.CurrentRoute
	}
	if routeExclusiveSite(a.SavedRoute) == string(site.Douyin) {
		a.LoginSite = string(site.Douyin)
	}
	a.CurrentRoute = RouteLogin
	a.LoginError = reason
	a.BannerError = reason
	a.BannerLoginSite = ""
	a.backend.PauseAllActiveWork()
}

// CompleteLogin transitions back to SavedRoute (or RouteHome) after successful login while keeping loaded data intact.
func (a *App) CompleteLogin() {
	a.LoginError = ""
	a.BannerError = ""
	a.BannerLoginSite = ""
	a.ManualCookieInput = ""
	a.ManualRefreshInput = ""
	a.DouyinCookieInput = ""
	if a.expiredNoticeShown != nil {
		if a.backend.StatusOf(string(site.Bilibili)).LoggedIn {
			delete(a.expiredNoticeShown, string(site.Bilibili))
		}
		if a.backend.StatusOf(string(site.Douyin)).LoggedIn {
			delete(a.expiredNoticeShown, string(site.Douyin))
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
	if id, ok := site.ParseID(siteID); ok {
		a.LoginSite = string(id)
	}
	if a.CurrentRoute != RouteLogin {
		a.SavedRoute = a.CurrentRoute
	}
	a.LoginError = ""
	a.Navigate(RouteLogin)
}

// Navigate switches the active route and persists it in ui-state.json.
func (a *App) Navigate(route string) {
	route = normalizeAppRoute(route)
	a.BannerError = ""
	a.Opening = nil
	a.PostMenuKey = ""
	a.DouyinCardMenuID = ""
	a.DouyinUserSubMenuOpen = false
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
	if route != RouteLogin && route != RouteSettings && !a.backend.HasAnyLogin() {
		a.SavedRoute = route
		if routeExclusiveSite(route) == string(site.Douyin) {
			a.LoginSite = string(site.Douyin)
		}
		route = RouteLogin
	}
	if a.CurrentRoute != route && a.CurrentRoute != RouteLogin {
		a.PreviousRoute = a.CurrentRoute
	}
	a.CurrentRoute = route
	if route == RouteHome && a.backend.HasAnyLogin() {
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

// OpenSubscriptionDetail opens the /subscriptions/:id detail view for sub.
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
	if !a.backend.HasAnyLogin() {
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
	a.syncLiveAccountsFromBackend()
	a.reloadLibraryItems()
	a.refreshTasks()
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

func (a *App) refreshSubscriptions() {
	if !a.backend.HasAnyLogin() {
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
	if !a.backend.HasAnyLogin() {
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
	req := library.FeedPageRequest{
		Order:      "newest",
		CachedOnly: true,
		Site:       normalizeSiteFilter(a.FeedSiteFilter),
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
	var authorRefs []library.AuthorRef
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
	sortOrder := a.LibrarySort
	if sortOrder == "pub_ts" {
		sortOrder = "newest"
	}
	if a.LibrarySort == "oldest" {
		sortOrder = "oldest"
	}
	items, err := a.backend.ListLibraryItems(library.FeedFilter{
		Site:       normalizeSiteFilter(a.LibrarySiteFilter),
		Kind:       a.LibraryKindFilter,
		Mid:        a.LibraryCreatorMid,
		Authors:    authors,
		AuthorRefs: authorRefs,
		Keyword:    a.LibrarySearch,
		Sort:       sortOrder,
		Limit:      60,
		Offset:     a.libraryOffset,
	})
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

// PopulateSyntheticFeed populates n synthetic feed entries for virtualized list performance testing and screenshots.
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
	a.FeedScroll = ui.ScrollState{}
	a.feedMoreBefore = false
	a.feedMoreAfter = false
	clear(a.postCards)
	a.resetFeedListState()
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
		a.LastParseError = "粘贴链接、分享文本或 UID"
		return
	}
	a.openLink(trimmed, nil)
}

// OpenVideo opens a video from a card: the video page shows at once.
func (a *App) OpenVideo(bvid, title, authorName string, mid int64, coverURL, duration string) {
	if strings.TrimSpace(bvid) == "" {
		return
	}
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
	awemeID = strings.TrimSpace(awemeID)
	if awemeID == "" {
		return
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
	a.BannerLoginSite = ""
	a.openSeq++
	seq := a.openSeq
	if preview != nil {
		p := *preview
		p.raw, p.seq, p.Err = raw, seq, ""
		route := RouteVideo
		if p.Site == string(site.Douyin) {
			route = RouteDouyinWork
			if p.Kind == "user" || p.Kind == "live" {
				route = RouteDouyinUser
			}
		} else if p.Kind == "user" || p.Kind == "live" {
			route = RouteUser
		}
		if a.CurrentRoute != route {
			a.PreviousRoute = a.CurrentRoute
		}
		a.CurrentRoute = route
		a.Opening = &p
		a.PostMenuKey = ""
		a.DouyinCardMenuID = ""
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
		msg := userErrorText(err)
		if preview != nil && a.Opening != nil {
			a.Opening.Err = msg
			return
		}
		a.LastParseError = msg
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
		jobSite := firstNonEmptyStrUI(j.Site, string(site.Bilibili))
		requestGuarded(a, "history-thumbs-"+jobSite+":"+j.Kind+":"+j.ID, nil, func(ctx context.Context) (struct{}, error) {
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
	if res.Site == string(site.Douyin) || res.DouyinWork != nil || res.DouyinUser != nil || res.DouyinLive != nil {
		a.applyDouyinParseResult(res)
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
			a.PreviousRoute = a.CurrentRoute
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
		a.DouyinDescExpanded = false
		if a.CurrentRoute != RouteDouyinWork {
			a.PreviousRoute = a.CurrentRoute
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
		a.DouyinUserListState = ui.ListState{}
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
				a.DouyinWorksAuthExpired = true
				a.DouyinWorksHasMore = false
			}
		}
		if uv != nil && uv.LiveRoom != nil {
			a.mergeLiveAccountStates([]service.LiveAccountState{*uv.LiveRoom})
		}
		if a.CurrentRoute != RouteDouyinUser {
			a.PreviousRoute = a.CurrentRoute
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
			a.DouyinWorksAuthExpired = true
			a.DouyinWorksHasMore = false
			a.DouyinWorksError = ""
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
	}, func(err error) { a.UserDynamicsError = userErrorText(err); a.showError(err) })
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
	}, func(err error) { a.UserVideosError = userErrorText(err); a.showError(err) })
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
	a.syncLiveAccountsFromBackend()
}

// OpenItemImageViewer resolves registered relative media paths via Backend.MediaPath (INV-08) and opens the image viewer.
// A live photo's cached motion clip plays inside the viewer (DEC-046 ①).
func (a *App) OpenItemImageViewer(itemKey string, urls []string, relPaths []string, index int) {
	a.openPostImageViewer(itemKey, "", urls, relPaths, nil, index)
}

// openPostImageViewer opens a post's images. liveURLs holds each image's
// online motion clip (实况) when the post is not cached; a cached clip is
// looked up in the library item and preferred. Live photos get the viewer's
// play button and play in place, cached or not (DEC-046 ①).
func (a *App) openPostImageViewer(itemKey, siteID string, urls, relPaths, liveURLs []string, index int) {
	n := maxInt(len(urls), len(relPaths))
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
	kinds := make([]string, n)
	inlinePaths := make([]string, n)
	inlineURLs := make([]string, n)
	durations := make([]int, n)
	hasLive := false
	if a.backend != nil && itemKey != "" {
		if item, ok, err := a.backend.GetLibraryItem(itemKey); err == nil && ok {
			if siteID == "" {
				siteID = item.Site
			}
			for i := 0; i < n && i < len(relPaths); i++ {
				rel := relPaths[i]
				if rel == "" {
					continue
				}
				for _, m := range item.Media {
					if m.Path != rel || m.Motion == "" {
						continue
					}
					if p, err := a.backend.MediaPath(itemKey, m.Motion); err == nil {
						inlinePaths[i] = p
						hasLive = true
					}
					for _, clip := range item.Media {
						if clip.Path == m.Motion {
							durations[i] = clip.Duration
						}
					}
				}
			}
		}
	}
	for i := 0; i < n && i < len(liveURLs); i++ {
		if liveURLs[i] != "" {
			inlineURLs[i] = liveURLs[i]
			hasLive = true
		}
	}
	a.OpenImageViewer(urls, resolved, index)
	a.ImageViewer.ItemKey = itemKey
	a.ImageViewer.RelPaths = relPaths
	if hasLive {
		for i := range kinds {
			kinds[i] = "image"
			if inlinePaths[i] != "" || inlineURLs[i] != "" {
				kinds[i] = "live_photo"
			}
		}
		a.ImageViewer.InlinePageKinds = kinds
		a.ImageViewer.InlineMediaPaths = inlinePaths
		a.ImageViewer.InlineMediaURLs = inlineURLs
		a.ImageViewer.InlineDurations = durations
		a.ImageViewer.InlineSite = normalizeSiteIDUI(siteID)
	}
}

// OpenImageViewer opens the in-app image viewer overlay, decoding local files or fetched image bytes.
func (a *App) OpenImageViewer(urls []string, localPaths []string, index int) {
	if len(urls) == 0 && len(localPaths) == 0 {
		return
	}
	a.closeImageViewer()
	revision := a.imageViewerRevision
	ctx, cancel := context.WithCancel(a.ctx)
	a.imageViewerCancel = cancel
	// Ensure URLs use https and never leak external Referer.
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
	a.ImageViewer = ImageViewerState{Open: true, URLs: cleanURLs, LocalPaths: localPaths, Index: index, Zoom: 1, Fit: true, InlinePageIndex: -1}
	backend := a.backend
	requestGuarded(a, fmt.Sprintf("image-%d", revision), func() bool {
		return a.ImageViewer.Open && a.imageViewerRevision == revision && backend.HasAnyLogin()
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
					errors[i] = userErrorText(err)
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

// OpenPlayerOverlay opens the in-app media player overlay for an item, loading its registered MP4, danmaku XML, and subtitles via Backend.MediaPath.
func (a *App) OpenPlayerOverlay(item library.ItemMetadata) {
	a.openPlayerFile(item, "")
}

// openPlayerFile selects a registered file before opening the decoder once.
func (a *App) openPlayerFile(item library.ItemMetadata, selected string) {
	a.pauseBGM()
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
	if item.DouyinVideo != nil {
		add(item.DouyinVideo.File)
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
	// shows the dark layer and a spinner meanwhile.
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
// Implements the responsive icon-rail + context-panel + page layout per docs/ui.md §1 (DEC-039).
func (a *App) View(c *ui.Context) {
	c.SetTheme(TraceTheme())

	if a.PlayerOverlay.Open {
		a.player.Advance(0)
		if a.player.Snapshot(1, 1).Playing {
			c.Invalidate()
		}
	}
	if a.ImageViewer.Open && a.ImageViewer.InlineActive && a.viewerPlayer != nil {
		a.viewerPlayer.Advance(0)
		if a.viewerPlayer.Snapshot(1, 1).Playing {
			c.Invalidate()
		}
	}
	if a.BGMActiveKey != "" && a.bgmPlayer != nil {
		a.bgmPlayer.Advance(0)
		if a.bgmPlayer.Snapshot(1, 1).Playing {
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
	a.titleBar = c.TitleBar()
	contentW := max(w-railWidth-panelW, 1)
	pageW := a.pageWidth(contentW)
	a.pageGapRight = max(0, (contentW-pageW)/2)

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
				Background(ColorBgPanel).
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

	// Under the hidden title bar, the top of the rail and the panel moves the
	// window; double-clicking it maximizes (DEC-046 ④). The page's top bar
	// does the same.
	if a.titleBar.Height > 0 {
		ui.Box(c).Label("窗口拖动区").Absolute().Top(0).Left(0).Size(railWidth+panelW, a.titleBar.Height).DragWindow()
	}

	// Overlays & Modals
	if a.ConfirmLogoutOpen {
		a.renderLogoutConfirmOverlay(c, w, h)
	}
	if a.DeleteConfirm.Open {
		a.renderDeleteConfirmOverlay(c, w, h)
	}
	if a.RestartConfirmOpen && a.updater != nil {
		a.renderRestartConfirmOverlay(c, w, h)
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
