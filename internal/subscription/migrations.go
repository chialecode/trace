package subscription

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/storage"
)

// SubscriptionsSchemaVersion is the on-disk schema version for subscriptions.json.
const SubscriptionsSchemaVersion = 5

// UpdatesDBSchemaVersion is the SQLite PRAGMA user_version for updates.db.
const UpdatesDBSchemaVersion = 2

// CurrentSchemaVersion aliases SubscriptionsSchemaVersion for callers inspecting
// the current subscriptions.json structure version.
const CurrentSchemaVersion = SubscriptionsSchemaVersion

const (
	// DefaultBilibiliHourlyQuota is the shared automatic request limit per hour for Bilibili.
	DefaultBilibiliHourlyQuota = 200
	// DefaultDouyinHourlyQuota is the shared automatic request limit per hour for Douyin.
	DefaultDouyinHourlyQuota = 120
	// DefaultWeiboHourlyQuota is the shared automatic request limit per hour for Weibo (DEC-089).
	DefaultWeiboHourlyQuota = 180
	// DefaultXHourlyQuota is the shared automatic request limit per hour for X (NFR-NET-005).
	DefaultXHourlyQuota = 60

	// WeiboBackoffDecayWindow is the quiet window after the last Weibo stop during which
	// a previously escalated backoff level decays back to 0 (DEC-089).
	WeiboBackoffDecayWindow = 6 * time.Hour
)

// SiteBackoffDuration returns the backoff duration for siteName at 1-based backoff level.
// Weibo uses a dedicated ladder (15m -> 30m -> 1h -> 2h -> 6h, capped at 6h) decoupled from
// IntervalMinutes (DEC-089); other sites double IntervalMinutes up to 24h.
func SiteBackoffDuration(siteName string, level int, intervalMinutes int) time.Duration {
	if level < 1 {
		level = 1
	}
	if normalizeSchedulerSite(siteName) == string(site.Weibo) {
		switch level {
		case 1:
			return 15 * time.Minute
		case 2:
			return 30 * time.Minute
		case 3:
			return 1 * time.Hour
		case 4:
			return 2 * time.Hour
		default:
			return 6 * time.Hour
		}
	}
	interval := time.Duration(intervalMinutes) * time.Minute
	if interval < time.Minute {
		interval = time.Hour
	}
	return min(interval*time.Duration(1<<min(level, 10)), 24*time.Hour)
}

var (
	migrateUpdatesV2HookMu sync.Mutex
	migrateUpdatesV2Hook   func(tx *sql.Tx) error
)

// SetMigrateUpdatesV2HookForTest installs a fault-injection callback invoked inside
// the updates.db v1 -> v2 SQLite transaction before PRAGMA user_version = 2 is committed.
func SetMigrateUpdatesV2HookForTest(fn func(tx *sql.Tx) error) func() {
	migrateUpdatesV2HookMu.Lock()
	prev := migrateUpdatesV2Hook
	migrateUpdatesV2Hook = fn
	migrateUpdatesV2HookMu.Unlock()
	return func() {
		migrateUpdatesV2HookMu.Lock()
		migrateUpdatesV2Hook = prev
		migrateUpdatesV2HookMu.Unlock()
	}
}

func getMigrateUpdatesV2Hook() func(tx *sql.Tx) error {
	migrateUpdatesV2HookMu.Lock()
	defer migrateUpdatesV2HookMu.Unlock()
	return migrateUpdatesV2Hook
}

type sqlQueryExecutor interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
}

const updatesBaseTablesSQL = `
CREATE TABLE IF NOT EXISTS account_state (
	site TEXT NOT NULL,
	uid TEXT NOT NULL,
	baseline_at TEXT NOT NULL DEFAULT '',
	last_checked_at TEXT NOT NULL DEFAULT '',
	last_kind TEXT NOT NULL DEFAULT '',
	newest_id TEXT NOT NULL DEFAULT '',
	newest_ts INTEGER NOT NULL DEFAULT 0,
	note TEXT NOT NULL DEFAULT '',
	unlinked_at TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (site, uid)
);
CREATE TABLE IF NOT EXISTS update_item (
	site TEXT NOT NULL,
	uid TEXT NOT NULL,
	dyn_id TEXT NOT NULL,
	pub_ts INTEGER NOT NULL DEFAULT 0,
	type TEXT NOT NULL DEFAULT '',
	snapshot TEXT NOT NULL DEFAULT '',
	discovered_at TEXT NOT NULL DEFAULT '',
	seen_at TEXT NOT NULL DEFAULT '',
	baseline_snapshot INTEGER NOT NULL DEFAULT 0,
	auto_task_id TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (site, uid, dyn_id)
);
CREATE INDEX IF NOT EXISTS idx_update_item_pub ON update_item(site, uid, pub_ts DESC, dyn_id DESC);
CREATE TABLE IF NOT EXISTS scheduler_state (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	consecutive_failures INTEGER NOT NULL DEFAULT 0,
	backoff_until TEXT NOT NULL DEFAULT '',
	last_stop_reason TEXT NOT NULL DEFAULT '',
	last_stop_at TEXT NOT NULL DEFAULT '',
	token_timestamps_json TEXT NOT NULL DEFAULT '[]'
);
INSERT OR IGNORE INTO scheduler_state (id, consecutive_failures, backoff_until, last_stop_reason, last_stop_at, token_timestamps_json)
VALUES (1, 0, '', '', '', '[]');
CREATE TABLE IF NOT EXISTS site_scheduler_state (
	site TEXT PRIMARY KEY,
	backoff_until TEXT NOT NULL DEFAULT '',
	backoff_level INTEGER NOT NULL DEFAULT 0,
	consecutive_failures INTEGER NOT NULL DEFAULT 0,
	last_stop_reason TEXT NOT NULL DEFAULT '',
	last_stop_at TEXT NOT NULL DEFAULT '',
	token_timestamps_json TEXT NOT NULL DEFAULT '[]'
);
CREATE TABLE IF NOT EXISTS account_works (
	site TEXT NOT NULL,
	uid TEXT NOT NULL,
	works_total INTEGER NOT NULL DEFAULT -1,
	works_at TEXT NOT NULL DEFAULT '',
	full_cache_at TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (site, uid)
);
CREATE TABLE IF NOT EXISTS account_baseline_pinned (
	site TEXT NOT NULL,
	uid TEXT NOT NULL,
	post_id TEXT NOT NULL,
	PRIMARY KEY (site, uid, post_id)
);
`

func (m *Manager) initUpdatesSchemaV2() error {
	v, err := storage.CheckSQLiteSchema(m.updatesDB, "updates.db", UpdatesDBSchemaVersion)
	if err != nil {
		return err
	}
	if v < UpdatesDBSchemaVersion {
		tx, err := m.updatesDB.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()

		if _, err := tx.Exec(updatesBaseTablesSQL); err != nil {
			return err
		}
		if err := migrateBaselineSnapshotsExec(tx); err != nil {
			return err
		}
		if _, err := tx.Exec(`
			INSERT INTO site_scheduler_state (
				site, backoff_until, backoff_level, consecutive_failures, last_stop_reason, last_stop_at, token_timestamps_json
			)
			SELECT 'bilibili', backoff_until, consecutive_failures, consecutive_failures, last_stop_reason, last_stop_at, token_timestamps_json
			FROM scheduler_state
			WHERE id = 1
			ON CONFLICT(site) DO UPDATE SET
				backoff_until = excluded.backoff_until,
				backoff_level = excluded.backoff_level,
				consecutive_failures = excluded.consecutive_failures,
				last_stop_reason = excluded.last_stop_reason,
				last_stop_at = excluded.last_stop_at,
				token_timestamps_json = excluded.token_timestamps_json
		`); err != nil {
			return err
		}
		if _, err := tx.Exec(`
			INSERT OR IGNORE INTO site_scheduler_state (
				site, backoff_until, backoff_level, consecutive_failures, last_stop_reason, last_stop_at, token_timestamps_json
			) VALUES ('bilibili', '', 0, 0, '', '', '[]')
		`); err != nil {
			return err
		}
		if hook := getMigrateUpdatesV2Hook(); hook != nil {
			if err := hook(tx); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", UpdatesDBSchemaVersion)); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		_, _ = m.updatesDB.Exec("PRAGMA wal_checkpoint(PASSIVE)")
		return nil
	}

	if _, err := m.updatesDB.Exec(updatesBaseTablesSQL); err != nil {
		return err
	}
	if _, err := m.updatesDB.Exec(`
		INSERT OR IGNORE INTO site_scheduler_state (
			site, backoff_until, backoff_level, consecutive_failures, last_stop_reason, last_stop_at, token_timestamps_json
		)
		SELECT 'bilibili', backoff_until, consecutive_failures, consecutive_failures, last_stop_reason, last_stop_at, token_timestamps_json
		FROM scheduler_state
		WHERE id = 1
	`); err != nil {
		return err
	}
	if _, err := m.updatesDB.Exec(`
		INSERT OR IGNORE INTO site_scheduler_state (
			site, backoff_until, backoff_level, consecutive_failures, last_stop_reason, last_stop_at, token_timestamps_json
		) VALUES ('bilibili', '', 0, 0, '', '', '[]')
	`); err != nil {
		return err
	}
	return m.migrateBaselineSnapshots()
}

// migrateBaselineSnapshots adds an idempotent, reversible classification for
// legacy first-check snapshots. The original rows, snapshots, and obsolete
// viewed markers remain untouched. Missing/invalid timestamps stay visible.
func (m *Manager) migrateBaselineSnapshots() error {
	tx, err := m.updatesDB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := migrateBaselineSnapshotsExec(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func migrateBaselineSnapshotsExec(exec sqlQueryExecutor) error {
	rows, err := exec.Query(`PRAGMA table_info(update_item)`)
	if err != nil {
		return err
	}
	hasFlag := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		hasFlag = hasFlag || name == "baseline_snapshot"
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !hasFlag {
		if _, err := exec.Exec(`ALTER TABLE update_item ADD COLUMN baseline_snapshot INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
		if _, err := exec.Exec(`UPDATE update_item SET baseline_snapshot = 1
			WHERE julianday(discovered_at) <= (
				SELECT julianday(baseline_at) FROM account_state
				WHERE account_state.site = update_item.site AND account_state.uid = update_item.uid
			)`); err != nil {
			return err
		}
	}
	return nil
}

// normalizeSchedulerSite parses a known site identifier for scheduler, quota,
// pacing, and round-cap lookups. Empty or unknown site names return "" and
// never fall back to Bilibili.
func normalizeSchedulerSite(raw string) string {
	id, ok := site.ParseID(raw)
	if !ok {
		return ""
	}
	return string(id)
}

// normalizeLegacyAccountSite normalizes a persisted account or SQLite row site
// field, mapping empty legacy v1 records (predating multi-site support) to
// bilibili while never coercing any unknown non-empty site name to bilibili.
func normalizeLegacyAccountSite(raw string) string {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	if trimmed == "" {
		return string(site.Bilibili)
	}
	if id, ok := site.ParseID(trimmed); ok {
		return string(id)
	}
	return trimmed
}

// SiteHourlyQuotaLimit returns the per-hour automatic request limit for a site.
// Unknown or empty site names return 0 rather than falling back to Bilibili.
func SiteHourlyQuotaLimit(siteName string) int {
	switch normalizeSchedulerSite(siteName) {
	case string(site.Bilibili):
		return DefaultBilibiliHourlyQuota
	case string(site.Douyin):
		return DefaultDouyinHourlyQuota
	case string(site.Weibo):
		return DefaultWeiboHourlyQuota
	case string(site.X):
		return DefaultXHourlyQuota
	default:
		return 0
	}
}

// GetSiteSchedulerState returns the persistent scheduler backoff and token bucket state for a site.
func (m *Manager) GetSiteSchedulerState(siteName string) SchedulerState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.getSiteSchedulerStateLocked(siteName)
}

func (m *Manager) getSiteSchedulerStateLocked(siteName string) SchedulerState {
	normSite := normalizeSchedulerSite(siteName)
	if normSite == "" {
		return SchedulerState{Site: strings.TrimSpace(siteName)}
	}
	st := SchedulerState{Site: normSite}
	var tokensRaw string
	err := m.updatesDB.QueryRow(`
		SELECT backoff_until, backoff_level, consecutive_failures, last_stop_reason, last_stop_at, token_timestamps_json
		FROM site_scheduler_state
		WHERE site = ?
	`, normSite).Scan(&st.BackoffUntil, &st.BackoffLevel, &st.ConsecutiveFailures, &st.LastStopReason, &st.LastStopAt, &tokensRaw)
	if err != nil {
		return st
	}
	if tokensRaw != "" {
		_ = json.Unmarshal([]byte(tokensRaw), &st.TokenTimestamps)
	}
	if m.maybeDecayWeiboBackoffLocked(normSite, &st, m.nowFunc().UTC()) {
		_ = m.saveSiteSchedulerStateLocked(normSite, st)
	}
	return st
}

// SaveSiteSchedulerState persists the scheduler backoff and token bucket state for a site.
func (m *Manager) SaveSiteSchedulerState(siteName string, st SchedulerState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saveSiteSchedulerStateLocked(siteName, st)
}

func (m *Manager) saveSiteSchedulerStateLocked(siteName string, st SchedulerState) error {
	normSite := normalizeSchedulerSite(siteName)
	if normSite == "" {
		return fmt.Errorf("%w: %q", site.ErrUnknownSite, siteName)
	}
	level := st.BackoffLevel
	failures := st.ConsecutiveFailures
	if failures == 0 && st.BackoffUntil == "" {
		level = 0
	} else if level == 0 && failures > 0 {
		level = failures
	} else if failures == 0 && level > 0 {
		failures = level
	}
	tokensRaw := []byte("[]")
	if len(st.TokenTimestamps) > 0 {
		if encoded, err := json.Marshal(st.TokenTimestamps); err == nil {
			tokensRaw = encoded
		}
	}
	_, err := m.updatesDB.Exec(`
		INSERT INTO site_scheduler_state (
			site, backoff_until, backoff_level, consecutive_failures, last_stop_reason, last_stop_at, token_timestamps_json
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(site) DO UPDATE SET
			backoff_until = excluded.backoff_until,
			backoff_level = excluded.backoff_level,
			consecutive_failures = excluded.consecutive_failures,
			last_stop_reason = excluded.last_stop_reason,
			last_stop_at = excluded.last_stop_at,
			token_timestamps_json = excluded.token_timestamps_json
	`, normSite, st.BackoffUntil, level, failures, st.LastStopReason, st.LastStopAt, string(tokensRaw))
	return err
}

// IsSiteInBackoff reports whether automatic jobs for siteName are paused due to backoff.
func (m *Manager) IsSiteInBackoff(siteName string) (bool, time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.isSiteInBackoffLocked(siteName)
}

func (m *Manager) isSiteInBackoffLocked(siteName string) (bool, time.Time) {
	normSite := normalizeSchedulerSite(siteName)
	if normSite == "" {
		return false, time.Time{}
	}
	st := m.getSiteSchedulerStateLocked(normSite)
	if st.BackoffUntil == "" {
		return false, time.Time{}
	}
	t, err := time.Parse(time.RFC3339, st.BackoffUntil)
	if err != nil {
		return false, time.Time{}
	}
	now := m.nowFunc().UTC()
	return now.Before(t), t
}

// CanUseSiteQuotaTokens checks whether n automatic request tokens are available in siteName's 1-hour bucket without consuming them.
func (m *Manager) CanUseSiteQuotaTokens(siteName string, n int) bool {
	normSite := normalizeSchedulerSite(siteName)
	if normSite == "" {
		return false
	}
	if n <= 0 {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.getSiteSchedulerStateLocked(normSite)
	cutoff := m.nowFunc().UTC().Add(-time.Hour).Unix()
	count := 0
	for _, ts := range st.TokenTimestamps {
		if ts > cutoff {
			count++
		}
	}
	return count+n <= SiteHourlyQuotaLimit(normSite)
}

// ConsumeSiteQuotaTokens reserves n tokens from siteName's hourly automatic request bucket.
func (m *Manager) ConsumeSiteQuotaTokens(siteName string, n int) bool {
	normSite := normalizeSchedulerSite(siteName)
	if normSite == "" {
		return false
	}
	if n <= 0 {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.consumeSiteQuotaTokensLocked(normSite, n)
}

func (m *Manager) consumeSiteQuotaTokensLocked(siteName string, n int) bool {
	normSite := normalizeSchedulerSite(siteName)
	if normSite == "" {
		return false
	}
	limit := SiteHourlyQuotaLimit(normSite)
	if limit <= 0 {
		return false
	}
	st := m.getSiteSchedulerStateLocked(normSite)
	now := m.nowFunc().UTC()
	cutoff := now.Add(-time.Hour).Unix()
	var valid []int64
	for _, ts := range st.TokenTimestamps {
		if ts > cutoff {
			valid = append(valid, ts)
		}
	}
	if len(valid)+n > limit {
		st.TokenTimestamps = valid
		_ = m.saveSiteSchedulerStateLocked(normSite, st)
		return false
	}
	nowUnix := now.Unix()
	for i := 0; i < n; i++ {
		valid = append(valid, nowUnix)
	}
	st.TokenTimestamps = valid
	return m.saveSiteSchedulerStateLocked(normSite, st) == nil
}

type rateLimitResetProvider interface {
	RateLimitResetAt() time.Time
}

func extractRateLimitResetAt(err error) time.Time {
	if err == nil {
		return time.Time{}
	}
	var p rateLimitResetProvider
	if errors.As(err, &p) && p != nil {
		return p.RateLimitResetAt()
	}
	return time.Time{}
}

func defaultRateLimitJitter() time.Duration {
	n, err := rand.Int(rand.Reader, big.NewInt(61))
	if err != nil {
		return 30 * time.Second
	}
	return time.Duration(n.Int64()) * time.Second
}

// SetRateLimitJitterFunc overrides the 0..60s jitter added after a site's
// rate-limit reset timestamp (for deterministic tests).
func (m *Manager) SetRateLimitJitterFunc(fn func() time.Duration) {
	m.mu.Lock()
	if fn == nil {
		m.rateLimitJitterFunc = defaultRateLimitJitter
	} else {
		m.rateLimitJitterFunc = fn
	}
	m.mu.Unlock()
}

func (m *Manager) nextRateLimitJitterLocked() time.Duration {
	fn := m.rateLimitJitterFunc
	if fn == nil {
		fn = defaultRateLimitJitter
	}
	d := fn()
	if d < 0 {
		return 0
	}
	if d > time.Minute {
		return time.Minute
	}
	return d
}

// SetOnBackoffCleared registers a callback invoked whenever active backoff for a site
// is explicitly cleared (for example after a manual probe succeeds, DEC-089) so
// background cache queues and download dispatchers can wake immediately.
func (m *Manager) SetOnBackoffCleared(fn func(siteID site.ID)) {
	m.mu.Lock()
	m.onBackoffCleared = fn
	m.mu.Unlock()
}

func (m *Manager) maybeDecayWeiboBackoffLocked(normSite string, state *SchedulerState, now time.Time) bool {
	if normSite != string(site.Weibo) || state == nil {
		return false
	}
	if state.ConsecutiveFailures == 0 && state.BackoffLevel == 0 && state.BackoffUntil == "" {
		return false
	}
	if state.BackoffUntil != "" {
		if until, err := time.Parse(time.RFC3339, state.BackoffUntil); err == nil && now.Before(until) {
			return false
		}
	}
	if state.LastStopAt == "" {
		return false
	}
	lastStop, err := time.Parse(time.RFC3339, state.LastStopAt)
	if err != nil || now.Sub(lastStop) < WeiboBackoffDecayWindow {
		return false
	}
	state.ConsecutiveFailures = 0
	state.BackoffLevel = 0
	state.BackoffUntil = ""
	return true
}

// ClearSiteBackoff clears active and escalated backoff state for siteName and wakes
// registered background consumers when an active backoff window was cleared (DEC-089).
func (m *Manager) ClearSiteBackoff(siteName string) bool {
	normSite := normalizeSchedulerSite(siteName)
	if normSite == "" {
		return false
	}
	m.mu.Lock()
	wasActive, _ := m.isSiteInBackoffLocked(normSite)
	st := m.getSiteSchedulerStateLocked(normSite)
	changed := wasActive || st.BackoffUntil != "" || st.BackoffLevel > 0 || st.ConsecutiveFailures > 0
	if changed {
		st.BackoffUntil = ""
		st.BackoffLevel = 0
		st.ConsecutiveFailures = 0
		_ = m.saveSiteSchedulerStateLocked(normSite, st)
	}
	cb := m.onBackoffCleared
	m.mu.Unlock()
	if wasActive && cb != nil {
		cb(site.ID(normSite))
	}
	return wasActive
}

// RecordSiteAutomaticSuccess resets an elapsed site backoff ladder once an automatic
// request succeeds cleanly after the backoff window has expired (DEC-089).
func (m *Manager) RecordSiteAutomaticSuccess(siteName string) {
	normSite := normalizeSchedulerSite(siteName)
	if normSite == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.getSiteSchedulerStateLocked(normSite)
	if st.ConsecutiveFailures == 0 && st.BackoffLevel == 0 && st.BackoffUntil == "" {
		return
	}
	if active, _ := m.isSiteInBackoffLocked(normSite); active {
		return
	}
	st.ConsecutiveFailures = 0
	st.BackoffLevel = 0
	st.BackoffUntil = ""
	_ = m.saveSiteSchedulerStateLocked(normSite, st)
}

// RecordSiteManualProbeFailure records a risk-control or rate-limit failure from an
// explicit user-initiated single-shot probe (DEC-089). If the site is already in active
// backoff, the existing backoff window is preserved without escalating BackoffLevel
// (extending only when a rate-limit resetAt is later); otherwise normal Level-1 backoff starts.
func (m *Manager) RecordSiteManualProbeFailure(siteName, kind string, resetAt time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recordSiteManualProbeFailureLocked(siteName, kind, resetAt)
}

func (m *Manager) recordSiteManualProbeFailureLocked(siteName, kind string, resetAt time.Time) {
	normSite := normalizeSchedulerSite(siteName)
	if normSite == "" {
		return
	}
	state := m.getSiteSchedulerStateLocked(normSite)
	now := m.nowFunc().UTC()
	if kind != site.ErrKindRiskControl && kind != site.ErrKindRateLimited {
		state.LastStopReason, state.LastStopAt = kind, now.Format(time.RFC3339)
		_ = m.saveSiteSchedulerStateLocked(normSite, state)
		return
	}
	if active, existingUntil := m.isSiteInBackoffLocked(normSite); active {
		state.LastStopReason, state.LastStopAt = kind, now.Format(time.RFC3339)
		if kind == site.ErrKindRateLimited && !resetAt.IsZero() {
			jitter := m.nextRateLimitJitterLocked()
			until := resetAt.UTC().Add(jitter)
			if !until.After(now) {
				until = now.Add(max(jitter, time.Second))
			}
			if maxUntil := now.Add(24 * time.Hour); until.After(maxUntil) {
				until = maxUntil
			}
			if until.After(existingUntil) {
				state.BackoffUntil = until.Format(time.RFC3339)
			}
		}
		_ = m.saveSiteSchedulerStateLocked(normSite, state)
		return
	}
	m.recordSiteAutomaticStopLocked(normSite, kind, resetAt)
}

// RecordSiteAutomaticStop records a risk-control or rate-limit stop for siteName and advances its exponential backoff.
func (m *Manager) RecordSiteAutomaticStop(siteName, kind string) {
	m.RecordSiteAutomaticStopUntil(siteName, kind, time.Time{})
}

// RecordSiteAutomaticStopUntil records a stop for siteName. When kind is
// ErrKindRateLimited and resetAt is non-zero, backoff pauses until resetAt plus
// 0..60s jitter (capped at 24h) instead of doubling exponentially; risk_control
// and unanchored rate limits use the per-site backoff ladder.
func (m *Manager) RecordSiteAutomaticStopUntil(siteName, kind string, resetAt time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recordSiteAutomaticStopLocked(siteName, kind, resetAt)
}

func (m *Manager) recordSiteAutomaticStopLocked(siteName, kind string, resetAt time.Time) {
	normSite := normalizeSchedulerSite(siteName)
	if normSite == "" {
		return
	}
	state := m.getSiteSchedulerStateLocked(normSite)
	now := m.nowFunc().UTC()
	m.maybeDecayWeiboBackoffLocked(normSite, &state, now)
	if previous, err := time.Parse(time.RFC3339, state.LastStopAt); err == nil && now.Sub(previous) < time.Second && state.LastStopReason == kind {
		if resetAt.IsZero() {
			return
		}
		if curUntil, uErr := time.Parse(time.RFC3339, state.BackoffUntil); uErr == nil && !curUntil.Before(resetAt.UTC()) {
			return
		}
	}
	state.LastStopReason, state.LastStopAt = kind, now.Format(time.RFC3339)
	if kind != site.ErrKindRiskControl && kind != site.ErrKindRateLimited {
		_ = m.saveSiteSchedulerStateLocked(normSite, state)
		return
	}
	if kind == site.ErrKindRateLimited && !resetAt.IsZero() {
		jitter := m.nextRateLimitJitterLocked()
		until := resetAt.UTC().Add(jitter)
		if !until.After(now) {
			until = now.Add(max(jitter, time.Second))
		}
		maxUntil := now.Add(24 * time.Hour)
		if until.After(maxUntil) {
			until = maxUntil
		}
		if existingUntil, uErr := time.Parse(time.RFC3339, state.BackoffUntil); uErr == nil && existingUntil.After(until) {
			until = existingUntil
		}
		if state.ConsecutiveFailures < 1 {
			state.ConsecutiveFailures = 1
		}
		if state.BackoffLevel < 1 {
			state.BackoffLevel = 1
		}
		state.BackoffUntil = until.Format(time.RFC3339)
		_ = m.saveSiteSchedulerStateLocked(normSite, state)
		return
	}
	state.ConsecutiveFailures++
	state.BackoffLevel = state.ConsecutiveFailures
	duration := SiteBackoffDuration(normSite, state.BackoffLevel, m.subSettings.IntervalMinutes)
	state.BackoffUntil = now.Add(duration).Format(time.RFC3339)
	_ = m.saveSiteSchedulerStateLocked(normSite, state)
}

// SiteAutomaticRequestAllowed checks backoff and charges 1 hourly quota token for siteName.
func (m *Manager) SiteAutomaticRequestAllowed(siteName string) error {
	normSite := normalizeSchedulerSite(siteName)
	if normSite == "" {
		return &site.Error{Site: site.ID(strings.TrimSpace(siteName)), Kind: site.ErrKindBadRequest, Message: fmt.Sprintf("不支持的站点 %q", siteName)}
	}
	if active, _ := m.IsSiteInBackoff(normSite); active {
		return &site.Error{Site: site.ID(normSite), Kind: site.ErrKindBackoff, Message: "自动请求仍在退避期"}
	}
	if !m.ConsumeSiteQuotaTokens(normSite, 1) {
		return &site.Error{Site: site.ID(normSite), Kind: site.ErrKindQuota, Message: "本小时自动请求额度已用尽"}
	}
	return nil
}

// SiteAutomaticRequestAllowedFor is SiteAutomaticRequestAllowed for a
// request of the given automatic source. Live work ("live": status checks
// and starting or resuming a recording) still stops in backoff but does not
// take the hourly quota: it keeps its own pace (Douyin live checks at least
// 6 seconds apart), so update checks and automatic caching keep the whole
// quota and live checks need not wait for it (DEC-060 ⑤).
func (m *Manager) SiteAutomaticRequestAllowedFor(siteName, source string) error {
	normSite := normalizeSchedulerSite(siteName)
	if normSite == "" {
		return &site.Error{Site: site.ID(strings.TrimSpace(siteName)), Kind: site.ErrKindBadRequest, Message: fmt.Sprintf("不支持的站点 %q", siteName)}
	}
	if source != "live" {
		return m.SiteAutomaticRequestAllowed(normSite)
	}
	if active, _ := m.IsSiteInBackoff(normSite); active {
		return &site.Error{Site: site.ID(normSite), Kind: site.ErrKindBackoff, Message: "自动请求仍在退避期"}
	}
	return nil
}

// NormalizeAndValidateAccount validates and normalizes a Bilibili, Douyin, or Weibo account reference.
// Empty or unknown sites are rejected and never fall back to Bilibili.
func NormalizeAndValidateAccount(acc AccountRef) (AccountRef, error) {
	siteID, ok := site.ParseID(acc.Site)
	if !ok {
		return AccountRef{}, fmt.Errorf("subscription: 不支持的站点 %q", acc.Site)
	}
	switch siteID {
	case site.Bilibili:
		normUID, err := site.NormalizeUID(site.Bilibili, acc.UID)
		if err != nil {
			return AccountRef{}, fmt.Errorf("subscription: 账号必须是有效的哔哩哔哩 UID")
		}
		acc.Site = string(site.Bilibili)
		acc.UID = normUID
		acc.UniqueID = ""
		acc.WebRID = ""
		acc.Face = sanitizeAccountFace(site.Bilibili, acc.Face)
		return acc, nil

	case site.Douyin:
		// Reject leading/trailing or embedded whitespace rather than silently trimming sec_uid.
		if err := validateStrictDouyinUID(acc.UID); err != nil {
			return AccountRef{}, err
		}
		normUID, err := site.NormalizeUID(site.Douyin, acc.UID)
		if err != nil || normUID != acc.UID {
			return AccountRef{}, fmt.Errorf("subscription: 账号必须是有效的抖音 sec_uid")
		}
		if acc.WebRID != "" {
			if err := validateOptionalDouyinToken(acc.WebRID, "webRid"); err != nil {
				return AccountRef{}, err
			}
		}
		acc.UniqueID = strings.TrimSpace(acc.UniqueID)
		if utf8.RuneCountInString(acc.UniqueID) > 64 {
			runes := []rune(acc.UniqueID)
			acc.UniqueID = string(runes[:64])
		}
		for _, r := range acc.UniqueID {
			if r < 0x20 || r == 0x7f {
				return AccountRef{}, fmt.Errorf("subscription: uniqueId 含非法控制字符")
			}
		}
		acc.Site = string(site.Douyin)
		acc.Face = sanitizeAccountFace(site.Douyin, acc.Face)
		return acc, nil

	case site.Weibo:
		normUID, err := site.NormalizeUID(site.Weibo, acc.UID)
		if err != nil {
			return AccountRef{}, fmt.Errorf("subscription: 账号必须是有效的微博 UID")
		}
		acc.Site = string(site.Weibo)
		acc.UID = normUID
		acc.UniqueID = ""
		acc.WebRID = ""
		acc.Face = sanitizeAccountFace(site.Weibo, acc.Face)
		return acc, nil

	case site.X:
		normUID, err := site.NormalizeUID(site.X, acc.UID)
		if err != nil {
			return AccountRef{}, fmt.Errorf("subscription: 账号必须是有效的 X 用户 ID")
		}
		handle, err := normalizeXScreenNameSnapshot(acc.UniqueID)
		if err != nil {
			return AccountRef{}, err
		}
		acc.Site = string(site.X)
		acc.UID = normUID
		acc.UniqueID = handle
		acc.WebRID = ""
		acc.Face = sanitizeAccountFace(site.X, acc.Face)
		return acc, nil

	default:
		return AccountRef{}, fmt.Errorf("subscription: 不支持的站点 %q", acc.Site)
	}
}

func normalizeXScreenNameSnapshot(raw string) (string, error) {
	cleaned := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "@"))
	if utf8.RuneCountInString(cleaned) > 64 {
		runes := []rune(cleaned)
		cleaned = string(runes[:64])
	}
	for _, r := range cleaned {
		if unicode.IsSpace(r) || r < 0x20 || r == 0x7f || r == '/' || r == '\\' || r == ':' || r == '?' || r == '#' {
			return "", fmt.Errorf("subscription: X screen_name 格式无效")
		}
	}
	return cleaned, nil
}

func validateStrictDouyinUID(uid string) error {
	if uid == "" || len(uid) > 256 || uid == "." || uid == ".." || strings.Contains(uid, "..") {
		return fmt.Errorf("subscription: 账号必须是有效的抖音 sec_uid")
	}
	for _, r := range uid {
		if unicode.IsSpace(r) || r < 0x20 || r == 0x7f || r == '/' || r == '\\' || r == ':' {
			return fmt.Errorf("subscription: 抖音 sec_uid 不能包含空白或路径分隔符")
		}
	}
	return nil
}

func validateOptionalDouyinToken(val, field string) error {
	if len(val) > 128 || val == "." || val == ".." || strings.Contains(val, "..") {
		return fmt.Errorf("subscription: %s 格式无效", field)
	}
	for _, r := range val {
		if unicode.IsSpace(r) || r < 0x20 || r == 0x7f || r == '/' || r == '\\' || r == ':' {
			return fmt.Errorf("subscription: %s 不能包含空白或路径分隔符", field)
		}
	}
	return nil
}

func sanitizeAccountFace(s site.ID, face string) string {
	trimmed := strings.TrimSpace(face)
	if trimmed == "" {
		return ""
	}
	lower := strings.ToLower(trimmed)
	unescaped := lower
	if decoded, err := url.QueryUnescape(lower); err == nil {
		unescaped = strings.ToLower(decoded)
	}
	for _, marker := range []string{
		"a_bogus=", "x-bogus=", "x-signature=", "x-expires=", "upsig=",
		"w_rid=", "sessionid=", "sessdata=", "auth_key=", "token=",
		"sign=", "signature=", "expires=",
		"ssig=", "kid=", "sub=", "subp=", "xsrf-token=", "wbpsess=",
		"scf=", "alf=", "ssologinstate=", "livephoto=",
		"auth_token=", "ct0=", "kdt=", "twid=", "guest_id=",
		"x-csrf-token", "authorization:",
	} {
		if strings.Contains(lower, marker) || strings.Contains(unescaped, marker) {
			return ""
		}
	}
	switch s {
	case site.Douyin:
		if strings.Contains(trimmed, "?") {
			return ""
		}
	case site.Weibo:
		if strings.Contains(trimmed, "?") || strings.Contains(lower, "%3f") || isWeiboEphemeralMediaURL(lower) || isWeiboEphemeralMediaURL(unescaped) {
			return ""
		}
	case site.X:
		if isXEphemeralMediaURL(lower) || isXEphemeralMediaURL(unescaped) {
			return ""
		}
		if strings.Contains(trimmed, "?") || strings.Contains(lower, "%3f") {
			if !isCleanXPublicImageURL(trimmed) {
				return ""
			}
		}
	}
	return trimmed
}

// isXEphemeralMediaURL reports whether lowerURL targets X video/HLS/private/live
// media hosts or extensions that must never be persisted in snapshots or account metadata.
func isXEphemeralMediaURL(lowerURL string) bool {
	for _, host := range []string{
		"video.twimg.com",
		"video-s.twimg.com",
		"ton.x.com",
		"ton.twitter.com",
		"pscp.tv",
		"periscope.tv",
	} {
		if strings.Contains(lowerURL, host) {
			return true
		}
	}
	pathPart := lowerURL
	if idx := strings.IndexAny(pathPart, "?#"); idx >= 0 {
		pathPart = pathPart[:idx]
	}
	for _, ext := range []string{".mp4", ".mov", ".m3u8", ".flv", ".ts", ".m4s", ".aac"} {
		if strings.HasSuffix(pathPart, ext) {
			return true
		}
	}
	return false
}

// isCleanXPublicImageURL allows public pbs.twimg.com image URLs whose query
// string carries only benign format/name parameters (e.g. ?format=jpg&name=orig).
func isCleanXPublicImageURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.User != nil {
		return false
	}
	if !strings.EqualFold(u.Hostname(), "pbs.twimg.com") {
		return false
	}
	if strings.Contains(strings.ToLower(u.RawPath), "%3f") || strings.Contains(strings.ToLower(u.Path), "%3f") {
		return false
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return false
	}
	for k, vals := range q {
		lk := strings.ToLower(strings.TrimSpace(k))
		if lk != "format" && lk != "name" {
			return false
		}
		for _, v := range vals {
			if v == "" || len(v) > 32 {
				return false
			}
			for _, r := range v {
				if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-') {
					return false
				}
			}
		}
	}
	return true
}

// isWeiboEphemeralMediaURL reports whether lowerURL targets Weibo video or
// LivePhoto media hosts/extensions that must never be persisted in snapshots
// or account metadata (public sinaimg.cn image URLs and weibo.com page URLs
// remain allowed).
func isWeiboEphemeralMediaURL(lowerURL string) bool {
	if strings.Contains(lowerURL, "weibocdn.com") ||
		strings.Contains(lowerURL, "video.weibo.com") ||
		strings.Contains(lowerURL, "livephoto.us.sinaimg.cn") {
		return true
	}
	pathPart := lowerURL
	if idx := strings.IndexAny(pathPart, "?#"); idx >= 0 {
		pathPart = pathPart[:idx]
	}
	for _, ext := range []string{".mp4", ".mov", ".m3u8", ".flv"} {
		if strings.HasSuffix(pathPart, ext) {
			return true
		}
	}
	return false
}

func normalizeSubscriptionAccountForLoad(acc AccountRef) (AccountRef, bool) {
	rawSite := strings.ToLower(strings.TrimSpace(acc.Site))
	if rawSite == "" {
		// Legacy v1 subscriptions.json records predate multi-site support and represent Bilibili accounts.
		rawSite = string(site.Bilibili)
	}
	siteID, ok := site.ParseID(rawSite)
	if !ok {
		return AccountRef{}, false
	}
	acc.Site = string(siteID)
	switch siteID {
	case site.Bilibili:
		if id, err := strconv.ParseInt(acc.UID, 10, 64); err == nil && id > 0 {
			acc.UID = strconv.FormatInt(id, 10)
			acc.Face = sanitizeAccountFace(site.Bilibili, acc.Face)
			return acc, true
		}
		return acc, strings.TrimSpace(acc.UID) != ""
	case site.Douyin:
		if err := validateStrictDouyinUID(acc.UID); err != nil {
			return AccountRef{}, false
		}
		if acc.WebRID != "" && validateOptionalDouyinToken(acc.WebRID, "webRid") != nil {
			acc.WebRID = ""
		}
		acc.UniqueID = strings.TrimSpace(acc.UniqueID)
		acc.Face = sanitizeAccountFace(site.Douyin, acc.Face)
		return acc, true
	case site.Weibo:
		normUID, err := site.NormalizeUID(site.Weibo, acc.UID)
		if err != nil {
			return AccountRef{}, false
		}
		acc.UID = normUID
		acc.UniqueID = ""
		acc.WebRID = ""
		acc.Face = sanitizeAccountFace(site.Weibo, acc.Face)
		return acc, true
	case site.X:
		normUID, err := site.NormalizeUID(site.X, acc.UID)
		if err != nil {
			return AccountRef{}, false
		}
		acc.UID = normUID
		if handle, hErr := normalizeXScreenNameSnapshot(acc.UniqueID); hErr == nil {
			acc.UniqueID = handle
		} else {
			acc.UniqueID = ""
		}
		acc.WebRID = ""
		acc.Face = sanitizeAccountFace(site.X, acc.Face)
		return acc, true
	default:
		return AccountRef{}, false
	}
}

// LinkAccount associates acc with subscription subID, preserving Douyin uniqueId and webRid fields.
// Ownership is determined strictly by <site>:<uid>, independent of webRid or uniqueId.
func (m *Manager) LinkAccount(subID string, acc AccountRef) (result Subscription, resultErr error) {
	normAcc, err := NormalizeAndValidateAccount(acc)
	if err != nil {
		return Subscription{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	before, snapshotErr := m.listLocked()
	if snapshotErr != nil {
		return Subscription{}, snapshotErr
	}
	defer func() {
		if resultErr != nil {
			resultErr = joinRestoreErr(resultErr, m.restoreSubscriptionViewLocked(before))
		}
	}()

	target, ok, err := m.findByIDOrSubIDLocked(subID)
	if err != nil || !ok {
		return Subscription{}, fmt.Errorf("subscription: %s not found", subID)
	}
	if owner, owned := m.ownerOfLocked(normAcc.Site, normAcc.UID); owned {
		if owner.ID == target.ID || owner.SubID == target.SubID {
			return target, nil
		}
		return owner, fmt.Errorf("%w: %s:%s already belongs to %s", ErrAccountConflict, normAcc.Site, normAcc.UID, owner.Name)
	}
	if len(target.Accounts) >= 20 {
		return Subscription{}, fmt.Errorf("subscription: maximum 20 accounts per subscription reached")
	}
	if normAcc.AddedAt == "" {
		normAcc.AddedAt = m.nowFunc().UTC().Format(time.RFC3339)
	}
	target.Accounts = append(target.Accounts, normAcc)
	if _, err := m.updatesDB.Exec(`UPDATE account_state SET unlinked_at = '' WHERE site = ? AND uid = ?`, normAcc.Site, normAcc.UID); err != nil {
		return Subscription{}, err
	}
	syncSubFields(&target, m.nowFunc())
	if err := m.upsertRowLocked(target); err != nil {
		return Subscription{}, err
	}
	if err := m.writeSubscriptionsToDiskLocked(); err != nil {
		return Subscription{}, err
	}
	return target, nil
}

// UpdateAccountWebRID updates or clears the cached webRid for a Douyin account without changing ownership.
func (m *Manager) UpdateAccountWebRID(siteName, uid, webRid string) (resultErr error) {
	normAcc, err := NormalizeAndValidateAccount(AccountRef{Site: siteName, UID: uid, WebRID: webRid})
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	before, snapshotErr := m.listLocked()
	if snapshotErr != nil {
		return snapshotErr
	}
	defer func() {
		if resultErr != nil {
			resultErr = joinRestoreErr(resultErr, m.restoreSubscriptionViewLocked(before))
		}
	}()

	owner, ok := m.ownerOfLocked(normAcc.Site, normAcc.UID)
	if !ok {
		return fmt.Errorf("subscription: account %s:%s not found", normAcc.Site, normAcc.UID)
	}
	changed := false
	for i := range owner.Accounts {
		if owner.Accounts[i].Site == normAcc.Site && owner.Accounts[i].UID == normAcc.UID {
			if owner.Accounts[i].WebRID != normAcc.WebRID {
				owner.Accounts[i].WebRID = normAcc.WebRID
				changed = true
			}
		}
	}
	if !changed {
		return nil
	}
	owner.UpdatedAt = m.nowFunc().UTC().Format(time.RFC3339)
	syncSubFields(&owner, m.nowFunc())
	if err := m.upsertRowLocked(owner); err != nil {
		return err
	}
	return m.writeSubscriptionsToDiskLocked()
}

func joinRestoreErr(primary, restore error) error {
	if restore == nil {
		return primary
	}
	return fmt.Errorf("%w (rollback: %v)", primary, restore)
}
