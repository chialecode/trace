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

// finishPartAsync converts a part while the session records the next one.
// Converting a long part takes tens of seconds (3.3 GB took about 40); a
// live playlist keeps only the last few seconds, so converting it in the
// recording loop lost the segments published meanwhile (dev 2026-11-18).
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
// session's manifest; the .part goes once the manifest names the MP4. A
// failed conversion keeps the .part and records the incident. The error is
// the manifest's: the session cannot go on without it.
func (m *Manager) finishPart(sess *recordSession, job partJob) error {
	finishCtx, cancel := context.WithTimeout(context.Background(), m.finalizeTimeout)
	summary, conversionErr := convertPart(finishCtx, job.path, filepath.Join(sess.itemDir, job.partRel))
	cancel()
	if conversionErr != nil {
		m.mu.Lock()
		addLiveMissing(&sess.itemMeta, job.pending, "转换失败，临时片段已保留")
		sess.lastErr = "直播录像转换失败：" + conversionErr.Error()
		m.mu.Unlock()
		m.recordSessionIncident(sess, library.LiveIncident{
			Kind:   library.LiveIncidentConvertFailed,
			At:     m.nowFunc().UTC().Format(time.RFC3339),
			Part:   job.pending,
			Reason: "直播录像转换失败：" + library.SanitizeLiveIncidentText(conversionErr.Error()),
		}, true)
		return m.writeSessionMeta(sess, false)
	}
	codec := job.codec
	if codec == "" && len(summary.Codecs) > 0 {
		codec = summary.Codecs[0]
	}
	m.mu.Lock()
	sess.itemMeta.Media = mergeLiveMedia(sess.itemMeta.Media, library.MediaFile{Role: "live_segment", Path: job.partRel, Bytes: summary.FileBytes, SHA256: summary.SHA256, Duration: int(math.Round(summary.DurationSec)), Width: summary.Width, Height: summary.Height})
	sess.itemMeta.PendingParts = removePending(sess.itemMeta.PendingParts, job.pending)
	clearLiveCheckpoint(&sess.itemMeta, job.pending)
	clearLiveMissing(&sess.itemMeta, job.pending)
	sess.itemMeta.Live.Segments = mergeLiveSegment(sess.itemMeta.Live.Segments, library.LiveSegmentInfo{File: job.partRel, Bytes: summary.FileBytes, SHA256: summary.SHA256, DurationSec: summary.DurationSec, Codec: codec, Width: summary.Width, Height: summary.Height, Quality: job.quality, QualityName: job.qualityName})
	sess.segments = append(sess.segments, job.partRel)
	m.mu.Unlock()
	if err := m.writeSessionMeta(sess, false); err != nil {
		return err
	}
	_ = os.Remove(job.path)
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
	sess.saveMu.Lock()
	defer sess.saveMu.Unlock()
	m.mu.Lock()
	if fromLoop {
		sess.itemMeta.Live.LastSequence, sess.itemMeta.Live.HasSequence = sess.lastSequence, sess.hasSequence
	}
	sess.itemMeta.Live.Incidents = cloneIncidents(sess.incidents)
	metaCopy := snapshotItemMeta(sess.itemMeta)
	m.mu.Unlock()
	saved, err := m.lib.SaveItem(sess.itemDir, metaCopy)
	if err == nil {
		m.mu.Lock()
		sess.itemMeta.Version, sess.itemMeta.Site, sess.itemMeta.Kind = saved.Version, saved.Site, saved.Kind
		sess.itemMeta.RawID, sess.itemMeta.ItemKey, sess.itemMeta.ItemDir = saved.RawID, saved.ItemKey, saved.ItemDir
		sess.itemMeta.CachedAt, sess.itemMeta.TotalBytes, sess.itemMeta.Status = saved.CachedAt, saved.TotalBytes, saved.Status
		m.mu.Unlock()
	}
	return err
}

// saveCheckpoint applies update to the manifest and writes it as the
// recording checkpoint, serialized with the session's other writes.
func (m *Manager) saveCheckpoint(sess *recordSession, update func(meta *library.ItemMetadata)) error {
	sess.saveMu.Lock()
	defer sess.saveMu.Unlock()
	m.mu.Lock()
	update(&sess.itemMeta)
	sess.itemMeta.Live.Incidents = cloneIncidents(sess.incidents)
	metaCopy := snapshotItemMeta(sess.itemMeta)
	m.mu.Unlock()
	_, err := m.lib.SaveRecordingCheckpoint(sess.itemDir, metaCopy)
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
