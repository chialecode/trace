package subscription

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
)

const (
	// DefaultBilibiliRoundAccountCap is the maximum Bilibili accounts checked in one scheduled round.
	DefaultBilibiliRoundAccountCap = 30
	// DefaultDouyinRoundAccountCap is the maximum Douyin accounts checked in one scheduled round.
	DefaultDouyinRoundAccountCap = 20

	// DefaultBilibiliAccountInterval is the minimum spacing between two Bilibili accounts in a check round.
	DefaultBilibiliAccountInterval = 2 * time.Second
	// DefaultDouyinAccountInterval is the minimum spacing between two Douyin accounts in a check round.
	DefaultDouyinAccountInterval = 2500 * time.Millisecond
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
func SiteScheduledRoundCap(siteName string) int {
	switch normalizeSchedulerSite(siteName) {
	case string(site.Douyin):
		return DefaultDouyinRoundAccountCap
	default:
		return DefaultBilibiliRoundAccountCap
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
	if m.siteIntervals != nil {
		if d, ok := m.siteIntervals[norm]; ok {
			return d
		}
	}
	if norm == string(site.Douyin) {
		if m.accountIntervalTouched && m.accountInterval == 0 {
			return 0
		}
		return DefaultDouyinAccountInterval
	}
	return m.accountInterval
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
	normSite := normalizeSchedulerSite(siteName)
	if normSite == string(site.Bilibili) {
		if id, err := strconv.ParseInt(uid, 10, 64); err == nil && id > 0 {
			uid = strconv.FormatInt(id, 10)
		}
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
			if normAcc.Site == string(site.Douyin) && normAcc.UniqueID != "" && owner.Accounts[i].UniqueID != normAcc.UniqueID {
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
// optionally filtered by siteName (empty siteName returns all sites).
func (m *Manager) ListAutoRecordAccounts(siteName string) []AccountRef {
	filterSite := strings.ToLower(strings.TrimSpace(siteName))
	m.mu.Lock()
	subs, err := m.listLocked()
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
			accSite := normalizeSchedulerSite(a.Site)
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

// ListSiteSchedulerStates returns the persisted scheduler backoff and quota states for both supported sites.
func (m *Manager) ListSiteSchedulerStates() map[string]SchedulerState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return map[string]SchedulerState{
		string(site.Bilibili): m.getSiteSchedulerStateLocked(string(site.Bilibili)),
		string(site.Douyin):   m.getSiteSchedulerStateLocked(string(site.Douyin)),
	}
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
	if m.isSiteAuthenticated(site.Bilibili) || m.isSiteAuthenticated(site.Douyin) {
		return true
	}
	m.mu.Lock()
	var extraSites []site.ID
	for s := range m.fetchers {
		if s != site.Bilibili && s != site.Douyin {
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
	case string(site.Douyin):
		return "未登录抖音，已跳过"
	case string(site.Bilibili):
		return "未登录哔哩哔哩，已跳过"
	default:
		return "未登录" + siteName + "，已跳过"
	}
}

func orderedSiteNames(bySite map[string][]roundAccountTarget) []string {
	var out []string
	for _, preferred := range []string{string(site.Bilibili), string(site.Douyin)} {
		if len(bySite[preferred]) > 0 {
			out = append(out, preferred)
		}
	}
	var extras []string
	for k, v := range bySite {
		if len(v) == 0 || k == string(site.Bilibili) || k == string(site.Douyin) {
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

func (m *Manager) executeMultiSiteRound(ctx context.Context, targetMid int64, isScheduled bool, subID string) (CheckSummary, error) {
	if !m.hasAnySiteAuth() {
		if isScheduled {
			return CheckSummary{}, nil
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
			prev = *m.lastSummary
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
	subs, err := m.listLocked()
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
	if sleepFn == nil {
		sleepFn = defaultSubscriptionSleep
	}

	roundStart := m.nowFunc().UTC()
	bySite := make(map[string][]roundAccountTarget)
	targetMidStr := ""
	if targetMid > 0 {
		targetMidStr = strconv.FormatInt(targetMid, 10)
	}

	for i := range subs {
		sub := &subs[i]
		if subID != "" && sub.ID != subID && sub.SubID != subID {
			continue
		}
		if targetMid > 0 && sub.Mid != targetMid {
			hasTarget := false
			for _, a := range sub.Accounts {
				if normalizeSchedulerSite(a.Site) == string(site.Bilibili) && a.UID == targetMidStr {
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
			accSite := normalizeSchedulerSite(a.Site)
			a.Site = accSite
			if targetMid > 0 && (accSite != string(site.Bilibili) || a.UID != targetMidStr) {
				continue
			}
			m.mu.Lock()
			st, _, _ := m.getAccountStateLocked(accSite, a.UID)
			m.mu.Unlock()
			bySite[accSite] = append(bySite[accSite], roundAccountTarget{
				subIndex:      i,
				acc:           a,
				lastCheckedAt: st.LastCheckedAt,
			})
		}
	}

	siteOrder := orderedSiteNames(bySite)
	if isScheduled && len(siteOrder) == 0 {
		for _, s := range []site.ID{site.Bilibili, site.Douyin} {
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
			requiredTokens := 2 * len(candidates)
			if siteName == string(site.Bilibili) {
				requiredTokens = 3*len(candidates) + 2
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
		m.mu.Lock()
		m.lastSummary = &summary
		m.mu.Unlock()
		if m.onEvent != nil {
			m.onEvent(summary)
		}
		return summary, nil
	}

	m.publishProgress(summary)

	consecutiveNetFailures := 0
	wholeRoundAborted := false
	var checkedAccounts []AccountRef
	checkedCountBySite := make(map[string]int)

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
				return summary, stateErr
			}

			summary.CheckedCreators++
			checkedCountBySite[siteName]++
			isBaseline := accState.BaselineAt == ""

			outcome, checkErr := m.checkAccountBySite(roundCtx, sub, tgt.acc, &accState, isBaseline, globalAutoCache)
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
					return summary, persistErr
				}
				if err := m.saveSubState(*sub); err != nil {
					return summary, err
				}

				if errKind == site.ErrKindRiskControl || errKind == site.ErrKindRateLimited || errKind == site.ErrKindAuthRequired {
					summary.StoppedByRiskControl = true
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

			if outcome.incomplete {
				accState.LastKind = "incomplete"
				accState.Note = outcome.incompleteNote
				summary.IncompleteAccounts = append(summary.IncompleteAccounts, siteName+":"+tgt.acc.UID)
				m.mu.Lock()
				persistErr := m.saveAccountStateLocked(accState)
				m.mu.Unlock()
				if persistErr != nil {
					return summary, persistErr
				}
				if err := m.saveSubState(*sub); err != nil {
					return summary, err
				}
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
				return summary, persistErr
			}

			sub.LastSuccessAt = nowStr
			sub.ConsecutiveFailures = 0
			sub.LastError = ""
			if err := m.saveSubState(*sub); err != nil {
				return summary, err
			}

			if isBaseline {
				summary.BaselineInitialized++
			}
			m.publishProgress(summary)
		}
	}

	if !wholeRoundAborted && roundCtx.Err() == nil {
		m.runPostRoundLiveCheck(roundCtx, checkedAccounts, abortedSites, &summary)
	}

	for _, siteName := range siteOrder {
		if len(activeBySite[siteName]) == 0 {
			continue
		}
		stopKind := abortedSites[siteName]
		if err := m.finalizeSiteRoundSchedulerState(siteName, roundStart, stopKind, checkedCountBySite[siteName], wholeRoundAborted); err != nil {
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
		}
	}

	summary.CurrentUID = ""
	summary.FinishedAt = m.nowFunc().UTC().Format(time.RFC3339)
	m.mu.Lock()
	m.lastSummary = &summary
	m.mu.Unlock()
	if m.onEvent != nil {
		m.onEvent(summary)
	}
	return summary, nil
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
		s := normalizeSchedulerSite(a.Site)
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
				errSite := string(site.SiteOf(err))
				if errSite == "" {
					errSite = string(site.Bilibili)
				}
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
	checkedCount int,
	wholeRoundAborted bool,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	schedSt := m.getSiteSchedulerStateLocked(siteName)
	now := m.nowFunc().UTC()

	if stopKind == site.ErrKindRiskControl || stopKind == site.ErrKindRateLimited || stopKind == site.ErrKindAuthRequired {
		// Avoid double-incrementing if the site client's automatic breaker already recorded this stop during the round.
		if prevAt, err := time.Parse(time.RFC3339, schedSt.LastStopAt); err == nil && !prevAt.Before(roundStart.Truncate(time.Second)) && schedSt.LastStopReason == stopKind && schedSt.BackoffUntil != "" {
			return nil
		}
		schedSt.ConsecutiveFailures++
		schedSt.BackoffLevel = schedSt.ConsecutiveFailures
		schedSt.LastStopReason = stopKind
		schedSt.LastStopAt = now.Format(time.RFC3339)
		baseInterval := time.Duration(m.subSettings.IntervalMinutes) * time.Minute
		if baseInterval < time.Minute {
			baseInterval = 60 * time.Minute
		}
		shift := min(schedSt.ConsecutiveFailures, 10)
		backoffDur := min(baseInterval*time.Duration(1<<shift), 24*time.Hour)
		schedSt.BackoffUntil = now.Add(backoffDur).Format(time.RFC3339)
		return m.saveSiteSchedulerStateLocked(siteName, schedSt)
	}

	if stopKind == "" && !wholeRoundAborted && checkedCount > 0 {
		schedSt.ConsecutiveFailures = 0
		schedSt.BackoffLevel = 0
		schedSt.BackoffUntil = ""
		return m.saveSiteSchedulerStateLocked(siteName, schedSt)
	}
	return nil
}

func (m *Manager) checkAccountBySite(
	ctx context.Context,
	sub *Subscription,
	acc AccountRef,
	accState *AccountState,
	isBaseline bool,
	globalAutoCache string,
) (accountCheckOutcome, error) {
	siteName := normalizeSchedulerSite(acc.Site)
	acc.Site = siteName

	m.mu.Lock()
	fetcher := m.fetchers[site.ID(siteName)]
	biliClient := m.client
	m.mu.Unlock()

	if siteName == string(site.Bilibili) && biliClient != nil {
		newVids, newDyns, autoTasks, gap, err := m.checkSingleAccount(ctx, sub, acc, accState, isBaseline, globalAutoCache)
		return accountCheckOutcome{
			newVideos:      newVids,
			newDynamics:    newDyns,
			autoCacheTasks: autoTasks,
			gapDetected:    gap,
		}, err
	}

	if fetcher == nil {
		return accountCheckOutcome{}, &site.Error{
			Site:    site.ID(siteName),
			Kind:    site.ErrKindBadRequest,
			Message: "未注册该站点的订阅抓取器",
		}
	}
	return m.checkFetcherAccount(ctx, fetcher, sub, acc, accState, isBaseline)
}

func formatIncompleteNote(reason string) string {
	trimmed := strings.TrimSpace(reason)
	if trimmed == "" {
		return "本轮结果不完整"
	}
	if strings.HasPrefix(trimmed, "本轮结果不完整") {
		return trimmed
	}
	return "本轮结果不完整：" + trimmed
}

type pendingUpdateRow struct {
	dynID    string
	pubTs    int64
	typ      string
	snapshot string
}

func (m *Manager) checkFetcherAccount(
	ctx context.Context,
	fetcher site.SubscriptionFetcher,
	sub *Subscription,
	acc AccountRef,
	accState *AccountState,
	isBaseline bool,
) (accountCheckOutcome, error) {
	siteID := site.ID(acc.Site)
	maxPages := 2
	if isBaseline {
		maxPages = 1
	}

	cursor := "0"
	seenIDs := make(map[string]bool)
	reachedBaseline := isBaseline

	var pendingFeed []library.FeedEntry
	var pendingItems []pendingUpdateRow

	var bestNonPinnedID string
	var bestNonPinnedTs int64
	var bestPinnedID string
	var bestPinnedTs int64
	var baselinePinned []string

	lastHasMore := false

	for pageIdx := 0; pageIdx < maxPages; pageIdx++ {
		page, err := fetcher.FetchCreatorPosts(ctx, acc.UID, cursor)
		if err != nil {
			return accountCheckOutcome{}, err
		}

		if pageIdx == 0 {
			m.applyPage1AuthorAndLiveHint(sub, acc, page)
		}

		if page.UncertainEmpty {
			return accountCheckOutcome{
				incomplete:     true,
				incompleteNote: formatIncompleteNote(page.UncertainReason),
			}, nil
		}

		authorName := firstNonEmptyStr(page.AuthorName, firstNonEmptyStr(acc.Name, sub.Name))
		authorFace := sanitizeAccountFace(siteID, firstNonEmptyStr(page.AuthorFace, acc.Face))

		for _, post := range page.Posts {
			postID := strings.TrimSpace(post.ID)
			if postID == "" || seenIDs[postID] {
				continue
			}
			seenIDs[postID] = true

			isBaselinePinned := !isBaseline && m.wasPinnedOnBaseline(acc.Site, acc.UID, postID)
			if !isBaseline && !post.IsTop {
				if isBaselinePinned || (accState.NewestID != "" && postID == accState.NewestID) || (accState.NewestTs > 0 && post.PubTs <= accState.NewestTs) {
					reachedBaseline = true
					break
				}
			}

			if post.IsTop {
				if isBaseline {
					baselinePinned = append(baselinePinned, postID)
				}
				if post.PubTs > bestPinnedTs || (post.PubTs == bestPinnedTs && compareNumericID(postID, bestPinnedID) > 0) {
					bestPinnedTs = post.PubTs
					bestPinnedID = postID
				}
			} else {
				if post.PubTs > bestNonPinnedTs || (post.PubTs == bestNonPinnedTs && compareNumericID(postID, bestNonPinnedID) > 0) {
					bestNonPinnedTs = post.PubTs
					bestNonPinnedID = postID
				}
			}

			normType := normalizeFetcherPostType(post.Type)
			feedKind := "video"
			canonicalURL := "https://www.douyin.com/video/" + postID
			if normType == "draw" {
				feedKind = "dynamic"
				canonicalURL = "https://www.douyin.com/note/" + postID
			} else if siteID != site.Douyin {
				canonicalURL = ""
			}

			pendingFeed = append(pendingFeed, library.FeedEntry{
				Site:         acc.Site,
				Kind:         feedKind,
				RawID:        postID,
				AuthorUID:    acc.UID,
				AuthorName:   authorName,
				AuthorFace:   authorFace,
				Title:        post.Title,
				CoverURL:     sanitizeAccountFace(siteID, post.CoverURL),
				CanonicalURL: canonicalURL,
				PubTs:        post.PubTs,
			})

			if !isBaseline {
				if isBaselinePinned {
					continue
				}
				if post.IsTop {
					if postID == accState.NewestID || (accState.NewestTs > 0 && post.PubTs <= accState.NewestTs) {
						continue
					}
				}
				pendingItems = append(pendingItems, pendingUpdateRow{
					dynID:    postID,
					pubTs:    post.PubTs,
					typ:      normType,
					snapshot: sanitizeSnapshotJSON(siteID, post.Snapshot, post),
				})
			}
		}

		lastHasMore = page.HasMore
		if reachedBaseline || !page.HasMore {
			reachedBaseline = true
			break
		}

		nextCursor := strings.TrimSpace(page.NextCursor)
		if nextCursor == "" || nextCursor == "0" || nextCursor == cursor {
			break
		}
		if cursor != "0" {
			if prevN, err1 := strconv.ParseInt(cursor, 10, 64); err1 == nil && prevN > 0 {
				if nextN, err2 := strconv.ParseInt(nextCursor, 10, 64); err2 == nil && nextN >= prevN {
					break
				}
			}
		}
		cursor = nextCursor
	}

	if m.lib != nil {
		for _, fe := range pendingFeed {
			if err := m.lib.UpsertFeedEntry(fe); err != nil {
				return accountCheckOutcome{}, err
			}
		}
	}

	nowStr := m.nowFunc().UTC().Format(time.RFC3339)
	var out accountCheckOutcome
	if !isBaseline {
		for _, it := range pendingItems {
			inserted, err := m.insertUpdateItem(acc.Site, acc.UID, it.dynID, it.pubTs, it.typ, it.snapshot, nowStr)
			if err != nil {
				return out, err
			}
			if inserted {
				if it.typ == "video" {
					out.newVideos++
				} else {
					out.newDynamics++
				}
			}
		}
		if !reachedBaseline && lastHasMore {
			out.gapDetected = true
		}
	} else {
		if err := m.recordBaselinePinnedIDs(acc.Site, acc.UID, baselinePinned); err != nil {
			return out, err
		}
	}

	if isBaseline {
		if bestNonPinnedTs > 0 {
			accState.NewestTs = bestNonPinnedTs
			accState.NewestID = bestNonPinnedID
		} else if bestPinnedTs > 0 {
			accState.NewestTs = bestPinnedTs
			accState.NewestID = bestPinnedID
		}
	} else {
		if bestNonPinnedTs > accState.NewestTs || (bestNonPinnedTs == accState.NewestTs && bestNonPinnedTs > 0 && compareNumericID(bestNonPinnedID, accState.NewestID) > 0) {
			accState.NewestTs = bestNonPinnedTs
			accState.NewestID = bestNonPinnedID
		}
	}

	m.enforceAccountItemCap(acc.Site, acc.UID, 200)
	return out, nil
}

func (m *Manager) applyPage1AuthorAndLiveHint(sub *Subscription, acc AccountRef, page site.CreatorPostsPage) {
	siteID := site.ID(acc.Site)
	cleanFace := sanitizeAccountFace(siteID, page.AuthorFace)
	cleanUniqueID := strings.TrimSpace(page.UniqueID)
	if utf8.RuneCountInString(cleanUniqueID) > 64 {
		runes := []rune(cleanUniqueID)
		cleanUniqueID = string(runes[:64])
	}

	liveRoomID := strings.TrimSpace(page.LiveRoomID)
	liveWebRID := strings.TrimSpace(page.LiveWebRID)
	if liveRoomID == "" && liveWebRID == "" {
		for _, p := range page.Posts {
			if p.LiveRoomID != "" || p.LiveWebRID != "" {
				liveRoomID = strings.TrimSpace(p.LiveRoomID)
				liveWebRID = strings.TrimSpace(p.LiveWebRID)
				break
			}
		}
	}
	if liveWebRID != "" && validateOptionalDouyinToken(liveWebRID, "webRid") != nil {
		liveWebRID = ""
	}

	var updatedAcc AccountRef = acc
	for i := range sub.Accounts {
		if sub.Accounts[i].Site == acc.Site && sub.Accounts[i].UID == acc.UID {
			if page.AuthorName != "" {
				sub.Accounts[i].Name = page.AuthorName
			}
			if cleanFace != "" {
				sub.Accounts[i].Face = cleanFace
			}
			if cleanUniqueID != "" {
				sub.Accounts[i].UniqueID = cleanUniqueID
			}
			if liveWebRID != "" {
				sub.Accounts[i].WebRID = liveWebRID
			}
			updatedAcc = sub.Accounts[i]
		}
	}

	if liveRoomID != "" || liveWebRID != "" {
		m.mu.Lock()
		hintFn := m.onLiveHint
		m.mu.Unlock()
		if hintFn != nil {
			hintFn(updatedAcc, liveRoomID, liveWebRID)
		}
	}
}

func (m *Manager) recordBaselinePinnedIDs(siteName, uid string, ids []string) error {
	normSite := normalizeSchedulerSite(siteName)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.updatesDB == nil {
		return nil
	}
	tx, err := m.updatesDB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM account_baseline_pinned WHERE site = ? AND uid = ?`, normSite, uid); err != nil {
		return err
	}
	for _, id := range ids {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			continue
		}
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO account_baseline_pinned (site, uid, post_id) VALUES (?, ?, ?)`,
			normSite, uid, trimmed,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (m *Manager) wasPinnedOnBaseline(siteName, uid, id string) bool {
	normSite := normalizeSchedulerSite(siteName)
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.updatesDB == nil {
		return false
	}
	var found int
	err := m.updatesDB.QueryRow(
		`SELECT 1 FROM account_baseline_pinned WHERE site = ? AND uid = ? AND post_id = ? LIMIT 1`,
		normSite, uid, trimmed,
	).Scan(&found)
	return err == nil && found == 1
}

func normalizeFetcherPostType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "video":
		return "video"
	case "draw", "note", "image", "image_note":
		return "draw"
	case "word":
		return "word"
	case "forward":
		return "forward"
	case "article":
		return "article"
	default:
		return "other"
	}
}

func sanitizeSnapshotJSON(siteID site.ID, raw string, post site.CreatorPost) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed != "" {
		var generic any
		if json.Unmarshal([]byte(trimmed), &generic) == nil {
			cleaned := scrubSignedValues(siteID, generic)
			if encoded, err := json.Marshal(cleaned); err == nil {
				return string(encoded)
			}
		}
	}
	fallback := map[string]any{
		"id":    post.ID,
		"pubTs": post.PubTs,
		"type":  normalizeFetcherPostType(post.Type),
		"title": post.Title,
	}
	if cleanCover := sanitizeAccountFace(siteID, post.CoverURL); cleanCover != "" {
		fallback["coverUrl"] = cleanCover
	}
	encoded, _ := json.Marshal(fallback)
	return string(encoded)
}

func scrubSignedValues(siteID site.ID, v any) any {
	switch val := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, elem := range val {
			out[k] = scrubSignedValues(siteID, elem)
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, elem := range val {
			out[i] = scrubSignedValues(siteID, elem)
		}
		return out
	case string:
		lower := strings.ToLower(val)
		if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
			return sanitizeAccountFace(siteID, val)
		}
		return val
	default:
		return v
	}
}

func (m *Manager) evaluateAutoCacheRulesForSites(ctx context.Context, abortedSites map[string]string) (createdTasks int, totalBacklog int, result error) {
	m.mu.Lock()
	subs, err := m.listLocked()
	hasEnqueuers := len(m.autoCacheEnqueuers) > 0
	dl := m.dl
	m.mu.Unlock()
	if err != nil {
		return 0, 0, err
	}
	if dl == nil && !hasEnqueuers {
		return 0, 0, nil
	}

	type candidate struct {
		subID    string
		site     string
		uid      string
		dynID    string
		pubTs    int64
		typ      string
		snapshot string
	}
	var allCandidates []candidate

	for _, s := range subs {
		sinceUnix := int64(0)
		typeAllowed := make(map[string]bool, len(s.Rules.AutoCache.Types))
		legacy := !s.Rules.AutoCache.Enabled
		if legacy {
			effective := s.AutoCache
			if effective == "inherit" || effective == "" {
				effective = m.subSettings.AutoCacheScope
			}
			if effective == "none" || effective == "" {
				continue
			}
			if effective == "video" || effective == "both" {
				typeAllowed["video"] = true
			}
			if effective == "dynamic" || effective == "both" {
				for _, typ := range []string{"video", "draw", "word", "forward"} {
					typeAllowed[typ] = true
				}
			}
		} else {
			sinceTime, err := time.Parse(time.RFC3339, s.Rules.AutoCache.Since)
			if err != nil {
				continue
			}
			sinceUnix = sinceTime.Unix()
			for _, typ := range s.Rules.AutoCache.Types {
				typeAllowed[strings.ToLower(strings.TrimSpace(typ))] = true
			}
		}

		for _, acc := range s.Accounts {
			accSite := normalizeSchedulerSite(acc.Site)
			if abortedSites != nil && abortedSites[accSite] != "" {
				continue
			}
			m.mu.Lock()
			rows, qErr := m.updatesDB.Query(`
				SELECT dyn_id, pub_ts, type, snapshot
				FROM update_item
				WHERE site = ? AND uid = ? AND pub_ts >= ? AND auto_task_id = '' AND baseline_snapshot = 0
				ORDER BY pub_ts ASC, dyn_id ASC
			`, accSite, acc.UID, sinceUnix)
			if qErr == nil {
				for rows.Next() {
					var dynID, typ, snap string
					var pubTs int64
					if err := rows.Scan(&dynID, &pubTs, &typ, &snap); err == nil {
						if typeAllowed[typ] {
							allCandidates = append(allCandidates, candidate{
								subID:    s.SubID,
								site:     accSite,
								uid:      acc.UID,
								dynID:    dynID,
								pubTs:    pubTs,
								typ:      typ,
								snapshot: snap,
							})
						}
					}
				}
				_ = rows.Close()
			}
			m.mu.Unlock()
			if qErr != nil {
				return createdTasks, totalBacklog, qErr
			}
		}
	}

	sort.SliceStable(allCandidates, func(i, j int) bool {
		if allCandidates[i].pubTs != allCandidates[j].pubTs {
			return allCandidates[i].pubTs < allCandidates[j].pubTs
		}
		return allCandidates[i].dynID < allCandidates[j].dynID
	})

	backlogBySub := make(map[string]int)
	for _, c := range allCandidates {
		if err := ctx.Err(); err != nil {
			return createdTasks, totalBacklog, err
		}
		if cachedKey := m.findCachedLibraryKey(c.site, c.dynID); cachedKey != "" {
			if err := m.setUpdateItemTaskID(c.site, c.uid, c.dynID, "cached:"+cachedKey); err != nil {
				return createdTasks, totalBacklog, err
			}
			continue
		}

		if createdTasks >= 20 {
			backlogBySub[c.subID]++
			totalBacklog++
			continue
		}

		m.mu.Lock()
		enq := m.autoCacheEnqueuers[site.ID(c.site)]
		m.mu.Unlock()

		if enq != nil {
			taskID, err := enq(ctx, UpdateItem{
				Site:     c.site,
				UID:      c.uid,
				DynID:    c.dynID,
				PubTs:    c.pubTs,
				Type:     c.typ,
				Snapshot: c.snapshot,
			})
			if err == nil && taskID != "" {
				createdTasks++
				if err := m.setUpdateItemTaskID(c.site, c.uid, c.dynID, taskID); err != nil {
					return createdTasks, totalBacklog, err
				}
			}
			continue
		}

		if c.site == string(site.Bilibili) && dl != nil {
			if strings.HasPrefix(strings.ToUpper(c.dynID), "BV1") {
				if tasks, err := dl.EnqueueVideo(ctx, download.VideoEnqueueRequest{BVID: c.dynID, Source: "auto"}); err == nil && len(tasks) > 0 {
					createdTasks++
					if err := m.setUpdateItemTaskID(c.site, c.uid, c.dynID, tasks[0].TaskID); err != nil {
						return createdTasks, totalBacklog, err
					}
				}
			} else {
				var dynItem bilibili.DynamicItem
				var itemPtr *bilibili.DynamicItem
				if c.snapshot != "" && json.Unmarshal([]byte(c.snapshot), &dynItem) == nil && dynItem.ID != "" {
					itemPtr = &dynItem
				}
				if task, err := dl.EnqueueDynamic(ctx, download.DynamicEnqueueRequest{
					DynamicID:         c.dynID,
					CacheLinkedVideos: true,
					Source:            "auto",
					Item:              itemPtr,
				}); err == nil {
					createdTasks++
					if err := m.setUpdateItemTaskID(c.site, c.uid, c.dynID, task.TaskID); err != nil {
						return createdTasks, totalBacklog, err
					}
				}
			}
		}
	}

	m.mu.Lock()
	current, listErr := m.listLocked()
	if listErr != nil {
		m.mu.Unlock()
		return createdTasks, totalBacklog, listErr
	}
	for i := range current {
		current[i].AutoCacheBacklog = backlogBySub[current[i].SubID]
		if err := m.upsertRowLocked(current[i]); err != nil {
			m.mu.Unlock()
			return createdTasks, totalBacklog, err
		}
	}
	writeErr := m.writeSubscriptionsToDiskLocked()
	m.mu.Unlock()

	return createdTasks, totalBacklog, writeErr
}

func (m *Manager) findCachedLibraryKey(siteName, dynID string) string {
	if m.lib == nil {
		return ""
	}
	var keys []string
	switch normalizeSchedulerSite(siteName) {
	case string(site.Douyin):
		keys = []string{"douyin:video:" + dynID, "douyin:dynamic:" + dynID}
	default:
		keys = []string{"bilibili:dynamic:" + dynID, "bilibili:video:" + dynID, dynID}
	}
	for _, k := range keys {
		if _, ok, _ := m.lib.GetItem(k); ok {
			return k
		}
	}
	return ""
}

func appendUniqueString(slice []string, val string) []string {
	for _, s := range slice {
		if s == val {
			return slice
		}
	}
	return append(slice, val)
}
