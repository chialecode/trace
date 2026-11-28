package companionclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/companionapi"
)

const (
	ticketKindMedia          = "media"
	ticketKindLivePreview    = "live_preview"
	ticketKindLivePreviewHLS = "live_preview_hls"
)

type mediaTicket struct {
	kind         string
	key          string
	relPath      string
	recordingKey string
}

// MediaProxy is a loopback-only HTTP server (127.0.0.1:<random port>) that
// forwards system video player requests over pinned-fingerprint TLS + Bearer
// token to the desktop companion server (design §6, FR-MOB-030).
type MediaProxy struct {
	mu        sync.Mutex
	srv       *http.Server
	ln        net.Listener
	addr      string
	client    *http.Client
	target    func() (baseURL, token, version, channel string, ok bool)
	tickets   map[string]mediaTicket
	nextReqID uint64
	active    map[string]map[uint64]context.CancelFunc
}

// NewMediaProxy starts an idle loopback HTTP proxy on 127.0.0.1:0.
// target provides the active desktop baseURL ("https://host:port"), bearer token,
// client version, and channel. httpClient should be a pinned-fingerprint client.
func NewMediaProxy(httpClient *http.Client, target func() (baseURL, token, version, channel string, ok bool)) (*MediaProxy, error) {
	if httpClient == nil || target == nil {
		return nil, errors.New("companionclient: media proxy requires httpClient and target")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &MediaProxy{
		ln:      ln,
		addr:    ln.Addr().String(),
		client:  httpClient,
		target:  target,
		tickets: make(map[string]mediaTicket),
		active:  make(map[string]map[uint64]context.CancelFunc),
	}
	p.srv = &http.Server{
		Handler:           http.HandlerFunc(p.serveHTTP),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		_ = p.srv.Serve(ln)
	}()
	return p, nil
}

// IssueTicket mints a 128-bit random in-memory ticket bound to a single (key, relPath)
// and returns the loopback URL and ticket string.
func (p *MediaProxy) IssueTicket(key, relPath string) (loopbackURL string, ticket string, err error) {
	key = strings.TrimSpace(key)
	relPath = strings.TrimPrefix(strings.TrimSpace(relPath), "/")
	if key == "" || relPath == "" {
		return "", "", errors.New("companionclient: media key and relPath are required")
	}
	t, err := RandomBase64URL128()
	if err != nil {
		return "", "", err
	}
	p.mu.Lock()
	if p.tickets == nil {
		p.mu.Unlock()
		return "", "", errors.New("companionclient: media proxy is closed")
	}
	p.tickets[t] = mediaTicket{kind: ticketKindMedia, key: key, relPath: relPath}
	addr := p.addr
	p.mu.Unlock()

	escapedPath := escapeMediaPath(key, relPath)
	return fmt.Sprintf("http://%s/media/%s?t=%s", addr, escapedPath, url.QueryEscape(t)), t, nil
}

// IssueLivePreviewTicket mints a 128-bit random in-memory ticket bound to one active
// live recording's .part preview endpoint (/api/v1/live/recordings/{id}/preview).
func (p *MediaProxy) IssueLivePreviewTicket(recordingKey string) (loopbackURL string, ticket string, err error) {
	recKey := strings.TrimSpace(recordingKey)
	if recKey == "" {
		return "", "", errors.New("companionclient: recordingKey is required")
	}
	t, err := RandomBase64URL128()
	if err != nil {
		return "", "", err
	}
	p.mu.Lock()
	if p.tickets == nil {
		p.mu.Unlock()
		return "", "", errors.New("companionclient: media proxy is closed")
	}
	p.tickets[t] = mediaTicket{kind: ticketKindLivePreview, recordingKey: recKey}
	addr := p.addr
	p.mu.Unlock()

	return fmt.Sprintf("http://%s/live-preview/%s/preview?t=%s", addr, url.PathEscape(recKey), url.QueryEscape(t)), t, nil
}

// IssueLivePreviewHLSTicket mints a 128-bit random in-memory ticket bound to one active
// live recording's HLS preview playlist (/api/v1/live/recordings/{id}/preview.m3u8) and
// its corresponding byte-range preview segments under the same ticket.
func (p *MediaProxy) IssueLivePreviewHLSTicket(recordingKey string) (playlistURL string, ticket string, err error) {
	recKey := strings.TrimSpace(recordingKey)
	if recKey == "" {
		return "", "", errors.New("companionclient: recordingKey is required")
	}
	t, err := RandomBase64URL128()
	if err != nil {
		return "", "", err
	}
	p.mu.Lock()
	if p.tickets == nil {
		p.mu.Unlock()
		return "", "", errors.New("companionclient: media proxy is closed")
	}
	p.tickets[t] = mediaTicket{kind: ticketKindLivePreviewHLS, recordingKey: recKey}
	addr := p.addr
	p.mu.Unlock()

	return fmt.Sprintf("http://%s/live-preview/%s/preview.m3u8?t=%s", addr, url.PathEscape(recKey), url.QueryEscape(t)), t, nil
}

// RevokeTicket invalidates a previously issued ticket and cancels any in-flight
// upstream requests using that ticket immediately.
func (p *MediaProxy) RevokeTicket(ticket string) {
	if ticket == "" {
		return
	}
	var cancels []context.CancelFunc
	p.mu.Lock()
	delete(p.tickets, ticket)
	if reqs := p.active[ticket]; reqs != nil {
		for _, fn := range reqs {
			cancels = append(cancels, fn)
		}
		delete(p.active, ticket)
	}
	p.mu.Unlock()
	for _, fn := range cancels {
		fn()
	}
}

// RevokeAll invalidates every active playback ticket and cancels all in-flight
// upstream streams immediately (called on disconnect/background).
func (p *MediaProxy) RevokeAll() {
	var cancels []context.CancelFunc
	p.mu.Lock()
	if p.tickets != nil {
		clear(p.tickets)
	}
	for t, reqs := range p.active {
		for _, fn := range reqs {
			cancels = append(cancels, fn)
		}
		delete(p.active, t)
	}
	p.mu.Unlock()
	for _, fn := range cancels {
		fn()
	}
}

// Close revokes all tickets, cancels active upstream streams, and shuts down the loopback listener.
func (p *MediaProxy) Close() error {
	var cancels []context.CancelFunc
	p.mu.Lock()
	if p.tickets != nil {
		clear(p.tickets)
		p.tickets = nil
	}
	for t, reqs := range p.active {
		for _, fn := range reqs {
			cancels = append(cancels, fn)
		}
		delete(p.active, t)
	}
	srv := p.srv
	p.mu.Unlock()
	for _, fn := range cancels {
		fn()
	}
	if srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
}

func (p *MediaProxy) registerActive(ticket string, parent context.Context) (context.Context, uint64, func(), bool) {
	ctx, cancel := context.WithCancel(parent)
	p.mu.Lock()
	if _, ok := p.tickets[ticket]; !ok {
		p.mu.Unlock()
		cancel()
		return nil, 0, nil, false
	}
	p.nextReqID++
	reqID := p.nextReqID
	if p.active[ticket] == nil {
		p.active[ticket] = make(map[uint64]context.CancelFunc)
	}
	p.active[ticket][reqID] = cancel
	p.mu.Unlock()

	cleanup := func() {
		p.mu.Lock()
		if reqs := p.active[ticket]; reqs != nil {
			delete(reqs, reqID)
			if len(reqs) == 0 {
				delete(p.active, ticket)
			}
		}
		p.mu.Unlock()
		cancel()
	}
	return ctx, reqID, cleanup, true
}

func (p *MediaProxy) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ticket := r.URL.Query().Get("t")
	if ticket == "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	p.mu.Lock()
	mt, ok := p.tickets[ticket]
	p.mu.Unlock()
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	var upstreamPath string
	rewriteHLS := false

	switch mt.kind {
	case ticketKindMedia, "":
		expectedPath := "/media/" + escapeMediaPath(mt.key, mt.relPath)
		if r.URL.EscapedPath() != expectedPath && r.URL.Path != "/media/"+mt.key+"/"+mt.relPath {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		upstreamPath = companionapi.MediaPath(mt.key, mt.relPath)

	case ticketKindLivePreview:
		expectedEsc := "/live-preview/" + url.PathEscape(mt.recordingKey) + "/preview"
		expectedRaw := "/live-preview/" + mt.recordingKey + "/preview"
		if r.URL.EscapedPath() != expectedEsc && r.URL.Path != expectedRaw {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		upstreamPath = appendLivePreviewQuery(companionapi.LiveRecordingPreviewPath(mt.recordingKey), r.URL.Query())

	case ticketKindLivePreviewHLS:
		expectedHLSEsc := "/live-preview/" + url.PathEscape(mt.recordingKey) + "/preview.m3u8"
		expectedHLSRaw := "/live-preview/" + mt.recordingKey + "/preview.m3u8"
		expectedSegEsc := "/live-preview/" + url.PathEscape(mt.recordingKey) + "/preview"
		expectedSegRaw := "/live-preview/" + mt.recordingKey + "/preview"
		switch {
		case r.URL.EscapedPath() == expectedHLSEsc || r.URL.Path == expectedHLSRaw:
			upstreamPath = appendLivePreviewQuery(companionapi.LiveRecordingPreviewHLSPath(mt.recordingKey), r.URL.Query())
			rewriteHLS = true
		case r.URL.EscapedPath() == expectedSegEsc || r.URL.Path == expectedSegRaw:
			upstreamPath = appendLivePreviewQuery(companionapi.LiveRecordingPreviewPath(mt.recordingKey), r.URL.Query())
		default:
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

	default:
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	baseURL, token, version, channel, connected := p.target()
	if !connected || baseURL == "" || token == "" {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}

	upCtx, _, cleanup, stillValid := p.registerActive(ticket, r.Context())
	if !stillValid {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	defer cleanup()

	upstreamURL := strings.TrimRight(baseURL, "/") + upstreamPath
	upReq, err := http.NewRequestWithContext(upCtx, r.Method, upstreamURL, nil)
	if err != nil {
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	upReq.Header.Set("Authorization", "Bearer "+token)
	upReq.Header.Set(companionapi.HeaderProtocol, companionapi.ProtocolVersion)
	if version != "" {
		upReq.Header.Set(companionapi.HeaderVersion, version)
	}
	if channel != "" {
		upReq.Header.Set(companionapi.HeaderChannel, channel)
	}
	if rng := r.Header.Get("Range"); rng != "" {
		upReq.Header.Set("Range", rng)
	}
	if ifRng := r.Header.Get("If-Range"); ifRng != "" {
		upReq.Header.Set("If-Range", ifRng)
	}

	// Stream response directly; never buffer whole video in memory and never touch disk.
	streamClient := *p.client
	streamClient.Timeout = 0
	resp, err := streamClient.Do(upReq)
	if err != nil {
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if rewriteHLS && resp.StatusCode == http.StatusOK {
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if readErr != nil {
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}
		rewritten := rewriteHLSPlaylistTicket(string(raw), ticket)
		for _, hdr := range []string{"Content-Type", companionapi.HeaderLivePart, companionapi.HeaderLiveBytes} {
			if v := resp.Header.Get(hdr); v != "" {
				w.Header().Set(hdr, v)
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = io.WriteString(w, rewritten)
		}
		return
	}

	for _, hdr := range []string{
		"Content-Type",
		"Content-Length",
		"Content-Range",
		"Accept-Ranges",
		"ETag",
		"Last-Modified",
		companionapi.HeaderLivePart,
		companionapi.HeaderLiveBytes,
	} {
		if v := resp.Header.Get(hdr); v != "" {
			w.Header().Set(hdr, v)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, resp.Body)
	}
}

func appendLivePreviewQuery(path string, q url.Values) string {
	out := url.Values{}
	if part := strings.TrimSpace(q.Get("part")); part != "" {
		out.Set("part", part)
	}
	if mode := strings.TrimSpace(q.Get("mode")); mode != "" {
		out.Set("mode", mode)
	}
	if len(out) == 0 {
		return path
	}
	return path + "?" + out.Encode()
}

func rewriteHLSPlaylistTicket(playlist, ticket string) string {
	escapedT := url.QueryEscape(ticket)
	lines := strings.Split(playlist, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "#EXT-X-MAP:"):
			lines[i] = rewriteHLSAttrURI(line, escapedT)
		case strings.HasPrefix(trimmed, "preview"):
			lines[i] = appendTicketQueryParam(trimmed, escapedT)
		}
	}
	return strings.Join(lines, "\n")
}

func rewriteHLSAttrURI(line, escapedT string) string {
	const marker = `URI="`
	idx := strings.Index(line, marker)
	if idx < 0 {
		return line
	}
	start := idx + len(marker)
	end := strings.IndexByte(line[start:], '"')
	if end < 0 {
		return line
	}
	origURI := line[start : start+end]
	newURI := appendTicketQueryParam(origURI, escapedT)
	return line[:start] + newURI + line[start+end:]
}

func appendTicketQueryParam(uri, escapedT string) string {
	if strings.Contains(uri, "?") {
		return uri + "&t=" + escapedT
	}
	return uri + "?t=" + escapedT
}

func escapeMediaPath(key, relPath string) string {
	segs := strings.Split(strings.TrimPrefix(relPath, "/"), "/")
	escaped := make([]string, 0, len(segs)+1)
	escaped = append(escaped, url.PathEscape(key))
	for _, s := range segs {
		escaped = append(escaped, url.PathEscape(s))
	}
	return strings.Join(escaped, "/")
}
