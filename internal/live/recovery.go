package live

import (
	"context"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
)

// RecoverOrphanParts processes only files named in a live item's pending list.
// It also converts a registered live segment saved without a duration (raw
// fragments under the final name), while skipping already-finalized short
// segments whose SHA256 and Live.Segments duration are recorded. Unrelated
// files and user-created .part files are never scanned or renamed.
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
			for _, session := range m.douyinSessions {
				active = active || session.itemDir == meta.ItemDir
			}
			timeout := m.finalizeTimeout
			m.mu.Unlock()
			if active {
				continue
			}
			if timeout <= 0 {
				timeout = 30 * time.Second
			}

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
				if !slices.Contains(meta.PendingParts, pending) {
					meta.PendingParts = append(meta.PendingParts, pending)
				}
			}
			if len(meta.PendingParts) == 0 {
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

				partInfo, partStatErr := os.Stat(partPath)
				_, finalStatErr := os.Stat(finalPath)
				pendingEmpty := (os.IsNotExist(partStatErr) || (partStatErr == nil && partInfo.Size() == 0)) &&
					os.IsNotExist(finalStatErr) &&
					(meta.Live == nil || meta.Live.PendingFile != rel || meta.Live.PendingBytes == 0)
				if pendingEmpty {
					if partStatErr == nil {
						_ = removeFileWithRetry(partPath)
					}
					clearLiveMissing(&meta, rel)
					clearLiveCheckpoint(&meta, rel)
					continue
				}

				ctx, cancel := context.WithTimeout(context.Background(), timeout)
				if meta.Live != nil && meta.Live.PendingFile == rel {
					if partStatErr == nil && meta.Live.PendingBytes > 0 && partInfo.Size() > meta.Live.PendingBytes {
						err = os.Truncate(partPath, meta.Live.PendingBytes)
					}
				}
				var summary download.MuxTrackSummary
				if err == nil {
					if os.IsNotExist(partStatErr) {
						summary, err = download.InspectMP4FileContext(ctx, finalPath)
						if err == nil && !finalizedLivePart(summary) {
							// The final name holds the unconverted fragments (for example a
							// ".part" renamed by hand): convert it instead of accepting it.
							if err = os.Rename(finalPath, partPath); err == nil {
								if summary, err = finalizeLivePart(ctx, partPath, finalPath); err == nil {
									_ = removeFileWithRetry(partPath)
								} else {
									_ = os.Rename(partPath, finalPath) // keep the file where the item expects it
								}
							}
						}
					} else {
						summary, err = finalizeLivePart(ctx, partPath, finalPath)
					}
				}
				cancel()
				if err != nil {
					remaining = append(remaining, rel)
					addLiveMissing(&meta, rel, "转换失败")
					if meta.Live != nil && !hasConvertFailedIncident(meta.Live.Incidents, rel) {
						meta.Live.Incidents, _, _ = appendCoalescedIncident(meta.Live.Incidents, library.LiveIncident{
							Kind:   library.LiveIncidentConvertFailed,
							At:     m.now().UTC().Format(time.RFC3339),
							Part:   rel,
							Reason: "直播录像转换失败：" + sanitizeLiveError(err),
						})
					}
					continue
				}
				meta.Media = mergeLiveMedia(meta.Media, library.MediaFile{
					Role:     "live_segment",
					Path:     finalRel,
					Bytes:    summary.FileBytes,
					SHA256:   summary.SHA256,
					Duration: roundedLiveDurationSec(summary.DurationSec),
					Width:    summary.Width,
					Height:   summary.Height,
				})
				if meta.Live != nil {
					codec := meta.Live.Codec
					if codec == "" && len(summary.Codecs) > 0 {
						codec = summary.Codecs[0]
						meta.Live.Codec = codec
					}
					qName := meta.Live.QualityName
					if qName == "" && meta.Live.Quality > 0 && (meta.Site == "" || meta.Site == string(site.Bilibili)) {
						qName = bilibili.LiveQualityFallbackName(meta.Live.Quality)
					}
					meta.Live.QualityName = qName
					meta.Live.Segments = mergeLiveSegment(meta.Live.Segments, library.LiveSegmentInfo{
						File:        finalRel,
						Bytes:       summary.FileBytes,
						SHA256:      summary.SHA256,
						DurationSec: summary.DurationSec,
						Codec:       codec,
						Quality:     meta.Live.Quality,
						QualityName: qName,
						Width:       summary.Width,
						Height:      summary.Height,
					})
					meta.Live.Incidents = dropResolvedConvertFailed(meta.Live.Incidents, rel)
				}
				clearLiveMissing(&meta, rel)
				clearLiveCheckpoint(&meta, rel)
			}
			meta.PendingParts = remaining
			meta.Status = "complete"
			if !hasAnyLiveSegment(meta) {
				meta.Status = "failed"
			}
			if len(remaining) > 0 || len(meta.Missing) > 0 {
				meta.Status = "partial"
			}
			if meta.Live != nil && meta.Live.StopReason == "" {
				meta.Live.StopReason = "app_closed"
				meta.Live.EndedAt = m.now().UTC().Format(time.RFC3339)
			}
			if _, err := m.lib.SaveItem(meta.ItemDir, meta); err == nil {
				for _, original := range itemsPendingToRemove(items, meta) {
					if path, err := library.ResolveItemPath(meta.ItemDir, original); err == nil {
						_ = removeFileWithRetry(path)
					}
				}
			}
		}
		if len(items) < 200 {
			return
		}
	}
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
