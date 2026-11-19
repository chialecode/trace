package live

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/subscription"
)

// Status queries live status for the given UIDs using a 60-second in-memory cache and <=50 UID batching (live.md §1).
func (m *Manager) Status(ctx context.Context, uids []int64) (map[int64]bilibili.LiveUserStatus, error) {
	if err := m.client.RequireAuth("live:status"); err != nil {
		return nil, err
	}
	now := m.now()
	result := make(map[int64]bilibili.LiveUserStatus, len(uids))
	var uncached []int64
	seen := make(map[int64]bool, len(uids))

	m.mu.Lock()
	for _, uid := range uids {
		if uid <= 0 || seen[uid] {
			continue
		}
		seen[uid] = true
		if entry, ok := m.statusCache[uid]; ok && now.Sub(entry.fetchedAt) < statusCacheTTL {
			result[uid] = entry.status
		} else {
			uncached = append(uncached, uid)
		}
	}
	m.mu.Unlock()

	if len(uncached) > 0 {
		fetched, err := m.client.LiveStatusByUIDs(ctx, uncached)
		if err != nil {
			return nil, err
		}
		m.mu.Lock()
		for _, uid := range uncached {
			st := fetched[uid]
			if st.UID == 0 {
				st.UID = uid
			}
			m.statusCache[uid] = cachedStatusEntry{
				status:    st,
				fetchedAt: now,
			}
			result[uid] = st
		}
		m.mu.Unlock()
	}
	return result, nil
}

// PollNow queries live status for all subscribed Bilibili creators (in <=50 UID batches),
// detects 0/2 -> 1 transitions (ignoring 2=carousel), and emits start notifications without triggering auto-recording.
func (m *Manager) PollNow(ctx context.Context) ([]RoomState, error) {
	return m.refreshAccounts(ctx, nil, false, true, false)
}

// RefreshForAccounts reads the live status of the given Bilibili accounts (cached for
// 60 seconds) and returns only the room states matching the requested UIDs. Like a poll
// round it starts recording a broadcast found live when the account's subscription
// records broadcasts, so a status refresh on the account page or by an update check
// does not leave it unrecorded until the next round.
func (m *Manager) RefreshForAccounts(ctx context.Context, uids []int64) ([]RoomState, error) {
	return m.refreshAccounts(ctx, uids, true, false, true)
}

func collectBilibiliRefreshTargets(subList []subscription.Subscription, selected []int64, filterToSelected bool) ([]int64, map[int64]subscription.Subscription, map[int64]bool) {
	subByUID := make(map[int64]subscription.Subscription, len(subList))
	allowed := make(map[int64]bool, len(selected))
	for _, uid := range selected {
		if uid > 0 {
			allowed[uid] = true
		}
	}
	var uids []int64
	mergeSub := func(uid int64, s subscription.Subscription) {
		if uid <= 0 || (filterToSelected && !allowed[uid]) {
			return
		}
		if existing, exists := subByUID[uid]; exists {
			existing.LiveAutoRecord = existing.LiveAutoRecord || s.LiveAutoRecord
			existing.Rules.AutoRecord = existing.Rules.AutoRecord || s.Rules.AutoRecord
			existing.LiveNotify = existing.LiveNotify || s.LiveNotify
			existing.Uname = firstNonEmpty(existing.Uname, s.Uname)
			existing.Face = firstNonEmpty(existing.Face, s.Face)
			if existing.RoomID <= 0 && s.RoomID > 0 {
				existing.RoomID = s.RoomID
			}
			subByUID[uid] = existing
			return
		}
		subByUID[uid] = s
		uids = append(uids, uid)
	}

	for _, s := range subList {
		for _, acc := range s.Accounts {
			if acc.Site != "" && acc.Site != string(site.Bilibili) {
				continue
			}
			if uid, pErr := strconv.ParseInt(acc.UID, 10, 64); pErr == nil && uid > 0 {
				mergeSub(uid, s)
			}
		}
		if s.Mid > 0 {
			mergeSub(s.Mid, s)
		}
	}
	// A user page can show a creator before they are subscribed. Explicit UI
	// lookups may query these accounts, without enabling rules or notifications.
	for _, uid := range selected {
		if uid > 0 {
			if _, exists := subByUID[uid]; !exists {
				subByUID[uid] = subscription.Subscription{Mid: uid}
				uids = append(uids, uid)
			}
		}
	}
	return uids, subByUID, allowed
}

func (m *Manager) refreshAccounts(ctx context.Context, selected []int64, filterToSelected, force, autoRecord bool) ([]RoomState, error) {
	if err := m.client.RequireAuth("live:poll"); err != nil {
		return nil, err
	}
	var subList []subscription.Subscription
	var err error
	if m.subs != nil {
		subList, err = m.subs.List()
		if err != nil {
			return nil, err
		}
	}

	uids, subByUID, allowed := collectBilibiliRefreshTargets(subList, selected, filterToSelected)
	if len(uids) == 0 {
		if filterToSelected {
			return nil, nil
		}
		return m.ListRooms(), nil
	}

	var statusMap map[int64]bilibili.LiveUserStatus
	if force {
		statusMap, err = m.client.LiveStatusByUIDs(ctx, uids)
	} else {
		statusMap, err = m.Status(ctx, uids)
	}
	if err != nil {
		return nil, err
	}

	now := m.now().UTC()
	nowStr := now.Format(time.RFC3339)
	var toAutoRecord []int64
	var notifications []Notification

	m.mu.Lock()
	notifyGlobal := m.liveSettings.NotifyOnStart
	for _, uid := range uids {
		sub := subByUID[uid]
		st, ok := statusMap[uid]
		if !ok {
			st = bilibili.LiveUserStatus{
				UID:        uid,
				RoomID:     sub.RoomID,
				Uname:      sub.Uname,
				Face:       sub.Face,
				LiveStatus: 0,
			}
		}
		if force {
			m.statusCache[uid] = cachedStatusEntry{status: st, fetchedAt: now}
		}

		prev, hadPrev := m.prevLive[uid]
		m.prevLive[uid] = st.LiveStatus

		if st.LiveStatus != 1 && st.RoomID > 0 {
			delete(m.stoppedBroadcasts, st.RoomID)
		}

		// Transition 0/2 -> 1 triggers notification and auto-record (LiveStatus==2 carousel never triggers).
		if st.LiveStatus == 1 && (!hadPrev || prev != 1) {
			if notifyGlobal && sub.LiveNotify {
				notifications = append(notifications, Notification{
					Site:       string(site.Bilibili),
					AccountUID: strconv.FormatInt(uid, 10),
					UID:        uid,
					RoomID:     st.RoomID,
					RoomIDStr:  strconv.FormatInt(st.RoomID, 10),
					Uname:      firstNonEmpty(st.Uname, sub.Uname),
					Title:      st.Title,
					StartedAt:  nowStr,
				})
			}
		}

		autoRecEnabled := sub.LiveAutoRecord || sub.Rules.AutoRecord
		if st.LiveStatus == 1 && autoRecEnabled && st.RoomID > 0 {
			if stopped, ok := m.stoppedBroadcasts[st.RoomID]; ok && st.LiveTime > 0 && stopped != st.LiveTime {
				delete(m.stoppedBroadcasts, st.RoomID)
			}
			_, suppressed := m.stoppedBroadcasts[st.RoomID]
			if _, already := m.sessions[st.RoomID]; autoRecord && !m.stopping && !already && !suppressed {
				toAutoRecord = append(toAutoRecord, st.RoomID)
			}
		}

		key := st.RoomID
		if key <= 0 {
			key = -uid
		}
		existing := m.states[key]
		if existing.RecordItemDir == "" && len(existing.Incidents) == 0 && existing.StopReason == "" && existing.LastError == "" {
			for oldKey, old := range m.states {
				if oldKey != key && old.UID == uid {
					existing = old
					break
				}
			}
		}
		recSess := m.sessions[st.RoomID]
		roomState := RoomState{
			Site:        string(site.Bilibili),
			AccountUID:  strconv.FormatInt(uid, 10),
			UID:         uid,
			RoomID:      st.RoomID,
			ShortID:     st.ShortID,
			Uname:       firstNonEmpty(st.Uname, sub.Uname),
			Face:        firstNonEmpty(st.Face, sub.Face),
			Title:       st.Title,
			Cover:       st.Cover,
			AreaName:    st.AreaName,
			LiveStatus:  st.LiveStatus,
			LiveSince:   st.LiveTime,
			StatusText:  LiveStatusText(st.LiveStatus),
			Quality:     existing.Quality,
			QualityName: existing.QualityName,
			Codec:       existing.Codec,
			Recording:   recSess != nil,
			Incidents:   cloneIncidents(existing.Incidents),
			// The last session's folder stays with its incidents after it ends,
			// so Tasks › 录制异常 shows them on that session only.
			RecordItemDir: existing.RecordItemDir,
			StopReason:    existing.StopReason,
			LastError:     existing.LastError,
			UpdatedAt:     nowStr,
		}
		m.normalizeBilibiliRoomStateLocked(&roomState)
		if recSess != nil {
			m.hydrateBilibiliStateFromSessionLocked(&roomState, recSess)
		} else {
			hydrateSessionPreviewFieldsLocked(&roomState, nil)
		}
		m.states[key] = roomState
		for oldKey, state := range m.states {
			if oldKey != key && state.UID == uid {
				delete(m.states, oldKey)
			}
		}
	}
	m.mu.Unlock()

	for _, n := range notifications {
		if m.onNotify != nil {
			m.onNotify(n)
		}
	}
	for _, roomID := range toAutoRecord {
		if _, rErr := m.StartRecording(ctx, roomID); rErr != nil {
			m.mu.Lock()
			if st, ok := m.states[roomID]; ok {
				st.LastError = sanitizeLiveError(rErr)
				m.states[roomID] = st
			}
			m.mu.Unlock()
			if isAutoStartBatchStopError(rErr) {
				if site.IsRoundStop(rErr) && m.client != nil {
					m.client.ReportAutomaticError(ctx, rErr)
				}
				if site.IsCircuitBreak(rErr) {
					m.recordSiteAutoStop(site.Bilibili, site.KindOf(rErr))
				}
				break
			}
		}
	}

	rooms := m.ListRooms()
	if !filterToSelected {
		return rooms, nil
	}
	filtered := make([]RoomState, 0, len(allowed))
	for _, r := range rooms {
		if r.Site == string(site.Bilibili) && allowed[r.UID] {
			filtered = append(filtered, r)
		}
	}
	return filtered, nil
}

// OpenRoom resolves and inspects a single live room by roomID or shortID.
func (m *Manager) OpenRoom(ctx context.Context, roomOrShortID int64) (RoomState, error) {
	info, err := m.client.RoomInit(ctx, roomOrShortID)
	if err != nil {
		return RoomState{}, err
	}

	var userSt bilibili.LiveUserStatus
	if info.UID > 0 {
		stMap, sErr := m.Status(ctx, []int64{info.UID})
		if sErr != nil {
			return RoomState{}, sErr
		}
		userSt = stMap[info.UID]
	}

	now := m.now().UTC()
	nowStr := now.Format(time.RFC3339)
	m.mu.Lock()
	existing := m.states[info.RoomID]
	recSess := m.sessions[info.RoomID]
	st := RoomState{
		Site:        string(site.Bilibili),
		UID:         info.UID,
		RoomID:      info.RoomID,
		ShortID:     info.ShortID,
		Uname:       firstNonEmpty(userSt.Uname, fmt.Sprintf("UID %d", info.UID)),
		Face:        userSt.Face,
		Title:       firstNonEmpty(userSt.Title, fmt.Sprintf("直播间 %d", info.RoomID)),
		Cover:       userSt.Cover,
		AreaName:    userSt.AreaName,
		LiveStatus:  info.LiveStatus,
		LiveSince:   max(info.LiveTime, userSt.LiveTime),
		StatusText:  LiveStatusText(info.LiveStatus),
		Quality:     existing.Quality,
		QualityName: existing.QualityName,
		Codec:       existing.Codec,
		Recording:   recSess != nil,
		Incidents:   cloneIncidents(existing.Incidents),
		// The last session's folder stays with its incidents after it ends,
		// so Tasks › 录制异常 shows them on that session only.
		RecordItemDir: existing.RecordItemDir,
		StopReason:    existing.StopReason,
		LastError:     existing.LastError,
		UpdatedAt:     nowStr,
	}
	m.normalizeBilibiliRoomStateLocked(&st)
	if recSess != nil {
		m.hydrateBilibiliStateFromSessionLocked(&st, recSess)
	} else {
		hydrateSessionPreviewFieldsLocked(&st, nil)
	}
	m.states[info.RoomID] = st
	m.mu.Unlock()
	return st, nil
}

func (m *Manager) normalizeBilibiliRoomStateLocked(st *RoomState) {
	if st == nil {
		return
	}
	if st.Site == "" {
		st.Site = string(site.Bilibili)
	}
	if st.AccountUID == "" && st.UID > 0 {
		st.AccountUID = strconv.FormatInt(st.UID, 10)
	}
	if st.RoomIDStr == "" && st.RoomID > 0 {
		st.RoomIDStr = strconv.FormatInt(st.RoomID, 10)
	}
	if st.RoomKey == "" {
		if st.RoomIDStr != "" {
			st.RoomKey = "bilibili:live:" + st.RoomIDStr
		} else if st.AccountUID != "" {
			st.RoomKey = "bilibili:user:" + st.AccountUID
		}
	}
	if st.QualityName == "" && st.Quality > 0 {
		st.QualityName = bilibili.LiveQualityFallbackName(st.Quality)
	}
}

func (m *Manager) hydrateBilibiliStateFromSessionLocked(st *RoomState, sess *recordSession) {
	if st == nil || sess == nil {
		return
	}
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
	hydrateSessionPreviewFieldsLocked(st, sess)
}
