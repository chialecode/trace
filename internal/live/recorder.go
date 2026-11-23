package live

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
)

type partStreamMeta struct {
	codec       string
	quality     int
	qualityName string
}

type reconnectOutcome uint8

const (
	reconnectOK reconnectOutcome = iota
	reconnectRetry
	reconnectEnded
	reconnectStop
)

type sessionDriver struct {
	siteID          site.ID
	unregister      func()
	saveCover       func(ctx context.Context) error
	currentMeta     func() partStreamMeta
	updateStateMeta func(meta partStreamMeta) // called under m.mu
	recordPart      func(ctx context.Context, flvConn **activeFLVConn, partPath string) (int64, error)
	reportStopError func(ctx context.Context, err error, reason string)
	reconnect       func(ctx context.Context) (reconnectOutcome, error)
	finalizeState   func(reason string, now time.Time) RoomState // called under m.mu
	saveStateLocked func(state RoomState)                        // called under m.mu
}

func nextPartNumber(meta library.ItemMetadata) int {
	next := 1
	for _, file := range meta.Media {
		var n int
		if count, _ := fmt.Sscanf(file.Path, "part-%d.mp4", &n); count == 1 {
			next = max(next, n+1)
		}
	}
	for _, path := range meta.PendingParts {
		var n int
		if count, _ := fmt.Sscanf(path, "part-%d.mp4.part", &n); count == 1 {
			next = max(next, n+1)
		}
	}
	return next
}

func (m *Manager) runRecordingSession(ctx context.Context, sess *recordSession, initial bilibili.LivePlaySelection, codecOrder []string) {
	selection := initial
	m.runSessionLoop(ctx, sess, sessionDriver{
		siteID: site.Bilibili,
		unregister: func() {
			m.mu.Lock()
			delete(m.sessions, sess.roomID)
			m.mu.Unlock()
		},
		saveCover: func(ctx context.Context) error {
			return m.saveLiveCover(ctx, sess)
		},
		currentMeta: func() partStreamMeta {
			qName := selection.QualityName
			if qName == "" && selection.Quality > 0 {
				qName = bilibili.LiveQualityFallbackName(selection.Quality)
			}
			return partStreamMeta{
				codec:       selection.Codec,
				quality:     selection.Quality,
				qualityName: qName,
			}
		},
		updateStateMeta: func(pm partStreamMeta) {
			if st, ok := m.states[sess.roomID]; ok {
				st.Codec, st.Quality, st.QualityName = pm.codec, pm.quality, pm.qualityName
				m.states[sess.roomID] = st
			}
		},
		recordPart: func(ctx context.Context, flvConn **activeFLVConn, partPath string) (int64, error) {
			nextTransport := string(site.LiveTransportHLSFMP4)
			if selection.Format == "flv" || selection.Protocol == "http_stream" {
				nextTransport = string(site.LiveTransportHTTPFLV)
			}
			m.mu.Lock()
			switchedTransport := sess.streamTransport != "" && sess.streamTransport != nextTransport
			if switchedTransport || nextTransport == string(site.LiveTransportHTTPFLV) {
				sess.lastSequence = 0
				sess.hasSequence = false
				sess.lastSequenceAt = time.Time{}
			}
			m.mu.Unlock()
			if nextTransport == string(site.LiveTransportHTTPFLV) {
				m.beginSessionPart(sess, sess.nextPart, partPath, nextTransport)
				return m.recordBilibiliFLVPart(ctx, sess, selection.URLs, flvConn, partPath)
			}
			if *flvConn != nil {
				_ = (*flvConn).Close()
				*flvConn = nil
			}
			m.beginSessionPart(sess, sess.nextPart, partPath, nextTransport)
			return m.recordHLSSegment(ctx, sess, selection.URLs, partPath, true)
		},
		reportStopError: func(ctx context.Context, err error, reason string) {
			if m.client != nil {
				leave := sess.enterExternal()
				m.client.ReportAutomaticError(ctx, err)
				leave()
			}
			if reason == site.ErrKindRiskControl || reason == site.ErrKindRateLimited {
				m.recordSiteAutoStop(site.Bilibili, reason)
			}
		},
		reconnect: func(ctx context.Context) (reconnectOutcome, error) {
			m.mu.Lock()
			var liveSince int64
			if sess.itemMeta.Live != nil {
				liveSince = sess.itemMeta.Live.LiveSince
			}
			m.mu.Unlock()

			leave := sess.enterExternal()
			room, err := m.client.RoomInit(ctx, sess.roomID)
			leave()
			if err == nil && (room.LiveStatus != 1 || (room.LiveTime > 0 && room.LiveTime != liveSince)) {
				return reconnectEnded, nil
			}
			var fresh bilibili.LivePlaySelection
			if err == nil {
				leave = sess.enterExternal()
				fresh, err = m.client.LiveRoomPlayInfo(ctx, sess.roomID, codecOrder)
				leave()
			}
			if err != nil {
				if liveStopReason(err) != "" {
					return reconnectStop, err
				}
				return reconnectRetry, err
			}
			if fresh.LiveStatus != 1 || len(fresh.URLs) == 0 {
				return reconnectEnded, nil
			}
			selection = fresh
			return reconnectOK, nil
		},
		finalizeState: func(reason string, now time.Time) RoomState {
			state := m.states[sess.roomID]
			m.normalizeBilibiliRoomStateLocked(&state)
			state.Recording = false
			state.StopReason = reason
			state.LastError = sess.lastErr
			state.RecordDurationSec = now.Sub(sess.startedAt).Seconds()
			state.RecordBytes = sess.bytesTotal
			state.RecordSegments = len(sess.segments)
			state.RecordItemDir = sess.itemDir
			state.Incidents = cloneIncidents(sess.incidents)
			hydrateSessionPreviewFieldsLocked(&state, nil)
			m.states[sess.roomID] = state
			return state
		},
		saveStateLocked: func(state RoomState) {
			m.states[sess.roomID] = state
		},
	})
}

func (m *Manager) runSessionLoop(ctx context.Context, sess *recordSession, driver sessionDriver) {
	defer m.wg.Done()
	defer close(sess.doneCh)
	defer driver.unregister()

	var flvConn *activeFLVConn
	defer func() {
		if flvConn != nil {
			_ = flvConn.Close()
		}
	}()

	if err := driver.saveCover(ctx); err != nil {
		reason := liveStopReason(err)
		if reason == "" {
			reason = "storage"
		}
		m.setSessionStop(sess, reason, err)
	}

	const maxConsecutiveTransientHLSSchemaRetries = 2
	hasRecordedMedia := len(sess.segments) > 0
	consecutiveTransientSchema := 0
	var lostAt time.Time
	for ctx.Err() == nil {
		m.mu.Lock()
		stopped := sess.stopReason != ""
		m.mu.Unlock()
		if stopped {
			break
		}
		if err := m.checkDiskSpace(sess.itemDir, driver.siteID); err != nil {
			if isDiskFullError(err) {
				m.recordSessionIncident(sess, library.LiveIncident{
					Kind:   library.LiveIncidentDisk,
					At:     m.now().UTC().Format(time.RFC3339),
					Reason: "保存位置磁盘空间不足，录制已停止",
				}, true)
			}
			m.setSessionStop(sess, "storage", err)
			break
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
				err = &site.Error{Site: driver.siteID, Kind: site.ErrKindStorage, Message: "录像目标已存在且未登记，原文件已保留"}
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

		pm := driver.currentMeta()
		m.mu.Lock()
		sess.itemMeta.PendingParts = append(sess.itemMeta.PendingParts, pending)
		if sess.itemMeta.Live != nil {
			sess.itemMeta.Live.Codec = pm.codec
			sess.itemMeta.Live.Quality = pm.quality
			sess.itemMeta.Live.QualityName = pm.qualityName
		}
		sess.codec, sess.quality, sess.qualityName = pm.codec, pm.quality, pm.qualityName
		driver.updateStateMeta(pm)
		m.mu.Unlock()
		if err := m.saveSession(sess); err != nil {
			m.setSessionStop(sess, "storage", err)
			break
		}

		sess.segmentDuration = 0
		m.mu.Lock()
		sess.wroteMedia = false
		m.mu.Unlock()

		n, recordErr := driver.recordPart(ctx, &flvConn, path)
		m.endSessionPart(sess)
		if !errors.Is(recordErr, errSegmentRollover) && flvConn != nil {
			_ = flvConn.Close()
			flvConn = nil
		}

		if n > 0 {
			m.mu.Lock()
			actualCodec := firstNonEmpty(sess.codec, pm.codec)
			wroteMedia := sess.wroteMedia
			m.mu.Unlock()
			job := partJob{
				path:        path,
				partRel:     partRel,
				pending:     pending,
				codec:       actualCodec,
				quality:     pm.quality,
				qualityName: pm.qualityName,
			}
			if errors.Is(recordErr, errSegmentRollover) && ctx.Err() == nil {
				m.finishPartAsync(sess, job)
			} else {
				sess.waitPartsFinished()
				if err := m.finishPart(sess, job); err != nil {
					m.setSessionStop(sess, "storage", err)
					break
				}
			}
			sess.nextPart++
			if wroteMedia {
				hasRecordedMedia = true
				lostAt = time.Time{}
				consecutiveTransientSchema = 0
			}
		} else {
			_ = removeFileWithRetry(path)
			m.mu.Lock()
			sess.itemMeta.PendingParts = removePending(sess.itemMeta.PendingParts, pending)
			clearLiveCheckpoint(&sess.itemMeta, pending)
			m.mu.Unlock()
		}

		if err := m.saveSession(sess); err != nil {
			if isDiskFullError(err) {
				m.recordSessionIncident(sess, library.LiveIncident{
					Kind:   library.LiveIncidentDisk,
					At:     m.now().UTC().Format(time.RFC3339),
					Part:   pending,
					Reason: "保存位置磁盘空间不足，录制已停止",
				}, true)
			}
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
			if isDiskFullError(recordErr) {
				m.recordSessionIncident(sess, library.LiveIncident{
					Kind:   library.LiveIncidentDisk,
					At:     m.now().UTC().Format(time.RFC3339),
					Part:   pending,
					Reason: "保存位置磁盘空间不足，录制已停止",
				}, true)
			}
			if reason := liveStopReason(recordErr); reason != "" {
				if isTransientHLSSchemaError(recordErr) && hasRecordedMedia && consecutiveTransientSchema < maxConsecutiveTransientHLSSchemaRetries {
					consecutiveTransientSchema++
				} else {
					driver.reportStopError(ctx, recordErr, reason)
					m.setSessionStop(sess, reason, recordErr)
					break
				}
			} else {
				consecutiveTransientSchema = 0
			}
			m.mu.Lock()
			sess.lastErr = sanitizeLiveError(recordErr)
			maxLoss := m.maxStreamLossDur
			m.mu.Unlock()
			now := m.now()
			if lostAt.IsZero() {
				lostAt = now
			}
			if maxLoss > 0 && now.Sub(lostAt) >= maxLoss {
				m.setSessionStop(sess, "stream_timeout", recordErr)
				break
			}
		}

		// Inner reconnect loop: keep retrying wait + status check + stream
		// re-selection until a fresh valid stream is acquired or the session stops,
		// so transient errors never reuse an expired URL or zeroed selection.
		m.markSessionDisconnected(sess, m.now())
		if !m.reconnectSessionLoop(ctx, sess, driver, &lostAt) {
			break
		}
	}

	sess.waitPartsFinished()
	now := m.now()
	m.closeSessionSlowIncident(sess, now)

	m.mu.Lock()
	if sess.stopReason == "" {
		if ctx.Err() != nil {
			sess.stopReason = "app_closed"
		} else {
			sess.stopReason = "ended"
		}
	}
	reason := sess.stopReason
	state := driver.finalizeState(reason, now)
	if sess.itemMeta.Live != nil {
		sess.itemMeta.Live.StopReason = reason
		sess.itemMeta.Live.EndedAt = now.UTC().Format(time.RFC3339)
		sess.itemMeta.Live.EndTitle = state.Title
	}
	sess.itemMeta.Status = "complete"
	if !hasAnyLiveSegment(sess.itemMeta) {
		sess.itemMeta.Status = "failed"
	}
	if reason != "ended" && reason != "user_stop" && reason != "app_closed" {
		addLiveMissing(&sess.itemMeta, "recording", "录制提前结束 ("+reason+")")
	}
	if len(sess.itemMeta.PendingParts) > 0 || len(sess.itemMeta.Missing) > 0 {
		sess.itemMeta.Status = "partial"
	}
	m.mu.Unlock()

	if err := m.saveSession(sess); err != nil {
		m.setSessionStop(sess, "storage", err)
		state.StopReason = "storage"
		state.LastError = "录像清单写入失败，已保留可恢复文件"
		m.mu.Lock()
		driver.saveStateLocked(state)
		m.mu.Unlock()
	}
	if m.onState != nil {
		leave := sess.enterExternal()
		m.onState(state)
		leave()
	}
}

func (m *Manager) reconnectSessionLoop(
	ctx context.Context,
	sess *recordSession,
	driver sessionDriver,
	lostAt *time.Time,
) bool {
	for {
		m.mu.Lock()
		wait := m.reconnectWait
		maxLoss := m.maxStreamLossDur
		stopped := sess.stopReason != ""
		m.mu.Unlock()
		if stopped || ctx.Err() != nil {
			return false
		}
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return false
			case <-timer.C:
			}
		}
		if ctx.Err() != nil {
			return false
		}

		outcome, err := driver.reconnect(ctx)
		switch outcome {
		case reconnectOK:
			return true
		case reconnectEnded:
			m.setSessionStop(sess, "ended", nil)
			return false
		case reconnectStop:
			reason := liveStopReason(err)
			if reason == "" {
				reason = site.ErrKindBlocked
			}
			driver.reportStopError(ctx, err, reason)
			m.setSessionStop(sess, reason, err)
			return false
		default:
			if err != nil {
				m.mu.Lock()
				sess.lastErr = sanitizeLiveError(err)
				m.mu.Unlock()
			}
			now := m.now()
			if lostAt.IsZero() {
				*lostAt = now
			}
			if maxLoss > 0 && now.Sub(*lostAt) >= maxLoss {
				m.setSessionStop(sess, "stream_timeout", err)
				return false
			}
		}
	}
}

func (m *Manager) checkDiskSpace(dir string, siteID site.ID) error {
	m.mu.Lock()
	freeFn := m.freeBytesFunc
	minFree := m.minFreeBytes
	m.mu.Unlock()
	if freeFn == nil || minFree == 0 {
		return nil
	}
	free, err := freeFn(dir)
	if err != nil {
		return err
	}
	if free < minFree {
		return &site.Error{Site: siteID, Kind: site.ErrKindStorage, Message: "磁盘空间不足 1 GiB，已停止录制"}
	}
	return nil
}

func (m *Manager) setSessionStop(sess *recordSession, reason string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess.stopReason = reason
	if err != nil {
		sess.lastErr = sanitizeLiveError(err)
	}
}

func liveStopReason(err error) string {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return "storage"
	}
	if IsUnsupportedCodec(err) {
		return bilibili.ErrKindBlocked
	}
	if IsFLVFormatError(err) {
		return bilibili.ErrKindSchemaChanged
	}
	switch bilibili.KindOf(err) {
	case bilibili.ErrKindRiskControl, bilibili.ErrKindRateLimited, bilibili.ErrKindAuthRequired, bilibili.ErrKindQuota, bilibili.ErrKindBackoff, "storage", bilibili.ErrKindSchemaChanged, bilibili.ErrKindBlocked:
		return bilibili.KindOf(err)
	}
	return ""
}

func sanitizeLiveError(err error) string {
	if err == nil {
		return ""
	}
	var se *site.Error
	if errors.As(err, &se) {
		if clean := library.SanitizeLiveIncidentText(se.Error()); clean != "" {
			return clean
		}
		return "直播请求失败"
	}
	unwrapped := site.UnwrapURLError(err)
	if unwrapped == nil {
		unwrapped = err
	}
	if clean := library.SanitizeLiveIncidentText(unwrapped.Error()); clean != "" {
		return clean
	}
	return "直播请求失败"
}

func mergeLiveMedia(files []library.MediaFile, added library.MediaFile) []library.MediaFile {
	for i := range files {
		if files[i].Path == added.Path {
			files[i] = added
			return files
		}
	}
	return append(files, added)
}

func removePending(values []string, value string) []string {
	var result []string
	for _, candidate := range values {
		if candidate != value {
			result = append(result, candidate)
		}
	}
	return result
}

func mergeLiveSegment(segments []library.LiveSegmentInfo, added library.LiveSegmentInfo) []library.LiveSegmentInfo {
	for i := range segments {
		if segments[i].File == added.File {
			segments[i] = added
			return segments
		}
	}
	return append(segments, added)
}

func addLiveMissing(meta *library.ItemMetadata, item, reason string) {
	clearLiveMissing(meta, item)
	meta.Missing = append(meta.Missing, library.MissingRecord{Item: item, Reason: reason})
}

func clearLiveMissing(meta *library.ItemMetadata, item string) {
	var keep []library.MissingRecord
	for _, missing := range meta.Missing {
		if missing.Item != item {
			keep = append(keep, missing)
		}
	}
	meta.Missing = keep
}

func clearLiveCheckpoint(meta *library.ItemMetadata, pending string) {
	if meta.Live == nil || meta.Live.PendingFile != pending {
		return
	}
	meta.Live.PendingFile = ""
	meta.Live.PendingBytes = 0
	meta.Live.PendingDuration = 0
}
