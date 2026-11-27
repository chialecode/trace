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

type mediaTicket struct {
	key     string
	relPath string
}

// MediaProxy is a loopback-only HTTP server (127.0.0.1:<random port>) that
// forwards system video player requests over pinned-fingerprint TLS + Bearer
// token to the desktop companion server (design §6).
type MediaProxy struct {
	mu      sync.Mutex
	srv     *http.Server
	ln      net.Listener
	addr    string
	client  *http.Client
	target  func() (baseURL, token, version, channel string, ok bool)
	tickets map[string]mediaTicket
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
	p.tickets[t] = mediaTicket{key: key, relPath: relPath}
	addr := p.addr
	p.mu.Unlock()

	escapedPath := escapeMediaPath(key, relPath)
	return fmt.Sprintf("http://%s/media/%s?t=%s", addr, escapedPath, url.QueryEscape(t)), t, nil
}

// RevokeTicket invalidates a previously issued ticket immediately.
func (p *MediaProxy) RevokeTicket(ticket string) {
	if ticket == "" {
		return
	}
	p.mu.Lock()
	delete(p.tickets, ticket)
	p.mu.Unlock()
}

// RevokeAll invalidates every active playback ticket (called on disconnect/background).
func (p *MediaProxy) RevokeAll() {
	p.mu.Lock()
	if p.tickets != nil {
		clear(p.tickets)
	}
	p.mu.Unlock()
}

// Close revokes all tickets and shuts down the loopback listener.
func (p *MediaProxy) Close() error {
	p.mu.Lock()
	if p.tickets != nil {
		clear(p.tickets)
		p.tickets = nil
	}
	srv := p.srv
	p.mu.Unlock()
	if srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
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

	expectedPath := "/media/" + escapeMediaPath(mt.key, mt.relPath)
	if r.URL.EscapedPath() != expectedPath && r.URL.Path != "/media/"+mt.key+"/"+mt.relPath {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	baseURL, token, version, channel, connected := p.target()
	if !connected || baseURL == "" || token == "" {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}

	upstreamURL := strings.TrimRight(baseURL, "/") + companionapi.MediaPath(mt.key, mt.relPath)
	upReq, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, nil)
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

	for _, hdr := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Last-Modified"} {
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

func escapeMediaPath(key, relPath string) string {
	segs := strings.Split(strings.TrimPrefix(relPath, "/"), "/")
	escaped := make([]string, 0, len(segs)+1)
	escaped = append(escaped, url.PathEscape(key))
	for _, s := range segs {
		escaped = append(escaped, url.PathEscape(s))
	}
	return strings.Join(escaped, "/")
}
