package service

import (
	"context"
	"io"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/site/weibo"
	"github.com/chialecode/trace/internal/site/x"
	"github.com/chialecode/trace/internal/storage"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/chialecode/trace/internal/uistate"
	"github.com/chialecode/trace/internal/update"
)

// Options configures the TRACE Backend service.
type Options struct {
	Locations                   appdirs.Locations
	BilibiliOpts                bilibili.ClientOptions
	DouyinOpts                  douyin.ClientOptions
	WeiboOpts                   weibo.ClientOptions
	XOpts                       x.ClientOptions
	FullCacheUseFeatureOriginal bool
	Desktop                     Desktop
	DisableBgWorkers            bool
	CacheQueueClock             CacheQueueClock
	CacheQueueRand              CacheQueueRand
	CacheQueuePacing            *CacheQueuePacing
	InstalledDevVersion         func() string
	// Profile is ProfileDesktop (the default) or ProfileRemote, the headless
	// trace-server (docs/design/remote.md §2, §4).
	Profile Profile
	// NodeName names this installation in item.json's cachedBy; the
	// computer's name when empty.
	NodeName string
	// LogMirror also receives every line written to trace.log (the remote
	// writes them to standard error for journald).
	LogMirror io.Writer
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
	WeiboPost            *WeiboPostView             `json:"weiboPost,omitempty"`
	WeiboVideo           *WeiboVideoView            `json:"weiboVideo,omitempty"`
	WeiboUser            *WeiboUserView             `json:"weiboUser,omitempty"`
	WeiboUserPosts       *WeiboUserPostsPage        `json:"weiboUserPosts,omitempty"`
	WeiboUserPostsError  string                     `json:"weiboUserPostsError,omitempty"`
	XPost                *XPostDTO                  `json:"xPost,omitempty"`
	XUser                *XUserDTO                  `json:"xUser,omitempty"`
	XUserPosts           *XUserPostsPageDTO         `json:"xUserPosts,omitempty"`
	XUserPostsError      string                     `json:"xUserPostsError,omitempty"`
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
	OpenExternalLink(rawURL string) error
	DouyinWork(ctx context.Context, awemeID string) (*DouyinWorkView, error)
	DouyinUser(ctx context.Context, secUID string) (*DouyinUserView, error)
	DouyinUserWorks(ctx context.Context, secUID, cursor string) (*DouyinUserWorksPage, error)
	DouyinLive(ctx context.Context, webRID string) (*DouyinLiveView, error)
	WeiboPost(ctx context.Context, mid string) (*WeiboPostView, error)
	WeiboVideo(ctx context.Context, oid string) (*WeiboVideoView, error)
	WeiboUser(ctx context.Context, uid string) (*WeiboUserView, error)
	WeiboUserPosts(ctx context.Context, uid, cursor string) (*WeiboUserPostsPage, error)
	XPost(ctx context.Context, statusID string) (*XPostDTO, error)
	XUser(ctx context.Context, restID string) (*XUserDTO, error)
	XUserByScreenName(ctx context.Context, screenName string) (*XUserDTO, error)
	XUserPosts(ctx context.Context, restID, cursor string) (*XUserPostsPageDTO, error)
	StagePreviewMedia(ctx context.Context, siteID, rawURL, ext string) (string, error)
	FetchHistoryThumbs(ctx context.Context, job HistoryThumbJob) error
	FetchImage(ctx context.Context, rawURL string) ([]byte, error)
	UserDynamicsPage(ctx context.Context, mid int64, offset string) (bilibili.UserDynamicsPage, error)
	UserVideosPage(ctx context.Context, mid int64, page int, opts ...UserVideosPageOptions) (bilibili.UserVideosPage, error)
}

// HistoryService defines recent parse history operations.
type HistoryService interface {
	ListHistory() []storage.HistoryEntry
	ClearHistory() error
	RemoveHistory(kind, id string) error
	RemoveHistoryForSite(siteID, kind, id string) error
	HistoryThumbPath(name string) (string, error)
}

// CacheService defines download queue, background list cache queue, supplement, and quality upgrade operations.
type CacheService interface {
	EnqueueVideoCache(ctx context.Context, req download.VideoEnqueueRequest) ([]download.Task, error)
	EnqueueDynamicCache(ctx context.Context, req download.DynamicEnqueueRequest) (download.Task, error)
	CacheDouyinWork(ctx context.Context, awemeID string) (download.Task, error)
	CacheDouyinWorkFor(ctx context.Context, awemeID, forUID, forName string) (download.Task, error)
	CacheWeiboPost(ctx context.Context, mid string, via *library.ItemVia) (download.Task, error)
	CacheWeiboVideo(ctx context.Context, oid string) (download.Task, error)
	CacheXPost(ctx context.Context, statusID string, via *library.ItemVia) (download.Task, error)
	ListCacheQueue() []CacheQueueTask
	FindCacheQueueTask(siteID, user, list string) (CacheQueueTask, bool)
	EnqueueCacheQueue(ctx context.Context, req CacheQueueRequest) (CacheQueueTask, error)
	PauseCacheQueue(id string) (CacheQueueTask, error)
	ResumeCacheQueue(id string) (CacheQueueTask, error)
	CancelCacheQueue(id string) (CacheQueueTask, error)
	// RemoveCacheQueue deletes a finished cache queue task from the list;
	// RemoveFinishedCacheQueues deletes all of them. Cached items stay.
	RemoveCacheQueue(id string) error
	RemoveFinishedCacheQueues() (int, error)
	CacheSupplement(ctx context.Context, bvid string) (download.Task, error)
	Upgrade(ctx context.Context, key string) (download.Task, error)
	ListDownloadTasks() ([]download.Task, error)
	TaskDetail(taskID string) (download.TaskDetail, bool, error)
	PauseTask(taskID string) (download.Task, error)
	ResumeTask(taskID string) (download.Task, error)
	RetryTask(taskID string) (download.Task, error)
	RetryAllTasks() (download.BatchRetryResult, error)
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
	GetLibraryItemMetaOnly(keyOrID string) (library.ItemMetadata, bool, error)
	MediaPath(key, relPath string) (string, error)
	PlanDelete(key string) (library.DeletePlan, error)
	Delete(key string) error
	DeleteLibraryItem(itemKey string, deleteFiles bool) error
	RebuildLibraryIndex() (library.RebuildReport, error)
	// RefreshLibrary rescans the library folders for files deleted or changed
	// outside TRACE (presence and size, no hashing) and reports what changed.
	RefreshLibrary() (library.RebuildReport, error)
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
	// MutateUIState changes ui-state.json under its lock, so a write from
	// another goroutine (the updater) between reading and writing is kept.
	MutateUIState(fn func(*uistate.State)) (uistate.State, error)
	GetUpdateHistory() update.History
	RecordUpdateCheck(rec update.CheckRecord) error
	RecordAppUpdate(rec update.UpdateRecord) error
	InstalledDevVersion() string
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
	MoveSubscriptionAccountToNew(site, uid, name, note string, rules subscription.SubRules) (subscription.Subscription, error)
	SetSubscriptionLinks(subID string, links []subscription.CustomLink) (subscription.Subscription, error)
	SetSubscriptionRules(subID string, rules subscription.SubRules) (subscription.Subscription, error)
	// Atomic cache capability tags (DEC-070).
	ListCapabilityTags() []subscription.CapabilityTag
	SetSubscriptionTags(subID string, tags []string) (subscription.Subscription, error)
	SetTagSubscriptions(tagID string, subIDs []string) error
	BatchUpdateSubscriptionsTags(subIDs []string, mode string, tags []string) error
	SubscriptionAccountWorks() map[string]AccountWorks
	PreviewAccount(ctx context.Context, input string) (subscription.AccountRef, *subscription.Subscription, error)
	OwnerOfAccount(site, uid string) (subscription.Subscription, bool)
	// AccountAvatarPath is the local copy of a subscribed account's avatar, or "".
	AccountAvatarPath(site, uid string) string
	DeleteSubscription(subIDOrMid string) error
}

// UpdateService defines subscription update checking, listing, and scheduler diagnostics.
type UpdateService interface {
	ListSubscriptionUpdatesPage(subID string, limit, offset int) ([]subscription.UpdateItem, error)
	ListSubscriptionUpdates(subID string) ([]subscription.UpdateItem, error)
	CheckSubscriptionsNow(ctx context.Context, targetMid int64) (subscription.CheckSummary, error)
	CheckSubscription(ctx context.Context, subID string) (subscription.CheckSummary, error)
	CheckAccount(ctx context.Context, subID, siteName, uid string) (subscription.CheckSummary, error)
	CancelSubscriptionCheck()
	SchedulerStatuses() map[string]SiteSchedulerStatus
	// CheckPlan is Tasks › 检查计划: each account's last and next checks.
	CheckPlan() []AccountCheckRow
}

// FollowingService defines read-only following group and creator import operations.
type FollowingService interface {
	RelationTags(ctx context.Context) ([]bilibili.RelationTag, error)
	RelationFollowings(ctx context.Context, tagID int64, pn, ps int) ([]bilibili.FollowingUser, error)
	ImportCreators(users []bilibili.FollowingUser, scope, autoCache string, liveNotify, liveAutoRecord bool) (int, error)
	FollowingGroups(ctx context.Context, siteID string) ([]FollowingGroupDTO, error)
	FollowingCreatorsPage(ctx context.Context, siteID, groupID string, page int) (FollowingCreatorsPageDTO, error)
	FollowingCreatorsPageWithCursor(ctx context.Context, siteID, groupID, cursor string) (FollowingCreatorsPageDTO, error)
	ImportFollowingCreators(siteID string, creators []FollowingCreatorDTO, rules subscription.SubRules) (subscription.ImportAccountRefsResult, error)
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
	// ClearLiveIncidents deletes the incident records of a finished live
	// session; the recording itself is kept.
	ClearLiveIncidents(itemKey, siteID, roomID, uid string) error
	LiveRecordingPreview(recKey string) (LiveRecordingPreviewInfo, bool)
}

// AppService defines application lifecycle, event subscription, and diagnostics operations.
type AppService interface {
	Close() error
	SubscribeEvents(fn func(Event)) func()
	Desktop() Desktop
	DiagnosticsSnapshot() Diagnostics
	RecentLogs(maxLines int) ([]string, error)
	// LogUserError records an error the UI showed to the user, so every
	// visible error also appears in the run log.
	LogUserError(err error)
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
	AccountService
	LiveService
	AppService
	RemoteService
	SubscriptionTransferService
	TransferService
	ConflictService
}

var _ BackendAPI = (*Backend)(nil)
