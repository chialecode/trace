package live

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/subscription"
)

type douyinCachedStatusEntry struct {
	status    site.LiveStatus
	state     RoomState
	fetchedAt time.Time
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
	var notify *RoomState
	nowStr := m.now().UTC().Format(time.RFC3339)
	m.mu.Lock()
	if webRID != "" && m.douyinWebRIDHints[normUID] != webRID {
		m.douyinWebRIDHints[normUID] = webRID
		syncNeeded = true
	}
	if roomID != "" && roomID != "0" {
		m.douyinRoomHints[normUID] = roomID
		effWebRID := firstNonEmpty(webRID, firstNonEmpty(m.douyinWebRIDHints[normUID], subAcc.WebRID))
		stateKey := douyinStateKey(roomID, normUID, effWebRID)
		prev, exists := m.douyinStates[stateKey]
		if !exists {
			if old, ok := m.findPrevDouyinStateLocked(normUID); ok {
				prev.RecordItemDir = old.RecordItemDir
				prev.Incidents = cloneIncidents(old.Incidents)
				prev.StopReason = old.StopReason
				prev.LastError = old.LastError
				prev.Quality = old.Quality
				prev.QualityName = old.QualityName
				prev.Codec = old.Codec
			}
		}
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
		hydrated := m.hydrateDouyinStateLocked(prev)
		m.douyinStates[stateKey] = hydrated
		for k, old := range m.douyinStates {
			if k != stateKey && old.AccountUID == normUID && !m.isRoomActivelyRecordingLocked(old.RoomIDStr) {
				delete(m.douyinStates, k)
			}
		}
		delete(m.douyinStatusCache, normUID)
		cp := hydrated
		notify = &cp
	}
	start := roomID != "" && roomID != "0" && m.douyinShouldAutoStartLocked(site.LiveStatus{Status: site.LiveStateLive, RoomID: roomID})
	m.mu.Unlock()

	if syncNeeded {
		m.syncWebRID(site.Douyin, normUID, webRID, false)
	}
	if notify != nil && m.onState != nil {
		m.onState(*notify)
	}
	// An update check that sees the account live starts its recording when
	// a subscription records its broadcasts, before the account's turn for
	// a live check. Starting asks for the stream, which confirms the broadcast;
	// a hint gone stale just starts nothing.
	if start && m.douyinAccountAutoRecords(normUID) {
		target := firstNonEmpty(webRID, roomID)
		m.mu.Lock()
		if !m.stopping && m.douyinShouldAutoStartLocked(site.LiveStatus{Status: site.LiveStateLive, RoomID: roomID}) {
			baseCtx := m.baseCtxLocked()
			m.wg.Add(1)
			m.mu.Unlock()
			go func() {
				defer m.wg.Done()
				_, _ = m.StartDouyinRecording(site.WithAutomaticRequests(baseCtx, "live"), normUID, target)
			}()
			return
		}
		m.mu.Unlock()
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
				m.normalizeBilibiliRoomStateLocked(&st)
				st.AccountUID = normUID
				if sess := m.sessions[st.RoomID]; sess != nil {
					m.hydrateBilibiliStateFromSessionLocked(&st, sess)
				} else {
					st.Recording = false
					hydrateSessionPreviewFieldsLocked(&st, nil)
				}
				return st, true
			}
		}
		return RoomState{}, false

	case site.Douyin:
		return m.cachedDouyinStateLocked(normUID)

	case site.Weibo, site.X:
		return RoomState{}, false
	}
	return RoomState{}, false
}

// cachedDouyinStateLocked resolves the single source of truth (m.douyinStates)
// for normUID without ever returning stale douyinStatusCache snapshots.
func (m *Manager) cachedDouyinStateLocked(normUID string) (RoomState, bool) {
	for _, sess := range m.douyinSessions {
		if sess != nil && sess.accountUID == normUID {
			if st, ok := m.douyinStates[sess.stateKey]; ok {
				return m.hydrateDouyinStateLocked(st), true
			}
		}
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

func (m *Manager) findPrevDouyinStateLocked(secUID string) (RoomState, bool) {
	if secUID == "" {
		return RoomState{}, false
	}
	var best RoomState
	var found bool
	for _, old := range m.douyinStates {
		if old.AccountUID != secUID {
			continue
		}
		oldHasMeta := old.RecordItemDir != "" || len(old.Incidents) > 0 || old.StopReason != "" || old.LastError != ""
		bestHasMeta := best.RecordItemDir != "" || len(best.Incidents) > 0 || best.StopReason != "" || best.LastError != ""
		if !found || (oldHasMeta && !bestHasMeta) || (oldHasMeta == bestHasMeta && old.UpdatedAt > best.UpdatedAt) {
			best = old
			found = true
		}
	}
	return best, found
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
		case site.Weibo, site.X:
			// Weibo and X live status polling are unsupported; exclude from batch status checks with 0 requests.
			continue
		default:
			continue
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
// A Douyin broadcast it finds for an account that records automatically
// starts recording.
func (m *Manager) CheckAccountLiveStatus(ctx context.Context, siteID site.ID, uid, webRIDHint string, force bool) (RoomState, error) {
	return m.checkAccountLiveStatus(ctx, siteID, uid, webRIDHint, force, true)
}

// checkAccountLiveStatus is CheckAccountLiveStatus; autoStart=false only reads
// the status (used by StartDouyinRecording so reading room metadata never
// re-enters recording start).
func (m *Manager) checkAccountLiveStatus(ctx context.Context, siteID site.ID, uid, webRIDHint string, force, autoStart bool) (RoomState, error) {
	switch siteID {
	case site.Bilibili:
		normUID, err := site.NormalizeUID(site.Bilibili, uid)
		if err != nil {
			return RoomState{}, &site.Error{Site: site.Bilibili, Kind: site.ErrKindBadRequest, Message: "无效的哔哩哔哩 UID"}
		}
		mid, _ := strconv.ParseInt(normUID, 10, 64)
		if _, err := m.refreshAccounts(ctx, []int64{mid}, true, force, autoStart); err != nil {
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

		now := m.now()
		subAcc := m.findDouyinSubAccount(normUID)

		m.mu.Lock()
		if !force {
			if cached, hit := m.douyinStatusCache[normUID]; hit && now.Sub(cached.fetchedAt) < statusCacheTTL {
				hydrated, okCur := m.cachedDouyinStateLocked(normUID)
				if !okCur {
					hydrated = m.hydrateDouyinStateLocked(cached.state)
				}
				liveSt := cached.status
				m.mu.Unlock()
				if autoStart && m.douyinAccountAutoRecords(normUID) {
					if m.autoStartDouyin(ctx, liveSt, hydrated) {
						if rec, ok := m.CachedAccountState(site.Douyin, normUID); ok {
							hydrated = rec
						}
					}
				}
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
		st := m.applyDouyinLiveStatus(liveSt, effectiveWebRID, subAcc, now)
		if autoStart && m.douyinAccountAutoRecords(normUID) {
			// The check counts as the account's live check, and a broadcast
			// it finds is recorded as a poll round would.
			m.mu.Lock()
			m.douyinLastAutoPoll[normUID] = now
			m.mu.Unlock()
			if m.autoStartDouyin(ctx, liveSt, st) {
				if rec, ok := m.CachedAccountState(site.Douyin, normUID); ok {
					st = rec
				}
			}
		}
		return st, nil

	case site.Weibo:
		return RoomState{Site: string(site.Weibo)}, errWeiboLiveUnsupported()

	case site.X:
		return RoomState{Site: string(site.X)}, errXLiveUnsupported()

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
	return m.applyDouyinLiveStatus(liveSt, target, subAcc, m.now()), nil
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
		if rawCover := strings.TrimSpace(liveSt.Cover); rawCover != "" {
			m.douyinRawCovers[secUID] = rawCover
		}
	}

	liveInt, statusText := mapSiteLiveStateDisplay(liveSt.Status)
	var parsedRoomID int64
	if roomIDStr != "" {
		parsedRoomID, _ = strconv.ParseInt(roomIDStr, 10, 64)
	}

	stateKey := douyinStateKey(roomIDStr, secUID, newWebRID)
	existing, hasDirect := m.douyinStates[stateKey]
	if (!hasDirect || (existing.RecordItemDir == "" && len(existing.Incidents) == 0 && existing.StopReason == "" && existing.LastError == "")) && secUID != "" {
		if prev, ok := m.findPrevDouyinStateLocked(secUID); ok {
			if existing.RecordItemDir == "" {
				existing.RecordItemDir = prev.RecordItemDir
			}
			if len(existing.Incidents) == 0 {
				existing.Incidents = prev.Incidents
			}
			if existing.StopReason == "" {
				existing.StopReason = prev.StopReason
			}
			if existing.LastError == "" {
				existing.LastError = prev.LastError
			}
			if existing.Quality == 0 {
				existing.Quality = prev.Quality
			}
			if existing.QualityName == "" {
				existing.QualityName = prev.QualityName
			}
			if existing.Codec == "" {
				existing.Codec = prev.Codec
			}
		}
	}
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
		Incidents:   cloneIncidents(existing.Incidents),
		// The last session's folder stays with its incidents after it ends,
		// so Tasks › 录制异常 shows them on that session only.
		RecordItemDir: existing.RecordItemDir,
		StopReason:    existing.StopReason,
		LastError:     existing.LastError,
		UpdatedAt:     now.UTC().Format(time.RFC3339),
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

func (m *Manager) isDouyinAccountRecordingLocked(uid string) bool {
	if uid == "" {
		return false
	}
	if rid := m.douyinRoomHints[uid]; rid != "" {
		if _, ok := m.douyinSessions[rid]; ok {
			return true
		}
	}
	for _, sess := range m.douyinSessions {
		if sess != nil && sess.accountUID == uid {
			return true
		}
	}
	return false
}

func (m *Manager) hydrateDouyinStateLocked(st RoomState) RoomState {
	if st.Site == "" {
		st.Site = string(site.Douyin)
	}
	if sess := m.douyinSessions[st.RoomIDStr]; sess != nil {
		st.Recording = true
		st.RecordSession = sess.sessionID
		st.RecordDurationSec = m.nowLocked().Sub(sess.startedAt).Seconds()
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
		hydrateSessionPreviewFieldsLocked(&st, sess)
	} else {
		st.Recording = false
		hydrateSessionPreviewFieldsLocked(&st, nil)
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

// douyinAccountAutoRecords tells whether a subscription records the
// broadcasts of a Douyin account.
func (m *Manager) douyinAccountAutoRecords(secUID string) bool {
	if m.subs == nil || secUID == "" {
		return false
	}
	list, err := m.subs.List()
	if err != nil {
		return false
	}
	for _, s := range list {
		if !s.Rules.AutoRecord && !s.LiveAutoRecord {
			continue
		}
		for _, acc := range s.Accounts {
			if acc.Site != string(site.Douyin) {
				continue
			}
			if norm, err := site.NormalizeUID(site.Douyin, acc.UID); err == nil && norm == secUID {
				return true
			}
		}
	}
	return false
}

// douyinShouldAutoStartLocked tells whether a broadcast a check found should
// start recording: it is live and neither recorded already nor stopped by
// hand. The caller holds m.mu.
func (m *Manager) douyinShouldAutoStartLocked(liveSt site.LiveStatus) bool {
	if liveSt.Status != site.LiveStateLive || liveSt.RoomID == "" || m.stopping {
		return false
	}
	_, running := m.douyinSessions[liveSt.RoomID]
	return !running && !m.douyinStoppedRooms[liveSt.RoomID]
}

// autoStartDouyin starts recording a broadcast found live outside a poll
// round (an account's status refreshed by hand or by an update check), for
// an account whose subscription records broadcasts. It reports whether a
// recording started.
func (m *Manager) autoStartDouyin(ctx context.Context, liveSt site.LiveStatus, st RoomState) bool {
	m.mu.Lock()
	if liveSt.Status != site.LiveStateLive && liveSt.RoomID != "" {
		delete(m.douyinStoppedRooms, liveSt.RoomID)
	}
	start := m.douyinShouldAutoStartLocked(liveSt)
	m.mu.Unlock()
	if !start {
		return false
	}
	if _, err := m.StartDouyinRecording(ctx, liveSt.AccountUID, firstNonEmpty(st.WebRID, liveSt.RoomID)); err != nil {
		m.mu.Lock()
		for k, rs := range m.douyinStates {
			if rs.AccountUID == liveSt.AccountUID {
				rs.LastError = sanitizeLiveError(err)
				m.douyinStates[k] = rs
			}
		}
		m.mu.Unlock()
		return false
	}
	return true
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
