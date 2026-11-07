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
	"sync"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
)

func (m *Manager) executeVideoTask(ctx context.Context, task *Task) error {
	return m.cacheVideoPart(ctx, task, false)
}

// All operations on one video's manifest share this lock, including supplement
// and upgrade. Different pages cannot overwrite one another's successful work.
func (m *Manager) lockVideo(id string) func() {
	lock, _ := m.videoLocks.LoadOrStore(id, &sync.Mutex{})
	lock.(*sync.Mutex).Lock()
	return lock.(*sync.Mutex).Unlock
}

func (m *Manager) cacheVideoPart(ctx context.Context, t *Task, upgrade bool) (result error) {
	unlock := m.lockVideo(t.TargetID)
	defer unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	cfg := m.dlSettings
	m.mu.Unlock()
	detail, err := m.client.VideoDetail(ctx, t.TargetID)
	if err != nil {
		return err
	}
	var page bilibili.VideoPage
	for _, candidate := range detail.Pages {
		if t.CID > 0 && candidate.CID == t.CID || t.CID == 0 && candidate.Page == max(1, t.Page) {
			page = candidate
			break
		}
	}
	if page.CID <= 0 {
		return &bilibili.SiteError{Kind: bilibili.ErrKindUnavailable, Message: "请求的分 P 已不可用"}
	}
	t.CID, t.Page, t.Title, t.PartTitle = page.CID, page.Page, detail.Title, page.Title
	t.Mid, t.AuthorName, t.AuthorFace, t.CoverURL = detail.Author.Mid, detail.Author.Name, detail.Author.Face, detail.Cover
	meta, exists, err := m.lib.GetItem(detail.BVID)
	if err != nil {
		return err
	}
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
	}
	if err := os.MkdirAll(meta.ItemDir, 0700); err != nil {
		return err
	}
	t.ItemDir, t.ItemKey = meta.ItemDir, library.CanonicalItemKey("bilibili", "video", detail.BVID)
	meta.Title, meta.Desc, meta.URL, meta.PubTs, meta.Duration = detail.Title, detail.Desc, detail.CanonicalURL, detail.PubTs, detail.Duration
	meta.Author = library.ItemAuthor{Mid: detail.Author.Mid, Name: detail.Author.Name, Face: detail.Author.Face}
	meta.DirectCached = meta.DirectCached || !t.LinkedVideo
	for _, candidate := range detail.Pages {
		if cfg.MultiPart == "all" || candidate.Page == page.Page {
			if !containsPage(meta.ExpectedPages, candidate.Page) {
				meta.ExpectedPages = append(meta.ExpectedPages, candidate.Page)
			}
		}
	}
	var old library.VideoPartRecord
	for _, part := range meta.Parts {
		if part.Page == page.Page {
			old = part
			break
		}
	}
	// Persist the expected pages before media requests, so a paused first page
	// never leaves a green item claiming to contain the whole video.
	if err := m.saveVideoManifest(t, &meta); err != nil {
		return err
	}
	defer func() {
		if result != nil {
			if !isStopError(result) && !mediaIsValid(meta, fmt.Sprintf("P%02d.mp4", page.Page)) && !mediaIsValid(meta, old.File) {
				addMissing(&meta, fmt.Sprintf("P%02d.media", page.Page), "分 P 尚未完成 ("+bilibili.KindOf(result)+")")
			}
			if saveErr := m.saveVideoManifest(t, &meta); saveErr != nil {
				result = saveErr
			}
		}
		if result == nil && len(meta.Missing) > 0 {
			t.Status = "partial"
		}
	}()
	if err := m.saveVideoCover(ctx, t, &meta, detail.Cover, cfg); err != nil {
		return err
	}
	if !upgrade && old.File != "" && mediaIsValid(meta, old.File) {
		for _, media := range meta.Media {
			if media.Path == old.File || strings.HasPrefix(media.Path, fmt.Sprintf("P%02d.", page.Page)) {
				m.setFileProgress(t, media.Role, media.Path, "completed", media.Bytes, media.Bytes, 0)
			}
		}
		extraErr := m.saveVideoExtras(ctx, t, &meta, &old, cfg)
		meta.Parts = mergePartRecords(meta.Parts, old)
		if extraErr != nil {
			return extraErr
		}
		return m.saveVideoManifest(t, &meta)
	}
	opts := bilibili.PlaySelectionOptions{PreferredCodec: cfg.PreferredVideoCodec, CodecOrder: cfg.CodecOrder, PreferLosslessAudio: cfg.PreferLosslessAudio, PreferDolbyAudio: cfg.PreferDolbyAudio, TargetQn: targetQuality(cfg.VideoQuality)}
	if upgrade && bilibili.QualityRank(old.Video.Quality) > bilibili.QualityRank(opts.TargetQn) {
		opts.TargetQn = old.Video.Quality
	}
	plan, err := m.client.PlayURL(ctx, detail.BVID, page.CID, opts)
	if err != nil {
		return err
	}
	if upgrade {
		if bilibili.QualityRank(plan.Video.Quality) < bilibili.QualityRank(old.Video.Quality) || old.Audio.Quality > 0 && (plan.Audio == nil || audioRank(plan.Audio.Quality) < audioRank(old.Audio.Quality)) {
			return &bilibili.SiteError{Kind: bilibili.ErrKindUnavailable, Message: "站点当前无法提供不低于旧缓存的画质与音质，旧文件已保留"}
		}
		if bilibili.QualityRank(plan.Video.Quality) <= bilibili.QualityRank(old.Video.Quality) && (plan.Audio == nil || audioRank(plan.Audio.Quality) <= audioRank(old.Audio.Quality)) {
			old.Available = availableRecord(plan)
			meta.Parts = mergePartRecords(meta.Parts, old)
			return m.saveVideoManifest(t, &meta)
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
	part := library.VideoPartRecord{CID: page.CID, Page: page.Page, Title: page.Title, Duration: page.Duration, File: mp4Rel, Video: library.PartVideoInfo{Quality: plan.Video.Quality, QualityName: plan.Video.QualityName, CodecID: plan.Video.CodecID, Codecs: plan.Video.Codecs, Width: plan.Video.Width, Height: plan.Video.Height, FrameRate: plan.Video.FrameRate}, Available: availableRecord(plan)}
	if plan.Mode == "dash" {
		vRel := fmt.Sprintf("P%02d.video-%d-%d.m4s", page.Page, plan.Video.Quality, plan.Video.CodecID)
		vPath = filepath.Join(meta.ItemDir, vRel)
		m.setFileProgress(t, "video_stream", vRel, "downloading", 0, 0, 0)
		n, hash, err := m.downloadStreamWithRefresh(ctx, plan.Video.URLs, vPath, t, videoURLs)
		if err != nil {
			m.setFileProgress(t, "video_stream", vRel, "failed", t.BytesDownloaded, t.TotalBytes, 0)
			return err
		}
		intermediate = append(intermediate, library.MediaFile{Role: "video_stream", Path: vRel, Bytes: n, SHA256: hash})
		meta.Media = mergeMediaFiles(meta.Media, append(newMedia, intermediate...))
		if plan.Audio != nil {
			part.Audio = library.PartAudioInfo{Quality: plan.Audio.Quality, Name: plan.Audio.Name, SavedAs: mp4Rel}
			mainAudio := plan.Audio
			isHigh := plan.Audio.Quality == 30250 || plan.Audio.Quality == 30251
			if isHigh {
				rel := fmt.Sprintf("P%02d.audio-%d.m4a", page.Page, plan.Audio.Quality)
				m.setFileProgress(t, "audio", rel, "downloading", 0, 0, 0)
				n, hash, err := m.downloadStreamWithRefresh(ctx, plan.Audio.URLs, filepath.Join(meta.ItemDir, rel), t, audioURLs(plan.Audio, false))
				if err != nil {
					m.setFileProgress(t, "audio", rel, "failed", 0, 0, 0)
					return err
				}
				part.Audio.SavedAs = rel
				newMedia = append(newMedia, library.MediaFile{Role: "audio", Path: rel, Bytes: n, SHA256: hash})
				meta.Media = mergeMediaFiles(meta.Media, newMedia)
				mainAudio = plan.FallbackAudio
				if mainAudio == nil {
					addMissing(&meta, fmt.Sprintf("P%02d.audio", page.Page), "最高音质已另存，但站点未提供可合并的普通音轨")
					return &bilibili.SiteError{Kind: bilibili.ErrKindUnavailable, Message: "最高音轨已保留，缺少可播放的普通音轨，主 MP4 尚未生成"}
				}
			}
			if mainAudio != nil {
				rel := fmt.Sprintf("P%02d.audio-%d.m4s", page.Page, mainAudio.Quality)
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
		_, err = MuxDASHFilesContext(ctx, vPath, aPath, mp4Path+".part")
	} else {
		m.setFileProgress(t, "video", mp4Rel, "downloading", 0, 0, 0)
		_, _, err = m.downloadStreamWithRefresh(ctx, plan.Video.URLs, mp4Path+".part", t, videoURLs)
	}
	if err != nil {
		m.setFileProgress(t, "video", mp4Rel, "failed", 0, 0, 0)
		return err
	}
	summary, err := InspectMP4FileContext(ctx, mp4Path+".part")
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
	if err := os.Rename(mp4Path+".part", mp4Path); err != nil {
		return err
	}
	m.setFileProgress(t, "video", mp4Rel, "completed", summary.FileBytes, summary.FileBytes, 0)
	newMedia = append(newMedia, library.MediaFile{Role: "video", Path: mp4Rel, Bytes: summary.FileBytes, SHA256: summary.SHA256, Width: summary.Width, Height: summary.Height, Duration: int(math.Round(summary.DurationSec))})
	for _, media := range intermediate {
		meta.Media = withoutMedia(meta.Media, media.Path)
	}
	meta.Media = mergeMediaFiles(meta.Media, newMedia)
	clearMissing(&meta, fmt.Sprintf("P%02d.media", page.Page))
	if cfg.SaveDanmaku {
		part.DanmakuState = "pending"
		addMissing(&meta, fmt.Sprintf("P%02d.danmaku.xml", page.Page), "等待保存弹幕")
	}
	if cfg.SaveSubtitles {
		part.SubtitleState = "pending"
		addMissing(&meta, fmt.Sprintf("P%02d.subtitles", page.Page), "等待保存字幕")
	}
	meta.Parts = mergePartRecords(meta.Parts, part)
	// Register the validated MP4 before any extra request can fail or expire auth.
	if err := m.saveVideoManifest(t, &meta); err != nil {
		return err
	}
	for _, media := range intermediate {
		_ = os.Remove(filepath.Join(meta.ItemDir, media.Path))
	}
	if err := m.saveVideoExtras(ctx, t, &meta, &part, cfg); err != nil {
		meta.Parts = mergePartRecords(meta.Parts, part)
		return err
	}
	meta.Parts = mergePartRecords(meta.Parts, part)
	return m.saveVideoManifest(t, &meta)
}

func (m *Manager) saveVideoManifest(t *Task, meta *library.ItemMetadata) error {
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
	saved, err := m.lib.SaveItem(meta.ItemDir, *meta)
	if err != nil {
		return err
	}
	*meta = saved
	t.ItemKey = saved.ItemKey
	m.saveTaskProgress(*t)
	return nil
}

func (m *Manager) saveVideoExtras(ctx context.Context, t *Task, meta *library.ItemMetadata, part *library.VideoPartRecord, cfg settings.DownloadSettings) error {
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
				return err
			}
		} else {
			if err := m.writeExtra(t, meta, "danmaku", dmRel, raw); err != nil {
				return err
			}
			part.Danmaku, part.DanmakuState = dmRel, "saved"
			clearMissing(meta, dmRel)
		}
	} else {
		part.DanmakuState = "saved"
		clearMissing(meta, dmRel)
	}
	if !cfg.SaveSubtitles {
		part.SubtitleState = "skipped"
		clearSubtitleMissing(meta, prefix)
		m.setFileProgress(t, "subtitle", prefix+".subtitles", "skipped", 0, 0, 0)
		return nil
	}
	needsSubtitles := part.SubtitleState != "saved"
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
	if !needsSubtitles {
		return nil
	}
	var savedLanguages []string
	for _, track := range part.SubtitleTracks {
		if mediaIsValid(*meta, track.VTT) && mediaIsValid(*meta, track.SRT) {
			savedLanguages = append(savedLanguages, track.Language)
		}
	}
	tracks, err := m.client.MissingSubtitles(ctx, meta.RawID, part.CID, cfg.IncludeAI, savedLanguages)
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
			if err := m.writeExtra(t, meta, "subtitle", file.rel, file.data); err != nil {
				addMissing(meta, file.rel, "字幕写入失败")
				return err
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
	if err != nil {
		addMissing(meta, prefix+".subtitles", "字幕列表或下载失败 ("+bilibili.KindOf(err)+")")
		part.SubtitleState = "missing"
		if isStopError(err) || ctx.Err() != nil {
			return err
		}
	}
	for _, missing := range meta.Missing {
		if subtitleMissingFor(missing, prefix) {
			part.SubtitleState = "missing"
		}
	}
	return nil
}

func (m *Manager) writeExtra(t *Task, meta *library.ItemMetadata, role, rel string, raw []byte) error {
	full, err := library.ResolveItemPath(meta.ItemDir, rel)
	if err != nil {
		return err
	}
	if err := m.claimTaskFile(t, meta.ItemDir, rel, full); err != nil {
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
	unlock := m.lockVideo(t.TargetID)
	defer unlock()
	meta, ok, err := m.lib.GetItem(t.TargetID)
	if err != nil {
		return err
	}
	if !ok {
		return &bilibili.SiteError{Kind: bilibili.ErrKindUnavailable, Message: "需要先缓存视频再补存弹幕和字幕"}
	}
	t.ItemDir, t.ItemKey = meta.ItemDir, meta.ItemKey
	defer func() {
		if len(meta.Missing) > 0 {
			t.Status = "partial"
		}
	}()
	m.mu.Lock()
	cfg := m.dlSettings
	m.mu.Unlock()
	for i := range meta.Parts {
		if meta.Parts[i].CID <= 0 {
			continue
		}
		err := m.saveVideoExtras(ctx, t, &meta, &meta.Parts[i], cfg)
		if saveErr := m.saveVideoManifest(t, &meta); saveErr != nil {
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
	return m.saveVideoManifest(t, &meta)
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
	for _, part := range meta.Parts {
		child := Task{Site: "bilibili", Kind: "video", TargetID: meta.RawID, CID: part.CID, Page: part.Page, Source: t.Source, LinkedVideo: !meta.DirectCached, ProgressOwner: t, Attempts: t.Attempts}
		if err := m.cacheVideoPart(ctx, &child, true); err != nil {
			return err
		}
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
	if !videoFound || audio && !audioFound || !summary.MoovBeforeMdat || !summary.HasElst || summary.DurationSec <= 0 || duration > 0 && math.Abs(summary.DurationSec-float64(duration)) > 2 {
		return &bilibili.SiteError{Kind: bilibili.ErrKindSchemaChanged, Message: "MP4 轨道、结构或时长校验失败，已保留旧文件和中间流"}
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
