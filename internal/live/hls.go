package live

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
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

func (m *Manager) fetchLiveBytes(ctx context.Context, targetURL string) ([]byte, error) {
	return m.fetchLiveLimitedBytes(ctx, targetURL, 32*1024*1024)
}

func (m *Manager) fetchLiveLimitedBytes(ctx context.Context, targetURL string, maxBytes int64) ([]byte, error) {
	if err := m.client.ValidateMediaURL(targetURL); err != nil {
		return nil, err
	}
	mediaClient, _ := m.client.MediaHTTPClient()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, &site.Error{
			Site:    site.Bilibili,
			Kind:    site.ErrKindNetwork,
			Message: "创建直播流请求失败",
			Err:     site.UnwrapURLError(err),
		}
	}
	req.Header.Set("User-Agent", bilibili.DefaultUserAgent)
	req.Header.Set("Referer", "https://live.bilibili.com/")
	req.Header.Set("Origin", "https://live.bilibili.com")

	resp, err := mediaClient.Do(req)
	if err != nil {
		var se *site.Error
		if errors.As(err, &se) {
			return nil, se
		}
		return nil, &site.Error{
			Site:    site.Bilibili,
			Kind:    site.ErrKindNetwork,
			Message: "请求直播流节点失败",
			Err:     site.UnwrapURLError(err),
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusPreconditionFailed {
		return nil, &bilibili.SiteError{
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
		return nil, &bilibili.SiteError{
			Site:       site.Bilibili,
			Kind:       kind,
			HTTPStatus: resp.StatusCode,
			Message:    "直播节点请求失败",
		}
	}
	reader := m.client.WrapMediaReader(resp.Body)
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		var se *site.Error
		if errors.As(err, &se) {
			return nil, se
		}
		return nil, &site.Error{
			Site:    site.Bilibili,
			Kind:    site.ErrKindNetwork,
			Message: "读取直播流数据失败",
			Err:     site.UnwrapURLError(err),
		}
	}
	if int64(len(data)) > maxBytes {
		return nil, &bilibili.SiteError{
			Site:    site.Bilibili,
			Kind:    bilibili.ErrKindSchemaChanged,
			Message: "直播响应超过大小上限",
		}
	}
	return data, nil
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
	defer func() {
		if err := f.Sync(); err != nil && result == nil {
			result = err
		}
		if err := f.Close(); err != nil && result == nil {
			result = err
		}
	}()

	urlIdx := 0
	playlistURL, initURI := urls[0], ""
	followedVariant := false
	var initBytes []byte
	partBase := filepath.Base(partPath)
	lastNew := m.now()

	for ctx.Err() == nil {
		if err := m.checkDiskSpace(sess.itemDir, site.Bilibili); err != nil {
			return written, err
		}
		raw, err := m.fetchLiveLimitedBytes(ctx, playlistURL, maxM3U8PlaylistBytes)
		if err != nil {
			if written == 0 && !followedVariant && urlIdx+1 < len(urls) && liveStopReason(err) == "" {
				urlIdx++
				playlistURL = urls[urlIdx]
				continue
			}
			return written, err
		}
		playlist := parseM3U8PlaylistDetails(string(raw), playlistURL)
		if len(playlist.variants) > 0 && len(playlist.segments) == 0 && playlist.valid {
			// Some nodes answer with a multivariant playlist. Follow it once; a
			// second level of indirection is treated as a schema change.
			variant, ok := playlist.bestVariantInfo()
			if followedVariant || !ok {
				return written, &bilibili.SiteError{Site: site.Bilibili, Kind: bilibili.ErrKindSchemaChanged, Message: "直播播放列表结构不完整（多级索引）"}
			}
			if err := m.client.ValidateMediaURL(variant.uri); err != nil {
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
			return written, &bilibili.SiteError{Site: site.Bilibili, Kind: bilibili.ErrKindSchemaChanged, Message: "直播播放列表结构不完整：" + playlist.invalidReason}
		}
		m.markSessionReconnected(sess, m.now(), false, partBase)

		m.mu.Lock()
		lastSeq, hasSeq, wroteMedia := sess.lastSequence, sess.hasSequence, sess.wroteMedia
		m.mu.Unlock()

		if hasSeq && len(playlist.segments) > 0 && playlist.segments[len(playlist.segments)-1].seq < lastSeq {
			// A sequence reset starts a new part even when the same init URI is
			// reused. Keeping the old watermark would skip the new broadcast data.
			m.mu.Lock()
			sess.lastSequence, sess.hasSequence = 0, false
			m.mu.Unlock()
			return written, errSegmentRollover
		}
		newCount := 0
		if wroteMedia && playlist.mapURI == initURI {
			for _, segment := range playlist.segments {
				if !hasSeq || segment.seq > lastSeq {
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
			if hasSeq && segment.seq <= lastSeq {
				continue
			}
			if wroteMedia && (segment.discontinuity || hasSeq && segment.seq-lastSeq > 4 || segment.mapURI != initURI) {
				return written, errSegmentRollover
			}
			if initURI == "" {
				initBytes, err = m.fetchLiveBytes(ctx, segment.mapURI)
				if err != nil {
					return written, err
				}
				if !validLiveInit(initBytes) {
					return written, &bilibili.SiteError{Site: site.Bilibili, Kind: bilibili.ErrKindSchemaChanged, Message: "直播初始化分片缺少有效视频轨"}
				}
				initURI = segment.mapURI
				n, err := f.Write(initBytes)
				written += int64(n)
				m.addSessionBytes(sess, int64(n))
				if err != nil {
					return written, err
				}
			}
			fetchStart := m.now()
			fragment, err := m.fetchLiveBytes(ctx, segment.uri)
			fetchEnd := m.now()
			if err != nil {
				return written, err
			}
			if !validLiveFragment(fragment) {
				return written, &bilibili.SiteError{Site: site.Bilibili, Kind: bilibili.ErrKindSchemaChanged, Message: "直播媒体分片不完整"}
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
			if !segment.discontinuity && hasSeq && segment.seq > lastSeq+1 {
				dropped := int(segment.seq - lastSeq - 1)
				targetDur := playlist.targetDuration
				if targetDur <= 0 {
					targetDur = segment.duration
				}
				if targetDur <= 0 {
					targetDur = 1.0
				}
				// A gap longer than the time since the previous segment cannot be
				// lost media: the restarted stream numbers its segments from a new
				// base (seen jumping by 100 million after a reconnect).
				if lostSec := float64(dropped) * targetDur; sess.lastSequenceAt.IsZero() || lostSec <= fetchEnd.Sub(sess.lastSequenceAt).Seconds()+hlsSequenceGapSlackSec {
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
				}
			}
			m.observeTransferSpeed(sess, fetchStart, fetchEnd, int64(len(fragment)), segment.duration, partBase)

			lastSeq, hasSeq, wroteMedia = segment.seq, true, true
			sess.lastSequenceAt = fetchEnd
			sess.segmentDuration += segment.duration
			dur := sess.segmentDuration
			m.mu.Lock()
			sess.lastSequence, sess.hasSequence, sess.wroteMedia = lastSeq, hasSeq, wroteMedia
			m.mu.Unlock()

			m.commitSessionPart(sess, written, dur)
			if err := m.saveCheckpoint(sess, func(meta *library.ItemMetadata) {
				meta.Live.LastSequence, meta.Live.HasSequence = lastSeq, hasSeq
				meta.Live.PendingFile = partBase
				meta.Live.PendingBytes, meta.Live.PendingDuration = written, dur
			}); err != nil {
				return written, err
			}
			newCount++
		}
		now := m.now()
		m.mu.Lock()
		noSegTimeout := m.noSegmentTimeout
		pollWait := m.hlsPollInterval
		m.mu.Unlock()

		if newCount > 0 {
			lastNew = now
		} else if noSegTimeout > 0 && now.Sub(lastNew) >= noSegTimeout {
			return written, &bilibili.SiteError{Site: site.Bilibili, Kind: bilibili.ErrKindNetwork, Message: "超过 10 秒未收到新直播分片"}
		}
		if playlist.endList {
			return written, nil
		}
		if pollWait <= 0 {
			pollWait = time.Duration(math.Max(1, math.Min(5, playlist.targetDuration)) * float64(time.Second))
		}
		timer := time.NewTimer(pollWait)
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
