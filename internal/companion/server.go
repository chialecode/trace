package companion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/companionapi"
	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/chialecode/trace/internal/uistate"
)

type ctxKeyVirtualIface struct{}

// withVirtualInterface tags an HTTP request context when accepted on a virtual/tunnel listener.
func withVirtualInterface(ctx context.Context, virtual bool) context.Context {
	return context.WithValue(ctx, ctxKeyVirtualIface{}, virtual)
}

func requestArrivedOnVirtual(r *http.Request) bool {
	v, _ := r.Context().Value(ctxKeyVirtualIface{}).(bool)
	return v
}

// Server serves the companion HTTPS JSON & WebSocket API on top of service.BackendAPI.
type Server struct {
	backend     service.BackendAPI
	store       *Store
	pairing     *pairingManager
	hub         *eventHub
	thumbs      *thumbCache
	limiter     *failureLimiter
	channel     string
	nodeName    string
	version     string
	startedAt   time.Time
	now         func() time.Time
	addrsFunc   func() []string
	diskFunc    func(string) (uint64, uint64, error)
	logf        func(format string, args ...any)
	onFeedSeen  func()
	pollMaxWait time.Duration
}

// ServerOptions configures a companion HTTP API server.
type ServerOptions struct {
	Backend     service.BackendAPI
	Store       *Store
	Pairing     *pairingManager
	Hub         *eventHub
	Thumbs      *thumbCache
	Channel     string
	NodeName    string
	Version     string
	StartedAt   time.Time
	Now         func() time.Time
	AddrsFunc   func() []string
	DiskFunc    func(string) (uint64, uint64, error)
	Logf        func(format string, args ...any)
	OnFeedSeen  func()
	PollMaxWait time.Duration
}

// NewServer constructs the companion HTTP handler.
func NewServer(opt ServerOptions) *Server {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.StartedAt.IsZero() {
		opt.StartedAt = opt.Now()
	}
	if opt.DiskFunc == nil {
		opt.DiskFunc = diskSpace
	}
	if opt.Logf == nil {
		opt.Logf = func(string, ...any) {}
	}
	if opt.Version == "" {
		opt.Version = appdirs.BuildInfo().Version
	}
	if opt.Channel == "" {
		if ch, ok := companionapi.NormalizeChannel(appdirs.BuildInfo().Channel); ok {
			opt.Channel = ch
		} else {
			opt.Channel = companionapi.ChannelDev
		}
	}
	if opt.PollMaxWait <= 0 {
		opt.PollMaxWait = companionapi.PairPollMaxWait
	}
	if opt.Pairing == nil {
		opt.Pairing = newPairingManager(opt.Now, nil)
	}
	if opt.Hub == nil {
		opt.Hub = newEventHub(opt.Now, nil)
	}
	return &Server{
		backend:     opt.Backend,
		store:       opt.Store,
		pairing:     opt.Pairing,
		hub:         opt.Hub,
		thumbs:      opt.Thumbs,
		limiter:     newFailureLimiter(),
		channel:     opt.Channel,
		nodeName:    opt.NodeName,
		version:     opt.Version,
		startedAt:   opt.StartedAt,
		now:         opt.Now,
		addrsFunc:   opt.AddrsFunc,
		diskFunc:    opt.DiskFunc,
		logf:        opt.Logf,
		onFeedSeen:  opt.OnFeedSeen,
		pollMaxWait: opt.PollMaxWait,
	}
}

func (s *Server) currentAddrs() []string {
	if s.addrsFunc != nil {
		return s.addrsFunc()
	}
	return nil
}

// ServeHTTP enforces source filtering, rate limiting, protocol version check,
// bearer authentication, request body bounds, and dispatches all /api/v1/ routes.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	w.Header().Set(companionapi.HeaderProtocol, companionapi.ProtocolVersion)
	w.Header().Set(companionapi.HeaderVersion, s.version)
	w.Header().Set(companionapi.HeaderChannel, s.channel)
	w.Header().Set("Cache-Control", "no-store")

	// 1. Source IP filtering MUST run before auth and before reading the request body (design §5.1).
	remoteIP, ok := ParseRemoteAddr(r.RemoteAddr)
	if !ok || !IsAllowedSource(remoteIP, requestArrivedOnVirtual(r)) {
		s.logf("companion rejected source: addr=%s", r.RemoteAddr)
		writeError(w, http.StatusForbidden, companionapi.CodeForbiddenSource, "电脑只接受同一局域网或 VPN 内的连接", nil)
		return
	}
	ipStr := remoteIP.String()

	// 2. Per-IP failure rate limiter (5 failures in 1m -> 10m block with 429).
	if blocked, retryAfter := s.limiter.IsBlocked(ipStr, now); blocked {
		writeError(w, http.StatusTooManyRequests, companionapi.CodeRateLimited, "尝试次数过多，请 10 分钟后再试", func(b *companionapi.ErrorBody) {
			b.Error.RetryAfterSecs = retryAfter
		})
		return
	}

	if !strings.HasPrefix(r.URL.Path, companionapi.APIPrefix) {
		s.limiter.RecordFailure(ipStr, now)
		writeError(w, http.StatusNotFound, companionapi.CodeNotFound, "没有这个接口", nil)
		return
	}

	// 3. Protocol version check (design §5.1, NFR-MOB-VER-001).
	if clientProto := strings.TrimSpace(r.Header.Get(companionapi.HeaderProtocol)); clientProto != companionapi.ProtocolVersion {
		clientVer := strings.TrimSpace(r.Header.Get(companionapi.HeaderVersion))
		clientCh := strings.TrimSpace(r.Header.Get(companionapi.HeaderChannel))
		writeError(w, http.StatusConflict, companionapi.CodeProtocolMismatch, "手机端与电脑端的协议版本不兼容", func(b *companionapi.ErrorBody) {
			b.Error.ServerVersion = s.version
			b.Error.ServerProtocol = companionapi.ProtocolVersion
			b.Error.ServerChannel = s.channel
			b.Error.ClientVersion = clientVer
			b.Error.ClientProtocol = clientProto
			b.Error.ClientChannel = clientCh
		})
		return
	}

	rel := strings.TrimPrefix(r.URL.Path, companionapi.APIPrefix)

	// 4. Pairing endpoints (unauthenticated by default; POST /api/v1/pair also accepts
	// an existing device Bearer token for design §4.7 QR re-scan address refresh).
	if rel == "pair" && r.Method == http.MethodPost {
		s.handlePairStart(w, r, ipStr, now)
		return
	}
	if strings.HasPrefix(rel, "pair/") && r.Method == http.MethodGet {
		pairID := strings.TrimPrefix(rel, "pair/")
		s.handlePairPoll(w, r, pairID)
		return
	}

	// 5. Authenticate Bearer device token in constant time (design §5.1).
	dev, authed := s.authenticateRequest(r)
	if !authed {
		s.limiter.RecordFailure(ipStr, now)
		s.logf("companion auth failed: ip=%s path=%s", ipStr, r.URL.Path)
		writeError(w, http.StatusUnauthorized, companionapi.CodeUnauthorized, "这台电脑已移除本机或访问令牌无效", nil)
		return
	}
	s.store.TouchDevice(dev.ID, ipStr, now)

	// 6. WebSocket event stream (must hijack before reading body).
	if rel == "events" && r.Method == http.MethodGet {
		s.handleEventsWS(w, r, dev, ipStr)
		return
	}

	// 7. Media & Avatar endpoints (streaming/Range, no request body).
	if strings.HasPrefix(rel, "media/") && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		s.handleMedia(w, r, strings.TrimPrefix(rel, "media/"))
		return
	}
	if strings.HasPrefix(rel, "avatars/") && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		s.handleAvatar(w, r, strings.TrimPrefix(rel, "avatars/"))
		return
	}

	// 8. Bound request body to 64 KiB (design §5.1).
	var body []byte
	if r.Body != nil && r.Method != http.MethodGet && r.Method != http.MethodHead {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, companionapi.MaxBodyBytes))
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				writeError(w, http.StatusRequestEntityTooLarge, companionapi.CodeTooLarge, "请求内容超过 64 KiB 上限", nil)
				return
			}
			writeError(w, http.StatusBadRequest, companionapi.CodeBadRequest, "请求内容读取失败", nil)
			return
		}
		body = raw
	}

	s.routeAuthed(w, r, rel, body, dev, ipStr)
}

func (s *Server) authenticateRequest(r *http.Request) (DeviceRecord, bool) {
	if s.store == nil {
		return DeviceRecord{}, false
	}
	auth := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok {
		return DeviceRecord{}, false
	}
	return s.store.Authenticate(token)
}

func (s *Server) handlePairStart(w http.ResponseWriter, r *http.Request, ipStr string, now time.Time) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, companionapi.MaxBodyBytes))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, companionapi.CodeTooLarge, "请求内容超过 64 KiB 上限", nil)
			return
		}
		writeError(w, http.StatusBadRequest, companionapi.CodeBadRequest, "请求内容读取失败", nil)
		return
	}
	var req companionapi.PairRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeError(w, http.StatusBadRequest, companionapi.CodeBadRequest, "配对请求格式不正确", nil)
		return
	}

	// Channel match check (FR-MOB-004, design §4.3).
	reqCh, ok := companionapi.NormalizeChannel(req.Channel)
	if !ok || reqCh != s.channel {
		writeError(w, http.StatusConflict, companionapi.CodeChannelMismatch, "通道不一致：正式版只能配对正式版，开发版只能配对开发版", func(b *companionapi.ErrorBody) {
			b.Error.ServerChannel = s.channel
			b.Error.ClientChannel = req.Channel
		})
		return
	}
	req.Channel = reqCh

	// Design §4.7: already-paired device re-scanning a QR code for the same node & fp
	// refreshes its address list and consumes the one-time code without desktop confirmation.
	if dev, authed := s.authenticateRequest(r); authed {
		if err := s.pairing.ConsumeCode(req.Code); err != nil {
			s.limiter.RecordFailure(ipStr, now)
			writeError(w, http.StatusUnauthorized, companionapi.CodeInvalidCode, err.Error(), nil)
			return
		}
		s.store.TouchDevice(dev.ID, ipStr, now)
		s.logf("companion device refreshed addrs: device=%s ip=%s", dev.ID, ipStr)
		writeJSON(w, http.StatusOK, companionapi.PairStartResponse{
			Status:  companionapi.PairStatusUpdated,
			Node:    s.store.NodeID(),
			Name:    s.nodeName,
			Channel: s.channel,
			Addrs:   s.currentAddrs(),
		})
		return
	}

	// Device limit check (design §4.5).
	if s.store.Count() >= companionapi.MaxDevices {
		writeError(w, http.StatusConflict, companionapi.CodeDeviceLimit, "已配对手机数量已达上限（5 台），请先在电脑端移除一台", nil)
		return
	}

	// Validate pairSecret before consuming the one-time code.
	if len(strings.TrimSpace(req.PairSecret)) < 16 {
		writeError(w, http.StatusBadRequest, companionapi.CodeBadRequest, "配对请求缺少有效的临时密钥", nil)
		return
	}

	// Validate & consume one-time code (QR code or 8-char short code).
	if err := s.pairing.ConsumeCode(req.Code); err != nil {
		s.limiter.RecordFailure(ipStr, now)
		s.logf("companion pairing code rejected: ip=%s", ipStr)
		writeError(w, http.StatusUnauthorized, companionapi.CodeInvalidCode, err.Error(), nil)
		return
	}

	pending, err := s.pairing.CreatePending(req, ipStr)
	if err != nil {
		writeError(w, http.StatusInternalServerError, companionapi.CodeInternal, "无法建立配对请求", nil)
		return
	}
	s.logf("companion pairing pending: pair=%s device=%s ip=%s", pending.PairID, pending.DeviceName, ipStr)
	writeJSON(w, http.StatusOK, companionapi.PairStartResponse{
		PairID:    pending.PairID,
		Status:    companionapi.PairStatusPending,
		ExpiresAt: pending.ExpiresAt,
		Node:      s.store.NodeID(),
		Name:      s.nodeName,
		Channel:   s.channel,
		Addrs:     s.currentAddrs(),
	})
}

func (s *Server) handlePairPoll(w http.ResponseWriter, r *http.Request, pairID string) {
	secret := r.Header.Get(companionapi.HeaderPairSecret)
	wait := s.pollMaxWait
	if rawWait := strings.TrimSpace(r.URL.Query().Get("wait")); rawWait != "" {
		if sec, err := strconv.Atoi(rawWait); err == nil && sec >= 0 {
			d := time.Duration(sec) * time.Second
			if d < wait {
				wait = d
			}
		}
	}
	resp, found := s.pairing.Poll(r.Context(), pairID, secret, wait)
	if !found {
		// Design §4.4: mismatched pairSecret or unknown pairId always returns 404.
		writeError(w, http.StatusNotFound, companionapi.CodeNotFound, "配对请求不存在或已失效", nil)
		return
	}
	if resp.Status == companionapi.PairStatusApproved {
		resp.Node = s.store.NodeID()
		resp.Name = s.nodeName
		resp.Channel = s.channel
		resp.Addrs = s.currentAddrs()
		resp.ServerVersion = s.version
		resp.Protocol = companionapi.ProtocolVersion
		s.logf("companion pairing approved: device=%s", resp.DeviceID)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleEventsWS(w http.ResponseWriter, r *http.Request, dev DeviceRecord, ipStr string) {
	conn, err := upgradeWebSocket(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, companionapi.CodeBadRequest, "WebSocket 握手失败", nil)
		return
	}
	s.logf("companion ws connected: device=%s ip=%s", dev.ID, ipStr)
	cl := s.hub.Add(dev.ID, conn)
	<-cl.done
	s.logf("companion ws disconnected: device=%s ip=%s", dev.ID, ipStr)
}

func (s *Server) routeAuthed(w http.ResponseWriter, r *http.Request, rel string, body []byte, dev DeviceRecord, ipStr string) {
	switch {
	// ---- Read-only endpoints (design §5.2) ----
	case rel == "status" && r.Method == http.MethodGet:
		s.handleStatus(w)
	case rel == "feed" && r.Method == http.MethodGet:
		s.handleFeed(w, r)
	case rel == "feed/subscriptions" && r.Method == http.MethodGet:
		s.handleFeedSubscriptions(w, r)
	case rel == "feed/seen" && r.Method == http.MethodPost:
		s.handleFeedSeen(w, body)
	case rel == "feed/position" && r.Method == http.MethodGet:
		s.handleGetFeedPosition(w, dev)
	case rel == "feed/position" && r.Method == http.MethodPut:
		s.handlePutFeedPosition(w, body, dev)
	case rel == "library" && r.Method == http.MethodGet:
		s.handleLibraryList(w, r)
	case strings.HasPrefix(rel, "library/items/") && r.Method == http.MethodGet:
		key, err := url.PathUnescape(strings.TrimPrefix(rel, "library/items/"))
		if err != nil || strings.TrimSpace(key) == "" {
			writeError(w, http.StatusBadRequest, companionapi.CodeBadRequest, "条目键不正确", nil)
			return
		}
		s.handleLibraryItem(w, key)
	case rel == "subscriptions" && r.Method == http.MethodGet:
		s.handleSubscriptions(w)
	case rel == "subscriptions/accounts" && r.Method == http.MethodGet:
		s.handleSubscriptionsAccounts(w)
	case strings.HasPrefix(rel, "subscriptions/") && r.Method == http.MethodGet:
		id, err := url.PathUnescape(strings.TrimPrefix(rel, "subscriptions/"))
		if err != nil || strings.TrimSpace(id) == "" {
			writeError(w, http.StatusBadRequest, companionapi.CodeBadRequest, "订阅编号不正确", nil)
			return
		}
		s.handleSubscriptionDetail(w, id)
	case rel == "tasks/downloads" && r.Method == http.MethodGet:
		s.handleDownloadTasks(w)
	case rel == "tasks/downloads/clear-finished" && r.Method == http.MethodPost:
		s.handleClearFinishedDownloads(w)
	case strings.HasPrefix(rel, "tasks/downloads/") && r.Method == http.MethodGet:
		id, err := url.PathUnescape(strings.TrimPrefix(rel, "tasks/downloads/"))
		if err != nil || strings.TrimSpace(id) == "" {
			writeError(w, http.StatusBadRequest, companionapi.CodeBadRequest, "任务编号不正确", nil)
			return
		}
		s.handleDownloadTaskDetail(w, id)
	case strings.HasPrefix(rel, "tasks/downloads/") && r.Method == http.MethodPost:
		s.handleDownloadTaskAction(w, strings.TrimPrefix(rel, "tasks/downloads/"))
	case rel == "tasks/queues" && r.Method == http.MethodGet:
		s.handleCacheQueues(w)
	case strings.HasPrefix(rel, "tasks/queues/") && r.Method == http.MethodPost:
		s.handleCacheQueueAction(w, strings.TrimPrefix(rel, "tasks/queues/"))
	case rel == "live/recordings" && r.Method == http.MethodGet:
		s.handleLiveRecordings(w)
	case rel == "live/incidents" && r.Method == http.MethodGet:
		s.handleLiveIncidents(w)
	case rel == "live/start" && r.Method == http.MethodPost:
		s.handleLiveStart(w, r, body)
	case strings.HasPrefix(rel, "live/recordings/") && strings.HasSuffix(rel, "/stop") && r.Method == http.MethodPost:
		rawID := strings.TrimSuffix(strings.TrimPrefix(rel, "live/recordings/"), "/stop")
		id, err := url.PathUnescape(rawID)
		if err != nil || strings.TrimSpace(id) == "" {
			writeError(w, http.StatusBadRequest, companionapi.CodeBadRequest, "录制编号不正确", nil)
			return
		}
		s.handleLiveStop(w, id)
	case rel == "checks/plan" && r.Method == http.MethodGet:
		s.handleCheckPlan(w)
	case rel == "checks" && r.Method == http.MethodPost:
		s.handleChecksStart(w, body)
	case rel == "checks/account" && r.Method == http.MethodPost:
		s.handleCheckAccount(w, body)
	case rel == "checks/cancel" && r.Method == http.MethodPost:
		s.backend.CancelSubscriptionCheck()
		writeJSON(w, http.StatusOK, companionapi.ActionResponse{OK: true, Message: "已取消检查更新"})
	case rel == "conflicts" && r.Method == http.MethodGet:
		s.handleConflicts(w)
	case rel == "transfers" && r.Method == http.MethodGet:
		s.handleTransfers(w)
	case rel == "devices/self" && r.Method == http.MethodDelete:
		s.handleUnpairSelf(w, dev, ipStr)
	default:
		writeError(w, http.StatusNotFound, companionapi.CodeNotFound, "没有这个接口", nil)
	}
}

func (s *Server) handleStatus(w http.ResponseWriter) {
	now := s.now()
	statuses := s.backend.Statuses()
	sites := make([]companionapi.SiteAuthStatus, 0, len(site.All()))
	loggedIn := false
	for _, sid := range site.All() {
		st := statuses[string(sid)]
		state := st.State
		if state == "" {
			if st.LoggedIn {
				state = "logged_in"
			} else {
				state = "logged_out"
			}
		}
		if st.LoggedIn || state == "logged_in" {
			loggedIn = true
		}
		uid := st.UID
		if uid == "" && st.Mid > 0 {
			uid = strconv.FormatInt(st.Mid, 10)
		}
		sites = append(sites, companionapi.SiteAuthStatus{
			Site:        string(sid),
			DisplayName: sid.DisplayName(),
			State:       state,
			UID:         uid,
			Uname:       st.Uname,
		})
	}

	var disk companionapi.DiskSpace
	cacheDir := s.backend.GetSettings().CacheDir
	if cacheDir == "" {
		cacheDir = s.backend.Locations().DefaultCacheDir
	}
	if cacheDir != "" && s.diskFunc != nil {
		if free, total, err := s.diskFunc(cacheDir); err == nil {
			disk = companionapi.DiskSpace{FreeBytes: free, TotalBytes: total}
		}
	}

	var downloadBadge, failedDownloads, activeRecordings, pendingConflicts int
	if loggedIn {
		if tasks, err := s.backend.ListDownloadTasks(); err == nil {
			for _, t := range tasks {
				switch t.Status {
				case "queued", "running":
					downloadBadge++
				case "failed", "partial":
					failedDownloads++
				}
			}
		}
		for _, r := range s.backend.ListLiveAccounts() {
			if r.Recording {
				activeRecordings++
			}
		}
		pendingConflicts = s.backend.PendingCacheConflictCount()
	}

	uptime := int64(now.Sub(s.startedAt).Seconds())
	if uptime < 0 {
		uptime = 0
	}

	writeJSON(w, http.StatusOK, companionapi.StatusResponse{
		Node:             s.store.NodeID(),
		Name:             s.nodeName,
		Version:          s.version,
		Channel:          s.channel,
		Protocol:         companionapi.ProtocolVersion,
		StartedAt:        s.startedAt.UTC().Format(time.RFC3339),
		UptimeSec:        uptime,
		Now:              now.UTC().Format(time.RFC3339),
		LoggedIn:         loggedIn,
		Sites:            sites,
		Disk:             disk,
		DownloadBadge:    downloadBadge,
		ActiveRecordings: activeRecordings,
		PendingCount:     pendingConflicts + failedDownloads,
	})
}

func (s *Server) handleFeed(w http.ResponseWriter, r *http.Request) {
	if !s.requireAnyLogin(w) {
		return
	}
	q := r.URL.Query()
	limit := 30
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			limit = v
		}
	}
	if limit > 100 {
		limit = 100
	}
	dir := strings.TrimSpace(q.Get("direction"))
	if dir != "newer" {
		dir = "older"
	}
	req := library.FeedPageRequest{
		Direction:  dir,
		Order:      "newest",
		Limit:      limit,
		Site:       strings.TrimSpace(q.Get("site")),
		CachedOnly: true,
	}
	if cursorKey := strings.TrimSpace(q.Get("cursorKey")); cursorKey != "" {
		pubTs, _ := strconv.ParseInt(strings.TrimSpace(q.Get("cursorPubTs")), 10, 64)
		req.Cursor = &library.FeedCursor{PubTs: pubTs, Key: cursorKey}
	}

	subs, _ := s.backend.ListSubscriptions()
	subByAccount := make(map[string]subscription.Subscription)
	subByID := make(map[string]subscription.Subscription, len(subs))
	for _, sub := range subs {
		subByID[sub.ID] = sub
		for _, a := range sub.Accounts {
			subByAccount[strings.ToLower(a.Site)+":"+a.UID] = sub
		}
	}
	if subID := strings.TrimSpace(q.Get("subId")); subID != "" {
		sub, ok := subByID[subID]
		if !ok {
			writeJSON(w, http.StatusOK, companionapi.FeedPageResponse{Items: []companionapi.FeedItem{}})
			return
		}
		for _, a := range sub.Accounts {
			req.AuthorRefs = append(req.AuthorRefs, library.AuthorRef{Site: a.Site, UID: a.UID})
		}
		if len(req.AuthorRefs) == 0 {
			writeJSON(w, http.StatusOK, companionapi.FeedPageResponse{Items: []companionapi.FeedItem{}})
			return
		}
	}

	page, err := s.backend.FeedPage(req)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	items := make([]companionapi.FeedItem, 0, len(page.Items))
	for _, fe := range page.Items {
		item := s.buildFeedItem(fe, subByAccount)
		items = append(items, item)
	}
	resp := companionapi.FeedPageResponse{
		Items:   items,
		HasMore: page.HasMore,
	}
	if page.NextCursor != nil {
		resp.NextCursor = &companionapi.FeedCursor{PubTs: page.NextCursor.PubTs, Key: page.NextCursor.Key}
	}
	if page.PrevCursor != nil {
		resp.PrevCursor = &companionapi.FeedCursor{PubTs: page.PrevCursor.PubTs, Key: page.PrevCursor.Key}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) buildFeedItem(fe library.FeedEntry, subByAccount map[string]subscription.Subscription) companionapi.FeedItem {
	authorUID := fe.AuthorUID
	if authorUID == "" && fe.Mid > 0 {
		authorUID = strconv.FormatInt(fe.Mid, 10)
	}
	fi := companionapi.FeedItem{
		FeedKey:      fe.FeedKey,
		ItemKey:      fe.ItemKey,
		Site:         fe.Site,
		Kind:         fe.Kind,
		Status:       fe.CacheStatus,
		AuthorUID:    authorUID,
		AuthorName:   fe.AuthorName,
		Title:        fe.Title,
		Text:         fe.Summary,
		CanonicalURL: fe.CanonicalURL,
		PubTs:        fe.PubTs,
	}
	lookupSite, lookupUID := strings.ToLower(fe.Site), authorUID
	if fe.ItemKey != "" {
		if meta, ok, err := s.backend.GetLibraryItemMetaOnly(fe.ItemKey); err == nil && ok {
			fi.Status = meta.Status
			fi.DynamicType = meta.DynamicType
			fi.TypeLabel = meta.NormalizeDisplayTypeLabel()
			fi.Title = firstNonEmpty(meta.Title, fi.Title)
			fi.Text = firstNonEmpty(meta.Text, fi.Text)
			fi.Desc = meta.Desc
			fi.Duration = meta.Duration
			fi.AuthorUniqueID = meta.Author.UniqueID
			fi.IsForward = meta.IsForwardContent()
			fi.TotalBytes = meta.TotalBytes
			fi.Links = mapLinks(meta.Links)
			fi.Via = mapVia(meta.Via)
			fi.Origin = mapOrigin(meta.Origin)
			fi.InReplyTo = mapReplyTarget(meta.InReplyTo)
			fi.Card = mapRichCard(meta.Card)
			fi.Thread = mapThread(meta.Thread)
			fi.Parts = mapVideoParts(meta.Parts)
			fi.NotePages = mapNotePages(meta.NotePages)
			fi.Music = mapMusic(meta.Music)
			fi.Live = mapLiveInfo(meta.Live)
			fi.CoverRelPath = findCoverRelPath(meta)
			fi.VideoRelPath = findPrimaryVideoRelPath(meta)
			fi.QualitySummary = buildQualitySummary(meta)
			fi.Images = extractFeedImages(meta)

			if meta.Via != nil && meta.Via.UID != "" {
				vs := strings.ToLower(firstNonEmpty(meta.Via.Site, meta.Site))
				lookupSite, lookupUID = vs, meta.Via.UID
			}
		}
	}
	if sub, ok := subByAccount[lookupSite+":"+lookupUID]; ok {
		fi.SubID = sub.ID
		fi.SubName = sub.Name
		if s.backend.AccountAvatarPath(lookupSite, lookupUID) != "" {
			fi.AuthorAvatarPath = companionapi.AvatarPath(lookupSite, lookupUID)
		} else if sub.Avatar.Site != "" && sub.Avatar.UID != "" && s.backend.AccountAvatarPath(sub.Avatar.Site, sub.Avatar.UID) != "" {
			fi.AuthorAvatarPath = companionapi.AvatarPath(sub.Avatar.Site, sub.Avatar.UID)
		}
	} else if s.backend.AccountAvatarPath(lookupSite, lookupUID) != "" {
		fi.AuthorAvatarPath = companionapi.AvatarPath(lookupSite, lookupUID)
	}
	return fi
}

func (s *Server) handleFeedSubscriptions(w http.ResponseWriter, r *http.Request) {
	if !s.requireAnyLogin(w) {
		return
	}
	siteFilter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("site")))
	if siteFilter == "" {
		siteFilter = "all"
	}
	subs, err := s.backend.ListSubscriptions()
	if err != nil {
		writeServiceError(w, err)
		return
	}
	stats, _ := s.backend.LibraryStatistics()
	uiSt := s.backend.GetUIState()
	liveAccounts := s.backend.ListLiveAccounts()

	liveByAcct := make(map[string]service.LiveAccountState, len(liveAccounts))
	for _, la := range liveAccounts {
		liveByAcct[strings.ToLower(la.Site)+":"+la.UID] = la
	}
	latestByAcct := make(map[string]int64, len(stats.Authors))
	for _, au := range stats.Authors {
		ns, nu := library.NormalizeAuthorUID(au.Site, au.UID, au.Mid)
		if nu == "" || au.LatestPubTs <= 0 || (siteFilter != "all" && ns != siteFilter) {
			continue
		}
		latestByAcct[ns+":"+nu] = au.LatestPubTs
	}

	var rows []companionapi.FeedSubscriptionRow
	for _, sub := range subs {
		var maxTs int64
		isLive := false
		isRec := false
		for _, a := range sub.Accounts {
			as := strings.ToLower(strings.TrimSpace(a.Site))
			if siteFilter != "all" && as != siteFilter {
				continue
			}
			key := as + ":" + a.UID
			if ts := latestByAcct[key]; ts > maxTs {
				maxTs = ts
			}
			if la, ok := liveByAcct[key]; ok {
				if la.LiveStatus == 1 {
					isLive = true
				}
				if la.Recording {
					isRec = true
				}
			}
		}
		if maxTs <= 0 && !isLive {
			continue
		}
		unread := false
		if maxTs > 0 {
			seenTs, hasSeen := uiSt.HomeSeen["sub:"+sub.ID]
			if hasSeen {
				unread = maxTs > seenTs
			} else if uiSt.Anchor.PubTs > 0 {
				unread = maxTs > uiSt.Anchor.PubTs
			}
		}
		rows = append(rows, companionapi.FeedSubscriptionRow{
			SubID:       sub.ID,
			Name:        sub.Name,
			AvatarPath:  s.subscriptionAvatarPath(sub),
			Live:        isLive,
			Recording:   isRec,
			Unread:      unread,
			LatestPubTs: maxTs,
		})
	}

	// Sort: live first (unread before read), then unread, then read; within each group newest first (DEC-053 ②).
	groupOf := func(r companionapi.FeedSubscriptionRow) int {
		if r.Live {
			return 0
		}
		if r.Unread {
			return 1
		}
		return 2
	}
	sort.SliceStable(rows, func(i, j int) bool {
		gi, gj := groupOf(rows[i]), groupOf(rows[j])
		if gi != gj {
			return gi < gj
		}
		if gi == 0 && rows[i].Unread != rows[j].Unread {
			return rows[i].Unread
		}
		if rows[i].LatestPubTs != rows[j].LatestPubTs {
			return rows[i].LatestPubTs > rows[j].LatestPubTs
		}
		return rows[i].SubID < rows[j].SubID
	})

	if rows == nil {
		rows = []companionapi.FeedSubscriptionRow{}
	}
	writeJSON(w, http.StatusOK, companionapi.FeedSubscriptionsResponse{Items: rows})
}

func (s *Server) handleFeedSeen(w http.ResponseWriter, body []byte) {
	if !s.requireAnyLogin(w) {
		return
	}
	var req companionapi.FeedSeenRequest
	if err := json.Unmarshal(body, &req); err != nil || strings.TrimSpace(req.SubID) == "" {
		writeError(w, http.StatusBadRequest, companionapi.CodeBadRequest, "缺少订阅编号", nil)
		return
	}
	subID := strings.TrimSpace(req.SubID)
	seenTs := req.SeenTs
	if seenTs <= 0 {
		// Compute latest cached pubTs across the subscription's accounts.
		if subs, err := s.backend.ListSubscriptions(); err == nil {
			stats, _ := s.backend.LibraryStatistics()
			latestByAcct := make(map[string]int64, len(stats.Authors))
			for _, au := range stats.Authors {
				ns, nu := library.NormalizeAuthorUID(au.Site, au.UID, au.Mid)
				if nu != "" && au.LatestPubTs > 0 {
					latestByAcct[ns+":"+nu] = au.LatestPubTs
				}
			}
			for _, sub := range subs {
				if sub.ID != subID {
					continue
				}
				for _, a := range sub.Accounts {
					if ts := latestByAcct[strings.ToLower(a.Site)+":"+a.UID]; ts > seenTs {
						seenTs = ts
					}
				}
				break
			}
		}
		if seenTs <= 0 {
			seenTs = s.now().Unix()
		}
	}

	// Share read state with desktop ui-state.json (DEC-085 ⑥).
	if _, err := s.backend.MutateUIState(func(st *uistate.State) {
		if st.HomeSeen == nil {
			st.HomeSeen = make(map[string]int64)
		}
		key := "sub:" + subID
		if seenTs > st.HomeSeen[key] {
			st.HomeSeen[key] = seenTs
		}
	}); err != nil {
		writeServiceError(w, err)
		return
	}
	s.backend.PublishFeedSeen(subID)
	if s.onFeedSeen != nil {
		s.onFeedSeen()
	}
	s.hub.Publish(companionapi.EventFrame{
		Type: companionapi.EventFeedChanged,
		Key:  "sub:" + subID,
	})
	writeJSON(w, http.StatusOK, companionapi.ActionResponse{OK: true})
}

func (s *Server) handleGetFeedPosition(w http.ResponseWriter, dev DeviceRecord) {
	if !s.requireAnyLogin(w) {
		return
	}
	positions, err := s.store.HomePositions(dev.ID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, companionapi.CodeUnauthorized, "设备记录不存在", nil)
		return
	}
	if positions == nil {
		positions = map[string]companionapi.HomePosition{}
	}
	writeJSON(w, http.StatusOK, companionapi.HomePositionsResponse{Positions: positions})
}

func (s *Server) handlePutFeedPosition(w http.ResponseWriter, body []byte, dev DeviceRecord) {
	if !s.requireAnyLogin(w) {
		return
	}
	var pos companionapi.HomePosition
	if err := json.Unmarshal(body, &pos); err != nil {
		writeError(w, http.StatusBadRequest, companionapi.CodeBadRequest, "阅读位置格式不正确", nil)
		return
	}
	siteKey := pos.Site
	if strings.TrimSpace(siteKey) == "" {
		siteKey = "all"
	}
	if err := s.store.SaveHomePosition(dev.ID, siteKey, pos); err != nil {
		switch {
		case errors.Is(err, ErrInvalidPositionSite):
			writeError(w, http.StatusBadRequest, companionapi.CodeBadRequest, "不支持的站点筛选", nil)
		case errors.Is(err, ErrDeviceNotFound):
			writeError(w, http.StatusUnauthorized, companionapi.CodeUnauthorized, "设备记录不存在", nil)
		default:
			s.logf("save home position failed: device=%s", dev.ID)
			writeError(w, http.StatusInternalServerError, companionapi.CodeInternal, "保存阅读位置失败，请在电脑端查看日志", nil)
		}
		return
	}
	writeJSON(w, http.StatusOK, companionapi.ActionResponse{OK: true})
}

func (s *Server) handleLibraryList(w http.ResponseWriter, r *http.Request) {
	if !s.requireAnyLogin(w) {
		return
	}
	q := r.URL.Query()
	limit := 40
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			limit = v
		}
	}
	if limit > 100 {
		limit = 100
	}
	offset := 0
	if raw := strings.TrimSpace(q.Get("offset")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v >= 0 {
			offset = v
		}
	}

	kind := strings.ToLower(strings.TrimSpace(q.Get("type")))
	if kind == "" {
		kind = strings.ToLower(strings.TrimSpace(q.Get("kind")))
	}
	if kind == "" {
		kind = "all"
	}
	siteFilter := strings.ToLower(strings.TrimSpace(q.Get("site")))

	filter := library.FeedFilter{
		Site:    siteFilter,
		Kind:    kind,
		Keyword: strings.TrimSpace(q.Get("q")),
		Limit:   limit + 1,
		Offset:  offset,
	}
	if kind == "image" || kind == "video" {
		filter.ExcludeForward = true
	}

	subs, _ := s.backend.ListSubscriptions()
	subByAccount := make(map[string]subscription.Subscription)
	subByID := make(map[string]subscription.Subscription, len(subs))
	for _, sub := range subs {
		subByID[sub.ID] = sub
		for _, a := range sub.Accounts {
			subByAccount[strings.ToLower(a.Site)+":"+a.UID] = sub
		}
	}
	if subID := strings.TrimSpace(q.Get("subId")); subID != "" {
		if sub, ok := subByID[subID]; ok {
			for _, a := range sub.Accounts {
				filter.AuthorRefs = append(filter.AuthorRefs, library.AuthorRef{Site: a.Site, UID: a.UID})
			}
			if len(filter.AuthorRefs) == 0 {
				writeJSON(w, http.StatusOK, companionapi.LibraryListResponse{Items: []companionapi.LibraryItemSummary{}})
				return
			}
		} else {
			writeJSON(w, http.StatusOK, companionapi.LibraryListResponse{Items: []companionapi.LibraryItemSummary{}})
			return
		}
	} else if uid := strings.TrimSpace(q.Get("authorUid")); uid != "" {
		authorSite := strings.ToLower(strings.TrimSpace(q.Get("authorSite")))
		if authorSite == "" && siteFilter != "" && siteFilter != "all" {
			authorSite = siteFilter
		}
		filter.AuthorRefs = []library.AuthorRef{{Site: authorSite, UID: uid}}
	}

	rawItems, err := s.backend.ListLibraryItems(filter)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	hasMore := false
	if len(rawItems) > limit {
		hasMore = true
		rawItems = rawItems[:limit]
	}

	items := make([]companionapi.LibraryItemSummary, 0, len(rawItems))
	for _, meta := range rawItems {
		items = append(items, buildLibrarySummary(meta, subByAccount))
	}

	resp := companionapi.LibraryListResponse{
		Items:   items,
		Total:   offset + len(items),
		HasMore: hasMore,
	}
	if hasMore {
		resp.NextOffset = offset + len(items)
	}
	if q.Get("includeAuthors") == "1" || q.Get("includeAuthors") == "true" {
		resp.Authors = s.buildLibraryAuthors(subs, siteFilter)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) buildLibraryAuthors(subs []subscription.Subscription, siteFilter string) []companionapi.LibraryAuthorSummary {
	stats, err := s.backend.LibraryStatistics()
	if err != nil {
		return nil
	}
	byAcct := make(map[string]library.ItemAuthor, len(stats.Authors))
	for _, au := range stats.Authors {
		ns, nu := library.NormalizeAuthorUID(au.Site, au.UID, au.Mid)
		if nu == "" || (siteFilter != "" && siteFilter != "all" && ns != siteFilter) {
			continue
		}
		byAcct[ns+":"+nu] = au
	}
	claimed := make(map[string]bool)
	var out []companionapi.LibraryAuthorSummary
	for _, sub := range subs {
		var count int
		var totalBytes int64
		for _, a := range sub.Accounts {
			k := strings.ToLower(a.Site) + ":" + a.UID
			claimed[k] = true
			if au, ok := byAcct[k]; ok {
				count += au.ItemCount
				totalBytes += au.TotalBytes
			}
		}
		if count > 0 {
			out = append(out, companionapi.LibraryAuthorSummary{
				Kind:       "subscription",
				SubID:      sub.ID,
				Name:       sub.Name,
				AvatarPath: s.subscriptionAvatarPath(sub),
				ItemCount:  count,
				TotalBytes: totalBytes,
			})
		}
	}
	for k, au := range byAcct {
		if claimed[k] || au.ItemCount <= 0 {
			continue
		}
		ns, nu := library.NormalizeAuthorUID(au.Site, au.UID, au.Mid)
		avatar := ""
		if s.backend.AccountAvatarPath(ns, nu) != "" {
			avatar = companionapi.AvatarPath(ns, nu)
		}
		out = append(out, companionapi.LibraryAuthorSummary{
			Kind:       "author",
			Site:       ns,
			UID:        nu,
			Name:       au.Name,
			AvatarPath: avatar,
			ItemCount:  au.ItemCount,
			TotalBytes: au.TotalBytes,
		})
	}
	return out
}

func (s *Server) handleLibraryItem(w http.ResponseWriter, key string) {
	if !s.requireAnyLogin(w) {
		return
	}
	meta, ok, err := s.backend.GetLibraryItem(key)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, companionapi.CodeNotFound, "媒体库里找不到这个条目", nil)
		return
	}
	authorSite, authorUID := library.NormalizeAuthorUID(meta.Site, meta.Author.UID, meta.Author.Mid)
	detail := companionapi.LibraryItemDetail{
		ItemKey:        meta.ItemKey,
		ID:             meta.ID,
		Site:           meta.Site,
		Kind:           meta.Kind,
		Status:         meta.Status,
		Title:          meta.Title,
		Desc:           meta.Desc,
		Text:           meta.Text,
		DynamicType:    meta.DynamicType,
		TypeLabel:      meta.NormalizeDisplayTypeLabel(),
		CanonicalURL:   meta.URL,
		PubTs:          meta.PubTs,
		CachedAt:       meta.CachedAt,
		Duration:       meta.Duration,
		AuthorUID:      authorUID,
		AuthorName:     meta.Author.Name,
		AuthorUniqueID: meta.Author.UniqueID,
		Media:          mapMediaFiles(meta.Media),
		Parts:          mapVideoParts(meta.Parts),
		VideoInfo:      mapDouyinVideoInfo(meta.DouyinVideo),
		NotePages:      mapNotePages(meta.NotePages),
		Music:          mapMusic(meta.Music),
		Live:           mapLiveInfo(meta.Live),
		Missing:        mapMissing(meta.Missing),
		Via:            mapVia(meta.Via),
		Origin:         mapOrigin(meta.Origin),
		InReplyTo:      mapReplyTarget(meta.InReplyTo),
		Card:           mapRichCard(meta.Card),
		Thread:         mapThread(meta.Thread),
		Links:          mapLinks(meta.Links),
		IsForward:      meta.IsForwardContent(),
		ArchivedOf:     meta.ArchivedOf,
		TotalBytes:     meta.TotalBytes,
	}
	lookupSite, lookupUID := authorSite, authorUID
	if meta.Via != nil && meta.Via.UID != "" {
		lookupSite = strings.ToLower(firstNonEmpty(meta.Via.Site, meta.Site))
		lookupUID = meta.Via.UID
	}
	if sub, found := s.backend.OwnerOfAccount(lookupSite, lookupUID); found {
		detail.SubID = sub.ID
		detail.SubName = sub.Name
	}
	if s.backend.AccountAvatarPath(lookupSite, lookupUID) != "" {
		detail.AuthorAvatarPath = companionapi.AvatarPath(lookupSite, lookupUID)
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleSubscriptions(w http.ResponseWriter) {
	if !s.requireAnyLogin(w) {
		return
	}
	subs, err := s.backend.ListSubscriptions()
	if err != nil {
		writeServiceError(w, err)
		return
	}
	items := s.mapSubscriptions(subs)
	writeJSON(w, http.StatusOK, companionapi.SubscriptionsResponse{Items: items})
}

func (s *Server) handleSubscriptionDetail(w http.ResponseWriter, id string) {
	if !s.requireAnyLogin(w) {
		return
	}
	subs, err := s.backend.ListSubscriptions()
	if err != nil {
		writeServiceError(w, err)
		return
	}
	for _, sub := range subs {
		if sub.ID == id {
			mapped := s.mapSubscriptions([]subscription.Subscription{sub})
			writeJSON(w, http.StatusOK, companionapi.SubscriptionDetailResponse{Subscription: mapped[0]})
			return
		}
	}
	writeError(w, http.StatusNotFound, companionapi.CodeNotFound, "找不到这个订阅", nil)
}

func (s *Server) handleSubscriptionsAccounts(w http.ResponseWriter) {
	if !s.requireAnyLogin(w) {
		return
	}
	subs, err := s.backend.ListSubscriptions()
	if err != nil {
		writeServiceError(w, err)
		return
	}
	mapped := s.mapSubscriptions(subs)
	var all []companionapi.SubscriptionAccountDTO
	for _, sub := range mapped {
		all = append(all, sub.Accounts...)
	}
	if all == nil {
		all = []companionapi.SubscriptionAccountDTO{}
	}
	writeJSON(w, http.StatusOK, companionapi.AllAccountsResponse{Items: all})
}

func (s *Server) mapSubscriptions(subs []subscription.Subscription) []companionapi.SubscriptionDTO {
	works := s.backend.SubscriptionAccountWorks()
	liveAccounts := s.backend.ListLiveAccounts()
	liveByAcct := make(map[string]service.LiveAccountState, len(liveAccounts))
	for _, la := range liveAccounts {
		liveByAcct[strings.ToLower(la.Site)+":"+la.UID] = la
	}
	tagLabels := make(map[string]string)
	for _, t := range s.backend.ListCapabilityTags() {
		tagLabels[t.ID] = t.Name
	}

	out := make([]companionapi.SubscriptionDTO, 0, len(subs))
	for _, sub := range subs {
		dto := companionapi.SubscriptionDTO{
			ID:         sub.ID,
			Name:       sub.Name,
			Note:       sub.Note,
			AvatarPath: s.subscriptionAvatarPath(sub),
			Tags:       append([]string(nil), sub.Tags...),
			CreatedAt:  sub.CreatedAt,
		}
		for _, tid := range sub.Tags {
			if lbl, ok := tagLabels[tid]; ok {
				dto.TagLabels = append(dto.TagLabels, lbl)
			} else {
				dto.TagLabels = append(dto.TagLabels, tid)
			}
		}
		dto.PresetLabel = trackingPresetLabel(sub.Tags)
		for _, l := range sub.Links {
			dto.Links = append(dto.Links, companionapi.CustomLinkDTO{Label: l.Label, URL: l.URL})
		}
		for _, a := range sub.Accounts {
			as := strings.ToLower(strings.TrimSpace(a.Site))
			acctKey := as + ":" + a.UID
			liveSupported := as == string(site.Bilibili) || as == string(site.Douyin)
			ad := companionapi.SubscriptionAccountDTO{
				SubID:         sub.ID,
				SubName:       sub.Name,
				Site:          as,
				UID:           a.UID,
				AccountKey:    acctKey,
				UniqueID:      a.UniqueID,
				Name:          a.Name,
				LiveSupported: liveSupported,
			}
			if s.backend.AccountAvatarPath(as, a.UID) != "" {
				ad.AvatarPath = companionapi.AvatarPath(as, a.UID)
			}
			if w, ok := works[acctKey]; ok {
				ad.TotalWorks = w.Total
				ad.CachedWorks = w.Cached
			}
			if liveSupported {
				if la, ok := liveByAcct[acctKey]; ok {
					ad.LiveStatus = la.LiveStatus
					ad.LiveTitle = la.Title
					ad.LiveStatusTxt = la.StatusText
					ad.Recording = la.Recording
					ad.RecordingKey = la.RecordingKey
					ad.QualityName = la.QualityName
					ad.Codec = la.Codec
					if la.LiveStatus == 1 {
						dto.Live = true
					}
					if la.Recording {
						dto.Recording = true
					}
				}
			}
			dto.Accounts = append(dto.Accounts, ad)
		}
		out = append(out, dto)
	}
	return out
}

func (s *Server) handleDownloadTasks(w http.ResponseWriter) {
	if !s.requireAnyLogin(w) {
		return
	}
	tasks, err := s.backend.ListDownloadTasks()
	if err != nil {
		writeServiceError(w, err)
		return
	}
	items := make([]companionapi.DownloadTaskDTO, 0, len(tasks))
	for _, t := range tasks {
		items = append(items, s.mapDownloadTask(t))
	}
	writeJSON(w, http.StatusOK, companionapi.DownloadTasksResponse{Items: items})
}

func (s *Server) handleDownloadTaskDetail(w http.ResponseWriter, id string) {
	if !s.requireAnyLogin(w) {
		return
	}
	detail, ok, err := s.backend.TaskDetail(id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, companionapi.CodeNotFound, "找不到这个下载任务", nil)
		return
	}
	files := make([]companionapi.DownloadTaskFileDTO, 0, len(detail.Files))
	for _, f := range detail.Files {
		files = append(files, companionapi.DownloadTaskFileDTO{
			Role:            f.Role,
			Path:            f.Path,
			Status:          f.Status,
			DownloadedBytes: f.DownloadedBytes,
			TotalBytes:      f.TotalBytes,
			SpeedBps:        f.SpeedBps,
		})
	}
	attempts := make([]companionapi.DownloadTaskAttemptDTO, 0, len(detail.Attempts))
	for _, a := range detail.Attempts {
		attempts = append(attempts, companionapi.DownloadTaskAttemptDTO{
			Time:       a.Time,
			File:       a.File,
			Offset:     a.Offset,
			Host:       a.Host,
			Attempt:    a.Attempt,
			ErrorKind:  a.ErrorKind,
			HTTPStatus: a.HTTPStatus,
		})
	}
	writeJSON(w, http.StatusOK, companionapi.DownloadTaskDetailResponse{
		Task:     s.mapDownloadTask(detail.Task),
		Files:    files,
		Attempts: attempts,
	})
}

func (s *Server) handleDownloadTaskAction(w http.ResponseWriter, rest string) {
	if !s.requireAnyLogin(w) {
		return
	}
	slash := strings.LastIndexByte(rest, '/')
	if slash <= 0 || slash >= len(rest)-1 {
		writeError(w, http.StatusNotFound, companionapi.CodeNotFound, "没有这个任务操作", nil)
		return
	}
	id, err := url.PathUnescape(rest[:slash])
	if err != nil || strings.TrimSpace(id) == "" {
		writeError(w, http.StatusBadRequest, companionapi.CodeBadRequest, "任务编号不正确", nil)
		return
	}
	action := rest[slash+1:]
	switch action {
	case "pause":
		t, err := s.backend.PauseTask(id)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s.mapDownloadTask(t))
	case "resume":
		t, err := s.backend.ResumeTask(id)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s.mapDownloadTask(t))
	case "cancel":
		t, err := s.backend.CancelTask(id)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s.mapDownloadTask(t))
	case "retry":
		t, err := s.backend.RetryTask(id)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s.mapDownloadTask(t))
	case "remove":
		if err := s.backend.RemoveTask(id); err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, companionapi.ActionResponse{OK: true})
	default:
		writeError(w, http.StatusNotFound, companionapi.CodeNotFound, "不支持的任务操作", nil)
	}
}

func (s *Server) handleClearFinishedDownloads(w http.ResponseWriter) {
	if !s.requireAnyLogin(w) {
		return
	}
	count, err := s.backend.RemoveFinishedTasks()
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, companionapi.ActionResponse{OK: true, Count: count})
}

func (s *Server) handleCacheQueues(w http.ResponseWriter) {
	if !s.requireAnyLogin(w) {
		return
	}
	list := s.backend.ListCacheQueue()
	items := make([]companionapi.CacheQueueDTO, 0, len(list))
	for _, q := range list {
		items = append(items, s.mapCacheQueue(q))
	}
	writeJSON(w, http.StatusOK, companionapi.CacheQueuesResponse{Items: items})
}

func (s *Server) handleCacheQueueAction(w http.ResponseWriter, rest string) {
	if !s.requireAnyLogin(w) {
		return
	}
	slash := strings.LastIndexByte(rest, '/')
	if slash <= 0 || slash >= len(rest)-1 {
		writeError(w, http.StatusNotFound, companionapi.CodeNotFound, "没有这个队列操作", nil)
		return
	}
	id, err := url.PathUnescape(rest[:slash])
	if err != nil || strings.TrimSpace(id) == "" {
		writeError(w, http.StatusBadRequest, companionapi.CodeBadRequest, "队列编号不正确", nil)
		return
	}
	action := rest[slash+1:]
	switch action {
	case "pause":
		q, err := s.backend.PauseCacheQueue(id)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s.mapCacheQueue(q))
	case "resume":
		q, err := s.backend.ResumeCacheQueue(id)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s.mapCacheQueue(q))
	case "cancel":
		q, err := s.backend.CancelCacheQueue(id)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s.mapCacheQueue(q))
	case "remove":
		if err := s.backend.RemoveCacheQueue(id); err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, companionapi.ActionResponse{OK: true})
	default:
		writeError(w, http.StatusNotFound, companionapi.CodeNotFound, "不支持的队列操作", nil)
	}
}

func (s *Server) handleLiveRecordings(w http.ResponseWriter) {
	if !s.requireAnyLogin(w) {
		return
	}
	rooms := s.backend.ListLiveAccounts()
	items := make([]companionapi.LiveRecordingDTO, 0, len(rooms))
	seenKeys := make(map[string]bool, len(rooms))
	seenActive := make(map[string]bool, len(rooms))
	for _, r := range rooms {
		if !r.Recording && r.RecordBytes == 0 && len(r.Incidents) == 0 && r.StopReason == "" {
			continue
		}
		dto := s.mapLiveRecording(r)
		items = append(items, dto)
		if dto.RecordingKey != "" {
			seenKeys[dto.RecordingKey] = true
		}
		if dto.ItemKey != "" {
			seenKeys[dto.ItemKey] = true
		}
		if dto.Site != "" && dto.UID != "" {
			seenActive[strings.ToLower(dto.Site)+":"+dto.UID+":"+dto.RoomID] = true
		}
	}
	// Also append recent finished live recordings from library (last 7 days), deduplicated against live state.
	cutoff := s.now().Add(-7 * 24 * time.Hour).Unix()
	if finished, err := s.backend.ListLibraryItems(library.FeedFilter{Kind: "live", Limit: 50}); err == nil {
		for _, item := range finished {
			if item.Status == "recording" || item.PubTs < cutoff {
				continue
			}
			if seenKeys[item.ItemKey] {
				continue
			}
			roomID := ""
			if item.Live != nil {
				roomID = firstNonEmpty(item.Live.RoomIDStr, strconv.FormatInt(item.Live.RoomID, 10))
			}
			if roomID != "" && seenActive[strings.ToLower(item.Site)+":"+item.Author.UID+":"+roomID] && item.PubTs >= s.now().Add(-15*time.Minute).Unix() {
				continue
			}
			rec := companionapi.LiveRecordingDTO{
				RecordingKey: item.ItemKey,
				AccountKey:   item.Site + ":" + item.Author.UID,
				Site:         item.Site,
				UID:          item.Author.UID,
				Uname:        item.Author.Name,
				Title:        item.Title,
				StatusText:   "已结束",
				Recording:    false,
				Bytes:        item.TotalBytes,
				ItemKey:      item.ItemKey,
			}
			if item.Live != nil {
				rec.RoomID = roomID
				rec.QualityName = item.Live.QualityName
				rec.Codec = item.Live.Codec
				rec.Segments = len(item.Live.Segments)
				rec.StartedAt = item.Live.StartedAt
				rec.EndedAt = item.Live.EndedAt
				rec.StopReason = item.Live.StopReason
				rec.Incidents = mapLiveIncidents(item.Live.Incidents)
				for _, seg := range item.Live.Segments {
					rec.DurationSec += seg.DurationSec
				}
			}
			if s.backend.AccountAvatarPath(item.Site, item.Author.UID) != "" {
				rec.AvatarPath = companionapi.AvatarPath(item.Site, item.Author.UID)
			}
			seenKeys[item.ItemKey] = true
			items = append(items, rec)
		}
	}
	writeJSON(w, http.StatusOK, companionapi.LiveRecordingsResponse{Items: items})
}

func (s *Server) handleLiveIncidents(w http.ResponseWriter) {
	if !s.requireAnyLogin(w) {
		return
	}
	var sessions []companionapi.LiveIncidentSessionDTO
	seenKeys := make(map[string]bool)
	seenAcct := make(map[string]bool)
	for _, r := range s.backend.ListLiveAccounts() {
		if len(r.Incidents) == 0 {
			continue
		}
		sessions = append(sessions, companionapi.LiveIncidentSessionDTO{
			RecordingKey: r.RecordingKey,
			Site:         r.Site,
			UID:          r.UID,
			Uname:        r.Uname,
			Title:        r.Title,
			Recording:    r.Recording,
			Incidents:    mapLiveIncidents(r.Incidents),
		})
		if r.RecordingKey != "" {
			seenKeys[r.RecordingKey] = true
		}
		if r.Site != "" && r.UID != "" {
			seenAcct[strings.ToLower(r.Site)+":"+r.UID] = true
		}
	}
	cutoff := s.now().Add(-7 * 24 * time.Hour).Unix()
	if finished, err := s.backend.ListLibraryItems(library.FeedFilter{Kind: "live", Limit: 100}); err == nil {
		for _, item := range finished {
			if item.Live == nil || len(item.Live.Incidents) == 0 || item.PubTs < cutoff {
				continue
			}
			if seenKeys[item.ItemKey] {
				continue
			}
			if seenAcct[strings.ToLower(item.Site)+":"+item.Author.UID] && item.PubTs >= s.now().Add(-15*time.Minute).Unix() {
				continue
			}
			seenKeys[item.ItemKey] = true
			sessions = append(sessions, companionapi.LiveIncidentSessionDTO{
				ItemKey:   item.ItemKey,
				Site:      item.Site,
				UID:       item.Author.UID,
				Uname:     item.Author.Name,
				Title:     item.Title,
				Recording: false,
				StartedAt: item.Live.StartedAt,
				EndedAt:   item.Live.EndedAt,
				Incidents: mapLiveIncidents(item.Live.Incidents),
			})
		}
	}
	if sessions == nil {
		sessions = []companionapi.LiveIncidentSessionDTO{}
	}
	writeJSON(w, http.StatusOK, companionapi.LiveIncidentsResponse{Items: sessions})
}

func (s *Server) handleLiveStart(w http.ResponseWriter, r *http.Request, body []byte) {
	if !s.requireAnyLogin(w) {
		return
	}
	var req companionapi.StartLiveRequest
	if err := json.Unmarshal(body, &req); err != nil || strings.TrimSpace(req.AccountKey) == "" {
		writeError(w, http.StatusBadRequest, companionapi.CodeBadRequest, "缺少账号标识 accountKey", nil)
		return
	}
	accountKey := strings.TrimSpace(req.AccountKey)
	siteID, _, err := site.ParseAccountKey(accountKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, companionapi.CodeBadRequest, "无效的账号标识 accountKey", nil)
		return
	}
	if siteID != site.Bilibili && siteID != site.Douyin {
		writeError(w, http.StatusBadRequest, companionapi.CodeUnsupported, "该站点暂不支持直播录制", func(b *companionapi.ErrorBody) {
			b.Error.Site = string(siteID)
		})
		return
	}
	state, err := s.backend.StartLiveRecordingByAccount(r.Context(), accountKey)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.mapLiveRecording(state))
}

func (s *Server) handleLiveStop(w http.ResponseWriter, id string) {
	if !s.requireAnyLogin(w) {
		return
	}
	state, err := s.backend.StopLiveRecordingByKey(id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.mapLiveRecording(state))
}

func (s *Server) handleCheckPlan(w http.ResponseWriter) {
	if !s.requireAnyLogin(w) {
		return
	}
	plan := s.backend.CheckPlan()
	diag := s.backend.DiagnosticsSnapshot()
	rows := make([]companionapi.CheckPlanRowDTO, 0, len(plan))
	for _, r := range plan {
		rows = append(rows, companionapi.CheckPlanRowDTO{
			SubID:         r.SubID,
			SubName:       r.SubName,
			Site:          r.Site,
			UID:           r.UID,
			Name:          r.Name,
			AutoRecord:    r.AutoRecord,
			LiveNote:      r.LiveNote,
			LastCheckedAt: r.LastCheckedAt,
			LastKind:      r.LastKind,
			Note:          r.Note,
			Plan:          r.Plan,
			NextAt:        r.NextAt,
			AfterBackoff:  r.AfterBackoff,
			LiveCheckedAt: r.LiveCheckedAt,
			LiveEverySec:  r.LiveEverySec,
		})
	}
	checking := strings.HasPrefix(diag.SubscriptionCheckStatus, "正在检查")
	writeJSON(w, http.StatusOK, companionapi.CheckPlanResponse{
		Items:    rows,
		Checking: checking,
		Status:   diag.SubscriptionCheckStatus,
	})
}

func (s *Server) publishCheckError(fallbackSite string, err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	siteID := string(site.SiteOf(err))
	if siteID == "" {
		siteID = fallbackSite
	}
	kind := site.KindOf(err)
	var msg string
	switch kind {
	case site.ErrKindAuthRequired:
		if siteID != "" {
			msg = fmt.Sprintf("请先在电脑上登录%s", site.ID(siteID).DisplayName())
		} else {
			msg = "请先在电脑上登录对应站点"
		}
	case site.ErrKindRiskControl:
		msg = "触发站点风控，检查更新已暂停"
	case site.ErrKindRateLimited:
		msg = "站点限流中，检查更新已暂停"
	default:
		msg = "检查更新失败，请在电脑端查看日志"
	}
	if s.hub != nil {
		s.hub.Publish(companionapi.EventFrame{
			Type:      companionapi.EventSubsCheck,
			Site:      siteID,
			State:     kind,
			Message:   msg,
			Timestamp: s.now().UTC().Format(time.RFC3339),
		})
	}
}

func (s *Server) handleChecksStart(w http.ResponseWriter, body []byte) {
	if !s.requireAnyLogin(w) {
		return
	}
	var req companionapi.CheckRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			writeError(w, http.StatusBadRequest, companionapi.CodeBadRequest, "检查请求格式不正确", nil)
			return
		}
	}
	// Trigger manual check asynchronously so HTTP returns immediately; progress and errors flow over WS subs:check.
	if len(req.SubIDs) == 0 {
		go func() {
			if _, err := s.backend.CheckSubscriptionsNow(context.Background(), 0); err != nil {
				s.publishCheckError("", err)
			}
		}()
		writeJSON(w, http.StatusOK, companionapi.ActionResponse{OK: true, Message: "已开始检查全部订阅"})
		return
	}

	// Validate requested subIds exist and reject synchronously when all accounts of the
	// requested subscription(s) belong to unauthenticated sites (FR-MOB-022, AC-96).
	allSubs, err := s.backend.ListSubscriptions()
	if err != nil {
		writeServiceError(w, err)
		return
	}
	byID := make(map[string]subscription.Subscription, len(allSubs))
	for _, sub := range allSubs {
		byID[sub.ID] = sub
	}
	ids := make([]string, 0, len(req.SubIDs))
	anyLoggedInAccount := false
	firstUnauthSite := ""
	totalAccounts := 0
	for _, rawID := range req.SubIDs {
		id := strings.TrimSpace(rawID)
		sub, ok := byID[id]
		if !ok {
			writeError(w, http.StatusNotFound, companionapi.CodeNotFound, "找不到对应的订阅", nil)
			return
		}
		ids = append(ids, id)
		for _, acc := range sub.Accounts {
			totalAccounts++
			sid := strings.ToLower(strings.TrimSpace(acc.Site))
			if sid == "" {
				sid = string(site.Bilibili)
			}
			if s.backend.StatusOf(sid).LoggedIn {
				anyLoggedInAccount = true
			} else if firstUnauthSite == "" {
				firstUnauthSite = sid
			}
		}
	}
	if totalAccounts > 0 && !anyLoggedInAccount && firstUnauthSite != "" {
		writeError(w, http.StatusPreconditionFailed, companionapi.CodeAuthRequired, fmt.Sprintf("请先在电脑上登录%s", site.ID(firstUnauthSite).DisplayName()), func(b *companionapi.ErrorBody) {
			b.Error.Site = firstUnauthSite
		})
		return
	}

	if len(ids) == 1 {
		subID := ids[0]
		go func() {
			if _, err := s.backend.CheckSubscription(context.Background(), subID); err != nil {
				s.publishCheckError("", err)
			}
		}()
		writeJSON(w, http.StatusOK, companionapi.ActionResponse{OK: true, Message: "已开始检查更新"})
		return
	}
	go func() {
		for _, id := range ids {
			if _, err := s.backend.CheckSubscription(context.Background(), id); err != nil {
				s.publishCheckError("", err)
				break
			}
		}
	}()
	writeJSON(w, http.StatusOK, companionapi.ActionResponse{OK: true, Count: len(ids), Message: "已开始检查更新"})
}

func (s *Server) handleCheckAccount(w http.ResponseWriter, body []byte) {
	if !s.requireAnyLogin(w) {
		return
	}
	var req companionapi.CheckAccountRequest
	if err := json.Unmarshal(body, &req); err != nil || strings.TrimSpace(req.Site) == "" || strings.TrimSpace(req.UID) == "" {
		writeError(w, http.StatusBadRequest, companionapi.CodeBadRequest, "缺少账号参数", nil)
		return
	}
	// Pre-check site login synchronously so unauthenticated sites immediately report auth_required (AC-96).
	sid := strings.ToLower(strings.TrimSpace(req.Site))
	if !s.backend.StatusOf(sid).LoggedIn {
		writeError(w, http.StatusPreconditionFailed, companionapi.CodeAuthRequired, fmt.Sprintf("请先在电脑上登录%s", site.ID(sid).DisplayName()), func(b *companionapi.ErrorBody) {
			b.Error.Site = sid
		})
		return
	}
	go func() {
		if _, err := s.backend.CheckAccount(context.Background(), strings.TrimSpace(req.SubID), sid, strings.TrimSpace(req.UID)); err != nil {
			s.publishCheckError(sid, err)
		}
	}()
	writeJSON(w, http.StatusOK, companionapi.ActionResponse{OK: true, Message: "已开始检查该账号"})
}

func (s *Server) handleConflicts(w http.ResponseWriter) {
	if !s.requireAnyLogin(w) {
		return
	}
	list, err := s.backend.ListCacheConflicts()
	if err != nil {
		writeServiceError(w, err)
		return
	}
	items := make([]companionapi.ConflictDTO, 0, len(list))
	for _, c := range list {
		items = append(items, companionapi.ConflictDTO{
			ID:            c.ID,
			ItemKey:       c.ItemKey,
			Site:          c.Site,
			Kind:          c.Kind,
			AuthorName:    c.AuthorName,
			Title:         c.Title,
			DetectedAt:    c.DetectedAt,
			ReasonKind:    c.ReasonKind,
			ReasonLabel:   c.ReasonLabel,
			LocalSummary:  c.LocalSummary,
			RemoteSummary: c.RemoteSummary,
			State:         c.State,
			LastError:     c.LastError,
		})
	}
	writeJSON(w, http.StatusOK, companionapi.ConflictsResponse{Items: items})
}

func (s *Server) handleTransfers(w http.ResponseWriter) {
	if !s.requireAnyLogin(w) {
		return
	}
	jobs := s.backend.TransferJobs()
	items := make([]companionapi.TransferJobDTO, 0, len(jobs))
	for _, j := range jobs {
		// Intentionally omit Source and Target local filesystem paths (NFR-MOB-SEC-003).
		items = append(items, companionapi.TransferJobDTO{
			ID:         j.ID,
			Kind:       j.Kind,
			Status:     j.Status,
			DoneItems:  j.Progress.Done,
			TotalItems: j.Progress.Total,
			DoneBytes:  j.Progress.Bytes,
			TotalBytes: j.Progress.TotalBytes,
			SpeedBps:   j.SpeedBps,
			Error:      j.Error,
			StartedAt:  j.StartedAt,
			FinishedAt: j.FinishedAt,
		})
	}
	writeJSON(w, http.StatusOK, companionapi.TransfersResponse{Items: items})
}

func (s *Server) handleUnpairSelf(w http.ResponseWriter, dev DeviceRecord, ipStr string) {
	if _, err := s.store.RevokeDevice(dev.ID); err != nil && !errors.Is(err, ErrDeviceNotFound) {
		writeError(w, http.StatusInternalServerError, companionapi.CodeInternal, "解除配对失败", nil)
		return
	}
	s.logf("companion device self-unpaired: device=%s ip=%s", dev.ID, ipStr)
	s.hub.RevokeDevice(dev.ID, 500*time.Millisecond)
	writeJSON(w, http.StatusOK, companionapi.ActionResponse{OK: true})
}

func (s *Server) requireAnyLogin(w http.ResponseWriter) bool {
	if s.backend.HasAnyLogin() {
		return true
	}
	writeError(w, http.StatusPreconditionFailed, companionapi.CodeAuthRequired, "请先在电脑上登录一个站点", nil)
	return false
}

func (s *Server) subscriptionAvatarPath(sub subscription.Subscription) string {
	if sub.Avatar.Site != "" && sub.Avatar.UID != "" {
		if s.backend.AccountAvatarPath(sub.Avatar.Site, sub.Avatar.UID) != "" {
			return companionapi.AvatarPath(sub.Avatar.Site, sub.Avatar.UID)
		}
	}
	for _, a := range sub.Accounts {
		if s.backend.AccountAvatarPath(a.Site, a.UID) != "" {
			return companionapi.AvatarPath(a.Site, a.UID)
		}
	}
	return ""
}

func (s *Server) mapDownloadTask(t download.Task) companionapi.DownloadTaskDTO {
	var speed int64
	for _, f := range t.Files {
		speed += f.SpeedBps
	}
	dto := companionapi.DownloadTaskDTO{
		TaskID:          t.TaskID,
		Site:            t.Site,
		Kind:            t.Kind,
		Source:          t.Source,
		TargetID:        t.TargetID,
		Title:           t.Title,
		PartTitle:       t.PartTitle,
		AuthorUID:       t.AuthorUID,
		AuthorName:      t.AuthorName,
		Status:          t.Status,
		PauseReason:     t.PauseReason,
		ErrorKind:       t.ErrorKind,
		ErrorMessage:    t.ErrorMessage,
		QualityHint:     t.QualityHint,
		BytesDownloaded: t.BytesDownloaded,
		TotalBytes:      t.TotalBytes,
		SpeedBps:        speed,
		Progress:        t.Progress,
		ItemKey:         t.ItemKey,
		CreatedAt:       t.CreatedAt,
		UpdatedAt:       t.UpdatedAt,
	}
	if t.ItemKey != "" {
		if meta, ok, err := s.backend.GetLibraryItemMetaOnly(t.ItemKey); err == nil && ok {
			dto.CoverRelPath = findCoverRelPath(meta)
		}
	}
	return dto
}

func (s *Server) mapCacheQueue(q service.CacheQueueTask) companionapi.CacheQueueDTO {
	recent := make([]companionapi.CacheQueueRecentDTO, 0, len(q.RecentItems))
	for _, ri := range q.RecentItems {
		recent = append(recent, companionapi.CacheQueueRecentDTO{
			ItemKey:  ri.ItemKey,
			Title:    ri.Title,
			CachedAt: ri.CachedAt,
		})
	}
	avatar := ""
	if s.backend.AccountAvatarPath(q.Site, q.User) != "" {
		avatar = companionapi.AvatarPath(q.Site, q.User)
	}
	return companionapi.CacheQueueDTO{
		ID:            q.ID,
		Site:          q.Site,
		User:          q.User,
		UserName:      q.UserName,
		AvatarPath:    avatar,
		List:          q.List,
		ListLabel:     q.ListLabel,
		Kind:          q.Kind,
		Origin:        q.Origin,
		Status:        q.Status,
		StatusText:    q.StatusText,
		PauseReason:   q.PauseReason,
		StopReason:    q.StopReason,
		Note:          q.Note,
		CachedCount:   q.CachedCount,
		SkippedCount:  q.SkippedCount,
		ActiveItemKey: q.ActiveItemKey,
		ActiveTitle:   q.ActiveTitle,
		RecentItems:   recent,
		NextRunAt:     q.NextRunAt,
		CreatedAt:     q.CreatedAt,
		UpdatedAt:     q.UpdatedAt,
	}
}

func (s *Server) mapLiveRecording(r service.LiveAccountState) companionapi.LiveRecordingDTO {
	avatar := ""
	if s.backend.AccountAvatarPath(r.Site, r.UID) != "" {
		avatar = companionapi.AvatarPath(r.Site, r.UID)
	}
	return companionapi.LiveRecordingDTO{
		RecordingKey: r.RecordingKey,
		AccountKey:   r.AccountKey,
		Site:         r.Site,
		UID:          r.UID,
		RoomID:       r.RoomID,
		Uname:        r.Uname,
		AvatarPath:   avatar,
		Title:        r.Title,
		StatusText:   r.StatusText,
		QualityName:  r.QualityName,
		Codec:        r.Codec,
		Recording:    r.Recording,
		DurationSec:  r.RecordDurationSec,
		SpeedBps:     r.RecordSpeedBps,
		Bytes:        r.RecordBytes,
		Segments:     r.RecordSegments,
		Incidents:    mapLiveIncidents(r.Incidents),
		StopReason:   r.StopReason,
		LastError:    r.LastError,
		UpdatedAt:    r.UpdatedAt,
	}
}

// ---- Sanitized mapping helpers (never expose ItemDir, cookies, or signed URLs) ----

func buildLibrarySummary(meta library.ItemMetadata, subByAccount map[string]subscription.Subscription) companionapi.LibraryItemSummary {
	authorSite, authorUID := library.NormalizeAuthorUID(meta.Site, meta.Author.UID, meta.Author.Mid)
	sum := companionapi.LibraryItemSummary{
		ItemKey:        meta.ItemKey,
		Site:           meta.Site,
		Kind:           meta.Kind,
		Status:         meta.Status,
		Title:          firstNonEmpty(meta.Title, meta.Text),
		AuthorUID:      authorUID,
		AuthorName:     meta.Author.Name,
		PubTs:          meta.PubTs,
		Duration:       meta.Duration,
		CoverRelPath:   findCoverRelPath(meta),
		PartCount:      len(meta.Parts),
		QualitySummary: buildQualitySummary(meta),
		IsForward:      meta.IsForwardContent(),
		TotalBytes:     meta.TotalBytes,
	}
	for _, m := range meta.Media {
		if m.Role == "image" {
			sum.ImageCount++
		}
	}
	if v := findPrimaryVideoRelPath(meta); v != "" {
		sum.PrimaryRelPath = v
		if meta.Kind == "live" {
			sum.PrimaryKind = "live"
		} else {
			sum.PrimaryKind = "video"
		}
	} else if sum.CoverRelPath != "" {
		sum.PrimaryRelPath = sum.CoverRelPath
		sum.PrimaryKind = "image"
	}
	lookupSite, lookupUID := authorSite, authorUID
	if meta.Via != nil && meta.Via.UID != "" {
		lookupSite = strings.ToLower(firstNonEmpty(meta.Via.Site, meta.Site))
		lookupUID = meta.Via.UID
	}
	if sub, ok := subByAccount[lookupSite+":"+lookupUID]; ok {
		sum.SubID = sub.ID
		sum.SubName = sub.Name
	}
	return sum
}

func findCoverRelPath(meta library.ItemMetadata) string {
	for _, m := range meta.Media {
		if m.Role == "cover" && m.Path != "" {
			return m.Path
		}
	}
	for _, p := range meta.NotePages {
		if p.Image != "" {
			return p.Image
		}
		if p.Cover != "" {
			return p.Cover
		}
	}
	for _, m := range meta.Media {
		if m.Role == "image" && m.Path != "" {
			return m.Path
		}
	}
	return ""
}

func findPrimaryVideoRelPath(meta library.ItemMetadata) string {
	if len(meta.Parts) > 0 && meta.Parts[0].File != "" {
		return meta.Parts[0].File
	}
	if meta.DouyinVideo != nil && meta.DouyinVideo.File != "" {
		return meta.DouyinVideo.File
	}
	if meta.Live != nil && len(meta.Live.Segments) > 0 && meta.Live.Segments[0].File != "" {
		return meta.Live.Segments[0].File
	}
	for _, m := range meta.Media {
		if (m.Role == "video" || m.Role == "live_segment") && m.Path != "" {
			return m.Path
		}
	}
	return ""
}

func buildQualitySummary(meta library.ItemMetadata) string {
	if len(meta.Parts) > 0 {
		p := meta.Parts[0]
		parts := nonEmptyParts(p.Video.QualityName, p.Video.Codecs, p.Audio.Name)
		if len(parts) > 0 {
			return strings.Join(parts, " · ")
		}
	}
	if meta.DouyinVideo != nil {
		parts := nonEmptyParts(meta.DouyinVideo.QualityName, meta.DouyinVideo.Codec)
		if len(parts) > 0 {
			return strings.Join(parts, " · ")
		}
	}
	if meta.Live != nil {
		parts := nonEmptyParts(meta.Live.QualityName, meta.Live.Codec)
		if len(parts) > 0 {
			return strings.Join(parts, " · ")
		}
	}
	return ""
}

func extractFeedImages(meta library.ItemMetadata) []companionapi.FeedImage {
	if len(meta.NotePages) > 0 {
		out := make([]companionapi.FeedImage, 0, len(meta.NotePages))
		for _, p := range meta.NotePages {
			if p.Image == "" {
				continue
			}
			out = append(out, companionapi.FeedImage{
				Path:        p.Image,
				NoWatermark: p.NoWatermark,
				Motion:      p.Motion,
				GIF:         p.GIF,
				FromOrigin:  p.FromOrigin,
				Width:       p.Width,
				Height:      p.Height,
			})
		}
		if len(out) > 0 {
			return out
		}
	}
	var out []companionapi.FeedImage
	for _, m := range meta.Media {
		if m.Role != "image" || m.Path == "" {
			continue
		}
		out = append(out, companionapi.FeedImage{
			Path:       m.Path,
			Motion:     m.Motion,
			GIF:        m.GIF,
			FromOrigin: m.FromOrigin,
			Width:      m.Width,
			Height:     m.Height,
		})
	}
	return out
}

func mapMediaFiles(in []library.MediaFile) []companionapi.MediaFileDTO {
	if len(in) == 0 {
		return nil
	}
	out := make([]companionapi.MediaFileDTO, 0, len(in))
	for _, m := range in {
		out = append(out, companionapi.MediaFileDTO{
			Role:       m.Role,
			Path:       m.Path,
			GIF:        m.GIF,
			FromOrigin: m.FromOrigin,
			Bytes:      m.Bytes,
			Width:      m.Width,
			Height:     m.Height,
			Duration:   m.Duration,
			Motion:     m.Motion,
		})
	}
	return out
}

func mapVideoParts(in []library.VideoPartRecord) []companionapi.VideoPartDTO {
	if len(in) == 0 {
		return nil
	}
	out := make([]companionapi.VideoPartDTO, 0, len(in))
	for _, p := range in {
		out = append(out, companionapi.VideoPartDTO{
			Page:        p.Page,
			Title:       p.Title,
			Duration:    p.Duration,
			File:        p.File,
			QualityName: p.Video.QualityName,
			Codecs:      p.Video.Codecs,
			Width:       p.Video.Width,
			Height:      p.Video.Height,
			AudioName:   p.Audio.Name,
			AudioFile:   p.Audio.SavedAs,
		})
	}
	return out
}

func mapDouyinVideoInfo(in *library.DouyinVideoInfo) *companionapi.VideoInfoDTO {
	if in == nil {
		return nil
	}
	return &companionapi.VideoInfoDTO{
		File:        in.File,
		QualityName: in.QualityName,
		Codec:       in.Codec,
		Width:       in.Width,
		Height:      in.Height,
		FPS:         in.FPS,
		Bitrate:     in.Bitrate,
	}
}

func mapNotePages(in []library.NotePageRecord) []companionapi.NotePageDTO {
	if len(in) == 0 {
		return nil
	}
	out := make([]companionapi.NotePageDTO, 0, len(in))
	for _, p := range in {
		out = append(out, companionapi.NotePageDTO{
			Index:       p.Index,
			Kind:        p.Kind,
			GIF:         p.GIF,
			FromOrigin:  p.FromOrigin,
			Image:       p.Image,
			NoWatermark: p.NoWatermark,
			Cover:       p.Cover,
			Motion:      p.Motion,
			Video:       p.Video,
			Width:       p.Width,
			Height:      p.Height,
			Duration:    p.Duration,
			VideoInfo:   mapDouyinVideoInfo(p.VideoInfo),
		})
	}
	return out
}

func mapMusic(in *library.MusicInfo) *companionapi.MusicDTO {
	if in == nil {
		return nil
	}
	return &companionapi.MusicDTO{
		Title:    in.Title,
		Author:   in.Author,
		Duration: in.Duration,
		File:     in.File,
		Cover:    in.Cover,
		Original: in.Original,
	}
}

func mapLiveInfo(in *library.LiveRecordingInfo) *companionapi.LiveInfoDTO {
	if in == nil {
		return nil
	}
	roomID := in.RoomIDStr
	if roomID == "" && in.RoomID > 0 {
		roomID = strconv.FormatInt(in.RoomID, 10)
	}
	segs := make([]companionapi.LiveSegmentDTO, 0, len(in.Segments))
	for _, sg := range in.Segments {
		segs = append(segs, companionapi.LiveSegmentDTO{
			File:        sg.File,
			Bytes:       sg.Bytes,
			DurationSec: sg.DurationSec,
			Codec:       sg.Codec,
			Width:       sg.Width,
			Height:      sg.Height,
			QualityName: sg.QualityName,
		})
	}
	return &companionapi.LiveInfoDTO{
		RoomID:      roomID,
		WebRID:      in.WebRID,
		LiveSince:   in.LiveSince,
		StartedAt:   in.StartedAt,
		EndedAt:     in.EndedAt,
		QualityName: in.QualityName,
		Codec:       in.Codec,
		Segments:    segs,
		Incidents:   mapLiveIncidents(in.Incidents),
	}
}

func mapLiveIncidents(in []library.LiveIncident) []companionapi.LiveIncident {
	if len(in) == 0 {
		return nil
	}
	out := make([]companionapi.LiveIncident, 0, len(in))
	for _, inc := range in {
		out = append(out, companionapi.LiveIncident{
			Kind:         inc.Kind,
			At:           inc.At,
			EndedAt:      inc.EndedAt,
			DurationSec:  inc.DurationSec,
			LostSegments: inc.LostSegments,
			Count:        inc.Count,
			Part:         inc.Part,
			Reason:       inc.Reason,
		})
	}
	return out
}

func mapMissing(in []library.MissingRecord) []companionapi.MissingItemDTO {
	if len(in) == 0 {
		return nil
	}
	out := make([]companionapi.MissingItemDTO, 0, len(in))
	for _, m := range in {
		out = append(out, companionapi.MissingItemDTO{Item: m.Item, Reason: m.Reason})
	}
	return out
}

func mapVia(in *library.ItemVia) *companionapi.ItemViaDTO {
	if in == nil {
		return nil
	}
	return &companionapi.ItemViaDTO{
		Reason: in.Reason,
		Site:   in.Site,
		UID:    in.UID,
		Name:   in.Name,
	}
}

func mapRichCard(in *library.ItemRichCard) *companionapi.ItemRichCardDTO {
	if in == nil {
		return nil
	}
	return &companionapi.ItemRichCardDTO{
		Kind:  in.Kind,
		Label: in.Label,
		Title: in.Title,
		Desc:  in.Desc,
		URL:   in.URL,
	}
}

func mapLinks(in []library.ItemLink) []companionapi.TextLink {
	if len(in) == 0 {
		return nil
	}
	out := make([]companionapi.TextLink, 0, len(in))
	for _, l := range in {
		out = append(out, companionapi.TextLink{Label: l.Label, URL: l.URL})
	}
	return out
}

func mapReplyTarget(in *library.ItemReplyTarget) *companionapi.ItemReplyTargetDTO {
	if in == nil {
		return nil
	}
	if in.StatusID == "" && in.UserID == "" && in.ScreenName == "" {
		return nil
	}
	return &companionapi.ItemReplyTargetDTO{
		StatusID:   in.StatusID,
		UserID:     in.UserID,
		ScreenName: in.ScreenName,
	}
}

func mapThread(in []library.ItemThreadPart) []companionapi.ThreadPartDTO {
	if len(in) == 0 {
		return nil
	}
	out := make([]companionapi.ThreadPartDTO, 0, len(in))
	for _, p := range in {
		out = append(out, companionapi.ThreadPartDTO{
			ID:           p.ID,
			PubTs:        p.PubTs,
			Text:         p.Text,
			Links:        mapLinks(p.Links),
			MediaIndexes: append([]int(nil), p.MediaIndexes...),
			Origin:       mapOrigin(p.Origin),
		})
	}
	return out
}

func mapOrigin(in *library.ItemOrigin) *companionapi.ItemOriginDTO {
	return mapOriginDepth(in, 0)
}

func mapOriginDepth(in *library.ItemOrigin, depth int) *companionapi.ItemOriginDTO {
	if in == nil {
		return nil
	}
	_, uid := library.NormalizeAuthorUID(in.Site, in.Author.UID, in.Author.Mid)
	var nested *companionapi.ItemOriginDTO
	if depth < 1 && in.Origin != nil {
		nested = mapOriginDepth(in.Origin, depth+1)
	}
	return &companionapi.ItemOriginDTO{
		Site:              in.Site,
		ID:                in.ID,
		CanonicalURL:      in.CanonicalURL,
		AuthorUID:         uid,
		AuthorUniqueID:    in.Author.UniqueID,
		AuthorName:        in.Author.Name,
		PubTs:             in.PubTs,
		Title:             in.Title,
		Text:              in.Text,
		Links:             mapLinks(in.Links),
		Card:              mapRichCard(in.Card),
		InReplyTo:         mapReplyTarget(in.InReplyTo),
		Origin:            nested,
		Unavailable:       in.Unavailable,
		UnavailableReason: in.UnavailableReason,
		VideoFromOrigin:   in.VideoFromOrigin,
	}
}

func trackingPresetLabel(tags []string) string {
	has := make(map[string]bool, len(tags))
	for _, t := range tags {
		if t != "auto_record" {
			has[t] = true
		}
	}
	switch {
	case len(has) == 0:
		return "仅手动"
	case len(has) == 1 && has["scheduled_check"]:
		return "只看新动态"
	case len(has) == 3 && has["scheduled_check"] && has["cache_video"] && has["cache_draw"]:
		return "追更并保存"
	case len(has) == 6 && has["scheduled_check"] && has["cache_video"] && has["cache_draw"] && has["cache_word"] && has["cache_forward"] && has["full_cache"]:
		return "完整存档"
	default:
		return "自定义"
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string, fill func(*companionapi.ErrorBody)) {
	var body companionapi.ErrorBody
	body.Error.Code = code
	body.Error.Message = msg
	if fill != nil {
		fill(&body)
	}
	writeJSON(w, status, body)
}

func writeServiceError(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}
	var se *site.Error
	siteID := ""
	msg := err.Error()
	if errors.As(err, &se) && se != nil {
		siteID = string(se.Site)
		if strings.TrimSpace(se.Message) != "" {
			msg = se.Message
		}
	}
	if site.IsUnsupported(err) {
		writeError(w, http.StatusBadRequest, companionapi.CodeUnsupported, msg, func(b *companionapi.ErrorBody) {
			b.Error.Site = siteID
		})
		return
	}
	switch site.KindOf(err) {
	case site.ErrKindAuthRequired:
		writeError(w, http.StatusPreconditionFailed, companionapi.CodeAuthRequired, msg, func(b *companionapi.ErrorBody) {
			b.Error.Site = siteID
		})
	case site.ErrKindRiskControl:
		writeError(w, http.StatusServiceUnavailable, companionapi.CodeRiskControl, msg, func(b *companionapi.ErrorBody) {
			b.Error.Site = siteID
		})
	case site.ErrKindRateLimited:
		writeError(w, http.StatusTooManyRequests, companionapi.CodeRateLimited, msg, func(b *companionapi.ErrorBody) {
			b.Error.Site = siteID
		})
	case site.ErrKindConflict:
		writeError(w, http.StatusConflict, companionapi.CodeConflict, msg, func(b *companionapi.ErrorBody) {
			b.Error.Site = siteID
		})
	case site.ErrKindUnsupportedLink, site.ErrKindInvalidInput, site.ErrKindBadRequest:
		writeError(w, http.StatusBadRequest, companionapi.CodeUnsupported, msg, func(b *companionapi.ErrorBody) {
			b.Error.Site = siteID
		})
	case site.ErrKindUnavailable:
		writeError(w, http.StatusNotFound, companionapi.CodeUnavailable, msg, func(b *companionapi.ErrorBody) {
			b.Error.Site = siteID
		})
	default:
		// Never leak raw local paths or unexpected internal details.
		writeError(w, http.StatusInternalServerError, companionapi.CodeInternal, "操作失败，请在电脑端查看日志", nil)
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func nonEmptyParts(vals ...string) []string {
	var out []string
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			out = append(out, s)
		}
	}
	return out
}
