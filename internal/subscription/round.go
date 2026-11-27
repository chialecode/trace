package subscription

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/site"
)

const (
	// DefaultBilibiliRoundAccountCap is the maximum Bilibili accounts checked in one scheduled round.
	DefaultBilibiliRoundAccountCap = 30
	// DefaultDouyinRoundAccountCap is the maximum Douyin accounts checked in one scheduled round.
	DefaultDouyinRoundAccountCap = 20
	// DefaultWeiboRoundAccountCap is the maximum Weibo accounts checked in one scheduled round (DEC-089).
	DefaultWeiboRoundAccountCap = 25
	// DefaultXRoundAccountCap is the maximum X accounts checked in one scheduled round.
	DefaultXRoundAccountCap = 15

	// DefaultBilibiliAccountInterval is the minimum spacing between two Bilibili accounts in a check round.
	DefaultBilibiliAccountInterval = 2 * time.Second
	// DefaultDouyinAccountInterval is the minimum spacing between two Douyin accounts in a check round.
	DefaultDouyinAccountInterval = 2500 * time.Millisecond
	// DefaultWeiboAccountInterval is the minimum spacing between two Weibo accounts in a check round (DEC-089).
	DefaultWeiboAccountInterval = 1500 * time.Millisecond
	// DefaultXAccountInterval is the minimum spacing between two X accounts in a check round.
	DefaultXAccountInterval = 5 * time.Second
)

// AutoCacheEnqueueFunc enqueues an automatic cache task (with source="auto") for a discovered update item
// and returns the created download task ID.
type AutoCacheEnqueueFunc func(ctx context.Context, item UpdateItem) (taskID string, err error)

type accountCheckOutcome struct {
	newVideos      int
	newDynamics    int
	autoCacheTasks int
	gapDetected    bool
	incomplete     bool
	incompleteNote string
}

func defaultSubscriptionSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// SiteScheduledRoundCap returns the per-round scheduled account limit for siteName.
// Unknown or empty site names return 0 rather than falling back to Bilibili.
func SiteScheduledRoundCap(siteName string) int {
	switch normalizeSchedulerSite(siteName) {
	case string(site.Bilibili):
		return DefaultBilibiliRoundAccountCap
	case string(site.Douyin):
		return DefaultDouyinRoundAccountCap
	case string(site.Weibo):
		return DefaultWeiboRoundAccountCap
	case string(site.X):
		return DefaultXRoundAccountCap
	default:
		return 0
	}
}

// RegisterFetcher registers a cross-site SubscriptionFetcher (such as Douyin's adapter) for update checks.
func (m *Manager) RegisterFetcher(fetcher site.SubscriptionFetcher) {
	if fetcher == nil {
		return
	}
	m.mu.Lock()
	if m.fetchers == nil {
		m.fetchers = make(map[site.ID]site.SubscriptionFetcher)
	}
	m.fetchers[fetcher.SiteID()] = fetcher
	m.mu.Unlock()
}

// SetAutoCacheGate installs a check run before automatic caching starts
// tasks; while it returns an error no new task starts (remote.md §6.2).
func (m *Manager) SetAutoCacheGate(fn func() error) {
	m.mu.Lock()
	m.autoCacheGate = fn
	m.mu.Unlock()
}

// SetSiteAuthCheck registers a callback reporting whether a given site currently has an active login session.
func (m *Manager) SetSiteAuthCheck(fn func(siteID site.ID) bool) {
	m.mu.Lock()
	m.siteAuthCheck = fn
	m.mu.Unlock()
}

// RegisterAutoCacheEnqueuer registers a per-site automatic cache task enqueuer (used for Douyin video/note works,
// or overriding Bilibili in tests).
func (m *Manager) RegisterAutoCacheEnqueuer(siteID site.ID, fn AutoCacheEnqueueFunc) {
	m.mu.Lock()
	if m.autoCacheEnqueuers == nil {
		m.autoCacheEnqueuers = make(map[site.ID]AutoCacheEnqueueFunc)
	}
	if fn == nil {
		delete(m.autoCacheEnqueuers, siteID)
	} else {
		m.autoCacheEnqueuers[siteID] = fn
	}
	m.mu.Unlock()
}

// SetOnAuthorAvatar registers a callback with the avatar URL a check saw for
// an account, before it is sanitized: Douyin avatar URLs carry query strings
// and are never stored, so the caller can keep a local copy of the image.
func (m *Manager) SetOnAuthorAvatar(fn func(siteID, uid, rawFace string)) {
	m.mu.Lock()
	m.onAuthorAvatar = fn
	m.mu.Unlock()
}

// SetOnLiveHint registers a callback invoked with 0 extra network requests when Douyin's page-1
// creator posts response carries an active LiveRoomID or LiveWebRID.
func (m *Manager) SetOnLiveHint(fn func(acc AccountRef, roomID, webRID string)) {
	m.mu.Lock()
	m.onLiveHint = fn
	m.mu.Unlock()
}

// SetSiteLiveCheck registers a multi-site live status check hook invoked at the end of a round for non-aborted sites.
func (m *Manager) SetSiteLiveCheck(fn func(ctx context.Context, accounts []AccountRef) error) {
	m.mu.Lock()
	m.siteLiveCheck = fn
	m.mu.Unlock()
}

// SetSiteAccountInterval overrides the inter-account check spacing for a specific site.
func (m *Manager) SetSiteAccountInterval(siteName string, d time.Duration) {
	norm := normalizeSchedulerSite(siteName)
	if norm == "" {
		return
	}
	m.mu.Lock()
	if m.siteIntervals == nil {
		m.siteIntervals = make(map[string]time.Duration)
	}
	m.siteIntervals[norm] = d
	if norm == string(site.Bilibili) {
		m.accountInterval = d
	}
	m.mu.Unlock()
}

// SiteAccountInterval returns the effective inter-account check spacing for siteName.
func (m *Manager) SiteAccountInterval(siteName string) time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.siteAccountIntervalLocked(siteName)
}

func (m *Manager) siteAccountIntervalLocked(siteName string) time.Duration {
	norm := normalizeSchedulerSite(siteName)
	if norm == "" {
		return 0
	}
	if m.siteIntervals != nil {
		if d, ok := m.siteIntervals[norm]; ok {
			return d
		}
	}
	switch site.ID(norm) {
	case site.Bilibili:
		return m.accountInterval
	case site.Douyin:
		if m.accountIntervalTouched && m.accountInterval == 0 {
			return 0
		}
		return DefaultDouyinAccountInterval
	case site.Weibo:
		if m.accountIntervalTouched && m.accountInterval == 0 {
			return 0
		}
		return DefaultWeiboAccountInterval
	case site.X:
		if m.accountIntervalTouched && m.accountInterval == 0 {
			return 0
		}
		return DefaultXAccountInterval
	default:
		return 0
	}
}

// SetSleepFunc overrides the inter-account pacing sleep function (for deterministic tests).
func (m *Manager) SetSleepFunc(fn func(ctx context.Context, d time.Duration) error) {
	m.mu.Lock()
	if fn == nil {
		m.sleepFunc = defaultSubscriptionSleep
	} else {
		m.sleepFunc = fn
	}
	m.mu.Unlock()
}

// PreviewResolvedAccount validates and normalizes a resolved AccountRef and reports its current owning Subscription, if any.
func (m *Manager) PreviewResolvedAccount(acc AccountRef) (AccountRef, *Subscription, error) {
	norm, err := NormalizeAndValidateAccount(acc)
	if err != nil {
		return AccountRef{}, nil, err
	}
	if owner, ok := m.OwnerOf(norm.Site, norm.UID); ok {
		ownerCopy := owner
		return norm, &ownerCopy, nil
	}
	return norm, nil, nil
}

// GetAccount looks up an AccountRef and its owning Subscription by <site, uid>.
func (m *Manager) GetAccount(siteName, uid string) (Subscription, AccountRef, bool) {
	siteID, ok := site.ParseID(siteName)
	if !ok {
		return Subscription{}, AccountRef{}, false
	}
	normSite := string(siteID)
	if normUID, err := site.NormalizeUID(siteID, uid); err == nil {
		uid = normUID
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	owner, ok := m.ownerOfLocked(normSite, uid)
	if !ok {
		return Subscription{}, AccountRef{}, false
	}
	for _, a := range owner.Accounts {
		if a.Site == normSite && a.UID == uid {
			return owner, a, true
		}
	}
	return owner, AccountRef{Site: normSite, UID: uid, Name: owner.Uname, Face: owner.Face}, true
}

// UpdateAccountSnapshot updates opportunistic profile snapshot fields (name, face, uniqueId) for an existing account
// without altering ownership or clearing webRid.
func (m *Manager) UpdateAccountSnapshot(siteName, uid, name, face, uniqueID string) (resultErr error) {
	normAcc, err := NormalizeAndValidateAccount(AccountRef{
		Site:     siteName,
		UID:      uid,
		Name:     name,
		Face:     face,
		UniqueID: uniqueID,
	})
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	before, snapshotErr := m.listSubsOnlyLocked()
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
		return nil
	}
	changed := false
	for i := range owner.Accounts {
		if owner.Accounts[i].Site == normAcc.Site && owner.Accounts[i].UID == normAcc.UID {
			if normAcc.Name != "" && owner.Accounts[i].Name != normAcc.Name {
				owner.Accounts[i].Name = normAcc.Name
				changed = true
			}
			if normAcc.Face != "" && owner.Accounts[i].Face != normAcc.Face {
				owner.Accounts[i].Face = normAcc.Face
				changed = true
			}
			if (normAcc.Site == string(site.Douyin) || normAcc.Site == string(site.X)) && normAcc.UniqueID != "" && owner.Accounts[i].UniqueID != normAcc.UniqueID {
				owner.Accounts[i].UniqueID = normAcc.UniqueID
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

// ListAutoRecordAccounts returns all linked accounts on subscriptions that have Rules.AutoRecord enabled,
// optionally filtered by siteName (empty siteName returns all live-capable sites; Weibo and X are always excluded).
func (m *Manager) ListAutoRecordAccounts(siteName string) []AccountRef {
	rawFilter := strings.ToLower(strings.TrimSpace(siteName))
	var filterSite string
	if rawFilter != "" {
		id, ok := site.ParseID(rawFilter)
		if !ok || id == site.Weibo || id == site.X {
			return nil
		}
		filterSite = string(id)
	}
	m.mu.Lock()
	subs, err := m.listSubsOnlyLocked()
	m.mu.Unlock()
	if err != nil {
		return nil
	}
	var out []AccountRef
	for _, s := range subs {
		if !s.Rules.AutoRecord && !s.LiveAutoRecord {
			continue
		}
		for _, a := range s.Accounts {
			accSite := normalizeLegacyAccountSite(a.Site)
			if accSite == string(site.Weibo) || accSite == string(site.X) || !site.ID(accSite).Valid() {
				continue
			}
			if filterSite != "" && accSite != filterSite {
				continue
			}
			cp := a
			cp.Site = accSite
			out = append(out, cp)
		}
	}
	return out
}

// ListSiteSchedulerStates returns the persisted scheduler backoff and quota states for all supported sites.
func (m *Manager) ListSiteSchedulerStates() map[string]SchedulerState {
	m.mu.Lock()
	defer m.mu.Unlock()
	all := site.All()
	out := make(map[string]SchedulerState, len(all))
	for _, s := range all {
		key := string(s)
		out[key] = m.getSiteSchedulerStateLocked(key)
	}
	return out
}

func (m *Manager) isSiteAuthenticated(siteID site.ID) bool {
	m.mu.Lock()
	authFn := m.siteAuthCheck
	hasFetcher := m.fetchers != nil && m.fetchers[siteID] != nil
	client := m.client
	m.mu.Unlock()

	if authFn != nil {
		return authFn(siteID)
	}
	if siteID == site.Bilibili {
		if client != nil {
			return client.HasAuth()
		}
		return hasFetcher
	}
	return hasFetcher
}

func (m *Manager) hasAnySiteAuth() bool {
	for _, s := range site.All() {
		if m.isSiteAuthenticated(s) {
			return true
		}
	}
	m.mu.Lock()
	var extraSites []site.ID
	for s := range m.fetchers {
		if !s.Valid() {
			extraSites = append(extraSites, s)
		}
	}
	m.mu.Unlock()
	for _, s := range extraSites {
		if m.isSiteAuthenticated(s) {
			return true
		}
	}
	return false
}

func unloggedSiteSkipMessage(siteName string) string {
	switch normalizeSchedulerSite(siteName) {
	case string(site.Bilibili):
		return "未登录哔哩哔哩，已跳过"
	case string(site.Douyin):
		return "未登录抖音，已跳过"
	case string(site.Weibo):
		return "未登录微博，已跳过"
	case string(site.X):
		return "未登录 X，已跳过"
	default:
		return "未登录" + strings.TrimSpace(siteName) + "，已跳过"
	}
}

func orderedSiteNames(bySite map[string][]roundAccountTarget) []string {
	var out []string
	all := site.All()
	seen := make(map[string]bool, len(all))
	for _, preferred := range all {
		key := string(preferred)
		seen[key] = true
		if len(bySite[key]) > 0 {
			out = append(out, key)
		}
	}
	var extras []string
	for k, v := range bySite {
		if len(v) == 0 || seen[k] {
			continue
		}
		extras = append(extras, k)
	}
	sort.Strings(extras)
	return append(out, extras...)
}

type roundAccountTarget struct {
	subIndex      int
	acc           AccountRef
	lastCheckedAt string
}

// executeMultiSiteRound checks the accounts of all subscriptions, of subID
// only, of the subscriptions with Bilibili account targetMid only, or (with
// accSite and accUID) one account of subID.
func (m *Manager) executeMultiSiteRound(ctx context.Context, targetMid int64, isScheduled bool, subID, accSite, accUID string) (CheckSummary, error) {
	if !m.hasAnySiteAuth() {
		if isScheduled {
			return CheckSummary{}, nil
		}
		if accSite != "" {
			targetSite := site.ID(accSite)
			msg := "请先登录" + targetSite.DisplayName()
			if targetSite == site.X {
				msg = "请先登录 X"
			}
			return CheckSummary{}, &site.Error{
				Site:    targetSite,
				Kind:    site.ErrKindAuthRequired,
				Message: msg,
			}
		}
		if m.client != nil {
			if err := m.client.RequireAuth("subscription:check"); err != nil {
				return CheckSummary{}, err
			}
		}
		return CheckSummary{}, &site.Error{
			Site:    site.Bilibili,
			Kind:    site.ErrKindAuthRequired,
			Message: "请先登录账号",
		}
	}

	m.mu.Lock()
	if m.checking {
		var prev CheckSummary
		if m.lastSummary != nil {
			prev = cloneCheckSummary(*m.lastSummary)
		}
		m.mu.Unlock()
		return prev, nil
	}
	roundCtx, roundCancel := context.WithCancel(ctx)
	m.checking = true
	m.checkCancel = roundCancel
	m.lastSummary = nil
	globalAutoCache := m.subSettings.AutoCacheScope
	sleepFn := m.sleepFunc
	if sleepFn == nil {
		sleepFn = defaultSubscriptionSleep
	}
	subs, err := m.listSubsOnlyLocked()
	accStates, _ := m.listAccountStatesLocked()
	m.mu.Unlock()

	defer func() {
		roundCancel()
		m.mu.Lock()
		m.checking = false
		m.checkCancel = nil
		m.mu.Unlock()
	}()

	if err != nil {
		return CheckSummary{}, err
	}

	roundStart := m.nowFunc().UTC()
	bySite := make(map[string][]roundAccountTarget)
	targetMidStr := ""
	if targetMid > 0 {
		targetMidStr = strconv.FormatInt(targetMid, 10)
	}

	onlySite := accSite
	for i := range subs {
		sub := &subs[i]
		if subID != "" && sub.ID != subID && sub.SubID != subID {
			continue
		}
		if targetMid > 0 && sub.Mid != targetMid {
			hasTarget := false
			for _, a := range sub.Accounts {
				if normalizeLegacyAccountSite(a.Site) == string(site.Bilibili) && a.UID == targetMidStr {
					hasTarget = true
					break
				}
			}
			if !hasTarget {
				continue
			}
		}
		if isScheduled && !sub.Rules.ScheduledCheck {
			continue
		}
		for _, a := range sub.Accounts {
			accSite := normalizeLegacyAccountSite(a.Site)
			if !site.ID(accSite).Valid() {
				continue
			}
			a.Site = accSite
			if targetMid > 0 && (accSite != string(site.Bilibili) || a.UID != targetMidStr) {
				continue
			}
			if accUID != "" && (accSite != onlySite || a.UID != accUID) {
				continue
			}
			st := accStates[accSite+":"+a.UID]
			bySite[accSite] = append(bySite[accSite], roundAccountTarget{
				subIndex:      i,
				acc:           a,
				lastCheckedAt: st.LastCheckedAt,
			})
		}
	}

	siteOrder := orderedSiteNames(bySite)
	if isScheduled && len(siteOrder) == 0 {
		for _, s := range site.All() {
			if m.isSiteAuthenticated(s) {
				if inBackoff, _ := m.IsSiteInBackoff(string(s)); inBackoff {
					return CheckSummary{
						DeferredByBackoff:     true,
						SiteDeferredByBackoff: []string{string(s)},
					}, nil
				}
			}
		}
		return CheckSummary{
			StartedAt:  roundStart.Format(time.RFC3339),
			FinishedAt: roundStart.Format(time.RFC3339),
		}, nil
	}

	summary := CheckSummary{
		StartedAt: roundStart.Format(time.RFC3339),
	}

	abortedSites := make(map[string]string)
	activeBySite := make(map[string][]roundAccountTarget)

	for _, siteName := range siteOrder {
		candidates := bySite[siteName]
		if isScheduled {
			sort.SliceStable(candidates, func(i, j int) bool {
				return candidates[i].lastCheckedAt < candidates[j].lastCheckedAt
			})
			capN := SiteScheduledRoundCap(siteName)
			if capN <= 0 {
				continue
			}
			if len(candidates) > capN {
				candidates = candidates[:capN]
			}
		}

		if !m.isSiteAuthenticated(site.ID(siteName)) {
			skipMsg := unloggedSiteSkipMessage(siteName)
			summary.SkippedUnauthSites = append(summary.SkippedUnauthSites, siteName)
			abortedSites[siteName] = "skipped_unauth"
			for _, tgt := range candidates {
				m.mu.Lock()
				st, _, stErr := m.getAccountStateLocked(siteName, tgt.acc.UID)
				if stErr == nil {
					st.LastKind = "skipped_unauth"
					st.Note = skipMsg
					_ = m.saveAccountStateLocked(st)
				}
				m.mu.Unlock()
			}
			continue
		}

		if isScheduled {
			if inBackoff, _ := m.IsSiteInBackoff(siteName); inBackoff {
				summary.DeferredByBackoff = true
				summary.SiteDeferredByBackoff = append(summary.SiteDeferredByBackoff, siteName)
				abortedSites[siteName] = site.ErrKindBackoff
				continue
			}
		}
		if isScheduled {
			var requiredTokens int
			switch site.ID(siteName) {
			case site.Bilibili:
				requiredTokens = 3*len(candidates) + 2
			case site.Douyin, site.Weibo, site.X:
				requiredTokens = 2 * len(candidates)
			default:
				requiredTokens = 1
			}
			if !m.CanUseSiteQuotaTokens(siteName, requiredTokens) {
				summary.DeferredByQuota = true
				summary.SiteDeferredByQuota = append(summary.SiteDeferredByQuota, siteName)
				abortedSites[siteName] = site.ErrKindQuota
				continue
			}
		}

		activeBySite[siteName] = candidates
		summary.TotalCreators += len(candidates)
	}

	if summary.TotalCreators == 0 && (summary.DeferredByBackoff || summary.DeferredByQuota) {
		summary.FinishedAt = m.nowFunc().UTC().Format(time.RFC3339)
		m.publishProgress(summary)
		return summary, nil
	}

	m.publishProgress(summary)

	consecutiveNetFailures := 0
	wholeRoundAborted := false
	var checkedAccounts []AccountRef
	checkedCountBySite := make(map[string]int)
	siteStopResetAt := make(map[string]time.Time)
	subsDirtyDisk := false

	flushSubsDisk := func() error {
		if !subsDirtyDisk {
			return nil
		}
		m.mu.Lock()
		err := m.writeSubscriptionsToDiskLocked()
		m.mu.Unlock()
		if err == nil {
			subsDirtyDisk = false
		}
		return err
	}

	siteCheckCtx := roundCtx
	if !isScheduled {
		siteCheckCtx = site.WithManualProbe(roundCtx)
	}
	manualWasBackedOff := make(map[string]bool)
	if !isScheduled {
		for _, siteName := range siteOrder {
			if len(activeBySite[siteName]) > 0 {
				if inBackoff, _ := m.IsSiteInBackoff(siteName); inBackoff {
					manualWasBackedOff[siteName] = true
				}
			}
		}
	}

	for _, siteName := range siteOrder {
		if wholeRoundAborted {
			break
		}
		targets := activeBySite[siteName]
		if len(targets) == 0 {
			continue
		}
		siteInterval := m.SiteAccountInterval(siteName)

		for idx, tgt := range targets {
			if roundCtx.Err() != nil {
				wholeRoundAborted = true
				summary.Canceled = true
				break
			}
			if idx > 0 && siteInterval > 0 {
				if err := sleepFn(roundCtx, siteInterval); err != nil {
					wholeRoundAborted = true
					summary.Canceled = true
					break
				}
			}
			if roundCtx.Err() != nil {
				wholeRoundAborted = true
				summary.Canceled = true
				break
			}

			summary.CurrentUID = tgt.acc.UID
			m.publishProgress(summary)

			sub := &subs[tgt.subIndex]
			m.mu.Lock()
			accState, _, stateErr := m.getAccountStateLocked(siteName, tgt.acc.UID)
			m.mu.Unlock()
			if stateErr != nil {
				_ = flushSubsDisk()
				return summary, stateErr
			}

			summary.CheckedCreators++
			checkedCountBySite[siteName]++
			isBaseline := accState.BaselineAt == ""

			outcome, checkErr := m.checkAccountBySite(siteCheckCtx, sub, tgt.acc, &accState, isBaseline, globalAutoCache)
			if roundCtx.Err() != nil {
				wholeRoundAborted = true
				summary.Canceled = true
				break
			}

			nowStr := m.nowFunc().UTC().Format(time.RFC3339)
			sub.LastCheckedAt = nowStr
			accState.LastCheckedAt = nowStr

			if checkErr != nil {
				errKind := site.KindOf(checkErr)
				accState.LastKind = errKind
				sub.ConsecutiveFailures++
				sub.LastError = checkErr.Error()
				summary.FailedCreators++
				summary.Errors = append(summary.Errors, fmt.Sprintf("%s:%s (%s): %v", siteName, tgt.acc.UID, tgt.acc.Name, checkErr))

				m.mu.Lock()
				persistErr := m.saveAccountStateLocked(accState)
				m.mu.Unlock()
				if persistErr != nil {
					_ = flushSubsDisk()
					return summary, persistErr
				}
				if err := m.saveSubStateDB(*sub); err != nil {
					_ = flushSubsDisk()
					return summary, err
				}
				subsDirtyDisk = true

				if errKind == site.ErrKindRiskControl || errKind == site.ErrKindRateLimited || errKind == site.ErrKindAuthRequired {
					summary.StoppedByRiskControl = true
					if summary.SiteStopped == nil {
						summary.SiteStopped = make(map[string]string)
					}
					summary.SiteStopped[siteName] = errKind
					abortedSites[siteName] = errKind
					if resetAt := extractRateLimitResetAt(checkErr); !resetAt.IsZero() {
						siteStopResetAt[siteName] = resetAt
					}
					break
				}
				if siteName == string(site.X) && errKind == site.ErrKindSchemaChanged {
					if summary.SiteStopped == nil {
						summary.SiteStopped = make(map[string]string)
					}
					summary.SiteStopped[siteName] = errKind
					abortedSites[siteName] = errKind
					break
				}
				if errKind == site.ErrKindQuota || errKind == site.ErrKindBackoff {
					abortedSites[siteName] = errKind
					if errKind == site.ErrKindQuota {
						summary.DeferredByQuota = true
						summary.SiteDeferredByQuota = appendUniqueString(summary.SiteDeferredByQuota, siteName)
					} else {
						summary.DeferredByBackoff = true
						summary.SiteDeferredByBackoff = appendUniqueString(summary.SiteDeferredByBackoff, siteName)
					}
					break
				}
				if errKind == site.ErrKindNetwork {
					consecutiveNetFailures++
					if consecutiveNetFailures >= 3 {
						wholeRoundAborted = true
						abortedSites[siteName] = site.ErrKindNetwork
						break
					}
				} else {
					consecutiveNetFailures = 0
				}
				continue
			}

			consecutiveNetFailures = 0
			checkedAccounts = append(checkedAccounts, tgt.acc)
			if !isScheduled && manualWasBackedOff[siteName] && !outcome.incomplete {
				m.ClearSiteBackoff(siteName)
				delete(manualWasBackedOff, siteName)
			} else if isScheduled && siteName == string(site.Weibo) && !outcome.incomplete {
				m.RecordSiteAutomaticSuccess(siteName)
			}

			if outcome.incomplete {
				accState.LastKind = "incomplete"
				accState.Note = outcome.incompleteNote
				summary.IncompleteAccounts = append(summary.IncompleteAccounts, siteName+":"+tgt.acc.UID)
				m.mu.Lock()
				persistErr := m.saveAccountStateLocked(accState)
				m.mu.Unlock()
				if persistErr != nil {
					_ = flushSubsDisk()
					return summary, persistErr
				}
				if err := m.saveSubStateDB(*sub); err != nil {
					_ = flushSubsDisk()
					return summary, err
				}
				subsDirtyDisk = true
				m.publishProgress(summary)
				continue
			}

			if !isBaseline {
				summary.NewVideos += outcome.newVideos
				summary.NewDynamics += outcome.newDynamics
				summary.AutoCacheTasks += outcome.autoCacheTasks
			}

			accState.LastKind = "ok"
			if outcome.gapDetected {
				accState.Note = "可能还有更早的更新没有拿到"
				summary.GapAccounts = append(summary.GapAccounts, siteName+":"+tgt.acc.UID)
			} else {
				accState.Note = ""
			}
			if isBaseline && accState.BaselineAt == "" {
				accState.BaselineAt = nowStr
			}
			m.mu.Lock()
			persistErr := m.saveAccountStateLocked(accState)
			m.mu.Unlock()
			if persistErr != nil {
				_ = flushSubsDisk()
				return summary, persistErr
			}

			sub.LastSuccessAt = nowStr
			sub.ConsecutiveFailures = 0
			sub.LastError = ""
			if err := m.saveSubStateDB(*sub); err != nil {
				_ = flushSubsDisk()
				return summary, err
			}
			subsDirtyDisk = true

			if isBaseline {
				summary.BaselineInitialized++
			}
			m.publishProgress(summary)
		}
	}

	// Flush accumulated subscription profile/status changes to subscriptions.json once per round.
	if err := flushSubsDisk(); err != nil {
		return summary, err
	}

	if !wholeRoundAborted && roundCtx.Err() == nil {
		m.runPostRoundLiveCheck(siteCheckCtx, checkedAccounts, abortedSites, &summary)
	}

	for _, siteName := range siteOrder {
		if len(activeBySite[siteName]) == 0 {
			continue
		}
		stopKind := abortedSites[siteName]
		if err := m.finalizeSiteRoundSchedulerState(siteName, roundStart, stopKind, siteStopResetAt[siteName], checkedCountBySite[siteName], wholeRoundAborted, isScheduled); err != nil {
			return summary, err
		}
	}

	if !wholeRoundAborted && roundCtx.Err() == nil {
		hasNonAbortedSite := false
		for _, siteName := range siteOrder {
			if len(activeBySite[siteName]) > 0 && abortedSites[siteName] == "" {
				hasNonAbortedSite = true
				break
			}
		}
		if hasNonAbortedSite || len(siteOrder) == 0 {
			addedTasks, backlog, err := m.evaluateAutoCacheRulesForSites(roundCtx, abortedSites)
			if err != nil {
				return summary, err
			}
			summary.AutoCacheTasks += addedTasks
			summary.AutoCacheBacklog = backlog
			for sName, reason := range abortedSites {
				switch reason {
				case site.ErrKindRiskControl, site.ErrKindRateLimited, site.ErrKindAuthRequired:
					summary.StoppedByRiskControl = true
					if summary.SiteStopped == nil {
						summary.SiteStopped = make(map[string]string)
					}
					if summary.SiteStopped[sName] == "" {
						summary.SiteStopped[sName] = reason
					}
				case site.ErrKindSchemaChanged:
					if summary.SiteStopped == nil {
						summary.SiteStopped = make(map[string]string)
					}
					if summary.SiteStopped[sName] == "" {
						summary.SiteStopped[sName] = reason
					}
				case site.ErrKindQuota:
					summary.DeferredByQuota = true
					summary.SiteDeferredByQuota = appendUniqueString(summary.SiteDeferredByQuota, sName)
				case site.ErrKindBackoff:
					summary.DeferredByBackoff = true
					summary.SiteDeferredByBackoff = appendUniqueString(summary.SiteDeferredByBackoff, sName)
				}
			}
		}
	}

	summary.CurrentUID = ""
	summary.FinishedAt = m.nowFunc().UTC().Format(time.RFC3339)
	m.publishProgress(summary)
	return summary, nil
}

func (m *Manager) saveSubStateDB(sub Subscription) error {
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
				if account.Name != "" {
					current.Accounts[i].Name = account.Name
				}
				if account.Face != "" {
					current.Accounts[i].Face = account.Face
				}
				if account.UniqueID != "" {
					current.Accounts[i].UniqueID = account.UniqueID
				}
				// Preserve concurrent UpdateAccountWebRID updates if this round snapshot
				// has no newer webRid.
				if account.WebRID != "" {
					current.Accounts[i].WebRID = account.WebRID
				}
			}
		}
	}
	current.LastCheckedAt, current.LastSuccessAt, current.LastError, current.ConsecutiveFailures = sub.LastCheckedAt, sub.LastSuccessAt, sub.LastError, sub.ConsecutiveFailures
	current.LastDynamicID, current.LastDynamicPubTs, current.LastVideoBVID, current.LastVideoPubTs = sub.LastDynamicID, sub.LastDynamicPubTs, sub.LastVideoBVID, sub.LastVideoPubTs
	current.UpdatedAt = now.UTC().Format(time.RFC3339)
	syncSubFields(&current, now)
	return m.upsertRowLocked(current)
}

func (m *Manager) runPostRoundLiveCheck(
	ctx context.Context,
	checkedAccounts []AccountRef,
	abortedSites map[string]string,
	summary *CheckSummary,
) {
	m.mu.Lock()
	siteLiveFn := m.siteLiveCheck
	legacyLiveFn := m.liveCheck
	m.mu.Unlock()

	var eligible []AccountRef
	for _, a := range checkedAccounts {
		s := normalizeLegacyAccountSite(a.Site)
		if s == string(site.Weibo) || s == string(site.X) || !site.ID(s).Valid() {
			continue // Weibo and X live status checks are unsupported (0 requests).
		}
		if abortedSites[s] != "" {
			continue
		}
		cp := a
		cp.Site = s
		eligible = append(eligible, cp)
	}
	if len(eligible) == 0 {
		return
	}

	if siteLiveFn != nil {
		if err := siteLiveFn(ctx, eligible); err != nil {
			summary.Errors = append(summary.Errors, err.Error())
			if isRoundStop(err) {
				errSite := normalizeSchedulerSite(string(site.SiteOf(err)))
				if errSite == "" {
					// Only attribute untagged live-check errors to Bilibili if Bilibili was checked.
					for _, a := range eligible {
						if a.Site == string(site.Bilibili) {
							errSite = string(site.Bilibili)
							break
						}
					}
				}
				if errSite != "" {
					kind := site.KindOf(err)
					abortedSites[errSite] = kind
					if kind == site.ErrKindRiskControl || kind == site.ErrKindRateLimited || kind == site.ErrKindAuthRequired {
						summary.StoppedByRiskControl = true
						if summary.SiteStopped == nil {
							summary.SiteStopped = make(map[string]string)
						}
						summary.SiteStopped[errSite] = kind
					}
				}
			}
		}
		return
	}

	if legacyLiveFn != nil && abortedSites[string(site.Bilibili)] == "" {
		var uids []int64
		for _, a := range eligible {
			if a.Site != string(site.Bilibili) {
				continue
			}
			if uid, err := strconv.ParseInt(a.UID, 10, 64); err == nil && uid > 0 {
				uids = append(uids, uid)
			}
		}
		if len(uids) > 0 {
			if err := legacyLiveFn(ctx, uids); err != nil {
				summary.Errors = append(summary.Errors, err.Error())
				if isRoundStop(err) {
					kind := site.KindOf(err)
					abortedSites[string(site.Bilibili)] = kind
					if kind == site.ErrKindRiskControl || kind == site.ErrKindRateLimited || kind == site.ErrKindAuthRequired {
						summary.StoppedByRiskControl = true
						if summary.SiteStopped == nil {
							summary.SiteStopped = make(map[string]string)
						}
						summary.SiteStopped[string(site.Bilibili)] = kind
					}
				}
			}
		}
	}
}

func (m *Manager) finalizeSiteRoundSchedulerState(
	siteName string,
	roundStart time.Time,
	stopKind string,
	resetAt time.Time,
	checkedCount int,
	wholeRoundAborted bool,
	isScheduled bool,
) error {
	var (
		wakeSite bool
		wakeCB   func(site.ID)
	)
	err := func() error {
		m.mu.Lock()
		defer m.mu.Unlock()

		schedSt := m.getSiteSchedulerStateLocked(siteName)
		now := m.nowFunc().UTC()

		// Backoff applies only to risk_control and rate_limited (subscriptions.md §3).
		// Manual probe failures preserve an active backoff window without escalating
		// BackoffLevel (DEC-089); scheduled stops advance the per-site backoff ladder.
		if stopKind == site.ErrKindRiskControl || stopKind == site.ErrKindRateLimited {
			if !isScheduled {
				m.recordSiteManualProbeFailureLocked(siteName, stopKind, resetAt)
				return nil
			}
			m.maybeDecayWeiboBackoffLocked(siteName, &schedSt, now)
			if stopKind == site.ErrKindRateLimited && !resetAt.IsZero() {
				if prevAt, err := time.Parse(time.RFC3339, schedSt.LastStopAt); err == nil && !prevAt.Before(roundStart.Truncate(time.Second)) && schedSt.LastStopReason == stopKind {
					if curUntil, uErr := time.Parse(time.RFC3339, schedSt.BackoffUntil); uErr == nil && !curUntil.Before(resetAt.UTC()) {
						return nil
					}
				}
				jitter := m.nextRateLimitJitterLocked()
				until := resetAt.UTC().Add(jitter)
				if !until.After(now) {
					until = now.Add(max(jitter, time.Second))
				}
				if maxUntil := now.Add(24 * time.Hour); until.After(maxUntil) {
					until = maxUntil
				}
				if curUntil, uErr := time.Parse(time.RFC3339, schedSt.BackoffUntil); uErr == nil && curUntil.After(until) {
					until = curUntil
				}
				if schedSt.ConsecutiveFailures < 1 {
					schedSt.ConsecutiveFailures = 1
				}
				if schedSt.BackoffLevel < 1 {
					schedSt.BackoffLevel = 1
				}
				schedSt.LastStopReason = stopKind
				schedSt.LastStopAt = now.Format(time.RFC3339)
				schedSt.BackoffUntil = until.Format(time.RFC3339)
				return m.saveSiteSchedulerStateLocked(siteName, schedSt)
			}
			// Avoid double-incrementing if the site client's automatic breaker already recorded this stop during the round.
			if prevAt, err := time.Parse(time.RFC3339, schedSt.LastStopAt); err == nil && !prevAt.Before(roundStart.Truncate(time.Second)) && schedSt.LastStopReason == stopKind && schedSt.BackoffUntil != "" {
				return nil
			}
			schedSt.ConsecutiveFailures++
			schedSt.BackoffLevel = schedSt.ConsecutiveFailures
			schedSt.LastStopReason = stopKind
			schedSt.LastStopAt = now.Format(time.RFC3339)
			backoffDur := SiteBackoffDuration(siteName, schedSt.BackoffLevel, m.subSettings.IntervalMinutes)
			schedSt.BackoffUntil = now.Add(backoffDur).Format(time.RFC3339)
			return m.saveSiteSchedulerStateLocked(siteName, schedSt)
		}

		if stopKind == site.ErrKindAuthRequired || stopKind == site.ErrKindSchemaChanged {
			schedSt.LastStopReason = stopKind
			schedSt.LastStopAt = now.Format(time.RFC3339)
			return m.saveSiteSchedulerStateLocked(siteName, schedSt)
		}

		if stopKind == "" && !wholeRoundAborted && checkedCount > 0 {
			wasActive, _ := m.isSiteInBackoffLocked(siteName)
			schedSt.ConsecutiveFailures = 0
			schedSt.BackoffLevel = 0
			schedSt.BackoffUntil = ""
			if err := m.saveSiteSchedulerStateLocked(siteName, schedSt); err != nil {
				return err
			}
			if wasActive && m.onBackoffCleared != nil {
				wakeSite = true
				wakeCB = m.onBackoffCleared
			}
		}
		return nil
	}()
	if err == nil && wakeSite && wakeCB != nil {
		wakeCB(site.ID(normalizeSchedulerSite(siteName)))
	}
	return err
}

func appendUniqueString(slice []string, val string) []string {
	for _, s := range slice {
		if s == val {
			return slice
		}
	}
	return append(slice, val)
}
