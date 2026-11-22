package live

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
)

// StartRecording begins recording a Bilibili live room into <cache_dir>/bilibili/<uid>/lives/<room_id>-<timestamp>/.
func (m *Manager) StartRecording(ctx context.Context, roomOrShortID int64) (RoomState, error) {
	if roomOrShortID <= 0 {
		return RoomState{}, &bilibili.SiteError{Site: site.Bilibili, Kind: site.ErrKindBadRequest, Message: "无效的直播间房间号"}
	}
	opCtx, cancelOp := m.withManagerContext(ctx)
	defer cancelOp()

	release, err := m.acquireStartSlot(opCtx, site.Bilibili, "bilibili:start:"+strconv.FormatInt(roomOrShortID, 10))
	if err != nil {
		return RoomState{}, err
	}
	defer release()

	// Fast path: if already recording this exact roomID, return active state with 0 network calls.
	m.mu.Lock()
	if sess := m.sessions[roomOrShortID]; sess != nil {
		st := m.states[roomOrShortID]
		m.normalizeBilibiliRoomStateLocked(&st)
		m.hydrateBilibiliStateFromSessionLocked(&st, sess)
		m.mu.Unlock()
		return st, nil
	}
	m.mu.Unlock()

	st, err := m.OpenRoom(opCtx, roomOrShortID)
	if err != nil {
		return RoomState{}, err
	}
	if st.RoomID > 0 {
		releaseCanon, cErr := m.acquireStartSlot(opCtx, site.Bilibili, "bilibili:room:"+strconv.FormatInt(st.RoomID, 10))
		if cErr != nil {
			return RoomState{}, cErr
		}
		defer releaseCanon()
	}
	if st.LiveStatus != 1 {
		return st, &bilibili.SiteError{
			Site:    site.Bilibili,
			Kind:    bilibili.ErrKindUnavailable,
			Message: fmt.Sprintf("直播间当前状态为「%s」，仅在直播中时支持录制", st.StatusText),
		}
	}
	if st.LiveSince <= 0 {
		return st, &bilibili.SiteError{Site: site.Bilibili, Kind: bilibili.ErrKindSchemaChanged, Message: "站点未返回本场直播的开播时间，无法建立录像条目"}
	}

	m.mu.Lock()
	if sess := m.sessions[st.RoomID]; sess != nil {
		cur := m.states[st.RoomID]
		m.normalizeBilibiliRoomStateLocked(&cur)
		m.hydrateBilibiliStateFromSessionLocked(&cur, sess)
		m.mu.Unlock()
		return cur, nil
	}
	codecOrder := append([]string(nil), m.liveSettings.CodecOrder...)
	m.mu.Unlock()

	cacheRoot := m.lib.CacheDir()
	if err := m.checkStartDiskSpace(cacheRoot, site.Bilibili); err != nil {
		return st, err
	}

	// Verify play info is accessible and not encrypted (203) before creating session.
	playSel, err := m.client.LiveRoomPlayInfo(opCtx, st.RoomID, codecOrder)
	if err != nil {
		return st, err
	}
	if playSel.QualityName == "" && playSel.Quality > 0 {
		playSel.QualityName = bilibili.LiveQualityFallbackName(playSel.Quality)
	}

	now := m.now().UTC()
	sessionID := fmt.Sprintf("%d:%d", st.RoomID, st.LiveSince)
	itemDir, err := library.EnsureWithinRoot(cacheRoot, filepath.Join(cacheRoot, "bilibili", strconv.FormatInt(st.UID, 10), "lives", fmt.Sprintf("%d-%d", st.RoomID, st.LiveSince)))
	if err != nil {
		return st, err
	}

	meta := library.ItemMetadata{
		Version:  1,
		ID:       "live:" + sessionID,
		Site:     "bilibili",
		Kind:     "live",
		Status:   "recording",
		Title:    st.Title,
		URL:      fmt.Sprintf("https://live.bilibili.com/%d", st.RoomID),
		PubTs:    st.LiveSince,
		CachedAt: now.Format(time.RFC3339),
		Author: library.ItemAuthor{
			Mid:  st.UID,
			Name: st.Uname,
			Face: st.Face,
		},
	}
	// Look up by the canonical key: "live:<room>:<since>" has two colons and
	// would be taken as a full key without the site, never matching.
	existing, exists, err := m.lib.GetItem(library.CanonicalItemKey("bilibili", "live", sessionID))
	if err != nil {
		return st, err
	}
	if exists {
		meta = existing
		if existing.ItemDir != "" {
			itemDir = existing.ItemDir
		}
		meta.Status = "recording"
	}
	if err := os.MkdirAll(itemDir, 0o700); err != nil {
		return st, err
	}
	if meta.Live == nil {
		meta.Live = &library.LiveRecordingInfo{RoomID: st.RoomID, LiveSince: st.LiveSince, StartedAt: now.Format(time.RFC3339)}
	}
	var resumedSeqAt time.Time
	if ts, parseErr := time.Parse(time.RFC3339Nano, meta.Live.LastSequenceAt); parseErr == nil && !ts.IsZero() {
		resumedSeqAt = ts.UTC()
	} else if ts, parseErr := time.Parse(time.RFC3339, meta.Live.EndedAt); parseErr == nil && !ts.IsZero() {
		resumedSeqAt = ts.UTC()
	} else if ts, parseErr := time.Parse(time.RFC3339, meta.Live.StartedAt); parseErr == nil && !ts.IsZero() {
		resumedSeqAt = ts.UTC()
	} else if st.LiveSince > 0 {
		resumedSeqAt = time.Unix(st.LiveSince, 0).UTC()
	}
	meta.Live.EndedAt, meta.Live.StopReason = "", ""
	meta.Live.Quality = playSel.Quality
	meta.Live.QualityName = playSel.QualityName
	meta.Live.Codec = playSel.Codec
	meta.Live.Source = "manual"
	if bilibili.AutomaticRequestSource(ctx) != "" {
		meta.Live.Source = "auto"
		st.Source = "auto"
	}
	savedMeta, err := m.lib.SaveItem(itemDir, meta)
	if err != nil {
		return st, err
	}

	recParent := context.Background()
	if meta.Live.Source == "auto" {
		recParent = bilibili.WithAutomaticRequests(recParent, "live")
	}
	recCtx, recCancel := context.WithCancel(recParent)
	initialTransport := string(site.LiveTransportHLSFMP4)
	if playSel.Format == "flv" || playSel.Protocol == "http_stream" {
		initialTransport = string(site.LiveTransportHTTPFLV)
	}
	var initLastSeq int64
	var initHasSeq bool
	var initLastSeqAt time.Time
	if initialTransport == string(site.LiveTransportHLSFMP4) && savedMeta.Live != nil && savedMeta.Live.HasSequence {
		initLastSeq = savedMeta.Live.LastSequence
		initHasSeq = true
		initLastSeqAt = resumedSeqAt
	}
	sess := &recordSession{
		siteID:          string(site.Bilibili),
		coverURL:        st.Cover,
		sessionID:       sessionID,
		startedAt:       now,
		reportAt:        now,
		roomID:          st.RoomID,
		uid:             st.UID,
		uname:           st.Uname,
		face:            st.Face,
		title:           st.Title,
		itemDir:         itemDir,
		itemMeta:        savedMeta,
		cancel:          recCancel,
		doneCh:          make(chan struct{}),
		nextPart:        nextPartNumber(savedMeta),
		lastSequence:    initLastSeq,
		hasSequence:     initHasSeq,
		lastSequenceAt:  initLastSeqAt,
		quality:         playSel.Quality,
		qualityName:     playSel.QualityName,
		codec:           playSel.Codec,
		streamTransport: initialTransport,
		previewSupport:  true,
		previewReason:   previewReasonWaitingFirstFragment,
	}
	m.initSessionIncidents(sess)
	for _, media := range savedMeta.Media {
		if media.Role == "live_segment" {
			sess.segments = append(sess.segments, media.Path)
			sess.bytesTotal += media.Bytes
		}
	}

	m.mu.Lock()
	if m.stopping {
		m.mu.Unlock()
		recCancel()
		return RoomState{}, &bilibili.SiteError{Site: site.Bilibili, Kind: bilibili.ErrKindUnavailable, Message: "应用正在退出，不能开始新的录制"}
	}
	if existingSess := m.sessions[st.RoomID]; existingSess != nil {
		cur := m.states[st.RoomID]
		m.normalizeBilibiliRoomStateLocked(&cur)
		m.hydrateBilibiliStateFromSessionLocked(&cur, existingSess)
		m.mu.Unlock()
		recCancel()
		return cur, nil
	}
	delete(m.stoppedBroadcasts, st.RoomID)
	m.sessions[st.RoomID] = sess
	st.Recording = true
	st.RecordSession = sessionID
	st.RecordItemDir = itemDir
	st.Quality = playSel.Quality
	st.QualityName = playSel.QualityName
	st.Codec = playSel.Codec
	st.StopReason = ""
	st.LastError = ""
	m.normalizeBilibiliRoomStateLocked(&st)
	hydrateSessionPreviewFieldsLocked(&st, sess)
	m.states[st.RoomID] = st
	m.wg.Add(1)
	m.mu.Unlock()

	go m.runRecordingSession(recCtx, sess, playSel, codecOrder)

	if m.onState != nil {
		m.onState(st)
	}
	return st, nil
}

// StopRecording stops an active recording for roomID and finalizes item.json,
// suppressing auto-restart for the remainder of the current broadcast (live.md §3).
func (m *Manager) StopRecording(roomID int64) (RoomState, error) {
	if roomID <= 0 {
		return RoomState{}, &bilibili.SiteError{Site: site.Bilibili, Kind: site.ErrKindBadRequest, Message: "无效的直播间房间号"}
	}
	return m.stopRecordingInternal(roomID, "user_stop", true)
}

func (m *Manager) stopRecordingInternal(roomID int64, reason string, suppressAutoRestart bool) (RoomState, error) {
	m.mu.Lock()
	if suppressAutoRestart {
		m.stoppedBroadcasts[roomID] = m.states[roomID].LiveSince
	}
	sess, ok := m.sessions[roomID]
	if ok && sess != nil && sess.stopReason == "" {
		sess.stopReason = reason
	}
	timeout := m.finalizeTimeout
	m.mu.Unlock()
	if !ok || sess == nil {
		m.mu.Lock()
		st := m.states[roomID]
		st.Recording = false
		m.normalizeBilibiliRoomStateLocked(&st)
		m.mu.Unlock()
		return st, nil
	}

	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	sess.cancel()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-sess.doneCh:
	case <-timer.C:
		m.mu.Lock()
		state := m.states[roomID]
		m.mu.Unlock()
		return state, &bilibili.SiteError{Site: site.Bilibili, Kind: "storage", Message: "录制收尾已到时间上限，临时片段会在下次启动时恢复"}
	}

	// runSessionLoop already wrote the finalized RoomState (including any
	// manifest-save LastError) and emitted onState before closing sess.doneCh.
	m.mu.Lock()
	st := m.states[roomID]
	m.normalizeBilibiliRoomStateLocked(&st)
	m.mu.Unlock()
	return st, nil
}
