package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

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

// Backend coordinates all TRACE domain packages without importing any UI framework.
type Backend struct {
	loc                         appdirs.Locations
	db                          *sql.DB
	updatesDB                   *sql.DB
	settings                    *settings.Store
	uiState                     *uistate.Store
	history                     *storage.HistoryStore
	credMgr                     *credential.Manager
	cred                        *credential.Store
	douyinCred                  *credential.Store
	weiboCred                   *credential.Store
	xCred                       *credential.Store
	bili                        *bilibili.Client
	douyin                      *douyin.Client
	weibo                       *weibo.Client
	x                           *x.Client
	biliAdapter                 *bilibili.Adapter
	douyinAdapter               *douyin.Adapter
	weiboAdapter                *weibo.Adapter
	xAdapter                    *x.Adapter
	weiboPostCache              *weiboPostPlanCache
	weiboMemMu                  sync.Mutex
	weiboMemGen                 uint64
	accountWeiboSeen            map[string]map[string]bool
	weiboFollowingSeen          map[string]map[string]bool
	weiboPreviewRT              http.RoundTripper
	weiboPreviewWrap            func(http.RoundTripper) http.RoundTripper
	xPostCache                  *xPostPlanCache
	xMemMu                      sync.Mutex
	xMemGen                     uint64
	accountXSeen                map[string]map[string]bool
	xFollowingSeen              map[string]map[string]bool
	xFollowingCursors           map[int]string
	fullCacheUseFeatureOriginal bool
	registry                    *site.Registry
	lib                         *library.Store
	dl                          *download.Manager
	queue                       *cacheQueueManager
	subs                        *subscription.Manager
	live                        *live.Manager
	desktop                     Desktop
	events                      *eventBus
	bgCtx                       context.Context
	bgCancel                    context.CancelFunc
	renewalWake                 chan struct{}
	bgMu                        sync.Mutex
	bgClosing                   bool
	bgWg                        sync.WaitGroup
	logMu                       sync.Mutex
	lastUserError               string
	lastUserErrorAt             time.Time
	settingsMu                  sync.Mutex
	syncStateMu                 sync.Mutex
	avatarInflight              sync.Map
	logPath                     string
	previewDir                  string
	corruptFiles                []string
	updateHistory               *update.HistoryStore
	installedDevVersion         func() string
	profile                     Profile
	logMirror                   io.Writer
	recentErrors                []RecentError
	startedAt                   time.Time
	remote                      *remoteLink
	cacheGate                   func() error
	transferOnce                sync.Once
	transferState               *transferJobs
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
	if err := update.GuardHistorySchema(loc.DataDir); err != nil {
		return nil, err
	}
	allSchemas := append(append([]storage.SchemaFile(nil), storage.DataSchemas...), storage.SchemaFile{
		Name:    cacheQueueFileName,
		Kind:    storage.SchemaJSON,
		Current: cacheQueueSchemaVersion,
	})
	if _, err := storage.GuardDataSchemas(loc.DataDir, allSchemas); err != nil {
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
	if opts.Profile == ProfileRemote {
		if forced, changed := remoteSettings(curSettings, loc.DefaultCacheDir); changed {
			if updated, uErr := setStore.Update(forced); uErr == nil {
				curSettings = updated
			}
		}
	}
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
	weiboCredStore := credMgr.Weibo()
	xCredStore := credMgr.X()

	var (
		db        *sql.DB
		updatesDB *sql.DB
		bgCancel  context.CancelFunc
		initOK    bool
	)
	defer func() {
		if initOK {
			return
		}
		if bgCancel != nil {
			bgCancel()
		}
		if updatesDB != nil {
			_ = updatesDB.Close()
		}
		if db != nil {
			_ = db.Close()
		}
	}()

	dbPath := filepath.Join(loc.DataDir, "library.db")
	_, dbStatErr := os.Stat(dbPath)
	dbWasMissing := os.IsNotExist(dbStatErr)
	var dbRec bool
	var dbBackup string
	db, dbRec, dbBackup, err = storage.OpenSQLite(dbPath)
	if err != nil {
		return nil, err
	}
	if dbRec && dbBackup != "" {
		corruptBackups = append(corruptBackups, dbBackup)
	}
	if _, err := storage.CheckSQLiteSchema(db, "library.db", schemaVersion("library.db")); err != nil {
		return nil, err
	}

	updatesDBPath := filepath.Join(loc.DataDir, "updates.db")
	var updatesRec bool
	var updatesBackup string
	updatesDB, updatesRec, updatesBackup, err = storage.OpenSQLite(updatesDBPath)
	if err != nil {
		return nil, err
	}
	if updatesRec && updatesBackup != "" {
		corruptBackups = append(corruptBackups, updatesBackup)
	}
	if _, err := storage.CheckSQLiteSchema(updatesDB, "updates.db", schemaVersion("updates.db")); err != nil {
		return nil, err
	}

	libStore, err := library.OpenWithDataDir(db, curSettings.CacheDir, loc.DataDir)
	if err != nil {
		return nil, err
	}
	if opts.Profile == ProfileRemote {
		// On the remote, completed items are uploaded and deleted from the
		// export folder; keep their index rows so auto-cache never re-downloads them.
		libStore.SetAllowMovedFiles(true)
	}
	if node, nErr := library.LoadOrCreateNode(loc.DataDir, nodeName(opts.NodeName)); nErr == nil {
		libStore.SetNode(node)
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
	var bgCtx context.Context
	bgCtx, bgCancel = context.WithCancel(context.Background())

	b := &Backend{
		profile:                     opts.Profile,
		logMirror:                   opts.LogMirror,
		startedAt:                   time.Now(),
		loc:                         loc,
		db:                          db,
		updatesDB:                   updatesDB,
		settings:                    setStore,
		uiState:                     uiStore,
		history:                     histStore,
		credMgr:                     credMgr,
		cred:                        credStore,
		douyinCred:                  douyinCredStore,
		weiboCred:                   weiboCredStore,
		xCred:                       xCredStore,
		weiboPostCache:              newWeiboPostPlanCache(nil),
		accountWeiboSeen:            make(map[string]map[string]bool),
		weiboFollowingSeen:          make(map[string]map[string]bool),
		xPostCache:                  newXPostPlanCache(nil),
		accountXSeen:                make(map[string]map[string]bool),
		xFollowingSeen:              make(map[string]map[string]bool),
		xFollowingCursors:           make(map[int]string),
		fullCacheUseFeatureOriginal: opts.FullCacheUseFeatureOriginal,
		lib:                         libStore,
		desktop:                     desk,
		events:                      evBus,
		bgCtx:                       bgCtx,
		bgCancel:                    bgCancel,
		renewalWake:                 make(chan struct{}, 1),
		remote:                      &remoteLink{},
		logPath:                     filepath.Join(loc.LogsDir, "trace.log"),
		previewDir:                  previewDir,
		corruptFiles:                corruptBackups,
	}
	if err := b.initUpdateHistory(opts); err != nil {
		return nil, err
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

	wbOpts := opts.WeiboOpts
	wbOpts.CredStore = weiboCredStore
	if wbOpts.PreferredCodec == nil {
		wbOpts.PreferredCodec = b.preferredWeiboCodec
	}
	wbClient := weibo.NewClient(wbOpts)
	wbAdapter := weibo.NewAdapter(wbClient)

	xOpts := opts.XOpts
	xOpts.CredStore = xCredStore
	xClient := x.NewClient(xOpts)
	xAdapter := x.NewAdapter(xClient)

	b.bili = biliClient
	b.douyin = dyClient
	b.weibo = wbClient
	b.x = xClient
	b.weiboPreviewRT = wbOpts.Transport
	b.weiboPreviewWrap = wbOpts.WrapTransport
	b.biliAdapter = biliAdapter
	b.douyinAdapter = dyAdapter
	b.weiboAdapter = wbAdapter
	b.xAdapter = xAdapter
	b.registry = site.NewRegistry(biliAdapter, dyAdapter, wbAdapter, xAdapter)

	taskLog := newTaskStatusLog()
	dlMgr, err := download.NewManager(db, libStore, biliClient, curSettings.Download, func(t download.Task) {
		if taskLog.changed(t.TaskID, t.Status) {
			b.logInfo(fmt.Sprintf("download task %s (%s) -> %s", t.TaskID, t.TargetID, t.Status))
		}
		message := t.TaskID + ":" + t.Status
		if t.ErrorKind == "storage" {
			message += ":storage"
		}
		evBus.PublishSite(EventTaskUpdated, t.Site, message)
		if t.Status == "completed" || t.Status == "partial" {
			evBus.PublishSite(EventLibraryChanged, t.Site, t.ItemKey)
		}
		if b.queue != nil {
			b.queue.onDownloadUpdated(t.Site)
		}
	})
	if err != nil {
		return nil, err
	}
	b.dl = dlMgr
	dlMgr.SetOnTaskBatchUpdate(func(action string, tasks []download.Task) {
		if len(tasks) == 0 {
			return
		}
		var firstSite string
		singleSite := true
		seenSites := make(map[string]struct{}, 4)
		for _, t := range tasks {
			taskLog.changed(t.TaskID, t.Status)
			if t.Site != "" {
				seenSites[t.Site] = struct{}{}
				if firstSite == "" {
					firstSite = t.Site
				} else if firstSite != t.Site {
					singleSite = false
				}
			}
		}
		b.logInfo(fmt.Sprintf("download batch %s: %d tasks", action, len(tasks)))
		msg := "batch:" + action + ":" + strconv.Itoa(len(tasks))
		if action == "retry_batch" {
			msg = "retried:all"
		}
		if singleSite && firstSite != "" && action != "retry_batch" {
			evBus.PublishSite(EventTaskUpdated, firstSite, msg)
		} else {
			evBus.Publish(EventTaskUpdated, msg)
		}
		if b.queue != nil {
			for s := range seenSites {
				b.queue.onDownloadUpdated(s)
			}
		}
	})
	dlMgr.RegisterWorkPlanner(&douyinWorkPlanner{backend: b})
	dlMgr.RegisterWorkPlanner(&weiboWorkPlanner{backend: b})
	dlMgr.RegisterWorkPlanner(&xWorkPlanner{backend: b})

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
		return nil, err
	}
	b.subs = subMgr
	if opts.CacheQueueClock != nil {
		subMgr.SetNowFunc(opts.CacheQueueClock.Now)
	}
	queuePath := filepath.Join(loc.DataDir, cacheQueueFileName)
	queueMgr, queueLoadBackup, err := newCacheQueueManager(b, queuePath, opts.CacheQueueClock, opts.CacheQueueRand, opts.CacheQueuePacing)
	if err != nil {
		return nil, err
	}
	if queueLoadBackup != "" {
		b.corruptFiles = append(b.corruptFiles, queueLoadBackup)
	}
	b.queue = queueMgr
	// Every table of both databases now has the layout of this build.
	if err := storage.StampSQLiteSchema(db, schemaVersion("library.db")); err != nil {
		return nil, err
	}
	if err := storage.StampSQLiteSchema(updatesDB, schemaVersion("updates.db")); err != nil {
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
		b.pauseSiteActiveWorkAsync(site.Bilibili, "auth_required", "哔哩哔哩登录账号已变更，任务已暂停")
	})
	biliClient.SetAutomaticRequestHandlers(subMgr.AutomaticRequestAllowed, subMgr.RecordAutomaticStop)
	dyClient.SetAutomaticRequestHandlers(
		func(_ context.Context, source string, _ string) error {
			return subMgr.SiteAutomaticRequestAllowedFor(string(site.Douyin), source)
		},
		func(_ string, err error) {
			subMgr.RecordSiteAutomaticStop(string(site.Douyin), site.KindOf(err))
		},
	)
	wbClient.SetAutomaticRequestHandlers(
		func(_ context.Context, source string, _ string) error {
			return subMgr.SiteAutomaticRequestAllowedFor(string(site.Weibo), source)
		},
		func(_ string, err error) {
			subMgr.RecordSiteAutomaticStop(string(site.Weibo), site.KindOf(err))
		},
	)
	xClient.SetAutomaticRequestHandlers(
		func(_ context.Context, source string, _ string) error {
			return subMgr.SiteAutomaticRequestAllowedFor(string(site.X), source)
		},
		func(_ string, err error) {
			b.recordXAutomaticStop(err)
		},
	)
	userCT0Handler := opts.XOpts.OnCT0Updated
	xClient.SetCT0UpdatedHandler(func(newCT0 string) {
		b.logInfo("x ct0 synced")
		if userCT0Handler != nil {
			userCT0Handler(newCT0)
		}
	})
	subMgr.RegisterFetcher(dyAdapter)
	subMgr.RegisterFetcher(wbAdapter)
	subMgr.RegisterFetcher(xAdapter)
	subMgr.SetSiteAuthCheck(func(siteID site.ID) bool {
		return b.credMgr.HasActiveCredential(string(siteID))
	})
	subMgr.RegisterAutoCacheEnqueuer(site.Douyin, func(ctx context.Context, item subscription.UpdateItem) (string, error) {
		// The update came from the subscribed account's works list: a
		// co-creation by someone else goes to that account's folder.
		t, err := b.enqueueDouyinWorkWithSource(ctx, item.DynID, "auto", item.UID, b.subscribedAccountName(site.Douyin, item.UID))
		return t.TaskID, err
	})
	subMgr.RegisterAutoCacheEnqueuer(site.Weibo, func(ctx context.Context, item subscription.UpdateItem) (string, error) {
		var via *library.ItemVia
		if strings.EqualFold(strings.TrimSpace(item.Type), weibo.RuleTypeForward) {
			via = &library.ItemVia{
				Reason:    library.ViaForward,
				Site:      string(site.Weibo),
				UID:       strings.TrimSpace(item.UID),
				Name:      b.subscribedAccountName(site.Weibo, item.UID),
				DynamicID: strings.TrimSpace(item.DynID),
			}
		}
		t, err := b.enqueueWeiboPostWithSource(ctx, item.DynID, "auto", via, nil)
		return t.TaskID, err
	})
	subMgr.RegisterAutoCacheEnqueuer(site.X, func(ctx context.Context, item subscription.UpdateItem) (string, error) {
		var via *library.ItemVia
		if strings.EqualFold(strings.TrimSpace(item.Type), x.RuleTypeForward) {
			via = &library.ItemVia{
				Reason:    library.ViaForward,
				Site:      string(site.X),
				UID:       strings.TrimSpace(item.UID),
				Name:      b.subscribedAccountName(site.X, item.UID),
				DynamicID: strings.TrimSpace(item.DynID),
			}
		}
		t, err := b.enqueueXPostWithSource(ctx, item.DynID, "auto", via, nil)
		return t.TaskID, err
	})
	if opts.Profile != ProfileRemote {
		subMgr.SetOnAuthorAvatar(b.rememberAccountAvatar)
	} else {
		// No new cache task starts while the export folder is almost full
		// (remote.md §6.2); the remote has no avatars or recent parses.
		subMgr.SetAutoCacheGate(b.remoteCacheGate)
	}
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
		b.pauseSiteActiveWorkAsync(site.Bilibili, "auth_required", "哔哩哔哩登录已失效，任务已暂停")
		evBus.PublishSite(EventAccountChanged, string(site.Bilibili), "expired")
	})
	dyClient.SetAuthExpiredHandler(func(reason string) {
		msg := firstNonEmpty(reason, "抖音登录已失效，任务已暂停")
		b.pauseSiteActiveWorkAsync(site.Douyin, "auth_required", msg)
		evBus.PublishSite(EventAccountChanged, string(site.Douyin), "expired")
	})
	wbClient.SetAuthExpiredHandler(func(reason string) {
		msg := firstNonEmpty(reason, "微博登录已失效，任务已暂停")
		b.clearWeiboMemoryCaches()
		b.pauseSiteActiveWorkAsync(site.Weibo, "auth_required", msg)
		evBus.PublishSite(EventAccountChanged, string(site.Weibo), "expired")
	})
	xClient.SetAuthExpiredHandler(func(reason string) {
		msg := firstNonEmpty(reason, "X 登录已失效，任务已暂停")
		b.clearXMemoryCaches()
		b.pauseSiteActiveWorkAsync(site.X, "auth_required", msg)
		evBus.PublishSite(EventAccountChanged, string(site.X), "expired")
	})

	build := appdirs.BuildInfo()
	b.logInfo(fmt.Sprintf("backend initialized: app=%s version=%s commit=%s built=%s mode=%s channel=%s", loc.AppName, build.Version, firstNonEmpty(build.Commit, "-"), firstNonEmpty(build.BuildTime, "-"), loc.Mode, loc.Channel))

	if !opts.DisableBgWorkers {
		if err := dlMgr.Start(bgCtx); err != nil {
			return nil, err
		}
		subMgr.Start(bgCtx)
		liveMgr.Start(bgCtx)
		if opts.Profile != ProfileRemote {
			// The remote never renews (Cookies are renewed only by the
			// desktop, DEC-062 ③) and never runs 全部缓存 (remote.md §4).
			queueMgr.Start(bgCtx)
			b.pruneAccountAvatars()
			b.bgWg.Add(1)
			go b.runRenewalLoop(bgCtx)
			b.bgWg.Add(1)
			go b.runFullCacheLoop(bgCtx)
			b.bgWg.Add(1)
			go b.runRemoteCookieRetry(bgCtx)
		}
		b.bgWg.Add(1)
		go func() { defer b.bgWg.Done(); _, _ = b.BootstrapAuth(bgCtx) }()
	}

	initOK = true
	return b, nil
}

func schemaVersion(name string) int {
	if sf, ok := storage.SchemaFor(name); ok {
		return sf.Current
	}
	return 1
}

// goBg spawns a tracked background goroutine bound to b.bgCtx.
// Returns false without spawning once Close has begun.
func (b *Backend) goBg(fn func(ctx context.Context)) bool {
	b.bgMu.Lock()
	if b.bgClosing || b.bgCtx == nil || b.bgCtx.Err() != nil {
		b.bgMu.Unlock()
		return false
	}
	b.bgWg.Add(1)
	ctx := b.bgCtx
	b.bgMu.Unlock()
	go func() {
		defer b.bgWg.Done()
		fn(ctx)
	}()
	return true
}

// pauseSiteActiveWork pauses active download tasks, cache queues, and stops active
// live recordings belonging ONLY to targetSite, leaving other sites untouched.
func (b *Backend) pauseSiteActiveWork(targetSite site.ID, reasonCode, reasonMsg string) {
	if b.dl != nil {
		b.dl.PauseForSite(targetSite, reasonCode, reasonMsg)
	}
	if b.queue != nil {
		b.queue.pauseForSite(targetSite, reasonCode, reasonMsg)
	}
	if b.live != nil {
		b.live.StopForSite(targetSite, reasonCode)
	}
}

// pauseSiteActiveWorkAsync pauses downloads and cache queues synchronously, and
// stops active live recordings for targetSite asynchronously on a tracked goroutine.
// Used from site client callbacks (auth expiry / account replacement) where the
// caller may hold a site client lock or run on a recording goroutine itself.
func (b *Backend) pauseSiteActiveWorkAsync(targetSite site.ID, reasonCode, reasonMsg string) {
	if b.dl != nil {
		b.dl.PauseForSite(targetSite, reasonCode, reasonMsg)
	}
	if b.queue != nil {
		b.queue.pauseForSite(targetSite, reasonCode, reasonMsg)
	}
	if b.live != nil {
		liveMgr := b.live
		b.goBg(func(context.Context) {
			liveMgr.StopForSite(targetSite, reasonCode)
		})
	}
}

// Close stops background workers, removes temporary media previews, and closes SQLite databases.
func (b *Backend) Close() error {
	b.bgMu.Lock()
	b.bgClosing = true
	b.bgMu.Unlock()
	if b.bgCancel != nil {
		b.bgCancel()
	}
	b.bgWg.Wait()
	if b.live != nil {
		b.live.Stop()
	}
	if b.queue != nil {
		b.queue.Stop()
	}
	if b.subs != nil {
		b.subs.Stop()
	}
	if b.dl != nil {
		b.dl.Stop()
	}
	if b.remote != nil {
		b.remote.closeClient()
	}
	if b.previewDir != "" {
		_ = os.RemoveAll(b.previewDir)
	}
	var errs []error
	if b.updatesDB != nil {
		if err := b.updatesDB.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if b.db != nil {
		if err := b.db.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
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

// WeiboClient returns the underlying Weibo site client.
func (b *Backend) WeiboClient() *weibo.Client {
	return b.weibo
}

// XClient returns the underlying X site client.
func (b *Backend) XClient() *x.Client {
	return b.x
}

func (b *Backend) recordXAutomaticStop(err error) {
	if b == nil || b.subs == nil || err == nil {
		return
	}
	kind := site.KindOf(err)
	if kind == site.ErrKindRateLimited {
		if rl, ok := x.RateLimitDetails(err); ok {
			until := rl.BlockedUntil
			if until.IsZero() {
				until = rl.ResetAt
			}
			if !until.IsZero() {
				b.subs.RecordSiteAutomaticStopUntil(string(site.X), kind, until)
				return
			}
		}
	}
	b.subs.RecordSiteAutomaticStop(string(site.X), kind)
}

// SetWeiboFullCacheUseFeatureOriginal toggles whether Weibo full-cache queues
// request feature=1 (server-side original filter) instead of feature=0 with local filtering.
func (b *Backend) SetWeiboFullCacheUseFeatureOriginal(enabled bool) {
	b.weiboMemMu.Lock()
	b.fullCacheUseFeatureOriginal = enabled
	b.weiboMemMu.Unlock()
}

func (b *Backend) weiboFullCacheUseFeatureOriginal() bool {
	b.weiboMemMu.Lock()
	defer b.weiboMemMu.Unlock()
	return b.fullCacheUseFeatureOriginal
}

func (b *Backend) clearWeiboMemoryCaches() {
	if b.weiboPostCache != nil {
		b.weiboPostCache.clear()
	}
	b.weiboMemMu.Lock()
	b.weiboMemGen++
	clear(b.accountWeiboSeen)
	clear(b.weiboFollowingSeen)
	b.weiboMemMu.Unlock()
}

func (b *Backend) snapshotWeiboSeenMap(store *map[string]map[string]bool, bucket string, reset bool) (map[string]bool, uint64) {
	b.weiboMemMu.Lock()
	defer b.weiboMemMu.Unlock()
	gen := b.weiboMemGen
	if reset {
		if *store != nil {
			delete(*store, bucket)
		}
		return make(map[string]bool), gen
	}
	if *store == nil || (*store)[bucket] == nil {
		return make(map[string]bool), gen
	}
	src := (*store)[bucket]
	cp := make(map[string]bool, len(src))
	for k, v := range src {
		if v {
			cp[k] = true
		}
	}
	return cp, gen
}

func (b *Backend) mergeWeiboSeenMap(store *map[string]map[string]bool, bucket string, gen uint64, seen map[string]bool) {
	if len(seen) == 0 {
		return
	}
	b.weiboMemMu.Lock()
	defer b.weiboMemMu.Unlock()
	if b.weiboMemGen != gen {
		return
	}
	if *store == nil {
		*store = make(map[string]map[string]bool)
	}
	dst := (*store)[bucket]
	if dst == nil {
		dst = make(map[string]bool, len(seen))
		(*store)[bucket] = dst
	}
	for k, v := range seen {
		if v {
			dst[k] = true
		}
	}
}

func (b *Backend) clearXMemoryCaches() {
	if b.xPostCache != nil {
		b.xPostCache.clear()
	}
	b.xMemMu.Lock()
	b.xMemGen++
	clear(b.accountXSeen)
	clear(b.xFollowingSeen)
	clear(b.xFollowingCursors)
	b.xMemMu.Unlock()
}

func (b *Backend) snapshotXSeenMap(store *map[string]map[string]bool, bucket string, reset bool) (map[string]bool, uint64) {
	b.xMemMu.Lock()
	defer b.xMemMu.Unlock()
	gen := b.xMemGen
	if reset {
		if *store != nil {
			delete(*store, bucket)
		}
		return make(map[string]bool), gen
	}
	if *store == nil || (*store)[bucket] == nil {
		return make(map[string]bool), gen
	}
	src := (*store)[bucket]
	cp := make(map[string]bool, len(src))
	for k, v := range src {
		if v {
			cp[k] = true
		}
	}
	return cp, gen
}

func (b *Backend) mergeXSeenMap(store *map[string]map[string]bool, bucket string, gen uint64, seen map[string]bool) {
	if len(seen) == 0 {
		return
	}
	b.xMemMu.Lock()
	defer b.xMemMu.Unlock()
	if b.xMemGen != gen {
		return
	}
	if *store == nil {
		*store = make(map[string]map[string]bool)
	}
	dst := (*store)[bucket]
	if dst == nil {
		dst = make(map[string]bool, len(seen))
		(*store)[bucket] = dst
	}
	for k, v := range seen {
		if v {
			dst[k] = true
		}
	}
}

func (b *Backend) getXFollowingCursor(page int) string {
	b.xMemMu.Lock()
	defer b.xMemMu.Unlock()
	if page <= 1 || b.xFollowingCursors == nil {
		return ""
	}
	return b.xFollowingCursors[page]
}

func (b *Backend) setXFollowingCursor(page int, cursor string, gen uint64, reset bool) {
	b.xMemMu.Lock()
	defer b.xMemMu.Unlock()
	if b.xMemGen != gen {
		return
	}
	if b.xFollowingCursors == nil || reset {
		b.xFollowingCursors = make(map[int]string)
	}
	if strings.TrimSpace(cursor) != "" {
		b.xFollowingCursors[page] = strings.TrimSpace(cursor)
	}
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
	prev := b.settings.Get()
	if b.profile == ProfileRemote {
		next, _ = remoteSettings(next, prev.CacheDir)
	}
	preview, err := b.settings.PreviewUpdate(next)
	if err != nil {
		b.settingsMu.Unlock()
		return prev, err
	}
	if preview.Network.ProxyMode == site.ProxyCustom {
		if _, err := site.ParseProxyURL(preview.Network.ProxyURL); err != nil {
			b.settingsMu.Unlock()
			return prev, &site.Error{Kind: site.ErrKindBadRequest, Message: err.Error()}
		}
	}
	var updated settings.Settings
	persist := func() error {
		var err error
		updated, err = b.settings.Update(preview)
		return err
	}
	if !samePath(prev.CacheDir, preview.CacheDir) {
		err = b.lib.ChangeCacheDir(preview.CacheDir, persist)
	} else {
		err = persist()
	}
	if err != nil {
		b.settingsMu.Unlock()
		return prev, err
	}
	_ = site.SetProxy(updated.Network.ProxyMode, updated.Network.ProxyURL) // checked above; applies at once
	b.dl.UpdateSettings(updated.Download)
	b.subs.UpdateSettings(updated.Subscription)
	b.live.UpdateSettings(updated.Live)
	b.settingsMu.Unlock()

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

// MutateUIState changes fields of ui-state.json under its lock.
func (b *Backend) MutateUIState(fn func(*uistate.State)) (uistate.State, error) {
	return b.uiState.Mutate(fn)
}

// ActiveWork counts the queued and running cache tasks and the live
// recordings that quitting would pause or end.
func (b *Backend) ActiveWork() (tasks, recordings int) {
	if b.dl != nil {
		if list, err := b.dl.ListNonTerminalTasks(); err == nil {
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
	if b.queue != nil {
		b.queue.pauseForExit()
	}
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

// PauseAllActiveWork pauses active download tasks, cache queues, and live
// recordings for sites that currently have no active credential, leaving
// still-authenticated sites running undisturbed.
func (b *Backend) PauseAllActiveWork() {
	for _, sid := range site.All() {
		if b.credMgr == nil || !b.credMgr.HasActiveCredential(string(sid)) {
			b.pauseSiteActiveWork(sid, "auth_required", "登录状态已变更，任务已暂停")
		}
	}
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

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
