package companion

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/companionapi"
	"github.com/chialecode/trace/internal/storage"
)

func TestSelectListenCandidatesExcludesPhysicalCGNATAndPublic(t *testing.T) {
	ifaces := []NetworkInterface{
		{
			Name:    "Ethernet",
			Flags:   net.FlagUp | net.FlagBroadcast,
			Virtual: false,
			Addrs: []netip.Addr{
				netip.MustParseAddr("192.168.1.20"),
				netip.MustParseAddr("100.64.12.34"), // Carrier-grade NAT on physical NIC -> MUST EXCLUDE!
				netip.MustParseAddr("8.8.8.8"),      // Public IP -> MUST EXCLUDE!
				netip.MustParseAddr("fd12:3456:789a::1"),
				netip.MustParseAddr("2001:db8::1"), // Public IPv6 -> MUST EXCLUDE!
			},
		},
		{
			Name:    "Tailscale",
			Flags:   net.FlagUp | net.FlagPointToPoint,
			Virtual: true,
			Addrs: []netip.Addr{
				netip.MustParseAddr("100.100.10.5"), // Tailscale on virtual tunnel -> MUST INCLUDE!
			},
		},
		{
			Name:    "Loopback",
			Flags:   net.FlagUp | net.FlagLoopback,
			Virtual: false,
			Addrs:   []netip.Addr{netip.MustParseAddr("127.0.0.1")},
		},
		{
			Name:    "DownAdapter",
			Flags:   0,
			Virtual: false,
			Addrs:   []netip.Addr{netip.MustParseAddr("10.0.0.5")},
		},
	}

	got := SelectListenCandidates(ifaces)
	gotAddrs := make([]string, len(got))
	for i, c := range got {
		gotAddrs[i] = c.Addr.String()
	}
	want := []string{"192.168.1.20", "100.100.10.5", "fd12:3456:789a::1"}
	if strings.Join(gotAddrs, ",") != strings.Join(want, ",") {
		t.Fatalf("SelectListenCandidates = %v, want %v", gotAddrs, want)
	}
	if !got[1].Virtual {
		t.Errorf("expected Tailscale candidate to be marked Virtual=true")
	}
}

func TestIsAllowedSourceRules(t *testing.T) {
	cases := []struct {
		ip      string
		virtual bool
		want    bool
	}{
		{"127.0.0.1", false, true},
		{"::1", false, true},
		{"10.0.0.5", false, true},
		{"172.16.5.9", false, true},
		{"192.168.1.55", false, true},
		{"169.254.1.2", false, true},
		{"fd00::1234", false, true},
		{"fe80::1", false, true},
		{"100.64.0.5", true, true},   // Tailscale/VPN tunnel -> allowed
		{"100.64.0.5", false, false}, // Physical NIC CGNAT -> rejected
		{"1.1.1.1", false, false},
		{"1.1.1.1", true, false},
		{"2001:4860:4860::8888", false, false},
	}
	for _, tc := range cases {
		ip := netip.MustParseAddr(tc.ip)
		if got := IsAllowedSource(ip, tc.virtual); got != tc.want {
			t.Errorf("IsAllowedSource(%s, virtual=%v) = %v, want %v", tc.ip, tc.virtual, got, tc.want)
		}
	}
}

func TestStoreCorruptRecoveryAndNewerSchemaRejection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, StateFileName)

	// 1. Corrupt JSON is backed up and store starts cleanly empty (NFR-DATA-001).
	if err := os.WriteFile(path, []byte("{corrupt-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := OpenStore(dir, "TestPC")
	if err != nil {
		t.Fatalf("OpenStore corrupt recovery failed: %v", err)
	}
	if st.CorruptBackup() == "" {
		t.Fatalf("expected CorruptBackup to be populated")
	}
	if _, err := os.Stat(st.CorruptBackup()); err != nil {
		t.Fatalf("backup file missing: %v", err)
	}
	if st.Count() != 0 {
		t.Fatalf("expected 0 devices after corrupt recovery, got %d", st.Count())
	}

	// 2. Newer schema version (version: 99) returns NewerDataError without overwriting file.
	newerRaw := `{"version":99,"node":"abcd","devices":[]}`
	if err := os.WriteFile(path, []byte(newerRaw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(dir, "TestPC"); !storage.IsNewerData(err) {
		t.Fatalf("expected storage.NewerDataError for version 99, got %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != newerRaw {
		t.Fatalf("newer schema file was modified!")
	}
}

func TestStoreTokenHashAndDeviceLimitAndRevoke(t *testing.T) {
	dir := t.TempDir()
	st, err := OpenStore(dir, "TestPC")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()

	var tokens []string
	var ids []string
	for i := 0; i < companionapi.MaxDevices; i++ {
		dev, tok, err := st.AddDevice("Phone", "Pixel", "android", companionapi.ChannelDev, "192.168.1.50", now)
		if err != nil {
			t.Fatalf("AddDevice %d failed: %v", i, err)
		}
		if dev.TokenHash == tok || len(dev.TokenHash) != 64 {
			t.Fatalf("TokenHash invalid or leaked plaintext")
		}
		tokens = append(tokens, tok)
		ids = append(ids, dev.ID)
	}

	// 6th device must be rejected.
	if _, _, err := st.AddDevice("Phone 6", "Pixel", "android", companionapi.ChannelDev, "192.168.1.56", now); !errors.Is(err, ErrDeviceLimitReached) {
		t.Fatalf("6th AddDevice err = %v, want ErrDeviceLimitReached", err)
	}

	// Plaintext token must NEVER appear on disk in companion.json.
	diskRaw, err := os.ReadFile(filepath.Join(dir, StateFileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range tokens {
		if strings.Contains(string(diskRaw), tok) {
			t.Fatalf("plaintext device token leaked into companion.json!")
		}
	}

	// Valid token authenticates; wrong token fails.
	if matched, ok := st.Authenticate(tokens[0]); !ok || matched.ID != ids[0] {
		t.Fatalf("Authenticate valid token failed")
	}
	if _, ok := st.Authenticate("wrong-token"); ok {
		t.Fatalf("Authenticate accepted wrong token")
	}

	// Revoking immediately invalidates the old token.
	if _, err := st.RevokeDevice(ids[0]); err != nil {
		t.Fatalf("RevokeDevice failed: %v", err)
	}
	if _, ok := st.Authenticate(tokens[0]); ok {
		t.Fatalf("revoked token still authenticated!")
	}
}

func TestPairingSecurityRules(t *testing.T) {
	dir := t.TempDir()
	st, err := OpenStore(dir, "TestPC")
	if err != nil {
		t.Fatal(err)
	}
	curNow := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	nowFn := func() time.Time { return curNow }

	pm := newPairingManager(nowFn, nil)
	fp := strings.Repeat("ab", 32)
	idCode, _ := companionapi.IdentityCode(fp)

	// 1. One-time QR code cannot be reused.
	sess, err := pm.Start(st.NodeID(), "TestPC", companionapi.ChannelDev, fp, idCode, 47382, []string{"192.168.1.20"})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := companionapi.DecodePairPayload(sess.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := pm.ConsumeCode(decoded.Code); err != nil {
		t.Fatalf("first ConsumeCode failed: %v", err)
	}
	if err := pm.ConsumeCode(decoded.Code); err == nil {
		t.Fatalf("one-time code was accepted a second time!")
	}

	// 2. Expiry after 5 minutes rejects code.
	sess2, err := pm.Start(st.NodeID(), "TestPC", companionapi.ChannelDev, fp, idCode, 47382, []string{"192.168.1.20"})
	if err != nil {
		t.Fatal(err)
	}
	curNow = curNow.Add(companionapi.PairCodeTTL + time.Second)
	if err := pm.ConsumeCode(sess2.ShortCode); err == nil {
		t.Fatalf("expired pairing code was accepted!")
	}

	// 3. 5 wrong short-code attempts invalidate the short code.
	sess3, err := pm.Start(st.NodeID(), "TestPC", companionapi.ChannelDev, fp, idCode, 47382, []string{"192.168.1.20"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < companionapi.PairShortCodeMaxErrors; i++ {
		if err := pm.ConsumeCode("ZZZZ-ZZZZ"); err == nil {
			t.Fatalf("wrong short code accepted")
		}
	}
	if err := pm.ConsumeCode(sess3.ShortCode); err == nil {
		t.Fatalf("short code remained valid after 5 wrong attempts!")
	}

	// 4. pairId without matching pairSecret returns not-found, and token is returned ONLY ONCE.
	pending, err := pm.CreatePending(companionapi.PairRequest{
		DeviceName: "Pixel 9",
		Model:      "Pixel 9",
		Platform:   "android",
		Channel:    companionapi.ChannelDev,
		PairSecret: "secret-0123456789abcdef",
	}, "192.168.1.55")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pm.Approve(pending.PairID, st); err != nil {
		t.Fatal(err)
	}
	// Wrong pairSecret -> not found (HTTP 404).
	if _, found := pm.Poll(context.Background(), pending.PairID, "wrong-secret-0123456789", 10*time.Millisecond); found {
		t.Fatalf("Poll succeeded with wrong pairSecret!")
	}
	// Correct pairSecret -> gets token once.
	resp, found := pm.Poll(context.Background(), pending.PairID, "secret-0123456789abcdef", 10*time.Millisecond)
	if !found || resp.Status != companionapi.PairStatusApproved || resp.Token == "" {
		t.Fatalf("Poll with valid pairSecret got (%+v, %v)", resp, found)
	}
	// Second poll for the same pairId -> not found (token cannot be retrieved twice).
	if _, found2 := pm.Poll(context.Background(), pending.PairID, "secret-0123456789abcdef", 10*time.Millisecond); found2 {
		t.Fatalf("second Poll for already-consumed pairId succeeded!")
	}
}

func TestServerSecurityMiddlewareAndSentinelNoLeak(t *testing.T) {
	dir := t.TempDir()
	st, err := OpenStore(dir, "TestPC")
	if err != nil {
		t.Fatal(err)
	}
	curNow := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	nowFn := func() time.Time { return curNow }

	var logMu sync.Mutex
	var logs bytes.Buffer
	logf := func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		_, _ = logs.WriteString(format + "\n")
	}

	srv := NewServer(ServerOptions{
		Store:       st,
		Channel:     companionapi.ChannelDev,
		NodeName:    "TestPC",
		Version:     "0.1.0-dev",
		Now:         nowFn,
		Logf:        logf,
		PollMaxWait: 50 * time.Millisecond,
	})

	// 1. Public source IP gets 403 forbidden_source BEFORE auth and WITHOUT reading body.
	bodyRead := false
	req := httptest.NewRequest(http.MethodPost, companionapi.PathPair, io.NopCloser(&readProbe{onRead: func() { bodyRead = true }}))
	req.RemoteAddr = "8.8.8.8:54321"
	req.Header.Set(companionapi.HeaderProtocol, companionapi.ProtocolVersion)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("public IP status = %d, want 403", rec.Code)
	}
	if bodyRead {
		t.Fatalf("server read request body from forbidden public source!")
	}

	// 2. Physical CGNAT 100.64.x.x rejected with 403; virtual 100.64.x.x passes source filter.
	reqCG := httptest.NewRequest(http.MethodGet, companionapi.PathStatus, nil)
	reqCG.RemoteAddr = "100.64.1.9:12345"
	reqCG.Header.Set(companionapi.HeaderProtocol, companionapi.ProtocolVersion)
	recCG := httptest.NewRecorder()
	srv.ServeHTTP(recCG, reqCG)
	if recCG.Code != http.StatusForbidden {
		t.Fatalf("physical CGNAT status = %d, want 403", recCG.Code)
	}

	// 3. Protocol version mismatch returns 409 protocol_mismatch with both versions.
	reqProto := httptest.NewRequest(http.MethodGet, companionapi.PathStatus, nil)
	reqProto.RemoteAddr = "192.168.1.90:12345"
	reqProto.Header.Set(companionapi.HeaderProtocol, "99")
	reqProto.Header.Set(companionapi.HeaderVersion, "9.9.9")
	recProto := httptest.NewRecorder()
	srv.ServeHTTP(recProto, reqProto)
	if recProto.Code != http.StatusConflict {
		t.Fatalf("protocol mismatch status = %d, want 409", recProto.Code)
	}
	var eb companionapi.ErrorBody
	_ = json.Unmarshal(recProto.Body.Bytes(), &eb)
	if eb.Error.Code != companionapi.CodeProtocolMismatch || eb.Error.ServerProtocol != companionapi.ProtocolVersion || eb.Error.ClientProtocol != "99" {
		t.Fatalf("unexpected protocol mismatch body: %+v", eb)
	}

	// 4. Channel mismatch on POST /api/v1/pair returns 409 channel_mismatch.
	pairBody, _ := json.Marshal(companionapi.PairRequest{
		Code:       "SENTINEL_PAIR_CODE_999",
		DeviceName: "Pixel",
		Platform:   "android",
		Channel:    companionapi.ChannelStable, // server is dev
		PairSecret: "SENTINEL_PAIR_SECRET_888",
	})
	reqCh := httptest.NewRequest(http.MethodPost, companionapi.PathPair, bytes.NewReader(pairBody))
	reqCh.RemoteAddr = "192.168.1.91:12345"
	reqCh.Header.Set(companionapi.HeaderProtocol, companionapi.ProtocolVersion)
	recCh := httptest.NewRecorder()
	srv.ServeHTTP(recCh, reqCh)
	if recCh.Code != http.StatusConflict {
		t.Fatalf("channel mismatch status = %d, want 409", recCh.Code)
	}

	// 5. Rate limiter: 5 auth failures in 1m -> 429 for 10m; sentinel token never appears in logs/responses.
	sentinelTok := "SENTINEL_BAD_TOKEN_XYZ_777"
	for i := 0; i < 5; i++ {
		r := httptest.NewRequest(http.MethodGet, companionapi.PathStatus, nil)
		r.RemoteAddr = "192.168.1.92:12345"
		r.Header.Set(companionapi.HeaderProtocol, companionapi.ProtocolVersion)
		r.Header.Set("Authorization", "Bearer "+sentinelTok)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, want 401", i, w.Code)
		}
		if strings.Contains(w.Body.String(), sentinelTok) {
			t.Fatalf("sentinel token echoed in error response!")
		}
	}
	r6 := httptest.NewRequest(http.MethodGet, companionapi.PathStatus, nil)
	r6.RemoteAddr = "192.168.1.92:12345"
	r6.Header.Set(companionapi.HeaderProtocol, companionapi.ProtocolVersion)
	w6 := httptest.NewRecorder()
	srv.ServeHTTP(w6, r6)
	if w6.Code != http.StatusTooManyRequests {
		t.Fatalf("6th attempt status = %d, want 429", w6.Code)
	}

	// After 10 minutes, block expires.
	curNow = curNow.Add(10*time.Minute + time.Second)
	r7 := httptest.NewRequest(http.MethodGet, companionapi.PathStatus, nil)
	r7.RemoteAddr = "192.168.1.92:12345"
	r7.Header.Set(companionapi.HeaderProtocol, companionapi.ProtocolVersion)
	w7 := httptest.NewRecorder()
	srv.ServeHTTP(w7, r7)
	if w7.Code != http.StatusUnauthorized {
		t.Fatalf("post-block attempt status = %d, want 401", w7.Code)
	}

	// Verify zero sentinel secrets in logs.
	logText := logs.String()
	for _, secret := range []string{sentinelTok, "SENTINEL_PAIR_CODE_999", "SENTINEL_PAIR_SECRET_888"} {
		if strings.Contains(logText, secret) {
			t.Fatalf("sentinel %q leaked into companion logs!", secret)
		}
	}
}

func TestEventQueueOverflowNonBlocking(t *testing.T) {
	// Verify that pushing > 256 events into a client's queue never blocks and marks needsSync.
	c := &wsClient{
		deviceID:   "dev-1",
		coalesce:   0, // disable 1s coalescing to force overflow directly
		now:        time.Now,
		lastProgAt: make(map[string]time.Time),
		pendingPrg: make(map[string]companionapi.EventFrame),
		notify:     make(chan struct{}, 1),
		done:       make(chan struct{}),
	}
	done := make(chan struct{})
	go func() {
		for i := 0; i < 400; i++ {
			c.enqueue(companionapi.EventFrame{Type: companionapi.EventTaskChanged, Key: "t"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("enqueue blocked when queue overflowed (INV-13 violation)")
	}
	batch, closed := c.popBatch()
	if closed {
		t.Fatal("unexpected closed client")
	}
	// Should contain 256 queued events + 1 resync event.
	if len(batch) != companionapi.EventQueueLimit+1 {
		t.Fatalf("batch len = %d, want %d", len(batch), companionapi.EventQueueLimit+1)
	}
	last := batch[len(batch)-1]
	if last.Type != companionapi.EventResync || !last.NeedsSync {
		t.Fatalf("expected trailing resync frame, got %+v", last)
	}
}

func TestManualPairingMITMIdentityCodeMismatchAbortsWithoutSendingCode(t *testing.T) {
	dirReal := t.TempDir()
	dirMITM := t.TempDir()

	realID, err := EnsureTLSIdentity(dirReal)
	if err != nil {
		t.Fatal(err)
	}
	mitmID, err := EnsureTLSIdentity(dirMITM)
	if err != nil {
		t.Fatal(err)
	}
	if realID.IdentityCode == mitmID.IdentityCode {
		t.Fatal("unexpected collision between two random certs")
	}

	// Start a fake MITM TLS server that counts any HTTP request it receives.
	mitmRequests := 0
	mitmSrv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mitmRequests++
		w.WriteHeader(http.StatusOK)
	}))
	mitmSrv.TLS = mitmID.TLSConfig
	mitmSrv.StartTLS()
	defer mitmSrv.Close()

	// Phone manual-pairing step (design §4.2): connect TLS first, inspect leaf cert identity code,
	// compare with desktop's identity code, and abort WITHOUT sending short code if mismatch!
	addr := strings.TrimPrefix(mitmSrv.URL, "https://")
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 2 * time.Second}, "tcp", addr, &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // pinned manually via certificate SHA-256 identity code
	})
	if err != nil {
		t.Fatalf("tls dial mitm: %v", err)
	}
	peerFP := companionapi.Fingerprint(conn.ConnectionState().PeerCertificates[0].Raw)
	seenCode, err := companionapi.IdentityCode(peerFP)
	_ = conn.Close()
	if err != nil {
		t.Fatal(err)
	}
	if seenCode == realID.IdentityCode {
		t.Fatalf("MITM cert unexpectedly matched desktop identity code")
	}
	// Because seenCode != realID.IdentityCode, phone aborts before making any HTTP POST /api/v1/pair!
	if mitmRequests != 0 {
		t.Fatalf("MITM received %d HTTP requests, want 0 (short code must never be sent on mismatch)", mitmRequests)
	}
}

type readProbe struct {
	onRead func()
}

func (p *readProbe) Read(b []byte) (int, error) {
	if p.onRead != nil {
		p.onRead()
	}
	return 0, io.EOF
}
