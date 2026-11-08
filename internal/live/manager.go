package live

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/subscription"
)

const (
	maxM3U8PlaylistBytes = 1024 * 1024 // 1 MiB playlist limit (live.md §2.3)
	defaultMinFreeBytes  = 1 << 30     // 1 GiB minimum free disk space (live.md §2.8)
	statusCacheTTL       = 60 * time.Second
)

var errSegmentRollover = errors.New("live: segment rollover required")

// RoomState describes the current live status and recording state of a room/creator.
type RoomState struct {
	UID               int64   `json:"uid"`
	RoomID            int64   `json:"roomId"`
	ShortID           int64   `json:"shortId,omitempty"`
	Uname             string  `json:"uname"`
	Face              string  `json:"face"`
	Title             string  `json:"title"`
	Cover             string  `json:"cover,omitempty"`
	AreaName          string  `json:"areaName,omitempty"`
	LiveStatus        int     `json:"liveStatus"` // 0=offline, 1=live, 2=carousel
	LiveSince         int64   `json:"liveSince,omitempty"`
	Source            string  `json:"source,omitempty"`
	StatusText        string  `json:"statusText"` // "未开播", "直播中", "轮播中"
	Quality           int     `json:"quality,omitempty"`
	QualityName       string  `json:"qualityName,omitempty"`
	Codec             string  `json:"codec,omitempty"`
	Recording         bool    `json:"recording"`
	RecordSession     string  `json:"recordSession,omitempty"`
	RecordDurationSec float64 `json:"recordDurationSec,omitempty"`
	RecordSpeedBps    int64   `json:"recordSpeedBps,omitempty"`
	RecordBytes       int64   `json:"recordBytes,omitempty"`
	RecordSegments    int     `json:"recordSegments,omitempty"`
	RecordItemDir     string  `json:"recordItemDir,omitempty"`
	StopReason        string  `json:"stopReason,omitempty"`
	LastError         string  `json:"lastError,omitempty"`
	UpdatedAt         string  `json:"updatedAt"`
}

// Notification represents a transition from offline/carousel (0/2) to live (1).
type Notification struct {
	UID       int64  `json:"uid"`
	RoomID    int64  `json:"roomId"`
	Uname     string `json:"uname"`
	Title     string `json:"title"`
	StartedAt string `json:"startedAt"`
}

type recordSession struct {
	sessionID       string
	roomID          int64
	uid             int64
	uname           string
	face            string
	title           string
	itemDir         string
	itemMeta        library.ItemMetadata
	cancel          context.CancelFunc
	doneCh          chan struct{}
	startedAt       time.Time
	reportAt        time.Time
	reportBytes     int64
	bytesTotal      int64
	segments        []string
	stopReason      string
	lastErr         string
	nextPart        int
	lastSequence    int64
	hasSequence     bool
	segmentDuration float64
	wroteMedia      bool
	quality         int
	qualityName     string
	codec           string
}

type cachedStatusEntry struct {
	status    bilibili.LiveUserStatus
	fetchedAt time.Time
}

// Manager coordinates live status polling, transition notifications, and stream recording.
type Manager struct {
	mu           sync.Mutex
	startMu      sync.Mutex
	lib          *library.Store
	subs         *subscription.Manager
	client       *bilibili.Client
	liveSettings settings.LiveSettings
	onNotify     func(Notification)
	onState      func(RoomState)
	nowFunc      func() time.Time

	states            map[int64]RoomState         // keyed by RoomID (or negative UID if RoomID unknown)
	prevLive          map[int64]int               // UID -> previous LiveStatus
	statusCache       map[int64]cachedStatusEntry // UID -> cached LiveUserStatus (60s TTL)
	sessions          map[int64]*recordSession    // RoomID -> active recording session
	stoppedBroadcasts map[int64]int64             // RoomID -> manually stopped live_time
	stopping          bool
	finalizeTimeout   time.Duration

	pollCancel context.CancelFunc
	pollWake   chan struct{}
	wg         sync.WaitGroup

	// Configurable intervals and thresholds for deterministic testing
	hlsPollInterval  time.Duration
	reconnectWait    time.Duration
	noSegmentTimeout time.Duration
	maxStreamLossDur time.Duration
	minFreeBytes     uint64
	freeBytesFunc    func(dir string) (uint64, error)
}

// NewManager creates a live status and recording Manager.
func NewManager(
	lib *library.Store,
	subs *subscription.Manager,
	client *bilibili.Client,
	liveSettings settings.LiveSettings,
	onNotify func(Notification),
	onState func(RoomState),
) *Manager {
	if liveSettings.MaxConcurrentRecordings <= 0 {
		liveSettings.MaxConcurrentRecordings = 2
	}
	return &Manager{
		lib:               lib,
		subs:              subs,
		client:            client,
		liveSettings:      liveSettings,
		onNotify:          onNotify,
		onState:           onState,
		nowFunc:           time.Now,
		states:            make(map[int64]RoomState),
		prevLive:          make(map[int64]int),
		statusCache:       make(map[int64]cachedStatusEntry),
		sessions:          make(map[int64]*recordSession),
		stoppedBroadcasts: make(map[int64]int64),
		finalizeTimeout:   30 * time.Second,
		pollWake:          make(chan struct{}, 1),
		hlsPollInterval:   0, // 0 uses #EXT-X-TARGETDURATION clamped to [1s, 5s]
		reconnectWait:     6 * time.Second,
		noSegmentTimeout:  10 * time.Second,
		maxStreamLossDur:  5 * time.Minute,
		minFreeBytes:      defaultMinFreeBytes,
		freeBytesFunc:     getFreeDiskBytes,
	}
}

// Start recovers registered recording parts and starts background status polling.
func (m *Manager) Start(parentCtx context.Context) {
	m.RecoverOrphanParts()

	m.mu.Lock()
	if m.pollCancel != nil {
		m.mu.Unlock()
		return
	}
	m.stopping = false
	ctx, cancel := context.WithCancel(parentCtx)
	m.pollCancel = cancel
	m.mu.Unlock()

	m.wg.Add(1)
	go m.pollLoop(ctx)
}

// Stop cancels polling and recordings, allowing a bounded finalization window.
func (m *Manager) Stop() {
	m.startMu.Lock()
	m.mu.Lock()
	m.stopping = true
	cancel := m.pollCancel
	m.pollCancel = nil
	m.mu.Unlock()
	m.startMu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.CancelRecordings("app_closed")
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(m.finalizeTimeout):
	}
}

// UpdateSettings updates live polling and recording settings.
func (m *Manager) UpdateSettings(s settings.LiveSettings) {
	if s.MaxConcurrentRecordings <= 0 {
		s.MaxConcurrentRecordings = 2
	}
	m.mu.Lock()
	m.liveSettings = s
	m.mu.Unlock()
	select {
	case m.pollWake <- struct{}{}:
	default:
	}
}

func (m *Manager) pollLoop(ctx context.Context) {
	defer m.wg.Done()
	for {
		m.mu.Lock()
		cfg := m.liveSettings
		m.mu.Unlock()

		hasAuto := false
		if m.subs != nil {
			if subs, err := m.subs.List(); err == nil {
				for _, sub := range subs {
					if sub.Rules.AutoRecord {
						hasAuto = true
					}
				}
			}
		}
		if !cfg.PollEnabled && !hasAuto {
			select {
			case <-ctx.Done():
				return
			case <-m.pollWake:
				continue
			case <-time.After(time.Minute):
				continue
			}
		}

		if m.client.HasAuth() && m.subs != nil {
			backoff, _ := m.subs.IsInBackoff()
			if !backoff {
				var uids []int64
				if list, err := m.subs.List(); err == nil {
					for _, sub := range list {
						if cfg.PollEnabled || sub.Rules.AutoRecord {
							for _, account := range sub.Accounts {
								uid, _ := strconv.ParseInt(account.UID, 10, 64)
								if uid > 0 {
									uids = append(uids, uid)
								}
							}
						}
					}
				}
				_, _ = m.refreshAccounts(bilibili.WithAutomaticRequests(ctx, "live"), uids, true, true)
			}
		}

		sec := cfg.PollIntervalSeconds
		if sec < 60 {
			sec = 60
		}
		timer := time.NewTimer(time.Duration(sec) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-m.pollWake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// Status queries live status for the given UIDs using a 60-second in-memory cache and <=50 UID batching (live.md §1).
func (m *Manager) Status(ctx context.Context, uids []int64) (map[int64]bilibili.LiveUserStatus, error) {
	if err := m.client.RequireAuth("live:status"); err != nil {
		return nil, err
	}
	now := m.nowFunc()
	result := make(map[int64]bilibili.LiveUserStatus, len(uids))
	var uncached []int64
	seen := make(map[int64]bool, len(uids))

	m.mu.Lock()
	for _, uid := range uids {
		if uid <= 0 || seen[uid] {
			continue
		}
		seen[uid] = true
		if entry, ok := m.statusCache[uid]; ok && now.Sub(entry.fetchedAt) < statusCacheTTL {
			result[uid] = entry.status
		} else {
			uncached = append(uncached, uid)
		}
	}
	m.mu.Unlock()

	if len(uncached) > 0 {
		fetched, err := m.client.LiveStatusByUIDs(ctx, uncached)
		if err != nil {
			return nil, err
		}
		m.mu.Lock()
		for _, uid := range uncached {
			st := fetched[uid]
			if st.UID == 0 {
				st.UID = uid
			}
			m.statusCache[uid] = cachedStatusEntry{
				status:    st,
				fetchedAt: now,
			}
			result[uid] = st
		}
		m.mu.Unlock()
	}
	return result, nil
}

// PollNow queries live status for all subscribed creators (in <=50 UID batches),
// detects 0/2 -> 1 transitions (ignoring 2=carousel), emits notifications, and triggers auto-recording (C32).
func (m *Manager) PollNow(ctx context.Context) ([]RoomState, error) {
	return m.refreshAccounts(ctx, nil, true, false)
}

func (m *Manager) RefreshForAccounts(ctx context.Context, uids []int64) ([]RoomState, error) {
	return m.refreshAccounts(ctx, uids, false, false)
}

func (m *Manager) refreshAccounts(ctx context.Context, selected []int64, force, autoRecord bool) ([]RoomState, error) {
	if err := m.client.RequireAuth("live:poll"); err != nil {
		return nil, err
	}
	var subList []subscription.Subscription
	var err error
	if m.subs != nil {
		subList, err = m.subs.List()
		if err != nil {
			return nil, err
		}
	}

	subByUID := make(map[int64]subscription.Subscription, len(subList))
	allowed := make(map[int64]bool)
	for _, uid := range selected {
		allowed[uid] = true
	}
	var uids []int64
	for _, s := range subList {

		for _, acc := range s.Accounts {
			if acc.Site != "bilibili" {
				continue
			}
			uid, err := strconv.ParseInt(acc.UID, 10, 64)
			if err == nil && uid > 0 && (selected == nil || allowed[uid]) {
				if _, exists := subByUID[uid]; !exists {
					subByUID[uid] = s
					uids = append(uids, uid)
				}
			}
		}
		if s.Mid > 0 && (selected == nil || allowed[s.Mid]) {
			if _, exists := subByUID[s.Mid]; !exists {
				subByUID[s.Mid] = s
				uids = append(uids, s.Mid)
			}
		}
	}
	// A user page can show a creator before they are subscribed. Explicit UI
	// lookups may query these accounts, without enabling rules or notifications.
	for _, uid := range selected {
		if uid > 0 {
			if _, exists := subByUID[uid]; !exists {
				subByUID[uid] = subscription.Subscription{Mid: uid}
				uids = append(uids, uid)
			}
		}
	}
	if len(uids) == 0 {
		return m.ListRooms(), nil
	}

	var statusMap map[int64]bilibili.LiveUserStatus
	if force {
		statusMap, err = m.client.LiveStatusByUIDs(ctx, uids)
	} else {
		statusMap, err = m.Status(ctx, uids)
	}
	if err != nil {
		return nil, err
	}

	now := m.nowFunc().UTC()
	nowStr := now.Format(time.RFC3339)
	var toAutoRecord []int64
	var notifications []Notification

	m.mu.Lock()
	notifyGlobal := m.liveSettings.NotifyOnStart
	maxRec := m.liveSettings.MaxConcurrentRecordings
	if maxRec <= 0 {
		maxRec = 2
	}
	for _, uid := range uids {
		sub := subByUID[uid]
		st, ok := statusMap[uid]
		if !ok {
			st = bilibili.LiveUserStatus{
				UID:        uid,
				RoomID:     sub.RoomID,
				Uname:      sub.Uname,
				Face:       sub.Face,
				LiveStatus: 0,
			}
		}
		if force {
			m.statusCache[uid] = cachedStatusEntry{status: st, fetchedAt: now}
		}

		prev, hadPrev := m.prevLive[uid]
		m.prevLive[uid] = st.LiveStatus

		if st.LiveStatus != 1 && st.RoomID > 0 {
			delete(m.stoppedBroadcasts, st.RoomID)
		}

		// Transition 0/2 -> 1 triggers notification and auto-record (LiveStatus==2 carousel NEVER triggers, C32).
		if st.LiveStatus == 1 && (!hadPrev || prev != 1) {
			if notifyGlobal && sub.LiveNotify {
				notifications = append(notifications, Notification{
					UID:       uid,
					RoomID:    st.RoomID,
					Uname:     firstNonEmpty(st.Uname, sub.Uname),
					Title:     st.Title,
					StartedAt: nowStr,
				})
			}
		}

		autoRecEnabled := sub.LiveAutoRecord || sub.Rules.AutoRecord
		if st.LiveStatus == 1 && autoRecEnabled && st.RoomID > 0 {
			if stopped, ok := m.stoppedBroadcasts[st.RoomID]; ok && st.LiveTime > 0 && stopped != st.LiveTime {
				delete(m.stoppedBroadcasts, st.RoomID)
			}
			_, suppressed := m.stoppedBroadcasts[st.RoomID]
			if _, already := m.sessions[st.RoomID]; autoRecord && !m.stopping && !already && !suppressed {
				toAutoRecord = append(toAutoRecord, st.RoomID)
			}
		}

		key := st.RoomID
		if key <= 0 {
			key = -uid
		}
		existing := m.states[key]
		recSess := m.sessions[st.RoomID]
		roomState := RoomState{
			UID:         uid,
			RoomID:      st.RoomID,
			ShortID:     st.ShortID,
			Uname:       firstNonEmpty(st.Uname, sub.Uname),
			Face:        firstNonEmpty(st.Face, sub.Face),
			Title:       st.Title,
			Cover:       st.Cover,
			AreaName:    st.AreaName,
			LiveStatus:  st.LiveStatus,
			LiveSince:   st.LiveTime,
			StatusText:  LiveStatusText(st.LiveStatus),
			Quality:     existing.Quality,
			QualityName: existing.QualityName,
			Codec:       existing.Codec,
			Recording:   recSess != nil,
			StopReason:  existing.StopReason,
			LastError:   existing.LastError,
			UpdatedAt:   nowStr,
		}
		if recSess != nil {
			roomState.RecordSession = recSess.sessionID
			roomState.RecordBytes = recSess.bytesTotal
			roomState.RecordSegments = len(recSess.segments)
			roomState.RecordItemDir = recSess.itemDir
			roomState.Quality = recSess.quality
			roomState.QualityName = recSess.qualityName
			roomState.Codec = recSess.codec
		}
		m.states[key] = roomState
		for oldKey, state := range m.states {
			if oldKey != key && state.UID == uid {
				delete(m.states, oldKey)
			}
		}
	}
	m.mu.Unlock()

	for _, n := range notifications {
		if m.onNotify != nil {
			m.onNotify(n)
		}
	}
	for _, roomID := range toAutoRecord {
		if _, rErr := m.StartRecording(bilibili.WithAutomaticRequests(ctx, "live"), roomID); rErr != nil {
			m.mu.Lock()
			if st, ok := m.states[roomID]; ok {
				st.LastError = rErr.Error()
				m.states[roomID] = st
			}
			m.mu.Unlock()
		}
	}

	return m.ListRooms(), nil
}

// OpenRoom resolves and inspects a single live room by roomID or shortID.
func (m *Manager) OpenRoom(ctx context.Context, roomOrShortID int64) (RoomState, error) {
	info, err := m.client.RoomInit(ctx, roomOrShortID)
	if err != nil {
		return RoomState{}, err
	}

	var userSt bilibili.LiveUserStatus
	if info.UID > 0 {
		stMap, sErr := m.Status(ctx, []int64{info.UID})
		if sErr != nil {
			return RoomState{}, sErr
		}
		userSt = stMap[info.UID]
	}

	now := m.nowFunc().UTC()
	nowStr := now.Format(time.RFC3339)
	m.mu.Lock()
	existing := m.states[info.RoomID]
	recSess := m.sessions[info.RoomID]
	st := RoomState{
		UID:         info.UID,
		RoomID:      info.RoomID,
		ShortID:     info.ShortID,
		Uname:       firstNonEmpty(userSt.Uname, fmt.Sprintf("UID %d", info.UID)),
		Face:        userSt.Face,
		Title:       firstNonEmpty(userSt.Title, fmt.Sprintf("直播间 %d", info.RoomID)),
		Cover:       userSt.Cover,
		AreaName:    userSt.AreaName,
		LiveStatus:  info.LiveStatus,
		LiveSince:   max(info.LiveTime, userSt.LiveTime),
		StatusText:  LiveStatusText(info.LiveStatus),
		Quality:     existing.Quality,
		QualityName: existing.QualityName,
		Codec:       existing.Codec,
		Recording:   recSess != nil,
		StopReason:  existing.StopReason,
		LastError:   existing.LastError,
		UpdatedAt:   nowStr,
	}
	if recSess != nil {
		st.RecordSession = recSess.sessionID
		st.RecordBytes = recSess.bytesTotal
		st.RecordSegments = len(recSess.segments)
		st.RecordItemDir = recSess.itemDir
		st.Quality = recSess.quality
		st.QualityName = recSess.qualityName
		st.Codec = recSess.codec
	}
	m.states[info.RoomID] = st
	m.mu.Unlock()
	return st, nil
}

// ListRooms returns all tracked live rooms ordered by LiveStatus (live first) and RoomID.
func (m *Manager) ListRooms() []RoomState {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]RoomState, 0, len(m.states))
	for _, st := range m.states {
		if sess := m.sessions[st.RoomID]; sess != nil {
			st.Recording = true
			st.RecordSession = sess.sessionID
			st.RecordDurationSec = m.nowFunc().Sub(sess.startedAt).Seconds()
			st.RecordBytes = sess.bytesTotal
			st.RecordSegments = len(sess.segments)
			st.RecordItemDir = sess.itemDir
			if sess.quality > 0 {
				st.Quality = sess.quality
			}
			if sess.qualityName != "" {
				st.QualityName = sess.qualityName
			}
			if sess.codec != "" {
				st.Codec = sess.codec
			}
			if sess.stopReason != "" {
				st.StopReason = sess.stopReason
			}
			if sess.lastErr != "" {
				st.LastError = sess.lastErr
			}
		} else {
			st.Recording = false
		}
		if st.QualityName == "" && st.Quality > 0 {
			st.QualityName = bilibili.LiveQualityFallbackName(st.Quality)
		}
		out = append(out, st)
	}

	sort.SliceStable(out, func(i, j int) bool {
		pi := statusPriority(out[i].LiveStatus)
		pj := statusPriority(out[j].LiveStatus)
		if pi != pj {
			return pi > pj
		}
		return out[i].RoomID < out[j].RoomID
	})
	return out
}

// StartRecording begins recording a live room into <cache_dir>/bilibili/<uid>_<uname>/live_<room_id>_<timestamp>/ (C32).
func (m *Manager) StartRecording(ctx context.Context, roomOrShortID int64) (RoomState, error) {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	m.mu.Lock()
	stopping := m.stopping
	m.mu.Unlock()
	if stopping {
		return RoomState{}, &bilibili.SiteError{Kind: bilibili.ErrKindUnavailable, Message: "应用正在退出，不能开始新的录制"}
	}
	st, err := m.OpenRoom(ctx, roomOrShortID)
	if err != nil {
		return RoomState{}, err
	}
	if st.LiveStatus != 1 {
		return st, &bilibili.SiteError{
			Kind:    bilibili.ErrKindUnavailable,
			Message: fmt.Sprintf("直播间当前状态为「%s」，仅在直播中时支持录制", st.StatusText),
		}
	}
	if st.LiveSince <= 0 {
		return st, &bilibili.SiteError{Kind: bilibili.ErrKindSchemaChanged, Message: "站点未返回本场直播的开播时间，无法建立录像条目"}
	}

	m.mu.Lock()
	if _, already := m.sessions[st.RoomID]; already {
		m.mu.Unlock()
		return m.OpenRoom(ctx, st.RoomID)
	}
	maxRec := m.liveSettings.MaxConcurrentRecordings
	if maxRec <= 0 {
		maxRec = 2
	}
	if len(m.sessions) >= maxRec {
		m.mu.Unlock()
		return st, fmt.Errorf("已达到同时录制上限（%d 路），未开始录制", maxRec)
	}
	codecOrder := append([]string(nil), m.liveSettings.CodecOrder...)
	m.mu.Unlock()

	cacheRoot := m.lib.CacheDir()
	if m.freeBytesFunc != nil && m.minFreeBytes > 0 {
		freeBytes, fErr := m.freeBytesFunc(cacheRoot)
		if fErr != nil {
			return st, fErr
		}
		if freeBytes < m.minFreeBytes {
			return st, &bilibili.SiteError{Kind: "storage", Message: "磁盘可用空间不足 1 GiB，无法开始录制"}
		}
	}

	// Verify play info is accessible and not encrypted (203) before creating session.
	playSel, err := m.client.LiveRoomPlayInfo(ctx, st.RoomID, codecOrder)
	if err != nil {
		return st, err
	}
	if playSel.QualityName == "" && playSel.Quality > 0 {
		playSel.QualityName = bilibili.LiveQualityFallbackName(playSel.Quality)
	}

	now := m.nowFunc().UTC()
	sessionID := fmt.Sprintf("%d:%d", st.RoomID, st.LiveSince)
	itemDir, err := library.EnsureWithinRoot(cacheRoot, filepath.Join(cacheRoot, "bilibili", strconv.FormatInt(st.UID, 10), "lives", fmt.Sprintf("%d-%d", st.RoomID, st.LiveSince)))
	if err != nil {
		return st, err
	}
	if err := os.MkdirAll(itemDir, 0o700); err != nil {
		return st, err
	}

	meta := library.ItemMetadata{
		Version:  1,
		ID:       "live:" + sessionID,
		Site:     "bilibili",
		Kind:     "live",
		Status:   "recording",
		Title:    st.Title,
		URL:      fmt.Sprintf("https://live.bilibili.com/%d", st.RoomID),
		PubTs:    st.LiveSince,
		CachedAt: now.Format(time.RFC3339),
		Author: library.ItemAuthor{
			Mid:  st.UID,
			Name: st.Uname,
			Face: st.Face,
		},
	}
	existing, exists, err := m.lib.GetItem("live:" + sessionID)
	if err != nil {
		return st, err
	}
	if exists {
		meta = existing
		itemDir = existing.ItemDir
		meta.Status = "recording"
	}
	if meta.Live == nil {
		meta.Live = &library.LiveRecordingInfo{RoomID: st.RoomID, LiveSince: st.LiveSince, StartedAt: now.Format(time.RFC3339)}
	}
	meta.Live.EndedAt, meta.Live.StopReason = "", ""
	meta.Live.Quality = playSel.Quality
	meta.Live.QualityName = playSel.QualityName
	meta.Live.Codec = playSel.Codec
	meta.Live.Source = "manual"
	if bilibili.AutomaticRequestSource(ctx) != "" {
		meta.Live.Source = "auto"
		st.Source = "auto"
	}
	savedMeta, err := m.lib.SaveItem(itemDir, meta)
	if err != nil {
		return st, err
	}

	recParent := context.Background()
	if meta.Live.Source == "auto" {
		recParent = bilibili.WithAutomaticRequests(recParent, "live")
	}
	recCtx, recCancel := context.WithCancel(recParent)
	sess := &recordSession{
		sessionID: sessionID,
		startedAt: now, reportAt: now,
		roomID:   st.RoomID,
		uid:      st.UID,
		uname:    st.Uname,
		face:     st.Face,
		title:    st.Title,
		itemDir:  itemDir,
		itemMeta: savedMeta,
		cancel:   recCancel,
		doneCh:   make(chan struct{}),
		nextPart: nextPartNumber(meta), lastSequence: meta.Live.LastSequence, hasSequence: meta.Live.HasSequence,
		quality:     playSel.Quality,
		qualityName: playSel.QualityName,
		codec:       playSel.Codec,
	}
	for _, media := range meta.Media {
		if media.Role == "live_segment" {
			sess.segments = append(sess.segments, media.Path)
			sess.bytesTotal += media.Bytes
		}
	}

	m.mu.Lock()
	m.sessions[st.RoomID] = sess
	st.Recording = true
	st.RecordSession = sessionID
	st.RecordItemDir = itemDir
	st.Quality = playSel.Quality
	st.QualityName = playSel.QualityName
	st.Codec = playSel.Codec
	st.StopReason = ""
	st.LastError = ""
	m.states[st.RoomID] = st
	m.mu.Unlock()

	m.wg.Add(1)
	go m.runRecordingSession(recCtx, sess, playSel, codecOrder)

	if m.onState != nil {
		m.onState(st)
	}
	return st, nil
}

// StopRecording stops an active recording for roomID and finalizes item.json,
// suppressing auto-restart for the remainder of the current broadcast (live.md §3).
func (m *Manager) StopRecording(roomID int64) (RoomState, error) {
	return m.stopRecordingInternal(roomID, "user_stop", true)
}

func (m *Manager) stopRecordingInternal(roomID int64, reason string, suppressAutoRestart bool) (RoomState, error) {
	m.mu.Lock()
	if suppressAutoRestart {
		m.stoppedBroadcasts[roomID] = m.states[roomID].LiveSince
	}
	sess, ok := m.sessions[roomID]
	if ok && sess != nil && sess.stopReason == "" {
		sess.stopReason = reason
	}
	m.mu.Unlock()
	if !ok || sess == nil {
		m.mu.Lock()
		st := m.states[roomID]
		st.Recording = false
		m.mu.Unlock()
		return st, nil
	}

	sess.cancel()
	select {
	case <-sess.doneCh:
	case <-time.After(m.finalizeTimeout):
		m.mu.Lock()
		state := m.states[roomID]
		m.mu.Unlock()
		return state, &bilibili.SiteError{Kind: "storage", Message: "录制收尾已到时间上限，临时片段会在下次启动时恢复"}
	}

	m.mu.Lock()
	st := m.states[roomID]
	st.Recording = false
	st.RecordDurationSec = m.nowFunc().Sub(sess.startedAt).Seconds()
	st.RecordBytes = sess.bytesTotal
	st.RecordSegments = len(sess.segments)
	st.RecordItemDir = sess.itemDir
	st.StopReason = sess.stopReason
	if sess.lastErr != "" {
		st.LastError = sess.lastErr
	}
	m.states[roomID] = st
	m.mu.Unlock()

	if m.onState != nil {
		m.onState(st)
	}
	return st, nil
}

func (m *Manager) fetchLiveBytes(ctx context.Context, targetURL string) ([]byte, error) {
	return m.fetchLiveLimitedBytes(ctx, targetURL, 32*1024*1024)
}

func (m *Manager) fetchLiveLimitedBytes(ctx context.Context, targetURL string, maxBytes int64) ([]byte, error) {
	if err := m.client.ValidateMediaURL(targetURL); err != nil {
		return nil, err
	}
	mediaClient, _ := m.client.MediaHTTPClient()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", bilibili.DefaultUserAgent)
	req.Header.Set("Referer", "https://live.bilibili.com/")
	req.Header.Set("Origin", "https://live.bilibili.com")

	resp, err := mediaClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusPreconditionFailed {
		return nil, &bilibili.SiteError{
			Kind:       bilibili.ErrKindRiskControl,
			HTTPStatus: resp.StatusCode,
			Message:    "直播请求触发风控，已停止录制",
		}
	}
	if resp.StatusCode != http.StatusOK {
		kind := bilibili.ErrKindNetwork
		if resp.StatusCode == 429 {
			kind = bilibili.ErrKindRateLimited
		}
		if resp.StatusCode == 401 {
			kind = bilibili.ErrKindAuthRequired
		}
		return nil, &bilibili.SiteError{Kind: kind, HTTPStatus: resp.StatusCode, Message: "直播节点请求失败"}
	}
	reader := m.client.WrapMediaReader(resp.Body)
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, &bilibili.SiteError{Kind: bilibili.ErrKindSchemaChanged, Message: "直播响应超过大小上限"}
	}
	return data, nil
}

func (m *Manager) addSessionBytes(sess *recordSession, delta int64) {
	m.mu.Lock()
	sess.bytesTotal += delta
	elapsed := m.nowFunc().Sub(sess.reportAt)
	if elapsed < time.Second {
		m.mu.Unlock()
		return
	}
	st := m.states[sess.roomID]
	st.RecordBytes = sess.bytesTotal
	st.RecordDurationSec = m.nowFunc().Sub(sess.startedAt).Seconds()
	st.RecordSpeedBps = int64(float64(sess.bytesTotal-sess.reportBytes) / elapsed.Seconds())
	st.RecordSegments = len(sess.segments)
	st.Recording = true
	m.states[sess.roomID] = st
	sess.reportAt = m.nowFunc()
	sess.reportBytes = sess.bytesTotal
	m.mu.Unlock()
	if m.onState != nil {
		m.onState(st)
	}
}

type m3u8Segment struct {
	uri           string
	seq           int64
	discontinuity bool
	mapURI        string
	duration      float64
}

type m3u8Playlist struct {
	mapURI           string
	targetDuration   float64
	mediaSequence    int64
	hasDiscontinuity bool
	segments         []m3u8Segment
	endList          bool
	valid            bool
	invalidReason    string
	encrypted        bool
	// variants is set for a multivariant (master) playlist instead of segments.
	variants []m3u8Variant
}

type m3u8Variant struct {
	uri       string
	bandwidth int64
}

// bestVariant picks the highest-bandwidth variant; ties keep playlist order.
func (p m3u8Playlist) bestVariant() (string, bool) {
	best := -1
	for i, v := range p.variants {
		if v.uri != "" && (best < 0 || v.bandwidth > p.variants[best].bandwidth) {
			best = i
		}
	}
	if best < 0 {
		return "", false
	}
	return p.variants[best].uri, true
}

func parseM3U8PlaylistDetails(content string, baseURL string) m3u8Playlist {
	baseParsed, _ := url.Parse(baseURL)
	resolveRef := func(ref string) string {
		ref = strings.Trim(strings.TrimSpace(ref), `"'`)
		if ref == "" {
			return ""
		}
		if baseParsed != nil {
			if u, err := baseParsed.Parse(ref); err == nil {
				return u.String()
			}
		}
		return ref
	}

	out := m3u8Playlist{
		targetDuration: 1.0,
		mediaSequence:  0,
		valid:          strings.HasPrefix(strings.TrimSpace(content), "#EXTM3U"),
	}
	if !out.valid {
		out.invalidReason = "缺少 #EXTM3U 头"
	}
	invalidate := func(reason string) {
		if out.valid {
			out.valid, out.invalidReason = false, reason
		}
	}
	var curSeq int64
	var duration float64
	nextDisc := false
	pendingVariant := int64(-1)

	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-TARGETDURATION:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:"))
			if td, err := strconv.ParseFloat(val, 64); err == nil && td > 0 && !math.IsNaN(td) && !math.IsInf(td, 0) {
				out.targetDuration = td
			}
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"))
			if seq, err := strconv.ParseInt(val, 10, 64); err == nil && seq >= 0 {
				out.mediaSequence = seq
				curSeq = seq
			}
			continue
		}
		if line == "#EXT-X-DISCONTINUITY" {
			out.hasDiscontinuity = true
			nextDisc = true
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-KEY:") {
			out.encrypted = !strings.Contains(strings.ToUpper(line), "METHOD=NONE")
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-BYTERANGE:") {
			invalidate("不支持按字节范围切分的分片")
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			pendingVariant = 0
			for _, attr := range strings.Split(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:"), ",") {
				if k, v, ok := strings.Cut(strings.TrimSpace(attr), "="); ok && strings.EqualFold(k, "BANDWIDTH") {
					if bw, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && bw > 0 {
						pendingVariant = bw
					}
				}
			}
			continue
		}
		if strings.HasPrefix(line, "#EXTINF:") {
			value := strings.SplitN(strings.TrimPrefix(line, "#EXTINF:"), ",", 2)[0]
			duration, _ = strconv.ParseFloat(value, 64)
			if duration <= 0 || math.IsNaN(duration) || math.IsInf(duration, 0) {
				invalidate("分片时长无效")
			}
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-MAP:") {
			attrs := strings.TrimPrefix(line, "#EXT-X-MAP:")
			for _, part := range strings.Split(attrs, ",") {
				kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
				if len(kv) == 2 && strings.EqualFold(kv[0], "URI") {
					out.mapURI = resolveRef(kv[1])
				}
			}
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-ENDLIST") {
			out.endList = true
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		if pendingVariant >= 0 {
			out.variants = append(out.variants, m3u8Variant{uri: resolveRef(line), bandwidth: pendingVariant})
			pendingVariant = -1
			continue
		}
		out.segments = append(out.segments, m3u8Segment{
			uri:           resolveRef(line),
			seq:           curSeq,
			discontinuity: nextDisc,
			mapURI:        out.mapURI,
			duration:      duration,
		})
		if out.mapURI == "" {
			invalidate("分片缺少 fMP4 初始化分片（EXT-X-MAP）")
		}
		curSeq++
		nextDisc = false
		duration = 0
	}
	return out
}

// finalizeFMP4Part defragments a `.mp4.part` file into `finalPath` via a temporary file,
// verifies it with `download.InspectMP4File`, and removes `partPath` ONLY if conversion succeeds.
// If conversion fails, `partPath` is preserved on disk (live.md §2.6, R-14).
// LiveStatusText returns the Chinese label for a Bilibili live_status code.
func LiveStatusText(status int) string {
	switch status {
	case 1:
		return "直播中"
	case 2:
		return "轮播中"
	default:
		return "未开播"
	}
}

func statusPriority(status int) int {
	switch status {
	case 1:
		return 3
	case 2:
		return 2
	default:
		return 1
	}
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// CancelRecordings only signals sessions, so it is safe from inside a site
// request made by a recording goroutine. Normal session cleanup still runs.
func (m *Manager) CancelRecordings(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, session := range m.sessions {
		session.stopReason = reason
		session.cancel()
	}
}
