package ui

import (
	"context"
	"fmt"
	"os"
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
	"github.com/chialecode/trace/internal/transfer"
	"github.com/chialecode/trace/internal/uistate"
	"github.com/chialecode/trace/internal/update"
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
	RouteWeiboPost     = "/weibo/post"
	RouteWeiboUser     = "/weibo/user"
	RouteXPost         = "/x/post"
	RouteXUser         = "/x/user"
	RouteXList         = "/x/list"
	RouteLibrary       = "/library"
	RouteLibraryItem   = "/library/item"
	RouteTasks         = "/tasks"
	RouteSettings      = "/settings"
	RouteLogin         = "/login"
)

// ImageViewerState holds state for the in-app image and mixed-page viewer overlay.
type ImageViewerState struct {
	Open               bool
	URLs               []string
	FallbackURLs       [][]string
	LocalPaths         []string
	ItemKey            string
	RelPaths           []string
	InlineSite         string   // site whose CDN serves InlineMediaURLs ("bilibili" or "douyin")
	InlinePageKinds    []string // per page: "image" | "live_photo" | "video" | "unknown"
	InlineMediaPaths   []string // per page: resolved local motion/video file path
	InlineMediaURLs    []string // per page: online motion/video URL for uncached preview
	InlineDurations    []int    // per page: duration in seconds when known
	InlineLoopMuted    []bool   // per page: true for looping muted GIF MP4s (e.g. X animated_gif)
	InlineActive       bool
	InlinePageIndex    int
	InlineLoading      bool
	InlineError        string
	InlineMediaPath    string
	InlineLoopPaused   bool
	Index              int
	Zoom               float32
	PanX               float32
	PanY               float32
	Fit                bool
	Dragging           bool
	PointerX           float32
	PointerY           float32
	Bitmaps            []*ui.Bitmap
	Errors             []string
	NoWatermarkPaths   []string     // per page: resolved local companion watermark-free image path
	NoWatermarkBitmaps []*ui.Bitmap // decoded companion watermark-free bitmaps (parallel to Bitmaps)
	NoWatermarkErrors  []string     // per page: companion load error (never blocks original)
	ShowNoWatermark    bool         // false = original (default), true = watermark-free companion when available
	LastActiveAt       time.Time
	ControlsHidden     bool
}

// PlayerOverlayState holds state for the in-app media player overlay.
type PlayerOverlayState struct {
	Open               bool
	ItemKey            string
	Title              string
	MediaPath          string
	Loading            bool
	LoadError          string
	Segment            int
	Segments           []string
	Chapters           []library.VideoChapter
	PendingSeekSec     float64
	DecoderMissing     bool
	MissingReason      string
	SeekNormalized     float64
	DanmakuEnabled     bool
	DanmakuOpacity     float64
	DanmakuArea        string
	DanmakuDensity     string
	Volume             float64
	VolumeMenuOpen     bool
	SubtitleLang       string
	SubtitleOptions    []string
	MoreMenuOpen       bool
	Fullscreen         bool
	LoopMuted          bool
	LoopPaused         bool
	PreviewMode        bool
	PreviewSite        string
	PreviewItemKey     string
	PreviewPendingKey  string
	PreviewCacheQueued bool
	PreviewCacheError  string
	PreviewOnCache     func(context.Context) error
	LastActiveAt       time.Time
	ControlsHidden     bool
	volumeScrubbing    bool
	opacityScrubbing   bool
}

// DeleteConfirmState holds state for the library item deletion confirmation modal.
type DeleteConfirmState struct {
	Open        bool
	Batch       bool
	Item        library.ItemMetadata
	TargetKeys  []string
	Plan        library.DeletePlan
	FileCount   int
	TotalBytes  int64
	DeleteFiles bool
}

// ForceRecacheConfirmState holds state for the manual force-redownload confirmation modal.
type ForceRecacheConfirmState struct {
	Open    bool
	ItemKey string
	Title   string
}

// HomeUpdatedActor is a subscription as a row of the Home panel, with its
// newest cached post under the site filter (DEC-053 ②).
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

type compactLibEntry struct {
	dotStatus string
	dotLabel  string
	relCover  string
	item      *library.ItemMetadata
}

type libraryPanelRow struct {
	isSub      bool
	sub        subscription.Subscription
	author     library.ItemAuthor
	site       string
	uid        string
	authorKey  string
	itemCount  int
	totalBytes int64
}

type libraryPanelSig struct {
	subsRev      int
	subsN        int
	subsFirst    *subscription.Subscription
	statsRev     int
	authorsN     int
	authorsFirst *library.ItemAuthor
	siteFilter   string
	subFilter    string
	sortBySize   bool
}

// XUserTabState holds independent pagination, error, and scroll state for one
// X user profile tab ("posts" | "replies" | "media" | "highlights") (DEC-092).
type XUserTabState struct {
	Posts          []service.XPostDTO
	NextCursor     string
	HasMore        bool
	Loaded         bool
	Error          string
	AuthExpired    bool
	UncertainEmpty bool
	BottomTips     bool
	BottomTipsText string
	ListState      *ui.ListState
}

// App manages the MyGo immediate-mode UI tree and state for TRACE.
type App struct {
	mu                   sync.Mutex
	ctx                  context.Context
	cancel               context.CancelFunc
	dispatch             func(func())
	pending              map[string]bool
	closed               bool
	fullscreen           func(bool)
	fileDialog           FileDialogHandler
	imageViewerRevision  uint64
	imageViewerCancel    context.CancelFunc
	playerPreviewCancel  context.CancelFunc
	runtimeDiagnostics   service.Diagnostics
	runtimeLogLines      []string
	runtimeLogError      string
	runtimeLogsNextRead  time.Time
	RuntimeLogsListState ui.ListState
	visibleLiveNextRead  map[int64]time.Time
	visibleLiveStopped   bool
	backend              service.BackendAPI
	player               *player.Controller
	bgmPlayer            *player.Controller
	viewerPlayer         *player.Controller
	livePreviewPlayer    *player.Controller
	unsub                func()

	// Coalesced backend event loop & single-flight async derived-data refresh.
	eventMu                     sync.Mutex
	pendingEvents               []service.Event
	eventFlushQueued            bool
	bgRefreshMu                 sync.Mutex
	bgRefreshRunning            bool
	bgRefreshPending            bool
	bgRefreshNext               bgRefreshSpec
	bgRefreshPendingRes         *bgRefreshResult
	bgRefreshApplyQueued        bool
	bgRefreshDefer              bool
	bgRefreshDeferred           bgRefreshSpec
	libraryStatsDirty           bool
	libraryStatsDirtyGen        uint64
	libraryStatsRequestedGen    uint64
	backfillSummaryDirty        bool
	backfillSummaryDirtyGen     uint64
	backfillSummaryRequestedGen uint64
	accountWorksDirty           bool
	accountWorksDirtyGen        uint64
	accountWorksRequestedGen    uint64
	libraryItemsGen             uint64

	// Cached diagnostics/update metadata to avoid per-frame SQLite & Registry reads.
	cachedDiag           service.Diagnostics
	cachedDiagAt         time.Time
	cachedInstalledDev   string
	cachedInstalledDevAt time.Time
	cachedUpdateHist     update.History
	cachedUpdateHistAt   time.Time

	// In-memory bitmap cache for local covers/thumbnails, freed on CloseToTray (DEC-030)
	images             bitmapCache
	imageRetry         map[string]imageRetry
	pendingLocalAvatar bool
	// homeBaselineTs is the newest post seen before this session, the
	// initial "seen" of subscriptions without a homeSeen entry; homeSeenTs
	// is the newest seen so far; homeSeenLocal is homeSeen as written.
	homeBaselineTs     int64
	homeSeenTs         int64
	homeBaselineLoaded bool
	homeSeenLocal      map[string]int64
	homeSeenRev        int
	// Reading positions of Home per site filter (DEC-053 ①).
	homePosRestored   map[string]bool
	homeFilterViews   map[string]*homeFeedView
	homePosSaved      map[string]uistate.HomePosition
	homePosSeen       uistate.HomePosition
	homePosSeenSite   string
	homePosSeenAt     time.Time
	homeRestorePhase  int
	homeRestoreOffset int
	homeListBounds    ui.Rect
	// The subscriptions of the Home panel (DEC-053 ②): the order fixed on
	// entering Home per site filter, and the rows built from it.
	homeSubOrder     map[string][]string
	homeSubRows      []HomeUpdatedActor
	homeSubRowsSig   homeSubsSig
	homeSubRowsValid bool
	homeSubIndex     map[string]int
	HomeSubListState ui.ListState
	homeStripScroll  ui.ScrollState
	subsRev          int
	statsRev         int
	// Indexed avatar face URLs built lazily from Subscriptions/FeedItems/LiveAccounts.
	faceIndexSig   faceIndexSig
	faceIndexValid bool
	biliFaceIndex  map[int64]string
	acctFaceIndex  map[string]string
	// Indexed subscription ownership for O(1) lookups on user pages.
	subOwnerIndexRev   int
	subOwnerIndexN     int
	subOwnerIndexFirst *subscription.Subscription
	subOwnerByKey      map[string]subscription.Subscription
	subOwnerByMid      map[int64]subscription.Subscription
	// Library sidebar virtualized source rows.
	LibraryPanelListState ui.ListState
	libraryPanelRowsCache []libraryPanelRow
	libraryPanelSig       libraryPanelSig
	libraryPanelValid     bool
	// Cached library metadata for compact video rows & /video detail keyed by cacheStateRev.
	compactLibCache    map[string]compactLibEntry
	compactLibCacheRev int
	// Cached index slice for /user virtual lists.
	userIdxCacheKey string
	userIdxCacheRev int
	userIdxCacheLen int
	userIdxCache    []int
	// Cached import plan copy-bytes total.
	planBytesPlan *transfer.Plan
	planBytesVal  int64
	// postCards caches the card built for each feed entry or dynamic, so
	// scrolling does not query the library for every visible row on every
	// frame. Cleared whenever tasks or the library change.
	postCards  map[string]PostCardData
	imageQueue chan struct{}

	// Navigation & per-site auth guard
	CurrentRoute           string
	SavedRoute             string
	PreviousRoute          string
	navBackStack           []string
	BannerError            string
	BannerLoginSite        string
	BannerInfo             string
	notificationStack      []floatingNotice
	notificationNextID     uint64
	bannerInfoTrackedText  string
	bannerErrorTrackedText string
	bannerErrorTrackedSite string
	updateNoticeTrackedKey string
	lastAnnouncedNoticeID  uint64
	expiredNoticeShown     map[string]bool

	// Expandable post/description states & PostCard "⋯" menu
	ExpandedPosts map[string]bool
	PostMenuKey   string

	// Parse Page
	ParsePageInput          string
	ParseInputComposing     bool
	LastParseError          string
	HistoryItems            []storage.HistoryEntry
	HistoryHeaderMenuOpen   bool
	HistoryClearConfirmOpen bool
	HistoryItemMenuID       string

	// Login Page
	LoginSite             string // "bilibili" | "douyin" | "weibo" | "x"
	LoginTab              string // "qr" | "cookie"
	WeiboLoginTab         string // "qr" | "cookie"
	QRSession             bilibili.QRSession
	QRBitmap              *ui.Bitmap
	QRStatusText          string
	WeiboQRSession        service.WeiboQRSessionDTO
	WeiboQRBitmap         *ui.Bitmap
	WeiboQRStatusText     string
	WeiboQRState          string // "" | "waiting" | "scanned" | "expired" | "canceled" | "error"
	ManualCookieInput     string
	ManualCookieComposing bool
	ManualRefreshInput    string
	DouyinCookieInput     string
	DouyinCookieComposing bool
	WeiboCookieInput      string
	WeiboCookieComposing  bool
	XCookieInput          string
	XCookieComposing      bool
	LoginError            string
	qrRevision            uint64
	qrCancel              context.CancelFunc
	weiboQRRevision       uint64
	weiboQRCancel         context.CancelFunc

	// Home Feed (DEC-037, DEC-040)
	FeedItems            []library.FeedEntry
	FeedListState        *ui.ListState
	FeedScroll           ui.ScrollState
	feedMoreBefore       bool
	feedMoreAfter        bool
	FeedError            string
	feedFailedBefore     bool
	FeedSortOrder        string // always "newest"
	FeedSiteFilter       string // "all" | "bilibili" | "douyin" | "weibo"
	FeedAuthorsFilter    []int64
	FeedAuthorRefsFilter []library.AuthorRef
	HomeActorFilter      *HomeUpdatedActor
	HomeRefreshRunning   bool
	// savedMain is the main timeline while a subscription's feed shows.
	savedMain *homeFeedView

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
	DouyinUserListState    *ui.ListState
	DouyinUserSubMenuOpen  bool

	// Weibo Post, Video & Creator Browsing
	CurrentWeiboPost                *service.WeiboPostView
	CurrentWeiboVideo               *service.WeiboVideoView
	CurrentWeiboUser                *service.WeiboUserView
	CurrentWeiboPosts               []service.WeiboPostView
	WeiboPostsNextCursor            string
	WeiboPostsHasMore               bool
	WeiboPostsError                 string
	WeiboPostsAuthExpired           bool
	WeiboPostsUncertainEmpty        bool
	WeiboBottomTipsVisible          bool
	WeiboBottomTipsText             string
	WeiboUserListState              *ui.ListState
	WeiboUserSubMenuOpen            bool
	weiboFilteredEmpty              bool
	WeiboEditHistoryExpanded        bool
	WeiboEditHistoryMid             string
	WeiboEditHistoryError           string
	WeiboEditHistoryItems           []service.WeiboEditRevisionDTO
	WeiboOfflineEditHistoryExpanded bool
	WeiboOfflineEditHistoryKey      string

	// X Post & Creator Browsing
	CurrentXPost         *service.XPostDTO
	CurrentXUser         *service.XUserDTO
	XUserTab             string // "posts" | "replies" | "media" | "highlights"
	XUserTabs            map[string]*XUserTabState
	xUserTabsOwner       string
	CurrentXPosts        []service.XPostDTO
	XPostsNextCursor     string
	XPostsHasMore        bool
	XPostsError          string
	XPostsAuthExpired    bool
	XPostsUncertainEmpty bool
	XBottomTipsVisible   bool
	XBottomTipsText      string
	XUserListState       *ui.ListState
	XUserSubMenuOpen     bool
	xFilteredEmpty       bool
	XList                XListViewState
	XUserLists           XUserListsModalState

	// Mine is the in-memory state of 我的 (never written to ui-state.json).
	Mine             MineState
	DouyinCardMenuID string

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

	// panelShown: this frame has a context panel beside the rail.
	panelShown bool
	// titleBar is the room the window buttons take under the hidden title
	// bar (DEC-046 ④); zero in tests, full screen and framed windows.
	titleBar ui.TitleBar
	// pageGapRight is the white space right of the centered page column.
	pageGapRight float32
	// SubPanelListState scrolls the subscriptions in the context panel.
	SubPanelListState ui.ListState

	// Opening is the link being opened from a card or entry: its page shows
	// at once with what the card knew, and fills in when OpenLink returns.
	// openSeq makes the newest click win.
	Opening       *OpeningPreview
	openSeq       int
	playerOpenSeq int

	// User Space Page
	CurrentUser                *bilibili.UserSpace
	UserTab                    string // "dynamics" | "videos"
	UserDynamics               []bilibili.DynamicItem
	UserDynamicsNext           string
	UserDynamicsHasMore        bool
	UserDynamicsError          string
	UserVideos                 []bilibili.UserVideoItem
	UserVideosPage             int
	UserVideosHasMore          bool
	UserVideosError            string
	UserVideosOrder            string // "pubdate" | "click" | "stow"
	UserVideosKeywordInput     string
	UserVideosKeywordComposing bool
	UserVideosAppliedKeyword   string
	UserVideosTID              int
	UserVideosZones            []bilibili.UserVideoZone
	userVideosSeq              uint64
	UserDynListState           *ui.ListState
	UserVidListState           *ui.ListState
	// UserListFilter is 全部 ("all") or 未缓存 ("uncached") on user pages (DEC-046 ⑧).
	UserListFilter  string
	userFilterCache uncachedFilterCache
	// douyinFilteredEmpty: 未缓存 left no loaded work on the Douyin user page.
	douyinFilteredEmpty bool
	// cacheStateRev changes whenever tasks or the library change.
	cacheStateRev int
	// libraryRev changes ONLY when the library changes (not on 1s download ticks).
	libraryRev int
	// libraryItemsDirty tracks off-route library changes to reload lazily on /library.
	libraryItemsDirty bool
	// cardLocals caches work cards' dots and local covers for cacheStateRev.
	cardLocals    map[string]workCardLocal
	cardLocalsRev int

	// Subscriptions Page
	Subscriptions  []subscription.Subscription
	SubListState   ui.ListState
	SubSearchQuery string
	subListQuery   string
	SubSort        string // "default" | "recent"
	subListSort    string
	CheckDone      int
	CheckTotal     int
	SelectedSubID  string
	SelectedSubMid int64
	SubDetailOpen  bool
	// SubAccountsOpen shows 订阅 › 全部账号 (accounts by site, DEC-057).
	SubAccountsOpen bool
	// SubChecksOpen shows 订阅 › 检查计划 (DEC-081, moved from Tasks).
	SubChecksOpen        bool
	SubAccountsSite      string
	SubAccountsListState ui.ListState
	SubMoreMenuOpen      bool
	SubManageMenuOpen    bool
	SubManageModalMode   string // "" | "rename" | "note" | "avatar" | "delete"
	SubAvatarInput       string
	SubDetailTab         string // "content" | "accounts"
	SubAccountMenuKey    string
	SubFeedSubID         string
	SubFeedAccountKey    string // "" = all linked accounts, or "<site>:<uid>" for single-account view (DEC-095)
	SubFeedRefsKey       string
	SubFeedItems         []library.FeedEntry
	SubFeedListState     *ui.ListState
	// checkPlan is 订阅 › 检查计划 as last read, at checkPlanAt.
	checkPlan   []service.AccountCheckRow
	checkPlanAt time.Time
	// checkingAccount is the "<site>:<uid>" a 检查计划 row is checking.
	checkingAccount    string
	SubFeedMoreAfter   bool
	SubFeedError       string
	subFeedDirty       bool
	SubRuleMenuOpen    bool
	SubCustomTagsSubID string
	// RuleBatch manages subscriptions of one capability tag; MultiTagBatch
	// batch-adds, removes, or replaces multiple capability tags across subscriptions.
	RuleBatch     RuleBatchDialogState
	MultiTagBatch MultiTagBatchDialogState
	// LibraryRefresh is the media library's "刷新" and its result dialog.
	LibraryRefresh LibraryRefreshState
	// Remote is Settings › 远程; Transfer is 媒体库 › 导入…/导出…; SubFile is
	// 订阅 › 导出订阅…/导入订阅… (docs/design/remote.md §6.3, §6.4, §7).
	Remote            RemoteUIState
	remoteGen         uint64
	Transfer          TransferUIState
	transferImportGen uint64
	transferExportGen uint64
	SubFile           SubFileUIState
	subExportGen      uint64
	subImportGen      uint64
	SubLinks          SubLinksModalState
	AddSubDialogOpen  bool
	AddSubPreset      TrackingPresetPickerState
	// SubscribePicker subscribes to an account: into an existing
	// subscription or a new one (DEC-058).
	SubscribePicker     SubscribePickerState
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
	ImportSubModalOpen  bool
	ImportPreset        TrackingPresetPickerState
	importFollowingsSeq uint64
	ImportSite          string // "bilibili" | "weibo" | "x"
	ImportFollowings    []bilibili.FollowingUser
	ImportCreators      []service.FollowingCreatorDTO
	ImportPage          int
	ImportCursor        string
	ImportHasMore       bool
	ImportGroup         string
	ImportGroups        []bilibili.RelationTag
	ImportGroupsDTO     []service.FollowingGroupDTO
	ImportGroupsEnabled bool
	ImportSearch        string
	ImportError         string
	ImportSelectedMids  map[int64]bool
	ImportSelectedKeys  map[string]bool
	LiveRooms           []live.RoomState
	LiveAccounts        []service.LiveAccountState

	// Library Page
	LibraryItems     []library.ItemMetadata
	LibraryListState ui.ListState
	// queueCovers caches the cover of items in the queues' recent lists.
	queueCovers    map[string][2]string
	queueCoversRev int
	// dismissedLive caches ui-state's DismissedLiveSessions (nil: not loaded).
	dismissedLive     map[string]bool
	LibraryKindFilter string // "" | "image" | "video" | "dynamic" | "live" | "forward"
	LibrarySiteFilter string // "all" | "bilibili" | "douyin"
	LibraryCreatorMid int64
	LibraryAuthorKey  string // "<site>:<uid>"
	LibrarySubFilter  string
	LibrarySort       string // "pub_ts" | "oldest"
	LibrarySearch     string
	LibraryHasMore    bool
	LibraryError      string
	libraryStatistics library.Statistics
	// accountWorks is each subscribed account's works total (from its last
	// parse) and cached count, keyed "site:uid" (DEC-061).
	accountWorks map[string]service.AccountWorks
	// videoCacheFor is the creator page the open video came from (DEC-061).
	videoCacheFor           videoCacheContext
	douyinCacheFor          douyinCacheContext
	weiboCacheFor           weiboCacheContext
	xCacheFor               xCacheContext
	libraryOffset           int
	LibraryAuthorMenu       bool
	LibrarySourceSortBySize bool
	LibrarySelectMode       bool
	LibrarySelectedKeys     map[string]bool
	ExpandedItemKey         string
	DeleteConfirm           DeleteConfirmState
	ForceRecacheConfirm     ForceRecacheConfirmState
	BackfillSummary         service.BackfillSummaryDTO

	// Tasks Page (4 sections: "active" | "live" | "attention" | "records", DEC-081)
	TasksSection              string // "active" | "live" | "attention" | "records" (legacy keys normalized in effectiveTasksSection)
	TasksActiveTab            string // "downloads" | "queue" sub-tab inside "active"
	TasksQueueSiteFilter      string // "" | "all" | "bilibili" | "douyin" | "weibo" | "x"
	SelectedCacheQueueID      string
	SelectedLiveIncidentKey   string
	ExpandedLiveIncidentKeys  map[string]bool
	liveLibItemsCache         []library.ItemMetadata
	liveLibItemsCacheRev      int
	CacheQueue                []service.CacheQueueTask
	CacheConflicts            []service.CacheConflictDTO
	CacheConflictPendingCount int
	conflictsLoadedBackend    service.BackendAPI
	conflictsLoadedOnTasks    bool
	ConflictBatchChoice       string
	ConflictBatchConfirmOpen  bool
	ConflictReplaceConfirmID  string
	ConflictActionError       string
	ConflictDiff              ConflictDiffModalState
	Tasks                     []download.Task
	TasksListState            ui.ListState
	ExpandedTaskID            string
	TaskRemoveConfirmID       string
	TaskActionError           string
	UpgradeCandidates         []library.ItemMetadata
	UpgradeDone               int
	UpgradeTotal              int
	upgradeCancel             context.CancelFunc
	LivePreviewKey            string
	LivePreviewTitle          string
	LivePreviewPartPath       string
	LivePreviewPartIndex      int
	LivePreviewLoading        bool
	LivePreviewUnsupported    bool
	LivePreviewReason         string
	LivePreviewError          string
	livePreviewOpenSeq        int
	livePreviewCancel         context.CancelFunc
	livePreviewOpener         func(ctx context.Context, path string, opts player.LivePreviewOptions) (player.MediaDecoder, error)

	// Settings Page
	SettingsCategory string // "account" | "download" | "quality" | "rules" | "subscription" | "storage" | "general" | "network" | "remote" | "mobile" | "logs" | "update" | "about"
	DraftSettings    settings.Settings
	// Mobile companion (Settings › 移动端, FR-MOB-001..003).
	companion CompanionAPI
	Mobile    MobileUIState
	// Settings → 网络: the custom proxy address being typed and why it was refused.
	ProxyDraftURL          string
	ProxyError             string
	proxyEditing           bool
	proxyDraftLoaded       bool
	proxyComposing         bool
	CacheDirInput          string
	AddLibraryRootInput    string
	MoveExistingFiles      bool
	ConfirmLogoutOpen      bool
	ConfirmLogoutSite      string
	SettingsPasteSite      string // "" | "douyin" | "weibo" | "x"
	SettingsPasteInput     string
	SettingsPasteComposing bool
	SettingsPasteError     string

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
	livePreviewCtrl := player.NewController(player.Options{
		AudioSink: newCompanionAudioSink(audioSink),
		Volume:    uiSt.Player.Volume,
		Muted:     uiSt.Player.Muted,
	})

	initialRoute := normalizeAppRoute(uiSt.LastRoute)
	if initialRoute == RouteLibraryItem {
		initialRoute = RouteLibrary
	}
	loginSite := string(site.Bilibili)
	if excl := routeExclusiveSite(initialRoute); excl != "" {
		loginSite = excl
	}

	ctx, cancel := context.WithCancel(context.Background())
	app := &App{
		ctx:                      ctx,
		cancel:                   cancel,
		pending:                  make(map[string]bool),
		imageQueue:               make(chan struct{}, 2),
		ExpandedPosts:            make(map[string]bool),
		expiredNoticeShown:       make(map[string]bool),
		backend:                  backend,
		player:                   ctrl,
		bgmPlayer:                bgmCtrl,
		viewerPlayer:             viewerCtrl,
		livePreviewPlayer:        livePreviewCtrl,
		CurrentRoute:             initialRoute,
		LoginSite:                loginSite,
		LoginTab:                 "qr",
		WeiboLoginTab:            "qr",
		FeedSortOrder:            "newest",
		FeedSiteFilter:           normalizeSiteFilter(uiSt.FeedFilter.Site),
		SelectedPartPages:        make(map[int]bool),
		UserTab:                  "dynamics",
		SubDetailTab:             normalizeSubDetailTab(uiSt.SubDetailTab),
		SubSort:                  uistate.NormalizeSubSort(uiSt.SubSort),
		subListSort:              uistate.NormalizeSubSort(uiSt.SubSort),
		ImportSite:               string(site.Bilibili),
		ImportSelectedMids:       make(map[int64]bool),
		ImportSelectedKeys:       make(map[string]bool),
		LibrarySiteFilter:        "all",
		LibrarySort:              "pub_ts",
		LibrarySelectedKeys:      make(map[string]bool),
		TasksSection:             TasksSectionActive,
		TasksActiveTab:           "downloads",
		ExpandedLiveIncidentKeys: make(map[string]bool),
		ConflictBatchChoice:      service.DefaultConflictChoice,
		SettingsCategory:         "account",
		DraftSettings:            s,
		CacheDirInput:            s.CacheDir,
		MoveExistingFiles:        true,
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
	app.resetSubFeedListState()
	app.UserDynListState, app.UserVidListState, app.DouyinUserListState, app.WeiboUserListState, app.XUserListState = &ui.ListState{}, &ui.ListState{}, &ui.ListState{}, &ui.ListState{}, &ui.ListState{}
	app.RefreshData()
	app.applyInitialSubscriptionRoute(uiSt.LastRoute)
	return app
}

func (a *App) resetFeedListState() {
	a.FeedListState = &ui.ListState{
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
	// Closing the window, to the tray or to quit (--quit-for-update too),
	// keeps where Home was read (DEC-053 ①).
	if a.HomeActorFilter == nil {
		a.flushHomePosition()
	}
	a.setPlayerFullscreen(false)
	a.clearUnsentLoginInputs()
	a.Remote.CodeInput = ""
	a.Remote.TokenInput = ""

	a.mu.Lock()
	a.closed = true
	a.cancel()
	if a.imageViewerCancel != nil {
		a.imageViewerCancel()
		a.imageViewerCancel = nil
	}
	if a.upgradeCancel != nil {
		a.upgradeCancel()
		a.upgradeCancel = nil
	}
	if a.livePreviewCancel != nil {
		a.livePreviewCancel()
		a.livePreviewCancel = nil
	}
	if a.qrCancel != nil {
		a.qrCancel()
		a.qrCancel = nil
	}
	if a.weiboQRCancel != nil {
		a.weiboQRCancel()
		a.weiboQRCancel = nil
	}
	qrFlowID := a.QRSession.FlowID
	a.QRSession = bilibili.QRSession{}
	weiboQRFlowID := a.WeiboQRSession.FlowID
	a.WeiboQRSession = service.WeiboQRSessionDTO{}
	unsub := a.unsub
	a.unsub = nil
	a.ReleaseBitmapsLocked()
	a.imageRetry = nil
	a.mu.Unlock()
	a.clearPendingEventLoopState()

	if qrFlowID != "" {
		a.backend.CancelQRLogin(qrFlowID)
	}
	if weiboQRFlowID != "" {
		a.backend.CancelWeiboQRLogin(weiboQRFlowID)
	}
	if unsub != nil {
		unsub()
	}
	for _, p := range []*player.Controller{a.player, a.bgmPlayer, a.viewerPlayer, a.livePreviewPlayer} {
		if p != nil {
			p.SetFrameNotify(nil)
			_ = p.Close()
		}
	}
}

// ReleaseBitmapsLocked frees any allocated UI bitmaps when closing to tray (DEC-030).
func (a *App) ReleaseBitmapsLocked() {
	a.QRBitmap = nil
	a.WeiboQRBitmap = nil
	a.ImageViewer.Bitmaps = nil
	a.images.reset()
	a.postCards = nil
}

// BindEventLoop subscribes to backend events and invokes updateFn (typically win.Update) on changes.
func (a *App) BindEventLoop(updateFn func(func())) {
	a.mu.Lock()
	a.dispatch = updateFn
	a.mu.Unlock()
	if updateFn != nil {
		// A frame decoded off the UI thread after a paused seek needs a
		// redraw; while playing, the view redraws every frame anyway.
		notify := func() {
			if a.isClosed() {
				return
			}
			updateFn(func() {})
		}
		a.player.SetFrameNotify(notify)
		if a.bgmPlayer != nil {
			a.bgmPlayer.SetFrameNotify(notify)
		}
		if a.viewerPlayer != nil {
			a.viewerPlayer.SetFrameNotify(notify)
		}
		if a.livePreviewPlayer != nil {
			a.livePreviewPlayer.SetFrameNotify(notify)
		}
	}
	if a.unsub != nil {
		a.unsub()
	}
	a.clearPendingEventLoopState()
	a.unsub = a.backend.SubscribeEvents(func(ev service.Event) {
		a.enqueueBackendEvent(ev, updateFn)
	})
}

func (a *App) purgeMissingImages() {
	a.mu.Lock()
	a.images.purgeNil(a.pending)
	a.mu.Unlock()
}

func (a *App) handleBackendEvent(ev service.Event) {
	if ev.Type == service.EventSubscriptionChecked && strings.HasPrefix(ev.Message, "progress:") {
		if a.isPending("check-updates") {
			_, _ = fmt.Sscanf(ev.Message, "progress:%d/%d", &a.CheckDone, &a.CheckTotal)
		}
		return
	}
	if ev.Type == service.EventLibraryChanged && strings.HasPrefix(ev.Message, "feed_seen:") {
		// Shared read state updated from mobile companion (DEC-085 ⑥).
		// Only ui-state HomeSeen changed; skip heavy library/task SQLite scans.
		a.homeSeenLocal = a.backend.GetUIState().HomeSeen
		a.homeSeenRev++
		a.homeSubRowsValid = false
		return
	}
	switch ev.Type {
	case service.EventSettingsChanged:
		a.cachedDiagAt = time.Time{}
		a.cachedUpdateHistAt = time.Time{}
		a.markBackfillSummaryDirty()
		a.ensureBackfillSummaryFresh()
	case service.EventAccountChanged:
		a.cacheStateRev++ // dots read "登录后查看" while signed out
		a.cachedDiagAt = time.Time{}
		a.purgeMissingImages()
		evSite := strings.ToLower(strings.TrimSpace(ev.Site))
		// 我的 keeps nothing across a sign-out, an expiry or another account.
		a.mineOnAccountChanged(evSite)
		if ev.Message == "logged_in" || ev.Message == "active" {
			if a.expiredNoticeShown != nil && evSite != "" {
				delete(a.expiredNoticeShown, evSite)
			}
			return
		}
		if ev.Message == "expired" || ev.Message == "logged_out" {
			a.handleSiteAccountLoss(evSite, ev.Message)
		}
	case service.EventTransferUpdated:
		a.onTransferUpdated(ev.Message)
	case service.EventCacheQueueUpdated:
		_, prevBackfillActive := a.activeWeiboBackfillQueue()
		a.cacheStateRev++
		a.CacheQueue = a.backend.ListCacheQueue()
		_, curBackfillActive := a.activeWeiboBackfillQueue()
		if prevBackfillActive && !curBackfillActive {
			a.markBackfillSummaryDirty()
			a.ensureBackfillSummaryFresh()
		}
	case service.EventCacheConflictsChanged:
		a.cacheStateRev++
		a.refreshCacheConflicts()
	case service.EventTaskUpdated, service.EventLibraryChanged, service.EventSubscriptionChecked, service.EventLiveUpdated:
		a.postCards = nil
		a.cacheStateRev++
		a.cachedDiagAt = time.Time{}
		if strings.HasSuffix(ev.Message, ":storage") {
			a.notifyError("任务状态写入失败，请检查存储后重试", "")
		}
		a.refreshTasks()
		if ev.Type == service.EventLibraryChanged {
			a.libraryRev++
			a.purgeMissingImages()
			a.subFeedDirty = true
			if ev.Message == "refreshed" && (a.HomeRefreshRunning || a.LibraryRefresh.Running) {
				a.libraryItemsDirty = a.CurrentRoute != RouteLibrary && a.CurrentRoute != RouteLibraryItem
			} else {
				if len(a.FeedItems) == 0 && a.HomeActorFilter == nil {
					a.loadInitialFeed()
				}
				if a.HomeActorFilter == nil && len(a.FeedItems) > 0 {
					a.feedMoreBefore = true
				}
				a.scheduleLibraryChangedRefresh()
			}
		}
		if ev.Type == service.EventSubscriptionChecked {
			a.purgeMissingImages()
			a.subFeedDirty = true
			a.refreshSubscriptionsListOnly()
			a.markAccountWorksDirty()
			a.ensureAccountWorksFresh()
		}
		if ev.Type == service.EventLiveUpdated {
			a.LiveRooms = bilibiliLiveRooms(a.backend.ListLiveRooms())
			a.syncLiveAccountsFromBackend()
			a.syncLivePreviewState()
		}
	}
}

// RefreshData reloads lists from local SQLite / stores without making network requests.
func (a *App) RefreshData() {
	if !a.backend.HasAnyLogin() {
		return
	}
	a.postCards = nil
	a.cacheStateRev++
	a.libraryRev++
	a.libraryItemsDirty = false
	a.libraryItemsGen++
	a.subFeedDirty = true
	a.cachedDiagAt = time.Time{}
	a.HistoryItems = a.backend.ListHistory()
	a.refreshLibraryStatistics()
	a.refreshBackfillSummary()
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
	a.LiveRooms = bilibiliLiveRooms(a.backend.ListLiveRooms())
	a.syncLiveAccountsFromBackend()
	a.liveLibItemsCacheRev = 0
	a.reloadLibraryItems()
	a.refreshTasks()
	a.refreshCacheConflicts()
	a.refreshTransferJobs()
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

	if a.PlayerOverlay.Open && a.player != nil {
		a.player.Advance(0)
		snap := a.player.Snapshot(1, 1)
		if snap.Playing {
			c.Invalidate()
		} else if a.PlayerOverlay.LoopMuted && !a.PlayerOverlay.LoopPaused && snap.Open && snap.DurationSec > 0 && snap.PositionSec >= snap.DurationSec-0.05 {
			a.player.SetMuted(true)
			a.player.Seek(0)
			a.player.Play()
			c.Invalidate()
		}
	}
	if a.ImageViewer.Open && a.ImageViewer.InlineActive && a.viewerPlayer != nil {
		a.viewerPlayer.Advance(0)
		snap := a.viewerPlayer.Snapshot(1, 1)
		if snap.Playing {
			c.Invalidate()
		} else if a.viewerInlinePageIsLoopMuted(a.ImageViewer.InlinePageIndex) && !a.ImageViewer.InlineLoopPaused && snap.Open && snap.DurationSec > 0 && snap.PositionSec >= snap.DurationSec-0.05 {
			a.viewerPlayer.SetMuted(true)
			a.viewerPlayer.Seek(0)
			a.viewerPlayer.Play()
			c.Invalidate()
		}
	}
	if a.BGMActiveKey != "" && a.bgmPlayer != nil {
		a.bgmPlayer.Advance(0)
		if a.bgmPlayer.Snapshot(1, 1).Playing {
			c.Invalidate()
		}
	}
	if a.LivePreviewKey != "" {
		a.syncLivePreviewState()
		if a.LivePreviewKey != "" && a.livePreviewPlayer != nil {
			a.livePreviewPlayer.Advance(0)
			if a.livePreviewPlayer.Snapshot(1, 1).Playing || a.LivePreviewLoading {
				c.Invalidate()
			}
		}
	}
	w, h := c.Size()
	if w <= 0 {
		w = 1280
	}
	if h <= 0 {
		h = 860
	}

	if a.CurrentRoute == RouteTasks {
		if !a.conflictsLoadedOnTasks || a.conflictsLoadedBackend != a.backend {
			a.conflictsLoadedOnTasks = true
			a.refreshCacheConflicts()
		}
	} else {
		a.conflictsLoadedOnTasks = false
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
	if a.Remote.ConfirmPushXOpen {
		a.renderRemotePushXConfirmOverlay(c, w, h)
	}
	if a.companion != nil {
		if snap := a.companion.Status(); a.Mobile.PairDialogOpen || len(snap.PendingPairs) > 0 {
			if len(snap.PendingPairs) > 0 {
				a.Mobile.PairDialogOpen = true
			}
			a.renderCompanionPairingDialog(c, snap)
		}
	}
	if a.DeleteConfirm.Open {
		a.renderDeleteConfirmOverlay(c, w, h)
	}
	if a.ForceRecacheConfirm.Open {
		a.renderForceRecacheConfirmOverlay(c, w, h)
	}
	if a.AddSubDialogOpen {
		a.renderAddSubOverlay(c, w, h)
	}
	if a.SubLinks.Open {
		a.renderSubLinksOverlay(c, w, h)
	}
	if a.ImportSubModalOpen {
		a.renderImportSubOverlay(c, w, h)
	}
	if a.RuleBatch.Open {
		a.renderRuleBatchOverlay(c, w, h)
	}
	if a.MultiTagBatch.Open {
		a.renderMultiTagBatchOverlay(c, w, h)
	}
	if a.SubscribePicker.Open {
		a.renderSubscribePickerOverlay(c, w, h)
	}
	if a.LibraryRefresh.Open {
		a.renderLibraryRefreshOverlay(c, w, h)
	}
	if a.Transfer.ExportOpen {
		a.renderExportOverlay(c, w, h)
	}
	if a.SubFile.ExportOpen {
		a.renderSubExportOverlay(c, w, h)
	}
	if a.SubFile.ImportOpen {
		a.renderSubImportOverlay(c, w, h)
	}
	if a.ConflictDiff.Open {
		a.renderConflictDiffOverlay(c, w, h)
	}
	if a.XUserLists.Open {
		a.renderXUserListsOverlay(c, w, h)
	}
	if a.ImageViewer.Open {
		a.renderImageViewerOverlay(c, w, h)
	}
	if a.PlayerOverlay.Open {
		a.renderPlayerOverlay(c, w, h)
	}
	// Render restart confirmation above all modals and media layers so clicking
	// "重启以更新" on the floating update bubble is never hidden behind them.
	if a.RestartConfirmOpen && a.updater != nil {
		a.renderRestartConfirmOverlay(c, w, h)
	}

	// Unified bottom-right floating notification bubbles (DEC-086).
	a.renderFloatingNotifications(c, w, h)
}

// hasOpenModalOverlay reports whether any modal dialog or fullscreen media
// overlay is currently open on top of the active route.
func (a *App) hasOpenModalOverlay() bool {
	return a.ConfirmLogoutOpen ||
		a.Remote.ConfirmPushXOpen ||
		a.Mobile.PairDialogOpen ||
		a.DeleteConfirm.Open ||
		a.ForceRecacheConfirm.Open ||
		(a.RestartConfirmOpen && a.updater != nil) ||
		a.AddSubDialogOpen ||
		a.SubLinks.Open ||
		a.ImportSubModalOpen ||
		a.RuleBatch.Open ||
		a.MultiTagBatch.Open ||
		a.SubscribePicker.Open ||
		a.LibraryRefresh.Open ||
		a.Transfer.ExportOpen ||
		a.SubFile.ExportOpen ||
		a.SubFile.ImportOpen ||
		a.ConflictDiff.Open ||
		a.XUserLists.Open ||
		a.ImageViewer.Open ||
		a.PlayerOverlay.Open
}
