package live

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
)

func cleanLiveDirKey(dir string) string {
	cleaned := filepath.Clean(strings.TrimSpace(dir))
	if runtime.GOOS == "windows" {
		return strings.ToLower(cleaned)
	}
	return cleaned
}

func (m *Manager) activeSessionForDirLocked(dir string) *recordSession {
	key := cleanLiveDirKey(dir)
	if key == "" || key == "." {
		return nil
	}
	for _, session := range m.sessions {
		if session != nil && cleanLiveDirKey(session.itemDir) == key {
			return session
		}
	}
	for _, session := range m.douyinSessions {
		if session != nil && cleanLiveDirKey(session.itemDir) == key {
			return session
		}
	}
	return nil
}

func (m *Manager) activeSessionForDir(dir string) *recordSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activeSessionForDirLocked(dir)
}

func (m *Manager) acquireRecoverDir(dir string) (func(), bool) {
	key := cleanLiveDirKey(dir)
	if key == "" || key == "." {
		return nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.activeSessionForDirLocked(dir) != nil {
		return nil, false
	}
	if m.recoveringDirs == nil {
		m.recoveringDirs = make(map[string]struct{})
	}
	if _, busy := m.recoveringDirs[key]; busy {
		return nil, false
	}
	m.recoveringDirs[key] = struct{}{}
	return func() {
		m.mu.Lock()
		delete(m.recoveringDirs, key)
		m.mu.Unlock()
	}, true
}

// RecoverOrphanParts synchronously processes only files named in a live item's pending list.
// It also converts a registered live segment saved without a duration (raw
// fragments under the final name), while skipping already-finalized short
// segments whose SHA256 and Live.Segments duration are recorded. Unrelated
// files and user-created .part files are never scanned or renamed.
// Manager.Start launches recoverOrphanParts asynchronously so startup never blocks.
func (m *Manager) RecoverOrphanParts() {
	m.mu.Lock()
	finCtx := m.finalizeCtxLocked()
	m.mu.Unlock()
	m.recoverOrphanParts(finCtx)
}

func (m *Manager) recoverOrphanParts(ctx context.Context) {
	if m.lib == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for offset := 0; ; offset += 200 {
		if ctx.Err() != nil {
			return
		}
		items, err := m.lib.ListItems(library.FeedFilter{Kind: "live", Limit: 200, Offset: offset})
		if err != nil {
			return
		}
		for _, meta := range items {
			if ctx.Err() != nil {
				return
			}
			pendingList := collectRecoverablePendingParts(meta)
			if len(pendingList) == 0 {
				continue
			}
			release, ok := m.acquireRecoverDir(meta.ItemDir)
			if !ok {
				continue
			}
			m.recoverSingleLiveItem(ctx, meta, pendingList)
			release()
		}
		if len(items) < 200 {
			return
		}
	}
}

func collectRecoverablePendingParts(meta library.ItemMetadata) []string {
	out := slices.Clone(meta.PendingParts)
	// A segment saved with no duration was never converted (an earlier
	// recovery accepted raw fragments); convert it like a pending part,
	// unless SHA256 + Live.Segments already confirm it was finalized.
	for _, media := range meta.Media {
		if media.Role != "live_segment" || media.Duration > 0 || !strings.HasPrefix(media.Path, "part-") {
			continue
		}
		if hasFinalizedLiveSegment(meta, media) {
			continue
		}
		pending := media.Path + ".part"
		if !slices.Contains(out, pending) {
			out = append(out, pending)
		}
	}
	return out
}

func recoveryLogRoom(meta library.ItemMetadata) string {
	if meta.Live != nil {
		if strings.TrimSpace(meta.Live.RoomIDStr) != "" {
			return strings.TrimSpace(meta.Live.RoomIDStr)
		}
		if meta.Live.RoomID > 0 {
			return strconv.FormatInt(meta.Live.RoomID, 10)
		}
	}
	return firstNonEmpty(meta.RawID, "-")
}

func (m *Manager) recoverSingleLiveItem(ctx context.Context, meta library.ItemMetadata, pendingList []string) {
	siteLabel := firstNonEmpty(meta.Site, string(site.Bilibili))
	roomLabel := recoveryLogRoom(meta)

	for _, rel := range pendingList {
		if ctx.Err() != nil {
			return
		}
		if !strings.HasPrefix(rel, "part-") || !strings.HasSuffix(rel, ".mp4.part") {
			continue
		}
		partPath, err := library.ResolveItemPath(meta.ItemDir, rel)
		if err != nil {
			continue
		}
		finalRel := strings.TrimSuffix(rel, ".part")
		finalPath, err := library.ResolveItemPath(meta.ItemDir, finalRel)
		if err != nil {
			continue
		}

		// If a live session started concurrently in the same directory, skip any part
		// it is actively writing (still allow finishing pre-existing older parts).
		if active := m.activeSessionForDir(meta.ItemDir); active != nil {
			m.mu.Lock()
			isActiveWriting := cleanLiveDirKey(active.activePartPath) == cleanLiveDirKey(partPath)
			m.mu.Unlock()
			if isActiveWriting {
				continue
			}
		}

		partInfo, partStatErr := os.Stat(partPath)
		_, finalStatErr := os.Stat(finalPath)
		noPositiveCheckpoint := meta.Live == nil || meta.Live.PendingFile != rel || meta.Live.PendingBytes == 0
		pendingEmpty := (os.IsNotExist(partStatErr) || (partStatErr == nil && partInfo.Size() == 0)) &&
			os.IsNotExist(finalStatErr) &&
			noPositiveCheckpoint
		discardedInitOnly := false
		if !pendingEmpty && partStatErr == nil && os.IsNotExist(finalStatErr) && noPositiveCheckpoint && isEmptyInitOnlyLivePart(partPath) {
			pendingEmpty = true
			discardedInitOnly = true
		}
		if pendingEmpty {
			savedOK := m.commitRecoveredPart(meta, func(target *library.ItemMetadata) {
				target.PendingParts = removePending(target.PendingParts, rel)
				clearLiveMissing(target, rel)
				clearLiveCheckpoint(target, rel)
				if target.Live != nil {
					target.Live.Incidents = dropResolvedConvertFailed(target.Live.Incidents, rel)
				}
			}, func(sess *recordSession) {
				sess.incidents = dropResolvedConvertFailed(sess.incidents, rel)
			})
			if savedOK {
				if partStatErr == nil {
					_ = removeFileWithRetry(partPath)
				}
				if discardedInitOnly {
					m.logEvent("INFO", "live recover discarded_empty: site=%s room=%s part=%s bytes=%d",
						siteLabel, roomLabel, rel, partInfo.Size())
				}
			}
			continue
		}

		startWall := time.Now()
		finishCtx, cancel := m.finishContext()
		var convErr error
		if meta.Live != nil && meta.Live.PendingFile == rel {
			if partStatErr == nil && meta.Live.PendingBytes > 0 && partInfo.Size() > meta.Live.PendingBytes {
				convErr = os.Truncate(partPath, meta.Live.PendingBytes)
			}
		}
		var summary download.MuxTrackSummary
		if convErr == nil {
			if os.IsNotExist(partStatErr) {
				// .part is already gone and finalPath exists on disk: keep full InspectMP4FileContext
				// because inline SHA-256 did not run.
				summary, convErr = download.InspectMP4FileContext(finishCtx, finalPath)
				if convErr == nil && !finalizedLivePart(summary) {
					// The final name holds the unconverted fragments (for example a
					// ".part" renamed by hand): convert it instead of accepting it.
					if convErr = os.Rename(finalPath, partPath); convErr == nil {
						if summary, convErr = convertPart(finishCtx, partPath, finalPath); convErr == nil {
							_ = removeFileWithRetry(partPath)
						} else {
							_ = os.Rename(partPath, finalPath) // keep the file where the item expects it
						}
					}
				}
			} else {
				summary, convErr = convertPart(finishCtx, partPath, finalPath)
			}
		}
		finCtxErr := finishCtx.Err()
		cancel()

		if convErr != nil {
			m.mu.Lock()
			stopping := m.stopping
			m.mu.Unlock()
			if ctx.Err() != nil || (stopping && finCtxErr != nil) {
				// Shutdown interrupted startup recovery: leave .part intact without
				// recording a convert_failed incident so next startup resumes cleanly.
				m.logEvent("WARN", "live recover interrupted: site=%s room=%s part=%s dur_ms=%d reason=app_closed",
					siteLabel, roomLabel, rel, time.Since(startWall).Milliseconds())
				return
			}
			m.logEvent("ERROR", "live recover failed: site=%s room=%s part=%s dur_ms=%d err=%s",
				siteLabel, roomLabel, rel, time.Since(startWall).Milliseconds(), sanitizeLiveError(convErr))
			m.commitRecoveredPart(meta, func(target *library.ItemMetadata) {
				if !slices.Contains(target.PendingParts, rel) {
					target.PendingParts = append(target.PendingParts, rel)
				}
				addLiveMissing(target, rel, "转换失败")
				if target.Live != nil && !hasConvertFailedIncident(target.Live.Incidents, rel) {
					target.Live.Incidents, _, _ = appendCoalescedIncident(target.Live.Incidents, library.LiveIncident{
						Kind:   library.LiveIncidentConvertFailed,
						At:     m.now().UTC().Format(time.RFC3339),
						Part:   rel,
						Reason: "直播录像转换失败：" + sanitizeLiveError(convErr),
					})
				}
			}, func(sess *recordSession) {
				if !hasConvertFailedIncident(sess.incidents, rel) {
					sess.incidents, _, _ = appendCoalescedIncident(sess.incidents, library.LiveIncident{
						Kind:   library.LiveIncidentConvertFailed,
						At:     m.now().UTC().Format(time.RFC3339),
						Part:   rel,
						Reason: "直播录像转换失败：" + sanitizeLiveError(convErr),
					})
				}
			})
			continue
		}

		savedOK := m.commitRecoveredPart(meta, func(target *library.ItemMetadata) {
			target.Media = mergeLiveMedia(target.Media, library.MediaFile{
				Role:     "live_segment",
				Path:     finalRel,
				Bytes:    summary.FileBytes,
				SHA256:   summary.SHA256,
				Duration: roundedLiveDurationSec(summary.DurationSec),
				Width:    summary.Width,
				Height:   summary.Height,
			})
			if target.Live != nil {
				codec := target.Live.Codec
				if codec == "" && len(summary.Codecs) > 0 {
					codec = summary.Codecs[0]
					target.Live.Codec = codec
				}
				qName := target.Live.QualityName
				if qName == "" && target.Live.Quality > 0 && (target.Site == "" || target.Site == string(site.Bilibili)) {
					qName = bilibili.LiveQualityFallbackName(target.Live.Quality)
				}
				target.Live.QualityName = qName
				target.Live.Segments = mergeLiveSegment(target.Live.Segments, library.LiveSegmentInfo{
					File:        finalRel,
					Bytes:       summary.FileBytes,
					SHA256:      summary.SHA256,
					DurationSec: summary.DurationSec,
					Codec:       codec,
					Quality:     target.Live.Quality,
					QualityName: qName,
					Width:       summary.Width,
					Height:      summary.Height,
				})
				target.Live.Incidents = dropResolvedConvertFailed(target.Live.Incidents, rel)
			}
			target.PendingParts = removePending(target.PendingParts, rel)
			clearLiveMissing(target, rel)
			clearLiveCheckpoint(target, rel)
		}, func(sess *recordSession) {
			sess.segments = mergeLiveSegmentPath(sess.segments, finalRel)
			sess.bytesTotal += summary.FileBytes
			sess.incidents = dropResolvedConvertFailed(sess.incidents, rel)
		})
		if savedOK {
			if partStatErr == nil {
				_ = removeFileWithRetry(partPath)
			}
			m.logEvent("INFO", "live recover done: site=%s room=%s part=%s bytes=%d dur_ms=%d",
				siteLabel, roomLabel, finalRel, summary.FileBytes, time.Since(startWall).Milliseconds())
		}
	}
}

// lockItemDirCommit acquires a lightweight per-itemDir serialization slot used
// only for short manifest read-modify-save + session registration sections
// (never held across network I/O or MP4 conversion).
func (m *Manager) lockItemDirCommit(itemDir string) func() {
	key := cleanLiveDirKey(itemDir)
	if key == "" {
		return func() {}
	}
	for {
		m.mu.Lock()
		if m.dirCommitSlots == nil {
			m.dirCommitSlots = make(map[string]chan struct{})
		}
		if waitCh, busy := m.dirCommitSlots[key]; busy {
			m.mu.Unlock()
			<-waitCh
			continue
		}
		done := make(chan struct{})
		m.dirCommitSlots[key] = done
		m.mu.Unlock()
		return func() {
			m.mu.Lock()
			if cur, ok := m.dirCommitSlots[key]; ok && cur == done {
				delete(m.dirCommitSlots, key)
			}
			m.mu.Unlock()
			close(done)
		}
	}
}

// lockItemDirForItem resolves the canonical itemDir for itemKey, acquires the
// per-itemDir commit slot, and loads the authoritative manifest under that slot.
func (m *Manager) lockItemDirForItem(itemKey, defaultDir string) (string, library.ItemMetadata, bool, func(), error) {
	targetDir := defaultDir
	if peek, ok, err := m.lib.GetItem(itemKey); err == nil && ok && peek.ItemDir != "" {
		targetDir = peek.ItemDir
	}
	unlock := m.lockItemDirCommit(targetDir)
	existing, exists, err := m.lib.GetItem(itemKey)
	if err != nil {
		unlock()
		return "", library.ItemMetadata{}, false, nil, err
	}
	if exists && existing.ItemDir != "" && cleanLiveDirKey(existing.ItemDir) != cleanLiveDirKey(targetDir) {
		unlock()
		targetDir = existing.ItemDir
		unlock = m.lockItemDirCommit(targetDir)
		existing, exists, err = m.lib.GetItem(itemKey)
		if err != nil {
			unlock()
			return "", library.ItemMetadata{}, false, nil, err
		}
	}
	if exists && existing.ItemDir != "" {
		targetDir = existing.ItemDir
	}
	return targetDir, existing, exists, unlock, nil
}

func (m *Manager) commitRecoveredPart(
	fallbackMeta library.ItemMetadata,
	mutate func(target *library.ItemMetadata),
	onActiveSessionLocked func(sess *recordSession),
) bool {
	unlockDir := m.lockItemDirCommit(fallbackMeta.ItemDir)
	if activeSess := m.activeSessionForDir(fallbackMeta.ItemDir); activeSess != nil {
		var notify *RoomState
		err := m.commitSessionMeta(activeSess, false, mutate, func() {
			if onActiveSessionLocked != nil {
				onActiveSessionLocked(activeSess)
			}
			if st, ok := m.syncSessionRoomStateLocked(activeSess); ok {
				cp := st
				notify = &cp
			}
		})
		unlockDir()
		if err != nil {
			return false
		}
		if notify != nil && m.onState != nil {
			leave := activeSess.enterExternal()
			m.onState(*notify)
			leave()
		}
		return true
	}

	curMeta := fallbackMeta
	if loaded, ok, err := m.lib.GetItem(fallbackMeta.ItemKey); err == nil && ok {
		curMeta = loaded
	}
	if hook := m.testInRecoveryCommit; hook != nil {
		hook(curMeta.ItemDir)
	}
	mutate(&curMeta)
	if curMeta.Status != "recording" || m.activeSessionForDir(curMeta.ItemDir) == nil {
		curMeta.Status = "complete"
		if !hasAnyLiveSegment(curMeta) {
			curMeta.Status = "failed"
		}
		if len(curMeta.PendingParts) > 0 || len(curMeta.Missing) > 0 {
			curMeta.Status = "partial"
		}
		if curMeta.Live != nil && curMeta.Live.StopReason == "" {
			curMeta.Live.StopReason = "app_closed"
			curMeta.Live.EndedAt = m.now().UTC().Format(time.RFC3339)
		}
	}
	_, err := m.lib.SaveLiveItem(curMeta.ItemDir, curMeta)
	unlockDir()
	return err == nil
}

func hasFinalizedLiveSegment(meta library.ItemMetadata, media library.MediaFile) bool {
	if media.SHA256 == "" || meta.Live == nil {
		return false
	}
	for _, seg := range meta.Live.Segments {
		if seg.File == media.Path && seg.DurationSec > 0 {
			return true
		}
	}
	return false
}

func hasAnyLiveSegment(meta library.ItemMetadata) bool {
	if meta.Live != nil && len(meta.Live.Segments) > 0 {
		return true
	}
	for _, mf := range meta.Media {
		if mf.Role == "live_segment" {
			return true
		}
	}
	return false
}

// isEmptyInitOnlyLivePart reports whether partPath is a valid fMP4 init header
// (ftyp+moov) with strictly zero media fragments and zero samples across every
// track (for example an orphan init-only .part written before a first media
// fragment arrived). Arbitrary or corrupted files return false so recovery
// never discards inspectable media.
func isEmptyInitOnlyLivePart(partPath string) bool {
	f, err := os.Open(partPath)
	if err != nil {
		return false
	}
	defer f.Close()
	parsed, err := mp4.DecodeFile(f, mp4.WithDecodeMode(mp4.DecModeLazyMdat), mp4.WithDecodeFlags(mp4.DecStartOnMoof))
	if err != nil || parsed == nil {
		return false
	}
	moov := parsed.Moov
	if moov == nil && parsed.Init != nil {
		moov = parsed.Init.Moov
	}
	if moov == nil || len(moov.Traks) == 0 || len(parsed.Segments) > 0 {
		return false
	}
	_, _, _, totalSamples := liveTracksWithSamples(parsed)
	return totalSamples == 0
}
