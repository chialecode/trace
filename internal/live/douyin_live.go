package live

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
	"github.com/chialecode/trace/internal/subscription"
)

const (
	defaultDouyinAutoPollInterval = 90 * time.Second
	defaultFLVConnectTimeout      = 10 * time.Second
	defaultFLVIdleTimeout         = 15 * time.Second
)

// LiveRoomAliveChecker is an optional extension on site.LiveProvider for batch room alive checks (e.g. Douyin check_alive).
type LiveRoomAliveChecker interface {
	CheckRoomsAlive(ctx context.Context, roomIDs []string) (map[string]bool, error)
}

// LiveAuthChecker is an optional extension on site.LiveProvider to report whether site credentials are active.
type LiveAuthChecker interface {
	HasAuth() bool
}

// LiveErrorReporter is an optional extension on site.LiveProvider to report automatic background errors.
type LiveErrorReporter interface {
	ReportAutomaticError(ctx context.Context, err error)
}

// WebRIDSyncFunc persists a backfilled or invalidated Douyin web_rid back to subscription storage.
type WebRIDSyncFunc func(siteID site.ID, uid, webRID string, invalidated bool)

// SiteBackoffChecker reports whether background polling for a given site is currently in backoff.
type SiteBackoffChecker func(siteID site.ID) (bool, time.Time)

type douyinCachedStatusEntry struct {
	status    site.LiveStatus
	state     RoomState
	fetchedAt time.Time
}

// RegisterSiteProvider registers a cross-site LiveProvider (such as Douyin or Bilibili).
func (m *Manager) RegisterSiteProvider(provider site.LiveProvider) {
	if provider == nil {
		return
	}
	m.mu.Lock()
	m.siteProviders[provider.SiteID()] = provider
	m.mu.Unlock()
}

// SetWebRIDSyncFunc sets a callback invoked whenever Douyin live status backfills or invalidates an account's web_rid.
func (m *Manager) SetWebRIDSyncFunc(fn WebRIDSyncFunc) {
	m.mu.Lock()
	m.webRIDSyncFn = fn
	m.mu.Unlock()
}

// SetSiteBackoffChecker sets a per-site backoff predicate used by background live polling.
func (m *Manager) SetSiteBackoffChecker(fn SiteBackoffChecker) {
	m.mu.Lock()
	m.siteBackoffFn = fn
	m.mu.Unlock()
}

// SetDouyinPollMinInterval overrides the 90s minimum per-account auto-record poll interval (used by unit tests).
func (m *Manager) SetDouyinPollMinInterval(d time.Duration) {
	m.mu.Lock()
	if d >= 0 {
		m.douyinPollMinInterval = d
	}
	m.mu.Unlock()
}

// SetFLVIdleTimeout overrides the 15s per-read idle timeout on HTTP-FLV streams (used by unit tests).
func (m *Manager) SetFLVIdleTimeout(d time.Duration) {
	m.mu.Lock()
	if d > 0 {
		m.flvIdleTimeout = d
	}
	m.mu.Unlock()
}

func (m *Manager) getSiteProvider(siteID site.ID) (site.LiveProvider, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.siteProviders[siteID]
	return p, ok
}

func (m *Manager) hasSiteAuth(siteID site.ID) bool {
	if siteID == site.Bilibili {
		return m.client != nil && m.client.HasAuth()
	}
	p, ok := m.getSiteProvider(siteID)
	if !ok {
		return false
	}
	if checker, ok := p.(LiveAuthChecker); ok {
		return checker.HasAuth()
	}
	return true
}

func (m *Manager) isSiteInBackoff(siteID site.ID) bool {
	m.mu.Lock()
	fn := m.siteBackoffFn
	m.mu.Unlock()
	if fn != nil {
		inBackoff, _ := fn(siteID)
		return inBackoff
	}
	if m.subs != nil {
		if perSite, ok := any(m.subs).(interface {
			IsSiteInBackoff(site string) (bool, time.Time)
		}); ok {
			inBackoff, _ := perSite.IsSiteInBackoff(string(siteID))
			return inBackoff
		}
		if siteID == site.Bilibili {
			inBackoff, _ := m.subs.IsInBackoff()
			return inBackoff
		}
	}
	return false
}

func (m *Manager) recordSiteAutoStop(siteID site.ID, reason string) {
	if m.subs == nil {
		return
	}
	if perSite, ok := any(m.subs).(interface {
		RecordSiteAutomaticStop(site, reason string)
	}); ok {
		perSite.RecordSiteAutomaticStop(string(siteID), reason)
		return
	}
	if siteID == site.Bilibili {
		m.subs.RecordAutomaticStop(reason)
	}
}

func (m *Manager) syncWebRID(siteID site.ID, uid, webRID string, invalidated bool) {
	m.mu.Lock()
	fn := m.webRIDSyncFn
	m.mu.Unlock()
	if fn != nil {
		fn(siteID, uid, webRID, invalidated)
	}
	if m.subs != nil {
		if updater, ok := any(m.subs).(interface {
			UpdateAccountWebRID(site, uid, webRID string) error
		}); ok {
			target := webRID
			if invalidated && target == "" {
				target = ""
			}
			_ = updater.UpdateAccountWebRID(string(siteID), uid, target)
		}
	}
}

// ObserveAccountLiveHint records a live web_rid or room_id hint discovered passively
// (e.g. from Douyin aweme/post page 1) with zero extra network requests.
func (m *Manager) ObserveAccountLiveHint(siteID site.ID, uid, webRID, roomID string) {
	if siteID != site.Douyin {
		return
	}
	normUID, err := site.NormalizeUID(site.Douyin, uid)
	if err != nil {
		return
	}
	webRID = strings.TrimSpace(webRID)
	roomID = strings.TrimSpace(roomID)

	subAcc := m.findDouyinSubAccount(normUID)
	var syncNeeded bool
	nowStr := m.nowFunc().UTC().Format(time.RFC3339)
	m.mu.Lock()
	if webRID != "" && m.douyinWebRIDHints[normUID] != webRID {
		m.douyinWebRIDHints[normUID] = webRID
		syncNeeded = true
	}
	if roomID != "" && roomID != "0" {
		m.douyinRoomHints[normUID] = roomID
		effWebRID := firstNonEmpty(webRID, firstNonEmpty(m.douyinWebRIDHints[normUID], subAcc.WebRID))
		stateKey := douyinStateKey(roomID, normUID, effWebRID)
		prev := m.douyinStates[stateKey]
		roomInt, _ := strconv.ParseInt(roomID, 10, 64)
		prev.Site = string(site.Douyin)
		prev.AccountUID = normUID
		prev.UniqueID = firstNonEmpty(prev.UniqueID, subAcc.UniqueID)
		prev.RoomID = roomInt
		prev.RoomIDStr = roomID
		prev.WebRID = firstNonEmpty(effWebRID, prev.WebRID)
		prev.RoomKey = "douyin:live:" + roomID
		prev.LiveState = site.LiveStateLive
		prev.LiveStatus = 1
		prev.StatusText = "直播中"
		prev.Uname = firstNonEmpty(prev.Uname, subAcc.Name)
		prev.Face = sanitizeLiveURL(firstNonEmpty(prev.Face, subAcc.Face))
		prev.UpdatedAt = nowStr
		m.douyinStates[stateKey] = prev
	}
	m.mu.Unlock()

	if syncNeeded {
		m.syncWebRID(site.Douyin, normUID, webRID, false)
	}
}

// CachedAccountState returns the most recently observed live RoomState for (siteID, uid)
// from memory without issuing any network requests (used by subscription grid & sidebar).
func (m *Manager) CachedAccountState(siteID site.ID, uid string) (RoomState, bool) {
	normUID, err := site.NormalizeUID(siteID, uid)
	if err != nil {
		return RoomState{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	switch siteID {
	case site.Bilibili:
		mid, _ := strconv.ParseInt(normUID, 10, 64)
		if mid <= 0 {
			return RoomState{}, false
		}
		for _, st := range m.states {
			if st.UID == mid {
				if st.Site == "" {
					st.Site = string(site.Bilibili)
				}
				st.AccountUID = normUID
				if sess := m.sessions[st.RoomID]; sess != nil {
					st.Recording = true
					st.RecordSession = sess.sessionID
					st.RecordBytes = sess.bytesTotal
					st.RecordSegments = len(sess.segments)
					st.RecordItemDir = sess.itemDir
				}
				return st, true
			}
		}
		return RoomState{}, false

	case site.Douyin:
		for _, sess := range m.douyinSessions {
			if sess != nil && sess.accountUID == normUID {
				if st, ok := m.douyinStates[sess.stateKey]; ok {
					return m.hydrateDouyinStateLocked(st), true
				}
			}
		}
		if cached, ok := m.douyinStatusCache[normUID]; ok {
			return m.hydrateDouyinStateLocked(cached.state), true
		}
		if rid := m.douyinRoomHints[normUID]; rid != "" {
			if st, ok := m.douyinStates["douyin:live:"+rid]; ok {
				return m.hydrateDouyinStateLocked(st), true
			}
		}
		var best RoomState
		var found bool
		for _, st := range m.douyinStates {
			if st.AccountUID != normUID {
				continue
			}
			if !found || st.UpdatedAt > best.UpdatedAt {
				best = st
				found = true
			}
		}
		if found {
			return m.hydrateDouyinStateLocked(best), true
		}
		return RoomState{}, false
	}
	return RoomState{}, false
}

// CachedStatesByAccountKeys returns in-memory cached live states keyed by "<site>:<uid>" with 0 network requests.
func (m *Manager) CachedStatesByAccountKeys(accountKeys []string) map[string]RoomState {
	out := make(map[string]RoomState, len(accountKeys))
	for _, key := range accountKeys {
		siteID, uid, err := site.ParseAccountKey(strings.TrimSpace(key))
		if err != nil {
			continue
		}
		if st, ok := m.CachedAccountState(siteID, uid); ok {
			out[site.AccountKey(siteID, uid)] = st
		}
	}
	return out
}

// StatusByAccountKeys queries live status across mixed Bilibili and Douyin "<site>:<uid>" keys,
// using <=50 UID batching for Bilibili and the 60s single-account cache for Douyin.
func (m *Manager) StatusByAccountKeys(ctx context.Context, accountKeys []string) (map[string]RoomState, error) {
	out := make(map[string]RoomState, len(accountKeys))
	var biliMids []int64
	var douyinUIDs []string
	seen := make(map[string]bool, len(accountKeys))

	for _, rawKey := range accountKeys {
		siteID, uid, err := site.ParseAccountKey(strings.TrimSpace(rawKey))
		if err != nil {
			continue
		}
		canon := site.AccountKey(siteID, uid)
		if seen[canon] {
			continue
		}
		seen[canon] = true
		switch siteID {
		case site.Bilibili:
			if mid, pErr := strconv.ParseInt(uid, 10, 64); pErr == nil && mid > 0 {
				biliMids = append(biliMids, mid)
			}
		case site.Douyin:
			douyinUIDs = append(douyinUIDs, uid)
		}
	}

	var firstErr error
	if len(biliMids) > 0 && m.client != nil && m.client.HasAuth() {
		if _, err := m.RefreshForAccounts(ctx, biliMids); err != nil {
			firstErr = err
		} else {
			for _, mid := range biliMids {
				uidStr := strconv.FormatInt(mid, 10)
				if st, ok := m.CachedAccountState(site.Bilibili, uidStr); ok {
					out[site.AccountKey(site.Bilibili, uidStr)] = st
				}
			}
		}
	}

	for _, secUID := range douyinUIDs {
		st, err := m.CheckAccountLiveStatus(ctx, site.Douyin, secUID, "", false)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		out[site.AccountKey(site.Douyin, secUID)] = st
	}
	return out, firstErr
}

// CheckAccountLiveStatus queries live status for one creator account using a 60s TTL cache
// (unless force is true) and synchronizes any backfilled/invalidated Douyin web_rid.
func (m *Manager) CheckAccountLiveStatus(ctx context.Context, siteID site.ID, uid, webRIDHint string, force bool) (RoomState, error) {
	switch siteID {
	case site.Bilibili:
		normUID, err := site.NormalizeUID(site.Bilibili, uid)
		if err != nil {
			return RoomState{}, &site.Error{Site: site.Bilibili, Kind: site.ErrKindBadRequest, Message: "无效的哔哩哔哩 UID"}
		}
		mid, _ := strconv.ParseInt(normUID, 10, 64)
		if _, err := m.refreshAccounts(ctx, []int64{mid}, force, false); err != nil {
			return RoomState{}, err
		}
		st, _ := m.CachedAccountState(site.Bilibili, normUID)
		return st, nil

	case site.Douyin:
		normUID, err := site.NormalizeUID(site.Douyin, uid)
		if err != nil {
			return RoomState{}, &site.Error{Site: site.Douyin, Kind: site.ErrKindBadRequest, Message: "无效的抖音主播 sec_uid"}
		}
		provider, ok := m.getSiteProvider(site.Douyin)
		if !ok {
			return RoomState{}, &site.Error{Site: site.Douyin, Kind: site.ErrKindUnavailable, Message: "抖音直播服务未初始化"}
		}

		now := m.nowFunc()
		subAcc := m.findDouyinSubAccount(normUID)

		m.mu.Lock()
		if !force {
			if cached, hit := m.douyinStatusCache[normUID]; hit && now.Sub(cached.fetchedAt) < statusCacheTTL {
				hydrated := m.hydrateDouyinStateLocked(cached.state)
				m.mu.Unlock()
				return hydrated, nil
			}
		}
		effectiveWebRID := firstNonEmpty(strings.TrimSpace(webRIDHint), firstNonEmpty(m.douyinWebRIDHints[normUID], subAcc.WebRID))
		m.mu.Unlock()

		liveSt, err := provider.CheckLiveStatus(ctx, normUID, effectiveWebRID)
		if err != nil {
			return RoomState{}, err
		}
		if liveSt.AccountUID == "" {
			liveSt.AccountUID = normUID
		}
		return m.applyDouyinLiveStatus(liveSt, effectiveWebRID, subAcc, now), nil

	default:
		return RoomState{}, site.ErrUnknownSite
	}
}

// OpenDouyinRoom inspects a Douyin live room by web_rid (or room_id with uidHint) and updates tracked state.
func (m *Manager) OpenDouyinRoom(ctx context.Context, webRIDOrRoomID, uidHint string) (RoomState, error) {
	provider, ok := m.getSiteProvider(site.Douyin)
	if !ok {
		return RoomState{}, &site.Error{Site: site.Douyin, Kind: site.ErrKindUnavailable, Message: "抖音直播服务未初始化"}
	}
	target := strings.TrimSpace(webRIDOrRoomID)
	uid := strings.TrimSpace(uidHint)
	if target == "" && uid == "" {
		return RoomState{}, &site.Error{Site: site.Douyin, Kind: site.ErrKindBadRequest, Message: "缺少抖音直播间号或主播标识"}
	}

	var subAcc subscription.AccountRef
	if uid != "" {
		if norm, err := site.NormalizeUID(site.Douyin, uid); err == nil {
			uid = norm
			subAcc = m.findDouyinSubAccount(uid)
			if target == "" {
				m.mu.Lock()
				target = firstNonEmpty(m.douyinWebRIDHints[uid], subAcc.WebRID)
				m.mu.Unlock()
			}
		}
	}

	liveSt, err := provider.CheckLiveStatus(ctx, uid, target)
	if err != nil {
		return RoomState{}, err
	}
	if subAcc.UID == "" && liveSt.AccountUID != "" {
		subAcc = m.findDouyinSubAccount(liveSt.AccountUID)
	}
	return m.applyDouyinLiveStatus(liveSt, target, subAcc, m.nowFunc()), nil
}

func (m *Manager) applyDouyinLiveStatus(liveSt site.LiveStatus, queriedWebRID string, subAcc subscription.AccountRef, now time.Time) RoomState {
	secUID := strings.TrimSpace(liveSt.AccountUID)
	newWebRID := strings.TrimSpace(liveSt.WebRID)
	roomIDStr := strings.TrimSpace(liveSt.RoomID)

	var needSync bool
	var syncVal string
	var syncInvalidated bool

	m.mu.Lock()
	if secUID != "" {
		if liveSt.InvalidatedWebRID {
			if newWebRID != "" {
				m.douyinWebRIDHints[secUID] = newWebRID
				syncVal = newWebRID
				syncInvalidated = true
			} else {
				delete(m.douyinWebRIDHints, secUID)
				syncVal = ""
				syncInvalidated = true
			}
			needSync = true
		} else if newWebRID != "" && m.douyinWebRIDHints[secUID] != newWebRID {
			m.douyinWebRIDHints[secUID] = newWebRID
			syncVal = newWebRID
			if subAcc.UID != "" && subAcc.WebRID != newWebRID {
				needSync = true
			} else if queriedWebRID == "" {
				needSync = true
			}
		}
		if roomIDStr != "" && roomIDStr != "0" {
			m.douyinRoomHints[secUID] = roomIDStr
		} else if liveSt.Status == site.LiveStateOffline || liveSt.Status == site.LiveStateNone {
			delete(m.douyinRoomHints, secUID)
		}
	}

	liveInt, statusText := mapSiteLiveStateDisplay(liveSt.Status)
	var parsedRoomID int64
	if roomIDStr != "" {
		parsedRoomID, _ = strconv.ParseInt(roomIDStr, 10, 64)
	}

	stateKey := douyinStateKey(roomIDStr, secUID, newWebRID)
	existing := m.douyinStates[stateKey]
	var recSess *recordSession
	if roomIDStr != "" {
		recSess = m.douyinSessions[roomIDStr]
	}

	st := RoomState{
		Site:        string(site.Douyin),
		AccountUID:  secUID,
		UniqueID:    subAcc.UniqueID,
		RoomID:      parsedRoomID,
		RoomIDStr:   roomIDStr,
		WebRID:      firstNonEmpty(newWebRID, m.douyinWebRIDHints[secUID]),
		RoomKey:     stateKey,
		LiveState:   liveSt.Status,
		Uname:       firstNonEmpty(liveSt.Uname, subAcc.Name),
		Face:        sanitizeLiveURL(firstNonEmpty(liveSt.Face, subAcc.Face)),
		Title:       liveSt.Title,
		Cover:       sanitizeLiveURL(liveSt.Cover),
		AreaName:    liveSt.AreaName,
		LiveStatus:  liveInt,
		LiveSince:   liveSt.LiveSince,
		StatusText:  statusText,
		Quality:     firstPositiveInt(liveSt.Quality, existing.Quality),
		QualityName: firstNonEmpty(liveSt.QualityName, existing.QualityName),
		Codec:       firstNonEmpty(liveSt.Codec, existing.Codec),
		Recording:   recSess != nil,
		StopReason:  existing.StopReason,
		LastError:   existing.LastError,
		UpdatedAt:   now.UTC().Format(time.RFC3339),
	}
	st = m.hydrateDouyinStateLocked(st)
	m.douyinStates[stateKey] = st
	if secUID != "" {
		for k, old := range m.douyinStates {
			if k != stateKey && old.AccountUID == secUID && !m.isRoomActivelyRecordingLocked(old.RoomIDStr) {
				delete(m.douyinStates, k)
			}
		}
		m.douyinStatusCache[secUID] = douyinCachedStatusEntry{
			status:    liveSt,
			state:     st,
			fetchedAt: now,
		}
	}
	m.mu.Unlock()

	if needSync && secUID != "" {
		m.syncWebRID(site.Douyin, secUID, syncVal, syncInvalidated)
	}
	return st
}

func (m *Manager) isRoomActivelyRecordingLocked(roomIDStr string) bool {
	if roomIDStr == "" {
		return false
	}
	_, ok := m.douyinSessions[roomIDStr]
	return ok
}

func (m *Manager) hydrateDouyinStateLocked(st RoomState) RoomState {
	if st.Site == "" {
		st.Site = string(site.Douyin)
	}
	if sess := m.douyinSessions[st.RoomIDStr]; sess != nil {
		st.Recording = true
		st.RecordSession = sess.sessionID
		st.RecordDurationSec = m.nowFunc().Sub(sess.startedAt).Seconds()
		st.RecordBytes = sess.bytesTotal
		st.RecordSegments = len(sess.segments)
		st.RecordItemDir = sess.itemDir
		if sess.quality > 0 {
			st.Quality = sess.quality
		}
		if sess.qualityName != "" {
			st.QualityName = sess.qualityName
		}
		if sess.codec != "" {
			st.Codec = sess.codec
		}
		if sess.stopReason != "" {
			st.StopReason = sess.stopReason
		}
		if sess.lastErr != "" {
			st.LastError = sess.lastErr
		}
	} else {
		st.Recording = false
	}
	return st
}

func douyinStateKey(roomIDStr, secUID, webRID string) string {
	if roomIDStr != "" && roomIDStr != "0" {
		return "douyin:live:" + roomIDStr
	}
	if secUID != "" {
		return "douyin:user:" + secUID
	}
	return "douyin:web:" + webRID
}

func mapSiteLiveStateDisplay(state string) (int, string) {
	switch state {
	case site.LiveStateLive:
		return 1, "直播中"
	case site.LiveStateRound:
		return 2, "轮播中"
	case site.LiveStateAudioOnly:
		return 0, "语音直播（暂不支持录制）"
	default:
		return 0, "未开播"
	}
}

func (m *Manager) findDouyinSubAccount(secUID string) subscription.AccountRef {
	if m.subs == nil || secUID == "" {
		return subscription.AccountRef{}
	}
	list, err := m.subs.List()
	if err != nil {
		return subscription.AccountRef{}
	}
	for _, s := range list {
		for _, acc := range s.Accounts {
			if acc.Site == string(site.Douyin) && acc.UID == secUID {
				if acc.Name == "" {
					acc.Name = s.Name
				}
				if acc.Face == "" {
					acc.Face = s.Face
				}
				return acc
			}
		}
	}
	return subscription.AccountRef{}
}

// PollDouyinAutoRecordNow polls subscribed Douyin accounts with AutoRecord enabled,
// enforcing >= 90s per-account interval, checking known active room_ids via CheckRoomsAlive
// when supported, and auto-starting HTTP-FLV recording on live broadcasts.
func (m *Manager) PollDouyinAutoRecordNow(ctx context.Context) ([]RoomState, error) {
	return m.pollDouyinAutoRecord(ctx)
}

func (m *Manager) pollDouyinAutoRecord(ctx context.Context) ([]RoomState, error) {
	provider, ok := m.getSiteProvider(site.Douyin)
	if !ok || m.subs == nil {
		return m.ListRooms(), nil
	}
	if !m.hasSiteAuth(site.Douyin) {
		return m.ListRooms(), nil
	}
	subList, err := m.subs.List()
	if err != nil {
		return nil, err
	}

	type autoTarget struct {
		sub subscription.Subscription
		acc subscription.AccountRef
	}
	var targets []autoTarget
	seen := make(map[string]bool)
	for _, s := range subList {
		if !s.Rules.AutoRecord && !s.LiveAutoRecord {
			continue
		}
		for _, acc := range s.Accounts {
			if acc.Site != string(site.Douyin) {
				continue
			}
			normUID, nErr := site.NormalizeUID(site.Douyin, acc.UID)
			if nErr != nil || seen[normUID] {
				continue
			}
			seen[normUID] = true
			acc.UID = normUID
			targets = append(targets, autoTarget{sub: s, acc: acc})
		}
	}
	if len(targets) == 0 {
		return m.ListRooms(), nil
	}

	// Step 1: Check known active room_ids via CheckRoomsAlive (batch check_alive) when supported.
	if aliveChecker, ok := provider.(LiveRoomAliveChecker); ok {
		var knownRooms []string
		roomToUID := make(map[string]string)
		m.mu.Lock()
		for _, t := range targets {
			if rid := m.douyinRoomHints[t.acc.UID]; rid != "" && rid != "0" {
				knownRooms = append(knownRooms, rid)
				roomToUID[rid] = t.acc.UID
			}
		}
		m.mu.Unlock()

		if len(knownRooms) > 0 {
			aliveMap, aErr := aliveChecker.CheckRoomsAlive(ctx, knownRooms)
			if aErr != nil {
				if site.IsRoundStop(aErr) {
					if rep, rOK := provider.(LiveErrorReporter); rOK {
						rep.ReportAutomaticError(ctx, aErr)
					}
					if site.IsCircuitBreak(aErr) {
						m.recordSiteAutoStop(site.Douyin, site.KindOf(aErr))
					}
					return nil, aErr
				}
			} else {
				now := m.nowFunc()
				m.mu.Lock()
				for _, rid := range knownRooms {
					alive, present := aliveMap[rid]
					if present && !alive {
						uid := roomToUID[rid]
						delete(m.douyinRoomHints, uid)
						delete(m.douyinStoppedRooms, rid)
						m.douyinPrevLive[uid] = site.LiveStateOffline
						if sess := m.douyinSessions[rid]; sess != nil {
							if sess.stopReason == "" {
								sess.stopReason = "ended"
							}
							sess.cancel()
						}
						key := "douyin:live:" + rid
						if st, has := m.douyinStates[key]; has {
							st.LiveStatus = 0
							st.LiveState = site.LiveStateOffline
							st.StatusText = "未开播"
							st.Recording = false
							st.UpdatedAt = now.UTC().Format(time.RFC3339)
							m.douyinStates[key] = st
						}
					}
				}
				m.mu.Unlock()
			}
		}
	}

	// Step 2: Poll each AutoRecord Douyin account whose last poll was >= douyinPollMinInterval ago.
	m.mu.Lock()
	minInterval := m.douyinPollMinInterval
	notifyGlobal := m.liveSettings.NotifyOnStart
	m.mu.Unlock()

	var notifications []Notification
	type startReq struct {
		uid    string
		webRID string
	}
	var toStart []startReq

	for _, t := range targets {
		if ctx.Err() != nil {
			break
		}
		uid := t.acc.UID
		now := m.nowFunc()

		m.mu.Lock()
		lastAt, hadLast := m.douyinLastAutoPoll[uid]
		_, alreadyRec := m.douyinSessions[m.douyinRoomHints[uid]]
		webRIDHint := firstNonEmpty(m.douyinWebRIDHints[uid], t.acc.WebRID)
		m.mu.Unlock()

		if alreadyRec {
			continue
		}
		if hadLast && minInterval > 0 && now.Sub(lastAt) < minInterval {
			continue
		}

		m.mu.Lock()
		m.douyinLastAutoPoll[uid] = now
		m.mu.Unlock()

		liveSt, cErr := provider.CheckLiveStatus(ctx, uid, webRIDHint)
		if cErr != nil {
			if site.IsRoundStop(cErr) {
				if rep, rOK := provider.(LiveErrorReporter); rOK {
					rep.ReportAutomaticError(ctx, cErr)
				}
				if site.IsCircuitBreak(cErr) {
					m.recordSiteAutoStop(site.Douyin, site.KindOf(cErr))
				}
				return m.ListRooms(), cErr
			}
			continue
		}
		if liveSt.AccountUID == "" {
			liveSt.AccountUID = uid
		}
		roomState := m.applyDouyinLiveStatus(liveSt, webRIDHint, t.acc, now)

		m.mu.Lock()
		prev := m.douyinPrevLive[uid]
		m.douyinPrevLive[uid] = liveSt.Status
		if liveSt.Status != site.LiveStateLive && liveSt.RoomID != "" {
			delete(m.douyinStoppedRooms, liveSt.RoomID)
		}
		if liveSt.Status == site.LiveStateLive && prev != site.LiveStateLive && notifyGlobal && t.sub.LiveNotify {
			notifications = append(notifications, Notification{
				Site:       string(site.Douyin),
				AccountUID: uid,
				RoomID:     roomState.RoomID,
				RoomIDStr:  liveSt.RoomID,
				WebRID:     roomState.WebRID,
				Uname:      firstNonEmpty(roomState.Uname, t.sub.Name),
				Title:      roomState.Title,
				StartedAt:  now.UTC().Format(time.RFC3339),
			})
		}
		suppressed := liveSt.RoomID != "" && m.douyinStoppedRooms[liveSt.RoomID]
		_, running := m.douyinSessions[liveSt.RoomID]
		shouldStart := liveSt.Status == site.LiveStateLive && liveSt.RoomID != "" && !m.stopping && !suppressed && !running
		m.mu.Unlock()

		if shouldStart {
			toStart = append(toStart, startReq{uid: uid, webRID: firstNonEmpty(roomState.WebRID, liveSt.RoomID)})
		}
	}

	for _, n := range notifications {
		if m.onNotify != nil {
			m.onNotify(n)
		}
	}
	for _, req := range toStart {
		if _, sErr := m.StartDouyinRecording(bilibili.WithAutomaticRequests(ctx, "live"), req.uid, req.webRID); sErr != nil {
			m.mu.Lock()
			for k, st := range m.douyinStates {
				if st.AccountUID == req.uid {
					st.LastError = sErr.Error()
					m.douyinStates[k] = st
				}
			}
			m.mu.Unlock()
		}
	}
	return m.ListRooms(), nil
}

// StartRecordingByKey dispatches live recording start by canonical room/account key or numeric Bilibili room ID.
func (m *Manager) StartRecordingByKey(ctx context.Context, key string) (RoomState, error) {
	key = strings.TrimSpace(key)
	switch {
	case strings.HasPrefix(key, "douyin:live:"):
		rid := strings.TrimPrefix(key, "douyin:live:")
		return m.StartDouyinRecording(ctx, "", rid)
	case strings.HasPrefix(key, "douyin:user:"):
		uid := strings.TrimPrefix(key, "douyin:user:")
		return m.StartDouyinRecording(ctx, uid, "")
	case strings.HasPrefix(key, "douyin:"):
		uid := strings.TrimPrefix(key, "douyin:")
		return m.StartDouyinRecording(ctx, uid, "")
	case strings.HasPrefix(key, "bilibili:live:"):
		rid, err := strconv.ParseInt(strings.TrimPrefix(key, "bilibili:live:"), 10, 64)
		if err != nil || rid <= 0 {
			return RoomState{}, &site.Error{Site: site.Bilibili, Kind: site.ErrKindBadRequest, Message: "无效的直播间房间号"}
		}
		return m.StartRecording(ctx, rid)
	default:
		if rid, err := strconv.ParseInt(key, 10, 64); err == nil && rid > 0 {
			m.mu.Lock()
			_, isDouyinRoom := m.douyinStates["douyin:live:"+key]
			m.mu.Unlock()
			if isDouyinRoom {
				return m.StartDouyinRecording(ctx, "", key)
			}
			return m.StartRecording(ctx, rid)
		}
		return RoomState{}, &site.Error{Kind: site.ErrKindBadRequest, Message: "无效的直播间标识"}
	}
}

// StopRecordingByKey stops an active Bilibili or Douyin live recording by room key, account key, or room ID.
func (m *Manager) StopRecordingByKey(key string) (RoomState, error) {
	key = strings.TrimSpace(key)
	switch {
	case strings.HasPrefix(key, "douyin:live:"):
		return m.StopDouyinRecording(strings.TrimPrefix(key, "douyin:live:"))
	case strings.HasPrefix(key, "douyin:user:"):
		return m.StopDouyinRecording(strings.TrimPrefix(key, "douyin:user:"))
	case strings.HasPrefix(key, "douyin:"):
		return m.StopDouyinRecording(strings.TrimPrefix(key, "douyin:"))
	case strings.HasPrefix(key, "bilibili:live:"):
		rid, _ := strconv.ParseInt(strings.TrimPrefix(key, "bilibili:live:"), 10, 64)
		return m.StopRecording(rid)
	default:
		m.mu.Lock()
		_, hasDouyin := m.douyinSessions[key]
		m.mu.Unlock()
		if hasDouyin {
			return m.StopDouyinRecording(key)
		}
		if rid, err := strconv.ParseInt(key, 10, 64); err == nil && rid > 0 {
			return m.StopRecording(rid)
		}
		return m.StopDouyinRecording(key)
	}
}

// StartDouyinRecording starts an HTTP-FLV live recording into
// <cache_dir>/douyin/<sha256(sec_uid)[:12]>/lives/<room_id>/ with item key douyin:live:<room_id>.
func (m *Manager) StartDouyinRecording(ctx context.Context, uid, roomOrWebRID string) (RoomState, error) {
	m.startMu.Lock()
	defer m.startMu.Unlock()

	m.mu.Lock()
	stopping := m.stopping
	m.mu.Unlock()
	if stopping {
		return RoomState{}, &site.Error{Site: site.Douyin, Kind: site.ErrKindUnavailable, Message: "应用正在退出，不能开始新的录制"}
	}

	provider, ok := m.getSiteProvider(site.Douyin)
	if !ok {
		return RoomState{}, &site.Error{Site: site.Douyin, Kind: site.ErrKindUnavailable, Message: "抖音直播服务未初始化"}
	}

	uid = strings.TrimSpace(uid)
	target := strings.TrimSpace(roomOrWebRID)
	if uid == "" && target != "" {
		m.mu.Lock()
		if st, exists := m.douyinStates["douyin:live:"+target]; exists && st.AccountUID != "" {
			uid = st.AccountUID
			if st.WebRID != "" {
				target = st.WebRID
			}
		} else {
			for _, st := range m.douyinStates {
				if st.WebRID == target || st.RoomIDStr == target {
					uid = st.AccountUID
					break
				}
			}
		}
		m.mu.Unlock()
	}
	if uid != "" {
		if normUID, err := site.NormalizeUID(site.Douyin, uid); err == nil {
			uid = normUID
		}
		if target == "" {
			subAcc := m.findDouyinSubAccount(uid)
			m.mu.Lock()
			target = firstNonEmpty(m.douyinWebRIDHints[uid], firstNonEmpty(subAcc.WebRID, m.douyinRoomHints[uid]))
			m.mu.Unlock()
		}
	}

	m.mu.Lock()
	maxRec := m.liveSettings.MaxConcurrentRecordings
	if maxRec <= 0 {
		maxRec = 2
	}
	codecOrder := append([]string(nil), m.liveSettings.CodecOrder...)
	m.mu.Unlock()

	cacheRoot := m.lib.CacheDir()
	if m.freeBytesFunc != nil && m.minFreeBytes > 0 {
		freeBytes, fErr := m.freeBytesFunc(cacheRoot)
		if fErr != nil {
			return RoomState{}, fErr
		}
		if freeBytes < m.minFreeBytes {
			return RoomState{}, &site.Error{Site: site.Douyin, Kind: site.ErrKindStorage, Message: "磁盘可用空间不足 1 GiB，无法开始录制"}
		}
	}

	plan, err := provider.SelectLiveStream(ctx, uid, target, codecOrder)
	if err != nil {
		return RoomState{}, err
	}
	if plan.Status == site.LiveStateAudioOnly {
		return RoomState{}, &site.Error{
			Site:        site.Douyin,
			Kind:        site.ErrKindUnavailable,
			Unsupported: true,
			Message:     "这个直播间是语音直播，暂不支持录制",
		}
	}
	if plan.Status != site.LiveStateLive {
		return RoomState{}, &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindUnavailable,
			Message: "主播当前未开播，仅在直播中时支持录制",
		}
	}
	if len(plan.URLs) == 0 || (plan.Transport != "" && plan.Transport != site.LiveTransportHTTPFLV) || strings.EqualFold(plan.Format, "ts") {
		return RoomState{}, &site.Error{
			Site:        site.Douyin,
			Kind:        site.ErrKindUnavailable,
			Unsupported: true,
			Message:     "这个直播间仅提供 HLS 流或缺少可录制的 HTTP-FLV 流，当前版本暂不支持录制",
		}
	}

	roomID := strings.TrimSpace(plan.RoomID)
	if roomID == "" || library.SanitizeFileName(roomID) != roomID {
		return RoomState{}, &site.Error{Site: site.Douyin, Kind: site.ErrKindSchemaChanged, Message: "抖音直播间缺少有效 room_id"}
	}
	secUID, err := site.NormalizeUID(site.Douyin, firstNonEmpty(plan.AccountUID, uid))
	if err != nil {
		return RoomState{}, &site.Error{Site: site.Douyin, Kind: site.ErrKindSchemaChanged, Message: "抖音直播间缺少有效主播 sec_uid"}
	}
	plan.AccountUID = secUID

	m.mu.Lock()
	if existingSess, already := m.douyinSessions[roomID]; already && existingSess != nil {
		st := m.hydrateDouyinStateLocked(m.douyinStates[existingSess.stateKey])
		m.mu.Unlock()
		return st, nil
	}
	if len(m.sessions)+len(m.douyinSessions) >= maxRec {
		m.mu.Unlock()
		return RoomState{}, fmt.Errorf("已达到同时录制上限（%d 路），未开始录制", maxRec)
	}
	m.mu.Unlock()

	subAcc := m.findDouyinSubAccount(secUID)
	// Populate room display metadata from cache or CheckLiveStatus.
	st, okCached := m.CachedAccountState(site.Douyin, secUID)
	if !okCached || st.RoomIDStr != roomID || st.Title == "" {
		forceFresh := !okCached || st.RoomIDStr != roomID
		if freshSt, cErr := m.CheckAccountLiveStatus(ctx, site.Douyin, secUID, firstNonEmpty(plan.WebRID, target), forceFresh); cErr == nil {
			st = freshSt
		}
	}

	now := m.nowFunc().UTC()
	liveSince := st.LiveSince
	if liveSince <= 0 {
		liveSince = now.Unix()
	}
	webRID := firstNonEmpty(plan.WebRID, st.WebRID)
	canonicalURL := "https://live.douyin.com/" + firstNonEmpty(webRID, roomID)

	itemDir, err := library.EnsureWithinRoot(cacheRoot, filepath.Join(cacheRoot, "douyin", site.DouyinAuthorDir(secUID), "lives", roomID))
	if err != nil {
		return RoomState{}, err
	}
	itemKey := library.CanonicalItemKey("douyin", "live", roomID)
	existingMeta, exists, err := m.lib.GetItem(itemKey)
	if err != nil {
		return RoomState{}, err
	}
	if exists && existingMeta.ItemDir != "" {
		itemDir = existingMeta.ItemDir
	}
	if err := os.MkdirAll(itemDir, 0o700); err != nil {
		return RoomState{}, err
	}

	parsedRoomInt, _ := strconv.ParseInt(roomID, 10, 64)
	title := firstNonEmpty(st.Title, "抖音直播 "+firstNonEmpty(webRID, roomID))
	uname := firstNonEmpty(st.Uname, subAcc.Name)
	face := sanitizeLiveURL(firstNonEmpty(st.Face, subAcc.Face))

	meta := library.ItemMetadata{
		Version:  1,
		ID:       "live:" + roomID,
		Site:     "douyin",
		Kind:     "live",
		Status:   "recording",
		Title:    title,
		URL:      canonicalURL,
		PubTs:    liveSince,
		CachedAt: now.Format(time.RFC3339),
		Author: library.ItemAuthor{
			Site:     "douyin",
			UID:      secUID,
			UniqueID: subAcc.UniqueID,
			Name:     uname,
			Face:     face,
		},
	}
	if exists {
		meta = existingMeta
		meta.Status = "recording"
		if title != "" {
			meta.Title = title
		}
		meta.URL = canonicalURL
		meta.Author.Site = "douyin"
		meta.Author.UID = secUID
		if uname != "" {
			meta.Author.Name = uname
		}
		if face != "" {
			meta.Author.Face = face
		}
	}
	if meta.Live == nil {
		meta.Live = &library.LiveRecordingInfo{
			RoomID:    parsedRoomInt,
			RoomIDStr: roomID,
			WebRID:    webRID,
			LiveSince: liveSince,
			StartedAt: now.Format(time.RFC3339),
		}
	}
	meta.Live.RoomID = parsedRoomInt
	meta.Live.RoomIDStr = roomID
	if webRID != "" {
		meta.Live.WebRID = webRID
	}
	meta.Live.EndedAt, meta.Live.StopReason = "", ""
	meta.Live.Quality = plan.Quality
	meta.Live.QualityName = plan.QualityName
	meta.Live.Codec = plan.Codec
	meta.Live.Source = "manual"
	if bilibili.AutomaticRequestSource(ctx) != "" {
		meta.Live.Source = "auto"
	}

	savedMeta, err := m.lib.SaveItem(itemDir, meta)
	if err != nil {
		return RoomState{}, err
	}

	transport := plan.Media
	if len(transport.AllowedDomains) == 0 {
		transport = site.DouyinLiveTransport("", false)
	}

	recParent := context.Background()
	if meta.Live.Source == "auto" {
		recParent = bilibili.WithAutomaticRequests(recParent, "live")
	}
	recCtx, recCancel := context.WithCancel(recParent)
	stateKey := "douyin:live:" + roomID
	sess := &recordSession{
		siteID:       string(site.Douyin),
		accountUID:   secUID,
		roomIDStr:    roomID,
		webRID:       webRID,
		stateKey:     stateKey,
		coverURL:     st.Cover,
		transport:    transport,
		sessionID:    roomID,
		roomID:       parsedRoomInt,
		uname:        uname,
		face:         face,
		title:        title,
		itemDir:      itemDir,
		itemMeta:     savedMeta,
		cancel:       recCancel,
		doneCh:       make(chan struct{}),
		startedAt:    now,
		reportAt:     now,
		nextPart:     nextPartNumber(savedMeta),
		lastSequence: savedMeta.Live.LastSequence,
		hasSequence:  savedMeta.Live.HasSequence,
		quality:      plan.Quality,
		qualityName:  plan.QualityName,
		codec:        plan.Codec,
	}
	for _, media := range savedMeta.Media {
		if media.Role == "live_segment" {
			sess.segments = append(sess.segments, media.Path)
			sess.bytesTotal += media.Bytes
		}
	}

	outState := RoomState{
		Site:           string(site.Douyin),
		AccountUID:     secUID,
		UniqueID:       subAcc.UniqueID,
		RoomID:         parsedRoomInt,
		RoomIDStr:      roomID,
		WebRID:         webRID,
		RoomKey:        stateKey,
		LiveState:      site.LiveStateLive,
		Uname:          uname,
		Face:           face,
		Title:          title,
		Cover:          sanitizeLiveURL(st.Cover),
		AreaName:       st.AreaName,
		LiveStatus:     1,
		LiveSince:      liveSince,
		Source:         meta.Live.Source,
		StatusText:     "直播中",
		Quality:        plan.Quality,
		QualityName:    plan.QualityName,
		Codec:          plan.Codec,
		Recording:      true,
		RecordSession:  roomID,
		RecordBytes:    sess.bytesTotal,
		RecordSegments: len(sess.segments),
		RecordItemDir:  itemDir,
		UpdatedAt:      now.Format(time.RFC3339),
	}

	m.mu.Lock()
	delete(m.douyinStoppedRooms, roomID)
	m.douyinSessions[roomID] = sess
	m.douyinStates[stateKey] = outState
	for k, old := range m.douyinStates {
		if k != stateKey && old.AccountUID == secUID && !m.isRoomActivelyRecordingLocked(old.RoomIDStr) {
			delete(m.douyinStates, k)
		}
	}
	m.mu.Unlock()

	m.wg.Add(1)
	go m.runDouyinRecordingSession(recCtx, sess, plan, codecOrder)

	if m.onState != nil {
		m.onState(outState)
	}
	return outState, nil
}

// StopDouyinRecording stops an active Douyin live recording by room_id, web_rid, or主播 sec_uid,
// suppressing auto-restart for the remainder of that room_id broadcast.
func (m *Manager) StopDouyinRecording(roomOrUID string) (RoomState, error) {
	target := strings.TrimSpace(roomOrUID)
	m.mu.Lock()
	var sess *recordSession
	var roomID string
	if s, ok := m.douyinSessions[target]; ok {
		sess = s
		roomID = target
	} else {
		for rid, s := range m.douyinSessions {
			if s.accountUID == target || s.webRID == target || s.stateKey == target {
				sess = s
				roomID = rid
				break
			}
		}
	}
	if roomID != "" {
		m.douyinStoppedRooms[roomID] = true
	}
	if sess != nil && sess.stopReason == "" {
		sess.stopReason = "user_stop"
	}
	m.mu.Unlock()

	if sess == nil {
		m.mu.Lock()
		for _, st := range m.douyinStates {
			if st.RoomIDStr == target || st.AccountUID == target || st.WebRID == target || st.RoomKey == target {
				st.Recording = false
				m.mu.Unlock()
				return st, nil
			}
		}
		m.mu.Unlock()
		return RoomState{Site: string(site.Douyin)}, nil
	}

	sess.cancel()
	select {
	case <-sess.doneCh:
	case <-time.After(m.finalizeTimeout):
		m.mu.Lock()
		state := m.douyinStates[sess.stateKey]
		m.mu.Unlock()
		return state, &site.Error{Site: site.Douyin, Kind: site.ErrKindStorage, Message: "录制收尾已到时间上限，临时片段会在下次启动时恢复"}
	}

	m.mu.Lock()
	st := m.douyinStates[sess.stateKey]
	st.Recording = false
	st.RecordDurationSec = m.nowFunc().Sub(sess.startedAt).Seconds()
	st.RecordBytes = sess.bytesTotal
	st.RecordSegments = len(sess.segments)
	st.RecordItemDir = sess.itemDir
	st.StopReason = sess.stopReason
	if sess.lastErr != "" {
		st.LastError = sess.lastErr
	}
	m.douyinStates[sess.stateKey] = st
	m.mu.Unlock()

	if m.onState != nil {
		m.onState(st)
	}
	return st, nil
}

func (m *Manager) runDouyinRecordingSession(ctx context.Context, sess *recordSession, initial site.LiveStreamPlan, codecOrder []string) {
	defer m.wg.Done()
	defer close(sess.doneCh)
	defer func() {
		m.mu.Lock()
		delete(m.douyinSessions, sess.roomIDStr)
		m.mu.Unlock()
	}()

	var flvConn *activeFLVConn
	defer func() {
		if flvConn != nil {
			_ = flvConn.Close()
		}
	}()

	plan := initial
	if len(plan.Media.AllowedDomains) == 0 {
		plan.Media = sess.transport
	}
	_ = m.saveDouyinLiveCover(ctx, sess, plan.Media)

	provider, _ := m.getSiteProvider(site.Douyin)
	var lostAt time.Time

	for ctx.Err() == nil {
		m.mu.Lock()
		stopped := sess.stopReason != ""
		m.mu.Unlock()
		if stopped {
			break
		}
		if m.freeBytesFunc != nil && m.minFreeBytes > 0 {
			if free, fErr := m.freeBytesFunc(sess.itemDir); fErr != nil {
				m.setSessionStop(sess, "storage", fErr)
				break
			} else if free < m.minFreeBytes {
				m.setSessionStop(sess, "storage", &site.Error{Site: site.Douyin, Kind: site.ErrKindStorage, Message: "磁盘空间不足 1 GiB，已停止录制"})
				break
			}
		}

		partRel := fmt.Sprintf("part-%03d.mp4", sess.nextPart)
		pending := partRel + ".part"
		path, err := library.ResolveItemPath(sess.itemDir, pending)
		if err != nil {
			m.setSessionStop(sess, "storage", err)
			break
		}
		for _, candidate := range []string{path, strings.TrimSuffix(path, ".part")} {
			if _, statErr := os.Lstat(candidate); statErr == nil {
				err = &site.Error{Site: site.Douyin, Kind: site.ErrKindStorage, Message: "录像目标已存在且未登记，原文件已保留"}
				break
			} else if !os.IsNotExist(statErr) {
				err = statErr
				break
			}
		}
		if err != nil {
			m.setSessionStop(sess, "storage", err)
			break
		}

		sess.itemMeta.PendingParts = append(sess.itemMeta.PendingParts, pending)
		sess.itemMeta.Live.Codec = plan.Codec
		sess.itemMeta.Live.Quality = plan.Quality
		sess.itemMeta.Live.QualityName = plan.QualityName
		m.mu.Lock()
		sess.codec, sess.quality, sess.qualityName = plan.Codec, plan.Quality, plan.QualityName
		if st, ok := m.douyinStates[sess.stateKey]; ok {
			st.Codec, st.Quality, st.QualityName = plan.Codec, plan.Quality, plan.QualityName
			m.douyinStates[sess.stateKey] = st
		}
		m.mu.Unlock()
		if err := m.saveSession(sess); err != nil {
			m.setSessionStop(sess, "storage", err)
			break
		}

		sess.segmentDuration, sess.wroteMedia = 0, false
		n, recordErr := m.recordFLVPartWithTransport(ctx, sess, plan.Media, plan.URLs, &flvConn, path)
		if !errors.Is(recordErr, errSegmentRollover) && flvConn != nil {
			_ = flvConn.Close()
			flvConn = nil
		}

		if n > 0 {
			finishCtx, cancel := context.WithTimeout(context.Background(), m.finalizeTimeout)
			summary, conversionErr := finalizeLivePart(finishCtx, path, filepath.Join(sess.itemDir, partRel), sess.segmentDuration)
			cancel()
			if conversionErr != nil {
				addLiveMissing(&sess.itemMeta, pending, "转换失败，临时片段已保留")
			} else {
				sess.itemMeta.Media = mergeLiveMedia(sess.itemMeta.Media, library.MediaFile{
					Role:     "live_segment",
					Path:     partRel,
					Bytes:    summary.FileBytes,
					SHA256:   summary.SHA256,
					Duration: int(math.Round(summary.DurationSec)),
					Width:    summary.Width,
					Height:   summary.Height,
				})
				sess.itemMeta.PendingParts = removePending(sess.itemMeta.PendingParts, pending)
				clearLiveCheckpoint(&sess.itemMeta, pending)
				clearLiveMissing(&sess.itemMeta, pending)
				codecName := sess.codec
				if codecName == "" && len(summary.Codecs) > 0 {
					codecName = summary.Codecs[0]
				}
				sess.itemMeta.Live.Segments = mergeLiveSegment(sess.itemMeta.Live.Segments, library.LiveSegmentInfo{
					File:        partRel,
					Bytes:       summary.FileBytes,
					SHA256:      summary.SHA256,
					DurationSec: summary.DurationSec,
					Codec:       codecName,
					Width:       summary.Width,
					Height:      summary.Height,
					Quality:     plan.Quality,
					QualityName: plan.QualityName,
				})
				m.mu.Lock()
				sess.segments = append(sess.segments, partRel)
				m.mu.Unlock()
				if err := m.saveSession(sess); err != nil {
					m.setSessionStop(sess, "storage", err)
					break
				}
				_ = os.Remove(path)
			}
			sess.nextPart++
			if sess.wroteMedia {
				lostAt = time.Time{}
			}
		} else {
			_ = os.Remove(path)
			sess.itemMeta.PendingParts = removePending(sess.itemMeta.PendingParts, pending)
			clearLiveCheckpoint(&sess.itemMeta, pending)
		}

		if err := m.saveSession(sess); err != nil {
			m.setSessionStop(sess, "storage", err)
			break
		}
		if ctx.Err() != nil {
			break
		}
		if errors.Is(recordErr, errSegmentRollover) {
			continue
		}
		if recordErr != nil {
			if reason := liveStopReason(recordErr); reason != "" {
				if rep, ok := provider.(LiveErrorReporter); ok {
					rep.ReportAutomaticError(ctx, recordErr)
				}
				if reason == "risk_control" || reason == "rate_limited" {
					m.recordSiteAutoStop(site.Douyin, reason)
				}
				m.setSessionStop(sess, reason, recordErr)
				break
			}
			m.mu.Lock()
			sess.lastErr = recordErr.Error()
			m.mu.Unlock()
			if lostAt.IsZero() {
				lostAt = m.nowFunc()
			}
			if m.maxStreamLossDur > 0 && m.nowFunc().Sub(lostAt) >= m.maxStreamLossDur {
				m.setSessionStop(sess, "stream_timeout", recordErr)
				break
			}
		}

		select {
		case <-ctx.Done():
		case <-time.After(m.reconnectWait):
		}
		if ctx.Err() != nil {
			break
		}
		if provider == nil {
			m.setSessionStop(sess, "ended", nil)
			break
		}

		// Never reuse an expired/disconnected FLV URL; check room state and re-select stream.
		liveSt, statusErr := provider.CheckLiveStatus(ctx, sess.accountUID, sess.webRID)
		if statusErr == nil {
			if liveSt.Status != site.LiveStateLive || (liveSt.RoomID != "" && liveSt.RoomID != sess.roomIDStr) {
				m.setSessionStop(sess, "ended", nil)
				break
			}
			if liveSt.WebRID != "" {
				sess.webRID = liveSt.WebRID
			}
		} else if reason := liveStopReason(statusErr); reason != "" {
			if rep, ok := provider.(LiveErrorReporter); ok {
				rep.ReportAutomaticError(ctx, statusErr)
			}
			if reason == "risk_control" || reason == "rate_limited" {
				m.recordSiteAutoStop(site.Douyin, reason)
			}
			m.setSessionStop(sess, reason, statusErr)
			break
		}

		freshPlan, selErr := provider.SelectLiveStream(ctx, sess.accountUID, firstNonEmpty(sess.webRID, sess.roomIDStr), codecOrder)
		if selErr != nil {
			var sErr *site.Error
			if errors.As(selErr, &sErr) && sErr.Kind == site.ErrKindUnavailable && !sErr.Unsupported {
				// Room went offline right between CheckLiveStatus and SelectLiveStream.
				if st2, sErr2 := provider.CheckLiveStatus(ctx, sess.accountUID, sess.webRID); sErr2 == nil && st2.Status != site.LiveStateLive {
					m.setSessionStop(sess, "ended", nil)
					break
				}
			}
			if reason := liveStopReason(selErr); reason != "" {
				if rep, ok := provider.(LiveErrorReporter); ok {
					rep.ReportAutomaticError(ctx, selErr)
				}
				if reason == "risk_control" || reason == "rate_limited" {
					m.recordSiteAutoStop(site.Douyin, reason)
				}
				m.setSessionStop(sess, reason, selErr)
				break
			}
			if lostAt.IsZero() {
				lostAt = m.nowFunc()
			}
			if m.maxStreamLossDur > 0 && m.nowFunc().Sub(lostAt) >= m.maxStreamLossDur {
				m.setSessionStop(sess, "stream_timeout", selErr)
				break
			}
			continue
		}
		if freshPlan.Status == site.LiveStateAudioOnly || (freshPlan.Transport != "" && freshPlan.Transport != site.LiveTransportHTTPFLV) || strings.EqualFold(freshPlan.Format, "ts") {
			m.setSessionStop(sess, site.ErrKindBlocked, &site.Error{
				Site:    site.Douyin,
				Kind:    site.ErrKindBlocked,
				Message: "直播间已切换为不支持录制的流格式",
			})
			break
		}
		if freshPlan.Status != site.LiveStateLive || len(freshPlan.URLs) == 0 || (freshPlan.RoomID != "" && freshPlan.RoomID != sess.roomIDStr) {
			m.setSessionStop(sess, "ended", nil)
			break
		}
		if len(freshPlan.Media.AllowedDomains) == 0 {
			freshPlan.Media = sess.transport
		}
		plan = freshPlan
	}

	m.mu.Lock()
	if sess.stopReason == "" {
		if ctx.Err() != nil {
			sess.stopReason = "app_closed"
		} else {
			sess.stopReason = "ended"
		}
	}
	reason := sess.stopReason
	state := m.douyinStates[sess.stateKey]
	state.Recording = false
	state.StopReason = reason
	state.LastError = sess.lastErr
	state.RecordDurationSec = m.nowFunc().Sub(sess.startedAt).Seconds()
	state.RecordBytes = sess.bytesTotal
	state.RecordSegments = len(sess.segments)
	if reason == "ended" {
		state.LiveStatus = 0
		state.LiveState = site.LiveStateOffline
		state.StatusText = "未开播"
	}
	m.douyinStates[sess.stateKey] = state
	m.mu.Unlock()

	sess.itemMeta.Live.StopReason = reason
	sess.itemMeta.Live.EndedAt = m.nowFunc().UTC().Format(time.RFC3339)
	sess.itemMeta.Live.EndTitle = state.Title
	sess.itemMeta.Status = "complete"
	if len(sess.itemMeta.Live.Segments) == 0 {
		sess.itemMeta.Status = "failed"
	}
	if reason != "ended" && reason != "user_stop" && reason != "app_closed" {
		addLiveMissing(&sess.itemMeta, "recording", "录制提前结束 ("+reason+")")
	}
	if len(sess.itemMeta.PendingParts) > 0 || len(sess.itemMeta.Missing) > 0 {
		sess.itemMeta.Status = "partial"
	}
	if err := m.saveSession(sess); err != nil {
		m.setSessionStop(sess, "storage", err)
		state.StopReason, state.LastError = "storage", "录像清单写入失败，已保留可恢复文件"
		m.mu.Lock()
		m.douyinStates[sess.stateKey] = state
		m.mu.Unlock()
	}
	if m.onState != nil {
		m.onState(state)
	}
}

type activeFLVConn struct {
	body    io.ReadCloser
	remuxer *FLVRemuxer
}

func (c *activeFLVConn) Close() error {
	if c == nil || c.body == nil {
		return nil
	}
	err := c.body.Close()
	c.body = nil
	return err
}

func (m *Manager) recordBilibiliFLVPart(
	ctx context.Context,
	sess *recordSession,
	urls []string,
	connPtr **activeFLVConn,
	partPath string,
) (int64, error) {
	allowLocal := false
	if m.client != nil {
		allowLocal = m.client.ValidateMediaURL("http://127.0.0.1:1/test.flv") == nil
	}
	spec := site.BilibiliMediaTransport(allowLocal)
	spec.Referer = "https://live.bilibili.com/"
	spec.Origin = "https://live.bilibili.com"
	return m.recordFLVPartWithTransport(ctx, sess, spec, urls, connPtr, partPath)
}

func (m *Manager) recordFLVPartWithTransport(
	ctx context.Context,
	sess *recordSession,
	transport site.MediaTransportSpec,
	urls []string,
	connPtr **activeFLVConn,
	partPath string,
) (written int64, result error) {
	if *connPtr == nil {
		m.mu.Lock()
		idleDur := m.flvIdleTimeout
		m.mu.Unlock()
		body, err := OpenHTTPFLVStream(ctx, transport, urls, idleDur)
		if err != nil {
			return 0, err
		}
		remuxer := NewFLVRemuxer(body, FLVRemuxOptions{})
		*connPtr = &activeFLVConn{
			body:    body,
			remuxer: remuxer,
		}
	}

	f, err := os.OpenFile(partPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return 0, err
	}
	defer func() {
		if sErr := f.Sync(); sErr != nil && result == nil {
			result = sErr
		}
		if cErr := f.Close(); cErr != nil && result == nil {
			result = cErr
		}
	}()

	var prevBytes int64
	cp, wErr := (*connPtr).remuxer.WritePart(ctx, f, func(cp FLVCheckpoint) error {
		if m.freeBytesFunc != nil && m.minFreeBytes > 0 {
			free, fErr := m.freeBytesFunc(sess.itemDir)
			if fErr != nil {
				return fErr
			}
			if free < m.minFreeBytes {
				return &site.Error{Site: transport.Site, Kind: site.ErrKindStorage, Message: "磁盘空间不足 1 GiB，已停止录制"}
			}
		}
		delta := cp.BytesWritten - prevBytes
		if delta > 0 {
			prevBytes = cp.BytesWritten
			written = cp.BytesWritten
			m.addSessionBytes(sess, delta)
		}
		sess.wroteMedia = true
		sess.segmentDuration = cp.DurationSec
		sess.lastSequence = cp.Sequence
		sess.hasSequence = true
		if cp.Codec != "" {
			sess.codec = cp.Codec
			sess.itemMeta.Live.Codec = cp.Codec
		}
		sess.itemMeta.Live.LastSequence = cp.Sequence
		sess.itemMeta.Live.HasSequence = true
		sess.itemMeta.Live.PendingFile = filepath.Base(partPath)
		sess.itemMeta.Live.PendingBytes = cp.BytesWritten
		sess.itemMeta.Live.PendingDuration = cp.DurationSec
		_, saveErr := m.lib.SaveRecordingCheckpoint(sess.itemDir, sess.itemMeta)
		return saveErr
	})
	if cp.BytesWritten > written {
		m.addSessionBytes(sess, cp.BytesWritten-written)
		written = cp.BytesWritten
	}
	return written, wErr
}

// OpenHTTPFLVStream opens an HTTP-FLV live stream over HTTPS (or localhost in tests),
// enforcing a 10s connect/header timeout, per-read idle timeout, <=5 redirect hops (ValidateRedirectURL),
// Referer/Origin headers with Cookie stripped, and rejecting HTML/JSON disguised responses.
func OpenHTTPFLVStream(ctx context.Context, transport site.MediaTransportSpec, urls []string, idleTimeout time.Duration) (io.ReadCloser, error) {
	if idleTimeout <= 0 {
		idleTimeout = defaultFLVIdleTimeout
	}
	var cleanURLs []string
	var lastValidateErr error
	for _, u := range urls {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if err := transport.ValidateURL(u); err != nil {
			lastValidateErr = err
			continue
		}
		cleanURLs = append(cleanURLs, u)
	}
	if len(cleanURLs) == 0 {
		if lastValidateErr != nil {
			return nil, lastValidateErr
		}
		return nil, &site.Error{Site: transport.Site, Kind: site.ErrKindUnavailable, Message: "直播拉流地址列表为空"}
	}

	httpClient := newLiveTransportClient(transport, defaultFLVConnectTimeout)
	var lastErr error
	for _, targetURL := range cleanURLs {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
		if err != nil {
			lastErr = err
			continue
		}
		transport.ApplyHeaders(req)

		resp, err := httpClient.Do(req)
		if err != nil {
			var se *site.Error
			if errors.As(err, &se) {
				lastErr = se
				if site.IsRoundStop(se) || se.Kind == site.ErrKindBlocked {
					return nil, se
				}
				continue
			}
			lastErr = &site.Error{Site: transport.Site, Kind: site.ErrKindNetwork, Message: "连接直播流节点失败", Err: err}
			continue
		}

		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			se := mapLiveHTTPStatus(transport.Site, resp.StatusCode)
			lastErr = se
			if site.IsRoundStop(se) {
				return nil, se
			}
			continue
		}

		ct := strings.TrimSpace(resp.Header.Get("Content-Type"))
		if badErr := checkLiveFLVContentType(transport.Site, ct); badErr != nil {
			_ = resp.Body.Close()
			return nil, badErr
		}

		return newFLVIdleReader(resp.Body, idleTimeout), nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, &site.Error{Site: transport.Site, Kind: site.ErrKindNetwork, Message: "无法连接任何直播流节点"}
}

func newLiveTransportClient(spec site.MediaTransportSpec, connectTimeout time.Duration) *http.Client {
	if connectTimeout <= 0 {
		connectTimeout = defaultFLVConnectTimeout
	}
	dialer := &net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second}
	guard := site.NewGuardedMediaDialer(spec.Site, spec.AllowLocalhost, dialer, "已拦截内网或回环直播流节点地址")
	tr := &http.Transport{
		Proxy:                 guard.Proxy,
		MaxIdleConns:          8,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   connectTimeout,
		ResponseHeaderTimeout: connectTimeout,
		DialContext:           guard.DialContext,
	}
	return &http.Client{
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return &site.Error{
					Site:    spec.Site,
					Kind:    site.ErrKindBlocked,
					Message: "直播流重定向超过 5 跳上限",
				}
			}
			if err := spec.ValidateRedirectURL(req.URL.String()); err != nil {
				return err
			}
			spec.ApplyHeaders(req)
			return nil
		},
	}
}

func mapLiveHTTPStatus(siteID site.ID, status int) *site.Error {
	switch status {
	case http.StatusUnauthorized:
		return &site.Error{Site: siteID, Kind: site.ErrKindAuthRequired, HTTPStatus: status, Message: "直播拉流需要有效登录凭证 (401)"}
	case http.StatusPreconditionFailed:
		return &site.Error{Site: siteID, Kind: site.ErrKindRiskControl, HTTPStatus: status, Message: "直播请求触发风控 (412)，已停止录制"}
	case http.StatusTooManyRequests:
		return &site.Error{Site: siteID, Kind: site.ErrKindRateLimited, HTTPStatus: status, Message: "直播请求触发频率限制 (429)，已停止录制"}
	case http.StatusForbidden:
		return &site.Error{Site: siteID, Kind: site.ErrKindForbidden, HTTPStatus: status, Message: "直播流地址已过期或被拒绝 (403)"}
	case http.StatusNotFound, http.StatusGone:
		return &site.Error{Site: siteID, Kind: site.ErrKindUnavailable, HTTPStatus: status, Message: fmt.Sprintf("直播流节点不可用 (%d)", status)}
	default:
		return &site.Error{Site: siteID, Kind: site.ErrKindNetwork, HTTPStatus: status, Message: fmt.Sprintf("直播流 HTTP 状态异常 (%d)", status)}
	}
}

func checkLiveFLVContentType(siteID site.ID, rawCT string) error {
	if rawCT == "" {
		return nil
	}
	mt, _, err := mime.ParseMediaType(rawCT)
	if err != nil {
		mt = strings.ToLower(strings.TrimSpace(strings.SplitN(rawCT, ";", 2)[0]))
	}
	mt = strings.ToLower(strings.TrimSpace(mt))
	switch mt {
	case "text/html", "application/xhtml+xml", "application/json", "text/json", "text/plain", "application/vnd.apple.mpegurl", "application/x-mpegurl":
		return &site.Error{
			Site:    siteID,
			Kind:    site.ErrKindSchemaChanged,
			Message: fmt.Sprintf("直播流返回了非 FLV 响应类型 (%s)", mt),
		}
	}
	return nil
}

type flvIdleReader struct {
	r       io.ReadCloser
	timeout time.Duration
}

func newFLVIdleReader(r io.ReadCloser, timeout time.Duration) io.ReadCloser {
	if timeout <= 0 {
		return r
	}
	return &flvIdleReader{r: r, timeout: timeout}
}

func (f *flvIdleReader) Read(p []byte) (int, error) {
	type res struct {
		n   int
		err error
	}
	ch := make(chan res, 1)
	go func() {
		n, err := f.r.Read(p)
		ch <- res{n: n, err: err}
	}()
	timer := time.NewTimer(f.timeout)
	defer timer.Stop()
	select {
	case out := <-ch:
		return out.n, out.err
	case <-timer.C:
		_ = f.r.Close()
		return 0, &site.Error{Kind: site.ErrKindNetwork, Message: "直播流超过 15 秒无数据返回"}
	}
}

func (f *flvIdleReader) Close() error {
	return f.r.Close()
}

func (m *Manager) saveDouyinLiveCover(ctx context.Context, sess *recordSession, transport site.MediaTransportSpec) error {
	if strings.TrimSpace(sess.coverURL) == "" {
		return nil
	}
	for _, mf := range sess.itemMeta.Media {
		if mf.Role == "cover" && library.VerifyMediaFile(sess.itemDir, mf, true) {
			return nil
		}
	}
	vodSpec := site.DouyinVODTransport(transport.UserAgent, transport.AllowLocalhost)
	if err := vodSpec.ValidateURL(sess.coverURL); err != nil {
		return nil
	}
	client := newLiveTransportClient(vodSpec, defaultFLVConnectTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sess.coverURL, nil)
	if err != nil {
		return nil
	}
	vodSpec.ApplyHeaders(req)
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024*1024))
	if err != nil || len(raw) < 8 {
		return nil
	}
	ext := sniffLiveCoverExt(raw, resp.Header.Get("Content-Type"))
	if ext == "" {
		return nil
	}
	rel := "cover" + ext
	full, err := library.ResolveItemPath(sess.itemDir, rel)
	if err != nil {
		return nil
	}
	if err := storage.WriteFileAtomic(full, raw, 0o600); err != nil {
		return nil
	}
	sum := sha256.Sum256(raw)
	sess.itemMeta.Media = mergeLiveMedia(sess.itemMeta.Media, library.MediaFile{
		Role:   "cover",
		Path:   rel,
		Bytes:  int64(len(raw)),
		SHA256: hex.EncodeToString(sum[:]),
	})
	_ = m.saveSession(sess)
	return nil
}

func sniffLiveCoverExt(b []byte, ct string) string {
	switch {
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return ".jpg"
	case len(b) >= 8 && bytes.Equal(b[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'}):
		return ".png"
	case len(b) >= 12 && bytes.Equal(b[:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")):
		return ".webp"
	case len(b) >= 6 && (bytes.Equal(b[:6], []byte("GIF87a")) || bytes.Equal(b[:6], []byte("GIF89a"))):
		return ".gif"
	}
	mt, _, _ := mime.ParseMediaType(ct)
	switch strings.ToLower(strings.TrimSpace(mt)) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	}
	return ""
}

func sanitizeLiveURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func firstPositiveInt(a, b int) int {
	if a > 0 {
		return a
	}
	return b
}
