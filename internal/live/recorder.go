package live

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site/bilibili"
)

// RecoverOrphanParts processes only files named in a live item's pending list.
// Imported roots are already represented by the library index; unrelated files
// and user-created .part files are never scanned or renamed.
func (m *Manager) RecoverOrphanParts() {
	if m.lib == nil {
		return
	}
	for offset := 0; ; offset += 200 {
		items, err := m.lib.ListItems(library.FeedFilter{Kind: "live", Limit: 200, Offset: offset})
		if err != nil {
			return
		}
		for _, meta := range items {
			m.mu.Lock()
			active := false
			for _, session := range m.sessions {
				active = active || session.itemDir == meta.ItemDir
			}
			m.mu.Unlock()
			if active || len(meta.PendingParts) == 0 {
				continue
			}
			var remaining []string
			for _, rel := range meta.PendingParts {
				if !strings.HasPrefix(rel, "part-") || !strings.HasSuffix(rel, ".mp4.part") {
					remaining = append(remaining, rel)
					continue
				}
				partPath, err := library.ResolveItemPath(meta.ItemDir, rel)
				if err != nil {
					remaining = append(remaining, rel)
					continue
				}
				finalRel := strings.TrimSuffix(rel, ".part")
				finalPath, err := library.ResolveItemPath(meta.ItemDir, finalRel)
				if err != nil {
					remaining = append(remaining, rel)
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), m.finalizeTimeout)
				expected := 0.0
				if meta.Live != nil && meta.Live.PendingFile == rel {
					expected = meta.Live.PendingDuration
					if info, statErr := os.Stat(partPath); statErr == nil && meta.Live.PendingBytes > 0 && info.Size() > meta.Live.PendingBytes {
						err = os.Truncate(partPath, meta.Live.PendingBytes)
					}
				}
				var summary download.MuxTrackSummary
				if err == nil {
					if _, statErr := os.Stat(partPath); os.IsNotExist(statErr) {
						summary, err = download.InspectMP4FileContext(ctx, finalPath)
					} else {
						summary, err = finalizeLivePart(ctx, partPath, finalPath, expected)
					}
				}
				cancel()
				if err != nil {
					remaining = append(remaining, rel)
					addLiveMissing(&meta, rel, "转换失败")
					continue
				}
				meta.Media = mergeLiveMedia(meta.Media, library.MediaFile{Role: "live_segment", Path: finalRel, Bytes: summary.FileBytes, SHA256: summary.SHA256, Duration: int(math.Round(summary.DurationSec)), Width: summary.Width, Height: summary.Height})
				if meta.Live != nil {
					qName := meta.Live.QualityName
					if qName == "" && meta.Live.Quality > 0 {
						qName = bilibili.LiveQualityFallbackName(meta.Live.Quality)
					}
					meta.Live.QualityName = qName
					meta.Live.Segments = mergeLiveSegment(meta.Live.Segments, library.LiveSegmentInfo{File: finalRel, Bytes: summary.FileBytes, SHA256: summary.SHA256, DurationSec: summary.DurationSec, Codec: meta.Live.Codec, Quality: meta.Live.Quality, QualityName: qName, Width: summary.Width, Height: summary.Height})
				}
				clearLiveMissing(&meta, rel)
				clearLiveCheckpoint(&meta, rel)
			}
			meta.PendingParts = remaining
			meta.Status = "complete"
			if len(remaining) > 0 || len(meta.Missing) > 0 {
				meta.Status = "partial"
			}
			if meta.Live != nil && meta.Live.StopReason == "" {
				meta.Live.StopReason = "app_closed"
				meta.Live.EndedAt = m.nowFunc().UTC().Format(time.RFC3339)
			}
			if _, err := m.lib.SaveItem(meta.ItemDir, meta); err == nil {
				for _, original := range itemsPendingToRemove(items, meta) {
					if path, err := library.ResolveItemPath(meta.ItemDir, original); err == nil {
						_ = os.Remove(path)
					}
				}
			}
		}
		if len(items) < 200 {
			return
		}
	}
}

func itemsPendingToRemove(items []library.ItemMetadata, saved library.ItemMetadata) []string {
	var result []string
	for _, item := range items {
		if item.ItemKey == saved.ItemKey {
			for _, rel := range item.PendingParts {
				found := false
				for _, pending := range saved.PendingParts {
					found = found || rel == pending
				}
				if !found {
					result = append(result, rel)
				}
			}
		}
	}
	return result
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
	defer m.wg.Done()
	defer close(sess.doneCh)
	defer func() { m.mu.Lock(); delete(m.sessions, sess.roomID); m.mu.Unlock() }()
	selection := initial
	if err := m.saveLiveCover(ctx, sess); err != nil {
		m.setSessionStop(sess, liveStopReason(err), err)
	}
	var lostAt time.Time
	for ctx.Err() == nil {
		m.mu.Lock()
		stopped := sess.stopReason != ""
		m.mu.Unlock()
		if stopped {
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
				err = &bilibili.SiteError{Kind: "storage", Message: "录像目标已存在且未登记，原文件已保留"}
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
		qName := selection.QualityName
		if qName == "" && selection.Quality > 0 {
			qName = bilibili.LiveQualityFallbackName(selection.Quality)
		}
		sess.itemMeta.PendingParts = append(sess.itemMeta.PendingParts, pending)
		sess.itemMeta.Live.Codec, sess.itemMeta.Live.Quality, sess.itemMeta.Live.QualityName = selection.Codec, selection.Quality, qName
		m.mu.Lock()
		sess.codec, sess.quality, sess.qualityName = selection.Codec, selection.Quality, qName
		if st, ok := m.states[sess.roomID]; ok {
			st.Codec, st.Quality, st.QualityName = selection.Codec, selection.Quality, qName
			m.states[sess.roomID] = st
		}
		m.mu.Unlock()
		if err := m.saveSession(sess); err != nil {
			m.setSessionStop(sess, "storage", err)
			break
		}
		sess.segmentDuration, sess.wroteMedia = 0, false
		n, recordErr := m.recordHLSSegment(ctx, sess, selection.URLs, path, true)
		if n > 0 {
			finishCtx, cancel := context.WithTimeout(context.Background(), m.finalizeTimeout)
			summary, conversionErr := finalizeLivePart(finishCtx, path, filepath.Join(sess.itemDir, partRel), sess.segmentDuration)
			cancel()
			if conversionErr != nil {
				addLiveMissing(&sess.itemMeta, pending, "转换失败，临时片段已保留")
			} else {
				sess.itemMeta.Media = mergeLiveMedia(sess.itemMeta.Media, library.MediaFile{Role: "live_segment", Path: partRel, Bytes: summary.FileBytes, SHA256: summary.SHA256, Duration: int(math.Round(summary.DurationSec)), Width: summary.Width, Height: summary.Height})
				sess.itemMeta.PendingParts = removePending(sess.itemMeta.PendingParts, pending)
				clearLiveCheckpoint(&sess.itemMeta, pending)
				clearLiveMissing(&sess.itemMeta, pending)
				sess.itemMeta.Live.Segments = mergeLiveSegment(sess.itemMeta.Live.Segments, library.LiveSegmentInfo{File: partRel, Bytes: summary.FileBytes, SHA256: summary.SHA256, DurationSec: summary.DurationSec, Codec: selection.Codec, Width: summary.Width, Height: summary.Height, Quality: selection.Quality, QualityName: qName})
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
				m.client.ReportAutomaticError(ctx, recordErr)
				if m.subs != nil && (reason == "risk_control" || reason == "rate_limited") {
					m.subs.RecordAutomaticStop(reason)
				}
				m.setSessionStop(sess, reason, recordErr)
				break
			}
			m.mu.Lock()
			sess.lastErr = recordErr.Error()
			m.mu.Unlock()
			if lostAt.IsZero() {
				lostAt = time.Now()
			}
			if m.maxStreamLossDur > 0 && time.Since(lostAt) >= m.maxStreamLossDur {
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
		room, err := m.client.RoomInit(ctx, sess.roomID)
		if err == nil && (room.LiveStatus != 1 || room.LiveTime > 0 && room.LiveTime != sess.itemMeta.Live.LiveSince) {
			m.setSessionStop(sess, "ended", nil)
			break
		}
		if err == nil {
			selection, err = m.client.LiveRoomPlayInfo(ctx, sess.roomID, codecOrder)
		}
		if err != nil {
			if reason := liveStopReason(err); reason != "" {
				m.setSessionStop(sess, reason, err)
				break
			}
			if lostAt.IsZero() {
				lostAt = time.Now()
			}
			if m.maxStreamLossDur > 0 && time.Since(lostAt) >= m.maxStreamLossDur {
				m.setSessionStop(sess, "stream_timeout", err)
				break
			}
			continue
		}
		if selection.LiveStatus != 1 || len(selection.URLs) == 0 {
			m.setSessionStop(sess, "ended", nil)
			break
		}
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
	state := m.states[sess.roomID]
	state.Recording, state.StopReason, state.LastError = false, reason, sess.lastErr
	state.RecordDurationSec, state.RecordBytes, state.RecordSegments = m.nowFunc().Sub(sess.startedAt).Seconds(), sess.bytesTotal, len(sess.segments)
	m.states[sess.roomID] = state
	m.mu.Unlock()
	sess.itemMeta.Live.StopReason, sess.itemMeta.Live.EndedAt, sess.itemMeta.Live.EndTitle = reason, m.nowFunc().UTC().Format(time.RFC3339), state.Title
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
		m.states[sess.roomID] = state
		m.mu.Unlock()
	}
	if m.onState != nil {
		m.onState(state)
	}
}

func (m *Manager) saveSession(sess *recordSession) error {
	sess.itemMeta.Live.LastSequence, sess.itemMeta.Live.HasSequence = sess.lastSequence, sess.hasSequence
	saved, err := m.lib.SaveItem(sess.itemDir, sess.itemMeta)
	if err == nil {
		sess.itemMeta = saved
	}
	return err
}

func (m *Manager) setSessionStop(sess *recordSession, reason string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess.stopReason = reason
	if err != nil {
		sess.lastErr = err.Error()
	}
}

func liveStopReason(err error) string {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return "storage"
	}
	switch bilibili.KindOf(err) {
	case bilibili.ErrKindRiskControl, bilibili.ErrKindRateLimited, bilibili.ErrKindAuthRequired, bilibili.ErrKindQuota, bilibili.ErrKindBackoff, "storage", bilibili.ErrKindSchemaChanged, bilibili.ErrKindBlocked:
		return bilibili.KindOf(err)
	}
	return ""
}

func (m *Manager) recordHLSSegment(ctx context.Context, sess *recordSession, urls []string, partPath string, isFMP4 bool) (written int64, result error) {
	if len(urls) == 0 || !isFMP4 {
		return 0, &bilibili.SiteError{Kind: bilibili.ErrKindUnavailable, Message: "没有可录制的 HLS fMP4 流"}
	}
	f, err := os.OpenFile(partPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		return 0, err
	}
	defer func() {
		if err := f.Sync(); err != nil && result == nil {
			result = err
		}
		if err := f.Close(); err != nil && result == nil {
			result = err
		}
	}()
	playlistURL, initURI := urls[0], ""
	followedVariant := false
	var initBytes []byte
	lastNew := time.Now()
	for ctx.Err() == nil {
		if m.freeBytesFunc != nil && m.minFreeBytes > 0 {
			free, err := m.freeBytesFunc(sess.itemDir)
			if err != nil {
				return written, err
			}
			if free < m.minFreeBytes {
				return written, &bilibili.SiteError{Kind: "storage", Message: "磁盘空间不足 1 GiB，已停止录制"}
			}
		}
		raw, err := m.fetchLiveLimitedBytes(ctx, playlistURL, maxM3U8PlaylistBytes)
		if err != nil {
			if written == 0 && len(urls) > 1 && playlistURL == urls[0] && liveStopReason(err) == "" {
				playlistURL = urls[1]
				continue
			}
			return written, err
		}
		playlist := parseM3U8PlaylistDetails(string(raw), playlistURL)
		if len(playlist.variants) > 0 && len(playlist.segments) == 0 && playlist.valid {
			// Some nodes answer with a multivariant playlist. Follow it once; a
			// second level of indirection is treated as a schema change.
			variant, ok := playlist.bestVariant()
			if followedVariant || !ok {
				return written, &bilibili.SiteError{Kind: bilibili.ErrKindSchemaChanged, Message: "直播播放列表结构不完整（多级索引）"}
			}
			if err := m.client.ValidateMediaURL(variant); err != nil {
				return written, &bilibili.SiteError{Kind: bilibili.ErrKindSchemaChanged, Message: "直播播放列表指向不受支持的节点"}
			}
			playlistURL, followedVariant = variant, true
			continue
		}
		if playlist.encrypted {
			return written, &bilibili.SiteError{Kind: bilibili.ErrKindBlocked, Message: "不支持加密的直播分片"}
		}
		if !playlist.valid {
			return written, &bilibili.SiteError{Kind: bilibili.ErrKindSchemaChanged, Message: "直播播放列表结构不完整：" + playlist.invalidReason}
		}
		if sess.hasSequence && len(playlist.segments) > 0 && playlist.segments[len(playlist.segments)-1].seq < sess.lastSequence {
			// A sequence reset starts a new part even when the same init URI is
			// reused. Keeping the old watermark would skip the new broadcast data.
			sess.lastSequence, sess.hasSequence = 0, false
			return written, errSegmentRollover
		}
		newCount := 0
		if sess.wroteMedia && playlist.mapURI == initURI {
			for _, segment := range playlist.segments {
				if !sess.hasSequence || segment.seq > sess.lastSequence {
					freshInit, err := m.fetchLiveBytes(ctx, segment.mapURI)
					if err != nil {
						return written, err
					}
					if !bytes.Equal(freshInit, initBytes) {
						return written, errSegmentRollover
					}
					break
				}
			}
		}
		for _, segment := range playlist.segments {
			if sess.hasSequence && segment.seq <= sess.lastSequence {
				continue
			}
			if sess.wroteMedia && (segment.discontinuity || sess.hasSequence && segment.seq-sess.lastSequence > 4 || segment.mapURI != initURI) {
				return written, errSegmentRollover
			}
			if initURI == "" {
				initBytes, err = m.fetchLiveBytes(ctx, segment.mapURI)
				if err != nil {
					return written, err
				}
				if !validLiveInit(initBytes) {
					return written, &bilibili.SiteError{Kind: bilibili.ErrKindSchemaChanged, Message: "直播初始化分片缺少有效视频轨"}
				}
				initURI = segment.mapURI
				n, err := f.Write(initBytes)
				written += int64(n)
				m.addSessionBytes(sess, int64(n))
				if err != nil {
					return written, err
				}
			}
			if segment.discontinuity && sess.wroteMedia {
				return written, errSegmentRollover
			}
			fragment, err := m.fetchLiveBytes(ctx, segment.uri)
			if err != nil {
				return written, err
			}
			if !validLiveFragment(fragment) {
				return written, &bilibili.SiteError{Kind: bilibili.ErrKindSchemaChanged, Message: "直播媒体分片不完整"}
			}
			n, err := f.Write(fragment)
			written += int64(n)
			m.addSessionBytes(sess, int64(n))
			if err != nil {
				return written, err
			}
			// Persist complete fragments only; fsync never leaves half a fragment as
			// the checkpoint used by startup recovery.
			if err := f.Sync(); err != nil {
				return written, err
			}
			sess.lastSequence, sess.hasSequence, sess.wroteMedia = segment.seq, true, true
			sess.segmentDuration += segment.duration
			sess.itemMeta.Live.LastSequence, sess.itemMeta.Live.HasSequence = sess.lastSequence, sess.hasSequence
			sess.itemMeta.Live.PendingFile = filepath.Base(partPath)
			sess.itemMeta.Live.PendingBytes, sess.itemMeta.Live.PendingDuration = written, sess.segmentDuration
			if _, err := m.lib.SaveRecordingCheckpoint(sess.itemDir, sess.itemMeta); err != nil {
				return written, err
			}
			newCount++
		}
		if newCount > 0 {
			lastNew = time.Now()
		} else if m.noSegmentTimeout > 0 && time.Since(lastNew) >= m.noSegmentTimeout {
			return written, &bilibili.SiteError{Kind: bilibili.ErrKindNetwork, Message: "超过 10 秒未收到新直播分片"}
		}
		if playlist.endList {
			return written, nil
		}
		wait := m.hlsPollInterval
		if wait <= 0 {
			wait = time.Duration(math.Max(1, math.Min(5, playlist.targetDuration)) * float64(time.Second))
		}
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
	}
	return written, ctx.Err()
}

func validLiveInit(raw []byte) bool {
	file, err := mp4.DecodeFile(bytes.NewReader(raw), mp4.WithDecodeFlags(mp4.DecStartOnMoof))
	if err != nil || file == nil {
		return false
	}
	moov := file.Moov
	if file.Init != nil {
		moov = file.Init.Moov
	}
	if moov == nil {
		return false
	}
	for _, track := range moov.Traks {
		if track.Mdia != nil && track.Mdia.Hdlr != nil && track.Mdia.Hdlr.HandlerType == "vide" {
			return true
		}
	}
	return false
}

func validLiveFragment(raw []byte) bool {
	file, err := mp4.DecodeFile(bytes.NewReader(raw), mp4.WithDecodeFlags(mp4.DecStartOnMoof))
	if err != nil || file == nil || len(file.Segments) == 0 {
		return false
	}
	for _, segment := range file.Segments {
		for _, fragment := range segment.Fragments {
			if fragment.Moof != nil && fragment.Mdat != nil && len(fragment.Mdat.Data) > 0 {
				return true
			}
		}
	}
	return false
}

type contextFile struct {
	*os.File
	ctx context.Context
}

func (f contextFile) Read(p []byte) (int, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.File.Read(p)
}
func (f contextFile) Write(p []byte) (int, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.File.Write(p)
}
func (f contextFile) Seek(offset int64, whence int) (int64, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.File.Seek(offset, whence)
}

// Media samples stay on disk while mp4ff reads metadata and copies them to the
// progressive output. The original .part remains until its manifest is saved.
func finalizeLivePart(ctx context.Context, partPath, finalPath string, expected float64) (download.MuxTrackSummary, error) {
	f, err := os.Open(partPath)
	if err != nil {
		return download.MuxTrackSummary{}, err
	}
	defer f.Close()
	source := contextFile{File: f, ctx: ctx}
	parsed, err := mp4.DecodeFile(source, mp4.WithDecodeMode(mp4.DecModeLazyMdat), mp4.WithDecodeFlags(mp4.DecStartOnMoof))
	if err != nil {
		return download.MuxTrackSummary{}, err
	}
	out, err := os.CreateTemp(filepath.Dir(finalPath), ".trace-live-*.part")
	if err != nil {
		return download.MuxTrackSummary{}, err
	}
	name := out.Name()
	defer os.Remove(name)
	if err := mp4.Defragment(parsed, source, contextFile{File: out, ctx: ctx}); err != nil {
		_ = out.Close()
		return download.MuxTrackSummary{}, err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return download.MuxTrackSummary{}, err
	}
	if err := out.Close(); err != nil {
		return download.MuxTrackSummary{}, err
	}
	summary, err := download.InspectMP4FileContext(ctx, name)
	if err != nil {
		return summary, err
	}
	video := false
	for _, handler := range summary.Handlers {
		video = video || handler == "vide"
	}
	if !video || !summary.MoovBeforeMdat || summary.DurationSec <= 0 || expected > 0 && math.Abs(summary.DurationSec-expected) > 0.1 {
		return summary, errors.New("直播录像轨道或时长校验失败")
	}
	if err := ctx.Err(); err != nil {
		return summary, err
	}
	if err := os.Rename(name, finalPath); err != nil {
		return summary, err
	}
	return summary, nil
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

func clearLiveCheckpoint(meta *library.ItemMetadata, rel string) {
	if meta.Live != nil && meta.Live.PendingFile == rel {
		meta.Live.PendingFile, meta.Live.PendingBytes, meta.Live.PendingDuration = "", 0, 0
	}
}

var _ io.ReadSeeker = contextFile{}
