package live

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/settings"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/storage"
	"github.com/chialecode/trace/internal/subscription"
)

type fakeDouyinLiveProvider struct {
	mu          sync.Mutex
	authed      bool
	statusFn    func(uid, cachedWebRID string, call int) (site.LiveStatus, error)
	streamFn    func(uid, roomOrWebRID string, codecOrder []string, call int) (site.LiveStreamPlan, error)
	aliveFn     func(roomIDs []string, call int) (map[string]bool, error)
	statusCalls map[string]int
	streamCalls map[string]int
	aliveCalls  int
	reported    []error
}

func newFakeDouyinLiveProvider() *fakeDouyinLiveProvider {
	return &fakeDouyinLiveProvider{
		authed:      true,
		statusCalls: make(map[string]int),
		streamCalls: make(map[string]int),
	}
}

func (p *fakeDouyinLiveProvider) SiteID() site.ID { return site.Douyin }

func (p *fakeDouyinLiveProvider) HasAuth() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.authed
}

func (p *fakeDouyinLiveProvider) ReportAutomaticError(_ context.Context, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reported = append(p.reported, err)
}

func (p *fakeDouyinLiveProvider) CheckLiveStatus(_ context.Context, uid string, cachedWebRID string) (site.LiveStatus, error) {
	p.mu.Lock()
	key := uid
	if key == "" {
		key = "room:" + cachedWebRID
	}
	p.statusCalls[key]++
	call := p.statusCalls[key]
	fn := p.statusFn
	p.mu.Unlock()
	if fn == nil {
		return site.LiveStatus{Site: site.Douyin, AccountUID: uid, WebRID: cachedWebRID, Status: site.LiveStateOffline}, nil
	}
	return fn(uid, cachedWebRID, call)
}

func (p *fakeDouyinLiveProvider) SelectLiveStream(_ context.Context, uid string, roomOrWebRID string, codecOrder []string) (site.LiveStreamPlan, error) {
	p.mu.Lock()
	key := roomOrWebRID
	if key == "" {
		key = uid
	}
	p.streamCalls[key]++
	call := p.streamCalls[key]
	fn := p.streamFn
	p.mu.Unlock()
	if fn == nil {
		return site.LiveStreamPlan{}, &site.Error{Site: site.Douyin, Kind: site.ErrKindUnavailable, Message: "无可用直播流"}
	}
	return fn(uid, roomOrWebRID, codecOrder, call)
}

func (p *fakeDouyinLiveProvider) CheckRoomsAlive(_ context.Context, roomIDs []string) (map[string]bool, error) {
	p.mu.Lock()
	p.aliveCalls++
	call := p.aliveCalls
	fn := p.aliveFn
	p.mu.Unlock()
	if fn == nil {
		out := make(map[string]bool, len(roomIDs))
		for _, r := range roomIDs {
			out[r] = true
		}
		return out, nil
	}
	return fn(roomIDs, call)
}

func (p *fakeDouyinLiveProvider) totalStatusCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	total := 0
	for _, n := range p.statusCalls {
		total += n
	}
	return total
}

func (p *fakeDouyinLiveProvider) statusCallCount(key string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.statusCalls[key]
}

func (p *fakeDouyinLiveProvider) streamCallCount(key string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.streamCalls[key]
}

func buildSyntheticFLVBytes(t *testing.T, variant string, durationFrames int) []byte {
	t.Helper()
	fb := newFLVBuilder(0x05)
	fb.writeOnMetaData()
	fb.writeAACConfig(0, testAACConfig48kStereo)

	switch variant {
	case "avc":
		sps := mustHex(t, testAVCSPSHex)
		pps := mustHex(t, testAVCPPSHex)
		idr := mustHex(t, testAVCSliceHex)
		pSlice := mustHex(t, testAVCPSlice)
		fb.writeClassicVideoConfig(0, flvVideoCodecAVC, buildAVCDecConfBytes(t, sps, pps, 4))
		audioTS := uint32(0)
		for i := 0; i < durationFrames; i++ {
			vTS := uint32(i * 33)
			for audioTS <= vTS {
				fb.writeAACFrame(audioTS, testAACSilentFrame)
				audioTS += 21
			}
			key := i%30 == 0
			nalu := pSlice
			if key {
				nalu = idr
			}
			fb.writeClassicVideoFrame(vTS, flvVideoCodecAVC, key, 0, packLengthPrefixedNALUs(4, nalu))
		}
	case "hevc12":
		vps := mustHex(t, testHEVCVPSHex)
		sps := mustHex(t, testHEVCSPS1Hex)
		pps := mustHex(t, testHEVCPPSHex)
		idr := mustHex(t, testHEVCIDRHex)
		trail := mustHex(t, testHEVCTrailHex)
		fb.writeClassicVideoConfig(0, flvVideoCodecHEVC, buildHEVCDecConfBytes(t, vps, sps, pps, 4))
		audioTS := uint32(0)
		for i := 0; i < durationFrames; i++ {
			vTS := uint32(i * 33)
			for audioTS <= vTS {
				fb.writeAACFrame(audioTS, testAACSilentFrame)
				audioTS += 21
			}
			key := i%30 == 0
			nalu := trail
			if key {
				nalu = idr
			}
			fb.writeClassicVideoFrame(vTS, flvVideoCodecHEVC, key, 0, packLengthPrefixedNALUs(4, nalu))
		}
	case "enhanced_hvc1":
		vps := mustHex(t, testHEVCVPSHex)
		sps := mustHex(t, testHEVCSPS1Hex)
		pps := mustHex(t, testHEVCPPSHex)
		idr := mustHex(t, testHEVCIDRHex)
		trail := mustHex(t, testHEVCTrailHex)
		fb.writeEnhancedVideo(0, 1, flvExPacketSequenceStart, "hvc1", buildHEVCDecConfBytes(t, vps, sps, pps, 4))
		audioTS := uint32(0)
		for i := 0; i < durationFrames; i++ {
			vTS := uint32(i * 33)
			for audioTS <= vTS {
				fb.writeAACFrame(audioTS, testAACSilentFrame)
				audioTS += 21
			}
			key := i%30 == 0
			ft := uint8(2)
			nalu := trail
			if key {
				ft = 1
				nalu = idr
			}
			fb.writeEnhancedVideo(vTS, ft, flvExPacketCodedFramesX, "hvc1", packLengthPrefixedNALUs(4, nalu))
		}
		fb.writeEnhancedVideo(uint32(durationFrames*33), 1, flvExPacketSequenceEnd, "hvc1", nil)
	default:
		t.Fatalf("unknown FLV variant %q", variant)
	}
	return fb.buf.Bytes()
}

func buildSyntheticParamChangeFLVBytes(t *testing.T) []byte {
	t.Helper()
	vps := mustHex(t, testHEVCVPSHex)
	sps1 := mustHex(t, testHEVCSPS1Hex) // 640x360
	sps2 := mustHex(t, testHEVCSPS2Hex) // 960x540
	pps := mustHex(t, testHEVCPPSHex)
	idr := mustHex(t, testHEVCIDRHex)
	trail := mustHex(t, testHEVCTrailHex)

	fb := newFLVBuilder(0x05)
	fb.writeAACConfig(0, testAACConfig48kStereo)
	fb.writeClassicVideoConfig(0, flvVideoCodecHEVC, buildHEVCDecConfBytes(t, vps, sps1, pps, 4))

	audioTS := uint32(0)
	// First 45 frames (~1.5s) at 640x360
	for i := 0; i < 45; i++ {
		vTS := uint32(i * 33)
		for audioTS <= vTS {
			fb.writeAACFrame(audioTS, testAACSilentFrame)
			audioTS += 21
		}
		key := i%30 == 0
		nalu := trail
		if key {
			nalu = idr
		}
		fb.writeClassicVideoFrame(vTS, flvVideoCodecHEVC, key, 0, packLengthPrefixedNALUs(4, nalu))
	}

	// Switch SPS to 960x540 on the SAME FLV stream!
	fb.writeClassicVideoConfig(45*33, flvVideoCodecHEVC, buildHEVCDecConfBytes(t, vps, sps2, pps, 4))
	for i := 45; i < 90; i++ {
		vTS := uint32(i * 33)
		for audioTS <= vTS {
			fb.writeAACFrame(audioTS, testAACSilentFrame)
			audioTS += 21
		}
		key := (i-45)%30 == 0
		nalu := trail
		if key {
			nalu = idr
		}
		fb.writeClassicVideoFrame(vTS, flvVideoCodecHEVC, key, 0, packLengthPrefixedNALUs(4, nalu))
	}
	return fb.buf.Bytes()
}

func setupLiveTestHarness(t *testing.T, handler http.Handler) (*Manager, *library.Store, *subscription.Manager, *httptest.Server, string) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	tmp := t.TempDir()
	db, _, _, err := storage.OpenSQLite(filepath.Join(tmp, "library.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cacheDir := filepath.Join(tmp, "cache")
	lib, err := library.Open(db, cacheDir)
	if err != nil {
		t.Fatalf("library.Open: %v", err)
	}
	cred, _, err := credential.Open(filepath.Join(tmp, "cred.dpapi"))
	if err != nil {
		t.Fatalf("credential.Open: %v", err)
	}
	_, _ = cred.SaveManual("SESSDATA=bili_live_secret; bili_jct=bili_csrf_secret; DedeUserID=1001", "rt_secret")

	client := bilibili.NewClient(bilibili.ClientOptions{
		CredStore:      cred,
		Endpoints:      bilibili.Endpoints{APIBase: srv.URL, LiveAPIBase: srv.URL, LiveWebBase: srv.URL},
		PacingInterval: -1,
		AllowLocalhost: true,
	})
	subMgr, err := subscription.NewManager(db, lib, nil, client, settings.Default(cacheDir).Subscription, nil)
	if err != nil {
		t.Fatalf("subscription.NewManager: %v", err)
	}

	liveCfg := settings.Default(cacheDir).Live
	liveCfg.MaxConcurrentRecordings = 3
	liveMgr := NewManager(lib, subMgr, client, liveCfg, nil, nil)
	liveMgr.hlsPollInterval = 25 * time.Millisecond
	liveMgr.reconnectWait = 20 * time.Millisecond
	liveMgr.freeBytesFunc = func(string) (uint64, error) { return 10 << 30, nil }
	t.Cleanup(liveMgr.Stop)
	return liveMgr, lib, subMgr, srv, cacheDir
}

func waitUntilRoomIdle(t *testing.T, mgr *Manager, roomKey string, timeout time.Duration) RoomState {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, r := range mgr.ListRooms() {
			if (r.RoomKey == roomKey || r.RoomIDStr == roomKey) && !r.Recording {
				return r
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for room %q to finish recording (rooms=%+v)", roomKey, mgr.ListRooms())
	return RoomState{}
}

func TestDouyinHTTPFLVEndToEndCodecsReconnectRolloverAndOrphanRecovery(t *testing.T) {
	const (
		secUID   = "MS4wLjABAAAA_live_creator_001"
		sigQuery = "?x-expires=1999999999&token=LIVE_SIG_SENTINEL"
	)

	avcFLV := buildSyntheticFLVBytes(t, "avc", 45)
	hevc12FLV := buildSyntheticFLVBytes(t, "hevc12", 45)
	enhFLV := buildSyntheticFLVBytes(t, "enhanced_hvc1", 45)
	paramChangeFLV := buildSyntheticParamChangeFLVBytes(t)
	jpegCover := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01, 0x01, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0xFF, 0xD9}

	var sawCookieLeak atomic.Bool
	var v1Hits atomic.Int32
	var v2Hits atomic.Int32
	var paramChangeHits atomic.Int32
	holdDone := make(chan struct{})
	holdStarted := make(chan struct{}, 1)
	biliHoldDone := make(chan struct{})
	biliHoldStarted := make(chan struct{}, 1)

	vM4S, err := download.BuildSyntheticVideoM4S(30, 30)
	if err != nil {
		t.Fatalf("BuildSyntheticVideoM4S: %v", err)
	}
	parsedBili, _ := mp4.DecodeFile(bytes.NewReader(vM4S), mp4.WithDecodeFlags(mp4.DecStartOnMoof))
	var biliInit, biliFrag bytes.Buffer
	_ = parsedBili.Init.Encode(&biliInit)
	_ = parsedBili.Segments[0].Fragments[0].Encode(&biliFrag)

	var srvURL string
	mux := http.NewServeMux()
	checkFLVHeaders := func(r *http.Request) {
		if r.Header.Get("Cookie") != "" {
			sawCookieLeak.Store(true)
		}
	}
	mux.HandleFunc("/cdn/cover.jpg", func(w http.ResponseWriter, r *http.Request) {
		checkFLVHeaders(r)
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(jpegCover)
	})
	mux.HandleFunc("/live/avc.flv", func(w http.ResponseWriter, r *http.Request) {
		checkFLVHeaders(r)
		w.Header().Set("Content-Type", "video/x-flv")
		_, _ = w.Write(avcFLV)
	})
	mux.HandleFunc("/live/hevc12.flv", func(w http.ResponseWriter, r *http.Request) {
		checkFLVHeaders(r)
		w.Header().Set("Content-Type", "video/x-flv")
		_, _ = w.Write(hevc12FLV)
	})
	mux.HandleFunc("/live/enh_hvc1.flv", func(w http.ResponseWriter, r *http.Request) {
		checkFLVHeaders(r)
		w.Header().Set("Content-Type", "video/x-flv")
		_, _ = w.Write(enhFLV)
	})
	mux.HandleFunc("/live/reconnect-v1.flv", func(w http.ResponseWriter, r *http.Request) {
		checkFLVHeaders(r)
		v1Hits.Add(1)
		w.Header().Set("Content-Type", "video/x-flv")
		_, _ = w.Write(avcFLV)
	})
	mux.HandleFunc("/live/reconnect-v2.flv", func(w http.ResponseWriter, r *http.Request) {
		checkFLVHeaders(r)
		v2Hits.Add(1)
		w.Header().Set("Content-Type", "video/x-flv")
		_, _ = w.Write(avcFLV)
	})
	mux.HandleFunc("/live/param-change.flv", func(w http.ResponseWriter, r *http.Request) {
		checkFLVHeaders(r)
		paramChangeHits.Add(1)
		w.Header().Set("Content-Type", "video/x-flv")
		_, _ = w.Write(paramChangeFLV)
	})
	mux.HandleFunc("/live/hold.flv", func(w http.ResponseWriter, r *http.Request) {
		checkFLVHeaders(r)
		w.Header().Set("Content-Type", "video/x-flv")
		_, _ = w.Write(avcFLV)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case holdStarted <- struct{}{}:
		default:
		}
		select {
		case <-holdDone:
		case <-r.Context().Done():
		}
	})

	// Bilibili active recording endpoints for StopForSite isolation test.
	mux.HandleFunc("/room/v1/Room/get_status_info_by_uids", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"1001": map[string]any{"uid": 1001, "room_id": 8899, "title": "B站直播 8899", "uname": "BiliStreamer", "live_status": 1, "live_time": 1794024000},
			},
		})
	})
	mux.HandleFunc("/room/v1/Room/room_init", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0, "data": map[string]any{"room_id": 8899, "uid": 1001, "live_status": 1, "live_time": 1794024000},
		})
	})
	mux.HandleFunc("/xlive/web-room/v2/index/getRoomPlayInfo", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"room_id": 8899, "uid": 1001, "live_status": 1,
				"playurl_info": map[string]any{"playurl": map[string]any{"stream": []map[string]any{{
					"protocol_name": "http_hls",
					"format": []map[string]any{{
						"format_name": "fmp4",
						"codec": []map[string]any{{
							"codec_name": "avc", "current_qn": 10000, "base_url": "/bili/live.m3u8",
							"url_info": []map[string]string{{"host": srvURL, "extra": ""}},
						}},
					}},
				}}}},
			},
		})
	})
	mux.HandleFunc("/bili/live.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		select {
		case biliHoldStarted <- struct{}{}:
		default:
		}
		endLine := ""
		select {
		case <-biliHoldDone:
			endLine = "#EXT-X-ENDLIST\n"
		default:
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:1\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:1.0,\nseg1.m4s\n" + endLine))
	})
	mux.HandleFunc("/bili/init.mp4", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(biliInit.Bytes()) })
	mux.HandleFunc("/bili/seg1.m4s", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(biliFrag.Bytes()) })

	liveMgr, libStore, _, srv, cacheDir := setupLiveTestHarness(t, mux)
	srvURL = srv.URL
	provider := newFakeDouyinLiveProvider()
	liveMgr.RegisterSiteProvider(provider)
	transport := site.DouyinLiveTransport("Mozilla/5.0 Test", true)

	// 1. End-to-end AVC, CodecID=12 HEVC, and Enhanced FLV hvc1 -> progressive part-001.mp4!
	for _, tc := range []struct {
		roomID  string
		webRID  string
		codec   string
		flvPath string
	}{
		{roomID: "7400000000000000101", webRID: "100101", codec: "avc", flvPath: "/live/avc.flv"},
		{roomID: "7400000000000000102", webRID: "100102", codec: "hevc", flvPath: "/live/hevc12.flv"},
		{roomID: "7400000000000000103", webRID: "100103", codec: "hevc", flvPath: "/live/enh_hvc1.flv"},
	} {
		provider.statusFn = func(uid, cachedWebRID string, call int) (site.LiveStatus, error) {
			st := site.LiveStateLive
			if call >= 2 {
				st = site.LiveStateOffline
			}
			return site.LiveStatus{
				Site: site.Douyin, AccountUID: secUID, WebRID: tc.webRID, RoomID: tc.roomID,
				Status: st, Title: "直播测试 " + tc.codec, Uname: "抖音主播",
				Cover: srv.URL + "/cdn/cover.jpg" + sigQuery, LiveSince: 1700010000,
			}, nil
		}
		provider.streamFn = func(uid, roomOrWebRID string, _ []string, _ int) (site.LiveStreamPlan, error) {
			return site.LiveStreamPlan{
				Site: site.Douyin, AccountUID: secUID, WebRID: tc.webRID, RoomID: tc.roomID,
				Status: site.LiveStateLive, Transport: site.LiveTransportHTTPFLV,
				Protocol: "http_stream", Format: "flv", Codec: tc.codec,
				Quality: 10000, QualityName: "原画",
				URLs:  []string{srv.URL + tc.flvPath + sigQuery},
				Media: transport,
			}, nil
		}

		stStart, err := liveMgr.StartDouyinRecording(context.Background(), secUID, tc.webRID)
		if err != nil || !stStart.Recording {
			t.Fatalf("StartDouyinRecording(%s): st=%+v err=%v", tc.codec, stStart, err)
		}
		stDone := waitUntilRoomIdle(t, liveMgr, "douyin:live:"+tc.roomID, 6*time.Second)
		if stDone.StopReason != "ended" || stDone.RecordSegments != 1 {
			t.Fatalf("unexpected finished state for %s: %+v", tc.codec, stDone)
		}
		if sawCookieLeak.Load() {
			t.Fatal("Douyin FLV request leaked Cookie header")
		}
		wantSuffix := filepath.Join("douyin", site.DouyinAuthorDir(secUID), "lives", tc.roomID)
		if !strings.HasSuffix(stDone.RecordItemDir, wantSuffix) {
			t.Fatalf("unexpected RecordItemDir %q, want suffix %q", stDone.RecordItemDir, wantSuffix)
		}
		partSummary, err := download.InspectMP4File(filepath.Join(stDone.RecordItemDir, "part-001.mp4"))
		if err != nil || !partSummary.MoovBeforeMdat || partSummary.DurationSec < 1.0 {
			t.Fatalf("invalid finalized part-001.mp4 for %s: summary=%+v err=%v", tc.codec, partSummary, err)
		}
		rawItem, _ := os.ReadFile(filepath.Join(stDone.RecordItemDir, "item.json"))
		if bytes.Contains(rawItem, []byte("LIVE_SIG_SENTINEL")) || bytes.Contains(rawItem, []byte("x-expires")) {
			t.Fatalf("signed URL leaked into live item.json: %s", string(rawItem))
		}
	}

	// 2. Mid-stream disconnect re-calls SelectLiveStream (fresh v2 URL, never reusing v1) -> part-001 + part-002!
	const reconnRoom = "7400000000000000201"
	provider.mu.Lock()
	provider.statusCalls = make(map[string]int)
	provider.streamCalls = make(map[string]int)
	provider.mu.Unlock()
	provider.statusFn = func(_, _ string, call int) (site.LiveStatus, error) {
		st := site.LiveStateLive
		if call >= 3 { // call 1 = Start, call 2 = reconnect #1 (still live), call 3 = reconnect #2 (ended)
			st = site.LiveStateOffline
		}
		return site.LiveStatus{
			Site: site.Douyin, AccountUID: secUID, WebRID: "200201", RoomID: reconnRoom,
			Status: st, Title: "断流重连测试", Uname: "抖音主播", LiveSince: 1700020000,
		}, nil
	}
	provider.streamFn = func(_, _ string, _ []string, call int) (site.LiveStreamPlan, error) {
		u := srv.URL + "/live/reconnect-v1.flv" + sigQuery
		if call >= 2 {
			u = srv.URL + "/live/reconnect-v2.flv" + sigQuery
		}
		return site.LiveStreamPlan{
			Site: site.Douyin, AccountUID: secUID, WebRID: "200201", RoomID: reconnRoom,
			Status: site.LiveStateLive, Transport: site.LiveTransportHTTPFLV,
			Format: "flv", Codec: "avc", QualityName: "原画", URLs: []string{u}, Media: transport,
		}, nil
	}
	if _, err := liveMgr.StartDouyinRecording(context.Background(), secUID, "200201"); err != nil {
		t.Fatalf("StartDouyinRecording(reconnect): %v", err)
	}
	reconnDone := waitUntilRoomIdle(t, liveMgr, "douyin:live:"+reconnRoom, 6*time.Second)
	if v1Hits.Load() != 1 || v2Hits.Load() != 1 || reconnDone.RecordSegments != 2 {
		t.Fatalf("expected 1 hit on v1 + 1 hit on v2 + 2 segments, got v1=%d v2=%d state=%+v", v1Hits.Load(), v2Hits.Load(), reconnDone)
	}

	// 3. Same-connection SPS/PPS parameter-set change rolls over into part-001 (640x360) + part-002 (960x540) on 1 HTTP request!
	const paramRoom = "7400000000000000202"
	provider.mu.Lock()
	provider.statusCalls = make(map[string]int)
	provider.streamCalls = make(map[string]int)
	provider.mu.Unlock()
	provider.statusFn = func(_, _ string, call int) (site.LiveStatus, error) {
		st := site.LiveStateLive
		if call >= 2 {
			st = site.LiveStateOffline
		}
		return site.LiveStatus{
			Site: site.Douyin, AccountUID: secUID, WebRID: "200202", RoomID: paramRoom,
			Status: st, Title: "参数集切换分段测试", Uname: "抖音主播", LiveSince: 1700020200,
		}, nil
	}
	provider.streamFn = func(_, _ string, _ []string, _ int) (site.LiveStreamPlan, error) {
		return site.LiveStreamPlan{
			Site: site.Douyin, AccountUID: secUID, WebRID: "200202", RoomID: paramRoom,
			Status: site.LiveStateLive, Transport: site.LiveTransportHTTPFLV,
			Format: "flv", Codec: "hevc", QualityName: "原画",
			URLs: []string{srv.URL + "/live/param-change.flv"}, Media: transport,
		}, nil
	}
	if _, err := liveMgr.StartDouyinRecording(context.Background(), secUID, "200202"); err != nil {
		t.Fatalf("StartDouyinRecording(paramChange): %v", err)
	}
	paramDone := waitUntilRoomIdle(t, liveMgr, "douyin:live:"+paramRoom, 6*time.Second)
	if paramChangeHits.Load() != 1 || paramDone.RecordSegments != 2 {
		t.Fatalf("expected single HTTP connection with 2 segments on param change, got hits=%d state=%+v", paramChangeHits.Load(), paramDone)
	}
	s1, err1 := download.InspectMP4File(filepath.Join(paramDone.RecordItemDir, "part-001.mp4"))
	s2, err2 := download.InspectMP4File(filepath.Join(paramDone.RecordItemDir, "part-002.mp4"))
	if err1 != nil || err2 != nil || s1.Width != 640 || s2.Width != 960 {
		t.Fatalf("expected 640x360 -> 960x540 split parts, got s1=%+v(%v) s2=%+v(%v)", s1, err1, s2, err2)
	}

	// 4. Audio-only & HLS-only Douyin rooms rejected with clear Chinese messages.
	provider.statusFn = func(_, _ string, _ int) (site.LiveStatus, error) {
		return site.LiveStatus{
			Site: site.Douyin, AccountUID: secUID, WebRID: "200203", RoomID: "7400000000000000203",
			Status: site.LiveStateAudioOnly, Title: "语音电台", Uname: "抖音主播",
		}, nil
	}
	provider.streamFn = func(_, _ string, _ []string, _ int) (site.LiveStreamPlan, error) {
		return site.LiveStreamPlan{
			Site: site.Douyin, AccountUID: secUID, WebRID: "200203", RoomID: "7400000000000000203",
			Status: site.LiveStateAudioOnly, Transport: site.LiveTransportHTTPFLV, Media: transport,
		}, nil
	}
	if _, err := liveMgr.StartDouyinRecording(context.Background(), secUID, "200203"); err == nil || !strings.Contains(err.Error(), "语音直播") {
		t.Fatalf("expected clear Chinese audio-only rejection, got %v", err)
	}

	provider.statusFn = func(_, _ string, _ int) (site.LiveStatus, error) {
		return site.LiveStatus{
			Site: site.Douyin, AccountUID: secUID, WebRID: "200204", RoomID: "7400000000000000204",
			Status: site.LiveStateLive, Title: "仅 HLS 直播", Uname: "抖音主播",
		}, nil
	}
	provider.streamFn = func(_, _ string, _ []string, _ int) (site.LiveStreamPlan, error) {
		return site.LiveStreamPlan{
			Site: site.Douyin, AccountUID: secUID, WebRID: "200204", RoomID: "7400000000000000204",
			Status: site.LiveStateLive, Transport: site.LiveTransportHLSFMP4,
			Format: "ts", URLs: []string{srv.URL + "/live/only.m3u8"}, Media: transport,
		}, nil
	}
	if _, err := liveMgr.StartDouyinRecording(context.Background(), secUID, "200204"); err == nil || !strings.Contains(err.Error(), "HTTP-FLV") {
		t.Fatalf("expected clear Chinese HLS-only rejection, got %v", err)
	}

	// 5. Simulated crash leaving part-001.mp4.part + trailing truncated bytes -> RecoverOrphanParts recovers valid part-001.mp4!
	const orphanRoom = "7400000000000000205"
	orphanDir := filepath.Join(cacheDir, "douyin", site.DouyinAuthorDir(secUID), "lives", orphanRoom)
	if err := os.MkdirAll(orphanDir, 0o700); err != nil {
		t.Fatalf("MkdirAll orphanDir: %v", err)
	}
	orphanPartPath := filepath.Join(orphanDir, "part-001.mp4.part")
	pf, err := os.OpenFile(orphanPartPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("OpenFile orphanPartPath: %v", err)
	}
	cp, err := NewFLVRemuxer(bytes.NewReader(avcFLV), FLVRemuxOptions{}).WritePart(context.Background(), pf, nil)
	if err != nil {
		_ = pf.Close()
		t.Fatalf("WritePart orphan: %v", err)
	}
	// Append uncheckpointed truncated tail bytes after PendingBytes!
	_, _ = pf.Write([]byte("TRUNCATED_UNFLUSHED_MOOF_GARBAGE_BYTES"))
	_ = pf.Close()

	_, err = libStore.SaveItem(orphanDir, library.ItemMetadata{
		Version:      1,
		ID:           "live:" + orphanRoom,
		Site:         "douyin",
		Kind:         "live",
		Status:       "recording",
		Title:        "崩溃恢复测试",
		Author:       library.ItemAuthor{Site: "douyin", UID: secUID, Name: "抖音主播"},
		PendingParts: []string{"part-001.mp4.part"},
		Live: &library.LiveRecordingInfo{
			RoomIDStr:       orphanRoom,
			PendingFile:     "part-001.mp4.part",
			PendingBytes:    cp.BytesWritten,
			PendingDuration: cp.DurationSec,
			Codec:           "avc",
			QualityName:     "原画",
		},
	})
	if err != nil {
		t.Fatalf("SaveItem orphan: %v", err)
	}
	liveMgr.RecoverOrphanParts()

	recoveredMeta, ok, err := libStore.GetItem("douyin:live:" + orphanRoom)
	if err != nil || !ok || recoveredMeta.Status != "complete" || len(recoveredMeta.PendingParts) != 0 {
		t.Fatalf("expected orphan part recovered to complete, got ok=%v err=%v meta=%+v", ok, err, recoveredMeta)
	}
	recSummary, err := download.InspectMP4File(filepath.Join(orphanDir, "part-001.mp4"))
	if err != nil || !recSummary.MoovBeforeMdat {
		t.Fatalf("expected recovered part-001.mp4 to have moov-before-mdat, got summary=%+v err=%v", recSummary, err)
	}

	// 6. StopForSite(site.Douyin, "auth_required") stops Douyin recording without stopping active Bilibili recording!
	provider.statusFn = func(_, _ string, _ int) (site.LiveStatus, error) {
		return site.LiveStatus{
			Site: site.Douyin, AccountUID: secUID, WebRID: "200206", RoomID: "7400000000000000206",
			Status: site.LiveStateLive, Title: "跨站停止隔离测试", Uname: "抖音主播", LiveSince: 1700020600,
		}, nil
	}
	provider.streamFn = func(_, _ string, _ []string, _ int) (site.LiveStreamPlan, error) {
		return site.LiveStreamPlan{
			Site: site.Douyin, AccountUID: secUID, WebRID: "200206", RoomID: "7400000000000000206",
			Status: site.LiveStateLive, Transport: site.LiveTransportHTTPFLV,
			Format: "flv", Codec: "avc", QualityName: "原画",
			URLs: []string{srv.URL + "/live/hold.flv"}, Media: transport,
		}, nil
	}
	if _, err := liveMgr.StartRecording(context.Background(), 8899); err != nil {
		t.Fatalf("StartRecording(bili 8899): %v", err)
	}
	if _, err := liveMgr.StartDouyinRecording(context.Background(), secUID, "200206"); err != nil {
		t.Fatalf("StartDouyinRecording(hold): %v", err)
	}
	<-biliHoldStarted
	<-holdStarted

	liveMgr.StopForSite(site.Douyin, "auth_required")
	close(holdDone)

	dyStopped := waitUntilRoomIdle(t, liveMgr, "douyin:live:7400000000000000206", 5*time.Second)
	if dyStopped.StopReason != "auth_required" {
		t.Fatalf("expected Douyin StopReason=auth_required, got %+v", dyStopped)
	}
	biliStillActive := false
	for _, r := range liveMgr.ListRooms() {
		if r.RoomID == 8899 && r.Recording {
			biliStillActive = true
		}
	}
	if !biliStillActive {
		t.Fatalf("StopForSite(Douyin) must NOT stop active Bilibili recording, rooms=%+v", liveMgr.ListRooms())
	}
	close(biliHoldDone)
	_, _ = liveMgr.StopRecording(8899)
}

func TestBilibiliHTTPFLVFallbackRecordingEndToEnd(t *testing.T) {
	avcFLV := buildSyntheticFLVBytes(t, "avc", 45)
	var roomLive atomic.Bool
	roomLive.Store(true)

	var srvURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/room/v1/Room/get_status_info_by_uids", func(w http.ResponseWriter, _ *http.Request) {
		st := 0
		if roomLive.Load() {
			st = 1
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"1001": map[string]any{"uid": 1001, "room_id": 9501, "title": "B站 FLV 直播", "uname": "BiliFLVStreamer", "live_status": st, "live_time": 1795000000},
			},
		})
	})
	mux.HandleFunc("/room/v1/Room/room_init", func(w http.ResponseWriter, _ *http.Request) {
		st := 0
		if roomLive.Load() {
			st = 1
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"room_id": 9501, "uid": 1001, "live_status": st, "live_time": 1795000000},
		})
	})
	mux.HandleFunc("/xlive/web-room/v2/index/getRoomPlayInfo", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"room_id": 9501, "uid": 1001, "live_status": 1,
				"playurl_info": map[string]any{
					"playurl": map[string]any{
						"g_qn_desc": []map[string]any{{"qn": 10000, "desc": "原画"}},
						"stream": []map[string]any{{
							"protocol_name": "http_stream",
							"format": []map[string]any{{
								"format_name": "flv",
								"codec": []map[string]any{{
									"codec_name": "avc",
									"current_qn": 10000,
									"base_url":   "/live-bvc/room9501.flv",
									"url_info":   []map[string]string{{"host": srvURL, "extra": "?tok=flv_only"}},
								}},
							}},
						}},
					},
				},
			},
		})
	})
	mux.HandleFunc("/live-bvc/room9501.flv", func(w http.ResponseWriter, _ *http.Request) {
		// After serving the FLV stream once, mark room offline so reconnect cleanly finishes as ended.
		roomLive.Store(false)
		w.Header().Set("Content-Type", "video/x-flv")
		_, _ = w.Write(avcFLV)
	})

	liveMgr, libStore, _, srv, _ := setupLiveTestHarness(t, mux)
	srvURL = srv.URL

	st, err := liveMgr.StartRecording(context.Background(), 9501)
	if err != nil || !st.Recording {
		t.Fatalf("StartRecording(9501 FLV fallback): st=%+v err=%v", st, err)
	}

	deadline := time.Now().Add(6 * time.Second)
	var finalRoom RoomState
	for time.Now().Before(deadline) {
		for _, r := range liveMgr.ListRooms() {
			if r.RoomID == 9501 && !r.Recording {
				finalRoom = r
				goto done
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for Bilibili FLV recording 9501 to finish: %+v", liveMgr.ListRooms())

done:
	if finalRoom.StopReason != "ended" || finalRoom.RecordSegments != 1 {
		t.Fatalf("unexpected Bilibili FLV recording final state: %+v", finalRoom)
	}
	summary, err := download.InspectMP4File(filepath.Join(finalRoom.RecordItemDir, "part-001.mp4"))
	if err != nil || !summary.MoovBeforeMdat || summary.DurationSec < 1.0 {
		t.Fatalf("invalid Bilibili FLV finalized part-001.mp4: summary=%+v err=%v", summary, err)
	}
	meta, ok, err := libStore.GetItem(library.CanonicalItemKey("bilibili", "live", st.RecordSession))
	if err != nil || !ok || meta.Status != "complete" {
		t.Fatalf("expected complete library item for Bilibili FLV session %q, got ok=%v err=%v meta=%+v", st.RecordSession, ok, err, meta)
	}
}

func TestDouyinLiveStatusLazyCacheWebRIDSyncRoomIdentityAndAutoPoll(t *testing.T) {
	const (
		secAuto   = "MS4wLjABAAAA_auto_creator_010"
		secManual = "MS4wLjABAAAA_manual_creator_011"
		roomFixed = "7400000000000009001"
	)

	avcFLV := buildSyntheticFLVBytes(t, "avc", 45)
	autoHoldDone := make(chan struct{})
	autoHoldStarted := make(chan struct{}, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/live/short.flv", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/x-flv")
		_, _ = w.Write(avcFLV)
	})
	mux.HandleFunc("/live/auto-hold.flv", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/x-flv")
		_, _ = w.Write(avcFLV)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case autoHoldStarted <- struct{}{}:
		default:
		}
		select {
		case <-autoHoldDone:
		case <-r.Context().Done():
		}
	})

	liveMgr, libStore, subMgr, srv, _ := setupLiveTestHarness(t, mux)
	provider := newFakeDouyinLiveProvider()
	liveMgr.RegisterSiteProvider(provider)
	transport := site.DouyinLiveTransport("Mozilla/5.0 Test", true)

	curNow := time.Unix(1700100000, 0).UTC()
	var nowMu sync.Mutex
	liveMgr.nowFunc = func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return curNow
	}
	advanceClock := func(d time.Duration) {
		nowMu.Lock()
		curNow = curNow.Add(d)
		nowMu.Unlock()
	}

	// Insert two Douyin subscriptions: secAuto (LiveAutoRecord=true) and secManual (LiveAutoRecord=false).
	if _, err := subMgr.Upsert(subscription.Subscription{
		Name:           "自动录播创作者",
		Enabled:        true,
		LiveAutoRecord: true,
		Accounts: []subscription.AccountRef{{
			Site: string(site.Douyin), UID: secAuto, Name: "自动主播",
		}},
	}); err != nil {
		t.Fatalf("Upsert secAuto: %v", err)
	}
	if _, err := subMgr.Upsert(subscription.Subscription{
		Name:           "普通订阅创作者",
		Enabled:        true,
		LiveAutoRecord: false,
		Accounts: []subscription.AccountRef{{
			Site: string(site.Douyin), UID: secManual, Name: "手动主播",
		}},
	}); err != nil {
		t.Fatalf("Upsert secManual: %v", err)
	}

	var synced []struct {
		uid         string
		webRID      string
		invalidated bool
	}
	var syncMu sync.Mutex
	liveMgr.SetWebRIDSyncFunc(func(_ site.ID, uid, webRID string, invalidated bool) {
		syncMu.Lock()
		synced = append(synced, struct {
			uid         string
			webRID      string
			invalidated bool
		}{uid: uid, webRID: webRID, invalidated: invalidated})
		syncMu.Unlock()
	})

	// 1. Subscription grid read (CachedStatesByAccountKeys + ListRooms + ObserveAccountLiveHint) MUST issue 0 network calls!
	_ = liveMgr.CachedStatesByAccountKeys([]string{"douyin:" + secAuto, "douyin:" + secManual})
	_ = liveMgr.ListRooms()
	liveMgr.ObserveAccountLiveHint(site.Douyin, secManual, "111222333", roomFixed)
	if got := provider.totalStatusCalls(); got != 0 {
		t.Fatalf("expected 0 CheckLiveStatus calls from cached grid & passive hint, got %d", got)
	}
	cachedSt, ok := liveMgr.CachedAccountState(site.Douyin, secManual)
	if !ok || cachedSt.WebRID != "111222333" || cachedSt.LiveStatus != 1 {
		t.Fatalf("expected passive live hint reflected in CachedAccountState, got ok=%v st=%+v", ok, cachedSt)
	}

	// 2. CheckAccountLiveStatus / StatusByAccountKeys uses 60s TTL cache and syncs backfilled & invalidated webRid!
	var invalidateNext atomic.Bool
	provider.statusFn = func(uid, cachedWebRID string, _ int) (site.LiveStatus, error) {
		if invalidateNext.Load() {
			return site.LiveStatus{
				Site: site.Douyin, AccountUID: uid, Status: site.LiveStateOffline, InvalidatedWebRID: true,
			}, nil
		}
		return site.LiveStatus{
			Site: site.Douyin, AccountUID: uid, WebRID: "555666777", RoomID: roomFixed,
			Status: site.LiveStateLive, Title: "60s 缓存校验", Uname: "手动主播", LiveSince: 1700100000,
		}, nil
	}

	_, err := liveMgr.StatusByAccountKeys(context.Background(), []string{"douyin:" + secManual})
	if err != nil {
		t.Fatalf("StatusByAccountKeys #1: %v", err)
	}
	// Within 60s TTL, calling again must issue 0 extra requests!
	advanceClock(30 * time.Second)
	_, err = liveMgr.StatusByAccountKeys(context.Background(), []string{"douyin:" + secManual})
	if err != nil {
		t.Fatalf("StatusByAccountKeys #2 (within TTL): %v", err)
	}
	if got := provider.statusCallCount(secManual); got != 1 {
		t.Fatalf("expected 1 CheckLiveStatus call within 60s TTL, got %d", got)
	}

	// After 61s, TTL expires; test InvalidatedWebRID clearing webRid via WebRIDSyncFunc!
	advanceClock(31 * time.Second)
	invalidateNext.Store(true)
	stAfterInvalid, err := liveMgr.CheckAccountLiveStatus(context.Background(), site.Douyin, secManual, "555666777", false)
	if err != nil {
		t.Fatalf("CheckAccountLiveStatus after TTL: %v", err)
	}
	if stAfterInvalid.WebRID != "" {
		t.Fatalf("expected invalidated webRid cleared on RoomState, got %q", stAfterInvalid.WebRID)
	}
	syncMu.Lock()
	sawBackfill := false
	sawInvalidate := false
	for _, ev := range synced {
		if ev.uid == secManual && ev.webRID == "555666777" && !ev.invalidated {
			sawBackfill = true
		}
		if ev.uid == secManual && ev.webRID == "" && ev.invalidated {
			sawInvalidate = true
		}
	}
	syncMu.Unlock()
	if !sawBackfill || !sawInvalidate {
		t.Fatalf("expected both backfill and invalidation sync callbacks, got %+v", synced)
	}

	// 3. Room identity: changing webRid for the SAME room_id reuses douyin:live:<room_id> and appends part-002.mp4!
	invalidateNext.Store(false)
	curWebRID := "111222333"
	var streamLive atomic.Bool
	streamLive.Store(true)
	provider.statusFn = func(uid, _ string, _ int) (site.LiveStatus, error) {
		st := site.LiveStateOffline
		if streamLive.Load() {
			st = site.LiveStateLive
		}
		return site.LiveStatus{
			Site: site.Douyin, AccountUID: uid, WebRID: curWebRID, RoomID: roomFixed,
			Status: st, Title: "同一场直播切换 webRid", Uname: "手动主播", LiveSince: 1700100100,
		}, nil
	}
	provider.streamFn = func(uid, _ string, _ []string, _ int) (site.LiveStreamPlan, error) {
		streamLive.Store(false) // finish after 1 part
		return site.LiveStreamPlan{
			Site: site.Douyin, AccountUID: uid, WebRID: curWebRID, RoomID: roomFixed,
			Status: site.LiveStateLive, Transport: site.LiveTransportHTTPFLV,
			Format: "flv", Codec: "avc", QualityName: "原画",
			URLs: []string{srv.URL + "/live/short.flv"}, Media: transport,
		}, nil
	}

	stRun1, err := liveMgr.StartDouyinRecording(context.Background(), secManual, "111222333")
	if err != nil {
		t.Fatalf("StartDouyinRecording run1: %v", err)
	}
	doneRun1 := waitUntilRoomIdle(t, liveMgr, "douyin:live:"+roomFixed, 6*time.Second)

	// Now webRid changes to "999888777", but room_id is STILL roomFixed!
	curWebRID = "999888777"
	streamLive.Store(true)
	stRun2, err := liveMgr.StartDouyinRecording(context.Background(), secManual, "999888777")
	if err != nil {
		t.Fatalf("StartDouyinRecording run2 (changed webRid): %v", err)
	}
	doneRun2 := waitUntilRoomIdle(t, liveMgr, "douyin:live:"+roomFixed, 6*time.Second)

	if stRun1.RecordItemDir != stRun2.RecordItemDir || doneRun1.RecordItemDir != doneRun2.RecordItemDir {
		t.Fatalf("expected identical RecordItemDir across webRid change: run1=%q run2=%q", stRun1.RecordItemDir, stRun2.RecordItemDir)
	}
	if _, err := os.Stat(filepath.Join(doneRun2.RecordItemDir, "part-001.mp4")); err != nil {
		t.Fatalf("missing part-001.mp4: %v", err)
	}
	if _, err := os.Stat(filepath.Join(doneRun2.RecordItemDir, "part-002.mp4")); err != nil {
		t.Fatalf("missing part-002.mp4 on reused room_id item: %v", err)
	}
	liveItems, err := libStore.ListItems(library.FeedFilter{Site: "douyin", Kind: "live", Limit: 50})
	if err != nil || len(liveItems) != 1 || liveItems[0].ItemKey != "douyin:live:"+roomFixed {
		t.Fatalf("expected exactly 1 library item for room_id across webRid change, got err=%v items=%+v", err, liveItems)
	}

	// 4. PollDouyinAutoRecordNow: polls ONLY AutoRecord=true accounts, enforces >=90s per-account interval, and stops ended sessions via CheckRoomsAlive!
	provider.mu.Lock()
	provider.statusCalls = make(map[string]int)
	provider.aliveCalls = 0
	provider.mu.Unlock()

	const autoRoom = "7400000000000009099"
	var autoRoomAlive atomic.Bool
	autoRoomAlive.Store(true)

	provider.statusFn = func(uid, _ string, _ int) (site.LiveStatus, error) {
		return site.LiveStatus{
			Site: site.Douyin, AccountUID: uid, WebRID: "777000111", RoomID: autoRoom,
			Status: site.LiveStateLive, Title: "自动录播测试", Uname: "自动主播", LiveSince: 1700105000,
		}, nil
	}
	provider.streamFn = func(uid, _ string, _ []string, _ int) (site.LiveStreamPlan, error) {
		return site.LiveStreamPlan{
			Site: site.Douyin, AccountUID: uid, WebRID: "777000111", RoomID: autoRoom,
			Status: site.LiveStateLive, Transport: site.LiveTransportHTTPFLV,
			Format: "flv", Codec: "avc", QualityName: "原画",
			URLs: []string{srv.URL + "/live/auto-hold.flv"}, Media: transport,
		}, nil
	}
	provider.aliveFn = func(roomIDs []string, _ int) (map[string]bool, error) {
		out := make(map[string]bool, len(roomIDs))
		for _, id := range roomIDs {
			out[id] = autoRoomAlive.Load()
		}
		return out, nil
	}

	// Poll #1: checks secAuto ONLY (secManual has AutoRecord=false -> 0 calls!) and starts auto-recording!
	if _, err := liveMgr.PollDouyinAutoRecordNow(context.Background()); err != nil {
		t.Fatalf("PollDouyinAutoRecordNow #1: %v", err)
	}
	<-autoHoldStarted
	if provider.statusCallCount(secManual) != 0 {
		t.Fatalf("PollDouyinAutoRecordNow must NOT poll AutoRecord=false accounts, got %d calls", provider.statusCallCount(secManual))
	}
	if provider.statusCallCount(secAuto) != 1 {
		t.Fatalf("expected 1 status call for secAuto on poll #1, got %d", provider.statusCallCount(secAuto))
	}

	// Poll #2 after only 30s (< 90s min interval): must make 0 additional CheckLiveStatus calls!
	advanceClock(30 * time.Second)
	if _, err := liveMgr.PollDouyinAutoRecordNow(context.Background()); err != nil {
		t.Fatalf("PollDouyinAutoRecordNow #2 (<90s): %v", err)
	}
	if provider.statusCallCount(secAuto) != 1 {
		t.Fatalf("expected >=90s per-account interval enforced (still 1 call), got %d", provider.statusCallCount(secAuto))
	}

	// Poll #3: CheckRoomsAlive reports autoRoom=false -> active auto-recording stops cleanly with StopReason="ended"!
	autoRoomAlive.Store(false)
	close(autoHoldDone)
	if _, err := liveMgr.PollDouyinAutoRecordNow(context.Background()); err != nil {
		t.Fatalf("PollDouyinAutoRecordNow #3 (check_alive=false): %v", err)
	}
	autoEnded := waitUntilRoomIdle(t, liveMgr, "douyin:live:"+autoRoom, 6*time.Second)
	if autoEnded.StopReason != "ended" {
		t.Fatalf("expected auto-recording stopped via CheckRoomsAlive with StopReason=ended, got %+v", autoEnded)
	}
}

func TestDouyinLiveRecorderFollowsPublicCDNRedirect(t *testing.T) {
	client := newLiveTransportClient(site.DouyinLiveTransport("", false), time.Second)
	via := []*http.Request{mustLiveReq(t, "https://pull-flv-f11.douyincdn.com/stage/stream-1.flv")}
	hop := mustLiveReq(t, "https://edge-7.cdnvendor.example.net/stage/stream-1.flv?sign=synthetic")
	hop.Header.Set("Cookie", "sessionid=must_never_reach_cdn")
	if err := client.CheckRedirect(hop, via); err != nil {
		t.Fatalf("recorder refused a public HTTPS CDN hop: %v", err)
	}
	if hop.Header.Get("Cookie") != "" || hop.Header.Get("Referer") != "https://live.douyin.com/" {
		t.Fatalf("hop headers not normalized: %v", hop.Header)
	}
	for _, target := range []string{
		"http://edge-7.cdnvendor.example.net/stage/stream-1.flv",
		"https://203.0.113.7/stage/stream-1.flv",
		"https://edge-7.cdnvendor.example.net:8080/stage/stream-1.flv",
	} {
		if err := client.CheckRedirect(mustLiveReq(t, target), via); !site.IsKind(err, site.ErrKindBlocked) {
			t.Fatalf("recorder followed %s: %v", target, err)
		}
	}
	five := []*http.Request{via[0], via[0], via[0], via[0], via[0]}
	if err := client.CheckRedirect(mustLiveReq(t, "https://edge-7.cdnvendor.example.net/s.flv"), five); err == nil {
		t.Fatal("recorder must stop after 5 redirect hops")
	}
}

func mustLiveReq(t *testing.T, raw string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}
