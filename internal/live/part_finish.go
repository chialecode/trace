package live

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/chialecode/trace/internal/library"
)

// convertPart is finalizeLivePart; tests slow it down.
var convertPart = finalizeLivePart

// partJob is a recorded part (fMP4 under ".part") to convert to its MP4.
type partJob struct {
	path        string // the .part file
	partRel     string // "part-NNN.mp4"
	pending     string // "part-NNN.mp4.part"
	codec       string // the stream's codec; empty takes the converted file's
	quality     int
	qualityName string
}

func roundedLiveDurationSec(sec float64) int {
	if sec <= 0 || math.IsNaN(sec) || math.IsInf(sec, 0) {
		return 0
	}
	return max(1, int(math.Round(sec)))
}

func removeFileWithRetry(path string) error {
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		err = os.Remove(path)
		if err == nil || os.IsNotExist(err) {
			return nil
		}
		time.Sleep(time.Duration(attempt+1) * 20 * time.Millisecond)
	}
	return err
}

func (m *Manager) finishContext() (context.Context, context.CancelFunc) {
	m.mu.Lock()
	stopping := m.stopping
	deadline := m.stopDeadline
	stallTimeout := m.finalizeStallTimeout
	finCtx := m.finalizeCtxLocked()
	m.mu.Unlock()
	if stallTimeout <= 0 {
		stallTimeout = defaultFinalizeStallTimeout
	}
	if stopping && !deadline.IsZero() {
		ctx, cancel := context.WithDeadline(finCtx, deadline)
		return withFinalizeStallTimeout(ctx, stallTimeout), cancel
	}
	ctx, cancel := context.WithCancel(finCtx)
	return withFinalizeStallTimeout(ctx, stallTimeout), cancel
}

// finishPartAsync converts a part while the session records the next one, so
// converting a long part never stalls playlist polling or loses segments.
// Parts of a session convert one at a time and in order; the session waits
// for them before it ends.
func (m *Manager) finishPartAsync(sess *recordSession, job partJob) {
	prev := sess.finishDone
	done := make(chan struct{})
	sess.finishDone = done
	go func() {
		defer close(done)
		if prev != nil {
			<-prev
		}
		if err := m.finishPart(sess, job); err != nil {
			m.setSessionStop(sess, "storage", err)
			if sess.cancel != nil {
				sess.cancel()
			}
		}
	}()
}

// waitPartsFinished waits for the parts converting in the background.
func (sess *recordSession) waitPartsFinished() {
	if sess.finishDone != nil {
		<-sess.finishDone
	}
}

// finishPart converts a recorded part to MP4 and registers it in the
// session's manifest; PendingParts retains the .part entry until the manifest
// save succeeds, and the .part file is removed only after commit. A failed
// conversion keeps the .part and records the incident.
func (m *Manager) finishPart(sess *recordSession, job partJob) error {
	startWall := time.Now()
	finishCtx, cancel := m.finishContext()
	summary, conversionErr := convertPart(finishCtx, job.path, filepath.Join(sess.itemDir, job.partRel))
	ctxErr := finishCtx.Err()
	cancel()
	if conversionErr != nil {
		m.mu.Lock()
		stopping := m.stopping
		m.mu.Unlock()
		if stopping && ctxErr != nil {
			// App shutdown deadline interrupted conversion: keep .part in PendingParts
			// for automatic recovery on next startup without recording a false failure.
			m.logEvent("WARN", "live convert interrupted: site=%s room=%s part=%s dur_ms=%d reason=app_closed",
				liveLogSite(sess), liveLogRoom(sess), job.pending, time.Since(startWall).Milliseconds())
			return m.writeSessionMeta(sess, false)
		}
		reason := "直播录像转换失败：" + sanitizeLiveError(conversionErr)
		m.logEvent("ERROR", "live convert failed: site=%s room=%s part=%s dur_ms=%d err=%s",
			liveLogSite(sess), liveLogRoom(sess), job.pending, time.Since(startWall).Milliseconds(), sanitizeLiveError(conversionErr))
		m.mu.Lock()
		addLiveMissing(&sess.itemMeta, job.pending, "转换失败，临时片段已保留")
		sess.lastErr = reason
		m.mu.Unlock()
		m.recordSessionIncident(sess, library.LiveIncident{
			Kind:   library.LiveIncidentConvertFailed,
			At:     m.now().UTC().Format(time.RFC3339),
			Part:   job.pending,
			Reason: reason,
		}, true)
		return m.writeSessionMeta(sess, false)
	}
	codec := job.codec
	if codec == "" && len(summary.Codecs) > 0 {
		codec = summary.Codecs[0]
	}
	mediaEntry := library.MediaFile{
		Role:     "live_segment",
		Path:     job.partRel,
		Bytes:    summary.FileBytes,
		SHA256:   summary.SHA256,
		Duration: roundedLiveDurationSec(summary.DurationSec),
		Width:    summary.Width,
		Height:   summary.Height,
	}
	segEntry := library.LiveSegmentInfo{
		File:        job.partRel,
		Bytes:       summary.FileBytes,
		SHA256:      summary.SHA256,
		DurationSec: summary.DurationSec,
		Codec:       codec,
		Width:       summary.Width,
		Height:      summary.Height,
		Quality:     job.quality,
		QualityName: job.qualityName,
	}
	applyPart := func(meta *library.ItemMetadata) {
		meta.Media = mergeLiveMedia(meta.Media, mediaEntry)
		meta.PendingParts = removePending(meta.PendingParts, job.pending)
		clearLiveCheckpoint(meta, job.pending)
		clearLiveMissing(meta, job.pending)
		if meta.Live != nil {
			meta.Live.Segments = mergeLiveSegment(meta.Live.Segments, segEntry)
		}
	}

	var notify *RoomState
	if err := m.commitSessionMeta(sess, false, applyPart, func() {
		sess.segments = mergeLiveSegmentPath(sess.segments, job.partRel)
		if st, ok := m.syncSessionRoomStateLocked(sess); ok {
			cp := st
			notify = &cp
		}
	}); err != nil {
		return err
	}

	_ = removeFileWithRetry(job.path)
	m.logEvent("INFO", "live convert done: site=%s room=%s part=%s bytes=%d dur_ms=%d",
		liveLogSite(sess), liveLogRoom(sess), job.partRel, summary.FileBytes, time.Since(startWall).Milliseconds())
	if notify != nil && m.onState != nil {
		leave := sess.enterExternal()
		m.onState(*notify)
		leave()
	}
	return nil
}

// saveSession writes the session's manifest from the recording loop.
func (m *Manager) saveSession(sess *recordSession) error {
	return m.writeSessionMeta(sess, true)
}

// writeSessionMeta writes the manifest. Writes of a session are serialized
// and each writes a copy taken as it starts, so the last write has the
// latest state; the manifest in memory stays the one parts and checkpoints
// change (only what the library derives is taken back). fromLoop also
// records the sequence the loop reached; a background part leaves it.
func (m *Manager) writeSessionMeta(sess *recordSession, fromLoop bool) error {
	return m.commitSessionMeta(sess, fromLoop, nil, nil)
}

// commitSessionMeta atomically applies mutate to a snapshot, persists it via
// m.lib.SaveItem, and only commits mutate + onSavedLocked back into sess once
// persistence succeeds.
func (m *Manager) commitSessionMeta(
	sess *recordSession,
	fromLoop bool,
	mutate func(meta *library.ItemMetadata),
	onSavedLocked func(),
) error {
	sess.saveMu.Lock()
	defer sess.saveMu.Unlock()
	m.mu.Lock()
	if fromLoop && sess.itemMeta.Live != nil {
		sess.itemMeta.Live.LastSequence, sess.itemMeta.Live.HasSequence = sess.lastSequence, sess.hasSequence
	}
	metaCopy := snapshotItemMeta(sess.itemMeta)
	if metaCopy.Live != nil {
		metaCopy.Live.Incidents = cloneIncidents(sess.incidents)
	}
	if mutate != nil {
		mutate(&metaCopy)
	}
	m.mu.Unlock()

	saveWall := time.Now()
	saved, err := m.lib.SaveLiveItem(sess.itemDir, metaCopy)
	saveDur := time.Since(saveWall)
	if err != nil {
		if saveDur >= slowLiveIOThreshold {
			m.logEvent("WARN", "live slow_io: site=%s room=%s op=save_session dur_ms=%d err=%s",
				liveLogSite(sess), liveLogRoom(sess), saveDur.Milliseconds(), sanitizeLiveError(err))
		}
		return err
	}
	m.mu.Lock()
	if mutate != nil {
		mutate(&sess.itemMeta)
	}
	if onSavedLocked != nil {
		onSavedLocked()
	}
	if sess.itemMeta.Live != nil {
		sess.itemMeta.Live.Incidents = cloneIncidents(sess.incidents)
	}
	sess.itemMeta.Version, sess.itemMeta.Site, sess.itemMeta.Kind = saved.Version, saved.Site, saved.Kind
	sess.itemMeta.RawID, sess.itemMeta.ItemKey, sess.itemMeta.ItemDir = saved.RawID, saved.ItemKey, saved.ItemDir
	sess.itemMeta.CachedAt, sess.itemMeta.TotalBytes, sess.itemMeta.Status = saved.CachedAt, saved.TotalBytes, saved.Status
	m.mu.Unlock()
	if saveDur >= slowLiveIOThreshold {
		m.logEvent("WARN", "live slow_io: site=%s room=%s op=save_session dur_ms=%d",
			liveLogSite(sess), liveLogRoom(sess), saveDur.Milliseconds())
	}
	return nil
}

// saveCheckpoint applies update to the manifest and writes it as the
// recording checkpoint, serialized with the session's other writes.
func (m *Manager) saveCheckpoint(sess *recordSession, update func(meta *library.ItemMetadata)) error {
	cpWall := time.Now()
	sess.saveMu.Lock()
	m.mu.Lock()
	update(&sess.itemMeta)
	if sess.itemMeta.Live != nil {
		sess.itemMeta.Live.Incidents = cloneIncidents(sess.incidents)
	}
	metaCopy := snapshotItemMeta(sess.itemMeta)
	m.mu.Unlock()
	_, err := m.lib.SaveRecordingCheckpoint(sess.itemDir, metaCopy)
	sess.saveMu.Unlock()
	if cpDur := time.Since(cpWall); cpDur >= slowLiveIOThreshold {
		m.logEvent("WARN", "live slow_io: site=%s room=%s op=checkpoint dur_ms=%d",
			liveLogSite(sess), liveLogRoom(sess), cpDur.Milliseconds())
	}
	return err
}

// snapshotItemMeta copies a live manifest deeply enough that writing it does
// not share what the session goes on changing.
func snapshotItemMeta(meta library.ItemMetadata) library.ItemMetadata {
	out := meta
	out.Media = slices.Clone(meta.Media)
	out.PendingParts = slices.Clone(meta.PendingParts)
	out.Missing = slices.Clone(meta.Missing)
	if meta.Live != nil {
		live := *meta.Live
		live.Segments = slices.Clone(meta.Live.Segments)
		live.Incidents = slices.Clone(meta.Live.Incidents)
		out.Live = &live
	}
	return out
}
