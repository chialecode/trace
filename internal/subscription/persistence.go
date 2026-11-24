package subscription

import (
	"cmp"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/storage"
)

// AccountSiteOrderRank returns the canonical display/storage order rank for a
// site identifier matching site.All() (bilibili -> douyin -> weibo -> x).
// Empty legacy site strings map to bilibili; unknown sites sort last.
func AccountSiteOrderRank(siteName string) int {
	norm := normalizeLegacyAccountSite(siteName)
	all := site.All()
	for i, id := range all {
		if string(id) == norm {
			return i
		}
	}
	return len(all)
}

// SortAccountsBySiteOrder sorts accounts in-place by canonical site order
// (bilibili -> douyin -> weibo -> x) while preserving the relative order of
// multiple accounts on the same site (DEC-071).
func SortAccountsBySiteOrder(accounts []AccountRef) {
	if len(accounts) < 2 {
		return
	}
	slices.SortStableFunc(accounts, func(a, b AccountRef) int {
		return cmp.Compare(AccountSiteOrderRank(a.Site), AccountSiteOrderRank(b.Site))
	})
}

func cloneSubscription(s Subscription) Subscription {
	if s.Accounts != nil {
		s.Accounts = append([]AccountRef(nil), s.Accounts...)
	}
	if s.Rules.AutoCache.Types != nil {
		s.Rules.AutoCache.Types = append([]string(nil), s.Rules.AutoCache.Types...)
	}
	return s
}

func (m *Manager) invalidateSubsCacheLocked() {
	m.subsCacheValid = false
	m.subsCache = nil
	m.ownerIndex = nil
	m.idIndex = nil
}

// ensureSubsCacheLocked populates the in-memory subscription slice and O(1)
// owner/ID lookup tables from the subscriptions table without scanning updatesDB.
func (m *Manager) ensureSubsCacheLocked() error {
	if m.subsCacheValid {
		return nil
	}
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
		return err
	}
	defer rows.Close()

	var subs []Subscription
	for rows.Next() {
		s, err := scanSub(rows)
		if err != nil {
			return err
		}
		subs = append(subs, s)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	ownerIdx := make(map[string]int, len(subs))
	idIdx := make(map[string]int, len(subs)*2)
	for i, s := range subs {
		if s.ID != "" {
			if _, ok := idIdx[s.ID]; !ok {
				idIdx[s.ID] = i
			}
		}
		if s.SubID != "" {
			if _, ok := idIdx[s.SubID]; !ok {
				idIdx[s.SubID] = i
			}
		}
		for _, a := range s.Accounts {
			accSite := normalizeLegacyAccountSite(a.Site)
			key := accSite + ":" + a.UID
			if _, ok := ownerIdx[key]; !ok {
				ownerIdx[key] = i
			}
		}
		if s.Mid > 0 {
			key := fmt.Sprintf("bilibili:%d", s.Mid)
			if _, ok := ownerIdx[key]; !ok {
				ownerIdx[key] = i
			}
		}
	}

	m.subsCache = subs
	m.ownerIndex = ownerIdx
	m.idIndex = idIdx
	m.subsCacheValid = true
	return nil
}

// listSubsOnlyLocked returns cloned subscriptions from cache without querying
// updatesDB for LatestUpdateTs.
func (m *Manager) listSubsOnlyLocked() ([]Subscription, error) {
	if err := m.ensureSubsCacheLocked(); err != nil {
		return nil, err
	}
	out := make([]Subscription, len(m.subsCache))
	for i, s := range m.subsCache {
		out[i] = cloneSubscription(s)
	}
	return out, nil
}

func (m *Manager) effectiveMaxSchemaVersion() int {
	if m.maxSchemaVersion > 0 {
		return m.maxSchemaVersion
	}
	return SubscriptionsSchemaVersion
}

func (m *Manager) loadSubscriptionsFromDiskLocked() error {
	m.invalidateSubsCacheLocked()
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

	supportedVersion := m.effectiveMaxSchemaVersion()
	var rawVersion struct {
		Version *int `json:"version"`
	}
	needsMigration := false
	fromVersion := 1
	if err := json.Unmarshal(raw, &rawVersion); err == nil {
		if rawVersion.Version != nil && *rawVersion.Version > supportedVersion {
			return &storage.NewerDataError{
				File:      filepath.Base(m.subsPath),
				Found:     *rawVersion.Version,
				Supported: supportedVersion,
			}
		}
		if rawVersion.Version != nil && *rawVersion.Version > 0 {
			fromVersion = *rawVersion.Version
		}
		needsMigration = rawVersion.Version == nil || *rawVersion.Version < supportedVersion
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
		m.invalidateSubsCacheLocked()
		return err
	}
	m.rules = sf.CacheRules
	rebound := m.ensureDefaultRuleLocked()

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
		if m.bindRuleLocked(&sub) {
			rebound = true
		}
		if err := upsertSubscriptionRow(tx, sub); err != nil {
			m.rules = prevRules
			return err
		}
		migratedSubs = append(migratedSubs, sub)
	}
	if needsMigration {
		bakPath := fmt.Sprintf("%s.premigrate-%d", m.subsPath, fromVersion)
		if info, statErr := os.Stat(bakPath); os.IsNotExist(statErr) || (statErr == nil && info.IsDir()) {
			if _, err := storage.BackupBeforeMigration(m.subsPath, fromVersion); err != nil {
				m.rules = prevRules
				return fmt.Errorf("subscription: back up %s before migration: %w", filepath.Base(m.subsPath), err)
			}
		}
		out := subscriptionsFile{
			Version:       SubscriptionsSchemaVersion,
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
	m.invalidateSubsCacheLocked()
	if rebound && !needsMigration {
		// Persist the reset default rule and the rules that keep its former
		// subscriptions' behaviour, so the generated rule IDs stay stable.
		return m.writeSubscriptionsToDiskLocked()
	}
	return nil
}

func (m *Manager) writeSubscriptionsToDiskLocked() (writeErr error) {
	if m.subsPath == "" {
		return nil
	}
	subs, err := m.listSubsOnlyLocked()
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
		Version:       SubscriptionsSchemaVersion,
		Subscriptions: subs,
		CacheRules:    m.rules,
	}
	data, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	if m.diskWriteHook != nil {
		if err := m.diskWriteHook(); err != nil {
			return err
		}
	}

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
	subs, err := m.listSubsOnlyLocked()
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
		site, uid string
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
	rowErr := rows.Err()
	_ = rows.Close()
	if rowErr != nil || len(toDelete) == 0 {
		return
	}

	tx, err := m.updatesDB.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	for _, c := range toDelete {
		if _, err := tx.Exec(`DELETE FROM update_item WHERE site = ? AND uid = ?`, c.site, c.uid); err != nil {
			return
		}
		if _, err := tx.Exec(`DELETE FROM account_baseline_pinned WHERE site = ? AND uid = ?`, c.site, c.uid); err != nil {
			return
		}
		if _, err := tx.Exec(`DELETE FROM account_works WHERE site = ? AND uid = ?`, c.site, c.uid); err != nil {
			return
		}
		if _, err := tx.Exec(`DELETE FROM account_state WHERE site = ? AND uid = ?`, c.site, c.uid); err != nil {
			return
		}
	}
	_ = tx.Commit()
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
			s.Accounts[i].Site = string(site.Bilibili)
		}
		switch site.ID(s.Accounts[i].Site) {
		case site.Bilibili:
			if id, err := strconv.ParseInt(s.Accounts[i].UID, 10, 64); err == nil && id > 0 {
				s.Accounts[i].UID = strconv.FormatInt(id, 10)
			}
		case site.Weibo:
			if norm, err := site.NormalizeUID(site.Weibo, s.Accounts[i].UID); err == nil {
				s.Accounts[i].UID = norm
			}
		case site.X:
			if norm, err := site.NormalizeUID(site.X, s.Accounts[i].UID); err == nil {
				s.Accounts[i].UID = norm
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
	SortAccountsBySiteOrder(s.Accounts)
	if len(s.Accounts) > 0 {
		primary := &s.Accounts[0]
		if primary.Site == "" {
			primary.Site = string(site.Bilibili)
		}
		if primary.AddedAt == "" {
			primary.AddedAt = s.CreatedAt
		}
		s.Site = primary.Site
		if primary.Site == string(site.Bilibili) {
			s.Mid, _ = strconv.ParseInt(primary.UID, 10, 64)
		} else {
			s.Mid = 0
		}
		avatarFace := primary.Face
		avatarLinked := false
		for _, account := range s.Accounts {
			if account.Site == s.Avatar.Site && account.UID == s.Avatar.UID {
				avatarLinked = true
				avatarFace = account.Face
				break
			}
		}
		if !avatarLinked {
			s.Avatar = AvatarRef{Site: primary.Site, UID: primary.UID}
		}
		s.Uname, s.Face = primary.Name, avatarFace
	} else {
		s.Avatar = AvatarRef{}
		s.Face = ""
	}
	if s.Name == "" {
		if len(s.Accounts) > 0 {
			switch site.ID(s.Accounts[0].Site) {
			case site.Bilibili:
				s.Name = firstNonEmptyStr(s.Uname, "UID "+s.Accounts[0].UID)
			case site.Douyin:
				s.Name = firstNonEmptyStr(s.Uname, firstNonEmptyStr(s.Accounts[0].UniqueID, s.Accounts[0].UID))
			case site.Weibo:
				s.Name = firstNonEmptyStr(s.Uname, "微博用户 "+s.Accounts[0].UID)
			case site.X:
				fallback := "X 用户 " + s.Accounts[0].UID
				if handle := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s.Accounts[0].UniqueID), "@")); handle != "" {
					fallback = "@" + handle
				}
				s.Name = firstNonEmptyStr(s.Uname, fallback)
			default:
				s.Name = firstNonEmptyStr(s.Uname, s.Accounts[0].UID)
			}
		} else if s.Mid > 0 {
			s.Name = firstNonEmptyStr(s.Uname, fmt.Sprintf("UID %d", s.Mid))
		} else {
			s.Name = firstNonEmptyStr(s.Uname, "未命名订阅")
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
	// Always default SubID to the unique immutable ID so unlinking/moving a
	// primary Bilibili account can never cause a primary-key collision later.
	if s.SubID == "" {
		s.SubID = s.ID
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
