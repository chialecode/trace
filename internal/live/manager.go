package live

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site"
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
	Site              string                 `json:"site,omitempty"`
	AccountUID        string                 `json:"accountUid,omitempty"`
	UniqueID          string                 `json:"uniqueId,omitempty"`
	UID               int64                  `json:"uid"`
	RoomID            int64                  `json:"roomId"`
	RoomIDStr         string                 `json:"roomIdStr,omitempty"`
	WebRID            string                 `json:"webRid,omitempty"`
	RoomKey           string                 `json:"roomKey,omitempty"`
	LiveState         string                 `json:"liveState,omitempty"`
	ShortID           int64                  `json:"shortId,omitempty"`
	Uname             string                 `json:"uname"`
	Face              string                 `json:"face"`
	Title             string                 `json:"title"`
	Cover             string                 `json:"cover,omitempty"`
	AreaName          string                 `json:"areaName,omitempty"`
	LiveStatus        int                    `json:"liveStatus"` // 0=offline, 1=live, 2=carousel
	LiveSince         int64                  `json:"liveSince,omitempty"`
	Source            string                 `json:"source,omitempty"`
	StatusText        string                 `json:"statusText"` // "未开播", "直播中", "轮播中"
	Quality           int                    `json:"quality,omitempty"`
	QualityName       string                 `json:"qualityName,omitempty"`
	Codec             string                 `json:"codec,omitempty"`
	Transport         string                 `json:"transport,omitempty"`
	AudioOnly         bool                   `json:"audioOnly,omitempty"`
	Recording         bool                   `json:"recording"`
	RecordSession     string                 `json:"recordSession,omitempty"`
	RecordDurationSec float64                `json:"recordDurationSec,omitempty"`
	RecordSpeedBps    int64                  `json:"recordSpeedBps,omitempty"`
	RecordBytes       int64                  `json:"recordBytes,omitempty"`
	RecordSegments    int                    `json:"recordSegments,omitempty"`
	RecordItemDir     string                 `json:"recordItemDir,omitempty"`
	RecordPartPath    string                 `json:"recordPartPath,omitempty"`
	RecordPartIndex   int                    `json:"recordPartIndex,omitempty"`
	RecordPartBytes   int64                  `json:"recordPartBytes,omitempty"`
	RecordPartReady   bool                   `json:"recordPartReady,omitempty"`
	PreviewSupported  bool                   `json:"previewSupported,omitempty"`
	PreviewReason     string                 `json:"previewReason,omitempty"`
	Incidents         []library.LiveIncident `json:"incidents,omitempty"`
	StopReason        string                 `json:"stopReason,omitempty"`
	LastError         string                 `json:"lastError,omitempty"`
	UpdatedAt         string                 `json:"updatedAt"`
}

// Notification represents a transition from offline/carousel (0/2) to live (1).
type Notification struct {
	Site       string `json:"site,omitempty"`
	AccountUID string `json:"accountUid,omitempty"`
	UID        int64  `json:"uid"`
	RoomID     int64  `json:"roomId"`
	RoomIDStr  string `json:"roomIdStr,omitempty"`
	WebRID     string `json:"webRid,omitempty"`
	Uname      string `json:"uname"`
	Title      string `json:"title"`
	StartedAt  string `json:"startedAt"`
}

type recordSession struct {
	siteID                   string
	accountUID               string
	roomIDStr                string
	webRID                   string
	stateKey                 string
	coverURL                 string
	transport                site.MediaTransportSpec
	sessionID                string
	roomID                   int64
	uid                      int64
	uname                    string
	face                     string
	title                    string
	itemDir                  string
	itemMeta                 library.ItemMetadata
	cancel                   context.CancelFunc
	doneCh                   chan struct{}
	startedAt                time.Time
	reportAt                 time.Time
	reportBytes              int64
	bytesTotal               int64
	segments                 []string
	stopReason               string
	lastErr                  string
	nextPart                 int
	lastSequence             int64
	hasSequence              bool
	lastSequenceAt           time.Time // wall-clock anchor for HLS lastSequence (seeded on resume; cleared on transport switch)
	segmentDuration          float64
	wroteMedia               bool
	quality                  int
	qualityName              string
	codec                    string
	streamTransport          string
	audioOnly                bool
	activePartPath           string
	activePartIndex          int
	activePartBytes          int64
	activePartDur            float64
	activePartReady          bool
	lastSpeedBps             int64
	previewSupport           bool
	previewReason            string
	incidents                []library.LiveIncident
	slow                     slowDetector
	nominalByteRate          float64
	pendingReconnectAt       time.Time
	pendingReconnectHadMedia bool
	// inExternal tracks active external site-client / callback invocations on this
	// session so re-entrant StopForSite never deadlocks waiting on doneCh.
	inExternal atomic.Int32
	// saveMu serializes writes of the manifest (item.json); finishDone
	// closes when the parts converting in the background are done.
	saveMu     sync.Mutex
	finishDone chan struct{}
}

func (sess *recordSession) enterExternal() func() {
	if sess == nil {
		return func() {}
	}
	sess.inExternal.Add(1)
	return func() {
		sess.inExternal.Add(-1)
	}
}

type cachedStatusEntry struct {
	status    bilibili.LiveUserStatus
	fetchedAt time.Time
}

// Manager coordinates live status polling, transition notifications, and stream recording.
type Manager struct {
	mu           sync.Mutex
	starting     map[string]chan struct{}
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

	siteProviders         map[site.ID]site.LiveProvider
	webRIDSyncFn          WebRIDSyncFunc
	siteBackoffFn         SiteBackoffChecker
	douyinPollMinInterval time.Duration
	// douyinLiveGap is the least time between two Douyin live checks.
	douyinLiveGap time.Duration
	// douyinGapWait replaces the wait between live checks in tests.
	douyinGapWait      func(ctx context.Context, d time.Duration) bool
	flvIdleTimeout     time.Duration
	douyinStates       map[string]RoomState
	douyinStatusCache  map[string]douyinCachedStatusEntry
	douyinRawCovers    map[string]string
	douyinLastAutoPoll map[string]time.Time
	douyinWebRIDHints  map[string]string
	douyinRoomHints    map[string]string
	douyinPrevLive     map[string]string
	douyinSessions     map[string]*recordSession
	douyinStoppedRooms map[string]bool

	stopping        bool
	stopDeadline    time.Time
	finalizeTimeout time.Duration

	baseCtx    context.Context
	baseCancel context.CancelFunc
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
	baseCtx, baseCancel := context.WithCancel(context.Background())
	return &Manager{
		starting:              make(map[string]chan struct{}),
		lib:                   lib,
		subs:                  subs,
		client:                client,
		liveSettings:          liveSettings,
		onNotify:              onNotify,
		onState:               onState,
		nowFunc:               time.Now,
		states:                make(map[int64]RoomState),
		prevLive:              make(map[int64]int),
		statusCache:           make(map[int64]cachedStatusEntry),
		sessions:              make(map[int64]*recordSession),
		stoppedBroadcasts:     make(map[int64]int64),
		siteProviders:         make(map[site.ID]site.LiveProvider),
		douyinPollMinInterval: defaultDouyinAutoPollInterval,
		douyinLiveGap:         DefaultDouyinLiveGap,
		flvIdleTimeout:        defaultFLVIdleTimeout,
		douyinStates:          make(map[string]RoomState),
		douyinStatusCache:     make(map[string]douyinCachedStatusEntry),
		douyinRawCovers:       make(map[string]string),
		douyinLastAutoPoll:    make(map[string]time.Time),
		douyinWebRIDHints:     make(map[string]string),
		douyinRoomHints:       make(map[string]string),
		douyinPrevLive:        make(map[string]string),
		douyinSessions:        make(map[string]*recordSession),
		douyinStoppedRooms:    make(map[string]bool),
		finalizeTimeout:       30 * time.Second,
		baseCtx:               baseCtx,
		baseCancel:            baseCancel,
		pollWake:              make(chan struct{}, 1),
		hlsPollInterval:       0, // 0 uses #EXT-X-TARGETDURATION clamped to [1s, 5s]
		reconnectWait:         6 * time.Second,
		noSegmentTimeout:      10 * time.Second,
		maxStreamLossDur:      5 * time.Minute,
		minFreeBytes:          defaultMinFreeBytes,
		freeBytesFunc:         getFreeDiskBytes,
	}
}

func (m *Manager) baseCtxLocked() context.Context {
	if m.baseCtx == nil {
		m.baseCtx, m.baseCancel = context.WithCancel(context.Background())
	}
	return m.baseCtx
}

func (m *Manager) withManagerContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	base := m.baseCtxLocked()
	m.mu.Unlock()
	opCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(base, cancel)
	return opCtx, func() {
		stop()
		cancel()
	}
}

// acquireStartSlot serializes concurrent start attempts for the same key without
// holding a global mutex across blocking network or storage I/O.
func (m *Manager) acquireStartSlot(ctx context.Context, siteID site.ID, key string) (func(), error) {
	for {
		m.mu.Lock()
		if m.stopping {
			m.mu.Unlock()
			return nil, &site.Error{Site: siteID, Kind: site.ErrKindUnavailable, Message: "应用正在退出，不能开始新的录制"}
		}
		if m.starting == nil {
			m.starting = make(map[string]chan struct{})
		}
		waitCh, busy := m.starting[key]
		if !busy {
			ch := make(chan struct{})
			m.starting[key] = ch
			m.mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					m.mu.Lock()
					if m.starting[key] == ch {
						delete(m.starting, key)
					}
					close(ch)
					m.mu.Unlock()
				})
			}, nil
		}
		baseDone := m.baseCtxLocked().Done()
		m.mu.Unlock()

		select {
		case <-waitCh:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-baseDone:
			return nil, &site.Error{Site: siteID, Kind: site.ErrKindUnavailable, Message: "应用正在退出，不能开始新的录制"}
		}
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
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	m.stopping = false
	m.stopDeadline = time.Time{}
	if m.baseCancel != nil {
		m.baseCancel()
	}
	m.baseCtx, m.baseCancel = context.WithCancel(parentCtx)
	ctx, cancel := context.WithCancel(m.baseCtx)
	m.pollCancel = cancel
	m.wg.Add(1)
	m.mu.Unlock()

	go m.pollLoop(ctx)
}

// Stop cancels polling and recordings, allowing a bounded finalization window.
func (m *Manager) Stop() {
	m.mu.Lock()
	m.stopping = true
	timeout := m.finalizeTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	m.stopDeadline = time.Now().Add(timeout)
	pollCancel := m.pollCancel
	m.pollCancel = nil
	baseCancel := m.baseCancel
	m.mu.Unlock()

	if pollCancel != nil {
		pollCancel()
	}
	if baseCancel != nil {
		baseCancel()
	}
	m.CancelRecordings("app_closed")

	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}

// UpdateSettings updates live polling and recording settings.
func (m *Manager) UpdateSettings(s settings.LiveSettings) {
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

		var subs []subscription.Subscription
		hasBiliAuto := false
		hasDouyinAuto := false
		var biliUIDs []int64
		seenBili := make(map[int64]bool)
		if m.subs != nil {
			if list, err := m.subs.List(); err == nil {
				subs = list
				for _, sub := range subs {
					auto := sub.Rules.AutoRecord || sub.LiveAutoRecord
					includeBili := cfg.PollEnabled || auto
					for _, acc := range sub.Accounts {
						switch site.ID(strings.ToLower(strings.TrimSpace(acc.Site))) {
						case site.Douyin:
							if auto {
								hasDouyinAuto = true
							}
						case site.Weibo, site.X:
							// Weibo and X live polling & recording are unsupported; never issue requests.
							continue
						case "", site.Bilibili:
							if auto {
								hasBiliAuto = true
							}
							if includeBili {
								if uid, err := strconv.ParseInt(acc.UID, 10, 64); err == nil && uid > 0 && !seenBili[uid] {
									seenBili[uid] = true
									biliUIDs = append(biliUIDs, uid)
								}
							}
						default:
							continue
						}
					}
					if sub.Mid > 0 && (sub.Site == "" || strings.EqualFold(strings.TrimSpace(sub.Site), string(site.Bilibili))) {
						if auto {
							hasBiliAuto = true
						}
						if includeBili && !seenBili[sub.Mid] {
							seenBili[sub.Mid] = true
							biliUIDs = append(biliUIDs, sub.Mid)
						}
					}
				}
			}
		}
		if !cfg.PollEnabled && !hasBiliAuto && !hasDouyinAuto {
			timer := time.NewTimer(time.Minute)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-m.pollWake:
				timer.Stop()
				continue
			case <-timer.C:
				continue
			}
		}

		// Rounds start an interval apart: the Douyin checks spaced through
		// the round do not push the next one back.
		roundStart := time.Now()
		if (cfg.PollEnabled || hasBiliAuto) && m.client != nil && m.client.HasAuth() && len(biliUIDs) > 0 {
			if !m.isSiteInBackoff(site.Bilibili) {
				_, _ = m.refreshAccounts(bilibili.WithAutomaticRequests(ctx, "live"), biliUIDs, true, true, true)
			}
		}

		if hasDouyinAuto && m.subs != nil && m.hasSiteAuth(site.Douyin) {
			if !m.isSiteInBackoff(site.Douyin) {
				_, _ = m.pollDouyinAutoRecord(bilibili.WithAutomaticRequests(ctx, "live"))
			}
		}

		sec := cfg.PollIntervalSeconds
		if sec < 60 {
			sec = 60
		}
		timer := time.NewTimer(max(time.Duration(sec)*time.Second-time.Since(roundStart), time.Second))
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

// ListRooms returns all tracked live rooms ordered by LiveStatus (live first) and RoomID.
func (m *Manager) ListRooms() []RoomState {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]RoomState, 0, len(m.states)+len(m.douyinStates))
	for _, st := range m.states {
		m.normalizeBilibiliRoomStateLocked(&st)
		if sess := m.sessions[st.RoomID]; sess != nil {
			m.hydrateBilibiliStateFromSessionLocked(&st, sess)
		} else {
			st.Recording = false
			hydrateSessionPreviewFieldsLocked(&st, nil)
		}
		if st.QualityName == "" && st.Quality > 0 {
			st.QualityName = bilibili.LiveQualityFallbackName(st.Quality)
		}
		out = append(out, st)
	}
	for _, st := range m.douyinStates {
		out = append(out, m.hydrateDouyinStateLocked(st))
	}

	sort.SliceStable(out, func(i, j int) bool {
		pi := statusPriority(out[i].LiveStatus)
		pj := statusPriority(out[j].LiveStatus)
		if pi != pj {
			return pi > pj
		}
		if out[i].Site != out[j].Site {
			return out[i].Site < out[j].Site
		}
		if out[i].RoomID != out[j].RoomID {
			return out[i].RoomID < out[j].RoomID
		}
		return out[i].RoomIDStr < out[j].RoomIDStr
	})
	return out
}

func (m *Manager) checkStartDiskSpace(cacheRoot string, siteID site.ID) error {
	m.mu.Lock()
	freeFn := m.freeBytesFunc
	minFree := m.minFreeBytes
	m.mu.Unlock()
	if freeFn == nil || minFree == 0 {
		return nil
	}
	freeBytes, fErr := freeFn(cacheRoot)
	if fErr != nil {
		return fErr
	}
	if freeBytes < minFree {
		return &site.Error{Site: siteID, Kind: site.ErrKindStorage, Message: "磁盘可用空间不足 1 GiB，无法开始录制"}
	}
	return nil
}

func (m *Manager) addSessionBytes(sess *recordSession, delta int64) {
	m.mu.Lock()
	sess.bytesTotal += delta
	now := m.nowLocked()
	elapsed := now.Sub(sess.reportAt)
	if elapsed < time.Second {
		m.mu.Unlock()
		return
	}
	speedBps := int64(float64(sess.bytesTotal-sess.reportBytes) / elapsed.Seconds())
	sess.lastSpeedBps = speedBps
	var st RoomState
	var updated bool
	switch sess.siteID {
	case string(site.Douyin):
		st = m.douyinStates[sess.stateKey]
		st.RecordBytes = sess.bytesTotal
		st.RecordDurationSec = now.Sub(sess.startedAt).Seconds()
		st.RecordSpeedBps = speedBps
		st.RecordSegments = len(sess.segments)
		st.Recording = true
		hydrateSessionPreviewFieldsLocked(&st, sess)
		m.douyinStates[sess.stateKey] = st
		updated = true
	case "", string(site.Bilibili):
		st = m.states[sess.roomID]
		m.normalizeBilibiliRoomStateLocked(&st)
		st.RecordBytes = sess.bytesTotal
		st.RecordDurationSec = now.Sub(sess.startedAt).Seconds()
		st.RecordSpeedBps = speedBps
		st.RecordSegments = len(sess.segments)
		st.Recording = true
		hydrateSessionPreviewFieldsLocked(&st, sess)
		m.states[sess.roomID] = st
		updated = true
	}
	sess.reportAt = now
	sess.reportBytes = sess.bytesTotal
	m.mu.Unlock()
	if updated && m.onState != nil {
		leave := sess.enterExternal()
		m.onState(st)
		leave()
	}
}

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

func isAutoStartBatchStopError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	switch liveStopReason(err) {
	case site.ErrKindAuthRequired, site.ErrKindRiskControl, site.ErrKindRateLimited,
		site.ErrKindQuota, site.ErrKindBackoff, site.ErrKindStorage:
		return true
	}
	return false
}

// CancelRecordings only signals sessions, so it is safe from inside a site
// request made by a recording goroutine. Existing stop reasons are preserved.
func (m *Manager) CancelRecordings(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, session := range m.sessions {
		if session.stopReason == "" {
			session.stopReason = reason
		}
		session.cancel()
	}
	for _, session := range m.douyinSessions {
		if session.stopReason == "" {
			session.stopReason = reason
		}
		session.cancel()
	}
}

// CancelRecordingsForSite signals active recordings belonging to siteID without waiting.
func (m *Manager) CancelRecordingsForSite(siteID site.ID, reason string) {
	if reason == "" {
		reason = "auth_required"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	switch siteID {
	case site.Bilibili:
		for _, session := range m.sessions {
			if session.stopReason == "" {
				session.stopReason = reason
			}
			session.cancel()
		}
	case site.Douyin:
		for _, session := range m.douyinSessions {
			if session.stopReason == "" {
				session.stopReason = reason
			}
			session.cancel()
		}
	case site.Weibo, site.X:
		// Weibo and X live recording are unsupported; nothing to cancel.
	}
}

// StopForSite cancels active recordings belonging to siteID without affecting other sites,
// waiting for sessions that are not currently inside an external site-client/callback
// call so re-entrant invocations from recording goroutines never self-deadlock.
func (m *Manager) StopForSite(siteID site.ID, reason string) {
	if reason == "" {
		reason = "auth_required"
	}
	var waitChs []chan struct{}
	m.mu.Lock()
	switch siteID {
	case site.Bilibili:
		for _, session := range m.sessions {
			if session.stopReason == "" {
				session.stopReason = reason
			}
			session.cancel()
			if session.inExternal.Load() == 0 {
				waitChs = append(waitChs, session.doneCh)
			}
		}
	case site.Douyin:
		for _, session := range m.douyinSessions {
			if session.stopReason == "" {
				session.stopReason = reason
			}
			session.cancel()
			if session.inExternal.Load() == 0 {
				waitChs = append(waitChs, session.doneCh)
			}
		}
	case site.Weibo, site.X:
		// Weibo and X live recording are unsupported; nothing to stop.
	}
	timeout := m.finalizeTimeout
	m.mu.Unlock()

	if len(waitChs) == 0 {
		return
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for _, ch := range waitChs {
		select {
		case <-ch:
		case <-deadline.C:
			return
		}
	}
}
