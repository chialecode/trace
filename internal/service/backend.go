package service

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/storage"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/chialecode/trace/internal/uistate"
)

// Options configures the TRACE Backend service.
type Options struct {
	Locations        appdirs.Locations
	BilibiliOpts     bilibili.ClientOptions
	DouyinOpts       douyin.ClientOptions
	Desktop          Desktop
	DisableBgWorkers bool
}

// OpenLinkResult contains the resolved target and fetched detail payload for UI navigation.
type OpenLinkResult struct {
	Site                 string                     `json:"site,omitempty"`
	SiteTarget           *site.ResolveTarget        `json:"siteTarget,omitempty"`
	Target               bilibili.ResolveTarget     `json:"target"`
	Video                *bilibili.VideoDetail      `json:"video,omitempty"`
	User                 *bilibili.UserSpace        `json:"user,omitempty"`
	UserVideos           *bilibili.UserVideosPage   `json:"userVideos,omitempty"`
	UserDynamicsError    string                     `json:"userDynamicsError,omitempty"`
	UserVideosError      string                     `json:"userVideosError,omitempty"`
	UserDynamics         *bilibili.UserDynamicsPage `json:"userDynamics,omitempty"`
	Dynamic              *bilibili.DynamicItem      `json:"dynamic,omitempty"`
	LiveRoom             *live.RoomState            `json:"liveRoom,omitempty"`
	DouyinWork           *DouyinWorkView            `json:"douyinWork,omitempty"`
	DouyinUser           *DouyinUserView            `json:"douyinUser,omitempty"`
	DouyinUserWorks      *DouyinUserWorksPage       `json:"douyinUserWorks,omitempty"`
	DouyinUserWorksError string                     `json:"douyinUserWorksError,omitempty"`
	DouyinLive           *DouyinLiveView            `json:"douyinLive,omitempty"`
	CachedItem           *library.ItemMetadata      `json:"cachedItem,omitempty"`
	// HistoryThumbs names the images of the recent-parse entry. OpenLink
	// records the entry without waiting for them; FetchHistoryThumbs adds
	// them afterwards, so opening a link waits only for its metadata.
	HistoryThumbs *HistoryThumbJob `json:"-"`
}

// HistoryThumbJob is the deferred thumbnail work of one recent-parse entry.
type HistoryThumbJob struct {
	Site      string
	Kind      string
	ID        string
	AvatarURL string
	CoverURL  string
}

// Diagnostics provides a sanitized runtime status snapshot for the Settings/Diagnostics view and smoke tests.
type Diagnostics struct {
	Version                 string                       `json:"version"`
	Commit                  string                       `json:"commit,omitempty"`
	BuildTime               string                       `json:"buildTime,omitempty"`
	AppName                 string                       `json:"appName"`
	DisplayName             string                       `json:"displayName"`
	Channel                 string                       `json:"channel"`
	Mode                    string                       `json:"mode"`
	DataDir                 string                       `json:"dataDir"`
	CacheDir                string                       `json:"cacheDir"`
	LogsDir                 string                       `json:"logsDir"`
	Account                 credential.Status            `json:"account"`
	Accounts                map[string]credential.Status `json:"accounts,omitempty"`
	LibraryCount            int                          `json:"libraryCount"`
	TaskCount               int                          `json:"taskCount"`
	SubscriptionCnt         int                          `json:"subscriptionCount"`
	LiveRoomCount           int                          `json:"liveRoomCount"`
	IndexStatus             string                       `json:"indexStatus,omitempty"`
	SubscriptionCheckStatus string                       `json:"subscriptionCheckStatus,omitempty"`
	AutoCacheStatus         string                       `json:"autoCacheStatus,omitempty"`
	LiveRecordStatus        string                       `json:"liveRecordStatus,omitempty"`
	CorruptRecovered        []string                     `json:"corruptRecovered,omitempty"`
	RecentEvents            []Event                      `json:"recentEvents,omitempty"`
}

// AuthService defines authentication and renewal operations across supported sites.
type AuthService interface {
	HasAnyLogin() bool
	Statuses() map[string]credential.Status
	StatusOf(siteID string) credential.Status
	Renewal(siteID string) RenewalStatus
	VerifySiteAccount(ctx context.Context, siteID string) (credential.Status, error)
	LoginWithCookie(ctx context.Context, siteID, rawCookie, rawRefreshToken string) (credential.Status, error)
	LogoutSite(siteID string) (credential.Status, error)
	AccountStatus() credential.Status
	BootstrapAuth(ctx context.Context) (credential.Status, error)
	VerifyAccount(ctx context.Context) (credential.Status, error)
	StartQRLogin(ctx context.Context) (bilibili.QRSession, error)
	PollQRLogin(ctx context.Context, flowID string) (bilibili.QRPollResult, error)
	CancelQRLogin(flowID string)
	ImportCookie(ctx context.Context, rawCookie, rawRefreshToken string) (credential.Status, error)
	RefreshCookie(ctx context.Context) (credential.Status, error)
	Logout() (credential.Status, error)
}

// ParseService defines link resolution and creator/video/note browsing operations.
type ParseService interface {
	Resolve(ctx context.Context, rawInput string) (site.ResolveTarget, error)
	OpenLink(ctx context.Context, rawInput string) (*OpenLinkResult, error)
	DouyinWork(ctx context.Context, awemeID string) (*DouyinWorkView, error)
	DouyinUser(ctx context.Context, secUID string) (*DouyinUserView, error)
	DouyinUserWorks(ctx context.Context, secUID, cursor string) (*DouyinUserWorksPage, error)
	DouyinLive(ctx context.Context, webRID string) (*DouyinLiveView, error)
	StagePreviewMedia(ctx context.Context, siteID, rawURL, ext string) (string, error)
	FetchHistoryThumbs(ctx context.Context, job HistoryThumbJob) error
	FetchImage(ctx context.Context, rawURL string) ([]byte, error)
	UserDynamicsPage(ctx context.Context, mid int64, offset string) (bilibili.UserDynamicsPage, error)
	UserVideosPage(ctx context.Context, mid int64, page int) (bilibili.UserVideosPage, error)
}

// HistoryService defines recent parse history operations.
type HistoryService interface {
	ListHistory() []storage.HistoryEntry
	ClearHistory() error
	RemoveHistory(kind, id string) error
	RemoveHistoryForSite(siteID, kind, id string) error
	HistoryThumbPath(name string) (string, error)
}

// CacheService defines download queue, supplement, and quality upgrade operations.
type CacheService interface {
	EnqueueVideoCache(ctx context.Context, req download.VideoEnqueueRequest) ([]download.Task, error)
	EnqueueDynamicCache(ctx context.Context, req download.DynamicEnqueueRequest) (download.Task, error)
	CacheDouyinWork(ctx context.Context, awemeID string) (download.Task, error)
	CacheOlderUserItems(ctx context.Context, req CacheOlderRequest) (CacheOlderResult, error)
	CacheSupplement(ctx context.Context, bvid string) (download.Task, error)
	Upgrade(ctx context.Context, key string) (download.Task, error)
	ListDownloadTasks() ([]download.Task, error)
	TaskDetail(taskID string) (download.TaskDetail, bool, error)
	PauseTask(taskID string) (download.Task, error)
	ResumeTask(taskID string) (download.Task, error)
	RetryTask(taskID string) (download.Task, error)
	CancelTask(taskID string) (download.Task, error)
	RemoveTask(taskID string) error
	RemoveFinishedTasks() (int, error)
	States(keys []string) map[string]library.DotState
	UpgradeCandidates(limit int) ([]library.ItemMetadata, error)
	CheckUpgradeKeys(ctx context.Context, keys []string, force bool) ([]library.ItemMetadata, error)
	CheckQualityUpgrade(ctx context.Context) ([]library.ItemMetadata, error)
}

// FeedService defines home feed pagination and anchor persistence operations.
type FeedService interface {
	QueryFeed(filter library.FeedFilter) ([]library.FeedEntry, error)
	FeedPage(req library.FeedPageRequest) (library.FeedPageResult, error)
	Anchor() uistate.FeedAnchor
	SaveAnchor(anchor uistate.FeedAnchor) error
	NewerCount(anchor uistate.FeedAnchor) (int, error)
}

// LibraryService defines local media library, safe deletion, and whitelist path operations.
type LibraryService interface {
	LibraryStatistics() (library.Statistics, error)
	ListLibraryItems(filter library.FeedFilter) ([]library.ItemMetadata, error)
	GetLibraryItem(keyOrID string) (library.ItemMetadata, bool, error)
	MediaPath(key, relPath string) (string, error)
	PlanDelete(key string) (library.DeletePlan, error)
	Delete(key string) error
	DeleteLibraryItem(itemKey string, deleteFiles bool) error
	RebuildLibraryIndex() (library.RebuildReport, error)
	AddLibraryRoot(selectedDir string) (library.AddRootReport, error)
	MigrateCacheDir(newDir string, moveExisting bool) (library.RebuildReport, error)
	OpenItemFolder(itemKey string) error
}

// SettingsService defines settings, UI state, and directory management operations.
type SettingsService interface {
	Locations() appdirs.Locations
	GetSettings() settings.Settings
	UpdateSettings(next settings.Settings) (settings.Settings, error)
	GetUIState() uistate.State
	UpdateUIState(next uistate.State) (uistate.State, error)
	OpenDataFolder(target string) error
}

// SubscriptionService defines multi-account subscription CRUD and binding operations.
type SubscriptionService interface {
	ListSubscriptions() ([]subscription.Subscription, error)
	UpsertSubscription(sub subscription.Subscription) (subscription.Subscription, error)
	CreateSubscription(name, note string, accounts []subscription.AccountRef, rules subscription.SubRules) (subscription.Subscription, error)
	LinkSubscriptionAccount(subID, site, uid, name, face string) (subscription.Subscription, error)
	LinkSubscriptionAccountRef(subID string, acct subscription.AccountRef) (subscription.Subscription, error)
	UnlinkSubscriptionAccount(subID, site, uid string) (subscription.Subscription, error)
	MoveSubscriptionAccount(site, uid, toSubID string) (subscription.Subscription, error)
	SetSubscriptionRules(subID string, rules subscription.SubRules) (subscription.Subscription, error)
	// Shared cache rules: subscriptions choose one by ID.
	ListCacheRules() []subscription.CacheRule
	SaveCacheRule(rule subscription.CacheRule) (subscription.CacheRule, error)
	DeleteCacheRule(id string) error
	SetSubscriptionRule(subID, ruleID string) (subscription.Subscription, error)
	PreviewAccount(ctx context.Context, input string) (subscription.AccountRef, *subscription.Subscription, error)
	OwnerOfAccount(site, uid string) (subscription.Subscription, bool)
	DeleteSubscription(subIDOrMid string) error
	ExportOPML(path string) error
	ImportOPML(path string) (int, error)
}

// UpdateService defines subscription update checking, listing, and scheduler diagnostics.
type UpdateService interface {
	ListSubscriptionUpdatesPage(subID string, limit, offset int) ([]subscription.UpdateItem, error)
	ListSubscriptionUpdates(subID string) ([]subscription.UpdateItem, error)
	CheckSubscriptionsNow(ctx context.Context, targetMid int64) (subscription.CheckSummary, error)
	CheckSubscription(ctx context.Context, subID string) (subscription.CheckSummary, error)
	CancelSubscriptionCheck()
	SchedulerStatuses() map[string]SiteSchedulerStatus
}

// FollowingService defines read-only following group and creator import operations.
type FollowingService interface {
	RelationTags(ctx context.Context) ([]bilibili.RelationTag, error)
	RelationFollowings(ctx context.Context, tagID int64, pn, ps int) ([]bilibili.FollowingUser, error)
	ImportCreators(users []bilibili.FollowingUser, scope, autoCache string, liveNotify, liveAutoRecord bool) (int, error)
}

// LiveService defines live status polling and stream recording operations across sites.
type LiveService interface {
	ListLiveRooms() []live.RoomState
	ListLiveAccounts() []LiveAccountState
	LiveStatus(ctx context.Context, uids []int64) ([]live.RoomState, error)
	LiveStatuses(ctx context.Context, accountKeys []string) ([]LiveAccountState, error)
	PollLiveNow(ctx context.Context) ([]live.RoomState, error)
	OpenLiveRoomInBrowser(roomID int64) error
	OpenLiveInBrowser(siteID, roomOrWebRID string) error
	StartLiveRecording(ctx context.Context, roomID int64) (live.RoomState, error)
	StartLiveRecordingByAccount(ctx context.Context, accountKey string) (LiveAccountState, error)
	StartLiveRecordingByRoom(ctx context.Context, siteID, roomOrWebRID string) (LiveAccountState, error)
	StopLiveRecording(roomID int64) (live.RoomState, error)
	StopLiveRecordingByKey(recKey string) (LiveAccountState, error)
}

// AppService defines application lifecycle, event subscription, and diagnostics operations.
type AppService interface {
	Close() error
	SubscribeEvents(fn func(Event)) func()
	Desktop() Desktop
	DiagnosticsSnapshot() Diagnostics
	RecentLogs(maxLines int) ([]string, error)
	PauseAllActiveWork()
}

// BackendAPI composes all TRACE domain service interfaces for the UI layer.
type BackendAPI interface {
	AuthService
	ParseService
	HistoryService
	CacheService
	FeedService
	LibraryService
	SettingsService
	SubscriptionService
	UpdateService
	FollowingService
	LiveService
	AppService
}

var _ BackendAPI = (*Backend)(nil)

// Backend coordinates all TRACE domain packages without importing any UI framework.
type Backend struct {
	loc           appdirs.Locations
	db            *sql.DB
	updatesDB     *sql.DB
	settings      *settings.Store
	uiState       *uistate.Store
	history       *storage.HistoryStore
	credMgr       *credential.Manager
	cred          *credential.Store
	douyinCred    *credential.Store
	bili          *bilibili.Client
	douyin        *douyin.Client
	biliAdapter   *bilibili.Adapter
	douyinAdapter *douyin.Adapter
	registry      *site.Registry
	lib           *library.Store
	dl            *download.Manager
	subs          *subscription.Manager
	live          *live.Manager
	desktop       Desktop
	events        *eventBus
	bgCtx         context.Context
	bgCancel      context.CancelFunc
	renewalWake   chan struct{}
	bgWg          sync.WaitGroup
	logMu         sync.Mutex
	settingsMu    sync.Mutex
	logPath       string
	previewDir    string
	corruptFiles  []string
}

// NewBackend initializes all storage, credential, site, library, download, subscription, and live components.
func NewBackend(opts Options) (*Backend, error) {
	loc := opts.Locations
	if loc.DataDir == "" {
		var err error
		loc, err = appdirs.Resolve()
		if err != nil {
			return nil, err
		}
	}
	if err := appdirs.EnsureDirectories(loc); err != nil {
		return nil, err
	}

	var corruptBackups []string

	// Data written by a newer TRACE is refused before anything is opened
	// or rewritten; data of an older structure is backed up before it is
	// migrated.
	if _, err := storage.GuardDataSchemas(loc.DataDir, storage.DataSchemas); err != nil {
		return nil, err
	}

	settingsPath := filepath.Join(loc.DataDir, "settings.json")
	setStore, setRec, setBackup, err := settings.Open(settingsPath, loc.DefaultCacheDir)
	if err != nil {
		return nil, err
	}
	if setRec && setBackup != "" {
		corruptBackups = append(corruptBackups, setBackup)
	}
	curSettings := setStore.Get()
	// Every site request follows Settings → 网络 (DEC-046 ⑤); an unusable
	// stored address falls back to the system's proxy.
	if err := site.SetProxy(curSettings.Network.ProxyMode, curSettings.Network.ProxyURL); err != nil {
		_ = site.SetProxy(site.ProxySystem, "")
	}
	if err := os.MkdirAll(curSettings.CacheDir, 0o700); err != nil {
		return nil, fmt.Errorf("service: mkdir cacheDir: %w", err)
	}

	uiStatePath := filepath.Join(loc.DataDir, "ui-state.json")
	uiStore, uiRec, uiBackup, err := uistate.Open(uiStatePath)
	if err != nil {
		return nil, err
	}
	if uiRec && uiBackup != "" {
		corruptBackups = append(corruptBackups, uiBackup)
	}

	historyPath := filepath.Join(loc.DataDir, "history.json")
	histStore, histBackup, err := storage.OpenHistory(historyPath)
	if err != nil {
		return nil, err
	}
	if histBackup != "" {
		corruptBackups = append(corruptBackups, histBackup)
	}

	// Preserve migration from legacy bilibili.dpapi to bilibili.bin before opening the multi-site manager.
	credPath := filepath.Join(loc.CredentialsDir, "bilibili.bin")
	legacyCredPath := filepath.Join(loc.CredentialsDir, "bilibili.dpapi")
	if _, err := os.Stat(credPath); os.IsNotExist(err) {
		if rawLegacy, lErr := os.ReadFile(legacyCredPath); lErr == nil {
			_ = storage.WriteFileAtomic(credPath, rawLegacy, 0o600)
		}
	}
	credMgr, credBackups, err := credential.OpenManager(loc.CredentialsDir)
	if err != nil {
		return nil, err
	}
	if len(credBackups) > 0 {
		corruptBackups = append(corruptBackups, credBackups...)
	}
	credStore := credMgr.Store(credential.SiteBilibili)
	douyinCredStore := credMgr.Store(credential.SiteDouyin)

	dbPath := filepath.Join(loc.DataDir, "library.db")
	_, dbStatErr := os.Stat(dbPath)
	dbWasMissing := os.IsNotExist(dbStatErr)
	db, dbRec, dbBackup, err := storage.OpenSQLite(dbPath)
	if err != nil {
		return nil, err
	}
	if dbRec && dbBackup != "" {
		corruptBackups = append(corruptBackups, dbBackup)
	}
	if _, err := storage.CheckSQLiteSchema(db, "library.db", schemaVersion("library.db")); err != nil {
		_ = db.Close()
		return nil, err
	}

	updatesDBPath := filepath.Join(loc.DataDir, "updates.db")
	updatesDB, updatesRec, updatesBackup, err := storage.OpenSQLite(updatesDBPath)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if updatesRec && updatesBackup != "" {
		corruptBackups = append(corruptBackups, updatesBackup)
	}
	if _, err := storage.CheckSQLiteSchema(updatesDB, "updates.db", schemaVersion("updates.db")); err != nil {
		_ = updatesDB.Close()
		_ = db.Close()
		return nil, err
	}

	libStore, err := library.OpenWithDataDir(db, curSettings.CacheDir, loc.DataDir)
	if err != nil {
		_ = updatesDB.Close()
		_ = db.Close()
		return nil, err
	}

	// If SQLite was recreated or did not exist on disk before startup, scan CacheDir to rebuild index from item.json files.
	if dbRec || dbWasMissing {
		if rep, rErr := libStore.RebuildIndex(); rErr == nil && len(rep.CorruptBackedUp) > 0 {
			corruptBackups = append(corruptBackups, rep.CorruptBackedUp...)
		}
	}

	desk := opts.Desktop
	if desk == nil {
		desk = NewDefaultDesktop()
	}

	previewDir := filepath.Join(loc.DataDir, "cache", "preview")
	_ = os.MkdirAll(previewDir, 0o700)

	evBus := newEventBus()
	bgCtx, bgCancel := context.WithCancel(context.Background())

	b := &Backend{
		loc:          loc,
		db:           db,
		updatesDB:    updatesDB,
		settings:     setStore,
		uiState:      uiStore,
		history:      histStore,
		credMgr:      credMgr,
		cred:         credStore,
		douyinCred:   douyinCredStore,
		lib:          libStore,
		desktop:      desk,
		events:       evBus,
		bgCtx:        bgCtx,
		bgCancel:     bgCancel,
		renewalWake:  make(chan struct{}, 1),
		logPath:      filepath.Join(loc.LogsDir, "trace.log"),
		previewDir:   previewDir,
		corruptFiles: corruptBackups,
	}

	biliOpts := opts.BilibiliOpts
	biliOpts.CredStore = credStore
	biliClient := bilibili.NewClient(biliOpts)
	biliAdapter := bilibili.NewAdapter(biliClient)

	dyOpts := opts.DouyinOpts
	dyOpts.CredStore = douyinCredStore
	if dyOpts.PreferredCodec == nil {
		dyOpts.PreferredCodec = b.preferredDouyinCodec
	}
	dyClient := douyin.NewClient(dyOpts)
	dyAdapter := douyin.NewAdapter(dyClient)

	b.bili = biliClient
	b.douyin = dyClient
	b.biliAdapter = biliAdapter
	b.douyinAdapter = dyAdapter
	b.registry = site.NewRegistry(biliAdapter, dyAdapter)

	dlMgr, err := download.NewManager(db, libStore, biliClient, curSettings.Download, func(t download.Task) {
		b.logInfo(fmt.Sprintf("download task %s (%s) -> %s", t.TaskID, t.TargetID, t.Status))
		message := t.TaskID + ":" + t.Status
		if t.ErrorKind == "storage" {
			message += ":storage"
		}
		evBus.PublishSite(EventTaskUpdated, t.Site, message)
		if t.Status == "completed" || t.Status == "partial" {
			evBus.PublishSite(EventLibraryChanged, t.Site, t.ItemKey)
		}
	})
	if err != nil {
		_ = updatesDB.Close()
		_ = db.Close()
		return nil, err
	}
	b.dl = dlMgr
	dlMgr.RegisterWorkPlanner(&douyinWorkPlanner{backend: b})

	subsPath := filepath.Join(loc.DataDir, "subscriptions.json")
	subMgr, err := subscription.NewManagerWithUpdatesDB(db, updatesDB, subsPath, libStore, dlMgr, biliClient, curSettings.Subscription, func(sum subscription.CheckSummary) {
		if sum.FinishedAt == "" {
			evBus.Publish(EventSubscriptionChecked, fmt.Sprintf("progress:%d/%d", sum.CheckedCreators, sum.TotalCreators))
			return
		}
		b.logInfo(fmt.Sprintf("subscription check finished: creators=%d newVideos=%d newDynamics=%d", sum.CheckedCreators, sum.NewVideos, sum.NewDynamics))
		evBus.Publish(EventSubscriptionChecked, fmt.Sprintf("checked=%d", sum.CheckedCreators))
	})
	if err != nil {
		_ = updatesDB.Close()
		_ = db.Close()
		return nil, err
	}
	b.subs = subMgr
	// Every table of both databases now has the layout of this build.
	if err := storage.StampSQLiteSchema(db, schemaVersion("library.db")); err != nil {
		_ = updatesDB.Close()
		_ = db.Close()
		return nil, err
	}
	if err := storage.StampSQLiteSchema(updatesDB, schemaVersion("updates.db")); err != nil {
		_ = updatesDB.Close()
		_ = db.Close()
		return nil, err
	}

	liveMgr := live.NewManager(libStore, subMgr, biliClient, curSettings.Live, func(n live.Notification) {
		nSite := firstNonEmpty(n.Site, string(site.Bilibili))
		roomLabel := firstNonEmpty(n.RoomIDStr, strconv.FormatInt(n.RoomID, 10))
		b.logInfo(fmt.Sprintf("live started: site=%s room=%s uname=%s", nSite, roomLabel, n.Uname))
		evBus.PublishSite(EventLiveUpdated, nSite, "live:"+roomLabel)
	}, func(st live.RoomState) {
		stSite := firstNonEmpty(st.Site, string(site.Bilibili))
		roomLabel := firstNonEmpty(st.RoomKey, strconv.FormatInt(st.RoomID, 10))
		evBus.PublishSite(EventLiveUpdated, stSite, fmt.Sprintf("room:%s:%d", roomLabel, st.LiveStatus))
	})
	liveMgr.RegisterSiteProvider(dyAdapter)
	liveMgr.SetSiteBackoffChecker(func(siteID site.ID) (bool, time.Time) {
		return subMgr.IsSiteInBackoff(string(siteID))
	})
	liveMgr.SetWebRIDSyncFunc(func(siteID site.ID, uid, webRID string, _ bool) {
		if siteID == site.Douyin && uid != "" {
			_ = subMgr.UpdateAccountWebRID(string(site.Douyin), uid, webRID)
		}
	})
	b.live = liveMgr

	biliClient.SetAccountReplacingHandler(func() {
		b.pauseSiteActiveWork(site.Bilibili, "auth_required", "哔哩哔哩登录账号已变更，任务已暂停")
	})
	biliClient.SetAutomaticRequestHandlers(subMgr.AutomaticRequestAllowed, subMgr.RecordAutomaticStop)
	dyClient.SetAutomaticRequestHandlers(
		func(_ context.Context, _ string, _ string) error {
			return subMgr.SiteAutomaticRequestAllowed(string(site.Douyin))
		},
		func(_ string, err error) {
			subMgr.RecordSiteAutomaticStop(string(site.Douyin), site.KindOf(err))
		},
	)
	subMgr.RegisterFetcher(dyAdapter)
	subMgr.SetSiteAuthCheck(func(siteID site.ID) bool {
		return b.credMgr.HasActiveCredential(string(siteID))
	})
	subMgr.RegisterAutoCacheEnqueuer(site.Douyin, func(ctx context.Context, item subscription.UpdateItem) (string, error) {
		t, err := b.enqueueDouyinWorkWithSource(ctx, item.DynID, "auto")
		return t.TaskID, err
	})
	subMgr.SetOnLiveHint(func(acc subscription.AccountRef, roomID string, webRID string) {
		if strings.EqualFold(acc.Site, string(site.Douyin)) {
			liveMgr.ObserveAccountLiveHint(site.Douyin, acc.UID, webRID, roomID)
			if webRID != "" {
				_ = subMgr.UpdateAccountWebRID(acc.Site, acc.UID, webRID)
			}
		}
	})
	subMgr.SetLiveCheck(func(ctx context.Context, uids []int64) error {
		_, err := liveMgr.RefreshForAccounts(ctx, uids)
		return err
	})
	subMgr.SetSiteLiveCheck(func(ctx context.Context, accounts []subscription.AccountRef) error {
		keys := make([]string, 0, len(accounts))
		for _, acc := range accounts {
			if k := formatAccountKey(site.ID(acc.Site), acc.UID); k != "" {
				keys = append(keys, k)
			}
		}
		_, err := b.LiveStatuses(ctx, keys)
		return err
	})
	biliClient.SetAuthExpiredHandler(func() {
		b.pauseSiteActiveWork(site.Bilibili, "auth_required", "哔哩哔哩登录已失效，任务已暂停")
		evBus.PublishSite(EventAccountChanged, string(site.Bilibili), "expired")
	})
	dyClient.SetAuthExpiredHandler(func(reason string) {
		msg := firstNonEmpty(reason, "抖音登录已失效，任务已暂停")
		b.pauseSiteActiveWork(site.Douyin, "auth_required", msg)
		evBus.PublishSite(EventAccountChanged, string(site.Douyin), "expired")
	})

	build := appdirs.BuildInfo()
	b.logInfo(fmt.Sprintf("backend initialized: app=%s version=%s commit=%s built=%s mode=%s channel=%s", loc.AppName, build.Version, firstNonEmpty(build.Commit, "-"), firstNonEmpty(build.BuildTime, "-"), loc.Mode, loc.Channel))

	if !opts.DisableBgWorkers {
		if err := dlMgr.Start(bgCtx); err != nil {
			bgCancel()
			_ = updatesDB.Close()
			_ = db.Close()
			return nil, err
		}
		subMgr.Start(bgCtx)
		liveMgr.Start(bgCtx)
		b.bgWg.Add(1)
		go b.runRenewalLoop(bgCtx)
		b.bgWg.Add(1)
		go func() { defer b.bgWg.Done(); _, _ = b.BootstrapAuth(bgCtx) }()
	}

	return b, nil
}

func schemaVersion(name string) int {
	if sf, ok := storage.SchemaFor(name); ok {
		return sf.Current
	}
	return 1
}

// pauseSiteActiveWork pauses active download tasks and stops active live recordings
// belonging ONLY to targetSite, leaving other sites' active tasks and recordings untouched.
func (b *Backend) pauseSiteActiveWork(targetSite site.ID, reasonCode, reasonMsg string) {
	if b.dl != nil {
		b.dl.PauseForSite(targetSite, reasonCode, reasonMsg)
	}
	if b.live != nil {
		b.live.StopForSite(targetSite, reasonCode)
	}
}

func (b *Backend) syncDouyinAccountWebRID(secUID, webRID string) {
	secUID = strings.TrimSpace(secUID)
	webRID = strings.TrimSpace(webRID)
	if secUID == "" || webRID == "" || b.subs == nil {
		return
	}
	_ = b.subs.UpdateAccountWebRID(string(site.Douyin), secUID, webRID)
}

func (b *Backend) lookupAccountWebRID(secUID string) string {
	secUID = strings.TrimSpace(secUID)
	if secUID == "" {
		return ""
	}
	if b.douyin != nil {
		if w := b.douyin.CachedWebRID(secUID); w != "" {
			return w
		}
	}
	if b.subs != nil {
		if _, acc, ok := b.subs.GetAccount(string(site.Douyin), secUID); ok && acc.WebRID != "" {
			return acc.WebRID
		}
	}
	return ""
}

func (b *Backend) startDouyinLiveRecording(ctx context.Context, uid string, webRID string) (LiveAccountState, error) {
	rs, err := b.live.StartDouyinRecording(ctx, uid, webRID)
	return mapRoomToAccountState(rs), err
}

func (b *Backend) runRenewalLoop(ctx context.Context) {
	defer b.bgWg.Done()
	// Wait 1 minute after startup before the first automatic renewal check.
	initialTimer := time.NewTimer(time.Minute)
	select {
	case <-ctx.Done():
		initialTimer.Stop()
		return
	case <-b.renewalWake:
		initialTimer.Stop()
		// Wait 1 minute after QR login confirmation
		t2 := time.NewTimer(time.Minute)
		select {
		case <-ctx.Done():
			t2.Stop()
			return
		case <-t2.C:
		}
	case <-initialTimer.C:
	}

	for {
		if ctx.Err() != nil {
			return
		}
		b.tryScheduledRenewal(ctx)

		timer := time.NewTimer(12 * time.Hour)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-b.renewalWake:
			timer.Stop()
			t2 := time.NewTimer(time.Minute)
			select {
			case <-ctx.Done():
				t2.Stop()
				return
			case <-t2.C:
			}
		case <-timer.C:
		}
	}
}

func (b *Backend) tryScheduledRenewal(ctx context.Context) {
	st := b.cred.Status()
	if !st.LoggedIn {
		return
	}
	if st.Source != "qr" {
		return
	}
	if b.subs != nil {
		if inBackoff, _ := b.subs.IsInBackoff(); inBackoff {
			return
		}
		if !b.subs.CanUseQuotaTokens(8) {
			return
		}
	}
	_, _ = b.RefreshCookie(bilibili.WithAutomaticRequests(ctx, "renewal"))
}

// Close stops background workers, removes temporary media previews, and closes SQLite databases.
func (b *Backend) Close() error {
	if b.bgCancel != nil {
		b.bgCancel()
	}
	b.bgWg.Wait()
	if b.live != nil {
		b.live.Stop()
	}
	if b.subs != nil {
		b.subs.Stop()
	}
	if b.dl != nil {
		b.dl.Stop()
	}
	if b.previewDir != "" {
		_ = os.RemoveAll(b.previewDir)
	}
	if b.updatesDB != nil {
		_ = b.updatesDB.Close()
	}
	if b.db != nil {
		return b.db.Close()
	}
	return nil
}

// SubscribeEvents registers an event listener and returns an unsubscribe function.
func (b *Backend) SubscribeEvents(fn func(Event)) func() {
	return b.events.Subscribe(fn)
}

// Locations returns the resolved runtime directories.
func (b *Backend) Locations() appdirs.Locations {
	return b.loc
}

// Desktop returns the configured OS desktop shell adapter.
func (b *Backend) Desktop() Desktop {
	return b.desktop
}

// BilibiliClient returns the underlying Bilibili site client.
func (b *Backend) BilibiliClient() *bilibili.Client {
	return b.bili
}

// DouyinClient returns the underlying Douyin site client.
func (b *Backend) DouyinClient() *douyin.Client {
	return b.douyin
}

// CredentialManager returns the multi-site credential manager.
func (b *Backend) CredentialManager() *credential.Manager {
	return b.credMgr
}

// SiteRegistry returns the multi-site link resolver registry.
func (b *Backend) SiteRegistry() *site.Registry {
	return b.registry
}

// LibraryStore returns the underlying library store.
func (b *Backend) LibraryStore() *library.Store {
	return b.lib
}

// HistoryStore returns the underlying history store.
func (b *Backend) HistoryStore() *storage.HistoryStore {
	return b.history
}

// DownloadManager returns the underlying download manager.
func (b *Backend) DownloadManager() *download.Manager {
	return b.dl
}

// SubscriptionManager returns the underlying subscription manager.
func (b *Backend) SubscriptionManager() *subscription.Manager {
	return b.subs
}

// LiveManager returns the underlying live manager.
func (b *Backend) LiveManager() *live.Manager {
	return b.live
}

// GetSettings returns a copy of the current settings.
func (b *Backend) GetSettings() settings.Settings {
	return b.settings.Get()
}

// UpdateSettings validates and persists settings, propagating changes to managers.
func (b *Backend) UpdateSettings(next settings.Settings) (settings.Settings, error) {
	b.settingsMu.Lock()
	defer b.settingsMu.Unlock()
	prev := b.settings.Get()
	preview, err := b.settings.PreviewUpdate(next)
	if err != nil {
		return prev, err
	}
	if preview.Network.ProxyMode == site.ProxyCustom {
		if _, err := site.ParseProxyURL(preview.Network.ProxyURL); err != nil {
			return prev, &site.Error{Kind: site.ErrKindBadRequest, Message: err.Error()}
		}
	}
	var updated settings.Settings
	persist := func() error {
		var err error
		updated, err = b.settings.Update(preview)
		return err
	}
	if !strings.EqualFold(prev.CacheDir, preview.CacheDir) {
		err = b.lib.ChangeCacheDir(preview.CacheDir, persist)
	} else {
		err = persist()
	}
	if err != nil {
		return prev, err
	}
	_ = site.SetProxy(updated.Network.ProxyMode, updated.Network.ProxyURL) // checked above; applies at once
	b.dl.UpdateSettings(updated.Download)
	b.subs.UpdateSettings(updated.Subscription)
	b.live.UpdateSettings(updated.Live)
	b.events.Publish(EventSettingsChanged, "updated")
	return updated, nil
}

// GetUIState returns the current UI state.
func (b *Backend) GetUIState() uistate.State {
	return b.uiState.Get()
}

// UpdateUIState persists the UI state atomically.
func (b *Backend) UpdateUIState(next uistate.State) (uistate.State, error) {
	return b.uiState.Update(next)
}

// AccountStatus returns the desensitized Bilibili account status (never exposing raw cookies or tokens).
func (b *Backend) AccountStatus() credential.Status {
	return b.cred.Status()
}

// BootstrapAuth verifies existing credentials per-site on startup without cross-site interference:
// - Logged-out sites are skipped with zero network requests.
// - Expired credentials pause only that site's active tasks and recordings.
// - Transient network or risk-control errors mark status "unknown" while keeping credentials usable.
func (b *Backend) BootstrapAuth(ctx context.Context) (credential.Status, error) {
	var biliOut credential.Status
	var allErr error

	biliSt := b.cred.Status()
	biliOut = biliSt
	if biliSt.State != "logged_out" && biliSt.LoggedIn {
		verified, err := b.bili.VerifyCredential(ctx)
		if err != nil && bilibili.IsKind(err, bilibili.ErrKindAuthRequired) {
			b.pauseSiteActiveWork(site.Bilibili, "auth_required", "哔哩哔哩登录已失效，任务已暂停")
		} else if err != nil {
			var stateErr error
			verified, stateErr = b.cred.MarkState("unknown", "无法确认登录状态，请重试")
			err = errors.Join(err, stateErr)
			if stateErr != nil {
				verified = b.cred.Status()
			}
		} else if !verified.LoggedIn {
			b.pauseSiteActiveWork(site.Bilibili, "auth_required", "哔哩哔哩登录已失效，任务已暂停")
		}
		biliOut = verified
		allErr = errors.Join(allErr, err)
		b.events.PublishSite(EventAccountChanged, string(site.Bilibili), verified.State)
	}

	dySt := b.douyinCred.Status()
	if dySt.State != "logged_out" && dySt.LoggedIn {
		dyVerified, dyErr := b.douyin.VerifyCredential(ctx)
		if dyErr != nil && site.IsKind(dyErr, site.ErrKindAuthRequired) {
			b.pauseSiteActiveWork(site.Douyin, "auth_required", "抖音登录已失效，任务已暂停")
		} else if dyErr != nil {
			var stateErr error
			dyVerified, stateErr = b.douyinCred.MarkState("unknown", "无法确认抖音登录状态，请稍后重试")
			dyErr = errors.Join(dyErr, stateErr)
			if stateErr != nil {
				dyVerified = b.douyinCred.Status()
			}
		} else if !dyVerified.LoggedIn {
			b.pauseSiteActiveWork(site.Douyin, "auth_required", "抖音登录已失效，任务已暂停")
		}
		allErr = errors.Join(allErr, dyErr)
		b.events.PublishSite(EventAccountChanged, string(site.Douyin), dyVerified.State)
	}

	return biliOut, allErr
}

// VerifyAccount queries Bilibili /x/web-interface/nav to refresh account profile and login state.
func (b *Backend) VerifyAccount(ctx context.Context) (credential.Status, error) {
	st, err := b.bili.VerifyCredential(ctx)
	if err != nil && bilibili.IsKind(err, bilibili.ErrKindAuthRequired) {
		b.pauseSiteActiveWork(site.Bilibili, "auth_required", "哔哩哔哩登录已失效，任务已暂停")
	}
	b.events.PublishSite(EventAccountChanged, string(site.Bilibili), st.State)
	return st, err
}

// StartQRLogin starts a new QR code login session and invalidates any previous QR session.
func (b *Backend) StartQRLogin(ctx context.Context) (bilibili.QRSession, error) {
	sess, err := b.bili.StartQR(ctx)
	if err != nil {
		b.logError("StartQRLogin", err)
		return bilibili.QRSession{}, err
	}
	b.logInfo("QR login started: flow=" + sess.FlowID)
	return sess, nil
}

// PollQRLogin polls the status of the active QR login flow.
func (b *Backend) PollQRLogin(ctx context.Context, flowID string) (bilibili.QRPollResult, error) {
	res, err := b.bili.PollQR(ctx, flowID)
	if err != nil {
		b.logError("PollQRLogin", err)
		return bilibili.QRPollResult{}, err
	}
	if res.State == "confirmed" {
		b.bili.ClearCaches()
		select {
		case b.renewalWake <- struct{}{}:
		default:
		}
		b.logInfo(fmt.Sprintf("QR login confirmed: mid=%d", res.Account.Mid))
		b.events.PublishSite(EventAccountChanged, string(site.Bilibili), "logged_in")
	}
	return res, nil
}

// CancelQRLogin cancels the QR login flow so late responses never write credentials.
func (b *Backend) CancelQRLogin(flowID string) {
	b.bili.CancelQR(flowID)
	b.logInfo("QR login canceled: flow=" + flowID)
}

// ImportCookie validates a manual Bilibili Cookie string against /x/web-interface/nav first,
// and only persists the credential if verification succeeds.
func (b *Backend) ImportCookie(ctx context.Context, rawCookie, rawRefreshToken string) (credential.Status, error) {
	prev := b.cred.Status()
	st, err := b.bili.LoginWithCookie(ctx, rawCookie, rawRefreshToken)
	if err != nil {
		b.logError("ImportCookie", err)
		return b.cred.Status(), err
	}
	if prev.LoggedIn && prev.Mid != 0 && prev.Mid != st.Mid {
		b.pauseSiteActiveWork(site.Bilibili, "auth_required", "哔哩哔哩登录账号已变更，任务已暂停")
	}
	b.logInfo(fmt.Sprintf("manual cookie imported: state=%s mid=%d", st.State, st.Mid))
	b.events.PublishSite(EventAccountChanged, string(site.Bilibili), st.State)
	return st, nil
}

// RefreshCookie runs the 5-step Bilibili Cookie renewal flow (or resumes confirmation if pending).
func (b *Backend) RefreshCookie(ctx context.Context) (credential.Status, error) {
	st, err := b.bili.CheckAndRefreshCookie(ctx)
	if err != nil {
		b.logError("RefreshCookie", err)
	}
	b.events.PublishSite(EventAccountChanged, string(site.Bilibili), st.State)
	return st, err
}

// MutateUIState changes fields of ui-state.json under its lock.
func (b *Backend) MutateUIState(fn func(*uistate.State)) (uistate.State, error) {
	return b.uiState.Mutate(fn)
}

// ActiveWork counts the queued and running cache tasks and the live
// recordings that quitting would pause or end.
func (b *Backend) ActiveWork() (tasks, recordings int) {
	if b.dl != nil {
		if list, err := b.dl.ListTasks(); err == nil {
			for _, t := range list {
				if t.Status == "running" || t.Status == "queued" {
					tasks++
				}
			}
		}
	}
	if b.live != nil {
		for _, r := range b.live.ListRooms() {
			if r.Recording {
				recordings++
			}
		}
	}
	return tasks, recordings
}

// PauseForExit pauses queued and running cache tasks for restart and finalizes active live recordings.
func (b *Backend) PauseForExit() {
	if b.dl != nil {
		b.dl.PauseAll("app_closed", "已暂停（应用关闭）")
	}
	if b.live == nil {
		return
	}
	var wg sync.WaitGroup
	for _, r := range b.live.ListRooms() {
		if !r.Recording {
			continue
		}
		key := strings.TrimSpace(r.RoomKey)
		if key == "" && r.RoomID > 0 {
			key = strconv.FormatInt(r.RoomID, 10)
		}
		if key == "" {
			continue
		}
		wg.Add(1)
		go func(recKey string) {
			defer wg.Done()
			if _, err := b.live.StopRecordingByKey(recKey); err != nil {
				b.logError("PauseForExit.StopRecording", err)
			}
		}(key)
	}
	wg.Wait()
	b.logInfo("work paused for exit")
}

// PauseAllActiveWork pauses running and queued download tasks and stops active live recordings across all sites.
func (b *Backend) PauseAllActiveWork() {
	if b.dl != nil {
		b.dl.PauseAll("auth_required", "登录状态已变更，任务已暂停")
	}
	if b.live != nil {
		b.live.StopForSite(site.Bilibili, "auth_required")
		b.live.StopForSite(site.Douyin, "auth_required")
	}
}

// Logout pauses Bilibili active tasks and live recordings, cancels any active QR login flow, and clears Bilibili credentials.
func (b *Backend) Logout() (credential.Status, error) {
	b.pauseSiteActiveWork(site.Bilibili, "auth_required", "已退出哔哩哔哩登录，任务已暂停")
	err := b.bili.LogoutCredential()
	st := b.cred.Status()
	if err != nil {
		b.logError("Logout", err)
	} else {
		b.logInfo("bilibili account logged out and credentials cleared")
	}
	b.events.PublishSite(EventAccountChanged, string(site.Bilibili), st.State)
	return st, err
}

// OpenLink resolves user input across supported sites after enforcing per-site login gates,
// fetches detail metadata, records sanitized navigation history, and returns OpenLinkResult.
func (b *Backend) OpenLink(ctx context.Context, rawInput string) (*OpenLinkResult, error) {
	if err := b.requireAnyLogin(); err != nil {
		return nil, err
	}
	siteID, normalizedInput, err := b.registry.DetectSite(rawInput)
	if err != nil {
		return nil, err
	}
	if err := b.requireSiteLogin(siteID); err != nil {
		return nil, err
	}

	if siteID == site.Douyin {
		return b.openDouyinLink(ctx, normalizedInput)
	}

	target, err := b.bili.ResolveLink(ctx, normalizedInput)
	if err != nil {
		b.logError("OpenLink.Resolve", err)
		return nil, err
	}

	siteTarget, _ := b.biliAdapter.Resolve(ctx, target.CanonicalURL)
	if siteTarget.Kind == "" {
		siteTarget = site.ResolveTarget{
			Site:          site.Bilibili,
			Kind:          target.Kind,
			ID:            target.ID,
			NumericAID:    target.AID,
			Page:          target.Page,
			NumericRoomID: target.RoomID,
			NumericMid:    target.Mid,
			CanonicalURL:  target.CanonicalURL,
			Unsupported:   target.Unsupported,
			Reason:        target.Reason,
		}
	}
	out := &OpenLinkResult{
		Site:       string(site.Bilibili),
		SiteTarget: &siteTarget,
		Target:     target,
	}

	switch target.Kind {
	case "video":
		vd, err := b.bili.VideoDetail(ctx, target.ID)
		if err != nil {
			b.logError("OpenLink.VideoDetail", err)
			return nil, err
		}
		out.Video = &vd
		if item, ok, _ := b.lib.GetItem(vd.BVID); ok {
			out.CachedItem = &item
		}
		if err := b.lib.UpsertFeedEntry(library.FeedEntry{
			Site:         "bilibili",
			Kind:         "video",
			RawID:        vd.BVID,
			Mid:          vd.Author.Mid,
			AuthorName:   vd.Author.Name,
			AuthorFace:   vd.Author.Face,
			Title:        vd.Title,
			Summary:      vd.Desc,
			CoverURL:     vd.Cover,
			CanonicalURL: vd.CanonicalURL,
			PubTs:        vd.PubTs,
		}); err != nil {
			return nil, &bilibili.SiteError{Site: site.Bilibili, Kind: "storage", Message: "解析记录写入失败", Err: err}
		}
		out.HistoryThumbs = &HistoryThumbJob{Site: "bilibili", Kind: "video", ID: vd.BVID, AvatarURL: vd.Author.Face, CoverURL: vd.Cover}
		if err := b.history.Record(storage.HistoryEntry{
			Site:         "bilibili",
			Kind:         "video",
			ID:           vd.BVID,
			Title:        vd.Title,
			Subtitle:     vd.Author.Name,
			CanonicalURL: vd.CanonicalURL,
			DurationText: formatHistoryDuration(vd.Duration),
		}); err != nil {
			return nil, &bilibili.SiteError{Site: site.Bilibili, Kind: "storage", Message: "历史记录写入失败", Err: err}
		}

	case "user":
		space, err := b.bili.UserCard(ctx, target.Mid)
		if err != nil {
			b.logError("OpenLink.UserCard", err)
			return nil, err
		}
		out.User = &space
		if dyns, dErr := b.bili.UserDynamics(ctx, target.Mid, ""); dErr == nil {
			out.UserDynamics = &dyns
		} else {
			out.UserDynamicsError = dErr.Error()
		}
		userID := fmt.Sprintf("%d", space.Mid)
		out.HistoryThumbs = &HistoryThumbJob{Site: "bilibili", Kind: "user", ID: userID, AvatarURL: space.Face}
		if err := b.history.Record(storage.HistoryEntry{
			Site:         "bilibili",
			Kind:         "user",
			ID:           userID,
			Title:        space.Name,
			Subtitle:     space.Sign,
			CanonicalURL: space.CanonicalURL,
		}); err != nil {
			return nil, &bilibili.SiteError{Site: site.Bilibili, Kind: "storage", Message: "历史记录写入失败", Err: err}
		}

	case "dynamic":
		dyn, err := b.bili.DynamicDetail(ctx, target.ID)
		if err != nil {
			b.logError("OpenLink.DynamicDetail", err)
			return nil, err
		}
		out.Dynamic = &dyn
		if item, ok, _ := b.lib.GetItem(library.CanonicalItemKey("bilibili", "dynamic", dyn.ID)); ok {
			out.CachedItem = &item
		}
		var coverURL string
		if len(dyn.Images) > 0 {
			coverURL = dyn.Images[0].URL
		} else if len(dyn.Videos) > 0 {
			coverURL = dyn.Videos[0].Cover
		}
		if err := b.lib.UpsertFeedEntry(library.FeedEntry{
			Site:         "bilibili",
			Kind:         "dynamic",
			RawID:        dyn.ID,
			Mid:          dyn.Author.Mid,
			AuthorName:   dyn.Author.Name,
			AuthorFace:   dyn.Author.Face,
			Title:        dyn.Title,
			Summary:      dyn.Text,
			CoverURL:     coverURL,
			CanonicalURL: dyn.CanonicalURL,
			PubTs:        dyn.PubTs,
		}); err != nil {
			return nil, &bilibili.SiteError{Site: site.Bilibili, Kind: "storage", Message: "解析记录写入失败", Err: err}
		}
		out.HistoryThumbs = &HistoryThumbJob{Site: "bilibili", Kind: "dynamic", ID: dyn.ID, AvatarURL: dyn.Author.Face, CoverURL: coverURL}
		if err := b.history.Record(storage.HistoryEntry{
			Site:         "bilibili",
			Kind:         "dynamic",
			ID:           dyn.ID,
			Title:        dyn.Title,
			Subtitle:     dyn.Author.Name,
			CanonicalURL: dyn.CanonicalURL,
		}); err != nil {
			return nil, &bilibili.SiteError{Site: site.Bilibili, Kind: "storage", Message: "历史记录写入失败", Err: err}
		}

	case "live":
		room, err := b.live.OpenRoom(ctx, target.RoomID)
		if err != nil {
			b.logError("OpenLink.LiveRoom", err)
			return nil, err
		}
		out.LiveRoom = &room
		space, err := b.bili.UserCard(ctx, room.UID)
		if err != nil {
			return nil, err
		}
		out.User = &space
		if dyns, dErr := b.bili.UserDynamics(ctx, room.UID, ""); dErr == nil {
			out.UserDynamics = &dyns
		} else {
			out.UserDynamicsError = dErr.Error()
		}
		liveID := fmt.Sprintf("live:%d", room.RoomID)
		out.HistoryThumbs = &HistoryThumbJob{Site: "bilibili", Kind: "live", ID: liveID, AvatarURL: space.Face, CoverURL: room.Cover}
		if err := b.history.Record(storage.HistoryEntry{
			Site:         "bilibili",
			Kind:         "live",
			ID:           liveID,
			Title:        room.Title,
			Subtitle:     room.Uname,
			CanonicalURL: fmt.Sprintf("https://live.bilibili.com/%d", room.RoomID),
		}); err != nil {
			return nil, &bilibili.SiteError{Site: site.Bilibili, Kind: "storage", Message: "历史记录写入失败", Err: err}
		}
	}

	return out, nil
}

func (b *Backend) openDouyinLink(ctx context.Context, normalizedInput string) (*OpenLinkResult, error) {
	target, err := b.douyin.ResolveLink(ctx, normalizedInput)
	if err != nil {
		b.logError("OpenLink.DouyinResolve", err)
		return nil, err
	}
	out := &OpenLinkResult{
		Site:       string(site.Douyin),
		SiteTarget: &target,
		Target: bilibili.ResolveTarget{
			Kind:         target.Kind,
			ID:           target.ID,
			CanonicalURL: target.CanonicalURL,
			Unsupported:  target.Unsupported,
			Reason:       target.Reason,
		},
	}

	switch target.Kind {
	case "video", "note":
		wv, err := b.DouyinWork(ctx, target.ID)
		if err != nil {
			return nil, err
		}
		out.DouyinWork = wv
		out.CachedItem = wv.CachedItem
		feedKind := "video"
		histKind := "video"
		if wv.Kind == douyin.WorkKindNote {
			feedKind = "dynamic"
			histKind = "note"
		}
		out.Target.Kind = histKind
		out.SiteTarget.Kind = histKind
		if err := b.lib.UpsertFeedEntry(library.FeedEntry{
			Site:         "douyin",
			Kind:         feedKind,
			RawID:        wv.ID,
			AuthorUID:    wv.Author.SecUID,
			AuthorName:   wv.Author.Nickname,
			AuthorFace:   wv.Author.AvatarURL,
			Title:        wv.Title,
			Summary:      wv.Desc,
			CoverURL:     wv.CoverURL,
			CanonicalURL: wv.CanonicalURL,
			PubTs:        wv.CreateTime,
		}); err != nil {
			return nil, &site.Error{Site: site.Douyin, Kind: "storage", Message: "解析记录写入失败", Err: err}
		}
		out.HistoryThumbs = &HistoryThumbJob{
			Site:      "douyin",
			Kind:      histKind,
			ID:        wv.ID,
			AvatarURL: wv.Author.AvatarURL,
			CoverURL:  wv.CoverURL,
		}
		if err := b.history.Record(storage.HistoryEntry{
			Site:         "douyin",
			Kind:         histKind,
			ID:           wv.ID,
			Title:        wv.Title,
			Subtitle:     wv.Author.Nickname,
			CanonicalURL: wv.CanonicalURL,
			DurationText: formatHistoryDuration(wv.DurationSec),
		}); err != nil {
			return nil, &site.Error{Site: site.Douyin, Kind: "storage", Message: "历史记录写入失败", Err: err}
		}

	case "user":
		secUID := firstNonEmpty(target.SecUID, target.ID)
		uv, err := b.DouyinUser(ctx, secUID)
		if err != nil {
			return nil, err
		}
		out.DouyinUser = uv
		out.Target.ID = uv.SecUID
		out.SiteTarget.ID = uv.SecUID
		out.SiteTarget.SecUID = uv.SecUID
		if !uv.IsPrivate {
			if wp, wErr := b.DouyinUserWorks(ctx, uv.SecUID, ""); wErr == nil {
				out.DouyinUserWorks = wp
			} else {
				out.DouyinUserWorksError = wErr.Error()
			}
		}
		out.HistoryThumbs = &HistoryThumbJob{
			Site:      "douyin",
			Kind:      "user",
			ID:        uv.SecUID,
			AvatarURL: uv.AvatarURL,
		}
		if err := b.history.Record(storage.HistoryEntry{
			Site:         "douyin",
			Kind:         "user",
			ID:           uv.SecUID,
			Title:        uv.Nickname,
			Subtitle:     uv.Signature,
			CanonicalURL: uv.CanonicalURL,
		}); err != nil {
			return nil, &site.Error{Site: site.Douyin, Kind: "storage", Message: "历史记录写入失败", Err: err}
		}

	case "live":
		webRID := firstNonEmpty(target.WebRID, target.ID)
		lv, err := b.DouyinLive(ctx, webRID)
		if err != nil {
			return nil, err
		}
		out.DouyinLive = lv
		if lv.OwnerSecUID != "" {
			if uv, uErr := b.DouyinUser(ctx, lv.OwnerSecUID); uErr == nil {
				if lv.State != nil {
					uv.LiveRoom = lv.State
					if lv.LiveStatus == 1 {
						uv.LiveStatus = 1
					}
				}
				out.DouyinUser = uv
				if !uv.IsPrivate {
					if wp, wErr := b.DouyinUserWorks(ctx, uv.SecUID, ""); wErr == nil {
						out.DouyinUserWorks = wp
					} else {
						out.DouyinUserWorksError = wErr.Error()
					}
				}
			}
		}
		liveID := "live:" + lv.WebRID
		out.HistoryThumbs = &HistoryThumbJob{
			Site:      "douyin",
			Kind:      "live",
			ID:        liveID,
			AvatarURL: lv.OwnerAvatar,
			CoverURL:  lv.CoverURL,
		}
		if err := b.history.Record(storage.HistoryEntry{
			Site:         "douyin",
			Kind:         "live",
			ID:           liveID,
			Title:        firstNonEmpty(lv.Title, lv.OwnerName+"的直播间"),
			Subtitle:     lv.OwnerName,
			CanonicalURL: lv.CanonicalURL,
		}); err != nil {
			return nil, &site.Error{Site: site.Douyin, Kind: "storage", Message: "历史记录写入失败", Err: err}
		}
	}

	return out, nil
}

func formatHistoryDuration(seconds int) string {
	if seconds <= 0 {
		return ""
	}
	h := seconds / 3600
	m := (seconds % 3600) / 60
	s := seconds % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

// FetchHistoryThumbs downloads and stores the thumbnails of a recent-parse
// entry after OpenLink returned. Image failures leave the placeholder; an
// entry removed meanwhile gets nothing.
func (b *Backend) FetchHistoryThumbs(ctx context.Context, job HistoryThumbJob) error {
	jobSite, err := normalizeSiteID(job.Site)
	if err != nil {
		return err
	}
	if err := b.requireSiteLogin(jobSite); err != nil {
		return err
	}
	if b.history == nil {
		return nil
	}
	var thumbs []storage.HistoryThumbnail
	for _, want := range []struct{ role, url string }{{"avatar", job.AvatarURL}, {"cover", job.CoverURL}} {
		if t := b.fetchHistoryThumbForSite(ctx, jobSite, want.role, want.url); len(t.Data) > 0 {
			thumbs = append(thumbs, t)
		}
	}
	if len(thumbs) == 0 {
		return nil
	}
	return b.history.AttachThumbs(string(jobSite), job.Kind, job.ID, thumbs...)
}

func (b *Backend) fetchHistoryThumb(ctx context.Context, role, rawURL string) storage.HistoryThumbnail {
	return b.fetchHistoryThumbForSite(ctx, site.Bilibili, role, rawURL)
}

func (b *Backend) fetchHistoryThumbForSite(ctx context.Context, siteID site.ID, role, rawURL string) storage.HistoryThumbnail {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" || b.history == nil {
		return storage.HistoryThumbnail{}
	}
	if strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "http://127.0.0.1") && !strings.HasPrefix(rawURL, "http://localhost") {
		rawURL = "https://" + strings.TrimPrefix(rawURL, "http://")
	}
	var raw []byte
	var err error
	switch siteID {
	case site.Douyin:
		if b.douyin == nil {
			return storage.HistoryThumbnail{}
		}
		raw, _, err = b.douyin.FetchImageLimited(ctx, rawURL, storage.MaxHistoryThumbBytes)
	default:
		if b.bili == nil {
			return storage.HistoryThumbnail{}
		}
		raw, err = b.bili.FetchImageLimited(ctx, rawURL, storage.MaxHistoryThumbBytes)
	}
	if err != nil || len(raw) == 0 || len(raw) > storage.MaxHistoryThumbBytes {
		return storage.HistoryThumbnail{}
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 24_000_000 {
		return storage.HistoryThumbnail{}
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return storage.HistoryThumbnail{}
	}
	w, h := cfg.Width, cfg.Height
	maxSide := 640
	if role == "avatar" {
		maxSide = 128
	}
	if w > maxSide {
		h, w = max(1, h*maxSide/w), maxSide
	}
	if h > maxSide {
		w, h = max(1, w*maxSide/h), maxSide
	}
	small := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.ApproxBiLinear.Scale(small, small.Bounds(), img, img.Bounds(), draw.Src, nil)
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, small); err != nil {
		return storage.HistoryThumbnail{}
	}
	return storage.HistoryThumbnail{Role: role, Ext: ".png", Data: encoded.Bytes()}
}

// HistoryThumbPath resolves a local cached parse-history thumbnail inside <DataDir>/thumbs/.
func (b *Backend) HistoryThumbPath(name string) (string, error) {
	if err := b.requireAnyLogin(); err != nil {
		return "", err
	}
	if b.history == nil {
		return "", fmt.Errorf("service: history store unavailable")
	}
	return b.history.ThumbPath(name)
}

// ListHistory returns the recent sanitized navigation history entries.
func (b *Backend) ListHistory() []storage.HistoryEntry {
	if b.requireAnyLogin() != nil {
		return nil
	}
	return b.history.List()
}

// ClearHistory clears the navigation history.
func (b *Backend) ClearHistory() error {
	if err := b.requireAnyLogin(); err != nil {
		return err
	}
	return b.history.Clear()
}

// EnqueueVideoCache enqueues Bilibili video parts for local caching.
func (b *Backend) EnqueueVideoCache(ctx context.Context, req download.VideoEnqueueRequest) ([]download.Task, error) {
	if err := b.requireSiteLogin(site.Bilibili); err != nil {
		return nil, err
	}
	tasks, err := b.dl.EnqueueVideo(ctx, req)
	if err != nil {
		b.logError("EnqueueVideoCache", err)
		return nil, err
	}
	b.events.PublishSite(EventTaskUpdated, string(site.Bilibili), "enqueued:video:"+req.BVID)
	return tasks, nil
}

// EnqueueDynamicCache enqueues a Bilibili dynamic post for local caching.
func (b *Backend) EnqueueDynamicCache(ctx context.Context, req download.DynamicEnqueueRequest) (download.Task, error) {
	if err := b.requireSiteLogin(site.Bilibili); err != nil {
		return download.Task{}, err
	}
	task, err := b.dl.EnqueueDynamic(ctx, req)
	if err != nil {
		b.logError("EnqueueDynamicCache", err)
		return download.Task{}, err
	}
	b.events.PublishSite(EventTaskUpdated, string(site.Bilibili), "enqueued:dynamic:"+req.DynamicID)
	return task, nil
}

// CacheSupplement re-downloads missing danmaku/subtitle/cover sidecars for an already cached Bilibili video without re-downloading video/audio.
func (b *Backend) CacheSupplement(ctx context.Context, bvid string) (download.Task, error) {
	if err := b.requireSiteLogin(site.Bilibili); err != nil {
		return download.Task{}, err
	}
	task, err := b.dl.CacheSupplement(ctx, bvid)
	if err != nil {
		b.logError("CacheSupplement", err)
		return download.Task{}, err
	}
	b.events.PublishSite(EventTaskUpdated, string(site.Bilibili), "enqueued:supplement:"+bvid)
	return task, nil
}

// Upgrade enqueues a quality upgrade download task for a cached Bilibili video item.
func (b *Backend) Upgrade(ctx context.Context, key string) (download.Task, error) {
	if err := b.requireSiteLogin(site.Bilibili); err != nil {
		return download.Task{}, err
	}
	task, err := b.dl.Upgrade(ctx, key)
	if err != nil {
		b.logError("Upgrade", err)
		return download.Task{}, err
	}
	b.events.PublishSite(EventTaskUpdated, string(site.Bilibili), "enqueued:upgrade:"+key)
	return task, nil
}

// ListDownloadTasks returns all download tasks across sites.
func (b *Backend) ListDownloadTasks() ([]download.Task, error) {
	if err := b.requireAnyLogin(); err != nil {
		return nil, err
	}
	return b.dl.ListTasks()
}

// TaskDetail returns per-file progress and sanitized attempt history for a download task.
func (b *Backend) TaskDetail(taskID string) (download.TaskDetail, bool, error) {
	if err := b.requireAnyLogin(); err != nil {
		return download.TaskDetail{}, false, err
	}
	return b.dl.TaskDetail(taskID)
}

// PauseTask pauses a download task.
func (b *Backend) PauseTask(taskID string) (download.Task, error) {
	if err := b.requireAnyLogin(); err != nil {
		return download.Task{}, err
	}
	return b.dl.Pause(taskID)
}

func (b *Backend) requireTaskSiteLogin(taskID string) error {
	if err := b.requireAnyLogin(); err != nil {
		return err
	}
	if detail, ok, err := b.dl.TaskDetail(taskID); err == nil && ok {
		tSite, sErr := normalizeSiteID(detail.Task.Site)
		if sErr != nil {
			return sErr
		}
		return b.requireSiteLogin(tSite)
	}
	return nil
}

// ResumeTask resumes a paused download task after verifying its owning site is logged in.
func (b *Backend) ResumeTask(taskID string) (download.Task, error) {
	if err := b.requireTaskSiteLogin(taskID); err != nil {
		return download.Task{}, err
	}
	return b.dl.Resume(taskID)
}

// RetryTask retries a failed or partial download task after verifying its owning site is logged in.
func (b *Backend) RetryTask(taskID string) (download.Task, error) {
	if err := b.requireTaskSiteLogin(taskID); err != nil {
		return download.Task{}, err
	}
	return b.dl.Retry(taskID)
}

// CancelTask cancels a download task and removes incomplete .part files.
func (b *Backend) CancelTask(taskID string) (download.Task, error) {
	if err := b.requireAnyLogin(); err != nil {
		return download.Task{}, err
	}
	return b.dl.Cancel(taskID)
}

// RemoveTask deletes a finished task record; cached files stay in the library.
func (b *Backend) RemoveTask(taskID string) error {
	if err := b.requireAnyLogin(); err != nil {
		return err
	}
	if err := b.dl.Remove(taskID); err != nil {
		return err
	}
	b.events.Publish(EventTaskUpdated, "removed:"+taskID)
	return nil
}

// RemoveFinishedTasks clears every completed or canceled task record.
func (b *Backend) RemoveFinishedTasks() (int, error) {
	if err := b.requireAnyLogin(); err != nil {
		return 0, err
	}
	n, err := b.dl.RemoveFinished()
	if n > 0 {
		b.events.Publish(EventTaskUpdated, "removed:finished")
	}
	return n, err
}

// QueryFeed queries the unified feed (cached + online-discovered items) ordered by pub_ts DESC.
func (b *Backend) QueryFeed(filter library.FeedFilter) ([]library.FeedEntry, error) {
	if err := b.requireAnyLogin(); err != nil {
		return nil, err
	}
	return b.lib.QueryFeed(filter)
}

// FeedPage queries a keyset-paginated page of the home feed.
func (b *Backend) FeedPage(req library.FeedPageRequest) (library.FeedPageResult, error) {
	if err := b.requireAnyLogin(); err != nil {
		return library.FeedPageResult{}, err
	}
	return b.lib.FeedPage(req)
}

// Anchor returns the persisted home feed reading anchor from ui-state.json.
func (b *Backend) Anchor() uistate.FeedAnchor {
	if b.requireAnyLogin() != nil {
		return uistate.FeedAnchor{}
	}
	return b.uiState.Get().Anchor
}

// SaveAnchor persists the home feed reading anchor into ui-state.json.
func (b *Backend) SaveAnchor(anchor uistate.FeedAnchor) error {
	if err := b.requireAnyLogin(); err != nil {
		return err
	}
	st := b.uiState.Get()
	st.Anchor = anchor
	_, err := b.uiState.Update(st)
	return err
}

// NewerCount returns the number of feed entries strictly newer than anchor.
func (b *Backend) NewerCount(anchor uistate.FeedAnchor) (int, error) {
	if err := b.requireAnyLogin(); err != nil {
		return 0, err
	}
	return b.lib.NewerCachedCount(anchor.PubTs, anchor.Key)
}

// ListLibraryItems returns locally cached items matching filter.
func (b *Backend) ListLibraryItems(filter library.FeedFilter) ([]library.ItemMetadata, error) {
	if err := b.requireAnyLogin(); err != nil {
		return nil, err
	}
	return b.lib.ListItems(filter)
}

// GetLibraryItem returns a single cached item by itemKey or rawID.
func (b *Backend) GetLibraryItem(keyOrID string) (library.ItemMetadata, bool, error) {
	if err := b.requireAnyLogin(); err != nil {
		return library.ItemMetadata{}, false, err
	}
	return b.lib.GetItem(keyOrID)
}

// MediaPath validates and resolves a registered relative file path within a cached item.
func (b *Backend) MediaPath(key, relPath string) (string, error) {
	if err := b.requireAnyLogin(); err != nil {
		return "", err
	}
	return b.lib.MediaPath(key, relPath)
}

// States computes the canonical cache status dots for the given item keys.
func (b *Backend) States(keys []string) map[string]library.DotState {
	if b.requireAnyLogin() != nil {
		out := make(map[string]library.DotState, len(keys))
		for _, key := range keys {
			out[key] = library.DotState{Key: key, Dot: "none", Label: "登录后查看缓存状态"}
		}
		return out
	}
	out := b.lib.States(keys)
	activeProgress := make(map[string]float64)
	latest := make(map[string]download.Task)
	if tasks, err := b.dl.ListTasks(); err == nil {
		for _, t := range tasks {
			for _, key := range []string{t.ItemKey, t.TargetID} {
				if key == "" {
					continue
				}
				if _, exists := latest[key]; !exists {
					latest[key] = t
				}
			}
			if t.Status == "queued" || t.Status == "downloading" || t.Status == "muxing" || t.Status == "running" {
				prog := t.Progress
				if prog == 0 && t.TotalBytes > 0 {
					prog = float64(t.BytesDownloaded) / float64(t.TotalBytes)
				}
				if t.ItemKey != "" {
					activeProgress[t.ItemKey] = prog
				}
				if t.TargetID != "" {
					activeProgress[t.TargetID] = prog
				}
			}
		}
	}
	for _, k := range keys {
		if prog, active := activeProgress[k]; active {
			out[k] = library.DotState{
				Key:      k,
				Dot:      "ring",
				Label:    "缓存中",
				Progress: prog,
			}
			continue
		}
		if task, exists := latest[k]; exists {
			switch task.Status {
			case "paused", "partial":
				dot := out[k]
				dot.Dot, dot.Label = "yellow", "部分缓存，重试可继续"
				if task.Status == "paused" {
					dot.Label = "已暂停，可继续"
				}
				out[k] = dot
			case "failed":
				dot := out[k]
				dot.Dot, dot.Label = "red", "缓存失败，可重试"
				out[k] = dot
			}
		}
	}
	return out
}

// PlanDelete computes the safe file deletion plan for an item key.
func (b *Backend) PlanDelete(key string) (library.DeletePlan, error) {
	if err := b.requireAnyLogin(); err != nil {
		return library.DeletePlan{}, err
	}
	return b.lib.PlanDelete(key)
}

// Delete executes a safe manifest-only file deletion for an item key.
func (b *Backend) Delete(key string) error {
	return b.DeleteLibraryItem(key, true)
}

// RebuildLibraryIndex scans CacheDir for item.json files and rebuilds the SQLite index.
func (b *Backend) RebuildLibraryIndex() (library.RebuildReport, error) {
	if err := b.requireAnyLogin(); err != nil {
		return library.RebuildReport{}, err
	}
	rep, err := b.lib.RebuildIndex()
	if len(rep.CorruptBackedUp) > 0 {
		b.logMu.Lock()
		b.corruptFiles = append(b.corruptFiles, rep.CorruptBackedUp...)
		b.logMu.Unlock()
	}
	b.events.Publish(EventLibraryChanged, "rebuilt")
	return rep, err
}

// AddLibraryRoot adds an existing cache root or legacy trace-data directory and indexes its manifests.
func (b *Backend) AddLibraryRoot(selectedDir string) (library.AddRootReport, error) {
	if err := b.requireAnyLogin(); err != nil {
		return library.AddRootReport{}, err
	}
	rep, err := b.lib.AddLibraryRoot(selectedDir)
	if err == nil {
		b.events.Publish(EventLibraryChanged, "root_added")
	}
	return rep, err
}

// DeleteLibraryItem removes an item from the index and optionally deletes its registered files from disk.
func (b *Backend) DeleteLibraryItem(itemKey string, deleteFiles bool) error {
	if err := b.requireAnyLogin(); err != nil {
		return err
	}
	if err := b.lib.DeleteItem(itemKey, deleteFiles); err != nil {
		return err
	}
	b.events.Publish(EventLibraryChanged, "deleted:"+itemKey)
	return nil
}

// MigrateCacheDir changes the active cache directory, optionally moves existing files, and updates settings.json.
func (b *Backend) MigrateCacheDir(newDir string, moveExisting bool) (library.RebuildReport, error) {
	if err := b.requireAnyLogin(); err != nil {
		return library.RebuildReport{}, err
	}
	if !moveExisting {
		cur := b.settings.Get()
		cur.CacheRoot, cur.CacheDir = newDir, newDir
		_, err := b.UpdateSettings(cur)
		return library.RebuildReport{}, err
	}
	b.settingsMu.Lock()
	defer b.settingsMu.Unlock()
	cur := b.settings.Get()
	cur.CacheDir, cur.CacheRoot = newDir, newDir
	cur, err := b.settings.PreviewUpdate(cur)
	if err != nil {
		return library.RebuildReport{}, err
	}
	var rep library.RebuildReport
	err = b.dl.WithIdleTasks(func() error {
		var migrationErr error
		rep, migrationErr = b.lib.MigrateCacheDirWithPersistence(newDir, true, func() error { _, err := b.settings.Update(cur); return err })
		return migrationErr
	})
	if err != nil {
		return rep, err
	}
	b.events.Publish(EventLibraryChanged, "migrated")
	return rep, nil
}

// UpgradeCandidates returns up to limit cached Bilibili video items ordered by oldest UpgradeCheckedAt.
func (b *Backend) UpgradeCandidates(limit int) ([]library.ItemMetadata, error) {
	if err := b.requireAnyLogin(); err != nil {
		return nil, err
	}
	return b.lib.UpgradeCandidates(limit)
}

// CheckUpgradeKeys checks the specified Bilibili video keys (or up to 50 candidates when empty) for quality upgrades.
func (b *Backend) CheckUpgradeKeys(ctx context.Context, keys []string, force bool) ([]library.ItemMetadata, error) {
	if err := b.requireSiteLogin(site.Bilibili); err != nil {
		return nil, err
	}
	dlCfg := b.settings.Get().Download
	opts := bilibili.PlaySelectionOptions{
		CodecOrder:          append([]string(nil), dlCfg.CodecOrder...),
		PreferredCodec:      dlCfg.PreferredVideoCodec,
		PreferLosslessAudio: dlCfg.PreferLosslessAudio,
		PreferDolbyAudio:    dlCfg.PreferDolbyAudio,
		TargetQn:            127,
	}
	items, err := b.lib.CheckUpgradeKeys(ctx, b.bili, keys, force, opts)
	if err != nil {
		b.logError("CheckUpgradeKeys", err)
		return items, err
	}
	b.events.PublishSite(EventLibraryChanged, string(site.Bilibili), "upgrade_checked")
	return items, nil
}

// CheckQualityUpgrade checks cached Bilibili videos against PlayURL for higher video/audio quality.
func (b *Backend) CheckQualityUpgrade(ctx context.Context) ([]library.ItemMetadata, error) {
	return b.CheckUpgradeKeys(ctx, nil, true)
}

// ListSubscriptions returns all creator subscriptions.
func (b *Backend) ListSubscriptions() ([]subscription.Subscription, error) {
	if err := b.requireAnyLogin(); err != nil {
		return nil, err
	}
	return b.subs.List()
}

// UpsertSubscription creates or updates a creator subscription.
func (b *Backend) UpsertSubscription(sub subscription.Subscription) (subscription.Subscription, error) {
	if err := b.requireAnyLogin(); err != nil {
		return subscription.Subscription{}, err
	}
	res, err := b.subs.Upsert(sub)
	if err == nil {
		b.events.Publish(EventSubscriptionChecked, "upserted")
	}
	return res, err
}

// CreateSubscription creates a new multi-account subscription.
func (b *Backend) CreateSubscription(name, note string, accounts []subscription.AccountRef, rules subscription.SubRules) (subscription.Subscription, error) {
	if err := b.requireAnyLogin(); err != nil {
		return subscription.Subscription{}, err
	}
	res, err := b.subs.Create(name, note, accounts, rules)
	if err == nil {
		b.events.Publish(EventSubscriptionChecked, "created")
	}
	return res, err
}

// LinkSubscriptionAccount links an account to an existing subscription.
func (b *Backend) LinkSubscriptionAccount(subID, siteID, uid, name, face string) (subscription.Subscription, error) {
	if err := b.requireAnyLogin(); err != nil {
		return subscription.Subscription{}, err
	}
	res, err := b.subs.Link(subID, siteID, uid, name, face)
	if err == nil {
		b.events.Publish(EventSubscriptionChecked, "linked")
	}
	return res, err
}

// UnlinkSubscriptionAccount removes an account from a subscription.
func (b *Backend) UnlinkSubscriptionAccount(subID, siteID, uid string) (subscription.Subscription, error) {
	if err := b.requireAnyLogin(); err != nil {
		return subscription.Subscription{}, err
	}
	res, err := b.subs.Unlink(subID, siteID, uid)
	if err == nil {
		b.events.Publish(EventSubscriptionChecked, "unlinked")
	}
	return res, err
}

// MoveSubscriptionAccount atomically moves an account from its current subscription to toSubID.
func (b *Backend) MoveSubscriptionAccount(siteID, uid, toSubID string) (subscription.Subscription, error) {
	if err := b.requireAnyLogin(); err != nil {
		return subscription.Subscription{}, err
	}
	res, err := b.subs.Move(siteID, uid, toSubID)
	if err == nil {
		b.events.Publish(EventSubscriptionChecked, "moved")
	}
	return res, err
}

// SetSubscriptionRules updates per-subscription rules and manages AutoCache.Since.
func (b *Backend) SetSubscriptionRules(subID string, rules subscription.SubRules) (subscription.Subscription, error) {
	if err := b.requireAnyLogin(); err != nil {
		return subscription.Subscription{}, err
	}
	res, err := b.subs.SetRules(subID, rules)
	if err == nil {
		b.events.Publish(EventSubscriptionChecked, "rules_updated")
	}
	return res, err
}

// ListCacheRules returns the shared cache rules, the built-in one first.
func (b *Backend) ListCacheRules() []subscription.CacheRule {
	return b.subs.ListRules()
}

// SaveCacheRule creates or edits a shared rule and applies it to every subscription that uses it.
func (b *Backend) SaveCacheRule(rule subscription.CacheRule) (subscription.CacheRule, error) {
	if err := b.requireAnyLogin(); err != nil {
		return subscription.CacheRule{}, err
	}
	res, err := b.subs.SaveRule(rule)
	if err == nil {
		b.events.Publish(EventSubscriptionChecked, "rules_updated")
	}
	return res, err
}

// DeleteCacheRule removes a shared rule; its subscriptions use the default.
func (b *Backend) DeleteCacheRule(id string) error {
	if err := b.requireAnyLogin(); err != nil {
		return err
	}
	err := b.subs.DeleteRule(id)
	if err == nil {
		b.events.Publish(EventSubscriptionChecked, "rules_updated")
	}
	return err
}

// SetSubscriptionRule makes a subscription use a shared rule.
func (b *Backend) SetSubscriptionRule(subID, ruleID string) (subscription.Subscription, error) {
	if err := b.requireAnyLogin(); err != nil {
		return subscription.Subscription{}, err
	}
	res, err := b.subs.SetSubscriptionRule(subID, ruleID)
	if err == nil {
		b.events.Publish(EventSubscriptionChecked, "rules_updated")
	}
	return res, err
}

// PreviewAccount resolves a Bilibili or Douyin account input and checks whether it is already owned by a subscription.
func (b *Backend) PreviewAccount(ctx context.Context, input string) (subscription.AccountRef, *subscription.Subscription, error) {
	if err := b.requireAnyLogin(); err != nil {
		return subscription.AccountRef{}, nil, err
	}
	siteID, normalized, err := b.registry.DetectSite(input)
	if err != nil {
		return subscription.AccountRef{}, nil, err
	}
	if err := b.requireSiteLogin(siteID); err != nil {
		return subscription.AccountRef{}, nil, err
	}

	if siteID == site.Douyin {
		target, rErr := b.douyin.ResolveLink(ctx, normalized)
		if rErr != nil {
			return subscription.AccountRef{}, nil, rErr
		}
		var secUID string
		switch target.Kind {
		case "user":
			secUID = firstNonEmpty(target.SecUID, target.ID)
		case "live":
			lv, lErr := b.DouyinLive(ctx, firstNonEmpty(target.WebRID, target.ID))
			if lErr != nil {
				return subscription.AccountRef{}, nil, lErr
			}
			secUID = lv.OwnerSecUID
		default:
			return subscription.AccountRef{}, nil, &site.Error{
				Site:    site.Douyin,
				Kind:    site.ErrKindBadRequest,
				Message: "请粘贴抖音用户主页链接、分享文本或 sec_uid",
			}
		}
		uv, uErr := b.DouyinUser(ctx, secUID)
		if uErr != nil {
			return subscription.AccountRef{}, nil, uErr
		}
		acc := subscription.AccountRef{
			Site:     string(site.Douyin),
			UID:      uv.SecUID,
			UniqueID: uv.UniqueID,
			WebRID:   uv.WebRID,
			Name:     uv.Nickname,
			Face:     uv.AvatarURL,
			AddedAt:  time.Now().UTC().Format(time.RFC3339),
		}
		return b.subs.PreviewResolvedAccount(acc)
	}

	target, err := b.bili.ResolveLink(ctx, normalized)
	if err != nil {
		return subscription.AccountRef{}, nil, err
	}
	if target.Kind != "user" {
		return subscription.AccountRef{}, nil, fmt.Errorf("请粘贴用户主页链接或 UID")
	}
	card, err := b.bili.UserCard(ctx, target.Mid)
	if err != nil {
		return subscription.AccountRef{}, nil, err
	}
	uidStr := fmt.Sprintf("%d", card.Mid)
	acc := subscription.AccountRef{
		Site:    "bilibili",
		UID:     uidStr,
		Name:    card.Name,
		Face:    card.Face,
		AddedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if owner, owned := b.subs.OwnerOf("bilibili", uidStr); owned {
		return acc, &owner, nil
	}
	return acc, nil, nil
}

// OwnerOfAccount returns the subscription owning (site, uid), if any.
func (b *Backend) OwnerOfAccount(siteID, uid string) (subscription.Subscription, bool) {
	if b.requireAnyLogin() != nil {
		return subscription.Subscription{}, false
	}
	return b.subs.OwnerOf(siteID, uid)
}

// ListSubscriptionUpdates returns discovered update items for a subscription.
func (b *Backend) ListSubscriptionUpdates(subID string) ([]subscription.UpdateItem, error) {
	if err := b.requireAnyLogin(); err != nil {
		return nil, err
	}
	return b.subs.ListUpdates(subID)
}

// DeleteSubscription removes a creator subscription.
func (b *Backend) DeleteSubscription(subIDOrMid string) error {
	if err := b.requireAnyLogin(); err != nil {
		return err
	}
	err := b.subs.Delete(subIDOrMid)
	if err == nil {
		b.events.Publish(EventSubscriptionChecked, "deleted")
	}
	return err
}

// CheckSubscriptionsNow triggers an immediate incremental check for targetMid (or 0 for all enabled).
func (b *Backend) CheckSubscriptionsNow(ctx context.Context, targetMid int64) (subscription.CheckSummary, error) {
	if err := b.requireAnyLogin(); err != nil {
		return subscription.CheckSummary{}, err
	}
	if targetMid > 0 {
		if err := b.requireSiteLogin(site.Bilibili); err != nil {
			return subscription.CheckSummary{}, err
		}
	}
	return b.subs.CheckNow(ctx, targetMid)
}

// ExportOPML writes all subscriptions as an OPML 2.0 XML file to path.
func (b *Backend) ExportOPML(path string) error {
	if err := b.requireAnyLogin(); err != nil {
		return err
	}
	subs, err := b.subs.List()
	if err != nil {
		return err
	}
	xmlBytes, err := subscription.ExportOPML(subs, time.Now())
	if err != nil {
		return err
	}
	return storage.WriteFileAtomic(path, xmlBytes, 0o600)
}

// ImportOPML imports creator subscriptions from an OPML 2.0 XML file at path.
func (b *Backend) ImportOPML(path string) (int, error) {
	if err := b.requireAnyLogin(); err != nil {
		return 0, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	creators, err := subscription.ParseOPML(raw)
	if err != nil {
		return 0, err
	}
	users := make([]bilibili.FollowingUser, 0, len(creators))
	for _, c := range creators {
		users = append(users, bilibili.FollowingUser{
			Mid:   c.Mid,
			Uname: c.Uname,
		})
	}
	n, err := b.subs.ImportCreators(users, "both", "inherit", true, false)
	if err == nil {
		b.events.Publish(EventSubscriptionChecked, "opml_imported")
	}
	return n, err
}

// RelationTags returns the logged-in user's Bilibili following groups.
func (b *Backend) RelationTags(ctx context.Context) ([]bilibili.RelationTag, error) {
	if err := b.requireSiteLogin(site.Bilibili); err != nil {
		return nil, err
	}
	return b.bili.RelationTags(ctx)
}

// RelationFollowings returns a page of Bilibili followings (either for a specific tagID > 0 or all followings when tagID == 0).
func (b *Backend) RelationFollowings(ctx context.Context, tagID int64, pn, ps int) ([]bilibili.FollowingUser, error) {
	if err := b.requireSiteLogin(site.Bilibili); err != nil {
		return nil, err
	}
	if tagID != 0 {
		mid := b.cred.Status().Mid
		return b.bili.RelationTagUsers(ctx, mid, tagID, pn, ps)
	}
	page, err := b.bili.RelationFollowings(ctx, 0, pn, ps)
	if err != nil {
		return nil, err
	}
	return page.Items, nil
}

// ImportCreators imports selected Bilibili creators into subscriptions idempotently.
func (b *Backend) ImportCreators(users []bilibili.FollowingUser, scope, autoCache string, liveNotify, liveAutoRecord bool) (int, error) {
	if err := b.requireSiteLogin(site.Bilibili); err != nil {
		return 0, err
	}
	n, err := b.subs.ImportCreators(users, scope, autoCache, liveNotify, liveAutoRecord)
	if err == nil {
		b.events.Publish(EventSubscriptionChecked, "imported")
	}
	return n, err
}

// ListLiveRooms returns the current live status and recording state of all Bilibili rooms.
func (b *Backend) ListLiveRooms() []live.RoomState {
	if b.requireAnyLogin() != nil {
		return nil
	}
	return b.live.ListRooms()
}

// PollLiveNow triggers an immediate live status batch poll.
func (b *Backend) PollLiveNow(ctx context.Context) ([]live.RoomState, error) {
	if err := b.requireAnyLogin(); err != nil {
		return nil, err
	}
	return b.live.PollNow(ctx)
}

// OpenLiveRoomInBrowser opens a Bilibili live room in the external browser via Desktop.
func (b *Backend) OpenLiveRoomInBrowser(roomID int64) error {
	if err := b.requireAnyLogin(); err != nil {
		return err
	}
	if roomID <= 0 {
		return fmt.Errorf("service: invalid roomID %d", roomID)
	}
	return b.desktop.OpenExternalURL(fmt.Sprintf("https://live.bilibili.com/%d", roomID))
}

// StartLiveRecording starts recording a Bilibili live room.
func (b *Backend) StartLiveRecording(ctx context.Context, roomID int64) (live.RoomState, error) {
	if err := b.requireSiteLogin(site.Bilibili); err != nil {
		return live.RoomState{}, err
	}
	return b.live.StartRecording(ctx, roomID)
}

// StopLiveRecording stops an active Bilibili live room recording and finalizes item.json.
func (b *Backend) StopLiveRecording(roomID int64) (live.RoomState, error) {
	if err := b.requireAnyLogin(); err != nil {
		return live.RoomState{}, err
	}
	return b.live.StopRecording(roomID)
}

// OpenItemFolder opens or reveals a cached item's folder on disk via Desktop.
func (b *Backend) OpenItemFolder(itemKey string) error {
	if err := b.requireAnyLogin(); err != nil {
		return err
	}
	item, ok, err := b.lib.GetItem(itemKey)
	if err != nil {
		return err
	}
	if !ok || item.ItemDir == "" {
		return fmt.Errorf("service: item folder not found for %s", itemKey)
	}
	return b.desktop.OpenPath(item.ItemDir)
}

// OpenDataFolder opens the DataDir, CacheDir, or LogsDir via Desktop.
func (b *Backend) OpenDataFolder(target string) error {
	switch target {
	case "cache":
		return b.desktop.OpenPath(b.lib.CacheDir())
	case "logs":
		return b.desktop.OpenPath(b.loc.LogsDir)
	default:
		return b.desktop.OpenPath(b.loc.DataDir)
	}
}

// DiagnosticsSnapshot returns a sanitized runtime summary without any secret values.
func (b *Backend) DiagnosticsSnapshot() Diagnostics {
	stats, indexErr := b.lib.Statistics()
	tasks, tasksErr := b.dl.ListTasks()
	subs, subsErr := b.subs.List()
	rooms := b.live.ListRooms()

	b.logMu.Lock()
	corrupt := append([]string(nil), b.corruptFiles...)
	b.logMu.Unlock()

	indexStatus := fmt.Sprintf("正常（已索引 %d 项）", stats.Items)
	if indexErr != nil {
		indexStatus = "读取索引失败：" + b.sanitizeLogLine(indexErr.Error())
	} else if len(corrupt) > 0 {
		indexStatus = fmt.Sprintf("已保留 %d 个损坏文件，当前已索引 %d 项", len(corrupt), stats.Items)
	}
	subCheckStatus := fmt.Sprintf("空闲（共 %d 个订阅）", len(subs))
	if b.subs != nil {
		if progress, checking := b.subs.CheckProgress(); checking {
			subCheckStatus = fmt.Sprintf("正在检查更新 %d / %d", progress.CheckedCreators, progress.TotalCreators)
		} else if inBackoff, until := b.subs.IsInBackoff(); inBackoff {
			subCheckStatus = fmt.Sprintf("风控退避中（暂停至 %s）", until.Local().Format("15:04"))
		}
	}
	if subsErr != nil {
		subCheckStatus = "读取订阅失败：" + b.sanitizeLogLine(subsErr.Error())
	}
	var runningTasks, queuedTasks, autoTasks int
	for _, t := range tasks {
		switch t.Status {
		case "running":
			runningTasks++
		case "queued":
			queuedTasks++
		}
		if t.Source == "auto" && (t.Status == "running" || t.Status == "queued") {
			autoTasks++
		}
	}
	autoCacheStatus := "空闲"
	if runningTasks > 0 || queuedTasks > 0 {
		autoCacheStatus = fmt.Sprintf("进行中 %d · 排队 %d（自动缓存 %d）", runningTasks, queuedTasks, autoTasks)
	}
	if tasksErr != nil {
		autoCacheStatus = "读取任务失败：" + b.sanitizeLogLine(tasksErr.Error())
	}
	var recordingRooms, liveRooms int
	for _, r := range rooms {
		if r.LiveStatus == 1 {
			liveRooms++
		}
		if r.Recording {
			recordingRooms++
		}
	}
	liveRecordStatus := "空闲"
	if recordingRooms > 0 || liveRooms > 0 {
		liveRecordStatus = fmt.Sprintf("录制中 %d 路 · 直播中 %d 个房间", recordingRooms, liveRooms)
	}

	build := appdirs.BuildInfo()
	return Diagnostics{
		Version:                 build.Version,
		Commit:                  build.Commit,
		BuildTime:               build.BuildTime,
		AppName:                 b.loc.AppName,
		DisplayName:             b.loc.DisplayName,
		Channel:                 b.loc.Channel,
		Mode:                    b.loc.Mode,
		DataDir:                 b.loc.DataDir,
		CacheDir:                b.lib.CacheDir(),
		LogsDir:                 b.loc.LogsDir,
		Account:                 b.cred.Status(),
		Accounts:                b.Statuses(),
		LibraryCount:            stats.Items,
		TaskCount:               len(tasks),
		SubscriptionCnt:         len(subs),
		LiveRoomCount:           len(rooms),
		IndexStatus:             indexStatus,
		SubscriptionCheckStatus: subCheckStatus,
		AutoCacheStatus:         autoCacheStatus,
		LiveRecordStatus:        liveRecordStatus,
		CorruptRecovered:        corrupt,
		RecentEvents:            b.events.Recent(),
	}
}

const runtimeLogReadLimit = 1 << 20

// RecentLogs reads at most 1 MiB from the end of trace.log and returns at most
// 500 sanitized lines. An incomplete oldest line is discarded.
func (b *Backend) RecentLogs(maxLines int) ([]string, error) {
	if maxLines <= 0 || maxLines > 500 {
		maxLines = 500
	}
	if b.logPath == "" {
		return nil, nil
	}
	b.logMu.Lock()
	defer b.logMu.Unlock()
	f, err := os.Open(b.logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("运行日志不是普通文件")
	}
	start := max(int64(0), info.Size()-runtimeLogReadLimit)
	raw := make([]byte, info.Size()-start)
	n, err := f.ReadAt(raw, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	raw = raw[:n]
	if start > 0 {
		if newline := bytes.IndexByte(raw, '\n'); newline >= 0 {
			raw = raw[newline+1:]
		} else {
			raw = nil
		}
	}
	rawLines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	var out []string
	for _, ln := range rawLines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		out = append(out, b.sanitizeLogLine(ln))
	}
	if len(out) > maxLines {
		out = out[len(out)-maxLines:]
	}
	return out, nil
}

var runtimeLogSecretField = regexp.MustCompile(`(?i)\b(?:cookie|authorization|sessdata|bili_jct|dedeuserid|dedeuserid__ckmd5|sid|ac_time_value|refresh_token|pending_old_refresh_token|access_key|auth_key|w_rid|upsig|wssecret|wstime|deadline|bwsv|sessionid|sessionid_ss|sid_tt|uid_tt|uid_tt_ss|sid_guard|odin_tt|passport_csrf_token|passport_csrf_token_default|ttwid|uifid|uifid_temp|mstoken|s_v_web_id|__ac_nonce|__ac_signature|a_bogus|x-bogus|x-signature|x-secsdk-web-signature|x-secsdk-csrf-token)["'\\]*\s*[=:]|[?&]e=`)
var runtimeLogURL = regexp.MustCompile(`(?i)https?://[^\s"<>]+`)
var runtimeLogPercentEscape = regexp.MustCompile(`(?i)%[0-9a-f]{2}`)

func (b *Backend) redactStoredSecrets(s string) string {
	if b.credMgr != nil {
		return b.credMgr.RedactString(s)
	}
	if b.cred != nil {
		return b.cred.RedactString(s)
	}
	return s
}

func (b *Backend) sanitizeLogLine(msg string) string {
	sanitized := b.redactStoredSecrets(msg)
	decoded := sanitized
	for i := 0; i < 3; i++ {
		if runtimeLogSecretField.MatchString(decoded) {
			return "[REDACTED_LOG_LINE]"
		}
		for _, rawURL := range runtimeLogURL.FindAllString(decoded, -1) {
			if strings.Contains(rawURL, "?") {
				return "[REDACTED_LOG_LINE]"
			}
		}
		if b.redactStoredSecrets(decoded) != decoded {
			return "[REDACTED_LOG_LINE]"
		}
		next, err := url.QueryUnescape(decoded)
		if err != nil {
			// A progress value such as "50%" must not prevent redaction of an
			// encoded credential elsewhere on the same line.
			next = runtimeLogPercentEscape.ReplaceAllStringFunc(decoded, func(escape string) string {
				value, _ := url.QueryUnescape(escape)
				return value
			})
		}
		if next == decoded {
			break
		}
		decoded = next
	}
	return sanitized
}

// Logf writes an informational line to trace.log.
func (b *Backend) Logf(format string, args ...any) {
	b.logInfo(fmt.Sprintf(format, args...))
}

func (b *Backend) logInfo(msg string) {
	b.writeLogLine("INFO", msg)
}

func (b *Backend) logError(op string, err error) {
	if err == nil {
		return
	}
	b.writeLogLine("ERROR", op+": "+err.Error())
}

func (b *Backend) writeLogLine(level, msg string) {
	if b.logPath == "" {
		return
	}
	sanitized := b.sanitizeLogLine(msg)
	line := fmt.Sprintf("%s [%s] %s\n", time.Now().UTC().Format(time.RFC3339), level, sanitized)

	b.logMu.Lock()
	defer b.logMu.Unlock()
	f, err := os.OpenFile(b.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	_, _ = f.WriteString(line)
	_ = f.Close()
}

// FetchImage fetches a whitelisted Bilibili or Douyin cover/avatar/note image without sending Cookie.
func (b *Backend) FetchImage(ctx context.Context, rawURL string) ([]byte, error) {
	if err := b.requireAnyLogin(); err != nil {
		return nil, err
	}
	imgSite := b.detectImageSite(rawURL)
	if err := b.requireSiteLogin(imgSite); err != nil {
		return nil, err
	}
	if imgSite == site.Douyin {
		data, _, err := b.douyin.FetchImage(ctx, rawURL)
		return data, err
	}
	return b.bili.FetchImage(ctx, rawURL)
}

func (b *Backend) RemoveHistory(kind, id string) error {
	if err := b.requireAnyLogin(); err != nil {
		return err
	}
	return b.history.Remove(kind, id)
}

func (b *Backend) CheckSubscription(ctx context.Context, subID string) (subscription.CheckSummary, error) {
	if err := b.requireAnyLogin(); err != nil {
		return subscription.CheckSummary{}, err
	}
	return b.subs.CheckSubscription(ctx, subID)
}

func (b *Backend) CancelSubscriptionCheck() { b.subs.CancelCheck() }

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
