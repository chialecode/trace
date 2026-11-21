package live

import (
	"strconv"
	"strings"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
)

const (
	previewReasonWaitingFirstFragment = "等待首个直播分片写入"
	previewReasonSwitchingPart        = "正在切换录制分段"
	previewReasonUnsupported          = "这场录制暂不支持实时预览"
)

// RecordingPreview describes the read-only local live preview state of a tracked or recording room.
// Querying RecordingPreview never performs any network request.
type RecordingPreview struct {
	RecordingKey     string                 `json:"recordingKey"`
	Site             string                 `json:"site"`
	AccountUID       string                 `json:"accountUid,omitempty"`
	RoomID           int64                  `json:"roomId,omitempty"`
	RoomIDStr        string                 `json:"roomIdStr,omitempty"`
	WebRID           string                 `json:"webRid,omitempty"`
	Uname            string                 `json:"uname"`
	Title            string                 `json:"title"`
	Codec            string                 `json:"codec,omitempty"`
	Quality          int                    `json:"quality,omitempty"`
	QualityName      string                 `json:"qualityName,omitempty"`
	Transport        string                 `json:"transport,omitempty"`
	AudioOnly        bool                   `json:"audioOnly,omitempty"`
	Recording        bool                   `json:"recording"`
	PartPath         string                 `json:"partPath,omitempty"`
	PartIndex        int                    `json:"partIndex,omitempty"`
	PartBytes        int64                  `json:"partBytes,omitempty"`
	PartDurationSec  float64                `json:"partDurationSec,omitempty"`
	PartReady        bool                   `json:"partReady,omitempty"`
	PreviewSupported bool                   `json:"previewSupported"`
	PreviewReason    string                 `json:"previewReason,omitempty"`
	ItemDir          string                 `json:"itemDir,omitempty"`
	DurationSec      float64                `json:"durationSec,omitempty"`
	SpeedBps         int64                  `json:"speedBps,omitempty"`
	BytesWritten     int64                  `json:"bytesWritten,omitempty"`
	Segments         int                    `json:"segments,omitempty"`
	Incidents        []library.LiveIncident `json:"incidents,omitempty"`
	StopReason       string                 `json:"stopReason,omitempty"`
	LastError        string                 `json:"lastError,omitempty"`
}

func hydrateSessionPreviewFieldsLocked(st *RoomState, sess *recordSession) {
	if st == nil {
		return
	}
	if sess == nil {
		st.RecordPartPath = ""
		st.RecordPartIndex = 0
		st.RecordPartBytes = 0
		st.RecordPartReady = false
		if st.LiveState == site.LiveStateAudioOnly {
			st.AudioOnly = true
			st.PreviewSupported = false
			st.PreviewReason = previewReasonUnsupported
		} else {
			st.PreviewSupported = false
			st.PreviewReason = ""
		}
		return
	}

	st.Transport = sess.streamTransport
	st.AudioOnly = sess.audioOnly
	st.RecordPartPath = sess.activePartPath
	st.RecordPartIndex = sess.activePartIndex
	st.RecordPartBytes = sess.activePartBytes
	st.RecordPartReady = sess.activePartReady
	st.PreviewSupported = sess.previewSupport
	st.PreviewReason = sess.previewReason
	st.Incidents = cloneIncidents(sess.incidents)
	if st.RecordSpeedBps == 0 && sess.lastSpeedBps > 0 {
		st.RecordSpeedBps = sess.lastSpeedBps
	}
}

func (m *Manager) beginSessionPart(sess *recordSession, partIndex int, partPath string, transport string) {
	if sess == nil {
		return
	}
	var notify *RoomState
	m.mu.Lock()
	sess.streamTransport = transport
	sess.activePartIndex = partIndex
	sess.activePartPath = partPath
	sess.activePartBytes = 0
	sess.activePartDur = 0
	sess.activePartReady = false
	sess.previewSupport = transport == string(site.LiveTransportHLSFMP4) || transport == string(site.LiveTransportHTTPFLV)
	if sess.previewSupport {
		sess.previewReason = previewReasonWaitingFirstFragment
	} else {
		sess.previewReason = previewReasonUnsupported
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

func (m *Manager) commitSessionPart(sess *recordSession, bytesWritten int64, durationSec float64) {
	if sess == nil {
		return
	}
	var notify *RoomState
	m.mu.Lock()
	wasReady := sess.activePartReady
	sess.activePartBytes = bytesWritten
	sess.activePartDur = durationSec
	if bytesWritten > 0 && sess.previewSupport {
		sess.activePartReady = true
		sess.previewReason = ""
	}
	if st, ok := m.syncSessionRoomStateLocked(sess); ok && !wasReady && sess.activePartReady {
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

func (m *Manager) endSessionPart(sess *recordSession) {
	if sess == nil {
		return
	}
	var notify *RoomState
	m.mu.Lock()
	wasActive := sess.activePartPath != ""
	sess.activePartPath = ""
	sess.activePartBytes = 0
	sess.activePartDur = 0
	sess.activePartReady = false
	if sess.stopReason == "" && sess.previewSupport {
		sess.previewReason = previewReasonSwitchingPart
	} else if !sess.previewSupport {
		sess.previewReason = previewReasonUnsupported
	} else {
		sess.previewReason = ""
	}
	if st, ok := m.syncSessionRoomStateLocked(sess); ok && wasActive {
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

func (m *Manager) syncSessionRoomStateLocked(sess *recordSession) (RoomState, bool) {
	if sess == nil {
		return RoomState{}, false
	}
	switch sess.siteID {
	case string(site.Douyin):
		st, ok := m.douyinStates[sess.stateKey]
		if !ok {
			return RoomState{}, false
		}
		st = m.hydrateDouyinStateLocked(st)
		m.douyinStates[sess.stateKey] = st
		return st, true
	case "", string(site.Bilibili):
		st, ok := m.states[sess.roomID]
		if !ok {
			return RoomState{}, false
		}
		m.normalizeBilibiliRoomStateLocked(&st)
		m.hydrateBilibiliStateFromSessionLocked(&st, sess)
		m.states[sess.roomID] = st
		return st, true
	default:
		return RoomState{}, false
	}
}

// RecordingPreview returns a read-only local preview snapshot for a recording or tracked room.
// It accepts canonical room keys ("bilibili:live:<room_id>", "douyin:live:<room_id>"),
// account keys ("bilibili:<mid>", "douyin:<sec_uid>", "bilibili:user:<mid>", "douyin:user:<sec_uid>"),
// session IDs ("live:<session>"), room IDs, UIDs, or Douyin web_rids, and never performs network I/O.
func (m *Manager) RecordingPreview(key string) (RecordingPreview, bool) {
	target := strings.TrimSpace(key)
	if target == "" || strings.HasPrefix(strings.ToLower(target), "weibo:") {
		return RecordingPreview{}, false
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if sess := m.matchBilibiliSessionLocked(target); sess != nil {
		st := m.states[sess.roomID]
		return m.buildPreviewLocked(site.Bilibili, st, sess), true
	}
	if sess := m.matchDouyinSessionLocked(target); sess != nil {
		st := m.douyinStates[sess.stateKey]
		return m.buildPreviewLocked(site.Douyin, st, sess), true
	}
	if st, ok := m.matchBilibiliStateLocked(target); ok {
		return m.buildPreviewLocked(site.Bilibili, st, nil), true
	}
	if st, ok := m.matchDouyinStateLocked(target); ok {
		return m.buildPreviewLocked(site.Douyin, st, nil), true
	}
	return RecordingPreview{}, false
}

func (m *Manager) matchBilibiliSessionLocked(target string) *recordSession {
	trimmed := target
	switch {
	case strings.HasPrefix(target, "bilibili:live:"):
		trimmed = strings.TrimSpace(strings.TrimPrefix(target, "bilibili:live:"))
		if trimmed == "" {
			return nil
		}
	case strings.HasPrefix(target, "bilibili:user:"):
		uid, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(target, "bilibili:user:")), 10, 64)
		if err != nil || uid <= 0 {
			return nil
		}
		for _, sess := range m.sessions {
			if sess != nil && sess.uid == uid {
				return sess
			}
		}
		return nil
	case strings.HasPrefix(target, "bilibili:"):
		rest := strings.TrimSpace(strings.TrimPrefix(target, "bilibili:"))
		if num, err := strconv.ParseInt(rest, 10, 64); err == nil && num > 0 {
			if sess := m.sessions[num]; sess != nil {
				return sess
			}
			for _, sess := range m.sessions {
				if sess != nil && sess.uid == num {
					return sess
				}
			}
		}
		return nil
	case strings.HasPrefix(target, "live:"):
		sid := strings.TrimSpace(strings.TrimPrefix(target, "live:"))
		if sid == "" {
			return nil
		}
		for _, sess := range m.sessions {
			if sess != nil && sess.sessionID != "" && sess.sessionID == sid {
				return sess
			}
		}
		return nil
	case strings.HasPrefix(target, "douyin:"), strings.HasPrefix(strings.ToLower(target), "weibo:"):
		return nil
	default:
		if strings.Contains(target, ":") {
			return nil
		}
	}

	if trimmed == "" {
		return nil
	}
	for _, sess := range m.sessions {
		if sess != nil && sess.sessionID != "" && sess.sessionID == trimmed {
			return sess
		}
	}
	if num, err := strconv.ParseInt(trimmed, 10, 64); err == nil && num > 0 {
		if sess := m.sessions[num]; sess != nil {
			return sess
		}
		for _, sess := range m.sessions {
			if sess != nil && sess.uid == num {
				return sess
			}
		}
	}
	return nil
}

func (m *Manager) matchDouyinSessionLocked(target string) *recordSession {
	if strings.HasPrefix(target, "bilibili:") || strings.HasPrefix(strings.ToLower(target), "weibo:") {
		return nil
	}
	var candidates []string
	hadPrefix := false
	for _, pfx := range []string{"douyin:live:", "douyin:user:", "douyin:web:", "douyin:", "live:"} {
		if strings.HasPrefix(target, pfx) {
			hadPrefix = true
			rest := strings.TrimSpace(strings.TrimPrefix(target, pfx))
			if rest == "" {
				return nil
			}
			candidates = append(candidates, target, rest)
			break
		}
	}
	if !hadPrefix {
		candidates = []string{target}
	}

	for _, c := range candidates {
		if c == "" {
			continue
		}
		if sess := m.douyinSessions[c]; sess != nil {
			return sess
		}
		for _, sess := range m.douyinSessions {
			if sess == nil {
				continue
			}
			if sess.stateKey == c || sess.accountUID == c || (sess.webRID != "" && sess.webRID == c) || sess.roomIDStr == c || sess.sessionID == c {
				return sess
			}
		}
	}
	return nil
}

func (m *Manager) matchBilibiliStateLocked(target string) (RoomState, bool) {
	if strings.HasPrefix(target, "douyin:") || strings.HasPrefix(strings.ToLower(target), "weibo:") {
		return RoomState{}, false
	}
	trimmed := target
	byUIDOnly := false
	hasBiliPrefix := false
	switch {
	case strings.HasPrefix(target, "bilibili:live:"):
		trimmed = strings.TrimSpace(strings.TrimPrefix(target, "bilibili:live:"))
		hasBiliPrefix = true
	case strings.HasPrefix(target, "bilibili:user:"):
		trimmed = strings.TrimSpace(strings.TrimPrefix(target, "bilibili:user:"))
		byUIDOnly = true
		hasBiliPrefix = true
	case strings.HasPrefix(target, "bilibili:"):
		trimmed = strings.TrimSpace(strings.TrimPrefix(target, "bilibili:"))
		hasBiliPrefix = true
	}
	if hasBiliPrefix && trimmed == "" {
		return RoomState{}, false
	}
	num, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || num <= 0 {
		sid := strings.TrimSpace(strings.TrimPrefix(target, "live:"))
		if sid == "" {
			return RoomState{}, false
		}
		for _, st := range m.states {
			if st.RecordSession != "" && st.RecordSession == sid {
				return st, true
			}
		}
		return RoomState{}, false
	}
	if !byUIDOnly {
		if st, ok := m.states[num]; ok {
			return st, true
		}
	}
	for _, st := range m.states {
		if st.UID == num || (!byUIDOnly && st.ShortID == num) {
			return st, true
		}
	}
	return RoomState{}, false
}

func (m *Manager) matchDouyinStateLocked(target string) (RoomState, bool) {
	if strings.HasPrefix(target, "bilibili:") || strings.HasPrefix(strings.ToLower(target), "weibo:") {
		return RoomState{}, false
	}
	trimmed := target
	for _, pfx := range []string{"douyin:live:", "douyin:user:", "douyin:web:", "douyin:", "live:"} {
		if strings.HasPrefix(target, pfx) {
			trimmed = strings.TrimSpace(strings.TrimPrefix(target, pfx))
			if trimmed == "" {
				return RoomState{}, false
			}
			break
		}
	}
	if st, ok := m.douyinStates[target]; ok {
		return m.hydrateDouyinStateLocked(st), true
	}
	if st, ok := m.douyinStates["douyin:live:"+trimmed]; ok {
		return m.hydrateDouyinStateLocked(st), true
	}
	if st, ok := m.douyinStates["douyin:user:"+trimmed]; ok {
		return m.hydrateDouyinStateLocked(st), true
	}
	for _, st := range m.douyinStates {
		if st.RoomIDStr == trimmed || st.AccountUID == trimmed || (st.WebRID != "" && st.WebRID == trimmed) || st.RoomKey == trimmed || (st.RecordSession != "" && st.RecordSession == trimmed) {
			return m.hydrateDouyinStateLocked(st), true
		}
	}
	return RoomState{}, false
}

func (m *Manager) buildPreviewLocked(siteID site.ID, st RoomState, sess *recordSession) RecordingPreview {
	isDouyin := siteID == site.Douyin
	defaultTransport := string(site.LiveTransportHLSFMP4)
	if isDouyin {
		defaultTransport = string(site.LiveTransportHTTPFLV)
	}

	roomID := st.RoomID
	uid := st.UID
	roomStr := st.RoomIDStr
	uidStr := st.AccountUID
	webRID := st.WebRID
	uname := st.Uname
	title := st.Title
	codec := st.Codec
	quality := st.Quality
	qualityName := st.QualityName
	transport := firstNonEmpty(st.Transport, defaultTransport)
	audioOnly := isDouyin && (st.AudioOnly || st.LiveState == site.LiveStateAudioOnly)
	itemDir := st.RecordItemDir
	stopReason := st.StopReason
	lastErr := st.LastError
	durSec := st.RecordDurationSec
	speedBps := st.RecordSpeedBps
	bytesWritten := st.RecordBytes
	segments := st.RecordSegments
	incidents := cloneIncidents(st.Incidents)

	var partPath, previewReason string
	var partIndex int
	var partBytes int64
	var partDur float64
	var partReady, previewSupported, recording bool
	if audioOnly {
		previewReason = previewReasonUnsupported
	}

	if sess != nil {
		recording = true
		if sess.roomID > 0 {
			roomID = sess.roomID
		}
		if sess.uid > 0 {
			uid = sess.uid
		}
		roomStr = firstNonEmpty(sess.roomIDStr, roomStr)
		uidStr = firstNonEmpty(sess.accountUID, uidStr)
		webRID = firstNonEmpty(sess.webRID, webRID)
		uname = firstNonEmpty(uname, sess.uname)
		title = firstNonEmpty(title, sess.title)
		codec = firstNonEmpty(sess.codec, codec)
		if sess.quality > 0 {
			quality = sess.quality
		}
		qualityName = firstNonEmpty(sess.qualityName, qualityName)
		transport = firstNonEmpty(sess.streamTransport, transport)
		if isDouyin {
			audioOnly = audioOnly || sess.audioOnly
		}
		itemDir = firstNonEmpty(sess.itemDir, itemDir)
		stopReason = firstNonEmpty(sess.stopReason, stopReason)
		lastErr = firstNonEmpty(sess.lastErr, lastErr)
		durSec = m.nowLocked().Sub(sess.startedAt).Seconds()
		if sess.lastSpeedBps > 0 {
			speedBps = sess.lastSpeedBps
		}
		bytesWritten = sess.bytesTotal
		segments = len(sess.segments)
		incidents = cloneIncidents(sess.incidents)
		partPath = sess.activePartPath
		partIndex = sess.activePartIndex
		partBytes = sess.activePartBytes
		partDur = sess.activePartDur
		partReady = sess.activePartReady
		previewSupported = sess.previewSupport
		previewReason = sess.previewReason
	}
	if !isDouyin && qualityName == "" && quality > 0 {
		qualityName = bilibili.LiveQualityFallbackName(quality)
	}
	if roomStr == "" && roomID > 0 {
		roomStr = strconv.FormatInt(roomID, 10)
	}
	if !isDouyin && uidStr == "" && uid > 0 {
		uidStr = strconv.FormatInt(uid, 10)
	}
	recKey := st.RoomKey
	if recKey == "" && roomStr != "" {
		recKey = string(siteID) + ":live:" + roomStr
	}

	return RecordingPreview{
		RecordingKey:     recKey,
		Site:             string(siteID),
		AccountUID:       uidStr,
		RoomID:           roomID,
		RoomIDStr:        roomStr,
		WebRID:           webRID,
		Uname:            uname,
		Title:            title,
		Codec:            codec,
		Quality:          quality,
		QualityName:      qualityName,
		Transport:        transport,
		AudioOnly:        audioOnly,
		Recording:        recording,
		PartPath:         partPath,
		PartIndex:        partIndex,
		PartBytes:        partBytes,
		PartDurationSec:  partDur,
		PartReady:        partReady,
		PreviewSupported: previewSupported,
		PreviewReason:    previewReason,
		ItemDir:          itemDir,
		DurationSec:      durSec,
		SpeedBps:         speedBps,
		BytesWritten:     bytesWritten,
		Segments:         segments,
		Incidents:        incidents,
		StopReason:       stopReason,
		LastError:        lastErr,
	}
}
