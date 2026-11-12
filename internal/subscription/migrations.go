package subscription

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/storage"
)

// CurrentSchemaVersion is the schema version for updates.db and subscriptions.json.
const CurrentSchemaVersion = 2

const (
	// DefaultBilibiliHourlyQuota is the shared automatic request limit per hour for Bilibili.
	DefaultBilibiliHourlyQuota = 200
	// DefaultDouyinHourlyQuota is the shared automatic request limit per hour for Douyin.
	DefaultDouyinHourlyQuota = 120
)

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
CREATE TABLE IF NOT EXISTS account_baseline_pinned (
	site TEXT NOT NULL,
	uid TEXT NOT NULL,
	post_id TEXT NOT NULL,
	PRIMARY KEY (site, uid, post_id)
);
`

func (m *Manager) initUpdatesSchemaV2() error {
	v, err := storage.CheckSQLiteSchema(m.updatesDB, "updates.db", CurrentSchemaVersion)
	if err != nil {
		return err
	}
	if v < CurrentSchemaVersion {
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
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", CurrentSchemaVersion)); err != nil {
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

func normalizeSchedulerSite(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return string(site.Bilibili)
	}
	return s
}

// SiteHourlyQuotaLimit returns the per-hour automatic request limit for a site.
func SiteHourlyQuotaLimit(siteName string) int {
	switch normalizeSchedulerSite(siteName) {
	case string(site.Douyin):
		return DefaultDouyinHourlyQuota
	default:
		return DefaultBilibiliHourlyQuota
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
	st := m.getSiteSchedulerStateLocked(siteName)
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
	if n <= 0 {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.getSiteSchedulerStateLocked(siteName)
	cutoff := m.nowFunc().UTC().Add(-time.Hour).Unix()
	count := 0
	for _, ts := range st.TokenTimestamps {
		if ts > cutoff {
			count++
		}
	}
	return count+n <= SiteHourlyQuotaLimit(siteName)
}

// ConsumeSiteQuotaTokens reserves n tokens from siteName's hourly automatic request bucket.
func (m *Manager) ConsumeSiteQuotaTokens(siteName string, n int) bool {
	if n <= 0 {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.consumeSiteQuotaTokensLocked(siteName, n)
}

func (m *Manager) consumeSiteQuotaTokensLocked(siteName string, n int) bool {
	normSite := normalizeSchedulerSite(siteName)
	limit := SiteHourlyQuotaLimit(normSite)
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

// RecordSiteAutomaticStop records a risk-control or rate-limit stop for siteName and advances its exponential backoff.
func (m *Manager) RecordSiteAutomaticStop(siteName, kind string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	normSite := normalizeSchedulerSite(siteName)
	state := m.getSiteSchedulerStateLocked(normSite)
	now := m.nowFunc().UTC()
	if previous, err := time.Parse(time.RFC3339, state.LastStopAt); err == nil && now.Sub(previous) < time.Second && state.LastStopReason == kind {
		return
	}
	state.ConsecutiveFailures++
	state.BackoffLevel = state.ConsecutiveFailures
	state.LastStopReason, state.LastStopAt = kind, now.Format(time.RFC3339)
	interval := time.Duration(m.subSettings.IntervalMinutes) * time.Minute
	if interval < time.Minute {
		interval = time.Hour
	}
	duration := interval * time.Duration(1<<min(state.BackoffLevel, 10))
	state.BackoffUntil = now.Add(min(duration, 24*time.Hour)).Format(time.RFC3339)
	_ = m.saveSiteSchedulerStateLocked(normSite, state)
}

// SiteAutomaticRequestAllowed checks backoff and charges 1 hourly quota token for siteName.
func (m *Manager) SiteAutomaticRequestAllowed(siteName string) error {
	normSite := normalizeSchedulerSite(siteName)
	if active, _ := m.IsSiteInBackoff(normSite); active {
		return &site.Error{Site: site.ID(normSite), Kind: site.ErrKindBackoff, Message: "自动请求仍在退避期"}
	}
	if !m.ConsumeSiteQuotaTokens(normSite, 1) {
		return &site.Error{Site: site.ID(normSite), Kind: site.ErrKindQuota, Message: "本小时自动请求额度已用尽"}
	}
	return nil
}

// NormalizeAndValidateAccount validates and normalizes a Bilibili or Douyin account reference.
func NormalizeAndValidateAccount(acc AccountRef) (AccountRef, error) {
	rawSite := strings.ToLower(strings.TrimSpace(acc.Site))
	if rawSite == "" {
		rawSite = string(site.Bilibili)
	}
	switch site.ID(rawSite) {
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

	default:
		return AccountRef{}, fmt.Errorf("subscription: 不支持的站点 %q", acc.Site)
	}
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
	for _, marker := range []string{"a_bogus=", "x-bogus=", "x-signature=", "x-expires=", "upsig=", "w_rid=", "sessionid=", "sessdata="} {
		if strings.Contains(lower, marker) {
			return ""
		}
	}
	if s == site.Douyin && strings.Contains(trimmed, "?") {
		return ""
	}
	return trimmed
}

func normalizeSubscriptionAccountForLoad(acc AccountRef) (AccountRef, bool) {
	rawSite := strings.ToLower(strings.TrimSpace(acc.Site))
	if rawSite == "" {
		rawSite = string(site.Bilibili)
	}
	acc.Site = rawSite
	if rawSite == string(site.Bilibili) {
		if id, err := strconv.ParseInt(acc.UID, 10, 64); err == nil && id > 0 {
			acc.UID = strconv.FormatInt(id, 10)
			acc.Face = sanitizeAccountFace(site.Bilibili, acc.Face)
			return acc, true
		}
		return acc, strings.TrimSpace(acc.UID) != ""
	}
	if rawSite == string(site.Douyin) {
		if err := validateStrictDouyinUID(acc.UID); err != nil {
			return AccountRef{}, false
		}
		if acc.WebRID != "" && validateOptionalDouyinToken(acc.WebRID, "webRid") != nil {
			acc.WebRID = ""
		}
		acc.UniqueID = strings.TrimSpace(acc.UniqueID)
		acc.Face = sanitizeAccountFace(site.Douyin, acc.Face)
		return acc, true
	}
	return AccountRef{}, false
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
	_, _ = m.updatesDB.Exec(`UPDATE account_state SET unlinked_at = '' WHERE site = ? AND uid = ?`, normAcc.Site, normAcc.UID)
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
