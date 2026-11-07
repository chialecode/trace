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
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/chialecode/trace/internal/uistate"
)

// Options configures the TRACE Backend service.
type Options struct {
	Locations        appdirs.Locations
	BilibiliOpts     bilibili.ClientOptions
	Desktop          Desktop
	DisableBgWorkers bool
}

// OpenLinkResult contains the resolved target and fetched detail payload for UI navigation (C14).
type OpenLinkResult struct {
	Target            bilibili.ResolveTarget     `json:"target"`
	Video             *bilibili.VideoDetail      `json:"video,omitempty"`
	User              *bilibili.UserSpace        `json:"user,omitempty"`
	UserVideos        *bilibili.UserVideosPage   `json:"userVideos,omitempty"`
	UserDynamicsError string                     `json:"userDynamicsError,omitempty"`
	UserVideosError   string                     `json:"userVideosError,omitempty"`
	UserDynamics      *bilibili.UserDynamicsPage `json:"userDynamics,omitempty"`
	Dynamic           *bilibili.DynamicItem      `json:"dynamic,omitempty"`
	LiveRoom          *live.RoomState            `json:"liveRoom,omitempty"`
	CachedItem        *library.ItemMetadata      `json:"cachedItem,omitempty"`
	// HistoryThumbs names the images of the recent-parse entry. OpenLink
	// records the entry without waiting for them; FetchHistoryThumbs adds
	// them afterwards, so opening a link waits only for its metadata.
	HistoryThumbs *HistoryThumbJob `json:"-"`
}

// HistoryThumbJob is the deferred thumbnail work of one recent-parse entry.
type HistoryThumbJob struct {
	Kind      string
	ID        string
	AvatarURL string
	CoverURL  string
}

// Diagnostics provides a sanitized runtime status snapshot for the Settings/Diagnostics view and smoke tests (C37).
type Diagnostics struct {
	AppName                 string            `json:"appName"`
	DisplayName             string            `json:"displayName"`
	Channel                 string            `json:"channel"`
	Mode                    string            `json:"mode"`
	DataDir                 string            `json:"dataDir"`
	CacheDir                string            `json:"cacheDir"`
	LogsDir                 string            `json:"logsDir"`
	Account                 credential.Status `json:"account"`
	LibraryCount            int               `json:"libraryCount"`
	TaskCount               int               `json:"taskCount"`
	SubscriptionCnt         int               `json:"subscriptionCount"`
	LiveRoomCount           int               `json:"liveRoomCount"`
	IndexStatus             string            `json:"indexStatus,omitempty"`
	SubscriptionCheckStatus string            `json:"subscriptionCheckStatus,omitempty"`
	AutoCacheStatus         string            `json:"autoCacheStatus,omitempty"`
	LiveRecordStatus        string            `json:"liveRecordStatus,omitempty"`
	CorruptRecovered        []string          `json:"corruptRecovered,omitempty"`
	RecentEvents            []Event           `json:"recentEvents,omitempty"`
}

// AuthService defines authentication and renewal operations (architecture.md §3).
type AuthService interface {
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

// ParseService defines link resolution and creator/video browsing operations (architecture.md §3).
type ParseService interface {
	OpenLink(ctx context.Context, rawInput string) (*OpenLinkResult, error)
	FetchHistoryThumbs(ctx context.Context, job HistoryThumbJob) error
	FetchImage(ctx context.Context, rawURL string) ([]byte, error)
	UserDynamicsPage(ctx context.Context, mid int64, offset string) (bilibili.UserDynamicsPage, error)
	UserVideosPage(ctx context.Context, mid int64, page int) (bilibili.UserVideosPage, error)
}

// HistoryService defines recent parse history operations (architecture.md §3).
type HistoryService interface {
	ListHistory() []storage.HistoryEntry
	ClearHistory() error
	RemoveHistory(kind, id string) error
	HistoryThumbPath(name string) (string, error)
}

// CacheService defines download queue, supplement, and quality upgrade operations (architecture.md §3).
type CacheService interface {
	EnqueueVideoCache(ctx context.Context, req download.VideoEnqueueRequest) ([]download.Task, error)
	EnqueueDynamicCache(ctx context.Context, req download.DynamicEnqueueRequest) (download.Task, error)
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

// FeedService defines home feed pagination and anchor persistence operations (architecture.md §3).
type FeedService interface {
	QueryFeed(filter library.FeedFilter) ([]library.FeedEntry, error)
	FeedPage(req library.FeedPageRequest) (library.FeedPageResult, error)
	Anchor() uistate.FeedAnchor
	SaveAnchor(anchor uistate.FeedAnchor) error
	NewerCount(anchor uistate.FeedAnchor) (int, error)
}

// LibraryService defines local media library, safe deletion, and whitelist path operations (architecture.md §3).
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

// SettingsService defines settings, UI state, and directory management operations (architecture.md §3).
type SettingsService interface {
	Locations() appdirs.Locations
	GetSettings() settings.Settings
	UpdateSettings(next settings.Settings) (settings.Settings, error)
	GetUIState() uistate.State
	UpdateUIState(next uistate.State) (uistate.State, error)
	OpenDataFolder(target string) error
}

// SubscriptionService defines multi-account subscription CRUD and binding operations (architecture.md §3).
type SubscriptionService interface {
	ListSubscriptions() ([]subscription.Subscription, error)
	UpsertSubscription(sub subscription.Subscription) (subscription.Subscription, error)
	CreateSubscription(name, note string, accounts []subscription.AccountRef, rules subscription.SubRules) (subscription.Subscription, error)
	LinkSubscriptionAccount(subID, site, uid, name, face string) (subscription.Subscription, error)
	UnlinkSubscriptionAccount(subID, site, uid string) (subscription.Subscription, error)
	MoveSubscriptionAccount(site, uid, toSubID string) (subscription.Subscription, error)
	SetSubscriptionRules(subID string, rules subscription.SubRules) (subscription.Subscription, error)
	// Shared cache rules (DEC-040): subscriptions choose one by ID.
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

// UpdateService defines subscription update checking and listing operations (architecture.md §3).
type UpdateService interface {
	ListSubscriptionUpdatesPage(subID string, limit, offset int) ([]subscription.UpdateItem, error)
	ListSubscriptionUpdates(subID string) ([]subscription.UpdateItem, error)
	CheckSubscriptionsNow(ctx context.Context, targetMid int64) (subscription.CheckSummary, error)
	CheckSubscription(ctx context.Context, subID string) (subscription.CheckSummary, error)
	CancelSubscriptionCheck()
}

// FollowingService defines read-only following group and creator import operations (architecture.md §3).
type FollowingService interface {
	RelationTags(ctx context.Context) ([]bilibili.RelationTag, error)
	RelationFollowings(ctx context.Context, tagID int64, pn, ps int) ([]bilibili.FollowingUser, error)
	ImportCreators(users []bilibili.FollowingUser, scope, autoCache string, liveNotify, liveAutoRecord bool) (int, error)
}

// LiveService defines live status polling and fMP4 recording operations (architecture.md §3).
type LiveService interface {
	ListLiveRooms() []live.RoomState
	LiveStatus(ctx context.Context, uids []int64) ([]live.RoomState, error)
	PollLiveNow(ctx context.Context) ([]live.RoomState, error)
	OpenLiveRoomInBrowser(roomID int64) error
	StartLiveRecording(ctx context.Context, roomID int64) (live.RoomState, error)
	StopLiveRecording(roomID int64) (live.RoomState, error)
}

// AppService defines application lifecycle, event subscription, and diagnostics operations (architecture.md §3).
type AppService interface {
	Close() error
	SubscribeEvents(fn func(Event)) func()
	Desktop() Desktop
	DiagnosticsSnapshot() Diagnostics
	RecentLogs(maxLines int) ([]string, error)
	PauseAllActiveWork()
}

// BackendAPI composes all TRACE domain service interfaces for the UI layer (architecture.md §3, INV-05).
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

// Backend coordinates all TRACE domain packages without importing any UI framework (INV-05).
type Backend struct {
	loc          appdirs.Locations
	db           *sql.DB
	updatesDB    *sql.DB
	settings     *settings.Store
	uiState      *uistate.Store
	history      *storage.HistoryStore
	cred         *credential.Store
	bili         *bilibili.Client
	lib          *library.Store
	dl           *download.Manager
	subs         *subscription.Manager
	live         *live.Manager
	desktop      Desktop
	events       *eventBus
	bgCtx        context.Context
	bgCancel     context.CancelFunc
	renewalWake  chan struct{}
	bgWg         sync.WaitGroup
	logMu        sync.Mutex
	settingsMu   sync.Mutex
	logPath      string
	corruptFiles []string
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

	settingsPath := filepath.Join(loc.DataDir, "settings.json")
	setStore, setRec, setBackup, err := settings.Open(settingsPath, loc.DefaultCacheDir)
	if err != nil {
		return nil, err
	}
	if setRec && setBackup != "" {
		corruptBackups = append(corruptBackups, setBackup)
	}
	curSettings := setStore.Get()
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

	credPath := filepath.Join(loc.CredentialsDir, "bilibili.bin")
	legacyCredPath := filepath.Join(loc.CredentialsDir, "bilibili.dpapi")
	if _, err := os.Stat(credPath); os.IsNotExist(err) {
		if rawLegacy, lErr := os.ReadFile(legacyCredPath); lErr == nil {
			_ = storage.WriteFileAtomic(credPath, rawLegacy, 0o600)
		}
	}
	credStore, credBackup, err := credential.Open(credPath)
	if err != nil {
		return nil, err
	}
	if credBackup != "" {
		corruptBackups = append(corruptBackups, credBackup)
	}

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

	updatesDBPath := filepath.Join(loc.DataDir, "updates.db")
	updatesDB, updatesRec, updatesBackup, err := storage.OpenSQLite(updatesDBPath)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if updatesRec && updatesBackup != "" {
		corruptBackups = append(corruptBackups, updatesBackup)
	}

	biliOpts := opts.BilibiliOpts
	biliOpts.CredStore = credStore
	biliClient := bilibili.NewClient(biliOpts)

	libStore, err := library.OpenWithDataDir(db, curSettings.CacheDir, loc.DataDir)
	if err != nil {
		_ = updatesDB.Close()
		_ = db.Close()
		return nil, err
	}

	// If SQLite was recreated or did not exist on disk before startup, scan CacheDir to rebuild index from item.json files (C28).
	if dbRec || dbWasMissing {
		if rep, rErr := libStore.RebuildIndex(); rErr == nil && len(rep.CorruptBackedUp) > 0 {
			corruptBackups = append(corruptBackups, rep.CorruptBackedUp...)
		}
	}

	desk := opts.Desktop
	if desk == nil {
		desk = NewDefaultDesktop()
	}

	evBus := newEventBus()
	bgCtx, bgCancel := context.WithCancel(context.Background())

	b := &Backend{
		loc:          loc,
		db:           db,
		updatesDB:    updatesDB,
		settings:     setStore,
		uiState:      uiStore,
		history:      histStore,
		cred:         credStore,
		bili:         biliClient,
		lib:          libStore,
		desktop:      desk,
		events:       evBus,
		bgCtx:        bgCtx,
		bgCancel:     bgCancel,
		renewalWake:  make(chan struct{}, 1),
		logPath:      filepath.Join(loc.LogsDir, "trace.log"),
		corruptFiles: corruptBackups,
	}

	dlMgr, err := download.NewManager(db, libStore, biliClient, curSettings.Download, func(t download.Task) {
		b.logInfo(fmt.Sprintf("download task %s (%s) -> %s", t.TaskID, t.TargetID, t.Status))
		message := t.TaskID + ":" + t.Status
		if t.ErrorKind == "storage" {
			message += ":storage"
		}
		evBus.Publish(EventTaskUpdated, message)
		if t.Status == "completed" || t.Status == "partial" {
			evBus.Publish(EventLibraryChanged, t.ItemKey)
		}
	})
	if err != nil {
		_ = updatesDB.Close()
		_ = db.Close()
		return nil, err
	}
	b.dl = dlMgr

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

	liveMgr := live.NewManager(libStore, subMgr, biliClient, curSettings.Live, func(n live.Notification) {
		b.logInfo(fmt.Sprintf("live started: room=%d uid=%d uname=%s", n.RoomID, n.UID, n.Uname))
		evBus.Publish(EventLiveUpdated, fmt.Sprintf("live:%d", n.RoomID))
	}, func(st live.RoomState) {
		evBus.Publish(EventLiveUpdated, fmt.Sprintf("room:%d:%d", st.RoomID, st.LiveStatus))
	})
	b.live = liveMgr
	biliClient.SetAccountReplacingHandler(func() {
		dlMgr.PauseAll("auth_required", "登录账号已变更，任务已暂停")
		subMgr.CancelCheck()
		liveMgr.CancelRecordings("auth_required")
	})
	biliClient.SetAutomaticRequestHandlers(subMgr.AutomaticRequestAllowed, subMgr.RecordAutomaticStop)
	subMgr.SetLiveCheck(func(ctx context.Context, uids []int64) error {
		_, err := liveMgr.RefreshForAccounts(ctx, uids)
		return err
	})
	biliClient.SetAuthExpiredHandler(func() {
		dlMgr.PauseAll("auth_required", "登录已失效，任务已暂停")
		subMgr.CancelCheck()
		liveMgr.CancelRecordings("auth_required")
		evBus.Publish(EventAccountChanged, "expired")
	})

	b.logInfo(fmt.Sprintf("backend initialized: app=%s mode=%s channel=%s", loc.AppName, loc.Mode, loc.Channel))

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

func (b *Backend) runRenewalLoop(ctx context.Context) {
	defer b.bgWg.Done()
	// Wait 1 minute after startup before the first automatic renewal check (architecture.md §5.4).
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

// Close stops background workers and closes the SQLite database.
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

// AccountStatus returns the desensitized account status (never exposing raw cookies or tokens, C05, C37).
func (b *Backend) AccountStatus() credential.Status {
	return b.cred.Status()
}

// BootstrapAuth verifies existing credentials on startup via /x/web-interface/nav:
// - If logged_out, does nothing.
// - If -101 / not logged in, marks status "expired" and pauses active tasks.
// - If network error, marks status "unknown" while keeping credentials usable (R-19).
func (b *Backend) BootstrapAuth(ctx context.Context) (credential.Status, error) {
	st := b.cred.Status()
	if st.State == "logged_out" || !st.LoggedIn {
		return st, nil
	}
	verified, err := b.bili.VerifyCredential(ctx)
	if err != nil && bilibili.IsKind(err, bilibili.ErrKindAuthRequired) {
		b.PauseAllActiveWork()
	} else if err != nil {
		var stateErr error
		verified, stateErr = b.cred.MarkState("unknown", "无法确认登录状态，请重试")
		err = errors.Join(err, stateErr)
		if stateErr != nil {
			verified = b.cred.Status()
		}
	} else if !verified.LoggedIn {
		b.PauseAllActiveWork()
	}
	b.events.Publish(EventAccountChanged, verified.State)
	return verified, err
}

// VerifyAccount queries Bilibili /x/web-interface/nav to refresh account profile and login state.
func (b *Backend) VerifyAccount(ctx context.Context) (credential.Status, error) {
	st, err := b.bili.VerifyCredential(ctx)
	if err != nil && bilibili.IsKind(err, bilibili.ErrKindAuthRequired) {
		b.PauseAllActiveWork()
	}
	b.events.Publish(EventAccountChanged, st.State)
	return st, err
}

// StartQRLogin starts a new QR code login session and invalidates any previous QR session (C06).
func (b *Backend) StartQRLogin(ctx context.Context) (bilibili.QRSession, error) {
	sess, err := b.bili.StartQR(ctx)
	if err != nil {
		b.logError("StartQRLogin", err)
		return bilibili.QRSession{}, err
	}
	b.logInfo("QR login started: flow=" + sess.FlowID)
	return sess, nil
}

// PollQRLogin polls the status of the active QR login flow (C06).
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
		b.events.Publish(EventAccountChanged, "logged_in")
	}
	return res, nil
}

// CancelQRLogin cancels the QR login flow so late responses never write credentials (C06).
func (b *Backend) CancelQRLogin(flowID string) {
	b.bili.CancelQR(flowID)
	b.logInfo("QR login canceled: flow=" + flowID)
}

// ImportCookie validates a manual Cookie string against /x/web-interface/nav FIRST,
// and only persists the credential if verification succeeds (R-02, C05, C07).
func (b *Backend) ImportCookie(ctx context.Context, rawCookie, rawRefreshToken string) (credential.Status, error) {
	prev := b.cred.Status()
	st, err := b.bili.LoginWithCookie(ctx, rawCookie, rawRefreshToken)
	if err != nil {
		b.logError("ImportCookie", err)
		return b.cred.Status(), err
	}
	if prev.LoggedIn && prev.Mid != 0 && prev.Mid != st.Mid {
		b.PauseAllActiveWork()
	}
	b.logInfo(fmt.Sprintf("manual cookie imported: state=%s mid=%d", st.State, st.Mid))
	b.events.Publish(EventAccountChanged, st.State)
	return st, nil
}

// RefreshCookie runs the 5-step Bilibili Cookie renewal flow (or resumes step 5 if pending, C08).
func (b *Backend) RefreshCookie(ctx context.Context) (credential.Status, error) {
	st, err := b.bili.CheckAndRefreshCookie(ctx)
	if err != nil {
		b.logError("RefreshCookie", err)
	}
	b.events.Publish(EventAccountChanged, st.State)
	return st, err
}

// PauseAllActiveWork pauses all running and queued download tasks and stops active live recordings (R-04, C06, C07).
func (b *Backend) PauseAllActiveWork() {
	if b.dl != nil {
		b.dl.PauseAll("auth_required", "登录状态已变更，任务已暂停")
	}
	if b.live != nil {
		for _, r := range b.live.ListRooms() {
			if r.Recording {
				_, _ = b.live.StopRecording(r.RoomID)
			}
		}
	}
}

// Logout pauses active tasks and live recordings, cancels any active QR login flow, and clears credentials (C05, C06, C07).
func (b *Backend) Logout() (credential.Status, error) {
	b.PauseAllActiveWork()
	err := b.bili.LogoutCredential()
	st := b.cred.Status()
	if err != nil {
		b.logError("Logout", err)
	} else {
		b.logInfo("account logged out and credentials cleared")
	}
	b.events.Publish(EventAccountChanged, st.State)
	return st, err
}

// OpenLink resolves a user input string (URL, share text, BV/av, UID, dynamic ID, live room),
// fetches its detail payload, records sanitized navigation history, and returns OpenLinkResult (C01..C04, C14).
func (b *Backend) OpenLink(ctx context.Context, rawInput string) (*OpenLinkResult, error) {
	if err := b.requireLogin(); err != nil {
		return nil, err
	}
	target, err := b.bili.ResolveLink(ctx, rawInput)
	if err != nil {
		b.logError("OpenLink.Resolve", err)
		return nil, err
	}

	out := &OpenLinkResult{Target: target}

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
			return nil, &bilibili.SiteError{Kind: "storage", Message: "解析记录写入失败", Err: err}
		}
		out.HistoryThumbs = &HistoryThumbJob{Kind: "video", ID: vd.BVID, AvatarURL: vd.Author.Face, CoverURL: vd.Cover}
		if err := b.history.Record(storage.HistoryEntry{
			Site:         "bilibili",
			Kind:         "video",
			ID:           vd.BVID,
			Title:        vd.Title,
			Subtitle:     vd.Author.Name,
			CanonicalURL: vd.CanonicalURL,
			DurationText: formatHistoryDuration(vd.Duration),
		}); err != nil {
			return nil, &bilibili.SiteError{Kind: "storage", Message: "历史记录写入失败", Err: err}
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
		out.HistoryThumbs = &HistoryThumbJob{Kind: "user", ID: userID, AvatarURL: space.Face}
		if err := b.history.Record(storage.HistoryEntry{
			Site:         "bilibili",
			Kind:         "user",
			ID:           userID,
			Title:        space.Name,
			Subtitle:     space.Sign,
			CanonicalURL: space.CanonicalURL,
		}); err != nil {
			return nil, &bilibili.SiteError{Kind: "storage", Message: "历史记录写入失败", Err: err}
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
			return nil, &bilibili.SiteError{Kind: "storage", Message: "解析记录写入失败", Err: err}
		}
		out.HistoryThumbs = &HistoryThumbJob{Kind: "dynamic", ID: dyn.ID, AvatarURL: dyn.Author.Face, CoverURL: coverURL}
		if err := b.history.Record(storage.HistoryEntry{
			Site:         "bilibili",
			Kind:         "dynamic",
			ID:           dyn.ID,
			Title:        dyn.Title,
			Subtitle:     dyn.Author.Name,
			CanonicalURL: dyn.CanonicalURL,
		}); err != nil {
			return nil, &bilibili.SiteError{Kind: "storage", Message: "历史记录写入失败", Err: err}
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
		out.HistoryThumbs = &HistoryThumbJob{Kind: "live", ID: liveID, AvatarURL: space.Face, CoverURL: room.Cover}
		if err := b.history.Record(storage.HistoryEntry{
			Site:         "bilibili",
			Kind:         "live",
			ID:           liveID,
			Title:        room.Title,
			Subtitle:     room.Uname,
			CanonicalURL: fmt.Sprintf("https://live.bilibili.com/%d", room.RoomID),
		}); err != nil {
			return nil, &bilibili.SiteError{Kind: "storage", Message: "历史记录写入失败", Err: err}
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
	if err := b.requireLogin(); err != nil {
		return err
	}
	if b.history == nil {
		return nil
	}
	var thumbs []storage.HistoryThumbnail
	for _, want := range []struct{ role, url string }{{"avatar", job.AvatarURL}, {"cover", job.CoverURL}} {
		if t := b.fetchHistoryThumb(ctx, want.role, want.url); len(t.Data) > 0 {
			thumbs = append(thumbs, t)
		}
	}
	if len(thumbs) == 0 {
		return nil
	}
	return b.history.AttachThumbs("bilibili", job.Kind, job.ID, thumbs...)
}

func (b *Backend) fetchHistoryThumb(ctx context.Context, role, rawURL string) storage.HistoryThumbnail {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" || b.history == nil || b.bili == nil {
		return storage.HistoryThumbnail{}
	}
	if strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "http://127.0.0.1") && !strings.HasPrefix(rawURL, "http://localhost") {
		rawURL = "https://" + strings.TrimPrefix(rawURL, "http://")
	}
	raw, err := b.bili.FetchImageLimited(ctx, rawURL, storage.MaxHistoryThumbBytes)
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

// HistoryThumbPath resolves a local cached parse-history thumbnail inside <DataDir>/thumbs/ (R11).
func (b *Backend) HistoryThumbPath(name string) (string, error) {
	if err := b.requireLogin(); err != nil {
		return "", err
	}
	if b.history == nil {
		return "", fmt.Errorf("service: history store unavailable")
	}
	return b.history.ThumbPath(name)
}

// ListHistory returns the recent sanitized navigation history entries (C03).
func (b *Backend) ListHistory() []storage.HistoryEntry {
	if b.requireLogin() != nil {
		return nil
	}
	return b.history.List()
}

// ClearHistory clears the navigation history.
func (b *Backend) ClearHistory() error {
	if err := b.requireLogin(); err != nil {
		return err
	}
	return b.history.Clear()
}

// EnqueueVideoCache enqueues video parts for local caching (C15).
func (b *Backend) EnqueueVideoCache(ctx context.Context, req download.VideoEnqueueRequest) ([]download.Task, error) {
	if err := b.requireLogin(); err != nil {
		return nil, err
	}
	tasks, err := b.dl.EnqueueVideo(ctx, req)
	if err != nil {
		b.logError("EnqueueVideoCache", err)
		return nil, err
	}
	b.events.Publish(EventTaskUpdated, "enqueued:video:"+req.BVID)
	return tasks, nil
}

// EnqueueDynamicCache enqueues a dynamic post for local caching (C19).
func (b *Backend) EnqueueDynamicCache(ctx context.Context, req download.DynamicEnqueueRequest) (download.Task, error) {
	if err := b.requireLogin(); err != nil {
		return download.Task{}, err
	}
	task, err := b.dl.EnqueueDynamic(ctx, req)
	if err != nil {
		b.logError("EnqueueDynamicCache", err)
		return download.Task{}, err
	}
	b.events.Publish(EventTaskUpdated, "enqueued:dynamic:"+req.DynamicID)
	return task, nil
}

// CacheSupplement re-downloads missing danmaku/subtitle/cover sidecars for an already cached video without re-downloading video/audio.
func (b *Backend) CacheSupplement(ctx context.Context, bvid string) (download.Task, error) {
	if err := b.requireLogin(); err != nil {
		return download.Task{}, err
	}
	task, err := b.dl.CacheSupplement(ctx, bvid)
	if err != nil {
		b.logError("CacheSupplement", err)
		return download.Task{}, err
	}
	b.events.Publish(EventTaskUpdated, "enqueued:supplement:"+bvid)
	return task, nil
}

// Upgrade enqueues a quality upgrade download task for a cached video item (C20).
func (b *Backend) Upgrade(ctx context.Context, key string) (download.Task, error) {
	if err := b.requireLogin(); err != nil {
		return download.Task{}, err
	}
	task, err := b.dl.Upgrade(ctx, key)
	if err != nil {
		b.logError("Upgrade", err)
		return download.Task{}, err
	}
	b.events.Publish(EventTaskUpdated, "enqueued:upgrade:"+key)
	return task, nil
}

// ListDownloadTasks returns all download tasks.
func (b *Backend) ListDownloadTasks() ([]download.Task, error) {
	if err := b.requireLogin(); err != nil {
		return nil, err
	}
	return b.dl.ListTasks()
}

// TaskDetail returns per-file progress and sanitized attempt history for a download task.
func (b *Backend) TaskDetail(taskID string) (download.TaskDetail, bool, error) {
	if err := b.requireLogin(); err != nil {
		return download.TaskDetail{}, false, err
	}
	return b.dl.TaskDetail(taskID)
}

// PauseTask pauses a download task.
func (b *Backend) PauseTask(taskID string) (download.Task, error) {
	if err := b.requireLogin(); err != nil {
		return download.Task{}, err
	}
	return b.dl.Pause(taskID)
}

// ResumeTask resumes a paused download task.
func (b *Backend) ResumeTask(taskID string) (download.Task, error) {
	if err := b.requireLogin(); err != nil {
		return download.Task{}, err
	}
	return b.dl.Resume(taskID)
}

// RetryTask retries a failed or partial download task.
func (b *Backend) RetryTask(taskID string) (download.Task, error) {
	if err := b.requireLogin(); err != nil {
		return download.Task{}, err
	}
	return b.dl.Retry(taskID)
}

// CancelTask cancels a download task and removes incomplete .part files.
func (b *Backend) CancelTask(taskID string) (download.Task, error) {
	if err := b.requireLogin(); err != nil {
		return download.Task{}, err
	}
	return b.dl.Cancel(taskID)
}

// RemoveTask deletes a finished task record; cached files stay in the library.
func (b *Backend) RemoveTask(taskID string) error {
	if err := b.requireLogin(); err != nil {
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
	if err := b.requireLogin(); err != nil {
		return 0, err
	}
	n, err := b.dl.RemoveFinished()
	if n > 0 {
		b.events.Publish(EventTaskUpdated, "removed:finished")
	}
	return n, err
}

// QueryFeed queries the unified feed (cached + online-discovered items) ordered by pub_ts DESC (C24).
func (b *Backend) QueryFeed(filter library.FeedFilter) ([]library.FeedEntry, error) {
	if err := b.requireLogin(); err != nil {
		return nil, err
	}
	return b.lib.QueryFeed(filter)
}

// FeedPage queries a keyset-paginated page of the home feed (C21, C22).
func (b *Backend) FeedPage(req library.FeedPageRequest) (library.FeedPageResult, error) {
	if err := b.requireLogin(); err != nil {
		return library.FeedPageResult{}, err
	}
	return b.lib.FeedPage(req)
}

// Anchor returns the persisted home feed reading anchor from ui-state.json (C21).
func (b *Backend) Anchor() uistate.FeedAnchor {
	if b.requireLogin() != nil {
		return uistate.FeedAnchor{}
	}
	return b.uiState.Get().Anchor
}

// SaveAnchor persists the home feed reading anchor into ui-state.json (C21).
func (b *Backend) SaveAnchor(anchor uistate.FeedAnchor) error {
	if err := b.requireLogin(); err != nil {
		return err
	}
	st := b.uiState.Get()
	st.Anchor = anchor
	_, err := b.uiState.Update(st)
	return err
}

// NewerCount returns the number of feed entries strictly newer than anchor (C21).
func (b *Backend) NewerCount(anchor uistate.FeedAnchor) (int, error) {
	if err := b.requireLogin(); err != nil {
		return 0, err
	}
	return b.lib.NewerCachedCount(anchor.PubTs, anchor.Key)
}

// ListLibraryItems returns locally cached items matching filter.
func (b *Backend) ListLibraryItems(filter library.FeedFilter) ([]library.ItemMetadata, error) {
	if err := b.requireLogin(); err != nil {
		return nil, err
	}
	return b.lib.ListItems(filter)
}

// GetLibraryItem returns a single cached item by itemKey or rawID.
func (b *Backend) GetLibraryItem(keyOrID string) (library.ItemMetadata, bool, error) {
	if err := b.requireLogin(); err != nil {
		return library.ItemMetadata{}, false, err
	}
	return b.lib.GetItem(keyOrID)
}

// MediaPath validates and resolves a registered relative file path within a cached item (INV-08, R-08).
func (b *Backend) MediaPath(key, relPath string) (string, error) {
	if err := b.requireLogin(); err != nil {
		return "", err
	}
	return b.lib.MediaPath(key, relPath)
}

// States computes the canonical cache status dots for the given item keys (FR-CACHE-004, INV-06).
func (b *Backend) States(keys []string) map[string]library.DotState {
	if b.requireLogin() != nil {
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
				if _, exists := latest[key]; !exists {
					latest[key] = t
				}
			}
			if t.Status == "queued" || t.Status == "downloading" || t.Status == "muxing" || t.Status == "running" {
				prog := t.Progress
				if prog == 0 && t.TotalBytes > 0 {
					prog = float64(t.BytesDownloaded) / float64(t.TotalBytes)
				}
				activeProgress[t.ItemKey] = prog
				activeProgress[t.TargetID] = prog
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

// PlanDelete computes the safe file deletion plan for an item key (INV-09, R-01).
func (b *Backend) PlanDelete(key string) (library.DeletePlan, error) {
	if err := b.requireLogin(); err != nil {
		return library.DeletePlan{}, err
	}
	return b.lib.PlanDelete(key)
}

// Delete executes a safe manifest-only file deletion for an item key (INV-09, R-01).
func (b *Backend) Delete(key string) error {
	return b.DeleteLibraryItem(key, true)
}

// RebuildLibraryIndex scans CacheDir for item.json files and rebuilds the SQLite index (C24, C25, C28).
func (b *Backend) RebuildLibraryIndex() (library.RebuildReport, error) {
	if err := b.requireLogin(); err != nil {
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

// AddLibraryRoot adds an existing cache root or legacy trace-data directory and indexes its manifests (C25, R-12).
func (b *Backend) AddLibraryRoot(selectedDir string) (library.AddRootReport, error) {
	if err := b.requireLogin(); err != nil {
		return library.AddRootReport{}, err
	}
	rep, err := b.lib.AddLibraryRoot(selectedDir)
	if err == nil {
		b.events.Publish(EventLibraryChanged, "root_added")
	}
	return rep, err
}

// DeleteLibraryItem removes an item from the index and optionally deletes its registered files from disk (C26, R-01).
func (b *Backend) DeleteLibraryItem(itemKey string, deleteFiles bool) error {
	if err := b.requireLogin(); err != nil {
		return err
	}
	if err := b.lib.DeleteItem(itemKey, deleteFiles); err != nil {
		return err
	}
	b.events.Publish(EventLibraryChanged, "deleted:"+itemKey)
	return nil
}

// MigrateCacheDir changes the active cache directory, optionally moves existing files, and updates settings.json (C26).
func (b *Backend) MigrateCacheDir(newDir string, moveExisting bool) (library.RebuildReport, error) {
	if err := b.requireLogin(); err != nil {
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

// UpgradeCandidates returns up to limit cached video items ordered by oldest UpgradeCheckedAt (C20).
func (b *Backend) UpgradeCandidates(limit int) ([]library.ItemMetadata, error) {
	if err := b.requireLogin(); err != nil {
		return nil, err
	}
	return b.lib.UpgradeCandidates(limit)
}

// CheckUpgradeKeys checks the specified video keys (or up to 50 candidates when empty) for quality upgrades (C20).
func (b *Backend) CheckUpgradeKeys(ctx context.Context, keys []string, force bool) ([]library.ItemMetadata, error) {
	if err := b.requireLogin(); err != nil {
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
	b.events.Publish(EventLibraryChanged, "upgrade_checked")
	return items, nil
}

// CheckQualityUpgrade checks cached videos against Bilibili PlayURL for higher video/audio quality (C20).
func (b *Backend) CheckQualityUpgrade(ctx context.Context) ([]library.ItemMetadata, error) {
	return b.CheckUpgradeKeys(ctx, nil, true)
}

// ListSubscriptions returns all creator subscriptions.
func (b *Backend) ListSubscriptions() ([]subscription.Subscription, error) {
	if err := b.requireLogin(); err != nil {
		return nil, err
	}
	return b.subs.List()
}

// UpsertSubscription creates or updates a creator subscription (C29).
func (b *Backend) UpsertSubscription(sub subscription.Subscription) (subscription.Subscription, error) {
	if err := b.requireLogin(); err != nil {
		return subscription.Subscription{}, err
	}
	res, err := b.subs.Upsert(sub)
	if err == nil {
		b.events.Publish(EventSubscriptionChecked, "upserted")
	}
	return res, err
}

// CreateSubscription creates a new multi-account subscription (C27, R-09).
func (b *Backend) CreateSubscription(name, note string, accounts []subscription.AccountRef, rules subscription.SubRules) (subscription.Subscription, error) {
	if err := b.requireLogin(); err != nil {
		return subscription.Subscription{}, err
	}
	res, err := b.subs.Create(name, note, accounts, rules)
	if err == nil {
		b.events.Publish(EventSubscriptionChecked, "created")
	}
	return res, err
}

// LinkSubscriptionAccount links an account to an existing subscription (C27, R-09).
func (b *Backend) LinkSubscriptionAccount(subID, site, uid, name, face string) (subscription.Subscription, error) {
	if err := b.requireLogin(); err != nil {
		return subscription.Subscription{}, err
	}
	res, err := b.subs.Link(subID, site, uid, name, face)
	if err == nil {
		b.events.Publish(EventSubscriptionChecked, "linked")
	}
	return res, err
}

// UnlinkSubscriptionAccount removes an account from a subscription (C27, R-09).
func (b *Backend) UnlinkSubscriptionAccount(subID, site, uid string) (subscription.Subscription, error) {
	if err := b.requireLogin(); err != nil {
		return subscription.Subscription{}, err
	}
	res, err := b.subs.Unlink(subID, site, uid)
	if err == nil {
		b.events.Publish(EventSubscriptionChecked, "unlinked")
	}
	return res, err
}

// MoveSubscriptionAccount atomically moves an account from its current subscription to toSubID (C27, R-09).
func (b *Backend) MoveSubscriptionAccount(site, uid, toSubID string) (subscription.Subscription, error) {
	if err := b.requireLogin(); err != nil {
		return subscription.Subscription{}, err
	}
	res, err := b.subs.Move(site, uid, toSubID)
	if err == nil {
		b.events.Publish(EventSubscriptionChecked, "moved")
	}
	return res, err
}

// SetSubscriptionRules updates per-subscription rules and manages AutoCache.Since (C27, R-09).
func (b *Backend) SetSubscriptionRules(subID string, rules subscription.SubRules) (subscription.Subscription, error) {
	if err := b.requireLogin(); err != nil {
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

// SaveCacheRule creates or edits a shared rule and applies it to every
// subscription that uses it.
func (b *Backend) SaveCacheRule(rule subscription.CacheRule) (subscription.CacheRule, error) {
	if err := b.requireLogin(); err != nil {
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
	if err := b.requireLogin(); err != nil {
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
	if err := b.requireLogin(); err != nil {
		return subscription.Subscription{}, err
	}
	res, err := b.subs.SetSubscriptionRule(subID, ruleID)
	if err == nil {
		b.events.Publish(EventSubscriptionChecked, "rules_updated")
	}
	return res, err
}

// PreviewAccount resolves an account input and checks whether it is already owned by a subscription (R-09).
func (b *Backend) PreviewAccount(ctx context.Context, input string) (subscription.AccountRef, *subscription.Subscription, error) {
	if err := b.requireLogin(); err != nil {
		return subscription.AccountRef{}, nil, err
	}
	target, err := b.bili.ResolveLink(ctx, input)
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
	mid, uname, face := card.Mid, card.Name, card.Face

	uidStr := fmt.Sprintf("%d", mid)
	acc := subscription.AccountRef{
		Site:    "bilibili",
		UID:     uidStr,
		Name:    uname,
		Face:    face,
		AddedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if owner, owned := b.subs.OwnerOf("bilibili", uidStr); owned {
		return acc, &owner, nil
	}
	return acc, nil, nil
}

// OwnerOfAccount returns the subscription owning (site, uid), if any (R-09).
func (b *Backend) OwnerOfAccount(site, uid string) (subscription.Subscription, bool) {
	if b.requireLogin() != nil {
		return subscription.Subscription{}, false
	}
	return b.subs.OwnerOf(site, uid)
}

// ListSubscriptionUpdates returns discovered update items for a subscription (C29, R-10).
func (b *Backend) ListSubscriptionUpdates(subID string) ([]subscription.UpdateItem, error) {
	if err := b.requireLogin(); err != nil {
		return nil, err
	}
	return b.subs.ListUpdates(subID)
}

// DeleteSubscription removes a creator subscription.
func (b *Backend) DeleteSubscription(subIDOrMid string) error {
	if err := b.requireLogin(); err != nil {
		return err
	}
	err := b.subs.Delete(subIDOrMid)
	if err == nil {
		b.events.Publish(EventSubscriptionChecked, "deleted")
	}
	return err
}

// CheckSubscriptionsNow triggers an immediate incremental check for targetMid (or 0 for all enabled, C31).
func (b *Backend) CheckSubscriptionsNow(ctx context.Context, targetMid int64) (subscription.CheckSummary, error) {
	if err := b.requireLogin(); err != nil {
		return subscription.CheckSummary{}, err
	}
	return b.subs.CheckNow(ctx, targetMid)
}

// ExportOPML writes all subscriptions as an OPML 2.0 XML file to path (C33).
func (b *Backend) ExportOPML(path string) error {
	if err := b.requireLogin(); err != nil {
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

// ImportOPML imports creator subscriptions from an OPML 2.0 XML file at path (C33).
func (b *Backend) ImportOPML(path string) (int, error) {
	if err := b.requireLogin(); err != nil {
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

// RelationTags returns the logged-in user's Bilibili following groups (C30).
func (b *Backend) RelationTags(ctx context.Context) ([]bilibili.RelationTag, error) {
	if err := b.requireLogin(); err != nil {
		return nil, err
	}
	return b.bili.RelationTags(ctx)
}

// RelationFollowings returns a page of Bilibili followings (either for a specific tagID > 0 or all followings when tagID == 0, C30).
func (b *Backend) RelationFollowings(ctx context.Context, tagID int64, pn, ps int) ([]bilibili.FollowingUser, error) {
	if err := b.requireLogin(); err != nil {
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

// ImportCreators imports selected creators into subscriptions idempotently (C29, C30).
func (b *Backend) ImportCreators(users []bilibili.FollowingUser, scope, autoCache string, liveNotify, liveAutoRecord bool) (int, error) {
	if err := b.requireLogin(); err != nil {
		return 0, err
	}
	n, err := b.subs.ImportCreators(users, scope, autoCache, liveNotify, liveAutoRecord)
	if err == nil {
		b.events.Publish(EventSubscriptionChecked, "imported")
	}
	return n, err
}

// ListLiveRooms returns the current live status and recording state of all rooms.
func (b *Backend) ListLiveRooms() []live.RoomState {
	if b.requireLogin() != nil {
		return nil
	}
	return b.live.ListRooms()
}

// PollLiveNow triggers an immediate live status batch poll (C34).
func (b *Backend) PollLiveNow(ctx context.Context) ([]live.RoomState, error) {
	if err := b.requireLogin(); err != nil {
		return nil, err
	}
	return b.live.PollNow(ctx)
}

// OpenLiveRoomInBrowser opens a Bilibili live room in the external browser via Desktop (§6.4).
func (b *Backend) OpenLiveRoomInBrowser(roomID int64) error {
	if err := b.requireLogin(); err != nil {
		return err
	}
	if roomID <= 0 {
		return fmt.Errorf("service: invalid roomID %d", roomID)
	}
	return b.desktop.OpenExternalURL(fmt.Sprintf("https://live.bilibili.com/%d", roomID))
}

// StartLiveRecording starts recording a live room (C34).
func (b *Backend) StartLiveRecording(ctx context.Context, roomID int64) (live.RoomState, error) {
	if err := b.requireLogin(); err != nil {
		return live.RoomState{}, err
	}
	return b.live.StartRecording(ctx, roomID)
}

// StopLiveRecording stops an active live room recording and finalizes item.json (C34).
func (b *Backend) StopLiveRecording(roomID int64) (live.RoomState, error) {
	if err := b.requireLogin(); err != nil {
		return live.RoomState{}, err
	}
	return b.live.StopRecording(roomID)
}

// OpenItemFolder opens or reveals a cached item's folder on disk via Desktop (§6.4).
func (b *Backend) OpenItemFolder(itemKey string) error {
	if err := b.requireLogin(); err != nil {
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

// OpenDataFolder opens the DataDir or CacheDir via Desktop (§6.4).
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

// DiagnosticsSnapshot returns a sanitized runtime summary without any secret values (C37).
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

	return Diagnostics{
		AppName:                 b.loc.AppName,
		DisplayName:             b.loc.DisplayName,
		Channel:                 b.loc.Channel,
		Mode:                    b.loc.Mode,
		DataDir:                 b.loc.DataDir,
		CacheDir:                b.lib.CacheDir(),
		LogsDir:                 b.loc.LogsDir,
		Account:                 b.cred.Status(),
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

var runtimeLogSecretField = regexp.MustCompile(`(?i)\b(?:cookie|authorization|sessdata|bili_jct|dedeuserid|dedeuserid__ckmd5|sid|ac_time_value|refresh_token|pending_old_refresh_token|access_key|auth_key|w_rid|upsig|wssecret|wstime|deadline|bwsv)["'\\]*\s*[=:]|[?&]e=`)
var runtimeLogURL = regexp.MustCompile(`(?i)https?://[^\s"<>]+`)
var runtimeLogPercentEscape = regexp.MustCompile(`(?i)%[0-9a-f]{2}`)

func (b *Backend) sanitizeLogLine(msg string) string {
	sanitized := msg
	if b.cred != nil {
		sanitized = b.cred.RedactString(sanitized)
	}
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
		if b.cred != nil && b.cred.RedactString(decoded) != decoded {
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

func (b *Backend) FetchImage(ctx context.Context, rawURL string) ([]byte, error) {
	if err := b.requireLogin(); err != nil {
		return nil, err
	}
	return b.bili.FetchImage(ctx, rawURL)
}
func (b *Backend) RemoveHistory(kind, id string) error {
	if err := b.requireLogin(); err != nil {
		return err
	}
	return b.history.Remove(kind, id)
}

func (b *Backend) CheckSubscription(ctx context.Context, subID string) (subscription.CheckSummary, error) {
	if err := b.requireLogin(); err != nil {
		return subscription.CheckSummary{}, err
	}
	return b.subs.CheckSubscription(ctx, subID)
}
func (b *Backend) CancelSubscriptionCheck() { b.subs.CancelCheck() }
