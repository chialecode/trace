package subscription

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
)

// ErrAccountConflict is returned when linking an account that already belongs to another subscription.
var ErrAccountConflict = errors.New("subscription: account already belongs to another subscription")

// AvatarRef identifies which linked account provides the subscription avatar.
type AvatarRef struct {
	Site string `json:"site"`
	UID  string `json:"uid"`
}

// AccountRef represents a site account associated with a Subscription (subscriptions.md §1).
type AccountRef struct {
	Site     string `json:"site"`
	UID      string `json:"uid"`
	UniqueID string `json:"uniqueId,omitempty"`
	WebRID   string `json:"webRid,omitempty"`
	Name     string `json:"name"`
	Face     string `json:"face"`
	AddedAt  string `json:"addedAt"`
}

// AutoCacheRule configures automatic caching for a subscription (subscriptions.md §1 & §4).
type AutoCacheRule struct {
	Enabled bool     `json:"enabled"`
	Types   []string `json:"types"` // "video", "draw", "word", "forward"
	Since   string   `json:"since"` // RFC3339 timestamp when enabled; empty when disabled
}

// SubRules holds the per-subscription automation rules (subscriptions.md §1).
type SubRules struct {
	ScheduledCheck bool          `json:"scheduledCheck"`
	AutoCache      AutoCacheRule `json:"autoCache"`
	AutoRecord     bool          `json:"autoRecord"`
}

// subscriptionsFile is the on-disk schema of subscriptions.json (subscriptions.md §1).
type subscriptionsFile struct {
	Version       int            `json:"version"`
	Subscriptions []Subscription `json:"subscriptions"`
	CacheRules    []CacheRule    `json:"cacheRules,omitempty"`
}

// Subscription represents a tracked person / entity with one or more site accounts (DEC-026, subscriptions.md §1).
// Includes backward-compatible primary-account fields (Mid, Uname, Face, etc.) kept in sync.
type Subscription struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	Note      string       `json:"note"`
	Avatar    AvatarRef    `json:"avatar"`
	CreatedAt string       `json:"createdAt"`
	Accounts  []AccountRef `json:"accounts"`
	Rules     SubRules     `json:"rules"`
	// RuleID is the shared cache rule the subscription uses (DEC-040);
	// Rules holds that rule's settings plus this subscription's own
	// auto-cache start time.
	RuleID string `json:"ruleId,omitempty"`

	// Runtime & compatibility fields
	SubID               string `json:"subId,omitempty"`
	Site                string `json:"site,omitempty"`
	Mid                 int64  `json:"mid,omitempty"`
	Uname               string `json:"uname,omitempty"`
	Face                string `json:"face,omitempty"`
	Sign                string `json:"sign,omitempty"`
	Enabled             bool   `json:"enabled,omitempty"`
	Scope               string `json:"scope,omitempty"`     // "video", "dynamic", "both"
	AutoCache           string `json:"autoCache,omitempty"` // "inherit", "none", "video", "dynamic", "both"
	LiveNotify          bool   `json:"liveNotify,omitempty"`
	LiveAutoRecord      bool   `json:"liveAutoRecord,omitempty"`
	RoomID              int64  `json:"roomId,omitempty"`
	LastVideoBVID       string `json:"lastVideoBvid,omitempty"`
	LastVideoPubTs      int64  `json:"lastVideoPubTs,omitempty"`
	LastDynamicID       string `json:"lastDynamicId,omitempty"`
	LastDynamicPubTs    int64  `json:"lastDynamicPubTs,omitempty"`
	LastCheckedAt       string `json:"lastCheckedAt,omitempty"`
	LastSuccessAt       string `json:"lastSuccessAt,omitempty"`
	ConsecutiveFailures int    `json:"consecutiveFailures,omitempty"`
	LastError           string `json:"lastError,omitempty"`
	UpdatedAt           string `json:"updatedAt,omitempty"`
	AutoCacheBacklog    int    `json:"autoCacheBacklog,omitempty"`
	LatestUpdateTs      int64  `json:"-"`
}

// AccountState represents a row in updates.db account_state (subscriptions.md §2).
type AccountState struct {
	Site          string `json:"site"`
	UID           string `json:"uid"`
	BaselineAt    string `json:"baselineAt"`
	LastCheckedAt string `json:"lastCheckedAt"`
	LastKind      string `json:"lastKind"`
	NewestID      string `json:"newestId"`
	NewestTs      int64  `json:"newestTs"`
	Note          string `json:"note"`
	UnlinkedAt    string `json:"unlinkedAt,omitempty"`
}

// UpdateItem represents a discovered dynamic in updates.db update_item (subscriptions.md §2).
type UpdateItem struct {
	Site         string `json:"site"`
	UID          string `json:"uid"`
	DynID        string `json:"dynId"`
	PubTs        int64  `json:"pubTs"`
	Type         string `json:"type"` // "video", "draw", "word", "forward", "article", "other"
	Snapshot     string `json:"snapshot"`
	DiscoveredAt string `json:"discoveredAt"`
	AutoTaskID   string `json:"autoTaskId"`
}

// SchedulerState represents the persistent scheduler backoff and token bucket state in updates.db (subscriptions.md §3).
type SchedulerState struct {
	Site                string  `json:"site,omitempty"`
	BackoffLevel        int     `json:"backoffLevel,omitempty"`
	ConsecutiveFailures int     `json:"consecutiveFailures"`
	BackoffUntil        string  `json:"backoffUntil"`
	LastStopReason      string  `json:"lastStopReason"`
	LastStopAt          string  `json:"lastStopAt"`
	TokenTimestamps     []int64 `json:"tokenTimestamps"`
}

// CheckSummary reports the result of a manual or scheduled subscription check run.
type CheckSummary struct {
	StartedAt             string            `json:"startedAt"`
	FinishedAt            string            `json:"finishedAt"`
	CheckedCreators       int               `json:"checkedCreators"`
	TotalCreators         int               `json:"totalCreators"`
	CurrentUID            string            `json:"currentUid,omitempty"`
	Canceled              bool              `json:"canceled,omitempty"`
	BaselineInitialized   int               `json:"baselineInitialized"`
	NewVideos             int               `json:"newVideos"`
	NewDynamics           int               `json:"newDynamics"`
	AutoCacheTasks        int               `json:"autoCacheTasks"`
	AutoCacheBacklog      int               `json:"autoCacheBacklog"`
	FailedCreators        int               `json:"failedCreators"`
	StoppedByRiskControl  bool              `json:"stoppedByRiskControl"`
	DeferredByQuota       bool              `json:"deferredByQuota,omitempty"`
	DeferredByBackoff     bool              `json:"deferredByBackoff,omitempty"`
	SiteStopped           map[string]string `json:"siteStopped,omitempty"`
	SiteDeferredByQuota   []string          `json:"siteDeferredByQuota,omitempty"`
	SiteDeferredByBackoff []string          `json:"siteDeferredByBackoff,omitempty"`
	SkippedUnauthSites    []string          `json:"skippedUnauthSites,omitempty"`
	IncompleteAccounts    []string          `json:"incompleteAccounts,omitempty"`
	GapAccounts           []string          `json:"gapAccounts,omitempty"`
	Errors                []string          `json:"errors,omitempty"`
}

// Manager coordinates subscription persistence (subscriptions.json + .bak), updates.db state, incremental checks, and scheduling.
type Manager struct {
	mu          sync.Mutex
	db          *sql.DB
	updatesDB   *sql.DB
	ownsUpdates bool
	subsPath    string
	lib         *library.Store
	dl          *download.Manager
	client      *bilibili.Client
	subSettings settings.SubscriptionSettings
	rules       []CacheRule
	onEvent     func(CheckSummary)
	liveCheck   func(context.Context, []int64) error
	nowFunc     func() time.Time

	fetchers               map[site.ID]site.SubscriptionFetcher
	siteAuthCheck          func(site.ID) bool
	autoCacheEnqueuers     map[site.ID]AutoCacheEnqueueFunc
	onLiveHint             func(acc AccountRef, roomID, webRID string)
	siteLiveCheck          func(ctx context.Context, accounts []AccountRef) error
	sleepFunc              func(ctx context.Context, d time.Duration) error
	siteIntervals          map[string]time.Duration
	accountIntervalTouched bool

	accountInterval      time.Duration
	corruptRecovered     bool
	duplicateAccountWarn bool

	checking    bool
	checkCancel context.CancelFunc
	lastSummary *CheckSummary

	schedCancel context.CancelFunc
	schedWake   chan struct{}
	wg          sync.WaitGroup
}

// NewManager initializes a subscription Manager backed by db (and automatically uses subscriptions.json in the same data directory).
func NewManager(
	db *sql.DB,
	lib *library.Store,
	dl *download.Manager,
	client *bilibili.Client,
	subSettings settings.SubscriptionSettings,
	onEvent func(CheckSummary),
) (*Manager, error) {
	dataDir := discoverDBDir(db)
	subsPath := ""
	if dataDir != "" {
		subsPath = filepath.Join(dataDir, "subscriptions.json")
	}
	return NewManagerWithUpdatesDB(db, db, subsPath, lib, dl, client, subSettings, onEvent)
}

// NewManagerWithUpdatesDB initializes a subscription Manager with an explicit updates.db handle and subscriptions.json path.
func NewManagerWithUpdatesDB(
	db *sql.DB,
	updatesDB *sql.DB,
	subsPath string,
	lib *library.Store,
	dl *download.Manager,
	client *bilibili.Client,
	subSettings settings.SubscriptionSettings,
	onEvent func(CheckSummary),
) (*Manager, error) {
	if updatesDB == nil {
		updatesDB = db
	}
	m := &Manager{
		db:                 db,
		updatesDB:          updatesDB,
		subsPath:           subsPath,
		lib:                lib,
		dl:                 dl,
		client:             client,
		subSettings:        subSettings,
		onEvent:            onEvent,
		nowFunc:            time.Now,
		fetchers:           make(map[site.ID]site.SubscriptionFetcher),
		autoCacheEnqueuers: make(map[site.ID]AutoCacheEnqueueFunc),
		sleepFunc:          defaultSubscriptionSleep,
		siteIntervals:      make(map[string]time.Duration),
		accountInterval:    DefaultBilibiliAccountInterval,
		schedWake:          make(chan struct{}, 1),
	}
	if err := m.initSchema(); err != nil {
		return nil, err
	}
	if err := m.loadSubscriptionsFromDiskLocked(); err != nil {
		return nil, err
	}
	m.cleanupExpiredUnlinkedAccountsLocked()
	return m, nil
}

func discoverDBDir(db *sql.DB) string {
	if db == nil {
		return ""
	}
	rows, err := db.Query(`PRAGMA database_list`)
	if err != nil {
		return ""
	}
	defer rows.Close()
	for rows.Next() {
		var seq int
		var name, file string
		if err := rows.Scan(&seq, &name, &file); err == nil && name == "main" && file != "" && !strings.HasPrefix(file, ":") {
			return filepath.Dir(file)
		}
	}
	return ""
}

// SetNowFunc overrides the clock used by Manager (for deterministic tests).
func (m *Manager) SetNowFunc(fn func() time.Time) {
	m.mu.Lock()
	if fn != nil {
		m.nowFunc = fn
	}
	m.mu.Unlock()
}

// SetAccountInterval sets the minimum delay between checking two accounts in a round (default 2s in production per subscriptions.md §2).
func (m *Manager) SetAccountInterval(d time.Duration) {
	m.mu.Lock()
	m.accountInterval = d
	m.accountIntervalTouched = true
	m.mu.Unlock()
}

// SetSubscriptionsPath overrides the path to subscriptions.json and reloads from disk.
func (m *Manager) SetSubscriptionsPath(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subsPath = path
	return m.loadSubscriptionsFromDiskLocked()
}

// CorruptRecovered reports whether subscriptions.json was recovered from a corrupt file on startup.
func (m *Manager) CorruptRecovered() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.corruptRecovered
}

func (m *Manager) initSchema() error {
	qSub := `CREATE TABLE IF NOT EXISTS subscriptions (
		sub_id TEXT PRIMARY KEY,
		site TEXT NOT NULL,
		mid INTEGER NOT NULL,
		uname TEXT NOT NULL DEFAULT '',
		face TEXT NOT NULL DEFAULT '',
		sign TEXT NOT NULL DEFAULT '',
		enabled INTEGER NOT NULL DEFAULT 1,
		scope TEXT NOT NULL DEFAULT 'both',
		auto_cache TEXT NOT NULL DEFAULT 'inherit',
		live_notify INTEGER NOT NULL DEFAULT 1,
		live_auto_record INTEGER NOT NULL DEFAULT 0,
		room_id INTEGER NOT NULL DEFAULT 0,
		last_video_bvid TEXT NOT NULL DEFAULT '',
		last_video_pub_ts INTEGER NOT NULL DEFAULT 0,
		last_dynamic_id TEXT NOT NULL DEFAULT '',
		last_dynamic_pub_ts INTEGER NOT NULL DEFAULT 0,
		last_checked_at TEXT NOT NULL DEFAULT '',
		last_success_at TEXT NOT NULL DEFAULT '',
		consecutive_failures INTEGER NOT NULL DEFAULT 0,
		last_error TEXT NOT NULL DEFAULT '',
		raw_json TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);`
	if _, err := m.db.Exec(qSub); err != nil {
		return err
	}
	_, _ = m.db.Exec(`ALTER TABLE subscriptions ADD COLUMN raw_json TEXT NOT NULL DEFAULT ''`)
	return m.initUpdatesSchemaV2()
}

func (m *Manager) loadSubscriptionsFromDiskLocked() error {
	if m.subsPath == "" {
		return nil
	}
	raw, err := os.ReadFile(m.subsPath)
	if err != nil {
		if os.IsNotExist(err) {
			m.rules = nil
			m.ensureDefaultRuleLocked()
			return nil
		}
		return err
	}

	var rawVersion struct {
		Version *int `json:"version"`
	}
	needsMigration := false
	if err := json.Unmarshal(raw, &rawVersion); err == nil {
		if rawVersion.Version != nil && *rawVersion.Version > CurrentSchemaVersion {
			return &storage.NewerDataError{
				File:      filepath.Base(m.subsPath),
				Found:     *rawVersion.Version,
				Supported: CurrentSchemaVersion,
			}
		}
		needsMigration = rawVersion.Version == nil || *rawVersion.Version < CurrentSchemaVersion
	}

	prevRules := append([]CacheRule(nil), m.rules...)
	m.rules = nil
	m.ensureDefaultRuleLocked()

	var sf subscriptionsFile
	if err := json.Unmarshal(raw, &sf); err != nil {
		// Corrupt subscriptions.json: rename and keep, set corruptRecovered = true (data-and-cache.md §1).
		corruptPath := fmt.Sprintf("%s.corrupt-%s", m.subsPath, m.nowFunc().UTC().Format("20060102-150405"))
		if err := os.Rename(m.subsPath, corruptPath); err != nil {
			m.rules = prevRules
			return err
		}
		m.corruptRecovered = true
		_, err := m.db.Exec(`DELETE FROM subscriptions`)
		return err
	}
	m.rules = sf.CacheRules
	m.ensureDefaultRuleLocked()

	// Enforce global uniqueness of <site>:<uid> across all subscriptions (keep first, warn once).
	seenAccounts := make(map[string]bool)
	tx, err := m.db.Begin()
	if err != nil {
		m.rules = prevRules
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM subscriptions`); err != nil {
		m.rules = prevRules
		return err
	}
	migratedSubs := make([]Subscription, 0, len(sf.Subscriptions))
	for _, sub := range sf.Subscriptions {
		hadAccounts := len(sub.Accounts) > 0
		var cleanAccs []AccountRef
		for _, acc := range sub.Accounts {
			normAcc, ok := normalizeSubscriptionAccountForLoad(acc)
			if !ok {
				continue
			}
			key := normAcc.Site + ":" + normAcc.UID
			if seenAccounts[key] {
				m.duplicateAccountWarn = true
				continue
			}
			seenAccounts[key] = true
			cleanAccs = append(cleanAccs, normAcc)
			if len(cleanAccs) >= 20 {
				break
			}
		}
		sub.Accounts = cleanAccs
		if hadAccounts && len(cleanAccs) == 0 {
			sub.Mid = 0
		}
		syncSubFields(&sub, m.nowFunc())
		m.bindRuleLocked(&sub)
		if err := upsertSubscriptionRow(tx, sub); err != nil {
			m.rules = prevRules
			return err
		}
		migratedSubs = append(migratedSubs, sub)
	}
	if needsMigration {
		out := subscriptionsFile{
			Version:       CurrentSchemaVersion,
			Subscriptions: migratedSubs,
			CacheRules:    m.rules,
		}
		if err := storage.WriteMigratedJSONAtomic(m.subsPath, out, 0o600); err != nil {
			m.rules = prevRules
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		m.rules = prevRules
		return err
	}
	return nil
}

func (m *Manager) writeSubscriptionsToDiskLocked() (writeErr error) {
	if m.subsPath == "" {
		return nil
	}
	subs, err := m.listLocked()
	if err != nil {
		return err
	}
	// Every subscription names the shared rule matching its settings
	// (new ones, imports and older per-subscription rules alike).
	for i := range subs {
		if m.bindRuleLocked(&subs[i]) {
			if err := m.upsertRowLocked(subs[i]); err != nil {
				return err
			}
		}
	}
	sf := subscriptionsFile{
		Version:       CurrentSchemaVersion,
		Subscriptions: subs,
		CacheRules:    m.rules,
	}
	data, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	// Save previous valid subscriptions.json to subscriptions.json.bak before writing (data-and-cache.md §1).
	if prev, err := os.ReadFile(m.subsPath); err == nil && len(prev) > 0 {
		var check subscriptionsFile
		if json.Unmarshal(prev, &check) == nil {
			if err := storage.WriteFileAtomic(m.subsPath+".bak", prev, 0o600); err != nil {
				return err
			}
		}
	}
	return storage.WriteFileAtomic(m.subsPath, data, 0o600)
}

// RestoreFromBackup restores subscriptions.json from subscriptions.json.bak (data-and-cache.md §1).
func (m *Manager) RestoreFromBackup() ([]Subscription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.subsPath == "" {
		return nil, fmt.Errorf("subscription: no subscriptions.json path configured")
	}
	bakBytes, err := os.ReadFile(m.subsPath + ".bak")
	if err != nil {
		return nil, fmt.Errorf("subscription: read subscriptions.json.bak: %w", err)
	}
	var sf subscriptionsFile
	if err := json.Unmarshal(bakBytes, &sf); err != nil {
		return nil, fmt.Errorf("subscription: invalid subscriptions.json.bak: %w", err)
	}
	if err := storage.WriteFileAtomic(m.subsPath, bakBytes, 0o600); err != nil {
		return nil, err
	}
	m.corruptRecovered = false
	if err := m.loadSubscriptionsFromDiskLocked(); err != nil {
		return nil, err
	}
	return m.listLocked()
}

func (m *Manager) cleanupExpiredUnlinkedAccountsLocked() {
	subs, err := m.listLocked()
	if err != nil {
		return
	}
	activeKeys := make(map[string]bool)
	for _, s := range subs {
		for _, a := range s.Accounts {
			activeKeys[a.Site+":"+a.UID] = true
		}
		if s.Mid > 0 {
			activeKeys[fmt.Sprintf("bilibili:%d", s.Mid)] = true
		}
	}

	rows, err := m.updatesDB.Query(`SELECT site, uid, unlinked_at, last_checked_at FROM account_state`)
	if err != nil {
		return
	}
	type candidate struct {
		site, uid, ts string
	}
	var toDelete []candidate
	now := m.nowFunc().UTC()
	for rows.Next() {
		var site, uid, unlinkedAt, lastCheckedAt string
		if err := rows.Scan(&site, &uid, &unlinkedAt, &lastCheckedAt); err != nil {
			continue
		}
		if activeKeys[site+":"+uid] {
			continue
		}
		refTs := unlinkedAt
		if refTs == "" {
			refTs = lastCheckedAt
		}
		if refTs != "" {
			if t, pErr := time.Parse(time.RFC3339, refTs); pErr == nil && now.Sub(t) > 30*24*time.Hour {
				toDelete = append(toDelete, candidate{site: site, uid: uid})
			}
		}
	}
	_ = rows.Close()

	for _, c := range toDelete {
		_, _ = m.updatesDB.Exec(`DELETE FROM update_item WHERE site = ? AND uid = ?`, c.site, c.uid)
		_, _ = m.updatesDB.Exec(`DELETE FROM account_baseline_pinned WHERE site = ? AND uid = ?`, c.site, c.uid)
		_, _ = m.updatesDB.Exec(`DELETE FROM account_state WHERE site = ? AND uid = ?`, c.site, c.uid)
	}
}

func syncSubFields(s *Subscription, now time.Time) {
	nowStr := now.UTC().Format(time.RFC3339)
	if utf8.RuneCountInString(s.Name) > 64 {
		runes := []rune(s.Name)
		s.Name = string(runes[:64])
	}
	if utf8.RuneCountInString(s.Note) > 500 {
		runes := []rune(s.Note)
		s.Note = string(runes[:500])
	}
	if s.Site == "" {
		s.Site = "bilibili"
	}
	if s.CreatedAt == "" {
		s.CreatedAt = nowStr
	}
	if s.UpdatedAt == "" {
		s.UpdatedAt = nowStr
	}
	for i := range s.Accounts {
		if s.Accounts[i].Site == "" {
			s.Accounts[i].Site = "bilibili"
		}
		if s.Accounts[i].Site == "bilibili" {
			if id, err := strconv.ParseInt(s.Accounts[i].UID, 10, 64); err == nil && id > 0 {
				s.Accounts[i].UID = strconv.FormatInt(id, 10)
			}
		}
	}

	// Sync Accounts <-> legacy Mid/Uname/Face
	if len(s.Accounts) == 0 && s.Mid > 0 {
		uidStr := strconv.FormatInt(s.Mid, 10)
		s.Accounts = []AccountRef{{
			Site:    s.Site,
			UID:     uidStr,
			Name:    firstNonEmptyStr(s.Uname, s.Name),
			Face:    s.Face,
			AddedAt: s.CreatedAt,
		}}
	}
	if len(s.Accounts) > 0 {
		primary := &s.Accounts[0]
		if primary.Site == "" {
			primary.Site = "bilibili"
		}
		if primary.AddedAt == "" {
			primary.AddedAt = s.CreatedAt
		}
		s.Site = primary.Site
		if primary.Site == "bilibili" {
			s.Mid, _ = strconv.ParseInt(primary.UID, 10, 64)
		} else {
			s.Mid = 0
		}
		s.Uname, s.Face = primary.Name, primary.Face
		avatarLinked := false
		for _, account := range s.Accounts {
			avatarLinked = avatarLinked || account.Site == s.Avatar.Site && account.UID == s.Avatar.UID
		}
		if !avatarLinked {
			s.Avatar = AvatarRef{Site: primary.Site, UID: primary.UID}
		}
	} else {
		s.Avatar = AvatarRef{}
	}
	if s.Name == "" {
		if len(s.Accounts) > 0 && s.Accounts[0].Site == "douyin" {
			s.Name = firstNonEmptyStr(s.Uname, firstNonEmptyStr(s.Accounts[0].UniqueID, s.Accounts[0].UID))
		} else {
			s.Name = firstNonEmptyStr(s.Uname, fmt.Sprintf("UID %d", s.Mid))
		}
	}
	if s.Uname == "" {
		s.Uname = s.Name
	}
	if s.ID == "" {
		if s.SubID != "" && !strings.Contains(s.SubID, ":") {
			s.ID = s.SubID
		} else {
			var rnd [16]byte
			_, _ = rand.Read(rnd[:])
			s.ID = hex.EncodeToString(rnd[:])
		}
	}
	if s.SubID == "" {
		if s.Mid > 0 {
			s.SubID = fmt.Sprintf("%s:%d", s.Site, s.Mid)
		} else {
			s.SubID = s.ID
		}
	}
	if s.Scope == "" {
		s.Scope = "both"
	}
	if s.AutoCache == "" {
		if s.Rules.AutoCache.Enabled {
			s.AutoCache = "dynamic"
		} else {
			s.AutoCache = "inherit"
		}
	}
	if !s.Enabled && s.Rules.ScheduledCheck {
		s.Enabled = true
	} else if s.Enabled && !s.Rules.ScheduledCheck {
		s.Rules.ScheduledCheck = true
	}
	if s.LiveAutoRecord && !s.Rules.AutoRecord {
		s.Rules.AutoRecord = true
	} else if s.Rules.AutoRecord && !s.LiveAutoRecord {
		s.LiveAutoRecord = true
	}
	if len(s.Rules.AutoCache.Types) == 0 {
		s.Rules.AutoCache.Types = []string{"video", "draw"}
	}
}

// Start launches the background schedule loop (and optional checkOnStartup).
func (m *Manager) Start(parentCtx context.Context) {
	m.mu.Lock()
	if m.schedCancel != nil {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parentCtx)
	m.schedCancel = cancel
	checkStartup := m.subSettings.Enabled && m.subSettings.CheckOnStartup
	m.mu.Unlock()

	m.wg.Add(1)
	go m.scheduleLoop(ctx, checkStartup)
}

// Stop stops the background schedule loop and closes updates.db if owned.
func (m *Manager) Stop() {
	m.mu.Lock()
	cancel := m.schedCancel
	m.schedCancel = nil
	if m.checkCancel != nil {
		m.checkCancel()
	}
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.wg.Wait()
}

// CancelCheck cancels an in-flight subscription check round after the current request finishes.
func (m *Manager) CancelCheck() {
	m.mu.Lock()
	if m.checkCancel != nil {
		m.checkCancel()
	}
	m.mu.Unlock()
}

// UpdateSettings updates the subscription schedule configuration and wakes the timer loop.
func (m *Manager) UpdateSettings(s settings.SubscriptionSettings) {
	m.mu.Lock()
	m.subSettings = s
	m.mu.Unlock()
	select {
	case m.schedWake <- struct{}{}:
	default:
	}
}

func (m *Manager) scheduleLoop(ctx context.Context, checkOnStartup bool) {
	defer m.wg.Done()
	if checkOnStartup && m.hasAnySiteAuth() {
		_, _ = m.RunScheduledRound(ctx)
	}

	for {
		m.mu.Lock()
		cfg := m.subSettings
		now := m.nowFunc()
		m.mu.Unlock()

		nextTime, ok := NextRunTime(cfg, now)
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-m.schedWake:
				continue
			}
		}

		waitDur := nextTime.Sub(now)
		if waitDur < time.Second {
			waitDur = time.Second
		}
		timer := time.NewTimer(waitDur)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-m.schedWake:
			timer.Stop()
			continue
		case <-timer.C:
			if m.hasAnySiteAuth() {
				_, _ = m.RunScheduledRound(ctx)
			}
		}
	}
}

// Create creates a new Subscription with one or more accounts, enforcing <= 20 accounts and global <site>:<uid> uniqueness (subscriptions.md §1).
func (m *Manager) Create(name, note string, accounts []AccountRef, rules SubRules) (result Subscription, resultErr error) {
	if err := validateSubscriptionInput(name, note, accounts); err != nil {
		return Subscription{}, err
	}
	if strings.TrimSpace(name) == "" {
		return Subscription{}, fmt.Errorf("subscription: name is required")
	}
	if len(accounts) > 20 {
		return Subscription{}, fmt.Errorf("subscription: a subscription cannot have more than 20 accounts")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	before, snapshotErr := m.listLocked()
	if snapshotErr != nil {
		return Subscription{}, snapshotErr
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, m.restoreSubscriptionViewLocked(before))
		}
	}()

	normAccounts := make([]AccountRef, 0, len(accounts))
	for _, acc := range accounts {
		norm, err := NormalizeAndValidateAccount(acc)
		if err != nil {
			return Subscription{}, err
		}
		if owner, ok := m.ownerOfLocked(norm.Site, norm.UID); ok {
			return owner, fmt.Errorf("%w: %s:%s is owned by %s (%s)", ErrAccountConflict, norm.Site, norm.UID, owner.Name, owner.ID)
		}
		normAccounts = append(normAccounts, norm)
	}

	now := m.nowFunc()
	var rnd [16]byte
	_, _ = rand.Read(rnd[:])
	sub := Subscription{
		ID:        hex.EncodeToString(rnd[:]),
		Name:      strings.TrimSpace(name),
		Note:      strings.TrimSpace(note),
		CreatedAt: now.UTC().Format(time.RFC3339),
		Accounts:  normAccounts,
		Rules:     rules,
		Enabled:   rules.ScheduledCheck,
	}
	if sub.Rules.AutoCache.Enabled && sub.Rules.AutoCache.Since == "" {
		sub.Rules.AutoCache.Since = now.UTC().Format(time.RFC3339)
	}
	syncSubFields(&sub, now)
	if err := m.upsertRowLocked(sub); err != nil {
		return Subscription{}, err
	}
	if err := m.writeSubscriptionsToDiskLocked(); err != nil {
		return Subscription{}, err
	}
	return sub, nil
}

// OwnerOf returns the Subscription that currently owns <site>:<uid>, if any.
func (m *Manager) OwnerOf(site, uid string) (Subscription, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ownerOfLocked(normalizeSchedulerSite(site), uid)
}

func (m *Manager) ownerOfLocked(site, uid string) (Subscription, bool) {
	site = normalizeSchedulerSite(site)
	if site == "bilibili" {
		if id, err := strconv.ParseInt(uid, 10, 64); err == nil && id > 0 {
			uid = strconv.FormatInt(id, 10)
		}
	}
	subs, err := m.listLocked()
	if err != nil {
		return Subscription{}, false
	}
	for _, s := range subs {
		for _, a := range s.Accounts {
			if a.Site == site && a.UID == uid {
				return s, true
			}
		}
		if site == "bilibili" && s.Mid > 0 && strconv.FormatInt(s.Mid, 10) == uid {
			return s, true
		}
	}
	return Subscription{}, false
}

// Link associates <site>:<uid> with subscription subID. If the account already belongs to another subscription, returns (ownerSub, ErrAccountConflict).
func (m *Manager) Link(subID, site, uid, name, face string) (result Subscription, resultErr error) {
	return m.LinkAccount(subID, AccountRef{
		Site: site,
		UID:  uid,
		Name: name,
		Face: face,
	})
}

// Unlink removes <site>:<uid> from subscription subID while preserving its account_state and update_item rows in updates.db (subscriptions.md §2).
func (m *Manager) Unlink(subID, site, uid string) (result Subscription, resultErr error) {
	site = normalizeSchedulerSite(site)
	if site == "bilibili" {
		if id, err := strconv.ParseInt(uid, 10, 64); err == nil && id > 0 {
			uid = strconv.FormatInt(id, 10)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	before, snapshotErr := m.listLocked()
	if snapshotErr != nil {
		return Subscription{}, snapshotErr
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, m.restoreSubscriptionViewLocked(before))
		}
	}()

	target, ok, err := m.findByIDOrSubIDLocked(subID)
	if err != nil || !ok {
		return Subscription{}, fmt.Errorf("subscription: %s not found", subID)
	}
	var kept []AccountRef
	for _, a := range target.Accounts {
		if a.Site == site && a.UID == uid {
			continue
		}
		kept = append(kept, a)
	}
	target.Accounts = kept
	if len(kept) > 0 && kept[0].Site == "bilibili" {
		if parsedMid, pErr := strconv.ParseInt(kept[0].UID, 10, 64); pErr == nil {
			target.Mid = parsedMid
		}
	} else {
		target.Mid = 0
	}
	nowStr := m.nowFunc().UTC().Format(time.RFC3339)
	_, _ = m.updatesDB.Exec(`UPDATE account_state SET unlinked_at = ? WHERE site = ? AND uid = ?`, nowStr, site, uid)
	syncSubFields(&target, m.nowFunc())
	if err := m.upsertRowLocked(target); err != nil {
		return Subscription{}, err
	}
	if err := m.writeSubscriptionsToDiskLocked(); err != nil {
		return Subscription{}, err
	}
	return target, nil
}

// Move atomically moves <site>:<uid> from its current owner subscription to toSubID under a single lock and single file write.
func (m *Manager) Move(site, uid, toSubID string) (result Subscription, resultErr error) {
	normAcc, err := NormalizeAndValidateAccount(AccountRef{Site: site, UID: uid})
	if err != nil {
		return Subscription{}, err
	}
	site, uid = normAcc.Site, normAcc.UID
	m.mu.Lock()
	defer m.mu.Unlock()
	before, snapshotErr := m.listLocked()
	if snapshotErr != nil {
		return Subscription{}, snapshotErr
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, m.restoreSubscriptionViewLocked(before))
		}
	}()

	dest, ok, err := m.findByIDOrSubIDLocked(toSubID)
	if err != nil || !ok {
		return Subscription{}, fmt.Errorf("subscription: destination %s not found", toSubID)
	}
	for _, account := range dest.Accounts {
		if account.Site == site && account.UID == uid {
			return dest, nil
		}
	}
	if len(dest.Accounts) >= 20 {
		return Subscription{}, fmt.Errorf("subscription: destination subscription already has 20 accounts")
	}

	var movedAcc AccountRef
	foundAcc := false
	owner, owned := m.ownerOfLocked(site, uid)
	if !owned {
		return Subscription{}, fmt.Errorf("subscription: 待迁移账号尚未关联订阅")
	}
	if owner.ID != dest.ID && owner.SubID != dest.SubID {
		var kept []AccountRef
		for _, a := range owner.Accounts {
			if a.Site == site && a.UID == uid {
				movedAcc = a
				foundAcc = true
				continue
			}
			kept = append(kept, a)
		}
		owner.Accounts = kept
		if len(kept) > 0 && kept[0].Site == "bilibili" {
			if parsedMid, pErr := strconv.ParseInt(kept[0].UID, 10, 64); pErr == nil {
				owner.Mid = parsedMid
			}
		} else {
			owner.Mid = 0
		}
		syncSubFields(&owner, m.nowFunc())
		if err := m.upsertRowLocked(owner); err != nil {
			return Subscription{}, err
		}
	}

	if !foundAcc {
		movedAcc = AccountRef{
			Site:    site,
			UID:     uid,
			AddedAt: m.nowFunc().UTC().Format(time.RFC3339),
		}
	}
	alreadyInDest := false
	for _, a := range dest.Accounts {
		if a.Site == site && a.UID == uid {
			alreadyInDest = true
			break
		}
	}
	if !alreadyInDest {
		dest.Accounts = append(dest.Accounts, movedAcc)
	}
	syncSubFields(&dest, m.nowFunc())
	if err := m.upsertRowLocked(dest); err != nil {
		return Subscription{}, err
	}
	if err := m.writeSubscriptionsToDiskLocked(); err != nil {
		return Subscription{}, err
	}
	return dest, nil
}

// SetRules updates the automation rules for subID, setting or clearing Rules.AutoCache.Since per subscriptions.md §4.
func (m *Manager) SetRules(subID string, rules SubRules) (result Subscription, resultErr error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	before, snapshotErr := m.listLocked()
	if snapshotErr != nil {
		return Subscription{}, snapshotErr
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, m.restoreSubscriptionViewLocked(before))
		}
	}()

	sub, ok, err := m.findByIDOrSubIDLocked(subID)
	if err != nil || !ok {
		return Subscription{}, fmt.Errorf("subscription: %s not found", subID)
	}
	m.applyRulesLocked(&sub, rules)
	if err := m.upsertRowLocked(sub); err != nil {
		return Subscription{}, err
	}
	if err := m.writeSubscriptionsToDiskLocked(); err != nil {
		return Subscription{}, err
	}
	return sub, nil
}

// Upsert creates or updates a creator subscription idempotently.
func (m *Manager) Upsert(in Subscription) (result Subscription, resultErr error) {
	if err := validateSubscriptionInput(in.Name, in.Note, in.Accounts); err != nil {
		return Subscription{}, err
	}
	for i := range in.Accounts {
		norm, err := NormalizeAndValidateAccount(in.Accounts[i])
		if err != nil {
			return Subscription{}, err
		}
		in.Accounts[i] = norm
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	before, snapshotErr := m.listLocked()
	if snapshotErr != nil {
		return Subscription{}, snapshotErr
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, m.restoreSubscriptionViewLocked(before))
		}
	}()

	if in.ID == "" && in.Mid <= 0 && len(in.Accounts) == 0 {
		return Subscription{}, fmt.Errorf("subscription: invalid creator mid %d", in.Mid)
	}
	now := m.nowFunc()
	lookupKey := in.SubID
	if lookupKey == "" {
		if in.ID != "" {
			lookupKey = in.ID
		} else if in.Mid > 0 {
			lookupKey = fmt.Sprintf("bilibili:%d", in.Mid)
		}
	}
	if lookupKey != "" {
		if existing, ok, _ := m.findByIDOrSubIDLocked(lookupKey); ok {
			if in.ID == "" {
				in.ID = existing.ID
			}
			if in.CreatedAt == "" {
				in.CreatedAt = existing.CreatedAt
			}
			if len(in.Accounts) == 0 {
				in.Accounts = existing.Accounts
			}
			if in.Rules.AutoCache.Since == "" && existing.Rules.AutoCache.Since != "" {
				in.Rules = existing.Rules
			}
		}
	}
	syncSubFields(&in, now)
	for _, account := range in.Accounts {
		if owner, exists := m.ownerOfLocked(firstNonEmptyStr(account.Site, "bilibili"), account.UID); exists && owner.ID != in.ID {
			return owner, ErrAccountConflict
		}
	}

	in.UpdatedAt = now.UTC().Format(time.RFC3339)
	if err := m.upsertRowLocked(in); err != nil {
		return Subscription{}, err
	}
	if err := m.writeSubscriptionsToDiskLocked(); err != nil {
		return Subscription{}, err
	}
	sub, _, err := m.getLocked(in.SubID)
	return sub, err
}

func (m *Manager) upsertRowLocked(in Subscription) error {
	return upsertSubscriptionRow(m.db, in)
}

func upsertSubscriptionRow(executor subscriptionExecutor, in Subscription) error {
	rawBytes, _ := json.Marshal(in)
	_, err := executor.Exec(`
		INSERT INTO subscriptions (
			sub_id, site, mid, uname, face, sign, enabled, scope, auto_cache,
			live_notify, live_auto_record, room_id,
			last_video_bvid, last_video_pub_ts, last_dynamic_id, last_dynamic_pub_ts,
			last_checked_at, last_success_at, consecutive_failures, last_error,
			raw_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(sub_id) DO UPDATE SET
			mid = excluded.mid,
			uname = CASE WHEN excluded.uname != '' THEN excluded.uname ELSE subscriptions.uname END,
			face = CASE WHEN excluded.face != '' THEN excluded.face ELSE subscriptions.face END,
			sign = CASE WHEN excluded.sign != '' THEN excluded.sign ELSE subscriptions.sign END,
			enabled = excluded.enabled,
			scope = excluded.scope,
			auto_cache = excluded.auto_cache,
			live_notify = excluded.live_notify,
			live_auto_record = excluded.live_auto_record,
			room_id = CASE WHEN excluded.room_id > 0 THEN excluded.room_id ELSE subscriptions.room_id END,
			last_video_bvid = CASE WHEN excluded.last_video_bvid != '' THEN excluded.last_video_bvid ELSE subscriptions.last_video_bvid END,
			last_video_pub_ts = CASE WHEN excluded.last_video_pub_ts > 0 THEN excluded.last_video_pub_ts ELSE subscriptions.last_video_pub_ts END,
			last_dynamic_id = CASE WHEN excluded.last_dynamic_id != '' THEN excluded.last_dynamic_id ELSE subscriptions.last_dynamic_id END,
			last_dynamic_pub_ts = CASE WHEN excluded.last_dynamic_pub_ts > 0 THEN excluded.last_dynamic_pub_ts ELSE subscriptions.last_dynamic_pub_ts END,
			last_checked_at = CASE WHEN excluded.last_checked_at != '' THEN excluded.last_checked_at ELSE subscriptions.last_checked_at END,
			last_success_at = CASE WHEN excluded.last_success_at != '' THEN excluded.last_success_at ELSE subscriptions.last_success_at END,
			consecutive_failures = excluded.consecutive_failures,
			last_error = excluded.last_error,
			raw_json = excluded.raw_json,
			updated_at = excluded.updated_at
	`,
		in.SubID, in.Site, in.Mid, in.Uname, in.Face, in.Sign,
		boolToInt(in.Enabled), in.Scope, in.AutoCache,
		boolToInt(in.LiveNotify), boolToInt(in.LiveAutoRecord), in.RoomID,
		in.LastVideoBVID, in.LastVideoPubTs, in.LastDynamicID, in.LastDynamicPubTs,
		in.LastCheckedAt, in.LastSuccessAt, in.ConsecutiveFailures, in.LastError,
		string(rawBytes), in.CreatedAt, in.UpdatedAt,
	)
	return err
}

// ImportCreators idempotently imports a batch of creators without overwriting existing watermarks.
func (m *Manager) ImportCreators(users []bilibili.FollowingUser, scope, autoCache string, liveNotify, liveAutoRecord bool) (int, error) {
	if scope == "" {
		scope = "both"
	}
	if autoCache == "" {
		autoCache = "inherit"
	}
	count := 0
	for _, u := range users {
		if u.Mid <= 0 {
			continue
		}
		existing, ok, _ := m.GetByMid(u.Mid)
		if ok {
			existing.Uname = firstNonEmptyStr(u.Uname, existing.Uname)
			existing.Name = existing.Uname
			existing.Face = firstNonEmptyStr(u.Face, existing.Face)
			existing.Sign = firstNonEmptyStr(u.Sign, existing.Sign)
			if len(existing.Accounts) > 0 {
				existing.Accounts[0].Name = existing.Uname
				existing.Accounts[0].Face = existing.Face
			}
			if _, err := m.Upsert(existing); err != nil {
				return count, err
			}
			count++
			continue
		}
		_, err := m.Upsert(Subscription{
			Site:           "bilibili",
			Mid:            u.Mid,
			Name:           firstNonEmptyStr(u.Uname, fmt.Sprintf("UID %d", u.Mid)),
			Uname:          firstNonEmptyStr(u.Uname, fmt.Sprintf("UID %d", u.Mid)),
			Face:           u.Face,
			Sign:           u.Sign,
			Enabled:        true,
			Scope:          scope,
			AutoCache:      autoCache,
			LiveNotify:     liveNotify,
			LiveAutoRecord: liveAutoRecord,
		})
		if err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// Delete removes a subscription by ID, subID ("bilibili:12345"), or numeric mid string ("12345").
func (m *Manager) Delete(subIDOrMid string) (resultErr error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	before, snapshotErr := m.listLocked()
	if snapshotErr != nil {
		return snapshotErr
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, m.restoreSubscriptionViewLocked(before))
		}
	}()

	sub, ok, _ := m.findByIDOrSubIDLocked(subIDOrMid)
	if ok {
		nowStr := m.nowFunc().UTC().Format(time.RFC3339)
		for _, a := range sub.Accounts {
			_, _ = m.updatesDB.Exec(`UPDATE account_state SET unlinked_at = ? WHERE site = ? AND uid = ?`, nowStr, a.Site, a.UID)
		}
		_, err := m.db.Exec(`DELETE FROM subscriptions WHERE sub_id = ?`, sub.SubID)
		if err := m.writeSubscriptionsToDiskLocked(); err != nil {
			return err
		}
		return err
	}
	mid, _ := strconv.ParseInt(strings.TrimPrefix(subIDOrMid, "bilibili:"), 10, 64)
	_, err := m.db.Exec(`DELETE FROM subscriptions WHERE sub_id = ? OR mid = ?`, subIDOrMid, mid)
	if err := m.writeSubscriptionsToDiskLocked(); err != nil {
		return err
	}
	return err
}

// List returns all subscriptions ordered by created_at ASC.
func (m *Manager) List() ([]Subscription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listLocked()
}

func (m *Manager) listLocked() ([]Subscription, error) {
	rows, err := m.db.Query(`
		SELECT
			sub_id, site, mid, uname, face, sign, enabled, scope, auto_cache,
			live_notify, live_auto_record, room_id,
			last_video_bvid, last_video_pub_ts, last_dynamic_id, last_dynamic_pub_ts,
			last_checked_at, last_success_at, consecutive_failures, last_error,
			raw_json, created_at, updated_at
		FROM subscriptions
		ORDER BY rowid ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Subscription
	for rows.Next() {
		s, err := scanSub(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if m.updatesDB != nil && len(out) > 0 {
		uRows, err := m.updatesDB.Query(`SELECT site, uid, MAX(pub_ts) FROM update_item WHERE baseline_snapshot = 0 GROUP BY site, uid`)
		if err != nil {
			return nil, fmt.Errorf("读取最近更新失败：%w", err)
		}
		defer uRows.Close()
		latestByAcc := make(map[string]int64)
		for uRows.Next() {
			var site, uid string
			var maxTs int64
			if err := uRows.Scan(&site, &uid, &maxTs); err != nil {
				return nil, fmt.Errorf("读取最近更新失败：%w", err)
			}
			latestByAcc[site+":"+uid] = maxTs
		}
		if err := uRows.Err(); err != nil {
			return nil, fmt.Errorf("读取最近更新失败：%w", err)
		}
		for i := range out {
			var maxSubTs int64
			for _, acc := range out[i].Accounts {
				if ts := latestByAcc[acc.Site+":"+acc.UID]; ts > maxSubTs {
					maxSubTs = ts
				}
			}
			if out[i].Mid > 0 {
				if ts := latestByAcc[fmt.Sprintf("bilibili:%d", out[i].Mid)]; ts > maxSubTs {
					maxSubTs = ts
				}
			}
			out[i].LatestUpdateTs = maxSubTs
		}
	}
	return out, nil
}

// GetByMid retrieves a subscription by creator Mid.
func (m *Manager) GetByMid(mid int64) (Subscription, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	uidStr := strconv.FormatInt(mid, 10)
	if owner, ok := m.ownerOfLocked("bilibili", uidStr); ok {
		return owner, true, nil
	}
	return m.getLocked(fmt.Sprintf("bilibili:%d", mid))
}

func (m *Manager) findByIDOrSubIDLocked(idOrSubID string) (Subscription, bool, error) {
	if s, ok, err := m.getLocked(idOrSubID); err == nil && ok {
		return s, true, nil
	}
	subs, err := m.listLocked()
	if err != nil {
		return Subscription{}, false, err
	}
	for _, s := range subs {
		if s.ID == idOrSubID || s.SubID == idOrSubID || (s.Mid > 0 && strconv.FormatInt(s.Mid, 10) == strings.TrimPrefix(idOrSubID, "bilibili:")) {
			return s, true, nil
		}
	}
	return Subscription{}, false, nil
}

func (m *Manager) getLocked(subID string) (Subscription, bool, error) {
	row := m.db.QueryRow(`
		SELECT
			sub_id, site, mid, uname, face, sign, enabled, scope, auto_cache,
			live_notify, live_auto_record, room_id,
			last_video_bvid, last_video_pub_ts, last_dynamic_id, last_dynamic_pub_ts,
			last_checked_at, last_success_at, consecutive_failures, last_error,
			raw_json, created_at, updated_at
		FROM subscriptions
		WHERE sub_id = ?
	`, subID)
	s, err := scanSub(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return Subscription{}, false, nil
		}
		return Subscription{}, false, err
	}
	return s, true, nil
}

// ListUpdates returns discovered update items for a subscription (or all subscriptions if subID == ""), newest first.
func (m *Manager) ListUpdates(subID string) ([]UpdateItem, error) {
	return m.ListUpdatesPage(subID, 200, 0)
}

func (m *Manager) ListUpdatesPage(subID string, limit, offset int) ([]UpdateItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var allowed map[string]bool
	if subID != "" {
		sub, ok, err := m.findByIDOrSubIDLocked(subID)
		if err != nil || !ok {
			return nil, err
		}
		allowed = make(map[string]bool, len(sub.Accounts))
		for _, a := range sub.Accounts {
			allowed[a.Site+":"+a.UID] = true
		}
	}

	clauses := []string{"baseline_snapshot = 0"}
	var args []any
	if allowed != nil {
		if len(allowed) == 0 {
			return nil, nil
		}
		var members []string
		for key := range allowed {
			pair := strings.SplitN(key, ":", 2)
			members = append(members, "(site = ? AND uid = ?)")
			args = append(args, pair[0], pair[1])
		}
		clauses = append(clauses, "("+strings.Join(members, " OR ")+")")
	}
	q := `SELECT site, uid, dyn_id, pub_ts, type, snapshot, discovered_at, auto_task_id FROM update_item`
	if len(clauses) > 0 {
		q += " WHERE " + strings.Join(clauses, " AND ")
	}
	if limit <= 0 {
		limit = 200
	}
	q += " ORDER BY pub_ts DESC, dyn_id DESC LIMIT ? OFFSET ?"
	args = append(args, limit, max(0, offset))
	rows, err := m.updatesDB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []UpdateItem
	for rows.Next() {
		var it UpdateItem
		if err := rows.Scan(&it.Site, &it.UID, &it.DynID, &it.PubTs, &it.Type, &it.Snapshot, &it.DiscoveredAt, &it.AutoTaskID); err != nil {
			return nil, err
		}
		if allowed != nil && !allowed[it.Site+":"+it.UID] {
			continue
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// GetAccountState returns the persisted AccountState in updates.db for (site, uid).
func (m *Manager) GetAccountState(site, uid string) (AccountState, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.getAccountStateLocked(site, uid)
}

func (m *Manager) getAccountStateLocked(site, uid string) (AccountState, bool, error) {
	if site == "" {
		site = "bilibili"
	}
	var st AccountState
	err := m.updatesDB.QueryRow(`
		SELECT site, uid, baseline_at, last_checked_at, last_kind, newest_id, newest_ts, note, unlinked_at
		FROM account_state WHERE site = ? AND uid = ?
	`, site, uid).Scan(&st.Site, &st.UID, &st.BaselineAt, &st.LastCheckedAt, &st.LastKind, &st.NewestID, &st.NewestTs, &st.Note, &st.UnlinkedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return AccountState{Site: site, UID: uid}, false, nil
		}
		return AccountState{}, false, err
	}
	return st, true, nil
}

func (m *Manager) saveAccountStateLocked(st AccountState) error {
	if st.Site == "" {
		st.Site = "bilibili"
	}
	_, err := m.updatesDB.Exec(`
		INSERT INTO account_state (site, uid, baseline_at, last_checked_at, last_kind, newest_id, newest_ts, note, unlinked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(site, uid) DO UPDATE SET
			baseline_at = excluded.baseline_at,
			last_checked_at = excluded.last_checked_at,
			last_kind = excluded.last_kind,
			newest_id = excluded.newest_id,
			newest_ts = excluded.newest_ts,
			note = excluded.note,
			unlinked_at = excluded.unlinked_at
	`, st.Site, st.UID, st.BaselineAt, st.LastCheckedAt, st.LastKind, st.NewestID, st.NewestTs, st.Note, st.UnlinkedAt)
	return err
}

// GetSchedulerState returns the persistent Bilibili scheduler backoff and token bucket state from updates.db.
func (m *Manager) GetSchedulerState() SchedulerState {
	return m.GetSiteSchedulerState("bilibili")
}

func (m *Manager) getSchedulerStateLocked() SchedulerState {
	return m.getSiteSchedulerStateLocked("bilibili")
}

func (m *Manager) saveSchedulerStateLocked(st SchedulerState) error {
	return m.saveSiteSchedulerStateLocked("bilibili", st)
}

// IsInBackoff reports whether scheduled Bilibili jobs (subscription checks, live checks, cookie renewal) are currently paused due to backoff (subscriptions.md §3).
func (m *Manager) IsInBackoff() (bool, time.Time) {
	return m.IsSiteInBackoff("bilibili")
}

// ConsumeQuotaTokens attempts to reserve n tokens from the shared 200 requests/hour Bilibili scheduled token bucket (subscriptions.md §3).
// If insufficient tokens remain in the current 1-hour window, returns false without consuming any tokens.
func (m *Manager) ConsumeQuotaTokens(n int) bool {
	return m.ConsumeSiteQuotaTokens("bilibili", n)
}

// RunScheduledRound executes a scheduled check round across enabled sites, respecting per-site backoff,
// per-site account caps (Bilibili <=30, Douyin <=20), per-site hourly token buckets (Bilibili 200/hr, Douyin 120/hr),
// per-site exponential backoff, and rule-based auto-cache.
func (m *Manager) RunScheduledRound(ctx context.Context) (CheckSummary, error) {
	m.mu.Lock()
	enabled := m.subSettings.Enabled
	m.mu.Unlock()
	if !enabled {
		return CheckSummary{}, nil
	}
	return m.runRoundInternal(site.WithAutomaticRequests(ctx, "subscription"), 0, true)
}

// CheckNow runs an immediate manual subscription check (for targetMid if > 0, or all enabled subscriptions).
// Enforces a single-flight lock so concurrent checks never overlap.
func (m *Manager) CheckNow(ctx context.Context, targetMid int64) (CheckSummary, error) {
	return m.runRoundInternal(ctx, targetMid, false)
}

func (m *Manager) runRoundInternal(ctx context.Context, targetMid int64, isScheduled bool) (CheckSummary, error) {
	return m.runRoundForSubscription(ctx, targetMid, isScheduled, "")
}

func (m *Manager) CheckSubscription(ctx context.Context, subID string) (CheckSummary, error) {
	if subID == "" {
		return CheckSummary{}, fmt.Errorf("subscription: no subscription selected")
	}
	return m.runRoundForSubscription(ctx, 0, false, subID)
}

func (m *Manager) runRoundForSubscription(ctx context.Context, targetMid int64, isScheduled bool, subID string) (CheckSummary, error) {
	return m.executeMultiSiteRound(ctx, targetMid, isScheduled, subID)
}

func (m *Manager) checkSingleAccount(
	ctx context.Context,
	sub *Subscription,
	acc AccountRef,
	accState *AccountState,
	isBaseline bool,
	globalAutoCache string,
) (newVideos int, newDynamics int, autoCacheTasks int, gapDetected bool, err error) {
	if acc.Site != "" && acc.Site != "bilibili" {
		return 0, 0, 0, false, nil
	}
	mid, _ := strconv.ParseInt(acc.UID, 10, 64)
	if mid <= 0 {
		mid = sub.Mid
	}

	checkVideos := sub.Scope == "video"
	checkDynamics := sub.Scope == "dynamic" || sub.Scope == "both" || sub.Scope == ""
	nowStr := m.nowFunc().UTC().Format(time.RFC3339)

	if checkVideos {
		vpage, vErr := m.client.UserVideos(ctx, mid, 1, 20, "pubdate", "")
		if vErr != nil {
			// If Scope=="both" and the test server only mocks UserDynamics, allow UserDynamics to proceed on 404.
			if sub.Scope == "video" || bilibili.IsKind(vErr, bilibili.ErrKindRiskControl) || bilibili.IsKind(vErr, bilibili.ErrKindRateLimited) || bilibili.IsKind(vErr, bilibili.ErrKindAuthRequired) || bilibili.IsKind(vErr, bilibili.ErrKindNetwork) {
				return 0, 0, 0, false, vErr
			}
			if !checkDynamics {
				return 0, 0, 0, false, vErr
			}
		} else {
			var maxPubTs = sub.LastVideoPubTs
			var maxBVID = sub.LastVideoBVID

			for _, item := range vpage.Items {
				updateAccountProfile(sub, acc, item.Author, "")
				isNew := item.PubTs > sub.LastVideoPubTs || (item.PubTs == sub.LastVideoPubTs && item.BVID != "" && item.BVID != sub.LastVideoBVID && sub.LastVideoPubTs > 0)
				if item.PubTs > maxPubTs {
					maxPubTs = item.PubTs
					maxBVID = item.BVID
				}

				if m.lib != nil {
					if err := m.lib.UpsertFeedEntry(library.FeedEntry{
						Site:         "bilibili",
						Kind:         "video",
						RawID:        item.BVID,
						Mid:          mid,
						AuthorName:   firstNonEmptyStr(item.Author, sub.Uname),
						AuthorFace:   sub.Face,
						Title:        item.Title,
						Summary:      item.Description,
						CoverURL:     item.Cover,
						CanonicalURL: "https://www.bilibili.com/video/" + item.BVID,
						PubTs:        item.PubTs,
					}); err != nil {
						return newVideos, newDynamics, 0, false, err
					}
				}

				if !isBaseline && isNew {
					snapJSON, _ := json.Marshal(item)
					inserted, err := m.insertUpdateItem(acc.Site, acc.UID, item.BVID, item.PubTs, "video", string(snapJSON), nowStr)
					if err != nil {
						return newVideos, newDynamics, 0, false, err
					}
					if inserted {
						newVideos++
					}
				}
			}
			sub.LastVideoPubTs = maxPubTs
			sub.LastVideoBVID = maxBVID
			if maxPubTs > accState.NewestTs {
				accState.NewestTs = maxPubTs
				accState.NewestID = maxBVID
			}
		}
	}

	if checkDynamics {
		maxPages := 3
		if isBaseline {
			maxPages = 1
		}
		offset := ""
		reachedBaseline := isBaseline
		var newestDynID = accState.NewestID
		var newestDynTs = accState.NewestTs

		for pageIdx := 0; pageIdx < maxPages; pageIdx++ {
			dpage, dErr := m.client.UserDynamics(ctx, mid, offset)
			if dErr != nil {
				return newVideos, newDynamics, autoCacheTasks, false, dErr
			}

			for _, dyn := range dpage.Items {
				updateAccountProfile(sub, acc, dyn.Author.Name, dyn.Author.Face)

				// Pinned dynamics do not trigger baseline stop condition (subscriptions.md §2).
				if !isBaseline && !dyn.Pinned {
					if (accState.NewestID != "" && dyn.ID == accState.NewestID) || (accState.NewestTs > 0 && dyn.PubTs <= accState.NewestTs) {
						reachedBaseline = true
						break
					}
				}

				if !dyn.Pinned {
					if dyn.PubTs > newestDynTs || (dyn.PubTs == newestDynTs && compareNumericID(dyn.ID, newestDynID) > 0) {
						newestDynTs = dyn.PubTs
						newestDynID = dyn.ID
					}
				}

				cover := ""
				if len(dyn.Images) > 0 {
					cover = dyn.Images[0].URL
				} else if len(dyn.Videos) > 0 {
					cover = dyn.Videos[0].Cover
				}
				if m.lib != nil {
					if err := m.lib.UpsertFeedEntry(library.FeedEntry{
						Site:         "bilibili",
						Kind:         "dynamic",
						RawID:        dyn.ID,
						Mid:          mid,
						AuthorName:   firstNonEmptyStr(dyn.Author.Name, sub.Uname),
						AuthorFace:   firstNonEmptyStr(dyn.Author.Face, sub.Face),
						Title:        dyn.Title,
						Summary:      dyn.Text,
						CoverURL:     cover,
						CanonicalURL: dyn.CanonicalURL,
						PubTs:        dyn.PubTs,
					}); err != nil {
						return newVideos, newDynamics, 0, false, err
					}
				}

				dynCategory := normalizeDynamicType(dyn.Type)
				snapJSON, _ := json.Marshal(dyn)
				// INV-12 (DEC-033): First check only establishes baseline; dynamics before baseline never enter update_item.
				if !isBaseline && !(dyn.Pinned && accState.NewestTs > 0 && dyn.PubTs <= accState.NewestTs) {
					inserted, err := m.insertUpdateItem(acc.Site, acc.UID, dyn.ID, dyn.PubTs, dynCategory, string(snapJSON), nowStr)
					if err != nil {
						return newVideos, newDynamics, 0, false, err
					}
					if inserted {
						newDynamics++
					}
				}
			}

			if reachedBaseline || !dpage.HasMore || dpage.Offset == "" {
				reachedBaseline = true
				break
			}
			offset = dpage.Offset
		}

		if !isBaseline && !reachedBaseline {
			gapDetected = true
		}
		if newestDynTs > 0 {
			sub.LastDynamicPubTs = newestDynTs
			sub.LastDynamicID = newestDynID
			accState.NewestTs = newestDynTs
			accState.NewestID = newestDynID
		}
	}

	m.enforceAccountItemCap(acc.Site, acc.UID, 200)
	return newVideos, newDynamics, autoCacheTasks, gapDetected, nil
}

func normalizeDynamicType(rawType string) string {
	switch strings.ToUpper(strings.TrimSpace(rawType)) {
	case "DYNAMIC_TYPE_AV", "VIDEO":
		return "video"
	case "DYNAMIC_TYPE_DRAW", "DRAW":
		return "draw"
	case "DYNAMIC_TYPE_WORD", "WORD":
		return "word"
	case "DYNAMIC_TYPE_FORWARD", "FORWARD":
		return "forward"
	case "DYNAMIC_TYPE_ARTICLE", "ARTICLE":
		return "article"
	default:
		return "other"
	}
}

func (m *Manager) insertUpdateItem(site, uid, dynID string, pubTs int64, typ, snapshot, discoveredAt string) (bool, error) {
	if site == "" {
		site = "bilibili"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	res, err := m.updatesDB.Exec(`
		INSERT OR IGNORE INTO update_item (site, uid, dyn_id, pub_ts, type, snapshot, discovered_at, auto_task_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, '')
	`, site, uid, dynID, pubTs, typ, snapshot, discoveredAt)
	if err != nil {
		return false, err
	}
	affected, _ := res.RowsAffected()
	return affected > 0, nil
}

func (m *Manager) setUpdateItemTaskID(site, uid, dynID, taskID string) error {
	if site == "" {
		site = "bilibili"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, err := m.updatesDB.Exec(`UPDATE update_item SET auto_task_id = ? WHERE site = ? AND uid = ? AND dyn_id = ?`, taskID, site, uid, dynID)
	return err
}

func (m *Manager) enforceAccountItemCap(site, uid string, maxItems int) {
	if site == "" {
		site = "bilibili"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var count int
	if err := m.updatesDB.QueryRow(`SELECT COUNT(*) FROM update_item WHERE site = ? AND uid = ? AND baseline_snapshot = 0`, site, uid).Scan(&count); err != nil || count <= maxItems {
		return
	}
	excess := count - maxItems
	_, _ = m.updatesDB.Exec(`
		DELETE FROM update_item
		WHERE rowid IN (
			SELECT rowid FROM update_item
			WHERE site = ? AND uid = ? AND baseline_snapshot = 0
			ORDER BY pub_ts ASC, dyn_id ASC
			LIMIT ?
		)
	`, site, uid, excess)
}

func (m *Manager) evaluateAutoCacheRules(ctx context.Context) (createdTasks int, totalBacklog int, result error) {
	return m.evaluateAutoCacheRulesForSites(ctx, nil)
}

func (m *Manager) saveSubState(sub Subscription) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.nowFunc()
	current, ok, err := m.findByIDOrSubIDLocked(sub.ID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	for i := range current.Accounts {
		for _, account := range sub.Accounts {
			if current.Accounts[i].Site == account.Site && current.Accounts[i].UID == account.UID {
				current.Accounts[i].Name, current.Accounts[i].Face = account.Name, account.Face
				if account.UniqueID != "" {
					current.Accounts[i].UniqueID = account.UniqueID
				}
				current.Accounts[i].WebRID = account.WebRID
			}
		}
	}
	current.LastCheckedAt, current.LastSuccessAt, current.LastError, current.ConsecutiveFailures = sub.LastCheckedAt, sub.LastSuccessAt, sub.LastError, sub.ConsecutiveFailures
	current.LastDynamicID, current.LastDynamicPubTs, current.LastVideoBVID, current.LastVideoPubTs = sub.LastDynamicID, sub.LastDynamicPubTs, sub.LastVideoBVID, sub.LastVideoPubTs
	current.UpdatedAt = now.UTC().Format(time.RFC3339)
	syncSubFields(&current, now)
	if err := m.upsertRowLocked(current); err != nil {
		return err
	}
	return m.writeSubscriptionsToDiskLocked()
}

func updateAccountProfile(sub *Subscription, account AccountRef, name, face string) {
	for i := range sub.Accounts {
		if sub.Accounts[i].Site == account.Site && sub.Accounts[i].UID == account.UID {
			if name != "" {
				sub.Accounts[i].Name = name
			}
			if face != "" {
				sub.Accounts[i].Face = face
			}
		}
	}
}

type subScanner interface {
	Scan(dest ...any) error
}

func scanSub(r subScanner) (Subscription, error) {
	var s Subscription
	var en, ln, lar int
	var rawJSON string
	err := r.Scan(
		&s.SubID, &s.Site, &s.Mid, &s.Uname, &s.Face, &s.Sign,
		&en, &s.Scope, &s.AutoCache, &ln, &lar, &s.RoomID,
		&s.LastVideoBVID, &s.LastVideoPubTs, &s.LastDynamicID, &s.LastDynamicPubTs,
		&s.LastCheckedAt, &s.LastSuccessAt, &s.ConsecutiveFailures, &s.LastError,
		&rawJSON, &s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		return Subscription{}, err
	}
	if rawJSON != "" {
		var full Subscription
		if json.Unmarshal([]byte(rawJSON), &full) == nil && full.ID != "" {
			s.ID = full.ID
			s.Name = full.Name
			s.Note = full.Note
			s.Avatar = full.Avatar
			s.Accounts = full.Accounts
			s.Rules = full.Rules
			s.RuleID = full.RuleID
			s.AutoCacheBacklog = full.AutoCacheBacklog
		}
	}
	s.Enabled = en == 1
	s.LiveNotify = ln == 1
	s.LiveAutoRecord = lar == 1
	syncSubFields(&s, time.Now())
	return s, nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func firstNonEmptyStr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func compareNumericID(a, b string) int {
	if len(a) != len(b) {
		if len(a) > len(b) {
			return 1
		}
		return -1
	}
	return strings.Compare(a, b)
}
