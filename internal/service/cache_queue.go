package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
)

const (
	CacheQueueQueued         = "queued"
	CacheQueueRunning        = "running"
	CacheQueueWaiting        = "waiting"
	CacheQueueWaitingBackoff = "waiting_backoff"
	CacheQueuePaused         = "paused"
	CacheQueueCompleted      = "completed"
	CacheQueueStopped        = "stopped"

	cacheQueueFileName       = "cache-queue.json"
	cacheQueueSchemaVersion  = 1
	maxCacheQueueRecentItems = 30
)

// CacheQueuePacing centralizes all conservative anti-risk-control delays and thresholds (Q-017).
type CacheQueuePacing struct {
	ItemMinDelay        time.Duration
	ItemMaxDelay        time.Duration
	LongRestMinItems    int
	LongRestMaxItems    int
	LongRestMinDelay    time.Duration
	LongRestMaxDelay    time.Duration
	BackoffCoolMinDelay time.Duration
	BackoffCoolMaxDelay time.Duration
	StartupMinDelay     time.Duration
	StartupMaxDelay     time.Duration
}

// DefaultCacheQueuePacing defines the default Q-017 pacing intervals for background list caching.
var DefaultCacheQueuePacing = CacheQueuePacing{
	ItemMinDelay:        40 * time.Second,
	ItemMaxDelay:        150 * time.Second,
	LongRestMinItems:    15,
	LongRestMaxItems:    25,
	LongRestMinDelay:    10 * time.Minute,
	LongRestMaxDelay:    25 * time.Minute,
	BackoffCoolMinDelay: 10 * time.Minute,
	BackoffCoolMaxDelay: 30 * time.Minute,
	StartupMinDelay:     1 * time.Minute,
	StartupMaxDelay:     5 * time.Minute,
}

// CacheQueueClock abstracts wall clock and timers so tests run deterministically with zero real sleeps.
type CacheQueueClock interface {
	Now() time.Time
	NewTimer(d time.Duration) CacheQueueTimer
}

// CacheQueueTimer is a single-shot timer created by CacheQueueClock.
type CacheQueueTimer interface {
	C() <-chan time.Time
	Stop() bool
}

// CacheQueueRand returns a pseudo-random integer in [0, n) for n > 0.
type CacheQueueRand func(n int) int

type realCacheQueueClock struct{}

type realCacheQueueTimer struct {
	t *time.Timer
}

func (realCacheQueueClock) Now() time.Time {
	return time.Now()
}

func (realCacheQueueClock) NewTimer(d time.Duration) CacheQueueTimer {
	return &realCacheQueueTimer{t: time.NewTimer(d)}
}

func (rt *realCacheQueueTimer) C() <-chan time.Time {
	return rt.t.C
}

func (rt *realCacheQueueTimer) Stop() bool {
	return rt.t.Stop()
}

// CacheQueueRequest asks to enqueue an entire creator list ("dynamics", "videos", or "works")
// for conservative newest-to-oldest background caching.
type CacheQueueRequest struct {
	Site      string                   `json:"site"`
	List      string                   `json:"list"`
	User      string                   `json:"user"`
	UserName  string                   `json:"userName"`
	AvatarURL string                   `json:"avatarUrl"`
	Dynamics  []bilibili.DynamicItem   `json:"-"`
	Videos    []bilibili.UserVideoItem `json:"-"`
	Works     []DouyinWorkCardDTO      `json:"-"`
	Cursor    string                   `json:"cursor"`
	HasMore   bool                     `json:"hasMore"`
	// Origin is QueueOriginFullCache when a full-cache rule started the run
	// (DEC-061); empty for "全部缓存" on a user page.
	Origin string `json:"origin,omitempty"`
}

// QueueOriginFullCache marks a queue run started by a full-cache rule (DEC-061).
const QueueOriginFullCache = "full_cache"

// CacheQueueRecentItem summarizes one item cached by a queue run so the detail panel can open it locally.
type CacheQueueRecentItem struct {
	ItemKey  string `json:"itemKey"`
	Title    string `json:"title"`
	CachedAt string `json:"cachedAt"`
}

// CacheQueueTask is the UI-facing DTO for one creator list background cache queue task.
type CacheQueueTask struct {
	ID               string                 `json:"id"`
	Site             string                 `json:"site"`
	User             string                 `json:"user"`
	UserName         string                 `json:"userName"`
	AvatarPath       string                 `json:"avatarPath,omitempty"`
	List             string                 `json:"list"`
	ListLabel        string                 `json:"listLabel"`
	Origin           string                 `json:"origin,omitempty"`
	Status           string                 `json:"status"`
	StatusText       string                 `json:"statusText"`
	PauseReason      string                 `json:"pauseReason,omitempty"`
	StopReason       string                 `json:"stopReason,omitempty"`
	Cursor           string                 `json:"cursor,omitempty"`
	HasMore          bool                   `json:"hasMore"`
	CachedCount      int                    `json:"cachedCount"`
	SkippedCount     int                    `json:"skippedCount"`
	ActiveDownloadID string                 `json:"activeDownloadId,omitempty"`
	ActiveItemKey    string                 `json:"activeItemKey,omitempty"`
	ActiveTitle      string                 `json:"activeTitle,omitempty"`
	RecentItems      []CacheQueueRecentItem `json:"recentItems,omitempty"`
	NextRunAt        string                 `json:"nextRunAt,omitempty"`
	CreatedAt        string                 `json:"createdAt"`
	UpdatedAt        string                 `json:"updatedAt"`
}

type cacheQueueFile struct {
	Version int                `json:"version"`
	Tasks   []cacheQueueRecord `json:"tasks"`
}

type cacheQueueRecord struct {
	ID                string                 `json:"id"`
	Site              string                 `json:"site"`
	User              string                 `json:"user"`
	UserName          string                 `json:"userName,omitempty"`
	AvatarKey         string                 `json:"avatarKey,omitempty"`
	List              string                 `json:"list"`
	Origin            string                 `json:"origin,omitempty"`
	Cursor            string                 `json:"cursor,omitempty"`
	HasMore           bool                   `json:"hasMore"`
	ProcessedKeys     []string               `json:"processedKeys,omitempty"`
	CachedCount       int                    `json:"cachedCount"`
	SkippedCount      int                    `json:"skippedCount"`
	SinceRestCount    int                    `json:"sinceRestCount,omitempty"`
	RestThreshold     int                    `json:"restThreshold,omitempty"`
	ActiveDownloadIDs []string               `json:"activeDownloadIds,omitempty"`
	ActiveItemKey     string                 `json:"activeItemKey,omitempty"`
	ActiveTitle       string                 `json:"activeTitle,omitempty"`
	RecentItems       []CacheQueueRecentItem `json:"recentItems,omitempty"`
	Status            string                 `json:"status"`
	PauseReason       string                 `json:"pauseReason,omitempty"`
	StopReason        string                 `json:"stopReason,omitempty"`
	NextRunAt         string                 `json:"nextRunAt,omitempty"`
	CreatedAt         string                 `json:"createdAt"`
	UpdatedAt         string                 `json:"updatedAt"`
}

type cacheQueueRuntime struct {
	pager                  queuePager
	pageItems              []queueCandidate
	pageIdx                int
	pageNextCursor         string
	pageNextHasMore        bool
	pageLoaded             bool
	seenKeys               map[string]bool
	nextPartEnqueuers      []queuePartEnqueuer
	resumePendingDownloads bool
	stepCancel             context.CancelFunc
}

type cacheQueueEntry struct {
	rec cacheQueueRecord
	rt  cacheQueueRuntime
}

type cacheQueueManager struct {
	b      *Backend
	path   string
	clock  CacheQueueClock
	rng    CacheQueueRand
	pacing CacheQueuePacing

	mu         sync.Mutex
	tasks      []*cacheQueueEntry
	dirty      bool
	fileExists bool
	started    bool
	stopCancel context.CancelFunc
	siteWake   map[site.ID]chan struct{}
	wg         sync.WaitGroup
}

var (
	queueFullURLPattern   = regexp.MustCompile(`(?i)https?://[^\s"'<>]+`)
	queueQueryTailPattern = regexp.MustCompile(`\?[A-Za-z0-9_.-]+=[^\s"'<>]*`)
	queueSecretKVPattern  = regexp.MustCompile(`(?i)\b(?:cookie|authorization|sessdata|bili_jct|dedeuserid|dedeuserid__ckmd5|sid|ac_time_value|refresh_token|pending_old_refresh_token|access_key|auth_key|w_rid|upsig|wssecret|wstime|deadline|bwsv|sessionid|sessionid_ss|sid_tt|uid_tt|uid_tt_ss|sid_guard|odin_tt|passport_csrf_token|passport_csrf_token_default|ttwid|uifid|uifid_temp|mstoken|s_v_web_id|__ac_nonce|__ac_signature|a_bogus|x-bogus|x-expires|x-signature|x-secsdk-web-signature|x-secsdk-csrf-token|token|sign)["'\\]*\s*[=:][^\s;"'<>]*`)
)

// guardCacheQueueSchema checks <dataDir>/cache-queue.json before databases open:
// files written by a newer schema version are refused; corrupt files are backed up.
func guardCacheQueueSchema(dataDir string) (string, error) {
	if strings.TrimSpace(dataDir) == "" {
		return "", nil
	}
	path := filepath.Join(dataDir, cacheQueueFileName)
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var probe struct {
		Version int `json:"version"`
	}
	if jErr := json.Unmarshal(raw, &probe); jErr != nil {
		backup, bErr := storage.BackupCorruptFile(path)
		if bErr != nil {
			return "", bErr
		}
		_ = os.Remove(path)
		return backup, nil
	}
	if probe.Version > cacheQueueSchemaVersion {
		return "", &storage.NewerDataError{
			File:      cacheQueueFileName,
			Found:     probe.Version,
			Supported: cacheQueueSchemaVersion,
		}
	}
	return "", nil
}

func newCacheQueueManager(b *Backend, path string, clock CacheQueueClock, rng CacheQueueRand, pacing *CacheQueuePacing) (*cacheQueueManager, string, error) {
	if clock == nil {
		clock = realCacheQueueClock{}
	}
	if rng == nil {
		rng = rand.IntN
	}
	cfg := DefaultCacheQueuePacing
	if pacing != nil {
		cfg = *pacing
	}
	m := &cacheQueueManager{
		b:      b,
		path:   path,
		clock:  clock,
		rng:    rng,
		pacing: cfg,
		siteWake: map[site.ID]chan struct{}{
			site.Bilibili: make(chan struct{}, 1),
			site.Douyin:   make(chan struct{}, 1),
		},
	}
	backup, err := m.loadFromDiskLocked()
	if err != nil {
		return nil, backup, err
	}
	return m, backup, nil
}

func (m *cacheQueueManager) loadFromDiskLocked() (string, error) {
	if strings.TrimSpace(m.path) == "" {
		return "", nil
	}
	raw, err := os.ReadFile(m.path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var doc cacheQueueFile
	if jErr := json.Unmarshal(raw, &doc); jErr != nil {
		backup, bErr := storage.BackupCorruptFile(m.path)
		if bErr != nil {
			return "", bErr
		}
		_ = os.Remove(m.path)
		return backup, nil
	}
	if doc.Version > cacheQueueSchemaVersion {
		return "", &storage.NewerDataError{
			File:      cacheQueueFileName,
			Found:     doc.Version,
			Supported: cacheQueueSchemaVersion,
		}
	}
	m.fileExists = true
	m.tasks = make([]*cacheQueueEntry, 0, len(doc.Tasks))
	for _, r := range doc.Tasks {
		clean := sanitizeQueueRecord(r)
		if clean.ID == "" || clean.Site == "" || clean.User == "" || clean.List == "" {
			continue
		}
		seen := make(map[string]bool, len(clean.ProcessedKeys))
		for _, k := range clean.ProcessedKeys {
			if k != "" {
				seen[k] = true
			}
		}
		m.tasks = append(m.tasks, &cacheQueueEntry{
			rec: clean,
			rt: cacheQueueRuntime{
				seenKeys:               seen,
				resumePendingDownloads: len(clean.ActiveDownloadIDs) > 0,
			},
		})
	}
	return "", nil
}

func (m *cacheQueueManager) sanitizeText(s string) string {
	if m != nil && m.b != nil {
		s = m.b.redactStoredSecrets(s)
	}
	return sanitizeQueueText(s)
}

func sanitizeQueueText(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = queueFullURLPattern.ReplaceAllString(s, "")
	s = queueQueryTailPattern.ReplaceAllString(s, "")
	s = queueSecretKVPattern.ReplaceAllString(s, "")
	return strings.Join(strings.Fields(s), " ")
}

func sanitizeCursor(c string) string {
	c = strings.TrimSpace(c)
	if strings.ContainsAny(c, "?=&;:/\\ \t\r\n") {
		return ""
	}
	return c
}

func sanitizeItemKey(k string) string {
	k = strings.TrimSpace(k)
	if strings.ContainsAny(k, "?=&;/\\ \t\r\n") || strings.Contains(k, "://") {
		return ""
	}
	return k
}

func sanitizeQueueRecord(r cacheQueueRecord) cacheQueueRecord {
	siteID, list, normUser, err := normalizeCacheQueueTarget(r.Site, r.List, r.User)
	if err != nil {
		return cacheQueueRecord{}
	}
	r.Site = string(siteID)
	r.List = list
	r.User = normUser
	r.ID = sanitizeCursor(r.ID)
	r.UserName = sanitizeQueueText(r.UserName)
	if r.Origin != QueueOriginFullCache {
		r.Origin = ""
	}
	if !avatarFileRe.MatchString(r.AvatarKey) {
		r.AvatarKey = ""
	}
	r.Cursor = sanitizeCursor(r.Cursor)
	r.ActiveItemKey = sanitizeItemKey(r.ActiveItemKey)
	r.ActiveTitle = sanitizeQueueText(r.ActiveTitle)
	r.StopReason = sanitizeQueueText(r.StopReason)
	r.PauseReason = sanitizeCursor(r.PauseReason)
	r.NextRunAt = sanitizeCursorFallbackRFC3339(r.NextRunAt)
	r.CreatedAt = sanitizeCursorFallbackRFC3339(r.CreatedAt)
	r.UpdatedAt = sanitizeCursorFallbackRFC3339(r.UpdatedAt)

	var cleanProcessed []string
	for _, k := range r.ProcessedKeys {
		if ck := sanitizeItemKey(k); ck != "" && !slices.Contains(cleanProcessed, ck) {
			cleanProcessed = append(cleanProcessed, ck)
		}
	}
	r.ProcessedKeys = cleanProcessed

	var cleanDL []string
	for _, id := range r.ActiveDownloadIDs {
		if cid := sanitizeCursor(id); cid != "" && !slices.Contains(cleanDL, cid) {
			cleanDL = append(cleanDL, cid)
		}
	}
	r.ActiveDownloadIDs = cleanDL

	var cleanRecent []CacheQueueRecentItem
	for _, item := range r.RecentItems {
		ck := sanitizeItemKey(item.ItemKey)
		if ck == "" {
			continue
		}
		cleanRecent = append(cleanRecent, CacheQueueRecentItem{
			ItemKey:  ck,
			Title:    sanitizeQueueText(item.Title),
			CachedAt: sanitizeCursorFallbackRFC3339(item.CachedAt),
		})
		if len(cleanRecent) >= maxCacheQueueRecentItems {
			break
		}
	}
	r.RecentItems = cleanRecent

	switch r.Status {
	case CacheQueueQueued, CacheQueueRunning, CacheQueueWaiting, CacheQueueWaitingBackoff,
		CacheQueuePaused, CacheQueueCompleted, CacheQueueStopped:
	default:
		r.Status = CacheQueueStopped
		if r.StopReason == "" {
			r.StopReason = "未知状态，已停止"
		}
	}
	return r
}

func sanitizeCursorFallbackRFC3339(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return ""
}

func isUnfinishedQueueStatus(status string) bool {
	return status != CacheQueueCompleted && status != CacheQueueStopped
}

func (m *cacheQueueManager) saveLocked() error {
	if strings.TrimSpace(m.path) == "" {
		return nil
	}
	if len(m.tasks) == 0 && !m.fileExists {
		return nil
	}
	doc := cacheQueueFile{
		Version: cacheQueueSchemaVersion,
		Tasks:   make([]cacheQueueRecord, 0, len(m.tasks)),
	}
	for _, e := range m.tasks {
		rec := sanitizeQueueRecord(e.rec)
		rec.UserName = m.sanitizeText(rec.UserName)
		rec.ActiveTitle = m.sanitizeText(rec.ActiveTitle)
		rec.StopReason = m.sanitizeText(rec.StopReason)
		for i := range rec.RecentItems {
			rec.RecentItems[i].Title = m.sanitizeText(rec.RecentItems[i].Title)
		}
		doc.Tasks = append(doc.Tasks, rec)
	}
	if err := storage.WriteJSONAtomic(m.path, doc, 0o600); err != nil {
		return err
	}
	m.fileExists = true
	m.dirty = false
	return nil
}

func (m *cacheQueueManager) avatarKeys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, e := range m.tasks {
		if e.rec.AvatarKey != "" && avatarFileRe.MatchString(e.rec.AvatarKey) {
			out = append(out, e.rec.AvatarKey)
		}
	}
	return out
}

func (m *cacheQueueManager) randIntInclusiveLocked(minVal, maxVal int) int {
	if maxVal <= minVal {
		return minVal
	}
	n := maxVal - minVal + 1
	v := m.rng(n)
	if v < 0 {
		v = 0
	} else if v >= n {
		v = n - 1
	}
	return minVal + v
}

func (m *cacheQueueManager) randDurationLocked(minD, maxD time.Duration) time.Duration {
	if maxD <= minD {
		return minD
	}
	diff := maxD - minD
	if diff >= time.Second && minD%time.Second == 0 && maxD%time.Second == 0 {
		spanSec := int(diff / time.Second)
		return minD + time.Duration(m.randIntInclusiveLocked(0, spanSec))*time.Second
	}
	spanMs := int(diff / time.Millisecond)
	if spanMs <= 0 {
		return minD
	}
	return minD + time.Duration(m.randIntInclusiveLocked(0, spanMs))*time.Millisecond
}

func (m *cacheQueueManager) pickRestThresholdLocked() int {
	return m.randIntInclusiveLocked(m.pacing.LongRestMinItems, m.pacing.LongRestMaxItems)
}

func (m *cacheQueueManager) nowRFC3339() string {
	return m.clock.Now().UTC().Format(time.RFC3339)
}

func makeCacheQueueID(siteID, list, user string) string {
	sum := sha256.Sum256([]byte(siteID + ":" + list + ":" + user))
	return "cq_" + hex.EncodeToString(sum[:8])
}

func (m *cacheQueueManager) statusTextLocked(rec cacheQueueRecord) string {
	switch rec.Status {
	case CacheQueueQueued:
		return "排队中"
	case CacheQueueRunning:
		return "进行中"
	case CacheQueueWaiting:
		if rec.StopReason != "" {
			return rec.StopReason
		}
		return "等待下一条"
	case CacheQueueWaitingBackoff:
		if rec.StopReason != "" {
			return rec.StopReason
		}
		return "等待站点退避结束"
	case CacheQueuePaused:
		if rec.PauseReason == "auth_required" {
			return firstNonEmpty(rec.StopReason, "登录已失效，重新登录后自动继续")
		}
		if rec.PauseReason == "app_closed" {
			return "已暂停（应用关闭）"
		}
		return firstNonEmpty(rec.StopReason, "已暂停")
	case CacheQueueCompleted:
		return "已完成（已到列表末尾）"
	case CacheQueueStopped:
		return firstNonEmpty(rec.StopReason, "已停止")
	default:
		return rec.Status
	}
}

func (m *cacheQueueManager) toDTOLocked(e *cacheQueueEntry) CacheQueueTask {
	rec := e.rec
	var activeDL string
	if len(rec.ActiveDownloadIDs) > 0 {
		activeDL = rec.ActiveDownloadIDs[0]
	}
	var avatarPath string
	if m.b != nil {
		avatarPath = m.b.AccountAvatarPath(rec.Site, rec.User)
	}
	recent := make([]CacheQueueRecentItem, len(rec.RecentItems))
	copy(recent, rec.RecentItems)
	userName := rec.UserName
	if userName == "" {
		userName = rec.User
	}
	return CacheQueueTask{
		ID:               rec.ID,
		Site:             rec.Site,
		User:             rec.User,
		UserName:         userName,
		AvatarPath:       avatarPath,
		List:             rec.List,
		ListLabel:        queueListLabel(rec),
		Origin:           rec.Origin,
		Status:           rec.Status,
		StatusText:       m.statusTextLocked(rec),
		PauseReason:      rec.PauseReason,
		StopReason:       rec.StopReason,
		Cursor:           rec.Cursor,
		HasMore:          rec.HasMore,
		CachedCount:      rec.CachedCount,
		SkippedCount:     rec.SkippedCount,
		ActiveDownloadID: activeDL,
		ActiveItemKey:    rec.ActiveItemKey,
		ActiveTitle:      rec.ActiveTitle,
		RecentItems:      recent,
		NextRunAt:        rec.NextRunAt,
		CreatedAt:        rec.CreatedAt,
		UpdatedAt:        rec.UpdatedAt,
	}
}

func (m *cacheQueueManager) notifyStateLocked(rec cacheQueueRecord) {
	if m.b == nil {
		return
	}
	m.b.logInfo(fmt.Sprintf("cache_queue.%s.%s id=%s state=%s cached=%d skipped=%d",
		rec.Site, rec.List, rec.ID, rec.Status, rec.CachedCount, rec.SkippedCount))
	if m.b.events != nil {
		m.b.events.PublishSite(EventCacheQueueUpdated, rec.Site, rec.ID+":"+rec.Status)
	}
}

func (m *cacheQueueManager) wakeSite(siteID site.ID) {
	if ch, ok := m.siteWake[siteID]; ok {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (m *cacheQueueManager) onDownloadUpdated(siteName string) {
	if sid, err := normalizeSiteID(siteName); err == nil {
		m.wakeSite(sid)
	}
}

// Start launches the independent per-site queue workers and schedules the 1–5m startup delay
// for any unfinished tasks recovered from the previous session.
func (m *cacheQueueManager) Start(ctx context.Context) {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	loopCtx, cancel := context.WithCancel(ctx)
	m.stopCancel = cancel
	m.started = true

	now := m.clock.Now().UTC()
	changed := false
	for _, sid := range []site.ID{site.Bilibili, site.Douyin} {
		loggedIn := m.hasSiteLogin(sid)
		assignedActive := false
		for _, e := range m.tasks {
			if e.rec.Site != string(sid) || !isUnfinishedQueueStatus(e.rec.Status) {
				continue
			}
			if e.rec.Status == CacheQueuePaused && e.rec.PauseReason == "user" {
				continue
			}
			if !loggedIn {
				if e.rec.Status != CacheQueuePaused || e.rec.PauseReason != "auth_required" {
					e.rec.Status = CacheQueuePaused
					e.rec.PauseReason = "auth_required"
					e.rec.StopReason = "未登录或登录已失效，重新登录后自动继续"
					e.rec.NextRunAt = ""
					e.rec.UpdatedAt = now.Format(time.RFC3339)
					e.rt.resumePendingDownloads = len(e.rec.ActiveDownloadIDs) > 0
					changed = true
				}
				continue
			}
			if e.rec.Status == CacheQueuePaused && e.rec.PauseReason == "auth_required" {
				// A task paused for an expired login resumes on the next login event (or by hand),
				// not merely because some credential is present at startup.
				continue
			}

			e.rt.resumePendingDownloads = len(e.rec.ActiveDownloadIDs) > 0
			if !assignedActive {
				assignedActive = true
				startupUntil := now.Add(m.randDurationLocked(m.pacing.StartupMinDelay, m.pacing.StartupMaxDelay))
				if e.rec.Status == CacheQueueWaitingBackoff {
					if existing, err := time.Parse(time.RFC3339, e.rec.NextRunAt); err != nil || existing.Before(startupUntil) {
						e.rec.NextRunAt = startupUntil.Format(time.RFC3339)
					}
				} else {
					if existing, err := time.Parse(time.RFC3339, e.rec.NextRunAt); err == nil && existing.After(startupUntil) {
						startupUntil = existing
					}
					e.rec.Status = CacheQueueWaiting
					e.rec.PauseReason = ""
					e.rec.StopReason = "应用启动后等待继续"
					e.rec.NextRunAt = startupUntil.Format(time.RFC3339)
				}
				e.rec.UpdatedAt = now.Format(time.RFC3339)
				changed = true
			} else if e.rec.Status != CacheQueueQueued {
				e.rec.Status = CacheQueueQueued
				e.rec.PauseReason = ""
				e.rec.StopReason = ""
				e.rec.NextRunAt = ""
				e.rec.UpdatedAt = now.Format(time.RFC3339)
				changed = true
			}
		}
	}
	if changed {
		_ = m.saveLocked()
	}
	m.mu.Unlock()

	for _, sid := range []site.ID{site.Bilibili, site.Douyin} {
		siteCopy := sid
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.runSiteLoop(loopCtx, siteCopy)
		}()
	}
}

// Stop halts background queue workers cleanly and flushes state to disk for restart recovery.
func (m *cacheQueueManager) Stop() {
	m.mu.Lock()
	cancel := m.stopCancel
	m.stopCancel = nil
	m.started = false
	for _, e := range m.tasks {
		if e.rt.stepCancel != nil {
			e.rt.stepCancel()
			e.rt.stepCancel = nil
		}
	}
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	m.wg.Wait()

	m.mu.Lock()
	_ = m.saveLocked()
	m.mu.Unlock()
}

func (m *cacheQueueManager) pauseForExit() {
	m.Stop()
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.nowRFC3339()
	changed := false
	for _, e := range m.tasks {
		if !isUnfinishedQueueStatus(e.rec.Status) {
			continue
		}
		if e.rec.Status == CacheQueuePaused {
			continue
		}
		if e.rec.Status == CacheQueueWaitingBackoff {
			// Keep waiting_backoff intact across exit so backoff cooldown is never shortened.
			continue
		}
		e.rec.Status = CacheQueuePaused
		e.rec.PauseReason = "app_closed"
		e.rec.StopReason = "已暂停（应用关闭）"
		e.rec.UpdatedAt = now
		changed = true
	}
	if changed {
		_ = m.saveLocked()
	}
}

func (m *cacheQueueManager) pauseForSite(siteID site.ID, reasonCode, reasonMsg string) {
	m.mu.Lock()
	now := m.nowRFC3339()
	changed := false
	var notify []cacheQueueRecord
	for _, e := range m.tasks {
		if e.rec.Site != string(siteID) || !isUnfinishedQueueStatus(e.rec.Status) {
			continue
		}
		if e.rec.Status == CacheQueuePaused && e.rec.PauseReason == "user" {
			continue
		}
		if e.rt.stepCancel != nil {
			e.rt.stepCancel()
			e.rt.stepCancel = nil
		}
		if reasonCode == "app_closed" && e.rec.Status == CacheQueueWaitingBackoff {
			continue
		}
		e.rec.Status = CacheQueuePaused
		e.rec.PauseReason = sanitizeCursor(reasonCode)
		e.rec.StopReason = sanitizeQueueText(reasonMsg)
		if reasonCode != "app_closed" {
			e.rec.NextRunAt = ""
		}
		e.rec.UpdatedAt = now
		e.rt.resumePendingDownloads = len(e.rec.ActiveDownloadIDs) > 0
		changed = true
		notify = append(notify, e.rec)
	}
	if changed {
		_ = m.saveLocked()
		for _, r := range notify {
			m.notifyStateLocked(r)
		}
	}
	m.mu.Unlock()
	m.wakeSite(siteID)
}

func (m *cacheQueueManager) snapshotAuthPausedIDs(siteID site.ID) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []string
	for _, e := range m.tasks {
		if e.rec.Site == string(siteID) && e.rec.Status == CacheQueuePaused && e.rec.PauseReason == "auth_required" {
			ids = append(ids, e.rec.ID)
		}
	}
	return ids
}

func (m *cacheQueueManager) resumeAuthPausedForSite(siteID site.ID) {
	m.resumeAuthPausedFilter(siteID, nil)
}

func (m *cacheQueueManager) resumeAuthPausedIDs(siteID site.ID, allowed []string) {
	if len(allowed) == 0 {
		return
	}
	m.resumeAuthPausedFilter(siteID, allowed)
}

func (m *cacheQueueManager) resumeAuthPausedFilter(siteID site.ID, onlyIDs []string) {
	m.mu.Lock()
	now := m.nowRFC3339()
	changed := false
	var notify []cacheQueueRecord
	for _, e := range m.tasks {
		if e.rec.Site != string(siteID) || e.rec.Status != CacheQueuePaused || e.rec.PauseReason != "auth_required" {
			continue
		}
		if onlyIDs != nil && !slices.Contains(onlyIDs, e.rec.ID) {
			continue
		}
		e.rec.Status = CacheQueueQueued
		e.rec.PauseReason = ""
		e.rec.StopReason = ""
		e.rec.NextRunAt = ""
		e.rec.UpdatedAt = now
		e.rt.resumePendingDownloads = len(e.rec.ActiveDownloadIDs) > 0
		changed = true
		notify = append(notify, e.rec)
	}
	if changed {
		_ = m.saveLocked()
		for _, r := range notify {
			m.notifyStateLocked(r)
		}
	}
	m.mu.Unlock()
	if changed {
		m.wakeSite(siteID)
	}
}

func (m *cacheQueueManager) hasSiteLogin(siteID site.ID) bool {
	if m.b == nil || m.b.credMgr == nil {
		return false
	}
	return m.b.credMgr.HasActiveCredential(string(siteID))
}

func (m *cacheQueueManager) siteBackoffOrQuotaEnd(siteID site.ID, now time.Time) (bool, time.Time) {
	if m.b == nil || m.b.subs == nil {
		return false, time.Time{}
	}
	nowUTC := now.UTC()
	var blocked bool
	var end time.Time

	if inBackoff, until := m.b.subs.IsSiteInBackoff(string(siteID)); inBackoff && until.After(nowUTC) {
		blocked = true
		end = until.UTC()
	}
	if !m.b.subs.CanUseSiteQuotaTokens(string(siteID), 1) {
		blocked = true
		st := m.b.subs.GetSiteSchedulerState(string(siteID))
		cutoff := nowUTC.Add(-time.Hour).Unix()
		var oldest int64
		for _, ts := range st.TokenTimestamps {
			if ts > cutoff && (oldest == 0 || ts < oldest) {
				oldest = ts
			}
		}
		quotaEnd := nowUTC.Add(time.Hour)
		if oldest > 0 {
			quotaEnd = time.Unix(oldest+3600, 0).UTC()
		}
		if quotaEnd.After(end) {
			end = quotaEnd
		}
	}
	return blocked, end
}

func (m *cacheQueueManager) backoffResumeTimeLocked(siteID site.ID, now time.Time) time.Time {
	nowUTC := now.UTC()
	_, baseEnd := m.siteBackoffOrQuotaEnd(siteID, nowUTC)
	if baseEnd.Before(nowUTC) {
		baseEnd = nowUTC
	}
	cool := m.randDurationLocked(m.pacing.BackoffCoolMinDelay, m.pacing.BackoffCoolMaxDelay)
	return baseEnd.Add(cool)
}

func backoffReasonText(kind string) string {
	switch kind {
	case site.ErrKindRiskControl:
		return "遇到站点风控，等待退避结束"
	case site.ErrKindRateLimited:
		return "遇到站点限流，等待退避结束"
	case site.ErrKindQuota:
		return "本小时自动请求额度已用完，等待额度恢复"
	default:
		return "站点处于退避期，等待退避结束"
	}
}

func isBackoffOrQuotaKind(kind string) bool {
	switch kind {
	case site.ErrKindRiskControl, site.ErrKindRateLimited, site.ErrKindQuota, site.ErrKindBackoff:
		return true
	}
	return false
}

func (m *cacheQueueManager) enterWaitingBackoffLocked(e *cacheQueueEntry, siteID site.ID, kind string) {
	if (kind == site.ErrKindRiskControl || kind == site.ErrKindRateLimited) && m.b != nil && m.b.subs != nil {
		if inBackoff, _ := m.b.subs.IsSiteInBackoff(string(siteID)); !inBackoff {
			m.b.subs.RecordSiteAutomaticStop(string(siteID), kind)
		}
	}
	now := m.clock.Now().UTC()
	resumeAt := m.backoffResumeTimeLocked(siteID, now)
	e.rec.Status = CacheQueueWaitingBackoff
	e.rec.PauseReason = ""
	e.rec.StopReason = backoffReasonText(kind)
	e.rec.NextRunAt = resumeAt.Format(time.RFC3339)
	e.rec.UpdatedAt = now.Format(time.RFC3339)
	e.rt.resumePendingDownloads = len(e.rec.ActiveDownloadIDs) > 0
	_ = m.saveLocked()
	m.notifyStateLocked(e.rec)
}

func (m *cacheQueueManager) selectActiveSiteEntryLocked(siteID site.ID) *cacheQueueEntry {
	for _, e := range m.tasks {
		if e.rec.Site != string(siteID) {
			continue
		}
		switch e.rec.Status {
		case CacheQueueRunning, CacheQueueWaiting, CacheQueueWaitingBackoff:
			return e
		}
	}
	for _, e := range m.tasks {
		if e.rec.Site != string(siteID) || e.rec.Status != CacheQueueQueued {
			continue
		}
		if !m.hasSiteLogin(siteID) {
			e.rec.Status = CacheQueuePaused
			e.rec.PauseReason = "auth_required"
			e.rec.StopReason = "未登录或登录已失效，重新登录后自动继续"
			e.rec.NextRunAt = ""
			e.rec.UpdatedAt = m.nowRFC3339()
			_ = m.saveLocked()
			m.notifyStateLocked(e.rec)
			continue
		}
		if e.rec.RestThreshold <= 0 {
			e.rec.RestThreshold = m.pickRestThresholdLocked()
		}
		e.rec.Status = CacheQueueRunning
		e.rec.PauseReason = ""
		e.rec.StopReason = ""
		e.rec.NextRunAt = ""
		e.rec.UpdatedAt = m.nowRFC3339()
		_ = m.saveLocked()
		m.notifyStateLocked(e.rec)
		return e
	}
	return nil
}

func (m *cacheQueueManager) runSiteLoop(ctx context.Context, siteID site.ID) {
	wakeCh := m.siteWake[siteID]
	for {
		if ctx.Err() != nil {
			return
		}

		m.mu.Lock()
		entry := m.selectActiveSiteEntryLocked(siteID)
		if entry == nil {
			m.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-wakeCh:
				continue
			}
		}

		// Phase A: Scheduled wait (inter-item delay, long rest, startup delay, or backoff cooldown).
		if entry.rec.Status == CacheQueueWaiting || entry.rec.Status == CacheQueueWaitingBackoff {
			nextAt, pErr := time.Parse(time.RFC3339, entry.rec.NextRunAt)
			now := m.clock.Now().UTC()
			if pErr == nil && now.Before(nextAt) {
				select {
				case <-wakeCh:
				default:
				}
				waitDur := nextAt.Sub(now)
				tm := m.clock.NewTimer(waitDur)
				m.mu.Unlock()

				select {
				case <-ctx.Done():
					tm.Stop()
					return
				case <-wakeCh:
					tm.Stop()
					continue
				case <-tm.C():
				}
				continue
			}

			if !m.hasSiteLogin(siteID) {
				m.mu.Unlock()
				m.pauseForSite(siteID, "auth_required", "未登录或登录已失效，重新登录后自动继续")
				continue
			}
			if blocked, _ := m.siteBackoffOrQuotaEnd(siteID, m.clock.Now()); blocked {
				kind := site.ErrKindBackoff
				if !m.b.subs.CanUseSiteQuotaTokens(string(siteID), 1) {
					kind = site.ErrKindQuota
				}
				m.enterWaitingBackoffLocked(entry, siteID, kind)
				m.mu.Unlock()
				continue
			}
			entry.rec.Status = CacheQueueRunning
			entry.rec.PauseReason = ""
			entry.rec.StopReason = ""
			entry.rec.NextRunAt = ""
			entry.rec.UpdatedAt = m.nowRFC3339()
			_ = m.saveLocked()
			m.notifyStateLocked(entry.rec)
		}

		if !m.hasSiteLogin(siteID) {
			m.mu.Unlock()
			m.pauseForSite(siteID, "auth_required", "未登录或登录已失效，重新登录后自动继续")
			continue
		}

		// Phase B: Monitor or resume the single active derived download item.
		if len(entry.rec.ActiveDownloadIDs) > 0 {
			activeIDs := append([]string(nil), entry.rec.ActiveDownloadIDs...)
			needResume := entry.rt.resumePendingDownloads
			entry.rt.resumePendingDownloads = false
			m.mu.Unlock()

			if needResume && m.b != nil && m.b.dl != nil {
				for _, dlID := range activeIDs {
					if t, ok, err := m.b.dl.GetTask(dlID); err == nil && ok && t.Status == "paused" {
						_, _ = m.b.dl.Resume(dlID)
					}
				}
			}

			stillRunning := false
			var authStopped bool
			var backoffKind string
			var appClosed bool
			var userPaused bool
			allSucceeded := true

			if m.b != nil && m.b.dl != nil {
				for _, dlID := range activeIDs {
					t, ok, err := m.b.dl.GetTask(dlID)
					if err != nil || !ok {
						allSucceeded = false
						continue
					}
					switch t.Status {
					case "queued", "running", "downloading", "muxing":
						stillRunning = true
					case "completed":
						// Part succeeded.
					case "partial":
						if m.b != nil && m.b.lib != nil {
							checkKey := firstNonEmpty(t.ItemKey, entry.rec.ActiveItemKey)
							if _, exists, lErr := m.b.lib.GetItem(checkKey); lErr != nil || !exists {
								allSucceeded = false
							}
						}
					case "paused":
						allSucceeded = false
						switch {
						case t.PauseReason == "auth_required" || t.ErrorKind == site.ErrKindAuthRequired:
							authStopped = true
						case t.PauseReason == "risk_control" || isBackoffOrQuotaKind(t.PauseReason) || isBackoffOrQuotaKind(t.ErrorKind):
							backoffKind = firstNonEmpty(t.ErrorKind, t.PauseReason)
						case t.PauseReason == "app_closed":
							appClosed = true
						default:
							userPaused = true
						}
					default: // "failed", "canceled"
						allSucceeded = false
						if t.ErrorKind == site.ErrKindAuthRequired {
							authStopped = true
						} else if isBackoffOrQuotaKind(t.ErrorKind) {
							backoffKind = t.ErrorKind
						}
					}
				}
			}

			if stillRunning {
				select {
				case <-ctx.Done():
					return
				case <-wakeCh:
					continue
				}
			}
			if appClosed || ctx.Err() != nil {
				return
			}

			m.mu.Lock()
			if entry.rec.Status != CacheQueueRunning || !slices.Equal(entry.rec.ActiveDownloadIDs, activeIDs) {
				m.mu.Unlock()
				continue
			}
			if authStopped {
				m.mu.Unlock()
				m.pauseForSite(siteID, "auth_required", "登录已失效，重新登录后自动继续")
				continue
			}
			if backoffKind != "" {
				m.enterWaitingBackoffLocked(entry, siteID, backoffKind)
				m.mu.Unlock()
				continue
			}
			if userPaused {
				entry.rec.Status = CacheQueuePaused
				entry.rec.PauseReason = "user"
				entry.rec.StopReason = "已手动暂停"
				entry.rec.NextRunAt = ""
				entry.rec.UpdatedAt = m.nowRFC3339()
				entry.rt.resumePendingDownloads = true
				_ = m.saveLocked()
				m.notifyStateLocked(entry.rec)
				m.mu.Unlock()
				continue
			}

			// If this was a multi-part video with remaining parts, enqueue the next part before marking the video done.
			if allSucceeded && len(entry.rt.nextPartEnqueuers) > 0 {
				nextPart := entry.rt.nextPartEnqueuers[0]
				stepCtx, stepCancel := context.WithCancel(site.WithAutomaticRequests(ctx, "cache_queue"))
				entry.rt.stepCancel = stepCancel
				m.mu.Unlock()

				pids, pErr := nextPart(stepCtx)

				m.mu.Lock()
				stepCancel()
				if entry.rt.stepCancel != nil {
					entry.rt.stepCancel = nil
				}
				if entry.rec.Status != CacheQueueRunning || ctx.Err() != nil {
					m.mu.Unlock()
					continue
				}
				if pErr != nil {
					kind := site.KindOf(pErr)
					if kind == site.ErrKindAuthRequired {
						m.mu.Unlock()
						m.pauseForSite(siteID, "auth_required", "登录已失效，重新登录后自动继续")
						continue
					}
					if isBackoffOrQuotaKind(kind) {
						m.enterWaitingBackoffLocked(entry, siteID, kind)
						m.mu.Unlock()
						continue
					}
					entry.rt.nextPartEnqueuers = nil
					m.finishItemOutcomeLocked(entry, false)
					m.mu.Unlock()
					continue
				}
				entry.rt.nextPartEnqueuers = entry.rt.nextPartEnqueuers[1:]
				entry.rec.ActiveDownloadIDs = pids
				entry.rec.UpdatedAt = m.nowRFC3339()
				_ = m.saveLocked()
				m.mu.Unlock()
				continue
			}

			m.finishItemOutcomeLocked(entry, allSucceeded)
			m.mu.Unlock()
			continue
		}

		// Phase C: Inspect candidates or fetch next page, skipping cached/uncacheable items without waiting.
		m.advanceCandidateStep(ctx, siteID, entry)
	}
}

func (m *cacheQueueManager) finishItemOutcomeLocked(entry *cacheQueueEntry, cached bool) {
	nowStr := m.nowRFC3339()
	itemKey := entry.rec.ActiveItemKey
	title := entry.rec.ActiveTitle
	entry.rec.ActiveDownloadIDs = nil
	entry.rec.ActiveItemKey = ""
	entry.rec.ActiveTitle = ""
	entry.rt.nextPartEnqueuers = nil

	if itemKey != "" && !slices.Contains(entry.rec.ProcessedKeys, itemKey) {
		entry.rec.ProcessedKeys = append(entry.rec.ProcessedKeys, itemKey)
	}

	if cached {
		entry.rec.CachedCount++
		entry.rec.SinceRestCount++
		if itemKey != "" {
			if title == "" && m.b != nil && m.b.lib != nil {
				if meta, ok, err := m.b.lib.GetItem(itemKey); err == nil && ok && meta.Title != "" {
					title = trimQueueSummaryTitle(meta.Title)
				}
			}
			summary := CacheQueueRecentItem{
				ItemKey:  itemKey,
				Title:    sanitizeQueueText(firstNonEmpty(title, itemKey)),
				CachedAt: nowStr,
			}
			entry.rec.RecentItems = append([]CacheQueueRecentItem{summary}, entry.rec.RecentItems...)
			if len(entry.rec.RecentItems) > maxCacheQueueRecentItems {
				entry.rec.RecentItems = entry.rec.RecentItems[:maxCacheQueueRecentItems]
			}
		}
	} else {
		entry.rec.SkippedCount++
	}

	// If the current page buffer is exhausted and there are no further pages, complete immediately.
	if entry.rt.pageLoaded && entry.rt.pageIdx >= len(entry.rt.pageItems) && !entry.rt.pageNextHasMore {
		entry.rec.Cursor = sanitizeCursor(entry.rt.pageNextCursor)
		entry.rec.HasMore = false
		entry.rec.ProcessedKeys = nil
		entry.rt.pageLoaded = false
		entry.rt.pageItems = nil
		entry.rt.pageIdx = 0
		entry.rec.Status = CacheQueueCompleted
		entry.rec.PauseReason = ""
		entry.rec.StopReason = ""
		entry.rec.NextRunAt = ""
		entry.rec.UpdatedAt = nowStr
		_ = m.saveLocked()
		m.notifyStateLocked(entry.rec)
		return
	}

	if entry.rec.RestThreshold <= 0 {
		entry.rec.RestThreshold = m.pickRestThresholdLocked()
	}
	var delay time.Duration
	reason := "等待下一条"
	if cached && entry.rec.SinceRestCount >= entry.rec.RestThreshold {
		delay = m.randDurationLocked(m.pacing.LongRestMinDelay, m.pacing.LongRestMaxDelay)
		entry.rec.SinceRestCount = 0
		entry.rec.RestThreshold = m.pickRestThresholdLocked()
		reason = "已完成一批缓存，长歇等待中"
	} else {
		delay = m.randDurationLocked(m.pacing.ItemMinDelay, m.pacing.ItemMaxDelay)
	}
	nextAt := m.clock.Now().UTC().Add(delay)
	entry.rec.Status = CacheQueueWaiting
	entry.rec.PauseReason = ""
	entry.rec.StopReason = reason
	entry.rec.NextRunAt = nextAt.Format(time.RFC3339)
	entry.rec.UpdatedAt = nowStr
	_ = m.saveLocked()
	m.notifyStateLocked(entry.rec)
}

func (m *cacheQueueManager) advanceCandidateStep(ctx context.Context, siteID site.ID, entry *cacheQueueEntry) {
	// Caller holds m.mu on entry; this method unlocks m.mu before returning!
	if entry.rt.pager == nil && m.b != nil {
		pager, err := m.b.buildQueuePager(siteID, entry.rec.List, entry.rec.User)
		if err != nil {
			entry.rec.Status = CacheQueueStopped
			entry.rec.StopReason = sanitizeQueueText(err.Error())
			entry.rec.UpdatedAt = m.nowRFC3339()
			_ = m.saveLocked()
			m.notifyStateLocked(entry.rec)
			m.mu.Unlock()
			return
		}
		entry.rt.pager = pager
	}

	// Exhausted current buffered page? Advance persistent cursor.
	if entry.rt.pageLoaded && entry.rt.pageIdx >= len(entry.rt.pageItems) {
		entry.rec.Cursor = sanitizeCursor(entry.rt.pageNextCursor)
		entry.rec.HasMore = entry.rt.pageNextHasMore
		entry.rec.ProcessedKeys = nil
		entry.rt.pageLoaded = false
		entry.rt.pageItems = nil
		entry.rt.pageIdx = 0
		entry.rec.UpdatedAt = m.nowRFC3339()
		if !entry.rec.HasMore {
			entry.rec.Status = CacheQueueCompleted
			entry.rec.PauseReason = ""
			entry.rec.StopReason = ""
			entry.rec.NextRunAt = ""
			_ = m.saveLocked()
			m.notifyStateLocked(entry.rec)
			m.mu.Unlock()
			return
		}
		_ = m.saveLocked()
	}

	// Fetch next page if no buffered page is loaded.
	if !entry.rt.pageLoaded {
		if !entry.rec.HasMore {
			entry.rec.Status = CacheQueueCompleted
			entry.rec.PauseReason = ""
			entry.rec.StopReason = ""
			entry.rec.NextRunAt = ""
			entry.rec.UpdatedAt = m.nowRFC3339()
			_ = m.saveLocked()
			m.notifyStateLocked(entry.rec)
			m.mu.Unlock()
			return
		}
		if blocked, _ := m.siteBackoffOrQuotaEnd(siteID, m.clock.Now()); blocked {
			kind := site.ErrKindBackoff
			if !m.b.subs.CanUseSiteQuotaTokens(string(siteID), 1) {
				kind = site.ErrKindQuota
			}
			m.enterWaitingBackoffLocked(entry, siteID, kind)
			m.mu.Unlock()
			return
		}

		cursor := entry.rec.Cursor
		pager := entry.rt.pager
		stepCtx, stepCancel := context.WithCancel(site.WithAutomaticRequests(ctx, "cache_queue"))
		entry.rt.stepCancel = stepCancel
		m.mu.Unlock()

		items, nextCursor, nextMore, err := pager(stepCtx, cursor)

		m.mu.Lock()
		stepCancel()
		if entry.rt.stepCancel != nil {
			entry.rt.stepCancel = nil
		}
		if entry.rec.Status != CacheQueueRunning || ctx.Err() != nil {
			m.mu.Unlock()
			return
		}
		if err != nil {
			kind := site.KindOf(err)
			if kind == site.ErrKindAuthRequired {
				m.mu.Unlock()
				m.pauseForSite(siteID, "auth_required", "登录已失效，重新登录后自动继续")
				return
			}
			if isBackoffOrQuotaKind(kind) {
				m.enterWaitingBackoffLocked(entry, siteID, kind)
				m.mu.Unlock()
				return
			}
			entry.rec.Status = CacheQueueStopped
			entry.rec.StopReason = sanitizeQueueText("读取列表失败：" + err.Error())
			entry.rec.NextRunAt = ""
			entry.rec.UpdatedAt = m.nowRFC3339()
			_ = m.saveLocked()
			m.notifyStateLocked(entry.rec)
			m.mu.Unlock()
			return
		}
		if len(items) == 0 {
			entry.rec.Cursor = sanitizeCursor(nextCursor)
			entry.rec.HasMore = false
			entry.rec.ProcessedKeys = nil
			entry.rec.Status = CacheQueueCompleted
			entry.rec.PauseReason = ""
			entry.rec.StopReason = ""
			entry.rec.NextRunAt = ""
			entry.rec.UpdatedAt = m.nowRFC3339()
			_ = m.saveLocked()
			m.notifyStateLocked(entry.rec)
			m.mu.Unlock()
			return
		}
		entry.rt.pageItems = items
		entry.rt.pageIdx = 0
		entry.rt.pageNextCursor = nextCursor
		entry.rt.pageNextHasMore = nextMore
		entry.rt.pageLoaded = true
	}

	// Scan buffered page items: skip cached/uncacheable items immediately without waiting.
	var keysToCheck []string
	for i := entry.rt.pageIdx; i < len(entry.rt.pageItems); i++ {
		if k := entry.rt.pageItems[i].key; k != "" {
			keysToCheck = append(keysToCheck, k)
		}
	}
	var states map[string]string
	if m.b != nil && len(keysToCheck) > 0 {
		rawStates := m.b.States(keysToCheck)
		states = make(map[string]string, len(rawStates))
		for k, st := range rawStates {
			states[k] = st.Dot
		}
	}

	skippedAny := false
	for entry.rt.pageIdx < len(entry.rt.pageItems) {
		cand := entry.rt.pageItems[entry.rt.pageIdx]
		if cand.key == "" {
			entry.rt.pageIdx++
			entry.rec.SkippedCount++
			skippedAny = true
			continue
		}
		if entry.rt.seenKeys[cand.key] || slices.Contains(entry.rec.ProcessedKeys, cand.key) {
			entry.rt.seenKeys[cand.key] = true
			entry.rt.pageIdx++
			continue
		}
		dot := states[cand.key]
		alreadyCachedOrActive := dot == "green" || dot == "yellow" || dot == "ring"
		if cand.uncacheable || alreadyCachedOrActive {
			entry.rt.seenKeys[cand.key] = true
			entry.rt.pageIdx++
			entry.rec.SkippedCount++
			if !slices.Contains(entry.rec.ProcessedKeys, cand.key) {
				entry.rec.ProcessedKeys = append(entry.rec.ProcessedKeys, cand.key)
			}
			skippedAny = true
			continue
		}

		// Found an uncached candidate! Check backoff/quota before calling cand.enqueue.
		if blocked, _ := m.siteBackoffOrQuotaEnd(siteID, m.clock.Now()); blocked {
			if skippedAny {
				entry.rec.UpdatedAt = m.nowRFC3339()
			}
			kind := site.ErrKindBackoff
			if !m.b.subs.CanUseSiteQuotaTokens(string(siteID), 1) {
				kind = site.ErrKindQuota
			}
			m.enterWaitingBackoffLocked(entry, siteID, kind)
			m.mu.Unlock()
			return
		}

		stepCtx, stepCancel := context.WithCancel(site.WithAutomaticRequests(ctx, "cache_queue"))
		entry.rt.stepCancel = stepCancel
		if skippedAny {
			entry.rec.UpdatedAt = m.nowRFC3339()
			_ = m.saveLocked()
			skippedAny = false
		}
		m.mu.Unlock()

		enq, err := cand.enqueue(stepCtx)

		m.mu.Lock()
		stepCancel()
		if entry.rt.stepCancel != nil {
			entry.rt.stepCancel = nil
		}
		if entry.rec.Status != CacheQueueRunning || ctx.Err() != nil {
			m.mu.Unlock()
			return
		}
		if err != nil {
			kind := site.KindOf(err)
			if kind == site.ErrKindAuthRequired {
				m.mu.Unlock()
				m.pauseForSite(siteID, "auth_required", "登录已失效，重新登录后自动继续")
				return
			}
			if isBackoffOrQuotaKind(kind) {
				m.enterWaitingBackoffLocked(entry, siteID, kind)
				m.mu.Unlock()
				return
			}
			// Ordinary per-item failure (e.g. deleted/unavailable). The attempt already sent
			// site requests, so count it as skipped and keep the normal random gap before the
			// next candidate: a run of failing items must not turn into back-to-back requests.
			entry.rt.seenKeys[cand.key] = true
			entry.rt.pageIdx++
			entry.rec.ActiveItemKey = cand.key
			entry.rec.ActiveTitle = ""
			m.finishItemOutcomeLocked(entry, false)
			m.mu.Unlock()
			return
		}

		// Enqueue succeeded!
		entry.rt.seenKeys[cand.key] = true
		entry.rt.pageIdx++
		entry.rec.ActiveItemKey = sanitizeItemKey(firstNonEmpty(enq.itemKey, cand.key))
		entry.rec.ActiveTitle = sanitizeQueueText(firstNonEmpty(enq.title, cand.title))
		if len(enq.taskIDs) == 0 {
			// Synchronous completion (used by synthetic candidate tests with no async download task).
			m.finishItemOutcomeLocked(entry, true)
			m.mu.Unlock()
			return
		}
		entry.rec.ActiveDownloadIDs = append([]string(nil), enq.taskIDs...)
		entry.rt.nextPartEnqueuers = enq.nextParts
		entry.rt.resumePendingDownloads = false
		entry.rec.UpdatedAt = m.nowRFC3339()
		_ = m.saveLocked()
		m.notifyStateLocked(entry.rec)
		m.mu.Unlock()
		return
	}

	if skippedAny {
		entry.rec.UpdatedAt = m.nowRFC3339()
		_ = m.saveLocked()
		m.notifyStateLocked(entry.rec)
	}
	m.mu.Unlock()
}

// ListCacheQueue returns all background cache queue tasks in queue order.
func (b *Backend) ListCacheQueue() []CacheQueueTask {
	if b.requireAnyLogin() != nil || b.queue == nil {
		return nil
	}
	b.queue.mu.Lock()
	defer b.queue.mu.Unlock()
	out := make([]CacheQueueTask, 0, len(b.queue.tasks))
	for _, e := range b.queue.tasks {
		out = append(out, b.queue.toDTOLocked(e))
	}
	return out
}

// FindCacheQueueTask reports whether (siteID, user, list) already has an unfinished task in the cache queue.
func (b *Backend) FindCacheQueueTask(siteID, user, list string) (CacheQueueTask, bool) {
	if b.requireAnyLogin() != nil || b.queue == nil {
		return CacheQueueTask{}, false
	}
	normSite, normList, normUser, err := normalizeCacheQueueTarget(siteID, list, user)
	if err != nil {
		return CacheQueueTask{}, false
	}
	b.queue.mu.Lock()
	defer b.queue.mu.Unlock()
	for _, e := range b.queue.tasks {
		if e.rec.Site == string(normSite) && e.rec.User == normUser && e.rec.List == normList && isUnfinishedQueueStatus(e.rec.Status) {
			return b.queue.toDTOLocked(e), true
		}
	}
	return CacheQueueTask{}, false
}

// EnqueueCacheQueue creates or deduplicates a background cache queue task for one creator's list.
func (b *Backend) EnqueueCacheQueue(ctx context.Context, req CacheQueueRequest) (CacheQueueTask, error) {
	_ = ctx
	normSite, normList, normUser, err := normalizeCacheQueueTarget(req.Site, req.List, req.User)
	if err != nil {
		return CacheQueueTask{}, err
	}
	if err := b.requireSiteLogin(normSite); err != nil {
		return CacheQueueTask{}, err
	}
	loaded, pager, err := b.buildQueueLoadedAndPager(normSite, normList, normUser, req)
	if err != nil {
		return CacheQueueTask{}, err
	}
	if strings.TrimSpace(req.AvatarURL) != "" {
		b.rememberQueueAvatar(string(normSite), normUser, req.AvatarURL)
	}
	return b.enqueueCacheQueueWithPager(normSite, normList, normUser, req, loaded, pager)
}

// queueListLabel names the list; a run a full-cache rule started says so.
func queueListLabel(rec cacheQueueRecord) string {
	label := cacheQueueListLabel(rec.Site, rec.List)
	if rec.Origin == QueueOriginFullCache {
		return "定期全部缓存 · " + label
	}
	return label
}

func queueOrigin(o string) string {
	if o == QueueOriginFullCache {
		return o
	}
	return ""
}

func (b *Backend) enqueueCacheQueueWithPager(
	normSite site.ID,
	normList, normUser string,
	req CacheQueueRequest,
	loaded []queueCandidate,
	pager queuePager,
) (CacheQueueTask, error) {
	if b.queue == nil {
		return CacheQueueTask{}, fmt.Errorf("service: cache queue not initialized")
	}
	m := b.queue
	m.mu.Lock()

	now := m.nowRFC3339()
	avatarKey := avatarFileName(string(normSite), normUser)
	cleanName := sanitizeQueueText(req.UserName)

	// 1. Deduplicate if an unfinished task already exists for (site, user, list).
	for _, e := range m.tasks {
		if e.rec.Site == string(normSite) && e.rec.User == normUser && e.rec.List == normList && isUnfinishedQueueStatus(e.rec.Status) {
			changed := false
			if cleanName != "" && e.rec.UserName != cleanName {
				e.rec.UserName = cleanName
				changed = true
			}
			if avatarKey != "" && e.rec.AvatarKey != avatarKey {
				e.rec.AvatarKey = avatarKey
				changed = true
			}
			if changed {
				e.rec.UpdatedAt = now
				_ = m.saveLocked()
			}
			dto := m.toDTOLocked(e)
			m.mu.Unlock()
			return dto, nil
		}
	}

	// 2. Remove any previous finished (completed/stopped) entry for the same (site, user, list) so restarting keeps one clean entry.
	createdAt := now
	taskID := makeCacheQueueID(string(normSite), normList, normUser)
	for i, e := range m.tasks {
		if e.rec.Site == string(normSite) && e.rec.User == normUser && e.rec.List == normList {
			if cleanName == "" {
				cleanName = e.rec.UserName
			}
			taskID = e.rec.ID
			m.tasks = append(m.tasks[:i], m.tasks[i+1:]...)
			break
		}
	}

	rec := cacheQueueRecord{
		ID:            taskID,
		Site:          string(normSite),
		User:          normUser,
		UserName:      cleanName,
		AvatarKey:     avatarKey,
		List:          normList,
		Origin:        queueOrigin(req.Origin),
		Cursor:        "",
		HasMore:       true,
		RestThreshold: m.pickRestThresholdLocked(),
		Status:        CacheQueueQueued,
		CreatedAt:     createdAt,
		UpdatedAt:     now,
	}
	rt := cacheQueueRuntime{
		pager:    pager,
		seenKeys: make(map[string]bool),
	}
	if len(loaded) > 0 {
		rt.pageItems = loaded
		rt.pageIdx = 0
		rt.pageNextCursor = sanitizeCursor(req.Cursor)
		rt.pageNextHasMore = req.HasMore && (normSite == site.Bilibili && normList == "videos" || rt.pageNextCursor != "")
		rt.pageLoaded = true
	} else if cleanCur := sanitizeCursor(req.Cursor); cleanCur != "" {
		rec.Cursor = cleanCur
		rec.HasMore = req.HasMore
	}

	entry := &cacheQueueEntry{rec: rec, rt: rt}
	m.tasks = append(m.tasks, entry)
	if err := m.saveLocked(); err != nil {
		m.mu.Unlock()
		return CacheQueueTask{}, err
	}
	m.notifyStateLocked(entry.rec)
	dto := m.toDTOLocked(entry)
	m.mu.Unlock()

	m.wakeSite(normSite)
	return dto, nil
}

// PauseCacheQueue pauses an unfinished background cache queue task and any derived download task in flight.
func (b *Backend) PauseCacheQueue(id string) (CacheQueueTask, error) {
	if err := b.requireAnyLogin(); err != nil {
		return CacheQueueTask{}, err
	}
	if b.queue == nil {
		return CacheQueueTask{}, &site.Error{Kind: site.ErrKindUnavailable, Message: "未找到该缓存队列任务"}
	}
	target := strings.TrimSpace(id)
	m := b.queue
	m.mu.Lock()
	var found *cacheQueueEntry
	for _, e := range m.tasks {
		if e.rec.ID == target {
			found = e
			break
		}
	}
	if found == nil {
		m.mu.Unlock()
		return CacheQueueTask{}, &site.Error{Kind: site.ErrKindUnavailable, Message: "未找到该缓存队列任务"}
	}
	if !isUnfinishedQueueStatus(found.rec.Status) {
		m.mu.Unlock()
		return CacheQueueTask{}, &site.Error{Kind: site.ErrKindBadRequest, Message: "该缓存队列任务已结束"}
	}
	if found.rt.stepCancel != nil {
		found.rt.stepCancel()
		found.rt.stepCancel = nil
	}
	activeIDs := append([]string(nil), found.rec.ActiveDownloadIDs...)
	found.rec.Status = CacheQueuePaused
	found.rec.PauseReason = "user"
	found.rec.StopReason = "已手动暂停"
	found.rec.NextRunAt = ""
	found.rec.UpdatedAt = m.nowRFC3339()
	found.rt.resumePendingDownloads = len(activeIDs) > 0
	if err := m.saveLocked(); err != nil {
		m.mu.Unlock()
		return CacheQueueTask{}, err
	}
	m.notifyStateLocked(found.rec)
	dto := m.toDTOLocked(found)
	siteID := site.ID(found.rec.Site)
	m.mu.Unlock()

	if b.dl != nil {
		for _, dlID := range activeIDs {
			if t, ok, err := b.dl.GetTask(dlID); err == nil && ok && (t.Status == "queued" || t.Status == "running") {
				_, _ = b.dl.Pause(dlID)
			}
		}
	}
	m.wakeSite(siteID)
	return dto, nil
}

// ResumeCacheQueue resumes a paused background cache queue task on its site.
func (b *Backend) ResumeCacheQueue(id string) (CacheQueueTask, error) {
	if b.queue == nil {
		return CacheQueueTask{}, &site.Error{Kind: site.ErrKindUnavailable, Message: "未找到该缓存队列任务"}
	}
	target := strings.TrimSpace(id)
	m := b.queue
	m.mu.Lock()
	var found *cacheQueueEntry
	for _, e := range m.tasks {
		if e.rec.ID == target {
			found = e
			break
		}
	}
	if found == nil {
		m.mu.Unlock()
		return CacheQueueTask{}, &site.Error{Kind: site.ErrKindUnavailable, Message: "未找到该缓存队列任务"}
	}
	siteID := site.ID(found.rec.Site)
	m.mu.Unlock()

	if err := b.requireSiteLogin(siteID); err != nil {
		return CacheQueueTask{}, err
	}

	m.mu.Lock()
	if !isUnfinishedQueueStatus(found.rec.Status) {
		m.mu.Unlock()
		return CacheQueueTask{}, &site.Error{Kind: site.ErrKindBadRequest, Message: "该缓存队列任务已结束，请重新加入队列"}
	}
	if found.rec.Status != CacheQueuePaused {
		dto := m.toDTOLocked(found)
		m.mu.Unlock()
		return dto, nil
	}
	found.rec.Status = CacheQueueQueued
	found.rec.PauseReason = ""
	found.rec.StopReason = ""
	found.rec.NextRunAt = ""
	found.rec.UpdatedAt = m.nowRFC3339()
	found.rt.resumePendingDownloads = len(found.rec.ActiveDownloadIDs) > 0
	if err := m.saveLocked(); err != nil {
		m.mu.Unlock()
		return CacheQueueTask{}, err
	}
	m.notifyStateLocked(found.rec)
	dto := m.toDTOLocked(found)
	m.mu.Unlock()

	m.wakeSite(siteID)
	return dto, nil
}

// CancelCacheQueue stops scheduling a background cache queue task and cancels its single unfinished
// derived download task (if any) without ever deleting items already cached in the local media library.
func (b *Backend) CancelCacheQueue(id string) (CacheQueueTask, error) {
	if err := b.requireAnyLogin(); err != nil {
		return CacheQueueTask{}, err
	}
	if b.queue == nil {
		return CacheQueueTask{}, &site.Error{Kind: site.ErrKindUnavailable, Message: "未找到该缓存队列任务"}
	}
	target := strings.TrimSpace(id)
	m := b.queue
	m.mu.Lock()
	var found *cacheQueueEntry
	for _, e := range m.tasks {
		if e.rec.ID == target {
			found = e
			break
		}
	}
	if found == nil {
		m.mu.Unlock()
		return CacheQueueTask{}, &site.Error{Kind: site.ErrKindUnavailable, Message: "未找到该缓存队列任务"}
	}
	if !isUnfinishedQueueStatus(found.rec.Status) {
		dto := m.toDTOLocked(found)
		m.mu.Unlock()
		return dto, nil
	}
	if found.rt.stepCancel != nil {
		found.rt.stepCancel()
		found.rt.stepCancel = nil
	}
	activeIDs := append([]string(nil), found.rec.ActiveDownloadIDs...)
	siteID := site.ID(found.rec.Site)
	found.rt.nextPartEnqueuers = nil
	found.rt.resumePendingDownloads = false
	found.rec.ActiveDownloadIDs = nil
	found.rec.ActiveItemKey = ""
	found.rec.ActiveTitle = ""
	found.rec.Status = CacheQueueStopped
	found.rec.PauseReason = ""
	found.rec.StopReason = "已取消"
	found.rec.NextRunAt = ""
	found.rec.UpdatedAt = m.nowRFC3339()
	if err := m.saveLocked(); err != nil {
		m.mu.Unlock()
		return CacheQueueTask{}, err
	}
	m.notifyStateLocked(found.rec)
	dto := m.toDTOLocked(found)
	m.mu.Unlock()

	if b.dl != nil {
		for _, dlID := range activeIDs {
			if t, ok, err := b.dl.GetTask(dlID); err == nil && ok && t.Status != "completed" && t.Status != "partial" && t.Status != "canceled" {
				_, _ = b.dl.Cancel(dlID)
			}
		}
	}
	b.pruneAccountAvatars()
	m.wakeSite(siteID)
	return dto, nil
}

// RemoveCacheQueue deletes a finished (completed, canceled or stopped) cache
// queue task from the task list. Unfinished tasks must be canceled first.
// Items it cached stay in the media library.
func (b *Backend) RemoveCacheQueue(id string) error {
	_, err := b.removeCacheQueues(func(rec cacheQueueRecord) bool { return rec.ID == strings.TrimSpace(id) }, true)
	return err
}

// RemoveFinishedCacheQueues deletes every finished cache queue task and
// returns how many were removed.
func (b *Backend) RemoveFinishedCacheQueues() (int, error) {
	return b.removeCacheQueues(func(rec cacheQueueRecord) bool { return !isUnfinishedQueueStatus(rec.Status) }, false)
}

func (b *Backend) removeCacheQueues(match func(cacheQueueRecord) bool, single bool) (int, error) {
	if b.queue == nil {
		if single {
			return 0, &site.Error{Kind: site.ErrKindUnavailable, Message: "未找到该缓存队列任务"}
		}
		return 0, nil
	}
	m := b.queue
	m.mu.Lock()
	kept := make([]*cacheQueueEntry, 0, len(m.tasks))
	var removed []cacheQueueRecord
	for _, e := range m.tasks {
		if !match(e.rec) {
			kept = append(kept, e)
			continue
		}
		if isUnfinishedQueueStatus(e.rec.Status) {
			if single {
				m.mu.Unlock()
				return 0, &site.Error{Kind: site.ErrKindBadRequest, Message: "缓存队列还在进行，请先取消再移除"}
			}
			kept = append(kept, e)
			continue
		}
		removed = append(removed, e.rec)
	}
	if len(removed) == 0 {
		m.mu.Unlock()
		if single {
			return 0, &site.Error{Kind: site.ErrKindUnavailable, Message: "未找到该缓存队列任务"}
		}
		return 0, nil
	}
	prev := m.tasks
	m.tasks = kept
	if err := m.saveLocked(); err != nil {
		m.tasks = prev
		m.mu.Unlock()
		return 0, err
	}
	for _, rec := range removed {
		rec.Status = "removed"
		m.notifyStateLocked(rec)
	}
	m.mu.Unlock()
	b.pruneAccountAvatars()
	return len(removed), nil
}

func (b *Backend) snapshotAuthPausedQueueIDs(siteID site.ID) []string {
	if b.queue == nil {
		return nil
	}
	return b.queue.snapshotAuthPausedIDs(siteID)
}

func (b *Backend) resumeAuthPausedQueueIDs(siteID site.ID, ids []string) {
	if b.queue != nil {
		b.queue.resumeAuthPausedIDs(siteID, ids)
	}
}
