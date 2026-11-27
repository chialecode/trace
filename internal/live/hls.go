package live

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
)

// hlsSequenceGapSlackSec is how much a sequence gap may exceed the time since
// the previous segment and still count as lost segments (EXTINF rounding and
// playlist windows), rather than as a restarted stream's new numbering.
const hlsSequenceGapSlackSec = 30.0

// transientHLSSchemaError wraps a schema_changed SiteError triggered by a
// malformed M3U8 playlist or truncated fMP4 media fragment so runSessionLoop
// can attempt a bounded stream reconnect when media has already been recorded.
type transientHLSSchemaError struct {
	err *bilibili.SiteError
}

func (e *transientHLSSchemaError) Error() string { return e.err.Error() }
func (e *transientHLSSchemaError) Unwrap() error { return e.err }

func isTransientHLSSchemaError(err error) bool {
	var te *transientHLSSchemaError
	return errors.As(err, &te)
}

func (m *Manager) hlsTimeouts() (playlistTimeout, segTimeout, idleTimeout, cpInterval time.Duration) {
	m.mu.Lock()
	playlistTimeout = m.hlsPlaylistTimeout
	segTimeout = m.hlsSegmentTimeout
	idleTimeout = m.hlsIdleTimeout
	cpInterval = m.checkpointInterval
	m.mu.Unlock()
	if playlistTimeout <= 0 {
		playlistTimeout = defaultHLSPlaylistTimeout
	}
	if segTimeout <= 0 {
		segTimeout = defaultHLSSegmentTimeout
	}
	if idleTimeout <= 0 {
		idleTimeout = defaultHLSIdleTimeout
	}
	if cpInterval <= 0 {
		cpInterval = defaultCheckpointInterval
	}
	return playlistTimeout, segTimeout, idleTimeout, cpInterval
}

func httpStatusCodeOf(err error) int {
	var se *site.Error
	if errors.As(err, &se) {
		return se.HTTPStatus
	}
	return 0
}

func isTransientHLSSegmentNetErr(err error) bool {
	if err == nil || liveStopReason(err) != "" {
		return false
	}
	var se *site.Error
	if !errors.As(err, &se) {
		return false
	}
	return se.Kind == site.ErrKindNetwork && se.HTTPStatus == 0
}

func isGlobalLiveStopErr(err error) bool {
	switch liveStopReason(err) {
	case bilibili.ErrKindRiskControl, bilibili.ErrKindRateLimited, bilibili.ErrKindAuthRequired, bilibili.ErrKindQuota, bilibili.ErrKindBackoff, "storage":
		return true
	default:
		return false
	}
}

func (m *Manager) fetchLiveBytes(ctx context.Context, targetURL string, allowScheduled bool) ([]byte, error) {
	_, segTimeout, idleTimeout, _ := m.hlsTimeouts()
	data, _, err := m.fetchLiveLimitedBytesWithTimeout(ctx, targetURL, 32*1024*1024, segTimeout, idleTimeout, allowScheduled)
	return data, err
}

func (m *Manager) fetchLiveLimitedBytes(ctx context.Context, targetURL string, maxBytes int64) ([]byte, error) {
	data, _, err := m.fetchLivePlaylistBytes(ctx, targetURL, maxBytes, false)
	return data, err
}

func (m *Manager) fetchLivePlaylistBytes(ctx context.Context, targetURL string, maxBytes int64, allowScheduled bool) ([]byte, string, error) {
	playlistTimeout, _, idleTimeout, _ := m.hlsTimeouts()
	return m.fetchLiveLimitedBytesWithTimeout(ctx, targetURL, maxBytes, playlistTimeout, idleTimeout, allowScheduled)
}

func (m *Manager) fetchLiveLimitedBytesWithTimeout(
	ctx context.Context,
	targetURL string,
	maxBytes int64,
	totalTimeout time.Duration,
	idleTimeout time.Duration,
	allowScheduled bool,
) ([]byte, string, error) {
	if err := m.client.ValidateLiveMediaURL(targetURL, allowScheduled); err != nil {
		return nil, targetURL, err
	}
	reqCtx := ctx
	var cancel context.CancelFunc
	if totalTimeout > 0 {
		reqCtx, cancel = context.WithTimeout(ctx, totalTimeout)
		defer cancel()
	}
	mediaClient, _ := m.client.LiveMediaHTTPClient()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, targetURL, &site.Error{
			Site:    site.Bilibili,
			Kind:    site.ErrKindNetwork,
			Message: "创建直播流请求失败",
			Err:     site.UnwrapURLError(err),
		}
	}
	site.BilibiliLiveTransport(m.client.AllowLocalhost()).ApplyHeaders(req)

	resp, err := mediaClient.Do(req)
	if err != nil {
		var se *site.Error
		if errors.As(err, &se) {
			return nil, targetURL, se
		}
		return nil, targetURL, &site.Error{
			Site:    site.Bilibili,
			Kind:    site.ErrKindNetwork,
			Message: "请求直播流节点失败",
			Err:     site.UnwrapURLError(err),
		}
	}
	defer resp.Body.Close()
	finalURL := targetURL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	if resp.StatusCode != http.StatusOK {
		// Drain short error bodies so HTTP/1.1 keep-alive connections remain reusable.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	}
	if resp.StatusCode == http.StatusPreconditionFailed {
		return nil, finalURL, &bilibili.SiteError{
			Site:       site.Bilibili,
			Kind:       bilibili.ErrKindRiskControl,
			HTTPStatus: resp.StatusCode,
			Message:    "直播请求触发风控，已停止录制",
		}
	}
	if resp.StatusCode != http.StatusOK {
		kind := bilibili.ErrKindNetwork
		if resp.StatusCode == http.StatusTooManyRequests {
			kind = bilibili.ErrKindRateLimited
		}
		if resp.StatusCode == http.StatusUnauthorized {
			kind = bilibili.ErrKindAuthRequired
		}
		return nil, finalURL, &bilibili.SiteError{
			Site:       site.Bilibili,
			Kind:       kind,
			HTTPStatus: resp.StatusCode,
			Message:    "直播节点请求失败",
		}
	}
	reader := m.client.WrapMediaReader(resp.Body)
	if idleTimeout > 0 {
		timeoutSec := max(1, int(idleTimeout.Seconds()))
		reader = site.WrapIdleReadCloser(reader, idleTimeout, func() error {
			return &site.Error{
				Site:    site.Bilibili,
				Kind:    site.ErrKindNetwork,
				Message: fmt.Sprintf("直播分片超过 %d 秒无数据返回", timeoutSec),
			}
		})
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		var se *site.Error
		if errors.As(err, &se) {
			return nil, finalURL, se
		}
		return nil, finalURL, &site.Error{
			Site:    site.Bilibili,
			Kind:    site.ErrKindNetwork,
			Message: "读取直播流数据失败",
			Err:     site.UnwrapURLError(err),
		}
	}
	if int64(len(data)) > maxBytes {
		return nil, finalURL, &bilibili.SiteError{
			Site:    site.Bilibili,
			Kind:    bilibili.ErrKindSchemaChanged,
			Message: "直播响应超过大小上限",
		}
	}
	return data, finalURL, nil
}

func (m *Manager) recordHLSSegment(ctx context.Context, sess *recordSession, urls []string, partPath string, isFMP4 bool) (written int64, result error) {
	if len(urls) == 0 || !isFMP4 {
		return 0, &bilibili.SiteError{Site: site.Bilibili, Kind: bilibili.ErrKindUnavailable, Message: "没有可录制的 HLS fMP4 流"}
	}
	if err := os.MkdirAll(filepath.Dir(partPath), 0o700); err != nil {
		return 0, err
	}
	f, err := os.OpenFile(partPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return 0, err
	}

	partBase := filepath.Base(partPath)
	var writeFailed bool
	var checkpointedBytes int64
	var lastCheckpointAt time.Time
	var lastCPSeq int64
	var lastCPHasSeq bool
	var lastCPSeqAt time.Time
	var lastCPDur float64

	flushCheckpoint := func() error {
		syncStart := time.Now()
		if err := f.Sync(); err != nil {
			return err
		}
		if dur := time.Since(syncStart); dur >= slowLiveIOThreshold {
			m.logEvent("WARN", "live slow_io: op=hls_fsync site=%s room=%s part=%s bytes=%d dur_ms=%d",
				liveLogSite(sess), liveLogRoom(sess), partBase, written, dur.Milliseconds())
		}
		if err := m.saveCheckpoint(sess, func(meta *library.ItemMetadata) {
			meta.Live.LastSequence, meta.Live.HasSequence = lastCPSeq, lastCPHasSeq
			if !lastCPSeqAt.IsZero() {
				meta.Live.LastSequenceAt = lastCPSeqAt.UTC().Format(time.RFC3339Nano)
			}
			meta.Live.PendingFile = partBase
			meta.Live.PendingBytes, meta.Live.PendingDuration = written, lastCPDur
		}); err != nil {
			return err
		}
		checkpointedBytes = written
		lastCheckpointAt = m.now()
		return nil
	}

	defer func() {
		if written > checkpointedBytes && !writeFailed && lastCPHasSeq {
			if cpErr := flushCheckpoint(); cpErr != nil && result == nil {
				result = cpErr
			}
		} else if written > 0 {
			if sErr := f.Sync(); sErr != nil && result == nil {
				result = sErr
			}
		}
		if cErr := f.Close(); cErr != nil && result == nil {
			result = cErr
		}
	}()

	urlIdx := 0
	playlistURL, initURI := urls[0], ""
	followedVariant := false
	var initBytes []byte
	initWritten := false
	lastNew := m.now()
	staleLowerSeqPolls := 0
	consecutiveSeg404 := 0
	skipped404Seq := int64(-1)

	noteCandidateFailed := func(failedURL string) {
		if ctx.Err() != nil {
			return
		}
		m.noteSessionFailedHost(sess, failedURL)
		if failedURL != urls[urlIdx] {
			m.noteSessionFailedHost(sess, urls[urlIdx])
		}
	}

	tryNextCandidate := func(failedURL, reason string) bool {
		if written > 0 || urlIdx+1 >= len(urls) {
			return false
		}
		noteCandidateFailed(failedURL)
		prevHost := liveLogHost(urls[urlIdx])
		urlIdx++
		playlistURL = urls[urlIdx]
		followedVariant = false
		initURI = ""
		initBytes = nil
		initWritten = false
		consecutiveSeg404 = 0
		skipped404Seq = -1
		staleLowerSeqPolls = 0
		m.logEvent("WARN", "live hls host_fallback: site=%s room=%s part=%s reason=%s from_host=%s to_host=%s",
			liveLogSite(sess), liveLogRoom(sess), partBase, reason, prevHost, liveLogHost(playlistURL))
		return true
	}

	isExpiredHealthyHost := func(err error) bool {
		if httpStatusCodeOf(err) != http.StatusForbidden {
			return false
		}
		m.mu.Lock()
		served := sess.wroteMedia || sess.selectionWroteMedia
		m.mu.Unlock()
		return served
	}

	for ctx.Err() == nil {
		pollWallStart := time.Now()
		skipPollSleep := false

		if err := m.checkDiskSpace(sess.itemDir, site.Bilibili); err != nil {
			return written, err
		}
		raw, effectivePlaylistURL, err := m.fetchLivePlaylistBytes(ctx, playlistURL, maxM3U8PlaylistBytes, followedVariant)
		if err != nil {
			expiredHealthy := isExpiredHealthyHost(err)
			if !expiredHealthy && !isGlobalLiveStopErr(err) && tryNextCandidate(playlistURL, "playlist_fetch") {
				continue
			}
			if !expiredHealthy && !isGlobalLiveStopErr(err) {
				noteCandidateFailed(playlistURL)
			}
			return written, err
		}
		playlist := parseM3U8PlaylistDetails(string(raw), effectivePlaylistURL)
		if len(playlist.variants) > 0 && len(playlist.segments) == 0 && playlist.valid {
			// Some nodes answer with a multivariant playlist. Follow it once; a
			// second level of indirection is treated as a schema change.
			variant, ok := playlist.bestVariantInfo()
			if followedVariant || !ok {
				if tryNextCandidate(playlistURL, "nested_multivariant") {
					continue
				}
				return written, &bilibili.SiteError{Site: site.Bilibili, Kind: bilibili.ErrKindSchemaChanged, Message: "直播播放列表结构不完整（多级索引）"}
			}
			if err := m.client.ValidateLiveMediaURL(variant.uri, true); err != nil {
				if tryNextCandidate(playlistURL, "blocked_variant") {
					continue
				}
				return written, &bilibili.SiteError{Site: site.Bilibili, Kind: bilibili.ErrKindSchemaChanged, Message: "直播播放列表指向不受支持的节点"}
			}
			if variant.bandwidth > 0 {
				m.setSessionNominalBitrateBps(sess, variant.bandwidth)
			}
			playlistURL, followedVariant = variant.uri, true
			continue
		}
		if playlist.encrypted {
			return written, &bilibili.SiteError{Site: site.Bilibili, Kind: bilibili.ErrKindBlocked, Message: "不支持加密的直播分片"}
		}
		if !playlist.valid {
			if tryNextCandidate(playlistURL, "invalid_playlist") {
				continue
			}
			noteCandidateFailed(playlistURL)
			return written, &transientHLSSchemaError{
				err: &bilibili.SiteError{Site: site.Bilibili, Kind: bilibili.ErrKindSchemaChanged, Message: "直播播放列表结构不完整：" + playlist.invalidReason},
			}
		}
		m.markSessionReconnected(sess, m.now(), false, partBase)

		m.mu.Lock()
		lastSeq, hasSeq, wroteMedia, lastSeqAt := sess.lastSequence, sess.hasSequence, sess.wroteMedia, sess.lastSequenceAt
		m.mu.Unlock()

		if hasSeq && len(playlist.segments) > 0 {
			newestSeq := playlist.segments[len(playlist.segments)-1].seq
			if newestSeq < lastSeq {
				staleLowerSeqPolls++
				if playlist.hasMediaSequence && (lastSeq-newestSeq > 10 || staleLowerSeqPolls >= 3) {
					// A genuine sequence reset starts a new part even when the same init URI
					// is reused. Keeping the old watermark would skip the new broadcast data.
					if written > checkpointedBytes && !writeFailed && lastCPHasSeq {
						if cpErr := flushCheckpoint(); cpErr != nil {
							return written, cpErr
						}
					}
					m.logEvent("INFO", "live hls rollover: site=%s room=%s part=%s reason=seq_reset from_seq=%d to_seq=%d",
						liveLogSite(sess), liveLogRoom(sess), partBase, lastSeq, newestSeq)
					m.mu.Lock()
					sess.lastSequence, sess.hasSequence, sess.lastSequenceAt = 0, false, time.Time{}
					m.mu.Unlock()
					return written, errSegmentRollover
				}
				m.logEvent("WARN", "live hls stale_seq_ignored: site=%s room=%s part=%s from_seq=%d to_seq=%d count=%d host=%s",
					liveLogSite(sess), liveLogRoom(sess), partBase, lastSeq, newestSeq, staleLowerSeqPolls, liveLogHost(playlistURL))
			} else {
				staleLowerSeqPolls = 0
			}
		}

		_, _, _, cpInterval := m.hlsTimeouts()
		newCount := 0
		switchedCandidate := false
		for idx, segment := range playlist.segments {
			if hasSeq && segment.seq <= lastSeq {
				continue
			}
			if skipped404Seq >= 0 && segment.seq <= skipped404Seq {
				continue
			}
			if wroteMedia && (segment.discontinuity || hasSeq && segment.seq-lastSeq > 4 || segment.mapURI != initURI) {
				reason := "init_uri_changed"
				if segment.discontinuity {
					reason = "discontinuity"
				} else if hasSeq && segment.seq-lastSeq > 4 {
					reason = "seq_gap"
				}
				m.logEvent("INFO", "live hls rollover: site=%s room=%s part=%s reason=%s from_seq=%d to_seq=%d",
					liveLogSite(sess), liveLogRoom(sess), partBase, reason, lastSeq, segment.seq)
				return written, errSegmentRollover
			}
			if !initWritten && initURI != "" && segment.mapURI != initURI {
				initURI = ""
				initBytes = nil
			}
			if initURI == "" {
				fetchedInit, initErr := m.fetchLiveBytes(ctx, segment.mapURI, true)
				if initErr != nil {
					expiredHealthy := isExpiredHealthyHost(initErr)
					if !expiredHealthy && !isGlobalLiveStopErr(initErr) && tryNextCandidate(segment.mapURI, "init_fetch") {
						switchedCandidate = true
						break
					}
					if !expiredHealthy && !isGlobalLiveStopErr(initErr) {
						noteCandidateFailed(segment.mapURI)
					}
					return written, initErr
				}
				if !validLiveInit(fetchedInit) {
					if tryNextCandidate(segment.mapURI, "invalid_init") {
						switchedCandidate = true
						break
					}
					noteCandidateFailed(segment.mapURI)
					return written, &transientHLSSchemaError{
						err: &bilibili.SiteError{Site: site.Bilibili, Kind: bilibili.ErrKindSchemaChanged, Message: "直播初始化分片缺少有效视频轨"},
					}
				}
				initURI = segment.mapURI
				initBytes = fetchedInit
			}
			fetchStart := m.now()
			fragment, err := m.fetchLiveBytes(ctx, segment.uri, true)
			if err != nil && ctx.Err() == nil && isTransientHLSSegmentNetErr(err) {
				retryTimer := time.NewTimer(200 * time.Millisecond)
				select {
				case <-ctx.Done():
					retryTimer.Stop()
					return written, ctx.Err()
				case <-retryTimer.C:
				}
				fetchStart = m.now()
				fragment, err = m.fetchLiveBytes(ctx, segment.uri, true)
			}
			fetchEnd := m.now()
			if err != nil {
				status := httpStatusCodeOf(err)
				if (status == http.StatusNotFound || status == http.StatusGone) && (wroteMedia || idx+1 < len(playlist.segments)) {
					consecutiveSeg404++
					if consecutiveSeg404 < 3 {
						skipped404Seq = segment.seq
						m.logEvent("WARN", "live hls segment_skip: site=%s room=%s part=%s seq=%d status=%d host=%s",
							liveLogSite(sess), liveLogRoom(sess), partBase, segment.seq, status, liveLogHost(segment.uri))
						if idx+1 < len(playlist.segments) {
							continue
						}
						skipPollSleep = true
						break
					}
				}
				expiredHealthy := isExpiredHealthyHost(err)
				if !expiredHealthy && !isGlobalLiveStopErr(err) && tryNextCandidate(segment.uri, "segment_fetch") {
					switchedCandidate = true
					break
				}
				if !expiredHealthy && !isGlobalLiveStopErr(err) {
					noteCandidateFailed(segment.uri)
				}
				return written, err
			}
			consecutiveSeg404 = 0
			skipped404Seq = -1
			if !validLiveFragment(fragment) {
				if tryNextCandidate(segment.uri, "invalid_fragment") {
					switchedCandidate = true
					break
				}
				noteCandidateFailed(segment.uri)
				return written, &transientHLSSchemaError{
					err: &bilibili.SiteError{Site: site.Bilibili, Kind: bilibili.ErrKindSchemaChanged, Message: "直播媒体分片不完整"},
				}
			}
			if !initWritten {
				nInit, wErr := f.Write(initBytes)
				written += int64(nInit)
				m.addSessionBytes(sess, int64(nInit))
				if wErr != nil {
					writeFailed = true
					return written, wErr
				}
				initWritten = true
				initBytes = nil
			}
			n, err := f.Write(fragment)
			written += int64(n)
			m.addSessionBytes(sess, int64(n))
			if err != nil {
				writeFailed = true
				return written, err
			}

			incidentRecorded := false
			if !segment.discontinuity && hasSeq && segment.seq > lastSeq+1 {
				dropped := int(segment.seq - lastSeq - 1)
				var segDur float64
				m.mu.Lock()
				if sess.extinfSamples > 0 && sess.avgExtinfSec > 0 {
					segDur = sess.avgExtinfSec
				}
				m.mu.Unlock()
				if segDur <= 0 && segment.duration > 0 {
					segDur = segment.duration
				}
				if segDur <= 0 && playlist.targetDuration > 0 {
					segDur = playlist.targetDuration
				}
				if segDur <= 0 {
					segDur = 1.0
				}
				// A gap longer than the wall-clock time since the previous segment
				// cannot be lost media: the restarted stream numbers its segments
				// from a new base (seen jumping by 100 million after a reconnect).
				// Never bypass this guard when lastSequenceAt is unset; fall back to
				// persisted session anchors and treat unanchored jumps as rebases.
				refTime := lastSeqAt
				if refTime.IsZero() {
					m.mu.Lock()
					if sess.itemMeta.Live != nil {
						if ts, parseErr := time.Parse(time.RFC3339Nano, sess.itemMeta.Live.LastSequenceAt); parseErr == nil && !ts.IsZero() {
							refTime = ts
						} else if ts, parseErr := time.Parse(time.RFC3339, sess.itemMeta.Live.EndedAt); parseErr == nil && !ts.IsZero() {
							refTime = ts
						} else if ts, parseErr := time.Parse(time.RFC3339, sess.itemMeta.Live.StartedAt); parseErr == nil && !ts.IsZero() {
							refTime = ts
						} else if sess.itemMeta.Live.LiveSince > 0 {
							refTime = time.Unix(sess.itemMeta.Live.LiveSince, 0)
						}
					}
					if refTime.IsZero() {
						refTime = sess.startedAt
					}
					m.mu.Unlock()
				}
				lostSec := float64(dropped) * segDur
				if !refTime.IsZero() {
					elapsedSec := fetchEnd.Sub(refTime).Seconds()
					if elapsedSec < 0 {
						elapsedSec = 0
					}
					if lostSec <= elapsedSec+hlsSequenceGapSlackSec {
						atStr := fetchEnd.UTC().Format(time.RFC3339)
						m.recordSessionIncident(sess, library.LiveIncident{
							Kind:         library.LiveIncidentLostSegments,
							At:           atStr,
							EndedAt:      atStr,
							DurationSec:  lostSec,
							LostSegments: dropped,
							Count:        1,
							Part:         partBase,
							Reason:       fmt.Sprintf("HLS 媒体分片序号跳号，丢失 %d 段", dropped),
						}, true)
						incidentRecorded = true
						m.logEvent("WARN", "live hls lost_segments: site=%s room=%s part=%s dropped=%d from_seq=%d to_seq=%d est_sec=%.1f elapsed_sec=%.1f host=%s",
							liveLogSite(sess), liveLogRoom(sess), partBase, dropped, lastSeq, segment.seq, roundDurationSec(lostSec), roundDurationSec(elapsedSec), liveLogHost(segment.uri))
					}
				}
			}
			m.observeTransferSpeed(sess, fetchStart, fetchEnd, int64(len(fragment)), segment.duration, partBase)

			lastSeq, hasSeq, wroteMedia, lastSeqAt = segment.seq, true, true, fetchEnd
			sess.segmentDuration += segment.duration
			dur := sess.segmentDuration
			m.mu.Lock()
			sess.lastSequence, sess.hasSequence, sess.wroteMedia, sess.selectionWroteMedia, sess.lastSequenceAt = lastSeq, hasSeq, wroteMedia, true, lastSeqAt
			if segment.duration > 0 {
				if sess.extinfSamples == 0 || sess.avgExtinfSec <= 0 {
					sess.avgExtinfSec = segment.duration
					sess.extinfSamples = 1
				} else {
					const maxExtinfWindow = 20
					n := sess.extinfSamples
					if n >= maxExtinfWindow {
						n = maxExtinfWindow - 1
					}
					sess.avgExtinfSec = (sess.avgExtinfSec*float64(n) + segment.duration) / float64(n+1)
					if sess.extinfSamples < maxExtinfWindow {
						sess.extinfSamples++
					}
				}
			}
			m.mu.Unlock()

			m.commitSessionPart(sess, written, dur)
			lastCPSeq, lastCPHasSeq, lastCPSeqAt, lastCPDur = lastSeq, hasSeq, lastSeqAt, dur

			// Persist complete fragments only; throttle fsync + item.json checkpointing
			// to first segment, every checkpointInterval, on incidents, and on part exit.
			shouldCheckpoint := checkpointedBytes == 0 || incidentRecorded || cpInterval <= 0 || m.now().Sub(lastCheckpointAt) >= cpInterval
			if shouldCheckpoint {
				if err := flushCheckpoint(); err != nil {
					return written, err
				}
			}
			newCount++
		}
		if switchedCandidate {
			continue
		}
		now := m.now()
		m.mu.Lock()
		noSegTimeout := m.noSegmentTimeout
		pollWait := m.hlsPollInterval
		m.mu.Unlock()

		if newCount > 0 {
			lastNew = now
		} else if noSegTimeout > 0 && now.Sub(lastNew) >= noSegTimeout {
			m.noteSessionFailedHost(sess, playlistURL)
			return written, &bilibili.SiteError{Site: site.Bilibili, Kind: bilibili.ErrKindNetwork, Message: "超过 10 秒未收到新直播分片"}
		}
		if playlist.endList {
			return written, nil
		}
		if skipPollSleep {
			continue
		}
		var sleepDur time.Duration
		if pollWait > 0 {
			sleepDur = pollWait
		} else {
			td := playlist.targetDuration
			if td <= 0 {
				td = 1.0
			}
			target := time.Duration(td * 0.5 * float64(time.Second))
			if target < 500*time.Millisecond {
				target = 500 * time.Millisecond
			}
			if target > 2*time.Second {
				target = 2 * time.Second
			}
			sleepDur = target - time.Since(pollWallStart)
			if sleepDur <= 0 {
				if newCount > 0 {
					continue
				}
				sleepDur = 50 * time.Millisecond
			}
		}
		timer := time.NewTimer(sleepDur)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
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
