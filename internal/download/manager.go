package download

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
)

// DefaultRangeChunkSize is the 8 MiB closed-interval segment size required by data-and-cache.md §2.4.
const DefaultRangeChunkSize int64 = 8 * 1024 * 1024

// MaxImageFileBytes is the 50 MiB per-image limit required by data-and-cache.md §2.4.
const MaxImageFileBytes int64 = 50 * 1024 * 1024

// TaskFileProgress tracks per-file download state within a Task.
type TaskFileProgress struct {
	Owned           bool   `json:"owned,omitempty"`
	Role            string `json:"role"`   // "cover", "image", "video_stream", "audio_stream", "video", "audio", "danmaku", "subtitle"
	Path            string `json:"path"`   // relative path within item folder
	Status          string `json:"status"` // "waiting", "downloading", "verifying", "completed", "failed", "skipped"
	DownloadedBytes int64  `json:"downloadedBytes"`
	TotalBytes      int64  `json:"totalBytes"`
	SpeedBps        int64  `json:"speedBps"`
	SHA256          string `json:"sha256,omitempty"`
	ItemDir         string `json:"itemDir,omitempty"`
}

// TaskAttempt records a desensitized failed segment attempt (host only, no query string or full URL, max 200 per task).
type TaskAttempt struct {
	Time       string `json:"time"`
	File       string `json:"file"`
	Offset     int64  `json:"offset"`
	Host       string `json:"host"`
	Attempt    int    `json:"attempt"`
	ErrorKind  string `json:"errorKind"`
	HTTPStatus int    `json:"httpStatus"`
}

// TaskDetail combines a Task with its file list and attempt history (TaskDetail(id)).
type TaskDetail struct {
	Task     Task               `json:"task"`
	Files    []TaskFileProgress `json:"files"`
	Attempts []TaskAttempt      `json:"attempts"`
}

// Task represents a persistent download/cache task stored in SQLite.
type Task struct {
	TaskID            string `json:"taskId"`
	Site              string `json:"site"`
	Kind              string `json:"kind"`             // "video", "dynamic", "supplement", "upgrade"
	Source            string `json:"source,omitempty"` // "manual" or "auto"
	TargetID          string `json:"targetId"`
	CID               int64  `json:"cid,omitempty"`
	Page              int    `json:"page,omitempty"`
	Title             string `json:"title"`
	PartTitle         string `json:"partTitle,omitempty"`
	Mid               int64  `json:"mid,omitempty"`
	AuthorUID         string `json:"authorUid,omitempty"`
	AuthorName        string `json:"authorName,omitempty"`
	AuthorFace        string `json:"authorFace,omitempty"`
	CoverURL          string `json:"coverUrl,omitempty"`
	CacheLinkedVideos bool   `json:"cacheLinkedVideos,omitempty"`
	LinkedVideo       bool   `json:"-"` // a video cached as part of a dynamic
	ViaName           string `json:"-"` // forwarder name for a video cached from a forward (DEC-056)
	ViaDynamicID      string `json:"-"` // forwarding dynamic for such a video
	// Via is the creator a Douyin work is cached for when they are not its
	// author (a co-creation found in their works list): the item goes to
	// their folder with this via. Kept in via_json so a resumed task still
	// writes it.
	Via             *library.ItemVia   `json:"via,omitempty"`
	ProgressOwner   *Task              `json:"-"`
	ProgressPrefix  string             `json:"-"`
	Status          string             `json:"status"` // "queued", "running", "paused", "completed", "failed", "canceled", "partial"
	PauseReason     string             `json:"pauseReason,omitempty"`
	ErrorKind       string             `json:"errorKind,omitempty"`
	ErrorMessage    string             `json:"errorMessage,omitempty"`
	LastError       string             `json:"lastError,omitempty"`
	QualityHint     string             `json:"qualityHint,omitempty"`
	BytesDownloaded int64              `json:"bytesDownloaded"`
	TotalBytes      int64              `json:"totalBytes"`
	Progress        float64            `json:"progress"`
	ItemKey         string             `json:"itemKey,omitempty"`
	ItemDir         string             `json:"itemDir,omitempty"`
	Files           []TaskFileProgress `json:"files,omitempty"`
	Attempts        []TaskAttempt      `json:"attempts,omitempty"`
	CreatedAt       string             `json:"createdAt"`
	UpdatedAt       string             `json:"updatedAt"`
}

// VideoEnqueueRequest specifies a video (and optional page subset) to cache.
type VideoEnqueueRequest struct {
	BVID   string                `json:"bvid"`
	Pages  []int                 `json:"pages,omitempty"` // 1-based page numbers; empty means all pages (or first page if MultiPart=="first")
	Source string                `json:"source,omitempty"`
	Detail *bilibili.VideoDetail `json:"-"`
	// ForMid is the creator the video is cached for (the subscription, user
	// page or cache queue). When that creator is a collaboration member but
	// not the uploader, a new item is stored under their folder (DEC-056).
	ForMid int64 `json:"forMid,omitempty"`
}

// DynamicEnqueueRequest specifies a dynamic post to cache.
type DynamicEnqueueRequest struct {
	DynamicID         string                `json:"dynamicId"`
	CacheLinkedVideos bool                  `json:"cacheLinkedVideos"`
	Source            string                `json:"source,omitempty"`
	Item              *bilibili.DynamicItem `json:"-"`
}

type keyedLock struct {
	ch   chan struct{}
	refs int
}

// Manager coordinates queued and active download tasks with concurrency limits and circuit breaking.
type Manager struct {
	mu                sync.Mutex
	itemLockMu        sync.Mutex
	itemLocks         map[string]*keyedLock
	db                *sql.DB
	lib               *library.Store
	client            *bilibili.Client
	dlSettings        settings.DownloadSettings
	onTaskUpdate      func(Task)
	onTaskBatchUpdate func(action string, tasks []Task)
	notifyMu          sync.Mutex
	lastNotify        map[string]taskNotification
	taskFaults        sync.Map

	chunkSize    int64
	backoffScale time.Duration
	mediaWrap    func(http.RoundTripper) http.RoundTripper
	transports   map[mediaTransportKey]*http.Transport

	planners     map[site.ID]WorkPlanner
	initialPlans map[string]WorkDownloadPlan

	ctx        context.Context
	cancel     context.CancelFunc
	wakeCh     chan struct{}
	activeRun  map[string]context.CancelFunc
	activeSite map[string]string
	wg         sync.WaitGroup
}

// NewManager creates and initializes a download Manager backed by db.
func NewManager(
	db *sql.DB,
	lib *library.Store,
	client *bilibili.Client,
	dlSettings settings.DownloadSettings,
	onUpdate func(Task),
) (*Manager, error) {
	if dlSettings.MaxConcurrentTasks < 1 {
		dlSettings.MaxConcurrentTasks = 2
	}
	m := &Manager{
		itemLocks:    make(map[string]*keyedLock),
		db:           db,
		lib:          lib,
		client:       client,
		dlSettings:   dlSettings,
		onTaskUpdate: onUpdate,
		lastNotify:   make(map[string]taskNotification),
		chunkSize:    DefaultRangeChunkSize,
		backoffScale: time.Second,
		transports:   make(map[mediaTransportKey]*http.Transport),
		planners:     make(map[site.ID]WorkPlanner),
		initialPlans: make(map[string]WorkDownloadPlan),
		wakeCh:       make(chan struct{}, 1),
		activeRun:    make(map[string]context.CancelFunc),
		activeSite:   make(map[string]string),
	}
	if err := m.initSchema(); err != nil {
		return nil, err
	}
	return m, nil
}

// lockItem acquires a context-cancellable per-item lock so concurrent tasks
// targeting the same manifest do not overwrite each other while remaining
// immediately cancellable on Pause/Cancel/Stop and never leaking map entries.
func (m *Manager) lockItem(ctx context.Context, key string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.itemLockMu.Lock()
	if m.itemLocks == nil {
		m.itemLocks = make(map[string]*keyedLock)
	}
	kl, ok := m.itemLocks[key]
	if !ok {
		kl = &keyedLock{ch: make(chan struct{}, 1)}
		m.itemLocks[key] = kl
	}
	kl.refs++
	m.itemLockMu.Unlock()

	releaseRef := func() {
		m.itemLockMu.Lock()
		kl.refs--
		if kl.refs == 0 && m.itemLocks[key] == kl {
			delete(m.itemLocks, key)
		}
		m.itemLockMu.Unlock()
	}

	select {
	case kl.ch <- struct{}{}:
		return func() {
			<-kl.ch
			releaseRef()
		}, nil
	case <-ctx.Done():
		releaseRef()
		return nil, ctx.Err()
	}
}

// SetBackoffScale overrides the 1s base retry backoff unit (used by unit tests for fast deterministic retries).
func (m *Manager) SetBackoffScale(scale time.Duration) {
	m.mu.Lock()
	m.backoffScale = scale
	m.mu.Unlock()
}

// SetMediaTransportWrap wraps the transport of every site media client the manager
// builds. Only the bounded real-site checker uses it, to put media requests on its
// quota ledger; the app never sets it.
func (m *Manager) SetMediaTransportWrap(wrap func(http.RoundTripper) http.RoundTripper) {
	m.mu.Lock()
	m.mediaWrap = wrap
	m.mu.Unlock()
}

// SetChunkSize overrides the 8 MiB Range segment size (used by unit tests to test multi-segment boundaries on small payloads).
func (m *Manager) SetChunkSize(bytes int64) {
	m.mu.Lock()
	if bytes > 0 {
		m.chunkSize = bytes
	}
	m.mu.Unlock()
}

// Start marks any leftover "queued" or "running" tasks from a previous session as
// "paused" ("已暂停（应用关闭）") per data-and-cache.md §2.4, and launches the worker loop.
func (m *Manager) Start(parentCtx context.Context) error {
	m.mu.Lock()
	if m.cancel != nil {
		m.mu.Unlock()
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := m.db.Exec(`
		UPDATE download_tasks
		SET status = 'paused', pause_reason = 'app_closed', error_message = '已暂停（应用关闭）', updated_at = ?
		WHERE status IN ('running', 'queued')
	`, now)
	if err != nil {
		m.mu.Unlock()
		return err
	}

	m.ctx, m.cancel = context.WithCancel(parentCtx)
	m.mu.Unlock()

	m.wg.Add(1)
	go m.dispatchLoop()
	return nil
}

// Stop cancels all active downloads, waits for background workers to exit, and
// releases idle connections on pooled media transports.
func (m *Manager) Stop() {
	m.PauseAll("app_closed", "已暂停（应用关闭）")
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	for _, fn := range m.activeRun {
		fn()
	}
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	m.wg.Wait()

	m.mu.Lock()
	transports := make([]*http.Transport, 0, len(m.transports))
	for _, tr := range m.transports {
		transports = append(transports, tr)
	}
	clear(m.transports)
	m.mu.Unlock()
	for _, tr := range transports {
		tr.CloseIdleConnections()
	}
}

func (m *Manager) pauseTasksWhereLocked(reason, errKind, message, whereClause string, whereArgs ...any) ([]string, error) {
	rows, err := m.db.Query(`SELECT task_id FROM download_tasks WHERE `+whereClause, whereArgs...)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if scanErr := rows.Scan(&id); scanErr != nil {
			_ = rows.Close()
			return nil, scanErr
		}
		ids = append(ids, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	args := make([]any, 0, 5+len(whereArgs))
	args = append(args, reason, errKind, errKind, message, now)
	args = append(args, whereArgs...)
	_, err = m.db.Exec(`
		UPDATE download_tasks
		SET status = 'paused',
			pause_reason = ?,
			error_kind = CASE WHEN ? != '' THEN ? ELSE error_kind END,
			error_message = ?,
			updated_at = ?
		WHERE `+whereClause, args...)
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// SetOnTaskBatchUpdate registers a coalesced notification callback for batch
// task transitions (PauseAll, PauseForSite, RetryActionable, site circuit-breaker).
// When set, batch operations invoke fn once with all affected task summaries
// instead of emitting M separate onTaskUpdate callbacks.
func (m *Manager) SetOnTaskBatchUpdate(fn func(action string, tasks []Task)) {
	m.notifyMu.Lock()
	m.onTaskBatchUpdate = fn
	m.notifyMu.Unlock()
}

func (m *Manager) emitBatchTasks(action string, ids []string) {
	if len(ids) == 0 {
		return
	}
	m.notifyMu.Lock()
	batchFn := m.onTaskBatchUpdate
	singleFn := m.onTaskUpdate
	m.notifyMu.Unlock()
	if batchFn == nil && singleFn == nil {
		return
	}
	tasks, err := m.listTasksSummaryByIDs(ids)
	if err != nil || len(tasks) == 0 {
		return
	}
	if batchFn != nil {
		now := time.Now()
		m.notifyMu.Lock()
		if m.lastNotify == nil {
			m.lastNotify = make(map[string]taskNotification)
		}
		for i := range tasks {
			if tasks[i].ErrorMessage == "" && tasks[i].LastError != "" {
				tasks[i].ErrorMessage = tasks[i].LastError
			}
			tasks[i].LastError = tasks[i].ErrorMessage
			switch tasks[i].Status {
			case "completed", "failed", "canceled", "partial":
				delete(m.lastNotify, tasks[i].TaskID)
			default:
				m.lastNotify[tasks[i].TaskID] = taskNotification{status: tasks[i].Status, at: now}
			}
		}
		m.notifyMu.Unlock()
		batchFn(action, tasks)
		return
	}
	for _, t := range tasks {
		m.emitUpdate(t)
	}
}

// PauseAll pauses all currently running and queued tasks across all sites with the specified reason.
func (m *Manager) PauseAll(reason, message string) {
	m.mu.Lock()
	for id, fn := range m.activeRun {
		delete(m.initialPlans, id)
		fn()
	}
	clear(m.initialPlans)
	if reason == "" {
		reason = "user"
	}
	if message == "" {
		message = "已暂停"
	}
	ids, err := m.pauseTasksWhereLocked(reason, "", message, `status IN ('queued', 'running')`)
	if err == nil {
		m.taskFaults.Range(func(key, _ any) bool { m.taskFaults.Delete(key); return true })
	}
	m.mu.Unlock()
	if err == nil {
		m.emitBatchTasks("pause_all", ids)
	}
}

// PauseForSite pauses only running and queued tasks belonging to siteID without interrupting other sites.
func (m *Manager) PauseForSite(siteID site.ID, reason, message string) {
	targetSite := strings.TrimSpace(string(siteID))
	if targetSite == "" {
		return
	}
	m.mu.Lock()
	var pausedIDs []string
	for id, fn := range m.activeRun {
		if m.activeSite[id] == targetSite {
			delete(m.initialPlans, id)
			pausedIDs = append(pausedIDs, id)
			fn()
		}
	}
	for id, plan := range m.initialPlans {
		if string(plan.Site) == targetSite {
			delete(m.initialPlans, id)
		}
	}
	if reason == "" {
		reason = "user"
	}
	if message == "" {
		message = "已暂停"
	}
	ids, err := m.pauseTasksWhereLocked(reason, "", message, `site = ? AND status IN ('queued', 'running')`, targetSite)
	if err == nil {
		for _, id := range pausedIDs {
			m.taskFaults.Delete(id)
		}
	}
	m.mu.Unlock()
	if err == nil {
		m.emitBatchTasks("pause_site", ids)
	}
	m.notifyWake()
}

// UpdateSettings updates download concurrency and stream preferences dynamically.
func (m *Manager) UpdateSettings(s settings.DownloadSettings) {
	m.mu.Lock()
	if s.MaxConcurrentTasks < 1 {
		s.MaxConcurrentTasks = 2
	}
	m.dlSettings = s
	m.mu.Unlock()
	m.notifyWake()
}

// EnqueueVideo resolves video pages and enqueues download tasks (deduplicating active/queued tasks by site+kind+targetId+cid).
func (m *Manager) EnqueueVideo(ctx context.Context, req VideoEnqueueRequest) ([]Task, error) {
	if err := m.client.RequireAuth("download:video"); err != nil {
		return nil, err
	}
	detail := req.Detail
	if detail == nil {
		vd, err := m.client.VideoDetail(ctx, req.BVID)
		if err != nil {
			return nil, err
		}
		detail = &vd
	}

	pageSet := make(map[int]bool, len(req.Pages))
	for _, p := range req.Pages {
		pageSet[p] = true
	}

	source := req.Source
	if source == "" {
		source = "manual"
	}

	m.mu.Lock()
	multiPart := m.dlSettings.MultiPart
	m.mu.Unlock()
	itemDir := ""
	if req.ForMid > 0 && req.ForMid != detail.Author.Mid && detail.HasStaffMember(req.ForMid) {
		root := m.lib.CacheDir()
		if dir, err := library.EnsureWithinRoot(root, filepath.Join(root, "bilibili", strconv.FormatInt(req.ForMid, 10), "videos", detail.BVID)); err == nil {
			itemDir = dir
		}
	}
	var tasks []Task
	for _, vp := range detail.Pages {
		if len(pageSet) == 0 && multiPart == "first" && vp.Page != 1 {
			continue
		}
		if len(pageSet) > 0 && !pageSet[vp.Page] {
			continue
		}
		t, err := m.enqueueSingleTask(Task{
			Site:       "bilibili",
			Kind:       "video",
			Source:     source,
			TargetID:   detail.BVID,
			CID:        vp.CID,
			Page:       vp.Page,
			Title:      detail.Title,
			PartTitle:  vp.Title,
			Mid:        detail.Author.Mid,
			AuthorName: detail.Author.Name,
			AuthorFace: detail.Author.Face,
			CoverURL:   detail.Cover,
			ItemKey:    library.CanonicalItemKey("bilibili", "video", detail.BVID),
			ItemDir:    itemDir,
		})
		if err != nil {
			return tasks, err
		}
		tasks = append(tasks, t)
	}
	m.notifyWake()
	return tasks, nil
}

// EnqueueDynamic enqueues a dynamic post for local caching (and optionally its linked videos).
func (m *Manager) EnqueueDynamic(ctx context.Context, req DynamicEnqueueRequest) (Task, error) {
	if err := m.client.RequireAuth("download:dynamic"); err != nil {
		return Task{}, err
	}
	dyn := req.Item
	if dyn == nil {
		fetched, err := m.client.DynamicDetail(ctx, req.DynamicID)
		if err != nil {
			return Task{}, err
		}
		dyn = &fetched
	}

	source := req.Source
	if source == "" {
		source = "manual"
	}

	t, err := m.enqueueSingleTask(Task{
		Site:              "bilibili",
		Kind:              "dynamic",
		Source:            source,
		TargetID:          dyn.ID,
		Page:              1,
		Title:             dyn.Title,
		Mid:               dyn.Author.Mid,
		AuthorName:        dyn.Author.Name,
		AuthorFace:        dyn.Author.Face,
		CacheLinkedVideos: true,
		ItemKey:           library.CanonicalItemKey("bilibili", "dynamic", dyn.ID),
	})
	if err != nil {
		return Task{}, err
	}
	m.notifyWake()
	return t, nil
}

// CacheSupplement enqueues a task that only downloads missing/unsaved danmaku and subtitles for an existing cached video (data-and-cache.md §2.9).
func (m *Manager) CacheSupplement(ctx context.Context, bvid string) (Task, error) {
	if err := m.client.RequireAuth("download:supplement"); err != nil {
		return Task{}, err
	}
	meta, ok, err := m.lib.GetItem(bvid)
	if err != nil {
		return Task{}, err
	}
	if !ok {
		return Task{}, fmt.Errorf("download: video %s is not in local library", bvid)
	}
	t, err := m.enqueueSingleTask(Task{
		Site:       "bilibili",
		Kind:       "supplement",
		Source:     "manual",
		TargetID:   meta.RawID,
		Page:       1,
		Title:      meta.Title,
		PartTitle:  "补存弹幕和字幕",
		Mid:        meta.Author.Mid,
		AuthorName: meta.Author.Name,
		AuthorFace: meta.Author.Face,
		ItemKey:    meta.ItemKey,
		ItemDir:    meta.ItemDir,
	})
	if err != nil {
		return Task{}, err
	}
	m.notifyWake()
	return t, nil
}

// Upgrade enqueues an upgrade task for a cached video that re-downloads only upgradable parts and atomically replaces old files after verification (data-and-cache.md §2.7).
func (m *Manager) Upgrade(ctx context.Context, keyOrBVID string) (Task, error) {
	if err := m.client.RequireAuth("download:upgrade"); err != nil {
		return Task{}, err
	}
	meta, ok, err := m.lib.GetItem(keyOrBVID)
	if err != nil {
		return Task{}, err
	}
	if !ok {
		return Task{}, fmt.Errorf("download: item %s is not in local library", keyOrBVID)
	}
	t, err := m.enqueueSingleTask(Task{
		Site:       "bilibili",
		Kind:       "upgrade",
		Source:     "manual",
		TargetID:   meta.RawID,
		Page:       1,
		Title:      meta.Title,
		PartTitle:  "画质升级",
		Mid:        meta.Author.Mid,
		AuthorName: meta.Author.Name,
		AuthorFace: meta.Author.Face,
		ItemKey:    meta.ItemKey,
		ItemDir:    meta.ItemDir,
	})
	if err != nil {
		return Task{}, err
	}
	m.notifyWake()
	return t, nil
}

// WithIdleTasks keeps cache migration from overlapping a worker that is still
// exiting after Pause, or an enqueue/start while registered files are moved.
// work must not call back into Manager.
func (m *Manager) WithIdleTasks(work func() error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.activeRun) > 0 {
		return fmt.Errorf("迁移前请等待下载任务停止写入")
	}
	var busy int
	if err := m.db.QueryRow("SELECT COUNT(*) FROM download_tasks WHERE status IN ('running', 'queued')").Scan(&busy); err != nil {
		return err
	}
	if busy > 0 {
		return fmt.Errorf("迁移前请暂停下载任务")
	}
	return work()
}

// Pause transitions a queued or running task to "paused".
func (m *Manager) Pause(taskID string) (Task, error) {
	m.mu.Lock()
	delete(m.initialPlans, taskID)
	if cancel, ok := m.activeRun[taskID]; ok {
		cancel()
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := m.db.Exec(`
		UPDATE download_tasks
		SET status = 'paused', pause_reason = 'user', updated_at = ?
		WHERE task_id = ? AND status IN ('queued', 'running')
	`, now, taskID)
	m.mu.Unlock()
	if err != nil {
		return Task{}, err
	}
	m.taskFaults.Delete(taskID)
	t, _, err := m.GetTask(taskID)
	m.emitUpdate(t)
	m.notifyWake()
	return t, err
}

// Resume transitions a paused, failed, or partial task back to "queued".
func (m *Manager) Resume(taskID string) (Task, error) {
	m.mu.Lock()
	delete(m.initialPlans, taskID)
	_, storageFault := m.taskFaults.Load(taskID)
	if storageFault {
		if cancel := m.activeRun[taskID]; cancel != nil {
			cancel()
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := m.db.Exec(`
		UPDATE download_tasks
		SET status = 'queued', pause_reason = '', error_kind = '', error_message = '', updated_at = ?
		WHERE task_id = ? AND (status IN ('paused', 'failed', 'partial') OR (? AND status = 'running'))
	`, now, taskID, storageFault)
	m.mu.Unlock()
	if err != nil {
		return Task{}, err
	}
	m.taskFaults.Delete(taskID)
	t, _, err := m.GetTask(taskID)
	m.emitUpdate(t)
	m.notifyWake()
	return t, err
}

// Retry resets a failed, partial, or paused task to "queued" so missing files are re-fetched.
func (m *Manager) Retry(taskID string) (Task, error) {
	return m.Resume(taskID)
}

// IsRetryableTask reports whether t is an actionable failed, partial, or
// non-user-paused task eligible for batch retry. Explicit user pauses
// (pause_reason == "user") and terminal tasks (completed, canceled) return false.
func IsRetryableTask(t Task) bool {
	if t.Status == "failed" || t.Status == "partial" {
		return true
	}
	if (t.Status == "paused" || strings.HasPrefix(t.Status, "paused_")) && strings.TrimSpace(t.PauseReason) != "user" {
		return true
	}
	return false
}

// BatchRetryResult summarizes a batch retry operation across actionable download tasks.
type BatchRetryResult struct {
	Retried          int      `json:"retried"`
	SkippedAuth      int      `json:"skippedAuth"`
	SkippedDuplicate int      `json:"skippedDuplicate"`
	MissingAuthSites []string `json:"missingAuthSites,omitempty"`
}

func normalizeTaskSite(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return string(site.Bilibili)
	}
	if id, ok := site.ParseID(trimmed); ok {
		return string(id)
	}
	return strings.ToLower(trimmed)
}

type taskDedupKey struct {
	site     string
	kind     string
	targetID string
	cid      int64
}

// RetryActionable re-queues all actionable failed, partial, and non-user-paused
// tasks whose normalized site is permitted in allowedSites, deduplicating by
// (site, kind, target_id, cid) against active/queued tasks and within the batch.
func (m *Manager) RetryActionable(allowedSites map[string]bool) (BatchRetryResult, error) {
	type retryCandidate struct {
		taskID       string
		storageFault bool
	}

	m.mu.Lock()
	rows, err := m.db.Query(`SELECT` + taskSummarySelectColumns + `
		FROM download_tasks
		WHERE status NOT IN ('completed', 'canceled')
		ORDER BY julianday(updated_at) DESC, rowid DESC
	`)
	if err != nil {
		m.mu.Unlock()
		return BatchRetryResult{}, err
	}
	var allTasks []Task
	for rows.Next() {
		t, scanErr := scanTaskSummary(rows)
		if scanErr != nil {
			_ = rows.Close()
			m.mu.Unlock()
			return BatchRetryResult{}, scanErr
		}
		allTasks = append(allTasks, t)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		m.mu.Unlock()
		return BatchRetryResult{}, err
	}

	activeKeys := make(map[taskDedupKey]bool)
	for _, raw := range allTasks {
		eff := m.withTaskFault(raw)
		if eff.Status == "queued" || eff.Status == "running" {
			dk := taskDedupKey{
				site:     normalizeTaskSite(eff.Site),
				kind:     eff.Kind,
				targetID: eff.TargetID,
				cid:      eff.CID,
			}
			activeKeys[dk] = true
		}
	}

	var (
		res        BatchRetryResult
		missingSet = make(map[string]bool)
		candidates []retryCandidate
	)
	for _, raw := range allTasks {
		eff := m.withTaskFault(raw)
		if !IsRetryableTask(eff) {
			continue
		}
		normSite := normalizeTaskSite(eff.Site)
		if !allowedSites[normSite] {
			res.SkippedAuth++
			missingSet[normSite] = true
			continue
		}
		dk := taskDedupKey{
			site:     normSite,
			kind:     eff.Kind,
			targetID: eff.TargetID,
			cid:      eff.CID,
		}
		if activeKeys[dk] {
			res.SkippedDuplicate++
			continue
		}
		activeKeys[dk] = true
		_, storageFault := m.taskFaults.Load(eff.TaskID)
		candidates = append(candidates, retryCandidate{
			taskID:       eff.TaskID,
			storageFault: storageFault,
		})
	}
	if len(missingSet) > 0 {
		res.MissingAuthSites = make([]string, 0, len(missingSet))
		for s := range missingSet {
			res.MissingAuthSites = append(res.MissingAuthSites, s)
		}
		sort.Strings(res.MissingAuthSites)
	}

	var retriedIDs []string
	if len(candidates) > 0 {
		tx, txErr := m.db.Begin()
		if txErr != nil {
			m.mu.Unlock()
			return BatchRetryResult{}, txErr
		}
		now := time.Now().UTC().Format(time.RFC3339)
		var updated []retryCandidate
		for _, cand := range candidates {
			execRes, execErr := tx.Exec(`
				UPDATE download_tasks
				SET status = 'queued', pause_reason = '', error_kind = '', error_message = '', updated_at = ?
				WHERE task_id = ?
				  AND (
					status IN ('failed', 'partial')
					OR ((status = 'paused' OR status LIKE 'paused_%') AND COALESCE(TRIM(pause_reason), '') != 'user')
					OR (? AND status = 'running')
				  )
			`, now, cand.taskID, cand.storageFault)
			if execErr != nil {
				_ = tx.Rollback()
				m.mu.Unlock()
				return BatchRetryResult{}, execErr
			}
			if n, _ := execRes.RowsAffected(); n > 0 {
				updated = append(updated, cand)
				retriedIDs = append(retriedIDs, cand.taskID)
			}
		}
		if commitErr := tx.Commit(); commitErr != nil {
			m.mu.Unlock()
			return BatchRetryResult{}, commitErr
		}
		for _, cand := range updated {
			if cand.storageFault {
				if cancel := m.activeRun[cand.taskID]; cancel != nil {
					cancel()
				}
			}
			delete(m.initialPlans, cand.taskID)
			m.taskFaults.Delete(cand.taskID)
		}
		res.Retried = len(retriedIDs)
	}
	m.mu.Unlock()

	if len(retriedIDs) > 0 {
		m.emitBatchTasks("retry_batch", retriedIDs)
		m.notifyWake()
	}
	return res, nil
}

// Cancel stops a task, marks it "canceled", and cleans up any incomplete .part files.
func (m *Manager) Cancel(taskID string) (Task, error) {
	m.mu.Lock()
	delete(m.initialPlans, taskID)
	if cancel, ok := m.activeRun[taskID]; ok {
		cancel()
	}
	_, stillRunning := m.activeRun[taskID]
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := m.db.Exec(`
		UPDATE download_tasks
		SET status = 'canceled', updated_at = ?
		WHERE task_id = ? AND status NOT IN ('completed', 'canceled')
	`, now, taskID)
	m.mu.Unlock()
	if err != nil {
		return Task{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		t, _, _ := m.GetTask(taskID)
		return t, fmt.Errorf("任务已结束，无需取消")
	}
	m.taskFaults.Delete(taskID)
	t, _, err := m.GetTask(taskID)
	if !stillRunning {
		cleanTaskPartFiles(t)
	}
	m.emitUpdate(t)
	m.notifyWake()
	return t, err
}

// Remove deletes a finished task record (completed, canceled, failed or
// partial). Cached files and the library entry are never touched; a failed
// task's unfinished temporary files are cleaned like a cancel.
func (m *Manager) Remove(taskID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, running := m.activeRun[taskID]; running {
		return fmt.Errorf("任务仍在进行，请先暂停或取消")
	}
	t, ok, err := m.GetTask(taskID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	switch t.Status {
	case "completed", "canceled", "failed", "partial":
	default:
		return fmt.Errorf("只有已结束的任务可以移除")
	}
	if t.Status == "failed" || t.Status == "partial" {
		cleanTaskPartFiles(t)
	}
	tx, err := m.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM task_attempts WHERE task_id = ?`, taskID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM download_tasks WHERE task_id = ?`, taskID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	delete(m.initialPlans, taskID)
	m.taskFaults.Delete(taskID)
	m.notifyMu.Lock()
	delete(m.lastNotify, taskID)
	m.notifyMu.Unlock()
	return nil
}

// RemoveFinished removes every completed or canceled task record and returns the count.
func (m *Manager) RemoveFinished() (int, error) {
	m.mu.Lock()
	rows, err := m.db.Query(`SELECT task_id FROM download_tasks WHERE status IN ('completed', 'canceled')`)
	if err != nil {
		m.mu.Unlock()
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			m.mu.Unlock()
			return 0, err
		}
		ids = append(ids, id)
	}
	err = errors.Join(rows.Err(), rows.Close())
	m.mu.Unlock()
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := m.Remove(id); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

func (m *Manager) notifyWake() {
	select {
	case m.wakeCh <- struct{}{}:
	default:
	}
}

func (m *Manager) dispatchLoop() {
	defer m.wg.Done()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-m.wakeCh:
			m.launchAvailableWorkers()
		}
	}
}

func (m *Manager) launchAvailableWorkers() {
	for {
		m.mu.Lock()
		if m.ctx == nil || m.ctx.Err() != nil {
			m.mu.Unlock()
			return
		}
		maxConc := m.dlSettings.MaxConcurrentTasks
		if maxConc < 1 {
			maxConc = 2
		}
		if len(m.activeRun) >= maxConc {
			m.mu.Unlock()
			return
		}

		nextTask, ok := m.claimNextQueuedLocked()
		if !ok {
			m.mu.Unlock()
			return
		}

		taskCtx, taskCancel := context.WithCancel(m.ctx)
		m.activeRun[nextTask.TaskID] = taskCancel
		m.activeSite[nextTask.TaskID] = nextTask.Site
		m.wg.Add(1)
		m.mu.Unlock()

		m.emitUpdate(nextTask)
		go func(t Task, ctx context.Context, cancel context.CancelFunc) {
			defer m.wg.Done()
			defer cancel()
			m.runTask(ctx, t)
			if latest, ok, err := m.GetTask(t.TaskID); err == nil && ok && latest.Status == "canceled" {
				cleanTaskPartFiles(latest)
			}
			m.mu.Lock()
			delete(m.activeRun, t.TaskID)
			delete(m.activeSite, t.TaskID)
			delete(m.initialPlans, t.TaskID)
			m.mu.Unlock()
			m.notifyWake()
		}(nextTask, taskCtx, taskCancel)
	}
}

func (m *Manager) runTask(ctx context.Context, t Task) {
	if t.Source == "auto" {
		ctx = site.WithAutomaticRequests(ctx, "download")
	}
	var err error
	switch site.ID(strings.ToLower(strings.TrimSpace(t.Site))) {
	case site.Bilibili:
		switch t.Kind {
		case "video":
			err = m.executeVideoTask(ctx, &t)
		case "dynamic":
			err = m.executeDynamicTask(ctx, &t)
		case "supplement":
			err = m.executeSupplementTask(ctx, &t)
		case "upgrade":
			err = m.executeUpgradeTask(ctx, &t)
		default:
			err = fmt.Errorf("unsupported download task kind %q", t.Kind)
		}
	case site.Douyin:
		err = m.executeDouyinTask(ctx, &t)
	case site.Weibo:
		err = m.executeWeiboTask(ctx, &t)
	case site.X:
		err = m.executeXTask(ctx, &t)
	default:
		err = &site.Error{
			Site:    site.ID(strings.TrimSpace(t.Site)),
			Kind:    site.ErrKindBadRequest,
			Message: fmt.Sprintf("不支持的下载站点: %q", t.Site),
		}
	}

	if ctx.Err() != nil {
		// Task was paused, canceled, or Manager stopped; preserve the state set by Pause/Cancel.
		return
	}

	if err != nil {
		m.reportSiteAutomaticError(ctx, t.Site, err)
		var netPause *networkPauseError
		if errors.As(err, &netPause) {
			t.Status = "paused"
			t.PauseReason = "network"
			t.ErrorKind = site.ErrKindNetwork
			t.ErrorMessage = netPause.Error()
			m.persistTaskProgress(t)
			return
		}
		errKind := classifyDownloadError(err)
		if errKind == site.ErrKindAuthRequired {
			t.Status, t.PauseReason = "paused", "auth_required"
			t.ErrorKind = errKind
			t.ErrorMessage = "已暂停（登录已失效）：" + err.Error()
			m.persistTaskProgress(t)
			m.pauseSitePeers(t.Site, t.TaskID, "auth_required", errKind, "已暂停（登录已失效）："+err.Error())
			return
		}
		if errKind == site.ErrKindQuota || errKind == site.ErrKindBackoff {
			t.Status, t.PauseReason = "paused", errKind
		}
		if (errKind == site.ErrKindUnavailable || errKind == site.ErrKindBlocked) && t.Site == string(site.Bilibili) {
			t.Status = "partial"
		}
		// Risk control or rate limit pauses this task and queued/running tasks of the same site only (data-and-cache.md §2.4).
		if errKind == site.ErrKindRiskControl || errKind == site.ErrKindRateLimited {
			t.Status = "paused"
			t.PauseReason = "risk_control"
			t.ErrorKind = errKind
			t.ErrorMessage = "已暂停（风控）：" + err.Error()
			m.persistTaskProgress(t)
			msg := fmt.Sprintf("检测到站点风控或频率限制 (%s)，已自动暂停同站点排队任务: %s", errKind, err.Error())
			m.pauseSitePeers(t.Site, t.TaskID, "risk_control", errKind, msg)
			return
		}
		if t.Status == "paused" {
			if t.ErrorKind == "" {
				t.ErrorKind = errKind
			}
			if t.ErrorMessage == "" {
				t.ErrorMessage = err.Error()
			}
			m.persistTaskProgress(t)
			return
		}
		if t.Status != "partial" {
			t.Status = "failed"
		}
		t.ErrorKind = errKind
		t.ErrorMessage = err.Error()
		m.persistTaskProgress(t)
		return
	}

	if t.Status != "partial" {
		t.Status = "completed"
		t.ErrorKind = ""
		t.ErrorMessage = ""
	}
	t.PauseReason = ""
	t.Progress = 1.0
	m.persistTaskProgress(t)
}

func (m *Manager) pauseSitePeers(siteID, triggeringTaskID, pauseReason, errKind, msg string) {
	siteID = strings.ToLower(strings.TrimSpace(siteID))
	if siteID == "" {
		return
	}
	m.mu.Lock()
	for id, fn := range m.activeRun {
		if id != triggeringTaskID && m.activeSite[id] == siteID {
			delete(m.initialPlans, id)
			fn()
		}
	}
	ids, err := m.pauseTasksWhereLocked(
		pauseReason,
		errKind,
		msg,
		`site = ? AND (status = 'queued' OR (status = 'running' AND task_id != ?))`,
		siteID, triggeringTaskID,
	)
	m.mu.Unlock()
	if err == nil {
		m.emitBatchTasks("pause_site", ids)
	}
}
