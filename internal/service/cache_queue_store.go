package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
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

	cacheQueueFileName          = "cache-queue.json"
	cacheQueueSchemaVersion     = 3
	maxCacheQueueRecentItems    = 30
	CacheQueueListWeiboBackfill = "weibo_backfill"
	CacheQueueKindWeiboBackfill = "weibo_backfill"
	CacheQueueOriginBackfill    = "backfill"
	CacheQueueBackfillUser      = "all"

	backfillPhaseOfflineLinks       = "offline_links"
	backfillPhaseFastCDN            = "fast_cdn"
	backfillPhaseSubscribedTimeline = "subscribed_timeline"
	backfillPhaseFallbackDetail     = "fallback_detail"
)

// CacheQueuePacing centralizes all conservative anti-risk-control delays and thresholds (Q-017 / DEC-089).
type CacheQueuePacing struct {
	ItemMinDelay        time.Duration
	ItemMaxDelay        time.Duration
	ZeroAPIItemMinDelay time.Duration
	ZeroAPIItemMaxDelay time.Duration
	FastCDNMinDelay     time.Duration
	FastCDNMaxDelay     time.Duration
	LongRestMinItems    int
	LongRestMaxItems    int
	LongRestMinDelay    time.Duration
	LongRestMaxDelay    time.Duration
	BackoffCoolMinDelay time.Duration
	BackoffCoolMaxDelay time.Duration
	QuotaCoolMinDelay   time.Duration
	QuotaCoolMaxDelay   time.Duration
	StartupMinDelay     time.Duration
	StartupMaxDelay     time.Duration
}

// DefaultCacheQueuePacing defines the default Q-017 pacing intervals for background list caching,
// plus the DEC-079 user-authorized 2–5s fast pacing for pure-CDN /oslarge/ backfill.
var DefaultCacheQueuePacing = CacheQueuePacing{
	ItemMinDelay:        40 * time.Second,
	ItemMaxDelay:        150 * time.Second,
	FastCDNMinDelay:     2 * time.Second,
	FastCDNMaxDelay:     5 * time.Second,
	LongRestMinItems:    15,
	LongRestMaxItems:    25,
	LongRestMinDelay:    10 * time.Minute,
	LongRestMaxDelay:    25 * time.Minute,
	BackoffCoolMinDelay: 10 * time.Minute,
	BackoffCoolMaxDelay: 30 * time.Minute,
	QuotaCoolMinDelay:   15 * time.Second,
	QuotaCoolMaxDelay:   60 * time.Second,
	StartupMinDelay:     1 * time.Minute,
	StartupMaxDelay:     5 * time.Minute,
}

// DefaultWeiboCacheQueuePacing defines Weibo-specific background cache queue pacing (DEC-089):
// 0-API list-reused items wait 8–20s, JSON-API items wait 20–45s, long rest every 30–50 items
// for 3–8m, post-backoff cooldown 2–5m, post-quota cooldown 15–60s, startup wait 15–60s.
var DefaultWeiboCacheQueuePacing = CacheQueuePacing{
	ItemMinDelay:        20 * time.Second,
	ItemMaxDelay:        45 * time.Second,
	ZeroAPIItemMinDelay: 8 * time.Second,
	ZeroAPIItemMaxDelay: 20 * time.Second,
	FastCDNMinDelay:     2 * time.Second,
	FastCDNMaxDelay:     5 * time.Second,
	LongRestMinItems:    30,
	LongRestMaxItems:    50,
	LongRestMinDelay:    3 * time.Minute,
	LongRestMaxDelay:    8 * time.Minute,
	BackoffCoolMinDelay: 2 * time.Minute,
	BackoffCoolMaxDelay: 5 * time.Minute,
	QuotaCoolMinDelay:   15 * time.Second,
	QuotaCoolMaxDelay:   60 * time.Second,
	StartupMinDelay:     15 * time.Second,
	StartupMaxDelay:     60 * time.Second,
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

// CacheQueueRequest asks to enqueue an entire creator list ("dynamics", "videos", "works", or "posts")
// for conservative newest-to-oldest background caching.
type CacheQueueRequest struct {
	Site           string                   `json:"site"`
	List           string                   `json:"list"`
	User           string                   `json:"user"`
	UserName       string                   `json:"userName"`
	AvatarURL      string                   `json:"avatarUrl"`
	Dynamics       []bilibili.DynamicItem   `json:"-"`
	Videos         []bilibili.UserVideoItem `json:"-"`
	Works          []DouyinWorkCardDTO      `json:"-"`
	WeiboPosts     []WeiboPostView          `json:"-"`
	XPosts         []XPostDTO               `json:"-"`
	Cursor         string                   `json:"cursor"`
	HasMore        bool                     `json:"hasMore"`
	CompletionNote string                   `json:"completionNote,omitempty"`
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
	Kind             string                 `json:"kind,omitempty"`
	BackfillPhase    string                 `json:"backfillPhase,omitempty"`
	BackfillTotal    int                    `json:"backfillTotal,omitempty"`
	Origin           string                 `json:"origin,omitempty"`
	Status           string                 `json:"status"`
	StatusText       string                 `json:"statusText"`
	PauseReason      string                 `json:"pauseReason,omitempty"`
	StopReason       string                 `json:"stopReason,omitempty"`
	Note             string                 `json:"note,omitempty"`
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
	ID                 string                 `json:"id"`
	Site               string                 `json:"site"`
	User               string                 `json:"user"`
	UserName           string                 `json:"userName,omitempty"`
	AvatarKey          string                 `json:"avatarKey,omitempty"`
	List               string                 `json:"list"`
	Kind               string                 `json:"kind,omitempty"`
	Origin             string                 `json:"origin,omitempty"`
	BackfillPhase      string                 `json:"backfillPhase,omitempty"`
	BackfillTotal      int                    `json:"backfillTotal,omitempty"`
	BackfillSubUIDs    []string               `json:"backfillSubUids,omitempty"`
	BackfillSubIndex   int                    `json:"backfillSubIndex,omitempty"`
	BackfillSubCursor  string                 `json:"backfillSubCursor,omitempty"`
	Cursor             string                 `json:"cursor,omitempty"`
	HasMore            bool                   `json:"hasMore"`
	ProcessedKeys      []string               `json:"processedKeys,omitempty"`
	CachedCount        int                    `json:"cachedCount"`
	SkippedCount       int                    `json:"skippedCount"`
	SinceRestCount     int                    `json:"sinceRestCount,omitempty"`
	RestThreshold      int                    `json:"restThreshold,omitempty"`
	ActiveDownloadIDs  []string               `json:"activeDownloadIds,omitempty"`
	ActivePendingPages []int                  `json:"activePendingPages,omitempty"`
	ActiveItemKey      string                 `json:"activeItemKey,omitempty"`
	ActiveTitle        string                 `json:"activeTitle,omitempty"`
	RecentItems        []CacheQueueRecentItem `json:"recentItems,omitempty"`
	Status             string                 `json:"status"`
	PauseReason        string                 `json:"pauseReason,omitempty"`
	StopReason         string                 `json:"stopReason,omitempty"`
	Note               string                 `json:"note,omitempty"`
	NextRunAt          string                 `json:"nextRunAt,omitempty"`
	CreatedAt          string                 `json:"createdAt"`
	UpdatedAt          string                 `json:"updatedAt"`
}

type cacheQueueRuntime struct {
	pager                  queuePager
	pageItems              []queueCandidate
	pageIdx                int
	pageNextCursor         string
	pageNextHasMore        bool
	pageCompletionNote     string
	pageLoaded             bool
	seenKeys               map[string]bool
	nextPartEnqueuers      []queuePartEnqueuer
	resumePendingDownloads bool
	lastItemZeroAPI        bool
	manualProbeOnce        bool
	stepCancel             context.CancelFunc
	backfillMatched        []backfillMatchedItem
	backfillPendingCursor  string
	backfillPendingUIDDone bool
}

type cacheQueueEntry struct {
	rec cacheQueueRecord
	rt  cacheQueueRuntime
}

type cacheQueueManager struct {
	b            *Backend
	path         string
	clock        CacheQueueClock
	rng          CacheQueueRand
	pacing       CacheQueuePacing
	customPacing bool

	mu                sync.Mutex
	tasks             []*cacheQueueEntry
	fileExists        bool
	started           bool
	stopCancel        context.CancelFunc
	siteWake          map[site.ID]chan struct{}
	siteCooldownUntil map[site.ID]time.Time
	wg                sync.WaitGroup
}

var (
	queueFullURLPattern   = regexp.MustCompile(`(?i)https?://[^\s"'<>]+`)
	queueQueryTailPattern = regexp.MustCompile(`\?[A-Za-z0-9_.-]+=[^\s"'<>]*`)
	queueSecretKVPattern  = regexp.MustCompile(`(?i)\b(?:` + secretKeyNamesPattern + `)["'\\]*\s*[=:][^\s;"'<>]*|\btrace-remote:\S+|\bbearer\s+\S+`)
	xQueueCursorPattern   = regexp.MustCompile(`^[A-Za-z0-9+/|_.-]{1,512}={0,2}$`)
)

func newCacheQueueManager(b *Backend, path string, clock CacheQueueClock, rng CacheQueueRand, pacing *CacheQueuePacing) (*cacheQueueManager, string, error) {
	if clock == nil {
		clock = realCacheQueueClock{}
	}
	if rng == nil {
		rng = rand.IntN
	}
	cfg := DefaultCacheQueuePacing
	customPacing := false
	if pacing != nil {
		cfg = *pacing
		customPacing = true
	}
	siteWake := make(map[site.ID]chan struct{}, len(site.All()))
	for _, sid := range site.All() {
		siteWake[sid] = make(chan struct{}, 1)
	}
	m := &cacheQueueManager{
		b:                 b,
		path:              path,
		clock:             clock,
		rng:               rng,
		pacing:            cfg,
		customPacing:      customPacing,
		siteWake:          siteWake,
		siteCooldownUntil: make(map[site.ID]time.Time, len(site.All())),
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
	if doc.Version < cacheQueueSchemaVersion {
		oldVersion := doc.Version
		if oldVersion <= 0 {
			oldVersion = 1
		}
		backupPath := fmt.Sprintf("%s.premigrate-%d", m.path, oldVersion)
		if info, statErr := os.Stat(backupPath); statErr != nil || info.IsDir() {
			if _, bErr := storage.BackupBeforeMigration(m.path, oldVersion); bErr != nil {
				return "", fmt.Errorf("service: back up %s before migration: %w", cacheQueueFileName, bErr)
			}
		}
		migratedDoc := m.buildPersistDocLocked()
		if err := storage.WriteMigratedJSONAtomic(m.path, migratedDoc, 0o600); err != nil {
			return "", err
		}
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

// sanitizeQueueCursor validates pagination cursors persisted in cache-queue.json.
// Allows a colon for Weibo's "<page>:<since_id>" format while rejecting URLs,
// query strings, whitespace, and path separators.
func sanitizeQueueCursor(c string) string {
	c = strings.TrimSpace(c)
	if c == "" || strings.Contains(c, "://") || strings.ContainsAny(c, "?=&;/\\ \t\r\n\"'<>") {
		return ""
	}
	return c
}

// sanitizeQueueCursorForSite validates site-specific cursors for cache-queue.json.
// X GraphQL Bottom cursors may use Base64 ("+", "/", trailing "=") or pipe tokens,
// so site.X accepts a strict printable token pattern while still rejecting URLs,
// query strings, control characters, and credential key-value substrings.
func sanitizeQueueCursorForSite(siteID site.ID, c string) string {
	if siteID == site.X {
		c = strings.TrimSpace(c)
		if c == "" || strings.Contains(c, "://") || !xQueueCursorPattern.MatchString(c) {
			return ""
		}
		if queueSecretKVPattern.MatchString(c) {
			return ""
		}
		return c
	}
	return sanitizeQueueCursor(c)
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
	if r.Origin != QueueOriginFullCache && r.Origin != CacheQueueOriginBackfill {
		r.Origin = ""
	}
	if r.List == CacheQueueListWeiboBackfill {
		r.Kind = CacheQueueKindWeiboBackfill
		switch r.BackfillPhase {
		case backfillPhaseOfflineLinks, backfillPhaseFastCDN, backfillPhaseSubscribedTimeline, backfillPhaseFallbackDetail:
		default:
			r.BackfillPhase = ""
		}
		if r.BackfillTotal < 0 {
			r.BackfillTotal = 0
		}
		if r.BackfillSubIndex < 0 {
			r.BackfillSubIndex = 0
		}
		var cleanUIDs []string
		for _, u := range r.BackfillSubUIDs {
			if norm, uErr := site.NormalizeUID(site.Weibo, u); uErr == nil && !slices.Contains(cleanUIDs, norm) {
				cleanUIDs = append(cleanUIDs, norm)
			}
		}
		r.BackfillSubUIDs = cleanUIDs
		r.BackfillSubCursor = sanitizeQueueCursor(r.BackfillSubCursor)
	} else {
		r.Kind = ""
		r.BackfillPhase = ""
		r.BackfillTotal = 0
		r.BackfillSubUIDs = nil
		r.BackfillSubIndex = 0
		r.BackfillSubCursor = ""
	}
	if !avatarFileRe.MatchString(r.AvatarKey) {
		r.AvatarKey = ""
	}
	r.Cursor = sanitizeQueueCursorForSite(siteID, r.Cursor)
	r.ActiveItemKey = sanitizeItemKey(r.ActiveItemKey)
	r.ActiveTitle = sanitizeQueueText(r.ActiveTitle)
	r.StopReason = sanitizeQueueText(r.StopReason)
	r.Note = sanitizeQueueText(r.Note)
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

	var cleanPages []int
	for _, p := range r.ActivePendingPages {
		if p > 0 && !slices.Contains(cleanPages, p) {
			cleanPages = append(cleanPages, p)
		}
	}
	r.ActivePendingPages = cleanPages

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

func (m *cacheQueueManager) buildPersistDocLocked() cacheQueueFile {
	doc := cacheQueueFile{
		Version: cacheQueueSchemaVersion,
		Tasks:   make([]cacheQueueRecord, 0, len(m.tasks)),
	}
	for _, e := range m.tasks {
		rec := sanitizeQueueRecord(e.rec)
		rec.UserName = m.sanitizeText(rec.UserName)
		rec.ActiveTitle = m.sanitizeText(rec.ActiveTitle)
		rec.StopReason = m.sanitizeText(rec.StopReason)
		rec.Note = m.sanitizeText(rec.Note)
		for i := range rec.RecentItems {
			rec.RecentItems[i].Title = m.sanitizeText(rec.RecentItems[i].Title)
		}
		doc.Tasks = append(doc.Tasks, rec)
	}
	return doc
}

func (m *cacheQueueManager) saveLocked() error {
	if strings.TrimSpace(m.path) == "" {
		return nil
	}
	if len(m.tasks) == 0 && !m.fileExists {
		return nil
	}
	doc := m.buildPersistDocLocked()
	if err := storage.WriteJSONAtomic(m.path, doc, 0o600); err != nil {
		return err
	}
	m.fileExists = true
	return nil
}

func (m *cacheQueueManager) saveLoggedLocked(op string) {
	if err := m.saveLocked(); err != nil && m.b != nil {
		m.b.logError("cache_queue."+op, err)
	}
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

func (m *cacheQueueManager) pacingForSite(siteID site.ID) CacheQueuePacing {
	if !m.customPacing && siteID == site.Weibo {
		return DefaultWeiboCacheQueuePacing
	}
	return m.pacing
}

func (m *cacheQueueManager) pickRestThresholdLocked() int {
	return m.pickRestThresholdForSiteLocked("")
}

func (m *cacheQueueManager) pickRestThresholdForSiteLocked(siteID site.ID) int {
	p := m.pacingForSite(siteID)
	return m.randIntInclusiveLocked(p.LongRestMinItems, p.LongRestMaxItems)
}

func (m *cacheQueueManager) fastCDNDelayLocked() time.Duration {
	minD, maxD := m.pacing.FastCDNMinDelay, m.pacing.FastCDNMaxDelay
	if minD <= 0 && maxD <= 0 {
		if m.pacing.ItemMinDelay > 0 && m.pacing.ItemMinDelay < 2*time.Second {
			minD, maxD = m.pacing.ItemMinDelay, m.pacing.ItemMaxDelay
		} else {
			minD, maxD = DefaultCacheQueuePacing.FastCDNMinDelay, DefaultCacheQueuePacing.FastCDNMaxDelay
		}
	}
	if maxD < minD {
		maxD = minD
	}
	return m.randDurationLocked(minD, maxD)
}

func (m *cacheQueueManager) nowRFC3339() string {
	return m.clock.Now().UTC().Format(time.RFC3339)
}

func makeCacheQueueID(siteID, list, user string) string {
	sum := sha256.Sum256([]byte(siteID + ":" + list + ":" + user))
	return "cq_" + hex.EncodeToString(sum[:8])
}

func bilibiliVideoIDFromItemKey(itemKey string) (string, bool) {
	const prefix = "bilibili:video:"
	if !strings.HasPrefix(itemKey, prefix) {
		return "", false
	}
	bvid := strings.TrimSpace(strings.TrimPrefix(itemKey, prefix))
	return bvid, bvid != ""
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
		if rec.Note != "" {
			return "已完成（" + rec.Note + "）"
		}
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
		Kind:             rec.Kind,
		BackfillPhase:    rec.BackfillPhase,
		BackfillTotal:    rec.BackfillTotal,
		Origin:           rec.Origin,
		Status:           rec.Status,
		StatusText:       m.statusTextLocked(rec),
		PauseReason:      rec.PauseReason,
		StopReason:       rec.StopReason,
		Note:             rec.Note,
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

// notifyState logs and publishes a queue state transition.
// Caller MUST NOT hold m.mu so synchronous event subscribers can safely query queue APIs.
func (m *cacheQueueManager) notifyState(rec cacheQueueRecord) {
	if m.b == nil {
		return
	}
	m.b.logInfo(fmt.Sprintf("cache_queue.%s.%s id=%s state=%s cached=%d skipped=%d",
		rec.Site, rec.List, rec.ID, rec.Status, rec.CachedCount, rec.SkippedCount))
	if m.b.events != nil {
		m.b.events.PublishSite(EventCacheQueueUpdated, rec.Site, rec.ID+":"+rec.Status)
	}
}

func (m *cacheQueueManager) notifyStates(recs []cacheQueueRecord) {
	for _, r := range recs {
		m.notifyState(r)
	}
}
