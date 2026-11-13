package download

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	jitterrand "math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
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
	TaskID            string             `json:"taskId"`
	Site              string             `json:"site"`
	Kind              string             `json:"kind"`             // "video", "dynamic", "supplement", "upgrade"
	Source            string             `json:"source,omitempty"` // "manual" or "auto"
	TargetID          string             `json:"targetId"`
	CID               int64              `json:"cid,omitempty"`
	Page              int                `json:"page,omitempty"`
	Title             string             `json:"title"`
	PartTitle         string             `json:"partTitle,omitempty"`
	Mid               int64              `json:"mid,omitempty"`
	AuthorUID         string             `json:"authorUid,omitempty"`
	AuthorName        string             `json:"authorName,omitempty"`
	AuthorFace        string             `json:"authorFace,omitempty"`
	CoverURL          string             `json:"coverUrl,omitempty"`
	CacheLinkedVideos bool               `json:"cacheLinkedVideos,omitempty"`
	LinkedVideo       bool               `json:"-"` // a video cached as part of a dynamic
	ProgressOwner     *Task              `json:"-"`
	ProgressPrefix    string             `json:"-"`
	Status            string             `json:"status"` // "queued", "running", "paused", "completed", "failed", "canceled", "partial"
	PauseReason       string             `json:"pauseReason,omitempty"`
	ErrorKind         string             `json:"errorKind,omitempty"`
	ErrorMessage      string             `json:"errorMessage,omitempty"`
	LastError         string             `json:"lastError,omitempty"`
	QualityHint       string             `json:"qualityHint,omitempty"`
	BytesDownloaded   int64              `json:"bytesDownloaded"`
	TotalBytes        int64              `json:"totalBytes"`
	Progress          float64            `json:"progress"`
	ItemKey           string             `json:"itemKey,omitempty"`
	ItemDir           string             `json:"itemDir,omitempty"`
	Files             []TaskFileProgress `json:"files,omitempty"`
	Attempts          []TaskAttempt      `json:"attempts,omitempty"`
	CreatedAt         string             `json:"createdAt"`
	UpdatedAt         string             `json:"updatedAt"`
}

// VideoEnqueueRequest specifies a video (and optional page subset) to cache.
type VideoEnqueueRequest struct {
	BVID   string                `json:"bvid"`
	Pages  []int                 `json:"pages,omitempty"` // 1-based page numbers; empty means all pages (or first page if MultiPart=="first")
	Source string                `json:"source,omitempty"`
	Detail *bilibili.VideoDetail `json:"-"`
}

// DynamicEnqueueRequest specifies a dynamic post to cache.
type DynamicEnqueueRequest struct {
	DynamicID         string                `json:"dynamicId"`
	CacheLinkedVideos bool                  `json:"cacheLinkedVideos"`
	Source            string                `json:"source,omitempty"`
	Item              *bilibili.DynamicItem `json:"-"`
}

// Manager coordinates queued and active download tasks with concurrency limits and circuit breaking.
type Manager struct {
	mu           sync.Mutex
	videoLocks   sync.Map
	db           *sql.DB
	lib          *library.Store
	client       *bilibili.Client
	dlSettings   settings.DownloadSettings
	onTaskUpdate func(Task)
	notifyMu     sync.Mutex
	lastNotify   map[string]taskNotification
	taskFaults   sync.Map

	chunkSize    int64
	backoffScale time.Duration
	mediaWrap    func(http.RoundTripper) http.RoundTripper

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
		db:           db,
		lib:          lib,
		client:       client,
		dlSettings:   dlSettings,
		onTaskUpdate: onUpdate,
		lastNotify:   make(map[string]taskNotification),
		chunkSize:    DefaultRangeChunkSize,
		backoffScale: time.Second,
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

func (m *Manager) initSchema() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS download_tasks (
			task_id TEXT PRIMARY KEY,
			site TEXT NOT NULL,
			kind TEXT NOT NULL,
			source TEXT NOT NULL DEFAULT 'manual',
			target_id TEXT NOT NULL,
			cid INTEGER NOT NULL DEFAULT 0,
			page INTEGER NOT NULL DEFAULT 1,
			title TEXT NOT NULL DEFAULT '',
			part_title TEXT NOT NULL DEFAULT '',
			mid INTEGER NOT NULL DEFAULT 0,
			author_uid TEXT NOT NULL DEFAULT '',
			author_name TEXT NOT NULL DEFAULT '',
			author_face TEXT NOT NULL DEFAULT '',
			cover_url TEXT NOT NULL DEFAULT '',
			cache_linked_videos INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'queued',
			pause_reason TEXT NOT NULL DEFAULT '',
			error_kind TEXT NOT NULL DEFAULT '',
			error_message TEXT NOT NULL DEFAULT '',
			quality_hint TEXT NOT NULL DEFAULT '',
			bytes_downloaded INTEGER NOT NULL DEFAULT 0,
			total_bytes INTEGER NOT NULL DEFAULT 0,
			progress REAL NOT NULL DEFAULT 0,
			item_key TEXT NOT NULL DEFAULT '',
			item_dir TEXT NOT NULL DEFAULT '',
			files_json TEXT NOT NULL DEFAULT '[]',
			attempts_json TEXT NOT NULL DEFAULT '[]',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS task_attempts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id TEXT NOT NULL,
			attempt_time TEXT NOT NULL,
			file_path TEXT NOT NULL,
			offset_bytes INTEGER NOT NULL DEFAULT 0,
			host TEXT NOT NULL DEFAULT '',
			attempt_num INTEGER NOT NULL DEFAULT 1,
			error_kind TEXT NOT NULL DEFAULT '',
			http_status INTEGER NOT NULL DEFAULT 0
		);`,
	}
	for _, q := range stmts {
		if _, err := m.db.Exec(q); err != nil {
			return err
		}
	}
	// Ensure columns exist on databases created before schema additions.
	for _, alter := range []string{
		`ALTER TABLE download_tasks ADD COLUMN source TEXT NOT NULL DEFAULT 'manual'`,
		`ALTER TABLE download_tasks ADD COLUMN author_uid TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE download_tasks ADD COLUMN pause_reason TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE download_tasks ADD COLUMN files_json TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE download_tasks ADD COLUMN attempts_json TEXT NOT NULL DEFAULT '[]'`,
	} {
		_, _ = m.db.Exec(alter)
	}
	return nil
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

// Stop cancels all active downloads and waits for background workers to exit.
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
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := m.db.Exec(`
		UPDATE download_tasks
		SET status = 'paused', pause_reason = ?, error_message = ?, updated_at = ?
		WHERE status IN ('queued', 'running')
	`, reason, message, now)
	if err == nil {
		m.taskFaults.Range(func(key, _ any) bool { m.taskFaults.Delete(key); return true })
	}
	m.mu.Unlock()
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
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := m.db.Exec(`
		UPDATE download_tasks
		SET status = 'paused', pause_reason = ?, error_message = ?, updated_at = ?
		WHERE site = ? AND status IN ('queued', 'running')
	`, reason, message, now, targetSite)
	if err == nil {
		for _, id := range pausedIDs {
			m.taskFaults.Delete(id)
		}
	}
	m.mu.Unlock()
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

func (m *Manager) enqueueSingleTask(in Task) (Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if in.Site == "" {
		in.Site = "bilibili"
	}
	if in.AuthorUID == "" && in.Mid > 0 && in.Site == "bilibili" {
		in.AuthorUID = strconv.FormatInt(in.Mid, 10)
	}

	// Deduplicate if an identical task for the same site is already queued or running.
	existing, ok, err := m.findActiveOrQueuedLocked(in.Site, in.Kind, in.TargetID, in.CID)
	if err != nil {
		return Task{}, err
	}
	if ok {
		return existing, nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	var rnd [6]byte
	_, _ = rand.Read(rnd[:])
	in.TaskID = "dl-" + hex.EncodeToString(rnd[:])
	in.Status = "queued"
	if in.Source == "" {
		in.Source = "manual"
	}
	in.CreatedAt = now
	in.UpdatedAt = now

	linkedInt := 0
	if in.CacheLinkedVideos {
		linkedInt = 1
	}

	filesJSON, _ := json.Marshal(in.Files)
	attemptsJSON, _ := json.Marshal(in.Attempts)

	_, err = m.db.Exec(`
		INSERT INTO download_tasks (
			task_id, site, kind, source, target_id, cid, page, title, part_title,
			mid, author_uid, author_name, author_face, cover_url, cache_linked_videos,
			status, pause_reason, error_kind, error_message, quality_hint,
			bytes_downloaded, total_bytes, progress, item_key, item_dir,
			files_json, attempts_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', '', '', 0, 0, 0, ?, ?, ?, ?, ?, ?)
	`,
		in.TaskID, in.Site, in.Kind, in.Source, in.TargetID, in.CID, in.Page, in.Title, in.PartTitle,
		in.Mid, in.AuthorUID, in.AuthorName, in.AuthorFace, in.CoverURL, linkedInt,
		in.Status, in.ItemKey, in.ItemDir, string(filesJSON), string(attemptsJSON), in.CreatedAt, in.UpdatedAt,
	)
	if err != nil {
		return Task{}, err
	}
	return in, nil
}

func (m *Manager) findActiveOrQueuedLocked(siteID, kind, targetID string, cid int64) (Task, bool, error) {
	row := m.db.QueryRow(`
		SELECT
			task_id, site, kind, source, target_id, cid, page, title, part_title,
			mid, author_uid, author_name, author_face, cover_url, cache_linked_videos,
			status, pause_reason, error_kind, error_message, quality_hint,
			bytes_downloaded, total_bytes, progress, item_key, item_dir,
			files_json, attempts_json, created_at, updated_at
		FROM download_tasks
		WHERE site = ? AND kind = ? AND target_id = ? AND cid = ? AND status IN ('queued', 'running')
		LIMIT 1
	`, siteID, kind, targetID, cid)
	t, err := scanTask(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return Task{}, false, nil
		}
		return Task{}, false, err
	}
	return t, true, nil
}

// ListTasks returns active tasks first, then tasks needing action, then finished
// tasks, with the most recently updated task first within each group.
func (m *Manager) ListTasks() ([]Task, error) {
	rows, err := m.db.Query(`
		SELECT
			task_id, site, kind, source, target_id, cid, page, title, part_title,
			mid, author_uid, author_name, author_face, cover_url, cache_linked_videos,
			status, pause_reason, error_kind, error_message, quality_hint,
			bytes_downloaded, total_bytes, progress, item_key, item_dir,
			files_json, attempts_json, created_at, updated_at
		FROM download_tasks
		ORDER BY CASE WHEN status IN ('running', 'queued') THEN 0 WHEN status IN ('paused', 'failed', 'partial') THEN 1 ELSE 2 END,
			julianday(updated_at) DESC, rowid DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m.withTaskFault(t))
	}
	return out, rows.Err()
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

// GetTask retrieves a single task by taskID.
func (m *Manager) GetTask(taskID string) (Task, bool, error) {
	row := m.db.QueryRow(`
		SELECT
			task_id, site, kind, source, target_id, cid, page, title, part_title,
			mid, author_uid, author_name, author_face, cover_url, cache_linked_videos,
			status, pause_reason, error_kind, error_message, quality_hint,
			bytes_downloaded, total_bytes, progress, item_key, item_dir,
			files_json, attempts_json, created_at, updated_at
		FROM download_tasks
		WHERE task_id = ?
	`, taskID)
	t, err := scanTask(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return Task{}, false, nil
		}
		return Task{}, false, err
	}
	return m.withTaskFault(t), true, nil
}

// TaskDetail returns the task along with its per-file progress manifest and desensitized attempt log.
func (m *Manager) TaskDetail(taskID string) (TaskDetail, bool, error) {
	t, ok, err := m.GetTask(taskID)
	if err != nil || !ok {
		return TaskDetail{}, ok, err
	}
	return TaskDetail{
		Task:     t,
		Files:    t.Files,
		Attempts: t.Attempts,
	}, true, nil
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
	m.taskFaults.Delete(taskID)
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
		go func(t Task, ctx context.Context) {
			defer m.wg.Done()
			m.runTask(ctx, t)
			if latest, ok, err := m.GetTask(t.TaskID); err == nil && ok && latest.Status == "canceled" {
				cleanTaskPartFiles(latest)
			}
			m.mu.Lock()
			delete(m.activeRun, t.TaskID)
			delete(m.activeSite, t.TaskID)
			m.mu.Unlock()
			m.notifyWake()
		}(nextTask, taskCtx)
	}
}

func (m *Manager) claimNextQueuedLocked() (Task, bool) {
	query := `
		SELECT
			task_id, site, kind, source, target_id, cid, page, title, part_title,
			mid, author_uid, author_name, author_face, cover_url, cache_linked_videos,
			status, pause_reason, error_kind, error_message, quality_hint,
			bytes_downloaded, total_bytes, progress, item_key, item_dir,
			files_json, attempts_json, created_at, updated_at
		FROM download_tasks
		WHERE status = 'queued'`
	var args []any
	if len(m.activeRun) > 0 {
		query += " AND task_id NOT IN (" + strings.TrimSuffix(strings.Repeat("?,", len(m.activeRun)), ",") + ")"
		for id := range m.activeRun {
			args = append(args, id)
		}
	}
	query += " ORDER BY rowid ASC LIMIT 1"
	row := m.db.QueryRow(query, args...)
	t, err := scanTask(row)
	if err != nil {
		return Task{}, false
	}
	now := time.Now().UTC().Format(time.RFC3339)
	t.Status = "running"
	t.PauseReason = ""
	t.ErrorKind = ""
	t.ErrorMessage = ""
	t.UpdatedAt = now
	_, _ = m.db.Exec(`UPDATE download_tasks SET status = 'running', pause_reason = '', error_kind = '', error_message = '', updated_at = ? WHERE task_id = ?`, now, t.TaskID)
	return t, true
}

func (m *Manager) runTask(ctx context.Context, t Task) {
	if t.Source == "auto" {
		ctx = bilibili.WithAutomaticRequests(ctx, "download")
	}
	var err error
	if t.Site == string(site.Douyin) {
		err = m.executeDouyinTask(ctx, &t)
	} else {
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
	}

	if ctx.Err() != nil {
		// Task was paused, canceled, or Manager stopped; preserve the state set by Pause/Cancel.
		return
	}

	if err != nil {
		m.reportSiteAutomaticError(ctx, t.Site, err)
		errKind := classifyDownloadError(err)
		if errKind == bilibili.ErrKindAuthRequired {
			t.Status, t.PauseReason = "paused", "auth_required"
			t.ErrorKind = errKind
			t.ErrorMessage = "已暂停（登录已失效）：" + err.Error()
			m.persistTaskProgress(t)
			m.pauseQueuedForSite(t.Site, "auth_required", errKind, "已暂停（登录已失效）："+err.Error())
			return
		}
		if errKind == bilibili.ErrKindQuota || errKind == bilibili.ErrKindBackoff {
			t.Status, t.PauseReason = "paused", errKind
		}
		if (errKind == bilibili.ErrKindUnavailable || errKind == bilibili.ErrKindBlocked) && t.Site != string(site.Douyin) {
			t.Status = "partial"
		}
		// Risk control or rate limit pauses this task and queued tasks of the same site only (data-and-cache.md §2.4).
		if errKind == bilibili.ErrKindRiskControl || errKind == bilibili.ErrKindRateLimited {
			t.Status = "paused"
			t.PauseReason = "risk_control"
			t.ErrorKind = errKind
			t.ErrorMessage = "已暂停（风控）：" + err.Error()
			m.persistTaskProgress(t)
			m.tripSiteCircuitBreaker(t.Site, t.TaskID, errKind, err.Error())
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
	}
	t.PauseReason = ""
	t.Progress = 1.0
	t.ErrorKind = ""
	t.ErrorMessage = ""
	m.persistTaskProgress(t)
}

func (m *Manager) tripSiteCircuitBreaker(siteID, triggeringTaskID, errKind, reason string) {
	if siteID == "" {
		siteID = string(site.Bilibili)
	}
	m.mu.Lock()
	now := time.Now().UTC().Format(time.RFC3339)
	msg := fmt.Sprintf("检测到站点风控或频率限制 (%s)，已自动暂停同站点排队任务: %s", errKind, reason)
	for id, fn := range m.activeRun {
		if id != triggeringTaskID && m.activeSite[id] == siteID {
			delete(m.initialPlans, id)
			fn()
		}
	}
	_, _ = m.db.Exec(`
		UPDATE download_tasks
		SET status = 'paused', pause_reason = 'risk_control', error_kind = ?, error_message = ?, updated_at = ?
		WHERE site = ? AND (status = 'queued' OR (status = 'running' AND task_id != ?))
	`, errKind, msg, now, siteID, triggeringTaskID)
	m.mu.Unlock()
}

func (m *Manager) pauseQueuedForSite(siteID, pauseReason, errKind, msg string) {
	if siteID == "" {
		siteID = string(site.Bilibili)
	}
	m.mu.Lock()
	now := time.Now().UTC().Format(time.RFC3339)
	_, _ = m.db.Exec(`
		UPDATE download_tasks
		SET status = 'paused', pause_reason = ?, error_kind = ?, error_message = ?, updated_at = ?
		WHERE site = ? AND status = 'queued'
	`, pauseReason, errKind, msg, now, siteID)
	m.mu.Unlock()
}

func (m *Manager) setFileProgress(t *Task, role, relPath, status string, downloaded, total, speed int64) {
	found := false
	for i := range t.Files {
		if t.Files[i].Path == relPath {
			t.Files[i].Role = role
			t.Files[i].Status = status
			t.Files[i].DownloadedBytes = downloaded
			if total > 0 {
				t.Files[i].TotalBytes = total
			}
			t.Files[i].SpeedBps = speed
			found = true
			break
		}
	}
	if !found {
		t.Files = append(t.Files, TaskFileProgress{
			Role:            role,
			Path:            relPath,
			Status:          status,
			DownloadedBytes: downloaded,
			TotalBytes:      total,
			SpeedBps:        speed,
		})
	}
}

func (m *Manager) recordAttempt(t *Task, relFile string, offset int64, rawURL string, attempt int, errKind string, httpStatus int) {
	host := ""
	if u, err := url.Parse(rawURL); err == nil {
		host = u.Hostname()
	}
	entry := TaskAttempt{
		Time:       time.Now().UTC().Format(time.RFC3339),
		File:       relFile,
		Offset:     offset,
		Host:       host,
		Attempt:    attempt,
		ErrorKind:  errKind,
		HTTPStatus: httpStatus,
	}
	t.Attempts = append(t.Attempts, entry)
	if len(t.Attempts) > 200 {
		t.Attempts = t.Attempts[len(t.Attempts)-200:]
	}
	if t.TaskID != "" {
		_, _ = m.db.Exec(`
			INSERT INTO task_attempts (task_id, attempt_time, file_path, offset_bytes, host, attempt_num, error_kind, http_status)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, t.TaskID, entry.Time, entry.File, entry.Offset, entry.Host, entry.Attempt, entry.ErrorKind, entry.HTTPStatus)
	}
}

func (m *Manager) executeDynamicTask(ctx context.Context, t *Task) (result error) {
	dyn, err := m.client.DynamicDetail(ctx, t.TargetID)
	if err != nil {
		return err
	}

	for origin := dyn.Origin; origin != nil; origin = origin.Origin {
		dyn.Images = append(dyn.Images, origin.Images...)
		dyn.Videos = append(dyn.Videos, origin.Videos...)
	}
	t.Title = dyn.Title
	t.Mid = dyn.Author.Mid
	t.AuthorName = dyn.Author.Name
	t.AuthorFace = dyn.Author.Face

	cacheRoot := m.lib.CacheDir()
	itemDir, err := library.EnsureWithinRoot(cacheRoot, filepath.Join(cacheRoot, "bilibili", strconv.FormatInt(dyn.Author.Mid, 10), "dynamics", dyn.ID))
	if err != nil {
		return err
	}
	previous, exists, err := m.lib.GetItem("dynamic:" + dyn.ID)
	if err != nil {
		return err
	}
	if exists {
		itemDir = previous.ItemDir
	} else if t.ItemDir != "" {
		itemDir = t.ItemDir
	}
	if err := os.MkdirAll(itemDir, 0o700); err != nil {
		return err
	}
	t.ItemDir = itemDir

	for idx, img := range dyn.Images {
		ext := guessImageExt(img.URL)
		relName := fmt.Sprintf("images/%02d%s", idx+1, ext)
		m.setFileProgress(t, "image", relName, "waiting", 0, 0, 0)
		if img.LiveURL != "" {
			m.setFileProgress(t, "motion", motionRelName(idx, img.LiveURL), "waiting", 0, 0, 0)
		}
	}
	m.persistTaskProgress(*t)

	var mediaFiles []library.MediaFile
	var partialErrors []string
	var videoRefs []string
	finalSaved := false
	for _, video := range dyn.Videos {
		if video.BVID != "" {
			videoRefs = append(videoRefs, "video:"+video.BVID)
		}
	}
	t.ItemKey = library.CanonicalItemKey("bilibili", "dynamic", dyn.ID)
	defer func() {
		if result == nil || finalSaved {
			return
		}
		// An interruption must retain already completed files and their hashes.
		missing := []library.MissingRecord{{Item: "dynamic", Reason: "动态缓存尚未完成 (" + bilibili.KindOf(result) + ")"}}
		meta := library.ItemMetadata{Version: 1, ID: "dynamic:" + dyn.ID, Site: "bilibili", Kind: "dynamic", Status: "partial", Title: dyn.Title, Text: dyn.Text, DynamicType: dyn.Type, TypeLabel: dyn.TypeLabel, URL: dyn.CanonicalURL, PubTs: dyn.PubTs, CachedAt: previous.CachedAt, Author: library.ItemAuthor{Mid: dyn.Author.Mid, Name: dyn.Author.Name, Face: dyn.Author.Face}, Media: mergeMediaFiles(previous.Media, mediaFiles), Videos: videoRefs, Missing: missing}
		if _, err := m.lib.SaveItem(itemDir, meta); err != nil {
			result = err
		}
	}()

	// Save orig.json snapshot if this dynamic is a forward post (data-and-cache.md §2.1 & §2.3).
	if dyn.Origin != nil {
		if origBytes, mErr := json.MarshalIndent(dyn.Origin, "", "  "); mErr == nil {
			if err := m.claimTaskFile(t, itemDir, "orig.json", filepath.Join(itemDir, "orig.json")); err != nil {
				return err
			}
			if err := storage.WriteFileAtomic(filepath.Join(itemDir, "orig.json"), origBytes, 0o600); err != nil {
				return err
			}
			mediaFiles = append(mediaFiles, library.MediaFile{Role: "snapshot", Path: "orig.json", Bytes: int64(len(origBytes))})
		}
	}

	for idx, img := range dyn.Images {
		ext := guessImageExt(img.URL)
		relName := fmt.Sprintf("images/%02d%s", idx+1, ext)
		// Existing manifests retain their registered names and location.
		if exists {
			for _, media := range previous.Media {
				if media.Role == "image" && media.Source == img.URL {
					relName = media.Path
					break
				}
			}
		}
		fullPath := filepath.Join(itemDir, relName)

		// If image already exists on disk from an earlier partial attempt, reuse it without re-downloading.
		if mediaIsValid(previous, relName) {
			info, statErr := os.Stat(fullPath)
			if statErr != nil {
				return statErr
			}
			m.setFileProgress(t, "image", relName, "completed", info.Size(), info.Size(), 0)
			for _, media := range previous.Media {
				if media.Path == relName {
					still := media
					motion, err := m.cacheLiveMotion(ctx, t, itemDir, previous, exists, idx, img.LiveURL, &partialErrors)
					if err != nil {
						return err
					}
					if motion != nil {
						still.Motion = motion.Path
						mediaFiles = append(mediaFiles, *motion)
					}
					mediaFiles = append(mediaFiles, still)
					break
				}
			}
			continue
		}

		m.setFileProgress(t, "image", relName, "downloading", 0, 0, 0)
		n, sha, dlErr := m.downloadFileWithRange(ctx, []string{img.URL}, fullPath, t)
		if dlErr != nil {
			m.setFileProgress(t, "image", relName, "failed", 0, 0, 0)
			if isStopError(dlErr) || isStorageError(dlErr) || ctx.Err() != nil {
				return dlErr
			}
			partialErrors = append(partialErrors, fmt.Sprintf("img_%02d: %v", idx+1, dlErr))
			continue
		}
		m.setFileProgress(t, "image", relName, "completed", n, n, 0)
		still := library.MediaFile{
			Role:   "image",
			Path:   relName,
			Bytes:  n,
			SHA256: sha,
			Source: img.URL,
			Width:  img.Width,
			Height: img.Height,
		}
		motion, err := m.cacheLiveMotion(ctx, t, itemDir, previous, exists, idx, img.LiveURL, &partialErrors)
		if err != nil {
			return err
		}
		if motion != nil {
			still.Motion = motion.Path
			mediaFiles = append(mediaFiles, *motion)
		}
		mediaFiles = append(mediaFiles, still)
	}

	for _, v := range dyn.Videos {
		if v.BVID != "" {
			if t.CacheLinkedVideos {
				detail, err := m.client.VideoDetail(ctx, v.BVID)
				if err != nil {
					return err
				}
				for _, page := range detail.Pages {
					vidTask := Task{Site: "bilibili", Kind: "video", TargetID: v.BVID, CID: page.CID, Page: page.Page, Source: t.Source, LinkedVideo: true, ProgressOwner: t, ProgressPrefix: "videos/" + v.BVID + "/", Attempts: t.Attempts}
					if err := m.executeVideoTask(ctx, &vidTask); err != nil {
						if bilibili.IsKind(err, bilibili.ErrKindRiskControl) || bilibili.IsKind(err, bilibili.ErrKindRateLimited) || bilibili.IsKind(err, bilibili.ErrKindAuthRequired) {
							return err
						}
						partialErrors = append(partialErrors, fmt.Sprintf("video %s P%d: %v", v.BVID, page.Page, err))
					}
					if vidTask.Status == "partial" {
						partialErrors = append(partialErrors, fmt.Sprintf("video %s P%d: 部分缓存", v.BVID, page.Page))
					}
				}
			}
		}
	}

	status := "complete"
	var missing []library.MissingRecord
	if dyn.Degraded {
		status = "partial"
		t.Status = "partial"
		missing = append(missing, library.MissingRecord{Item: "dynamic", Reason: fmt.Sprintf("类型不支持 (%s)", dyn.TypeLabel)})
	}
	if len(partialErrors) > 0 {
		status = "partial"
		t.Status = "partial"
		for _, pe := range partialErrors {
			missing = append(missing, library.MissingRecord{Item: "media", Reason: pe})
		}
	}

	meta := library.ItemMetadata{
		Version:     1,
		ID:          "dynamic:" + dyn.ID,
		Site:        "bilibili",
		Kind:        "dynamic",
		Status:      status,
		Title:       dyn.Title,
		Text:        dyn.Text,
		DynamicType: dyn.Type,
		TypeLabel:   dyn.TypeLabel,
		URL:         dyn.CanonicalURL,
		PubTs:       dyn.PubTs,
		CachedAt:    previous.CachedAt,
		Author: library.ItemAuthor{
			Mid:  dyn.Author.Mid,
			Name: dyn.Author.Name,
			Face: dyn.Author.Face,
		},
		Media:   mediaFiles,
		Videos:  videoRefs,
		Missing: missing,
	}

	savedMeta, saveErr := m.lib.SaveItem(itemDir, meta)
	if saveErr != nil {
		return saveErr
	}
	t.ItemKey = savedMeta.ItemKey
	finalSaved = true

	if len(partialErrors) > 0 {
		return fmt.Errorf("部分媒体下载失败 (%s)", strings.Join(partialErrors, "; "))
	}
	return nil
}

func (m *Manager) downloadStreamWithRefresh(
	ctx context.Context,
	urls []string,
	finalPath string,
	t *Task,
	refreshURLs func() ([]string, error),
) (int64, string, error) {
	n, sha, err := m.downloadFileWithRange(ctx, urls, finalPath, t)
	if err == nil {
		return n, sha, nil
	}
	// If all URLs expired (403, 404, 410), refresh PlayURL at most once and resume from byte offset (data-and-cache.md §2.4).
	if refreshURLs != nil && (bilibili.IsKind(err, bilibili.ErrKindForbidden) || bilibili.IsKind(err, bilibili.ErrKindUnavailable)) {
		freshURLs, rErr := refreshURLs()
		if rErr != nil {
			if bilibili.IsKind(rErr, bilibili.ErrKindRiskControl) || bilibili.IsKind(rErr, bilibili.ErrKindRateLimited) {
				return 0, "", rErr
			}
			return 0, "", err
		}
		if len(freshURLs) > 0 {
			return m.downloadFileWithRange(ctx, freshURLs, finalPath, t)
		}
	}
	return 0, "", err
}

// downloadFileWithRange downloads from candidate URLs into finalPath via <finalPath>.part,
// using 8 MiB closed-interval Range requests, per-segment retry with exponential backoff + URL rotation,
// and Content-Range verification (data-and-cache.md §2.4).
func (m *Manager) downloadFileWithRange(ctx context.Context, urls []string, finalPath string, t *Task) (int64, string, error) {
	var cleanURLs []string
	rejectedNodes := 0
	for _, u := range urls {
		if u = strings.TrimSpace(u); u == "" {
			continue
		}
		// Third-party MCDN/PCDN nodes outside the media allowlist are skipped
		// up front instead of being recorded as failed attempts.
		if m.client != nil && m.client.ValidateMediaURL(u) != nil {
			rejectedNodes++
			continue
		}
		cleanURLs = append(cleanURLs, u)
	}
	if len(cleanURLs) == 0 && rejectedNodes > 0 {
		return 0, "", &bilibili.SiteError{
			Kind:    bilibili.ErrKindUnavailable,
			Message: "站点只返回了不受支持的第三方媒体节点，请稍后重试",
		}
	}
	if len(cleanURLs) == 0 {
		return 0, "", &bilibili.SiteError{
			Kind:    bilibili.ErrKindUnavailable,
			Message: "下载地址列表为空",
		}
	}

	root, rel := filepath.Dir(finalPath), filepath.Base(finalPath)
	if t.ItemDir != "" {
		root = t.ItemDir
		var err error
		rel, err = filepath.Rel(root, finalPath)
		if err != nil {
			return 0, "", err
		}
		rel = filepath.ToSlash(rel)
	}
	if _, err := library.ResolveItemPath(root, rel); err != nil {
		return 0, "", err
	}
	if _, err := library.ResolveItemPath(root, rel+".part"); err != nil {
		return 0, "", err
	}
	if err := m.claimTaskFile(t, root, rel, finalPath); err != nil {
		return 0, "", err
	}
	// A nonempty file alone is not proof of completion. Reuse only a verified
	// checkpoint with its original length (and hash, when available).
	for _, file := range t.Files {
		if taskFileIdentity(file.Path) == taskFileIdentity(rel) && file.TotalBytes > 0 && library.VerifyMediaFile(root, library.MediaFile{Path: rel, Bytes: file.TotalBytes, SHA256: file.SHA256}, true) {
			sha, sz, err := hashFileSHA256(finalPath)
			return sz, sha, err
		}
	}

	if err := os.MkdirAll(filepath.Dir(finalPath), 0o700); err != nil {
		return 0, "", err
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
	m.mu.Unlock()

	partPath := finalPath + ".part"
	relFile := filepath.Base(finalPath)
	var offset int64
	if info, err := os.Stat(partPath); err == nil && !info.IsDir() {
		offset = info.Size()
	}

	var totalSize int64 = -1
	for _, file := range t.Files {
		if taskFileIdentity(file.Path) == taskFileIdentity(rel) && file.TotalBytes > 0 {
			totalSize = file.TotalBytes
			break
		}
	}
	if totalSize > 0 && offset > totalSize {
		if err := os.Truncate(partPath, 0); err != nil {
			return 0, "", err
		}
		offset = 0
	}
	urlIdx := 0
	segRetries := 0
	fullResets := 0
	var lastErr error

	for {
		if ctx.Err() != nil {
			return offset, "", ctx.Err()
		}
		if totalSize > 0 && offset == totalSize {
			break
		}

		targetURL := cleanURLs[urlIdx%len(cleanURLs)]
		endByte := offset + chunkSize - 1
		if totalSize > 0 && endByte >= totalSize {
			endByte = totalSize - 1
		}

		res, err := m.fetchSingleSegment(ctx, targetURL, partPath, offset, endByte, totalSize, t)
		if err == nil {
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
		errKind := bilibili.KindOf(err)
		httpStatus := 0
		if se, ok := err.(*bilibili.SiteError); ok {
			httpStatus = se.HTTPStatus
		}

		// Risk control (412 / -352 / -412) or rate limit (429): pause immediately without retry!
		if errKind == bilibili.ErrKindRiskControl || errKind == bilibili.ErrKindRateLimited || errKind == bilibili.ErrKindAuthRequired || errKind == bilibili.ErrKindSchemaChanged || isStorageError(err) {
			m.recordAttempt(t, relFile, offset, targetURL, segRetries+1, errKind, httpStatus)
			return offset, "", err
		}

		// Subsequent segment returned 200 OK instead of 206: truncate .part, rotate URL, and count towards 5-reset limit.
		if res.truncatedToZero {
			offset = 0
			totalSize = -1
			fullResets++
			urlIdx++
			m.recordAttempt(t, relFile, offset, targetURL, fullResets, bilibili.ErrKindNetwork, http.StatusOK)
			if fullResets >= 5 {
				t.Status = "paused"
				t.PauseReason = "network"
				t.ErrorKind = bilibili.ErrKindNetwork
				t.ErrorMessage = "已暂停（网络）：节点不支持分段续传且重试已达上限"
				return offset, "", lastErr
			}
			continue
		}

		// Expired/forbidden URL (403, 404, 410): rotate through remaining candidate URLs once without sleeping.
		if errKind == bilibili.ErrKindForbidden || errKind == bilibili.ErrKindUnavailable {
			segRetries++
			urlIdx++
			m.recordAttempt(t, relFile, offset, targetURL, segRetries, errKind, httpStatus)
			if segRetries >= len(cleanURLs) {
				return offset, "", lastErr
			}
			continue
		}

		// Transport error (premature EOF, timeout, reset, 5xx): keep written bytes, retry up to 5 times with exponential backoff and URL rotation.
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
			t.ErrorKind = bilibili.ErrKindNetwork
			t.ErrorMessage = "已暂停（网络）：分段重试已达上限"
			return offset, "", lastErr
		}

		// Exponential backoff: 1, 2, 4, 8, 16 units
		backoffUnits := int64(1 << (segRetries - 1))
		if backoffUnits > 16 {
			backoffUnits = 16
		}
		sleepDur := time.Duration(backoffUnits) * backoffScale
		sleepDur = time.Duration(float64(sleepDur) * (0.8 + 0.4*jitterrand.Float64()))
		select {
		case <-ctx.Done():
			return offset, "", ctx.Err()
		case <-time.After(sleepDur):
		}
	}

	shaHex, actualSize, err := hashFileSHA256(partPath)
	if err != nil {
		return offset, "", err
	}
	if totalSize > 0 && actualSize != totalSize {
		return actualSize, "", &bilibili.SiteError{
			Kind:    bilibili.ErrKindNetwork,
			Message: fmt.Sprintf("文件大小校验不一致: 实际 %d != 预期 %d", actualSize, totalSize),
		}
	}

	if err := os.Rename(partPath, finalPath); err != nil {
		return actualSize, "", err
	}
	for i := range t.Files {
		if taskFileIdentity(t.Files[i].Path) == taskFileIdentity(rel) {
			t.Files[i].DownloadedBytes, t.Files[i].TotalBytes = actualSize, actualSize
			t.Files[i].SHA256, t.Files[i].Status = shaHex, "completed"
		}
	}
	m.persistTaskProgress(*t)
	return actualSize, shaHex, nil
}

type segmentResult struct {
	newOffset       int64
	totalSize       int64
	done            bool
	truncatedToZero bool
}

func (m *Manager) fetchSingleSegment(
	ctx context.Context,
	targetURL string,
	partPath string,
	offset int64,
	endByte int64,
	knownTotal int64,
	t *Task,
) (segmentResult, error) {
	if err := m.client.ValidateMediaURL(targetURL); err != nil {
		return segmentResult{newOffset: offset}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return segmentResult{newOffset: offset}, err
	}
	req.Header.Set("User-Agent", bilibili.DefaultUserAgent)
	req.Header.Set("Referer", bilibili.DefaultReferer)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, endByte))

	mediaClient, _ := m.client.MediaHTTPClient()
	resp, err := mediaClient.Do(req)
	if err != nil {
		return segmentResult{newOffset: offset}, &bilibili.SiteError{
			Kind:     bilibili.ErrKindNetwork,
			Endpoint: targetURL,
			Message:  "媒体下载连接失败",
			Err:      err,
		}
	}
	defer resp.Body.Close()

	// Handle 416 Range Not Satisfiable when offset > 0 by truncating .part and restarting.
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && offset > 0 {
		_ = os.Truncate(partPath, 0)
		return segmentResult{newOffset: 0, truncatedToZero: true}, &bilibili.SiteError{
			Kind:       bilibili.ErrKindNetwork,
			HTTPStatus: resp.StatusCode,
			Message:    "Range 416，重置分段文件",
		}
	}

	if resp.StatusCode >= 400 {
		return segmentResult{newOffset: offset}, mapMediaHTTPStatus(resp.StatusCode, targetURL)
	}
	isImage := false
	relFile := taskRelativePath(t, strings.TrimSuffix(partPath, ".part"))
	for _, file := range t.Files {
		if taskFileIdentity(file.Path) == taskFileIdentity(relFile) && (file.Role == "image" || file.Role == "cover") {
			isImage = true
			break
		}
	}
	if isImage && !strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "image/") {
		return segmentResult{newOffset: offset}, &bilibili.SiteError{Kind: bilibili.ErrKindSchemaChanged, Message: "图片响应不是 image/*"}
	}

	var f *os.File
	var expectedSegLen int64 = -1
	totalSize := knownTotal
	doneAfterThis := false

	switch resp.StatusCode {
	case http.StatusPartialContent:
		cr := strings.TrimSpace(resp.Header.Get("Content-Range"))
		if cr != "" {
			start, end, total, ok := parseContentRange(cr)
			if !ok || start != offset || end < start || end > endByte || total <= end || (knownTotal > 0 && total != knownTotal) {
				return segmentResult{newOffset: offset}, &bilibili.SiteError{
					Kind:    bilibili.ErrKindNetwork,
					Message: fmt.Sprintf("Content-Range 校验失败: %q (offset=%d, knownTotal=%d)", cr, offset, knownTotal),
				}
			}
			if total > 0 {
				totalSize = total
			}
			if end >= start {
				expectedSegLen = end - start + 1
			}
			if resp.ContentLength >= 0 && resp.ContentLength != expectedSegLen {
				return segmentResult{newOffset: offset}, &bilibili.SiteError{Kind: bilibili.ErrKindNetwork, Message: "分段 Content-Length 与区间长度不一致"}
			}
			if totalSize > 0 && end+1 >= totalSize {
				doneAfterThis = true
			}
		} else {
			return segmentResult{newOffset: offset}, &bilibili.SiteError{Kind: bilibili.ErrKindNetwork, Message: "206 响应缺少 Content-Range"}
		}
		f, err = os.OpenFile(partPath, os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			_, err = f.Seek(offset, io.SeekStart)
		}
	case http.StatusOK:
		if offset > 0 {
			// Subsequent segment returned 200 OK instead of 206: truncate .part, rotate node, and retry from 0 (data-and-cache.md §2.4).
			_ = os.Remove(partPath)
			return segmentResult{newOffset: 0, truncatedToZero: true}, &bilibili.SiteError{
				Kind:       bilibili.ErrKindNetwork,
				HTTPStatus: http.StatusOK,
				Message:    "节点未返回 206 Partial Content，已截断从头重试",
			}
		}
		if resp.ContentLength <= 0 {
			return segmentResult{newOffset: offset}, &bilibili.SiteError{Kind: bilibili.ErrKindNetwork, Message: "完整响应缺少有效的 Content-Length"}
		}
		if resp.ContentLength > 0 {
			expectedSegLen = resp.ContentLength
			totalSize = resp.ContentLength
		}
		doneAfterThis = true
		f, err = os.OpenFile(partPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	default:
		return segmentResult{newOffset: offset}, mapMediaHTTPStatus(resp.StatusCode, targetURL)
	}
	if isImage && totalSize > MaxImageFileBytes {
		if f != nil {
			_ = f.Close()
		}
		return segmentResult{newOffset: offset}, &bilibili.SiteError{Kind: bilibili.ErrKindSchemaChanged, Message: "图片超过 50 MiB"}
	}
	if err != nil {
		return segmentResult{newOffset: offset}, err
	}

	wrapped := m.client.WrapMediaReader(resp.Body)
	defer wrapped.Close()
	reader := io.LimitReader(wrapped, expectedSegLen+1)

	buf := make([]byte, 64*1024)
	var segWritten int64
	written := offset
	if totalSize > 0 {
		t.TotalBytes = totalSize
	} else if resp.ContentLength > 0 {
		t.TotalBytes = offset + resp.ContentLength
	}

	progressAt := time.Now()
	progressBytes := offset
	for {
		nr, rErr := reader.Read(buf)
		if nr > 0 {
			if segWritten+int64(nr) > expectedSegLen {
				_ = f.Close()
				if err := os.Truncate(partPath, offset); err != nil {
					return segmentResult{newOffset: offset}, err
				}
				return segmentResult{newOffset: offset, totalSize: totalSize}, &bilibili.SiteError{Kind: bilibili.ErrKindNetwork, Message: "分段正文超出声明区间"}
			}
			nw, wErr := f.Write(buf[:nr])
			if wErr != nil {
				_ = f.Close()
				return segmentResult{newOffset: written, totalSize: totalSize}, wErr
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
			return segmentResult{newOffset: written, totalSize: totalSize}, &bilibili.SiteError{
				Kind:     bilibili.ErrKindNetwork,
				Endpoint: targetURL,
				Message:  "媒体分段读取中断",
				Err:      rErr,
			}
		}
	}

	if err := f.Sync(); err != nil {
		_ = f.Close()
		return segmentResult{newOffset: written, totalSize: totalSize}, err
	}
	if err := f.Close(); err != nil {
		return segmentResult{newOffset: written, totalSize: totalSize}, err
	}

	if expectedSegLen >= 0 && segWritten != expectedSegLen {
		return segmentResult{newOffset: written, totalSize: totalSize}, &bilibili.SiteError{
			Kind:     bilibili.ErrKindNetwork,
			Endpoint: targetURL,
			Message:  fmt.Sprintf("分段字节未读满: 实读 %d < 预期 %d", segWritten, expectedSegLen),
		}
	}

	return segmentResult{
		newOffset: written,
		totalSize: totalSize,
		done:      doneAfterThis,
	}, nil
}

func parseContentRange(cr string) (start, end, total int64, ok bool) {
	cr = strings.TrimSpace(cr)
	if !strings.HasPrefix(strings.ToLower(cr), "bytes ") {
		return 0, 0, 0, false
	}
	rest := strings.TrimSpace(cr[6:])
	slashIdx := strings.IndexByte(rest, '/')
	if slashIdx <= 0 {
		return 0, 0, 0, false
	}
	rangePart := rest[:slashIdx]
	totalPart := rest[slashIdx+1:]
	dashIdx := strings.IndexByte(rangePart, '-')
	if dashIdx <= 0 {
		return 0, 0, 0, false
	}
	sVal, err1 := strconv.ParseInt(strings.TrimSpace(rangePart[:dashIdx]), 10, 64)
	eVal, err2 := strconv.ParseInt(strings.TrimSpace(rangePart[dashIdx+1:]), 10, 64)
	if err1 != nil || err2 != nil || sVal < 0 || eVal < sVal {
		return 0, 0, 0, false
	}
	var tVal int64 = -1
	if totalPart != "*" {
		parsedTotal, err3 := strconv.ParseInt(strings.TrimSpace(totalPart), 10, 64)
		if err3 != nil || parsedTotal <= 0 {
			return 0, 0, 0, false
		}
		tVal = parsedTotal
	}
	return sVal, eVal, tVal, true
}

func mapMediaHTTPStatus(status int, endpoint string) *bilibili.SiteError {
	switch status {
	case 401:
		return &bilibili.SiteError{Kind: bilibili.ErrKindAuthRequired, HTTPStatus: status, Endpoint: endpoint, Message: "媒体需要登录凭证 (401)"}
	case 403:
		return &bilibili.SiteError{Kind: bilibili.ErrKindForbidden, HTTPStatus: status, Endpoint: endpoint, Message: "媒体访问被拒绝 (403)"}
	case 404, 410:
		return &bilibili.SiteError{Kind: bilibili.ErrKindUnavailable, HTTPStatus: status, Endpoint: endpoint, Message: fmt.Sprintf("媒体资源已失效 (%d)", status)}
	case 412:
		return &bilibili.SiteError{Kind: bilibili.ErrKindRiskControl, HTTPStatus: status, Endpoint: endpoint, Message: "媒体请求触发风控 (412)"}
	case 429:
		return &bilibili.SiteError{Kind: bilibili.ErrKindRateLimited, HTTPStatus: status, Endpoint: endpoint, Message: "媒体请求触发限流 (429)"}
	default:
		return &bilibili.SiteError{Kind: bilibili.ErrKindNetwork, HTTPStatus: status, Endpoint: endpoint, Message: fmt.Sprintf("媒体 HTTP 状态异常 (%d)", status)}
	}
}

func (m *Manager) saveTaskProgress(t Task) error {
	if t.ProgressOwner != nil {
		owner := t.ProgressOwner
		for _, file := range t.Files {
			file.Path = t.ProgressPrefix + file.Path
			file.ItemDir = t.ItemDir
			found := false
			for i := range owner.Files {
				if owner.Files[i].Path == file.Path {
					owner.Files[i], found = file, true
					break
				}
			}
			if !found {
				owner.Files = append(owner.Files, file)
			}
		}
		owner.Attempts = t.Attempts
		return m.saveTaskProgress(*owner)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	t.UpdatedAt = now
	if t.ErrorMessage == "" && t.LastError != "" {
		t.ErrorMessage = t.LastError
	}
	t.LastError = t.ErrorMessage
	filesJSON, _ := json.Marshal(t.Files)
	attemptsJSON, _ := json.Marshal(t.Attempts)
	res, err := m.db.Exec(`
		UPDATE download_tasks
		SET
			cid = ?, page = ?, title = ?, part_title = ?,
			mid = ?, author_uid = ?, author_name = ?, author_face = ?, cover_url = ?,
			status = ?, pause_reason = ?, error_kind = ?, error_message = ?, quality_hint = ?,
			bytes_downloaded = ?, total_bytes = ?, progress = ?,
			item_key = ?, item_dir = ?, files_json = ?, attempts_json = ?, updated_at = ?
		WHERE task_id = ? AND status = 'running'
	`,
		t.CID, t.Page, t.Title, t.PartTitle,
		t.Mid, t.AuthorUID, t.AuthorName, t.AuthorFace, t.CoverURL,
		t.Status, t.PauseReason, t.ErrorKind, t.ErrorMessage, t.QualityHint,
		t.BytesDownloaded, t.TotalBytes, t.Progress,
		t.ItemKey, t.ItemDir, string(filesJSON), string(attemptsJSON), t.UpdatedAt,
		t.TaskID,
	)
	if err != nil {
		return err
	}
	if changed, _ := res.RowsAffected(); changed == 0 {
		return nil
	}
	m.taskFaults.Delete(t.TaskID)
	m.emitUpdate(t)
	return nil
}

func (m *Manager) emitUpdate(t Task) {
	if t.ErrorMessage == "" && t.LastError != "" {
		t.ErrorMessage = t.LastError
	}
	t.LastError = t.ErrorMessage
	if m.onTaskUpdate != nil && t.TaskID != "" {
		m.notifyMu.Lock()
		if m.lastNotify == nil {
			m.lastNotify = make(map[string]taskNotification)
		}
		previous := m.lastNotify[t.TaskID]
		if previous.status == t.Status && t.Status == "running" && time.Since(previous.at) < time.Second {
			m.notifyMu.Unlock()
			return
		}
		m.lastNotify[t.TaskID] = taskNotification{status: t.Status, at: time.Now()}
		m.notifyMu.Unlock()
		m.onTaskUpdate(t)
	}
}

type taskNotification struct {
	status string
	at     time.Time
}

func (m *Manager) persistTaskProgress(task Task) {
	if err := m.saveTaskProgress(task); err != nil {
		task.Status, task.ErrorKind, task.ErrorMessage = "failed", "storage", "任务状态写入失败，请检查存储后重试"
		m.taskFaults.Store(task.TaskID, task)
		m.emitUpdate(task)
	}
}

func (m *Manager) withTaskFault(task Task) Task {
	if fault, exists := m.taskFaults.Load(task.TaskID); exists {
		return fault.(Task)
	}
	return task
}

// Register task ownership before creating any file. A pre-existing name with
// no task checkpoint or manifest is a user file, and must never be replaced.
func (m *Manager) claimTaskFile(t *Task, root, rel, finalPath string) error {
	owned := false
	for _, file := range t.Files {
		if taskFileIdentity(file.Path) == taskFileIdentity(rel) && taskFileOwned(file) {
			owned = true
		}
	}
	if t.ProgressOwner != nil {
		for _, file := range t.ProgressOwner.Files {
			if taskFileIdentity(file.Path) == taskFileIdentity(t.ProgressPrefix+rel) && strings.EqualFold(file.ItemDir, root) && taskFileOwned(file) {
				owned = true
			}
		}
	}
	if m.lib != nil {
		if meta, ok, err := m.lib.GetItem(t.ItemKey); err != nil {
			return err
		} else if ok && strings.EqualFold(meta.ItemDir, root) {
			for _, file := range meta.Media {
				if taskFileIdentity(file.Path) == taskFileIdentity(rel) {
					owned = true
				}
			}
		}
	}
	if !owned {
		for _, path := range []string{finalPath, finalPath + ".part"} {
			if _, err := os.Lstat(path); err == nil {
				return &bilibili.SiteError{Kind: "storage", Message: "目标文件已存在且未由本任务登记，已保留原文件"}
			} else if !os.IsNotExist(err) {
				return err
			}
		}
	}
	found := false
	for i := range t.Files {
		if taskFileIdentity(t.Files[i].Path) == taskFileIdentity(rel) {
			t.Files[i].Owned = true
			found = true
		}
	}
	if !found {
		t.Files = append(t.Files, TaskFileProgress{Path: rel, Status: "waiting", Owned: true})
	}
	return m.saveTaskProgress(*t)
}

func taskFileIdentity(rel string) string {
	return strings.TrimSuffix(filepath.ToSlash(rel), ".part")
}

func taskFileOwned(file TaskFileProgress) bool {
	// Pre-ownership task records can prove a written file through a durable
	// length checkpoint or hash. A waiting filename alone grants no ownership.
	return file.Owned || file.SHA256 != "" || file.DownloadedBytes > 0 && file.TotalBytes >= file.DownloadedBytes
}

func taskRelativePath(task *Task, filePath string) string {
	if task.ItemDir != "" {
		if rel, err := filepath.Rel(task.ItemDir, filePath); err == nil {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.Base(filePath)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanTask(r rowScanner) (Task, error) {
	var t Task
	var linkedInt int
	var filesRaw, attemptsRaw string
	err := r.Scan(
		&t.TaskID, &t.Site, &t.Kind, &t.Source, &t.TargetID, &t.CID, &t.Page, &t.Title, &t.PartTitle,
		&t.Mid, &t.AuthorUID, &t.AuthorName, &t.AuthorFace, &t.CoverURL, &linkedInt,
		&t.Status, &t.PauseReason, &t.ErrorKind, &t.ErrorMessage, &t.QualityHint,
		&t.BytesDownloaded, &t.TotalBytes, &t.Progress, &t.ItemKey, &t.ItemDir,
		&filesRaw, &attemptsRaw, &t.CreatedAt, &t.UpdatedAt,
	)
	t.LastError = t.ErrorMessage
	t.CacheLinkedVideos = linkedInt == 1
	if filesRaw != "" {
		_ = json.Unmarshal([]byte(filesRaw), &t.Files)
	}
	if attemptsRaw != "" {
		_ = json.Unmarshal([]byte(attemptsRaw), &t.Attempts)
	}
	return t, err
}

func hashFileSHA256(filePath string) (string, int64, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// motionRelName is where the motion clip of a dynamic's idx-th live photo
// (实况) is kept, next to its still.
func motionRelName(idx int, rawURL string) string {
	ext := ".mp4"
	if u, err := url.Parse(rawURL); err == nil {
		switch e := strings.ToLower(path.Ext(u.Path)); e {
		case ".mp4", ".mov", ".m4v":
			ext = e
		}
	}
	return fmt.Sprintf("images/%02d_live%s", idx+1, ext)
}

// cacheLiveMotion keeps the motion clip of a live photo with its still. A
// clip that fails to download leaves the post partial, as any other missing
// file does; stop, storage and cancel errors end the task.
func (m *Manager) cacheLiveMotion(ctx context.Context, t *Task, itemDir string, previous library.ItemMetadata, exists bool, idx int, liveURL string, partialErrors *[]string) (*library.MediaFile, error) {
	if liveURL == "" {
		return nil, nil
	}
	relName := motionRelName(idx, liveURL)
	if exists {
		for _, media := range previous.Media {
			if media.Role == "motion" && media.Source == liveURL {
				relName = media.Path
				break
			}
		}
	}
	if mediaIsValid(previous, relName) {
		for _, media := range previous.Media {
			if media.Path == relName {
				m.setFileProgress(t, "motion", relName, "completed", media.Bytes, media.Bytes, 0)
				file := media
				return &file, nil
			}
		}
	}
	m.setFileProgress(t, "motion", relName, "downloading", 0, 0, 0)
	n, sha, err := m.downloadFileWithRange(ctx, []string{liveURL}, filepath.Join(itemDir, relName), t)
	if err != nil {
		m.setFileProgress(t, "motion", relName, "failed", 0, 0, 0)
		if isStopError(err) || isStorageError(err) || ctx.Err() != nil {
			return nil, err
		}
		*partialErrors = append(*partialErrors, fmt.Sprintf("img_%02d 实况: %v", idx+1, err))
		return nil, nil
	}
	// Keep the clip a plain progressive MP4 with moov ahead of mdat, so system
	// players preview it too (DEC-046 ①); a fragmented or broken clip is
	// removed and leaves the post partial.
	if strings.EqualFold(path.Ext(relName), ".mp4") || strings.EqualFold(path.Ext(relName), ".m4v") {
		full := filepath.Join(itemDir, relName)
		summary, vErr := EnsureSingleMP4Context(ctx, full, 0)
		if vErr != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			_ = os.Remove(full)
			m.setFileProgress(t, "motion", relName, "failed", 0, 0, 0)
			*partialErrors = append(*partialErrors, fmt.Sprintf("img_%02d 实况: %v", idx+1, vErr))
			return nil, nil
		}
		n, sha = summary.FileBytes, summary.SHA256
		m.setFileProgress(t, "motion", relName, "completed", n, n, 0)
		return &library.MediaFile{Role: "motion", Path: relName, Bytes: n, SHA256: sha, Source: liveURL,
			Width: summary.Width, Height: summary.Height, Duration: int(summary.DurationSec + 0.5)}, nil
	}
	m.setFileProgress(t, "motion", relName, "completed", n, n, 0)
	return &library.MediaFile{Role: "motion", Path: relName, Bytes: n, SHA256: sha, Source: liveURL}, nil
}

func guessImageExt(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err == nil && u.Path != "" {
		ext := strings.ToLower(path.Ext(u.Path))
		switch ext {
		case ".jpg", ".jpeg", ".png", ".webp", ".gif", ".avif":
			return ext
		}
	}
	return ".jpg"
}

func isTempStagingPath(rel string) bool {
	return strings.HasSuffix(rel, ".raw") || strings.HasSuffix(rel, ".imgtmp") || strings.HasSuffix(rel, ".audtmp")
}

func cleanTaskPartFiles(t Task) {
	if t.ItemDir == "" {
		return
	}
	for _, file := range t.Files {
		if !taskFileOwned(file) || file.Status == "completed" || file.Status == "skipped" {
			continue
		}
		root, rel := t.ItemDir, file.Path
		if file.ItemDir != "" {
			root, rel = file.ItemDir, filepath.Base(file.Path)
		}
		rel = taskFileIdentity(rel)
		candidates := []string{rel + ".part", rel + ".part.part", rel + ".raw", rel + ".raw.part"}
		if isTempStagingPath(rel) {
			candidates = append(candidates, rel)
		}
		for _, pending := range candidates {
			partPath, err := library.ResolveItemPath(root, pending)
			if err == nil {
				_ = os.Remove(partPath)
			}
		}
	}
}

func mergePartRecords(existing []library.VideoPartRecord, added library.VideoPartRecord) []library.VideoPartRecord {
	out := make([]library.VideoPartRecord, 0, len(existing)+1)
	replaced := false
	for _, p := range existing {
		if p.Page == added.Page || (p.CID > 0 && p.CID == added.CID) {
			out = append(out, added)
			replaced = true
		} else {
			out = append(out, p)
		}
	}
	if !replaced {
		out = append(out, added)
	}
	return out
}

func mergeMediaFiles(existing []library.MediaFile, added []library.MediaFile) []library.MediaFile {
	byPath := make(map[string]library.MediaFile, len(existing)+len(added))
	var order []string
	for _, m := range existing {
		byPath[m.Path] = m
		order = append(order, m.Path)
	}
	for _, m := range added {
		if _, seen := byPath[m.Path]; !seen {
			order = append(order, m.Path)
		}
		byPath[m.Path] = m
	}
	out := make([]library.MediaFile, 0, len(order))
	for _, p := range order {
		out = append(out, byPath[p])
	}
	return out
}

func audioRank(id int) int {
	switch id {
	case 30251:
		return 5
	case 30250:
		return 4
	case 30280:
		return 3
	case 30232:
		return 2
	case 30216:
		return 1
	}
	return 0
}

func isStopError(err error) bool {
	return bilibili.IsKind(err, bilibili.ErrKindRiskControl) || bilibili.IsKind(err, bilibili.ErrKindRateLimited) || bilibili.IsKind(err, bilibili.ErrKindAuthRequired)
}

func isStorageError(err error) bool {
	var pathErr *os.PathError
	return errors.As(err, &pathErr) || bilibili.IsKind(err, "storage")
}

func targetQuality(quality string) int {
	switch quality {
	case "2160":
		return 120
	case "1080":
		return 112
	case "720":
		return 74
	}
	return 127
}
