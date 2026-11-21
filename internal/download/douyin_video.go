package download

import (
	"context"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
)

const (
	douyinOriginalGear    = "original"
	douyinOriginalQuality = "原画"
)

func (m *Manager) executeDouyinVideoTask(ctx context.Context, t *Task, plan WorkDownloadPlan, planner WorkPlanner) (result error) {
	previous, exists, itemDir, err := m.resolveDouyinItemDir(t, "video", plan.AuthorUID, t.TargetID)
	if err != nil {
		return err
	}

	m.mu.Lock()
	cfg := m.dlSettings
	m.mu.Unlock()

	title := firstNonEmptyStr(plan.Title, plan.Desc, t.Title, "抖音视频 "+t.TargetID)
	canonicalURL := firstNonEmptyStr(sanitizeStoredURL(plan.CanonicalURL), "https://www.douyin.com/video/"+t.TargetID)
	t.Title = title
	t.AuthorName = firstNonEmptyStr(plan.AuthorName, t.AuthorName)
	t.AuthorFace = sanitizeStoredURL(firstNonEmptyStr(plan.AuthorFace, t.AuthorFace))
	if len(plan.CoverURLs) > 0 {
		t.CoverURL = sanitizeStoredURL(plan.CoverURLs[0])
	}
	if plan.DowngradeHint != "" {
		t.QualityHint = plan.DowngradeHint
	}

	meta := library.ItemMetadata{
		Version:  1,
		ID:       "video:" + t.TargetID,
		Site:     "douyin",
		Kind:     "video",
		Status:   "partial",
		Title:    title,
		Desc:     plan.Desc,
		URL:      canonicalURL,
		PubTs:    plan.PubTs,
		CachedAt: previous.CachedAt,
		Duration: plan.DurationSec,
		Author: library.ItemAuthor{
			Site:     "douyin",
			UID:      plan.AuthorUID,
			UniqueID: plan.AuthorUniqueID,
			Name:     t.AuthorName,
			Face:     t.AuthorFace,
		},
		Media:        append([]library.MediaFile(nil), previous.Media...),
		Missing:      append([]library.MissingRecord(nil), previous.Missing...),
		DirectCached: true,
		Via:          m.douyinItemVia(t, previous, exists, itemDir),
	}

	finalSaved := false
	defer func() {
		if result == nil || finalSaved {
			return
		}
		if !isSiteStopError(result) && !mediaIsValid(meta, "video.mp4") {
			addMissing(&meta, "video.mp4", "视频尚未完成 ("+classifyDownloadError(result)+")")
		}
		meta.Status = "partial"
		saved, sErr := m.lib.SaveItem(itemDir, meta)
		if sErr != nil {
			if !isSiteStopError(result) && ctx.Err() == nil {
				result = sErr
			}
			return
		}
		t.ItemKey = saved.ItemKey
	}()

	refreshOncePlan := m.makeSinglePlanRefresher(ctx, t.TargetID, planner)

	// 1. Save cover.<ext> when enabled.
	if cfg.SaveCover && len(plan.CoverURLs) > 0 {
		refreshCover := makeOnceURLRefresher(refreshOncePlan, func(fresh WorkDownloadPlan) ([]string, error) {
			return fresh.CoverURLs, nil
		})
		if coverMedia, cErr := m.cacheDouyinSniffedImage(ctx, t, itemDir, previous, exists, "cover", "cover", plan.CoverURLs, 0, 0, plan.Transport, refreshCover); cErr != nil {
			if isSiteStopError(cErr) || isStorageError(cErr) || ctx.Err() != nil {
				return cErr
			}
			addMissing(&meta, "cover", "封面保存失败 ("+classifyDownloadError(cErr)+")")
		} else if coverMedia != nil {
			meta.Media = replaceRoleMedia(meta.Media, "cover", *coverMedia)
			clearMissing(&meta, "cover")
		}
	}

	// 2. Download & validate single-file video.mp4 (including bounded-memory moov relocation).
	videoRel := "video.mp4"
	videoPath, err := library.ResolveItemPath(itemDir, videoRel)
	if err != nil {
		return err
	}
	if exists && mediaIsValid(previous, videoRel) {
		for _, mf := range previous.Media {
			if mf.Path == videoRel {
				m.setFileProgress(t, "video", videoRel, "completed", mf.Bytes, mf.Bytes, 0)
				meta.Media = mergeMediaFiles(meta.Media, []library.MediaFile{mf})
				clearMissing(&meta, videoRel)
				break
			}
		}
	} else {
		if err := m.claimTaskFile(t, itemDir, videoRel, videoPath); err != nil {
			return err
		}
		m.setFileProgress(t, "video", videoRel, "downloading", 0, 0, 0)
		refreshVideoURLs := makeOnceURLRefresher(refreshOncePlan, func(fresh WorkDownloadPlan) ([]string, error) {
			if plan.Codec != "" && fresh.Codec != "" && !strings.EqualFold(plan.Codec, fresh.Codec) {
				return nil, &site.Error{Site: site.Douyin, Kind: site.ErrKindUnavailable, Message: "续传地址的视频编码发生变化，已保留断点"}
			}
			if plan.VideoGear != "" && fresh.VideoGear != "" && plan.VideoGear != fresh.VideoGear {
				return nil, &site.Error{Site: site.Douyin, Kind: site.ErrKindUnavailable, Message: "续传地址的清晰度档位发生变化，已保留断点"}
			}
			return fresh.VideoURLs, nil
		})
		rawRel := videoRel + stageSuffix
		stagingPath := videoPath + stageSuffix
		m.adoptLegacyStaging(t, itemDir, videoRel, ".raw", stageSuffix)
		var summary MuxTrackSummary
		original := false
		if len(plan.OriginalURLs) > 0 {
			origSummary, oErr := m.cacheDouyinOriginal(ctx, t, plan, refreshOncePlan, videoRel, videoPath)
			switch {
			case oErr == nil:
				summary, original = origSummary, true
				// A transcoded attempt from an earlier run is no longer needed.
				_ = os.Remove(stagingPath)
				_ = os.Remove(stagingPath + ".part")
				m.removeTempTaskProgress(t, rawRel)
			case ctx.Err() != nil || site.IsRoundStop(oErr) || isStorageError(oErr):
				m.setFileProgress(t, "video", videoRel, "failed", t.BytesDownloaded, t.TotalBytes, 0)
				return oErr
			}
			// Any other failure (including networkPauseError on OriginalURLs) falls back to VideoURLs below.
		}
		if !original {
			_, _, dlErr := m.downloadDouyinMP4WithFallback(ctx, plan.Transport, plan.VideoURLs, plan.NeedSetToken, stagingPath, "video", t, planner, refreshVideoURLs)
			if dlErr != nil {
				if ctx.Err() == nil && !fileOrPartExists(stagingPath) {
					m.removeTempTaskProgress(t, rawRel)
				}
				m.setFileProgress(t, "video", videoRel, "failed", t.BytesDownloaded, t.TotalBytes, 0)
				return dlErr
			}

			m.setFileProgress(t, "video", videoRel, "verifying", t.BytesDownloaded, t.TotalBytes, 0)
			var vErr error
			summary, vErr = EnsureSingleMP4Context(ctx, stagingPath, float64(plan.DurationSec))
			if vErr != nil {
				if ctx.Err() == nil {
					_ = os.Remove(stagingPath)
					m.removeTempTaskProgress(t, rawRel)
					m.setFileProgress(t, "video", videoRel, "failed", 0, 0, 0)
				}
				return vErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := os.Rename(stagingPath, videoPath); err != nil {
				return err
			}
			m.removeTempTaskProgress(t, rawRel)
		}
		durSec := int(math.Round(summary.DurationSec))
		if durSec <= 0 {
			durSec = plan.DurationSec
		}
		meta.Duration = durSec
		width, height := summary.Width, summary.Height
		if width <= 0 {
			width = plan.Width
		}
		if height <= 0 {
			height = plan.Height
		}
		videoMedia := library.MediaFile{
			Role:     "video",
			Path:     videoRel,
			Bytes:    summary.FileBytes,
			SHA256:   summary.SHA256,
			Width:    width,
			Height:   height,
			Duration: durSec,
		}
		m.setFileProgress(t, "video", videoRel, "completed", summary.FileBytes, summary.FileBytes, 0)
		meta.Media = mergeMediaFiles(meta.Media, []library.MediaFile{videoMedia})
		clearMissing(&meta, videoRel)
		meta.DouyinVideo = &library.DouyinVideoInfo{
			File:           videoRel,
			Gear:           plan.VideoGear,
			QualityName:    plan.QualityName,
			Codec:          firstNonEmptyStr(plan.Codec, summaryPrimaryVideoCodec(summary)),
			Width:          width,
			Height:         height,
			FPS:            plan.FPS,
			Bitrate:        plan.Bitrate,
			AvailableGears: append([]string(nil), plan.AvailableGears...),
		}
		if original {
			info := meta.DouyinVideo
			info.Gear, info.QualityName = douyinOriginalGear, douyinOriginalQuality
			info.Codec = firstNonEmptyStr(summaryPrimaryVideoCodec(summary), plan.Codec)
			info.FPS = 0
			info.Bitrate = 0
			if summary.DurationSec > 0 {
				info.Bitrate = int64(float64(summary.FileBytes*8) / summary.DurationSec)
			}
			info.AvailableGears = append([]string{douyinOriginalQuality}, plan.AvailableGears...)
		}
	}

	if meta.DouyinVideo == nil && previous.DouyinVideo != nil {
		cp := *previous.DouyinVideo
		meta.DouyinVideo = &cp
	}

	meta.Status = "complete"
	if len(meta.Missing) > 0 {
		meta.Status = "partial"
		t.Status = "partial"
		t.ErrorMessage = summarizeMissingReasons(meta.Missing)
	}
	saved, err := m.lib.SaveItem(itemDir, meta)
	if err != nil {
		return err
	}
	t.ItemKey = saved.ItemKey
	finalSaved = true
	return nil
}

// cacheDouyinOriginal downloads the untranscoded source (原画) into its own staging
// file and validates it as a single progressive MP4, then moves it to videoPath.
// On a failure the caller falls back to the transcoded stream, so the staging file
// is removed unless the task was cancelled or paused (kept for resume).
func (m *Manager) cacheDouyinOriginal(
	ctx context.Context,
	t *Task,
	plan WorkDownloadPlan,
	refreshPlan func() (WorkDownloadPlan, error),
	videoRel, videoPath string,
) (MuxTrackSummary, error) {
	origRel := videoRel + origSuffix
	origPath := videoPath + origSuffix
	m.adoptLegacyStaging(t, filepath.Dir(videoPath), videoRel, ".orig.raw", origSuffix)
	discard := func() {
		if ctx.Err() == nil {
			_ = os.Remove(origPath)
			_ = os.Remove(origPath + ".part")
			m.removeTempTaskProgress(t, origRel)
		}
	}
	refresh := makeOnceURLRefresher(refreshPlan, func(fresh WorkDownloadPlan) ([]string, error) {
		if len(fresh.OriginalURLs) == 0 {
			return nil, &site.Error{Site: site.Douyin, Kind: site.ErrKindUnavailable, Message: "原画地址已不可用"}
		}
		return fresh.OriginalURLs, nil
	})
	if _, _, err := m.downloadWithTransportRefresh(ctx, plan.Transport, plan.OriginalURLs, origPath, "video", t, refresh); err != nil {
		discard()
		return MuxTrackSummary{}, err
	}
	m.setFileProgress(t, "video", videoRel, "verifying", t.BytesDownloaded, t.TotalBytes, 0)
	summary, err := EnsureSingleMP4Context(ctx, origPath, float64(plan.DurationSec))
	if err == nil && summaryPrimaryVideoCodec(summary) == "" {
		err = &site.Error{Site: site.Douyin, Kind: site.ErrKindSchemaChanged, Message: "原画文件没有视频轨"}
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = os.Rename(origPath, videoPath)
	}
	if err != nil {
		discard()
		return MuxTrackSummary{}, err
	}
	m.removeTempTaskProgress(t, origRel)
	return summary, nil
}

func (m *Manager) cacheDouyinSingleMP4(
	ctx context.Context,
	t *Task,
	itemDir string,
	previous library.ItemMetadata,
	exists bool,
	role string,
	relPath string,
	urls []string,
	needSetToken bool,
	expectedDuration int,
	fallbackW, fallbackH int,
	transport site.MediaTransportSpec,
	planner WorkPlanner,
	refreshURLs func() ([]string, error),
) (*library.MediaFile, error) {
	if exists && mediaIsValid(previous, relPath) {
		for _, mf := range previous.Media {
			if mf.Path == relPath {
				m.setFileProgress(t, role, relPath, "completed", mf.Bytes, mf.Bytes, 0)
				cp := mf
				return &cp, nil
			}
		}
	}
	fullPath, err := library.ResolveItemPath(itemDir, relPath)
	if err != nil {
		return nil, err
	}
	if err := m.claimTaskFile(t, itemDir, relPath, fullPath); err != nil {
		return nil, err
	}
	m.setFileProgress(t, role, relPath, "downloading", 0, 0, 0)
	rawRel := relPath + stageSuffix
	stagingPath := fullPath + stageSuffix
	m.adoptLegacyStaging(t, itemDir, relPath, ".raw", stageSuffix)
	if _, _, err := m.downloadDouyinMP4WithFallback(ctx, transport, urls, needSetToken, stagingPath, role, t, planner, refreshURLs); err != nil {
		if ctx.Err() == nil && !fileOrPartExists(stagingPath) {
			m.removeTempTaskProgress(t, rawRel)
		}
		m.setFileProgress(t, role, relPath, "failed", 0, 0, 0)
		return nil, err
	}
	m.setFileProgress(t, role, relPath, "verifying", 0, 0, 0)
	summary, err := EnsureSingleMP4Context(ctx, stagingPath, float64(expectedDuration))
	if err != nil {
		if ctx.Err() == nil {
			_ = os.Remove(stagingPath)
			m.removeTempTaskProgress(t, rawRel)
			m.setFileProgress(t, role, relPath, "failed", 0, 0, 0)
		}
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.Rename(stagingPath, fullPath); err != nil {
		return nil, err
	}
	m.removeTempTaskProgress(t, rawRel)
	dur := int(math.Round(summary.DurationSec))
	if dur <= 0 {
		dur = expectedDuration
	}
	w, h := summary.Width, summary.Height
	if w <= 0 {
		w = fallbackW
	}
	if h <= 0 {
		h = fallbackH
	}
	m.setFileProgress(t, role, relPath, "completed", summary.FileBytes, summary.FileBytes, 0)
	return &library.MediaFile{
		Role:     role,
		Path:     relPath,
		Bytes:    summary.FileBytes,
		SHA256:   summary.SHA256,
		Width:    w,
		Height:   h,
		Duration: dur,
	}, nil
}

func playURLResolverOf(planner WorkPlanner) PlayURLResolver {
	if planner == nil {
		return nil
	}
	if bsa, ok := planner.(*basicSitePlannerAdapter); ok {
		r, _ := bsa.inner.(PlayURLResolver)
		return r
	}
	r, _ := planner.(PlayURLResolver)
	return r
}

func splitDouyinPlayRedirectURLs(urls []string, allowLocalhost bool) (direct, play []string) {
	for _, raw := range urls {
		uStr := strings.TrimSpace(raw)
		if uStr == "" {
			continue
		}
		u, err := url.Parse(uStr)
		if err != nil || u.Host == "" {
			direct = append(direct, uStr)
			continue
		}
		host := strings.ToLower(u.Hostname())
		cleanPath := strings.TrimSuffix(strings.ToLower(u.Path), "/")
		isPlayHost := site.MatchDomainSuffix(host, []string{"douyin.com", "iesdouyin.com", "amemv.com"}) ||
			(allowLocalhost && site.IsLoopbackHost(host))
		if isPlayHost && cleanPath == "/aweme/v1/play" {
			play = append(play, uStr)
		} else {
			direct = append(direct, uStr)
		}
	}
	return direct, play
}

func (m *Manager) resolveAndDownloadPlayRedirect(
	ctx context.Context,
	transport site.MediaTransportSpec,
	playURLs []string,
	stagingPath string,
	role string,
	t *Task,
	resolver PlayURLResolver,
) (int64, string, error) {
	if resolver == nil || len(playURLs) == 0 {
		return 0, "", &site.Error{Site: site.Douyin, Kind: site.ErrKindUnavailable, Message: "无可用播放跳转地址"}
	}
	const maxDraws = 2
	var lastErr error
	for draw := 0; draw < maxDraws; draw++ {
		if ctx.Err() != nil {
			return 0, "", ctx.Err()
		}
		playURL := playURLs[draw%len(playURLs)]
		resolvedURL, rErr := resolver.ResolvePlayRedirect(ctx, playURL)
		if rErr != nil {
			if ctx.Err() != nil || isSiteStopError(rErr) || isStorageError(rErr) {
				return 0, "", rErr
			}
			lastErr = rErr
			if draw+1 >= len(playURLs) {
				break
			}
			continue
		}

		// The authenticated first hop on www.douyin.com already resolved into a
		// cookieless CDN URL; allow subsequent public HTTPS 443 edge redirects
		// (matching live FLV and music policies) while blocking IP/local/non-443.
		playTransport := transport
		playTransport.PublicRedirects = true
		if playTransport.ValidateURL(resolvedURL) != nil && playTransport.ValidateRedirectURL(resolvedURL) == nil {
			if parsed, pErr := url.Parse(resolvedURL); pErr == nil && parsed.Hostname() != "" {
				allowed := append([]string(nil), playTransport.AllowedDomains...)
				allowed = append(allowed, strings.ToLower(parsed.Hostname()))
				playTransport.AllowedDomains = allowed
			}
		}

		n, sha, _, dlErr := m.downloadRange(ctx, m.transportRangeSpec(playTransport, role, true), []string{resolvedURL}, stagingPath, t)
		if dlErr == nil {
			return n, sha, nil
		}
		if ctx.Err() != nil || isSiteStopError(dlErr) || isStorageError(dlErr) {
			return 0, "", dlErr
		}
		lastErr = dlErr
	}
	return 0, "", lastErr
}

// downloadDouyinMP4WithFallback downloads a Douyin single-file MP4 (video, note
// video page, or live-photo motion clip). When needSetToken is true (e.g. direct
// v26-web CDN links require a session token and answer HTTP 403), it resolves the
// authenticated www.douyin.com/aweme/v1/play/ redirector first into a cookieless
// CDN URL. For normal works it tries direct CDN URLs first and only uses the play
// redirector if direct CDN URLs still fail with 403/404 after refresh.
func (m *Manager) downloadDouyinMP4WithFallback(
	ctx context.Context,
	transport site.MediaTransportSpec,
	urls []string,
	needSetToken bool,
	stagingPath string,
	role string,
	t *Task,
	planner WorkPlanner,
	refreshURLs func() ([]string, error),
) (int64, string, error) {
	resolver := playURLResolverOf(planner)
	directURLs, playURLs := splitDouyinPlayRedirectURLs(urls, transport.AllowLocalhost)

	var (
		refreshed    bool
		freshDirect  []string
		freshPlay    []string
		refreshError error
	)
	refreshOnce := func() ([]string, []string, error) {
		if refreshed {
			return freshDirect, freshPlay, refreshError
		}
		refreshed = true
		if refreshURLs == nil {
			return nil, nil, nil
		}
		freshAll, rErr := refreshURLs()
		refreshError = rErr
		if rErr != nil {
			return nil, nil, rErr
		}
		freshDirect, freshPlay = splitDouyinPlayRedirectURLs(freshAll, transport.AllowLocalhost)
		return freshDirect, freshPlay, nil
	}
	refreshDirectOnly := func() ([]string, error) {
		d, _, rErr := refreshOnce()
		return d, rErr
	}

	if needSetToken && len(playURLs) > 0 && resolver != nil {
		n, sha, err := m.resolveAndDownloadPlayRedirect(ctx, transport, playURLs, stagingPath, role, t, resolver)
		if err == nil || ctx.Err() != nil || isSiteStopError(err) || isStorageError(err) {
			return n, sha, err
		}
		if refreshURLs != nil && (site.IsKind(err, site.ErrKindForbidden) || site.IsKind(err, site.ErrKindUnavailable)) {
			fDirect, fPlay, rErr := refreshOnce()
			if rErr != nil {
				if ctx.Err() != nil || isSiteStopError(rErr) || isStorageError(rErr) || site.IsKind(rErr, site.ErrKindUnavailable) {
					return 0, "", rErr
				}
			} else {
				if len(fPlay) > 0 {
					n2, sha2, err2 := m.resolveAndDownloadPlayRedirect(ctx, transport, fPlay, stagingPath, role, t, resolver)
					if err2 == nil || ctx.Err() != nil || isSiteStopError(err2) || isStorageError(err2) {
						return n2, sha2, err2
					}
					err = err2
				}
				if len(fDirect) > 0 {
					n3, sha3, _, err3 := m.downloadRange(ctx, m.transportRangeSpec(transport, role, true), fDirect, stagingPath, t)
					if err3 == nil || ctx.Err() != nil || isSiteStopError(err3) || isStorageError(err3) {
						return n3, sha3, err3
					}
					return 0, "", err3
				}
			}
		}
		if len(directURLs) > 0 && !refreshed {
			if n2, sha2, _, err2 := m.downloadRange(ctx, m.transportRangeSpec(transport, role, true), directURLs, stagingPath, t); err2 == nil || ctx.Err() != nil || isSiteStopError(err2) || isStorageError(err2) {
				return n2, sha2, err2
			}
		}
		return 0, "", err
	}

	if len(directURLs) > 0 {
		n, sha, err := m.downloadWithTransportRefresh(ctx, transport, directURLs, stagingPath, role, t, refreshDirectOnly)
		if err == nil || ctx.Err() != nil || isSiteStopError(err) || isStorageError(err) {
			return n, sha, err
		}
		if resolver != nil && (site.IsKind(err, site.ErrKindForbidden) || site.IsKind(err, site.ErrKindUnavailable)) {
			candPlay := playURLs
			if len(freshPlay) > 0 {
				candPlay = freshPlay
			}
			if len(candPlay) > 0 {
				n2, sha2, err2 := m.resolveAndDownloadPlayRedirect(ctx, transport, candPlay, stagingPath, role, t, resolver)
				if err2 == nil || ctx.Err() != nil || isSiteStopError(err2) || isStorageError(err2) {
					return n2, sha2, err2
				}
			}
		}
		return 0, "", err
	}

	if len(playURLs) > 0 && resolver != nil {
		n, sha, err := m.resolveAndDownloadPlayRedirect(ctx, transport, playURLs, stagingPath, role, t, resolver)
		if err == nil || ctx.Err() != nil || isSiteStopError(err) || isStorageError(err) {
			return n, sha, err
		}
		if refreshURLs != nil && (site.IsKind(err, site.ErrKindForbidden) || site.IsKind(err, site.ErrKindUnavailable)) {
			_, fPlay, rErr := refreshOnce()
			if rErr == nil && len(fPlay) > 0 {
				return m.resolveAndDownloadPlayRedirect(ctx, transport, fPlay, stagingPath, role, t, resolver)
			}
		}
		return 0, "", err
	}

	return m.downloadWithTransportRefresh(ctx, transport, urls, stagingPath, role, t, refreshURLs)
}
