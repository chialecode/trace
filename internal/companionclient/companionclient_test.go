package companionclient

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/companionapi"
)

func generateTestCert(t *testing.T) (tls.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  key,
	}, Fingerprint(der)
}

func TestQRCodeRoundTripAndValidation(t *testing.T) {
	_, fp := generateTestCert(t)
	in := companionapi.PairPayload{
		Version:     1,
		Node:        "node-1",
		Name:        "My-PC",
		Channel:     "dev",
		Port:        47382,
		Addrs:       []string{"192.168.1.20:47382", "10.0.0.5"},
		Fingerprint: fp,
		Code:        "secret-128",
	}
	encoded, err := EncodeQRCode(in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseQRCode(encoded)
	if err != nil {
		t.Fatalf("ParseQRCode failed: %v", err)
	}
	if got.Node != "node-1" || got.Channel != "dev" || got.Fingerprint != fp {
		t.Fatalf("unexpected decoded payload: %+v", got)
	}
	if len(got.Addrs) != 2 || got.Addrs[1] != "10.0.0.5:47382" {
		t.Fatalf("unexpected normalized addrs: %v", got.Addrs)
	}

	if _, err := ParseQRCode("trace-remote:1:abc"); err == nil {
		t.Fatal("expected error for wrong prefix")
	}
}

func TestIdentityCodeAndShortCode(t *testing.T) {
	fp := "3f7a91c20b5ed844" + strings.Repeat("a", 48)
	if got := IdentityCode(fp); got != "3F7A-91C2-0B5E-D844" {
		t.Fatalf("IdentityCode = %q", got)
	}
	sc, err := NormalizeShortCode(" 3f7k 9q2m ")
	if err != nil || sc != "3F7K-9Q2M" {
		t.Fatalf("NormalizeShortCode = (%q, %v)", sc, err)
	}
	if _, err := NormalizeShortCode("123"); err == nil {
		t.Fatal("expected error for short code != 8 chars")
	}
}

func TestProbeIdentitySendsZeroHTTPRequests(t *testing.T) {
	cert, fp := generateTestCert(t)
	var httpHits atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	srv.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
	}
	srv.StartTLS()
	defer srv.Close()

	addr := strings.TrimPrefix(srv.URL, "https://")
	gotFP, idCode, err := ProbeIdentity(context.Background(), addr)
	if err != nil {
		t.Fatalf("ProbeIdentity failed: %v", err)
	}
	if gotFP != fp {
		t.Fatalf("fp = %q, want %q", gotFP, fp)
	}
	if idCode != IdentityCode(fp) {
		t.Fatalf("identityCode = %q, want %q", idCode, IdentityCode(fp))
	}
	if httpHits.Load() != 0 {
		t.Fatalf("expected 0 HTTP hits during ProbeIdentity, got %d", httpHits.Load())
	}
}

func TestPinnedHTTPClientRejectsMismatchedCert(t *testing.T) {
	cert1, fp1 := generateTestCert(t)
	_, fp2 := generateTestCert(t)
	if fp1 == fp2 {
		t.Fatal("unexpected identical fingerprints")
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert1},
	}
	srv.StartTLS()
	defer srv.Close()

	client, err := PinnedHTTPClient(fp2, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Get(srv.URL + "/api/v1/status")
	if err == nil {
		t.Fatal("expected TLS pinning error")
	}
	if ce := ClassifyTransportError(err); ce.Kind != KindCertChanged {
		t.Fatalf("Kind = %q, want %q (err=%v)", ce.Kind, KindCertChanged, err)
	}
}

func TestMediaProxyRangeTicketsAndRevocation(t *testing.T) {
	cert, fp := generateTestCert(t)
	payload := []byte("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	const wantToken = "device-token-secret-xyz"

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+wantToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/api/v1/media/bili%3A123/video.mp4" && r.URL.Path != "/api/v1/media/bili:123/video.mp4" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		http.ServeContent(w, r, "video.mp4", time.Time{}, bytes.NewReader(payload))
	}))
	srv.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
	}
	srv.StartTLS()
	defer srv.Close()

	pinned, err := PinnedHTTPClient(fp, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := NewMediaProxy(pinned, func() (string, string, string, string, bool) {
		return srv.URL, wantToken, "0.1.0", "dev", true
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	playURL, ticket, err := proxy.IssueTicket("bili:123", "video.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(playURL, "http://127.0.0.1:") {
		t.Fatalf("expected loopback URL, got %q", playURL)
	}

	// Range request should return 206 Partial Content with Cache-Control: no-store.
	req, _ := http.NewRequest(http.MethodGet, playURL, nil)
	req.Header.Set("Range", "bytes=10-19")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", resp.StatusCode)
	}
	if string(body) != "ABCDEFGHIJ" {
		t.Fatalf("range body = %q, want ABCDEFGHIJ", string(body))
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", resp.Header.Get("Cache-Control"))
	}

	// Wrong path with valid ticket -> 403.
	wrongPathURL := strings.Replace(playURL, "video.mp4", "other.mp4", 1)
	resp2, err := http.Get(wrongPathURL)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong path status = %d, want 403", resp2.StatusCode)
	}

	// Revoke ticket -> 403.
	proxy.RevokeTicket(ticket)
	resp3, err := http.Get(playURL)
	if err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusForbidden {
		t.Fatalf("revoked ticket status = %d, want 403", resp3.StatusCode)
	}
}

func TestMemoryImageCacheAndBackoff(t *testing.T) {
	c := NewMemoryImageCache(10)
	c.Put("a", []byte("123456"))
	c.Put("b", []byte("7890"))
	if c.Bytes() != 10 {
		t.Fatalf("Bytes = %d, want 10", c.Bytes())
	}
	if _, ok := c.Get("a"); !ok {
		t.Fatal("expected a in cache")
	}
	// Adding "c" (4 bytes) should evict "b" since "a" was just touched.
	c.Put("c", []byte("wxyz"))
	if _, ok := c.Get("b"); ok {
		t.Fatal("expected b to be evicted")
	}
	if got, ok := c.Get("a"); !ok || string(got) != "123456" {
		t.Fatalf("expected a preserved, got %q (%v)", got, ok)
	}
	c.Clear()
	if c.Bytes() != 0 {
		t.Fatalf("after Clear Bytes = %d", c.Bytes())
	}

	sched := NewReconnectSchedule()
	want := []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second, 30 * time.Second}
	for i, w := range want {
		if got := sched.Next(); got != w {
			t.Fatalf("step %d = %v, want %v", i, got, w)
		}
	}
	sched.Reset()
	if got := sched.Next(); got != 2*time.Second {
		t.Fatalf("after Reset = %v, want 2s", got)
	}
}

func TestWebSocketEventsAndHeartbeatTimeout(t *testing.T) {
	cert, fp := generateTestCert(t)

	writeServerFrame := func(w io.Writer, op byte, payload []byte) {
		var hdr [2]byte
		hdr[0] = 0x80 | op
		hdr[1] = byte(len(payload))
		_, _ = w.Write(hdr[:])
		_, _ = w.Write(payload)
	}

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/events" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		key := r.Header.Get("Sec-WebSocket-Key")
		h := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		accept := base64.StdEncoding.EncodeToString(h[:])
		hj, ok := w.(http.Hijacker)
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		conn, brw, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + accept + "\r\n\r\n")
		_ = brw.Flush()

		// Send ping, then bye event, then go silent to trigger client heartbeat timeout.
		writeServerFrame(conn, wsOpPing, []byte("hi"))
		// Read pong from client.
		var buf [16]byte
		_, _ = io.ReadFull(bufio.NewReader(conn), buf[:8])
		writeServerFrame(conn, wsOpText, []byte(`{"type":"bye","reason":"exit"}`))
		time.Sleep(500 * time.Millisecond)
	}))
	srv.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
	}
	srv.StartTLS()
	defer srv.Close()

	pinned, err := PinnedHTTPClient(fp, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ec, err := DialEvents(context.Background(), pinned, srv.URL, "tok", "0.1.0", "dev", 120*time.Millisecond)
	if err != nil {
		t.Fatalf("DialEvents failed: %v", err)
	}
	defer ec.Close()

	ev, err := ec.ReadEvent(context.Background())
	if err != nil {
		t.Fatalf("ReadEvent failed: %v", err)
	}
	if ev.Type != "bye" || ev.Reason != "exit" {
		t.Fatalf("unexpected event: %+v", ev)
	}
	// Next ReadEvent should time out after ~120ms when server sends no frames.
	_, err = ec.ReadEvent(context.Background())
	if err == nil {
		t.Fatal("expected heartbeat timeout error")
	}
	if KindOf(err) != KindUnreachable {
		t.Fatalf("timeout kind = %q, want %q (err=%v)", KindOf(err), KindUnreachable, err)
	}
	_ = binary.BigEndian
}

func TestConnectStatusFailoverLoggedOutAndNoLeakSentinel(t *testing.T) {
	cert, fp := generateTestCert(t)
	const sentinelToken = "sentinel-device-token-secret-123456"
	const sentinelCode = "SENTINEL-PAIR-CODE"
	const sentinelSecret = "sentinel-pair-secret-hex-999"

	var loggedIn atomic.Bool
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/status":
			_ = json.NewEncoder(w).Encode(companionapi.StatusResponse{
				Node:     "node-1",
				Name:     "Desk-PC",
				Version:  "0.1.0-dev",
				Protocol: "1",
				Channel:  "dev",
				LoggedIn: loggedIn.Load(),
				Disk:     companionapi.DiskSpace{FreeBytes: 10 << 30, TotalBytes: 100 << 30},
			})
		case "/api/v1/pair":
			if r.Header.Get("Authorization") == "Bearer "+sentinelToken {
				_ = json.NewEncoder(w).Encode(companionapi.PairStartResponse{
					Status: "updated",
					Node:   "node-1",
					Name:   "Desk-PC",
					Addrs:  []string{strings.TrimPrefix(r.Host, "https://")},
				})
				return
			}
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{
					"code":    "channel_mismatch",
					"message": "通道不一致",
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	srv.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
	}
	srv.StartTLS()
	defer srv.Close()

	// Closed local port for first address so ConnectStatus fails over to srv.
	deadLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadAddr := deadLn.Addr().String()
	_ = deadLn.Close()
	liveAddr := strings.TrimPrefix(srv.URL, "https://")

	rec := PairingRecord{
		Node:     "node-1",
		Name:     "Desk-PC",
		Channel:  "dev",
		FP:       fp,
		Addrs:    []string{deadAddr, liveAddr},
		LastAddr: deadAddr,
	}
	cli, err := NewClient(rec, sentinelToken, "0.1.0-dev", "dev")
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	// 1. Failover succeeds to liveAddr, returns KindDesktopLoggedOut when loggedIn=false.
	_, gotAddr, err := cli.ConnectStatus(context.Background())
	if KindOf(err) != KindDesktopLoggedOut {
		t.Fatalf("expected KindDesktopLoggedOut, got %v", err)
	}
	if gotAddr != liveAddr {
		t.Fatalf("activeAddr = %q, want %q", gotAddr, liveAddr)
	}

	// 2. Logged in -> succeeds.
	loggedIn.Store(true)
	st, gotAddr, err := cli.ConnectStatus(context.Background())
	if err != nil {
		t.Fatalf("ConnectStatus failed: %v", err)
	}
	if st.Name != "Desk-PC" || gotAddr != liveAddr {
		t.Fatalf("unexpected status=%+v addr=%q", st, gotAddr)
	}

	// 3. Re-scan update addresses sends Bearer token and does not leak secrets in errors.
	freshAddrs, winAddr, err := RescanUpdateAddrs(context.Background(), []string{deadAddr, liveAddr}, fp, sentinelToken, companionapi.PairRequest{
		Code:       sentinelCode,
		PairSecret: sentinelSecret,
		DeviceName: "Pixel",
		Platform:   "android",
		Channel:    "dev",
	})
	if err != nil || winAddr != liveAddr || len(freshAddrs) == 0 {
		t.Fatalf("RescanUpdateAddrs = (%v, %q, %v)", freshAddrs, winAddr, err)
	}

	// 4. Channel mismatch error must not leak sentinel values.
	_, _, _, err = PairWithAddrs(context.Background(), []string{liveAddr}, fp, companionapi.PairRequest{
		Code:       sentinelCode,
		PairSecret: sentinelSecret,
		DeviceName: "Pixel",
		Platform:   "android",
		Channel:    "stable",
	}, nil)
	if KindOf(err) != KindChannelMismatch {
		t.Fatalf("expected KindChannelMismatch, got %v", err)
	}
	for _, secret := range []string{sentinelToken, sentinelCode, sentinelSecret} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked secret %q: %q", secret, err.Error())
		}
	}
}

func TestPinnedHTTPClientLongPollAnd401InvalidCodeAndRescanPortNormalization(t *testing.T) {
	cert, fp := generateTestCert(t)

	// 1. Long-poll client (>30s timeout) must not keep 30s ResponseHeaderTimeout.
	pollCli, err := PinnedHTTPClient(fp, 70*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	tr, ok := pollCli.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", pollCli.Transport)
	}
	if tr.ResponseHeaderTimeout < 65*time.Second && tr.ResponseHeaderTimeout != 0 {
		t.Fatalf("ResponseHeaderTimeout = %v, want >= 65s or 0", tr.ResponseHeaderTimeout)
	}

	// 2. HTTP 401 with invalid_code vs unauthorized distinction.
	ceCode := classifyHTTPStatus(http.StatusUnauthorized, http.Header{}, []byte(`{"error":{"code":"invalid_code"}}`), "0.1.0")
	if ceCode.Kind != KindBadPairingCode {
		t.Fatalf("401 invalid_code Kind = %q, want %q", ceCode.Kind, KindBadPairingCode)
	}
	ceAuth := classifyHTTPStatus(http.StatusUnauthorized, http.Header{}, []byte(`{"error":{"code":"unauthorized"}}`), "0.1.0")
	if ceAuth.Kind != KindUnauthorized {
		t.Fatalf("401 unauthorized Kind = %q, want %q", ceAuth.Kind, KindUnauthorized)
	}

	// 3. RescanUpdateAddrs normalizes bare IPv4/IPv6 with winning port and deduplicates.
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == companionapi.PathPair && r.Method == http.MethodPost {
			_ = json.NewEncoder(w).Encode(companionapi.PairStartResponse{
				Status: "updated",
				Addrs:  []string{"192.168.1.99", "fd00::1", "192.168.1.99"},
			})
			return
		}
		http.NotFound(w, r)
	}))
	srv.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
	}
	srv.StartTLS()
	defer srv.Close()
	addr := srv.Listener.Addr().String()

	_, wantPort, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	gotAddrs, winAddr, err := RescanUpdateAddrs(context.Background(), []string{addr}, fp, "tok-123", companionapi.PairRequest{
		Code:    "ABCD-EFGH",
		Channel: "dev",
	})
	if err != nil || winAddr != addr {
		t.Fatalf("RescanUpdateAddrs = (%v, %q, %v)", gotAddrs, winAddr, err)
	}
	wantAddrs := []string{"192.168.1.99:" + wantPort, "[fd00::1]:" + wantPort}
	if len(gotAddrs) != len(wantAddrs) || gotAddrs[0] != wantAddrs[0] || gotAddrs[1] != wantAddrs[1] {
		t.Fatalf("normalized rescan addrs = %v, want %v", gotAddrs, wantAddrs)
	}
}

func TestMediaProxyLivePreviewTicketsHLSAndRevokeCancelsStream(t *testing.T) {
	cert, fp := generateTestCert(t)
	payload := []byte("LIVE-FMP4-BYTES-0123456789ABCDEF")
	const wantToken = "live-preview-tok-123"

	streamCanceled := make(chan struct{}, 1)
	streamStarted := make(chan struct{}, 1)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+wantToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/live/recordings/bilibili:1:2/preview", "/api/v1/live/recordings/bilibili%3A1%3A2/preview":
			if r.URL.Query().Get("mode") == "stream" {
				w.Header().Set("Content-Type", "video/mp4")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(payload[:8])
				if fl, ok := w.(http.Flusher); ok {
					fl.Flush()
				}
				select {
				case streamStarted <- struct{}{}:
				default:
				}
				<-r.Context().Done()
				select {
				case streamCanceled <- struct{}{}:
				default:
				}
				return
			}
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set(companionapi.HeaderLivePart, "1")
			http.ServeContent(w, r, "live.mp4", time.Time{}, bytes.NewReader(payload))
		case "/api/v1/live/recordings/bilibili:1:2/preview.m3u8", "/api/v1/live/recordings/bilibili%3A1%3A2/preview.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl; charset=utf-8")
			_, _ = io.WriteString(w, "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-MAP:URI=\"preview?part=1\",BYTERANGE=\"16@0\"\n#EXTINF:2.000,\n#EXT-X-BYTERANGE:16@16\npreview?part=1\n")
		default:
			http.NotFound(w, r)
		}
	}))
	srv.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
	}
	srv.StartTLS()
	defer srv.Close()

	pinned, err := PinnedHTTPClient(fp, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := NewMediaProxy(pinned, func() (string, string, string, string, bool) {
		return srv.URL, wantToken, "0.2.0-dev", "dev", true
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	// 1. Live preview ticket + Range + cross-recording rejection.
	playURL, ticket, err := proxy.IssueLivePreviewTicket("bilibili:1:2")
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, playURL, nil)
	req.Header.Set("Range", "bytes=0-15")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || string(body) != string(payload[:16]) {
		t.Fatalf("live preview range = (%d, %q)", resp.StatusCode, string(body))
	}
	badURL := strings.Replace(playURL, "bilibili:1:2", "bilibili:9:9", 1)
	if badURL == playURL {
		t.Fatalf("test setup bug: playURL did not contain recordingKey: %q", playURL)
	}
	respBad, err := http.Get(badURL)
	if err != nil {
		t.Fatal(err)
	}
	respBad.Body.Close()
	if respBad.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-recording ticket status = %d, want 403", respBad.StatusCode)
	}

	// 2. RevokeTicket immediately cancels in-flight upstream preview stream!
	go func() {
		r, err := http.Get(playURL + "&mode=stream")
		if err == nil {
			_, _ = io.ReadAll(r.Body)
			r.Body.Close()
		}
	}()
	select {
	case <-streamStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for upstream preview stream to start")
	}
	proxy.RevokeTicket(ticket)
	select {
	case <-streamCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("RevokeTicket did not cancel in-flight upstream stream")
	}

	// 3. HLS preview ticket rewrites URI and authorizes byte-range segment under same ticket.
	hlsURL, hlsTicket, err := proxy.IssueLivePreviewHLSTicket("bilibili:1:2")
	if err != nil {
		t.Fatal(err)
	}
	respM3U, err := http.Get(hlsURL)
	if err != nil {
		t.Fatal(err)
	}
	m3u, _ := io.ReadAll(respM3U.Body)
	respM3U.Body.Close()
	if respM3U.StatusCode != http.StatusOK || !strings.Contains(string(m3u), `URI="preview?part=1&t=`) {
		t.Fatalf("HLS playlist rewrite failed: status=%d body=%s", respM3U.StatusCode, string(m3u))
	}
	segURL := strings.Replace(hlsURL, "/preview.m3u8?t=", "/preview?part=1&t=", 1)
	segReq, _ := http.NewRequest(http.MethodGet, segURL, nil)
	segReq.Header.Set("Range", "bytes=16-31")
	respSeg, err := http.DefaultClient.Do(segReq)
	if err != nil {
		t.Fatal(err)
	}
	segBody, _ := io.ReadAll(respSeg.Body)
	respSeg.Body.Close()
	if respSeg.StatusCode != http.StatusPartialContent || string(segBody) != string(payload[16:32]) {
		t.Fatalf("HLS segment fetch = (%d, %q)", respSeg.StatusCode, string(segBody))
	}
	proxy.RevokeTicket(hlsTicket)

	// 4. Client.UpdateAddrs prepends discovered LAN address and preserves pinned FP.
	cli, err := NewClient(PairingRecord{
		Node:     "n1",
		FP:       fp,
		Channel:  "dev",
		Addrs:    []string{"192.168.1.10:47382"},
		LastAddr: "192.168.1.10:47382",
	}, wantToken, "0.2.0-dev", "dev")
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	cli.UpdateAddrs([]string{"192.168.1.99:47382", "192.168.1.10:47382"})
	rec := cli.Record()
	if rec.FP != fp || len(rec.Addrs) != 2 || rec.Addrs[0] != "192.168.1.99:47382" {
		t.Fatalf("UpdateAddrs unexpected record: %+v", rec)
	}
}
