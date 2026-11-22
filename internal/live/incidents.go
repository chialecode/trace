package live

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/chialecode/trace/internal/library"
)

const (
	incidentCoalesceWindow     = time.Minute
	slowSpeedThresholdDuration = 30 * time.Second
)

type slowDetector struct {
	slowSince       time.Time
	lastSlow        time.Time
	activeIdx       int
	baseDurationSec float64
}

func newSlowDetector() slowDetector {
	return slowDetector{activeIdx: -1}
}

// SetNowFunc overrides the wall-clock source used by Manager for status caching and incident timing.
func (m *Manager) SetNowFunc(fn func() time.Time) {
	if fn == nil {
		return
	}
	m.mu.Lock()
	m.nowFunc = fn
	m.mu.Unlock()
}

func (m *Manager) now() time.Time {
	m.mu.Lock()
	fn := m.nowFunc
	m.mu.Unlock()
	if fn == nil {
		return time.Now()
	}
	return fn()
}

func (m *Manager) nowLocked() time.Time {
	if m.nowFunc == nil {
		return time.Now()
	}
	return m.nowFunc()
}

func roundDurationSec(sec float64) float64 {
	if sec <= 0 || math.IsNaN(sec) || math.IsInf(sec, 0) {
		return 0
	}
	return math.Round(sec*10) / 10
}

func cloneIncidents(src []library.LiveIncident) []library.LiveIncident {
	if len(src) == 0 {
		return nil
	}
	out := make([]library.LiveIncident, len(src))
	copy(out, src)
	return out
}

// dropResolvedConvertFailed removes the "convert failed" records of a part
// that a later recovery converted, so the session no longer reports it.
func dropResolvedConvertFailed(list []library.LiveIncident, part string) []library.LiveIncident {
	var keep []library.LiveIncident
	for _, inc := range list {
		if inc.Kind == library.LiveIncidentConvertFailed && inc.Part == part {
			continue
		}
		keep = append(keep, inc)
	}
	return keep
}

func hasConvertFailedIncident(list []library.LiveIncident, part string) bool {
	partBase := filepath.Base(strings.TrimSpace(part))
	for _, inc := range list {
		if inc.Kind == library.LiveIncidentConvertFailed && inc.Part == partBase {
			return true
		}
	}
	return false
}

func normalizeSingleIncident(inc library.LiveIncident) (library.LiveIncident, bool) {
	inc.Kind = strings.TrimSpace(inc.Kind)
	if inc.Kind == "" {
		return library.LiveIncident{}, false
	}
	inc.At = strings.TrimSpace(inc.At)
	inc.EndedAt = strings.TrimSpace(inc.EndedAt)
	if inc.EndedAt == "" {
		inc.EndedAt = inc.At
	}
	inc.DurationSec = roundDurationSec(inc.DurationSec)
	if inc.LostSegments < 0 {
		inc.LostSegments = 0
	}
	if inc.Count < 1 {
		inc.Count = 1
	}
	if inc.Part != "" {
		inc.Part = filepath.Base(strings.TrimSpace(inc.Part))
		if idx := strings.IndexAny(inc.Part, "?#"); idx >= 0 {
			inc.Part = inc.Part[:idx]
		}
		if inc.Part == "." || inc.Part == ".." || strings.Contains(inc.Part, "://") {
			inc.Part = ""
		}
	}
	inc.Reason = library.SanitizeLiveIncidentText(inc.Reason)
	return inc, true
}

// appendCoalescedIncident merges adjacent incidents of the same Kind within 1 minute
// and caps the per-session list at library.MaxLiveIncidentsPerSession (200).
func appendCoalescedIncident(list []library.LiveIncident, raw library.LiveIncident) ([]library.LiveIncident, int, float64) {
	inc, ok := normalizeSingleIncident(raw)
	if !ok {
		return list, -1, 0
	}
	incAt, errAt := time.Parse(time.RFC3339, inc.At)

	for i := len(list) - 1; i >= 0; i-- {
		if list[i].Kind != inc.Kind {
			continue
		}
		prev := &list[i]
		refStr := prev.EndedAt
		if refStr == "" {
			refStr = prev.At
		}
		refAt, errRef := time.Parse(time.RFC3339, refStr)
		if errAt == nil && errRef == nil {
			gap := incAt.Sub(refAt)
			if gap >= -time.Second && gap <= incidentCoalesceWindow {
				baseDur := prev.DurationSec
				if prev.Count < 1 {
					prev.Count = 1
				}
				prev.Count += inc.Count
				prev.DurationSec = roundDurationSec(prev.DurationSec + inc.DurationSec)
				prev.LostSegments += inc.LostSegments
				if inc.EndedAt != "" {
					if endAt, errEnd := time.Parse(time.RFC3339, inc.EndedAt); errEnd != nil || !endAt.Before(refAt) {
						prev.EndedAt = inc.EndedAt
					}
				}
				if inc.Part != "" {
					prev.Part = inc.Part
				}
				if inc.Kind == library.LiveIncidentLostSegments && prev.LostSegments > 0 {
					prev.Reason = fmt.Sprintf("HLS 媒体分片序号跳号，累计丢失 %d 段", prev.LostSegments)
				} else if prev.Reason == "" && inc.Reason != "" {
					prev.Reason = inc.Reason
				}
				return list, i, baseDur
			}
		}
		break
	}

	if len(list) >= library.MaxLiveIncidentsPerSession {
		return list, -1, 0
	}
	list = append(list, inc)
	return list, len(list) - 1, 0
}

func (m *Manager) initSessionIncidents(sess *recordSession) {
	if sess == nil {
		return
	}
	sess.slow = newSlowDetector()
	if sess.itemMeta.Live != nil && len(sess.itemMeta.Live.Incidents) > 0 {
		sess.incidents = library.SanitizeLiveIncidents(sess.itemMeta.Live.Incidents)
		sess.itemMeta.Live.Incidents = cloneIncidents(sess.incidents)
	}
}

func (m *Manager) recordSessionIncident(sess *recordSession, inc library.LiveIncident, notifyState bool) {
	if sess == nil {
		return
	}
	var notify *RoomState
	m.mu.Lock()
	if inc.At == "" {
		inc.At = m.nowLocked().UTC().Format(time.RFC3339)
	}
	sess.incidents, _, _ = appendCoalescedIncident(sess.incidents, inc)
	if st, ok := m.syncSessionRoomStateLocked(sess); ok && notifyState {
		cp := st
		notify = &cp
	}
	if sess.itemMeta.Live != nil {
		sess.itemMeta.Live.Incidents = cloneIncidents(sess.incidents)
	}
	m.mu.Unlock()

	if notify != nil && m.onState != nil {
		leave := sess.enterExternal()
		m.onState(*notify)
		leave()
	}
}

func (m *Manager) markSessionDisconnected(sess *recordSession, at time.Time) {
	if sess == nil {
		return
	}
	m.mu.Lock()
	if sess.pendingReconnectAt.IsZero() {
		sess.pendingReconnectAt = at
		sess.pendingReconnectHadMedia = sess.wroteMedia || len(sess.segments) > 0
	}
	m.closeSessionSlowLocked(sess, at)
	m.mu.Unlock()
}

func (m *Manager) markSessionReconnected(sess *recordSession, recoveredAt time.Time, isFLV bool, partBase string) {
	if sess == nil {
		return
	}
	var notify *RoomState
	m.mu.Lock()
	if sess.pendingReconnectAt.IsZero() {
		m.mu.Unlock()
		return
	}
	discAt := sess.pendingReconnectAt
	hadMedia := sess.pendingReconnectHadMedia
	sess.pendingReconnectAt = time.Time{}
	sess.pendingReconnectHadMedia = false

	gapSec := recoveredAt.Sub(discAt).Seconds()
	if gapSec < 0 {
		gapSec = 0
	}
	atStr := discAt.UTC().Format(time.RFC3339)
	endStr := recoveredAt.UTC().Format(time.RFC3339)

	sess.incidents, _, _ = appendCoalescedIncident(sess.incidents, library.LiveIncident{
		Kind:        library.LiveIncidentReconnect,
		At:          atStr,
		EndedAt:     endStr,
		DurationSec: gapSec,
		Count:       1,
		Part:        partBase,
		Reason:      "断流后已重新取流",
	})
	if isFLV && hadMedia && gapSec > 0 {
		sess.incidents, _, _ = appendCoalescedIncident(sess.incidents, library.LiveIncident{
			Kind:        library.LiveIncidentLostSegments,
			At:          atStr,
			EndedAt:     endStr,
			DurationSec: gapSec,
			Count:       1,
			Part:        partBase,
			Reason:      "断流重连期间出现直播空档",
		})
	}
	if sess.itemMeta.Live != nil {
		sess.itemMeta.Live.Incidents = cloneIncidents(sess.incidents)
	}
	if st, ok := m.syncSessionRoomStateLocked(sess); ok {
		cp := st
		notify = &cp
	}
	m.mu.Unlock()

	if notify != nil && m.onState != nil {
		leave := sess.enterExternal()
		m.onState(*notify)
		leave()
	}
}

func (m *Manager) setSessionNominalBitrateBps(sess *recordSession, bitrateBps int64) {
	if sess == nil || bitrateBps <= 0 {
		return
	}
	m.mu.Lock()
	sess.nominalByteRate = float64(bitrateBps) / 8.0
	m.mu.Unlock()
}

func (m *Manager) observeTransferSpeed(
	sess *recordSession,
	spanStart time.Time,
	spanEnd time.Time,
	bytesTransferred int64,
	mediaDurationSec float64,
	partBase string,
) {
	if sess == nil || bytesTransferred <= 0 {
		return
	}
	wallSec := spanEnd.Sub(spanStart).Seconds()
	if wallSec <= 0 {
		return
	}
	actualByteRate := float64(bytesTransferred) / wallSec

	var notify *RoomState
	m.mu.Lock()
	isSlow := false
	if mediaDurationSec > 0 {
		mediaByteRate := float64(bytesTransferred) / mediaDurationSec
		if actualByteRate < mediaByteRate*0.95 {
			isSlow = true
		}
	} else if sess.nominalByteRate > 0 && wallSec >= 0.2 {
		if actualByteRate < sess.nominalByteRate*0.90 {
			isSlow = true
		}
	}

	if isSlow {
		if sess.slow.slowSince.IsZero() {
			sess.slow.slowSince = spanStart
		}
		sess.slow.lastSlow = spanEnd
		dur := spanEnd.Sub(sess.slow.slowSince)
		if dur >= slowSpeedThresholdDuration {
			opened := false
			if sess.slow.activeIdx >= 0 && sess.slow.activeIdx < len(sess.incidents) && sess.incidents[sess.slow.activeIdx].Kind == library.LiveIncidentSlow {
				idx := sess.slow.activeIdx
				sess.incidents[idx].DurationSec = roundDurationSec(sess.slow.baseDurationSec + dur.Seconds())
				sess.incidents[idx].EndedAt = spanEnd.UTC().Format(time.RFC3339)
				if partBase != "" {
					sess.incidents[idx].Part = filepath.Base(partBase)
				}
			} else {
				var idx int
				var baseDur float64
				sess.incidents, idx, baseDur = appendCoalescedIncident(sess.incidents, library.LiveIncident{
					Kind:        library.LiveIncidentSlow,
					At:          sess.slow.slowSince.UTC().Format(time.RFC3339),
					EndedAt:     spanEnd.UTC().Format(time.RFC3339),
					DurationSec: dur.Seconds(),
					Count:       1,
					Part:        partBase,
					Reason:      "连续 30 秒以上下载速度低于直播流码率",
				})
				sess.slow.activeIdx = idx
				sess.slow.baseDurationSec = baseDur
				opened = idx >= 0
			}
			if sess.itemMeta.Live != nil {
				sess.itemMeta.Live.Incidents = cloneIncidents(sess.incidents)
			}
			if st, ok := m.syncSessionRoomStateLocked(sess); ok && opened {
				cp := st
				notify = &cp
			}
		}
	} else {
		m.closeSessionSlowLocked(sess, spanStart)
	}
	m.mu.Unlock()

	if notify != nil && m.onState != nil {
		leave := sess.enterExternal()
		m.onState(*notify)
		leave()
	}
}

func (m *Manager) closeSessionSlowIncident(sess *recordSession, now time.Time) {
	if sess == nil {
		return
	}
	m.mu.Lock()
	m.closeSessionSlowLocked(sess, now)
	m.mu.Unlock()
}

func (m *Manager) closeSessionSlowLocked(sess *recordSession, now time.Time) {
	if sess == nil {
		return
	}
	if sess.slow.activeIdx >= 0 && sess.slow.activeIdx < len(sess.incidents) && sess.incidents[sess.slow.activeIdx].Kind == library.LiveIncidentSlow {
		endAt := sess.slow.lastSlow
		if now.After(endAt) {
			endAt = now
		}
		dur := endAt.Sub(sess.slow.slowSince).Seconds()
		if dur > 0 {
			sess.incidents[sess.slow.activeIdx].DurationSec = roundDurationSec(sess.slow.baseDurationSec + dur)
			sess.incidents[sess.slow.activeIdx].EndedAt = endAt.UTC().Format(time.RFC3339)
		}
		if sess.itemMeta.Live != nil {
			sess.itemMeta.Live.Incidents = cloneIncidents(sess.incidents)
		}
	}
	sess.slow = newSlowDetector()
}

func isDiskFullError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ENOSPC) || isPlatformDiskFullError(err) {
		return true
	}
	msg := err.Error()
	if strings.Contains(msg, "磁盘空间不足") || strings.Contains(msg, "磁盘可用空间不足") {
		return true
	}
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "no space left") ||
		strings.Contains(lower, "disk full") ||
		strings.Contains(lower, "not enough space on the disk") ||
		strings.Contains(lower, "there is not enough space")
}

// ClearIdleIncidents forgets the in-memory incidents of rooms that are not
// recording and match siteID with roomID or uid (Tasks › 录制异常 "删除").
// A room that is recording keeps its list. It returns whether anything changed.
func (m *Manager) ClearIdleIncidents(siteID, roomID, uid string) bool {
	roomID, uid = strings.TrimSpace(roomID), strings.TrimSpace(uid)
	if roomID == "" && uid == "" {
		return false
	}
	matches := func(st RoomState) bool {
		if st.Recording || len(st.Incidents) == 0 {
			return false
		}
		rooms := []string{st.RoomIDStr, st.WebRID}
		if st.RoomID > 0 {
			rooms = append(rooms, strconv.FormatInt(st.RoomID, 10))
		}
		uids := []string{st.AccountUID}
		if st.UID > 0 {
			uids = append(uids, strconv.FormatInt(st.UID, 10))
		}
		for _, r := range rooms {
			if roomID != "" && r == roomID {
				return true
			}
		}
		for _, u := range uids {
			if uid != "" && u == uid {
				return true
			}
		}
		return false
	}
	var changed []RoomState
	m.mu.Lock()
	switch strings.ToLower(strings.TrimSpace(siteID)) {
	case "douyin":
		for k, st := range m.douyinStates {
			if matches(st) {
				st.Incidents = nil
				m.douyinStates[k] = st
				changed = append(changed, st)
			}
		}
	case "", "bilibili":
		for k, st := range m.states {
			if matches(st) {
				st.Incidents = nil
				m.states[k] = st
				changed = append(changed, st)
			}
		}
	default:
		// Weibo and unknown sites have no tracked live rooms.
	}
	onState := m.onState
	m.mu.Unlock()
	if onState != nil {
		for _, st := range changed {
			onState(st)
		}
	}
	return len(changed) > 0
}
