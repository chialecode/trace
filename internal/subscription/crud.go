package subscription

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
)

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
	before, snapshotErr := m.listSubsOnlyLocked()
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
	id := hex.EncodeToString(rnd[:])
	sub := Subscription{
		ID:        id,
		SubID:     id,
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
func (m *Manager) OwnerOf(siteName, uid string) (Subscription, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ownerOfLocked(siteName, uid)
}

func (m *Manager) ownerOfLocked(siteName, uid string) (Subscription, bool) {
	siteID, ok := site.ParseID(siteName)
	if !ok {
		return Subscription{}, false
	}
	normSite := string(siteID)
	if normUID, err := site.NormalizeUID(siteID, uid); err == nil {
		uid = normUID
	}
	if err := m.ensureSubsCacheLocked(); err != nil {
		return Subscription{}, false
	}
	if idx, ok := m.ownerIndex[normSite+":"+uid]; ok && idx >= 0 && idx < len(m.subsCache) {
		return cloneSubscription(m.subsCache[idx]), true
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

// rekeyLegacySubIDLocked migrates a legacy "<site>:<uid>" primary key to sub.ID
// whenever that account is unlinked or moved away so the old key can never
// collide with a future subscription for the same account.
func (m *Manager) rekeyLegacySubIDLocked(sub *Subscription, removedSite, removedUID string) error {
	legacyKey := removedSite + ":" + removedUID
	if sub.SubID == "" || sub.SubID == sub.ID || sub.SubID != legacyKey {
		return nil
	}
	oldSubID := sub.SubID
	sub.SubID = sub.ID
	if _, err := m.db.Exec(`DELETE FROM subscriptions WHERE sub_id = ?`, oldSubID); err != nil {
		return err
	}
	m.invalidateSubsCacheLocked()
	return nil
}

// Unlink removes <site>:<uid> from subscription subID while preserving its account_state and update_item rows in updates.db (subscriptions.md §2).
func (m *Manager) Unlink(subID, siteName, uid string) (result Subscription, resultErr error) {
	siteID, ok := site.ParseID(siteName)
	if !ok {
		return Subscription{}, fmt.Errorf("subscription: 不支持的站点 %q", siteName)
	}
	normSite := string(siteID)
	if normUID, err := site.NormalizeUID(siteID, uid); err == nil {
		uid = normUID
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	before, snapshotErr := m.listSubsOnlyLocked()
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
		if a.Site == normSite && a.UID == uid {
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
	if err := m.rekeyLegacySubIDLocked(&target, normSite, uid); err != nil {
		return Subscription{}, err
	}
	nowStr := m.nowFunc().UTC().Format(time.RFC3339)
	if _, err := m.updatesDB.Exec(`UPDATE account_state SET unlinked_at = ? WHERE site = ? AND uid = ?`, nowStr, normSite, uid); err != nil {
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

// Move atomically moves <site>:<uid> from its current owner subscription to toSubID under a single lock and single file write.
func (m *Manager) Move(siteName, uid, toSubID string) (result Subscription, resultErr error) {
	normAcc, err := NormalizeAndValidateAccount(AccountRef{Site: siteName, UID: uid})
	if err != nil {
		return Subscription{}, err
	}
	normSite, normUID := normAcc.Site, normAcc.UID
	m.mu.Lock()
	defer m.mu.Unlock()
	before, snapshotErr := m.listSubsOnlyLocked()
	if snapshotErr != nil {
		return Subscription{}, snapshotErr
	}
	beforeRules := slices.Clone(m.rules)
	defer func() {
		if resultErr != nil {
			m.rules = beforeRules
			resultErr = errors.Join(resultErr, m.restoreSubscriptionViewLocked(before))
		}
	}()

	dest, ok, err := m.findByIDOrSubIDLocked(toSubID)
	if err != nil || !ok {
		return Subscription{}, fmt.Errorf("subscription: destination %s not found", toSubID)
	}
	for _, account := range dest.Accounts {
		if normalizeLegacyAccountSite(account.Site) == normSite && account.UID == normUID {
			return dest, nil
		}
	}
	if len(dest.Accounts) >= 20 {
		return Subscription{}, fmt.Errorf("subscription: destination subscription already has 20 accounts")
	}

	now := m.nowFunc()
	nowStr := now.UTC().Format(time.RFC3339)

	var movedAcc AccountRef
	foundAcc := false
	owner, owned := m.ownerOfLocked(normSite, normUID)
	if !owned {
		return Subscription{}, fmt.Errorf("subscription: 待迁移账号尚未关联订阅")
	}
	if owner.ID != dest.ID && owner.SubID != dest.SubID {
		var kept []AccountRef
		for _, a := range owner.Accounts {
			if normalizeLegacyAccountSite(a.Site) == normSite && a.UID == normUID {
				movedAcc = a
				movedAcc.Site = normSite
				movedAcc.UID = normUID
				foundAcc = true
				continue
			}
			kept = append(kept, a)
		}
		owner.Accounts = kept
		if len(kept) > 0 && normalizeLegacyAccountSite(kept[0].Site) == string(site.Bilibili) {
			if parsedMid, pErr := strconv.ParseInt(kept[0].UID, 10, 64); pErr == nil {
				owner.Mid = parsedMid
			}
		} else {
			owner.Mid = 0
		}
		if err := m.rekeyLegacySubIDLocked(&owner, normSite, normUID); err != nil {
			return Subscription{}, err
		}
		owner.UpdatedAt = nowStr
		syncSubFields(&owner, now)
		if err := m.upsertRowLocked(owner); err != nil {
			return Subscription{}, err
		}
	}

	if !foundAcc {
		movedAcc = AccountRef{
			Site:    normSite,
			UID:     normUID,
			AddedAt: nowStr,
		}
	}
	alreadyInDest := false
	for _, a := range dest.Accounts {
		if normalizeLegacyAccountSite(a.Site) == normSite && a.UID == normUID {
			alreadyInDest = true
			break
		}
	}
	if !alreadyInDest {
		dest.Accounts = append(dest.Accounts, movedAcc)
	}
	dest.UpdatedAt = nowStr
	syncSubFields(&dest, now)
	if err := m.upsertRowLocked(dest); err != nil {
		return Subscription{}, err
	}
	if err := m.writeSubscriptionsToDiskLocked(); err != nil {
		return Subscription{}, err
	}
	return dest, nil
}

// MoveToNew atomically moves <site>:<uid> from its current owner subscription into a
// newly created subscription under a single lock and a single subscriptions.json write.
// Account metadata (UniqueID, WebRID, Name, Face, AddedAt) and updates.db state/items
// remain intact with zero network requests and without setting unlinked_at.
func (m *Manager) MoveToNew(siteName, uid, name, note string, rules SubRules) (result Subscription, resultErr error) {
	if err := validateSubscriptionInput(name, note, nil); err != nil {
		return Subscription{}, err
	}
	if strings.TrimSpace(name) == "" {
		return Subscription{}, fmt.Errorf("subscription: name is required")
	}
	normAcc, err := NormalizeAndValidateAccount(AccountRef{Site: siteName, UID: uid})
	if err != nil {
		return Subscription{}, err
	}
	normSite, normUID := normAcc.Site, normAcc.UID

	m.mu.Lock()
	defer m.mu.Unlock()
	before, snapshotErr := m.listSubsOnlyLocked()
	if snapshotErr != nil {
		return Subscription{}, snapshotErr
	}
	beforeRules := slices.Clone(m.rules)
	defer func() {
		if resultErr != nil {
			m.rules = beforeRules
			resultErr = errors.Join(resultErr, m.restoreSubscriptionViewLocked(before))
		}
	}()

	owner, owned := m.ownerOfLocked(normSite, normUID)
	if !owned {
		return Subscription{}, fmt.Errorf("subscription: 待迁移账号尚未关联订阅")
	}

	now := m.nowFunc()
	nowStr := now.UTC().Format(time.RFC3339)

	var movedAcc AccountRef
	foundAcc := false
	var kept []AccountRef
	for _, a := range owner.Accounts {
		if normalizeLegacyAccountSite(a.Site) == normSite && a.UID == normUID {
			movedAcc = a
			movedAcc.Site = normSite
			movedAcc.UID = normUID
			foundAcc = true
			continue
		}
		kept = append(kept, a)
	}
	if !foundAcc {
		movedAcc = AccountRef{
			Site:    normSite,
			UID:     normUID,
			AddedAt: nowStr,
		}
	}

	owner.Accounts = kept
	if len(kept) > 0 && normalizeLegacyAccountSite(kept[0].Site) == string(site.Bilibili) {
		if parsedMid, pErr := strconv.ParseInt(kept[0].UID, 10, 64); pErr == nil {
			owner.Mid = parsedMid
		}
	} else {
		owner.Mid = 0
	}
	if err := m.rekeyLegacySubIDLocked(&owner, normSite, normUID); err != nil {
		return Subscription{}, err
	}
	owner.UpdatedAt = nowStr
	syncSubFields(&owner, now)
	if err := m.upsertRowLocked(owner); err != nil {
		return Subscription{}, err
	}

	var rnd [16]byte
	_, _ = rand.Read(rnd[:])
	id := hex.EncodeToString(rnd[:])
	sub := Subscription{
		ID:             id,
		SubID:          id,
		Name:           strings.TrimSpace(name),
		Note:           strings.TrimSpace(note),
		Avatar:         AvatarRef{Site: movedAcc.Site, UID: movedAcc.UID},
		CreatedAt:      nowStr,
		UpdatedAt:      nowStr,
		Accounts:       []AccountRef{movedAcc},
		Rules:          rules,
		Enabled:        rules.ScheduledCheck,
		LiveAutoRecord: rules.AutoRecord,
	}
	if sub.Rules.AutoCache.Enabled && sub.Rules.AutoCache.Since == "" {
		sub.Rules.AutoCache.Since = nowStr
	}
	syncSubFields(&sub, now)
	m.bindRuleLocked(&sub)
	if err := m.upsertRowLocked(sub); err != nil {
		return Subscription{}, err
	}
	if err := m.writeSubscriptionsToDiskLocked(); err != nil {
		return Subscription{}, err
	}
	return sub, nil
}

// SetRules updates the automation rules for subID, setting or clearing Rules.AutoCache.Since per subscriptions.md §4.
func (m *Manager) SetRules(subID string, rules SubRules) (result Subscription, resultErr error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	before, snapshotErr := m.listSubsOnlyLocked()
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
	// Legacy v1 callers of Upsert may omit AccountRef.Site for Bilibili accounts;
	// non-empty unknown site identifiers are still rejected by NormalizeAndValidateAccount.
	if len(in.Accounts) > 0 {
		accs := make([]AccountRef, len(in.Accounts))
		copy(accs, in.Accounts)
		for i := range accs {
			if strings.TrimSpace(accs[i].Site) == "" {
				accs[i].Site = string(site.Bilibili)
			}
		}
		in.Accounts = accs
	}
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
	before, snapshotErr := m.listSubsOnlyLocked()
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
	var existing Subscription
	var found bool
	if in.SubID != "" {
		existing, found, _ = m.findByIDOrSubIDLocked(in.SubID)
	} else if in.ID != "" {
		existing, found, _ = m.findByIDOrSubIDLocked(in.ID)
	} else if in.Mid > 0 {
		existing, found = m.ownerOfLocked("bilibili", strconv.FormatInt(in.Mid, 10))
	}
	if found {
		if in.ID == "" {
			in.ID = existing.ID
		}
		if in.SubID == "" {
			in.SubID = existing.SubID
		}
		if in.CreatedAt == "" {
			in.CreatedAt = existing.CreatedAt
		}
		if len(in.Accounts) == 0 {
			in.Accounts = existing.Accounts
		}
		// Respect explicit toggle changes on either legacy (Enabled/LiveAutoRecord)
		// or modern (Rules.ScheduledCheck/AutoRecord) fields before syncSubFields ORs them.
		if in.Enabled != existing.Enabled && in.Rules.ScheduledCheck == existing.Rules.ScheduledCheck {
			in.Rules.ScheduledCheck = in.Enabled
		} else if in.Rules.ScheduledCheck != existing.Rules.ScheduledCheck && in.Enabled == existing.Enabled {
			in.Enabled = in.Rules.ScheduledCheck
		}
		if in.LiveAutoRecord != existing.LiveAutoRecord && in.Rules.AutoRecord == existing.Rules.AutoRecord {
			in.Rules.AutoRecord = in.LiveAutoRecord
		} else if in.Rules.AutoRecord != existing.Rules.AutoRecord && in.LiveAutoRecord == existing.LiveAutoRecord {
			in.LiveAutoRecord = in.Rules.AutoRecord
		}
		// Preserve existing AutoCache rule when caller passed a legacy Upsert
		// without AutoCache fields (and did not set AutoCache="none"), without
		// clobbering ScheduledCheck / AutoRecord toggles.
		if !in.Rules.AutoCache.Enabled && len(in.Rules.AutoCache.Types) == 0 && in.Rules.AutoCache.Since == "" &&
			in.AutoCache != "none" && existing.Rules.AutoCache.Enabled {
			in.Rules.AutoCache = existing.Rules.AutoCache
		} else if in.Rules.AutoCache.Enabled && in.Rules.AutoCache.Since == "" && existing.Rules.AutoCache.Since != "" {
			in.Rules.AutoCache.Since = existing.Rules.AutoCache.Since
		}
	}
	syncSubFields(&in, now)
	for _, account := range in.Accounts {
		if owner, exists := m.ownerOfLocked(account.Site, account.UID); exists && owner.ID != in.ID {
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
	err := upsertSubscriptionRow(m.db, in)
	if err == nil {
		m.invalidateSubsCacheLocked()
	}
	return err
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

// ImportAccountRefsResult summarizes a multi-site following import batch.
type ImportAccountRefsResult struct {
	Created int `json:"created"`
	Skipped int `json:"skipped"`
}

// ImportAccountRefs creates one subscription per unowned account in accounts
// using the supplied name/face snapshots with zero extra network requests.
// Already-subscribed accounts (matched by canonical <site>:<uid>) and
// duplicates within the same batch are skipped without touching existing
// subscriptions or watermarks. All new subscriptions are persisted atomically
// in a single subscriptions.json write.
func (m *Manager) ImportAccountRefs(accounts []AccountRef, rules SubRules) (result ImportAccountRefsResult, resultErr error) {
	if len(accounts) == 0 {
		return ImportAccountRefsResult{}, nil
	}
	normalized := make([]AccountRef, 0, len(accounts))
	for _, acc := range accounts {
		norm, err := NormalizeAndValidateAccount(acc)
		if err != nil {
			return ImportAccountRefsResult{}, err
		}
		normalized = append(normalized, norm)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	before, snapshotErr := m.listSubsOnlyLocked()
	if snapshotErr != nil {
		return ImportAccountRefsResult{}, snapshotErr
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, m.restoreSubscriptionViewLocked(before))
		}
	}()

	now := m.nowFunc()
	nowStr := now.UTC().Format(time.RFC3339)
	seen := make(map[string]bool, len(normalized))
	for _, norm := range normalized {
		key := norm.Site + ":" + norm.UID
		if seen[key] {
			result.Skipped++
			continue
		}
		seen[key] = true
		if _, owned := m.ownerOfLocked(norm.Site, norm.UID); owned {
			result.Skipped++
			continue
		}
		if norm.AddedAt == "" {
			norm.AddedAt = nowStr
		}
		var rnd [16]byte
		_, _ = rand.Read(rnd[:])
		id := hex.EncodeToString(rnd[:])
		subRules := rules
		if subRules.AutoCache.Enabled && subRules.AutoCache.Since == "" {
			subRules.AutoCache.Since = nowStr
		}
		sub := Subscription{
			ID:             id,
			SubID:          id,
			Name:           strings.TrimSpace(norm.Name),
			CreatedAt:      nowStr,
			UpdatedAt:      nowStr,
			Accounts:       []AccountRef{norm},
			Rules:          subRules,
			Enabled:        subRules.ScheduledCheck,
			LiveAutoRecord: subRules.AutoRecord,
		}
		syncSubFields(&sub, now)
		if err := m.upsertRowLocked(sub); err != nil {
			return ImportAccountRefsResult{}, err
		}
		result.Created++
	}
	if result.Created > 0 {
		if err := m.writeSubscriptionsToDiskLocked(); err != nil {
			return ImportAccountRefsResult{}, err
		}
	}
	return result, nil
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
			uidStr := strconv.FormatInt(u.Mid, 10)
			matched := false
			for i := range existing.Accounts {
				if normalizeLegacyAccountSite(existing.Accounts[i].Site) == string(site.Bilibili) && existing.Accounts[i].UID == uidStr {
					existing.Accounts[i].Name = existing.Uname
					existing.Accounts[i].Face = firstNonEmptyStr(u.Face, existing.Accounts[i].Face)
					matched = true
				}
			}
			if !matched && len(existing.Accounts) > 0 {
				existing.Accounts[0].Name = existing.Uname
				existing.Accounts[0].Face = existing.Face
			}
			if _, err := m.Upsert(existing); err != nil {
				return count, err
			}
			count++
			continue
		}
		// New subscriptions start on the built-in rule: no scheduled check
		// unless the caller asks for automatic caching or recording.
		_, err := m.Upsert(Subscription{
			Site:           "bilibili",
			Mid:            u.Mid,
			Name:           firstNonEmptyStr(u.Uname, fmt.Sprintf("UID %d", u.Mid)),
			Uname:          firstNonEmptyStr(u.Uname, fmt.Sprintf("UID %d", u.Mid)),
			Face:           u.Face,
			Sign:           u.Sign,
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
// Missing IDs are a safe no-op and never delete unrelated subscriptions with mid = 0.
func (m *Manager) Delete(subIDOrMid string) (resultErr error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	before, snapshotErr := m.listSubsOnlyLocked()
	if snapshotErr != nil {
		return snapshotErr
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, m.restoreSubscriptionViewLocked(before))
		}
	}()

	sub, ok, err := m.findByIDOrSubIDLocked(subIDOrMid)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	nowStr := m.nowFunc().UTC().Format(time.RFC3339)
	for _, a := range sub.Accounts {
		if _, err := m.updatesDB.Exec(`UPDATE account_state SET unlinked_at = ? WHERE site = ? AND uid = ?`, nowStr, a.Site, a.UID); err != nil {
			return err
		}
	}
	if _, err := m.db.Exec(`DELETE FROM subscriptions WHERE sub_id = ?`, sub.SubID); err != nil {
		return err
	}
	m.invalidateSubsCacheLocked()
	return m.writeSubscriptionsToDiskLocked()
}

// List returns all subscriptions ordered by created_at ASC.
func (m *Manager) List() ([]Subscription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listLocked()
}

func (m *Manager) listLocked() ([]Subscription, error) {
	out, err := m.listSubsOnlyLocked()
	if err != nil {
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
	if mid <= 0 {
		return Subscription{}, false, nil
	}
	uidStr := strconv.FormatInt(mid, 10)
	if owner, ok := m.ownerOfLocked("bilibili", uidStr); ok {
		return owner, true, nil
	}
	return m.getLocked(fmt.Sprintf("bilibili:%d", mid))
}

func (m *Manager) findByIDOrSubIDLocked(idOrSubID string) (Subscription, bool, error) {
	idOrSubID = strings.TrimSpace(idOrSubID)
	if idOrSubID == "" {
		return Subscription{}, false, nil
	}
	if err := m.ensureSubsCacheLocked(); err != nil {
		return Subscription{}, false, err
	}
	if idx, ok := m.idIndex[idOrSubID]; ok && idx >= 0 && idx < len(m.subsCache) {
		return cloneSubscription(m.subsCache[idx]), true, nil
	}
	trimmed := strings.TrimPrefix(idOrSubID, "bilibili:")
	if mid, err := strconv.ParseInt(trimmed, 10, 64); err == nil && mid > 0 {
		if idx, ok := m.ownerIndex[fmt.Sprintf("bilibili:%d", mid)]; ok && idx >= 0 && idx < len(m.subsCache) {
			return cloneSubscription(m.subsCache[idx]), true, nil
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
