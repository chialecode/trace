package download

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
)

// XHLSOnlyMissingReason is the manifest missing-item notice recorded when an X video
// only provides HLS (.m3u8) streams without a progressive video/mp4 variant (x.md §15.1 #15).
const XHLSOnlyMissingReason = "这条视频只有分段流，暂不支持缓存"

// XWorkEnqueueRequest specifies an X post (status_id) to enqueue for local caching.
type XWorkEnqueueRequest struct {
	TargetID string             `json:"targetId"`
	Source   string             `json:"source,omitempty"` // "manual" or "auto"
	Plan     *WorkDownloadPlan  `json:"-"`
	SitePlan *site.DownloadPlan `json:"-"`
	// Via optionally supplies forwarding context (DEC-056) when caching a retweet or
	// quote post under the forwarder's forwards/ folder.
	Via *library.ItemVia `json:"via,omitempty"`
}

// EnqueueXWork resolves (or accepts an in-memory plan for) an X post and enqueues
// a persistent download task.
func (m *Manager) EnqueueXWork(ctx context.Context, req XWorkEnqueueRequest) (Task, error) {
	rawTarget := strings.TrimSpace(req.TargetID)
	if rawTarget == "" && req.Plan != nil {
		rawTarget = strings.TrimSpace(req.Plan.TargetID)
	}
	if rawTarget == "" && req.SitePlan != nil {
		rawTarget = strings.TrimSpace(req.SitePlan.TargetID)
	}
	if rawTarget == "" {
		return Task{}, &site.Error{
			Site:    site.X,
			Kind:    site.ErrKindBadRequest,
			Message: "无效的 X 帖子 ID",
		}
	}

	planner, hasPlanner := m.getWorkPlanner(site.X)
	if checker, ok := planner.(SiteAuthChecker); ok {
		if err := checker.RequireAuth("download:x"); err != nil {
			return Task{}, err
		}
	}

	var plan WorkDownloadPlan
	switch {
	case req.Plan != nil:
		plan = *req.Plan
	case req.SitePlan != nil:
		plan = convertSiteDownloadPlan(*req.SitePlan)
	default:
		if !hasPlanner {
			return Task{}, &site.Error{
				Site:    site.X,
				Kind:    site.ErrKindUnavailable,
				Message: "X 下载规划器尚未初始化",
			}
		}
		m.mu.Lock()
		opts := WorkPlanOptions{
			VideoQuality: m.dlSettings.VideoQuality,
			CodecOrder:   append([]string(nil), m.dlSettings.CodecOrder...),
		}
		m.mu.Unlock()
		fetched, err := planner.PlanWorkDownload(ctx, rawTarget, opts)
		if err != nil {
			return Task{}, err
		}
		plan = fetched
	}

	plan.Site = site.X
	if strings.TrimSpace(plan.TargetID) == "" {
		plan.TargetID = rawTarget
	}

	itemKind, rawID, itemKey, plannerTarget, err := resolveXIdentity(plan.ItemKey, plan.TargetID)
	if err != nil {
		return Task{}, err
	}
	plan.Kind = itemKind
	plan.ItemKey = itemKey
	plan.TargetID = plannerTarget

	normUID, err := site.NormalizeUID(site.X, plan.AuthorUID)
	if err != nil {
		return Task{}, &site.Error{
			Site:    site.X,
			Kind:    site.ErrKindSchemaChanged,
			Message: "X 帖子缺少有效作者 ID",
		}
	}
	plan.AuthorUID = normUID
	plan.Transport = normalizeXMediaTransport(plan.Transport)

	forward, via, folderUID, err := resolveXPlacement(plan, req.Via, rawID)
	if err != nil {
		return Task{}, err
	}

	cacheRoot := m.lib.CacheDir()
	itemDir, err := library.XPostDir(cacheRoot, folderUID, rawID, forward)
	if err != nil {
		return Task{}, err
	}
	if existing, exists, gErr := m.lib.GetItem(itemKey); gErr == nil && exists && existing.ItemDir != "" {
		itemDir = existing.ItemDir
		if existing.Via != nil {
			cp := *existing.Via
			via = &cp
		} else {
			via = nil
		}
	}

	source := strings.TrimSpace(req.Source)
	if source == "" {
		source = "manual"
	}
	coverDisplay := ""
	if len(plan.CoverURLs) > 0 {
		coverDisplay = sanitizeXTaskURL(plan.CoverURLs[0])
	}
	title := firstNonEmptyStr(plan.Title, plan.Desc, "X 帖子 "+rawID)

	t, err := m.enqueueSingleTaskWithPlan(Task{
		Site:        string(site.X),
		Kind:        itemKind,
		Source:      source,
		TargetID:    plannerTarget,
		Page:        1,
		Title:       title,
		AuthorUID:   normUID,
		AuthorName:  plan.AuthorName,
		AuthorFace:  sanitizeXTaskURL(plan.AuthorFace),
		CoverURL:    coverDisplay,
		QualityHint: plan.DowngradeHint,
		ItemKey:     itemKey,
		ItemDir:     itemDir,
		Via:         via,
	}, &plan)
	if err != nil {
		return Task{}, err
	}

	m.notifyWake()
	return t, nil
}

func (m *Manager) consumeInitialOrFetchXPlan(ctx context.Context, taskID, targetID string) (WorkDownloadPlan, WorkPlanner, error) {
	m.mu.Lock()
	initial, hasInitial := m.initialPlans[taskID]
	if hasInitial {
		delete(m.initialPlans, taskID)
	}
	planner := m.planners[site.X]
	opts := WorkPlanOptions{
		VideoQuality: m.dlSettings.VideoQuality,
		CodecOrder:   append([]string(nil), m.dlSettings.CodecOrder...),
	}
	m.mu.Unlock()

	if hasInitial {
		initial.Site = site.X
		initial.Transport = normalizeXMediaTransport(initial.Transport)
		return initial, planner, nil
	}
	if planner == nil {
		return WorkDownloadPlan{}, nil, &site.Error{
			Site:    site.X,
			Kind:    site.ErrKindUnavailable,
			Message: "X 下载规划器尚未初始化",
		}
	}
	if checker, ok := planner.(SiteAuthChecker); ok {
		if err := checker.RequireAuth("download:x"); err != nil {
			return WorkDownloadPlan{}, planner, err
		}
	}
	plan, err := planner.PlanWorkDownload(ctx, targetID, opts)
	if err != nil {
		return WorkDownloadPlan{}, planner, err
	}
	plan.Site = site.X
	plan.Transport = normalizeXMediaTransport(plan.Transport)
	return plan, planner, nil
}

func (m *Manager) executeXTask(ctx context.Context, t *Task) error {
	unlock, err := m.lockItem(ctx, "x:"+strings.TrimSpace(t.TargetID))
	if err != nil {
		return err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	plan, planner, err := m.consumeInitialOrFetchXPlan(ctx, t.TaskID, t.TargetID)
	if err != nil {
		return err
	}
	normUID, err := site.NormalizeUID(site.X, firstNonEmptyStr(plan.AuthorUID, t.AuthorUID))
	if err != nil {
		return &site.Error{
			Site:    site.X,
			Kind:    site.ErrKindSchemaChanged,
			Message: "X 帖子缺少有效作者 ID",
		}
	}
	plan.AuthorUID = normUID
	t.AuthorUID = normUID
	plan.Transport = normalizeXMediaTransport(plan.Transport)

	itemKind, rawID, itemKey, plannerTarget, err := resolveXIdentity(firstNonEmptyStr(plan.ItemKey, t.ItemKey), firstNonEmptyStr(plan.TargetID, t.TargetID))
	if err != nil {
		return err
	}
	t.Kind = itemKind
	t.ItemKey = itemKey
	t.TargetID = plannerTarget
	plan.Kind = itemKind
	plan.ItemKey = itemKey
	plan.TargetID = plannerTarget

	forward, via, folderUID, err := resolveXPlacement(plan, t.Via, rawID)
	if err != nil {
		return err
	}
	previous, exists, itemDir, resolvedForward, resolvedVia, err := m.resolveXItemDir(t, itemKey, folderUID, rawID, forward, via)
	if err != nil {
		return err
	}

	if len(plan.Pages) == 0 && (len(plan.VideoURLs) > 0 || isXPlanHLSOnly(plan)) {
		return m.executeXSingleVideoTask(ctx, t, plan, planner, rawID, previous, exists, itemDir, resolvedForward, resolvedVia)
	}
	return m.executeXPostTask(ctx, t, plan, planner, rawID, previous, exists, itemDir, resolvedForward, resolvedVia)
}

func (m *Manager) resolveXItemDir(
	t *Task,
	itemKey, folderUID, rawID string,
	forward bool,
	via *library.ItemVia,
) (library.ItemMetadata, bool, string, bool, *library.ItemVia, error) {
	previous, exists, err := m.lib.GetItem(itemKey)
	if err != nil {
		return library.ItemMetadata{}, false, "", false, nil, err
	}
	cacheRoot := m.lib.CacheDir()
	itemDir, err := library.XPostDir(cacheRoot, folderUID, rawID, forward)
	if err != nil {
		return library.ItemMetadata{}, false, "", false, nil, err
	}
	resolvedForward := forward
	resolvedVia := via
	if exists && previous.ItemDir != "" {
		// Already-cached items stay in their existing folder and preserve original
		// forward classification and via attribution (never moved).
		itemDir = previous.ItemDir
		if previous.Via != nil {
			cp := *previous.Via
			resolvedVia = &cp
		} else {
			resolvedVia = nil
		}
		resolvedForward = (resolvedVia != nil && resolvedVia.Reason == library.ViaForward) ||
			strings.EqualFold(strings.TrimSpace(previous.DynamicType), "forward")
	} else if t.ItemDir != "" {
		if checked, cErr := library.EnsureWithinRoot(cacheRoot, t.ItemDir); cErr == nil {
			itemDir = checked
		}
	}
	if err := os.MkdirAll(itemDir, 0o700); err != nil {
		return library.ItemMetadata{}, false, "", false, nil, err
	}
	t.ItemKey = itemKey
	t.ItemDir = itemDir
	return previous, exists, itemDir, resolvedForward, resolvedVia, nil
}

func (m *Manager) executeXSingleVideoTask(
	ctx context.Context,
	t *Task,
	plan WorkDownloadPlan,
	planner WorkPlanner,
	rawID string,
	previous library.ItemMetadata,
	exists bool,
	itemDir string,
	forward bool,
	via *library.ItemVia,
) (result error) {
	m.mu.Lock()
	cfg := m.dlSettings
	m.mu.Unlock()

	fullText, threadParts := buildXTextAndThread(rawID, plan, previous)
	title := firstNonEmptyStr(plan.Title, plan.Desc, fullText, t.Title, "X 视频 "+rawID)
	canonicalURL := firstNonEmptyStr(sanitizeXCanonicalPageURL(plan.CanonicalURL), xFallbackCanonicalURL(rawID))
	t.Title = title
	t.AuthorName = firstNonEmptyStr(plan.AuthorName, t.AuthorName)
	t.AuthorFace = sanitizeXTaskURL(firstNonEmptyStr(plan.AuthorFace, t.AuthorFace))
	if len(plan.CoverURLs) > 0 {
		t.CoverURL = sanitizeXTaskURL(plan.CoverURLs[0])
	}
	if plan.DowngradeHint != "" {
		t.QualityHint = plan.DowngradeHint
	}

	dynType := "video"
	typeLabel := "视频"
	if forward {
		dynType = "forward"
		typeLabel = "转发"
	}

	originMeta := buildXItemOrigin(plan, previous)
	cardMeta := buildWeiboItemRichCard(plan.Card, previous.Card)

	meta := library.ItemMetadata{
		Version:     1,
		ID:          "dynamic:" + rawID,
		Site:        string(site.X),
		Kind:        "dynamic",
		Status:      "partial",
		Title:       title,
		Desc:        plan.Desc,
		Text:        fullText,
		DynamicType: dynType,
		TypeLabel:   typeLabel,
		URL:         canonicalURL,
		PubTs:       plan.PubTs,
		CachedAt:    previous.CachedAt,
		Duration:    plan.DurationSec,
		Author: library.ItemAuthor{
			Site:     string(site.X),
			UID:      plan.AuthorUID,
			UniqueID: strings.TrimSpace(plan.AuthorUniqueID),
			Name:     t.AuthorName,
			Face:     t.AuthorFace,
		},
		Media:        append([]library.MediaFile(nil), previous.Media...),
		Missing:      append([]library.MissingRecord(nil), previous.Missing...),
		DirectCached: true,
		Via:          via,
		Origin:       originMeta,
		Card:         cardMeta,
		Thread:       threadParts,
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

	refreshOncePlan := m.makeSinglePlanRefresherForSite(ctx, site.X, t.TargetID, planner)

	// 1. Save cover.<ext> when enabled (no Cookie, no Referer).
	if cfg.SaveCover && len(plan.CoverURLs) > 0 {
		coverCandidates := buildXImageCandidateURLs(plan.CoverURLs)
		refreshCover := makeOnceURLRefresher(refreshOncePlan, func(fresh WorkDownloadPlan) ([]string, error) {
			return buildXImageCandidateURLs(fresh.CoverURLs), nil
		})
		if coverMedia, cErr := m.cacheDouyinSniffedImage(ctx, t, itemDir, previous, exists, "cover", "cover", coverCandidates, 0, 0, plan.Transport, refreshCover); cErr != nil {
			if isSiteStopError(cErr) || isStorageError(cErr) || ctx.Err() != nil {
				return cErr
			}
			addMissing(&meta, "cover", "封面保存失败 ("+classifyDownloadError(cErr)+")")
		} else if coverMedia != nil {
			coverMedia.FromOrigin = plan.VideoFromOrigin
			meta.Media = replaceRoleMedia(meta.Media, "cover", *coverMedia)
			clearMissing(&meta, "cover")
		}
	}

	// 2. Check HLS-only videos before issuing any CDN request (x.md §15.1 #15).
	progressiveURLs, sawHLS := filterProgressiveXVideoURLs(plan.VideoURLs)
	if len(progressiveURLs) == 0 && (plan.HLSOnly || sawHLS || isXPlanHLSOnly(plan)) {
		addMissing(&meta, "video.mp4", XHLSOnlyMissingReason)
		meta.Status = "partial"
		t.Status = "partial"
		t.ErrorMessage = summarizeMissingReasons(meta.Missing)
		saved, err := m.lib.SaveItem(itemDir, meta)
		if err != nil {
			return err
		}
		t.ItemKey = saved.ItemKey
		finalSaved = true
		return nil
	}

	// 3. Download & validate single-file video.mp4 (streaming tail-moov reorder, duration/track verification).
	videoRel := "video.mp4"
	videoPath, err := library.ResolveItemPath(itemDir, videoRel)
	if err != nil {
		return err
	}
	if exists && mediaIsValid(previous, videoRel) {
		for _, mf := range previous.Media {
			if mf.Path == videoRel {
				if plan.VideoFromOrigin {
					mf.FromOrigin = true
				}
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
				return nil, &site.Error{Site: site.X, Kind: site.ErrKindUnavailable, Message: "续传地址的视频编码发生变化，已保留断点"}
			}
			if plan.QualityName != "" && fresh.QualityName != "" && !strings.EqualFold(plan.QualityName, fresh.QualityName) {
				return nil, &site.Error{Site: site.X, Kind: site.ErrKindUnavailable, Message: "续传地址的清晰度档位发生变化，已保留断点"}
			}
			mp4s, _ := filterProgressiveXVideoURLs(fresh.VideoURLs)
			return mp4s, nil
		})
		rawRel := videoRel + stageSuffix
		stagingPath := videoPath + stageSuffix
		_, _, dlErr := m.downloadWithTransportRefresh(ctx, plan.Transport, progressiveURLs, stagingPath, "video", t, refreshVideoURLs)
		if dlErr != nil {
			if ctx.Err() == nil && !fileOrPartExists(stagingPath) {
				m.removeTempTaskProgress(t, rawRel)
			}
			m.setFileProgress(t, "video", videoRel, "failed", t.BytesDownloaded, t.TotalBytes, 0)
			return dlErr
		}

		m.setFileProgress(t, "video", videoRel, "verifying", t.BytesDownloaded, t.TotalBytes, 0)
		summary, vErr := EnsureSingleMP4Context(ctx, stagingPath, float64(plan.DurationSec))
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
			Role:       "video",
			Path:       videoRel,
			FromOrigin: plan.VideoFromOrigin,
			Bytes:      summary.FileBytes,
			SHA256:     summary.SHA256,
			Width:      width,
			Height:     height,
			Duration:   durSec,
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

func (m *Manager) executeXPostTask(
	ctx context.Context,
	t *Task,
	plan WorkDownloadPlan,
	planner WorkPlanner,
	rawID string,
	previous library.ItemMetadata,
	exists bool,
	itemDir string,
	forward bool,
	via *library.ItemVia,
) (result error) {
	m.mu.Lock()
	cfg := m.dlSettings
	m.mu.Unlock()

	fullText, threadParts := buildXTextAndThread(rawID, plan, previous)
	title := firstNonEmptyStr(plan.Title, plan.Desc, fullText, t.Title, "X 帖子 "+rawID)
	canonicalURL := firstNonEmptyStr(sanitizeXCanonicalPageURL(plan.CanonicalURL), xFallbackCanonicalURL(rawID))
	t.Title = title
	t.AuthorName = firstNonEmptyStr(plan.AuthorName, t.AuthorName)
	t.AuthorFace = sanitizeXTaskURL(firstNonEmptyStr(plan.AuthorFace, t.AuthorFace))
	if len(plan.CoverURLs) > 0 {
		t.CoverURL = sanitizeXTaskURL(plan.CoverURLs[0])
	}

	dynType := "word"
	typeLabel := "文字"
	if len(plan.Pages) > 0 {
		dynType = "draw"
		typeLabel = "图文"
	}
	if forward {
		dynType = "forward"
		typeLabel = "转发"
	}

	originMeta := buildXItemOrigin(plan, previous)
	cardMeta := buildWeiboItemRichCard(plan.Card, previous.Card)

	var mediaFiles []library.MediaFile
	var notePages []library.NotePageRecord
	var missing []library.MissingRecord
	var partialErrors []string
	finalSaved := false

	defer func() {
		if result == nil || finalSaved {
			return
		}
		if !isSiteStopError(result) {
			missing = append(missing, library.MissingRecord{
				Item:   "post",
				Reason: "X 帖子缓存尚未完成 (" + classifyDownloadError(result) + ")",
			})
		}
		partialMeta := library.ItemMetadata{
			Version:     1,
			ID:          "dynamic:" + rawID,
			Site:        string(site.X),
			Kind:        "dynamic",
			Status:      "partial",
			Title:       title,
			Desc:        plan.Desc,
			Text:        fullText,
			DynamicType: dynType,
			TypeLabel:   typeLabel,
			URL:         canonicalURL,
			PubTs:       plan.PubTs,
			CachedAt:    previous.CachedAt,
			Author: library.ItemAuthor{
				Site:     string(site.X),
				UID:      plan.AuthorUID,
				UniqueID: strings.TrimSpace(plan.AuthorUniqueID),
				Name:     t.AuthorName,
				Face:     t.AuthorFace,
			},
			Media:        mergeMediaFiles(previous.Media, mediaFiles),
			NotePages:    notePages,
			Missing:      missing,
			DirectCached: true,
			Via:          via,
			Origin:       originMeta,
			Card:         cardMeta,
			Thread:       threadParts,
		}
		saved, sErr := m.lib.SaveItem(itemDir, partialMeta)
		if sErr != nil {
			if !isSiteStopError(result) && ctx.Err() == nil {
				result = sErr
			}
			return
		}
		t.ItemKey = saved.ItemKey
	}()

	refreshOncePlan := m.makeSinglePlanRefresherForSite(ctx, site.X, t.TargetID, planner)

	if cfg.SaveCover && len(plan.CoverURLs) > 0 && len(plan.Pages) == 0 {
		coverCandidates := buildXImageCandidateURLs(plan.CoverURLs)
		refreshCover := makeOnceURLRefresher(refreshOncePlan, func(fresh WorkDownloadPlan) ([]string, error) {
			return buildXImageCandidateURLs(fresh.CoverURLs), nil
		})
		if coverMedia, cErr := m.cacheDouyinSniffedImage(ctx, t, itemDir, previous, exists, "cover", "cover", coverCandidates, 0, 0, plan.Transport, refreshCover); cErr != nil {
			if isSiteStopError(cErr) || isStorageError(cErr) || ctx.Err() != nil {
				return cErr
			}
			missing = append(missing, library.MissingRecord{
				Item:   "cover",
				Reason: "封面保存失败 (" + classifyDownloadError(cErr) + ")",
			})
		} else if coverMedia != nil {
			if plan.Card == nil && plan.OriginCard != nil {
				coverMedia.FromOrigin = true
			}
			mediaFiles = append(mediaFiles, *coverMedia)
		}
	}

	for i, page := range plan.Pages {
		pageIdx := page.Index
		if pageIdx <= 0 {
			pageIdx = i + 1
		}
		prefix := fmt.Sprintf("images/%02d", pageIdx)
		pageKind := strings.ToLower(strings.TrimSpace(page.Kind))
		if pageKind == "animated_gif" {
			pageKind = "gif"
		}
		if pageKind == "" {
			if len(page.VideoURLs) > 0 || page.HLSOnly {
				pageKind = "video"
			} else {
				pageKind = "image"
			}
		}

		switch pageKind {
		case "image":
			imgCandidates := buildXImageCandidateURLs(page.ImageURLs)
			refreshImg := makeOnceURLRefresher(refreshOncePlan, func(fresh WorkDownloadPlan) ([]string, error) {
				if p, ok := findPlannedPage(fresh.Pages, pageIdx, i); ok {
					return buildXImageCandidateURLs(p.ImageURLs), nil
				}
				return nil, nil
			})
			imgMedia, imgErr := m.cacheDouyinSniffedImage(ctx, t, itemDir, previous, exists, "image", prefix, imgCandidates, page.Width, page.Height, plan.Transport, refreshImg)
			if imgErr != nil {
				if isSiteStopError(imgErr) || isStorageError(imgErr) || ctx.Err() != nil {
					return imgErr
				}
				reason := fmt.Sprintf("第 %02d 张图片下载失败 (%s)", pageIdx, classifyDownloadError(imgErr))
				missing = append(missing, library.MissingRecord{Item: fmt.Sprintf("page_%02d", pageIdx), Reason: reason})
				partialErrors = append(partialErrors, reason)
				notePages = append(notePages, library.NotePageRecord{Index: pageIdx, Kind: "image", FromOrigin: page.FromOrigin, Width: page.Width, Height: page.Height})
				continue
			}
			imgMedia.FromOrigin = page.FromOrigin
			mediaFiles = append(mediaFiles, *imgMedia)
			notePages = append(notePages, library.NotePageRecord{
				Index:      pageIdx,
				Kind:       "image",
				FromOrigin: page.FromOrigin,
				Image:      imgMedia.Path,
				Width:      page.Width,
				Height:     page.Height,
			})

		case "gif":
			// X serves animated GIFs as muted looping progressive MP4s (images/NN.mp4, role "video", gif: true) plus poster image.
			var coverRel string
			if len(page.ImageURLs) > 0 {
				coverCandidates := buildXImageCandidateURLs(page.ImageURLs)
				refreshPageCover := makeOnceURLRefresher(refreshOncePlan, func(fresh WorkDownloadPlan) ([]string, error) {
					if p, ok := findPlannedPage(fresh.Pages, pageIdx, i); ok {
						return buildXImageCandidateURLs(p.ImageURLs), nil
					}
					return nil, nil
				})
				coverMedia, cErr := m.cacheDouyinSniffedImage(ctx, t, itemDir, previous, exists, "cover", prefix, coverCandidates, page.Width, page.Height, plan.Transport, refreshPageCover)
				if cErr != nil {
					if isSiteStopError(cErr) || isStorageError(cErr) || ctx.Err() != nil {
						return cErr
					}
					reason := fmt.Sprintf("第 %02d 项动图封面下载失败 (%s)", pageIdx, classifyDownloadError(cErr))
					missing = append(missing, library.MissingRecord{Item: fmt.Sprintf("page_%02d.cover", pageIdx), Reason: reason})
					partialErrors = append(partialErrors, reason)
				} else if coverMedia != nil {
					coverMedia.FromOrigin = page.FromOrigin
					coverRel = coverMedia.Path
					mediaFiles = append(mediaFiles, *coverMedia)
				}
			}

			videoRel := fmt.Sprintf("images/%02d.mp4", pageIdx)
			gifURLs, _ := filterProgressiveXVideoURLs(page.VideoURLs)
			refreshGIFVideo := makeOnceURLRefresher(refreshOncePlan, func(fresh WorkDownloadPlan) ([]string, error) {
				if p, ok := findPlannedPage(fresh.Pages, pageIdx, i); ok {
					mp4s, _ := filterProgressiveXVideoURLs(p.VideoURLs)
					return mp4s, nil
				}
				return nil, nil
			})
			vidMedia, vErr := m.cacheXSingleMP4(ctx, t, itemDir, previous, exists, videoRel, gifURLs, page.DurationSec, page.Width, page.Height, true, plan.Transport, refreshGIFVideo)
			rec := library.NotePageRecord{
				Index:      pageIdx,
				Kind:       "video",
				GIF:        true,
				FromOrigin: page.FromOrigin,
				Cover:      coverRel,
				Width:      page.Width,
				Height:     page.Height,
				Duration:   page.DurationSec,
			}
			if vErr != nil {
				if isSiteStopError(vErr) || isStorageError(vErr) || ctx.Err() != nil {
					return vErr
				}
				reason := fmt.Sprintf("第 %02d 项动图视频下载失败 (%s)", pageIdx, classifyDownloadError(vErr))
				missing = append(missing, library.MissingRecord{Item: videoRel, Reason: reason})
				partialErrors = append(partialErrors, reason)
			} else if vidMedia != nil {
				vidMedia.GIF = true
				vidMedia.FromOrigin = page.FromOrigin
				rec.Video = vidMedia.Path
				if vidMedia.Duration > 0 {
					rec.Duration = vidMedia.Duration
				}
				if vidMedia.Width > 0 {
					rec.Width = vidMedia.Width
				}
				if vidMedia.Height > 0 {
					rec.Height = vidMedia.Height
				}
				rec.VideoInfo = &library.DouyinVideoInfo{
					File:        vidMedia.Path,
					Gear:        page.Gear,
					QualityName: page.QualityName,
					Codec:       page.Codec,
					Width:       rec.Width,
					Height:      rec.Height,
					FPS:         page.FPS,
					Bitrate:     page.Bitrate,
				}
				mediaFiles = append(mediaFiles, *vidMedia)
			}
			notePages = append(notePages, rec)

		case "video":
			var coverRel string
			if len(page.ImageURLs) > 0 {
				coverCandidates := buildXImageCandidateURLs(page.ImageURLs)
				refreshPageCover := makeOnceURLRefresher(refreshOncePlan, func(fresh WorkDownloadPlan) ([]string, error) {
					if p, ok := findPlannedPage(fresh.Pages, pageIdx, i); ok {
						return buildXImageCandidateURLs(p.ImageURLs), nil
					}
					return nil, nil
				})
				coverMedia, cErr := m.cacheDouyinSniffedImage(ctx, t, itemDir, previous, exists, "cover", prefix, coverCandidates, page.Width, page.Height, plan.Transport, refreshPageCover)
				if cErr != nil {
					if isSiteStopError(cErr) || isStorageError(cErr) || ctx.Err() != nil {
						return cErr
					}
					reason := fmt.Sprintf("第 %02d 项视频封面下载失败 (%s)", pageIdx, classifyDownloadError(cErr))
					missing = append(missing, library.MissingRecord{Item: fmt.Sprintf("page_%02d.cover", pageIdx), Reason: reason})
					partialErrors = append(partialErrors, reason)
				} else if coverMedia != nil {
					coverMedia.FromOrigin = page.FromOrigin
					coverRel = coverMedia.Path
					mediaFiles = append(mediaFiles, *coverMedia)
				}
			}

			videoRel := fmt.Sprintf("images/%02d.mp4", pageIdx)
			rec := library.NotePageRecord{
				Index:      pageIdx,
				Kind:       "video",
				FromOrigin: page.FromOrigin,
				Cover:      coverRel,
				Width:      page.Width,
				Height:     page.Height,
				Duration:   page.DurationSec,
			}
			progressiveURLs, sawHLS := filterProgressiveXVideoURLs(page.VideoURLs)
			if len(progressiveURLs) == 0 && (page.HLSOnly || sawHLS || isXPageHLSOnly(page)) {
				missing = append(missing, library.MissingRecord{Item: videoRel, Reason: XHLSOnlyMissingReason})
				partialErrors = append(partialErrors, XHLSOnlyMissingReason)
				notePages = append(notePages, rec)
				continue
			}

			refreshPageVideo := makeOnceURLRefresher(refreshOncePlan, func(fresh WorkDownloadPlan) ([]string, error) {
				if p, ok := findPlannedPage(fresh.Pages, pageIdx, i); ok {
					mp4s, _ := filterProgressiveXVideoURLs(p.VideoURLs)
					return mp4s, nil
				}
				return nil, nil
			})
			vidMedia, vErr := m.cacheXSingleMP4(ctx, t, itemDir, previous, exists, videoRel, progressiveURLs, page.DurationSec, page.Width, page.Height, false, plan.Transport, refreshPageVideo)
			if vErr != nil {
				if isSiteStopError(vErr) || isStorageError(vErr) || ctx.Err() != nil {
					return vErr
				}
				reason := fmt.Sprintf("第 %02d 项视频下载失败 (%s)", pageIdx, classifyDownloadError(vErr))
				missing = append(missing, library.MissingRecord{Item: videoRel, Reason: reason})
				partialErrors = append(partialErrors, reason)
			} else if vidMedia != nil {
				vidMedia.FromOrigin = page.FromOrigin
				rec.Video = vidMedia.Path
				if vidMedia.Duration > 0 {
					rec.Duration = vidMedia.Duration
				}
				if vidMedia.Width > 0 {
					rec.Width = vidMedia.Width
				}
				if vidMedia.Height > 0 {
					rec.Height = vidMedia.Height
				}
				rec.VideoInfo = &library.DouyinVideoInfo{
					File:        vidMedia.Path,
					Gear:        page.Gear,
					QualityName: page.QualityName,
					Codec:       page.Codec,
					Width:       rec.Width,
					Height:      rec.Height,
					FPS:         page.FPS,
					Bitrate:     page.Bitrate,
				}
				mediaFiles = append(mediaFiles, *vidMedia)
			}
			notePages = append(notePages, rec)

		default:
			var savedImg string
			if len(page.ImageURLs) > 0 {
				imgCandidates := buildXImageCandidateURLs(page.ImageURLs)
				refreshUnknownImg := makeOnceURLRefresher(refreshOncePlan, func(fresh WorkDownloadPlan) ([]string, error) {
					if p, ok := findPlannedPage(fresh.Pages, pageIdx, i); ok {
						return buildXImageCandidateURLs(p.ImageURLs), nil
					}
					return nil, nil
				})
				if imgMedia, imgErr := m.cacheDouyinSniffedImage(ctx, t, itemDir, previous, exists, "image", prefix, imgCandidates, page.Width, page.Height, plan.Transport, refreshUnknownImg); imgErr != nil {
					if isSiteStopError(imgErr) || isStorageError(imgErr) || ctx.Err() != nil {
						return imgErr
					}
				} else if imgMedia != nil {
					imgMedia.FromOrigin = page.FromOrigin
					savedImg = imgMedia.Path
					mediaFiles = append(mediaFiles, *imgMedia)
				}
			}
			unkTag := firstNonEmptyStr(page.UnknownKind, pageKind, "unknown")
			reason := firstNonEmptyStr(page.UnknownReason, fmt.Sprintf("第 %02d 项包含暂不支持的媒体类型 (%s)", pageIdx, unkTag))
			missing = append(missing, library.MissingRecord{Item: fmt.Sprintf("page_%02d", pageIdx), Reason: reason})
			notePages = append(notePages, library.NotePageRecord{
				Index:       pageIdx,
				Kind:        "unknown",
				FromOrigin:  page.FromOrigin,
				UnknownKind: unkTag,
				Image:       savedImg,
				Width:       page.Width,
				Height:      page.Height,
			})
		}
	}

	status := "complete"
	if len(missing) > 0 || len(partialErrors) > 0 {
		status = "partial"
		t.Status = "partial"
		t.ErrorMessage = summarizeMissingReasons(missing)
	}

	mediaFiles, notePages = preserveValidPreviousImages(itemDir, previous, exists, mediaFiles, notePages)

	meta := library.ItemMetadata{
		Version:     1,
		ID:          "dynamic:" + rawID,
		Site:        string(site.X),
		Kind:        "dynamic",
		Status:      status,
		Title:       title,
		Desc:        plan.Desc,
		Text:        fullText,
		DynamicType: dynType,
		TypeLabel:   typeLabel,
		URL:         canonicalURL,
		PubTs:       plan.PubTs,
		CachedAt:    previous.CachedAt,
		Author: library.ItemAuthor{
			Site:     string(site.X),
			UID:      plan.AuthorUID,
			UniqueID: strings.TrimSpace(plan.AuthorUniqueID),
			Name:     t.AuthorName,
			Face:     t.AuthorFace,
		},
		Media:        mediaFiles,
		NotePages:    notePages,
		Missing:      missing,
		DirectCached: true,
		Via:          via,
		Origin:       originMeta,
		Card:         cardMeta,
		Thread:       threadParts,
	}

	saved, saveErr := m.lib.SaveItem(itemDir, meta)
	if saveErr != nil {
		return saveErr
	}
	t.ItemKey = saved.ItemKey
	finalSaved = true
	return nil
}

func (m *Manager) cacheXSingleMP4(
	ctx context.Context,
	t *Task,
	itemDir string,
	previous library.ItemMetadata,
	exists bool,
	relPath string,
	urls []string,
	expectedDuration int,
	fallbackW, fallbackH int,
	isGIF bool,
	transport site.MediaTransportSpec,
	refreshURLs func() ([]string, error),
) (*library.MediaFile, error) {
	if exists && mediaIsValid(previous, relPath) {
		for _, mf := range previous.Media {
			if mf.Path == relPath {
				if isGIF {
					mf.GIF = true
				}
				m.setFileProgress(t, "video", relPath, "completed", mf.Bytes, mf.Bytes, 0)
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
	m.setFileProgress(t, "video", relPath, "downloading", 0, 0, 0)
	rawRel := relPath + stageSuffix
	stagingPath := fullPath + stageSuffix
	if _, _, err := m.downloadWithTransportRefresh(ctx, transport, urls, stagingPath, "video", t, refreshURLs); err != nil {
		if ctx.Err() == nil && !fileOrPartExists(stagingPath) {
			m.removeTempTaskProgress(t, rawRel)
		}
		m.setFileProgress(t, "video", relPath, "failed", 0, 0, 0)
		return nil, err
	}
	m.setFileProgress(t, "video", relPath, "verifying", 0, 0, 0)
	summary, vErr := EnsureSingleMP4Context(ctx, stagingPath, float64(expectedDuration))
	if vErr != nil {
		if ctx.Err() == nil {
			_ = os.Remove(stagingPath)
			m.removeTempTaskProgress(t, rawRel)
			m.setFileProgress(t, "video", relPath, "failed", 0, 0, 0)
		}
		return nil, vErr
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
	m.setFileProgress(t, "video", relPath, "completed", summary.FileBytes, summary.FileBytes, 0)
	return &library.MediaFile{
		Role:     "video",
		Path:     relPath,
		GIF:      isGIF,
		Bytes:    summary.FileBytes,
		SHA256:   summary.SHA256,
		Width:    w,
		Height:   h,
		Duration: dur,
	}, nil
}

func resolveXIdentity(planItemKey, targetID string) (itemKind, rawID, itemKey, plannerTarget string, err error) {
	planItemKey = strings.TrimSpace(planItemKey)
	targetID = strings.TrimSpace(targetID)

	cand := library.NormalizeRawID("dynamic", targetID)
	lowerKey := strings.ToLower(planItemKey)
	if strings.HasPrefix(lowerKey, "x:dynamic:") {
		if keyID := strings.TrimSpace(planItemKey[len("x:dynamic:"):]); keyID != "" {
			cand = keyID
		}
	}
	normID, normErr := site.NormalizeUID(site.X, cand)
	if normErr != nil {
		return "", "", "", "", &site.Error{
			Site:    site.X,
			Kind:    site.ErrKindBadRequest,
			Message: "无效的 X 帖子 ID",
		}
	}
	return "dynamic", normID, library.CanonicalItemKey(string(site.X), "dynamic", normID), normID, nil
}

func hasXOriginSignal(plan WorkDownloadPlan) bool {
	return strings.TrimSpace(plan.OriginID) != "" ||
		strings.TrimSpace(plan.OriginAuthorUID) != "" ||
		strings.TrimSpace(plan.OriginAuthorName) != "" ||
		strings.TrimSpace(plan.OriginText) != "" ||
		plan.OriginUnavailable ||
		plan.OriginCard != nil ||
		plan.VideoFromOrigin ||
		plan.OriginPubTs > 0
}

func resolveXPlacement(plan WorkDownloadPlan, reqVia *library.ItemVia, rawID string) (forward bool, via *library.ItemVia, folderUID string, err error) {
	forward = plan.IsForward || plan.IsFastRetweet || (reqVia != nil && reqVia.Reason == library.ViaForward) || hasXOriginSignal(plan)
	if !forward {
		return false, nil, plan.AuthorUID, nil
	}
	rawFwdUID := plan.AuthorUID
	fwdName := strings.TrimSpace(plan.AuthorName)
	dynID := rawID
	if reqVia != nil {
		if strings.TrimSpace(reqVia.UID) != "" {
			rawFwdUID = reqVia.UID
		}
		if strings.TrimSpace(reqVia.Name) != "" {
			fwdName = strings.TrimSpace(reqVia.Name)
		}
		if strings.TrimSpace(reqVia.DynamicID) != "" {
			dynID = strings.TrimSpace(reqVia.DynamicID)
		}
	}
	fwdUID, uErr := site.NormalizeUID(site.X, rawFwdUID)
	if uErr != nil {
		return false, nil, "", &site.Error{
			Site:    site.X,
			Kind:    site.ErrKindSchemaChanged,
			Message: "转发或引用帖缺少有效的转发者 ID",
		}
	}
	return true, &library.ItemVia{
		Reason:    library.ViaForward,
		Site:      string(site.X),
		UID:       fwdUID,
		Name:      fwdName,
		DynamicID: dynID,
	}, fwdUID, nil
}

func buildXTextAndThread(rawID string, plan WorkDownloadPlan, previous library.ItemMetadata) (string, []library.ItemThreadPart) {
	var thread []library.ItemThreadPart
	if len(plan.Thread) > 0 {
		thread = make([]library.ItemThreadPart, 0, len(plan.Thread))
		for _, seg := range plan.Thread {
			id := strings.TrimSpace(seg.ID)
			txt := strings.TrimSpace(seg.Text)
			if id == "" && txt == "" && len(seg.MediaIndexes) == 0 {
				continue
			}
			thread = append(thread, library.ItemThreadPart{
				ID:           id,
				PubTs:        seg.PubTs,
				Text:         txt,
				MediaIndexes: append([]int(nil), seg.MediaIndexes...),
			})
		}
	} else if len(previous.Thread) > 0 {
		thread = make([]library.ItemThreadPart, len(previous.Thread))
		for i, seg := range previous.Thread {
			seg.MediaIndexes = append([]int(nil), seg.MediaIndexes...)
			thread[i] = seg
		}
	}

	fullText := strings.TrimSpace(firstNonEmptyStr(plan.Desc, plan.Title))
	for _, seg := range thread {
		if seg.Text == "" {
			continue
		}
		if seg.ID == rawID && fullText != "" {
			continue
		}
		if fullText == "" {
			fullText = seg.Text
		} else if !strings.Contains(fullText, seg.Text) {
			fullText += "\n\n" + seg.Text
		}
	}
	return fullText, thread
}

func buildXItemOrigin(plan WorkDownloadPlan, previous library.ItemMetadata) *library.ItemOrigin {
	if !hasXOriginSignal(plan) {
		if previous.Origin != nil {
			cp := *previous.Origin
			if cp.Card != nil {
				cc := *cp.Card
				cp.Card = &cc
			}
			return &cp
		}
		return nil
	}
	originUID := strings.TrimSpace(plan.OriginAuthorUID)
	if norm, err := site.NormalizeUID(site.X, originUID); err == nil {
		originUID = norm
	}
	originID := strings.TrimSpace(plan.OriginID)
	originCanonical := sanitizeXCanonicalPageURL(plan.OriginCanonicalURL)
	if originCanonical == "" && originID != "" {
		originCanonical = xFallbackCanonicalURL(originID)
	}
	return &library.ItemOrigin{
		Site:         string(site.X),
		ID:           originID,
		CanonicalURL: originCanonical,
		Author: library.ItemAuthor{
			Site:     string(site.X),
			UID:      originUID,
			UniqueID: strings.TrimSpace(plan.OriginAuthorUniqueID),
			Name:     strings.TrimSpace(plan.OriginAuthorName),
			Face:     sanitizeXTaskURL(plan.OriginAuthorFace),
		},
		PubTs:             plan.OriginPubTs,
		Title:             strings.TrimSpace(plan.OriginVideoTitle),
		Text:              strings.TrimSpace(plan.OriginText),
		Card:              buildWeiboItemRichCard(plan.OriginCard, nil),
		Unavailable:       plan.OriginUnavailable,
		UnavailableReason: strings.TrimSpace(plan.OriginUnavailableReason),
		VideoFromOrigin:   plan.VideoFromOrigin,
	}
}

func normalizeXMediaTransport(in site.MediaTransportSpec) site.MediaTransportSpec {
	base := site.XMediaTransport(in.UserAgent, in.AllowLocalhost)
	if len(in.AllowedDomains) > 0 {
		base.AllowedDomains = append([]string(nil), in.AllowedDomains...)
	}
	// Per x.md §6.2 & §15.1 #15: never send Cookie, Referer, or Origin to X CDN,
	// and keep redirects strictly within the explicit X CDN allowlist.
	base.Referer = ""
	base.Origin = ""
	base.PublicRedirects = false
	base.AllowedInitialHostPaths = nil
	return base
}

func isHLSPlaylistURL(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false
	}
	if u, err := url.Parse(trimmed); err == nil {
		lp := strings.ToLower(u.Path)
		if strings.HasSuffix(lp, ".m3u8") || strings.HasSuffix(lp, ".m3u") {
			return true
		}
	}
	lower := strings.ToLower(trimmed)
	return strings.Contains(lower, ".m3u8")
}

func filterProgressiveXVideoURLs(urls []string) (mp4URLs []string, sawHLS bool) {
	for _, raw := range urls {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		if isHLSPlaylistURL(trimmed) {
			sawHLS = true
			continue
		}
		mp4URLs = append(mp4URLs, trimmed)
	}
	return mp4URLs, sawHLS
}

func isXPlanHLSOnly(plan WorkDownloadPlan) bool {
	if plan.HLSOnly {
		return true
	}
	mp4s, sawHLS := filterProgressiveXVideoURLs(plan.VideoURLs)
	if len(mp4s) > 0 {
		return false
	}
	if sawHLS {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(plan.Codec), "hls") ||
		strings.EqualFold(strings.TrimSpace(plan.QualityName), "hls") ||
		strings.EqualFold(strings.TrimSpace(plan.QualityName), "hls_only") ||
		strings.Contains(strings.ToLower(plan.DowngradeHint), "分段流") {
		return true
	}
	return false
}

func isXPageHLSOnly(page PlannedNotePage) bool {
	if page.HLSOnly {
		return true
	}
	mp4s, sawHLS := filterProgressiveXVideoURLs(page.VideoURLs)
	if len(mp4s) > 0 {
		return false
	}
	if sawHLS {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(page.Codec), "hls") ||
		strings.EqualFold(strings.TrimSpace(page.QualityName), "hls") ||
		strings.EqualFold(strings.TrimSpace(page.QualityName), "hls_only")
}

// buildXImageCandidateURLs normalizes pbs.twimg.com/media/ URLs to ?format=<ext>&name=orig
// (per x.md §5.4 #1). Recompressed ?name=large copies are never used as fallbacks.
// Non-media or non-allowlisted URLs pass through unchanged for host allowlist enforcement.
func buildXImageCandidateURLs(rawURLs []string) []string {
	var primaries []string
	seen := make(map[string]bool, len(rawURLs))
	addPrimary := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			primaries = append(primaries, s)
		}
	}
	for _, raw := range rawURLs {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		origURL, _, normalized := normalizeXMediaImageOrigAndLarge(trimmed)
		if normalized {
			// Originals only: never fall back to the recompressed name=large copy.
			addPrimary(origURL)
		} else {
			addPrimary(trimmed)
		}
	}
	return primaries
}

func normalizeXMediaImageOrigAndLarge(raw string) (origURL, largeURL string, normalized bool) {
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.User != nil {
		return raw, "", false
	}
	if !strings.EqualFold(u.Hostname(), "pbs.twimg.com") || !strings.HasPrefix(u.Path, "/media/") {
		return raw, "", false
	}

	p := u.Path
	// Strip legacy Twitter colon size suffix (e.g. /media/ID.jpg:large).
	if slash := strings.LastIndexByte(p, '/'); slash >= 0 {
		if colon := strings.IndexByte(p[slash+1:], ':'); colon >= 0 {
			p = p[:slash+1+colon]
		}
	}

	q := u.Query()
	ext := strings.ToLower(path.Ext(p))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".webp":
		p = strings.TrimSuffix(p, ext)
		fmtVal := strings.TrimPrefix(ext, ".")
		if fmtVal == "jpeg" {
			fmtVal = "jpg"
		}
		if strings.TrimSpace(q.Get("format")) == "" {
			q.Set("format", fmtVal)
		}
	}
	if strings.EqualFold(strings.TrimSpace(q.Get("format")), "jpeg") {
		q.Set("format", "jpg")
	}

	origU := *u
	origU.Path = p
	origQ := cloneValues(q)
	origQ.Set("name", "orig")
	origU.RawQuery = origQ.Encode()
	origU.Fragment = ""

	largeU := *u
	largeU.Path = p
	largeQ := cloneValues(q)
	largeQ.Set("name", "large")
	largeU.RawQuery = largeQ.Encode()
	largeU.Fragment = ""

	return origU.String(), largeU.String(), true
}

func cloneValues(in url.Values) url.Values {
	out := make(url.Values, len(in))
	for k, v := range in {
		out[k] = append([]string(nil), v...)
	}
	return out
}

func xFallbackCanonicalURL(rawID string) string {
	return "https://x.com/i/web/status/" + rawID
}

func sanitizeXCanonicalPageURL(raw string) string {
	clean := sanitizeStoredURL(raw)
	if clean == "" {
		return ""
	}
	if u, err := url.Parse(clean); err == nil {
		host := strings.ToLower(u.Hostname())
		if host == "pbs.twimg.com" || host == "video.twimg.com" || host == "video-s.twimg.com" ||
			host == "ton.x.com" || host == "ton.twitter.com" {
			return ""
		}
	}
	return clean
}

// sanitizeXTaskURL allows only clean public pbs.twimg.com image/avatar URLs
// (optionally with public format/name parameters) and strips all video/private/signed URLs.
func sanitizeXTaskURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	lower := strings.ToLower(trimmed)
	for _, marker := range []string{
		"auth_token=", "auth_token%3d",
		"ct0=", "ct0%3d",
		"kdt=", "kdt%3d",
		"twid=", "twid%3d",
		"guest_id=", "guest_id%3d",
		"x-csrf-token", "authorization", "bearer",
		"sig=", "signature=", "token=", "expires=", "policy=", "hmac=",
	} {
		if strings.Contains(lower, marker) {
			return ""
		}
	}
	u, err := url.Parse(trimmed)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Host == "" || u.User != nil {
		return ""
	}
	if port := u.Port(); port != "" && port != "443" {
		return ""
	}
	if !strings.EqualFold(u.Hostname(), "pbs.twimg.com") {
		return ""
	}
	if u.RawQuery != "" {
		q, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			return ""
		}
		cleanQ := url.Values{}
		for k, vals := range q {
			if len(vals) != 1 {
				return ""
			}
			v := strings.TrimSpace(vals[0])
			switch k {
			case "format":
				if len(v) == 0 || len(v) > 8 || !isASCIIAlphaNum(v, false) {
					return ""
				}
				cleanQ.Set("format", v)
			case "name":
				if len(v) == 0 || len(v) > 16 || !isASCIIAlphaNum(v, true) {
					return ""
				}
				cleanQ.Set("name", v)
			default:
				return ""
			}
		}
		u.RawQuery = cleanQ.Encode()
	}
	u.Fragment = ""
	return u.String()
}

func isASCIIAlphaNum(s string, allowUnderscore bool) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || (allowUnderscore && c == '_')
		if !ok {
			return false
		}
	}
	return len(s) > 0
}
