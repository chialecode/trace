package download

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
)

// WeiboImageUnavailableReason is recorded when all candidate URLs for a non-GIF Weibo image
// return Sina CDN's HTTP-200 deleted-image placeholder GIF (or 404/410).
const WeiboImageUnavailableReason = "图片已在源站失效"

type weiboPlaceholderGIFError struct{}

func (e *weiboPlaceholderGIFError) Error() string { return WeiboImageUnavailableReason }

func (e *weiboPlaceholderGIFError) Unwrap() error {
	return &site.Error{
		Site:    site.Weibo,
		Kind:    site.ErrKindUnavailable,
		Message: WeiboImageUnavailableReason,
	}
}

func isWeiboPlaceholderError(err error) bool {
	var target *weiboPlaceholderGIFError
	return errors.As(err, &target)
}

// WeiboWorkEnqueueRequest specifies a Weibo post (mid) or standalone video object (1034:<id>)
// to enqueue for local caching.
type WeiboWorkEnqueueRequest struct {
	TargetID string             `json:"targetId"`
	Kind     string             `json:"kind,omitempty"`   // optional hint: "dynamic" (posts, default) or "video" (standalone 1034: without host post)
	Source   string             `json:"source,omitempty"` // "manual" or "auto"
	Plan     *WorkDownloadPlan  `json:"-"`
	SitePlan *site.DownloadPlan `json:"-"`
	// Via optionally supplies forwarding context (DEC-056) when caching a retweet or
	// fast-retweet under the forwarder's forwards/ folder.
	Via *library.ItemVia `json:"via,omitempty"`
}

// EnqueueWeiboWork resolves (or accepts an in-memory plan for) a Weibo post or standalone video
// and enqueues a persistent download task.
func (m *Manager) EnqueueWeiboWork(ctx context.Context, req WeiboWorkEnqueueRequest) (Task, error) {
	rawTarget := strings.TrimSpace(req.TargetID)
	if rawTarget == "" && req.Plan != nil {
		rawTarget = strings.TrimSpace(req.Plan.TargetID)
	}
	if rawTarget == "" && req.SitePlan != nil {
		rawTarget = strings.TrimSpace(req.SitePlan.TargetID)
	}
	if rawTarget == "" {
		return Task{}, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBadRequest,
			Message: "无效的微博目标 ID",
		}
	}

	planner, hasPlanner := m.getWorkPlanner(site.Weibo)
	if checker, ok := planner.(SiteAuthChecker); ok {
		if err := checker.RequireAuth("download:weibo"); err != nil {
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
				Site:    site.Weibo,
				Kind:    site.ErrKindUnavailable,
				Message: "微博下载规划器尚未初始化",
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

	plan.Site = site.Weibo
	if strings.TrimSpace(plan.TargetID) == "" {
		plan.TargetID = rawTarget
	}

	itemKind, rawID, itemKey, plannerTarget, err := resolveWeiboIdentity(req.Kind, plan.ItemKey, plan.TargetID)
	if err != nil {
		return Task{}, err
	}
	plan.Kind = itemKind
	plan.ItemKey = itemKey
	plan.TargetID = plannerTarget

	normUID, err := site.NormalizeUID(site.Weibo, plan.AuthorUID)
	if err != nil {
		return Task{}, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindSchemaChanged,
			Message: "微博内容缺少有效作者 UID",
		}
	}
	plan.AuthorUID = normUID
	plan.Transport = normalizeWeiboMediaTransport(plan.Transport)

	forward, via, folderUID, err := resolveWeiboPlacement(plan, req.Via, rawID)
	if err != nil {
		return Task{}, err
	}

	cacheRoot := m.lib.CacheDir()
	itemDir, err := library.WeiboPostDir(cacheRoot, folderUID, rawID, forward)
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
		coverDisplay = sanitizeWeiboTaskURL(plan.CoverURLs[0])
	}
	title := firstNonEmptyStr(plan.Title, plan.Desc, "微博 "+rawID)

	t, err := m.enqueueSingleTaskWithPlan(Task{
		Site:        string(site.Weibo),
		Kind:        itemKind,
		Source:      source,
		TargetID:    plannerTarget,
		Page:        1,
		Title:       title,
		AuthorUID:   normUID,
		AuthorName:  plan.AuthorName,
		AuthorFace:  sanitizeWeiboTaskURL(plan.AuthorFace),
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

func (m *Manager) consumeInitialOrFetchWeiboPlan(ctx context.Context, taskID, targetID string) (WorkDownloadPlan, WorkPlanner, error) {
	m.mu.Lock()
	initial, hasInitial := m.initialPlans[taskID]
	if hasInitial {
		delete(m.initialPlans, taskID)
	}
	planner := m.planners[site.Weibo]
	opts := WorkPlanOptions{
		VideoQuality: m.dlSettings.VideoQuality,
		CodecOrder:   append([]string(nil), m.dlSettings.CodecOrder...),
	}
	m.mu.Unlock()

	if hasInitial {
		initial.Site = site.Weibo
		initial.Transport = normalizeWeiboMediaTransport(initial.Transport)
		return initial, planner, nil
	}
	if planner == nil {
		return WorkDownloadPlan{}, nil, &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindUnavailable,
			Message: "微博下载规划器尚未初始化",
		}
	}
	if checker, ok := planner.(SiteAuthChecker); ok {
		if err := checker.RequireAuth("download:weibo"); err != nil {
			return WorkDownloadPlan{}, planner, err
		}
	}
	plan, err := planner.PlanWorkDownload(ctx, targetID, opts)
	if err != nil {
		return WorkDownloadPlan{}, planner, err
	}
	plan.Site = site.Weibo
	plan.Transport = normalizeWeiboMediaTransport(plan.Transport)
	return plan, planner, nil
}

func (m *Manager) executeWeiboTask(ctx context.Context, t *Task) error {
	lockTarget := strings.ReplaceAll(strings.TrimSpace(t.TargetID), ":", "_")
	unlock, err := m.lockItem(ctx, "weibo:"+lockTarget)
	if err != nil {
		return err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	plan, planner, err := m.consumeInitialOrFetchWeiboPlan(ctx, t.TaskID, t.TargetID)
	if err != nil {
		return err
	}
	normUID, err := site.NormalizeUID(site.Weibo, firstNonEmptyStr(plan.AuthorUID, t.AuthorUID))
	if err != nil {
		return &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindSchemaChanged,
			Message: "微博内容缺少有效作者 UID",
		}
	}
	plan.AuthorUID = normUID
	t.AuthorUID = normUID
	plan.Transport = normalizeWeiboMediaTransport(plan.Transport)

	itemKind, rawID, itemKey, plannerTarget, err := resolveWeiboIdentity(t.Kind, firstNonEmptyStr(plan.ItemKey, t.ItemKey), firstNonEmptyStr(plan.TargetID, t.TargetID))
	if err != nil {
		return err
	}
	t.Kind = itemKind
	t.ItemKey = itemKey
	t.TargetID = plannerTarget
	plan.Kind = itemKind
	plan.ItemKey = itemKey
	plan.TargetID = plannerTarget

	forward, via, folderUID, err := resolveWeiboPlacement(plan, t.Via, rawID)
	if err != nil {
		return err
	}
	previous, exists, itemDir, resolvedForward, resolvedVia, err := m.resolveWeiboItemDir(t, itemKey, folderUID, rawID, forward, via)
	if err != nil {
		return err
	}

	if len(plan.Pages) == 0 && len(plan.VideoURLs) > 0 {
		return m.executeWeiboSingleVideoTask(ctx, t, plan, planner, itemKind, rawID, previous, exists, itemDir, resolvedForward, resolvedVia)
	}
	return m.executeWeiboPostTask(ctx, t, plan, planner, itemKind, rawID, previous, exists, itemDir, resolvedForward, resolvedVia)
}

func (m *Manager) resolveWeiboItemDir(
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
	itemDir, err := library.WeiboPostDir(cacheRoot, folderUID, rawID, forward)
	if err != nil {
		return library.ItemMetadata{}, false, "", false, nil, err
	}
	resolvedForward := forward
	resolvedVia := via
	if exists && previous.ItemDir != "" {
		// Already-cached items remain in their existing directory and keep their original
		// via and forward classification (neither overwriting an original post with a
		// later fast-retweet via nor downgrading an already-cached forward).
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

func (m *Manager) executeWeiboSingleVideoTask(
	ctx context.Context,
	t *Task,
	plan WorkDownloadPlan,
	planner WorkPlanner,
	itemKind, rawID string,
	previous library.ItemMetadata,
	exists bool,
	itemDir string,
	forward bool,
	via *library.ItemVia,
) (result error) {
	m.mu.Lock()
	cfg := m.dlSettings
	m.mu.Unlock()

	title := firstNonEmptyStr(plan.Title, plan.Desc, t.Title, "微博视频 "+rawID)
	canonicalURL := firstNonEmptyStr(sanitizeStoredURL(plan.CanonicalURL), weiboFallbackCanonicalURL(itemKind, rawID))
	t.Title = title
	t.AuthorName = firstNonEmptyStr(plan.AuthorName, t.AuthorName)
	t.AuthorFace = sanitizeWeiboTaskURL(firstNonEmptyStr(plan.AuthorFace, t.AuthorFace))
	if len(plan.CoverURLs) > 0 {
		t.CoverURL = sanitizeWeiboTaskURL(plan.CoverURLs[0])
	}
	if plan.DowngradeHint != "" {
		t.QualityHint = plan.DowngradeHint
	}

	dynType := "video"
	typeLabel := "视频"
	if forward {
		dynType = "forward"
		typeLabel = "转发"
	} else if itemKind == "video" {
		dynType = ""
	}

	originMeta := buildWeiboItemOrigin(plan, previous)
	cardMeta := buildWeiboItemRichCard(plan.Card, previous.Card)

	meta := library.ItemMetadata{
		Version:     1,
		ID:          itemKind + ":" + rawID,
		Site:        string(site.Weibo),
		Kind:        itemKind,
		Status:      "partial",
		Title:       title,
		Desc:        plan.Desc,
		Text:        firstNonEmptyStr(plan.Desc, plan.Title),
		DynamicType: dynType,
		TypeLabel:   typeLabel,
		URL:         canonicalURL,
		PubTs:       plan.PubTs,
		CachedAt:    previous.CachedAt,
		Duration:    plan.DurationSec,
		Author: library.ItemAuthor{
			Site: string(site.Weibo),
			UID:  plan.AuthorUID,
			Name: t.AuthorName,
			Face: t.AuthorFace,
		},
		Media:        append([]library.MediaFile(nil), previous.Media...),
		Missing:      append([]library.MissingRecord(nil), previous.Missing...),
		DirectCached: true,
		Via:          via,
		Origin:       originMeta,
		Card:         cardMeta,
		Links:        buildWeiboItemLinks(plan.Links, previous.Links),
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

	refreshOncePlan := m.makeSinglePlanRefresherForSite(ctx, site.Weibo, t.TargetID, planner)

	// 1. Save cover.<ext> when enabled.
	if cfg.SaveCover && len(plan.CoverURLs) > 0 {
		refreshCover := makeOnceURLRefresher(refreshOncePlan, func(fresh WorkDownloadPlan) ([]string, error) {
			return fresh.CoverURLs, nil
		})
		if coverMedia, cErr := m.cacheWeiboStillImage(ctx, t, itemDir, previous, exists, "cover", "cover", plan.CoverURLs, 0, 0, plan.Transport, refreshCover); cErr != nil {
			if isSiteStopError(cErr) || isStorageError(cErr) || ctx.Err() != nil {
				return cErr
			}
			coverReason := "封面保存失败 (" + classifyDownloadError(cErr) + ")"
			if isWeiboPlaceholderError(cErr) {
				coverReason = "封面" + WeiboImageUnavailableReason
			}
			addMissing(&meta, "cover", coverReason)
		} else if coverMedia != nil {
			coverMedia.FromOrigin = plan.VideoFromOrigin
			meta.Media = replaceRoleMedia(meta.Media, "cover", *coverMedia)
			clearMissing(&meta, "cover")
		}
	}

	// 2. Download & validate single-file video.mp4 (streaming moov relocation, no elst required).
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
				return nil, &site.Error{Site: site.Weibo, Kind: site.ErrKindUnavailable, Message: "续传地址的视频编码发生变化，已保留断点"}
			}
			if plan.QualityName != "" && fresh.QualityName != "" && !strings.EqualFold(plan.QualityName, fresh.QualityName) {
				return nil, &site.Error{Site: site.Weibo, Kind: site.ErrKindUnavailable, Message: "续传地址的清晰度档位发生变化，已保留断点"}
			}
			return fresh.VideoURLs, nil
		})
		rawRel := videoRel + stageSuffix
		stagingPath := videoPath + stageSuffix
		_, _, dlErr := m.downloadWithTransportRefresh(ctx, plan.Transport, plan.VideoURLs, stagingPath, "video", t, refreshVideoURLs)
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

func (m *Manager) executeWeiboPostTask(
	ctx context.Context,
	t *Task,
	plan WorkDownloadPlan,
	planner WorkPlanner,
	itemKind, rawID string,
	previous library.ItemMetadata,
	exists bool,
	itemDir string,
	forward bool,
	via *library.ItemVia,
) (result error) {
	m.mu.Lock()
	cfg := m.dlSettings
	m.mu.Unlock()

	title := firstNonEmptyStr(plan.Title, plan.Desc, t.Title, "微博 "+rawID)
	canonicalURL := firstNonEmptyStr(sanitizeStoredURL(plan.CanonicalURL), weiboFallbackCanonicalURL(itemKind, rawID))
	t.Title = title
	t.AuthorName = firstNonEmptyStr(plan.AuthorName, t.AuthorName)
	t.AuthorFace = sanitizeWeiboTaskURL(firstNonEmptyStr(plan.AuthorFace, t.AuthorFace))
	if len(plan.CoverURLs) > 0 {
		t.CoverURL = sanitizeWeiboTaskURL(plan.CoverURLs[0])
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

	originMeta := buildWeiboItemOrigin(plan, previous)
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
				Reason: "微博缓存尚未完成 (" + classifyDownloadError(result) + ")",
			})
		}
		partialMeta := library.ItemMetadata{
			Version:     1,
			ID:          itemKind + ":" + rawID,
			Site:        string(site.Weibo),
			Kind:        itemKind,
			Status:      "partial",
			Title:       title,
			Desc:        plan.Desc,
			Text:        firstNonEmptyStr(plan.Desc, plan.Title),
			DynamicType: dynType,
			TypeLabel:   typeLabel,
			URL:         canonicalURL,
			PubTs:       plan.PubTs,
			CachedAt:    previous.CachedAt,
			Author: library.ItemAuthor{
				Site: string(site.Weibo),
				UID:  plan.AuthorUID,
				Name: t.AuthorName,
				Face: t.AuthorFace,
			},
			Media:        mergeMediaFiles(previous.Media, mediaFiles),
			NotePages:    notePages,
			Missing:      missing,
			DirectCached: true,
			Via:          via,
			Origin:       originMeta,
			Card:         cardMeta,
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

	refreshOncePlan := m.makeSinglePlanRefresherForSite(ctx, site.Weibo, t.TargetID, planner)
	livePhotoTransport := weiboLivePhotoTransport(plan.Transport)

	if cfg.SaveCover && len(plan.CoverURLs) > 0 && len(plan.Pages) == 0 {
		refreshCover := makeOnceURLRefresher(refreshOncePlan, func(fresh WorkDownloadPlan) ([]string, error) {
			return fresh.CoverURLs, nil
		})
		if coverMedia, cErr := m.cacheWeiboStillImage(ctx, t, itemDir, previous, exists, "cover", "cover", plan.CoverURLs, 0, 0, plan.Transport, refreshCover); cErr != nil {
			if isSiteStopError(cErr) || isStorageError(cErr) || ctx.Err() != nil {
				return cErr
			}
			coverReason := "封面保存失败 (" + classifyDownloadError(cErr) + ")"
			if isWeiboPlaceholderError(cErr) {
				coverReason = "封面" + WeiboImageUnavailableReason
			}
			missing = append(missing, library.MissingRecord{
				Item:   "cover",
				Reason: coverReason,
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
		if pageKind == "" {
			if len(page.VideoURLs) > 0 {
				pageKind = "live_photo"
			} else {
				pageKind = "image"
			}
		}

		switch pageKind {
		case "gif":
			refreshImg := makeOnceURLRefresher(refreshOncePlan, func(fresh WorkDownloadPlan) ([]string, error) {
				if p, ok := findPlannedPage(fresh.Pages, pageIdx, i); ok {
					return p.ImageURLs, nil
				}
				return nil, nil
			})
			imgMedia, imgErr := m.cacheDouyinSniffedImage(ctx, t, itemDir, previous, exists, "image", prefix, page.ImageURLs, page.Width, page.Height, plan.Transport, refreshImg)
			if imgErr != nil {
				if isSiteStopError(imgErr) || isStorageError(imgErr) || ctx.Err() != nil {
					return imgErr
				}
				reason := fmt.Sprintf("第 %02d 张动图下载失败 (%s)", pageIdx, classifyDownloadError(imgErr))
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

		case "image":
			refreshImg := makeOnceURLRefresher(refreshOncePlan, func(fresh WorkDownloadPlan) ([]string, error) {
				if p, ok := findPlannedPage(fresh.Pages, pageIdx, i); ok {
					return p.ImageURLs, nil
				}
				return nil, nil
			})
			imgMedia, imgErr := m.cacheWeiboStillImage(ctx, t, itemDir, previous, exists, "image", prefix, page.ImageURLs, page.Width, page.Height, plan.Transport, refreshImg)
			if imgErr != nil {
				if isSiteStopError(imgErr) || isStorageError(imgErr) || ctx.Err() != nil {
					return imgErr
				}
				reason := fmt.Sprintf("第 %02d 张图片下载失败 (%s)", pageIdx, classifyDownloadError(imgErr))
				if isWeiboPlaceholderError(imgErr) {
					reason = fmt.Sprintf("第 %02d 张%s", pageIdx, WeiboImageUnavailableReason)
				}
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

		case "live_photo", "motion":
			refreshImg := makeOnceURLRefresher(refreshOncePlan, func(fresh WorkDownloadPlan) ([]string, error) {
				if p, ok := findPlannedPage(fresh.Pages, pageIdx, i); ok {
					return p.ImageURLs, nil
				}
				return nil, nil
			})
			imgMedia, imgErr := m.cacheWeiboStillImage(ctx, t, itemDir, previous, exists, "image", prefix, page.ImageURLs, page.Width, page.Height, plan.Transport, refreshImg)
			if imgErr != nil {
				if isSiteStopError(imgErr) || isStorageError(imgErr) || ctx.Err() != nil {
					return imgErr
				}
				reason := fmt.Sprintf("第 %02d 张实况静帧下载失败 (%s)", pageIdx, classifyDownloadError(imgErr))
				if isWeiboPlaceholderError(imgErr) {
					reason = fmt.Sprintf("第 %02d 张实况静帧：%s", pageIdx, WeiboImageUnavailableReason)
				}
				missing = append(missing, library.MissingRecord{Item: fmt.Sprintf("page_%02d", pageIdx), Reason: reason})
				partialErrors = append(partialErrors, reason)
			}

			motionRel := fmt.Sprintf("images/%02d_live.mp4", pageIdx)
			refreshMotion := makeOnceURLRefresher(refreshOncePlan, func(fresh WorkDownloadPlan) ([]string, error) {
				if p, ok := findPlannedPage(fresh.Pages, pageIdx, i); ok {
					return p.VideoURLs, nil
				}
				return nil, nil
			})
			motionMedia, mErr := m.cacheWeiboLivePhotoClip(ctx, t, itemDir, previous, exists, motionRel, page.VideoURLs, page.DurationSec, page.Width, page.Height, livePhotoTransport, refreshMotion)
			rec := library.NotePageRecord{
				Index:      pageIdx,
				Kind:       "motion",
				FromOrigin: page.FromOrigin,
				Width:      page.Width,
				Height:     page.Height,
				Duration:   page.DurationSec,
			}
			if imgMedia != nil {
				rec.Image = imgMedia.Path
			}
			if mErr != nil {
				if isSiteStopError(mErr) || isStorageError(mErr) || ctx.Err() != nil {
					return mErr
				}
				reason := fmt.Sprintf("第 %02d 张实况片段下载失败 (%s)", pageIdx, classifyDownloadError(mErr))
				missing = append(missing, library.MissingRecord{Item: motionRel, Reason: reason})
				partialErrors = append(partialErrors, reason)
			} else if motionMedia != nil {
				motionMedia.FromOrigin = page.FromOrigin
				if imgMedia != nil {
					imgMedia.Motion = motionMedia.Path
				}
				rec.Motion = motionMedia.Path
				if motionMedia.Duration > 0 {
					rec.Duration = motionMedia.Duration
				}
				mediaFiles = append(mediaFiles, *motionMedia)
			}
			if imgMedia != nil {
				imgMedia.FromOrigin = page.FromOrigin
				mediaFiles = append(mediaFiles, *imgMedia)
			}
			notePages = append(notePages, rec)

		case "video":
			var coverRel string
			if len(page.ImageURLs) > 0 {
				refreshPageCover := makeOnceURLRefresher(refreshOncePlan, func(fresh WorkDownloadPlan) ([]string, error) {
					if p, ok := findPlannedPage(fresh.Pages, pageIdx, i); ok {
						return p.ImageURLs, nil
					}
					return nil, nil
				})
				coverMedia, cErr := m.cacheWeiboStillImage(ctx, t, itemDir, previous, exists, "cover", prefix, page.ImageURLs, page.Width, page.Height, plan.Transport, refreshPageCover)
				if cErr != nil {
					if isSiteStopError(cErr) || isStorageError(cErr) || ctx.Err() != nil {
						return cErr
					}
					reason := fmt.Sprintf("第 %02d 项视频封面下载失败 (%s)", pageIdx, classifyDownloadError(cErr))
					if isWeiboPlaceholderError(cErr) {
						reason = fmt.Sprintf("第 %02d 项视频封面：%s", pageIdx, WeiboImageUnavailableReason)
					}
					missing = append(missing, library.MissingRecord{Item: fmt.Sprintf("page_%02d.cover", pageIdx), Reason: reason})
					partialErrors = append(partialErrors, reason)
				} else if coverMedia != nil {
					coverMedia.FromOrigin = page.FromOrigin
					coverRel = coverMedia.Path
					mediaFiles = append(mediaFiles, *coverMedia)
				}
			}

			videoRel := fmt.Sprintf("images/%02d.mp4", pageIdx)
			refreshPageVideo := makeOnceURLRefresher(refreshOncePlan, func(fresh WorkDownloadPlan) ([]string, error) {
				if p, ok := findPlannedPage(fresh.Pages, pageIdx, i); ok {
					return p.VideoURLs, nil
				}
				return nil, nil
			})
			vidMedia, vErr := m.cacheWeiboSingleMP4(ctx, t, itemDir, previous, exists, "video", videoRel, page.VideoURLs, page.DurationSec, page.Width, page.Height, plan.Transport, refreshPageVideo, false)
			rec := library.NotePageRecord{
				Index:      pageIdx,
				Kind:       "video",
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
				refreshUnknownImg := makeOnceURLRefresher(refreshOncePlan, func(fresh WorkDownloadPlan) ([]string, error) {
					if p, ok := findPlannedPage(fresh.Pages, pageIdx, i); ok {
						return p.ImageURLs, nil
					}
					return nil, nil
				})
				if imgMedia, imgErr := m.cacheWeiboStillImage(ctx, t, itemDir, previous, exists, "image", prefix, page.ImageURLs, page.Width, page.Height, plan.Transport, refreshUnknownImg); imgErr != nil {
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

	meta := library.ItemMetadata{
		Version:     1,
		ID:          itemKind + ":" + rawID,
		Site:        string(site.Weibo),
		Kind:        itemKind,
		Status:      status,
		Title:       title,
		Desc:        plan.Desc,
		Text:        firstNonEmptyStr(plan.Desc, plan.Title),
		DynamicType: dynType,
		TypeLabel:   typeLabel,
		URL:         canonicalURL,
		PubTs:       plan.PubTs,
		CachedAt:    previous.CachedAt,
		Author: library.ItemAuthor{
			Site: string(site.Weibo),
			UID:  plan.AuthorUID,
			Name: t.AuthorName,
			Face: t.AuthorFace,
		},
		Media:        mediaFiles,
		NotePages:    notePages,
		Missing:      missing,
		DirectCached: true,
		Via:          via,
		Origin:       originMeta,
		Card:         cardMeta,
		Links:        buildWeiboItemLinks(plan.Links, previous.Links),
	}

	saved, saveErr := m.lib.SaveItem(itemDir, meta)
	if saveErr != nil {
		return saveErr
	}
	t.ItemKey = saved.ItemKey
	finalSaved = true
	return nil
}

func (m *Manager) cacheWeiboStillImage(
	ctx context.Context,
	t *Task,
	itemDir string,
	previous library.ItemMetadata,
	exists bool,
	role string,
	relStem string,
	urls []string,
	width, height int,
	transport site.MediaTransportSpec,
	refreshURLs func() ([]string, error),
) (*library.MediaFile, error) {
	if exists {
		for _, mf := range previous.Media {
			if mf.Role == role && strings.HasPrefix(mf.Path, relStem+".") && m.mediaAlreadyCompleted(previous, mf) {
				// Builds before the placeholder check saved Sina's HTTP-200
				// "image deleted" GIF as if it were the picture. Never reuse
				// it: drop the placeholder and try the source again.
				if strings.EqualFold(filepath.Ext(mf.Path), ".gif") {
					if full, rErr := library.ResolveItemPath(itemDir, mf.Path); rErr == nil {
						firstURL := ""
						if len(urls) > 0 {
							firstURL = urls[0]
						}
						if looksLikeWeiboPlaceholderGIF(full, firstURL, width, height) {
							_ = os.Remove(full)
							continue
						}
					}
				}
				m.setFileProgress(t, role, mf.Path, "completed", mf.Bytes, mf.Bytes, 0)
				cp := mf
				return &cp, nil
			}
		}
	}

	tempRel := relStem + imgSuffix
	m.adoptLegacyStaging(t, itemDir, relStem, ".imgtmp", imgSuffix)
	tempFull, err := library.ResolveItemPath(itemDir, tempRel)
	if err != nil {
		return nil, err
	}
	if err := m.claimTaskFile(t, itemDir, tempRel, tempFull); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(tempFull), 0o700); err != nil {
		return nil, err
	}

	spec := m.transportRangeSpec(transport, role, false)
	tried := make(map[string]bool)
	sawPlaceholder := false
	var lastErr error

	tryCandidates := func(cands []string, allowAlreadyTriedExact bool) (int64, string, string, bool, error) {
		for _, cand := range cands {
			cand = strings.TrimSpace(cand)
			if cand == "" {
				continue
			}
			if !allowAlreadyTriedExact && tried[cand] {
				continue
			}
			tried[cand] = true
			// Clear any stale temp file before trying the next candidate URL.
			_ = os.Remove(tempFull)
			_ = os.Remove(tempFull + ".part")
			m.resetFileProgress(t, tempRel)

			n, sha, ct, dlErr := m.downloadRange(ctx, spec, []string{cand}, tempFull, t)
			if dlErr != nil {
				if ctx.Err() != nil {
					return 0, "", "", false, ctx.Err()
				}
				_ = os.Remove(tempFull)
				if isSiteStopError(dlErr) || isStorageError(dlErr) {
					return 0, "", "", false, dlErr
				}
				lastErr = dlErr
				continue
			}
			if looksLikeWeiboPlaceholderGIF(tempFull, cand, width, height) {
				_ = os.Remove(tempFull)
				sawPlaceholder = true
				lastErr = &weiboPlaceholderGIFError{}
				continue
			}
			ext, sErr := sniffImageExtension(tempFull, ct)
			if sErr != nil {
				_ = os.Remove(tempFull)
				lastErr = &site.Error{Site: transport.Site, Kind: site.ErrKindSchemaChanged, Message: sErr.Error()}
				continue
			}
			return n, sha, ext, true, nil
		}
		return 0, "", "", false, nil
	}

	initialCands := expandWeiboImageCandidates(urls)
	n, sha, ext, ok, fatalErr := tryCandidates(initialCands, false)
	if fatalErr != nil {
		if ctx.Err() == nil && !fileOrPartExists(tempFull) {
			m.removeTempTaskProgress(t, tempRel)
		}
		return nil, fatalErr
	}
	if !ok && refreshURLs != nil && (sawPlaceholder || lastErr == nil || site.IsKind(lastErr, site.ErrKindForbidden) || site.IsKind(lastErr, site.ErrKindUnavailable)) {
		freshURLs, rErr := refreshURLs()
		if rErr != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if isSiteStopError(rErr) || isStorageError(rErr) {
				if !fileOrPartExists(tempFull) {
					m.removeTempTaskProgress(t, tempRel)
				}
				return nil, rErr
			}
		} else if len(freshURLs) > 0 {
			// Try any exact refreshed URLs first, then newly derived fallbacks not yet attempted.
			n, sha, ext, ok, fatalErr = tryCandidates(freshURLs, true)
			if fatalErr != nil {
				if ctx.Err() == nil && !fileOrPartExists(tempFull) {
					m.removeTempTaskProgress(t, tempRel)
				}
				return nil, fatalErr
			}
			if !ok {
				n, sha, ext, ok, fatalErr = tryCandidates(expandWeiboImageCandidates(freshURLs), false)
				if fatalErr != nil {
					if ctx.Err() == nil && !fileOrPartExists(tempFull) {
						m.removeTempTaskProgress(t, tempRel)
					}
					return nil, fatalErr
				}
			}
		}
	}
	if !ok {
		if ctx.Err() == nil {
			_ = os.Remove(tempFull)
			_ = os.Remove(tempFull + ".part")
			m.removeTempTaskProgress(t, tempRel)
		}
		if sawPlaceholder {
			return nil, &weiboPlaceholderGIFError{}
		}
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, &site.Error{Site: site.Weibo, Kind: site.ErrKindUnavailable, Message: "图片下载地址列表为空"}
	}

	finalRel := relStem + ext
	finalFull, err := library.ResolveItemPath(itemDir, finalRel)
	if err != nil {
		if ctx.Err() == nil {
			_ = os.Remove(tempFull)
			m.removeTempTaskProgress(t, tempRel)
		}
		return nil, err
	}
	if err := m.claimTaskFile(t, itemDir, finalRel, finalFull); err != nil {
		if ctx.Err() == nil {
			_ = os.Remove(tempFull)
			m.removeTempTaskProgress(t, tempRel)
		}
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.Rename(tempFull, finalFull); err != nil {
		if ctx.Err() == nil {
			_ = os.Remove(tempFull)
			m.removeTempTaskProgress(t, tempRel)
		}
		return nil, err
	}
	m.removeTempTaskProgress(t, tempRel)
	m.setFileProgress(t, role, finalRel, "completed", n, n, 0)
	return &library.MediaFile{
		Role:   role,
		Path:   finalRel,
		Bytes:  n,
		SHA256: sha,
		Width:  width,
		Height: height,
	}, nil
}

// expandWeiboImageCandidates preserves the caller's ordered URLs first and appends
// deduplicated alternate wx1..wx4.sinaimg.cn edge hosts and alternate size tiers
// (/large/, /mw2000/, /orj1080/, /bmiddle/) so a deleted-image placeholder GIF or
// missing edge tier automatically falls back before giving up.
func expandWeiboImageCandidates(urls []string) []string {
	var out []string
	seen := make(map[string]bool, len(urls)*4)
	add := func(u string) {
		u = strings.TrimSpace(u)
		if u != "" && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	for _, raw := range urls {
		add(raw)
	}
	altHosts := []string{"wx1.sinaimg.cn", "wx2.sinaimg.cn", "wx3.sinaimg.cn", "wx4.sinaimg.cn"}
	// Only upgrade to the original-size /large/ tier; never downgrade to
	// recompressed /mw2000/, /orj1080/ or /bmiddle/ copies (originals only,
	// user request 2026-11-24).
	knownTiers := []string{"/large/", "/mw2000/", "/orj1080/", "/bmiddle/", "/mw690/", "/mw1024/", "/orj360/", "/orj480/"}
	for _, raw := range urls {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Host == "" {
			continue
		}
		host := strings.ToLower(u.Hostname())
		if host != "sinaimg.cn" && !strings.HasSuffix(host, ".sinaimg.cn") {
			continue
		}
		if host == "wx1.sinaimg.cn" || host == "wx2.sinaimg.cn" || host == "wx3.sinaimg.cn" || host == "wx4.sinaimg.cn" {
			for _, h := range altHosts {
				if h == host {
					continue
				}
				cp := *u
				cp.Host = h
				add(cp.String())
			}
		}
		for _, tier := range knownTiers {
			if strings.HasPrefix(u.Path, tier) {
				if tier != "/large/" {
					cp := *u
					cp.Path = "/large/" + strings.TrimPrefix(u.Path, tier)
					add(cp.String())
				}
				break
			}
		}
	}
	return out
}

func (m *Manager) cacheWeiboLivePhotoClip(
	ctx context.Context,
	t *Task,
	itemDir string,
	previous library.ItemMetadata,
	exists bool,
	relPath string,
	urls []string,
	expectedDuration int,
	fallbackW, fallbackH int,
	transport site.MediaTransportSpec,
	refreshURLs func() ([]string, error),
) (*library.MediaFile, error) {
	// Reuse any existing valid motion clip (either .mp4 or legacy .mov) for this page.
	if exists {
		stem := strings.TrimSuffix(relPath, filepath.Ext(relPath))
		for _, mf := range previous.Media {
			if mf.Role == "motion" && strings.HasPrefix(mf.Path, stem+".") && mediaIsValid(previous, mf.Path) {
				m.setFileProgress(t, "motion", mf.Path, "completed", mf.Bytes, mf.Bytes, 0)
				cp := mf
				return &cp, nil
			}
		}
	}
	return m.cacheWeiboSingleMP4(ctx, t, itemDir, previous, exists, "motion", relPath, urls, expectedDuration, fallbackW, fallbackH, transport, refreshURLs, true)
}

func (m *Manager) cacheWeiboSingleMP4(
	ctx context.Context,
	t *Task,
	itemDir string,
	previous library.ItemMetadata,
	exists bool,
	role string,
	relPath string,
	urls []string,
	expectedDuration int,
	fallbackW, fallbackH int,
	transport site.MediaTransportSpec,
	refreshURLs func() ([]string, error),
	allowQuickTimeRepackage bool,
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
	if _, _, err := m.downloadWithTransportRefresh(ctx, transport, urls, stagingPath, role, t, refreshURLs); err != nil {
		if ctx.Err() == nil && !fileOrPartExists(stagingPath) {
			m.removeTempTaskProgress(t, rawRel)
		}
		m.setFileProgress(t, role, relPath, "failed", 0, 0, 0)
		return nil, err
	}
	m.setFileProgress(t, role, relPath, "verifying", 0, 0, 0)
	var summary MuxTrackSummary
	var vErr error
	if allowQuickTimeRepackage {
		summary, _, vErr = EnsureLivePhotoMP4Context(ctx, stagingPath, float64(expectedDuration))
	} else {
		summary, vErr = EnsureSingleMP4Context(ctx, stagingPath, float64(expectedDuration))
	}
	if vErr != nil {
		if ctx.Err() == nil {
			_ = os.Remove(stagingPath)
			m.removeTempTaskProgress(t, rawRel)
			m.setFileProgress(t, role, relPath, "failed", 0, 0, 0)
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

func resolveWeiboIdentity(reqKind, planItemKey, targetID string) (itemKind, rawID, itemKey, plannerTarget string, err error) {
	reqKind = strings.ToLower(strings.TrimSpace(reqKind))
	planItemKey = strings.TrimSpace(planItemKey)
	targetID = strings.TrimSpace(targetID)

	stripped := library.NormalizeRawID("", targetID)
	lowerKey := strings.ToLower(planItemKey)
	isStandaloneVideo := strings.HasPrefix(lowerKey, "weibo:video:") ||
		(!strings.HasPrefix(lowerKey, "weibo:dynamic:") &&
			(reqKind == "video" ||
				strings.HasPrefix(stripped, "1034:") ||
				strings.HasPrefix(stripped, "1034_")))

	if isStandaloneVideo {
		cand := stripped
		if strings.HasPrefix(lowerKey, "weibo:video:") && cand == "" {
			cand = planItemKey[len("weibo:video:"):]
		}
		rawID = strings.ReplaceAll(cand, ":", "_")
		if err := library.ValidateSafeIDSegment(rawID); err != nil {
			return "", "", "", "", &site.Error{
				Site:    site.Weibo,
				Kind:    site.ErrKindBadRequest,
				Message: "无效的微博视频对象 ID",
			}
		}
		plannerTarget = cand
		if strings.HasPrefix(rawID, "1034_") && !strings.Contains(plannerTarget, ":") {
			plannerTarget = "1034:" + strings.TrimPrefix(rawID, "1034_")
		}
		return "video", rawID, library.CanonicalItemKey(string(site.Weibo), "video", rawID), plannerTarget, nil
	}

	cand := stripped
	if strings.HasPrefix(lowerKey, "weibo:dynamic:") {
		if keyID := strings.TrimSpace(planItemKey[len("weibo:dynamic:"):]); keyID != "" {
			cand = keyID
		}
	}
	if err := library.ValidateSafeIDSegment(cand); err != nil {
		return "", "", "", "", &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindBadRequest,
			Message: "无效的微博 ID",
		}
	}
	return "dynamic", cand, library.CanonicalItemKey(string(site.Weibo), "dynamic", cand), cand, nil
}

func resolveWeiboPlacement(plan WorkDownloadPlan, reqVia *library.ItemVia, rawID string) (forward bool, via *library.ItemVia, folderUID string, err error) {
	forward = plan.IsForward || plan.IsFastRetweet || (reqVia != nil && reqVia.Reason == library.ViaForward)
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
	fwdUID, uErr := site.NormalizeUID(site.Weibo, rawFwdUID)
	if uErr != nil {
		return false, nil, "", &site.Error{
			Site:    site.Weibo,
			Kind:    site.ErrKindSchemaChanged,
			Message: "转发微博缺少有效的转发者 UID",
		}
	}
	return true, &library.ItemVia{
		Reason:    library.ViaForward,
		Site:      string(site.Weibo),
		UID:       fwdUID,
		Name:      fwdName,
		DynamicID: dynID,
	}, fwdUID, nil
}

func buildWeiboItemOrigin(plan WorkDownloadPlan, previous library.ItemMetadata) *library.ItemOrigin {
	hasOriginContent := strings.TrimSpace(plan.OriginAuthorUID) != "" ||
		strings.TrimSpace(plan.OriginAuthorName) != "" ||
		strings.TrimSpace(plan.OriginText) != "" ||
		len(plan.OriginLinks) > 0 ||
		plan.OriginUnavailable ||
		plan.OriginCard != nil ||
		plan.VideoFromOrigin ||
		strings.TrimSpace(plan.OriginMblogID) != "" ||
		strings.TrimSpace(plan.OriginVideoTitle) != "" ||
		plan.OriginPubTs > 0
	if !hasOriginContent {
		if previous.Origin != nil {
			cp := *previous.Origin
			cp.Links = append([]library.ItemLink(nil), cp.Links...)
			if cp.Card != nil {
				cc := *cp.Card
				cp.Card = &cc
			}
			return &cp
		}
		return nil
	}
	originUID := strings.TrimSpace(plan.OriginAuthorUID)
	if norm, err := site.NormalizeUID(site.Weibo, originUID); err == nil {
		originUID = norm
	}
	var prevOriginLinks []library.ItemLink
	if previous.Origin != nil {
		prevOriginLinks = previous.Origin.Links
	}
	return &library.ItemOrigin{
		Site:         string(site.Weibo),
		ID:           strings.TrimSpace(plan.OriginID),
		MblogID:      strings.TrimSpace(plan.OriginMblogID),
		CanonicalURL: sanitizeStoredURL(plan.OriginCanonicalURL),
		Author: library.ItemAuthor{
			Site: string(site.Weibo),
			UID:  originUID,
			Name: strings.TrimSpace(plan.OriginAuthorName),
			Face: sanitizeWeiboTaskURL(plan.OriginAuthorFace),
		},
		PubTs:             plan.OriginPubTs,
		Title:             strings.TrimSpace(plan.OriginVideoTitle),
		Text:              strings.TrimSpace(plan.OriginText),
		Links:             buildWeiboItemLinks(plan.OriginLinks, prevOriginLinks),
		Card:              buildWeiboItemRichCard(plan.OriginCard, nil),
		Unavailable:       plan.OriginUnavailable,
		UnavailableReason: strings.TrimSpace(plan.OriginUnavailableReason),
		VideoFromOrigin:   plan.VideoFromOrigin,
	}
}

func buildWeiboItemLinks(links []site.TextLink, prev []library.ItemLink) []library.ItemLink {
	if len(links) == 0 {
		if len(prev) == 0 {
			return nil
		}
		return append([]library.ItemLink(nil), prev...)
	}
	out := make([]library.ItemLink, 0, len(links))
	for _, l := range links {
		lbl := strings.TrimSpace(l.Label)
		u := strings.TrimSpace(l.URL)
		if lbl == "" || u == "" {
			continue
		}
		out = append(out, library.ItemLink{
			Label: lbl,
			URL:   u,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func buildWeiboItemRichCard(card *site.RichCardSummary, prev *library.ItemRichCard) *library.ItemRichCard {
	if card == nil {
		if prev == nil {
			return nil
		}
		cp := *prev
		return &cp
	}
	out := &library.ItemRichCard{
		Kind:  strings.TrimSpace(card.Kind),
		Label: strings.TrimSpace(card.Label),
		Title: strings.TrimSpace(card.Title),
		Desc:  strings.TrimSpace(card.Desc),
		URL:   strings.TrimSpace(card.URL),
	}
	if out.Kind == "" && out.Label == "" && out.Title == "" && out.Desc == "" && out.URL == "" {
		return nil
	}
	return out
}

func normalizeWeiboMediaTransport(in site.MediaTransportSpec) site.MediaTransportSpec {
	base := site.WeiboMediaTransport(in.UserAgent, in.AllowLocalhost)
	if len(in.AllowedDomains) > 0 {
		base.AllowedDomains = append([]string(nil), in.AllowedDomains...)
	}
	if strings.TrimSpace(in.Referer) != "" {
		base.Referer = in.Referer
	}
	// Weibo CDN redirects must stay within the explicit Weibo CDN allowlist.
	base.PublicRedirects = false
	// Never inherit AllowedInitialHostPaths onto regular images/videos/covers.
	base.AllowedInitialHostPaths = nil
	return base
}

func weiboLivePhotoTransport(in site.MediaTransportSpec) site.MediaTransportSpec {
	spec := normalizeWeiboMediaTransport(in)
	live := site.WeiboLivePhotoMediaTransport(spec.UserAgent, spec.AllowLocalhost)
	spec.AllowedInitialHostPaths = append([]site.ExactInitialURLRule(nil), live.AllowedInitialHostPaths...)
	return spec
}

func weiboFallbackCanonicalURL(itemKind, rawID string) string {
	if itemKind == "video" {
		return "https://weibo.com/tv/show/" + strings.Replace(rawID, "1034_", "1034:", 1)
	}
	return "https://weibo.com/detail/" + rawID
}

func sanitizeWeiboTaskURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	lower := strings.ToLower(trimmed)
	for _, marker := range []string{
		"ssig=", "expires=", "kid=",
		"ssig%3d", "expires%3d", "kid%3d",
		"sub=", "subp=", "xsrf-token=", "wbpsess=",
		"sub%3d", "subp%3d", "xsrf-token%3d", "wbpsess%3d",
	} {
		if strings.Contains(lower, marker) {
			return ""
		}
	}
	clean := sanitizeStoredURL(trimmed)
	if clean == "" {
		return ""
	}
	if u, err := url.Parse(clean); err == nil {
		host := strings.ToLower(u.Hostname())
		if host == "weibocdn.com" || strings.HasSuffix(host, ".weibocdn.com") || host == "video.weibo.com" {
			return ""
		}
	}
	return clean
}
