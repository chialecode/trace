package live

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
)

// StartDouyinRecording starts an HTTP-FLV live recording into
// <cache_dir>/douyin/<sha256(sec_uid)[:12]>/lives/<room_id>/ with item key douyin:live:<room_id>.
func (m *Manager) StartDouyinRecording(ctx context.Context, uid, roomOrWebRID string) (RoomState, error) {
	uid = strings.TrimSpace(uid)
	target := strings.TrimSpace(roomOrWebRID)
	if uid == "" && target == "" {
		return RoomState{}, &site.Error{Site: site.Douyin, Kind: site.ErrKindBadRequest, Message: "缺少抖音直播间号或主播标识"}
	}

	provider, ok := m.getSiteProvider(site.Douyin)
	if !ok {
		return RoomState{}, &site.Error{Site: site.Douyin, Kind: site.ErrKindUnavailable, Message: "抖音直播服务未初始化"}
	}

	opCtx, cancelOp := m.withManagerContext(ctx)
	defer cancelOp()

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

	startKey := "douyin:start:" + firstNonEmpty(uid, target)
	releaseStart, err := m.acquireStartSlot(opCtx, site.Douyin, startKey)
	if err != nil {
		return RoomState{}, err
	}
	defer releaseStart()

	m.mu.Lock()
	codecOrder := append([]string(nil), m.liveSettings.CodecOrder...)
	m.mu.Unlock()

	cacheRoot := m.lib.CacheDir()
	if err := m.checkStartDiskSpace(cacheRoot, site.Douyin); err != nil {
		return RoomState{}, err
	}

	plan, err := provider.SelectLiveStream(opCtx, uid, target, codecOrder)
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

	releaseRoom, err := m.acquireStartSlot(opCtx, site.Douyin, "douyin:room:"+roomID)
	if err != nil {
		return RoomState{}, err
	}
	defer releaseRoom()

	m.mu.Lock()
	if existingSess, already := m.douyinSessions[roomID]; already && existingSess != nil {
		st := m.hydrateDouyinStateLocked(m.douyinStates[existingSess.stateKey])
		m.mu.Unlock()
		return st, nil
	}
	m.mu.Unlock()

	subAcc := m.findDouyinSubAccount(secUID)
	// Populate room display metadata from cache or CheckLiveStatus.
	st, okCached := m.CachedAccountState(site.Douyin, secUID)
	if !okCached || st.RoomIDStr != roomID || st.Title == "" {
		forceFresh := !okCached || st.RoomIDStr != roomID
		if freshSt, cErr := m.checkAccountLiveStatus(opCtx, site.Douyin, secUID, firstNonEmpty(plan.WebRID, target), forceFresh, false); cErr == nil {
			st = freshSt
		}
	}

	m.mu.Lock()
	rawCover := firstNonEmpty(m.douyinRawCovers[secUID], st.Cover)
	m.mu.Unlock()

	now := m.now().UTC()
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
		siteID:          string(site.Douyin),
		accountUID:      secUID,
		roomIDStr:       roomID,
		webRID:          webRID,
		stateKey:        stateKey,
		coverURL:        rawCover,
		transport:       transport,
		sessionID:       roomID,
		roomID:          parsedRoomInt,
		uname:           uname,
		face:            face,
		title:           title,
		itemDir:         itemDir,
		itemMeta:        savedMeta,
		cancel:          recCancel,
		doneCh:          make(chan struct{}),
		startedAt:       now,
		reportAt:        now,
		nextPart:        nextPartNumber(savedMeta),
		quality:         plan.Quality,
		qualityName:     plan.QualityName,
		codec:           plan.Codec,
		streamTransport: string(site.LiveTransportHTTPFLV),
		previewSupport:  true,
		previewReason:   previewReasonWaitingFirstFragment,
	}
	m.initSessionIncidents(sess)
	if plan.BitrateBps > 0 {
		sess.nominalByteRate = float64(plan.BitrateBps) / 8.0
	}
	for _, media := range savedMeta.Media {
		if media.Role == "live_segment" {
			sess.segments = append(sess.segments, media.Path)
			sess.bytesTotal += media.Bytes
		}
	}

	outState := RoomState{
		Site:             string(site.Douyin),
		AccountUID:       secUID,
		UniqueID:         subAcc.UniqueID,
		RoomID:           parsedRoomInt,
		RoomIDStr:        roomID,
		WebRID:           webRID,
		RoomKey:          stateKey,
		LiveState:        site.LiveStateLive,
		Uname:            uname,
		Face:             face,
		Title:            title,
		Cover:            sanitizeLiveURL(rawCover),
		AreaName:         st.AreaName,
		LiveStatus:       1,
		LiveSince:        liveSince,
		Source:           meta.Live.Source,
		StatusText:       "直播中",
		Quality:          plan.Quality,
		QualityName:      plan.QualityName,
		Codec:            plan.Codec,
		Transport:        string(site.LiveTransportHTTPFLV),
		Recording:        true,
		RecordSession:    roomID,
		RecordBytes:      sess.bytesTotal,
		RecordSegments:   len(sess.segments),
		RecordItemDir:    itemDir,
		PreviewSupported: true,
		PreviewReason:    previewReasonWaitingFirstFragment,
		Incidents:        cloneIncidents(sess.incidents),
		UpdatedAt:        now.Format(time.RFC3339),
	}

	m.mu.Lock()
	if m.stopping {
		m.mu.Unlock()
		recCancel()
		return RoomState{}, &site.Error{Site: site.Douyin, Kind: site.ErrKindUnavailable, Message: "应用正在退出，不能开始新的录制"}
	}
	if existingSess, already := m.douyinSessions[roomID]; already && existingSess != nil {
		cur := m.hydrateDouyinStateLocked(m.douyinStates[existingSess.stateKey])
		m.mu.Unlock()
		recCancel()
		return cur, nil
	}
	delete(m.douyinStoppedRooms, roomID)
	m.douyinSessions[roomID] = sess
	m.douyinStates[stateKey] = outState
	for k, old := range m.douyinStates {
		if k != stateKey && old.AccountUID == secUID && !m.isRoomActivelyRecordingLocked(old.RoomIDStr) {
			delete(m.douyinStates, k)
		}
	}
	m.wg.Add(1)
	m.mu.Unlock()

	go m.runDouyinRecordingSession(recCtx, sess, plan, codecOrder)

	if m.onState != nil {
		m.onState(outState)
	}
	return outState, nil
}

// StopDouyinRecording stops an active Douyin live recording by room_id, web_rid, or sec_uid,
// suppressing auto-restart for the remainder of that room_id broadcast.
func (m *Manager) StopDouyinRecording(roomOrUID string) (RoomState, error) {
	target := strings.TrimSpace(roomOrUID)
	if target == "" {
		return RoomState{Site: string(site.Douyin)}, &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindBadRequest,
			Message: "缺少抖音直播间号或主播标识",
		}
	}
	m.mu.Lock()
	var sess *recordSession
	var roomID string
	if s, ok := m.douyinSessions[target]; ok {
		sess = s
		roomID = target
	} else {
		for rid, s := range m.douyinSessions {
			if s == nil {
				continue
			}
			if s.accountUID == target || (s.webRID != "" && s.webRID == target) || s.stateKey == target {
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
	timeout := m.finalizeTimeout
	m.mu.Unlock()

	if sess == nil {
		m.mu.Lock()
		for _, st := range m.douyinStates {
			if st.RoomIDStr == target || st.AccountUID == target || (st.WebRID != "" && st.WebRID == target) || st.RoomKey == target {
				st.Recording = false
				hydrateSessionPreviewFieldsLocked(&st, nil)
				m.mu.Unlock()
				return st, nil
			}
		}
		m.mu.Unlock()
		return RoomState{Site: string(site.Douyin)}, nil
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
		state := m.douyinStates[sess.stateKey]
		m.mu.Unlock()
		return state, &site.Error{Site: site.Douyin, Kind: site.ErrKindStorage, Message: "录制收尾已到时间上限，临时片段会在下次启动时恢复"}
	}

	// runSessionLoop already wrote the finalized RoomState (including any
	// manifest-save LastError) and emitted onState before closing sess.doneCh.
	m.mu.Lock()
	st := m.douyinStates[sess.stateKey]
	m.mu.Unlock()
	return st, nil
}

func (m *Manager) runDouyinRecordingSession(ctx context.Context, sess *recordSession, initial site.LiveStreamPlan, codecOrder []string) {
	plan := initial
	if len(plan.Media.AllowedDomains) == 0 {
		plan.Media = sess.transport
	}
	provider, _ := m.getSiteProvider(site.Douyin)

	m.runSessionLoop(ctx, sess, sessionDriver{
		siteID: site.Douyin,
		unregister: func() {
			m.mu.Lock()
			delete(m.douyinSessions, sess.roomIDStr)
			m.mu.Unlock()
		},
		saveCover: func(ctx context.Context) error {
			return m.saveDouyinLiveCover(ctx, sess, plan.Media)
		},
		currentMeta: func() partStreamMeta {
			return partStreamMeta{
				codec:       plan.Codec,
				quality:     plan.Quality,
				qualityName: plan.QualityName,
			}
		},
		updateStateMeta: func(pm partStreamMeta) {
			if st, ok := m.douyinStates[sess.stateKey]; ok {
				st.Codec, st.Quality, st.QualityName = pm.codec, pm.quality, pm.qualityName
				m.douyinStates[sess.stateKey] = st
			}
		},
		recordPart: func(ctx context.Context, flvConn **activeFLVConn, partPath string) (int64, error) {
			m.beginSessionPart(sess, sess.nextPart, partPath, string(site.LiveTransportHTTPFLV))
			return m.recordFLVPartWithTransport(ctx, sess, plan.Media, plan.URLs, flvConn, partPath)
		},
		reportStopError: func(ctx context.Context, err error, reason string) {
			if rep, ok := provider.(LiveErrorReporter); ok {
				leave := sess.enterExternal()
				rep.ReportAutomaticError(ctx, err)
				leave()
			}
			if reason == site.ErrKindRiskControl || reason == site.ErrKindRateLimited {
				m.recordSiteAutoStop(site.Douyin, reason)
			}
		},
		reconnect: func(ctx context.Context) (reconnectOutcome, error) {
			if provider == nil {
				return reconnectEnded, nil
			}
			m.mu.Lock()
			curWebRID := sess.webRID
			m.mu.Unlock()

			// Never reuse an expired/disconnected FLV URL; check room state and re-select stream.
			leave := sess.enterExternal()
			liveSt, statusErr := provider.CheckLiveStatus(ctx, sess.accountUID, curWebRID)
			leave()
			if statusErr == nil {
				if liveSt.Status != site.LiveStateLive || (liveSt.RoomID != "" && liveSt.RoomID != sess.roomIDStr) {
					return reconnectEnded, nil
				}
				if liveSt.WebRID != "" {
					m.mu.Lock()
					sess.webRID = liveSt.WebRID
					curWebRID = liveSt.WebRID
					m.mu.Unlock()
				}
			} else if liveStopReason(statusErr) != "" {
				return reconnectStop, statusErr
			}

			leave = sess.enterExternal()
			freshPlan, selErr := provider.SelectLiveStream(ctx, sess.accountUID, firstNonEmpty(curWebRID, sess.roomIDStr), codecOrder)
			leave()
			if selErr != nil {
				var sErr *site.Error
				if errors.As(selErr, &sErr) && sErr.Kind == site.ErrKindUnavailable && !sErr.Unsupported {
					// Room went offline right between CheckLiveStatus and SelectLiveStream.
					leave = sess.enterExternal()
					st2, sErr2 := provider.CheckLiveStatus(ctx, sess.accountUID, curWebRID)
					leave()
					if sErr2 == nil && st2.Status != site.LiveStateLive {
						return reconnectEnded, nil
					}
				}
				if liveStopReason(selErr) != "" {
					return reconnectStop, selErr
				}
				return reconnectRetry, selErr
			}
			if freshPlan.Status == site.LiveStateAudioOnly || (freshPlan.Transport != "" && freshPlan.Transport != site.LiveTransportHTTPFLV) || strings.EqualFold(freshPlan.Format, "ts") {
				return reconnectStop, &site.Error{
					Site:    site.Douyin,
					Kind:    site.ErrKindBlocked,
					Message: "直播间已切换为不支持录制的流格式",
				}
			}
			if freshPlan.Status != site.LiveStateLive || len(freshPlan.URLs) == 0 || (freshPlan.RoomID != "" && freshPlan.RoomID != sess.roomIDStr) {
				return reconnectEnded, nil
			}
			if len(freshPlan.Media.AllowedDomains) == 0 {
				freshPlan.Media = sess.transport
			}
			if freshPlan.BitrateBps > 0 {
				m.setSessionNominalBitrateBps(sess, freshPlan.BitrateBps)
			}
			plan = freshPlan
			return reconnectOK, nil
		},
		finalizeState: func(reason string, now time.Time) RoomState {
			state := m.douyinStates[sess.stateKey]
			state.Recording = false
			state.StopReason = reason
			state.LastError = sess.lastErr
			state.RecordDurationSec = now.Sub(sess.startedAt).Seconds()
			state.RecordBytes = sess.bytesTotal
			state.RecordSegments = len(sess.segments)
			state.RecordItemDir = sess.itemDir
			state.Incidents = cloneIncidents(sess.incidents)
			hydrateSessionPreviewFieldsLocked(&state, nil)
			if reason == "ended" {
				state.LiveStatus = 0
				state.LiveState = site.LiveStateOffline
				state.StatusText = "未开播"
				if sess.accountUID != "" {
					delete(m.douyinStatusCache, sess.accountUID)
					if m.douyinRoomHints[sess.accountUID] == sess.roomIDStr {
						delete(m.douyinRoomHints, sess.accountUID)
					}
					m.douyinPrevLive[sess.accountUID] = site.LiveStateOffline
				}
			}
			m.douyinStates[sess.stateKey] = state
			return state
		},
		saveStateLocked: func(state RoomState) {
			m.douyinStates[sess.stateKey] = state
		},
	})
}
