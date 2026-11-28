package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
)

// videoVia records why a new video item sits under another creator's folder:
// bilibili/<mid>/forwards/<bvid> is a forwarder's, bilibili/<mid>/videos/<bvid>
// with mid a collaboration member is that member's (DEC-056). Anything else,
// including the uploader's own folder, has none.
func videoVia(root, itemDir string, detail bilibili.VideoDetail, t *Task) *library.ItemVia {
	rel, err := filepath.Rel(root, itemDir)
	if err != nil {
		return nil
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) != 4 || parts[0] != "bilibili" {
		return nil
	}
	mid, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || mid <= 0 || mid == detail.Author.Mid {
		return nil
	}
	switch parts[2] {
	case "forwards":
		return &library.ItemVia{Reason: library.ViaForward, Site: "bilibili", UID: parts[1], Name: t.ViaName, DynamicID: t.ViaDynamicID}
	case "videos":
		if member, ok := detail.StaffMember(mid); ok {
			return &library.ItemVia{Reason: library.ViaCollab, Site: "bilibili", UID: parts[1], Name: member.Name}
		}
	}
	return nil
}

func (m *Manager) executeVideoTask(ctx context.Context, task *Task) error {
	return m.cacheVideoPart(ctx, task, false)
}

func (m *Manager) cacheVideoPart(ctx context.Context, t *Task, upgrade bool) (result error) {
	unlock, err := m.lockItem(ctx, t.TargetID)
	if err != nil {
		return err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	cfg := m.dlSettings
	m.mu.Unlock()

	storedMode, selectedPages := m.taskResolutionAndPages(t.TaskID)
	mode := effectiveConflictResolution(ctx, t.ConflictResolution, storedMode)
	forceOverwrite := effectiveForceOverwrite(ctx, t.Source, t.ForceOverwrite, m.taskForceOverwrite(t.TaskID))

	preMeta, preExists, _ := m.lib.GetItem(t.TargetID)
	if preExists && preMeta.ItemDir != "" {
		_ = library.RecoverInterruptedItemSwap(preMeta.ItemDir, preMeta)
		preMeta, preExists, _ = m.lib.GetItem(t.TargetID)
	}

	detail, err := m.client.VideoDetail(ctx, t.TargetID)
	if err != nil {
		if preExists && localHasProtectedContent(preMeta) && isDegradedSiteError(err) {
			return wrapRemoteDegraded(err)
		}
		return err
	}

	meta, exists, err := m.lib.GetItem(detail.BVID)
	if err != nil {
		return err
	}
	if exists && meta.ItemDir != "" {
		_ = library.RecoverInterruptedItemSwap(meta.ItemDir, meta)
		meta, exists, err = m.lib.GetItem(detail.BVID)
		if err != nil {
			return err
		}
	}
	hasProtectedLocal := exists && localHasProtectedContent(meta)

	if detail.IsUpowerLocked() {
		upowerErr := bilibili.NewUpowerLockedError("/x/web-interface/wbi/view")
		if hasProtectedLocal {
			return wrapRemoteDegraded(upowerErr)
		}
		return upowerErr
	}

	if hasProtectedLocal {
		if mode == ConflictResolveKeepBoth || mode == ConflictResolveUseRemote || forceOverwrite {
			effectiveMode := mode
			if effectiveMode == "" {
				effectiveMode = ConflictResolveUseRemote
			}
			return m.executeBilibiliVideoConflictReplace(ctx, t, detail, meta, effectiveMode, selectedPages)
		}
		notice, isConflict, degErr := classifyBilibiliVideoVsLocal(meta, detail, t)
		if degErr != nil {
			return degErr
		}
		if isConflict {
			m.emitConflict(notice)
			return ErrConflictDetected
		}
	}

	var page bilibili.VideoPage
	for _, candidate := range detail.Pages {
		if t.CID > 0 && candidate.CID == t.CID || t.CID == 0 && candidate.Page == max(1, t.Page) {
			page = candidate
			break
		}
	}
	if page.CID <= 0 {
		unavailErr := &bilibili.SiteError{Kind: bilibili.ErrKindUnavailable, Message: "请求的分 P 已不可用"}
		if hasProtectedLocal {
			return wrapRemoteDegraded(unavailErr)
		}
		return unavailErr
	}
	t.CID, t.Page, t.Title, t.PartTitle = page.CID, page.Page, detail.Title, page.Title
	t.Mid, t.AuthorName, t.AuthorFace, t.CoverURL = detail.Author.Mid, detail.Author.Name, detail.Author.Face, detail.Cover
	if !exists {
		meta = library.ItemMetadata{Version: 1, Site: "bilibili", Kind: "video", ID: "video:" + detail.BVID, Status: "partial"}
		root := m.lib.CacheDir()
		dir := filepath.Join(root, "bilibili", strconv.FormatInt(detail.Author.Mid, 10), "videos", detail.BVID)
		if t.ItemDir != "" {
			dir = t.ItemDir
		}
		meta.ItemDir, err = library.EnsureWithinRoot(root, dir)
		if err != nil {
			return err
		}
		meta.Via = videoVia(root, meta.ItemDir, detail, t)
	}
	if err := os.MkdirAll(meta.ItemDir, 0700); err != nil {
		return err
	}
	t.ItemDir, t.ItemKey = meta.ItemDir, library.CanonicalItemKey("bilibili", "video", detail.BVID)

	var old library.VideoPartRecord
	for _, part := range meta.Parts {
		if part.Page == page.Page {
			old = part
			break
		}
	}

	// Pre-download overwrite guard (Invariant #5): NEVER mutate or save item.json
	// before media is verified when local item already has valid cached media!
	if !hasProtectedLocal {
		meta.Title, meta.Desc, meta.URL, meta.PubTs, meta.Duration = detail.Title, detail.Desc, detail.CanonicalURL, detail.PubTs, detail.Duration
		meta.RemotePartCount = len(detail.Pages)
		meta.Author = library.ItemAuthor{Mid: detail.Author.Mid, Name: detail.Author.Name, Face: detail.Author.Face}
		meta.DirectCached = meta.DirectCached || !t.LinkedVideo
		for _, candidate := range detail.Pages {
			if cfg.MultiPart == "all" || candidate.Page == page.Page {
				if !containsPage(meta.ExpectedPages, candidate.Page) {
					meta.ExpectedPages = append(meta.ExpectedPages, candidate.Page)
				}
			}
		}
		// Persist the expected pages before media requests on fresh caches, so a
		// paused first page never leaves a green item claiming to contain the whole video.
		if err := m.saveVideoManifest(t, &meta, false); err != nil {
			return err
		}
	}
	defer func() {
		if result != nil && !hasProtectedLocal {
			if !isStopError(result) && !mediaIsValid(meta, fmt.Sprintf("P%02d.mp4", page.Page)) && !mediaIsValid(meta, old.File) {
				addMissing(&meta, fmt.Sprintf("P%02d.media", page.Page), "分 P 尚未完成 ("+bilibili.KindOf(result)+")")
			}
			if saveErr := m.saveVideoManifest(t, &meta, false); saveErr != nil {
				result = saveErr
			}
		}
		if result == nil && len(meta.Missing) > 0 {
			t.Status = "partial"
		}
	}()

	applyVerifiedHeaderUpdates := func() {
		meta.Title = preserveMonotonicText(meta.Title, detail.Title, false)
		meta.Desc = preserveMonotonicText(meta.Desc, detail.Desc, false)
		meta.URL, meta.PubTs, meta.Duration = detail.CanonicalURL, detail.PubTs, detail.Duration
		meta.RemotePartCount = len(detail.Pages)
		meta.Author = library.ItemAuthor{Mid: detail.Author.Mid, Name: detail.Author.Name, Face: detail.Author.Face}
		meta.DirectCached = meta.DirectCached || !t.LinkedVideo
		for _, candidate := range detail.Pages {
			if cfg.MultiPart == "all" || candidate.Page == page.Page {
				if !containsPage(meta.ExpectedPages, candidate.Page) {
					meta.ExpectedPages = append(meta.ExpectedPages, candidate.Page)
				}
			}
		}
	}

	if !hasProtectedLocal {
		if err := m.saveVideoCover(ctx, t, &meta, detail.Cover, cfg); err != nil {
			return err
		}
	}
	if !upgrade && old.File != "" && mediaIsValid(meta, old.File) {
		applyVerifiedHeaderUpdates()
		if err := m.saveVideoCover(ctx, t, &meta, detail.Cover, cfg); err != nil {
			return err
		}
		for _, media := range meta.Media {
			if media.Path == old.File || strings.HasPrefix(media.Path, fmt.Sprintf("P%02d.", page.Page)) {
				m.setFileProgress(t, media.Role, media.Path, "completed", media.Bytes, media.Bytes, 0)
			}
		}
		chaptersOK, extraErr := m.saveVideoExtras(ctx, t, &meta, &old, cfg)
		meta.Parts = mergePartRecords(meta.Parts, old)
		if extraErr != nil {
			return extraErr
		}
		return m.saveVideoManifest(t, &meta, chaptersOK && allPartsExtrasReady(meta, cfg))
	}
	opts := bilibili.PlaySelectionOptions{PreferredCodec: cfg.PreferredVideoCodec, CodecOrder: cfg.CodecOrder, PreferLosslessAudio: cfg.PreferLosslessAudio, PreferDolbyAudio: cfg.PreferDolbyAudio, TargetQn: targetQuality(cfg.VideoQuality)}
	if upgrade && bilibili.QualityRank(old.Video.Quality) > bilibili.QualityRank(opts.TargetQn) {
		opts.TargetQn = old.Video.Quality
	}
	plan, err := m.client.PlayURL(ctx, detail.BVID, page.CID, opts)
	if err != nil {
		if hasProtectedLocal && isDegradedSiteError(err) {
			return wrapRemoteDegraded(err)
		}
		return err
	}
	if upgrade {
		if bilibili.QualityRank(plan.Video.Quality) < bilibili.QualityRank(old.Video.Quality) || old.Audio.Quality > 0 && (plan.Audio == nil || audioRank(plan.Audio.Quality) < audioRank(old.Audio.Quality)) {
			return wrapRemoteDegraded(&bilibili.SiteError{Kind: bilibili.ErrKindUnavailable, Message: "站点当前无法提供不低于旧缓存的画质与音质，旧文件已保留"})
		}
		if bilibili.QualityRank(plan.Video.Quality) <= bilibili.QualityRank(old.Video.Quality) && (plan.Audio == nil || audioRank(plan.Audio.Quality) <= audioRank(old.Audio.Quality)) {
			applyVerifiedHeaderUpdates()
			old.Available = availableRecord(plan)
			chaptersOK, extraErr := m.saveVideoExtras(ctx, t, &meta, &old, cfg)
			meta.Parts = mergePartRecords(meta.Parts, old)
			if extraErr != nil {
				return extraErr
			}
			return m.saveVideoManifest(t, &meta, chaptersOK && allPartsExtrasReady(meta, cfg))
		}
	}
	t.QualityHint = plan.Available.DowngradeHint
	mp4Rel := fmt.Sprintf("P%02d.mp4", page.Page)
	if old.File != "" {
		mp4Rel = old.File
	}
	mp4Path, err := library.ResolveItemPath(meta.ItemDir, mp4Rel)
	if err != nil {
		return err
	}
	if err := m.claimTaskFile(t, meta.ItemDir, mp4Rel, mp4Path); err != nil {
		return err
	}
	m.setFileProgress(t, "video", mp4Rel, "waiting", 0, 0, 0)
	var refreshed *bilibili.PlayPlan
	refresh := func() (bilibili.PlayPlan, error) {
		if refreshed != nil {
			return *refreshed, nil
		}
		fresh, err := m.client.PlayURL(ctx, detail.BVID, page.CID, opts)
		if err == nil {
			refreshed = &fresh
		}
		return fresh, err
	}
	videoURLs := func() ([]string, error) {
		fresh, err := refresh()
		if err != nil {
			return nil, err
		}
		if fresh.Mode != plan.Mode || fresh.Video.Quality != plan.Video.Quality || fresh.Video.CodecID != plan.Video.CodecID || fresh.Video.Codecs != plan.Video.Codecs {
			return nil, &bilibili.SiteError{Kind: bilibili.ErrKindUnavailable, Message: "续传地址的画质或编码发生变化，已保留断点"}
		}
		return fresh.Video.URLs, nil
	}
	audioURLs := func(original *bilibili.SelectedAudioStream, fallback bool) func() ([]string, error) {
		return func() ([]string, error) {
			fresh, err := refresh()
			if err != nil {
				return nil, err
			}
			candidate := fresh.Audio
			if fallback {
				candidate = fresh.FallbackAudio
			}
			if original == nil || candidate == nil || candidate.Quality != original.Quality || candidate.Codecs != original.Codecs {
				return nil, &bilibili.SiteError{Kind: bilibili.ErrKindUnavailable, Message: "续传地址的音质或编码发生变化，已保留断点"}
			}
			return candidate.URLs, nil
		}
	}
	var newMedia []library.MediaFile
	var vPath, aPath string
	var intermediate []library.MediaFile
	part := library.VideoPartRecord{
		CID:      page.CID,
		Page:     page.Page,
		Title:    page.Title,
		Duration: page.Duration,
		File:     mp4Rel,
		Video: library.PartVideoInfo{
			Quality:     plan.Video.Quality,
			QualityName: plan.Video.QualityName,
			CodecID:     plan.Video.CodecID,
			Codecs:      plan.Video.Codecs,
			Width:       plan.Video.Width,
			Height:      plan.Video.Height,
			FrameRate:   plan.Video.FrameRate,
		},
		Available: availableRecord(plan),
	}
	// Monotonic supplement protection on same-CID upgrades/completions (Invariant #4):
	// carry forward verified danmaku, subtitle tracks, and Hi-Res/Dolby sidecar audio!
	if old.CID == page.CID {
		if old.Danmaku != "" && mediaIsValid(meta, old.Danmaku) {
			part.Danmaku = old.Danmaku
			part.DanmakuState = "saved"
		}
		if len(old.Subtitles) > 0 || len(old.SubtitleTracks) > 0 {
			for _, subRel := range old.Subtitles {
				if mediaIsValid(meta, subRel) {
					part.Subtitles = append(part.Subtitles, subRel)
				}
			}
			for _, tr := range old.SubtitleTracks {
				if mediaIsValid(meta, tr.VTT) && mediaIsValid(meta, tr.SRT) {
					part.SubtitleTracks = append(part.SubtitleTracks, tr)
				}
			}
			if len(part.Subtitles) > 0 && old.SubtitleState == "saved" {
				part.SubtitleState = "saved"
			}
		}
		if len(old.Chapters) > 0 {
			part.Chapters = append([]library.VideoChapter(nil), old.Chapters...)
		}
		if old.ChaptersChecked || len(old.Chapters) > 0 {
			part.ChaptersChecked = true
		}
		if old.SubtitleIncludesAI {
			part.SubtitleIncludesAI = true
		}
	}

	var summary MuxTrackSummary
	var stagedReadyPath string
	var stageRelToClean string
	if plan.Mode == "dash" {
		// The DASH streams are only material for the MP4: they carry a
		// temporary name the whole time, so an export folder never shows a
		// finished-looking file that is not part of the item (remote.md §6.1).
		legacyV := fmt.Sprintf("P%02d.video-%d-%d.m4s", page.Page, plan.Video.Quality, plan.Video.CodecID)
		vRel := storage.TempPrefix + legacyV
		m.adoptLegacyName(t, meta.ItemDir, legacyV, vRel)
		vPath = filepath.Join(meta.ItemDir, vRel)
		m.setFileProgress(t, "video_stream", vRel, "downloading", 0, 0, 0)
		n, hash, err := m.downloadStreamWithRefresh(ctx, plan.Video.URLs, vPath, t, videoURLs)
		if err != nil {
			m.setFileProgress(t, "video_stream", vRel, "failed", t.BytesDownloaded, t.TotalBytes, 0)
			return err
		}
		intermediate = append(intermediate, library.MediaFile{Role: "video_stream", Path: vRel, Bytes: n, SHA256: hash})
		if !hasProtectedLocal {
			meta.Media = mergeMediaFiles(meta.Media, append(newMedia, intermediate...))
		}
		if plan.Audio != nil {
			part.Audio = library.PartAudioInfo{Quality: plan.Audio.Quality, Name: plan.Audio.Name, SavedAs: mp4Rel}
			mainAudio := plan.Audio
			isHigh := plan.Audio.Quality == 30250 || plan.Audio.Quality == 30251
			if isHigh {
				rel := fmt.Sprintf("P%02d.audio-%d.m4a", page.Page, plan.Audio.Quality)
				if mediaIsValid(meta, rel) {
					for _, mf := range meta.Media {
						if mf.Path == rel {
							part.Audio.SavedAs = rel
							newMedia = append(newMedia, mf)
							break
						}
					}
				} else {
					m.setFileProgress(t, "audio", rel, "downloading", 0, 0, 0)
					n, hash, err := m.downloadStreamWithRefresh(ctx, plan.Audio.URLs, filepath.Join(meta.ItemDir, rel), t, audioURLs(plan.Audio, false))
					if err != nil {
						m.setFileProgress(t, "audio", rel, "failed", 0, 0, 0)
						return err
					}
					part.Audio.SavedAs = rel
					newMedia = append(newMedia, library.MediaFile{Role: "audio", Path: rel, Bytes: n, SHA256: hash})
					if !hasProtectedLocal {
						meta.Media = mergeMediaFiles(meta.Media, newMedia)
					}
				}
				mainAudio = plan.FallbackAudio
				if mainAudio == nil {
					addMissing(&meta, fmt.Sprintf("P%02d.audio", page.Page), "最高音质已另存，但站点未提供可合并的普通音轨")
					return &bilibili.SiteError{Kind: bilibili.ErrKindUnavailable, Message: "最高音轨已保留，缺少可播放的普通音轨，主 MP4 尚未生成"}
				}
			} else if old.CID == page.CID && old.Audio.SavedAs != "" && old.Audio.SavedAs != old.File && mediaIsValid(meta, old.Audio.SavedAs) {
				// Preserve existing verified Hi-Res / Dolby sidecar audio monotonically!
				part.Audio = old.Audio
			}
			if mainAudio != nil {
				legacyA := fmt.Sprintf("P%02d.audio-%d.m4s", page.Page, mainAudio.Quality)
				rel := storage.TempPrefix + legacyA
				m.adoptLegacyName(t, meta.ItemDir, legacyA, rel)
				aPath = filepath.Join(meta.ItemDir, rel)
				m.setFileProgress(t, "audio_stream", rel, "downloading", 0, 0, 0)
				n, hash, err := m.downloadStreamWithRefresh(ctx, mainAudio.URLs, aPath, t, audioURLs(mainAudio, isHigh))
				if err != nil {
					m.setFileProgress(t, "audio_stream", rel, "failed", 0, 0, 0)
					return err
				}
				intermediate = append(intermediate, library.MediaFile{Role: "audio_stream", Path: rel, Bytes: n, SHA256: hash})
				clearMissing(&meta, fmt.Sprintf("P%02d.audio", page.Page))
			}
		}
		m.setFileProgress(t, "video", mp4Rel, "verifying", 0, 0, 0)
		if _, err = MuxDASHFilesContext(ctx, vPath, aPath, mp4Path+".part"); err != nil {
			m.setFileProgress(t, "video", mp4Rel, "failed", 0, 0, 0)
			return err
		}
		summary, err = InspectMP4FileContext(ctx, mp4Path+".part")
		if err == nil {
			err = validateVideoOutput(summary, page.Duration, aPath != "")
		}
		if err != nil {
			m.setFileProgress(t, "video", mp4Rel, "failed", 0, 0, 0)
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		stagedReadyPath = mp4Path + ".part"
	} else {
		stageRel := mp4Rel + stageSuffix
		stagePath := mp4Path + stageSuffix
		m.setFileProgress(t, "video", mp4Rel, "downloading", 0, 0, 0)
		if _, _, err = m.downloadStreamWithRefresh(ctx, plan.Video.URLs, stagePath, t, videoURLs); err != nil {
			if ctx.Err() == nil && !fileOrPartExists(stagePath) {
				m.removeTempTaskProgress(t, stageRel)
			}
			m.setFileProgress(t, "video", mp4Rel, "failed", 0, 0, 0)
			return err
		}
		m.setFileProgress(t, "video", mp4Rel, "verifying", 0, 0, 0)
		summary, err = EnsureSingleMP4Context(ctx, stagePath, float64(page.Duration))
		if err != nil {
			if ctx.Err() == nil {
				_ = os.Remove(stagePath)
				m.removeTempTaskProgress(t, stageRel)
				m.setFileProgress(t, "video", mp4Rel, "failed", 0, 0, 0)
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		stagedReadyPath = stagePath
		stageRelToClean = stageRel
	}

	prevSnapshot := meta
	applyVerifiedHeaderUpdates()
	m.setFileProgress(t, "video", mp4Rel, "completed", summary.FileBytes, summary.FileBytes, 0)
	newMedia = append(newMedia, library.MediaFile{Role: "video", Path: mp4Rel, Bytes: summary.FileBytes, SHA256: summary.SHA256, Width: summary.Width, Height: summary.Height, Duration: int(math.Round(summary.DurationSec))})
	for _, media := range intermediate {
		meta.Media = withoutMedia(meta.Media, media.Path)
	}
	// Drop any previously registered intermediate streams for this page (both
	// legacy Pxx.* and current .trace-Pxx.* names from an earlier quality/codec).
	legacyPrefix := fmt.Sprintf("P%02d.", page.Page)
	tempPrefix := storage.TempPrefix + legacyPrefix
	for _, media := range meta.Media {
		if (media.Role == "video_stream" || media.Role == "audio_stream") &&
			(strings.HasPrefix(media.Path, legacyPrefix) || strings.HasPrefix(media.Path, tempPrefix)) {
			intermediate = append(intermediate, media)
			meta.Media = withoutMedia(meta.Media, media.Path)
		}
	}
	meta.Media = mergeMediaFiles(meta.Media, newMedia)
	clearMissing(&meta, fmt.Sprintf("P%02d.media", page.Page))
	if cfg.SaveDanmaku && !mediaIsValid(meta, part.Danmaku) {
		part.DanmakuState = "pending"
		addMissing(&meta, fmt.Sprintf("P%02d.danmaku.xml", page.Page), "等待保存弹幕")
	}
	if cfg.SaveSubtitles && part.SubtitleState != "saved" {
		part.SubtitleState = "pending"
		addMissing(&meta, fmt.Sprintf("P%02d.subtitles", page.Page), "等待保存字幕")
	}
	meta.Parts = mergePartRecords(meta.Parts, part)

	// Swap verified MP4 over live path via .trace-prev-* backup & rollback, and
	// register the validated MP4 before any extra request can fail or expire auth.
	if err := swapVerifiedFileInPlace(meta.ItemDir, mp4Rel, stagedReadyPath, func() error {
		return m.saveVideoManifest(t, &meta, false)
	}); err != nil {
		meta = prevSnapshot
		return err
	}
	if stageRelToClean != "" {
		m.removeTempTaskProgress(t, stageRelToClean)
	}
	for _, media := range intermediate {
		_ = os.Remove(filepath.Join(meta.ItemDir, media.Path))
	}
	if hasProtectedLocal {
		if err := m.saveVideoCover(ctx, t, &meta, detail.Cover, cfg); err != nil {
			return err
		}
	}
	chaptersOK, extraErr := m.saveVideoExtras(ctx, t, &meta, &part, cfg)
	meta.Parts = mergePartRecords(meta.Parts, part)
	if extraErr != nil {
		return extraErr
	}
	return m.saveVideoManifest(t, &meta, chaptersOK && allPartsExtrasReady(meta, cfg))
}

func allPartsExtrasReady(meta library.ItemMetadata, cfg settings.DownloadSettings) bool {
	if len(meta.Parts) == 0 {
		return false
	}
	if cfg.MultiPart != "first" && meta.RemotePartCount > 0 && len(meta.Parts) < meta.RemotePartCount {
		return false
	}
	for _, part := range meta.Parts {
		if part.File == "" {
			return false
		}
		if !part.ChaptersChecked && len(part.Chapters) == 0 {
			return false
		}
		if cfg.SaveDanmaku && part.DanmakuState != "saved" {
			return false
		}
		if cfg.SaveSubtitles {
			if part.SubtitleState != "saved" {
				return false
			}
			if cfg.IncludeAI && !part.SubtitleIncludesAI {
				return false
			}
		} else if part.SubtitleState == "" || part.SubtitleState == "pending" {
			return false
		}
	}
	return true
}

func (m *Manager) saveVideoManifest(t *Task, meta *library.ItemMetadata, stampBiliStandard bool) error {
	meta.Status = "complete"
	meta.UpgradeAvailable, meta.UpgradeHint = false, ""
	for _, page := range meta.ExpectedPages {
		found := false
		for _, part := range meta.Parts {
			if part.Page == page && part.File != "" {
				found = true
			}
		}
		if !found {
			meta.Status = "partial"
		}
	}
	if len(meta.Missing) > 0 {
		meta.Status = "partial"
	}
	for _, part := range meta.Parts {
		if bilibili.QualityRank(part.Available.BestVideo) > bilibili.QualityRank(part.Video.Quality) || audioRank(part.Available.BestAudio) > audioRank(part.Audio.Quality) {
			meta.UpgradeAvailable, meta.UpgradeHint = true, "当前缓存低于站点可提供的最高档"
		}
	}
	stampCacheStandardAndAssets(meta, 0, 0, bilibiliVideoAssetIDsFromParts(meta.Parts))
	if stampBiliStandard && meta.Status == "complete" && len(meta.Missing) == 0 && meta.CacheStandardVersion < library.BilibiliVideoStandardVersion {
		meta.CacheStandardVersion = library.BilibiliVideoStandardVersion
	}
	saved, err := m.lib.SaveItem(meta.ItemDir, *meta)
	if err != nil {
		return err
	}
	*meta = saved
	t.ItemKey = saved.ItemKey
	m.saveTaskProgress(*t)
	return nil
}

func (m *Manager) saveVideoExtras(ctx context.Context, t *Task, meta *library.ItemMetadata, part *library.VideoPartRecord, cfg settings.DownloadSettings) (bool, error) {
	prefix := fmt.Sprintf("P%02d", part.Page)
	dmRel := prefix + ".danmaku.xml"
	if !cfg.SaveDanmaku {
		part.DanmakuState = "skipped"
		clearMissing(meta, dmRel)
		m.setFileProgress(t, "danmaku", dmRel, "skipped", 0, 0, 0)
	} else if !mediaIsValid(*meta, part.Danmaku) {
		m.setFileProgress(t, "danmaku", dmRel, "downloading", 0, 0, 0)
		raw, err := m.client.DanmakuXML(ctx, part.CID, part.Duration)
		if err != nil {
			part.DanmakuState = "missing"
			m.setFileProgress(t, "danmaku", dmRel, "failed", 0, 0, 0)
			addMissing(meta, dmRel, "弹幕保存失败 ("+bilibili.KindOf(err)+")")
			if isStopError(err) || ctx.Err() != nil {
				return false, err
			}
		} else {
			if err := m.writeExtra(ctx, t, meta, "danmaku", dmRel, raw); err != nil {
				return false, err
			}
			part.Danmaku, part.DanmakuState = dmRel, "saved"
			clearMissing(meta, dmRel)
		}
	} else {
		part.DanmakuState = "saved"
		clearMissing(meta, dmRel)
	}

	// Chapters (view_points) are always fetched on fresh parts or manifests below
	// BilibiliVideoStandardVersion even when cfg.SaveSubtitles is false (DEC-096).
	// Parts that already succeeded player/wbi/v2 (ChaptersChecked) skip redundant requests.
	freshPart := part.SubtitleState == "" || part.SubtitleState == "pending"
	needChapters := !part.ChaptersChecked && len(part.Chapters) == 0 && (freshPart || meta.CacheStandardVersion < library.BilibiliVideoStandardVersion)

	needsSubtitles := false
	if !cfg.SaveSubtitles {
		part.SubtitleState = "skipped"
		clearSubtitleMissing(meta, prefix)
		m.setFileProgress(t, "subtitle", prefix+".subtitles", "skipped", 0, 0, 0)
	} else {
		needsSubtitles = part.SubtitleState != "saved"
		if cfg.IncludeAI && !part.SubtitleIncludesAI {
			needsSubtitles = true
		}
		for _, missing := range meta.Missing {
			if subtitleMissingFor(missing, prefix) {
				needsSubtitles = true
			}
		}
		for _, rel := range part.Subtitles {
			if !mediaIsValid(*meta, rel) {
				needsSubtitles = true
			}
		}
	}

	if !needsSubtitles && !needChapters {
		return true, nil
	}

	var savedLanguages []string
	if needsSubtitles {
		for _, track := range part.SubtitleTracks {
			if mediaIsValid(*meta, track.VTT) && mediaIsValid(*meta, track.SRT) {
				savedLanguages = append(savedLanguages, track.Language)
			}
		}
	}
	extras, err := m.client.MissingSubtitlesAndChapters(ctx, meta.RawID, part.CID, cfg.IncludeAI, savedLanguages, needsSubtitles)
	if len(extras.Chapters) > 0 {
		part.Chapters = convertVideoChapters(extras.Chapters)
	}

	if !needsSubtitles {
		if err != nil {
			if isStopError(err) || ctx.Err() != nil {
				return false, err
			}
			return false, nil
		}
		part.ChaptersChecked = true
		return true, nil
	}

	tracks := extras.Subtitles
	clearSubtitleMissing(meta, prefix)
	for _, track := range tracks {
		lang := library.SanitizeFileName(track.Lan)
		if lang == "" {
			lang = "und"
		}
		if track.ErrorKind != "" {
			addMissing(meta, prefix+"."+lang+".subtitles", "字幕保存失败 ("+track.ErrorKind+")")
			continue
		}
		vtt, srt := prefix+"."+lang+".vtt", prefix+"."+lang+".srt"
		for _, file := range []struct {
			rel  string
			data []byte
		}{{vtt, track.VTT}, {srt, track.SRT}} {
			if err := m.writeExtra(ctx, t, meta, "subtitle", file.rel, file.data); err != nil {
				addMissing(meta, file.rel, "字幕写入失败")
				return false, err
			}
			if !containsString(part.Subtitles, file.rel) {
				part.Subtitles = append(part.Subtitles, file.rel)
			}
		}
		info := library.PartSubtitleInfo{Language: track.Lan, Name: track.LanDoc, AI: track.AI, VTT: vtt, SRT: srt}
		replaced := false
		for i := range part.SubtitleTracks {
			if part.SubtitleTracks[i].Language == track.Lan {
				part.SubtitleTracks[i], replaced = info, true
			}
		}
		if !replaced {
			part.SubtitleTracks = append(part.SubtitleTracks, info)
		}
	}
	part.SubtitleState = "saved"
	chaptersOK := err == nil
	if err == nil {
		part.ChaptersChecked = true
	}
	if err != nil {
		addMissing(meta, prefix+".subtitles", "字幕列表或下载失败 ("+bilibili.KindOf(err)+")")
		part.SubtitleState = "missing"
		if isStopError(err) || ctx.Err() != nil {
			return false, err
		}
	}
	for _, missing := range meta.Missing {
		if subtitleMissingFor(missing, prefix) {
			part.SubtitleState = "missing"
		}
	}
	if part.SubtitleState == "saved" && err == nil {
		if cfg.IncludeAI {
			part.SubtitleIncludesAI = true
		}
	}
	return chaptersOK, nil
}

func convertVideoChapters(in []bilibili.VideoChapter) []library.VideoChapter {
	if len(in) == 0 {
		return nil
	}
	out := make([]library.VideoChapter, 0, len(in))
	for _, ch := range in {
		title := strings.TrimSpace(ch.Title)
		if title == "" || ch.FromSec < 0 || ch.ToSec <= ch.FromSec {
			continue
		}
		out = append(out, library.VideoChapter{
			FromSec: ch.FromSec,
			ToSec:   ch.ToSec,
			Title:   title,
		})
	}
	return out
}

func (m *Manager) writeExtra(ctx context.Context, t *Task, meta *library.ItemMetadata, role, rel string, raw []byte) error {
	full, err := library.ResolveItemPath(meta.ItemDir, rel)
	if err != nil {
		return err
	}
	if err := m.claimTaskFile(t, meta.ItemDir, rel, full); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.setFileProgress(t, role, rel, "verifying", 0, int64(len(raw)), 0)
	if err := storage.WriteFileAtomic(full+".part", raw, 0600); err != nil {
		m.setFileProgress(t, role, rel, "failed", 0, int64(len(raw)), 0)
		return err
	}
	if err := os.Rename(full+".part", full); err != nil {
		return err
	}
	hash := sha256.Sum256(raw)
	meta.Media = mergeMediaFiles(meta.Media, []library.MediaFile{{Role: role, Path: rel, Bytes: int64(len(raw)), SHA256: hex.EncodeToString(hash[:])}})
	m.setFileProgress(t, role, rel, "completed", int64(len(raw)), int64(len(raw)), 0)
	return m.saveTaskProgress(*t)
}

func (m *Manager) executeSupplementTask(ctx context.Context, t *Task) error {
	unlock, err := m.lockItem(ctx, t.TargetID)
	if err != nil {
		return err
	}
	defer unlock()
	meta, ok, err := m.lib.GetItem(t.TargetID)
	if err != nil {
		return err
	}
	if !ok {
		return &bilibili.SiteError{Kind: bilibili.ErrKindUnavailable, Message: "需要先缓存视频再补存弹幕和字幕"}
	}
	t.ItemDir, t.ItemKey = meta.ItemDir, meta.ItemKey

	storedMode, selectedPages := m.taskResolutionAndPages(t.TaskID)
	mode := effectiveConflictResolution(ctx, t.ConflictResolution, storedMode)

	// Reuse pre-fetched VideoDetail passed by queue probe when available so
	// supplement avoids duplicate VideoDetail requests (DEC-096).
	if preDetail, hasPre := m.takeSupplementDetail(t.TaskID); hasPre && len(preDetail.Pages) > 0 {
		meta.RemotePartCount = len(preDetail.Pages)
	} else if detail, dErr := m.client.VideoDetail(ctx, meta.RawID); dErr != nil {
		if localHasProtectedContent(meta) && isDegradedSiteError(dErr) {
			return wrapRemoteDegraded(dErr)
		}
		if isStopError(dErr) || ctx.Err() != nil {
			return dErr
		}
	} else {
		if len(detail.Pages) > 0 {
			meta.RemotePartCount = len(detail.Pages)
		}
		if localHasProtectedContent(meta) {
			if mode == ConflictResolveKeepBoth || mode == ConflictResolveUseRemote {
				return m.executeBilibiliVideoConflictReplace(ctx, t, detail, meta, mode, selectedPages)
			}
			notice, isConflict, degErr := classifyBilibiliVideoVsLocal(meta, detail, t)
			if degErr != nil {
				return degErr
			}
			if isConflict {
				m.emitConflict(notice)
				return ErrConflictDetected
			}
		}
	}

	defer func() {
		if len(meta.Missing) > 0 {
			t.Status = "partial"
		}
	}()
	m.mu.Lock()
	cfg := m.dlSettings
	m.mu.Unlock()
	allChaptersOK := len(meta.Parts) > 0
	for i := range meta.Parts {
		if meta.Parts[i].CID <= 0 {
			continue
		}
		chaptersOK, err := m.saveVideoExtras(ctx, t, &meta, &meta.Parts[i], cfg)
		allChaptersOK = allChaptersOK && chaptersOK
		if saveErr := m.saveVideoManifest(t, &meta, false); saveErr != nil {
			return saveErr
		}
		if err != nil {
			return err
		}
	}
	allSaved := true
	for _, part := range meta.Parts {
		allSaved = allSaved && (part.SubtitleState == "saved" || part.SubtitleState == "skipped")
	}
	if allSaved {
		clearMissing(&meta, "subtitles")
	}
	stampV3 := allChaptersOK && allSaved && len(meta.Missing) == 0 && allPartsExtrasReady(meta, cfg)
	if stampV3 {
		nowStr := time.Now().UTC().Format(time.RFC3339)
		meta.RecheckedAt = nowStr
		meta.UpgradeCheckedAt = nowStr
	}
	return m.saveVideoManifest(t, &meta, stampV3)
}

func (m *Manager) executeUpgradeTask(ctx context.Context, t *Task) error {
	meta, ok, err := m.lib.GetItem(t.TargetID)
	if err != nil {
		return err
	}
	if !ok {
		return &bilibili.SiteError{Kind: bilibili.ErrKindUnavailable, Message: "要升级的视频不在媒体库中"}
	}
	t.ItemDir, t.ItemKey = meta.ItemDir, meta.ItemKey

	storedMode, selectedPages := m.taskResolutionAndPages(t.TaskID)
	mode := effectiveConflictResolution(ctx, t.ConflictResolution, storedMode)
	if mode == ConflictResolveKeepBoth || mode == ConflictResolveUseRemote {
		unlock, err := m.lockItem(ctx, t.TargetID)
		if err != nil {
			return err
		}
		defer unlock()
		detail, dErr := m.client.VideoDetail(ctx, meta.RawID)
		if dErr != nil {
			if localHasProtectedContent(meta) && isDegradedSiteError(dErr) {
				return wrapRemoteDegraded(dErr)
			}
			return dErr
		}
		return m.executeBilibiliVideoConflictReplace(ctx, t, detail, meta, mode, selectedPages)
	}

	for _, part := range meta.Parts {
		child := Task{Site: "bilibili", Kind: "video", TargetID: meta.RawID, CID: part.CID, Page: part.Page, Source: t.Source, ConflictResolution: mode, LinkedVideo: !meta.DirectCached, ProgressOwner: t, Attempts: t.Attempts}
		if err := m.cacheVideoPart(ctx, &child, true); err != nil {
			t.Attempts = child.Attempts
			return err
		}
		t.Attempts = child.Attempts
		if child.Status == "partial" {
			t.Status = "partial"
		}
	}
	return nil
}

func validateVideoOutput(summary MuxTrackSummary, duration int, audio bool) error {
	videoFound, audioFound := false, false
	for _, handler := range summary.Handlers {
		videoFound = videoFound || handler == "vide"
		audioFound = audioFound || handler == "soun"
	}
	reason := ""
	switch {
	case !videoFound:
		reason = "合并后的 MP4 没有视频轨"
	case audio && !audioFound:
		reason = "合并后的 MP4 没有音频轨"
	case !summary.MoovBeforeMdat || !summary.HasElst:
		reason = "合并后的 MP4 结构不对"
	case summary.DurationSec <= 0:
		reason = "合并后的 MP4 时长为 0"
	default:
		reason = durationMismatch(summary.DurationSec, float64(duration))
	}
	if reason != "" {
		return &bilibili.SiteError{Kind: bilibili.ErrKindSchemaChanged, Message: reason + "，已保留旧文件和中间流"}
	}
	return nil
}

func availableRecord(plan bilibili.PlayPlan) library.PartAvailableInfo {
	a := plan.Available
	return library.PartAvailableInfo{AcceptQuality: a.AcceptQuality, Formats: a.Formats, Returned: a.Returned, BestVideo: a.BestVideo, BestAudio: a.BestAudio, HasDolby: a.HasDolby, HasFlac: a.HasFlac, ProbedAt: a.ProbedAt, Downgraded: a.Downgraded, DowngradeHint: a.DowngradeHint}
}

func mediaIsValid(meta library.ItemMetadata, rel string) bool {
	if rel == "" {
		return false
	}
	for _, file := range meta.Media {
		if file.Path == rel {
			return library.VerifyMediaFile(meta.ItemDir, file, true)
		}
	}
	return false
}
func containsPage(pages []int, page int) bool {
	for _, value := range pages {
		if value == page {
			return true
		}
	}
	return false
}
func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
func withoutMedia(files []library.MediaFile, path string) []library.MediaFile {
	var result []library.MediaFile
	for _, file := range files {
		if file.Path != path {
			result = append(result, file)
		}
	}
	return result
}
func addMissing(meta *library.ItemMetadata, item, reason string) {
	clearMissing(meta, item)
	meta.Missing = append(meta.Missing, library.MissingRecord{Item: item, Reason: reason})
}
func clearMissing(meta *library.ItemMetadata, item string) {
	var keep []library.MissingRecord
	for _, missing := range meta.Missing {
		if missing.Item != item {
			keep = append(keep, missing)
		}
	}
	meta.Missing = keep
}
func subtitleMissingFor(missing library.MissingRecord, prefix string) bool {
	return strings.HasPrefix(missing.Item, prefix+".") && (strings.Contains(missing.Item, "subtitle") || strings.HasSuffix(missing.Item, ".vtt") || strings.HasSuffix(missing.Item, ".srt")) || missing.Item == "subtitles" && strings.HasPrefix(missing.Reason, prefix)
}
func clearSubtitleMissing(meta *library.ItemMetadata, prefix string) {
	var keep []library.MissingRecord
	for _, missing := range meta.Missing {
		if !subtitleMissingFor(missing, prefix) {
			keep = append(keep, missing)
		}
	}
	meta.Missing = keep
}
