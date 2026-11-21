package live

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/subscription"
)

// douyinLiveChecksPerRoundLocked is how many Douyin live requests one poll
// round may make: as many as fit in the round at the live check gap.
func (m *Manager) douyinLiveChecksPerRoundLocked() int {
	sec := max(m.liveSettings.PollIntervalSeconds, 60)
	if m.douyinLiveGap <= 0 {
		return math.MaxInt32
	}
	return max(1, int(time.Duration(sec)*time.Second/m.douyinLiveGap))
}

// waitDouyinLiveGap waits the gap before a round's next live check; false
// when ctx ends first.
func (m *Manager) waitDouyinLiveGap(ctx context.Context) bool {
	m.mu.Lock()
	gap, wait := m.douyinLiveGap, m.douyinGapWait
	m.mu.Unlock()
	if gap <= 0 {
		return ctx.Err() == nil
	}
	if wait != nil {
		return wait(ctx, gap)
	}
	t := time.NewTimer(gap)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
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
	targetIdx := make(map[string]int)
	for _, s := range subList {
		if !s.Rules.AutoRecord && !s.LiveAutoRecord {
			continue
		}
		for _, acc := range s.Accounts {
			if acc.Site != string(site.Douyin) {
				continue
			}
			normUID, nErr := site.NormalizeUID(site.Douyin, acc.UID)
			if nErr != nil {
				continue
			}
			acc.UID = normUID
			if idx, exists := targetIdx[normUID]; exists {
				// Multi-subscription OR-merge so LiveNotify/WebRID on later subscriptions aren't shadowed.
				targets[idx].sub.LiveNotify = targets[idx].sub.LiveNotify || s.LiveNotify
				targets[idx].sub.Name = firstNonEmpty(targets[idx].sub.Name, s.Name)
				targets[idx].acc.WebRID = firstNonEmpty(targets[idx].acc.WebRID, acc.WebRID)
				targets[idx].acc.Name = firstNonEmpty(targets[idx].acc.Name, acc.Name)
				targets[idx].acc.Face = firstNonEmpty(targets[idx].acc.Face, acc.Face)
				continue
			}
			targetIdx[normUID] = len(targets)
			targets = append(targets, autoTarget{sub: s, acc: acc})
		}
	}
	if len(targets) == 0 {
		return m.ListRooms(), nil
	}
	// The accounts checked longest ago go first (never checked: first of
	// all), so a round that cannot check them all goes round all of them.
	m.mu.Lock()
	sort.SliceStable(targets, func(i, j int) bool {
		return m.douyinLastAutoPoll[targets[i].acc.UID].Before(m.douyinLastAutoPoll[targets[j].acc.UID])
	})
	requests := m.douyinLiveChecksPerRoundLocked()
	m.mu.Unlock()
	sent := 0 // live requests of this round, spaced by the live check gap

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

		// A round with a single request spends it on an account's status:
		// the recordings notice an ended broadcast themselves.
		if len(knownRooms) > 0 && requests >= 2 {
			requests--
			sent++
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
				now := m.now()
				m.mu.Lock()
				for _, rid := range knownRooms {
					alive, present := aliveMap[rid]
					if present && !alive {
						uid := roomToUID[rid]
						delete(m.douyinRoomHints, uid)
						delete(m.douyinStoppedRooms, rid)
						delete(m.douyinStatusCache, uid)
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
		checkNow := m.now()

		m.mu.Lock()
		lastAt, hadLast := m.douyinLastAutoPoll[uid]
		alreadyRec := m.isDouyinAccountRecordingLocked(uid)
		webRIDHint := firstNonEmpty(m.douyinWebRIDHints[uid], t.acc.WebRID)
		m.mu.Unlock()

		if alreadyRec {
			continue
		}
		if hadLast && minInterval > 0 && checkNow.Sub(lastAt) < minInterval {
			continue
		}
		if requests <= 0 {
			break
		}
		if sent > 0 && !m.waitDouyinLiveGap(ctx) {
			break
		}
		requests--
		sent++

		// Capture timestamp after gap sleep so cache/poll timestamps reflect actual check time.
		now := m.now()
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
		shouldStart := m.douyinShouldAutoStartLocked(liveSt)
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
					st.LastError = sanitizeLiveError(sErr)
					m.douyinStates[k] = st
				}
			}
			m.mu.Unlock()
			if isAutoStartBatchStopError(sErr) {
				if site.IsRoundStop(sErr) {
					if rep, rOK := provider.(LiveErrorReporter); rOK {
						rep.ReportAutomaticError(ctx, sErr)
					}
				}
				if site.IsCircuitBreak(sErr) {
					m.recordSiteAutoStop(site.Douyin, site.KindOf(sErr))
				}
				break
			}
		}
	}
	return m.ListRooms(), nil
}
