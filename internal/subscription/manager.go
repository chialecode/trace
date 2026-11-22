package subscription

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
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
	FullCache      bool          `json:"fullCache,omitempty"` // DEC-061
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
	mu sync.Mutex
	// formerDefaultName is the name a user gave the once-editable default
	// rule; the rule that keeps its subscriptions' settings is named after it.
	formerDefaultName string
	db                *sql.DB
	updatesDB         *sql.DB
	subsPath          string
	lib               *library.Store
	dl                *download.Manager
	client            *bilibili.Client
	subSettings       settings.SubscriptionSettings
	rules             []CacheRule
	onEvent           func(CheckSummary)
	liveCheck         func(context.Context, []int64) error
	nowFunc           func() time.Time

	// In-memory index of the subscriptions table (invalidated on every write/load)
	// so OwnerOf / findByIDOrSubIDLocked never scan SQLite or updatesDB per call.
	subsCacheValid bool
	subsCache      []Subscription
	ownerIndex     map[string]int // "<site>:<uid>" -> index in subsCache
	idIndex        map[string]int // ID / SubID -> index in subsCache

	fetchers               map[site.ID]site.SubscriptionFetcher
	siteAuthCheck          func(site.ID) bool
	autoCacheEnqueuers     map[site.ID]AutoCacheEnqueueFunc
	autoCacheGate          func() error
	onLiveHint             func(acc AccountRef, roomID, webRID string)
	onAuthorAvatar         func(siteID, uid, rawFace string)
	siteLiveCheck          func(ctx context.Context, accounts []AccountRef) error
	sleepFunc              func(ctx context.Context, d time.Duration) error
	rateLimitJitterFunc    func() time.Duration
	siteIntervals          map[string]time.Duration
	accountIntervalTouched bool

	accountInterval      time.Duration
	corruptRecovered     bool
	duplicateAccountWarn bool
	maxSchemaVersion     int

	checking    bool
	checkCancel context.CancelFunc
	lastSummary *CheckSummary

	schedCancel context.CancelFunc
	// nextRunAt is when the schedule loop runs the next round; zero when
	// scheduled checking is off or the loop is not running.
	nextRunAt time.Time
	schedWake chan struct{}
	wg        sync.WaitGroup

	diskWriteHook func() error
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
		db:                  db,
		updatesDB:           updatesDB,
		subsPath:            subsPath,
		lib:                 lib,
		dl:                  dl,
		client:              client,
		subSettings:         subSettings,
		onEvent:             onEvent,
		nowFunc:             time.Now,
		fetchers:            make(map[site.ID]site.SubscriptionFetcher),
		autoCacheEnqueuers:  make(map[site.ID]AutoCacheEnqueueFunc),
		sleepFunc:           defaultSubscriptionSleep,
		rateLimitJitterFunc: defaultRateLimitJitter,
		siteIntervals:       make(map[string]time.Duration),
		accountInterval:     DefaultBilibiliAccountInterval,
		schedWake:           make(chan struct{}, 1),
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
	var dir string
	for rows.Next() {
		var seq int
		var name, file string
		if err := rows.Scan(&seq, &name, &file); err == nil && name == "main" && file != "" && !strings.HasPrefix(file, ":") {
			dir = filepath.Dir(file)
		}
	}
	if rows.Err() != nil {
		return ""
	}
	return dir
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

func (m *Manager) getAccountStateLocked(siteName, uid string) (AccountState, bool, error) {
	normSite := normalizeSchedulerSite(siteName)
	if normSite == "" {
		return AccountState{Site: strings.TrimSpace(siteName), UID: uid}, false, nil
	}
	var st AccountState
	err := m.updatesDB.QueryRow(`
		SELECT site, uid, baseline_at, last_checked_at, last_kind, newest_id, newest_ts, note, unlinked_at
		FROM account_state WHERE site = ? AND uid = ?
	`, normSite, uid).Scan(&st.Site, &st.UID, &st.BaselineAt, &st.LastCheckedAt, &st.LastKind, &st.NewestID, &st.NewestTs, &st.Note, &st.UnlinkedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return AccountState{Site: normSite, UID: uid}, false, nil
		}
		return AccountState{}, false, err
	}
	return st, true, nil
}

func (m *Manager) listAccountStatesLocked() (map[string]AccountState, error) {
	out := map[string]AccountState{}
	if m.updatesDB == nil {
		return out, nil
	}
	rows, err := m.updatesDB.Query(`
		SELECT site, uid, baseline_at, last_checked_at, last_kind, newest_id, newest_ts, note, unlinked_at
		FROM account_state
	`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var st AccountState
		if err := rows.Scan(&st.Site, &st.UID, &st.BaselineAt, &st.LastCheckedAt, &st.LastKind, &st.NewestID, &st.NewestTs, &st.Note, &st.UnlinkedAt); err != nil {
			return out, err
		}
		out[normalizeLegacyAccountSite(st.Site)+":"+st.UID] = st
	}
	return out, rows.Err()
}

func (m *Manager) saveAccountStateLocked(st AccountState) error {
	normSite := normalizeSchedulerSite(st.Site)
	if normSite == "" {
		return site.ErrUnknownSite
	}
	st.Site = normSite
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

// IsInBackoff reports whether scheduled Bilibili jobs (subscription checks, live checks, cookie renewal) are currently paused due to backoff (subscriptions.md §3).
func (m *Manager) IsInBackoff() (bool, time.Time) {
	return m.IsSiteInBackoff("bilibili")
}

// ConsumeQuotaTokens attempts to reserve n tokens from the shared 200 requests/hour Bilibili scheduled token bucket (subscriptions.md §3).
// If insufficient tokens remain in the current 1-hour window, returns false without consuming any tokens.
func (m *Manager) ConsumeQuotaTokens(n int) bool {
	return m.ConsumeSiteQuotaTokens("bilibili", n)
}

func (m *Manager) insertUpdateItem(siteName, uid, dynID string, pubTs int64, typ, snapshot, discoveredAt string) (bool, error) {
	normSite := normalizeSchedulerSite(siteName)
	if normSite == "" {
		return false, site.ErrUnknownSite
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	res, err := m.updatesDB.Exec(`
		INSERT OR IGNORE INTO update_item (site, uid, dyn_id, pub_ts, type, snapshot, discovered_at, auto_task_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, '')
	`, normSite, uid, dynID, pubTs, typ, snapshot, discoveredAt)
	if err != nil {
		return false, err
	}
	affected, _ := res.RowsAffected()
	return affected > 0, nil
}

func (m *Manager) setUpdateItemTaskID(siteName, uid, dynID, taskID string) error {
	normSite := normalizeSchedulerSite(siteName)
	if normSite == "" {
		return site.ErrUnknownSite
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, err := m.updatesDB.Exec(`UPDATE update_item SET auto_task_id = ? WHERE site = ? AND uid = ? AND dyn_id = ?`, taskID, normSite, uid, dynID)
	return err
}

func (m *Manager) enforceAccountItemCap(siteName, uid string, maxItems int) {
	normSite := normalizeSchedulerSite(siteName)
	if normSite == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var count int
	if err := m.updatesDB.QueryRow(`SELECT COUNT(*) FROM update_item WHERE site = ? AND uid = ? AND baseline_snapshot = 0`, normSite, uid).Scan(&count); err != nil || count <= maxItems {
		return
	}
	excess := count - maxItems
	if _, err := m.updatesDB.Exec(`
		DELETE FROM update_item
		WHERE rowid IN (
			SELECT rowid FROM update_item
			WHERE site = ? AND uid = ? AND baseline_snapshot = 0
			ORDER BY pub_ts ASC, dyn_id ASC
			LIMIT ?
		)
	`, normSite, uid, excess); err != nil {
		return
	}
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
