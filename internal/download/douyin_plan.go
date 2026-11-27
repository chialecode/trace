package download

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
)

// WorkPlanOptions carries user quality and codec preferences when planning a site download.
type WorkPlanOptions struct {
	VideoQuality string
	CodecOrder   []string
}

// PlannedNotePage describes one 1-based page in a Douyin/Weibo/X image or mixed note.
type PlannedNotePage struct {
	Index            int
	Kind             string // "image", "gif", "live_photo", "video", "unknown"
	MediaID          string `json:"mediaId,omitempty"`
	FromOrigin       bool
	HLSOnly          bool
	UnknownKind      string
	UnknownReason    string
	Width            int
	Height           int
	DurationSec      int
	ImageURLs        []string
	WatermarkFreeURL string `json:"-"`
	VideoURLs        []string
	NeedSetToken     bool
	Gear             string
	QualityName      string
	Codec            string
	FPS              int
	Bitrate          int64
}

// PlannedThreadPart describes one author self-reply segment saved into the same item (e.g. X threads).
type PlannedThreadPart struct {
	ID           string
	PubTs        int64
	Text         string
	Links        []site.TextLink
	MediaIndexes []int
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

// WorkDownloadPlan holds rich metadata and ephemeral CDN URLs for caching a Douyin, Weibo, or X work.
type WorkDownloadPlan struct {
	Site                    site.ID
	ItemKey                 string // optional canonical key override (e.g. "weibo:dynamic:<mid>", "weibo:video:<oid>", "x:dynamic:<status_id>")
	Kind                    string // "video" or "dynamic" ("note" / "image_note" normalized to "dynamic")
	TargetID                string
	Title                   string
	Desc                    string
	Links                   []site.TextLink
	CanonicalURL            string
	PubTs                   int64
	DurationSec             int
	AuthorUID               string
	AuthorUniqueID          string
	AuthorName              string
	AuthorFace              string
	CoverURLs               []string
	VideoURLs               []string
	HLSOnly                 bool
	NeedSetToken            bool
	OriginalURLs            []string // untranscoded source (原画), tried before VideoURLs
	VideoGear               string
	QualityName             string
	Codec                   string
	Width                   int
	Height                  int
	FPS                     int
	Bitrate                 int64
	AvailableGears          []string
	DowngradeHint           string
	IsForward               bool
	IsFastRetweet           bool
	OriginID                string
	OriginMblogID           string
	OriginCanonicalURL      string
	OriginAuthorUID         string
	OriginAuthorUniqueID    string
	OriginAuthorName        string
	OriginAuthorFace        string
	OriginPubTs             int64
	OriginText              string
	OriginLinks             []site.TextLink
	OriginVideoTitle        string
	OriginUnavailable       bool
	OriginUnavailableReason string
	OriginCard              *site.RichCardSummary
	Card                    *site.RichCardSummary
	VideoFromOrigin         bool
	VisibleType             int
	VisibleLabel            string
	Pages                   []PlannedNotePage
	Thread                  []PlannedThreadPart
	Music                   *PlannedMusic
	MediaID                 string   // clean platform media ID for single-video works
	EditTs                  int64    // sanitized remote edit timestamp
	EditCount               int      // sanitized remote edit counter
	AssetIDs                []string // sanitized ordered platform asset IDs
	ConflictResolution      string   // explicit "keep_both" or "use_remote" when resolving a conflict
	ForceOverwrite          bool     `json:"-"` // explicit manual-only force redownload & atomic overwrite (DEC-079)
	Transport               site.MediaTransportSpec
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

// PlayURLResolver is an optional interface implemented by planners that can
// resolve an authenticated main-site play redirector URL (e.g.
// www.douyin.com/aweme/v1/play/) into a cookieless CDN media URL.
type PlayURLResolver interface {
	ResolvePlayRedirect(ctx context.Context, playURL string) (string, error)
}

// DouyinWorkEnqueueRequest specifies a Douyin video or image/mixed note to enqueue for local caching.
type DouyinWorkEnqueueRequest struct {
	AwemeID string            `json:"awemeId"`
	Kind    string            `json:"kind,omitempty"`   // optional hint: "video" or "dynamic"/"note"
	Source  string            `json:"source,omitempty"` // "manual" or "auto"
	Plan    *WorkDownloadPlan `json:"-"`
	// ForUID and ForName are the account the work is cached for when it
	// came from that account's works list (subscription, cache queue, user
	// page). A work by someone else there is a co-creation: a new item goes
	// to this account's folder with via collab (DEC-063).
	ForUID  string `json:"forUid,omitempty"`
	ForName string `json:"forName,omitempty"`
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

func (a *basicSitePlannerAdapter) ResolvePlayRedirect(ctx context.Context, playURL string) (string, error) {
	if res, ok := a.inner.(PlayURLResolver); ok {
		return res.ResolvePlayRedirect(ctx, playURL)
	}
	return "", &site.Error{Site: a.inner.SiteID(), Kind: site.ErrKindUnavailable, Message: "不支持解析播放跳转地址"}
}

func (a *basicSitePlannerAdapter) PlanWorkDownload(ctx context.Context, targetID string, _ WorkPlanOptions) (WorkDownloadPlan, error) {
	p, err := a.inner.PlanDownload(ctx, targetID, 1)
	if err != nil {
		return WorkDownloadPlan{}, err
	}
	return convertSiteDownloadPlan(p), nil
}

// WorkDownloadPlanFromSite converts a cross-site site.DownloadPlan into a rich WorkDownloadPlan.
func WorkDownloadPlanFromSite(p site.DownloadPlan) WorkDownloadPlan {
	return convertSiteDownloadPlan(p)
}

func convertSiteDownloadPlan(p site.DownloadPlan) WorkDownloadPlan {
	kind := "video"
	hlsOnly := false
	if p.Site == site.Weibo {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(p.ItemKey)), "weibo:video:") ||
			strings.EqualFold(strings.TrimSpace(p.Kind), "video") {
			kind = "video"
		} else {
			kind = "dynamic"
		}
	} else if p.Site == site.X {
		kind = "dynamic"
		if strings.EqualFold(strings.TrimSpace(p.Kind), "hls_only") {
			hlsOnly = true
		}
	} else if p.Kind == "image_note" || p.Kind == "note" || p.Kind == "dynamic" || len(p.Images) > 0 {
		kind = "dynamic"
	}
	canonicalURL := strings.TrimSpace(p.CanonicalURL)
	if canonicalURL == "" && p.TargetID != "" {
		switch p.Site {
		case site.Douyin:
			if kind == "dynamic" {
				canonicalURL = "https://www.douyin.com/note/" + p.TargetID
			} else {
				canonicalURL = "https://www.douyin.com/video/" + p.TargetID
			}
		case site.Weibo:
			if kind == "video" {
				canonicalURL = "https://weibo.com/tv/show/" + strings.Replace(p.TargetID, "1034_", "1034:", 1)
			} else {
				canonicalURL = "https://weibo.com/detail/" + p.TargetID
			}
		case site.X:
			canonicalURL = "https://x.com/i/web/status/" + p.TargetID
		}
	}
	pages := make([]PlannedNotePage, 0, len(p.Images))
	for i, img := range p.Images {
		idx := img.Index
		if idx <= 0 {
			idx = i + 1
		}
		pageKind := strings.ToLower(strings.TrimSpace(img.MediaRole))
		pageHLSOnly := false
		switch pageKind {
		case "image", "gif", "live_photo", "motion", "video":
		case "animated_gif":
			pageKind = "gif"
		case "hls_only":
			pageKind = "video"
			pageHLSOnly = true
		default:
			if len(img.VideoURLs) > 0 {
				pageKind = "live_photo"
			} else {
				pageKind = "image"
			}
		}
		pages = append(pages, PlannedNotePage{
			Index:            idx,
			Kind:             pageKind,
			FromOrigin:       img.FromOrigin,
			HLSOnly:          pageHLSOnly,
			Width:            img.Width,
			Height:           img.Height,
			DurationSec:      img.DurationSec,
			ImageURLs:        append([]string(nil), img.ImageURLs...),
			WatermarkFreeURL: img.WatermarkFreeURL,
			VideoURLs:        append([]string(nil), img.VideoURLs...),
			QualityName:      img.QualityLabel,
			Codec:            img.VideoCodec,
			FPS:              img.FPS,
			Bitrate:          img.Bitrate,
		})
	}
	var music *PlannedMusic
	if len(p.MusicURLs) > 0 {
		music = &PlannedMusic{
			PlayURLs: append([]string(nil), p.MusicURLs...),
		}
	}
	transport := p.Media
	if len(transport.AllowedDomains) == 0 {
		switch p.Site {
		case site.Douyin:
			transport = site.DouyinVODTransport("", false)
		case site.Weibo:
			transport = site.WeiboMediaTransport("", false)
		case site.X:
			transport = site.XMediaTransport("", false)
		}
	}
	var originCard *site.RichCardSummary
	if p.OriginCard != nil {
		cp := *p.OriginCard
		originCard = &cp
	}
	var outerCard *site.RichCardSummary
	if p.Card != nil {
		cp := *p.Card
		outerCard = &cp
	}
	return WorkDownloadPlan{
		Site:                    p.Site,
		ItemKey:                 strings.TrimSpace(p.ItemKey),
		Kind:                    kind,
		TargetID:                p.TargetID,
		Title:                   p.Title,
		Desc:                    p.Desc,
		Links:                   append([]site.TextLink(nil), p.Links...),
		CanonicalURL:            canonicalURL,
		PubTs:                   p.PubTs,
		DurationSec:             p.DurationSec,
		AuthorUID:               p.AuthorUID,
		AuthorName:              p.AuthorName,
		AuthorFace:              p.AuthorFace,
		CoverURLs:               append([]string(nil), p.CoverURLs...),
		VideoURLs:               append([]string(nil), p.VideoURLs...),
		HLSOnly:                 hlsOnly,
		OriginalURLs:            append([]string(nil), p.OriginalURLs...),
		QualityName:             p.QualityLabel,
		Codec:                   p.VideoCodec,
		Width:                   p.Width,
		Height:                  p.Height,
		FPS:                     p.FPS,
		Bitrate:                 p.Bitrate,
		IsForward:               p.IsForward,
		IsFastRetweet:           p.IsFastRetweet,
		OriginID:                p.OriginID,
		OriginMblogID:           p.OriginMblogID,
		OriginCanonicalURL:      p.OriginCanonicalURL,
		OriginAuthorUID:         p.OriginAuthorUID,
		OriginAuthorName:        p.OriginAuthorName,
		OriginAuthorFace:        p.OriginAuthorFace,
		OriginPubTs:             p.OriginPubTs,
		OriginText:              p.OriginText,
		OriginLinks:             append([]site.TextLink(nil), p.OriginLinks...),
		OriginVideoTitle:        p.OriginVideoTitle,
		OriginUnavailable:       p.OriginUnavailable,
		OriginUnavailableReason: p.OriginUnavailableReason,
		OriginCard:              originCard,
		Card:                    outerCard,
		VideoFromOrigin:         p.VideoFromOrigin,
		VisibleType:             p.VisibleType,
		VisibleLabel:            p.VisibleLabel,
		Pages:                   pages,
		Music:                   music,
		Transport:               transport,
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

	source := strings.TrimSpace(req.Source)
	if source == "" {
		source = "manual"
	}
	if source == "manual" {
		ctx = site.WithManualProbe(ctx)
	} else if (source == "auto" || source == "queue") && !site.IsManualProbe(ctx) && site.AutomaticRequestSource(ctx) == "" {
		ctx = site.WithAutomaticRequests(ctx, "download")
	}
	var reqPlanMode string
	var reqPlanForce bool
	if req.Plan != nil {
		reqPlanMode = req.Plan.ConflictResolution
		reqPlanForce = req.Plan.ForceOverwrite
	}
	mode := effectiveConflictResolution(ctx, reqPlanMode)
	forceOverwrite := effectiveForceOverwrite(ctx, source, reqPlanForce)

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
			return Task{}, m.wrapDegradedIfLocalProtected("douyin:video:"+awemeID, mode, err)
		}
		plan = fetched
	}

	plan.Site = site.Douyin
	if plan.TargetID == "" {
		plan.TargetID = awemeID
	}
	if mode != "" {
		plan.ConflictResolution = mode
	}
	plan.ForceOverwrite = forceOverwrite
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
	var via *library.ItemVia
	if existing, exists, gErr := m.fetchExistingAnyKind(itemKey); gErr == nil && exists && existing.ItemDir != "" {
		itemDir = existing.ItemDir
		if existing.Via != nil {
			cp := *existing.Via
			via = &cp
		}
	} else if forUID, fErr := site.NormalizeUID(site.Douyin, req.ForUID); fErr == nil && forUID != normUID {
		if dir, dErr := library.EnsureWithinRoot(cacheRoot, filepath.Join(cacheRoot, "douyin", site.DouyinAuthorDir(forUID), "works", plan.TargetID)); dErr == nil {
			itemDir = dir
			via = &library.ItemVia{Reason: library.ViaCollab, Site: string(site.Douyin), UID: forUID, Name: strings.TrimSpace(req.ForName)}
		}
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

	t, err := m.enqueueSingleTaskWithPlan(Task{
		Site:               string(site.Douyin),
		Kind:               kind,
		Source:             source,
		TargetID:           plan.TargetID,
		Page:               1,
		Title:              title,
		AuthorUID:          normUID,
		AuthorName:         plan.AuthorName,
		AuthorFace:         sanitizeStoredURL(plan.AuthorFace),
		CoverURL:           coverDisplay,
		QualityHint:        plan.DowngradeHint,
		ItemKey:            itemKey,
		ItemDir:            itemDir,
		Via:                via,
		ConflictResolution: mode,
		ForceOverwrite:     forceOverwrite,
	}, &plan)
	if err != nil {
		return Task{}, err
	}
	if mode != "" {
		m.storeTaskResolutionAndPages(t.TaskID, mode, nil)
	}
	if forceOverwrite {
		m.storeTaskForceOverwrite(t.TaskID, true)
	}
	m.markManualProbeIfBackedOff(ctx, t.TaskID, t.Site)

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

// musicFailureReason is the missing-music reason: the error class plus the
// site error's own words (a refused host, a status), which name no signed
// URL, so a partial note says why its music failed.
func musicFailureReason(err error) string {
	reason := fmt.Sprintf("配乐下载失败 (%s)", classifyDownloadError(err))
	var se *site.Error
	if errors.As(err, &se) {
		if msg := strings.TrimSpace(se.Message); msg != "" {
			reason += "：" + msg
		}
	}
	return reason
}

func summarizeMissingReasons(missing []library.MissingRecord) string {
	var reasons []string
	seen := make(map[string]bool, len(missing))
	for _, m := range missing {
		r := strings.TrimSpace(m.Reason)
		if r != "" && !seen[r] {
			seen[r] = true
			reasons = append(reasons, r)
		}
	}
	switch len(reasons) {
	case 0:
		return ""
	case 1:
		if len(missing) > 1 {
			return fmt.Sprintf("%s（共 %d 项未完成）", reasons[0], len(missing))
		}
		return reasons[0]
	default:
		return fmt.Sprintf("%s 等 %d 项未完成", reasons[0], len(missing))
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
	switch id := site.ID(strings.ToLower(strings.TrimSpace(siteStr))); id {
	case site.Bilibili:
		if m.client != nil {
			m.client.ReportAutomaticError(ctx, err)
		}
	case site.Douyin, site.Weibo, site.X:
		if planner, ok := m.getWorkPlanner(id); ok {
			if rep, ok := planner.(SiteErrorReporter); ok {
				rep.ReportAutomaticError(ctx, err)
			}
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

	if hasInitial && (initial.TargetID != "" || len(initial.VideoURLs) > 0 || len(initial.OriginalURLs) > 0 || len(initial.Pages) > 0) {
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
	if hasInitial && initial.ConflictResolution != "" && plan.ConflictResolution == "" {
		plan.ConflictResolution = initial.ConflictResolution
	}
	if hasInitial && initial.ForceOverwrite {
		plan.ForceOverwrite = true
	}
	if len(plan.Transport.AllowedDomains) == 0 {
		plan.Transport = site.DouyinVODTransport("", false)
	}
	return plan, planner, nil
}

func (m *Manager) executeDouyinTask(ctx context.Context, t *Task) error {
	unlock, err := m.lockItem(ctx, "douyin:"+t.TargetID)
	if err != nil {
		return err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	storedMode, _ := m.taskResolutionAndPages(t.TaskID)
	mode := effectiveConflictResolution(ctx, t.ConflictResolution, storedMode)

	plan, planner, err := m.consumeInitialOrFetchDouyinPlan(ctx, t.TaskID, t.TargetID)
	if err != nil {
		return m.wrapDegradedIfLocalProtected(firstNonEmptyStr(t.ItemKey, "douyin:video:"+t.TargetID), mode, err)
	}
	mode = effectiveConflictResolution(ctx, t.ConflictResolution, storedMode, plan.ConflictResolution)
	plan.ConflictResolution = mode
	t.ConflictResolution = mode
	forceOverwrite := effectiveForceOverwrite(ctx, t.Source, t.ForceOverwrite, m.taskForceOverwrite(t.TaskID), plan.ForceOverwrite)
	plan.ForceOverwrite = forceOverwrite
	t.ForceOverwrite = forceOverwrite
	if forceOverwrite {
		ctx = WithForceOverwrite(ctx)
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
	plan.Kind = kind
	t.Kind = kind
	if kind == "dynamic" {
		return m.executeDouyinNoteTask(ctx, t, plan, planner)
	}
	return m.executeDouyinVideoTask(ctx, t, plan, planner)
}

// douyinItemVia is the via of a Douyin item: an existing item keeps its own;
// a new one stored under the folder of the account it was cached for
// (Task.Via) records that account as a co-creator.
func (m *Manager) douyinItemVia(t *Task, previous library.ItemMetadata, exists bool, itemDir string) *library.ItemVia {
	if exists && previous.Via != nil {
		return previous.Via
	}
	if t.Via == nil || t.Via.UID == "" {
		return nil
	}
	root := m.lib.CacheDir()
	want, err := library.EnsureWithinRoot(root, filepath.Join(root, "douyin", site.DouyinAuthorDir(t.Via.UID), "works", t.TargetID))
	if err != nil || !strings.EqualFold(filepath.Clean(want), filepath.Clean(itemDir)) {
		return nil
	}
	v := *t.Via
	return &v
}

func (m *Manager) resolveDouyinItemDir(t *Task, kind, normUID, awemeID string) (library.ItemMetadata, bool, string, error) {
	itemKey := library.CanonicalItemKey("douyin", kind, awemeID)
	previous, exists, err := m.fetchExistingAnyKind(itemKey)
	if err != nil {
		return library.ItemMetadata{}, false, "", err
	}
	if exists && previous.ItemDir != "" {
		_ = library.RecoverInterruptedItemSwap(previous.ItemDir, previous)
		previous, exists, err = m.fetchExistingAnyKind(itemKey)
		if err != nil {
			return library.ItemMetadata{}, false, "", err
		}
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

func (m *Manager) makeSinglePlanRefresher(ctx context.Context, targetID string, planner WorkPlanner) func() (WorkDownloadPlan, error) {
	return m.makeSinglePlanRefresherForSite(ctx, site.Douyin, targetID, planner)
}

func (m *Manager) makeSinglePlanRefresherForSite(ctx context.Context, siteID site.ID, targetID string, planner WorkPlanner) func() (WorkDownloadPlan, error) {
	var once sync.Once
	var cached WorkDownloadPlan
	var cachedErr error
	return func() (WorkDownloadPlan, error) {
		once.Do(func() {
			if planner == nil {
				cachedErr = &site.Error{Site: siteID, Kind: site.ErrKindUnavailable, Message: "无法刷新过期的媒体地址"}
				return
			}
			m.mu.Lock()
			opts := WorkPlanOptions{
				VideoQuality: m.dlSettings.VideoQuality,
				CodecOrder:   append([]string(nil), m.dlSettings.CodecOrder...),
			}
			m.mu.Unlock()
			cached, cachedErr = planner.PlanWorkDownload(ctx, targetID, opts)
		})
		return cached, cachedErr
	}
}

// makeOnceURLRefresher shares one refreshed plan across a task's files while
// limiting each individual file to at most one URL refresh attempt.
func makeOnceURLRefresher(
	refreshPlan func() (WorkDownloadPlan, error),
	extract func(WorkDownloadPlan) ([]string, error),
) func() ([]string, error) {
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

func isSiteStopError(err error) bool {
	k := classifyDownloadError(err)
	return k == site.ErrKindRiskControl || k == site.ErrKindRateLimited || k == site.ErrKindAuthRequired
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

// Staging names of media that is downloaded first and checked or converted
// afterwards. They all end in ".part", so nothing that copies an item folder
// takes them for finished files (docs/design/remote.md §6.1).
const (
	stageSuffix = ".stage.part" // transcoded stream before it is checked
	origSuffix  = ".orig.part"  // untranscoded source before it is checked
	imgSuffix   = ".img.part"   // image before its type is known
	audSuffix   = ".aud.part"   // audio before its type is known
)

// stagingMarkers are the staging suffixes without the trailing ".part",
// with the names earlier builds used (kept so their downloads resume).
var stagingMarkers = []string{".stage", ".orig", ".img", ".aud", ".orig.raw", ".raw", ".imgtmp", ".audtmp"}

// trimStagingSuffixes maps a download or staging path back to its final name.
func trimStagingSuffixes(p string) string {
	p = strings.TrimSuffix(p, ".part")
	p = strings.TrimSuffix(p, ".part")
	for _, suf := range stagingMarkers {
		if strings.HasSuffix(p, suf) {
			return strings.TrimSuffix(p, suf)
		}
	}
	return p
}

// adoptLegacyStaging renames the staging files an earlier build left for
// base (base+legacy and its ".part") to the current names, so a download
// that was in progress during an upgrade resumes. Only files this task
// registered are renamed; its progress record follows the new name.
func (m *Manager) adoptLegacyStaging(t *Task, itemDir, base, legacy, current string) {
	m.adoptLegacyName(t, itemDir, base+legacy, base+current)
}

// adoptLegacyName moves a file this task owns from the name an older
// version used (oldRel, and its .part) to newRel, so a download paused
// before the upgrade resumes instead of starting over.
func (m *Manager) adoptLegacyName(t *Task, itemDir, oldRel, newRel string) {
	if t == nil || itemDir == "" || oldRel == newRel {
		return
	}
	idx := -1
	for i := range t.Files {
		if taskFileIdentity(t.Files[i].Path) == taskFileIdentity(oldRel) && taskFileOwned(t.Files[i]) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return
	}
	for _, pair := range [][2]string{{oldRel, newRel}, {oldRel + ".part", newRel + ".part"}} {
		from, err1 := library.ResolveItemPath(itemDir, pair[0])
		to, err2 := library.ResolveItemPath(itemDir, pair[1])
		if err1 != nil || err2 != nil {
			continue
		}
		if _, err := os.Lstat(to); err == nil {
			continue
		}
		if _, err := os.Lstat(from); err == nil {
			_ = os.Rename(from, to)
		}
	}
	t.Files[idx].Path = newRel
	_ = m.saveTaskProgress(*t)
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
