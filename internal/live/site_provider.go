package live

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/site"
)

const (
	defaultDouyinAutoPollInterval = 90 * time.Second
	// DefaultDouyinLiveGap is the least time between two Douyin live checks.
	// Douyin has no batch status request, so each account costs a request;
	// a round checks every account it can at this pace (a 3-minute round:
	// 30 accounts), the longest unchecked first, and an account's interval
	// grows past the round only by this gap per further account. The checks
	// do not take the site's hourly quota, which stays for update checks,
	// automatic caching and starting recordings (DEC-060 ⑤).
	DefaultDouyinLiveGap = 6 * time.Second

	// WeiboLiveUnsupportedMessage is the user-facing notice when Weibo live
	// status or recording is requested.
	WeiboLiveUnsupportedMessage = "微博直播暂不支持录制"
)

func errWeiboLiveUnsupported() error {
	return &site.Error{
		Site:        site.Weibo,
		Kind:        site.ErrKindUnavailable,
		Unsupported: true,
		Message:     WeiboLiveUnsupportedMessage,
	}
}

// LiveRoomAliveChecker is an optional extension on site.LiveProvider for batch room alive checks (e.g. Douyin check_alive).
type LiveRoomAliveChecker interface {
	CheckRoomsAlive(ctx context.Context, roomIDs []string) (map[string]bool, error)
}

// LiveAuthChecker is an optional extension on site.LiveProvider to report whether site credentials are active.
type LiveAuthChecker interface {
	HasAuth() bool
}

// LiveErrorReporter is an optional extension on site.LiveProvider to report automatic background errors.
type LiveErrorReporter interface {
	ReportAutomaticError(ctx context.Context, err error)
}

// WebRIDSyncFunc persists a backfilled or invalidated Douyin web_rid back to subscription storage.
type WebRIDSyncFunc func(siteID site.ID, uid, webRID string, invalidated bool)

// SiteBackoffChecker reports whether background polling for a given site is currently in backoff.
type SiteBackoffChecker func(siteID site.ID) (bool, time.Time)

// RegisterSiteProvider registers a cross-site LiveProvider (such as Douyin or Bilibili).
// Weibo live recording/polling is unsupported, so any Weibo provider is ignored.
func (m *Manager) RegisterSiteProvider(provider site.LiveProvider) {
	if provider == nil || provider.SiteID() == site.Weibo {
		return
	}
	m.mu.Lock()
	m.siteProviders[provider.SiteID()] = provider
	m.mu.Unlock()
}

// SetWebRIDSyncFunc sets a callback invoked whenever Douyin live status backfills or invalidates an account's web_rid.
func (m *Manager) SetWebRIDSyncFunc(fn WebRIDSyncFunc) {
	m.mu.Lock()
	m.webRIDSyncFn = fn
	m.mu.Unlock()
}

// SetSiteBackoffChecker sets a per-site backoff predicate used by background live polling.
func (m *Manager) SetSiteBackoffChecker(fn SiteBackoffChecker) {
	m.mu.Lock()
	m.siteBackoffFn = fn
	m.mu.Unlock()
}

// SetDouyinPollMinInterval overrides the 90s minimum per-account auto-record poll interval (used by unit tests).
func (m *Manager) SetDouyinPollMinInterval(d time.Duration) {
	m.mu.Lock()
	if d >= 0 {
		m.douyinPollMinInterval = d
	}
	m.mu.Unlock()
}

// SetDouyinLiveGap overrides the least time between two Douyin live checks
// (used by unit tests; 0 sends them back to back).
func (m *Manager) SetDouyinLiveGap(d time.Duration) {
	m.mu.Lock()
	m.douyinLiveGap = max(d, 0)
	m.mu.Unlock()
}

// DouyinLiveGap is the least time between two Douyin live checks.
func (m *Manager) DouyinLiveGap() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.douyinLiveGap
}

// DouyinLiveCheckEvery estimates how often each of n auto-record Douyin
// accounts gets its live status checked.
func (m *Manager) DouyinLiveCheckEvery(n int) time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n <= 0 {
		return 0
	}
	sec := max(m.liveSettings.PollIntervalSeconds, 60)
	per := m.douyinLiveChecksPerRoundLocked()
	rounds := (n + per - 1) / per
	return max(m.douyinPollMinInterval, time.Duration(rounds*sec)*time.Second)
}

// SetFLVIdleTimeout overrides the 15s per-read idle timeout on HTTP-FLV streams (used by unit tests).
func (m *Manager) SetFLVIdleTimeout(d time.Duration) {
	m.mu.Lock()
	if d > 0 {
		m.flvIdleTimeout = d
	}
	m.mu.Unlock()
}

func (m *Manager) getSiteProvider(siteID site.ID) (site.LiveProvider, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.siteProviders[siteID]
	return p, ok
}

func (m *Manager) hasSiteAuth(siteID site.ID) bool {
	switch siteID {
	case site.Bilibili:
		return m.client != nil && m.client.HasAuth()
	case site.Douyin:
		p, ok := m.getSiteProvider(site.Douyin)
		if !ok {
			return false
		}
		if checker, ok := p.(LiveAuthChecker); ok {
			return checker.HasAuth()
		}
		return true
	case site.Weibo:
		return false
	default:
		return false
	}
}

func (m *Manager) isSiteInBackoff(siteID site.ID) bool {
	m.mu.Lock()
	fn := m.siteBackoffFn
	m.mu.Unlock()
	if fn != nil {
		inBackoff, _ := fn(siteID)
		return inBackoff
	}
	if m.subs != nil {
		if perSite, ok := any(m.subs).(interface {
			IsSiteInBackoff(site string) (bool, time.Time)
		}); ok {
			inBackoff, _ := perSite.IsSiteInBackoff(string(siteID))
			return inBackoff
		}
		if siteID == site.Bilibili {
			inBackoff, _ := m.subs.IsInBackoff()
			return inBackoff
		}
	}
	return false
}

func (m *Manager) recordSiteAutoStop(siteID site.ID, reason string) {
	if m.subs == nil {
		return
	}
	if perSite, ok := any(m.subs).(interface {
		RecordSiteAutomaticStop(site, reason string)
	}); ok {
		perSite.RecordSiteAutomaticStop(string(siteID), reason)
		return
	}
	if siteID == site.Bilibili {
		m.subs.RecordAutomaticStop(reason)
	}
}

func (m *Manager) syncWebRID(siteID site.ID, uid, webRID string, invalidated bool) {
	m.mu.Lock()
	fn := m.webRIDSyncFn
	m.mu.Unlock()
	if fn != nil {
		fn(siteID, uid, webRID, invalidated)
	}
	if m.subs != nil {
		if updater, ok := any(m.subs).(interface {
			UpdateAccountWebRID(site, uid, webRID string) error
		}); ok {
			_ = updater.UpdateAccountWebRID(string(siteID), uid, webRID)
		}
	}
}

// StartRecordingByKey dispatches live recording start by canonical room/account key or numeric Bilibili room ID.
// Any Weibo key ("weibo:*") is rejected explicitly without issuing any network request.
func (m *Manager) StartRecordingByKey(ctx context.Context, key string) (RoomState, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return RoomState{}, &site.Error{Kind: site.ErrKindBadRequest, Message: "无效的直播间标识"}
	}
	lowerKey := strings.ToLower(key)
	switch {
	case strings.HasPrefix(lowerKey, "weibo:"):
		return RoomState{Site: string(site.Weibo)}, errWeiboLiveUnsupported()
	case strings.HasPrefix(key, "douyin:live:"):
		rid := strings.TrimSpace(strings.TrimPrefix(key, "douyin:live:"))
		if rid == "" {
			return RoomState{}, &site.Error{Site: site.Douyin, Kind: site.ErrKindBadRequest, Message: "无效的直播间标识"}
		}
		return m.StartDouyinRecording(ctx, "", rid)
	case strings.HasPrefix(key, "douyin:user:"):
		uid := strings.TrimSpace(strings.TrimPrefix(key, "douyin:user:"))
		if uid == "" {
			return RoomState{}, &site.Error{Site: site.Douyin, Kind: site.ErrKindBadRequest, Message: "无效的直播间标识"}
		}
		return m.StartDouyinRecording(ctx, uid, "")
	case strings.HasPrefix(key, "douyin:web:"):
		webRID := strings.TrimSpace(strings.TrimPrefix(key, "douyin:web:"))
		if webRID == "" {
			return RoomState{}, &site.Error{Site: site.Douyin, Kind: site.ErrKindBadRequest, Message: "无效的直播间标识"}
		}
		return m.StartDouyinRecording(ctx, "", webRID)
	case strings.HasPrefix(key, "douyin:"):
		uid := strings.TrimSpace(strings.TrimPrefix(key, "douyin:"))
		if uid == "" {
			return RoomState{}, &site.Error{Site: site.Douyin, Kind: site.ErrKindBadRequest, Message: "无效的直播间标识"}
		}
		return m.StartDouyinRecording(ctx, uid, "")
	case strings.HasPrefix(key, "bilibili:live:"):
		rid, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(key, "bilibili:live:")), 10, 64)
		if err != nil || rid <= 0 {
			return RoomState{}, &site.Error{Site: site.Bilibili, Kind: site.ErrKindBadRequest, Message: "无效的直播间房间号"}
		}
		return m.StartRecording(ctx, rid)
	case strings.HasPrefix(key, "bilibili:user:"), strings.HasPrefix(key, "bilibili:"):
		rest := strings.TrimPrefix(key, "bilibili:user:")
		byUIDOnly := strings.HasPrefix(key, "bilibili:user:")
		if !byUIDOnly {
			rest = strings.TrimPrefix(key, "bilibili:")
		}
		num, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
		if err != nil || num <= 0 {
			return RoomState{}, &site.Error{Site: site.Bilibili, Kind: site.ErrKindBadRequest, Message: "无效的直播间标识"}
		}
		if !byUIDOnly {
			m.mu.Lock()
			_, hasRoom := m.states[num]
			m.mu.Unlock()
			if hasRoom {
				return m.StartRecording(ctx, num)
			}
		}
		rooms, err := m.RefreshForAccounts(ctx, []int64{num})
		if err != nil {
			return RoomState{}, err
		}
		if len(rooms) == 0 || rooms[0].RoomID <= 0 {
			return RoomState{}, &site.Error{Site: site.Bilibili, Kind: site.ErrKindUnavailable, Message: "该账号未开通直播间"}
		}
		return m.StartRecording(ctx, rooms[0].RoomID)
	default:
		if rid, err := strconv.ParseInt(key, 10, 64); err == nil && rid > 0 {
			m.mu.Lock()
			_, isDouyinRoom := m.douyinStates["douyin:live:"+key]
			m.mu.Unlock()
			if isDouyinRoom {
				return m.StartDouyinRecording(ctx, "", key)
			}
			return m.StartRecording(ctx, rid)
		}
		return RoomState{}, &site.Error{Kind: site.ErrKindBadRequest, Message: "无效的直播间标识"}
	}
}

// StopRecordingByKey stops an active Bilibili or Douyin live recording by room key, account key, or room ID.
// Any Weibo key ("weibo:*") is rejected explicitly without falling through to other sites.
func (m *Manager) StopRecordingByKey(key string) (RoomState, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return RoomState{}, &site.Error{Kind: site.ErrKindBadRequest, Message: "无效的直播间标识"}
	}
	lowerKey := strings.ToLower(key)
	switch {
	case strings.HasPrefix(lowerKey, "weibo:"):
		return RoomState{Site: string(site.Weibo)}, errWeiboLiveUnsupported()
	case strings.HasPrefix(key, "douyin:live:"):
		rest := strings.TrimSpace(strings.TrimPrefix(key, "douyin:live:"))
		if rest == "" {
			return RoomState{Site: string(site.Douyin)}, &site.Error{Site: site.Douyin, Kind: site.ErrKindBadRequest, Message: "缺少抖音直播间号或主播标识"}
		}
		return m.StopDouyinRecording(rest)
	case strings.HasPrefix(key, "douyin:user:"):
		rest := strings.TrimSpace(strings.TrimPrefix(key, "douyin:user:"))
		if rest == "" {
			return RoomState{Site: string(site.Douyin)}, &site.Error{Site: site.Douyin, Kind: site.ErrKindBadRequest, Message: "缺少抖音直播间号或主播标识"}
		}
		return m.StopDouyinRecording(rest)
	case strings.HasPrefix(key, "douyin:web:"):
		rest := strings.TrimSpace(strings.TrimPrefix(key, "douyin:web:"))
		if rest == "" {
			return RoomState{Site: string(site.Douyin)}, &site.Error{Site: site.Douyin, Kind: site.ErrKindBadRequest, Message: "缺少抖音直播间号或主播标识"}
		}
		return m.StopDouyinRecording(rest)
	case strings.HasPrefix(key, "douyin:"):
		rest := strings.TrimSpace(strings.TrimPrefix(key, "douyin:"))
		if rest == "" {
			return RoomState{Site: string(site.Douyin)}, &site.Error{Site: site.Douyin, Kind: site.ErrKindBadRequest, Message: "缺少抖音直播间号或主播标识"}
		}
		return m.StopDouyinRecording(rest)
	case strings.HasPrefix(key, "bilibili:live:"):
		rid, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(key, "bilibili:live:")), 10, 64)
		if err != nil || rid <= 0 {
			return RoomState{}, &site.Error{Site: site.Bilibili, Kind: site.ErrKindBadRequest, Message: "无效的直播间房间号"}
		}
		return m.StopRecording(rid)
	case strings.HasPrefix(key, "bilibili:user:"), strings.HasPrefix(key, "bilibili:"):
		byUIDOnly := strings.HasPrefix(key, "bilibili:user:")
		rest := strings.TrimPrefix(key, "bilibili:user:")
		if !byUIDOnly {
			rest = strings.TrimPrefix(key, "bilibili:")
		}
		num, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
		if err != nil || num <= 0 {
			return RoomState{}, &site.Error{Site: site.Bilibili, Kind: site.ErrKindBadRequest, Message: "无效的直播间标识"}
		}
		m.mu.Lock()
		var matchedRoom int64
		if !byUIDOnly {
			if _, ok := m.sessions[num]; ok {
				matchedRoom = num
			}
		}
		if matchedRoom == 0 {
			for rid, s := range m.sessions {
				if s != nil && s.uid == num {
					matchedRoom = rid
					break
				}
			}
		}
		if matchedRoom == 0 {
			if !byUIDOnly {
				if st, ok := m.states[num]; ok && st.RoomID > 0 {
					matchedRoom = st.RoomID
				}
			}
			if matchedRoom == 0 {
				for _, st := range m.states {
					if st.UID == num && st.RoomID > 0 {
						matchedRoom = st.RoomID
						break
					}
				}
			}
		}
		m.mu.Unlock()
		if matchedRoom > 0 {
			return m.StopRecording(matchedRoom)
		}
		return RoomState{Site: string(site.Bilibili)}, nil
	default:
		if strings.Contains(key, ":") {
			return RoomState{}, &site.Error{Kind: site.ErrKindBadRequest, Message: "无效的直播间标识"}
		}
		m.mu.Lock()
		_, hasDouyin := m.douyinSessions[key]
		m.mu.Unlock()
		if hasDouyin {
			return m.StopDouyinRecording(key)
		}
		if rid, err := strconv.ParseInt(key, 10, 64); err == nil && rid > 0 {
			return m.StopRecording(rid)
		}
		return m.StopDouyinRecording(key)
	}
}

// LiveCheckTimes returns when each account's live status was last fetched
// by a check, keyed "<site>:<uid>".
func (m *Manager) LiveCheckTimes() map[string]time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]time.Time, len(m.statusCache)+len(m.douyinLastAutoPoll))
	for uid, e := range m.statusCache {
		if !e.fetchedAt.IsZero() {
			out[string(site.Bilibili)+":"+strconv.FormatInt(uid, 10)] = e.fetchedAt
		}
	}
	for uid, at := range m.douyinLastAutoPoll {
		out[string(site.Douyin)+":"+uid] = at
	}
	return out
}

// LiveCheckEvery estimates how often an auto-record account of siteID has
// its live status checked, n being that site's auto-record accounts:
// Bilibili checks them all each round, Douyin within its budget; Weibo and
// unknown sites return 0 (never fall back to Bilibili).
func (m *Manager) LiveCheckEvery(siteID site.ID, n int) time.Duration {
	switch siteID {
	case site.Bilibili:
		m.mu.Lock()
		defer m.mu.Unlock()
		return time.Duration(max(m.liveSettings.PollIntervalSeconds, 60)) * time.Second
	case site.Douyin:
		return m.DouyinLiveCheckEvery(n)
	case site.Weibo:
		return 0
	default:
		return 0
	}
}
