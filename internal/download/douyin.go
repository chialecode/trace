package download

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	jitterrand "math/rand/v2"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
)

// WorkPlanOptions carries user quality and codec preferences when planning a site download.
type WorkPlanOptions struct {
	VideoQuality string
	CodecOrder   []string
}

// PlannedNotePage describes one 1-based page in a Douyin image or mixed note.
type PlannedNotePage struct {
	Index         int
	Kind          string // "image", "live_photo", "video", "unknown"
	UnknownKind   string
	UnknownReason string
	Width         int
	Height        int
	DurationSec   int
	ImageURLs     []string
	VideoURLs     []string
	Gear          string
	QualityName   string
	Codec         string
	FPS           int
	Bitrate       int64
}

// PlannedMusic describes background music metadata and candidate URLs on a Douyin note.
type PlannedMusic struct {
	ID          string
	Title       string
	Author      string
	DurationSec int
	Original    bool
	CoverURLs   []string
	PlayURLs    []string
}

// WorkDownloadPlan holds rich metadata and ephemeral CDN URLs for caching a Douyin video or note.
type WorkDownloadPlan struct {
	Site           site.ID
	Kind           string // "video" or "dynamic" ("note" / "image_note" normalized to "dynamic")
	TargetID       string
	Title          string
	Desc           string
	CanonicalURL   string
	PubTs          int64
	DurationSec    int
	AuthorUID      string
	AuthorUniqueID string
	AuthorName     string
	AuthorFace     string
	CoverURLs      []string
	VideoURLs      []string
	OriginalURLs   []string // untranscoded source (原画), tried before VideoURLs
	VideoGear      string
	QualityName    string
	Codec          string
	Width          int
	Height         int
	FPS            int
	Bitrate        int64
	AvailableGears []string
	DowngradeHint  string
	Pages          []PlannedNotePage
	Music          *PlannedMusic
	Transport      site.MediaTransportSpec
}

// WorkPlanner plans rich work downloads for a site without exposing site-private packages to download.
type WorkPlanner interface {
	SiteID() site.ID
	PlanWorkDownload(ctx context.Context, targetID string, opts WorkPlanOptions) (WorkDownloadPlan, error)
}

// SiteAuthChecker is an optional interface implemented by planners that enforce login before enqueueing.
type SiteAuthChecker interface {
	RequireAuth(action string) error
}

// SiteErrorReporter is an optional interface implemented by planners that track automatic-request errors.
type SiteErrorReporter interface {
	ReportAutomaticError(ctx context.Context, err error)
}

// DouyinWorkEnqueueRequest specifies a Douyin video or image/mixed note to enqueue for local caching.
type DouyinWorkEnqueueRequest struct {
	AwemeID string            `json:"awemeId"`
	Kind    string            `json:"kind,omitempty"`   // optional hint: "video" or "dynamic"/"note"
	Source  string            `json:"source,omitempty"` // "manual" or "auto"
	Plan    *WorkDownloadPlan `json:"-"`
}

// RegisterSitePlanner registers a cross-site DownloadPlanner (upgrading automatically if it also implements WorkPlanner).
func (m *Manager) RegisterSitePlanner(planner site.DownloadPlanner) {
	if planner == nil {
		return
	}
	if wp, ok := planner.(WorkPlanner); ok {
		m.RegisterWorkPlanner(wp)
		return
	}
	m.RegisterWorkPlanner(&basicSitePlannerAdapter{inner: planner})
}

// RegisterWorkPlanner registers a rich WorkPlanner for its SiteID.
func (m *Manager) RegisterWorkPlanner(planner WorkPlanner) {
	if planner == nil {
		return
	}
	m.mu.Lock()
	m.planners[planner.SiteID()] = planner
	m.mu.Unlock()
}

func (m *Manager) getWorkPlanner(siteID site.ID) (WorkPlanner, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.planners[siteID]
	return p, ok
}

type basicSitePlannerAdapter struct {
	inner site.DownloadPlanner
}

func (a *basicSitePlannerAdapter) SiteID() site.ID {
	return a.inner.SiteID()
}

func (a *basicSitePlannerAdapter) RequireAuth(action string) error {
	if checker, ok := a.inner.(SiteAuthChecker); ok {
		return checker.RequireAuth(action)
	}
	return nil
}

func (a *basicSitePlannerAdapter) ReportAutomaticError(ctx context.Context, err error) {
	if rep, ok := a.inner.(SiteErrorReporter); ok {
		rep.ReportAutomaticError(ctx, err)
	}
}

func (a *basicSitePlannerAdapter) PlanWorkDownload(ctx context.Context, targetID string, _ WorkPlanOptions) (WorkDownloadPlan, error) {
	p, err := a.inner.PlanDownload(ctx, targetID, 1)
	if err != nil {
		return WorkDownloadPlan{}, err
	}
	return convertSiteDownloadPlan(p), nil
}

func convertSiteDownloadPlan(p site.DownloadPlan) WorkDownloadPlan {
	kind := "video"
	if p.Kind == "image_note" || p.Kind == "note" || p.Kind == "dynamic" || len(p.Images) > 0 {
		kind = "dynamic"
	}
	canonicalURL := ""
	if p.Site == site.Douyin && p.TargetID != "" {
		if kind == "dynamic" {
			canonicalURL = "https://www.douyin.com/note/" + p.TargetID
		} else {
			canonicalURL = "https://www.douyin.com/video/" + p.TargetID
		}
	}
	pages := make([]PlannedNotePage, 0, len(p.Images))
	for i, img := range p.Images {
		idx := img.Index
		if idx <= 0 {
			idx = i + 1
		}
		pageKind := "image"
		if len(img.VideoURLs) > 0 {
			pageKind = "live_photo"
		}
		pages = append(pages, PlannedNotePage{
			Index:     idx,
			Kind:      pageKind,
			Width:     img.Width,
			Height:    img.Height,
			ImageURLs: append([]string(nil), img.ImageURLs...),
			VideoURLs: append([]string(nil), img.VideoURLs...),
		})
	}
	var music *PlannedMusic
	if len(p.MusicURLs) > 0 {
		music = &PlannedMusic{
			PlayURLs: append([]string(nil), p.MusicURLs...),
		}
	}
	transport := p.Media
	if len(transport.AllowedDomains) == 0 && p.Site == site.Douyin {
		transport = site.DouyinVODTransport("", false)
	}
	return WorkDownloadPlan{
		Site:         p.Site,
		Kind:         kind,
		TargetID:     p.TargetID,
		Title:        p.Title,
		Desc:         p.Desc,
		CanonicalURL: canonicalURL,
		PubTs:        p.PubTs,
		DurationSec:  p.DurationSec,
		AuthorUID:    p.AuthorUID,
		AuthorName:   p.AuthorName,
		CoverURLs:    append([]string(nil), p.CoverURLs...),
		VideoURLs:    append([]string(nil), p.VideoURLs...),
		OriginalURLs: append([]string(nil), p.OriginalURLs...),
		QualityName:  p.QualityLabel,
		Codec:        p.VideoCodec,
		Width:        p.Width,
		Height:       p.Height,
		Pages:        pages,
		Music:        music,
		Transport:    transport,
	}
}

// EnqueueDouyinWork resolves (or accepts a pre-planned) Douyin video or note and enqueues a download task.
func (m *Manager) EnqueueDouyinWork(ctx context.Context, req DouyinWorkEnqueueRequest) (Task, error) {
	awemeID := strings.TrimSpace(req.AwemeID)
	if awemeID == "" && req.Plan != nil {
		awemeID = strings.TrimSpace(req.Plan.TargetID)
	}
	if awemeID == "" || library.SanitizeFileName(awemeID) != awemeID {
		return Task{}, &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindBadRequest,
			Message: "无效的抖音作品 ID",
		}
	}

	planner, hasPlanner := m.getWorkPlanner(site.Douyin)
	if checker, ok := planner.(SiteAuthChecker); ok {
		if err := checker.RequireAuth("download:douyin"); err != nil {
			return Task{}, err
		}
	}

	var plan WorkDownloadPlan
	if req.Plan != nil {
		plan = *req.Plan
	} else {
		if !hasPlanner {
			return Task{}, &site.Error{
				Site:    site.Douyin,
				Kind:    site.ErrKindUnavailable,
				Message: "抖音下载规划器尚未初始化",
			}
		}
		m.mu.Lock()
		opts := WorkPlanOptions{
			VideoQuality: m.dlSettings.VideoQuality,
			CodecOrder:   append([]string(nil), m.dlSettings.CodecOrder...),
		}
		m.mu.Unlock()
		fetched, err := planner.PlanWorkDownload(ctx, awemeID, opts)
		if err != nil {
			return Task{}, err
		}
		plan = fetched
	}

	plan.Site = site.Douyin
	if plan.TargetID == "" {
		plan.TargetID = awemeID
	}
	kind := normalizeDouyinTaskKind(firstNonEmptyStr(plan.Kind, req.Kind), len(plan.Pages) > 0)
	plan.Kind = kind

	normUID, err := site.NormalizeUID(site.Douyin, plan.AuthorUID)
	if err != nil {
		return Task{}, &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindSchemaChanged,
			Message: "抖音作品缺少有效作者标识",
		}
	}
	plan.AuthorUID = normUID
	if len(plan.Transport.AllowedDomains) == 0 {
		plan.Transport = site.DouyinVODTransport("", false)
	}

	cacheRoot := m.lib.CacheDir()
	itemDir, err := library.EnsureWithinRoot(cacheRoot, filepath.Join(cacheRoot, "douyin", site.DouyinAuthorDir(normUID), "works", plan.TargetID))
	if err != nil {
		return Task{}, err
	}
	itemKey := library.CanonicalItemKey("douyin", kind, plan.TargetID)
	if existing, exists, gErr := m.lib.GetItem(itemKey); gErr == nil && exists && existing.ItemDir != "" {
		itemDir = existing.ItemDir
	}

	source := strings.TrimSpace(req.Source)
	if source == "" {
		source = "manual"
	}
	coverDisplay := ""
	if len(plan.CoverURLs) > 0 {
		coverDisplay = sanitizeStoredURL(plan.CoverURLs[0])
	}

	title := strings.TrimSpace(plan.Title)
	if title == "" {
		title = strings.TrimSpace(plan.Desc)
	}
	if title == "" {
		title = "抖音作品 " + plan.TargetID
	}

	t, err := m.enqueueSingleTask(Task{
		Site:        string(site.Douyin),
		Kind:        kind,
		Source:      source,
		TargetID:    plan.TargetID,
		Page:        1,
		Title:       title,
		AuthorUID:   normUID,
		AuthorName:  plan.AuthorName,
		AuthorFace:  sanitizeStoredURL(plan.AuthorFace),
		CoverURL:    coverDisplay,
		QualityHint: plan.DowngradeHint,
		ItemKey:     itemKey,
		ItemDir:     itemDir,
	})
	if err != nil {
		return Task{}, err
	}

	m.mu.Lock()
	if t.Status == "queued" {
		m.initialPlans[t.TaskID] = plan
	}
	m.mu.Unlock()

	m.notifyWake()
	return t, nil
}

func normalizeDouyinTaskKind(raw string, hasPages bool) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "dynamic", "note", "image_note", "images":
		return "dynamic"
	case "video", "single_mp4":
		if hasPages {
			return "dynamic"
		}
		return "video"
	default:
		if hasPages {
			return "dynamic"
		}
		return "video"
	}
}

func classifyDownloadError(err error) string {
	if err == nil {
		return ""
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return site.ErrKindStorage
	}
	var encErr *EncryptedMediaError
	if errors.As(err, &encErr) {
		return site.ErrKindBlocked
	}
	var mp4Err *SingleMP4ValidationError
	if errors.As(err, &mp4Err) {
		return site.ErrKindSchemaChanged
	}
	return site.KindOf(err)
}

func (m *Manager) reportSiteAutomaticError(ctx context.Context, siteStr string, err error) {
	if err == nil {
		return
	}
	if siteStr == "" || siteStr == string(site.Bilibili) {
		if m.client != nil {
			m.client.ReportAutomaticError(ctx, err)
		}
		return
	}
	if planner, ok := m.getWorkPlanner(site.ID(siteStr)); ok {
		if rep, ok := planner.(SiteErrorReporter); ok {
			rep.ReportAutomaticError(ctx, err)
		}
	}
}

func (m *Manager) consumeInitialOrFetchDouyinPlan(ctx context.Context, taskID, targetID string) (WorkDownloadPlan, WorkPlanner, error) {
	m.mu.Lock()
	initial, hasInitial := m.initialPlans[taskID]
	if hasInitial {
		delete(m.initialPlans, taskID)
	}
	planner := m.planners[site.Douyin]
	opts := WorkPlanOptions{
		VideoQuality: m.dlSettings.VideoQuality,
		CodecOrder:   append([]string(nil), m.dlSettings.CodecOrder...),
	}
	m.mu.Unlock()

	if hasInitial {
		return initial, planner, nil
	}
	if planner == nil {
		return WorkDownloadPlan{}, nil, &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindUnavailable,
			Message: "抖音下载规划器尚未初始化",
		}
	}
	if checker, ok := planner.(SiteAuthChecker); ok {
		if err := checker.RequireAuth("download:douyin"); err != nil {
			return WorkDownloadPlan{}, planner, err
		}
	}
	plan, err := planner.PlanWorkDownload(ctx, targetID, opts)
	if err != nil {
		return WorkDownloadPlan{}, planner, err
	}
	if len(plan.Transport.AllowedDomains) == 0 {
		plan.Transport = site.DouyinVODTransport("", false)
	}
	return plan, planner, nil
}

func (m *Manager) executeDouyinTask(ctx context.Context, t *Task) error {
	unlock := m.lockVideo("douyin:" + t.TargetID)
	defer unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	plan, planner, err := m.consumeInitialOrFetchDouyinPlan(ctx, t.TaskID, t.TargetID)
	if err != nil {
		return err
	}
	normUID, err := site.NormalizeUID(site.Douyin, firstNonEmptyStr(plan.AuthorUID, t.AuthorUID))
	if err != nil {
		return &site.Error{
			Site:    site.Douyin,
			Kind:    site.ErrKindSchemaChanged,
			Message: "抖音作品缺少有效作者标识",
		}
	}
	plan.AuthorUID = normUID
	t.AuthorUID = normUID

	kind := normalizeDouyinTaskKind(firstNonEmptyStr(plan.Kind, t.Kind), len(plan.Pages) > 0)
	t.Kind = kind
	if kind == "dynamic" {
		return m.executeDouyinNoteTask(ctx, t, plan, planner)
	}
	return m.executeDouyinVideoTask(ctx, t, plan, planner)
}

func (m *Manager) resolveDouyinItemDir(t *Task, kind, normUID, awemeID string) (library.ItemMetadata, bool, string, error) {
	itemKey := library.CanonicalItemKey("douyin", kind, awemeID)
	previous, exists, err := m.lib.GetItem(itemKey)
	if err != nil {
		return library.ItemMetadata{}, false, "", err
	}
	cacheRoot := m.lib.CacheDir()
	itemDir, err := library.EnsureWithinRoot(cacheRoot, filepath.Join(cacheRoot, "douyin", site.DouyinAuthorDir(normUID), "works", awemeID))
	if err != nil {
		return library.ItemMetadata{}, false, "", err
	}
	if exists && previous.ItemDir != "" {
		itemDir = previous.ItemDir
	} else if t.ItemDir != "" {
		if checked, cErr := library.EnsureWithinRoot(cacheRoot, t.ItemDir); cErr == nil {
			itemDir = checked
		}
	}
	if err := os.MkdirAll(itemDir, 0o700); err != nil {
		return library.ItemMetadata{}, false, "", err
	}
	t.ItemKey = itemKey
	t.ItemDir = itemDir
	return previous, exists, itemDir, nil
}

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
		if saved, sErr := m.lib.SaveItem(itemDir, meta); sErr == nil {
			t.ItemKey = saved.ItemKey
		}
	}()

	refreshOncePlan := m.makeSinglePlanRefresher(ctx, t.TargetID, planner)

	// 1. Save cover.<ext> when enabled.
	if cfg.SaveCover && len(plan.CoverURLs) > 0 {
		if coverMedia, cErr := m.cacheDouyinSniffedImage(ctx, t, itemDir, previous, exists, "cover", "cover", plan.CoverURLs, 0, 0, plan.Transport, func() ([]string, error) {
			fresh, rErr := refreshOncePlan()
			if rErr != nil {
				return nil, rErr
			}
			return fresh.CoverURLs, nil
		}); cErr != nil {
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
		refreshVideoURLs := m.makePerFileURLRefresher(ctx, t.TargetID, planner, func(fresh WorkDownloadPlan) ([]string, error) {
			if plan.Codec != "" && fresh.Codec != "" && !strings.EqualFold(plan.Codec, fresh.Codec) {
				return nil, &site.Error{Site: site.Douyin, Kind: site.ErrKindUnavailable, Message: "续传地址的视频编码发生变化，已保留断点"}
			}
			if plan.VideoGear != "" && fresh.VideoGear != "" && plan.VideoGear != fresh.VideoGear {
				return nil, &site.Error{Site: site.Douyin, Kind: site.ErrKindUnavailable, Message: "续传地址的清晰度档位发生变化，已保留断点"}
			}
			return fresh.VideoURLs, nil
		})
		rawRel := videoRel + ".raw"
		stagingPath := videoPath + ".raw"
		var summary MuxTrackSummary
		original := false
		if len(plan.OriginalURLs) > 0 {
			origSummary, oErr := m.cacheDouyinOriginal(ctx, t, plan, planner, videoRel, videoPath)
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
			// Any other failure falls back to the best transcoded stream below.
		}
		if !original {
			_, _, dlErr := m.downloadWithTransportRefresh(ctx, plan.Transport, plan.VideoURLs, stagingPath, "video", t, refreshVideoURLs)
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
				_ = os.Remove(stagingPath)
				if ctx.Err() == nil {
					m.removeTempTaskProgress(t, rawRel)
				}
				m.setFileProgress(t, "video", videoRel, "failed", 0, 0, 0)
				return vErr
			}
			if err := ctx.Err(); err != nil {
				_ = os.Remove(stagingPath)
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
	}
	saved, err := m.lib.SaveItem(itemDir, meta)
	if err != nil {
		return err
	}
	t.ItemKey = saved.ItemKey
	finalSaved = true
	return nil
}

func (m *Manager) executeDouyinNoteTask(ctx context.Context, t *Task, plan WorkDownloadPlan, planner WorkPlanner) (result error) {
	previous, exists, itemDir, err := m.resolveDouyinItemDir(t, "dynamic", plan.AuthorUID, t.TargetID)
	if err != nil {
		return err
	}

	m.mu.Lock()
	cfg := m.dlSettings
	m.mu.Unlock()

	title := firstNonEmptyStr(plan.Title, plan.Desc, t.Title, "抖音图文 "+t.TargetID)
	canonicalURL := firstNonEmptyStr(sanitizeStoredURL(plan.CanonicalURL), "https://www.douyin.com/note/"+t.TargetID)
	t.Title = title
	t.AuthorName = firstNonEmptyStr(plan.AuthorName, t.AuthorName)
	t.AuthorFace = sanitizeStoredURL(firstNonEmptyStr(plan.AuthorFace, t.AuthorFace))
	if len(plan.CoverURLs) > 0 {
		t.CoverURL = sanitizeStoredURL(plan.CoverURLs[0])
	}

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
				Item:   "note",
				Reason: "图文缓存尚未完成 (" + classifyDownloadError(result) + ")",
			})
		}
		partialMeta := library.ItemMetadata{
			Version:     1,
			ID:          "dynamic:" + t.TargetID,
			Site:        "douyin",
			Kind:        "dynamic",
			Status:      "partial",
			Title:       title,
			Desc:        plan.Desc,
			Text:        firstNonEmptyStr(plan.Desc, plan.Title),
			DynamicType: "draw",
			TypeLabel:   "图文",
			URL:         canonicalURL,
			PubTs:       plan.PubTs,
			CachedAt:    previous.CachedAt,
			Author: library.ItemAuthor{
				Site:     "douyin",
				UID:      plan.AuthorUID,
				UniqueID: plan.AuthorUniqueID,
				Name:     t.AuthorName,
				Face:     t.AuthorFace,
			},
			Media:     mergeMediaFiles(previous.Media, mediaFiles),
			NotePages: notePages,
			Missing:   missing,
		}
		if saved, sErr := m.lib.SaveItem(itemDir, partialMeta); sErr == nil {
			t.ItemKey = saved.ItemKey
		}
	}()

	// Optional cover.<ext> when explicitly provided.
	if cfg.SaveCover && len(plan.CoverURLs) > 0 {
		refreshCover := m.makePerFileURLRefresher(ctx, t.TargetID, planner, func(fresh WorkDownloadPlan) ([]string, error) {
			return fresh.CoverURLs, nil
		})
		if coverMedia, cErr := m.cacheDouyinSniffedImage(ctx, t, itemDir, previous, exists, "cover", "cover", plan.CoverURLs, 0, 0, plan.Transport, refreshCover); cErr == nil && coverMedia != nil {
			mediaFiles = append(mediaFiles, *coverMedia)
		}
	}

	// Process ordered pages 1..N.
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
		case "image":
			refreshImg := m.makePerFileURLRefresher(ctx, t.TargetID, planner, func(fresh WorkDownloadPlan) ([]string, error) {
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
				reason := fmt.Sprintf("第 %02d 页图片下载失败 (%s)", pageIdx, classifyDownloadError(imgErr))
				missing = append(missing, library.MissingRecord{Item: fmt.Sprintf("page_%02d", pageIdx), Reason: reason})
				partialErrors = append(partialErrors, reason)
				notePages = append(notePages, library.NotePageRecord{Index: pageIdx, Kind: "image", Width: page.Width, Height: page.Height})
				continue
			}
			mediaFiles = append(mediaFiles, *imgMedia)
			notePages = append(notePages, library.NotePageRecord{
				Index:  pageIdx,
				Kind:   "image",
				Image:  imgMedia.Path,
				Width:  page.Width,
				Height: page.Height,
			})

		case "live_photo", "motion":
			refreshImg := m.makePerFileURLRefresher(ctx, t.TargetID, planner, func(fresh WorkDownloadPlan) ([]string, error) {
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
				reason := fmt.Sprintf("第 %02d 页实况静帧下载失败 (%s)", pageIdx, classifyDownloadError(imgErr))
				missing = append(missing, library.MissingRecord{Item: fmt.Sprintf("page_%02d", pageIdx), Reason: reason})
				partialErrors = append(partialErrors, reason)
				notePages = append(notePages, library.NotePageRecord{Index: pageIdx, Kind: "motion", Width: page.Width, Height: page.Height, Duration: page.DurationSec})
				continue
			}

			motionRel := fmt.Sprintf("images/%02d_live.mp4", pageIdx)
			refreshMotion := m.makePerFileURLRefresher(ctx, t.TargetID, planner, func(fresh WorkDownloadPlan) ([]string, error) {
				if p, ok := findPlannedPage(fresh.Pages, pageIdx, i); ok {
					return p.VideoURLs, nil
				}
				return nil, nil
			})
			motionMedia, mErr := m.cacheDouyinSingleMP4(ctx, t, itemDir, previous, exists, "motion", motionRel, page.VideoURLs, page.DurationSec, page.Width, page.Height, plan.Transport, refreshMotion)
			rec := library.NotePageRecord{
				Index:    pageIdx,
				Kind:     "motion",
				Image:    imgMedia.Path,
				Width:    page.Width,
				Height:   page.Height,
				Duration: page.DurationSec,
			}
			if mErr != nil {
				if isSiteStopError(mErr) || isStorageError(mErr) || ctx.Err() != nil {
					return mErr
				}
				reason := fmt.Sprintf("第 %02d 页实况视频下载失败 (%s)", pageIdx, classifyDownloadError(mErr))
				missing = append(missing, library.MissingRecord{Item: motionRel, Reason: reason})
				partialErrors = append(partialErrors, reason)
			} else if motionMedia != nil {
				imgMedia.Motion = motionMedia.Path
				rec.Motion = motionMedia.Path
				if motionMedia.Duration > 0 {
					rec.Duration = motionMedia.Duration
				}
				mediaFiles = append(mediaFiles, *motionMedia)
			}
			mediaFiles = append(mediaFiles, *imgMedia)
			notePages = append(notePages, rec)

		case "video":
			var coverRel string
			if len(page.ImageURLs) > 0 {
				refreshPageCover := m.makePerFileURLRefresher(ctx, t.TargetID, planner, func(fresh WorkDownloadPlan) ([]string, error) {
					if p, ok := findPlannedPage(fresh.Pages, pageIdx, i); ok {
						return p.ImageURLs, nil
					}
					return nil, nil
				})
				coverMedia, cErr := m.cacheDouyinSniffedImage(ctx, t, itemDir, previous, exists, "image", prefix, page.ImageURLs, page.Width, page.Height, plan.Transport, refreshPageCover)
				if cErr != nil {
					if isSiteStopError(cErr) || isStorageError(cErr) || ctx.Err() != nil {
						return cErr
					}
					reason := fmt.Sprintf("第 %02d 页视频封面下载失败 (%s)", pageIdx, classifyDownloadError(cErr))
					missing = append(missing, library.MissingRecord{Item: fmt.Sprintf("page_%02d.cover", pageIdx), Reason: reason})
					partialErrors = append(partialErrors, reason)
				} else if coverMedia != nil {
					coverRel = coverMedia.Path
					mediaFiles = append(mediaFiles, *coverMedia)
				}
			}

			videoRel := fmt.Sprintf("images/%02d.mp4", pageIdx)
			refreshPageVideo := m.makePerFileURLRefresher(ctx, t.TargetID, planner, func(fresh WorkDownloadPlan) ([]string, error) {
				if p, ok := findPlannedPage(fresh.Pages, pageIdx, i); ok {
					return p.VideoURLs, nil
				}
				return nil, nil
			})
			vidMedia, vErr := m.cacheDouyinSingleMP4(ctx, t, itemDir, previous, exists, "video", videoRel, page.VideoURLs, page.DurationSec, page.Width, page.Height, plan.Transport, refreshPageVideo)
			rec := library.NotePageRecord{
				Index:    pageIdx,
				Kind:     "video",
				Cover:    coverRel,
				Width:    page.Width,
				Height:   page.Height,
				Duration: page.DurationSec,
			}
			if vErr != nil {
				if isSiteStopError(vErr) || isStorageError(vErr) || ctx.Err() != nil {
					return vErr
				}
				reason := fmt.Sprintf("第 %02d 页视频下载失败 (%s)", pageIdx, classifyDownloadError(vErr))
				missing = append(missing, library.MissingRecord{Item: videoRel, Reason: reason})
				partialErrors = append(partialErrors, reason)
			} else if vidMedia != nil {
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
			// Unrecognized page kind ("unknown"): keep available image, record unknownKind + missing, never fail whole item.
			var savedImg string
			if len(page.ImageURLs) > 0 {
				refreshUnknownImg := m.makePerFileURLRefresher(ctx, t.TargetID, planner, func(fresh WorkDownloadPlan) ([]string, error) {
					if p, ok := findPlannedPage(fresh.Pages, pageIdx, i); ok {
						return p.ImageURLs, nil
					}
					return nil, nil
				})
				if imgMedia, imgErr := m.cacheDouyinSniffedImage(ctx, t, itemDir, previous, exists, "image", prefix, page.ImageURLs, page.Width, page.Height, plan.Transport, refreshUnknownImg); imgErr != nil {
					if isSiteStopError(imgErr) || isStorageError(imgErr) || ctx.Err() != nil {
						return imgErr
					}
				} else if imgMedia != nil {
					savedImg = imgMedia.Path
					mediaFiles = append(mediaFiles, *imgMedia)
				}
			}
			unkTag := firstNonEmptyStr(page.UnknownKind, pageKind, "unknown")
			reason := firstNonEmptyStr(page.UnknownReason, fmt.Sprintf("第 %02d 页包含暂未识别的页面类型 (%s)，已保留可用图片", pageIdx, unkTag))
			missing = append(missing, library.MissingRecord{
				Item:   fmt.Sprintf("page_%02d", pageIdx),
				Reason: reason,
			})
			notePages = append(notePages, library.NotePageRecord{
				Index:       pageIdx,
				Kind:        "unknown",
				UnknownKind: unkTag,
				Image:       savedImg,
				Width:       page.Width,
				Height:      page.Height,
			})
		}
	}

	// Process background music if present on the note.
	var musicInfo *library.MusicInfo
	if plan.Music != nil {
		musicCover := ""
		if len(plan.Music.CoverURLs) > 0 {
			musicCover = sanitizeStoredURL(plan.Music.CoverURLs[0])
		}
		musicInfo = &library.MusicInfo{
			ID:       plan.Music.ID,
			Title:    plan.Music.Title,
			Author:   plan.Music.Author,
			Duration: plan.Music.DurationSec,
			Cover:    musicCover,
			Original: plan.Music.Original,
		}
		if len(plan.Music.PlayURLs) == 0 {
			missing = append(missing, library.MissingRecord{
				Item:   "music",
				Reason: "配乐无可用地址（版权或地区限制）",
			})
		} else {
			refreshMusic := m.makePerFileURLRefresher(ctx, t.TargetID, planner, func(fresh WorkDownloadPlan) ([]string, error) {
				if fresh.Music != nil {
					return fresh.Music.PlayURLs, nil
				}
				return nil, nil
			})
			musicMedia, mErr := m.cacheDouyinSniffedAudio(ctx, t, itemDir, previous, exists, "music", plan.Music.PlayURLs, plan.Music.DurationSec, plan.Transport, refreshMusic)
			if mErr != nil {
				if isSiteStopError(mErr) || isStorageError(mErr) || ctx.Err() != nil {
					return mErr
				}
				reason := fmt.Sprintf("配乐下载失败 (%s)", classifyDownloadError(mErr))
				missing = append(missing, library.MissingRecord{
					Item:   "music",
					Reason: reason,
				})
				partialErrors = append(partialErrors, reason)
			} else if musicMedia != nil {
				musicInfo.File = musicMedia.Path
				mediaFiles = append(mediaFiles, *musicMedia)
			}
		}
	}

	status := "complete"
	if len(missing) > 0 || len(partialErrors) > 0 {
		status = "partial"
		t.Status = "partial"
	}

	meta := library.ItemMetadata{
		Version:     1,
		ID:          "dynamic:" + t.TargetID,
		Site:        "douyin",
		Kind:        "dynamic",
		Status:      status,
		Title:       title,
		Desc:        plan.Desc,
		Text:        firstNonEmptyStr(plan.Desc, plan.Title),
		DynamicType: "draw",
		TypeLabel:   "图文",
		URL:         canonicalURL,
		PubTs:       plan.PubTs,
		CachedAt:    previous.CachedAt,
		Author: library.ItemAuthor{
			Site:     "douyin",
			UID:      plan.AuthorUID,
			UniqueID: plan.AuthorUniqueID,
			Name:     t.AuthorName,
			Face:     t.AuthorFace,
		},
		Media:        mediaFiles,
		NotePages:    notePages,
		Music:        musicInfo,
		Missing:      missing,
		DirectCached: true,
	}

	saved, saveErr := m.lib.SaveItem(itemDir, meta)
	if saveErr != nil {
		return saveErr
	}
	t.ItemKey = saved.ItemKey
	finalSaved = true
	return nil
}

func findPlannedPage(pages []PlannedNotePage, index, fallbackPos int) (PlannedNotePage, bool) {
	for _, p := range pages {
		if p.Index == index {
			return p, true
		}
	}
	if fallbackPos >= 0 && fallbackPos < len(pages) {
		return pages[fallbackPos], true
	}
	return PlannedNotePage{}, false
}

func replaceRoleMedia(existing []library.MediaFile, role string, added library.MediaFile) []library.MediaFile {
	var filtered []library.MediaFile
	for _, mf := range existing {
		if mf.Role != role {
			filtered = append(filtered, mf)
		}
	}
	return append(filtered, added)
}

func (m *Manager) makeSinglePlanRefresher(ctx context.Context, targetID string, planner WorkPlanner) func() (WorkDownloadPlan, error) {
	var called bool
	var cached WorkDownloadPlan
	var cachedErr error
	return func() (WorkDownloadPlan, error) {
		if called {
			return cached, cachedErr
		}
		called = true
		if planner == nil {
			cachedErr = &site.Error{Site: site.Douyin, Kind: site.ErrKindUnavailable, Message: "无法刷新过期的媒体地址"}
			return WorkDownloadPlan{}, cachedErr
		}
		m.mu.Lock()
		opts := WorkPlanOptions{
			VideoQuality: m.dlSettings.VideoQuality,
			CodecOrder:   append([]string(nil), m.dlSettings.CodecOrder...),
		}
		m.mu.Unlock()
		cached, cachedErr = planner.PlanWorkDownload(ctx, targetID, opts)
		return cached, cachedErr
	}
}

// makePerFileURLRefresher ensures each file re-fetches work detail at most once on 403/404/410.
func (m *Manager) makePerFileURLRefresher(
	ctx context.Context,
	targetID string,
	planner WorkPlanner,
	extract func(WorkDownloadPlan) ([]string, error),
) func() ([]string, error) {
	refreshPlan := m.makeSinglePlanRefresher(ctx, targetID, planner)
	var used bool
	return func() ([]string, error) {
		if used {
			return nil, nil
		}
		used = true
		fresh, err := refreshPlan()
		if err != nil {
			return nil, err
		}
		return extract(fresh)
	}
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
	expectedDuration int,
	fallbackW, fallbackH int,
	transport site.MediaTransportSpec,
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
	rawRel := relPath + ".raw"
	stagingPath := fullPath + ".raw"
	if _, _, err := m.downloadWithTransportRefresh(ctx, transport, urls, stagingPath, role, t, refreshURLs); err != nil {
		if ctx.Err() == nil && !fileOrPartExists(stagingPath) {
			m.removeTempTaskProgress(t, rawRel)
		}
		m.setFileProgress(t, role, relPath, "failed", 0, 0, 0)
		return nil, err
	}
	m.setFileProgress(t, role, relPath, "verifying", 0, 0, 0)
	summary, err := EnsureSingleMP4Context(ctx, stagingPath, float64(expectedDuration))
	if err != nil {
		_ = os.Remove(stagingPath)
		if ctx.Err() == nil {
			m.removeTempTaskProgress(t, rawRel)
		}
		m.setFileProgress(t, role, relPath, "failed", 0, 0, 0)
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = os.Remove(stagingPath)
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

func (m *Manager) cacheDouyinSniffedImage(
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
			if mf.Role == role && strings.HasPrefix(mf.Path, relStem+".") && mediaIsValid(previous, mf.Path) {
				m.setFileProgress(t, role, mf.Path, "completed", mf.Bytes, mf.Bytes, 0)
				cp := mf
				return &cp, nil
			}
		}
	}

	tempRel := relStem + ".imgtmp"
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
	n, sha, ct, err := m.downloadWithTransportRefreshCT(ctx, transport, urls, tempFull, role, t, refreshURLs)
	if err != nil {
		_ = os.Remove(tempFull)
		if ctx.Err() == nil && !fileOrPartExists(tempFull) {
			m.removeTempTaskProgress(t, tempRel)
		}
		return nil, err
	}
	ext, err := sniffImageExtension(tempFull, ct)
	if err != nil {
		_ = os.Remove(tempFull)
		if ctx.Err() == nil {
			m.removeTempTaskProgress(t, tempRel)
		}
		return nil, &site.Error{Site: transport.Site, Kind: site.ErrKindSchemaChanged, Message: err.Error()}
	}
	finalRel := relStem + ext
	finalFull, err := library.ResolveItemPath(itemDir, finalRel)
	if err != nil {
		_ = os.Remove(tempFull)
		if ctx.Err() == nil {
			m.removeTempTaskProgress(t, tempRel)
		}
		return nil, err
	}
	if err := m.claimTaskFile(t, itemDir, finalRel, finalFull); err != nil {
		_ = os.Remove(tempFull)
		if ctx.Err() == nil {
			m.removeTempTaskProgress(t, tempRel)
		}
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = os.Remove(tempFull)
		return nil, err
	}
	if err := os.Rename(tempFull, finalFull); err != nil {
		_ = os.Remove(tempFull)
		if ctx.Err() == nil {
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

func (m *Manager) cacheDouyinSniffedAudio(
	ctx context.Context,
	t *Task,
	itemDir string,
	previous library.ItemMetadata,
	exists bool,
	relStem string,
	urls []string,
	durationSec int,
	transport site.MediaTransportSpec,
	refreshURLs func() ([]string, error),
) (*library.MediaFile, error) {
	if exists {
		for _, mf := range previous.Media {
			if mf.Role == "audio" && strings.HasPrefix(mf.Path, relStem+".") && mediaIsValid(previous, mf.Path) {
				m.setFileProgress(t, "audio", mf.Path, "completed", mf.Bytes, mf.Bytes, 0)
				cp := mf
				return &cp, nil
			}
		}
	}

	tempRel := relStem + ".audtmp"
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
	n, sha, ct, err := m.downloadWithTransportRefreshCT(ctx, transport, urls, tempFull, "audio", t, refreshURLs)
	if err != nil {
		_ = os.Remove(tempFull)
		if ctx.Err() == nil && !fileOrPartExists(tempFull) {
			m.removeTempTaskProgress(t, tempRel)
		}
		return nil, err
	}
	ext, err := sniffAudioExtension(tempFull, ct)
	if err != nil {
		_ = os.Remove(tempFull)
		if ctx.Err() == nil {
			m.removeTempTaskProgress(t, tempRel)
		}
		return nil, &site.Error{Site: transport.Site, Kind: site.ErrKindSchemaChanged, Message: err.Error()}
	}
	finalRel := relStem + ext
	finalFull, err := library.ResolveItemPath(itemDir, finalRel)
	if err != nil {
		_ = os.Remove(tempFull)
		if ctx.Err() == nil {
			m.removeTempTaskProgress(t, tempRel)
		}
		return nil, err
	}
	if err := m.claimTaskFile(t, itemDir, finalRel, finalFull); err != nil {
		_ = os.Remove(tempFull)
		if ctx.Err() == nil {
			m.removeTempTaskProgress(t, tempRel)
		}
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = os.Remove(tempFull)
		return nil, err
	}
	if err := os.Rename(tempFull, finalFull); err != nil {
		_ = os.Remove(tempFull)
		if ctx.Err() == nil {
			m.removeTempTaskProgress(t, tempRel)
		}
		return nil, err
	}
	m.removeTempTaskProgress(t, tempRel)
	m.setFileProgress(t, "audio", finalRel, "completed", n, n, 0)
	return &library.MediaFile{
		Role:     "audio",
		Path:     finalRel,
		Bytes:    n,
		SHA256:   sha,
		Duration: durationSec,
	}, nil
}

func fileOrPartExists(path string) bool {
	if _, err := os.Stat(path); err == nil {
		return true
	}
	if _, err := os.Stat(path + ".part"); err == nil {
		return true
	}
	return false
}

const (
	douyinOriginalGear    = "original"
	douyinOriginalQuality = "原画"
)

// cacheDouyinOriginal downloads the untranscoded source (原画) into its own staging
// file and validates it as a single progressive MP4, then moves it to videoPath.
// On a failure the caller falls back to the transcoded stream, so the staging file
// is removed unless the task was cancelled or paused (kept for resume).
func (m *Manager) cacheDouyinOriginal(ctx context.Context, t *Task, plan WorkDownloadPlan, planner WorkPlanner, videoRel, videoPath string) (MuxTrackSummary, error) {
	origRel := videoRel + ".orig.raw"
	origPath := videoPath + ".orig.raw"
	discard := func() {
		if ctx.Err() == nil {
			_ = os.Remove(origPath)
			_ = os.Remove(origPath + ".part")
			m.removeTempTaskProgress(t, origRel)
		}
	}
	refresh := m.makePerFileURLRefresher(ctx, t.TargetID, planner, func(fresh WorkDownloadPlan) ([]string, error) {
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

func trimStagingSuffixes(p string) string {
	for _, suf := range []string{".part", ".raw", ".imgtmp", ".audtmp"} {
		p = strings.TrimSuffix(p, suf)
	}
	return p
}

func (m *Manager) removeTempTaskProgress(t *Task, tempRel string) {
	filtered := t.Files[:0]
	for _, f := range t.Files {
		if taskFileIdentity(f.Path) != taskFileIdentity(tempRel) {
			filtered = append(filtered, f)
		}
	}
	t.Files = filtered
}

func (m *Manager) downloadWithTransportRefresh(
	ctx context.Context,
	transport site.MediaTransportSpec,
	urls []string,
	finalPath string,
	role string,
	t *Task,
	refreshURLs func() ([]string, error),
) (int64, string, error) {
	n, sha, _, err := m.downloadWithTransportRefreshCT(ctx, transport, urls, finalPath, role, t, refreshURLs)
	return n, sha, err
}

func (m *Manager) downloadWithTransportRefreshCT(
	ctx context.Context,
	transport site.MediaTransportSpec,
	urls []string,
	finalPath string,
	role string,
	t *Task,
	refreshURLs func() ([]string, error),
) (int64, string, string, error) {
	n, sha, ct, err := m.downloadWithTransportRange(ctx, transport, urls, finalPath, role, t)
	if err == nil {
		return n, sha, ct, nil
	}
	if refreshURLs != nil && (site.IsKind(err, site.ErrKindForbidden) || site.IsKind(err, site.ErrKindUnavailable)) {
		freshURLs, rErr := refreshURLs()
		if rErr != nil {
			if site.IsKind(rErr, site.ErrKindRiskControl) || site.IsKind(rErr, site.ErrKindRateLimited) || site.IsKind(rErr, site.ErrKindAuthRequired) {
				return 0, "", "", rErr
			}
			return 0, "", "", err
		}
		if len(freshURLs) > 0 {
			return m.downloadWithTransportRange(ctx, transport, freshURLs, finalPath, role, t)
		}
	}
	return 0, "", "", err
}

func (m *Manager) downloadWithTransportRange(
	ctx context.Context,
	transport site.MediaTransportSpec,
	urls []string,
	finalPath string,
	role string,
	t *Task,
) (int64, string, string, error) {
	var cleanURLs []string
	var lastValidateErr error
	for _, u := range urls {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if vErr := transport.ValidateURL(u); vErr != nil {
			lastValidateErr = vErr
			continue
		}
		cleanURLs = append(cleanURLs, u)
	}
	if len(cleanURLs) == 0 {
		if lastValidateErr != nil {
			return 0, "", "", lastValidateErr
		}
		return 0, "", "", &site.Error{
			Site:    transport.Site,
			Kind:    site.ErrKindUnavailable,
			Message: "下载地址列表为空",
		}
	}

	root, rel := filepath.Dir(finalPath), filepath.Base(finalPath)
	if t.ItemDir != "" {
		root = t.ItemDir
		r, err := filepath.Rel(root, finalPath)
		if err != nil {
			return 0, "", "", err
		}
		rel = filepath.ToSlash(r)
	}
	if _, err := library.ResolveItemPath(root, rel); err != nil {
		return 0, "", "", err
	}
	if _, err := library.ResolveItemPath(root, rel+".part"); err != nil {
		return 0, "", "", err
	}
	if err := m.claimTaskFile(t, root, rel, finalPath); err != nil {
		return 0, "", "", err
	}
	if err := ctx.Err(); err != nil {
		return 0, "", "", err
	}
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o700); err != nil {
		return 0, "", "", err
	}

	m.mu.Lock()
	chunkSize := m.chunkSize
	if chunkSize <= 0 {
		chunkSize = DefaultRangeChunkSize
	}
	backoffScale := m.backoffScale
	if backoffScale <= 0 {
		backoffScale = time.Second
	}
	mediaWrap := m.mediaWrap
	m.mu.Unlock()

	httpClient := newTransportHTTPClient(transport)
	if mediaWrap != nil {
		httpClient.Transport = mediaWrap(httpClient.Transport)
	}
	partPath := finalPath + ".part"
	relFile := taskRelativePath(t, trimStagingSuffixes(finalPath))

	var offset int64
	if info, err := os.Stat(partPath); err == nil && !info.IsDir() {
		offset = info.Size()
	}

	var totalSize int64 = -1
	var contentType string
	urlIdx := 0
	segRetries := 0
	fullResets := 0
	var lastErr error

	for {
		if ctx.Err() != nil {
			return offset, "", "", ctx.Err()
		}
		if totalSize > 0 && offset == totalSize {
			break
		}

		targetURL := cleanURLs[urlIdx%len(cleanURLs)]
		endByte := offset + chunkSize - 1
		if totalSize > 0 && endByte >= totalSize {
			endByte = totalSize - 1
		}

		res, ct, err := m.fetchTransportSegment(ctx, httpClient, transport, targetURL, partPath, offset, endByte, totalSize, role, t)
		if err == nil {
			if ct != "" {
				contentType = ct
			}
			offset = res.newOffset
			if res.totalSize > 0 {
				totalSize = res.totalSize
			}
			segRetries = 0
			if res.done || (totalSize > 0 && offset >= totalSize) {
				break
			}
			continue
		}

		lastErr = err
		errKind := classifyDownloadError(err)
		httpStatus := 0
		var se *site.Error
		if errors.As(err, &se) {
			httpStatus = se.HTTPStatus
		}

		if errKind == site.ErrKindRiskControl || errKind == site.ErrKindRateLimited || errKind == site.ErrKindAuthRequired || errKind == site.ErrKindBlocked || errKind == site.ErrKindSchemaChanged || isStorageError(err) {
			m.recordAttempt(t, relFile, offset, targetURL, segRetries+1, errKind, httpStatus)
			return offset, "", "", err
		}

		if res.truncatedToZero {
			offset = 0
			totalSize = -1
			fullResets++
			urlIdx++
			m.recordAttempt(t, relFile, offset, targetURL, fullResets, site.ErrKindNetwork, http.StatusOK)
			if fullResets >= 5 {
				t.Status = "paused"
				t.PauseReason = "network"
				t.ErrorKind = site.ErrKindNetwork
				t.ErrorMessage = "已暂停（网络）：节点不支持分段续传且重试已达上限"
				return offset, "", "", lastErr
			}
			continue
		}

		if errKind == site.ErrKindForbidden || errKind == site.ErrKindUnavailable {
			segRetries++
			urlIdx++
			m.recordAttempt(t, relFile, offset, targetURL, segRetries, errKind, httpStatus)
			if segRetries >= len(cleanURLs) {
				return offset, "", "", lastErr
			}
			continue
		}

		offset = res.newOffset
		if res.totalSize > 0 {
			totalSize = res.totalSize
		}
		segRetries++
		urlIdx++
		m.recordAttempt(t, relFile, offset, targetURL, segRetries, errKind, httpStatus)
		if segRetries >= 5 {
			t.Status = "paused"
			t.PauseReason = "network"
			t.ErrorKind = site.ErrKindNetwork
			t.ErrorMessage = "已暂停（网络）：分段重试已达上限"
			return offset, "", "", lastErr
		}

		backoffUnits := int64(1 << (segRetries - 1))
		if backoffUnits > 16 {
			backoffUnits = 16
		}
		sleepDur := time.Duration(backoffUnits) * backoffScale
		sleepDur = time.Duration(float64(sleepDur) * (0.8 + 0.4*jitterrand.Float64()))
		select {
		case <-ctx.Done():
			return offset, "", "", ctx.Err()
		case <-time.After(sleepDur):
		}
	}

	shaHex, actualSize, err := hashFileSHA256(partPath)
	if err != nil {
		return offset, "", "", err
	}
	if totalSize > 0 && actualSize != totalSize {
		return actualSize, "", "", &site.Error{
			Site:    transport.Site,
			Kind:    site.ErrKindNetwork,
			Message: fmt.Sprintf("文件大小校验不一致: 实际 %d != 预期 %d", actualSize, totalSize),
		}
	}
	if err := os.Rename(partPath, finalPath); err != nil {
		return actualSize, "", "", err
	}
	return actualSize, shaHex, contentType, nil
}

func (m *Manager) fetchTransportSegment(
	ctx context.Context,
	httpClient *http.Client,
	transport site.MediaTransportSpec,
	targetURL string,
	partPath string,
	offset int64,
	endByte int64,
	knownTotal int64,
	role string,
	t *Task,
) (segmentResult, string, error) {
	if err := transport.ValidateURL(targetURL); err != nil {
		return segmentResult{newOffset: offset}, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return segmentResult{newOffset: offset}, "", err
	}
	transport.ApplyHeaders(req)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, endByte))

	resp, err := httpClient.Do(req)
	if err != nil {
		var se *site.Error
		if errors.As(err, &se) {
			return segmentResult{newOffset: offset}, "", se
		}
		return segmentResult{newOffset: offset}, "", &site.Error{
			Site:    transport.Site,
			Kind:    site.ErrKindNetwork,
			Message: "媒体下载连接失败",
			Err:     err,
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && offset > 0 {
		_ = os.Truncate(partPath, 0)
		return segmentResult{newOffset: 0, truncatedToZero: true}, "", &site.Error{
			Site:       transport.Site,
			Kind:       site.ErrKindNetwork,
			HTTPStatus: resp.StatusCode,
			Message:    "Range 416，重置分段文件",
		}
	}
	if resp.StatusCode >= 400 {
		mapped := mapMediaHTTPStatus(resp.StatusCode, "")
		mapped.Site = transport.Site
		return segmentResult{newOffset: offset}, "", mapped
	}

	rawCT := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if err := rejectDisguisedMediaContentType(transport.Site, rawCT, role, resp.StatusCode); err != nil {
		return segmentResult{newOffset: offset}, rawCT, err
	}

	isImage := role == "image" || role == "cover"
	var f *os.File
	var expectedSegLen int64 = -1
	totalSize := knownTotal
	doneAfterThis := false

	switch resp.StatusCode {
	case http.StatusPartialContent:
		cr := strings.TrimSpace(resp.Header.Get("Content-Range"))
		if cr == "" {
			return segmentResult{newOffset: offset}, rawCT, &site.Error{Site: transport.Site, Kind: site.ErrKindNetwork, Message: "206 响应缺少 Content-Range"}
		}
		start, end, total, ok := parseContentRange(cr)
		if !ok || start != offset || end < start || end > endByte || total <= end || (knownTotal > 0 && total != knownTotal) {
			return segmentResult{newOffset: offset}, rawCT, &site.Error{
				Site:    transport.Site,
				Kind:    site.ErrKindNetwork,
				Message: fmt.Sprintf("Content-Range 校验失败: %q", cr),
			}
		}
		if total > 0 {
			totalSize = total
		}
		expectedSegLen = end - start + 1
		if resp.ContentLength >= 0 && resp.ContentLength != expectedSegLen {
			return segmentResult{newOffset: offset}, rawCT, &site.Error{Site: transport.Site, Kind: site.ErrKindNetwork, Message: "分段 Content-Length 与区间长度不一致"}
		}
		if totalSize > 0 && end+1 >= totalSize {
			doneAfterThis = true
		}
		f, err = os.OpenFile(partPath, os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			_, err = f.Seek(offset, io.SeekStart)
		}

	case http.StatusOK:
		if offset > 0 {
			_ = os.Remove(partPath)
			return segmentResult{newOffset: 0, truncatedToZero: true}, rawCT, &site.Error{
				Site:       transport.Site,
				Kind:       site.ErrKindNetwork,
				HTTPStatus: http.StatusOK,
				Message:    "节点未返回 206 Partial Content，已截断从头重试",
			}
		}
		if resp.ContentLength <= 0 {
			return segmentResult{newOffset: offset}, rawCT, &site.Error{Site: transport.Site, Kind: site.ErrKindNetwork, Message: "完整响应缺少有效的 Content-Length"}
		}
		expectedSegLen = resp.ContentLength
		totalSize = resp.ContentLength
		doneAfterThis = true
		f, err = os.OpenFile(partPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)

	default:
		mapped := mapMediaHTTPStatus(resp.StatusCode, "")
		mapped.Site = transport.Site
		return segmentResult{newOffset: offset}, rawCT, mapped
	}

	if isImage && totalSize > MaxImageFileBytes {
		if f != nil {
			_ = f.Close()
		}
		return segmentResult{newOffset: offset}, rawCT, &site.Error{Site: transport.Site, Kind: site.ErrKindSchemaChanged, Message: "图片超过 50 MiB"}
	}
	if err != nil {
		return segmentResult{newOffset: offset}, rawCT, err
	}

	idleBody := newMediaIdleReader(resp.Body, 20*time.Second)
	defer idleBody.Close()
	reader := io.LimitReader(idleBody, expectedSegLen+1)

	buf := make([]byte, 64*1024)
	var segWritten int64
	written := offset
	checkedHead := offset > 0
	if totalSize > 0 {
		t.TotalBytes = totalSize
	} else if resp.ContentLength > 0 {
		t.TotalBytes = offset + resp.ContentLength
	}

	relFile := taskRelativePath(t, strings.TrimSuffix(strings.TrimSuffix(partPath, ".part"), ".raw"))
	progressAt := time.Now()
	progressBytes := offset

	for {
		nr, rErr := reader.Read(buf)
		if nr > 0 {
			if !checkedHead {
				checkedHead = true
				if looksLikeDisguisedTextBody(buf[:nr]) {
					_ = f.Close()
					_ = os.Remove(partPath)
					return segmentResult{newOffset: 0}, rawCT, &site.Error{
						Site:       transport.Site,
						Kind:       site.ErrKindSchemaChanged,
						HTTPStatus: resp.StatusCode,
						Message:    "媒体响应正文为 HTML 或 JSON 文本，已拒绝写入",
					}
				}
			}
			if segWritten+int64(nr) > expectedSegLen {
				_ = f.Close()
				_ = os.Truncate(partPath, offset)
				return segmentResult{newOffset: offset, totalSize: totalSize}, rawCT, &site.Error{Site: transport.Site, Kind: site.ErrKindNetwork, Message: "分段正文超出声明区间"}
			}
			nw, wErr := f.Write(buf[:nr])
			if wErr != nil {
				_ = f.Close()
				return segmentResult{newOffset: written, totalSize: totalSize}, rawCT, wErr
			}
			segWritten += int64(nw)
			written += int64(nw)
			t.BytesDownloaded = written
			if t.TotalBytes > 0 {
				t.Progress = float64(written) / float64(t.TotalBytes)
				if t.Progress > 0.99 {
					t.Progress = 0.99
				}
			}
		}
		if elapsed := time.Since(progressAt); elapsed >= 250*time.Millisecond {
			speed := int64(float64(written-progressBytes) / elapsed.Seconds())
			for i := range t.Files {
				if taskFileIdentity(t.Files[i].Path) == taskFileIdentity(relFile) {
					t.Files[i].DownloadedBytes = written
					t.Files[i].TotalBytes = t.TotalBytes
					t.Files[i].SpeedBps = speed
					t.Files[i].Status = "downloading"
				}
			}
			m.persistTaskProgress(*t)
			progressAt = time.Now()
			progressBytes = written
		}
		if rErr != nil {
			if rErr == io.EOF {
				break
			}
			_ = f.Sync()
			_ = f.Close()
			return segmentResult{newOffset: written, totalSize: totalSize}, rawCT, &site.Error{
				Site:    transport.Site,
				Kind:    site.ErrKindNetwork,
				Message: "媒体分段读取中断",
				Err:     rErr,
			}
		}
	}

	if err := f.Sync(); err != nil {
		_ = f.Close()
		return segmentResult{newOffset: written, totalSize: totalSize}, rawCT, err
	}
	if err := f.Close(); err != nil {
		return segmentResult{newOffset: written, totalSize: totalSize}, rawCT, err
	}
	if expectedSegLen >= 0 && segWritten != expectedSegLen {
		return segmentResult{newOffset: written, totalSize: totalSize}, rawCT, &site.Error{
			Site:    transport.Site,
			Kind:    site.ErrKindNetwork,
			Message: fmt.Sprintf("分段字节未读满: 实读 %d < 预期 %d", segWritten, expectedSegLen),
		}
	}
	return segmentResult{
		newOffset: written,
		totalSize: totalSize,
		done:      doneAfterThis,
	}, rawCT, nil
}

func newTransportHTTPClient(spec site.MediaTransportSpec) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	guard := site.NewGuardedMediaDialer(spec.Site, spec.AllowLocalhost, dialer, "已拦截内网或回环媒体节点地址")
	tr := &http.Transport{
		Proxy:                 guard.Proxy,
		MaxIdleConns:          16,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DialContext:           guard.DialContext,
	}
	return &http.Client{
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return &site.Error{
					Site:    spec.Site,
					Kind:    site.ErrKindBlocked,
					Message: "媒体重定向超过 5 跳上限",
				}
			}
			if err := spec.ValidateURL(req.URL.String()); err != nil {
				return err
			}
			spec.ApplyHeaders(req)
			return nil
		},
	}
}

func rejectDisguisedMediaContentType(siteID site.ID, rawCT, role string, statusCode int) error {
	mediaType, _, err := mime.ParseMediaType(rawCT)
	if err != nil {
		mediaType = strings.ToLower(strings.TrimSpace(strings.SplitN(rawCT, ";", 2)[0]))
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	switch mediaType {
	case "text/html", "application/xhtml+xml", "application/json", "text/json", "text/plain", "text/xml", "application/xml", "application/javascript", "text/javascript":
		return &site.Error{
			Site:       siteID,
			Kind:       site.ErrKindSchemaChanged,
			HTTPStatus: statusCode,
			Message:    fmt.Sprintf("媒体请求返回了非媒体类型 (%s)", mediaType),
		}
	}
	if (role == "image" || role == "cover") && mediaType != "" &&
		!strings.HasPrefix(mediaType, "image/") && mediaType != "application/octet-stream" {
		return &site.Error{
			Site:       siteID,
			Kind:       site.ErrKindSchemaChanged,
			HTTPStatus: statusCode,
			Message:    fmt.Sprintf("图片响应不是有效的图片类型 (%s)", mediaType),
		}
	}
	return nil
}

func looksLikeDisguisedTextBody(head []byte) bool {
	trimmed := bytes.TrimLeft(head, " \t\r\n")
	if len(trimmed) == 0 {
		return false
	}
	lower := bytes.ToLower(trimmed[:min(len(trimmed), 32)])
	if bytes.HasPrefix(lower, []byte("<!doctype html")) ||
		bytes.HasPrefix(lower, []byte("<html")) ||
		bytes.HasPrefix(lower, []byte("<head")) ||
		bytes.HasPrefix(lower, []byte("<body")) {
		return true
	}
	if trimmed[0] == '{' && (bytes.Contains(lower, []byte(`"status_code"`)) || bytes.Contains(lower, []byte(`"code"`)) || bytes.Contains(lower, []byte(`"error"`))) {
		return true
	}
	return false
}

func sniffImageExtension(filePath, contentType string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var head [32]byte
	n, _ := io.ReadFull(f, head[:])
	b := head[:n]

	switch {
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return ".jpg", nil
	case len(b) >= 8 && bytes.Equal(b[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'}):
		return ".png", nil
	case len(b) >= 12 && bytes.Equal(b[:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")):
		return ".webp", nil
	case len(b) >= 6 && (bytes.Equal(b[:6], []byte("GIF87a")) || bytes.Equal(b[:6], []byte("GIF89a"))):
		return ".gif", nil
	case len(b) >= 12 && bytes.Equal(b[4:8], []byte("ftyp")):
		brand := string(b[8:12])
		switch brand {
		case "avif", "avis":
			return ".avif", nil
		case "heic", "heix", "hevc", "hevx", "mif1", "msf1":
			return ".heic", nil
		}
	}

	mt, _, _ := mime.ParseMediaType(contentType)
	switch strings.ToLower(strings.TrimSpace(mt)) {
	case "image/jpeg", "image/jpg":
		if len(b) >= 4 && !looksLikeDisguisedTextBody(b) {
			return ".jpg", nil
		}
	case "image/png":
		if len(b) >= 4 && !looksLikeDisguisedTextBody(b) {
			return ".png", nil
		}
	case "image/webp":
		if len(b) >= 4 && !looksLikeDisguisedTextBody(b) {
			return ".webp", nil
		}
	case "image/gif":
		if len(b) >= 4 && !looksLikeDisguisedTextBody(b) {
			return ".gif", nil
		}
	case "image/avif":
		if len(b) >= 4 && !looksLikeDisguisedTextBody(b) {
			return ".avif", nil
		}
	case "image/heic", "image/heif":
		if len(b) >= 4 && !looksLikeDisguisedTextBody(b) {
			return ".heic", nil
		}
	}
	return "", errors.New("文件头不是受支持的图片格式")
}

func sniffAudioExtension(filePath, contentType string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var head [32]byte
	n, _ := io.ReadFull(f, head[:])
	b := head[:n]
	if len(b) == 0 || looksLikeDisguisedTextBody(b) {
		return "", errors.New("配乐文件不是有效的音频格式")
	}

	switch {
	case len(b) >= 3 && bytes.Equal(b[:3], []byte("ID3")):
		return ".mp3", nil
	case len(b) >= 2 && b[0] == 0xFF && (b[1]&0xF6) == 0xF0:
		return ".aac", nil
	case len(b) >= 2 && b[0] == 0xFF && (b[1]&0xE0) == 0xE0 && ((b[1]>>1)&0x03) != 0:
		return ".mp3", nil
	case len(b) >= 12 && bytes.Equal(b[4:8], []byte("ftyp")):
		return ".m4a", nil
	case len(b) >= 4 && bytes.Equal(b[:4], []byte("OggS")):
		return ".ogg", nil
	case len(b) >= 4 && bytes.Equal(b[:4], []byte("fLaC")):
		return ".flac", nil
	case len(b) >= 12 && bytes.Equal(b[:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WAVE")):
		return ".wav", nil
	}

	mt, _, _ := mime.ParseMediaType(contentType)
	switch strings.ToLower(strings.TrimSpace(mt)) {
	case "audio/mpeg", "audio/mp3":
		return ".mp3", nil
	case "audio/mp4", "audio/x-m4a", "audio/m4a", "video/mp4":
		return ".m4a", nil
	case "audio/aac", "audio/aacp":
		return ".aac", nil
	case "audio/ogg", "application/ogg":
		return ".ogg", nil
	case "audio/flac":
		return ".flac", nil
	case "audio/wav", "audio/x-wav", "audio/wave":
		return ".wav", nil
	}
	return ".m4a", nil
}

type mediaIdleReader struct {
	r       io.ReadCloser
	timeout time.Duration
}

func newMediaIdleReader(r io.ReadCloser, timeout time.Duration) io.ReadCloser {
	if timeout <= 0 {
		return r
	}
	return &mediaIdleReader{r: r, timeout: timeout}
}

func (m *mediaIdleReader) Read(p []byte) (int, error) {
	type res struct {
		n   int
		err error
	}
	ch := make(chan res, 1)
	go func() {
		n, err := m.r.Read(p)
		ch <- res{n: n, err: err}
	}()
	timer := time.NewTimer(m.timeout)
	defer timer.Stop()
	select {
	case out := <-ch:
		return out.n, out.err
	case <-timer.C:
		_ = m.r.Close()
		return 0, errors.New("media read idle timeout")
	}
}

func (m *mediaIdleReader) Close() error {
	return m.r.Close()
}

func isSiteStopError(err error) bool {
	k := classifyDownloadError(err)
	return k == site.ErrKindRiskControl || k == site.ErrKindRateLimited || k == site.ErrKindAuthRequired
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func sanitizeStoredURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func summaryPrimaryVideoCodec(s MuxTrackSummary) string {
	for i, h := range s.Handlers {
		if h == "vide" && i < len(s.Codecs) {
			switch s.Codecs[i] {
			case "avc1", "avc3":
				return "avc"
			case "hvc1", "hev1":
				return "hevc"
			default:
				return s.Codecs[i]
			}
		}
	}
	return ""
}
