package remoteserver

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/remote"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/upload"
)

// Service is what the API needs from the backend (implemented by
// *service.Backend in remote mode).
type Service interface {
	ServerStatus() remote.Status
	ServerSubscriptions() remote.SubscriptionsInfo
	ApplyRemoteSnapshot(raw []byte) (remote.SubscriptionsInfo, error)
	AcceptPushedCredential(ctx context.Context, siteID, cookie string) (remote.SiteStatus, error)
	ClearPushedCredential(siteID string) error
}

// Uploader is the upload part of the API.
type Uploader interface {
	Status() remote.UploadStatus
	RetryNow()
}

// Disk reports the export folder's free and total bytes.
type Disk func() (free, total uint64, err error)

// Space thresholds of the export folder (remote.md §6.2).
const (
	DiskWarnBytes  = 5 << 30
	DiskPauseBytes = 1 << 30
)

// Failure limiting: 5 failures from one address within a minute block it
// for 10 minutes.
const (
	failWindow    = time.Minute
	failLimit     = 5
	blockFor      = 10 * time.Minute
	maxTrackedIPs = 10000
	pruneInterval = time.Minute
)

// Server is the HTTPS API.
type Server struct {
	dataDir string
	svc     Service
	up      Uploader
	disk    Disk
	now     func() time.Time
	logf    func(format string, args ...any)

	mu        sync.Mutex
	tokenHash string
	cfgInfo   os.FileInfo
	failures  map[string][]time.Time
	blocked   map[string]time.Time
	lastPrune time.Time
	requests  int
}

// Options configure a Server.
type Options struct {
	DataDir   string
	TokenHash string
	Service   Service
	Uploader  Uploader
	Disk      Disk
	Now       func() time.Time
	Logf      func(format string, args ...any)
}

// New builds the API.
func New(opt Options) *Server {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Logf == nil {
		opt.Logf = func(string, ...any) {}
	}
	return &Server{dataDir: opt.DataDir, svc: opt.Service, up: opt.Uploader, disk: opt.Disk, now: opt.Now, logf: opt.Logf,
		tokenHash: opt.TokenHash, failures: map[string][]time.Time{}, blocked: map[string]time.Time{}}
}

// Requests counts the HTTP requests that reached the handler (tests).
func (s *Server) Requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

// TLSConfig loads the certificate of dataDir (TLS 1.3 only).
func TLSConfig(dataDir string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(filepath.Join(dataDir, TLSDir, CertFile), filepath.Join(dataDir, TLSDir, KeyFile))
	if err != nil {
		return nil, err
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}}, nil
}

// HTTPServer wraps the handler with the timeouts of the API.
func (s *Server) HTTPServer(tlsCfg *tls.Config) *http.Server {
	return &http.Server{
		Handler:           s,
		TLSConfig:         tlsCfg,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		// Handshake failures (a client pinned to another certificate, scans)
		// go to the run log, not to standard error unformatted.
		ErrorLog: log.New(logWriter{s.logf}, "", 0),
	}
}

// Serve accepts TLS connections on ln until ctx ends.
func (s *Server) Serve(ctx context.Context, ln net.Listener, tlsCfg *tls.Config) error {
	hs := s.HTTPServer(tlsCfg)
	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(tls.NewListener(ln, tlsCfg)) }()
	select {
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := hs.Shutdown(sctx); err != nil {
			_ = hs.Close()
		}
		<-errc
		return nil
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func clientIP(r *http.Request) string {
	// X-Forwarded-For is never trusted: the API is reached directly.
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, errCode, msg string, fill func(*remote.ErrorBody)) {
	var body remote.ErrorBody
	body.Error.Code, body.Error.Message = errCode, msg
	if fill != nil {
		fill(&body)
	}
	writeJSON(w, code, body)
}

// blockedLocked reports whether ip is blocked now and the remaining seconds.
func (s *Server) blockedLocked(ip string, now time.Time) (bool, int) {
	until, ok := s.blocked[ip]
	if !ok {
		return false, 0
	}
	if now.Before(until) {
		rem := int(math.Ceil(until.Sub(now).Seconds()))
		if rem < 1 {
			rem = 1
		}
		return true, rem
	}
	delete(s.blocked, ip)
	delete(s.failures, ip)
	return false, 0
}

func (s *Server) pruneLimitersLocked(now time.Time, force bool) {
	if !force && !s.lastPrune.IsZero() && now.Sub(s.lastPrune) < pruneInterval {
		return
	}
	s.lastPrune = now
	for k, until := range s.blocked {
		if !now.Before(until) {
			delete(s.blocked, k)
		}
	}
	for k, times := range s.failures {
		kept := times[:0]
		for _, t := range times {
			if now.Sub(t) < failWindow {
				kept = append(kept, t)
			}
		}
		if len(kept) == 0 {
			delete(s.failures, k)
		} else {
			s.failures[k] = kept
		}
	}
}

func (s *Server) evictOldestLimiterLocked() {
	// Prefer evicting an unblocked IP with the oldest failure timestamp.
	var victim string
	var oldest time.Time
	for k, times := range s.failures {
		if _, isBlocked := s.blocked[k]; isBlocked {
			continue
		}
		last := times[len(times)-1]
		if victim == "" || last.Before(oldest) {
			victim, oldest = k, last
		}
	}
	if victim != "" {
		delete(s.failures, victim)
		return
	}
	for k, until := range s.blocked {
		if victim == "" || until.Before(oldest) {
			victim, oldest = k, until
		}
	}
	if victim != "" {
		delete(s.blocked, victim)
		delete(s.failures, victim)
	}
}

func (s *Server) recordFailure(ip string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLimitersLocked(now, len(s.failures) >= maxTrackedIPs || len(s.blocked) >= maxTrackedIPs)
	if _, exists := s.failures[ip]; !exists {
		for len(s.failures) >= maxTrackedIPs || len(s.blocked) >= maxTrackedIPs {
			s.evictOldestLimiterLocked()
		}
	}
	var kept []time.Time
	for _, t := range s.failures[ip] {
		if now.Sub(t) < failWindow {
			kept = append(kept, t)
		}
	}
	kept = append(kept, now)
	s.failures[ip] = kept
	if len(kept) >= failLimit {
		s.blocked[ip] = now.Add(blockFor)
	}
}

func (s *Server) reloadTokenHash(force bool) string {
	s.mu.Lock()
	hash := s.tokenHash
	prevInfo := s.cfgInfo
	s.mu.Unlock()
	if s.dataDir == "" {
		return hash
	}
	cfgPath := filepath.Join(s.dataDir, ConfigFile)
	st, err := os.Stat(cfgPath)
	if err != nil {
		return hash
	}
	unchanged := prevInfo != nil && os.SameFile(prevInfo, st) &&
		st.ModTime().Equal(prevInfo.ModTime()) && st.Size() == prevInfo.Size()
	if unchanged && !force {
		return hash
	}
	if cfg, err := LoadConfig(s.dataDir); err == nil {
		hash = cfg.TokenHash
		s.mu.Lock()
		s.tokenHash = hash
		s.cfgInfo = st
		s.mu.Unlock()
	}
	return hash
}

// tokenOK compares in constant time against the stored hash; server.json is
// stat-checked so `trace-server token` takes effect at once without re-reading
// and parsing the config on every request.
func (s *Server) tokenOK(token string) bool {
	if token == "" {
		return false
	}
	got := HashToken(token)
	hash := s.reloadTokenHash(false)
	if subtle.ConstantTimeCompare([]byte(got), []byte(hash)) == 1 {
		return true
	}
	// On mismatch, re-read once in case server.json was updated in-place within
	// the same filesystem mtime tick.
	if s.dataDir != "" {
		fresh := s.reloadTokenHash(true)
		if fresh != hash && subtle.ConstantTimeCompare([]byte(got), []byte(fresh)) == 1 {
			return true
		}
	}
	return false
}

// ServeHTTP authenticates, checks the protocol and routes.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	ip := clientIP(r)
	s.mu.Lock()
	s.requests++
	blocked, retryAfter := s.blockedLocked(ip, now)
	s.mu.Unlock()
	version := appdirs.BuildInfo().Version
	w.Header().Set(remote.HeaderProtocol, remote.ProtocolVersion)
	w.Header().Set(remote.HeaderVersion, version)
	if blocked {
		writeError(w, http.StatusTooManyRequests, remote.CodeRateLimited, "失败次数过多，请稍后再试", func(b *remote.ErrorBody) { b.Error.RetryAfterSecs = retryAfter })
		return
	}
	if !strings.HasPrefix(r.URL.Path, remote.APIPrefix) {
		s.recordFailure(ip, now)
		writeError(w, http.StatusNotFound, remote.CodeNotFound, "没有这个地址", nil)
		return
	}
	auth := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok || !s.tokenOK(strings.TrimSpace(token)) {
		s.recordFailure(ip, now)
		writeError(w, http.StatusUnauthorized, remote.CodeUnauthorized, "访问令牌不正确", nil)
		return
	}
	if p := r.Header.Get(remote.HeaderProtocol); p != remote.ProtocolVersion {
		cv := r.Header.Get(remote.HeaderVersion)
		writeError(w, http.StatusConflict, remote.CodeProtocol, "两边的协议版本不同", func(b *remote.ErrorBody) {
			b.Error.ServerVersion, b.Error.ServerProtocol = version, remote.ProtocolVersion
			b.Error.ClientVersion, b.Error.ClientProtocol = cv, p
		})
		return
	}
	var body []byte
	if r.Body != nil {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, remote.MaxBodyBytes))
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				writeError(w, http.StatusRequestEntityTooLarge, remote.CodeTooLarge, "请求内容超过 1 MiB", nil)
				return
			}
			writeError(w, http.StatusBadRequest, remote.CodeBadRequest, "请求内容读取失败", nil)
			return
		}
		body = raw
	}
	s.route(w, r, body)
}

func (s *Server) route(w http.ResponseWriter, r *http.Request, body []byte) {
	path := strings.TrimPrefix(r.URL.Path, remote.APIPrefix)
	switch {
	case path == "status" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, s.Status())
	case path == "subscriptions" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, s.svc.ServerSubscriptions())
	case path == "subscriptions" && r.Method == http.MethodPut:
		info, err := s.svc.ApplyRemoteSnapshot(body)
		if err != nil {
			writeError(w, http.StatusBadRequest, remote.CodeBadRequest, "订阅同步失败："+err.Error(), nil)
			return
		}
		writeJSON(w, http.StatusOK, info)
	case strings.HasPrefix(path, "credentials/") && (r.Method == http.MethodPut || r.Method == http.MethodDelete):
		siteID := strings.ToLower(strings.TrimPrefix(path, "credentials/"))
		if siteID != string(site.Bilibili) && siteID != string(site.Douyin) {
			writeError(w, http.StatusNotFound, remote.CodeNotFound, "不支持这个站点", nil)
			return
		}
		if r.Method == http.MethodDelete {
			if err := s.svc.ClearPushedCredential(siteID); err != nil {
				writeError(w, http.StatusInternalServerError, remote.CodeInternal, "清除登录失败", nil)
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"site": siteID, "state": remote.SiteNone})
			return
		}
		var push remote.CredentialPush
		if err := json.Unmarshal(body, &push); err != nil || strings.TrimSpace(push.Cookie) == "" {
			writeError(w, http.StatusBadRequest, remote.CodeBadRequest, "请求里没有 Cookie", nil)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		st, err := s.svc.AcceptPushedCredential(ctx, siteID, push.Cookie)
		if err != nil {
			// The site's reason, never the Cookie.
			writeError(w, http.StatusUnprocessableEntity, remote.CodeCredentialRejected, credentialReason(err), nil)
			return
		}
		writeJSON(w, http.StatusOK, st)
	case path == "upload/retry" && r.Method == http.MethodPost:
		if s.up != nil {
			s.up.RetryNow()
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		writeError(w, http.StatusNotFound, remote.CodeNotFound, "没有这个接口", nil)
	}
}

func credentialReason(err error) string {
	var se *site.Error
	if errors.As(err, &se) && strings.TrimSpace(se.Message) != "" {
		return se.Message
	}
	switch site.KindOf(err) {
	case site.ErrKindAuthRequired:
		return "站点确认这个 Cookie 未登录或已失效"
	case site.ErrKindRiskControl, site.ErrKindRateLimited:
		return "站点暂时限制了远程的请求，请稍后再同步"
	case site.ErrKindNetwork:
		return "远程连不上站点，请检查远程的网络"
	}
	return "站点校验没有通过"
}

// Status is the full status: the backend's, the upload and the disk.
func (s *Server) Status() remote.Status {
	st := s.svc.ServerStatus()
	if s.up != nil {
		st.Upload = s.up.Status()
	} else {
		st.Upload = remote.UploadStatus{Mode: remote.UploadNone}
	}
	if s.disk != nil {
		if free, total, err := s.disk(); err == nil {
			st.Disk = remote.DiskStatus{Free: free, Total: total, Warning: free < DiskWarnBytes, Paused: free < DiskPauseBytes}
		} else {
			s.logf("disk space check failed: %v", err)
			st.Disk = remote.DiskStatus{Warning: true, Paused: true}
		}
	}
	return st
}

// CacheGate is the backend's gate for new cache tasks: closed while the
// export folder has less than 1 GiB free or cannot be queried.
func CacheGate(disk Disk) func() error {
	return func() error {
		if disk == nil {
			return nil
		}
		free, _, err := disk()
		if err != nil {
			return errors.New("无法读取导出目录剩余空间，暂停新的缓存任务")
		}
		if free < DiskPauseBytes {
			return errors.New("导出目录剩余空间不足 1 GiB，暂停新的缓存任务")
		}
		return nil
	}
}

var _ Uploader = (*upload.Uploader)(nil)

// logWriter hands net/http's error lines to the run log.
type logWriter struct {
	logf func(format string, args ...any)
}

func (w logWriter) Write(p []byte) (int, error) {
	w.logf("https: %s", strings.TrimSpace(string(p)))
	return len(p), nil
}
