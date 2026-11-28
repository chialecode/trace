package companion

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/companionapi"
	"github.com/chialecode/trace/internal/companionclient"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/subscription"
)

// ---- Helpers for fake sleep inhibitor, fake mDNS socket, synthetic fMP4, and live preview backend wrapper ----

type mockSleepInhibitor struct {
	mu      sync.Mutex
	active  bool
	closed  bool
	history []bool
}

func (m *mockSleepInhibitor) SetActive(active bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active = active
	m.history = append(m.history, active)
	return nil
}

func (m *mockSleepInhibitor) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *mockSleepInhibitor) snapshot() (active bool, closed bool, hist []bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active, m.closed, append([]bool(nil), m.history...)
}

type sentMDNSPacket struct {
	payload []byte
	dst     net.Addr
}

type fakeMDNSSocket struct {
	mu   sync.Mutex
	inCh chan struct {
		data []byte
		src  net.Addr
	}
	sent   []sentMDNSPacket
	closed bool
}

func newFakeMDNSSocket() *fakeMDNSSocket {
	return &fakeMDNSSocket{
		inCh: make(chan struct {
			data []byte
			src  net.Addr
		}, 16),
	}
}

func (f *fakeMDNSSocket) ReadFrom(b []byte) (int, net.Addr, error) {
	msg, ok := <-f.inCh
	if !ok {
		return 0, nil, net.ErrClosed
	}
	n := copy(b, msg.data)
	return n, msg.src, nil
}

func (f *fakeMDNSSocket) WriteTo(b []byte, addr net.Addr) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, net.ErrClosed
	}
	cp := append([]byte(nil), b...)
	f.sent = append(f.sent, sentMDNSPacket{payload: cp, dst: addr})
	return len(b), nil
}

func (f *fakeMDNSSocket) Close() error {
	f.mu.Lock()
	if !f.closed {
		f.closed = true
		close(f.inCh)
	}
	f.mu.Unlock()
	return nil
}

func (f *fakeMDNSSocket) inject(data []byte, src net.Addr) {
	f.inCh <- struct {
		data []byte
		src  net.Addr
	}{data: append([]byte(nil), data...), src: src}
}

func (f *fakeMDNSSocket) packets() []sentMDNSPacket {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]sentMDNSPacket, len(f.sent))
	copy(out, f.sent)
	return out
}

func buildDNSQuery(qname string, qtype uint16, unicast bool) []byte {
	var hdr [12]byte
	binary.BigEndian.PutUint16(hdr[4:6], 1) // QDCOUNT=1
	var buf []byte
	buf = append(buf, hdr[:]...)
	buf = append(buf, encodeDNSName(qname)...)
	buf = binary.BigEndian.AppendUint16(buf, qtype)
	qclass := dnsClassIN
	if unicast {
		qclass |= 0x8000
	}
	buf = binary.BigEndian.AppendUint16(buf, qclass)
	return buf
}

func makeMP4Box(boxType string, payload []byte) []byte {
	total := uint32(8 + len(payload))
	out := make([]byte, total)
	binary.BigEndian.PutUint32(out[0:4], total)
	copy(out[4:8], boxType)
	copy(out[8:], payload)
	return out
}

func buildSyntheticFMP4(fragCount int) (initBytes []byte, frags [][]byte) {
	ftyp := makeMP4Box("ftyp", []byte("isom\x00\x00\x02\x00isomiso5mp41"))
	moov := makeMP4Box("moov", bytes.Repeat([]byte{0x11}, 40))
	initBytes = append(ftyp, moov...)
	for i := 0; i < fragCount; i++ {
		moof := makeMP4Box("moof", bytes.Repeat([]byte{byte(0x20 + i)}, 24))
		mdat := makeMP4Box("mdat", bytes.Repeat([]byte{byte(0x40 + i)}, 64))
		frags = append(frags, append(moof, mdat...))
	}
	return initBytes, frags
}

type livePreviewBackendWrapper struct {
	service.BackendAPI
	mu       sync.RWMutex
	accounts []service.LiveAccountState
	previews map[string]service.LiveRecordingPreviewInfo
}

func (w *livePreviewBackendWrapper) ListLiveAccounts() []service.LiveAccountState {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return append([]service.LiveAccountState(nil), w.accounts...)
}

func (w *livePreviewBackendWrapper) LiveRecordingPreview(recKey string) (service.LiveRecordingPreviewInfo, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	info, ok := w.previews[recKey]
	return info, ok
}

func (w *livePreviewBackendWrapper) setPreview(recKey string, acct service.LiveAccountState, info service.LiveRecordingPreviewInfo) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.previews == nil {
		w.previews = make(map[string]service.LiveRecordingPreviewInfo)
	}
	w.previews[recKey] = info
	updated := false
	for i := range w.accounts {
		if w.accounts[i].RecordingKey == recKey {
			w.accounts[i] = acct
			updated = true
			break
		}
	}
	if !updated {
		w.accounts = append(w.accounts, acct)
	}
}

// ---- Test 1: LAN mDNS Advertiser, Interface Filtering, Goodbye TTL=0, and Secret Sentinels ----

func TestMDNSAdvertiserWireInterfaceFilteringAndNoSecretLeak(t *testing.T) {
	root := t.TempDir()
	backend := newTestCompanionBackend(t, true)
	defer backend.Close()

	sentinelToken := "SENTINEL_MDNS_DEVICE_TOKEN_999"
	sentinelCookie := "SENTINEL_MDNS_COOKIE_888"
	sentinelCode := "SENTINEL_MDNS_PAIR_CODE_777"

	// Verify filterMDNSAddrs strictly excludes public IPs, physical CGNAT, loopback, and IPv6 link-local.
	filtered := filterMDNSAddrs([]ListenCandidate{
		{Addr: netip.MustParseAddr("192.168.1.25"), Virtual: false, Iface: "Wi-Fi"},
		{Addr: netip.MustParseAddr("100.64.5.10"), Virtual: false, Iface: "Ethernet"}, // physical CGNAT -> excluded
		{Addr: netip.MustParseAddr("100.100.1.5"), Virtual: true, Iface: "Tailscale"}, // virtual VPN -> allowed
		{Addr: netip.MustParseAddr("8.8.8.8"), Virtual: false, Iface: "WAN"},          // public -> excluded
		{Addr: netip.MustParseAddr("127.0.0.1"), Virtual: true, Iface: "lo"},          // loopback -> excluded
		{Addr: netip.MustParseAddr("fe80::1"), Virtual: false, Iface: "Wi-Fi"},        // link-local -> excluded
		{Addr: netip.MustParseAddr("fd12:3456::1"), Virtual: false, Iface: "Wi-Fi"},   // ULA -> allowed
	})
	var gotStrs []string
	for _, a := range filtered {
		gotStrs = append(gotStrs, a.String())
	}
	wantStrs := []string{"192.168.1.25", "100.100.1.5", "fd12:3456::1"}
	if !slices.Equal(gotStrs, wantStrs) {
		t.Fatalf("filterMDNSAddrs = %v, want %v", gotStrs, wantStrs)
	}

	fakeSock := newFakeMDNSSocket()
	mgr, err := NewManager(ManagerOptions{
		DataDir:         root,
		Channel:         companionapi.ChannelDev,
		NodeName:        "DeskPC",
		Version:         "0.2.0-dev",
		Backend:         backend,
		ListenOverrides: []string{"127.0.0.1"},
		SleepInhibitor:  &mockSleepInhibitor{},
		MDNSFactory: func(_ []ListenCandidate) ([]mdnsBoundSocket, error) {
			return []mdnsBoundSocket{{
				ifaceName: "Wi-Fi",
				sock:      fakeSock,
				addrs:     []netip.Addr{netip.MustParseAddr("192.168.1.25")},
			}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Shutdown(companionapi.ByeReasonExit)

	port := pickFreeLoopbackPort(t)
	if err := mgr.ApplySettings(true, port); err != nil {
		t.Fatalf("ApplySettings(true): %v", err)
	}
	if !mgr.Status().MDNSActive {
		t.Fatalf("expected Status().MDNSActive=true while enabled")
	}

	// 1. Initial unsolicited announcement must be broadcast immediately.
	deadline := time.Now().Add(time.Second)
	for len(fakeSock.packets()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	pkts := fakeSock.packets()
	if len(pkts) == 0 {
		t.Fatalf("expected initial mDNS announcement packet")
	}
	initPkt := pkts[0].payload
	if !bytes.Contains(initPkt, []byte("_trace-companion")) || !bytes.Contains(initPkt, []byte("node="+mgr.Store().NodeID())) || !bytes.Contains(initPkt, []byte("channel=dev")) || !bytes.Contains(initPkt, []byte("proto=1")) || !bytes.Contains(initPkt, []byte("v=0.2.0-dev")) {
		t.Fatalf("initial mDNS announcement missing expected DNS-SD records: %q", string(initPkt))
	}

	// 2. Incoming PTR query from LAN with QU=1 -> unicast response to requester; query from public IP -> ignored!
	beforeCount := len(fakeSock.packets())
	fakeSock.inject(buildDNSQuery(companionapi.MDNSServiceFull, dnsTypePTR, true), &net.UDPAddr{IP: net.IPv4(8, 8, 8, 8), Port: 5353})
	time.Sleep(40 * time.Millisecond)
	if got := len(fakeSock.packets()); got != beforeCount {
		t.Fatalf("public IP mDNS query must be ignored, got %d new packets", got-beforeCount)
	}

	lanSrc := &net.UDPAddr{IP: net.IPv4(192, 168, 1, 50), Port: 54321}
	fakeSock.inject(buildDNSQuery(companionapi.MDNSServiceFull, dnsTypePTR, true), lanSrc)
	deadline = time.Now().Add(time.Second)
	for len(fakeSock.packets()) <= beforeCount && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	pkts = fakeSock.packets()
	if len(pkts) <= beforeCount {
		t.Fatalf("expected unicast response to LAN PTR query")
	}
	reply := pkts[len(pkts)-1]
	if reply.dst.String() != lanSrc.String() {
		t.Fatalf("QU unicast reply dst = %v, want %v", reply.dst, lanSrc)
	}
	if !bytes.Contains(reply.payload, []byte{192, 168, 1, 25}) {
		t.Fatalf("PTR response missing LAN A record 192.168.1.25")
	}

	// 3. Disabling companion sends TTL=0 goodbye packet and marks MDNSActive=false.
	beforeDisable := len(fakeSock.packets())
	if err := mgr.ApplySettings(false, port); err != nil {
		t.Fatalf("ApplySettings(false): %v", err)
	}
	if mgr.Status().MDNSActive {
		t.Fatalf("expected MDNSActive=false after disable")
	}
	pkts = fakeSock.packets()
	if len(pkts) <= beforeDisable {
		t.Fatalf("expected TTL=0 goodbye packet on disable")
	}
	goodbye := pkts[len(pkts)-1].payload
	// Verify goodbye packet has TTL=0 on first answer RR.
	if !hasZeroTTLAnswer(goodbye) {
		t.Fatalf("goodbye packet did not carry TTL=0 on answer RR")
	}

	// 4. Security sentinel check across ALL emitted mDNS packets.
	for i, p := range pkts {
		raw := string(p.payload)
		assertNoLeaks(t, raw, sentinelToken, sentinelCookie, sentinelCode, root, "Bearer", "SESSDATA")
		if bytes.Contains(p.payload, []byte{8, 8, 8, 8}) {
			t.Fatalf("packet %d leaked public IP", i)
		}
	}
}

func hasZeroTTLAnswer(pkt []byte) bool {
	if len(pkt) < 12 {
		return false
	}
	anCount := int(binary.BigEndian.Uint16(pkt[6:8]))
	if anCount == 0 {
		return false
	}
	_, off, err := decodeDNSName(pkt, 12)
	if err != nil || off+10 > len(pkt) {
		return false
	}
	ttl := binary.BigEndian.Uint32(pkt[off+4 : off+8])
	return ttl == 0
}

// ---- Test 2: Prevent Sleep State Machine (Mocked Inhibitor, Zero OS Calls) ----

func TestSleepInhibitorLifecycleAndClientTransitions(t *testing.T) {
	root := t.TempDir()
	backend := newTestCompanionBackend(t, true)
	defer backend.Close()

	inhib := &mockSleepInhibitor{}
	mgr, err := NewManager(ManagerOptions{
		DataDir:            root,
		Channel:            companionapi.ChannelDev,
		NodeName:           "SleepPC",
		Version:            "0.2.0-dev",
		Backend:            backend,
		ListenOverrides:    []string{"127.0.0.1"},
		AdvertiseOverrides: []string{"127.0.0.1"},
		SleepInhibitor:     inhib,
	})
	if err != nil {
		t.Fatal(err)
	}

	port := pickFreeLoopbackPort(t)
	if err := mgr.ApplySettings(true, port); err != nil {
		t.Fatalf("ApplySettings(true): %v", err)
	}
	_, token, err := mgr.Store().AddDevice("Phone", "Pixel", "android", companionapi.ChannelDev, "127.0.0.1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	fp := mgr.Status().Fingerprint
	phone := newPinnedPhoneClient(fp, fmt.Sprintf("https://127.0.0.1:%d", port))

	waitSleep := func(want bool) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			active, _, _ := inhib.snapshot()
			if active == want && mgr.Status().SleepInhibited == want {
				return
			}
			time.Sleep(15 * time.Millisecond)
		}
		active, _, hist := inhib.snapshot()
		t.Fatalf("sleep active = %v (status=%v), want %v; history=%v", active, mgr.Status().SleepInhibited, want, hist)
	}

	// 1. Default preventSleep=false: connecting a client does NOT inhibit sleep.
	if mgr.Status().PreventSleep {
		t.Fatalf("preventSleep must default to false")
	}
	ws1 := phone.dialWS(t, companionapi.PathEvents, token)
	time.Sleep(60 * time.Millisecond)
	waitSleep(false)

	// 2. Enabling preventSleep while ws1 is online activates sleep inhibition immediately.
	mgr.SetPreventSleep(true)
	waitSleep(true)

	// 3. Connecting ws2 keeps sleep active without toggling off.
	ws2 := phone.dialWS(t, companionapi.PathEvents, token)
	time.Sleep(60 * time.Millisecond)
	waitSleep(true)

	// 4. Disconnecting ws1 while ws2 remains online keeps sleep active.
	_ = ws1.Close()
	time.Sleep(80 * time.Millisecond)
	waitSleep(true)

	// 5. Disconnecting the last client (ws2) clears sleep inhibition immediately.
	_ = ws2.Close()
	waitSleep(false)

	// 6. With 0 clients connected and preventSleep=true, sleep stays inactive.
	time.Sleep(40 * time.Millisecond)
	waitSleep(false)

	// 7. Connect ws3 -> activates; toggle preventSleep=false -> immediately clears!
	ws3 := phone.dialWS(t, companionapi.PathEvents, token)
	waitSleep(true)
	mgr.SetPreventSleep(false)
	waitSleep(false)

	// 8. Re-enable preventSleep -> activates; disable companion -> immediately clears!
	mgr.SetPreventSleep(true)
	waitSleep(true)
	if err := mgr.ApplySettings(false, port); err != nil {
		t.Fatal(err)
	}
	_ = ws3.Close()
	waitSleep(false)

	// 9. Re-enable + connect ws4 -> activates; Shutdown clears and closes inhibitor!
	if err := mgr.ApplySettings(true, port); err != nil {
		t.Fatal(err)
	}
	ws4 := phone.dialWS(t, companionapi.PathEvents, token)
	defer ws4.Close()
	waitSleep(true)

	mgr.Shutdown(companionapi.ByeReasonExit)
	active, closed, _ := inhib.snapshot()
	if active || !closed {
		t.Fatalf("after Shutdown: active=%v closed=%v, want false, true", active, closed)
	}
}

// ---- Test 3: Live Recording Real-Time Preview (Range, HLS, Stream, Windows Lock-Free Rename) ----

func TestLiveRecordingPreviewRangeHLSStreamAndLockFreeRename(t *testing.T) {
	root := t.TempDir()
	baseBackend := newTestCompanionBackend(t, true)
	defer baseBackend.Close()

	wrapped := &livePreviewBackendWrapper{BackendAPI: baseBackend}
	itemDir := filepath.Join(root, "live_item")
	if err := os.MkdirAll(itemDir, 0o700); err != nil {
		t.Fatal(err)
	}
	partPath := filepath.Join(itemDir, "part-001.mp4.part")
	initBytes, frags := buildSyntheticFMP4(3)
	// Write init + 2 complete fragments + 1 incomplete trailing moof header (must NOT be served!).
	incompleteTail := makeMP4Box("moof", bytes.Repeat([]byte{0x99}, 50))[:18]
	committedTwo := append(append([]byte(nil), initBytes...), frags[0]...)
	committedTwo = append(committedTwo, frags[1]...)
	diskInitial := append(append([]byte(nil), committedTwo...), incompleteTail...)
	if err := os.WriteFile(partPath, diskInitial, 0o600); err != nil {
		t.Fatal(err)
	}

	const recKey = "bilibili:1001:223344"
	acct := service.LiveAccountState{
		AccountKey:   "bilibili:1001",
		Site:         "bilibili",
		UID:          "1001",
		RoomID:       "223344",
		Uname:        "LiveHost",
		Title:        "实时预览测试直播",
		Recording:    true,
		RecordingKey: recKey,
		RecordBytes:  int64(len(committedTwo)),
	}
	prevInfo := service.LiveRecordingPreviewInfo{
		RecordingKey:     recKey,
		Site:             "bilibili",
		AccountUID:       "1001",
		RoomID:           "223344",
		Uname:            "LiveHost",
		Title:            "实时预览测试直播",
		Recording:        true,
		PartPath:         partPath,
		PartIndex:        1,
		PartBytes:        int64(len(diskInitial)),
		PartDurationSec:  4.0,
		PartReady:        true,
		PreviewSupported: true,
		ItemDir:          itemDir,
	}
	wrapped.setPreview(recKey, acct, prevInfo)

	// Also add an audio-only recording that must report PreviewAvailable=false & 400 unsupported.
	const audioKey = "bilibili:1002:556677"
	wrapped.setPreview(audioKey, service.LiveAccountState{
		AccountKey:   "bilibili:1002",
		Site:         "bilibili",
		UID:          "1002",
		RoomID:       "556677",
		Uname:        "AudioHost",
		Recording:    true,
		RecordingKey: audioKey,
		AudioOnly:    true,
	}, service.LiveRecordingPreviewInfo{
		RecordingKey:     audioKey,
		Recording:        true,
		AudioOnly:        true,
		PreviewSupported: false,
		PreviewReason:    "这场录制暂不支持实时预览",
	})

	mgr, err := NewManager(ManagerOptions{
		DataDir:            filepath.Join(root, "data"),
		Channel:            companionapi.ChannelDev,
		NodeName:           "LivePC",
		Version:            "0.2.0-dev",
		Backend:            wrapped,
		ListenOverrides:    []string{"127.0.0.1"},
		AdvertiseOverrides: []string{"127.0.0.1"},
		SleepInhibitor:     &mockSleepInhibitor{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Shutdown(companionapi.ByeReasonExit)

	port := pickFreeLoopbackPort(t)
	if err := mgr.ApplySettings(true, port); err != nil {
		t.Fatal(err)
	}
	_, token, err := mgr.Store().AddDevice("Phone", "Pixel", "android", companionapi.ChannelDev, "127.0.0.1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	fp := mgr.Status().Fingerprint
	phone := newPinnedPhoneClient(fp, fmt.Sprintf("https://127.0.0.1:%d", port))

	// 1. GET /api/v1/live/recordings populates PreviewAvailable / PreviewReason / PartIndex.
	var recs companionapi.LiveRecordingsResponse
	code, rawRecs := phone.doJSON(t, http.MethodGet, companionapi.PathLiveRecordings, token, nil, nil, &recs)
	if code != http.StatusOK || len(recs.Items) != 2 {
		t.Fatalf("GET /live/recordings = %d body=%s", code, rawRecs)
	}
	assertNoLeaks(t, rawRecs, root, partPath)
	byKey := map[string]companionapi.LiveRecordingDTO{}
	for _, it := range recs.Items {
		byKey[it.RecordingKey] = it
	}
	if !byKey[recKey].PreviewAvailable || byKey[recKey].PartIndex != 1 {
		t.Fatalf("video recording preview metadata mismatch: %+v", byKey[recKey])
	}
	if byKey[audioKey].PreviewAvailable || byKey[audioKey].PreviewReason == "" {
		t.Fatalf("audio-only recording should not be previewable: %+v", byKey[audioKey])
	}

	// 2. GET preview returns strictly complete fMP4 boxes (excludes incomplete trailing moof).
	respFull, bodyFull := phone.doRaw(t, http.MethodGet, companionapi.LiveRecordingPreviewPath(recKey), token, nil)
	if respFull.StatusCode != http.StatusOK {
		t.Fatalf("GET preview status = %d body=%s", respFull.StatusCode, string(bodyFull))
	}
	if respFull.Header.Get("Content-Type") != "video/mp4" || respFull.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected headers: ct=%q cc=%q", respFull.Header.Get("Content-Type"), respFull.Header.Get("Cache-Control"))
	}
	if respFull.Header.Get(companionapi.HeaderLivePart) != "1" {
		t.Fatalf("X-Trace-Live-Part = %q, want 1", respFull.Header.Get(companionapi.HeaderLivePart))
	}
	if !bytes.Equal(bodyFull, committedTwo) {
		t.Fatalf("preview len = %d, want committedTwo len %d", len(bodyFull), len(committedTwo))
	}

	// 3. Range 206 request works and ?part=99 returns 409 conflict.
	respRng, bodyRng := phone.doRaw(t, http.MethodGet, companionapi.LiveRecordingPreviewPath(recKey)+"?part=1", token, map[string]string{
		"Range": "bytes=0-15",
	})
	if respRng.StatusCode != http.StatusPartialContent || !bytes.Equal(bodyRng, committedTwo[:16]) {
		t.Fatalf("Range preview = (%d, %d bytes)", respRng.StatusCode, len(bodyRng))
	}
	respStale, _ := phone.doRaw(t, http.MethodGet, companionapi.LiveRecordingPreviewPath(recKey)+"?part=99", token, nil)
	if respStale.StatusCode != http.StatusConflict {
		t.Fatalf("stale ?part=99 status = %d, want 409", respStale.StatusCode)
	}

	// 4. HLS playlist (.m3u8) returns RFC 8216 v7 map + 2 byte-range segments and no ENDLIST.
	respHLS, bodyHLS := phone.doRaw(t, http.MethodGet, companionapi.LiveRecordingPreviewHLSPath(recKey), token, nil)
	if respHLS.StatusCode != http.StatusOK {
		t.Fatalf("GET preview.m3u8 = %d body=%s", respHLS.StatusCode, string(bodyHLS))
	}
	hlsText := string(bodyHLS)
	if !strings.Contains(hlsText, "#EXT-X-VERSION:7") ||
		!strings.Contains(hlsText, fmt.Sprintf(`#EXT-X-MAP:URI="preview?part=1",BYTERANGE="%d@0"`, len(initBytes))) ||
		strings.Count(hlsText, "#EXT-X-BYTERANGE:") != 2 ||
		strings.Contains(hlsText, "#EXT-X-ENDLIST") {
		t.Fatalf("unexpected HLS playlist:\n%s", hlsText)
	}

	// 5. Audio-only recording preview rejected with 400 unsupported.
	respAudio, rawAudio := phone.doRaw(t, http.MethodGet, companionapi.LiveRecordingPreviewPath(audioKey), token, nil)
	if respAudio.StatusCode != http.StatusBadRequest {
		t.Fatalf("audio-only preview status = %d, want 400; body=%s", respAudio.StatusCode, string(rawAudio))
	}

	// 6. ?mode=stream tails newly appended fragment AND never blocks Windows os.Rename!
	streamCtx, cancelStream := context.WithCancel(context.Background())
	streamReq, _ := http.NewRequestWithContext(streamCtx, http.MethodGet, phone.baseURL+companionapi.LiveRecordingPreviewPath(recKey)+"?mode=stream", nil)
	streamReq.Header.Set("Authorization", "Bearer "+token)
	streamReq.Header.Set(companionapi.HeaderProtocol, companionapi.ProtocolVersion)
	streamReq.Header.Set(companionapi.HeaderChannel, companionapi.ChannelDev)
	streamResp, err := phone.httpCli.Do(streamReq)
	if err != nil {
		t.Fatalf("GET preview?mode=stream: %v", err)
	}
	gotInitial := make([]byte, len(committedTwo))
	if _, err := io.ReadFull(streamResp.Body, gotInitial); err != nil {
		streamResp.Body.Close()
		t.Fatalf("read initial stream bytes: %v", err)
	}
	if !bytes.Equal(gotInitial, committedTwo) {
		streamResp.Body.Close()
		t.Fatalf("initial stream bytes mismatch")
	}

	// Append fragment #3 (overwriting incompleteTail cleanly as a recorder flush does).
	committedThree := append(append([]byte(nil), committedTwo...), frags[2]...)
	if err := os.WriteFile(partPath, committedThree, 0o600); err != nil {
		streamResp.Body.Close()
		t.Fatalf("write frag 3 while streaming: %v", err)
	}
	prevInfo.PartBytes = int64(len(committedThree))
	wrapped.setPreview(recKey, acct, prevInfo)

	gotFrag3 := make([]byte, len(frags[2]))
	if _, err := io.ReadFull(streamResp.Body, gotFrag3); err != nil {
		streamResp.Body.Close()
		t.Fatalf("read appended frag 3 from stream: %v", err)
	}
	if !bytes.Equal(gotFrag3, frags[2]) {
		streamResp.Body.Close()
		t.Fatalf("appended frag 3 mismatch")
	}

	// CRITICAL Windows lock test: while stream is open/ending, finalize via os.Rename MUST succeed!
	finalPath := filepath.Join(itemDir, "part-001.mp4")
	prevInfo.Recording = false
	acct.Recording = false
	wrapped.setPreview(recKey, acct, prevInfo)
	if err := os.Rename(partPath, finalPath); err != nil {
		cancelStream()
		streamResp.Body.Close()
		t.Fatalf("os.Rename(.part -> .mp4) failed with sharing lock while/after previewing: %v", err)
	}
	cancelStream()
	_ = streamResp.Body.Close()

	// After recording ends, preview endpoint returns 404 without leaking local path.
	respEnded, rawEnded := phone.doRaw(t, http.MethodGet, companionapi.LiveRecordingPreviewPath(recKey), token, nil)
	if respEnded.StatusCode != http.StatusNotFound {
		t.Fatalf("ended recording preview = %d, want 404", respEnded.StatusCode)
	}
	assertNoLeaks(t, string(rawEnded), root, partPath, finalPath)
}

// ---- Test 4: Full Loopback End-to-End Integration with companionclient + MediaProxy for All V2 Features ----

type v2SynthBiliRT struct {
	userCardCalls atomic.Int32
}

func (rt *v2SynthBiliRT) RoundTrip(req *http.Request) (*http.Response, error) {
	path := req.URL.Path
	switch {
	case strings.Contains(path, "/x/web-interface/nav"):
		return jsonHTTPResp(req, `{"code":0,"message":"0","data":{"isLogin":true,"mid":10001,"uname":"V2测试用户","face":"","wbi_img":{"img_url":"https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png","sub_url":"https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png"}}}`), nil
	case strings.Contains(path, "/x/web-interface/wbi/view"), strings.Contains(path, "/x/web-interface/view"):
		bvid := req.URL.Query().Get("bvid")
		if bvid == "BV1ab411c7mD" {
			return jsonHTTPResp(req, `{"code":0,"data":{"bvid":"BV1ab411c7mD","aid":111,"title":"已缓存测试视频","desc":"desc","pic":"","pubdate":1732600000,"duration":60,"owner":{"mid":1001,"name":"UP_V2","face":""},"pages":[{"cid":101,"page":1,"part":"P1","duration":60}]}}`), nil
		}
		if bvid == "BV1cd411c7mE" {
			return jsonHTTPResp(req, `{"code":0,"data":{"bvid":"BV1cd411c7mE","aid":222,"title":"手机分享的新视频","desc":"new desc","pic":"https://i0.hdslb.com/bfs/secret_signed_cover.jpg?auth=SENTINEL_SIGNED_URL","pubdate":1732601234,"duration":95,"owner":{"mid":1001,"name":"UP_V2","face":""},"pages":[{"cid":202,"page":1,"part":"第一集","duration":95}]}}`), nil
		}
		return jsonHTTPResp(req, `{"code":-404,"message":"啥都木有"}`), nil
	case strings.Contains(path, "/x/space/wbi/acc/info"), strings.Contains(path, "/x/web-interface/card"):
		rt.userCardCalls.Add(1)
		return jsonHTTPResp(req, `{"code":0,"data":{"mid":1001,"name":"UP_V2"}}`), nil
	default:
		return jsonHTTPResp(req, `{"code":0,"data":{}}`), nil
	}
}

func jsonHTTPResp(req *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func TestV2LoopbackClientAndProxyEndToEnd(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	cacheDir := filepath.Join(root, "cache")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}

	const (
		sentinelCookie    = "SENTINEL_V2_SESSDATA_COOKIE_777"
		sentinelSignedURL = "SENTINEL_SIGNED_URL"
		conflictID        = "cf-v2-loopback-1"
	)

	// Pre-seed cache-conflicts.json before NewBackend so ConflictService loads a real pending conflict.
	seedConflict := map[string]any{
		"version": 1,
		"conflicts": []map[string]any{{
			"id":                conflictID,
			"itemKey":           "bilibili:video:BV1ab411c7mD",
			"site":              "bilibili",
			"kind":              "video",
			"targetId":          "BV1ab411c7mD",
			"authorUid":         "1001",
			"authorName":        "UP_V2",
			"title":             "已缓存测试视频",
			"detectedAt":        time.Now().UTC().Format(time.RFC3339),
			"reasonKind":        service.ConflictReasonParts,
			"localSummary":      "本地 1P",
			"remoteSummary":     "源站 2P",
			"remoteFingerprint": "fp_v2_test_rev2",
			"state":             service.ConflictStatePending,
		}},
	}
	rawSeed, _ := json.Marshal(seedConflict)
	if err := os.WriteFile(filepath.Join(dataDir, "cache-conflicts.json"), rawSeed, 0o600); err != nil {
		t.Fatal(err)
	}

	biliRT := &v2SynthBiliRT{}
	loc := appdirs.Locations{
		Mode:            "portable",
		Channel:         "dev",
		AppName:         "trace-dev",
		DisplayName:     "TRACE Dev",
		Identifier:      "io.github.chialecode.trace-dev",
		DataDir:         dataDir,
		DefaultCacheDir: cacheDir,
		CredentialsDir:  filepath.Join(dataDir, "credentials"),
		LogsDir:         filepath.Join(dataDir, "logs"),
		TempDir:         filepath.Join(root, "tmp"),
		LockFile:        filepath.Join(dataDir, "app.lock"),
	}
	backend, err := service.NewBackend(service.Options{
		Locations:        loc,
		DisableBgWorkers: true,
		NodeName:         "V2Desktop",
		BilibiliOpts: bilibili.ClientOptions{
			WrapTransport: func(http.RoundTripper) http.RoundTripper { return biliRT },
		},
	})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	defer backend.Close()

	if _, err := backend.ImportCookie(context.Background(), "SESSDATA="+sentinelCookie+"; bili_jct=csrf1; DedeUserID=10001", ""); err != nil {
		t.Fatalf("ImportCookie: %v", err)
	}

	sub, err := backend.CreateSubscription("V2订阅", "", []subscription.AccountRef{
		{Site: "bilibili", UID: "1001", Name: "UP_V2"},
	}, subscription.SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}

	// Seed cached video with danmaku XML, SubtitleTracks (VTT + SRT ONLY via SubtitleTracks!), and Chapters.
	vDir := filepath.Join(cacheDir, "bilibili", "1001", "videos", "BV1ab411c7mD")
	_ = os.MkdirAll(vDir, 0o700)
	videoBytes := bytes.Repeat([]byte("VID-V2-"), 64)
	danmakuBytes := []byte(`<?xml version="1.0" encoding="UTF-8"?><i><d p="1.5,1,25,16777215,0,0,0,0">第一条弹幕</d></i>`)
	vttBytes := []byte("WEBVTT\n\n00:00:01.000 --> 00:00:03.000\n你好世界\n")
	srtBytes := []byte("1\n00:00:01,000 --> 00:00:03,000\n你好世界\n")
	_ = os.WriteFile(filepath.Join(vDir, "P01.mp4"), videoBytes, 0o600)
	_ = os.WriteFile(filepath.Join(vDir, "P01.danmaku.xml"), danmakuBytes, 0o600)
	_ = os.WriteFile(filepath.Join(vDir, "P01.zh-CN.vtt"), vttBytes, 0o600)
	_ = os.WriteFile(filepath.Join(vDir, "P01.zh-CN.srt"), srtBytes, 0o600)
	_ = os.WriteFile(filepath.Join(vDir, "unregistered.xml"), []byte("<secret/>"), 0o600)

	cachedMeta, err := backend.LibraryStore().SaveItem(vDir, library.ItemMetadata{
		Version:  1,
		ID:       "BV1ab411c7mD",
		Site:     "bilibili",
		Kind:     "video",
		Status:   "complete",
		Title:    "已缓存测试视频",
		URL:      "https://www.bilibili.com/video/BV1ab411c7mD",
		PubTs:    1732600000,
		Duration: 60,
		Author:   library.ItemAuthor{Site: "bilibili", UID: "1001", Mid: 1001, Name: "UP_V2"},
		Media: []library.MediaFile{
			{Role: "video", Path: "P01.mp4", Bytes: int64(len(videoBytes)), SHA256: hexSHA256(videoBytes), Duration: 60},
		},
		Parts: []library.VideoPartRecord{{
			CID:      101,
			Page:     1,
			Title:    "P1",
			Duration: 60,
			File:     "P01.mp4",
			Danmaku:  "P01.danmaku.xml",
			Video:    library.PartVideoInfo{Quality: 80, QualityName: "1080P", Codecs: "avc1"},
			SubtitleTracks: []library.PartSubtitleInfo{{
				Language: "zh-CN",
				Name:     "中文（中国）",
				AI:       false,
				VTT:      "P01.zh-CN.vtt",
				SRT:      "P01.zh-CN.srt",
			}},
			Chapters: []library.VideoChapter{
				{FromSec: 0, ToSec: 20, Title: "开场"},
				{FromSec: 20, ToSec: 60, Title: "演示"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("SaveItem: %v", err)
	}

	// Wrap backend with synthetic live preview for MediaProxy loopback verification.
	wrapped := &livePreviewBackendWrapper{BackendAPI: backend}
	liveDir := filepath.Join(root, "live_v2")
	_ = os.MkdirAll(liveDir, 0o700)
	partPath := filepath.Join(liveDir, "part-001.mp4.part")
	initB, frags := buildSyntheticFMP4(2)
	livePayload := append(append(append([]byte(nil), initB...), frags[0]...), frags[1]...)
	_ = os.WriteFile(partPath, livePayload, 0o600)
	const liveRecKey = "bilibili:1001:888999"
	wrapped.setPreview(liveRecKey, service.LiveAccountState{
		AccountKey: "bilibili:1001", Site: "bilibili", UID: "1001", RoomID: "888999",
		Uname: "UP_V2", Title: "V2实时预览", Recording: true, RecordingKey: liveRecKey,
	}, service.LiveRecordingPreviewInfo{
		RecordingKey: liveRecKey, Recording: true, PartPath: partPath, PartIndex: 1,
		PartBytes: int64(len(livePayload)), PartDurationSec: 4.0, PartReady: true,
		PreviewSupported: true, ItemDir: liveDir,
	})

	var logMu sync.Mutex
	var logBuf bytes.Buffer
	inhib := &mockSleepInhibitor{}
	mgr, err := NewManager(ManagerOptions{
		DataDir:            dataDir,
		Channel:            companionapi.ChannelDev,
		NodeName:           "V2Desktop",
		Version:            "0.2.0-dev",
		Backend:            wrapped,
		ListenOverrides:    []string{"127.0.0.1"},
		AdvertiseOverrides: []string{"127.0.0.1"},
		SleepInhibitor:     inhib,
		Logf: func(f string, a ...any) {
			logMu.Lock()
			defer logMu.Unlock()
			fmt.Fprintf(&logBuf, f+"\n", a...)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Shutdown(companionapi.ByeReasonExit)

	port := pickFreeLoopbackPort(t)
	mgr.SetPreventSleep(true)
	if err := mgr.ApplySettings(true, port); err != nil {
		t.Fatal(err)
	}
	_, token, err := mgr.Store().AddDevice("PhoneV2", "Pixel 9", "android", companionapi.ChannelDev, "127.0.0.1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	cli, err := companionclient.NewClient(companionclient.PairingRecord{
		Node:     mgr.Store().NodeID(),
		Name:     "V2Desktop",
		Channel:  companionapi.ChannelDev,
		FP:       mgr.Status().Fingerprint,
		Addrs:    []string{addr},
		LastAddr: addr,
	}, token, "0.2.0-dev", companionapi.ChannelDev)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	ctx := context.Background()

	// 1. ConnectStatus advertises all v2 capabilities and preventSleep=true.
	st, _, err := cli.ConnectStatus(ctx)
	if err != nil {
		t.Fatalf("ConnectStatus: %v", err)
	}
	for _, capName := range companionapi.V2Capabilities() {
		if !slices.Contains(st.Capabilities, capName) {
			t.Fatalf("Status missing capability %q: %v", capName, st.Capabilities)
		}
	}
	if !st.PreventSleep {
		t.Fatalf("expected Status.PreventSleep=true")
	}

	// Open real WebSocket event stream (also activates sleep inhibitor!).
	wsConn, err := cli.DialEvents(ctx, 5*time.Second)
	if err != nil {
		t.Fatalf("DialEvents: %v", err)
	}
	defer wsConn.Close()
	evCh := make(chan companionapi.EventFrame, 32)
	go func() {
		for {
			ev, rErr := wsConn.ReadEvent(ctx)
			if rErr != nil {
				return
			}
			evCh <- ev
		}
	}()
	waitForEvent := func(wantType string) {
		t.Helper()
		deadline := time.After(2 * time.Second)
		for {
			select {
			case ev := <-evCh:
				if ev.Type == wantType {
					return
				}
			case <-deadline:
				t.Fatalf("timed out waiting for WS event %q", wantType)
			}
		}
	}

	// 2. Parse & Cache via companionclient:
	// (a) New Bilibili video -> enqueues manual task without leaking remote cover URL or cookie.
	pcResp, err := cli.ParseAndCache(ctx, "快看这个视频 https://www.bilibili.com/video/BV1cd411c7mE 很好看")
	if err != nil {
		t.Fatalf("ParseAndCache(new video): %v", err)
	}
	rawPC, _ := json.Marshal(pcResp)
	if !pcResp.OK || pcResp.Site != "bilibili" || pcResp.Kind != "video" || pcResp.TaskID == "" || pcResp.AlreadyCached {
		t.Fatalf("unexpected ParseAndCache response: %s", rawPC)
	}
	assertNoLeaks(t, string(rawPC), sentinelCookie, sentinelSignedURL, token, root)

	// (b) Already cached video -> AlreadyCached=true.
	pcCached, err := cli.ParseAndCache(ctx, "https://www.bilibili.com/video/BV1ab411c7mD")
	if err != nil || !pcCached.AlreadyCached || pcCached.ItemKey != cachedMeta.ItemKey {
		t.Fatalf("ParseAndCache(cached): resp=%+v err=%v", pcCached, err)
	}

	// (c) User space link -> rejected as unsupported with 0 UserCard requests!
	var ce *companionclient.Error
	if _, err := cli.ParseAndCache(ctx, "https://space.bilibili.com/1001"); !errors.As(err, &ce) || ce.Code != companionapi.CodeUnsupported {
		t.Fatalf("ParseAndCache(user) err = %+v, want code=%q", err, companionapi.CodeUnsupported)
	}
	if biliRT.userCardCalls.Load() != 0 {
		t.Fatalf("ParseAndCache(user) unexpectedly called UserCard %d times", biliRT.userCardCalls.Load())
	}

	// (d) Unauthenticated site (Douyin) -> auth_required with Site="douyin".
	_, err = cli.ParseAndCache(ctx, "https://www.douyin.com/video/7123456789012345678")
	ce = nil
	if !errors.As(err, &ce) || ce.Code != companionapi.CodeAuthRequired || ce.Site != "douyin" {
		t.Fatalf("ParseAndCache(unauth douyin) err = %+v, want code=%q site=douyin", err, companionapi.CodeAuthRequired)
	}

	// 3. Subscription tags update + WS subs:changed.
	autoRec := true
	subDet, err := cli.UpdateSubscriptionTags(ctx, sub.ID, companionapi.UpdateSubscriptionTagsRequest{
		Preset:     companionapi.PresetFullArchive,
		AutoRecord: &autoRec,
	})
	if err != nil {
		t.Fatalf("UpdateSubscriptionTags(preset): %v", err)
	}
	if subDet.Subscription.PresetID != companionapi.PresetFullArchive || !slices.Contains(subDet.Subscription.Tags, companionapi.TagAutoRecord) {
		t.Fatalf("unexpected updated sub: %+v", subDet.Subscription)
	}
	waitForEvent(companionapi.EventSubsChanged)

	// Clear to manual via explicit empty slice []string{}.
	subMan, err := cli.UpdateSubscriptionTags(ctx, sub.ID, companionapi.UpdateSubscriptionTagsRequest{
		Tags: []string{},
	})
	if err != nil {
		t.Fatalf("UpdateSubscriptionTags(empty): %v", err)
	}
	if subMan.Subscription.PresetID != companionapi.PresetManual || len(subMan.Subscription.Tags) != 0 {
		t.Fatalf("expected manual preset with 0 tags, got %+v", subMan.Subscription)
	}

	// 4. Content conflict resolution (keep_local) + WS conflicts:changed.
	if backend.PendingCacheConflictCount() != 1 {
		t.Fatalf("expected 1 seeded conflict, got %d", backend.PendingCacheConflictCount())
	}
	resCF, err := cli.ResolveConflict(ctx, conflictID, companionapi.ConflictKeepLocal)
	if err != nil || !resCF.OK || resCF.Choice != companionapi.ConflictKeepLocal {
		t.Fatalf("ResolveConflict(keep_local) = (%+v, %v)", resCF, err)
	}
	if backend.PendingCacheConflictCount() != 0 {
		t.Fatalf("conflict still pending after keep_local")
	}
	waitForEvent(companionapi.EventConflictsChanged)

	// 5. Playback extras: chapters, danmaku XML, WebVTT & SRT subtitles (via MediaPath whitelist).
	var itemDet companionapi.LibraryItemDetail
	if err := cli.DoJSON(ctx, http.MethodGet, companionapi.LibraryItemPath(cachedMeta.ItemKey), nil, &itemDet); err != nil {
		t.Fatalf("GET item detail: %v", err)
	}
	if len(itemDet.Parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(itemDet.Parts))
	}
	p0 := itemDet.Parts[0]
	if p0.DanmakuRelPath != "P01.danmaku.xml" || len(p0.Subtitles) != 1 || p0.Subtitles[0].RelPath != "P01.zh-CN.vtt" || p0.Subtitles[0].SRT != "P01.zh-CN.srt" || len(p0.Chapters) != 2 {
		t.Fatalf("unexpected playback extras on VideoPartDTO: %+v", p0)
	}
	gotDM, err := cli.FetchDanmaku(ctx, cachedMeta.ItemKey, p0.DanmakuRelPath)
	if err != nil || !bytes.Equal(gotDM, danmakuBytes) {
		t.Fatalf("FetchDanmaku = (%q, %v)", string(gotDM), err)
	}
	gotVTT, err := cli.FetchSubtitle(ctx, cachedMeta.ItemKey, p0.Subtitles[0].RelPath)
	if err != nil || !bytes.Equal(gotVTT, vttBytes) {
		t.Fatalf("FetchSubtitle(vtt) = (%q, %v)", string(gotVTT), err)
	}
	gotSRT, err := cli.FetchSubtitle(ctx, cachedMeta.ItemKey, p0.Subtitles[0].SRT)
	if err != nil || !bytes.Equal(gotSRT, srtBytes) {
		t.Fatalf("FetchSubtitle(srt) = (%q, %v)", string(gotSRT), err)
	}
	// Unregistered XML file must be rejected by MediaPath whitelist!
	if _, err := cli.FetchDanmaku(ctx, cachedMeta.ItemKey, "unregistered.xml"); err == nil {
		t.Fatalf("expected MediaPath whitelist to reject unregistered.xml")
	}

	// 6. MediaProxy live preview loopback tickets (both Range MP4 and HLS .m3u8).
	proxy, err := companionclient.NewMediaProxy(cli.HTTPClient(), func() (string, string, string, string, bool) {
		return cli.ActiveBaseURL(), cli.Token(), cli.AppVersion(), cli.Channel(), true
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	previewURL, ticket1, err := proxy.IssueLivePreviewTicket(liveRecKey)
	if err != nil {
		t.Fatal(err)
	}
	reqP, _ := http.NewRequest(http.MethodGet, previewURL, nil)
	reqP.Header.Set("Range", "bytes=0-31")
	respP, err := http.DefaultClient.Do(reqP)
	if err != nil {
		t.Fatal(err)
	}
	bodyP, _ := io.ReadAll(respP.Body)
	respP.Body.Close()
	if respP.StatusCode != http.StatusPartialContent || !bytes.Equal(bodyP, livePayload[:32]) {
		t.Fatalf("MediaProxy live preview = (%d, %d bytes)", respP.StatusCode, len(bodyP))
	}
	proxy.RevokeTicket(ticket1)

	hlsURL, ticket2, err := proxy.IssueLivePreviewHLSTicket(liveRecKey)
	if err != nil {
		t.Fatal(err)
	}
	respM3U8, err := http.Get(hlsURL)
	if err != nil {
		t.Fatal(err)
	}
	m3u8Bytes, _ := io.ReadAll(respM3U8.Body)
	respM3U8.Body.Close()
	if respM3U8.StatusCode != http.StatusOK || !strings.Contains(string(m3u8Bytes), "preview?part=1&t=") {
		t.Fatalf("MediaProxy HLS m3u8 = (%d, %q)", respM3U8.StatusCode, string(m3u8Bytes))
	}
	proxy.RevokeTicket(ticket2)

	// 7. Verify zero sentinel leaks in logs.
	logMu.Lock()
	logs := logBuf.String()
	logMu.Unlock()
	assertNoLeaks(t, logs, sentinelCookie, sentinelSignedURL, token, root)
}

func TestSanitizePublicErrorAndWriteServiceErrorSentinels(t *testing.T) {
	const fallback = "操作失败，请重试"
	if got := sanitizePublicError("请先在电脑上登录抖音", fallback); got != "请先在电脑上登录抖音" {
		t.Fatalf("safe message altered: %q", got)
	}
	if got := sanitizePublicError("   ", fallback); got != fallback {
		t.Fatalf("empty message = %q, want fallback", got)
	}

	leakyCases := []struct {
		name     string
		raw      string
		sentinel string
	}{
		{"bilibili sessdata", "auth failed SESSDATA=SENTINEL_BILI_999", "SENTINEL_BILI_999"},
		{"bilibili bili_jct", "csrf bili_jct=SENTINEL_JCT_1", "SENTINEL_JCT_1"},
		{"douyin sessionid", "douyin err sessionid=SENTINEL_DY_1", "SENTINEL_DY_1"},
		{"douyin sid_guard", "douyin err sid_guard=SENTINEL_DY_2", "SENTINEL_DY_2"},
		{"douyin msToken", "douyin err msToken=SENTINEL_DY_3", "SENTINEL_DY_3"},
		{"douyin a_bogus", "douyin err a_bogus=SENTINEL_DY_4", "SENTINEL_DY_4"},
		{"weibo sub", "weibo err SUB=SENTINEL_WB_1", "SENTINEL_WB_1"},
		{"weibo subp", "weibo err SUBP=SENTINEL_WB_2", "SENTINEL_WB_2"},
		{"weibo wbpsess", "weibo err WBPSESS=SENTINEL_WB_3", "SENTINEL_WB_3"},
		{"weibo xsrf", "weibo err XSRF-TOKEN=SENTINEL_WB_4", "SENTINEL_WB_4"},
		{"x auth_token", "x err auth_token=SENTINEL_X_1", "SENTINEL_X_1"},
		{"x ct0", "x err ct0=SENTINEL_X_2", "SENTINEL_X_2"},
		{"x kdt", "x err kdt=SENTINEL_X_3", "SENTINEL_X_3"},
		{"bearer header", "upstream Authorization: Bearer SENTINEL_BEARER_1", "SENTINEL_BEARER_1"},
		{"generic token", "failed token=SENTINEL_TOK_1", "SENTINEL_TOK_1"},
		{"generic cookie", "Cookie: foo=SENTINEL_CK_1", "SENTINEL_CK_1"},
		{"signed url", "get https://cdn.example.com/v.mp4?auth=SENTINEL_URL_1 failed", "SENTINEL_URL_1"},
		{"windows drive path", `open C:\Users\alice\AppData\Local\trace\secret.json`, `C:\Users\alice`},
		{"windows unc path", `open \\fileserver\share\trace\cred.dat failed`, `\\fileserver\share`},
		{"unix home path", "open /home/alice/.config/trace/cred.dat failed", "/home/alice"},
	}

	for _, tc := range leakyCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizePublicError(tc.raw, fallback); got != fallback {
				t.Fatalf("sanitizePublicError(%q) = %q, want fallback %q", tc.raw, got, fallback)
			}
			// Also verify writeServiceError redacts *site.Error messages before emitting JSON.
			rec := httptest.NewRecorder()
			writeServiceError(rec, &site.Error{
				Site:    site.Douyin,
				Kind:    site.ErrKindAuthRequired,
				Message: tc.raw,
			})
			body := rec.Body.String()
			if rec.Code != http.StatusPreconditionFailed {
				t.Fatalf("status = %d, want 412; body=%s", rec.Code, body)
			}
			if strings.Contains(body, tc.sentinel) {
				t.Fatalf("writeServiceError leaked sentinel %q in %s", tc.sentinel, body)
			}
		})
	}
}
